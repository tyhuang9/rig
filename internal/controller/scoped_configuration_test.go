package controller_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func TestScopedConfigurationAPIRequiresPlanAndSeparatesComponentPurposes(t *testing.T) {
	root := t.TempDir()
	stateRoot, sourceRoot := filepath.Join(root, "state"), filepath.Join(root, "source")
	writeFixtureFile(t, sourceRoot, "api/server.js", "// backend fixture")
	writeFixtureFile(t, sourceRoot, "site/public/index.html", "site fixture")
	db, err := database.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.New(db)
	bootstrap, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := authService.Bootstrap(bootstrap, "admin", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	appStore := apps.New(db)
	app, err := appStore.Create("Scoped fixture", "", sourceRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := sourceinspection.InspectLocal(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	setup := projectanalysis.DeploymentSetup{Components: []projectanalysis.SetupComponent{
		{ID: "site", Technology: "static", RootDirectory: "site", PackageManager: "npm", NodeVersion: "24", OutputDirectory: "public", InternalPort: 8080, HealthProbe: "/"},
		{ID: "api", Technology: "node", RootDirectory: "api", PackageManager: "npm", NodeVersion: "24", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/healthz"},
	}}
	plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	planStore, err := deploymentplans.New(db, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := planStore.Replace(context.Background(), app.ID, user.ID, deploymentplans.ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	configStore, err := appconfig.New(db, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&controller.Server{Auth: authService, Apps: appStore, Jobs: jobs.New(db), Machines: machineStore, DeploymentPlans: planStore, Configuration: configStore, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	path := "/api/v1/apps/" + app.ID + "/scoped-configuration"
	save := map[string]any{
		"expectedRevisionNumber": 0, "planRevisionId": revision.ID, "planRevisionNumber": revision.RevisionNumber,
		"publicBuildDisclosureAcknowledged": true,
		"entries": []map[string]any{
			{"key": "DATABASE_URL", "phase": "runtime", "targetComponent": "api", "sensitive": true, "value": "synthetic-database-secret"},
			{"key": "VITE_BUILD_LABEL", "phase": "build", "targetComponent": "site", "sensitive": false, "value": "first-label"},
		}, "remove": []any{},
	}
	missingAcknowledgment := map[string]any{}
	for key, value := range save {
		missingAcknowledgment[key] = value
	}
	missingAcknowledgment["publicBuildDisclosureAcknowledged"] = false
	denied := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, path, missingAcknowledgment)
	if denied.Code != http.StatusUnprocessableEntity || !jsonProblemCode(denied.Body.Bytes(), "invalid_configuration") {
		t.Fatalf("missing public disclosure: %d %s", denied.Code, denied.Body.String())
	}
	secretBuild := map[string]any{}
	for key, value := range save {
		secretBuild[key] = value
	}
	secretBuild["entries"] = []map[string]any{{"key": "VITE_PRIVATE", "phase": "build", "targetComponent": "site", "sensitive": true, "value": "must-not-reach-build"}}
	badScope := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, path, secretBuild)
	if badScope.Code != http.StatusUnprocessableEntity || !jsonProblemCode(badScope.Body.Bytes(), "invalid_configuration") || strings.Contains(badScope.Body.String(), "must-not-reach-build") {
		t.Fatalf("secret build scope: %d %s", badScope.Code, badScope.Body.String())
	}
	wrongPlan := map[string]any{}
	for key, value := range save {
		wrongPlan[key] = value
	}
	wrongPlan["planRevisionNumber"] = revision.RevisionNumber + 1
	conflict := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, path, wrongPlan)
	if conflict.Code != http.StatusConflict || !jsonProblemCode(conflict.Body.Bytes(), "configuration_review_required") {
		t.Fatalf("stale plan: %d %s", conflict.Code, conflict.Body.String())
	}
	saved := authenticatedJSONRequest(t, handler, session, http.MethodPut, path, save)
	if saved.Header().Get("Cache-Control") != "no-store" || strings.Contains(saved.Body.String(), "synthetic-database-secret") || !strings.Contains(saved.Body.String(), `"formatVersion":2`) || !strings.Contains(saved.Body.String(), `"deploymentPlanRevisionId":"`+revision.ID+`"`) || !strings.Contains(saved.Body.String(), `"deploymentPlanRevisionNumber":1`) {
		t.Fatalf("scoped save: %d %s", saved.Code, saved.Body.String())
	}
	read := authenticatedJSONRequest(t, handler, session, http.MethodGet, "/api/v1/apps/"+app.ID+"/configuration", nil)
	if read.Header().Get("Cache-Control") != "no-store" || strings.Contains(read.Body.String(), "synthetic-database-secret") || !strings.Contains(read.Body.String(), `"deploymentPlanRevisionId":"`+revision.ID+`"`) {
		t.Fatalf("scoped read: %d %s", read.Code, read.Body.String())
	}
	legacyWrite := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, "/api/v1/apps/"+app.ID+"/configuration", map[string]any{"expectedRevisionNumber": 1, "variables": []any{}, "secrets": []any{}, "remove": []any{}})
	if legacyWrite.Code != http.StatusConflict || !jsonProblemCode(legacyWrite.Body.Bytes(), "configuration_review_required") {
		t.Fatalf("legacy overwrite of scoped head: %d %s", legacyWrite.Code, legacyWrite.Body.String())
	}
	identity, err := configStore.RevisionIdentity(context.Background(), app.ID)
	if err != nil || identity.RevisionNumber != 1 {
		t.Fatalf("scoped identity: %+v %v", identity, err)
	}
	serverRuntime, err := configStore.ExportComponentRuntimeForExecution(context.Background(), app.ID, identity.RevisionID, 1, revision.ID, revision.RevisionNumber, "api")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serverRuntime.Environment), "synthetic-database-secret") || strings.Contains(string(serverRuntime.Environment), "VITE_BUILD_LABEL") {
		t.Fatal("server runtime scope crossed build or omitted its own secret")
	}
	serverRuntime.Clear()
	staticRuntime, err := configStore.ExportComponentRuntimeForExecution(context.Background(), app.ID, identity.RevisionID, 1, revision.ID, revision.RevisionNumber, "site")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(staticRuntime.Environment), "synthetic-database-secret") || strings.Contains(string(staticRuntime.Environment), "VITE_BUILD_LABEL") {
		t.Fatal("static runtime received server or build values")
	}
	staticRuntime.Clear()
	build, err := configStore.ExportComponentBuildForExecution(context.Background(), app.ID, identity.RevisionID, 1, revision.ID, revision.RevisionNumber, "site")
	if err != nil {
		t.Fatal(err)
	}
	if len(build.PublicBuildValues) != 1 || build.PublicBuildValues[0].Key != "VITE_BUILD_LABEL" || build.PublicBuildValues[0].Value != "first-label" || strings.Contains(string(build.Environment), "synthetic-database-secret") {
		t.Fatalf("public build values crossed scope: %+v", build.PublicBuildValues)
	}
	build.Clear()
}
