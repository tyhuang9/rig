package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// managedGatewayCurrentPhysicalDriver is the production adapter for ordinary
// operations on a SQL-selected rebind generation. The embedded driver retains
// the independently implemented predecessor-retirement method.
type managedGatewayCurrentPhysicalDriver struct {
	managerGatewayCurrentPhysicalDriver
	runtime gatewayCurrentPhysicalRuntime
}

type gatewayCurrentPhysicalRuntime interface {
	observe(context.Context, gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
	reconcile(context.Context, gatewayCurrentPhysicalTarget, func(context.Context) error) error
	stop(context.Context, []gatewayCurrentPhysicalTarget, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

// gatewayCurrentPhysicalTarget is invocation-local authority. Terminal and
// Resources are immutable protected evidence; State is one canonical logical
// endpoint. Pending/LANRecovery retain the exact durable recovery marker that
// must still exist while an ordinary effect runs.
type gatewayCurrentPhysicalTarget struct {
	State       gatewayCurrentRouteState
	Lineage     appaccess.GatewayCurrentLineageRef
	Terminal    gatewayRebindAttemptTerminalView
	Identity    gatewayCurrentIdentity
	Resources   gatewayRebindFinalHandoverResourceBindings
	Pending     *gatewayCurrentPendingRoute
	LANRecovery *gatewayCurrentLANRecoveryBatch
}

func newManagedGatewayCurrentPhysicalDriver(m *Manager) gatewayCurrentPhysicalDriver {
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: m},
	}
	driver.runtime = managerGatewayCurrentPhysicalRuntime{
		manager: m, hostProbe: probeGatewayV2HostStatus,
		containerProbe: func(ctx context.Context, id, address string, port uint16, host, challenge string) bool {
			return m != nil && m.probeGatewayV2ContainerChallenge(ctx, id, address, port, host, challenge)
		},
	}
	return driver
}

func (d managedGatewayCurrentPhysicalDriver) applyGatewayCurrentPhysical(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	return d.applyGatewayCurrentPhysicalAuthorized(ctx, transition, nil)
}

func (d managedGatewayCurrentPhysicalDriver) applyGatewayCurrentPhysicalAuthorized(ctx context.Context,
	transition gatewayCurrentPhysicalTransition, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	selection, err := d.selectExact(ctx, transition.Pending)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, transition.Effective,
		transition.Pending.Pending, nil)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	guard := func(effectCtx context.Context) error {
		if _, guardErr := d.selectExact(effectCtx, transition.Pending); guardErr != nil {
			return guardErr
		}
		if authorize != nil {
			return authorize(effectCtx)
		}
		return nil
	}
	return d.reconcile(ctx, target, guard, gatewayCurrentPhysicalRecoveryEffective)
}

func (d managedGatewayCurrentPhysicalDriver) restoreGatewayCurrentPhysical(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	return d.restoreGatewayCurrentPhysicalAuthorized(ctx, transition, nil)
}

func (d managedGatewayCurrentPhysicalDriver) restoreGatewayCurrentPhysicalAuthorized(ctx context.Context,
	transition gatewayCurrentPhysicalTransition, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	selection, err := d.selectExact(ctx, transition.Pending)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, transition.Before,
		transition.Pending.Pending, nil)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	guard := func(effectCtx context.Context) error {
		if _, guardErr := d.selectExact(effectCtx, transition.Pending); guardErr != nil {
			return guardErr
		}
		if authorize != nil {
			return authorize(effectCtx)
		}
		return nil
	}
	return d.reconcile(ctx, target, guard, gatewayCurrentPhysicalRecoveryBefore)
}

func (d managedGatewayCurrentPhysicalDriver) attestGatewayCurrentPhysical(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	if selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	fresh, err := d.selectExact(ctx, *selection.State)
	if err != nil || fresh.Lineage != selection.Lineage {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	targets, err := gatewayCurrentPhysicalTargetsForSelection(fresh)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	first, err := d.observeAny(ctx, targets)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	if _, err := d.selectExact(ctx, *selection.State); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	second, err := d.observeAny(ctx, targets)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if _, err := d.selectExact(ctx, *selection.State); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	return second, nil
}

// stopGatewayCurrentPhysical may be reached precisely because a protected
// save acknowledgement or SQL selection cannot be re-established. Its
// purpose-bound target is captured and revalidated from protected evidence;
// it authorizes withdrawal only and cannot authorize serving or select a
// different protected generation.
func (d managedGatewayCurrentPhysicalDriver) stopGatewayCurrentPhysical(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	m := d.manager()
	if m == nil || !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	owned, err := m.gatewayCurrentOwnedStopTargetForTransitionLocked(transition)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	proof, err := d.stopGatewayCurrentOwned(ctx, owned)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	if !reflect.DeepEqual(proof.State, transition.Before) && !reflect.DeepEqual(proof.State, transition.Effective) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return proof, nil
}

// stopGatewayCurrentOwnedTarget implements the protected-only emergency and
// batch-retirement withdrawal seam. It deliberately has no SQL selection or
// process-latch prerequisite: those conditions may be unavailable precisely
// when withdrawal is required. The purpose-bound target can authorize only
// its exact final container identity and canonical physical endpoint(s).
func (d managedGatewayCurrentPhysicalDriver) stopGatewayCurrentOwnedTarget(ctx context.Context,
	owned gatewayCurrentOwnedStopTarget,
) error {
	_, err := d.stopGatewayCurrentOwned(ctx, owned)
	return err
}

func (d managedGatewayCurrentPhysicalDriver) stopGatewayCurrentOwned(ctx context.Context,
	owned gatewayCurrentOwnedStopTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	m := d.manager()
	if m == nil || d.runtime == nil || ctx == nil || ctx.Err() != nil || !validGatewayCurrentOwnedStopTarget(owned) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	targets, err := gatewayCurrentPhysicalTargetsForOwnedStop(owned)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	guard := func(effectCtx context.Context) error {
		confirmed, confirmErr := m.revalidateGatewayCurrentOwnedStopTargetLocked(owned)
		if confirmErr != nil || confirmed.Lineage != owned.Lineage ||
			!reflect.DeepEqual(confirmed.Terminal, owned.Terminal) ||
			!reflect.DeepEqual(confirmed.FinalContainer, owned.FinalContainer) {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		return nil
	}
	proof, err := d.runtime.stop(ctx, targets, guard)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped ||
		!reflect.DeepEqual(proof.Lineage, owned.Lineage) ||
		!reflect.DeepEqual(proof.Terminal, owned.Terminal) ||
		!reflect.DeepEqual(proof.Resources.FinalContainer, &owned.FinalContainer) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager())
	defer cancel()
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	matched := false
	for _, target := range targets {
		matched = matched || (reflect.DeepEqual(proof.State, target.State) &&
			reflect.DeepEqual(proof.Pending, target.Pending) &&
			reflect.DeepEqual(proof.LANRecovery, target.LANRecovery))
	}
	if !matched {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return proof, nil
}

func (d managedGatewayCurrentPhysicalDriver) reconcile(ctx context.Context, target gatewayCurrentPhysicalTarget,
	guard func(context.Context) error, expected gatewayCurrentPhysicalOutcome,
) (gatewayCurrentPhysicalAttestation, error) {
	if d.runtime == nil || guard == nil ||
		(expected != gatewayCurrentPhysicalRecoveryBefore && expected != gatewayCurrentPhysicalRecoveryEffective) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	proof, err := d.runtime.observe(ctx, target)
	if err == nil && gatewayCurrentPhysicalAttestationAtTarget(proof, target) && proof.Outcome == expected {
		if confirmErr := guard(ctx); confirmErr != nil {
			return gatewayCurrentPhysicalAttestation{}, confirmErr
		}
		return proof, nil
	}
	if err := d.runtime.reconcile(ctx, target, guard); err != nil {
		// A cancelled command or lost acknowledgement may have completed. Use
		// an independent bounded read to return success only for the exact goal.
		proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager())
		defer cancel()
		proof, proofErr := d.runtime.observe(proofCtx, target)
		if proofErr != nil || !gatewayCurrentPhysicalAttestationAtTarget(proof, target) || proof.Outcome != expected {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		if confirmErr := guard(proofCtx); confirmErr != nil {
			return gatewayCurrentPhysicalAttestation{}, confirmErr
		}
		return proof, nil
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager())
	defer cancel()
	proof, err = d.runtime.observe(proofCtx, target)
	if err != nil || !gatewayCurrentPhysicalAttestationAtTarget(proof, target) || proof.Outcome != expected {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	return proof, nil
}

func (d managedGatewayCurrentPhysicalDriver) observeAny(ctx context.Context,
	targets []gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	var matched *gatewayCurrentPhysicalAttestation
	for _, target := range targets {
		value, err := d.runtime.observe(ctx, target)
		if err != nil {
			continue
		}
		if matched != nil {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		copy := value
		matched = &copy
	}
	if matched == nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return *matched, nil
}

func (d managedGatewayCurrentPhysicalDriver) selectExact(ctx context.Context,
	want gatewayCurrentRouteState,
) (gatewayCurrentSelection, error) {
	m := d.manager()
	if m == nil || ctx == nil || ctx.Err() != nil || m.gatewayRebindAdmissionBlocked() ||
		!validGatewayCurrentRouteState(want) {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	selection, snapshot, err := m.readGatewayCurrentSelectionLocked(ctx)
	if err != nil || !gatewayCurrentRouteMutationSnapshotReady(snapshot) ||
		selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil ||
		!reflect.DeepEqual(*selection.State, want) || m.gatewayRebindAdmissionBlocked() || ctx.Err() != nil {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := m.observeGatewayCurrentPredecessorsRetiredLocked(ctx, selection, snapshot); err != nil {
		return gatewayCurrentSelection{}, err
	}
	return selection, nil
}

func (d managedGatewayCurrentPhysicalDriver) manager() *Manager {
	return d.managerGatewayCurrentPhysicalDriver.manager
}

func gatewayCurrentPhysicalTargetForOwnedStop(owned gatewayCurrentOwnedStopTarget,
	state gatewayCurrentRouteState,
) (gatewayCurrentPhysicalTarget, error) {
	if !validGatewayCurrentOwnedStopTarget(owned) || !validGatewayCurrentRouteState(state) ||
		state.Pending != nil || state.LANRecovery != nil || state.Lineage != owned.Lineage ||
		!gatewayCurrentOwnedStopDigestPermitted(owned, state.Digest) ||
		owned.Terminal.Resources.FinalContainer == nil ||
		!reflect.DeepEqual(owned.FinalContainer, *owned.Terminal.Resources.FinalContainer) ||
		!reflect.DeepEqual(state.Identity.Rebind, &owned.Terminal.SuccessorIdentity) {
		return gatewayCurrentPhysicalTarget{}, errors.New("invalid generated ingress current owned-stop physical target")
	}
	return gatewayCurrentPhysicalTarget{State: cloneGatewayCurrentRouteState(state), Lineage: state.Lineage,
		Terminal: owned.Terminal, Identity: state.Identity, Resources: owned.Terminal.Resources,
		Pending: cloneGatewayCurrentPendingRoute(owned.Pending)}, nil
}

func gatewayCurrentPhysicalTargetsForOwnedStop(owned gatewayCurrentOwnedStopTarget) (
	[]gatewayCurrentPhysicalTarget, error,
) {
	if !validGatewayCurrentOwnedStopTarget(owned) {
		return nil, errors.New("invalid generated ingress current owned-stop target")
	}
	if owned.Transition != nil {
		result := make([]gatewayCurrentPhysicalTarget, 0, 2)
		for _, state := range []gatewayCurrentRouteState{owned.Transition.Before, owned.Transition.Effective} {
			target, err := gatewayCurrentPhysicalTargetForOwnedStop(owned, state)
			if err != nil {
				return nil, err
			}
			result = append(result, target)
		}
		return result, nil
	}
	states := []gatewayCurrentRouteState{owned.State}
	if owned.State.Pending != nil || owned.State.LANRecovery != nil {
		projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(owned.State)
		if err != nil {
			return nil, err
		}
		states = []gatewayCurrentRouteState{projection.Before}
		if projection.Effective != nil && projection.Effective.Digest != projection.Before.Digest {
			states = append(states, *projection.Effective)
		}
		if projection.Withdrawn != nil && projection.Withdrawn.Digest != projection.Before.Digest &&
			(projection.Effective == nil || projection.Withdrawn.Digest != projection.Effective.Digest) {
			states = append(states, *projection.Withdrawn)
		}
	}
	result := make([]gatewayCurrentPhysicalTarget, 0, len(states))
	for _, state := range states {
		target, err := gatewayCurrentPhysicalTargetForOwnedStateStop(owned, state)
		if err != nil {
			return nil, err
		}
		result = append(result, target)
	}
	return result, nil
}

func gatewayCurrentPhysicalTargetForOwnedStateStop(owned gatewayCurrentOwnedStopTarget,
	state gatewayCurrentRouteState,
) (gatewayCurrentPhysicalTarget, error) {
	if !validGatewayCurrentOwnedStopTarget(owned) || owned.Transition != nil ||
		!validGatewayCurrentRouteState(state) || state.Pending != nil || state.LANRecovery != nil ||
		state.Lineage != owned.Lineage || owned.Terminal.Resources.FinalContainer == nil ||
		!reflect.DeepEqual(owned.FinalContainer, *owned.Terminal.Resources.FinalContainer) ||
		!reflect.DeepEqual(state.Identity.Rebind, &owned.Terminal.SuccessorIdentity) {
		return gatewayCurrentPhysicalTarget{}, errors.New("invalid generated ingress current owned-state physical target")
	}
	matched := false
	if owned.State.Pending == nil && owned.State.LANRecovery == nil {
		matched = reflect.DeepEqual(state, owned.State)
	} else if projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(owned.State); err == nil {
		matched = reflect.DeepEqual(state, projection.Before) ||
			(projection.Effective != nil && reflect.DeepEqual(state, *projection.Effective)) ||
			(projection.Withdrawn != nil && reflect.DeepEqual(state, *projection.Withdrawn))
	}
	if !matched {
		return gatewayCurrentPhysicalTarget{}, errors.New("generated ingress current owned-state projection is unauthorized")
	}
	return gatewayCurrentPhysicalTarget{State: cloneGatewayCurrentRouteState(state), Lineage: owned.Lineage,
		Terminal: owned.Terminal, Identity: state.Identity, Resources: owned.Terminal.Resources,
		Pending:     cloneGatewayCurrentPendingRoute(owned.Pending),
		LANRecovery: cloneGatewayCurrentLANRecoveryBatch(owned.State.LANRecovery)}, nil
}

func gatewayCurrentPhysicalTargetFor(selection gatewayCurrentSelection, state gatewayCurrentRouteState,
	pending *gatewayCurrentPendingRoute, recovery *gatewayCurrentLANRecoveryBatch,
) (gatewayCurrentPhysicalTarget, error) {
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.Lineage != state.Lineage ||
		!validGatewayCurrentRouteState(state) || state.Pending != nil || state.LANRecovery != nil ||
		!gatewayRebindAttemptTerminalMatchesLineage(terminal, state.Lineage) ||
		terminal.Resources.FinalContainer == nil || !reflect.DeepEqual(state.Identity.Rebind, &terminal.SuccessorIdentity) {
		return gatewayCurrentPhysicalTarget{}, errors.New("invalid generated ingress current physical target")
	}
	return gatewayCurrentPhysicalTarget{State: cloneGatewayCurrentRouteState(state), Lineage: state.Lineage,
		Terminal: terminal, Identity: state.Identity, Resources: terminal.Resources,
		Pending: cloneGatewayCurrentPendingRoute(pending), LANRecovery: cloneGatewayCurrentLANRecoveryBatch(recovery)}, nil
}

func gatewayCurrentPhysicalTargetsForSelection(selection gatewayCurrentSelection) ([]gatewayCurrentPhysicalTarget, error) {
	if selection.State == nil {
		return nil, errors.New("invalid generated ingress current physical selection")
	}
	selected := *selection.State
	if selected.Pending == nil && selected.LANRecovery == nil {
		target, err := gatewayCurrentPhysicalTargetFor(selection, selected, nil, nil)
		return []gatewayCurrentPhysicalTarget{target}, err
	}
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(selected)
	if err != nil {
		return nil, err
	}
	before, err := gatewayCurrentPhysicalTargetFor(selection, projection.Before, selected.Pending, selected.LANRecovery)
	if err != nil {
		return nil, err
	}
	result := []gatewayCurrentPhysicalTarget{before}
	if projection.Effective != nil {
		effective, err := gatewayCurrentPhysicalTargetFor(selection, *projection.Effective,
			selected.Pending, selected.LANRecovery)
		if err != nil {
			return nil, err
		}
		result = append(result, effective)
	}
	if projection.Withdrawn != nil && projection.Withdrawn.Digest != projection.Before.Digest &&
		(projection.Effective == nil || projection.Withdrawn.Digest != projection.Effective.Digest) {
		withdrawn, err := gatewayCurrentPhysicalTargetFor(selection, *projection.Withdrawn,
			selected.Pending, selected.LANRecovery)
		if err != nil {
			return nil, err
		}
		result = append(result, withdrawn)
	}
	return result, nil
}

func gatewayCurrentPhysicalAttestationAtTarget(value gatewayCurrentPhysicalAttestation,
	target gatewayCurrentPhysicalTarget,
) bool {
	return validGatewayCurrentPhysicalAttestation(value) && value.Outcome != gatewayCurrentPhysicalRecoveryStopped &&
		reflect.DeepEqual(value.State, target.State) && value.Lineage == target.Lineage &&
		gatewayRebindAttemptTerminalViewSame(value.Terminal, target.Terminal) &&
		reflect.DeepEqual(value.Pending, target.Pending) && reflect.DeepEqual(value.LANRecovery, target.LANRecovery)
}

func gatewayRebindAttemptTerminalViewSame(left, right gatewayRebindAttemptTerminalView) bool {
	return reflect.DeepEqual(left, right)
}

func gatewayCurrentPhysicalProofContext(ctx context.Context, m *Manager) (context.Context, context.CancelFunc) {
	timeout := v2ObservationTimeout
	if m != nil && m.options.CommandTimeout > 0 && m.options.CommandTimeout*3 < timeout {
		timeout = m.options.CommandTimeout * 3
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func gatewayCurrentPhysicalDriverError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

var _ gatewayCurrentPhysicalDriver = managedGatewayCurrentPhysicalDriver{}
var _ gatewayCurrentOwnedStopDriver = managedGatewayCurrentPhysicalDriver{}
