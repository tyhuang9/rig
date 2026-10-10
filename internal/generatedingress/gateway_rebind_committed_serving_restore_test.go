package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

// The crash is a real SQL database_committed transition with retained protected
// history. Docker and network probes use the concrete executor's test boundary.
func gatewayRebindCommittedServingFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindPhysicalReconcileRequest, gatewayCurrentSelection, *gatewayCurrentPhysicalExecutor,
) {
	return gatewayRebindCommittedServingFixtureWithApprovals(t, nil)
}

func gatewayRebindCommittedServingFixtureWithApprovals(t *testing.T,
	prepare func(gatewayRebindPredecessorFixture, *gatewayRebindCommitInput),
) (gatewayRebindPredecessorFixture, gatewayRebindPhysicalReconcileRequest, gatewayCurrentSelection, *gatewayCurrentPhysicalExecutor) {
	t.Helper()
	ctx := context.Background()
	// Match the SQL component from initial fixture creation, before immutable
	// journals are written. No committed identity/history is rewritten.
	f := newGatewayRebindPredecessorFixtureWithEndpoint(t, false, '3')
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	f.manager.options.RebindCurrentStateRepository = f.repository
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	input := completeGatewayRebindCoordinatorFixture(t, f)
	if prepare != nil {
		prepare(f, &input)
	}
	template := newGatewayCurrentStateFixture(t)
	f.manager.gatewayRebindV2NetworkObserver = func(_ context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		value := template.intent.NetworkObservation
		value.OperationID, value.ClaimRequestDigest, value.ProfileSpecDigest = claim.Spec.OperationID, claim.RequestDigest, claim.ConfigureApproval.SpecDigest
		return value, nil
	}
	interrupted := &gatewayRebindRecoveryFailAttestationDriver{gatewayRebindBoundedPhysicalDriver: &gatewayRebindBoundedPhysicalDriver{t: t, template: template}}
	if _, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, interrupted); err == nil {
		t.Fatal("expected crash before committed-current attestation")
	}
	// A new process has fresh latches. The SQL fence must survive the crash.
	f.manager = freshGatewayRebindRecoveryManager(f.manager)
	f.manager.gatewayRebindFailStop, f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || snapshot.Rebind.Phase != appaccess.GatewayRebindDatabaseCommitted || f.repository.CheckGatewayRebindFence(ctx) == nil {
		t.Fatalf("missing retained database commit: %v", err)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := gatewayRebindPreparedAttemptFromHistory(snapshot.Rebind, history)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := gatewayRebindActiveTerminalV2(history, *snapshot.Rebind.Active.Claim.V2)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: attempt, Terminal: terminal,
		Mode: gatewayRebindPhysicalReconcileForwardOnly, SQLPhase: snapshot.Rebind.Phase,
		DatabaseCommitObserved: true}
	selection, err := f.manager.selectGatewayCurrentLocked(ctx, snapshot.Rebind)
	if err != nil {
		t.Fatal(err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	port := f.journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, *selection.State))
	f.manager.gatewayCurrentPhysicalDriver = managedGatewayCurrentServingExecutorDriver(
		gatewayCurrentStateFixture{manager: f.manager}, runner, port)
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, f.manager.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	releaseGateway, err := f.manager.lockGatewayRaw(ctx)
	if err != nil {
		_ = releaseEffects()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.manager.releaseGatewayRebindTerminalLocks(releaseEffects, releaseGateway, nil); err != nil {
			t.Error(err)
		}
	})
	return f, request, selection, runner
}

func TestGatewayRebindCommittedServingRestoreRequiresCurrentApprovers(t *testing.T) {
	for _, kind := range []string{"approved", "rebind before start", "configure before start", "rebind after start", "configure after start"} {
		t.Run(kind, func(t *testing.T) {
			f, request, _, runner := gatewayRebindCommittedServingFixtureWithApprovals(t,
				func(f gatewayRebindPredecessorFixture, input *gatewayRebindCommitInput) {
					for _, approval := range []*appaccess.Approval{&input.RebindApproval, &input.ConfigureApproval} {
						approval.ActorID = uuid.NewString()
						if _, err := f.db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
							VALUES(?,?,'hash','administrator',datetime('now'),datetime('now'))`, approval.ActorID, approval.ActorID); err != nil {
							t.Fatal(err)
						}
					}
				})
			ctx := context.Background()
			before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil || !before.ActiveRebindApprovalsAuthorizeServing() {
				t.Fatalf("initial approval: %v", err)
			}
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			actor := request.Attempt.Claim.RebindApproval.ActorID
			if kind == "configure before start" || kind == "configure after start" {
				actor = request.Attempt.Claim.ConfigureApproval.ActorID
			}
			demoted := false
			demote := func() {
				if _, err := f.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, actor); err != nil {
					t.Fatal(err)
				}
				demoted = true
			}
			late := kind == "rebind after start" || kind == "configure after start"
			if kind != "approved" && !late {
				demote()
			}
			guard := func(context.Context) error {
				if late && runner.container.Running && !demoted {
					demote()
				}
				return nil
			}
			_, err = f.manager.restoreGatewayRebindCommittedServingLocked(ctx, f.repository, request, guard)
			started := containsGatewayCurrentPhysicalEffect(runner.effects, []string{"container", "start", runner.target.Resources.FinalContainer.ID})
			stopped := containsGatewayCurrentPhysicalEffect(runner.effects, []string{"container", "stop", "--time", "10", runner.target.Resources.FinalContainer.ID})
			if kind == "approved" {
				if err != nil || !started || stopped || !runner.container.Running {
					t.Fatalf("distinct approvals refused: effects=%v err=%v", runner.effects, err)
				}
			} else if err == nil || !demoted || runner.container.Running || started != late || stopped != late ||
				(late && !f.manager.gatewayRebindFailStop.Load()) {
				t.Fatalf("revocation accepted or withdrawal missing: effects=%v demoted=%t err=%v", runner.effects, demoted, err)
			}
			after, readErr := f.repository.HostingGatewayStartupSnapshot(ctx)
			if readErr != nil || !reflect.DeepEqual(before.Rebind, after.Rebind) ||
				after.ActiveRebindApprovalsAuthorizeServing() != (kind == "approved") || f.repository.CheckGatewayRebindFence(ctx) == nil {
				t.Fatalf("revocation lost ownership/history or fence: %v", readErr)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
		})
	}
}

func TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence(t *testing.T) {
	f, request, selection, runner := gatewayRebindCommittedServingFixture(t)
	ctx := context.Background()
	before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	driver := f.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	if _, err := driver.selectExact(ctx, *selection.State); err == nil {
		t.Fatal("ordinary selection accepted an active SQL fence")
	}
	for _, alter := range []func(*gatewayRebindPhysicalReconcileRequest){
		func(r *gatewayRebindPhysicalReconcileRequest) { r.SQLPhase = appaccess.GatewayRebindPrepared },
		func(r *gatewayRebindPhysicalReconcileRequest) { r.RollbackAllowed = true },
		func(r *gatewayRebindPhysicalReconcileRequest) { r.DatabaseCommitObserved = false },
		func(r *gatewayRebindPhysicalReconcileRequest) { r.Terminal = nil },
	} {
		wrong := request
		alter(&wrong)
		if _, err := f.manager.restoreGatewayRebindCommittedServingLocked(ctx, f.repository, wrong,
			func(context.Context) error { return nil }); err == nil || len(runner.effects) != 0 || f.manager.gatewayRebindAdmissionBlocked() {
			t.Fatal("invalid active recovery request reached physical effect")
		}
	}
	runner.lostStartAck = true
	guardCalls := 0
	guard := func(context.Context) error { guardCalls++; return nil }
	proof, err := f.manager.restoreGatewayRebindCommittedServingLocked(ctx, f.repository, request, guard)
	if err != nil || !runner.container.Running || len(runner.effects) != 1 || guardCalls < 4 ||
		!gatewayCurrentServingRestoreProofMatches(proof, runner.target, gatewayCurrentPhysicalStableServing) {
		t.Fatalf("active restart failed: effects=%v guardCalls=%d err=%v", runner.effects, guardCalls, err)
	}
	if _, err := f.manager.restoreGatewayRebindCommittedServingLocked(ctx, f.repository, request, guard); err != nil || len(runner.effects) != 1 {
		t.Fatalf("active restore not idempotent: %v", err)
	}
	after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	installed, loadErr := selection.Store.load()
	if err != nil || loadErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(*selection.State, installed) ||
		f.repository.CheckGatewayRebindFence(ctx) == nil || f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatal("physical restoration changed SQL/protected authority or released fence")
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
	for _, latch := range []*atomic.Bool{f.manager.gatewayRebindFailStop, f.manager.gatewayRebindCommitBarrier} {
		latch.Store(true)
		if _, err := f.manager.restoreGatewayRebindCommittedServingLocked(ctx, f.repository, request, guard); err == nil || !latch.Load() || len(runner.effects) != 1 {
			t.Fatal("active restore bypassed or cleared process latch")
		}
		latch.Store(false)
	}
	// While SQL is database_committed no ordinary route mutation is admitted.
	// Even a valid newer revision present before entry is not its commit baseline.
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, *selection.State))
	next := cloneGatewayCurrentRouteState(*selection.State)
	next.Revision++
	next.Digest, _ = gatewayCurrentRouteStateDigest(next)
	if err := selection.Store.saveNext(*selection.State, next); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.restoreGatewayRebindCommittedServingLocked(ctx, f.repository, request, guard); err == nil ||
		runner.container.Running || !f.manager.gatewayRebindFailStop.Load() || len(runner.effects) != 1 ||
		f.repository.CheckGatewayRebindFence(ctx) == nil {
		t.Fatalf("changed pre-entry baseline authorized restart: effects=%v err=%v", runner.effects, err)
	}
}

func TestGatewayRebindCommittedServingRestoreRefusesBoundaryDrift(t *testing.T) {
	for _, kind := range []string{"runtime before start", "runtime after start", "protected before start", "attempt after start", "SQL event before start", "protected after start"} {
		t.Run(kind, func(t *testing.T) {
			f, request, selection, runner := gatewayRebindCommittedServingFixture(t)
			ctx := context.Background()
			checks, drift := 0, false
			repository := gatewayCurrentServingRestoreRepository{Repository: f.repository, mutate: func(snapshot *appaccess.HostingGatewayStartupSnapshot) {
				if drift && (kind == "runtime before start" || kind == "runtime after start") {
					snapshot.RuntimeComponents[0].State = "failed"
				}
				if drift && kind == "SQL event before start" {
					event := *snapshot.Rebind.CurrentDatabaseCommittedEvent
					event.Sequence++
					snapshot.Rebind.CurrentDatabaseCommittedEvent = &event
				}
			}}
			guard := func(context.Context) error {
				checks++
				if (kind == "runtime before start" || kind == "protected before start" || kind == "SQL event before start") && checks >= 6 {
					drift = true
				}
				if runner.container.Running {
					drift = true
				}
				if drift && (kind == "protected before start" || kind == "protected after start") {
					current, err := selection.Store.load()
					if err != nil {
						return err
					}
					if current.Revision == selection.State.Revision {
						next := cloneGatewayCurrentRouteState(current)
						next.Revision++
						next.Digest, _ = gatewayCurrentRouteStateDigest(next)
						return selection.Store.saveNext(current, next)
					}
				}
				if drift && kind == "attempt after start" {
					return errors.New("attempt authority changed")
				}
				return nil
			}
			_, err := f.manager.restoreGatewayRebindCommittedServingLocked(ctx, repository, request, guard)
			started := containsGatewayCurrentPhysicalEffect(runner.effects, []string{"container", "start", runner.target.Resources.FinalContainer.ID})
			mayLive := kind == "protected after start"
			wantStarted := kind == "runtime after start" || kind == "attempt after start" || mayLive
			if err == nil || !drift || started != wantStarted || runner.container.Running != mayLive || !f.manager.gatewayRebindFailStop.Load() || f.repository.CheckGatewayRebindFence(ctx) == nil {
				t.Fatalf("drift accepted: checks=%d effects=%v running=%t err=%v", checks, runner.effects, runner.container.Running, err)
			}
			if mayLive {
				var diagnostic *Error
				if !errors.As(err, &diagnostic) || !diagnostic.candidateMayBeLive {
					t.Fatal("changed protected state was not reported as potentially live")
				}
			}
			if wantStarted && !mayLive && !containsGatewayCurrentPhysicalEffect(runner.effects, []string{"container", "stop", "--time", "10", runner.target.Resources.FinalContainer.ID}) {
				t.Fatalf("post-start authority failure omitted exact-owner stop: %v", runner.effects)
			}
		})
	}
}
