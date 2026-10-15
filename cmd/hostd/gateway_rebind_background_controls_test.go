package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/controllerrelay"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	backgroundControlOwner      = "background-control-owner"
	backgroundControlConnection = "background-control-connection"
	backgroundControlApp        = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaab"
	backgroundControlController = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbc"
	backgroundControlBinding    = "cccccccc-cccc-4ccc-8ccc-cccccccccccd"
)

type backgroundControlTimer struct{ *time.Timer }

func (timer backgroundControlTimer) C() <-chan time.Time { return timer.Timer.C }

type backgroundControlClock struct{ now time.Time }

func (clock backgroundControlClock) Now() time.Time { return clock.now }
func (backgroundControlClock) NewTimer(delay time.Duration) autodeploy.Timer {
	return backgroundControlTimer{Timer: time.NewTimer(delay)}
}

type backgroundControlRepository struct {
	*autodeploy.Repository
	cancel        context.CancelFunc
	recoveryCalls int
	recovered     int
	recoveryErr   error
}

func (repository *backgroundControlRepository) RecoverExpiredLeases(ctx context.Context, now time.Time, limit int) (int, error) {
	repository.recoveryCalls++
	count, err := repository.Repository.RecoverExpiredLeases(ctx, now, limit)
	repository.recovered, repository.recoveryErr = count, err
	repository.cancel()
	return count, err
}

type backgroundControlResolver struct{}

func (backgroundControlResolver) ResolveHead(context.Context, autodeploy.SourceScope) (string, error) {
	return "", errors.New("unexpected background control source resolution")
}

type backgroundControlJobs struct{}

func (backgroundControlJobs) CreateWithInputFinalized(jobs.CreateRequest, jobs.CreateFinalizer) (jobs.Job, bool, error) {
	return jobs.Job{}, false, errors.New("unexpected background control job creation")
}

// TestGatewayRebindBackgroundSentinelsUseRealRecoveryMethods drives the actual
// coordinator run loop and repository epoch mutation. It intentionally does
// not stand in for Supervisor.Run: the live process scenario owns the exact
// keyless needs-attention terminal-state assertion.
func TestGatewayRebindBackgroundSentinelsUseRealRecoveryMethods(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	seedAt := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	stamp := seedAt.Format(time.RFC3339Nano)
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := db.Exec(query, args...); execErr != nil {
			t.Fatal(execErr)
		}
	}
	mustExec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?,'administrator',?,?)`, backgroundControlOwner, "background-control", "hash", stamp, stamp)
	mustExec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at)
		VALUES(?,?,'github','connected','42','fixture',1,?,?,?,?,?)`, backgroundControlConnection, backgroundControlOwner, stamp, stamp, stamp, stamp, stamp)
	mustExec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, backgroundControlApp, "background-control", "Background recovery control", stamp, stamp)
	mustExec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at)
		VALUES(?,'github',?,?,?,'fixture','hosting-notes','main','refs/heads/main','compose.yaml','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?)`, backgroundControlApp, backgroundControlConnection, int64(101), int64(202), stamp, stamp)
	mustExec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, backgroundControlController, stamp, stamp)
	mustExec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, backgroundControlBinding, backgroundControlOwner, backgroundControlConnection, backgroundControlController, int64(101), int64(202), stamp, stamp, stamp)
	mustExec(`INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,key_id,last_error_code,attempt,next_attempt_at,last_ready_at,last_seen_at,state_changed_at,updated_at)
		VALUES(?,?,?,'disconnected',NULL,NULL,0,NULL,NULL,NULL,?,?)`, backgroundControlController, int64(7), int64(1), stamp, stamp)

	repository := autodeploy.NewRepository(db)
	status, err := repository.Configure(context.Background(), autodeploy.ConfigureRequest{
		ApplicationID: backgroundControlApp, ActorUserID: backgroundControlOwner, ExpectedRevision: 0, Enabled: true,
	}, seedAt)
	if err != nil || !status.Enabled {
		t.Fatalf("seed real auto-deploy configuration status=%#v error=%v", status, err)
	}
	_, lease, err := repository.ClaimDue(context.Background(), uuid.NewString(), seedAt, time.Second)
	if err != nil || lease.ApplicationID != backgroundControlApp {
		t.Fatalf("seed real expired lease lease=%#v error=%v", lease, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clock := backgroundControlClock{now: seedAt.Add(2 * time.Second)}
	coordinatorConfig := autodeploy.DefaultCoordinatorConfig()
	coordinatorConfig.Clock = clock
	controlRepository := &backgroundControlRepository{Repository: repository, cancel: cancel}
	coordinator, err := autodeploy.NewCoordinator(controlRepository, backgroundControlResolver{}, backgroundControlJobs{}, coordinatorConfig)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if runErr := coordinator.Run(ctx); runErr != nil {
		t.Fatal(runErr)
	}
	if !errors.Is(ctx.Err(), context.Canceled) || controlRepository.recoveryCalls != 1 ||
		controlRepository.recoveryErr != nil || controlRepository.recovered != 1 {
		t.Fatalf("coordinator recovery was not observed: context=%v calls=%d recovered=%d error=%v",
			ctx.Err(), controlRepository.recoveryCalls, controlRepository.recovered, controlRepository.recoveryErr)
	}

	var token, expiry sql.NullString
	if err = db.QueryRow(`SELECT lease_token,lease_expires_at FROM github_auto_deploy_heads WHERE application_id=?`, backgroundControlApp).Scan(&token, &expiry); err != nil || token.Valid || expiry.Valid {
		t.Fatalf("coordinator recovery lease token=%#v expiry=%#v error=%v", token, expiry, err)
	}

	// This is deliberately a repository-only control for BeginSessionEpoch;
	// liveHostdRebindRunRecoveryBackgroundIsolation verifies the real
	// Supervisor.Run transition from this disconnected row to needs_attention.
	relay, err := controllerrelay.NewRepository(db).BeginSessionEpoch(context.Background(), clock.Now())
	if err != nil || relay.ControllerID != backgroundControlController || relay.Epoch != 8 || relay.Fence != 1 || relay.State != controllerrelay.SessionDisconnected {
		t.Fatalf("real relay epoch recovery status=%#v error=%v", relay, err)
	}
	var activeKeys int
	if err = db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE controller_id=? AND state IN ('active','pending')`, backgroundControlController).Scan(&activeKeys); err != nil || activeKeys != 0 {
		t.Fatalf("background relay key inventory=%d error=%v", activeKeys, err)
	}
	credentials, err := controllerrelay.NewFileCredentialStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	page, err := credentials.ControllerKeyCredentials("", 1000)
	if err != nil || !page.Complete || len(page.Credentials) != 0 || len(page.Issues) != 0 {
		t.Fatalf("background relay credentials complete=%t credentials=%d issues=%d error=%v", page.Complete, len(page.Credentials), len(page.Issues), err)
	}
}
