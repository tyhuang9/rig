package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/auth"
)

type lanRecoveryHeadServiceFake struct {
	head  LANRecoveryHead
	err   error
	calls int
}

func (f *lanRecoveryHeadServiceFake) ReadLANRecoveryHead(context.Context) (LANRecoveryHead, error) {
	f.calls++
	return f.head, f.err
}

func TestLANRecoveryHeadRequiresAuthenticatedAdministratorAndRecoveryMode(t *testing.T) {
	head := validLANRecoveryHeadFixture()
	service := &lanRecoveryHeadServiceFake{head: head}
	server := lanRecoveryControllerFixture(service, "administrator")
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/lan/recovery", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" || service.calls != 0 {
		t.Fatalf("unauthenticated recovery read: status=%d cache=%q calls=%d", response.Code,
			response.Header().Get("Cache-Control"), service.calls)
	}

	server.Auth = controllerAuthFake{user: auth.User{ID: "different-current-administrator", Role: "viewer"}}
	request = authenticatedLANRecoveryRequest()
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("viewer recovery read: status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}

	normal := lanRecoveryControllerFixture(service, "administrator")
	normal.RecoveryOnly = false
	request = authenticatedLANRecoveryRequest()
	response = httptest.NewRecorder()
	normal.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || service.calls != 0 ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("normal-mode recovery read: status=%d cache=%q calls=%d body=%s", response.Code,
			response.Header().Get("Cache-Control"), service.calls, response.Body.String())
	}
}

func TestLANRecoveryHeadReturnsOnlyExactPinnedNoStoreProjection(t *testing.T) {
	head := validLANRecoveryHeadFixture()
	service := &lanRecoveryHeadServiceFake{head: head}
	server := lanRecoveryControllerFixture(service, "administrator")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedLANRecoveryRequest())
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || service.calls != 1 {
		t.Fatalf("recovery head: status=%d cache=%q calls=%d body=%s", response.Code,
			response.Header().Get("Cache-Control"), service.calls, response.Body.String())
	}
	var got apicontract.LANRecoveryHead
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != head.Kind || got.AppID != head.AppID || got.OperationID != head.OperationID ||
		!got.Batch || got.BatchPosition != 2 || got.BatchCount != 3 ||
		got.ClaimState != head.ClaimState || got.ApprovalDigest != head.ApprovalDigest {
		t.Fatalf("unexpected recovery response: %+v", got)
	}
	var raw map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw["url"]; leaked {
		t.Fatalf("recovery response leaked a URL: %s", response.Body.String())
	}

	service.err = errors.New("protected proof failed")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedLANRecoveryRequest())
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("failed proof: status=%d cache=%q body=%s", response.Code,
			response.Header().Get("Cache-Control"), response.Body.String())
	}

	service.err = nil
	service.head.OperationID = "88888888-8888-4888-8888-888888888888"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedLANRecoveryRequest())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("mismatched static pin: status=%d body=%s", response.Code, response.Body.String())
	}
}

func lanRecoveryControllerFixture(service LANRecoveryHeadService, role string) *Server {
	return &Server{
		Auth:             controllerAuthFake{user: auth.User{ID: "different-current-administrator", Role: role}},
		LANRecoveryHeads: service, RecoveryOnly: true, RecoveryLANBatch: true,
		RecoveryBatchHead: 1, RecoveryBatchCount: 3,
		RecoveryKind:        RecoveryLANGrant,
		RecoveryOperationID: "22222222-2222-4222-8222-222222222222",
		RecoveryAppID:       "11111111-1111-4111-8111-111111111111",
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func authenticatedLANRecoveryRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/lan/recovery", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	return request
}

func validLANRecoveryHeadFixture() LANRecoveryHead {
	return LANRecoveryHead{
		Kind: RecoveryLANGrant, AppID: "11111111-1111-4111-8111-111111111111",
		OperationID: "22222222-2222-4222-8222-222222222222", Batch: true,
		BatchPosition: 2, BatchCount: 3, ClaimState: "prepared",
		AccessRevisionID: "33333333-3333-4333-8333-333333333333", AccessRevisionNumber: 4,
		AllocationID:     "44444444-4444-4444-8444-444444444444",
		OwnerOperationID: "55555555-5555-4555-8555-555555555555", Port: 8104,
		ApprovalDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}
