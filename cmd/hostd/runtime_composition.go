package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/composeruntime"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedexecutor"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedmigration"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/runtimeexecutor"
)

type runtimeCompositionDependencies struct {
	db                      *sql.DB
	applications            *apps.Store
	snapshots               *releasesnapshot.Materializer
	configuration           *appconfig.Store
	deployments             *deployments.Repository
	plans                   *deploymentplans.Store
	bindings                *deploymentbinding.Repository
	effectsWorkingDirectory string
	generated               *generatedRuntimeDomainDependencies
}

type runtimeCompositionOptions struct {
	dockerExecutable       string
	runner                 runtimeprocess.CommandRunner
	routePauseLogger       *slog.Logger
	beforeStep             func(string) error
	migrationRunnerFactory func(generatedruntime.MigrationRunner) generatedruntime.MigrationRunner
}

// generatedRuntimeDomainDependencies are assembled before the outer startup
// effects lease is released. The domain is installed only after the private
// constructor returns and the reacquired-lease completed join returns the
// same wrapper. Runtime composition consumes this exact owner graph and
// cannot create a Manager, budget, candidate authority, or route authority.
type generatedRuntimeDomainDependencies struct {
	domain      *generatedingress.GeneratedDomain
	deployments *deployments.Repository
	environment *generatedruntime.SecureEnvironmentStager
	directories docker.ControllerDirectories
	engine      generatedruntime.EngineOptions
	options     generatedingress.GeneratedDomainOptions
	stores      *deployments.GeneratedOwnerStores
}

type runtimeComposition struct {
	executor  jobs.Executor
	compose   runtimeexecutor.BoundExecutor
	generated runtimeexecutor.BoundExecutor
	ingress   *generatedingress.Manager
	state     *generatedruntimestate.Repository
}

type runtimeCapabilities struct {
	caddy     bool
	fake      bool
	compose   bool
	generated bool
}

func resolveRuntimeDockerExecutable(configuration config.Config, resolve func() (string, error)) (string, error) {
	if !configuration.GeneratedRuntime {
		return "", nil
	}
	if resolve == nil {
		return "", errors.New("Docker executable resolver is required")
	}
	path, err := resolve()
	if err != nil {
		return "", fmt.Errorf("resolve Docker executable: %w", err)
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("resolved Docker executable is invalid")
	}
	return path, nil
}

func runtimeCapabilitiesFor(configuration config.Config) runtimeCapabilities {
	return runtimeCapabilities{
		caddy: configuration.CaddyManagement || configuration.GeneratedRuntime,
		fake:  configuration.FakeRuntime, compose: configuration.ComposeRuntime, generated: configuration.GeneratedRuntime,
	}
}

func applyRuntimeCapabilities(server *controller.Server, capabilities runtimeCapabilities) {
	if server == nil {
		return
	}
	server.Caddy = capabilities.caddy
	server.FakeRuntime = capabilities.fake
	server.ComposeRuntime = capabilities.compose
	server.GeneratedRuntime = capabilities.generated
}

func prepareGeneratedRuntimeDomainDependencies(ctx context.Context, configuration config.Config,
	dependencies runtimeCompositionDependencies, options runtimeCompositionOptions,
) (*generatedRuntimeDomainDependencies, error) {
	if ctx == nil || !configuration.GeneratedRuntime || dependencies.db == nil ||
		dependencies.configuration == nil || dependencies.plans == nil || dependencies.bindings == nil ||
		!dependencies.bindings.UsesDatabase(dependencies.db) ||
		!dependencies.plans.ConfiguredFor(dependencies.db) ||
		!dependencies.configuration.ConfiguredFor(dependencies.db) ||
		dependencies.effectsWorkingDirectory == "" ||
		!filepath.IsAbs(dependencies.effectsWorkingDirectory) ||
		filepath.Clean(dependencies.effectsWorkingDirectory) != dependencies.effectsWorkingDirectory {
		return nil, errors.New("generated domain dependencies are required")
	}
	if options.dockerExecutable == "" || !filepath.IsAbs(options.dockerExecutable) ||
		filepath.Clean(options.dockerExecutable) != options.dockerExecutable {
		return nil, errors.New("generated runtime Docker executable is invalid")
	}
	if options.runner == nil {
		options.runner = &runtimeprocess.ExecRunner{}
	}
	step := func(name string) error {
		if options.beforeStep == nil {
			return nil
		}
		if err := options.beforeStep(name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	positiveFence, err := applifecycle.NewPositiveFenceValidator(applifecycle.New(dependencies.db))
	if err != nil {
		return nil, fmt.Errorf("runtime deployment positive fence setup: %w", err)
	}
	executionGuard, err := deploymentexecution.NewGuard(dependencies.db, dependencies.bindings,
		positiveFence, dependencies.effectsWorkingDirectory)
	if err != nil {
		return nil, fmt.Errorf("runtime deployment execution guard setup: %w", err)
	}
	if err := step("generated_environment_temp_create"); err != nil {
		return nil, err
	}
	environmentTemporary, err := securetemp.NewGeneratedRuntime(configuration.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("generated runtime environment storage setup: %w", err)
	}
	if err := step("generated_environment_temp_recover"); err != nil {
		return nil, err
	}
	if err := environmentTemporary.Recover(); err != nil {
		return nil, fmt.Errorf("clean generated runtime environment files: %w", err)
	}
	if err := step("controller_directories_create"); err != nil {
		return nil, err
	}
	directories, err := docker.PrepareControllerDirectories(configuration.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("generated controller directory setup: %w", err)
	}
	if err := step("environment_stager_create"); err != nil {
		return nil, err
	}
	environment, err := generatedruntime.NewSecureEnvironmentStager(environmentTemporary)
	if err != nil {
		return nil, fmt.Errorf("generated runtime environment setup: %w", err)
	}
	if err := step("migration_authority_create"); err != nil {
		return nil, err
	}
	runtimeDeployments, err := deployments.NewWithExecutionGuardAndGeneratedMigration(
		dependencies.db, executionGuard, dependencies.plans, dependencies.configuration,
		environment, options.runner, deployments.GeneratedMigrationProcessOptions{
			DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
			DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
			Timeout: 10 * time.Minute, OutputLimit: 16 << 10,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("generated migration authority setup: %w", err)
	}
	stores, err := runtimeDeployments.GeneratedOwnerStores()
	if err != nil {
		return nil, fmt.Errorf("generated domain store setup: %w", err)
	}
	engineOptions := generatedruntime.EngineOptions{
		DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		CommandTimeout: 30 * time.Second, HealthTimeout: 2 * time.Minute,
		HealthPollInterval: time.Second, OutputLimit: 64 << 10,
		Limits: generatedruntime.ContainerLimits{
			MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256,
			TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3,
		},
		ReplacementDiskBytes: 256 << 20,
	}
	domainOptions := generatedingress.GeneratedDomainOptions{
		DockerExecutable: engineOptions.DockerExecutable, DockerEndpoint: engineOptions.DockerEndpoint,
		DockerConfigDirectory: engineOptions.DockerConfigDirectory, WorkingDirectory: engineOptions.WorkingDirectory,
		DataRoot: configuration.DataRoot, CommandTimeout: engineOptions.CommandTimeout, PullTimeout: 5 * time.Minute,
		HealthTimeout: engineOptions.HealthTimeout, HealthPollInterval: engineOptions.HealthPollInterval,
		OutputLimit: engineOptions.OutputLimit, MemoryBytes: engineOptions.Limits.MemoryBytes,
		MilliCPUs: engineOptions.Limits.MilliCPUs, PIDs: engineOptions.Limits.PIDs,
		TmpfsBytes: engineOptions.Limits.TmpfsBytes, LogSize: engineOptions.Limits.LogSize,
		LogFiles: engineOptions.Limits.LogFiles, ReplacementDiskBytes: engineOptions.ReplacementDiskBytes,
	}
	return &generatedRuntimeDomainDependencies{
		deployments: runtimeDeployments, environment: environment, directories: directories,
		engine: engineOptions, options: domainOptions, stores: stores,
	}, nil
}

func prepareRuntimeComposition(ctx context.Context, configuration config.Config, dependencies runtimeCompositionDependencies, options runtimeCompositionOptions) (runtimeComposition, error) {
	if ctx == nil || dependencies.db == nil || dependencies.applications == nil || dependencies.snapshots == nil || dependencies.configuration == nil || dependencies.deployments == nil || dependencies.plans == nil {
		return runtimeComposition{}, errors.New("runtime composition dependencies are required")
	}
	if configuration.FakeRuntime && (configuration.ComposeRuntime || configuration.GeneratedRuntime) {
		return runtimeComposition{}, errors.New("fake runtime is mutually exclusive with real runtimes")
	}
	if (configuration.ComposeRuntime || configuration.GeneratedRuntime) &&
		(dependencies.bindings == nil || !dependencies.bindings.UsesDatabase(dependencies.db)) {
		return runtimeComposition{}, errors.New("runtime deployment lifecycle binding repository is required")
	}
	if configuration.GeneratedRuntime &&
		(!dependencies.plans.ConfiguredFor(dependencies.db) || !dependencies.configuration.ConfiguredFor(dependencies.db)) {
		return runtimeComposition{}, errors.New("generated runtime stores must use the composition database")
	}
	if configuration.GeneratedRuntime && (dependencies.generated == nil || dependencies.generated.domain == nil ||
		dependencies.generated.deployments == nil || dependencies.generated.environment == nil ||
		dependencies.generated.stores == nil || dependencies.generated.domain.Gateway() == nil ||
		dependencies.generated.domain.Candidates() == nil || dependencies.generated.domain.Routes() == nil) {
		return runtimeComposition{}, errors.New("complete generated domain is required")
	}
	var runtimeDeployments = dependencies.deployments
	if configuration.ComposeRuntime && !configuration.GeneratedRuntime {
		if !dependencies.deployments.UsesDatabase(dependencies.db) || dependencies.effectsWorkingDirectory == "" ||
			!filepath.IsAbs(dependencies.effectsWorkingDirectory) || filepath.Clean(dependencies.effectsWorkingDirectory) != dependencies.effectsWorkingDirectory {
			return runtimeComposition{}, errors.New("runtime deployment execution dependencies are required")
		}
		positiveFence, err := applifecycle.NewPositiveFenceValidator(applifecycle.New(dependencies.db))
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("runtime deployment positive fence setup: %w", err)
		}
		executionGuard, err := deploymentexecution.NewGuard(dependencies.db, dependencies.bindings, positiveFence, dependencies.effectsWorkingDirectory)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("runtime deployment execution guard setup: %w", err)
		}
		runtimeDeployments, err = deployments.NewWithExecutionGuard(dependencies.db, executionGuard)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("runtime deployment repository setup: %w", err)
		}
	}
	if configuration.GeneratedRuntime {
		runtimeDeployments = dependencies.generated.deployments
	}
	if configuration.GeneratedRuntime && (options.dockerExecutable == "" || !filepath.IsAbs(options.dockerExecutable) || filepath.Clean(options.dockerExecutable) != options.dockerExecutable) {
		return runtimeComposition{}, errors.New("generated runtime Docker executable is invalid")
	}
	if options.runner == nil {
		options.runner = &runtimeprocess.ExecRunner{}
	}
	recoveryContext := context.WithoutCancel(ctx)
	fenceCheck := rebindFenceCheck(dependencies.db)
	if err := fenceCheck(recoveryContext); err != nil {
		return runtimeComposition{}, fmt.Errorf("inspect LAN gateway rebind fence before runtime recovery: %w", err)
	}
	step := func(name string) error {
		if options.beforeStep == nil {
			return nil
		}
		if err := options.beforeStep(name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	if err := step("compose_temp_create"); err != nil {
		return runtimeComposition{}, err
	}
	composeTemporary, err := securetemp.New(configuration.DataRoot)
	if err != nil {
		return runtimeComposition{}, fmt.Errorf("compose runtime temporary storage setup: %w", err)
	}
	if err := step("compose_temp_recover"); err != nil {
		return runtimeComposition{}, err
	}
	if err := composeTemporary.Recover(); err != nil {
		return runtimeComposition{}, fmt.Errorf("clean compose runtime temporary files: %w", err)
	}

	result := runtimeComposition{}
	if configuration.GeneratedRuntime {
		if err := step("generated_build_temp_create"); err != nil {
			return runtimeComposition{}, err
		}
		buildTemporary, err := securetemp.NewGeneratedBuild(configuration.DataRoot)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated build temporary storage setup: %w", err)
		}
		if err := step("generated_build_temp_recover"); err != nil {
			return runtimeComposition{}, err
		}
		if err := buildTemporary.Recover(); err != nil {
			return runtimeComposition{}, fmt.Errorf("clean generated build temporary files: %w", err)
		}

		if err := step("artifact_repository_create"); err != nil {
			return runtimeComposition{}, err
		}
		artifacts := generatedimage.NewArtifactRepository(dependencies.db)
		if err := step("artifact_repository_recover"); err != nil {
			return runtimeComposition{}, err
		}
		if _, err := artifacts.Recover(recoveryContext); err != nil {
			return runtimeComposition{}, fmt.Errorf("recover generated image artifacts: %w", err)
		}

		environment := dependencies.generated.environment
		if err := step("builder_create"); err != nil {
			return runtimeComposition{}, err
		}
		builder, err := generatedimage.NewBuilderManager(runtimeDeployments, options.runner, generatedimage.BuilderManagerOptions{
			DataRoot: configuration.DataRoot, DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
		})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated builder setup: %w", err)
		}
		result.ingress = dependencies.generated.domain.Gateway()

		if err := step("runtime_engine_create"); err != nil {
			return runtimeComposition{}, err
		}
		engine, err := generatedruntime.NewEngine(options.runner, environment,
			dependencies.generated.domain.Candidates(), dependencies.generated.engine)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated runtime engine setup: %w", err)
		}
		if err := step("compiler_create"); err != nil {
			return runtimeComposition{}, err
		}
		compiler, err := generatedimage.NewCompiler(dependencies.snapshots, dependencies.plans, dependencies.configuration, runtimeDeployments, buildTemporary, builder, options.runner, engine, generatedimage.CompilerOptions{})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated compiler setup: %w", err)
		}
		if err := step("migration_runner_create"); err != nil {
			return runtimeComposition{}, err
		}
		migration, err := generatedmigration.New(runtimeDeployments)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated migration runner setup: %w", err)
		}
		var migrationRunner generatedruntime.MigrationRunner = migration
		if options.migrationRunnerFactory != nil {
			migrationRunner = options.migrationRunnerFactory(migrationRunner)
			if migrationRunner == nil {
				return runtimeComposition{}, errors.New("generated migration runner factory returned nil")
			}
		}
		if err := step("runtime_state_create"); err != nil {
			return runtimeComposition{}, err
		}
		state := generatedruntimestate.New(dependencies.db)
		result.state = state
		if err := step("authorization_gate_create"); err != nil {
			return runtimeComposition{}, err
		}
		authorization, err := generatedexecutor.NewAuthorizationGate(runtimeDeployments, dependencies.plans, engine)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated authorization setup: %w", err)
		}
		if err := step("generated_executor_create"); err != nil {
			return runtimeComposition{}, err
		}
		result.generated, err = generatedexecutor.NewExecutor(
			dependencies.applications, dependencies.snapshots, dependencies.configuration, runtimeDeployments,
			dependencies.plans, compiler, artifacts, state, engine, authorization,
			dependencies.generated.domain.Routes(), migrationRunner,
			generatedexecutor.Options{RoutePauseLogger: options.routePauseLogger},
		)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated executor setup: %w", err)
		}
	}

	if configuration.ComposeRuntime {
		if err := step("compose_executor_create"); err != nil {
			return runtimeComposition{}, err
		}
		composeDockerExecutable := ""
		if configuration.GeneratedRuntime {
			composeDockerExecutable = options.dockerExecutable
		}
		result.compose, err = composeruntime.NewExecutor(
			dependencies.applications, dependencies.snapshots, dependencies.configuration, runtimeDeployments,
			composeTemporary, options.runner, composeruntime.ExecutorOptions{
				DockerExecutable: composeDockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
				ConfigTimeout: configuration.ComposeConfigTimeout, ApplyTimeout: configuration.ComposeApplyTimeout, WaitTimeout: configuration.ComposeWaitTimeout,
			},
		)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("compose runtime setup: %w", err)
		}
	}

	switch {
	case configuration.FakeRuntime:
		result.executor = jobs.NewFakeExecutor()
	case configuration.ComposeRuntime || configuration.GeneratedRuntime:
		if err := step("runtime_router_create"); err != nil {
			return runtimeComposition{}, err
		}
		result.executor, err = runtimeexecutor.New(runtimeDeployments, dependencies.plans, dependencies.snapshots,
			result.compose, result.generated, dependencies.bindings, fenceCheck)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("runtime strategy router setup: %w", err)
		}
	}
	return result, nil
}
