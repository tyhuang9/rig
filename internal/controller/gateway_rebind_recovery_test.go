package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type rebindRecoveryHandlerRepository struct {
	generatedingress.GatewayRebindRecoveryRepository
}

type rebindRecoveryHandlerRuntime struct {
	inspection generatedingress.GatewayRebindOperationInspection
	review     generatedingress.GatewayRebindRecoveryReview
	response   generatedingress.GatewayRebindReconcileResponse
	err        error
	calls      int
	repository generatedingress.GatewayRebindRecoveryReadRepository
	operation  string
	actor      string
	request    generatedingress.GatewayRebindReconcileRequest
	guard      func(context.Context) error
	onCall     func()
}

type rebindRecoveryNoCommandRunner struct{ calls int }

func (r *rebindRecoveryNoCommandRunner) Run(context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.calls++
	return runtimeprocess.CommandResult{}, errors.New("unexpected physical command")
}

func (f *rebindRecoveryHandlerRuntime) InspectGatewayRebindOperation(_ context.Context,
	repository generatedingress.GatewayRebindRecoveryReadRepository, operationID string,
) (generatedingress.GatewayRebindOperationInspection, error) {
	f.calls++
	f.repository, f.operation = repository, operationID
	if f.onCall != nil {
		f.onCall()
	}
	return f.inspection, f.err
}

func (f *rebindRecoveryHandlerRuntime) InspectGatewayRebindRecoveryReview(_ context.Context,
	repository generatedingress.GatewayRebindRecoveryReadRepository, operationID, actorID string,
	guard func(context.Context) error,
) (generatedingress.GatewayRebindRecoveryReview, error) {
	f.calls++
	f.repository, f.operation, f.actor, f.guard = repository, operationID, actorID, guard
	if f.onCall != nil {
		f.onCall()
	}
	return f.review, f.err
}

func (f *rebindRecoveryHandlerRuntime) ReconcileGatewayRebind(_ context.Context,
	repository generatedingress.GatewayRebindRecoveryRepository,
	request generatedingress.GatewayRebindReconcileRequest, actorID string,
	guard func(context.Context) error,
) (generatedingress.GatewayRebindReconcileResponse, error) {
	f.calls++
	f.repository, f.operation, f.actor, f.request, f.guard = repository, request.OperationID, actorID, request, guard
	if f.onCall != nil {
		f.onCall()
	}
	return f.response, f.err
}

func newRebindRecoveryHandlerFixture(t *testing.T, kind string) (*Server, *http.Request,
	*rebindRecoveryHandlerRuntime, *rebindSessionAuthFake,
) {
	t.Helper()
	s, request, authentication := newRebindSessionFixture()
	authentication.authenticate = func(token string) (auth.User, string, error) {
		if token != "original-session" {
			return auth.User{}, "", errors.New("invalid test session")
		}
		return auth.User{ID: rebindRequestRevision, Role: "administrator"}, "live-csrf-hash", nil
	}
	request = request.WithContext(context.WithValue(request.Context(), principalKey{}, principal{
		user: auth.User{ID: rebindRequestRevision, Role: "administrator"},
	}))
	request.URL.Path = "/api/v1/system/lan-gateway-rebind/" + rebindRequestOperation
	request.SetPathValue("operationId", rebindRequestOperation)
	switch kind {
	case "status":
		request.Method = http.MethodGet
		request.Header.Del("X-CSRF-Token")
		request.Header.Del("Content-Type")
		request.Body = nil
	case "review":
		request.Method = http.MethodPost
		request.URL.Path += "/recovery-proposal"
		request.Header.Set("Content-Type", "application/json")
		request.Body = io.NopCloser(strings.NewReader(`{}`))
	case "reconcile":
		request.Method = http.MethodPost
		request.URL.Path += "/reconcile"
		request.Header.Set("Content-Type", "application/json")
		request.Body = io.NopCloser(strings.NewReader(`{"version":1,"action":"reconcile","recoveryDigest":"` +
			strings.Repeat("a", 64) + `","recoveryApprovalDigest":"` + strings.Repeat("b", 64) + `"}`))
	default:
		t.Fatalf("unknown recovery handler kind %q", kind)
	}
	runtime := &rebindRecoveryHandlerRuntime{}
	s.GeneratedRuntime = true
	s.GatewayRebindRecoveries = &rebindRecoveryHandlerRepository{}
	s.GatewayRebindRecoveryRuntime = runtime
	return s, request, runtime, authentication
}

func invokeRebindRecoveryHandler(s *Server, request *http.Request, kind string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	switch kind {
	case "status":
		s.getLANGatewayRebind(response, request)
	case "review":
		s.inspectLANGatewayRebindRecovery(response, request)
	case "reconcile":
		s.reconcileLANGatewayRebind(response, request)
	default:
		panic("unknown recovery handler kind")
	}
	return response
}

func rebindRecoveryTerminalInspection() generatedingress.GatewayRebindOperationInspection {
	receipt := strings.Repeat("d", 64)
	return generatedingress.GatewayRebindOperationInspection{
		Version:     generatedingress.GatewayRebindOperationInspectionVersion,
		OperationID: rebindRequestOperation, Availability: generatedingress.GatewayRebindOperationAvailable,
		Operation: generatedingress.GatewayRebindOperationStatus{
			OperationID: rebindRequestOperation, StoredState: appaccess.GatewayRebindCommitted,
			StateSequence: 4, DatabaseCommitObserved: true,
			TerminalDisposition: appaccess.GatewayRebindDispositionCommit, TerminalReceiptDigest: receipt,
		},
		DatabaseCommittedEvent: &appaccess.GatewayRebindEvent{
			OperationID: rebindRequestOperation, Sequence: 3, State: appaccess.GatewayRebindDatabaseCommitted,
		},
		Protected: &appaccess.GatewayRebindProtectedRecoveryEvidence{
			Presence: appaccess.GatewayRebindProtectedEvidencePresent, OperationID: rebindRequestOperation,
			Generation: 2, ScanDigest: strings.Repeat("1", 64), IntentDigest: strings.Repeat("2", 64),
			RecordSequence: 17, RecordDigest: strings.Repeat("3", 64),
			FixedDisposition: appaccess.GatewayRebindDispositionCommit, TerminalReceiptDigest: receipt,
		},
		ProtectedPhase: "handover_committed", HistoricalTerminal: true,
		TerminalProof:       generatedingress.GatewayRebindProofVerified,
		TerminalProofDigest: strings.Repeat("e", 64),
		CurrentProof:        generatedingress.GatewayRebindProofUnavailable,
		OperationFence:      generatedingress.GatewayRebindOperationFenceReleased,
		AllowedActions:      []generatedingress.GatewayRebindRecoveryAction{},
	}
}

func rebindRecoveryAbsentInspection() generatedingress.GatewayRebindOperationInspection {
	return generatedingress.GatewayRebindOperationInspection{
		Version:        generatedingress.GatewayRebindOperationInspectionVersion,
		OperationID:    rebindRequestOperation,
		Availability:   generatedingress.GatewayRebindOperationValidatedAbsent,
		TerminalProof:  generatedingress.GatewayRebindProofUnknown,
		CurrentProof:   generatedingress.GatewayRebindProofUnknown,
		OperationFence: generatedingress.GatewayRebindOperationFenceUnavailable,
		AllowedActions: []generatedingress.GatewayRebindRecoveryAction{},
	}
}

func TestLANGatewayRebindStatusProjectsOnlyCuratedEvidence(t *testing.T) {
	server, request, runtime, authentication := newRebindRecoveryHandlerFixture(t, "status")
	authentication.checkCSRF = func(string, string) bool {
		t.Fatal("GET status invoked CSRF validation")
		return false
	}
	runtime.inspection = rebindRecoveryTerminalInspection()
	response := invokeRebindRecoveryHandler(server, request, "status")
	if response.Code != http.StatusOK || runtime.calls != 1 ||
		runtime.repository != server.GatewayRebindRecoveries || runtime.operation != rebindRequestOperation ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%d calls=%d operation=%q body=%s",
			response.Code, runtime.calls, runtime.operation, response.Body.String())
	}
	var status lanGatewayRebindStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil ||
		status.OperationID != rebindRequestOperation || status.Operation == nil ||
		status.Operation.StoredState != string(appaccess.GatewayRebindCommitted) ||
		status.Protected == nil || status.Protected.Phase != "handover_committed" ||
		status.Protected.RecordSequence != 17 || status.TerminalProof != "verified" ||
		status.CurrentProof != "unavailable" || status.OperationFence != "released" ||
		status.AllowedActions == nil || len(status.AllowedActions) != 0 {
		t.Fatalf("unexpected status projection: %s (%v)", response.Body.String(), err)
	}
	for _, forbidden := range []string{
		"databaseCommittedEvent", "scanDigest", "intentDigest", "recordDigest",
		"authorization", "auditEventId", "auditedAt", "createdAt",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestLANGatewayRebindRecoveryReviewSuppressesNonExecutableConsent(t *testing.T) {
	server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, "review")
	runtime.review = generatedingress.GatewayRebindRecoveryReview{
		Version: generatedingress.GatewayRebindRecoveryReviewVersion,
		Status:  rebindRecoveryTerminalInspection(),
	}
	response := invokeRebindRecoveryHandler(server, request, "review")
	if response.Code != http.StatusOK || runtime.calls != 1 ||
		runtime.repository != server.GatewayRebindRecoveries || runtime.operation != rebindRequestOperation ||
		runtime.actor != rebindRequestRevision || runtime.guard == nil {
		t.Fatalf("response=%d calls=%d actor=%q body=%s",
			response.Code, runtime.calls, runtime.actor, response.Body.String())
	}
	var review lanGatewayRebindRecoveryReview
	if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil ||
		review.OperationID != rebindRequestOperation || review.Version != 1 ||
		review.RecoveryDigest != "" || review.RecoveryApprovalDigest != "" ||
		len(review.Status.AllowedActions) != 0 {
		t.Fatalf("non-executable review=%s (%v)", response.Body.String(), err)
	}
	for _, forbidden := range []string{"recoveryDigest", "recoveryApprovalDigest", "snapshot", "authorization"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("review exposed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestLANGatewayRebindRecoveryValidatedAbsenceIsOnlyNotFound(t *testing.T) {
	for _, kind := range []string{"status", "review"} {
		t.Run(kind, func(t *testing.T) {
			server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, kind)
			if kind == "status" {
				runtime.inspection = rebindRecoveryAbsentInspection()
			} else {
				runtime.review = generatedingress.GatewayRebindRecoveryReview{
					Version: generatedingress.GatewayRebindRecoveryReviewVersion,
					Status:  rebindRecoveryAbsentInspection(),
				}
			}
			response := invokeRebindRecoveryHandler(server, request, kind)
			if response.Code != http.StatusNotFound || runtime.calls != 1 ||
				!strings.Contains(response.Body.String(), `"code":"lan_gateway_rebind_not_found"`) ||
				strings.Contains(response.Body.String(), rebindRequestOperation) {
				t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindReconcileForwardsPrincipalAndExactConsent(t *testing.T) {
	server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, "reconcile")
	runtime.err = appaccess.ErrConflict
	response := invokeRebindRecoveryHandler(server, request, "reconcile")
	if response.Code != http.StatusConflict || runtime.calls != 1 ||
		runtime.repository != server.GatewayRebindRecoveries || runtime.actor != rebindRequestRevision ||
		runtime.operation != rebindRequestOperation || runtime.guard == nil ||
		runtime.request.Version != generatedingress.GatewayRebindReconcileVersion ||
		runtime.request.RecoveryDigest != strings.Repeat("a", 64) ||
		runtime.request.RecoveryApprovalDigest != strings.Repeat("b", 64) {
		t.Fatalf("response=%d actor=%q request=%#v body=%s",
			response.Code, runtime.actor, runtime.request, response.Body.String())
	}
	if err := runtime.guard(context.Background()); err != nil {
		t.Fatalf("forwarded live guard refused unchanged session: %v", err)
	}
}

func TestLANGatewayRebindReconcileRejectsCrossBoundAuthorization(t *testing.T) {
	server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, "reconcile")
	runtime.response = generatedingress.GatewayRebindReconcileResponse{
		Version: generatedingress.GatewayRebindReconcileVersion,
		Status:  rebindRecoveryTerminalInspection(),
		Authorization: appaccess.GatewayRebindReconcileAuthorization{
			ActorID: rebindRequestOperation, AuditEventID: 1, AuditedAt: time.Unix(1, 0).UTC(),
		},
	}
	response := invokeRebindRecoveryHandler(server, request, "reconcile")
	if response.Code != http.StatusServiceUnavailable || runtime.calls != 1 ||
		strings.Contains(response.Body.String(), "audit") ||
		strings.Contains(response.Body.String(), rebindRequestOperation) {
		t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
	}
}

// This is an explicit test-only assembly for dormant endpoints. It uses the
// real HTTP authentication middleware, SQLite repository, Manager locks and
// protected-state store. The command runner is a tripwire: validated absence
// must not turn into a physical Docker probe or mutation.
func TestLANGatewayRebindRecoveryRealHTTPAssemblyValidatesAbsence(t *testing.T) {
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authentication := auth.New(db)
	bootstrapToken, err := authentication.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := authentication.Bootstrap(bootstrapToken, "rebind-admin", "this is a secure passphrase")
	if err != nil {
		t.Fatal(err)
	}
	repository := appaccess.New(db)
	runner := &rebindRecoveryNoCommandRunner{}
	manager, err := generatedingress.New(runner, generatedingress.Options{
		DockerExecutable:      filepath.Join(root, "docker.exe"),
		DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory:      directories.WorkingDirectory, DataRoot: root,
		RebindFenceCheck:             repository.CheckGatewayRebindFence,
		RebindCurrentStateRepository: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		Auth: authentication, GeneratedRuntime: true,
		GatewayRebindRecoveries: repository, GatewayRebindRecoveryRuntime: manager,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/system/lan-gateway-rebind/{operationId}",
		noStore(server.requireOperation(operationGetLANGatewayRebind, server.getLANGatewayRebind)))
	mux.HandleFunc("POST /api/v1/system/lan-gateway-rebind/{operationId}/recovery-proposal",
		noStore(server.requireOperation(operationInspectLANGatewayRebindRecovery,
			server.inspectLANGatewayRebindRecovery)))
	mux.HandleFunc("POST /api/v1/system/lan-gateway-rebind/{operationId}/reconcile",
		noStore(server.requireOperation(operationReconcileLANGatewayRebind,
			server.reconcileLANGatewayRebind)))

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		value := httptest.NewRequest(method, path, strings.NewReader(body))
		value.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
		if method == http.MethodPost {
			value.Header.Set("Content-Type", "application/json")
			value.Header.Set("X-CSRF-Token", session.CSRF)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, value)
		return response
	}

	status := request(http.MethodGet,
		"/api/v1/system/lan-gateway-rebind/"+rebindRequestOperation, "")
	if status.Code != http.StatusNotFound ||
		!strings.Contains(status.Body.String(), `"code":"lan_gateway_rebind_not_found"`) {
		t.Fatalf("real status response=%d %s", status.Code, status.Body.String())
	}
	review := request(http.MethodPost,
		"/api/v1/system/lan-gateway-rebind/"+rebindRequestOperation+"/recovery-proposal", `{}`)
	if review.Code != http.StatusNotFound ||
		!strings.Contains(review.Body.String(), `"code":"lan_gateway_rebind_not_found"`) {
		t.Fatalf("real review response=%d %s", review.Code, review.Body.String())
	}
	reconcile := request(http.MethodPost,
		"/api/v1/system/lan-gateway-rebind/"+rebindRequestOperation+"/reconcile",
		`{"version":1,"action":"reconcile","recoveryDigest":"`+strings.Repeat("a", 64)+
			`","recoveryApprovalDigest":"`+strings.Repeat("b", 64)+`"}`)
	if reconcile.Code == http.StatusOK || reconcile.Code == http.StatusCreated ||
		strings.Contains(reconcile.Body.String(), "unexpected physical command") {
		t.Fatalf("real reconcile response=%d %s", reconcile.Code, reconcile.Body.String())
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE resource_type='lan_gateway_rebind' AND action=?`,
		appaccess.GatewayRebindReconcilePurpose).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 0 || runner.calls != 0 {
		t.Fatalf("absence caused audit or physical effects: audits=%d commands=%d", audits, runner.calls)
	}
}

func TestLANGatewayRebindRecoveryHandlersRefuseBeforeRuntime(t *testing.T) {
	for _, kind := range []string{"status", "review", "reconcile"} {
		for _, fixture := range []struct {
			name   string
			status int
			mutate func(*Server, **http.Request)
		}{
			{"viewer", http.StatusForbidden, func(_ *Server, request **http.Request) {
				*request = (*request).WithContext(context.WithValue((*request).Context(), principalKey{}, principal{
					user: auth.User{ID: rebindRequestRevision, Role: "viewer"},
				}))
			}},
			{"runtime disabled", http.StatusServiceUnavailable, func(server *Server, _ **http.Request) {
				server.GeneratedRuntime = false
			}},
			{"missing runtime", http.StatusServiceUnavailable, func(server *Server, _ **http.Request) {
				server.GatewayRebindRecoveryRuntime = nil
			}},
			{"missing repository", http.StatusServiceUnavailable, func(server *Server, _ **http.Request) {
				server.GatewayRebindRecoveries = nil
			}},
		} {
			t.Run(kind+"/"+fixture.name, func(t *testing.T) {
				server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, kind)
				// Capability refusal must win without consuming even malformed input.
				if fixture.status == http.StatusServiceUnavailable {
					request.URL.RawQuery = "injected=true"
				}
				fixture.mutate(server, &request)
				response := invokeRebindRecoveryHandler(server, request, kind)
				if response.Code != fixture.status || runtime.calls != 0 ||
					response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("response=%d calls=%d headers=%v body=%s",
						response.Code, runtime.calls, response.Header(), response.Body.String())
				}
			})
		}
	}
}

func TestLANGatewayRebindRecoveryHandlersEnforceKindAndPinBeforeRuntime(t *testing.T) {
	for _, kind := range []string{"status", "review", "reconcile"} {
		for _, fixture := range []struct {
			name string
			kind string
			pin  string
		}{
			{"wrong recovery kind", RecoveryGatewayUpgrade, rebindRequestOperation},
			{"missing recovery pin", RecoveryGatewayRebind, ""},
			{"different recovery pin", RecoveryGatewayRebind, rebindProfileOperation},
		} {
			t.Run(kind+"/"+fixture.name, func(t *testing.T) {
				server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, kind)
				server.RecoveryOnly = true
				server.RecoveryKind = fixture.kind
				server.RecoveryOperationID = fixture.pin
				response := invokeRebindRecoveryHandler(server, request, kind)
				if response.Code != http.StatusConflict || runtime.calls != 0 ||
					response.Header().Get("Cache-Control") != "no-store" ||
					!strings.Contains(response.Body.String(), `"code":"lan_gateway_conflict"`) {
					t.Fatalf("response=%d calls=%d headers=%v body=%s",
						response.Code, runtime.calls, response.Header(), response.Body.String())
				}
			})
		}
	}
}

func TestLANGatewayRebindCanonicalRoutesAndRecoveryOnlyPolicy(t *testing.T) {
	wantRoutes := map[string]string{
		operationInspectLANGatewayRebind:         http.MethodPost + " /api/v1/system/lan-gateway-rebind/proposal",
		operationCommitLANGatewayRebind:          http.MethodPost + " /api/v1/system/lan-gateway-rebind",
		operationGetLANGatewayRebind:             http.MethodGet + " /api/v1/system/lan-gateway-rebind/{operationId}",
		operationInspectLANGatewayRebindRecovery: http.MethodPost + " /api/v1/system/lan-gateway-rebind/{operationId}/recovery-proposal",
		operationReconcileLANGatewayRebind:       http.MethodPost + " /api/v1/system/lan-gateway-rebind/{operationId}/reconcile",
	}
	gotRoutes := make(map[string]string, len(wantRoutes))
	for _, route := range (&Server{}).apiRoutes() {
		if _, ok := wantRoutes[route.operationID]; ok {
			gotRoutes[route.operationID] = route.method + " " + route.path
		}
	}
	if len(gotRoutes) != len(wantRoutes) {
		t.Fatalf("registered rebind routes=%v", gotRoutes)
	}
	for operation, want := range wantRoutes {
		if gotRoutes[operation] != want {
			t.Errorf("route %s=%q want %q", operation, gotRoutes[operation], want)
		}
	}

	runtime := &rebindRecoveryHandlerRuntime{inspection: rebindRecoveryTerminalInspection()}
	server := &Server{
		Auth:                         controllerAuthFake{user: auth.User{ID: rebindRequestRevision, Role: "administrator"}},
		GeneratedRuntime:             true,
		GatewayRebindRecoveries:      &rebindRecoveryHandlerRepository{},
		GatewayRebindRecoveryRuntime: runtime,
		RecoveryOnly:                 true,
		RecoveryKind:                 RecoveryGatewayRebind,
		RecoveryOperationID:          rebindRequestOperation,
		Logger:                       relayTestLogger(),
	}
	handler := server.Handler()

	if response := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/auth/me", ""); response.Code != http.StatusOK {
		t.Fatalf("authentication route=%d %s", response.Code, response.Body.String())
	}
	statusPath := "/api/v1/system/lan-gateway-rebind/" + rebindRequestOperation
	response := relayAuthenticatedRequest(handler, http.MethodGet, statusPath, "")
	if response.Code != http.StatusOK || runtime.calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("pinned status=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
	}

	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/system/status", ""},
		{http.MethodPost, "/api/v1/system/lan-gateway-rebind/proposal", `{}`},
		{http.MethodPost, "/api/v1/system/lan-gateway-rebind", `{}`},
	} {
		response = relayAuthenticatedRequest(handler, route.method, route.path, route.body)
		assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
		if runtime.calls != 1 {
			t.Fatalf("restricted route reached runtime: %s %s calls=%d", route.method, route.path, runtime.calls)
		}
	}

	for _, route := range []struct {
		method string
		suffix string
		body   string
	}{
		{http.MethodGet, "", ""},
		{http.MethodPost, "/recovery-proposal", `{}`},
		{http.MethodPost, "/reconcile", `{"version":1,"action":"reconcile","recoveryDigest":"` +
			strings.Repeat("a", 64) + `","recoveryApprovalDigest":"` + strings.Repeat("b", 64) + `"}`},
	} {
		response = relayAuthenticatedRequest(handler, route.method,
			"/api/v1/system/lan-gateway-rebind/"+rebindProfileOperation+route.suffix, route.body)
		assertGatewayProblem(t, response, http.StatusConflict, "lan_gateway_conflict")
		if runtime.calls != 1 {
			t.Fatalf("different operation reached runtime: %s calls=%d", route.suffix, runtime.calls)
		}
	}

	request := httptest.NewRequest(http.MethodPost, statusPath+"/recovery-proposal", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertGatewayProblem(t, response, http.StatusForbidden, "csrf_failed")
	if runtime.calls != 1 {
		t.Fatalf("missing CSRF reached runtime: calls=%d", runtime.calls)
	}

	// A terminal response never relaxes this process's recovery-only gate.
	response = relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/system/status", "")
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
	response = relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/system/lan-gateway-rebind", `{}`)
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
}

func TestLANGatewayRebindRecoveryHandlersRejectAmbiguousRequests(t *testing.T) {
	for _, fixture := range []struct {
		name, kind string
		mutate     func(*http.Request)
	}{
		{"status query", "status", func(request *http.Request) { request.URL.RawQuery = "operationId=other" }},
		{"status body", "status", func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader(`{}`))
		}},
		{"review policy", "review", func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader(`{"policy":"forward_only"}`))
		}},
		{"review actor", "review", func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader(`{"actorId":"` + rebindRequestRevision + `"}`))
		}},
		{"reconcile actor", "reconcile", func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader(`{"version":1,"action":"reconcile","recoveryDigest":"` +
				strings.Repeat("a", 64) + `","recoveryApprovalDigest":"` + strings.Repeat("b", 64) +
				`","actorId":"` + rebindRequestRevision + `"}`))
		}},
		{"reconcile direction", "reconcile", func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader(`{"version":1,"action":"rollback","recoveryDigest":"` +
				strings.Repeat("a", 64) + `","recoveryApprovalDigest":"` + strings.Repeat("b", 64) + `"}`))
		}},
		{"mismatched path", "reconcile", func(request *http.Request) {
			request.SetPathValue("operationId", "not-a-canonical-operation")
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, fixture.kind)
			fixture.mutate(request)
			response := invokeRebindRecoveryHandler(server, request, fixture.kind)
			if response.Code != http.StatusBadRequest || runtime.calls != 0 ||
				response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindRecoveryHandlersRecheckLiveSessionBeforeDisclosure(t *testing.T) {
	for _, kind := range []string{"status", "review", "reconcile"} {
		t.Run(kind, func(t *testing.T) {
			server, request, runtime, authentication := newRebindRecoveryHandlerFixture(t, kind)
			runtime.onCall = func() {
				authentication.authenticate = func(string) (auth.User, string, error) {
					return auth.User{}, "", errors.New("sensitive revoked session")
				}
			}
			response := invokeRebindRecoveryHandler(server, request, kind)
			if response.Code != http.StatusForbidden || runtime.calls != 1 ||
				strings.Contains(response.Body.String(), "sensitive") ||
				strings.Contains(response.Body.String(), "operation") {
				t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindRecoveryHandlersRefuseCancelledRequestBeforeDisclosure(t *testing.T) {
	for _, kind := range []string{"status", "review", "reconcile"} {
		t.Run(kind, func(t *testing.T) {
			server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, kind)
			requestContext, cancel := context.WithCancel(request.Context())
			request = request.WithContext(requestContext)
			runtime.onCall = cancel
			response := invokeRebindRecoveryHandler(server, request, kind)
			if response.Code != http.StatusForbidden || runtime.calls != 1 ||
				strings.Contains(response.Body.String(), "operationId") ||
				strings.Contains(response.Body.String(), "recoveryDigest") {
				t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindRecoveryHandlersDoNotTreatRepositoryNotFoundAsAbsence(t *testing.T) {
	for _, kind := range []string{"status", "review", "reconcile"} {
		t.Run(kind, func(t *testing.T) {
			server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, kind)
			runtime.err = errors.Join(appaccess.ErrNotFound, errors.New("sensitive repository path"))
			response := invokeRebindRecoveryHandler(server, request, kind)
			if response.Code != http.StatusServiceUnavailable || runtime.calls != 1 ||
				strings.Contains(response.Body.String(), "sensitive") || response.Code == http.StatusNotFound {
				t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindRecoveryHandlersRejectRuntimeCrossBinding(t *testing.T) {
	for _, kind := range []string{"status", "review", "reconcile"} {
		t.Run(kind, func(t *testing.T) {
			server, request, runtime, _ := newRebindRecoveryHandlerFixture(t, kind)
			// Every zero runtime projection is invalid for this exact operation.
			// The controller must validate it before writing any status, consent,
			// current-authority or audit-derived field.
			response := invokeRebindRecoveryHandler(server, request, kind)
			if response.Code != http.StatusServiceUnavailable || runtime.calls != 1 ||
				strings.Contains(response.Body.String(), "recoveryDigest") ||
				strings.Contains(response.Body.String(), "audit") {
				t.Fatalf("response=%d calls=%d body=%s", response.Code, runtime.calls, response.Body.String())
			}
		})
	}
}
