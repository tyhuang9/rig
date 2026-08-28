package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/config"
	"github.com/hostd/hostd/internal/relay/store"
	"github.com/hostd/hostd/internal/relay/wss"
)

type readinessStub struct {
	err   error
	calls atomic.Int64
	bound atomic.Bool
}

func (s *readinessStub) Ready(ctx context.Context) error {
	s.calls.Add(1)
	deadline, ok := ctx.Deadline()
	if ok && time.Until(deadline) <= 2*time.Second {
		s.bound.Store(true)
	}
	return s.err
}

type statsStub struct{ stats wss.Stats }

func (s statsStub) Stats() wss.Stats { return s.stats }

func TestOperationalRoutesAreExactStableAndNoStore(t *testing.T) {
	var accepting atomic.Bool
	accepting.Store(true)
	ready := &readinessStub{}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fallback-Path", r.URL.Path)
		http.NotFound(w, r)
	})
	websocket := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	m := &metrics{websocket: statsStub{stats: wss.Stats{Capacity: 7}}, accepting: &accepting}
	handler := relayHTTPHandler{service: fallback, websocket: websocket, store: ready, accepting: &accepting, metrics: m, serviceSlots: make(chan struct{}, serviceConcurrency)}

	for _, test := range []struct {
		path, body string
		status     int
		storeCalls int64
	}{
		{"/healthz", "ok\n", http.StatusOK, 0},
		{"/readyz", "ready\n", http.StatusOK, 1},
		{"/v1/controllers/connect", "", http.StatusTeapot, 1},
		{"/healthz/", "not found\n", http.StatusNotFound, 1},
		{"/readyz/", "not found\n", http.StatusNotFound, 1},
		{"/v1/controllers/connect/", "not found\n", http.StatusNotFound, 1},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://relay.test"+test.path, nil)
		request.Header.Set("Forwarded", "for=203.0.113.9;proto=https")
		request.Header.Set("X-Forwarded-For", "203.0.113.9")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.status || recorder.Body.String() != test.body {
			t.Fatalf("%s: status=%d body=%q", test.path, recorder.Code, recorder.Body.String())
		}
		if ready.calls.Load() != test.storeCalls {
			t.Fatalf("%s: readiness calls=%d want=%d", test.path, ready.calls.Load(), test.storeCalls)
		}
	}
	if !ready.bound.Load() {
		t.Fatal("readiness context was not bounded to two seconds")
	}
}

func TestOperationalMethodsAndUnreadyResponses(t *testing.T) {
	var accepting atomic.Bool
	ready := &readinessStub{}
	handler := relayHTTPHandler{service: http.NotFoundHandler(), websocket: http.NotFoundHandler(), store: ready, accepting: &accepting, metrics: &metrics{accepting: &accepting}, serviceSlots: make(chan struct{}, serviceConcurrency)}
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodPut} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(method, "http://relay.test"+path, nil))
			if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Body.String() != "method not allowed\n" {
				t.Fatalf("%s %s: status=%d headers=%v body=%q", method, path, recorder.Code, recorder.Header(), recorder.Body.String())
			}
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://relay.test/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != "not ready\n" || ready.calls.Load() != 0 {
		t.Fatalf("unready response status=%d body=%q calls=%d", recorder.Code, recorder.Body.String(), ready.calls.Load())
	}
}

func TestReservedRoutesRejectQueryAndEscapedForms(t *testing.T) {
	var accepting atomic.Bool
	accepting.Store(true)
	ready := &readinessStub{}
	serviceCalls := atomic.Int64{}
	handler := relayHTTPHandler{
		service:   http.HandlerFunc(func(http.ResponseWriter, *http.Request) { serviceCalls.Add(1) }),
		websocket: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected WSS dispatch") }),
		store:     ready, accepting: &accepting, metrics: &metrics{accepting: &accepting}, serviceSlots: make(chan struct{}, serviceConcurrency),
	}
	for _, target := range []string{
		"http://relay.test/healthz?probe=secret",
		"http://relay.test/readyz?probe=secret",
		"http://relay.test/metrics?probe=secret",
		"http://relay.test/v1/controllers/connect?probe=secret",
		"http://relay.test/healthz%2F",
		"http://relay.test/v1/controllers/connect%2Fchild",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound || recorder.Body.String() != "not found\n" || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d headers=%v body=%q", target, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
	if serviceCalls.Load() != 0 || ready.calls.Load() != 0 {
		t.Fatalf("reserved invalid route delegated: service=%d ready=%d", serviceCalls.Load(), ready.calls.Load())
	}
}

func TestWebSocketDeadlineExemptionRequiresCanonicalHandshakeHeaders(t *testing.T) {
	valid := func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "http://relay.test"+controllerConnectPath, nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Protocol", "rig.relay.v1")
		request.Header.Set("Sec-WebSocket-Version", "13")
		request.Header.Set("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==")
		return request
	}
	if !exemptWebSocketRequest(valid()) {
		t.Fatal("canonical WebSocket handshake was not exempt")
	}
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing version", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Version") }},
		{"wrong version", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "12") }},
		{"noncanonical version", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", " 13") }},
		{"duplicate version", func(r *http.Request) { r.Header.Add("Sec-WebSocket-Version", "13") }},
		{"missing key", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") }},
		{"invalid key", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "not-base64") }},
		{"wrong key length", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "c2hvcnQ=") }},
		{"noncanonical key", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", " MDEyMzQ1Njc4OWFiY2RlZg==") }},
		{"duplicate key", func(r *http.Request) { r.Header.Add("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			test.mutate(request)
			if exemptWebSocketRequest(request) {
				t.Fatal("invalid WebSocket handshake bypassed HTTP deadlines")
			}
		})
	}
}

func TestMetricsAreDeterministicFixedCardinalityAndConcurrent(t *testing.T) {
	var accepting atomic.Bool
	accepting.Store(true)
	fixedNow := time.Unix(2_000_000_000, 0).UTC()
	stats := wss.Stats{Active: 2, Authenticated: 1, Capacity: 5, CapacityRejections: 3}
	stats.LifecycleOutcomes[0], stats.Deliveries[0], stats.Decisions[0] = 4, 5, 6
	m := &metrics{websocket: statsStub{stats: stats}, accepting: &accepting, now: func() time.Time { return fixedNow }}
	m.ObserveWebhook("persisted")
	m.ObserveWebhook("ghp_SECRET_DYNAMIC")
	m.ObserveReadiness("success", "probe_success")
	m.ObserveReadiness("failure", "raw_database_error")
	m.observeBackgroundItems("expired_enrollments", 7)
	var first strings.Builder
	m.writeTo(&first)
	text := first.String()
	if strings.Count(text, "rig_relay_background_runs_total{") != len(jobNames)*len(jobResults) {
		t.Fatalf("job series count=%d", strings.Count(text, "rig_relay_background_runs_total{"))
	}
	for _, job := range jobNames {
		for _, result := range jobResults {
			want := `rig_relay_background_runs_total{job="` + job + `",outcome="` + result + `"} 0`
			if !strings.Contains(text, want) {
				t.Fatalf("missing zero series %q", want)
			}
		}
	}
	if strings.Count(text, "rig_relay_webhook_outcomes_total{") != len(webhookOutcomes) ||
		strings.Count(text, "rig_relay_wss_lifecycle_total{") != len(wss.LifecycleOutcomeNames()) ||
		strings.Count(text, "rig_relay_wss_deliveries_total{") != len(wss.DeliveryKindNames()) ||
		strings.Count(text, "rig_relay_wss_decisions_total{") != len(wss.DecisionNames()) ||
		strings.Count(text, "rig_relay_readiness_total{") != len(readinessStates) ||
		strings.Count(text, "rig_relay_background_duration_seconds_bucket{") != len(jobNames)*len(jobResults)*(len(durationBounds)+1) {
		t.Fatalf("unexpected metric cardinality:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), "http://") || strings.Contains(strings.ToLower(text), "secret") || strings.Contains(text, "ghp_") || strings.Contains(text, "database_error") {
		t.Fatalf("metrics leaked dynamic data: %q", text)
	}
	for _, want := range []string{
		"rig_relay_wss_capacity_rejections_total 3",
		"rig_relay_wss_sessions_authenticated 1",
		`rig_relay_wss_lifecycle_total{outcome="handshake"} 4`,
		`rig_relay_wss_deliveries_total{kind="desired"} 5`,
		`rig_relay_wss_decisions_total{decision="ack"} 6`,
		`rig_relay_webhook_outcomes_total{outcome="persisted"} 1`,
		`rig_relay_readiness_total{state="probe_success"} 1`,
		`rig_relay_background_items_total{kind="expired_enrollments"} 7`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metric signal %q missing: %q", want, text)
		}
	}
	if !strings.Contains(text, "rig_relay_wss_capacity_rejections_total 3") {
		t.Fatalf("capacity rejection signal missing: %q", text)
	}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				m.observe("redelivery", "success", time.Second)
				io.Copy(io.Discard, strings.NewReader(captureMetrics(m)))
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if got := m.jobs[1][0].Load(); got != 800 {
		t.Fatalf("concurrent metric count=%d", got)
	}
}

func captureMetrics(m *metrics) string {
	var value strings.Builder
	m.writeTo(&value)
	return value.String()
}

func TestWebSocketAndHTTPTimeoutMapping(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReadTimeout = 23 * time.Second
	cfg.WriteTimeout = 17 * time.Second
	cfg.IdleTimeout = 91 * time.Second
	cfg.MaxSessionDuration = 12 * time.Hour
	cfg.MaxEnvelopeBytes = 32 << 10
	cfg.MaxSubscriptions = 77
	mapped := websocketConfig(cfg)
	if mapped.WriteTimeout != cfg.WriteTimeout || mapped.IdleTimeout != cfg.IdleTimeout || mapped.SessionLifetime != cfg.MaxSessionDuration || mapped.MaxEnvelopeBytes != cfg.MaxEnvelopeBytes || mapped.MaxSubscriptions != cfg.MaxSubscriptions || mapped.HandshakeMaxBytes != cfg.MaxEnvelopeBytes {
		t.Fatalf("WSS mapping=%+v", mapped)
	}
	if mapped.HandshakeTimeout == cfg.ReadTimeout {
		t.Fatal("HTTP read timeout was incorrectly mapped to WSS handshake timeout")
	}
	if _, err := wss.NewHandler(new(store.Store), mapped, wss.Options{}); err != nil {
		t.Fatalf("mapped WSS config rejected: %v", err)
	}
	defaultFloor := mapped.HandshakeTimeout + mapped.StoreTimeout + 2*mapped.WriteTimeout
	if other := mapped.StoreTimeout + mapped.WriteTimeout + mapped.LeaseRenewInterval; other > defaultFloor {
		defaultFloor = other
	}
	if mapped.LeaseDuration-defaultFloor < 5*time.Second {
		t.Fatalf("default lease slack=%v", mapped.LeaseDuration-defaultFloor)
	}
	cfg = config.Defaults()
	cfg.WriteTimeout = 30 * time.Second
	mapped = websocketConfig(cfg)
	if _, err := wss.NewHandler(new(store.Store), mapped, wss.Options{}); err != nil {
		t.Fatalf("30s write-timeout WSS config rejected: %+v err=%v", mapped, err)
	}
	maxFloor := mapped.HandshakeTimeout + mapped.StoreTimeout + 2*mapped.WriteTimeout
	if other := mapped.StoreTimeout + mapped.WriteTimeout + mapped.LeaseRenewInterval; other > maxFloor {
		maxFloor = other
	}
	if mapped.LeaseDuration-maxFloor < 5*time.Second {
		t.Fatalf("30s lease slack=%v", mapped.LeaseDuration-maxFloor)
	}
	server := http.Server{ReadHeaderTimeout: cfg.ReadTimeout, ReadTimeout: 0, WriteTimeout: 0, IdleTimeout: cfg.IdleTimeout}
	if server.ReadHeaderTimeout != cfg.ReadTimeout || server.ReadTimeout != 0 || server.WriteTimeout != 0 || server.IdleTimeout != cfg.IdleTimeout {
		t.Fatalf("HTTP timeouts read_header=%v read=%v write=%v idle=%v", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestTLSConfigIsInMemoryOptionalAndTLS12Minimum(t *testing.T) {
	if cfg, err := relayTLSConfig(nil, nil); err != nil || cfg != nil {
		t.Fatalf("plaintext config=%v err=%v", cfg, err)
	}
	if _, err := relayTLSConfig([]byte("cert-path-or-bytes"), nil); err == nil {
		t.Fatal("partial TLS pair accepted")
	}
	certificate, privateKey := testTLSCertificate(t)
	cfg, err := relayTLSConfig(certificate, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS12 || len(cfg.Certificates) != 1 {
		t.Fatalf("TLS config=%+v", cfg)
	}
	clear(certificate)
	clear(privateKey)
	if len(cfg.Certificates[0].Certificate) == 0 {
		t.Fatal("TLS config retained no parsed certificate after source destruction")
	}
}

func testTLSCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
