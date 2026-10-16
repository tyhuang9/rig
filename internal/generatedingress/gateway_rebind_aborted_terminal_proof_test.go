package generatedingress

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindAbortTerminalReadOnlyRepository struct {
	*appaccess.Repository
	claimCalls      int
	transitionCalls int
}

func (r *gatewayRebindAbortTerminalReadOnlyRepository) ClaimGatewayRebindV2(ctx context.Context,
	proposal appaccess.GatewayRebindPreclaimProposalV2,
) (appaccess.GatewayRebindClaimV2, bool, error) {
	r.claimCalls++
	return r.Repository.ClaimGatewayRebindV2(ctx, proposal)
}

func (r *gatewayRebindAbortTerminalReadOnlyRepository) ApplyGatewayRebindTransition(ctx context.Context,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	r.transitionCalls++
	return r.Repository.ApplyGatewayRebindTransition(ctx, proof)
}

var _ GatewayRebindCommitRepository = (*gatewayRebindAbortTerminalReadOnlyRepository)(nil)

type gatewayRebindAbortedTerminalFixture struct {
	fixture    gatewayRebindTypedHandoverRuntimeFixture
	manager    *Manager
	repository *gatewayRebindAbortTerminalReadOnlyRepository
	request    GatewayRebindCommitRequest
	receipt    gatewayRebindTerminalReceiptV2
}

func newGatewayRebindAbortedTerminalFixture(t *testing.T) gatewayRebindAbortedTerminalFixture {
	t.Helper()
	ctx := context.Background()
	fixture, _, physicalRequest := newGatewayRebindFixedAbortRecoveryFixture(t, nil)
	if physicalRequest.Terminal == nil || physicalRequest.Terminal.RollbackProof == nil ||
		physicalRequest.Terminal.RollbackProof.PhysicalProof == nil {
		t.Fatal("fixed abort fixture did not retain an actual rollback receipt")
	}
	receipt := *physicalRequest.Terminal
	snapshot, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active == nil {
		t.Fatalf("fixed abort active SQL unavailable: snapshot=%#v error=%v", snapshot, err)
	}
	var result GatewayRebindCommitResult
	err = fixture.predecessor.manager.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
		var applyErr error
		result, applyErr = fixture.predecessor.manager.applyGatewayRebindRetainedRollbackLocked(ctx,
			fixture.predecessor.repository, snapshot, receipt)
		return fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Load(), applyErr
	})
	expectedAuthority := gatewayCurrentAuthority(receipt.Predecessor.Lineage)
	if err != nil || result.OperationID != fixture.attempt.Claim.Spec.OperationID ||
		result.InitialPhase != appaccess.GatewayRebindPrepared ||
		result.FinalPhase != appaccess.GatewayRebindRolledBack ||
		result.Disposition != appaccess.GatewayRebindDispositionAbort ||
		result.TerminalReceiptDigest != receipt.Digest ||
		result.SelectedCurrentAuthority != expectedAuthority || !result.FenceReleased {
		t.Fatalf("apply retained rollback result=%#v error=%v", result, err)
	}
	terminal, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || terminal.Active != nil || terminal.CurrentSource == nil ||
		*terminal.CurrentSource != expectedAuthority || terminal.DatabaseCommitObserved ||
		terminal.DatabaseCommittedEvent != nil || fixture.predecessor.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatalf("retained rollback did not reach unfenced terminal SQL: snapshot=%#v error=%v", terminal, err)
	}
	index := gatewayRebindCommitReplayHistoryIndex(t, terminal, fixture.attempt.Claim.Spec.OperationID)
	if index < 0 || terminal.History[index].Claim.V2 == nil ||
		terminal.History[index].Claim.V2.State != appaccess.GatewayRebindRolledBack ||
		terminal.History[index].Claim.V2.StateSequence != 2 {
		t.Fatalf("retained rollback terminal history=%#v", terminal.History)
	}
	request := gatewayRebindCommitRequestFromHistory(t, terminal.History[index])

	// Reopen both durable readers after the actual rollback transition. The
	// physical transport remains the only controlled dependency below.
	if err := fixture.predecessor.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(fixture.predecessor.manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := &gatewayRebindAbortTerminalReadOnlyRepository{Repository: appaccess.New(db)}
	options := fixture.predecessor.manager.options
	options.RebindFenceCheck = repository.CheckGatewayRebindFence
	options.RebindCurrentStateRepository = repository
	manager, err := newManager(fixture.runner, options)
	if err != nil {
		t.Fatal(err)
	}
	manager.gatewayRebindFailStop = &atomic.Bool{}
	manager.gatewayRebindCommitBarrier = &atomic.Bool{}

	runtime := fixture.runtime
	runtime.manager = manager
	runtime.stage.manager = manager
	runtime.repository = repository
	fixture.runtime = runtime
	fixture.predecessor.manager = manager
	fixture.predecessor.repository = repository.Repository
	fixture.predecessor.db = db

	reopened, err := repository.GatewayRebindRecoverySnapshot(ctx)
	history, historyErr := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	protected, receiptErr := gatewayRebindActiveTerminalV2(history, *reopened.History[index].Claim.V2)
	if err != nil || !reflect.DeepEqual(terminal, reopened) || historyErr != nil || receiptErr != nil ||
		protected == nil || !reflect.DeepEqual(receipt, *protected) {
		t.Fatalf("reopened rollback evidence changed: sql=%t errors=%v/%v/%v protected=%#v",
			reflect.DeepEqual(terminal, reopened), err, historyErr, receiptErr, protected)
	}
	return gatewayRebindAbortedTerminalFixture{fixture: fixture, manager: manager,
		repository: repository, request: request, receipt: receipt}
}

type gatewayRebindAbortTerminalProofObservation struct {
	reviveAfterFirstCurrent bool
	firstCurrentObserved    bool
	firstCurrentDigest      string
	currentProofSuccesses   int
	terminalStageReads      int
	hostProbeReads          int
	containerProbeReads     int
}

type gatewayRebindAbortTerminalProofRunner struct {
	base        *gatewayRebindTypedHandoverRuntimeRunner
	observation *gatewayRebindAbortTerminalProofObservation
}

func (r gatewayRebindAbortTerminalProofRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if r.observation.firstCurrentObserved && len(args) >= 3 && args[0] == "container" &&
		args[1] == "inspect" && args[len(args)-1] == r.base.intent.Identity.StageContainer {
		r.observation.terminalStageReads++
	}
	return gatewayRebindTerminalProofFixtureRunner{base: r.base}.Run(ctx, request)
}

func (f gatewayRebindAbortedTerminalFixture) proofDependencies(
	observation *gatewayRebindAbortTerminalProofObservation,
) (gatewayRebindCommitCurrentProofReader, gatewayRebindCommitTerminalPhysicalDriver,
	managerGatewayRebindCrossStoreDriver,
) {
	manager := f.manager
	manager.runner = gatewayRebindAbortTerminalProofRunner{base: f.fixture.runner, observation: observation}
	handover := f.fixture.runtime
	hostProbe := handover.hostProbe
	handover.hostProbe = func(ctx context.Context, address string, port uint16,
		host, path string,
	) gatewayV2HostProbeResult {
		observation.hostProbeReads++
		return hostProbe(ctx, address, port, host, path)
	}
	containerProbe := handover.containerProbe
	handover.containerProbe = func(ctx context.Context, id, address string, port uint16,
		host, challenge string,
	) bool {
		observation.containerProbeReads++
		return containerProbe(ctx, id, address, port, host, challenge)
	}
	runtime := managerGatewayCurrentPhysicalRuntime{manager: manager,
		hostProbe: handover.hostProbe, containerProbe: handover.containerProbe}
	physical := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: manager},
		runtime:                             runtime,
	}
	predecessor := func(ctx context.Context, selection gatewayCurrentSelection) (
		gatewayRebindAdmissionPredecessorProof, error,
	) {
		return manager.readGatewayRebindAdmissionPredecessorWithRuntimeLocked(ctx, selection,
			manager.inspectGatewayRebindDocker, handover, runtime)
	}
	current := func(ctx context.Context, repository GatewayRebindCommitRepository,
		snapshot appaccess.GatewayRebindRecoverySnapshot, liveSession func(context.Context) error,
	) (gatewayRebindCommitCurrentProof, error) {
		proof, err := manager.readGatewayRebindCommitCurrentProofWithPredecessorLocked(ctx, repository,
			snapshot, liveSession, predecessor)
		if err == nil && validSHA256(proof.Digest) {
			observation.currentProofSuccesses++
			if observation.currentProofSuccesses == 1 {
				observation.firstCurrentObserved = true
				observation.firstCurrentDigest = proof.Digest
				if observation.reviveAfterFirstCurrent {
					f.fixture.runner.stagePresent = true
					f.fixture.runner.stage.start()
					f.fixture.runner.stage.ownedContainers = []string{f.fixture.attempt.Intent.Identity.StageContainer}
				}
			}
		}
		return proof, err
	}
	driver := managerGatewayRebindCrossStoreDriver{manager: manager,
		stage: f.fixture.stageDriver, handover: handover}
	return current, physical, driver
}

type gatewayRebindAbortTerminalReadOnlyState struct {
	recovery    appaccess.GatewayRebindRecoverySnapshot
	startup     appaccess.HostingGatewayStartupSnapshot
	protected   gatewayHistorySnapshot
	effects     int
	barrier     bool
	failStop    bool
	claims      int
	transitions int
}

func (f gatewayRebindAbortedTerminalFixture) readOnlyState(t *testing.T) gatewayRebindAbortTerminalReadOnlyState {
	t.Helper()
	ctx := context.Background()
	recovery, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	startup, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	if f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("terminal rollback unexpectedly retained the SQL fence")
	}
	return gatewayRebindAbortTerminalReadOnlyState{recovery: recovery, startup: startup,
		protected: protected, effects: len(f.fixture.runner.effects),
		barrier:  f.manager.gatewayRebindCommitBarrierLatch().Load(),
		failStop: f.manager.gatewayRebindFailStopLatch().Load(),
		claims:   f.repository.claimCalls, transitions: f.repository.transitionCalls}
}

func (f gatewayRebindAbortedTerminalFixture) requireReadOnlyState(t *testing.T,
	before gatewayRebindAbortTerminalReadOnlyState,
) {
	t.Helper()
	after := f.readOnlyState(t)
	if !reflect.DeepEqual(before.recovery, after.recovery) || !reflect.DeepEqual(before.startup, after.startup) ||
		!sameGatewayHistorySnapshot(before.protected, after.protected) || before.effects != after.effects ||
		before.barrier != after.barrier || before.failStop != after.failStop ||
		before.claims != after.claims || before.transitions != after.transitions ||
		after.claims != 0 || after.transitions != 0 {
		t.Fatalf("terminal rollback proof mutated durable state: recovery=%t startup=%t protected=%t effects=%d/%d latches=%t/%t->%t/%t writes=%d/%d",
			reflect.DeepEqual(before.recovery, after.recovery), reflect.DeepEqual(before.startup, after.startup),
			sameGatewayHistorySnapshot(before.protected, after.protected), before.effects, after.effects,
			before.barrier, before.failStop, after.barrier, after.failStop, after.claims, after.transitions)
	}
}

func TestGatewayRebindAbortedTerminalProofCoreIsFreshAndReadOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		revive bool
	}{
		{name: "verified"},
		{name: "successor revived after first current proof", revive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGatewayRebindAbortedTerminalFixture(t)
			ctx := context.Background()
			before := f.readOnlyState(t)
			observation := &gatewayRebindAbortTerminalProofObservation{reviveAfterFirstCurrent: test.revive}
			readCurrent, physical, driver := f.proofDependencies(observation)
			var response GatewayRebindCommitResponse
			err := f.manager.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
				var proofErr error
				response, proofErr = f.manager.readGatewayRebindTerminalResponseWithProofLocked(ctx,
					f.repository, f.request, gatewayRebindTestAdministrator, func(context.Context) error { return nil },
					driver, false, readCurrent, physical)
				return false, proofErr
			})
			f.requireReadOnlyState(t, before)
			if !observation.firstCurrentObserved || !validSHA256(observation.firstCurrentDigest) ||
				observation.terminalStageReads == 0 || observation.hostProbeReads == 0 ||
				observation.containerProbeReads == 0 {
				t.Fatalf("actual proof boundaries were not observed: %+v", *observation)
			}
			if test.revive {
				if err == nil || response != (GatewayRebindCommitResponse{}) ||
					observation.currentProofSuccesses != 1 || !f.fixture.runner.stagePresent ||
					!f.fixture.runner.stage.container.Running ||
					len(f.fixture.runner.effects) != before.effects {
					t.Fatalf("revived successor was accepted or repaired: response=%#v observation=%+v present=%t running=%t effects=%d/%d error=%v",
						response, *observation, f.fixture.runner.stagePresent,
						f.fixture.runner.stage.container.Running, len(f.fixture.runner.effects), before.effects, err)
				}
				return
			}
			expectedAuthority := gatewayCurrentAuthority(f.receipt.Predecessor.Lineage)
			if err != nil || response.Created || response.RecoveryRequired || !response.FenceReleased ||
				response.Operation.OperationID != f.request.Proposal.OperationID ||
				response.Operation.StoredState != appaccess.GatewayRebindRolledBack ||
				response.Operation.StateSequence != 2 || response.Operation.DatabaseCommitObserved ||
				response.Operation.TerminalDisposition != appaccess.GatewayRebindDispositionAbort ||
				response.Operation.TerminalReceiptDigest != f.receipt.Digest ||
				response.SelectedCurrentAuthority == nil || *response.SelectedCurrentAuthority != expectedAuthority ||
				response.CurrentProof != GatewayRebindProofVerified ||
				response.TerminalProof != GatewayRebindProofVerified ||
				!validSHA256(response.CurrentAttestationDigest) ||
				!validSHA256(response.TerminalProofDigest) || observation.currentProofSuccesses != 2 ||
				before.recovery.Active != nil || before.recovery.CurrentSource == nil ||
				*before.recovery.CurrentSource != expectedAuthority {
				t.Fatalf("fresh aborted proof response=%#v observation=%+v error=%v", response, *observation, err)
			}
		})
	}
}

func TestGatewayRebindAbortedTerminalExportedWrapperRejectsMissingPredecessorInventory(t *testing.T) {
	f := newGatewayRebindAbortedTerminalFixture(t)
	ctx := context.Background()
	// The exported facade must reject the missing SQL-selected predecessor
	// during concrete Docker inventory, before host or container proof probes.
	f.fixture.runner.predecessor.FinalContainerFound = false
	before := f.readOnlyState(t)
	requestStart := len(f.fixture.runner.requests)
	response, commitErr := f.manager.CommitGatewayRebind(ctx, f.repository, f.request,
		gatewayRebindTestAdministrator, func(context.Context) error { return nil })
	f.requireReadOnlyState(t, before)
	probeCommands := 0
	for _, args := range f.fixture.runner.requests[requestStart:] {
		if len(args) >= 2 && args[0] == "container" && args[1] == "exec" {
			probeCommands++
		}
	}
	if commitErr == nil || response != (GatewayRebindCommitResponse{}) || probeCommands != 0 ||
		f.fixture.runner.predecessor.FinalContainerFound || len(f.fixture.runner.effects) != before.effects {
		t.Fatalf("exported retry with missing predecessor inventory response=%#v probes=%d present=%t effects=%d/%d error=%v",
			response, probeCommands, f.fixture.runner.predecessor.FinalContainerFound,
			len(f.fixture.runner.effects), before.effects, commitErr)
	}
	for _, args := range f.fixture.runner.requests[requestStart:] {
		if len(args) > 0 && strings.Contains(strings.Join(args, " "), gatewayV2ChallengePathPrefix) {
			t.Fatalf("missing predecessor inventory reached an external challenge probe: %v", args)
		}
	}
}

// TestGatewayRebindRollbackTerminalObserverBindsCheckpointRoutesDigest is a
// limited observer output-contract control. It uses the observer's explicit
// predecessor-reader seam after a real typed rollback has produced stable
// stage absence. It does not stand in for the SQL/protected-history acceptance
// exercised by TestGatewayRebindAbortedTerminalProofCoreIsFreshAndReadOnly.
func TestGatewayRebindRollbackTerminalObserverBindsCheckpointRoutesDigest(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	fixture.runner.failFinalCreate = true
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	physical, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("prepare observer contract rollback: result=%#v error=%v", physical, err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 {
		t.Fatalf("read observer contract history: intents=%d error=%v", len(history.IntentsV2), err)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(history.IntentsV2[0].Intent, history)
	if err != nil {
		t.Fatal(err)
	}
	predecessor, err := fixture.runtime.predecessor(context.Background(), attempt)
	if err != nil || !predecessor.Running || !validSHA256(predecessor.Routes) {
		t.Fatalf("read observer contract predecessor: predecessor=%#v error=%v", predecessor, err)
	}
	effects := len(fixture.runner.effects)

	currentFixture := newGatewayCurrentStateFixture(t)
	currentCheckpoint, _ := gatewayRebindAttemptTypedFixture(t, currentFixture)
	if !validGatewayRebindPredecessorCheckpoint(currentCheckpoint) || currentCheckpoint.CurrentState == nil ||
		currentCheckpoint.UpgradeState != nil {
		t.Fatal("current-checkpoint observer fixture is invalid")
	}

	for _, test := range []struct {
		name       string
		checkpoint gatewayRebindPredecessorCheckpoint
	}{
		{name: "gateway upgrade checkpoint", checkpoint: attempt.Checkpoint},
		{name: "gateway rebind checkpoint", checkpoint: currentCheckpoint},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := attempt
			candidate.Checkpoint = test.checkpoint
			reads := 0
			result, observeErr := fixture.runtime.observeGatewayRebindRollbackServing(context.Background(), candidate,
				func(context.Context) (gatewayRebindTypedPredecessorObservation, error) {
					reads++
					return predecessor, nil
				}, func(context.Context) error { return nil })
			expected, digestErr := gatewayRebindTypedCheckpointRoutesDigest(test.checkpoint)
			if observeErr != nil || digestErr != nil || result.Refuse || reads != 2 ||
				result.Observation.PredecessorRoutesDigest != expected || expected == predecessor.Routes ||
				!validSHA256(result.Observation.StageAbsenceDigest) || len(fixture.runner.effects) != effects {
				t.Fatalf("purpose-bound observer result=%#v raw=%s expected=%s reads=%d effects=%d/%d errors=%v/%v",
					result, predecessor.Routes, expected, reads, len(fixture.runner.effects), effects,
					observeErr, digestErr)
			}
		})
	}

	invalid := []struct {
		name       string
		checkpoint gatewayRebindPredecessorCheckpoint
	}{
		{name: "missing checkpoint union", checkpoint: func() gatewayRebindPredecessorCheckpoint {
			value := attempt.Checkpoint
			value.UpgradeState, value.CurrentState = nil, nil
			return value
		}()},
		{name: "mixed checkpoint union", checkpoint: func() gatewayRebindPredecessorCheckpoint {
			value := attempt.Checkpoint
			value.CurrentState = currentCheckpoint.CurrentState
			return value
		}()},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			candidate := attempt
			candidate.Checkpoint = test.checkpoint
			reads := 0
			result, observeErr := fixture.runtime.observeGatewayRebindRollbackServing(context.Background(), candidate,
				func(context.Context) (gatewayRebindTypedPredecessorObservation, error) {
					reads++
					return predecessor, nil
				}, func(context.Context) error { return nil })
			if observeErr != nil || !result.Refuse || result.Observation != (gatewayRebindRollbackServingObservation{}) ||
				reads != 2 || len(fixture.runner.effects) != effects {
				t.Fatalf("invalid checkpoint union escaped refusal: result=%#v reads=%d effects=%d/%d error=%v",
					result, reads, len(fixture.runner.effects), effects, observeErr)
			}
		})
	}

	for _, test := range []struct {
		name      string
		guard     gatewayRebindTypedEffectGuard
		wantReads int
	}{
		{name: "stopped predecessor", wantReads: 1},
		{name: "raw routes changed between reads", wantReads: 2},
		{name: "final guard refused", wantReads: 2,
			guard: func(context.Context) error { return appaccess.ErrApprovalRequired }},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			read := func(context.Context) (gatewayRebindTypedPredecessorObservation, error) {
				reads++
				value := predecessor
				switch test.name {
				case "stopped predecessor":
					value.Running = false
				case "raw routes changed between reads":
					if reads == 2 {
						value.Routes = strings.Repeat("f", 64)
					}
				}
				return value, nil
			}
			guard := test.guard
			if guard == nil {
				guard = func(context.Context) error { return nil }
			}
			result, observeErr := fixture.runtime.observeGatewayRebindRollbackServing(context.Background(), attempt,
				read, guard)
			if observeErr != nil || !result.Refuse || result.Observation != (gatewayRebindRollbackServingObservation{}) ||
				reads != test.wantReads || len(fixture.runner.effects) != effects {
				t.Fatalf("observer refusal changed: result=%#v reads=%d effects=%d/%d error=%v",
					result, reads, len(fixture.runner.effects), effects, observeErr)
			}
		})
	}
}
