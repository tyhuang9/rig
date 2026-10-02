package hostnetwork

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
)

var (
	ErrInvalidIngressSubnet   = errors.New("invalid Docker ingress subnet")
	ErrIncompleteHostSnapshot = errors.New("incomplete host IPv4 network snapshot")
	ErrIngressSubnetOverlap   = errors.New("Docker ingress subnet overlaps host networking")
)

// IPv4NetworkSnapshot is a read-only view of all IPv4 routes and assigned
// interface prefixes observed on the host. The completion marker is private
// so callers cannot manufacture a usable snapshot without reading the host.
type IPv4NetworkSnapshot struct {
	Routes            []netip.Prefix
	InterfacePrefixes []netip.Prefix
	complete          bool
}

// CurrentIPv4NetworkSnapshot reads the host route table and every interface's
// assigned IPv4 prefixes. It returns no usable snapshot if either read fails.
func CurrentIPv4NetworkSnapshot() (IPv4NetworkSnapshot, error) {
	routes, err := currentIPv4Routes()
	if err != nil {
		return IPv4NetworkSnapshot{}, fmt.Errorf("%w: read host IPv4 routes: %w", ErrIncompleteHostSnapshot, err)
	}
	interfacePrefixes, err := currentIPv4InterfacePrefixes()
	if err != nil {
		return IPv4NetworkSnapshot{}, fmt.Errorf("%w: read host IPv4 interface prefixes: %w", ErrIncompleteHostSnapshot, err)
	}
	snapshot := IPv4NetworkSnapshot{
		Routes:            routes,
		InterfacePrefixes: interfacePrefixes,
		complete:          true,
	}
	if err := validateIPv4NetworkSnapshot(snapshot); err != nil {
		return IPv4NetworkSnapshot{}, err
	}
	return snapshot, nil
}

// CheckDockerIngressSubnetNonoverlap is a pure check over an already captured
// host snapshot. The proposed subnet must be canonical and wholly contained in
// an RFC 1918 IPv4 range. Only default routes are excluded from overlap checks.
func CheckDockerIngressSubnetNonoverlap(proposed netip.Prefix, snapshot IPv4NetworkSnapshot) error {
	if !isCanonicalPrivateIPv4Prefix(proposed) {
		return fmt.Errorf("%w: %v", ErrInvalidIngressSubnet, proposed)
	}
	if err := validateIPv4NetworkSnapshot(snapshot); err != nil {
		return err
	}
	for _, route := range snapshot.Routes {
		if route.Bits() == 0 {
			continue
		}
		if prefixesOverlap(proposed, route) {
			return fmt.Errorf("%w: proposed %s and route %s", ErrIngressSubnetOverlap, proposed, route)
		}
	}
	for _, assigned := range snapshot.InterfacePrefixes {
		if prefixesOverlap(proposed, assigned) {
			return fmt.Errorf("%w: proposed %s and interface prefix %s", ErrIngressSubnetOverlap, proposed, assigned)
		}
	}
	return nil
}

type routeInterfaceSnapshot struct {
	index     int
	addresses []net.Addr
}

func currentIPv4InterfacePrefixes() ([]netip.Prefix, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	snapshots := make([]routeInterfaceSnapshot, 0, len(interfaces))
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("interface %d: %w", iface.Index, err)
		}
		snapshots = append(snapshots, routeInterfaceSnapshot{index: iface.Index, addresses: addresses})
	}
	return interfacePrefixesFromSnapshots(snapshots)
}

func interfacePrefixesFromSnapshots(snapshots []routeInterfaceSnapshot) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, snapshot := range snapshots {
		if snapshot.index <= 0 {
			return nil, fmt.Errorf("%w: invalid interface index %d", ErrIncompleteHostSnapshot, snapshot.index)
		}
		for _, value := range snapshot.addresses {
			network, ok := value.(*net.IPNet)
			if !ok || network == nil || network.IP == nil {
				return nil, fmt.Errorf("%w: interface %d returned an invalid address", ErrIncompleteHostSnapshot, snapshot.index)
			}
			addressBytes := network.IP.To4()
			if addressBytes == nil {
				continue
			}
			bits, width := network.Mask.Size()
			if width == 128 {
				// IPv4-mapped IPv6 addresses are IPv6 assignments even though
				// net.IP.To4 can represent their final four bytes.
				continue
			}
			if width != 32 || bits < 0 || bits > 32 {
				return nil, fmt.Errorf("%w: interface %d returned an invalid IPv4 mask", ErrIncompleteHostSnapshot, snapshot.index)
			}
			address := netip.AddrFrom4([4]byte{addressBytes[0], addressBytes[1], addressBytes[2], addressBytes[3]})
			prefixes = append(prefixes, netip.PrefixFrom(address, bits).Masked())
		}
	}
	sortPrefixes(prefixes)
	return prefixes, nil
}

func validateIPv4NetworkSnapshot(snapshot IPv4NetworkSnapshot) error {
	if !snapshot.complete {
		return ErrIncompleteHostSnapshot
	}
	for _, group := range []struct {
		name     string
		prefixes []netip.Prefix
	}{
		{name: "route", prefixes: snapshot.Routes},
		{name: "interface prefix", prefixes: snapshot.InterfacePrefixes},
	} {
		for _, prefix := range group.prefixes {
			if !prefix.IsValid() || !prefix.Addr().Is4() || prefix != prefix.Masked() {
				return fmt.Errorf("%w: invalid %s %v", ErrIncompleteHostSnapshot, group.name, prefix)
			}
		}
	}
	return nil
}

func isCanonicalPrivateIPv4Prefix(prefix netip.Prefix) bool {
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix != prefix.Masked() {
		return false
	}
	for _, private := range privateIPv4Prefixes {
		if prefix.Bits() >= private.Bits() && private.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

var privateIPv4Prefixes = [...]netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

func prefixesOverlap(left, right netip.Prefix) bool {
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func sortPrefixes(prefixes []netip.Prefix) {
	sort.Slice(prefixes, func(i, j int) bool {
		if comparison := prefixes[i].Addr().Compare(prefixes[j].Addr()); comparison != 0 {
			return comparison < 0
		}
		return prefixes[i].Bits() < prefixes[j].Bits()
	})
}
