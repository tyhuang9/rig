package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentPhysicalTransitionKind string

const (
	gatewayCurrentPhysicalRouteSwitch gatewayCurrentPhysicalTransitionKind = "route_switch"
	gatewayCurrentPhysicalLANGrant    gatewayCurrentPhysicalTransitionKind = "lan_grant"
	gatewayCurrentPhysicalLANDisable  gatewayCurrentPhysicalTransitionKind = "lan_disable"
	// gatewayCurrentPhysicalLANWithdrawal is the request-bound quarantine
	// written after an otherwise effective grant cannot complete its terminal
	// callback. It carries the exact prior grant in Pending.Previous and never
	// fabricates a disable request or operation identity.
	gatewayCurrentPhysicalLANWithdrawal gatewayCurrentPhysicalTransitionKind = "lan_grant_withdrawal"
)

type gatewayCurrentPhysicalOutcome string

const (
	gatewayCurrentPhysicalStableServing     gatewayCurrentPhysicalOutcome = "stable_serving"
	gatewayCurrentPhysicalRecoveryBefore    gatewayCurrentPhysicalOutcome = "recovery_before"
	gatewayCurrentPhysicalRecoveryEffective gatewayCurrentPhysicalOutcome = "recovery_effective"
	gatewayCurrentPhysicalRecoveryMixed     gatewayCurrentPhysicalOutcome = "recovery_mixed"
	gatewayCurrentPhysicalRecoveryStopped   gatewayCurrentPhysicalOutcome = "recovery_stopped"
)

// gatewayCurrentPhysicalTransition is the complete protected intent supplied
// to the physical adapter. Pending is durable before apply is called. The
// adapter never writes protected state and returns only fresh observation.
type gatewayCurrentPhysicalTransition struct {
	Kind      gatewayCurrentPhysicalTransitionKind
	AppID     string
	Before    gatewayCurrentRouteState
	Pending   gatewayCurrentRouteState
	Effective gatewayCurrentRouteState
}

type gatewayCurrentRuntimeProof struct {
	Profile                   GatewayV2ProfileBinding `json:"profile"`
	ImageID                   string                  `json:"imageId"`
	ContainerID               string                  `json:"containerId"`
	ActiveConfigDigest        string                  `json:"activeConfigDigest"`
	RoutePlanDigest           string                  `json:"routePlanDigest"`
	ListenerDigest            string                  `json:"listenerDigest"`
	ListenerAbsent            bool                    `json:"listenerAbsent,omitempty"`
	ApplicationNetworksDigest string                  `json:"applicationNetworksDigest"`
	Digest                    string                  `json:"digest"`
}

type gatewayCurrentPhysicalAttestation struct {
	Outcome     gatewayCurrentPhysicalOutcome
	State       gatewayCurrentRouteState
	Lineage     appaccess.GatewayCurrentLineageRef
	Terminal    gatewayRebindAttemptTerminalView
	Identity    gatewayCurrentIdentity
	Resources   gatewayRebindFinalHandoverResourceBindings
	Runtime     gatewayCurrentRuntimeProof
	Pending     *gatewayCurrentPendingRoute
	LANRecovery *gatewayCurrentLANRecoveryBatch
	Digest      string
}

// gatewayCurrentPhysicalOutcomeProjection is the only logical-state
// normalization used by read/startup attestation. Both projections retain the
// selected protected revision, clear recovery metadata, and recompute their
// digest. Transition execution continues to use its separately retained
// original Before revision.
type gatewayCurrentPhysicalOutcomeProjection struct {
	Before    gatewayCurrentRouteState
	Effective *gatewayCurrentRouteState
}

type gatewayCurrentPhysicalDriver interface {
	applyGatewayCurrentPhysical(context.Context, gatewayCurrentPhysicalTransition) (gatewayCurrentPhysicalAttestation, error)
	restoreGatewayCurrentPhysical(context.Context, gatewayCurrentPhysicalTransition) (gatewayCurrentPhysicalAttestation, error)
	stopGatewayCurrentPhysical(context.Context, gatewayCurrentPhysicalTransition) (gatewayCurrentPhysicalAttestation, error)
	attestGatewayCurrentPhysical(context.Context, gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error)
	stopGatewayCurrentOwnedPredecessor(context.Context, gatewayRebindAttemptTerminalView) error
}

type managerGatewayCurrentPhysicalDriver struct{ manager *Manager }

func (managerGatewayCurrentPhysicalDriver) applyGatewayCurrentPhysical(context.Context,
	gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	return gatewayCurrentPhysicalAttestation{}, errors.New("generated ingress current physical apply is unavailable")
}

func (managerGatewayCurrentPhysicalDriver) restoreGatewayCurrentPhysical(context.Context,
	gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	return gatewayCurrentPhysicalAttestation{}, errors.New("generated ingress current physical restore is unavailable")
}

func (managerGatewayCurrentPhysicalDriver) stopGatewayCurrentPhysical(context.Context,
	gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	return gatewayCurrentPhysicalAttestation{}, errors.New("generated ingress current physical stop is unavailable")
}

func (managerGatewayCurrentPhysicalDriver) attestGatewayCurrentPhysical(context.Context,
	gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	return gatewayCurrentPhysicalAttestation{}, errors.New("generated ingress current physical attestation is unavailable")
}

func (managerGatewayCurrentPhysicalDriver) stopGatewayCurrentOwnedPredecessor(context.Context,
	gatewayRebindAttemptTerminalView,
) error {
	return errors.New("generated ingress current predecessor stop is unavailable")
}

func (m *Manager) currentPhysicalDriver() gatewayCurrentPhysicalDriver {
	if m != nil && m.gatewayCurrentPhysicalDriver != nil {
		return m.gatewayCurrentPhysicalDriver
	}
	return managerGatewayCurrentPhysicalDriver{manager: m}
}

func (m *Manager) applyGatewayCurrentPhysicalLocked(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	value, err := m.currentPhysicalDriver().applyGatewayCurrentPhysical(ctx, transition)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) ||
		value.Outcome != gatewayCurrentPhysicalRecoveryEffective ||
		value.Lineage != transition.Before.Lineage ||
		!reflect.DeepEqual(value.State, transition.Effective) ||
		!reflect.DeepEqual(value.Pending, transition.Pending.Pending) || value.LANRecovery != nil {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return value, nil
}

// restoreGatewayCurrentPhysicalLocked returns an exact route or grant
// transition to its original Before topology. RouteSwitch recovery preserves
// the deployment head's rollback semantics. A pre-activation LANGrant removes
// the uncommitted publication; a durable withdrawal republishes the exact
// grant only after the caller proves SQL committed. The marker remains
// installed during the effect, and clearing it is a separate reconciled step.
func (m *Manager) restoreGatewayCurrentPhysicalLocked(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	if m == nil || ctx == nil || ctx.Err() != nil ||
		(transition.Kind != gatewayCurrentPhysicalRouteSwitch && transition.Kind != gatewayCurrentPhysicalLANGrant &&
			transition.Kind != gatewayCurrentPhysicalLANWithdrawal) ||
		!validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	value, err := m.currentPhysicalDriver().restoreGatewayCurrentPhysical(ctx, transition)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) ||
		value.Outcome != gatewayCurrentPhysicalRecoveryBefore ||
		value.Lineage != transition.Before.Lineage ||
		!reflect.DeepEqual(value.State, transition.Before) ||
		!reflect.DeepEqual(value.Pending, transition.Pending.Pending) || value.LANRecovery != nil {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return value, nil
}

// stopGatewayCurrentPhysicalLocked is the fail-closed compensation for an
// ambiguous protected save in an ordinary operation. It stops only the exact
// selected generation and transition resources. It is deliberately distinct
// from stopGatewayCurrentOwnedPredecessorLocked, which retires the old
// generation after a rebind commit.
func (m *Manager) stopGatewayCurrentPhysicalLocked(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	value, err := m.currentPhysicalDriver().stopGatewayCurrentPhysical(ctx, transition)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) ||
		value.Outcome != gatewayCurrentPhysicalRecoveryStopped ||
		value.Lineage != transition.Before.Lineage ||
		(!reflect.DeepEqual(value.State, transition.Before) && !reflect.DeepEqual(value.State, transition.Effective)) ||
		!reflect.DeepEqual(value.Pending, transition.Pending.Pending) || value.LANRecovery != nil {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return value, nil
}

func (m *Manager) attestGatewayCurrentPhysicalLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	terminal, terminalErr := gatewayCurrentSelectionTerminalView(selection)
	if m == nil || ctx == nil || ctx.Err() != nil || selection.Kind != gatewayCurrentSelectionRebind ||
		selection.State == nil || terminalErr != nil ||
		!gatewayRebindAttemptTerminalMatchesLineage(terminal, selection.Lineage) ||
		selection.State.Lineage != selection.Lineage || !validGatewayCurrentRouteState(*selection.State) {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	value, err := m.currentPhysicalDriver().attestGatewayCurrentPhysical(ctx, selection)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) || value.Lineage != selection.Lineage ||
		value.Terminal.Digest != terminal.Digest ||
		!reflect.DeepEqual(value.Pending, selection.State.Pending) ||
		!reflect.DeepEqual(value.LANRecovery, selection.State.LANRecovery) {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	if !gatewayCurrentPhysicalAttestationMatchesSelection(value, *selection.State) {
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return value, nil
}

// gatewayCurrentSelectionTerminalView is the common receipt adapter used by
// ordinary operations. The typed terminal checkpoint extends this selector
// without changing the physical driver contract or legacy receipt bytes.
func gatewayCurrentSelectionTerminalView(selection gatewayCurrentSelection) (gatewayRebindAttemptTerminalView, error) {
	if selection.Receipt == nil {
		return gatewayRebindAttemptTerminalView{}, errors.New("generated ingress current terminal is unavailable")
	}
	return newGatewayRebindAttemptTerminalViewLegacy(*selection.Receipt)
}

func (m *Manager) stopGatewayCurrentOwnedPredecessorLocked(ctx context.Context,
	terminal gatewayRebindAttemptTerminalView,
) error {
	if m == nil || ctx == nil || ctx.Err() != nil || terminal.Disposition != gatewayRebindFinalHandoverTerminalCommit {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return m.currentPhysicalDriver().stopGatewayCurrentOwnedPredecessor(ctx, terminal)
}

func gatewayCurrentRuntimeProofDigest(value gatewayCurrentRuntimeProof) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayCurrentRuntimeProof(value gatewayCurrentRuntimeProof, state gatewayCurrentRouteState,
	resources gatewayRebindFinalHandoverResourceBindings,
) bool {
	if value.Profile != (GatewayV2ProfileBinding{RevisionID: state.Profile.RevisionID,
		RevisionNumber: state.Profile.RevisionNumber, SpecDigest: state.Profile.SpecDigest,
		SelectedIPv4: state.Profile.SelectedIPv4, InterfaceID: state.Profile.InterfaceID,
		PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd}) ||
		resources.FinalContainer == nil || value.ImageID != resources.ImageID ||
		value.ContainerID != resources.FinalContainer.ID || !validSHA256(value.ImageID) ||
		!validContainerID(value.ContainerID) || !validSHA256(value.ActiveConfigDigest) ||
		!validSHA256(value.RoutePlanDigest) || !validSHA256(value.ListenerDigest) ||
		!validSHA256(value.ApplicationNetworksDigest) || !validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayCurrentRuntimeProofDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayCurrentPhysicalAttestationDigest(value gatewayCurrentPhysicalAttestation) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayCurrentPhysicalAttestation(value gatewayCurrentPhysicalAttestation) bool {
	if value.Outcome != gatewayCurrentPhysicalStableServing && value.Outcome != gatewayCurrentPhysicalRecoveryBefore &&
		value.Outcome != gatewayCurrentPhysicalRecoveryEffective && value.Outcome != gatewayCurrentPhysicalRecoveryMixed &&
		value.Outcome != gatewayCurrentPhysicalRecoveryStopped {
		return false
	}
	if !validGatewayCurrentRouteState(value.State) || value.State.Pending != nil || value.State.LANRecovery != nil ||
		value.Lineage != value.State.Lineage ||
		!gatewayRebindAttemptTerminalMatchesLineage(value.Terminal, value.Lineage) ||
		!reflect.DeepEqual(value.Identity, value.State.Identity) ||
		!reflect.DeepEqual(value.Resources, value.Terminal.Resources) ||
		!validGatewayCurrentRuntimeProof(value.Runtime, value.State, value.Resources) || !validSHA256(value.Digest) {
		return false
	}
	if value.Outcome == gatewayCurrentPhysicalStableServing && (value.Pending != nil || value.LANRecovery != nil) {
		return false
	}
	if value.Runtime.ListenerAbsent != (value.Outcome == gatewayCurrentPhysicalRecoveryStopped) {
		return false
	}
	digest, err := gatewayCurrentPhysicalAttestationDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayCurrentPhysicalAttestationMatchesSelection(value gatewayCurrentPhysicalAttestation,
	selected gatewayCurrentRouteState,
) bool {
	if selected.Pending == nil && selected.LANRecovery == nil {
		return value.Outcome == gatewayCurrentPhysicalStableServing && reflect.DeepEqual(value.State, selected)
	}
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(selected)
	if err != nil {
		return false
	}
	if selected.LANRecovery != nil {
		return (value.Outcome == gatewayCurrentPhysicalRecoveryMixed ||
			value.Outcome == gatewayCurrentPhysicalRecoveryStopped) && reflect.DeepEqual(value.State, projection.Before)
	}
	switch value.Outcome {
	case gatewayCurrentPhysicalRecoveryBefore:
		return reflect.DeepEqual(value.State, projection.Before)
	case gatewayCurrentPhysicalRecoveryEffective:
		return projection.Effective != nil && reflect.DeepEqual(value.State, *projection.Effective)
	case gatewayCurrentPhysicalRecoveryMixed, gatewayCurrentPhysicalRecoveryStopped:
		return reflect.DeepEqual(value.State, projection.Before) ||
			(projection.Effective != nil && reflect.DeepEqual(value.State, *projection.Effective))
	default:
		return false
	}
}

func gatewayCurrentPhysicalOutcomeProjectionForSelection(selected gatewayCurrentRouteState,
) (gatewayCurrentPhysicalOutcomeProjection, error) {
	if !validGatewayCurrentRouteState(selected) || (selected.Pending == nil && selected.LANRecovery == nil) {
		return gatewayCurrentPhysicalOutcomeProjection{}, errors.New("invalid current physical outcome projection")
	}
	before := cloneGatewayCurrentRouteState(selected)
	before.Pending, before.LANRecovery = nil, nil
	before.Digest, _ = gatewayCurrentRouteStateDigest(before)
	if !validGatewayCurrentRouteState(before) {
		return gatewayCurrentPhysicalOutcomeProjection{}, errors.New("invalid current physical before projection")
	}
	result := gatewayCurrentPhysicalOutcomeProjection{Before: before}
	if selected.Pending == nil {
		return result, nil
	}
	effective := cloneGatewayCurrentRouteState(before)
	effective.Apps[selected.Pending.AppID] = cloneGatewayCurrentAppRoute(selected.Pending.Proposed)
	effective.Digest, _ = gatewayCurrentRouteStateDigest(effective)
	if !validGatewayCurrentRouteState(effective) {
		return gatewayCurrentPhysicalOutcomeProjection{}, errors.New("invalid current physical effective projection")
	}
	result.Effective = &effective
	return result, nil
}

func validGatewayCurrentPhysicalTransition(value gatewayCurrentPhysicalTransition) bool {
	if !validAppID(value.AppID) || (value.Kind != gatewayCurrentPhysicalRouteSwitch &&
		value.Kind != gatewayCurrentPhysicalLANGrant && value.Kind != gatewayCurrentPhysicalLANDisable &&
		value.Kind != gatewayCurrentPhysicalLANWithdrawal) ||
		!validGatewayCurrentRouteState(value.Before) || value.Before.Pending != nil || value.Before.LANRecovery != nil ||
		!validGatewayCurrentRouteState(value.Pending) || value.Pending.Pending == nil || value.Pending.LANRecovery != nil ||
		!validGatewayCurrentRouteState(value.Effective) || value.Effective.Pending != nil || value.Effective.LANRecovery != nil ||
		!sameGatewayCurrentRouteOrigin(value.Before, value.Pending) ||
		!sameGatewayCurrentRouteOrigin(value.Before, value.Effective) ||
		!gatewayCurrentPhysicalPendingRevisionMatches(value) || value.Effective.Revision != value.Pending.Revision+1 ||
		!reflect.DeepEqual(value.Before.Apps, value.Pending.Apps) || value.Pending.Pending.AppID != value.AppID {
		return false
	}
	proposed := cloneGatewayCurrentRouteState(value.Before)
	proposed.Revision = value.Effective.Revision
	proposed.Apps[value.AppID] = cloneGatewayCurrentAppRoute(value.Pending.Pending.Proposed)
	proposed.Digest = value.Effective.Digest
	if value.Pending.Pending.Kind == gatewayV2PendingLANDisable {
		proposed.Apps[value.AppID] = cloneGatewayCurrentAppRoute(value.Pending.Pending.Proposed)
	}
	if !reflect.DeepEqual(proposed, value.Effective) {
		return false
	}
	switch value.Kind {
	case gatewayCurrentPhysicalRouteSwitch:
		return value.Pending.Pending.Kind == gatewayV2PendingRouteSwitch || value.Pending.Pending.Kind == ""
	case gatewayCurrentPhysicalLANGrant:
		return value.Pending.Pending.Kind == gatewayV2PendingLANGrant
	case gatewayCurrentPhysicalLANDisable:
		return value.Pending.Pending.Kind == gatewayV2PendingLANDisable
	case gatewayCurrentPhysicalLANWithdrawal:
		return value.Pending.Pending.Kind == gatewayV2PendingLANWithdrawal
	}
	return false
}

func gatewayCurrentPhysicalPendingRevisionMatches(value gatewayCurrentPhysicalTransition) bool {
	if value.Before.Revision == ^uint64(0) {
		return false
	}
	want := value.Before.Revision + 1
	if value.Kind == gatewayCurrentPhysicalLANGrant && value.Pending.Pending != nil &&
		value.Pending.Pending.ActivationUncertain {
		if want == ^uint64(0) {
			return false
		}
		want++
	}
	return value.Pending.Revision == want
}

func validGatewayCurrentPendingRoute(state gatewayCurrentRouteState) bool {
	pending := state.Pending
	if pending == nil {
		return true
	}
	if !validAppID(pending.AppID) {
		return false
	}
	committed, exists := state.Apps[pending.AppID]
	if exists != (pending.Previous != nil) || (pending.Previous != nil && !reflect.DeepEqual(committed, *pending.Previous)) {
		return false
	}
	switch pending.Kind {
	case "", gatewayV2PendingRouteSwitch:
		if pending.Disable != nil || pending.ActivationUncertain || validateRoute(pending.Proposed.Route) != nil {
			return false
		}
		if pending.Previous == nil {
			return pending.Proposed.LAN == nil
		}
		return pending.Previous.Route.Slot != pending.Proposed.Route.Slot &&
			reflect.DeepEqual(pending.Previous.LAN, pending.Proposed.LAN)
	case gatewayV2PendingLANGrant:
		if pending.Disable != nil || pending.Previous == nil || pending.Previous.LAN != nil ||
			pending.Proposed.LAN == nil || pending.Proposed.LAN.Transfer != nil ||
			!reflect.DeepEqual(pending.Previous.Route, pending.Proposed.Route) {
			return false
		}
		ports, allocations, attempts := gatewayCurrentBindingSets(state)
		return ports != nil && validGatewayCurrentRawBinding(pending.AppID, state.Profile,
			*pending.Proposed.LAN, ports, allocations, attempts)
	case gatewayV2PendingLANWithdrawal:
		return pending.Disable == nil && pending.ActivationUncertain && pending.Previous != nil &&
			pending.Previous.LAN != nil && pending.Proposed.LAN == nil &&
			reflect.DeepEqual(pending.Previous.Route, pending.Proposed.Route)
	case gatewayV2PendingLANDisable:
		return validGatewayCurrentLANDisablePending(state, *pending)
	default:
		return false
	}
}

func gatewayCurrentBindingSets(state gatewayCurrentRouteState) (map[uint16]string, map[string]struct{}, map[string]struct{}) {
	ports := make(map[uint16]string)
	allocations := make(map[string]struct{})
	attempts := make(map[string]struct{})
	for appID, app := range state.Apps {
		if app.LAN != nil && !validGatewayCurrentRawBinding(appID, state.Profile, *app.LAN, ports, allocations, attempts) {
			return nil, nil, nil
		}
	}
	return ports, allocations, attempts
}

func validGatewayCurrentLANDisablePending(state gatewayCurrentRouteState, pending gatewayCurrentPendingRoute) bool {
	if !pending.ActivationUncertain || pending.Disable == nil || !validGatewayV2LANDisableRequest(*pending.Disable) ||
		pending.Disable.AppID != pending.AppID || pending.Previous == nil || pending.Previous.LAN == nil ||
		!reflect.DeepEqual(pending.Previous.Route, pending.Proposed.Route) || pending.Proposed.LAN != nil ||
		pending.Disable.Port != pending.Previous.LAN.Raw.Port || pending.Disable.Port < state.Profile.PortStart ||
		pending.Disable.Port > state.Profile.PortEnd || pending.Disable.SourceGrant == nil {
		return false
	}
	raw, err := gatewayV2LANBindingForRequest(*pending.Disable.SourceGrant)
	if err != nil || !reflect.DeepEqual(raw, pending.Previous.LAN.Raw) ||
		pending.Disable.GatewayProfileRevisionID != raw.ProfileRevisionID ||
		pending.Disable.GatewayProfileRevisionNumber != raw.ProfileRevisionNumber ||
		pending.Disable.GatewayProfileSpecDigest != raw.ProfileSpecDigest {
		return false
	}
	if pending.Previous.LAN.Transfer == nil {
		return raw.ProfileRevisionID == state.Profile.RevisionID &&
			raw.ProfileRevisionNumber == state.Profile.RevisionNumber && raw.ProfileSpecDigest == state.Profile.SpecDigest
	}
	transfer := pending.Previous.LAN.Transfer
	return transfer.SuccessorProfileRevisionID == state.Profile.RevisionID &&
		transfer.SuccessorProfileRevisionNumber == state.Profile.RevisionNumber &&
		transfer.SuccessorProfileSpecDigest == state.Profile.SpecDigest
}

func validGatewayCurrentLANRecoveryBatch(state gatewayCurrentRouteState) bool {
	batch := state.LANRecovery
	if batch == nil || state.Pending != nil || len(batch.Items) == 0 || len(batch.Items) > maxStateApps ||
		batch.Head < 0 || batch.Head > len(batch.Items) {
		return false
	}
	operations := make(map[string]struct{}, len(batch.Items))
	apps := make(map[string]struct{}, len(batch.Items))
	for index, item := range batch.Items {
		operationID, ok := validGatewayCurrentLANRecoveryItem(state, item)
		if !ok {
			return false
		}
		if _, duplicate := operations[operationID]; duplicate {
			return false
		}
		if _, duplicate := apps[item.AppID]; duplicate {
			return false
		}
		operations[operationID], apps[item.AppID] = struct{}{}, struct{}{}
		if index > 0 && !gatewayCurrentLANRecoveryItemLess(batch.Items[index-1], item) {
			return false
		}
		if index < batch.Head && state.Apps[item.AppID].LAN != nil {
			return false
		}
	}
	if batch.LegacyPending == nil {
		return true
	}
	legacy := cloneGatewayCurrentRouteState(state)
	legacy.LANRecovery = nil
	legacy.Pending = cloneGatewayCurrentPendingRoute(batch.LegacyPending)
	if batch.LegacyPending.Previous == nil {
		delete(legacy.Apps, batch.LegacyPending.AppID)
	} else {
		legacy.Apps[batch.LegacyPending.AppID] = cloneGatewayCurrentAppRoute(*batch.LegacyPending.Previous)
	}
	if !validGatewayCurrentPendingRoute(legacy) {
		return false
	}
	for _, item := range batch.Items {
		if gatewayCurrentPendingMatchesRecovery(*batch.LegacyPending, item) {
			return true
		}
	}
	return false
}

func validGatewayCurrentLANRecoveryItem(state gatewayCurrentRouteState,
	item gatewayCurrentLANRecoveryItem,
) (string, bool) {
	app, exists := state.Apps[item.AppID]
	if !validAppID(item.AppID) || !exists {
		return "", false
	}
	switch item.Kind {
	case gatewayV2PendingLANGrant:
		if item.Grant == nil || item.Disable != nil || item.Grant.Transfer != nil {
			return "", false
		}
		if app.LAN != nil {
			if !reflect.DeepEqual(*app.LAN, *item.Grant) {
				return "", false
			}
			return item.Grant.Raw.GrantAttemptID, true
		}
		ports, allocations, attempts := gatewayCurrentBindingSets(state)
		if ports == nil || !validGatewayCurrentRawBinding(item.AppID, state.Profile, *item.Grant,
			ports, allocations, attempts) {
			return "", false
		}
		return item.Grant.Raw.GrantAttemptID, true
	case gatewayV2PendingLANDisable:
		if item.Grant != nil || item.Disable == nil || !validGatewayV2LANDisableRequest(*item.Disable) ||
			item.Disable.AppID != item.AppID || item.Disable.SourceGrant == nil {
			return "", false
		}
		raw, err := gatewayV2LANBindingForRequest(*item.Disable.SourceGrant)
		if err != nil || (app.LAN != nil && !reflect.DeepEqual(app.LAN.Raw, raw)) {
			return "", false
		}
		return item.Disable.OperationID, true
	default:
		return "", false
	}
}

func gatewayCurrentLANRecoveryItemLess(left, right gatewayCurrentLANRecoveryItem) bool {
	leftID, _ := validGatewayCurrentLANRecoveryItemIdentity(left)
	rightID, _ := validGatewayCurrentLANRecoveryItemIdentity(right)
	if left.AppID != right.AppID {
		return left.AppID < right.AppID
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return leftID < rightID
}

func validGatewayCurrentLANRecoveryItemIdentity(item gatewayCurrentLANRecoveryItem) (string, bool) {
	if item.Kind == gatewayV2PendingLANGrant && item.Grant != nil {
		return item.Grant.Raw.GrantAttemptID, true
	}
	if item.Kind == gatewayV2PendingLANDisable && item.Disable != nil {
		return item.Disable.OperationID, true
	}
	return "", false
}

func gatewayCurrentPendingMatchesRecovery(pending gatewayCurrentPendingRoute,
	item gatewayCurrentLANRecoveryItem,
) bool {
	if pending.AppID != item.AppID {
		return false
	}
	if item.Kind == gatewayV2PendingLANGrant && item.Grant != nil {
		if pending.Kind == gatewayV2PendingLANGrant && pending.Proposed.LAN != nil {
			return reflect.DeepEqual(*pending.Proposed.LAN, *item.Grant)
		}
		return pending.Kind == gatewayV2PendingLANWithdrawal && pending.Previous != nil &&
			pending.Previous.LAN != nil && reflect.DeepEqual(*pending.Previous.LAN, *item.Grant)
	}
	return item.Kind == gatewayV2PendingLANDisable && item.Disable != nil && pending.Disable != nil &&
		reflect.DeepEqual(*pending.Disable, *item.Disable)
}

func gatewayCurrentSortedRecoveryItems(values []gatewayCurrentLANRecoveryItem) []gatewayCurrentLANRecoveryItem {
	result := append([]gatewayCurrentLANRecoveryItem(nil), values...)
	sort.Slice(result, func(i, j int) bool { return gatewayCurrentLANRecoveryItemLess(result[i], result[j]) })
	return result
}
