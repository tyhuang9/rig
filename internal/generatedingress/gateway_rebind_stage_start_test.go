package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindStageStartFake struct {
	*gatewayRebindStageConfigCopyFake
	endpointID       string
	startCalls       int
	stopCalls        int
	startErr         error
	startTakesEffect bool
	stopErr          error
	stopTakesEffect  bool
	liveConfigDrift  bool
	hostProbeDrift   bool
	containerDrift   bool
	loopbackLive     bool
	selectedStuck    bool
	loopbackStuck    bool
	hostProbeCalls   int
	containerCalls   int
	stoppedSelected  int
	stoppedLoopback  int
}

func (f *gatewayRebindStageStartFake) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	value, err := f.gatewayRebindStageConfigCopyFake.inspect(ctx, intent)
	if err != nil || !f.running {
		return value, err
	}
	value.StageContainer.Networks = map[string]*networkAttachment{
		intent.Intent.Identity.IngressNetwork: {
			IPAddress: intent.Intent.Network.ContainerIPv4, GwPriority: caddyGatewayPriority,
		},
	}
	value.StageRuntime.EffectivePortBindings = cloneGatewayRebindStagePortBindings(value.StageContainer.PortBindings)
	configured := value.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork]
	configured.NetworkID = f.stage.Network.ID
	configured.EndpointID = f.endpointID
	configured.IPAddress = intent.Intent.Network.ContainerIPv4
	value.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork] = configured
	subnet := strings.Split(intent.Intent.Network.Subnet, "/")[1]
	value.Network.Containers = map[string]caddyNetworkContainerInspection{
		f.containerID: {Name: intent.Intent.Identity.StageContainer,
			IPv4Address: intent.Intent.Network.ContainerIPv4 + "/" + subnet},
	}
	return value, nil
}

func cloneGatewayRebindStagePortBindings(values map[string][]map[string]string) map[string][]map[string]string {
	result := make(map[string][]map[string]string, len(values))
	for key, bindings := range values {
		result[key] = make([]map[string]string, len(bindings))
		for index, binding := range bindings {
			result[key][index] = map[string]string{"HostIp": binding["HostIp"], "HostPort": binding["HostPort"]}
		}
	}
	return result
}

func (f *gatewayRebindStageStartFake) start(_ context.Context, id string) error {
	f.startCalls++
	if id != f.containerID {
		return errors.New("unexpected start identity")
	}
	if f.startErr == nil || f.startTakesEffect {
		f.running = true
	}
	return f.startErr
}

func (f *gatewayRebindStageStartFake) stop(_ context.Context, id string) error {
	f.stopCalls++
	if id != f.containerID {
		return errors.New("unexpected stop identity")
	}
	if f.stopErr == nil || f.stopTakesEffect {
		f.running = false
	}
	return f.stopErr
}

func (f *gatewayRebindStageStartFake) liveConfig(context.Context, string) ([]byte, error) {
	value := append([]byte(nil), f.expected...)
	if f.liveConfigDrift {
		return []byte(`{"admin":{"listen":"localhost:2020"}}`), nil
	}
	return value, nil
}

func (f *gatewayRebindStageStartFake) hostProbe(_ context.Context, address string, port uint16,
	host, path string,
) gatewayV2HostProbeResult {
	f.hostProbeCalls++
	if address == "127.0.0.1" {
		if !f.running {
			f.stoppedLoopback++
		}
		return gatewayV2HostProbeResult{Connected: (f.loopbackLive && f.running) || f.loopbackStuck}
	}
	if !f.running && address == f.intent.Intent.SuccessorProfile.SelectedIPv4 {
		f.stoppedSelected++
		return gatewayV2HostProbeResult{Connected: f.selectedStuck}
	}
	if f.hostProbeDrift || address != f.intent.Intent.SuccessorProfile.SelectedIPv4 ||
		port < f.intent.Intent.SuccessorProfile.PortStart || port > f.intent.Intent.SuccessorProfile.PortEnd ||
		(host != address && host != "wrong.invalid") {
		return gatewayV2HostProbeResult{}
	}
	result := gatewayV2HostProbeResult{Status: http.StatusNotFound, Connected: true, Responded: true}
	if strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
		challenge := strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
		if host == address && challenge == gatewayV2PortChallenge(f.stage.StageStartIntent.ProbeToken, port) {
			result.Body = gatewayV2ChallengeBodyPrefix + challenge
		}
	}
	return result
}

func (f *gatewayRebindStageStartFake) containerProbe(_ context.Context, id, address string,
	port uint16, host, challenge string,
) bool {
	f.containerCalls++
	return !f.containerDrift && id == f.containerID && address == f.intent.Intent.Network.ContainerIPv4 &&
		port >= f.intent.Intent.SuccessorProfile.PortStart && port <= f.intent.Intent.SuccessorProfile.PortEnd &&
		host == f.intent.Intent.SuccessorProfile.SelectedIPv4 &&
		challenge == gatewayV2PortChallenge(f.stage.StageStartIntent.ProbeToken, port)
}

func TestGatewayRebindStageStartAppendsServingReceiptAndReplaysObservationOnly(t *testing.T) {
	fixture, fake, reads, records := installGatewayRebindStageServingIntent(t)
	beforeDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeRoute, err := fixture.predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	baseHistory, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(baseHistory.Progress) != 9 {
		t.Fatalf("base history=%d error=%v", len(baseHistory.Progress), err)
	}
	baseBytes := make([][]byte, len(baseHistory.Progress))
	for index := range baseHistory.Progress {
		baseBytes[index], err = os.ReadFile(baseHistory.Progress[index].Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := false
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), func() { checkpoint = true }); err != nil {
		t.Fatalf("start exact rebind stage: %v", err)
	}
	if !checkpoint || !fake.running || fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("effect checkpoint=%t running=%t starts=%d stops=%d",
			checkpoint, fake.running, fake.startCalls, fake.stopCalls)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 10 {
		t.Fatalf("serving history=%d error=%v", len(history.Progress), err)
	}
	for index := range baseBytes {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !bytes.Equal(body, baseBytes[index]) ||
			history.Progress[index].Record.Digest != records[index].Digest {
			t.Fatalf("sequence %d changed while appending serving receipt: %v", index+1, readErr)
		}
	}
	serving := history.Progress[9].Record
	if serving.Sequence != 10 || serving.Phase != gatewayRebindProgressStageServing || serving.Stage == nil ||
		serving.Stage.StageServing == nil || serving.Stage.StageServing.EndpointID != fake.endpointID ||
		serving.Stage.StageServing.PriorProgressDigest != records[8].Digest {
		t.Fatalf("invalid stage serving receipt: %#v", serving)
	}
	afterDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) {
		t.Fatalf("stage start changed SQLite: equal=%t error=%v", reflect.DeepEqual(beforeDatabase, afterDatabase), err)
	}
	afterRoute, err := fixture.predecessor.manager.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatalf("stage start changed route state: equal=%t error=%v", reflect.DeepEqual(beforeRoute, afterRoute), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatalf("fresh Manager replay: %v", err)
	}
	if fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("receipt replay mutated Docker: starts=%d stops=%d", fake.startCalls, fake.stopCalls)
	}
}

func TestGatewayRebindStageStartAdoptsExactRunningLostAcknowledgement(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	fake.running = true
	fake.startErr = errors.New("lost start acknowledgement")
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("adopt exact running stage: %v", err)
	}
	if fake.startCalls != 0 || fake.stopCalls != 0 {
		t.Fatalf("running adoption dispatched effect: starts=%d stops=%d", fake.startCalls, fake.stopCalls)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 10 {
		t.Fatalf("adoption history=%d error=%v", len(history.Progress), err)
	}
}

func TestGatewayRebindStageStartErrorAfterEffectAdoptsExactServingStage(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	fake.startErr = errors.New("lost Docker start response")
	fake.startTakesEffect = true
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("lost start acknowledgement was not adopted: %v", err)
	}
	if fake.startCalls != 1 || fake.stopCalls != 0 || !fake.running {
		t.Fatalf("lost acknowledgement effect starts=%d stops=%d running=%t",
			fake.startCalls, fake.stopCalls, fake.running)
	}
}

func TestGatewayRebindStageStartErrorWithoutEffectLeavesProvenStoppedSequenceNine(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	fake.startErr = errors.New("Docker rejected start")
	err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil)
	if err == nil || candidateMayBeLive(err) || fake.running || fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("start rejection error=%v running=%t starts=%d stops=%d",
			err, fake.running, fake.startCalls, fake.stopCalls)
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 9 {
		t.Fatalf("start rejection history=%d error=%v", len(history.Progress), scanErr)
	}
}

func TestGatewayRebindStageStartImmediateRecheckAdoptsConcurrentExactStart(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), func() { fake.running = true }); err != nil {
		t.Fatalf("concurrent exact start was not adopted: %v", err)
	}
	if fake.startCalls != 0 || fake.stopCalls != 0 || !fake.running {
		t.Fatalf("concurrent adoption starts=%d stops=%d running=%t",
			fake.startCalls, fake.stopCalls, fake.running)
	}
}

func TestGatewayRebindStageStartImmediateInterfaceDriftNeverStarts(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	original := reads.network.candidates
	drift := false
	reads.network.candidates = func() ([]hostNetworkCandidate, error) {
		values, err := original()
		if drift {
			for index := range values {
				if values[index].InterfaceID == fixture.intent.Intent.SuccessorProfile.InterfaceID {
					values[index].IPv4 = "192.168.88.99"
					break
				}
			}
		}
		return values, err
	}
	err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), func() { drift = true })
	if err == nil || fake.startCalls != 0 || fake.stopCalls != 0 || fake.running {
		t.Fatalf("interface drift error=%v starts=%d stops=%d running=%t",
			err, fake.startCalls, fake.stopCalls, fake.running)
	}
}

func TestGatewayRebindStageStartCheckpointReattestationRejectsConfigAndImageDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageStartFake)
	}{
		{name: "restart config", mutate: func(fake *gatewayRebindStageStartFake) {
			fake.inventory = gatewayRebindStageConfigInventoryEmpty
		}},
		{name: "pinned image", mutate: func(fake *gatewayRebindStageStartFake) {
			fake.imageDrift = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
			err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(10), func() { test.mutate(fake) })
			if err == nil || fake.startCalls != 0 || fake.stopCalls != 0 || fake.running {
				t.Fatalf("checkpoint drift error=%v starts=%d stops=%d running=%t",
					err, fake.startCalls, fake.stopCalls, fake.running)
			}
		})
	}
}

func TestGatewayRebindStageStartCheckpointReattestationRejectsPredecessorDrift(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	baseInspect := gatewayRebindStageNetworkInspect(t, fixture)
	drift := false
	inspect := func(ctx context.Context, source routeState, state gatewayV2RouteState,
		journal gatewayMigrationJournal,
	) (gatewayV2DockerObservation, error) {
		observation, err := baseInspect(ctx, source, state, journal)
		if drift {
			labels := make(map[string]string, len(observation.FinalContainer.Labels))
			for key, value := range observation.FinalContainer.Labels {
				labels[key] = value
			}
			labels[gatewayV2OperationLabelKey] = "11111111-1111-4111-8111-111111111111"
			observation.FinalContainer.Labels = labels
		}
		return observation, err
	}
	err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, inspect, fake, gatewayRebindProgressTimestamp(10),
		func() { drift = true })
	if err == nil || fake.startCalls != 0 || fake.stopCalls != 0 || fake.running {
		t.Fatalf("predecessor checkpoint drift error=%v starts=%d stops=%d running=%t",
			err, fake.startCalls, fake.stopCalls, fake.running)
	}
}

func TestGatewayRebindStageStartAcceptsAlternatingExactPredecessorMountOrder(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	baseInspect := gatewayRebindStageNetworkInspect(t, fixture)
	inspectCalls := 0
	inspect := func(ctx context.Context, source routeState, state gatewayV2RouteState,
		journal gatewayMigrationJournal,
	) (gatewayV2DockerObservation, error) {
		observation, err := baseInspect(ctx, source, state, journal)
		inspectCalls++
		if inspectCalls%2 == 0 && len(observation.FinalContainer.Mounts) == 2 {
			observation.FinalContainer.Mounts[0], observation.FinalContainer.Mounts[1] =
				observation.FinalContainer.Mounts[1], observation.FinalContainer.Mounts[0]
		}
		return observation, err
	}
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, inspect, fake, gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("alternating exact predecessor mount order was rejected: %v", err)
	}
	if inspectCalls < 4 || fake.startCalls != 1 || fake.stopCalls != 0 || !fake.running {
		t.Fatalf("mount-order run inspections=%d starts=%d stops=%d running=%t",
			inspectCalls, fake.startCalls, fake.stopCalls, fake.running)
	}
}

func TestGatewayRebindStageStartProofFailureCompensatesExactOwnedStage(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*gatewayRebindStageStartFake)
	}{
		{name: "host challenge", configure: func(fake *gatewayRebindStageStartFake) { fake.hostProbeDrift = true }},
		{name: "container challenge", configure: func(fake *gatewayRebindStageStartFake) { fake.containerDrift = true }},
		{name: "loopback exposure", configure: func(fake *gatewayRebindStageStartFake) { fake.loopbackLive = true }},
		{name: "live config", configure: func(fake *gatewayRebindStageStartFake) { fake.liveConfigDrift = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
			test.configure(fake)
			err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(10), nil)
			if err == nil || fake.running || fake.startCalls != 1 || fake.stopCalls != 1 || candidateMayBeLive(err) {
				t.Fatalf("failed proof was not safely compensated: error=%v live=%t starts=%d stops=%d",
					err, fake.running, fake.startCalls, fake.stopCalls)
			}
			ports := int(fixture.intent.Intent.SuccessorProfile.PortEnd-
				fixture.intent.Intent.SuccessorProfile.PortStart) + 1
			if fake.stoppedSelected != 2*ports || fake.stoppedLoopback != 2*ports {
				t.Fatalf("withdrawal probes selected=%d loopback=%d ports=%d",
					fake.stoppedSelected, fake.stoppedLoopback, ports)
			}
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 9 {
				t.Fatalf("failed proof advanced history: count=%d error=%v", len(history.Progress), scanErr)
			}
		})
	}
}

func TestGatewayRebindStageStartStuckHostListenerLeavesCandidateUnresolved(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*gatewayRebindStageStartFake)
	}{
		{name: "selected host", configure: func(fake *gatewayRebindStageStartFake) {
			fake.hostProbeDrift = true
			fake.selectedStuck = true
		}},
		{name: "loopback", configure: func(fake *gatewayRebindStageStartFake) {
			fake.hostProbeDrift = true
			fake.loopbackStuck = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
			test.configure(fake)
			err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(10), nil)
			if err == nil || !candidateMayBeLive(err) || fake.running || fake.startCalls != 1 || fake.stopCalls != 1 {
				t.Fatalf("stuck listener error=%v live=%t starts=%d stops=%d",
					err, fake.running, fake.startCalls, fake.stopCalls)
			}
		})
	}
}

func TestGatewayRebindStageStartCompensationUncertaintyMarksCandidateLive(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	fake.hostProbeDrift = true
	fake.stopErr = errors.New("uncertain Docker stop")
	err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil)
	if err == nil || !candidateMayBeLive(err) || !fake.running || fake.startCalls != 1 || fake.stopCalls != 1 {
		t.Fatalf("uncertain compensation error=%v live=%t starts=%d stops=%d",
			err, fake.running, fake.startCalls, fake.stopCalls)
	}
}

func TestGatewayRebindStageStartStopErrorWithProvenWithdrawalIsSafe(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	fake.hostProbeDrift = true
	fake.stopErr = errors.New("lost Docker stop acknowledgement")
	fake.stopTakesEffect = true
	err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil)
	if err == nil || candidateMayBeLive(err) || fake.running || fake.startCalls != 1 || fake.stopCalls != 1 {
		t.Fatalf("attested withdrawal error=%v live=%t starts=%d stops=%d",
			err, fake.running, fake.startCalls, fake.stopCalls)
	}
}

func TestGatewayRebindStageStartAmbiguousReceiptInstallDoesNotStartTwice(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	originalWrite := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if err := originalWrite(path, purpose, body); err != nil {
			return err
		}
		return errors.New("injected protected write uncertainty")
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = originalWrite })
	err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil)
	if err == nil || !candidateMayBeLive(err) || fake.startCalls != 1 || fake.stopCalls != 0 || !fake.running {
		t.Fatalf("ambiguous install error=%v starts=%d stops=%d running=%t",
			err, fake.startCalls, fake.stopCalls, fake.running)
	}
	upgradeProtectedWriteNew = originalWrite
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatalf("fresh Manager did not reattest ambiguous receipt: %v", err)
	}
	if fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("ambiguous receipt replay mutated Docker: starts=%d stops=%d", fake.startCalls, fake.stopCalls)
	}
}

func TestGatewayRebindStageServingReplayDriftIsObservationOnlyAndCandidateLive(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	inspect := gatewayRebindStageNetworkInspect(t, fixture)
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, inspect, fake, gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("install serving receipt: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 10 {
		t.Fatalf("serving history=%d error=%v", len(history.Progress), err)
	}
	before := make([][]byte, len(history.Progress))
	for index := range history.Progress {
		before[index], err = os.ReadFile(history.Progress[index].Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	originalEndpoint := fake.endpointID
	for _, test := range []struct {
		name   string
		mutate func()
		reset  func()
	}{
		{name: "endpoint", mutate: func() { fake.endpointID = strings.Repeat("f", 64) },
			reset: func() { fake.endpointID = originalEndpoint }},
		{name: "host challenge", mutate: func() { fake.hostProbeDrift = true },
			reset: func() { fake.hostProbeDrift = false }},
		{name: "container challenge", mutate: func() { fake.containerDrift = true },
			reset: func() { fake.containerDrift = false }},
		{name: "live config", mutate: func() { fake.liveConfigDrift = true },
			reset: func() { fake.liveConfigDrift = false }},
		{name: "copied restart config", mutate: func() { fake.inventory = gatewayRebindStageConfigInventoryEmpty },
			reset: func() { fake.inventory = gatewayRebindStageConfigInventoryExact }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.mutate()
			defer test.reset()
			starts, stops := fake.startCalls, fake.stopCalls
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			replayErr := restarted.startGatewayRebindSuccessorStageWithDriver(context.Background(),
				fixture.predecessor.repository, reads, inspect, fake, gatewayRebindProgressTimestamp(11), nil)
			if replayErr == nil || !candidateMayBeLive(replayErr) ||
				fake.startCalls != starts || fake.stopCalls != stops {
				t.Fatalf("drift replay error=%v starts=%d/%d stops=%d/%d",
					replayErr, fake.startCalls, starts, fake.stopCalls, stops)
			}
			after, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(after.Progress) != len(before) {
				t.Fatalf("drift replay history=%d error=%v", len(after.Progress), scanErr)
			}
			for index := range after.Progress {
				body, readErr := os.ReadFile(after.Progress[index].Store.path)
				if readErr != nil || !bytes.Equal(body, before[index]) {
					t.Fatalf("drift replay changed sequence %d: %v", index+1, readErr)
				}
			}
		})
	}
}

func TestGatewayRebindStageServingBindingAndPublicationArePortBound(t *testing.T) {
	fixture, fake, _, records := installGatewayRebindStageServingIntent(t)
	binding, err := gatewayRebindStageServingBindingFor(fixture.intent, records[8], fake.endpointID)
	if err != nil || !validGatewayRebindStageServingBinding(fixture.intent, records[8], binding) {
		t.Fatalf("valid serving binding rejected: %#v error=%v", binding, err)
	}
	wantBindings := make(map[string][]map[string]string)
	for port := fixture.intent.Intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		wantBindings[value+"/tcp"] = []map[string]string{{
			"HostIp": fixture.intent.Intent.SuccessorProfile.SelectedIPv4, "HostPort": value,
		}}
		if port == fixture.intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
	wantDigest, err := canonicalDigest(wantBindings)
	if err != nil || binding.EffectivePortBindingsDigest != wantDigest ||
		binding.ConfigDigest != records[8].Stage.StageConfigIntent.ContentDigest {
		t.Fatalf("serving binding projection=%#v digest_error=%v", binding, err)
	}
	fake.running = true
	fake.hostProbeCalls = 0
	fake.containerCalls = 0
	if !proveGatewayRebindStagePublication(context.Background(), binding.StageStartIntent,
		fake.containerID, fake.hostProbe, fake.containerProbe) {
		t.Fatal("exact rebind publication proof was rejected")
	}
	ports := int(binding.StageStartIntent.PortEnd-binding.StageStartIntent.PortStart) + 1
	if fake.hostProbeCalls != 4*ports || fake.containerCalls != ports {
		t.Fatalf("publication proof calls host=%d container=%d ports=%d",
			fake.hostProbeCalls, fake.containerCalls, ports)
	}
	for _, test := range []struct {
		name string
		host string
	}{
		{name: "selected host status", host: binding.StageStartIntent.SelectedIPv4},
		{name: "wrong host status", host: "wrong.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := func(ctx context.Context, address string, port uint16,
				host, path string,
			) gatewayV2HostProbeResult {
				result := fake.hostProbe(ctx, address, port, host, path)
				if path == "/" && host == test.host {
					result.Status = http.StatusOK
				}
				return result
			}
			if proveGatewayRebindStagePublication(context.Background(), binding.StageStartIntent,
				fake.containerID, probe, fake.containerProbe) {
				t.Fatal("non-404 host response was accepted")
			}
		})
	}
	fake.containerDrift = true
	if proveGatewayRebindStagePublication(context.Background(), binding.StageStartIntent,
		fake.containerID, fake.hostProbe, fake.containerProbe) {
		t.Fatal("container challenge drift was accepted")
	}
}

func TestGatewayRebindRunningStageValidatorRejectsRuntimeAndTopologyDrift(t *testing.T) {
	fixture, fake, _, _ := installGatewayRebindStageServingIntent(t)
	fake.running = true
	base, err := fake.inspect(context.Background(), fixture.intent)
	if err != nil || !validGatewayRebindRunningStageContainerObservation(fixture.intent, fake.stage, base) {
		t.Fatalf("exact running stage rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageContainerObservation)
	}{
		{name: "wrong endpoint", mutate: func(value *gatewayRebindStageContainerObservation) {
			network := value.StageRuntime.ConfiguredNetworks[fixture.intent.Intent.Identity.IngressNetwork]
			network.EndpointID = "sha256:" + network.EndpointID
			value.StageRuntime.ConfiguredNetworks[fixture.intent.Intent.Identity.IngressNetwork] = network
		}},
		{name: "wrong effective bind", mutate: func(value *gatewayRebindStageContainerObservation) {
			port := strconv.FormatUint(uint64(fixture.intent.Intent.SuccessorProfile.PortStart), 10) + "/tcp"
			value.StageRuntime.EffectivePortBindings[port][0]["HostPort"] = "1"
		}},
		{name: "extra configured network", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageRuntime.ConfiguredNetworks["foreign"] = gatewayV2ConfiguredNetwork{NetworkID: strings.Repeat("f", 64)}
		}},
		{name: "extra network container", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.Network.Containers[strings.Repeat("f", 64)] = caddyNetworkContainerInspection{
				Name: "foreign", IPv4Address: "10.0.0.3/28",
			}
		}},
		{name: "restart count", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageRuntime.RestartCount = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, inspectErr := fake.inspect(context.Background(), fixture.intent)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			test.mutate(&value)
			if validGatewayRebindRunningStageContainerObservation(fixture.intent, fake.stage, value) {
				t.Fatal("running-stage drift was accepted")
			}
		})
	}
}

func TestGatewayRebindStageServingConstructorAndScannerRejectPriorStageRewrite(t *testing.T) {
	fixture, fake, _, records := installGatewayRebindStageServingIntent(t)
	binding, err := gatewayRebindStageServingBindingFor(fixture.intent, records[8], fake.endpointID)
	if err != nil {
		t.Fatal(err)
	}
	invalidBinding := binding
	invalidBinding.PriorProgressDigest = strings.Repeat("f", 64)
	if record, constructErr := newGatewayRebindStageServingProgress(fixture.intent, records[8],
		invalidBinding, gatewayRebindProgressTimestamp(10)); constructErr == nil ||
		!reflect.DeepEqual(record, gatewayRebindProgressRecord{}) {
		t.Fatalf("constructor accepted rewritten prior binding: record=%#v error=%v", record, constructErr)
	}
	authentic, err := newGatewayRebindStageServingProgress(fixture.intent, records[8], binding,
		gatewayRebindProgressTimestamp(10))
	if err != nil {
		t.Fatal(err)
	}
	forged := authentic
	stage := *authentic.Stage
	changedCopy := *stage.StageConfigCopy
	changedCopy.PriorProgressDigest = strings.Repeat("d", 64)
	stage.StageConfigCopy = &changedCopy
	forged.Stage = &stage
	forged.Digest, err = gatewayRebindProgressDigest(forged)
	if err != nil || !validGatewayRebindProgressRecord(forged) {
		t.Fatalf("forged serving record was not structurally valid: %v", err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 10)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, forged, false, maxGatewayRebindProgressBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("scanner accepted sequence ten rewrite of sequence nine stage")
	}
}

type gatewayRebindStageCommandRunner struct{ requests [][]string }

func (r *gatewayRebindStageCommandRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, append([]string(nil), request.Args...))
	return runtimeprocess.CommandResult{}, nil
}

func TestGatewayRebindStageStartProductionDriverCommandsUseOnlyBoundID(t *testing.T) {
	id := strings.Repeat("d", 64)
	runner := &gatewayRebindStageCommandRunner{}
	driver := managerGatewayRebindStageStartDriver{manager: &Manager{
		runner: runner, options: Options{DockerExecutable: "docker"},
	}}
	if err := driver.start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := driver.stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"container", "start", id}, {"container", "stop", "--time", "10", id}}
	if !reflect.DeepEqual(runner.requests, want) {
		t.Fatalf("stage effect commands=%#v want=%#v", runner.requests, want)
	}
}

func installGatewayRebindStageServingIntent(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindStageStartFake, gatewayRebindSuccessorPreflightReads, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, configFake, reads, _ := installGatewayRebindStageStartIntentProgress(t)
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageStartIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		configFake, gatewayRebindProgressTimestamp(9), nil); err != nil {
		t.Fatalf("install sequence-nine start intent: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 9 {
		t.Fatalf("sequence-nine history=%d error=%v", len(history.Progress), err)
	}
	configFake.stage = *history.Progress[8].Record.Stage
	fake := &gatewayRebindStageStartFake{
		gatewayRebindStageConfigCopyFake: configFake,
		endpointID:                       strings.Repeat("e", 64),
	}
	records := make([]gatewayRebindProgressRecord, len(history.Progress))
	for index := range history.Progress {
		records[index] = history.Progress[index].Record
	}
	return fixture, fake, reads, records
}

func candidateMayBeLive(err error) bool {
	var target *Error
	return errors.As(err, &target) && target.candidateMayBeLive
}

var _ gatewayRebindStageStartDriver = (*gatewayRebindStageStartFake)(nil)
