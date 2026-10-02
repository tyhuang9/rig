package generatedingress

import (
	"net/netip"

	"github.com/hostd/hostd/internal/hostnetwork"
)

// gatewayV2HostPreflight rechecks the exact approved interface and Docker
// subnet immediately before creating the ingress network. Once Docker creates
// that network, its own bridge route can overlap the planned subnet; the
// selected-interface check below is used again before a host-port bind.
func gatewayV2HostPreflight(profile gatewayProfileBinding, plan gatewayV2NetworkPlan) error {
	return checkGatewayV2HostPreflight(profile, plan, gatewayV2HostPreflightReads{
		candidates: hostnetwork.CurrentCandidates,
		checkSubnet: func(prefix netip.Prefix) error {
			snapshot, err := hostnetwork.CurrentIPv4NetworkSnapshot()
			if err != nil {
				return err
			}
			return hostnetwork.CheckDockerIngressSubnetNonoverlap(prefix, snapshot)
		},
	})
}

func gatewayV2SelectedInterfacePreflight(profile gatewayProfileBinding) error {
	return checkGatewayV2SelectedInterface(profile, hostnetwork.CurrentCandidates)
}

type gatewayV2HostPreflightReads struct {
	candidates  func() ([]hostnetwork.Candidate, error)
	checkSubnet func(netip.Prefix) error
}

func checkGatewayV2HostPreflight(profile gatewayProfileBinding, plan gatewayV2NetworkPlan, reads gatewayV2HostPreflightReads) error {
	if !validGatewayProfileBinding(profile) || !validGatewayV2NetworkPlan(plan) ||
		!gatewayV2NetworkExcludesSelectedLAN(profile, plan) || reads.candidates == nil || reads.checkSubnet == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	if err := checkGatewayV2SelectedInterface(profile, reads.candidates); err != nil {
		return err
	}
	prefix, err := netip.ParsePrefix(plan.Subnet)
	if err != nil || reads.checkSubnet(prefix) != nil {
		return &Error{Code: DiagnosticIngressDrift}
	}
	return nil
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
