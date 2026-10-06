package generatedingress

import (
	"context"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func gatewayCurrentGrantTransition(before gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest,
) (gatewayCurrentPhysicalTransition, gatewayCurrentRouteState, error) {
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil || before.Pending != nil || before.LANRecovery != nil ||
		before.Profile.RevisionID != raw.ProfileRevisionID ||
		before.Profile.RevisionNumber != raw.ProfileRevisionNumber ||
		before.Profile.SpecDigest != raw.ProfileSpecDigest {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteState{}, &Error{Code: DiagnosticRouteInvalid}
	}
	previous, exists := before.Apps[request.AppID]
	if !exists || previous.LAN != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteState{}, &Error{Code: DiagnosticRouteInvalid}
	}
	proposed := cloneGatewayCurrentOperationApp(previous)
	proposed.LAN = &gatewayCurrentLANBinding{Raw: raw}
	previousRef := cloneGatewayCurrentOperationApp(previous)
	pending, err := gatewayCurrentNextState(before, func(next *gatewayCurrentRouteState) {
		next.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANGrant, AppID: request.AppID,
			Previous: &previousRef, Proposed: cloneGatewayCurrentOperationApp(proposed)}
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteState{}, err
	}
	physicalEffective, err := gatewayCurrentNextState(pending, func(next *gatewayCurrentRouteState) {
		next.Pending = nil
		next.Apps[request.AppID] = cloneGatewayCurrentOperationApp(proposed)
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteState{}, err
	}
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalLANGrant,
		AppID: request.AppID, Before: cloneGatewayCurrentOperationState(before), Pending: pending,
		Effective: physicalEffective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	activationPending, err := gatewayCurrentNextState(pending, func(next *gatewayCurrentRouteState) {
		next.Pending.ActivationUncertain = true
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteState{}, err
	}
	return transition, activationPending, nil
}

func gatewayCurrentGrantActivationTransition(first gatewayCurrentPhysicalTransition,
	activationPending gatewayCurrentRouteState,
) (gatewayCurrentPhysicalTransition, error) {
	if first.Kind != gatewayCurrentPhysicalLANGrant || first.Pending.Pending == nil ||
		activationPending.Pending == nil || !activationPending.Pending.ActivationUncertain ||
		!reflect.DeepEqual(first.Pending.Pending.Previous, activationPending.Pending.Previous) ||
		!reflect.DeepEqual(first.Pending.Pending.Proposed, activationPending.Pending.Proposed) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	effective, err := gatewayCurrentNextState(activationPending, func(next *gatewayCurrentRouteState) {
		next.Pending = nil
		next.Apps[first.AppID] = cloneGatewayCurrentOperationApp(activationPending.Pending.Proposed)
	})
	if err != nil {
		return gatewayCurrentPhysicalTransition{}, err
	}
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalLANGrant, AppID: first.AppID,
		Before: cloneGatewayCurrentOperationState(first.Before), Pending: cloneGatewayCurrentOperationState(activationPending),
		Effective: effective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	return transition, nil
}

func gatewayCurrentDisableTransition(before gatewayCurrentRouteState,
	request GatewayV2LANDisableRequest,
) (gatewayCurrentPhysicalTransition, error) {
	if !validGatewayV2LANDisableRequest(request) || before.Pending != nil || before.LANRecovery != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	previous, exists := before.Apps[request.AppID]
	if !exists || previous.LAN == nil || request.SourceGrant == nil {
		return gatewayCurrentPhysicalTransition{}, &Error{Code: DiagnosticRouteInvalid}
	}
	raw, err := gatewayV2LANBindingForRequest(*request.SourceGrant)
	if err != nil || !reflect.DeepEqual(previous.LAN.Raw, raw) || previous.LAN.Raw.Port != request.Port {
		return gatewayCurrentPhysicalTransition{}, &Error{Code: DiagnosticRouteInvalid}
	}
	proposed := cloneGatewayCurrentOperationApp(previous)
	proposed.LAN = nil
	previousRef := cloneGatewayCurrentOperationApp(previous)
	disable := cloneGatewayCurrentDisableRequest(request)
	pending, err := gatewayCurrentNextState(before, func(next *gatewayCurrentRouteState) {
		next.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANDisable, AppID: request.AppID,
			Previous: &previousRef, Proposed: cloneGatewayCurrentOperationApp(proposed),
			ActivationUncertain: true, Disable: &disable}
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
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalLANDisable,
		AppID: request.AppID, Before: cloneGatewayCurrentOperationState(before), Pending: pending, Effective: effective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	return transition, nil
}

func gatewayCurrentGrantWithdrawalTransition(before gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest,
) (gatewayCurrentPhysicalTransition, error) {
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil || before.Pending != nil || before.LANRecovery != nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	previous, exists := before.Apps[request.AppID]
	if !exists || previous.LAN == nil || !reflect.DeepEqual(previous.LAN.Raw, raw) {
		return gatewayCurrentPhysicalTransition{}, &Error{Code: DiagnosticRouteInvalid}
	}
	proposed := cloneGatewayCurrentOperationApp(previous)
	proposed.LAN = nil
	previousRef := cloneGatewayCurrentOperationApp(previous)
	pending, err := gatewayCurrentNextState(before, func(next *gatewayCurrentRouteState) {
		next.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANWithdrawal,
			AppID: request.AppID, Previous: &previousRef, Proposed: cloneGatewayCurrentOperationApp(proposed),
			ActivationUncertain: true}
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
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalLANWithdrawal,
		AppID: request.AppID, Before: cloneGatewayCurrentOperationState(before), Pending: pending,
		Effective: effective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentRouteOperationError(nil)
	}
	return transition, nil
}

func gatewayCurrentGrantObservation(state gatewayCurrentRouteState, request GatewayV2LANGrantRequest,
	disposition GatewayV2LANGrantDisposition, activationUncertain bool,
) (GatewayV2LANGrantObservation, error) {
	if !validGatewayCurrentRouteState(state) ||
		(disposition != GatewayV2LANGrantCommitted && disposition != GatewayV2LANGrantPendingPublished &&
			disposition != GatewayV2LANGrantPendingReloadOnly &&
			disposition != GatewayV2LANGrantWithdrawnPendingReconciliation) {
		return GatewayV2LANGrantObservation{}, gatewayCurrentRouteOperationError(nil)
	}
	var app gatewayCurrentAppRoute
	var binding *gatewayCurrentLANBinding
	if state.Pending != nil && state.Pending.AppID == request.AppID {
		switch state.Pending.Kind {
		case gatewayV2PendingLANGrant:
			app = cloneGatewayCurrentOperationApp(state.Pending.Proposed)
			binding = app.LAN
		case gatewayV2PendingLANWithdrawal:
			if state.Pending.Previous == nil {
				return GatewayV2LANGrantObservation{}, gatewayCurrentRouteOperationError(nil)
			}
			app = cloneGatewayCurrentOperationApp(*state.Pending.Previous)
			binding = app.LAN
		default:
			return GatewayV2LANGrantObservation{}, gatewayCurrentRouteOperationError(nil)
		}
	} else {
		var exists bool
		app, exists = state.Apps[request.AppID]
		if !exists {
			return GatewayV2LANGrantObservation{}, gatewayCurrentRouteOperationError(nil)
		}
		binding = app.LAN
	}
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil || binding == nil || !reflect.DeepEqual(binding.Raw, raw) {
		return GatewayV2LANGrantObservation{}, gatewayCurrentRouteOperationError(nil)
	}
	proof, err := gatewayCurrentEffectiveProof(state, binding)
	if err != nil {
		return GatewayV2LANGrantObservation{}, gatewayCurrentRouteOperationError(nil)
	}
	return GatewayV2LANGrantObservation{
		Request: request, Disposition: disposition, ActivationUncertain: activationUncertain,
		Slot: app.Route.Slot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...),
		GatewayOperationID: state.Lineage.OperationID, ProtectedStateDigest: state.Digest,
		EffectiveBinding: proof, ObservedAt: time.Now().UTC(),
	}, nil
}

func gatewayCurrentGrantResult(state gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest,
) (GatewayV2LANGrantResult, error) {
	observation, err := gatewayCurrentGrantObservation(state, request, GatewayV2LANGrantCommitted, false)
	if err != nil {
		return GatewayV2LANGrantResult{}, err
	}
	return GatewayV2LANGrantResult{Receipt: GatewayV2LANGrantReceipt{
		Request: observation.Request, GatewayOperationID: observation.GatewayOperationID,
		ProtectedStateDigest: observation.ProtectedStateDigest, ObservedAt: observation.ObservedAt,
	}}, nil
}

func (m *Manager) gatewayCurrentRetainedTransitionLocked(ctx context.Context,
	transitions ...gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalTransition, gatewayCurrentSelection, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled || selection.State == nil {
		return gatewayCurrentPhysicalTransition{}, gatewayCurrentSelection{}, gatewayCurrentRouteOperationError(ctx)
	}
	for _, transition := range transitions {
		if reflect.DeepEqual(*selection.State, transition.Pending) {
			return transition, selection, nil
		}
	}
	return gatewayCurrentPhysicalTransition{}, selection, gatewayCurrentRouteOperationError(ctx)
}

func (m *Manager) restoreGatewayCurrentGrantBeforeLocked(ctx context.Context,
	transitions ...gatewayCurrentPhysicalTransition,
) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	transition, _, err := m.gatewayCurrentRetainedTransitionLocked(recoveryCtx, transitions...)
	if err != nil {
		for _, candidate := range transitions {
			if _, stopErr := m.stopGatewayCurrentPhysicalLocked(recoveryCtx, candidate); stopErr == nil {
				return gatewayCurrentRouteOperationError(ctx)
			}
		}
		return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	if _, err := m.restoreGatewayCurrentPhysicalLocked(recoveryCtx, transition); err != nil {
		if _, stopErr := m.stopGatewayCurrentPhysicalLocked(recoveryCtx, transition); stopErr != nil {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
	}
	return gatewayCurrentRouteOperationError(ctx)
}

func (m *Manager) grantGatewayCurrentLANStateMachineLocked(ctx context.Context,
	request GatewayV2LANGrantRequest, authorize GatewayV2LANGrantAuthorizer,
) (result GatewayV2LANGrantResult, handled bool, resultErr error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANGrantResult{}, handled, err
	}
	before := cloneGatewayCurrentOperationState(*selection.State)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		return GatewayV2LANGrantResult{}, true, &Error{Code: DiagnosticRouteInvalid}
	}
	app, exists := before.Apps[request.AppID]
	if !exists {
		return GatewayV2LANGrantResult{}, true, &Error{Code: DiagnosticRouteInvalid}
	}
	if app.LAN == nil && (before.Profile.RevisionID != raw.ProfileRevisionID ||
		before.Profile.RevisionNumber != raw.ProfileRevisionNumber ||
		before.Profile.SpecDigest != raw.ProfileSpecDigest) {
		return GatewayV2LANGrantResult{}, true, &Error{Code: DiagnosticRouteInvalid}
	}
	if app.LAN != nil && !reflect.DeepEqual(app.LAN.Raw, raw) {
		return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	lease, err := acquireGatewayV2LANGrantAuthorization(ctx, request, authorize)
	if err != nil {
		return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			result = GatewayV2LANGrantResult{}
			handled = true
			resultErr = gatewayCurrentRouteOperationError(ctx)
		}
	}()
	if app.LAN != nil {
		if _, err := m.attestGatewayCurrentStateLocked(ctx, before); err != nil {
			return GatewayV2LANGrantResult{}, true, err
		}
		if err := lease.Activate(ctx, request); err != nil {
			if quarantineErr := m.quarantineGatewayCurrentGrantLocked(ctx, before, request); quarantineErr != nil {
				return GatewayV2LANGrantResult{}, true, quarantineErr
			}
			return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		if _, err := m.attestGatewayCurrentStateLocked(ctx, before); err != nil {
			if quarantineErr := m.quarantineGatewayCurrentGrantLocked(ctx, before, request); quarantineErr != nil {
				return GatewayV2LANGrantResult{}, true, quarantineErr
			}
			return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		result, err = gatewayCurrentGrantResult(before, request)
		return result, true, err
	}

	first, activationPending, err := gatewayCurrentGrantTransition(before, request)
	if err != nil {
		return GatewayV2LANGrantResult{}, true, err
	}
	activation, err := gatewayCurrentGrantActivationTransition(first, activationPending)
	if err != nil {
		return GatewayV2LANGrantResult{}, true, err
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, before, first.Pending); err != nil {
		return GatewayV2LANGrantResult{}, true, err
	}
	if err := lease.Revalidate(ctx, request); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, first)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, first.Pending); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, first)
	}
	if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, first); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, first)
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, first.Pending, activationPending); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, first, activation)
	}
	if err := lease.Activate(ctx, request); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, activation)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, activationPending); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, activation)
	}
	if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, activation); err != nil {
		return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, activation)
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, activationPending, activation.Effective); err != nil {
		if retained, _, retainedErr := m.gatewayCurrentRetainedTransitionLocked(ctx, activation); retainedErr == nil {
			return GatewayV2LANGrantResult{}, true, m.restoreGatewayCurrentGrantBeforeLocked(ctx, retained)
		}
		if current, currentErr := m.confirmGatewayCurrentStateLocked(ctx, activation.Effective); currentErr == nil && current.State != nil {
			if quarantineErr := m.quarantineGatewayCurrentGrantLocked(ctx, activation.Effective, request); quarantineErr != nil {
				return GatewayV2LANGrantResult{}, true, quarantineErr
			}
			return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancel()
		if _, stopErr := m.stopGatewayCurrentPhysicalLocked(stopCtx, activation); stopErr != nil {
			return GatewayV2LANGrantResult{}, true,
				&Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.attestGatewayCurrentStateLocked(ctx, activation.Effective); err != nil {
		if quarantineErr := m.quarantineGatewayCurrentGrantLocked(ctx, activation.Effective, request); quarantineErr != nil {
			return GatewayV2LANGrantResult{}, true, quarantineErr
		}
		return GatewayV2LANGrantResult{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	result, err = gatewayCurrentGrantResult(activation.Effective, request)
	return result, true, err
}

func (m *Manager) quarantineGatewayCurrentGrantLocked(ctx context.Context, before gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest,
) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	transition, err := gatewayCurrentGrantWithdrawalTransition(before, request)
	if err != nil {
		return err
	}
	if err := m.persistGatewayCurrentExactLocked(recoveryCtx, before, transition.Pending); err != nil {
		if _, confirmErr := m.confirmGatewayCurrentStateLocked(recoveryCtx, transition.Pending); confirmErr != nil {
			if _, stopErr := m.stopGatewayCurrentPhysicalLocked(recoveryCtx, transition); stopErr != nil {
				return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
			}
			return gatewayCurrentRouteOperationError(ctx)
		}
	}
	if _, err := m.confirmGatewayCurrentStateLocked(recoveryCtx, transition.Pending); err != nil {
		return err
	}
	if _, err := m.applyGatewayCurrentPhysicalLocked(recoveryCtx, transition); err != nil {
		if _, stopErr := m.stopGatewayCurrentPhysicalLocked(recoveryCtx, transition); stopErr != nil {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayCurrentRouteOperationError(ctx)
	}
	_, err = m.confirmGatewayCurrentStateLocked(recoveryCtx, transition.Pending)
	return err
}

func (m *Manager) observeGatewayCurrentLANLocked(ctx context.Context,
	request GatewayV2LANGrantRequest,
) (GatewayV2LANGrantObservation, bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANGrantObservation{}, handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.LANRecovery != nil {
		return GatewayV2LANGrantObservation{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	attestation, err := m.attestGatewayCurrentStateLocked(ctx, state)
	if err != nil {
		return GatewayV2LANGrantObservation{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	disposition := GatewayV2LANGrantDisposition("")
	activationUncertain := false
	if state.Pending == nil {
		if attestation.Outcome != gatewayCurrentPhysicalStableServing {
			return GatewayV2LANGrantObservation{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		disposition = GatewayV2LANGrantCommitted
	} else {
		if state.Pending.AppID != request.AppID ||
			(state.Pending.Kind != gatewayV2PendingLANGrant && state.Pending.Kind != gatewayV2PendingLANWithdrawal) {
			return GatewayV2LANGrantObservation{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		activationUncertain = state.Pending.ActivationUncertain
		switch attestation.Outcome {
		case gatewayCurrentPhysicalRecoveryMixed:
			disposition = GatewayV2LANGrantPendingReloadOnly
		case gatewayCurrentPhysicalRecoveryStopped:
			disposition = GatewayV2LANGrantWithdrawnPendingReconciliation
		case gatewayCurrentPhysicalRecoveryBefore:
			if state.Pending.Kind == gatewayV2PendingLANGrant {
				disposition = GatewayV2LANGrantWithdrawnPendingReconciliation
			} else {
				disposition = GatewayV2LANGrantPendingPublished
			}
		case gatewayCurrentPhysicalRecoveryEffective:
			if state.Pending.Kind == gatewayV2PendingLANGrant {
				disposition = GatewayV2LANGrantPendingPublished
			} else {
				disposition = GatewayV2LANGrantWithdrawnPendingReconciliation
			}
		default:
			return GatewayV2LANGrantObservation{}, true, gatewayCurrentRouteOperationError(ctx)
		}
	}
	observation, err := gatewayCurrentGrantObservation(state, request, disposition, activationUncertain)
	return observation, true, err
}

func (m *Manager) withGatewayCurrentLANObservationLocked(ctx context.Context,
	request GatewayV2LANGrantRequest, fn func(context.Context, GatewayV2LANGrantObservation) error,
) (bool, error) {
	observation, handled, err := m.observeGatewayCurrentLANLocked(ctx, request)
	if err != nil || !handled {
		return handled, err
	}
	if ctx.Err() != nil {
		return true, &Error{Code: DiagnosticCancelled}
	}
	if err := fn(ctx, observation); err != nil {
		return true, err
	}
	if ctx.Err() != nil {
		return true, &Error{Code: DiagnosticCancelled}
	}
	confirmed, confirmedHandled, err := m.observeGatewayCurrentLANLocked(ctx, request)
	if err != nil || !confirmedHandled || !sameGatewayCurrentGrantObservation(observation, confirmed) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	return true, nil
}

func sameGatewayCurrentGrantObservation(left, right GatewayV2LANGrantObservation) bool {
	left.ObservedAt = time.Time{}
	right.ObservedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func (m *Manager) withGatewayCurrentLANAbsenceResolutionLocked(ctx context.Context,
	request GatewayV2LANGrantRequest, fn func(context.Context, GatewayV2LANGrantObservation) error,
) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil || state.Profile.RevisionID != raw.ProfileRevisionID ||
		state.Profile.RevisionNumber != raw.ProfileRevisionNumber || state.Profile.SpecDigest != raw.ProfileSpecDigest {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	for _, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		binding := app.LAN.Raw
		if binding.GrantAttemptID == request.AttemptID ||
			binding.GrantRequestDigest == request.ClaimRequestDigest ||
			binding.AllocationID == request.AllocationID || binding.AccessRevisionID == request.AccessRevisionID ||
			binding.Port == request.Port {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
	}
	attestation, err := m.attestGatewayCurrentStateLocked(ctx, state)
	if err != nil || attestation.Outcome != gatewayCurrentPhysicalStableServing ||
		!reflect.DeepEqual(attestation.State, state) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	observation := GatewayV2LANGrantObservation{Request: request,
		Disposition:        GatewayV2LANGrantWithdrawnPendingReconciliation,
		GatewayOperationID: state.Lineage.OperationID, ProtectedStateDigest: state.Digest,
		ObservedAt: time.Now().UTC()}
	if err := fn(ctx, observation); err != nil {
		return true, err
	}
	confirmed, err := m.attestGatewayCurrentStateLocked(ctx, state)
	if err != nil || confirmed.Outcome != gatewayCurrentPhysicalStableServing ||
		!reflect.DeepEqual(confirmed.State, state) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	return true, nil
}

func (m *Manager) withGatewayCurrentLANCommitResolutionLocked(ctx context.Context,
	request GatewayV2LANGrantRequest,
	resolve func(context.Context, GatewayV2LANGrantObservation) error,
) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	observation, err := gatewayCurrentGrantObservation(state, request, GatewayV2LANGrantCommitted, false)
	if err != nil {
		return true, err
	}
	attestation, err := m.attestGatewayCurrentStateLocked(ctx, state)
	if err != nil || attestation.Outcome != gatewayCurrentPhysicalStableServing ||
		!reflect.DeepEqual(attestation.State, state) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if err := resolve(ctx, observation); err != nil {
		if quarantineErr := m.quarantineGatewayCurrentGrantLocked(ctx, state, request); quarantineErr != nil {
			return true, quarantineErr
		}
		return true, err
	}
	confirmed, err := m.attestGatewayCurrentStateLocked(ctx, state)
	if err != nil || confirmed.Outcome != gatewayCurrentPhysicalStableServing {
		if quarantineErr := m.quarantineGatewayCurrentGrantLocked(ctx, state, request); quarantineErr != nil {
			return true, quarantineErr
		}
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	return true, nil
}

func (m *Manager) recoverGatewayCurrentLANLocked(ctx context.Context,
	request GatewayV2LANGrantRequest,
) (GatewayV2LANGrantRecovery, bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANGrantRecovery{}, handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.LANRecovery != nil {
		return GatewayV2LANGrantRecovery{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	if state.Pending == nil {
		observation, _, err := m.observeGatewayCurrentLANLocked(ctx, request)
		return GatewayV2LANGrantRecovery{Observation: observation}, true, err
	}
	if state.Pending.AppID != request.AppID ||
		(state.Pending.Kind != gatewayV2PendingLANGrant && state.Pending.Kind != gatewayV2PendingLANWithdrawal) {
		return GatewayV2LANGrantRecovery{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := gatewayCurrentGrantObservation(state, request,
		GatewayV2LANGrantWithdrawnPendingReconciliation, state.Pending.ActivationUncertain); err != nil {
		return GatewayV2LANGrantRecovery{}, true, err
	}
	transition, err := gatewayCurrentTransitionFromPending(state)
	if err != nil {
		return GatewayV2LANGrantRecovery{}, true, err
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
		return GatewayV2LANGrantRecovery{}, true, err
	}
	if state.Pending.Kind == gatewayV2PendingLANGrant {
		if _, err := m.restoreGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
			return GatewayV2LANGrantRecovery{}, true, gatewayCurrentRouteOperationError(ctx)
		}
	} else if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
		return GatewayV2LANGrantRecovery{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
		return GatewayV2LANGrantRecovery{}, true, err
	}
	observation, err := gatewayCurrentGrantObservation(state, request,
		GatewayV2LANGrantWithdrawnPendingReconciliation, state.Pending.ActivationUncertain)
	return GatewayV2LANGrantRecovery{Observation: observation}, true, err
}

func (m *Manager) withGatewayCurrentLANRollbackResolutionLocked(ctx context.Context,
	request GatewayV2LANGrantRequest, fn func(context.Context, GatewayV2LANGrantObservation) error,
) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.Pending == nil || state.LANRecovery != nil || state.Pending.AppID != request.AppID ||
		(state.Pending.Kind != gatewayV2PendingLANGrant && state.Pending.Kind != gatewayV2PendingLANWithdrawal) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := gatewayCurrentGrantObservation(state, request,
		GatewayV2LANGrantWithdrawnPendingReconciliation, state.Pending.ActivationUncertain); err != nil {
		return true, err
	}
	transition, err := gatewayCurrentTransitionFromPending(state)
	if err != nil {
		return true, err
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
		return true, err
	}
	if state.Pending.Kind == gatewayV2PendingLANGrant {
		if _, err := m.restoreGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
	} else if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
		return true, err
	}
	observation, err := gatewayCurrentGrantObservation(state, request,
		GatewayV2LANGrantWithdrawnPendingReconciliation, state.Pending.ActivationUncertain)
	if err != nil {
		return true, err
	}
	if err := fn(ctx, observation); err != nil {
		return true, err
	}
	cleared := transition.Effective
	if state.Pending.Kind == gatewayV2PendingLANGrant {
		cleared, err = gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
			next.Pending = nil
		})
		if err != nil {
			return true, err
		}
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, state, cleared); err != nil {
		return true, err
	}
	if _, err := m.attestGatewayCurrentStateLocked(ctx, cleared); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) withGatewayCurrentLANCommitRecoveryLocked(ctx context.Context,
	request GatewayV2LANGrantRequest,
	validateCommitted func(context.Context, GatewayV2LANGrantObservation) error,
) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.Pending == nil || state.LANRecovery != nil || state.Pending.AppID != request.AppID ||
		!state.Pending.ActivationUncertain ||
		(state.Pending.Kind != gatewayV2PendingLANGrant && state.Pending.Kind != gatewayV2PendingLANWithdrawal) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := gatewayCurrentGrantObservation(state, request,
		GatewayV2LANGrantWithdrawnPendingReconciliation, true); err != nil {
		return true, err
	}
	transition, err := gatewayCurrentTransitionFromPending(state)
	if err != nil {
		return true, err
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
		return true, err
	}
	if state.Pending.Kind == gatewayV2PendingLANGrant {
		if _, err := m.restoreGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
	} else if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	observation, err := gatewayCurrentGrantObservation(state, request,
		GatewayV2LANGrantWithdrawnPendingReconciliation, true)
	if err != nil {
		return true, err
	}
	if err := validateCommitted(ctx, observation); err != nil {
		return true, err
	}
	if _, err := m.confirmGatewayCurrentStateLocked(ctx, state); err != nil {
		return true, err
	}
	var effective gatewayCurrentRouteState
	if state.Pending.Kind == gatewayV2PendingLANGrant {
		if _, err := m.applyGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		effective = transition.Effective
	} else {
		if _, err := m.restoreGatewayCurrentPhysicalLocked(ctx, transition); err != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		effective, err = gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
			next.Pending = nil
			next.Apps[request.AppID] = cloneGatewayCurrentOperationApp(*state.Pending.Previous)
		})
		if err != nil {
			return true, err
		}
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, state, effective); err != nil {
		return true, err
	}
	if _, err := m.attestGatewayCurrentStateLocked(ctx, effective); err != nil {
		return true, err
	}
	return true, nil
}

func gatewayCurrentDisableObservation(state gatewayCurrentRouteState, request GatewayV2LANDisableRequest,
	disposition GatewayV2LANDisableDisposition,
) (GatewayV2LANDisableObservation, error) {
	if !validGatewayCurrentRouteState(state) || !validGatewayV2LANDisableRequest(request) ||
		(disposition != GatewayV2LANDisablePendingPublished &&
			disposition != GatewayV2LANDisableWithdrawnPending && disposition != GatewayV2LANDisableDisabled) {
		return GatewayV2LANDisableObservation{}, gatewayCurrentRouteOperationError(nil)
	}
	return GatewayV2LANDisableObservation{Request: cloneGatewayCurrentDisableRequest(request), Disposition: disposition,
		GatewayOperationID: state.Lineage.OperationID, ProtectedStateDigest: state.Digest,
		ObservedAt: time.Now().UTC()}, nil
}

func gatewayCurrentDisableResult(observation GatewayV2LANDisableObservation) GatewayV2LANDisableResult {
	return GatewayV2LANDisableResult{Receipt: GatewayV2LANDisableReceipt{
		Request: observation.Request, GatewayOperationID: observation.GatewayOperationID,
		ProtectedStateDigest: observation.ProtectedStateDigest, ObservedAt: observation.ObservedAt,
	}}
}

func gatewayCurrentDisableBindingAbsent(state gatewayCurrentRouteState,
	request GatewayV2LANDisableRequest,
) bool {
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		raw := app.LAN.Raw
		if appID == request.AppID || raw.AllocationID == request.AllocationID ||
			raw.AccessRevisionID == request.AccessRevisionID || raw.Port == request.Port {
			return false
		}
	}
	return true
}

func (m *Manager) disableGatewayCurrentLANStateMachineLocked(ctx context.Context,
	request GatewayV2LANDisableRequest, authorize GatewayV2LANDisableAuthorizer,
) (result GatewayV2LANDisableResult, handled bool, resultErr error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANDisableResult{}, handled, err
	}
	before := cloneGatewayCurrentOperationState(*selection.State)
	app, exists := before.Apps[request.AppID]
	lease, err := acquireGatewayV2LANDisableAuthorization(ctx, request, authorize)
	if err != nil {
		return GatewayV2LANDisableResult{}, true, err
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			result = GatewayV2LANDisableResult{}
			handled = true
			resultErr = gatewayCurrentRouteOperationError(ctx)
		}
	}()
	if !exists || app.LAN == nil {
		if !gatewayCurrentDisableBindingAbsent(before, request) {
			return GatewayV2LANDisableResult{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		if err := lease.Withdraw(ctx, request); err != nil {
			return GatewayV2LANDisableResult{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		if _, err := m.attestGatewayCurrentStateLocked(ctx, before); err != nil {
			return GatewayV2LANDisableResult{}, true, err
		}
		observation, err := gatewayCurrentDisableObservation(before, request, GatewayV2LANDisableDisabled)
		return gatewayCurrentDisableResult(observation), true, err
	}
	transition, err := gatewayCurrentDisableTransition(before, request)
	if err != nil {
		return GatewayV2LANDisableResult{}, true, err
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, before, transition.Pending); err != nil {
		return GatewayV2LANDisableResult{}, true, err
	}
	withdrawErr := lease.Withdraw(ctx, request)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if _, err := m.confirmGatewayCurrentStateLocked(stopCtx, transition.Pending); err != nil {
		return GatewayV2LANDisableResult{}, true, err
	}
	if _, err := m.applyGatewayCurrentPhysicalLocked(stopCtx, transition); err != nil {
		if _, stopErr := m.stopGatewayCurrentPhysicalLocked(stopCtx, transition); stopErr != nil {
			return GatewayV2LANDisableResult{}, true,
				&Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return GatewayV2LANDisableResult{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(stopCtx, transition.Pending); err != nil {
		return GatewayV2LANDisableResult{}, true, err
	}
	if withdrawErr != nil {
		return GatewayV2LANDisableResult{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	observation, err := gatewayCurrentDisableObservation(transition.Pending, request,
		GatewayV2LANDisableWithdrawnPending)
	return gatewayCurrentDisableResult(observation), true, err
}

func (m *Manager) observeGatewayCurrentLANDisableLocked(ctx context.Context,
	request GatewayV2LANDisableRequest,
) (GatewayV2LANDisableObservation, bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANDisableObservation{}, handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.LANRecovery != nil {
		return GatewayV2LANDisableObservation{}, true, gatewayCurrentRouteOperationError(ctx)
	}
	attestation, err := m.attestGatewayCurrentStateLocked(ctx, state)
	if err != nil {
		return GatewayV2LANDisableObservation{}, true, err
	}
	disposition := GatewayV2LANDisableDisposition("")
	if state.Pending == nil {
		app, exists := state.Apps[request.AppID]
		if exists && app.LAN != nil {
			if request.SourceGrant == nil {
				return GatewayV2LANDisableObservation{}, true, gatewayCurrentRouteOperationError(ctx)
			}
			raw, rawErr := gatewayV2LANBindingForRequest(*request.SourceGrant)
			if rawErr != nil || !reflect.DeepEqual(raw, app.LAN.Raw) {
				return GatewayV2LANDisableObservation{}, true, gatewayCurrentRouteOperationError(ctx)
			}
			disposition = GatewayV2LANDisablePendingPublished
		} else {
			if !gatewayCurrentDisableBindingAbsent(state, request) {
				return GatewayV2LANDisableObservation{}, true, gatewayCurrentRouteOperationError(ctx)
			}
			disposition = GatewayV2LANDisableDisabled
		}
	} else {
		if state.Pending.Kind != gatewayV2PendingLANDisable || state.Pending.Disable == nil ||
			!reflect.DeepEqual(*state.Pending.Disable, request) {
			return GatewayV2LANDisableObservation{}, true, gatewayCurrentRouteOperationError(ctx)
		}
		switch attestation.Outcome {
		case gatewayCurrentPhysicalRecoveryBefore:
			disposition = GatewayV2LANDisablePendingPublished
		case gatewayCurrentPhysicalRecoveryEffective, gatewayCurrentPhysicalRecoveryStopped:
			disposition = GatewayV2LANDisableWithdrawnPending
		default:
			return GatewayV2LANDisableObservation{}, true, gatewayCurrentRouteOperationError(ctx)
		}
	}
	observation, err := gatewayCurrentDisableObservation(state, request, disposition)
	return observation, true, err
}

func (m *Manager) withGatewayCurrentLANDisableResolutionLocked(ctx context.Context,
	request GatewayV2LANDisableRequest, authorize GatewayV2LANDisableResolutionAuthorizer,
	resolve func(context.Context, GatewayV2LANDisableObservation) error,
	acknowledge func(context.Context, GatewayV2LANDisableObservation) error,
) (bool, error) {
	selection, _, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	if state.LANRecovery != nil {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if err := authorize(ctx, request); err != nil {
		return true, err
	}
	if state.Pending == nil {
		app, exists := state.Apps[request.AppID]
		if exists && app.LAN != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		if !gatewayCurrentDisableBindingAbsent(state, request) {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		attestation, err := m.attestGatewayCurrentStateLocked(ctx, state)
		if err != nil || attestation.Outcome != gatewayCurrentPhysicalStableServing ||
			!reflect.DeepEqual(attestation.State, state) {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		observation, err := gatewayCurrentDisableObservation(state, request, GatewayV2LANDisableDisabled)
		if err != nil {
			return true, err
		}
		if err := resolve(ctx, observation); err != nil {
			return true, err
		}
		if _, err := m.attestGatewayCurrentStateLocked(ctx, state); err != nil {
			return true, err
		}
		if acknowledge != nil {
			if err := acknowledge(ctx, observation); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if state.Pending.Kind != gatewayV2PendingLANDisable || state.Pending.Disable == nil ||
		!reflect.DeepEqual(*state.Pending.Disable, request) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	transition, err := gatewayCurrentTransitionFromPending(state)
	if err != nil {
		return true, err
	}
	physicalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if _, err := m.confirmGatewayCurrentStateLocked(physicalCtx, state); err != nil {
		return true, err
	}
	if _, err := m.applyGatewayCurrentPhysicalLocked(physicalCtx, transition); err != nil {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.confirmGatewayCurrentStateLocked(physicalCtx, state); err != nil {
		return true, err
	}
	observation, err := gatewayCurrentDisableObservation(state, request, GatewayV2LANDisableWithdrawnPending)
	if err != nil {
		return true, err
	}
	if err := resolve(ctx, observation); err != nil {
		return true, err
	}
	if err := m.persistGatewayCurrentExactLocked(ctx, state, transition.Effective); err != nil {
		return true, err
	}
	if _, err := m.attestGatewayCurrentStateLocked(ctx, transition.Effective); err != nil {
		return true, err
	}
	if acknowledge != nil {
		cleared, err := gatewayCurrentDisableObservation(transition.Effective, request,
			GatewayV2LANDisableDisabled)
		if err != nil {
			return true, err
		}
		if err := acknowledge(ctx, cleared); err != nil {
			return true, err
		}
	}
	return true, nil
}
