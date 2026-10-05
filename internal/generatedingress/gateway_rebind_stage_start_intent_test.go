package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayRebindStageStartIntentAppendsAndFreshManagerReplays(t *testing.T) {
	fixture, fake, reads, records := installGatewayRebindStageStartIntentProgress(t)
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 {
		t.Fatalf("base history=%d error=%v", len(history.Progress), err)
	}
	baseBytes := make([][]byte, len(history.Progress))
	for index, entry := range history.Progress {
		baseBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := false
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageStartIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(9), func() { checkpoint = true }); err != nil {
		t.Fatalf("prepare stage start intent: %v", err)
	}
	if !checkpoint || fake.copyCalls != 1 {
		t.Fatalf("checkpoint=%t config copies=%d", checkpoint, fake.copyCalls)
	}
	history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 9 {
		t.Fatalf("sequence-nine history=%d error=%v", len(history.Progress), err)
	}
	for index := range baseBytes {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !bytes.Equal(body, baseBytes[index]) || history.Progress[index].Record.Digest != records[index].Digest {
			t.Fatalf("sequence %d changed: read=%v", index+1, readErr)
		}
	}
	binding, err := gatewayRebindStageStartIntentBindingFor(fixture.intent, records[7])
	if err != nil || history.Progress[8].Record.Phase != gatewayRebindProgressStageStartIntent ||
		history.Progress[8].Record.Stage == nil || history.Progress[8].Record.Stage.StageStartIntent == nil ||
		*history.Progress[8].Record.Stage.StageStartIntent != binding {
		t.Fatalf("stage start binding=%#v expected=%#v error=%v", history.Progress[8].Record, binding, err)
	}
	if binding.StageContainer.ID != records[7].Stage.StageContainer.ID || binding.Network.ID != records[7].Stage.Network.ID ||
		binding.SelectedIPv4 != fixture.intent.Intent.SuccessorProfile.SelectedIPv4 ||
		binding.InterfaceID != fixture.intent.Intent.SuccessorProfile.InterfaceID ||
		binding.ContainerIPv4 != fixture.intent.Intent.Network.ContainerIPv4 || binding.StartEffectDigest == "" {
		t.Fatalf("stage start contract is incomplete: %#v", binding)
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite changed: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	ninthBytes, err := os.ReadFile(history.Progress[8].Store.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.prepareGatewayRebindSuccessorStageStartIntentWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("fresh Manager replay: %v", err)
	}
	replayed, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(replayed.Progress) != 9 || replayed.Progress[8].Record.Digest != history.Progress[8].Record.Digest {
		t.Fatalf("replay changed protected history: count=%d error=%v", len(replayed.Progress), err)
	}
	replayBytes, err := os.ReadFile(history.Progress[8].Store.path)
	if err != nil || !bytes.Equal(ninthBytes, replayBytes) || fake.copyCalls != 1 {
		t.Fatalf("replay changed receipt or copied config: read=%v copies=%d", err, fake.copyCalls)
	}
	replayDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, replayDatabase) {
		t.Fatalf("replay changed SQLite: equal=%t error=%v", reflect.DeepEqual(before, replayDatabase), err)
	}
	fake.running = true
	if err := restarted.prepareGatewayRebindSuccessorStageStartIntentWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(11), nil); err == nil {
		t.Fatal("replay accepted a running successor")
	}
	driftBytes, err := os.ReadFile(history.Progress[8].Store.path)
	if err != nil || !bytes.Equal(ninthBytes, driftBytes) {
		t.Fatalf("drift replay changed receipt: %v", err)
	}
}

func TestGatewayRebindStageStartIntentRejectsDriftAndDoesNotAdvance(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*gatewayRebindStageConfigCopyFake)
		checkpoint func(*gatewayRebindStageConfigCopyFake) func()
	}{
		{name: "missing copied config", configure: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.inventory = gatewayRebindStageConfigInventoryEmpty
		}},
		{name: "config inventory error", configure: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.inventoryErr = errors.New("config inventory unavailable")
		}},
		{name: "container begins running", checkpoint: func(fake *gatewayRebindStageConfigCopyFake) func() {
			return func() { fake.running = true }
		}},
		{name: "effective host binding appears", checkpoint: func(fake *gatewayRebindStageConfigCopyFake) func() {
			return func() { fake.effectivePort = true }
		}},
		{name: "pinned image drifts", checkpoint: func(fake *gatewayRebindStageConfigCopyFake) func() {
			return func() { fake.imageDrift = true }
		}},
		{name: "bound container changes", checkpoint: func(fake *gatewayRebindStageConfigCopyFake) func() {
			return func() { fake.containerID = strings.Repeat("f", 64) }
		}},
		{name: "config changes between observations", checkpoint: func(fake *gatewayRebindStageConfigCopyFake) func() {
			return func() { fake.inventory = gatewayRebindStageConfigInventoryEmpty }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageStartIntentProgress(t)
			if test.configure != nil {
				test.configure(fake)
			}
			var checkpoint func()
			if test.checkpoint != nil {
				checkpoint = test.checkpoint(fake)
			}
			err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageStartIntentWithDriver(
				context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
				fake, gatewayRebindProgressTimestamp(9), checkpoint)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 8 || fake.copyCalls != 1 {
				t.Fatalf("drift advanced history: err=%v scan=%v progress=%d copies=%d", err, scanErr, len(history.Progress), fake.copyCalls)
			}
		})
	}
}

func TestGatewayRebindStageStartIntentRejectsEndpointDriftAndAmbiguousInstall(t *testing.T) {
	t.Run("selected host endpoint", func(t *testing.T) {
		fixture, fake, reads, _ := installGatewayRebindStageStartIntentProgress(t)
		original := reads.network.candidates
		reads.network.candidates = func() ([]hostNetworkCandidate, error) {
			values, err := original()
			for index := range values {
				if values[index].InterfaceID == fixture.intent.Intent.SuccessorProfile.InterfaceID &&
					values[index].IPv4 == fixture.intent.Intent.SuccessorProfile.SelectedIPv4 {
					values[index].InterfaceID = "drifted-interface"
				}
			}
			return values, err
		}
		err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageStartIntentWithDriver(
			context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
			fake, gatewayRebindProgressTimestamp(9), nil)
		history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err == nil || scanErr != nil || len(history.Progress) != 8 {
			t.Fatalf("endpoint drift advanced history: err=%v scan=%v progress=%d", err, scanErr, len(history.Progress))
		}
	})

	t.Run("post-install uncertainty requires fresh replay", func(t *testing.T) {
		fixture, fake, reads, _ := installGatewayRebindStageStartIntentProgress(t)
		original := upgradeProtectedWriteNew
		upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
			if err := original(path, purpose, body); err != nil {
				return err
			}
			return errors.New("injected post-install uncertainty")
		}
		t.Cleanup(func() { upgradeProtectedWriteNew = original })
		err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageStartIntentWithDriver(
			context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
			fake, gatewayRebindProgressTimestamp(9), nil)
		if err == nil {
			t.Fatal("ambiguous sequence-nine install reported success")
		}
		upgradeProtectedWriteNew = original
		history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || len(history.Progress) != 9 {
			t.Fatalf("ambiguous install lost receipt: scan=%v progress=%d", scanErr, len(history.Progress))
		}
		restarted := newGatewayRebindStageContainerManager(t, fixture)
		if err := restarted.prepareGatewayRebindSuccessorStageStartIntentWithDriver(context.Background(),
			fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
			gatewayRebindProgressTimestamp(10), nil); err != nil {
			t.Fatalf("fresh replay after ambiguity: %v", err)
		}
	})
}

func TestGatewayRebindStageStartIntentRejectsForgedBindingAndPreservesV1Records(t *testing.T) {
	fixture, _, _, records := installGatewayRebindStageStartIntentProgress(t)
	for _, record := range records {
		currentBytes, currentErr := json.Marshal(record)
		legacy := preStartGatewayRebindProgress(record)
		legacyBytes, legacyErr := json.Marshal(legacy)
		if currentErr != nil || legacyErr != nil || !bytes.Equal(currentBytes, legacyBytes) {
			t.Fatalf("sequence %d changed from the pre-start schema: current=%s legacy=%s errors=%v/%v",
				record.Sequence, currentBytes, legacyBytes, currentErr, legacyErr)
		}
		legacy.Digest = ""
		legacyDigest, digestErr := canonicalDigest(legacy)
		if digestErr != nil || legacyDigest != record.Digest {
			t.Fatalf("sequence %d changed digest: current=%s legacy=%s error=%v",
				record.Sequence, record.Digest, legacyDigest, digestErr)
		}
	}
	binding, err := gatewayRebindStageStartIntentBindingFor(fixture.intent, records[7])
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageStartIntentBinding)
	}{
		{name: "container substitution", mutate: func(value *gatewayRebindStageStartIntentBinding) {
			value.StageContainer.ID = strings.Repeat("a", 64)
		}},
		{name: "network substitution", mutate: func(value *gatewayRebindStageStartIntentBinding) {
			value.Network.ID = strings.Repeat("b", 64)
		}},
		{name: "interface substitution", mutate: func(value *gatewayRebindStageStartIntentBinding) {
			value.InterfaceID = "unexpected-interface"
		}},
		{name: "probe substitution", mutate: func(value *gatewayRebindStageStartIntentBinding) {
			value.ProbeToken = strings.Repeat("c", 64)
		}},
		{name: "effect digest substitution", mutate: func(value *gatewayRebindStageStartIntentBinding) {
			value.StartEffectDigest = strings.Repeat("d", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := binding
			test.mutate(&forged)
			if _, err := newGatewayRebindStageStartIntentProgress(fixture.intent, records[7], forged,
				gatewayRebindProgressTimestamp(9)); err == nil {
				t.Fatal("forged start intent was accepted")
			}
		})
	}

	previous := records[7]
	original, err := gatewayRebindProgressDigest(previous)
	if err != nil || original != previous.Digest {
		t.Fatalf("v1 sequence-eight digest changed: digest=%s want=%s error=%v", original, previous.Digest, err)
	}
	if previous.Stage.StageStartIntent != nil {
		t.Fatal("v1 sequence-eight record unexpectedly has a stage-start field")
	}
}

func TestGatewayRebindStageStartEffectProjectionV1(t *testing.T) {
	fixture, _, _, records := installGatewayRebindStageStartIntentProgress(t)
	binding := mustGatewayRebindStageStartIntentBinding(t, fixture.intent, records[7])
	// This separately spells out the persisted v1 effect contract. Changing a
	// command, listener, challenge, wrong-Host status, or protected input must
	// not silently make an existing sequence-nine receipt unreadable.
	expected, err := canonicalDigest(struct {
		Context              string                              `json:"context"`
		Version              int                                 `json:"version"`
		Command              []string                            `json:"command"`
		StageContainerID     string                              `json:"stageContainerId"`
		NetworkID            string                              `json:"networkId"`
		InterfaceID          string                              `json:"interfaceId"`
		SelectedIPv4         string                              `json:"selectedIpv4"`
		PortStart            uint16                              `json:"portStart"`
		PortEnd              uint16                              `json:"portEnd"`
		ContainerIPv4        string                              `json:"containerIpv4"`
		ProbeToken           string                              `json:"probeToken"`
		LocalListener        string                              `json:"localListener"`
		ProbePathPrefix      string                              `json:"probePathPrefix"`
		ProbeBodyPrefix      string                              `json:"probeBodyPrefix"`
		FallbackStatusCode   int                                 `json:"fallbackStatusCode"`
		LoopbackForbidden    bool                                `json:"loopbackForbidden"`
		StageConfigCopy      gatewayRebindStageConfigCopyBinding `json:"stageConfigCopy"`
		ProtectedIntent      string                              `json:"protectedIntentDigest"`
		ProtectedPredecessor string                              `json:"protectedPredecessorDigest"`
		PriorProgress        string                              `json:"priorProgressDigest"`
	}{
		Context: "hostd/generated-ingress/rebind/stage-start-effect/v1", Version: 1,
		Command:          []string{"container", "start", binding.StageContainer.ID},
		StageContainerID: binding.StageContainer.ID, NetworkID: binding.Network.ID,
		InterfaceID: binding.InterfaceID, SelectedIPv4: binding.SelectedIPv4,
		PortStart: binding.PortStart, PortEnd: binding.PortEnd, ContainerIPv4: binding.ContainerIPv4,
		ProbeToken: binding.ProbeToken,
		LocalListener: net.JoinHostPort(binding.ContainerIPv4,
			strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		ProbePathPrefix: "/.well-known/rig-gateway/", ProbeBodyPrefix: "rig-gateway-v2:",
		FallbackStatusCode: 404, LoopbackForbidden: true,
		StageConfigCopy:      binding.StageConfigCopy,
		ProtectedIntent:      binding.ProtectedIntentDigest,
		ProtectedPredecessor: binding.ProtectedPredecessorDigest,
		PriorProgress:        binding.PriorProgressDigest,
	})
	if err != nil || binding.StartEffectDigest != expected {
		t.Fatalf("v1 start-effect digest=%s expected=%s error=%v", binding.StartEffectDigest, expected, err)
	}
}

func TestGatewayRebindStageStartIntentScannerRejectsSelfConsistentForgery(t *testing.T) {
	fixture, _, _, records := installGatewayRebindStageStartIntentProgress(t)
	binding, err := gatewayRebindStageStartIntentBindingFor(fixture.intent, records[7])
	if err != nil {
		t.Fatal(err)
	}
	// This preserves the binding's internal digest so the scanner must reject it
	// for violating the protected sequence-eight contract, rather than merely a
	// malformed JSON shape.
	binding.InterfaceID = "forged-interface"
	binding.StartEffectDigest, err = gatewayRebindStageStartEffectDigest(binding)
	if err != nil {
		t.Fatal(err)
	}
	record, err := newGatewayRebindStageStartIntentProgress(fixture.intent, records[7],
		binding, gatewayRebindProgressTimestamp(9))
	if err == nil || !reflect.DeepEqual(record, gatewayRebindProgressRecord{}) {
		t.Fatalf("forged sequence-nine constructor accepted record=%#v error=%v", record, err)
	}

	// The constructor fails before persistence. Build the authentic record,
	// replace its start intent with the self-consistent forged value, and write
	// it as an attacker could only by bypassing create-only state APIs.
	authentic, err := newGatewayRebindStageStartIntentProgress(fixture.intent, records[7],
		mustGatewayRebindStageStartIntentBinding(t, fixture.intent, records[7]), gatewayRebindProgressTimestamp(9))
	if err != nil {
		t.Fatal(err)
	}
	forged := authentic
	stage := *authentic.Stage
	stage.StageStartIntent = &binding
	forged.Stage = &stage
	forged.Digest, err = gatewayRebindProgressDigest(forged)
	if err != nil || !validGatewayRebindProgressRecord(forged) {
		t.Fatalf("forged record did not remain structurally valid: %v", err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 9)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, forged, false, maxGatewayRebindProgressBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("scanner accepted self-consistent forged stage-start binding")
	}
}

func TestGatewayRebindStageStartIntentScannerRejectsPriorStageRewrite(t *testing.T) {
	fixture, _, _, records := installGatewayRebindStageStartIntentProgress(t)
	authentic, err := newGatewayRebindStageStartIntentProgress(fixture.intent, records[7],
		mustGatewayRebindStageStartIntentBinding(t, fixture.intent, records[7]), gatewayRebindProgressTimestamp(9))
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
		t.Fatalf("prior-stage rewrite was not structurally valid: %v", err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 9)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, forged, false, maxGatewayRebindProgressBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("scanner accepted a sequence-nine rewrite of the sequence-eight stage")
	}
}

func mustGatewayRebindStageStartIntentBinding(t *testing.T, intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord,
) gatewayRebindStageStartIntentBinding {
	t.Helper()
	value, err := gatewayRebindStageStartIntentBindingFor(intent, previous)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// This is the sequence-eight storage shape before stageStartIntent was added.
// It intentionally does not embed the current stage type, so the test detects
// an accidental change in an earlier record's JSON field order or omission.
type preStartGatewayRebindStage struct {
	Identity                 gatewayRebindSuccessorIdentity         `json:"identity"`
	ApprovedCaddyImageDigest string                                 `json:"approvedCaddyImageDigest"`
	ObservedDockerImageID    string                                 `json:"observedDockerImageId"`
	NetworkPlan              gatewayRebindSuccessorIntentNetwork    `json:"networkPlan"`
	NetworkPlanDigest        string                                 `json:"networkPlanDigest"`
	NetworkTopologyDigest    string                                 `json:"networkTopologyDigest"`
	Network                  *gatewayRebindStageNetworkBinding      `json:"network,omitempty"`
	ConfigVolume             *gatewayRebindStageConfigVolumeBinding `json:"configVolume,omitempty"`
	DataVolume               *gatewayRebindStageDataVolumeBinding   `json:"dataVolume,omitempty"`
	StageContainer           *gatewayRebindStageContainerBinding    `json:"stageContainer,omitempty"`
	StageConfigIntent        *gatewayRebindStageConfigIntentBinding `json:"stageConfigIntent,omitempty"`
	StageConfigCopy          *gatewayRebindStageConfigCopyBinding   `json:"stageConfigCopy,omitempty"`
}

type preStartGatewayRebindRecord struct {
	Version               int                         `json:"version"`
	Purpose               string                      `json:"purpose"`
	Generation            uint64                      `json:"generation"`
	OperationID           string                      `json:"operationId"`
	Sequence              uint64                      `json:"sequence"`
	Phase                 gatewayRebindProgressPhase  `json:"phase"`
	OccurredAt            string                      `json:"occurredAt"`
	ProtectedIntentDigest string                      `json:"protectedIntentDigest"`
	PreviousDigest        string                      `json:"previousDigest,omitempty"`
	Stage                 *preStartGatewayRebindStage `json:"stage,omitempty"`
	Digest                string                      `json:"digest"`
}

func preStartGatewayRebindProgress(value gatewayRebindProgressRecord) preStartGatewayRebindRecord {
	legacy := preStartGatewayRebindRecord{
		Version: value.Version, Purpose: value.Purpose, Generation: value.Generation,
		OperationID: value.OperationID, Sequence: value.Sequence, Phase: value.Phase,
		OccurredAt: value.OccurredAt, ProtectedIntentDigest: value.ProtectedIntentDigest,
		PreviousDigest: value.PreviousDigest, Digest: value.Digest,
	}
	if value.Stage != nil {
		legacy.Stage = &preStartGatewayRebindStage{
			Identity: value.Stage.Identity, ApprovedCaddyImageDigest: value.Stage.ApprovedCaddyImageDigest,
			ObservedDockerImageID: value.Stage.ObservedDockerImageID,
			NetworkPlan:           value.Stage.NetworkPlan, NetworkPlanDigest: value.Stage.NetworkPlanDigest,
			NetworkTopologyDigest: value.Stage.NetworkTopologyDigest, Network: value.Stage.Network,
			ConfigVolume: value.Stage.ConfigVolume, DataVolume: value.Stage.DataVolume,
			StageContainer: value.Stage.StageContainer, StageConfigIntent: value.Stage.StageConfigIntent,
			StageConfigCopy: value.Stage.StageConfigCopy,
		}
	}
	return legacy
}

func installGatewayRebindStageStartIntentProgress(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindStageConfigCopyFake, gatewayRebindSuccessorPreflightReads, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(8), nil); err != nil {
		t.Fatalf("install config copy receipt: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 {
		t.Fatalf("config copy history=%d error=%v", len(history.Progress), err)
	}
	fake.stage = *history.Progress[7].Record.Stage
	values := make([]gatewayRebindProgressRecord, len(history.Progress))
	for index := range history.Progress {
		values[index] = history.Progress[index].Record
	}
	return fixture, fake, reads, values
}

var _ gatewayRebindStageStartIntentDriver = (*gatewayRebindStageConfigCopyFake)(nil)
