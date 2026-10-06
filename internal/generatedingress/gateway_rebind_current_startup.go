package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// The caller holds both gateway locks and has selected current authority from
// fresh SQL and the complete protected history. This path never converts a
// rebind receipt or operational state into a native upgrade journal.
func (m *Manager) inspectGatewayRebindCurrentStartupLocked(ctx context.Context,
	claims gatewayV2StartupClaimSet, current gatewayCurrentSelection,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) (GatewayV2StartupInspection, error) {
	invalid := gatewayV2StartupInspectionError(ctx)
	if current.Kind != gatewayCurrentSelectionRebind || current.State == nil ||
		snapshot.CurrentSource == nil || *snapshot.CurrentSource != gatewayCurrentAuthority(current.Lineage) ||
		snapshot.Active != nil || snapshot.Phase != "" || snapshot.RollbackAllowed ||
		snapshot.DatabaseCommitObserved || snapshot.DatabaseCommittedEvent != nil {
		return GatewayV2StartupInspection{}, invalid
	}
	history, beforeFiles, err := m.scanGatewayUpgradeHistoryLockedModeRebindAware(false, "")
	if err != nil || len(history.generations) != len(claims.byOperation) {
		return GatewayV2StartupInspection{}, invalid
	}
	committed := 0
	for _, selected := range history.generations {
		claim, ok := claims.byOperation[selected.operationID]
		if !ok || !gatewayV2StartupClaimMatchesSelection(claim, selected, m.options.HostPort) {
			return GatewayV2StartupInspection{}, invalid
		}
		switch {
		case gatewayV2StartupSelectionHasTerminalReceipt(selected):
			if claim.State != appaccess.GatewayProfileUpgradeRolledBack {
				return GatewayV2StartupInspection{}, invalid
			}
		case selected.Existing && !selected.Aborted && selected.Journal.Phase == gatewayPhaseCommitted:
			if claim.State != appaccess.GatewayProfileUpgradeCommitted || selected.State.Pending != nil || selected.State.LANRecovery != nil {
				return GatewayV2StartupInspection{}, invalid
			}
			committed++
		default:
			return GatewayV2StartupInspection{}, invalid
		}
	}
	if committed != 1 {
		return GatewayV2StartupInspection{}, invalid
	}
	proofCtx, cancel := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancel()
	physical, err := m.attestGatewayCurrentPhysicalLocked(proofCtx, current)
	if err != nil {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	inspection := GatewayV2StartupInspection{Disposition: GatewayV2StartupNormalV2,
		OperationID: current.Lineage.OperationID, CurrentGatewaySource: *snapshot.CurrentSource}
	if current.State.LANRecovery != nil || (current.State.Pending != nil &&
		current.State.Pending.Kind != "" && current.State.Pending.Kind != gatewayV2PendingRouteSwitch) {
		inspection.Disposition = GatewayV2StartupRecoveryOnly
	}
	// An ordinary pending route is handled by Recover before deployment workers
	// or HTTP admission. LAN recovery instead retains its request-bound surface.
	if current.State.Pending == nil && current.State.LANRecovery == nil && physical.Outcome != gatewayCurrentPhysicalStableServing {
		return GatewayV2StartupInspection{}, invalid
	}
	confirmed, confirmedSQL, present, err := m.readOptionalGatewayCurrentSelectionLocked(ctx)
	if err != nil || !present || !reflect.DeepEqual(snapshot, confirmedSQL) ||
		!sameGatewayCurrentSelection(current, confirmed) || ctx.Err() != nil {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	confirmedHistory, afterFiles, err := m.scanGatewayUpgradeHistoryLockedModeRebindAware(false, "")
	if err != nil || !sameGatewayV2StartupHistory(history, confirmedHistory) || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
		return GatewayV2StartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	return inspection, nil
}
