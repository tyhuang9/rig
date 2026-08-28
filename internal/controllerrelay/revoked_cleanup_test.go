package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRevokedCleanupSerializesAgainstWriterOnEveryDeletionPath(t *testing.T) {
	t.Run("completed rotation", func(t *testing.T) {
		repository, _, now := newRepositoryHarness(t)
		createTestIdentity(t, repository, now)
		credentials := newMemoryControlCredentials()
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
		newKeyID := "a1000000-0000-4000-8000-000000000001"
		rotationID := "a2000000-0000-4000-8000-000000000001"
		newKey := testPendingKey(now)
		newKey.KeyID = newKeyID
		newKey.ProtectedKeyRef = ProtectedKeyRef(repositoryTestControllerID, newKeyID)
		if err := repository.CreateKey(context.Background(), newKey); err != nil {
			t.Fatal(err)
		}
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, newKeyID, newKey.PublicKey)
		completedAt := now.Add(time.Minute)
		tx, err := repository.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE relay_controller_keys SET state='revoked',updated_at=?,revoked_at=? WHERE controller_id=? AND key_id=?`, timestamp(completedAt), timestamp(completedAt), repositoryTestControllerID, repositoryTestKeyID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE relay_controller_keys SET state='active',updated_at=?,activated_at=?,possession_confirmed_at=? WHERE controller_id=? AND key_id=?`, timestamp(completedAt), timestamp(completedAt), timestamp(completedAt), repositoryTestControllerID, newKeyID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,created_at,updated_at,completed_at) VALUES(?,?,?,?, 'completed',?,?,?,?,?)`, rotationID, repositoryTestControllerID, repositoryTestKeyID, newKeyID, timestamp(now.Add(time.Hour)), timestamp(completedAt), timestamp(now), timestamp(completedAt), timestamp(completedAt)); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		ready := SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: newKeyID, LastReadyAt: &completedAt, LastSeenAt: &completedAt, StateChangedAt: completedAt, UpdatedAt: completedAt}
		if err = repository.AdvanceSessionStatus(context.Background(), 0, 0, ready); err != nil {
			t.Fatal(err)
		}
		writer := testWriteLeaseForActiveKey(now.Add(2*time.Minute), newKeyID, 1)
		if err = repository.BeginControllerKeyWrite(context.Background(), writer); err != nil {
			t.Fatal(err)
		}
		defer clear(writer.PublicKey)
		service := newTestControlService(t, repository, credentials, now.Add(2*time.Minute))
		if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, newKeyID, 1, 1); err != nil {
			t.Fatal(err)
		}
		assertProtectedKeyAndUncleared(t, repository, credentials, repositoryTestKeyID)
		if err = repository.FinishControllerKeyIOLease(context.Background(), writer); err != nil {
			t.Fatal(err)
		}
		if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, newKeyID, 1, 1); err != nil {
			t.Fatal(err)
		}
		assertProtectedKeyCleared(t, repository, credentials, repositoryTestKeyID)
	})

	t.Run("recovery", func(t *testing.T) {
		repository, _, now := newRepositoryHarness(t)
		createTestIdentity(t, repository, now)
		credentials := newMemoryControlCredentials()
		candidate := seedFailedRevokedCleanupCandidate(t, repository, credentials, now, 2)
		writer := testWriteLeaseForActiveKey(now.Add(time.Minute), repositoryTestKeyID, 2)
		if err := repository.BeginControllerKeyWrite(context.Background(), writer); err != nil {
			t.Fatal(err)
		}
		defer clear(writer.PublicKey)
		service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
		page, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
		if err != nil || page.Cleaned != 0 {
			t.Fatalf("writer-owned recovery page=%#v err=%v", page, err)
		}
		assertProtectedKeyAndUncleared(t, repository, credentials, candidate.KeyID)
		if err = repository.FinishControllerKeyIOLease(context.Background(), writer); err != nil {
			t.Fatal(err)
		}
		page, err = service.RecoverRevokedControllerKeys(context.Background(), "", 10)
		if err != nil || page.Cleaned != 1 {
			t.Fatalf("released recovery page=%#v err=%v", page, err)
		}
		assertProtectedKeyCleared(t, repository, credentials, candidate.KeyID)
	})

	t.Run("failed rotation", func(t *testing.T) {
		repository, _, now := newRepositoryHarness(t)
		createTestIdentity(t, repository, now)
		credentials := newMemoryControlCredentials()
		service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
		proposal, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
		if err != nil {
			t.Fatal(err)
		}
		rotation, err := repository.Rotation(context.Background(), repositoryTestControllerID, proposal.RotationID)
		if err != nil {
			t.Fatal(err)
		}
		failed := make(chan struct{})
		release := make(chan struct{})
		paused := &pauseAfterFailExpiredRepository{sessionControlRepository: repository, failed: failed, release: release}
		failingService := newTestControlServiceForRepository(t, paused, credentials, now.Add(30*time.Minute))
		result := make(chan error, 1)
		go func() {
			result <- failingService.failExpiredRotation(context.Background(), rotation, now.Add(30*time.Minute))
		}()
		<-failed
		writer := testWriteLeaseForActiveKey(now.Add(30*time.Minute), repositoryTestKeyID, 3)
		if err = repository.BeginControllerKeyWrite(context.Background(), writer); err != nil {
			t.Fatal(err)
		}
		defer clear(writer.PublicKey)
		close(release)
		if err = <-result; err != nil {
			t.Fatal(err)
		}
		assertProtectedKeyAndUncleared(t, repository, credentials, proposal.NewKeyID)
		if err = repository.FinishControllerKeyIOLease(context.Background(), writer); err != nil {
			t.Fatal(err)
		}
		page, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
		if err != nil || page.Cleaned != 1 {
			t.Fatalf("failed rotation released recovery page=%#v err=%v", page, err)
		}
		assertProtectedKeyCleared(t, repository, credentials, proposal.NewKeyID)
	})
}

func TestCompetingRevokedCleanupsDeleteAndCountExactlyOnce(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	candidate := seedFailedRevokedCleanupCandidate(t, repository, credentials, now, 4)
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := &blockingRevokedRemovalCredentials{sessionControlCredentials: credentials, keyID: candidate.KeyID, started: started, release: release}
	first := newTestControlServiceForRepository(t, repository, blocking, now.Add(time.Minute))
	second := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	type outcome struct {
		page RevokedKeyCleanupPage
		err  error
	}
	firstResult := make(chan outcome, 1)
	go func() {
		page, err := first.RecoverRevokedControllerKeys(context.Background(), "", 10)
		firstResult <- outcome{page: page, err: err}
	}()
	<-started
	secondPage, err := second.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || secondPage.Cleaned != 0 || len(secondPage.Candidates) != 1 {
		t.Fatalf("competing cleanup page=%#v err=%v", secondPage, err)
	}
	close(release)
	winner := <-firstResult
	if winner.err != nil || winner.page.Cleaned != 1 {
		t.Fatalf("winning cleanup page=%#v err=%v", winner.page, winner.err)
	}
	if winner.page.Cleaned+secondPage.Cleaned != 1 || credentials.removeCalls != 1 {
		t.Fatalf("competing cleanup counts winner=%d competitor=%d removes=%d", winner.page.Cleaned, secondPage.Cleaned, credentials.removeCalls)
	}
	assertProtectedKeyCleared(t, repository, credentials, candidate.KeyID)
	retry, err := second.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || retry.Cleaned != 0 || len(retry.Candidates) != 0 {
		t.Fatalf("competing cleanup retry=%#v err=%v", retry, err)
	}
}

func TestRevokedCleanupRecoversCrashAfterDeleteMarkAndFinish(t *testing.T) {
	for _, test := range []struct {
		name       string
		wrap       func(sessionControlRepository) sessionControlRepository
		wantError  bool
		wantLease  int
		wantMarked bool
	}{
		{name: "after delete", wrap: func(repository sessionControlRepository) sessionControlRepository {
			return &failRevokedMarkRepository{sessionControlRepository: repository}
		}, wantError: true, wantLease: 1},
		{name: "after mark", wrap: func(repository sessionControlRepository) sessionControlRepository {
			return &failRevokedFinishRepository{sessionControlRepository: repository}
		}, wantError: true, wantLease: 1, wantMarked: true},
		{name: "after finish", wrap: func(repository sessionControlRepository) sessionControlRepository { return repository }, wantMarked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _, now := newRepositoryHarness(t)
			createTestIdentity(t, repository, now)
			credentials := newMemoryControlCredentials()
			candidate := seedFailedRevokedCleanupCandidate(t, repository, credentials, now, 5)
			service := newTestControlServiceForRepository(t, test.wrap(repository), credentials, now.Add(time.Minute))
			page, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
			if (err != nil) != test.wantError || page.Cleaned != 1 {
				t.Fatalf("crash boundary page=%#v err=%v", page, err)
			}
			var leases int
			if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases WHERE operation='revoked_cleanup'`).Scan(&leases); err != nil || leases != test.wantLease {
				t.Fatalf("crash boundary leases=%d err=%v", leases, err)
			}
			var marker any
			if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE controller_id=? AND key_id=?`, candidate.ControllerID, candidate.KeyID).Scan(&marker); err != nil || (marker != nil) != test.wantMarked {
				t.Fatalf("crash boundary marker=%v err=%v", marker, err)
			}
			if credentials.has(candidate.ProtectedKeyRef) {
				t.Fatal("confirmed deletion did not remove exact protected key")
			}
			restarted := newTestControlService(t, repository, credentials, now.Add(4*time.Minute))
			recovered, recoverErr := restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
			if recoverErr != nil || recovered.Cleaned != 0 {
				t.Fatalf("crash restart recovery=%#v err=%v", recovered, recoverErr)
			}
			assertProtectedKeyCleared(t, repository, credentials, candidate.KeyID)
			if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases WHERE operation='revoked_cleanup'`).Scan(&leases); err != nil || leases != 0 {
				t.Fatalf("crash recovery leases=%d err=%v", leases, err)
			}
		})
	}
}

func TestStartKeyRotationRetriesExactWriteAfterCleanupReleases(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	cleanup := ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(repositoryTestControllerID), ControllerID: repositoryTestControllerID,
		LeaseID: "d1000000-0000-4000-8000-000000000001", Operation: ControllerKeyIORevokedCleanup,
		Phase: ControllerKeyIORecovery, Fence: 1, LeaseExpiresAt: now.Add(5 * time.Minute),
		KeyID: "d2000000-0000-4000-8000-000000000001", ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, "d2000000-0000-4000-8000-000000000001"), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.AcquireControllerKeyCleanupLease(context.Background(), cleanup); err != nil {
		t.Fatal(err)
	}
	conflicted := make(chan struct{})
	observed := &observeWriteConflictRepository{sessionControlRepository: repository, conflicted: conflicted}
	service := newTestControlServiceForRepository(t, observed, credentials, now.Add(time.Minute))
	type outcome struct {
		proposal string
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		proposal, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
		rotationID := ""
		if proposal != nil {
			rotationID = proposal.RotationID
		}
		result <- outcome{proposal: rotationID, err: err}
	}()
	<-conflicted
	if err := repository.FinishControllerKeyIOLease(context.Background(), cleanup); err != nil {
		t.Fatal(err)
	}
	started := <-result
	if started.err != nil || started.proposal == "" {
		t.Fatalf("cleanup-release write retry proposal=%q err=%v", started.proposal, started.err)
	}
	if observed.calls < 2 {
		t.Fatalf("write lease was not retried after cleanup release: calls=%d", observed.calls)
	}
	var writes, rotations int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases WHERE operation='write'`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_key_rotations WHERE state IN ('prepare','propose','confirm','new_key_auth','finalize')`).Scan(&rotations); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || rotations != 1 || len(credentials.keys) != 1 {
		t.Fatalf("write retry state writes=%d live rotations=%d keys=%d", writes, rotations, len(credentials.keys))
	}
}

func TestControllerKeyRecoverySweepTracksCompletedStreamsAndRetriesNextSweep(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	base := newMemoryControlCredentials()
	candidate := seedFailedRevokedCleanupCandidate(t, repository, base, now, 6)
	for index, controllerID := range []string{
		"e1000000-0000-4000-8000-000000000001",
		"e1000000-0000-4000-8000-000000000002",
	} {
		keyID := fmt.Sprintf("e2000000-0000-4000-8000-%012d", index+1)
		seedMemoryControllerKey(t, base, controllerID, keyID, bytes.Repeat([]byte{byte(0x70 + index)}, ed25519.PublicKeySize))
	}
	tempControllerID := "e3000000-0000-4000-8000-000000000003"
	tempName := ".hostd-secret-000000003"
	base.temporary[tempControllerID+"\x00"+tempName] = ControllerKeyTemporaryArtifact{ControllerID: tempControllerID, Name: tempName}
	credentials := &sweepTrackingCredentials{sessionControlCredentials: base, persistentKeyID: candidate.KeyID, persistent: true, failCredentialInventory: 1}
	trackedRepository := &sweepTrackingRepository{sessionControlRepository: repository}
	service := newTestControlServiceForRepository(t, trackedRepository, credentials, now.Add(time.Minute))
	cursor := ControllerKeyRecoveryCursor{}
	totalCleaned := 0
	completed := false
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, _ := service.RecoverControllerKeysPage(context.Background(), cursor, 1)
		totalCleaned += page.Cleaned
		cursor = page.NextCursor
		if page.Complete {
			completed = true
			break
		}
	}
	if !completed || totalCleaned != 3 {
		t.Fatalf("mixed recovery sweep completed=%v cleaned=%d cursor=%#v", completed, totalCleaned, cursor)
	}
	if trackedRepository.leaseCalls != 1 || trackedRepository.revokedCalls != 2 || credentials.temporaryCalls != 2 || credentials.credentialCalls != 5 || credentials.removalAttempts != 1 {
		t.Fatalf("completed streams restarted leases=%d revoked=%d credentials=%d temporary=%d failed-removals=%d", trackedRepository.leaseCalls, trackedRepository.revokedCalls, credentials.credentialCalls, credentials.temporaryCalls, credentials.removalAttempts)
	}
	if !cursor.LeasesComplete || !cursor.RevokedComplete || !cursor.CredentialsComplete || !cursor.TemporaryComplete || !base.has(candidate.ProtectedKeyRef) {
		t.Fatalf("terminal sweep cursor=%#v persistent candidate present=%v", cursor, base.has(candidate.ProtectedKeyRef))
	}

	// A new zero cursor is a new sweep: the persistent candidate is retried,
	// while the terminal cursor itself performs no more work.
	terminal, err := service.RecoverControllerKeysPage(context.Background(), cursor, 1)
	if err != nil || !terminal.Complete || credentials.removalAttempts != 1 {
		t.Fatalf("terminal cursor restarted work page=%#v err=%v attempts=%d", terminal, err, credentials.removalAttempts)
	}
	nextSweep, err := service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 1)
	if err == nil || credentials.removalAttempts != 2 || nextSweep.NextCursor.RevokedCursor == "" {
		t.Fatalf("next sweep did not retry persistent candidate page=%#v err=%v attempts=%d", nextSweep, err, credentials.removalAttempts)
	}
	credentials.persistent = false
	cursor = ControllerKeyRecoveryCursor{}
	nextSweep = ControllerKeyRecoveryPage{}
	for pageNumber := 0; pageNumber < 10 && !nextSweep.Complete; pageNumber++ {
		nextSweep, err = service.RecoverControllerKeysPage(context.Background(), cursor, 1)
		cursor = nextSweep.NextCursor
		if err != nil {
			t.Fatal(err)
		}
	}
	if !nextSweep.Complete || base.has(candidate.ProtectedKeyRef) {
		t.Fatalf("resolved next sweep page=%#v candidate present=%v", nextSweep, base.has(candidate.ProtectedKeyRef))
	}
}

type pauseAfterFailExpiredRepository struct {
	sessionControlRepository
	failed  chan struct{}
	release chan struct{}
}

type observeWriteConflictRepository struct {
	sessionControlRepository
	mu         sync.Mutex
	calls      int
	conflicted chan struct{}
	once       sync.Once
}

func (repository *observeWriteConflictRepository) BeginControllerKeyWrite(ctx context.Context, lease ControllerKeyIOLease) error {
	repository.mu.Lock()
	repository.calls++
	repository.mu.Unlock()
	err := repository.sessionControlRepository.BeginControllerKeyWrite(ctx, lease)
	if errors.Is(err, ErrConflict) {
		repository.once.Do(func() { close(repository.conflicted) })
	}
	return err
}

type sweepTrackingRepository struct {
	sessionControlRepository
	leaseCalls   int
	revokedCalls int
}

func (repository *sweepTrackingRepository) ExpiredControllerKeyIOLeases(ctx context.Context, cursor string, at time.Time, limit int) (ControllerKeyIOLeasePage, error) {
	repository.leaseCalls++
	return repository.sessionControlRepository.ExpiredControllerKeyIOLeases(ctx, cursor, at, limit)
}

func (repository *sweepTrackingRepository) RevokedRotationKeyCleanupCandidates(ctx context.Context, cursor string, limit int) (RevokedKeyCleanupPage, error) {
	repository.revokedCalls++
	return repository.sessionControlRepository.RevokedRotationKeyCleanupCandidates(ctx, cursor, limit)
}

type sweepTrackingCredentials struct {
	sessionControlCredentials
	persistentKeyID         string
	persistent              bool
	failCredentialInventory int
	credentialCalls         int
	temporaryCalls          int
	removalAttempts         int
}

func (credentials *sweepTrackingCredentials) ControllerKeyCredentials(cursor string, limit int) (ControllerKeyCredentialPage, error) {
	credentials.credentialCalls++
	if credentials.failCredentialInventory > 0 {
		credentials.failCredentialInventory--
		return ControllerKeyCredentialPage{}, errors.New("transient credential inventory failure")
	}
	return credentials.sessionControlCredentials.ControllerKeyCredentials(cursor, limit)
}

func (credentials *sweepTrackingCredentials) ControllerKeyTemporaryArtifacts(cursor string, limit int) (ControllerKeyTemporaryArtifactPage, error) {
	credentials.temporaryCalls++
	return credentials.sessionControlCredentials.ControllerKeyTemporaryArtifacts(cursor, limit)
}

func (credentials *sweepTrackingCredentials) RemoveControllerKeyWithResult(controllerID, keyID string) (bool, error) {
	if keyID == credentials.persistentKeyID {
		credentials.removalAttempts++
		if credentials.persistent {
			return false, errors.New("persistent revoked cleanup failure")
		}
	}
	return credentials.sessionControlCredentials.RemoveControllerKeyWithResult(controllerID, keyID)
}

func (repository *pauseAfterFailExpiredRepository) FailExpiredRotation(ctx context.Context, controllerID, rotationID string, at time.Time) error {
	if err := repository.sessionControlRepository.FailExpiredRotation(ctx, controllerID, rotationID, at); err != nil {
		return err
	}
	close(repository.failed)
	<-repository.release
	return nil
}

type blockingRevokedRemovalCredentials struct {
	sessionControlCredentials
	keyID   string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (credentials *blockingRevokedRemovalCredentials) RemoveControllerKeyWithResult(controllerID, keyID string) (bool, error) {
	if keyID == credentials.keyID {
		credentials.once.Do(func() { close(credentials.started) })
		<-credentials.release
	}
	return credentials.sessionControlCredentials.RemoveControllerKeyWithResult(controllerID, keyID)
}

type failRevokedMarkRepository struct {
	sessionControlRepository
	once sync.Once
}

func (repository *failRevokedMarkRepository) MarkRevokedControllerKeyCleared(ctx context.Context, candidate RevokedKeyCleanupCandidate, at time.Time) error {
	failed := false
	repository.once.Do(func() { failed = true })
	if failed {
		return errors.New("crash after revoked key delete")
	}
	return repository.sessionControlRepository.MarkRevokedControllerKeyCleared(ctx, candidate, at)
}

type failRevokedFinishRepository struct {
	sessionControlRepository
	once sync.Once
}

func (repository *failRevokedFinishRepository) FinishControllerKeyIOLease(ctx context.Context, lease ControllerKeyIOLease) error {
	if lease.Operation == ControllerKeyIORevokedCleanup {
		failed := false
		repository.once.Do(func() { failed = true })
		if failed {
			return errors.New("crash after revoked key marker")
		}
	}
	return repository.sessionControlRepository.FinishControllerKeyIOLease(ctx, lease)
}

func seedFailedRevokedCleanupCandidate(t *testing.T, repository *Repository, credentials *memoryControlCredentials, now time.Time, suffix int) RevokedKeyCleanupCandidate {
	t.Helper()
	keyID := fmt.Sprintf("b1000000-0000-4000-8000-%012d", suffix)
	rotationID := fmt.Sprintf("b2000000-0000-4000-8000-%012d", suffix)
	revokedAt := now.Add(time.Second)
	key := ControllerKey{
		KeyID: keyID, ControllerID: repositoryTestControllerID, PublicKey: bytes.Repeat([]byte{byte(0x30 + suffix)}, ed25519.PublicKeySize),
		Algorithm: KeyAlgorithmEd25519, State: KeyRevoked, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, keyID),
		CreatedAt: now, UpdatedAt: revokedAt, RevokedAt: &revokedAt,
	}
	if err := repository.CreateKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, keyID, key.PublicKey)
	if _, err := repository.db.Exec(`INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,last_error_code,created_at,updated_at,completed_at) VALUES(?,?,?,?, 'failed',?,?, 'rotation_failed',?,?,?)`, rotationID, repositoryTestControllerID, repositoryTestKeyID, keyID, timestamp(now.Add(time.Hour)), timestamp(revokedAt), timestamp(now), timestamp(revokedAt), timestamp(revokedAt)); err != nil {
		t.Fatal(err)
	}
	return RevokedKeyCleanupCandidate{ControllerID: repositoryTestControllerID, KeyID: keyID, ProtectedKeyRef: key.ProtectedKeyRef}
}

func testWriteLeaseForActiveKey(now time.Time, activeKeyID string, suffix int) ControllerKeyIOLease {
	keyID := fmt.Sprintf("c1000000-0000-4000-8000-%012d", suffix)
	rotationID := fmt.Sprintf("c2000000-0000-4000-8000-%012d", suffix)
	leaseID := fmt.Sprintf("c3000000-0000-4000-8000-%012d", suffix)
	return ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(repositoryTestControllerID), ControllerID: repositoryTestControllerID, LeaseID: leaseID,
		Operation: ControllerKeyIOWrite, Phase: ControllerKeyIOActive, Fence: 1, LeaseExpiresAt: now.Add(5 * time.Minute),
		KeyID: keyID, RotationID: rotationID, OldKeyID: activeKeyID, PublicKey: bytes.Repeat([]byte{0x7a}, ed25519.PublicKeySize),
		ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, keyID), CreatedAt: now, UpdatedAt: now,
	}
}

func newTestControlServiceForRepository(t *testing.T, repository sessionControlRepository, credentials sessionControlCredentials, now time.Time) *SessionControlService {
	t.Helper()
	config := DefaultSessionControlConfig()
	config.Now = func() time.Time { return now }
	service, err := NewSessionControlService(repository, credentials, config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertProtectedKeyAndUncleared(t *testing.T, repository *Repository, credentials *memoryControlCredentials, keyID string) {
	t.Helper()
	ref := ProtectedKeyRef(repositoryTestControllerID, keyID)
	if !credentials.has(ref) {
		t.Fatalf("protected key %s was deleted without cleanup ownership", keyID)
	}
	var marker any
	if err := repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE controller_id=? AND key_id=?`, repositoryTestControllerID, keyID).Scan(&marker); err != nil || marker != nil {
		t.Fatalf("protected key %s marker=%v err=%v", keyID, marker, err)
	}
}

func assertProtectedKeyCleared(t *testing.T, repository *Repository, credentials *memoryControlCredentials, keyID string) {
	t.Helper()
	ref := ProtectedKeyRef(repositoryTestControllerID, keyID)
	if credentials.has(ref) {
		t.Fatalf("protected key %s remained after cleanup", keyID)
	}
	var marker any
	if err := repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE controller_id=? AND key_id=?`, repositoryTestControllerID, keyID).Scan(&marker); err != nil || marker == nil {
		t.Fatalf("protected key %s marker=%v err=%v", keyID, marker, err)
	}
}
