package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindStrictReturnedClaimRepository struct {
	gatewayRebindAdmissionRepository
	mutate func(*appaccess.GatewayRebindClaimV2, *bool)
}

func (r gatewayRebindStrictReturnedClaimRepository) ClaimGatewayRebindV2(ctx context.Context,
	proposal appaccess.GatewayRebindPreclaimProposalV2,
) (appaccess.GatewayRebindClaimV2, bool, error) {
	claim, created, err := r.gatewayRebindAdmissionRepository.ClaimGatewayRebindV2(ctx, proposal)
	if err == nil && r.mutate != nil {
		r.mutate(&claim, &created)
	}
	return claim, created, err
}

func TestGatewayRebindStrictClaimRejectsWellFormedNoncanonicalDigests(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*appaccess.GatewayRebindClaimV2)
	}{
		{name: "claim request", mutate: func(value *appaccess.GatewayRebindClaimV2) {
			value.RequestDigest = strings.Repeat("f", 64)
		}},
		{name: "successor request", mutate: func(value *appaccess.GatewayRebindClaimV2) {
			value.SuccessorProfileRequestDigest = strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, reader := gatewayRebindStrictNativeFixture(t)
			repository := gatewayRebindStrictReturnedClaimRepository{
				gatewayRebindAdmissionRepository: fixture.repository,
				mutate:                           func(claim *appaccess.GatewayRebindClaimV2, _ *bool) { test.mutate(claim) },
			}
			hookCalls := 0
			fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
				hookCalls++
				return nil
			}
			var strict gatewayRebindStrictClaim
			_, claimErr := withCrossStoreFixtureEffectLocks(t, fixture.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					var err error
					strict, err = fixture.manager.claimGatewayRebindStrictLocked(context.Background(), repository,
						input, reader, func(context.Context) error { return nil })
					return gatewayRebindPreparedAttempt{}, err
				})
			if claimErr == nil || hookCalls != 0 || !reflect.DeepEqual(strict.accepted, gatewayRebindAcceptedClaim{}) {
				t.Fatalf("noncanonical returned claim gained provenance: strict=%#v hooks=%d error=%v",
					strict, hookCalls, claimErr)
			}
		})
	}
}

func TestGatewayRebindStrictClaimKeepsCreatedFalseOutOfContinuation(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    appaccess.GatewayRebindState
		sequence int64
	}{
		{name: "progressed", state: appaccess.GatewayRebindSuccessorReady, sequence: 2},
		{name: "terminal", state: appaccess.GatewayRebindCommitted, sequence: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, reader := gatewayRebindStrictNativeFixture(t)
			repository := gatewayRebindStrictReturnedClaimRepository{
				gatewayRebindAdmissionRepository: fixture.repository,
				mutate: func(claim *appaccess.GatewayRebindClaimV2, created *bool) {
					*created = false
					claim.State = test.state
					claim.StateSequence = test.sequence
					claim.UpdatedAt = claim.UpdatedAt.Add(time.Second)
				},
			}
			beforeFiles, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			var strict gatewayRebindStrictClaim
			_, continueErr := withCrossStoreFixtureEffectLocks(t, fixture.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					var claimErr error
					strict, claimErr = fixture.manager.claimGatewayRebindStrictLocked(context.Background(), repository,
						input, reader, func(context.Context) error { return nil })
					if claimErr != nil {
						return gatewayRebindPreparedAttempt{}, claimErr
					}
					return fixture.manager.continueGatewayRebindStrictLocked(context.Background(), repository,
						input, strict, reader, func(context.Context) error { return nil })
				})
			afterFiles, filesErr := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			if continueErr == nil || filesErr != nil || strict.accepted.created ||
				strict.accepted.claim.State != test.state || strict.accepted.claim.StateSequence != test.sequence ||
				!validGatewayRebindAcceptedClaim(strict.accepted, strict.candidate) ||
				!sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
				t.Fatalf("created=false claim entered continuation: strict=%#v files=%v error=%v",
					strict, filesErr, continueErr)
			}
		})
	}
}

func TestGatewayRebindStrictClaimPreservesValidatedProvenanceAcrossPostClaimHookError(t *testing.T) {
	fixture, input, reader := gatewayRebindStrictNativeFixture(t)
	sentinel := errors.New("injected postclaim failure")
	fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
		return sentinel
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	var strict gatewayRebindStrictClaim
	_, claimErr := withCrossStoreFixtureEffectLocks(t, fixture.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			var err error
			strict, err = fixture.manager.claimGatewayRebindStrictLocked(context.Background(), fixture.repository,
				input, reader, func(context.Context) error { return nil })
			return gatewayRebindPreparedAttempt{}, err
		})
	afterFiles, filesErr := readGatewayHistorySnapshotMode(fixture.manager.store, true)
	if claimErr == nil || errors.Is(claimErr, sentinel) || filesErr != nil ||
		!strict.accepted.created || !validGatewayRebindAcceptedClaim(strict.accepted, strict.candidate) ||
		!sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
		t.Fatalf("postclaim provenance missing or protected state changed: strict=%#v files=%v error=%v",
			strict, filesErr, claimErr)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || !reflect.DeepEqual(*snapshot.Active.Claim.V2, strict.accepted.claim) {
		t.Fatalf("validated provenance does not match retained A0 claim: active=%#v error=%v", snapshot.Active, err)
	}
}

func TestAbortAcceptedGatewayRebindPreparedWithoutIntentRefusesA0AndRevokedSession(t *testing.T) {
	for _, test := range []struct {
		name              string
		installCheckpoint bool
		revoke            bool
		revokeAfterProof  bool
		mutateAccepted    bool
		wantProofCalls    int
		wantError         bool
	}{
		{name: "a0_without_checkpoint"},
		{name: "revoked_after_checkpoint", installCheckpoint: true, revoke: true, wantError: true},
		{name: "revoked_after_no_effect_proof", installCheckpoint: true, revokeAfterProof: true,
			wantProofCalls: 1, wantError: true},
		{name: "changed_accepted_claim", installCheckpoint: true, mutateAccepted: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, reader := gatewayRebindStrictNativeFixture(t)
			driver := &gatewayRebindBoundedPhysicalDriver{t: t}
			var strict gatewayRebindStrictClaim
			_, err := withCrossStoreFixtureEffectLocks(t, fixture.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					var claimErr error
					strict, claimErr = fixture.manager.claimGatewayRebindStrictLocked(context.Background(),
						fixture.repository, input, reader, func(context.Context) error { return nil })
					if claimErr != nil {
						return gatewayRebindPreparedAttempt{}, claimErr
					}
					if test.installCheckpoint {
						fixture.manager.gatewayRebindAfterPreparedCheckpoint = func(context.Context) error {
							return errors.New("injected checkpoint boundary")
						}
						_, sessionPassed, prepareErr := fixture.manager.continueGatewayRebindStrictAdmissionLocked(
							context.Background(), fixture.repository, input, strict, reader,
							func(context.Context) error { return nil })
						if prepareErr == nil || !sessionPassed {
							t.Fatalf("checkpoint boundary not reached: session=%t error=%v", sessionPassed, prepareErr)
						}
					}
					if test.mutateAccepted {
						strict.accepted.claim.RequestDigest = strings.Repeat("f", 64)
					}
					guard := func(context.Context) error { return nil }
					if test.revoke {
						guard = func(context.Context) error { return appaccess.ErrApprovalRequired }
					} else if test.revokeAfterProof {
						guardCalls := 0
						guard = func(context.Context) error {
							guardCalls++
							if guardCalls == 2 {
								return appaccess.ErrApprovalRequired
							}
							return nil
						}
					}
					_, handled, abortErr := fixture.manager.abortAcceptedGatewayRebindPreparedWithoutIntentLocked(
						context.Background(), fixture.repository, driver, strict, guard)
					if handled || (abortErr != nil) != test.wantError {
						t.Fatalf("ineligible accepted claim aborted: handled=%t error=%v", handled, abortErr)
					}
					return gatewayRebindPreparedAttempt{}, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if snapshotErr != nil || historyErr != nil || snapshot.Active == nil ||
				driver.abortCalls != test.wantProofCalls ||
				len(history.TerminalsV2) != 0 {
				t.Fatalf("refused abort changed authority: active=%#v calls=%d terminals=%d errors=%v/%v",
					snapshot.Active, driver.abortCalls, len(history.TerminalsV2), snapshotErr, historyErr)
			}
		})
	}
}

func TestAbortAcceptedGatewayRebindPreparedWithoutIntentPublishesExactNoEffectTerminal(t *testing.T) {
	fixture, input, reader := gatewayRebindStrictNativeFixture(t)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayRebindBoundedPhysicalDriver{t: t}
	var result GatewayRebindCommitResult
	_, err := withCrossStoreFixtureEffectLocks(t, fixture.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			strict, claimErr := fixture.manager.claimGatewayRebindStrictLocked(context.Background(),
				fixture.repository, input, reader, func(context.Context) error { return nil })
			if claimErr != nil {
				return gatewayRebindPreparedAttempt{}, claimErr
			}
			fixture.manager.gatewayRebindAfterPreparedCheckpoint = func(context.Context) error {
				return errors.New("injected checkpoint boundary")
			}
			_, sessionPassed, prepareErr := fixture.manager.continueGatewayRebindStrictAdmissionLocked(
				context.Background(), fixture.repository, input, strict, reader,
				func(context.Context) error { return nil })
			if prepareErr == nil || !sessionPassed {
				t.Fatalf("checkpoint boundary not reached: session=%t error=%v", sessionPassed, prepareErr)
			}
			var handled bool
			var abortErr error
			result, handled, abortErr = fixture.manager.abortAcceptedGatewayRebindPreparedWithoutIntentLocked(
				context.Background(), fixture.repository, driver, strict, func(context.Context) error { return nil })
			if abortErr != nil || !handled {
				return gatewayRebindPreparedAttempt{}, errors.New("accepted no-effect abort was not handled")
			}
			return gatewayRebindPreparedAttempt{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if snapshotErr != nil || historyErr != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
		result.FinalPhase != appaccess.GatewayRebindRolledBack || !result.FenceReleased || driver.abortCalls != 1 ||
		!fixture.manager.gatewayRebindCommitBarrierLatch().Load() || fixture.manager.gatewayRebindFailStopLatch().Load() ||
		len(history.Checkpoints) != 1 || len(history.IntentsV2) != 0 || len(history.Progress) != 0 ||
		len(history.TerminalsV2) != 1 || history.TerminalsV2[0].Receipt.NoEffectProof == nil ||
		history.TerminalsV2[0].Receipt.Digest != result.TerminalReceiptDigest {
		t.Fatalf("no-effect abort result=%#v snapshot=%#v calls=%d history=%d/%d/%d/%d errors=%v/%v",
			result, snapshot, driver.abortCalls, len(history.Checkpoints), len(history.IntentsV2),
			len(history.Progress), len(history.TerminalsV2), snapshotErr, historyErr)
	}
}

func TestObserveGatewayRebindRollbackServingUsesRetainedAttemptWithoutEffects(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	fixture.runner.failFinalCreate = true
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	physical, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("prepare retained rollback: result=%#v error=%v", physical, err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 {
		t.Fatalf("read retained rollback history: intents=%d error=%v", len(history.IntentsV2), err)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(history.IntentsV2[0].Intent, history)
	if err != nil || !reflect.DeepEqual(attempt.Claim, fixture.attempt.Claim) {
		t.Fatalf("rebuild retained rollback attempt: attempt=%#v error=%v", attempt, err)
	}

	effects := len(fixture.runner.effects)
	reads := 0
	result, err := fixture.runtime.observeGatewayRebindRollbackServing(context.Background(), attempt,
		func(ctx context.Context) (gatewayRebindTypedPredecessorObservation, error) {
			reads++
			return fixture.runtime.predecessor(ctx, attempt)
		}, func(context.Context) error { return nil })
	if err != nil || result.Refuse || reads != 2 ||
		!validContainerID(result.Observation.PredecessorContainerID) ||
		!validSHA256(result.Observation.PredecessorObservationDigest) ||
		!validSHA256(result.Observation.PredecessorRoutesDigest) ||
		!validSHA256(result.Observation.StageAbsenceDigest) || len(fixture.runner.effects) != effects {
		t.Fatalf("pure rollback observation=%#v reads=%d effects=%d/%d error=%v",
			result, reads, len(fixture.runner.effects), effects, err)
	}

	sentinel := errors.New("retained predecessor unreadable")
	result, err = fixture.runtime.observeGatewayRebindRollbackServing(context.Background(), attempt,
		func(context.Context) (gatewayRebindTypedPredecessorObservation, error) {
			return gatewayRebindTypedPredecessorObservation{}, sentinel
		},
		func(context.Context) error { return nil })
	if !errors.Is(err, sentinel) || result.Refuse || len(fixture.runner.effects) != effects {
		t.Fatalf("reader error was converted to refusal: result=%#v effects=%d/%d error=%v",
			result, len(fixture.runner.effects), effects, err)
	}
}

func TestCommitGatewayRebindClassifiesActiveReplayWithoutAdmission(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*GatewayRebindCommitRequest, *string)
		wantActive bool
		wantExact  bool
	}{
		{name: "identical", wantExact: true},
		{name: "different_actor", mutate: func(_ *GatewayRebindCommitRequest, actor *string) {
			*actor = "78eb1572-d684-4f0a-ab41-613aaf559e93"
		}},
		{name: "different_review", mutate: func(request *GatewayRebindCommitRequest, _ *string) {
			request.ProposalDigest = differentGatewayRebindReviewDigest(request.ProposalDigest)
		}},
		{name: "different_operation", wantActive: true, mutate: func(request *GatewayRebindCommitRequest, _ *string) {
			request.Proposal.OperationID = "4bc14f05-b5d1-465f-b99e-4b6167206f91"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, _ := newGatewayRebindCoordinatorFixture(t)
			review, err := newGatewayRebindCommitReview(input.Inspection)
			if err != nil {
				t.Fatal(err)
			}
			claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(),
				appaccess.GatewayRebindPreclaimProposalV2{Spec: input.Inspection.Spec,
					RebindApproval: input.RebindApproval, ConfigureApproval: input.ConfigureApproval,
					Roster: input.Inspection.Roster, RuntimeHeads: input.Inspection.RuntimeHeads})
			if err != nil || !created {
				t.Fatalf("create active claim: created=%t error=%v", created, err)
			}
			request := GatewayRebindCommitRequest{Version: review.Version,
				Proposal: GatewayRebindProposalInput{OperationID: claim.Spec.OperationID,
					SuccessorProfileRevisionID:     claim.Spec.SuccessorProfileRevisionID,
					SuccessorProfileRevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
					SuccessorProfileOperationID:    claim.Spec.SuccessorProfileOperationID,
					SuccessorProfile:               claim.Spec.SuccessorProfile},
				ProposalDigest: review.ProposalDigest, ConfigureApprovalDigest: review.ConfigureApprovalDigest,
				RebindApprovalDigest: review.RebindApprovalDigest}
			actor := gatewayRebindTestAdministrator
			if test.mutate != nil {
				test.mutate(&request, &actor)
			}
			before, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			guardCalls := 0
			response, commitErr := fixture.manager.CommitGatewayRebind(context.Background(), fixture.repository,
				request, actor, func(context.Context) error { guardCalls++; return nil })
			after, readErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			switch {
			case readErr != nil || !reflect.DeepEqual(before, after):
				t.Fatalf("active replay mutated SQL: error=%v changed=%t", readErr, !reflect.DeepEqual(before, after))
			case test.wantExact && (commitErr != nil || !response.RecoveryRequired || response.Created ||
				response.Operation.OperationID != claim.Spec.OperationID || guardCalls != 2):
				t.Fatalf("identical active replay response=%#v guards=%d error=%v", response, guardCalls, commitErr)
			case test.wantActive && !errors.Is(commitErr, appaccess.ErrGatewayRebindActive):
				t.Fatalf("different operation did not preserve active conflict: %v", commitErr)
			case !test.wantExact && !test.wantActive && !errors.Is(commitErr, appaccess.ErrConflict):
				t.Fatalf("changed active retry was not a conflict: %v", commitErr)
			}
		})
	}
}

type gatewayRebindCommittedTerminalFixture struct {
	fixture gatewayRebindTypedHandoverRuntimeFixture
	request GatewayRebindCommitRequest
	receipt gatewayRebindTerminalReceiptV2
}

func newGatewayRebindCommittedTerminalFixture(t *testing.T) gatewayRebindCommittedTerminalFixture {
	t.Helper()
	ctx := context.Background()
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	physical, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionCommit {
		t.Fatalf("complete typed terminal physical result=%#v error=%v", physical, err)
	}
	receipt, err := fixture.predecessor.manager.installGatewayRebindTerminalForPhysicalLocked(
		ctx, fixture.attempt, physical)
	if err != nil {
		t.Fatal(err)
	}
	request.Mode, request.Terminal = gatewayRebindPhysicalReconcileForwardOnly, &receipt
	attesting := gatewayRebindForwardBoundedAttestationDriver{driver}
	err = fixture.predecessor.manager.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
		_, recoverErr := fixture.predecessor.manager.recoverGatewayRebindCommitTerminalLocked(ctx,
			fixture.predecessor.repository, attesting, fixture.attempt, receipt)
		return fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Load(), recoverErr
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active != nil {
		t.Fatalf("terminal SQL unavailable: snapshot=%#v error=%v", snapshot, err)
	}
	index := gatewayRebindCommitReplayHistoryIndex(t, snapshot, fixture.attempt.Claim.Spec.OperationID)
	if index < 0 {
		t.Fatal("terminal SQL history missing")
	}
	return gatewayRebindCommittedTerminalFixture{fixture: fixture,
		request: gatewayRebindCommitRequestFromHistory(t, snapshot.History[index]), receipt: receipt}
}

func gatewayRebindCommitRequestFromHistory(t *testing.T,
	history appaccess.GatewayRebindHistoryEntry,
) GatewayRebindCommitRequest {
	t.Helper()
	if history.Claim.V2 == nil {
		t.Fatal("typed claim missing")
	}
	claim := *history.Claim.V2
	inspection := GatewayRebindProposalInspection{Spec: claim.Spec,
		Roster:                      append([]appaccess.GatewayRebindRosterEntryV2{}, history.RosterV2...),
		RuntimeHeads:                append([]appaccess.GatewayRebindRuntimeHead{}, history.RuntimeHeads...),
		SpecDigest:                  claim.RebindApproval.SpecDigest,
		SuccessorProfileSpecDigest:  claim.ConfigureApproval.SpecDigest,
		ProtectedGeneration:         claim.Spec.SuccessorProtectedGeneration,
		PredecessorCheckpointDigest: claim.Spec.Predecessor.PredecessorCheckpointDigest,
		SourceStateVersion:          claim.Spec.Predecessor.SourceStateVersion,
		SourceStateRevision:         claim.Spec.Predecessor.SourceStateRevision,
		SourceStateDigest:           claim.Spec.Predecessor.SourceStateDigest}
	review, err := newGatewayRebindCommitReview(inspection)
	if err != nil {
		t.Fatal(err)
	}
	return GatewayRebindCommitRequest{Version: review.Version,
		Proposal: GatewayRebindProposalInput{OperationID: claim.Spec.OperationID,
			SuccessorProfileRevisionID:     claim.Spec.SuccessorProfileRevisionID,
			SuccessorProfileRevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
			SuccessorProfileOperationID:    claim.Spec.SuccessorProfileOperationID,
			SuccessorProfile:               claim.Spec.SuccessorProfile},
		ProposalDigest: review.ProposalDigest, ConfigureApprovalDigest: review.ConfigureApprovalDigest,
		RebindApprovalDigest: review.RebindApprovalDigest}
}

type gatewayRebindTerminalProofFixtureRunner struct {
	base *gatewayRebindTypedHandoverRuntimeRunner
}

func (r gatewayRebindTerminalProofFixtureRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) == 7 && reflect.DeepEqual(args[:6], []string{
		"container", "ls", "--all", "--quiet", "--no-trunc", "--filter",
	}) && (args[6] == "volume="+r.base.intent.Identity.ConfigVolume ||
		args[6] == "volume="+r.base.intent.Identity.DataVolume ||
		args[6] == "volume="+r.base.predecessorState.Identity.ConfigVolume ||
		args[6] == "volume="+r.base.predecessorState.Identity.DataVolume) {
		r.base.requests = append(r.base.requests, append([]string(nil), args...))
		volume := strings.TrimPrefix(args[6], "volume=")
		var ids []string
		for _, candidate := range []struct {
			present   bool
			container caddyInspection
		}{{r.base.stagePresent, r.base.stage.container}, {r.base.finalPresent, r.base.final},
			{r.base.predecessor.FinalContainerFound, r.base.predecessor.FinalContainer}} {
			if !candidate.present {
				continue
			}
			for _, mount := range candidate.container.Mounts {
				if mount.Type == "volume" && mount.Name == volume {
					ids = append(ids, normalizeID(candidate.container.ID))
					break
				}
			}
		}
		sort.Strings(ids)
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(ids, "\n"))}, nil
	}
	return r.base.Run(ctx, request)
}

func (f gatewayRebindCommittedTerminalFixture) proofDependencies() (
	gatewayRebindCommitCurrentProofReader, gatewayRebindCommitTerminalPhysicalDriver,
) {
	manager := f.fixture.predecessor.manager
	manager.runner = gatewayRebindTerminalProofFixtureRunner{base: f.fixture.runner}
	// The handover fixture keeps the complete config-volume archive because the
	// handover validates every retained file. A current-state Docker `cp` of the
	// exact active file returns a single-entry archive instead.
	f.fixture.runner.stage.archiveOverride = gatewayRebindSingleConfigArchive(f.fixture.runner.t,
		f.fixture.attempt.Intent.Identity.ActiveConfigFilename, f.fixture.runner.stage.activeBody)
	handover := f.fixture.runtime
	probe := handover.hostProbe
	handover.hostProbe = func(ctx context.Context, address string, port uint16,
		host, path string,
	) gatewayV2HostProbeResult {
		// Model retired LAN listeners from the same container state used by
		// the handover fixture. A revived predecessor becomes reachable again.
		if address != "127.0.0.1" && f.fixture.runner.listenerAbsent(ctx, address, port) {
			return gatewayV2HostProbeResult{}
		}
		return probe(ctx, address, port, host, path)
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
		return manager.readGatewayRebindCommitCurrentProofWithPredecessorLocked(ctx, repository,
			snapshot, liveSession, predecessor)
	}
	return current, physical
}

func TestGatewayRebindCommittedTerminalProofCoreIsFreshAndReadOnly(t *testing.T) {
	for _, mode := range []string{"verified", "successor stopped", "predecessor revived during retirement observation"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayRebindCommittedTerminalFixture(t)
			ctx := context.Background()
			revivalObservations := 0
			if mode == "successor stopped" {
				f.fixture.runner.final.Running = false
			}
			if mode == "predecessor revived during retirement observation" {
				observe := f.fixture.predecessor.manager.gatewayRebindV2NetworkObserver
				f.fixture.predecessor.manager.gatewayRebindV2NetworkObserver = func(ctx context.Context,
					claim appaccess.GatewayRebindClaimV2,
				) (gatewayRebindSuccessorNetworkObservation, error) {
					revivalObservations++
					value, err := observe(ctx, claim)
					if revivalObservations == 1 {
						f.fixture.runner.startPredecessor()
					}
					return value, err
				}
			}
			before, err := f.fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			files, err := readGatewayHistorySnapshotMode(f.fixture.predecessor.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			effects := len(f.fixture.runner.effects)
			readCurrent, physical := f.proofDependencies()
			var response GatewayRebindCommitResponse
			err = f.fixture.predecessor.manager.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
				var proofErr error
				response, proofErr = f.fixture.predecessor.manager.readGatewayRebindTerminalResponseWithProofLocked(ctx,
					f.fixture.predecessor.repository, f.request, gatewayRebindTestAdministrator,
					func(context.Context) error { return nil }, managerGatewayRebindCrossStoreDriver{
						manager: f.fixture.predecessor.manager,
					}, false, readCurrent, physical)
				return false, proofErr
			})
			after, readErr := f.fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			afterFiles, filesErr := readGatewayHistorySnapshotMode(f.fixture.predecessor.manager.store, true)
			if readErr != nil || filesErr != nil || !reflect.DeepEqual(before, after) ||
				!sameGatewayHistorySnapshot(files, afterFiles) || len(f.fixture.runner.effects) != effects {
				t.Fatal("terminal proof changed SQL, protected state, or Docker")
			}
			if mode == "predecessor revived during retirement observation" && revivalObservations == 0 {
				t.Fatal("predecessor revival injection was not observed")
			}
			if mode == "verified" {
				if err != nil || response.RecoveryRequired || response.Created || !response.FenceReleased ||
					response.TerminalProof != GatewayRebindProofVerified ||
					response.CurrentProof != GatewayRebindProofVerified ||
					!validSHA256(response.TerminalProofDigest) ||
					!validSHA256(response.CurrentAttestationDigest) {
					t.Fatalf("fresh committed proof response=%#v error=%v", response, err)
				}
			} else if err == nil || response != (GatewayRebindCommitResponse{}) {
				t.Fatalf("invalid fresh terminal state received success: mode=%s response=%#v error=%v",
					mode, response, err)
			}
		})
	}
}

func TestGatewayRebindCommittedTerminalExportedWrapperRejectsMissingCurrentInventory(t *testing.T) {
	f := newGatewayRebindCommittedTerminalFixture(t)
	ctx := context.Background()
	// Fail a concrete Docker read before the production host probes are reached.
	// The managed proof-core tests above cover the complete controlled probe path;
	// this exported wrapper control establishes fail-closed production wiring.
	f.fixture.runner.finalPresent = false
	before, err := f.fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(f.fixture.predecessor.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	effects := len(f.fixture.runner.effects)
	response, commitErr := f.fixture.predecessor.manager.CommitGatewayRebind(ctx,
		f.fixture.predecessor.repository, f.request, gatewayRebindTestAdministrator,
		func(context.Context) error { return nil })
	after, readErr := f.fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	afterFiles, filesErr := readGatewayHistorySnapshotMode(f.fixture.predecessor.manager.store, true)
	if commitErr == nil || response != (GatewayRebindCommitResponse{}) || readErr != nil || filesErr != nil ||
		!reflect.DeepEqual(before, after) || !sameGatewayHistorySnapshot(files, afterFiles) ||
		len(f.fixture.runner.effects) != effects {
		t.Fatalf("exported retry with missing current inventory response=%#v effects=%d/%d changed=%t/%t error=%v read=%v/%v",
			response, len(f.fixture.runner.effects), effects, !reflect.DeepEqual(before, after),
			!sameGatewayHistorySnapshot(files, afterFiles), commitErr, readErr, filesErr)
	}
}

func TestGatewayRebindCommittedTerminalClassifiesLaterSameLineageRevisionAsSuperseded(t *testing.T) {
	f := newGatewayRebindCommittedTerminalFixture(t)
	ctx := context.Background()
	snapshot, err := f.fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := readGatewayRebindCommitReplay(ctx, snapshot, f.request, gatewayRebindTestAdministrator)
	history, historyErr := f.fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	current, currentErr := f.fixture.predecessor.manager.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil || !found || historyErr != nil || currentErr != nil {
		t.Fatalf("terminal selection fixture: found=%t errors=%v/%v/%v", found, err, historyErr, currentErr)
	}
	exact, err := f.fixture.predecessor.manager.gatewayRebindTerminalSelectsCurrentLocked(
		projection, history, f.receipt, current)
	if err != nil || !exact || current.State == nil {
		t.Fatalf("exact commit baseline not selected: exact=%t error=%v", exact, err)
	}
	later := current
	state := cloneGatewayCurrentRouteState(*current.State)
	state.Revision++
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		t.Fatalf("later current revision invalid: %v", err)
	}
	later.State = &state
	exact, err = f.fixture.predecessor.manager.gatewayRebindTerminalSelectsCurrentLocked(
		projection, history, f.receipt, later)
	if err != nil || exact {
		t.Fatalf("later same-lineage revision was treated as old terminal current: exact=%t error=%v", exact, err)
	}
}

func TestGatewayRebindHistoricalTerminalWithNewActiveReturnsUnavailableProofs(t *testing.T) {
	f := newGatewayRebindCommittedTerminalFixture(t)
	ctx := context.Background()
	input := GatewayRebindProposalInput{OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: f.fixture.attempt.Intent.SuccessorProfile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: f.fixture.attempt.Claim.Spec.SuccessorProfile}
	review, err := f.fixture.predecessor.manager.InspectGatewayRebindCommitReview(ctx,
		f.fixture.predecessor.repository, input)
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := f.fixture.predecessor.repository.ClaimGatewayRebindV2(ctx,
		appaccess.GatewayRebindPreclaimProposalV2{Spec: review.Inspection.Spec,
			RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
				SpecDigest: review.RebindApprovalDigest, ActorID: gatewayRebindTestAdministrator},
			ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
				SpecDigest: review.ConfigureApprovalDigest, ActorID: gatewayRebindTestAdministrator},
			Roster: review.Inspection.Roster, RuntimeHeads: review.Inspection.RuntimeHeads})
	if err != nil || !created {
		t.Fatalf("create newer active claim: created=%t error=%v", created, err)
	}
	beforeEffects := len(f.fixture.runner.effects)
	response, err := f.fixture.predecessor.manager.CommitGatewayRebind(ctx,
		f.fixture.predecessor.repository, f.request, gatewayRebindTestAdministrator,
		func(context.Context) error { return nil })
	if err != nil || response.RecoveryRequired || response.Created || !response.FenceReleased ||
		response.TerminalProof != GatewayRebindProofUnavailable ||
		response.CurrentProof != GatewayRebindProofUnavailable || len(f.fixture.runner.effects) != beforeEffects ||
		!errors.Is(f.fixture.predecessor.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
		t.Fatalf("historical terminal under newer fence response=%#v effects=%d/%d error=%v",
			response, len(f.fixture.runner.effects), beforeEffects, err)
	}
}
