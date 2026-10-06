package generatedingress

import (
	"context"
	"reflect"
)

// The caller holds the effects lease and both gateway locks. Retirement is a
// protected-state write only: the whole queue must already be terminal and its
// complete runtime withdrawal must be freshly proved before removing the fence.
func (m *Manager) retireGatewayCurrentLANRecoveryBatchLocked(ctx context.Context,
	claims gatewayV2LANAccessStartupClaims,
) (bool, error) {
	selection, snapshot, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentRouteState(*selection.State)
	if state.LANRecovery == nil || state.LANRecovery.Head != len(state.LANRecovery.Items) ||
		!gatewayCurrentLANRecoveryCensusMatchesHead(state, claims, snapshot.CurrentTransfers) {
		return true, gatewayV2LANDisableError(ctx)
	}
	if err := m.validateGatewayCurrentLANRetainedHistoryLocked(ctx, claims, snapshot); err != nil {
		return true, err
	}
	retired, err := gatewayCurrentLANRecoveryRetiredState(state)
	if err != nil {
		return true, err
	}
	inspection, err := gatewayCurrentLANStartupCensus(retired, claims)
	if err != nil || inspection.Disposition != GatewayV2LANStartupNormal {
		return true, gatewayV2LANDisableError(ctx)
	}

	// A lost proof or write acknowledgement cannot reopen ordinary admission.
	// Reload only the two exact states this retirement may have installed; an
	// unrelated revision must never become new withdrawal authority here.
	fail := func() (bool, error) {
		m.gatewayRebindFailStopLatch().Store(true)
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancel()
		installed, loadErr := selection.Store.load()
		if loadErr != nil || (!reflect.DeepEqual(installed, state) && !reflect.DeepEqual(installed, retired)) {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		target, targetErr := m.gatewayCurrentOwnedStopTargetForStateLocked(installed)
		if targetErr != nil || m.stopGatewayCurrentOwnedTargetLocked(stopCtx, target) != nil {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return true, gatewayV2LANDisableError(ctx)
	}
	proof, err := m.attestGatewayCurrentLANRecoveryBatchLocked(ctx, selection)
	if err != nil || !proof.BatchAbsent {
		return fail()
	}
	// A stopped gateway proves safe quarantine, but does not prove the normal
	// topology to which retirement would return. Keep its durable recovery
	// marker until that topology has actually resumed under the batch guard.
	if proof.Attestation.Outcome != gatewayCurrentPhysicalRecoveryMixed || proof.Attestation.Runtime.ListenerAbsent {
		return true, gatewayV2LANDisableError(ctx)
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(ctx, snapshot, state, claims); err != nil {
		return fail()
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, state, retired); err != nil {
		return fail()
	}
	physical, err := m.attestGatewayCurrentStateLocked(ctx, retired)
	if err != nil || physical.Outcome != gatewayCurrentPhysicalStableServing ||
		!reflect.DeepEqual(physical.State, retired) {
		return fail()
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(ctx, snapshot, retired, claims); err != nil {
		return fail()
	}
	return true, nil
}
