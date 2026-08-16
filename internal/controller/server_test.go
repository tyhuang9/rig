package controller_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
)

func TestAuthAndProtectedAPI(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := auth.New(db)
	token, _ := a.EnsureBootstrapToken()
	_, session, err := a.Bootstrap(token, "admin", "this is a secure passphrase")
	if err != nil {
		t.Fatal(err)
	}
	m := machines.New(db)
	if _, err := m.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	h := (&controller.Server{Auth: a, Apps: apps.New(db), Jobs: jobs.New(db), Machines: m, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/apps", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{"name":"Fixture"}`))
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", session.CSRF)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201 got %d: %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{"name":"Other"}`))
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("fresh-tab CSRF retrieval failed: %d %s", w.Code, w.Body.String())
	}
	var csrfBody struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &csrfBody); err != nil || csrfBody.CSRFToken == "" || csrfBody.CSRFToken == session.CSRF {
		t.Fatalf("unexpected rotated CSRF response: %s (%v)", w.Body.String(), err)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{"name":"Restored tab"}`))
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	r.Header.Set("X-CSRF-Token", csrfBody.CSRFToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("rotated CSRF rejected: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{
		"/api/v1/apps/missing/services",
		"/api/v1/apps/missing/logs/stream",
		"/api/v1/jobs/missing/events",
		"/api/v1/jobs/missing/events/stream",
	} {
		r = httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: want 404 got %d: %s", path, w.Code, w.Body.String())
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/apps/any/deep/link", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "root") {
		t.Fatalf("embedded SPA deep link failed: %d", w.Code)
	}
}
