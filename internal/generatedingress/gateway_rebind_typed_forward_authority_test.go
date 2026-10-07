package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func newGatewayRebindTypedForwardOnlyFixture(t *testing.T) (gatewayRebindTypedHandoverRuntimeFixture,
	managerGatewayRebindCrossStoreDriver, gatewayRebindPhysicalReconcileRequest,
) {
	t.Helper()
	fixture := newGatewayRebindTypedHandoverRuntimeFixtureWithApprovals(t, true,
		func(f gatewayRebindPredecessorFixture, input *gatewayRebindCommitInput) {
			for _, approval := range []*appaccess.Approval{&input.RebindApproval, &input.ConfigureApproval} {
				approval.ActorID = uuid.NewString()
				if _, err := f.db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
					VALUES(?,?,'hash','administrator',datetime('now'),datetime('now'))`, approval.ActorID, approval.ActorID); err != nil {
					t.Fatal(err)
				}
			}
		})
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	physical, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionCommit ||
		!fixture.runner.final.Running || fixture.runner.predecessor.FinalContainer.Running {
		t.Fatalf("forward fixture did not complete handover: disposition=%s err=%v", physical.Disposition, err)
	}
	receipt, err := fixture.predecessor.manager.installGatewayRebindTerminalForPhysicalLocked(
		context.Background(), fixture.attempt, physical)
	if err != nil {
		t.Fatal(err)
	}
	request.Mode, request.Terminal = gatewayRebindPhysicalReconcileForwardOnly, &receipt
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("forward-only fixture request is invalid")
	}
	fixture.runner.effects = nil
	return fixture, driver, request
}

func TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor(t *testing.T) {
	fixture, driver, request := newGatewayRebindTypedForwardOnlyFixture(t)
	ctx := context.Background()
	for _, phase := range []appaccess.GatewayRebindState{appaccess.GatewayRebindPrepared,
		appaccess.GatewayRebindSuccessorReady, appaccess.GatewayRebindDatabaseCommitted} {
		if phase != appaccess.GatewayRebindPrepared {
			advanceGatewayRebindTypedForwardFixture(t, fixture, request, phase)
		}
		snapshot, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
		if err != nil || snapshot.Phase != phase {
			t.Fatalf("forward fixture phase=%s err=%v", snapshot.Phase, err)
		}
		currentRequest := gatewayRebindForwardConfirmationRequest(fixture.attempt, *request.Terminal, snapshot)
		cases := []string{"revoked"}
		if phase == appaccess.GatewayRebindPrepared {
			cases = []string{"approved", "revoked"}
		} else if phase == appaccess.GatewayRebindSuccessorReady {
			cases = []string{"stale phase after acknowledgement"}
		} else {
			cases = []string{"approved", "revoked", "late revocation", "already stopped", "lost stop acknowledgement",
				"ambiguous listener", "replacement container", "cancelled caller", "restart during probes", "final SQL failure"}
		}
		for _, kind := range cases {
			t.Run(string(phase)+"/"+kind, func(t *testing.T) {
				fixture.predecessor.manager.gatewayRebindFailStopLatch().Store(false)
				fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Store(false)
				fixture.runner.startFinal()
				fixture.runner.effects, fixture.runner.absenceRequests = nil, nil
				fixture.runner.refuseAbsence = nil
				fixture.predecessor.manager.runner = fixture.runner
				runtime := fixture.runtime
				driver.handover = runtime
				actor := fixture.attempt.Claim.RebindApproval.ActorID
				if phase == appaccess.GatewayRebindSuccessorReady || kind == "late revocation" {
					actor = fixture.attempt.Claim.ConfigureApproval.ActorID
				} else if phase == appaccess.GatewayRebindDatabaseCommitted && kind == "revoked" {
					actor = fixture.predecessor.state.Apps[fixture.predecessor.stateAppID()].LAN.ApprovedBy
				}
				demoted := false
				demote := func() {
					if demoted {
						return
					}
					update, err := fixture.predecessor.db.Exec(`UPDATE users SET role='viewer' WHERE id=? AND role='administrator'`, actor)
					if err != nil {
						t.Fatal(err)
					}
					if count, err := update.RowsAffected(); err != nil || count != 1 {
						t.Fatalf("revocation affected=%d err=%v", count, err)
					}
					demoted = true
				}
				defer func() {
					if _, err := fixture.predecessor.db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, actor); err != nil {
						t.Error(err)
					}
					fixture.runner.final.ID = fixture.runner.finalID
				}()
				if kind != "approved" && kind != "already stopped" && kind != "late revocation" && kind != "final SQL failure" {
					demote()
				}
				mutated := false
				if kind == "late revocation" || kind == "restart during probes" {
					runtime.listenerAbsent = func(probeCtx context.Context, address string, port uint16) bool {
						absent := fixture.runner.listenerAbsent(probeCtx, address, port)
						if !mutated && absent {
							if kind == "late revocation" {
								demote()
							} else {
								fixture.runner.startFinal()
							}
							mutated = true
						}
						return absent
					}
					driver.handover = runtime
				}
				if kind == "already stopped" {
					fixture.runner.stopFinal()
				}
				if kind == "replacement container" {
					fixture.runner.final.ID = strings.Repeat("8", 64)
				}
				if kind == "ambiguous listener" {
					fixture.runner.refuseAbsence = &gatewayRebindTypedHandoverAbsenceRequest{
						Address: fixture.attempt.Intent.SuccessorProfile.SelectedIPv4, Port: fixture.attempt.Intent.SuccessorProfile.PortStart}
				}
				if kind == "lost stop acknowledgement" {
					fixture.predecessor.manager.runner = &gatewayRebindForwardLostStopAckRunner{fixture.runner}
				}
				before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if err != nil {
					t.Fatal(err)
				}
				usedRequest := currentRequest
				if kind == "stale phase after acknowledgement" {
					usedRequest = request
				}
				callCtx := ctx
				if kind == "cancelled caller" {
					var cancel context.CancelFunc
					callCtx, cancel = context.WithCancel(ctx)
					cancel()
				}
				var result gatewayRebindTypedPhysicalResult
				var reconcileErr error
				if kind == "final SQL failure" {
					repository := &gatewayRebindForwardFailFinalSQLRepository{Repository: fixture.predecessor.repository, beforeFailure: demote}
					// Current-state attestation is a separate adapter integration.
					// Keep that one boundary bounded; all forward confirmation and
					// withdrawal effects here use the concrete typed runtime.
					attesting := gatewayRebindForwardBoundedAttestationDriver{driver}
					recovered, err := fixture.predecessor.manager.recoverGatewayRebindCommitTerminalLocked(ctx, repository,
						attesting, fixture.attempt, *request.Terminal)
					reconcileErr = err
					if repository.calls != 1 || !demoted || recovered != (GatewayRebindCommitResult{}) ||
						!fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Load() {
						t.Fatalf("final SQL failure boundary was not reached: calls=%d demoted=%t result=%#v err=%v", repository.calls, demoted, recovered, err)
					}
				} else {
					result, reconcileErr = driver.reconcileSuccessorLocked(callCtx, usedRequest, fixture.appendProgress)
				}
				if kind == "approved" {
					if reconcileErr != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit ||
						!fixture.runner.final.Running || len(fixture.runner.effects) != 0 {
						t.Fatalf("approved recovery refused: effects=%v err=%v", fixture.runner.effects, reconcileErr)
					}
				} else {
					wantLive := kind == "ambiguous listener" || kind == "replacement container" || kind == "restart during probes"
					wantRunning := kind == "replacement container" || kind == "restart during probes"
					wantStops := 1
					if kind == "already stopped" || kind == "replacement container" {
						wantStops = 0
					}
					var diagnostic *Error
					if reconcileErr == nil || !errors.As(reconcileErr, &diagnostic) || diagnostic.candidateMayBeLive != wantLive ||
						!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || fixture.runner.final.Running != wantRunning ||
						len(fixture.runner.effects) != wantStops || !fixture.predecessor.manager.gatewayRebindFailStopLatch().Load() ||
						(kind == "late revocation" && !demoted) || (kind == "restart during probes" && !mutated) {
						t.Fatalf("forward refusal final=%t wantRunning=%t effects=%v demoted=%t mutated=%t err=%v", fixture.runner.final.Running, wantRunning, fixture.runner.effects, demoted, mutated, reconcileErr)
					}
					for _, effect := range fixture.runner.effects {
						if !reflect.DeepEqual(effect, []string{"container", "stop", "--time", "10", fixture.runner.finalID}) {
							t.Fatalf("foreign or destructive effect: %v", effect)
						}
					}
				}
				after, readErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
				retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if readErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
					fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil || fixture.runner.predecessor.FinalContainer.Running ||
					!fixture.runner.finalPresent || !fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
					t.Fatalf("forward recovery changed history/fence/resources: SQL=%v history=%v", readErr, historyErr)
				}
			})
		}
	}
}

type gatewayRebindForwardLostStopAckRunner struct {
	*gatewayRebindTypedHandoverRuntimeRunner
}

type gatewayRebindForwardBoundedAttestationDriver struct {
	managerGatewayRebindCrossStoreDriver
}

func (d gatewayRebindForwardBoundedAttestationDriver) attestCommittedCurrentLocked(_ context.Context,
	selection gatewayCurrentSelection,
) (string, error) {
	return canonicalDigest(selection.Lineage)
}

type gatewayRebindForwardFailFinalSQLRepository struct {
	*appaccess.Repository
	beforeFailure func()
	calls         int
}

func (r *gatewayRebindForwardFailFinalSQLRepository) ApplyGatewayRebindTransition(ctx context.Context,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	if proof.NextState == appaccess.GatewayRebindCommitted {
		r.calls++
		r.beforeFailure()
		return appaccess.GatewayRebindTransitionCommand{}, errors.New("injected final SQL transition failure")
	}
	return r.Repository.ApplyGatewayRebindTransition(ctx, proof)
}

func (r *gatewayRebindForwardLostStopAckRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	result, err := r.gatewayRebindTypedHandoverRuntimeRunner.Run(ctx, request)
	if args := request.Args; err == nil && len(args) == 5 && args[0] == "container" && args[1] == "stop" && args[4] == r.finalID {
		return result, errors.New("injected lost exact successor stop acknowledgement")
	}
	return result, err
}

func advanceGatewayRebindTypedForwardFixture(t *testing.T, fixture gatewayRebindTypedHandoverRuntimeFixture,
	request gatewayRebindPhysicalReconcileRequest, phase appaccess.GatewayRebindState,
) {
	t.Helper()
	ctx := context.Background()
	snapshot, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var state gatewayCurrentRouteState
	var transfers []appaccess.GatewayRebindAllocationTransfer
	if phase == appaccess.GatewayRebindDatabaseCommitted {
		transfers, err = newGatewayRebindTransfersV2(fixture.attempt.Intent, *request.Terminal, fixture.attempt.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		state, err = newGatewayCurrentRouteBaselineFromV2Terminal(fixture.attempt.Intent, *request.Terminal, fixture.attempt.Checkpoint, transfers)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayCurrentRouteStateStore(fixture.predecessor.manager.options.DataRoot, state.Lineage)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.installBaseline(state); err != nil {
			t.Fatal(err)
		}
	}
	proof, err := gatewayRebindTransitionProofV2(snapshot, fixture.attempt.Claim, *request.Terminal, phase, state, transfers, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyGatewayRebindTransitionReconciled(ctx, fixture.predecessor.repository, proof); err != nil {
		t.Fatal(err)
	}
}
