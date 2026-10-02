package generatedingress

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"sort"
	"strings"
)

const gatewayV2NetworkPrefixBits = 28

var gatewayV2PrivateNetworkPools = [...]netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

type gatewayV2HostNetworkSnapshot struct {
	Routes     []netip.Prefix
	Interfaces []netip.Prefix
}

type gatewayV2NetworkPlanReads struct {
	candidates func() ([]hostNetworkCandidate, error)
	host       func() (gatewayV2HostNetworkSnapshot, error)
	docker     func(context.Context) ([]netip.Prefix, error)
}

// hostNetworkCandidate is the narrow projection needed by the network
// planner. The production adapter converts hostnetwork.Candidate values so
// plan-selection tests cannot manufacture a hostnetwork completion marker.
type hostNetworkCandidate struct {
	InterfaceID string
	IPv4        string
	Prefix      netip.Prefix
}

type gatewayV2DockerInventory interface {
	listNetworkIDs(context.Context) ([]string, error)
	inspectNetworkPrefixes(context.Context, string) ([]netip.Prefix, error)
}

type managerGatewayV2DockerInventory struct{ manager *Manager }

func (d managerGatewayV2DockerInventory) listNetworkIDs(ctx context.Context) ([]string, error) {
	if d.manager == nil || ctx == nil {
		return nil, errors.New("generated ingress Docker inventory is unavailable")
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, "network", "ls", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	defer clearResult(&result)
	return parseGatewayV2DockerNetworkIDs(result.Stdout)
}

func (d managerGatewayV2DockerInventory) inspectNetworkPrefixes(ctx context.Context, id string) ([]netip.Prefix, error) {
	if d.manager == nil || ctx == nil || !validContainerID(id) || normalizeID(id) != id {
		return nil, errors.New("invalid generated ingress Docker network identity")
	}
	network, observedID, found, err := d.manager.inspectNamedGatewayNetwork(ctx, id)
	defer clearCaddyNetworkInspection(&network)
	if err != nil || !found || normalizeID(observedID) != id {
		return nil, errors.New("generated ingress Docker network inventory changed")
	}
	return gatewayV2IPv4PrefixesFromNetwork(network)
}

func gatewayV2IPv4PrefixesFromNetwork(network caddyNetworkInspection) ([]netip.Prefix, error) {
	prefixes, err := gatewayV2IPv4PrefixesFromIPAM(network.IPAM.Config)
	if err != nil {
		return nil, err
	}
	if len(prefixes) != 0 {
		return prefixes, nil
	}
	// Docker's built-in host and none networks have no allocated subnet. A
	// bridge or other address-bearing network without IPv4 IPAM is unknown,
	// not evidence that its address space is free.
	builtInWithoutIPAM := len(network.IPAM.Config) == 0 && network.Scope == "local" &&
		((network.Name == "host" && network.Driver == "host") ||
			(network.Name == "none" && network.Driver == "null"))
	if !builtInWithoutIPAM {
		return nil, errors.New("generated ingress Docker network has unknown IPv4 address space")
	}
	return []netip.Prefix{}, nil
}

func gatewayV2IPv4PrefixesFromIPAM(configs []networkIPAM) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(configs))
	for _, config := range configs {
		if config.Subnet == "" {
			return nil, errors.New("generated ingress Docker network has incomplete IPAM")
		}
		prefix, parseErr := netip.ParsePrefix(config.Subnet)
		if parseErr != nil || !prefix.IsValid() || prefix != prefix.Masked() {
			return nil, errors.New("generated ingress Docker network has invalid IPAM")
		}
		if prefix.Addr().Is4() {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes, nil
}

func parseGatewayV2DockerNetworkIDs(output []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return []string{}, nil
	}
	lines := bytes.Split(trimmed, []byte{'\n'})
	ids := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		id := strings.TrimSpace(string(line))
		if !validContainerID(id) || normalizeID(id) != id {
			return nil, errors.New("generated ingress Docker network list is invalid")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("generated ingress Docker network list contains duplicates")
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func readStableGatewayV2DockerNetworkPrefixes(ctx context.Context, inventory gatewayV2DockerInventory) ([]netip.Prefix, error) {
	if ctx == nil || inventory == nil {
		return nil, errors.New("generated ingress Docker inventory reader is invalid")
	}
	before, err := inventory.listNetworkIDs(ctx)
	if err != nil {
		return nil, err
	}
	prefixes := make([]netip.Prefix, 0, len(before))
	for _, id := range before {
		values, inspectErr := inventory.inspectNetworkPrefixes(ctx, id)
		if inspectErr != nil {
			return nil, inspectErr
		}
		prefixes = append(prefixes, values...)
	}
	after, err := inventory.listNetworkIDs(ctx)
	if err != nil || !equalStrings(before, after) {
		return nil, errors.New("generated ingress Docker network inventory is unstable")
	}
	return prefixes, nil
}

func selectGatewayV2NetworkPlan(ctx context.Context, profile gatewayProfileBinding, reads gatewayV2NetworkPlanReads) (gatewayV2NetworkPlan, error) {
	blocked, err := readGatewayV2BlockedPrefixes(ctx, profile, reads)
	if err != nil {
		return gatewayV2NetworkPlan{}, err
	}
	prefix, ok := firstAvailableGatewayV2Prefix(blocked)
	if !ok {
		return gatewayV2NetworkPlan{}, &Error{Code: DiagnosticIngressDrift}
	}
	base := ipv4ToUint32(prefix.Addr())
	plan := gatewayV2NetworkPlan{
		Subnet:        prefix.String(),
		GatewayIPv4:   uint32ToIPv4(base + 1).String(),
		ContainerIPv4: uint32ToIPv4(base + 2).String(),
	}
	if !validGatewayV2NetworkPlan(plan) || !gatewayV2NetworkExcludesSelectedLAN(profile, plan) {
		return gatewayV2NetworkPlan{}, &Error{Code: DiagnosticIngressDrift}
	}
	return plan, nil
}

func readGatewayV2BlockedPrefixes(ctx context.Context, profile gatewayProfileBinding, reads gatewayV2NetworkPlanReads) ([]netip.Prefix, error) {
	if ctx == nil || !validGatewayProfileBinding(profile) || reads.candidates == nil || reads.host == nil || reads.docker == nil {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	candidates, err := reads.candidates()
	selectedPrefix, selectedOK := selectGatewayV2Candidate(candidates, profile.InterfaceID, profile.SelectedIPv4)
	if err != nil || !selectedOK {
		return nil, &Error{Code: DiagnosticIngressDrift}
	}
	host, err := reads.host()
	if err != nil || !validGatewayV2HostSnapshot(host) {
		return nil, &Error{Code: DiagnosticIngressDrift}
	}
	docker, err := reads.docker(ctx)
	if err != nil || !validGatewayV2PrefixInventory(docker) {
		return nil, &Error{Code: DiagnosticIngressDrift}
	}
	selected, err := netip.ParseAddr(profile.SelectedIPv4)
	if err != nil || !selected.Is4() {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	blocked := make([]netip.Prefix, 0, len(host.Routes)+len(host.Interfaces)+len(docker)+1)
	for _, route := range host.Routes {
		if route.Bits() != 0 {
			blocked = append(blocked, route)
		}
	}
	blocked = append(blocked, host.Interfaces...)
	blocked = append(blocked, docker...)
	blocked = append(blocked, selectedPrefix)
	return blocked, nil
}

func validateGatewayV2NetworkPlanAgainstReads(ctx context.Context, profile gatewayProfileBinding, plan gatewayV2NetworkPlan, reads gatewayV2NetworkPlanReads) error {
	if !validGatewayV2NetworkPlan(plan) || !gatewayV2NetworkExcludesSelectedLAN(profile, plan) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	blocked, err := readGatewayV2BlockedPrefixes(ctx, profile, reads)
	if err != nil {
		return err
	}
	planned := netip.MustParsePrefix(plan.Subnet)
	for _, prefix := range blocked {
		if gatewayV2PrefixesOverlap(planned, prefix) {
			return &Error{Code: DiagnosticIngressDrift}
		}
	}
	return nil
}

func validGatewayV2HostSnapshot(snapshot gatewayV2HostNetworkSnapshot) bool {
	return validGatewayV2PrefixInventory(snapshot.Routes) && validGatewayV2PrefixInventory(snapshot.Interfaces)
}

func validGatewayV2PrefixInventory(prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if !prefix.IsValid() || !prefix.Addr().Is4() || prefix != prefix.Masked() {
			return false
		}
	}
	return true
}

func selectGatewayV2Candidate(candidates []hostNetworkCandidate, interfaceID, ipv4 string) (netip.Prefix, bool) {
	address, err := netip.ParseAddr(ipv4)
	if err != nil || !address.Is4() {
		return netip.Prefix{}, false
	}
	matched := false
	var selectedPrefix netip.Prefix
	owners := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.IPv4 != ipv4 || candidate.InterfaceID == "" {
			continue
		}
		if !candidate.Prefix.IsValid() || !candidate.Prefix.Addr().Is4() ||
			candidate.Prefix != candidate.Prefix.Masked() || !candidate.Prefix.Contains(address) {
			return netip.Prefix{}, false
		}
		owners[candidate.InterfaceID] = struct{}{}
		if candidate.InterfaceID == interfaceID {
			if matched && selectedPrefix != candidate.Prefix {
				return netip.Prefix{}, false
			}
			selectedPrefix = candidate.Prefix
			matched = true
		}
	}
	return selectedPrefix, matched && len(owners) == 1
}

type gatewayV2IPv4Interval struct{ first, last uint32 }

func firstAvailableGatewayV2Prefix(blocked []netip.Prefix) (netip.Prefix, bool) {
	for _, pool := range gatewayV2PrivateNetworkPools {
		poolFirst := ipv4ToUint32(pool.Addr())
		poolLast := ipv4ToUint32(lastIPv4Address(pool))
		intervals := make([]gatewayV2IPv4Interval, 0, len(blocked))
		for _, prefix := range blocked {
			if !prefix.IsValid() || !prefix.Addr().Is4() || prefix != prefix.Masked() {
				return netip.Prefix{}, false
			}
			first := ipv4ToUint32(prefix.Addr())
			last := ipv4ToUint32(lastIPv4Address(prefix))
			if last < poolFirst || first > poolLast {
				continue
			}
			if first < poolFirst {
				first = poolFirst
			}
			if last > poolLast {
				last = poolLast
			}
			intervals = append(intervals, gatewayV2IPv4Interval{first: first, last: last})
		}
		sort.Slice(intervals, func(i, j int) bool {
			if intervals[i].first != intervals[j].first {
				return intervals[i].first < intervals[j].first
			}
			return intervals[i].last < intervals[j].last
		})
		cursor := poolFirst
		for _, interval := range intervals {
			if interval.last < cursor {
				continue
			}
			if interval.first > cursor && uint64(interval.first)-uint64(cursor) >= 16 {
				return netip.PrefixFrom(uint32ToIPv4(cursor), gatewayV2NetworkPrefixBits), true
			}
			if interval.last == ^uint32(0) {
				cursor = interval.last
				break
			}
			cursor = alignGatewayV2Prefix(interval.last + 1)
			if cursor > poolLast {
				break
			}
		}
		if cursor <= poolLast && uint64(poolLast)-uint64(cursor)+1 >= 16 {
			return netip.PrefixFrom(uint32ToIPv4(cursor), gatewayV2NetworkPrefixBits), true
		}
	}
	return netip.Prefix{}, false
}

func alignGatewayV2Prefix(value uint32) uint32 { return (value + 15) &^ uint32(15) }

func ipv4ToUint32(address netip.Addr) uint32 {
	value := address.As4()
	return binary.BigEndian.Uint32(value[:])
}

func uint32ToIPv4(value uint32) netip.Addr {
	var address [4]byte
	binary.BigEndian.PutUint32(address[:], value)
	return netip.AddrFrom4(address)
}

func gatewayV2PrefixesOverlap(left, right netip.Prefix) bool {
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
