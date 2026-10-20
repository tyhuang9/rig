package deployments

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

type GeneratedNetworkRequest struct {
	Claim                        deploymentexecution.Claim
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	Findings                     []Finding
}

type GeneratedNetworkExecution struct {
	state *generatedNetworkExecutionState
}

type generatedNetworkExecutionState struct {
	mu      sync.Mutex
	changed *sync.Cond
	repo    *Repository
	ctx     context.Context
	pinned  *deploymentexecution.Pinned
	request GeneratedNetworkRequest
	active  bool
	closing bool
	left    bool
}

func (r *Repository) PinGeneratedNetwork(ctx context.Context, request GeneratedNetworkRequest) (*GeneratedNetworkExecution, error) {
	if !r.ExecutionConfigured() {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if uuid.Validate(request.DeploymentID) != nil || request.Claim.JobID() == "" ||
		uuid.Validate(request.Claim.ApplicationID()) != nil || request.Claim.Attempt() < 1 ||
		!validReleaseID(request.ReleaseID) || uuid.Validate(request.ConfigurationRevisionID) != nil ||
		request.ConfigurationRevisionNumber < 1 || uuid.Validate(request.DeploymentPlanRevisionID) != nil ||
		request.DeploymentPlanRevisionNumber < 1 {
		return nil, ErrInvalidDeployment
	}
	if err := validateFindings(request.Findings); err != nil {
		return nil, err
	}
	pinned, err := r.executionGuard.PinScope(ctx)
	if err != nil {
		return nil, err
	}
	request.Findings = append([]Finding(nil), request.Findings...)
	state := &generatedNetworkExecutionState{repo: r, ctx: ctx, pinned: pinned, request: request}
	state.changed = sync.NewCond(&state.mu)
	return &GeneratedNetworkExecution{state: state}, nil
}

func (e *GeneratedNetworkExecution) CheckBeforeCreate() error {
	state, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	return state.check(true)
}

func (e *GeneratedNetworkExecution) CheckObservation() error {
	state, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	return state.check(false)
}

func (e *GeneratedNetworkExecution) Leave() {
	if e == nil || e.state == nil {
		return
	}
	state := e.state
	state.mu.Lock()
	if state.closing {
		for !state.left {
			state.changed.Wait()
		}
		state.mu.Unlock()
		return
	}
	state.closing = true
	for state.active {
		state.changed.Wait()
	}
	pinned := state.pinned
	state.pinned = nil
	state.mu.Unlock()
	if pinned != nil {
		pinned.Leave()
	}
	state.mu.Lock()
	state.repo = nil
	state.ctx = nil
	state.left = true
	state.changed.Broadcast()
	state.mu.Unlock()
}

func (e *GeneratedNetworkExecution) begin() (*generatedNetworkExecutionState, func(), error) {
	if e == nil || e.state == nil {
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	state := e.state
	state.mu.Lock()
	if state.closing || state.left || state.active || state.repo == nil || state.ctx == nil || state.pinned == nil {
		state.mu.Unlock()
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	state.active = true
	state.mu.Unlock()
	if err := contextError(state.ctx); err != nil {
		state.mu.Lock()
		state.active = false
		state.changed.Broadcast()
		state.mu.Unlock()
		return nil, nil, err
	}
	done := func() {
		state.mu.Lock()
		state.active = false
		state.changed.Broadcast()
		state.mu.Unlock()
	}
	return state, done, nil
}

func (s *generatedNetworkExecutionState) check(create bool) error {
	tx, err := s.repo.db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	defer tx.Rollback()
	if create {
		err = s.pinned.CheckGeneratedNetworkCreateTx(tx, s.request.Claim, s.request.DeploymentID)
	} else {
		err = s.pinned.CheckGeneratedNetworkObservationTx(tx, s.request.Claim, s.request.DeploymentID)
	}
	if err != nil {
		return err
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if err := s.validateTx(tx); err != nil {
		return err
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	return nil
}

func (s *generatedNetworkExecutionState) validateTx(tx *sql.Tx) error {
	var strategy RuntimeStrategy
	var initialized bool
	var releaseID, configurationID, planID string
	var configurationNumber, planNumber int64
	err := tx.QueryRowContext(s.ctx, `SELECT runtime_strategy,provenance_initialized,COALESCE(release_id,''),
		COALESCE(actual_configuration_revision_id,''),actual_configuration_revision_number,
		COALESCE(deployment_plan_revision_id,''),COALESCE(deployment_plan_revision_number,0)
		FROM deployments WHERE id=? AND app_id=? AND job_id=?`, s.request.DeploymentID,
		s.request.Claim.ApplicationID(), s.request.Claim.JobID()).
		Scan(&strategy, &initialized, &releaseID, &configurationID, &configurationNumber, &planID, &planNumber)
	if err != nil {
		classified := gateDatabaseError(s.ctx, err)
		if errors.Is(err, sql.ErrNoRows) {
			classified = errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
		if canceled := contextError(s.ctx); canceled != nil {
			return errors.Join(canceled, classified)
		}
		return classified
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if !initialized || strategy != RuntimeGeneratedNode || releaseID != s.request.ReleaseID ||
		configurationID != s.request.ConfigurationRevisionID || configurationNumber != s.request.ConfigurationRevisionNumber ||
		planID != s.request.DeploymentPlanRevisionID || planNumber != s.request.DeploymentPlanRevisionNumber {
		return ErrInvalidDeployment
	}

	var runtimeAppID, runtimeReleaseID, runtimePlanID, candidateSlot, previousDeploymentID, previousSlot string
	var runtimePlanNumber int64
	var phase generatedruntimestate.Phase
	err = tx.QueryRowContext(s.ctx, `SELECT app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,COALESCE(previous_active_deployment_id,''),COALESCE(previous_active_slot,''),phase
		FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?`,
		s.request.DeploymentID, s.request.Claim.ApplicationID()).
		Scan(&runtimeAppID, &runtimeReleaseID, &runtimePlanID, &runtimePlanNumber, &candidateSlot, &previousDeploymentID, &previousSlot, &phase)
	if err != nil {
		classified := gateDatabaseError(s.ctx, err)
		if errors.Is(err, sql.ErrNoRows) {
			classified = errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
		if canceled := contextError(s.ctx); canceled != nil {
			return errors.Join(canceled, classified)
		}
		return classified
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if runtimeAppID != s.request.Claim.ApplicationID() || runtimeReleaseID != s.request.ReleaseID ||
		runtimePlanID != s.request.DeploymentPlanRevisionID || runtimePlanNumber != s.request.DeploymentPlanRevisionNumber ||
		!generatedNetworkPhase(phase) || !validGeneratedSlotRelationship(candidateSlot, previousDeploymentID, previousSlot) {
		return ErrInvalidDeployment
	}

	var activeDeploymentID, activeSlot string
	err = tx.QueryRowContext(s.ctx, `SELECT COALESCE(deployment_id,''),COALESCE(slot,'') FROM generated_runtime_active_heads WHERE app_id=?`, s.request.Claim.ApplicationID()).
		Scan(&activeDeploymentID, &activeSlot)
	if err != nil {
		classified := gateDatabaseError(s.ctx, err)
		if errors.Is(err, sql.ErrNoRows) {
			classified = errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
		if canceled := contextError(s.ctx); canceled != nil {
			return errors.Join(canceled, classified)
		}
		return classified
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if activeDeploymentID != previousDeploymentID || activeSlot != previousSlot {
		return deploymentexecution.ErrAuthorityStale
	}

	var planStrategy, acceptanceStatus, migrationDigest string
	err = tx.QueryRowContext(s.ctx, `SELECT strategy,acceptance_status,migration_evidence_digest FROM deployment_plan_revisions
		WHERE id=? AND app_id=? AND revision_number=?`, s.request.DeploymentPlanRevisionID,
		s.request.Claim.ApplicationID(), s.request.DeploymentPlanRevisionNumber).
		Scan(&planStrategy, &acceptanceStatus, &migrationDigest)
	if err != nil {
		classified := gateDatabaseError(s.ctx, err)
		if errors.Is(err, sql.ErrNoRows) {
			classified = errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
		if canceled := contextError(s.ctx); canceled != nil {
			return errors.Join(canceled, classified)
		}
		return classified
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if planStrategy != string(RuntimeGeneratedNode) || acceptanceStatus != "accepted" {
		return ErrInvalidDeployment
	}
	if migrationDigest != "" {
		var approvals int
		if err := tx.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM deployment_plan_migration_approvals
			WHERE revision_id=? AND app_id=? AND approval_revision=1`, s.request.DeploymentPlanRevisionID,
			s.request.Claim.ApplicationID()).Scan(&approvals); err != nil {
			return gateDatabaseError(s.ctx, err)
		}
		if err := contextError(s.ctx); err != nil {
			return err
		}
		if approvals != 1 {
			return ErrApprovalRequired
		}
	}

	persisted, err := findingsTx(s.ctx, tx, s.request.Claim.ApplicationID(), s.request.DeploymentID)
	if s.repo.afterGeneratedNetworkIdentityRead != nil {
		s.repo.afterGeneratedNetworkIdentityRead()
	}
	if canceled := contextError(s.ctx); canceled != nil {
		return errors.Join(canceled, err)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidDeployment) {
			return err
		}
		return gateDatabaseError(s.ctx, err)
	}
	if !sameFindingSet(persisted, s.request.Findings) {
		return ErrInvalidDeployment
	}
	rejected, missingApproval, err := evaluatePolicyTx(s.ctx, tx, s.request.Claim.ApplicationID(), persisted)
	if err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if rejected {
		return ErrRejectedCapability
	}
	if missingApproval {
		return ErrApprovalRequired
	}
	return nil
}

func generatedNetworkPhase(phase generatedruntimestate.Phase) bool {
	switch phase {
	case generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate:
		return true
	default:
		return false
	}
}

func validGeneratedSlotRelationship(candidateSlot, previousDeploymentID, previousSlot string) bool {
	if candidateSlot != "blue" && candidateSlot != "green" {
		return false
	}
	if (previousDeploymentID == "") != (previousSlot == "") {
		return false
	}
	if previousSlot == "" {
		return candidateSlot == "blue"
	}
	return (previousSlot == "blue" || previousSlot == "green") && previousSlot != candidateSlot
}
