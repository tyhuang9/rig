package generatedingress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestUpgradeGatewayV2CoordinatesFreshMigrationUnderOneLock(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, transferDriver, osLockHeld := gatewayV2CoordinatorTestFixture(t)

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("upgrade result = %+v, err=%v", result, err)
	}
	if *osLockHeld {
		t.Fatal("gateway OS lock remained held")
	}
	_, journal, err := store.loadBoundUpgrade(request.OperationID)
	if err != nil || journal.Phase != gatewayPhaseCommitted {
		t.Fatalf("committed journal = %+v, err=%v", journal, err)
	}
	if sourceDriver.calls != 1 || sourceDriver.selectCalls != 1 || stageDriver.hostPreflightCalls != 3 || !transferDriver.v1ServingAtFinalCreate ||
		transferDriver.v1Running || !transferDriver.finalRunning {
		t.Fatalf("coordinator drivers: source=%+v stage=%+v transfer=%+v", sourceDriver, stageDriver, transferDriver)
	}
	stageCreates, finalCreates := stageDriver.createCalls, transferDriver.createCalls
	result, err = manager.UpgradeGatewayV2(context.Background(), request)
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted || sourceDriver.calls != 1 || sourceDriver.selectCalls != 1 ||
		stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
		t.Fatalf("committed replay: result=%+v err=%v source=%d stageCreates=%d finalCreates=%d", result, err, sourceDriver.calls, stageDriver.createCalls, transferDriver.createCalls)
	}
}

func TestUpgradeGatewayV2RejectsEveryMismatchedResumeBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GatewayV2UpgradeRequest)
	}{
		{name: "operation", mutate: func(value *GatewayV2UpgradeRequest) { value.OperationID = "66666666-6666-4666-8666-666666666666" }},
		{name: "actor", mutate: func(value *GatewayV2UpgradeRequest) { value.ApprovedBy = "77777777-7777-4777-8777-777777777777" }},
		{name: "profile revision", mutate: func(value *GatewayV2UpgradeRequest) {
			value.Profile.RevisionNumber++
			value.ApprovedActionDigest = mustGatewayV2CoordinatorActionDigest(t, value.Profile)
		}},
		{name: "action digest", mutate: func(value *GatewayV2UpgradeRequest) { value.ApprovedActionDigest = strings.Repeat("f", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, _, request, _, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
			if result, err := manager.UpgradeGatewayV2(context.Background(), request); err != nil || result.Outcome != GatewayV2UpgradeCommitted {
				t.Fatalf("initial upgrade: result=%+v err=%v", result, err)
			}
			stageCreates, finalCreates := stageDriver.createCalls, transferDriver.createCalls
			test.mutate(&request)
			result, err := manager.UpgradeGatewayV2(context.Background(), request)
			if err == nil || result.Outcome != GatewayV2UpgradeUnresolved || stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
				t.Fatalf("mismatched resume: result=%+v err=%v stageCreates=%d finalCreates=%d", result, err, stageDriver.createCalls, transferDriver.createCalls)
			}
		})
	}
}

func TestUpgradeGatewayV2FailedNetworkSelectionWritesNoProtectedState(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	sourceDriver.selectErr = errors.New("injected incomplete Docker inventory")

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeUnresolved {
		t.Fatalf("selection failure result=%+v err=%v", result, err)
	}
	if sourceDriver.selectCalls != 1 || sourceDriver.calls != 0 || stageDriver.createCalls != 0 || transferDriver.createCalls != 0 {
		t.Fatalf("drivers after selection failure: source=%+v stage=%+v transfer=%+v", sourceDriver, stageDriver, transferDriver)
	}
	if _, stateErr := store.loadV2State(); stateErr == nil {
		t.Fatal("v2 state was written after failed network selection")
	}
	if _, journalErr := store.loadMigrationJournal(); journalErr == nil {
		t.Fatal("migration journal was written after failed network selection")
	}
}

func TestUpgradeGatewayV2ReplayReusesJournalBoundNetworkWithoutReselection(t *testing.T) {
	manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	if result, err := manager.UpgradeGatewayV2(context.Background(), request); err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("initial upgrade: result=%+v err=%v", result, err)
	}
	state, _, err := store.loadBoundUpgrade(request.OperationID)
	if err != nil || state.Network != sourceDriver.plan {
		t.Fatalf("bound network=%+v err=%v, want %+v", state.Network, err, sourceDriver.plan)
	}
	sourceDriver.plan = gatewayV2NetworkPlan{Subnet: "10.241.0.0/28", GatewayIPv4: "10.241.0.1", ContainerIPv4: "10.241.0.2"}
	sourceDriver.selectErr = errors.New("planner must not run during replay")
	if result, err := manager.UpgradeGatewayV2(context.Background(), request); err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("replay result=%+v err=%v", result, err)
	}
	if sourceDriver.selectCalls != 1 {
		t.Fatalf("network selections=%d, want 1", sourceDriver.selectCalls)
	}
	replayed, _, err := store.loadBoundUpgrade(request.OperationID)
	if err != nil || replayed.Network != state.Network {
		t.Fatalf("replayed network=%+v err=%v, want %+v", replayed.Network, err, state.Network)
	}
}

func TestUpgradeGatewayV2NeverReplacesRolledBackOperation(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	stageDriver.failAt = "selected_preflight"

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		t.Fatalf("rolled-back result = %+v, err=%v", result, err)
	}
	_, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseRolledBack || transferDriver.createCalls != 0 {
		t.Fatalf("rolled-back journal=%+v loadErr=%v transferCreates=%d", journal, loadErr, transferDriver.createCalls)
	}
	stageDriver.failAt = ""
	creates, sourceCalls := stageDriver.createCalls, sourceDriver.calls
	result, err = manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack || stageDriver.createCalls != creates || sourceDriver.calls != sourceCalls {
		t.Fatalf("rolled-back replay: result=%+v err=%v creates=%d sourceCalls=%d", result, err, stageDriver.createCalls, sourceDriver.calls)
	}
}

func TestUpgradeGatewayV2ReportsDriftedRolledBackTopologyAsUnresolved(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	sourceDriver.rollbackTopology = gatewayTopologyUnknownOrDrift
	stageDriver.failAt = "selected_preflight"

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeUnresolved {
		t.Fatalf("drifted rollback result = %+v, err=%v", result, err)
	}
	_, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseRolledBack || transferDriver.createCalls != 0 {
		t.Fatalf("drifted rollback journal=%+v loadErr=%v transferCreates=%d", journal, loadErr, transferDriver.createCalls)
	}
	result, err = manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeUnresolved {
		t.Fatalf("drifted rollback replay = %+v, err=%v", result, err)
	}
	sourceDriver.rollbackTopology = gatewayTopologyExactV1Only
	result, err = manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		t.Fatalf("reattested rollback replay = %+v, err=%v", result, err)
	}
}

func TestUpgradeGatewayV2RejectsChangedManagerHostPortOnResume(t *testing.T) {
	manager, _, request, _, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	if result, err := manager.UpgradeGatewayV2(context.Background(), request); err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("initial upgrade: result=%+v err=%v", result, err)
	}
	stageCreates, finalCreates := stageDriver.createCalls, transferDriver.createCalls
	manager.options.HostPort++
	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeUnresolved || stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
		t.Fatalf("changed host port replay: result=%+v err=%v stageCreates=%d finalCreates=%d", result, err, stageDriver.createCalls, transferDriver.createCalls)
	}
}

func TestUpgradeGatewayV2MarksUnknownStageTopologyUnresolved(t *testing.T) {
	manager, store, request, _, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	stageDriver.identityDrift = true

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved {
		t.Fatalf("unknown topology result = %+v, err=%v", result, err)
	}
	_, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseUncertain || transferDriver.createCalls != 0 {
		t.Fatalf("uncertain journal=%+v loadErr=%v transferCreates=%d", journal, loadErr, transferDriver.createCalls)
	}
}

func TestUpgradeGatewayV2DoesNotMaskProtectedPreparationFailure(t *testing.T) {
	for _, purpose := range []string{v2RouteStatePurpose, gatewayMigrationPurpose} {
		t.Run(purpose, func(t *testing.T) {
			manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
			original := upgradeProtectedWriteNew
			failed := false
			upgradeProtectedWriteNew = func(path, gotPurpose string, plaintext []byte) error {
				err := original(path, gotPurpose, plaintext)
				if err == nil && !failed && gotPurpose == purpose {
					failed = true
					return errors.New("injected post-install preparation failure")
				}
				return err
			}
			restored := false
			restore := func() {
				if !restored {
					upgradeProtectedWriteNew = original
					restored = true
				}
			}
			t.Cleanup(restore)

			result, err := manager.UpgradeGatewayV2(context.Background(), request)
			if err == nil || result.Outcome != GatewayV2UpgradeUnresolved {
				t.Fatalf("post-install failure result = %+v, err=%v", result, err)
			}
			if _, stateErr := store.loadV2State(); stateErr != nil {
				t.Fatalf("installed state missing after injected failure: %v", stateErr)
			}
			restore()
			result, err = manager.UpgradeGatewayV2(context.Background(), request)
			if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
				t.Fatalf("fresh retry result = %+v, err=%v", result, err)
			}
			wantSourceCalls := 1
			if purpose == v2RouteStatePurpose {
				wantSourceCalls = 2
			}
			if sourceDriver.calls != wantSourceCalls {
				t.Fatalf("source attestations = %d, want %d", sourceDriver.calls, wantSourceCalls)
			}
		})
	}
}

func TestUpgradeGatewayV2RequiresFreshSuccessAfterFinalAttestationFailure(t *testing.T) {
	manager, store, request, _, _, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	wrapper := &gatewayV2CoordinatorFinalProofDriver{fakeGatewayV2TransferDriver: transferDriver, failAt: 4}
	manager.gatewayV2TransferDriver = wrapper

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeUnresolved {
		t.Fatalf("final proof failure result = %+v, err=%v", result, err)
	}
	_, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseCommitted {
		t.Fatalf("post-proof journal = %+v, err=%v", journal, loadErr)
	}
	result, err = manager.UpgradeGatewayV2(context.Background(), request)
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("final proof retry result = %+v, err=%v", result, err)
	}
}

func TestUpgradeGatewayV2DoesNotReportCommittedWhenGatewayLockReleaseFails(t *testing.T) {
	manager, store, request, _, _, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if *osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		*osLockHeld = true
		return func() error {
			*osLockHeld = false
			return errors.New("injected gateway lock release failure")
		}, nil
	}

	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || *osLockHeld {
		t.Fatalf("release failure result = %+v, err=%v lockHeld=%t", result, err, *osLockHeld)
	}
	_, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseCommitted {
		t.Fatalf("durable outcome after release failure: journal=%+v err=%v", journal, loadErr)
	}
}

func TestUpgradeGatewayV2DoesNotReportRolledBackWhenGatewayLockReleaseFails(t *testing.T) {
	manager, store, request, _, stageDriver, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	stageDriver.failAt = "selected_preflight"
	result, err := manager.UpgradeGatewayV2(context.Background(), request)
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		t.Fatalf("initial rollback result=%+v error=%v", result, err)
	}
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if *osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		*osLockHeld = true
		return func() error {
			*osLockHeld = false
			return errors.New("injected gateway lock release failure")
		}, nil
	}
	result, err = manager.UpgradeGatewayV2(context.Background(), request)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || *osLockHeld {
		t.Fatalf("rollback release failure result=%+v error=%v lockHeld=%t", result, err, *osLockHeld)
	}
	_, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseRolledBack {
		t.Fatalf("durable rollback after release failure: journal=%+v error=%v", journal, loadErr)
	}
}

func TestGatewayUpgradeActionDigestUsesAppAccessCanonicalContract(t *testing.T) {
	_, input := upgradeTestPreparation(t)
	want, err := appaccess.GatewayProfileUpgradeSpecDigest(appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID: input.Profile.RevisionID, ProfileRevisionNumber: input.Profile.RevisionNumber, ProfileSpecDigest: input.Profile.SpecDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := gatewayUpgradeActionDigest(input.Profile, gatewayV2IdentityVersion)
	if err != nil || got != want {
		t.Fatalf("generated-ingress digest=%q err=%v, appaccess digest=%q", got, err, want)
	}
}

type fakeGatewayV2CoordinatorDriver struct {
	t           *testing.T
	manager     *Manager
	osLockHeld  *bool
	digest      string
	calls       int
	plan        gatewayV2NetworkPlan
	selectErr   error
	selectCalls int

	rollbackTopology gatewayObservedTopology
}

func (d *fakeGatewayV2CoordinatorDriver) selectNetworkPlan(context.Context, gatewayProfileBinding) (gatewayV2NetworkPlan, error) {
	d.t.Helper()
	d.selectCalls++
	if d.manager.mu.TryLock() {
		d.manager.mu.Unlock()
		d.t.Error("network selection ran without Manager lock")
	}
	if d.osLockHeld == nil || !*d.osLockHeld {
		d.t.Error("network selection ran without OS lock")
	}
	return d.plan, d.selectErr
}

func (d *fakeGatewayV2CoordinatorDriver) attestSourceV1(context.Context, routeState, gatewayUpgradePreparation) (string, error) {
	d.t.Helper()
	d.calls++
	if d.manager.mu.TryLock() {
		d.manager.mu.Unlock()
		d.t.Error("source attestation ran without Manager lock")
	}
	if d.osLockHeld == nil || !*d.osLockHeld {
		d.t.Error("source attestation ran without OS lock")
	}
	return d.digest, nil
}

type gatewayV2CoordinatorFinalProofDriver struct {
	*fakeGatewayV2TransferDriver
	proofCalls int
	failAt     int
}

func (d *gatewayV2CoordinatorFinalProofDriver) proveFinalHostRoutes(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	d.proofCalls++
	if d.proofCalls == d.failAt {
		d.checkLocked()
		return false
	}
	return d.fakeGatewayV2TransferDriver.proveFinalHostRoutes(ctx, state, journal)
}

func gatewayV2CoordinatorTestFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore, GatewayV2UpgradeRequest, *fakeGatewayV2CoordinatorDriver, *fakeGatewayV2UpgradeDriver, *fakeGatewayV2TransferDriver, *bool) {
	t.Helper()
	manager, _ := newManagerFixture(t, false)
	source, preparation := upgradeTestPreparation(t)
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	request := GatewayV2UpgradeRequest{
		OperationID: preparation.OperationID,
		Profile: GatewayV2ProfileBinding{
			RevisionID: preparation.Profile.RevisionID, RevisionNumber: preparation.Profile.RevisionNumber,
			SpecDigest: preparation.Profile.SpecDigest, SelectedIPv4: preparation.Profile.SelectedIPv4,
			InterfaceID: preparation.Profile.InterfaceID, PortStart: preparation.Profile.PortStart, PortEnd: preparation.Profile.PortEnd,
		},
		ApprovedBy: preparation.ApprovedBy, ApprovedActionDigest: preparation.ApprovedActionDigest,
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	osLockHeld := false
	originalAcquire := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		osLockHeld = true
		return func() error { osLockHeld = false; return nil }, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
	sourceDriver := &fakeGatewayV2CoordinatorDriver{
		t: t, manager: manager, osLockHeld: &osLockHeld, digest: preparation.SourceIdentityDigest,
		plan:             preparation.Network,
		rollbackTopology: gatewayTopologyExactV1Only,
	}
	stageDriver := &fakeGatewayV2UpgradeDriver{t: t, manager: manager, osLockHeld: &osLockHeld, v1Running: true}
	transferDriver := &fakeGatewayV2TransferDriver{
		t: t, manager: manager, osLockHeld: &osLockHeld, v1Running: true, stageExists: true, stageRunning: true,
	}
	manager.gatewayV2CoordinatorDriver = sourceDriver
	manager.gatewayV2UpgradeDriver = stageDriver
	manager.gatewayV2TransferDriver = transferDriver
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology {
		sourceDriver.t.Helper()
		if sourceDriver.manager.mu.TryLock() {
			sourceDriver.manager.mu.Unlock()
			sourceDriver.t.Error("rollback attestation ran without Manager lock")
		}
		if sourceDriver.osLockHeld == nil || !*sourceDriver.osLockHeld {
			sourceDriver.t.Error("rollback attestation ran without OS lock")
		}
		return sourceDriver.rollbackTopology
	}
	return manager, store, request, sourceDriver, stageDriver, transferDriver, &osLockHeld
}

func mustGatewayV2CoordinatorActionDigest(t *testing.T, profile GatewayV2ProfileBinding) string {
	t.Helper()
	digest, err := appaccess.GatewayProfileUpgradeSpecDigest(appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.RevisionID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

var _ gatewayV2CoordinatorDriver = (*fakeGatewayV2CoordinatorDriver)(nil)
var _ gatewayV2TransferDriver = (*gatewayV2CoordinatorFinalProofDriver)(nil)
