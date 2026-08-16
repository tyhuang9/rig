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

func TestBrowserContractUsesLowerCamelJSON(t *testing.T) {
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
	user, session, err := authService.Bootstrap(token, "admin", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(db)
	handler := (&controller.Server{Auth: authService, Apps: apps.New(db), Jobs: jobService, Machines: machineStore, FakeRuntime: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()

	me := authenticatedRequest(t, handler, session, http.MethodGet, "/api/v1/auth/me", "")
	var meBody map[string]map[string]any
	if err := json.Unmarshal(me.Body.Bytes(), &meBody); err != nil {
		t.Fatal(err)
	}
	if meBody["user"]["id"] != user.ID || meBody["user"]["username"] != "admin" || meBody["user"]["role"] != "administrator" {
		t.Fatalf("unexpected user JSON: %s", me.Body.String())
	}
	if _, present := meBody["user"]["ID"]; present {
		t.Fatalf("upper-case DTO key leaked: %s", me.Body.String())
	}

	created := authenticatedRequest(t, handler, session, http.MethodPost, "/api/v1/apps", `{"name":"Fixture","sourcePath":"C:/fixture"}`)
	var appBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &appBody); err != nil {
		t.Fatal(err)
	}
	appID, ok := appBody["id"].(string)
	if !ok || appBody["name"] != "Fixture" {
		t.Fatalf("unexpected application JSON: %s", created.Body.String())
	}

	deployed := authenticatedRequest(t, handler, session, http.MethodPost, "/api/v1/apps/"+appID+"/deployments", "{}")
	var jobBody struct {
		Job map[string]any `json:"job"`
	}
	if err := json.Unmarshal(deployed.Body.Bytes(), &jobBody); err != nil {
		t.Fatal(err)
	}
	if jobBody.Job["resourceId"] != appID || jobBody.Job["progress"] != float64(0) {
		t.Fatalf("unexpected job JSON: %s", deployed.Body.String())
	}
	if _, present := jobBody.Job["ID"]; present {
		t.Fatalf("upper-case job key leaked: %s", deployed.Body.String())
	}
}

func authenticatedRequest(t *testing.T, handler http.Handler, session auth.Session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	request.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", session.CSRF)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	return response
}
