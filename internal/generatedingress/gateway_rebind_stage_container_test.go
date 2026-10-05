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

type gatewayRebindStageContainerFake struct {
	intent        gatewayRebindProtectedIntent
	stage         gatewayRebindStageIntent
	volumes       gatewayRebindStageDataVolumeFake
	found         bool
	foreign       bool
	running       bool
	effectivePort bool
	wrongNetwork  bool
	extraOwned    bool
	containerID   string
	createID      string
	createErr     error
	createAbsent  bool
	createCalls   int
	inspectCalls  int
	onInspect     func(*gatewayRebindStageContainerFake)
	onCreate      func()
}

func (f *gatewayRebindStageContainerFake) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	f.inspectCalls++
	if f.onInspect != nil {
		f.onInspect(f)
	}
	base, err := f.volumes.inspect(ctx, intent)
	if err != nil || !reflect.DeepEqual(intent, f.intent) {
		return gatewayRebindStageContainerObservation{}, errors.New("unexpected intent")
	}
	value := gatewayRebindStageContainerObservation{
		Network: base.Network, NetworkID: base.NetworkID, NetworkFound: base.NetworkFound,
		ConfigVolume: base.ConfigVolume, ConfigVolumeIdentity: base.ConfigVolumeIdentity,
		ConfigVolumeFound: base.ConfigVolumeFound, DataVolume: base.DataVolume,
		DataVolumeIdentity: base.DataVolumeIdentity, DataVolumeFound: base.DataVolumeFound,
		OwnedVolumes: base.OwnedVolumes, OwnedNetworks: base.OwnedNetworks,
	}
	if f.found {
		value.StageContainerFound = true
		value.OwnedContainers = []string{intent.Intent.Identity.StageContainer}
		value.StageContainer, value.StageRuntime = gatewayRebindStageContainerTestInspection(f)
	}
	if f.extraOwned {
		value.OwnedContainers = append(value.OwnedContainers, "rig-rebind-unexpected")
		slices.Sort(value.OwnedContainers)
	}
	return value, nil
}

func (f *gatewayRebindStageContainerFake) inspectImage(context.Context) (imageInspection, bool, error) {
	return imageInspection{ID: "sha256:" + f.stage.ObservedDockerImageID, OS: "linux",
		RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}, true, nil
}

func (f *gatewayRebindStageContainerFake) create(_ context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) (string, error) {
	f.createCalls++
	if !reflect.DeepEqual(intent, f.intent) || !reflect.DeepEqual(stage, f.stage) {
		return "", errors.New("unexpected create input")
	}
	if !f.createAbsent {
		f.found = true
	}
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createID != "" {
		return f.createID, f.createErr
	}
	return f.containerID, f.createErr
}

func gatewayRebindStageContainerTestInspection(f *gatewayRebindStageContainerFake) (caddyInspection,
	gatewayContainerRuntime,
) {
	intent := f.intent
	labels := gatewayRebindStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole)
	if f.foreign {
		labels[gatewayV2OperationLabelKey] = "11111111-1111-4111-8111-111111111111"
	}
	bindings := make(map[string][]map[string]string)
	for port := intent.Intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		bindings[value+"/tcp"] = []map[string]string{{
			"HostIp": intent.Intent.SuccessorProfile.SelectedIPv4, "HostPort": value,
		}}
		if port == intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
	networkID := f.stage.Network.ID
	if f.wrongNetwork {
		networkID = strings.Repeat("e", 64)
	}
	configured := map[string]gatewayV2ConfiguredNetwork{
		intent.Intent.Identity.IngressNetwork: {
			IPAMConfig: &gatewayV2ConfiguredIPAM{IPv4Address: intent.Intent.Network.ContainerIPv4},
			NetworkID:  networkID, GwPriority: caddyGatewayPriority,
		},
	}
	effective := map[string][]map[string]string{}
	if f.effectivePort {
		effective[strconv.FormatUint(uint64(intent.Intent.SuccessorProfile.PortStart), 10)+"/tcp"] =
			[]map[string]string{{"HostIp": intent.Intent.SuccessorProfile.SelectedIPv4,
				"HostPort": strconv.FormatUint(uint64(intent.Intent.SuccessorProfile.PortStart), 10)}}
	}
	return caddyInspection{
		ID: f.containerID, Name: "/" + intent.Intent.Identity.StageContainer,
		Image: "sha256:" + f.stage.ObservedDockerImageID, Labels: labels,
		Hostname: intent.Intent.Identity.StageHostname, User: "1000:1000",
		Env:        []string{"PATH=/usr/local/bin", "XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"},
		Entrypoint: []string{caddyExecutable}, Cmd: []string{"run", "--config", "/config/" + intent.Intent.Identity.StageConfigFilename},
		ReadOnly: true, CapAdd: []string{caddyCapability}, CapDrop: []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		Mounts: []mountInspection{
			{Type: "volume", Name: intent.Intent.Identity.ConfigVolume, Destination: "/config", RW: true},
			{Type: "volume", Name: intent.Intent.Identity.DataVolume, Destination: "/data", RW: true},
		},
		Memory: 268435456, MemorySwap: 268435456, NanoCPUs: 1_000_000_000, PIDsLimit: 128,
		LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"},
		Restart: gatewayV2StageRestartPolicy, NetworkMode: intent.Intent.Identity.IngressNetwork,
		Ulimits: []ulimitInspection{{Name: "nofile", Hard: 1024, Soft: 1024}}, Running: f.running,
		PortBindings: bindings,
	}, gatewayContainerRuntime{EffectivePortBindings: effective, ConfiguredNetworks: configured}
}

func TestGatewayRebindStageContainerCreateArgsAreGenerationScopedStoppedAndPrivate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	stage := *records[4].Stage
	args, err := gatewayRebindStageContainerCreateArgs(fixture.intent, stage)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, required := range []string{
		"container\x00create", "--name\x00" + fixture.intent.Intent.Identity.StageContainer,
		"--hostname\x00" + fixture.intent.Intent.Identity.StageHostname,
		"--network\x00name=" + fixture.intent.Intent.Identity.IngressNetwork + ",ip=" + fixture.intent.Intent.Network.ContainerIPv4 + ",gw-priority=1",
		"--restart\x00no", "--user\x001000:1000", "--read-only", "--cap-drop\x00ALL",
		"--cap-add\x00" + caddyCapability, "--security-opt\x00no-new-privileges",
		"sha256:" + stage.ObservedDockerImageID, "/config/" + fixture.intent.Intent.Identity.StageConfigFilename,
		gatewayRebindGenerationLabelKey + "=" + strconv.FormatUint(fixture.intent.Generation, 10),
		gatewayV2ResourceRoleLabelKey + "=" + gatewayV2StageContainerRole,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("container create args omit %q: %#v", required, args)
		}
	}
	for port := fixture.intent.Intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		if !strings.Contains(joined, "--publish\x00"+fixture.intent.Intent.SuccessorProfile.SelectedIPv4+":"+value+":"+value+"/tcp") {
			t.Fatalf("container create args omit selected pool port %s", value)
		}
		if port == fixture.intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
	for _, prohibited := range []string{"start", "cp"} {
		if slices.Contains(args, prohibited) {
			t.Fatalf("stopped-container-only create args contain %q: %#v", prohibited, args)
		}
	}
	if strings.Contains(joined, "127.0.0.1:") {
		t.Fatalf("stopped stage create args include a loopback publication: %#v", args)
	}
}

func TestGatewayRebindStageContainerCreatesBindsAndReplays(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	baseHistory, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(baseHistory.Progress) != 5 {
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
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6),
		func() { checkpointReached = true }); err != nil {
		t.Fatalf("stage stopped container: %v found=%t create=%d inspect=%d checkpoint=%t",
			err, fake.found, fake.createCalls, fake.inspectCalls, checkpointReached)
	}
	if fake.createCalls != 1 || !fake.found || fake.running {
		t.Fatalf("container effect create=%d found=%t running=%t", fake.createCalls, fake.found, fake.running)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 6 {
		t.Fatalf("progress=%#v error=%v", history.Progress, err)
	}
	for index := range baseHistory.Progress {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !reflect.DeepEqual(baseHistory.Progress[index].Record, history.Progress[index].Record) ||
			!reflect.DeepEqual(baseBytes[index], body) || records[index].Digest != history.Progress[index].Record.Digest {
			t.Fatalf("sequence %d changed while appending stopped-container receipt: read=%v", index+1, readErr)
		}
	}
	bound := history.Progress[5].Record
	ownership, err := gatewayRebindStageContainerOwnershipDigest(fixture.intent)
	configuration, configErr := gatewayRebindStageContainerConfigurationDigest(fixture.intent, *records[4].Stage)
	if err != nil || configErr != nil || bound.Stage == nil || bound.Stage.StageContainer == nil ||
		bound.Stage.StageContainer.ID != fake.containerID || bound.Stage.StageContainer.OwnershipDigest != ownership ||
		bound.Stage.StageContainer.ConfigurationDigest != configuration {
		t.Fatalf("invalid bound stopped container progress: %#v errors=%v/%v", bound, err, configErr)
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite claim changed during container staging: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	replayReads := gatewayRebindStageContainerReads(fixture, fake)
	if err := restarted.stageGatewayRebindSuccessorContainerWithDriver(
		context.Background(), fixture.predecessor.repository, replayReads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), nil); err != nil {
		t.Fatalf("restart replay failed: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("restart recreated bound stage container: calls=%d", fake.createCalls)
	}
	dataFake, dataReads := gatewayRebindStageDataVolumeTestDriver(fixture)
	if err := restarted.stageGatewayRebindSuccessorDataVolumeWithDriver(
		context.Background(), fixture.predecessor.repository, dataReads,
		gatewayRebindStageNetworkInspect(t, fixture), dataFake, gatewayRebindProgressTimestamp(7), nil); err == nil {
		t.Fatal("sequence-five-only data-volume writer accepted sequence-six history")
	}
	if dataFake.createCalls != 0 {
		t.Fatalf("older data-volume writer mutated Docker after sequence six: calls=%d", dataFake.createCalls)
	}
}

func TestGatewayRebindStageContainerRejectsPreCreateAndPostCreateDrift(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*gatewayRebindStageContainerFake)
		checkpoint func(*gatewayRebindStageContainerFake) func()
		wantCreate int
	}{
		{name: "existing unbound container", configure: func(fake *gatewayRebindStageContainerFake) { fake.found = true }},
		{name: "container absent after create", configure: func(fake *gatewayRebindStageContainerFake) { fake.createAbsent = true }, wantCreate: 1},
		{name: "foreign container labels after create", configure: func(fake *gatewayRebindStageContainerFake) { fake.foreign = true }, wantCreate: 1},
		{name: "running container after create", configure: func(fake *gatewayRebindStageContainerFake) { fake.running = true }, wantCreate: 1},
		{name: "effective host bind after create", configure: func(fake *gatewayRebindStageContainerFake) { fake.effectivePort = true }, wantCreate: 1},
		{name: "wrong configured network after create", configure: func(fake *gatewayRebindStageContainerFake) { fake.wrongNetwork = true }, wantCreate: 1},
		{name: "extra owned container after create", configure: func(fake *gatewayRebindStageContainerFake) {
			fake.onCreate = func() { fake.extraOwned = true }
		}, wantCreate: 1},
		{name: "owned volume drift before create", checkpoint: func(fake *gatewayRebindStageContainerFake) func() {
			return func() { fake.volumes.extraVolume = true }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			records := installGatewayRebindStageContainerDataProgress(t, fixture)
			fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
			if test.configure != nil {
				test.configure(fake)
			}
			var checkpoint func()
			if test.checkpoint != nil {
				checkpoint = test.checkpoint(fake)
			}
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), checkpoint)
			assertGatewayRebindStageContainerRejected(t, fixture, fake, err, test.wantCreate)
		})
	}
}

func TestGatewayRebindStageContainerRejectsHostTopologyDriftAdjacentToCreate(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	original := reads.network.host
	drift := false
	reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
		value, err := original()
		if drift {
			value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("10.99.0.0/16"))
		}
		return value, err
	}
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6),
		func() { drift = true })
	assertGatewayRebindStageContainerRejected(t, fixture, fake, err, 0)
}

func TestGatewayRebindStageContainerRejectsReturnedIDMismatch(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	fake.createID = strings.Repeat("f", 64)
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil)
	assertGatewayRebindStageContainerRejected(t, fixture, fake, err, 1)
}

func TestGatewayRebindStageContainerReplayRejectsPhysicalDriftWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageContainerFake)
	}{
		{name: "missing container", mutate: func(fake *gatewayRebindStageContainerFake) { fake.found = false }},
		{name: "replaced container id", mutate: func(fake *gatewayRebindStageContainerFake) { fake.containerID = strings.Repeat("f", 64) }},
		{name: "running container", mutate: func(fake *gatewayRebindStageContainerFake) { fake.running = true }},
		{name: "effective host bind", mutate: func(fake *gatewayRebindStageContainerFake) { fake.effectivePort = true }},
		{name: "wrong ownership labels", mutate: func(fake *gatewayRebindStageContainerFake) { fake.foreign = true }},
		{name: "wrong configured network", mutate: func(fake *gatewayRebindStageContainerFake) { fake.wrongNetwork = true }},
		{name: "extra owned container", mutate: func(fake *gatewayRebindStageContainerFake) { fake.extraOwned = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			records := installGatewayRebindStageContainerDataProgress(t, fixture)
			fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
			if err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil); err != nil {
				t.Fatalf("install valid sequence-six receipt: %v", err)
			}
			history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.Progress) != 6 {
				t.Fatalf("base sequence-six history=%d error=%v", len(history.Progress), err)
			}
			before := make([][]byte, len(history.Progress))
			for index, entry := range history.Progress {
				before[index], err = os.ReadFile(entry.Store.path)
				if err != nil {
					t.Fatal(err)
				}
			}
			test.mutate(fake)
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			replayReads := gatewayRebindStageContainerReads(fixture, fake)
			if err := restarted.stageGatewayRebindSuccessorContainerWithDriver(
				context.Background(), fixture.predecessor.repository, replayReads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), nil); err == nil {
				t.Fatal("fresh Manager accepted drifted sequence-six container")
			}
			if fake.createCalls != 1 {
				t.Fatalf("replay mutated Docker after drift: create calls=%d", fake.createCalls)
			}
			after, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || !reflect.DeepEqual(history.Progress, after.Progress) {
				t.Fatalf("replay changed protected history: error=%v", scanErr)
			}
			for index, entry := range after.Progress {
				body, readErr := os.ReadFile(entry.Store.path)
				if readErr != nil || !reflect.DeepEqual(before[index], body) {
					t.Fatalf("replay changed sequence %d bytes: error=%v", index+1, readErr)
				}
			}
		})
	}
}

func TestGatewayRebindStageContainerPreservesAmbiguousCreateAndWriteBoundaries(t *testing.T) {
	for _, test := range []struct {
		name            string
		createError     bool
		ambiguousWrite  bool
		restartSucceeds bool
	}{
		{name: "Docker create returned an error after creating container", createError: true},
		{name: "protected write rejected"},
		{name: "protected write installed ambiguously", ambiguousWrite: true, restartSucceeds: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			records := installGatewayRebindStageContainerDataProgress(t, fixture)
			fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
			if test.createError {
				fake.createErr = errors.New("injected Docker create uncertainty")
			}
			originalWrite := upgradeProtectedWriteNew
			if !test.createError {
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
			err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil)
			if err == nil || !fake.found || fake.createCalls != 1 {
				t.Fatalf("injected boundary was not preserved: error=%v found=%t create=%d", err, fake.found, fake.createCalls)
			}
			if !test.createError {
				upgradeProtectedWriteNew = originalWrite
			}
			fake.createErr = nil
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			replayReads := gatewayRebindStageContainerReads(fixture, fake)
			restartErr := restarted.stageGatewayRebindSuccessorContainerWithDriver(
				context.Background(), fixture.predecessor.repository, replayReads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), nil)
			if test.restartSucceeds && restartErr != nil {
				t.Fatalf("fresh restart did not replay installed binding: %v", restartErr)
			}
			if !test.restartSucceeds && restartErr == nil {
				t.Fatal("fresh restart adopted an unbound existing stage container")
			}
			if fake.createCalls != 1 {
				t.Fatalf("recovery recreated existing stage container: calls=%d", fake.createCalls)
			}
		})
	}
}

func TestGatewayRebindStageContainerCancellationAfterCreateLeavesUnboundEffect(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	ctx, cancel := context.WithCancel(context.Background())
	fake.onCreate = cancel
	err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
		ctx, fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(6), nil)
	if err == nil || !fake.found || fake.createCalls != 1 {
		t.Fatalf("post-create cancellation was not preserved: error=%v found=%t create=%d",
			err, fake.found, fake.createCalls)
	}
	fake.onCreate = nil
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	replayReads := gatewayRebindStageContainerReads(fixture, fake)
	if err := restarted.stageGatewayRebindSuccessorContainerWithDriver(
		context.Background(), fixture.predecessor.repository, replayReads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), nil); err == nil {
		t.Fatal("restart adopted the unbound container left by cancellation")
	}
	if fake.createCalls != 1 {
		t.Fatalf("restart recreated the ambiguous container: calls=%d", fake.createCalls)
	}
}

func installGatewayRebindStageContainerDataProgress(t *testing.T,
	fixture gatewayRebindEffectBoundaryFixture,
) []gatewayRebindProgressRecord {
	t.Helper()
	records := installGatewayRebindStageDataVolumeConfigProgress(t, fixture)
	ownership, err := gatewayRebindStageDataVolumeOwnershipDigest(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	fifth, err := newGatewayRebindStageDataVolumeProgress(fixture.intent, records[3],
		gatewayRebindStageDataVolumeBinding{
			Name:       fixture.intent.Intent.Identity.DataVolume,
			Mountpoint: "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.DataVolume + "/_data",
			CreatedAt:  "2026-10-03T12:00:04Z", OwnershipDigest: ownership,
		}, gatewayRebindProgressTimestamp(5))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, fifth.Sequence)
	if err != nil || store.installExact(context.Background(), fifth) != nil {
		t.Fatalf("install data volume progress: %v", err)
	}
	return append(records, fifth)
}

func gatewayRebindStageContainerTestDriver(fixture gatewayRebindEffectBoundaryFixture,
	stage gatewayRebindStageIntent,
) (*gatewayRebindStageContainerFake, gatewayRebindSuccessorPreflightReads) {
	fake := &gatewayRebindStageContainerFake{
		intent: fixture.intent, stage: stage, containerID: strings.Repeat("c", 64),
		volumes: gatewayRebindStageDataVolumeFake{
			intent: fixture.intent, networkID: strings.Repeat("d", 64), imageID: strings.Repeat("b", 64),
			configFound:   true,
			configMount:   "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.ConfigVolume + "/_data",
			configCreated: "2026-10-03T12:00:03Z", found: true,
			mountpoint: "/var/lib/docker/volumes/" + fixture.intent.Intent.Identity.DataVolume + "/_data",
			createdAt:  "2026-10-03T12:00:04Z",
		},
	}
	return fake, gatewayRebindStageContainerReads(fixture, fake)
}

func gatewayRebindStageContainerReads(fixture gatewayRebindEffectBoundaryFixture,
	fake *gatewayRebindStageContainerFake,
) gatewayRebindSuccessorPreflightReads {
	reads := fixture.reads
	originalIDs, originalDocker := reads.dockerIDs, reads.network.docker
	reads.dockerIDs = func(ctx context.Context) ([]string, error) {
		ids, err := originalIDs(ctx)
		ids = append(ids, fake.volumes.networkID)
		slices.Sort(ids)
		return ids, err
	}
	reads.network.docker = func(ctx context.Context) ([]netip.Prefix, error) {
		prefixes, err := originalDocker(ctx)
		prefixes = append(prefixes, netip.MustParsePrefix(fixture.intent.Intent.Network.Subnet))
		return prefixes, err
	}
	return reads
}

func newGatewayRebindStageContainerManager(t *testing.T,
	fixture gatewayRebindEffectBoundaryFixture,
) *Manager {
	t.Helper()
	manager, err := New(fixture.predecessor.manager.runner, fixture.predecessor.manager.options)
	if err != nil {
		t.Fatalf("construct fresh Manager for restart: %v", err)
	}
	return manager
}

func assertGatewayRebindStageContainerRejected(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	fake *gatewayRebindStageContainerFake, err error, wantCreate int,
) {
	t.Helper()
	if err == nil {
		t.Fatal("stage container unexpectedly succeeded")
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 5 || fake.createCalls != wantCreate {
		t.Fatalf("rejected stage mutated protected state: progress=%d create=%d scan_error=%v",
			len(history.Progress), fake.createCalls, scanErr)
	}
}
