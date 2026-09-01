package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/runtimeexecutor"
	"github.com/hostd/hostd/internal/sourceconnections"
)

func TestResolveRuntimeDockerExecutableOnlyForGeneratedRuntime(t *testing.T) {
	for _, test := range []struct {
		name      string
		generated bool
		wantCalls int
		wantPath  string
	}{
		{name: "disabled"},
		{name: "enabled", generated: true, wantCalls: 1, wantPath: filepath.Join(t.TempDir(), "docker")},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			path, err := resolveRuntimeDockerExecutable(config.Config{GeneratedRuntime: test.generated}, func() (string, error) {
				calls++
				return test.wantPath, nil
			})
			if err != nil || calls != test.wantCalls || path != test.wantPath {
				t.Fatalf("resolved path=%q calls=%d err=%v", path, calls, err)
			}
		})
	}
	if _, err := resolveRuntimeDockerExecutable(config.Config{GeneratedRuntime: true}, func() (string, error) {
		return "", errors.New("missing")
	}); err == nil {
		t.Fatal("generated runtime accepted a missing Docker executable")
	}
}

func TestRuntimeCompositionEnablementAndRouterSelection(t *testing.T) {
	for _, test := range []struct {
		name                     string
		fake, compose, generated bool
		wantRouter               bool
	}{
		{name: "disabled"},
		{name: "fake", fake: true},
		{name: "compose", compose: true, wantRouter: true},
		{name: "generated", generated: true, wantRouter: true},
		{name: "compose and generated", compose: true, generated: true, wantRouter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			configuration := fixture.configuration
			configuration.FakeRuntime, configuration.ComposeRuntime, configuration.GeneratedRuntime = test.fake, test.compose, test.generated
			var ingressRecovered int
			result, err := prepareRuntimeComposition(context.Background(), configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				recoverIngress: func(ctx context.Context, _ *generatedingress.Manager) error {
					if ctx.Err() != nil {
						t.Fatalf("ingress recovery context = %v", ctx.Err())
					}
					ingressRecovered++
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, isRouter := result.executor.(*runtimeexecutor.Router)
			_, isFake := result.executor.(*jobs.FakeExecutor)
			if isRouter != test.wantRouter || isFake != test.fake || (result.compose != nil) != test.compose || (result.generated != nil) != test.generated {
				t.Fatalf("composition = executor %T compose=%t generated=%t", result.executor, result.compose != nil, result.generated != nil)
			}
			if !test.fake && !test.wantRouter && result.executor != nil {
				t.Fatalf("disabled executor = %T", result.executor)
			}
			if ingressRecovered != boolInt(test.generated) {
				t.Fatalf("ingress recovery count = %d", ingressRecovered)
			}
		})
	}
}

func TestGeneratedCompositionRecoversBeforeDeploymentJobAndWorker(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	fixture.configuration.GeneratedRuntime = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls []string
	composition, err := prepareRuntimeComposition(ctx, fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
		beforeStep: func(name string) error {
			calls = append(calls, name)
			return nil
		},
		recoverIngress: func(recoveryContext context.Context, _ *generatedingress.Manager) error {
			if recoveryContext.Err() != nil {
				t.Fatalf("ingress recovery inherited cancellation: %v", recoveryContext.Err())
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		deployments: func(recoveryContext context.Context) error {
			if recoveryContext.Err() != nil {
				t.Fatalf("deployment recovery inherited cancellation: %v", recoveryContext.Err())
			}
			calls = append(calls, "deployments_recover")
			return nil
		},
		jobs: func() error {
			calls = append(calls, "jobs_recover")
			return nil
		},
	}, composition.executor, func(workerContext context.Context, _ jobs.Executor) error {
		if workerContext != ctx {
			t.Fatal("worker did not receive original context")
		}
		calls = append(calls, "worker")
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
	wantRecoveryOrder := []string{
		"compose_temp_recover", "generated_build_temp_recover", "generated_environment_temp_recover",
		"artifact_repository_recover", "ingress_recover", "deployments_recover", "jobs_recover", "worker",
	}
	var recoveryOrder []string
	for _, call := range calls {
		for _, wanted := range wantRecoveryOrder {
			if call == wanted {
				recoveryOrder = append(recoveryOrder, call)
			}
		}
	}
	if !reflect.DeepEqual(recoveryOrder, wantRecoveryOrder) {
		t.Fatalf("recovery order = %v, want %v; all calls=%v", recoveryOrder, wantRecoveryOrder, calls)
	}
}

func TestGeneratedCompositionRecoversEachTemporaryNamespace(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	fixture.configuration.GeneratedRuntime = true
	composeTemporary, err := securetemp.New(fixture.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	buildTemporary, err := securetemp.NewGeneratedBuild(fixture.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	environmentTemporary, err := securetemp.NewGeneratedRuntime(fixture.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	var operations []string
	for _, temporary := range []*securetemp.Manager{composeTemporary, buildTemporary, environmentTemporary} {
		files, err := temporary.Create(uuid.NewString(), 1)
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, files.Directory)
	}

	if _, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
		recoverIngress:   func(context.Context, *generatedingress.Manager) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if _, err := os.Stat(operation); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary operation %q remains after composition: %v", operation, err)
		}
	}
}

func TestGeneratedCompositionRejectsInvalidDockerPathBeforeRecovery(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	fixture.configuration.GeneratedRuntime = true
	composeTemporary, err := securetemp.New(fixture.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	files, err := composeTemporary.Create(uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{}); err == nil {
		t.Fatal("generated composition accepted an unresolved Docker executable")
	}
	if _, err := os.Stat(files.Directory); err != nil {
		t.Fatalf("invalid Docker path caused recovery before validation: %v", err)
	}
}

func TestGeneratedCompositionFailureStopsAtExactStep(t *testing.T) {
	for _, failAt := range []string{
		"compose_temp_create", "compose_temp_recover", "generated_build_temp_create", "generated_build_temp_recover",
		"generated_environment_temp_create", "generated_environment_temp_recover",
		"artifact_repository_create", "artifact_repository_recover", "controller_directories_create", "builder_create", "compiler_create",
		"environment_stager_create", "ingress_create", "ingress_recover", "runtime_engine_create",
		"migration_runner_create", "runtime_state_create", "authorization_gate_create", "generated_executor_create",
		"compose_executor_create", "runtime_router_create",
	} {
		t.Run(failAt, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.ComposeRuntime = true
			fixture.configuration.GeneratedRuntime = true
			failure := errors.New("injected startup failure")
			var calls []string
			result, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				beforeStep: func(name string) error {
					calls = append(calls, name)
					if name == failAt {
						return failure
					}
					return nil
				},
				recoverIngress: func(context.Context, *generatedingress.Manager) error { return nil },
			})
			if !errors.Is(err, failure) || result.executor != nil || calls[len(calls)-1] != failAt {
				t.Fatalf("result=%#v err=%v calls=%v", result, err, calls)
			}
		})
	}
}

func TestGeneratedRuntimeImpliesManagedIngressAndServerCapability(t *testing.T) {
	for _, test := range []struct {
		configuration config.Config
		want          runtimeCapabilities
	}{
		{configuration: config.Config{}, want: runtimeCapabilities{}},
		{configuration: config.Config{CaddyManagement: true}, want: runtimeCapabilities{caddy: true}},
		{configuration: config.Config{GeneratedRuntime: true}, want: runtimeCapabilities{caddy: true, generated: true}},
		{configuration: config.Config{FakeRuntime: true, ComposeRuntime: false}, want: runtimeCapabilities{fake: true}},
		{configuration: config.Config{ComposeRuntime: true, GeneratedRuntime: true}, want: runtimeCapabilities{caddy: true, compose: true, generated: true}},
	} {
		got := runtimeCapabilitiesFor(test.configuration)
		if got != test.want {
			t.Fatalf("capabilities for %#v = %#v, want %#v", test.configuration, got, test.want)
		}
		server := &controller.Server{}
		applyRuntimeCapabilities(server, got)
		if server.Caddy != test.want.caddy || server.FakeRuntime != test.want.fake || server.ComposeRuntime != test.want.compose || server.GeneratedRuntime != test.want.generated {
			t.Fatalf("server capabilities = caddy=%t fake=%t compose=%t generated=%t", server.Caddy, server.FakeRuntime, server.ComposeRuntime, server.GeneratedRuntime)
		}
	}
}

type runtimeCompositionFixture struct {
	db               *sql.DB
	configuration    config.Config
	dependencies     runtimeCompositionDependencies
	dockerExecutable string
}

func newRuntimeCompositionFixture(t *testing.T) runtimeCompositionFixture {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	applicationConfiguration, err := appconfig.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), nil, sourceconnections.NewFileCredentialStore(root), "", time.Now)
	snapshots, err := releasesnapshot.New(db, sources, root)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	dockerExecutable := filepath.Join(root, "docker-test")
	if err := os.WriteFile(dockerExecutable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config.Defaults()
	configuration.DataRoot = root
	return runtimeCompositionFixture{
		db: db, configuration: configuration, dockerExecutable: dockerExecutable,
		dependencies: runtimeCompositionDependencies{
			db: db, applications: apps.New(db), snapshots: snapshots, configuration: applicationConfiguration,
			deployments: deployments.New(db), plans: plans,
		},
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
