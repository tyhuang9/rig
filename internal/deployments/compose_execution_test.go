package deployments

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/jobs"
)

type composeExecutionFixture struct {
	guarded    *guardedRepositoryFixture
	deployment Deployment
	claim      deploymentexecution.Claim
	releaseID  string
	configID   string
	request    ComposeExecutionRequest
}

func newComposeExecutionFixture(t *testing.T) *composeExecutionFixture {
	return newComposeExecutionFixtureWithFindings(t, nil)
}

func newComposeExecutionFixtureWithFindings(t *testing.T, findings []Finding) *composeExecutionFixture {
	t.Helper()
	base := newRepositoryFixture(t)
	guarded := newGuardedRepositoryFixture(t, base)
	guarded.sequence++
	created, err := guarded.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: guarded.appID, IdempotencyKey: "compose-execution", RequestedBy: guarded.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create deployment job: created=%t error=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := base.repository.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	configID := uuid.NewString()
	if _, err := base.repository.db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,0)`, configID, guarded.appID, 1, "compose-execution:"+configID, guarded.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	releaseID := strings.Repeat("e", 32)
	if _, err := base.repository.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number) VALUES(?,?,'ready','{}',?,'ready',?,?,1)`, releaseID, guarded.appID, stamp, strings.Repeat("a", 64), configID); err != nil {
		t.Fatal(err)
	}
	deployment, _, err := guarded.repository.GetOrCreateByJob(context.Background(), guarded.appID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = guarded.repository.Initialize(context.Background(), guarded.appID, deployment.ID, releaseID, configID, 1)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, guarded.appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := ComposeExecutionRequest{Claim: claim, DeploymentID: deployment.ID, ReleaseID: releaseID, ConfigurationRevisionID: configID, ConfigurationRevisionNumber: 1, Findings: append([]Finding(nil), findings...)}
	gate := func() error {
		return guarded.repository.Gate(guarded.context, GateRequest{Claim: claim, DeploymentID: deployment.ID, Findings: append([]Finding(nil), findings...)})
	}
	requiresApproval := false
	for _, current := range findings {
		requiresApproval = requiresApproval || current.Disposition == "approval_required"
	}
	if err := gate(); requiresApproval {
		if !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("initial approval gate: %v", err)
		}
		for _, current := range findings {
			if current.Disposition == "approval_required" {
				if _, _, err := guarded.repository.Grant(context.Background(), guarded.appID, guarded.actorID, current.Fingerprint); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := gate(); err != nil {
			t.Fatalf("approved gate: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	return &composeExecutionFixture{guarded: guarded, deployment: deployment, claim: claim, releaseID: releaseID, configID: configID, request: request}
}

func (f *composeExecutionFixture) status(t *testing.T) Deployment {
	t.Helper()
	value, err := f.guarded.repository.Get(context.Background(), f.guarded.appID, f.deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestComposeExecutionUsesApplyingOnlyCommandAndFreshFixedObservations(t *testing.T) {
	fixture := newComposeExecutionFixture(t)
	if !fixture.guarded.repository.ExecutionConfigured() || New(fixture.guarded.repository.db).ExecutionConfigured() {
		t.Fatal("execution configuration classification is incorrect")
	}
	execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	copyExecution := *execution
	if err := execution.CheckBeforeUp(); err != nil {
		t.Fatalf("check before up: %v", err)
	}
	applying := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim)
	if err := execution.RecordSucceeded(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("applying completion error=%v", err)
	}
	if after := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim); !reflect.DeepEqual(applying, after) {
		t.Fatalf("applying completion refusal changed durable state: before=%#v after=%#v", applying, after)
	}
	if err := execution.RecordWaitingHealth(); err != nil {
		t.Fatalf("record waiting health: %v", err)
	}
	waiting := fixture.status(t)
	if waiting.Status != WaitingHealth || waiting.FinishedAt != (time.Time{}) {
		t.Fatalf("waiting deployment=%#v", waiting)
	}
	if err := execution.CheckBeforeUp(); !errors.Is(err, deploymentexecution.ErrStageInvalid) {
		t.Fatalf("waiting-health up check=%v", err)
	}
	if err := execution.RecordWaitingHealth(); err != nil {
		t.Fatalf("idempotent waiting-health observation: %v", err)
	}
	if err := execution.RecordSucceeded(); err != nil {
		t.Fatalf("record succeeded: %v", err)
	}
	if succeeded := fixture.status(t); succeeded.Status != Succeeded || succeeded.FinishedAt.IsZero() {
		t.Fatalf("succeeded deployment=%#v", succeeded)
	}
	execution.Leave()
	copyExecution.Leave()
	if err := execution.RecordSucceeded(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("left execution error=%v", err)
	}
	if err := copyExecution.RecordSucceeded(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("copied left execution error=%v", err)
	}
}

func TestComposeExecutionPreservesCancellationWithProvenanceRefusal(t *testing.T) {
	fixture := newComposeExecutionFixture(t)
	ctx, cancel := context.WithCancel(fixture.guarded.context)
	request := fixture.request
	request.ReleaseID = strings.Repeat("f", 32)
	execution, err := fixture.guarded.repository.PinComposeExecution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	fixture.guarded.repository.afterComposeIdentityRead = cancel
	before := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim)
	err = execution.CheckBeforeUp()
	if !errors.Is(err, ErrInvalidDeployment) || !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) || errors.Is(err, deploymentexecution.ErrUnavailable) {
		t.Fatalf("combined provenance/cancellation error=%v", err)
	}
	if after := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim); !reflect.DeepEqual(before, after) {
		t.Fatalf("provenance refusal changed durable state: before=%#v after=%#v", before, after)
	}
}

func TestComposeExecutionPolicyDriftAndCompletionStageRefuseWithoutMutation(t *testing.T) {
	fixture := newComposeExecutionFixture(t)
	execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	if _, err := fixture.guarded.repository.db.Exec(`UPDATE deployments SET status='waiting_health' WHERE id=?`, fixture.deployment.ID); err != nil {
		t.Fatal(err)
	}
	if err := execution.RecordSucceeded(); err != nil {
		t.Fatal(err)
	}
	if err := execution.RecordSucceeded(); !errors.Is(err, deploymentexecution.ErrStageInvalid) {
		t.Fatalf("terminal observation error=%v", err)
	}
	if got := fixture.status(t).Status; got != Succeeded {
		t.Fatalf("status=%s", got)
	}
}

func TestComposeExecutionPinsCopiedFindingsAndRefusesFreshPolicyDriftWithoutMutation(t *testing.T) {
	t.Run("caller finding mutation does not change the pinned policy", func(t *testing.T) {
		allowed := finding("external_bind", "service:web:/srv/data:/data", "allowed")
		fixture := newComposeExecutionFixtureWithFindings(t, []Finding{allowed})
		request := fixture.request
		execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		request.Findings[0].Scope = "caller-mutated"
		if err := execution.CheckBeforeUp(); err != nil {
			t.Fatalf("copied finding check: %v", err)
		}
	})

	for _, boundary := range []struct {
		name    string
		prepare func(*testing.T, *ComposeExecution)
		check   func(*ComposeExecution) error
	}{
		{name: "before up", check: (*ComposeExecution).CheckBeforeUp},
		{name: "waiting-health observation", check: (*ComposeExecution).RecordWaitingHealth},
		{name: "succeeded observation", prepare: func(t *testing.T, execution *ComposeExecution) {
			if err := execution.RecordWaitingHealth(); err != nil {
				t.Fatal(err)
			}
		}, check: (*ComposeExecution).RecordSucceeded},
	} {
		t.Run("persisted finding drift is read only/"+boundary.name, func(t *testing.T) {
			allowed := finding("external_bind", "service:web:/srv/data:/data", "allowed")
			fixture := newComposeExecutionFixtureWithFindings(t, []Finding{allowed})
			execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Leave()
			if boundary.prepare != nil {
				boundary.prepare(t, execution)
			}
			if _, err := fixture.guarded.repository.db.Exec(`DROP TRIGGER deployment_finding_immutable`); err != nil {
				t.Fatal(err)
			}
			changed := finding("external_bind", "service:web:/different:/data", "allowed")
			if _, err := fixture.guarded.repository.db.Exec(`UPDATE deployment_policy_findings SET scope=?,fingerprint=? WHERE deployment_id=?`, changed.Scope, changed.Fingerprint, fixture.deployment.ID); err != nil {
				t.Fatal(err)
			}
			before := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim)
			if err := boundary.check(execution); !errors.Is(err, ErrInvalidDeployment) || errors.Is(err, deploymentexecution.ErrUnavailable) {
				t.Fatalf("policy drift error=%v", err)
			}
			if after := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim); !reflect.DeepEqual(before, after) {
				t.Fatalf("policy drift refusal changed durable state: before=%#v after=%#v", before, after)
			}
		})
	}

	t.Run("approval revocation is observed after Gate", func(t *testing.T) {
		required := finding("privileged", "service:web", "approval_required")
		fixture := newComposeExecutionFixtureWithFindings(t, []Finding{required})
		execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, err := fixture.guarded.repository.db.Exec(`DROP TRIGGER runtime_approval_revoke_active`); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := fixture.guarded.repository.db.Exec(`UPDATE runtime_approvals SET revoked_by=?,revoked_at=? WHERE app_id=? AND fingerprint=?`, fixture.guarded.actorID, stamp, fixture.guarded.appID, required.Fingerprint); err != nil {
			t.Fatal(err)
		}
		before := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim)
		if err := execution.CheckBeforeUp(); !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("revoked approval error=%v", err)
		}
		if after := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim); !reflect.DeepEqual(before, after) {
			t.Fatalf("approval refusal changed durable state: before=%#v after=%#v", before, after)
		}
	})
}

func TestComposeExecutionFailsClosedForZeroUnconfiguredAndExpiredValues(t *testing.T) {
	var nilExecution *ComposeExecution
	if err := nilExecution.CheckBeforeUp(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("nil execution check=%v", err)
	}
	nilExecution.Leave()
	zero := &ComposeExecution{}
	if err := zero.RecordWaitingHealth(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("zero execution observation=%v", err)
	}

	fixture := newComposeExecutionFixture(t)
	if execution, err := New(fixture.guarded.repository.db).PinComposeExecution(fixture.guarded.context, fixture.request); execution != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("unconfigured repository execution=%#v error=%v", execution, err)
	}
	if execution, err := (*Repository)(nil).PinComposeExecution(fixture.guarded.context, fixture.request); execution != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("nil repository execution=%#v error=%v", execution, err)
	}
	if execution, err := fixture.guarded.repository.PinComposeExecution(context.Background(), fixture.request); execution != nil || !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("missing scope execution=%#v error=%v", execution, err)
	}
	if execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, ComposeExecutionRequest{}); execution != nil || !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("zero request execution=%#v error=%v", execution, err)
	}
}

func TestComposeExecutionLeaveWaitsForActiveCheckAndInvalidatesCopies(t *testing.T) {
	fixture := newComposeExecutionFixture(t)
	execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	copyExecution := *execution
	entered, unblock := make(chan struct{}), make(chan struct{})
	var unblockOnce sync.Once
	releaseCheck := func() { unblockOnce.Do(func() { close(unblock) }) }
	defer releaseCheck()
	fixture.guarded.repository.afterComposeIdentityRead = func() { close(entered); <-unblock }
	checkDone := make(chan error, 1)
	go func() { checkDone <- execution.CheckBeforeUp() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active check did not reach identity read")
	}
	leaveDone := make(chan struct{})
	go func() { copyExecution.Leave(); close(leaveDone) }()
	closingDeadline := time.Now().Add(5 * time.Second)
	for {
		execution.state.mu.Lock()
		closing := execution.state.closing
		execution.state.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(closingDeadline) {
			t.Fatal("Leave did not enter closing state")
		}
		runtime.Gosched()
	}
	select {
	case <-leaveDone:
		t.Fatal("Leave returned while the check was active")
	default:
	}
	if err := execution.RecordWaitingHealth(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("closing execution accepted a new observation: %v", err)
	}
	releaseCheck()
	select {
	case err := <-checkDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active check did not finish")
	}
	select {
	case <-leaveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Leave did not finish after the active check")
	}
	if err := execution.CheckBeforeUp(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("original copy remained live: %v", err)
	}
}

func TestComposeExecutionObservationFaultsRollbackAtBothStages(t *testing.T) {
	for _, stage := range []struct {
		name   string
		target Status
		apply  func(*ComposeExecution) error
	}{
		{name: "waiting health", target: WaitingHealth, apply: (*ComposeExecution).RecordWaitingHealth},
		{name: "succeeded", target: Succeeded, apply: (*ComposeExecution).RecordSucceeded},
	} {
		for _, fault := range []struct {
			name    string
			install func(*testing.T, *composeExecutionFixture, Status)
			want    error
		}{
			{name: "SQL update failure", want: deploymentexecution.ErrUnavailable, install: installComposeExecutionAbortFault},
			{name: "zero affected row", want: ErrInvalidTransition, install: installComposeExecutionIgnoreFault},
			{name: "commit failure", want: deploymentexecution.ErrUnavailable, install: installComposeExecutionCommitFault},
		} {
			t.Run(stage.name+"/"+fault.name, func(t *testing.T) {
				fixture := newComposeExecutionFixture(t)
				execution, err := fixture.guarded.repository.PinComposeExecution(fixture.guarded.context, fixture.request)
				if err != nil {
					t.Fatal(err)
				}
				defer execution.Leave()
				if stage.target == Succeeded {
					if err := execution.RecordWaitingHealth(); err != nil {
						t.Fatal(err)
					}
				}
				before := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim)
				fault.install(t, fixture, stage.target)
				if err := stage.apply(execution); !errors.Is(err, fault.want) {
					t.Fatalf("observation error=%v want=%v", err, fault.want)
				}
				if after := fixture.guarded.snapshot(t, fixture.deployment, fixture.claim); !reflect.DeepEqual(before, after) {
					t.Fatalf("fault changed durable state: before=%#v after=%#v", before, after)
				}
				if stage.target == Succeeded && before.deployment.Status != WaitingHealth {
					t.Fatalf("first committed observation was not retained: %#v", before.deployment)
				}
			})
		}
	}
}

func installComposeExecutionAbortFault(t *testing.T, fixture *composeExecutionFixture, target Status) {
	t.Helper()
	statement := fmt.Sprintf(`CREATE TRIGGER compose_execution_abort BEFORE UPDATE OF status ON deployments WHEN NEW.status='%s' BEGIN SELECT RAISE(ABORT,'compose observation fault'); END`, target)
	if _, err := fixture.guarded.repository.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func installComposeExecutionIgnoreFault(t *testing.T, fixture *composeExecutionFixture, target Status) {
	t.Helper()
	statement := fmt.Sprintf(`CREATE TRIGGER compose_execution_ignore BEFORE UPDATE OF status ON deployments WHEN NEW.status='%s' BEGIN SELECT RAISE(IGNORE); END`, target)
	if _, err := fixture.guarded.repository.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func installComposeExecutionCommitFault(t *testing.T, fixture *composeExecutionFixture, target Status) {
	t.Helper()
	if _, err := fixture.guarded.repository.db.Exec(`CREATE TABLE compose_execution_commit_fault(id TEXT PRIMARY KEY,missing_app TEXT NOT NULL,FOREIGN KEY(missing_app) REFERENCES applications(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	statement := fmt.Sprintf(`CREATE TRIGGER compose_execution_commit AFTER UPDATE OF status ON deployments WHEN NEW.status='%s' BEGIN INSERT INTO compose_execution_commit_fault(id,missing_app) VALUES(NEW.id,'missing-app'); END`, target)
	if _, err := fixture.guarded.repository.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}
