package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/composeruntime"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
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
	db            *sql.DB
	applications  *apps.Store
	snapshots     *releasesnapshot.Materializer
	configuration *appconfig.Store
	deployments   *deployments.Repository
	plans         *deploymentplans.Store
}

type runtimeCompositionOptions struct {
	dockerExecutable string
	runner           runtimeprocess.CommandRunner
	beforeStep       func(string) error
	recoverIngress   func(context.Context, *generatedingress.Manager) error
}

type runtimeComposition struct {
	executor  jobs.Executor
	compose   jobs.Executor
	generated jobs.Executor
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

func prepareRuntimeComposition(ctx context.Context, configuration config.Config, dependencies runtimeCompositionDependencies, options runtimeCompositionOptions) (runtimeComposition, error) {
	if ctx == nil || dependencies.db == nil || dependencies.applications == nil || dependencies.snapshots == nil || dependencies.configuration == nil || dependencies.deployments == nil || dependencies.plans == nil {
		return runtimeComposition{}, errors.New("runtime composition dependencies are required")
	}
	if configuration.FakeRuntime && (configuration.ComposeRuntime || configuration.GeneratedRuntime) {
		return runtimeComposition{}, errors.New("fake runtime is mutually exclusive with real runtimes")
	}
	if configuration.GeneratedRuntime && (options.dockerExecutable == "" || !filepath.IsAbs(options.dockerExecutable) || filepath.Clean(options.dockerExecutable) != options.dockerExecutable) {
		return runtimeComposition{}, errors.New("generated runtime Docker executable is invalid")
	}
	if options.runner == nil {
		options.runner = runtimeprocess.ExecRunner{}
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
	recoveryContext := context.WithoutCancel(ctx)

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

		if err := step("generated_environment_temp_create"); err != nil {
			return runtimeComposition{}, err
		}
		environmentTemporary, err := securetemp.NewGeneratedRuntime(configuration.DataRoot)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated runtime environment storage setup: %w", err)
		}
		if err := step("generated_environment_temp_recover"); err != nil {
			return runtimeComposition{}, err
		}
		if err := environmentTemporary.Recover(); err != nil {
			return runtimeComposition{}, fmt.Errorf("clean generated runtime environment files: %w", err)
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

		if err := step("controller_directories_create"); err != nil {
			return runtimeComposition{}, err
		}
		directories, err := docker.PrepareControllerDirectories(configuration.DataRoot)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated controller directory setup: %w", err)
		}
		if err := step("builder_create"); err != nil {
			return runtimeComposition{}, err
		}
		builder, err := generatedimage.NewBuilderManager(options.runner, generatedimage.BuilderManagerOptions{
			DataRoot: configuration.DataRoot, DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
		})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated builder setup: %w", err)
		}
		if err := step("compiler_create"); err != nil {
			return runtimeComposition{}, err
		}
		compiler, err := generatedimage.NewCompiler(dependencies.snapshots, dependencies.plans, artifacts, buildTemporary, builder, options.runner, generatedimage.CompilerOptions{})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated compiler setup: %w", err)
		}
		if err := step("environment_stager_create"); err != nil {
			return runtimeComposition{}, err
		}
		environment, err := generatedruntime.NewSecureEnvironmentStager(environmentTemporary)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated runtime environment setup: %w", err)
		}
		if err := step("ingress_create"); err != nil {
			return runtimeComposition{}, err
		}
		ingress, err := generatedingress.New(options.runner, generatedingress.Options{
			DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
			DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
			DataRoot: configuration.DataRoot,
		})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated ingress setup: %w", err)
		}
		if err := step("ingress_recover"); err != nil {
			return runtimeComposition{}, err
		}
		if options.recoverIngress != nil {
			err = options.recoverIngress(recoveryContext, ingress)
		} else {
			err = ingress.Recover(recoveryContext)
		}
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("recover generated ingress: %w", err)
		}

		if err := step("runtime_engine_create"); err != nil {
			return runtimeComposition{}, err
		}
		engine, err := generatedruntime.NewEngine(options.runner, environment, ingress, generatedruntime.EngineOptions{
			DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
			DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated runtime engine setup: %w", err)
		}
		if err := step("migration_runner_create"); err != nil {
			return runtimeComposition{}, err
		}
		migration, err := generatedmigration.New(dependencies.configuration, environment, options.runner, generatedmigration.Options{
			DockerExecutable: options.dockerExecutable, DockerEndpoint: configuration.DockerEndpoint,
			DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		})
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated migration runner setup: %w", err)
		}
		if err := step("runtime_state_create"); err != nil {
			return runtimeComposition{}, err
		}
		state := generatedruntimestate.New(dependencies.db)
		if err := step("authorization_gate_create"); err != nil {
			return runtimeComposition{}, err
		}
		authorization, err := generatedexecutor.NewAuthorizationGate(dependencies.deployments, dependencies.plans, engine)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("generated authorization setup: %w", err)
		}
		if err := step("generated_executor_create"); err != nil {
			return runtimeComposition{}, err
		}
		result.generated, err = generatedexecutor.NewExecutor(
			dependencies.applications, dependencies.snapshots, dependencies.configuration, dependencies.deployments,
			dependencies.plans, compiler, artifacts, state, engine, authorization, ingress, migration, generatedexecutor.Options{},
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
			dependencies.applications, dependencies.snapshots, dependencies.configuration, dependencies.deployments,
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
		result.executor, err = runtimeexecutor.New(dependencies.deployments, dependencies.plans, dependencies.snapshots, result.compose, result.generated)
		if err != nil {
			return runtimeComposition{}, fmt.Errorf("runtime strategy router setup: %w", err)
		}
	}
	return result, nil
}
