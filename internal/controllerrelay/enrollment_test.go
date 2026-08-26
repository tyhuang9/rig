package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/sourceconnections"
)

var enrollmentTestNow = time.Date(2026, 8, 25, 18, 30, 0, 123456789, time.UTC)

func TestEnrollmentServiceStartSignsUnit8TranscriptAndPersistsPollCredential(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	result, err := fixture.service.Start(context.Background(), fixture.startInput())
	if err != nil {
		t.Fatalf("start enrollment: %v", err)
	}
	if result.Status != EnrollmentPending || result.AuthorizationURL != "https://github.com/login/oauth/authorize?state=safe" || !result.ExpiresAt.Equal(enrollmentTestNow.Add(10*time.Minute)) {
		t.Fatalf("unexpected result %#v", result)
	}
	request := fixture.client.startedRequest
	transcript, err := protocol.EnrollmentTranscript(protocol.EnrollmentProof{
		ControllerID: request.ControllerID, KeyID: request.KeyID, PublicKey: request.PublicKey,
		InstallationID: request.InstallationID, RepositoryID: request.RepositoryID,
		RequestNonce: request.RequestNonce, IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(transcript)
	if !protocol.Verify(fixture.publicKey, transcript, request.Signature) {
		t.Fatal("service signature did not verify against Unit 8 EnrollmentTranscript")
	}
	stored := fixture.repository.enrollments[result.EnrollmentID]
	if stored.ProtectedPollRef != ProtectedEnrollmentPollRef(credentialTestControllerID, result.EnrollmentID) || stored.Purpose != EnrollmentPurpose || stored.State != EnrollmentPending {
		t.Fatalf("unexpected persisted enrollment %#v", stored)
	}
	token := fixture.credentials.pollTokens[result.EnrollmentID]
	if token.OwnerUserID != fixture.owner || !bytes.Equal(token.Token, fixture.client.pollToken) {
		t.Fatal("poll credential was not purpose-bound and persisted before metadata")
	}
	secretText := base64.RawURLEncoding.EncodeToString(fixture.client.pollToken)
	for _, rendered := range []string{fmt.Sprint(result), fmt.Sprintf("%#v", result), result.LogValue().String()} {
		if strings.Contains(rendered, secretText) {
			t.Fatalf("start result leaked poll token: %s", rendered)
		}
	}
}

func TestEnrollmentServiceStartRemovesExactPollCredentialOnDatabaseFailure(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	fixture.repository.createEnrollmentErr = errors.New("database unavailable")
	_, err := fixture.service.Start(context.Background(), fixture.startInput())
	if !IsEnrollmentCode(err, "persistence_unavailable") {
		t.Fatalf("unexpected error %v", err)
	}
	if len(fixture.credentials.pollTokens) != 0 || fixture.credentials.removedPollCount != 1 {
		t.Fatalf("orphan poll credential not removed exactly: %#v", fixture.credentials.pollTokens)
	}
}

func TestEnrollmentServiceStartRecoversLostDatabaseSuccessResponse(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	fixture.repository.createEnrollmentCommitThenErr = true
	result, err := fixture.service.Start(context.Background(), fixture.startInput())
	if err != nil {
		t.Fatalf("lost success response was not recovered: %v", err)
	}
	if _, ok := fixture.repository.enrollments[result.EnrollmentID]; !ok {
		t.Fatal("committed enrollment missing")
	}
	if fixture.credentials.removedPollCount != 0 {
		t.Fatal("protected poll credential removed after confirmed commit")
	}
}

func TestEnrollmentServicePollPendingIsIdempotentAndRetainsCredential(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
	fixture.client.pollStatus = RelayEnrollmentStatus{Status: EnrollmentPending}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
		if err != nil || result.Status != EnrollmentPending {
			t.Fatalf("pending poll %d: result=%#v err=%v", attempt, result, err)
		}
	}
	if fixture.repository.markPolledCount != 2 || fixture.client.pollCount != 2 {
		t.Fatalf("unexpected pending poll counts repository=%d client=%d", fixture.repository.markPolledCount, fixture.client.pollCount)
	}
	if _, ok := fixture.credentials.pollTokens[enrollmentID]; !ok {
		t.Fatal("pending poll removed bearer credential")
	}
}

func TestEnrollmentServicePollTerminalStatesCommitBeforeCredentialCleanup(t *testing.T) {
	for _, status := range []string{EnrollmentAuthorized, EnrollmentDenied, EnrollmentFailed} {
		t.Run(status, func(t *testing.T) {
			fixture := newEnrollmentServiceFixture(t)
			enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
			fixture.client.pollStatus = RelayEnrollmentStatus{Status: status}
			fixture.credentials.onRemovePoll = func() {
				stored := fixture.repository.enrollments[enrollmentID]
				if stored.State == EnrollmentPending {
					t.Fatal("poll credential removed before terminal database commit")
				}
			}
			result, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
			if err != nil || result.Status != status {
				t.Fatalf("terminal poll result=%#v err=%v", result, err)
			}
			if status == EnrollmentAuthorized && !validCanonicalUUID(result.BindingID) {
				t.Fatalf("authorized result missing binding ID: %#v", result)
			}
			stored := fixture.repository.enrollments[enrollmentID]
			if stored.ProtectedPollRef != "" || stored.PollRefClearedAt == nil {
				t.Fatalf("poll ref not cleared after exact deletion: %#v", stored)
			}
			clientCalls := fixture.client.pollCount
			retried, retryErr := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
			if retryErr != nil || retried.Status != status || fixture.client.pollCount != clientCalls {
				t.Fatalf("terminal retry was not idempotent: %#v %v", retried, retryErr)
			}
		})
	}
}

func TestEnrollmentServicePollExpiresLocallyWithoutRelayMutation(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow)
	result, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
	if err != nil || result.Status != EnrollmentExpired {
		t.Fatalf("local expiry result=%#v err=%v", result, err)
	}
	if fixture.client.pollCount != 0 {
		t.Fatal("expired attempt called relay")
	}
}

func TestEnrollmentServicePollKeepsPendingStateAfterLostRelayResponse(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
	fixture.client.pollErr = &ClientError{Code: "relay_unavailable"}
	_, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
	if !IsEnrollmentCode(err, "relay_unavailable") {
		t.Fatalf("unexpected lost response error %v", err)
	}
	if fixture.repository.enrollments[enrollmentID].State != EnrollmentPending {
		t.Fatal("lost relay response changed durable state")
	}
	if _, ok := fixture.credentials.pollTokens[enrollmentID]; !ok {
		t.Fatal("lost relay response removed retry credential")
	}
}

func TestEnrollmentServicePollRecoversLostTerminalDatabaseResponse(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
	fixture.client.pollStatus = RelayEnrollmentStatus{Status: EnrollmentAuthorized}
	fixture.repository.completeCommitThenErr = true
	result, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
	if err != nil || result.Status != EnrollmentAuthorized || result.BindingID == "" {
		t.Fatalf("lost database response not recovered result=%#v err=%v", result, err)
	}
	if fixture.repository.enrollments[enrollmentID].ProtectedPollRef != "" {
		t.Fatal("terminal poll credential ref not cleaned")
	}
}

func TestEnrollmentServicePollRelayLostAttemptBecomesSanitizedFailure(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
	fixture.client.pollErr = &ClientError{Code: "enrollment_not_found"}
	result, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
	if err != nil || result.Status != EnrollmentFailed {
		t.Fatalf("lost attempt result=%#v err=%v", result, err)
	}
	stored := fixture.repository.enrollments[enrollmentID]
	if stored.LastErrorCode != ErrorEnrollmentFailed || strings.Contains(stored.LastErrorCode, "provider") {
		t.Fatalf("unsanitized error stored: %q", stored.LastErrorCode)
	}
}

func TestEnrollmentServiceFailurePathsPersistThroughRealRepositoryAndCleanupAfterCommit(t *testing.T) {
	tests := []struct {
		name       string
		writePoll  bool
		pollStatus RelayEnrollmentStatus
		pollErr    error
		wantCalls  int
	}{
		{name: "credential missing", writePoll: false, wantCalls: 0},
		{name: "relay enrollment lost", writePoll: true, pollErr: &ClientError{Code: "enrollment_not_found"}, wantCalls: 1},
		{name: "relay declared failure", writePoll: true, pollStatus: RelayEnrollmentStatus{Status: EnrollmentFailed}, wantCalls: 1},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, root, now := newRepositoryHarness(t)
			credentials, err := NewFileCredentialStore(root)
			if err != nil {
				t.Fatal(err)
			}
			controllerID := fmt.Sprintf("a0000000-0000-4000-8000-%012d", index+1)
			keyID := fmt.Sprintf("b0000000-0000-4000-8000-%012d", index+1)
			enrollmentID := fmt.Sprintf("c0000000-0000-4000-8000-%012d", index+1)
			privateKey := testPrivateKey(byte(0x30 + index))
			defer clear(privateKey)
			publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
			defer clear(publicKey)
			protectedKeyRef, err := credentials.WriteControllerKey(ControllerKeyBundle{
				Version: credentialVersion, ControllerID: controllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey,
			})
			if err != nil {
				t.Fatal(err)
			}
			activated := now
			if err = repository.CreateIdentity(context.Background(), ControllerIdentity{ControllerID: controllerID, State: ControllerActive, CreatedAt: now, UpdatedAt: now}, ControllerKey{
				KeyID: keyID, ControllerID: controllerID, PublicKey: publicKey, Algorithm: KeyAlgorithmEd25519,
				State: KeyActive, ProtectedKeyRef: protectedKeyRef, CreatedAt: now, UpdatedAt: now,
				ActivatedAt: &activated, PossessionConfirmedAt: &activated,
			}); err != nil {
				t.Fatal(err)
			}
			pollToken := bytes.Repeat([]byte{byte(0x61 + index)}, pollTokenBytes)
			protectedPollRef := ProtectedEnrollmentPollRef(controllerID, enrollmentID)
			if test.writePoll {
				protectedPollRef, err = credentials.WriteEnrollmentPollToken(EnrollmentPollToken{
					Version: credentialVersion, ControllerID: controllerID, EnrollmentID: enrollmentID, OwnerUserID: "owner", Token: pollToken,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			enrollment := Enrollment{
				EnrollmentID: enrollmentID, OwnerUserID: "owner", ConnectionID: "connection-a", ControllerID: controllerID, KeyID: keyID,
				InstallationID: 101, RepositoryID: 202, Purpose: EnrollmentPurpose, ProtectedPollRef: protectedPollRef,
				State: EnrollmentPending, CreatedAt: now, ExpiresAt: now.Add(enrollmentAttemptLifetime), StateChangedAt: now, UpdatedAt: now,
			}
			if err = repository.CreateEnrollment(context.Background(), enrollment); err != nil {
				t.Fatal(err)
			}
			orderedCredentials := &repositoryOrderingCredentials{FileCredentialStore: credentials, repository: repository, owner: "owner", enrollmentID: enrollmentID}
			client := &fakeEnrollmentClient{pollStatus: test.pollStatus, pollErr: test.pollErr}
			service, err := NewEnrollmentService(repository, fakeSourceAccess{}, orderedCredentials, client, func() time.Time { return now.Add(time.Minute) }, bytes.NewReader(bytes.Repeat([]byte{0x7d}, 512)))
			if err != nil {
				t.Fatal(err)
			}
			result, pollErr := service.Poll(context.Background(), "owner", enrollmentID)
			if pollErr != nil || result.Status != EnrollmentFailed || result.BindingID != "" {
				t.Fatalf("terminal result=%#v err=%v", result, pollErr)
			}
			if client.pollCount != test.wantCalls {
				t.Fatalf("relay poll calls=%d want=%d", client.pollCount, test.wantCalls)
			}
			stored, err := repository.Enrollment(context.Background(), "owner", enrollmentID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.State != EnrollmentFailed || stored.LastErrorCode != ErrorEnrollmentFailed || stored.CompletedAt == nil || stored.ProtectedPollRef != "" || stored.PollRefClearedAt == nil {
				t.Fatalf("real repository terminal state=%#v", stored)
			}
			if !orderedCredentials.observedTerminalBeforeRemoval {
				t.Fatal("protected credential cleanup ran before durable terminal state")
			}
			assertNoEnrollmentFailureSecretArtifacts(t, root, pollToken)
		})
	}
}

func TestEnrollmentServiceRetriesCleanupAfterTerminalCommit(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
	fixture.client.pollStatus = RelayEnrollmentStatus{Status: EnrollmentDenied}
	fixture.credentials.removePollErr = errors.New("filesystem unavailable")
	result, err := fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
	if result.Status != EnrollmentDenied || !IsEnrollmentCode(err, "credential_cleanup_pending") {
		t.Fatalf("expected cleanup-pending terminal result, got %#v %v", result, err)
	}
	if fixture.repository.enrollments[enrollmentID].State != EnrollmentDenied {
		t.Fatal("cleanup failure reverted terminal database state")
	}
	fixture.credentials.removePollErr = nil
	clientCalls := fixture.client.pollCount
	result, err = fixture.service.Poll(context.Background(), fixture.owner, enrollmentID)
	if err != nil || result.Status != EnrollmentDenied || fixture.client.pollCount != clientCalls {
		t.Fatalf("cleanup retry failed result=%#v err=%v", result, err)
	}
}

func TestEnrollmentServiceRecoverExpiresAndCleansTerminalRefs(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	enrollmentID := fixture.seedPendingEnrollment(t, enrollmentTestNow)
	cleaned, err := fixture.service.Recover(context.Background(), 100)
	if err != nil || cleaned != 1 {
		t.Fatalf("recover cleaned=%d err=%v", cleaned, err)
	}
	stored := fixture.repository.enrollments[enrollmentID]
	if stored.State != EnrollmentExpired || stored.ProtectedPollRef != "" {
		t.Fatalf("recovery did not expire then clean ref: %#v", stored)
	}
}

func TestEnrollmentServiceRecoverRemovesOnlyDatabaseAbsentCrashOrphan(t *testing.T) {
	fixture := newEnrollmentServiceFixture(t)
	orphanID := "55555555-5555-4555-8555-555555555555"
	fixture.credentials.pollTokens[orphanID] = EnrollmentPollToken{
		Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: orphanID,
		OwnerUserID: fixture.owner, Token: bytes.Repeat([]byte{0x33}, pollTokenBytes),
	}
	retainedID := fixture.seedPendingEnrollment(t, enrollmentTestNow.Add(time.Minute))
	cleaned, err := fixture.service.Recover(context.Background(), 100)
	if err != nil || cleaned != 1 {
		t.Fatalf("orphan recovery cleaned=%d err=%v", cleaned, err)
	}
	if _, ok := fixture.credentials.pollTokens[orphanID]; ok {
		t.Fatal("database-absent crash orphan was retained")
	}
	if _, ok := fixture.credentials.pollTokens[retainedID]; !ok {
		t.Fatal("recovery broadly deleted a pending credential")
	}
}

func TestEnrollmentServicePagedRecoveryAdvancesPastSaturatedHealthyPrefix(t *testing.T) {
	store, err := NewFileCredentialStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controllerID := "d0000000-0000-4000-8000-000000000001"
	healthyIDs := []string{
		"10000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
	}
	orphanID := "f0000000-0000-4000-8000-000000000003"
	owner := "owner-1"
	currentNow := enrollmentTestNow
	expiresAt := currentNow.Add(enrollmentAttemptLifetime)
	repository := &fakeEnrollmentRepository{enrollments: make(map[string]Enrollment)}
	for index, enrollmentID := range append(append([]string(nil), healthyIDs...), orphanID) {
		if _, err = store.WriteEnrollmentPollToken(EnrollmentPollToken{
			Version: credentialVersion, ControllerID: controllerID, EnrollmentID: enrollmentID,
			OwnerUserID: owner, Token: bytes.Repeat([]byte{byte(0x40 + index)}, pollTokenBytes),
		}); err != nil {
			t.Fatal(err)
		}
		if enrollmentID != orphanID {
			repository.enrollments[enrollmentID] = Enrollment{
				EnrollmentID: enrollmentID, OwnerUserID: owner, ConnectionID: "connection-1", ControllerID: controllerID,
				KeyID: credentialTestKeyID, InstallationID: 7, RepositoryID: int64(8 + index),
				Purpose: EnrollmentPurpose, ProtectedPollRef: ProtectedEnrollmentPollRef(controllerID, enrollmentID),
				State: EnrollmentPending, CreatedAt: currentNow, ExpiresAt: expiresAt, StateChangedAt: currentNow, UpdatedAt: currentNow,
			}
		}
	}
	service, err := NewEnrollmentService(repository, fakeSourceAccess{}, store, &fakeEnrollmentClient{}, func() time.Time { return currentNow }, bytes.NewReader(bytes.Repeat([]byte{0x55}, 512)))
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.RecoverPage(context.Background(), EnrollmentRecoveryCursor{}, len(healthyIDs))
	if err != nil {
		t.Fatal(err)
	}
	if first.Cleaned != 0 || first.Scanned != len(healthyIDs) || first.Complete || first.NextCursor.CredentialCursor == "" || !first.NextRunAt.Equal(currentNow) || !first.PostExpiryRunAt.Equal(expiresAt) {
		t.Fatalf("first bounded recovery page=%#v", first)
	}
	for _, enrollmentID := range healthyIDs {
		token, readErr := store.ReadEnrollmentPollToken(controllerID, enrollmentID)
		if readErr != nil {
			t.Fatalf("healthy prefix credential %s removed: %v", enrollmentID, readErr)
		}
		token.Destroy()
	}

	second, err := service.RecoverPage(context.Background(), first.NextCursor, len(healthyIDs))
	if err != nil {
		t.Fatal(err)
	}
	if second.Cleaned != 1 || second.Scanned != 1 || !second.Complete || second.NextCursor.CredentialCursor != "" || !second.PostExpiryRunAt.Equal(expiresAt) || !second.NextRunAt.Equal(expiresAt) {
		t.Fatalf("second bounded recovery page=%#v", second)
	}
	if _, err = store.ReadEnrollmentPollToken(controllerID, orphanID); err == nil {
		t.Fatal("later database-absent orphan survived cursor campaign")
	}
	for _, enrollmentID := range healthyIDs {
		token, readErr := store.ReadEnrollmentPollToken(controllerID, enrollmentID)
		if readErr != nil {
			t.Fatalf("healthy credential %s removed after orphan cleanup: %v", enrollmentID, readErr)
		}
		token.Destroy()
	}

	// The explicit post-expiry seam preserves the ten-minute authority window:
	// a scheduled new pass at the recorded expiry clears only then-terminal rows.
	currentNow = expiresAt.Add(time.Nanosecond)
	postExpiry, err := service.RecoverPage(context.Background(), EnrollmentRecoveryCursor{}, len(healthyIDs))
	if err != nil {
		t.Fatal(err)
	}
	if postExpiry.Cleaned != len(healthyIDs) {
		t.Fatalf("post-expiry cleanup page=%#v", postExpiry)
	}
	for _, enrollmentID := range healthyIDs {
		stored := repository.enrollments[enrollmentID]
		if stored.State != EnrollmentExpired || stored.ProtectedPollRef != "" {
			t.Fatalf("scheduled cleanup left enrollment %#v", stored)
		}
		if _, err = store.ReadEnrollmentPollToken(controllerID, enrollmentID); err == nil {
			t.Fatalf("expired credential %s remained readable", enrollmentID)
		}
	}
}

type enrollmentServiceFixture struct {
	service     *EnrollmentService
	repository  *fakeEnrollmentRepository
	credentials *fakeEnrollmentCredentials
	client      *fakeEnrollmentClient
	publicKey   ed25519.PublicKey
	privateKey  ed25519.PrivateKey
	owner       string
	connection  string
}

func newEnrollmentServiceFixture(t *testing.T) *enrollmentServiceFixture {
	t.Helper()
	privateKey := testPrivateKey(6)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	activated := enrollmentTestNow
	repository := &fakeEnrollmentRepository{
		identity: ControllerIdentity{ControllerID: credentialTestControllerID, State: ControllerActive, CreatedAt: enrollmentTestNow, UpdatedAt: enrollmentTestNow},
		key: ControllerKey{
			KeyID: credentialTestKeyID, ControllerID: credentialTestControllerID, PublicKey: append([]byte(nil), publicKey...),
			Algorithm: KeyAlgorithmEd25519, State: KeyActive, ProtectedKeyRef: ProtectedKeyRef(credentialTestControllerID, credentialTestKeyID),
			CreatedAt: enrollmentTestNow, UpdatedAt: enrollmentTestNow, ActivatedAt: &activated, PossessionConfirmedAt: &activated,
		},
		enrollments: make(map[string]Enrollment),
	}
	credentials := &fakeEnrollmentCredentials{
		controllerKeys: map[string]ControllerKeyBundle{credentialTestKeyID: {Version: credentialVersion, ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID, PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...)}},
		pollTokens:     make(map[string]EnrollmentPollToken),
	}
	client := &fakeEnrollmentClient{pollToken: bytes.Repeat([]byte{0x5c}, pollTokenBytes), pollStatus: RelayEnrollmentStatus{Status: EnrollmentPending}}
	service, err := NewEnrollmentService(repository, fakeSourceAccess{}, credentials, client, func() time.Time { return enrollmentTestNow }, bytes.NewReader(bytes.Repeat([]byte{0x9d}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	return &enrollmentServiceFixture{service: service, repository: repository, credentials: credentials, client: client, publicKey: publicKey, privateKey: privateKey, owner: "owner-1", connection: "connection-1"}
}

func (fixture *enrollmentServiceFixture) startInput() StartEnrollmentInput {
	return StartEnrollmentInput{OwnerUserID: fixture.owner, ConnectionID: fixture.connection, InstallationID: 7, RepositoryID: 8}
}

func (fixture *enrollmentServiceFixture) seedPendingEnrollment(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	enrollmentID := credentialTestEnrollmentID
	value := Enrollment{
		EnrollmentID: enrollmentID, OwnerUserID: fixture.owner, ConnectionID: fixture.connection,
		ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID,
		InstallationID: 7, RepositoryID: 8, Purpose: EnrollmentPurpose,
		ProtectedPollRef: ProtectedEnrollmentPollRef(credentialTestControllerID, enrollmentID), State: EnrollmentPending,
		CreatedAt: enrollmentTestNow.Add(-time.Minute), ExpiresAt: expiresAt, StateChangedAt: enrollmentTestNow.Add(-time.Minute), UpdatedAt: enrollmentTestNow.Add(-time.Minute),
	}
	fixture.repository.enrollments[enrollmentID] = value
	fixture.credentials.pollTokens[enrollmentID] = EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: enrollmentID, OwnerUserID: fixture.owner, Token: append([]byte(nil), fixture.client.pollToken...)}
	return enrollmentID
}

type fakeSourceAccess struct{ err error }

func (source fakeSourceAccess) Repository(context.Context, string, string, int64, int64) (sourceconnections.SourceRepository, error) {
	if source.err != nil {
		return sourceconnections.SourceRepository{}, source.err
	}
	return sourceconnections.SourceRepository{ID: 8, Owner: "octo", Name: "private"}, nil
}

type fakeEnrollmentClient struct {
	startedRequest RelayEnrollmentRequest
	pollToken      []byte
	startErr       error
	pollStatus     RelayEnrollmentStatus
	pollErr        error
	pollCount      int
}

func (client *fakeEnrollmentClient) Start(_ context.Context, request RelayEnrollmentRequest) (RelayEnrollmentStart, error) {
	client.startedRequest = request
	if client.startErr != nil {
		return RelayEnrollmentStart{}, client.startErr
	}
	return RelayEnrollmentStart{AuthorizationURL: "https://github.com/login/oauth/authorize?state=safe", PollToken: append([]byte(nil), client.pollToken...)}, nil
}

func (client *fakeEnrollmentClient) Poll(context.Context, []byte) (RelayEnrollmentStatus, error) {
	client.pollCount++
	return client.pollStatus, client.pollErr
}

type fakeEnrollmentCredentials struct {
	controllerKeys   map[string]ControllerKeyBundle
	pollTokens       map[string]EnrollmentPollToken
	removePollErr    error
	removedPollCount int
	onRemovePoll     func()
}

type repositoryOrderingCredentials struct {
	*FileCredentialStore
	repository                    *Repository
	owner                         string
	enrollmentID                  string
	observedTerminalBeforeRemoval bool
}

func (credentials *repositoryOrderingCredentials) RemoveEnrollmentPollToken(controllerID, enrollmentID string) error {
	stored, err := credentials.repository.Enrollment(context.Background(), credentials.owner, credentials.enrollmentID)
	if err != nil {
		return err
	}
	if stored.State == EnrollmentPending {
		return errors.New("protected credential removal observed pending database state")
	}
	credentials.observedTerminalBeforeRemoval = true
	return credentials.FileCredentialStore.RemoveEnrollmentPollToken(controllerID, enrollmentID)
}

func assertNoEnrollmentFailureSecretArtifacts(t *testing.T, root string, pollToken []byte) {
	t.Helper()
	forbidden := [][]byte{
		[]byte("credential_missing"), []byte("relay_enrollment_lost"), []byte("relay_enrollment_failed"),
		pollToken, []byte(base64.RawURLEncoding.EncodeToString(pollToken)),
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		defer clear(body)
		for _, secret := range forbidden {
			if len(secret) > 0 && bytes.Contains(body, secret) {
				return fmt.Errorf("artifact %s contains forbidden enrollment material", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (credentials *fakeEnrollmentCredentials) WriteControllerKey(bundle ControllerKeyBundle) (string, error) {
	if credentials.controllerKeys == nil {
		credentials.controllerKeys = make(map[string]ControllerKeyBundle)
	}
	credentials.controllerKeys[bundle.KeyID] = cloneControllerKeyBundle(bundle)
	return ProtectedKeyRef(bundle.ControllerID, bundle.KeyID), nil
}

func (credentials *fakeEnrollmentCredentials) ReadControllerKey(controllerID, keyID string, expected []byte) (ControllerKeyBundle, error) {
	bundle, ok := credentials.controllerKeys[keyID]
	if !ok || bundle.ControllerID != controllerID || !bytes.Equal(bundle.PublicKey, expected) {
		return ControllerKeyBundle{}, errors.New("key unavailable")
	}
	return cloneControllerKeyBundle(bundle), nil
}

func (credentials *fakeEnrollmentCredentials) RemoveControllerKey(_ string, keyID string) error {
	delete(credentials.controllerKeys, keyID)
	return nil
}

func (credentials *fakeEnrollmentCredentials) WriteEnrollmentPollToken(token EnrollmentPollToken) (string, error) {
	credentials.pollTokens[token.EnrollmentID] = clonePollToken(token)
	return ProtectedEnrollmentPollRef(token.ControllerID, token.EnrollmentID), nil
}

func (credentials *fakeEnrollmentCredentials) ReadEnrollmentPollToken(controllerID, enrollmentID string) (EnrollmentPollToken, error) {
	token, ok := credentials.pollTokens[enrollmentID]
	if !ok || token.ControllerID != controllerID {
		return EnrollmentPollToken{}, errors.New("poll token unavailable")
	}
	return clonePollToken(token), nil
}

func (credentials *fakeEnrollmentCredentials) RemoveEnrollmentPollToken(_ string, enrollmentID string) error {
	if credentials.onRemovePoll != nil {
		credentials.onRemovePoll()
	}
	if credentials.removePollErr != nil {
		return credentials.removePollErr
	}
	delete(credentials.pollTokens, enrollmentID)
	credentials.removedPollCount++
	return nil
}

func (credentials *fakeEnrollmentCredentials) EnrollmentPollCredentials(cursor string, limit int) (EnrollmentPollCredentialPage, error) {
	cursorControllerID, cursorEnrollmentID, err := parseEnrollmentPollCursor(cursor)
	if err != nil {
		return EnrollmentPollCredentialPage{}, err
	}
	all := make([]EnrollmentPollCredentialMetadata, 0, len(credentials.pollTokens))
	for _, token := range credentials.pollTokens {
		all = append(all, EnrollmentPollCredentialMetadata{
			ControllerID: token.ControllerID, EnrollmentID: token.EnrollmentID, OwnerUserID: token.OwnerUserID,
			ProtectedRef: ProtectedEnrollmentPollRef(token.ControllerID, token.EnrollmentID),
		})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ControllerID == all[j].ControllerID {
			return all[i].EnrollmentID < all[j].EnrollmentID
		}
		return all[i].ControllerID < all[j].ControllerID
	})
	result := make([]EnrollmentPollCredentialMetadata, 0, limit)
	for _, metadata := range all {
		if cursorControllerID != "" && (metadata.ControllerID < cursorControllerID || metadata.ControllerID == cursorControllerID && metadata.EnrollmentID <= cursorEnrollmentID) {
			continue
		}
		result = append(result, metadata)
		if len(result) == limit {
			last := result[len(result)-1]
			return EnrollmentPollCredentialPage{Credentials: result, NextCursor: enrollmentPollCursor(last.ControllerID, last.EnrollmentID)}, nil
		}
	}
	return EnrollmentPollCredentialPage{Credentials: result, Complete: true}, nil
}

func cloneControllerKeyBundle(bundle ControllerKeyBundle) ControllerKeyBundle {
	bundle.PrivateKey = append(ed25519.PrivateKey(nil), bundle.PrivateKey...)
	bundle.PublicKey = append(ed25519.PublicKey(nil), bundle.PublicKey...)
	return bundle
}

func clonePollToken(token EnrollmentPollToken) EnrollmentPollToken {
	token.Token = append([]byte(nil), token.Token...)
	return token
}

type fakeEnrollmentRepository struct {
	mutex                         sync.Mutex
	identity                      ControllerIdentity
	key                           ControllerKey
	enrollments                   map[string]Enrollment
	createEnrollmentErr           error
	createEnrollmentCommitThenErr bool
	completeCommitThenErr         bool
	markPolledCount               int
}

func (repository *fakeEnrollmentRepository) ActiveIdentity(context.Context) (ControllerIdentity, ControllerKey, error) {
	if repository.identity.ControllerID == "" {
		return ControllerIdentity{}, ControllerKey{}, ErrNotFound
	}
	key := repository.key
	key.PublicKey = append([]byte(nil), key.PublicKey...)
	return repository.identity, key, nil
}

func (repository *fakeEnrollmentRepository) CreateIdentity(_ context.Context, identity ControllerIdentity, key ControllerKey) error {
	if repository.identity.ControllerID != "" {
		return ErrConflict
	}
	repository.identity, repository.key = identity, key
	return nil
}

func (repository *fakeEnrollmentRepository) CreateEnrollment(_ context.Context, enrollment Enrollment) error {
	if repository.createEnrollmentCommitThenErr {
		repository.enrollments[enrollment.EnrollmentID] = enrollment
		return errors.New("lost commit response")
	}
	if repository.createEnrollmentErr != nil {
		return repository.createEnrollmentErr
	}
	repository.enrollments[enrollment.EnrollmentID] = enrollment
	return nil
}

func (repository *fakeEnrollmentRepository) Enrollment(_ context.Context, owner, enrollmentID string) (Enrollment, error) {
	enrollment, ok := repository.enrollments[enrollmentID]
	if !ok || enrollment.OwnerUserID != owner {
		return Enrollment{}, ErrNotFound
	}
	return enrollment, nil
}

func (repository *fakeEnrollmentRepository) MarkEnrollmentPolled(_ context.Context, owner, enrollmentID string, at time.Time) error {
	enrollment, ok := repository.enrollments[enrollmentID]
	if !ok || enrollment.OwnerUserID != owner || enrollment.State != EnrollmentPending {
		return ErrState
	}
	enrollment.LastPolledAt, enrollment.UpdatedAt = &at, at
	repository.enrollments[enrollmentID] = enrollment
	repository.markPolledCount++
	return nil
}

func (repository *fakeEnrollmentRepository) CompleteEnrollment(_ context.Context, owner, enrollmentID, state, bindingID, errorCode string, at time.Time) (Enrollment, error) {
	enrollment, ok := repository.enrollments[enrollmentID]
	if !ok || enrollment.OwnerUserID != owner {
		return Enrollment{}, ErrNotFound
	}
	if enrollment.State != EnrollmentPending {
		if enrollment.State == state && enrollment.BindingID == bindingID && enrollment.LastErrorCode == errorCode {
			return enrollment, nil
		}
		return Enrollment{}, ErrState
	}
	enrollment.State, enrollment.BindingID, enrollment.LastErrorCode = state, bindingID, errorCode
	enrollment.StateChangedAt, enrollment.UpdatedAt, enrollment.CompletedAt = at, at, &at
	repository.enrollments[enrollmentID] = enrollment
	if repository.completeCommitThenErr {
		repository.completeCommitThenErr = false
		return Enrollment{}, errors.New("lost commit response")
	}
	return enrollment, nil
}

func (repository *fakeEnrollmentRepository) ClearEnrollmentPollRef(_ context.Context, owner, enrollmentID string, at time.Time) error {
	enrollment, ok := repository.enrollments[enrollmentID]
	if !ok || enrollment.OwnerUserID != owner || enrollment.State == EnrollmentPending {
		return ErrState
	}
	enrollment.ProtectedPollRef, enrollment.PollRefClearedAt, enrollment.UpdatedAt = "", &at, at
	repository.enrollments[enrollmentID] = enrollment
	return nil
}

func (repository *fakeEnrollmentRepository) RecoverEnrollments(_ context.Context, now time.Time, limit int) ([]Enrollment, error) {
	var result []Enrollment
	for id, enrollment := range repository.enrollments {
		if enrollment.State == EnrollmentPending && !now.Before(enrollment.ExpiresAt) {
			enrollment.State, enrollment.StateChangedAt, enrollment.UpdatedAt, enrollment.CompletedAt = EnrollmentExpired, now, now, &now
			repository.enrollments[id] = enrollment
		}
		if enrollment.State != EnrollmentPending && enrollment.ProtectedPollRef != "" && len(result) < limit {
			result = append(result, enrollment)
		}
	}
	return result, nil
}
