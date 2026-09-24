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

func TestCompilerStagesStandaloneRecipeFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, component, technology, manager, install, build, run, output string
		port                                                              uint16
		included, excluded                                                []string
	}{
		{name: "node", component: "api", technology: "node", manager: "npm", install: "npm ci", run: "node server.js", port: 3000,
			included: []string{"package.json", "package-lock.json", "server.js"}, excluded: []string{"node_modules"}},
		{name: "vite", component: "web", technology: "static", manager: "yarn", install: "corepack yarn install --immutable", build: "corepack yarn build", run: "rig-static --root 'dist' --port 8080", output: "dist", port: 8080,
			included: []string{"package.json", "yarn.lock", ".yarnrc.yml", "index.html", "src/main.js"}, excluded: []string{"node_modules", "dist", ".yarn/install-state.gz"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-"+map[string]string{"node": "node-api", "vite": "vite-only"}[tc.name]))
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := sourceinspection.InspectLocal(workspace)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(workspace, "rig-setup.json"))
			if err != nil {
				t.Fatal(err)
			}
			var setup projectanalysis.DeploymentSetup
			if err := json.Unmarshal(body, &setup); err != nil {
				t.Fatal(err)
			}
			plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Components) != 1 {
				t.Fatalf("components = %+v", plan.Components)
			}
			component := plan.Components[0]
			if component.Technology != tc.technology || component.PackageManager != tc.manager || component.InstallBehavior != tc.install || component.BuildCommand != tc.build || component.RunCommand != tc.run || component.StaticOutputDirectory != tc.output || component.InternalPort != tc.port {
				t.Fatalf("accepted component = %+v", component)
			}
			digest, err := deploymentplans.CanonicalDigest(plan)
			if err != nil {
				t.Fatal(err)
			}
			fixture.release.WorkspacePath = workspace
			fixture.release.ResolvedSHA = inspection.Analysis.StructuralFingerprint
			fixture.releaseReader.release = fixture.release
			fixture.revision.Plan, fixture.revision.CanonicalDigest = plan, digest
			if tc.name == "vite" {
				fixture.config.values = []appconfig.ValueInput{{Key: "VITE_BUILD_MARKER", Value: "vite-public-A"}}
			}
			fixture.compiler, err = NewCompiler(fixture.releaseReader, compilerPlanReader{revision: fixture.revision}, fixture.config, fixture.artifacts, fixture.temporary, fixture.builder, fixture.runner, CompilerOptions{BuildTimeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
				contextDirectory := request.Args[len(request.Args)-1]
				for _, name := range tc.included {
					if _, err := os.Stat(filepath.Join(contextDirectory, "source", filepath.FromSlash(name))); err != nil {
						t.Fatalf("missing staged %q: %v", name, err)
					}
				}
				for _, name := range tc.excluded {
					if _, err := os.Stat(filepath.Join(contextDirectory, "source", name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("local output %q reached context: %v", name, err)
					}
				}
				assertFileEquals(t, filepath.Join(contextDirectory, "rig", "root.path"), ".")
				assertFileEquals(t, filepath.Join(contextDirectory, "rig", "install.path"), ".")
				assertFileEquals(t, secretSource(t, request.Args, "rig-install-command"), tc.install)
				if tc.build != "" {
					assertFileEquals(t, secretSource(t, request.Args, "rig-build-command"), tc.build)
				}
				return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("5", 64)), 0o600)
			}
			artifact, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, tc.component, uuid.NewString(), 1)
			if err != nil || artifact.State != ArtifactReady {
				t.Fatalf("artifact=%+v err=%v", artifact, err)
			}
		})
	}
}
