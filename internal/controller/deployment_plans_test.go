package controller_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
)

func TestDeploymentPlanAPIAnalyzesAcceptsAndRejectsStaleState(t *testing.T) {
	handler, session, _, app, source := deploymentPlanAPIFixture(t, map[string]string{
		"package.json":      `{"packageManager":"npm@11","scripts":{"build":"vite build"},"dependencies":{"vite":"6"}}`,
		"package-lock.json": `{"lockfileVersion":3}`,
	})

	inspection := inspectLocalProject(t, handler, session, source)
	if inspection.Analysis.Source.Type != "local" || inspection.Analysis.ResolvedDigest != inspection.Analysis.StructuralFingerprint || len(inspection.Analysis.Candidates) != 1 {
		t.Fatalf("analysis identity = %#v", inspection.Analysis)
	}
	candidate := inspection.Analysis.Candidates[0]
	if candidate.Status != "ready" || !candidate.Install.Present || len(candidate.Components) != 1 {
		t.Fatalf("candidate = %#v", candidate)
	}
	component := candidate.Components[0]
	if component.Migration.Present || !component.InternalPort.Present || !component.HealthProbe.Present {
		t.Fatalf("optional analysis values are ambiguous: %#v", component)
	}
	port, err := strconv.Atoi(component.InternalPort.Value)
	if err != nil {
		t.Fatal(err)
	}
	buildCommand := `npm run build && node -e "require('fs').writeFileSync('rig-command-ran','x')"`
	request := apicontract.AcceptDeploymentPlanRequest{
		ExpectedRevisionNumber: 0, ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
		ExpectedCandidateDigest: candidate.Digest, CandidateID: candidate.ID, PackageManager: candidate.PackageManager.Name,
		InstallBehavior: candidate.Install.Command, MigrationCommand: "",
		Components: []apicontract.DeploymentPlanComponentInput{{
			ComponentID: component.ID, BuildCommand: buildCommand, RunCommand: component.Run.Command,
			NodeVersion: candidate.NodeVersion.Value, InternalPort: port, HealthProbe: component.HealthProbe.Path,
		}},
	}
	saved := authenticatedJSONRequest(t, handler, session, http.MethodPut, "/api/v1/apps/"+app.ID+"/deployment-plan", request)
	if saved.Code != http.StatusOK || saved.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("accept=%d %s headers=%v", saved.Code, saved.Body.String(), saved.Header())
	}
	var revision apicontract.DeploymentPlanRevision
	if err := json.Unmarshal(saved.Body.Bytes(), &revision); err != nil {
		t.Fatal(err)
	}
	if revision.RevisionNumber != 1 || revision.Source.Provider != "local" || revision.Components[0].BuildCommand != buildCommand || revision.Migration.Present {
		t.Fatalf("revision = %#v", revision)
	}
	if _, err := os.Stat(filepath.Join(source, "rig-command-ran")); !os.IsNotExist(err) {
		t.Fatalf("review command executed on the host: %v", err)
	}

	current := authenticatedRequest(t, handler, session, http.MethodGet, "/api/v1/apps/"+app.ID+"/deployment-plan", "")
	if current.Code != http.StatusOK || current.Header().Get("Cache-Control") != "no-store" || current.Body.String() != saved.Body.String() {
		t.Fatalf("get=%d %s headers=%v", current.Code, current.Body.String(), current.Header())
	}

	writeFixtureFile(t, source, "package.json", `{"packageManager":"npm@11","scripts":{"build":"vite build --outDir public"},"dependencies":{"vite":"6"}}`)
	request.ExpectedRevisionNumber = 1
	stale := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, "/api/v1/apps/"+app.ID+"/deployment-plan", request)
	if stale.Code != http.StatusConflict || !jsonProblemCode(stale.Body.Bytes(), "deployment_plan_review_required") {
		t.Fatalf("stale accept=%d %s", stale.Code, stale.Body.String())
	}
}

func TestDeploymentPlanMigrationApprovalIsSeparateAndCASProtected(t *testing.T) {
	handler, session, _, app, source := deploymentPlanAPIFixture(t, map[string]string{
		"package.json": `{
			"packageManager":"npm@11","scripts":{"build":"tsc","start":"node dist/index.js"},
			"dependencies":{"express":"5","prisma":"6"}
		}`,
		"package-lock.json":    `{"lockfileVersion":3}`,
		"prisma/schema.prisma": `datasource db { provider = "postgresql" url = env("DATABASE_URL") } model App { id Int @id }`,
	})
	inspection := inspectLocalProject(t, handler, session, source)
	candidate := inspection.Analysis.Candidates[0]
	component := candidate.Components[0]
	if candidate.Status != "needs_input" || !component.Migration.Present {
		t.Fatalf("migration candidate = %#v", candidate)
	}
	request := apicontract.AcceptDeploymentPlanRequest{
		ExpectedRevisionNumber: 0, ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
		ExpectedCandidateDigest: candidate.Digest, CandidateID: candidate.ID, PackageManager: candidate.PackageManager.Name,
		InstallBehavior: candidate.Install.Command, MigrationCommand: component.Migration.Command,
		Components: []apicontract.DeploymentPlanComponentInput{{
			ComponentID: component.ID, BuildCommand: component.Build.Command, RunCommand: component.Run.Command,
			NodeVersion: candidate.NodeVersion.Value, InternalPort: 3000, HealthProbe: "/health",
		}},
	}
	saved := authenticatedJSONRequest(t, handler, session, http.MethodPut, "/api/v1/apps/"+app.ID+"/deployment-plan", request)
	if saved.Code != http.StatusOK {
		t.Fatalf("accept=%d %s", saved.Code, saved.Body.String())
	}
	var revision apicontract.DeploymentPlanRevision
	if err := json.Unmarshal(saved.Body.Bytes(), &revision); err != nil {
		t.Fatal(err)
	}
	if !revision.Migration.Present || revision.Migration.ApprovalStatus != "pending" || revision.Migration.ComponentName != component.ID || revision.Migration.RootDirectory != "." || len(revision.Migration.EnvironmentKeys) != 1 || revision.Migration.EnvironmentKeys[0] != "DATABASE_URL" {
		t.Fatalf("migration was not pending: %#v", revision.Migration)
	}
	approval := apicontract.ApproveDeploymentPlanMigrationRequest{RevisionID: revision.RevisionID, RevisionNumber: revision.RevisionNumber, ExpectedApprovalRevision: 0}
	approved := authenticatedJSONRequest(t, handler, session, http.MethodPost, "/api/v1/apps/"+app.ID+"/deployment-plan/migration-approval", approval)
	if approved.Code != http.StatusOK || approved.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("approve=%d %s", approved.Code, approved.Body.String())
	}
	if err := json.Unmarshal(approved.Body.Bytes(), &revision); err != nil || revision.Migration.ApprovalStatus != "approved" || revision.Migration.ApprovedBy == "" || revision.Migration.ApprovedAt == "" {
		t.Fatalf("approved migration = %#v (%v)", revision.Migration, err)
	}
	conflict := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPost, "/api/v1/apps/"+app.ID+"/deployment-plan/migration-approval", approval)
	if conflict.Code != http.StatusConflict || !jsonProblemCode(conflict.Body.Bytes(), "migration_approval_conflict") {
		t.Fatalf("second approval=%d %s", conflict.Code, conflict.Body.String())
	}
}

func TestDeploymentPlanMutationsRequireAdministrator(t *testing.T) {
	handler, admin, operator, app, _ := deploymentPlanAPIFixture(t, map[string]string{
		"package.json":      `{"packageManager":"npm@11","scripts":{"start":"node server.js"},"dependencies":{"express":"5"}}`,
		"package-lock.json": `{"lockfileVersion":3}`,
	})
	denied := rawAuthenticatedJSONRequest(t, handler, operator, http.MethodPut, "/api/v1/apps/"+app.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{})
	if denied.Code != http.StatusForbidden || !jsonProblemCode(denied.Body.Bytes(), "deployment_plan_forbidden") || denied.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("operator acceptance=%d %s headers=%v", denied.Code, denied.Body.String(), denied.Header())
	}
	current := rawAuthenticatedJSONRequest(t, handler, admin, http.MethodGet, "/api/v1/apps/"+app.ID+"/deployment-plan", nil)
	if current.Code != http.StatusNotFound || !jsonProblemCode(current.Body.Bytes(), "deployment_plan_not_found") {
		t.Fatalf("operator acceptance changed the plan head: %d %s", current.Code, current.Body.String())
	}
	migrationDenied := rawAuthenticatedJSONRequest(t, handler, operator, http.MethodPost, "/api/v1/apps/"+app.ID+"/deployment-plan/migration-approval", apicontract.ApproveDeploymentPlanMigrationRequest{})
	if migrationDenied.Code != http.StatusForbidden || !jsonProblemCode(migrationDenied.Body.Bytes(), "deployment_plan_forbidden") || migrationDenied.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("operator migration approval=%d %s headers=%v", migrationDenied.Code, migrationDenied.Body.String(), migrationDenied.Header())
	}
}

func deploymentPlanAPIFixture(t *testing.T, files map[string]string) (http.Handler, auth.Session, auth.Session, apps.Application, string) {
	t.Helper()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	sourceRoot := filepath.Join(root, "source")
	for name, body := range files {
		writeFixtureFile(t, sourceRoot, name, body)
	}
	db, err := database.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authService := auth.New(db)
	token, _ := authService.EnsureBootstrapToken()
	_, session, err := authService.Bootstrap(token, "admin", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	const operatorID = "99999999-9999-4999-8999-999999999999"
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		SELECT ?, 'operator', passphrase_hash, 'operator', datetime('now'), datetime('now') FROM users WHERE username='admin'`, operatorID); err != nil {
		t.Fatal(err)
	}
	operatorSession, err := authService.NewSession(operatorID)
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	appStore := apps.New(db)
	app, err := appStore.Create("Generated fixture", "", sourceRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	planStore, err := deploymentplans.New(db, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobs.New(db), Machines: machineStore, DeploymentPlans: planStore,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()
	return handler, session, operatorSession, app, sourceRoot
}

func inspectLocalProject(t *testing.T, handler http.Handler, session auth.Session, source string) apicontract.InspectResponse {
	t.Helper()
	response := authenticatedJSONRequest(t, handler, session, http.MethodPost, "/api/v1/apps/import/inspect", map[string]string{"sourcePath": source})
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("inspect=%d %s headers=%v", response.Code, response.Body.String(), response.Header())
	}
	var result apicontract.InspectResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func authenticatedJSONRequest(t *testing.T, handler http.Handler, session auth.Session, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	response := rawAuthenticatedJSONRequest(t, handler, session, method, path, body)
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	return response
}

func rawAuthenticatedJSONRequest(t *testing.T, handler http.Handler, session auth.Session, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	request.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", session.CSRF)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func writeFixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jsonProblemCode(body []byte, expected string) bool {
	var problem struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(body, &problem) == nil && problem.Code == expected
}
