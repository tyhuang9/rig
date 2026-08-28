package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/composeruntime"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/secretfile"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const bootstrapSecretFilename = "bootstrap-token.secret"

func runServer(args []string) int {
	cfg, err := startupConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err = cfg.EnsureDataRoot(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logger := newStructuredLogger(os.Stderr, cfg.LogLevel)
	db, err := database.Open(cfg.DataRoot)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		return 1
	}
	defer db.Close()
	a := auth.New(db)
	token, err := a.EnsureBootstrapToken()
	if err != nil {
		logger.Error("bootstrap token setup failed", "error", err)
		return 1
	}
	bootstrapCompleted, err := prepareBootstrapToken(os.Stdout, cfg.DataRoot, token, auth.BootstrapTokenLifetime, func(err error) {
		logger.Error("bootstrap token file cleanup failed", "error", err)
	})
	if err != nil {
		logger.Error("bootstrap token file setup failed", "error", err)
		return 1
	}
	defer bootstrapCompleted()
	m := machines.New(db)
	if _, err := m.EnsureLocal(); err != nil {
		logger.Error("local machine setup failed", "error", err)
		return 1
	}
	diagnostic := docker.Check(context.Background(), cfg.CaddyManagement, cfg.DockerEndpoint, cfg.DataRoot)
	if err := m.UpdateLocalDiagnostics(diagnostic.DockerVersion, diagnostic.ComposeVersion, diagnostic.Resources); err != nil {
		logger.Error("local diagnostics persistence failed", "error", err)
		return 1
	}
	j := jobs.New(db)
	applicationConfiguration, err := appconfig.New(db, cfg.DataRoot)
	if err != nil {
		logger.Error("application configuration setup failed", "error", err)
		return 1
	}
	if err := applicationConfiguration.Recover(context.Background()); err != nil {
		logger.Error("application configuration recovery failed", "error", err)
		return 1
	}
	var githubProvider sourceconnections.Provider
	if cfg.GitHubConnectionsEnabled() {
		githubProvider, err = githubapp.New(cfg.GitHubClientID)
		if err != nil {
			logger.Error("GitHub App configuration failed", "error", err)
			return 1
		}
	}
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), githubProvider, sourceconnections.NewFileCredentialStore(cfg.DataRoot), cfg.GitHubAppSlug, time.Now)
	snapshots, err := releasesnapshot.New(db, sources, cfg.DataRoot, releasesnapshot.RetentionOptions{
		PerAppBytes: cfg.ReleaseWorkspacePerAppBytes,
		GlobalBytes: cfg.ReleaseWorkspaceGlobalBytes,
	})
	if err != nil {
		logger.Error("release snapshot configuration failed", "error", err)
		return 1
	}
	if err := snapshots.Recover(); err != nil {
		logger.Error("release snapshot recovery failed", "error", err)
		return 1
	}
	applications := apps.New(db)
	deploymentRepository := deployments.New(db)
	temporary, err := securetemp.New(cfg.DataRoot)
	if err != nil {
		logger.Error("compose runtime temporary storage setup failed", "error", err)
		return 1
	}
	var executor jobs.Executor
	if cfg.FakeRuntime {
		executor = jobs.NewFakeExecutor()
	}
	if cfg.ComposeRuntime {
		executor, err = composeruntime.NewExecutor(applications, snapshots, applicationConfiguration, deploymentRepository, temporary, runtimeprocess.ExecRunner{}, composeruntime.ExecutorOptions{
			DockerEndpoint: cfg.DockerEndpoint,
			ConfigTimeout:  cfg.ComposeConfigTimeout,
			ApplyTimeout:   cfg.ComposeApplyTimeout,
			WaitTimeout:    cfg.ComposeWaitTimeout,
		})
		if err != nil {
			logger.Error("compose runtime setup failed", "error", err)
			return 1
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		temporary:   temporary.Recover,
		deployments: deploymentRepository.Recover,
		jobs:        j.RecoverInterrupted,
	}, executor, j.RunWorker, func(err error) {
		logger.Error("job worker stopped", "error", err)
		stop()
	})
	if err != nil {
		logger.Error("runtime recovery failed", "error", err)
		return 1
	}
	autoDeployRepository := autodeploy.NewRepository(db)
	autoDeployDone, autoDeployWake, autoDeployReconcile := startAutoDeploy(ctx, cfg, logger, func() (autoDeployRunner, error) {
		return newAutoDeployRunner(cfg, autoDeployRepository, sources, j, logger)
	})
	relayManagement := newControllerRelayManagementTarget()
	relayDone := startControllerRelay(ctx, cfg, logger, relayManagement, func() (controllerRelayRunner, error) {
		return newControllerRelayRuntime(cfg, db, sources, logger, autoDeployWake, autoDeployReconcile)
	})
	effectiveAutoDeploy := cfg.ComposeRuntime && cfg.GitHubConnectionsEnabled() && sources.ProviderEnabled()
	s := &http.Server{Addr: cfg.ListenAddress, Handler: (&controller.Server{Auth: a, Apps: applications, Jobs: j, Machines: m, Sources: sources, Configuration: applicationConfiguration, Deployments: deploymentRepository, RelayManagement: relayManagement, AutoDeploy: autoDeployRepository, AutoDeployAvailable: effectiveAutoDeploy, RelayReconcile: relayManagement.Reconcile, AutoDeployReconcile: autoDeployReconcile, Caddy: cfg.CaddyManagement, FakeRuntime: cfg.FakeRuntime, ComposeRuntime: cfg.ComposeRuntime, DockerEndpoint: cfg.DockerEndpoint, DataRoot: cfg.DataRoot, Logger: logger, BootstrapCompleted: bootstrapCompleted}).Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdown)
	}()
	logger.Info("hostd listening", "address", cfg.ListenAddress, "fake_runtime", cfg.FakeRuntime, "compose_runtime", cfg.ComposeRuntime)
	serveErr := s.ListenAndServe()
	if serveErr != nil && serveErr != http.ErrServerClosed {
		logger.Error("server stopped", "error", serveErr)
	}
	stop()
	if !waitForWorker(workerDone, 10*time.Second) {
		logger.Warn("job worker did not stop before shutdown timeout")
	}
	_ = waitForControllerRelay(relayDone, controllerRelayShutdownTimeout, logger)
	_ = waitForAutoDeploy(autoDeployDone, autoDeployShutdownTimeout, logger)
	if serveErr != nil && serveErr != http.ErrServerClosed {
		return 1
	}
	return 0
}

type runtimeRecovery struct {
	temporary   func() error
	deployments func(context.Context) error
	jobs        func() error
}

func prepareRuntimeWorker(ctx context.Context, recovery runtimeRecovery, executor jobs.Executor, run func(context.Context, jobs.Executor) error, reportFailure func(error)) (<-chan struct{}, error) {
	if recovery.temporary == nil || recovery.deployments == nil || recovery.jobs == nil {
		return nil, errors.New("runtime recovery dependencies are required")
	}
	if err := recovery.temporary(); err != nil {
		return nil, fmt.Errorf("clean compose runtime temporary files: %w", err)
	}
	if err := recovery.deployments(context.WithoutCancel(ctx)); err != nil {
		return nil, fmt.Errorf("recover deployments: %w", err)
	}
	if err := recovery.jobs(); err != nil {
		return nil, fmt.Errorf("recover jobs: %w", err)
	}
	done := make(chan struct{})
	if executor == nil {
		close(done)
		return done, nil
	}
	if run == nil {
		return nil, errors.New("runtime worker is required")
	}
	go func() {
		defer close(done)
		if err := run(ctx, executor); err != nil && reportFailure != nil {
			reportFailure(err)
		}
	}()
	return done, nil
}

func waitForWorker(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func startupConfig(args []string) (config.Config, error) {
	return config.FromFlags(args)
}

func newStructuredLogger(w io.Writer, logLevel string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(logLevel)}))
}

func prepareBootstrapToken(output io.Writer, dataRoot, token string, lifetime time.Duration, reportCleanupError func(error)) (func(), error) {
	path := filepath.Join(dataRoot, bootstrapSecretFilename)
	if token == "" {
		if err := secretfile.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale bootstrap token: %w", err)
		}
		return func() {}, nil
	}
	plaintext := []byte(token)
	defer clear(plaintext)
	if err := secretfile.Write(path, auth.BootstrapSecretPurpose, plaintext); err != nil {
		return nil, err
	}
	if err := auth.WriteBootstrapTokenPath(output, path); err != nil {
		if cleanupErr := secretfile.Remove(path); cleanupErr != nil && reportCleanupError != nil {
			reportCleanupError(fmt.Errorf("remove bootstrap token file after output failure: %w", cleanupErr))
		}
		return nil, err
	}
	var once sync.Once
	remove := func() {
		once.Do(func() {
			if err := secretfile.Remove(path); err != nil && reportCleanupError != nil {
				reportCleanupError(fmt.Errorf("remove bootstrap token file: %w", err))
			}
		})
	}
	timer := time.AfterFunc(lifetime, remove)
	return func() {
		timer.Stop()
		remove()
	}, nil
}

func parseLevel(v string) slog.Level {
	switch v {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
