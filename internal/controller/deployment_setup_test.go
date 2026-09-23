package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/githubapp"
)

func explicitSetup() *apicontract.DeploymentSetupInput {
	return &apicontract.DeploymentSetupInput{Components: []apicontract.DeploymentSetupComponentInput{{ID: "app", Technology: "node", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24", InstallCommand: "", BuildCommand: "", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health"}}}
}

func TestDeploymentSetupAPIAcceptsUndetectedAppAndRejectsStaleOrInvalidSetup(t *testing.T) {
	handler, session, _, app, source := deploymentPlanAPIFixture(t, map[string]string{"server.js": "// metadata only"})
	setup := explicitSetup()
	inspect := func() apicontract.InspectResponse {
		t.Helper()
		response := authenticatedJSONRequest(t, handler, session, http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{SourcePath: source, Setup: setup})
		var result apicontract.InspectResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	inspection := inspect()
	if len(inspection.Findings) != 0 || inspection.Source.ComposePath != "" || len(inspection.Analysis.Candidates) != 1 {
		t.Fatalf("manual inspection: %#v", inspection)
	}
	candidate := inspection.Analysis.Candidates[0]
	request := apicontract.AcceptDeploymentPlanRequest{Setup: setup, CandidateID: candidate.ID, ExpectedCandidateDigest: candidate.Digest, ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint}
	endpoint := "/api/v1/apps/" + app.ID + "/deployment-plan"
	response := authenticatedJSONRequest(t, handler, session, http.MethodPut, endpoint, request)
	var revision apicontract.DeploymentPlanRevision
	if err := json.Unmarshal(response.Body.Bytes(), &revision); err != nil {
		t.Fatal(err)
	}
	if revision.Setup == nil || revision.Setup.Components[0].StartCommand != "node server.js" || revision.Setup.Components[0].InstallCommand != "" || revision.RevisionNumber != 1 {
		t.Fatalf("saved setup=%#v", revision.Setup)
	}
	stale := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, endpoint, request)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale revision=%d %s", stale.Code, stale.Body.String())
	}
	request.ExpectedRevisionNumber = 1
	setup.Components[0].StartCommand = "node changed.js"
	stale = rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, endpoint, request)
	if stale.Code != http.StatusConflict || !jsonProblemCode(stale.Body.Bytes(), "deployment_plan_review_required") {
		t.Fatal("changed settings accepted with old candidate digest")
	}
	setup.Components[0].StartCommand = "node server.js"
	writeFixtureFile(t, source, "package.json", `{"engines":{"node":">=24"}}`)
	stale = rawAuthenticatedJSONRequest(t, handler, session, http.MethodPut, endpoint, request)
	if stale.Code != http.StatusConflict {
		t.Fatal("changed source accepted without reinspection")
	}
	for _, root := range []string{"missing", "../escape", "server.js"} {
		setup.Components[0].RootDirectory = root
		invalid := rawAuthenticatedJSONRequest(t, handler, session, http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{SourcePath: source, Setup: setup})
		if invalid.Code != http.StatusUnprocessableEntity || !jsonProblemCode(invalid.Body.Bytes(), "invalid_deployment_setup") {
			t.Fatalf("invalid root %q: %d %s", root, invalid.Code, invalid.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(source, "changed.js")); !os.IsNotExist(err) {
		t.Fatal("setup executed repository commands")
	}
}

func TestGitHubManualSetupCreatesDraftWithoutInferredCandidate(t *testing.T) {
	harness := newSourceHarness(t, true)
	owner := harness.sessionUserID(t, harness.session)
	started, err := harness.service.Start(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	harness.clock.now = harness.clock.now.Add(5 * time.Second)
	connection, err := harness.service.Poll(context.Background(), owner, started.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	harness.provider.repository = githubapp.Repository{ID: 77, Owner: "owner", Name: "manual", DefaultBranch: "main"}
	harness.provider.branch = githubapp.Branch{Name: "main", SHA: sha}
	harness.provider.tree = githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "server.js", Type: "blob", Mode: "100644", SHA: sha}}}
	source := apicontract.GitHubSource{ConnectionID: connection.ID, InstallationID: 9, RepositoryID: 77, Branch: "main"}
	response := authenticatedJSONRequest(t, harness.handler, harness.session, http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{Name: "Manual GitHub app", GithubSource: source, Setup: explicitSetup()})
	var app apicontract.Application
	if err := json.Unmarshal(response.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	if app.Source.ComposePath != "" || app.Source.RepositoryID != 77 {
		t.Fatalf("wrong source: %#v", app.Source)
	}
	// A branch advancing to a different commit invalidates the reviewed setup,
	// even when its filenames and package metadata have not changed.
	inspected := authenticatedJSONRequest(t, harness.handler, harness.session, http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: source, Setup: explicitSetup()})
	var inspection apicontract.InspectResponse
	if err := json.Unmarshal(inspected.Body.Bytes(), &inspection); err != nil {
		t.Fatal(err)
	}
	candidate := inspection.Analysis.Candidates[len(inspection.Analysis.Candidates)-1]
	harness.provider.branch.SHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stale := rawAuthenticatedJSONRequest(t, harness.handler, harness.session, http.MethodPut, "/api/v1/apps/"+app.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{Setup: explicitSetup(), CandidateID: candidate.ID, ExpectedCandidateDigest: candidate.Digest, ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint})
	if stale.Code != http.StatusConflict || !jsonProblemCode(stale.Body.Bytes(), "deployment_plan_review_required") {
		t.Fatalf("changed commit accepted: %d %s", stale.Code, stale.Body.String())
	}
	// GitHub reports symbolic links as blobs; they are not evidence of a usable root.
	harness.provider.tree.Entries[0].Mode = "120000"
	invalid := rawAuthenticatedJSONRequest(t, harness.handler, harness.session, http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: source, Setup: explicitSetup()})
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatal("symlink-only source accepted")
	}
}
