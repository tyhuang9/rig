package generatedrecovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

type recoveryJobSubject struct {
	id           string
	appID        string
	status       string
	phase        string
	typeName     string
	resourceType string
}

type recoveryDeploymentSubject struct {
	id           string
	appID        string
	jobID        string
	status       string
	strategy     string
	mutable      bool
	jobType      string
	resourceType string
	resourceID   string
	jobStatus    string
	jobPhase     string
}

type recoveryCensus struct {
	jobs        map[string]recoveryJobSubject
	deployments map[string]recoveryDeploymentSubject
	runtimes    []runtimeDeployment
	invalidLink bool
}

type recoveryAuthority struct {
	jobs map[string]deploymentbinding.Classification
}

// RecoverRuntime classifies the complete deployment recovery census before
// applying any startup mutation. Authority reads, deployment recovery, job
// recovery, stale retirement, and events share one writer transaction.
func RecoverRuntime(ctx context.Context, db *sql.DB, now time.Time, bindings *deploymentbinding.Repository) (RuntimeResult, error) {
	if ctx == nil || db == nil || now.IsZero() || bindings == nil || !bindings.UsesDatabase(db) {
		return RuntimeResult{}, ErrInvalidRecovery
	}
	if err := ctx.Err(); err != nil {
		return RuntimeResult{}, fmt.Errorf("%w: %w", deploymentbinding.ErrCanceled, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	defer tx.Rollback()
	if err := reserveRecoveryWriter(ctx, tx); err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	census, err := loadRecoveryCensus(ctx, tx)
	if err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	authority, err := classifyRecoveryAuthority(ctx, tx, bindings, census)
	if err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	formattedNow := now.UTC().Format(time.RFC3339Nano)
	deployments, err := recoverDeploymentsTx(ctx, tx, formattedNow, census, authority)
	if err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	jobs, err := recoverJobsTx(ctx, tx, formattedNow, authority)
	if err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return RuntimeResult{}, fmt.Errorf("%w: %w", deploymentbinding.ErrCanceled, err)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeResult{}, normalizeRecoveryError(ctx, err)
	}
	return RuntimeResult{Deployments: deployments, Jobs: jobs}, nil
}

func reserveRecoveryWriter(ctx context.Context, tx *sql.Tx) error {
	if ctx == nil || tx == nil {
		return ErrInvalidRecovery
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET id=id WHERE 0`)
	return err
}

func loadRecoveryCensus(ctx context.Context, tx *sql.Tx) (*recoveryCensus, error) {
	result := &recoveryCensus{
		jobs:        make(map[string]recoveryJobSubject),
		deployments: make(map[string]recoveryDeploymentSubject),
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,resource_id,status,phase,type,resource_type
		FROM jobs
		WHERE type='deploy' AND resource_type='application'
		  AND status IN ('queued','assigned','running','waiting_external','waiting_user')
		ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var value recoveryJobSubject
		if err := rows.Scan(&value.id, &value.appID, &value.status, &value.phase, &value.typeName, &value.resourceType); err != nil {
			_ = rows.Close()
			return nil, err
		}
		result.addJob(value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, deploymentRecoverySubjectQuery+`
		WHERE d.status IN ('preparing','applying','waiting_health') ORDER BY d.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		value, err := scanRecoveryDeploymentSubject(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		value.mutable = true
		result.addDeployment(value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	result.runtimes, err = loadRuntimeDeployments(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, runtime := range result.runtimes {
		deployment, found := result.deployments[runtime.deploymentID]
		if !found {
			deployment, err = loadRecoveryDeploymentSubject(ctx, tx, runtime.deploymentID)
			if err != nil {
				return nil, err
			}
			result.addDeployment(deployment)
		}
		if deployment.appID != runtime.appID {
			result.invalidLink = true
		}
	}
	return result, nil
}

const deploymentRecoverySubjectQuery = `SELECT
	d.id,d.app_id,COALESCE(d.job_id,''),d.status,d.runtime_strategy,
	COALESCE(j.type,''),COALESCE(j.resource_type,''),COALESCE(j.resource_id,''),
	COALESCE(j.status,''),COALESCE(j.phase,'')
	FROM deployments d LEFT JOIN jobs j ON j.id=d.job_id `

type recoverySubjectScanner interface {
	Scan(...any) error
}

func scanRecoveryDeploymentSubject(scanner recoverySubjectScanner) (recoveryDeploymentSubject, error) {
	var value recoveryDeploymentSubject
	err := scanner.Scan(&value.id, &value.appID, &value.jobID, &value.status, &value.strategy,
		&value.jobType, &value.resourceType, &value.resourceID, &value.jobStatus, &value.jobPhase)
	return value, err
}

func loadRecoveryDeploymentSubject(ctx context.Context, tx *sql.Tx, deploymentID string) (recoveryDeploymentSubject, error) {
	value, err := scanRecoveryDeploymentSubject(tx.QueryRowContext(ctx, deploymentRecoverySubjectQuery+`WHERE d.id=?`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return recoveryDeploymentSubject{id: deploymentID}, nil
	}
	return value, err
}

func (c *recoveryCensus) addJob(value recoveryJobSubject) {
	if value.id == "" || value.appID == "" || value.typeName != "deploy" || value.resourceType != "application" {
		c.invalidLink = true
	}
	if existing, found := c.jobs[value.id]; found && (existing.appID != value.appID || existing.typeName != value.typeName || existing.resourceType != value.resourceType) {
		c.invalidLink = true
		return
	}
	c.jobs[value.id] = value
}

func (c *recoveryCensus) addDeployment(value recoveryDeploymentSubject) {
	if value.id == "" || value.appID == "" || value.jobID == "" || value.jobType != "deploy" ||
		value.resourceType != "application" || value.resourceID != value.appID {
		c.invalidLink = true
	}
	if existing, found := c.deployments[value.id]; found {
		if existing.appID != value.appID || existing.jobID != value.jobID {
			c.invalidLink = true
			return
		}
		value.mutable = value.mutable || existing.mutable
	}
	c.deployments[value.id] = value
	c.addJob(recoveryJobSubject{
		id: value.jobID, appID: value.resourceID, status: value.jobStatus, phase: value.jobPhase,
		typeName: value.jobType, resourceType: value.resourceType,
	})
}

func (c *recoveryCensus) hasDeploymentWork() bool {
	return c != nil && (len(c.jobs) != 0 || len(c.deployments) != 0 || len(c.runtimes) != 0 || c.invalidLink)
}

func classifyRecoveryAuthority(ctx context.Context, tx *sql.Tx, bindings *deploymentbinding.Repository, census *recoveryCensus) (*recoveryAuthority, error) {
	if census == nil || census.invalidLink {
		return nil, deploymentbinding.ErrInvalidBinding
	}
	jobIDs := sortedRecoveryJobIDs(census.jobs)
	result := &recoveryAuthority{jobs: make(map[string]deploymentbinding.Classification, len(jobIDs))}
	for _, jobID := range jobIDs {
		subject := census.jobs[jobID]
		binding, err := bindings.ReadTx(ctx, tx, jobID)
		if err != nil {
			return nil, err
		}
		if binding.JobID() != jobID || binding.ApplicationID() != subject.appID {
			return nil, deploymentbinding.ErrInvalidBinding
		}
		classification, err := bindings.CheckTx(ctx, tx, binding)
		if err != nil {
			return nil, err
		}
		if classification != deploymentbinding.ClassificationCurrent && classification != deploymentbinding.ClassificationStale {
			return nil, deploymentbinding.ErrInvalidConfiguration
		}
		result.jobs[jobID] = classification
	}
	for deploymentID, deployment := range census.deployments {
		if _, found := result.jobs[deployment.jobID]; !found || deploymentID == "" {
			return nil, deploymentbinding.ErrInvalidBinding
		}
	}
	return result, nil
}

func recoverDeploymentsTx(ctx context.Context, tx *sql.Tx, now string, census *recoveryCensus, authority *recoveryAuthority) (DeploymentResult, error) {
	if census == nil || authority == nil {
		return DeploymentResult{}, ErrInvalidRecovery
	}
	currentRuntimes := make([]runtimeDeployment, 0, len(census.runtimes))
	retained := make(map[string]struct{})
	for _, runtime := range census.runtimes {
		classification, err := census.deploymentClassification(authority, runtime.deploymentID)
		if err != nil {
			return DeploymentResult{}, err
		}
		if classification == deploymentbinding.ClassificationStale {
			retained[runtime.deploymentID] = struct{}{}
			continue
		}
		currentRuntimes = append(currentRuntimes, runtime)
	}
	safe, err := resumableDeployments(ctx, tx, currentRuntimes)
	if err != nil {
		return DeploymentResult{}, err
	}
	result := DeploymentResult{PreservedGenerated: len(safe)}
	uncertainCommitted := make(map[string]struct{})
	for _, runtime := range currentRuntimes {
		if _, preserved := safe[runtime.deploymentID]; preserved {
			continue
		}
		if runtime.phase == "succeeded" {
			uncertainCommitted[runtime.deploymentID] = struct{}{}
			result.PausedGenerated++
			continue
		}
		if err := failRuntimeDeployment(ctx, tx, runtime, now); err != nil {
			return DeploymentResult{}, err
		}
		result.FailedGenerated++
	}

	deploymentIDs := sortedRecoveryDeploymentIDs(census.deployments)
	for _, deploymentID := range deploymentIDs {
		deployment := census.deployments[deploymentID]
		if !deployment.mutable {
			continue
		}
		classification, err := census.deploymentClassification(authority, deploymentID)
		if err != nil {
			return DeploymentResult{}, err
		}
		if classification == deploymentbinding.ClassificationStale {
			retained[deploymentID] = struct{}{}
			continue
		}
		if _, preserved := safe[deploymentID]; preserved {
			continue
		}
		if _, uncertain := uncertainCommitted[deploymentID]; uncertain {
			continue
		}
		updated, err := tx.ExecContext(ctx, `UPDATE deployments SET status='failed',finished_at=?,diagnostic_code=?,failure_code=?,failure_summary=? WHERE id=? AND status IN ('preparing','applying','waiting_health')`, now, diagnosticDaemonRestarted, diagnosticDaemonRestarted, restartSummary, deploymentID)
		if err != nil {
			return DeploymentResult{}, err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			return DeploymentResult{}, ErrRecoveryConflict
		}
		if deployment.strategy != "generated_node" {
			result.FailedOther++
		}
	}
	result.RetainedStale = len(retained)
	return result, nil
}

func recoverJobsTx(ctx context.Context, tx *sql.Tx, now string, authority *recoveryAuthority) (JobResult, error) {
	result := JobResult{}
	if authority != nil {
		jobIDs := make([]string, 0, len(authority.jobs))
		for jobID, classification := range authority.jobs {
			if classification == deploymentbinding.ClassificationStale {
				jobIDs = append(jobIDs, jobID)
			}
		}
		sort.Strings(jobIDs)
		for _, jobID := range jobIDs {
			var status, phase string
			err := tx.QueryRowContext(ctx, `SELECT status,phase FROM jobs WHERE id=?`, jobID).Scan(&status, &phase)
			if errors.Is(err, sql.ErrNoRows) {
				return JobResult{}, deploymentbinding.ErrInvalidBinding
			}
			if err != nil {
				return JobResult{}, err
			}
			if !nonterminalDeploymentJobStatus(status) {
				continue
			}
			updated, err := tx.ExecContext(ctx, `UPDATE jobs SET
				status='needs_attention',phase='needs_attention',pause_disposition=NULL,
				error_code=?,error_detail=?,updated_at=?,finished_at=?
				WHERE id=? AND status=? AND phase=? AND type='deploy' AND resource_type='application'`,
				deploymentLifecycleStale, deploymentStaleSummary, now, now, jobID, status, phase)
			if err != nil {
				return JobResult{}, err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				return JobResult{}, ErrRecoveryConflict
			}
			if err := appendJobEvent(ctx, tx, now, jobID, "error", "needs_attention", deploymentLifecycleStale, deploymentStaleSummary); err != nil {
				return JobResult{}, err
			}
			result.RetiredStale++
		}
	}

	runtimes, err := loadRuntimeDeployments(ctx, tx)
	if err != nil {
		return JobResult{}, err
	}
	currentRuntimes := make([]runtimeDeployment, 0, len(runtimes))
	for _, runtime := range runtimes {
		if authority == nil {
			return JobResult{}, ErrGuardedRecoveryRequired
		}
		classification, found, err := runtimeClassification(ctx, tx, authority, runtime)
		if err != nil {
			return JobResult{}, err
		}
		if !found {
			return JobResult{}, deploymentbinding.ErrInvalidBinding
		}
		if classification == deploymentbinding.ClassificationStale {
			continue
		}
		currentRuntimes = append(currentRuntimes, runtime)
	}
	safe, err := resumableDeployments(ctx, tx, currentRuntimes)
	if err != nil {
		return JobResult{}, err
	}
	safeJobs := make(map[string]struct{}, len(safe))
	for _, value := range safe {
		if value.jobStatus != "queued" {
			safeJobs[value.jobID] = struct{}{}
		}
	}
	uncertainJobs := make(map[string]struct{})
	for _, runtime := range currentRuntimes {
		if runtime.phase != "succeeded" {
			continue
		}
		if _, preserved := safe[runtime.deploymentID]; preserved {
			continue
		}
		var jobID string
		err := tx.QueryRowContext(ctx, `SELECT d.job_id FROM deployments d JOIN jobs j ON j.id=d.job_id
			WHERE d.id=? AND d.app_id=? AND d.status IN ('preparing','applying','waiting_health','succeeded')
			AND j.type='deploy' AND j.resource_type='application' AND j.resource_id=d.app_id`,
			runtime.deploymentID, runtime.appID).Scan(&jobID)
		if errors.Is(err, sql.ErrNoRows) {
			return JobResult{}, deploymentbinding.ErrInvalidBinding
		}
		if err != nil {
			return JobResult{}, err
		}
		if classification, found := authority.jobs[jobID]; !found || classification != deploymentbinding.ClassificationCurrent {
			return JobResult{}, deploymentbinding.ErrInvalidBinding
		}
		uncertainJobs[jobID] = struct{}{}
	}

	rows, err := tx.QueryContext(ctx, `SELECT id,status,phase,type,resource_type FROM jobs WHERE status IN ('assigned','running','waiting_external') ORDER BY created_at,id`)
	if err != nil {
		return JobResult{}, err
	}
	type activeJob struct{ id, status, phase, typeName, resourceType string }
	var active []activeJob
	for rows.Next() {
		var value activeJob
		if err := rows.Scan(&value.id, &value.status, &value.phase, &value.typeName, &value.resourceType); err != nil {
			_ = rows.Close()
			return JobResult{}, err
		}
		active = append(active, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return JobResult{}, err
	}
	if err := rows.Close(); err != nil {
		return JobResult{}, err
	}
	for _, job := range active {
		if job.typeName == "deploy" && job.resourceType == "application" {
			if authority == nil {
				return JobResult{}, ErrGuardedRecoveryRequired
			}
			classification, found := authority.jobs[job.id]
			if !found || classification != deploymentbinding.ClassificationCurrent {
				return JobResult{}, deploymentbinding.ErrInvalidBinding
			}
		}
		if _, requeue := safeJobs[job.id]; requeue {
			updated, err := tx.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='queued',progress_percent=0,checkpoint_json='{}',pause_disposition=NULL,error_code=NULL,error_detail=NULL,started_at=NULL,finished_at=NULL,updated_at=? WHERE id=? AND status=? AND phase=?`, now, job.id, job.status, job.phase)
			if err != nil {
				return JobResult{}, err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				return JobResult{}, ErrRecoveryConflict
			}
			if err := appendJobEvent(ctx, tx, now, job.id, "warn", "queued", diagnosticDaemonRestarted, requeuedJobSummary); err != nil {
				return JobResult{}, err
			}
			result.RequeuedGenerated++
			continue
		}
		if _, uncertain := uncertainJobs[job.id]; uncertain {
			updated, err := tx.ExecContext(ctx, `UPDATE jobs SET status='waiting_user',phase=?,checkpoint_json=?,pause_disposition=?,error_code=NULL,error_detail=NULL,updated_at=? WHERE id=? AND status=? AND phase=?`,
				routeReconciliationPause, `{"phase":"route_reconciliation_required"}`, routeReconciliationPause, now, job.id, job.status, job.phase)
			if err != nil {
				return JobResult{}, err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				return JobResult{}, ErrRecoveryConflict
			}
			if err := appendJobEvent(ctx, tx, now, job.id, "warn", routeReconciliationPause, diagnosticDaemonRestarted, "Committed route requires reconciliation after hostd restarted"); err != nil {
				return JobResult{}, err
			}
			result.PausedGenerated++
			continue
		}
		updated, err := tx.ExecContext(ctx, `UPDATE jobs SET status='interrupted',phase='interrupted',pause_disposition=NULL,error_code=?,error_detail=?,updated_at=?,finished_at=? WHERE id=? AND status=? AND phase=?`, diagnosticDaemonRestarted, interruptedJobSummary, now, now, job.id, job.status, job.phase)
		if err != nil {
			return JobResult{}, err
		}
		if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
			return JobResult{}, ErrRecoveryConflict
		}
		if err := appendJobEvent(ctx, tx, now, job.id, "error", "interrupted", diagnosticDaemonRestarted, interruptedJobSummary); err != nil {
			return JobResult{}, err
		}
		result.Interrupted++
	}
	uncertainJobIDs := make([]string, 0, len(uncertainJobs))
	for jobID := range uncertainJobs {
		uncertainJobIDs = append(uncertainJobIDs, jobID)
	}
	sort.Strings(uncertainJobIDs)
	for _, jobID := range uncertainJobIDs {
		updated, err := tx.ExecContext(ctx, `UPDATE jobs SET phase=?,checkpoint_json=?,pause_disposition=?,error_code=NULL,error_detail=NULL,updated_at=?
			WHERE id=? AND status='waiting_user' AND (phase<>? OR COALESCE(pause_disposition,'')<>?)`,
			routeReconciliationPause, `{"phase":"route_reconciliation_required"}`, routeReconciliationPause, now,
			jobID, routeReconciliationPause, routeReconciliationPause)
		if err != nil {
			return JobResult{}, err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return JobResult{}, err
		}
		if changed == 1 {
			if err := appendJobEvent(ctx, tx, now, jobID, "warn", routeReconciliationPause, diagnosticDaemonRestarted, "Committed route requires reconciliation after hostd restarted"); err != nil {
				return JobResult{}, err
			}
			result.PausedGenerated++
		}
	}
	return result, nil
}

func runtimeClassification(ctx context.Context, tx *sql.Tx, authority *recoveryAuthority, runtime runtimeDeployment) (deploymentbinding.Classification, bool, error) {
	var jobID, appID, jobType, resourceType, resourceID string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(d.job_id,''),d.app_id,COALESCE(j.type,''),COALESCE(j.resource_type,''),COALESCE(j.resource_id,'')
		FROM deployments d LEFT JOIN jobs j ON j.id=d.job_id WHERE d.id=?`, runtime.deploymentID).
		Scan(&jobID, &appID, &jobType, &resourceType, &resourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if jobID == "" || appID != runtime.appID || jobType != "deploy" || resourceType != "application" || resourceID != appID {
		return 0, false, deploymentbinding.ErrInvalidBinding
	}
	classification, found := authority.jobs[jobID]
	return classification, found, nil
}

func (c *recoveryCensus) deploymentClassification(authority *recoveryAuthority, deploymentID string) (deploymentbinding.Classification, error) {
	deployment, found := c.deployments[deploymentID]
	if !found {
		return 0, deploymentbinding.ErrInvalidBinding
	}
	classification, found := authority.jobs[deployment.jobID]
	if !found {
		return 0, deploymentbinding.ErrInvalidBinding
	}
	if classification != deploymentbinding.ClassificationCurrent && classification != deploymentbinding.ClassificationStale {
		return 0, deploymentbinding.ErrInvalidConfiguration
	}
	return classification, nil
}

func nonterminalDeploymentJobStatus(status string) bool {
	switch status {
	case "queued", "assigned", "running", "waiting_external", "waiting_user":
		return true
	default:
		return false
	}
}

func sortedRecoveryJobIDs(values map[string]recoveryJobSubject) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func sortedRecoveryDeploymentIDs(values map[string]recoveryDeploymentSubject) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func normalizeRecoveryError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("%w: %w", deploymentbinding.ErrCanceled, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", deploymentbinding.ErrCanceled, err)
	}
	for _, known := range []error{
		ErrInvalidRecovery, ErrRecoveryConflict, ErrGuardedRecoveryRequired,
		deploymentbinding.ErrBindingNotFound, deploymentbinding.ErrInvalidBinding,
		deploymentbinding.ErrLifecycleUnresolved, deploymentbinding.ErrUnavailable,
		deploymentbinding.ErrInvalidConfiguration, deploymentbinding.ErrCanceled,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	return fmt.Errorf("%w: %v", deploymentbinding.ErrUnavailable, err)
}
