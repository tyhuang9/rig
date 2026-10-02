package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2NetworkPlanSelectsFirstAlignedGapDeterministically(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	events := make([]string, 0, 3)
	reads := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			events = append(events, "interfaces")
			return []hostNetworkCandidate{gatewayV2TestCandidate(input.Profile)}, nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			events = append(events, "host")
			return gatewayV2HostNetworkSnapshot{
				Routes:     []netip.Prefix{netip.MustParsePrefix("10.0.0.0/28")},
				Interfaces: []netip.Prefix{netip.MustParsePrefix("10.0.0.16/28")},
			}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			events = append(events, "docker")
			return []netip.Prefix{netip.MustParsePrefix("10.0.0.32/28")}, nil
		},
	}
	plan, err := selectGatewayV2NetworkPlan(context.Background(), input.Profile, reads)
	if err != nil {
		t.Fatal(err)
	}
	want := gatewayV2NetworkPlan{Subnet: "10.0.0.48/28", GatewayIPv4: "10.0.0.49", ContainerIPv4: "10.0.0.50"}
	if plan != want || !reflect.DeepEqual(events, []string{"interfaces", "host", "docker"}) {
		t.Fatalf("plan=%+v events=%v, want %+v and ordered reads", plan, events, want)
	}

	// Input order cannot change the selected gap.
	blocked := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.32/28"),
		netip.MustParsePrefix("10.0.0.16/28"),
		netip.MustParsePrefix("10.0.0.0/28"),
	}
	prefix, ok := firstAvailableGatewayV2Prefix(blocked)
	if !ok || prefix.String() != want.Subnet {
		t.Fatalf("permuted blocked prefixes selected %v, ok=%t", prefix, ok)
	}
}

func TestGatewayV2NetworkPlanExcludesSelectedLANPrefixEvenWithEmptyHostSnapshot(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	input.Profile.SelectedIPv4 = "10.0.0.5"
	digest, err := appaccess.GatewayProfileSpecDigest(appaccess.GatewayProfileSpec{
		SelectedIPv4: input.Profile.SelectedIPv4,
		InterfaceID:  input.Profile.InterfaceID,
		PortStart:    input.Profile.PortStart,
		PortEnd:      input.Profile.PortEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Profile.SpecDigest = digest
	reads := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return []hostNetworkCandidate{gatewayV2TestCandidate(input.Profile)}, nil
		},
		host:   func() (gatewayV2HostNetworkSnapshot, error) { return gatewayV2HostNetworkSnapshot{}, nil },
		docker: func(context.Context) ([]netip.Prefix, error) { return nil, nil },
	}
	plan, err := selectGatewayV2NetworkPlan(context.Background(), input.Profile, reads)
	if err != nil || plan.Subnet != "10.0.1.0/28" {
		t.Fatalf("selected-LAN exclusion plan=%+v err=%v", plan, err)
	}
}

func TestGatewayV2NetworkPlanSkipsLargeIntervalsAndReportsExhaustion(t *testing.T) {
	blocked := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/9"),
		netip.MustParsePrefix("10.128.0.0/10"),
		netip.MustParsePrefix("10.192.0.0/11"),
	}
	prefix, ok := firstAvailableGatewayV2Prefix(blocked)
	if !ok || prefix.String() != "10.224.0.0/28" {
		t.Fatalf("large-interval selection=%v ok=%t", prefix, ok)
	}

	exhausted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	if prefix, ok := firstAvailableGatewayV2Prefix(exhausted); ok {
		t.Fatalf("exhausted RFC1918 space selected %v", prefix)
	}
}

func TestGatewayV2NetworkPlanSelectionFailsClosedOnIncompleteSnapshots(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	base := gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return []hostNetworkCandidate{gatewayV2TestCandidate(input.Profile)}, nil
		},
		host:   func() (gatewayV2HostNetworkSnapshot, error) { return gatewayV2HostNetworkSnapshot{}, nil },
		docker: func(context.Context) ([]netip.Prefix, error) { return nil, nil },
	}
	tests := []struct {
		name   string
		mutate func(*gatewayV2NetworkPlanReads)
	}{
		{name: "ambiguous selected address", mutate: func(reads *gatewayV2NetworkPlanReads) {
			reads.candidates = func() ([]hostNetworkCandidate, error) {
				return []hostNetworkCandidate{
					gatewayV2TestCandidate(input.Profile),
					{InterfaceID: "replacement", IPv4: input.Profile.SelectedIPv4, Prefix: gatewayV2TestCandidate(input.Profile).Prefix},
				}, nil
			}
		}},
		{name: "invalid selected prefix", mutate: func(reads *gatewayV2NetworkPlanReads) {
			reads.candidates = func() ([]hostNetworkCandidate, error) {
				candidate := gatewayV2TestCandidate(input.Profile)
				candidate.Prefix = netip.MustParsePrefix("10.0.0.0/24")
				return []hostNetworkCandidate{candidate}, nil
			}
		}},
		{name: "host read error", mutate: func(reads *gatewayV2NetworkPlanReads) {
			reads.host = func() (gatewayV2HostNetworkSnapshot, error) {
				return gatewayV2HostNetworkSnapshot{}, errors.New("host")
			}
		}},
		{name: "invalid host prefix", mutate: func(reads *gatewayV2NetworkPlanReads) {
			reads.host = func() (gatewayV2HostNetworkSnapshot, error) {
				return gatewayV2HostNetworkSnapshot{Routes: []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr("10.0.0.1"), 24)}}, nil
			}
		}},
		{name: "Docker read error", mutate: func(reads *gatewayV2NetworkPlanReads) {
			reads.docker = func(context.Context) ([]netip.Prefix, error) { return nil, errors.New("docker") }
		}},
		{name: "invalid Docker prefix", mutate: func(reads *gatewayV2NetworkPlanReads) {
			reads.docker = func(context.Context) ([]netip.Prefix, error) { return []netip.Prefix{{}}, nil }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reads := base
			test.mutate(&reads)
			if _, err := selectGatewayV2NetworkPlan(context.Background(), input.Profile, reads); !IsCode(err, DiagnosticIngressDrift) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestStableGatewayV2DockerInventoryUsesImmutableIDsAndRejectsDrift(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	inventory := &fakeGatewayV2DockerInventory{
		lists: [][]string{{first, second}, {first, second}},
		prefixes: map[string][]netip.Prefix{
			first:  {netip.MustParsePrefix("172.17.0.0/16")},
			second: {netip.MustParsePrefix("192.168.100.0/24")},
		},
	}
	prefixes, err := readStableGatewayV2DockerNetworkPrefixes(context.Background(), inventory)
	if err != nil || len(prefixes) != 2 {
		t.Fatalf("stable inventory prefixes=%v err=%v", prefixes, err)
	}
	wantEvents := []string{"list", "inspect:" + first, "inspect:" + second, "list"}
	if !reflect.DeepEqual(inventory.events, wantEvents) {
		t.Fatalf("inventory events=%v, want %v", inventory.events, wantEvents)
	}

	drifted := &fakeGatewayV2DockerInventory{
		lists:    [][]string{{first}, {second}},
		prefixes: map[string][]netip.Prefix{first: {netip.MustParsePrefix("172.17.0.0/16")}},
	}
	if _, err := readStableGatewayV2DockerNetworkPrefixes(context.Background(), drifted); err == nil {
		t.Fatal("unstable Docker inventory was accepted")
	}

	incomplete := &fakeGatewayV2DockerInventory{lists: [][]string{{first}}, inspectErr: errors.New("inspect failed")}
	if _, err := readStableGatewayV2DockerNetworkPrefixes(context.Background(), incomplete); err == nil {
		t.Fatal("incomplete Docker inventory was accepted")
	}
}

func TestParseGatewayV2DockerNetworkIDsRequiresCompleteCanonicalSet(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	ids, err := parseGatewayV2DockerNetworkIDs([]byte(second + "\r\n" + first + "\n"))
	if err != nil || !reflect.DeepEqual(ids, []string{first, second}) {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	for _, value := range [][]byte{
		[]byte("short\n"),
		[]byte(first + "\n" + first + "\n"),
		[]byte("sha256:" + first + "\n"),
	} {
		if _, err := parseGatewayV2DockerNetworkIDs(value); err == nil {
			t.Fatalf("invalid network IDs accepted: %q", value)
		}
	}
}

func TestGatewayV2DockerIPAMInventoryRejectsMalformedEntries(t *testing.T) {
	prefixes, err := gatewayV2IPv4PrefixesFromIPAM([]networkIPAM{
		{Subnet: "fd00::/64"},
		{Subnet: "172.17.0.0/16"},
	})
	if err != nil || !reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("172.17.0.0/16")}) {
		t.Fatalf("valid IPAM prefixes=%v err=%v", prefixes, err)
	}
	for _, configs := range [][]networkIPAM{
		{{Subnet: ""}},
		{{Subnet: "not-a-prefix"}},
		{{Subnet: "10.0.0.1/24"}},
	} {
		if _, err := gatewayV2IPv4PrefixesFromIPAM(configs); err == nil {
			t.Fatalf("malformed IPAM accepted: %+v", configs)
		}
	}
}

func TestGatewayV2DockerNetworkInventoryAllowsOnlyBuiltinsWithoutIPv4IPAM(t *testing.T) {
	for _, network := range []caddyNetworkInspection{
		{Name: "host", Driver: "host", Scope: "local"},
		{Name: "none", Driver: "null", Scope: "local"},
	} {
		prefixes, err := gatewayV2IPv4PrefixesFromNetwork(network)
		if err != nil || len(prefixes) != 0 {
			t.Fatalf("built-in %q prefixes=%v err=%v", network.Name, prefixes, err)
		}
	}
	for _, network := range []caddyNetworkInspection{
		{Name: "external", Driver: "bridge", Scope: "local"},
		{Name: "external", Driver: "macvlan", Scope: "local"},
		{Name: "host", Driver: "bridge", Scope: "local"},
		{Name: "host", Driver: "host", Scope: "swarm"},
		{Name: "none", Driver: "null", Scope: "local", IPAM: caddyNetworkIPAM{Config: []networkIPAM{{Subnet: "fd00::/64"}}}},
	} {
		if _, err := gatewayV2IPv4PrefixesFromNetwork(network); err == nil {
			t.Fatalf("unclassifiable Docker network was accepted: %+v", network)
		}
	}
}

type fakeGatewayV2DockerInventory struct {
	lists      [][]string
	prefixes   map[string][]netip.Prefix
	inspectErr error
	events     []string
}

func (f *fakeGatewayV2DockerInventory) listNetworkIDs(context.Context) ([]string, error) {
	f.events = append(f.events, "list")
	if len(f.lists) == 0 {
		return nil, errors.New("unexpected list")
	}
	result := append([]string(nil), f.lists[0]...)
	f.lists = f.lists[1:]
	return result, nil
}

func (f *fakeGatewayV2DockerInventory) inspectNetworkPrefixes(_ context.Context, id string) ([]netip.Prefix, error) {
	f.events = append(f.events, "inspect:"+id)
	if f.inspectErr != nil {
		return nil, f.inspectErr
	}
	values, ok := f.prefixes[id]
	if !ok {
		return nil, errors.New("missing network")
	}
	return append([]netip.Prefix(nil), values...), nil
}

var _ gatewayV2DockerInventory = (*fakeGatewayV2DockerInventory)(nil)

func gatewayV2TestCandidate(profile gatewayProfileBinding) hostNetworkCandidate {
	address := netip.MustParseAddr(profile.SelectedIPv4)
	return hostNetworkCandidate{
		InterfaceID: profile.InterfaceID,
		IPv4:        profile.SelectedIPv4,
		Prefix:      netip.PrefixFrom(address, 24).Masked(),
	}
}
