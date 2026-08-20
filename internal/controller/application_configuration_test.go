package controller_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
)

func TestApplicationConfigurationAPIStoresMasksAndConflictsSafely(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.New(db)
	token, _ := authService.EnsureBootstrapToken()
	_, session, err := authService.Bootstrap(token, "admin", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machinesStore := machines.New(db)
	if _, err := machinesStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	app, err := apps.New(db).Create("Config fixture", "", "C:/fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	configurationStore, err := appconfig.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler := (&controller.Server{Auth: authService, Apps: apps.New(db), Jobs: jobs.New(db), Machines: machinesStore, Configuration: configurationStore, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}).Handler()

	initial := authenticatedRequest(t, handler, session, http.MethodGet, "/api/v1/apps/"+app.ID+"/configuration", "")
	if initial.Header().Get("Cache-Control") != "no-store" || !strings.Contains(initial.Body.String(), `"revisionNumber":0`) {
		t.Fatalf("initial=%d %s headers=%v", initial.Code, initial.Body.String(), initial.Header())
	}
	requestBody := `{"expectedRevisionNumber":0,"variables":[{"key":"EMPTY","value":""}],"secrets":[{"key":"TOKEN","value":"sentinel-api-secret"}],"remove":[]}`
	saved := authenticatedRequest(t, handler, session, http.MethodPut, "/api/v1/apps/"+app.ID+"/configuration", requestBody)
	if saved.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control=%q", saved.Header().Get("Cache-Control"))
	}
	var body struct {
		Entries        []map[string]any `json:"entries"`
		RevisionNumber int64            `json:"revisionNumber"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RevisionNumber != 1 {
		t.Fatalf("response=%s", saved.Body.String())
	}
	for _, entry := range body.Entries {
		switch entry["key"] {
		case "EMPTY":
			if value, present := entry["value"]; !present || value != "" {
				t.Fatalf("empty variable ambiguous: %#v", entry)
			}
		case "TOKEN":
			if _, present := entry["value"]; present {
				t.Fatalf("secret value field present: %#v", entry)
			}
		}
	}
	if strings.Contains(saved.Body.String(), "sentinel-api-secret") {
		t.Fatal("secret echoed by API")
	}

	conflictRequest := httptest.NewRequest(http.MethodPut, "/api/v1/apps/"+app.ID+"/configuration", strings.NewReader(requestBody))
	conflictRequest.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	conflictRequest.Header.Set("Content-Type", "application/json")
	conflictRequest.Header.Set("X-CSRF-Token", session.CSRF)
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, conflictRequest)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"code":"configuration_conflict"`) || strings.Contains(conflict.Body.String(), "sentinel-api-secret") {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	invalidBody := `{"expectedRevisionNumber":1,"variables":[{"key":"BAD-KEY","value":"sentinel-problem-visible"}],"secrets":[{"key":"TOKEN","value":"sentinel-problem-secret"}],"remove":[]}`
	invalidRequest := httptest.NewRequest(http.MethodPut, "/api/v1/apps/"+app.ID+"/configuration", strings.NewReader(invalidBody))
	invalidRequest.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest.Header.Set("X-CSRF-Token", session.CSRF)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusUnprocessableEntity || strings.Contains(invalid.Body.String(), "sentinel-problem-visible") || strings.Contains(invalid.Body.String(), "sentinel-problem-secret") {
		t.Fatalf("invalid problem=%d %s", invalid.Code, invalid.Body.String())
	}
	for _, sentinel := range []string{"sentinel-api-secret", "sentinel-problem-visible", "sentinel-problem-secret"} {
		if strings.Contains(logs.String(), sentinel) {
			t.Fatalf("configuration value found in controller logs: %s", sentinel)
		}
	}
}

func TestApplicationConfigurationAPIUsesStableSafeProblems(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.New(db)
	token, _ := authService.EnsureBootstrapToken()
	_, session, err := authService.Bootstrap(token, "admin", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	configurationStore, _ := appconfig.New(db, root)
	handler := (&controller.Server{Auth: authService, Apps: apps.New(db), Jobs: jobs.New(db), Machines: machines.New(db), Configuration: configurationStore, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/apps/missing/configuration", strings.NewReader(`{"expectedRevisionNumber":0,"variables":[{"key":"BAD-KEY","value":"do-not-echo"}],"secrets":[],"remove":[]}`))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", session.CSRF)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"app_not_found"`) || strings.Contains(response.Body.String(), "do-not-echo") {
		t.Fatalf("problem=%d %s", response.Code, response.Body.String())
	}
}
