package deploymentexecution

import (
	"database/sql"
	"errors"
	"time"

	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// CheckGeneratedRouteRetirementTx revalidates the original live Claim and the
// exact internally committed activation receipt at a predecessor effect
// boundary. The complete binding check includes the completed authorizing
// lifecycle operation and rejects any unresolved application history.
func (p *Pinned) CheckGeneratedRouteRetirementTx(tx *sql.Tx, claim Claim,
	receipt generatedruntimestate.RouteActivationReceipt,
) error {
	if !validRetirementReceiptIdentity(receipt, claim) {
		return ErrInvalidClaim
	}
	// This purpose-specific status gate preserves the ordinary deployment
	// authority checks, including current worker attempt and noncancellation.
	if err := p.checkTx(tx, claim, receipt.SuccessorDeploymentID, generatedRouteActivationStatus); err != nil {
		return err
	}
	guard, ctx, done, err := p.beginCheck()
	if err != nil {
		return err
	}
	defer done()
	if tx == nil || guard == nil || !guard.UsesDatabase(guard.db) {
		return ErrInvalidConfiguration
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}
	var persisted generatedruntimestate.RouteActivationReceipt
	var activatedAt string
	err = tx.QueryRowContext(ctx, `SELECT id,app_id,successor_deployment_id,successor_release_id,
		successor_slot,successor_generation,predecessor_deployment_id,predecessor_release_id,
		predecessor_slot,predecessor_generation,predecessor_activation_receipt_id,creator_job_id,
		creator_worker_attempt,candidate_roster_digest,predecessor_roster_digest,route_intent_digest,
		committed_route_proof_digest,activated_at FROM generated_route_activations WHERE id=?`,
		receipt.ReceiptID).Scan(&persisted.ReceiptID, &persisted.AppID, &persisted.SuccessorDeploymentID,
		&persisted.SuccessorReleaseID, &persisted.SuccessorSlot, &persisted.SuccessorGeneration,
		&persisted.PredecessorDeploymentID, &persisted.PredecessorReleaseID, &persisted.PredecessorSlot,
		&persisted.PredecessorGeneration, &persisted.PredecessorActivationReceiptID, &persisted.CreatorJobID,
		&persisted.CreatorWorkerAttempt, &persisted.CandidateRosterDigest, &persisted.PredecessorRosterDigest,
		&persisted.RouteIntentDigest, &persisted.CommittedRouteProofDigest, &activatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	persisted.ActivatedAt, err = time.Parse(time.RFC3339Nano, activatedAt)
	if err != nil || persisted != receipt {
		return ErrIdentityLost
	}
	var phase generatedruntimestate.Phase
	err = tx.QueryRowContext(ctx, `SELECT phase FROM generated_runtime_deployments
		WHERE deployment_id=? AND app_id=?`, receipt.SuccessorDeploymentID, receipt.AppID).Scan(&phase)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if phase != generatedruntimestate.PhaseDraining {
		return ErrStageInvalid
	}
	var headDeployment, headRelease, headSlot, headReceipt string
	var headGeneration int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(deployment_id,''),COALESCE(release_id,''),
		COALESCE(slot,''),generation,COALESCE(activation_receipt_id,'')
		FROM generated_runtime_active_heads WHERE app_id=?`, receipt.AppID).
		Scan(&headDeployment, &headRelease, &headSlot, &headGeneration, &headReceipt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if headDeployment != receipt.SuccessorDeploymentID || headRelease != receipt.SuccessorReleaseID ||
		headSlot != receipt.SuccessorSlot || headGeneration != receipt.SuccessorGeneration ||
		headReceipt != receipt.ReceiptID {
		return ErrAuthorityStale
	}
	return p.checkCurrent(ctx)
}

func validRetirementReceiptIdentity(receipt generatedruntimestate.RouteActivationReceipt, claim Claim) bool {
	return claim.valid() && receipt.ReceiptID != "" && receipt.AppID == claim.ApplicationID() &&
		receipt.SuccessorDeploymentID != "" && receipt.CreatorJobID == claim.JobID() &&
		receipt.CreatorWorkerAttempt > 0 && receipt.CreatorWorkerAttempt <= claim.Attempt() &&
		receipt.SuccessorGeneration > 0 && receipt.PredecessorGeneration == receipt.SuccessorGeneration-1 &&
		receipt.CandidateRosterDigest != "" && receipt.PredecessorRosterDigest != "" &&
		receipt.RouteIntentDigest != "" && receipt.CommittedRouteProofDigest != "" && !receipt.ActivatedAt.IsZero()
}
