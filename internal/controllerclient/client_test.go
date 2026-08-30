package controllerclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/auth"
)

func TestLoginAndMutationRefreshesCSRFOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/sessions":
			http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "session"})
			_, _ = w.Write([]byte(`{"csrfToken":"old"}`))
		case "/api/v1/apps/a%2Fb/start", "/api/v1/apps/a/b/start":
			if r.URL.EscapedPath() != "/api/v1/apps/a%2Fb/start" {
				t.Errorf("escaped path = %q", r.URL.EscapedPath())
			}
			if requests.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"csrf_failed","detail":"expired"}`))
				return
			}
			if r.Header.Get("X-CSRF-Token") != "new" {
				t.Errorf("csrf = %q", r.Header.Get("X-CSRF-Token"))
			}
			_, _ = w.Write([]byte(`{"created":true,"job":{"id":"job"}}`))
		case "/api/v1/auth/csrf":
			if r.Header.Get("Cookie") == "" {
				t.Error("missing cookie")
			}
			_, _ = w.Write([]byte(`{"csrfToken":"new"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := client.Login(context.Background(), apicontract.LoginRequest{Username: "u", Passphrase: "p"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Start(context.Background(), &session, "a/b", "key")
	if err != nil {
		t.Fatal(err)
	}
	if !response.Created || session.CSRFToken != "new" || requests.Load() != 2 {
		t.Fatalf("response=%+v session=%+v requests=%d", response, session, requests.Load())
	}
}

func TestProblemResponseIsBoundedAndTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"bad","detail":"invalid"}` + strings.Repeat("x", 2<<20)))
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	_, err := client.BootstrapStatus(context.Background())
	var problem *ProblemError
	if !errors.As(err, &problem) || problem.StatusCode != http.StatusBadRequest {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestSessionOriginBindingPreventsCredentialForwarding(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Status(context.Background(), Session{SessionToken: "session", CSRFToken: "csrf", ControllerOrigin: "http://127.0.0.1:7345"})
	if err == nil || !strings.Contains(err.Error(), "different endpoint") {
		t.Fatalf("origin mismatch error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("origin mismatch made %d request(s)", requests.Load())
	}
}

func TestLoginBindsSessionToControllerOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "session"})
		_, _ = w.Write([]byte(`{"csrfToken":"csrf"}`))
	}))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := client.Login(context.Background(), apicontract.LoginRequest{Username: "u", Passphrase: "p"})
	if err != nil || session.ControllerOrigin == "" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
}
