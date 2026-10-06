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
	Claim                          AppAccessGrantClaim
	Revision                       AppAccessRevision
	Allocation                     Allocation
	Profile                        GatewayProfileRevision
	EffectiveProfile               GatewayProfileRevision
	CurrentGatewaySource           GatewayCurrentAuthorityRef
	TransferChain                  []GatewayRebindAllocationTransfer
	TransferChainTipDigest         string
	TerminalReceiptDigest          string
	RetainedEffectiveProfile       GatewayProfileRevision
	RetainedGatewaySource          GatewayCurrentAuthorityRef
	RetainedTransferChain          []GatewayRebindAllocationTransfer
	RetainedTransferChainTipDigest string
	RetainedTerminalReceiptDigest  string
	AppArchived                    bool
	AccessHeadCurrent              bool
	ProfileHeadCurrent             bool
	ApproverIsAdministrator        bool
	DisableIntent                  *AppAccessDisableIntent
}

type AppAccessGrantStartupSnapshot struct {
	Claims []AppAccessGrantStartupClaim
}

// HostingGatewayStartupSnapshot is the single durable startup census used by
// hostd before it starts normal workers. Gateway upgrade, grant, disable and
// current rebind facts are observed under the same SQLite read transaction.
type HostingGatewayStartupSnapshot struct {
	Upgrades          GatewayUpgradeStartupSnapshot
	Grants            AppAccessGrantStartupSnapshot
	Disables          AppAccessDisableStartupSnapshot
	Rebind            GatewayRebindRecoverySnapshot
	RuntimeHeads      []GatewayRebindRuntimeHead
	RuntimeComponents []GatewayStartupRuntimeComponent
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

// HostingGatewayStartupSnapshot reads gateway upgrade claims, all LAN attempts,
// current rebind authority and complete active runtime heads/components from
// one SQLite snapshot. Consumers
// must still compare these facts with protected state before starting workers or exposing
// URLs; this method performs no external observation or recovery.
func (r *Repository) HostingGatewayStartupSnapshot(ctx context.Context) (HostingGatewayStartupSnapshot, error) {
	if r == nil || r.db == nil || ctx == nil {
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
	disables, err := r.readAppAccessDisableStartupSnapshot(ctx, tx)
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	rebind, err := readGatewayRebindRecoverySnapshot(ctx, tx)
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	heads, err := readGatewayRebindRuntimeHeads(ctx, tx)
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	components, err := readGatewayStartupRuntimeComponents(ctx, tx, heads)
	if err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return HostingGatewayStartupSnapshot{}, err
	}
	return HostingGatewayStartupSnapshot{Upgrades: gateway, Grants: grants, Disables: disables, Rebind: rebind,
		RuntimeHeads: heads, RuntimeComponents: components}, nil
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
	var rebindRecovery *GatewayRebindRecoverySnapshot
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
		if claim.State == GatewayProfileUpgradePrepared || claim.State == GatewayProfileUpgradeServing ||
			claim.State == GatewayProfileUpgradeUnresolved {
			activeClaims++
			if activeClaims > 1 || current == nil || actorRole != "administrator" ||
				current.ID != profile.ID || current.RevisionNumber != profile.RevisionNumber ||
				current.SpecDigest != profile.SpecDigest {
				return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
			}
		} else if claim.State == GatewayProfileUpgradeCommitted {
			currentMatches := current != nil && current.ID == profile.ID &&
				current.RevisionNumber == profile.RevisionNumber && current.SpecDigest == profile.SpecDigest
			if currentMatches {
				activeClaims++
				selectedProfile, selectedSource, readErr := readGatewayCurrentAuthority(ctx, tx)
				if readErr != nil || activeClaims > 1 || actorRole != "administrator" || selectedProfile != profile ||
					selectedSource.Kind != GatewayRebindSourceGatewayUpgrade ||
					selectedSource.OperationID != claim.OperationID ||
					selectedSource.ProfileRevisionID != profile.ID ||
					selectedSource.ProfileRevisionNumber != profile.RevisionNumber ||
					selectedSource.ProfileSpecDigest != profile.SpecDigest {
					return GatewayUpgradeStartupSnapshot{}, invalidRebindStoredState(readErr)
				}
			} else {
				if rebindRecovery == nil {
					value, readErr := readGatewayRebindRecoverySnapshot(ctx, tx)
					if readErr != nil {
						return GatewayUpgradeStartupSnapshot{}, readErr
					}
					rebindRecovery = &value
				}
				if !gatewayRebindRecoveryDescendsFromUpgrade(*rebindRecovery, claim, profile) {
					return GatewayUpgradeStartupSnapshot{}, ErrInvalidStoredState
				}
			}
		}
		result.Claims = append(result.Claims, GatewayUpgradeStartupClaim{
			Claim: claim, Profile: profile, ApprovedActionDigest: actionDigest,
		})
	}
	return result, nil
}

func gatewayRebindRecoveryDescendsFromUpgrade(snapshot GatewayRebindRecoverySnapshot,
	claim GatewayProfileUpgradeClaim, profile GatewayProfileRevision,
) bool {
	if snapshot.CurrentProfile == nil || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.Kind != GatewayRebindSourceGatewayRebind ||
		snapshot.CurrentSource.ProfileRevisionID != snapshot.CurrentProfile.ID ||
		snapshot.CurrentSource.ProfileRevisionNumber != snapshot.CurrentProfile.RevisionNumber ||
		snapshot.CurrentSource.ProfileSpecDigest != snapshot.CurrentProfile.SpecDigest {
		return false
	}
	authority := *snapshot.CurrentSource
	seen := make(map[string]struct{}, len(snapshot.History))
	for authority.Kind == GatewayRebindSourceGatewayRebind {
		if _, duplicate := seen[authority.OperationID]; duplicate {
			return false
		}
		seen[authority.OperationID] = struct{}{}
		history := findGatewayRebindHistory(snapshot.History, authority.OperationID)
		if history == nil || retainedGatewayRebindDisposition(history.Commands) != GatewayRebindDispositionCommit ||
			!gatewayRebindHistoryHasDatabaseCommit(*history) {
			return false
		}
		successorID, successorNumber, successorDigest := historyClaimSuccessorProfile(history.Claim)
		if authority.ProfileRevisionID != successorID || authority.ProfileRevisionNumber != successorNumber ||
			authority.ProfileSpecDigest != successorDigest ||
			authority.TerminalReceiptDigest != retainedGatewayRebindReceipt(history.Commands) {
			return false
		}
		authority = historyClaimPredecessorAuthority(history.Claim)
	}
	return authority.Kind == GatewayRebindSourceGatewayUpgrade && authority.OperationID == claim.OperationID &&
		authority.ProfileRevisionID == profile.ID && authority.ProfileRevisionNumber == profile.RevisionNumber &&
		authority.ProfileSpecDigest == profile.SpecDigest && authority.TerminalReceiptDigest == ""
}

func gatewayRebindHistoryHasDatabaseCommit(history GatewayRebindHistoryEntry) bool {
	for _, event := range history.Events {
		if event.State == GatewayRebindDatabaseCommitted {
			return true
		}
	}
	return false
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
		revision.Allocation.Port != claim.Spec.Port ||
		revision.Allocation.GatewayProfileRevisionID != claim.Spec.GatewayProfileRevisionID ||
		revision.Allocation.GatewayProfileRevisionNumber != claim.Spec.GatewayProfileRevisionNumber ||
		profile.SpecDigest != claim.Spec.GatewayProfileSpecDigest ||
		!appAccessGrantAllocationStateMatches(claim.State, revision.Allocation.State) {
		return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
	}
	var disableIntent *AppAccessDisableIntent
	var disableOperationID string
	err = tx.QueryRowContext(ctx, `SELECT operation_id FROM lan_app_access_disable_intents
		WHERE allocation_id=?`, claim.Spec.AllocationID).Scan(&disableOperationID)
	if err == nil {
		intent, readErr := readAppAccessDisableIntent(ctx, tx, disableOperationID)
		if readErr != nil || intent.Spec != AppAccessDisableSpecFor(revision) {
			if readErr != nil {
				return AppAccessGrantStartupClaim{}, readErr
			}
			return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
		}
		disableIntent = &intent
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessGrantStartupClaim{}, err
	}
	if claim.RetiredAt == nil {
		if revision.Allocation.ReleasedAt != nil {
			matches, matchErr := releasedRolledBackGrantMatchesDisable(ctx, tx, claim, revision, disableIntent)
			if matchErr != nil {
				return AppAccessGrantStartupClaim{}, matchErr
			}
			if !matches {
				return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
			}
		}
	} else if claim.State != AppAccessGrantCommitted || revision.Allocation.ReleasedAt == nil ||
		!revision.Allocation.ReleasedAt.Equal(*claim.RetiredAt) {
		return AppAccessGrantStartupClaim{}, ErrInvalidStoredState
	}

	value := AppAccessGrantStartupClaim{
		Claim: claim, Revision: revision, Allocation: revision.Allocation,
		Profile: profile, EffectiveProfile: profile,
		DisableIntent: disableIntent,
	}
	if claim.State == AppAccessGrantCommitted {
		resolution, resolveErr := resolveGatewayBindingForStoredGrant(ctx, tx, GatewayBindingRef{
			AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
			AccessRevisionID: claim.Spec.AccessRevisionID, GrantAttemptID: claim.AttemptID,
		}, profile, claim)
		if resolveErr != nil || resolution.RawProfile.ID != profile.ID ||
			resolution.RawProfile.RevisionNumber != profile.RevisionNumber ||
			resolution.RawProfile.SpecDigest != profile.SpecDigest {
			return AppAccessGrantStartupClaim{}, invalidRebindStoredState(resolveErr)
		}
		if claim.RetiredAt != nil && resolution.CurrentGatewaySource.Kind != "" {
			value.EffectiveProfile = GatewayProfileRevision{}
			value.RetainedEffectiveProfile = resolution.EffectiveProfile
			value.RetainedGatewaySource = resolution.CurrentGatewaySource
			value.RetainedTransferChain = append([]GatewayRebindAllocationTransfer(nil), resolution.TransferChain...)
			value.RetainedTransferChainTipDigest = resolution.TransferChainTipDigest
			value.RetainedTerminalReceiptDigest = resolution.TerminalReceiptDigest
		} else if claim.RetiredAt == nil {
			value.EffectiveProfile = resolution.EffectiveProfile
			value.CurrentGatewaySource = resolution.CurrentGatewaySource
			value.TransferChain = append([]GatewayRebindAllocationTransfer(nil), resolution.TransferChain...)
			value.TransferChainTipDigest = resolution.TransferChainTipDigest
			value.TerminalReceiptDigest = resolution.TerminalReceiptDigest
		}
	} else if claim.State == AppAccessGrantApplying || claim.State == AppAccessGrantDBActive ||
		claim.State == AppAccessGrantUncertain {
		source, resolveErr := readOptionalGatewayCurrentAuthority(ctx, tx, profile)
		if resolveErr != nil {
			return AppAccessGrantStartupClaim{}, invalidRebindStoredState(resolveErr)
		}
		value.CurrentGatewaySource = source
		value.TerminalReceiptDigest = source.TerminalReceiptDigest
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
	)`, value.EffectiveProfile.ID, value.EffectiveProfile.RevisionNumber).Scan(&current); err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	value.ProfileHeadCurrent = current == 1
	administrator, err := administratorExists(ctx, tx, claim.Spec.ApprovedBy)
	if err != nil {
		return AppAccessGrantStartupClaim{}, err
	}
	value.ApproverIsAdministrator = administrator

	return value, nil
}

// A migration-028 prepared grant can legitimately coexist with a migration-027
// disable intent. If recovery proves the grant rolled back and then commits the
// exact backfilled disable, migration 029 releases the allocation without
// retiring the rolled-back claim. Accept that retained history only when the
// immutable disable claim and its terminal proof explain the exact release.
func releasedRolledBackGrantMatchesDisable(ctx context.Context, tx *sql.Tx,
	claim AppAccessGrantClaim, revision AppAccessRevision, intent *AppAccessDisableIntent,
) (bool, error) {
	if claim.State != AppAccessGrantRolledBack || claim.RetiredAt != nil ||
		revision.Allocation.ReleasedAt == nil || intent == nil {
		return false, nil
	}
	disable, err := readAppAccessDisableClaim(ctx, tx, intent.OperationID)
	if err != nil {
		return false, err
	}
	if disable.State != AppAccessDisableCommitted || disable.Proof == nil ||
		disable.Spec != AppAccessDisableSpecFor(revision) ||
		!revision.Allocation.ReleasedAt.Equal(disable.Proof.ObservedAt) {
		return false, nil
	}
	if disable.SourceGrantAttemptID == "" {
		return true, nil
	}
	if disable.SourceGrantAttemptID == claim.AttemptID {
		return false, nil
	}
	source, err := readAppAccessGrantClaim(ctx, tx, disable.SourceGrantAttemptID)
	if err != nil {
		return false, err
	}
	return source.State == AppAccessGrantCommitted &&
		source.Spec.AppID == disable.Spec.AppID &&
		source.Spec.AllocationID == disable.Spec.AllocationID &&
		source.Spec.OwnerOperationID == disable.Spec.OwnerOperationID &&
		source.Spec.AccessRevisionID == disable.Spec.AccessRevisionID &&
		source.RetiredAt != nil && source.RetiredAt.Equal(disable.Proof.ObservedAt) &&
		source.RetiredByDisableOperationID == disable.OperationID, nil
}
