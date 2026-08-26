package autodeploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/controllerrelay"
	controldb "github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	testOwner        = "owner"
	testOtherOwner   = "other-owner"
	testApp          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testController   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testBinding      = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	testConnection   = "connection-a"
	testInstallation = int64(101)
	testRepository   = int64(202)
	testRef          = "refs/heads/main"
	testSHA          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secondSHA        = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type repositoryFixture struct {
	db         *sql.DB
	repository *Repository
	now        time.Time
}

func newRepositoryFixture(t *testing.T) *repositoryFixture {
	t.Helper()
	db, err := controldb.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	stamp := timestamp(now)
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?,'administrator',?,?),(?,?,?,'administrator',?,?)`, testOwner, testOwner, "hash", stamp, stamp, testOtherOwner, testOtherOwner, "hash", stamp, stamp)
	mustExec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES(?,?,'github','connected','42','octocat',1,?,?,?,?,?)`, testConnection, testOwner, stamp, stamp, stamp, stamp, stamp)
	mustExec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,'Application','draft',?,?)`, testApp, testApp, stamp, stamp)
	mustExec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?,'github',?,?,?,'octo','app','main',?,'compose.yaml',?,?,?)`, testApp, testConnection, testInstallation, testRepository, testRef, testSHA, stamp, stamp)
	mustExec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, testController, stamp, stamp)
	mustExec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, testBinding, testOwner, testConnection, testController, testInstallation, testRepository, stamp, stamp, stamp)
	return &repositoryFixture{db: db, repository: NewRepository(db), now: now}
}

func TestConfigureIsDefaultOffOwnerScopedAndRetiresLastSubscription(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	initial, err := fixture.repository.Get(ctx, testApp)
	if err != nil || initial.Enabled || initial.Revision != 0 || initial.State != StateDisabled {
		t.Fatalf("initial status=%#v err=%v", initial, err)
	}
	if _, err = fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOtherOwner, ExpectedRevision: 0, Enabled: true}, fixture.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner enable=%v", err)
	}
	enabled, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
	if err != nil || !enabled.Enabled || enabled.Revision != 1 || enabled.State != StateIdle || enabled.ControllerID != testController || enabled.BindingID != testBinding || enabled.SubscriptionID == "" {
		t.Fatalf("enabled status=%#v err=%v", enabled, err)
	}
	for _, request := range []ConfigureRequest{
		{ApplicationID: testApp, ActorUserID: testOtherOwner, ExpectedRevision: 1, Enabled: true},
		{ApplicationID: testApp, ActorUserID: testOtherOwner, ExpectedRevision: 1, Enabled: false},
	} {
		if _, err = fixture.repository.Configure(ctx, request, fixture.now.Add(time.Millisecond)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-owner enabled mutation %#v = %v", request, err)
		}
	}
	afterUnauthorized, err := fixture.repository.Get(ctx, testApp)
	if err != nil || afterUnauthorized.Revision != 1 || !afterUnauthorized.Enabled || afterUnauthorized.SubscriptionID != enabled.SubscriptionID {
		t.Fatalf("cross-owner mutation changed state=%#v err=%v", afterUnauthorized, err)
	}
	if _, err = fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: false}, fixture.now); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale disable=%v", err)
	}

	secondApp := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	stamp := timestamp(fixture.now)
	if _, err = fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,'Second','draft',?,?)`, secondApp, secondApp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.db.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?,'github',?,?,?,'octo','app','main',?,'compose.yaml',?,?,?)`, secondApp, testConnection, testInstallation, testRepository, testRef, testSHA, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	second, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: secondApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now.Add(time.Second))
	if err != nil || second.SubscriptionID != enabled.SubscriptionID {
		t.Fatalf("shared subscription=%#v err=%v", second, err)
	}
	disabled, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 1, Enabled: false}, fixture.now.Add(2*time.Second))
	if err != nil || disabled.Enabled || disabled.State != StateDisabled || disabled.SubscriptionID != "" {
		t.Fatalf("first disable=%#v err=%v", disabled, err)
	}
	var subscriptionState string
	if err = fixture.db.QueryRow(`SELECT state FROM relay_controller_subscriptions WHERE subscription_id=?`, enabled.SubscriptionID).Scan(&subscriptionState); err != nil || subscriptionState != "active" {
		t.Fatalf("shared subscription state=%q err=%v", subscriptionState, err)
	}
	if _, err = fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: secondApp, ActorUserID: testOwner, ExpectedRevision: 1, Enabled: false}, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.db.QueryRow(`SELECT state FROM relay_controller_subscriptions WHERE subscription_id=?`, enabled.SubscriptionID).Scan(&subscriptionState); err != nil || subscriptionState != "retired" {
		t.Fatalf("last subscription state=%q err=%v", subscriptionState, err)
	}
}

func TestConfigureRejectsLocalArchivedAndConcurrentCAS(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	localApp := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	stamp := timestamp(fixture.now)
	if _, err := fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,'Local','draft',?,?)`, localApp, localApp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO application_sources(application_id,source_type,created_at,updated_at) VALUES(?,'local',?,?)`, localApp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: localApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("local enable=%v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, stamp, testApp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived enable=%v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE applications SET archived_at=NULL WHERE id=?`, testApp); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent enable=%v", err)
		}
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("concurrent enable successes=%d conflicts=%d", successes, conflicts)
	}
	status, err := fixture.repository.Get(ctx, testApp)
	if err != nil || status.Revision != 1 || !status.Enabled {
		t.Fatalf("post-concurrency status=%#v err=%v", status, err)
	}
	refreshedAt := fixture.now.Add(time.Minute)
	if _, err := fixture.db.Exec(`UPDATE application_sources SET repository_owner='renamed-owner',repository_name='renamed-repository',resolved_sha=?,updated_at=? WHERE application_id=?`, secondSHA, timestamp(refreshedAt), testApp); err != nil {
		t.Fatalf("materializer-compatible source refresh: %v", err)
	}
	var repositoryOwner, repositoryName, resolvedSHA, updatedAt string
	if err := fixture.db.QueryRow(`SELECT repository_owner,repository_name,resolved_sha,updated_at FROM application_sources WHERE application_id=?`, testApp).Scan(&repositoryOwner, &repositoryName, &resolvedSHA, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if repositoryOwner != "renamed-owner" || repositoryName != "renamed-repository" || resolvedSHA != secondSHA || updatedAt != timestamp(refreshedAt) {
		t.Fatalf("source refresh owner=%q name=%q sha=%q updated=%q", repositoryOwner, repositoryName, resolvedSHA, updatedAt)
	}
	if _, err := fixture.db.Exec(`UPDATE application_sources SET tracked_branch='other',tracked_ref='refs/heads/other' WHERE application_id=?`, testApp); err == nil {
		t.Fatal("enabled source changed")
	}
	if _, err := fixture.db.Exec(`UPDATE application_sources SET repository_id=? WHERE application_id=?`, testRepository+1, testApp); err == nil {
		t.Fatal("enabled repository scope changed")
	}
	if _, err := fixture.db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, stamp, testApp); err == nil {
		t.Fatal("enabled app archived")
	}
}

func TestConfigureRejectsDisableWhileAutoDeployJobIsNonterminal(t *testing.T) {
	for _, jobStatus := range []string{"queued", "running", "waiting_user"} {
		t.Run(jobStatus, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			status, lease, dispatch := prepareCoordinatorDispatch(t, fixture)
			jobID := insertCoordinatorJob(t, fixture, status, dispatch, jobStatus, "")
			if err := fixture.repository.LinkDispatchJob(context.Background(), lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			before, err := fixture.repository.Get(context.Background(), testApp)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 1, Enabled: false}, fixture.now.Add(2*time.Second)); !errors.Is(err, ErrApplicationBusy) {
				t.Fatalf("disable %s job=%v", jobStatus, err)
			}
			after, err := fixture.repository.Get(context.Background(), testApp)
			if err != nil || after.Revision != before.Revision || !after.Enabled || after.State != before.State || after.ActiveJobID != jobID || after.SubscriptionID != before.SubscriptionID {
				t.Fatalf("busy disable mutated status before=%#v after=%#v err=%v", before, after, err)
			}
		})
	}

	t.Run("terminal job", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		status, lease, dispatch := prepareCoordinatorDispatch(t, fixture)
		jobID := insertCoordinatorJob(t, fixture, status, dispatch, "failed", "invalid_source")
		if err := fixture.repository.LinkDispatchJob(context.Background(), lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		disabled, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 1, Enabled: false}, fixture.now.Add(2*time.Second))
		if err != nil || disabled.Enabled || disabled.State != StateDisabled || disabled.Revision != 2 {
			t.Fatalf("terminal job disable status=%#v err=%v", disabled, err)
		}
	})
}

func TestLeaseFenceRecoveryAndBoundedStatusListing(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	status, first, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Second)
	if err != nil || status.LeaseFence != 1 || first.Fence != 1 {
		t.Fatalf("first claim status=%#v lease=%#v err=%v", status, first, err)
	}
	if _, _, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("overlapping claim=%v", err)
	}
	if recovered, err := fixture.repository.RecoverExpiredLeases(ctx, fixture.now.Add(2*time.Second), 10); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	if err := fixture.repository.ReleaseLease(ctx, first, fixture.now.Add(2*time.Second)); !errors.Is(err, ErrState) {
		t.Fatalf("stale release=%v", err)
	}
	_, second, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(2*time.Second), time.Minute)
	if err != nil || second.Fence != 2 {
		t.Fatalf("second lease=%#v err=%v", second, err)
	}
	if err := fixture.repository.ReleaseLease(ctx, second, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	values, err := fixture.repository.List(ctx, "", 1)
	if err != nil || len(values) != 1 || values[0].ApplicationID != testApp {
		t.Fatalf("bounded list=%#v err=%v", values, err)
	}
	if _, exposed := reflect.TypeOf(Status{}).FieldByName("LeaseToken"); exposed {
		t.Fatal("general status exposes the coordinator lease token")
	}
}

func TestLeaseExpiryUsesFixedNanosecondBoundariesAndFencesReclaims(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	now := fixture.now.Add(123456789 * time.Nanosecond)
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, now); err != nil {
		t.Fatal(err)
	}
	_, first, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := fixture.repository.RecoverExpiredLeases(ctx, first.ExpiresAt.Add(-time.Nanosecond), 1); err != nil || recovered != 0 {
		t.Fatalf("recovered before fractional expiry=%d err=%v", recovered, err)
	}
	if err := fixture.repository.Pause(ctx, first, PauseDeploymentFailed, first.ExpiresAt); !errors.Is(err, ErrState) {
		t.Fatalf("pause at exact expiry=%v", err)
	}
	if err := fixture.repository.ScheduleRetry(ctx, first, first.ExpiresAt.Add(time.Second), first.ExpiresAt); !errors.Is(err, ErrState) {
		t.Fatalf("retry at exact expiry=%v", err)
	}
	if recovered, err := fixture.repository.RecoverExpiredLeases(ctx, first.ExpiresAt, 1); err != nil || recovered != 1 {
		t.Fatalf("recovered at fractional expiry=%d err=%v", recovered, err)
	}
	_, second, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), first.ExpiresAt, time.Minute)
	if err != nil || second.Fence != first.Fence+1 {
		t.Fatalf("reclaimed lease=%#v err=%v", second, err)
	}
	if err = fixture.repository.ScheduleRetry(ctx, first, first.ExpiresAt.Add(time.Second), first.ExpiresAt); !errors.Is(err, ErrState) {
		t.Fatalf("old lease scheduled retry after reclaim=%v", err)
	}
	if err = fixture.repository.Pause(ctx, first, PauseDeploymentFailed, first.ExpiresAt.Add(time.Nanosecond)); !errors.Is(err, ErrState) {
		t.Fatalf("old lease paused after reclaim=%v", err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, second, 0, testSHA, first.ExpiresAt.Add(6*time.Hour), first.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ScheduleRetry(ctx, second, first.ExpiresAt.Add(2*time.Second), first.ExpiresAt.Add(time.Second)); err != nil {
		t.Fatalf("current lease retry=%v", err)
	}
	status, err := fixture.repository.Get(ctx, testApp)
	if err != nil || status.NextRetryAt == nil || !status.NextRetryAt.Equal(first.ExpiresAt.Add(2*time.Second)) {
		t.Fatalf("fixed retry timestamp status=%#v err=%v", status, err)
	}
}

func TestPrepareDispatchEnforcesRetryDeadline(t *testing.T) {
	for _, test := range []struct {
		name      string
		offset    time.Duration
		wantState bool
	}{
		{name: "before", offset: -time.Nanosecond, wantState: true},
		{name: "at", offset: 0},
		{name: "after", offset: time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			ctx := context.Background()
			now := fixture.now.Add(234567891 * time.Nanosecond)
			if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, now); err != nil {
				t.Fatal(err)
			}
			_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, now.Add(6*time.Hour), now); err != nil {
				t.Fatal(err)
			}
			due := now.Add(5 * time.Second)
			if err = fixture.repository.ScheduleRetry(ctx, lease, due, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			_, err = fixture.repository.PrepareDispatch(ctx, lease, due.Add(test.offset))
			if test.wantState && !errors.Is(err, ErrState) {
				t.Fatalf("prepare before retry deadline=%v", err)
			}
			if !test.wantState && err != nil {
				t.Fatalf("prepare %s retry deadline=%v", test.name, err)
			}
		})
	}
}

func TestClaimDueUsesFractionalReconcileBoundary(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	due := fixture.now.Add(345678912 * time.Nanosecond)
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, due); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), due.Add(-time.Nanosecond), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claimed before fractional reconcile boundary=%v", err)
	}
	if _, _, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), due, time.Minute); err != nil {
		t.Fatalf("not claimable at fractional reconcile boundary=%v", err)
	}
}

func TestClaimDueDoesNotBusyPollActiveJobAheadOfOtherApps(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	firstStatus, firstLease, dispatch := prepareCoordinatorDispatch(t, fixture)
	jobID := insertCoordinatorJob(t, fixture, firstStatus, dispatch, "running", "")
	linkedAt := fixture.now.Add(time.Second)
	if err := fixture.repository.LinkDispatchJob(ctx, firstLease, dispatch.Sequence, dispatch.Generation, jobID, linkedAt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.ReleaseLease(ctx, firstLease, linkedAt.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	secondApp := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	insertGitHubApplication(t, fixture, secondApp, "Second")
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: secondApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, linkedAt); err != nil {
		t.Fatal(err)
	}
	claimed, secondLease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), linkedAt.Add(time.Second), time.Minute)
	if err != nil || claimed.ApplicationID != secondApp {
		t.Fatalf("fair claim status=%#v err=%v", claimed, err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, secondLease, 0, testSHA, linkedAt.Add(6*time.Hour), linkedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReleaseLease(ctx, secondLease, linkedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, _, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), linkedAt.Add(activeJobPollInterval), time.Minute)
	if err != nil || claimed.ApplicationID != testApp {
		t.Fatalf("active job not claimable at bounded poll deadline status=%#v err=%v", claimed, err)
	}
}

func TestClaimDueUsesEarliestCurrentlyDueReason(t *testing.T) {
	t.Run("retry future does not mask ACK", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		ctx := context.Background()
		first, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
		if err != nil {
			t.Fatal(err)
		}
		_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
			t.Fatal(err)
		}
		if err = fixture.repository.ScheduleRetry(ctx, lease, fixture.now.Add(time.Hour), fixture.now.Add(time.Nanosecond)); err != nil {
			t.Fatal(err)
		}
		if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(2*time.Nanosecond)); err != nil {
			t.Fatal(err)
		}
		commitDesired(t, fixture, first, 1, secondSHA, fixture.now.Add(time.Second))

		secondApp := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		insertGitHubApplicationWithBranch(t, fixture, secondApp, "Second", "other")
		if _, err = fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: secondApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		claimed, _, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(3*time.Second), time.Minute)
		if err != nil || claimed.ApplicationID != testApp {
			t.Fatalf("future retry masked earlier ACK status=%#v err=%v", claimed, err)
		}
	})

	t.Run("job poll does not mask ACK", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		ctx := context.Background()
		first, lease, dispatch := prepareCoordinatorDispatch(t, fixture)
		jobID := insertCoordinatorJob(t, fixture, first, dispatch, "running", "")
		if err := fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now); err != nil {
			t.Fatal(err)
		}
		if err := fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(time.Nanosecond)); err != nil {
			t.Fatal(err)
		}
		commitDesired(t, fixture, first, 1, secondSHA, fixture.now.Add(time.Second))

		secondApp := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		insertGitHubApplicationWithBranch(t, fixture, secondApp, "Second", "other")
		if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: secondApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		claimed, _, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(10*time.Second), time.Minute)
		if err != nil || claimed.ApplicationID != testApp {
			t.Fatalf("job poll masked earlier ACK status=%#v err=%v", claimed, err)
		}
	})
}

func TestClaimDueOrdersExactSecondACKBeforeFractionalDeadline(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	first, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, first, 1, secondSHA, fixture.now)

	secondApp := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	insertGitHubApplicationWithBranch(t, fixture, secondApp, "Second", "other")
	fractionalDue := fixture.now.Add(100 * time.Millisecond)
	if _, err = fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: secondApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fractionalDue); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(time.Second), time.Minute)
	if err != nil || claimed.ApplicationID != testApp {
		t.Fatalf("fractional deadline sorted before exact-second ACK status=%#v err=%v", claimed, err)
	}
	var receivedAt string
	if err = fixture.db.QueryRow(`SELECT received_at FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, testController, first.SubscriptionID).Scan(&receivedAt); err != nil {
		t.Fatal(err)
	}
	if receivedAt != timestamp(fixture.now) {
		t.Fatalf("ACK timestamp=%q want=%q", receivedAt, timestamp(fixture.now))
	}
}

func TestClaimDueCandidateDiscoveryUsesReasonSpecificIndexes(t *testing.T) {
	fixture := newRepositoryFixture(t)
	rows, err := fixture.db.Query(`EXPLAIN QUERY PLAN `+claimDueCandidateSQL, timestamp(fixture.now), timestamp(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	details := make([]string, 0)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
		upper := strings.ToUpper(detail)
		if strings.Contains(upper, "SCAN H") && !strings.Contains(upper, "USING INDEX") {
			t.Fatalf("unindexed work-head discovery: %s", detail)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "; ")
	for _, index := range []string{
		"github_auto_deploy_dispatch_due",
		"github_auto_deploy_job_poll_due",
		"github_auto_deploy_ack_live",
		"relay_subscription_active_set",
		"relay_source_ack_active",
		"github_auto_deploy_unresolved_due",
		"github_auto_deploy_reconcile_due",
		"github_auto_deploy_retry_due",
	} {
		if !strings.Contains(plan, index) {
			t.Errorf("candidate plan omitted %s: %s", index, plan)
		}
	}
	if strings.Count(claimDueCandidateSQL, "LIMIT 1") != 7 {
		t.Fatalf("candidate discovery is not branch-bounded: %s", claimDueCandidateSQL)
	}
}

func TestClaimDueACKCandidateIsDrivenByLiveHeads(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	if _, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	seedRetiredACKHeads(t, fixture, 2048)
	if _, _, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(time.Second), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retired ACK history became eligible work: %v", err)
	}

	rows, err := fixture.db.Query(`EXPLAIN QUERY PLAN `+claimDueACKCandidateSQL, timestamp(fixture.now.Add(time.Second)), timestamp(fixture.now.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"SCAN h USING INDEX github_auto_deploy_ack_live",
		"SEARCH c USING INDEX sqlite_autoindex_github_auto_deploy_configs_1 (application_id=?)",
		"SEARCH s USING COVERING INDEX relay_subscription_active_set (controller_id=? AND subscription_id=?)",
		"SEARCH a USING INDEX relay_source_ack_active (controller_id=? AND subscription_id=? AND generation>?)",
		"USE TEMP B-TREE FOR ORDER BY",
	}
	if !reflect.DeepEqual(details, expected) {
		t.Fatalf("unexpected live ACK candidate plan\n got: %#v\nwant: %#v", details, expected)
	}
}

func TestPausedHeadCrashRecoveryDispatchReplayAndSuccessFollowup(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.Pause(ctx, lease, PauseMissingConfig, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	commitDesired(t, fixture, status, 1, testSHA, fixture.now.Add(3*time.Second))
	_, lease, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(3*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("claim paused new generation: %v", err)
	}
	head, err := fixture.repository.PeekNewestACK(ctx, lease, fixture.now.Add(3*time.Second))
	if err != nil || head.Generation != 1 {
		t.Fatalf("peek generation=%#v err=%v", head, err)
	}
	if err = fixture.repository.ReserveResolve(ctx, lease, head.Generation, fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.FinalizeResolvedHead(ctx, lease, 1, testSHA, fixture.now.Add(6*time.Hour), fixture.now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	stillPaused, err := fixture.repository.Get(ctx, testApp)
	if err != nil || stillPaused.State != StatePaused || stillPaused.PauseCode != PauseMissingConfig {
		t.Fatalf("same SHA resumed pause=%#v err=%v", stillPaused, err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(5*time.Second), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("same paused SHA stayed due: %v", err)
	}
	_, lease, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(6*time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("paused periodic reconciliation: %v", err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 1, secondSHA, fixture.now.Add(12*time.Hour), fixture.now.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	periodic, err := fixture.repository.Get(ctx, testApp)
	if err != nil || periodic.State != StateIdle || periodic.LatestResolvedGeneration != 1 || periodic.LatestResolvedSHA != secondSHA {
		t.Fatalf("same-generation periodic head=%#v err=%v", periodic, err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(6*time.Hour+time.Second)); err != nil {
		t.Fatal(err)
	}

	thirdSHA := "cccccccccccccccccccccccccccccccccccccccc"
	commitDesired(t, fixture, status, 2, thirdSHA, fixture.now.Add(6*time.Hour+2*time.Second))
	_, lease, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(6*time.Hour+2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReserveResolve(ctx, lease, 2, fixture.now.Add(6*time.Hour+2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(6*time.Hour+3*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, lease, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(6*time.Hour+4*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("claim after crash between consume and resolve: %v", err)
	}
	if err = fixture.repository.ReserveResolve(ctx, lease, 2, fixture.now.Add(6*time.Hour+4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.FinalizeResolvedHead(ctx, lease, 2, thirdSHA, fixture.now.Add(12*time.Hour), fixture.now.Add(6*time.Hour+4*time.Second)); err != nil {
		t.Fatal(err)
	}
	resumed, err := fixture.repository.Get(ctx, testApp)
	if err != nil || resumed.State != StateIdle || resumed.PauseCode != "" {
		t.Fatalf("new SHA resume=%#v err=%v", resumed, err)
	}
	dispatch, err := fixture.repository.PrepareDispatch(ctx, lease, fixture.now.Add(6*time.Hour+5*time.Second))
	if err != nil || dispatch.Generation != 2 || dispatch.SHA != thirdSHA {
		t.Fatalf("prepare=%#v err=%v", dispatch, err)
	}
	replayed, err := fixture.repository.PrepareDispatch(ctx, lease, fixture.now.Add(6*time.Hour+6*time.Second))
	if err != nil || replayed != dispatch {
		t.Fatalf("dispatch replay=%#v err=%v", replayed, err)
	}
	jobID := insertCoordinatorJob(t, fixture, resumed, dispatch, "queued", "")
	if err = fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(6*time.Hour+7*time.Second)); err != nil {
		t.Fatal(err)
	}
	fourthSHA := "dddddddddddddddddddddddddddddddddddddddd"
	commitDesired(t, fixture, status, 3, fourthSHA, fixture.now.Add(6*time.Hour+8*time.Second))
	if err = fixture.repository.ReserveResolve(ctx, lease, 3, fixture.now.Add(6*time.Hour+8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.FinalizeResolvedHead(ctx, lease, 3, fourthSHA, fixture.now.Add(12*time.Hour), fixture.now.Add(6*time.Hour+8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=? WHERE id=?`, timestamp(fixture.now.Add(6*time.Hour+9*time.Second)), timestamp(fixture.now.Add(6*time.Hour+9*time.Second)), jobID); err != nil {
		t.Fatal(err)
	}
	releaseID := insertActualRelease(t, fixture, thirdSHA, dispatch.Sequence)
	if _, err = fixture.db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,started_at,finished_at) VALUES(?,?,?,?, 'succeeded','current',1,?,?)`, uuid.NewString(), testApp, releaseID, jobID, timestamp(fixture.now), timestamp(fixture.now.Add(13*time.Second))); err != nil {
		t.Fatal(err)
	}
	completedAt := fixture.now.Add(6*time.Hour + 10*time.Second)
	completed, err := fixture.repository.RefreshActiveJob(ctx, lease, completedAt)
	if err != nil || completed.State != StateIdle || completed.LastSuccessfulDeployedSHA != thirdSHA || completed.NextReconcileAt == nil || !completed.NextReconcileAt.Equal(completedAt) {
		t.Fatalf("completed follow-up=%#v err=%v", completed, err)
	}
	if err = fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(6*time.Hour+11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(6*time.Hour+12*time.Second), time.Minute); err != nil {
		t.Fatalf("exact follow-up was not durable: %v", err)
	}
}

func TestReserveResolveRejectsGenerationBeyondCompactHead(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now)
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReserveResolve(ctx, lease, 2, fixture.now); !errors.Is(err, ErrState) {
		t.Fatalf("future generation reservation=%v", err)
	}
	unchanged, err := fixture.repository.Get(ctx, testApp)
	if err != nil || unchanged.LastConsumedGeneration != 0 || unchanged.LatestResolvedSHA != "" {
		t.Fatalf("future generation mutated state=%#v err=%v", unchanged, err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 1, secondSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	recorded, err := fixture.repository.Get(ctx, testApp)
	if err != nil || recorded.LastConsumedGeneration != 1 || recorded.LatestResolvedGeneration != 1 || recorded.LatestResolvedSHA != secondSHA {
		t.Fatalf("exact checkpoint state=%#v err=%v", recorded, err)
	}
}

func TestResolveReservationIsFencedAndClearedOnAccessLoss(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now)
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReserveResolve(ctx, lease, 1, fixture.now); err != nil {
		t.Fatal(err)
	}
	removedAt := fixture.now.Add(time.Second)
	decision, err := controllerrelay.NewRepository(fixture.db).CommitAccessChange(ctx, testController, protocol.AccessChange{
		Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), removedAt), EventID: uuid.NewString(),
		InstallationID: testInstallation, RepositoryID: testRepository, ChangeCode: "repository.removed", ObservedAt: removedAt, AckRequired: true,
	}, removedAt)
	if err != nil || decision.Kind != controllerrelay.DecisionAck {
		t.Fatalf("access removal=%#v err=%v", decision, err)
	}
	if err = fixture.repository.FinalizeResolvedHead(ctx, lease, 1, secondSHA, fixture.now.Add(6*time.Hour), removedAt); !errors.Is(err, ErrState) {
		t.Fatalf("fenced finalization=%v", err)
	}
	paused, err := fixture.repository.Get(ctx, testApp)
	if err != nil || paused.State != StatePaused || paused.PauseCode != PauseSourceAccessLost {
		t.Fatalf("access-loss state=%#v err=%v", paused, err)
	}
	var generation, fence sql.NullInt64
	if err = fixture.db.QueryRow(`SELECT resolving_generation,resolving_lease_fence FROM github_auto_deploy_heads WHERE application_id=?`, testApp).Scan(&generation, &fence); err != nil {
		t.Fatal(err)
	}
	if generation.Valid || fence.Valid {
		t.Fatalf("access loss retained reservation generation=%#v fence=%#v", generation, fence)
	}
}

func TestFinalizeResolveDynamicAccessLossRetainsActiveJob(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, lease, dispatch := prepareCoordinatorDispatch(t, fixture)
	jobID := insertCoordinatorJob(t, fixture, status, dispatch, "running", "")
	if err := fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.ReleaseLease(ctx, lease, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now.Add(2*time.Second))
	_, resolveLease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.ReserveResolve(ctx, resolveLease, 1, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	setSourceConnectionStatus(t, fixture, "disconnected", fixture.now.Add(3*time.Second))
	if err = fixture.repository.FinalizeResolvedHead(ctx, resolveLease, 1, secondSHA, fixture.now.Add(6*time.Hour), fixture.now.Add(3*time.Second)); !errors.Is(err, ErrSourceAccessLost) {
		t.Fatalf("dynamic access-loss finalization=%v", err)
	}
	paused, err := fixture.repository.Get(ctx, testApp)
	if err != nil || paused.State != StatePaused || paused.PauseCode != PauseSourceAccessLost || paused.ActiveJobID != jobID || paused.ActiveSHA != testSHA || paused.NextJobPollAt == nil || paused.LeaseExpiresAt != nil || paused.LastConsumedGeneration != 0 {
		t.Fatalf("active job was not retained across dynamic access loss status=%#v err=%v", paused, err)
	}
	assertNoResolveReservation(t, fixture.db, testApp)
}

func TestLinkDerivesWaitingAndTerminalJobStateWithoutMutatingJobs(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	dispatch, err := fixture.repository.PrepareDispatch(ctx, lease, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	jobID := insertCoordinatorJob(t, fixture, status, dispatch, "waiting_user", "")
	if err = fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	paused, err := fixture.repository.Get(ctx, testApp)
	if err != nil || paused.State != StatePaused || paused.ActiveJobID != jobID || paused.PauseCode != PauseApprovalRequired {
		t.Fatalf("waiting job status=%#v err=%v", paused, err)
	}
	if _, err = fixture.repository.Resume(ctx, testApp, testOwner, 1, fixture.now.Add(2*time.Second)); !errors.Is(err, ErrState) {
		t.Fatalf("waiting job bypassed approval=%v", err)
	}
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='queued',phase='queued',pause_disposition=NULL,updated_at=? WHERE id=?`, timestamp(fixture.now.Add(2*time.Second)), jobID); err != nil {
		t.Fatal(err)
	}
	resumed, err := fixture.repository.Resume(ctx, testApp, testOwner, 1, fixture.now.Add(3*time.Second))
	if err != nil || resumed.State != StateDeploying || resumed.ActiveJobID != jobID {
		t.Fatalf("approved job resume=%#v err=%v", resumed, err)
	}
	if _, err = fixture.repository.RefreshActiveJob(ctx, lease, fixture.now.Add(3*time.Second)); !errors.Is(err, ErrState) {
		t.Fatalf("administrator resume did not fence worker lease: %v", err)
	}
	_, lease, err = fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now.Add(3*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("claim approved deployment: %v", err)
	}
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='needs_attention',phase='needs_attention',error_code='configuration_unavailable',updated_at=?,finished_at=? WHERE id=?`, timestamp(fixture.now.Add(4*time.Second)), timestamp(fixture.now.Add(4*time.Second)), jobID); err != nil {
		t.Fatal(err)
	}
	attention, err := fixture.repository.RefreshActiveJob(ctx, lease, fixture.now.Add(5*time.Second))
	if err != nil || attention.State != StatePaused || attention.ActiveJobID != "" || attention.PauseCode != PauseMissingConfig {
		t.Fatalf("terminal attention=%#v err=%v", attention, err)
	}
}

func TestLinkAcceptsExactTerminalRaceAndRejectsUnrelatedJob(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	dispatch, err := fixture.repository.PrepareDispatch(ctx, lease, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	jobID := insertCoordinatorJob(t, fixture, status, dispatch, "needs_attention", "invalid_source")
	if err = fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation+1, jobID, fixture.now.Add(time.Second)); !errors.Is(err, ErrState) {
		t.Fatalf("mismatched dispatch generation linked: %v", err)
	}
	if err = fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.repository.Get(ctx, testApp)
	if err != nil || got.State != StatePaused || got.ActiveJobID != "" || got.PauseCode != PauseInvalidSource {
		t.Fatalf("terminal link=%#v err=%v", got, err)
	}
}

func TestSuccessfulCurrentSourceJobRecordsActualForwardRelease(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	status, lease, dispatch := prepareCoordinatorDispatch(t, fixture)
	jobID := insertCoordinatorJob(t, fixture, status, dispatch, "running", "")
	if err := fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=? WHERE id=?`, timestamp(fixture.now.Add(2*time.Second)), timestamp(fixture.now.Add(2*time.Second)), jobID); err != nil {
		t.Fatal(err)
	}
	actualRelease := insertReleaseWithProvenance(t, fixture, testApp, "github", testRepository, testRef, secondSHA, dispatch.Sequence)
	insertSucceededDeployment(t, fixture, testApp, actualRelease, jobID)
	completedAt := fixture.now.Add(3 * time.Second)
	completed, err := fixture.repository.RefreshActiveJob(ctx, lease, completedAt)
	if err != nil || completed.State != StateIdle || completed.LastSuccessfulDeployedSHA != secondSHA || completed.LastSuccessfulDeployedSHA == dispatch.SHA {
		t.Fatalf("current-source forward completion=%#v dispatch=%#v err=%v", completed, dispatch, err)
	}
	if completed.NextReconcileAt == nil || !completed.NextReconcileAt.Equal(completedAt) {
		t.Fatalf("forward completion did not retain coalescing work=%#v", completed)
	}
}

func TestSuccessfulJobRejectsMismatchedReleaseProvenance(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		repository int64
		ref        string
		otherApp   bool
	}{
		{name: "application", provider: "github", repository: testRepository, ref: testRef, otherApp: true},
		{name: "provider", provider: "local", repository: testRepository, ref: testRef},
		{name: "repository", provider: "github", repository: testRepository + 1, ref: testRef},
		{name: "ref", provider: "github", repository: testRepository, ref: "refs/heads/other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			ctx := context.Background()
			status, lease, dispatch := prepareCoordinatorDispatch(t, fixture)
			jobID := insertCoordinatorJob(t, fixture, status, dispatch, "running", "")
			if err := fixture.repository.LinkDispatchJob(ctx, lease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=? WHERE id=?`, timestamp(fixture.now.Add(2*time.Second)), timestamp(fixture.now.Add(2*time.Second)), jobID); err != nil {
				t.Fatal(err)
			}
			appID := testApp
			if test.otherApp {
				appID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
				insertGitHubApplication(t, fixture, appID, "Other")
				if _, err := fixture.db.Exec(`DROP TRIGGER deployment_linkage_valid_insert`); err != nil {
					t.Fatal(err)
				}
			}
			releaseID := insertReleaseWithProvenance(t, fixture, appID, test.provider, test.repository, test.ref, secondSHA, dispatch.Sequence)
			insertSucceededDeployment(t, fixture, appID, releaseID, jobID)
			completed, err := fixture.repository.RefreshActiveJob(ctx, lease, fixture.now.Add(3*time.Second))
			if err != nil || completed.State != StatePaused || completed.PauseCode != PauseInvalidSource || completed.ActiveJobID != "" || completed.LastSuccessfulDeployedSHA != "" {
				t.Fatalf("mismatched %s completion=%#v err=%v", test.name, completed, err)
			}
		})
	}
}

func TestRefreshActiveJobPreservesSourceAccessLostOverlay(t *testing.T) {
	for _, jobStatus := range []string{"running", "waiting_user", "succeeded", "failed"} {
		t.Run(jobStatus, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			ctx := context.Background()
			status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			_, firstLease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = reserveAndFinalize(ctx, fixture.repository, firstLease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
				t.Fatal(err)
			}
			dispatch, err := fixture.repository.PrepareDispatch(ctx, firstLease, fixture.now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			jobID := insertCoordinatorJob(t, fixture, status, dispatch, "queued", "")
			if err = fixture.repository.LinkDispatchJob(ctx, firstLease, dispatch.Sequence, dispatch.Generation, jobID, fixture.now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}

			removedAt := fixture.now.Add(3 * time.Second)
			change := protocol.AccessChange{
				Envelope:       protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), removedAt),
				EventID:        uuid.NewString(),
				InstallationID: testInstallation,
				RepositoryID:   testRepository,
				ChangeCode:     "repository.removed",
				ObservedAt:     removedAt,
				AckRequired:    true,
			}
			decision, err := controllerrelay.NewRepository(fixture.db).CommitAccessChange(ctx, testController, change, removedAt)
			if err != nil || decision.Kind != controllerrelay.DecisionAck {
				t.Fatalf("access removal decision=%#v err=%v", decision, err)
			}

			updateAt := fixture.now.Add(4 * time.Second)
			switch jobStatus {
			case "waiting_user":
				_, err = fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition='approval_required',updated_at=? WHERE id=?`, timestamp(updateAt), jobID)
			case "succeeded":
				releaseID := insertActualRelease(t, fixture, testSHA, dispatch.Sequence)
				insertSucceededDeployment(t, fixture, testApp, releaseID, jobID)
				_, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',updated_at=?,finished_at=? WHERE id=?`, timestamp(updateAt), timestamp(updateAt), jobID)
			case "failed":
				_, err = fixture.db.Exec(`UPDATE jobs SET status='failed',phase='failed',error_code='provider_unavailable',updated_at=?,finished_at=? WHERE id=?`, timestamp(updateAt), timestamp(updateAt), jobID)
			default:
				_, err = fixture.db.Exec(`UPDATE jobs SET status='running',phase='deploying',updated_at=? WHERE id=?`, timestamp(updateAt), jobID)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), updateAt, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			refreshed, err := fixture.repository.RefreshActiveJob(ctx, lease, updateAt)
			if err != nil {
				t.Fatal(err)
			}
			if refreshed.State != StatePaused || refreshed.PauseCode != PauseSourceAccessLost || refreshed.PausedSHA != testSHA {
				t.Fatalf("overlay lost status=%#v", refreshed)
			}
			terminal := jobStatus == "succeeded" || jobStatus == "failed"
			if terminal && refreshed.ActiveJobID != "" {
				t.Fatalf("terminal overlay retained active job=%#v", refreshed)
			}
			if !terminal && refreshed.ActiveJobID != jobID {
				t.Fatalf("nonterminal overlay lost active job=%#v", refreshed)
			}
			if jobStatus == "succeeded" && refreshed.LastSuccessfulDeployedSHA != testSHA {
				t.Fatalf("successful overlay lost provenance=%#v", refreshed)
			}
		})
	}
}

func commitDesired(t *testing.T, fixture *repositoryFixture, status Status, generation uint64, sha string, at time.Time) {
	t.Helper()
	source := protocol.SourceDesired{
		Envelope:       protocol.NewEnvelope(protocol.TypeSourceDesired, uuid.NewString(), at),
		DeliveryID:     uuid.NewString(),
		SubscriptionID: status.SubscriptionID,
		Generation:     generation,
		InstallationID: testInstallation,
		RepositoryID:   testRepository,
		Ref:            testRef,
		ObservedSHA:    sha,
		ObservedAt:     at,
	}
	decision, err := controllerrelay.NewRepository(fixture.db).CommitSourceDesired(context.Background(), testController, source, at)
	if err != nil || decision.Kind != controllerrelay.DecisionAck {
		t.Fatalf("commit desired generation %d decision=%#v err=%v", generation, decision, err)
	}
}

func setSourceConnectionStatus(t *testing.T, fixture *repositoryFixture, status string, at time.Time) {
	t.Helper()
	result, err := fixture.db.Exec(`UPDATE source_connections SET status=?,updated_at=? WHERE id=?`, status, timestamp(at), testConnection)
	if err != nil {
		t.Fatal(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		t.Fatalf("source connection status rows=%d err=%v", changed, rowsErr)
	}
	var persisted string
	if err = fixture.db.QueryRow(`SELECT status FROM source_connections WHERE id=?`, testConnection).Scan(&persisted); err != nil || persisted != status {
		t.Fatalf("source connection status=%q want=%q err=%v", persisted, status, err)
	}
}

func insertCoordinatorJob(t *testing.T, fixture *repositoryFixture, status Status, dispatch PreparedDispatch, jobStatus, errorCode string) string {
	t.Helper()
	jobID := uuid.NewString()
	stamp := timestamp(fixture.now)
	input := `{"releaseId":"","configurationMode":"current"}`
	pauseDisposition := any(nil)
	phase := jobStatus
	if jobStatus == "waiting_user" {
		pauseDisposition = PauseApprovalRequired
		phase = PauseApprovalRequired
	}
	if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,idempotency_key,requested_by,input_json,pause_disposition,error_code,created_at,updated_at) VALUES(?,'deploy','application',?,?,?,?,?,?,?, ?,?,?)`, jobID, status.ApplicationID, jobStatus, phase, DispatchIdempotencyKey(status.Revision, dispatch.Sequence), status.SourceOwnerUserID, input, pauseDisposition, nullString(errorCode), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func insertActualRelease(t *testing.T, fixture *repositoryFixture, sha string, sequence uint64) string {
	return insertReleaseWithProvenance(t, fixture, testApp, "github", testRepository, testRef, sha, sequence)
}

func insertReleaseWithProvenance(t *testing.T, fixture *repositoryFixture, applicationID, provider string, repositoryID int64, ref, sha string, sequence uint64) string {
	t.Helper()
	releaseID := uuid.NewString()
	stamp := timestamp(fixture.now)
	if _, err := fixture.db.Exec(`INSERT INTO releases(id,app_id,source_commit_sha,source_branch,status,metadata_json,created_at,source_provider,repository_id,repository_owner,repository_name,tracked_ref,resolved_sha,compose_path,archive_sha256,workspace_path,workspace_state,materialized_at) VALUES(?,?,?,'main','ready','{}',?,?,?,'octo','app',?,?,'compose.yaml',?,?,'ready',?)`, releaseID, applicationID, sha, stamp, provider, repositoryID, ref, sha, fmt.Sprintf("%064x", sequence), "releases/"+releaseID, stamp); err != nil {
		t.Fatal(err)
	}
	return releaseID
}

func insertSucceededDeployment(t *testing.T, fixture *repositoryFixture, applicationID, releaseID, jobID string) {
	t.Helper()
	if _, err := fixture.db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,started_at,finished_at) VALUES(?,?,?,?, 'succeeded','current',1,?,?)`, uuid.NewString(), applicationID, releaseID, jobID, timestamp(fixture.now), timestamp(fixture.now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
}

func prepareCoordinatorDispatch(t *testing.T, fixture *repositoryFixture) (Status, WorkLease, PreparedDispatch) {
	t.Helper()
	ctx := context.Background()
	status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	dispatch, err := fixture.repository.PrepareDispatch(ctx, lease, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	return status, lease, dispatch
}

func reserveAndFinalize(ctx context.Context, repository *Repository, lease WorkLease, generation uint64, sha string, nextReconcileAt, at time.Time) error {
	if err := repository.ReserveResolve(ctx, lease, generation, at); err != nil {
		return err
	}
	return repository.FinalizeResolvedHead(ctx, lease, generation, sha, nextReconcileAt, at)
}

func insertGitHubApplication(t *testing.T, fixture *repositoryFixture, applicationID, name string) {
	insertGitHubApplicationWithBranch(t, fixture, applicationID, name, "main")
}

func insertGitHubApplicationWithBranch(t *testing.T, fixture *repositoryFixture, applicationID, name, branch string) {
	t.Helper()
	stamp := timestamp(fixture.now)
	if _, err := fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, applicationID, applicationID, name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?,'github',?,?,?,'octo','app',?,?,'compose.yaml',?,?,?)`, applicationID, testConnection, testInstallation, testRepository, branch, "refs/heads/"+branch, testSHA, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func seedRetiredACKHeads(t *testing.T, fixture *repositoryFixture, count int) {
	t.Helper()
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	subscriptionStatement, err := tx.Prepare(`INSERT INTO relay_controller_subscriptions(subscription_id,owner_user_id,binding_id,controller_id,installation_id,repository_id,tracked_ref,state,created_at,retired_at) VALUES(?,?,?,?,?,?,?,'retired',?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriptionStatement.Close()
	ackStatement, err := tx.Prepare(`INSERT INTO relay_source_ack_heads(controller_id,subscription_id,delivery_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at,received_at) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer ackStatement.Close()
	stamp := timestamp(fixture.now.Add(-time.Hour))
	for item := 1; item <= count; item++ {
		subscriptionID := fmt.Sprintf("%08x-1000-4000-8000-%012x", item, item)
		deliveryID := fmt.Sprintf("%08x-2000-4000-8000-%012x", item, item)
		ref := fmt.Sprintf("refs/heads/retired-%d", item)
		if _, err = subscriptionStatement.Exec(subscriptionID, testOwner, testBinding, testController, testInstallation, testRepository, ref, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err = ackStatement.Exec(testController, subscriptionID, deliveryID, 1, testInstallation, testRepository, ref, testSHA, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
