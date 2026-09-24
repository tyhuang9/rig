package generatedimage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func TestCompilerStagesNextFixtureWithoutDocker(t *testing.T) {
	fixture := newCompilerFixture(t)
	workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-nextjs"))
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := sourceinspection.InspectLocal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	setupBody, err := os.ReadFile(filepath.Join(workspace, "rig-setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var setup projectanalysis.DeploymentSetup
	if err := json.Unmarshal(setupBody, &setup); err != nil {
		t.Fatal(err)
	}
	plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Components) != 1 || plan.Components[0].Technology != "nextjs" || plan.Components[0].RunCommand != "npm run start" || plan.Components[0].InternalPort != 3000 {
		t.Fatalf("unexpected accepted Next.js component: %+v", plan.Components)
	}
	digest, err := deploymentplans.CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.release.WorkspacePath = workspace
	fixture.release.ResolvedSHA = inspection.Analysis.StructuralFingerprint
	fixture.releaseReader.release = fixture.release
	fixture.revision.Plan, fixture.revision.CanonicalDigest = plan, digest
	fixture.config.values = []appconfig.ValueInput{{Key: "NEXT_PUBLIC_BUILD_MARKER", Value: "next-public-A"}}
	fixture.compiler, err = NewCompiler(fixture.releaseReader, compilerPlanReader{revision: fixture.revision}, fixture.config, fixture.artifacts, fixture.temporary, fixture.builder, fixture.runner, CompilerOptions{BuildTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		contextDirectory := request.Args[len(request.Args)-1]
		for _, name := range []string{"package.json", "package-lock.json", "app/page.jsx", "app/api/runtime/route.js", "public/pixel.png"} {
			if _, err := os.Stat(filepath.Join(contextDirectory, "source", filepath.FromSlash(name))); err != nil {
				t.Fatalf("fixture source %q absent from generated build context: %v", name, err)
			}
		}
		for _, name := range []string{"node_modules", ".next"} {
			if _, err := os.Stat(filepath.Join(contextDirectory, "source", name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("local output %q reached build context: %v", name, err)
			}
		}
		assertFileEquals(t, filepath.Join(contextDirectory, "rig", "root.path"), ".")
		assertFileEquals(t, filepath.Join(contextDirectory, "rig", "install.path"), ".")
		assertFileEquals(t, secretSource(t, request.Args, "rig-install-command"), "npm ci")
		assertFileEquals(t, secretSource(t, request.Args, "rig-build-command"), "npm run build")
		body, err := os.ReadFile(secretSource(t, request.Args, "rig-public-build-values"))
		if err != nil {
			t.Fatal(err)
		}
		var values []appconfig.ValueInput
		if err := json.Unmarshal(body, &values); err != nil || len(values) != 1 || values[0].Key != "NEXT_PUBLIC_BUILD_MARKER" || values[0].Value != "next-public-A" {
			t.Fatalf("public build values=%+v err=%v", values, err)
		}
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("4", 64)), 0o600)
	}
	artifact, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "web", uuid.NewString(), 1)
	if err != nil || artifact.State != ArtifactReady {
		t.Fatalf("Next.js fixture did not stage: artifact=%+v err=%v", artifact, err)
	}
}
