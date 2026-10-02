package appaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// AppAccessGrantState is the retained cross-process state of one gateway
// mutation attempt. Claims never expire. A retry after a proved rollback uses
// a fresh AttemptID so an old process cannot regain ownership accidentally.
type AppAccessGrantState string

const (
	AppAccessGrantPrepared   AppAccessGrantState = "prepared"
	AppAccessGrantApplying   AppAccessGrantState = "applying"
	AppAccessGrantDBActive   AppAccessGrantState = "db_active"
	AppAccessGrantUncertain  AppAccessGrantState = "uncertain"
	AppAccessGrantCommitted  AppAccessGrantState = "committed"
	AppAccessGrantRolledBack AppAccessGrantState = "rolled_back"
)

// AppAccessGrantSpec is the exact approved access and gateway profile identity
// bound to one grant attempt. AttemptID is deliberately separate: it is an
// execution identity and is not part of the administrator-approved
// AppAccessSpecDigest.
type AppAccessGrantSpec struct {
	AppID                        string `json:"appId"`
	AllocationID                 string `json:"allocationId"`
	OwnerOperationID             string `json:"ownerOperationId"`
	AccessRevisionID             string `json:"accessRevisionId"`
	AccessRevisionNumber         int64  `json:"accessRevisionNumber"`
	AccessSpecDigest             string `json:"accessSpecDigest"`
	Port                         uint16 `json:"port"`
	GatewayProfileRevisionID     string `json:"gatewayProfileRevisionId"`
	GatewayProfileRevisionNumber int64  `json:"gatewayProfileRevisionNumber"`
	GatewayProfileSpecDigest     string `json:"gatewayProfileSpecDigest"`
	ApprovedBy                   string `json:"approvedBy"`
}

type ClaimAppAccessGrantInput struct {
	AttemptID string             `json:"attemptId"`
	Spec      AppAccessGrantSpec `json:"spec"`
	ActorID   string             `json:"actorId"`
}

// AppAccessGrantClaimOwner must accompany every read or transition. The
// request digest and complete allocation owner prevent an attempt ID alone
// from acting as a bearer capability.
type AppAccessGrantClaimOwner struct {
	AttemptID        string
	RequestDigest    string
	AppID            string
	AllocationID     string
	OwnerOperationID string
	AccessRevisionID string
	// ActorID is the administrator executing this transition, independent of
	// the earlier administrator whose approval is retained in the claim.
	ActorID string
}

// AppAccessGrantProof records the protected gateway state freshly observed by
// the trusted controller. Persistence does not validate route presence or
// absence; callers must prove the requested terminal outcome under the gateway
// lock immediately before ResolveAppAccessGrantClaim.
type AppAccessGrantProof struct {
	GatewayOperationID   string
	ProtectedStateDigest string
	// ObservedAt is populated by the repository when the proof is retained.
	// Callers do not supply or control the durable timestamp.
	ObservedAt time.Time
}

type AppAccessGrantClaim struct {
	AttemptID                   string
	RequestDigest               string
	Spec                        AppAccessGrantSpec
	ApprovedAt                  time.Time
	CreatedAt                   time.Time
	State                       AppAccessGrantState
	StateSequence               int64
	UpdatedAt                   time.Time
	Proof                       *AppAccessGrantProof
	RetiredAt                   *time.Time
	RetiredByDisableOperationID string
}

// AppAccessGrantSpecFor derives the execution binding from immutable approved
// records. It does not attest that the gateway or application is serving.
func AppAccessGrantSpecFor(revision AppAccessRevision, profile GatewayProfileRevision) AppAccessGrantSpec {
	return AppAccessGrantSpec{
		AppID:                        revision.AppID,
		AllocationID:                 revision.Allocation.ID,
		OwnerOperationID:             revision.OperationID,
		AccessRevisionID:             revision.ID,
		AccessRevisionNumber:         revision.RevisionNumber,
		AccessSpecDigest:             revision.SpecDigest,
		Port:                         revision.Allocation.Port,
		GatewayProfileRevisionID:     profile.ID,
		GatewayProfileRevisionNumber: profile.RevisionNumber,
		GatewayProfileSpecDigest:     profile.SpecDigest,
		ApprovedBy:                   revision.ApprovedBy,
	}
}

func AppAccessGrantClaimOwnerFor(claim AppAccessGrantClaim) AppAccessGrantClaimOwner {
	return AppAccessGrantClaimOwner{
		AttemptID: claim.AttemptID, RequestDigest: claim.RequestDigest,
		AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID,
		AccessRevisionID: claim.Spec.AccessRevisionID,
		ActorID:          claim.Spec.ApprovedBy,
	}
}

// ClaimAppAccessGrant creates a non-expiring prepared logical lease. It uses a
// short SQLite write transaction and performs no gateway, Docker, or network
// work. An exact AttemptID replay returns the retained claim; a changed payload
// returns ErrIdempotencyMismatch.
func (r *Repository) ClaimAppAccessGrant(ctx context.Context, input ClaimAppAccessGrantInput) (AppAccessGrantClaim, bool, error) {
	if r == nil || r.db == nil || !validUUID(input.AttemptID) || !validAppAccessGrantSpec(input.Spec) || !validUUID(input.ActorID) {
		return AppAccessGrantClaim{}, false, ErrInvalidInput
	}
	requestDigest, err := appAccessGrantRequestDigest(input)
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	defer tx.Rollback()
	actorIsAdministrator, err := administratorExists(ctx, tx, input.ActorID)
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if !actorIsAdministrator {
		return AppAccessGrantClaim{}, false, ErrApprovalRequired
	}
	if r.afterGrantClaimLock != nil {
		r.afterGrantClaimLock()
	}
	if existing, lookupErr := readAppAccessGrantClaim(ctx, tx, input.AttemptID); lookupErr == nil {
		if existing.RequestDigest != requestDigest {
			return AppAccessGrantClaim{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessGrantClaim{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return AppAccessGrantClaim{}, false, lookupErr
	}

	revision, profile, err := validateCurrentAppAccessGrantSpec(ctx, tx, input.Spec, true)
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	var blocking int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_grant_claims
		WHERE allocation_id=? AND state IN ('prepared','applying','db_active','uncertain','committed')
		  AND retired_at IS NULL
	)`, input.Spec.AllocationID).Scan(&blocking); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if blocking != 0 {
		return AppAccessGrantClaim{}, false, ErrConflict
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_grant_claims
		WHERE state IN ('prepared','applying','db_active','uncertain')
	)`).Scan(&blocking); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if blocking != 0 {
		return AppAccessGrantClaim{}, false, ErrConflict
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_disable_claims
		WHERE state IN ('prepared','withdrawing','uncertain')
	)`).Scan(&blocking); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if blocking != 0 {
		return AppAccessGrantClaim{}, false, ErrConflict
	}

	now := r.now().UTC()
	value := AppAccessGrantClaim{
		AttemptID: input.AttemptID, RequestDigest: requestDigest, Spec: input.Spec,
		ApprovedAt: revision.ApprovedAt, CreatedAt: now,
		State: AppAccessGrantPrepared, StateSequence: 1, UpdatedAt: now,
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_app_access_grant_claims(
		attempt_id,request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		access_spec_digest,allocated_port,gateway_profile_revision_id,
		gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
		approved_at,created_at,state,state_sequence,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.AttemptID, value.RequestDigest, ActionEnableAppAccess,
		value.Spec.AppID, value.Spec.AllocationID, value.Spec.OwnerOperationID,
		value.Spec.AccessRevisionID, value.Spec.AccessRevisionNumber,
		value.Spec.AccessSpecDigest, value.Spec.Port,
		value.Spec.GatewayProfileRevisionID, value.Spec.GatewayProfileRevisionNumber,
		value.Spec.GatewayProfileSpecDigest, value.Spec.ApprovedBy,
		formatTime(value.ApprovedAt), stamp, value.State, value.StateSequence, stamp); err != nil {
		return AppAccessGrantClaim{}, false, classifyImmediateTransactionError(err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"attemptId": value.AttemptID, "allocationId": value.Spec.AllocationID,
		"ownerOperationId":             value.Spec.OwnerOperationID,
		"accessRevisionId":             value.Spec.AccessRevisionID,
		"accessRevisionNumber":         value.Spec.AccessRevisionNumber,
		"gatewayProfileRevisionId":     profile.ID,
		"gatewayProfileRevisionNumber": profile.RevisionNumber,
		"state":                        value.State, "stateSequence": value.StateSequence,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,?,?,?,?,?)`, input.ActorID, "lan_app_access_grant.claim",
		"lan_app_access_grant", value.AttemptID, string(metadata), stamp); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	return value, true, nil
}

// AdvanceAppAccessGrantClaim performs one nonterminal transition in a short
// transaction. Applying->DBActive atomically activates the exact allocation;
// a commit/readback error therefore leaves either applying plus the prior
// allocation state or DBActive plus active. Both outcomes retain the claim.
// Terminal targets are rejected and require ResolveAppAccessGrantClaim.
func (r *Repository) AdvanceAppAccessGrantClaim(ctx context.Context, owner AppAccessGrantClaimOwner,
	expected, target AppAccessGrantState,
) (AppAccessGrantClaim, bool, error) {
	if r == nil || r.db == nil || !validAppAccessGrantClaimOwner(owner) ||
		!validAppAccessGrantTransition(expected, target) || isTerminalAppAccessGrantState(target) {
		return AppAccessGrantClaim{}, false, ErrInvalidInput
	}
	return r.transitionAppAccessGrantClaim(ctx, owner, expected, target, nil)
}

// ResolveAppAccessGrantClaim records a terminal result only after the trusted
// controller has freshly proved the requested gateway outcome under the
// gateway lock. The repository validates identities and retains the supplied
// protected-state proof, but cannot itself establish route presence/absence.
func (r *Repository) ResolveAppAccessGrantClaim(ctx context.Context, owner AppAccessGrantClaimOwner,
	expected, target AppAccessGrantState, proof AppAccessGrantProof,
) (AppAccessGrantClaim, bool, error) {
	if r == nil || r.db == nil || !validAppAccessGrantClaimOwner(owner) ||
		!validAppAccessGrantTransition(expected, target) || !isTerminalAppAccessGrantState(target) ||
		!validUUID(proof.GatewayOperationID) || !validDigest(proof.ProtectedStateDigest) {
		return AppAccessGrantClaim{}, false, ErrInvalidInput
	}
	proof.ObservedAt = time.Time{}
	return r.transitionAppAccessGrantClaim(ctx, owner, expected, target, &proof)
}

func (r *Repository) transitionAppAccessGrantClaim(ctx context.Context, owner AppAccessGrantClaimOwner,
	expected, target AppAccessGrantState, proof *AppAccessGrantProof,
) (AppAccessGrantClaim, bool, error) {
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	defer tx.Rollback()
	actorIsAdministrator, err := administratorExists(ctx, tx, owner.ActorID)
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if !actorIsAdministrator {
		return AppAccessGrantClaim{}, false, ErrApprovalRequired
	}
	value, err := readAppAccessGrantClaim(ctx, tx, owner.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantClaim{}, false, ErrNotFound
	}
	if err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if !appAccessGrantOwnerMatches(value, owner) {
		return AppAccessGrantClaim{}, false, ErrConflict
	}
	if value.State == target {
		var predecessor AppAccessGrantState
		if value.StateSequence <= 1 || tx.QueryRowContext(ctx, `SELECT state
			FROM lan_app_access_grant_claim_events WHERE attempt_id=? AND sequence=?`,
			value.AttemptID, value.StateSequence-1).Scan(&predecessor) != nil || predecessor != expected {
			return AppAccessGrantClaim{}, false, ErrConflict
		}
		if proof != nil && (value.Proof == nil || value.Proof.GatewayOperationID != proof.GatewayOperationID ||
			value.Proof.ProtectedStateDigest != proof.ProtectedStateDigest) {
			return AppAccessGrantClaim{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessGrantClaim{}, false, err
		}
		return value, false, nil
	}
	if value.State != expected {
		return AppAccessGrantClaim{}, false, ErrConflict
	}
	now := r.now().UTC()
	if proof != nil {
		proof.ObservedAt = now
	}
	var result sql.Result
	if proof == nil {
		result, err = tx.ExecContext(ctx, `UPDATE lan_app_access_grant_claims
			SET state=?,state_sequence=state_sequence+1,updated_at=?
			WHERE attempt_id=? AND request_digest=? AND app_id=? AND allocation_id=?
			  AND allocation_owner_operation_id=? AND access_revision_id=?
			  AND state=? AND state_sequence=?`, target, formatTime(now),
			owner.AttemptID, owner.RequestDigest, owner.AppID, owner.AllocationID,
			owner.OwnerOperationID, owner.AccessRevisionID, expected, value.StateSequence)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE lan_app_access_grant_claims
			SET state=?,state_sequence=state_sequence+1,updated_at=?,
				resolution_outcome=?,gateway_operation_id=?,protected_state_digest=?,resolved_at=?
			WHERE attempt_id=? AND request_digest=? AND app_id=? AND allocation_id=?
			  AND allocation_owner_operation_id=? AND access_revision_id=?
			  AND state=? AND state_sequence=?`, target, formatTime(now), target,
			proof.GatewayOperationID, proof.ProtectedStateDigest, formatTime(proof.ObservedAt),
			owner.AttemptID, owner.RequestDigest, owner.AppID, owner.AllocationID,
			owner.OwnerOperationID, owner.AccessRevisionID, expected, value.StateSequence)
	}
	if err != nil {
		return AppAccessGrantClaim{}, false, classifyImmediateTransactionError(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return AppAccessGrantClaim{}, false, ErrConflict
	}
	previous := value.State
	value.State = target
	value.StateSequence++
	value.UpdatedAt = now
	if proof != nil {
		copyProof := *proof
		value.Proof = &copyProof
	}
	allocation, err := readAllocationByID(ctx, tx, value.Spec.AllocationID)
	if err != nil || !appAccessGrantAllocationStateMatches(value.State, allocation.State) {
		if err != nil {
			return AppAccessGrantClaim{}, false, err
		}
		return AppAccessGrantClaim{}, false, ErrInvalidStoredState
	}
	metadata, _ := json.Marshal(map[string]any{
		"attemptId": value.AttemptID, "previousState": previous,
		"state": value.State, "stateSequence": value.StateSequence,
		"gatewayOperationId": func() string {
			if proof != nil {
				return proof.GatewayOperationID
			}
			return ""
		}(),
		"protectedStateDigest": func() string {
			if proof != nil {
				return proof.ProtectedStateDigest
			}
			return ""
		}(),
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,?,?,?,?,?)`, owner.ActorID, "lan_app_access_grant.transition",
		"lan_app_access_grant", value.AttemptID, string(metadata), formatTime(now)); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	if r.beforeGrantTransitionCommit != nil {
		r.beforeGrantTransitionCommit()
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessGrantClaim{}, false, err
	}
	return value, true, nil
}

func (r *Repository) AppAccessGrantClaim(ctx context.Context, attemptID string) (AppAccessGrantClaim, error) {
	if r == nil || r.db == nil || !validUUID(attemptID) {
		return AppAccessGrantClaim{}, ErrInvalidInput
	}
	value, err := readAppAccessGrantClaim(ctx, r.db, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantClaim{}, ErrNotFound
	}
	return value, err
}

// CurrentAppAccessGrantClaim returns the retained non-rolled-back attempt for
// one exact owner. It performs no gateway observation and is not serving proof.
func (r *Repository) CurrentAppAccessGrantClaim(ctx context.Context, owner AllocationOwner) (AppAccessGrantClaim, error) {
	if r == nil || r.db == nil || !validUUID(owner.AllocationID) || !validUUID(owner.AppID) ||
		!validUUID(owner.OperationID) || !validUUID(owner.AccessRevisionID) {
		return AppAccessGrantClaim{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AppAccessGrantClaim{}, err
	}
	defer tx.Rollback()
	var attemptID string
	err = tx.QueryRowContext(ctx, `SELECT attempt_id FROM lan_app_access_grant_claims
		WHERE allocation_id=? AND app_id=? AND allocation_owner_operation_id=?
		  AND access_revision_id=?
		  AND state IN ('prepared','applying','db_active','uncertain','committed')
		  AND retired_at IS NULL`,
		owner.AllocationID, owner.AppID, owner.OperationID, owner.AccessRevisionID).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantClaim{}, ErrNotFound
	}
	if err != nil {
		return AppAccessGrantClaim{}, err
	}
	value, err := readAppAccessGrantClaim(ctx, tx, attemptID)
	if err != nil {
		return AppAccessGrantClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppAccessGrantClaim{}, err
	}
	return value, nil
}

type appAccessGrantQuerier interface {
	rowQuerier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readAppAccessGrantClaim(ctx context.Context, query appAccessGrantQuerier, attemptID string) (AppAccessGrantClaim, error) {
	var value AppAccessGrantClaim
	var action, approvedAt, createdAt, updatedAt string
	var outcome, gatewayOperationID, protectedStateDigest, resolvedAt sql.NullString
	var retiredAt, retiredByDisableOperationID sql.NullString
	value.AttemptID = attemptID
	err := query.QueryRowContext(ctx, `SELECT request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		access_spec_digest,allocated_port,gateway_profile_revision_id,
		gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
		approved_at,created_at,state,state_sequence,updated_at,resolution_outcome,
		gateway_operation_id,protected_state_digest,resolved_at,retired_at,
		retired_by_disable_operation_id
		FROM lan_app_access_grant_claims WHERE attempt_id=?`, attemptID).Scan(
		&value.RequestDigest, &action, &value.Spec.AppID, &value.Spec.AllocationID,
		&value.Spec.OwnerOperationID, &value.Spec.AccessRevisionID,
		&value.Spec.AccessRevisionNumber, &value.Spec.AccessSpecDigest, &value.Spec.Port,
		&value.Spec.GatewayProfileRevisionID, &value.Spec.GatewayProfileRevisionNumber,
		&value.Spec.GatewayProfileSpecDigest, &value.Spec.ApprovedBy, &approvedAt,
		&createdAt, &value.State, &value.StateSequence, &updatedAt, &outcome,
		&gatewayOperationID, &protectedStateDigest, &resolvedAt, &retiredAt,
		&retiredByDisableOperationID)
	if err != nil {
		return AppAccessGrantClaim{}, err
	}
	value.ApprovedAt, err = parseTime(approvedAt)
	if err != nil {
		return AppAccessGrantClaim{}, ErrInvalidStoredState
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return AppAccessGrantClaim{}, ErrInvalidStoredState
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return AppAccessGrantClaim{}, ErrInvalidStoredState
	}
	if isTerminalAppAccessGrantState(value.State) {
		if !outcome.Valid || outcome.String != string(value.State) || !gatewayOperationID.Valid ||
			!protectedStateDigest.Valid || !resolvedAt.Valid {
			return AppAccessGrantClaim{}, ErrInvalidStoredState
		}
		observedAt, parseErr := parseTime(resolvedAt.String)
		if parseErr != nil {
			return AppAccessGrantClaim{}, ErrInvalidStoredState
		}
		value.Proof = &AppAccessGrantProof{
			GatewayOperationID:   gatewayOperationID.String,
			ProtectedStateDigest: protectedStateDigest.String, ObservedAt: observedAt,
		}
		if !validUUID(value.Proof.GatewayOperationID) || !validDigest(value.Proof.ProtectedStateDigest) ||
			!value.Proof.ObservedAt.Equal(value.UpdatedAt) {
			return AppAccessGrantClaim{}, ErrInvalidStoredState
		}
	} else if outcome.Valid || gatewayOperationID.Valid || protectedStateDigest.Valid || resolvedAt.Valid {
		return AppAccessGrantClaim{}, ErrInvalidStoredState
	}
	if retiredAt.Valid != retiredByDisableOperationID.Valid {
		return AppAccessGrantClaim{}, ErrInvalidStoredState
	}
	if retiredAt.Valid {
		stamp, parseErr := parseTime(retiredAt.String)
		if parseErr != nil || value.State != AppAccessGrantCommitted ||
			!validUUID(retiredByDisableOperationID.String) || stamp.Before(value.UpdatedAt) {
			return AppAccessGrantClaim{}, ErrInvalidStoredState
		}
		value.RetiredAt = &stamp
		value.RetiredByDisableOperationID = retiredByDisableOperationID.String
	}
	requestDigest, digestErr := appAccessGrantRequestDigest(ClaimAppAccessGrantInput{
		AttemptID: value.AttemptID, Spec: value.Spec,
	})
	if digestErr != nil || action != string(ActionEnableAppAccess) ||
		requestDigest != value.RequestDigest || !validAppAccessGrantState(value.State) ||
		value.StateSequence <= 0 || !validUUID(value.AttemptID) ||
		!validUUID(value.Spec.ApprovedBy) || value.CreatedAt.Before(value.ApprovedAt) ||
		value.UpdatedAt.Before(value.CreatedAt) {
		return AppAccessGrantClaim{}, ErrInvalidStoredState
	}
	if err := validateAppAccessGrantClaimHistory(ctx, query, value); err != nil {
		return AppAccessGrantClaim{}, err
	}
	return value, nil
}

func validateAppAccessGrantClaimHistory(ctx context.Context, query appAccessGrantQuerier, claim AppAccessGrantClaim) error {
	rows, err := query.QueryContext(ctx, `SELECT sequence,state,gateway_operation_id,
		protected_state_digest,created_at FROM lan_app_access_grant_claim_events
		WHERE attempt_id=? ORDER BY sequence`, claim.AttemptID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int64
	var previous, last AppAccessGrantState
	var previousAt, lastAt time.Time
	var lastGateway, lastDigest sql.NullString
	for rows.Next() {
		var sequence int64
		var state AppAccessGrantState
		var gateway, digest sql.NullString
		var created string
		if err := rows.Scan(&sequence, &state, &gateway, &digest, &created); err != nil {
			return err
		}
		count++
		stamp, parseErr := parseTime(created)
		if parseErr != nil || sequence != count || !validAppAccessGrantState(state) {
			return ErrInvalidStoredState
		}
		if sequence == 1 {
			if state != AppAccessGrantPrepared || !stamp.Equal(claim.CreatedAt) {
				return ErrInvalidStoredState
			}
		} else if !validAppAccessGrantTransition(previous, state) || stamp.Before(previousAt) {
			return ErrInvalidStoredState
		}
		if isTerminalAppAccessGrantState(state) != (gateway.Valid && digest.Valid) {
			return ErrInvalidStoredState
		}
		if isTerminalAppAccessGrantState(state) && (!validUUID(gateway.String) || !validDigest(digest.String)) {
			return ErrInvalidStoredState
		}
		previous, previousAt = state, stamp
		last, lastAt, lastGateway, lastDigest = state, stamp, gateway, digest
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != claim.StateSequence || last != claim.State || !lastAt.Equal(claim.UpdatedAt) {
		return ErrInvalidStoredState
	}
	if claim.Proof == nil {
		if lastGateway.Valid || lastDigest.Valid {
			return ErrInvalidStoredState
		}
	} else if !lastGateway.Valid || !lastDigest.Valid ||
		lastGateway.String != claim.Proof.GatewayOperationID ||
		lastDigest.String != claim.Proof.ProtectedStateDigest {
		return ErrInvalidStoredState
	}
	return nil
}

func appAccessGrantRequestDigest(input ClaimAppAccessGrantInput) (string, error) {
	if !validUUID(input.AttemptID) || !validAppAccessGrantSpec(input.Spec) {
		return "", ErrInvalidInput
	}
	return digestJSON(struct {
		Version   int                `json:"version"`
		AttemptID string             `json:"attemptId"`
		Spec      AppAccessGrantSpec `json:"spec"`
	}{Version: 1, AttemptID: input.AttemptID, Spec: input.Spec})
}

func validAppAccessGrantSpec(spec AppAccessGrantSpec) bool {
	return validUUID(spec.AppID) && validUUID(spec.AllocationID) &&
		validUUID(spec.OwnerOperationID) && validUUID(spec.AccessRevisionID) &&
		spec.AccessRevisionNumber > 0 && validDigest(spec.AccessSpecDigest) &&
		spec.Port >= GatewayPortStart && spec.Port <= GatewayPortEnd &&
		validUUID(spec.GatewayProfileRevisionID) && spec.GatewayProfileRevisionNumber > 0 &&
		validDigest(spec.GatewayProfileSpecDigest) && validUUID(spec.ApprovedBy)
}

func validAppAccessGrantClaimOwner(owner AppAccessGrantClaimOwner) bool {
	return validUUID(owner.AttemptID) && validDigest(owner.RequestDigest) &&
		validUUID(owner.AppID) && validUUID(owner.AllocationID) &&
		validUUID(owner.OwnerOperationID) && validUUID(owner.AccessRevisionID) && validUUID(owner.ActorID)
}

func appAccessGrantOwnerMatches(claim AppAccessGrantClaim, owner AppAccessGrantClaimOwner) bool {
	return claim.AttemptID == owner.AttemptID && claim.RequestDigest == owner.RequestDigest &&
		claim.Spec.AppID == owner.AppID && claim.Spec.AllocationID == owner.AllocationID &&
		claim.Spec.OwnerOperationID == owner.OwnerOperationID &&
		claim.Spec.AccessRevisionID == owner.AccessRevisionID
}

func validAppAccessGrantState(state AppAccessGrantState) bool {
	switch state {
	case AppAccessGrantPrepared, AppAccessGrantApplying, AppAccessGrantDBActive,
		AppAccessGrantUncertain, AppAccessGrantCommitted, AppAccessGrantRolledBack:
		return true
	default:
		return false
	}
}

func isTerminalAppAccessGrantState(state AppAccessGrantState) bool {
	return state == AppAccessGrantCommitted || state == AppAccessGrantRolledBack
}

func validAppAccessGrantTransition(from, to AppAccessGrantState) bool {
	switch from {
	case AppAccessGrantPrepared:
		return to == AppAccessGrantApplying || to == AppAccessGrantRolledBack
	case AppAccessGrantApplying:
		return to == AppAccessGrantDBActive || to == AppAccessGrantUncertain || to == AppAccessGrantRolledBack
	case AppAccessGrantDBActive:
		return to == AppAccessGrantCommitted || to == AppAccessGrantUncertain
	case AppAccessGrantUncertain:
		return to == AppAccessGrantCommitted || to == AppAccessGrantRolledBack
	default:
		return false
	}
}

func appAccessGrantAllocationStateMatches(state AppAccessGrantState, allocation AllocationState) bool {
	switch state {
	case AppAccessGrantPrepared, AppAccessGrantApplying, AppAccessGrantRolledBack:
		return allocation == AllocationReserved || allocation == AllocationActive || allocation == AllocationUncertain
	case AppAccessGrantDBActive, AppAccessGrantCommitted:
		return allocation == AllocationActive
	case AppAccessGrantUncertain:
		return allocation == AllocationUncertain
	default:
		return false
	}
}

func validateCurrentAppAccessGrantSpec(ctx context.Context, query rowQuerier, spec AppAccessGrantSpec,
	rejectDisable bool,
) (AppAccessRevision, GatewayProfileRevision, error) {
	var headRevision sql.NullString
	var headNumber int64
	var archived sql.NullString
	if err := query.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number,a.archived_at
		FROM lan_app_access_heads h JOIN applications a ON a.id=h.app_id WHERE h.app_id=?`,
		spec.AppID).Scan(&headRevision, &headNumber, &archived); errors.Is(err, sql.ErrNoRows) {
		return AppAccessRevision{}, GatewayProfileRevision{}, ErrNotFound
	} else if err != nil {
		return AppAccessRevision{}, GatewayProfileRevision{}, err
	}
	if archived.Valid || !headRevision.Valid || headRevision.String != spec.AccessRevisionID ||
		headNumber != spec.AccessRevisionNumber {
		return AppAccessRevision{}, GatewayProfileRevision{}, ErrConflict
	}
	revision, _, err := readAccessRevision(ctx, query, spec.AppID, spec.AccessRevisionID, spec.AccessRevisionNumber)
	if err != nil {
		return AppAccessRevision{}, GatewayProfileRevision{}, err
	}
	if revision.OperationID != spec.OwnerOperationID || revision.SpecDigest != spec.AccessSpecDigest ||
		revision.ApprovedBy != spec.ApprovedBy || revision.Allocation.ID != spec.AllocationID ||
		revision.Allocation.Port != spec.Port || revision.Allocation.ReleasedAt != nil ||
		revision.Allocation.GatewayProfileRevisionID != spec.GatewayProfileRevisionID ||
		revision.Allocation.GatewayProfileRevisionNumber != spec.GatewayProfileRevisionNumber {
		return AppAccessRevision{}, GatewayProfileRevision{}, ErrConflict
	}
	administrator, err := administratorExists(ctx, query, spec.ApprovedBy)
	if err != nil {
		return AppAccessRevision{}, GatewayProfileRevision{}, err
	}
	if !administrator {
		return AppAccessRevision{}, GatewayProfileRevision{}, ErrApprovalRequired
	}
	var profileHead sql.NullString
	var profileNumber int64
	if err := query.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&profileHead, &profileNumber); err != nil {
		return AppAccessRevision{}, GatewayProfileRevision{}, err
	}
	if !profileHead.Valid || profileHead.String != spec.GatewayProfileRevisionID ||
		profileNumber != spec.GatewayProfileRevisionNumber {
		return AppAccessRevision{}, GatewayProfileRevision{}, ErrConflict
	}
	profile, _, err := readGatewayRevision(ctx, query, spec.GatewayProfileRevisionID, spec.GatewayProfileRevisionNumber)
	if err != nil {
		return AppAccessRevision{}, GatewayProfileRevision{}, err
	}
	if profile.SpecDigest != spec.GatewayProfileSpecDigest {
		return AppAccessRevision{}, GatewayProfileRevision{}, ErrConflict
	}
	if rejectDisable {
		var disable int
		if err := query.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM lan_app_access_disable_intents WHERE allocation_id=?
		)`, spec.AllocationID).Scan(&disable); err != nil {
			return AppAccessRevision{}, GatewayProfileRevision{}, err
		}
		if disable != 0 {
			return AppAccessRevision{}, GatewayProfileRevision{}, ErrConflict
		}
	}
	return revision, profile, nil
}
