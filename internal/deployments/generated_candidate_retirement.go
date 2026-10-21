package deployments

import (
	"context"
	"database/sql"

	"github.com/hostd/hostd/internal/deploymentexecution"
)

// CheckLegacyGeneratedCandidateRemoval closes the old public-value removal
// path for every receipt-owned identity, including a name-only create intent.
// A successful check describes only a legacy target; it grants no authority
// over a receipt-owned candidate and never changes capacity or durable state.
func (r *Repository) CheckLegacyGeneratedCandidateRemoval(ctx context.Context, appID, deploymentID, component, containerName, containerID string) error {
	if r == nil || r.db == nil || ctx == nil || !candidatePolicyUUID(appID) || !candidatePolicyUUID(deploymentID) ||
		!candidatePolicyText(component, 256) || containerName == "" || containerID == "" {
		return deploymentexecution.ErrInvalidConfiguration
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	var owned bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM generated_candidate_intents i
		JOIN generated_runtime_components c
		  ON c.deployment_id=i.deployment_id AND c.component_name=i.component_name
		JOIN generated_runtime_deployments rd ON rd.deployment_id=i.deployment_id
		WHERE (rd.app_id=? AND i.deployment_id=? AND i.component_name=?)
		   OR c.container_name=? OR c.container_id=?
	)`, appID, deploymentID, component, containerName, containerID).Scan(&owned)
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	if owned {
		return ErrGeneratedCandidateRetirementUnavailable
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(ctx, err)
	}
	return nil
}
