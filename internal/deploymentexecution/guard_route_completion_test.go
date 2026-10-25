package deploymentexecution

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestCheckGeneratedRouteCompletionReplayTxRefusesUnsealedAndCrossed(t *testing.T) {
	jobID, appID, deploymentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	claim := Claim{jobID: jobID, applicationID: appID, attempt: 3}
	activation := generatedruntimestate.RouteActivationReceipt{
		ReceiptID: uuid.NewString(), AppID: appID, SuccessorDeploymentID: deploymentID,
		SuccessorReleaseID: uuid.NewString(), SuccessorSlot: "blue", SuccessorGeneration: 2,
		PredecessorDeploymentID: uuid.NewString(), PredecessorReleaseID: uuid.NewString(),
		PredecessorSlot: "green", PredecessorGeneration: 1,
		PredecessorActivationReceiptID: uuid.NewString(), CreatorJobID: jobID,
		CreatorWorkerAttempt: 2, CandidateRosterDigest: "candidate", PredecessorRosterDigest: "predecessor",
		RouteIntentDigest: "intent", CommittedRouteProofDigest: "proof", ActivatedAt: time.Now().UTC(),
	}
	completion := generatedruntimestate.RouteCompletionReceipt{
		ActivationReceiptID: activation.ReceiptID, AppID: appID, SuccessorDeploymentID: deploymentID,
		CreatorJobID: jobID, CreatorWorkerAttempt: 2, CurrentJobID: jobID,
		CurrentWorkerAttempt: 3, CompletionCode: "deployment_completed", CompletedAt: time.Now().UTC(),
	}

	t.Run("unsealed scalar values", func(t *testing.T) {
		var pinned *Pinned
		if err := pinned.CheckGeneratedRouteCompletionReplayTx(nil, claim, activation, completion); !errors.Is(err, ErrInvalidClaim) {
			t.Fatalf("unsealed replay error=%v", err)
		}
	})
	t.Run("crossed current attempt", func(t *testing.T) {
		crossed := completion
		crossed.CurrentWorkerAttempt++
		if err := (&Pinned{}).CheckGeneratedRouteCompletionReplayTx(nil, claim, activation, crossed); !errors.Is(err, ErrInvalidClaim) {
			t.Fatalf("crossed replay error=%v", err)
		}
	})
	t.Run("crossed activation", func(t *testing.T) {
		crossed := completion
		crossed.ActivationReceiptID = uuid.NewString()
		if err := (&Pinned{}).CheckGeneratedRouteCompletionReplayTx(nil, claim, activation, crossed); !errors.Is(err, ErrInvalidClaim) {
			t.Fatalf("crossed activation error=%v", err)
		}
	})
}
