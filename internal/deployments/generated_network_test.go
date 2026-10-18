package deployments

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type generatedNetworkFixture struct {
	guarded    *guardedRepositoryFixture
	request    GeneratedNetworkRequest
	claim      deploymentexecution.Claim
	binding    deploymentbinding.Binding
	state      *generatedruntimestate.Repository
	deployment Deployment
}

func newGeneratedNetworkFixture(t *testing.T, phase generatedruntimestate.Phase, migration bool) *generatedNetworkFixture {
	t.Helper()
	base := newRepositoryFixture(t)
	guarded := newGuardedRepositoryFixture(t, base)
	created, err := guarded.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: guarded.appID, IdempotencyKey: "generated-network-" + string(phase), RequestedBy: guarded.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create generated deployment: created=%t error=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := base.repository.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	configID, planID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := base.repository.db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,0)`, configID, guarded.appID, 1, "generated-network:"+configID, guarded.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	migrationDigest := ""
	if migration {
		migrationDigest = strings.Repeat("9", 64)
	}
	if _, err := base.repository.db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,0,?,?,?,'accepted',?,?)`, planID, guarded.appID, "generated-network:"+planID, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), migrationDigest, guarded.actorID, stamp, guarded.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if migration {
		if _, err := base.repository.db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,?,?)`, planID, guarded.appID, guarded.actorID, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := base.repository.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1,?,1)`, releaseID, guarded.appID, stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), configID, planID); err != nil {
		t.Fatal(err)
	}
	deployment, _, err := guarded.repository.GetOrCreateByJob(context.Background(), guarded.appID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = guarded.repository.InitializeRuntime(context.Background(), guarded.appID, deployment.ID, releaseID, configID, 1, RuntimeGeneratedNode, planID, 1)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, guarded.appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := guarded.repository.Gate(guarded.context, GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatal(err)
	}
	state := generatedruntimestate.New(base.repository.db)
	runtimeDeployment, wasCreated, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: guarded.appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"},
	})
	if err != nil || !wasCreated {
		t.Fatalf("begin generated state: created=%t error=%v", wasCreated, err)
	}
	for _, next := range generatedNetworkPhasePath(phase) {
		diagnostic := generatedruntimestate.DiagnosticCode("")
		if next == generatedruntimestate.PhaseFailed {
			diagnostic = generatedruntimestate.DiagnosticInternalError
		} else if next == generatedruntimestate.PhaseCancelled {
			diagnostic = generatedruntimestate.DiagnosticCancelled
		}
		runtimeDeployment, err = state.Advance(context.Background(), guarded.appID, deployment.ID, runtimeDeployment.Phase, next, diagnostic)
		if err != nil {
			t.Fatalf("advance generated state to %s: %v", next, err)
		}
	}
	return &generatedNetworkFixture{
		guarded: guarded, state: state, deployment: deployment, claim: claim, binding: created.Binding,
		request: GeneratedNetworkRequest{Claim: claim, DeploymentID: deployment.ID, ReleaseID: releaseID,
			ConfigurationRevisionID: configID, ConfigurationRevisionNumber: 1,
			DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1},
	}
}

func generatedNetworkPhasePath(phase generatedruntimestate.Phase) []generatedruntimestate.Phase {
	switch phase {
	case generatedruntimestate.PhasePreflight:
		return nil
	case generatedruntimestate.PhaseBuilding:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding}
	case generatedruntimestate.PhaseMigrating:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseMigrating}
	case generatedruntimestate.PhaseStartingCandidate:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate}
	case generatedruntimestate.PhaseWaitingHealth:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, phase}
	case generatedruntimestate.PhaseSwitchingRoute:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, phase}
	case generatedruntimestate.PhaseDraining:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute, phase}
	case generatedruntimestate.PhaseSucceeded:
		return []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, phase}
	case generatedruntimestate.PhaseFailed, generatedruntimestate.PhaseCancelled:
		return []generatedruntimestate.Phase{phase}
	default:
		return nil
	}
}

func TestGeneratedNetworkExecutionAcceptsOnlyFixedRuntimePhases(t *testing.T) {
	for _, test := range []struct {
		phase     generatedruntimestate.Phase
		migration bool
		allowed   bool
	}{
		{generatedruntimestate.PhaseBuilding, false, true},
		{generatedruntimestate.PhaseMigrating, true, true},
		{generatedruntimestate.PhaseStartingCandidate, false, true},
		{generatedruntimestate.PhasePreflight, false, false},
		{generatedruntimestate.PhaseWaitingHealth, false, false},
		{generatedruntimestate.PhaseSwitchingRoute, false, false},
		{generatedruntimestate.PhaseDraining, false, false},
		{generatedruntimestate.PhaseSucceeded, false, false},
		{generatedruntimestate.PhaseFailed, false, false},
		{generatedruntimestate.PhaseCancelled, false, false},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			fixture := newGeneratedNetworkFixture(t, test.phase, test.migration)
			execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Leave()
			err = execution.CheckBeforeCreate()
			if test.allowed && err != nil {
				t.Fatal(err)
			}
			if !test.allowed && !errors.Is(err, ErrInvalidDeployment) {
				t.Fatalf("phase %s refusal=%v", test.phase, err)
			}
		})
	}
}

func TestGeneratedNetworkExecutionRequiresExactImmutableTupleAndFullPolicy(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	mutations := []struct {
		name   string
		mutate func(*GeneratedNetworkRequest)
	}{
		{"release", func(request *GeneratedNetworkRequest) { request.ReleaseID = uuid.NewString() }},
		{"configuration", func(request *GeneratedNetworkRequest) { request.ConfigurationRevisionID = uuid.NewString() }},
		{"configuration number", func(request *GeneratedNetworkRequest) { request.ConfigurationRevisionNumber++ }},
		{"plan", func(request *GeneratedNetworkRequest) { request.DeploymentPlanRevisionID = uuid.NewString() }},
		{"plan number", func(request *GeneratedNetworkRequest) { request.DeploymentPlanRevisionNumber++ }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request
			test.mutate(&request)
			execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, request)
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Leave()
			if err := execution.CheckBeforeCreate(); !errors.Is(err, ErrInvalidDeployment) && !errors.Is(err, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("tuple refusal=%v", err)
			}
		})
	}
	fingerprint := findingFingerprint("v1", "external_bind", "component:api:/tmp")
	if _, err := fixture.guarded.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?, 'v1','external_bind','component:api:/tmp',?,'allowed',?)`, uuid.NewString(), fixture.deployment.ID, fingerprint, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	if err := execution.CheckObservation(); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("extra retained policy evidence accepted: %v", err)
	}
}

func TestGeneratedNetworkExecutionCopiesRequestAndLeaveInvalidatesAliases(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	request := fixture.request
	finding := Finding{PolicyVersion: "v1", Capability: "network", Scope: "app", Disposition: "allowed"}
	finding.Fingerprint = findingFingerprint(finding.PolicyVersion, finding.Capability, finding.Scope)
	request.Findings = []Finding{finding}
	if _, err := fixture.guarded.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), fixture.deployment.ID, finding.PolicyVersion, finding.Capability, finding.Scope, finding.Fingerprint, finding.Disposition, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, request)
	if err != nil {
		t.Fatal(err)
	}
	copyExecution := *execution
	request.Findings[0].Scope = "mutated"
	if err := execution.CheckBeforeCreate(); err != nil {
		t.Fatalf("execution did not retain copied findings: %v", err)
	}
	execution.Leave()
	execution.Leave()
	if err := copyExecution.CheckBeforeCreate(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("copied execution remained live after Leave: %v", err)
	}
	var zero GeneratedNetworkExecution
	if err := zero.CheckObservation(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("zero execution error=%v", err)
	}
}

func TestGeneratedNetworkExecutionLeaveWaitsForActiveSQL(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	fixture.guarded.repository.afterGeneratedNetworkIdentityRead = func() { close(entered); <-unblock }
	defer func() {
		fixture.guarded.repository.afterGeneratedNetworkIdentityRead = nil
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	checkDone := make(chan error, 1)
	go func() { checkDone <- execution.CheckObservation() }()
	awaitGeneratedNetworkSignal(t, entered, "network check")
	leaveDone := make(chan struct{})
	go func() { execution.Leave(); close(leaveDone) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		execution.state.mu.Lock()
		closing := execution.state.closing
		execution.state.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Leave did not enter closing state")
		}
		runtime.Gosched()
	}
	select {
	case <-leaveDone:
		t.Fatal("Leave completed while SQL check was active")
	default:
	}
	close(unblock)
	if err := awaitGeneratedNetworkError(t, checkDone, "network check completion"); err != nil {
		t.Fatal(err)
	}
	awaitGeneratedNetworkSignal(t, leaveDone, "network Leave")
}

func TestGeneratedNetworkExecutionRefusesAttemptSlotHeadAndPlanDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *generatedNetworkFixture, *GeneratedNetworkRequest)
		cause  error
	}{
		{name: "wrong attempt", cause: deploymentexecution.ErrIdentityLost, mutate: func(t *testing.T, fixture *generatedNetworkFixture, request *GeneratedNetworkRequest) {
			claim, err := deploymentexecution.NewClaim(fixture.binding, fixture.claim.JobID(), fixture.claim.ApplicationID(), 2)
			if err != nil {
				t.Fatal(err)
			}
			request.Claim = claim
		}},
		{name: "slot corruption", cause: ErrInvalidDeployment, mutate: func(t *testing.T, fixture *generatedNetworkFixture, _ *GeneratedNetworkRequest) {
			if _, err := fixture.guarded.repository.db.Exec(`DROP TRIGGER generated_runtime_deployment_identity_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.guarded.repository.db.Exec(`UPDATE generated_runtime_deployments SET candidate_slot='green' WHERE deployment_id=?`, fixture.deployment.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "active predecessor drift", cause: deploymentexecution.ErrAuthorityStale, mutate: func(t *testing.T, fixture *generatedNetworkFixture, _ *GeneratedNetworkRequest) {
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := fixture.guarded.repository.db.Exec(`DROP TRIGGER generated_runtime_active_head_valid_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.guarded.repository.db.Exec(`UPDATE generated_runtime_active_heads SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=? WHERE app_id=?`, fixture.deployment.ID, fixture.request.ReleaseID, stamp, fixture.claim.ApplicationID()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "plan acceptance lost", cause: ErrInvalidDeployment, mutate: func(t *testing.T, fixture *generatedNetworkFixture, _ *GeneratedNetworkRequest) {
			if _, err := fixture.guarded.repository.db.Exec(`DROP TRIGGER deployment_plan_revision_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.guarded.repository.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.guarded.repository.db.Exec(`UPDATE deployment_plan_revisions SET acceptance_status='rejected' WHERE id=?`, fixture.request.DeploymentPlanRevisionID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
			request := fixture.request
			test.mutate(t, fixture, &request)
			execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, request)
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Leave()
			if err := execution.CheckBeforeCreate(); !errors.Is(err, test.cause) {
				t.Fatalf("drift refusal=%v want=%v", err, test.cause)
			}
		})
	}
}

func TestGeneratedNetworkExecutionRefusesMissingApprovalAndMalformedEvidence(t *testing.T) {
	for _, test := range []struct {
		name        string
		fingerprint string
		disposition string
		request     bool
		cause       error
	}{
		{name: "missing current approval", disposition: "approval_required", request: true, cause: ErrApprovalRequired},
		{name: "malformed retained evidence", fingerprint: strings.Repeat("a", 64), disposition: "allowed", cause: ErrInvalidDeployment},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
			finding := Finding{PolicyVersion: "v1", Capability: "network", Scope: "app", Disposition: test.disposition}
			finding.Fingerprint = test.fingerprint
			if finding.Fingerprint == "" {
				finding.Fingerprint = findingFingerprint(finding.PolicyVersion, finding.Capability, finding.Scope)
			}
			if _, err := fixture.guarded.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), fixture.deployment.ID, finding.PolicyVersion, finding.Capability, finding.Scope, finding.Fingerprint, finding.Disposition, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			request := fixture.request
			if test.request {
				request.Findings = []Finding{finding}
			}
			execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, request)
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Leave()
			if err := execution.CheckObservation(); !errors.Is(err, test.cause) {
				t.Fatalf("evidence refusal=%v want=%v", err, test.cause)
			}
		})
	}
}

func TestGeneratedNetworkExecutionObservesMigrationApprovalLoss(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseMigrating, true)
	execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	if err := execution.CheckBeforeCreate(); err != nil {
		t.Fatalf("initial migration authority: %v", err)
	}
	if _, err := fixture.guarded.repository.db.Exec(`DELETE FROM deployment_plan_migration_approvals WHERE revision_id=? AND app_id=?`, fixture.request.DeploymentPlanRevisionID, fixture.claim.ApplicationID()); err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckObservation(); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("lost migration approval error=%v", err)
	}
}

func TestGeneratedNetworkExecutionRequiresOriginalLiveScopeAndRepository(t *testing.T) {
	t.Run("missing scope", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		if _, err := fixture.guarded.repository.PinGeneratedNetwork(context.Background(), fixture.request); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("missing scope error=%v", err)
		}
	})
	t.Run("released scope", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		if err := fixture.guarded.release(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, fixture.request); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("released scope error=%v", err)
		}
	})
	t.Run("foreign scope", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		foreignScope, release, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx := deploymenteffects.WithScope(context.Background(), foreignScope)
		if _, err := fixture.guarded.repository.PinGeneratedNetwork(ctx, fixture.request); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("foreign scope error=%v", err)
		}
	})
	t.Run("foreign repository claim", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		foreign := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := fixture.request
		request.Claim = foreign.claim
		execution, err := fixture.guarded.repository.PinGeneratedNetwork(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if err := execution.CheckBeforeCreate(); !errors.Is(err, deploymentexecution.ErrAuthorityInvalid) && !errors.Is(err, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("foreign claim error=%v", err)
		}
	})
	t.Run("unconfigured repository", func(t *testing.T) {
		if _, err := New(nil).PinGeneratedNetwork(context.Background(), GeneratedNetworkRequest{}); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
			t.Fatalf("unconfigured repository error=%v", err)
		}
	})
}

func TestGeneratedNetworkExecutionPreservesCancellationAfterPolicyRead(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	parent, cancel := context.WithCancel(context.Background())
	ctx := deploymenteffects.WithScope(parent, fixture.guarded.scope)
	execution, err := fixture.guarded.repository.PinGeneratedNetwork(ctx, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	fixture.guarded.repository.afterGeneratedNetworkIdentityRead = cancel
	defer func() { fixture.guarded.repository.afterGeneratedNetworkIdentityRead = nil }()
	if err := execution.CheckObservation(); !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation causes=%v", err)
	}
}

func awaitGeneratedNetworkSignal(t *testing.T, channel <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func awaitGeneratedNetworkError(t *testing.T, channel <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}
