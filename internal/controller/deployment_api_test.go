package controller_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/composeruntime"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
)

// deploymentAPIFixture deliberately uses a real migrated SQLite database. The
// API tests below exercise the controller's authorization and IDOR boundaries
// together with durable job/deployment state, not an in-memory imitation.
type deploymentAPIFixture struct {
	db          *sql.DB
	auth        *auth.Service
	session     auth.Session
	otherUserID string
	apps        *apps.Store
	jobs        *jobs.Service
	deployments *deployments.Repository
	handler     http.Handler
	app         apps.Application
	otherApp    apps.Application
	stateRoot   string
	logs        *bytes.Buffer
}

func newDeploymentAPIFixture(t *testing.T, composeRuntime, fakeRuntime bool) deploymentAPIFixture {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authService := auth.New(db)
	token, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := authService.Bootstrap(token, "admin", "this is a secure passphrase")
	if err != nil {
		t.Fatal(err)
	}
	// A second authenticated identity is used to prove job ownership checks.
	// Authentication owns user creation, so bootstrap a separate temporary DB
	// session is not useful here; direct insertion is limited to fixture setup.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	otherID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, otherID, "other-admin", "fixture", now, now); err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	appStore := apps.New(db)
	app, err := appStore.Create("Fixture", "", "C:/fixture/compose.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	otherApp, err := appStore.Create("Other fixture", "", "C:/other/compose.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(db)
	deploymentStore := deployments.New(db)
	logs := &bytes.Buffer{}
	handler := (&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobService, Machines: machineStore,
		Deployments: deploymentStore, ComposeRuntime: composeRuntime, FakeRuntime: fakeRuntime,
		DataRoot: t.TempDir(), Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}).Handler()
	return deploymentAPIFixture{db: db, auth: authService, session: session, otherUserID: otherID, apps: appStore, jobs: jobService, deployments: deploymentStore, handler: handler, app: app, otherApp: otherApp, stateRoot: stateRoot, logs: logs}
}

func (f deploymentAPIFixture) request(method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: f.session.Token})
	r.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("X-CSRF-Token", f.session.CSRF)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f deploymentAPIFixture) createDeployment(t *testing.T, appID, mode string) (jobs.Job, deployments.Deployment) {
	t.Helper()
	job, _, err := f.jobs.CreateWithInput(jobs.CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: f.userID(t), Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationMode(mode)}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, _, err := f.deployments.GetOrCreateByJob(context.Background(), appID, job.ID, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.deployments.Initialize(context.Background(), appID, deployment.ID, "", "", 0); err != nil {
		t.Fatal(err)
	}
	return job, deployment
}

func (f deploymentAPIFixture) userID(t *testing.T) string {
	t.Helper()
	user, _, err := f.auth.Authenticate(f.session.Token)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func assertProblemCode(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", w.Code, wantStatus, w.Body.String())
	}
	var problem apicontract.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem JSON: %v (%s)", err, w.Body.String())
	}
	if problem.Code != wantCode || problem.Detail == "" || problem.RequestID == "" {
		t.Fatalf("problem = %#v, want code %q", problem, wantCode)
	}
}

func TestDeploymentMutationsRequireAuthenticationAndCSRF(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	releaseID := f.insertRelease(t, f.app.ID, "ready")
	for _, endpoint := range []struct {
		name   string
		path   string
		body   string
		method string
	}{
		{"latest", "/api/v1/apps/" + f.app.ID + "/deployments", "{}", http.MethodPost},
		{"prior", "/api/v1/apps/" + f.app.ID + "/releases/" + releaseID + "/deployments", `{"configurationMode":"current"}`, http.MethodPost},
		{"grant", "/api/v1/apps/" + f.app.ID + "/runtime-approvals", `{"fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, http.MethodPost},
		{"revoke", "/api/v1/apps/" + f.app.ID + "/runtime-approvals/" + uuid.NewString(), "", http.MethodDelete},
		{"resume", "/api/v1/jobs/" + uuid.NewString() + "/resume", "", http.MethodPost},
	} {
		t.Run(endpoint.name+" unauthenticated", func(t *testing.T) {
			r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			assertProblemCode(t, w, http.StatusUnauthorized, "unauthenticated")
		})
		t.Run(endpoint.name+" csrf", func(t *testing.T) {
			r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: f.session.Token})
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			assertProblemCode(t, w, http.StatusForbidden, "csrf_failed")
		})
	}
}

func TestComposeRuntimeCapabilityDefaultsOffAndDoesNotImplyFakeRuntime(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		compose bool
		fake    bool
	}{
		{"default", false, false},
		{"compose only", true, false},
		{"fake only", false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newDeploymentAPIFixture(t, testCase.compose, testCase.fake)
			w := f.request(http.MethodGet, "/api/v1/system/status", "")
			if w.Code != http.StatusOK {
				t.Fatalf("status endpoint: %d %s", w.Code, w.Body.String())
			}
			var status apicontract.SystemStatus
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.Capabilities.ComposeRuntime != testCase.compose || status.Capabilities.FakeRuntime != testCase.fake {
				t.Fatalf("capabilities = %#v", status.Capabilities)
			}
		})
	}
}

func TestLatestDeploymentIsTypedActorBoundAndIdempotent(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	path := "/api/v1/apps/" + f.app.ID + "/deployments"
	first := f.requestWithKey(http.MethodPost, path, "{}", "latest-deployment")
	if first.Code != http.StatusAccepted {
		t.Fatalf("initial latest deployment: %d %s", first.Code, first.Body.String())
	}
	var initial apicontract.JobMutationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if !initial.Created || initial.Job.Type != "deploy" || initial.Job.ResourceType != "application" || initial.Job.ResourceID != f.app.ID || initial.Job.RequestedBy != f.userID(t) || initial.Job.Attempt != 0 {
		t.Fatalf("initial deployment job = %#v", initial.Job)
	}
	persisted, err := f.jobs.Get(initial.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var input jobs.DeploymentInput
	if err := json.Unmarshal(persisted.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input.ReleaseID != "" || input.ConfigurationMode != jobs.ConfigurationCurrent {
		t.Fatalf("durable latest input = %#v", input)
	}
	replay := f.requestWithKey(http.MethodPost, path, "{}", "latest-deployment")
	if replay.Code != http.StatusOK {
		t.Fatalf("idempotent replay: %d %s", replay.Code, replay.Body.String())
	}
	var replayed apicontract.JobMutationResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.Created || replayed.Job.ID != initial.Job.ID || replayed.Job.RequestedBy != initial.Job.RequestedBy {
		t.Fatalf("idempotent replay = %#v", replayed)
	}
}

func TestDeploymentRequestConflictsAreSanitized(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	latest := "/api/v1/apps/" + f.app.ID + "/deployments"
	first := f.requestWithKey(http.MethodPost, latest, "{}", "same-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first deployment: %d %s", first.Code, first.Body.String())
	}
	releaseID := f.insertRelease(t, f.app.ID, "ready")
	conflict := f.requestWithKey(http.MethodPost, "/api/v1/apps/"+f.app.ID+"/releases/"+releaseID+"/deployments", `{"configurationMode":"original"}`, "same-key")
	assertProblemCode(t, conflict, http.StatusConflict, "idempotency_conflict")
	assertNoSensitiveDetail(t, conflict.Body.String())

	busy := f.request(http.MethodPost, latest, "{}")
	assertProblemCode(t, busy, http.StatusConflict, "application_busy")
	assertNoSensitiveDetail(t, busy.Body.String())

	tooLong := f.requestWithKey(http.MethodPost, latest, "{}", strings.Repeat("a", 201))
	assertProblemCode(t, tooLong, http.StatusUnprocessableEntity, "invalid_idempotency_key")
	assertNoSensitiveDetail(t, tooLong.Body.String())
}

func TestPriorReleaseDeploymentValidatesReadinessModeAndAppBoundary(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	path := func(appID, releaseID string) string {
		return "/api/v1/apps/" + appID + "/releases/" + releaseID + "/deployments"
	}
	readyID := f.insertRelease(t, f.app.ID, "ready")
	for _, testCase := range []struct {
		name      string
		appID     string
		releaseID string
		body      string
		status    int
		problem   string
	}{
		{"missing", f.app.ID, uuid.NewString(), `{"configurationMode":"current"}`, http.StatusNotFound, "release_not_found"},
		{"nonready", f.app.ID, f.insertRelease(t, f.app.ID, "failed"), `{"configurationMode":"current"}`, http.StatusNotFound, "release_not_found"},
		{"cross app cloaked", f.app.ID, f.insertRelease(t, f.otherApp.ID, "ready"), `{"configurationMode":"current"}`, http.StatusNotFound, "release_not_found"},
		{"missing mode", f.app.ID, readyID, `{}`, http.StatusUnprocessableEntity, "invalid_deployment"},
		{"invalid mode", f.app.ID, readyID, `{"configurationMode":"future"}`, http.StatusUnprocessableEntity, "invalid_deployment"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			w := f.request(http.MethodPost, path(testCase.appID, testCase.releaseID), testCase.body)
			assertProblemCode(t, w, testCase.status, testCase.problem)
			assertNoSensitiveDetail(t, w.Body.String())
		})
	}

	for _, mode := range []string{"current", "original"} {
		t.Run(mode, func(t *testing.T) {
			w := f.requestWithKey(http.MethodPost, path(f.app.ID, readyID), `{"configurationMode":"`+mode+`"}`, "prior-"+mode)
			if w.Code != http.StatusAccepted {
				t.Fatalf("prior %s: %d %s", mode, w.Code, w.Body.String())
			}
			var response apicontract.JobMutationResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			job, err := f.jobs.Get(response.Job.ID)
			if err != nil {
				t.Fatal(err)
			}
			var input jobs.DeploymentInput
			if err := json.Unmarshal(job.Input, &input); err != nil || input.ReleaseID != readyID || input.ConfigurationMode != jobs.ConfigurationMode(mode) {
				t.Fatalf("prior input = %#v, err=%v", input, err)
			}
			if _, err := f.jobs.Cancel(response.Job.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPriorReleaseAndResumeNeedRealComposeRuntime(t *testing.T) {
	defaultRuntime := newDeploymentAPIFixture(t, false, false)
	defaultLatest := defaultRuntime.request(http.MethodPost, "/api/v1/apps/"+defaultRuntime.app.ID+"/deployments", "{}")
	assertProblemCode(t, defaultLatest, http.StatusConflict, "capability_unavailable")

	f := newDeploymentAPIFixture(t, false, true)
	latest := f.request(http.MethodPost, "/api/v1/apps/"+f.app.ID+"/deployments", "{}")
	if latest.Code != http.StatusAccepted {
		t.Fatalf("fake-runtime latest deployment compatibility: %d %s", latest.Code, latest.Body.String())
	}
	var latestBody apicontract.JobMutationResponse
	if err := json.Unmarshal(latest.Body.Bytes(), &latestBody); err != nil || latestBody.Job.Type != "deploy" || latestBody.Job.RequestedBy != f.userID(t) {
		t.Fatalf("fake-runtime latest deployment = %#v, err=%v", latestBody, err)
	}
	if _, err := f.jobs.Cancel(latestBody.Job.ID); err != nil {
		t.Fatal(err)
	}
	releaseID := f.insertRelease(t, f.app.ID, "ready")
	prior := f.request(http.MethodPost, "/api/v1/apps/"+f.app.ID+"/releases/"+releaseID+"/deployments", `{"configurationMode":"current"}`)
	assertProblemCode(t, prior, http.StatusConflict, "capability_unavailable")
	job, _ := f.createDeployment(t, f.app.ID, "current")
	if _, err := f.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	resume := f.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/resume", "")
	assertProblemCode(t, resume, http.StatusConflict, "capability_unavailable")
}

func TestDeploymentAndReleaseListsExposeSafeHistory(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	_, deployment := f.createDeployment(t, f.app.ID, "current")
	fingerprint := strings.Repeat("c", 64)
	if _, err := f.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), deployment.ID, "compose-runtime-v1", "privileged", "service:web", fingerprint, "approval_required", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	legacyID := uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO deployments(id,app_id,status,configuration_mode,actual_configuration_revision_number,provenance_initialized,failure_summary) VALUES(?,?, 'failed','original',0,0,'legacy failure')`, legacyID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	deploymentsResponse := f.request(http.MethodGet, "/api/v1/apps/"+f.app.ID+"/deployments", "")
	if deploymentsResponse.Code != http.StatusOK {
		t.Fatalf("deployment history: %d %s", deploymentsResponse.Code, deploymentsResponse.Body.String())
	}
	var history apicontract.DeploymentList
	if err := json.Unmarshal(deploymentsResponse.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 2 {
		t.Fatalf("deployment count = %d: %#v", len(history.Items), history.Items)
	}
	var foundLegacy, foundFindings bool
	for _, item := range history.Items {
		if item.ID == legacyID && item.JobID == "" {
			foundLegacy = true
		}
		if item.ID == deployment.ID && len(item.Findings) == 1 && item.Findings[0].Fingerprint == fingerprint {
			foundFindings = true
		}
	}
	if !foundLegacy || !foundFindings {
		t.Fatalf("history did not preserve legacy/findings: %#v", history.Items)
	}

	releaseID := f.insertRelease(t, f.app.ID, "ready")
	if _, err := f.db.Exec(`UPDATE releases SET workspace_path=? WHERE id=?`, `C:\rig\releases\confidential`, releaseID); err != nil {
		t.Fatal(err)
	}
	releasesResponse := f.request(http.MethodGet, "/api/v1/apps/"+f.app.ID+"/releases", "")
	if releasesResponse.Code != http.StatusOK {
		t.Fatalf("release history: %d %s", releasesResponse.Code, releasesResponse.Body.String())
	}
	if strings.Contains(releasesResponse.Body.String(), "confidential") || strings.Contains(releasesResponse.Body.String(), "workspace_path") {
		t.Fatalf("workspace path leaked: %s", releasesResponse.Body.String())
	}
	var releases apicontract.ReleaseList
	if err := json.Unmarshal(releasesResponse.Body.Bytes(), &releases); err != nil || len(releases.Items) != 1 {
		t.Fatalf("release contract: %s (%v)", releasesResponse.Body.String(), err)
	}
	release := releases.Items[0]
	if release.ID != releaseID || release.SourceProvider != "github" || release.RepositoryID != 17 || release.RepositoryOwner != "owner" || release.RepositoryName != "repository" || release.TrackedRef != "refs/heads/main" || release.ResolvedSha == "" || release.ArchiveSha256 == "" || release.ConfigurationRevisionNumber != 0 {
		t.Fatalf("release provenance incomplete: %#v", release)
	}
}

func TestSecretDerivedPolicyNeverLeaksThroughDurableOrPublicSurfaces(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	const secret = "RigSecretCapabilitySentinel9Z"
	origin := appconfig.SecretOrigin{
		RevisionID: uuid.NewString(), RevisionNumber: 9,
		Key: []byte("TOKEN"), Value: []byte(secret),
	}
	model := []byte(`{"services":{"web":{"cap_add":["RIGSECRETCAPABILITYSENTINEL9Z"]}}}`)
	project := "rig-" + strings.ReplaceAll(f.app.ID, "-", "")
	policyFindings, err := composeruntime.EvaluatePolicy(model, t.TempDir(), f.app.ID, project, origin)
	if err != nil || len(policyFindings) != 1 || policyFindings[0].Disposition != composeruntime.DispositionRejected {
		t.Fatalf("policy findings=%#v err=%v", policyFindings, err)
	}

	job, deployment := f.createDeployment(t, f.app.ID, "current")
	findings := make([]deployments.Finding, len(policyFindings))
	for index, finding := range policyFindings {
		findings[index] = deployments.Finding{
			PolicyVersion: finding.PolicyVersion, Capability: finding.Capability,
			Scope: finding.Scope, Fingerprint: finding.Fingerprint, Disposition: finding.Disposition,
		}
	}
	if err := f.deployments.Gate(context.Background(), f.app.ID, deployment.ID, findings); !errors.Is(err, deployments.ErrRejectedCapability) {
		t.Fatalf("gate error=%v", err)
	}
	var adminID string
	if err := f.db.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.deployments.Grant(context.Background(), f.app.ID, adminID, policyFindings[0].Fingerprint); !errors.Is(err, deployments.ErrInvalidDeployment) {
		t.Fatalf("rejected secret finding became approvable: %v", err)
	}

	history := f.request(http.MethodGet, "/api/v1/apps/"+f.app.ID+"/deployments", "")
	events := f.request(http.MethodGet, "/api/v1/jobs/"+job.ID+"/events", "")
	problem := f.request(http.MethodPost, "/api/v1/apps/"+f.app.ID+"/runtime-approvals", `{"fingerprint":"`+secret+`"}`)
	for name, payload := range map[string][]byte{
		"history response": history.Body.Bytes(), "job events": events.Body.Bytes(),
		"problem response": problem.Body.Bytes(), "logs": f.logs.Bytes(),
	} {
		assertSecretAbsent(t, name, payload, secret)
	}

	if _, err := f.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(f.stateRoot, "control.db*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		assertSecretAbsent(t, filepath.Base(path), contents, secret)
	}
}

func assertSecretAbsent(t *testing.T, surface string, contents []byte, secret string) {
	t.Helper()
	lower := bytes.ToLower(contents)
	if bytes.Contains(lower, bytes.ToLower([]byte(secret))) {
		t.Fatalf("secret found in %s", surface)
	}
}

func TestLocalReleaseHistoryNeverExposesAbsoluteSourceOrWorkspacePath(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	releaseID := uuid.NewString()
	absSource := `C:\users\operator\private-source`
	_, err := f.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,source_commit_sha,source_branch,compose_path,archive_sha256,workspace_tree_sha256,workspace_path,workspace_state,configuration_revision_number) VALUES(?,?, 'ready','{}',?,'local',0,?,?, '', 'compose.yaml',?,?,?, 'ready',0)`, releaseID, f.app.ID, time.Now().UTC().Format(time.RFC3339Nano), strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("a", 64), absSource)
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(http.MethodGet, "/api/v1/apps/"+f.app.ID+"/releases", "")
	if response.Code != http.StatusOK {
		t.Fatalf("release history: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), absSource) || strings.Contains(response.Body.String(), "private-source") || strings.Contains(response.Body.String(), "workspacePath") {
		t.Fatalf("absolute local path leaked: %s", response.Body.String())
	}
	var releases apicontract.ReleaseList
	if err := json.Unmarshal(response.Body.Bytes(), &releases); err != nil || len(releases.Items) != 1 || releases.Items[0].SourceProvider != "local" || releases.Items[0].RepositoryID != 0 {
		t.Fatalf("local release contract=%#v err=%v", releases, err)
	}
}

func TestRuntimeApprovalLifecycleOnlyUsesStoredFinding(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	_, deployment := f.createDeployment(t, f.app.ID, "current")
	fingerprint := policyFindingFingerprint("compose-runtime-v1", "privileged", "service:web")
	if err := f.deployments.Gate(context.Background(), f.app.ID, deployment.ID, []deployments.Finding{{PolicyVersion: "compose-runtime-v1", Capability: "privileged", Scope: "service:web", Fingerprint: fingerprint, Disposition: "approval_required"}}); !errors.Is(err, deployments.ErrApprovalRequired) {
		t.Fatal(err)
	}
	path := "/api/v1/apps/" + f.app.ID + "/runtime-approvals"
	created := f.request(http.MethodPost, path, `{"fingerprint":"`+fingerprint+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", created.Code, created.Body.String())
	}
	var grant apicontract.RuntimeApprovalMutationResponse
	if err := json.Unmarshal(created.Body.Bytes(), &grant); err != nil || !grant.Created || grant.Approval.AppID != f.app.ID || grant.Approval.Fingerprint != fingerprint || grant.Approval.GrantedBy != f.userID(t) {
		t.Fatalf("grant = %#v, err=%v", grant, err)
	}
	duplicate := f.request(http.MethodPost, path, `{"fingerprint":"`+fingerprint+`"}`)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate grant: %d %s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateBody apicontract.RuntimeApprovalMutationResponse
	if err := json.Unmarshal(duplicate.Body.Bytes(), &duplicateBody); err != nil || duplicateBody.Created || duplicateBody.Approval.ID != grant.Approval.ID {
		t.Fatalf("duplicate grant = %#v, err=%v", duplicateBody, err)
	}
	listed := f.request(http.MethodGet, path, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("approval list: %d %s", listed.Code, listed.Body.String())
	}
	var list apicontract.RuntimeApprovalList
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != grant.Approval.ID || list.Items[0].RevokedAt != "" {
		t.Fatalf("approval list = %#v, err=%v", list, err)
	}
	crossAppList := f.request(http.MethodGet, "/api/v1/apps/"+f.otherApp.ID+"/runtime-approvals", "")
	if crossAppList.Code != http.StatusOK {
		t.Fatalf("cross-app approval list: %d %s", crossAppList.Code, crossAppList.Body.String())
	}
	var otherList apicontract.RuntimeApprovalList
	if err := json.Unmarshal(crossAppList.Body.Bytes(), &otherList); err != nil || len(otherList.Items) != 0 {
		t.Fatalf("cross-app approval leaked: %#v, err=%v", otherList, err)
	}
	for _, testCase := range []struct{ name, appID, body string }{
		{"arbitrary", f.app.ID, `{"fingerprint":"` + strings.Repeat("e", 64) + `"}`},
		{"cross app", f.otherApp.ID, `{"fingerprint":"` + fingerprint + `"}`},
		{"malformed", f.app.ID, `{"fingerprint":"UPPER"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			w := f.request(http.MethodPost, "/api/v1/apps/"+testCase.appID+"/runtime-approvals", testCase.body)
			if testCase.name == "malformed" {
				assertProblemCode(t, w, http.StatusUnprocessableEntity, "invalid_approval")
			} else {
				assertProblemCode(t, w, http.StatusNotFound, "finding_not_found")
			}
		})
	}

	// The first grant is still in use while the deployment has passed the
	// approval gate, so revocation must fail closed. Once terminal, the exact
	// same approval can be revoked and remains visible in history.
	if err := f.deployments.Gate(context.Background(), f.app.ID, deployment.ID, []deployments.Finding{{PolicyVersion: "compose-runtime-v1", Capability: "privileged", Scope: "service:web", Fingerprint: fingerprint, Disposition: "approval_required"}}); err != nil {
		t.Fatal(err)
	}
	inUse := f.request(http.MethodDelete, path+"/"+grant.Approval.ID, "")
	assertProblemCode(t, inUse, http.StatusConflict, "approval_in_use")
	crossAppRevoke := f.request(http.MethodDelete, "/api/v1/apps/"+f.otherApp.ID+"/runtime-approvals/"+grant.Approval.ID, "")
	assertProblemCode(t, crossAppRevoke, http.StatusNotFound, "approval_not_found")
	if _, err := f.deployments.Transition(context.Background(), f.app.ID, deployment.ID, deployments.Failed, "apply_failed"); err != nil {
		t.Fatal(err)
	}
	revoked := f.request(http.MethodDelete, path+"/"+grant.Approval.ID, "")
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body.String())
	}
	var revokedBody apicontract.RuntimeApprovalResponse
	if err := json.Unmarshal(revoked.Body.Bytes(), &revokedBody); err != nil || revokedBody.Approval.RevokedAt == "" || revokedBody.Approval.RevokedBy != f.userID(t) {
		t.Fatalf("revoked = %#v, err=%v", revokedBody, err)
	}
	revokedList := f.request(http.MethodGet, path, "")
	if err := json.Unmarshal(revokedList.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != grant.Approval.ID || list.Items[0].RevokedAt == "" || list.Items[0].RevokedBy != f.userID(t) {
		t.Fatalf("revoked approval history = %#v, err=%v", list, err)
	}
}

func TestDeploymentHistoryRoutesCloakMissingApplication(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	missingApp := uuid.NewString()
	for _, suffix := range []string{"/deployments", "/releases", "/runtime-approvals"} {
		t.Run(suffix, func(t *testing.T) {
			response := f.request(http.MethodGet, "/api/v1/apps/"+missingApp+suffix, "")
			assertProblemCode(t, response, http.StatusNotFound, "app_not_found")
			assertNoSensitiveDetail(t, response.Body.String())
		})
	}
}

func TestResumeOnlyRequeuesOwnWaitingDeploymentOnComposeRuntime(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	job, _ := f.createDeployment(t, f.app.ID, "current")
	if _, err := f.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required',attempt=4 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	resumed := f.request(http.MethodPost, "/api/v1/jobs/"+job.ID+"/resume", "")
	if resumed.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", resumed.Code, resumed.Body.String())
	}
	var resumedBody apicontract.JobResponse
	if err := json.Unmarshal(resumed.Body.Bytes(), &resumedBody); err != nil {
		t.Fatal(err)
	}
	if resumedBody.Job.ID != job.ID || resumedBody.Job.Status != string(jobs.Queued) || resumedBody.Job.Phase != "queued" || resumedBody.Job.PauseDisposition != "" || resumedBody.Job.RequestedBy != f.userID(t) || resumedBody.Job.Attempt != 4 {
		t.Fatalf("resumed job = %#v", resumedBody.Job)
	}
	events := f.request(http.MethodGet, "/api/v1/jobs/"+job.ID+"/events", "")
	if events.Code != http.StatusOK {
		t.Fatalf("events: %d %s", events.Code, events.Body.String())
	}
	var eventList apicontract.JobEventList
	if err := json.Unmarshal(events.Body.Bytes(), &eventList); err != nil || len(eventList.Items) < 2 {
		t.Fatalf("event contract = %#v, err=%v", eventList, err)
	}
	if eventList.Items[0].Attempt != 0 || eventList.Items[len(eventList.Items)-1].Attempt != 4 || eventList.Items[len(eventList.Items)-1].Code != "job_resumed" {
		t.Fatalf("event attempt history was not mapped: %#v", eventList.Items)
	}
	if _, err := f.jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}

	notPaused, _ := f.createDeployment(t, f.app.ID, "current")
	notPausedResponse := f.request(http.MethodPost, "/api/v1/jobs/"+notPaused.ID+"/resume", "")
	assertProblemCode(t, notPausedResponse, http.StatusConflict, "job_not_paused")
	if _, err := f.jobs.Cancel(notPaused.ID); err != nil {
		t.Fatal(err)
	}

	nonDeploy, _, err := f.jobs.CreateWithInput(jobs.CreateRequest{Type: "start", ResourceType: "application", ResourceID: f.app.ID, RequestedBy: f.userID(t), Input: jobs.NoInput{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required' WHERE id=?`, nonDeploy.ID); err != nil {
		t.Fatal(err)
	}
	unrelated := f.request(http.MethodPost, "/api/v1/jobs/"+nonDeploy.ID+"/resume", "")
	assertProblemCode(t, unrelated, http.StatusNotFound, "job_not_found")

	otherActor, _ := f.createDeployment(t, f.otherApp.ID, "current")
	if _, err := f.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required',requested_by=? WHERE id=?`, f.otherUserID, otherActor.ID); err != nil {
		t.Fatal(err)
	}
	actorMismatch := f.request(http.MethodPost, "/api/v1/jobs/"+otherActor.ID+"/resume", "")
	assertProblemCode(t, actorMismatch, http.StatusNotFound, "job_not_found")
	assertNoSensitiveDetail(t, actorMismatch.Body.String())

	missingApp, _, err := f.jobs.CreateWithInput(jobs.CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: uuid.NewString(), RequestedBy: f.userID(t), Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required' WHERE id=?`, missingApp.ID); err != nil {
		t.Fatal(err)
	}
	crossApp := f.request(http.MethodPost, "/api/v1/jobs/"+missingApp.ID+"/resume", "")
	assertProblemCode(t, crossApp, http.StatusNotFound, "job_not_found")
}

func TestResumeRequiresEveryExactActiveRuntimeApproval(t *testing.T) {
	f := newDeploymentAPIFixture(t, true, false)
	job, deployment := f.createDeployment(t, f.app.ID, "current")
	findings := []deployments.Finding{
		{
			PolicyVersion: "compose-runtime-v1", Capability: "privileged", Scope: "service:web",
			Fingerprint: policyFindingFingerprint("compose-runtime-v1", "privileged", "service:web"), Disposition: "approval_required",
		},
		{
			PolicyVersion: "compose-runtime-v1", Capability: "host-network", Scope: "service:worker",
			Fingerprint: policyFindingFingerprint("compose-runtime-v1", "host-network", "service:worker"), Disposition: "approval_required",
		},
	}
	if err := f.deployments.Gate(context.Background(), f.app.ID, deployment.ID, findings); !errors.Is(err, deployments.ErrApprovalRequired) {
		t.Fatalf("initial approval gate = %v", err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required',attempt=3 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}

	actorID := f.userID(t)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	wrongIdentityID, wrongAppID := uuid.NewString(), uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, wrongIdentityID, f.app.ID, "compose-runtime-v0", "different-capability", "service:different", findings[0].Fingerprint, actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, wrongAppID, f.otherApp.ID, findings[1].PolicyVersion, findings[1].Capability, findings[1].Scope, findings[1].Fingerprint, actorID, stamp); err != nil {
		t.Fatal(err)
	}

	resumePath := "/api/v1/jobs/" + job.ID + "/resume"
	assertResumeApprovalRequired := func(label string) {
		t.Helper()
		response := f.request(http.MethodPost, resumePath, "")
		assertProblemCode(t, response, http.StatusConflict, "approval_required")
		assertNoSensitiveDetail(t, response.Body.String())
		if strings.Contains(response.Body.String(), findings[0].Fingerprint) || strings.Contains(response.Body.String(), findings[1].Scope) {
			t.Fatalf("%s response exposed policy identity: %s", label, response.Body.String())
		}
		persisted, err := f.jobs.Get(job.ID)
		if err != nil || persisted.Status != string(jobs.WaitingUser) || persisted.Phase != "approval_required" || persisted.PauseDisposition != "approval_required" || persisted.Attempt != 3 {
			t.Fatalf("%s changed waiting job: %#v, %v", label, persisted, err)
		}
		events, err := f.jobs.Events(job.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Code == "job_resumed" {
				t.Fatalf("%s appended a resume event: %#v", label, events)
			}
		}
	}

	assertResumeApprovalRequired("mismatched approvals")
	if _, err := f.deployments.Revoke(context.Background(), f.app.ID, wrongIdentityID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.deployments.Revoke(context.Background(), f.otherApp.ID, wrongAppID, actorID); err != nil {
		t.Fatal(err)
	}
	firstApproval, _, err := f.deployments.Grant(context.Background(), f.app.ID, actorID, findings[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	assertResumeApprovalRequired("one of two approvals")
	if _, _, err := f.deployments.Grant(context.Background(), f.app.ID, actorID, findings[1].Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := f.deployments.Revoke(context.Background(), f.app.ID, firstApproval.ID, actorID); err != nil {
		t.Fatal(err)
	}
	assertResumeApprovalRequired("revoked approval")
	if _, _, err := f.deployments.Grant(context.Background(), f.app.ID, actorID, findings[0].Fingerprint); err != nil {
		t.Fatal(err)
	}

	resumed := f.request(http.MethodPost, resumePath, "")
	if resumed.Code != http.StatusOK {
		t.Fatalf("approved resume: %d %s", resumed.Code, resumed.Body.String())
	}
	persisted, err := f.jobs.Get(job.ID)
	if err != nil || persisted.Status != string(jobs.Queued) || persisted.Phase != "queued" || persisted.PauseDisposition != "" || persisted.Attempt != 3 {
		t.Fatalf("approved resume = %#v, %v", persisted, err)
	}
	events, err := f.jobs.Events(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	resumedEvents := 0
	for _, event := range events {
		if event.Code == "job_resumed" {
			resumedEvents++
		}
	}
	if resumedEvents != 1 {
		t.Fatalf("approved resume events = %#v", events)
	}
}

func (f deploymentAPIFixture) requestWithKey(method, path, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: f.session.Token})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", f.session.CSRF)
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func assertNoSensitiveDetail(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{"sqlite", "constraint", "sentinel", "github token", "C:\\rig", "confidential"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("unsafe internal detail %q in %s", forbidden, body)
		}
	}
}

func policyFindingFingerprint(version, capability, scope string) string {
	canonical, _ := json.Marshal(struct {
		PolicyVersion string `json:"policyVersion"`
		Capability    string `json:"capability"`
		Scope         string `json:"scope"`
	}{version, capability, scope})
	return fmt.Sprintf("%x", sha256.Sum256(canonical))
}

func (f deploymentAPIFixture) insertRelease(t *testing.T, appID, state string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,repository_owner,repository_name,tracked_ref,resolved_sha,source_commit_sha,source_branch,compose_path,archive_sha256,workspace_tree_sha256,workspace_state,configuration_revision_number) VALUES(?,?,?,'{}',?,'github',17,'owner','repository','refs/heads/main','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','main','compose.yaml','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,0)`, id, appID, state, time.Now().UTC().Format(time.RFC3339Nano), state)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
