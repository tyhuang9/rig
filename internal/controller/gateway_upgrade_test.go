package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

func TestLANGatewayUpgradeCommitsWithLockedAuthorizationAndSeparatesObservation(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeCommitted
	fixture.runtime.authorizeCalls = 2
	fixture.runtime.mutate = true
	fixture.runtime.observed = generatedingress.GatewayV2OperationStatus{
		OperationID: fixture.operationID, Availability: generatedingress.GatewayV2OperationServing,
	}

	response := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST=%d cache=%q %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var mutation apicontract.LANGatewayUpgradeMutation
	if err := json.Unmarshal(response.Body.Bytes(), &mutation); err != nil {
		t.Fatal(err)
	}
	if !mutation.Created || mutation.Claim.OperationID != fixture.operationID || mutation.Claim.State != string(appaccess.GatewayProfileUpgradeCommitted) ||
		fixture.runtime.upgradeCalls != 1 || fixture.runtime.mutationCalls != 1 || fixture.service.authorizationCalls != 2 {
		t.Fatalf("mutation=%#v runtime=%#v auth calls=%d", mutation, fixture.runtime, fixture.service.authorizationCalls)
	}

	readResponse := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-upgrade", "")
	if readResponse.Code != http.StatusOK || readResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET=%d cache=%q %s", readResponse.Code, readResponse.Header().Get("Cache-Control"), readResponse.Body.String())
	}
	var read apicontract.LANGatewayUpgradeRead
	if err := json.Unmarshal(readResponse.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Proposal == nil || read.Proposal.ActionDigest != fixture.actionDigest || read.DesiredClaim == nil ||
		read.DesiredClaim.State != string(appaccess.GatewayProfileUpgradeCommitted) ||
		read.Observed.OperationID != fixture.operationID || read.Observed.Availability != string(generatedingress.GatewayV2OperationServing) {
		t.Fatalf("read=%#v", read)
	}
	if strings.Contains(strings.ToLower(readResponse.Body.String()), "url") {
		t.Fatalf("gateway upgrade status exposed a URL: %s", readResponse.Body.String())
	}

	fixture.runtime.mutate = false
	fixture.runtime.authorizeCalls = 1
	replay := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body.String())
	}
	var replayMutation apicontract.LANGatewayUpgradeMutation
	if err := json.Unmarshal(replay.Body.Bytes(), &replayMutation); err != nil {
		t.Fatal(err)
	}
	if replayMutation.Created || replayMutation.Claim.State != string(appaccess.GatewayProfileUpgradeCommitted) || fixture.runtime.upgradeCalls != 2 {
		t.Fatalf("replay=%#v runtime calls=%d", replayMutation, fixture.runtime.upgradeCalls)
	}
}

func TestLANGatewayUpgradeDurableRollbackReplayReattestsWithoutMutation(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeRolledBack
	fixture.runtime.resultErr = errors.New("sensitive preflight failure")
	fixture.runtime.authorizeCalls = 1

	response := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "sensitive") {
		t.Fatalf("rollback=%d %s", response.Code, response.Body.String())
	}
	var mutation apicontract.LANGatewayUpgradeMutation
	if err := json.Unmarshal(response.Body.Bytes(), &mutation); err != nil {
		t.Fatal(err)
	}
	if mutation.Claim.State != string(appaccess.GatewayProfileUpgradeRolledBack) || fixture.runtime.mutationCalls != 0 {
		t.Fatalf("rollback=%#v mutations=%d", mutation, fixture.runtime.mutationCalls)
	}
	if _, err := fixture.repository.CurrentGatewayProfileUpgradeClaim(context.Background()); !errors.Is(err, appaccess.ErrNotFound) {
		t.Fatalf("rolled-back claim remained current: %v", err)
	}

	upgradeCalls := fixture.runtime.upgradeCalls
	authorizationCalls := fixture.service.authorizationCalls
	replay := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	if replay.Code != http.StatusOK || fixture.runtime.upgradeCalls != upgradeCalls+1 || fixture.service.authorizationCalls != authorizationCalls+1 {
		t.Fatalf("rollback replay=%d runtime=%d auth=%d body=%s", replay.Code, fixture.runtime.upgradeCalls, fixture.service.authorizationCalls, replay.Body.String())
	}
	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeUnresolved
	fixture.runtime.resultErr = errors.New("protected rollback evidence unavailable")
	unresolved := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, unresolved, http.StatusServiceUnavailable, "lan_gateway_unavailable")
}

func TestLANGatewayUpgradeAuthorizationDenialPinsUnresolvedWithoutMutation(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	fixture.service.denyAuthorization = true
	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeCommitted
	fixture.runtime.authorizeCalls = 2
	fixture.runtime.mutate = true
	secret := "sensitive authorization detail"
	fixture.service.authorizationErr = errors.New(secret)

	response := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "lan_gateway_unavailable")
	if strings.Contains(response.Body.String(), secret) || fixture.runtime.mutationCalls != 0 {
		t.Fatalf("denial leaked or mutated: mutations=%d body=%s", fixture.runtime.mutationCalls, response.Body.String())
	}
	claim, err := fixture.repository.CurrentGatewayProfileUpgradeClaim(context.Background())
	if err != nil || claim.OperationID != fixture.operationID || claim.State != appaccess.GatewayProfileUpgradeUnresolved {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
}

func TestLANGatewayUpgradeRechecksSessionBeforeDockerMutation(t *testing.T) {
	for _, test := range []struct {
		name       string
		failAuthAt int
		failCSRFAt int
	}{
		{name: "session revoked", failAuthAt: 3},
		{name: "csrf rotated", failCSRFAt: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLANGatewayUpgradeFixture(t)
			sessionAuth := &gatewayUpgradeSessionAuth{
				controllerAuthFake: controllerAuthFake{user: auth.User{ID: fixture.actorID, Role: "administrator"}},
				failAuthAt:         test.failAuthAt,
				failCSRFAt:         test.failCSRFAt,
			}
			fixture.runtime.outcome = generatedingress.GatewayV2UpgradeCommitted
			fixture.runtime.authorizeCalls = 2
			fixture.runtime.mutate = true
			fixture.handler = (&Server{
				Auth: sessionAuth, GatewayUpgrades: fixture.service, GatewayUpgradeRuntime: fixture.runtime,
				GeneratedRuntime: true, Logger: relayTestLogger(),
			}).Handler()

			response := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
			assertGatewayProblem(t, response, http.StatusServiceUnavailable, "lan_gateway_unavailable")
			if fixture.runtime.mutationCalls != 0 {
				t.Fatalf("expired session reached mutation: %d", fixture.runtime.mutationCalls)
			}
			claim, err := fixture.repository.CurrentGatewayProfileUpgradeClaim(context.Background())
			if err != nil || claim.State != appaccess.GatewayProfileUpgradeUnresolved {
				t.Fatalf("claim=%#v err=%v", claim, err)
			}
		})
	}
}

func TestLANGatewayUpgradeUnresolvedCommittedReplayDoesNotReportSuccess(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeCommitted
	fixture.runtime.authorizeCalls = 1
	fixture.runtime.mutate = true
	first := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	if first.Code != http.StatusCreated {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}

	fixture.runtime.outcome = generatedingress.GatewayV2UpgradeUnresolved
	fixture.runtime.resultErr = errors.New("fresh observation unavailable")
	fixture.runtime.mutate = false
	replay := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, replay, http.StatusServiceUnavailable, "lan_gateway_unavailable")
	claim, err := fixture.repository.CurrentGatewayProfileUpgradeClaim(context.Background())
	if err != nil || claim.State != appaccess.GatewayProfileUpgradeCommitted {
		t.Fatalf("committed desired claim was changed: %#v %v", claim, err)
	}
}

func TestLANGatewayUpgradeReadRejectsClaimChangeAcrossObservation(t *testing.T) {
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
	fixture.runtime.observed = generatedingress.GatewayV2OperationStatus{
		OperationID: fixture.operationID, Availability: generatedingress.GatewayV2OperationUnavailable,
	}
	fixture.runtime.afterObserve = func() {
		if _, _, advanceErr := fixture.repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), gatewayUpgradeOwner(claim),
			appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeUnresolved); advanceErr != nil {
			t.Errorf("advance during observation: %v", advanceErr)
		}
	}
	response := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-upgrade", "")
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "lan_gateway_unavailable")
}

func TestLANGatewayUpgradeRejectsChangedStaleAndInvalidRequestsBeforeRuntime(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)

	stale := fixture.post(t, fixture.requestBody(fixture.operationID, uuid.NewString(), fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, stale, http.StatusConflict, "lan_gateway_conflict")
	wrongDigest := fixture.post(t, fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, strings.Repeat("0", 64)))
	assertGatewayProblem(t, wrongDigest, http.StatusConflict, "lan_gateway_approval_mismatch")
	if fixture.runtime.upgradeCalls != 0 {
		t.Fatalf("rejected request reached runtime %d times", fixture.runtime.upgradeCalls)
	}
	if _, err := fixture.repository.CurrentGatewayProfileUpgradeClaim(context.Background()); !errors.Is(err, appaccess.ErrNotFound) {
		t.Fatalf("rejected request created claim: %v", err)
	}

	invalidBodies := []string{
		`{}`,
		`null`,
		`{"operationId":null,"profileRevisionId":"` + fixture.profile.ID + `","profileRevisionNumber":1,"actionDigest":"` + fixture.actionDigest + `"}`,
		`{"operationId":"` + uuid.NewString() + `","operationId":"` + uuid.NewString() + `","profileRevisionId":"` + fixture.profile.ID + `","profileRevisionNumber":1,"actionDigest":"` + fixture.actionDigest + `"}`,
		`{"operationId":"` + uuid.NewString() + `","profileRevisionId":"` + fixture.profile.ID + `","profileRevisionNumber":1,"actionDigest":"` + fixture.actionDigest + `","actorId":"` + uuid.NewString() + `"}`,
	}
	for _, body := range invalidBodies {
		response := fixture.post(t, body)
		assertGatewayProblem(t, response, http.StatusUnprocessableEntity, "invalid_lan_gateway_request")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/lan-gateway-upgrade", strings.NewReader(fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest)))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.Header.Set("Content-Type", "text/plain")
	wrongType := httptest.NewRecorder()
	fixture.handler.ServeHTTP(wrongType, request)
	assertGatewayProblem(t, wrongType, http.StatusUnprocessableEntity, "invalid_lan_gateway_request")
}

func TestLANGatewayUpgradeRequiresGeneratedRuntimeBeforeClaim(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	handler := (&Server{
		Auth:            controllerAuthFake{user: auth.User{ID: fixture.actorID, Role: "administrator"}},
		GatewayUpgrades: fixture.repository, GeneratedRuntime: false, Logger: relayTestLogger(),
	}).Handler()
	response := relayAuthenticatedRequest(handler, http.MethodPost, "/api/v1/system/lan-gateway-upgrade",
		fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest))
	assertGatewayProblem(t, response, http.StatusConflict, "capability_unavailable")
	if _, err := fixture.repository.CurrentGatewayProfileUpgradeClaim(context.Background()); !errors.Is(err, appaccess.ErrNotFound) {
		t.Fatalf("missing runtime created claim: %v", err)
	}
}

func TestLANGatewayUpgradeReadWithoutClaimDoesNotInferUnavailable(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	response := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-upgrade", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET=%d %s", response.Code, response.Body.String())
	}
	var read apicontract.LANGatewayUpgradeRead
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.DesiredClaim != nil || read.Observed.OperationID != "" || read.Observed.Availability != string(generatedingress.GatewayV2OperationUnknown) ||
		fixture.runtime.observeCalls != 0 {
		t.Fatalf("unclaimed read inferred topology: %#v observe calls=%d", read, fixture.runtime.observeCalls)
	}
}

func TestLANGatewayUpgradeRoutesEnforceAdministratorSessionAndCSRF(t *testing.T) {
	fixture := newLANGatewayUpgradeFixture(t)
	viewer := (&Server{
		Auth:            controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "operator"}},
		GatewayUpgrades: fixture.repository, GatewayUpgradeRuntime: fixture.runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
	}).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body := ""
		if method == http.MethodPost {
			body = fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest)
		}
		response := relayAuthenticatedRequest(viewer, method, "/api/v1/system/lan-gateway-upgrade", body)
		assertGatewayProblem(t, response, http.StatusForbidden, "lan_gateway_forbidden")
	}
	unauthenticated := httptestGatewayRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-upgrade", "", false, false)
	assertGatewayProblem(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	missingCSRF := httptestGatewayRequest(fixture.handler, http.MethodPost, "/api/v1/system/lan-gateway-upgrade",
		fixture.requestBody(fixture.operationID, fixture.profile.ID, fixture.profile.RevisionNumber, fixture.actionDigest), true, false)
	assertGatewayProblem(t, missingCSRF, http.StatusForbidden, "csrf_failed")
	if fixture.runtime.upgradeCalls != 0 {
		t.Fatalf("auth failures reached runtime %d times", fixture.runtime.upgradeCalls)
	}
}

type lanGatewayUpgradeFixture struct {
	handler      http.Handler
	repository   *appaccess.Repository
	service      *gatewayUpgradeServiceSpy
	runtime      *gatewayUpgradeRuntimeFake
	actorID      string
	operationID  string
	profile      appaccess.GatewayProfileRevision
	actionDigest string
}

func newLANGatewayUpgradeFixture(t *testing.T) *lanGatewayUpgradeFixture {
	t.Helper()
	profileFixture := newLANGatewayProfileFixture(t)
	spec := appaccess.GatewayProfileSpec{
		SelectedIPv4: profileFixture.candidate.IPv4, InterfaceID: profileFixture.candidate.InterfaceID,
		PortStart: 8100, PortEnd: 8119,
	}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	configured := profileFixture.configure(t, uuid.NewString(), 0, spec, profileDigest)
	if configured.Code != http.StatusCreated {
		t.Fatalf("configure profile=%d %s", configured.Code, configured.Body.String())
	}
	profile, err := profileFixture.repository.CurrentGatewayProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actionDigest, err := gatewayUpgradeActionDigest(profile)
	if err != nil {
		t.Fatal(err)
	}
	service := &gatewayUpgradeServiceSpy{GatewayUpgradeService: profileFixture.repository}
	runtime := &gatewayUpgradeRuntimeFake{}
	fixture := &lanGatewayUpgradeFixture{
		repository: profileFixture.repository, service: service, runtime: runtime,
		actorID: profileFixture.actorID, operationID: uuid.NewString(), profile: profile, actionDigest: actionDigest,
	}
	fixture.handler = (&Server{
		Auth:            controllerAuthFake{user: auth.User{ID: fixture.actorID, Role: "administrator"}},
		GatewayUpgrades: service, GatewayUpgradeRuntime: runtime, GeneratedRuntime: true, Logger: relayTestLogger(),
	}).Handler()
	return fixture
}

func (fixture *lanGatewayUpgradeFixture) requestBody(operationID, profileID string, revision int64, digest string) string {
	return fmt.Sprintf(`{"operationId":%q,"profileRevisionId":%q,"profileRevisionNumber":%d,"actionDigest":%q}`,
		operationID, profileID, revision, digest)
}

func (fixture *lanGatewayUpgradeFixture) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return relayAuthenticatedRequest(fixture.handler, http.MethodPost, "/api/v1/system/lan-gateway-upgrade", body)
}

type gatewayUpgradeServiceSpy struct {
	GatewayUpgradeService
	authorizationCalls int
	denyAuthorization  bool
	authorizationErr   error
}

func (service *gatewayUpgradeServiceSpy) AuthorizeGatewayProfileUpgrade(ctx context.Context,
	input appaccess.GatewayProfileUpgradeAuthorizationInput,
) (appaccess.GatewayProfileUpgradeAuthorization, error) {
	service.authorizationCalls++
	if service.denyAuthorization {
		if service.authorizationErr != nil {
			return appaccess.GatewayProfileUpgradeAuthorization{}, service.authorizationErr
		}
		return appaccess.GatewayProfileUpgradeAuthorization{}, appaccess.ErrConflict
	}
	return service.GatewayUpgradeService.AuthorizeGatewayProfileUpgrade(ctx, input)
}

type gatewayUpgradeRuntimeFake struct {
	upgradeCalls   int
	mutationCalls  int
	authorizeCalls int
	observeCalls   int
	mutate         bool
	outcome        generatedingress.GatewayV2UpgradeOutcome
	resultErr      error
	observed       generatedingress.GatewayV2OperationStatus
	observeErr     error
	afterObserve   func()
}

type gatewayUpgradeSessionAuth struct {
	controllerAuthFake
	authenticateCalls int
	csrfCalls         int
	failAuthAt        int
	failCSRFAt        int
}

func (service *gatewayUpgradeSessionAuth) Authenticate(token string) (auth.User, string, error) {
	service.authenticateCalls++
	if service.failAuthAt > 0 && service.authenticateCalls >= service.failAuthAt {
		return auth.User{}, "", errors.New("session revoked")
	}
	return service.controllerAuthFake.Authenticate(token)
}

func (service *gatewayUpgradeSessionAuth) CheckCSRF(hash, token string) bool {
	service.csrfCalls++
	return (service.failCSRFAt == 0 || service.csrfCalls < service.failCSRFAt) && service.controllerAuthFake.CheckCSRF(hash, token)
}

func (runtime *gatewayUpgradeRuntimeFake) UpgradeGatewayV2(ctx context.Context, request generatedingress.GatewayV2UpgradeRequest,
	authorize generatedingress.GatewayV2UpgradeAuthorizer,
) (generatedingress.GatewayV2UpgradeResult, error) {
	runtime.upgradeCalls++
	for index := 0; index < runtime.authorizeCalls; index++ {
		if err := authorize(ctx, request); err != nil {
			return generatedingress.GatewayV2UpgradeResult{Outcome: generatedingress.GatewayV2UpgradeUnresolved}, err
		}
	}
	if runtime.mutate {
		runtime.mutationCalls++
	}
	return generatedingress.GatewayV2UpgradeResult{Outcome: runtime.outcome}, runtime.resultErr
}

func (runtime *gatewayUpgradeRuntimeFake) ObserveGatewayV2Operation(context.Context, string) (generatedingress.GatewayV2OperationStatus, error) {
	runtime.observeCalls++
	if runtime.afterObserve != nil {
		runtime.afterObserve()
	}
	return runtime.observed, runtime.observeErr
}
