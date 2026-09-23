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

func TestCompilerStagesHostingNotesFixtureWithoutDocker(t *testing.T) {
	fixture := newCompilerFixture(t)
	workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
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
	digest, err := deploymentplans.CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture.release.WorkspacePath = workspace
	fixture.release.ResolvedSHA = inspection.Analysis.StructuralFingerprint
	fixture.releaseReader.release = fixture.release
	fixture.revision.Plan, fixture.revision.CanonicalDigest = plan, digest
	fixture.compiler, err = NewCompiler(fixture.releaseReader, compilerPlanReader{revision: fixture.revision}, fixture.config, fixture.artifacts, fixture.temporary, fixture.builder, fixture.runner, CompilerOptions{BuildTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	configurationID := uuid.NewString()
	component := "api"
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		contextDirectory := request.Args[len(request.Args)-1]
		for _, path := range []string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "api/src/server.js", "frontend/package.json"} {
			if _, err := os.Stat(filepath.Join(contextDirectory, "source", filepath.FromSlash(path))); err != nil {
				t.Fatalf("fixture source %q absent from generated build context: %v", path, err)
			}
		}
		for _, path := range []string{"node_modules", "api/node_modules", "frontend/node_modules", "frontend/dist", "harness/certs"} {
			if _, err := os.Stat(filepath.Join(contextDirectory, "source", filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("local dependency or generated output %q reached build context: %v", path, err)
			}
		}
		assertFileEquals(t, filepath.Join(contextDirectory, "rig", "root.path"), component)
		assertFileEquals(t, filepath.Join(contextDirectory, "rig", "install.path"), component)
		assertFileEquals(t, secretSource(t, request.Args, "rig-install-command"), "pnpm install --frozen-lockfile")
		if component == "api" {
			for _, arg := range request.Args {
				if strings.Contains(arg, "rig-public-build-values") {
					t.Fatal("public frontend build values reached API build")
				}
			}
		} else {
			assertFileEquals(t, secretSource(t, request.Args, "rig-build-command"), "pnpm build")
			body, err := os.ReadFile(secretSource(t, request.Args, "rig-public-build-values"))
			if err != nil {
				t.Fatal(err)
			}
			var values []appconfig.ValueInput
			if err := json.Unmarshal(body, &values); err != nil || len(values) != 1 || values[0].Key != "VITE_BUILD_LABEL" || values[0].Value != "candidate-A" {
				t.Fatalf("frontend public build values=%+v err=%v", values, err)
			}
		}
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("4", 64)), 0o600)
	}
	for _, name := range []string{"api", "frontend"} {
		component = name
		fixture.config.values = nil
		if name == "frontend" {
			fixture.config.values = []appconfig.ValueInput{{Key: "VITE_BUILD_LABEL", Value: "candidate-A"}}
		}
		artifact, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, name, configurationID, 1)
		if err != nil || artifact.State != ArtifactReady {
			t.Fatalf("fixture component %s did not stage: artifact=%+v err=%v", name, artifact, err)
		}
	}
}
