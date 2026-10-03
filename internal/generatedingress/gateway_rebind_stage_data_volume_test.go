package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type gatewayRebindStageDataVolumeFake struct {
	intent         gatewayRebindProtectedIntent
	networkID      string
	imageID        string
	configFound    bool
	configForeign  bool
	configMount    string
	configCreated  string
	found          bool
	foreign        bool
	extraVolume    bool
	stageFound     bool
	ownedContainer bool
	mountpoint     string
	createdAt      string
	createName     string
	createErr      error
	createAbsent   bool
	createCalls    int
	inspectCalls   int
	onInspect      func(*gatewayRebindStageDataVolumeFake)
	onCreate       func()
}

func (f *gatewayRebindStageDataVolumeFake) inspect(_ context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageDataVolumeObservation, error) {
	f.inspectCalls++
	if f.onInspect != nil {
		f.onInspect(f)
	}
	if !reflect.DeepEqual(intent, f.intent) {
		return gatewayRebindStageDataVolumeObservation{}, errors.New("unexpected intent")
	}
	bridgeName, _ := gatewayRebindStageBridgeName(intent)
	value := gatewayRebindStageDataVolumeObservation{
		NetworkFound: true, NetworkID: f.networkID,
		Network: caddyNetworkInspection{
			Name: intent.Intent.Identity.IngressNetwork, Driver: "bridge", Scope: "local",
			Options: map[string]string{gatewayRebindBridgeNameOptionKey: bridgeName},
			Labels:  gatewayRebindStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
			IPAM: caddyNetworkIPAM{Config: []networkIPAM{{
				Subnet: intent.Intent.Network.Subnet, Gateway: intent.Intent.Network.GatewayIPv4,
			}}}, Containers: map[string]caddyNetworkContainerInspection{},
		},
		StageContainerFound: f.stageFound,
		OwnedNetworks:       []string{intent.Intent.Identity.IngressNetwork},
	}
	if f.ownedContainer {
		value.OwnedContainers = []string{intent.Intent.Identity.StageContainer}
	}
	if f.configFound {
		labels := gatewayRebindStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)
		if f.configForeign {
			labels[gatewayV2OperationLabelKey] = "11111111-1111-4111-8111-111111111111"
		}
		value.ConfigVolumeFound = true
		value.ConfigVolume = volumeInspection{
			Name: intent.Intent.Identity.ConfigVolume, Driver: "local", Scope: "local",
			Options: map[string]string{}, Labels: labels,
		}
		value.ConfigVolumeIdentity = gatewayV1VolumeIdentity{Mountpoint: f.configMount, CreatedAt: f.configCreated}
		value.OwnedVolumes = []string{intent.Intent.Identity.ConfigVolume}
	}
	if f.found {
		labels := gatewayRebindStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole)
		if f.foreign {
			labels[gatewayV2OperationLabelKey] = "11111111-1111-4111-8111-111111111111"
		}
		value.DataVolumeFound = true
		value.DataVolume = volumeInspection{
			Name: intent.Intent.Identity.DataVolume, Driver: "local", Scope: "local",
			Options: map[string]string{}, Labels: labels,
		}
		value.DataVolumeIdentity = gatewayV1VolumeIdentity{Mountpoint: f.mountpoint, CreatedAt: f.createdAt}
		value.OwnedVolumes = append(value.OwnedVolumes, intent.Intent.Identity.DataVolume)
	}
	if f.extraVolume {
		value.OwnedVolumes = append(value.OwnedVolumes, "rig-rebind-unexpected")
	}
	slices.Sort(value.OwnedVolumes)
	return value, nil
}

func (f *gatewayRebindStageDataVolumeFake) inspectImage(context.Context) (imageInspection, bool, error) {
	return imageInspection{ID: "sha256:" + f.imageID, OS: "linux",
		RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}, true, nil
}

func (f *gatewayRebindStageDataVolumeFake) create(_ context.Context,
	_ gatewayRebindProtectedIntent,
) (string, error) {
	f.createCalls++
	if !f.createAbsent {
		f.found = true
	}
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createName != "" {
		return f.createName, f.createErr
	}
	return f.intent.Intent.Identity.DataVolume, f.createErr
}

func TestGatewayRebindStageDataVolumeCreateArgsAreGenerationScopedAndPrivate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	args, err := gatewayRebindStageDataVolumeCreateArgs(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"volume", "create", "--driver", "local"}
	want = appendGatewayV2Labels(want, gatewayRebindStageResourceLabels(fixture.intent,
		gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole))
	want = append(want, fixture.intent.Intent.Identity.DataVolume)
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("volume create args=%#v want %#v", args, want)
	}
	joined := strings.Join(args, "\x00")
	for _, required := range []string{
		fixture.intent.Intent.Identity.DataVolume,
		gatewayV2IdentityLabelKey + "=" + gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey + "=" + fixture.intent.OperationID,
		gatewayRebindGenerationLabelKey + "=" + strconv.FormatUint(fixture.intent.Generation, 10),
		gatewayRebindIntentDigestLabelKey + "=" + fixture.intent.Digest,
		gatewayV2ResourceRoleLabelKey + "=" + gatewayV2DataVolumeRole,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("volume create args omit %q: %#v", required, args)
		}
	}
	for _, prohibited := range []string{"--publish", "container", "network"} {
		if slices.Contains(args, prohibited) {
			t.Fatalf("data-volume-only create args contain %q: %#v", prohibited, args)
		}
	}
}

func TestGatewayRebindStageDataVolumeCreatesBindsAndReplays(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
	fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
	baseHistory, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(baseHistory.Progress) != 4 {
		t.Fatalf("base progress=%d error=%v", len(baseHistory.Progress), err)
	}
	baseBytes := make([][]byte, len(baseHistory.Progress))
	for index, entry := range baseHistory.Progress {
		baseBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpointReached := false
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5),
		func() { checkpointReached = true }); err != nil {
		history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		t.Fatalf("stage data volume: %v found=%t create=%d inspect=%d checkpoint=%t progress=%d scan=%v",
			err, fake.found, fake.createCalls, fake.inspectCalls, checkpointReached, len(history.Progress), scanErr)
	}
	if fake.createCalls != 1 {
		t.Fatalf("volume create calls=%d, want 1", fake.createCalls)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 5 {
		t.Fatalf("progress=%#v error=%v", history.Progress, err)
	}
	for index := range baseHistory.Progress {
		bytes, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !reflect.DeepEqual(baseHistory.Progress[index].Record, history.Progress[index].Record) ||
			!reflect.DeepEqual(baseBytes[index], bytes) || records[index].Digest != history.Progress[index].Record.Digest {
			t.Fatalf("sequence %d changed while appending data-volume receipt: read=%v", index+1, readErr)
		}
	}
	bound := history.Progress[4].Record
	boundHistory := append([]gatewayRebindProgressSelection(nil), history.Progress...)
	boundBytes := make([][]byte, len(history.Progress))
	for index, entry := range history.Progress {
		boundBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	ownership, err := gatewayRebindStageDataVolumeOwnershipDigest(fixture.intent)
	if err != nil || bound.PreviousDigest != records[3].Digest || bound.Stage == nil ||
		bound.Stage.ConfigVolume == nil || !reflect.DeepEqual(bound.Stage.ConfigVolume, records[3].Stage.ConfigVolume) ||
		bound.Stage.DataVolume == nil || bound.Stage.DataVolume.Name != fixture.intent.Intent.Identity.DataVolume ||
		bound.Stage.DataVolume.Mountpoint != fake.mountpoint || bound.Stage.DataVolume.CreatedAt != fake.createdAt ||
		bound.Stage.DataVolume.OwnershipDigest != ownership {
		t.Fatalf("invalid bound data volume progress: %#v error=%v", bound, err)
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite claim changed during data volume staging: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil); err != nil {
		t.Fatalf("restart replay failed: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("restart recreated bound data volume: calls=%d", fake.createCalls)
	}
	replayed, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(boundHistory, replayed.Progress) {
		t.Fatalf("restart changed protected progress: error=%v", err)
	}
	for index, entry := range replayed.Progress {
		bytes, readErr := os.ReadFile(entry.Store.path)
		if readErr != nil || !reflect.DeepEqual(boundBytes[index], bytes) {
			t.Fatalf("restart changed protected progress bytes at %d: error=%v", index, readErr)
		}
	}
	if _, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), nil); err == nil {
		t.Fatal("prepared pre-effect attestor accepted sequence five")
	}
	if err := fixture.predecessor.manager.inspectGatewayRebindPreparedDockerPredecessor(
		context.Background(), fixture.predecessor.repository, nil,
		gatewayRebindStageNetworkInspect(t, fixture)); err == nil {
		t.Fatal("public prepared predecessor attestor accepted sequence five")
	}

	networkFake := &gatewayRebindStageNetworkFake{
		intent: fixture.intent, id: fake.networkID, found: true, imageID: fake.imageID,
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), networkFake, gatewayRebindProgressTimestamp(7), nil); err == nil {
		t.Fatal("network-stage entrypoint accepted sequence five")
	}
	if networkFake.createCalls != 0 {
		t.Fatalf("network-stage entrypoint mutated Docker after sequence five: %d", networkFake.createCalls)
	}
	configFake := &gatewayRebindStageConfigVolumeFake{}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), configFake, gatewayRebindProgressTimestamp(7), nil); err == nil {
		t.Fatal("config-volume entrypoint accepted sequence five")
	}
	if configFake.createCalls != 0 {
		t.Fatalf("config-volume entrypoint mutated Docker after sequence five: %d", configFake.createCalls)
	}
}

func TestGatewayRebindStageDataVolumeRejectsUnboundAndInvalidEffects(t *testing.T) {
	for _, test := range []struct {
		name        string
		before      bool
		preMutate   func(*gatewayRebindStageDataVolumeFake)
		mutate      func(*gatewayRebindStageDataVolumeFake)
		wantCreated bool
	}{
		{name: "unbound exact volume", before: true},
		{name: "stage container already exists", preMutate: func(f *gatewayRebindStageDataVolumeFake) { f.stageFound = true }},
		{name: "owned container after create", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.ownedContainer = true }, wantCreated: true},
		{name: "foreign labels after create", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.foreign = true }, wantCreated: true},
		{name: "invalid created at", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.createdAt = "not-a-time" }, wantCreated: true},
		{name: "extra owned volume", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.extraVolume = true }, wantCreated: true},
		{name: "config volume disappeared", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.configFound = false }, wantCreated: true},
		{name: "config identity substituted", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.configMount += "-replacement" }, wantCreated: true},
		{name: "create returned wrong name", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.createName = "wrong" }, wantCreated: true},
		{name: "create returned error", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.createErr = errors.New("create uncertain") }, wantCreated: true},
		{name: "create reported success but resource absent", mutate: func(f *gatewayRebindStageDataVolumeFake) { f.createAbsent = true }, wantCreated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
			fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
			if test.before {
				fake.found = true
			}
			if test.preMutate != nil {
				test.preMutate(fake)
			}
			if test.mutate != nil {
				originalOnCreate := fake.onCreate
				fake.onCreate = func() {
					if originalOnCreate != nil {
						originalOnCreate()
					}
					test.mutate(fake)
				}
				if test.name == "create returned wrong name" || test.name == "create returned error" ||
					test.name == "create reported success but resource absent" {
					test.mutate(fake)
					fake.onCreate = nil
				}
			}
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 4 {
				t.Fatalf("invalid effect was bound: error=%v progress=%d scan_error=%v", err, len(history.Progress), scanErr)
			}
			wantCalls := 0
			if test.wantCreated {
				wantCalls = 1
			}
			if fake.createCalls != wantCalls {
				t.Fatalf("create calls=%d want=%d", fake.createCalls, wantCalls)
			}
		})
	}
}

func TestGatewayRebindStageDataVolumeReplayRejectsIdentityAndCensusSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageDataVolumeFake, *gatewayRebindSuccessorPreflightReads)
	}{
		{name: "mountpoint", mutate: func(fake *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.mountpoint += "-replacement"
		}},
		{name: "created at", mutate: func(fake *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.createdAt = "2026-10-03T12:09:03Z"
		}},
		{name: "foreign labels", mutate: func(fake *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.foreign = true
		}},
		{name: "config mountpoint", mutate: func(fake *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.configMount += "-replacement"
		}},
		{name: "extra owned volume", mutate: func(fake *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.extraVolume = true
		}},
		{name: "network ID", mutate: func(fake *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.networkID = strings.Repeat("e", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
			fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil); err != nil {
				t.Fatal(err)
			}
			test.mutate(fake, &reads)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil); err == nil {
				t.Fatal("replay accepted substituted Docker identity or census")
			}
			if fake.createCalls != 1 {
				t.Fatalf("replay mutated Docker after substitution: create calls=%d", fake.createCalls)
			}
		})
	}
}

func TestGatewayRebindStageDataVolumeRejectsStaleAuthorizationBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindEffectBoundaryFixture, *gatewayRebindStageDataVolumeFake, *gatewayRebindSuccessorPreflightReads)
	}{
		{name: "stale image", mutate: func(_ *gatewayRebindEffectBoundaryFixture, f *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			f.imageID = strings.Repeat("9", 64)
		}},
		{name: "stale config identity", mutate: func(_ *gatewayRebindEffectBoundaryFixture, f *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			f.configMount += "-replacement"
		}},
		{name: "foreign config labels", mutate: func(_ *gatewayRebindEffectBoundaryFixture, f *gatewayRebindStageDataVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			f.configForeign = true
		}},
		{name: "extra Docker network", mutate: func(_ *gatewayRebindEffectBoundaryFixture, _ *gatewayRebindStageDataVolumeFake, reads *gatewayRebindSuccessorPreflightReads) {
			original := reads.dockerIDs
			reads.dockerIDs = func(ctx context.Context) ([]string, error) {
				values, err := original(ctx)
				values = append(values, strings.Repeat("9", 64))
				slices.Sort(values)
				return values, err
			}
		}},
		{name: "host topology drift", mutate: func(_ *gatewayRebindEffectBoundaryFixture, _ *gatewayRebindStageDataVolumeFake, reads *gatewayRebindSuccessorPreflightReads) {
			original := reads.network.host
			reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
				value, err := original()
				value.Routes = append(value.Routes, netip.MustParsePrefix("10.99.0.0/16"))
				return value, err
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
			fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
			test.mutate(&fixture, fake, &reads)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
			assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 0)
		})
	}

	t.Run("SQLite claim drift", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
		fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
		assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 0)
	})

	t.Run("predecessor Docker drift", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
		fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
			observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
			return observation, nil
		}
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
			context.Background(), fixture.predecessor.repository, reads, inspect, fake,
			gatewayRebindProgressTimestamp(5), nil)
		assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 0)
	})

	t.Run("drift adjacent to create", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
		fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
		original := reads.network.host
		drift := false
		reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
			value, err := original()
			if drift {
				value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("10.99.0.0/16"))
			}
			return value, err
		}
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), func() { drift = true })
		assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 0)
	})
}

func TestGatewayRebindStageDataVolumeRejectsNonMonotonicReceiptTimeBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name     string
		sequence uint64
	}{
		{name: "equal", sequence: 4},
		{name: "older", sequence: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
			fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(test.sequence), nil)
			assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 0)
		})
	}
}

func TestGatewayRebindStageDataVolumeRejectsClaimDriftAfterCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
	fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
	fake.onCreate = func() {
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
	}
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
	assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 1)
	if !fake.found {
		t.Fatal("injected claim drift did not occur after volume creation")
	}
}

func TestGatewayRebindStageDataVolumeRejectsCheckpointSequenceFourRemovalBeforeCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
	fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 4)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointErr error
	err = fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5),
		func() { checkpointErr = os.Remove(store.path) })
	if checkpointErr != nil {
		t.Fatalf("remove sequence-four checkpoint: %v", checkpointErr)
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil || scanErr != nil || len(history.Progress) != 3 || fake.createCalls != 0 {
		t.Fatalf("removed sequence four reached create: error=%v progress=%d create=%d scan_error=%v",
			err, len(history.Progress), fake.createCalls, scanErr)
	}
}

func TestGatewayRebindStageDataVolumeRejectsUnstablePostCreateIdentity(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
	fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
	fake.onInspect = func(value *gatewayRebindStageDataVolumeFake) {
		if value.found && value.inspectCalls == 8 {
			value.createdAt = "2026-10-03T12:09:03Z"
		}
	}
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
	assertGatewayRebindStageDataVolumeRejected(t, fixture, fake, err, 1)
}

func TestGatewayRebindStageDataVolumePreservesAmbiguousEffectAndWriteBoundaries(t *testing.T) {
	for _, test := range []struct {
		name            string
		ambiguousWrite  bool
		cancel          bool
		createError     bool
		restartSucceeds bool
	}{
		{name: "Docker create returned an error after creating volume", createError: true},
		{name: "protected write rejected"},
		{name: "protected write installed ambiguously", ambiguousWrite: true, restartSucceeds: true},
		{name: "context cancelled after create", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
			fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
			if test.createError {
				fake.createErr = errors.New("injected Docker create uncertainty")
			}
			ctx := context.Background()
			originalWrite := upgradeProtectedWriteNew
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				fake.onCreate = cancel
			} else if !test.createError {
				upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
					if test.ambiguousWrite {
						if err := originalWrite(path, purpose, body); err != nil {
							return err
						}
					}
					return errors.New("injected protected write uncertainty")
				}
				t.Cleanup(func() { upgradeProtectedWriteNew = originalWrite })
			}
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				ctx, fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
			if err == nil || !fake.found || fake.createCalls != 1 {
				t.Fatalf("injected boundary was not preserved: error=%v found=%t create=%d", err, fake.found, fake.createCalls)
			}
			if !test.cancel {
				upgradeProtectedWriteNew = originalWrite
			}
			fake.onCreate = nil
			fake.createErr = nil
			restartErr := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil)
			if test.restartSucceeds && restartErr != nil {
				t.Fatalf("fresh restart did not replay installed binding: %v", restartErr)
			}
			if !test.restartSucceeds && restartErr == nil {
				t.Fatal("fresh restart adopted an unbound existing data volume")
			}
			if fake.createCalls != 1 {
				t.Fatalf("recovery recreated existing data volume: calls=%d", fake.createCalls)
			}
		})
	}
}

func TestGatewayRebindStageDataVolumeCancellationBeforeEffectReleasesLocks(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
	fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		ctx, fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(5), nil); err == nil {
		t.Fatal("cancelled stage unexpectedly succeeded")
	}
	if fake.createCalls != 0 {
		t.Fatalf("cancelled stage created a volume: calls=%d", fake.createCalls)
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil); err != nil {
		t.Fatalf("fresh stage after cancellation did not acquire released locks: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("fresh stage create calls=%d want 1", fake.createCalls)
	}
}

func TestGatewayRebindStageDataVolumeReleaseErrorsPreserveExactBinding(t *testing.T) {
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
			installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
			fake, reads := gatewayRebindStageDataVolumeTestDriver(fixture)
			test.inject(t)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorDataVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 5 || fake.createCalls != 1 {
				t.Fatalf("release error lost or corrupted binding: error=%v progress=%d create=%d scan_error=%v",
					err, len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func installGatewayRebindStageDataVolumeConfigProgress(t *testing.T,
	fixture gatewayRebindEffectBoundaryFixture,
) []gatewayRebindProgressRecord {
	t.Helper()
	records := installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
	ownership, err := gatewayRebindStageNetworkOwnershipDigest(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	third, err := newGatewayRebindStageNetworkProgress(fixture.intent, records[1],
		strings.Repeat("d", 64), ownership, gatewayRebindProgressTimestamp(3))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, third.Sequence)
	if err != nil || store.installExact(context.Background(), third) != nil {
		t.Fatalf("install network progress: %v", err)
	}
	configOwnership, err := gatewayRebindStageConfigVolumeOwnershipDigest(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := newGatewayRebindStageConfigVolumeProgress(fixture.intent, third,
		gatewayRebindStageConfigVolumeBinding{
			Name:       fixture.intent.Intent.Identity.ConfigVolume,
			Mountpoint: "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.ConfigVolume + "/_data",
			CreatedAt:  "2026-10-03T12:00:03Z", OwnershipDigest: configOwnership,
		}, gatewayRebindProgressTimestamp(4))
	if err != nil {
		t.Fatal(err)
	}
	store, err = newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, fourth.Sequence)
	if err != nil || store.installExact(context.Background(), fourth) != nil {
		t.Fatalf("install config volume progress: %v", err)
	}
	return append(records, third, fourth)
}

func gatewayRebindStageDataVolumeTestDriver(fixture gatewayRebindEffectBoundaryFixture) (
	*gatewayRebindStageDataVolumeFake, gatewayRebindSuccessorPreflightReads,
) {
	fake := &gatewayRebindStageDataVolumeFake{
		intent: fixture.intent, networkID: strings.Repeat("d", 64), imageID: strings.Repeat("b", 64),
		configFound:   true,
		configMount:   "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.ConfigVolume + "/_data",
		configCreated: "2026-10-03T12:00:03Z",
		mountpoint:    "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.DataVolume + "/_data",
		createdAt:     "2026-10-03T12:00:04Z",
	}
	reads := fixture.reads
	originalIDs, originalDocker := reads.dockerIDs, reads.network.docker
	reads.dockerIDs = func(ctx context.Context) ([]string, error) {
		ids, err := originalIDs(ctx)
		ids = append(ids, fake.networkID)
		slices.Sort(ids)
		return ids, err
	}
	reads.network.docker = func(ctx context.Context) ([]netip.Prefix, error) {
		prefixes, err := originalDocker(ctx)
		prefixes = append(prefixes, netip.MustParsePrefix(fixture.intent.Intent.Network.Subnet))
		return prefixes, err
	}
	return fake, reads
}

func assertGatewayRebindStageDataVolumeRejected(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	fake *gatewayRebindStageDataVolumeFake, err error, wantCreate int,
) {
	t.Helper()
	if err == nil {
		t.Fatal("stage data volume unexpectedly succeeded")
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 4 || fake.createCalls != wantCreate {
		t.Fatalf("rejected stage mutated protected state: progress=%d create=%d scan_error=%v",
			len(history.Progress), fake.createCalls, scanErr)
	}
}
