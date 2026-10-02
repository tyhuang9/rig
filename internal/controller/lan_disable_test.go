package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

type lanDisableRuntimeFake struct {
	operationID string
	withdrawals int
	finalizes   int
	failProof   bool
	failAck     bool
	resolved    bool
}

func (f *lanDisableRuntimeFake) observation(request generatedingress.GatewayV2LANDisableRequest,
	disposition generatedingress.GatewayV2LANDisableDisposition,
) generatedingress.GatewayV2LANDisableObservation {
	digest := strings.Repeat("b", 64)
	if disposition == generatedingress.GatewayV2LANDisableDisabled {
		digest = strings.Repeat("c", 64)
	}
	return generatedingress.GatewayV2LANDisableObservation{
		Request: request, Disposition: disposition, GatewayOperationID: f.operationID,
		ProtectedStateDigest: digest, ObservedAt: time.Now().UTC(),
	}
}

func (f *lanDisableRuntimeFake) DisableGatewayV2LAN(ctx context.Context, request generatedingress.GatewayV2LANDisableRequest,
	authorize generatedingress.GatewayV2LANDisableAuthorizer,
) (generatedingress.GatewayV2LANDisableResult, error) {
	lease, err := authorize(ctx, request)
	if err != nil {
		return generatedingress.GatewayV2LANDisableResult{}, err
	}
	defer lease.Release()
	if err := lease.Withdraw(ctx, request); err != nil {
		return generatedingress.GatewayV2LANDisableResult{}, err
	}
	f.withdrawals++
	observed := f.observation(request, generatedingress.GatewayV2LANDisableWithdrawnPending)
	return generatedingress.GatewayV2LANDisableResult{Receipt: generatedingress.GatewayV2LANDisableReceipt{
		Request: request, GatewayOperationID: observed.GatewayOperationID,
		ProtectedStateDigest: observed.ProtectedStateDigest, ObservedAt: observed.ObservedAt,
	}}, nil
}

func (f *lanDisableRuntimeFake) ObserveGatewayV2LANDisable(_ context.Context,
	request generatedingress.GatewayV2LANDisableRequest,
) (generatedingress.GatewayV2LANDisableObservation, error) {
	return f.observation(request, generatedingress.GatewayV2LANDisableDisabled), nil
}

func (f *lanDisableRuntimeFake) WithGatewayV2LANDisableFinalization(ctx context.Context,
	request generatedingress.GatewayV2LANDisableRequest,
	authorize generatedingress.GatewayV2LANDisableResolutionAuthorizer,
	resolve func(context.Context, generatedingress.GatewayV2LANDisableObservation) error,
	acknowledge func(context.Context, generatedingress.GatewayV2LANDisableObservation) error,
) error {
	f.finalizes++
	if err := authorize(ctx, request); err != nil {
		return err
	}
	if f.failProof {
		return errors.New("injected missing protected 404 proof")
	}
	disposition := generatedingress.GatewayV2LANDisableWithdrawnPending
	if f.resolved {
		disposition = generatedingress.GatewayV2LANDisableDisabled
	}
	if err := resolve(ctx, f.observation(request, disposition)); err != nil {
		return err
	}
	f.resolved = true
	if f.failAck {
		return errors.New("injected protected clear acknowledgment failure")
	}
	return acknowledge(ctx, f.observation(request, generatedingress.GatewayV2LANDisableDisabled))
}

type lanDisableBatchRuntimeFake struct {
	lanDisableRuntimeFake
	batchCalls       int
	headAdvanced     bool
	protectedCleared bool
	beforeResolve    func()
}

func (f *lanDisableBatchRuntimeFake) WithGatewayV2LANRecoveryDisableFinalization(ctx context.Context,
	request generatedingress.GatewayV2LANDisableRequest,
	resolve func(context.Context, generatedingress.GatewayV2LANDisableObservation) error,
	acknowledge func(context.Context, generatedingress.GatewayV2LANDisableObservation) error,
) error {
	f.batchCalls++
	if f.headAdvanced {
		return errors.New("injected batch head already advanced")
	}
	if f.beforeResolve != nil {
		f.beforeResolve()
	}
	resolveObservation := f.observation(request, generatedingress.GatewayV2LANDisableWithdrawnPending)
	if f.protectedCleared {
		resolveObservation.ProtectedStateDigest = strings.Repeat("c", 64)
	}
	if err := resolve(ctx, resolveObservation); err != nil {
		return err
	}
	f.protectedCleared = true
	if err := acknowledge(ctx, f.observation(request, generatedingress.GatewayV2LANDisableDisabled)); err != nil {
		return err
	}
	f.headAdvanced = true
	return nil
}

type lanDisableServiceFake struct {
	LANAppDisableService
	claimReads      int
	staleClaimRead  int
	failAckAttempts int
}

func (f *lanDisableServiceFake) AppAccessDisableClaim(ctx context.Context,
	operationID string,
) (appaccess.AppAccessDisableClaim, error) {
	claim, err := f.LANAppDisableService.AppAccessDisableClaim(ctx, operationID)
	if err != nil {
		return claim, err
	}
	f.claimReads++
	if f.staleClaimRead > 0 && f.claimReads >= f.staleClaimRead {
		claim.RequestDigest = strings.Repeat("d", 64)
	}
	return claim, nil
}

func (f *lanDisableServiceFake) AcknowledgeAppAccessDisableProtectedClear(ctx context.Context,
	operationID, gatewayOperationID, protectedStateDigest string, observedAt time.Time,
) (appaccess.AppAccessDisableProtectedClearAck, bool, error) {
	if f.failAckAttempts > 0 {
		f.failAckAttempts--
		return appaccess.AppAccessDisableProtectedClearAck{}, false,
			errors.New("injected protected clear acknowledgment failure")
	}
	return f.LANAppDisableService.AcknowledgeAppAccessDisableProtectedClear(ctx,
		operationID, gatewayOperationID, protectedStateDigest, observedAt)
}

func (f *lanAccessFixture) disableHandler(runtime LANAppDisableRuntime, recoveryOnly bool, operationID string) http.Handler {
	return f.disableHandlerWith(runtime, f.repository, controllerAuthFake{
		user: auth.User{ID: f.actorID, Role: "administrator"}}, recoveryOnly, false, operationID)
}

func (f *lanAccessFixture) disableHandlerWith(runtime LANAppDisableRuntime, service LANAppDisableService,
	authService authenticationService, recoveryOnly, recoveryBatch bool, operationID string,
) http.Handler {
	return (&Server{
		Auth: authService,
		Apps: f.applications, AppAccess: f.repository, AppDisables: service,
		LANDisableRuntime: runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
		RecoveryOnly: recoveryOnly, RecoveryLANBatch: recoveryBatch, RecoveryKind: RecoveryLANDisable,
		RecoveryOperationID: operationID, RecoveryAppID: f.appID,
	}).Handler()
}

func disableBody(t *testing.T, revision appaccess.AppAccessRevision, operationID string) string {
	t.Helper()
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(revision))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"operationId":%q,"accessRevisionId":%q,"accessRevisionNumber":%d,"allocationId":%q,"approvalDigest":%q}`,
		operationID, revision.ID, revision.RevisionNumber, revision.Allocation.ID, digest)
}

func (f *lanAccessFixture) prepareLANDisableClaim(t *testing.T, revision appaccess.AppAccessRevision,
	operationID string,
) appaccess.AppAccessDisableClaim {
	t.Helper()
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(revision))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := f.repository.ClaimAppAccessDisable(context.Background(),
		appaccess.ApproveAppAccessDisableInput{
			OperationID: operationID, ExpectedRevisionNumber: revision.RevisionNumber,
			Owner: appaccess.AllocationOwner{AllocationID: revision.Allocation.ID,
				AppID: f.appID, OperationID: revision.OperationID, AccessRevisionID: revision.ID},
			Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess,
				SpecDigest: digest, ActorID: f.actorID},
		})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

type revocableLANDisableAuth struct {
	controllerAuthFake
	revoked *bool
}

func (f revocableLANDisableAuth) Authenticate(token string) (auth.User, string, error) {
	if *f.revoked {
		return auth.User{}, "", errors.New("session revoked")
	}
	return f.controllerAuthFake.Authenticate(token)
}

func TestLANDisableBatchRecoveryFinalizesExactPinnedHeadWithoutSingularDispatch(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	f.prepareLANDisableClaim(t, revision, operationID)
	runtime := &lanDisableBatchRuntimeFake{lanDisableRuntimeFake: lanDisableRuntimeFake{operationID: uuid.NewString()}}
	handler := f.disableHandlerWith(runtime, f.repository, controllerAuthFake{
		user: auth.User{ID: f.actorID, Role: "administrator"}}, true, true, operationID)

	response := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+f.appID+"/lan-access/disables", disableBody(t, revision, operationID))
	if response.Code != http.StatusOK || strings.Contains(strings.ToLower(response.Body.String()), "url") {
		t.Fatalf("batch disable=%d %s", response.Code, response.Body.String())
	}
	claim, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	snapshot, snapshotErr := f.repository.AppAccessDisableStartupSnapshot(context.Background())
	if err != nil || snapshotErr != nil || claim.State != appaccess.AppAccessDisableCommitted ||
		claim.Proof == nil || len(snapshot.Claims) != 1 || snapshot.Claims[0].ProtectedClearAck == nil ||
		snapshot.Claims[0].ProtectedClearAck.GatewayOperationID != runtime.operationID {
		t.Fatalf("claim=%#v snapshot=%#v err=%v snapshotErr=%v", claim, snapshot, err, snapshotErr)
	}
	if runtime.batchCalls != 1 || !runtime.headAdvanced || runtime.withdrawals != 0 || runtime.finalizes != 0 {
		t.Fatalf("batch=%d advanced=%t withdrawals=%d singular=%d",
			runtime.batchCalls, runtime.headAdvanced, runtime.withdrawals, runtime.finalizes)
	}
}

func TestLANDisableBatchRecoveryRevokedAuthorizationRetainsHeadAndBlocksNextOperation(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	f.prepareLANDisableClaim(t, revision, operationID)
	revoked := false
	runtime := &lanDisableBatchRuntimeFake{lanDisableRuntimeFake: lanDisableRuntimeFake{operationID: uuid.NewString()}}
	runtime.beforeResolve = func() { revoked = true }
	handler := f.disableHandlerWith(runtime, f.repository, revocableLANDisableAuth{
		controllerAuthFake: controllerAuthFake{user: auth.User{ID: f.actorID, Role: "administrator"}},
		revoked:            &revoked,
	}, true, true, operationID)
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"

	response := relayAuthenticatedRequest(handler, http.MethodPost, path, disableBody(t, revision, operationID))
	if response.Code != http.StatusConflict || runtime.protectedCleared || runtime.headAdvanced {
		t.Fatalf("revoked batch disable=%d cleared=%t advanced=%t %s",
			response.Code, runtime.protectedCleared, runtime.headAdvanced, response.Body.String())
	}
	claim, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	if err != nil || claim.State != appaccess.AppAccessDisablePrepared {
		t.Fatalf("revoked claim=%#v err=%v", claim, err)
	}
	revoked = false
	runtime.beforeResolve = nil
	blocked := relayAuthenticatedRequest(handler, http.MethodPost, path,
		disableBody(t, revision, uuid.NewString()))
	if blocked.Code != http.StatusServiceUnavailable || runtime.batchCalls != 1 || runtime.headAdvanced ||
		!strings.Contains(blocked.Body.String(), "gateway_reconciliation_required") {
		t.Fatalf("next operation=%d calls=%d advanced=%t %s",
			blocked.Code, runtime.batchCalls, runtime.headAdvanced, blocked.Body.String())
	}
}

func TestLANDisableBatchRecoveryRejectsStaleClaimIdentityBeforeProtectedClear(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	f.prepareLANDisableClaim(t, revision, operationID)
	service := &lanDisableServiceFake{LANAppDisableService: f.repository, staleClaimRead: 2}
	runtime := &lanDisableBatchRuntimeFake{lanDisableRuntimeFake: lanDisableRuntimeFake{operationID: uuid.NewString()}}
	handler := f.disableHandlerWith(runtime, service, controllerAuthFake{
		user: auth.User{ID: f.actorID, Role: "administrator"}}, true, true, operationID)

	response := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+f.appID+"/lan-access/disables", disableBody(t, revision, operationID))
	claim, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	if response.Code != http.StatusServiceUnavailable || err != nil ||
		claim.State != appaccess.AppAccessDisablePrepared || runtime.protectedCleared || runtime.headAdvanced ||
		runtime.withdrawals != 0 || runtime.finalizes != 0 {
		t.Fatalf("stale response=%d claim=%#v err=%v cleared=%t advanced=%t withdrawals=%d singular=%d body=%s",
			response.Code, claim, err, runtime.protectedCleared, runtime.headAdvanced,
			runtime.withdrawals, runtime.finalizes, response.Body.String())
	}
}

func TestLANDisableBatchRecoveryUncertainCallbackFailureReplaysCommittedCurrentHead(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	claim := f.prepareLANDisableClaim(t, revision, operationID)
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	withdrawing, _, err := f.repository.AdvanceAppAccessDisableClaim(context.Background(), owner,
		appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.AdvanceAppAccessDisableClaim(context.Background(), owner,
		withdrawing.State, appaccess.AppAccessDisableUncertain); err != nil {
		t.Fatal(err)
	}
	service := &lanDisableServiceFake{LANAppDisableService: f.repository, failAckAttempts: 1}
	runtime := &lanDisableBatchRuntimeFake{lanDisableRuntimeFake: lanDisableRuntimeFake{operationID: uuid.NewString()}}
	handler := f.disableHandlerWith(runtime, service, controllerAuthFake{
		user: auth.User{ID: f.actorID, Role: "administrator"}}, true, true, operationID)
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"
	body := disableBody(t, revision, operationID)

	failed := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	committed, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	snapshot, snapshotErr := f.repository.AppAccessDisableStartupSnapshot(context.Background())
	if failed.Code != http.StatusServiceUnavailable || err != nil || snapshotErr != nil ||
		committed.State != appaccess.AppAccessDisableCommitted || committed.Proof == nil ||
		len(snapshot.Claims) != 1 || snapshot.Claims[0].ProtectedClearAck != nil ||
		!runtime.protectedCleared || runtime.headAdvanced {
		t.Fatalf("failed=%d claim=%#v snapshot=%#v err=%v snapshotErr=%v cleared=%t advanced=%t body=%s",
			failed.Code, committed, snapshot, err, snapshotErr,
			runtime.protectedCleared, runtime.headAdvanced, failed.Body.String())
	}
	sequence := committed.StateSequence

	replayed := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	confirmed, confirmErr := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	snapshot, snapshotErr = f.repository.AppAccessDisableStartupSnapshot(context.Background())
	if replayed.Code != http.StatusOK || strings.Contains(strings.ToLower(replayed.Body.String()), "url") ||
		confirmErr != nil || snapshotErr != nil || confirmed.StateSequence != sequence ||
		len(snapshot.Claims) != 1 || snapshot.Claims[0].ProtectedClearAck == nil ||
		!runtime.headAdvanced || runtime.batchCalls != 2 || runtime.withdrawals != 0 || runtime.finalizes != 0 {
		t.Fatalf("replay=%d claim=%#v snapshot=%#v confirmErr=%v snapshotErr=%v advanced=%t calls=%d withdrawals=%d singular=%d body=%s",
			replayed.Code, confirmed, snapshot, confirmErr, snapshotErr, runtime.headAdvanced,
			runtime.batchCalls, runtime.withdrawals, runtime.finalizes, replayed.Body.String())
	}
}

func TestLANDisableReleasesOnlyAfterProofAndReplayRetainsHistory(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"
	runtime := &lanDisableRuntimeFake{operationID: uuid.NewString(), failProof: true}
	handler := f.disableHandler(runtime, false, "")
	body := disableBody(t, revision, operationID)

	first := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing 404 proof response=%d %s", first.Code, first.Body.String())
	}
	claim, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	if err != nil || claim.State != appaccess.AppAccessDisableWithdrawing {
		t.Fatalf("pending disable claim=%#v err=%v", claim, err)
	}
	stillOwned, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil || stillOwned.Allocation.ReleasedAt != nil || stillOwned.Allocation.ID != revision.Allocation.ID {
		t.Fatalf("unproved disable released allocation: %#v err=%v", stillOwned, err)
	}

	runtime.failProof = false
	retry := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	if retry.Code != http.StatusOK || strings.Contains(strings.ToLower(retry.Body.String()), "url") {
		t.Fatalf("retry=%d %s", retry.Code, retry.Body.String())
	}
	var mutation lanDisableMutation
	if err := json.Unmarshal(retry.Body.Bytes(), &mutation); err != nil || mutation.Created ||
		mutation.Claim.State != appaccess.AppAccessDisableCommitted || mutation.Claim.ReleasedAt == nil {
		t.Fatalf("terminal mutation=%#v err=%v", mutation, err)
	}
	if _, err := f.repository.CurrentAppAccess(context.Background(), f.appID); !errors.Is(err, appaccess.ErrNotFound) {
		t.Fatalf("released application access remains current: %v", err)
	}
	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+f.appID+"/lan-access", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"availability":"local_only"`) ||
		!strings.Contains(status.Body.String(), `"expectedRevisionNumber":1`) ||
		strings.Contains(strings.ToLower(status.Body.String()), "url") {
		t.Fatalf("post-disable access=%d %s", status.Code, status.Body.String())
	}
	read := relayAuthenticatedRequest(handler, http.MethodGet, path+"/"+operationID, "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"state":"committed"`) ||
		strings.Contains(strings.ToLower(read.Body.String()), "url") {
		t.Fatalf("disable read=%d %s", read.Code, read.Body.String())
	}
	if runtime.withdrawals != 1 {
		t.Fatalf("retry repeated gateway withdrawal: %d", runtime.withdrawals)
	}
	terminalReplay := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	if terminalReplay.Code != http.StatusOK || runtime.withdrawals != 1 ||
		!strings.Contains(terminalReplay.Body.String(), `"created":false`) {
		t.Fatalf("terminal replay=%d withdrawals=%d %s", terminalReplay.Code, runtime.withdrawals, terminalReplay.Body.String())
	}
	// A later deliberate share starts a new revision. The old disable claim and
	// allocation remain immutable history; the local-only read supplies its CAS.
	newOperation := uuid.NewString()
	newReservation := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+f.appID+"/lan-access/reservations", f.reserveBody(newOperation, 1))
	if newReservation.Code != http.StatusCreated {
		t.Fatalf("new reservation after disable=%d %s", newReservation.Code, newReservation.Body.String())
	}
	var reservation lanReservationMutation
	if err := json.Unmarshal(newReservation.Body.Bytes(), &reservation); err != nil {
		t.Fatal(err)
	}
	newApproval := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+f.appID+"/lan-access/approval",
		f.approvalBody(newOperation, reservation.Allocation.ID, 1, reservation.ApprovalDigest))
	if newApproval.Code != http.StatusCreated {
		t.Fatalf("new approval after disable=%d %s", newApproval.Code, newApproval.Body.String())
	}
	newHead, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil || newHead.RevisionNumber != 2 || newHead.Allocation.ID == revision.Allocation.ID {
		t.Fatalf("new access head=%#v err=%v", newHead, err)
	}
	historicalReplay := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	if historicalReplay.Code != http.StatusOK || runtime.withdrawals != 1 ||
		!strings.Contains(historicalReplay.Body.String(), `"created":false`) ||
		!strings.Contains(historicalReplay.Body.String(), `"state":"committed"`) {
		t.Fatalf("old disable replay after new access=%d withdrawals=%d %s",
			historicalReplay.Code, runtime.withdrawals, historicalReplay.Body.String())
	}
}

func TestLANDisableCommittedWithoutClearAckStaysFencedUntilReplay(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"
	runtime := &lanDisableRuntimeFake{operationID: uuid.NewString(), failAck: true}
	handler := f.disableHandler(runtime, false, "")
	body := disableBody(t, revision, operationID)
	first := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing clear acknowledgment response=%d %s", first.Code, first.Body.String())
	}
	claim, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	if err != nil || claim.State != appaccess.AppAccessDisableCommitted {
		t.Fatalf("terminal claim after ack failure=%#v err=%v", claim, err)
	}
	if _, _, err := f.repository.ReserveAppAccess(context.Background(), appaccess.ReserveAppAccessInput{
		AppID: f.appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 1,
		GatewayProfileRevisionID:     revision.Allocation.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: revision.Allocation.GatewayProfileRevisionNumber,
	}); !errors.Is(err, appaccess.ErrConflict) {
		t.Fatalf("unacknowledged disable admitted new allocation: %v", err)
	}
	runtime.failAck = false
	retry := relayAuthenticatedRequest(handler, http.MethodPost, path, body)
	if retry.Code != http.StatusOK || runtime.withdrawals != 1 ||
		!strings.Contains(retry.Body.String(), `"state":"committed"`) {
		t.Fatalf("ack retry=%d withdrawals=%d %s", retry.Code, runtime.withdrawals, retry.Body.String())
	}
	if _, _, err := f.repository.ReserveAppAccess(context.Background(), appaccess.ReserveAppAccessInput{
		AppID: f.appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 1,
		GatewayProfileRevisionID:     revision.Allocation.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: revision.Allocation.GatewayProfileRevisionNumber,
	}); err != nil {
		t.Fatalf("acknowledged disable kept reservation fenced: %v", err)
	}
}

func TestLANDisableRequiresAdministratorCSRFAndExactApproval(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"
	runtime := &lanDisableRuntimeFake{operationID: uuid.NewString()}
	handler := f.disableHandler(runtime, false, "")
	body := disableBody(t, revision, uuid.NewString())
	viewer := (&Server{
		Auth:        controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "viewer"}},
		AppDisables: f.repository, LANDisableRuntime: runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
	}).Handler()
	assertGatewayProblem(t, relayAuthenticatedRequest(viewer, http.MethodPost, path, body), http.StatusForbidden, "lan_access_forbidden")
	assertGatewayProblem(t, httptestGatewayRequest(handler, http.MethodPost, path, body, false, false), http.StatusUnauthorized, "unauthenticated")
	assertGatewayProblem(t, httptestGatewayRequest(handler, http.MethodPost, path, body, true, false), http.StatusForbidden, "csrf_failed")
	bad := strings.Replace(body, `"approvalDigest":"`, `"approvalDigest":"0`, 1)
	assertGatewayProblem(t, relayAuthenticatedRequest(handler, http.MethodPost, path, bad), http.StatusUnprocessableEntity, "invalid_lan_access_request")
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(revision))
	if err != nil {
		t.Fatal(err)
	}
	wrongApproval := strings.Replace(body, digest, strings.Repeat("0", 64), 1)
	assertGatewayProblem(t, relayAuthenticatedRequest(handler, http.MethodPost, path, wrongApproval), http.StatusConflict, "lan_disable_approval_mismatch")
	if runtime.withdrawals != 0 {
		t.Fatalf("unauthorized disable reached gateway: %d", runtime.withdrawals)
	}
}

func TestLANDisableRecoveryAllowsNewAdministratorAfterApproverDemotion(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"
	body := disableBody(t, revision, operationID)
	runtime := &lanDisableRuntimeFake{operationID: uuid.NewString(), failProof: true}
	first := relayAuthenticatedRequest(f.disableHandler(runtime, false, ""), http.MethodPost, path, body)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial uncertain disable=%d %s", first.Code, first.Body.String())
	}
	claim, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	if err != nil || claim.State != appaccess.AppAccessDisableWithdrawing {
		t.Fatalf("pending disable=%#v err=%v", claim, err)
	}
	newAdministrator := uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'lan-disable-recovery-admin','fixture','administrator',?,?)`,
		newAdministrator, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, f.actorID); err != nil {
		t.Fatal(err)
	}
	f.actorID = newAdministrator
	runtime.failProof = false
	recovered := relayAuthenticatedRequest(f.disableHandler(runtime, true, operationID), http.MethodPost, path, body)
	if recovered.Code != http.StatusOK ||
		!strings.Contains(recovered.Body.String(), `"state":"committed"`) {
		t.Fatalf("new administrator recovery=%d %s", recovered.Code, recovered.Body.String())
	}
	retained, err := f.repository.AppAccessDisableClaim(context.Background(), operationID)
	if err != nil || retained.ApprovedBy != claim.ApprovedBy || retained.State != appaccess.AppAccessDisableCommitted {
		t.Fatalf("original approval was not retained: %#v err=%v", retained, err)
	}
}

func TestLANDisableRecoveryOnlyClearsCommittedPendingOperation(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	operationID := uuid.NewString()
	runtime := &lanDisableRuntimeFake{operationID: uuid.NewString()}
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(revision))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := f.repository.ClaimAppAccessDisable(context.Background(),
		appaccess.ApproveAppAccessDisableInput{
			OperationID: operationID, ExpectedRevisionNumber: revision.RevisionNumber,
			Owner: appaccess.AllocationOwner{AllocationID: revision.Allocation.ID,
				AppID: f.appID, OperationID: revision.OperationID, AccessRevisionID: revision.ID},
			Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess,
				SpecDigest: digest, ActorID: f.actorID},
		})
	if err != nil {
		t.Fatal(err)
	}
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	if _, _, err := f.repository.AdvanceAppAccessDisableClaim(context.Background(), owner,
		appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.ResolveAppAccessDisableClaim(context.Background(), owner,
		appaccess.AppAccessDisableWithdrawing, appaccess.AppAccessDisableCommitted,
		appaccess.AppAccessDisableProof{GatewayOperationID: runtime.operationID,
			ProtectedStateDigest: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/apps/" + f.appID + "/lan-access/disables"
	response := relayAuthenticatedRequest(f.disableHandler(runtime, true, operationID), http.MethodPost,
		path, disableBody(t, revision, operationID))
	if response.Code != http.StatusOK || !runtime.resolved ||
		!strings.Contains(response.Body.String(), `"state":"committed"`) {
		t.Fatalf("committed recovery replay=%d resolved=%t %s", response.Code, runtime.resolved, response.Body.String())
	}
}
