package controllerrelay

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

const (
	repositoryTestControllerID = "11111111-1111-4111-8111-111111111111"
	repositoryTestKeyID        = "22222222-2222-4222-8222-222222222222"
	repositoryTestNewKeyID     = "33333333-3333-4333-8333-333333333333"
	repositoryTestEnrollmentID = "44444444-4444-4444-8444-444444444444"
	repositoryTestBindingID    = "55555555-5555-4555-8555-555555555555"
	repositoryTestRotationID   = "66666666-6666-4666-8666-666666666666"
)

func TestIdentityAndKeyRepositoryEnforcesCanonicalCASState(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	identity, key := testIdentity(now)
	if err := repository.CreateIdentity(context.Background(), identity, key); err != nil {
		t.Fatal(err)
	}
	gotIdentity, gotKey, err := repository.ActiveIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotIdentity.ControllerID != identity.ControllerID || gotKey.KeyID != key.KeyID || !bytes.Equal(gotKey.PublicKey, key.PublicKey) {
		t.Fatalf("active identity = %#v key=%#v", gotIdentity, gotKey)
	}
	gotKey.PublicKey[0] ^= 0xff
	again, err := repository.Key(context.Background(), repositoryTestControllerID, repositoryTestKeyID)
	if err != nil || again.PublicKey[0] != key.PublicKey[0] {
		t.Fatalf("stored public key aliased caller: %#v %v", again.PublicKey, err)
	}

	invalid := key
	invalid.KeyID = strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	invalid.ProtectedKeyRef = ProtectedKeyRef(repositoryTestControllerID, invalid.KeyID)
	if err := repository.CreateKey(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("noncanonical key ID error = %v", err)
	}

	pending := testPendingKey(now)
	if err := repository.CreateKey(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if err := repository.CASKeyState(context.Background(), repositoryTestControllerID, repositoryTestNewKeyID, KeyPending, KeyActive, now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active key error = %v", err)
	}
	if err := repository.CASKeyState(context.Background(), repositoryTestControllerID, repositoryTestNewKeyID, KeyActive, KeyRevoked, now.Add(2*time.Minute)); !errors.Is(err, ErrState) {
		t.Fatalf("stale key CAS error = %v", err)
	}
	if err := repository.CASControllerState(context.Background(), repositoryTestControllerID, ControllerActive, ControllerRevoked, ErrorKeyRevoked, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ActiveIdentity(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked active identity error = %v", err)
	}
	if err := repository.CASControllerState(context.Background(), repositoryTestControllerID, ControllerActive, ControllerRevoked, "ghu_secret_material", now.Add(4*time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe error code accepted: %v", err)
	}
}

func TestEnrollmentCompletionIsOwnerScopedAtomicAndCleanupOrdered(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	enrollment := testEnrollment(now)
	if err := repository.CreateEnrollment(context.Background(), enrollment); err != nil {
		t.Fatal(err)
	}
	mismatched := enrollment
	mismatched.ConnectionID = "connection-b"
	if err := repository.CreateEnrollment(context.Background(), mismatched); !errors.Is(err, ErrNotFound) {
		t.Fatalf("existing enrollment ID masked owner-mismatched source: %v", err)
	}
	if _, err := repository.Enrollment(context.Background(), "other-owner", enrollment.EnrollmentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read error = %v", err)
	}
	if err := repository.MarkEnrollmentPolled(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.CompleteEnrollment(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, EnrollmentAuthorized, repositoryTestBindingID, "", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != EnrollmentAuthorized || completed.BindingID != repositoryTestBindingID || completed.ProtectedPollRef != enrollment.ProtectedPollRef || completed.CompletedAt == nil {
		t.Fatalf("completed enrollment = %#v", completed)
	}
	if _, err := repository.CompleteEnrollment(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, EnrollmentAuthorized, repositoryTestBindingID, "", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("idempotent completion = %v", err)
	}
	if _, err := repository.CompleteEnrollment(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, EnrollmentDenied, "", "", now.Add(3*time.Minute)); !errors.Is(err, ErrState) {
		t.Fatalf("conflicting terminal completion = %v", err)
	}
	binding, err := repository.Binding(context.Background(), enrollment.OwnerUserID, repositoryTestBindingID)
	if err != nil || binding.State != BindingAuthorized || binding.RepositoryID != enrollment.RepositoryID {
		t.Fatalf("authorized binding = %#v %v", binding, err)
	}
	if err := repository.ClearEnrollmentPollRef(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.ClearEnrollmentPollRef(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("idempotent poll cleanup = %v", err)
	}
	cleaned, err := repository.Enrollment(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID)
	if err != nil || cleaned.ProtectedPollRef != "" || cleaned.PollRefClearedAt == nil {
		t.Fatalf("cleaned enrollment = %#v %v", cleaned, err)
	}
	if err := repository.MarkBindingAccessLost(context.Background(), enrollment.OwnerUserID, repositoryTestBindingID, ErrorSourceAccessLost, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkBindingRemovalPending(context.Background(), enrollment.OwnerUserID, repositoryTestBindingID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkBindingRemoved(context.Background(), enrollment.OwnerUserID, repositoryTestBindingID, now.Add(8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkBindingAccessLost(context.Background(), enrollment.OwnerUserID, repositoryTestBindingID, ErrorSourceAccessLost, now.Add(9*time.Minute)); !errors.Is(err, ErrState) {
		t.Fatalf("terminal binding transition = %v", err)
	}
}

func TestEnrollmentConflictsAndRecoveryRetainProtectedReference(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	enrollment := testEnrollment(now)
	enrollment.ExpiresAt = now.Add(time.Minute)
	if err := repository.CreateEnrollment(context.Background(), enrollment); err != nil {
		t.Fatal(err)
	}
	duplicate := enrollment
	duplicate.EnrollmentID = "77777777-7777-4777-8777-777777777777"
	duplicate.ProtectedPollRef = ProtectedEnrollmentPollRef(duplicate.ControllerID, duplicate.EnrollmentID)
	if err := repository.CreateEnrollment(context.Background(), duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pending identity error = %v", err)
	}
	invalid := enrollment
	invalid.EnrollmentID = "88888888-8888-4888-8888-888888888888"
	invalid.InstallationID = 0
	invalid.ProtectedPollRef = "../poll-token"
	if err := repository.CreateEnrollment(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid enrollment error = %v", err)
	}
	due, err := repository.RecoverEnrollments(context.Background(), now.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].State != EnrollmentExpired || due[0].ProtectedPollRef != enrollment.ProtectedPollRef || due[0].PollRefClearedAt != nil {
		t.Fatalf("recovery rows = %#v", due)
	}
	if err := repository.MarkEnrollmentPolled(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, now.Add(3*time.Minute)); !errors.Is(err, ErrState) {
		t.Fatalf("terminal poll error = %v", err)
	}
}

func TestRotationMetadataUsesStrictForwardCASAndNoProtocolMaterial(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	if err := repository.CreateKey(context.Background(), testPendingKey(now)); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: repositoryTestNewKeyID, State: RotationPrepare, ExpiresAt: now.Add(time.Hour), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	if err := repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPrepare, RotationPropose, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPrepare, RotationConfirm, "", now.Add(2*time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("skipped rotation stage = %v", err)
	}
	if err := repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPropose, RotationFailed, "raw_signature_material", now.Add(3*time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe rotation code = %v", err)
	}
	if err := repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPropose, RotationFailed, ErrorRotationFailed, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := repository.Rotation(context.Background(), repositoryTestControllerID, repositoryTestRotationID)
	if err != nil || got.State != RotationFailed || got.CompletedAt == nil || got.LastErrorCode != ErrorRotationFailed {
		t.Fatalf("failed rotation = %#v %v", got, err)
	}
}

func TestRepositoryRejectsSensitiveValuesBeforeSQLite(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	enrollment := testEnrollment(now)
	if err := repository.CreateEnrollment(context.Background(), enrollment); err != nil {
		t.Fatal(err)
	}
	sentinels := []string{"ghu_repository_test_secret", "oauth-pkce-verifier-sentinel", "raw-websocket-frame-sentinel", "ed25519-private-key-sentinel"}
	for _, sentinel := range sentinels {
		if _, err := repository.CompleteEnrollment(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, EnrollmentFailed, "", sentinel, now.Add(time.Minute)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("sensitive value %q error = %v", sentinel, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(body))
		for _, sentinel := range sentinels {
			if strings.Contains(lower, strings.ToLower(sentinel)) {
				t.Fatalf("SQLite artifact %s contains forbidden sentinel %q", entry.Name(), sentinel)
			}
		}
	}
}

func newRepositoryHarness(t *testing.T) (*Repository, string, time.Time) {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	for _, owner := range []string{"owner", "other-owner"} {
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, owner, owner, "hash", timestamp(now), timestamp(now)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES('connection-a','owner','github','connected','42','octocat',1,?,?,?,?,?)`, timestamp(now.Add(time.Hour)), timestamp(now.Add(24*time.Hour)), timestamp(now), timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES('connection-b','other-owner','github','connected','43','other',1,?,?,?,?,?)`, timestamp(now.Add(time.Hour)), timestamp(now.Add(24*time.Hour)), timestamp(now), timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	return NewRepository(db), root, now
}

func testIdentity(now time.Time) (ControllerIdentity, ControllerKey) {
	activated, confirmed := now, now
	return ControllerIdentity{ControllerID: repositoryTestControllerID, State: ControllerActive, CreatedAt: now, UpdatedAt: now}, ControllerKey{KeyID: repositoryTestKeyID, ControllerID: repositoryTestControllerID, PublicKey: bytes.Repeat([]byte{0x11}, 32), Algorithm: KeyAlgorithmEd25519, State: KeyActive, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID), CreatedAt: now, UpdatedAt: now, ActivatedAt: &activated, PossessionConfirmedAt: &confirmed}
}

func testPendingKey(now time.Time) ControllerKey {
	return ControllerKey{KeyID: repositoryTestNewKeyID, ControllerID: repositoryTestControllerID, PublicKey: bytes.Repeat([]byte{0x22}, 32), Algorithm: KeyAlgorithmEd25519, State: KeyPending, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, repositoryTestNewKeyID), CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
}

func createTestIdentity(t *testing.T, repository *Repository, now time.Time) {
	t.Helper()
	identity, key := testIdentity(now)
	if err := repository.CreateIdentity(context.Background(), identity, key); err != nil {
		t.Fatal(err)
	}
}

func testEnrollment(now time.Time) Enrollment {
	return Enrollment{EnrollmentID: repositoryTestEnrollmentID, OwnerUserID: "owner", ConnectionID: "connection-a", ControllerID: repositoryTestControllerID, KeyID: repositoryTestKeyID, InstallationID: 101, RepositoryID: 202, Purpose: EnrollmentPurpose, ProtectedPollRef: ProtectedEnrollmentPollRef(repositoryTestControllerID, repositoryTestEnrollmentID), State: EnrollmentPending, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute), StateChangedAt: now, UpdatedAt: now}
}
