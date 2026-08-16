package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/auth"
)

func TestLoginSessionFileAndCancelCommand(t *testing.T) {
	const sessionToken = "session-secret-value"
	const csrfToken = "csrf-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/sessions":
			var credentials map[string]string
			if err := json.NewDecoder(request.Body).Decode(&credentials); err != nil || credentials["username"] != "admin" || credentials["passphrase"] != "a safe passphrase" {
				t.Errorf("unexpected login credentials: %#v %v", credentials, err)
			}
			http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: sessionToken, HttpOnly: true})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"csrfToken":"` + csrfToken + `"}`))
		case "/api/v1/jobs/job-one/cancel":
			cookie, err := request.Cookie(auth.SessionCookie)
			if err != nil || cookie.Value != sessionToken || request.Header.Get("X-CSRF-Token") != csrfToken || request.Method != http.MethodPost {
				t.Errorf("cancel authentication failed: cookie=%#v err=%v csrf=%q method=%s", cookie, err, request.Header.Get("X-CSRF-Token"), request.Method)
			}
			_, _ = w.Write([]byte(`{"job":{"id":"job-one","status":"cancelled"}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	sessionFile := filepath.Join(t.TempDir(), "config", "session.json")
	var output bytes.Buffer
	err := execute([]string{"--endpoint", server.URL, "--session-file", sessionFile, "login", "--credentials-stdin"}, strings.NewReader(`{"username":"admin","passphrase":"a safe passphrase"}`), &output, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("session mode = %o", info.Mode().Perm())
	}
	output.Reset()
	if err := execute([]string{"--endpoint", server.URL, "--session-file", sessionFile, "jobs", "cancel", "job-one"}, strings.NewReader(""), &output, server.Client()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status": "cancelled"`) {
		t.Fatalf("cancel output: %s", output.String())
	}
}

func TestSessionStdinAndOperationalFailuresReturnErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/system/status" {
			http.NotFound(w, request)
			return
		}
		if _, err := request.Cookie(auth.SessionCookie); err != nil {
			t.Error("session cookie missing")
		}
		_, _ = w.Write([]byte(`{"daemon":"running"}`))
	}))
	defer server.Close()
	var output bytes.Buffer
	stdin := strings.NewReader(`{"sessionToken":"session","csrfToken":"csrf"}`)
	if err := execute([]string{"--endpoint", server.URL, "--session-stdin", "status"}, stdin, &output, server.Client()); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"--endpoint", "http://127.0.0.1:1", "--session-file", filepath.Join(t.TempDir(), "missing"), "status"}, strings.NewReader(""), &output, server.Client()); err == nil {
		t.Fatal("missing session file returned success")
	}
	if err := execute([]string{"--session-token", "must-not-be-supported", "status"}, strings.NewReader(""), &output, server.Client()); err == nil {
		t.Fatal("session credential argv flag was accepted")
	}
}
