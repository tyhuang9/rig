package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindReconcileTestRepository struct {
	*appaccess.Repository
	audited       bool
	driftBefore   bool
	driftAfter    bool
	authorizeCall int
}

func (r *gatewayRebindReconcileTestRepository) HostingGatewayStartupSnapshot(ctx context.Context) (
	appaccess.HostingGatewayStartupSnapshot, error,
) {
	value, err := r.Repository.HostingGatewayStartupSnapshot(ctx)
	if err == nil && (r.driftBefore || r.driftAfter && r.audited) && value.Rebind.CurrentSource != nil {
		current := *value.Rebind.CurrentSource
		current.ProfileSpecDigest = strings.Repeat("f", 64)
		value.Rebind.CurrentSource = &current
	}
	return value, err
}

func (r *gatewayRebindReconcileTestRepository) AuthorizeAndAuditGatewayRebindReconcile(ctx context.Context,
	input appaccess.GatewayRebindReconcileAuthorizationInput,
) (appaccess.GatewayRebindReconcileAuthorization, error) {
	r.authorizeCall++
	value, err := r.Repository.AuthorizeAndAuditGatewayRebindReconcile(ctx, input)
	if err == nil {
		r.audited = true
	}
	return value, err
}

type gatewayRebindReconcileFixtureRunner struct {
	base          gatewayRebindTerminalProofFixtureRunner
	activeSource  string
	activeArchive []byte
}

func (r gatewayRebindReconcileFixtureRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" &&
		args[2] == r.activeSource && args[3] == "-" {
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.activeArchive...)}, nil
	}
	return r.base.Run(ctx, request)
}

func gatewayRebindReconcileControlledCurrentReader(t *testing.T,
	fixture gatewayRebindTypedHandoverRuntimeFixture,
) gatewayRebindRecoveryCurrentProofReader {
	t.Helper()
	manager := fixture.predecessor.manager
	activeArchive := gatewayRebindSingleConfigArchive(t,
		fixture.attempt.Intent.Identity.ActiveConfigFilename, fixture.runner.stage.activeBody)
	t.Cleanup(func() { clear(activeArchive) })
	manager.runner = gatewayRebindReconcileFixtureRunner{
		base:          gatewayRebindTerminalProofFixtureRunner{base: fixture.runner},
		activeSource:  fixture.runner.finalID + ":/config/" + fixture.attempt.Intent.Identity.ActiveConfigFilename,
		activeArchive: activeArchive,
	}
	handover := fixture.runtime
	probe := handover.hostProbe
	handover.hostProbe = func(ctx context.Context, address string, port uint16,
		host, path string,
	) gatewayV2HostProbeResult {
		if address != "127.0.0.1" && fixture.runner.listenerAbsent(ctx, address, port) {
			return gatewayV2HostProbeResult{}
		}
		return probe(ctx, address, port, host, path)
	}
	runtime := managerGatewayCurrentPhysicalRuntime{manager: manager,
		hostProbe: handover.hostProbe, containerProbe: handover.containerProbe}
	manager.gatewayCurrentPhysicalDriver = managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: manager},
		runtime:                             runtime,
	}
	predecessor := func(ctx context.Context, selection gatewayCurrentSelection) (
		gatewayRebindAdmissionPredecessorProof, error,
	) {
		return manager.readGatewayRebindAdmissionPredecessorWithRuntimeLocked(ctx, selection,
			manager.inspectGatewayRebindDocker, handover, runtime)
	}
	return func(ctx context.Context, repository GatewayRebindRecoveryReadRepository,
		snapshot appaccess.GatewayRebindRecoverySnapshot, liveSession func(context.Context) error,
	) (gatewayRebindCommitCurrentProof, error) {
		operationID := ""
		if snapshot.Active != nil && snapshot.Active.Claim.V2 != nil {
			operationID = snapshot.Active.Claim.V2.Spec.OperationID
		}
		return manager.readGatewayRebindCurrentProofWithPredecessorLocked(ctx, repository,
			snapshot, liveSession, predecessor, operationID)
	}
}

func gatewayRebindReconcileForwardReview(t *testing.T, fixture gatewayRebindTypedHandoverRuntimeFixture,
	repository GatewayRebindRecoveryReadRepository, readCurrent gatewayRebindRecoveryCurrentProofReader,
) GatewayRebindRecoveryReview {
	return gatewayRebindReconcileReview(t, fixture, repository, readCurrent,
		appaccess.GatewayRebindPrepared, false, appaccess.GatewayRebindRecoveryForwardOnly,
		appaccess.GatewayRebindDispositionCommit)
}

func gatewayRebindReconcileReview(t *testing.T, fixture gatewayRebindTypedHandoverRuntimeFixture,
	repository GatewayRebindRecoveryReadRepository, readCurrent gatewayRebindRecoveryCurrentProofReader,
	wantState appaccess.GatewayRebindState, wantDatabaseCommit bool,
	wantPolicy appaccess.GatewayRebindRecoveryPolicy,
	wantProtectedDisposition appaccess.GatewayRebindTerminalDisposition,
) GatewayRebindRecoveryReview {
	t.Helper()
	var review GatewayRebindRecoveryReview
	_, err := withCrossStoreFixtureEffectLocks(t, fixture.predecessor.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			var reviewErr error
			review, reviewErr = fixture.predecessor.manager.inspectGatewayRebindRecoveryReviewWithCurrentProofLocked(
				context.Background(), repository, fixture.attempt.Claim.Spec.OperationID,
				gatewayRebindTestAdministrator, func(context.Context) error { return nil }, readCurrent)
			return fixture.attempt, reviewErr
		})
	if err != nil || len(review.Status.AllowedActions) != 1 || review.Status.Operation.StoredState != wantState ||
		review.Status.Operation.DatabaseCommitObserved != wantDatabaseCommit ||
		review.Status.Operation.TerminalDisposition != appaccess.GatewayRebindDispositionNone ||
		review.AuthorizationReview.Snapshot.Policy != wantPolicy ||
		review.AuthorizationReview.Snapshot.Trusted.Protected.FixedDisposition != wantProtectedDisposition {
		t.Fatalf("build recovery review: version=%d availability=%s policy=%s phase=%s database_commit=%t disposition=%s protected_disposition=%s error=%v",
			review.Version, review.Status.Availability, review.Status.RecoveryPolicy,
			review.Status.Operation.StoredState, review.Status.Operation.DatabaseCommitObserved,
			review.Status.Operation.TerminalDisposition,
			review.AuthorizationReview.Snapshot.Trusted.Protected.FixedDisposition, err)
	}
	return review
}

func TestGatewayRebindReconcileWithRuntimeUsesFreshAuditAndPassedRepository(t *testing.T) {
	for _, mode := range []string{"success", "fixed abort", "pre-DB unresolved commit", "post-DB unresolved commit",
		"invocation repository drift", "post-audit drift", "post-audit session"} {
		t.Run(mode, func(t *testing.T) {
			fixture, driver, physicalRequest := newGatewayRebindTypedForwardOnlyFixture(t)
			wantReviewState := appaccess.GatewayRebindPrepared
			wantReviewDatabaseCommit := false
			wantPolicy := appaccess.GatewayRebindRecoveryForwardOnly
			wantDisposition := appaccess.GatewayRebindDispositionCommit
			wantTerminalState := appaccess.GatewayRebindCommitted
			if mode == "fixed abort" {
				fixture, driver, physicalRequest = newGatewayRebindFixedAbortRecoveryFixture(t, nil)
				wantPolicy = appaccess.GatewayRebindRecoveryRollbackOnly
				wantDisposition = appaccess.GatewayRebindDispositionAbort
				wantTerminalState = appaccess.GatewayRebindRolledBack
			} else if mode == "pre-DB unresolved commit" || mode == "post-DB unresolved commit" {
				if mode == "post-DB unresolved commit" {
					advanceGatewayRebindTypedForwardFixture(t, fixture, physicalRequest,
						appaccess.GatewayRebindSuccessorReady)
					advanceGatewayRebindTypedForwardFixture(t, fixture, physicalRequest,
						appaccess.GatewayRebindDatabaseCommitted)
					wantReviewDatabaseCommit = true
				}
				snapshot := gatewayRebindRequestedUnresolvedSnapshot(t, fixture, physicalRequest.Terminal)
				wantReviewState = appaccess.GatewayRebindUnresolved
				if snapshot.Phase != wantReviewState ||
					snapshot.DatabaseCommitObserved != wantReviewDatabaseCommit ||
					(snapshot.DatabaseCommittedEvent != nil) != wantReviewDatabaseCommit {
					t.Fatalf("prepare unresolved recovery phase=%s database_commit=%t event=%t",
						snapshot.Phase, snapshot.DatabaseCommitObserved, snapshot.DatabaseCommittedEvent != nil)
				}
			}
			manager := fixture.predecessor.manager
			readCurrent := gatewayRebindReconcileControlledCurrentReader(t, fixture)
			review := gatewayRebindReconcileReview(t, fixture, fixture.predecessor.repository, readCurrent,
				wantReviewState, wantReviewDatabaseCommit, wantPolicy, wantDisposition)
			request := GatewayRebindReconcileRequest{
				Version: GatewayRebindReconcileVersion, OperationID: fixture.attempt.Claim.Spec.OperationID,
				RecoveryDigest:         review.AuthorizationReview.RecoveryDigest,
				RecoveryApprovalDigest: review.AuthorizationReview.RecoveryApprovalDigest,
			}
			repository := &gatewayRebindReconcileTestRepository{Repository: fixture.predecessor.repository,
				driftBefore: mode == "invocation repository drift", driftAfter: mode == "post-audit drift"}
			before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			files, err := readGatewayHistorySnapshotMode(manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			fixture.runner.effects = nil
			manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (
				appaccess.GatewayRebindRecoverySnapshot, error,
			) {
				return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("options repository must not be used")
			})
			manager.options.RebindFenceCheck = func(context.Context) error {
				return errors.New("options fence must not be used")
			}
			guardCalls := 0
			guard := func(context.Context) error {
				guardCalls++
				if mode == "post-audit session" && repository.audited {
					return errors.New("session revoked")
				}
				return nil
			}
			response, reconcileErr := manager.reconcileGatewayRebindWithRuntime(context.Background(), repository,
				request, gatewayRebindTestAdministrator, guard, driver, readCurrent)
			after, readErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
			afterFiles, filesErr := readGatewayHistorySnapshotMode(manager.store, true)
			if mode == "success" || mode == "fixed abort" ||
				mode == "pre-DB unresolved commit" || mode == "post-DB unresolved commit" {
				if reconcileErr != nil || ValidateGatewayRebindReconcileResponse(response, request,
					gatewayRebindTestAdministrator) != nil || response.Status.Operation.StoredState != wantTerminalState ||
					response.Status.Operation.TerminalDisposition != wantDisposition ||
					response.Status.Operation.DatabaseCommitObserved != (wantDisposition == appaccess.GatewayRebindDispositionCommit) ||
					response.Status.TerminalProof != GatewayRebindProofVerified ||
					response.Status.CurrentProof != GatewayRebindProofVerified || repository.authorizeCall != 1 ||
					readErr != nil || filesErr != nil || len(fixture.runner.effects) != 0 || after.Active != nil {
					t.Fatalf("reconcile version=%d phase=%s disposition=%s terminal_proof=%s current_proof=%s audit=%d effects=%d active=%t read_error=%v files_error=%v error=%v",
						response.Version, response.Status.Operation.StoredState,
						response.Status.Operation.TerminalDisposition, response.Status.TerminalProof,
						response.Status.CurrentProof, repository.authorizeCall, len(fixture.runner.effects),
						after.Active != nil, readErr, filesErr, reconcileErr)
				}
			} else {
				wantAudit := 1
				if mode == "invocation repository drift" {
					wantAudit = 0
				}
				if reconcileErr == nil || !reflect.DeepEqual(response, GatewayRebindReconcileResponse{}) ||
					repository.authorizeCall != wantAudit || readErr != nil || filesErr != nil ||
					!reflect.DeepEqual(before, after) || !sameGatewayHistorySnapshot(files, afterFiles) ||
					len(fixture.runner.effects) != 0 {
					t.Fatalf("mode=%s response=%#v audit=%d effects=%v SQLsame=%t filessame=%t error=%v",
						mode, response, repository.authorizeCall, fixture.runner.effects,
						reflect.DeepEqual(before, after), sameGatewayHistorySnapshot(files, afterFiles), reconcileErr)
				}
			}
			if guardCalls == 0 {
				t.Fatal("live session was not checked")
			}
		})
	}
}
