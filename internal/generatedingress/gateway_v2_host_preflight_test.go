package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/hostd/hostd/internal/hostnetwork"
)

func TestGatewayV2HostPreflightRequiresApprovedInterfaceAndNonoverlap(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	approved := gatewayV2TestCandidate(input.Profile)
	reads := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return []hostNetworkCandidate{approved}, nil },
		host:       func() (gatewayV2HostNetworkSnapshot, error) { return gatewayV2HostNetworkSnapshot{}, nil },
		docker:     func(context.Context) ([]netip.Prefix, error) { return nil, nil },
	}
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, input.Network, reads); err != nil {
		t.Fatalf("approved host preflight: %v", err)
	}
	if err := checkGatewayV2SelectedInterface(input.Profile, func() ([]hostnetwork.Candidate, error) {
		return []hostnetwork.Candidate{{InterfaceID: approved.InterfaceID, IPv4: approved.IPv4}}, nil
	}); err != nil {
		t.Fatalf("approved interface before port bind: %v", err)
	}

	reads.candidates = func() ([]hostNetworkCandidate, error) {
		return []hostNetworkCandidate{{InterfaceID: "replacement", IPv4: input.Profile.SelectedIPv4, Prefix: approved.Prefix}}, nil
	}
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("changed interface: %v", err)
	}

	reads.candidates = func() ([]hostNetworkCandidate, error) {
		return []hostNetworkCandidate{approved, {InterfaceID: "duplicate", IPv4: input.Profile.SelectedIPv4, Prefix: approved.Prefix}}, nil
	}
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("ambiguous address: %v", err)
	}

	reads.candidates = func() ([]hostNetworkCandidate, error) { return []hostNetworkCandidate{approved}, nil }
	reads.docker = func(context.Context) ([]netip.Prefix, error) {
		return []netip.Prefix{netip.MustParsePrefix(input.Network.Subnet)}, nil
	}
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("overlapping Docker network: %v", err)
	}
}

func TestGatewayV2HostPreflightFailsClosedOnIncompleteReads(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	reads := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return nil, errors.New("interface read failed") },
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			t.Fatal("host snapshot read after failed interface read")
			return gatewayV2HostNetworkSnapshot{}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			t.Fatal("Docker snapshot read after failed interface read")
			return nil, nil
		},
	}
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("incomplete interface read: %v", err)
	}
	reads.candidates = func() ([]hostNetworkCandidate, error) {
		return []hostNetworkCandidate{gatewayV2TestCandidate(input.Profile)}, nil
	}
	reads.host = func() (gatewayV2HostNetworkSnapshot, error) {
		return gatewayV2HostNetworkSnapshot{}, errors.New("incomplete host snapshot")
	}
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("incomplete route read: %v", err)
	}

	invalid := input.Network
	invalid.ContainerIPv4 = invalid.GatewayIPv4
	if err := validateGatewayV2NetworkPlanAgainstReads(context.Background(), input.Profile, invalid, reads); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("invalid network plan: %v", err)
	}
}
