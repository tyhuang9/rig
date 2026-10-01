package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

// AppAccessReservationReview contains the immutable reservation and the
// exact current approval input derived from it. It is consent data only; it
// neither applies gateway state nor proves that a route is serving.
type AppAccessReservationReview struct {
	Allocation             Allocation
	ExpectedRevisionNumber int64
	ApprovalDigest         string
}

// AppAccessDisableReview contains the exact current disable approval input.
// It is present only while the current access allocation is unreleased and
// has no retained disable claim.
type AppAccessDisableReview struct {
	Spec           AppAccessDisableSpec
	ApprovalDigest string
}

// AppAccessOperatorSnapshot is the read-only, normal-mode view of one
// application's current LAN access lineage. Stored claims and reviews are
// durable authorization data, never gateway proof or a LAN URL.
type AppAccessOperatorSnapshot struct {
	ExpectedRevisionNumber int64
	DesiredAccess          *AppAccessRevision
	PendingReservation     *AppAccessReservationReview
	GrantClaim             *AppAccessGrantClaim
	DisableClaim           *AppAccessDisableClaim
	DisableReview          *AppAccessDisableReview
}

// ReadAppAccessOperatorSnapshot reads one application's current operator
// state under one SQLite read transaction. It only returns a pending
// reservation, grant claim, or disable claim when each is bound to the exact
// current app head and allocation. Superseded history is excluded; the current
// head's committed disable remains readable after its allocation is released.
func (r *Repository) ReadAppAccessOperatorSnapshot(ctx context.Context, appID string) (AppAccessOperatorSnapshot, error) {
	if r == nil || r.db == nil || !validUUID(appID) {
		return AppAccessOperatorSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	defer tx.Rollback()

	snapshot, err := r.readAppAccessOperatorSnapshot(ctx, tx, appID)
	if err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	return snapshot, nil
}

func (r *Repository) readAppAccessOperatorSnapshot(ctx context.Context, tx *sql.Tx, appID string) (AppAccessOperatorSnapshot, error) {
	var archived sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM applications WHERE id=?`, appID).Scan(&archived); errors.Is(err, sql.ErrNoRows) {
		return AppAccessOperatorSnapshot{}, ErrNotFound
	} else if err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	if archived.Valid {
		return AppAccessOperatorSnapshot{}, ErrNotFound
	}

	var headID sql.NullString
	var headNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_app_access_heads WHERE app_id=?`, appID).Scan(&headID, &headNumber); errors.Is(err, sql.ErrNoRows) {
		return AppAccessOperatorSnapshot{}, ErrInvalidStoredState
	} else if err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	if headNumber < 0 || (headNumber == 0 && headID.Valid) || (headNumber > 0 && !headID.Valid) {
		return AppAccessOperatorSnapshot{}, ErrInvalidStoredState
	}
	if r.afterOperatorSnapshotHeadRead != nil {
		r.afterOperatorSnapshotHeadRead()
	}

	result := AppAccessOperatorSnapshot{ExpectedRevisionNumber: headNumber}
	var current *AppAccessRevision
	if headNumber > 0 {
		revision, requestDigest, err := readAccessRevision(ctx, tx, appID, headID.String, headNumber)
		if err != nil {
			return AppAccessOperatorSnapshot{}, operatorSnapshotStoredError(err)
		}
		if err := validateOperatorCurrentAccessRevision(revision, requestDigest); err != nil {
			return AppAccessOperatorSnapshot{}, err
		}
		current = &revision
	}

	live, err := readOperatorLiveAllocation(ctx, tx, appID)
	if err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	if current == nil {
		if live != nil {
			review, err := readAppAccessReservationReview(ctx, tx, *live, headNumber)
			if err != nil {
				return AppAccessOperatorSnapshot{}, err
			}
			result.PendingReservation = &review
			if err := requireNoOperatorClaims(ctx, tx, live.ID); err != nil {
				return AppAccessOperatorSnapshot{}, err
			}
		}
		return result, nil
	}

	if current.Allocation.ReleasedAt == nil {
		if live == nil || live.ID != current.Allocation.ID {
			return AppAccessOperatorSnapshot{}, ErrInvalidStoredState
		}
		if err := validateOperatorCurrentProfile(ctx, tx, current.Allocation); err != nil {
			return AppAccessOperatorSnapshot{}, err
		}
		value := *current
		result.DesiredAccess = &value
	} else if live != nil {
		review, err := readAppAccessReservationReview(ctx, tx, *live, headNumber)
		if err != nil {
			return AppAccessOperatorSnapshot{}, err
		}
		result.PendingReservation = &review
		if err := requireNoOperatorClaims(ctx, tx, live.ID); err != nil {
			return AppAccessOperatorSnapshot{}, err
		}
	}

	disable, hasDisable, err := readOperatorCurrentDisable(ctx, tx, *current)
	if err != nil {
		return AppAccessOperatorSnapshot{}, err
	}
	if hasDisable {
		result.DisableClaim = &disable
	} else if current.Allocation.ReleasedAt != nil {
		return AppAccessOperatorSnapshot{}, ErrInvalidStoredState
	} else {
		spec := AppAccessDisableSpecFor(*current)
		digest, err := AppAccessDisableSpecDigest(spec)
		if err != nil || !validDigest(digest) {
			return AppAccessOperatorSnapshot{}, ErrInvalidStoredState
		}
		result.DisableReview = &AppAccessDisableReview{Spec: spec, ApprovalDigest: digest}
	}

	if result.DesiredAccess != nil {
		grant, found, err := readOperatorCurrentGrant(ctx, tx, *current)
		if err != nil {
			return AppAccessOperatorSnapshot{}, err
		}
		if found {
			result.GrantClaim = &grant
		}
	}
	return result, nil
}

func readOperatorLiveAllocation(ctx context.Context, tx *sql.Tx, appID string) (*Allocation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM lan_port_allocations
		WHERE app_id=? AND released_at IS NULL AND disabled_at IS NULL ORDER BY id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		if len(ids) > 1 {
			return nil, ErrInvalidStoredState
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	value, err := readAllocationByID(ctx, tx, ids[0])
	if err != nil {
		return nil, operatorSnapshotStoredError(err)
	}
	if value.AppID != appID || value.ReleasedAt != nil {
		return nil, ErrInvalidStoredState
	}
	return &value, nil
}

func readAppAccessReservationReview(ctx context.Context, tx *sql.Tx, allocation Allocation, expected int64) (AppAccessReservationReview, error) {
	if allocation.ReleasedAt != nil || allocation.State != AllocationReserved || allocation.OwnerRevisionID != "" {
		return AppAccessReservationReview{}, ErrInvalidStoredState
	}
	if err := validateOperatorCurrentProfile(ctx, tx, allocation); err != nil {
		return AppAccessReservationReview{}, err
	}
	wantReservationDigest, err := reservationRequestDigest(ReserveAppAccessInput{
		AppID: allocation.AppID, OperationID: allocation.OwnerOperationID,
		ExpectedRevisionNumber: expected, GatewayProfileRevisionID: allocation.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: allocation.GatewayProfileRevisionNumber,
	})
	if err != nil || wantReservationDigest != allocation.ReservationDigest {
		return AppAccessReservationReview{}, ErrInvalidStoredState
	}
	approvalDigest, err := AppAccessSpecDigest(AppAccessSpecFor(allocation))
	if err != nil || !validDigest(approvalDigest) {
		return AppAccessReservationReview{}, ErrInvalidStoredState
	}
	return AppAccessReservationReview{
		Allocation: allocation, ExpectedRevisionNumber: expected, ApprovalDigest: approvalDigest,
	}, nil
}

func validateOperatorCurrentProfile(ctx context.Context, tx *sql.Tx, allocation Allocation) error {
	var profileID sql.NullString
	var profileNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&profileID, &profileNumber); err != nil {
		return operatorSnapshotStoredError(err)
	}
	if !profileID.Valid || profileNumber <= 0 || profileID.String != allocation.GatewayProfileRevisionID ||
		profileNumber != allocation.GatewayProfileRevisionNumber {
		return ErrInvalidStoredState
	}
	profile, _, err := readGatewayRevision(ctx, tx, profileID.String, profileNumber)
	if err != nil {
		return operatorSnapshotStoredError(err)
	}
	if profile.ID != allocation.GatewayProfileRevisionID || profile.RevisionNumber != allocation.GatewayProfileRevisionNumber {
		return ErrInvalidStoredState
	}
	return nil
}

func validateOperatorCurrentAccessRevision(revision AppAccessRevision, requestDigest string) error {
	if revision.RevisionNumber <= 0 || !validUUID(revision.ApprovedBy) {
		return ErrInvalidStoredState
	}
	wantReservationDigest, err := reservationRequestDigest(ReserveAppAccessInput{
		AppID: revision.AppID, OperationID: revision.OperationID,
		ExpectedRevisionNumber:       revision.RevisionNumber - 1,
		GatewayProfileRevisionID:     revision.Allocation.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: revision.Allocation.GatewayProfileRevisionNumber,
	})
	if err != nil || wantReservationDigest != revision.Allocation.ReservationDigest {
		return ErrInvalidStoredState
	}
	wantRequestDigest, err := approvalRequestDigest(ApproveAppAccessInput{
		AppID: revision.AppID, OperationID: revision.OperationID,
		AllocationID: revision.Allocation.ID, ExpectedRevisionNumber: revision.RevisionNumber - 1,
		Approval: Approval{Action: ActionEnableAppAccess, SpecDigest: revision.SpecDigest, ActorID: revision.ApprovedBy},
	})
	if err != nil || wantRequestDigest != requestDigest {
		return ErrInvalidStoredState
	}
	return nil
}

func readOperatorCurrentGrant(ctx context.Context, tx *sql.Tx, revision AppAccessRevision) (AppAccessGrantClaim, bool, error) {
	attemptID, found, err := readSingleOperatorClaimID(ctx, tx, `SELECT attempt_id
		FROM lan_app_access_grant_claims
		WHERE allocation_id=? AND state IN ('prepared','applying','db_active','uncertain','committed')
		  AND retired_at IS NULL
		ORDER BY attempt_id`, revision.Allocation.ID)
	if err != nil || !found {
		return AppAccessGrantClaim{}, found, err
	}
	claim, err := readAppAccessGrantClaim(ctx, tx, attemptID)
	if err != nil {
		return AppAccessGrantClaim{}, false, operatorSnapshotStoredError(err)
	}
	profile, _, err := readGatewayRevision(ctx, tx, revision.Allocation.GatewayProfileRevisionID,
		revision.Allocation.GatewayProfileRevisionNumber)
	if err != nil {
		return AppAccessGrantClaim{}, false, operatorSnapshotStoredError(err)
	}
	if claim.Spec != AppAccessGrantSpecFor(revision, profile) ||
		!appAccessGrantAllocationStateMatches(claim.State, revision.Allocation.State) {
		return AppAccessGrantClaim{}, false, ErrInvalidStoredState
	}
	return claim, true, nil
}

func readOperatorCurrentDisable(ctx context.Context, tx *sql.Tx, revision AppAccessRevision) (AppAccessDisableClaim, bool, error) {
	operationID, found, err := readSingleOperatorClaimID(ctx, tx, `SELECT operation_id
		FROM lan_app_access_disable_claims WHERE allocation_id=? ORDER BY operation_id`, revision.Allocation.ID)
	if err != nil || !found {
		return AppAccessDisableClaim{}, found, err
	}
	claim, err := readAppAccessDisableClaim(ctx, tx, operationID)
	if err != nil {
		return AppAccessDisableClaim{}, false, operatorSnapshotStoredError(err)
	}
	if claim.Spec != AppAccessDisableSpecFor(revision) {
		return AppAccessDisableClaim{}, false, ErrInvalidStoredState
	}
	value, err := readAppAccessDisableAuthorization(ctx, tx, claim)
	if err != nil {
		return AppAccessDisableClaim{}, false, operatorSnapshotStoredError(err)
	}
	if value.Revision.ID != revision.ID || value.Revision.RevisionNumber != revision.RevisionNumber ||
		value.Allocation.ID != revision.Allocation.ID {
		return AppAccessDisableClaim{}, false, ErrInvalidStoredState
	}
	return claim, true, nil
}

func requireNoOperatorClaims(ctx context.Context, tx *sql.Tx, allocationID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_grant_claims
		WHERE allocation_id=? AND state IN ('prepared','applying','db_active','uncertain','committed')
		  AND retired_at IS NULL
		UNION ALL
		SELECT 1 FROM lan_app_access_disable_claims WHERE allocation_id=?
	)`, allocationID, allocationID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return ErrInvalidStoredState
	}
	return nil
}

func readSingleOperatorClaimID(ctx context.Context, tx *sql.Tx, statement, allocationID string) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, statement, allocationID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", false, err
		}
		ids = append(ids, id)
		if len(ids) > 1 {
			return "", false, ErrInvalidStoredState
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(ids) == 0 {
		return "", false, nil
	}
	return ids[0], true, nil
}

func operatorSnapshotStoredError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrApprovalRequired) || errors.Is(err, ErrReservationReleased) {
		return ErrInvalidStoredState
	}
	return err
}
