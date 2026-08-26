package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestControllerIdentityWriteLeaseSerializesConcurrentCreators(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	firstWritten := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstCredentials := &blockingIdentityWriteCredentials{
		enrollmentCredentials: store,
		written:               firstWritten,
		release:               releaseFirst,
	}
	conflicted := make(chan struct{})
	secondRepository := &observingIdentityConflictRepository{enrollmentRepository: repository, conflicted: conflicted}
	first, err := NewEnrollmentService(repository, fakeSourceAccess{}, firstCredentials, &fakeEnrollmentClient{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x31}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEnrollmentService(secondRepository, fakeSourceAccess{}, store, &fakeEnrollmentClient{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x52}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	type identityResult struct {
		identity ControllerIdentity
		key      ControllerKey
		bundle   ControllerKeyBundle
		err      error
	}
	firstResult := make(chan identityResult, 1)
	secondResult := make(chan identityResult, 1)
	go func() {
		identity, key, bundle, activeErr := first.activeIdentity(context.Background())
		firstResult <- identityResult{identity: identity, key: key, bundle: bundle, err: activeErr}
	}()
	<-firstWritten
	go func() {
		identity, key, bundle, activeErr := second.activeIdentity(context.Background())
		secondResult <- identityResult{identity: identity, key: key, bundle: bundle, err: activeErr}
	}()
	<-conflicted
	close(releaseFirst)
	winner := <-firstResult
	loser := <-secondResult
	defer winner.bundle.Destroy()
	defer loser.bundle.Destroy()
	if winner.err != nil || loser.err != nil {
		t.Fatalf("concurrent identity results winner=%v loser=%v", winner.err, loser.err)
	}
	if winner.identity.ControllerID != loser.identity.ControllerID || winner.key.KeyID != loser.key.KeyID {
		t.Fatalf("creators did not converge winner=%s/%s loser=%s/%s", winner.identity.ControllerID, winner.key.KeyID, loser.identity.ControllerID, loser.key.KeyID)
	}
	var leases, controllers, keys int
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controllers`).Scan(&controllers); err != nil {
		t.Fatal(err)
	}
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if leases != 0 || controllers != 1 || keys != 1 {
		t.Fatalf("unexpected durable identity counts leases=%d controllers=%d keys=%d", leases, controllers, keys)
	}
	inventory, err := store.ControllerKeyCredentials("", 10)
	if err != nil || len(inventory.Credentials) != 1 || len(inventory.Issues) != 0 {
		t.Fatalf("winner/loser left secret artifacts inventory=%#v err=%v", inventory, err)
	}
	temporary, err := store.ControllerKeyTemporaryArtifacts("", 10)
	if err != nil || len(temporary.Artifacts) != 0 {
		t.Fatalf("winner/loser left temporary artifacts inventory=%#v err=%v", temporary, err)
	}
}

func TestControllerIdentityWriteLeaseCrashMatrix(t *testing.T) {
	tests := []struct {
		name        string
		installKey  bool
		installTemp bool
		wantCleaned int
	}{
		{name: "post key pre identity", installKey: true, wantCleaned: 1},
		{name: "temp only", installTemp: true, wantCleaned: 1},
		{name: "temp and installed", installKey: true, installTemp: true, wantCleaned: 2},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, root, now := newRepositoryHarness(t)
			store, err := NewFileCredentialStore(root)
			if err != nil {
				t.Fatal(err)
			}
			lease, bundle, _, _ := testIdentityWriteCandidate(now, byte(0x61+index), index)
			defer bundle.Destroy()
			defer clear(lease.PublicKey)
			if err = repository.BeginControllerIdentityWrite(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			if test.installKey {
				if _, err = store.WriteControllerKey(bundle); err != nil {
					t.Fatal(err)
				}
			}
			var tempPath string
			if test.installTemp {
				tempPath = createControllerKeyTempArtifact(t, store, root, lease.ControllerID)
			}
			active := newTestControlService(t, repository, store, now.Add(time.Minute))
			page, err := active.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
			if err != nil || page.Cleaned != 0 {
				t.Fatalf("active identity writer recovery page=%#v err=%v", page, err)
			}
			assertIdentityCrashArtifacts(t, store, bundle, tempPath, test.installKey, test.installTemp)

			restarted := newTestControlService(t, repository, store, lease.LeaseExpiresAt.Add(time.Minute))
			page, err = restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
			if err != nil || page.Cleaned != test.wantCleaned {
				t.Fatalf("expired identity writer recovery page=%#v err=%v want cleaned=%d", page, err, test.wantCleaned)
			}
			assertIdentityCrashArtifacts(t, store, bundle, tempPath, false, false)
			var leases int
			if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases`).Scan(&leases); err != nil || leases != 0 {
				t.Fatalf("identity recovery leases=%d err=%v", leases, err)
			}
		})
	}
}

func TestStaleControllerIdentityWriterCannotCommitAndLateArtifactConverges(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, bundle, identity, key := testIdentityWriteCandidate(now, 0x74, 7)
	defer bundle.Destroy()
	defer clear(lease.PublicKey)
	if err = repository.BeginControllerIdentityWrite(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	restarted := newTestControlService(t, repository, store, lease.LeaseExpiresAt.Add(time.Minute))
	page, err := restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 0 {
		t.Fatalf("empty expired identity recovery page=%#v err=%v", page, err)
	}
	if _, err = store.WriteControllerKey(bundle); err != nil {
		t.Fatal(err)
	}
	if err = repository.CreateIdentity(context.Background(), lease, identity, key, lease.LeaseExpiresAt.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fenced stale identity writer materialized: %v", err)
	}
	if _, _, err = repository.ActiveIdentity(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale identity became authoritative: %v", err)
	}
	page, err = restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 {
		t.Fatalf("late stale artifact did not converge page=%#v err=%v", page, err)
	}
	if _, err = store.ReadControllerKey(bundle.ControllerID, bundle.KeyID, bundle.PublicKey); err == nil {
		t.Fatal("late stale artifact remained readable")
	}
}

func TestEnrollmentIdentityMaterializationRecoversAmbiguousCommit(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := &ambiguousIdentityMaterializationRepository{enrollmentRepository: repository}
	service, err := NewEnrollmentService(ambiguous, fakeSourceAccess{}, store, &fakeEnrollmentClient{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x45}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	identity, key, bundle, err := service.activeIdentity(context.Background())
	defer bundle.Destroy()
	if err != nil || identity.ControllerID == "" || key.KeyID == "" {
		t.Fatalf("ambiguous identity materialization was not recovered identity=%#v key=%#v err=%v", identity, key, err)
	}
	var leases int
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("ambiguous commit left lease count=%d err=%v", leases, err)
	}
	stored, err := store.ReadControllerKey(identity.ControllerID, key.KeyID, key.PublicKey)
	if err != nil {
		t.Fatalf("ambiguous committed key missing: %v", err)
	}
	stored.Destroy()
}

func TestEnrollmentIdentityMaterializationFailurePreservesDurableOwnership(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failedIdentityMaterializationRepository{enrollmentRepository: repository}
	service, err := NewEnrollmentService(failing, fakeSourceAccess{}, store, &fakeEnrollmentClient{}, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x46}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	_, _, bundle, err := service.activeIdentity(context.Background())
	bundle.Destroy()
	if !IsEnrollmentCode(err, "persistence_unavailable") {
		t.Fatalf("unexpected identity materialization failure: %v", err)
	}
	var lease ControllerKeyIOLease
	lease, err = scanControllerKeyIOLease(repository.db.QueryRow(controllerKeyIOLeaseSelect+` WHERE scope_key=?`, controllerIdentityIOScope))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(lease.PublicKey)
	stored, err := store.ReadControllerKey(lease.ControllerID, lease.KeyID, lease.PublicKey)
	if err != nil {
		t.Fatalf("database failure deleted durably owned key: %v", err)
	}
	stored.Destroy()
	active := newTestControlService(t, repository, store, now.Add(time.Minute))
	page, err := active.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 0 {
		t.Fatalf("active failed materialization lease did not fail closed page=%#v err=%v", page, err)
	}
	restarted := newTestControlService(t, repository, store, lease.LeaseExpiresAt.Add(time.Minute))
	page, err = restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 {
		t.Fatalf("failed materialization did not converge page=%#v err=%v", page, err)
	}
}

type blockingIdentityWriteCredentials struct {
	enrollmentCredentials
	written chan struct{}
	release chan struct{}
	once    sync.Once
}

func (credentials *blockingIdentityWriteCredentials) WriteControllerKey(bundle ControllerKeyBundle) (string, error) {
	ref, err := credentials.enrollmentCredentials.WriteControllerKey(bundle)
	if err != nil {
		return "", err
	}
	credentials.once.Do(func() { close(credentials.written) })
	<-credentials.release
	return ref, nil
}

type observingIdentityConflictRepository struct {
	enrollmentRepository
	conflicted chan struct{}
	once       sync.Once
}

func (repository *observingIdentityConflictRepository) BeginControllerIdentityWrite(ctx context.Context, lease ControllerKeyIOLease) error {
	err := repository.enrollmentRepository.BeginControllerIdentityWrite(ctx, lease)
	if errors.Is(err, ErrConflict) {
		repository.once.Do(func() { close(repository.conflicted) })
	}
	return err
}

type ambiguousIdentityMaterializationRepository struct{ enrollmentRepository }

func (repository *ambiguousIdentityMaterializationRepository) CreateIdentity(ctx context.Context, lease ControllerKeyIOLease, identity ControllerIdentity, key ControllerKey, at time.Time) error {
	if err := repository.enrollmentRepository.CreateIdentity(ctx, lease, identity, key, at); err != nil {
		return err
	}
	return errors.New("ambiguous identity materialization response")
}

type failedIdentityMaterializationRepository struct{ enrollmentRepository }

func (*failedIdentityMaterializationRepository) CreateIdentity(context.Context, ControllerKeyIOLease, ControllerIdentity, ControllerKey, time.Time) error {
	return errors.New("identity database unavailable")
}

func testIdentityWriteCandidate(now time.Time, seed byte, suffix int) (ControllerKeyIOLease, ControllerKeyBundle, ControllerIdentity, ControllerKey) {
	controllerID := "81000000-0000-4000-8000-00000000000" + string(rune('0'+suffix))
	keyID := "82000000-0000-4000-8000-00000000000" + string(rune('0'+suffix))
	leaseID := "83000000-0000-4000-8000-00000000000" + string(rune('0'+suffix))
	privateKey := testPrivateKey(seed)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	bundle := ControllerKeyBundle{Version: credentialVersion, ControllerID: controllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: append(ed25519.PublicKey(nil), publicKey...)}
	lease := ControllerKeyIOLease{
		ScopeKey: controllerIdentityIOScope, ControllerID: controllerID, LeaseID: leaseID, Operation: ControllerKeyIOIdentityWrite,
		Phase: ControllerKeyIOActive, Fence: 1, LeaseExpiresAt: now.Add(5 * time.Minute), KeyID: keyID,
		PublicKey: append([]byte(nil), publicKey...), ProtectedKeyRef: ProtectedKeyRef(controllerID, keyID), CreatedAt: now, UpdatedAt: now,
	}
	activated := now
	identity := ControllerIdentity{ControllerID: controllerID, State: ControllerActive, CreatedAt: now, UpdatedAt: now}
	key := ControllerKey{
		KeyID: keyID, ControllerID: controllerID, PublicKey: publicKey, Algorithm: KeyAlgorithmEd25519, State: KeyActive,
		ProtectedKeyRef: lease.ProtectedKeyRef, CreatedAt: now, UpdatedAt: now, ActivatedAt: &activated, PossessionConfirmedAt: &activated,
	}
	return lease, bundle, identity, key
}

func createControllerKeyTempArtifact(t *testing.T, store *FileCredentialStore, root, controllerID string) string {
	t.Helper()
	keysPath := filepath.Join(root, "secrets", "relay", "controllers", controllerID, "keys")
	if err := store.prepareParent(keysPath); err != nil {
		t.Fatal(err)
	}
	temporary, err := os.CreateTemp(keysPath, ".hostd-secret-*")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(temporary.Name())
	if err = temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if !validControllerKeyTemporaryArtifactName(name) {
		t.Fatalf("generated unsafe temporary artifact name %q", name)
	}
	return temporary.Name()
}

func assertIdentityCrashArtifacts(t *testing.T, store *FileCredentialStore, bundle ControllerKeyBundle, tempPath string, wantKey, wantTemp bool) {
	t.Helper()
	stored, keyErr := store.ReadControllerKey(bundle.ControllerID, bundle.KeyID, bundle.PublicKey)
	if keyErr == nil {
		stored.Destroy()
	}
	if wantKey != (keyErr == nil) {
		t.Fatalf("key presence=%t want=%t err=%v", keyErr == nil, wantKey, keyErr)
	}
	if tempPath != "" {
		_, tempErr := os.Lstat(tempPath)
		if wantTemp != (tempErr == nil) {
			t.Fatalf("temporary presence=%t want=%t err=%v", tempErr == nil, wantTemp, tempErr)
		}
	}
}
