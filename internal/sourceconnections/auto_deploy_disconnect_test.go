package sourceconnections

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	autoDeployTestApp          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	autoDeploySecondApp        = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	autoDeployTestController   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	autoDeployTestBinding      = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	autoDeployTestInstallation = int64(101)
	autoDeployTestRepository   = int64(202)
	autoDeployTestRef          = "refs/heads/main"
	autoDeployTestSHA          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	autoDeployResolvedSHA      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type autoDeployDisconnectFixture struct {
	db         *sql.DB
	connection Connection
	clock      *testClock
	sources    *Repository
	autoDeploy *autodeploy.Repository
	now        time.Time
}

func newAutoDeployDisconnectFixture(t *testing.T) *autoDeployDisconnectFixture {
	t.Helper()
	service, _, clock, db, _ := testService(t)
	connection := connectService(t, service, clock)
	now := clock.Time().UTC()
	if err := service.repository.UpsertInstallationPage(context.Background(), "owner", connection.ID, []Installation{{
		ID: autoDeployTestInstallation, AccountLogin: "octo", AccountType: "Organization", TargetType: "Organization", RepositorySelection: "selected", CachedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	stamp := timestamp(now)
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, autoDeployTestController, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, autoDeployTestBinding, "owner", connection.ID, autoDeployTestController, autoDeployTestInstallation, autoDeployTestRepository, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return &autoDeployDisconnectFixture{db: db, connection: connection, clock: clock, sources: service.repository, autoDeploy: autodeploy.NewRepository(db), now: now}
}

func (fixture *autoDeployDisconnectFixture) addApplication(t *testing.T, applicationID string) autodeploy.Status {
	t.Helper()
	stamp := timestamp(fixture.now)
	if _, err := fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,'Application','draft',?,?)`, applicationID, applicationID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?,'github',?,?,?,'octo','app','main',?,'compose.yaml',?,?,?)`, applicationID, fixture.connection.ID, autoDeployTestInstallation, autoDeployTestRepository, autoDeployTestRef, autoDeployTestSHA, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.autoDeploy.Configure(context.Background(), autodeploy.ConfigureRequest{ApplicationID: applicationID, ActorUserID: "owner", Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (fixture *autoDeployDisconnectFixture) reserveAndFinalize(t *testing.T, applicationID string, at time.Time) (autodeploy.Status, autodeploy.WorkLease, autodeploy.PreparedDispatch) {
	t.Helper()
	status, err := fixture.autoDeploy.Get(context.Background(), applicationID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, lease, err := fixture.autoDeploy.ClaimDue(context.Background(), uuid.NewString(), at, time.Minute)
	if err != nil || claimed.ApplicationID != applicationID {
		t.Fatalf("claim application=%q want=%q err=%v", claimed.ApplicationID, applicationID, err)
	}
	if err = fixture.autoDeploy.ReserveResolve(context.Background(), lease, 0, at); err != nil {
		t.Fatal(err)
	}
	if err = fixture.autoDeploy.FinalizeResolvedHead(context.Background(), lease, 0, autoDeployTestSHA, at.Add(6*time.Hour), at); err != nil {
		t.Fatal(err)
	}
	dispatch, err := fixture.autoDeploy.PrepareDispatch(context.Background(), lease, at)
	if err != nil {
		t.Fatal(err)
	}
	return status, lease, dispatch
}

func (fixture *autoDeployDisconnectFixture) createAndLink(t *testing.T, status autodeploy.Status, lease autodeploy.WorkLease, dispatch autodeploy.PreparedDispatch, at time.Time, barrier func()) (jobs.Job, error) {
	t.Helper()
	job, _, err := jobs.New(fixture.db).CreateWithInputFinalized(jobs.CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: status.ApplicationID,
		IdempotencyKey: autodeploy.DispatchIdempotencyKey(status.Revision, dispatch.Sequence), RequestedBy: status.SourceOwnerUserID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, func(tx *sql.Tx, job jobs.Job) error {
		if err := fixture.autoDeploy.LinkDispatchJobTx(context.Background(), tx, lease, dispatch.Sequence, dispatch.Generation, job.ID, at); err != nil {
			return err
		}
		if barrier != nil {
			barrier()
		}
		return nil
	})
	return job, err
}

func TestDisconnectAfterFinalizeRollsBackSubsequentJobLink(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	status := fixture.addApplication(t, autoDeployTestApp)
	status, lease, dispatch := fixture.reserveAndFinalize(t, status.ApplicationID, fixture.now)
	if err := fixture.sources.Disconnect(context.Background(), "owner", fixture.connection.ID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.createAndLink(t, status, lease, dispatch, fixture.now.Add(2*time.Second), nil); err == nil {
		t.Fatal("disconnect-fenced job link unexpectedly committed")
	}
	assertAutoDeployDisconnectState(t, fixture, status.ApplicationID, "", 0)
}

func TestJobLinkCommitBeforeDisconnectRetainsActiveOverlay(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	status := fixture.addApplication(t, autoDeployTestApp)
	status, lease, dispatch := fixture.reserveAndFinalize(t, status.ApplicationID, fixture.now)
	linked := make(chan struct{})
	release := make(chan struct{})
	created := make(chan struct {
		job jobs.Job
		err error
	}, 1)
	go func() {
		job, err := fixture.createAndLink(t, status, lease, dispatch, fixture.now.Add(time.Second), func() {
			close(linked)
			<-release
		})
		created <- struct {
			job jobs.Job
			err error
		}{job: job, err: err}
	}()
	select {
	case <-linked:
	case <-time.After(time.Second):
		t.Fatal("job link transaction did not reach barrier")
	}
	disconnected := make(chan error, 1)
	go func() {
		disconnected <- fixture.sources.Disconnect(context.Background(), "owner", fixture.connection.ID, fixture.now.Add(2*time.Second))
	}()
	close(release)
	result := <-created
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
	assertAutoDeployDisconnectState(t, fixture, status.ApplicationID, result.job.ID, 1)
}

func TestDisconnectBeforeFinalizeAndLinkFencesBothApplications(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	first := fixture.addApplication(t, autoDeployTestApp)
	_, firstLease, err := fixture.autoDeploy.ClaimDue(context.Background(), uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.autoDeploy.ReserveResolve(context.Background(), firstLease, 0, fixture.now); err != nil {
		t.Fatal(err)
	}
	second := fixture.addApplication(t, autoDeploySecondApp)
	second, secondLease, secondDispatch := fixture.reserveAndFinalize(t, second.ApplicationID, fixture.now.Add(time.Second))
	if err = fixture.sources.Disconnect(context.Background(), "owner", fixture.connection.ID, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.autoDeploy.FinalizeResolvedHead(context.Background(), firstLease, 0, autoDeployResolvedSHA, fixture.now.Add(6*time.Hour), fixture.now.Add(3*time.Second)); err == nil {
		t.Fatal("disconnect-fenced finalization unexpectedly committed")
	}
	if _, err = fixture.createAndLink(t, second, secondLease, secondDispatch, fixture.now.Add(3*time.Second), nil); err == nil {
		t.Fatal("disconnect-fenced second job link unexpectedly committed")
	}
	assertAutoDeployDisconnectState(t, fixture, first.ApplicationID, "", 0)
	assertAutoDeployDisconnectState(t, fixture, second.ApplicationID, "", 0)
}

func TestDisconnectHelperFailureRollsBackConnectionCacheHeadAndJob(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	status := fixture.addApplication(t, autoDeployTestApp)
	status, lease, dispatch := fixture.reserveAndFinalize(t, status.ApplicationID, fixture.now)
	job, err := fixture.createAndLink(t, status, lease, dispatch, fixture.now.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := fixture.autoDeploy.Get(context.Background(), status.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.db.Exec(`CREATE TRIGGER auto_deploy_disconnect_test_failure BEFORE UPDATE OF state ON github_auto_deploy_heads WHEN NEW.pause_code='source_access_lost' BEGIN SELECT RAISE(ABORT,'injected auto-deploy pause failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = fixture.sources.Disconnect(context.Background(), "owner", fixture.connection.ID, fixture.now.Add(2*time.Second)); err == nil {
		t.Fatal("injected disconnect failure unexpectedly committed")
	}
	connection, err := fixture.sources.Get(context.Background(), "owner", fixture.connection.ID)
	if err != nil || connection.Status != StatusConnected {
		t.Fatalf("connection rollback=%#v err=%v", connection, err)
	}
	after, err := fixture.autoDeploy.Get(context.Background(), status.ApplicationID)
	if err != nil || after.State != autodeploy.StateDeploying || after.ActiveJobID != job.ID || after.LeaseFence != before.LeaseFence {
		t.Fatalf("head rollback=%#v before=%#v err=%v", after, before, err)
	}
	assertAutoDeployInstallationAndSubscription(t, fixture, status.SubscriptionID, 1, "active")
	assertAutoDeployJobCount(t, fixture.db, status.ApplicationID, 1)
}

func TestMarkAccessLostAtomicallyPausesAndFencesAutoDeploy(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	status := fixture.addApplication(t, autoDeployTestApp)
	_, lease, err := fixture.autoDeploy.ClaimDue(context.Background(), uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.autoDeploy.ReserveResolve(context.Background(), lease, 0, fixture.now); err != nil {
		t.Fatal(err)
	}
	if err = fixture.sources.MarkTerminal(context.Background(), "owner", fixture.connection.ID, StatusAccessLost, "source_access_lost", fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	paused, err := fixture.autoDeploy.Get(context.Background(), status.ApplicationID)
	if err != nil || paused.State != autodeploy.StatePaused || paused.PauseCode != autodeploy.PauseSourceAccessLost || paused.LeaseExpiresAt != nil || paused.LeaseFence != lease.Fence+1 {
		t.Fatalf("access-lost head=%#v err=%v", paused, err)
	}
	connection, err := fixture.sources.Get(context.Background(), "owner", fixture.connection.ID)
	if err != nil || connection.Status != StatusAccessLost {
		t.Fatalf("access-lost connection=%#v err=%v", connection, err)
	}
	assertAutoDeployInstallationAndSubscription(t, fixture, status.SubscriptionID, 1, "active")
}

type autoDeployResolver struct {
	mutex sync.Mutex
	calls int
}

func (resolver *autoDeployResolver) ResolveHead(context.Context, autodeploy.SourceScope) (string, error) {
	resolver.mutex.Lock()
	resolver.calls++
	resolver.mutex.Unlock()
	return autoDeployResolvedSHA, nil
}

func (resolver *autoDeployResolver) Calls() int {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return resolver.calls
}

type autoDeployClock struct{ now time.Time }

func (clock autoDeployClock) Now() time.Time { return clock.now }
func (clock autoDeployClock) NewTimer(delay time.Duration) autodeploy.Timer {
	return autoDeployTimer{Timer: time.NewTimer(delay)}
}

type autoDeployTimer struct{ *time.Timer }

func (timer autoDeployTimer) C() <-chan time.Time { return timer.Timer.C }

func TestAuthorizedResumeReconcilesGenerationZeroExactlyOnce(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	status := fixture.addApplication(t, autoDeployTestApp)
	if err := fixture.sources.MarkTerminal(context.Background(), "owner", fixture.connection.ID, StatusAccessLost, "source_access_lost", fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.autoDeploy.Resume(context.Background(), status.ApplicationID, "owner", status.Revision, fixture.now.Add(2*time.Second)); !errors.Is(err, autodeploy.ErrSourceAccessLost) {
		t.Fatalf("disconnected resume=%v", err)
	}
	bundle := TokenBundle{ProviderUserID: "42", ProviderLogin: "octocat", Generation: fixture.connection.CredentialGeneration + 1, AccessExpiresAt: fixture.now.Add(time.Hour), RefreshExpiresAt: fixture.now.Add(24 * time.Hour)}
	if err := fixture.sources.Connect(context.Background(), "owner", fixture.connection.ID, bundle, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	stillPaused, err := fixture.autoDeploy.Get(context.Background(), status.ApplicationID)
	if err != nil || stillPaused.State != autodeploy.StatePaused || stillPaused.PauseCode != autodeploy.PauseSourceAccessLost {
		t.Fatalf("reconnect auto-resumed status=%#v err=%v", stillPaused, err)
	}
	resumedAt := fixture.now.Add(4 * time.Second)
	resumed, err := fixture.autoDeploy.Resume(context.Background(), status.ApplicationID, "owner", status.Revision, resumedAt)
	if err != nil || resumed.State != autodeploy.StateIdle || resumed.NextReconcileAt == nil || !resumed.NextReconcileAt.Equal(resumedAt) {
		t.Fatalf("authorized resume=%#v err=%v", resumed, err)
	}
	resolver := &autoDeployResolver{}
	config := autodeploy.DefaultCoordinatorConfig()
	config.Clock = autoDeployClock{now: resumedAt}
	config.PollInterval = time.Hour
	config.MinResolveInterval = time.Nanosecond
	coordinator, err := autodeploy.NewCoordinator(fixture.autoDeploy, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	active := waitAutoDeployStatus(t, fixture.autoDeploy, status.ApplicationID, func(value autodeploy.Status) bool { return value.ActiveJobID != "" })
	for index := 0; index < 1000; index++ {
		coordinator.Wake()
	}
	time.Sleep(20 * time.Millisecond)
	if resolver.Calls() != 1 || active.LastConsumedGeneration != 0 || active.LatestResolvedGeneration != 0 || active.LatestResolvedSHA != autoDeployResolvedSHA {
		t.Fatalf("generation-zero convergence=%#v resolver_calls=%d", active, resolver.Calls())
	}
	assertAutoDeployJobCount(t, fixture.db, status.ApplicationID, 1)
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedResumeWithActiveJobPollsWithoutParallelResolve(t *testing.T) {
	fixture := newAutoDeployDisconnectFixture(t)
	status := fixture.addApplication(t, autoDeployTestApp)
	status, lease, dispatch := fixture.reserveAndFinalize(t, status.ApplicationID, fixture.now)
	job, err := fixture.createAndLink(t, status, lease, dispatch, fixture.now.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.sources.MarkTerminal(context.Background(), "owner", fixture.connection.ID, StatusAccessLost, "source_access_lost", fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	bundle := TokenBundle{ProviderUserID: "42", ProviderLogin: "octocat", Generation: fixture.connection.CredentialGeneration + 1, AccessExpiresAt: fixture.now.Add(time.Hour), RefreshExpiresAt: fixture.now.Add(24 * time.Hour)}
	if err = fixture.sources.Connect(context.Background(), "owner", fixture.connection.ID, bundle, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	resumedAt := fixture.now.Add(4 * time.Second)
	resumed, err := fixture.autoDeploy.Resume(context.Background(), status.ApplicationID, "owner", status.Revision, resumedAt)
	if err != nil || resumed.State != autodeploy.StateDeploying || resumed.ActiveJobID != job.ID || resumed.NextJobPollAt == nil || !resumed.NextJobPollAt.Equal(resumedAt) {
		t.Fatalf("active resume=%#v err=%v", resumed, err)
	}
	resolver := &autoDeployResolver{}
	config := autodeploy.DefaultCoordinatorConfig()
	config.Clock = autoDeployClock{now: resumedAt}
	config.PollInterval = time.Hour
	config.MinResolveInterval = time.Nanosecond
	coordinator, err := autodeploy.NewCoordinator(fixture.autoDeploy, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	polled := waitAutoDeployStatus(t, fixture.autoDeploy, status.ApplicationID, func(value autodeploy.Status) bool {
		return value.ActiveJobID == job.ID && value.NextJobPollAt != nil && value.NextJobPollAt.After(resumedAt)
	})
	if resolver.Calls() != 0 || polled.State != autodeploy.StateDeploying {
		t.Fatalf("active resume parallel-resolved status=%#v calls=%d", polled, resolver.Calls())
	}
	assertAutoDeployJobCount(t, fixture.db, status.ApplicationID, 1)
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func waitAutoDeployStatus(t *testing.T, repository *autodeploy.Repository, applicationID string, ready func(autodeploy.Status) bool) autodeploy.Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		value, err := repository.Get(context.Background(), applicationID)
		if err != nil {
			t.Fatal(err)
		}
		if ready(value) {
			return value
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto-deploy status did not converge: %#v", value)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertAutoDeployDisconnectState(t *testing.T, fixture *autoDeployDisconnectFixture, applicationID, activeJobID string, jobsWant int) {
	t.Helper()
	status, err := fixture.autoDeploy.Get(context.Background(), applicationID)
	if err != nil || status.State != autodeploy.StatePaused || status.PauseCode != autodeploy.PauseSourceAccessLost || status.ActiveJobID != activeJobID || status.LeaseExpiresAt != nil || status.PreparedDispatchSequence != 0 || status.NextRetryAt != nil || status.NextReconcileAt != nil {
		t.Fatalf("disconnect state=%#v active_job=%q err=%v", status, activeJobID, err)
	}
	connection, err := fixture.sources.Get(context.Background(), "owner", fixture.connection.ID)
	if err != nil || connection.Status != StatusDisconnected {
		t.Fatalf("disconnect connection=%#v err=%v", connection, err)
	}
	assertAutoDeployInstallationAndSubscription(t, fixture, status.SubscriptionID, 0, "active")
	assertAutoDeployJobCount(t, fixture.db, applicationID, jobsWant)
}

func assertAutoDeployInstallationAndSubscription(t *testing.T, fixture *autoDeployDisconnectFixture, subscriptionID string, installations int, subscriptionState string) {
	t.Helper()
	var installationCount int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM github_installations WHERE connection_id=?`, fixture.connection.ID).Scan(&installationCount); err != nil || installationCount != installations {
		t.Fatalf("installation count=%d want=%d err=%v", installationCount, installations, err)
	}
	var state string
	if err := fixture.db.QueryRow(`SELECT state FROM relay_controller_subscriptions WHERE subscription_id=?`, subscriptionID).Scan(&state); err != nil || state != subscriptionState {
		t.Fatalf("subscription state=%q want=%q err=%v", state, subscriptionState, err)
	}
}

func assertAutoDeployJobCount(t *testing.T, db *sql.DB, applicationID string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE resource_id=?`, applicationID).Scan(&count); err != nil || count != want {
		t.Fatalf("job count=%d want=%d err=%v", count, want, err)
	}
}
