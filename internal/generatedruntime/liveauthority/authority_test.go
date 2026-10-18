//go:build live_docker

package liveauthority

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestAuthorityBindsConcreteScopedNetworkExecution(t *testing.T) {
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
	authority := New(t, db, directories.WorkingDirectory)
	first := generatedruntime.CandidateSpec{
		AppID: uuid.NewString(), ReleaseID: uuid.NewString(), DeploymentID: uuid.NewString(), ArtifactID: uuid.NewString(),
		DeploymentPlanRevisionID: uuid.NewString(), ComponentName: "web", Role: generatedruntime.RoleServer,
		ImageContentID: "sha256:" + strings.Repeat("6", 64), BuildDefinitionDigest: strings.Repeat("7", 64),
	}
	boundContext, first := authority.BindCandidate(t, context.Background(), first)
	execution, err := authority.Repository().PinGeneratedNetwork(boundContext, first.Network)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckBeforeCreate(); err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckObservation(); err != nil {
		t.Fatal(err)
	}
	execution.Leave()
	authority.FinishCandidate(t, first.DeploymentID)

	second := first
	second.DeploymentID, second.ArtifactID = uuid.NewString(), uuid.NewString()
	second.ActiveSlot = generatedruntime.SlotBlue
	_, second = authority.BindCandidate(t, context.Background(), second)
	if second.Network.Claim.JobID() == first.Network.Claim.JobID() || second.Network.DeploymentID != second.DeploymentID {
		t.Fatal("replacement did not receive its own exact immutable deployment claim")
	}
	authority.RejectCandidate(t, second.DeploymentID, first.DeploymentID)
}
