// Package hostnetwork identifies the exact host interface approved for LAN
// publication. It does not publish ports or change host networking.
package hostnetwork

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnavailable = errors.New("selected LAN interface or address is unavailable")
	ErrAmbiguous   = errors.New("selected LAN address belongs to multiple interfaces")
)

// Candidate is an explicitly selectable, currently assigned private IPv4.
// InterfaceID includes both the OS interface index and name. A change to
// either requires the operator to approve a new profile.
type Candidate struct {
	InterfaceID string
	Name        string
	IPv4        string
	Prefix      netip.Prefix
}

type interfaceSnapshot struct {
	Interface net.Interface
	Addresses []net.Addr
}

// CurrentCandidates fails as a whole if interface enumeration is incomplete;
// callers must not fall back to another adapter or a wildcard binding.
func CurrentCandidates() ([]Candidate, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	snapshots := make([]interfaceSnapshot, 0, len(interfaces))
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, interfaceSnapshot{Interface: iface, Addresses: addresses})
	}
	return candidatesFromSnapshots(snapshots), nil
}

// Select requires the approved interface and address to remain uniquely
// present. It never chooses another eligible interface on the caller's behalf.
func Select(candidates []Candidate, interfaceID, ipv4 string) (Candidate, error) {
	address, err := netip.ParseAddr(ipv4)
	if err != nil || !address.Is4() || !address.IsPrivate() || address.String() != ipv4 || interfaceID == "" {
		return Candidate{}, ErrUnavailable
	}
	var selected Candidate
	matched := false
	owners := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.IPv4 != ipv4 || candidate.InterfaceID == "" {
			continue
		}
		owners[candidate.InterfaceID] = struct{}{}
		if candidate.InterfaceID == interfaceID {
			selected = candidate
			matched = true
		}
	}
	if len(owners) > 1 {
		return Candidate{}, ErrAmbiguous
	}
	if !matched {
		return Candidate{}, ErrUnavailable
	}
	return selected, nil
}

func candidatesFromSnapshots(snapshots []interfaceSnapshot) []Candidate {
	var result []Candidate
	seen := make(map[string]struct{})
	for _, snapshot := range snapshots {
		iface := snapshot.Interface
		if iface.Index <= 0 || !validInterfaceName(iface.Name) ||
			iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		id := fmt.Sprintf("%d/%s", iface.Index, iface.Name)
		for _, value := range snapshot.Addresses {
			network, ok := value.(*net.IPNet)
			if !ok || network == nil {
				continue
			}
			bytes := network.IP.To4()
			if bytes == nil {
				continue
			}
			address := netip.AddrFrom4([4]byte{bytes[0], bytes[1], bytes[2], bytes[3]})
			bits, width := network.Mask.Size()
			if !address.IsPrivate() || width != 32 || bits < 0 || bits > 32 {
				continue
			}
			key := id + "|" + address.String()
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, Candidate{
				InterfaceID: id, Name: iface.Name, IPv4: address.String(),
				Prefix: netip.PrefixFrom(address, bits).Masked(),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].InterfaceID != result[j].InterfaceID {
			return result[i].InterfaceID < result[j].InterfaceID
		}
		return result[i].IPv4 < result[j].IPv4
	})
	return result
}

func validInterfaceName(name string) bool {
	if !utf8.ValidString(name) || name == "" || len(name) > 480 || strings.TrimSpace(name) != name {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
