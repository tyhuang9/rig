package deploymentexecution

import (
	"database/sql"
	"errors"

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
	persisted, err := generatedruntimestate.New(guard.db).ReadRouteActivationReceiptTx(ctx, tx, receipt.ReceiptID)
	if errors.Is(err, generatedruntimestate.ErrNotFound) || errors.Is(err, generatedruntimestate.ErrInvalidState) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if persisted != receipt {
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
		receipt.CreatorWorkerAttempt > 0 && receipt.WriterJobID == claim.JobID() &&
		receipt.CreatorWorkerAttempt <= receipt.WriterWorkerAttempt &&
		receipt.WriterWorkerAttempt > 0 && receipt.WriterWorkerAttempt <= claim.Attempt() &&
		receipt.SuccessorGeneration > 0 && receipt.PredecessorGeneration == receipt.SuccessorGeneration-1 &&
		receipt.CandidateRosterDigest != "" && receipt.PredecessorRosterDigest != "" &&
		receipt.RouteIntentDigest != "" && receipt.CommittedRouteProofDigest != "" && !receipt.ActivatedAt.IsZero()
}
