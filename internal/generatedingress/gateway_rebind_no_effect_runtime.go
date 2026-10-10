package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindNoEffectPhysicalObservation struct {
	Version          int                                      `json:"version"`
	Purpose          string                                   `json:"purpose"`
	Identity         gatewayRebindSuccessorIdentity           `json:"identity"`
	Profile          gatewayRebindSuccessorIntentProfile      `json:"profile"`
	Candidates       []gatewayRebindSuccessorNetworkCandidate `json:"candidates"`
	HostRoutes       []string                                 `json:"hostRoutes"`
	HostInterfaces   []string                                 `json:"hostInterfaces"`
	DockerPrefixes   []string                                 `json:"dockerPrefixes"`
	DockerNetworkIDs []string                                 `json:"dockerNetworkIds"`
	OwnedContainers  []string                                 `json:"ownedContainers"`
	OwnedVolumes     []string                                 `json:"ownedVolumes"`
	OwnedNetworks    []string                                 `json:"ownedNetworks"`
}

func gatewayRebindNoEffectProfile(claim appaccess.GatewayRebindClaimV2) gatewayRebindSuccessorIntentProfile {
	return gatewayRebindSuccessorIntentProfile{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
		SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
	}
}

func (m *Manager) inspectGatewayRebindClaimOwnedNames(ctx context.Context,
	generation uint64, operationID string, args ...string,
) ([]string, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || generation == 0 ||
		!validCanonicalUUID(operationID) || len(args) < 2 {
		return nil, errors.New("invalid no-effect ownership inventory input")
	}
	format := "{{.Name}}"
	if args[0] == "container" {
		format = "{{.Names}}"
	}
	args = append(args,
		"--filter", "label="+gatewayV2ManagedLabelKey,
		"--filter", "label="+gatewayV2IdentityLabelKey+"="+gatewayRebindSuccessorIdentityVersion,
		"--filter", "label="+gatewayV2OperationLabelKey+"="+operationID,
		"--filter", "label="+gatewayRebindGenerationLabelKey+"="+strconv.FormatUint(generation, 10),
		"--format", format)
	result, err := m.run(ctx, m.options.CommandTimeout, args...)
	if err != nil {
		return nil, err
	}
	defer clearResult(&result)
	body := strings.TrimSpace(string(result.Stdout))
	if body == "" {
		return []string{}, nil
	}
	values := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r") {
			return nil, errors.New("invalid no-effect ownership inventory")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("duplicated no-effect ownership inventory")
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	return values, nil
}

func (m *Manager) readGatewayRebindNoEffectPhysical(ctx context.Context,
	identity gatewayRebindSuccessorIdentity, profile gatewayRebindSuccessorIntentProfile,
) (gatewayRebindNoEffectPhysicalObservation, error) {
	invalid := func() (gatewayRebindNoEffectPhysicalObservation, error) {
		return gatewayRebindNoEffectPhysicalObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if m == nil || ctx == nil || ctx.Err() != nil || !validGatewayRebindSuccessorIdentityValue(identity) ||
		!validGatewayRebindSuccessorIntentProfile(profile) {
		return invalid()
	}
	if _, _, found, err := m.inspectNamedGatewayNetwork(ctx, identity.IngressNetwork); err != nil || found {
		return invalid()
	}
	if _, _, found, err := m.inspectNamedVolumeWithIdentity(ctx, identity.ConfigVolume); err != nil || found {
		return invalid()
	}
	if _, _, found, err := m.inspectNamedVolumeWithIdentity(ctx, identity.DataVolume); err != nil || found {
		return invalid()
	}
	if _, _, found, err := m.inspectNamedGatewayContainer(ctx, identity.StageContainer); err != nil || found {
		return invalid()
	}
	if _, _, found, err := m.inspectNamedGatewayContainer(ctx, identity.FinalContainer); err != nil || found {
		return invalid()
	}
	containers, err := m.inspectGatewayRebindClaimOwnedNames(ctx, identity.Generation,
		identity.OperationID, "container", "ls", "--all")
	if err != nil || len(containers) != 0 {
		return invalid()
	}
	volumes, err := m.inspectGatewayRebindClaimOwnedNames(ctx, identity.Generation,
		identity.OperationID, "volume", "ls")
	if err != nil || len(volumes) != 0 {
		return invalid()
	}
	networks, err := m.inspectGatewayRebindClaimOwnedNames(ctx, identity.Generation,
		identity.OperationID, "network", "ls")
	if err != nil || len(networks) != 0 {
		return invalid()
	}
	reads := gatewayV2ProductionNetworkPlanReads(m)
	candidates, err := reads.candidates()
	if err != nil {
		return invalid()
	}
	host, err := reads.host()
	if err != nil {
		return invalid()
	}
	inventory := managerGatewayV2DockerInventory{manager: m}
	idsBefore, err := inventory.listNetworkIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return invalid()
	}
	dockerPrefixes, err := reads.docker(ctx)
	if err != nil || ctx.Err() != nil {
		return invalid()
	}
	idsAfter, err := inventory.listNetworkIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return invalid()
	}
	routes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return invalid()
	}
	interfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return invalid()
	}
	prefixes, err := canonicalGatewayRebindPrefixes(dockerPrefixes)
	if err != nil {
		return invalid()
	}
	addressPresent := false
	for _, candidate := range candidates {
		if candidate.IPv4 == profile.SelectedIPv4 {
			addressPresent = true
			break
		}
	}
	if addressPresent {
		for port := profile.PortStart; ; port++ {
			if !probeGatewayRebindHandoverListenerAbsent(ctx, profile.SelectedIPv4, port) {
				return invalid()
			}
			if port == profile.PortEnd {
				break
			}
		}
	}
	return gatewayRebindNoEffectPhysicalObservation{
		Version: 1, Purpose: "hostd/generated-ingress/rebind/no-successor-effects-observation/v1",
		Identity: identity, Profile: profile, Candidates: gatewayRebindCandidateProjection(candidates),
		HostRoutes: routes, HostInterfaces: interfaces, DockerPrefixes: prefixes,
		DockerNetworkIDs: append([]string(nil), idsAfter...), OwnedContainers: containers,
		OwnedVolumes: volumes, OwnedNetworks: networks,
	}, nil
}

func (d managerGatewayRebindCrossStoreDriver) proveNoSuccessorEffectsLocked(ctx context.Context,
	claim appaccess.GatewayRebindClaimV2, roster []appaccess.GatewayRebindRosterEntryV2,
	checkpoint gatewayRebindPredecessorCheckpoint,
) (gatewayRebindNoEffectAbortProof, error) {
	invalid := func() (gatewayRebindNoEffectAbortProof, error) {
		return gatewayRebindNoEffectAbortProof{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil || claim.State != appaccess.GatewayRebindPrepared ||
		claim.StateSequence != 1 || !validGatewayRebindPredecessorCheckpoint(checkpoint) ||
		claim.Spec.OperationID != checkpoint.OperationID ||
		claim.Spec.SuccessorProtectedGeneration != checkpoint.Generation ||
		claim.Spec.Predecessor != checkpoint.sourceRef() {
		return invalid()
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(roster)
	if err != nil || rosterDigest != claim.Spec.RosterDigest || int64(len(roster)) != claim.Spec.RosterCount {
		return invalid()
	}
	profile := gatewayRebindNoEffectProfile(claim)
	identity, err := newGatewayRebindSuccessorIdentity(checkpoint.Generation, claim.Spec.OperationID,
		GatewayRebindSuccessorProfile(profile))
	if err != nil {
		return invalid()
	}
	first, err := d.manager.readGatewayRebindNoEffectPhysical(ctx, identity, profile)
	if err != nil {
		return invalid()
	}
	second, err := d.manager.readGatewayRebindNoEffectPhysical(ctx, identity, profile)
	if err != nil || !reflect.DeepEqual(first, second) || ctx.Err() != nil {
		return invalid()
	}
	observationDigest, err := canonicalDigest(second)
	createdAt := gatewayRebindTimeStrictlyAfter(d.manager.gatewayRebindProgressTime(),
		claim.CreatedAt.UTC().Format(time.RFC3339Nano))
	proof := gatewayRebindNoEffectAbortProof{
		Version: gatewayRebindNoEffectProofVersion, Purpose: gatewayRebindNoEffectProofPurpose,
		Generation: checkpoint.Generation, OperationID: checkpoint.OperationID,
		ClaimRequestDigest: claim.RequestDigest, PredecessorCheckpointDigest: checkpoint.Digest,
		SourceStateDigest: checkpoint.SourceStateDigest, SuccessorIdentityDigest: identity.Digest,
		ObservationDigest: observationDigest, CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}
	if err == nil {
		proof.Digest, err = gatewayRebindNoEffectAbortProofDigest(proof)
	}
	if err != nil || !validGatewayRebindNoEffectAbortProof(proof) {
		return invalid()
	}
	return proof, nil
}
