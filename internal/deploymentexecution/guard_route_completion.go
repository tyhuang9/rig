package deploymentexecution

import (
	"database/sql"
	"errors"

	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// CheckGeneratedRouteCompletionReplayTx revalidates a live Claim against one
// exact terminal route completion. It is deliberately read-only and cannot
// authorize retirement or a fresh completion mutation.
func (p *Pinned) CheckGeneratedRouteCompletionReplayTx(tx *sql.Tx, claim Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	completion generatedruntimestate.RouteCompletionReceipt,
) error {
	if !validCompletionReplayIdentity(activation, completion, claim) {
		return ErrInvalidClaim
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
	classification, err := guard.bindings.CheckTx(ctx, tx, claim.binding)
	if err != nil {
		return bindingError(ctx, err)
	}
	if classification != deploymentbinding.ClassificationCurrent {
		if classification == deploymentbinding.ClassificationStale {
			return ErrAuthorityStale
		}
		return ErrInvalidConfiguration
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}

	persisted, found, err := generatedruntimestate.New(guard.db).ReadRouteCompletionReceiptTx(
		ctx, tx, activation.ReceiptID)
	if err != nil || !found || persisted != completion {
		return errors.Join(ErrIdentityLost, err)
	}
	receipt, err := generatedruntimestate.New(guard.db).ReadRouteActivationReceiptTx(ctx, tx, activation.ReceiptID)
	if errors.Is(err, generatedruntimestate.ErrNotFound) || errors.Is(err, generatedruntimestate.ErrInvalidState) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if receipt != activation {
		return errors.Join(ErrIdentityLost, err)
	}

	var headDeployment, headRelease, headSlot, headReceipt string
	var headGeneration int64
	var runtimePhase generatedruntimestate.Phase
	var deploymentStatus, deploymentJobID, jobType, resourceType, resourceID, jobStatus, jobPhase string
	var provenanceInitialized bool
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT head.deployment_id,head.release_id,head.slot,
		head.generation,head.activation_receipt_id,runtime.phase,deployment.status,
		deployment.job_id,deployment.provenance_initialized,job.type,job.resource_type,job.resource_id,
		job.status,job.phase,job.attempt
		FROM generated_runtime_active_heads head
		JOIN generated_runtime_deployments runtime
		  ON runtime.deployment_id=head.deployment_id AND runtime.app_id=head.app_id
		 AND runtime.release_id=head.release_id AND runtime.candidate_slot=head.slot
		JOIN deployments deployment
		  ON deployment.id=runtime.deployment_id AND deployment.app_id=runtime.app_id
		JOIN jobs job ON job.id=deployment.job_id
		WHERE head.app_id=?`, activation.AppID).Scan(&headDeployment, &headRelease, &headSlot,
		&headGeneration, &headReceipt, &runtimePhase, &deploymentStatus, &deploymentJobID, &provenanceInitialized,
		&jobType, &resourceType, &resourceID, &jobStatus, &jobPhase, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if headDeployment != activation.SuccessorDeploymentID || headRelease != activation.SuccessorReleaseID ||
		headSlot != activation.SuccessorSlot || headGeneration != activation.SuccessorGeneration ||
		headReceipt != activation.ReceiptID || runtimePhase != generatedruntimestate.PhaseSucceeded ||
		deploymentStatus != "succeeded" || deploymentJobID != claim.JobID() ||
		deploymentJobID != completion.CurrentJobID || !provenanceInitialized || jobType != "deploy" ||
		resourceType != "application" || resourceID != activation.AppID || jobStatus != "succeeded" ||
		jobPhase != "succeeded" || attempt != claim.attempt {
		return ErrIdentityLost
	}
	pending, err := guard.positiveFence.PendingStartExistsTx(ctx, tx)
	if err != nil {
		return validatorError(ctx, err)
	}
	if pending {
		return ErrPositiveFence
	}
	return p.checkCurrent(ctx)
}

func validCompletionReplayIdentity(activation generatedruntimestate.RouteActivationReceipt,
	completion generatedruntimestate.RouteCompletionReceipt, claim Claim,
) bool {
	return validRetirementReceiptIdentity(activation, claim) &&
		completion.ActivationReceiptID == activation.ReceiptID && completion.AppID == activation.AppID &&
		completion.SuccessorDeploymentID == activation.SuccessorDeploymentID &&
		completion.CreatorJobID == activation.CreatorJobID &&
		completion.CreatorWorkerAttempt == activation.CreatorWorkerAttempt &&
		completion.CurrentJobID == claim.JobID() && completion.CurrentWorkerAttempt == claim.Attempt() &&
		completion.CompletionCode == "deployment_completed" && !completion.CompletedAt.IsZero()
}
