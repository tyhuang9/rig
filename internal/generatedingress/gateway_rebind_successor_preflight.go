package generatedingress

import (
	"context"
	"net/netip"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindSuccessorPreflight is the immutable result of a passive
// successor-network inspection. A future writer must repeat this inspection
// while holding its deployment-effects admission lease before creating any
// protected successor artifact or Docker resource.
type GatewayRebindSuccessorPreflight struct {
	RebindOperationID  string
	ClaimRequestDigest string
	Profile            GatewayRebindSuccessorProfile
	Network            GatewayRebindSuccessorNetworkPlan
}

// GatewayRebindSuccessorProfile projects the already-approved successor
// identity and host binding retained by the prepared SQLite claim.
type GatewayRebindSuccessorProfile struct {
	RevisionID     string
	RevisionNumber int64
	OperationID    string
	RequestDigest  string
	SpecDigest     string
	SelectedIPv4   string
	InterfaceID    string
	PortStart      uint16
	PortEnd        uint16
	ApprovedBy     string
}

// GatewayRebindSuccessorNetworkPlan is the deterministic first nonconflicting
// private network derived from the complete stable host and Docker inventory.
type GatewayRebindSuccessorNetworkPlan struct {
	Subnet        string
	GatewayIPv4   string
	ContainerIPv4 string
}

type gatewayRebindSuccessorPreflightObservation struct {
	database   appaccess.GatewayRebindStartupSnapshot
	candidates []hostNetworkCandidate
	host       gatewayV2HostNetworkSnapshot
	dockerIDs  []string
	docker     []netip.Prefix
	result     GatewayRebindSuccessorPreflight
}

type gatewayRebindSuccessorPreflightReads struct {
	network   gatewayV2NetworkPlanReads
	dockerIDs func(context.Context) ([]string, error)
}

// InspectGatewayRebindSuccessorPreflight proves that exactly one prepared,
// approved rebind claim names a uniquely present successor interface and that
// two complete host and ID-bound Docker observations yield the same exact
// deterministic network plan. The predecessor interface is deliberately not
// inspected because it may be absent or DHCP-drifted during an approved
// rebind. This method is strictly read-only.
func (m *Manager) InspectGatewayRebindSuccessorPreflight(ctx context.Context,
	repository *appaccess.Repository,
) (result GatewayRebindSuccessorPreflight, resultErr error) {
	if m == nil || ctx == nil || repository == nil {
		return GatewayRebindSuccessorPreflight{}, &Error{Code: DiagnosticValidationFailed}
	}
	inventory := managerGatewayV2DockerInventory{manager: m}
	return m.inspectGatewayRebindSuccessorPreflight(
		ctx, repository, gatewayRebindSuccessorPreflightReads{
			network: gatewayV2ProductionNetworkPlanReads(m), dockerIDs: inventory.listNetworkIDs,
		}, nil,
	)
}

func (m *Manager) inspectGatewayRebindSuccessorPreflight(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads, checkpoint func(),
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

	first, err := readGatewayRebindSuccessorPreflightObservation(ctx, repository, reads)
	if err != nil {
		return GatewayRebindSuccessorPreflight{}, err
	}
	if checkpoint != nil {
		checkpoint()
	}
	if ctx.Err() != nil {
		return GatewayRebindSuccessorPreflight{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	second, err := readGatewayRebindSuccessorPreflightObservation(ctx, repository, reads)
	if err != nil {
		return GatewayRebindSuccessorPreflight{}, err
	}
	if ctx.Err() != nil || !reflect.DeepEqual(first, second) {
		return GatewayRebindSuccessorPreflight{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	return first.result, nil
}

func readGatewayRebindSuccessorPreflightObservation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
) (gatewayRebindSuccessorPreflightObservation, error) {
	if ctx.Err() != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	snapshot, err := repository.GatewayRebindStartupSnapshot(ctx)
	if err != nil || len(snapshot.Claims) != 1 {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorPreflightError(ctx)
	}
	claim := snapshot.Claims[0]
	profile := gatewayProfileBinding{
		RevisionID:     claim.Claim.Spec.SuccessorProfileRevisionID,
		RevisionNumber: claim.Claim.Spec.SuccessorProfileRevisionNumber,
		SpecDigest:     claim.Claim.ConfigureApproval.SpecDigest,
		SelectedIPv4:   claim.Claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID:    claim.Claim.Spec.SuccessorProfile.InterfaceID,
		PortStart:      claim.Claim.Spec.SuccessorProfile.PortStart,
		PortEnd:        claim.Claim.Spec.SuccessorProfile.PortEnd,
	}
	if claim.Claim.State != appaccess.GatewayRebindPrepared || claim.Claim.StateSequence != 1 ||
		!validGatewayProfileBinding(profile) {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorPreflightError(ctx)
	}

	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	dockerIDsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsBefore) {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	docker, err := reads.network.docker(ctx)
	if err != nil || ctx.Err() != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	dockerIDsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsAfter) ||
		!equalStrings(dockerIDsBefore, dockerIDsAfter) {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
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
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	result := GatewayRebindSuccessorPreflight{
		RebindOperationID:  claim.Claim.Spec.OperationID,
		ClaimRequestDigest: claim.Claim.RequestDigest,
		Profile: GatewayRebindSuccessorProfile{
			RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
			OperationID:   claim.Claim.Spec.SuccessorProfileOperationID,
			RequestDigest: claim.Claim.SuccessorProfileRequestDigest,
			SpecDigest:    profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4,
			InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
			ApprovedBy: claim.Claim.ConfigureApproval.ActorID,
		},
		Network: GatewayRebindSuccessorNetworkPlan{
			Subnet: plan.Subnet, GatewayIPv4: plan.GatewayIPv4, ContainerIPv4: plan.ContainerIPv4,
		},
	}
	return gatewayRebindSuccessorPreflightObservation{
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

func validGatewayRebindSuccessorDockerIDs(ids []string) bool {
	for index, id := range ids {
		if !validContainerID(id) || normalizeID(id) != id ||
			(index > 0 && ids[index-1] >= id) {
			return false
		}
	}
	return true
}

func gatewayRebindSuccessorPreflightError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

func gatewayRebindSuccessorNetworkError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticIngressDrift}
}
