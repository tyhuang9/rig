package generatedimage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func manualCompilerFixture(t *testing.T) compilerFixture {
	t.Helper()
	fixture := newCompilerFixture(t)
	for _, name := range []string{"package.json", "package-lock.json"} {
		if err := os.Remove(filepath.Join(fixture.release.WorkspacePath, name)); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := sourceinspection.InspectLocal(fixture.release.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	setup := projectanalysis.DeploymentSetup{Components: []projectanalysis.SetupComponent{{ID: "app", Technology: "node", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}}}
	plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	fixture.revision.Plan = plan
	fixture.revision.CanonicalDigest, err = deploymentplans.CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.compiler.plans = compilerPlanReader{revision: fixture.revision}
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		for _, arg := range request.Args {
			if strings.Contains(arg, "rig-install-command") || strings.Contains(arg, "rig-build-command") {
				t.Fatal("skipped phase received a build secret")
			}
		}
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("1", 64)), 0o600)
	}
	return fixture
}

func TestCompilerBuildsUndetectedManualPlanAndCompatibleLaterSources(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "initial"
		if changed {
			name = "compatible-source-change"
		}
		t.Run(name, func(t *testing.T) {
			fixture := manualCompilerFixture(t)
			if changed {
				writeTestFile(t, filepath.Join(fixture.release.WorkspacePath, "package.json"), `{"scripts":{"start":"different-detected-command"},"engines":{"node":">=24"}}`)
			}
			artifact, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
			if err != nil || artifact.State != ArtifactReady {
				t.Fatalf("manual compiler: %v %#v", err, artifact)
			}
			if fixture.releaseReader.calls != 2 || fixture.builder.calls != 1 {
				t.Fatal("manual plan bypassed normal integrity/build lifecycle")
			}
		})
	}
}

func TestManualCompilerRetainsPinRuntimeAndWorkspaceChecks(t *testing.T) {
	for _, condition := range []string{"revision", "provider", "repository", "engine", "workspace-changed"} {
		t.Run(condition, func(t *testing.T) {
			fixture := manualCompilerFixture(t)
			switch condition {
			case "revision":
				fixture.revision.RevisionNumber++
			case "provider":
				fixture.revision.Plan.Source.Provider = "github"
			case "repository":
				fixture.revision.Plan.Source.RepositoryID = 99
			case "engine":
				writeTestFile(t, filepath.Join(fixture.release.WorkspacePath, "package.json"), `{"engines":{"node":">=30"}}`)
			case "workspace-changed":
				fixture.releaseReader.beforeSecond = func() { fixture.releaseReader.release.WorkspaceTreeSHA256 = strings.Repeat("d", 64) }
			}
			fixture.compiler.plans = compilerPlanReader{revision: fixture.revision}
			_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
			expected := "deployment_plan_review_required"
			if condition == "workspace-changed" {
				expected = string(DiagnosticSourceIntegrityFailed)
			}
			if !IsCompileCode(err, expected) || fixture.builder.calls != 0 || fixture.runner.request.Executable != "" {
				t.Fatalf("unsafe manual compile %s: %v", condition, err)
			}
		})
	}
}
