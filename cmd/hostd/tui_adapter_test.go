package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/tui"
)

type memorySessionStore struct {
	value   []byte
	cleared bool
}

func (s *memorySessionStore) Load(context.Context) ([]byte, error) {
	return append([]byte(nil), s.value...), nil
}
func (s *memorySessionStore) Save(_ context.Context, value []byte) error {
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
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == string(want) {
		t.Fatal("protected session was written as plaintext")
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
