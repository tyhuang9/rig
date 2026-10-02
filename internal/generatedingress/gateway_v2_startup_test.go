package generatedingress

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2StartupDispositionTable(t *testing.T) {
	tests := []struct {
		name          string
		history       string
		claimState    appaccess.GatewayProfileUpgradeState
		claimSequence int64
		omitClaim     bool
		mismatch      bool
		want          GatewayV2StartupDisposition
		wantError     bool
	}{
		{name: "empty v1", history: "none", omitClaim: true, want: GatewayV2StartupNormalV1},
		{name: "prepared before any artifact", history: "none", claimState: appaccess.GatewayProfileUpgradePrepared, claimSequence: 1, want: GatewayV2StartupRecoveryOnly},
		{name: "unresolved before any artifact", history: "none", claimState: appaccess.GatewayProfileUpgradeUnresolved, claimSequence: 2, want: GatewayV2StartupRecoveryOnly},
		{name: "state only", history: "state_only", claimState: appaccess.GatewayProfileUpgradePrepared, claimSequence: 1, want: GatewayV2StartupRecoveryOnly},
		{name: "prepared journal", history: "prepared", claimState: appaccess.GatewayProfileUpgradeServing, claimSequence: 2, want: GatewayV2StartupRecoveryOnly},
		{name: "unretired rollback", history: "rolled_back", claimState: appaccess.GatewayProfileUpgradeUnresolved, claimSequence: 2, want: GatewayV2StartupRecoveryOnly},
		{name: "retired rollback with stale claim", history: "retired", claimState: appaccess.GatewayProfileUpgradeUnresolved, claimSequence: 2, want: GatewayV2StartupRecoveryOnly},
		{name: "retired rollback", history: "retired", claimState: appaccess.GatewayProfileUpgradeRolledBack, claimSequence: 2, want: GatewayV2StartupNormalV1},
		{name: "pre-journal receipt", history: "aborted", claimState: appaccess.GatewayProfileUpgradeRolledBack, claimSequence: 2, want: GatewayV2StartupNormalV1},
		{name: "committed with stale serving claim", history: "committed", claimState: appaccess.GatewayProfileUpgradeServing, claimSequence: 2, want: GatewayV2StartupRecoveryOnly},
		{name: "committed", history: "committed", claimState: appaccess.GatewayProfileUpgradeCommitted, claimSequence: 2, want: GatewayV2StartupNormalV2},
		{name: "claim binding mismatch", history: "prepared", claimState: appaccess.GatewayProfileUpgradePrepared, claimSequence: 1, mismatch: true, wantError: true},
		{name: "committed database ahead", history: "none", claimState: appaccess.GatewayProfileUpgradeCommitted, claimSequence: 2, wantError: true},
		{name: "serving requires history", history: "none", claimState: appaccess.GatewayProfileUpgradeServing, claimSequence: 2, wantError: true},
		{name: "rolled back database ahead", history: "rolled_back", claimState: appaccess.GatewayProfileUpgradeRolledBack, claimSequence: 2, wantError: true},
		{name: "protected history requires claim", history: "prepared", omitClaim: true, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, request, sourceDriver, _, transferDriver, osLockHeld := gatewayV2CoordinatorTestFixture(t)
			switch test.history {
			case "none":
			case "state_only":
				installGatewayV2PreparationAbortStateOnly(t, manager, store, request, sourceDriver)
			case "prepared":
				installGatewayV2StartupPrepared(t, manager, store, request, sourceDriver)
			case "rolled_back":
				installRolledBackGeneration(t, manager, 0, request.OperationID)
			case "retired":
				retiredStore, state, journal := installRolledBackGeneration(t, manager, 0, request.OperationID)
				if _, err := retiredStore.installRollbackRetirementReceipt(state, journal); err != nil {
					t.Fatal(err)
				}
			case "aborted":
				abortStore, _, _, receipt := preparePreJournalAbortGeneration(t, manager, 0, request.OperationID, false)
				if _, err := abortStore.installPreJournalAbortReceipt(receipt); err != nil {
					t.Fatal(err)
				}
			case "committed":
				installCommittedGeneration(t, manager, store, request.OperationID)
				transferDriver.v1Running = false
				transferDriver.stageExists = false
				transferDriver.stageRunning = false
				transferDriver.finalExists = true
				transferDriver.finalRunning = true
				transferDriver.finalConfig = []byte("attested startup config")
			default:
				t.Fatalf("unknown history fixture %q", test.history)
			}

			claims := []GatewayV2StartupClaim(nil)
			if !test.omitClaim {
				candidate := request
				if test.mismatch {
					candidate.ApprovedBy = "77777777-7777-4777-8777-777777777777"
				}
				claims = []GatewayV2StartupClaim{{
					Request: candidate, State: test.claimState, StateSequence: test.claimSequence,
				}}
			}

			got, err := manager.InspectGatewayV2Startup(context.Background(), claims)
			if test.wantError {
				if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
					t.Fatalf("inspection=%+v err=%v", got, err)
				}
			} else {
				if err != nil || got.Disposition != test.want {
					t.Fatalf("inspection=%+v err=%v, want disposition %q", got, err, test.want)
				}
				wantOperationID := ""
				if test.want != GatewayV2StartupNormalV1 {
					wantOperationID = request.OperationID
				}
				if got.OperationID != wantOperationID {
					t.Fatalf("operation=%q, want %q", got.OperationID, wantOperationID)
				}
			}
			if *osLockHeld {
				t.Fatal("startup inspection retained the OS gateway lock")
			}
		})
	}
}

func TestGatewayV2StartupProofFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		history string
		state   appaccess.GatewayProfileUpgradeState
		fail    func(*fakeGatewayV2CoordinatorDriver, *fakeGatewayV2TransferDriver)
	}{
		{
			name: "v1 and absence proof", history: "none", state: appaccess.GatewayProfileUpgradePrepared,
			fail: func(source *fakeGatewayV2CoordinatorDriver, _ *fakeGatewayV2TransferDriver) {
				source.abortErr = errors.New("v1 drift")
			},
		},
		{
			name: "retirement replay proof", history: "retired", state: appaccess.GatewayProfileUpgradeRolledBack,
			fail: func(source *fakeGatewayV2CoordinatorDriver, _ *fakeGatewayV2TransferDriver) {
				source.retirementObservation.IngressNetworkPresent = true
			},
		},
		{
			name: "final topology proof", history: "committed", state: appaccess.GatewayProfileUpgradeCommitted,
			fail: func(_ *fakeGatewayV2CoordinatorDriver, transfer *fakeGatewayV2TransferDriver) {
				transfer.finalRunning = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, request, sourceDriver, _, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
			switch test.history {
			case "none":
			case "retired":
				retiredStore, state, journal := installRolledBackGeneration(t, manager, 0, request.OperationID)
				if _, err := retiredStore.installRollbackRetirementReceipt(state, journal); err != nil {
					t.Fatal(err)
				}
			case "committed":
				installCommittedGeneration(t, manager, store, request.OperationID)
				transferDriver.v1Running = false
				transferDriver.stageExists = false
				transferDriver.stageRunning = false
				transferDriver.finalExists = true
				transferDriver.finalRunning = true
				transferDriver.finalConfig = []byte("attested startup config")
			}
			test.fail(sourceDriver, transferDriver)
			sequence := int64(2)
			if test.state == appaccess.GatewayProfileUpgradePrepared {
				sequence = 1
			}
			got, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{{
				Request: request, State: test.state, StateSequence: sequence,
			}})
			if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
				t.Fatalf("inspection=%+v err=%v", got, err)
			}
		})
	}
}

func TestGatewayV2StartupRejectsInvalidClaimSets(t *testing.T) {
	manager, _, request, _, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	otherRequest := request
	otherRequest.OperationID = "77777777-7777-4777-8777-777777777777"
	tests := []struct {
		name   string
		claims []GatewayV2StartupClaim
	}{
		{name: "zero sequence", claims: []GatewayV2StartupClaim{{Request: request, State: appaccess.GatewayProfileUpgradePrepared}}},
		{name: "prepared sequence advanced", claims: []GatewayV2StartupClaim{{Request: request, State: appaccess.GatewayProfileUpgradePrepared, StateSequence: 2}}},
		{name: "terminal sequence missing transition", claims: []GatewayV2StartupClaim{{Request: request, State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 1}}},
		{name: "unknown state", claims: []GatewayV2StartupClaim{{Request: request, State: "future", StateSequence: 1}}},
		{name: "duplicate", claims: []GatewayV2StartupClaim{
			{Request: request, State: appaccess.GatewayProfileUpgradeRolledBack, StateSequence: 2},
			{Request: request, State: appaccess.GatewayProfileUpgradeRolledBack, StateSequence: 2},
		}},
		{name: "competing active claims", claims: []GatewayV2StartupClaim{
			{Request: request, State: appaccess.GatewayProfileUpgradePrepared, StateSequence: 1},
			{Request: otherRequest, State: appaccess.GatewayProfileUpgradeUnresolved, StateSequence: 2},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := manager.InspectGatewayV2Startup(context.Background(), test.claims)
			if !IsCode(err, DiagnosticValidationFailed) || got != (GatewayV2StartupInspection{}) {
				t.Fatalf("inspection=%+v err=%v", got, err)
			}
		})
	}
}

func TestGatewayV2StartupMatchesEveryHistoricalClaim(t *testing.T) {
	manager, _, request0, _, _, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	store0, state0, journal0 := installRolledBackGeneration(t, manager, 0, request0.OperationID)
	if _, err := store0.installRollbackRetirementReceipt(state0, journal0); err != nil {
		t.Fatal(err)
	}

	request1 := request0
	request1.OperationID = "77777777-7777-4777-8777-777777777777"
	next, err := manager.resolveGatewayUpgradeGenerationLocked(request1.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	installCommittedGeneration(t, manager, next.Store, request1.OperationID)
	transferDriver.v1Running = false
	transferDriver.stageExists = false
	transferDriver.stageRunning = false
	transferDriver.finalExists = true
	transferDriver.finalRunning = true
	transferDriver.finalConfig = []byte("attested startup config")

	claims := []GatewayV2StartupClaim{
		{Request: request0, State: appaccess.GatewayProfileUpgradeRolledBack, StateSequence: 2},
		{Request: request1, State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2},
	}
	got, err := manager.InspectGatewayV2Startup(context.Background(), claims)
	if err != nil || got != (GatewayV2StartupInspection{
		Disposition: GatewayV2StartupNormalV2, OperationID: request1.OperationID,
	}) {
		t.Fatalf("inspection=%+v err=%v", got, err)
	}

	claims[0].Request.ApprovedBy = "88888888-8888-4888-8888-888888888888"
	got, err = manager.InspectGatewayV2Startup(context.Background(), claims)
	if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
		t.Fatalf("historical mismatch inspection=%+v err=%v", got, err)
	}
}

func TestGatewayV2StartupReceiptHistoryBeforeNoArtifactClaimReplaysBothProofs(t *testing.T) {
	manager, _, request0, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	store0, state0, journal0 := installRolledBackGeneration(t, manager, 0, request0.OperationID)
	if _, err := store0.installRollbackRetirementReceipt(state0, journal0); err != nil {
		t.Fatal(err)
	}
	request1 := request0
	request1.OperationID = "77777777-7777-4777-8777-777777777777"

	got, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{
		{Request: request0, State: appaccess.GatewayProfileUpgradeRolledBack, StateSequence: 2},
		{Request: request1, State: appaccess.GatewayProfileUpgradePrepared, StateSequence: 1},
	})
	if err != nil || got != (GatewayV2StartupInspection{
		Disposition: GatewayV2StartupRecoveryOnly, OperationID: request1.OperationID,
	}) {
		t.Fatalf("inspection=%+v err=%v", got, err)
	}
	if sourceDriver.retirementObserveCalls != 1 || sourceDriver.abortCalls != 1 {
		t.Fatalf("retirement proofs=%d no-artifact proofs=%d", sourceDriver.retirementObserveCalls, sourceDriver.abortCalls)
	}
}

func TestGatewayV2StartupRereadsSourceAndHistory(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		manager, _, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
		manager.gatewayV2CoordinatorDriver = &gatewayV2StartupMutatingObserver{
			fakeGatewayV2CoordinatorDriver: sourceDriver,
			afterAbort: func() {
				if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{}}); err != nil {
					t.Fatal(err)
				}
			},
		}
		got, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{{
			Request: request, State: appaccess.GatewayProfileUpgradePrepared, StateSequence: 1,
		}})
		if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
			t.Fatalf("inspection=%+v err=%v", got, err)
		}
	})

	t.Run("history", func(t *testing.T) {
		manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
		manager.gatewayV2CoordinatorDriver = &gatewayV2StartupMutatingObserver{
			fakeGatewayV2CoordinatorDriver: sourceDriver,
			afterAbort: func() {
				installGatewayV2PreparationAbortStateOnly(t, manager, store, request, sourceDriver)
			},
		}
		got, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{{
			Request: request, State: appaccess.GatewayProfileUpgradePrepared, StateSequence: 1,
		}})
		if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
			t.Fatalf("inspection=%+v err=%v", got, err)
		}
	})
}

type gatewayV2StartupMutatingObserver struct {
	*fakeGatewayV2CoordinatorDriver
	afterAbort func()
}

func (d *gatewayV2StartupMutatingObserver) attestPreparationAbort(ctx context.Context, source routeState,
	preparation gatewayUpgradePreparation,
) (string, error) {
	digest, err := d.fakeGatewayV2CoordinatorDriver.attestPreparationAbort(ctx, source, preparation)
	if err == nil && d.afterAbort != nil {
		d.afterAbort()
	}
	return digest, err
}

func installGatewayV2StartupPrepared(t *testing.T, manager *Manager, store *gatewayUpgradeStateStore,
	request GatewayV2UpgradeRequest, sourceDriver *fakeGatewayV2CoordinatorDriver,
) {
	t.Helper()
	preparation, err := gatewayV2UpgradePreparation(request)
	if err != nil {
		t.Fatal(err)
	}
	preparation.LocalHostPort = manager.options.HostPort
	preparation.Network = sourceDriver.plan
	preparation.SourceIdentityDigest = sourceDriver.digest
	source, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, preparation)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGatewayV2ProtectedState(store, state, journal); err != nil {
		t.Fatal(err)
	}
}
