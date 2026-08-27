package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controllerrelay"
)

type controllerAuthFake struct{ user auth.User }

func (fake controllerAuthFake) BootstrapStatus() (bool, error) { return false, nil }
func (fake controllerAuthFake) Bootstrap(string, string, string) (auth.User, auth.Session, error) {
	return auth.User{}, auth.Session{}, errors.New("unused")
}
func (fake controllerAuthFake) Login(string, string) (auth.User, auth.Session, error) {
	return auth.User{}, auth.Session{}, errors.New("unused")
}
func (fake controllerAuthFake) Authenticate(string) (auth.User, string, error) {
	return fake.user, "csrf-hash", nil
}
func (controllerAuthFake) CheckCSRF(_ string, token string) bool { return token == "csrf-token" }
func (controllerAuthFake) RotateCSRF(string) (string, error)     { return "", errors.New("unused") }
func (controllerAuthFake) Logout(string) error                   { return nil }

type relayManagementFake struct {
	status        controllerrelay.ManagementStatus
	start         controllerrelay.ManagementEnrollmentStart
	startErr      error
	poll          controllerrelay.ManagementEnrollmentStatus
	pollErr       error
	remove        controllerrelay.ManagementBindingStatus
	removeErr     error
	rotation      controllerrelay.ManagementKeyRotationStatus
	rotationErr   error
	startOwner    string
	pollOwner     string
	removeOwner   string
	startInput    controllerrelay.ManagementEnrollmentInput
	pollID        string
	removeID      string
	rotationCalls int
}

func (fake *relayManagementFake) Status() controllerrelay.ManagementStatus { return fake.status }
func (fake *relayManagementFake) StartEnrollment(_ context.Context, owner string, input controllerrelay.ManagementEnrollmentInput) (controllerrelay.ManagementEnrollmentStart, error) {
	fake.startOwner, fake.startInput = owner, input
	return fake.start, fake.startErr
}
func (fake *relayManagementFake) PollEnrollment(_ context.Context, owner, id string) (controllerrelay.ManagementEnrollmentStatus, error) {
	fake.pollOwner, fake.pollID = owner, id
	return fake.poll, fake.pollErr
}
func (fake *relayManagementFake) RemoveBinding(_ context.Context, owner, id string) (controllerrelay.ManagementBindingStatus, error) {
	fake.removeOwner, fake.removeID = owner, id
	return fake.remove, fake.removeErr
}
func (fake *relayManagementFake) RotateKey(context.Context) (controllerrelay.ManagementKeyRotationStatus, error) {
	fake.rotationCalls++
	return fake.rotation, fake.rotationErr
}

func TestNewControllerRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	server := &Server{Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}}, Logger: relayTestLogger()}
	handler := server.Handler()
	allRoutes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/relay/status"},
		{http.MethodPost, "/api/v1/relay/enrollments"},
		{http.MethodPost, "/api/v1/relay/enrollments/" + uuid.NewString() + "/poll"},
		{http.MethodDelete, "/api/v1/relay/bindings/" + uuid.NewString()},
		{http.MethodPost, "/api/v1/relay/key-rotations"},
		{http.MethodGet, "/api/v1/apps/" + uuid.NewString() + "/auto-deploy"},
		{http.MethodPut, "/api/v1/apps/" + uuid.NewString() + "/auto-deploy"},
		{http.MethodPost, "/api/v1/apps/" + uuid.NewString() + "/auto-deploy/resume"},
	}
	for _, route := range allRoutes {
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated=%d", route.method, route.path, response.Code)
		}
	}
	for _, route := range allRoutes {
		if route.method == http.MethodGet {
			continue
		}
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"csrf_failed"`) {
			t.Errorf("%s %s without CSRF=%d %s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestRelayManagementRoutesValidateScopeAndProjectContracts(t *testing.T) {
	owner := uuid.NewString()
	enrollmentID, bindingID, rotationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	fake := &relayManagementFake{
		status:   controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementAvailable, State: "ready", Outcome: "idle", PendingCommands: -1, ObserverDropped: ^uint64(0)},
		start:    controllerrelay.ManagementEnrollmentStart{EnrollmentID: enrollmentID, AuthorizationURL: "https://relay.example/enroll", State: controllerrelay.EnrollmentPending, ExpiresAt: now.Add(time.Minute)},
		poll:     controllerrelay.ManagementEnrollmentStatus{EnrollmentID: enrollmentID, State: controllerrelay.EnrollmentPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)},
		remove:   controllerrelay.ManagementBindingStatus{BindingID: bindingID, State: controllerrelay.BindingRemovalPending, UpdatedAt: now},
		rotation: controllerrelay.ManagementKeyRotationStatus{RotationID: rotationID, State: controllerrelay.RotationPrepare, ExpiresAt: now.Add(time.Minute)},
	}
	handler := (&Server{Auth: controllerAuthFake{user: auth.User{ID: owner, Role: "administrator"}}, RelayManagement: fake, Logger: relayTestLogger()}).Handler()

	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/relay/status", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"availability":"available"`) || !strings.Contains(status.Body.String(), `"pendingCommands":0`) || strings.Contains(status.Body.String(), "184467") {
		t.Fatalf("relay status=%d %s", status.Code, status.Body.String())
	}
	invalid := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/enrollments", `{"connectionId":"bad","installationId":1,"repositoryId":2,"owner":"attacker"}`)
	if invalid.Code != http.StatusUnprocessableEntity || fake.startOwner != "" {
		t.Fatalf("invalid enrollment=%d %s owner=%q", invalid.Code, invalid.Body.String(), fake.startOwner)
	}
	started := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/enrollments", `{"connectionId":"0123456789abcdef0123456789abcdef","installationId":1,"repositoryId":2}`)
	if started.Code != http.StatusCreated || started.Header().Get("Cache-Control") != "no-store" || fake.startOwner != owner || fake.startInput.RepositoryID != 2 {
		t.Fatalf("start=%d %s owner=%q input=%#v", started.Code, started.Body.String(), fake.startOwner, fake.startInput)
	}
	badPoll := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/enrollments/not-a-uuid/poll", "")
	if badPoll.Code != http.StatusUnprocessableEntity || fake.pollOwner != "" {
		t.Fatalf("bad poll=%d %s", badPoll.Code, badPoll.Body.String())
	}
	pending := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/enrollments/"+enrollmentID+"/poll", "")
	if pending.Code != http.StatusAccepted || pending.Header().Get("Cache-Control") != "no-store" || fake.pollOwner != owner {
		t.Fatalf("pending poll=%d %s owner=%q", pending.Code, pending.Body.String(), fake.pollOwner)
	}
	fake.poll.State = controllerrelay.EnrollmentAuthorized
	terminal := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/enrollments/"+enrollmentID+"/poll", "")
	if terminal.Code != http.StatusOK {
		t.Fatalf("terminal poll=%d %s", terminal.Code, terminal.Body.String())
	}
	removing := relayAuthenticatedRequest(handler, http.MethodDelete, "/api/v1/relay/bindings/"+bindingID, "")
	if removing.Code != http.StatusAccepted || fake.removeOwner != owner {
		t.Fatalf("remove=%d %s owner=%q", removing.Code, removing.Body.String(), fake.removeOwner)
	}
	fake.remove.State = controllerrelay.BindingRemoved
	removed := relayAuthenticatedRequest(handler, http.MethodDelete, "/api/v1/relay/bindings/"+bindingID, "")
	if removed.Code != http.StatusOK {
		t.Fatalf("removed=%d %s", removed.Code, removed.Body.String())
	}
	rotation := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
	if rotation.Code != http.StatusAccepted || fake.rotationCalls != 1 {
		t.Fatalf("rotation=%d %s calls=%d", rotation.Code, rotation.Body.String(), fake.rotationCalls)
	}
}

func TestRelayUnavailableAndErrorsAreStableAndBounded(t *testing.T) {
	owner := uuid.NewString()
	handler := (&Server{Auth: controllerAuthFake{user: auth.User{ID: owner, Role: "administrator"}}, Logger: relayTestLogger()}).Handler()
	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/relay/status", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"availability":"unavailable"`) || !strings.Contains(status.Body.String(), `"diagnosticsUnavailable":true`) {
		t.Fatalf("nil relay status=%d %s", status.Code, status.Body.String())
	}
	response := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"relay_unavailable"`) {
		t.Fatalf("nil relay=%d %s", response.Code, response.Body.String())
	}
	fake := &relayManagementFake{rotationErr: &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorUnavailable, RetryAfter: 24 * time.Hour}}
	handler = (&Server{Auth: controllerAuthFake{user: auth.User{ID: owner, Role: "administrator"}}, RelayManagement: fake, Logger: relayTestLogger()}).Handler()
	response = relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "300" || strings.Contains(response.Body.String(), "24h") {
		t.Fatalf("bounded relay error=%d retry=%q %s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
	fake.rotationErr = errors.New("credential=secret sqlite internal path")
	response = relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") || !strings.Contains(response.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("unsafe relay error=%d %s", response.Code, response.Body.String())
	}
}

func TestRelaySourceErrorsUseStableSafeProblems(t *testing.T) {
	const rawDetail = "credential=secret provider-private-body internal/path"
	owner := uuid.NewString()
	fake := &relayManagementFake{}
	handler := (&Server{Auth: controllerAuthFake{user: auth.User{ID: owner, Role: "administrator"}}, RelayManagement: fake, Logger: relayTestLogger()}).Handler()
	tests := []struct {
		code   string
		status int
		detail string
	}{
		{code: "invalid_source", status: http.StatusUnprocessableEntity, detail: "GitHub source is invalid or no longer exists"},
		{code: "authentication_required", status: http.StatusForbidden, detail: "GitHub authorization does not permit this operation"},
		{code: "provider_unavailable", status: http.StatusServiceUnavailable, detail: "GitHub is temporarily unavailable"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			fake.rotationErr = errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: test.code})
			response := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) || !strings.Contains(response.Body.String(), `"detail":"`+test.detail+`"`) {
				t.Fatalf("mapping=%d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), rawDetail) || strings.Contains(response.Body.String(), "credential") || strings.Contains(response.Body.String(), "provider-private") {
				t.Fatalf("raw error detail leaked: %s", response.Body.String())
			}
		})
	}
}

func TestRelayFailureLoggingCoversEverySafeMapping(t *testing.T) {
	const rawDetail = "app=11111111-1111-4111-8111-111111111111 enrollment=22222222-2222-4222-8222-222222222222 binding=33333333-3333-4333-8333-333333333333 controller=private source=credential provider-body=/internal/path"
	owner := uuid.NewString()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		retry  int64
	}{
		{name: "raw error", err: errors.New(rawDetail), status: http.StatusInternalServerError, code: "internal_error"},
		{name: "management unavailable", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorUnavailable, RetryAfter: 24 * time.Hour}), status: http.StatusServiceUnavailable, code: "relay_unavailable", retry: 300},
		{name: "relay unavailable", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "relay_unavailable"}), status: http.StatusServiceUnavailable, code: "relay_unavailable"},
		{name: "provider unavailable", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "provider_unavailable"}), status: http.StatusServiceUnavailable, code: "provider_unavailable"},
		{name: "invalid request", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorInvalidRequest}), status: http.StatusUnprocessableEntity, code: "invalid_relay_request"},
		{name: "enrollment missing", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorEnrollmentMissing}), status: http.StatusNotFound, code: "relay_enrollment_not_found"},
		{name: "binding missing", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorBindingMissing}), status: http.StatusNotFound, code: "relay_binding_not_found"},
		{name: "binding state", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorBindingState}), status: http.StatusConflict, code: "relay_state_conflict"},
		{name: "rotation conflict", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorRotationConflict}), status: http.StatusConflict, code: "relay_state_conflict"},
		{name: "identity missing", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorIdentity}), status: http.StatusConflict, code: "relay_prerequisite_missing"},
		{name: "invalid source", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "invalid_source"}), status: http.StatusUnprocessableEntity, code: "invalid_source"},
		{name: "authentication required", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "authentication_required"}), status: http.StatusForbidden, code: "authentication_required"},
		{name: "source access lost", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "source_access_lost"}), status: http.StatusConflict, code: "source_access_lost"},
		{name: "authorization denied", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "authorization_denied"}), status: http.StatusConflict, code: "source_access_lost"},
		{name: "authorization expired", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "authorization_expired"}), status: http.StatusConflict, code: "source_access_lost"},
		{name: "unknown management code", err: errors.Join(errors.New(rawDetail), &controllerrelay.ManagementError{Code: "private_provider_code"}), status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			fake := &relayManagementFake{rotationErr: test.err}
			handler := (&Server{Auth: controllerAuthFake{user: auth.User{ID: owner, Role: "administrator"}}, RelayManagement: fake, Logger: failureTestLogger(&logs)}).Handler()
			response := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			assertSafeFailureLog(t, logs.String(), response, operationStartRelayKeyRotation, test.code, test.status, test.retry, rawDetail)
		})
	}
}

func TestOperationAwareAuthLoggingDoesNotRecordRawResourcePaths(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		operation string
		code      string
		status    int
		cookie    bool
	}{
		{name: "relay status unauthenticated", method: http.MethodGet, path: "/api/v1/relay/status", operation: operationGetRelayStatus, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "relay enrollment unauthenticated", method: http.MethodPost, path: "/api/v1/relay/enrollments", operation: operationStartRelayEnrollment, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "relay poll unauthenticated", method: http.MethodPost, path: "/api/v1/relay/enrollments/22222222-2222-4222-8222-222222222222/poll", operation: operationPollRelayEnrollment, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "relay binding unauthenticated", method: http.MethodDelete, path: "/api/v1/relay/bindings/33333333-3333-4333-8333-333333333333", operation: operationRemoveRelayBinding, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "relay rotation unauthenticated", method: http.MethodPost, path: "/api/v1/relay/key-rotations", operation: operationStartRelayKeyRotation, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "app unauthenticated", method: http.MethodGet, path: "/api/v1/apps/11111111-1111-4111-8111-111111111111/auto-deploy", operation: operationGetApplicationAutoDeploy, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "app update unauthenticated", method: http.MethodPut, path: "/api/v1/apps/11111111-1111-4111-8111-111111111111/auto-deploy", operation: operationUpdateApplicationAutoDeploy, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "app resume unauthenticated", method: http.MethodPost, path: "/api/v1/apps/11111111-1111-4111-8111-111111111111/auto-deploy/resume", operation: operationResumeApplicationAutoDeploy, code: "unauthenticated", status: http.StatusUnauthorized},
		{name: "enrollment csrf", method: http.MethodPost, path: "/api/v1/relay/enrollments/22222222-2222-4222-8222-222222222222/poll", operation: operationPollRelayEnrollment, code: "csrf_failed", status: http.StatusForbidden, cookie: true},
		{name: "binding csrf", method: http.MethodDelete, path: "/api/v1/relay/bindings/33333333-3333-4333-8333-333333333333", operation: operationRemoveRelayBinding, code: "csrf_failed", status: http.StatusForbidden, cookie: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			handler := (&Server{Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}}, Logger: failureTestLogger(&logs)}).Handler()
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			assertSafeFailureLog(t, logs.String(), response, test.operation, test.code, test.status, 0, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333")
		})
	}
}

func TestRelayRotationRequiresAdministrator(t *testing.T) {
	fake := &relayManagementFake{}
	handler := (&Server{Auth: controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "operator"}}, RelayManagement: fake, Logger: relayTestLogger()}).Handler()
	response := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/relay/key-rotations", "")
	if response.Code != http.StatusForbidden || fake.rotationCalls != 0 {
		t.Fatalf("operator rotation=%d %s calls=%d", response.Code, response.Body.String(), fake.rotationCalls)
	}
}

func relayAuthenticatedRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	request.Header.Set("X-CSRF-Token", "csrf-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func relayTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func failureTestLogger(destination io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(destination, nil))
}

func assertSafeFailureLog(t *testing.T, output string, response *httptest.ResponseRecorder, operation, code string, status int, retryAfter int64, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(output, value) {
			t.Fatalf("unsafe log contains %q: %s", value, output)
		}
	}
	if strings.Contains(output, `"path"`) || strings.Contains(output, `"error"`) || strings.Contains(output, `"detail"`) {
		t.Fatalf("unsafe/high-cardinality log schema: %s", output)
	}
	var failure map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log: %v: %s", err, line)
		}
		if record["msg"] == "api handler failure" {
			if failure != nil {
				t.Fatalf("multiple failure logs: %s", output)
			}
			failure = record
		}
	}
	if failure == nil {
		t.Fatalf("missing failure log: %s", output)
	}
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "request_id": true, "operation": true, "problem_code": true, "status": true, "retry_after_seconds": true}
	for key := range failure {
		if !allowed[key] {
			t.Fatalf("unexpected failure log field %q: %#v", key, failure)
		}
	}
	if len(failure) != len(allowed) {
		t.Fatalf("failure log cardinality=%d want=%d: %#v", len(failure), len(allowed), failure)
	}
	if failure["request_id"] != response.Header().Get("X-Request-ID") || failure["operation"] != operation || failure["problem_code"] != code || failure["status"] != float64(status) || failure["retry_after_seconds"] != float64(retryAfter) {
		t.Fatalf("failure log values=%#v response_request_id=%q", failure, response.Header().Get("X-Request-ID"))
	}
}
