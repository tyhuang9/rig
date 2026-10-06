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
