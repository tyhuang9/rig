package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

// This fake owns physical state independently of the protected history. Effects
// enforce the real ordering constraints instead of inferring success from the
// coordinator's requested phase.
type gatewayRebindFinalHandoverFake struct {
	*gatewayRebindFinalConfigCopyFake
	t                        *testing.T
	manager                  *Manager
	observation              gatewayRebindFinalHandoverObservation
	initialPredecessorDigest string
	predecessorRoutesDigest  string
	finalID                  string
	effects                  []string
	failAt                   string
	failAfter                bool
	failed                   bool
	onEffect                 func(string)
	observeHook              func(gatewayRebindFinalHandoverContext, *gatewayRebindFinalHandoverObservation) error
}

func installGatewayRebindFinalHandoverFixture(t *testing.T) (gatewayRebindEffectBoundaryFixture, *gatewayRebindFinalHandoverFake) {
	t.Helper()
	fixture, stageFake, reads, _ := installGatewayRebindFinalConfigLoopbackServingProgress(t)
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), stageFake,
		gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatal(err)
	}
	copyFake := newGatewayRebindFinalConfigCopyFake(t, fixture, stageFake)
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), copyFake,
		gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 {
		t.Fatalf("sequence-twelve fixture: %v", err)
	}
	source, err := fixture.predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	docker, err := gatewayRebindStageNetworkInspect(t, fixture)(context.Background(), source, history.Predecessor.State, history.Predecessor.Journal)
	if err != nil {
		t.Fatal(err)
	}
	defer clearGatewayV2DockerObservation(&docker)
	predDigest, err := gatewayRebindEffectBoundaryDockerDigest(docker, history.Predecessor.State.Identity)
	if err != nil {
		t.Fatal(err)
	}
	predRoutes, err := gatewayRebindFinalHandoverPredecessorRoutesDigest(history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	fake := &gatewayRebindFinalHandoverFake{gatewayRebindFinalConfigCopyFake: copyFake, t: t,
		manager: fixture.predecessor.manager, finalID: strings.Repeat("9", 64),
		initialPredecessorDigest: predDigest, predecessorRoutesDigest: predRoutes,
		observation: gatewayRebindFinalHandoverObservation{
			Stage: gatewayRebindHandoverContainerRunning, Final: gatewayRebindHandoverContainerAbsent,
			PredecessorRunning: true, PredecessorAddress: gatewayRebindPredecessorAddressPresent,
			PredecessorObservationDigest: predDigest, PredecessorRoutesDigest: predRoutes,
			ConfigVolumePresent: true, DataVolumePresent: true, IngressNetworkPresent: true,
			ConfigDigest: history.Progress[11].Record.Stage.FinalConfigIntent.ContentDigest,
			HostDigest:   strings.Repeat("a", 64), InventoryDigest: strings.Repeat("b", 64),
			ApplicationEndpointsDigest: strings.Repeat("c", 64),
		},
	}
	for name, id := range docker.ApplicationNetworkIDs {
		fake.observation.ApplicationNetworks = append(fake.observation.ApplicationNetworks,
			gatewayRebindHandoverApplicationNetwork{Name: name, ID: normalizeID(id)})
	}
	sort.Slice(fake.observation.ApplicationNetworks, func(i, j int) bool {
		return fake.observation.ApplicationNetworks[i].Name < fake.observation.ApplicationNetworks[j].Name
	})
	return fixture, fake
}

func (f *gatewayRebindFinalHandoverFake) observeHandover(ctx context.Context, value gatewayRebindFinalHandoverContext) (gatewayRebindFinalHandoverObservation, error) {
	if ctx.Err() != nil {
		return gatewayRebindFinalHandoverObservation{}, ctx.Err()
	}
	if f.manager.mu.TryLock() {
		f.manager.mu.Unlock()
		f.t.Error("handover observation lacked Manager lock")
	}
	if !validGatewayRebindFinalHandoverBase(value) {
		return gatewayRebindFinalHandoverObservation{}, errors.New("unbound observation input")
	}
	if f.observation.Final != gatewayRebindHandoverContainerAbsent &&
		(value.Final == nil || value.Final.ID != f.observation.FinalID) && value.CreatedFinalID != f.observation.FinalID {
		return gatewayRebindFinalHandoverObservation{}, errors.New("created final has no durable or invocation-local binding")
	}
	result := f.observation
	result.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), result.ApplicationNetworks...)
	if !gatewayRebindHandoverMayProvePredecessorRoutes(value.Phase) || !result.PredecessorRunning ||
		result.PredecessorAddress != gatewayRebindPredecessorAddressPresent || result.Final == gatewayRebindHandoverContainerRunning {
		result.PredecessorRoutesDigest = ""
	}
	if f.observeHook != nil {
		if err := f.observeHook(value, &result); err != nil {
			return gatewayRebindFinalHandoverObservation{}, err
		}
	}
	result.Digest, _ = gatewayRebindFinalHandoverObservationDigest(result)
	return result, nil
}

func (f *gatewayRebindFinalHandoverFake) effect(ctx context.Context, value gatewayRebindFinalHandoverContext, name string, apply func()) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.manager.mu.TryLock() {
		f.manager.mu.Unlock()
		f.t.Error("handover effect lacked Manager lock")
	}
	if value.CreatedFinalID != "" {
		f.t.Fatal("provisional final ID authorized mutation")
	}
	f.effects = append(f.effects, name)
	fail := f.failAt == name && !f.failed
	if fail {
		f.failed = true
		if !f.failAfter {
			return errors.New("injected effect failure")
		}
	}
	apply()
	if f.onEffect != nil {
		f.onEffect(name)
	}
	if fail {
		return errors.New("injected lost effect acknowledgment")
	}
	return nil
}
func (f *gatewayRebindFinalHandoverFake) stopStage(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "stop_stage", func() {
		if f.observation.Stage != gatewayRebindHandoverContainerRunning {
			f.t.Fatal("stage stop was not exact running stage")
		}
		f.observation.Stage = gatewayRebindHandoverContainerStopped
	})
}
func (f *gatewayRebindFinalHandoverFake) removeStage(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "remove_stage", func() {
		if f.observation.Stage != gatewayRebindHandoverContainerStopped {
			f.t.Fatal("stage removal preceded withdrawal")
		}
		f.observation.Stage = gatewayRebindHandoverContainerAbsent
		f.observation.ConfigDigest = ""
	})
}
func (f *gatewayRebindFinalHandoverFake) createFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) (string, error) {
	err := f.effect(ctx, v, "create_final", func() {
		if f.observation.Stage != gatewayRebindHandoverContainerAbsent || f.observation.Final != gatewayRebindHandoverContainerAbsent || v.Final != nil {
			f.t.Fatal("final creation did not follow exact stage removal")
		}
		f.observation.Final = gatewayRebindHandoverContainerStopped
		f.observation.FinalID = f.finalID
		f.observation.ConfigDigest = v.Plan.FinalConfigDigest
	})
	if err != nil {
		return "", err
	}
	return f.finalID, nil
}
func (f *gatewayRebindFinalHandoverFake) stopPredecessor(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "stop_predecessor", func() {
		if v.Final == nil || v.Phase != gatewayRebindProgressCutoverIntent || f.observation.Final != gatewayRebindHandoverContainerStopped || !f.observation.PredecessorRunning {
			f.t.Fatal("predecessor stop preceded exact stopped final binding/cutover intent")
		}
		f.observation.PredecessorRunning = false
		f.observation.PredecessorRoutesDigest = ""
		f.observation.PredecessorStopDigest = strings.Repeat("e", 64)
		f.observation.PredecessorObservationDigest = strings.Repeat("e", 64)
	})
}
func (f *gatewayRebindFinalHandoverFake) startFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "start_final", func() {
		if v.Final == nil || f.observation.PredecessorRunning || f.observation.Stage != gatewayRebindHandoverContainerAbsent || f.observation.Final != gatewayRebindHandoverContainerStopped {
			f.t.Fatal("final start preceded predecessor/stage withdrawal")
		}
		f.observation.Final = gatewayRebindHandoverContainerRunning
		f.observation.RoutesDigest = v.Plan.RoutePlanDigest
	})
}
func (f *gatewayRebindFinalHandoverFake) stopFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "stop_final", func() {
		if v.Final == nil || f.observation.Final != gatewayRebindHandoverContainerRunning {
			f.t.Fatal("final stop lacked bound running final")
		}
		f.observation.Final = gatewayRebindHandoverContainerStopped
		f.observation.RoutesDigest = ""
	})
}
func (f *gatewayRebindFinalHandoverFake) startPredecessor(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "start_predecessor", func() {
		if f.observation.PredecessorAddress != gatewayRebindPredecessorAddressPresent || f.observation.Final == gatewayRebindHandoverContainerRunning || f.observation.PredecessorRunning {
			f.t.Fatal("predecessor restore preceded successor withdrawal or lacks address")
		}
		f.observation.PredecessorRunning = true
		f.observation.PredecessorRoutesDigest = f.predecessorRoutesDigest
		f.observation.PredecessorStopDigest = ""
		f.observation.PredecessorObservationDigest = f.initialPredecessorDigest
	})
}
func (f *gatewayRebindFinalHandoverFake) removeFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "remove_final", func() {
		if v.Final == nil || f.observation.Final != gatewayRebindHandoverContainerStopped {
			f.t.Fatal("final removal lacked exact stopped binding")
		}
		f.observation.Final = gatewayRebindHandoverContainerAbsent
		f.observation.FinalID = ""
		f.observation.ConfigDigest = ""
	})
}
func (f *gatewayRebindFinalHandoverFake) removeConfigVolume(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "remove_config", func() { f.assertNoContainers(); f.observation.ConfigVolumePresent = false })
}
func (f *gatewayRebindFinalHandoverFake) removeDataVolume(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "remove_data", func() { f.assertNoContainers(); f.observation.DataVolumePresent = false })
}
func (f *gatewayRebindFinalHandoverFake) removeIngressNetwork(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return f.effect(ctx, v, "remove_network", func() { f.assertNoContainers(); f.observation.IngressNetworkPresent = false })
}
func (f *gatewayRebindFinalHandoverFake) assertNoContainers() {
	if f.observation.Stage != gatewayRebindHandoverContainerAbsent || f.observation.Final != gatewayRebindHandoverContainerAbsent {
		f.t.Fatal("removed resource while successor container remains")
	}
}

func TestGatewayRebindFinalHandoverCommitsInOrderAndReplaysWithoutEffects(t *testing.T) {
	fixture, fake := installGatewayRebindFinalHandoverFixture(t)
	before, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes := liveGatewayRebindProgressBytes(t, before)
	beforeSQL, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Progress[11].Record.Stage.FinalConfigIntent.RoutePlan.Routes) < 2 {
		t.Fatal("fixture lacks complete LAN and loopback route coverage")
	}
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop_stage", "remove_stage", "create_final", "stop_predecessor", "start_final"}
	if !reflect.DeepEqual(fake.effects, want) {
		t.Fatalf("effect order=%v", fake.effects)
	}
	history := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalCommit)
	for i, body := range beforeBytes {
		after, err := os.ReadFile(history.Progress[i].Store.path)
		if err != nil || !bytes.Equal(body, after) {
			t.Fatalf("rewrote sequence %d: %v", i+1, err)
		}
	}
	if !reflect.DeepEqual(before.Predecessor, history.Predecessor) || !reflect.DeepEqual(before.Source, history.Source) {
		t.Fatal("private terminal changed current predecessor or public route")
	}
	afterSQL, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(beforeSQL, afterSQL) {
		t.Fatalf("private handover changed prepared SQL/fence: %v", err)
	}
	terminalBytes, err := os.ReadFile(history.Terminals[0].Store.path)
	if err != nil {
		t.Fatal(err)
	}
	fake.manager = newGatewayRebindStageContainerManager(t, fixture)
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.effects, want) {
		t.Fatal("terminal replay repeated an effect")
	}
	after, err := os.ReadFile(history.Terminals[0].Store.path)
	if err != nil || !bytes.Equal(after, terminalBytes) {
		t.Fatal("terminal replay rewrote receipt")
	}
}

func TestGatewayRebindFinalHandoverEffectUncertaintyRequiresFreshProof(t *testing.T) {
	for _, test := range []struct {
		name           string
		after, unbound bool
	}{
		{"stop_stage", true, false}, {"remove_stage", true, false}, {"create_final", false, false}, {"create_final", true, true},
		{"stop_predecessor", true, false}, {"start_final", false, false}, {"start_final", true, false},
	} {
		name := test.name + "/before"
		if test.after {
			name = test.name + "/after"
		}
		t.Run(name, func(t *testing.T) {
			fixture, fake := installGatewayRebindFinalHandoverFixture(t)
			fake.failAt, fake.failAfter = test.name, test.after
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err == nil {
				t.Fatal("uncertain effect reported success")
			}
			history, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.Terminals) != 0 {
				t.Fatalf("uncertainty wrote terminal: %v", err)
			}
			effects := append([]string(nil), fake.effects...)
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			err = fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil)
			if test.unbound {
				if err == nil {
					t.Fatal("unbound final adopted by name")
				}
				if err := fake.manager.rollbackGatewayRebindFinalHandoverWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(31), nil); err == nil {
					t.Fatal("unbound final authorized rollback")
				}
				if !reflect.DeepEqual(effects, fake.effects) {
					t.Fatal("unbound final triggered mutation")
				}
				return
			}
			if err != nil {
				t.Fatalf("fresh retry: %v", err)
			}
			assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalCommit)
			count := 0
			for _, effect := range fake.effects {
				if effect == test.name {
					count++
				}
			}
			want := 1
			if !test.after {
				want = 2
			}
			if count != want {
				t.Fatalf("effect retry count=%d want=%d trace=%v", count, want, fake.effects)
			}
		})
	}
}

func TestGatewayRebindFinalHandoverProtectedWriteUncertaintyReplays(t *testing.T) {
	for _, sequence := range []uint64{13, 14, 15, 16, 17, 0} {
		t.Run(finalHandoverWriteName(sequence), func(t *testing.T) {
			fixture, fake := installGatewayRebindFinalHandoverFixture(t)
			original := upgradeProtectedWriteNew
			failed := false
			upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
				if err := original(path, purpose, body); err != nil {
					return err
				}
				name, _ := gatewayRebindProgressName(fixture.intent.Generation, fixture.intent.OperationID, sequence)
				if !failed && ((sequence == 0 && strings.HasPrefix(filepath.Base(path), gatewayRebindFinalHandoverTerminalFilenamePrefix)) || (sequence != 0 && filepath.Base(path) == name)) {
					failed = true
					return errors.New("injected protected write acknowledgment loss")
				}
				return nil
			}
			t.Cleanup(func() { upgradeProtectedWriteNew = original })
			err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil)
			upgradeProtectedWriteNew = original
			if err == nil || !failed {
				t.Fatalf("injected write uncertainty error=%v reached=%t", err, failed)
			}
			before, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil {
				t.Fatal(err)
			}
			beforeBytes := liveGatewayRebindProgressBytes(t, before)
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err != nil {
				t.Fatalf("fresh retry: %v", err)
			}
			after := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalCommit)
			for i, body := range beforeBytes {
				actual, err := os.ReadFile(after.Progress[i].Store.path)
				if err != nil || !bytes.Equal(body, actual) {
					t.Fatal("retry rewrote existing protected progress")
				}
			}
			if !reflect.DeepEqual(fake.effects, []string{"stop_stage", "remove_stage", "create_final", "stop_predecessor", "start_final"}) {
				t.Fatalf("retry repeated effects: %v", fake.effects)
			}
		})
	}
}

func finalHandoverWriteName(sequence uint64) string {
	if sequence == 0 {
		return "terminal"
	}
	return "sequence-" + strconv.FormatUint(sequence, 10)
}

func TestGatewayRebindFinalHandoverRollbackRestoresServiceBeforeTerminalAbort(t *testing.T) {
	for _, test := range []struct {
		name, fail            string
		after, initialStopped bool
	}{
		{name: "before cutover", fail: "create_final"},
		{name: "after predecessor stop", fail: "stop_predecessor", after: true},
		{name: "after final start", fail: "start_final", after: true},
		{name: "initial stopped predecessor with absent NIC", fail: "stop_stage", initialStopped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake := installGatewayRebindFinalHandoverFixture(t)
			if test.initialStopped {
				fake.observation.PredecessorRunning = false
				fake.observation.PredecessorAddress = gatewayRebindPredecessorAddressAbsent
				fake.observation.PredecessorRoutesDigest = ""
				fake.observation.PredecessorStopDigest = strings.Repeat("e", 64)
				fake.initialPredecessorDigest = strings.Repeat("e", 64)
				fake.observation.PredecessorObservationDigest = fake.initialPredecessorDigest
			}
			fake.failAt, fake.failAfter = test.fail, test.after
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err == nil {
				t.Fatal("fault not reached")
			}
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			if err := fake.manager.rollbackGatewayRebindFinalHandoverWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			history := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalAbort)
			if fake.observation.PredecessorRunning == test.initialStopped || fake.observation.Stage != gatewayRebindHandoverContainerAbsent || fake.observation.Final != gatewayRebindHandoverContainerAbsent || fake.observation.ConfigVolumePresent || fake.observation.DataVolumePresent || fake.observation.IngressNetworkPresent {
				t.Fatal("abort lacks expected exact physical outcome")
			}
			if test.after && test.fail == "start_final" {
				stop, start := -1, -1
				for i, effect := range fake.effects {
					if effect == "stop_final" {
						stop = i
					}
					if effect == "start_predecessor" {
						start = i
					}
				}
				if stop < 0 || start <= stop {
					t.Fatalf("rollback did not withdraw final before predecessor restore: %v", fake.effects)
				}
			}
			before := append([]string(nil), fake.effects...)
			beforeBytes := liveGatewayRebindProgressBytes(t, history)
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(40), nil); err != nil {
				t.Fatalf("terminal abort replay: %v", err)
			}
			after := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalAbort)
			if !reflect.DeepEqual(before, fake.effects) || !reflect.DeepEqual(beforeBytes, liveGatewayRebindProgressBytes(t, after)) {
				t.Fatal("abort replay mutated physical/history state")
			}
		})
	}
}

func TestGatewayRebindFinalHandoverCheckpointAndTerminalDriftNeverRepair(t *testing.T) {
	fixture, fake := installGatewayRebindFinalHandoverFixture(t)
	checkpoint := func() { fake.observation.HostDigest = strings.Repeat("f", 64) }
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), checkpoint); err == nil {
		t.Fatal("host drift across checkpoint accepted")
	}
	history, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 || len(fake.effects) != 0 {
		t.Fatal("checkpoint drift advanced history or Docker")
	}
	fake.observation.HostDigest = strings.Repeat("a", 64)
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), fake.effects...)
	fake.observation.RoutesDigest = ""
	fake.manager = newGatewayRebindStageContainerManager(t, fixture)
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err == nil {
		t.Fatal("terminal replay accepted missing route proof")
	}
	if !reflect.DeepEqual(before, fake.effects) {
		t.Fatal("terminal drift triggered automatic repair")
	}
}

func TestGatewayRebindFinalHandoverRollbackLostAcknowledgmentResumesWithoutRepeatingEffects(t *testing.T) {
	for _, effect := range []string{"stop_final", "start_predecessor", "remove_final", "remove_config", "remove_data", "remove_network"} {
		t.Run(effect, func(t *testing.T) {
			fixture, fake := installGatewayRebindFinalHandoverFixture(t)
			// Leave the exact bound final running at cutover with its start
			// acknowledgment lost. Explicit rollback then owns all compensation.
			fake.failAt, fake.failAfter = "start_final", true
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err == nil {
				t.Fatal("final start acknowledgment fault was not reached")
			}
			fake.failAt, fake.failAfter, fake.failed = effect, true, false
			if err := fake.manager.rollbackGatewayRebindFinalHandoverWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err == nil || !fake.failed {
				t.Fatal("rollback acknowledgment fault was not reached")
			}
			pending, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(pending.Terminals) != 0 || pending.Progress[len(pending.Progress)-1].Record.Phase != gatewayRebindProgressRollbackIntent {
				t.Fatal("uncertain compensation did not retain a nonterminal rollback intent")
			}
			before := liveGatewayRebindProgressBytes(t, pending)
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			// A normal resume must honor the durable rollback decision rather
			// than returning to forward cutover or needing another rollback vote.
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(60), nil); err != nil {
				t.Fatalf("resume durable rollback: %v", err)
			}
			after := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalAbort)
			for index, body := range before {
				actual, err := os.ReadFile(after.Progress[index].Store.path)
				if err != nil || !bytes.Equal(body, actual) {
					t.Fatal("rollback recovery rewrote existing history")
				}
			}
			want := []string{"stop_stage", "remove_stage", "create_final", "stop_predecessor", "start_final", "stop_final", "start_predecessor", "remove_final", "remove_config", "remove_data", "remove_network"}
			if !reflect.DeepEqual(fake.effects, want) {
				t.Fatalf("rollback repeated or reordered effects: %v", fake.effects)
			}
			if !fake.observation.PredecessorRunning || fake.observation.Final != gatewayRebindHandoverContainerAbsent || fake.observation.ConfigVolumePresent || fake.observation.DataVolumePresent || fake.observation.IngressNetworkPresent {
				t.Fatal("abort was committed before original service and successor cleanup were proven")
			}
		})
	}
}

func TestGatewayRebindFinalHandoverCancellationAfterPredecessorStopRequiresFreshInvocation(t *testing.T) {
	fixture, fake := installGatewayRebindFinalHandoverFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.onEffect = func(name string) {
		if name == "stop_predecessor" {
			cancel()
		}
	}
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(ctx, fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err == nil {
		t.Fatal("cancelled handover reported success")
	}
	if fake.observation.PredecessorRunning || fake.observation.Final != gatewayRebindHandoverContainerStopped || containsString(fake.effects, "start_final") {
		t.Fatal("cancellation advanced beyond exact predecessor stop")
	}
	fake.onEffect = nil
	fake.manager = newGatewayRebindStageContainerManager(t, fixture)
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err != nil {
		t.Fatal(err)
	}
	assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalCommit)
}

func TestGatewayRebindFinalHandoverFreshLoopbackHeadDriftPreventsAllEffects(t *testing.T) {
	fixture, fake := installGatewayRebindFinalHandoverFixture(t)
	for _, trigger := range []string{"lan_gateway_rebind_fence_runtime_head_update", "generated_runtime_active_head_valid_update"} {
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := func() {
		result, err := fixture.predecessor.db.Exec(`UPDATE generated_runtime_active_heads SET generation=generation+1,updated_at=? WHERE app_id=?`, gatewayRebindProgressTimestamp(40).Format(time.RFC3339Nano), "ffffffff-ffff-4fff-8fff-ffffffffffff")
		if err != nil {
			t.Fatal(err)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("loopback drift rows=%d error=%v", n, err)
		}
	}
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), checkpoint); err == nil {
		t.Fatal("fresh loopback head drift authorized handover")
	}
	history, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 || len(history.Terminals) != 0 || len(fake.effects) != 0 {
		t.Fatal("runtime-head drift changed protected or physical state")
	}
}

const finalHandoverCrashFixtureEnvironment = "RIG_TEST_FINAL_HANDOVER_CRASH_FIXTURE"
const finalHandoverCrashModeEnvironment = "RIG_TEST_FINAL_HANDOVER_CRASH_MODE"

// Only the independent fake physical world is serialized here. The child reads
// the actual SQLite database and protected records written by the production
// coordinator; no progress or terminal record is synthesized by this fixture.
type finalHandoverCrashFixture struct {
	DataRoot, WorkingDirectory, DockerConfigDirectory, DockerExecutable string
	HostPort                                                            uint16
	Observation                                                         gatewayRebindFinalHandoverObservation
	InitialPredecessorDigest, PredecessorRoutesDigest, FinalID          string
	Effects                                                             []string
}

func TestGatewayRebindFinalHandoverProcessCrashAfterPredecessorStopRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess coverage is disabled in short mode")
	}
	fixture, fake := installGatewayRebindFinalHandoverFixture(t)
	options := fake.manager.options
	state := finalHandoverCrashFixture{DataRoot: options.DataRoot, WorkingDirectory: options.WorkingDirectory,
		DockerConfigDirectory: options.DockerConfigDirectory, DockerExecutable: options.DockerExecutable, HostPort: options.HostPort,
		Observation: fake.observation, InitialPredecessorDigest: fake.initialPredecessorDigest,
		PredecessorRoutesDigest: fake.predecessorRoutesDigest, FinalID: fake.finalID}
	path := filepath.Join(t.TempDir(), "physical-fixture.json")
	writeFinalHandoverCrashFixture(t, path, state)
	run := func(mode string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGatewayRebindFinalHandoverCrashHelper$", "-test.count=1")
		command.Env = append(os.Environ(), finalHandoverCrashFixtureEnvironment+"="+path, finalHandoverCrashModeEnvironment+"="+mode)
		output, err := command.CombinedOutput()
		if err != nil && mode != "crash" {
			t.Fatalf("fresh-process %s: %v\n%s", mode, err, output)
		}
		return err
	}
	err := run("crash")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("helper did not terminate at predecessor-stop boundary: %v", err)
	}
	history, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 15 || history.Progress[14].Record.Phase != gatewayRebindProgressCutoverIntent || len(history.Terminals) != 0 {
		t.Fatalf("crash history=%d scan=%v", len(history.Progress), err)
	}
	crashed := readFinalHandoverCrashFixture(t, path)
	if crashed.Observation.PredecessorRunning || crashed.Observation.Final != gatewayRebindHandoverContainerStopped || containsString(crashed.Effects, "start_final") {
		t.Fatal("crash snapshot did not retain exact stopped predecessor/final")
	}
	run("resume")
	assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalCommit)
	completed := readFinalHandoverCrashFixture(t, path)
	want := []string{"stop_stage", "remove_stage", "create_final", "stop_predecessor", "start_final"}
	if !reflect.DeepEqual(completed.Effects, want) {
		t.Fatalf("fresh process repeated an effect: %v", completed.Effects)
	}
	run("replay")
	if after := readFinalHandoverCrashFixture(t, path); !reflect.DeepEqual(after, completed) {
		t.Fatal("separate terminal replay process changed physical snapshot")
	}
}

func TestGatewayRebindFinalHandoverCrashHelper(t *testing.T) {
	path := os.Getenv(finalHandoverCrashFixtureEnvironment)
	if path == "" {
		return
	}
	state := readFinalHandoverCrashFixture(t, path)
	manager, err := New(&finalConfigDriverRunner{}, Options{DataRoot: state.DataRoot, WorkingDirectory: state.WorkingDirectory,
		DockerConfigDirectory: state.DockerConfigDirectory, DockerExecutable: state.DockerExecutable, HostPort: state.HostPort,
		RebindFenceCheck: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(state.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fake := &gatewayRebindFinalHandoverFake{t: t, manager: manager, observation: state.Observation, initialPredecessorDigest: state.InitialPredecessorDigest,
		predecessorRoutesDigest: state.PredecessorRoutesDigest, finalID: state.FinalID, effects: append([]string(nil), state.Effects...)}
	fake.onEffect = func(name string) {
		state.Observation, state.Effects = fake.observation, append([]string(nil), fake.effects...)
		writeFinalHandoverCrashFixture(t, path, state)
		if os.Getenv(finalHandoverCrashModeEnvironment) == "crash" && name == "stop_predecessor" {
			os.Exit(73)
		}
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) == 0 {
		t.Fatal("child could not read current protected progress")
	}
	lastAt, err := time.Parse(time.RFC3339Nano, history.Progress[len(history.Progress)-1].Record.OccurredAt)
	if err != nil {
		t.Fatal("child could not parse protected progress timestamp")
	}
	requestedAt := lastAt.Add(time.Second)
	if err := manager.handoverGatewayRebindFinalWithDriver(context.Background(), appaccess.New(db), fake, requestedAt, nil); err != nil {
		t.Fatal(err)
	}
}

func writeFinalHandoverCrashFixture(t *testing.T, path string, value finalHandoverCrashFixture) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("persist test physical snapshot: write=%v close=%v", err, closeErr)
	}
}
func readFinalHandoverCrashFixture(t *testing.T, path string) finalHandoverCrashFixture {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value finalHandoverCrashFixture
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertGatewayRebindFinalHandoverTerminal(t *testing.T, fixture gatewayRebindEffectBoundaryFixture, want gatewayRebindFinalHandoverTerminalDisposition) gatewayRebindProtectedIntentHistory {
	t.Helper()
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Terminals) != 1 || history.Terminals[0].Receipt.Disposition != want {
		t.Fatalf("terminal=%v scan=%v", history.Terminals, err)
	}
	return history
}

var _ gatewayRebindFinalHandoverDriver = (*gatewayRebindFinalHandoverFake)(nil)
