package generatedingress

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveFinalHandoverEnvironment = "RIG_RUN_LIVE_GATEWAY_REBIND_HANDOVER"
const liveFinalHandoverChildEnvironment = "RIG_TEST_LIVE_FINAL_HANDOVER_CHILD"
const liveFinalHandoverModeEnvironment = "RIG_TEST_LIVE_FINAL_HANDOVER_MODE"

// liveGatewayRebindSourceFixture owns the original upgraded gateway and two
// applications, before any rebind claim or successor resource exists. Each
// journey must separately register cleanup for its exact successor identities.
type liveGatewayRebindSourceFixture struct {
	fixture                        *liveGatewayV2Fixture
	db                             *sql.DB
	repository                     *appaccess.Repository
	profile                        appaccess.GatewayProfileRevision
	proposal                       appaccess.GatewayRebindPreclaimProposal
	loopbackID, loopbackReply      string
	predecessorIPv4, successorIPv4 string
}

type liveFinalHandoverFixture struct {
	liveGatewayRebindSourceFixture
	intent   gatewayRebindProtectedIntent
	before   gatewayRebindProtectedIntentHistory
	prepared appaccess.GatewayRebindStartupSnapshot
}

func newLiveGatewayRebindSourceFixture(t *testing.T, permissionEnvironment string) liveGatewayRebindSourceFixture {
	t.Helper()
	before, after := liveGatewayRebindHostAddresses(t, permissionEnvironment)
	spec := liveGatewayV2FixtureSpec{appID: "e1111111-1111-4111-8111-111111111111", planID: "e2222222-2222-4222-8222-222222222222",
		operationID: "e3333333-3333-4333-8333-333333333333", profileRevision: "e4444444-4444-4444-8444-444444444444",
		approvedBy: gatewayRebindTestAdministrator, imageTag: "rig-generated-gateway-v2-live:final-handover", applicationReply: "final-handover-lan", countRequests: true}
	fixture := newLiveGatewayV2Fixture(t, spec)
	journeyContext, cancelJourney := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancelJourney)
	fixture.ctx = journeyContext
	fixture.interfaceIP = before.IPv4
	fixture.request = liveGatewayV2Request(t, spec, before, fixture.port)
	loopbackID, loopbackPlan := "f1111111-1111-4111-8111-111111111111", "f2222222-2222-4222-8222-222222222222"
	loopbackReply := "final-handover-loopback"
	appNetwork, err := generatedruntime.DescribeAppNetwork(loopbackID)
	if err != nil {
		t.Fatal(err)
	}
	image := liveGatewayImage{tag: "rig-generated-gateway-v2-live:final-handover-loopback", appID: loopbackID}
	// The first fixture already owns v1; only the new application's identities
	// must be absent at this boundary.
	if liveGatewayResourceExists(t, fixture.ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, "network", appNetwork.Name) ||
		liveGatewayResourceExists(t, fixture.ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, "image", image.tag) {
		t.Fatal("loopback-only fixture identities already exist")
	}
	engine, err := generatedruntime.NewEngine(fixture.runner, liveEnvironmentStager{}, liveCapacitySource{}, generatedruntime.EngineOptions{
		DockerExecutable: fixture.docker, DockerConfigDirectory: fixture.dockerConfig, WorkingDirectory: fixture.ingress.options.WorkingDirectory,
		CommandTimeout: 45 * time.Second, HealthTimeout: 90 * time.Second, HealthPollInterval: 250 * time.Millisecond, OutputLimit: liveDockerOutputLimit,
		Limits: generatedruntime.ContainerLimits{MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 128, TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 2}, ReplacementDiskBytes: 96 << 20})
	if err != nil {
		t.Fatal("create second live runtime")
	}
	var secondCandidates []generatedruntime.Candidate
	t.Cleanup(func() {
		// Both application networks are still attached to the retained predecessor.
		// Remove that exact bound gateway before either application network.
		liveGatewayV2Cleanup(t, fixture)
		liveGatewayCleanup(t, engine, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
			secondCandidates, map[string]string{appNetwork.Name: loopbackID}, []liveGatewayImage{image})
	})
	secondSpec := liveCandidateSpec("green", loopbackID, loopbackPlan)
	secondSpec.ImageContentID = buildLiveGatewayImage(t, fixture.ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, image.tag, secondSpec, loopbackReply, "0.0.0.0")
	second := startLiveCandidate(t, fixture.ctx, engine, secondSpec)
	secondCandidates = append(secondCandidates, second)
	if err := fixture.ingress.Switch(fixture.ctx, generatedruntime.RouteSwitchRequest{AppID: loopbackID, ToSlot: second.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(second)}}); err != nil {
		failLiveIngress(t, "install loopback-only source route", err)
	}
	engine.ReleaseAdmission(second)
	fixture.source, err = fixture.ingress.store.load()
	if err != nil || len(fixture.source.Active) != 2 {
		t.Fatal("load complete two-application source")
	}
	db, repository := fixture.db, fixture.repository
	profile := prepareLiveGatewayRebindStagePredecessor(t, fixture, db, repository)
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, loopbackID, "handover-loopback", "Loopback-only handover app"); err != nil {
		t.Fatal(err)
	}
	secondFixture := *fixture
	secondFixture.candidates = []generatedruntime.Candidate{second}
	seedLiveFinalHandoverLoopbackRuntime(t, db, &secondFixture)
	proposal := seedLiveGatewayRebindPublicPassiveLineage(t, fixture, db, repository, profile, appaccess.GatewayProfileSpec{
		SelectedIPv4: after.IPv4, InterfaceID: after.InterfaceID, PortStart: fixture.port, PortEnd: fixture.port})
	return liveGatewayRebindSourceFixture{fixture: fixture, db: db, repository: repository,
		profile: profile, proposal: proposal, loopbackID: loopbackID, loopbackReply: loopbackReply,
		predecessorIPv4: before.IPv4, successorIPv4: after.IPv4}
}

func newLiveFinalHandoverFixture(t *testing.T) liveFinalHandoverFixture {
	t.Helper()
	source := newLiveGatewayRebindSourceFixture(t, liveFinalHandoverEnvironment)
	fixture, db, repository, proposal := source.fixture, source.db, source.repository, source.proposal
	state, journal, store := liveGatewayV2LoadDurableOperation(t, fixture)
	if len(state.Apps) != 2 || state.Apps[source.loopbackID].LAN != nil || state.Apps[fixture.spec.appID].LAN == nil {
		t.Fatal("predecessor does not bind LAN plus loopback-only routes")
	}
	preclaim, err := repository.GatewayRebindPreclaimSnapshot(fixture.ctx, proposal)
	if err != nil || !gatewayRebindPreclaimMatches(preclaim, state, journal) {
		t.Fatal("admit real two-app handover claim")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, state, journal) {
		t.Fatal("bind prepared handover claim")
	}
	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil {
		t.Fatal("observe approved real successor address")
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve handover successor generation")
	}
	predecessor := gatewayUpgradeGenerationSelection{Store: store, Generation: store.generation, State: state, Journal: journal, Existing: true, operationID: journal.OperationID}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact approved handover intent")
	}
	t.Cleanup(func() { cleanupLiveGatewayRebindFinalHandover(t, fixture, repository, intent) })
	imageObservation, found, err := (managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}).inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(imageObservation, found) {
		t.Fatal("inspect pinned handover gateway image")
	}
	now := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, now)
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, 1)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install handover sequence one")
	}
	secondRecord, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{OccurredAt: now.Add(time.Nanosecond), ObservedDockerImageID: normalizeID(imageObservation.ID), NetworkTopologyDigest: intent.NetworkObservationDigest})
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, 2)
	if err != nil || secondStore.installExact(fixture.ctx, secondRecord) != nil {
		t.Fatal("install handover sequence two")
	}
	type stageCall func(context.Context, *appaccess.Repository, gatewayRebindSuccessorPreflightReads, gatewayRebindDockerInspector, time.Time, func()) error
	for index, run := range []stageCall{
		fixture.ingress.stageGatewayRebindSuccessorNetwork, fixture.ingress.stageGatewayRebindSuccessorConfigVolume,
		fixture.ingress.stageGatewayRebindSuccessorDataVolume, fixture.ingress.stageGatewayRebindSuccessorContainer,
		fixture.ingress.prepareGatewayRebindSuccessorStageConfigIntent, fixture.ingress.copyGatewayRebindSuccessorStageConfig,
		fixture.ingress.prepareGatewayRebindSuccessorStageStartIntent, fixture.ingress.startGatewayRebindSuccessorStage,
		fixture.ingress.prepareGatewayRebindSuccessorFinalConfigIntent, fixture.ingress.copyGatewayRebindSuccessorFinalConfig,
	} {
		if err := run(fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, now.Add(time.Duration(index+2)*time.Nanosecond), nil); err != nil {
			failLiveIngress(t, fmt.Sprintf("prepare real handover sequence %d", index+3), err)
		}
	}
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 || len(history.Progress[11].Record.Stage.FinalConfigIntent.RoutePlan.Routes) != 2 {
		t.Fatal("handover requires exact two-app sequence twelve")
	}
	return liveFinalHandoverFixture{liveGatewayRebindSourceFixture: source, intent: intent, before: history, prepared: prepared}
}

func TestLiveGatewayRebindFinalHandoverCommitAndRestart(t *testing.T) {
	liveGatewayRebindFinalHandoverJourney(t, false)
}

func TestLiveGatewayRebindFinalHandoverRollbackAndRestart(t *testing.T) {
	liveGatewayRebindFinalHandoverJourney(t, true)
}

func liveGatewayRebindFinalHandoverJourney(t *testing.T, rollback bool) {
	t.Helper()
	f := newLiveFinalHandoverFixture(t)
	fixture, manager := f.fixture, f.fixture.ingress
	beforeBytes := liveGatewayRebindProgressBytes(t, f.before)
	beforeHeads, err := f.repository.GatewayRebindRuntimeHeads(fixture.ctx)
	if err != nil || len(beforeHeads) != 2 {
		t.Fatal("read complete two-app runtime heads")
	}
	options := manager.options
	child := liveFinalHandoverChild{DataRoot: options.DataRoot, WorkingDirectory: options.WorkingDirectory,
		DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory, HostPort: options.HostPort}
	path := filepath.Join(fixture.root, "handover-child.json")
	body, err := json.Marshal(child)
	if err != nil || os.WriteFile(path, body, 0o600) != nil {
		t.Fatal("write private child fixture configuration")
	}
	mode := "crash-after-stop"
	if rollback {
		mode = "crash-after-start"
	}
	liveRunFinalHandoverChild(t, fixture.ctx, path, mode)
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 15 || len(history.Terminals) != 0 || history.Progress[14].Record.Phase != gatewayRebindProgressCutoverIntent {
		t.Fatal("crash did not retain exact pre-serving cutover history")
	}
	driver := newManagerGatewayRebindFinalHandoverDriver(manager, liveGatewayRebindStageProductionReads(manager), manager.inspectGatewayRebindDocker)
	contextValue := (&gatewayRebindFinalHandoverRun{}).handoverContext(history, history.Progress[14].Record, "")
	physical, err := driver.observeHandover(fixture.ctx, contextValue)
	if err != nil || physical.Stage != gatewayRebindHandoverContainerAbsent || physical.PredecessorRunning || physical.PredecessorAddress != gatewayRebindPredecessorAddressPresent {
		t.Fatal("fresh controller did not prove withdrawn predecessor after process crash")
	}
	if rollback {
		if physical.Final != gatewayRebindHandoverContainerRunning || physical.RoutesDigest != contextValue.Plan.RoutePlanDigest {
			t.Fatal("real final start crash did not retain complete successor routing")
		}
	} else if physical.Final != gatewayRebindHandoverContainerStopped || physical.RoutesDigest != "" {
		t.Fatal("real predecessor-stop crash dispatched final start")
	}
	if !probeGatewayRebindHandoverListenerAbsent(fixture.ctx, f.predecessorIPv4, fixture.port) {
		t.Fatal("old LAN bind remains after actual predecessor stop")
	}
	action := "resume"
	want := gatewayRebindFinalHandoverTerminalCommit
	if rollback {
		action, want = "rollback", gatewayRebindFinalHandoverTerminalAbort
	}
	liveRunFinalHandoverChild(t, fixture.ctx, path, action)
	terminal, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(terminal.Terminals) != 1 || terminal.Terminals[0].Receipt.Disposition != want {
		t.Fatal("fresh process did not install exact terminal receipt")
	}
	terminalBytes := liveGatewayRebindProgressBytes(t, terminal)
	for index := range beforeBytes {
		if !bytes.Equal(beforeBytes[index], terminalBytes[index]) {
			t.Fatalf("handover rewrote predecessor progress sequence %d", index+1)
		}
	}
	terminalFile, err := os.ReadFile(terminal.Terminals[0].Store.path)
	if err != nil {
		t.Fatal("read exact terminal receipt bytes")
	}
	liveRunFinalHandoverChild(t, fixture.ctx, path, "replay")
	replayed, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterReceipt, readErr := os.ReadFile(terminal.Terminals[0].Store.path)
	if err != nil || readErr != nil || !bytes.Equal(terminalFile, afterReceipt) || !reflect.DeepEqual(terminalBytes, liveGatewayRebindProgressBytes(t, replayed)) {
		t.Fatal("terminal subprocess replay rewrote immutable evidence")
	}
	afterPrepared, err := f.repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(f.prepared, afterPrepared) {
		t.Fatal("private handover changed prepared SQL claim or grants")
	}
	afterHeads, err := f.repository.GatewayRebindRuntimeHeads(fixture.ctx)
	if err != nil || !reflect.DeepEqual(beforeHeads, afterHeads) {
		t.Fatal("handover changed active generated-runtime heads")
	}
	if !gatewayRebindGenerationSelectionEqual(f.before.Predecessor, replayed.Predecessor) || !reflect.DeepEqual(f.before.Source, replayed.Source) {
		t.Fatal("private terminal outcome advanced current predecessor or public source routes")
	}
	assertLiveOneShot(t, fixture.ctx, options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply, "handover did not preserve LAN app loopback route")
	assertLiveOneShot(t, fixture.ctx, options.HostPort, f.loopbackID, "/", f.loopbackReply, "handover lost loopback-only application")
	active, withdrawn := f.successorIPv4, f.predecessorIPv4
	if rollback {
		active, withdrawn = f.predecessorIPv4, f.successorIPv4
	}
	if !liveGatewayRebindRuntimeLANBody(fixture.ctx, active, fixture.port, fixture.spec.applicationReply) {
		t.Fatal("selected final LAN route returned the wrong application")
	}
	wrongHost := driver.hostProbe(fixture.ctx, active, fixture.port, f.loopbackID+".rig.localhost", "/")
	if !wrongHost.Connected || !wrongHost.Responded || wrongHost.Status != 404 {
		t.Fatal("loopback-only app leaked onto LAN listener")
	}
	if !probeGatewayRebindHandoverListenerAbsent(fixture.ctx, withdrawn, fixture.port) || !probeGatewayRebindHandoverListenerAbsent(fixture.ctx, "127.0.0.1", fixture.port) {
		t.Fatal("inactive LAN publication was not withdrawn")
	}
	t.Log("verified two actual private addresses, two complete routes, old bind withdrawal, fresh-process recovery, and immutable terminal replay; SQL remains prepared")
}

type liveFinalHandoverChild struct {
	DataRoot, WorkingDirectory, DockerExecutable, DockerConfigDirectory string
	HostPort                                                            uint16
}

func liveRunFinalHandoverChild(t *testing.T, ctx context.Context, path, mode string) {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveGatewayRebindFinalHandoverChild$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), liveFinalHandoverChildEnvironment+"="+path, liveFinalHandoverModeEnvironment+"="+mode)
	output, err := command.CombinedOutput()
	if strings.HasPrefix(mode, "crash-") {
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 73 || !bytes.Contains(output, []byte("HANDOVER_EFFECT_COMPLETED:"+mode)) {
			t.Fatalf("child did not reach exact real Docker crash boundary: %v\n%s", err, output)
		}
	} else if err != nil || !bytes.Contains(output, []byte("HANDOVER_CHILD_COMPLETED:"+mode)) {
		t.Fatalf("fresh-process %s failed: %v\n%s", mode, err, output)
	}
}

func TestLiveGatewayRebindFinalHandoverChild(t *testing.T) {
	path := os.Getenv(liveFinalHandoverChildEnvironment)
	if path == "" {
		return
	}
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" || os.Getenv(liveFinalHandoverEnvironment) != "1" || runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("child requires explicit disposable local Docker permission")
	}
	body, err := os.ReadFile(path)
	var value liveFinalHandoverChild
	if err != nil || json.Unmarshal(body, &value) != nil {
		t.Fatal("read child fixture paths")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	manager, err := New(runtimeprocess.ExecRunner{}, Options{DataRoot: value.DataRoot, WorkingDirectory: value.WorkingDirectory, DockerExecutable: value.DockerExecutable,
		DockerConfigDirectory: value.DockerConfigDirectory, HostPort: value.HostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute, OutputLimit: liveDockerOutputLimit,
		RebindFenceCheck: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal("reopen real controller in independent process")
	}
	db, err := database.Open(value.DataRoot)
	if err != nil {
		t.Fatal("reopen real SQLite in independent process")
	}
	defer db.Close()
	mode := os.Getenv(liveFinalHandoverModeEnvironment)
	driver := &liveFinalHandoverDriver{managerGatewayRebindFinalHandoverDriver: newManagerGatewayRebindFinalHandoverDriver(manager, liveGatewayRebindStageProductionReads(manager), manager.inspectGatewayRebindDocker), mode: mode}
	if mode == "rollback" {
		err = manager.rollbackGatewayRebindFinalHandoverWithDriver(ctx, appaccess.New(db), driver, time.Now().UTC(), nil)
	} else {
		err = manager.handoverGatewayRebindFinalWithDriver(ctx, appaccess.New(db), driver, time.Now().UTC(), nil)
	}
	if err != nil {
		t.Logf("private handover refusal: effects=%d coordinator_observations=%d last_phase=%q last_observation_failed=%t handover_diagnostic_stage=%q",
			driver.effects, driver.observations, driver.lastPhase, driver.lastObservationFailed, driver.lastDiagnosticStage)
		failLiveIngress(t, "run private handover in independent process", err)
	}
	if mode == "replay" && driver.effects != 0 {
		t.Fatal("terminal replay dispatched a Docker effect")
	}
	fmt.Println("HANDOVER_CHILD_COMPLETED:" + mode)
}

type liveFinalHandoverDriver struct {
	managerGatewayRebindFinalHandoverDriver
	mode                  string
	effects               int
	observations          int
	lastPhase             gatewayRebindProgressPhase
	lastObservationFailed bool
	lastDiagnosticStage   string
}

// Record only bounded diagnostic metadata. All observation decisions and
// effects still use the production driver; no failed proof is normalized.
func (d *liveFinalHandoverDriver) observeHandover(ctx context.Context,
	value gatewayRebindFinalHandoverContext,
) (gatewayRebindFinalHandoverObservation, error) {
	d.observations++
	d.lastPhase = value.Phase
	observation, err := d.managerGatewayRebindFinalHandoverDriver.observeHandover(ctx, value)
	d.lastObservationFailed = err != nil
	d.lastDiagnosticStage = ""
	if stage, ok := gatewayRebindHandoverDiagnosticStageFrom(err); ok {
		d.lastDiagnosticStage = stage.String()
	}
	return observation, err
}

func (d *liveFinalHandoverDriver) effect(name string, run func() error) error {
	d.effects++
	if d.mode == "replay" {
		return errors.New("terminal replay attempted a Docker effect")
	}
	if err := run(); err != nil {
		return err
	}
	if (d.mode == "crash-after-stop" && name == "stop_predecessor") || (d.mode == "crash-after-start" && name == "start_final") {
		fmt.Println("HANDOVER_EFFECT_COMPLETED:" + d.mode)
		_ = os.Stdout.Sync()
		os.Exit(73)
	}
	return nil
}
func (d *liveFinalHandoverDriver) createFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) (string, error) {
	d.effects++
	if d.mode == "replay" {
		return "", errors.New("terminal replay attempted Docker create")
	}
	return d.managerGatewayRebindFinalHandoverDriver.createFinal(ctx, v)
}
func (d *liveFinalHandoverDriver) stopStage(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("stop_stage", func() error { return d.managerGatewayRebindFinalHandoverDriver.stopStage(ctx, v) })
}
func (d *liveFinalHandoverDriver) removeStage(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("remove_stage", func() error { return d.managerGatewayRebindFinalHandoverDriver.removeStage(ctx, v) })
}
func (d *liveFinalHandoverDriver) stopPredecessor(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("stop_predecessor", func() error { return d.managerGatewayRebindFinalHandoverDriver.stopPredecessor(ctx, v) })
}
func (d *liveFinalHandoverDriver) startPredecessor(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("start_predecessor", func() error { return d.managerGatewayRebindFinalHandoverDriver.startPredecessor(ctx, v) })
}
func (d *liveFinalHandoverDriver) startFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("start_final", func() error { return d.managerGatewayRebindFinalHandoverDriver.startFinal(ctx, v) })
}
func (d *liveFinalHandoverDriver) stopFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("stop_final", func() error { return d.managerGatewayRebindFinalHandoverDriver.stopFinal(ctx, v) })
}
func (d *liveFinalHandoverDriver) removeFinal(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("remove_final", func() error { return d.managerGatewayRebindFinalHandoverDriver.removeFinal(ctx, v) })
}
func (d *liveFinalHandoverDriver) removeConfigVolume(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("remove_config", func() error { return d.managerGatewayRebindFinalHandoverDriver.removeConfigVolume(ctx, v) })
}
func (d *liveFinalHandoverDriver) removeDataVolume(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("remove_data", func() error { return d.managerGatewayRebindFinalHandoverDriver.removeDataVolume(ctx, v) })
}
func (d *liveFinalHandoverDriver) removeIngressNetwork(ctx context.Context, v gatewayRebindFinalHandoverContext) error {
	return d.effect("remove_network", func() error { return d.managerGatewayRebindFinalHandoverDriver.removeIngressNetwork(ctx, v) })
}

var _ gatewayRebindFinalHandoverDriver = (*liveFinalHandoverDriver)(nil)

func seedLiveFinalHandoverLoopbackRuntime(t *testing.T, db *sql.DB,
	fixture *liveGatewayV2Fixture,
) generatedruntimestate.ActiveHead {
	t.Helper()
	candidate := fixture.candidates[0]
	spec := liveCandidateSpec("green", candidate.AppID, candidate.DeploymentPlanRevisionID)
	if candidate.ReleaseID != spec.ReleaseID || candidate.DeploymentID != spec.DeploymentID ||
		candidate.ArtifactID != spec.ArtifactID || candidate.Component != spec.ComponentName {
		t.Fatal("live candidate does not match its runtime seed identities")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
		candidate.DeploymentPlanRevisionID, candidate.AppID,
		"apps/"+candidate.AppID+"/deployment-plans/"+candidate.DeploymentPlanRevisionID+".secret",
		strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64),
		gatewayRebindTestAdministrator, stamp, gatewayRebindTestAdministrator, stamp); err != nil {
		t.Fatalf("seed live candidate deployment plan: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, candidate.ReleaseID, candidate.AppID, stamp,
		strings.Repeat("d", 64), strings.Repeat("f", 64), candidate.DeploymentPlanRevisionID); err != nil {
		t.Fatalf("seed live candidate release: %v", err)
	}
	jobID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,?,?)`, jobID, candidate.AppID,
		gatewayRebindTestAdministrator, stamp, stamp); err != nil {
		t.Fatalf("seed live candidate job: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, candidate.DeploymentID,
		candidate.AppID, candidate.ReleaseID, jobID, candidate.DeploymentPlanRevisionID); err != nil {
		t.Fatalf("seed live candidate deployment: %v", err)
	}
	runtime := generatedruntimestate.New(db)
	value, _, err := runtime.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: candidate.DeploymentID, AppID: candidate.AppID, ReleaseID: candidate.ReleaseID,
		DeploymentPlanRevisionID:     candidate.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: 1, ComponentNames: []string{candidate.Component},
	})
	if err != nil {
		t.Fatalf("begin live candidate runtime: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,
		created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, candidate.ArtifactID,
		candidate.ReleaseID, candidate.DeploymentPlanRevisionID, 1, candidate.Component,
		spec.BuildDefinitionDigest, candidate.ImageContentID, stamp, stamp, stamp); err != nil {
		t.Fatalf("seed live candidate image artifact: %v", err)
	}
	if _, err := runtime.SetImageReady(context.Background(), candidate.AppID, candidate.DeploymentID,
		candidate.Component, candidate.ArtifactID); err != nil {
		t.Fatalf("mark live candidate image ready: %v", err)
	}
	if _, err := runtime.SetContainerStarting(context.Background(), candidate.AppID, candidate.DeploymentID,
		candidate.Component, candidate.ContainerName); err != nil {
		t.Fatalf("mark live candidate container starting: %v", err)
	}
	if _, err := runtime.SetContainerRunning(context.Background(), candidate.AppID, candidate.DeploymentID,
		candidate.Component, candidate.ContainerID); err != nil {
		t.Fatalf("mark live candidate container running: %v", err)
	}
	if _, err := runtime.AdvanceComponent(context.Background(), candidate.AppID, candidate.DeploymentID,
		candidate.Component, generatedruntimestate.ComponentRunning,
		generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatalf("mark live candidate component healthy: %v", err)
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(context.Background(), candidate.AppID, candidate.DeploymentID,
			value.Phase, next, "")
		if err != nil {
			t.Fatalf("advance live candidate runtime: %v", err)
		}
	}
	head, changed, err := runtime.SwitchActive(context.Background(), candidate.AppID, candidate.DeploymentID, 0)
	if err != nil || !changed || head.DeploymentID != candidate.DeploymentID ||
		head.ReleaseID != candidate.ReleaseID || head.Slot != string(candidate.Slot) {
		t.Fatalf("switch live candidate runtime: changed=%t err=%v", changed, err)
	}
	completed := time.Now().UTC().Format(time.RFC3339Nano)
	jobResult, err := db.Exec(`UPDATE jobs SET status='succeeded',phase='completed',updated_at=?,finished_at=?
		WHERE id=? AND status='running'`, completed, completed, jobID)
	if err != nil {
		t.Fatalf("settle live candidate job: %v", err)
	}
	jobRows, err := jobResult.RowsAffected()
	if err != nil || jobRows != 1 {
		t.Fatalf("settle exact live candidate job: rows=%d err=%v", jobRows, err)
	}
	deploymentResult, err := db.Exec(`UPDATE deployments SET status='succeeded',finished_at=?
		WHERE id=? AND status='preparing'`, completed, candidate.DeploymentID)
	if err != nil {
		t.Fatalf("settle live candidate deployment: %v", err)
	}
	deploymentRows, err := deploymentResult.RowsAffected()
	if err != nil || deploymentRows != 1 {
		t.Fatalf("settle exact live candidate deployment: rows=%d err=%v", deploymentRows, err)
	}
	return head
}
