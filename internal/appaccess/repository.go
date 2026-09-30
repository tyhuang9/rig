package appaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

type Repository struct {
	db                            *sql.DB
	now                           func() time.Time
	afterGatewayLock              func()
	afterGatewayClaimLock         func()
	afterCurrentClaimLookup       func()
	afterUpgradeAuthClaimRead     func()
	afterUpgradeStartupClaimsRead func()
	afterReservationLock          func()
	afterApprovalLock             func()
	beforeReservationCommit       func()
}

func New(db *sql.DB) *Repository { return &Repository{db: db, now: time.Now} }

// ConfigureGatewayProfile appends one approved desired profile and advances
// the singleton profile head with compare-and-swap. The head is not proof that
// gateway v2 was applied. A later maintenance journal and fresh attestation
// must gate both app activation and every reported URL. This method rejects a
// new desired profile while any live allocation or non-rolled-back gateway
// upgrade claim still pins the current one.
func (r *Repository) ConfigureGatewayProfile(ctx context.Context, input ConfigureGatewayInput) (GatewayProfileRevision, bool, error) {
	canonical, err := canonicalGatewaySpec(input.Spec)
	if r == nil || r.db == nil || err != nil || !validUUID(input.OperationID) || input.ExpectedRevisionNumber < 0 {
		return GatewayProfileRevision{}, false, ErrInvalidInput
	}
	specDigest, err := GatewayProfileSpecDigest(canonical)
	if err != nil || !validApproval(input.Approval, ActionConfigureGateway, specDigest) {
		return GatewayProfileRevision{}, false, ErrApprovalRequired
	}
	requestDigest, err := gatewayRequestDigest(input, canonical)
	if err != nil {
		return GatewayProfileRevision{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayProfileRevision{}, false, err
	}
	defer tx.Rollback()
	if r.afterGatewayLock != nil {
		r.afterGatewayLock()
	}
	if existing, storedDigest, lookupErr := readGatewayByOperation(ctx, tx, input.OperationID); lookupErr == nil {
		if storedDigest != requestDigest {
			return GatewayProfileRevision{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return GatewayProfileRevision{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return GatewayProfileRevision{}, false, lookupErr
	}
	if ok, err := administratorExists(ctx, tx, input.Approval.ActorID); err != nil {
		return GatewayProfileRevision{}, false, err
	} else if !ok {
		return GatewayProfileRevision{}, false, ErrApprovalRequired
	}
	if blocking, err := blockingGatewayProfileUpgradeClaimExists(ctx, tx); err != nil {
		return GatewayProfileRevision{}, false, err
	} else if blocking {
		return GatewayProfileRevision{}, false, ErrConflict
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_number FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&current); err != nil {
		return GatewayProfileRevision{}, false, err
	}
	if current != input.ExpectedRevisionNumber {
		return GatewayProfileRevision{}, false, ErrConflict
	}
	if current > 0 {
		var liveAllocations int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lan_port_allocations WHERE released_at IS NULL)`).Scan(&liveAllocations); err != nil {
			return GatewayProfileRevision{}, false, err
		}
		if liveAllocations != 0 {
			return GatewayProfileRevision{}, false, ErrConflict
		}
	}
	now := r.now().UTC()
	value := GatewayProfileRevision{
		ID:             uuid.NewString(),
		RevisionNumber: current + 1,
		OperationID:    input.OperationID,
		Spec:           canonical,
		SpecDigest:     specDigest,
		ApprovedBy:     input.Approval.ActorID,
		ApprovedAt:     now,
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,interface_id,port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.RevisionNumber, value.OperationID, requestDigest, ActionConfigureGateway, canonical.SelectedIPv4, canonical.InterfaceID, canonical.PortStart, canonical.PortEnd, specDigest, value.ApprovedBy, stamp); err != nil {
		return GatewayProfileRevision{}, false, classifyGatewayWrite(ctx, tx, input.OperationID, requestDigest, input.ExpectedRevisionNumber, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=?,updated_at=? WHERE singleton=1 AND revision_number=?`, value.ID, value.RevisionNumber, stamp, input.ExpectedRevisionNumber)
	if err != nil {
		return GatewayProfileRevision{}, false, err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return GatewayProfileRevision{}, false, ErrConflict
	}
	metadata, _ := json.Marshal(map[string]any{"revisionId": value.ID, "revisionNumber": value.RevisionNumber, "selectedIpv4": canonical.SelectedIPv4, "interfaceId": canonical.InterfaceID, "portStart": canonical.PortStart, "portEnd": canonical.PortEnd, "specDigest": specDigest})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`, value.ApprovedBy, string(ActionConfigureGateway), "lan_gateway_profile", value.ID, string(metadata), stamp); err != nil {
		return GatewayProfileRevision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GatewayProfileRevision{}, false, err
	}
	return value, true, nil
}

func (r *Repository) CurrentGatewayProfile(ctx context.Context) (GatewayProfileRevision, error) {
	if r == nil || r.db == nil {
		return GatewayProfileRevision{}, ErrInvalidInput
	}
	var id sql.NullString
	var number int64
	if err := r.db.QueryRowContext(ctx, `SELECT revision_id,revision_number FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&id, &number); err != nil {
		return GatewayProfileRevision{}, err
	}
	if number == 0 || !id.Valid {
		return GatewayProfileRevision{}, ErrNotFound
	}
	value, _, err := readGatewayRevision(ctx, r.db, id.String, number)
	return value, err
}

// ReplayGatewayProfile reads an immutable operation before a controller
// performs fresh host-interface validation. A committed exact retry remains
// identifiable even when that interface has since disappeared. New writes
// must still use ConfigureGatewayProfile after their own host preflight.
func (r *Repository) ReplayGatewayProfile(ctx context.Context, input ConfigureGatewayInput) (GatewayProfileRevision, error) {
	canonical, err := canonicalGatewaySpec(input.Spec)
	if r == nil || r.db == nil || err != nil || !validUUID(input.OperationID) || input.ExpectedRevisionNumber < 0 {
		return GatewayProfileRevision{}, ErrInvalidInput
	}
	specDigest, err := GatewayProfileSpecDigest(canonical)
	if err != nil || !validApproval(input.Approval, ActionConfigureGateway, specDigest) {
		return GatewayProfileRevision{}, ErrApprovalRequired
	}
	requestDigest, err := gatewayRequestDigest(input, canonical)
	if err != nil {
		return GatewayProfileRevision{}, err
	}
	existing, storedDigest, err := readGatewayByOperation(ctx, r.db, input.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileRevision{}, ErrNotFound
	}
	if err != nil {
		return GatewayProfileRevision{}, err
	}
	if storedDigest != requestDigest {
		return GatewayProfileRevision{}, ErrIdempotencyMismatch
	}
	return existing, nil
}

// ReserveAppAccess assigns the first available port in the current approved
// profile. It performs no network action and does not advance the access head.
func (r *Repository) ReserveAppAccess(ctx context.Context, input ReserveAppAccessInput) (Allocation, bool, error) {
	if r == nil || r.db == nil || !validUUID(input.AppID) || !validUUID(input.OperationID) || input.ExpectedRevisionNumber < 0 || !validUUID(input.GatewayProfileRevisionID) || input.GatewayProfileRevisionNumber <= 0 {
		return Allocation{}, false, ErrInvalidInput
	}
	requestDigest, err := reservationRequestDigest(input)
	if err != nil {
		return Allocation{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return Allocation{}, false, err
	}
	defer tx.Rollback()
	commit := func() error {
		if r.beforeReservationCommit != nil {
			r.beforeReservationCommit()
		}
		return tx.Commit(ctx)
	}
	if r.afterReservationLock != nil {
		r.afterReservationLock()
	}
	if existing, lookupErr := readAllocationByOperation(ctx, tx, input.OperationID); lookupErr == nil {
		if existing.ReservationDigest != requestDigest {
			return Allocation{}, false, ErrIdempotencyMismatch
		}
		if err := commit(); err != nil {
			return Allocation{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return Allocation{}, false, lookupErr
	}
	var currentAccess int64
	if err := tx.QueryRowContext(ctx, `SELECT h.revision_number FROM lan_app_access_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, input.AppID).Scan(&currentAccess); errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, false, ErrNotFound
	} else if err != nil {
		return Allocation{}, false, err
	}
	if currentAccess != input.ExpectedRevisionNumber {
		return Allocation{}, false, ErrConflict
	}
	var profileStart, profileEnd uint16
	if err := tx.QueryRowContext(ctx, `SELECT r.port_start,r.port_end FROM lan_gateway_profile_heads h JOIN lan_gateway_profile_revisions r ON r.id=h.revision_id AND r.revision_number=h.revision_number WHERE h.singleton=1 AND h.revision_id=? AND h.revision_number=?`, input.GatewayProfileRevisionID, input.GatewayProfileRevisionNumber).Scan(&profileStart, &profileEnd); errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, false, ErrConflict
	} else if err != nil {
		return Allocation{}, false, err
	}
	var liveApp int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lan_port_allocations WHERE app_id=? AND released_at IS NULL)`, input.AppID).Scan(&liveApp); err != nil {
		return Allocation{}, false, err
	}
	if liveApp != 0 {
		return Allocation{}, false, ErrConflict
	}
	now := r.now().UTC()
	var availablePort uint16
	for port := profileStart; port <= profileEnd; port++ {
		var occupied int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lan_port_allocations WHERE port=? AND released_at IS NULL)`, port).Scan(&occupied); err != nil {
			return Allocation{}, false, err
		}
		if occupied == 0 {
			availablePort = port
			break
		}
	}
	if availablePort == 0 {
		return Allocation{}, false, ErrPoolExhausted
	}
	value := Allocation{
		ID:                           uuid.NewString(),
		AppID:                        input.AppID,
		Port:                         availablePort,
		OwnerOperationID:             input.OperationID,
		ReservationDigest:            requestDigest,
		GatewayProfileRevisionID:     input.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: input.GatewayProfileRevisionNumber,
		State:                        AllocationReserved,
		ReservedAt:                   now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_port_allocations(
		id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,gateway_profile_revision_number,state,reserved_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID, value.AppID, value.Port, value.OwnerOperationID, value.ReservationDigest, value.GatewayProfileRevisionID, value.GatewayProfileRevisionNumber, value.State, formatTime(now)); err != nil {
		return Allocation{}, false, classifyImmediateTransactionError(err)
	}
	if err := commit(); err != nil {
		return Allocation{}, false, err
	}
	return value, true, nil
}

// ApproveAppAccess binds an administrator approval to the exact reserved port
// and current gateway profile, appends an immutable access revision, and CASes
// the app head. The allocation remains reserved until a later gateway unit
// proves the external route and explicitly activates it.
func (r *Repository) ApproveAppAccess(ctx context.Context, input ApproveAppAccessInput) (AppAccessRevision, bool, error) {
	if r == nil || r.db == nil || !validUUID(input.AppID) || !validUUID(input.OperationID) || !validUUID(input.AllocationID) || input.ExpectedRevisionNumber < 0 {
		return AppAccessRevision{}, false, ErrInvalidInput
	}
	requestDigest, err := approvalRequestDigest(input)
	if err != nil {
		return AppAccessRevision{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessRevision{}, false, err
	}
	defer tx.Rollback()
	if r.afterApprovalLock != nil {
		r.afterApprovalLock()
	}
	if existing, storedDigest, lookupErr := readAccessByOperation(ctx, tx, input.AppID, input.OperationID); lookupErr == nil {
		if storedDigest != requestDigest {
			return AppAccessRevision{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessRevision{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return AppAccessRevision{}, false, lookupErr
	}
	allocation, err := readAllocationByID(ctx, tx, input.AllocationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessRevision{}, false, ErrNotFound
	}
	if err != nil {
		return AppAccessRevision{}, false, err
	}
	if allocation.AppID != input.AppID || allocation.OwnerOperationID != input.OperationID {
		return AppAccessRevision{}, false, ErrConflict
	}
	wantReservationDigest, err := reservationRequestDigest(ReserveAppAccessInput{
		AppID:                        input.AppID,
		OperationID:                  input.OperationID,
		ExpectedRevisionNumber:       input.ExpectedRevisionNumber,
		GatewayProfileRevisionID:     allocation.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: allocation.GatewayProfileRevisionNumber,
	})
	if err != nil || wantReservationDigest != allocation.ReservationDigest {
		return AppAccessRevision{}, false, ErrIdempotencyMismatch
	}
	specDigest, err := AppAccessSpecDigest(AppAccessSpecFor(allocation))
	if err != nil || !validApproval(input.Approval, ActionEnableAppAccess, specDigest) {
		return AppAccessRevision{}, false, ErrApprovalRequired
	}
	if ok, err := administratorExists(ctx, tx, input.Approval.ActorID); err != nil {
		return AppAccessRevision{}, false, err
	} else if !ok {
		return AppAccessRevision{}, false, ErrApprovalRequired
	}
	var currentAccess int64
	if err := tx.QueryRowContext(ctx, `SELECT h.revision_number FROM lan_app_access_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, input.AppID).Scan(&currentAccess); errors.Is(err, sql.ErrNoRows) {
		return AppAccessRevision{}, false, ErrConflict
	} else if err != nil {
		return AppAccessRevision{}, false, err
	}
	if currentAccess != input.ExpectedRevisionNumber {
		return AppAccessRevision{}, false, ErrConflict
	}
	var currentProfileID sql.NullString
	var currentProfileNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_id,revision_number FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&currentProfileID, &currentProfileNumber); err != nil {
		return AppAccessRevision{}, false, err
	}
	if !currentProfileID.Valid || currentProfileID.String != allocation.GatewayProfileRevisionID || currentProfileNumber != allocation.GatewayProfileRevisionNumber {
		return AppAccessRevision{}, false, ErrConflict
	}
	if allocation.ReleasedAt != nil {
		return AppAccessRevision{}, false, ErrReservationReleased
	}
	if allocation.State != AllocationReserved || allocation.OwnerRevisionID != "" {
		return AppAccessRevision{}, false, ErrInvalidTransition
	}
	now := r.now().UTC()
	value := AppAccessRevision{
		ID:             uuid.NewString(),
		AppID:          input.AppID,
		RevisionNumber: currentAccess + 1,
		OperationID:    input.OperationID,
		SpecDigest:     specDigest,
		ApprovedBy:     input.Approval.ActorID,
		ApprovedAt:     now,
		Allocation:     allocation,
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_app_access_revisions(
		id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AppID, value.RevisionNumber, value.OperationID, requestDigest, ActionEnableAppAccess, allocation.ID, allocation.Port, allocation.GatewayProfileRevisionID, allocation.GatewayProfileRevisionNumber, specDigest, value.ApprovedBy, stamp); err != nil {
		return AppAccessRevision{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE lan_app_access_heads SET revision_id=?,revision_number=?,updated_at=? WHERE app_id=? AND revision_number=?`, value.ID, value.RevisionNumber, stamp, value.AppID, input.ExpectedRevisionNumber)
	if err != nil {
		return AppAccessRevision{}, false, err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return AppAccessRevision{}, false, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=? AND app_id=? AND owner_operation_id=? AND owner_revision_id IS NULL AND state='reserved' AND released_at IS NULL`, value.ID, allocation.ID, value.AppID, value.OperationID)
	if err != nil {
		return AppAccessRevision{}, false, err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return AppAccessRevision{}, false, ErrConflict
	}
	value.Allocation.OwnerRevisionID = value.ID
	metadata, _ := json.Marshal(map[string]any{"revisionId": value.ID, "revisionNumber": value.RevisionNumber, "allocationId": allocation.ID, "port": allocation.Port, "gatewayProfileRevisionId": allocation.GatewayProfileRevisionID, "gatewayProfileRevisionNumber": allocation.GatewayProfileRevisionNumber, "specDigest": specDigest})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`, value.ApprovedBy, string(ActionEnableAppAccess), "application", value.AppID, string(metadata), stamp); err != nil {
		return AppAccessRevision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessRevision{}, false, err
	}
	return value, true, nil
}

func (r *Repository) CurrentAppAccess(ctx context.Context, appID string) (AppAccessRevision, error) {
	if r == nil || r.db == nil || !validUUID(appID) {
		return AppAccessRevision{}, ErrInvalidInput
	}
	var id sql.NullString
	var number int64
	if err := r.db.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number FROM lan_app_access_heads h JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL WHERE h.app_id=?`, appID).Scan(&id, &number); errors.Is(err, sql.ErrNoRows) {
		return AppAccessRevision{}, ErrNotFound
	} else if err != nil {
		return AppAccessRevision{}, err
	}
	if number == 0 || !id.Valid {
		return AppAccessRevision{}, ErrNotFound
	}
	value, _, err := readAccessRevision(ctx, r.db, appID, id.String, number)
	return value, err
}

// CleanupReserved releases only the exact operation's unapproved reservation.
// The historical row is retained and exact replays continue to return it.
func (r *Repository) CleanupReserved(ctx context.Context, owner AllocationOwner) (bool, error) {
	if r == nil || r.db == nil || !validUUID(owner.AllocationID) || !validUUID(owner.AppID) || !validUUID(owner.OperationID) || owner.AccessRevisionID != "" {
		return false, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	value, err := readAllocationByID(ctx, tx, owner.AllocationID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if value.AppID != owner.AppID || value.OwnerOperationID != owner.OperationID {
		return false, ErrConflict
	}
	if value.ReleasedAt != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if value.State != AllocationReserved || value.OwnerRevisionID != "" {
		return false, ErrInvalidTransition
	}
	result, err := tx.ExecContext(ctx, `UPDATE lan_port_allocations SET released_at=? WHERE id=? AND app_id=? AND owner_operation_id=? AND owner_revision_id IS NULL AND state='reserved' AND released_at IS NULL`, formatTime(r.now().UTC()), owner.AllocationID, owner.AppID, owner.OperationID)
	if err != nil {
		return false, err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return false, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) Activate(ctx context.Context, owner AllocationOwner) (Allocation, error) {
	return r.transitionAllocation(ctx, owner, AllocationActive)
}

// MarkUncertain retains ownership when an external gateway outcome cannot be
// proved. Activate may later reconcile uncertain back to active, but only the
// caller can supply the required fresh gateway, binding, route, and serving
// deployment attestation; this storage layer does not perform those checks.
func (r *Repository) MarkUncertain(ctx context.Context, owner AllocationOwner) (Allocation, error) {
	return r.transitionAllocation(ctx, owner, AllocationUncertain)
}

func (r *Repository) transitionAllocation(ctx context.Context, owner AllocationOwner, target AllocationState) (Allocation, error) {
	if r == nil || r.db == nil || !validUUID(owner.AllocationID) || !validUUID(owner.AppID) || !validUUID(owner.OperationID) || !validUUID(owner.AccessRevisionID) || (target != AllocationActive && target != AllocationUncertain) {
		return Allocation{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Allocation{}, err
	}
	defer tx.Rollback()
	value, err := readAllocationByID(ctx, tx, owner.AllocationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNotFound
	}
	if err != nil {
		return Allocation{}, err
	}
	if value.AppID != owner.AppID || value.OwnerOperationID != owner.OperationID || value.OwnerRevisionID != owner.AccessRevisionID {
		return Allocation{}, ErrConflict
	}
	if value.ReleasedAt != nil {
		return Allocation{}, ErrReservationReleased
	}
	if target == AllocationActive {
		var current int
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1
			FROM applications a
			JOIN lan_app_access_revisions r
			  ON r.id=? AND r.app_id=a.id AND r.operation_id=? AND r.allocation_id=?
			JOIN lan_app_access_heads h
			  ON h.app_id=r.app_id AND h.revision_id=r.id AND h.revision_number=r.revision_number
			JOIN lan_gateway_profile_heads g
			  ON g.singleton=1 AND g.revision_id=? AND g.revision_number=?
			WHERE a.id=? AND a.archived_at IS NULL
		)`, owner.AccessRevisionID, owner.OperationID, owner.AllocationID, value.GatewayProfileRevisionID, value.GatewayProfileRevisionNumber, owner.AppID).Scan(&current)
		if err != nil {
			return Allocation{}, err
		}
		if current != 1 {
			return Allocation{}, ErrConflict
		}
	}
	if value.State == target {
		if err := tx.Commit(); err != nil {
			return Allocation{}, err
		}
		return value, nil
	}
	valid := value.State == AllocationReserved || (value.State == AllocationActive && target == AllocationUncertain) || (value.State == AllocationUncertain && target == AllocationActive)
	if !valid {
		return Allocation{}, ErrInvalidTransition
	}
	result, err := tx.ExecContext(ctx, `UPDATE lan_port_allocations SET state=? WHERE id=? AND app_id=? AND owner_operation_id=? AND owner_revision_id=? AND state=? AND released_at IS NULL`, target, owner.AllocationID, owner.AppID, owner.OperationID, owner.AccessRevisionID, value.State)
	if err != nil {
		return Allocation{}, err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return Allocation{}, ErrConflict
	}
	value.State = target
	if err := tx.Commit(); err != nil {
		return Allocation{}, err
	}
	return value, nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readGatewayByOperation(ctx context.Context, query rowQuerier, operationID string) (GatewayProfileRevision, string, error) {
	var id string
	var number int64
	var requestDigest string
	if err := query.QueryRowContext(ctx, `SELECT id,revision_number,request_digest FROM lan_gateway_profile_revisions WHERE operation_id=?`, operationID).Scan(&id, &number, &requestDigest); err != nil {
		return GatewayProfileRevision{}, "", err
	}
	value, storedDigest, err := readGatewayRevision(ctx, query, id, number)
	if err != nil {
		return GatewayProfileRevision{}, "", err
	}
	if storedDigest != requestDigest {
		return GatewayProfileRevision{}, "", ErrInvalidStoredState
	}
	return value, requestDigest, nil
}

func readGatewayRevision(ctx context.Context, query rowQuerier, id string, number int64) (GatewayProfileRevision, string, error) {
	var value GatewayProfileRevision
	var requestDigest, action, approvedAt string
	value.ID = id
	value.RevisionNumber = number
	err := query.QueryRowContext(ctx, `SELECT operation_id,request_digest,approval_action,selected_ipv4,interface_id,port_start,port_end,spec_digest,approved_by,approved_at FROM lan_gateway_profile_revisions WHERE id=? AND revision_number=?`, id, number).Scan(
		&value.OperationID, &requestDigest, &action, &value.Spec.SelectedIPv4, &value.Spec.InterfaceID, &value.Spec.PortStart, &value.Spec.PortEnd, &value.SpecDigest, &value.ApprovedBy, &approvedAt,
	)
	if err != nil {
		return GatewayProfileRevision{}, "", err
	}
	parsed, err := parseTime(approvedAt)
	computed, digestErr := GatewayProfileSpecDigest(value.Spec)
	if err != nil || digestErr != nil || action != string(ActionConfigureGateway) || computed != value.SpecDigest || !validUUID(value.ID) || !validUUID(value.OperationID) || !validDigest(requestDigest) {
		return GatewayProfileRevision{}, "", ErrInvalidStoredState
	}
	value.ApprovedAt = parsed
	return value, requestDigest, nil
}

func readAllocationByOperation(ctx context.Context, query rowQuerier, operationID string) (Allocation, error) {
	var id string
	if err := query.QueryRowContext(ctx, `SELECT id FROM lan_port_allocations WHERE owner_operation_id=?`, operationID).Scan(&id); err != nil {
		return Allocation{}, err
	}
	return readAllocationByID(ctx, query, id)
}

func readAllocationByID(ctx context.Context, query rowQuerier, id string) (Allocation, error) {
	var value Allocation
	var ownerRevision, released sql.NullString
	var reserved string
	value.ID = id
	err := query.QueryRowContext(ctx, `SELECT app_id,port,owner_operation_id,owner_revision_id,reservation_digest,gateway_profile_revision_id,gateway_profile_revision_number,state,reserved_at,released_at FROM lan_port_allocations WHERE id=?`, id).Scan(
		&value.AppID, &value.Port, &value.OwnerOperationID, &ownerRevision, &value.ReservationDigest, &value.GatewayProfileRevisionID, &value.GatewayProfileRevisionNumber, &value.State, &reserved, &released,
	)
	if err != nil {
		return Allocation{}, err
	}
	value.OwnerRevisionID = ownerRevision.String
	reservedAt, err := parseTime(reserved)
	if err != nil {
		return Allocation{}, ErrInvalidStoredState
	}
	value.ReservedAt = reservedAt
	if released.Valid {
		parsed, err := parseTime(released.String)
		if err != nil {
			return Allocation{}, ErrInvalidStoredState
		}
		value.ReleasedAt = &parsed
	}
	if !validUUID(value.ID) || !validUUID(value.AppID) || !validUUID(value.OwnerOperationID) || (value.OwnerRevisionID != "" && !validUUID(value.OwnerRevisionID)) || !validUUID(value.GatewayProfileRevisionID) || value.GatewayProfileRevisionNumber <= 0 || !validDigest(value.ReservationDigest) || value.Port < GatewayPortStart || value.Port > GatewayPortEnd || (value.State != AllocationReserved && value.State != AllocationActive && value.State != AllocationUncertain) || (value.ReleasedAt != nil && (value.State != AllocationReserved || value.OwnerRevisionID != "")) {
		return Allocation{}, ErrInvalidStoredState
	}
	return value, nil
}

func readAccessByOperation(ctx context.Context, query rowQuerier, appID, operationID string) (AppAccessRevision, string, error) {
	var id string
	var number int64
	var requestDigest string
	if err := query.QueryRowContext(ctx, `SELECT id,revision_number,request_digest FROM lan_app_access_revisions WHERE app_id=? AND operation_id=?`, appID, operationID).Scan(&id, &number, &requestDigest); err != nil {
		return AppAccessRevision{}, "", err
	}
	value, storedDigest, err := readAccessRevision(ctx, query, appID, id, number)
	if err != nil {
		return AppAccessRevision{}, "", err
	}
	if storedDigest != requestDigest {
		return AppAccessRevision{}, "", ErrInvalidStoredState
	}
	return value, requestDigest, nil
}

func readAccessRevision(ctx context.Context, query rowQuerier, appID, id string, number int64) (AppAccessRevision, string, error) {
	var value AppAccessRevision
	var requestDigest, action, allocationID, approvedAt string
	var storedPort uint16
	var profileID string
	var profileNumber int64
	value.ID = id
	value.AppID = appID
	value.RevisionNumber = number
	err := query.QueryRowContext(ctx, `SELECT operation_id,request_digest,approval_action,allocation_id,allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,spec_digest,approved_by,approved_at FROM lan_app_access_revisions WHERE id=? AND app_id=? AND revision_number=?`, id, appID, number).Scan(
		&value.OperationID, &requestDigest, &action, &allocationID, &storedPort, &profileID, &profileNumber, &value.SpecDigest, &value.ApprovedBy, &approvedAt,
	)
	if err != nil {
		return AppAccessRevision{}, "", err
	}
	value.Allocation, err = readAllocationByID(ctx, query, allocationID)
	if err != nil {
		return AppAccessRevision{}, "", err
	}
	parsed, parseErr := parseTime(approvedAt)
	computed, digestErr := AppAccessSpecDigest(AppAccessSpecFor(value.Allocation))
	if parseErr != nil || digestErr != nil || action != string(ActionEnableAppAccess) || computed != value.SpecDigest || value.Allocation.AppID != appID || value.Allocation.Port != storedPort || value.Allocation.GatewayProfileRevisionID != profileID || value.Allocation.GatewayProfileRevisionNumber != profileNumber || value.Allocation.OwnerOperationID != value.OperationID || value.Allocation.OwnerRevisionID != id || !validUUID(value.ID) || !validUUID(value.OperationID) || !validDigest(requestDigest) {
		return AppAccessRevision{}, "", ErrInvalidStoredState
	}
	value.ApprovedAt = parsed
	return value, requestDigest, nil
}

func administratorExists(ctx context.Context, query rowQuerier, actorID string) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND role='administrator')`, actorID).Scan(&exists)
	return exists == 1, err
}

func gatewayRequestDigest(input ConfigureGatewayInput, spec GatewayProfileSpec) (string, error) {
	return digestJSON(struct {
		Expected int64              `json:"expectedRevisionNumber"`
		Spec     GatewayProfileSpec `json:"spec"`
		Approval Approval           `json:"approval"`
	}{input.ExpectedRevisionNumber, spec, input.Approval})
}

func reservationRequestDigest(input ReserveAppAccessInput) (string, error) {
	return digestJSON(struct {
		AppID           string `json:"appId"`
		Expected        int64  `json:"expectedRevisionNumber"`
		ProfileID       string `json:"gatewayProfileRevisionId"`
		ProfileRevision int64  `json:"gatewayProfileRevisionNumber"`
	}{input.AppID, input.ExpectedRevisionNumber, input.GatewayProfileRevisionID, input.GatewayProfileRevisionNumber})
}

func approvalRequestDigest(input ApproveAppAccessInput) (string, error) {
	return digestJSON(struct {
		AppID        string   `json:"appId"`
		AllocationID string   `json:"allocationId"`
		Expected     int64    `json:"expectedRevisionNumber"`
		Approval     Approval `json:"approval"`
	}{input.AppID, input.AllocationID, input.ExpectedRevisionNumber, input.Approval})
}

func classifyGatewayWrite(ctx context.Context, tx rowQuerier, operationID, requestDigest string, expected int64, cause error) error {
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT request_digest FROM lan_gateway_profile_revisions WHERE operation_id=?`, operationID).Scan(&stored); err == nil {
		if stored == requestDigest {
			return cause
		}
		return ErrIdempotencyMismatch
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_number FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&current); err == nil && current != expected {
		return ErrConflict
	}
	return cause
}

type immediateTransaction struct {
	*sql.Conn
	done bool
}

func beginImmediateTransaction(ctx context.Context, db *sql.DB) (*immediateTransaction, error) {
	connection, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := connection.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		_ = connection.Close()
		return nil, classifyImmediateTransactionError(err)
	}
	return &immediateTransaction{Conn: connection}, nil
}

func (tx *immediateTransaction) Commit(ctx context.Context) error {
	if tx == nil || tx.Conn == nil || tx.done {
		return ErrInvalidStoredState
	}
	if _, err := tx.ExecContext(ctx, `COMMIT`); err != nil {
		return classifyImmediateTransactionError(err)
	}
	tx.done = true
	return tx.Conn.Close()
}

func (tx *immediateTransaction) Rollback() error {
	if tx == nil || tx.Conn == nil || tx.done {
		return nil
	}
	_, rollbackErr := tx.ExecContext(context.Background(), `ROLLBACK`)
	tx.done = true
	closeErr := tx.Conn.Close()
	if rollbackErr != nil {
		return rollbackErr
	}
	return closeErr
}

func classifyImmediateTransactionError(err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			return ErrConflict
		}
	}
	return err
}

func formatTime(value time.Time) string { return value.UTC().Format(timestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse LAN access timestamp: %w", err)
	}
	return parsed, nil
}
