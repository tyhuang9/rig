package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayRebindAdmissionPhysicalProofVersion = 1

type gatewayRebindAdmissionProofTarget struct {
	Spec                       appaccess.GatewayRebindSpecV2
	SuccessorProfileSpecDigest string
	Checkpoint                 gatewayRebindPredecessorCheckpoint
}

type gatewayRebindAdmissionUpgradeProof struct {
	Source           routeState
	State            gatewayV2RouteState
	Journal          gatewayMigrationJournal
	Docker           gatewayV2DockerObservation
	DockerDigest     string
	FinalContainerID string
	IngressNetworkID string
}

type gatewayRebindAdmissionCurrentProof struct {
	State     gatewayCurrentRouteState
	Target    gatewayCurrentPhysicalTarget
	Physical  gatewayRebindCurrentPredecessorPhysical
	Inventory gatewayCurrentPhysicalInventory
}

type gatewayRebindAdmissionPredecessorProof struct {
	Upgrade *gatewayRebindAdmissionUpgradeProof
	Current *gatewayRebindAdmissionCurrentProof
}

type gatewayRebindAdmissionSuccessorTarget struct {
	OperationID        string
	ProfileOperationID string
	Profile            gatewayProfileBinding
}

type gatewayRebindAdmissionSuccessorProof struct {
	Target  gatewayRebindAdmissionSuccessorTarget
	Network gatewayRebindSuccessorNetworkObservation
}

type gatewayRebindAdmissionPhysicalProof struct {
	Version     int
	Selection   gatewayCurrentSelection
	Checkpoint  gatewayRebindPredecessorCheckpoint
	Predecessor gatewayRebindAdmissionPredecessorProof
	Successor   gatewayRebindAdmissionSuccessorProof
}

type gatewayRebindAdmissionProofReader func(context.Context, gatewayRebindAdmissionProofTarget,
	gatewayCurrentSelection) (gatewayRebindAdmissionPhysicalProof, error)

func gatewayRebindAdmissionTarget(inspection GatewayRebindProposalInspection,
	checkpoint gatewayRebindPredecessorCheckpoint,
) gatewayRebindAdmissionProofTarget {
	return gatewayRebindAdmissionProofTarget{Spec: inspection.Spec,
		SuccessorProfileSpecDigest: inspection.SuccessorProfileSpecDigest, Checkpoint: checkpoint}
}

func gatewayRebindAdmissionTargetForClaim(claim appaccess.GatewayRebindClaimV2,
	checkpoint gatewayRebindPredecessorCheckpoint,
) gatewayRebindAdmissionProofTarget {
	return gatewayRebindAdmissionProofTarget{Spec: claim.Spec,
		SuccessorProfileSpecDigest: claim.ConfigureApproval.SpecDigest, Checkpoint: checkpoint}
}

func validGatewayRebindAdmissionProofTarget(value gatewayRebindAdmissionProofTarget) bool {
	profile := gatewayProfileBinding{
		RevisionID: value.Spec.SuccessorProfileRevisionID, RevisionNumber: value.Spec.SuccessorProfileRevisionNumber,
		SpecDigest: value.SuccessorProfileSpecDigest, SelectedIPv4: value.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: value.Spec.SuccessorProfile.InterfaceID, PortStart: value.Spec.SuccessorProfile.PortStart,
		PortEnd: value.Spec.SuccessorProfile.PortEnd,
	}
	return validCanonicalUUID(value.Spec.OperationID) &&
		validCanonicalUUID(value.Spec.SuccessorProfileOperationID) &&
		value.Spec.SuccessorProfileOperationID != value.Spec.OperationID && validGatewayProfileBinding(profile) &&
		validGatewayRebindPredecessorCheckpoint(value.Checkpoint) &&
		value.Checkpoint.OperationID == value.Spec.OperationID &&
		value.Checkpoint.Generation == value.Spec.SuccessorProtectedGeneration &&
		value.Checkpoint.Digest == value.Spec.Predecessor.PredecessorCheckpointDigest &&
		value.Checkpoint.sourceRef() == value.Spec.Predecessor
}

func (m *Manager) readGatewayRebindAdmissionPhysicalLocked(ctx context.Context,
	target gatewayRebindAdmissionProofTarget, selection gatewayCurrentSelection,
) (gatewayRebindAdmissionPhysicalProof, error) {
	inventory := managerGatewayV2DockerInventory{manager: m}
	reads := gatewayRebindSuccessorPreflightReads{network: gatewayV2ProductionNetworkPlanReads(m),
		dockerIDs: inventory.listNetworkIDs}
	return m.readGatewayRebindAdmissionPhysicalWithReadsLocked(ctx, target, selection,
		m.inspectGatewayRebindDocker, reads)
}

func (m *Manager) readGatewayRebindAdmissionPhysicalWithReadsLocked(ctx context.Context,
	target gatewayRebindAdmissionProofTarget, selection gatewayCurrentSelection,
	inspectDocker gatewayRebindDockerInspector,
	reads gatewayRebindSuccessorPreflightReads,
) (gatewayRebindAdmissionPhysicalProof, error) {
	invalid := func() (gatewayRebindAdmissionPhysicalProof, error) {
		return gatewayRebindAdmissionPhysicalProof{}, gatewayRebindProposalError(ctx)
	}
	if m == nil || ctx == nil || inspectDocker == nil || ctx.Err() != nil ||
		!validGatewayRebindAdmissionProofTarget(target) {
		return invalid()
	}
	actualCheckpoint, err := gatewayRebindCheckpointForSelection(target.Checkpoint.Generation,
		target.Checkpoint.OperationID, selection)
	if err != nil || !reflect.DeepEqual(actualCheckpoint, target.Checkpoint) {
		return invalid()
	}

	predecessor, err := m.readGatewayRebindAdmissionPredecessorWithInspectLocked(ctx, selection, inspectDocker)
	if err != nil {
		return invalid()
	}
	profile := gatewayProfileBinding{
		RevisionID: target.Spec.SuccessorProfileRevisionID, RevisionNumber: target.Spec.SuccessorProfileRevisionNumber,
		SpecDigest: target.SuccessorProfileSpecDigest, SelectedIPv4: target.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: target.Spec.SuccessorProfile.InterfaceID, PortStart: target.Spec.SuccessorProfile.PortStart,
		PortEnd: target.Spec.SuccessorProfile.PortEnd,
	}
	successorTarget := gatewayRebindAdmissionSuccessorTarget{OperationID: target.Spec.OperationID,
		ProfileOperationID: target.Spec.SuccessorProfileOperationID, Profile: profile}
	firstSuccessor, err := readGatewayRebindAdmissionSuccessor(ctx, successorTarget, reads)
	if err != nil {
		return invalid()
	}
	secondSuccessor, err := readGatewayRebindAdmissionSuccessor(ctx, successorTarget, reads)
	if err != nil || !reflect.DeepEqual(firstSuccessor, secondSuccessor) || ctx.Err() != nil {
		return invalid()
	}
	return gatewayRebindAdmissionPhysicalProof{Version: gatewayRebindAdmissionPhysicalProofVersion,
		Selection: selection, Checkpoint: target.Checkpoint, Predecessor: predecessor,
		Successor: secondSuccessor}, nil
}

func (m *Manager) readGatewayRebindAdmissionPredecessorLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayRebindAdmissionPredecessorProof, error) {
	return m.readGatewayRebindAdmissionPredecessorWithInspectLocked(ctx, selection, m.inspectGatewayRebindDocker)
}

func (m *Manager) readGatewayRebindAdmissionPredecessorWithInspectLocked(ctx context.Context,
	selection gatewayCurrentSelection, inspectDocker gatewayRebindDockerInspector,
) (gatewayRebindAdmissionPredecessorProof, error) {
	handover := newGatewayRebindTypedHandoverRuntime(m)
	physical := managerGatewayCurrentPhysicalRuntime{manager: m,
		hostProbe: handover.hostProbe, containerProbe: handover.containerProbe}
	return m.readGatewayRebindAdmissionPredecessorWithRuntimeLocked(ctx, selection, inspectDocker,
		handover, physical)
}

func (m *Manager) readGatewayRebindAdmissionPredecessorWithRuntimeLocked(ctx context.Context,
	selection gatewayCurrentSelection, inspectDocker gatewayRebindDockerInspector,
	handover gatewayRebindTypedHandoverRuntime, physical managerGatewayCurrentPhysicalRuntime,
) (gatewayRebindAdmissionPredecessorProof, error) {
	invalid := func() (gatewayRebindAdmissionPredecessorProof, error) {
		return gatewayRebindAdmissionPredecessorProof{}, gatewayRebindProposalError(ctx)
	}
	if m == nil || ctx == nil || inspectDocker == nil || ctx.Err() != nil ||
		handover.manager != m || physical.manager != m || handover.hostProbe == nil ||
		handover.containerProbe == nil || physical.hostProbe == nil || physical.containerProbe == nil {
		return invalid()
	}
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || selection.UpgradeSource == nil {
			return invalid()
		}
		state, source, journal := selection.Upgrade.State, *selection.UpgradeSource, selection.Upgrade.Journal
		first, err := inspectDocker(ctx, source, state, journal)
		if err != nil || !validGatewayRebindPredecessorDocker(source, state, journal, first) {
			clearGatewayV2DockerObservation(&first)
			return invalid()
		}
		defer clearGatewayV2DockerObservation(&first)
		second, err := inspectDocker(ctx, source, state, journal)
		if err != nil || !validGatewayRebindPredecessorDocker(source, state, journal, second) ||
			!sameGatewayRebindPredecessorDockerObservation(first, second, state.Identity) || ctx.Err() != nil {
			clearGatewayV2DockerObservation(&second)
			return invalid()
		}
		digest, err := gatewayRebindPredecessorDockerDigest(second, state.Identity)
		if err != nil || !validSHA256(digest) {
			clearGatewayV2DockerObservation(&second)
			return invalid()
		}
		second.FinalContainer.Mounts = canonicalGatewayRebindTypedMounts(second.FinalContainer.Mounts)
		return gatewayRebindAdmissionPredecessorProof{Upgrade: &gatewayRebindAdmissionUpgradeProof{
			Source: source, State: state, Journal: journal, Docker: second, DockerDigest: digest,
			FinalContainerID: second.FinalContainer.ID, IngressNetworkID: second.IngressNetworkID,
		}}, nil
	case gatewayCurrentSelectionRebind:
		if selection.State == nil {
			return invalid()
		}
		target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
		if err != nil {
			return invalid()
		}
		first, err := handover.observeCurrentPredecessor(ctx, target)
		if err != nil {
			return invalid()
		}
		second, err := handover.observeCurrentPredecessor(ctx, target)
		if err != nil || !reflect.DeepEqual(first, second) || !validSHA256(second.Digest) || ctx.Err() != nil {
			return invalid()
		}
		inventory, err := physical.read(ctx, target)
		if err != nil || !gatewayRebindAdmissionCurrentPhysicalMatches(target, second, inventory) {
			clearGatewayCurrentPhysicalInventory(&inventory)
			return invalid()
		}
		return gatewayRebindAdmissionPredecessorProof{Current: &gatewayRebindAdmissionCurrentProof{
			State: cloneGatewayCurrentRouteState(*selection.State), Target: target,
			Physical: second, Inventory: inventory,
		}}, nil
	default:
		return invalid()
	}
}

func readGatewayRebindAdmissionSuccessor(ctx context.Context, target gatewayRebindAdmissionSuccessorTarget,
	reads gatewayRebindSuccessorPreflightReads,
) (gatewayRebindAdmissionSuccessorProof, error) {
	invalid := func() (gatewayRebindAdmissionSuccessorProof, error) {
		return gatewayRebindAdmissionSuccessorProof{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	raw, err := readGatewayRebindSuccessorPhysical(ctx, target, reads)
	if err != nil {
		return invalid()
	}
	network, err := newGatewayRebindSuccessorNetworkObservation(raw)
	if err != nil {
		return invalid()
	}
	return gatewayRebindAdmissionSuccessorProof{Target: target, Network: network}, nil
}

func readGatewayRebindSuccessorPhysical(ctx context.Context, target gatewayRebindAdmissionSuccessorTarget,
	reads gatewayRebindSuccessorPreflightReads,
) (gatewayRebindSuccessorPreflightObservation, error) {
	invalid := func() (gatewayRebindSuccessorPreflightObservation, error) {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	if ctx == nil || ctx.Err() != nil || !validCanonicalUUID(target.OperationID) ||
		!validCanonicalUUID(target.ProfileOperationID) || target.OperationID == target.ProfileOperationID ||
		!validGatewayProfileBinding(target.Profile) || reads.network.candidates == nil || reads.network.host == nil ||
		reads.network.docker == nil || reads.dockerIDs == nil {
		return invalid()
	}
	candidates, err := reads.network.candidates()
	if err != nil {
		return invalid()
	}
	host, err := reads.network.host()
	if err != nil {
		return invalid()
	}
	dockerIDsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsBefore) {
		return invalid()
	}
	docker, err := reads.network.docker(ctx)
	if err != nil || ctx.Err() != nil {
		return invalid()
	}
	dockerIDsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsAfter) ||
		!equalStrings(dockerIDsBefore, dockerIDsAfter) {
		return invalid()
	}
	captured := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return append([]hostNetworkCandidate(nil), candidates...), nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{Routes: append([]netip.Prefix(nil), host.Routes...),
				Interfaces: append([]netip.Prefix(nil), host.Interfaces...)}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			return append([]netip.Prefix(nil), docker...), nil
		},
	}
	plan, err := selectGatewayV2NetworkPlan(ctx, target.Profile, captured)
	if err != nil {
		return invalid()
	}
	return gatewayRebindSuccessorPreflightObservation{
		candidates: append([]hostNetworkCandidate(nil), candidates...),
		host: gatewayV2HostNetworkSnapshot{Routes: append([]netip.Prefix(nil), host.Routes...),
			Interfaces: append([]netip.Prefix(nil), host.Interfaces...)},
		dockerIDs: append([]string(nil), dockerIDsBefore...), docker: append([]netip.Prefix(nil), docker...),
		result: GatewayRebindSuccessorPreflight{RebindOperationID: target.OperationID,
			Profile: GatewayRebindSuccessorProfile{RevisionID: target.Profile.RevisionID,
				RevisionNumber: target.Profile.RevisionNumber, OperationID: target.ProfileOperationID,
				SpecDigest: target.Profile.SpecDigest, SelectedIPv4: target.Profile.SelectedIPv4,
				InterfaceID: target.Profile.InterfaceID, PortStart: target.Profile.PortStart,
				PortEnd: target.Profile.PortEnd},
			Network: GatewayRebindSuccessorNetworkPlan{Subnet: plan.Subnet,
				GatewayIPv4: plan.GatewayIPv4, ContainerIPv4: plan.ContainerIPv4}},
	}, nil
}

func sameGatewayRebindAdmissionPhysicalProof(left, right gatewayRebindAdmissionPhysicalProof) bool {
	return left.Version == gatewayRebindAdmissionPhysicalProofVersion &&
		right.Version == gatewayRebindAdmissionPhysicalProofVersion &&
		sameGatewayCurrentSelection(left.Selection, right.Selection) &&
		reflect.DeepEqual(left.Checkpoint, right.Checkpoint) &&
		reflect.DeepEqual(left.Predecessor, right.Predecessor) &&
		reflect.DeepEqual(left.Successor, right.Successor)
}

func gatewayRebindAdmissionProofMatchesTarget(ctx context.Context, proof gatewayRebindAdmissionPhysicalProof,
	target gatewayRebindAdmissionProofTarget, selection gatewayCurrentSelection,
) bool {
	expectedProfile := gatewayProfileBinding{
		RevisionID: target.Spec.SuccessorProfileRevisionID, RevisionNumber: target.Spec.SuccessorProfileRevisionNumber,
		SpecDigest: target.SuccessorProfileSpecDigest, SelectedIPv4: target.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: target.Spec.SuccessorProfile.InterfaceID, PortStart: target.Spec.SuccessorProfile.PortStart,
		PortEnd: target.Spec.SuccessorProfile.PortEnd,
	}
	if proof.Version != gatewayRebindAdmissionPhysicalProofVersion ||
		!sameGatewayCurrentSelection(proof.Selection, selection) ||
		!reflect.DeepEqual(proof.Checkpoint, target.Checkpoint) ||
		proof.Successor.Target.OperationID != target.Spec.OperationID ||
		proof.Successor.Target.ProfileOperationID != target.Spec.SuccessorProfileOperationID ||
		!reflect.DeepEqual(proof.Successor.Target.Profile, expectedProfile) ||
		proof.Successor.Network.OperationID != target.Spec.OperationID ||
		proof.Successor.Network.ClaimRequestDigest != "" ||
		proof.Successor.Network.ProfileSpecDigest != target.SuccessorProfileSpecDigest ||
		!validGatewayRebindAdmissionSuccessorProof(ctx, proof.Successor) {
		return false
	}
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || selection.UpgradeSource == nil ||
			proof.Predecessor.Upgrade == nil || proof.Predecessor.Current != nil {
			return false
		}
		upgrade := proof.Predecessor.Upgrade
		digest, err := gatewayRebindPredecessorDockerDigest(upgrade.Docker, selection.Upgrade.State.Identity)
		return reflect.DeepEqual(upgrade.Source, *selection.UpgradeSource) &&
			reflect.DeepEqual(upgrade.State, selection.Upgrade.State) &&
			reflect.DeepEqual(upgrade.Journal, selection.Upgrade.Journal) &&
			validGatewayRebindPredecessorDocker(upgrade.Source, upgrade.State, upgrade.Journal, upgrade.Docker) &&
			err == nil && digest == upgrade.DockerDigest && validSHA256(digest) &&
			upgrade.FinalContainerID == upgrade.Docker.FinalContainer.ID &&
			upgrade.IngressNetworkID == upgrade.Docker.IngressNetworkID &&
			validContainerID(upgrade.FinalContainerID) && validContainerID(upgrade.IngressNetworkID)
	case gatewayCurrentSelectionRebind:
		if selection.State == nil || proof.Predecessor.Upgrade != nil || proof.Predecessor.Current == nil {
			return false
		}
		current := proof.Predecessor.Current
		derived, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
		return err == nil && reflect.DeepEqual(current.State, *selection.State) &&
			reflect.DeepEqual(current.Target, derived) &&
			gatewayRebindAdmissionCurrentPhysicalMatches(derived, current.Physical, current.Inventory)
	default:
		return false
	}
}

func validGatewayRebindAdmissionSuccessorProof(ctx context.Context,
	proof gatewayRebindAdmissionSuccessorProof,
) bool {
	if ctx == nil || ctx.Err() != nil || !validGatewayProfileBinding(proof.Target.Profile) ||
		proof.Network.Version != gatewayRebindProtectedIntentVersion ||
		proof.Network.OperationID != proof.Target.OperationID ||
		proof.Network.ProfileSpecDigest != proof.Target.Profile.SpecDigest ||
		!validGatewayRebindSuccessorDockerIDs(proof.Network.DockerNetworkIDs) {
		return false
	}
	candidates := make([]hostNetworkCandidate, 0, len(proof.Network.Candidates))
	for index, value := range proof.Network.Candidates {
		address, addressErr := netip.ParseAddr(value.IPv4)
		prefix, prefixErr := netip.ParsePrefix(value.Prefix)
		if value.InterfaceID == "" || addressErr != nil || !address.Is4() || prefixErr != nil ||
			!prefix.Addr().Is4() || prefix != prefix.Masked() || !prefix.Contains(address) ||
			(index > 0 && !gatewayRebindAdmissionCandidateLess(proof.Network.Candidates[index-1], value)) {
			return false
		}
		candidates = append(candidates, hostNetworkCandidate{InterfaceID: value.InterfaceID,
			IPv4: value.IPv4, Prefix: prefix})
	}
	hostRoutes, ok := parseGatewayRebindPrefixes(proof.Network.HostRoutes)
	if !ok {
		return false
	}
	hostInterfaces, ok := parseGatewayRebindPrefixes(proof.Network.HostInterfaces)
	if !ok {
		return false
	}
	docker, ok := parseGatewayRebindPrefixes(proof.Network.DockerPrefixes)
	if !ok {
		return false
	}
	plan, err := selectGatewayV2NetworkPlan(ctx, proof.Target.Profile, gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return candidates, nil },
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{Routes: hostRoutes, Interfaces: hostInterfaces}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) { return docker, nil },
	})
	return err == nil && reflect.DeepEqual(proof.Network.Plan, gatewayRebindSuccessorIntentNetwork{
		Subnet: plan.Subnet, GatewayIPv4: plan.GatewayIPv4, ContainerIPv4: plan.ContainerIPv4})
}

func gatewayRebindAdmissionCandidateLess(left, right gatewayRebindSuccessorNetworkCandidate) bool {
	if left.InterfaceID != right.InterfaceID {
		return left.InterfaceID < right.InterfaceID
	}
	if left.IPv4 != right.IPv4 {
		return left.IPv4 < right.IPv4
	}
	return left.Prefix < right.Prefix
}

func gatewayRebindAdmissionCurrentPhysicalMatches(target gatewayCurrentPhysicalTarget,
	physical gatewayRebindCurrentPredecessorPhysical, inventory gatewayCurrentPhysicalInventory,
) bool {
	if !validGatewayCurrentPhysicalTarget(target) || target.Pending != nil || target.LANRecovery != nil ||
		!validSHA256(physical.Digest) {
		return false
	}
	if physical.Running {
		return physical.Serving != nil && inventory.Final.Running && !inventory.Final.Restarting &&
			physical.Serving.Runtime.ImageID == target.Resources.ImageID &&
			normalizeID(inventory.Image.ID) == target.Resources.ImageID &&
			validGatewayPinnedImage(inventory.Image, true) &&
			validGatewayCurrentPhysicalAttestation(*physical.Serving) &&
			physical.Serving.Outcome == gatewayCurrentPhysicalStableServing &&
			reflect.DeepEqual(physical.Serving.State, target.State) &&
			physical.Serving.Lineage == target.Lineage && physical.Serving.Digest == physical.Digest &&
			physical.Serving.Resources.FinalContainer.ID == inventory.Final.ID &&
			physical.Serving.Runtime.ContainerID == inventory.Final.ID
	}
	if physical.Serving != nil || inventory.Final.Running || inventory.Final.Restarting ||
		gatewayV2HasEffectivePortBinding(inventory.FinalRuntime.EffectivePortBindings) {
		return false
	}
	expected, err := gatewayCurrentPhysicalConfigBytes(target.State)
	if err != nil {
		return false
	}
	defer clear(expected)
	digest, err := canonicalDigest(struct {
		Purpose   string
		Target    gatewayCurrentPhysicalTarget
		Inventory gatewayCurrentPhysicalInventory
	}{"hostd/generated-ingress/rebind/stopped-current-predecessor/v1", target, inventory})
	return err == nil && sameCaddyConfig(expected, inventory.RestartConfig) && digest == physical.Digest
}

func (m *Manager) attestGatewayRebindClaimAdmissionLocked(ctx context.Context,
	repository gatewayRebindAdmissionRepository, candidate gatewayRebindAdmissionCandidate,
	claim appaccess.GatewayRebindClaimV2, proofReader gatewayRebindAdmissionProofReader,
	expected gatewayRebindAdmissionPhysicalProof,
) (gatewayRebindSuccessorNetworkObservation, error) {
	invalid := func() (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, gatewayRebindProposalError(ctx)
	}
	if m == nil || ctx == nil || repository == nil || proofReader == nil || ctx.Err() != nil {
		return invalid()
	}
	before, heads, err := readGatewayRebindPreparedClaimCensus(ctx, repository)
	if err != nil || !m.gatewayRebindPreparedClaimCensusMatchesLocked(ctx, before, heads,
		candidate, claim) {
		return invalid()
	}
	target := gatewayRebindAdmissionTargetForClaim(claim, candidate.Checkpoint)
	observed, err := proofReader(ctx, target, candidate.Selection)
	if err != nil || !gatewayRebindAdmissionProofMatchesTarget(ctx, observed, target, candidate.Selection) ||
		!sameGatewayRebindAdmissionPhysicalProof(expected, observed) {
		return invalid()
	}
	after, afterHeads, err := readGatewayRebindPreparedClaimCensus(ctx, repository)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(before, after) ||
		!sameGatewayRebindRuntimeHeads(heads, afterHeads) ||
		!m.gatewayRebindPreparedClaimCensusMatchesLocked(ctx, after, afterHeads, candidate, claim) {
		return invalid()
	}
	network, err := gatewayRebindClaimBoundNetwork(claim, observed)
	if err != nil {
		return invalid()
	}
	return network, nil
}

func (m *Manager) attestGatewayRebindPreparedA0Locked(ctx context.Context,
	repository gatewayRebindProposalRepository, supplied appaccess.GatewayRebindRecoverySnapshot,
	claim appaccess.GatewayRebindClaimV2, selection gatewayCurrentSelection,
	checkpoint gatewayRebindPredecessorCheckpoint, proofReader gatewayRebindAdmissionProofReader,
) (gatewayRebindSuccessorNetworkObservation, error) {
	invalid := func() (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, gatewayRebindProposalError(ctx)
	}
	if m == nil || ctx == nil || repository == nil || proofReader == nil || supplied.Active == nil || ctx.Err() != nil {
		return invalid()
	}
	candidate := gatewayRebindAdmissionCandidate{Snapshot: supplied, Selection: selection,
		Checkpoint: checkpoint, Proposal: appaccess.GatewayRebindPreclaimProposalV2{Spec: claim.Spec,
			RebindApproval: claim.RebindApproval, ConfigureApproval: claim.ConfigureApproval,
			Roster: supplied.Active.RosterV2, RuntimeHeads: supplied.Active.RuntimeHeads}}
	before, heads, err := readGatewayRebindPreparedClaimCensus(ctx, repository)
	if err != nil || !reflect.DeepEqual(supplied, before) ||
		!m.gatewayRebindPreparedClaimCensusMatchesLocked(ctx, before, heads, candidate, claim) {
		return invalid()
	}
	target := gatewayRebindAdmissionTargetForClaim(claim, checkpoint)
	first, err := proofReader(ctx, target, selection)
	if err != nil || !gatewayRebindAdmissionProofMatchesTarget(ctx, first, target, selection) {
		return invalid()
	}
	middle, middleHeads, err := readGatewayRebindPreparedClaimCensus(ctx, repository)
	if err != nil || !reflect.DeepEqual(before, middle) || !sameGatewayRebindRuntimeHeads(heads, middleHeads) ||
		!m.gatewayRebindPreparedClaimCensusMatchesLocked(ctx, middle, middleHeads, candidate, claim) {
		return invalid()
	}
	second, err := proofReader(ctx, target, selection)
	if err != nil || !gatewayRebindAdmissionProofMatchesTarget(ctx, second, target, selection) ||
		!sameGatewayRebindAdmissionPhysicalProof(first, second) {
		return invalid()
	}
	after, afterHeads, err := readGatewayRebindPreparedClaimCensus(ctx, repository)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(before, after) ||
		!sameGatewayRebindRuntimeHeads(heads, afterHeads) ||
		!m.gatewayRebindPreparedClaimCensusMatchesLocked(ctx, after, afterHeads, candidate, claim) {
		return invalid()
	}
	network, err := gatewayRebindClaimBoundNetwork(claim, second)
	if err != nil {
		return invalid()
	}
	return network, nil
}

func readGatewayRebindPreparedClaimCensus(ctx context.Context,
	repository gatewayRebindProposalRepository,
) (appaccess.GatewayRebindRecoverySnapshot, []appaccess.GatewayRebindRuntimeHead, error) {
	if ctx == nil || repository == nil || ctx.Err() != nil {
		return appaccess.GatewayRebindRecoverySnapshot{}, nil, errors.New("invalid generated ingress rebind admission census")
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return appaccess.GatewayRebindRecoverySnapshot{}, nil, err
	}
	heads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || ctx.Err() != nil {
		return appaccess.GatewayRebindRecoverySnapshot{}, nil, errors.New("invalid generated ingress rebind admission census")
	}
	return snapshot, heads, nil
}

func (m *Manager) gatewayRebindPreparedClaimCensusMatchesLocked(ctx context.Context,
	snapshot appaccess.GatewayRebindRecoverySnapshot, heads []appaccess.GatewayRebindRuntimeHead,
	candidate gatewayRebindAdmissionCandidate, claim appaccess.GatewayRebindClaimV2,
) bool {
	if snapshot.Active == nil || snapshot.Active.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		snapshot.Active.Claim.V2 == nil || !reflect.DeepEqual(*snapshot.Active.Claim.V2, claim) ||
		snapshot.Phase != appaccess.GatewayRebindPrepared || !snapshot.RollbackAllowed ||
		snapshot.DatabaseCommitObserved || snapshot.DatabaseCommittedEvent != nil ||
		claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		len(snapshot.Active.RosterV1) != 0 || len(snapshot.Active.Events) != 1 ||
		!gatewayRebindPreparedInitialEventMatches(snapshot.Active.Events[0], claim) ||
		len(snapshot.Active.Commands) != 0 || len(snapshot.Active.Transfers) != 0 ||
		!sameGatewayRebindRosterV2(snapshot.Active.RosterV2, candidate.Proposal.Roster) ||
		!sameGatewayRebindRuntimeHeads(snapshot.Active.RuntimeHeads, candidate.Proposal.RuntimeHeads) ||
		!sameGatewayRebindRuntimeHeads(heads, candidate.Proposal.RuntimeHeads) ||
		!reflect.DeepEqual(snapshot.CurrentProfile, candidate.Snapshot.CurrentProfile) ||
		!reflect.DeepEqual(snapshot.CurrentSource, candidate.Snapshot.CurrentSource) ||
		!reflect.DeepEqual(snapshot.CurrentDatabaseCommittedEvent, candidate.Snapshot.CurrentDatabaseCommittedEvent) ||
		!reflect.DeepEqual(snapshot.CurrentTransfers, candidate.Snapshot.CurrentTransfers) {
		return false
	}
	activeMatches := 0
	for index := range snapshot.History {
		entry := snapshot.History[index]
		if entry.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && entry.Claim.V2 != nil &&
			entry.Claim.V2.Spec.OperationID == claim.Spec.OperationID {
			activeMatches++
			if !reflect.DeepEqual(entry, *snapshot.Active) {
				return false
			}
		}
	}
	if activeMatches != 1 {
		return false
	}
	selected, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	return err == nil && sameGatewayCurrentSelection(selected, candidate.Selection)
}

func gatewayRebindPreparedInitialEventMatches(event appaccess.GatewayRebindEvent,
	claim appaccess.GatewayRebindClaimV2,
) bool {
	return event.OperationID == claim.Spec.OperationID && event.Sequence == 1 &&
		event.State == appaccess.GatewayRebindPrepared && event.CreatedAt.Equal(claim.CreatedAt)
}

func gatewayRebindClaimBoundNetwork(claim appaccess.GatewayRebindClaimV2,
	proof gatewayRebindAdmissionPhysicalProof,
) (gatewayRebindSuccessorNetworkObservation, error) {
	if claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		claim.Spec.OperationID != proof.Successor.Target.OperationID ||
		claim.Spec.SuccessorProfileOperationID != proof.Successor.Target.ProfileOperationID ||
		claim.ConfigureApproval.SpecDigest != proof.Successor.Target.Profile.SpecDigest {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid generated ingress rebind admission network binding")
	}
	value := proof.Successor.Network
	value.OperationID = claim.Spec.OperationID
	value.ClaimRequestDigest = claim.RequestDigest
	value.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
	return value, nil
}
