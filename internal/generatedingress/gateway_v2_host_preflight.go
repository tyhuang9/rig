package generatedingress

import (
	"context"
	"net/netip"

	"github.com/hostd/hostd/internal/hostnetwork"
)

// gatewayV2HostPreflight rechecks the exact approved interface, the complete
// host routing snapshot, and a stable ID-bound Docker network inventory. It
// validates the journal-bound plan and never selects a replacement.
func gatewayV2HostPreflight(ctx context.Context, manager *Manager, profile gatewayProfileBinding, plan gatewayV2NetworkPlan) error {
	if manager == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	return validateGatewayV2NetworkPlanAgainstReads(ctx, profile, plan, gatewayV2ProductionNetworkPlanReads(manager))
}

func gatewayV2SelectNetworkPlan(ctx context.Context, manager *Manager, profile gatewayProfileBinding) (gatewayV2NetworkPlan, error) {
	if manager == nil || ctx == nil {
		return gatewayV2NetworkPlan{}, &Error{Code: DiagnosticValidationFailed}
	}
	return selectGatewayV2NetworkPlan(ctx, profile, gatewayV2ProductionNetworkPlanReads(manager))
}

func gatewayV2ProductionNetworkPlanReads(manager *Manager) gatewayV2NetworkPlanReads {
	return gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			values, err := hostnetwork.CurrentCandidates()
			if err != nil {
				return nil, err
			}
			result := make([]hostNetworkCandidate, 0, len(values))
			for _, value := range values {
				result = append(result, hostNetworkCandidate{InterfaceID: value.InterfaceID, IPv4: value.IPv4, Prefix: value.Prefix})
			}
			return result, nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			snapshot, err := hostnetwork.CurrentIPv4NetworkSnapshot()
			if err != nil {
				return gatewayV2HostNetworkSnapshot{}, err
			}
			return gatewayV2HostNetworkSnapshot{
				Routes:     append([]netip.Prefix(nil), snapshot.Routes...),
				Interfaces: append([]netip.Prefix(nil), snapshot.InterfacePrefixes...),
			}, nil
		},
		docker: func(ctx context.Context) ([]netip.Prefix, error) {
			return readStableGatewayV2DockerNetworkPrefixes(ctx, managerGatewayV2DockerInventory{manager: manager})
		},
	}
}

func gatewayV2SelectedInterfacePreflight(profile gatewayProfileBinding) error {
	return checkGatewayV2SelectedInterface(profile, hostnetwork.CurrentCandidates)
}

func checkGatewayV2SelectedInterface(profile gatewayProfileBinding, candidatesReader func() ([]hostnetwork.Candidate, error)) error {
	if !validGatewayProfileBinding(profile) || candidatesReader == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	candidates, err := candidatesReader()
	if err != nil {
		return &Error{Code: DiagnosticIngressDrift}
	}
	if _, err := hostnetwork.Select(candidates, profile.InterfaceID, profile.SelectedIPv4); err != nil {
		return &Error{Code: DiagnosticIngressDrift}
	}
	return nil
}
