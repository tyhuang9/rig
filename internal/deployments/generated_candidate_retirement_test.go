package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCandidateReceiptClosesLegacyRemovalForNameOnlyAndReconstructedIdentity(t *testing.T) {
	f := newCandidateValidationFixture(t)
	r := f.migration.repository
	appID, deploymentID := f.pins.claim.ApplicationID(), f.pins.deploymentID
	id := strings.Repeat("a", 64)
	if err := r.CheckLegacyGeneratedCandidateRemoval(context.Background(), appID, deploymentID, "api", "rig-candidate-api", id); err != nil {
		t.Fatalf("legacy target before receipt: %v", err)
	}
	receipt := f.beginReceipt(t)
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
	tx, err := r.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := f.state.RecordCandidateCreatedTx(context.Background(), tx, appID, deploymentID, "api", receipt, "rig-candidate-api", id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckLegacyGeneratedCandidateRemoval(context.Background(), uuid.NewString(), uuid.NewString(), "other", "other-name", id); !errors.Is(err, ErrGeneratedCandidateRetirementUnavailable) {
		t.Fatalf("reconstructed container ID bypassed receipt: %v", err)
	}
	current, err := f.state.Get(context.Background(), appID, deploymentID)
	if err != nil || len(current.Components) != 1 || current.Components[0].ContainerID != id || string(current.Components[0].State) != "running" {
		t.Fatalf("read-only barrier changed candidate state: %#v, %v", current, err)
	}
}
