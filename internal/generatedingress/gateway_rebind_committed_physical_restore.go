package generatedingress

import (
	"context"
	"reflect"
)

func (d managedGatewayCurrentPhysicalDriver) restoreGatewayRebindCommittedServing(ctx context.Context,
	action gatewayRebindCommittedServingRestoreAction, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if authorize == nil || !validGatewayRebindCommittedServingRestoreAction(action) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	target, err := d.selectGatewayRebindCommittedServingExact(ctx, action)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	guard := func(effectCtx context.Context) error {
		if effectCtx == nil || effectCtx.Err() != nil || authorize(effectCtx) != nil {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		fresh, err := d.selectGatewayRebindCommittedServingExact(effectCtx, action)
		if err != nil || !reflect.DeepEqual(target, fresh) {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		return nil
	}
	return d.restoreGatewayCurrentServingAtTarget(ctx, target, gatewayCurrentPhysicalStableServing, guard)
}

func (d managedGatewayCurrentPhysicalDriver) selectGatewayRebindCommittedServingExact(ctx context.Context,
	action gatewayRebindCommittedServingRestoreAction,
) (gatewayCurrentPhysicalTarget, error) {
	m := d.manager()
	if m == nil || ctx == nil || ctx.Err() != nil || m.gatewayRebindAdmissionBlocked() ||
		!validGatewayRebindCommittedServingRestoreAction(action) {
		return gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	selection, snapshot, err := m.readGatewayCurrentSelectionLocked(ctx)
	if err != nil || !gatewayRebindCommittedServingSnapshotMatches(action.Request, snapshot) ||
		selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil ||
		!reflect.DeepEqual(*selection.State, action.State) || m.gatewayRebindAdmissionBlocked() {
		return gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil || terminal.Format != gatewayRebindAttemptTerminalTypedV2 ||
		!reflect.DeepEqual(terminal.TypedReceipt, action.Request.Terminal) {
		return gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return gatewayCurrentPhysicalTargetFor(selection, action.State, nil, nil)
}

var _ gatewayRebindCommittedServingRestoreDriver = managedGatewayCurrentPhysicalDriver{}
