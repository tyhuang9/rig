package controller

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/machines"
)

type autoDeployServiceFake struct {
	status       autodeploy.Status
	getErr       error
	configureErr error
	resumeErr    error
	getCalls     int
	configures   []autodeploy.ConfigureRequest
	resumes      []autoDeployResumeCall
}

type autoDeployResumeCall struct {
	applicationID string
	actorUserID   string
	revision      uint64
}

func (fake *autoDeployServiceFake) Get(_ context.Context, applicationID string) (autodeploy.Status, error) {
	fake.getCalls++
	value := fake.status
	value.ApplicationID = applicationID
	return value, fake.getErr
}

func (fake *autoDeployServiceFake) Configure(_ context.Context, request autodeploy.ConfigureRequest, _ time.Time) (autodeploy.Status, error) {
	fake.configures = append(fake.configures, request)
	value := fake.status
	value.ApplicationID = request.ApplicationID
	value.Enabled = request.Enabled
	return value, fake.configureErr
}

func (fake *autoDeployServiceFake) Resume(_ context.Context, applicationID, actorUserID string, revision uint64, _ time.Time) (autodeploy.Status, error) {
	fake.resumes = append(fake.resumes, autoDeployResumeCall{applicationID: applicationID, actorUserID: actorUserID, revision: revision})
	value := fake.status
	value.ApplicationID = applicationID
	return value, fake.resumeErr
}

func TestAutoDeployRoutesRequireAdministratorAndPrecheckApplication(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID)}
	operator := (&Server{Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "operator"}}, Apps: store, AutoDeploy: fake, AutoDeployAvailable: true, Logger: relayTestLogger()}).Handler()
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/apps/" + application.ID + "/auto-deploy", ""},
		{http.MethodPut, "/api/v1/apps/" + application.ID + "/auto-deploy", `{"expectedRevision":0,"enabled":false}`},
		{http.MethodPost, "/api/v1/apps/" + application.ID + "/auto-deploy/resume", `{"expectedRevision":0}`},
	} {
		response := relayAuthenticatedRequest(operator, request.method, request.path, request.body)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"auto_deploy_forbidden"`) {
			t.Errorf("operator %s=%d %s", request.path, response.Code, response.Body.String())
		}
	}
	if fake.getCalls != 0 || len(fake.configures) != 0 || len(fake.resumes) != 0 {
		t.Fatalf("forbidden calls get=%d configure=%d resume=%d", fake.getCalls, len(fake.configures), len(fake.resumes))
	}

	admin := (&Server{Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}}, Apps: store, AutoDeploy: fake, AutoDeployAvailable: true, Logger: relayTestLogger()}).Handler()
	notFound := relayAuthenticatedRequest(admin, http.MethodGet, "/api/v1/apps/"+uuid.NewString()+"/auto-deploy", "")
	if notFound.Code != http.StatusNotFound || !strings.Contains(notFound.Body.String(), `"code":"app_not_found"`) || fake.getCalls != 0 {
		t.Fatalf("missing app=%d %s service_calls=%d", notFound.Code, notFound.Body.String(), fake.getCalls)
	}
	invalid := relayAuthenticatedRequest(admin, http.MethodGet, "/api/v1/apps/not-a-uuid/auto-deploy", "")
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), `"code":"invalid_auto_deploy_request"`) {
		t.Fatalf("invalid app=%d %s", invalid.Code, invalid.Body.String())
	}
}

func TestAutoDeployGETCuratesStatusAndApplicationSource(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	status := autoDeployTestStatus(application.ID)
	status.SourceOwnerUserID = "credential-secret"
	status.ConfiguredByUserID = "internal-owner"
	status.ControllerID = "private-controller"
	status.BindingID = "private-binding"
	status.SubscriptionID = "private-subscription"
	fake := &autoDeployServiceFake{status: status}
	handler := autoDeployAdminHandler(store, fake, true, nil, nil)
	response := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+application.ID+"/auto-deploy", "")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"applicationId":"`+application.ID+`"`) || !strings.Contains(body, `"source":{"type":"local"`) {
		t.Fatalf("auto-deploy GET=%d %s", response.Code, body)
	}
	for _, forbidden := range []string{"credential-secret", "internal-owner", "private-controller", "private-binding", "private-subscription", "sourceOwnerUserId", "configuredByUserId", "controllerId", "bindingId", "subscriptionId"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("GET leaked %q: %s", forbidden, body)
		}
	}

	fake.getErr = autodeploy.ErrNotFound
	response = relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+application.ID+"/auto-deploy", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"auto_deploy_prerequisite_missing"`) {
		t.Fatalf("missing prerequisite=%d %s", response.Code, response.Body.String())
	}
}

func TestAutoDeployConfigureCapabilityValidationAndCallbacks(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID)}
	handler := autoDeployAdminHandler(store, fake, false, nil, nil)
	unknown := relayAuthenticatedRequest(handler, http.MethodPut, "/api/v1/apps/"+application.ID+"/auto-deploy", `{"expectedRevision":0,"enabled":true,"owner":"attacker"}`)
	if unknown.Code != http.StatusUnprocessableEntity || len(fake.configures) != 0 {
		t.Fatalf("unknown field=%d %s calls=%d", unknown.Code, unknown.Body.String(), len(fake.configures))
	}
	enabling := relayAuthenticatedRequest(handler, http.MethodPut, "/api/v1/apps/"+application.ID+"/auto-deploy", `{"expectedRevision":0,"enabled":true}`)
	if enabling.Code != http.StatusConflict || !strings.Contains(enabling.Body.String(), `"code":"capability_unavailable"`) || len(fake.configures) != 0 {
		t.Fatalf("disabled capability=%d %s calls=%d", enabling.Code, enabling.Body.String(), len(fake.configures))
	}

	callbacks := make([]string, 0, 2)
	handler = autoDeployAdminHandler(store, fake, false, func() { callbacks = append(callbacks, "relay") }, func() { callbacks = append(callbacks, "auto") })
	disabling := relayAuthenticatedRequest(handler, http.MethodPut, "/api/v1/apps/"+application.ID+"/auto-deploy", `{"expectedRevision":7,"enabled":false}`)
	if disabling.Code != http.StatusOK || len(fake.configures) != 1 || fake.configures[0].ApplicationID != application.ID || fake.configures[0].ActorUserID == "" || fake.configures[0].ExpectedRevision != 7 || fake.configures[0].Enabled || strings.Join(callbacks, ",") != "relay,auto" {
		t.Fatalf("disable=%d %s request=%#v callbacks=%v", disabling.Code, disabling.Body.String(), fake.configures, callbacks)
	}

	fake.configureErr = errors.New("sqlite credential secret")
	callbacks = callbacks[:0]
	failure := relayAuthenticatedRequest(handler, http.MethodPut, "/api/v1/apps/"+application.ID+"/auto-deploy", `{"expectedRevision":7,"enabled":false}`)
	if failure.Code != http.StatusInternalServerError || strings.Contains(failure.Body.String(), "secret") || len(callbacks) != 0 {
		t.Fatalf("failed configure=%d %s callbacks=%v", failure.Code, failure.Body.String(), callbacks)
	}
}

func TestAutoDeployRequiredScalarFieldsRejectInvalidPresenceAndTypes(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID)}
	relayCalls, autoCalls := 0, 0
	handler := autoDeployAdminHandler(store, fake, true, func() { relayCalls++ }, func() { autoCalls++ })
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "update missing expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"enabled":true}`},
		{name: "update missing enabled", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0}`},
		{name: "update null expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":null,"enabled":true}`},
		{name: "update null enabled", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"enabled":null}`},
		{name: "update duplicate expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"expectedRevision":1,"enabled":true}`},
		{name: "update duplicate enabled", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"enabled":true,"enabled":false}`},
		{name: "update string expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":"0","enabled":true}`},
		{name: "update boolean expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":true,"enabled":true}`},
		{name: "update fractional expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0.5,"enabled":true}`},
		{name: "update string enabled", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"enabled":"true"}`},
		{name: "update numeric enabled", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"enabled":1}`},
		{name: "update negative expected revision", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":-1,"enabled":true}`},
		{name: "update unknown field", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"enabled":true,"owner":"private"}`},
		{name: "update trailing object", method: http.MethodPut, path: "/auto-deploy", body: `{"expectedRevision":0,"enabled":true} {}`},
		{name: "update array", method: http.MethodPut, path: "/auto-deploy", body: `[]`},
		{name: "update empty body", method: http.MethodPut, path: "/auto-deploy", body: ``},
		{name: "resume missing expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{}`},
		{name: "resume null expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":null}`},
		{name: "resume duplicate expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":0,"expectedRevision":1}`},
		{name: "resume string expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":"0"}`},
		{name: "resume boolean expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":true}`},
		{name: "resume fractional expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":0.5}`},
		{name: "resume negative expected revision", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":-1}`},
		{name: "resume unknown field", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":0,"enabled":true}`},
		{name: "resume trailing scalar", method: http.MethodPost, path: "/auto-deploy/resume", body: `{"expectedRevision":0} true`},
		{name: "resume array", method: http.MethodPost, path: "/auto-deploy/resume", body: `[]`},
		{name: "resume empty body", method: http.MethodPost, path: "/auto-deploy/resume", body: ``},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake.configures = nil
			fake.resumes = nil
			fake.getCalls = 0
			relayCalls, autoCalls = 0, 0
			response := relayAuthenticatedRequest(handler, test.method, "/api/v1/apps/"+application.ID+test.path, test.body)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"invalid_auto_deploy_request"`) {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			if fake.getCalls != 0 || len(fake.configures) != 0 || len(fake.resumes) != 0 || relayCalls != 0 || autoCalls != 0 {
				t.Fatalf("invalid body reached service/callbacks: get=%d configure=%d resume=%d relay=%d auto=%d", fake.getCalls, len(fake.configures), len(fake.resumes), relayCalls, autoCalls)
			}
		})
	}
}

func TestAutoDeployRequestBodySizeBoundary(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	valid := `{"expectedRevision":0,"enabled":false}`
	exactCap := valid + strings.Repeat(" ", maxAutoDeployRequestBodyBytes-len(valid))
	if len(exactCap) != maxAutoDeployRequestBodyBytes {
		t.Fatalf("exact-cap fixture bytes=%d", len(exactCap))
	}
	hiddenTrailingValue := exactCap + `{}`
	oversizedSingleObject := `{"expectedRevision":` + strings.Repeat(" ", maxAutoDeployRequestBodyBytes) + `0,"enabled":false}`

	tests := []struct {
		name       string
		body       string
		status     int
		wantCalls  int
		wantRelays int
		wantAutos  int
	}{
		{name: "exact cap valid", body: exactCap, status: http.StatusOK, wantCalls: 1, wantRelays: 1, wantAutos: 1},
		{name: "trailing value hidden beyond cap", body: hiddenTrailingValue, status: http.StatusUnprocessableEntity},
		{name: "oversized single object", body: oversizedSingleObject, status: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID)}
			relayCalls, autoCalls := 0, 0
			handler := autoDeployAdminHandler(store, fake, true, func() { relayCalls++ }, func() { autoCalls++ })
			response := relayAuthenticatedRequest(handler, http.MethodPut, "/api/v1/apps/"+application.ID+"/auto-deploy", test.body)
			if response.Code != test.status {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			if test.status == http.StatusUnprocessableEntity && !strings.Contains(response.Body.String(), `"code":"invalid_auto_deploy_request"`) {
				t.Fatalf("oversized problem=%s", response.Body.String())
			}
			if len(fake.configures) != test.wantCalls || len(fake.resumes) != 0 || fake.getCalls != 0 || relayCalls != test.wantRelays || autoCalls != test.wantAutos {
				t.Fatalf("service/callbacks configure=%d resume=%d get=%d relay=%d auto=%d", len(fake.configures), len(fake.resumes), fake.getCalls, relayCalls, autoCalls)
			}
		})
	}
}

func TestAutoDeployConfigureAndResumeErrorMappings(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID)}
	handler := autoDeployAdminHandler(store, fake, true, nil, nil)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{autodeploy.ErrInvalid, http.StatusUnprocessableEntity, "invalid_auto_deploy_request"},
		{autodeploy.ErrConflict, http.StatusConflict, "auto_deploy_conflict"},
		{autodeploy.ErrState, http.StatusConflict, "auto_deploy_state_conflict"},
		{autodeploy.ErrApplicationBusy, http.StatusConflict, "application_busy"},
		{autodeploy.ErrSourceAccessLost, http.StatusConflict, "source_access_lost"},
		{autodeploy.ErrUnauthorized, http.StatusForbidden, "auto_deploy_forbidden"},
		{autodeploy.ErrNotFound, http.StatusConflict, "auto_deploy_prerequisite_missing"},
		{errors.New("private credential detail"), http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range cases {
		fake.configureErr = test.err
		response := relayAuthenticatedRequest(handler, http.MethodPut, "/api/v1/apps/"+application.ID+"/auto-deploy", `{"expectedRevision":0,"enabled":true}`)
		if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) || strings.Contains(response.Body.String(), "credential") {
			t.Errorf("configure err=%v got=%d %s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestAutoDeployFailureLoggingCoversEverySafeMapping(t *testing.T) {
	const rawDetail = "controller=private source=credential provider-body=/internal/path"
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid", err: errors.Join(errors.New(rawDetail), autodeploy.ErrInvalid), status: http.StatusUnprocessableEntity, code: "invalid_auto_deploy_request"},
		{name: "revision conflict", err: errors.Join(errors.New(rawDetail), autodeploy.ErrConflict), status: http.StatusConflict, code: "auto_deploy_conflict"},
		{name: "state conflict", err: errors.Join(errors.New(rawDetail), autodeploy.ErrState), status: http.StatusConflict, code: "auto_deploy_state_conflict"},
		{name: "application busy", err: errors.Join(errors.New(rawDetail), autodeploy.ErrApplicationBusy), status: http.StatusConflict, code: "application_busy"},
		{name: "source access lost", err: errors.Join(errors.New(rawDetail), autodeploy.ErrSourceAccessLost), status: http.StatusConflict, code: "source_access_lost"},
		{name: "forbidden", err: errors.Join(errors.New(rawDetail), autodeploy.ErrUnauthorized), status: http.StatusForbidden, code: "auto_deploy_forbidden"},
		{name: "prerequisite missing", err: errors.Join(errors.New(rawDetail), autodeploy.ErrNotFound), status: http.StatusConflict, code: "auto_deploy_prerequisite_missing"},
		{name: "internal", err: errors.New(rawDetail), status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			relayCalls, autoCalls := 0, 0
			fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID), configureErr: test.err}
			handler := (&Server{
				Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}}, Apps: store,
				AutoDeploy: fake, AutoDeployAvailable: true,
				RelayReconcile: func() { relayCalls++ }, AutoDeployReconcile: func() { autoCalls++ },
				Logger: failureTestLogger(&logs),
			}).Handler()
			path := "/api/v1/apps/" + application.ID + "/auto-deploy"
			response := relayAuthenticatedRequest(handler, http.MethodPut, path, `{"expectedRevision":0,"enabled":true}`)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			if len(fake.configures) != 1 || relayCalls != 0 || autoCalls != 0 {
				t.Fatalf("failure calls configure=%d relay=%d auto=%d", len(fake.configures), relayCalls, autoCalls)
			}
			assertSafeFailureLog(t, logs.String(), response, operationUpdateApplicationAutoDeploy, test.code, test.status, 0, rawDetail, application.ID, path)
		})
	}
}

func TestAutoDeployResumeOnlyReconcilesAfterSuccess(t *testing.T) {
	store, application, closeDB := autoDeployTestStore(t)
	defer closeDB()
	fake := &autoDeployServiceFake{status: autoDeployTestStatus(application.ID)}
	relayCalls, autoCalls := 0, 0
	handler := autoDeployAdminHandler(store, fake, true, func() { relayCalls++ }, func() { autoCalls++ })
	invalid := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/apps/"+application.ID+"/auto-deploy/resume", `{"expectedRevision":-1}`)
	if invalid.Code != http.StatusUnprocessableEntity || len(fake.resumes) != 0 {
		t.Fatalf("invalid resume=%d %s", invalid.Code, invalid.Body.String())
	}
	response := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/apps/"+application.ID+"/auto-deploy/resume", `{"expectedRevision":9}`)
	if response.Code != http.StatusOK || len(fake.resumes) != 1 || fake.resumes[0].applicationID != application.ID || fake.resumes[0].actorUserID == "" || fake.resumes[0].revision != 9 || relayCalls != 0 || autoCalls != 1 {
		t.Fatalf("resume=%d %s calls=%#v relay=%d auto=%d", response.Code, response.Body.String(), fake.resumes, relayCalls, autoCalls)
	}
	fake.resumeErr = autodeploy.ErrState
	response = relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/apps/"+application.ID+"/auto-deploy/resume", `{"expectedRevision":9}`)
	if response.Code != http.StatusConflict || autoCalls != 1 {
		t.Fatalf("failed resume=%d %s auto=%d", response.Code, response.Body.String(), autoCalls)
	}
}

func autoDeployTestStore(t *testing.T) (*apps.Store, apps.Application, func()) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	store := apps.New(db)
	if _, err := machines.New(db).EnsureLocal(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	application, err := store.Create("Auto Deploy Test", "", "", "")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return store, application, func() { _ = db.Close() }
}

func autoDeployTestStatus(applicationID string) autodeploy.Status {
	return autodeploy.Status{
		ApplicationID: applicationID, Revision: 7, Enabled: true, State: autodeploy.StateIdle,
		SourceScopeActive: true, LatestResolvedSHA: strings.Repeat("a", 40), ActiveSHA: strings.Repeat("b", 40),
		LastSuccessfulDeployedSHA: strings.Repeat("c", 40), PausedSHA: "", RetryAttempt: 2,
		UpdatedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	}
}

func autoDeployAdminHandler(store *apps.Store, service AutoDeployService, available bool, relayReconcile, autoReconcile func()) http.Handler {
	return (&Server{
		Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}}, Apps: store,
		AutoDeploy: service, AutoDeployAvailable: available, RelayReconcile: relayReconcile,
		AutoDeployReconcile: autoReconcile, Logger: relayTestLogger(),
	}).Handler()
}
