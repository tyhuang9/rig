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

	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/tui"
)

type memorySessionStore struct {
	value   []byte
	cleared bool
	saveErr error
}

func (s *memorySessionStore) Load(context.Context) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}
func (s *memorySessionStore) Save(_ context.Context, value []byte) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.value = append([]byte(nil), value...)
	return nil
}
func (s *memorySessionStore) Clear(context.Context) error {
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
