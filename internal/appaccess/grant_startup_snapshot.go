package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

// AppAccessGrantStartupClaim contains the retained database side of one grant
// attempt and the current facts required for fail-closed startup comparison
// with protected gateway state. Historical rolled-back attempts are included
// so a protected binding can be matched by AttemptID and rejected precisely.
type AppAccessGrantStartupClaim struct {
	Claim                   AppAccessGrantClaim
	Revision                AppAccessRevision
	Allocation              Allocation
	Profile                 GatewayProfileRevision
	AppArchived             bool
	AccessHeadCurrent       bool
	ProfileHeadCurrent      bool
	ApproverIsAdministrator bool
	DisableIntent           *AppAccessDisableIntent
}

type AppAccessGrantStartupSnapshot struct {
	Claims []AppAccessGrantStartupClaim
}

// HostingGatewayStartupSnapshot is the single durable startup census used by
// hostd before it starts normal workers. Gateway upgrade and per-app grant
// facts are observed under the same SQLite read transaction.
type HostingGatewayStartupSnapshot struct {
	Upgrades GatewayUpgradeStartupSnapshot
	Grants   AppAccessGrantStartupSnapshot
}

// AppAccessGrantStartupSnapshot reads every retained attempt, immutable event
// chain, exact allocation/revision/profile, current heads, administrator role,
// and disable status from one SQLite snapshot. It performs no recovery and is
// not serving proof. The controller must compare it with protected gateway
// state before starting workers or reporting URLs.
func (r *Repository) AppAccessGrantStartupSnapshot(ctx context.Context) (AppAccessGrantStartupSnapshot, error) {
	if r == nil || r.db == nil {
		return AppAccessGrantStartupSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AppAccessGrantStartupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := r.readAppAccessGrantStartupSnapshot(ctx, tx)
	if err != nil {
		return AppAccessGrantStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppAccessGrantStartupSnapshot{}, err
	}
	return snapshot, nil
}

// HostingGatewayStartupSnapshot reads gateway upgrade claims and all LAN grant
// attempts from one SQLite snapshot. Consumers must still compare these facts
// with protected generated-ingress state before starting workers or exposing
// URLs; this method performs no external observation or recovery.
func (r *Repository) HostingGatewayStartupSnapshot(ctx context.Context) (HostingGatewayStartupSnapshot, error) {
	if r == nil || r.db == nil {
		return HostingGatewayStartupSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	defer tx.Rollback()
	gateway, err := r.readGatewayUpgradeStartupSnapshot(ctx, tx)
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	grants, err := r.readAppAccessGrantStartupSnapshot(ctx, tx)
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	return HostingGatewayStartupSnapshot{Upgrades: gateway, Grants: grants}, nil
}

func (r *Repository) readAppAccessGrantStartupSnapshot(ctx context.Context, tx *sql.Tx) (AppAccessGrantStartupSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id
		FROM lan_app_access_grant_claims ORDER BY created_at,attempt_id`)
	if err != nil {
		return AppAccessGrantStartupSnapshot{}, err
	}
	var attemptIDs []string
	for rows.Next() {
		var attemptID string
		if err := rows.Scan(&attemptID); err != nil {
			_ = rows.Close()
			return AppAccessGrantStartupSnapshot{}, err
		}
		attemptIDs = append(attemptIDs, attemptID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return AppAccessGrantStartupSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return AppAccessGrantStartupSnapshot{}, err
	}
	if r.afterGrantStartupClaimsRead != nil {
		r.afterGrantStartupClaimsRead()
	}

	snapshot := AppAccessGrantStartupSnapshot{Claims: make([]AppAccessGrantStartupClaim, 0, len(attemptIDs))}
	for _, attemptID := range attemptIDs {
		value, err := readAppAccessGrantStartupClaim(ctx, tx, attemptID)
		if err != nil {
			return AppAccessGrantStartupSnapshot{}, err
		}
		snapshot.Claims = append(snapshot.Claims, value)
	}
	return snapshot, nil
}

func (r *Repository) readGatewayUpgradeStartupSnapshot(ctx context.Context, tx *sql.Tx) (GatewayUpgradeStartupSnapshot, error) {
	current, err := readGatewayUpgradeStartupHead(ctx, tx)
	if err != nil {
		return GatewayUpgradeStartupSnapshot{}, err
	}
	operationIDs, err := readGatewayUpgradeStartupOperationIDs(ctx, tx)
	if err != nil {
		return GatewayUpgradeStartupSnapshot{}, err
	}
	if r.afterUpgradeStartupClaimsRead != nil {
		r.afterUpgradeStartupClaimsRead()
	}
	result := GatewayUpgradeStartupSnapshot{
		CurrentProfile: current,
		Claims:         make([]GatewayUpgradeStartupClaim, 0, len(operationIDs)),
	}
	activeClaims := 0
	for _, operationID := range operationIDs {
		claim, err := readGatewayProfileUpgradeClaim(ctx, tx, operationID)
		if errors.Is(err, sql.ErrNoRows) {
			return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
		}
		if err != nil {
			return GatewayUpgradeStartupSnapshot{}, err
		}
		if err := validateGatewayProfileUpgradeClaimHistory(ctx, tx, claim); err != nil {
			return GatewayUpgradeStartupSnapshot{}, err
		}
		profile, requestDigest, err := readGatewayRevision(ctx, tx, claim.ProfileRevisionID, claim.ProfileRevisionNumber)
		if errors.Is(err, sql.ErrNoRows) {
			return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
		}
		if err != nil {
			return GatewayUpgradeStartupSnapshot{}, err
		}
		if err := validateGatewayUpgradeStartupProfile(ctx, tx, profile, requestDigest); err != nil {
			return GatewayUpgradeStartupSnapshot{}, err
		}
		if profile.ID != claim.ProfileRevisionID || profile.RevisionNumber != claim.ProfileRevisionNumber ||
			profile.SpecDigest != claim.ProfileSpecDigest {
			return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
		}
		actionDigest, err := GatewayProfileUpgradeSpecDigest(GatewayProfileUpgradeSpec{
			ProfileRevisionID: claim.ProfileRevisionID, ProfileRevisionNumber: claim.ProfileRevisionNumber,
			ProfileSpecDigest: claim.ProfileSpecDigest,
		})
		if err != nil || !validDigest(actionDigest) {
			return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
		}
		actorRole, err := gatewayUpgradeStartupActorRole(ctx, tx, claim.ApprovedBy)
		if errors.Is(err, sql.ErrNoRows) {
			return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
		}
		if err != nil {
			return GatewayUpgradeStartupSnapshot{}, err
		}
		if claim.State != GatewayProfileUpgradeRolledBack {
			activeClaims++
			if activeClaims > 1 || current == nil || actorRole != "administrator" ||
				current.ID != profile.ID || current.RevisionNumber != profile.RevisionNumber ||
				current.SpecDigest != profile.SpecDigest {
				return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
			}
		}
		result.Claims = append(result.Claims, GatewayUpgradeStartupClaim{
			Claim: claim, Profile: profile, ApprovedActionDigest: actionDigest,
		})
	}
	return result, nil
}

func readAppAccessGrantStartupClaim(ctx context.Context, tx *sql.Tx, attemptID string) (AppAccessGrantStartupClaim, error) {
	claim, err := readAppAccessGrantClaim(ctx, tx, attemptID)
	if err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	revision, _, err := readAccessRevision(ctx, tx, claim.Spec.AppID,
		claim.Spec.AccessRevisionID, claim.Spec.AccessRevisionNumber)
	if err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	profile, _, err := readGatewayRevision(ctx, tx, claim.Spec.GatewayProfileRevisionID,
		claim.Spec.GatewayProfileRevisionNumber)
	if err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	if revision.OperationID != claim.Spec.OwnerOperationID ||
		revision.SpecDigest != claim.Spec.AccessSpecDigest ||
		revision.ApprovedBy != claim.Spec.ApprovedBy ||
		revision.Allocation.ID != claim.Spec.AllocationID ||
		revision.Allocation.Port != claim.Spec.Port || revision.Allocation.ReleasedAt != nil ||
		revision.Allocation.GatewayProfileRevisionID != claim.Spec.GatewayProfileRevisionID ||
		revision.Allocation.GatewayProfileRevisionNumber != claim.Spec.GatewayProfileRevisionNumber ||
		profile.SpecDigest != claim.Spec.GatewayProfileSpecDigest ||
		!appAccessGrantAllocationStateMatches(claim.State, revision.Allocation.State) {
		return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
	}

	value := AppAccessGrantStartupClaim{
		Claim: claim, Revision: revision, Allocation: revision.Allocation, Profile: profile,
	}
	var archived sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM applications WHERE id=?`, claim.Spec.AppID).Scan(&archived); errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
	} else if err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	value.AppArchived = archived.Valid
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_heads
		WHERE app_id=? AND revision_id=? AND revision_number=?
	)`, claim.Spec.AppID, claim.Spec.AccessRevisionID, claim.Spec.AccessRevisionNumber).Scan(&current); err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	value.AccessHeadCurrent = current == 1
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_gateway_profile_heads
		WHERE singleton=1 AND revision_id=? AND revision_number=?
	)`, claim.Spec.GatewayProfileRevisionID, claim.Spec.GatewayProfileRevisionNumber).Scan(&current); err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	value.ProfileHeadCurrent = current == 1
	administrator, err := administratorExists(ctx, tx, claim.Spec.ApprovedBy)
	if err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	value.ApproverIsAdministrator = administrator

	var disableOperationID string
	err = tx.QueryRowContext(ctx, `SELECT operation_id FROM lan_app_access_disable_intents
		WHERE allocation_id=?`, claim.Spec.AllocationID).Scan(&disableOperationID)
	if err == nil {
		intent, readErr := readAppAccessDisableIntent(ctx, tx, disableOperationID)
		if readErr != nil || intent.Spec.AppID != claim.Spec.AppID ||
			intent.Spec.OwnerOperationID != claim.Spec.OwnerOperationID ||
			intent.Spec.AccessRevisionID != claim.Spec.AccessRevisionID {
			if readErr != nil {
				return AppAccessGrantStartupClaim{}, readErr
			}
			return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
		}
		value.DisableIntent = &intent
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantStartupClaim{}, err
	}
	return value, nil
}
