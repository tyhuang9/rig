package generatedingress

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes(t *testing.T) {
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	scope, release, err := deploymenteffects.AcquireScoped(ctx, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// Construction must not acquire another lease. A second fixture for the
	// same controller directory used to deadlock here before any Docker work.
	newAuthority := func() *liveGeneratedAuthority {
		root := t.TempDir()
		db, openErr := database.Open(root)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() { _ = db.Close() })
		return liveCandidateFixtureAuthority(t, db, root, directories.WorkingDirectory)
	}
	authority := newAuthority()
	_ = newAuthority()
	scoped := deploymenteffects.WithScope(ctx, scope)
	spec := liveCandidateSpec("blue", uuid.NewString(), uuid.NewString())
	spec = authority.PrepareCandidate(t, ctx, spec)
	scoped, bound := authority.bindCandidate(t, scoped, spec)
	execution, err := authority.repository.PinGeneratedNetwork(scoped, bound.Network)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckBeforeCreate(); err != nil {
		execution.Leave()
		t.Fatal(err)
	}
	execution.Leave()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if leave, err := deploymenteffects.EnterScope(scoped, directories.WorkingDirectory); err == nil {
		leave()
		t.Fatal("released fixture scope remained usable")
	}
	next, releaseNext, err := deploymenteffects.AcquireScoped(ctx, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseNext()
	nextContext, replay := authority.bindCandidate(t, deploymenteffects.WithScope(ctx, next), bound)
	if replay.Network.Claim != bound.Network.Claim {
		t.Fatal("scope reacquisition changed the original deployment claim")
	}
	replayed, err := authority.repository.PinGeneratedNetwork(nextContext, replay.Network)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Leave()
	if err := replayed.CheckObservation(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveGeneratedAuthorityUsesCallerDatabaseAcrossSequentialBindings(t *testing.T) {
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority := liveCandidateFixtureAuthority(t, db, root, directories.WorkingDirectory)
	ctx := context.Background()

	var bound []liveRuntimeCandidateSpec
	for index, version := range []string{"blue", "green"} {
		original := liveCandidateSpec(version, uuid.NewString(), uuid.NewString())
		prepared := authority.PrepareCandidate(t, ctx, original)
		if prepared.DeploymentID != "" || prepared.DeploymentPlanRevisionID == original.DeploymentPlanRevisionID ||
			prepared.ConfigurationRevisionID == "" {
			t.Fatalf("preparation %d retained obsolete identity or omitted protected revisions", index+1)
		}
		scope, release, err := deploymenteffects.AcquireScoped(ctx, directories.WorkingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		boundSpec := authority.BindNetwork(t, deploymenteffects.WithScope(ctx, scope), prepared)
		authority.CancelNetwork(t, deploymenteffects.WithScope(ctx, scope), boundSpec.DeploymentID)
		if err := release(); err != nil {
			t.Fatal(err)
		}
		bound = append(bound, boundSpec)
	}
	if bound[0].DeploymentID == "" || bound[0].DeploymentID == bound[1].DeploymentID ||
		bound[0].DeploymentPlanRevisionID == bound[1].DeploymentPlanRevisionID ||
		bound[0].ConfigurationRevisionID == bound[1].ConfigurationRevisionID {
		t.Fatal("sequential bindings did not retain distinct genuine database identities")
	}
	var jobs, deployments, runtimeDeployments int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='deploy'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM deployments`).Scan(&deployments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_deployments`).Scan(&runtimeDeployments); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || deployments != 2 || runtimeDeployments != 2 {
		t.Fatalf("same-database binding history changed: jobs=%d deployments=%d runtime=%d", jobs, deployments, runtimeDeployments)
	}

	var before, after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	candidate := liveRuntimeCandidate{AppID: bound[0].AppID, DeploymentID: bound[0].DeploymentID}
	if _, err := authority.publishedHistory(ctx, db, candidate); err == nil {
		t.Fatal("missing original session unexpectedly validated")
	}
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("published-history refusal wrote database state: before=%d after=%d", before, after)
	}

	foreign, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	if _, err := authority.publishedHistory(ctx, foreign, candidate); err == nil {
		t.Fatal("foreign database unexpectedly validated")
	}
}

func liveCandidateFixtureAuthority(t *testing.T, db *sql.DB, dataRoot, workingDirectory string) *liveGeneratedAuthority {
	t.Helper()
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	authority := newLiveRuntimeAuthority(t, db, workingDirectory, plans, configuration)
	return &liveGeneratedAuthority{LiveRuntimeTestAuthority: authority, repository: authority.Repository(),
		db: db, workingDirectory: workingDirectory, sessions: make(map[string]liveRuntimeSession)}
}
