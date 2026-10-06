package generatedingress

import (
	"context"
	"math"
	"reflect"

	"github.com/hostd/hostd/internal/generatedruntime"
)

type gatewayCurrentRouteMutation func(gatewayCurrentRouteState) (gatewayCurrentRouteState, error)

// mutateGatewayCurrentLocked updates only the SQL-selected rebind current
// generation. The caller holds the Manager and gateway OS locks. This helper
// performs no SQL write, Docker effect, or lock acquisition.
func (m *Manager) mutateGatewayCurrentLocked(ctx context.Context,
	mutate gatewayCurrentRouteMutation,
) (bool, error) {
	if m == nil || ctx == nil || mutate == nil {
		return false, &Error{Code: DiagnosticValidationFailed}
	}
	if ctx.Err() != nil {
		return false, &Error{Code: DiagnosticCancelled}
	}
	if m.options.RebindCurrentStateRepository == nil {
		return false, gatewayCurrentRouteOperationError(ctx)
	}

	selection, beforeSnapshot, err := m.readGatewayCurrentSelectionLocked(ctx)
	if err != nil {
		return false, gatewayCurrentRouteOperationError(ctx)
	}
	if selection.Kind == gatewayCurrentSelectionUpgrade {
		return false, nil
	}
	if selection.Kind != gatewayCurrentSelectionRebind || selection.Store == nil || selection.State == nil {
		return false, gatewayCurrentRouteOperationError(ctx)
	}

	previous := cloneGatewayCurrentOperationState(*selection.State)
	next, err := mutate(cloneGatewayCurrentOperationState(previous))
	if err != nil {
		return true, err
	}
	if ctx.Err() != nil {
		return true, &Error{Code: DiagnosticCancelled}
	}
	// The callback owns only the mutable projection. Revision and digest remain
	// the helper's responsibility so callbacks cannot skip revisions or bless
	// their own bytes.
	if next.Revision != previous.Revision || next.Digest != previous.Digest ||
		!sameGatewayCurrentRouteOrigin(previous, next) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	changed := !reflect.DeepEqual(previous.Apps, next.Apps) ||
		!reflect.DeepEqual(previous.Pending, next.Pending) ||
		!reflect.DeepEqual(previous.LANRecovery, next.LANRecovery)
	if changed {
		if previous.Revision == math.MaxUint64 {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		// Reconfirm SQL authority and the exact selected protected revision as
		// close as possible to the durable write. The outer effects lease is
		// still responsible for excluding a SQL current-head change during the
		// remaining file write window.
		writeSelection, writeSnapshot, selectionErr := m.readGatewayCurrentSelectionLocked(ctx)
		if selectionErr != nil || !reflect.DeepEqual(beforeSnapshot, writeSnapshot) ||
			!gatewayCurrentMutationSelectionMatches(selection, writeSelection, previous) {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
		if ctx.Err() != nil {
			return true, &Error{Code: DiagnosticCancelled}
		}
		next.Revision = previous.Revision + 1
		next.Digest = ""
		next.Digest, err = gatewayCurrentRouteStateDigest(next)
		if err != nil || !validGatewayCurrentRouteState(next) ||
			writeSelection.Store.saveNext(previous, next) != nil {
			return true, gatewayCurrentRouteOperationError(ctx)
		}
	} else {
		next = previous
	}

	// If this post-write read fails, the new protected revision remains
	// installed and the caller receives an unresolved result. These helpers do
	// not claim a Docker effect or silently roll back a possibly selected route.
	confirmed, afterSnapshot, err := m.readGatewayCurrentSelectionLocked(ctx)
	if err != nil || !reflect.DeepEqual(beforeSnapshot, afterSnapshot) ||
		!gatewayCurrentMutationSelectionMatches(selection, confirmed, next) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	return true, nil
}

// switchGatewayCurrentRouteLocked persists a route-head advance for the
// selected rebind current. Docker publication remains the caller's effect.
func (m *Manager) switchGatewayCurrentRouteLocked(ctx context.Context,
	request generatedruntime.RouteSwitchRequest,
) (bool, error) {
	if !validSwitchRequest(request) {
		return false, &Error{Code: DiagnosticValidationFailed}
	}
	return m.mutateGatewayCurrentLocked(ctx, func(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
		if state.Pending != nil || state.LANRecovery != nil {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		previous, exists := state.Apps[request.AppID]
		proposed := gatewayCurrentAppRoute{Route: routeRecord{
			Slot: request.ToSlot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...),
		}}
		if exists {
			proposed.LAN = previous.LAN
		}
		if exists && reflect.DeepEqual(previous, proposed) {
			return state, nil
		}
		if request.FromSlot == "" {
			if exists {
				return gatewayCurrentRouteState{}, &Error{Code: DiagnosticRouteInvalid}
			}
		} else if !exists || previous.Route.Slot != request.FromSlot {
			return gatewayCurrentRouteState{}, &Error{Code: DiagnosticRouteInvalid}
		}
		state.Apps[request.AppID] = proposed
		return state, nil
	})
}

// grantGatewayCurrentLANLocked adds only a native binding whose raw profile
// is the selected current profile. Transferred bindings are created solely by
// the rebind commit program.
func (m *Manager) grantGatewayCurrentLANLocked(ctx context.Context,
	request GatewayV2LANGrantRequest,
) (bool, error) {
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		return false, &Error{Code: DiagnosticValidationFailed}
	}
	return m.mutateGatewayCurrentLocked(ctx, func(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
		if state.Pending != nil || state.LANRecovery != nil {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		if state.Profile.RevisionID != raw.ProfileRevisionID ||
			state.Profile.RevisionNumber != raw.ProfileRevisionNumber ||
			state.Profile.SpecDigest != raw.ProfileSpecDigest {
			return gatewayCurrentRouteState{}, &Error{Code: DiagnosticRouteInvalid}
		}
		app, exists := state.Apps[request.AppID]
		if !exists {
			return gatewayCurrentRouteState{}, &Error{Code: DiagnosticRouteInvalid}
		}
		binding := gatewayCurrentLANBinding{Raw: raw}
		if app.LAN != nil {
			if reflect.DeepEqual(*app.LAN, binding) {
				return state, nil
			}
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		app.LAN = &binding
		state.Apps[request.AppID] = app
		return state, nil
	})
}

// disableGatewayCurrentLANLocked removes the exact active native or
// transferred binding. The immutable transfer manifest stays in the origin.
func (m *Manager) disableGatewayCurrentLANLocked(ctx context.Context,
	request GatewayV2LANDisableRequest,
) (bool, error) {
	if !validGatewayV2LANDisableRequest(request) {
		return false, &Error{Code: DiagnosticValidationFailed}
	}
	return m.mutateGatewayCurrentLocked(ctx, func(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
		if state.Pending != nil || state.LANRecovery != nil {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		app, exists := state.Apps[request.AppID]
		if !exists || app.LAN == nil {
			return state, nil
		}
		if request.SourceGrant == nil {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		raw, bindingErr := gatewayV2LANBindingForRequest(*request.SourceGrant)
		if bindingErr != nil || !reflect.DeepEqual(app.LAN.Raw, raw) {
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(ctx)
		}
		app.LAN = nil
		state.Apps[request.AppID] = app
		return state, nil
	})
}

func cloneGatewayCurrentOperationState(value gatewayCurrentRouteState) gatewayCurrentRouteState {
	result := cloneGatewayCurrentRouteState(value)
	if value.Pending != nil {
		pending := *value.Pending
		if value.Pending.Previous != nil {
			previous := cloneGatewayCurrentOperationApp(*value.Pending.Previous)
			pending.Previous = &previous
		}
		pending.Proposed = cloneGatewayCurrentOperationApp(value.Pending.Proposed)
		if value.Pending.Disable != nil {
			disable := cloneGatewayCurrentDisableRequest(*value.Pending.Disable)
			pending.Disable = &disable
		}
		result.Pending = &pending
	}
	if value.LANRecovery != nil {
		recovery := *value.LANRecovery
		recovery.Items = make([]gatewayCurrentLANRecoveryItem, len(value.LANRecovery.Items))
		for index, item := range value.LANRecovery.Items {
			copy := item
			if item.Grant != nil {
				grant := cloneGatewayCurrentOperationBinding(*item.Grant)
				copy.Grant = &grant
			}
			if item.Disable != nil {
				disable := cloneGatewayCurrentDisableRequest(*item.Disable)
				copy.Disable = &disable
			}
			recovery.Items[index] = copy
		}
		if value.LANRecovery.LegacyPending != nil {
			legacy := cloneGatewayCurrentOperationPending(*value.LANRecovery.LegacyPending)
			recovery.LegacyPending = &legacy
		}
		result.LANRecovery = &recovery
	}
	return result
}

func gatewayCurrentMutationSelectionMatches(before, after gatewayCurrentSelection,
	want gatewayCurrentRouteState,
) bool {
	return after.Kind == gatewayCurrentSelectionRebind && after.Store != nil && after.State != nil &&
		before.Store != nil && after.Lineage == before.Lineage && after.Store.path == before.Store.path &&
		reflect.DeepEqual(*after.State, want)
}

func cloneGatewayCurrentOperationPending(value gatewayCurrentPendingRoute) gatewayCurrentPendingRoute {
	result := value
	if value.Previous != nil {
		previous := cloneGatewayCurrentOperationApp(*value.Previous)
		result.Previous = &previous
	}
	result.Proposed = cloneGatewayCurrentOperationApp(value.Proposed)
	if value.Disable != nil {
		disable := cloneGatewayCurrentDisableRequest(*value.Disable)
		result.Disable = &disable
	}
	return result
}

func cloneGatewayCurrentOperationApp(value gatewayCurrentAppRoute) gatewayCurrentAppRoute {
	value.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), value.Route.Endpoints...)
	if value.LAN != nil {
		binding := cloneGatewayCurrentOperationBinding(*value.LAN)
		value.LAN = &binding
	}
	return value
}

func cloneGatewayCurrentOperationBinding(value gatewayCurrentLANBinding) gatewayCurrentLANBinding {
	if value.Transfer != nil {
		transfer := *value.Transfer
		if value.Transfer.PredecessorTransferDigest != nil {
			predecessor := *value.Transfer.PredecessorTransferDigest
			transfer.PredecessorTransferDigest = &predecessor
		}
		value.Transfer = &transfer
	}
	return value
}

func cloneGatewayCurrentDisableRequest(value GatewayV2LANDisableRequest) GatewayV2LANDisableRequest {
	if value.SourceGrant != nil {
		grant := *value.SourceGrant
		value.SourceGrant = &grant
	}
	return value
}

func gatewayCurrentRouteOperationError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}
