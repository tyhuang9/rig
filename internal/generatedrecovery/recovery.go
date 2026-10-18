// Package generatedrecovery classifies and persists daemon-restart recovery
// without loading protected deployment-plan bundles. It stores only durable
// identities, lifecycle states, and fixed diagnostics; commands, environment
// values, and runtime output never cross this boundary.
package generatedrecovery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntime"
)

const (
	diagnosticDaemonRestarted = "daemon_restarted"
	restartSummary            = "Deployment interrupted because hostd restarted"
	interruptedJobSummary     = "Job interrupted because hostd restarted"
	requeuedJobSummary        = "Generated deployment requeued because hostd restarted"
	routeReconciliationPause  = "route_reconciliation_required"
	replacementCapacityPause  = "insufficient_replacement_capacity"
	deploymentLifecycleStale  = "deployment_lifecycle_stale"
	deploymentStaleSummary    = "Deployment no longer matches the application's desired lifecycle."
)

var (
	ErrInvalidRecovery         = errors.New("invalid generated restart recovery")
	ErrRecoveryConflict        = errors.New("generated restart recovery conflict")
	ErrGuardedRecoveryRequired = errors.New("guarded deployment recovery required")
)

// DeploymentResult contains non-secret aggregate recovery outcomes.
type DeploymentResult struct {
	PreservedGenerated int
	PausedGenerated    int
	FailedGenerated    int
	FailedOther        int
	RetainedStale      int
}

// JobResult contains non-secret aggregate recovery outcomes.
type JobResult struct {
	RequeuedGenerated int
	PausedGenerated   int
	Interrupted       int
	RetiredStale      int
}

// RuntimeResult contains the aggregate result of the single guarded startup
// recovery transaction. Counts are published only after that transaction
// commits successfully.
type RuntimeResult struct {
	Deployments DeploymentResult
	Jobs        JobResult
}

type runtimeDeployment struct {
	deploymentID       string
	appID              string
	releaseID          string
	planRevisionID     string
	planRevisionNumber int64
	candidateSlot      string
	previousDeployment string
	previousSlot       string
	phase              string
	migrationState     string
	components         []runtimeComponent
}

type runtimeComponent struct {
	name          string
	slot          string
	artifactID    string
	containerName string
	containerID   string
	state         string
	artifactValid bool
}

type recoveryBinding struct {
	deploymentID        string
	appID               string
	releaseID           string
	jobID               string
	deploymentStatus    string
	configurationMode   string
	planRevisionID      string
	planRevisionNumber  int64
	actualConfigID      string
	actualConfigNumber  int64
	configBundleVersion int64
	configPlanID        string
	configPlanNumber    int64
	jobStatus           string
	jobPhase            string
	pauseDisposition    string
	jobType             string
	jobResourceType     string
	jobResourceID       string
	requestedBy         string
	jobInput            []byte
	jobAttempt          int
}

type activeHead struct {
	deploymentID string
	releaseID    string
	slot         string
}

type deploymentInput struct {
	ReleaseID                           string `json:"releaseId"`
	ConfigurationMode                   string `json:"configurationMode"`
	ExpectedPlanRevisionID              string `json:"expectedPlanRevisionId"`
	ExpectedPlanRevisionNumber          int64  `json:"expectedPlanRevisionNumber"`
	ExpectedConfigurationRevisionID     string `json:"expectedConfigurationRevisionId"`
	ExpectedConfigurationRevisionNumber int64  `json:"expectedConfigurationRevisionNumber"`
}

// RecoverDeployments is a compatibility wrapper for callers that have no
// deployment recovery work. Deployment work requires RecoverRuntime so its
// authority classification and job/event writes share one transaction.
func RecoverDeployments(ctx context.Context, db *sql.DB, now time.Time) (DeploymentResult, error) {
	if ctx == nil || db == nil || now.IsZero() {
		return DeploymentResult{}, ErrInvalidRecovery
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentResult{}, err
	}
	defer tx.Rollback()
	if err := reserveRecoveryWriter(ctx, tx); err != nil {
		return DeploymentResult{}, err
	}
	census, err := loadRecoveryCensus(ctx, tx)
	if err != nil {
		return DeploymentResult{}, err
	}
	if census.hasDeploymentWork() {
		return DeploymentResult{}, ErrGuardedRecoveryRequired
	}
	if err := tx.Commit(); err != nil {
		return DeploymentResult{}, err
	}
	return DeploymentResult{}, nil
}

// RecoverJobs retains legacy generic non-deployment interruption. It refuses
// before mutation when any deployment recovery subject exists.
func RecoverJobs(ctx context.Context, db *sql.DB, now time.Time) (JobResult, error) {
	if ctx == nil || db == nil || now.IsZero() {
		return JobResult{}, ErrInvalidRecovery
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return JobResult{}, err
	}
	defer tx.Rollback()
	if err := reserveRecoveryWriter(ctx, tx); err != nil {
		return JobResult{}, err
	}
	census, err := loadRecoveryCensus(ctx, tx)
	if err != nil {
		return JobResult{}, err
	}
	if census.hasDeploymentWork() {
		return JobResult{}, ErrGuardedRecoveryRequired
	}
	result, err := recoverJobsTx(ctx, tx, now.UTC().Format(time.RFC3339Nano), nil)
	if err != nil {
		return JobResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return JobResult{}, err
	}
	return result, nil
}

func resumableDeployments(ctx context.Context, tx *sql.Tx, runtimes []runtimeDeployment) (map[string]recoveryBinding, error) {
	result := make(map[string]recoveryBinding)
	for _, runtimeDeployment := range runtimes {
		binding, found, err := loadRecoveryBinding(ctx, tx, runtimeDeployment.deploymentID)
		if err != nil {
			return nil, err
		}
		if !found || !bindingMatchesRuntime(binding, runtimeDeployment) {
			continue
		}
		head, found, err := loadActiveHead(ctx, tx, runtimeDeployment.appID)
		if err != nil {
			return nil, err
		}
		if !found || !runtimeStateResumable(runtimeDeployment, head) {
			continue
		}
		if runtimeDeployment.phase == "succeeded" {
			drained, err := previousSlotDrained(ctx, tx, runtimeDeployment)
			if err != nil {
				return nil, err
			}
			if !drained {
				continue
			}
		}
		result[runtimeDeployment.deploymentID] = binding
	}
	return result, nil
}

func previousSlotDrained(ctx context.Context, tx *sql.Tx, value runtimeDeployment) (bool, error) {
	if value.previousDeployment == "" {
		return true, nil
	}
	var total, stopped int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN c.state='stopped' THEN 1 ELSE 0 END),0)
		FROM generated_runtime_components c JOIN generated_runtime_deployments d ON d.deployment_id=c.deployment_id
		WHERE d.deployment_id=? AND d.app_id=? AND d.candidate_slot=?`, value.previousDeployment, value.appID, value.previousSlot).Scan(&total, &stopped)
	return total >= 1 && total <= 2 && stopped == total, err
}

func loadRuntimeDeployments(ctx context.Context, tx *sql.Tx) ([]runtimeDeployment, error) {
	rows, err := tx.QueryContext(ctx, `SELECT deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,candidate_slot,COALESCE(previous_active_deployment_id,''),COALESCE(previous_active_slot,''),phase,migration_state FROM generated_runtime_deployments r WHERE phase NOT IN ('failed','cancelled') AND (phase<>'succeeded' OR EXISTS(
		SELECT 1 FROM deployments d JOIN jobs j ON j.id=d.job_id
		WHERE d.id=r.deployment_id AND d.status IN ('preparing','applying','waiting_health','succeeded')
		  AND j.status IN ('queued','assigned','running','waiting_external','waiting_user')
	)) ORDER BY updated_at,deployment_id`)
	if err != nil {
		return nil, err
	}
	var result []runtimeDeployment
	for rows.Next() {
		var value runtimeDeployment
		if err := rows.Scan(&value.deploymentID, &value.appID, &value.releaseID, &value.planRevisionID, &value.planRevisionNumber, &value.candidateSlot, &value.previousDeployment, &value.previousSlot, &value.phase, &value.migrationState); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		components, err := loadRuntimeComponents(ctx, tx, result[index])
		if err != nil {
			return nil, err
		}
		result[index].components = components
	}
	return result, nil
}

func loadRuntimeComponents(ctx context.Context, tx *sql.Tx, deployment runtimeDeployment) ([]runtimeComponent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.component_name,c.slot,COALESCE(c.image_artifact_id,''),COALESCE(c.container_name,''),COALESCE(c.container_id,''),c.state,
		CASE WHEN c.image_artifact_id IS NOT NULL AND EXISTS(
			SELECT 1 FROM generated_image_artifacts a
			WHERE a.id=c.image_artifact_id AND a.release_id=? AND a.deployment_plan_revision_id=?
			  AND a.deployment_plan_revision_number=? AND a.component_id=c.component_name
			  AND a.state='ready' AND a.image_content_id IS NOT NULL
		) THEN 1 ELSE 0 END
	FROM generated_runtime_components c WHERE c.deployment_id=? ORDER BY c.component_name`, deployment.releaseID, deployment.planRevisionID, deployment.planRevisionNumber, deployment.deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []runtimeComponent
	for rows.Next() {
		var value runtimeComponent
		if err := rows.Scan(&value.name, &value.slot, &value.artifactID, &value.containerName, &value.containerID, &value.state, &value.artifactValid); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func loadRecoveryBinding(ctx context.Context, tx *sql.Tx, deploymentID string) (recoveryBinding, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT d.id,d.app_id,d.release_id,d.job_id,d.status,d.configuration_mode,d.deployment_plan_revision_id,d.deployment_plan_revision_number,
		COALESCE(d.actual_configuration_revision_id,''),d.actual_configuration_revision_number,COALESCE(c.bundle_version,0),COALESCE(c.deployment_plan_revision_id,''),COALESCE(c.deployment_plan_revision_number,0),
		j.status,j.phase,COALESCE(j.pause_disposition,''),j.type,j.resource_type,j.resource_id,COALESCE(j.requested_by,''),j.input_json,j.attempt
	FROM deployments d
	JOIN jobs j ON j.id=d.job_id AND j.type='deploy' AND j.resource_type='application' AND j.resource_id=d.app_id
	JOIN releases r ON r.id=d.release_id AND r.app_id=d.app_id AND (r.workspace_state='ready' OR r.workspace_state IS NULL)
		AND r.deployment_plan_revision_id=d.deployment_plan_revision_id AND r.deployment_plan_revision_number=d.deployment_plan_revision_number
	JOIN deployment_plan_revisions p ON p.id=d.deployment_plan_revision_id AND p.app_id=d.app_id
		AND p.revision_number=d.deployment_plan_revision_number AND p.strategy='generated_node' AND p.acceptance_status='accepted'
	LEFT JOIN application_configuration_revisions c ON c.id=d.actual_configuration_revision_id AND c.app_id=d.app_id
		AND c.revision_number=d.actual_configuration_revision_number
	WHERE d.id=? AND d.status IN ('preparing','applying','waiting_health','succeeded')
	  AND d.runtime_strategy='generated_node' AND d.provenance_initialized=1
	  AND d.release_id IS NOT NULL AND d.deployment_plan_revision_id IS NOT NULL AND d.deployment_plan_revision_number>0
	  AND ((d.actual_configuration_revision_number=0 AND d.actual_configuration_revision_id IS NULL) OR
		(c.id IS NOT NULL AND c.bundle_version=2 AND c.deployment_plan_revision_id=d.deployment_plan_revision_id AND c.deployment_plan_revision_number=d.deployment_plan_revision_number))
	  AND (p.migration_evidence_digest='' OR EXISTS(
		SELECT 1 FROM deployment_plan_migration_approvals a WHERE a.revision_id=p.id AND a.app_id=p.app_id
	  ))
	  AND (j.status IN ('queued','assigned','running','waiting_external') OR
	       (j.status='waiting_user' AND ((j.phase=? AND j.pause_disposition=?) OR (j.phase=? AND j.pause_disposition=?))))`, deploymentID,
		routeReconciliationPause, routeReconciliationPause, replacementCapacityPause, replacementCapacityPause)
	var value recoveryBinding
	var input string
	if err := row.Scan(&value.deploymentID, &value.appID, &value.releaseID, &value.jobID, &value.deploymentStatus, &value.configurationMode, &value.planRevisionID, &value.planRevisionNumber,
		&value.actualConfigID, &value.actualConfigNumber, &value.configBundleVersion, &value.configPlanID, &value.configPlanNumber,
		&value.jobStatus, &value.jobPhase, &value.pauseDisposition, &value.jobType, &value.jobResourceType, &value.jobResourceID, &value.requestedBy, &input, &value.jobAttempt); errors.Is(err, sql.ErrNoRows) {
		return recoveryBinding{}, false, nil
	} else if err != nil {
		return recoveryBinding{}, false, err
	}
	value.jobInput = []byte(input)
	return value, true, nil
}

func loadActiveHead(ctx context.Context, tx *sql.Tx, appID string) (activeHead, bool, error) {
	var value activeHead
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(deployment_id,''),COALESCE(release_id,''),COALESCE(slot,'') FROM generated_runtime_active_heads WHERE app_id=?`, appID).Scan(&value.deploymentID, &value.releaseID, &value.slot)
	if errors.Is(err, sql.ErrNoRows) {
		return activeHead{}, false, nil
	}
	return value, err == nil, err
}

func bindingMatchesRuntime(binding recoveryBinding, runtimeDeployment runtimeDeployment) bool {
	if uuid.Validate(binding.deploymentID) != nil || uuid.Validate(binding.appID) != nil || uuid.Validate(binding.jobID) != nil || uuid.Validate(binding.planRevisionID) != nil || uuid.Validate(binding.requestedBy) != nil ||
		binding.deploymentID != runtimeDeployment.deploymentID || binding.appID != runtimeDeployment.appID || binding.releaseID != runtimeDeployment.releaseID ||
		binding.planRevisionID != runtimeDeployment.planRevisionID || binding.planRevisionNumber != runtimeDeployment.planRevisionNumber || binding.jobResourceID != binding.appID ||
		binding.jobType != "deploy" || binding.jobResourceType != "application" || binding.jobAttempt < 1 ||
		(binding.jobStatus == "waiting_external" && binding.jobPhase == "cancelling") ||
		!actualConfigurationMatchesPlan(binding) || !intentionalPauseMatchesRuntime(binding, runtimeDeployment) {
		return false
	}
	input, ok := decodeDeploymentInput(binding.jobInput)
	if !ok || !inputMatchesBinding(input, binding) {
		return false
	}
	switch runtimeDeployment.phase {
	case "preflight", "building":
		return binding.deploymentStatus == "preparing" || binding.deploymentStatus == "applying"
	case "migrating", "starting_candidate":
		return binding.deploymentStatus == "applying"
	case "waiting_health", "switching_route", "draining", "succeeded":
		if runtimeDeployment.phase == "succeeded" && binding.jobStatus == "waiting_user" {
			return false
		}
		if runtimeDeployment.phase == "succeeded" && binding.deploymentStatus == "succeeded" {
			return true
		}
		return binding.deploymentStatus == "applying" || binding.deploymentStatus == "waiting_health"
	default:
		return false
	}
}

func actualConfigurationMatchesPlan(binding recoveryBinding) bool {
	if binding.actualConfigNumber == 0 {
		return binding.actualConfigID == "" && binding.configBundleVersion == 0 && binding.configPlanID == "" && binding.configPlanNumber == 0
	}
	return uuid.Validate(binding.actualConfigID) == nil && binding.configBundleVersion == 2 &&
		binding.configPlanID == binding.planRevisionID && binding.configPlanNumber == binding.planRevisionNumber
}

func intentionalPauseMatchesRuntime(binding recoveryBinding, runtimeDeployment runtimeDeployment) bool {
	if binding.jobStatus != "waiting_user" {
		return true
	}
	if binding.jobPhase != binding.pauseDisposition {
		return false
	}
	switch binding.pauseDisposition {
	case routeReconciliationPause:
		return true
	case replacementCapacityPause:
		switch runtimeDeployment.phase {
		case "building", "starting_candidate", "waiting_health", "switching_route":
			return true
		case "migrating":
			return runtimeDeployment.migrationState == "pending" || runtimeDeployment.migrationState == "succeeded"
		default:
			return false
		}
	default:
		return false
	}
}

func inputMatchesBinding(input deploymentInput, binding recoveryBinding) bool {
	if input.ConfigurationMode != binding.configurationMode {
		return false
	}
	if input.ExpectedPlanRevisionID == "" {
		return input.ReleaseID == "" || input.ReleaseID == binding.releaseID
	}
	return input.ExpectedPlanRevisionID == binding.planRevisionID && input.ExpectedPlanRevisionNumber == binding.planRevisionNumber &&
		input.ExpectedConfigurationRevisionID == binding.actualConfigID && input.ExpectedConfigurationRevisionNumber == binding.actualConfigNumber
}

func decodeDeploymentInput(raw []byte) (deploymentInput, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input deploymentInput
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return deploymentInput{}, false
	}
	if input.ConfigurationMode != "current" && input.ConfigurationMode != "original" {
		return deploymentInput{}, false
	}
	if input.ReleaseID != "" {
		if _, err := uuid.Parse(input.ReleaseID); err != nil {
			return deploymentInput{}, false
		}
	}
	if input.ExpectedPlanRevisionID == "" {
		if input.ExpectedPlanRevisionNumber != 0 || input.ExpectedConfigurationRevisionID != "" || input.ExpectedConfigurationRevisionNumber != 0 {
			return deploymentInput{}, false
		}
		return input, true
	}
	if input.ReleaseID != "" || input.ConfigurationMode != "current" || uuid.Validate(input.ExpectedPlanRevisionID) != nil || input.ExpectedPlanRevisionNumber < 1 ||
		input.ExpectedConfigurationRevisionNumber < 0 || (input.ExpectedConfigurationRevisionID == "") != (input.ExpectedConfigurationRevisionNumber == 0) ||
		(input.ExpectedConfigurationRevisionID != "" && uuid.Validate(input.ExpectedConfigurationRevisionID) != nil) {
		return deploymentInput{}, false
	}
	return input, true
}

func runtimeStateResumable(value runtimeDeployment, head activeHead) bool {
	if len(value.components) < 1 || len(value.components) > 2 || (value.candidateSlot != "blue" && value.candidateSlot != "green") ||
		(value.previousDeployment == "") != (value.previousSlot == "") || (value.previousSlot != "" && value.previousSlot == value.candidateSlot) {
		return false
	}
	descriptionByComponent := make(map[string]generatedruntime.CandidateDescription, len(value.components))
	for _, component := range value.components {
		if component.slot != value.candidateSlot {
			return false
		}
		description, err := generatedruntime.DescribeInactiveCandidate(value.appID, component.name, generatedruntime.Slot(value.previousSlot))
		if err != nil || string(description.Slot) != value.candidateSlot {
			return false
		}
		descriptionByComponent[component.name] = description
	}
	headIsPrevious := head.deploymentID == value.previousDeployment && head.slot == value.previousSlot
	headIsCandidate := head.deploymentID == value.deploymentID && head.releaseID == value.releaseID && head.slot == value.candidateSlot

	allowedStates := map[string]bool{}
	switch value.phase {
	case "preflight":
		if (value.migrationState != "not_required" && value.migrationState != "pending") || !headIsPrevious {
			return false
		}
		allowedStates["pending"] = true
	case "building":
		if (value.migrationState != "not_required" && value.migrationState != "pending") || !headIsPrevious {
			return false
		}
		allowedStates["pending"], allowedStates["image_ready"] = true, true
	case "migrating":
		if (value.migrationState != "pending" && value.migrationState != "succeeded") || !headIsPrevious {
			return false
		}
		allowedStates["image_ready"] = true
	case "starting_candidate":
		if (value.migrationState != "not_required" && value.migrationState != "succeeded") || !headIsPrevious {
			return false
		}
		allowedStates["image_ready"], allowedStates["running"] = true, true
	case "waiting_health":
		if (value.migrationState != "not_required" && value.migrationState != "succeeded") || !headIsPrevious {
			return false
		}
		allowedStates["running"], allowedStates["healthy"] = true, true
	case "switching_route":
		if value.migrationState != "not_required" && value.migrationState != "succeeded" {
			return false
		}
		if headIsPrevious {
			allowedStates["healthy"] = true
		} else if headIsCandidate {
			allowedStates["active"] = true
		} else {
			return false
		}
	case "draining", "succeeded":
		if (value.migrationState != "not_required" && value.migrationState != "succeeded") || !headIsCandidate {
			return false
		}
		allowedStates["active"] = true
	default:
		return false
	}
	for _, component := range value.components {
		if !allowedStates[component.state] || !componentStateIdentityValid(component, descriptionByComponent[component.name]) {
			return false
		}
	}
	return true
}

func componentStateIdentityValid(component runtimeComponent, description generatedruntime.CandidateDescription) bool {
	switch component.state {
	case "pending":
		return component.artifactID == "" && component.containerName == "" && component.containerID == ""
	case "image_ready":
		return component.artifactValid && component.artifactID != "" && component.containerName == "" && component.containerID == ""
	case "running", "healthy", "active":
		return component.artifactValid && component.artifactID != "" && component.containerName == description.ContainerName && lowerHex(component.containerID, 64)
	default:
		// In particular, starting without a durable container ID is never
		// resumable; recovery does not discover containers by mutable name.
		return false
	}
}

func failRuntimeDeployment(ctx context.Context, tx *sql.Tx, value runtimeDeployment, now string) error {
	if value.phase == "migrating" && value.migrationState == "running" {
		updated, err := tx.ExecContext(ctx, `UPDATE generated_runtime_deployments SET migration_state='failed',migration_finished_at=?,updated_at=? WHERE deployment_id=? AND phase='migrating' AND migration_state='running'`, now, now, value.deploymentID)
		if err != nil {
			return err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			return ErrRecoveryConflict
		}
	}
	for _, component := range value.components {
		switch component.state {
		case "pending", "image_ready", "starting", "running", "healthy", "active", "draining":
		default:
			continue
		}
		updated, err := tx.ExecContext(ctx, `UPDATE generated_runtime_components SET state='failed',diagnostic_code=?,updated_at=?,finished_at=? WHERE deployment_id=? AND component_name=? AND state=?`, diagnosticDaemonRestarted, now, now, value.deploymentID, component.name, component.state)
		if err != nil {
			return err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			return ErrRecoveryConflict
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE generated_runtime_deployments SET phase='failed',diagnostic_code=?,updated_at=?,finished_at=? WHERE deployment_id=? AND phase=?`, diagnosticDaemonRestarted, now, now, value.deploymentID, value.phase)
	if err != nil {
		return err
	}
	if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
		return ErrRecoveryConflict
	}
	return nil
}

func appendJobEvent(ctx context.Context, tx *sql.Tx, now, jobID, level, phase, code, message string) error {
	var sequence, attempt int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM job_events WHERE job_id=?`, jobID).Scan(&sequence); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT attempt FROM jobs WHERE id=?`, jobID).Scan(&attempt); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message) VALUES(?,?,?,?,?,?,?,?)`, jobID, sequence, attempt, now, level, phase, code, message)
	return err
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
