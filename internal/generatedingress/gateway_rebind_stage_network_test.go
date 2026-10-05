package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type gatewayRebindStageNetworkFake struct {
	intent       gatewayRebindProtectedIntent
	id           string
	found        bool
	foreign      bool
	extraConfig  bool
	extraVolume  bool
	imageID      string
	createID     string
	createErr    error
	createAbsent bool
	createCalls  int
	inspectCalls int
	onCreate     func()
}

func (f *gatewayRebindStageNetworkFake) inspect(_ context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageNetworkObservation, error) {
	f.inspectCalls++
	if !reflect.DeepEqual(intent, f.intent) {
		return gatewayRebindStageNetworkObservation{}, nil
	}
	if !f.found {
		value := gatewayRebindStageNetworkObservation{ConfigVolumeFound: f.extraConfig}
		if f.extraConfig {
			value.OwnedVolumes = []string{intent.Intent.Identity.ConfigVolume}
		}
		if f.extraVolume {
			value.OwnedVolumes = append(value.OwnedVolumes, "rig-rebind-unexpected")
		}
		return value, nil
	}
	labels := gatewayRebindStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole)
	if f.foreign {
		labels[gatewayV2OperationLabelKey] = "11111111-1111-4111-8111-111111111111"
	}
	bridgeName, _ := gatewayRebindStageBridgeName(intent)
	return gatewayRebindStageNetworkObservation{
		Found: true, ID: f.id,
		Network: caddyNetworkInspection{
			Name: intent.Intent.Identity.IngressNetwork, Driver: "bridge", Scope: "local",
			Options: map[string]string{gatewayRebindBridgeNameOptionKey: bridgeName}, Labels: labels,
			IPAM: caddyNetworkIPAM{Config: []networkIPAM{{
				Subnet: intent.Intent.Network.Subnet, Gateway: intent.Intent.Network.GatewayIPv4,
			}}}, Containers: map[string]caddyNetworkContainerInspection{},
		},
		ConfigVolumeFound: f.extraConfig,
		OwnedVolumes: func() []string {
			result := []string{}
			if f.extraConfig {
				result = append(result, intent.Intent.Identity.ConfigVolume)
			}
			if f.extraVolume {
				result = append(result, "rig-rebind-unexpected")
			}
			return result
		}(),
		OwnedNetworks: []string{intent.Intent.Identity.IngressNetwork},
	}, nil
}

func (f *gatewayRebindStageNetworkFake) inspectImage(_ context.Context) (imageInspection, bool, error) {
	return imageInspection{
		ID: "sha256:" + f.imageID, OS: "linux", RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest},
	}, true, nil
}

func (f *gatewayRebindStageNetworkFake) create(_ context.Context,
	_ gatewayRebindProtectedIntent,
) (string, error) {
	f.createCalls++
	if !f.createAbsent {
		f.found = true
	}
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createID != "" {
		return f.createID, f.createErr
	}
	return f.id, f.createErr
}

func TestGatewayRebindStageNetworkCreateArgsAreGenerationScopedAndUnpublished(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	args, err := gatewayRebindStageNetworkCreateArgs(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	bridgeName, err := gatewayRebindStageBridgeName(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		fixture.intent.Intent.Identity.IngressNetwork,
		gatewayRebindBridgeNameOptionKey + "=" + bridgeName,
		gatewayV2IdentityLabelKey + "=" + gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey + "=" + fixture.intent.OperationID,
		gatewayRebindGenerationLabelKey + "=" + strconv.FormatUint(fixture.intent.Generation, 10),
		gatewayRebindIntentDigestLabelKey + "=" + fixture.intent.Digest,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("network create args omit %q: %#v", required, args)
		}
	}
	for _, prohibited := range []string{"--publish", "container", "volume"} {
		if slices.Contains(args, prohibited) {
			t.Fatalf("network-only create args contain %q: %#v", prohibited, args)
		}
	}
}

func TestGatewayRebindStageNetworkCreatesBindsAndReplays(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
	before, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(3), nil); err != nil {
		history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		t.Fatalf("stage error=%v create=%d inspect=%d found=%t progress=%d scan_error=%v", err, fake.createCalls,
			fake.inspectCalls, fake.found, len(history.Progress), scanErr)
	}
	if fake.createCalls != 1 {
		t.Fatalf("network create calls=%d, want 1", fake.createCalls)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 3 {
		t.Fatalf("progress=%#v error=%v", history.Progress, err)
	}
	bound := history.Progress[2].Record
	ownership, err := gatewayRebindStageNetworkOwnershipDigest(fixture.intent)
	if err != nil || bound.PreviousDigest != records[1].Digest || bound.Stage == nil || bound.Stage.Network == nil ||
		bound.Stage.Network.ID != fake.id || bound.Stage.Network.OwnershipDigest != ownership {
		t.Fatalf("invalid bound network progress: %#v error=%v", bound, err)
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite claim changed during network staging: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(4), nil); err != nil {
		t.Fatalf("restart replay failed: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("restart recreated bound network: calls=%d", fake.createCalls)
	}
}

func TestGatewayRebindStageNetworkCreatePathAllowsOnlyExactFinalMountReordering(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(int, *gatewayV2DockerObservation)
		wantError bool
		wantReads int
	}{
		{name: "alternating exact mount order", mutate: func(read int, value *gatewayV2DockerObservation) {
			if read%2 == 0 {
				value.FinalContainer.Mounts[0], value.FinalContainer.Mounts[1] =
					value.FinalContainer.Mounts[1], value.FinalContainer.Mounts[0]
			}
		}, wantReads: 8},
		{name: "wrong mount", mutate: func(read int, value *gatewayV2DockerObservation) {
			if read == 2 {
				value.FinalContainer.Mounts[0].Name = "different-volume"
			}
		}, wantError: true, wantReads: 2},
		{name: "non-mount drift", mutate: func(read int, value *gatewayV2DockerObservation) {
			if read == 2 {
				value.Final404Proven = !value.Final404Proven
			}
		}, wantError: true, wantReads: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			records := installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			dockerReads := 0
			inspect := func(_ context.Context, _ routeState, _ gatewayV2RouteState,
				_ gatewayMigrationJournal,
			) (gatewayV2DockerObservation, error) {
				dockerReads++
				observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
				test.mutate(dockerReads, &observation)
				return observation, nil
			}

			err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads, inspect, fake,
				gatewayRebindProgressTimestamp(3), nil)
			if (err != nil) != test.wantError || dockerReads != test.wantReads {
				t.Fatalf("stage create error=%v reads=%d want_error=%t want_reads=%d",
					err, dockerReads, test.wantError, test.wantReads)
			}

			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if test.wantError {
				if scanErr != nil || len(history.Progress) != 2 || fake.createCalls != 0 {
					t.Fatalf("rejected create path mutated state: progress=%d create=%d scan_error=%v",
						len(history.Progress), fake.createCalls, scanErr)
				}
				return
			}
			if scanErr != nil || len(history.Progress) != 3 || fake.createCalls != 1 {
				t.Fatalf("accepted create path state: progress=%d create=%d scan_error=%v",
					len(history.Progress), fake.createCalls, scanErr)
			}
			bound := history.Progress[2].Record
			ownership, digestErr := gatewayRebindStageNetworkOwnershipDigest(fixture.intent)
			if digestErr != nil || bound.Sequence != 3 || bound.PreviousDigest != records[1].Digest ||
				bound.Stage == nil || bound.Stage.Network == nil || bound.Stage.Network.ID != fake.id ||
				bound.Stage.Network.OwnershipDigest != ownership {
				t.Fatalf("invalid protected sequence-three network binding: %#v digest_error=%v", bound, digestErr)
			}
		})
	}
}

func TestGatewayRebindStageNetworkAttestationAllowsOnlyExactFinalMountReordering(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*gatewayV2DockerObservation)
		wantErr bool
	}{
		{name: "exact mount reordering", mutate: func(value *gatewayV2DockerObservation) {
			value.FinalContainer.Mounts[0], value.FinalContainer.Mounts[1] =
				value.FinalContainer.Mounts[1], value.FinalContainer.Mounts[0]
		}},
		{name: "wrong mount", mutate: func(value *gatewayV2DockerObservation) {
			value.FinalContainer.Mounts[0].Name = "different-volume"
		}, wantErr: true},
		{name: "non-mount drift", mutate: func(value *gatewayV2DockerObservation) {
			value.Final404Proven = !value.Final404Proven
		}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(3), nil); err != nil {
				t.Fatalf("bind stage network fixture: %v", err)
			}

			dockerReads := 0
			inspect := func(_ context.Context, _ routeState, _ gatewayV2RouteState,
				_ gatewayMigrationJournal,
			) (gatewayV2DockerObservation, error) {
				dockerReads++
				observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
				if dockerReads == 2 {
					test.mutate(&observation)
				}
				return observation, nil
			}
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads, inspect, fake,
				gatewayRebindProgressTimestamp(4), nil)
			if (err != nil) != test.wantErr || dockerReads != 2 {
				t.Fatalf("stage network replay error=%v reads=%d want_error=%t", err, dockerReads, test.wantErr)
			}
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 3 || fake.createCalls != 1 {
				t.Fatalf("attestation replay mutated state: progress=%d create=%d scan_error=%v",
					len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func TestGatewayRebindStageNetworkAcceptsExactDockerBridgeHostDeltaOnly(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
	bridgeName, err := gatewayRebindStageBridgeName(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	plan := netip.MustParsePrefix(fixture.intent.Intent.Network.Subnet)
	gateway := netip.MustParseAddr(fixture.intent.Intent.Network.GatewayIPv4)
	originalCandidates := reads.network.candidates
	malformedBridge := false
	reads.network.candidates = func() ([]hostNetworkCandidate, error) {
		values, readErr := originalCandidates()
		if fake.found {
			interfaceID := "42/" + bridgeName
			if malformedBridge {
				interfaceID = "0/" + bridgeName
			}
			values = append(values, hostNetworkCandidate{
				InterfaceID: interfaceID, IPv4: fixture.intent.Intent.Network.GatewayIPv4, Prefix: plan,
			})
		}
		return values, readErr
	}
	originalHost := reads.network.host
	unrelated := false
	reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
		value, readErr := originalHost()
		if fake.found {
			value.Routes = append(value.Routes, plan,
				netip.PrefixFrom(gateway, 32), netip.PrefixFrom(lastIPv4Address(plan), 32))
			value.Interfaces = append(value.Interfaces, plan)
		}
		if unrelated {
			value.Routes = append(value.Routes, netip.MustParsePrefix("10.99.0.0/16"))
		}
		return value, readErr
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(3), nil); err != nil {
		t.Fatalf("exact Docker bridge host delta was rejected: %v", err)
	}
	unrelated = true
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(4), nil); err == nil {
		t.Fatal("bound replay accepted an unrelated host route delta")
	}
	if fake.createCalls != 1 {
		t.Fatalf("host drift replay mutated Docker: create=%d", fake.createCalls)
	}
	unrelated = false
	malformedBridge = true
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(5), nil); err == nil {
		t.Fatal("bound replay accepted a malformed Docker bridge candidate identity")
	}
}

func TestGatewayRebindStageNetworkRouteDeltaRejectsPartialOrUnrelatedChanges(t *testing.T) {
	baseline := []string{"0.0.0.0/0", "10.99.0.0/16"}
	const plan, gateway = "10.240.0.0/28", "10.240.0.1"
	routes := func(additions ...string) []string {
		values := append([]string{}, baseline...)
		values = append(values, additions...)
		sort.Strings(values)
		return values
	}
	for _, test := range []struct {
		name   string
		values []string
		accept bool
	}{
		{name: "baseline", values: routes(), accept: true},
		{name: "connected subnet", values: routes(plan), accept: true},
		{name: "exact Linux bridge", values: routes(plan, "10.240.0.1/32", "10.240.0.15/32"), accept: true},
		{name: "missing gateway", values: routes(plan, "10.240.0.15/32")},
		{name: "missing broadcast", values: routes(plan, "10.240.0.1/32")},
		{name: "duplicate subnet", values: routes(plan, plan, "10.240.0.1/32", "10.240.0.15/32")},
		{name: "duplicate gateway", values: routes(plan, "10.240.0.1/32", "10.240.0.1/32", "10.240.0.15/32")},
		{name: "extra inside subnet", values: routes(plan, "10.240.0.1/32", "10.240.0.15/32", "10.240.0.2/32")},
		{name: "extra outside subnet", values: routes(plan, "10.240.0.1/32", "10.240.0.15/32", "10.241.0.0/16")},
		{name: "removed baseline", values: []string{"0.0.0.0/0", plan, "10.240.0.1/32", "10.240.0.15/32"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := gatewayRebindStageNetworkRoutesMatch(test.values, baseline, plan, gateway); got != test.accept {
				t.Fatalf("route delta accepted=%t, want %t", got, test.accept)
			}
		})
	}
}

func TestGatewayRebindStageNetworkRejectsRouteDriftAfterCreateBeforeBinding(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra []netip.Prefix
	}{
		{name: "missing broadcast"},
		{name: "unrelated route", extra: []netip.Prefix{
			netip.MustParsePrefix("198.51.100.0/24"),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			plan := netip.MustParsePrefix(fixture.intent.Intent.Network.Subnet)
			gateway := netip.MustParseAddr(fixture.intent.Intent.Network.GatewayIPv4)
			bridgeName, err := gatewayRebindStageBridgeName(fixture.intent)
			if err != nil {
				t.Fatal(err)
			}
			originalCandidates := reads.network.candidates
			reads.network.candidates = func() ([]hostNetworkCandidate, error) {
				values, readErr := originalCandidates()
				if fake.found {
					values = append(values, hostNetworkCandidate{
						InterfaceID: "42/" + bridgeName, IPv4: gateway.String(), Prefix: plan,
					})
				}
				return values, readErr
			}
			originalHost := reads.network.host
			reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
				value, readErr := originalHost()
				if fake.found {
					value.Routes = append(value.Routes, plan, netip.PrefixFrom(gateway, 32))
					if len(test.extra) > 0 {
						value.Routes = append(value.Routes, netip.PrefixFrom(lastIPv4Address(plan), 32))
						value.Routes = append(value.Routes, test.extra...)
					}
					value.Interfaces = append(value.Interfaces, plan)
				}
				return value, readErr
			}
			err = fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(3), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 2 || !fake.found || fake.createCalls != 1 {
				t.Fatalf("post-create route drift bound an uncertain network: error=%v scan=%v progress=%d create=%d found=%t",
					err, scanErr, len(history.Progress), fake.createCalls, fake.found)
			}
		})
	}
}

func TestGatewayRebindStageNetworkFailsClosedOnAmbiguousCreateAndUnboundRestart(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
	fake.createErr = errors.New("injected ambiguous create result")
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(3), nil)
	if err == nil || !fake.found || fake.createCalls != 1 {
		t.Fatalf("ambiguous create was adopted: error=%v create=%d found=%t", err, fake.createCalls, fake.found)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 2 {
		t.Fatalf("ambiguous create installed a resource binding: progress=%#v error=%v", history.Progress, err)
	}
	fake.createErr = nil
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(4), nil); err == nil {
		t.Fatal("fresh restart adopted a network without a protected ID binding")
	}
	if fake.createCalls != 1 {
		t.Fatalf("fresh restart recreated an unresolved network: create=%d", fake.createCalls)
	}
}

func TestGatewayRebindStageNetworkRejectsInvalidCreateOutcomes(t *testing.T) {
	for _, test := range []struct {
		name         string
		createErr    error
		createID     string
		createAbsent bool
	}{
		{name: "clean create error without resource", createErr: errors.New("injected create error"), createAbsent: true},
		{name: "invalid returned identity", createID: "invalid"},
		{name: "success without resource", createAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			fake.createErr, fake.createID, fake.createAbsent = test.createErr, test.createID, test.createAbsent
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(3), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 2 || fake.createCalls != 1 {
				t.Fatalf("invalid create outcome was bound: error=%v progress=%d create=%d scan_error=%v",
					err, len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func TestGatewayRebindStageNetworkRejectsDuplicateSuccessorNetworkID(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
	if len(fixture.intent.NetworkObservation.DockerNetworkIDs) == 0 {
		t.Fatal("fixture has no baseline Docker network identity")
	}
	fake.id = fixture.intent.NetworkObservation.DockerNetworkIDs[0]
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(3), nil)
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil || scanErr != nil || len(history.Progress) != 2 || fake.createCalls != 1 {
		t.Fatalf("duplicate successor network identity was bound: error=%v progress=%d create=%d scan_error=%v",
			err, len(history.Progress), fake.createCalls, scanErr)
	}
}

func TestGatewayRebindStageNetworkRejectsFreshSQLAndPredecessorDriftBeforeCreate(t *testing.T) {
	t.Run("SQLite claim drift", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`,
			strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})

	t.Run("predecessor Docker drift", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		inspect := func(context.Context, routeState, gatewayV2RouteState,
			gatewayMigrationJournal,
		) (gatewayV2DockerObservation, error) {
			observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
			observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
			return observation, nil
		}
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads, inspect, fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})
}

func TestGatewayRebindStageNetworkRejectsCheckpointProgressReplacementBeforeCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointErr error
	checkpoint := func() {
		if removeErr := os.Remove(store.path); removeErr != nil {
			checkpointErr = removeErr
			return
		}
		observation := gatewayRebindProgressStageObservation(fixture.intent, 2)
		observation.ObservedDockerImageID = strings.Repeat("d", 64)
		replacement, replaceErr := newGatewayRebindStageIntentProgress(fixture.intent, records[0], observation)
		if replaceErr != nil {
			checkpointErr = replaceErr
			return
		}
		checkpointErr = store.installExact(context.Background(), replacement)
	}
	err = fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(3), checkpoint)
	if checkpointErr != nil {
		t.Fatalf("replace checkpoint progress: %v", checkpointErr)
	}
	assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
}

func TestGatewayRebindStageNetworkRejectsCheckpointProgressRemovalBeforeCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointErr error
	err = fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(3), func() { checkpointErr = os.Remove(store.path) })
	if checkpointErr != nil {
		t.Fatalf("remove checkpoint progress: %v", checkpointErr)
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil || scanErr != nil || len(history.Progress) != 1 || fake.createCalls != 0 {
		t.Fatalf("checkpoint progress removal reached create: error=%v progress=%d create=%d scan_error=%v",
			err, len(history.Progress), fake.createCalls, scanErr)
	}
}

func TestGatewayRebindStageNetworkReleaseErrorsPreserveExactBinding(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*testing.T)
	}{
		{name: "deployment effects lease release", inject: func(t *testing.T) {
			original := gatewayRebindAcquireDeploymentEffects
			gatewayRebindAcquireDeploymentEffects = func(ctx context.Context, directory string) (func() error, error) {
				release, err := original(ctx, directory)
				if err != nil {
					return nil, err
				}
				return func() error {
					_ = release()
					return errors.New("injected effects release error")
				}, nil
			}
			t.Cleanup(func() { gatewayRebindAcquireDeploymentEffects = original })
		}},
		{name: "gateway OS lock release", inject: func(t *testing.T) {
			original := managerAcquireGatewayOSLock
			managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
				release, err := original(ctx, store)
				if err != nil {
					return nil, err
				}
				return func() error {
					_ = release()
					return errors.New("injected gateway release error")
				}, nil
			}
			t.Cleanup(func() { managerAcquireGatewayOSLock = original })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			test.inject(t)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(3), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 3 || fake.createCalls != 1 {
				t.Fatalf("release error lost or corrupted binding: error=%v progress=%d create=%d scan_error=%v",
					err, len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func TestGatewayRebindStageNetworkFailsClosedOnForeignOrStaleBoundary(t *testing.T) {
	t.Run("foreign exact name", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		fake.found, fake.foreign = true, true
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})

	t.Run("unbound successor volume", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		fake.extraConfig = true
		// The exact volume is visible even though the network itself is absent.
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})

	t.Run("unexpected owned successor volume", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		fake.extraVolume = true
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})

	t.Run("boundary drifts adjacent to create", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		originalHost := reads.network.host
		drift := false
		reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
			value, err := originalHost()
			if drift {
				value.Routes = append(value.Routes, netip.MustParsePrefix("10.99.0.0/16"))
			}
			return value, err
		}
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), func() { drift = true })
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})

	t.Run("successful create identity is substituted before inspection", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		fake.createID = fake.id
		fake.onCreate = func() { fake.id = strings.Repeat("e", 64) }
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err == nil || scanErr != nil || len(history.Progress) != 2 || fake.createCalls != 1 {
			t.Fatalf("substituted create identity was bound: error=%v progress=%d create=%d scan_error=%v",
				err, len(history.Progress), fake.createCalls, scanErr)
		}
	})
}

func TestGatewayRebindStageNetworkRejectsStaleStageImageAndTopology(t *testing.T) {
	t.Run("stale pinned image", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		fake.imageID = strings.Repeat("d", 64)
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})

	t.Run("stale topology digest", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		records := installGatewayRebindEffectBoundaryProgress(t, fixture, 1)
		observation := gatewayRebindProgressStageObservation(fixture.intent, 2)
		observation.NetworkTopologyDigest = strings.Repeat("d", 64)
		second, err := newGatewayRebindStageIntentProgress(fixture.intent, records[0], observation)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
			fixture.intent.Generation, fixture.intent.OperationID, 2)
		if err != nil || store.installExact(context.Background(), second) != nil {
			t.Fatalf("install stale stage fixture: %v", err)
		}
		fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
		err = fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(3), nil)
		assertGatewayRebindStageNetworkRejected(t, fixture, fake, err)
	})
}

func TestGatewayRebindStageNetworkRecoversWriteAndCancellationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name            string
		ambiguous       bool
		cancel          bool
		restartSucceeds bool
	}{
		{name: "protected write rejected"},
		{name: "protected write installed ambiguously", ambiguous: true, restartSucceeds: true},
		{name: "context cancelled after create", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			ctx := context.Background()
			originalWrite := upgradeProtectedWriteNew
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				fake.onCreate = cancel
			} else {
				upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
					if test.ambiguous {
						if err := originalWrite(path, purpose, body); err != nil {
							return err
						}
					}
					return errors.New("injected protected write uncertainty")
				}
				t.Cleanup(func() { upgradeProtectedWriteNew = originalWrite })
			}
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				ctx, fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(3), nil)
			if err == nil || !fake.found || fake.createCalls != 1 {
				t.Fatalf("injected boundary was not preserved: error=%v found=%t create=%d",
					err, fake.found, fake.createCalls)
			}
			if !test.cancel {
				// Recovery must use a fresh protected writer after any uncertain
				// outcome; never hide the original error inside this invocation.
				upgradeProtectedWriteNew = originalWrite
			}
			fake.onCreate = nil
			restartErr := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(4), nil)
			if test.restartSucceeds && restartErr != nil {
				t.Fatalf("fresh restart did not replay the installed protected binding: %v", restartErr)
			}
			if !test.restartSucceeds && restartErr == nil {
				t.Fatal("fresh restart adopted an unbound existing network")
			}
			if fake.createCalls != 1 {
				t.Fatalf("recovery recreated existing network: calls=%d", fake.createCalls)
			}
		})
	}
}

func TestGatewayRebindStageNetworkReplayRejectsBoundIDSubstitutionAndExtraInventory(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageNetworkFake, *gatewayRebindSuccessorPreflightReads)
	}{
		{name: "bound ID substitution", mutate: func(fake *gatewayRebindStageNetworkFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.id = strings.Repeat("b", 64)
		}},
		{name: "extra Docker network", mutate: func(_ *gatewayRebindStageNetworkFake, reads *gatewayRebindSuccessorPreflightReads) {
			originalIDs := reads.dockerIDs
			reads.dockerIDs = func(ctx context.Context) ([]string, error) {
				ids, err := originalIDs(ctx)
				ids = append(ids, strings.Repeat("c", 64))
				slices.Sort(ids)
				return ids, err
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			fake, reads := gatewayRebindStageNetworkTestDriver(fixture)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(3), nil); err != nil {
				t.Fatal(err)
			}
			test.mutate(fake, &reads)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(4), nil); err == nil {
				t.Fatal("replay accepted substituted or extra Docker identity")
			}
			if fake.createCalls != 1 {
				t.Fatalf("replay mutated Docker after drift: create calls=%d", fake.createCalls)
			}
		})
	}
}

func gatewayRebindStageNetworkTestDriver(fixture gatewayRebindEffectBoundaryFixture) (
	*gatewayRebindStageNetworkFake, gatewayRebindSuccessorPreflightReads,
) {
	id := strings.Repeat("d", 64)
	fake := &gatewayRebindStageNetworkFake{
		intent: fixture.intent, id: id, imageID: strings.Repeat("b", 64),
	}
	reads := fixture.reads
	originalIDs, originalDocker := reads.dockerIDs, reads.network.docker
	reads.dockerIDs = func(ctx context.Context) ([]string, error) {
		ids, err := originalIDs(ctx)
		if fake.found && !fake.foreign {
			ids = append(ids, fake.id)
			slices.Sort(ids)
		}
		return ids, err
	}
	reads.network.docker = func(ctx context.Context) ([]netip.Prefix, error) {
		prefixes, err := originalDocker(ctx)
		if fake.found && !fake.foreign {
			prefixes = append(prefixes, netip.MustParsePrefix(fixture.intent.Intent.Network.Subnet))
		}
		return prefixes, err
	}
	return fake, reads
}

func gatewayRebindStageNetworkInspect(t *testing.T,
	fixture gatewayRebindEffectBoundaryFixture,
) gatewayRebindDockerInspector {
	t.Helper()
	return func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) (gatewayV2DockerObservation, error) {
		return gatewayRebindFixtureDockerObservation(t, fixture.predecessor), nil
	}
}

func assertGatewayRebindStageNetworkRejected(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	fake *gatewayRebindStageNetworkFake, err error,
) {
	t.Helper()
	if err == nil {
		t.Fatal("stage network unexpectedly succeeded")
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 2 || fake.createCalls != 0 {
		t.Fatalf("rejected stage mutated state: progress=%d create=%d scan_error=%v",
			len(history.Progress), fake.createCalls, scanErr)
	}
}
