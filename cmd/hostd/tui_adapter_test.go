package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/tui"
)

type memorySessionStore struct {
	value        []byte
	cleared      bool
	saveErr      error
	clearStarted chan struct{}
	releaseClear <-chan struct{}
}

func (s *memorySessionStore) Load(context.Context) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}
func (s *memorySessionStore) Save(_ context.Context, value []byte) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.value = append([]byte(nil), value...)
	s.cleared = false
	return nil
}
func (s *memorySessionStore) Clear(context.Context) error {
	if s.clearStarted != nil {
		close(s.clearStarted)
	}
	if s.releaseClear != nil {
		<-s.releaseClear
	}
	s.value = nil
	s.cleared = true
	return nil
}

func TestProtectedSessionStoreRoundTripAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := newProtectedSessionStore(path)
	want := []byte(`{"sessionToken":"session-secret","csrfToken":"csrf-secret"}`)
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "session-secret") || strings.Contains(string(raw), "csrf-secret") {
			t.Fatal("protected session contains plaintext credentials")
		}
	} else {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("session file mode = %v, want a regular file", info.Mode())
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("session file permissions = %04o, want 0600", info.Mode().Perm())
		}
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("session = %q, want %q", got, want)
	}
	if err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Load(context.Background()); err != nil || value != nil {
		t.Fatalf("cleared store = %q, %v", value, err)
	}
}

func TestMapTUIErrorPreservesHTTPProblem(t *testing.T) {
	input := &controllerclient.ProblemError{StatusCode: 401, Status: "401 Unauthorized"}
	input.Problem.Code = "unauthenticated"
	input.Problem.Detail = "Authentication required"
	err := mapTUIError(input)
	var output *tui.HTTPError
	if !errors.As(err, &output) || output.Status != 401 || output.Code != "unauthenticated" || output.Detail != "Authentication required" {
		t.Fatalf("mapped error = %#v", err)
	}
}

func TestCancelAndResumePropagateSessionSaveFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"job":{"id":"job","status":"queued"}}`))
	}))
	defer server.Close()
	stored, err := json.Marshal(controllerclient.Session{SessionToken: "session", CSRFToken: "csrf"})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"cancel", "resume"} {
		t.Run(operation, func(t *testing.T) {
			client, err := newTUIControllerClient(server.URL, &memorySessionStore{value: stored, saveErr: errors.New("disk full")})
			if err != nil {
				t.Fatal(err)
			}
			var callErr error
			if operation == "cancel" {
				_, callErr = client.CancelJob(context.Background(), "job", "key")
			} else {
				_, callErr = client.ResumeJob(context.Background(), "job", "key")
			}
			if callErr == nil || !strings.Contains(callErr.Error(), "disk full") {
				t.Fatalf("save error = %v", callErr)
			}
		})
	}
}

func TestTUIControllerEndpointRejectsCredentialForwardingTargets(t *testing.T) {
	for _, endpoint := range []string{
		"http://localhost:7345",
		"http://192.0.2.1:7345",
		"ftp://127.0.0.1:7345",
		"http://user:pass@127.0.0.1:7345",
		"http://127.0.0.1:7345/api",
		"http://127.0.0.1:7345?forward=1",
		"http://127.0.0.1:7345#fragment",
		"http://127.0.0.1",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := newTUIControllerClient(endpoint, &memorySessionStore{}); err == nil {
				t.Fatalf("unsafe endpoint %q was accepted", endpoint)
			}
		})
	}
	for _, endpoint := range []string{"http://127.0.0.1:7345", "https://[::1]:7345"} {
		if _, err := newTUIControllerClient(endpoint, &memorySessionStore{}); err != nil {
			t.Fatalf("safe endpoint %q rejected: %v", endpoint, err)
		}
	}
}

func TestTUIRefusesSessionBoundToDifferentOriginBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	stored, err := json.Marshal(controllerclient.Session{SessionToken: "session", CSRFToken: "csrf", ControllerOrigin: "http://127.0.0.1:7345"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := newTUIControllerClient(server.URL, &memorySessionStore{value: stored})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("origin mismatch was accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("origin mismatch made %d request(s)", requests.Load())
	}
}

func TestTUIInvalidPersistedSessionDoesNotPoisonCachedState(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	valid, err := json.Marshal(controllerclient.Session{
		SessionToken:     "correct-session",
		CSRFToken:        "correct-csrf",
		ControllerOrigin: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		invalid []byte
	}{
		{name: "malformed", invalid: []byte(`{"sessionToken":`)},
		{name: "different origin", invalid: []byte(`{"sessionToken":"stale","csrfToken":"stale","controllerOrigin":"http://127.0.0.1:7345"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memorySessionStore{value: test.invalid}
			client, err := newTUIControllerClient(server.URL, store)
			if err != nil {
				t.Fatal(err)
			}
			adapter := client.(*tuiControllerClient)
			if _, err := adapter.current(context.Background()); err == nil {
				t.Fatal("invalid persisted session was accepted")
			}

			store.value = append([]byte(nil), valid...)
			got, err := adapter.current(context.Background())
			if err != nil {
				t.Fatalf("corrected persisted session failed to load: %v", err)
			}
			if got.SessionToken != "correct-session" || got.CSRFToken != "correct-csrf" {
				t.Fatalf("session = %#v, want corrected persisted session", got)
			}
		})
	}
}

func TestTUILogoutClearsExpiredProtectedSessionLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthenticated","detail":"expired"}`))
	}))
	defer server.Close()
	stored, err := json.Marshal(controllerclient.Session{SessionToken: "session", CSRFToken: "csrf"})
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySessionStore{value: stored}
	client, err := newTUIControllerClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Logout(context.Background()); err == nil {
		t.Fatal("remote expiration was unexpectedly successful")
	}
	if !store.cleared || len(store.value) != 0 {
		t.Fatal("expired session remained in protected storage")
	}
}

func TestTUIFollowMapsUnauthorizedAndConditionallyClearsSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthenticated","detail":"expired"}`))
	}))
	defer server.Close()
	stored, err := json.Marshal(controllerclient.Session{SessionToken: "session-a", CSRFToken: "csrf-a", ControllerOrigin: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySessionStore{value: stored}
	client, err := newTUIControllerClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, failures := client.FollowJob(ctx, "job-a", 0)
	var streamErr error
	select {
	case streamErr = <-failures:
	case _, ok := <-events:
		t.Fatalf("event channel closed before mapped authentication failure (open=%v)", ok)
	case <-ctx.Done():
		t.Fatal("timed out waiting for stream failure")
	}
	var httpErr *tui.HTTPError
	if !errors.As(streamErr, &httpErr) || httpErr.Status != http.StatusUnauthorized || httpErr.SessionGeneration == 0 {
		t.Fatalf("stream error = %#v", streamErr)
	}
	if err := client.ClearSession(context.Background(), httpErr.SessionGeneration); err != nil {
		t.Fatal(err)
	}
	if !store.cleared || len(store.value) != 0 {
		t.Fatal("expired stream session remained in protected storage")
	}
}

func TestDelayedLogoutAndExpiryCleanupCannotEraseNewLogin(t *testing.T) {
	logoutStarted := make(chan struct{})
	releaseLogout := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/auth/sessions/current":
			close(logoutStarted)
			<-releaseLogout
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/sessions":
			http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "session-b"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"csrfToken":"csrf-b","user":{"id":"user-b","username":"new-admin"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stored, err := json.Marshal(controllerclient.Session{SessionToken: "session-a", CSRFToken: "csrf-a", ControllerOrigin: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySessionStore{value: stored}
	client, err := newTUIControllerClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	adapter := client.(*tuiControllerClient)
	old, err := adapter.currentSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	logoutDone := make(chan error, 1)
	go func() { logoutDone <- client.Logout(context.Background()) }()
	select {
	case <-logoutStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("logout did not start")
	}
	if _, err := client.Login(context.Background(), apicontract.LoginRequest{Username: "new-admin", Passphrase: "new-passphrase"}); err != nil {
		t.Fatal(err)
	}
	close(releaseLogout)
	if err := <-logoutDone; err != nil {
		t.Fatal(err)
	}
	if err := client.ClearSession(context.Background(), old.generation); err != nil {
		t.Fatal(err)
	}
	stale := old.session
	stale.CSRFToken = "stale-refreshed-csrf"
	if err := adapter.saveIfCurrent(context.Background(), old.generation, stale); err != nil {
		t.Fatal(err)
	}
	current, err := adapter.current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.SessionToken != "session-b" || current.CSRFToken != "csrf-b" || store.cleared {
		t.Fatalf("new session was erased: session=%#v cleared=%v", current, store.cleared)
	}
	var persisted controllerclient.Session
	if err := json.Unmarshal(store.value, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SessionToken != "session-b" || persisted.CSRFToken != "csrf-b" {
		t.Fatalf("persisted session = %#v", persisted)
	}
}

func TestDelayedExpiryClearSerializesWithNewLogin(t *testing.T) {
	loginStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/sessions" {
			http.NotFound(w, r)
			return
		}
		close(loginStarted)
		http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "session-b"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"csrfToken":"csrf-b","user":{"id":"user-b","username":"new-admin"}}`))
	}))
	defer server.Close()
	stored, err := json.Marshal(controllerclient.Session{SessionToken: "session-a", CSRFToken: "csrf-a", ControllerOrigin: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	clearStarted := make(chan struct{})
	releaseClear := make(chan struct{})
	store := &memorySessionStore{value: stored, clearStarted: clearStarted, releaseClear: releaseClear}
	client, err := newTUIControllerClient(server.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	adapter := client.(*tuiControllerClient)
	old, err := adapter.currentSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clearDone := make(chan error, 1)
	go func() { clearDone <- client.ClearSession(context.Background(), old.generation) }()
	select {
	case <-clearStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("local session clear did not start")
	}
	loginDone := make(chan error, 1)
	go func() {
		_, loginErr := client.Login(context.Background(), apicontract.LoginRequest{Username: "new-admin", Passphrase: "new-passphrase"})
		loginDone <- loginErr
	}()
	select {
	case <-loginStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("new login did not start while clear was delayed")
	}
	close(releaseClear)
	if err := <-clearDone; err != nil {
		t.Fatal(err)
	}
	if err := <-loginDone; err != nil {
		t.Fatal(err)
	}
	current, err := adapter.current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.SessionToken != "session-b" || current.CSRFToken != "csrf-b" || store.cleared {
		t.Fatalf("new login did not replace cleared session: session=%#v cleared=%v", current, store.cleared)
	}
}
