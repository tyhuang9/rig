package releasesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func TestUndetectedManualSetupMaterializesAndPinsHistoricalRevisions(t *testing.T) {
	m, db, dataRoot, appID, actorID, source := localMaterializerFixture(t, false)
	if err := os.Remove(filepath.Join(source, "deploy", "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "server.js"), []byte("// no framework metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := sourceinspection.InspectLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	setup := projectanalysis.DeploymentSetup{Components: []projectanalysis.SetupComponent{{ID: "app", Technology: "node", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}}}
	plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	store, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := store.Replace(context.Background(), appID, actorID, deploymentplans.ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.MaterializeLocal(context.Background(), appID, source)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeploymentPlanRevisionID != firstPlan.ID || first.ComposePath != "" {
		t.Fatalf("wrong release pin: %#v", first)
	}
	plan.Components[0].RunCommand = "node server.js --next"
	secondPlan, err := store.Replace(context.Background(), appID, actorID, deploymentplans.ReplaceInput{ExpectedRevisionNumber: 1, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.MaterializeLocal(context.Background(), appID, source)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.DeploymentPlanRevisionID != secondPlan.ID {
		t.Fatal("new plan reused an old release")
	}
	if err := m.validateMaterializedWorkspace(context.Background(), first, first.WorkspacePath); err != nil {
		t.Fatalf("historical source could no longer be used: %v", err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"engines":{"node":">=30"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MaterializeLocal(context.Background(), appID, source); !IsCode(err, "deployment_plan_review_required") {
		t.Fatalf("incompatible runtime was not stopped: %v", err)
	}
}
