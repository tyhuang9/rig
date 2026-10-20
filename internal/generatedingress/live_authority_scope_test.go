package generatedingress

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
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
	authority := liveGeneratedDeployments(t, directories.WorkingDirectory)
	_ = liveGeneratedDeployments(t, directories.WorkingDirectory)
	scoped := deploymenteffects.WithScope(ctx, scope)
	spec := liveCandidateSpec("blue", uuid.NewString(), uuid.NewString())
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
