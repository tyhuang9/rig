package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/docker"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/runtimeexecutor"
	"github.com/hostd/hostd/internal/sourceconnections"
	generateddomaintest "github.com/hostd/hostd/internal/testsupport/generateddomain"
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
			result, err := prepareRuntimeCompositionForTest(t, context.Background(), configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
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
		})
	}
}

func prepareCompleteGeneratedRuntimeDomainForTest(t *testing.T, ctx context.Context, configuration config.Config, dependencies runtimeCompositionDependencies, options runtimeCompositionOptions) (*generatedRuntimeDomainDependencies, error) {
	t.Helper()
	if !configuration.GeneratedRuntime {
		return nil, errors.New("generated runtime fixture requires generated runtime")
	}
	if options.runner == nil {
		options.runner = generateddomaintest.NewRunner(generateddomaintest.Options{})
	}
	options.beforeStep = nil
	generated, err := prepareGeneratedRuntimeDomainDependencies(ctx, configuration, dependencies, options)
	if err != nil {
		return nil, fmt.Errorf("prepare generated domain dependencies: %w", err)
	}
	domain, err := generatedingress.NewGeneratedDomain(ctx, generated.stores, options.runner, generated.environment, generated.options)
	if err != nil {
		calls := -1
		if runner, ok := options.runner.(*generateddomaintest.Runner); ok {
			calls = runner.Calls()
		}
		return nil, fmt.Errorf("construct generated domain after %d Docker calls: %w", calls, err)
	}
	joined, err := generatedingress.NewGeneratedDomain(ctx, generated.stores, options.runner, generated.environment, generated.options)
	if err != nil {
		return nil, fmt.Errorf("join generated domain: %w", err)
	}
	if joined != domain {
		return nil, errors.New("complete generated domain join returned a different owner")
	}
	generated.domain = domain
	return generated, nil
}

func prepareRuntimeCompositionForTest(t *testing.T, ctx context.Context, configuration config.Config, dependencies runtimeCompositionDependencies, options runtimeCompositionOptions) (runtimeComposition, error) {
	t.Helper()
	if configuration.GeneratedRuntime {
		if options.runner == nil {
			options.runner = generateddomaintest.NewRunner(generateddomaintest.Options{})
		}
		generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, ctx, configuration, dependencies, options)
		if err != nil {
			return runtimeComposition{}, err
		}
		dependencies.generated = generated
	}
	return prepareRuntimeComposition(ctx, configuration, dependencies, options)
}

func TestHostdDaemonAssemblyRunnerComposesGeneratedRuntime(t *testing.T) {
	compose := func(t *testing.T, assembly hostdDaemonAssemblyOptions) runtimeComposition {
		t.Helper()
		fixture := newRuntimeCompositionFixture(t)
		fixture.configuration.GeneratedRuntime = true
		assembly = normalizeHostdDaemonAssembly(assembly)
		composition, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
			dockerExecutable: fixture.dockerExecutable,
			runner:           assembly.runner,
		})
		if err != nil {
			t.Fatalf("compose generated runtime: %v", err)
		}
		if composition.generated == nil {
			t.Fatal("generated runtime engine was not composed")
		}
		return composition
	}

	t.Run("default runner", func(t *testing.T) {
		if normalizeHostdDaemonAssembly(hostdDaemonAssemblyOptions{}).runner == nil {
			t.Fatal("default daemon runner was not installed")
		}
	})

	t.Run("injected runner", func(t *testing.T) {
		injected := generateddomaintest.NewRunner(generateddomaintest.Options{})
		assembly := normalizeHostdDaemonAssembly(hostdDaemonAssemblyOptions{runner: injected})
		if assembly.runner != injected {
			t.Fatalf("injected daemon runner identity changed: got %p want %p", assembly.runner, injected)
		}
		compose(t, assembly)
		if injected.Calls() == 0 {
			t.Fatal("complete generated domain did not use the injected daemon runner")
		}
	})
}

type runtimeCompositionReporter struct{ updates []jobs.ProgressUpdate }

func (reporter *runtimeCompositionReporter) Report(update jobs.ProgressUpdate) error {
	reporter.updates = append(reporter.updates, update)
	return nil
}

func TestRuntimeCompositionRequiresSameDatabaseMigrationStoresBeforeSetup(t *testing.T) {
	for _, dependency := range []string{"plans", "configuration"} {
		t.Run(dependency, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.GeneratedRuntime = true
			foreignRoot := filepath.Clean(t.TempDir())
			foreignDB, err := database.Open(foreignRoot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = foreignDB.Close() })
			dependencies := fixture.dependencies
			if dependency == "plans" {
				dependencies.plans, err = deploymentplans.New(foreignDB, foreignRoot)
			} else {
				dependencies.configuration, err = appconfig.New(foreignDB, foreignRoot)
			}
			if err != nil {
				t.Fatal(err)
			}
			steps := 0
			runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
			result, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				runner:           runner,
				beforeStep: func(string) error {
					steps++
					return nil
				},
			})
			if err == nil || result.executor != nil || result.generated != nil || steps != 0 || runner.Calls() != 0 {
				t.Fatalf("foreign %s accepted or crossed setup: err=%v steps=%d calls=%d", dependency, err, steps, runner.Calls())
			}
		})
	}
}

func TestRuntimeCompositionRequiresSameDatabaseDeploymentBindingsBeforeSetup(t *testing.T) {
	t.Run("same database", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		fixture.configuration.ComposeRuntime = true
		result, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := result.executor.(*runtimeexecutor.Router); !ok {
			t.Fatalf("executor=%T", result.executor)
		}
	})
	for _, mode := range []string{"compose", "generated"} {
		for _, repository := range []string{"missing", "unconfigured", "foreign"} {
			t.Run(mode+"/"+repository, func(t *testing.T) {
				fixture := newRuntimeCompositionFixture(t)
				fixture.configuration.ComposeRuntime = mode == "compose"
				fixture.configuration.GeneratedRuntime = mode == "generated"
				dependencies := fixture.dependencies
				switch repository {
				case "missing":
					dependencies.bindings = nil
				case "unconfigured":
					dependencies.bindings = &deploymentbinding.Repository{}
				case "foreign":
					foreignDB, err := database.Open(filepath.Join(t.TempDir(), "foreign"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = foreignDB.Close() })
					_, dependencies.bindings, err = newDeploymentJobService(foreignDB)
					if err != nil {
						t.Fatal(err)
					}
				}
				steps := 0
				runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
				result, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, dependencies, runtimeCompositionOptions{
					dockerExecutable: fixture.dockerExecutable,
					runner:           runner,
					beforeStep: func(string) error {
						steps++
						return nil
					},
				})
				if err == nil || result.executor != nil || steps != 0 || runner.Calls() != 0 {
					t.Fatalf("composition=%#v error=%v steps=%d runner=%d", result, err, steps, runner.Calls())
				}
			})
		}
	}
}

func TestRuntimeCompositionRequiresSameDatabaseDeploymentsAndEffectsDirectoryBeforeSetup(t *testing.T) {
	for _, dependency := range []string{"missing deployments", "unconfigured deployments", "foreign deployments", "missing effects directory"} {
		t.Run("compose/"+dependency, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.ComposeRuntime = true
			dependencies := fixture.dependencies
			switch dependency {
			case "missing deployments":
				dependencies.deployments = nil
			case "unconfigured deployments":
				dependencies.deployments = deployments.New(nil)
			case "foreign deployments":
				foreignDB, err := database.Open(filepath.Join(t.TempDir(), "foreign"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = foreignDB.Close() })
				dependencies.deployments = deployments.New(foreignDB)
			case "missing effects directory":
				dependencies.effectsWorkingDirectory = ""
			}
			steps := 0
			runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
			result, err := prepareRuntimeComposition(context.Background(), fixture.configuration, dependencies, runtimeCompositionOptions{
				runner: runner,
				beforeStep: func(string) error {
					steps++
					return nil
				},
			})
			if err == nil || result.executor != nil || steps != 0 || runner.Calls() != 0 {
				t.Fatalf("composition=%#v error=%v steps=%d runner=%d", result, err, steps, runner.Calls())
			}
		})
	}

	t.Run("generated/missing deployments rejects before complete-domain requirement", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		fixture.configuration.GeneratedRuntime = true
		fixture.dependencies.deployments = nil
		steps := 0
		result, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
			beforeStep: func(string) error {
				steps++
				return nil
			},
		})
		if err == nil || result.executor != nil || steps != 0 {
			t.Fatalf("composition=%#v error=%v steps=%d", result, err, steps)
		}
	})

	t.Run("generated/missing effects directory rejects during domain preparation", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		fixture.configuration.GeneratedRuntime = true
		fixture.dependencies.effectsWorkingDirectory = ""
		runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
		result, err := prepareGeneratedRuntimeDomainDependencies(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
			dockerExecutable: fixture.dockerExecutable,
			runner:           runner,
		})
		if err == nil || result != nil || runner.Calls() != 0 {
			t.Fatalf("preparation=%#v error=%v runner=%d", result, err, runner.Calls())
		}
	})

	for _, dependency := range []string{"unconfigured deployments", "foreign deployments"} {
		t.Run("generated/domain owns deployments/"+dependency, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.GeneratedRuntime = true
			if dependency == "unconfigured deployments" {
				fixture.dependencies.deployments = deployments.New(nil)
			} else {
				foreignDB, err := database.Open(filepath.Join(t.TempDir(), "foreign"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = foreignDB.Close() })
				fixture.dependencies.deployments = deployments.New(foreignDB)
			}
			runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
			result, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				runner:           runner,
			})
			if err != nil || result.generated == nil || runner.Calls() == 0 {
				t.Fatalf("composition=%#v error=%v runner=%d", result, err, runner.Calls())
			}
		})
	}
}

func TestRuntimeCompositionFakeAndDisabledDoNotRequireDeploymentBindings(t *testing.T) {
	for _, mode := range []string{"disabled", "fake"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.dependencies.bindings = nil
			fixture.configuration.FakeRuntime = mode == "fake"
			result, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, fake := result.executor.(*jobs.FakeExecutor)
			if fake != (mode == "fake") || (mode == "disabled" && result.executor != nil) {
				t.Fatalf("executor=%T", result.executor)
			}
		})
	}
}

func TestRuntimeCompositionRouterUsesSameDatabasePersistedBindings(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	fixture.configuration.ComposeRuntime = true
	runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
	composition, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unbound job refuses before preparation", func(t *testing.T) {
		jobID := uuid.NewString()
		reporter := &runtimeCompositionReporter{}
		_, err := composition.executor.Execute(context.Background(), jobs.Job{
			ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: uuid.NewString(), RequestedBy: uuid.NewString(), Attempt: 1,
			Input: json.RawMessage(`{"configurationMode":"current"}`),
		}, reporter)
		var executionErr *jobs.ExecutionError
		if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || !errors.Is(err, deploymentbinding.ErrBindingNotFound) {
			t.Fatalf("error=%v", err)
		}
		var deploymentsCount int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE job_id=?`, jobID).Scan(&deploymentsCount); err != nil {
			t.Fatal(err)
		}
		if deploymentsCount != 0 || len(reporter.updates) != 0 || runner.Calls() != 0 {
			t.Fatalf("deployments=%d updates=%d runner=%d", deploymentsCount, len(reporter.updates), runner.Calls())
		}
	})

	t.Run("accepted bound job reaches compose validation", func(t *testing.T) {
		actorID, appID := uuid.NewString(), uuid.NewString()
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := fixture.db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, actorID, "hash", stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'pending',?,?)`, appID, appID, appID, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		created, err := fixture.deploymentJobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
			ApplicationID: appID, IdempotencyKey: "composition-bound-job", RequestedBy: actorID,
			Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
		}, jobs.DeploymentCreateHooks{})
		if err != nil || !created.Created {
			t.Fatalf("created=%t error=%v", created.Created, err)
		}
		job := created.Job
		job.Attempt = 1
		reporter := &runtimeCompositionReporter{}
		_, err = composition.executor.Execute(context.Background(), job, reporter)
		if errors.Is(err, deploymentbinding.ErrBindingNotFound) || len(reporter.updates) == 0 || reporter.updates[0].Phase != "validate" {
			t.Fatalf("error=%v updates=%#v", err, reporter.updates)
		}
		var deploymentsCount int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE job_id=?`, job.ID).Scan(&deploymentsCount); err != nil {
			t.Fatal(err)
		}
		if deploymentsCount != 1 || runner.Calls() != 0 {
			t.Fatalf("deployments=%d runner=%d", deploymentsCount, runner.Calls())
		}
	})
}

func TestGeneratedCompositionRecoversBeforeDeploymentJobAndWorker(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	fixture.configuration.GeneratedRuntime = true
	runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
	generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
		runner:           runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.dependencies.generated = generated
	directories, err := docker.PrepareControllerDirectories(fixture.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	admit, err := deploymentEffectsAdmission(fixture.db, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	releaseStartupAdmission, err := admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = releaseStartupAdmission() })
	assertStartupAdmissionHeld := func(stage string) {
		t.Helper()
		probeContext, cancelProbe := context.WithTimeout(context.Background(), 100*time.Millisecond)
		probeRelease, probeErr := admit(probeContext)
		cancelProbe()
		if probeRelease != nil {
			_ = probeRelease()
			t.Fatalf("%s acquired a nested deployment effects lease", stage)
		}
		if !errors.Is(probeErr, context.DeadlineExceeded) {
			t.Fatalf("%s deployment effects lease probe error = %v", stage, probeErr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	composition, err := prepareRuntimeComposition(ctx, fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
		runner:           runner,
		beforeStep: func(name string) error {
			assertStartupAdmissionHeld("composition " + name)
			calls = append(calls, name)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		recoverState: func(recoveryContext context.Context) error {
			if recoveryContext != ctx {
				t.Fatal("runtime recovery did not receive original context")
			}
			assertStartupAdmissionHeld("runtime recovery")
			calls = append(calls, "runtime_recover")
			return nil
		},
		releaseAdmission: releaseStartupAdmission,
	}, composition.executor, func(workerContext context.Context, _ jobs.Executor) error {
		if workerContext != ctx {
			t.Fatal("worker did not receive original context")
		}
		workerAdmissionContext, cancelWorkerAdmission := context.WithTimeout(context.Background(), time.Second)
		workerRelease, workerAdmissionErr := admit(workerAdmissionContext)
		cancelWorkerAdmission()
		if workerAdmissionErr != nil {
			t.Errorf("worker could not acquire released startup admission: %v", workerAdmissionErr)
			return nil
		}
		if err := workerRelease(); err != nil {
			t.Errorf("worker admission release failed: %v", err)
			return nil
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
		"compose_temp_recover", "generated_build_temp_recover", "artifact_repository_recover",
		"runtime_recover", "worker",
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

func TestGeneratedCompositionUsesExactCompleteDomainOwner(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	fixture.configuration.GeneratedRuntime = true
	runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
	generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
		runner:           runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.domain == nil || generated.domain.Gateway() == nil || generated.domain.Candidates() == nil || generated.domain.Routes() == nil {
		t.Fatal("complete generated domain did not expose all sealed owners")
	}
	fixture.dependencies.generated = generated
	composition, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
		runner:           runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if composition.generated == nil || composition.ingress != generated.domain.Gateway() {
		t.Fatalf("composition did not retain exact generated domain gateway: ingress=%p owner=%p executor=%T", composition.ingress, generated.domain.Gateway(), composition.generated)
	}
}

func TestGeneratedCompositionMigrationRunnerFactory(t *testing.T) {
	t.Run("wrapped runner", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		fixture.configuration.GeneratedRuntime = true
		var received generatedruntime.MigrationRunner
		_, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
			dockerExecutable: fixture.dockerExecutable,
			migrationRunnerFactory: func(runner generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
				received = runner
				return runtimeCompositionMigrationRunner{delegate: runner}
			},
		})
		if err != nil || received == nil {
			t.Fatalf("wrapped migration runner received=%T err=%v", received, err)
		}
	})

	t.Run("nil runner rejected", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		fixture.configuration.GeneratedRuntime = true
		_, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
			dockerExecutable: fixture.dockerExecutable,
			migrationRunnerFactory: func(generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
				return nil
			},
		})
		if err == nil {
			t.Fatal("generated composition accepted a nil wrapped migration runner")
		}
	})
}

type runtimeCompositionMigrationRunner struct {
	delegate generatedruntime.MigrationRunner
}

func (runner runtimeCompositionMigrationRunner) Run(ctx context.Context, invocation deployments.GeneratedMigrationInvocation) error {
	return runner.delegate.Run(ctx, invocation)
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

	if _, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
		dockerExecutable: fixture.dockerExecutable,
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
	if _, err := prepareRuntimeCompositionForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{}); err == nil {
		t.Fatal("generated composition accepted an unresolved Docker executable")
	}
	if _, err := os.Stat(files.Directory); err != nil {
		t.Fatalf("invalid Docker path caused recovery before validation: %v", err)
	}
}

func TestRuntimeCompositionRebindFenceFailsBeforeRecovery(t *testing.T) {
	for _, mode := range []string{"compose", "generated"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.ComposeRuntime = mode == "compose"
			fixture.configuration.GeneratedRuntime = mode == "generated"
			runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
			if fixture.configuration.GeneratedRuntime {
				generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
					dockerExecutable: fixture.dockerExecutable,
					runner:           runner,
				})
				if err != nil {
					t.Fatal(err)
				}
				fixture.dependencies.generated = generated
			}
			if _, err := fixture.db.Exec(`DROP TABLE lan_gateway_rebind_claims`); err != nil {
				t.Fatal(err)
			}
			steps := 0
			_, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				runner:           runner,
				beforeStep: func(string) error {
					steps++
					return nil
				},
			})
			if err == nil || steps != 0 {
				t.Fatalf("composition error=%v steps=%d", err, steps)
			}
		})
	}
}

func TestGeneratedDomainPreparationFailureStopsAtExactStep(t *testing.T) {
	steps := []string{
		"generated_environment_temp_create", "generated_environment_temp_recover",
		"controller_directories_create", "environment_stager_create", "migration_authority_create",
	}
	for index, failAt := range steps {
		t.Run(failAt, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.GeneratedRuntime = true
			failure := errors.New("injected startup failure")
			var calls []string
			result, err := prepareGeneratedRuntimeDomainDependencies(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				runner:           generateddomaintest.NewRunner(generateddomaintest.Options{}),
				beforeStep: func(name string) error {
					calls = append(calls, name)
					if name == failAt {
						return failure
					}
					return nil
				},
			})
			if !errors.Is(err, failure) || result != nil || !reflect.DeepEqual(calls, steps[:index+1]) {
				t.Fatalf("result=%#v err=%v calls=%v", result, err, calls)
			}
		})
	}
}

func TestGeneratedCompositionFailureStopsAtExactStep(t *testing.T) {
	steps := []string{
		"compose_temp_create", "compose_temp_recover", "generated_build_temp_create", "generated_build_temp_recover",
		"artifact_repository_create", "artifact_repository_recover", "builder_create", "runtime_engine_create",
		"compiler_create", "migration_runner_create", "runtime_state_create", "authorization_gate_create",
		"generated_executor_create", "compose_executor_create", "runtime_router_create",
	}
	for index, failAt := range steps {
		t.Run(failAt, func(t *testing.T) {
			fixture := newRuntimeCompositionFixture(t)
			fixture.configuration.ComposeRuntime = true
			fixture.configuration.GeneratedRuntime = true
			runner := generateddomaintest.NewRunner(generateddomaintest.Options{})
			generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				runner:           runner,
			})
			if err != nil {
				t.Fatal(err)
			}
			fixture.dependencies.generated = generated
			failure := errors.New("injected startup failure")
			var calls []string
			result, err := prepareRuntimeComposition(context.Background(), fixture.configuration, fixture.dependencies, runtimeCompositionOptions{
				dockerExecutable: fixture.dockerExecutable,
				runner:           runner,
				beforeStep: func(name string) error {
					calls = append(calls, name)
					if name == failAt {
						return failure
					}
					return nil
				},
			})
			if !errors.Is(err, failure) || result.executor != nil || !reflect.DeepEqual(calls, steps[:index+1]) {
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
	deploymentJobs   *jobs.Service
	configuration    config.Config
	dependencies     runtimeCompositionDependencies
	dockerExecutable string
}

func newRuntimeCompositionFixture(t *testing.T) runtimeCompositionFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directories, err := docker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	db, err := database.OpenForGeneratedDomain(context.Background(), root, lease)
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
	deploymentJobs, bindings, err := newDeploymentJobService(db)
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
		db: db, deploymentJobs: deploymentJobs, configuration: configuration, dockerExecutable: dockerExecutable,
		dependencies: runtimeCompositionDependencies{
			db: db, applications: apps.New(db), snapshots: snapshots, configuration: applicationConfiguration,
			deployments: deployments.New(db), plans: plans, bindings: bindings,
			effectsWorkingDirectory: directories.WorkingDirectory,
		},
	}
}
