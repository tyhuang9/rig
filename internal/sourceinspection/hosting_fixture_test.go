package sourceinspection_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func TestHostingNotesFixtureAcceptsReviewedFrontendAndBackendSetup(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "hosting-notes")
	result, err := sourceinspection.InspectLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source.ComposePath != "" {
		t.Fatalf("external dependency harness selected as app Compose: %q", result.Source.ComposePath)
	}
	var generated *projectanalysis.DeploymentPlanCandidate
	for index := range result.Analysis.Candidates {
		candidate := &result.Analysis.Candidates[index]
		if candidate.Kind == projectanalysis.PlanKindJavaScript {
			generated = candidate
			break
		}
	}
	if generated == nil || len(generated.Components) != 2 {
		t.Fatalf("expected generated API and frontend candidates, got %+v", result.Analysis.Candidates)
	}
	roots := map[string]string{}
	for _, component := range generated.Components {
		roots[component.RootDirectory] = component.Kind
	}
	if roots["api"] != projectanalysis.ComponentServer || roots["frontend"] != projectanalysis.ComponentStatic {
		t.Fatalf("wrong generated fixture components: %+v", roots)
	}

	setupBody, err := os.ReadFile(filepath.Join(root, "rig-setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var setup projectanalysis.DeploymentSetup
	if err := json.Unmarshal(setupBody, &setup); err != nil {
		t.Fatal(err)
	}
	plan, _, err := deploymentplans.AcceptSetup(result.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: result.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != deploymentplans.StrategyGeneratedNode || len(plan.Components) != 2 {
		t.Fatalf("unexpected accepted plan: %+v", plan)
	}
	for _, component := range plan.Components {
		switch component.Name {
		case "api":
			if component.Role != "server" || component.RunCommand != "node src/server.js" || component.HealthProbe != "/readyz" || component.InstallDirectory != "api" {
				t.Fatalf("incorrect API execution plan: %+v", component)
			}
		case "frontend":
			if component.Role != "static" || component.BuildCommand != "pnpm build" || component.StaticOutputDirectory != "dist" || component.InstallDirectory != "frontend" {
				t.Fatalf("incorrect frontend execution plan: %+v", component)
			}
		default:
			t.Fatalf("unexpected component: %+v", component)
		}
	}
}
