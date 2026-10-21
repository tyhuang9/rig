package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestCandidateReceiptClosesLegacyRemovalForNameOnlyAndReconstructedIdentity(t *testing.T) {
	f := newGeneratedMigrationFixture(t)
	r := f.repository
	state := generatedruntimestate.New(r.db)
	appID, deploymentID := f.claim.ApplicationID(), f.request.DeploymentID
	id := strings.Repeat("a", 64)
	if err := r.CheckLegacyGeneratedCandidateRemoval(context.Background(), appID, deploymentID, "api", "rig-candidate-api", id); err != nil {
		t.Fatalf("legacy target before receipt: %v", err)
	}
	if _, err := state.BeginMigration(context.Background(), appID, deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.FinishMigration(context.Background(), appID, deploymentID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Advance(context.Background(), appID, deploymentID,
		generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
		t.Fatal(err)
	}
	receipt := uuid.NewString()
	tx, err := r.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := state.BeginCandidateTx(context.Background(), tx, generatedruntimestate.CandidateIntentInput{
		ReceiptID: receipt, AppID: appID, DeploymentID: deploymentID, ComponentName: "api", ContainerName: "rig-candidate-api",
		CreatorJobID: f.claim.JobID(), CreatorWorkerAttempt: f.claim.Attempt(),
		ConfigurationRevisionID: f.request.ConfigurationRevisionID, ConfigurationRevisionNumber: f.request.ConfigurationRevisionNumber,
		ImageArtifactID: f.request.ArtifactID, ImageArtifactAttempt: f.request.ArtifactAttemptNumber,
		EnvironmentOperationID: f.request.ArtifactID, EnvironmentStagingAttempt: f.claim.Attempt(),
		CandidatePolicyVersion: "candidate-v1", CandidatePolicyDigest: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, deployment, component, containerName string
	}{
		{"exact name-only intent", deploymentID, "api", "rig-candidate-api"},
		{"receipt identity with changed name", deploymentID, "api", "other-name"},
		{"reconstructed name with changed deployment", uuid.NewString(), "other", "rig-candidate-api"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := r.CheckLegacyGeneratedCandidateRemoval(context.Background(), appID, test.deployment, test.component, test.containerName, id)
			if !errors.Is(err, ErrGeneratedCandidateRetirementUnavailable) {
				t.Fatalf("receipt-owned target was not refused: %v", err)
			}
		})
	}
	tx, err = r.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := state.RecordCandidateCreatedTx(context.Background(), tx, appID, deploymentID, "api", receipt, "rig-candidate-api", id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckLegacyGeneratedCandidateRemoval(context.Background(), uuid.NewString(), uuid.NewString(), "other", "other-name", id); !errors.Is(err, ErrGeneratedCandidateRetirementUnavailable) {
		t.Fatalf("reconstructed container ID bypassed receipt: %v", err)
	}
	current, err := state.Get(context.Background(), appID, deploymentID)
	if err != nil || len(current.Components) != 1 || current.Components[0].ContainerID != id || string(current.Components[0].State) != "running" {
		t.Fatalf("read-only barrier changed candidate state: %#v, %v", current, err)
	}
}
