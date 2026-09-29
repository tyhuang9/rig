package hostnetwork

import (
	"errors"
	"net"
	"testing"
)

func TestCandidatesRequireUpNonLoopbackPrivateIPv4AndRemainDeterministic(t *testing.T) {
	snapshots := []interfaceSnapshot{
		{Interface: net.Interface{Index: 5, Name: "Ethernet", Flags: net.FlagUp}, Addresses: []net.Addr{
			ipv4("192.168.50.20", 24), ipv4("192.168.50.20", 24), ipv4("8.8.8.8", 24),
			&net.IPNet{IP: net.ParseIP("fd00::1"), Mask: net.CIDRMask(64, 128)},
		}},
		{Interface: net.Interface{Index: 2, Name: "Wi-Fi", Flags: net.FlagUp}, Addresses: []net.Addr{
			ipv4("10.0.0.8", 24),
		}},
		{Interface: net.Interface{Index: 3, Name: "Down"}, Addresses: []net.Addr{ipv4("172.16.0.4", 16)}},
		{Interface: net.Interface{Index: 4, Name: "Loopback", Flags: net.FlagUp | net.FlagLoopback}, Addresses: []net.Addr{ipv4("127.0.0.1", 8)}},
		{Interface: net.Interface{Index: 6, Name: "bad\nname", Flags: net.FlagUp}, Addresses: []net.Addr{ipv4("10.0.0.9", 24)}},
		{Interface: net.Interface{Index: 7, Name: "Bad mask", Flags: net.FlagUp}, Addresses: []net.Addr{
			&net.IPNet{IP: net.ParseIP("10.0.0.10"), Mask: net.IPMask{255, 0, 255, 0}},
		}},
	}
	got := candidatesFromSnapshots(snapshots)
	if len(got) != 2 || got[0].InterfaceID != "2/Wi-Fi" || got[0].IPv4 != "10.0.0.8" || got[0].Prefix.String() != "10.0.0.0/24" ||
		got[1].InterfaceID != "5/Ethernet" || got[1].IPv4 != "192.168.50.20" || got[1].Prefix.String() != "192.168.50.0/24" {
		t.Fatalf("eligible candidates = %#v", got)
	}
}

func TestSelectRequiresExactCurrentOwnerAndRejectsAmbiguity(t *testing.T) {
	first := Candidate{InterfaceID: "5/Ethernet", Name: "Ethernet", IPv4: "192.168.50.20"}
	selected, err := Select([]Candidate{first}, first.InterfaceID, first.IPv4)
	if err != nil || selected != first {
		t.Fatalf("selected = %#v err=%v", selected, err)
	}
	for _, test := range []struct {
		name       string
		candidates []Candidate
		id         string
		ip         string
		want       error
	}{
		{"renamed adapter", []Candidate{first}, "5/Ethernet 2", first.IPv4, ErrUnavailable},
		{"DHCP address changed", []Candidate{first}, first.InterfaceID, "192.168.50.21", ErrUnavailable},
		{"wildcard", []Candidate{first}, first.InterfaceID, "0.0.0.0", ErrUnavailable},
		{"duplicate assigned IP", []Candidate{first, {InterfaceID: "7/Wi-Fi", IPv4: first.IPv4}}, first.InterfaceID, first.IPv4, ErrAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Select(test.candidates, test.id, test.ip); !errors.Is(err, test.want) {
				t.Fatalf("selection error = %v, want %v", err, test.want)
			}
		})
	}
}

func ipv4(value string, bits int) *net.IPNet {
	return &net.IPNet{IP: net.ParseIP(value), Mask: net.CIDRMask(bits, 32)}
}
