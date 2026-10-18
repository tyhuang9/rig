package controller_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
)

func TestDesiredLifecycleRealActionsRemainUnavailableAndFakeActionsDoNotAuthorIntent(t *testing.T) {
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
	_, session, err := authService.Bootstrap(token, "lifecycle-admin", "this is a secure passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	appStore := apps.New(db)
	jobService := jobs.New(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	realHandler := (&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobService, Machines: machineStore,
		FakeRuntime: false, Logger: logger,
	}).Handler()
	fakeHandler := (&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobService, Machines: machineStore,
		FakeRuntime: true, Logger: logger,
	}).Handler()

	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			application, err := appStore.Create("Lifecycle "+action, "", "C:/apps/"+action, "")
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/apps/" + application.ID + "/" + action
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
			request.Header.Set("X-CSRF-Token", session.CSRF)
			blocked := httptest.NewRecorder()
			realHandler.ServeHTTP(blocked, request)
			if blocked.Code != http.StatusConflict {
				t.Fatalf("real %s status=%d body=%s", action, blocked.Code, blocked.Body.String())
			}
			var problem struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(blocked.Body.Bytes(), &problem); err != nil || problem.Code != "capability_unavailable" {
				t.Fatalf("real %s problem=%s error=%v", action, blocked.Body.String(), err)
			}
			assertDesiredLifecycleUnauthored(t, db, application.ID, action, 0)

			accepted := authenticatedRequest(t, fakeHandler, session, http.MethodPost, path, "")
			if accepted.Code != http.StatusAccepted {
				t.Fatalf("fake %s status=%d body=%s", action, accepted.Code, accepted.Body.String())
			}
			assertDesiredLifecycleUnauthored(t, db, application.ID, action, 1)
		})
	}
}

func assertDesiredLifecycleUnauthored(t *testing.T, db *sql.DB, appID, action string, wantJobs int) {
	t.Helper()
	var revision, transitions, actionJobs int
	var state sql.NullString
	if err := db.QueryRow(`SELECT desired_state,revision_number
		FROM application_desired_lifecycle_heads WHERE app_id=?`, appID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_transitions
		WHERE app_id=?`, appID).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs
		WHERE resource_type='application' AND resource_id=? AND type=?`, appID, action).Scan(&actionJobs); err != nil {
		t.Fatal(err)
	}
	if state.Valid || revision != 0 || transitions != 0 || actionJobs != wantJobs {
		t.Fatalf("action=%s state=%v revision=%d transitions=%d jobs=%d wantJobs=%d",
			action, state, revision, transitions, actionJobs, wantJobs)
	}
}
