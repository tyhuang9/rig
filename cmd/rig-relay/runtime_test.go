package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/config"
	"github.com/hostd/hostd/internal/relay/service"
	"github.com/hostd/hostd/internal/relay/store"
	"github.com/hostd/hostd/internal/relay/wss"
)

type runtimeStoreStub struct {
	order *[]string
	mu    *sync.Mutex
}

func (s *runtimeStoreStub) record(value string) {
	s.mu.Lock()
	*s.order = append(*s.order, value)
	s.mu.Unlock()
}
func (*runtimeStoreStub) Ready(context.Context) error                      { return nil }
func (*runtimeStoreStub) ExpireEnrollments(context.Context) (int64, error) { return 0, nil }
func (*runtimeStoreStub) ExpireRotations(context.Context) (int64, error)   { return 0, nil }
func (*runtimeStoreStub) PruneDurableState(context.Context, store.DurableRetentionPolicy) (store.DurablePruneResult, error) {
	return store.DurablePruneResult{}, nil
}
func (s *runtimeStoreStub) Close() { s.record("store_close") }

type runtimeServiceStub struct {
	order *[]string
	mu    *sync.Mutex
}

func (*runtimeServiceStub) Handler() http.Handler                    { return http.NotFoundHandler() }
func (*runtimeServiceStub) RunRecoveryScan(context.Context) error    { return nil }
func (*runtimeServiceStub) RunRedeliveryBatch(context.Context) error { return nil }
func (s *runtimeServiceStub) Close() {
	s.mu.Lock()
	*s.order = append(*s.order, "service_close")
	s.mu.Unlock()
}

type runtimeWSSStub struct {
	order   *[]string
	mu      *sync.Mutex
	waitErr error
}

func (*runtimeWSSStub) ServeHTTP(http.ResponseWriter, *http.Request) {}
func (*runtimeWSSStub) Stats() wss.Stats                             { return wss.Stats{Capacity: 4} }
func (s *runtimeWSSStub) StopAdmissions()                            { s.record("wss_stop") }
func (s *runtimeWSSStub) record(value string) {
	s.mu.Lock()
	*s.order = append(*s.order, value)
	s.mu.Unlock()
}
func (s *runtimeWSSStub) Wait(context.Context) error {
	s.mu.Lock()
	*s.order = append(*s.order, "wss_wait")
	s.mu.Unlock()
	return s.waitErr
}

type runtimeSchedulerStub struct {
	order       *[]string
	mu          *sync.Mutex
	secretViews [][]byte
	waitErr     error
}

func (s *runtimeSchedulerStub) Start(context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, secret := range s.secretViews {
		for _, value := range secret {
			if value != 0 {
				*s.order = append(*s.order, "secret_not_destroyed")
				return
			}
		}
	}
	*s.order = append(*s.order, "secrets_destroyed", "jobs_start")
}
func (s *runtimeSchedulerStub) Wait(context.Context) error {
	s.mu.Lock()
	*s.order = append(*s.order, "jobs_wait")
	s.mu.Unlock()
	return s.waitErr
}

func TestDrainFailureIsNonzeroAndSkipsDependencyClose(t *testing.T) {
	cfg := validRuntimeConfig(t)
	var order []string
	var mu sync.Mutex
	server := &runtimeServerStub{order: &order, mu: &mu, serveStart: make(chan struct{}), serveDone: make(chan struct{})}
	deps := runtimeDependencies(t, cfg, server, &order, &mu)
	originalScheduler := deps.newScheduler
	deps.newScheduler = func(recovery recoveryJobs, maintenance maintenanceJobs, interval time.Duration, metricSet *metrics) schedulerRuntime {
		runtime := originalScheduler(recovery, maintenance, interval, metricSet).(*runtimeSchedulerStub)
		runtime.waitErr = errors.New("stalled job")
		return runtime
	}
	if code := runWithDependencies(deps); code != 1 {
		t.Fatalf("exit code=%d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(order, ",")
	if strings.Contains(joined, "service_close") || strings.Contains(joined, "store_close") {
		t.Fatalf("unsafe dependency close order=%v", order)
	}
	if !strings.Contains(joined, "log:shutdown_failed") || !strings.Contains(joined, "wss_wait") {
		t.Fatalf("missing independent drains/order=%v", order)
	}
}

type listenerStub struct{}

func (listenerStub) Accept() (net.Conn, error) { return nil, errors.New("not used") }
func (listenerStub) Close() error              { return nil }
func (listenerStub) Addr() net.Addr            { return addressStub("127.0.0.1:7346") }

type addressStub string

func (addressStub) Network() string  { return "tcp" }
func (a addressStub) String() string { return string(a) }

type runtimeServerStub struct {
	order       *[]string
	mu          *sync.Mutex
	serveStart  chan struct{}
	serveDone   chan struct{}
	serveErr    error
	returnNow   bool
	shutdownOne sync.Once
}

func (s *runtimeServerStub) Serve(net.Listener) error {
	close(s.serveStart)
	if s.returnNow {
		return s.serveErr
	}
	<-s.serveDone
	return http.ErrServerClosed
}
func (s *runtimeServerStub) Shutdown(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > shutdownTimeout {
		return errors.New("shutdown context not bounded")
	}
	s.mu.Lock()
	*s.order = append(*s.order, "listener_stop")
	s.mu.Unlock()
	s.shutdownOne.Do(func() { close(s.serveDone) })
	return nil
}
func (s *runtimeServerStub) Close() error {
	s.mu.Lock()
	*s.order = append(*s.order, "server_force_close")
	s.mu.Unlock()
	s.shutdownOne.Do(func() { close(s.serveDone) })
	return nil
}

func validRuntimeConfig(t *testing.T) config.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	baseURL, err := url.Parse("https://relay.example.test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.PublicBaseURL = baseURL
	cfg.PostgresDSN = config.Secret("postgres://relay:password@database/relay")
	cfg.GitHubClientID = "client"
	cfg.GitHubAppID = 42
	cfg.GitHubClientSecret = config.Secret("0123456789abcdef")
	cfg.GitHubPrivateKey = config.Secret(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	cfg.WebhookSecret = config.Secret("0123456789abcdef")
	cfg.EnrollmentKey = config.Secret(make([]byte, 32))
	certificate, privateKey := testTLSCertificate(t)
	cfg.TLSCertificate = certificate
	cfg.TLSPrivateKey = config.Secret(privateKey)
	return cfg
}

func runtimeDependencies(t *testing.T, cfg config.Config, server *runtimeServerStub, order *[]string, mu *sync.Mutex) dependencies {
	t.Helper()
	persistence := &runtimeStoreStub{order: order, mu: mu}
	serviceStub := &runtimeServiceStub{order: order, mu: mu}
	wssStub := &runtimeWSSStub{order: order, mu: mu}
	schedulerStub := &runtimeSchedulerStub{order: order, mu: mu, secretViews: [][]byte{cfg.PostgresDSN, cfg.GitHubClientSecret, cfg.GitHubPrivateKey, cfg.WebhookSecret, cfg.EnrollmentKey, cfg.TLSCertificate, cfg.TLSPrivateKey}}
	logger := safeLogger(io.Discard)
	return dependencies{
		loadConfig: func() (config.Config, error) { return cfg, nil },
		openStore: func(ctx context.Context, dsn string, options store.Options) (relayPersistence, error) {
			if _, ok := ctx.Deadline(); !ok || dsn != string(cfg.PostgresDSN) {
				t.Fatal("unbounded startup or changed DSN")
			}
			if options.ReadinessObserver == nil {
				t.Fatal("readiness observer not wired")
			}
			mu.Lock()
			*order = append(*order, "store_open")
			mu.Unlock()
			return persistence, nil
		},
		newService: func(_ relayPersistence, options service.Options) (relayService, error) {
			if options.Transport == http.DefaultTransport || options.Now == nil || options.Random == nil {
				t.Fatal("service dependencies not cloned/injected")
			}
			return serviceStub, nil
		},
		newWebsocket: func(_ relayPersistence, _ wss.Config, options wss.Options) (websocketRuntime, error) {
			if options.Lifecycle == nil || options.Logger != logger {
				t.Fatal("missing shared lifecycle or safe process logger")
			}
			return wssStub, nil
		},
		listen: func(network, address string) (net.Listener, error) {
			if network != "tcp" || address != cfg.ListenAddress {
				t.Fatal("invalid listen mapping")
			}
			mu.Lock()
			*order = append(*order, "listen")
			mu.Unlock()
			return listenerStub{}, nil
		},
		wrapTLS: func(listener net.Listener, tlsConfig *tls.Config) net.Listener {
			if tlsConfig == nil || tlsConfig.MinVersion != tls.VersionTLS12 {
				t.Fatal("production listener missing validated TLS")
			}
			return listener
		},
		newServer: func(*http.Server) serverRuntime { return server },
		newScheduler: func(recovery recoveryJobs, maintenance maintenanceJobs, interval time.Duration, _ *metrics) schedulerRuntime {
			if recovery != serviceStub || maintenance != persistence || interval != cfg.RecoveryInterval {
				t.Fatal("scheduler dependency mismatch")
			}
			return schedulerStub
		},
		signals: func() (<-chan os.Signal, func()) {
			value := make(chan os.Signal, 1)
			go func() {
				<-server.serveStart
				value <- os.Interrupt
			}()
			return value, func() {}
		},
		transport: http.DefaultTransport.(*http.Transport),
		now:       time.Now,
		random:    io.LimitReader(rand.Reader, 1<<20),
		logCode:   func(code string) { mu.Lock(); *order = append(*order, "log:"+code); mu.Unlock() },
		logger:    logger,
	}
}

func TestRunDestroysSecretsAndShutsDownInOrder(t *testing.T) {
	cfg := validRuntimeConfig(t)
	var order []string
	var mu sync.Mutex
	server := &runtimeServerStub{order: &order, mu: &mu, serveStart: make(chan struct{}), serveDone: make(chan struct{})}
	if code := runWithDependencies(runtimeDependencies(t, cfg, server, &order, &mu)); code != 0 {
		t.Fatalf("exit code=%d", code)
	}
	want := []string{"store_open", "listen", "secrets_destroyed", "jobs_start", "wss_stop", "listener_stop", "jobs_wait", "wss_wait", "service_close", "store_close"}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != len(want) {
		t.Fatalf("shutdown order=%v want=%v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("shutdown order=%v want=%v", order, want)
		}
	}
}

func TestUnexpectedServeFailureIsNonzeroWithoutRawErrorLog(t *testing.T) {
	for _, serveErr := range []error{nil, http.ErrServerClosed, errors.New("secret-listener-address")} {
		cfg := validRuntimeConfig(t)
		var order []string
		var mu sync.Mutex
		server := &runtimeServerStub{order: &order, mu: &mu, serveStart: make(chan struct{}), serveDone: make(chan struct{}), serveErr: serveErr, returnNow: true}
		deps := runtimeDependencies(t, cfg, server, &order, &mu)
		deps.signals = func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() {} }
		if code := runWithDependencies(deps); code != 1 {
			t.Fatalf("serveErr=%v exit code=%d", serveErr, code)
		}
		mu.Lock()
		if order[len(order)-1] != "log:serve_failed" {
			t.Fatalf("serve failure order=%v", order)
		}
		for _, event := range order {
			if event == "secret-listener-address" {
				t.Fatalf("raw serve error leaked: %v", order)
			}
		}
		mu.Unlock()
	}
}

func TestStartupFailureDestroysConfigAndClosesStoreLast(t *testing.T) {
	cfg := validRuntimeConfig(t)
	certificate, tlsKey := testTLSCertificate(t)
	cfg.TLSCertificate = certificate
	cfg.TLSPrivateKey = config.Secret(tlsKey)
	secretViews := [][]byte{cfg.PostgresDSN, cfg.GitHubClientSecret, cfg.GitHubPrivateKey, cfg.WebhookSecret, cfg.EnrollmentKey, cfg.TLSCertificate, cfg.TLSPrivateKey}
	var order []string
	var mu sync.Mutex
	server := &runtimeServerStub{order: &order, mu: &mu, serveStart: make(chan struct{}), serveDone: make(chan struct{})}
	deps := runtimeDependencies(t, cfg, server, &order, &mu)
	deps.wrapTLS = func(listener net.Listener, tlsConfig *tls.Config) net.Listener {
		if tlsConfig.MinVersion != tls.VersionTLS12 {
			t.Fatal("TLS minimum not preserved")
		}
		return listener
	}
	deps.listen = func(string, string) (net.Listener, error) { return nil, errors.New("secret-bind-address") }
	if code := runWithDependencies(deps); code != 1 {
		t.Fatalf("exit code=%d", code)
	}
	for _, secret := range secretViews {
		for _, value := range secret {
			if value != 0 {
				t.Fatal("startup failure retained raw config secret")
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	wantTail := []string{"log:listen_failed", "service_close", "store_close"}
	if len(order) < len(wantTail) {
		t.Fatalf("startup failure order=%v", order)
	}
	for index, want := range wantTail {
		if order[len(order)-len(wantTail)+index] != want {
			t.Fatalf("startup failure order=%v", order)
		}
	}
}

func TestTLSStartupFailuresDestroyAllRawConfiguration(t *testing.T) {
	validCert, validKey := testTLSCertificate(t)
	_, otherKey := testTLSCertificate(t)
	for _, pair := range []struct {
		name string
		cert []byte
		key  []byte
	}{
		{name: "certificate only", cert: append([]byte(nil), validCert...)},
		{name: "key only", key: append([]byte(nil), validKey...)},
		{name: "malformed", cert: []byte("secret-certificate"), key: []byte("secret-private-key")},
		{name: "mismatch", cert: append([]byte(nil), validCert...), key: append([]byte(nil), otherKey...)},
	} {
		t.Run(pair.name, func(t *testing.T) {
			cfg := validRuntimeConfig(t)
			cfg.TLSCertificate = pair.cert
			cfg.TLSPrivateKey = config.Secret(pair.key)
			views := [][]byte{cfg.PostgresDSN, cfg.GitHubClientSecret, cfg.GitHubPrivateKey, cfg.WebhookSecret, cfg.EnrollmentKey, cfg.TLSCertificate, cfg.TLSPrivateKey}
			opened := false
			logged := ""
			deps := dependencies{
				loadConfig: func() (config.Config, error) { return cfg, nil },
				openStore:  func(context.Context, string, store.Options) (relayPersistence, error) { opened = true; return nil, nil },
				logCode:    func(code string) { logged = code },
			}
			if code := runWithDependencies(deps); code != 1 || opened || logged != "tls_invalid" {
				t.Fatalf("exit=%d opened=%v log=%q", code, opened, logged)
			}
			for _, secret := range views {
				for _, value := range secret {
					if value != 0 {
						t.Fatal("TLS startup failure retained raw secret")
					}
				}
			}
		})
	}
}

func TestProductionPlaintextRuntimeRejectedBeforeStoreOpen(t *testing.T) {
	cfg := validRuntimeConfig(t)
	views := [][]byte{cfg.PostgresDSN, cfg.GitHubClientSecret, cfg.GitHubPrivateKey, cfg.WebhookSecret, cfg.EnrollmentKey}
	clear(cfg.TLSCertificate)
	clear(cfg.TLSPrivateKey)
	cfg.TLSCertificate = nil
	cfg.TLSPrivateKey = nil
	opened := false
	logged := ""
	deps := dependencies{
		loadConfig: func() (config.Config, error) { return cfg, nil },
		openStore: func(context.Context, string, store.Options) (relayPersistence, error) {
			opened = true
			return nil, nil
		},
		logCode: func(code string) { logged = code },
	}
	if code := runWithDependencies(deps); code != 1 || opened || logged != "tls_invalid" {
		t.Fatalf("exit=%d opened=%v log=%q", code, opened, logged)
	}
	for _, view := range views {
		for _, value := range view {
			if value != 0 {
				t.Fatal("production plaintext rejection retained raw secret")
			}
		}
	}
}

func TestLoopbackDevelopmentPlaintextRuntimeAccepted(t *testing.T) {
	cfg := validRuntimeConfig(t)
	cfg.LoopbackDevelopment = true
	cfg.ListenAddress = "127.0.0.1:7346"
	cfg.PublicBaseURL, _ = url.Parse("http://127.0.0.1:7346")
	clear(cfg.TLSCertificate)
	clear(cfg.TLSPrivateKey)
	cfg.TLSCertificate = nil
	cfg.TLSPrivateKey = nil
	var order []string
	var mu sync.Mutex
	server := &runtimeServerStub{order: &order, mu: &mu, serveStart: make(chan struct{}), serveDone: make(chan struct{})}
	deps := runtimeDependencies(t, cfg, server, &order, &mu)
	deps.wrapTLS = func(net.Listener, *tls.Config) net.Listener {
		t.Fatal("loopback plaintext listener was wrapped in TLS")
		return nil
	}
	if code := runWithDependencies(deps); code != 0 {
		t.Fatalf("exit code=%d", code)
	}
}

func TestDefaultConstructorsRejectIncompatiblePersistenceWithoutPanic(t *testing.T) {
	deps := defaultDependencies()
	order := []string{}
	persistence := &runtimeStoreStub{order: &order, mu: new(sync.Mutex)}
	if _, err := deps.newService(persistence, service.Options{}); err == nil {
		t.Fatal("incompatible service persistence accepted")
	}
	if _, err := deps.newWebsocket(persistence, wss.DefaultConfig(), wss.Options{}); err == nil {
		t.Fatal("incompatible WSS persistence accepted")
	}
}
