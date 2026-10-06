package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// gatewayV2LANStartupAuthority binds the native startup consumer to the same
// current SQL authority and protected generation throughout its observation.
// Rebound generations require their own physical attestor, not a native journal.
type gatewayV2LANStartupAuthority struct {
	selection gatewayCurrentSelection
	snapshot  appaccess.GatewayRebindRecoverySnapshot
	present   bool
}

func (m *Manager) readGatewayV2LANStartupAuthorityLocked(ctx context.Context,
	store *gatewayUpgradeStateStore, state gatewayV2RouteState, journal gatewayMigrationJournal,
	grants gatewayV2LANStartupClaimSet, disables map[string]GatewayV2LANDisableStartupClaim,
) (gatewayV2LANStartupAuthority, error) {
	invalid := gatewayV2StartupInspectionError(ctx)
	selection, snapshot, present, err := m.readOptionalGatewayCurrentSelectionLocked(ctx)
	if err != nil || snapshot.Active != nil || snapshot.Phase != "" || snapshot.DatabaseCommittedEvent != nil ||
		snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed {
		return gatewayV2LANStartupAuthority{}, invalid
	}
	var proof GatewayV2LANEffectiveBindingProof
	if present {
		if selection.Kind != gatewayCurrentSelectionUpgrade || selection.Upgrade == nil || selection.UpgradeSource == nil ||
			!sameGatewayV2PreparationAbortStore(store, selection.Upgrade.Store) ||
			!reflect.DeepEqual(state, selection.Upgrade.State) || !reflect.DeepEqual(journal, selection.Upgrade.Journal) {
			return gatewayV2LANStartupAuthority{}, invalid
		}
		proof, err = gatewayUpgradeEffectiveProof(*selection.Upgrade)
		if err != nil {
			return gatewayV2LANStartupAuthority{}, invalid
		}
	}
	for _, claim := range grants.byAttempt {
		required := present && claim.State != appaccess.AppAccessGrantPrepared && claim.State != appaccess.AppAccessGrantRolledBack
		if !gatewayV2LANStartupProjectionsMatchNative(claim.CurrentBinding, claim.RetainedBinding, proof, present, required) {
			return gatewayV2LANStartupAuthority{}, invalid
		}
	}
	for _, claim := range disables {
		required := present && (claim.Request.SourceGrant != nil || claim.State == appaccess.AppAccessDisableWithdrawing ||
			claim.State == appaccess.AppAccessDisableUncertain)
		if !gatewayV2LANStartupProjectionsMatchNative(claim.CurrentBinding, claim.RetainedBinding, proof, present, required) {
			return gatewayV2LANStartupAuthority{}, invalid
		}
	}
	return gatewayV2LANStartupAuthority{selection: selection, snapshot: snapshot, present: present}, nil
}

func gatewayV2LANStartupProjectionsMatchNative(current, retained *GatewayV2LANStartupBindingProjection,
	proof GatewayV2LANEffectiveBindingProof, present, required bool,
) bool {
	if current != nil && retained != nil {
		return false
	}
	projection := current
	if retained != nil {
		projection = retained
	}
	if projection == nil {
		return !required
	}
	return present && validGatewayV2LANStartupProjection(*projection) &&
		projection.EffectiveProfile == proof.EffectiveProfile &&
		projection.GatewaySource == gatewayCurrentAuthority(proof.ProtectedLineage) &&
		len(projection.TransferChain) == 0 && projection.TransferChainTipDigest == "" && projection.TerminalReceiptDigest == ""
}

func (m *Manager) confirmGatewayV2LANStartupAuthorityLocked(ctx context.Context,
	before gatewayV2LANStartupAuthority, store *gatewayUpgradeStateStore,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
	grants gatewayV2LANStartupClaimSet, disables map[string]GatewayV2LANDisableStartupClaim,
) error {
	after, err := m.readGatewayV2LANStartupAuthorityLocked(ctx, store, state, journal, grants, disables)
	if err != nil || before.present != after.present || !reflect.DeepEqual(before.snapshot, after.snapshot) ||
		!sameGatewayCurrentSelection(before.selection, after.selection) ||
		!reflect.DeepEqual(before.selection.UpgradeSource, after.selection.UpgradeSource) || ctx.Err() != nil {
		return gatewayV2StartupInspectionError(ctx)
	}
	return nil
}

// A quarantine may durably add a Pending marker. Revalidate the new native
// state against unchanged SQL and immutable authority without requiring the
// operational state to equal its pre-write value.
func (m *Manager) confirmGatewayV2LANStartupMutationLocked(ctx context.Context,
	before gatewayV2LANStartupAuthority, store *gatewayUpgradeStateStore, journal gatewayMigrationJournal,
	grants gatewayV2LANStartupClaimSet, disables map[string]GatewayV2LANDisableStartupClaim,
) (gatewayV2RouteState, error) {
	state, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retainedJournal, journal) {
		return gatewayV2RouteState{}, gatewayV2StartupInspectionError(ctx)
	}
	after, err := m.readGatewayV2LANStartupAuthorityLocked(ctx, store, state, journal, grants, disables)
	if err != nil || before.present != after.present || !reflect.DeepEqual(before.snapshot, after.snapshot) ||
		before.selection.Kind != after.selection.Kind || before.selection.Lineage != after.selection.Lineage ||
		!reflect.DeepEqual(before.selection.UpgradeSource, after.selection.UpgradeSource) || ctx.Err() != nil {
		return gatewayV2RouteState{}, gatewayV2StartupInspectionError(ctx)
	}
	return state, nil
}

func (m *Manager) confirmGatewayV2LANStartupMutationResultLocked(ctx context.Context,
	before gatewayV2LANStartupAuthority, store *gatewayUpgradeStateStore, expected gatewayV2RouteState,
	journal gatewayMigrationJournal, grants gatewayV2LANStartupClaimSet, disables map[string]GatewayV2LANDisableStartupClaim,
) error {
	state, err := m.confirmGatewayV2LANStartupMutationLocked(ctx, before, store, journal, grants, disables)
	if err != nil || !reflect.DeepEqual(state, expected) {
		return gatewayV2StartupInspectionError(ctx)
	}
	return nil
}

// Recheck at the effect boundary, after the protected pending write and any
// physical observation. Only the exact withdrawal derived from that retained
// marker may be applied; failure preserves the marker for recovery.
type gatewayV2LANStartupMutationDriver struct {
	gatewayV2LANGrantDriver
	manager   *Manager
	authority gatewayV2LANStartupAuthority
	store     *gatewayUpgradeStateStore
	journal   gatewayMigrationJournal
	grants    gatewayV2LANStartupClaimSet
	disables  map[string]GatewayV2LANDisableStartupClaim
	// The exact durable marker read before the only apply, retained for the
	// caller's final comparison after the core's physical proof/readback.
	appliedPending *gatewayV2RouteState
}

func (d *gatewayV2LANStartupMutationDriver) apply(ctx context.Context, proposed gatewayV2RouteState, name string) error {
	state, err := d.manager.confirmGatewayV2LANStartupMutationLocked(ctx, d.authority, d.store, d.journal, d.grants, d.disables)
	if err != nil || state.Pending == nil {
		return gatewayV2StartupInspectionError(ctx)
	}
	var withdrawn gatewayV2RouteState
	if state.Pending.Kind == gatewayV2PendingLANDisable {
		if state.Pending.Disable == nil {
			return gatewayV2StartupInspectionError(ctx)
		}
		request := *state.Pending.Disable
		claim, exists := d.disables[request.OperationID]
		if !exists || !reflect.DeepEqual(claim.Request, request) {
			return gatewayV2StartupInspectionError(ctx)
		}
		_, withdrawn, err = gatewayV2LANDisablePendingStates(state, request)
	} else {
		request, requestErr := gatewayV2LANPendingRequest(*state.Pending)
		claim, exists := d.grants.byAttempt[request.AttemptID]
		if requestErr != nil || !exists || claim.Request != request {
			return gatewayV2StartupInspectionError(ctx)
		}
		var pending bool
		withdrawn, _, pending, err = gatewayV2LANGrantStatesForRequest(state, request)
		if !pending {
			return gatewayV2StartupInspectionError(ctx)
		}
	}
	if err != nil || !reflect.DeepEqual(withdrawn, proposed) {
		return gatewayV2StartupInspectionError(ctx)
	}
	pending := cloneGatewayV2RouteState(state)
	d.appliedPending = &pending
	return d.gatewayV2LANGrantDriver.apply(ctx, proposed, name)
}
