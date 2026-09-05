package sourceconnections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
	refreshStarted     chan struct{}
	refreshGate        chan struct{}
	installationPage   githubapp.InstallationPage
	installationPages  map[int]githubapp.InstallationPage
	installationErrors []error
	installationCalls  int
	installationGate   chan struct{}
	repositoryPage     githubapp.RepositoryPage
	repositoryPages    map[int64]map[int]githubapp.RepositoryPage
	repository         githubapp.Repository
	branchPage         githubapp.BranchPage
	branch             githubapp.Branch
	repositoryError    error
	repositoryErrors   []error
	branchError        error
	repositoryCalls    int
	repositoryInstalls []int64
	branchIdentities   [][2]string
	treeIdentities     [][2]string
	contentIdentities  [][2]string
	archiveIdentities  [][2]string
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
	provider.refreshCalls++
	started, gate := provider.refreshStarted, provider.refreshGate
	tokens, err := provider.refreshTokens, provider.refreshError
	provider.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	return tokens, err
}
func (provider *fakeProvider) CurrentUser(context.Context, string) (githubapp.User, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.userCalls++
	return provider.user, provider.userError
}
func (provider *fakeProvider) Installations(_ context.Context, _ string, page, _ int) (githubapp.InstallationPage, error) {
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
	if provider.installationPages != nil {
		return provider.installationPages[page], nil
	}
	return provider.installationPage, nil
}
func (provider *fakeProvider) Repositories(_ context.Context, _ string, installationID int64, page, _ int) (githubapp.RepositoryPage, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.repositoryCalls++
	if len(provider.repositoryErrors) > 0 {
		err := provider.repositoryErrors[0]
		provider.repositoryErrors = provider.repositoryErrors[1:]
		if err != nil {
			return githubapp.RepositoryPage{}, err
		}
	}
	if provider.repositoryPages != nil {
		return provider.repositoryPages[installationID][page], provider.repositoryError
	}
	return provider.repositoryPage, provider.repositoryError
}
func (provider *fakeProvider) Repository(_ context.Context, _ string, installationID, _ int64) (githubapp.Repository, error) {
	provider.repositoryCalls++
	provider.repositoryInstalls = append(provider.repositoryInstalls, installationID)
	return provider.repository, provider.repositoryError
}
func (provider *fakeProvider) Branches(_ context.Context, _, owner, repository string, _, _ int) (githubapp.BranchPage, error) {
	provider.branchIdentities = append(provider.branchIdentities, [2]string{owner, repository})
	return provider.branchPage, provider.branchError
}
func (provider *fakeProvider) Branch(_ context.Context, _, owner, repository, _ string) (githubapp.Branch, error) {
	provider.branchIdentities = append(provider.branchIdentities, [2]string{owner, repository})
	return provider.branch, provider.branchError
}
func (provider *fakeProvider) Tree(_ context.Context, _, owner, repository, _ string) (githubapp.Tree, error) {
	provider.treeIdentities = append(provider.treeIdentities, [2]string{owner, repository})
	return githubapp.Tree{}, nil
}
func (provider *fakeProvider) Content(_ context.Context, _, owner, repository, _, _ string) ([]byte, error) {
	provider.contentIdentities = append(provider.contentIdentities, [2]string{owner, repository})
	return nil, nil
}
func (provider *fakeProvider) Archive(_ context.Context, _, owner, repository, _ string) (io.ReadCloser, error) {
	provider.archiveIdentities = append(provider.archiveIdentities, [2]string{owner, repository})
	return nil, nil
}

func TestDownloadArchiveRejectsNilProviderBody(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connection := connectService(t, service, clock)
	repository := SourceRepository{ID: 7, Owner: "stale", Name: "stale"}
	provider.repository = githubapp.Repository{ID: 7, Owner: "canonical", Name: "repository", DefaultBranch: "main"}
	body, err := service.DownloadArchive(context.Background(), "owner", connection.ID, 9, repository, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if body != nil || !IsCode(err, "provider_unavailable") {
		t.Fatalf("body=%v err=%v", body, err)
	}
	if len(provider.archiveIdentities) != 1 || provider.archiveIdentities[0] != [2]string{"canonical", "repository"} {
		t.Fatalf("archive identity = %#v", provider.archiveIdentities)
	}
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

type gatedDeviceCredentialStore struct {
	CredentialStore
	entered chan string
	release chan struct{}
}

type bundleWriteSpyStore struct {
	CredentialStore
	target string
	writes int
}

func (store *bundleWriteSpyStore) WriteBundle(id string, bundle TokenBundle) error {
	if id == store.target {
		store.writes++
	}
	return store.CredentialStore.WriteBundle(id, bundle)
}

func (store *gatedDeviceCredentialStore) WriteDevice(id, value string) error {
	store.entered <- id
	<-store.release
	return store.CredentialStore.WriteDevice(id, value)
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

func TestExpiredPollIsOwnerScopedPurgesDeviceSecretAndMarksTerminal(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	started, err := service.Start(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * time.Minute)
	if _, err := service.Poll(context.Background(), "other", started.ConnectionID); !IsCode(err, "connection_not_found") {
		t.Fatalf("other owner poll = %v", err)
	}
	if _, err := store.ReadDevice(started.ConnectionID); err != nil {
		t.Fatalf("other owner removed device credential: %v", err)
	}
	if _, err := service.Poll(context.Background(), "owner", started.ConnectionID); !IsCode(err, "authorization_expired") {
		t.Fatalf("expired poll = %v", err)
	}
	if provider.pollCalls != 0 {
		t.Fatalf("expired poll made %d provider calls", provider.pollCalls)
	}
	if _, err := store.ReadDevice(started.ConnectionID); err == nil {
		t.Fatal("expired device credential remains")
	}
	connection, err := service.repository.Get(context.Background(), "owner", started.ConnectionID)
	if err != nil || connection.Status != StatusExpired || connection.LastErrorCode != "authorization_expired" || connection.PendingExpiresAt != nil || connection.NextPollAt != nil {
		t.Fatalf("expired connection = %#v, %v", connection, err)
	}
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
	staleIdentity := SourceRepository{ID: 77, Owner: "ui-supplied-owner", Name: "ui-supplied-name"}
	if _, err := service.ReadTree(context.Background(), "owner", connection.ID, 9, staleIdentity, branch.SHA); err != nil {
		t.Fatalf("read tree = %v", err)
	}
	if _, err := service.ReadContent(context.Background(), "owner", connection.ID, 9, staleIdentity, "compose.yaml", branch.SHA); err != nil {
		t.Fatalf("read content = %v", err)
	}
	if len(provider.branchIdentities) != 2 || provider.branchIdentities[0] != [2]string{"new-owner", "renamed"} || provider.branchIdentities[1] != [2]string{"new-owner", "renamed"} {
		t.Fatalf("canonical branch identities = %#v", provider.branchIdentities)
	}
	if len(provider.treeIdentities) != 1 || provider.treeIdentities[0] != [2]string{"new-owner", "renamed"} || len(provider.contentIdentities) != 1 || provider.contentIdentities[0] != [2]string{"new-owner", "renamed"} {
		t.Fatalf("canonical read identities tree=%#v content=%#v", provider.treeIdentities, provider.contentIdentities)
	}
	if len(provider.repositoryInstalls) != 4 {
		t.Fatalf("repository installation scope = %#v", provider.repositoryInstalls)
	}
	for _, installationID := range provider.repositoryInstalls {
		if installationID != 9 {
			t.Fatalf("repository escaped installation 9: %#v", provider.repositoryInstalls)
		}
	}
}

func TestRepositoryItemFailuresDoNotPurgeConnectionAndProviderFailuresAreSanitized(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	connection := connectService(t, service, clock)
	for _, code := range []string{"not_found", "forbidden"} {
		provider.repositoryError = &githubapp.Error{Code: code}
		if _, err := service.Repository(context.Background(), "owner", connection.ID, 9, 77); !IsCode(err, "invalid_source") {
			t.Fatalf("%s repository error = %v", code, err)
		}
		got, err := service.repository.Get(context.Background(), "owner", connection.ID)
		if err != nil || got.Status != StatusConnected {
			t.Fatalf("%s changed connection = %#v, %v", code, got, err)
		}
		if _, err := store.ReadBundle(connection.ID); err != nil {
			t.Fatalf("%s purged credential: %v", code, err)
		}
	}
	provider.repositoryError = errors.New("raw provider body secret")
	if _, err := service.Repositories(context.Background(), "owner", connection.ID, 9, 1, 30); !IsCode(err, "provider_unavailable") || strings.Contains(err.Error(), "raw") {
		t.Fatalf("provider error = %v", err)
	}
}

func TestRepositoryOperationsRejectCrossInstallationAndItemFailuresWithoutPurging(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	connection := connectService(t, service, clock)
	provider.repository = githubapp.Repository{ID: 77, Owner: "canonical-owner", Name: "canonical-repo", DefaultBranch: "main"}
	provider.branch = githubapp.Branch{Name: "main", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	if _, err := service.Repository(context.Background(), "owner", connection.ID, 9, 77); err != nil {
		t.Fatalf("selected installation membership = %v", err)
	}
	provider.repositoryError = &githubapp.Error{Code: "not_found"}
	if _, err := service.Branches(context.Background(), "owner", connection.ID, 10, 77, 1, 30); !IsCode(err, "invalid_source") {
		t.Fatalf("cross-installation repository = %v", err)
	}
	if len(provider.branchIdentities) != 0 || provider.repositoryInstalls[len(provider.repositoryInstalls)-1] != 10 {
		t.Fatalf("cross-installation lookup reached item route: installs=%#v branches=%#v", provider.repositoryInstalls, provider.branchIdentities)
	}

	provider.repositoryError = nil
	for _, code := range []string{"not_found", "forbidden"} {
		provider.branchError = &githubapp.Error{Code: code}
		if _, _, err := service.Resolve(context.Background(), "owner", connection.ID, 9, 77, "main"); !IsCode(err, "invalid_source") {
			t.Fatalf("%s item error = %v", code, err)
		}
	}
	got, err := service.repository.Get(context.Background(), "owner", connection.ID)
	if err != nil || got.Status != StatusConnected {
		t.Fatalf("repository failure changed connection = %#v, %v", got, err)
	}
	if _, err := store.ReadBundle(connection.ID); err != nil {
		t.Fatalf("repository failure purged bundle: %v", err)
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

func TestDefaultConnectionPersistsAndReconnectsStableIdentity(t *testing.T) {
	service, provider, clock, db, store := testService(t)
	first := connectDefaultService(t, service, clock)
	if first.CredentialGeneration != 1 {
		t.Fatalf("initial generation = %d", first.CredentialGeneration)
	}

	restarted := NewService(NewRepository(db), provider, store, "hostd-test", clock.Time)
	persisted, configured, err := restarted.Default(context.Background(), "owner")
	if err != nil || !configured || persisted.ID != first.ID || persisted.ProviderLogin != "octo" {
		t.Fatalf("persisted default = %#v configured=%v err=%v", persisted, configured, err)
	}
	started, err := restarted.StartDefault(context.Background(), "owner")
	if err != nil || started.ConnectionID != first.ID {
		t.Fatalf("reconnect start = %#v err=%v", started, err)
	}
	clock.Advance(5 * time.Second)
	reconnected, err := restarted.PollDefault(context.Background(), "owner", started.ConnectionID, started.AuthorizationID)
	if err != nil || reconnected.Connection.ID != first.ID || reconnected.Connection.CredentialGeneration != 2 {
		t.Fatalf("reconnected = %#v err=%v", reconnected, err)
	}
	var defaults int
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_default_connections WHERE owner_user_id='owner'`).Scan(&defaults); err != nil || defaults != 1 {
		t.Fatalf("default mappings = %d err=%v", defaults, err)
	}
}

func TestDefaultReconnectMismatchAndTransientFailuresPreserveCredentials(t *testing.T) {
	service, provider, clock, db, store := testService(t)
	connection := connectDefaultService(t, service, clock)
	original, err := store.ReadBundle(connection.ID)
	if err != nil {
		t.Fatal(err)
	}

	provider.pollErrors = []error{&githubapp.Error{Code: "provider_unavailable"}}
	started, err := service.StartDefault(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	if _, err := service.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID); !IsCode(err, "provider_unavailable") {
		t.Fatalf("transient poll = %v", err)
	}
	current, err := service.repository.Get(context.Background(), "owner", connection.ID)
	if err != nil || current.Status != StatusConnected || current.CredentialGeneration != original.Generation {
		t.Fatalf("connection after transient = %#v err=%v", current, err)
	}
	retained, err := store.ReadBundle(connection.ID)
	if err != nil || retained.AccessToken != original.AccessToken {
		t.Fatalf("bundle after transient = %v err=%v", retained, err)
	}

	clock.Advance(5 * time.Second)
	provider.user = githubapp.User{ID: "84", Login: "other-octo"}
	if _, err := service.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID); !IsCode(err, "authorization_identity_mismatch") {
		t.Fatalf("identity mismatch = %v", err)
	}
	current, err = service.repository.Get(context.Background(), "owner", connection.ID)
	if err != nil || current.Status != StatusConnected || current.ProviderUserID != "42" || current.CredentialGeneration != original.Generation {
		t.Fatalf("connection after mismatch = %#v err=%v", current, err)
	}
	retained, err = store.ReadBundle(connection.ID)
	if err != nil || retained.ProviderUserID != "42" || retained.AccessToken != original.AccessToken {
		t.Fatalf("bundle after mismatch = %v err=%v", retained, err)
	}
	assertSQLiteHasNoSentinels(t, db, "device-sensitive", "ghu_sensitive", "ghr_sensitive", "other-octo")
}

func TestDefaultAuthorizationSupersessionAndOwnerIsolation(t *testing.T) {
	service, _, clock, _, store := testService(t)
	first, err := service.StartDefault(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.StartDefault(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if first.ConnectionID != second.ConnectionID || first.AuthorizationID == second.AuthorizationID {
		t.Fatalf("attempt identities first=%#v second=%#v", first, second)
	}
	if _, err := store.ReadDevice(first.AuthorizationID); !credentialMissing(err) {
		t.Fatalf("superseded device credential remains: %v", err)
	}
	if _, err := service.PollDefault(context.Background(), "owner", first.ConnectionID, first.AuthorizationID); !IsCode(err, "authorization_superseded") {
		t.Fatalf("superseded poll = %v", err)
	}
	if _, err := service.PollDefault(context.Background(), "other", second.ConnectionID, second.AuthorizationID); !IsCode(err, "connection_not_found") {
		t.Fatalf("cross-owner poll = %v", err)
	}
	if _, configured, err := service.Default(context.Background(), "other"); err != nil || configured {
		t.Fatalf("cross-owner default configured=%v err=%v", configured, err)
	}
	clock.Advance(5 * time.Second)
	if result, err := service.PollDefault(context.Background(), "owner", second.ConnectionID, second.AuthorizationID); err != nil || result.Connection.Status != StatusConnected {
		t.Fatalf("current poll = %#v err=%v", result, err)
	}
}

func TestConcurrentDefaultStartsLeaveOnePollableAttemptWithoutCredentialLeaks(t *testing.T) {
	service, _, _, db, store := testService(t)
	const starts = 12
	results := make(chan ConnectionAuthorization, starts)
	errorsFound := make(chan error, starts)
	var group sync.WaitGroup
	for range starts {
		group.Add(1)
		go func() {
			defer group.Done()
			started, err := service.StartDefault(context.Background(), "owner")
			if err != nil {
				errorsFound <- err
				return
			}
			results <- started
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent start: %v", err)
	}
	var pendingID string
	if err := db.QueryRow(`SELECT id FROM github_connection_authorizations WHERE owner_user_id='owner' AND status='pending'`).Scan(&pendingID); err != nil {
		t.Fatal(err)
	}
	var pendingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_connection_authorizations WHERE owner_user_id='owner' AND status='pending'`).Scan(&pendingCount); err != nil || pendingCount != 1 {
		t.Fatalf("pending attempts=%d err=%v", pendingCount, err)
	}
	seen := 0
	for result := range results {
		seen++
		_, err := store.ReadDevice(result.AuthorizationID)
		if result.AuthorizationID == pendingID {
			if err != nil {
				t.Errorf("pending credential missing: %v", err)
			}
		} else if !credentialMissing(err) {
			t.Errorf("superseded credential %s remains: %v", result.AuthorizationID, err)
		}
	}
	if seen != starts {
		t.Fatalf("successful starts=%d", seen)
	}
}

func TestInitialDefaultStartSerializesCredentialWriteWithDisconnect(t *testing.T) {
	service, _, _, _, realStore := testService(t)
	gated := &gatedDeviceCredentialStore{CredentialStore: realStore, entered: make(chan string, 1), release: make(chan struct{})}
	service.credentials = gated
	type startResult struct {
		value ConnectionAuthorization
		err   error
	}
	started := make(chan startResult, 1)
	go func() {
		value, err := service.StartDefault(context.Background(), "owner")
		started <- startResult{value: value, err: err}
	}()
	authorizationID := <-gated.entered
	var connectionID string
	if err := service.repository.db.QueryRow(`SELECT connection_id FROM github_connection_authorizations WHERE id=?`, authorizationID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	disconnectStarted := make(chan struct{})
	disconnected := make(chan error, 1)
	go func() {
		close(disconnectStarted)
		disconnected <- service.Disconnect(context.Background(), "owner", connectionID)
	}()
	<-disconnectStarted
	var premature error
	completedEarly := false
	select {
	case premature = <-disconnected:
		completedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(gated.release)
	result := <-started
	if result.err != nil || result.value.ConnectionID != connectionID || result.value.AuthorizationID != authorizationID {
		t.Fatalf("start = %#v err=%v", result.value, result.err)
	}
	if completedEarly {
		t.Fatalf("disconnect completed during credential write: %v", premature)
	}
	if !completedEarly {
		if err := <-disconnected; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := realStore.ReadDevice(authorizationID); !credentialMissing(err) {
		t.Fatalf("device credential remains after disconnect: %v", err)
	}
	attempt, err := service.repository.Authorization(context.Background(), "owner", connectionID, result.value.AuthorizationID)
	if err != nil || attempt.Status != "failed" {
		t.Fatalf("authorization = %#v err=%v", attempt, err)
	}
}

func TestReconnectSerializesWithRefreshAndDisconnect(t *testing.T) {
	t.Run("refresh", func(t *testing.T) {
		service, provider, clock, _, store := testService(t)
		connection := connectDefaultService(t, service, clock)
		started, err := service.StartDefault(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(5 * time.Second)
		provider.refreshStarted = make(chan struct{}, 1)
		provider.refreshGate = make(chan struct{})
		results := make(chan error, 2)
		go func() {
			_, err := service.Refresh(context.Background(), "owner", connection.ID)
			results <- err
		}()
		<-provider.refreshStarted
		go func() {
			_, err := service.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID)
			results <- err
		}()
		close(provider.refreshGate)
		first, second := <-results, <-results
		if first != nil && second != nil {
			t.Fatalf("refresh/poll errors = %v, %v", first, second)
		}
		if !IsCode(first, "authorization_superseded") && !IsCode(second, "authorization_superseded") {
			t.Fatalf("poll was not superseded: %v, %v", first, second)
		}
		persisted, err := service.repository.Get(context.Background(), "owner", connection.ID)
		if err != nil || persisted.Status != StatusConnected {
			t.Fatalf("persisted = %#v err=%v", persisted, err)
		}
		bundle, err := store.ReadBundle(connection.ID)
		if err != nil || bundle.Generation != persisted.CredentialGeneration || bundle.ProviderUserID != persisted.ProviderUserID {
			t.Fatalf("bundle=%v persisted=%#v err=%v", bundle, persisted, err)
		}
	})

	t.Run("disconnect", func(t *testing.T) {
		service, _, clock, _, store := testService(t)
		connection := connectDefaultService(t, service, clock)
		started, err := service.StartDefault(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(5 * time.Second)
		begin := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-begin; results <- service.Disconnect(context.Background(), "owner", connection.ID) }()
		go func() {
			<-begin
			_, err := service.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID)
			results <- err
		}()
		close(begin)
		first, second := <-results, <-results
		for _, result := range []error{first, second} {
			if result != nil && !IsCode(result, "authorization_failed") {
				t.Fatalf("race result = %v", result)
			}
		}
		persisted, err := service.repository.Get(context.Background(), "owner", connection.ID)
		if err != nil || persisted.Status != StatusDisconnected || persisted.CredentialGeneration != 0 {
			t.Fatalf("persisted = %#v err=%v", persisted, err)
		}
		if _, err := store.ReadBundle(connection.ID); !credentialMissing(err) {
			t.Fatalf("bundle remains after disconnect: %v", err)
		}
		pending, err := service.repository.PendingAuthorizationIDs(context.Background(), "owner", connection.ID)
		if err != nil || len(pending) != 0 {
			t.Fatalf("pending attempts=%v err=%v", pending, err)
		}
	})
}

func TestPendingAuthorizationReconcilesAlreadyPromotedMatchingBundleAfterRestart(t *testing.T) {
	service, provider, clock, _, store := testService(t)
	connection := connectDefaultService(t, service, clock)
	started, err := service.StartDefault(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	promoted := TokenBundle{
		Version: tokenBundleVersion, Generation: connection.CredentialGeneration + 1,
		AccessToken: "reconnect-access", RefreshToken: "reconnect-refresh",
		AccessExpiresAt: clock.Time().Add(time.Hour), RefreshExpiresAt: clock.Time().Add(24 * time.Hour),
		ProviderUserID: connection.ProviderUserID, ProviderLogin: connection.ProviderLogin,
	}
	if err := store.WriteBundle(started.AuthorizationID, promoted); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBundle(connection.ID, promoted); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(service.repository, provider, store, "hostd-test", clock.Time)
	provider.installationPage = githubapp.InstallationPage{TotalCount: 0}
	if _, err := restarted.DefaultRepositories(context.Background(), "owner", "", 1, 30); err != nil {
		t.Fatalf("promote active bundle = %v", err)
	}
	result, err := restarted.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID)
	if err != nil || result.Authorization.Status != "connected" || result.Connection.CredentialGeneration != promoted.Generation {
		t.Fatalf("reconciled = %#v err=%v", result, err)
	}
	if _, err := store.ReadBundle(started.AuthorizationID); !credentialMissing(err) {
		t.Fatalf("staged bundle remains: %v", err)
	}
	active, err := store.ReadBundle(connection.ID)
	if err != nil || !tokenBundlesEqual(active, promoted) {
		t.Fatalf("active bundle changed: %#v err=%v", active, err)
	}
}

func TestStaleAuthorizationNeverOverwritesActiveBundleAndRemainsSuperseded(t *testing.T) {
	for _, test := range []struct {
		name             string
		activeGeneration int64
	}{
		{name: "newer generation", activeGeneration: 3},
		{name: "same promoted generation with different tokens", activeGeneration: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, clock, _, realStore := testService(t)
			connection := connectDefaultService(t, service, clock)
			started, err := service.StartDefault(context.Background(), "owner")
			if err != nil {
				t.Fatal(err)
			}
			staged := TokenBundle{
				Version: tokenBundleVersion, Generation: connection.CredentialGeneration + 1,
				AccessToken: "staged-access", RefreshToken: "staged-refresh",
				AccessExpiresAt: clock.Time().Add(time.Hour), RefreshExpiresAt: clock.Time().Add(24 * time.Hour),
				ProviderUserID: connection.ProviderUserID, ProviderLogin: connection.ProviderLogin,
			}
			active := staged
			active.Generation = test.activeGeneration
			active.AccessToken = "active-access"
			active.RefreshToken = "active-refresh"
			if err := realStore.WriteBundle(started.AuthorizationID, staged); err != nil {
				t.Fatal(err)
			}
			if err := realStore.WriteBundle(connection.ID, active); err != nil {
				t.Fatal(err)
			}
			if err := service.repository.Connect(context.Background(), "owner", connection.ID, active, clock.Time()); err != nil {
				t.Fatal(err)
			}
			spy := &bundleWriteSpyStore{CredentialStore: realStore, target: connection.ID}
			service.credentials = spy
			if _, err := service.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID); !IsCode(err, "authorization_superseded") {
				t.Fatalf("first poll = %v", err)
			}
			if _, err := service.PollDefault(context.Background(), "owner", connection.ID, started.AuthorizationID); !IsCode(err, "authorization_superseded") {
				t.Fatalf("repeated poll = %v", err)
			}
			if spy.writes != 0 {
				t.Fatalf("active credential writes = %d", spy.writes)
			}
			persisted, err := realStore.ReadBundle(connection.ID)
			if err != nil || !tokenBundlesEqual(persisted, active) {
				t.Fatalf("active bundle changed: %#v err=%v", persisted, err)
			}
			attempt, err := service.repository.Authorization(context.Background(), "owner", connection.ID, started.AuthorizationID)
			if err != nil || attempt.Status != "superseded" || attempt.LastErrorCode != "authorization_superseded" {
				t.Fatalf("attempt = %#v err=%v", attempt, err)
			}
		})
	}
}

func TestDefaultRepositorySearchAggregatesPersonalAndOrganizationPages(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connectDefaultService(t, service, clock)
	installations := make([]githubapp.Installation, 100)
	for i := range installations {
		when := clock.Time()
		installations[i] = githubapp.Installation{ID: int64(i + 1), AccountLogin: "unused", AccountType: "Organization", TargetType: "Organization", RepositorySelection: "selected", SuspendedAt: &when}
	}
	installations[0] = githubapp.Installation{ID: 1, AccountLogin: "octo", AccountType: "User", TargetType: "User", RepositorySelection: "all"}
	provider.installationPages = map[int]githubapp.InstallationPage{
		1: {TotalCount: 101, Installations: installations},
		2: {TotalCount: 101, Installations: []githubapp.Installation{{ID: 101, AccountLogin: "acme", AccountType: "Organization", TargetType: "Organization", RepositorySelection: "selected"}}},
	}
	personal := make([]githubapp.Repository, 100)
	for i := range personal {
		personal[i] = githubapp.Repository{ID: int64(i + 1), Owner: "octo", Name: fmt.Sprintf("repo-%03d", i), DefaultBranch: "main"}
	}
	provider.repositoryPages = map[int64]map[int]githubapp.RepositoryPage{
		1: {
			1: {TotalCount: 101, Repositories: personal},
			2: {TotalCount: 101, Repositories: []githubapp.Repository{{ID: 1001, Owner: "octo", Name: "needle-personal", DefaultBranch: "main"}}},
		},
		101: {1: {TotalCount: 1, Repositories: []githubapp.Repository{{ID: 2001, Owner: "acme", Name: "needle-org", DefaultBranch: "trunk", Private: true}}}},
	}
	result, err := service.DefaultRepositories(context.Background(), "owner", "needle", 1, 30)
	if err != nil || result.TotalCount != 2 || len(result.Repositories) != 2 {
		t.Fatalf("aggregate = %#v err=%v", result, err)
	}
	if result.Repositories[0].InstallationID != 101 || result.Repositories[1].InstallationID != 1 {
		t.Fatalf("aggregate ordering/identity = %#v", result.Repositories)
	}
	provider.repositoryError = &githubapp.Error{Code: "provider_unavailable"}
	if _, err := service.DefaultRepositories(context.Background(), "owner", "", 1, 30); !IsCode(err, "provider_unavailable") {
		t.Fatalf("aggregate transient error = %v", err)
	}
	provider.repositoryError = nil
	if recovered, err := service.DefaultRepositories(context.Background(), "owner", "needle-org", 1, 30); err != nil || recovered.TotalCount != 1 {
		t.Fatalf("aggregate recovery = %#v err=%v", recovered, err)
	}
}

func TestDefaultRepositoryAggregationRestartsCleanlyAfterUnauthorizedRefresh(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connectDefaultService(t, service, clock)
	provider.installationPage = githubapp.InstallationPage{TotalCount: 2, Installations: []githubapp.Installation{
		{ID: 1, AccountLogin: "personal"}, {ID: 2, AccountLogin: "organization"},
	}}
	provider.repositoryPages = map[int64]map[int]githubapp.RepositoryPage{
		1: {1: {TotalCount: 1, Repositories: []githubapp.Repository{{ID: 10, Owner: "octo", Name: "one", DefaultBranch: "main"}}}},
		2: {1: {TotalCount: 1, Repositories: []githubapp.Repository{{ID: 20, Owner: "acme", Name: "two", DefaultBranch: "main"}}}},
	}
	provider.repositoryErrors = []error{nil, &githubapp.Error{Code: "unauthorized"}, nil, nil}
	result, err := service.DefaultRepositories(context.Background(), "owner", "", 1, 30)
	if err != nil || result.TotalCount != 2 || len(result.Repositories) != 2 {
		t.Fatalf("aggregate after refresh = %#v err=%v", result, err)
	}
	if result.Repositories[0].ID != 20 || result.Repositories[1].ID != 10 {
		t.Fatalf("aggregate contains stale or duplicate traversal results: %#v", result.Repositories)
	}
}

func TestDefaultRepositoryAggregationRejectsCumulativeTotalOverflow(t *testing.T) {
	t.Run("installations", func(t *testing.T) {
		service, provider, clock, _, _ := testService(t)
		connectDefaultService(t, service, clock)
		first := make([]githubapp.Installation, 100)
		for index := range first {
			first[index] = githubapp.Installation{ID: int64(index + 1), AccountLogin: fmt.Sprintf("owner-%d", index+1)}
		}
		provider.installationPages = map[int]githubapp.InstallationPage{
			1: {TotalCount: 101, Installations: first},
			2: {TotalCount: 101, Installations: []githubapp.Installation{{ID: 101, AccountLogin: "owner-101"}, {ID: 102, AccountLogin: "owner-102"}}},
		}
		if _, err := service.DefaultRepositories(context.Background(), "owner", "", 1, 30); !IsCode(err, "invalid_source") {
			t.Fatalf("cumulative installation overflow = %v", err)
		}
	})

	t.Run("repositories", func(t *testing.T) {
		service, provider, clock, _, _ := testService(t)
		connectDefaultService(t, service, clock)
		provider.installationPage = githubapp.InstallationPage{TotalCount: 1, Installations: []githubapp.Installation{{ID: 1, AccountLogin: "owner"}}}
		first := make([]githubapp.Repository, 100)
		for index := range first {
			first[index] = githubapp.Repository{ID: int64(index + 1), Owner: "owner", Name: fmt.Sprintf("repo-%d", index+1), DefaultBranch: "main"}
		}
		provider.repositoryPages = map[int64]map[int]githubapp.RepositoryPage{1: {
			1: {TotalCount: 101, Repositories: first},
			2: {TotalCount: 101, Repositories: []githubapp.Repository{{ID: 101, Owner: "owner", Name: "repo-101"}, {ID: 102, Owner: "owner", Name: "repo-102"}}},
		}}
		if _, err := service.DefaultRepositories(context.Background(), "owner", "", 1, 30); !IsCode(err, "invalid_source") {
			t.Fatalf("cumulative repository overflow = %v", err)
		}
	})
}

func TestDefaultRepositoryAggregationPaginatesOnlySelectableRepositories(t *testing.T) {
	service, provider, clock, _, _ := testService(t)
	connectDefaultService(t, service, clock)
	provider.installationPage = githubapp.InstallationPage{TotalCount: 1, Installations: []githubapp.Installation{{ID: 1, AccountLogin: "owner"}}}
	provider.repositoryPage = githubapp.RepositoryPage{TotalCount: 3, Repositories: []githubapp.Repository{
		{ID: 1, Owner: "owner", Name: "archived", DefaultBranch: "main", Archived: true},
		{ID: 2, Owner: "owner", Name: "disabled", DefaultBranch: "main", Disabled: true},
		{ID: 3, Owner: "owner", Name: "selectable", DefaultBranch: "main"},
	}}
	result, err := service.DefaultRepositories(context.Background(), "owner", "", 1, 1)
	if err != nil || result.TotalCount != 1 || len(result.Repositories) != 1 || result.Repositories[0].ID != 3 {
		t.Fatalf("selectable pagination = %#v err=%v", result, err)
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

func connectDefaultService(t *testing.T, service *Service, clock *testClock) Connection {
	t.Helper()
	started, err := service.StartDefault(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	result, err := service.PollDefault(context.Background(), "owner", started.ConnectionID, started.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	return result.Connection
}

func assertSQLiteHasNoSentinels(t *testing.T, db *sql.DB, sentinels ...string) {
	t.Helper()
	var values []byte
	for _, table := range []string{"source_connections", "github_installations", "github_default_connections", "github_connection_authorizations", "jobs", "job_events", "audit_events"} {
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
