package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
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
	h := (&controller.Server{Auth: a, Apps: apps.New(db), Jobs: jobs.New(db), Machines: m, FakeRuntime: true, DataRoot: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
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
	var createdApp apicontract.Application
	if err := json.Unmarshal(w.Body.Bytes(), &createdApp); err != nil || createdApp.ID == "" || createdApp.Name != "Fixture" {
		t.Fatalf("application contract response: %s (%v)", w.Body.String(), err)
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
	var csrfBody apicontract.CSRFResponse
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

	w = authenticatedRequest(t, h, session, http.MethodGet, "/api/v1/system/status", "")
	var status apicontract.SystemStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Daemon != "running" || !status.Capabilities.FakeRuntime {
		t.Fatalf("system status contract response: %s (%v)", w.Body.String(), err)
	}
	w = authenticatedRequest(t, h, session, http.MethodGet, "/api/v1/apps", "")
	var applicationList apicontract.ApplicationList
	if err := json.Unmarshal(w.Body.Bytes(), &applicationList); err != nil || len(applicationList.Items) != 2 {
		t.Fatalf("application list contract response: %s (%v)", w.Body.String(), err)
	}
	w = authenticatedRequest(t, h, session, http.MethodGet, "/api/v1/machines", "")
	var machineList apicontract.MachineList
	if err := json.Unmarshal(w.Body.Bytes(), &machineList); err != nil || len(machineList.Items) != 1 || machineList.Items[0].ID == "" {
		t.Fatalf("machine list contract response: %s (%v)", w.Body.String(), err)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/apps/"+createdApp.ID+"/deployments", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	r.Header.Set("X-CSRF-Token", csrfBody.CSRFToken)
	r.Header.Set("Idempotency-Key", "generated-contract")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var mutation apicontract.JobMutationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &mutation); err != nil || w.Code != http.StatusAccepted || !mutation.Created || mutation.Job.ID == "" {
		t.Fatalf("job mutation contract response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	w = authenticatedRequest(t, h, session, http.MethodGet, "/api/v1/jobs", "")
	var jobList apicontract.JobList
	if err := json.Unmarshal(w.Body.Bytes(), &jobList); err != nil || len(jobList.Items) != 1 {
		t.Fatalf("job list contract response: %s (%v)", w.Body.String(), err)
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

func TestCancelAPIAndLastEventIDReplay(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.New(db)
	token, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := authService.Bootstrap(token, "admin", "this is a secure passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(db)
	handler := (&controller.Server{Auth: authService, Apps: apps.New(db), Jobs: jobService, Machines: machineStore, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()

	job, _, err := jobService.Create("deploy", "application", "app-cancel", "cancel-api")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/jobs/" + job.ID + "/cancel"
	request := httptest.NewRequest(http.MethodPost, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated cancel = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, path, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cancel without CSRF = %d", response.Code)
	}
	response = authenticatedRequest(t, handler, session, http.MethodPost, path, "")
	if !strings.Contains(response.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel response: %s", response.Body.String())
	}
	var cancelled apicontract.JobResponse
	if err := json.Unmarshal(response.Body.Bytes(), &cancelled); err != nil || cancelled.Job.ID != job.ID || cancelled.Job.Status != string(jobs.Cancelled) {
		t.Fatalf("cancel contract response: %s (%v)", response.Body.String(), err)
	}
	request = httptest.NewRequest(http.MethodPost, path, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	request.Header.Set("X-CSRF-Token", session.CSRF)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("terminal cancel = %d %s", response.Code, response.Body.String())
	}

	replayJob, _, err := jobService.Create("deploy", "application", "app-replay", "replay-api")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := jobService.Events(replayJob.ID, 0)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial events: %#v %v", initial, err)
	}
	second, err := jobService.Append(replayJob.ID, "info", "validate", "phase_started", "Validation started")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+replayJob.ID+"/events/stream", nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	request.Header.Set("Last-Event-ID", fmt.Sprint(initial[0].ID))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), fmt.Sprintf("id: %d", second.ID)) || strings.Contains(response.Body.String(), fmt.Sprintf("id: %d\n", initial[0].ID)) {
		t.Fatalf("Last-Event-ID replay failed: %d %s", response.Code, response.Body.String())
	}
}

func TestBootstrapResponseUsesGeneratedContract(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.New(db)
	token, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	handler := (&controller.Server{Auth: authService, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", strings.NewReader(`{"token":"`+token+`","username":"admin","passphrase":"this is a secure passphrase"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var session apicontract.SessionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || response.Code != http.StatusCreated || session.User.ID == "" || session.User.Username != "admin" || session.CSRFToken == "" {
		t.Fatalf("bootstrap contract response: %d %s (%v)", response.Code, response.Body.String(), err)
	}
}
