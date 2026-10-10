package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// The caller holds both gateway locks. Startup may withdraw a stale committed
// publication, but cannot resolve its SQL claim or clear the retained marker.
func (m *Manager) quarantineGatewayCurrentLANStartupLocked(ctx context.Context,
	claims gatewayV2LANAccessStartupClaims,
) (bool, error) {
	selection, snapshot, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentRouteState(*selection.State)
	inspection, err := gatewayCurrentLANStartupCensus(state, claims)
	if err != nil || len(inspection.Recoveries) != 0 {
		return true, gatewayV2StartupInspectionError(ctx)
	}
	if err := m.validateGatewayCurrentLANRetainedHistoryLocked(ctx, claims, snapshot); err != nil {
		return true, err
	}
	if _, err := m.attestGatewayCurrentStateLocked(ctx, state); err != nil {
		return true, err
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(ctx, snapshot, state, claims); err != nil {
		return true, err
	}
	if inspection.Disposition == GatewayV2LANStartupNormal {
		return true, nil
	}
	var transition gatewayCurrentPhysicalTransition
	switch inspection.RecoveryKind {
	case GatewayV2LANRecoveryGrant:
		request := claims.grants.byAttempt[inspection.OperationID].Request
		if state.Pending == nil {
			if state.Apps[request.AppID].LAN == nil {
				if _, _, err := gatewayCurrentGrantTransition(state, request); err != nil {
					return true, err
				}
				// The complete physical census already proved this unpublished
				// claim absent. Its explicit resolution still belongs to SQL.
				return true, nil
			}
			transition, err = gatewayCurrentGrantWithdrawalTransition(state, request)
		} else {
			if _, err := gatewayCurrentGrantObservation(state, request,
				GatewayV2LANGrantWithdrawnPendingReconciliation, state.Pending.ActivationUncertain); err != nil {
				return true, err
			}
			transition, err = gatewayCurrentTransitionFromPending(state)
		}
	case GatewayV2LANRecoveryDisable:
		request := claims.disables[inspection.OperationID].Request
		if state.Pending == nil {
			if state.Apps[request.AppID].LAN == nil {
				if !gatewayCurrentDisableBindingAbsent(state, request) {
					return true, gatewayV2StartupInspectionError(ctx)
				}
				return true, nil
			}
			transition, err = gatewayCurrentDisableTransition(state, request)
		} else {
			if state.Pending.Kind != gatewayV2PendingLANDisable || state.Pending.Disable == nil ||
				!reflect.DeepEqual(*state.Pending.Disable, request) {
				return true, gatewayV2StartupInspectionError(ctx)
			}
			transition, err = gatewayCurrentTransitionFromPending(state)
		}
	default:
		return true, gatewayV2StartupInspectionError(ctx)
	}
	if err != nil || ctx.Err() != nil {
		return true, gatewayV2StartupInspectionError(ctx)
	}
	// Once withdrawal starts, client cancellation must not leave the previous
	// publication live. Failure retains exact evidence and stops only its owner.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	stop := func() (bool, error) {
		stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancelStop()
		if _, err := m.stopGatewayCurrentPhysicalLocked(stopCtx, transition); err != nil {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return true, gatewayV2StartupInspectionError(ctx)
	}
	if state.Pending == nil {
		if err := m.persistGatewayCurrentExactLocked(workCtx, state, transition.Pending); err != nil {
			if confirmErr := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, transition.Pending, claims); confirmErr != nil {
				return stop()
			}
		}
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, transition.Pending, claims); err != nil {
		return stop()
	}
	if transition.Kind == gatewayCurrentPhysicalLANGrant {
		_, err = m.restoreGatewayCurrentPhysicalLocked(workCtx, transition)
	} else {
		_, err = m.applyGatewayCurrentPhysicalLocked(workCtx, transition)
	}
	if err != nil {
		return stop()
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, transition.Pending, claims); err != nil {
		return stop()
	}
	return true, nil
}

func (m *Manager) confirmGatewayCurrentLANStartupStateLocked(ctx context.Context,
	snapshot appaccess.GatewayRebindRecoverySnapshot, state gatewayCurrentRouteState,
	claims gatewayV2LANAccessStartupClaims,
) error {
	selection, current, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled || selection.State == nil || !reflect.DeepEqual(snapshot, current) ||
		!reflect.DeepEqual(state, *selection.State) || ctx.Err() != nil {
		return gatewayV2StartupInspectionError(ctx)
	}
	return m.validateGatewayCurrentLANRetainedHistoryLocked(ctx, claims, snapshot)
}
