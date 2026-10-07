package generatedingress

import (
	"context"
	"reflect"
)

// The caller holds the effects lease and both gateway locks. All unsafe
// bindings are withdrawn together; logical head clearance and SQL completion
// belong to separate, ordered finalization calls.
func (m *Manager) quarantineGatewayCurrentLANRecoveryBatchLocked(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) (bool, error) {
	selection, snapshot, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentRouteState(*selection.State)
	batch := state
	fail := func() (bool, error) {
		m.gatewayRebindFailStopLatch().Store(true)
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancel()
		installed, loadErr := selection.Store.load()
		if loadErr != nil || (!reflect.DeepEqual(installed, state) && !reflect.DeepEqual(installed, batch)) {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		target, targetErr := m.gatewayCurrentOwnedStopTargetForStateLocked(installed)
		if targetErr != nil || m.stopGatewayCurrentOwnedTargetLocked(stopCtx, target) != nil {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return true, gatewayV2StartupInspectionError(ctx)
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		return fail()
	}
	if _, ok := m.currentPhysicalDriver().(gatewayCurrentLANRecoveryPhysicalDriver); !ok {
		return fail()
	}
	if err := m.validateGatewayCurrentLANRetainedHistoryLocked(ctx, claims, snapshot); err != nil {
		return fail()
	}
	if state.LANRecovery == nil {
		batch, err = gatewayCurrentLANRecoveryInstallState(state, claims)
		if err != nil || !gatewayCurrentLANRecoveryCensusMatchesHead(batch, claims, snapshot.CurrentTransfers) {
			// In particular, a committed stale grant cannot be converted into a
			// rollback or an invented disable. Stop its exact owner and retain SQL.
			return fail()
		}
		if _, err := m.attestGatewayCurrentStateLocked(ctx, state); err != nil {
			return fail()
		}
	} else if !gatewayCurrentLANRecoveryCensusMatchesHead(state, claims, snapshot.CurrentTransfers) {
		return fail()
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(ctx, snapshot, state, claims); err != nil || ctx.Err() != nil {
		return fail()
	}
	// Once the durable quarantine starts, finish withdrawal or compensation
	// with an independent bounded context even if the requesting client leaves.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if state.LANRecovery == nil {
		if err := m.persistGatewayCurrentExactLocked(workCtx, state, batch); err != nil {
			return fail()
		}
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, batch, claims); err != nil {
		return fail()
	}
	selected, err := m.confirmGatewayCurrentStateLocked(workCtx, batch)
	if err != nil {
		return fail()
	}
	proof, err := m.attestGatewayCurrentLANRecoveryBatchLocked(workCtx, selected)
	if err != nil {
		return fail()
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, batch, claims); err != nil {
		return fail()
	}
	if !proof.BatchAbsent {
		if batch.LANRecovery.Head == len(batch.LANRecovery.Items) {
			return fail()
		}
		if _, err := m.withdrawGatewayCurrentLANRecoveryBatchLocked(workCtx, selected); err != nil {
			return fail()
		}
		if err := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, batch, claims); err != nil {
			return fail()
		}
		proof, err = m.attestGatewayCurrentLANRecoveryBatchLocked(workCtx, selected)
		if err != nil || !proof.BatchAbsent {
			return fail()
		}
	}
	if err := m.confirmGatewayCurrentLANStartupStateLocked(workCtx, snapshot, batch, claims); err != nil {
		return fail()
	}
	return true, nil
}
