package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

type pauseExecutor struct {
	disposition string
}

func (executor pauseExecutor) Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
	disposition := executor.disposition
	if disposition == "" {
		disposition = PauseApprovalRequired
	}
	return ExecutionResult{Disposition: ExecutionWaitingUser, PauseDisposition: disposition}, nil
}

func TestCreateRejectsOversizedIdempotencyKeyAtServiceBoundary(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	_, _, err := service.CreateWithInput(CreateRequest{
		Type:           "deploy",
		ResourceType:   "application",
		ResourceID:     "application",
		IdempotencyKey: strings.Repeat("x", 201),
		Input:          NoInput{},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized idempotency key error = %v", err)
	}
}

func TestDeploymentInputIsSealedAndRoundTrips(t *testing.T) {
	releaseID := uuid.NewString()
	input, err := marshalInput(DeploymentInput{ReleaseID: releaseID, ConfigurationMode: ConfigurationCurrent})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DeploymentInputFor(Job{Type: "deploy", Input: input})
	if err != nil || decoded.ReleaseID != releaseID || decoded.ConfigurationMode != ConfigurationCurrent {
		t.Fatalf("decoded = %#v, %v", decoded, err)
	}
	latest, err := marshalInput(DeploymentInput{ConfigurationMode: ConfigurationCurrent})
	if err != nil {
		t.Fatalf("latest deployment input: %v", err)
	}
	if decoded, err := DeploymentInputFor(Job{Type: "deploy", Input: latest}); err != nil || decoded.ReleaseID != "" {
		t.Fatalf("latest decoded = %#v, %v", decoded, err)
	}
	rawReleaseID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if input, err := marshalInput(DeploymentInput{ReleaseID: rawReleaseID, ConfigurationMode: ConfigurationOriginal}); err != nil {
		t.Fatalf("materialized release ID rejected: %v", err)
	} else if decoded, err := DeploymentInputFor(Job{Type: "deploy", Input: input}); err != nil || decoded.ReleaseID != rawReleaseID {
		t.Fatalf("materialized release decoded = %#v, %v", decoded, err)
	}
	for _, invalid := range []Job{
		{Type: "deploy", Input: []byte(`{"releaseId":"` + releaseID + `","configurationMode":"original","path":"/tmp"}`)},
		{Type: "deploy", Input: []byte(`{"releaseId":"not-a-uuid","configurationMode":"original"}`)},
		{Type: "deploy", Input: []byte(`{"releaseId":"` + releaseID + `","configurationMode":"other"}`)},
	} {
		if _, err := DeploymentInputFor(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid input accepted: %s (%v)", invalid.Input, err)
		}
	}
}

func TestRealDeploymentCompletionAndFailuresAreCentrallySanitized(t *testing.T) {
	if message, err := completionMessage("deployment_completed"); err != nil || message != "Deployment completed" {
		t.Fatalf("completion message=%q err=%v", message, err)
	}
	want := map[string]string{
		"invalid_source":                  "Application source is invalid",
		"source_unavailable":              "Application source is unavailable",
		"source_access_lost":              "Access to the application source was lost",
		"provider_unavailable":            "Application source provider is unavailable",
		"source_too_large":                "Application source exceeds deployment limits",
		"source_storage_full":             "Application source storage is full",
		"configuration_unavailable":       "Application configuration is unavailable",
		"process_termination_failed":      "Runtime process termination failed",
		"compose_invalid":                 "Compose configuration is invalid",
		"compose_config_invalid":          "Compose configuration is invalid",
		"compose_config_timeout":          "Compose configuration check timed out",
		"compose_config_output_truncated": "Compose configuration output exceeded the allowed limit",
		"policy_rejected":                 "Compose configuration requests an unsupported capability",
		"apply_failed":                    "Container runtime failed to apply the deployment",
		"compose_apply_failed":            "Container runtime failed to apply the deployment",
		"compose_apply_timeout":           "Container runtime apply timed out",
		"compose_apply_output_truncated":  "Container runtime apply output exceeded the allowed limit",
		"health_failed":                   "Deployment did not become healthy",
		"internal_error":                  "Deployment failed because of an internal error",
	}
	for code, message := range want {
		gotCode, gotMessage := safeExecutionFailure(&ExecutionError{Code: code, Detail: "provider-secret raw stderr"})
		if gotCode != code || gotMessage != message || gotMessage == "provider-secret raw stderr" {
			t.Fatalf("code=%q got=%q/%q", code, gotCode, gotMessage)
		}
	}
}

func TestWaitingUserResumeStartsANewAttemptAndPausedCancelIsTerminal(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	releaseID := uuid.NewString()
	insertReadyReleaseFixture(t, service, "app", releaseID)
	job, _, err := service.CreateWithInput(CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: "app",
		RequestedBy: "owner", Input: DeploymentInput{ReleaseID: releaseID, ConfigurationMode: ConfigurationOriginal},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runOne(context.Background(), pauseExecutor{}); err != nil {
		t.Fatal(err)
	}
	paused, err := service.Get(job.ID)
	if err != nil || paused.Status != string(WaitingUser) || paused.PauseDisposition != PauseApprovalRequired || paused.Attempt != 1 || paused.RequestedBy != "owner" {
		t.Fatalf("paused = %#v, %v", paused, err)
	}
	resumed, err := service.Resume(job.ID)
	if err != nil || resumed.Status != string(Queued) || resumed.PauseDisposition != "" || resumed.Attempt != 1 {
		t.Fatalf("resumed = %#v, %v", resumed, err)
	}
	claimed, ok, err := service.claimNext(context.Background())
	if err != nil || !ok || claimed.Attempt != 2 {
		t.Fatalf("second claim = %#v, %t, %v", claimed, ok, err)
	}
	if err := service.pause(job.ID, PauseApprovalRequired); err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Cancel(job.ID)
	if err != nil || cancelled.Status != string(Cancelled) || cancelled.PauseDisposition != "" {
		t.Fatalf("paused cancellation = %#v, %v", cancelled, err)
	}
	if _, err := service.Resume(job.ID); !errors.Is(err, ErrJobNotPaused) {
		t.Fatalf("resume cancelled = %v", err)
	}
}

func TestWorkerPersistsOnlyStablePauseStates(t *testing.T) {
	testCases := []struct {
		disposition string
		message     string
	}{
		{PauseApprovalRequired, "Deployment requires approval"},
		{PauseMigrationApprovalRequired, "Deployment migration requires approval"},
		{PauseInsufficientReplacementCapacity, "Deployment is waiting for replacement capacity"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.disposition, func(t *testing.T) {
			service, closeDB := newTestService(t)
			defer closeDB()
			releaseID := uuid.NewString()
			insertReadyReleaseFixture(t, service, "app", releaseID)
			job, _, err := service.CreateWithInput(CreateRequest{
				Type: "deploy", ResourceType: "application", ResourceID: "app",
				RequestedBy: "owner", Input: DeploymentInput{ReleaseID: releaseID, ConfigurationMode: ConfigurationOriginal},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.runOne(context.Background(), pauseExecutor{disposition: testCase.disposition}); err != nil {
				t.Fatal(err)
			}
			paused, err := service.Get(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantCheckpoint := `{"phase":"` + testCase.disposition + `"}`
			if paused.Status != string(WaitingUser) || paused.Phase != testCase.disposition || paused.PauseDisposition != testCase.disposition || paused.Checkpoint != wantCheckpoint {
				t.Fatalf("paused = %#v, want disposition %q", paused, testCase.disposition)
			}
			events, err := service.Events(job.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Level != "warn" || last.Phase != testCase.disposition || last.Code != testCase.disposition || last.Message != testCase.message {
				t.Fatalf("pause event = %#v", last)
			}
		})
	}

	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "invalid-pause", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.claimNext(context.Background()); err != nil || !ok {
		t.Fatalf("claim = %t, %v", ok, err)
	}
	if err := service.pause(job.ID, "untrusted-detail"); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("invalid pause disposition = %v", err)
	}
}

func TestCapacityPauseCanBeRetriedWithoutApproval(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "capacity-retry", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.claimNext(context.Background()); err != nil || !ok {
		t.Fatalf("claim = %t, %v", ok, err)
	}
	if err := service.pause(job.ID, PauseInsufficientReplacementCapacity); err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(job.ID)
	if err != nil || resumed.Status != string(Queued) || resumed.Phase != "queued" || resumed.PauseDisposition != "" || resumed.Checkpoint != "{}" {
		t.Fatalf("resumed = %#v, %v", resumed, err)
	}
}

func TestMigrationPauseRequiresExactPlanApproval(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := New(db)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	appID, actorID, planID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?, 'hash',?,?)`, actorID, actorID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, appID, appID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,?,?,?,'accepted',?,?)`,
		planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret", strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), actorID, now, actorID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, planID, now, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, appID, now, strings.Repeat("b", 64), strings.Repeat("e", 64), planID); err != nil {
		t.Fatal(err)
	}
	job, _, err := service.CreateWithInput(CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID,
		Input: DeploymentInput{ReleaseID: releaseID, ConfigurationMode: ConfigurationOriginal},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,?, 'needs_attention','original',1,'generated_node',?,1)`, uuid.NewString(), appID, releaseID, job.ID, planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,checkpoint_json=?,pause_disposition=?,attempt=1 WHERE id=?`, PauseMigrationApprovalRequired, `{"phase":"migration_approval_required"}`, PauseMigrationApprovalRequired, job.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Resume(job.ID); !errors.Is(err, ErrMigrationApprovalRequired) {
		t.Fatalf("resume without approval = %v", err)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(WaitingUser) || persisted.PauseDisposition != PauseMigrationApprovalRequired || persisted.Attempt != 1 {
		t.Fatalf("unapproved migration changed job = %#v, %v", persisted, err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,?,?)`, planID, appID, actorID, now); err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(job.ID)
	if err != nil || resumed.Status != string(Queued) || resumed.PauseDisposition != "" || resumed.Attempt != 1 {
		t.Fatalf("approved migration resume = %#v, %v", resumed, err)
	}
}

func TestResumeWaitsForConcurrentApprovalRevocationAndFailsClosed(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	peerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peerDB.Close() })
	service := New(db)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	appID, actorID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'resume-owner','hash',?,?)`, actorID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,'resume-race','Resume race',?,?)`, appID, now, now); err != nil {
		t.Fatal(err)
	}
	job, _, err := service.CreateWithInput(CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: appID,
		RequestedBy: actorID, Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent},
	})
	if err != nil {
		t.Fatal(err)
	}
	deploymentID, findingID, approvalID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	const fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized) VALUES(?,?,?,'needs_attention','current',1)`, deploymentID, appID, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?, 'compose-runtime-v1','privileged','service:web',?,'approval_required',?)`, findingID, deploymentID, fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?, 'compose-runtime-v1','privileged','service:web',?,?,?)`, approvalID, appID, fingerprint, actorID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required',attempt=2 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.wake:
	default:
	}

	revocation, err := peerDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	revocationOpen := true
	t.Cleanup(func() {
		if revocationOpen {
			_ = revocation.Rollback()
		}
	})
	if _, err := revocation.Exec(`UPDATE runtime_approvals SET revoked_by=?,revoked_at=? WHERE id=? AND revoked_at IS NULL`, actorID, time.Now().UTC().Format(time.RFC3339Nano), approvalID); err != nil {
		t.Fatal(err)
	}

	resumeCalling := make(chan struct{})
	resumeResult := make(chan error, 1)
	go func() {
		close(resumeCalling)
		_, resumeErr := service.Resume(job.ID)
		resumeResult <- resumeErr
	}()
	<-resumeCalling
	select {
	case resumeErr := <-resumeResult:
		t.Fatalf("resume returned before the revocation writer committed: %v", resumeErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := revocation.Commit(); err != nil {
		t.Fatal(err)
	}
	revocationOpen = false
	select {
	case resumeErr := <-resumeResult:
		if !errors.Is(resumeErr, ErrApprovalRequired) {
			t.Fatalf("resume after revocation = %v, want %v", resumeErr, ErrApprovalRequired)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not finish after revocation committed")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(WaitingUser) || persisted.Phase != "approval_required" || persisted.PauseDisposition != "approval_required" || persisted.Attempt != 2 {
		t.Fatalf("resume after revocation changed job = %#v, %v", persisted, err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || countEvents(events, "job_resumed") != 0 {
		t.Fatalf("resume after revocation events = %#v, %v", events, err)
	}
	select {
	case <-service.wake:
		t.Fatal("resume after revocation woke a worker")
	default:
	}
}

func TestRecoveryPreservesIntentionalUserPause(t *testing.T) {
	for _, disposition := range []string{PauseApprovalRequired, PauseMigrationApprovalRequired, PauseInsufficientReplacementCapacity} {
		t.Run(disposition, func(t *testing.T) {
			service, closeDB := newTestService(t)
			defer closeDB()
			job, _, err := service.Create("deploy", "application", "paused-recovery", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := service.claimNext(context.Background()); err != nil || !ok {
				t.Fatalf("claim = %t, %v", ok, err)
			}
			if err := service.pause(job.ID, disposition); err != nil {
				t.Fatal(err)
			}
			if err := service.RecoverInterrupted(); err != nil {
				t.Fatal(err)
			}
			persisted, err := service.Get(job.ID)
			if err != nil || persisted.Status != string(WaitingUser) || persisted.PauseDisposition != disposition {
				t.Fatalf("recovered pause = %#v, %v", persisted, err)
			}
		})
	}
}
