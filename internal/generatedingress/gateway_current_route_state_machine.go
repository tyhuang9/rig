package generatedingress

import (
	"context"
	"math"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

// gatewayCurrentSelectedStateLocked returns the current rebind generation
// only when SQL selection is stable and no newer rebind claim is active. The
// caller holds the Manager and gateway OS locks.
func (m *Manager) gatewayCurrentSelectedStateLocked(ctx context.Context) (
	gatewayCurrentSelection, appaccess.GatewayRebindRecoverySnapshot, bool, error,
) {
	selection, snapshot, present, err := m.readOptionalGatewayCurrentSelectionLocked(ctx)
	if err != nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false,
			gatewayCurrentRouteOperationError(ctx)
	}
	if !present {
		return selection, snapshot, false, nil
	}
	if !gatewayCurrentRouteMutationSnapshotReady(snapshot) {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, true,
			gatewayCurrentRouteOperationError(ctx)
	}
	if selection.Kind == gatewayCurrentSelectionUpgrade {
		return selection, snapshot, false, nil
	}
	if selection.Kind != gatewayCurrentSelectionRebind ||
		selection.Store == nil || selection.State == nil || selection.State.Pending != nil ||
		selection.State.LANRecovery != nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, true,
			gatewayCurrentRouteOperationError(ctx)
	}
	return selection, snapshot, true, nil
}

// gatewayCurrentSelectedStateForRecoveryLocked admits a retained operation
// marker while keeping the same stable SQL selection and active-rebind fence.
func (m *Manager) gatewayCurrentSelectedStateForRecoveryLocked(ctx context.Context) (
	gatewayCurrentSelection, appaccess.GatewayRebindRecoverySnapshot, bool, error,
) {
	selection, snapshot, present, err := m.readOptionalGatewayCurrentSelectionLocked(ctx)
	if err != nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false,
			gatewayCurrentRouteOperationError(ctx)
	}
	if !present {
		return selection, snapshot, false, nil
	}
	if !gatewayCurrentRouteMutationSnapshotReady(snapshot) {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, true,
			gatewayCurrentRouteOperationError(ctx)
	}
	if selection.Kind == gatewayCurrentSelectionUpgrade {
		return selection, snapshot, false, nil
	}
	if selection.Kind != gatewayCurrentSelectionRebind ||
		selection.Store == nil || selection.State == nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, true,
			gatewayCurrentRouteOperationError(ctx)
	}
	return selection, snapshot, true, nil
}

func gatewayCurrentNextState(previous gatewayCurrentRouteState,
	mutate func(*gatewayCurrentRouteState),
) (gatewayCurrentRouteState, error) {
	if !validGatewayCurrentRouteState(previous) || previous.Revision == math.MaxUint64 || mutate == nil {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	next := cloneGatewayCurrentOperationState(previous)
	mutate(&next)
	next.Revision = previous.Revision + 1
	next.Digest = ""
	digest, err := gatewayCurrentRouteStateDigest(next)
	if err != nil {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	next.Digest = digest
	if !validGatewayCurrentRouteState(next) || !sameGatewayCurrentRouteOrigin(previous, next) {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	return next, nil
}

func gatewayCurrentSwitchTransition(before gatewayCurrentRouteState,
	request generatedruntime.RouteSwitchRequest,
) (gatewayCurrentPhysicalTransition, error) {
	if !validSwitchRequest(request) || before.Pending != nil || before.LANRecovery != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	previous, exists := before.Apps[request.AppID]
	proposed := gatewayCurrentAppRoute{Route: routeRecord{Slot: request.ToSlot,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...)}}
	if exists {
		proposed.LAN = previous.LAN
	}
	if request.FromSlot == "" {
		if exists {
			return gatewayCurrentPhysicalTransition{}, &Error{Code: DiagnosticRouteInvalid}
		}
	} else if !exists || previous.Route.Slot != request.FromSlot {
		return gatewayCurrentPhysicalTransition{}, &Error{Code: DiagnosticRouteInvalid}
	}
	var previousRef *gatewayCurrentAppRoute
	if exists {
		copy := cloneGatewayCurrentOperationApp(previous)
		previousRef = &copy
	}
	pending, err := gatewayCurrentNextState(before, func(next *gatewayCurrentRouteState) {
		next.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingRouteSwitch, AppID: request.AppID,
			Previous: previousRef, Proposed: cloneGatewayCurrentOperationApp(proposed)}
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, err
	}
	effective, err := gatewayCurrentNextState(pending, func(next *gatewayCurrentRouteState) {
		next.Pending = nil
		next.Apps[request.AppID] = cloneGatewayCurrentOperationApp(proposed)
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, err
	}
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalRouteSwitch,
		AppID: request.AppID, Before: cloneGatewayCurrentOperationState(before), Pending: pending, Effective: effective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	return transition, nil
}

func gatewayCurrentTransitionFromPending(state gatewayCurrentRouteState) (gatewayCurrentPhysicalTransition, error) {
	if !validGatewayCurrentRouteState(state) || state.Pending == nil || state.LANRecovery != nil ||
		state.Revision < 2 {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	revisionGap := uint64(1)
	if state.Pending.Kind == gatewayV2PendingLANGrant && state.Pending.ActivationUncertain {
		revisionGap = 2
	}
	if state.Revision <= revisionGap {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	before := cloneGatewayCurrentOperationState(state)
	before.Revision = state.Revision - revisionGap
	before.Pending = nil
	if state.Pending.Previous == nil {
		delete(before.Apps, state.Pending.AppID)
	} else {
		before.Apps[state.Pending.AppID] = cloneGatewayCurrentOperationApp(*state.Pending.Previous)
	}
	before.Digest = ""
	var err error
	before.Digest, err = gatewayCurrentRouteStateDigest(before)
	if err != nil || !validGatewayCurrentRouteState(before) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	effective, err := gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
		next.Pending = nil
		next.Apps[state.Pending.AppID] = cloneGatewayCurrentOperationApp(state.Pending.Proposed)
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, err
	}
	kind := gatewayCurrentPhysicalTransitionKind("")
	switch state.Pending.Kind {
	case "", gatewayV2PendingRouteSwitch:
		kind = gatewayCurrentPhysicalRouteSwitch
	case gatewayV2PendingLANGrant:
		kind = gatewayCurrentPhysicalLANGrant
	case gatewayV2PendingLANDisable:
		kind = gatewayCurrentPhysicalLANDisable
	case gatewayV2PendingLANWithdrawal:
		kind = gatewayCurrentPhysicalLANWithdrawal
	default:
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	transition := gatewayCurrentPhysicalTransition{Kind: kind, AppID: state.Pending.AppID,
		Before: before, Pending: cloneGatewayCurrentOperationState(state), Effective: effective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	return transition, nil
}

// persistGatewayCurrentExactLocked writes one precomputed next revision
// through the SQL-rechecked current-state mutation boundary.
func (m *Manager) persistGatewayCurrentExactLocked(ctx context.Context,
	previous, next gatewayCurrentRouteState,
) error {
	if next.Revision != previous.Revision+1 || !sameGatewayCurrentRouteOrigin(previous, next) ||
		!validGatewayCurrentRouteState(previous) || !validGatewayCurrentRouteState(next) {
		return gatewayCurrentRouteOperationError(ctx)
	}
	handled, err := m.mutateGatewayCurrentLocked(ctx, func(installed gatewayCurrentRouteState) (
		gatewayCurrentRouteState, error,
	) {
		if !reflect.DeepEqual(installed, previous) {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		candidate := cloneGatewayCurrentOperationState(installed)
		candidate.Apps = cloneGatewayCurrentOperationState(next).Apps
		candidate.Pending = cloneGatewayCurrentPendingRoute(next.Pending)
		candidate.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(next.LANRecovery)
		proof := cloneGatewayCurrentOperationState(candidate)
		proof.Revision = next.Revision
		proof.Digest = ""
		proof.Digest, _ = gatewayCurrentRouteStateDigest(proof)
		if !reflect.DeepEqual(proof, next) {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		return candidate, nil
	})
	if err != nil || !handled {
		return gatewayCurrentRouteOperationError(ctx)
	}
	return nil
}

func (m *Manager) confirmGatewayCurrentStateLocked(ctx context.Context,
	want gatewayCurrentRouteState,
) (gatewayCurrentSelection, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled || selection.State == nil || !reflect.DeepEqual(*selection.State, want) {
		return gatewayCurrentSelection{}, gatewayCurrentRouteOperationError(ctx)
	}
	return selection, nil
}

func (m *Manager) attestGatewayCurrentStateLocked(ctx context.Context,
	want gatewayCurrentRouteState,
) (gatewayCurrentPhysicalAttestation, error) {
	selection, err := m.confirmGatewayCurrentStateLocked(ctx, want)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	attestation, err := m.attestGatewayCurrentPhysicalLocked(ctx, selection)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, want); err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	return attestation, nil
}

// switchGatewayCurrentStateMachineLocked advances a selected rebind route
// through durable Pending, exact physical publication, and durable Effective.
func (m *Manager) switchGatewayCurrentStateMachineLocked(ctx context.Context,
	request generatedruntime.RouteSwitchRequest,
) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateLocked(ctx)
	if err != nil {
		return handled, markCandidateMayBeLive(err)
	}
	if !handled {
		return false, nil
	}
	before := cloneGatewayCurrentOperationState(*selection.State)
	if current, exists := before.Apps[request.AppID]; exists {
		proposed := gatewayCurrentAppRoute{Route: routeRecord{Slot: request.ToSlot,
			Endpoints: append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...)}, LAN: current.LAN}
		if reflect.DeepEqual(current, proposed) {
			_, err := m.attestGatewayCurrentStateLocked(ctx, before)
			if err != nil {
				return true, markCandidateMayBeLive(err)
			}
			return true, nil
		}
	}
	transition, err := gatewayCurrentSwitchTransition(before, request)
	if err != nil {
		return true, err
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, before, transition.Pending); err != nil {
		return true, err
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, transition.Pending); err != nil {
		return true, err
	}
	if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
		return true, markCandidateMayBeLive(gatewayCurrentRouteOperationError(ctx))
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, transition.Pending, transition.Effective); err != nil {
		return true, markCandidateMayBeLive(err)
	}
	attestation, err := m.attestGatewayCurrentStateLocked(ctx, transition.Effective)
	if err != nil || attestation.Outcome != gatewayCurrentPhysicalStableServing ||
		!reflect.DeepEqual(attestation.State, transition.Effective) {
		return true, markCandidateMayBeLive(gatewayCurrentRouteOperationError(ctx))
	}
	return true, nil
}

// recoverGatewayCurrentStateMachineLocked performs only recovery decisions
// that do not need a database grant/disable resolution. Request-bound LAN
// uncertainty remains durable for its dedicated controller recovery path.
func (m *Manager) recoverGatewayCurrentStateMachineLocked(ctx context.Context) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.LANRecovery != nil {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if state.Pending == nil {
		attestation, err := m.attestGatewayCurrentStateLocked(ctx, state)
		if err != nil || attestation.Outcome != gatewayCurrentPhysicalStableServing ||
			!reflect.DeepEqual(attestation.State, state) {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		return true, nil
	}
	transition, err := gatewayCurrentTransitionFromPending(state)
	if err != nil {
		return true, err
	}
	switch state.Pending.Kind {
	case "", gatewayV2PendingRouteSwitch:
		if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		if _, err := m.restoreGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		cleared, clearErr := gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
			next.Pending = nil
		})
		if clearErr != nil || !reflect.DeepEqual(cleared.Apps, transition.Before.Apps) {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		if err := m.persistGatewayCurrentExactLocked(ctx, state, cleared); err != nil {
			return true, err
		}
		_, err = m.attestGatewayCurrentStateLocked(ctx, cleared)
		return true, err
	case gatewayV2PendingLANGrant:
		if state.Pending.ActivationUncertain {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		projection, projectionErr := gatewayCurrentPhysicalOutcomeProjectionForSelection(state)
		attestation, attestErr := m.attestGatewayCurrentPhysicalLocked(ctx, selection)
		if projectionErr != nil || attestErr != nil ||
			attestation.Outcome != gatewayCurrentPhysicalRecoveryBefore ||
			!reflect.DeepEqual(attestation.State, projection.Before) {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		cleared, clearErr := gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
			next.Pending = nil
		})
		if clearErr != nil || !reflect.DeepEqual(cleared.Apps, transition.Before.Apps) ||
			m.persistGatewayCurrentExactLocked(ctx, state, cleared) != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		_, err = m.attestGatewayCurrentStateLocked(ctx, cleared)
		return true, err
	default:
		return true, gatewayCurrentRouteOperationError(ctx)
	}
}
