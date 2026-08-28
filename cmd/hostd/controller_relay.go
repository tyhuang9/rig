package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controllerrelay"
)

const controllerRelayShutdownTimeout = 10 * time.Second

type controllerRelayRunner interface {
	Run(context.Context) error
	Snapshot() controllerrelay.SupervisorSnapshot
}

type controllerRelayFactory func() (controllerRelayRunner, error)

// startControllerRelay is deliberately a no-op before invoking its factory
// unless the paired controller relay configuration enabled it.
func startControllerRelay(ctx context.Context, cfg config.Config, logger *slog.Logger, factory controllerRelayFactory) <-chan struct{} {
	done := make(chan struct{})
	if !cfg.ControllerRelay {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		if factory == nil {
			logger.Warn("controller relay unavailable", "outcome", "persistence_unavailable")
			return
		}
		runner, err := factory()
		if err != nil || runner == nil {
			// Construction must not make the primary host or manual deployments
			// unavailable. Never attach an arbitrary construction error because it
			// may include credential-provider or endpoint detail.
			logger.Warn("controller relay unavailable", "outcome", "persistence_unavailable")
			return
		}
		if runner.Run(ctx) != nil {
			// Relay lifecycle outcomes are isolated from host lifetime and are
			// intentionally fixed-safe here; detailed observability comes only
			// from the safe Supervisor observer below.
			logger.Warn("controller relay stopped", "outcome", "persistence_unavailable")
		}
	}()
	return done
}

func waitForControllerRelay(done <-chan struct{}, timeout time.Duration, logger *slog.Logger) bool {
	if waitForWorker(done, timeout) {
		return true
	}
	logger.Warn("controller relay did not stop before shutdown timeout", "outcome", "persistence_unavailable")
	return false
}

func newControllerRelayRunner(cfg config.Config, db *sql.DB, logger *slog.Logger) (controllerRelayRunner, error) {
	if !cfg.ControllerRelay || db == nil || logger == nil {
		return nil, errors.New("controller relay is unavailable")
	}
	repository := controllerrelay.NewRepository(db)
	credentials, err := controllerrelay.NewFileCredentialStore(cfg.DataRoot)
	if err != nil {
		return nil, err
	}
	controls, err := controllerrelay.NewSessionControlService(repository, credentials, controllerrelay.DefaultSessionControlConfig())
	if err != nil {
		return nil, err
	}
	transportConfig := controllerrelay.DefaultSessionTransportConfig()
	transportConfig.ControlHandler = controls
	transport, err := controllerrelay.NewSessionTransport(cfg.RelayOrigin, repository, credentials, nil, nil, transportConfig)
	if err != nil {
		return nil, err
	}
	supervisorConfig := controllerrelay.DefaultSupervisorConfig()
	var supervisor *controllerrelay.Supervisor
	supervisorConfig.Observer = func(_ context.Context, event controllerrelay.SupervisorEvent) error {
		snapshot := controllerrelay.SupervisorSnapshot{}
		if supervisor != nil {
			snapshot = supervisor.Snapshot()
		}
		logControllerRelayEvent(logger, event, snapshot)
		return nil
	}
	supervisor, err = controllerrelay.NewSupervisor(transport, repository, controls, controls, supervisorConfig)
	if err != nil {
		return nil, err
	}
	return supervisor, nil
}

func logControllerRelayEvent(logger *slog.Logger, event controllerrelay.SupervisorEvent, snapshot controllerrelay.SupervisorSnapshot) {
	logger.Info("controller relay lifecycle",
		"kind", safeRelayKind(event.Kind), "stage", safeRelayStage(event.Stage), "fallback", event.Fallback,
		"outcome", safeRelayOutcome(event.Outcome), "attempt", event.Attempt,
		"recovery_scanned", event.Recovery.Scanned, "recovery_cleaned", event.Recovery.Cleaned, "recovery_issues", event.Recovery.Issues,
		"recovery_lease_scanned", event.Recovery.LeaseScanned, "recovery_revoked_scanned", event.Recovery.RevokedScanned,
		"recovery_credential_scanned", event.Recovery.CredentialScanned, "recovery_temporary_scanned", event.Recovery.TemporaryScanned,
		"pending_commands", event.Diagnostics.PendingCommands, "oldest_pending_age", event.Diagnostics.OldestPendingAge,
		"active_leases", event.Diagnostics.ActiveLeases, "expired_leases", event.Diagnostics.ExpiredLeases,
		"observer_dropped", snapshot.ObserverDropped)
}

func safeRelayKind(value string) string {
	switch value {
	case controllerrelay.SupervisorHandshake, controllerrelay.SupervisorFallback, controllerrelay.SupervisorReady, controllerrelay.SupervisorDisconnect, controllerrelay.SupervisorBackoff, controllerrelay.SupervisorAttention, controllerrelay.SupervisorStopped, controllerrelay.SupervisorRecovery, controllerrelay.SupervisorDiagnostics:
		return value
	default:
		return "unknown"
	}
}

func safeRelayStage(value string) string {
	switch value {
	case controllerrelay.SessionTransportConnecting, controllerrelay.SessionTransportAuthenticating, controllerrelay.SessionTransportReady, controllerrelay.SessionBackoff, controllerrelay.SessionNeedsAttention, controllerrelay.SessionStopped:
		return value
	default:
		return "unknown"
	}
}

func safeRelayOutcome(value string) string {
	switch value {
	case "relay_unavailable", "connection_closed", "protocol_error", "persistence_unavailable", "credential_unavailable", "identity_unavailable", "queue_saturated", "session_expired":
		return value
	default:
		return "protocol_error"
	}
}
