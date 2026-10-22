package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CandidateIntentInput is the immutable provenance recorded immediately
// before a generated candidate container may be created.
type CandidateIntentInput struct {
	ReceiptID                   string
	AppID                       string
	DeploymentID                string
	ComponentName               string
	ContainerName               string
	CreatorJobID                string
	CreatorWorkerAttempt        int
	ConfigurationRevisionID     string
	ConfigurationRevisionNumber int64
	ImageArtifactID             string
	ImageArtifactAttempt        int64
	EnvironmentOperationID      string
	EnvironmentStagingAttempt   int
	CandidatePolicyVersion      string
	CandidatePolicyDigest       string
}

func readGeneratedDomainStartupCandidateIntents(ctx context.Context, tx *sql.Tx) ([]GeneratedDomainStartupCandidateIntent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.deployment_id,i.component_name,i.creator_job_id,i.creator_worker_attempt,
		i.configuration_revision_id,i.configuration_revision_number,i.image_artifact_id,i.image_artifact_attempt,
		i.environment_operation_id,i.environment_staging_attempt,i.candidate_policy_version,i.candidate_policy_digest,i.created_at,
		COALESCE(c.deployment_id,''),COALESCE(c.component_name,''),COALESCE(c.image_artifact_id,''),
		COALESCE(rd.app_id,''),COALESCE(rd.release_id,''),COALESCE(rd.deployment_plan_revision_id,''),
		COALESCE(rd.deployment_plan_revision_number,0),COALESCE(d.id,''),COALESCE(d.job_id,''),
		COALESCE(d.actual_configuration_revision_id,''),COALESCE(d.actual_configuration_revision_number,0),
		COALESCE(d.runtime_strategy,''),COALESCE(d.provenance_initialized,0),
		COALESCE(j.id,''),COALESCE(j.type,''),COALESCE(j.resource_type,''),COALESCE(j.resource_id,''),
		COALESCE(cr.id,''),COALESCE(cr.app_id,''),COALESCE(cr.revision_number,0),COALESCE(cr.bundle_version,0),
		COALESCE(cr.deployment_plan_revision_id,''),COALESCE(cr.deployment_plan_revision_number,0),
		COALESCE(a.id,''),COALESCE(a.release_id,''),COALESCE(a.deployment_plan_revision_id,''),
		COALESCE(a.deployment_plan_revision_number,0),COALESCE(a.component_id,''),COALESCE(a.attempt_number,0)
		FROM generated_candidate_intents i
		LEFT JOIN generated_runtime_components c
		 ON c.deployment_id=i.deployment_id AND c.component_name=i.component_name
		LEFT JOIN generated_runtime_deployments rd ON rd.deployment_id=i.deployment_id
		LEFT JOIN deployments d ON d.id=i.deployment_id
		LEFT JOIN jobs j ON j.id=i.creator_job_id
		LEFT JOIN application_configuration_revisions cr ON cr.id=i.configuration_revision_id
		LEFT JOIN generated_image_artifacts a ON a.id=i.image_artifact_id
		ORDER BY i.deployment_id,i.component_name,i.id`)
	if err != nil {
		return nil, err
	}
	result := []GeneratedDomainStartupCandidateIntent{}
	for rows.Next() {
		var value GeneratedDomainStartupCandidateIntent
		var createdAt string
		var componentDeployment, componentName, componentArtifact string
		var runtimeApp, runtimeRelease, runtimePlan string
		var runtimePlanNumber int64
		var deploymentID, deploymentJob, deploymentConfiguration, deploymentStrategy string
		var deploymentConfigurationNumber int64
		var deploymentInitialized int
		var jobID, jobType, jobResourceType, jobResourceID string
		var configurationID, configurationApp, configurationPlan string
		var configurationNumber, configurationBundle, configurationPlanNumber int64
		var artifactID, artifactRelease, artifactPlan, artifactComponent string
		var artifactPlanNumber, artifactAttempt int64
		if err := rows.Scan(&value.ReceiptID, &value.DeploymentID, &value.ComponentName,
			&value.CreatorJobID, &value.CreatorWorkerAttempt, &value.ConfigurationRevisionID,
			&value.ConfigurationRevisionNumber, &value.ImageArtifactID, &value.ImageArtifactAttempt,
			&value.EnvironmentOperationID, &value.EnvironmentStagingAttempt, &value.CandidatePolicyVersion,
			&value.CandidatePolicyDigest, &createdAt, &componentDeployment, &componentName, &componentArtifact,
			&runtimeApp, &runtimeRelease, &runtimePlan, &runtimePlanNumber, &deploymentID, &deploymentJob,
			&deploymentConfiguration, &deploymentConfigurationNumber, &deploymentStrategy, &deploymentInitialized,
			&jobID, &jobType, &jobResourceType, &jobResourceID, &configurationID, &configurationApp,
			&configurationNumber, &configurationBundle, &configurationPlan, &configurationPlanNumber,
			&artifactID, &artifactRelease, &artifactPlan, &artifactPlanNumber, &artifactComponent, &artifactAttempt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !validGeneratedDomainStartupCandidateIntent(value) {
			_ = rows.Close()
			return nil, ErrInvalidState
		}
		if _, err := parseTime(createdAt); err != nil {
			_ = rows.Close()
			return nil, ErrInvalidState
		}
		if componentDeployment != value.DeploymentID || componentName != value.ComponentName ||
			componentArtifact != value.ImageArtifactID || !canonicalUUID(runtimeApp) || !validReleaseID(runtimeRelease) ||
			!canonicalUUID(runtimePlan) || runtimePlanNumber < 1 || deploymentID != value.DeploymentID ||
			deploymentJob != value.CreatorJobID || deploymentConfiguration != value.ConfigurationRevisionID ||
			deploymentConfigurationNumber != value.ConfigurationRevisionNumber || deploymentStrategy != "generated_node" ||
			deploymentInitialized != 1 || jobID != value.CreatorJobID || jobType != "deploy" ||
			jobResourceType != "application" || jobResourceID != runtimeApp ||
			configurationID != value.ConfigurationRevisionID || configurationApp != runtimeApp ||
			configurationNumber != value.ConfigurationRevisionNumber || configurationBundle != 2 ||
			configurationPlan != runtimePlan || configurationPlanNumber != runtimePlanNumber ||
			artifactID != value.ImageArtifactID || artifactRelease != runtimeRelease || artifactPlan != runtimePlan ||
			artifactPlanNumber != runtimePlanNumber || artifactComponent != value.ComponentName ||
			artifactAttempt != value.ImageArtifactAttempt {
			_ = rows.Close()
			return nil, ErrInvalidState
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func validGeneratedDomainStartupCandidateIntent(value GeneratedDomainStartupCandidateIntent) bool {
	return canonicalUUID(value.ReceiptID) && canonicalUUID(value.DeploymentID) &&
		validText(value.ComponentName, 256) && canonicalUUID(value.CreatorJobID) && value.CreatorWorkerAttempt > 0 &&
		canonicalUUID(value.ConfigurationRevisionID) && value.ConfigurationRevisionNumber > 0 &&
		canonicalUUID(value.ImageArtifactID) && value.ImageArtifactAttempt > 0 &&
		value.EnvironmentOperationID == value.ImageArtifactID &&
		value.EnvironmentStagingAttempt == value.CreatorWorkerAttempt &&
		validText(value.CandidatePolicyVersion, 128) && lowerHex(value.CandidatePolicyDigest, 64)
}

// BeginCandidateTx records immutable create intent and advances the exact
// image-ready component in the caller's guarded writer transaction.
func (r *Repository) BeginCandidateTx(ctx context.Context, tx *sql.Tx, input CandidateIntentInput) error {
	if r == nil || r.db == nil || ctx == nil || tx == nil ||
		!canonicalUUID(input.ReceiptID) || !canonicalUUID(input.AppID) || !canonicalUUID(input.DeploymentID) ||
		!validText(input.ComponentName, 256) || !validContainerName(input.ContainerName) ||
		!canonicalUUID(input.CreatorJobID) || input.CreatorWorkerAttempt < 1 ||
		!canonicalUUID(input.ConfigurationRevisionID) || input.ConfigurationRevisionNumber < 1 ||
		!canonicalUUID(input.ImageArtifactID) || input.ImageArtifactAttempt < 1 ||
		!canonicalUUID(input.EnvironmentOperationID) || input.EnvironmentOperationID != input.ImageArtifactID ||
		input.EnvironmentStagingAttempt < 1 || input.EnvironmentStagingAttempt != input.CreatorWorkerAttempt ||
		!validText(input.CandidatePolicyVersion, 128) || !lowerHex(input.CandidatePolicyDigest, 64) {
		return ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO generated_candidate_intents(
		id,deployment_id,component_name,creator_job_id,creator_worker_attempt,
		configuration_revision_id,configuration_revision_number,image_artifact_id,image_artifact_attempt,
		environment_operation_id,environment_staging_attempt,candidate_policy_version,candidate_policy_digest,created_at
	) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?
	FROM generated_runtime_components c
	JOIN generated_runtime_deployments rd ON rd.deployment_id=c.deployment_id
	JOIN deployments d ON d.id=rd.deployment_id AND d.app_id=rd.app_id
	JOIN releases rel ON rel.id=rd.release_id AND rel.app_id=rd.app_id
	JOIN jobs j ON j.id=d.job_id
	JOIN application_configuration_revisions cr ON cr.id=? AND cr.app_id=rd.app_id
	JOIN generated_image_artifacts artifact ON artifact.id=?
	WHERE c.deployment_id=? AND c.component_name=? AND rd.app_id=?
	  AND c.slot=rd.candidate_slot AND c.state='image_ready' AND c.image_artifact_id=?
	  AND c.container_name IS NULL AND c.container_id IS NULL AND rd.phase='starting_candidate'
	  AND d.release_id=rd.release_id AND d.runtime_strategy='generated_node' AND d.provenance_initialized=1
	  AND d.status='applying' AND d.deployment_plan_revision_id=rd.deployment_plan_revision_id
	  AND d.deployment_plan_revision_number=rd.deployment_plan_revision_number
	  AND d.actual_configuration_revision_id=? AND d.actual_configuration_revision_number=?
	  AND rel.deployment_plan_revision_id=rd.deployment_plan_revision_id
	  AND rel.deployment_plan_revision_number=rd.deployment_plan_revision_number
	  AND cr.revision_number=? AND cr.bundle_version=2
	  AND cr.deployment_plan_revision_id=rd.deployment_plan_revision_id
	  AND cr.deployment_plan_revision_number=rd.deployment_plan_revision_number
	  AND j.id=? AND j.type='deploy' AND j.resource_type='application' AND j.resource_id=rd.app_id
	  AND j.status='running' AND j.phase<>'cancelling' AND j.attempt=?
	  AND artifact.release_id=rd.release_id
	  AND artifact.deployment_plan_revision_id=rd.deployment_plan_revision_id
	  AND artifact.deployment_plan_revision_number=rd.deployment_plan_revision_number
	  AND artifact.component_id=? AND artifact.attempt_number=? AND artifact.state='ready'
	  AND NOT EXISTS (
		SELECT 1 FROM generated_candidate_intents existing
		WHERE existing.id=? OR (existing.deployment_id=? AND existing.component_name=?)
	  )`,
		input.ReceiptID, input.DeploymentID, input.ComponentName, input.CreatorJobID, input.CreatorWorkerAttempt,
		input.ConfigurationRevisionID, input.ConfigurationRevisionNumber, input.ImageArtifactID, input.ImageArtifactAttempt,
		input.EnvironmentOperationID, input.EnvironmentStagingAttempt, input.CandidatePolicyVersion, input.CandidatePolicyDigest, now,
		input.ConfigurationRevisionID, input.ImageArtifactID,
		input.DeploymentID, input.ComponentName, input.AppID, input.ImageArtifactID,
		input.ConfigurationRevisionID, input.ConfigurationRevisionNumber, input.ConfigurationRevisionNumber,
		input.CreatorJobID, input.CreatorWorkerAttempt, input.ComponentName, input.ImageArtifactAttempt,
		input.ReceiptID, input.DeploymentID, input.ComponentName)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	result, err = tx.ExecContext(ctx, `UPDATE generated_runtime_components
		SET state='starting',container_name=?,updated_at=?
		WHERE deployment_id=? AND component_name=? AND state='image_ready' AND image_artifact_id=?
		  AND container_name IS NULL AND container_id IS NULL
		  AND EXISTS (
			SELECT 1 FROM generated_candidate_intents intent
			JOIN generated_runtime_deployments rd ON rd.deployment_id=intent.deployment_id
			WHERE intent.id=? AND intent.deployment_id=generated_runtime_components.deployment_id
			  AND intent.component_name=generated_runtime_components.component_name
			  AND intent.image_artifact_id=generated_runtime_components.image_artifact_id
			  AND rd.app_id=? AND rd.phase='starting_candidate'
		  )`, input.ContainerName, now, input.DeploymentID, input.ComponentName, input.ImageArtifactID, input.ReceiptID, input.AppID)
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

// RecordCandidateCreatedTx attaches the observed immutable container ID to the
// exact receipt and name inside the caller's guarded writer transaction.
func (r *Repository) RecordCandidateCreatedTx(ctx context.Context, tx *sql.Tx, appID, deploymentID, componentName, receiptID, containerName, containerID string) error {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) ||
		!validText(componentName, 256) || !canonicalUUID(receiptID) || !validContainerName(containerName) || !lowerHex(containerID, 64) {
		return ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_components
		SET state='running',container_id=?,updated_at=?
		WHERE deployment_id=? AND component_name=? AND state='starting' AND container_name=? AND container_id IS NULL
		  AND EXISTS (
			SELECT 1 FROM generated_candidate_intents intent
			JOIN generated_runtime_deployments rd ON rd.deployment_id=intent.deployment_id
			WHERE intent.id=? AND intent.deployment_id=generated_runtime_components.deployment_id
			  AND intent.component_name=generated_runtime_components.component_name
			  AND intent.image_artifact_id=generated_runtime_components.image_artifact_id
			  AND rd.app_id=? AND rd.phase='starting_candidate'
		  )`, containerID, now, deploymentID, componentName, containerName, receiptID, appID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	return nil
}

// RecordCandidateHealthyTx advances the exact receipt-backed running
// container after the caller has freshly observed it healthy.
func (r *Repository) RecordCandidateHealthyTx(ctx context.Context, tx *sql.Tx, appID, deploymentID, componentName, receiptID, containerID string) error {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) ||
		!validText(componentName, 256) || !canonicalUUID(receiptID) || !lowerHex(containerID, 64) {
		return ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_components
		SET state='healthy',updated_at=?
		WHERE deployment_id=? AND component_name=? AND state='running'
		  AND container_name IS NOT NULL AND container_id=?
		  AND EXISTS (
			SELECT 1 FROM generated_candidate_intents intent
			JOIN generated_runtime_deployments rd ON rd.deployment_id=intent.deployment_id
			WHERE intent.id=? AND intent.deployment_id=generated_runtime_components.deployment_id
			  AND intent.component_name=generated_runtime_components.component_name
			  AND intent.image_artifact_id=generated_runtime_components.image_artifact_id
			  AND rd.app_id=? AND rd.phase='waiting_health'
		  )`, now, deploymentID, componentName, containerID, receiptID, appID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	return nil
}

// FinishCandidateStartingTx advances only a complete receipt-backed Running
// roster. The protected owner must separately compare exact component names
// with its pinned plan because persisted accepted plans store only a count.
func (r *Repository) FinishCandidateStartingTx(ctx context.Context, tx *sql.Tx, appID, deploymentID string) error {
	if err := r.candidateRosterReadyTx(ctx, tx, appID, deploymentID, PhaseStartingCandidate, "applying", ComponentRunning); err != nil {
		return err
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_deployments SET phase='waiting_health',updated_at=?
		WHERE deployment_id=? AND app_id=? AND phase='starting_candidate'`, now, deploymentID, appID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	result, err = tx.ExecContext(ctx, `UPDATE deployments SET status='waiting_health'
		WHERE id=? AND app_id=? AND status='applying' AND runtime_strategy='generated_node'
		  AND provenance_initialized=1
		  AND EXISTS (
			SELECT 1 FROM generated_runtime_deployments rd
			WHERE rd.deployment_id=deployments.id AND rd.app_id=deployments.app_id AND rd.phase='waiting_health'
		  )`, deploymentID, appID)
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

// FinishCandidateHealthTx advances only a complete receipt-backed Healthy
// roster. Route handoff is performed by the protected owner after this stage.
func (r *Repository) FinishCandidateHealthTx(ctx context.Context, tx *sql.Tx, appID, deploymentID string) error {
	if err := r.candidateRosterReadyTx(ctx, tx, appID, deploymentID, PhaseWaitingHealth, "waiting_health", ComponentHealthy); err != nil {
		return err
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_deployments SET phase='switching_route',updated_at=?
		WHERE deployment_id=? AND app_id=? AND phase='waiting_health'`, now, deploymentID, appID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (r *Repository) candidateRosterReadyTx(ctx context.Context, tx *sql.Tx, appID, deploymentID string, runtimePhase Phase, deploymentStatus string, componentState ComponentState) error {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) {
		return ErrInvalidTransition
	}
	var phase Phase
	var status, strategy, acceptance string
	var provenance, expectedComponents int
	err := tx.QueryRowContext(ctx, `SELECT rd.phase,d.status,d.runtime_strategy,d.provenance_initialized,p.acceptance_status,p.component_count
		FROM generated_runtime_deployments rd
		JOIN deployments d ON d.id=rd.deployment_id AND d.app_id=rd.app_id
		JOIN deployment_plan_revisions p
		  ON p.id=rd.deployment_plan_revision_id AND p.app_id=rd.app_id
		 AND p.revision_number=rd.deployment_plan_revision_number
		WHERE rd.deployment_id=? AND rd.app_id=?
		  AND d.release_id=rd.release_id
		  AND d.deployment_plan_revision_id=rd.deployment_plan_revision_id
		  AND d.deployment_plan_revision_number=rd.deployment_plan_revision_number`, deploymentID, appID).
		Scan(&phase, &status, &strategy, &provenance, &acceptance, &expectedComponents)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if phase != runtimePhase || status != deploymentStatus || strategy != "generated_node" || provenance != 1 ||
		acceptance != "accepted" || expectedComponents < 1 || expectedComponents > 2 {
		return ErrInvalidTransition
	}
	var total, ready int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN
		c.slot=rd.candidate_slot AND c.state=? AND c.container_name IS NOT NULL AND c.container_id IS NOT NULL
		AND EXISTS (
			SELECT 1 FROM generated_candidate_intents intent
			JOIN generated_image_artifacts artifact ON artifact.id=intent.image_artifact_id
			JOIN application_configuration_revisions cr
			  ON cr.id=intent.configuration_revision_id AND cr.app_id=rd.app_id
			WHERE intent.deployment_id=c.deployment_id AND intent.component_name=c.component_name
			  AND intent.creator_job_id=d.job_id
			  AND intent.configuration_revision_id=d.actual_configuration_revision_id
			  AND intent.configuration_revision_number=d.actual_configuration_revision_number
			  AND cr.revision_number=intent.configuration_revision_number AND cr.bundle_version=2
			  AND cr.deployment_plan_revision_id=rd.deployment_plan_revision_id
			  AND cr.deployment_plan_revision_number=rd.deployment_plan_revision_number
			  AND intent.image_artifact_id=c.image_artifact_id
			  AND intent.environment_operation_id=intent.image_artifact_id
			  AND intent.environment_staging_attempt=intent.creator_worker_attempt
			  AND artifact.release_id=rd.release_id
			  AND artifact.deployment_plan_revision_id=rd.deployment_plan_revision_id
			  AND artifact.deployment_plan_revision_number=rd.deployment_plan_revision_number
			  AND artifact.component_id=c.component_name
			  AND artifact.attempt_number=intent.image_artifact_attempt AND artifact.state='ready'
		) THEN 1 ELSE 0 END),0)
		FROM generated_runtime_components c
		JOIN generated_runtime_deployments rd ON rd.deployment_id=c.deployment_id
		JOIN deployments d ON d.id=rd.deployment_id AND d.app_id=rd.app_id
		WHERE c.deployment_id=? AND rd.app_id=?`, componentState, deploymentID, appID).Scan(&total, &ready)
	if err != nil {
		return err
	}
	if total != expectedComponents || ready != expectedComponents {
		return ErrInvalidTransition
	}
	return nil
}
