package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type candidateObservedFailureBoundary struct {
	phase             Phase
	mainStatus        string
	mainDiagnostic    string
	mainSummary       string
	componentWhereSQL string
}

// RecordCandidateObservedFailureTx atomically fails a complete generated
// candidate roster in one accepted pre-route stage. The protected caller owns
// the physical absence proof and the guarded writer transaction.
func (r *Repository) RecordCandidateObservedFailureTx(
	ctx context.Context,
	tx *sql.Tx,
	appID, deploymentID string,
	diagnostic DiagnosticCode,
) error {
	if r == nil || r.db == nil || ctx == nil || tx == nil ||
		!canonicalUUID(appID) || !canonicalUUID(deploymentID) {
		return ErrInvalidTransition
	}
	boundary, ok := candidateFailureBoundary(diagnostic)
	if !ok {
		return ErrInvalidTransition
	}

	var phase Phase
	var status, strategy, acceptance string
	var provenance, expectedComponents, releaseValid, configurationValid, activeHead, lifecycleRetained int
	err := tx.QueryRowContext(ctx, `SELECT rd.phase,d.status,d.runtime_strategy,d.provenance_initialized,
		p.acceptance_status,p.component_count,
		EXISTS(SELECT 1 FROM releases rel
			WHERE rel.id=rd.release_id AND rel.app_id=rd.app_id
			  AND rel.deployment_plan_revision_id=rd.deployment_plan_revision_id
			  AND rel.deployment_plan_revision_number=rd.deployment_plan_revision_number),
		EXISTS(SELECT 1 FROM application_configuration_revisions cr
			WHERE cr.id=d.actual_configuration_revision_id AND cr.app_id=rd.app_id
			  AND cr.revision_number=d.actual_configuration_revision_number AND cr.bundle_version=2
			  AND cr.deployment_plan_revision_id=rd.deployment_plan_revision_id
			  AND cr.deployment_plan_revision_number=rd.deployment_plan_revision_number),
		EXISTS(SELECT 1 FROM generated_runtime_active_heads h
			WHERE h.app_id=rd.app_id AND h.deployment_id=rd.deployment_id),
		EXISTS(SELECT 1 FROM application_lifecycle_operations operation
			WHERE operation.app_id=rd.app_id AND operation.deployment_id=rd.deployment_id)
		FROM generated_runtime_deployments rd
		JOIN deployments d ON d.id=rd.deployment_id AND d.app_id=rd.app_id
		JOIN deployment_plan_revisions p
		  ON p.id=rd.deployment_plan_revision_id AND p.app_id=rd.app_id
		 AND p.revision_number=rd.deployment_plan_revision_number
		WHERE rd.deployment_id=? AND rd.app_id=?
		  AND d.release_id=rd.release_id AND d.runtime_strategy='generated_node'
		  AND d.deployment_plan_revision_id=rd.deployment_plan_revision_id
		  AND d.deployment_plan_revision_number=rd.deployment_plan_revision_number`, deploymentID, appID).Scan(
		&phase, &status, &strategy, &provenance, &acceptance, &expectedComponents,
		&releaseValid, &configurationValid, &activeHead, &lifecycleRetained,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if phase != boundary.phase || status != boundary.mainStatus || strategy != "generated_node" ||
		provenance != 1 || acceptance != "accepted" || expectedComponents < 1 || expectedComponents > 2 ||
		releaseValid != 1 || configurationValid != 1 || activeHead != 0 || lifecycleRetained != 0 {
		return ErrInvalidTransition
	}

	var total, valid int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN
		c.slot=rd.candidate_slot
		AND a.id=c.image_artifact_id AND a.release_id=rd.release_id
		AND a.deployment_plan_revision_id=rd.deployment_plan_revision_id
		AND a.deployment_plan_revision_number=rd.deployment_plan_revision_number
		AND a.component_id=c.component_name AND a.attempt_number>0 AND a.state='ready'
		AND (
			(?='start' AND c.state='image_ready' AND c.container_name IS NULL
				AND c.container_id IS NULL AND intent.id IS NULL)
			OR (
				((?='start' AND c.state IN ('starting','running'))
				 OR (?='health' AND c.state IN ('running','healthy')))
				AND ((c.state='starting' AND c.container_name IS NOT NULL AND c.container_id IS NULL)
				  OR (c.state IN ('running','healthy') AND c.container_name IS NOT NULL AND c.container_id IS NOT NULL))
				AND intent.id IS NOT NULL AND intent.creator_job_id=d.job_id
				AND intent.creator_worker_attempt>0 AND intent.creator_worker_attempt<=j.attempt
				AND intent.configuration_revision_id=d.actual_configuration_revision_id
				AND intent.configuration_revision_number=d.actual_configuration_revision_number
				AND cr.id=intent.configuration_revision_id AND cr.app_id=rd.app_id
				AND cr.revision_number=intent.configuration_revision_number AND cr.bundle_version=2
				AND cr.deployment_plan_revision_id=rd.deployment_plan_revision_id
				AND cr.deployment_plan_revision_number=rd.deployment_plan_revision_number
				AND intent.image_artifact_id=c.image_artifact_id
				AND intent.image_artifact_attempt=a.attempt_number
				AND intent.environment_operation_id=intent.image_artifact_id
				AND intent.environment_staging_attempt=intent.creator_worker_attempt
			)
		) THEN 1 ELSE 0 END),0)
		FROM generated_runtime_components c
		JOIN generated_runtime_deployments rd ON rd.deployment_id=c.deployment_id
		JOIN deployments d ON d.id=rd.deployment_id AND d.app_id=rd.app_id
		JOIN jobs j ON j.id=d.job_id
		LEFT JOIN generated_image_artifacts a ON a.id=c.image_artifact_id
		LEFT JOIN generated_candidate_intents intent
		  ON intent.deployment_id=c.deployment_id AND intent.component_name=c.component_name
		LEFT JOIN application_configuration_revisions cr
		  ON cr.id=intent.configuration_revision_id AND cr.app_id=rd.app_id
		WHERE c.deployment_id=? AND rd.app_id=?`,
		candidateFailureMode(boundary), candidateFailureMode(boundary), candidateFailureMode(boundary),
		deploymentID, appID).Scan(&total, &valid)
	if err != nil {
		return err
	}
	if total != expectedComponents || valid != expectedComponents {
		return ErrInvalidTransition
	}

	now := r.now().UTC().Format(time.RFC3339Nano)
	componentQuery := `UPDATE generated_runtime_components
		SET state='failed',diagnostic_code=?,updated_at=?,finished_at=?
		WHERE deployment_id=? AND slot=(
			SELECT candidate_slot FROM generated_runtime_deployments
			WHERE deployment_id=? AND app_id=? AND phase=?
		) AND ` + boundary.componentWhereSQL
	result, err := tx.ExecContext(ctx, componentQuery, diagnostic, now, now, deploymentID, deploymentID, appID, boundary.phase)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != int64(expectedComponents) {
		return ErrInvalidTransition
	}

	result, err = tx.ExecContext(ctx, `UPDATE generated_runtime_deployments
		SET phase='failed',diagnostic_code=?,updated_at=?,finished_at=?
		WHERE deployment_id=? AND app_id=? AND phase=?`, diagnostic, now, now, deploymentID, appID, boundary.phase)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}

	result, err = tx.ExecContext(ctx, `UPDATE deployments
		SET status='failed',finished_at=?,diagnostic_code=?,failure_code=?,failure_summary=?
		WHERE id=? AND app_id=? AND status=? AND runtime_strategy='generated_node'
		  AND provenance_initialized=1
		  AND EXISTS (SELECT 1 FROM generated_runtime_deployments rd
			WHERE rd.deployment_id=deployments.id AND rd.app_id=deployments.app_id
			  AND rd.phase='failed' AND rd.diagnostic_code=?)`,
		now, boundary.mainDiagnostic, boundary.mainDiagnostic, boundary.mainSummary,
		deploymentID, appID, boundary.mainStatus, diagnostic)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func candidateFailureBoundary(diagnostic DiagnosticCode) (candidateObservedFailureBoundary, bool) {
	switch diagnostic {
	case DiagnosticStartFailed:
		return candidateObservedFailureBoundary{
			phase:             PhaseStartingCandidate,
			mainStatus:        "applying",
			mainDiagnostic:    "apply_failed",
			mainSummary:       "Container runtime failed to apply the deployment",
			componentWhereSQL: "state IN ('image_ready','starting','running')",
		}, true
	case DiagnosticHealthFailed:
		return candidateObservedFailureBoundary{
			phase:             PhaseWaitingHealth,
			mainStatus:        "waiting_health",
			mainDiagnostic:    "health_failed",
			mainSummary:       "Deployment did not become healthy",
			componentWhereSQL: "state IN ('running','healthy')",
		}, true
	default:
		return candidateObservedFailureBoundary{}, false
	}
}

func candidateFailureMode(boundary candidateObservedFailureBoundary) string {
	if boundary.phase == PhaseStartingCandidate {
		return "start"
	}
	return "health"
}
