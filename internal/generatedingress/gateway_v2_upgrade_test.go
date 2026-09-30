package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayV2StartCaptureRunner struct{ args []string }

func (r *gatewayV2StartCaptureRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.args = append([]string(nil), request.Args...)
	return runtimeprocess.CommandResult{}, nil
}

func TestGatewayV2ProductionStageStartUsesPlainBoundContainerID(t *testing.T) {
	_, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseStageIntent
	journal.Resources.StageContainerID = strings.Repeat("d", 64)
	runner := &gatewayV2StartCaptureRunner{}
	driver := managerGatewayV2UpgradeDriver{manager: &Manager{runner: runner, options: Options{DockerExecutable: "docker"}}}
	if err := driver.startStage(context.Background(), state, journal); err != nil {
		t.Fatal(err)
	}
	want := []string{"container", "start", journal.Resources.StageContainerID}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("Docker args = %v, want %v", runner.args, want)
	}
}

func TestStageGatewayV2SuccessBindsExactResourcesAndRetainsV1(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
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
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatal(err)
	}
	if osLockHeld {
		t.Fatal("gateway OS lock remained held after stage")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseStaged || journal.Resources.ImageID != strings.Repeat("a", 64) ||
		journal.Resources.IngressNetworkID != strings.Repeat("b", 64) || journal.Resources.StageContainerID != strings.Repeat("d", 64) ||
		journal.Resources.ConfigVolume == (gatewayV2VolumeResourceBinding{}) || journal.Resources.DataVolume == (gatewayV2VolumeResourceBinding{}) {
		t.Fatalf("staged journal = %+v", journal)
	}
	if !driver.v1Running || !driver.stageRunning || driver.stageRemoved || driver.startCalls != 1 || driver.hostPreflightCalls != 2 || driver.selectedPreflightCalls != 1 {
		t.Fatalf("driver state = %+v", driver)
	}
	if len(driver.events) < 3 || strings.Join(driver.events[len(driver.events)-3:], ",") != "attest_stopped_stage,selected_preflight,start_stage" {
		t.Fatalf("pre-start order = %v", driver.events)
	}
	creates := driver.createCalls
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("idempotent stage: %v", err)
	}
	if driver.createCalls != creates {
		t.Fatalf("idempotent call created resources: before=%d after=%d", creates, driver.createCalls)
	}
}

func TestStageGatewayV2CreateBeforeBindFailureMarksUnboundResourceUncertain(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.failAt = "create_stage_after_mutation"
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("error = %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseUncertain || journal.Resources.StageContainerID != "" || !driver.stageExists || driver.stageRunning || !driver.v1Running {
		t.Fatalf("unbound create outcome: journal=%+v driver=%+v", journal, driver)
	}
	if driver.removeCalls != 0 || driver.startCalls != 0 {
		t.Fatalf("mutated unbound stage: remove=%d start=%d", driver.removeCalls, driver.startCalls)
	}
}

func TestStageGatewayV2CrashAfterBindingStoppedStageCompensatesWithoutStarting(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	operationID := state.OperationID
	if _, err := store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseStageIntent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.bindMigrationDockerResource(operationID, gatewayPhaseStageIntent, gatewayV2ResourceImage, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.bindMigrationDockerResource(operationID, gatewayPhaseStageIntent, gatewayV2ResourceIngressNetwork, "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	config := gatewayV1VolumeIdentity{Mountpoint: "/volumes/config", CreatedAt: "2026-09-29T00:01:00Z"}
	data := gatewayV1VolumeIdentity{Mountpoint: "/volumes/data", CreatedAt: "2026-09-29T00:02:00Z"}
	if _, err := store.bindMigrationVolumeResource(operationID, gatewayPhaseStageIntent, gatewayV2ResourceConfigVolume, config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.bindMigrationVolumeResource(operationID, gatewayPhaseStageIntent, gatewayV2ResourceDataVolume, data); err != nil {
		t.Fatal(err)
	}
	if _, err := store.bindMigrationDockerResource(operationID, gatewayPhaseStageIntent, gatewayV2ResourceStageContainer, "sha256:"+strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	driver.networkExists, driver.configExists, driver.dataExists, driver.stageExists = true, true, true, true
	if err := manager.StageGatewayV2(context.Background(), operationID); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("crash recovery error = %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(operationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || driver.stageExists || driver.stageRunning || !driver.v1Running || driver.startCalls != 0 || driver.removeCalls != 1 {
		t.Fatalf("preconfig compensation: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2ConfigCopyFailureRollsBackOnlyBoundStoppedStage(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.failAt = "copy_stage_config"
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("config-copy failure was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || journal.Resources.StageContainerID != strings.Repeat("d", 64) ||
		driver.stageExists || driver.stageRunning || !driver.v1Running || driver.removeCalls != 1 || driver.startCalls != 0 {
		t.Fatalf("config rollback outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2StartFailureStopsAndRollsBackBeforeClaimingCompletion(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.failAt = "start_stage"
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("start failure was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || driver.stageExists || driver.stageRunning || !driver.v1Running ||
		driver.stopCalls == 0 || driver.removeCalls != 1 {
		t.Fatalf("start rollback outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2RestartConfigMismatchNeverStartsListener(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.corruptRestartConfig = true
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("restart config mismatch was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || driver.startCalls != 0 || driver.stageExists || !driver.v1Running {
		t.Fatalf("restart mismatch outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2SelectedInterfaceFailureNeverStartsListener(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.failAt = "selected_preflight"
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err == nil {
		t.Fatal("selected-interface failure was accepted")
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseRolledBack || driver.startCalls != 0 || driver.stageExists || !driver.v1Running {
		t.Fatalf("interface failure outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2AttestedStartSurvivesCommandError(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.failAt = "start_stage_after_mutation"
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); err != nil {
		t.Fatalf("attested start: %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseStaged || !driver.stageRunning || !driver.v1Running {
		t.Fatalf("attested start outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2IdentityDriftFailsClosedWithoutDockerMutation(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.identityDrift = true
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("error = %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseUncertain || driver.createCalls != 0 || driver.startCalls != 0 || driver.removeCalls != 0 || !driver.v1Running {
		t.Fatalf("identity drift outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func TestStageGatewayV2RollbackUncertaintyNeverRecordsRolledBackWithStagePresent(t *testing.T) {
	manager, store, state, driver := gatewayV2StageTestFixture(t)
	driver.failAt = "copy_stage_config"
	driver.secondaryFailAt = "remove_stage"
	if err := manager.StageGatewayV2(context.Background(), state.OperationID); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("error = %v", err)
	}
	_, journal, err := store.loadBoundUpgrade(state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != gatewayPhaseUncertain || !driver.stageExists || driver.stageRunning || !driver.v1Running {
		t.Fatalf("uncertain rollback outcome: journal=%+v driver=%+v", journal, driver)
	}
}

func gatewayV2StageTestFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore, gatewayV2RouteState, *fakeGatewayV2UpgradeDriver) {
	t.Helper()
	manager, _ := newManagerFixture(t, false)
	source, input := upgradeTestPreparation(t)
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	driver := &fakeGatewayV2UpgradeDriver{t: t, manager: manager, v1Running: true}
	manager.gatewayV2UpgradeDriver = driver
	return manager, store, state, driver
}

type fakeGatewayV2UpgradeDriver struct {
	t          *testing.T
	manager    *Manager
	osLockHeld *bool

	v1Running            bool
	networkExists        bool
	configExists         bool
	dataExists           bool
	stageExists          bool
	stageRunning         bool
	stageRemoved         bool
	stageConfig          []byte
	identityDrift        bool
	corruptRestartConfig bool

	failAt          string
	secondaryFailAt string
	failed          map[string]bool

	createCalls            int
	startCalls             int
	stopCalls              int
	removeCalls            int
	hostPreflightCalls     int
	selectedPreflightCalls int
	events                 []string
}

func (d *fakeGatewayV2UpgradeDriver) checkLocked() {
	d.t.Helper()
	if d.manager.mu.TryLock() {
		d.manager.mu.Unlock()
		d.t.Error("gateway upgrade driver ran without Manager lock")
	}
	if d.osLockHeld != nil && !*d.osLockHeld {
		d.t.Error("gateway upgrade driver ran without OS lock")
	}
}

func (d *fakeGatewayV2UpgradeDriver) shouldFail(name string) bool {
	if d.failed == nil {
		d.failed = make(map[string]bool)
	}
	if d.failed[name] {
		return false
	}
	if d.failAt == name || d.secondaryFailAt == name {
		d.failed[name] = true
		return true
	}
	return false
}

func (d *fakeGatewayV2UpgradeDriver) observeTopology(_ context.Context, _ routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayObservedTopology {
	d.checkLocked()
	if d.identityDrift {
		return gatewayTopologyUnknownOrDrift
	}
	stageBound := journal.Resources.StageContainerID == strings.Repeat("d", 64)
	if d.v1Running && d.stageExists && d.stageRunning && stageBound && d.exactStageConfig(state) && d.fullInfrastructure(journal) {
		return gatewayTopologyExactV1WithStage
	}
	if d.v1Running && !d.stageExists && ((!d.networkExists && !d.configExists && !d.dataExists) || d.fullInfrastructure(journal)) {
		return gatewayTopologyExactV1Only
	}
	return gatewayTopologyUnknownOrDrift
}

func (d *fakeGatewayV2UpgradeDriver) observeRecovery(_ context.Context, _ routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayV2RecoveryTopology {
	d.checkLocked()
	if d.identityDrift || journal.Phase != gatewayPhaseStageIntent {
		return gatewayV2RecoveryUnknown
	}
	if d.stageExists {
		if journal.Resources.StageContainerID == strings.Repeat("d", 64) && !d.stageRunning && d.exactStageConfig(state) && d.fullInfrastructure(journal) {
			return gatewayV2RecoveryStageIntentStoppedStage
		}
		return gatewayV2RecoveryUnknown
	}
	if d.resourcePresenceMatchesBindings(journal) {
		return gatewayV2RecoveryStageIntentPartialInfrastructure
	}
	return gatewayV2RecoveryUnknown
}

func (d *fakeGatewayV2UpgradeDriver) hostPreflight(_ context.Context, _ gatewayProfileBinding, _ gatewayV2NetworkPlan) error {
	d.checkLocked()
	d.hostPreflightCalls++
	if d.shouldFail("host_preflight") {
		return errors.New("host preflight failed")
	}
	return nil
}

func (d *fakeGatewayV2UpgradeDriver) selectedInterfacePreflight(gatewayProfileBinding) error {
	d.checkLocked()
	d.selectedPreflightCalls++
	d.events = append(d.events, "selected_preflight")
	if d.shouldFail("selected_preflight") {
		return errors.New("selected interface preflight failed")
	}
	return nil
}

func (d *fakeGatewayV2UpgradeDriver) pinnedImage(context.Context) (string, error) {
	d.checkLocked()
	return "sha256:" + strings.Repeat("a", 64), nil
}

func (d *fakeGatewayV2UpgradeDriver) createIngressNetwork(context.Context, gatewayV2RouteState, gatewayMigrationJournal) (string, error) {
	d.checkLocked()
	d.createCalls++
	d.networkExists = true
	if d.shouldFail("create_network_after_mutation") {
		return "", errors.New("network create uncertain")
	}
	return "sha256:" + strings.Repeat("b", 64), nil
}

func (d *fakeGatewayV2UpgradeDriver) createVolume(_ context.Context, _ gatewayV2RouteState, _ gatewayMigrationJournal, role string) (gatewayV1VolumeIdentity, error) {
	d.checkLocked()
	d.createCalls++
	if role == gatewayV2ConfigVolumeRole {
		d.configExists = true
		return gatewayV1VolumeIdentity{Mountpoint: "/volumes/config", CreatedAt: "2026-09-29T00:01:00Z"}, nil
	}
	d.dataExists = true
	return gatewayV1VolumeIdentity{Mountpoint: "/volumes/data", CreatedAt: "2026-09-29T00:02:00Z"}, nil
}

func (d *fakeGatewayV2UpgradeDriver) createStageContainer(context.Context, gatewayV2RouteState, gatewayMigrationJournal) (string, error) {
	d.checkLocked()
	d.createCalls++
	d.stageExists = true
	d.stageRunning = false
	if d.shouldFail("create_stage_after_mutation") {
		return "", errors.New("stage create uncertain")
	}
	return "sha256:" + strings.Repeat("d", 64), nil
}

func (d *fakeGatewayV2UpgradeDriver) copyStageConfig(_ context.Context, _ gatewayV2RouteState, _ gatewayMigrationJournal, contents []byte) error {
	d.checkLocked()
	if d.shouldFail("copy_stage_config") {
		return errors.New("stage config copy failed")
	}
	d.stageConfig = append([]byte(nil), contents...)
	return nil
}

func (d *fakeGatewayV2UpgradeDriver) readStageRestartConfig(context.Context, gatewayV2RouteState, gatewayMigrationJournal) ([]byte, error) {
	d.checkLocked()
	if d.corruptRestartConfig {
		return []byte(`{"admin":{"listen":"localhost:2019"}}`), nil
	}
	return append([]byte(nil), d.stageConfig...), nil
}

func (d *fakeGatewayV2UpgradeDriver) attestStoppedStage(_ context.Context, _ routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	d.checkLocked()
	d.events = append(d.events, "attest_stopped_stage")
	return !d.identityDrift && d.stageExists && !d.stageRunning && journal.Resources.StageContainerID == strings.Repeat("d", 64) &&
		d.exactStageConfig(state) && d.fullInfrastructure(journal)
}

func (d *fakeGatewayV2UpgradeDriver) attestStoppedStageForCompensation(_ context.Context, _ routeState, _ gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	d.checkLocked()
	return !d.identityDrift && d.v1Running && d.stageExists && !d.stageRunning &&
		journal.Resources.StageContainerID == strings.Repeat("d", 64) && d.fullInfrastructure(journal)
}

func (d *fakeGatewayV2UpgradeDriver) startStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.startCalls++
	d.events = append(d.events, "start_stage")
	if d.shouldFail("start_stage_after_mutation") {
		d.stageRunning = true
		return errors.New("stage start reported failure")
	}
	if d.shouldFail("start_stage") {
		return errors.New("stage start failed")
	}
	d.stageRunning = true
	return nil
}

func (d *fakeGatewayV2UpgradeDriver) stopStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.stopCalls++
	if d.shouldFail("stop_stage") {
		return errors.New("stage stop failed")
	}
	d.stageRunning = false
	return nil
}

func (d *fakeGatewayV2UpgradeDriver) removeStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error {
	d.checkLocked()
	d.removeCalls++
	if d.shouldFail("remove_stage") {
		return errors.New("stage remove failed")
	}
	d.stageExists = false
	d.stageRemoved = true
	d.stageConfig = nil
	return nil
}

func (d *fakeGatewayV2UpgradeDriver) fullInfrastructure(journal gatewayMigrationJournal) bool {
	return d.networkExists && d.configExists && d.dataExists && journal.Resources.ImageID == strings.Repeat("a", 64) &&
		journal.Resources.IngressNetworkID == strings.Repeat("b", 64) &&
		journal.Resources.ConfigVolume != (gatewayV2VolumeResourceBinding{}) && journal.Resources.DataVolume != (gatewayV2VolumeResourceBinding{})
}

func (d *fakeGatewayV2UpgradeDriver) resourcePresenceMatchesBindings(journal gatewayMigrationJournal) bool {
	_, configBound := validGatewayV2VolumeResourceBinding(journal.Resources.ConfigVolume)
	_, dataBound := validGatewayV2VolumeResourceBinding(journal.Resources.DataVolume)
	return d.networkExists == (journal.Resources.IngressNetworkID != "") && d.configExists == configBound && d.dataExists == dataBound &&
		journal.Resources.StageContainerID == ""
}

func (d *fakeGatewayV2UpgradeDriver) exactStageConfig(state gatewayV2RouteState) bool {
	expected, err := buildGatewayV2StageConfig(state)
	return err == nil && sameCaddyConfig(expected, d.stageConfig)
}

var _ gatewayV2UpgradeDriver = (*fakeGatewayV2UpgradeDriver)(nil)
