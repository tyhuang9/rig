package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPErrorLogDiscardsRawBytesAndCoalesces(t *testing.T) {
	const sentinel = "ghp_TOKEN_secret_peer_203.0.113.44_raw-error_goroutine"
	var output bytes.Buffer
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	metricSet := &metrics{now: func() time.Time { return now }}
	errorLog := newHTTPErrorLog(metricSet, safeLogger(&output), func() time.Time { return now })
	for i := 0; i < 20; i++ {
		errorLog.Print(sentinel)
	}
	if metricSet.httpServerErrors.Load() != 20 {
		t.Fatalf("server error count=%d", metricSet.httpServerErrors.Load())
	}
	if got := strings.Count(output.String(), "code=server_error"); got != 1 {
		t.Fatalf("coalesced log count=%d output=%q", got, output.String())
	}
	assertNoErrorLogSentinel(t, output.String(), sentinel)
}

func TestProcessLifecycleLogsRejectDynamicPhases(t *testing.T) {
	const sentinel = "ghp_SECRET_DYNAMIC_PHASE"
	var output bytes.Buffer
	logger := safeLogger(&output)
	logProcessPhase(logger, "startup", sentinel)
	logProcessPhase(logger, "startup", "configured")
	if got := output.String(); !strings.Contains(got, "lifecycle=startup phase=configured") || strings.Contains(got, sentinel) || strings.Count(got, "relay process lifecycle") != 1 {
		t.Fatalf("process lifecycle log=%q", got)
	}
}

func TestHTTPErrorLogSanitizesMalformedTLSAndPanicOverRealTCP(t *testing.T) {
	const marker = "ghp_INJECTED_TOKEN_secret_raw_error"
	var output bytes.Buffer
	metricSet := &metrics{}
	errorLog := newHTTPErrorLog(metricSet, safeLogger(&output), time.Now)

	certificate, privateKey := testTLSCertificate(t)
	pair, err := tls.X509KeyPair(certificate, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	tlsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsServer := &http.Server{Handler: http.NotFoundHandler(), ErrorLog: errorLog}
	tlsDone := make(chan error, 1)
	go func() {
		tlsDone <- tlsServer.Serve(tls.NewListener(tlsListener, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}))
	}()
	connection, err := net.DialTimeout("tcp", tlsListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(connection, "%s\r\n", marker)
	_ = connection.Close()
	waitForServerErrors(t, metricSet, 1)
	shutdownServer(t, tlsServer, tlsDone)

	panicListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	panicServer := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(marker) }), ErrorLog: errorLog}
	panicDone := make(chan error, 1)
	go func() { panicDone <- panicServer.Serve(panicListener) }()
	panicConnection, err := net.DialTimeout("tcp", panicListener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(panicConnection, "GET /%s HTTP/1.1\r\nHost: relay.test\r\nConnection: close\r\n\r\n", marker)
	_ = panicConnection.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = bufio.NewReader(panicConnection).ReadString('\n')
	_ = panicConnection.Close()
	waitForServerErrors(t, metricSet, 2)
	shutdownServer(t, panicServer, panicDone)

	if metricSet.httpServerErrors.Load() != 2 {
		t.Fatalf("server error count=%d", metricSet.httpServerErrors.Load())
	}
	assertNoErrorLogSentinel(t, output.String(), marker)
}

func waitForServerErrors(t *testing.T, metricSet *metrics, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for metricSet.httpServerErrors.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := metricSet.httpServerErrors.Load(); got < want {
		t.Fatalf("server error count=%d want at least %d", got, want)
	}
}

func shutdownServer(t *testing.T, server *http.Server, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func assertNoErrorLogSentinel(t *testing.T, output string, marker string) {
	t.Helper()
	for _, forbidden := range []string{marker, "127.0.0.1", "203.0.113.44", "ghp_", "secret", "raw-error", "raw_error", "goroutine", "panic serving", "TLS handshake error"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("sanitized error log contains %q: %q", forbidden, output)
		}
	}
}
