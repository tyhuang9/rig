package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/hostnetwork"
)

func TestLANGatewayProfileReadReturnsDesiredStateCandidatesAndExactProposal(t *testing.T) {
	fixture := newLANGatewayProfileFixture(t)
	query := url.Values{
		"interfaceId":  {fixture.candidate.InterfaceID},
		"selectedIpv4": {fixture.candidate.IPv4},
		"portStart":    {"8102"},
		"portEnd":      {"8110"},
	}
	response := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-profile?"+query.Encode(), "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET=%d cache=%q %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var read apicontract.LANGatewayProfileRead
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.ExpectedRevisionNumber != 0 || read.DesiredProfile != nil || len(read.Candidates) != 1 || read.Proposal == nil {
		t.Fatalf("unexpected read model: %#v", read)
	}
	wantSpec := appaccess.GatewayProfileSpec{SelectedIPv4: fixture.candidate.IPv4, InterfaceID: fixture.candidate.InterfaceID, PortStart: 8102, PortEnd: 8110}
	wantDigest, err := appaccess.GatewayProfileSpecDigest(wantSpec)
	if err != nil {
		t.Fatal(err)
	}
	if read.Proposal.ApprovalDigest != wantDigest || read.Proposal.Spec.PortStart != 8102 || read.Proposal.Spec.PortEnd != 8110 || read.Candidates[0].Prefix != fixture.candidate.Prefix.String() {
		t.Fatalf("proposal/candidate=%#v %#v", read.Proposal, read.Candidates[0])
	}
	for _, forbidden := range []string{"url", "serving", "listener", "docker"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("desired-state response implied observation with %q: %s", forbidden, response.Body.String())
		}
	}

	created := fixture.configure(t, uuid.NewString(), 0, wantSpec, wantDigest)
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("configure=%d cache=%q %s", created.Code, created.Header().Get("Cache-Control"), created.Body.String())
	}
	response = relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-profile", "")
	read = apicontract.LANGatewayProfileRead{}
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || read.ExpectedRevisionNumber != 1 || read.DesiredProfile == nil || read.DesiredProfile.RevisionNumber != 1 || read.DesiredProfile.SpecDigest != wantDigest || read.Proposal != nil {
		t.Fatalf("configured desired read=%d %#v", response.Code, read)
	}
}

func TestLANGatewayProfileConfigureDerivesActorAndPreservesExactReplay(t *testing.T) {
	fixture := newLANGatewayProfileFixture(t)
	spec := appaccess.GatewayProfileSpec{SelectedIPv4: fixture.candidate.IPv4, InterfaceID: fixture.candidate.InterfaceID, PortStart: 8100, PortEnd: 8119}
	digest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	operationID := uuid.NewString()
	created := fixture.configure(t, operationID, 0, spec, digest)
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	var mutation apicontract.LANGatewayProfileMutation
	if err := json.Unmarshal(created.Body.Bytes(), &mutation); err != nil {
		t.Fatal(err)
	}
	if !mutation.Created || mutation.Profile.OperationID != operationID || mutation.Profile.ApprovedBy != fixture.actorID || mutation.Profile.SpecDigest != digest {
		t.Fatalf("created mutation=%#v", mutation)
	}

	fixture.candidates = nil
	replayed := fixture.configure(t, operationID, 0, spec, digest)
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay=%d %s", replayed.Code, replayed.Body.String())
	}
	var replay apicontract.LANGatewayProfileMutation
	if err := json.Unmarshal(replayed.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Created || replay.Profile.ID != mutation.Profile.ID || replay.Profile.RevisionNumber != mutation.Profile.RevisionNumber {
		t.Fatalf("replay mutation=%#v want id=%s", replay, mutation.Profile.ID)
	}
	fixture.candidateErr = errors.New("transient interface discovery failure")
	if response := fixture.configure(t, operationID, 0, spec, digest); response.Code != http.StatusOK {
		t.Fatalf("exact replay after discovery failure=%d %s", response.Code, response.Body.String())
	}
}

func TestLANGatewayProfileConfigureRejectsStaleReplayDigestAndChangedInterface(t *testing.T) {
	fixture := newLANGatewayProfileFixture(t)
	spec := appaccess.GatewayProfileSpec{SelectedIPv4: fixture.candidate.IPv4, InterfaceID: fixture.candidate.InterfaceID, PortStart: 8100, PortEnd: 8119}
	digest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	operationID := uuid.NewString()
	if response := fixture.configure(t, operationID, 0, spec, digest); response.Code != http.StatusCreated {
		t.Fatalf("seed=%d %s", response.Code, response.Body.String())
	}

	stale := fixture.configure(t, uuid.NewString(), 0, spec, digest)
	assertGatewayProblem(t, stale, http.StatusConflict, "lan_gateway_conflict")

	changedSpec := spec
	changedSpec.PortEnd = 8118
	changedDigest, err := appaccess.GatewayProfileSpecDigest(changedSpec)
	if err != nil {
		t.Fatal(err)
	}
	replayConflict := fixture.configure(t, operationID, 0, changedSpec, changedDigest)
	assertGatewayProblem(t, replayConflict, http.StatusConflict, "lan_gateway_replay_conflict")

	wrongDigest := fixture.configure(t, uuid.NewString(), 1, spec, strings.Repeat("0", 64))
	assertGatewayProblem(t, wrongDigest, http.StatusConflict, "lan_gateway_approval_mismatch")

	fixture.candidates = nil
	changedInterface := fixture.configure(t, uuid.NewString(), 1, spec, digest)
	assertGatewayProblem(t, changedInterface, http.StatusConflict, "lan_gateway_interface_changed")
	otherOwner := fixture.candidate
	otherOwner.InterfaceID = "8/Wi-Fi"
	otherOwner.Name = "Wi-Fi"
	fixture.candidates = []hostnetwork.Candidate{fixture.candidate, otherOwner}
	ambiguousInterface := fixture.configure(t, uuid.NewString(), 1, spec, digest)
	assertGatewayProblem(t, ambiguousInterface, http.StatusConflict, "lan_gateway_interface_changed")
	current, err := fixture.repository.CurrentGatewayProfile(context.Background())
	if err != nil || current.RevisionNumber != 1 || current.OperationID != operationID {
		t.Fatalf("rejected writes changed desired head: %#v %v", current, err)
	}
}

func TestLANGatewayProfileRoutesEnforceAdministratorAuthenticationAndCSRF(t *testing.T) {
	candidate := testLANGatewayCandidate()
	service := &gatewayProfileServiceFake{}
	viewerHandler := (&Server{
		Auth:              controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "operator"}},
		GatewayProfiles:   service,
		GatewayCandidates: func() ([]hostnetwork.Candidate, error) { return []hostnetwork.Candidate{candidate}, nil },
		Logger:            relayTestLogger(),
	}).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := relayAuthenticatedRequest(viewerHandler, method, "/api/v1/system/lan-gateway-profile", `{}`)
		assertGatewayProblem(t, response, http.StatusForbidden, "lan_gateway_forbidden")
	}
	if service.currentCalls != 0 || service.replayCalls != 0 || service.configureCalls != 0 {
		t.Fatalf("viewer reached profile service: %#v", service)
	}

	adminHandler := (&Server{
		Auth:              controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}},
		GatewayProfiles:   service,
		GatewayCandidates: func() ([]hostnetwork.Candidate, error) { return []hostnetwork.Candidate{candidate}, nil },
		Logger:            relayTestLogger(),
	}).Handler()
	unauthenticated := httptestGatewayRequest(adminHandler, http.MethodGet, "/api/v1/system/lan-gateway-profile", "", false, false)
	assertGatewayProblem(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	missingCSRF := httptestGatewayRequest(adminHandler, http.MethodPost, "/api/v1/system/lan-gateway-profile", `{}`, true, false)
	assertGatewayProblem(t, missingCSRF, http.StatusForbidden, "csrf_failed")
	if service.currentCalls != 0 || service.replayCalls != 0 || service.configureCalls != 0 {
		t.Fatalf("auth failures reached profile service: %#v", service)
	}
}

func TestLANGatewayProfileProposalAndBodyValidationFailClosed(t *testing.T) {
	fixture := newLANGatewayProfileFixture(t)
	invalidQueries := []string{
		"?interfaceId=" + url.QueryEscape(fixture.candidate.InterfaceID),
		"?interfaceId=" + url.QueryEscape(fixture.candidate.InterfaceID) + "&selectedIpv4=" + fixture.candidate.IPv4 + "&portStart=8100&portEnd=8119&unexpected=true",
		"?interfaceId=" + url.QueryEscape(fixture.candidate.InterfaceID) + "&interfaceId=other&selectedIpv4=" + fixture.candidate.IPv4 + "&portStart=8100&portEnd=8119",
		"?interfaceId=" + url.QueryEscape(fixture.candidate.InterfaceID) + "&selectedIpv4=" + fixture.candidate.IPv4 + "&portStart=8120&portEnd=8120",
		"?interfaceId=" + url.QueryEscape(fixture.candidate.InterfaceID) + "&selectedIpv4=not-an-ip&portStart=8100&portEnd=8119",
		"?interfaceId=" + url.QueryEscape(fixture.candidate.InterfaceID) + "&selectedIpv4=8.8.8.8&portStart=8100&portEnd=8119",
		"?interfaceId=" + strings.Repeat("x", 513) + "&selectedIpv4=" + fixture.candidate.IPv4 + "&portStart=8100&portEnd=8119",
	}
	for _, query := range invalidQueries {
		response := relayAuthenticatedRequest(fixture.handler, http.MethodGet, "/api/v1/system/lan-gateway-profile"+query, "")
		assertGatewayProblem(t, response, http.StatusUnprocessableEntity, "invalid_lan_gateway_request")
	}

	digest := strings.Repeat("0", 64)
	invalidBodies := []string{
		`{}`,
		`{"operationId":"` + uuid.NewString() + `","expectedRevisionNumber":0,"spec":null,"approvalDigest":"` + digest + `"}`,
		`{"operationId":"` + uuid.NewString() + `","expectedRevisionNumber":0,"spec":{"selectedIpv4":"` + fixture.candidate.IPv4 + `","interfaceId":"` + fixture.candidate.InterfaceID + `","portStart":8100,"portStart":8101,"portEnd":8119},"approvalDigest":"` + digest + `"}`,
		`{"operationId":"` + uuid.NewString() + `","expectedRevisionNumber":0,"spec":{"selectedIpv4":"` + fixture.candidate.IPv4 + `","interfaceId":"` + fixture.candidate.InterfaceID + `","portStart":8100,"portEnd":8119},"approvalDigest":"` + digest + `","actorId":"attacker"}`,
	}
	for _, body := range invalidBodies {
		response := relayAuthenticatedRequest(fixture.handler, http.MethodPost, "/api/v1/system/lan-gateway-profile", body)
		assertGatewayProblem(t, response, http.StatusUnprocessableEntity, "invalid_lan_gateway_request")
	}
}

func TestLANGatewayProfileDiscoveryFailureIsSafe(t *testing.T) {
	secret := "sensitive-interface-discovery-detail"
	handler := (&Server{
		Auth:              controllerAuthFake{user: auth.User{ID: uuid.NewString(), Role: "administrator"}},
		GatewayProfiles:   &gatewayProfileServiceFake{currentErr: appaccess.ErrNotFound},
		GatewayCandidates: func() ([]hostnetwork.Candidate, error) { return nil, errors.New(secret) },
		Logger:            relayTestLogger(),
	}).Handler()
	response := relayAuthenticatedRequest(handler, http.MethodGet, "/api/v1/system/lan-gateway-profile", "")
	assertGatewayProblem(t, response, http.StatusServiceUnavailable, "lan_gateway_unavailable")
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("response leaked discovery detail: %s", response.Body.String())
	}
}

type lanGatewayProfileFixture struct {
	handler      http.Handler
	repository   *appaccess.Repository
	actorID      string
	candidate    hostnetwork.Candidate
	candidates   []hostnetwork.Candidate
	candidateErr error
}

func newLANGatewayProfileFixture(t *testing.T) *lanGatewayProfileFixture {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	actorID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?,?,?,?)`, actorID, "gateway-admin", "fixture", "administrator", now, now); err != nil {
		t.Fatal(err)
	}
	fixture := &lanGatewayProfileFixture{repository: appaccess.New(db), actorID: actorID, candidate: testLANGatewayCandidate()}
	fixture.candidates = []hostnetwork.Candidate{fixture.candidate}
	fixture.handler = (&Server{
		Auth:            controllerAuthFake{user: auth.User{ID: actorID, Role: "administrator"}},
		GatewayProfiles: fixture.repository,
		GatewayCandidates: func() ([]hostnetwork.Candidate, error) {
			if fixture.candidateErr != nil {
				return nil, fixture.candidateErr
			}
			return append([]hostnetwork.Candidate(nil), fixture.candidates...), nil
		},
		Logger: relayTestLogger(),
	}).Handler()
	return fixture
}

func (fixture *lanGatewayProfileFixture) configure(t *testing.T, operationID string, expected int64, spec appaccess.GatewayProfileSpec, digest string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"operationId":%q,"expectedRevisionNumber":%d,"spec":{"selectedIpv4":%q,"interfaceId":%q,"portStart":%d,"portEnd":%d},"approvalDigest":%q}`,
		operationID, expected, spec.SelectedIPv4, spec.InterfaceID, spec.PortStart, spec.PortEnd, digest)
	return relayAuthenticatedRequest(fixture.handler, http.MethodPost, "/api/v1/system/lan-gateway-profile", body)
}

func testLANGatewayCandidate() hostnetwork.Candidate {
	return hostnetwork.Candidate{InterfaceID: "7/Ethernet LAN", Name: "Ethernet LAN", IPv4: "192.168.50.20", Prefix: netip.MustParsePrefix("192.168.50.0/24")}
}

type gatewayProfileServiceFake struct {
	current        appaccess.GatewayProfileRevision
	currentErr     error
	currentCalls   int
	replayCalls    int
	configureCalls int
}

func (fake *gatewayProfileServiceFake) CurrentGatewayProfile(context.Context) (appaccess.GatewayProfileRevision, error) {
	fake.currentCalls++
	return fake.current, fake.currentErr
}

func (fake *gatewayProfileServiceFake) ReplayGatewayProfile(context.Context, appaccess.ConfigureGatewayInput) (appaccess.GatewayProfileRevision, error) {
	fake.replayCalls++
	return appaccess.GatewayProfileRevision{}, appaccess.ErrNotFound
}

func (fake *gatewayProfileServiceFake) ConfigureGatewayProfile(context.Context, appaccess.ConfigureGatewayInput) (appaccess.GatewayProfileRevision, bool, error) {
	fake.configureCalls++
	return appaccess.GatewayProfileRevision{}, false, nil
}

func assertGatewayProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("problem=%d cache=%q body=%s want=%d/%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String(), status, code)
	}
}

func httptestGatewayRequest(handler http.Handler, method, path, body string, cookie, csrf bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie {
		request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "session"})
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", "csrf-token")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var _ GatewayProfileService = (*gatewayProfileServiceFake)(nil)
