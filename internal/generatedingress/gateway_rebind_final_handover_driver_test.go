package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindFinalHandoverWithdrawalRequiresExplicitRefusal(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", syscall.ECONNREFUSED, true},
		{"wrapped refusal", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"timeout", syscall.ETIMEDOUT, false}, {"denied", syscall.EACCES, false},
		{"unreachable", syscall.ENETUNREACH, false}, {"cancelled", context.Canceled, false},
		{"unknown", errors.New("unavailable"), false}, {"no result", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				calls++
				if network != "tcp4" || address != "192.168.1.10:18080" {
					t.Fatalf("wrong local probe target: %s %s", network, address)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("negative probe has no bounded deadline")
				}
				return nil, test.err
			}
			if got := gatewayRebindHandoverListenerAbsentWithDial(context.Background(), "192.168.1.10", 18080, dial); got != test.want || calls != 1 {
				t.Fatalf("absence=%t want=%t calls=%d", got, test.want, calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if gatewayRebindHandoverListenerAbsentWithDial(ctx, "127.0.0.1", 18080, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("cancelled observation dialed")
		return nil, nil
	}) {
		t.Fatal("cancelled observation accepted")
	}
	for _, address := range []string{"", "0.0.0.0", "8.8.8.8", "::1", "host.invalid"} {
		if gatewayRebindHandoverListenerAbsentWithDial(context.Background(), address, 18080, func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("unapproved address dialed")
			return nil, nil
		}) {
			t.Fatal("unapproved address accepted")
		}
	}
	left, right := net.Pipe()
	defer right.Close()
	if gatewayRebindHandoverListenerAbsentWithDial(context.Background(), "127.0.0.1", 18080, func(context.Context, string, string) (net.Conn, error) { return left, nil }) {
		t.Fatal("connected listener claimed absent")
	}
}

func TestGatewayRebindFinalHandoverApplicationProofRequiresExactJointRoster(t *testing.T) {
	const networkName = "net-a"
	for _, finalRunning := range []bool{false, true} {
		for _, drift := range []string{"none", "extra member", "missing app", "replacement network", "wrong owner", "stopped gateway member", "stolen alias", "unhealthy"} {
			t.Run(strconv.FormatBool(finalRunning)+"/"+drift, func(t *testing.T) {
				routes, networks, endpoints := gatewayV2EndpointIdentityFixture()
				var appID string
				for id := range routes {
					appID = id
				}
				appContainerID, oldID, finalID, networkID := strings.Repeat("1", 64), strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64)
				oldName, finalName := "old-gateway", "new-gateway"
				network := networks[networkName]
				network.Driver, network.Scope = "bridge", "local"
				network.Labels = map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": appID}
				network.Containers = map[string]caddyNetworkContainerInspection{appContainerID: {Name: "frontend", IPv4Address: "172.30.0.2/24"}}
				proof := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent, Final: gatewayRebindHandoverContainerStopped, FinalID: finalID, PredecessorRunning: !finalRunning}
				activeID, activeName := oldID, oldName
				if finalRunning {
					proof.Final = gatewayRebindHandoverContainerRunning
					activeID, activeName = finalID, finalName
				}
				network.Containers[activeID] = caddyNetworkContainerInspection{Name: activeName, IPv4Address: "172.30.0.4/24"}
				endpoints[activeID] = endpointInspection{ID: activeID, Running: true, Networks: map[string]*networkAttachment{networkName: {Aliases: []string{activeName}, IPAddress: "172.30.0.4"}}}
				value := gatewayRebindFinalHandoverContext{SequenceTwelve: gatewayRebindProgressRecord{Stage: &gatewayRebindStageIntent{FinalConfigIntent: &gatewayRebindFinalConfigIntentBinding{RoutePlan: gatewayRebindFinalConfigRoutePlan{Routes: []gatewayRebindFinalConfigRouteBinding{{AppID: appID, Route: routes[appID]}}}}}}}
				value.Predecessor.Journal.Resources.FinalContainerID = oldID
				physical := gatewayRebindHandoverPhysical{Final: caddyInspection{ID: finalID, Name: "/" + finalName, Networks: map[string]*networkAttachment{}}, FinalRuntime: gatewayContainerRuntime{ConfiguredNetworks: map[string]gatewayV2ConfiguredNetwork{networkName: {NetworkID: networkID}}}}
				if finalRunning {
					physical.Final.Networks[networkName] = &networkAttachment{IPAddress: "172.30.0.4"}
				}
				actualID := networkID
				switch drift {
				case "extra member":
					network.Containers[strings.Repeat("7", 64)] = caddyNetworkContainerInspection{Name: "foreign", IPv4Address: "172.30.0.7/24"}
				case "missing app":
					delete(network.Containers, appContainerID)
				case "replacement network":
					actualID = strings.Repeat("8", 64)
				case "wrong owner":
					network.Labels["io.rig.application"] = "foreign"
				case "stopped gateway member":
					stopped := finalID
					if finalRunning {
						stopped = oldID
					}
					network.Containers[stopped] = caddyNetworkContainerInspection{Name: "stopped", IPv4Address: "172.30.0.8/24"}
				case "stolen alias":
					endpoints[activeID].Networks[networkName].Aliases = []string{"frontend-blue"}
				case "unhealthy":
					endpoint := endpoints[appContainerID]
					endpoint.Health = "unhealthy"
					endpoints[appContainerID] = endpoint
				}
				physical.Predecessor.ApplicationNetworks = map[string]caddyNetworkInspection{networkName: network}
				physical.Predecessor.ApplicationNetworkIDs = map[string]string{networkName: networkID}
				runner := &gatewayRebindHandoverApplicationRunner{networks: map[string]caddyNetworkInspection{networkName: network}, ids: map[string]string{networkName: actualID}, endpoints: endpoints}
				driver := newManagerGatewayRebindFinalHandoverDriver(&Manager{runner: runner, options: Options{DockerExecutable: "docker"}}, gatewayRebindSuccessorPreflightReads{}, nil)
				bindings, _, digest, err := driver.handoverApplicationProof(context.Background(), value, physical, proof)
				if drift == "none" {
					if err != nil || !validSHA256(digest) || len(bindings) != 1 || bindings[0].ID != networkID {
						t.Fatalf("valid joint roster rejected: %v", err)
					}
				} else if err == nil {
					t.Fatal("unproven joint membership accepted")
				}
			})
		}
	}
}

type gatewayRebindHandoverApplicationRunner struct {
	networks  map[string]caddyNetworkInspection
	ids       map[string]string
	endpoints map[string]endpointInspection
}

func (r *gatewayRebindHandoverApplicationRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) != 5 || args[1] != "inspect" {
		return runtimeprocess.CommandResult{}, errors.New("unexpected mutation or inventory command")
	}
	var body []byte
	var err error
	switch args[0] {
	case "network":
		n := r.networks[args[4]]
		body, err = json.Marshal([]gatewayNetworkIdentityInspection{{ID: r.ids[args[4]], Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, Options: n.Options, IPAM: n.IPAM, Labels: n.Labels, Containers: n.Containers}})
	case "container":
		if args[3] != endpointInspectFormat {
			return runtimeprocess.CommandResult{}, errors.New("unexpected container inspection")
		}
		body, err = json.Marshal(r.endpoints[normalizeID(args[4])])
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected command")
	}
	return runtimeprocess.CommandResult{Stdout: body}, err
}

func TestGatewayRebindFinalHandoverHostProofRejectsChangedOrUnreadableInventories(t *testing.T) {
	value, _ := newGatewayRebindFinalHandoverPlanTestContext(t)
	makeReads := func() gatewayRebindSuccessorPreflightReads {
		prefixes := func(values []string) []netip.Prefix {
			result := []netip.Prefix{}
			for _, text := range values {
				result = append(result, netip.MustParsePrefix(text))
			}
			return result
		}
		return gatewayRebindSuccessorPreflightReads{
			network: gatewayV2NetworkPlanReads{
				candidates: func() ([]hostNetworkCandidate, error) {
					result := []hostNetworkCandidate{}
					for _, c := range value.Intent.NetworkObservation.Candidates {
						result = append(result, hostNetworkCandidate{InterfaceID: c.InterfaceID, IPv4: c.IPv4, Prefix: netip.MustParsePrefix(c.Prefix)})
					}
					return result, nil
				},
				host: func() (gatewayV2HostNetworkSnapshot, error) {
					return gatewayV2HostNetworkSnapshot{Routes: prefixes(value.Intent.NetworkObservation.HostRoutes), Interfaces: prefixes(value.Intent.NetworkObservation.HostInterfaces)}, nil
				},
				docker: func(context.Context) ([]netip.Prefix, error) {
					return append(prefixes(value.Intent.NetworkObservation.DockerPrefixes), netip.MustParsePrefix(value.Intent.Intent.Network.Subnet)), nil
				},
			},
			dockerIDs: func(context.Context) ([]string, error) {
				ids := append(append([]string{}, value.Intent.NetworkObservation.DockerNetworkIDs...), value.SequenceTwelve.Stage.Network.ID)
				sort.Strings(ids)
				return ids, nil
			},
		}
	}
	for _, drift := range []string{"none", "candidate read", "host read", "ID read", "prefix read", "selected missing", "selected duplicate", "route drift", "ID drift", "prefix drift"} {
		t.Run(drift, func(t *testing.T) {
			reads := makeReads()
			switch drift {
			case "candidate read":
				reads.network.candidates = func() ([]hostNetworkCandidate, error) { return nil, errors.New("unreadable") }
			case "host read":
				reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
					return gatewayV2HostNetworkSnapshot{}, errors.New("unreadable")
				}
			case "ID read":
				reads.dockerIDs = func(context.Context) ([]string, error) { return nil, errors.New("unreadable") }
			case "prefix read":
				reads.network.docker = func(context.Context) ([]netip.Prefix, error) { return nil, errors.New("unreadable") }
			case "selected missing":
				reads.network.candidates = func() ([]hostNetworkCandidate, error) { return []hostNetworkCandidate{}, nil }
			case "selected duplicate":
				original := reads.network.candidates
				reads.network.candidates = func() ([]hostNetworkCandidate, error) { v, e := original(); return append(v, v[0]), e }
			case "route drift":
				original := reads.network.host
				reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
					v, e := original()
					v.Routes = append(v.Routes, netip.MustParsePrefix("100.64.0.0/16"))
					return v, e
				}
			case "ID drift":
				original := reads.dockerIDs
				reads.dockerIDs = func(ctx context.Context) ([]string, error) {
					v, e := original(ctx)
					v = append(v, strings.Repeat("7", 64))
					sort.Strings(v)
					return v, e
				}
			case "prefix drift":
				original := reads.network.docker
				reads.network.docker = func(ctx context.Context) ([]netip.Prefix, error) {
					v, e := original(ctx)
					return append(v, netip.MustParsePrefix("100.64.0.0/16")), e
				}
			}
			driver := newManagerGatewayRebindFinalHandoverDriver(nil, reads, nil)
			digest, address, err := driver.handoverHostProof(context.Background(), value, true)
			if drift == "none" {
				if err != nil || !validSHA256(digest) || (address != gatewayRebindPredecessorAddressPresent && address != gatewayRebindPredecessorAddressAbsent) {
					t.Fatalf("exact inventory rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("changed/unreadable inventory accepted")
			}
		})
	}
}

func TestGatewayRebindFinalHandoverDriverRequiresExactResourcesAndVolumeUsers(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(), fixture.predecessor.repository,
		reads, gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := fixture.predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	value := gatewayRebindFinalHandoverContext{Intent: fixture.intent, SequenceTwelve: history.Progress[11].Record, Predecessor: history.Predecessor, Source: source}
	observed, err := fake.inspect(context.Background(), fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	physical := gatewayRebindHandoverPhysical{Stage: observed}
	proof := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerRunning, Final: gatewayRebindHandoverContainerAbsent,
		ConfigVolumePresent: true, DataVolumePresent: true, IngressNetworkPresent: true}
	if !validGatewayRebindHandoverResources(value, physical, proof) {
		t.Fatal("exact bound resources rejected")
	}
	for name, mutate := range map[string]func(*gatewayRebindHandoverPhysical){
		"replacement network": func(p *gatewayRebindHandoverPhysical) { p.Stage.NetworkID = strings.Repeat("8", 64) },
		"replacement config":  func(p *gatewayRebindHandoverPhysical) { p.Stage.ConfigVolumeIdentity.CreatedAt += "changed" },
		"replacement data":    func(p *gatewayRebindHandoverPhysical) { p.Stage.DataVolumeIdentity.Mountpoint += "/foreign" },
		"foreign label": func(p *gatewayRebindHandoverPhysical) {
			p.Stage.ConfigVolume.Labels[gatewayV2OperationLabelKey] = "foreign"
		},
		"foreign owned resource": func(p *gatewayRebindHandoverPhysical) {
			p.Stage.OwnedContainers = append(p.Stage.OwnedContainers, "foreign")
		},
		"foreign network member": func(p *gatewayRebindHandoverPhysical) {
			p.Stage.Network.Containers[strings.Repeat("8", 64)] = caddyNetworkContainerInspection{Name: "foreign", IPv4Address: "10.0.0.9/28"}
		},
		"missing network member": func(p *gatewayRebindHandoverPhysical) { p.Stage.Network.Containers = nil },
		"foreign volume options": func(p *gatewayRebindHandoverPhysical) { p.Stage.DataVolume.Options = map[string]string{"device": "/"} },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(physical)
			var candidate gatewayRebindHandoverPhysical
			if err := json.Unmarshal(encoded, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if validGatewayRebindHandoverResources(value, candidate, proof) {
				t.Fatal("drift authorized resource mutation")
			}
		})
	}
	runner := &gatewayRebindHandoverInventoryRunner{output: []byte(value.SequenceTwelve.Stage.StageContainer.ID + "\n")}
	driver := newManagerGatewayRebindFinalHandoverDriver(&Manager{runner: runner, options: Options{DockerExecutable: "docker"}}, reads, nil)
	if !driver.handoverVolumeUsersExact(context.Background(), value, proof) || len(runner.requests) != 2 {
		t.Fatal("exact volume attachment inventory rejected")
	}
	for _, args := range runner.requests {
		if len(args) != 7 || !reflect.DeepEqual(args[:6], []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter"}) ||
			(args[6] != "volume="+value.Intent.Intent.Identity.ConfigVolume && args[6] != "volume="+value.Intent.Intent.Identity.DataVolume) {
			t.Fatal("volume census did not include stopped containers by exact volume name")
		}
	}
	for _, output := range []string{"", value.SequenceTwelve.Stage.StageContainer.ID + "\n" + strings.Repeat("8", 64), "short", strings.Repeat("8", 64)} {
		runner.output = []byte(output)
		if driver.handoverVolumeUsersExact(context.Background(), value, proof) {
			t.Fatal("missing or foreign volume user accepted")
		}
	}
	runner.output = []byte(value.SequenceTwelve.Stage.StageContainer.ID)
	runner.truncated = true
	if driver.handoverVolumeUsersExact(context.Background(), value, proof) {
		t.Fatal("truncated volume inventory accepted")
	}
}

type gatewayRebindHandoverInventoryRunner struct {
	output    []byte
	truncated bool
	requests  [][]string
}

func (r *gatewayRebindHandoverInventoryRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, append([]string{}, request.Args...))
	return runtimeprocess.CommandResult{Stdout: append([]byte{}, r.output...), StdoutTruncated: r.truncated}, nil
}

func TestGatewayRebindFinalHandoverWithdrawalAccountsForRetainedLoopbackAndAbsentOldNIC(t *testing.T) {
	value, _ := newGatewayRebindFinalHandoverPlanTestContext(t)
	value.Intent.Intent.SuccessorProfile.SelectedIPv4 = "192.168.1.20"
	value.Intent.Intent.SuccessorProfile.PortStart = 18080
	value.Intent.Intent.SuccessorProfile.PortEnd = 18081
	value.Predecessor.State.Profile.SelectedIPv4 = "192.168.1.10"
	value.Predecessor.State.Profile.PortStart = 18080
	value.Predecessor.State.Profile.PortEnd = 18081
	value.Predecessor.Journal.Source.LocalHostPort = 17000
	proof := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent, Final: gatewayRebindHandoverContainerStopped, PredecessorAddress: gatewayRebindPredecessorAddressPresent}
	var requested []string
	probe := func(_ context.Context, address string, port uint16) bool {
		requested = append(requested, address+":"+strconv.Itoa(int(port)))
		return true
	}
	if !proveGatewayRebindHandoverWithdrawnBindings(context.Background(), value, proof, probe) {
		t.Fatal("complete withdrawal rejected")
	}
	want := []string{"192.168.1.20:18080", "192.168.1.20:18081", "192.168.1.10:18080", "192.168.1.10:18081", "127.0.0.1:17000"}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("withdrawal probes=%v want=%v", requested, want)
	}
	for _, failed := range want {
		if proveGatewayRebindHandoverWithdrawnBindings(context.Background(), value, proof, func(_ context.Context, address string, port uint16) bool {
			return address+":"+strconv.Itoa(int(port)) != failed
		}) {
			t.Fatal("one unknown or live publication accepted")
		}
	}
	requested = nil
	proof.PredecessorAddress = gatewayRebindPredecessorAddressAbsent
	proof.Final = gatewayRebindHandoverContainerRunning
	if !proveGatewayRebindHandoverWithdrawnBindings(context.Background(), value, proof, probe) || len(requested) != 0 {
		t.Fatal("absent old interface or newly owned loopback was treated as an empty present listener")
	}
}

func TestGatewayRebindFinalHandoverPredecessorProjectionRetainsForeignMembersAndInput(t *testing.T) {
	oldID, finalID, foreignID := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	networks := map[string]caddyNetworkInspection{"app": {Containers: map[string]caddyNetworkContainerInspection{
		oldID: {Name: "old"}, finalID: {Name: "successor"}, foreignID: {Name: "foreign"}}}, "empty": {}}
	before, _ := json.Marshal(networks)
	projected := gatewayRebindWithoutFinalMember(networks, finalID)
	if len(projected["app"].Containers) != 2 || projected["app"].Containers[foreignID].Name != "foreign" || projected["app"].Containers[oldID].Name != "old" || projected["empty"].Containers != nil {
		t.Fatal("projection hid unrelated evidence")
	}
	after, _ := json.Marshal(networks)
	if string(before) != string(after) {
		t.Fatal("normalization changed raw network proof")
	}
	if gatewayRebindWithoutFinalMember(nil, finalID) != nil {
		t.Fatal("normalization changed nil map shape")
	}
}
