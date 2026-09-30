package generatedingress

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/hostd/hostd/internal/hostnetwork"
)

func TestGatewayV2HostPreflightRequiresApprovedInterfaceAndNonoverlap(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	approved := hostnetwork.Candidate{InterfaceID: input.Profile.InterfaceID, IPv4: input.Profile.SelectedIPv4}
	checks := 0
	reads := gatewayV2HostPreflightReads{
		candidates: func() ([]hostnetwork.Candidate, error) {
			return []hostnetwork.Candidate{approved}, nil
		},
		checkSubnet: func(prefix netip.Prefix) error {
			checks++
			if prefix.String() != input.Network.Subnet {
				t.Fatalf("checked subnet = %s", prefix)
			}
			return nil
		},
	}
	if err := checkGatewayV2HostPreflight(input.Profile, input.Network, reads); err != nil || checks != 1 {
		t.Fatalf("approved host preflight: err=%v checks=%d", err, checks)
	}
	if err := checkGatewayV2SelectedInterface(input.Profile, reads.candidates); err != nil {
		t.Fatalf("approved interface before port bind: %v", err)
	}

	reads.candidates = func() ([]hostnetwork.Candidate, error) {
		return []hostnetwork.Candidate{{InterfaceID: "replacement", IPv4: input.Profile.SelectedIPv4}}, nil
	}
	if err := checkGatewayV2HostPreflight(input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) || checks != 1 {
		t.Fatalf("changed interface: err=%v checks=%d", err, checks)
	}
	if err := checkGatewayV2SelectedInterface(input.Profile, reads.candidates); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("changed interface before port bind: %v", err)
	}

	reads.candidates = func() ([]hostnetwork.Candidate, error) {
		return []hostnetwork.Candidate{approved, {InterfaceID: "duplicate", IPv4: input.Profile.SelectedIPv4}}, nil
	}
	if err := checkGatewayV2HostPreflight(input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) || checks != 1 {
		t.Fatalf("ambiguous address: err=%v checks=%d", err, checks)
	}

	reads.candidates = func() ([]hostnetwork.Candidate, error) { return []hostnetwork.Candidate{approved}, nil }
	reads.checkSubnet = func(netip.Prefix) error { return hostnetwork.ErrIngressSubnetOverlap }
	if err := checkGatewayV2HostPreflight(input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("overlapping host route: %v", err)
	}
}

func TestGatewayV2HostPreflightFailsClosedOnIncompleteReads(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	reads := gatewayV2HostPreflightReads{
		candidates:  func() ([]hostnetwork.Candidate, error) { return nil, errors.New("interface read failed") },
		checkSubnet: func(netip.Prefix) error { t.Fatal("subnet checked after failed interface read"); return nil },
	}
	if err := checkGatewayV2HostPreflight(input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("incomplete interface read: %v", err)
	}
	reads.candidates = func() ([]hostnetwork.Candidate, error) {
		return []hostnetwork.Candidate{{InterfaceID: input.Profile.InterfaceID, IPv4: input.Profile.SelectedIPv4}}, nil
	}
	reads.checkSubnet = func(netip.Prefix) error { return hostnetwork.ErrIncompleteHostSnapshot }
	if err := checkGatewayV2HostPreflight(input.Profile, input.Network, reads); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("incomplete route read: %v", err)
	}

	invalid := input.Network
	invalid.ContainerIPv4 = invalid.GatewayIPv4
	if err := checkGatewayV2HostPreflight(input.Profile, invalid, reads); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("invalid network plan: %v", err)
	}
}
