package generatedimage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

type compilerAttestorRunner struct {
	run      func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)
	requests []runtimeprocess.CommandRequest
}

func (r *compilerAttestorRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	return r.run(ctx, request)
}

type unusedCompilerEnvironment struct{}

func (unusedCompilerEnvironment) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("compiler attestor test unexpectedly staged runtime environment")
}

type compilerAttestorCapacity struct{}

func (compilerAttestorCapacity) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 8 << 30}, nil
}

func TestCompilerCanonicalAttestorControlsBoundedReplacement(t *testing.T) {
	if os.Getenv("RIG_TEST_COMPILER_IMAGE_ABSENCE_EXIT") == "1" {
		os.Exit(1)
	}
	helperContext, cancelHelper := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelHelper()
	helper := exec.CommandContext(helperContext, os.Args[0], "-test.run=^TestCompilerCanonicalAttestorControlsBoundedReplacement$")
	helper.Env = append(os.Environ(), "RIG_TEST_COMPILER_IMAGE_ABSENCE_EXIT=1")
	exitErr, ok := helper.Run().(*exec.ExitError)
	if !ok || exitErr.ProcessState == nil || exitErr.ExitCode() != 1 {
		t.Fatal("compiler attestor helper did not provide a completed exit-one process")
	}

	for _, test := range []struct {
		name        string
		observation string
		definitive  bool
		fabricated  bool
		cancel      bool
	}{
		{name: "exact absence", observation: "missing", definitive: true},
		{name: "valid contradiction", observation: "contradiction", definitive: true},
		{name: "caller fabricated diagnostic", fabricated: true},
		{name: "malformed observation", observation: "malformed"},
		{name: "truncated observation", observation: "truncated"},
		{name: "transport uncertainty", observation: "transport"},
		{name: "cancelled observation", observation: "exact", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			old := seedReadyCompilerArtifact(t, &fixture)
			newImageID := "sha256:" + strings.Repeat("9", 64)
			fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
				return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte(newImageID), 0o600)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.fabricated {
				fixture.compiler.validator = &compilerImageValidator{err: &generatedruntime.Error{Code: generatedruntime.DiagnosticImageUnavailable}}
			} else {
				runner := &compilerAttestorRunner{}
				runner.run = func(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
					if len(request.Args) < 2 || request.Args[0] != "image" || request.Args[1] != "inspect" {
						t.Fatalf("attestor command=%#v", request.Args)
					}
					contentID := request.Args[len(request.Args)-1]
					if contentID == old.ImageContentID {
						if test.cancel {
							cancel()
						}
						switch test.observation {
						case "missing":
							return runtimeprocess.CommandResult{Stderr: []byte("Error response from daemon: No such image: " + contentID)}, exitErr
						case "contradiction":
							return compilerImageObservation(t, &fixture, "sha256:"+strings.Repeat("8", 64)), nil
						case "malformed":
							return runtimeprocess.CommandResult{Stdout: []byte(`{"id":`)}, nil
						case "truncated":
							result := compilerImageObservation(t, &fixture, contentID)
							result.StdoutTruncated = true
							return result, nil
						case "transport":
							return runtimeprocess.CommandResult{Stderr: []byte("Cannot connect to Docker")}, errors.New("transport unavailable")
						case "exact":
							return compilerImageObservation(t, &fixture, contentID), nil
						default:
							t.Fatalf("unknown observation %q", test.observation)
						}
					}
					return compilerImageObservation(t, &fixture, contentID), nil
				}
				fixture.compiler.validator = newCompilerAttestorEngine(t, fixture.authority.repository, runner)
			}

			artifact, err := fixture.compile(ctx)
			store := NewArtifactRepository(fixture.authority.db)
			persistedOld, oldErr := store.Get(context.Background(), old.ID)
			if oldErr != nil {
				t.Fatal(oldErr)
			}
			var artifactCount int
			if countErr := fixture.authority.db.QueryRow(`SELECT COUNT(*) FROM generated_image_artifacts WHERE release_id=? AND component_id=?`, fixture.release.ID, fixture.request.ComponentName).Scan(&artifactCount); countErr != nil {
				t.Fatal(countErr)
			}
			component, componentErr := generatedruntimestate.New(fixture.authority.db).Component(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
			if componentErr != nil {
				t.Fatal(componentErr)
			}
			if test.definitive {
				if err != nil || artifact.State != ArtifactReady || artifact.AttemptNumber != old.AttemptNumber+1 || artifact.ImageContentID != newImageID || artifactCount != 2 {
					t.Fatalf("bounded replacement artifact=%#v count=%d error=%v", artifact, artifactCount, err)
				}
				if persistedOld.State != ArtifactUnavailable || persistedOld.ImageContentID != old.ImageContentID || !persistedOld.FinishedAt.Equal(old.FinishedAt) || component.State != generatedruntimestate.ComponentImageReady || component.ImageArtifactID != artifact.ID {
					t.Fatalf("replacement evidence old=%#v component=%#v", persistedOld, component)
				}
				if fixture.builderPrepareCalls() != 1 || len(fixture.runner.request.Args) == 0 {
					t.Fatal("definitive observation did not perform exactly one build")
				}
				return
			}
			if artifact != (Artifact{}) || !IsCompileCode(err, "image_validation_uncertain") || !IsCompileUncertain(err) || artifactCount != 1 {
				t.Fatalf("uncertain observation artifact=%#v count=%d error=%v", artifact, artifactCount, err)
			}
			if persistedOld != old || component.State != generatedruntimestate.ComponentPending || component.ImageArtifactID != "" || fixture.builderPrepareCalls() != 0 || len(fixture.runner.request.Args) != 0 {
				t.Fatalf("uncertainty changed evidence: old=%#v component=%#v builder=%d request=%#v", persistedOld, component, fixture.builderPrepareCalls(), fixture.runner.request.Args)
			}
			if test.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
		})
	}
}

func TestGeneratedImageConstructorsRequireConcreteConfiguredDependencies(t *testing.T) {
	fixture := newCompilerFixture(t)
	options := BuilderManagerOptions{DataRoot: t.TempDir(), DockerExecutable: filepath.Join(t.TempDir(), "docker.exe"), PrepareTimeout: time.Minute}
	var typedNilRunner *compilerCommandRunner
	if manager, err := NewBuilderManager(fixture.authority.repository, typedNilRunner, options); manager != nil || err == nil {
		t.Fatalf("typed nil runner manager=%#v error=%v", manager, err)
	}
	unconfigured := deployments.New(fixture.authority.db)
	if manager, err := NewBuilderManager(unconfigured, fixture.commandRunner, options); manager != nil || err == nil {
		t.Fatalf("unconfigured repository manager=%#v error=%v", manager, err)
	}
	var typedNilValidator *compilerImageValidator
	if compiler, err := NewCompiler(fixture.releaseReader, compilerPlanReader{revision: fixture.revision}, fixture.config, fixture.authority.repository, fixture.temporary, fixture.builder, fixture.commandRunner, typedNilValidator, CompilerOptions{BuildTimeout: time.Minute}); compiler != nil || err == nil {
		t.Fatalf("typed nil validator compiler=%#v error=%v", compiler, err)
	}
	foreignRelease, foreignRevision := defaultGeneratedImageTestSource()
	foreignAuthority := newGeneratedImageTestAuthority(t, foreignRelease, foreignRevision, "app")
	foreignBuilder, err := NewBuilderManager(foreignAuthority.repository, fixture.commandRunner, options)
	if err != nil {
		t.Fatal(err)
	}
	if compiler, err := NewCompiler(fixture.releaseReader, compilerPlanReader{revision: fixture.revision}, fixture.config, fixture.authority.repository, fixture.temporary, foreignBuilder, fixture.commandRunner, fixture.validator, CompilerOptions{BuildTimeout: time.Minute}); compiler != nil || err == nil {
		t.Fatalf("foreign manager compiler=%#v error=%v", compiler, err)
	}
}

func TestCompilerImageReadyResumeRequiresFreshExactPhysicalEvidence(t *testing.T) {
	for _, test := range []struct {
		name       string
		contradict bool
	}{
		{name: "exact image resumes"},
		{name: "contradictory image refuses without replacement", contradict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			ready := seedReadyCompilerArtifact(t, &fixture)
			state := generatedruntimestate.New(fixture.authority.db)
			if _, err := state.SetImageReady(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName, ready.ID); err != nil {
				t.Fatal(err)
			}
			runner := &compilerAttestorRunner{}
			runner.run = func(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				observed := request.Args[len(request.Args)-1]
				if test.contradict {
					observed = "sha256:" + strings.Repeat("6", 64)
				}
				return compilerImageObservation(t, &fixture, observed), nil
			}
			fixture.compiler.validator = newCompilerAttestorEngine(t, fixture.authority.repository, runner)

			artifact, err := fixture.compile(context.Background())
			persisted, persistedErr := NewArtifactRepository(fixture.authority.db).Get(context.Background(), ready.ID)
			if persistedErr != nil || persisted != ready {
				t.Fatalf("ready evidence changed: before=%#v after=%#v err=%v", ready, persisted, persistedErr)
			}
			var count int
			if countErr := fixture.authority.db.QueryRow(`SELECT COUNT(*) FROM generated_image_artifacts WHERE release_id=? AND component_id=?`, fixture.release.ID, fixture.request.ComponentName).Scan(&count); countErr != nil {
				t.Fatal(countErr)
			}
			if test.contradict {
				if artifact != (Artifact{}) || !IsCompileCode(err, "image_validation_uncertain") || count != 1 {
					t.Fatalf("attached contradiction artifact=%#v count=%d error=%v", artifact, count, err)
				}
			} else if err != nil || artifact != ready || count != 1 {
				t.Fatalf("exact resume artifact=%#v count=%d error=%v", artifact, count, err)
			}
			if fixture.builderPrepareCalls() != 0 || len(fixture.runner.request.Args) != 0 || len(runner.requests) != 1 {
				t.Fatalf("resume commands: builder=%d build=%#v inspections=%d", fixture.builderPrepareCalls(), fixture.runner.request.Args, len(runner.requests))
			}
		})
	}
}

func TestCompilerRefusesInterruptedBuildingWithoutExecutingAnotherBuild(t *testing.T) {
	fixture := newCompilerFixture(t)
	store := NewArtifactRepository(fixture.authority.db)
	_, digest, err := definitionForBuild(fixture.revision, fixture.request.ComponentName, nil)
	if err != nil {
		t.Fatal(err)
	}
	building, created, err := store.Begin(context.Background(), BeginArtifactInput{
		ReleaseID: fixture.release.ID, DeploymentPlanRevisionID: fixture.revision.ID, DeploymentPlanRevisionNumber: fixture.revision.RevisionNumber,
		ComponentID: fixture.request.ComponentName, CompilerVersion: CompilerVersion, BuildDefinitionDigest: digest,
	})
	if err != nil || !created {
		t.Fatalf("seed building artifact: created=%t err=%v", created, err)
	}
	artifact, err := fixture.compile(context.Background())
	if artifact != (Artifact{}) || !IsCompileCode(err, "build_in_progress") || !IsCompileUncertain(err) {
		t.Fatalf("interrupted build artifact=%#v error=%v", artifact, err)
	}
	persisted, persistedErr := store.Get(context.Background(), building.ID)
	if persistedErr != nil || persisted != building || fixture.builderPrepareCalls() != 0 || len(fixture.runner.request.Args) != 0 {
		t.Fatalf("interrupted evidence changed: before=%#v after=%#v err=%v builder=%d build=%#v", building, persisted, persistedErr, fixture.builderPrepareCalls(), fixture.runner.request.Args)
	}
}

func TestCompilerRevalidatesAuthorityAfterPhysicalImageObservation(t *testing.T) {
	for _, test := range []struct {
		name  string
		reuse bool
	}{
		{name: "new build result"},
		{name: "reused ready attachment", reuse: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			imageID := "sha256:" + strings.Repeat("5", 64)
			if test.reuse {
				seedReadyCompilerArtifact(t, &fixture)
			} else {
				fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
					return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte(imageID), 0o600)
				}
			}
			barrierCalls := 0
			fixture.compiler.afterImageValidated = func() {
				barrierCalls++
				invalidateGeneratedImageJobAttempt(t, fixture.authority)
			}
			artifact, err := fixture.compile(context.Background())
			if artifact != (Artifact{}) || !errors.Is(err, deploymentexecution.ErrIdentityLost) || !IsCompileCode(err, "image_result_unavailable") || barrierCalls != 1 {
				t.Fatalf("post-observation refusal artifact=%#v barrier=%d error=%v", artifact, barrierCalls, err)
			}
			persisted := fixture.latestArtifact(t)
			component, componentErr := generatedruntimestate.New(fixture.authority.db).Component(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
			wantState := ArtifactBuilding
			wantContent := ""
			if test.reuse {
				wantState, wantContent = ArtifactReady, "sha256:"+strings.Repeat("7", 64)
			}
			if componentErr != nil || persisted.State != wantState || persisted.ImageContentID != wantContent || component.State != generatedruntimestate.ComponentPending || component.ImageArtifactID != "" {
				t.Fatalf("post-observation refusal changed evidence: artifact=%#v component=%#v error=%v", persisted, component, componentErr)
			}
		})
	}
}

func TestCompilerAttemptedBuildUncertaintyPreservesBuildingEvidenceAndCauses(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     string
		wantCode string
	}{
		{name: "runner error", kind: "runner", wantCode: string(DiagnosticBuildFailed)},
		{name: "timeout", kind: "timeout", wantCode: string(DiagnosticBuildTimeout)},
		{name: "cancellation", kind: "cancel", wantCode: string(DiagnosticBuildCancelled)},
		{name: "termination failure", kind: "termination", wantCode: string(DiagnosticProcessTerminationFailed)},
		{name: "truncated output", kind: "truncated", wantCode: string(DiagnosticBuildOutputTruncated)},
		{name: "missing iid file", kind: "missing_iid", wantCode: string(DiagnosticBuildFailed)},
		{name: "malformed iid file", kind: "bad_iid", wantCode: string(DiagnosticBuildFailed)},
		{name: "image observation uncertainty", kind: "observation", wantCode: "image_validation_uncertain"},
		{name: "cleanup failure", kind: "cleanup", wantCode: string(DiagnosticInternalError)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			sentinel := errors.New("compiler-attempt-sentinel")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writeIID := func(request runtimeprocess.CommandRequest, value string) error {
				return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte(value), 0o600)
			}
			switch test.kind {
			case "runner":
				fixture.runner.err = sentinel
			case "timeout":
				fixture.runner.err = context.DeadlineExceeded
			case "cancel":
				fixture.runner.run = func(runtimeprocess.CommandRequest) error { cancel(); return sentinel }
			case "termination":
				fixture.runner.err = errors.Join(sentinel, runtimeprocess.ErrTerminationFailed)
			case "truncated":
				fixture.runner.result = runtimeprocess.CommandResult{StdoutTruncated: true}
			case "missing_iid":
			case "bad_iid":
				fixture.runner.run = func(request runtimeprocess.CommandRequest) error { return writeIID(request, "not-an-image-id") }
			case "observation":
				fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
					return writeIID(request, "sha256:"+strings.Repeat("4", 64))
				}
				fixture.validator.err = sentinel
			case "cleanup":
				fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
					return writeIID(request, "sha256:"+strings.Repeat("4", 64))
				}
				fixture.compiler.cleanup = func(*securetemp.Files) error { return sentinel }
			}
			artifact, err := fixture.compile(ctx)
			if artifact != (Artifact{}) || !IsCompileCode(err, test.wantCode) || !IsCompileUncertain(err) {
				t.Fatalf("uncertain build artifact=%#v error=%v", artifact, err)
			}
			if (test.kind == "runner" || test.kind == "cancel" || test.kind == "termination" || test.kind == "observation" || test.kind == "cleanup") && !errors.Is(err, sentinel) {
				t.Fatalf("original cause lost: %v", err)
			}
			if test.kind == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline cause lost: %v", err)
			}
			if test.kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			if test.kind == "termination" && !errors.Is(err, runtimeprocess.ErrTerminationFailed) {
				t.Fatalf("termination cause lost: %v", err)
			}
			persisted := fixture.latestArtifact(t)
			component, componentErr := generatedruntimestate.New(fixture.authority.db).Component(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
			if componentErr != nil || persisted.State != ArtifactBuilding || persisted.ImageContentID != "" || persisted.DiagnosticCode != "" || component.State != generatedruntimestate.ComponentPending || component.ImageArtifactID != "" {
				t.Fatalf("uncertain build changed evidence: artifact=%#v component=%#v error=%v", persisted, component, componentErr)
			}
		})
	}
}

func TestCompilerPreservesBuildingWhenKnownFailureObservationDoesNotCommit(t *testing.T) {
	fixture := newCompilerFixture(t)
	if _, err := fixture.authority.db.Exec(`CREATE TRIGGER generated_image_failure_fault BEFORE UPDATE OF state ON generated_image_artifacts WHEN NEW.state='failed' BEGIN SELECT RAISE(ABORT,'image failure observation fault'); END`); err != nil {
		t.Fatal(err)
	}
	fixture.releaseReader.beforeSecond = func() {
		writeTestFile(t, filepath.Join(fixture.release.WorkspacePath, "package.json"), `{"name":"demo","scripts":{"build":"changed","start":"node server.js"},"dependencies":{"express":"1.0.0"}}`)
	}
	artifact, err := fixture.compile(context.Background())
	if artifact != (Artifact{}) || !IsCompileCode(err, string(DiagnosticSourceIntegrityFailed)) || !IsCompileUncertain(err) {
		t.Fatalf("failure-observation result artifact=%#v error=%v", artifact, err)
	}
	persisted := fixture.latestArtifact(t)
	component, componentErr := generatedruntimestate.New(fixture.authority.db).Component(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
	if componentErr != nil || persisted.State != ArtifactBuilding || persisted.DiagnosticCode != "" || component.State != generatedruntimestate.ComponentPending {
		t.Fatalf("failed observation changed evidence: artifact=%#v component=%#v error=%v", persisted, component, componentErr)
	}
}

func seedReadyCompilerArtifact(t *testing.T, fixture *compilerFixture) Artifact {
	t.Helper()
	_, digest, err := definitionForBuild(fixture.revision, fixture.request.ComponentName, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewArtifactRepository(fixture.authority.db)
	artifact, created, err := store.Begin(context.Background(), BeginArtifactInput{
		ReleaseID: fixture.release.ID, DeploymentPlanRevisionID: fixture.revision.ID, DeploymentPlanRevisionNumber: fixture.revision.RevisionNumber,
		ComponentID: fixture.request.ComponentName, CompilerVersion: CompilerVersion, BuildDefinitionDigest: digest,
	})
	if err != nil || !created {
		t.Fatalf("seed reusable artifact: created=%t err=%v", created, err)
	}
	artifact, err = store.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func compilerImageObservation(t *testing.T, fixture *compilerFixture, observedContentID string) runtimeprocess.CommandResult {
	t.Helper()
	artifact := fixture.latestArtifact(t)
	definition, _, err := definitionForBuild(fixture.revision, fixture.request.ComponentName, nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		"io.rig.managed": "generated-image", "io.rig.application": fixture.request.Claim.ApplicationID(),
		"io.rig.release": fixture.request.ReleaseID, "io.rig.artifact": artifact.ID,
		"io.rig.plan": fixture.request.DeploymentPlanRevisionID, "io.rig.component": fixture.request.ComponentName,
		"io.rig.role": definition.role, "io.rig.definition": artifact.BuildDefinitionDigest,
	}
	body, err := json.Marshal(map[string]any{"id": observedContentID, "size": 1, "labels": labels, "user": "node", "workingDir": "/workspace", "entrypoint": []string{"/usr/local/bin/rig-entrypoint"}})
	if err != nil {
		t.Fatal(err)
	}
	return runtimeprocess.CommandResult{Stdout: body}
}

func newCompilerAttestorEngine(t *testing.T, repository *deployments.Repository, runner runtimeprocess.CommandRunner) *generatedruntime.Engine {
	t.Helper()
	root := t.TempDir()
	engine, err := generatedruntime.NewEngine(runner, unusedCompilerEnvironment{}, compilerAttestorCapacity{}, repository, generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: t.TempDir(), WorkingDirectory: root, CommandTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
