// Package generatedruntimestate persists audit-safe generated runtime
// deployment, component, migration-attempt, and active-slot state. Commands,
// environment values, and runtime output never cross this boundary.
package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Phase string
type MigrationState string
type ComponentState string
type DiagnosticCode string

const (
	PhasePreflight         Phase = "preflight"
	PhaseBuilding          Phase = "building"
	PhaseMigrating         Phase = "migrating"
	PhaseStartingCandidate Phase = "starting_candidate"
	PhaseWaitingHealth     Phase = "waiting_health"
	PhaseSwitchingRoute    Phase = "switching_route"
	PhaseDraining          Phase = "draining"
	PhaseSucceeded         Phase = "succeeded"
	PhaseFailed            Phase = "failed"
	PhaseCancelled         Phase = "cancelled"
)

const (
	MigrationNotRequired MigrationState = "not_required"
	MigrationPending     MigrationState = "pending"
	MigrationRunning     MigrationState = "running"
	MigrationSucceeded   MigrationState = "succeeded"
	MigrationFailed      MigrationState = "failed"
)

const (
	ComponentPending    ComponentState = "pending"
	ComponentImageReady ComponentState = "image_ready"
	ComponentStarting   ComponentState = "starting"
	ComponentRunning    ComponentState = "running"
	ComponentHealthy    ComponentState = "healthy"
	ComponentActive     ComponentState = "active"
	ComponentDraining   ComponentState = "draining"
	ComponentStopped    ComponentState = "stopped"
	ComponentFailed     ComponentState = "failed"
)

const (
	DiagnosticInsufficientReplacementCapacity DiagnosticCode = "insufficient_replacement_capacity"
	DiagnosticPlanReviewRequired              DiagnosticCode = "deployment_plan_review_required"
	DiagnosticMigrationFailed                 DiagnosticCode = "migration_failed"
	DiagnosticStartFailed                     DiagnosticCode = "start_failed"
	DiagnosticHealthFailed                    DiagnosticCode = "health_failed"
	DiagnosticRouteSwitchFailed               DiagnosticCode = "route_switch_failed"
	DiagnosticRuntimeUnavailable              DiagnosticCode = "runtime_unavailable"
	DiagnosticDaemonRestarted                 DiagnosticCode = "daemon_restarted"
	DiagnosticCancelled                       DiagnosticCode = "cancelled"
	DiagnosticInternalError                   DiagnosticCode = "internal_error"
)

var (
	ErrInvalidState              = errors.New("invalid generated runtime state")
	ErrNotFound                  = errors.New("generated runtime state not found")
	ErrInvalidTransition         = errors.New("invalid generated runtime state transition")
	ErrConflict                  = errors.New("generated runtime active head conflict")
	ErrMigrationApprovalRequired = errors.New("generated runtime migration approval required")
	ErrDeploymentInProgress      = errors.New("generated runtime deployment already in progress")
)

type Deployment struct {
	DeploymentID                 string
	AppID                        string
	ReleaseID                    string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	CandidateSlot                string
	PreviousActiveDeploymentID   string
	PreviousActiveSlot           string
	Phase                        Phase
	MigrationState               MigrationState
	DiagnosticCode               DiagnosticCode
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	MigrationStartedAt           time.Time
	MigrationFinishedAt          time.Time
	FinishedAt                   time.Time
	Components                   []Component
}

type Component struct {
	DeploymentID    string
	Name            string
	Slot            string
	ImageArtifactID string
	ContainerName   string
	ContainerID     string
	State           ComponentState
	DiagnosticCode  DiagnosticCode
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      time.Time
}

type ActiveHead struct {
	AppID        string
	DeploymentID string
	ReleaseID    string
	Slot         string
	Generation   int64
	UpdatedAt    time.Time
}

type BeginInput struct {
	DeploymentID                 string
	AppID                        string
	ReleaseID                    string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ComponentNames               []string
}

type Repository struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Repository { return &Repository{db: db, now: time.Now} }

// Begin creates the durable preflight record and derives the inactive slot
// from the current active head in the same transaction. A replay with the exact
// same immutable identity returns the existing record.
func (r *Repository) Begin(ctx context.Context, input BeginInput) (Deployment, bool, error) {
	components, ok := canonicalComponents(input.ComponentNames)
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(input.DeploymentID) || !canonicalUUID(input.AppID) ||
		!validReleaseID(input.ReleaseID) || !canonicalUUID(input.DeploymentPlanRevisionID) || input.DeploymentPlanRevisionNumber < 1 || !ok {
		return Deployment{}, false, ErrInvalidState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, false, err
	}
	defer tx.Rollback()

	if existing, err := getDeployment(ctx, tx, input.AppID, input.DeploymentID); err == nil {
		if existing.ReleaseID != input.ReleaseID || existing.DeploymentPlanRevisionID != input.DeploymentPlanRevisionID ||
			existing.DeploymentPlanRevisionNumber != input.DeploymentPlanRevisionNumber || !sameComponentNames(existing.Components, components) {
			return Deployment{}, false, ErrInvalidState
		}
		return existing, false, tx.Commit()
	} else if !errors.Is(err, ErrNotFound) {
		return Deployment{}, false, err
	}
	var inProgressDeploymentID string
	if err := tx.QueryRowContext(ctx, `SELECT deployment_id FROM generated_runtime_deployments WHERE app_id=? AND phase NOT IN ('succeeded','failed','cancelled') ORDER BY created_at,deployment_id LIMIT 1`, input.AppID).Scan(&inProgressDeploymentID); err == nil {
		return Deployment{}, false, ErrDeploymentInProgress
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, err
	}

	var previousDeploymentID, previousSlot string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(deployment_id,''),COALESCE(slot,'') FROM generated_runtime_active_heads WHERE app_id=?`, input.AppID).Scan(&previousDeploymentID, &previousSlot); errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, ErrInvalidState
	} else if err != nil {
		return Deployment{}, false, err
	}
	candidateSlot := "blue"
	if previousSlot == "blue" {
		candidateSlot = "green"
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,previous_active_deployment_id,previous_active_slot,phase,migration_state,
		diagnostic_code,created_at,updated_at
	) SELECT d.id,d.app_id,d.release_id,d.deployment_plan_revision_id,d.deployment_plan_revision_number,
		?,?,?,?,CASE WHEN p.migration_evidence_digest='' THEN 'not_required' ELSE 'pending' END,NULL,?,?
	FROM deployments d
	JOIN deployment_plan_revisions p ON p.id=d.deployment_plan_revision_id AND p.app_id=d.app_id AND p.revision_number=d.deployment_plan_revision_number
	WHERE d.id=? AND d.app_id=? AND d.release_id=? AND d.runtime_strategy='generated_node' AND d.provenance_initialized=1
	  AND d.deployment_plan_revision_id=? AND d.deployment_plan_revision_number=? AND p.strategy='generated_node'
	  AND p.component_count=?
	  AND (p.migration_evidence_digest='' OR EXISTS(
		SELECT 1 FROM deployment_plan_migration_approvals a WHERE a.revision_id=p.id AND a.app_id=p.app_id
	  ))`, candidateSlot, nullable(previousDeploymentID), nullable(previousSlot), PhasePreflight, now, now,
		input.DeploymentID, input.AppID, input.ReleaseID, input.DeploymentPlanRevisionID, input.DeploymentPlanRevisionNumber, len(components))
	if err != nil {
		return Deployment{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Deployment{}, false, err
	}
	if changed != 1 {
		if migrationApprovalMissing(ctx, tx, input) {
			return Deployment{}, false, ErrMigrationApprovalRequired
		}
		return Deployment{}, false, ErrInvalidState
	}
	for _, component := range components {
		if _, err := tx.ExecContext(ctx, `INSERT INTO generated_runtime_components(deployment_id,component_name,slot,state,created_at,updated_at) VALUES(?,?,?,'pending',?,?)`, input.DeploymentID, component, candidateSlot, now, now); err != nil {
			return Deployment{}, false, err
		}
	}
	value, err := getDeployment(ctx, tx, input.AppID, input.DeploymentID)
	if err != nil {
		return Deployment{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, false, err
	}
	return value, true, nil
}

func (r *Repository) Get(ctx context.Context, appID, deploymentID string) (Deployment, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) {
		return Deployment{}, ErrNotFound
	}
	return getDeployment(ctx, r.db, appID, deploymentID)
}

func (r *Repository) Active(ctx context.Context, appID string) (ActiveHead, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) {
		return ActiveHead{}, ErrNotFound
	}
	return scanActiveHead(r.db.QueryRowContext(ctx, activeHeadSelect+` WHERE app_id=?`, appID))
}

func (r *Repository) Recoverable(ctx context.Context) ([]Deployment, error) {
	if r == nil || r.db == nil || ctx == nil {
		return nil, ErrInvalidState
	}
	rows, err := r.db.QueryContext(ctx, deploymentSelect+` WHERE phase NOT IN ('succeeded','failed','cancelled') ORDER BY updated_at,deployment_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Deployment
	for rows.Next() {
		value, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Components, err = listComponents(ctx, r.db, result[index].DeploymentID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *Repository) Advance(ctx context.Context, appID, deploymentID string, expected, next Phase, diagnostic DiagnosticCode) (Deployment, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) || !allowedPhaseTransition(expected, next) || !validPhaseDiagnostic(next, diagnostic) {
		return Deployment{}, ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	finished := any(nil)
	if terminalPhase(next) {
		finished = now
	}
	condition := ""
	switch {
	case expected == PhaseBuilding && next == PhaseMigrating:
		condition = " AND migration_state='pending'"
	case expected == PhaseBuilding && next == PhaseStartingCandidate:
		condition = " AND migration_state='not_required'"
	case expected == PhaseMigrating && next == PhaseStartingCandidate:
		condition = " AND migration_state='succeeded'"
	}
	result, err := r.db.ExecContext(ctx, `UPDATE generated_runtime_deployments SET phase=?,diagnostic_code=?,updated_at=?,finished_at=? WHERE deployment_id=? AND app_id=? AND phase=?`+condition, next, nullable(string(diagnostic)), now, finished, deploymentID, appID, expected)
	if err != nil {
		return Deployment{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Deployment{}, err
	} else if changed != 1 {
		return Deployment{}, r.transitionError(ctx, appID, deploymentID)
	}
	return r.Get(ctx, appID, deploymentID)
}

func (r *Repository) BeginMigration(ctx context.Context, appID, deploymentID string) (Deployment, error) {
	return r.transitionMigration(ctx, appID, deploymentID, MigrationPending, MigrationRunning)
}

func (r *Repository) FinishMigration(ctx context.Context, appID, deploymentID string, succeeded bool) (Deployment, error) {
	next := MigrationFailed
	if succeeded {
		next = MigrationSucceeded
	}
	return r.transitionMigration(ctx, appID, deploymentID, MigrationRunning, next)
}

func (r *Repository) transitionMigration(ctx context.Context, appID, deploymentID string, expected, next MigrationState) (Deployment, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) {
		return Deployment{}, ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	var result sql.Result
	var err error
	if next == MigrationRunning {
		result, err = r.db.ExecContext(ctx, `UPDATE generated_runtime_deployments SET migration_state=?,migration_started_at=?,updated_at=? WHERE deployment_id=? AND app_id=? AND phase='migrating' AND migration_state=?`, next, now, now, deploymentID, appID, expected)
	} else {
		result, err = r.db.ExecContext(ctx, `UPDATE generated_runtime_deployments SET migration_state=?,migration_finished_at=?,updated_at=? WHERE deployment_id=? AND app_id=? AND phase='migrating' AND migration_state=?`, next, now, now, deploymentID, appID, expected)
	}
	if err != nil {
		return Deployment{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Deployment{}, err
	} else if changed != 1 {
		return Deployment{}, r.transitionError(ctx, appID, deploymentID)
	}
	return r.Get(ctx, appID, deploymentID)
}

func (r *Repository) SetImageReady(ctx context.Context, appID, deploymentID, componentName, artifactID string) (Component, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) || !validText(componentName, 256) || !canonicalUUID(artifactID) {
		return Component{}, ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, `UPDATE generated_runtime_components SET image_artifact_id=?,state='image_ready',updated_at=? WHERE deployment_id=? AND component_name=? AND state='pending' AND EXISTS(SELECT 1 FROM generated_runtime_deployments d WHERE d.deployment_id=? AND d.app_id=?)`, artifactID, now, deploymentID, componentName, deploymentID, appID)
	if err != nil {
		return Component{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Component{}, err
	} else if changed != 1 {
		return Component{}, ErrInvalidTransition
	}
	return r.Component(ctx, appID, deploymentID, componentName)
}

func (r *Repository) SetContainerStarting(ctx context.Context, appID, deploymentID, componentName, containerName string) (Component, error) {
	if !validContainerName(containerName) {
		return Component{}, ErrInvalidTransition
	}
	return r.updateComponent(ctx, appID, deploymentID, componentName, ComponentImageReady, ComponentStarting, containerName, "", "")
}

func (r *Repository) SetContainerRunning(ctx context.Context, appID, deploymentID, componentName, containerID string) (Component, error) {
	if !lowerHex(containerID, 64) {
		return Component{}, ErrInvalidTransition
	}
	return r.updateComponent(ctx, appID, deploymentID, componentName, ComponentStarting, ComponentRunning, "", containerID, "")
}

func (r *Repository) AdvanceComponent(ctx context.Context, appID, deploymentID, componentName string, expected, next ComponentState) (Component, error) {
	if !allowedComponentTransition(expected, next) || next == ComponentFailed || next == ComponentStarting || next == ComponentRunning || next == ComponentImageReady || next == ComponentActive {
		return Component{}, ErrInvalidTransition
	}
	return r.updateComponent(ctx, appID, deploymentID, componentName, expected, next, "", "", "")
}

func (r *Repository) FailComponent(ctx context.Context, appID, deploymentID, componentName string, expected ComponentState, diagnostic DiagnosticCode) (Component, error) {
	if !allowedComponentTransition(expected, ComponentFailed) || !componentDiagnostic(diagnostic) {
		return Component{}, ErrInvalidTransition
	}
	return r.updateComponent(ctx, appID, deploymentID, componentName, expected, ComponentFailed, "", "", diagnostic)
}

func (r *Repository) updateComponent(ctx context.Context, appID, deploymentID, componentName string, expected, next ComponentState, containerName, containerID string, diagnostic DiagnosticCode) (Component, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) || !validText(componentName, 256) {
		return Component{}, ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	finished := any(nil)
	if next == ComponentStopped || next == ComponentFailed {
		finished = now
	}
	result, err := r.db.ExecContext(ctx, `UPDATE generated_runtime_components SET state=?,container_name=CASE WHEN ?<>'' THEN ? ELSE container_name END,container_id=CASE WHEN ?<>'' THEN ? ELSE container_id END,diagnostic_code=?,updated_at=?,finished_at=? WHERE deployment_id=? AND component_name=? AND state=? AND EXISTS(SELECT 1 FROM generated_runtime_deployments d WHERE d.deployment_id=? AND d.app_id=?)`, next, containerName, containerName, containerID, containerID, nullable(string(diagnostic)), now, finished, deploymentID, componentName, expected, deploymentID, appID)
	if err != nil {
		return Component{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return Component{}, err
	} else if changed != 1 {
		return Component{}, ErrInvalidTransition
	}
	return r.Component(ctx, appID, deploymentID, componentName)
}

func (r *Repository) Component(ctx context.Context, appID, deploymentID, componentName string) (Component, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) || !validText(componentName, 256) {
		return Component{}, ErrNotFound
	}
	value, err := scanComponent(r.db.QueryRowContext(ctx, componentSelect+` WHERE c.deployment_id=? AND c.component_name=? AND d.app_id=?`, deploymentID, componentName, appID))
	if errors.Is(err, sql.ErrNoRows) {
		return Component{}, ErrNotFound
	}
	return value, err
}

// SwitchActive records the already completed atomic ingress switch and marks
// all healthy candidate components active in the same transaction. The
// expected generation and the deployment's captured previous head prevent a
// stale candidate from winning.
func (r *Repository) SwitchActive(ctx context.Context, appID, deploymentID string, expectedGeneration int64) (ActiveHead, bool, error) {
	if r == nil || r.db == nil || ctx == nil || !canonicalUUID(appID) || !canonicalUUID(deploymentID) || expectedGeneration < 0 {
		return ActiveHead{}, false, ErrInvalidState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ActiveHead{}, false, err
	}
	defer tx.Rollback()
	current, err := scanActiveHead(tx.QueryRowContext(ctx, activeHeadSelect+` WHERE app_id=?`, appID))
	if err != nil {
		return ActiveHead{}, false, err
	}
	if current.DeploymentID == deploymentID {
		return current, false, tx.Commit()
	}
	if current.Generation != expectedGeneration {
		return ActiveHead{}, false, ErrConflict
	}
	var releaseID, slot, previousDeploymentID, previousSlot string
	var phase Phase
	if err := tx.QueryRowContext(ctx, `SELECT release_id,candidate_slot,COALESCE(previous_active_deployment_id,''),COALESCE(previous_active_slot,''),phase FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?`, deploymentID, appID).Scan(&releaseID, &slot, &previousDeploymentID, &previousSlot, &phase); errors.Is(err, sql.ErrNoRows) {
		return ActiveHead{}, false, ErrNotFound
	} else if err != nil {
		return ActiveHead{}, false, err
	}
	if phase != PhaseSwitchingRoute || previousDeploymentID != current.DeploymentID || previousSlot != current.Slot {
		return ActiveHead{}, false, ErrConflict
	}
	var componentCount, healthyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN state='healthy' THEN 1 ELSE 0 END),0) FROM generated_runtime_components WHERE deployment_id=?`, deploymentID).Scan(&componentCount, &healthyCount); err != nil {
		return ActiveHead{}, false, err
	}
	if componentCount == 0 || componentCount != healthyCount {
		return ActiveHead{}, false, ErrInvalidTransition
	}
	now := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_active_heads SET deployment_id=?,release_id=?,slot=?,generation=generation+1,updated_at=? WHERE app_id=? AND generation=?`, deploymentID, releaseID, slot, now, appID, expectedGeneration)
	if err != nil {
		return ActiveHead{}, false, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return ActiveHead{}, false, err
	} else if changed != 1 {
		return ActiveHead{}, false, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generated_runtime_components SET state='active',updated_at=? WHERE deployment_id=? AND state='healthy'`, now, deploymentID); err != nil {
		return ActiveHead{}, false, err
	}
	value, err := scanActiveHead(tx.QueryRowContext(ctx, activeHeadSelect+` WHERE app_id=?`, appID))
	if err != nil {
		return ActiveHead{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ActiveHead{}, false, err
	}
	return value, true, nil
}

func (r *Repository) transitionError(ctx context.Context, appID, deploymentID string) error {
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?)`, deploymentID, appID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrInvalidTransition
}

func migrationApprovalMissing(ctx context.Context, query queryer, input BeginInput) bool {
	var missing int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM deployment_plan_revisions p
		WHERE p.id=? AND p.app_id=? AND p.revision_number=? AND p.migration_evidence_digest<>''
		  AND NOT EXISTS(SELECT 1 FROM deployment_plan_migration_approvals a WHERE a.revision_id=p.id AND a.app_id=p.app_id)
	)`, input.DeploymentPlanRevisionID, input.AppID, input.DeploymentPlanRevisionNumber).Scan(&missing)
	return err == nil && missing == 1
}

const deploymentSelect = `SELECT deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,candidate_slot,COALESCE(previous_active_deployment_id,''),COALESCE(previous_active_slot,''),phase,migration_state,COALESCE(diagnostic_code,''),created_at,updated_at,COALESCE(migration_started_at,''),COALESCE(migration_finished_at,''),COALESCE(finished_at,'') FROM generated_runtime_deployments`
const componentSelect = `SELECT c.deployment_id,c.component_name,c.slot,COALESCE(c.image_artifact_id,''),COALESCE(c.container_name,''),COALESCE(c.container_id,''),c.state,COALESCE(c.diagnostic_code,''),c.created_at,c.updated_at,COALESCE(c.finished_at,'') FROM generated_runtime_components c JOIN generated_runtime_deployments d ON d.deployment_id=c.deployment_id`
const activeHeadSelect = `SELECT app_id,COALESCE(deployment_id,''),COALESCE(release_id,''),COALESCE(slot,''),generation,COALESCE(updated_at,'') FROM generated_runtime_active_heads`

type scanner interface{ Scan(...any) error }
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getDeployment(ctx context.Context, query interface {
	queryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, appID, deploymentID string) (Deployment, error) {
	value, err := scanDeployment(query.QueryRowContext(ctx, deploymentSelect+` WHERE app_id=? AND deployment_id=?`, appID, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	value.Components, err = listComponents(ctx, query, deploymentID)
	return value, err
}

func listComponents(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, deploymentID string) ([]Component, error) {
	rows, err := query.QueryContext(ctx, componentSelect+` WHERE c.deployment_id=? ORDER BY c.component_name`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Component{}
	for rows.Next() {
		value, err := scanComponent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanDeployment(row scanner) (Deployment, error) {
	var value Deployment
	var created, updated, migrationStarted, migrationFinished, finished string
	if err := row.Scan(&value.DeploymentID, &value.AppID, &value.ReleaseID, &value.DeploymentPlanRevisionID, &value.DeploymentPlanRevisionNumber, &value.CandidateSlot, &value.PreviousActiveDeploymentID, &value.PreviousActiveSlot, &value.Phase, &value.MigrationState, &value.DiagnosticCode, &created, &updated, &migrationStarted, &migrationFinished, &finished); err != nil {
		return Deployment{}, err
	}
	var err error
	if value.CreatedAt, err = parseTime(created); err != nil {
		return Deployment{}, err
	}
	if value.UpdatedAt, err = parseTime(updated); err != nil {
		return Deployment{}, err
	}
	value.MigrationStartedAt, _ = parseOptionalTime(migrationStarted)
	value.MigrationFinishedAt, _ = parseOptionalTime(migrationFinished)
	value.FinishedAt, _ = parseOptionalTime(finished)
	return value, nil
}

func scanComponent(row scanner) (Component, error) {
	var value Component
	var created, updated, finished string
	if err := row.Scan(&value.DeploymentID, &value.Name, &value.Slot, &value.ImageArtifactID, &value.ContainerName, &value.ContainerID, &value.State, &value.DiagnosticCode, &created, &updated, &finished); err != nil {
		return Component{}, err
	}
	var err error
	if value.CreatedAt, err = parseTime(created); err != nil {
		return Component{}, err
	}
	if value.UpdatedAt, err = parseTime(updated); err != nil {
		return Component{}, err
	}
	value.FinishedAt, _ = parseOptionalTime(finished)
	return value, nil
}

func scanActiveHead(row scanner) (ActiveHead, error) {
	var value ActiveHead
	var updated string
	if err := row.Scan(&value.AppID, &value.DeploymentID, &value.ReleaseID, &value.Slot, &value.Generation, &updated); errors.Is(err, sql.ErrNoRows) {
		return ActiveHead{}, ErrNotFound
	} else if err != nil {
		return ActiveHead{}, err
	}
	value.UpdatedAt, _ = parseOptionalTime(updated)
	return value, nil
}

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return parseTime(value)
}

func canonicalComponents(values []string) ([]string, bool) {
	if len(values) < 1 || len(values) > 64 {
		return nil, false
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !validText(value, 256) || (index > 0 && result[index-1] == value) {
			return nil, false
		}
	}
	return result, true
}

func sameComponentNames(values []Component, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	for index := range values {
		if values[index].Name != expected[index] {
			return false
		}
	}
	return true
}

func allowedPhaseTransition(current, next Phase) bool {
	allowed := map[Phase]map[Phase]bool{
		PhasePreflight:         {PhaseBuilding: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseBuilding:          {PhaseMigrating: true, PhaseStartingCandidate: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseMigrating:         {PhaseStartingCandidate: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseStartingCandidate: {PhaseWaitingHealth: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseWaitingHealth:     {PhaseSwitchingRoute: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseSwitchingRoute:    {PhaseDraining: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseDraining:          {PhaseSucceeded: true, PhaseFailed: true, PhaseCancelled: true},
	}
	return allowed[current][next]
}

func validPhaseDiagnostic(next Phase, diagnostic DiagnosticCode) bool {
	if next == PhaseCancelled {
		return diagnostic == DiagnosticCancelled
	}
	if next == PhaseFailed {
		return knownDiagnostic(diagnostic) && diagnostic != DiagnosticCancelled
	}
	return diagnostic == ""
}

func knownDiagnostic(value DiagnosticCode) bool {
	switch value {
	case DiagnosticInsufficientReplacementCapacity, DiagnosticPlanReviewRequired, DiagnosticMigrationFailed,
		DiagnosticStartFailed, DiagnosticHealthFailed, DiagnosticRouteSwitchFailed, DiagnosticRuntimeUnavailable,
		DiagnosticDaemonRestarted, DiagnosticCancelled, DiagnosticInternalError:
		return true
	default:
		return false
	}
}

func componentDiagnostic(value DiagnosticCode) bool {
	switch value {
	case DiagnosticStartFailed, DiagnosticHealthFailed, DiagnosticRuntimeUnavailable, DiagnosticDaemonRestarted, DiagnosticCancelled, DiagnosticInternalError:
		return true
	default:
		return false
	}
}

func terminalPhase(value Phase) bool {
	return value == PhaseSucceeded || value == PhaseFailed || value == PhaseCancelled
}

func allowedComponentTransition(current, next ComponentState) bool {
	allowed := map[ComponentState]map[ComponentState]bool{
		ComponentPending:    {ComponentImageReady: true, ComponentFailed: true},
		ComponentImageReady: {ComponentStarting: true, ComponentFailed: true},
		ComponentStarting:   {ComponentRunning: true, ComponentFailed: true},
		ComponentRunning:    {ComponentHealthy: true, ComponentFailed: true},
		ComponentHealthy:    {ComponentActive: true, ComponentFailed: true},
		ComponentActive:     {ComponentDraining: true, ComponentStopped: true, ComponentFailed: true},
		ComponentDraining:   {ComponentStopped: true, ComponentFailed: true},
	}
	return allowed[current][next]
}

func validText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validContainerName(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || (index > 0 && (character == '_' || character == '.' || character == '-')) {
			continue
		}
		return false
	}
	return true
}

func validReleaseID(value string) bool { return canonicalUUID(value) || lowerHex(value, 32) }
func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
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
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
