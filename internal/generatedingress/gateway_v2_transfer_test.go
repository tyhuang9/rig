package generatedingress

import (
	"context"
	"errors"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestTransferGatewayV2KeepsV1ServingUntilStoppedFinalIsAttested(t *testing.T) {
	manager, store, state, driver, osLockHeld := gatewayV2TransferTestFixture(t)
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatal(err)
	}
	if *osLockHeld {
		t.Fatal("gateway OS lock remained held")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseCommitted || journal.Resources.FinalContainerID != strings.Repeat("e", 64) {
		t.Fatalf("committed journal = %+v", journal)
	}
	if driver.v1Running || driver.stageExists || !driver.finalExists || !driver.finalRunning {
		t.Fatalf("final topology = %+v", driver)
	}
	create := gatewayV2TransferEventIndex(driver.events, "create_final")
	stopV1 := gatewayV2TransferEventIndex(driver.events, "stop_v1")
	startFinal := gatewayV2TransferEventIndex(driver.events, "start_final")
	if create < 0 || stopV1 <= create || startFinal <= stopV1 || !driver.v1ServingAtFinalCreate {
		t.Fatalf("cutover ordering = %v", driver.events)
	}
	if driver.selectedPreflightCalls < 5 {
		t.Fatalf("selected-interface proofs = %d, want pre-create, pre-stop, pre-start, post-start, pre-commit", driver.selectedPreflightCalls)
	}
	creates := driver.createCalls
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("idempotent committed transfer: %v", err)
	}
	if driver.createCalls != creates {
		t.Fatalf("committed replay created final: before=%d after=%d", creates, driver.createCalls)
	}
}

func TestTransferGatewayV2CreateBeforeBindFailureMarksUnboundFinalUncertain(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	driver.failAt = "create_final_after_mutation"
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("error = %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseUncertain || journal.Resources.FinalContainerID != "" || !driver.v1Running ||
		!driver.finalExists || driver.finalRunning || driver.stopV1Calls != 0 || driver.removeFinalCalls != 0 {
		t.Fatalf("unbound final outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestTransferGatewayV2InterfaceDriftBeforeV1StopRollsBackWithoutOutage(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	driver.failSelectedPreflightAt = 2
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("interface drift was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || !driver.v1Running || driver.finalExists || driver.stopV1Calls != 0 || driver.removeFinalCalls != 1 {
		t.Fatalf("pre-stop rollback outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestTransferGatewayV2StartFailureRestoresV1AndRemovesBoundFinal(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	driver.failAt = "start_final"
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("final start failure was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || !driver.v1Running || driver.finalExists || driver.startV1Calls != 1 || driver.removeFinalCalls != 1 {
		t.Fatalf("start rollback outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestTransferGatewayV2AttestedStartSurvivesCommandError(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	driver.failAt = "start_final_after_mutation"
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("attested final start: %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseCommitted || driver.v1Running || !driver.finalRunning {
		t.Fatalf("attested start outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestTransferGatewayV2PostStartInterfaceDriftStopsFinalBeforeRestoringV1(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	driver.failSelectedPreflightAt = 4
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("post-start interface drift was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	stopFinal := gatewayV2TransferEventIndex(driver.events, "stop_final")
	startV1 := gatewayV2TransferEventIndex(driver.events, "start_v1")
	if journal.Phase != gatewayPhaseRolledBack || stopFinal < 0 || startV1 <= stopFinal || !driver.v1Running || driver.finalExists {
		t.Fatalf("post-start rollback: journal=%+v events=%v driver=%+v", journal, driver.events, driver)
	}
}

func TestTransferGatewayV2ResumesBoundStoppedFinal(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	expected, err := expectedGatewayV2FinalConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	driver.finalConfig = append([]byte(nil), expected...)
	if _, err := store.transitionMigrationJournal(state.OperationID, gatewayPhaseStaged, gatewayPhaseTransferIntent); err != nil {
		t.Fatal(err)
	}
	driver.stageExists = false
	driver.stageRunning = false
	driver.finalExists = true
	if _, err := store.bindMigrationDockerResource(state.OperationID, gatewayPhaseTransferIntent, gatewayV2ResourceFinalContainer, "sha256:"+strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatal(err)
	}
	_, committed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || committed.Phase != gatewayPhaseCommitted || driver.createCalls != 0 || driver.v1Running || !driver.finalRunning {
		t.Fatalf("resumed stopped final: journal=%+v err=%v driver=%+v", committed, err, driver)
	}
}

func TestTransferGatewayV2ResumesAfterAmbiguousFinalBindingWrite(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	restore := injectGatewayV2TransferPostInstallFailure(t, func(body string) bool {
		return strings.Contains(body, `"finalContainerId":"`+strings.Repeat("e", 64)+`"`)
	})
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("ambiguous final binding was treated as success")
	}
	restore()
	_, installed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || installed.Phase != gatewayPhaseTransferIntent || installed.Resources.FinalContainerID != strings.Repeat("e", 64) ||
		!driver.v1Running || !driver.finalExists || driver.finalRunning {
		t.Fatalf("post-bind interruption: journal=%+v err=%v driver=%+v", installed, err, driver)
	}
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("resume after binding: %v", err)
	}
	_, committed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || committed.Phase != gatewayPhaseCommitted || driver.createCalls != 1 {
		t.Fatalf("binding resume: journal=%+v err=%v creates=%d", committed, err, driver.createCalls)
	}
}

func TestTransferGatewayV2ResumesAfterAmbiguousTransferIntentWrite(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	restore := injectGatewayV2TransferPostInstallFailure(t, func(body string) bool {
		return strings.Contains(body, `"phase":"transfer_intent"`)
	})
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("ambiguous transfer intent was treated as success")
	}
	restore()
	_, installed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || installed.Phase != gatewayPhaseTransferIntent || !driver.v1Running || !driver.stageRunning || len(driver.finalConfig) == 0 {
		t.Fatalf("post-intent interruption: journal=%+v err=%v driver=%+v", installed, err, driver)
	}
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("resume after transfer intent: %v", err)
	}
	_, committed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || committed.Phase != gatewayPhaseCommitted {
		t.Fatalf("transfer-intent resume: journal=%+v err=%v", committed, err)
	}
}

func TestTransferGatewayV2ResumesFromInstalledV2Serving(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	restore := injectGatewayV2TransferPostInstallFailure(t, func(body string) bool {
		return strings.Contains(body, `"phase":"v2_serving"`)
	})
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("ambiguous v2-serving write was treated as success")
	}
	restore()
	_, installed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || installed.Phase != gatewayPhaseV2Serving || driver.v1Running || !driver.finalRunning {
		t.Fatalf("v2-serving interruption: journal=%+v err=%v driver=%+v", installed, err, driver)
	}
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("resume from v2-serving: %v", err)
	}
	_, committed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || committed.Phase != gatewayPhaseCommitted {
		t.Fatalf("v2-serving resume: journal=%+v err=%v", committed, err)
	}
}

func TestTransferGatewayV2ResumesFromInstalledRollbackIntent(t *testing.T) {
	manager, store, state, driver, _ := gatewayV2TransferTestFixture(t)
	driver.failSelectedPreflightAt = 4
	restore := injectGatewayV2TransferPostInstallFailure(t, func(body string) bool {
		return strings.Contains(body, `"phase":"rollback_intent"`)
	})
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("ambiguous rollback intent was treated as success")
	}
	restore()
	_, installed, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || installed.Phase != gatewayPhaseRollbackIntent || driver.v1Running || !driver.finalRunning {
		t.Fatalf("rollback-intent interruption: journal=%+v err=%v driver=%+v", installed, err, driver)
	}
	if err := manager.TransferGatewayV2(context.Background(), state.OperationID); err == nil {
		// The resumed operation returns the original unresolved migration error
		// after it has safely completed rollback.
		t.Fatal("resumed rollback unexpectedly reported transfer success")
	}
	_, rolledBack, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil || rolledBack.Phase != gatewayPhaseRolledBack || !driver.v1Running || driver.finalExists {
		t.Fatalf("rollback-intent resume: journal=%+v err=%v driver=%+v", rolledBack, err, driver)
	}
}

func TestClassifyGatewayV2TransferRecoveryAllowsExactV1ServingWindows(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseTransferIntent
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Resources.FinalContainerID = ""
	noContainers := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
	noContainers.StageContainer = caddyInspection{}
	noContainers.StageRuntime = gatewayContainerRuntime{}
	noContainers.StageContainerFound = false
	noContainers.StageConfig = nil
	noContainers.StageRestartConfig = nil
	noContainers.OwnedContainers = nil
	noContainers.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	if got := classifyGatewayV2RecoveryTopology(source, state, journal, noContainers); got != gatewayV2RecoveryTransferIntentV1ServingNoContainers {
		t.Fatalf("v1-serving no-containers = %q", got)
	}

	journal.Resources.FinalContainerID = strings.Repeat("d", 64)
	stoppedFinal := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	stoppedFinal.V1Container.Running = true
	stoppedFinal.V1Config = append([]byte(nil), stoppedFinal.V1RestartConfig...)
	stopGatewayV2TestContainer(&stoppedFinal.FinalContainer, &stoppedFinal.FinalRuntime, &stoppedFinal.FinalConfig, state, stoppedFinal.IngressNetworkID, stoppedFinal.ApplicationNetworkIDs)
	disconnectStoppedGatewayV2TestContainer(&stoppedFinal, stoppedFinal.FinalContainer.ID, state.Identity.FinalContainer)
	stoppedFinal.Final404Proven = false
	stoppedFinal.FinalRoutesProven = false
	stoppedFinal.FinalHostPublicationProven = false
	if got := classifyGatewayV2RecoveryTopology(source, state, journal, stoppedFinal); got != gatewayV2RecoveryTransferIntentV1ServingStoppedFinal {
		t.Fatalf("v1-serving stopped-final = %q", got)
	}
}

func TestGatewayV2FinalLoopbackRouteProofChecksEveryHost(t *testing.T) {
	_, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseCommitted
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	seen := make(map[string]int)
	probe := func(_ context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
		seen[host]++
		if address != "127.0.0.1" || port != journal.Source.LocalHostPort {
			return gatewayV2HostProbeResult{}
		}
		status := 204
		if host == "wrong.invalid" {
			status = 404
		}
		result := gatewayV2HostProbeResult{Status: status, Connected: true, Responded: true}
		if strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
			result.Status = 404
			result.Body = gatewayV2ChallengeBodyPrefix + strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
		} else if path != "/" {
			return gatewayV2HostProbeResult{}
		}
		return result
	}
	if !proveGatewayV2FinalLoopbackRoutes(context.Background(), state, journal, probe) {
		t.Fatal("exact loopback route matrix was rejected")
	}
	if seen["wrong.invalid"] != 1 || len(seen) != len(state.Apps)+1 {
		t.Fatalf("probed hosts = %v", seen)
	}
	failedHost := ""
	for appID := range state.Apps {
		host := appID + ".rig.localhost"
		if seen[host] != 2 {
			t.Fatalf("app host %q was probed %d times", host, seen[host])
		}
		failedHost = host
	}
	failed := func(_ context.Context, _ string, _ uint16, host, path string) gatewayV2HostProbeResult {
		if host == failedHost {
			return gatewayV2HostProbeResult{}
		}
		status := 204
		if host == "wrong.invalid" {
			status = 404
		}
		result := gatewayV2HostProbeResult{Status: status, Connected: true, Responded: true}
		if strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
			result.Status = 404
			result.Body = gatewayV2ChallengeBodyPrefix + strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
		}
		return result
	}
	if proveGatewayV2FinalLoopbackRoutes(context.Background(), state, journal, failed) {
		t.Fatal("missing app loopback route was accepted")
	}
}

func TestGatewayV2ProductionTransferRefusesReplacementV1Identity(t *testing.T) {
	runner := &gatewayV2ReplacementRunner{}
	manager := &Manager{runner: runner, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
	}}
	driver := managerGatewayV2TransferDriver{manager: manager}
	expected := "sha256:" + strings.Repeat("a", 64)
	if err := driver.setContainerRunning(context.Background(), caddyContainerName, expected, false); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("replacement error = %v", err)
	}
	if runner.mutationCalls != 0 {
		t.Fatalf("replacement v1 received %d mutations", runner.mutationCalls)
	}
}

type gatewayV2ReplacementRunner struct{ mutationCalls int }

func (r *gatewayV2ReplacementRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if len(request.Args) >= 2 && request.Args[0] == "container" && request.Args[1] == "inspect" {
		body := `{"id":"sha256:` + strings.Repeat("b", 64) + `","name":"/` + caddyContainerName + `","running":true,"restarting":false}`
		return runtimeprocess.CommandResult{Stdout: []byte(body)}, nil
	}
	r.mutationCalls++
	return runtimeprocess.CommandResult{}, nil
}

func gatewayV2TransferTestFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore, gatewayV2RouteState, *fakeGatewayV2TransferDriver, *bool) {
	t.Helper()
	manager, store, state, _ := gatewayV2StageTestFixture(t)
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatal(err)
	}
	driver := &fakeGatewayV2TransferDriver{t: t, manager: manager, v1Running: true, stageExists: true, stageRunning: true}
	manager.gatewayV2TransferDriver = driver
	originalAcquire := managerAcquireGatewayOSLock
	osLockHeld := false
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		osLockHeld = true
		return func() error { osLockHeld = false; return nil }, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
	driver.osLockHeld = &osLockHeld
	return manager, store, state, driver, &osLockHeld
}

type fakeGatewayV2TransferDriver struct {
	t          *testing.T
	manager    *Manager
	osLockHeld *bool

	v1Running               bool
	stageExists             bool
	stageRunning            bool
	finalExists             bool
	finalRunning            bool
	finalConfig             []byte
	v1ServingAtFinalCreate  bool
	failAt                  string
	failed                  bool
	failSelectedPreflightAt int

	selectedPreflightCalls int
	createCalls            int
	stopV1Calls            int
	startV1Calls           int
	removeFinalCalls       int
	events                 []string
}

func (d *fakeGatewayV2TransferDriver) checkLocked() {
	d.t.Helper()
	if d.manager.mu.TryLock() {
		d.manager.mu.Unlock()
		d.t.Error("gateway transfer driver ran without Manager lock")
	}
	if d.osLockHeld != nil && !*d.osLockHeld {
		d.t.Error("gateway transfer driver ran without OS lock")
	}
}

func (d *fakeGatewayV2TransferDriver) shouldFail(name string) bool {
	if !d.failed && d.failAt == name {
		d.failed = true
		return true
	}
	return false
}

func (d *fakeGatewayV2TransferDriver) observeTopology(_ context.Context, _ routeState, _ gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
	d.checkLocked()
	if d.v1Running && d.stageExists && d.stageRunning && !d.finalExists {
		return gatewayTopologyExactV1WithStage
	}
	if d.v1Running && !d.stageExists && !d.finalExists {
		return gatewayTopologyExactV1Only
	}
	if !d.v1Running && !d.stageExists && d.finalExists && d.finalRunning && len(d.finalConfig) != 0 {
		return gatewayTopologyExactFinalV2
	}
	return gatewayTopologyUnknownOrDrift
}

func (d *fakeGatewayV2TransferDriver) observeRecovery(_ context.Context, _ routeState, _ gatewayV2RouteState, journal gatewayMigrationJournal) gatewayV2RecoveryTopology {
	d.checkLocked()
	if journal.Phase != gatewayPhaseTransferIntent && journal.Phase != gatewayPhaseRollbackIntent {
		return gatewayV2RecoveryUnknown
	}
	if d.v1Running {
		switch {
		case d.stageExists && !d.stageRunning && !d.finalExists:
			return gatewayV2RecoveryTransferIntentV1ServingStoppedStage
		case !d.stageExists && !d.finalExists:
			return gatewayV2RecoveryTransferIntentV1ServingNoContainers
		case !d.stageExists && d.finalExists && !d.finalRunning && len(d.finalConfig) != 0 && journal.Resources.FinalContainerID == strings.Repeat("e", 64):
			return gatewayV2RecoveryTransferIntentV1ServingStoppedFinal
		}
		return gatewayV2RecoveryUnknown
	}
	switch {
	case d.stageExists && d.stageRunning && !d.finalExists:
		return gatewayV2RecoveryTransferIntentStoppedV1
	case d.stageExists && !d.stageRunning && !d.finalExists:
		return gatewayV2RecoveryTransferIntentV1StoppedStage
	case !d.stageExists && !d.finalExists:
		return gatewayV2RecoveryTransferIntentNoContainers
	case !d.stageExists && d.finalExists && !d.finalRunning && len(d.finalConfig) != 0 && journal.Resources.FinalContainerID == strings.Repeat("e", 64):
		return gatewayV2RecoveryTransferIntentStoppedFinal
	default:
		return gatewayV2RecoveryUnknown
	}
}

func (d *fakeGatewayV2TransferDriver) proveFinalHostRoutes(context.Context, gatewayV2RouteState, gatewayMigrationJournal) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_final_host_routes")
	return !d.v1Running && d.finalExists && d.finalRunning && len(d.finalConfig) != 0
}

func (d *fakeGatewayV2TransferDriver) selectedInterfacePreflight(gatewayProfileBinding) error {
	d.checkLocked()
	d.selectedPreflightCalls++
	d.events = append(d.events, "selected_preflight")
	if d.selectedPreflightCalls == d.failSelectedPreflightAt {
		return errors.New("selected interface drift")
	}
	return nil
}

func (d *fakeGatewayV2TransferDriver) copyFinalConfigToStage(_ context.Context, _ gatewayV2RouteState, _ gatewayMigrationJournal, contents []byte) error {
	d.checkLocked()
	d.events = append(d.events, "copy_final_config")
	d.finalConfig = append([]byte(nil), contents...)
	return nil
}

func (d *fakeGatewayV2TransferDriver) readFinalConfigFromStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) ([]byte, error) {
	d.checkLocked()
	d.events = append(d.events, "read_final_config")
	return append([]byte(nil), d.finalConfig...), nil
}

func (d *fakeGatewayV2TransferDriver) stopStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "stop_stage")
	d.stageRunning = false
	return nil
}

func (d *fakeGatewayV2TransferDriver) removeStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "remove_stage")
	d.stageExists = false
	return nil
}

func (d *fakeGatewayV2TransferDriver) createFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) (string, error) {
	d.checkLocked()
	d.events = append(d.events, "create_final")
	d.createCalls++
	d.v1ServingAtFinalCreate = d.v1Running
	d.finalExists = true
	if d.shouldFail("create_final_after_mutation") {
		return "", errors.New("final create uncertain")
	}
	return "sha256:" + strings.Repeat("e", 64), nil
}

func (d *fakeGatewayV2TransferDriver) stopV1(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "stop_v1")
	d.stopV1Calls++
	d.v1Running = false
	return nil
}

func (d *fakeGatewayV2TransferDriver) startV1(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "start_v1")
	d.startV1Calls++
	d.v1Running = true
	return nil
}

func (d *fakeGatewayV2TransferDriver) startFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "start_final")
	if d.shouldFail("start_final_after_mutation") {
		d.finalRunning = true
		return errors.New("final start reported failure")
	}
	if d.shouldFail("start_final") {
		return errors.New("final start failed")
	}
	d.finalRunning = true
	return nil
}

func (d *fakeGatewayV2TransferDriver) stopFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "stop_final")
	d.finalRunning = false
	return nil
}

func (d *fakeGatewayV2TransferDriver) removeFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "remove_final")
	d.removeFinalCalls++
	d.finalExists = false
	d.finalConfig = nil
	return nil
}

func gatewayV2TransferEventIndex(events []string, wanted string) int {
	for index, event := range events {
		if event == wanted {
			return index
		}
	}
	return -1
}

func injectGatewayV2TransferPostInstallFailure(t *testing.T, matches func(string) bool) func() {
	t.Helper()
	original := upgradeProtectedWrite
	failed := false
	upgradeProtectedWrite = func(path, purpose string, plaintext []byte) error {
		if err := original(path, purpose, plaintext); err != nil {
			return err
		}
		if !failed && purpose == gatewayMigrationPurpose && matches(string(plaintext)) {
			failed = true
			return errors.New("injected post-install transfer journal error")
		}
		return nil
	}
	restored := false
	restore := func() {
		if !restored {
			upgradeProtectedWrite = original
			restored = true
		}
	}
	t.Cleanup(restore)
	return restore
}

var _ gatewayV2TransferDriver = (*fakeGatewayV2TransferDriver)(nil)
