package generatedingress

import (
	"context"
	"reflect"
)

// The caller holds both gateway locks. SQL selects the current generation;
// the complete census and retained history constrain its immutable queue.
// Observation neither changes a head nor clears a protected binding.
func (m *Manager) observeGatewayCurrentLANRecoveryHeadLocked(ctx context.Context,
	claims gatewayV2LANAccessStartupClaims,
) (GatewayV2LANRecoveryHead, bool, bool, error) {
	selection, snapshot, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANRecoveryHead{}, false, handled, err
	}
	state := *selection.State
	if state.LANRecovery == nil {
		confirmed, confirmedSQL, selected, err := m.readOptionalGatewayCurrentSelectionLocked(ctx)
		if err != nil || !selected || !reflect.DeepEqual(snapshot, confirmedSQL) ||
			!sameGatewayCurrentSelection(selection, confirmed) || ctx.Err() != nil {
			return GatewayV2LANRecoveryHead{}, false, true, gatewayV2LANDisableError(ctx)
		}
		return GatewayV2LANRecoveryHead{}, false, true, nil
	}
	if !gatewayCurrentLANRecoveryCensusMatchesHead(state, claims, snapshot.CurrentTransfers) {
		return GatewayV2LANRecoveryHead{}, true, true, gatewayV2LANDisableError(ctx)
	}
	if err := m.validateGatewayCurrentLANRetainedHistoryLocked(ctx, claims, snapshot); err != nil {
		return GatewayV2LANRecoveryHead{}, true, true, err
	}
	proof, err := m.attestGatewayCurrentLANRecoveryBatchLocked(ctx, selection)
	if err != nil || !proof.BatchAbsent {
		return GatewayV2LANRecoveryHead{}, true, true, gatewayV2LANDisableError(ctx)
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(ctx, snapshot, state, claims); err != nil {
		return GatewayV2LANRecoveryHead{}, true, true, err
	}
	batch := state.LANRecovery
	head := GatewayV2LANRecoveryHead{Head: batch.Head, Count: len(batch.Items)}
	if batch.Head == len(batch.Items) {
		return head, true, true, nil
	}
	item := batch.Items[batch.Head]
	head.AppID = item.AppID
	switch item.Kind {
	case gatewayV2PendingLANGrant:
		head.Kind, head.OperationID = GatewayV2LANRecoveryGrant, item.Grant.Raw.GrantAttemptID
	case gatewayV2PendingLANDisable:
		head.Kind, head.OperationID = GatewayV2LANRecoveryDisable, item.Disable.OperationID
	default:
		return GatewayV2LANRecoveryHead{}, true, true, gatewayV2LANDisableError(ctx)
	}
	return head, true, true, nil
}
