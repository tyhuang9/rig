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

func (f *lanAccessFixture) disableHandler(runtime *lanDisableRuntimeFake, recoveryOnly bool, operationID string) http.Handler {
	return (&Server{
		Auth: controllerAuthFake{user: auth.User{ID: f.actorID, Role: "administrator"}},
		Apps: f.applications, AppAccess: f.repository, AppDisables: f.repository,
		LANDisableRuntime: runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
		RecoveryOnly: recoveryOnly, RecoveryKind: RecoveryLANDisable,
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
