package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

// Stop at the first physical boundary. These tests establish the coordinator's
// direction and actual SQL phase, not successful recovery or physical proof.
type gatewayRebindRequestedRecoveryRecorder struct {
	managerGatewayRebindCrossStoreDriver
	requests []gatewayRebindPhysicalReconcileRequest
	stop     error
}

func (d *gatewayRebindRequestedRecoveryRecorder) reconcileSuccessorLocked(_ context.Context,
	request gatewayRebindPhysicalReconcileRequest, _ gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	d.requests = append(d.requests, request)
	return gatewayRebindTypedPhysicalResult{}, d.stop
}

func TestGatewayRebindRequestedRecoveryPreservesActualUnresolvedDirection(t *testing.T) {
	for _, test := range []struct {
		name         string
		abort        bool
		database     bool
		undecided    bool
		wantRollback bool
		policy       appaccess.GatewayRebindRecoveryPolicy
		wantMode     gatewayRebindPhysicalReconcileMode
		wantCalls    int
	}{
		{name: "pre-DB fixed commit", policy: appaccess.GatewayRebindRecoveryForwardOnly,
			wantMode: gatewayRebindPhysicalReconcileForwardOnly, wantCalls: 1},
		{name: "pre-DB fixed abort", abort: true, wantRollback: true, policy: appaccess.GatewayRebindRecoveryRollbackOnly,
			wantMode: gatewayRebindPhysicalReconcileRollbackOnly, wantCalls: 1},
		{name: "post-DB fixed commit", database: true, policy: appaccess.GatewayRebindRecoveryForwardOnly,
			wantMode: gatewayRebindPhysicalReconcileForwardOnly, wantCalls: 1},
		{name: "post-DB refuses rollback", database: true, policy: appaccess.GatewayRebindRecoveryRollbackOnly},
		{name: "pre-DB undecided refuses invented direction", undecided: true, wantRollback: true,
			policy: appaccess.GatewayRebindRecoveryEvidenceDecides},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			var fixture gatewayRebindTypedHandoverRuntimeFixture
			var driver managerGatewayRebindCrossStoreDriver
			var terminal *gatewayRebindTerminalReceiptV2
			if !test.abort && !test.undecided {
				var request gatewayRebindPhysicalReconcileRequest
				fixture, driver, request = newGatewayRebindTypedForwardOnlyFixture(t)
				terminal = request.Terminal
				if test.database {
					advanceGatewayRebindTypedForwardFixture(t, fixture, request, appaccess.GatewayRebindSuccessorReady)
					advanceGatewayRebindTypedForwardFixture(t, fixture, request, appaccess.GatewayRebindDatabaseCommitted)
				}
			} else {
				fixture = newGatewayRebindTypedHandoverRuntimeFixture(t, true)
				driver = managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
					stage: fixture.stageDriver, handover: fixture.runtime}
				if test.abort {
					fixture.runner.failFinalCreate = true
					physical, err := driver.reconcileSuccessorLocked(ctx, gatewayRebindPhysicalReconcileRequest{
						Attempt: fixture.attempt, Mode: gatewayRebindPhysicalReconcileUndecided,
						SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true,
					}, fixture.appendProgress)
					if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionAbort {
						t.Fatalf("prepare actual protected abort: disposition=%s error=%v", physical.Disposition, err)
					}
					receipt, err := fixture.predecessor.manager.installGatewayRebindTerminalForPhysicalLocked(ctx,
						fixture.attempt, physical)
					if err != nil {
						t.Fatal(err)
					}
					terminal = &receipt
				}
			}
			manager, repository := fixture.predecessor.manager, fixture.predecessor.repository
			before := gatewayRebindRequestedUnresolvedSnapshot(t, fixture, terminal)
			if before.Phase != appaccess.GatewayRebindUnresolved || before.Active == nil ||
				before.Active.Claim.V2.State != appaccess.GatewayRebindUnresolved ||
				before.DatabaseCommitObserved != test.database ||
				(before.DatabaseCommittedEvent != nil) != test.database || before.RollbackAllowed != test.wantRollback {
				t.Fatal("fixture did not retain the actual unresolved phase and database barrier")
			}
			files, err := readGatewayHistorySnapshotMode(manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			fixture.runner.effects = nil
			sentinel := errors.New("stop at requested recovery physical boundary")
			recorder := &gatewayRebindRequestedRecoveryRecorder{
				managerGatewayRebindCrossStoreDriver: driver, stop: sentinel}
			var result GatewayRebindStartupRecoveryResult
			err = manager.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
				var recoveryErr error
				result, recoveryErr = manager.recoverGatewayRebindStartupExpectedLocked(ctx, repository,
					recorder, gatewayRebindRequestedRecovery{OperationID: fixture.attempt.Claim.Spec.OperationID,
						Policy: test.policy})
				return false, recoveryErr
			})
			if err == nil || len(recorder.requests) != test.wantCalls ||
				!reflect.DeepEqual(result, GatewayRebindStartupRecoveryResult{}) {
				t.Fatalf("requested recovery calls=%d want=%d result=%#v error=%v",
					len(recorder.requests), test.wantCalls, result, err)
			}
			if test.wantCalls == 1 {
				request := recorder.requests[0]
				if !errors.Is(err, sentinel) || request.SQLPhase != appaccess.GatewayRebindUnresolved ||
					request.Mode != test.wantMode || request.DatabaseCommitObserved != test.database ||
					request.RollbackAllowed != before.RollbackAllowed ||
					!reflect.DeepEqual(request.Terminal, terminal) || !validGatewayRebindPhysicalReconcileRequest(request) {
					t.Fatalf("physical boundary changed actual phase or fixed direction: phase=%s mode=%s error=%v",
						request.SQLPhase, request.Mode, err)
				}
			} else if errors.Is(err, sentinel) {
				t.Fatal("unsupported direction reached physical boundary")
			}
			after, readErr := repository.GatewayRebindRecoverySnapshot(ctx)
			afterFiles, filesErr := readGatewayHistorySnapshotMode(manager.store, true)
			if readErr != nil || filesErr != nil || !reflect.DeepEqual(before, after) ||
				!sameGatewayHistorySnapshot(files, afterFiles) || len(fixture.runner.effects) != 0 ||
				!errors.Is(repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
				t.Fatal("direction selection changed SQL, retained history, Docker state or the active fence")
			}
		})
	}
}

func gatewayRebindRequestedUnresolvedSnapshot(t *testing.T,
	fixture gatewayRebindTypedHandoverRuntimeFixture, terminal *gatewayRebindTerminalReceiptV2,
) appaccess.GatewayRebindRecoverySnapshot {
	t.Helper()
	ctx := context.Background()
	repository := fixture.predecessor.repository
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil || snapshot.CurrentSource == nil {
		t.Fatalf("read active direction fixture: %v", err)
	}
	var proof appaccess.GatewayRebindTransitionProof
	if terminal != nil {
		proof, err = gatewayRebindTransitionProofV2(snapshot, fixture.attempt.Claim, *terminal,
			appaccess.GatewayRebindUnresolved, gatewayCurrentRouteState{}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		// Retain an undecided prefix's actual last record; do not invent a
		// terminal receipt or translate the stored phase for recovery.
		last := fixture.records[len(fixture.records)-1]
		claim, source := snapshot.Active.Claim.V2, snapshot.CurrentSource
		proof = appaccess.GatewayRebindTransitionProof{
			Version: appaccess.GatewayRebindTransitionVersionV1, Purpose: appaccess.GatewayRebindTransitionPurpose,
			OperationID: claim.Spec.OperationID, ClaimRequestDigest: claim.RequestDigest,
			ClaimSpecDigest: claim.RebindApproval.SpecDigest, ExpectedState: snapshot.Phase,
			ExpectedSequence: claim.StateSequence, NextState: appaccess.GatewayRebindUnresolved,
			ExpectedHeadRevisionID: source.ProfileRevisionID, ExpectedHeadRevisionNumber: source.ProfileRevisionNumber,
			ExpectedHeadSpecDigest: source.ProfileSpecDigest, ProtectedGeneration: last.Generation,
			ProtectedPhase: string(last.Phase), ProtectedRecordSequence: last.Sequence, ProtectedRecordDigest: last.Digest,
			TerminalDisposition:         appaccess.GatewayRebindDispositionNone,
			PredecessorCheckpointDigest: claim.Spec.Predecessor.PredecessorCheckpointDigest,
			SourceStateVersion:          claim.Spec.Predecessor.SourceStateVersion,
			SourceStateRevision:         claim.Spec.Predecessor.SourceStateRevision, SourceStateDigest: claim.Spec.Predecessor.SourceStateDigest,
		}
	}
	if _, err = repository.ApplyGatewayRebindTransition(ctx, proof); err != nil {
		t.Fatalf("record actual unresolved transition: %v", err)
	}
	snapshot, err = repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
