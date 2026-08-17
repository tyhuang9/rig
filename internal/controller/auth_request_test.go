package controller

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/auth"
)

type countingAuthenticationService struct {
	bootstrapCalls   int
	loginCalls       int
	bootstrapUser    auth.User
	bootstrapSession auth.Session
	bootstrapErr     error
	loginUser        auth.User
	loginSession     auth.Session
	loginErr         error
}

func (s *countingAuthenticationService) BootstrapStatus() (bool, error) { return true, nil }
func (s *countingAuthenticationService) Bootstrap(string, string, string) (auth.User, auth.Session, error) {
	s.bootstrapCalls++
	return s.bootstrapUser, s.bootstrapSession, s.bootstrapErr
}
func (s *countingAuthenticationService) Login(string, string) (auth.User, auth.Session, error) {
	s.loginCalls++
	return s.loginUser, s.loginSession, s.loginErr
}
func (s *countingAuthenticationService) Authenticate(string) (auth.User, string, error) {
	return auth.User{}, "", errors.New("unauthenticated")
}
func (s *countingAuthenticationService) CheckCSRF(string, string) bool { return false }
func (s *countingAuthenticationService) RotateCSRF(string) (string, error) {
	return "", errors.New("unauthenticated")
}
func (s *countingAuthenticationService) Logout(string) error { return nil }

func TestAuthenticationRequestsAreRejectedBeforeServiceWork(t *testing.T) {
	authentication := &countingAuthenticationService{}
	handler := (&Server{Auth: authentication, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		status      int
	}{
		{name: "bootstrap text plain", path: "/api/v1/auth/bootstrap", contentType: "text/plain", body: `{"token":"token","username":"admin","passphrase":"this is a secure passphrase"}`, status: http.StatusUnsupportedMediaType},
		{name: "login missing content type", path: "/api/v1/auth/sessions", body: `{"username":"admin","passphrase":"this is a secure passphrase"}`, status: http.StatusUnsupportedMediaType},
		{name: "bootstrap malformed", path: "/api/v1/auth/bootstrap", contentType: "application/json", body: `{`, status: http.StatusBadRequest},
		{name: "login oversized body", path: "/api/v1/auth/sessions", contentType: "application/json", body: strings.Repeat("x", maxAuthRequestBodyBytes+1), status: http.StatusRequestEntityTooLarge},
		{name: "bootstrap token too long", path: "/api/v1/auth/bootstrap", contentType: "application/json", body: `{"token":"` + strings.Repeat("t", maxAuthTokenBytes+1) + `","username":"admin","passphrase":"this is a secure passphrase"}`, status: http.StatusBadRequest},
		{name: "login username too long", path: "/api/v1/auth/sessions", contentType: "application/json", body: `{"username":"` + strings.Repeat("u", maxAuthUsernameBytes+1) + `","passphrase":"this is a secure passphrase"}`, status: http.StatusBadRequest},
		{name: "login passphrase too long", path: "/api/v1/auth/sessions", contentType: "application/json", body: `{"username":"admin","passphrase":"` + strings.Repeat("p", maxAuthPassphraseBytes+1) + `"}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("authentication failure relaxed CORS")
			}
		})
	}
	if authentication.bootstrapCalls != 0 || authentication.loginCalls != 0 {
		t.Fatalf("rejected requests reached authentication service: bootstrap=%d login=%d", authentication.bootstrapCalls, authentication.loginCalls)
	}
}

func TestAuthenticationWorkRateLimitIsSharedPerClient(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	authentication := &countingAuthenticationService{
		bootstrapErr: errors.New("not configured"),
		loginErr:     errors.New("invalid credentials"),
	}
	handler := (&Server{
		Auth:               authentication,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		authenticationWork: newAuthenticationWorkGate(func() time.Time { return now }, 2, 2, time.Minute, 2),
	}).Handler()

	for _, path := range []string{"/api/v1/auth/sessions", "/api/v1/auth/bootstrap"} {
		response := serveAuthenticationRequest(handler, path, "198.51.100.4:4321")
		if response.Code == http.StatusTooManyRequests {
			t.Fatalf("request %s was rate limited too early: %s", path, response.Body.String())
		}
	}
	response := serveAuthenticationRequest(handler, "/api/v1/auth/sessions", "198.51.100.4:4321")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited status = %d, want %d: %s", response.Code, http.StatusTooManyRequests, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "60" || !strings.Contains(response.Body.String(), `"code":"auth_rate_limited"`) {
		t.Fatalf("rate limit response = headers=%v body=%s", response.Header(), response.Body.String())
	}
	if authentication.bootstrapCalls != 1 || authentication.loginCalls != 1 {
		t.Fatalf("rate limited request reached service: bootstrap=%d login=%d", authentication.bootstrapCalls, authentication.loginCalls)
	}

	response = serveAuthenticationRequest(handler, "/api/v1/auth/sessions", "198.51.100.5:4321")
	if response.Code != http.StatusUnauthorized || authentication.loginCalls != 2 {
		t.Fatalf("different client was unexpectedly limited: status=%d login=%d", response.Code, authentication.loginCalls)
	}
	now = now.Add(time.Minute)
	response = serveAuthenticationRequest(handler, "/api/v1/auth/sessions", "198.51.100.4:4321")
	if response.Code != http.StatusUnauthorized || authentication.loginCalls != 3 {
		t.Fatalf("expired rate window did not reset: status=%d login=%d", response.Code, authentication.loginCalls)
	}
}

func TestAuthenticationRateLimiterBoundsClientState(t *testing.T) {
	gate := newAuthenticationWorkGate(time.Now, 1, 10, time.Minute, 2)
	for _, remoteAddr := range []string{"198.51.100.1:1", "198.51.100.2:1", "198.51.100.3:1"} {
		if allowed, _ := gate.admit(remoteAddr); !allowed {
			t.Fatalf("first request from %s was rejected", remoteAddr)
		}
		gate.release()
	}
	if len(gate.rates.entries) > 2 {
		t.Fatalf("rate limiter retained %d client entries, want at most 2", len(gate.rates.entries))
	}
}

func TestAuthenticationWorkConcurrencyLimitIsSharedAcrossEndpoints(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	authentication := &blockingAuthenticationService{
		started: started,
		release: release,
	}
	handler := (&Server{
		Auth:               authentication,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		authenticationWork: newAuthenticationWorkGate(time.Now, 1, 10, time.Minute, 10),
	}).Handler()

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- serveAuthenticationRequest(handler, "/api/v1/auth/bootstrap", "198.51.100.10:4321")
	}()
	<-started
	response := serveAuthenticationRequest(handler, "/api/v1/auth/sessions", "198.51.100.11:4321")
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("concurrency overflow = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if authentication.loginCalls != 0 {
		t.Fatalf("concurrency overflow reached login: %d calls", authentication.loginCalls)
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusBadRequest {
		t.Fatalf("blocked bootstrap = %d: %s", first.Code, first.Body.String())
	}
}

func TestAuthenticationWorkGateAllowsNormalBootstrapAndLogin(t *testing.T) {
	session := auth.Session{Token: "session-token", CSRF: "csrf-token", ExpiresAt: time.Now().Add(time.Hour)}
	authentication := &countingAuthenticationService{
		bootstrapUser:    auth.User{ID: "bootstrap-user", Username: "admin", Role: "administrator"},
		bootstrapSession: session,
		loginUser:        auth.User{ID: "login-user", Username: "admin", Role: "administrator"},
		loginSession:     session,
	}
	handler := (&Server{
		Auth:               authentication,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		authenticationWork: newAuthenticationWorkGate(time.Now, 1, 2, time.Minute, 2),
	}).Handler()

	for _, path := range []string{"/api/v1/auth/bootstrap", "/api/v1/auth/sessions"} {
		response := serveAuthenticationRequest(handler, path, "198.51.100.20:4321")
		if response.Code != http.StatusCreated {
			t.Fatalf("normal %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
	if authentication.bootstrapCalls != 1 || authentication.loginCalls != 1 {
		t.Fatalf("normal requests did not reach service: bootstrap=%d login=%d", authentication.bootstrapCalls, authentication.loginCalls)
	}
}

type blockingAuthenticationService struct {
	countingAuthenticationService
	started chan<- struct{}
	release <-chan struct{}
}

func (s *blockingAuthenticationService) Bootstrap(string, string, string) (auth.User, auth.Session, error) {
	s.bootstrapCalls++
	s.started <- struct{}{}
	<-s.release
	return auth.User{}, auth.Session{}, errors.New("not configured")
}

func serveAuthenticationRequest(handler http.Handler, path, remoteAddr string) *httptest.ResponseRecorder {
	body := `{"username":"admin","passphrase":"this is a secure passphrase"}`
	if path == "/api/v1/auth/bootstrap" {
		body = `{"token":"bootstrap-token","username":"admin","passphrase":"this is a secure passphrase"}`
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
