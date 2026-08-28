package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/tui"
)

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
