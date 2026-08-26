package controllerrelay

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestControllerKeyIORepositoryFencesWriterAndSerializesDirectoryCleanup(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	lease := testControllerKeyWriteLease(now.Add(time.Minute), "44000000-0000-4000-8000-000000000010", "55000000-0000-4000-8000-000000000010", bytes.Repeat([]byte{0x44}, 32))
	defer clear(lease.PublicKey)
	if err := repository.BeginControllerKeyWrite(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	cleanup := ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(lease.ControllerID), ControllerID: lease.ControllerID, LeaseID: "66000000-0000-4000-8000-000000000010", Operation: ControllerKeyIOKeyCleanup,
		Phase: ControllerKeyIORecovery, Fence: 1, LeaseExpiresAt: lease.CreatedAt.Add(2 * time.Minute), KeyID: lease.KeyID,
		ProtectedKeyRef: lease.ProtectedKeyRef, CreatedAt: lease.CreatedAt, UpdatedAt: lease.CreatedAt,
	}
	if err := repository.AcquireControllerKeyCleanupLease(context.Background(), cleanup); !errors.Is(err, ErrConflict) {
		t.Fatalf("active writer did not block cleanup: %v", err)
	}
	if _, err := repository.ClaimExpiredControllerKeyIOLease(context.Background(), lease, cleanup.LeaseID, lease.CreatedAt.Add(time.Minute), lease.CreatedAt.Add(2*time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unexpired writer claim error=%v", err)
	}
	page, err := repository.ExpiredControllerKeyIOLeases(context.Background(), "", lease.LeaseExpiresAt.Add(-time.Second), 1)
	if err != nil || len(page.Leases) != 0 || !page.Complete {
		t.Fatalf("premature expiry page=%#v err=%v", page, err)
	}
	claimAt := lease.LeaseExpiresAt.Add(time.Second)
	page, err = repository.ExpiredControllerKeyIOLeases(context.Background(), "", claimAt, 1)
	if err != nil || len(page.Leases) != 1 || page.Leases[0].LeaseID != lease.LeaseID {
		t.Fatalf("expired page=%#v err=%v", page, err)
	}
	clear(page.Leases[0].PublicKey)
	claimed, err := repository.ClaimExpiredControllerKeyIOLease(context.Background(), lease, cleanup.LeaseID, claimAt, claimAt.Add(2*time.Minute))
	if err != nil || claimed.Fence != 2 || claimed.Phase != ControllerKeyIORecovery {
		t.Fatalf("claimed lease=%#v err=%v", claimed, err)
	}
	defer clear(claimed.PublicKey)
	key := ControllerKey{KeyID: lease.KeyID, ControllerID: lease.ControllerID, PublicKey: append([]byte(nil), lease.PublicKey...), Algorithm: KeyAlgorithmEd25519, State: KeyPending, ProtectedKeyRef: lease.ProtectedKeyRef, CreatedAt: lease.CreatedAt, UpdatedAt: lease.CreatedAt}
	rotation := KeyRotation{RotationID: lease.RotationID, ControllerID: lease.ControllerID, OldKeyID: lease.OldKeyID, NewKeyID: lease.KeyID, State: RotationPrepare, ExpiresAt: lease.CreatedAt.Add(15 * time.Minute), StateChangedAt: lease.CreatedAt, CreatedAt: lease.CreatedAt, UpdatedAt: lease.CreatedAt}
	if err = repository.MaterializePendingKeyAndRotation(context.Background(), lease, key, rotation, claimAt); !errors.Is(err, ErrState) {
		t.Fatalf("stale writer materialization error=%v", err)
	}
	clear(key.PublicKey)
	if err = repository.FinishControllerKeyIOLease(context.Background(), lease); !errors.Is(err, ErrState) {
		t.Fatalf("stale writer finished newer lease: %v", err)
	}
	if err = repository.FinishControllerKeyIOLease(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ExpiredControllerKeyIOLeases(context.Background(), "not-a-cursor", claimAt, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid lease cursor error=%v", err)
	}
}

func TestControllerKeyIORepositoryPagesSingletonIdentityLeaseBeforeControllerScopes(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	identityLease, bundle, _, _ := testIdentityWriteCandidate(now, 0x5a, 8)
	defer clear(identityLease.PublicKey)
	bundle.Destroy()
	if err := repository.BeginControllerIdentityWrite(context.Background(), identityLease); err != nil {
		t.Fatal(err)
	}
	controllerID := "91000000-0000-4000-8000-000000000009"
	keyID := "92000000-0000-4000-8000-000000000009"
	cleanup := ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(controllerID), ControllerID: controllerID, LeaseID: "93000000-0000-4000-8000-000000000009",
		Operation: ControllerKeyIOKeyCleanup, Phase: ControllerKeyIORecovery, Fence: 1, LeaseExpiresAt: now.Add(5 * time.Minute),
		KeyID: keyID, ProtectedKeyRef: ProtectedKeyRef(controllerID, keyID), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.AcquireControllerKeyCleanupLease(context.Background(), cleanup); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ExpiredControllerKeyIOLeases(context.Background(), "", now.Add(6*time.Minute), 1)
	if err != nil || len(page.Leases) != 1 || page.Leases[0].Operation != ControllerKeyIOIdentityWrite || page.NextCursor == "" || page.Complete {
		t.Fatalf("identity-first page=%#v err=%v", page, err)
	}
	clear(page.Leases[0].PublicKey)
	next, err := repository.ExpiredControllerKeyIOLeases(context.Background(), page.NextCursor, now.Add(6*time.Minute), 1)
	if err != nil || len(next.Leases) != 1 || next.Leases[0].Operation != ControllerKeyIOKeyCleanup {
		t.Fatalf("controller continuation page=%#v err=%v", next, err)
	}
}

func TestControllerKeyIOProductionRecoveryQueryPlans(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	controllerID := "94000000-0000-4000-8000-000000000009"
	keyID := "95000000-0000-4000-8000-000000000009"

	expiredPlan := explainRepositoryQueryPlan(t, repository,
		controllerKeyIOLeaseSelect+` WHERE lease_expires_at<=? AND (?='' OR scope_key>?) ORDER BY scope_key LIMIT ?`,
		timestamp(now), "", "", 1,
	)
	if !strings.Contains(expiredPlan, "using index relay_controller_key_io_expiry") {
		t.Fatalf("expired lease page did not use the bounded expiry index: %s", expiredPlan)
	}

	revokedPlan := explainRepositoryQueryPlan(t, repository,
		revokedRotationKeyCleanupSelect+` AND k.controller_id=? AND k.key_id=? AND k.protected_key_ref=?`,
		controllerID, keyID, ProtectedKeyRef(controllerID, keyID),
	)
	for _, requiredIndex := range []string{
		"using index sqlite_autoindex_relay_controller_keys_2",
		"using covering index relay_key_rotation_old_reference",
		"using covering index relay_key_rotation_new_reference",
	} {
		if !strings.Contains(revokedPlan, requiredIndex) {
			t.Fatalf("exact revoked candidate did not use %q: %s", requiredIndex, revokedPlan)
		}
	}
}

func explainRepositoryQueryPlan(t *testing.T, repository *Repository, query string, arguments ...any) string {
	t.Helper()
	rows, err := repository.db.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	details := make([]string, 0, 8)
	for rows.Next() {
		var id, parent, ignored int
		var detail string
		if err = rows.Scan(&id, &parent, &ignored, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(strings.Join(details, "\n"))
}
