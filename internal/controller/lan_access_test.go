package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/machines"
)

type lanAccessFixture struct {
	db           *sql.DB
	handler      http.Handler
	repository   *appaccess.Repository
	applications *apps.Store
	appID        string
	actorID      string
	profile      appaccess.GatewayProfileRevision
}

func newLANAccessFixture(t *testing.T) *lanAccessFixture {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	actorID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?,?,?,?)`, actorID, "lan-access-admin", "fixture", "administrator", now, now); err != nil {
		t.Fatal(err)
	}
	applications := apps.New(db)
	if _, err := machines.New(db).EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	app, err := applications.Create("LAN access fixture", "", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	repository := appaccess.New(db)
	spec := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.50.20", InterfaceID: "7/Ethernet LAN", PortStart: 8100, PortEnd: 8101}
	digest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, Spec: spec,
		Approval: appaccess.Approval{Action: appaccess.ActionConfigureGateway, SpecDigest: digest, ActorID: actorID},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &lanAccessFixture{db: db, repository: repository, applications: applications, appID: app.ID, actorID: actorID, profile: profile}
	fixture.handler = (&Server{
		Auth: controllerAuthFake{user: auth.User{ID: actorID, Role: "administrator"}},
		Apps: applications, AppAccess: repository, GeneratedRuntime: true, Logger: relayTestLogger(),
	}).Handler()
	return fixture
}

type lanGrantRuntimeFake struct {
	commit               bool
	pending              bool
	quarantined          bool
	grants               int
	operationID          string
	beforeResolution     func()
	cancelAfterGrant     func()
	beforeCommitRecovery func()
}

type failingGrantResolution struct{ *appaccess.Repository }

func (f failingGrantResolution) ResolveAppAccessGrantClaim(context.Context, appaccess.AppAccessGrantClaimOwner,
	appaccess.AppAccessGrantState, appaccess.AppAccessGrantState, appaccess.AppAccessGrantProof,
) (appaccess.AppAccessGrantClaim, bool, error) {
	return appaccess.AppAccessGrantClaim{}, false, fmt.Errorf("injected terminal database write failure")
}

func (f *lanGrantRuntimeFake) observation(request generatedingress.GatewayV2LANGrantRequest, disposition generatedingress.GatewayV2LANGrantDisposition) generatedingress.GatewayV2LANGrantObservation {
	return generatedingress.GatewayV2LANGrantObservation{
		Request: request, Disposition: disposition, GatewayOperationID: f.operationID,
		ProtectedStateDigest: strings.Repeat("a", 64), ObservedAt: time.Now().UTC(),
	}
}

func (f *lanGrantRuntimeFake) GrantGatewayV2LAN(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	authorize generatedingress.GatewayV2LANGrantAuthorizer,
) (generatedingress.GatewayV2LANGrantResult, error) {
	f.grants++
	if !f.commit {
		return generatedingress.GatewayV2LANGrantResult{}, fmt.Errorf("injected pre-publication failure")
	}
	lease, err := authorize(ctx, request)
	if err != nil {
		return generatedingress.GatewayV2LANGrantResult{}, err
	}
	defer lease.Release()
	if err := lease.Revalidate(ctx, request); err != nil {
		return generatedingress.GatewayV2LANGrantResult{}, err
	}
	if err := lease.Activate(ctx, request); err != nil {
		return generatedingress.GatewayV2LANGrantResult{}, err
	}
	if f.pending {
		return generatedingress.GatewayV2LANGrantResult{}, fmt.Errorf("injected ambiguous protected commit failure")
	}
	observed := f.observation(request, generatedingress.GatewayV2LANGrantCommitted)
	if f.cancelAfterGrant != nil {
		f.cancelAfterGrant()
	}
	return generatedingress.GatewayV2LANGrantResult{Receipt: generatedingress.GatewayV2LANGrantReceipt{
		Request: request, GatewayOperationID: observed.GatewayOperationID,
		ProtectedStateDigest: observed.ProtectedStateDigest, ObservedAt: observed.ObservedAt,
	}}, nil
}

func (f *lanGrantRuntimeFake) ObserveGatewayV2LAN(_ context.Context, request generatedingress.GatewayV2LANGrantRequest) (generatedingress.GatewayV2LANGrantObservation, error) {
	if !f.commit {
		return generatedingress.GatewayV2LANGrantObservation{}, fmt.Errorf("no grant artifact")
	}
	if f.quarantined {
		return f.observation(request, generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation), nil
	}
	if f.pending {
		return f.observation(request, generatedingress.GatewayV2LANGrantPendingPublished), nil
	}
	return f.observation(request, generatedingress.GatewayV2LANGrantCommitted), nil
}

func (f *lanGrantRuntimeFake) WithGatewayV2LANObservation(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	observed, err := f.ObserveGatewayV2LAN(ctx, request)
	if err != nil {
		return err
	}
	return fn(ctx, observed)
}

func (f *lanGrantRuntimeFake) WithGatewayV2LANResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	if f.beforeResolution != nil {
		f.beforeResolution()
	}
	return fn(ctx, f.observation(request, generatedingress.GatewayV2LANGrantCommitted))
}

func (f *lanGrantRuntimeFake) WithGatewayV2LANCommitResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantReceipt) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.beforeResolution != nil {
		f.beforeResolution()
	}
	observed := f.observation(request, generatedingress.GatewayV2LANGrantCommitted)
	err := fn(ctx, generatedingress.GatewayV2LANGrantReceipt{
		Request: request, GatewayOperationID: observed.GatewayOperationID,
		ProtectedStateDigest: observed.ProtectedStateDigest, ObservedAt: observed.ObservedAt,
	})
	if err != nil {
		f.quarantined = true
	}
	return err
}

func TestLANAppGrantFinishesTerminalReconciliationAfterClientDisconnect(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	requestCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString(), cancelAfterGrant: disconnect}
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	attemptID := uuid.NewString()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(grantBody(revision, attemptID))).WithContext(requestCtx)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.grantHandler(runtime, false, "").ServeHTTP(response, request)
	claim, err := f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State != appaccess.AppAccessGrantCommitted || response.Code != http.StatusCreated {
		t.Fatalf("response=%d claim=%#v err=%v", response.Code, claim, err)
	}
}

func (f *lanGrantRuntimeFake) WithGatewayV2LANCommitRecovery(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	if f.beforeCommitRecovery != nil {
		f.beforeCommitRecovery()
	}
	if err := fn(ctx, f.observation(request, generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation)); err != nil {
		return err
	}
	f.quarantined = false
	return nil
}

type revocableLANGrantAuth struct {
	controllerAuthFake
	revoked *bool
}

func (f revocableLANGrantAuth) Authenticate(token string) (auth.User, string, error) {
	if *f.revoked {
		return auth.User{}, "", fmt.Errorf("session revoked")
	}
	return f.controllerAuthFake.Authenticate(token)
}

func TestLANAppGrantRecoveryRequiresCurrentActorSessionBeforeRepublish(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}
	attemptID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	created := relayAuthenticatedRequest(f.grantHandler(runtime, false, ""), http.MethodPost, path, grantBody(revision, attemptID))
	if created.Code != http.StatusCreated {
		t.Fatalf("initial grant=%d %s", created.Code, created.Body.String())
	}
	runtime.quarantined = true
	revoked := false
	runtime.beforeCommitRecovery = func() { revoked = true }
	recovery := (&Server{
		Auth: revocableLANGrantAuth{controllerAuthFake: controllerAuthFake{
			user: auth.User{ID: f.actorID, Role: "administrator"}}, revoked: &revoked},
		Apps: f.applications, AppAccess: f.repository, AppGrants: f.repository,
		LANGrantRuntime: runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
		RecoveryOnly: true, RecoveryKind: RecoveryLANGrant,
		RecoveryOperationID: attemptID, RecoveryAppID: f.appID,
	}).Handler()
	response := relayAuthenticatedRequest(recovery, http.MethodPost, path, grantBody(revision, attemptID))
	if response.Code == http.StatusOK || response.Code == http.StatusCreated || !runtime.quarantined {
		t.Fatalf("revoked recovery=%d quarantined=%t body=%s", response.Code, runtime.quarantined, response.Body.String())
	}
}

func (f *lanGrantRuntimeFake) WithGatewayV2LANAbsenceResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	return fn(ctx, f.observation(request, generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation))
}

func (f *lanGrantRuntimeFake) WithGatewayV2LANRollbackResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	if err := fn(ctx, f.observation(request, generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation)); err != nil {
		return err
	}
	f.quarantined = false
	return nil
}

func (f *lanGrantRuntimeFake) RecoverGatewayV2LAN(context.Context, generatedingress.GatewayV2LANGrantRequest) (generatedingress.GatewayV2LANGrantRecovery, error) {
	return generatedingress.GatewayV2LANGrantRecovery{}, fmt.Errorf("standalone recovery must not be called")
}

func (f *lanAccessFixture) reserveBody(operationID string, expected int64) string {
	return fmt.Sprintf(`{"operationId":%q,"expectedRevisionNumber":%d,"gatewayProfileRevisionId":%q,"gatewayProfileRevisionNumber":%d}`,
		operationID, expected, f.profile.ID, f.profile.RevisionNumber)
}

func (f *lanAccessFixture) approvalBody(operationID, allocationID string, expected int64, digest string) string {
	return fmt.Sprintf(`{"operationId":%q,"allocationId":%q,"expectedRevisionNumber":%d,"approvalDigest":%q}`,
		operationID, allocationID, expected, digest)
}

func TestLANAppAccessReserveReviewApproveAndStatusWithholdURL(t *testing.T) {
	f := newLANAccessFixture(t)
	path := "/api/v1/apps/" + f.appID + "/lan-access"
	initial := relayAuthenticatedRequest(f.handler, http.MethodGet, path, "")
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"availability":"local_only"`) {
		t.Fatalf("initial=%d %s", initial.Code, initial.Body.String())
	}
	operationID := uuid.NewString()
	reserved := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", f.reserveBody(operationID, 0))
	if reserved.Code != http.StatusCreated || reserved.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reserve=%d cache=%q %s", reserved.Code, reserved.Header().Get("Cache-Control"), reserved.Body.String())
	}
	var reservation lanReservationMutation
	if err := json.Unmarshal(reserved.Body.Bytes(), &reservation); err != nil {
		t.Fatal(err)
	}
	if reservation.Allocation.AppID != f.appID || reservation.Allocation.Port != 8100 || reservation.Allocation.State != appaccess.AllocationReserved ||
		reservation.Allocation.OwnerOperationID != operationID || !reservation.Created {
		t.Fatalf("reservation=%#v", reservation)
	}
	wantDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: f.appID, AllocationID: reservation.Allocation.ID, Port: reservation.Allocation.Port,
		GatewayProfileRevisionID: f.profile.ID, GatewayProfileRevisionNumber: f.profile.RevisionNumber,
	})
	if err != nil || reservation.ApprovalDigest != wantDigest {
		t.Fatalf("review digest=%q want=%q err=%v", reservation.ApprovalDigest, wantDigest, err)
	}
	if strings.Contains(strings.ToLower(reserved.Body.String()), "url") {
		t.Fatalf("reservation exposed URL: %s", reserved.Body.String())
	}
	replayed := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", f.reserveBody(operationID, 0))
	var replay lanReservationMutation
	if err := json.Unmarshal(replayed.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replayed.Code != http.StatusOK || replay.Created || replay.Allocation.ID != reservation.Allocation.ID {
		t.Fatalf("replay=%d %#v", replayed.Code, replay)
	}

	approved := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/approval", f.approvalBody(operationID, reservation.Allocation.ID, 0, wantDigest))
	if approved.Code != http.StatusCreated {
		t.Fatalf("approve=%d %s", approved.Code, approved.Body.String())
	}
	var approval lanApprovalMutation
	if err := json.Unmarshal(approved.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	if !approval.Created || approval.Revision.ApprovedBy != f.actorID || approval.Revision.RevisionNumber != 1 ||
		approval.Revision.SpecDigest != wantDigest || approval.Revision.Allocation.ID != reservation.Allocation.ID ||
		approval.Revision.Allocation.State != appaccess.AllocationReserved {
		t.Fatalf("approval=%#v", approval)
	}
	status := relayAuthenticatedRequest(f.handler, http.MethodGet, path, "")
	var read lanAccessRead
	if err := json.Unmarshal(status.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if status.Code != http.StatusOK || read.ExpectedRevisionNumber != 1 || read.DesiredAccess == nil || read.Availability != "unverified" ||
		strings.Contains(strings.ToLower(status.Body.String()), "url") {
		t.Fatalf("status=%d %#v body=%s", status.Code, read, status.Body.String())
	}
	approvedReplay := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/approval", f.approvalBody(operationID, reservation.Allocation.ID, 0, wantDigest))
	if approvedReplay.Code != http.StatusOK || !strings.Contains(approvedReplay.Body.String(), `"created":false`) {
		t.Fatalf("approval replay=%d %s", approvedReplay.Code, approvedReplay.Body.String())
	}
}

func TestLANAppAccessRejectsUnauthorizedMalformedAndStaleActions(t *testing.T) {
	f := newLANAccessFixture(t)
	path := "/api/v1/apps/" + f.appID + "/lan-access"
	operationID := uuid.NewString()
	viewer := (&Server{
		Auth:      controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "viewer"}},
		AppAccess: f.repository, GeneratedRuntime: true, Logger: relayTestLogger(),
	}).Handler()
	assertGatewayProblem(t, relayAuthenticatedRequest(viewer, http.MethodGet, path, ""), http.StatusForbidden, "lan_access_forbidden")
	for _, endpoint := range []string{path + "/reservations", path + "/approval"} {
		response := relayAuthenticatedRequest(viewer, http.MethodPost, endpoint, `{}`)
		assertGatewayProblem(t, response, http.StatusForbidden, "lan_access_forbidden")
		response = httptestGatewayRequest(f.handler, http.MethodPost, endpoint, `{}`, false, false)
		assertGatewayProblem(t, response, http.StatusUnauthorized, "unauthenticated")
		response = httptestGatewayRequest(f.handler, http.MethodPost, endpoint, `{}`, true, false)
		assertGatewayProblem(t, response, http.StatusForbidden, "csrf_failed")
	}
	for _, body := range []string{
		`{}`, `{"operationId":"` + operationID + `","expectedRevisionNumber":0,"gatewayProfileRevisionId":"` + f.profile.ID + `","gatewayProfileRevisionNumber":1,"actorId":"attacker"}`,
		`{"operationId":"` + operationID + `","operationId":"` + operationID + `","expectedRevisionNumber":0,"gatewayProfileRevisionId":"` + f.profile.ID + `","gatewayProfileRevisionNumber":1}`,
	} {
		response := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", body)
		assertGatewayProblem(t, response, http.StatusUnprocessableEntity, "invalid_lan_access_request")
	}
	stale := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", f.reserveBody(operationID, 1))
	assertGatewayProblem(t, stale, http.StatusConflict, "lan_access_conflict")
	reserved := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", f.reserveBody(operationID, 0))
	if reserved.Code != http.StatusCreated {
		t.Fatalf("reserve=%d %s", reserved.Code, reserved.Body.String())
	}
	var reservation lanReservationMutation
	if err := json.Unmarshal(reserved.Body.Bytes(), &reservation); err != nil {
		t.Fatal(err)
	}
	wrong := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/approval",
		f.approvalBody(operationID, reservation.Allocation.ID, 0, strings.Repeat("0", 64)))
	assertGatewayProblem(t, wrong, http.StatusConflict, "lan_access_approval_mismatch")
	changedReplay := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", f.reserveBody(operationID, 1))
	assertGatewayProblem(t, changedReplay, http.StatusConflict, "lan_access_replay_conflict")
	status := relayAuthenticatedRequest(f.handler, http.MethodGet, path, "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"availability":"local_only"`) {
		t.Fatalf("rejected actions changed approved head: %d %s", status.Code, status.Body.String())
	}
}

func (f *lanAccessFixture) approveForGrant(t *testing.T) appaccess.AppAccessRevision {
	t.Helper()
	path := "/api/v1/apps/" + f.appID + "/lan-access"
	operationID := uuid.NewString()
	reserved := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/reservations", f.reserveBody(operationID, 0))
	if reserved.Code != http.StatusCreated {
		t.Fatalf("reserve=%d %s", reserved.Code, reserved.Body.String())
	}
	var reservation lanReservationMutation
	if err := json.Unmarshal(reserved.Body.Bytes(), &reservation); err != nil {
		t.Fatal(err)
	}
	approved := relayAuthenticatedRequest(f.handler, http.MethodPost, path+"/approval",
		f.approvalBody(operationID, reservation.Allocation.ID, 0, reservation.ApprovalDigest))
	if approved.Code != http.StatusCreated {
		t.Fatalf("approve=%d %s", approved.Code, approved.Body.String())
	}
	revision, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func (f *lanAccessFixture) grantHandler(runtime *lanGrantRuntimeFake, recoveryOnly bool, attemptID string) http.Handler {
	return f.grantHandlerWithService(runtime, f.repository, recoveryOnly, attemptID)
}

func (f *lanAccessFixture) grantHandlerWithService(runtime *lanGrantRuntimeFake, grants LANAppGrantService, recoveryOnly bool, attemptID string) http.Handler {
	return (&Server{
		Auth: controllerAuthFake{user: auth.User{ID: f.actorID, Role: "administrator"}},
		Apps: f.applications, AppAccess: f.repository, AppGrants: grants,
		LANGrantRuntime: runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
		RecoveryOnly: recoveryOnly, RecoveryKind: RecoveryLANGrant,
		RecoveryOperationID: attemptID, RecoveryAppID: f.appID,
	}).Handler()
}

func grantBody(revision appaccess.AppAccessRevision, attemptID string) string {
	return fmt.Sprintf(`{"attemptId":%q,"accessRevisionId":%q,"accessRevisionNumber":%d,"approvalDigest":%q}`,
		attemptID, revision.ID, revision.RevisionNumber, revision.SpecDigest)
}

func TestLANAppGrantCommitReplayAndWithholdUnprovenURL(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}
	handler := f.grantHandler(runtime, false, "")
	attemptID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	response := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, attemptID))
	if response.Code != http.StatusCreated || strings.Contains(strings.ToLower(response.Body.String()), "url") {
		t.Fatalf("grant=%d %s", response.Code, response.Body.String())
	}
	claim, err := f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State != appaccess.AppAccessGrantCommitted || claim.Proof == nil {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	current, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil || current.Allocation.State != appaccess.AllocationActive {
		t.Fatalf("allocation=%#v err=%v", current.Allocation, err)
	}
	replay := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, attemptID))
	if replay.Code != http.StatusOK || runtime.grants != 1 || !strings.Contains(replay.Body.String(), `"created":false`) {
		t.Fatalf("replay=%d grants=%d %s", replay.Code, runtime.grants, replay.Body.String())
	}
	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+f.appID+"/lan-access", "")
	if status.Code != http.StatusOK || strings.Contains(strings.ToLower(status.Body.String()), "url") ||
		!strings.Contains(status.Body.String(), `"availability":"unverified"`) {
		t.Fatalf("unproven URL status=%d %s", status.Code, status.Body.String())
	}
}

func TestLANAppGrantPrePublicationFailureRollsBackAndRecoveryPinsAttempt(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{operationID: uuid.NewString()}
	handler := f.grantHandler(runtime, false, "")
	attemptID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	response := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, attemptID))
	assertGatewayProblem(t, response, http.StatusConflict, "lan_access_conflict")
	claim, err := f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State != appaccess.AppAccessGrantRolledBack {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	current, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil || current.Allocation.State != appaccess.AllocationReserved {
		t.Fatalf("allocation=%#v err=%v", current.Allocation, err)
	}

	preparedID := uuid.NewString()
	prepared, _, err := f.repository.ClaimAppAccessGrant(context.Background(), appaccess.ClaimAppAccessGrantInput{
		AttemptID: preparedID, Spec: appaccess.AppAccessGrantSpecFor(current, f.profile), ActorID: current.ApprovedBy,
	})
	if err != nil || prepared.State != appaccess.AppAccessGrantPrepared {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	recovery := f.grantHandler(runtime, true, preparedID)
	wrong := relayAuthenticatedRequest(recovery, http.MethodPost, path, grantBody(revision, uuid.NewString()))
	assertGatewayProblem(t, wrong, http.StatusServiceUnavailable, "gateway_reconciliation_required")
	blocked := relayAuthenticatedRequest(recovery, http.MethodGet, "/api/v1/system/status", "")
	assertGatewayProblem(t, blocked, http.StatusServiceUnavailable, "gateway_reconciliation_required")
	rolled := relayAuthenticatedRequest(recovery, http.MethodPost, path, grantBody(revision, preparedID))
	assertGatewayProblem(t, rolled, http.StatusConflict, "lan_access_conflict")
	if runtime.grants != 1 {
		t.Fatalf("recovery initiated grant: %d calls", runtime.grants)
	}
	confirmed, err := f.repository.AppAccessGrantClaim(context.Background(), preparedID)
	if err != nil || confirmed.State != appaccess.AppAccessGrantRolledBack {
		t.Fatalf("recovered=%#v err=%v", confirmed, err)
	}
}

func TestLANAppGrantActivationUncertaintyWithdrawsAndRetainsPort(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, pending: true, operationID: uuid.NewString()}
	handler := f.grantHandler(runtime, false, "")
	attemptID := uuid.NewString()
	response := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+f.appID+"/lan-access/grants", grantBody(revision, attemptID))
	assertGatewayProblem(t, response, http.StatusConflict, "lan_access_conflict")
	claim, err := f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State != appaccess.AppAccessGrantRolledBack || claim.Proof == nil {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	current, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil || current.Allocation.State != appaccess.AllocationUncertain || current.Allocation.Port != revision.Allocation.Port {
		t.Fatalf("uncertain allocation=%#v err=%v", current.Allocation, err)
	}
	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+f.appID+"/lan-access", "")
	if status.Code != http.StatusOK || strings.Contains(strings.ToLower(status.Body.String()), "url") {
		t.Fatalf("uncertain status=%d %s", status.Code, status.Body.String())
	}
}

func TestLANAppGrantCommittedReplayRejectsLaterDisableIntent(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}
	handler := f.grantHandler(runtime, false, "")
	attemptID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	first := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, attemptID))
	if first.Code != http.StatusCreated {
		t.Fatalf("grant=%d %s", first.Code, first.Body.String())
	}
	current, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(current))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.repository.ApproveAppAccessDisable(context.Background(), appaccess.ApproveAppAccessDisableInput{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: current.RevisionNumber,
		Owner: appaccess.AllocationOwner{AllocationID: current.Allocation.ID, AppID: current.AppID,
			OperationID: current.OperationID, AccessRevisionID: current.ID},
		Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess, SpecDigest: digest, ActorID: f.actorID},
	})
	if err != nil {
		t.Fatal(err)
	}
	replay := relayAuthenticatedRequest(handler, http.MethodPost, path, grantBody(revision, attemptID))
	assertGatewayProblem(t, replay, http.StatusConflict, "lan_access_conflict")
	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+f.appID+"/lan-access", "")
	if status.Code != http.StatusOK || strings.Contains(strings.ToLower(status.Body.String()), "url") {
		t.Fatalf("disabled URL status=%d %s", status.Code, status.Body.String())
	}
}

func TestLANAppGrantRejectsDemotedApproverBeforeTerminalCommit(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}
	runtime.beforeResolution = func() {
		if _, err := f.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, f.actorID); err != nil {
			t.Fatal(err)
		}
	}
	handler := f.grantHandler(runtime, false, "")
	attemptID := uuid.NewString()
	response := relayAuthenticatedRequest(handler, http.MethodPost,
		"/api/v1/apps/"+f.appID+"/lan-access/grants", grantBody(revision, attemptID))
	assertGatewayProblem(t, response, http.StatusConflict, "lan_access_approval_mismatch")
	if !runtime.quarantined {
		t.Fatal("terminal authorization failure left the test LAN binding published")
	}
	claim, err := f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State == appaccess.AppAccessGrantCommitted {
		t.Fatalf("demoted approval committed: %#v err=%v", claim, err)
	}
	status := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/apps/"+f.appID+"/lan-access", "")
	if status.Code != http.StatusOK || strings.Contains(strings.ToLower(status.Body.String()), "url") {
		t.Fatalf("demoted approval exposed URL: %d %s", status.Code, status.Body.String())
	}
}

func TestLANAppGrantTerminalDBFailureQuarantinesThenRecovers404(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}
	attemptID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	failing := f.grantHandlerWithService(runtime, failingGrantResolution{f.repository}, false, "")
	response := relayAuthenticatedRequest(failing, http.MethodPost, path, grantBody(revision, attemptID))
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "lan_access_unavailable")
	if !runtime.quarantined {
		t.Fatal("failed terminal database write left gateway published")
	}
	claim, err := f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State != appaccess.AppAccessGrantDBActive {
		t.Fatalf("unresolved claim=%#v err=%v", claim, err)
	}
	status := relayAuthenticatedRequest(f.grantHandler(runtime, false, ""), http.MethodGet,
		"/api/v1/apps/"+f.appID+"/lan-access", "")
	if status.Code != http.StatusOK || strings.Contains(strings.ToLower(status.Body.String()), "url") {
		t.Fatalf("quarantined status=%d %s", status.Code, status.Body.String())
	}
	recovery := f.grantHandler(runtime, true, attemptID)
	rolled := relayAuthenticatedRequest(recovery, http.MethodPost, path, grantBody(revision, attemptID))
	assertGatewayProblem(t, rolled, http.StatusConflict, "lan_access_conflict")
	if runtime.quarantined {
		t.Fatal("reconciled rollback retained test quarantine")
	}
	claim, err = f.repository.AppAccessGrantClaim(context.Background(), attemptID)
	if err != nil || claim.State != appaccess.AppAccessGrantRolledBack {
		t.Fatalf("recovered claim=%#v err=%v", claim, err)
	}
	current, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil || current.Allocation.State != appaccess.AllocationUncertain {
		t.Fatalf("allocation after uncertain rollback=%#v err=%v", current.Allocation, err)
	}
}

func TestLANAppGrantRecoveryDoesNotRepublishAfterDisableIntent(t *testing.T) {
	f := newLANAccessFixture(t)
	revision := f.approveForGrant(t)
	runtime := &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}
	attemptID := uuid.NewString()
	path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
	created := relayAuthenticatedRequest(f.grantHandler(runtime, false, ""), http.MethodPost, path, grantBody(revision, attemptID))
	if created.Code != http.StatusCreated {
		t.Fatalf("grant=%d %s", created.Code, created.Body.String())
	}
	current, err := f.repository.CurrentAppAccess(context.Background(), f.appID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(current))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.repository.ApproveAppAccessDisable(context.Background(), appaccess.ApproveAppAccessDisableInput{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: current.RevisionNumber,
		Owner: appaccess.AllocationOwner{AllocationID: current.Allocation.ID, AppID: current.AppID,
			OperationID: current.OperationID, AccessRevisionID: current.ID},
		Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess, SpecDigest: digest, ActorID: f.actorID},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.quarantined = true
	recovered := relayAuthenticatedRequest(f.grantHandler(runtime, true, attemptID), http.MethodPost, path, grantBody(revision, attemptID))
	assertGatewayProblem(t, recovered, http.StatusConflict, "lan_access_conflict")
	if !runtime.quarantined {
		t.Fatal("disable intent allowed protected LAN binding to be republished")
	}
}

func TestLANAppGrantOperationNamesRemainAuditable(t *testing.T) {
	for _, operation := range []string{operationGetApplicationLANGrant, operationGrantApplicationLANAccess} {
		if got := safeHandlerOperation(operation); got != operation {
			t.Fatalf("operation %q logged as %q", operation, got)
		}
	}
}
