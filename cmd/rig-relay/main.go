package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hostd/hostd/internal/relay/config"
	"github.com/hostd/hostd/internal/relay/service"
	"github.com/hostd/hostd/internal/relay/store"
	"github.com/hostd/hostd/internal/relay/wss"
)

const shutdownTimeout = 15 * time.Second

func main() { os.Exit(run()) }

type relayPersistence interface {
	maintenanceJobs
	readyStore
	Close()
}

type relayService interface {
	recoveryJobs
	Handler() http.Handler
	Close()
}

type websocketRuntime interface {
	http.Handler
	Stats() wss.Stats
	StopAdmissions()
	Wait(context.Context) error
}

type serverRuntime interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type schedulerRuntime interface {
	Start(context.Context)
	Wait(context.Context) error
}

type standardServer struct{ server *http.Server }

func (s standardServer) Serve(listener net.Listener) error  { return s.server.Serve(listener) }
func (s standardServer) Shutdown(ctx context.Context) error { return s.server.Shutdown(ctx) }
func (s standardServer) Close() error                       { return s.server.Close() }

type dependencies struct {
	loadConfig   func() (config.Config, error)
	openStore    func(context.Context, string, store.Options) (relayPersistence, error)
	newService   func(relayPersistence, service.Options) (relayService, error)
	newWebsocket func(relayPersistence, wss.Config, wss.Options) (websocketRuntime, error)
	listen       func(string, string) (net.Listener, error)
	wrapTLS      func(net.Listener, *tls.Config) net.Listener
	newServer    func(*http.Server) serverRuntime
	newScheduler func(recoveryJobs, maintenanceJobs, time.Duration, *metrics) schedulerRuntime
	signals      func() (<-chan os.Signal, func())
	transport    *http.Transport
	now          func() time.Time
	random       io.Reader
	logCode      func(string)
	logger       *slog.Logger
}

func defaultDependencies() dependencies {
	return dependencies{
		loadConfig: config.LoadOS,
		openStore: func(ctx context.Context, dsn string, options store.Options) (relayPersistence, error) {
			return store.Open(ctx, dsn, options)
		},
		newService: func(persistence relayPersistence, options service.Options) (relayService, error) {
			serviceStore, ok := persistence.(service.Store)
			if !ok {
				return nil, errors.New("relay persistence lacks service contract")
			}
			options.Store = serviceStore
			return service.New(options)
		},
		newWebsocket: func(persistence relayPersistence, cfg wss.Config, options wss.Options) (websocketRuntime, error) {
			stateStore, ok := persistence.(wss.StateStore)
			if !ok {
				return nil, errors.New("relay persistence lacks WSS contract")
			}
			return wss.NewHandler(stateStore, cfg, options)
		},
		listen:    net.Listen,
		wrapTLS:   func(listener net.Listener, cfg *tls.Config) net.Listener { return tls.NewListener(listener, cfg) },
		newServer: func(server *http.Server) serverRuntime { return standardServer{server: server} },
		newScheduler: func(recovery recoveryJobs, maintenance maintenanceJobs, interval time.Duration, metricSet *metrics) schedulerRuntime {
			return newScheduler(recovery, maintenance, interval, metricSet)
		},
		signals: func() (<-chan os.Signal, func()) {
			value := make(chan os.Signal, 1)
			signal.Notify(value, os.Interrupt, syscall.SIGTERM)
			return value, func() { signal.Stop(value) }
		},
		transport: http.DefaultTransport.(*http.Transport),
		now:       time.Now,
		random:    rand.Reader,
		logCode:   logCode,
		logger:    processLogger,
	}
}

func run() int { return runWithDependencies(defaultDependencies()) }

func runWithDependencies(deps dependencies) int {
	metricSet := &metrics{now: deps.now}
	cfg, err := deps.loadConfig()
	if err != nil {
		deps.logCode("config_invalid")
		return 1
	}
	defer cfg.DestroySecrets()

	githubKey, err := config.ParseRSAPrivateKey(cfg.GitHubPrivateKey)
	if err != nil {
		deps.logCode("github_key_invalid")
		return 1
	}
	tlsConfig, err := relayTLSConfig(cfg.TLSCertificate, cfg.TLSPrivateKey)
	if err != nil {
		deps.logCode("tls_invalid")
		return 1
	}
	if !cfg.LoopbackDevelopment && tlsConfig == nil {
		deps.logCode("tls_invalid")
		return 1
	}
	logProcessPhase(deps.logger, "startup", "configured")

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	persistence, err := deps.openStore(startupCtx, string(cfg.PostgresDSN), store.Options{ReadinessObserver: metricSet})
	startupCancel()
	if err != nil {
		deps.logCode("store_open_failed")
		return 1
	}
	logProcessPhase(deps.logger, "startup", "store_ready")
	storeOpen := true
	defer func() {
		if storeOpen {
			persistence.Close()
		}
	}()

	if deps.transport == nil {
		deps.logCode("transport_invalid")
		return 1
	}
	clientSecret := cfg.GitHubClientSecret.Clone()
	webhookSecret := cfg.WebhookSecret.Clone()
	enrollmentKey := cfg.EnrollmentKey.Clone()
	relayService, err := deps.newService(persistence, service.Options{
		Transport: deps.transport.Clone(), Now: deps.now, Random: deps.random,
		PublicBaseURL: cfg.PublicBaseURL, GitHubClientID: cfg.GitHubClientID,
		GitHubClientSecret: clientSecret, GitHubAppID: cfg.GitHubAppID,
		GitHubPrivateKey: githubKey, WebhookSecret: webhookSecret, EnrollmentKey: enrollmentKey,
		RecoveryWindow: cfg.RecoveryWindow, ProviderTimeout: cfg.WriteTimeout, LoopbackDevelopment: cfg.LoopbackDevelopment,
		Observer: metricSet,
	})
	clear(clientSecret)
	clear(webhookSecret)
	clear(enrollmentKey)
	if err != nil {
		deps.logCode("service_invalid")
		return 1
	}
	logProcessPhase(deps.logger, "startup", "service_ready")
	serviceOpen := true
	defer func() {
		if serviceOpen {
			relayService.Close()
		}
	}()

	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	wssConfig := websocketConfig(cfg)
	websocketHandler, err := deps.newWebsocket(persistence, wssConfig, wss.Options{Lifecycle: lifecycle, Logger: deps.logger})
	if err != nil {
		cancelLifecycle()
		deps.logCode("wss_invalid")
		return 1
	}
	metricSet.websocket = websocketHandler
	logProcessPhase(deps.logger, "startup", "websocket_ready")

	listener, err := deps.listen("tcp", cfg.ListenAddress)
	if err != nil {
		cancelLifecycle()
		deps.logCode("listen_failed")
		return 1
	}
	logProcessPhase(deps.logger, "startup", "listener_ready")
	listenerMetrics := &listenerStats{}
	listener = newCappedListener(listener, wssConfig.MaxConnections+httpConnectionHeadroom, listenerMetrics)
	if tlsConfig != nil {
		listener = deps.wrapTLS(listener, tlsConfig)
	}

	var accepting atomic.Bool
	handler := &relayHTTPHandler{
		service: relayService.Handler(), websocket: websocketHandler, store: persistence, accepting: &accepting,
		readTimeout: cfg.ReadTimeout, writeTimeout: cfg.WriteTimeout, serviceSlots: make(chan struct{}, serviceConcurrency),
	}
	metricSet.accepting, metricSet.listener, metricSet.http = &accepting, listenerMetrics, handler
	handler.metrics = metricSet
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	httpConfig := &http.Server{
		Handler: handler, ReadHeaderTimeout: cfg.ReadTimeout, ReadTimeout: 0, WriteTimeout: 0,
		IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 1 << 20, Protocols: protocols,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, connectionContextKey{}, conn)
		},
		ErrorLog: newHTTPErrorLog(metricSet, deps.logger, deps.now),
	}
	httpServer := deps.newServer(httpConfig)

	// Every long-lived consumer has now copied or parsed its required material.
	cfg.DestroySecrets()

	jobs := deps.newScheduler(relayService, persistence, cfg.RecoveryInterval, metricSet)
	signals, stopSignals := deps.signals()
	defer stopSignals()
	accepting.Store(true)
	jobs.Start(lifecycle)
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	logProcessPhase(deps.logger, "startup", "serving")

	unexpected := false
	select {
	case <-signals:
	case <-serveErr:
		unexpected = true
	}
	accepting.Store(false)
	logProcessPhase(deps.logger, "shutdown", "started")
	handler.StopAdmissions()
	websocketHandler.StopAdmissions()
	cancelLifecycle()

	// Concurrency has started. Deferred startup cleanup is disabled: dependency
	// Close is unsafe unless all users are proven drained below.
	serviceOpen = false
	storeOpen = false
	drainFailed := false
	if err = withShutdownContext(httpServer.Shutdown); err != nil {
		drainFailed = true
		_ = httpServer.Close()
		logProcessPhase(deps.logger, "shutdown", "server_failed")
	} else {
		logProcessPhase(deps.logger, "shutdown", "server_drained")
	}
	for _, drain := range []func(context.Context) error{handler.Wait, jobs.Wait, websocketHandler.Wait} {
		if err = withShutdownContext(drain); err != nil {
			drainFailed = true
		}
	}
	if drainFailed {
		deps.logCode("shutdown_failed")
		return 1
	}
	logProcessPhase(deps.logger, "shutdown", "consumers_drained")
	relayService.Close()
	persistence.Close()
	logProcessPhase(deps.logger, "shutdown", "complete")
	if unexpected {
		deps.logCode("serve_failed")
		return 1
	}
	return 0
}

func withShutdownContext(operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return operation(ctx)
}

func websocketConfig(cfg config.Config) wss.Config {
	result := wss.DefaultConfig()
	result.WriteTimeout = cfg.WriteTimeout
	result.IdleTimeout = cfg.IdleTimeout
	result.SessionLifetime = cfg.MaxSessionDuration
	result.MaxEnvelopeBytes = cfg.MaxEnvelopeBytes
	result.MaxSubscriptions = cfg.MaxSubscriptions
	if result.HandshakeMaxBytes > cfg.MaxEnvelopeBytes {
		result.HandshakeMaxBytes = cfg.MaxEnvelopeBytes
	}
	leaseFloor := result.StoreTimeout + result.WriteTimeout + result.LeaseRenewInterval
	if handshakeFloor := result.HandshakeTimeout + result.StoreTimeout + 2*result.WriteTimeout; handshakeFloor > leaseFloor {
		leaseFloor = handshakeFloor
	}
	if result.LeaseDuration <= leaseFloor {
		result.LeaseDuration = leaseFloor + 5*time.Second
	}
	return result
}

func relayTLSConfig(certificate, privateKey []byte) (*tls.Config, error) {
	if len(certificate) == 0 && len(privateKey) == 0 {
		return nil, nil
	}
	if len(certificate) == 0 || len(privateKey) == 0 {
		return nil, errors.New("relay TLS pair required")
	}
	pair, err := tls.X509KeyPair(certificate, privateKey)
	if err != nil {
		return nil, errors.New("relay TLS pair invalid")
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}

var processLogger = safeLogger(os.Stderr)

func safeLogger(destination io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(destination, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
		if attribute.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attribute
	}}))
}

func logCode(code string) {
	processLogger.Error("relay process event", "code", code)
}

func logProcessPhase(logger *slog.Logger, lifecycle, phase string) {
	if logger == nil || !closedProcessPhase(lifecycle, phase) {
		return
	}
	logger.Info("relay process lifecycle", "lifecycle", lifecycle, "phase", phase)
}

func closedProcessPhase(lifecycle, phase string) bool {
	switch lifecycle + "/" + phase {
	case "startup/configured", "startup/store_ready", "startup/service_ready", "startup/websocket_ready", "startup/listener_ready", "startup/serving",
		"shutdown/started", "shutdown/server_failed", "shutdown/server_drained", "shutdown/consumers_drained", "shutdown/complete":
		return true
	default:
		return false
	}
}
