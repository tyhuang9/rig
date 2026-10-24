package generatedingress

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2StartupPreparedAbortRetainsOrdinaryRefusalAndAdmitsExactProof(t *testing.T) {
	fixture, driver := newGatewayRebindStartupCheckpointFixture(t)
	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
		&gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template})
	if err != nil || result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased {
		t.Fatalf("prepared abort recovery=%#v error=%v", result, err)
	}
	prepared, err := fresh.InspectGatewayRebindCurrent(context.Background(), fixture.repository)
	if err != nil || prepared.ActiveOperationID != "" || !prepared.FenceReleased ||
		prepared.CurrentRecoveryMode != GatewayCurrentRecoveryNative || len(prepared.Retained) != 1 {
		t.Fatalf("prepared terminal proof=%#v error=%v", prepared, err)
	}
	claims := []GatewayV2StartupClaim{{
		Request: gatewayV2UpgradeRequestFromStateForTest(fixture.state),
		State:   appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2,
	}}
	inspection, err := fresh.InspectGatewayV2Startup(context.Background(), claims)
	if err == nil || inspection != (GatewayV2StartupInspection{}) ||
		GatewayStartupDiagnosticCheckpoint(err) != "upgrade_history" {
		t.Fatalf("retained prepared abort did not reproduce ordinary startup refusal: inspection=%#v checkpoint=%q error=%v",
			inspection, GatewayStartupDiagnosticCheckpoint(err), err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil {
		t.Fatal(err)
	}
	beforeSQL, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		proof GatewayRebindCurrentInspection
	}{
		{name: "missing proof"},
		{name: "forged receipt", proof: func() GatewayRebindCurrentInspection {
			value := prepared
			value.Retained = append([]GatewayRebindRetainedOperationInspection(nil), value.Retained...)
			value.Retained[0].TerminalReceiptDigest = strings.Repeat("f", 64)
			return value
		}()},
		{name: "mismatched current authority", proof: func() GatewayRebindCurrentInspection {
			value := prepared
			value.SelectedCurrentAuthority.OperationID = "99999999-9999-4999-8999-999999999999"
			return value
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, inspectErr := fresh.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, test.proof)
			if inspectErr == nil || got != (GatewayV2StartupInspection{}) ||
				GatewayStartupDiagnosticCheckpoint(inspectErr) != "upgrade_current_binding" {
				t.Fatalf("invalid prepared proof was accepted: inspection=%#v checkpoint=%q error=%v",
					got, GatewayStartupDiagnosticCheckpoint(inspectErr), inspectErr)
			}
			afterFiles, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
			afterSQL, sqlErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if filesErr != nil || sqlErr != nil || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) ||
				!reflect.DeepEqual(beforeSQL, afterSQL) {
				t.Fatalf("proof refusal changed durable evidence: files=%v SQL=%v", filesErr, sqlErr)
			}
		})
	}

	t.Run("stale SQL current binding", func(t *testing.T) {
		original := fresh.options.RebindCurrentStateRepository
		fresh.options.RebindCurrentStateRepository = &gatewayRebindRecoverySnapshotDriftRepository{
			Repository: fixture.repository, drift: true,
		}
		t.Cleanup(func() { fresh.options.RebindCurrentStateRepository = original })
		got, inspectErr := fresh.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared)
		if inspectErr == nil || got != (GatewayV2StartupInspection{}) ||
			GatewayStartupDiagnosticCheckpoint(inspectErr) != "upgrade_current_binding" {
			t.Fatalf("stale SQL binding was accepted: inspection=%#v checkpoint=%q error=%v",
				got, GatewayStartupDiagnosticCheckpoint(inspectErr), inspectErr)
		}
	})

	t.Run("different manager data root", func(t *testing.T) {
		other, _ := newManagerFixture(t, false)
		other.options.RebindFenceCheck = fixture.repository.CheckGatewayRebindFence
		other.options.RebindCurrentStateRepository = fixture.repository
		got, inspectErr := other.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared)
		if inspectErr == nil || got != (GatewayV2StartupInspection{}) ||
			GatewayStartupDiagnosticCheckpoint(inspectErr) != "upgrade_current_binding" {
			t.Fatalf("foreign data root accepted proof: inspection=%#v checkpoint=%q error=%v",
				got, GatewayStartupDiagnosticCheckpoint(inspectErr), inspectErr)
		}
	})
	if present, err := fresh.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || present {
		t.Fatalf("prepared native abort LAN batch presence=%v error=%v", present, err)
	}
	fresh.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) gatewayObservedTopology {
		return gatewayTopologyExactFinalV2
	}

	fresh.gatewayV2TransferDriver = &fakeGatewayV2TransferDriver{t: t, manager: fresh,
		finalExists: true, finalRunning: true, finalConfig: []byte("attested startup config")}
	// R01 and R02 differ only in the retained nonterminal prefix observed by the
	// crashed process. Recovery converges both to this exact immutable ABORT
	// terminal before normal startup is admitted.
	for _, boundary := range []string{"r01 rollback intent", "r02 rollback completed"} {
		t.Run(boundary, func(t *testing.T) {
			got, inspectErr := fresh.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared)
			if inspectErr != nil || got.Disposition != GatewayV2StartupNormalV2 ||
				got.OperationID != fixture.state.OperationID || got.CurrentGatewaySource.Kind != "" {
				t.Fatalf("exact prepared abort proof was not admitted: inspection=%#v checkpoint=%q error=%v",
					got, GatewayStartupDiagnosticCheckpoint(inspectErr), inspectErr)
			}
		})
	}

	history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := newGatewayRebindPredecessorCheckpoint(prepared.Retained[0].Generation+1,
		uuid.NewString(), lineage, &history.Predecessor.State, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fresh.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpointStore.installExact(checkpoint); err != nil {
		t.Fatalf("install later unresolved checkpoint: %v", err)
	}
	ignoredTail, err := fresh.InspectGatewayRebindCurrent(context.Background(), fixture.repository)
	if err != nil || !reflect.DeepEqual(ignoredTail, prepared) {
		t.Fatalf("fixture did not isolate omitted protected tail: proof=%#v error=%v", ignoredTail, err)
	}
	beforeTailRefusal, err := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil {
		t.Fatal(err)
	}
	got, inspectErr := fresh.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared)
	if inspectErr == nil || got != (GatewayV2StartupInspection{}) ||
		GatewayStartupDiagnosticCheckpoint(inspectErr) != "upgrade_current_binding" {
		t.Fatalf("later unresolved protected tail was accepted: inspection=%#v checkpoint=%q error=%v",
			got, GatewayStartupDiagnosticCheckpoint(inspectErr), inspectErr)
	}
	afterTailRefusal, err := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil || !sameGatewayHistorySnapshot(beforeTailRefusal, afterTailRefusal) {
		t.Fatalf("unresolved-tail refusal changed protected evidence: %v", err)
	}
}

func TestGatewayV2StartupPreparedLostCutoverAckCommitDirectionAdmitsExactProof(t *testing.T) {
	// Lost-cutover-ack recovery converges to the same fully committed current
	// authority and immutable terminal proof exercised by this startup handoff.
	fixture, snapshot, claims, _ := gatewayRebindCurrentStartupFixture(t)
	repository := &gatewayRebindProposalRepositoryFake{snapshot: snapshot}
	prepared, err := fixture.manager.InspectGatewayRebindCurrent(context.Background(), repository)
	if err != nil || prepared.ActiveOperationID != "" || !prepared.FenceReleased ||
		prepared.CurrentRecoveryMode != GatewayCurrentRecoveryStable || len(prepared.Retained) != 1 {
		t.Fatalf("lost-cutover-ack terminal proof=%#v error=%v", prepared, err)
	}
	inspection, err := fixture.manager.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared)
	if err != nil || inspection.Disposition != GatewayV2StartupNormalV2 ||
		inspection.OperationID != snapshot.CurrentSource.OperationID ||
		inspection.CurrentGatewaySource != *snapshot.CurrentSource {
		t.Fatalf("commit-direction prepared startup=%#v error=%v", inspection, err)
	}
}

func TestGatewayV2StartupPreparedRebindRequiresFreshProofAfterRouteRecovery(t *testing.T) {
	fixture, snapshot, claims, physical := gatewayRebindCurrentStartupFixture(t)
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: physical.attest.Terminal,
		pendingAttestOutcome: gatewayCurrentPhysicalRecoveryBefore}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	transition, err := gatewayCurrentSwitchTransition(fixture.baseline,
		routeOperationSwitchRequest(t, appID, app.Route))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.saveNext(fixture.baseline, transition.Pending); err != nil {
		t.Fatal(err)
	}
	repository := &gatewayRebindProposalRepositoryFake{snapshot: snapshot}
	prepared, err := fixture.manager.InspectGatewayRebindCurrent(context.Background(), repository)
	if err != nil || prepared.CurrentRecoveryMode != GatewayCurrentRecoveryRoute {
		t.Fatalf("pending-route prepared proof=%#v error=%v", prepared, err)
	}
	before, err := fixture.manager.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared)
	if err != nil || before.Disposition != GatewayV2StartupNormalV2 {
		t.Fatalf("pending-route prepared startup=%#v error=%v", before, err)
	}
	if err := exerciseGatewayCurrentRecoveryCore(fixture.manager, context.Background()); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.manager.InspectGatewayRebindCurrent(context.Background(), repository)
	if err != nil || current.CurrentRecoveryMode != GatewayCurrentRecoveryStable ||
		current.CurrentStateRevision <= prepared.CurrentStateRevision || current.CurrentStateDigest == prepared.CurrentStateDigest {
		t.Fatalf("post-recovery current proof=%#v error=%v", current, err)
	}
	if got, staleErr := fixture.manager.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, prepared); staleErr == nil || got != (GatewayV2StartupInspection{}) ||
		GatewayStartupDiagnosticCheckpoint(staleErr) != "upgrade_current_binding" {
		t.Fatalf("stale pre-recovery proof was accepted: inspection=%#v checkpoint=%q error=%v",
			got, GatewayStartupDiagnosticCheckpoint(staleErr), staleErr)
	}
	after, err := fixture.manager.InspectGatewayV2StartupPreparedRebind(context.Background(), claims, current)
	if err != nil || after != before {
		t.Fatalf("fresh post-recovery proof changed startup: before=%#v after=%#v error=%v", before, after, err)
	}
}
