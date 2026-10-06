package generatedingress

import (
	"context"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func (m *Manager) withGatewayCurrentLANRecoveryGrantFinalizationLocked(ctx context.Context,
	request GatewayV2LANGrantRequest, resolve func(context.Context, GatewayV2LANGrantObservation) error,
) (bool, error) {
	return m.withGatewayCurrentLANRecoveryHeadLocked(ctx,
		func(item gatewayCurrentLANRecoveryItem) bool {
			return item.Kind == gatewayV2PendingLANGrant && item.Grant != nil &&
				gatewayV2LANGrantRequestForBinding(item.AppID, item.Grant.Raw) == request
		}, func(workCtx context.Context, state gatewayCurrentRouteState, item gatewayCurrentLANRecoveryItem) error {
			// The immutable head remains the raw rollback identity after a clear
			// or a prepared grant that never published. Report the actual state
			// digest; do not synthesize a live LAN binding to build this receipt.
			proof, err := gatewayCurrentEffectiveProof(state, item.Grant)
			if err != nil {
				return err
			}
			app := state.Apps[item.AppID]
			return resolve(workCtx, GatewayV2LANGrantObservation{Request: request,
				Disposition: GatewayV2LANGrantWithdrawnPendingReconciliation, ActivationUncertain: true,
				Slot: app.Route.Slot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...),
				GatewayOperationID: state.Lineage.OperationID, ProtectedStateDigest: state.Digest,
				EffectiveBinding: proof, ObservedAt: time.Now().UTC()})
		}, nil)
}

func (m *Manager) withGatewayCurrentLANRecoveryDisableFinalizationLocked(ctx context.Context,
	request GatewayV2LANDisableRequest,
	resolve, acknowledge func(context.Context, GatewayV2LANDisableObservation) error,
) (bool, error) {
	return m.withGatewayCurrentLANRecoveryHeadLocked(ctx,
		func(item gatewayCurrentLANRecoveryItem) bool {
			return item.Kind == gatewayV2PendingLANDisable && item.Disable != nil && reflect.DeepEqual(*item.Disable, request)
		}, func(workCtx context.Context, state gatewayCurrentRouteState, _ gatewayCurrentLANRecoveryItem) error {
			observation, err := gatewayCurrentDisableObservation(state, request, GatewayV2LANDisableWithdrawnPending)
			if err != nil {
				return err
			}
			return resolve(workCtx, observation)
		}, func(workCtx context.Context, state gatewayCurrentRouteState, _ gatewayCurrentLANRecoveryItem) error {
			observation, err := gatewayCurrentDisableObservation(state, request, GatewayV2LANDisableDisabled)
			if err != nil {
				return err
			}
			return acknowledge(workCtx, observation)
		})
}

// The caller owns the effects lease and both gateway locks. Startup dispatch
// supplies the complete census and pins the exact operation. These callbacks
// must authorize and read back its SQL terminal state/clear acknowledgment.
// Only the protected head can progress, and every boundary reproves the whole
// withdrawn queue and unchanged SQL-selected gateway and retained history.
func (m *Manager) withGatewayCurrentLANRecoveryHeadLocked(ctx context.Context,
	matches func(gatewayCurrentLANRecoveryItem) bool,
	resolve, afterClear func(context.Context, gatewayCurrentRouteState, gatewayCurrentLANRecoveryItem) error,
) (bool, error) {
	selection, snapshot, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentRouteState(*selection.State)
	if state.LANRecovery == nil || state.LANRecovery.Head == len(state.LANRecovery.Items) {
		return true, gatewayV2LANDisableError(ctx)
	}
	item := state.LANRecovery.Items[state.LANRecovery.Head]
	if !matches(item) {
		return true, gatewayV2LANDisableError(ctx)
	}
	cleared, err := gatewayCurrentLANRecoveryClearedHeadState(state)
	if err != nil {
		return true, err
	}
	advanced, err := gatewayCurrentLANRecoveryAdvanceHeadState(cleared)
	if err != nil {
		return true, err
	}
	fail := func() (bool, error) {
		m.gatewayRebindFailStopLatch().Store(true)
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancel()
		installed, err := selection.Store.load()
		if err != nil || (!reflect.DeepEqual(installed, state) && !reflect.DeepEqual(installed, cleared) &&
			!reflect.DeepEqual(installed, advanced)) {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		target, err := m.gatewayCurrentOwnedStopTargetForStateLocked(installed)
		if err != nil || m.stopGatewayCurrentOwnedTargetLocked(stopCtx, target) != nil {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return true, gatewayV2LANDisableError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return fail()
	}
	confirm := func(want gatewayCurrentRouteState) bool {
		selected, current, ok, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
		if err != nil || !ok || selected.State == nil || !reflect.DeepEqual(*selected.State, want) ||
			!reflect.DeepEqual(snapshot, current) || ctx.Err() != nil {
			return false
		}
		after, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		return err == nil && sameGatewayRebindCurrentHistory(history, after) && ctx.Err() == nil
	}
	prove := func(want gatewayCurrentRouteState) bool {
		if !confirm(want) {
			return false
		}
		selected := selection
		selected.State = &want
		proof, err := m.attestGatewayCurrentLANRecoveryBatchLocked(ctx, selected)
		return err == nil && proof.BatchAbsent && confirm(want)
	}
	if !prove(state) {
		return fail()
	}
	resolveErr := resolve(ctx, state, item)
	if !prove(state) {
		return fail()
	}
	if resolveErr != nil {
		return true, resolveErr
	}
	// Cleared replay is byte-identical, not a revision-only write. It still
	// needs a fresh absence proof before the SQL acknowledgment callback.
	if !reflect.DeepEqual(state, cleared) {
		if err := m.persistGatewayCurrentExactLocked(ctx, state, cleared); err != nil {
			return fail()
		}
	}
	if !prove(cleared) {
		return fail()
	}
	if afterClear != nil {
		ackErr := afterClear(ctx, cleared, item)
		if !prove(cleared) {
			return fail()
		}
		if ackErr != nil {
			return true, ackErr
		}
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, cleared, advanced); err != nil || !prove(advanced) {
		return fail()
	}
	return true, nil
}
