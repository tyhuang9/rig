package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

type enrollmentStore struct {
	fakeStore
	created     store.EnrollmentInput
	pollHash    []byte
	claimedHash []byte
	completed   bool
	failed      string
	failContext error
	claimHook   func()
	returnedID  string
}

func (f *enrollmentStore) CreateEnrollment(_ context.Context, input store.EnrollmentInput) (string, error) {
	input.PublicKey = append([]byte(nil), input.PublicKey...)
	input.StateHash = append([]byte(nil), input.StateHash...)
	input.PollHash = append([]byte(nil), input.PollHash...)
	input.PKCECiphertext = append([]byte(nil), input.PKCECiphertext...)
	input.PKCESealNonce = append([]byte(nil), input.PKCESealNonce...)
	input.RequestNonce = append([]byte(nil), input.RequestNonce...)
	f.created = input
	if f.returnedID != "" {
		return f.returnedID, nil
	}
	return input.EnrollmentID, nil
}

func (f *enrollmentStore) ClaimEnrollmentState(_ context.Context, hash []byte) (store.EnrollmentClaim, error) {
	f.claimedHash = append([]byte(nil), hash...)
	if f.claimHook != nil {
		f.claimHook()
	}
	return store.EnrollmentClaim{
		EnrollmentID: f.created.EnrollmentID, ControllerID: f.created.ControllerID, KeyID: f.created.KeyID,
		PublicKey: append([]byte(nil), f.created.PublicKey...), InstallationID: f.created.InstallationID,
		RepositoryID: f.created.RepositoryID, PKCECiphertext: append([]byte(nil), f.created.PKCECiphertext...),
		PKCESealNonce: append([]byte(nil), f.created.PKCESealNonce...), RequestNonce: append([]byte(nil), f.created.RequestNonce...),
		ExpiresAt: f.created.ExpiresAt,
	}, nil
}

func (f *enrollmentStore) CompleteEnrollment(context.Context, string) error {
	f.completed = true
	return nil
}
func (f *enrollmentStore) FailEnrollment(ctx context.Context, _ string, code string) error {
	f.failed = code
	f.failContext = ctx.Err()
	return nil
}
func (f *enrollmentStore) PollEnrollment(_ context.Context, hash []byte) (store.EnrollmentStatus, error) {
	f.pollHash = append([]byte(nil), hash...)
	return store.EnrollmentStatus{Status: "authorized"}, nil
}

func TestEnrollmentReturnedTokensDrivePollAndLiveVerifiedCallback(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	providerCalls := 0
	transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
		providerCalls++
		if request.URL.Scheme != "https" || (request.URL.Host != "github.com" && request.URL.Host != "api.github.com") {
			t.Fatalf("unsafe provider URL: %s", request.URL)
		}
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		switch request.URL.String() {
		case "https://github.com/login/oauth/access_token":
			if request.URL.RawQuery != "" {
				t.Fatalf("token exchange query contains credentials: %q", request.URL.RawQuery)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			values, err := url.ParseQuery(string(body))
			expected := map[string]string{
				"client_id":     "client-id",
				"client_secret": strings.Repeat("\x01", 16),
				"code":          "provider-code",
				"code_verifier": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
				"redirect_uri":  "https://relay.example.test/v1/github/callback",
				"repository_id": "8",
			}
			if err != nil || len(values) != len(expected) {
				t.Fatalf("token exchange form cardinality=%d parse=%v", len(values), err)
			}
			for key, want := range expected {
				entries, exists := values[key]
				if !exists || len(entries) != 1 || entries[0] != want {
					t.Fatalf("token exchange field %q mismatch", key)
				}
			}
			response.Body = io.NopCloser(strings.NewReader(`{"access_token":"ghu_sensitive_access","refresh_token":"ghr_sensitive_refresh"}`))
		case "https://api.github.com/user":
			response.Body = io.NopCloser(strings.NewReader(`{"id":123}`))
		case "https://api.github.com/user/installations?page=1&per_page=100":
			response.Body = io.NopCloser(strings.NewReader(`{"installations":[{"id":7}]}`))
		case "https://api.github.com/user/installations/7/repositories?page=1&per_page=100":
			response.Body = io.NopCloser(strings.NewReader(`{"repositories":[{"id":8}]}`))
		default:
			t.Fatalf("unexpected provider request: %s", request.URL)
		}
		return response, nil
	})
	persistence := &enrollmentStore{}
	s := newEnrollmentTestService(t, persistence, transport, now)

	requestBody := signedEnrollmentRequest(t, now, 7, 8)
	request := httptest.NewRequest(http.MethodPost, startEnrollmentPath, bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var started struct {
		AuthorizationURL string `json:"authorizationUrl"`
		PollToken        string `json:"pollToken"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeCanonical(authorize.Query().Get("state"), 32)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(state)
	poll, err := decodeCanonical(started.PollToken, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(poll)
	if !bytes.Equal(persistence.created.StateHash, domainHash("rig.relay.v1/oauth-state", state)) || !bytes.Equal(persistence.created.PollHash, domainHash("rig.relay.v1/enrollment-poll", poll)) {
		t.Fatal("returned state/poll tokens do not match persisted hashes")
	}
	aad := enrollmentAAD(persistence.created.EnrollmentID, persistence.created.ControllerID, persistence.created.KeyID, 7, 8, persistence.created.RequestNonce)
	verifier, err := s.openVerifier(persistence.created.PKCECiphertext, persistence.created.PKCESealNonce, aad)
	if err != nil {
		t.Fatal(err)
	}
	challenge := sha256.Sum256(verifier)
	clear(verifier)
	clear(aad)
	if authorize.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || authorize.Query().Get("repository_id") != "8" {
		t.Fatal("authorization URL does not bind PKCE and repository")
	}

	pollRequest := httptest.NewRequest(http.MethodPost, pollEnrollmentPath, strings.NewReader(`{"pollToken":"`+started.PollToken+`"}`))
	pollRequest.Header.Set("Content-Type", "application/json")
	pollRecorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(pollRecorder, pollRequest)
	if pollRecorder.Code != http.StatusOK || !bytes.Equal(persistence.pollHash, persistence.created.PollHash) || !strings.Contains(pollRecorder.Body.String(), `"status":"authorized"`) {
		t.Fatalf("poll status=%d body=%s", pollRecorder.Code, pollRecorder.Body.String())
	}

	callback := httptest.NewRequest(http.MethodGet, oauthCallbackPath+"?state="+url.QueryEscape(authorize.Query().Get("state"))+"&code=provider-code&installation_id=999", nil)
	callbackRecorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(callbackRecorder, callback)
	if callbackRecorder.Code != http.StatusOK || !persistence.completed || persistence.failed != "" {
		t.Fatalf("callback status=%d completed=%v failed=%q", callbackRecorder.Code, persistence.completed, persistence.failed)
	}
	if !bytes.Equal(persistence.claimedHash, persistence.created.StateHash) || providerCalls != 4 {
		t.Fatalf("claim/provider calls mismatch: calls=%d", providerCalls)
	}
	if callbackRecorder.Header().Get("Referrer-Policy") != "no-referrer" || callbackRecorder.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("terminal callback security headers missing")
	}
}

func TestStartEnrollmentFailsClosedOnStoreEnrollmentIDMismatch(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	persistence := &enrollmentStore{returnedID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider call"); return nil, nil }), now)
	request := httptest.NewRequest(http.MethodPost, startEnrollmentPath, bytes.NewReader(signedEnrollmentRequest(t, now, 7, 8)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "store.inconsistent") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCallbackFailureCleanupSurvivesRequestCancellationAfterStateClaim(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	state := bytes.Repeat([]byte{7}, 32)
	persistence := &enrollmentStore{created: store.EnrollmentInput{
		EnrollmentID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		ControllerID: "11111111-1111-4111-8111-111111111111",
		KeyID:        "22222222-2222-4222-8222-222222222222",
		ExpiresAt:    now.Add(time.Minute),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	persistence.claimHook = cancel
	s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider call"); return nil, nil }), now)
	request := httptest.NewRequest(http.MethodGet, oauthCallbackPath+"?state="+base64.RawURLEncoding.EncodeToString(state)+"&error=access_denied", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	if persistence.failed != "oauth.denied" || persistence.failContext != nil {
		t.Fatalf("failure=%q cleanup context=%v", persistence.failed, persistence.failContext)
	}
}

func newEnrollmentTestService(t *testing.T, persistence Store, transport http.RoundTripper, now time.Time) *Service {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://relay.example.test")
	s, err := New(Options{
		Transport: transport, Store: persistence, Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{9}, 4096)),
		PublicBaseURL: base, GitHubClientID: "client-id", GitHubClientSecret: bytes.Repeat([]byte{1}, 16), GitHubAppID: 1,
		GitHubPrivateKey: key, WebhookSecret: bytes.Repeat([]byte{2}, 32), EnrollmentKey: bytes.Repeat([]byte{3}, 32), RecoveryWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func signedEnrollmentRequest(t *testing.T, now time.Time, installationID, repositoryID int64) []byte {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := startEnrollmentRequest{
		ControllerID: "11111111-1111-4111-8111-111111111111", KeyID: "22222222-2222-4222-8222-222222222222",
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), InstallationID: installationID, RepositoryID: repositoryID,
		RequestNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)), IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	transcript, err := protocol.EnrollmentTranscript(protocol.EnrollmentProof{
		ControllerID: input.ControllerID, KeyID: input.KeyID, PublicKey: input.PublicKey,
		InstallationID: input.InstallationID, RepositoryID: input.RepositoryID, RequestNonce: input.RequestNonce,
		IssuedAt: input.IssuedAt, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, transcript))
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
