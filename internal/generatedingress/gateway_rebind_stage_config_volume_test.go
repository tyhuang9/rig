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

type gatewayRebindStageConfigVolumeFake struct {
	intent         gatewayRebindProtectedIntent
	networkID      string
	imageID        string
	found          bool
	foreign        bool
	extraVolume    bool
	dataFound      bool
	stageFound     bool
	ownedContainer bool
	mountpoint     string
	createdAt      string
	createName     string
	createErr      error
	createAbsent   bool
	createCalls    int
	inspectCalls   int
	onInspect      func(*gatewayRebindStageConfigVolumeFake)
	onCreate       func()
}

func (f *gatewayRebindStageConfigVolumeFake) inspect(_ context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageConfigVolumeObservation, error) {
	f.inspectCalls++
	if f.onInspect != nil {
		f.onInspect(f)
	}
	if !reflect.DeepEqual(intent, f.intent) {
		return gatewayRebindStageConfigVolumeObservation{}, errors.New("unexpected intent")
	}
	bridgeName, _ := gatewayRebindStageBridgeName(intent)
	value := gatewayRebindStageConfigVolumeObservation{
		NetworkFound: true, NetworkID: f.networkID,
		Network: caddyNetworkInspection{
			Name: intent.Intent.Identity.IngressNetwork, Driver: "bridge", Scope: "local",
			Options: map[string]string{gatewayRebindBridgeNameOptionKey: bridgeName},
			Labels:  gatewayRebindStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
			IPAM: caddyNetworkIPAM{Config: []networkIPAM{{
				Subnet: intent.Intent.Network.Subnet, Gateway: intent.Intent.Network.GatewayIPv4,
			}}}, Containers: map[string]caddyNetworkContainerInspection{},
		},
		DataVolumeFound:     f.dataFound,
		StageContainerFound: f.stageFound,
		OwnedNetworks:       []string{intent.Intent.Identity.IngressNetwork},
	}
	if f.ownedContainer {
		value.OwnedContainers = []string{intent.Intent.Identity.StageContainer}
	}
	if f.found {
		labels := gatewayRebindStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)
		if f.foreign {
			labels[gatewayV2OperationLabelKey] = "11111111-1111-4111-8111-111111111111"
		}
		value.ConfigVolumeFound = true
		value.ConfigVolume = volumeInspection{
			Name: intent.Intent.Identity.ConfigVolume, Driver: "local", Scope: "local",
			Options: map[string]string{}, Labels: labels,
		}
		value.ConfigVolumeIdentity = gatewayV1VolumeIdentity{Mountpoint: f.mountpoint, CreatedAt: f.createdAt}
		value.OwnedVolumes = []string{intent.Intent.Identity.ConfigVolume}
	}
	if f.dataFound {
		value.OwnedVolumes = append(value.OwnedVolumes, intent.Intent.Identity.DataVolume)
	}
	if f.extraVolume {
		value.OwnedVolumes = append(value.OwnedVolumes, "rig-rebind-unexpected")
	}
	slices.Sort(value.OwnedVolumes)
	return value, nil
}

func (f *gatewayRebindStageConfigVolumeFake) inspectImage(context.Context) (imageInspection, bool, error) {
	return imageInspection{ID: "sha256:" + f.imageID, OS: "linux",
		RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}, true, nil
}

func (f *gatewayRebindStageConfigVolumeFake) create(_ context.Context,
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
	return f.intent.Intent.Identity.ConfigVolume, f.createErr
}

func TestGatewayRebindStageConfigVolumeCreateArgsAreGenerationScopedAndPrivate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	args, err := gatewayRebindStageConfigVolumeCreateArgs(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, required := range []string{
		fixture.intent.Intent.Identity.ConfigVolume,
		gatewayV2IdentityLabelKey + "=" + gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey + "=" + fixture.intent.OperationID,
		gatewayRebindGenerationLabelKey + "=" + strconv.FormatUint(fixture.intent.Generation, 10),
		gatewayRebindIntentDigestLabelKey + "=" + fixture.intent.Digest,
		gatewayV2ResourceRoleLabelKey + "=" + gatewayV2ConfigVolumeRole,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("volume create args omit %q: %#v", required, args)
		}
	}
	for _, prohibited := range []string{"--publish", "container", "network"} {
		if slices.Contains(args, prohibited) {
			t.Fatalf("config-volume-only create args contain %q: %#v", prohibited, args)
		}
	}
}

func TestGatewayRebindStageConfigVolumeCreatesBindsAndReplays(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
	fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
	before, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpointReached := false
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4),
		func() { checkpointReached = true }); err != nil {
		history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		t.Fatalf("stage config volume: %v found=%t create=%d inspect=%d checkpoint=%t progress=%d scan=%v",
			err, fake.found, fake.createCalls, fake.inspectCalls, checkpointReached, len(history.Progress), scanErr)
	}
	if fake.createCalls != 1 {
		t.Fatalf("volume create calls=%d, want 1", fake.createCalls)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 4 {
		t.Fatalf("progress=%#v error=%v", history.Progress, err)
	}
	bound := history.Progress[3].Record
	boundHistory := append([]gatewayRebindProgressSelection(nil), history.Progress...)
	boundBytes := make([][]byte, len(history.Progress))
	for index, entry := range history.Progress {
		boundBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	ownership, err := gatewayRebindStageConfigVolumeOwnershipDigest(fixture.intent)
	if err != nil || bound.PreviousDigest != records[2].Digest || bound.Stage == nil ||
		bound.Stage.ConfigVolume == nil || bound.Stage.ConfigVolume.Name != fixture.intent.Intent.Identity.ConfigVolume ||
		bound.Stage.ConfigVolume.Mountpoint != fake.mountpoint || bound.Stage.ConfigVolume.CreatedAt != fake.createdAt ||
		bound.Stage.ConfigVolume.OwnershipDigest != ownership {
		t.Fatalf("invalid bound config volume progress: %#v error=%v", bound, err)
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite claim changed during config volume staging: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil); err != nil {
		t.Fatalf("restart replay failed: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("restart recreated bound config volume: calls=%d", fake.createCalls)
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
		t.Fatal("prepared pre-effect attestor accepted sequence four")
	}
	if err := fixture.predecessor.manager.inspectGatewayRebindPreparedDockerPredecessor(
		context.Background(), fixture.predecessor.repository, nil,
		gatewayRebindStageNetworkInspect(t, fixture)); err == nil {
		t.Fatal("public prepared predecessor attestor accepted sequence four")
	}

	networkFake := &gatewayRebindStageNetworkFake{
		intent: fixture.intent, id: fake.networkID, found: true, imageID: fake.imageID,
	}
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorNetworkWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), networkFake, gatewayRebindProgressTimestamp(6), nil); err == nil {
		t.Fatal("network-stage entrypoint accepted sequence four")
	}
	if networkFake.createCalls != 0 {
		t.Fatalf("network-stage entrypoint mutated Docker after sequence four: %d", networkFake.createCalls)
	}
}

func TestGatewayRebindStageConfigVolumeRejectsUnboundAndInvalidEffects(t *testing.T) {
	for _, test := range []struct {
		name        string
		before      bool
		preMutate   func(*gatewayRebindStageConfigVolumeFake)
		mutate      func(*gatewayRebindStageConfigVolumeFake)
		wantCreated bool
	}{
		{name: "unbound exact volume", before: true},
		{name: "stage container already exists", preMutate: func(f *gatewayRebindStageConfigVolumeFake) { f.stageFound = true }},
		{name: "owned container after create", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.ownedContainer = true }, wantCreated: true},
		{name: "foreign labels after create", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.foreign = true }, wantCreated: true},
		{name: "invalid created at", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.createdAt = "not-a-time" }, wantCreated: true},
		{name: "extra owned volume", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.extraVolume = true }, wantCreated: true},
		{name: "data volume appeared", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.dataFound = true }, wantCreated: true},
		{name: "create returned wrong name", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.createName = "wrong" }, wantCreated: true},
		{name: "create returned error", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.createErr = errors.New("create uncertain") }, wantCreated: true},
		{name: "create reported success but resource absent", mutate: func(f *gatewayRebindStageConfigVolumeFake) { f.createAbsent = true }, wantCreated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
			fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
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
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 3 {
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

func TestGatewayRebindStageConfigVolumeReplayRejectsIdentityAndCensusSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageConfigVolumeFake, *gatewayRebindSuccessorPreflightReads)
	}{
		{name: "mountpoint", mutate: func(fake *gatewayRebindStageConfigVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.mountpoint += "-replacement"
		}},
		{name: "created at", mutate: func(fake *gatewayRebindStageConfigVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.createdAt = "2026-10-03T12:09:03Z"
		}},
		{name: "foreign labels", mutate: func(fake *gatewayRebindStageConfigVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.foreign = true
		}},
		{name: "extra owned volume", mutate: func(fake *gatewayRebindStageConfigVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.extraVolume = true
		}},
		{name: "network ID", mutate: func(fake *gatewayRebindStageConfigVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			fake.networkID = strings.Repeat("e", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
			fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil); err != nil {
				t.Fatal(err)
			}
			test.mutate(fake, &reads)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil); err == nil {
				t.Fatal("replay accepted substituted Docker identity or census")
			}
			if fake.createCalls != 1 {
				t.Fatalf("replay mutated Docker after substitution: create calls=%d", fake.createCalls)
			}
		})
	}
}

func TestGatewayRebindStageConfigVolumeRejectsStaleAuthorizationBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindEffectBoundaryFixture, *gatewayRebindStageConfigVolumeFake, *gatewayRebindSuccessorPreflightReads)
	}{
		{name: "stale image", mutate: func(_ *gatewayRebindEffectBoundaryFixture, f *gatewayRebindStageConfigVolumeFake, _ *gatewayRebindSuccessorPreflightReads) {
			f.imageID = strings.Repeat("9", 64)
		}},
		{name: "extra Docker network", mutate: func(_ *gatewayRebindEffectBoundaryFixture, _ *gatewayRebindStageConfigVolumeFake, reads *gatewayRebindSuccessorPreflightReads) {
			original := reads.dockerIDs
			reads.dockerIDs = func(ctx context.Context) ([]string, error) {
				values, err := original(ctx)
				values = append(values, strings.Repeat("9", 64))
				slices.Sort(values)
				return values, err
			}
		}},
		{name: "host topology drift", mutate: func(_ *gatewayRebindEffectBoundaryFixture, _ *gatewayRebindStageConfigVolumeFake, reads *gatewayRebindSuccessorPreflightReads) {
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
			installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
			fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
			test.mutate(&fixture, fake, &reads)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
			assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 0)
		})
	}

	t.Run("SQLite claim drift", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
		fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
		assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 0)
	})

	t.Run("predecessor Docker drift", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
		fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
			observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
			return observation, nil
		}
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
			context.Background(), fixture.predecessor.repository, reads, inspect, fake,
			gatewayRebindProgressTimestamp(4), nil)
		assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 0)
	})

	t.Run("drift adjacent to create", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
		fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
		original := reads.network.host
		drift := false
		reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
			value, err := original()
			if drift {
				value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("10.99.0.0/16"))
			}
			return value, err
		}
		err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), func() { drift = true })
		assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 0)
	})
}

func TestGatewayRebindStageConfigVolumeRejectsNonMonotonicReceiptTimeBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name     string
		sequence uint64
	}{
		{name: "equal", sequence: 3},
		{name: "older", sequence: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
			fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(test.sequence), nil)
			assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 0)
		})
	}
}

func TestGatewayRebindStageConfigVolumeRejectsClaimDriftAfterCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
	fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
	fake.onCreate = func() {
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
	}
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
	assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 1)
	if !fake.found {
		t.Fatal("injected claim drift did not occur after volume creation")
	}
}

func TestGatewayRebindStageConfigVolumeRejectsCheckpointSequenceThreeRemovalBeforeCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
	fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 3)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointErr error
	err = fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4),
		func() { checkpointErr = os.Remove(store.path) })
	if checkpointErr != nil {
		t.Fatalf("remove sequence-three checkpoint: %v", checkpointErr)
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil || scanErr != nil || len(history.Progress) != 2 || fake.createCalls != 0 {
		t.Fatalf("removed sequence three reached create: error=%v progress=%d create=%d scan_error=%v",
			err, len(history.Progress), fake.createCalls, scanErr)
	}
}

func TestGatewayRebindStageConfigVolumeRejectsUnstablePostCreateIdentity(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
	fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
	fake.onInspect = func(value *gatewayRebindStageConfigVolumeFake) {
		if value.found && value.inspectCalls == 8 {
			value.createdAt = "2026-10-03T12:09:03Z"
		}
	}
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
	assertGatewayRebindStageConfigVolumeRejected(t, fixture, fake, err, 1)
}

func TestGatewayRebindStageConfigVolumePreservesAmbiguousEffectAndWriteBoundaries(t *testing.T) {
	for _, test := range []struct {
		name            string
		ambiguousWrite  bool
		cancel          bool
		restartSucceeds bool
	}{
		{name: "protected write rejected"},
		{name: "protected write installed ambiguously", ambiguousWrite: true, restartSucceeds: true},
		{name: "context cancelled after create", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
			fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
			ctx := context.Background()
			originalWrite := upgradeProtectedWriteNew
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				fake.onCreate = cancel
			} else {
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
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				ctx, fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
			if err == nil || !fake.found || fake.createCalls != 1 {
				t.Fatalf("injected boundary was not preserved: error=%v found=%t create=%d", err, fake.found, fake.createCalls)
			}
			if !test.cancel {
				upgradeProtectedWriteNew = originalWrite
			}
			fake.onCreate = nil
			restartErr := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(5), nil)
			if test.restartSucceeds && restartErr != nil {
				t.Fatalf("fresh restart did not replay installed binding: %v", restartErr)
			}
			if !test.restartSucceeds && restartErr == nil {
				t.Fatal("fresh restart adopted an unbound existing config volume")
			}
			if fake.createCalls != 1 {
				t.Fatalf("recovery recreated existing config volume: calls=%d", fake.createCalls)
			}
		})
	}
}

func TestGatewayRebindStageConfigVolumeReleaseErrorsPreserveExactBinding(t *testing.T) {
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
			installGatewayRebindStageConfigVolumeNetworkProgress(t, fixture)
			fake, reads := gatewayRebindStageConfigVolumeTestDriver(fixture)
			test.inject(t)
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorConfigVolumeWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(4), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 4 || fake.createCalls != 1 {
				t.Fatalf("release error lost or corrupted binding: error=%v progress=%d create=%d scan_error=%v",
					err, len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func installGatewayRebindStageConfigVolumeNetworkProgress(t *testing.T,
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
	return append(records, third)
}

func gatewayRebindStageConfigVolumeTestDriver(fixture gatewayRebindEffectBoundaryFixture) (
	*gatewayRebindStageConfigVolumeFake, gatewayRebindSuccessorPreflightReads,
) {
	fake := &gatewayRebindStageConfigVolumeFake{
		intent: fixture.intent, networkID: strings.Repeat("d", 64), imageID: strings.Repeat("b", 64),
		mountpoint: "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.ConfigVolume + "/_data",
		createdAt:  "2026-10-03T12:00:03Z",
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

func assertGatewayRebindStageConfigVolumeRejected(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	fake *gatewayRebindStageConfigVolumeFake, err error, wantCreate int,
) {
	t.Helper()
	if err == nil {
		t.Fatal("stage config volume unexpectedly succeeded")
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 3 || fake.createCalls != wantCreate {
		t.Fatalf("rejected stage mutated protected state: progress=%d create=%d scan_error=%v",
			len(history.Progress), fake.createCalls, scanErr)
	}
}
