package deployments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedrecovery"
)

type Status string
type RuntimeStrategy string

const (
	Preparing      Status = "preparing"
	Applying       Status = "applying"
	WaitingHealth  Status = "waiting_health"
	Succeeded      Status = "succeeded"
	Failed         Status = "failed"
	Cancelled      Status = "cancelled"
	NeedsAttention Status = "needs_attention"
)

const (
	RuntimeCompose       RuntimeStrategy = "compose"
	RuntimeGeneratedNode RuntimeStrategy = "generated_node"
)

var (
	ErrNotFound           = errors.New("deployment not found")
	ErrInvalidTransition  = errors.New("invalid deployment transition")
	ErrApprovalRequired   = errors.New("runtime approval required")
	ErrApprovalInUse      = errors.New("runtime approval is in use")
	ErrInvalidDeployment  = errors.New("invalid deployment")
	ErrRejectedCapability = errors.New("runtime capability rejected")
)

type Deployment struct {
	ID                                string          `json:"id"`
	AppID                             string          `json:"appId"`
	ReleaseID                         string          `json:"releaseId,omitempty"`
	JobID                             string          `json:"jobId"`
	MachineID                         string          `json:"machineId,omitempty"`
	Status                            Status          `json:"status"`
	ConfigurationMode                 string          `json:"configurationMode"`
	ActualConfigurationRevisionID     string          `json:"actualConfigurationRevisionId,omitempty"`
	ActualConfigurationRevisionNumber int64           `json:"actualConfigurationRevisionNumber"`
	RuntimeStrategy                   RuntimeStrategy `json:"runtimeStrategy"`
	DeploymentPlanRevisionID          string          `json:"deploymentPlanRevisionId,omitempty"`
	DeploymentPlanRevisionNumber      int64           `json:"deploymentPlanRevisionNumber"`
	ProvenanceInitialized             bool            `json:"-"`
	StartedAt                         time.Time       `json:"startedAt,omitempty"`
	FinishedAt                        time.Time       `json:"finishedAt,omitempty"`
	DiagnosticCode                    string          `json:"diagnosticCode,omitempty"`
	FailureSummary                    string          `json:"failureSummary,omitempty"`
	Findings                          []Finding       `json:"findings"`
}

type Release struct {
	ID                           string    `json:"id"`
	AppID                        string    `json:"appId"`
	SourceProvider               string    `json:"sourceProvider"`
	RepositoryID                 int64     `json:"repositoryId,omitempty"`
	RepositoryOwner              string    `json:"repositoryOwner,omitempty"`
	RepositoryName               string    `json:"repositoryName,omitempty"`
	TrackedRef                   string    `json:"trackedRef,omitempty"`
	ResolvedSHA                  string    `json:"resolvedSha,omitempty"`
	SourceCommitSHA              string    `json:"sourceCommitSha,omitempty"`
	SourceBranch                 string    `json:"sourceBranch,omitempty"`
	ComposePath                  string    `json:"composePath,omitempty"`
	ArchiveSHA256                string    `json:"archiveSha256,omitempty"`
	WorkspaceState               string    `json:"workspaceState,omitempty"`
	ConfigurationRevisionID      string    `json:"configurationRevisionId,omitempty"`
	ConfigurationRevisionNumber  int64     `json:"configurationRevisionNumber"`
	DeploymentPlanRevisionID     string    `json:"deploymentPlanRevisionId,omitempty"`
	DeploymentPlanRevisionNumber int64     `json:"deploymentPlanRevisionNumber"`
	CreatedAt                    time.Time `json:"createdAt"`
}

type Finding struct {
	ID            string    `json:"id"`
	DeploymentID  string    `json:"deploymentId"`
	PolicyVersion string    `json:"policyVersion"`
	Capability    string    `json:"capability"`
	Scope         string    `json:"scope"`
	Fingerprint   string    `json:"fingerprint"`
	Disposition   string    `json:"disposition"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Approval struct {
	ID            string    `json:"id"`
	AppID         string    `json:"appId"`
	PolicyVersion string    `json:"policyVersion"`
	Capability    string    `json:"capability"`
	Scope         string    `json:"scope"`
	Fingerprint   string    `json:"fingerprint"`
	GrantedBy     string    `json:"grantedBy"`
	GrantedAt     time.Time `json:"grantedAt"`
	RevokedBy     string    `json:"revokedBy,omitempty"`
	RevokedAt     time.Time `json:"revokedAt,omitempty"`
}

type Repository struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Repository { return &Repository{db: db, now: time.Now} }

// GetOrCreateByJob atomically establishes the one-to-one durable linkage
// between a deployment job and its deployment history row. Replays return the
// original row only when the app and configuration mode still match.
func (r *Repository) GetOrCreateByJob(ctx context.Context, appID, jobID, configurationMode string) (Deployment, bool, error) {
	if r == nil || r.db == nil || uuid.Validate(appID) != nil || uuid.Validate(jobID) != nil || (configurationMode != "current" && configurationMode != "original") {
		return Deployment{}, false, ErrInvalidDeployment
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, false, err
	}
	defer tx.Rollback()

	var existingID, existingAppID, existingMode string
	lookupErr := tx.QueryRowContext(ctx, `SELECT id,app_id,configuration_mode FROM deployments WHERE job_id=?`, jobID).Scan(&existingID, &existingAppID, &existingMode)
	if lookupErr == nil {
		if existingAppID != appID || existingMode != configurationMode {
			return Deployment{}, false, ErrInvalidDeployment
		}
		if err := tx.Commit(); err != nil {
			return Deployment{}, false, err
		}
		deployment, err := r.Get(ctx, appID, existingID)
		return deployment, false, err
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return Deployment{}, false, lookupErr
	}

	id := uuid.NewString()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO deployments(id,app_id,job_id,machine_id,status,configuration_mode) SELECT ?,?,?,active_machine_id,'preparing',? FROM applications WHERE id=? AND archived_at IS NULL`, id, appID, jobID, configurationMode, appID)
	if err != nil {
		return Deployment{}, false, err
	}
	createdCount, err := result.RowsAffected()
	if err != nil {
		return Deployment{}, false, err
	}
	if createdCount != 0 && createdCount != 1 {
		return Deployment{}, false, ErrInvalidDeployment
	}

	var persistedID, persistedMode string
	if err := tx.QueryRowContext(ctx, `SELECT id,configuration_mode FROM deployments WHERE job_id=? AND app_id=?`, jobID, appID).Scan(&persistedID, &persistedMode); errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, ErrInvalidDeployment
	} else if err != nil {
		return Deployment{}, false, err
	}
	if persistedMode != configurationMode {
		return Deployment{}, false, ErrInvalidDeployment
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, false, err
	}
	deployment, err := r.Get(ctx, appID, persistedID)
	return deployment, createdCount == 1, err
}

func (r *Repository) Create(ctx context.Context, appID, jobID, configurationMode string) (Deployment, error) {
	if r == nil || r.db == nil || uuid.Validate(appID) != nil || uuid.Validate(jobID) != nil || (configurationMode != "current" && configurationMode != "original") {
		return Deployment{}, ErrInvalidDeployment
	}
	id := uuid.NewString()
	_, err := r.db.ExecContext(ctx, `INSERT INTO deployments(id,app_id,job_id,machine_id,status,configuration_mode) SELECT ?,?,?,active_machine_id,'preparing',? FROM applications WHERE id=? AND archived_at IS NULL`, id, appID, jobID, configurationMode, appID)
	if err != nil {
		return Deployment{}, err
	}
	return r.Get(ctx, appID, id)
}

// Initialize resolves immutable release and actual configuration provenance.
// The migration permits this exactly once while the deployment is preparing.
func (r *Repository) Initialize(ctx context.Context, appID, deploymentID, releaseID, configurationID string, configurationNumber int64) (Deployment, error) {
	return r.InitializeRuntime(ctx, appID, deploymentID, releaseID, configurationID, configurationNumber, RuntimeCompose, "", 0)
}

// InitializeRuntime resolves immutable release, configuration, runtime
// strategy, and accepted-plan provenance in one transaction. Compose callers
// may omit plan provenance; when a release pins a Compose plan it is inherited.
func (r *Repository) InitializeRuntime(ctx context.Context, appID, deploymentID, releaseID, configurationID string, configurationNumber int64, strategy RuntimeStrategy, planRevisionID string, planRevisionNumber int64) (Deployment, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(appID) != nil || uuid.Validate(deploymentID) != nil ||
		(releaseID != "" && !validReleaseID(releaseID)) || configurationNumber < 0 || (configurationNumber == 0) != (configurationID == "") ||
		(strategy != RuntimeCompose && strategy != RuntimeGeneratedNode) || (planRevisionNumber == 0) != (planRevisionID == "") ||
		(planRevisionID != "" && (uuid.Validate(planRevisionID) != nil || planRevisionNumber < 1)) {
		return Deployment{}, ErrInvalidDeployment
	}
	if strategy == RuntimeGeneratedNode && (releaseID == "" || planRevisionID == "") {
		return Deployment{}, ErrInvalidDeployment
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback()
	var currentStatus Status
	var initialized bool
	if err := tx.QueryRowContext(ctx, `SELECT status,provenance_initialized FROM deployments WHERE id=? AND app_id=?`, deploymentID, appID).Scan(&currentStatus, &initialized); errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	} else if err != nil {
		return Deployment{}, err
	}
	if currentStatus != Preparing || initialized {
		return Deployment{}, ErrInvalidTransition
	}
	if releaseID == "" {
		if strategy != RuntimeCompose || planRevisionID != "" {
			return Deployment{}, ErrInvalidDeployment
		}
	} else {
		var releasePlanID, releasePlanStrategy string
		var releasePlanNumber int64
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(r.deployment_plan_revision_id,''),COALESCE(r.deployment_plan_revision_number,0),COALESCE(p.strategy,'')
			FROM releases r LEFT JOIN deployment_plan_revisions p ON p.id=r.deployment_plan_revision_id AND p.app_id=r.app_id AND p.revision_number=r.deployment_plan_revision_number
			WHERE r.id=? AND r.app_id=? AND (r.workspace_state='ready' OR r.workspace_state IS NULL)`, releaseID, appID).Scan(&releasePlanID, &releasePlanNumber, &releasePlanStrategy)
		if errors.Is(err, sql.ErrNoRows) {
			return Deployment{}, ErrInvalidDeployment
		}
		if err != nil {
			return Deployment{}, err
		}
		if releasePlanID != "" {
			if releasePlanStrategy != string(strategy) {
				return Deployment{}, ErrInvalidDeployment
			}
			if planRevisionID == "" && strategy == RuntimeCompose {
				planRevisionID, planRevisionNumber = releasePlanID, releasePlanNumber
			}
			if planRevisionID != releasePlanID || planRevisionNumber != releasePlanNumber {
				return Deployment{}, ErrInvalidDeployment
			}
		} else if planRevisionID != "" {
			return Deployment{}, ErrInvalidDeployment
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE deployments SET release_id=?,actual_configuration_revision_id=?,actual_configuration_revision_number=?,runtime_strategy=?,deployment_plan_revision_id=?,deployment_plan_revision_number=?,provenance_initialized=1 WHERE id=? AND app_id=? AND status='preparing' AND provenance_initialized=0`, nullable(releaseID), nullable(configurationID), configurationNumber, strategy, nullable(planRevisionID), nullableInt(planRevisionNumber), deploymentID, appID)
	if err != nil {
		return Deployment{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return Deployment{}, ErrInvalidTransition
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, err
	}
	return r.Get(ctx, appID, deploymentID)
}

func (r *Repository) Transition(ctx context.Context, appID, deploymentID string, next Status, diagnosticCode string) (Deployment, error) {
	if !knownStatus(next) || !knownDiagnostic(diagnosticCode) {
		return Deployment{}, ErrInvalidTransition
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback()
	var current Status
	if err := tx.QueryRowContext(ctx, `SELECT status FROM deployments WHERE id=? AND app_id=?`, deploymentID, appID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	} else if err != nil {
		return Deployment{}, err
	}
	if !allowedTransition(current, next) {
		return Deployment{}, ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	finished := any(nil)
	if terminal(next) {
		finished = now
	}
	summary := diagnosticSummary(diagnosticCode)
	result, err := tx.ExecContext(ctx, `UPDATE deployments SET status=?,started_at=CASE WHEN ? IN ('applying','waiting_health','succeeded') THEN COALESCE(started_at,?) ELSE started_at END,finished_at=?,diagnostic_code=?,failure_code=?,failure_summary=? WHERE id=? AND app_id=? AND status=?`, next, next, now, finished, nullable(diagnosticCode), nullable(diagnosticCode), nullable(summary), deploymentID, appID, current)
	if err != nil {
		return Deployment{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return Deployment{}, ErrInvalidTransition
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, err
	}
	return r.Get(ctx, appID, deploymentID)
}

func allowedTransition(current, next Status) bool {
	allowed := map[Status]map[Status]bool{
		Preparing:      {NeedsAttention: true, Applying: true, Failed: true, Cancelled: true},
		NeedsAttention: {Preparing: true, Applying: true, Failed: true, Cancelled: true},
		Applying:       {WaitingHealth: true, Succeeded: true, Failed: true, Cancelled: true, NeedsAttention: true},
		WaitingHealth:  {Succeeded: true, Failed: true, Cancelled: true, NeedsAttention: true},
	}
	return allowed[current][next]
}

func knownStatus(value Status) bool {
	switch value {
	case Preparing, Applying, WaitingHealth, Succeeded, Failed, Cancelled, NeedsAttention:
		return true
	default:
		return false
	}
}

func terminal(value Status) bool {
	return value == Succeeded || value == Failed || value == Cancelled
}

func knownDiagnostic(value string) bool {
	switch value {
	case "", "daemon_restarted", "runtime_unavailable", "process_termination_failed", "compose_invalid", "compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated", "policy_rejected", "approval_required", "apply_failed", "compose_apply_failed", "compose_apply_timeout", "compose_apply_output_truncated", "health_failed", "cancelled", "internal_error", "invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full", "provider_unavailable", "configuration_unavailable":
		return true
	default:
		return false
	}
}

func diagnosticSummary(value string) string {
	switch value {
	case "daemon_restarted":
		return "Deployment interrupted because hostd restarted"
	case "runtime_unavailable":
		return "Container runtime is unavailable"
	case "process_termination_failed":
		return "Runtime process termination failed"
	case "compose_invalid", "compose_config_invalid":
		return "Compose configuration is invalid"
	case "compose_config_timeout":
		return "Compose configuration check timed out"
	case "compose_config_output_truncated":
		return "Compose configuration output exceeded the allowed limit"
	case "policy_rejected":
		return "Compose configuration requests an unsupported capability"
	case "approval_required":
		return "Deployment requires administrator approval"
	case "apply_failed", "compose_apply_failed":
		return "Container runtime failed to apply the deployment"
	case "compose_apply_timeout":
		return "Container runtime apply timed out"
	case "compose_apply_output_truncated":
		return "Container runtime apply output exceeded the allowed limit"
	case "health_failed":
		return "Deployment did not become healthy"
	case "cancelled":
		return "Deployment was cancelled"
	case "internal_error":
		return "Deployment failed because of an internal error"
	case "invalid_source":
		return "Application source is invalid"
	case "source_unavailable":
		return "Application source is unavailable"
	case "source_access_lost":
		return "Access to the application source was lost"
	case "source_too_large":
		return "Application source exceeds deployment limits"
	case "source_storage_full":
		return "Application source storage is full"
	case "provider_unavailable":
		return "Application source provider is unavailable"
	case "configuration_unavailable":
		return "Application configuration is unavailable"
	default:
		return ""
	}
}

func (r *Repository) Recover(ctx context.Context) error {
	_, err := generatedrecovery.RecoverDeployments(ctx, r.db, r.now().UTC())
	return err
}

func (r *Repository) Get(ctx context.Context, appID, deploymentID string) (Deployment, error) {
	deployment, err := scanDeployment(r.db.QueryRowContext(ctx, deploymentSelect+` WHERE d.id=? AND d.app_id=?`, deploymentID, appID))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	deployment.Findings, err = r.Findings(ctx, appID, deploymentID)
	return deployment, err
}

func (r *Repository) List(ctx context.Context, appID string, limit int) ([]Deployment, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, deploymentSelect+` LEFT JOIN jobs j ON j.id=d.job_id WHERE d.app_id=? ORDER BY COALESCE(d.started_at,j.created_at,d.finished_at,'') DESC,d.id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Deployment{}
	for rows.Next() {
		deployment, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, deployment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Findings, err = r.Findings(ctx, appID, result[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

const deploymentSelect = `SELECT d.id,d.app_id,COALESCE(d.release_id,''),COALESCE(d.job_id,''),COALESCE(d.machine_id,''),d.status,d.configuration_mode,COALESCE(d.actual_configuration_revision_id,''),d.actual_configuration_revision_number,d.runtime_strategy,COALESCE(d.deployment_plan_revision_id,''),COALESCE(d.deployment_plan_revision_number,0),d.provenance_initialized,COALESCE(d.started_at,''),COALESCE(d.finished_at,''),COALESCE(d.diagnostic_code,''),COALESCE(d.failure_summary,'') FROM deployments d`

type scanner interface{ Scan(...any) error }

func scanDeployment(row scanner) (Deployment, error) {
	var value Deployment
	var started, finished string
	err := row.Scan(&value.ID, &value.AppID, &value.ReleaseID, &value.JobID, &value.MachineID, &value.Status, &value.ConfigurationMode, &value.ActualConfigurationRevisionID, &value.ActualConfigurationRevisionNumber, &value.RuntimeStrategy, &value.DeploymentPlanRevisionID, &value.DeploymentPlanRevisionNumber, &value.ProvenanceInitialized, &started, &finished, &value.DiagnosticCode, &value.FailureSummary)
	value.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	value.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	return value, err
}

func (r *Repository) Releases(ctx context.Context, appID string, limit int) ([]Release, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, releaseSelect+` WHERE app_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Release{}
	for rows.Next() {
		value, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

const releaseSelect = `SELECT id,app_id,COALESCE(source_provider,''),COALESCE(repository_id,0),COALESCE(repository_owner,''),COALESCE(repository_name,''),COALESCE(tracked_ref,''),COALESCE(resolved_sha,''),source_commit_sha,source_branch,COALESCE(compose_path,''),COALESCE(archive_sha256,''),COALESCE(workspace_state,''),COALESCE(configuration_revision_id,''),configuration_revision_number,COALESCE(deployment_plan_revision_id,''),COALESCE(deployment_plan_revision_number,0),created_at FROM releases`

func scanRelease(row scanner) (Release, error) {
	var value Release
	var created string
	err := row.Scan(&value.ID, &value.AppID, &value.SourceProvider, &value.RepositoryID, &value.RepositoryOwner, &value.RepositoryName, &value.TrackedRef, &value.ResolvedSHA, &value.SourceCommitSHA, &value.SourceBranch, &value.ComposePath, &value.ArchiveSHA256, &value.WorkspaceState, &value.ConfigurationRevisionID, &value.ConfigurationRevisionNumber, &value.DeploymentPlanRevisionID, &value.DeploymentPlanRevisionNumber, &created)
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return value, err
}

// Release returns only an app-bound ready release suitable for a prior-release
// deployment request. The executor revalidates its managed workspace before use.
func (r *Repository) Release(ctx context.Context, appID, releaseID string) (Release, error) {
	if uuid.Validate(appID) != nil || !validReleaseID(releaseID) {
		return Release{}, ErrNotFound
	}
	value, err := scanRelease(r.db.QueryRowContext(ctx, releaseSelect+` WHERE app_id=? AND id=? AND workspace_state='ready'`, appID, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) Findings(ctx context.Context, appID, deploymentID string) ([]Finding, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT f.id,f.deployment_id,f.policy_version,f.capability,f.scope,f.fingerprint,f.disposition,f.created_at FROM deployment_policy_findings f JOIN deployments d ON d.id=f.deployment_id WHERE d.app_id=? AND f.deployment_id=? ORDER BY f.capability,f.scope`, appID, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Finding{}
	for rows.Next() {
		var value Finding
		var created string
		if err := rows.Scan(&value.ID, &value.DeploymentID, &value.PolicyVersion, &value.Capability, &value.Scope, &value.Fingerprint, &value.Disposition, &created); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, value)
	}
	return result, rows.Err()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func validateFindings(findings []Finding) error {
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		if len(finding.PolicyVersion) > 64 || finding.PolicyVersion == "" || len(finding.Capability) > 128 || finding.Capability == "" || len(finding.Scope) > 4096 || finding.Scope == "" || !lowerHex(finding.Fingerprint, 64) || finding.Fingerprint != findingFingerprint(finding.PolicyVersion, finding.Capability, finding.Scope) || (finding.Disposition != "allowed" && finding.Disposition != "approval_required" && finding.Disposition != "rejected") {
			return ErrInvalidDeployment
		}
		if _, exists := seen[finding.Fingerprint]; exists {
			return ErrInvalidDeployment
		}
		seen[finding.Fingerprint] = struct{}{}
	}
	return nil
}

// Gate persists the evaluated findings and, in the same transaction, rechecks
// exact active approvals before moving to applying immediately before mutation.
func (r *Repository) Gate(ctx context.Context, appID, deploymentID string, findings []Finding) error {
	if err := validateFindings(findings); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status Status
	var provenanceInitialized bool
	if err := tx.QueryRowContext(ctx, `SELECT status,provenance_initialized FROM deployments WHERE id=? AND app_id=?`, deploymentID, appID).Scan(&status, &provenanceInitialized); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if (status != Preparing && status != NeedsAttention) || !provenanceInitialized {
		return ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	sorted := append([]Finding(nil), findings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Fingerprint < sorted[j].Fingerprint })
	rejected := false
	missingApproval := false
	for _, finding := range sorted {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), deploymentID, finding.PolicyVersion, finding.Capability, finding.Scope, finding.Fingerprint, finding.Disposition, now)
		if err != nil {
			return err
		}
		if finding.Disposition == "rejected" {
			rejected = true
		}
		if finding.Disposition == "approval_required" {
			var approved int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_approvals WHERE app_id=? AND policy_version=? AND capability=? AND scope=? AND fingerprint=? AND revoked_at IS NULL`, appID, finding.PolicyVersion, finding.Capability, finding.Scope, finding.Fingerprint).Scan(&approved); err != nil {
				return err
			}
			if approved != 1 {
				missingApproval = true
			}
		}
	}
	if rejected {
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status='failed',finished_at=?,diagnostic_code='policy_rejected',failure_code='policy_rejected',failure_summary='Compose configuration requests an unsupported capability' WHERE id=? AND app_id=? AND status IN ('preparing','needs_attention')`, now, deploymentID, appID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrRejectedCapability
	}
	if missingApproval {
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status='needs_attention',diagnostic_code='approval_required',failure_code='approval_required',failure_summary='Deployment requires administrator approval' WHERE id=? AND app_id=? AND status IN ('preparing','needs_attention')`, deploymentID, appID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrApprovalRequired
	}
	result, err := tx.ExecContext(ctx, `UPDATE deployments SET status='applying',started_at=COALESCE(started_at,?),diagnostic_code=NULL,failure_code=NULL,failure_summary=NULL WHERE id=? AND app_id=? AND status IN ('preparing','needs_attention')`, now, deploymentID, appID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrInvalidTransition
	}
	return tx.Commit()
}

func (r *Repository) Grant(ctx context.Context, appID, actorID, fingerprint string) (Approval, bool, error) {
	if uuid.Validate(appID) != nil || uuid.Validate(actorID) != nil || !lowerHex(fingerprint, 64) {
		return Approval{}, false, ErrInvalidDeployment
	}
	var finding Finding
	var created string
	if err := r.db.QueryRowContext(ctx, `SELECT f.id,f.deployment_id,f.policy_version,f.capability,f.scope,f.fingerprint,f.disposition,f.created_at FROM deployment_policy_findings f JOIN deployments d ON d.id=f.deployment_id WHERE d.app_id=? AND f.fingerprint=? AND f.disposition='approval_required' ORDER BY f.created_at DESC LIMIT 1`, appID, fingerprint).Scan(&finding.ID, &finding.DeploymentID, &finding.PolicyVersion, &finding.Capability, &finding.Scope, &finding.Fingerprint, &finding.Disposition, &created); errors.Is(err, sql.ErrNoRows) {
		return Approval{}, false, ErrInvalidDeployment
	} else if err != nil {
		return Approval{}, false, err
	}
	finding.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if current, err := r.activeApproval(ctx, appID, fingerprint); err == nil {
		return current, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Approval{}, false, err
	}
	value := Approval{ID: uuid.NewString(), AppID: appID, PolicyVersion: finding.PolicyVersion, Capability: finding.Capability, Scope: finding.Scope, Fingerprint: finding.Fingerprint, GrantedBy: actorID, GrantedAt: r.now().UTC()}
	_, err := r.db.ExecContext(ctx, `INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, value.ID, value.AppID, value.PolicyVersion, value.Capability, value.Scope, value.Fingerprint, value.GrantedBy, value.GrantedAt.Format(time.RFC3339Nano))
	if err != nil {
		if current, lookupErr := r.activeApproval(ctx, appID, finding.Fingerprint); lookupErr == nil {
			return current, false, nil
		}
		return Approval{}, false, err
	}
	return value, true, nil
}

func (r *Repository) activeApproval(ctx context.Context, appID, fingerprint string) (Approval, error) {
	return scanApproval(r.db.QueryRowContext(ctx, approvalSelect+` WHERE app_id=? AND fingerprint=? AND revoked_at IS NULL`, appID, fingerprint))
}

func (r *Repository) Approvals(ctx context.Context, appID string) ([]Approval, error) {
	rows, err := r.db.QueryContext(ctx, approvalSelect+` WHERE app_id=? ORDER BY granted_at DESC,id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Approval{}
	for rows.Next() {
		value, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

const approvalSelect = `SELECT id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at,COALESCE(revoked_by,''),COALESCE(revoked_at,'') FROM runtime_approvals`

func scanApproval(row scanner) (Approval, error) {
	var value Approval
	var granted, revoked string
	err := row.Scan(&value.ID, &value.AppID, &value.PolicyVersion, &value.Capability, &value.Scope, &value.Fingerprint, &value.GrantedBy, &granted, &value.RevokedBy, &revoked)
	value.GrantedAt, _ = time.Parse(time.RFC3339Nano, granted)
	value.RevokedAt, _ = time.Parse(time.RFC3339Nano, revoked)
	return value, err
}

func (r *Repository) Revoke(ctx context.Context, appID, approvalID, actorID string) (Approval, error) {
	if uuid.Validate(actorID) != nil {
		return Approval{}, ErrInvalidDeployment
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, `UPDATE runtime_approvals SET revoked_by=?,revoked_at=? WHERE id=? AND app_id=? AND revoked_at IS NULL`, actorID, now, approvalID, appID)
	if err != nil {
		if contains(err.Error(), "approval is in use") {
			return Approval{}, ErrApprovalInUse
		}
		return Approval{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return Approval{}, ErrNotFound
	}
	return scanApproval(r.db.QueryRowContext(ctx, approvalSelect+` WHERE id=? AND app_id=?`, approvalID, appID))
}

func contains(value, part string) bool { return strings.Contains(value, part) }

func findingFingerprint(policyVersion, capability, scope string) string {
	canonical, _ := json.Marshal(struct {
		PolicyVersion string `json:"policyVersion"`
		Capability    string `json:"capability"`
		Scope         string `json:"scope"`
	}{policyVersion, capability, scope})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
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

func validReleaseID(value string) bool {
	return uuid.Validate(value) == nil || lowerHex(value, 32)
}
