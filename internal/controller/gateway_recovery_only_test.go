package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

func TestRecoveryOnlyControllerPinsOneOperationAndStaysRestrictedAfterRollback(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	claim, _, err := fixture.repository.ClaimGatewayProfileUpgrade(context.Background(), appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: fixture.operationID,
		Spec: appaccess.GatewayProfileUpgradeSpec{
			ProfileRevisionID: fixture.profile.ID, ProfileRevisionNumber: fixture.profile.RevisionNumber,
			ProfileSpecDigest: fixture.profile.SpecDigest,
		},
		Approval: appaccess.Approval{Action: appaccess.ActionUpgradeGateway, SpecDigest: fixture.actionDigest, ActorID: fixture.actorID},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler = (&Server{
		Auth:            controllerAuthFake{user: auth.User{ID: fixture.actorID, Role: "administrator"}},
		GatewayUpgrades: fixture.service, GatewayUpgradeRuntime: fixture.runtime,
		GeneratedRuntime: true, RecoveryOnly: true, RecoveryOperationID: fixture.operationID,
		Logger: relayTestLogger(),
	}).Handler()

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/system/status"},
		{http.MethodPost, "/api/v1/apps"},
		{http.MethodGet, "/api/v1/unlisted"},
	} {
		response := relayAuthenticatedRequest(fixture.handler, route.method, route.path, "")
		assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s missing no-store", route.method, route.path)
		}
	}
	if response := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/auth/me", ""); response.Code != http.StatusOK {
		t.Fatalf("authentication unavailable: %d %s", response.Code, response.Body.String())
	}

	other := uuid.NewString()
	response := fixture.post(t, fixture.requestBody(other, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
	if fixture.runtime.upgradeCalls != 0 {
		t.Fatal("different operation reached runtime")
	}
	if _, err := fixture.repository.GatewayProfileUpgradeClaim(context.Background(), other); err != appaccess.ErrNotFound {
		t.Fatalf("different operation acquired claim: %v", err)
	}

	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeRolledBack
	fixture.runtime.authorizeCalls = 1
	response = fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	if response.Code != http.StatusOK {
		t.Fatalf("reconcile=%d %s", response.Code, response.Body.String())
	}
	stored, err := fixture.repository.GatewayProfileUpgradeClaim(context.Background(), claim.OperationID)
	if err != nil || stored.State != appaccess.GatewayProfileUpgradeRolledBack {
		t.Fatalf("stored claim=%#v error=%v", stored, err)
	}
	read := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-upgrade", "")
	if read.Code != http.StatusOK {
		t.Fatalf("historical read=%d %s", read.Code, read.Body.String())
	}
	var body apicontract.LANGatewayUpgradeRead
	if err := json.Unmarshal(read.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.DesiredClaim == nil || body.DesiredClaim.OperationID != claim.OperationID || body.DesiredClaim.State != string(appaccess.GatewayProfileUpgradeRolledBack) {
		t.Fatalf("historical claim not visible: %#v", body)
	}
	response = relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/status", "")
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
	response = fixture.post(t, fixture.requestBody(other, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "gateway_reconciliation_required")
}
