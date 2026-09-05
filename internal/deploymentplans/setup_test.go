package deploymentplans

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func TestAcceptedManualSetupSurvivesReanalysisRestartAndRevisionReplacement(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("api/server.js", "// no recognized manifest")
	write("site/public/index.html", "hello")
	inspect := func() sourceinspection.Result {
		t.Helper()
		got, err := sourceinspection.InspectLocal(root)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	inspection := inspect()
	setup := projectanalysis.DeploymentSetup{Components: []projectanalysis.SetupComponent{
		{ID: "a-site", Technology: "static", RootDirectory: "site", PackageManager: "npm", NodeVersion: "24", OutputDirectory: "public", InternalPort: 8080, HealthProbe: "/"},
		{ID: "z-api", Technology: "node", RootDirectory: "api", PackageManager: "pnpm", NodeVersion: "22", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health"},
	}}
	plan, _, err := AcceptSetup(inspection.Analysis, setup, SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Components[0].Role != "static" || plan.Components[1].RunCommand != "node server.js" {
		t.Fatal("component settings crossed roots after sorting")
	}
	db, dataRoot := planDB(t), t.TempDir()
	store, err := New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	write("api/package.json", `{"scripts":{"start":"something-else"},"engines":{"node":">=22"}}`)
	if difference, err := CompareAnalysis(plan, inspect().Analysis); err != nil || len(difference) != 0 {
		t.Fatalf("manual configuration overwritten: %v %v", difference, err)
	}
	store, err = New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), planTestApp)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CanonicalDigest != first.CanonicalDigest {
		t.Fatal("restart changed immutable plan")
	}
	loaded.Plan.Components[1].RunCommand = "node replacement.js"
	if _, err := store.Replace(context.Background(), planTestApp, "owner", ReplaceInput{ExpectedRevisionNumber: 1, Plan: loaded.Plan}); err != nil {
		t.Fatal(err)
	}
	historical, err := store.GetRevision(context.Background(), planTestApp, first.ID, 1)
	if err != nil || historical.Plan.Components[1].RunCommand != "node server.js" {
		t.Fatal("historical revision was rewritten")
	}
	write("api/package.json", `{"engines":{"node":">=30"}}`)
	if difference, err := CompareAnalysis(plan, inspect().Analysis); err != nil || len(difference) == 0 {
		t.Fatal("incompatible runtime did not require review")
	}
	if err := os.Rename(filepath.Join(root, "site"), filepath.Join(root, "moved-site")); err != nil {
		t.Fatal(err)
	}
	if difference, err := CompareAnalysis(plan, inspect().Analysis); err != nil || len(difference) == 0 {
		t.Fatal("missing root did not require review")
	}
}
