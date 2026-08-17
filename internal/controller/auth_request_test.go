package controller

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/auth"
)

type countingAuthenticationService struct {
	bootstrapCalls int
	loginCalls     int
}

func (s *countingAuthenticationService) BootstrapStatus() (bool, error) { return true, nil }
func (s *countingAuthenticationService) Bootstrap(string, string, string) (auth.User, auth.Session, error) {
	s.bootstrapCalls++
	return auth.User{}, auth.Session{}, errors.New("not configured")
}
func (s *countingAuthenticationService) Login(string, string) (auth.User, auth.Session, error) {
	s.loginCalls++
	return auth.User{}, auth.Session{}, errors.New("invalid credentials")
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
