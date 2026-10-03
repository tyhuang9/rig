package generatedingress

import (
	"context"
	"net/netip"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindPreclaimSuccessorObservation struct {
	database   appaccess.GatewayRebindPreclaimSnapshot
	candidates []hostNetworkCandidate
	host       gatewayV2HostNetworkSnapshot
	dockerIDs  []string
	docker     []netip.Prefix
	result     GatewayRebindSuccessorPreflight
}

// InspectGatewayRebindPreclaimSuccessorPreflight checks an approved, unpersisted
// rebind proposal against a stable successor interface and complete host and
// Docker network inventory. It requires zero claims and is strictly read-only.
// The result is advisory: a future writer must repeat this inspection under
// its deployment-effects lease before claiming or creating successor effects.
func (m *Manager) InspectGatewayRebindPreclaimSuccessorPreflight(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
) (GatewayRebindSuccessorPreflight, error) {
	if m == nil || ctx == nil || repository == nil {
		return GatewayRebindSuccessorPreflight{}, &Error{Code: DiagnosticValidationFailed}
	}
	inventory := managerGatewayV2DockerInventory{manager: m}
	return m.inspectGatewayRebindPreclaimSuccessorPreflight(ctx, repository, proposal,
		gatewayRebindSuccessorPreflightReads{
			network: gatewayV2ProductionNetworkPlanReads(m), dockerIDs: inventory.listNetworkIDs,
		}, nil)
}

func (m *Manager) inspectGatewayRebindPreclaimSuccessorPreflight(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
	reads gatewayRebindSuccessorPreflightReads, checkpoint func(),
) (result GatewayRebindSuccessorPreflight, resultErr error) {
	if m == nil || ctx == nil || repository == nil ||
		reads.network.candidates == nil || reads.network.host == nil ||
		reads.network.docker == nil || reads.dockerIDs == nil {
		return GatewayRebindSuccessorPreflight{}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGatewayRaw(ctx)
	if err != nil {
		return GatewayRebindSuccessorPreflight{}, err
	}
	defer func() {
		if err := release(); err != nil {
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
		if resultErr != nil {
			result = GatewayRebindSuccessorPreflight{}
		}
	}()

	first, err := readGatewayRebindPreclaimSuccessorObservation(ctx, repository, proposal, reads)
	if err != nil {
		return GatewayRebindSuccessorPreflight{}, err
	}
	if checkpoint != nil {
		checkpoint()
	}
	if ctx.Err() != nil {
		return GatewayRebindSuccessorPreflight{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	second, err := readGatewayRebindPreclaimSuccessorObservation(ctx, repository, proposal, reads)
	if err != nil {
		return GatewayRebindSuccessorPreflight{}, err
	}
	if ctx.Err() != nil || !reflect.DeepEqual(first, second) {
		return GatewayRebindSuccessorPreflight{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	// A head or approval can change while the second network inventory is read.
	finalDatabase, err := repository.GatewayRebindPreclaimSnapshot(ctx, proposal)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(first.database, finalDatabase) {
		return GatewayRebindSuccessorPreflight{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	return first.result, nil
}

func readGatewayRebindPreclaimSuccessorObservation(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
	reads gatewayRebindSuccessorPreflightReads,
) (gatewayRebindPreclaimSuccessorObservation, error) {
	if ctx.Err() != nil {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	snapshot, err := repository.GatewayRebindPreclaimSnapshot(ctx, proposal)
	if err != nil {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	claim := snapshot.ProposedClaim
	profile := gatewayProfileBinding{
		RevisionID:     claim.Spec.SuccessorProfileRevisionID,
		RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SpecDigest:     claim.ConfigureApproval.SpecDigest,
		SelectedIPv4:   claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID:    claim.Spec.SuccessorProfile.InterfaceID,
		PortStart:      claim.Spec.SuccessorProfile.PortStart,
		PortEnd:        claim.Spec.SuccessorProfile.PortEnd,
	}
	if claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		!validGatewayProfileBinding(profile) {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorPreflightError(ctx)
	}

	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	dockerIDsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsBefore) {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	docker, err := reads.network.docker(ctx)
	if err != nil || ctx.Err() != nil {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	dockerIDsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsAfter) ||
		!equalStrings(dockerIDsBefore, dockerIDsAfter) {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}

	captured := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return append([]hostNetworkCandidate(nil), candidates...), nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{
				Routes:     append([]netip.Prefix(nil), host.Routes...),
				Interfaces: append([]netip.Prefix(nil), host.Interfaces...),
			}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			return append([]netip.Prefix(nil), docker...), nil
		},
	}
	plan, err := selectGatewayV2NetworkPlan(ctx, profile, captured)
	if err != nil {
		return gatewayRebindPreclaimSuccessorObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	result := GatewayRebindSuccessorPreflight{
		RebindOperationID:  claim.Spec.OperationID,
		ClaimRequestDigest: claim.RequestDigest,
		Profile: GatewayRebindSuccessorProfile{
			RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
			OperationID:   claim.Spec.SuccessorProfileOperationID,
			RequestDigest: claim.SuccessorProfileRequestDigest,
			SpecDigest:    profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4,
			InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
			ApprovedBy: claim.ConfigureApproval.ActorID,
		},
		Network: GatewayRebindSuccessorNetworkPlan{
			Subnet: plan.Subnet, GatewayIPv4: plan.GatewayIPv4, ContainerIPv4: plan.ContainerIPv4,
		},
	}
	return gatewayRebindPreclaimSuccessorObservation{
		database:   snapshot,
		candidates: append([]hostNetworkCandidate(nil), candidates...),
		host: gatewayV2HostNetworkSnapshot{
			Routes:     append([]netip.Prefix(nil), host.Routes...),
			Interfaces: append([]netip.Prefix(nil), host.Interfaces...),
		},
		dockerIDs: append([]string(nil), dockerIDsBefore...),
		docker:    append([]netip.Prefix(nil), docker...),
		result:    result,
	}, nil
}
