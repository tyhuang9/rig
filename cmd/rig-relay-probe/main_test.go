package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeHealthAndReadiness(t *testing.T) {
	server, caFile := newTLSServer(t, "relay.internal.example", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte("ok\n"))
		case "/readyz":
			_, _ = w.Write([]byte("ready\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for _, endpoint := range []string{"health", "ready"} {
		t.Run(endpoint, func(t *testing.T) {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			code := run(probeArgs(server.URL, caFile, endpoint, "relay.internal.example", "2s"), stdout, stderr)
			if code != 0 || stdout.String() != "relay probe ok\n" || stderr.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestProbeRejectsUntrustedAndWrongHostname(t *testing.T) {
	server, caFile := newTLSServer(t, "relay.internal.example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer server.Close()
	other := filepath.Join(t.TempDir(), "other-ca.pem")
	_, otherCA := certificatePair(t, "other.internal.example")
	if err := os.WriteFile(other, otherCA, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, ca, serverName string
	}{
		{"untrusted CA", other, "relay.internal.example"},
		{"hostname mismatch", caFile, "wrong.internal.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertProbeFailure(t, probeArgs(server.URL, test.ca, "health", test.serverName, "2s"))
		})
	}
}

func TestProbeRejectsRedirectOversizeAndTimeout(t *testing.T) {
	var requests atomic.Int32
	redirect, caFile := newTLSServer(t, "relay.internal.example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Redirect(w, &http.Request{}, "https://redirect.internal.example/healthz", http.StatusFound)
	}))
	assertProbeFailure(t, probeArgs(redirect.URL, caFile, "health", "relay.internal.example", "2s"))
	redirect.Close()
	if requests.Load() != 1 {
		t.Fatalf("redirect followed: requests=%d", requests.Load())
	}

	oversize, caFile := newTLSServer(t, "relay.internal.example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maximumResponseBytes+1)))
	}))
	assertProbeFailure(t, probeArgs(oversize.URL, caFile, "health", "relay.internal.example", "2s"))
	oversize.Close()

	oversizeHeader, caFile := newTLSServer(t, "relay.internal.example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Oversized", strings.Repeat("x", (16<<10)+1))
		_, _ = w.Write([]byte("ok\n"))
	}))
	assertProbeFailure(t, probeArgs(oversizeHeader.URL, caFile, "health", "relay.internal.example", "2s"))
	oversizeHeader.Close()

	slow, caFile := newTLSServer(t, "relay.internal.example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("ok\n"))
	}))
	assertProbeFailure(t, probeArgs(slow.URL, caFile, "health", "relay.internal.example", "20ms"))
	slow.Close()
}

func TestProbeRejectsArbitraryOrInsecureTargets(t *testing.T) {
	for _, base := range []string{
		"http://relay.internal.example:7346",
		"https://relay.internal.example:7346/admin",
		"https://relay.internal.example:7346/?token=sensitive",
		"https://user:password@relay.internal.example:7346",
		"https://relay.internal.example:7346/#fragment",
	} {
		t.Run(base, func(t *testing.T) {
			assertProbeFailure(t, probeArgs(base, "", "health", "relay.internal.example", "2s"))
		})
	}
	for _, endpoint := range []string{"", "metrics", "/healthz"} {
		assertProbeFailure(t, probeArgs("https://relay.internal.example:7346", "", endpoint, "relay.internal.example", "2s"))
	}
	for _, serverName := range []string{"", "127.0.0.1", "*.internal.example", "invalid name"} {
		assertProbeFailure(t, probeArgs("https://relay.internal.example:7346", "", "health", serverName, "2s"))
	}
}

func TestProbeRejectsInvalidCAFiles(t *testing.T) {
	directory := t.TempDir()
	bad := filepath.Join(directory, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertProbeFailure(t, probeArgs("https://relay.internal.example:7346", bad, "health", "relay.internal.example", "2s"))
	assertProbeFailure(t, probeArgs("https://relay.internal.example:7346", directory, "health", "relay.internal.example", "2s"))
}

func TestProbeOutputIsFixedAndPrivacySafe(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	args := probeArgs("https://sensitive-hostname.invalid:7346", "sensitive-ca-path", "health", "sensitive-sni.invalid", "20ms")
	if code := run(args, stdout, stderr); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if stdout.Len() != 0 || stderr.String() != "relay probe failed\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	for _, sentinel := range []string{"sensitive-hostname", "sensitive-ca-path", "sensitive-sni"} {
		if strings.Contains(stdout.String()+stderr.String(), sentinel) {
			t.Fatalf("output leaked %q", sentinel)
		}
	}
}

func probeArgs(base, caFile, endpoint, serverName, timeout string) []string {
	return []string{"--base-url=" + base, "--ca-file=" + caFile, "--endpoint=" + endpoint, "--server-name=" + serverName, "--timeout=" + timeout}
}

func assertProbeFailure(t *testing.T, args []string) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := run(args, stdout, stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "relay probe failed\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func newTLSServer(t *testing.T, serverName string, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	pair, ca := certificatePair(t, serverName)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, ca, 0o600); err != nil {
		server.Close()
		t.Fatal(err)
	}
	return server, caFile
}

func certificatePair(t *testing.T, serverName string) (tls.Certificate, []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay probe test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: serverName}, DNSNames: []string{serverName},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if len(caPEM) == 0 {
		t.Fatal(fmt.Errorf("failed to encode CA"))
	}
	return certificate, caPEM
}
