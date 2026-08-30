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
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/secretfile"
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

func TestCancelPersistsRotatedCSRFOnlyAfterSuccess(t *testing.T) {
	const sessionToken = "session-secret-value"
	var cancelCalls int
	var failMutation atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/sessions":
			http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: sessionToken, HttpOnly: true})
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"csrfToken":"old-csrf"}`))
		case "/api/v1/jobs/job-one/cancel":
			cancelCalls++
			if failMutation.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"type":"about:blank","title":"failure","status":500,"detail":"failed"}`))
				return
			}
			if request.Header.Get("X-CSRF-Token") == "old-csrf" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"type":"https://hostd.dev/problems/csrf","title":"Forbidden","status":403,"code":"csrf_failed","detail":"expired"}`))
				return
			}
			if request.Header.Get("X-CSRF-Token") != "new-csrf" {
				t.Fatalf("retry csrf = %q", request.Header.Get("X-CSRF-Token"))
			}
			_, _ = w.Write([]byte(`{"job":{"id":"job-one","status":"cancelled"}}`))
		case "/api/v1/auth/csrf":
			_, _ = w.Write([]byte(`{"csrfToken":"new-csrf"}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	sessionFile := filepath.Join(t.TempDir(), "session.json")
	var output bytes.Buffer
	if err := execute([]string{"--endpoint", server.URL, "--session-file", sessionFile, "login", "--credentials-stdin"}, strings.NewReader(`{"username":"admin","passphrase":"safe"}`), &output, server.Client()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := execute([]string{"--endpoint", server.URL, "--session-file", sessionFile, "jobs", "cancel", "job-one"}, strings.NewReader(""), &output, server.Client()); err != nil {
		t.Fatal(err)
	}
	if cancelCalls != 2 {
		t.Fatalf("cancel calls = %d, want CSRF retry", cancelCalls)
	}
	updated, err := controllerclient.ReadSessionFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CSRFToken != "new-csrf" {
		t.Fatalf("persisted csrf = %q, want new-csrf", updated.CSRFToken)
	}

	rotated, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(original, rotated) {
		t.Fatal("successful CSRF rotation did not update protected session")
	}

	failedBefore, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	failMutation.Store(true)
	if err := execute([]string{"--endpoint", server.URL, "--session-file", sessionFile, "jobs", "cancel", "job-one"}, strings.NewReader(""), &output, server.Client()); err == nil {
		t.Fatal("failed mutation returned success")
	}
	failedAfter, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(failedBefore, failedAfter) {
		t.Fatal("failed mutation rewrote protected session")
	}
}

func TestBootstrapTokenReadsProtectedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-token.secret")
	if err := secretfile.Write(path, auth.BootstrapSecretPurpose, []byte("one-time-token")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := execute([]string{"bootstrap-token", "--file", path}, strings.NewReader(""), &output, http.DefaultClient); err != nil {
		t.Fatal(err)
	}
	if output.String() != "one-time-token\n" {
		t.Fatalf("bootstrap token output = %q", output.String())
	}
	output.Reset()
	if err := os.WriteFile(path, []byte("plaintext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"bootstrap-token", "--file", path}, strings.NewReader(""), &output, http.DefaultClient); err == nil {
		t.Fatal("plaintext bootstrap token file was accepted")
	} else if strings.Contains(err.Error(), "one-time-token") || output.Len() != 0 {
		t.Fatalf("bootstrap token leaked on retrieval failure: output=%q error=%q", output.String(), err)
	}
	if err := execute([]string{"bootstrap-token"}, strings.NewReader(""), &output, http.DefaultClient); err == nil {
		t.Fatal("bootstrap token command accepted a missing file")
	}
}
