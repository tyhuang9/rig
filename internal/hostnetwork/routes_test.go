package hostnetwork

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestCheckDockerIngressSubnetNonoverlap(t *testing.T) {
	proposed := netip.MustParsePrefix("172.30.40.0/28")
	tests := []struct {
		name     string
		snapshot IPv4NetworkSnapshot
		want     error
	}{
		{
			name: "disjoint including default route",
			snapshot: IPv4NetworkSnapshot{
				complete: true,
				Routes: []netip.Prefix{
					netip.MustParsePrefix("0.0.0.0/0"),
					netip.MustParsePrefix("10.20.0.0/16"),
				},
				InterfacePrefixes: []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")},
			},
		},
		{
			name: "route contains proposal",
			snapshot: IPv4NetworkSnapshot{complete: true, Routes: []netip.Prefix{
				netip.MustParsePrefix("172.30.0.0/16"),
			}},
			want: ErrIngressSubnetOverlap,
		},
		{
			name: "proposal contains route",
			snapshot: IPv4NetworkSnapshot{complete: true, Routes: []netip.Prefix{
				netip.MustParsePrefix("172.30.40.8/30"),
			}},
			want: ErrIngressSubnetOverlap,
		},
		{
			name: "interface prefix overlaps",
			snapshot: IPv4NetworkSnapshot{complete: true, InterfacePrefixes: []netip.Prefix{
				netip.MustParsePrefix("172.30.40.0/24"),
			}},
			want: ErrIngressSubnetOverlap,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckDockerIngressSubnetNonoverlap(proposed, test.snapshot)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCheckIgnoresOnlyDefaultRoute(t *testing.T) {
	proposed := netip.MustParsePrefix("10.200.0.0/24")
	if err := CheckDockerIngressSubnetNonoverlap(proposed, IPv4NetworkSnapshot{
		complete: true,
		Routes:   []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}); err != nil {
		t.Fatalf("default route rejected proposal: %v", err)
	}
	if err := CheckDockerIngressSubnetNonoverlap(proposed, IPv4NetworkSnapshot{
		complete: true,
		Routes:   []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1")},
	}); !errors.Is(err, ErrIngressSubnetOverlap) {
		t.Fatalf("more-specific route error = %v, want overlap", err)
	}
	if err := CheckDockerIngressSubnetNonoverlap(proposed, IPv4NetworkSnapshot{
		complete:          true,
		InterfacePrefixes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	}); !errors.Is(err, ErrIngressSubnetOverlap) {
		t.Fatalf("default interface prefix error = %v, want overlap", err)
	}
}

func TestCheckRejectsInvalidProposalAndIncompleteSnapshot(t *testing.T) {
	validSnapshot := IPv4NetworkSnapshot{complete: true}
	for _, proposed := range []netip.Prefix{
		{},
		netip.PrefixFrom(netip.MustParseAddr("10.20.30.40"), 24),
		netip.MustParsePrefix("8.8.8.0/24"),
		netip.MustParsePrefix("10.0.0.0/7"),
		netip.MustParsePrefix("fd00::/64"),
	} {
		if err := CheckDockerIngressSubnetNonoverlap(proposed, validSnapshot); !errors.Is(err, ErrInvalidIngressSubnet) {
			t.Errorf("proposal %v error = %v, want invalid subnet", proposed, err)
		}
	}

	proposed := netip.MustParsePrefix("10.200.0.0/24")
	for _, snapshot := range []IPv4NetworkSnapshot{
		{},
		{complete: true, Routes: []netip.Prefix{{}}},
		{complete: true, Routes: []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr("192.168.1.9"), 24)}},
		{complete: true, InterfacePrefixes: []netip.Prefix{netip.MustParsePrefix("fd00::/64")}},
	} {
		if err := CheckDockerIngressSubnetNonoverlap(proposed, snapshot); !errors.Is(err, ErrIncompleteHostSnapshot) {
			t.Errorf("snapshot %#v error = %v, want incomplete snapshot", snapshot, err)
		}
	}
}

func TestInterfacePrefixesFailClosedOnInvalidData(t *testing.T) {
	prefixes, err := interfacePrefixesFromSnapshots([]routeInterfaceSnapshot{
		{index: 2, addresses: []net.Addr{
			&net.IPNet{IP: net.ParseIP("192.168.50.21"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("fd00::1"), Mask: net.CIDRMask(64, 128)},
		}},
	})
	if err != nil || len(prefixes) != 1 || prefixes[0] != netip.MustParsePrefix("192.168.50.0/24") {
		t.Fatalf("prefixes = %v, err = %v", prefixes, err)
	}

	for _, snapshots := range [][]routeInterfaceSnapshot{
		{{index: 0}},
		{{index: 2, addresses: []net.Addr{nil}}},
		{{index: 2, addresses: []net.Addr{opaqueAddr("unknown")}}},
		{{index: 2, addresses: []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.IPMask{255, 0, 255, 0}}}}},
	} {
		if _, err := interfacePrefixesFromSnapshots(snapshots); !errors.Is(err, ErrIncompleteHostSnapshot) {
			t.Errorf("snapshots %#v error = %v, want incomplete snapshot", snapshots, err)
		}
	}
}

type opaqueAddr string

func (address opaqueAddr) Network() string { return "opaque" }
func (address opaqueAddr) String() string  { return string(address) }
