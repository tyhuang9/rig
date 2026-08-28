package sourceconnections

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/githubapp"
)

type fakeProvider struct {
	mu                 sync.Mutex
	device             githubapp.DeviceAuthorization
	pollTokens         githubapp.TokenBundle
	pollErrors         []error
	pollCalls          int
	user               githubapp.User
	userError          error
	userCalls          int
	refreshTokens      githubapp.TokenBundle
	refreshError       error
	refreshCalls       int
	installationPage   githubapp.InstallationPage
	installationErrors []error
	installationCalls  int
	installationGate   chan struct{}
	repositoryPage     githubapp.RepositoryPage
	repository         githubapp.Repository
	branchPage         githubapp.BranchPage
	branch             githubapp.Branch
	repositoryError    error
	branchError        error
	repositoryCalls    int
}

func (provider *fakeProvider) StartDevice(context.Context) (githubapp.DeviceAuthorization, error) {
	return provider.device, nil
}
func (provider *fakeProvider) PollDevice(context.Context, string) (githubapp.TokenBundle, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.pollCalls++
	if len(provider.pollErrors) > 0 {
		err := provider.pollErrors[0]
		provider.pollErrors = provider.pollErrors[1:]
		return githubapp.TokenBundle{}, err
	}
	return provider.pollTokens, nil
}
func (provider *fakeProvider) Refresh(context.Context, string) (githubapp.TokenBundle, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.refreshCalls++
	return provider.refreshTokens, provider.refreshError
}
func (provider *fakeProvider) CurrentUser(context.Context, string) (githubapp.User, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.userCalls++
	return provider.user, provider.userError
}
func (provider *fakeProvider) Installations(context.Context, string, int, int) (githubapp.InstallationPage, error) {
	if provider.installationGate != nil {
		<-provider.installationGate
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.installationCalls++
	if len(provider.installationErrors) > 0 {
		err := provider.installationErrors[0]
		provider.installationErrors = provider.installationErrors[1:]
		if err != nil {
			return githubapp.InstallationPage{}, err
		}
	}
	return provider.installationPage, nil
}
func (provider *fakeProvider) Repositories(context.Context, string, int64, int, int) (githubapp.RepositoryPage, error) {
	provider.repositoryCalls++
	return provider.repositoryPage, provider.repositoryError
}
func (provider *fakeProvider) Repository(context.Context, string, int64, int64) (githubapp.Repository, error) {
	provider.repositoryCalls++
	return provider.repository, provider.repositoryError
}
func (provider *fakeProvider) Branches(context.Context, string, int64, int, int) (githubapp.BranchPage, error) {
	return provider.branchPage, provider.branchError
}
func (provider *fakeProvider) Branch(context.Context, string, int64, string) (githubapp.Branch, error) {
	return provider.branch, provider.branchError
}
func (provider *fakeProvider) Tree(context.Context, string, int64, string) (githubapp.Tree, error) {
	return githubapp.Tree{}, nil
}
func (provider *fakeProvider) Content(context.Context, string, int64, string, string) ([]byte, error) {
	return nil, nil
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

type faultCredentialStore struct {
	CredentialStore
	failBundleWrite    bool
	failDeviceRemove   int
	failExchangeRemove int
}

func (store *faultCredentialStore) WriteBundle(id string, bundle TokenBundle) error {
	if store.failBundleWrite {
		return errors.New("injected bundle write failure")
	}
	return store.CredentialStore.WriteBundle(id, bundle)
}

func (store *faultCredentialStore) RemoveDevice(id string) error {
	if store.failDeviceRemove > 0 {
		store.failDeviceRemove--
		return errors.New("injected device remove failure")
	}
	return store.CredentialStore.RemoveDevice(id)
}

func (store *faultCredentialStore) RemoveExchange(id string) error {
	if store.failExchangeRemove > 0 {
		store.failExchangeRemove--
		return errors.New("injected exchange remove failure")
	}
	return store.CredentialStore.RemoveExchange(id)
}

func (clock *testClock) Time() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}
func (clock *testClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func TestPollEnforcesTimingSlowDownAndFinalizesWithoutPersistingDeviceCode(t *testing.T) {
	service, provider, clock, db, store := testService(t)
	provider.pollErrors = []error{&githubapp.Error{Code: "authorization_pending"}, &githubapp.Error{Code: "slow_down"}}
	started, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "poll_too_soon") {
		t.Fatalf("early poll error = %v", err)
	}
	if provider.pollCalls != 0 {
		t.Fatalf("early poll made %d provider calls", provider.pollCalls)
	}
	clock.Advance(5 * time.Second)
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "authorization_pending") {
		t.Fatalf("pending poll error = %v", err)
	}
	clock.Advance(5 * time.Second)
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "authorization_pending") {
		t.Fatalf("slow poll error = %v", err)
	}
	clock.Advance(10 * time.Second)
	connection, err := service.Poll(context.Background(), "owner", started.ConnectionID)
	if err != nil || connection.Status != StatusConnected || connection.CredentialGeneration != 1 {
		t.Fatalf("connected = %#v, %v", connection, err)
	}
	if _, err := store.ReadDevice(started.ConnectionID); err == nil {
		t.Fatal("device credential remains after finalization")
	}
	assertSQLiteHasNoSentinels(t, db, "device-sensitive", "ghu_sensitive", "ghr_sensitive", "raw provider description")
}

func TestInstallationsRefreshesOnceOnUnauthorizedAndCachesOnlyReturnedPage(t *testing.T) {
	service, provider, clock, db, _ := testService(t)
	connection := connectService(t, service, clock)
	provider.installationErrors = []error{&githubapp.Error{Code: "unauthorized"}, nil}
	provider.installationPage = githubapp.InstallationPage{TotalCount: 1, Installations: []githubapp.Installation{{ID: 7, AccountLogin: "acme", AccountType: "Organization", TargetType: "Organization", RepositorySelection: "selected"}}}
	page, err := service.Installations(context.Background(), "owner", connection.ID, 2, 100)
	if err != nil || len(page.Installations) != 1 {
		t.Fatalf("Installations = %#v, %v", page, err)
	}
	if provider.refreshCalls != 1 || provider.installationCalls != 2 {
		t.Fatalf("refresh calls = %d, installation calls = %d", provider.refreshCalls, provider.installationCalls)
	}
	var cached int
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_installations WHERE connection_id = ? AND installation_id = 7`, connection.ID).Scan(&cached); err != nil || cached != 1 {
		t.Fatalf("cached rows = %d, %v", cached, err)
	}
}

func TestMissingCredentialsFailClosedAndDisconnectIsOwnerScopedIdempotent(t *testing.T) {
	service, _, clock, _, store := testService(t)
	connection := connectService(t, service, clock)
	if err := store.RemoveBundle(connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), "other", connection.ID); !IsCode(err, "connection_not_found") {
		t.Fatalf("other-owner refresh error = %v", err)
	}
	if _, err := service.Refresh(context.Background(), "owner", connection.ID); !IsCode(err, "source_access_lost") {
		t.Fatalf("missing credential error = %v", err)
	}
	got, err := service.repository.Get(context.Background(), "owner", connection.ID)
	if err != nil || got.Status != StatusAccessLost {
		t.Fatalf("access-lost row = %#v, %v", got, err)
	}
	if err := service.Disconnect(context.Background(), "owner", connection.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Disconnect(context.Background(), "owner", connection.ID); err != nil {
		t.Fatalf("idempotent disconnect: %v", err)
	}
}

func TestRepositoryBrowsingIsOwnerScopedAndResolvesRenamedRepositoryBranch(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connection := connectService(t, service, clock)
	provider.repositoryPage = githubapp.RepositoryPage{TotalCount: 1, Repositories: []githubapp.Repository{{ID: 77, Owner: "new-owner", Name: "renamed", DefaultBranch: "main"}}}
	provider.repository = provider.repositoryPage.Repositories[0]
	provider.branchPage = githubapp.BranchPage{Branches: []githubapp.Branch{{Name: "feature/slash", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	provider.branch = provider.branchPage.Branches[0]
	if _, err := service.Repositories(context.Background(), "other", connection.ID, 9, 1, 30); !IsCode(err, "connection_not_found") {
		t.Fatalf("other owner error = %v", err)
	}
	page, err := service.Repositories(context.Background(), "owner", connection.ID, 9, 1, 30)
	if err != nil || page.Repositories[0].Owner != "new-owner" {
		t.Fatalf("repositories = %#v err=%v", page, err)
	}
	branches, err := service.Branches(context.Background(), "owner", connection.ID, 9, 77, 1, 30)
	if err != nil || branches.Branches[0].Name != "feature/slash" {
		t.Fatalf("branches = %#v err=%v", branches, err)
	}
	repository, branch, err := service.Resolve(context.Background(), "owner", connection.ID, 9, 77, "feature/slash")
	if err != nil || repository.Name != "renamed" || branch.SHA == "" {
		t.Fatalf("resolve = %#v %#v err=%v", repository, branch, err)
	}
}

func TestRepositoryAccessLossAndProviderFailuresAreSanitized(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connection := connectService(t, service, clock)
	provider.repositoryError = &githubapp.Error{Code: "not_found"}
	if _, err := service.Repositories(context.Background(), "owner", connection.ID, 9, 1, 30); !IsCode(err, "source_access_lost") {
		t.Fatalf("access loss = %v", err)
	}
	provider.repositoryError = errors.New("raw provider body secret")
	if _, err := service.Repositories(context.Background(), "owner", connection.ID, 9, 1, 30); !IsCode(err, "provider_unavailable") || strings.Contains(err.Error(), "raw") {
		t.Fatalf("provider error = %v", err)
	}
}

func TestNewerBundleGenerationReconcilesBeforeProviderUse(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	connection := connectService(t, service, clock)
	bundle, err := store.ReadBundle(connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Generation = 2
	bundle.AccessToken = "access-new"
	bundle.RefreshToken = "refresh-new"
	if err := store.WriteBundle(connection.ID, bundle); err != nil {
		t.Fatal(err)
	}
	provider.installationPage = githubapp.InstallationPage{}
	if _, err := service.Installations(context.Background(), "owner", connection.ID, 1, 10); err != nil {
		t.Fatal(err)
	}
	got, err := service.repository.Get(context.Background(), "owner", connection.ID)
	if err != nil || got.CredentialGeneration != 2 {
		t.Fatalf("reconciled connection = %#v, %v", got, err)
	}
}

func TestConnectionLockSerializesInstallationAndDisconnect(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connection := connectService(t, service, clock)
	provider.installationGate = make(chan struct{})
	provider.installationPage = githubapp.InstallationPage{}
	installDone := make(chan error, 1)
	go func() {
		_, err := service.Installations(context.Background(), "owner", connection.ID, 1, 10)
		installDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	disconnectDone := make(chan error, 1)
	go func() { disconnectDone <- service.Disconnect(context.Background(), "owner", connection.ID) }()
	select {
	case err := <-disconnectDone:
		t.Fatalf("disconnect bypassed connection lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(provider.installationGate)
	if err := <-installDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disconnectDone; err != nil {
		t.Fatal(err)
	}
}

func TestConnectedPollRetriesStaleDeviceCleanup(t *testing.T) {
	service, _, clock, _, realStore := testService(t)
	connection := connectService(t, service, clock)
	if err := realStore.WriteDevice(connection.ID, "stale-device"); err != nil {
		t.Fatal(err)
	}
	faults := &faultCredentialStore{CredentialStore: realStore, failDeviceRemove: 1}
	service.credentials = faults
	if _, err := service.Poll(context.Background(), "owner", connection.ID); !IsCode(err, "internal_error") {
		t.Fatalf("cleanup failure = %v", err)
	}
	if _, err := service.Poll(context.Background(), "owner", connection.ID); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if _, err := realStore.ReadDevice(connection.ID); err == nil {
		t.Fatal("stale device file remains")
	}
}

func TestRefreshWriteFailurePurgesOldBundleAndMarksAccessLost(t *testing.T) {
	service, _, clock, _, realStore := testService(t)
	connection := connectService(t, service, clock)
	service.credentials = &faultCredentialStore{CredentialStore: realStore, failBundleWrite: true}
	if _, err := service.Refresh(context.Background(), "owner", connection.ID); !IsCode(err, "source_access_lost") {
		t.Fatalf("refresh write failure = %v", err)
	}
	if _, err := realStore.ReadBundle(connection.ID); err == nil {
		t.Fatal("old bundle remains after failed rotation")
	}
	got, err := service.repository.Get(context.Background(), "owner", connection.ID)
	if err != nil || got.Status != StatusAccessLost {
		t.Fatalf("access-lost row = %#v, %v", got, err)
	}
}

func TestAccessLostIdentityRequiresDisconnectBeforeReauthorization(t *testing.T) {
	service, _, clock, _, store := testService(t)
	first := connectService(t, service, clock)
	if err := store.RemoveBundle(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), "owner", first.ID); !IsCode(err, "source_access_lost") {
		t.Fatalf("lose access = %v", err)
	}
	second, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if _, err := service.Poll(context.Background(), "owner", second.ConnectionID); !IsCode(err, "identity_already_connected") {
		t.Fatalf("duplicate identity = %v", err)
	}
	if err := service.Disconnect(context.Background(), "owner", first.ID); err != nil {
		t.Fatal(err)
	}
	third, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if got, err := service.Poll(context.Background(), "owner", third.ConnectionID); err != nil || got.Status != StatusConnected {
		t.Fatalf("reauthorized = %#v, %v", got, err)
	}
}

func TestTransientPollFailureAdvancesTimingAndLockEntriesAreReleased(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	provider.pollErrors = []error{&githubapp.Error{Code: "provider_unavailable"}}
	started, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "provider_unavailable") {
		t.Fatalf("transient failure = %v", err)
	}
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "poll_too_soon") {
		t.Fatalf("immediate retry = %v", err)
	}
	service.locks.mutex.Lock()
	remaining := len(service.locks.values)
	service.locks.mutex.Unlock()
	if remaining != 0 {
		t.Fatalf("keyed lock entries retained = %d", remaining)
	}
}

func TestRepositoryRejectsCredentialGenerationRollback(t *testing.T) {
	service, _, clock, _, store := testService(t)
	connection := connectService(t, service, clock)
	bundle, err := store.ReadBundle(connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.repository.Connect(context.Background(), "owner", connection.ID, bundle, clock.Time()); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("same generation update = %v", err)
	}
	bundle.Generation = 0
	if err := service.repository.Connect(context.Background(), "owner", connection.ID, bundle, clock.Time()); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("generation rollback = %v", err)
	}
}

func TestInvalidRefreshPurgesCredentialsAndSlowDownStaysWithinPersistenceBound(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	connection := connectService(t, service, clock)
	provider.refreshError = &githubapp.Error{Code: "expired_token"}
	if _, err := service.Refresh(context.Background(), "owner", connection.ID); !IsCode(err, "source_access_lost") {
		t.Fatalf("invalid refresh error = %v", err)
	}
	if _, err := store.ReadBundle(connection.ID); err == nil {
		t.Fatal("bundle remains after invalid refresh")
	}

	provider.refreshError = nil
	provider.device.Interval = 300 * time.Second
	provider.pollErrors = []error{&githubapp.Error{Code: "slow_down"}}
	started, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(300 * time.Second)
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "authorization_pending") {
		t.Fatalf("slow-down error = %v", err)
	}
	pending, err := service.repository.Get(context.Background(), "owner", started.ConnectionID)
	if err != nil || pending.PollInterval != 300*time.Second {
		t.Fatalf("clamped interval = %s, %v", pending.PollInterval, err)
	}
}

func TestFinalizingExchangeRecoversUserPromotionDatabaseAndCleanupFailures(t *testing.T) {
	t.Run("current user transient", func(t *testing.T) {
		service, provider, clock, _, store := testService(t)
		provider.userError = &githubapp.Error{Code: "provider_unavailable"}
		started, err := service.Start(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(5 * time.Second)
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "provider_unavailable") {
			t.Fatalf("first poll = %v", err)
		}
		if _, err := store.ReadExchange(started.ConnectionID); err != nil {
			t.Fatalf("durable exchange missing: %v", err)
		}
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "poll_too_soon") {
			t.Fatalf("unthrottled finalize retry = %v", err)
		}
		provider.userError = nil
		clock.Advance(5 * time.Second)
		if got, err := service.Poll(context.Background(), "owner", started.ConnectionID); err != nil || got.Status != StatusConnected {
			t.Fatalf("recovered = %#v, %v", got, err)
		}
		if provider.pollCalls != 1 {
			t.Fatalf("device exchange repeated %d times", provider.pollCalls)
		}
	})

	t.Run("bundle promotion", func(t *testing.T) {
		service, provider, clock, _, realStore := testService(t)
		started, err := service.Start(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		faults := &faultCredentialStore{CredentialStore: realStore, failBundleWrite: true}
		service.credentials = faults
		clock.Advance(5 * time.Second)
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "internal_error") {
			t.Fatalf("promotion failure = %v", err)
		}
		faults.failBundleWrite = false
		clock.Advance(5 * time.Second)
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); err != nil {
			t.Fatalf("promotion recovery = %v", err)
		}
		if provider.pollCalls != 1 {
			t.Fatalf("device exchange repeated %d times", provider.pollCalls)
		}
	})

	t.Run("database connect", func(t *testing.T) {
		service, provider, clock, db, _ := testService(t)
		started, err := service.Start(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TRIGGER fail_connection_finalize BEFORE UPDATE ON source_connections WHEN NEW.status = 'connected' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
			t.Fatal(err)
		}
		clock.Advance(5 * time.Second)
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "internal_error") {
			t.Fatalf("database failure = %v", err)
		}
		if _, err := db.Exec(`DROP TRIGGER fail_connection_finalize`); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); err != nil {
			t.Fatalf("database recovery = %v", err)
		}
		if provider.pollCalls != 1 {
			t.Fatalf("device exchange repeated %d times", provider.pollCalls)
		}
	})

	t.Run("exchange cleanup", func(t *testing.T) {
		service, provider, clock, _, realStore := testService(t)
		started, err := service.Start(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		faults := &faultCredentialStore{CredentialStore: realStore, failExchangeRemove: 1}
		service.credentials = faults
		clock.Advance(5 * time.Second)
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "internal_error") {
			t.Fatalf("cleanup failure = %v", err)
		}
		if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); err != nil {
			t.Fatalf("cleanup recovery = %v", err)
		}
		if _, err := realStore.ReadExchange(started.ConnectionID); err == nil {
			t.Fatal("exchange remains after cleanup retry")
		}
		if provider.pollCalls != 1 {
			t.Fatalf("device exchange repeated %d times", provider.pollCalls)
		}
	})
}

func TestExpiredExchangeAccessRefreshesWithoutUsingDeviceGrantExpiry(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	started, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	now := clock.Time()
	exchange := TokenExchange{Version: 1, AccessToken: "expired-access", RefreshToken: "valid-refresh", AccessExpiresAt: now.Add(-time.Minute), RefreshExpiresAt: now.Add(time.Hour)}
	if err := store.WriteExchange(started.ConnectionID, exchange); err != nil {
		t.Fatal(err)
	}
	clock.Advance(20 * time.Minute)
	if got, err := service.Poll(context.Background(), "owner", started.ConnectionID); err != nil || got.Status != StatusConnected {
		t.Fatalf("exchange refresh = %#v, %v", got, err)
	}
	if provider.refreshCalls != 1 || provider.pollCalls != 0 {
		t.Fatalf("refresh calls = %d, device poll calls = %d", provider.refreshCalls, provider.pollCalls)
	}
}

func testService(t *testing.T) (*Service, *fakeProvider, *testClock, *sql.DB, *FileCredentialStore) {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO users(id, username, passphrase_hash, created_at, updated_at) VALUES ('owner', 'owner', 'hash', datetime('now'), datetime('now')), ('other', 'other', 'hash', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	provider := &fakeProvider{
		device:        githubapp.DeviceAuthorization{DeviceCode: "device-sensitive", UserCode: "ABCD-1234", VerificationURI: githubapp.VerificationURI, ExpiresIn: 15 * time.Minute, Interval: 5 * time.Second},
		pollTokens:    githubapp.TokenBundle{AccessToken: "ghu_sensitive", RefreshToken: "ghr_sensitive", AccessExpiresIn: time.Hour, RefreshExpiresIn: 24 * time.Hour},
		refreshTokens: githubapp.TokenBundle{AccessToken: "access-new", RefreshToken: "refresh-new", AccessExpiresIn: time.Hour, RefreshExpiresIn: 24 * time.Hour},
		user:          githubapp.User{ID: "42", Login: "octo"},
	}
	store := NewFileCredentialStore(root)
	service := NewService(NewRepository(db), provider, store, "hostd-test", clock.Time)
	return service, provider, clock, db, store
}

func connectService(t *testing.T, service *Service, clock *testClock) Connection {
	t.Helper()
	started, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	connection, err := service.Poll(context.Background(), "owner", started.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func assertSQLiteHasNoSentinels(t *testing.T, db *sql.DB, sentinels ...string) {
	t.Helper()
	var values []byte
	for _, table := range []string{"source_connections", "github_installations", "jobs", "job_events", "audit_events"} {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			targets := make([]any, len(columns))
			valuesRow := make([]sql.RawBytes, len(columns))
			for i := range targets {
				targets[i] = &valuesRow[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			for _, value := range valuesRow {
				values = append(values, value...)
			}
		}
		rows.Close()
	}
	for _, sentinel := range sentinels {
		if containsBytes(values, []byte(sentinel)) {
			t.Errorf("SQLite query results contain sentinel %q", sentinel)
		}
	}
	var sequence int
	var name, databasePath string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal"} {
		contents, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range sentinels {
			if containsBytes(contents, []byte(sentinel)) {
				t.Errorf("SQLite file %s contains sentinel %q", path, sentinel)
			}
		}
	}
}

func containsBytes(value, substring []byte) bool {
	if len(substring) == 0 {
		return true
	}
	for index := 0; index+len(substring) <= len(value); index++ {
		if string(value[index:index+len(substring)]) == string(substring) {
			return true
		}
	}
	return false
}
