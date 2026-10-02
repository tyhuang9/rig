package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

type lanBatchGrantRuntimeFake struct {
	*lanGrantRuntimeFake
	beforeResolve func()
	calls         int
	finished      bool
}

func (f *lanBatchGrantRuntimeFake) WithGatewayV2LANRecoveryGrantFinalization(ctx context.Context,
	request generatedingress.GatewayV2LANGrantRequest,
	resolve func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	if f.finished {
		return fmt.Errorf("batch head already advanced")
	}
	f.calls++
	if f.beforeResolve != nil {
		f.beforeResolve()
	}
	if err := resolve(ctx, f.observation(request,
		generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation)); err != nil {
		return err
	}
	f.finished = true
	return nil
}

func batchGrantPreparedClaim(t *testing.T, fixture *lanAccessFixture) (appaccess.AppAccessRevision, appaccess.AppAccessGrantClaim) {
	t.Helper()
	revision := fixture.approveForGrant(t)
	claim, _, err := fixture.repository.ClaimAppAccessGrant(context.Background(), appaccess.ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, fixture.profile),
		ActorID: fixture.actorID,
	})
	if err != nil || claim.State != appaccess.AppAccessGrantPrepared {
		t.Fatalf("prepared grant=%#v err=%v", claim, err)
	}
	return revision, claim
}

func batchGrantHandler(fixture *lanAccessFixture, service LANAppGrantService, runtime *lanBatchGrantRuntimeFake,
	authentication authenticationService, attemptID string,
) http.Handler {
	return (&Server{
		Auth: authentication, Apps: fixture.applications, AppAccess: fixture.repository,
		AppGrants: service, LANGrantRuntime: runtime, GeneratedRuntime: true,
		RecoveryOnly: true, RecoveryLANBatch: true, RecoveryKind: RecoveryLANGrant,
		RecoveryOperationID: attemptID, RecoveryAppID: fixture.appID, Logger: relayTestLogger(),
	}).Handler()
}

func TestLANBatchGrantRollsBackPinnedHeadAndStaysRestricted(t *testing.T) {
	fixture := newLANAccessFixture(t)
	revision, claim := batchGrantPreparedClaim(t, fixture)
	runtime := &lanBatchGrantRuntimeFake{lanGrantRuntimeFake: &lanGrantRuntimeFake{operationID: uuid.NewString()}}
	handler := batchGrantHandler(fixture, fixture.repository, runtime,
		controllerAuthFake{user: auth.User{ID: fixture.actorID, Role: "administrator"}}, claim.AttemptID)
	path := "/api/v1/apps/" + fixture.appID + "/lan-access/grants"
	response := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, claim.AttemptID))
	if response.Code != http.StatusOK || runtime.calls != 1 || runtime.grants != 0 ||
		!strings.Contains(response.Body.String(), `"state":"rolled_back"`) ||
		strings.Contains(strings.ToLower(response.Body.String()), "url") {
		t.Fatalf("response=%d calls=%d grants=%d body=%s", response.Code, runtime.calls, runtime.grants, response.Body.String())
	}
	stored, err := fixture.repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || stored.State != appaccess.AppAccessGrantRolledBack || stored.Proof == nil ||
		stored.Proof.GatewayOperationID != runtime.operationID {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	other := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, uuid.NewString()))
	assertGatewayProblem(t, other, http.StatusServiceUnavailable, "gateway_reconciliation_required")
	replay := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, claim.AttemptID))
	if replay.Code == http.StatusOK || runtime.calls != 1 {
		t.Fatalf("advanced head replay=%d calls=%d body=%s", replay.Code, runtime.calls, replay.Body.String())
	}
	blocked := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/system/status", "")
	assertGatewayProblem(t, blocked, http.StatusServiceUnavailable, "gateway_reconciliation_required")
}

func TestLANBatchGrantRevokedActorRetainsPreparedClaim(t *testing.T) {
	fixture := newLANAccessFixture(t)
	revision, claim := batchGrantPreparedClaim(t, fixture)
	revoked := false
	runtime := &lanBatchGrantRuntimeFake{lanGrantRuntimeFake: &lanGrantRuntimeFake{operationID: uuid.NewString()},
		beforeResolve: func() { revoked = true }}
	handler := batchGrantHandler(fixture, fixture.repository, runtime,
		revocableLANGrantAuth{controllerAuthFake: controllerAuthFake{
			user: auth.User{ID: fixture.actorID, Role: "administrator"}}, revoked: &revoked}, claim.AttemptID)
	response := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+fixture.appID+"/lan-access/grants", grantBody(revision, claim.AttemptID))
	if response.Code == http.StatusOK || response.Code == http.StatusCreated || runtime.finished {
		t.Fatalf("revoked response=%d finished=%t body=%s", response.Code, runtime.finished, response.Body.String())
	}
	stored, err := fixture.repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || stored.State != appaccess.AppAccessGrantPrepared || stored.Proof != nil {
		t.Fatalf("revoked stored=%#v err=%v", stored, err)
	}
}

func TestLANBatchGrantTerminalWriteFailureRetainsHead(t *testing.T) {
	fixture := newLANAccessFixture(t)
	revision, claim := batchGrantPreparedClaim(t, fixture)
	runtime := &lanBatchGrantRuntimeFake{lanGrantRuntimeFake: &lanGrantRuntimeFake{operationID: uuid.NewString()}}
	handler := batchGrantHandler(fixture, failingGrantResolution{fixture.repository}, runtime,
		controllerAuthFake{user: auth.User{ID: fixture.actorID, Role: "administrator"}}, claim.AttemptID)
	response := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+fixture.appID+"/lan-access/grants", grantBody(revision, claim.AttemptID))
	if response.Code == http.StatusOK || response.Code == http.StatusCreated || runtime.finished || runtime.calls != 1 {
		t.Fatalf("failed response=%d calls=%d finished=%t body=%s", response.Code, runtime.calls, runtime.finished, response.Body.String())
	}
	stored, err := fixture.repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || stored.State != appaccess.AppAccessGrantPrepared {
		t.Fatalf("failed stored=%#v err=%v", stored, err)
	}
}
