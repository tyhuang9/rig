package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/bootstraplocator"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/docker"
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
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		logger.Error("controller listener reservation failed", "error", err)
		return 1
	}
	defer listener.Close()
	ownerDirectories, err := docker.PrepareControllerDirectories(cfg.DataRoot)
	if err != nil {
		logger.Error("controller owner directory setup failed", "error", err)
		return 1
	}
	ownerLease, err := controllerowner.Acquire(context.Background(), ownerDirectories.WorkingDirectory)
	if err != nil {
		logger.Error("controller data root ownership failed", "error", err)
		return 1
	}
	defer func() {
		if err := ownerLease.Close(); err != nil {
			logger.Error("controller data root ownership release failed", "error", err)
		}
	}()
	dockerExecutable, err := resolveRuntimeDockerExecutable(cfg, docker.ResolveExecutable)
	if err != nil {
		logger.Error("Docker executable resolution failed", "error", err)
		return 1
	}
	emergencyStop := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := stopOwnedGatewayOnStartupFailure(stopCtx, cfg, dockerExecutable, ownerDirectories); stopErr != nil {
			logger.Error("owned gateway emergency stop could not be verified", "error", stopErr)
		} else {
			logger.Warn("owned gateway stopped after startup reconciliation failure")
		}
	}
	db, err := database.Open(cfg.DataRoot)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		emergencyStop()
		return 1
	}
	defer db.Close()
	rebindCheck := rebindFenceCheck(db)
	gate, err := inspectGatewayStartup(context.Background(), cfg, db, dockerExecutable, ownerDirectories)
	if err != nil {
		logger.Error("gateway startup inspection failed", "error", err)
		emergencyStop()
		return 1
	}
	if gate.recoveryBatch || gate.recoveryKind == controller.RecoveryLANGrant || gate.recoveryKind == controller.RecoveryLANDisable {
		gate, err = quarantineLANRecoveryStartup(context.Background(), db, gate)
		if err != nil {
			logger.Error("LAN access startup quarantine failed", "error", err)
			emergencyStop()
			return 1
		}
	}
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
	if token != "" {
		removeLocator, locatorErr := bootstraplocator.DefaultStore().Register(cfg.DataRoot, auth.BootstrapTokenLifetime)
		if locatorErr != nil {
			logger.Warn("bootstrap token command discovery unavailable; use the printed protected file path", "error", locatorErr)
		} else {
			removeToken := bootstrapCompleted
			bootstrapCompleted = func() {
				removeToken()
				if err := removeLocator(); err != nil {
					logger.Error("bootstrap token locator cleanup failed", "error", err)
				}
			}
		}
	}
	defer bootstrapCompleted()
	if err := rebindCheck(context.Background()); err != nil {
		logger.Error("LAN gateway rebind fence changed before controller startup", "error", err)
		emergencyStop()
		return 1
	}
	if gate.recoveryKind != "" {
		logger.Warn("controller entering gateway recovery mode", "kind", gate.recoveryKind, "operation_id", gate.recoveryID)
		return runRecoveryOnlyController(cfg, logger, listener, a, appaccess.New(db), gate, bootstrapCompleted)
	}
	m := machines.New(db)
	if _, err := m.EnsureLocal(); err != nil {
		logger.Error("local machine setup failed", "error", err)
		return 1
	}
	capabilities := runtimeCapabilitiesFor(cfg)
	checker := docker.NewChecker(cfg.DockerEndpoint, cfg.DataRoot)
	checker.DockerExecutable = dockerExecutable
	diagnostic := checker.Check(context.Background(), capabilities.caddy)
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
	planStore, err := deploymentplans.New(db, cfg.DataRoot)
	if err != nil {
		logger.Error("deployment plan storage setup failed", "error", err)
		return 1
	}
	if err := planStore.Recover(context.Background()); err != nil {
		logger.Error("deployment plan recovery failed", "error", err)
		return 1
	}
	deploymentRepository := deployments.New(db)
	runtime, err := prepareRuntimeComposition(context.Background(), cfg, runtimeCompositionDependencies{
		db: db, applications: applications, snapshots: snapshots, configuration: applicationConfiguration,
		deployments: deploymentRepository, plans: planStore,
	}, runtimeCompositionOptions{dockerExecutable: dockerExecutable, preinspectedIngress: gate.ingress})
	if err != nil {
		logger.Error("runtime composition failed", "error", err)
		return 1
	}
	if cfg.GeneratedRuntime {
		confirmedSnapshot, snapshotErr := appaccess.New(db).HostingGatewayStartupSnapshot(context.Background())
		if snapshotErr != nil || !reflect.DeepEqual(gate.snapshot, confirmedSnapshot) {
			logger.Error("hosting gateway startup claim snapshot changed", "error", snapshotErr)
			emergencyStop()
			return 1
		}
		confirmedInspection, inspectErr := runtime.ingress.InspectGatewayV2Startup(context.Background(), gatewayStartupClaims(confirmedSnapshot.Upgrades))
		if inspectErr != nil || confirmedInspection != gate.inspection {
			logger.Error("gateway startup inspection changed after recovery", "error", inspectErr)
			emergencyStop()
			return 1
		}
		if gate.inspection.Disposition == generatedingress.GatewayV2StartupNormalV2 {
			confirmedAccessInspection, accessErr := runtime.ingress.InspectGatewayV2LANAccessStartup(context.Background(),
				lanGrantStartupClaims(confirmedSnapshot.Grants), lanDisableStartupClaims(confirmedSnapshot.Disables))
			if accessErr != nil || !reflect.DeepEqual(confirmedAccessInspection, gate.accessInspection) {
				logger.Error("LAN access startup inspection changed after recovery", "error", accessErr)
				emergencyStop()
				return 1
			}
		}
	}
	if err := rebindCheck(context.Background()); err != nil {
		logger.Error("LAN gateway rebind fence changed before worker start", "error", err)
		emergencyStop()
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerRun := j.RunWorker
	var admission func(context.Context) (func() error, error)
	if cfg.ComposeRuntime || cfg.GeneratedRuntime {
		admission, err = deploymentEffectsAdmission(db, ownerDirectories.WorkingDirectory)
		if err != nil {
			logger.Error("deployment effects admission setup failed", "error", err)
			return 1
		}
		workerRun = func(runContext context.Context, executor jobs.Executor) error {
			return j.RunWorkerWithAdmission(runContext, executor, admission)
		}
	}
	workerDone, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		deployments: deploymentRepository.Recover,
		jobs:        j.RecoverInterrupted,
		admission:   admission,
	}, runtime.executor, workerRun, func(err error) {
		logger.Error("job worker stopped", "error", err)
		stop()
	})
	if err != nil {
		logger.Error("runtime recovery failed", "error", err)
		return 1
	}
	autoDeployRepository := autodeploy.NewRepository(db)
	var autoDeployPreflight autodeploy.DispatchPreflight
	if cfg.GeneratedRuntime {
		autoDeployPreflight, err = newGeneratedAutoDeployPreflight(planStore, sources, snapshots)
		if err != nil {
			logger.Error("generated auto-deploy preflight setup failed", "error", err)
			return 1
		}
	}
	autoDeployDone, autoDeployWake, autoDeployReconcile := startAutoDeploy(ctx, cfg, logger, func() (autoDeployRunner, error) {
		return newGeneratedAwareAutoDeployRunner(cfg, autoDeployRepository, sources, j, autoDeployPreflight, logger)
	})
	relayManagement := newControllerRelayManagementTarget()
	relayDone := startControllerRelay(ctx, cfg, logger, relayManagement, func() (controllerRelayRunner, error) {
		return newControllerRelayRuntime(cfg, db, sources, logger, autoDeployWake, autoDeployReconcile)
	})
	effectiveAutoDeploy := (cfg.ComposeRuntime || cfg.GeneratedRuntime) && cfg.GitHubConnectionsEnabled() && sources.ProviderEnabled()
	appAccessRepository := appaccess.New(db)
	controllerServer := &controller.Server{Auth: a, Apps: applications, Jobs: j, Machines: m, Sources: sources, Configuration: applicationConfiguration, Deployments: deploymentRepository, DeploymentPlans: planStore, GeneratedIngress: runtime.ingress, GeneratedRuntimeState: runtime.state, RelayManagement: relayManagement, AutoDeploy: autoDeployRepository, GatewayProfiles: appAccessRepository, GatewayUpgrades: appAccessRepository, AppAccess: appAccessRepository, AppGrants: appAccessRepository, AppDisables: appAccessRepository, AutoDeployAvailable: effectiveAutoDeploy, RelayReconcile: relayManagement.Reconcile, AutoDeployReconcile: autoDeployReconcile, DockerEndpoint: cfg.DockerEndpoint, DataRoot: cfg.DataRoot, Logger: logger, BootstrapCompleted: bootstrapCompleted}
	if runtime.ingress != nil {
		controllerServer.GatewayUpgradeRuntime = runtime.ingress
		controllerServer.LANGrantRuntime = runtime.ingress
		controllerServer.LANDisableRuntime = runtime.ingress
	}
	applyRuntimeCapabilities(controllerServer, capabilities)
	s := &http.Server{Addr: cfg.ListenAddress, Handler: controllerServer.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdown)
	}()
	logger.Info("hostd listening", "address", cfg.ListenAddress, "fake_runtime", capabilities.fake, "compose_runtime", capabilities.compose, "generated_runtime", capabilities.generated)
	serveErr := s.Serve(listener)
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

func runRecoveryOnlyController(cfg config.Config, logger *slog.Logger, listener net.Listener, authentication *auth.Service,
	upgrades *appaccess.Repository, gate gatewayStartup,
	bootstrapCompleted func(),
) int {
	if gate.recoveryID == "" || gate.ingress == nil || upgrades == nil || listener == nil ||
		(gate.recoveryKind != controller.RecoveryGatewayUpgrade && gate.recoveryKind != controller.RecoveryLANGrant && gate.recoveryKind != controller.RecoveryLANDisable) ||
		((gate.recoveryKind == controller.RecoveryLANGrant || gate.recoveryKind == controller.RecoveryLANDisable) && gate.recoveryAppID == "") ||
		(gate.recoveryBatch && gate.recoveryKind != controller.RecoveryLANGrant && gate.recoveryKind != controller.RecoveryLANDisable) {
		logger.Error("gateway recovery controller is missing its pinned operation")
		return 1
	}
	var recoveryHeads controller.LANRecoveryHeadService
	if gate.recoveryKind == controller.RecoveryLANGrant || gate.recoveryKind == controller.RecoveryLANDisable {
		reader, err := newLANRecoveryHeadReader(upgrades, gate.ingress, gate)
		if err != nil {
			logger.Error("LAN recovery reader is missing its pinned operation", "error", err)
			return 1
		}
		recoveryHeads = reader
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &controller.Server{
		Auth: authentication, GatewayUpgrades: upgrades, GatewayUpgradeRuntime: gate.ingress,
		AppAccess: upgrades, AppGrants: upgrades, LANGrantRuntime: gate.ingress,
		AppDisables: upgrades, LANDisableRuntime: gate.ingress, LANRecoveryHeads: recoveryHeads,
		GeneratedRuntime: true, RecoveryOnly: true, RecoveryKind: gate.recoveryKind,
		RecoveryOperationID: gate.recoveryID, RecoveryAppID: gate.recoveryAppID,
		RecoveryLANBatch: gate.recoveryBatch, RecoveryBatchHead: gate.recoveryBatchHead,
		RecoveryBatchCount: gate.recoveryBatchCount,
		Logger:             logger, BootstrapCompleted: bootstrapCompleted,
	}
	httpServer := &http.Server{Addr: cfg.ListenAddress, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	logger.Info("hostd recovery controller listening", "address", cfg.ListenAddress, "operation_id", gate.recoveryID)
	err := httpServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		logger.Error("recovery controller stopped", "error", err)
		return 1
	}
	return 0
}

type runtimeRecovery struct {
	deployments func(context.Context) error
	jobs        func() error
	admission   func(context.Context) (func() error, error)
}

func prepareRuntimeWorker(ctx context.Context, recovery runtimeRecovery, executor jobs.Executor, run func(context.Context, jobs.Executor) error, reportFailure func(error)) (<-chan struct{}, error) {
	if recovery.deployments == nil || recovery.jobs == nil {
		return nil, errors.New("runtime recovery dependencies are required")
	}
	recoverState := func() error {
		if err := recovery.deployments(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("recover deployments: %w", err)
		}
		if err := recovery.jobs(); err != nil {
			return fmt.Errorf("recover jobs: %w", err)
		}
		return nil
	}
	if recovery.admission != nil {
		admissionContext, cancelAdmission := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		release, err := recovery.admission(admissionContext)
		cancelAdmission()
		if err != nil {
			return nil, fmt.Errorf("admit runtime recovery: %w", err)
		}
		if release == nil {
			return nil, errors.New("runtime recovery admission returned no release")
		}
		recoverErr := recoverState()
		releaseErr := release()
		if recoverErr != nil || releaseErr != nil {
			return nil, errors.Join(recoverErr, releaseErr)
		}
	} else if err := recoverState(); err != nil {
		return nil, err
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
