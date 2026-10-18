package deployments

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
)

type ComposeExecutionRequest struct {
	Claim                       deploymentexecution.Claim
	DeploymentID                string
	ReleaseID                   string
	ConfigurationRevisionID     string
	ConfigurationRevisionNumber int64
	Findings                    []Finding
}

type ComposeExecution struct {
	state *composeExecutionState
}

type composeExecutionState struct {
	mu      sync.Mutex
	changed *sync.Cond
	repo    *Repository
	ctx     context.Context
	pinned  *deploymentexecution.Pinned
	request ComposeExecutionRequest
	active  bool
	closing bool
	left    bool
}

func (r *Repository) ExecutionConfigured() bool {
	return r != nil && r.db != nil && r.executionGuard != nil && r.executionGuard.UsesDatabase(r.db)
}

func (r *Repository) PinComposeExecution(ctx context.Context, request ComposeExecutionRequest) (*ComposeExecution, error) {
	if !r.ExecutionConfigured() {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if uuid.Validate(request.DeploymentID) != nil || request.Claim.JobID() == "" ||
		uuid.Validate(request.Claim.ApplicationID()) != nil || request.Claim.Attempt() < 1 ||
		!validReleaseID(request.ReleaseID) || uuid.Validate(request.ConfigurationRevisionID) != nil ||
		request.ConfigurationRevisionNumber < 1 {
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
	state := &composeExecutionState{repo: r, ctx: ctx, pinned: pinned, request: request}
	state.changed = sync.NewCond(&state.mu)
	return &ComposeExecution{state: state}, nil
}

func (c *ComposeExecution) CheckBeforeUp() error {
	state, done, err := c.begin()
	if err != nil {
		return err
	}
	defer done()
	return state.check(false, Applying, false)
}

func (c *ComposeExecution) RecordWaitingHealth() error {
	state, done, err := c.begin()
	if err != nil {
		return err
	}
	defer done()
	return state.check(true, WaitingHealth, true)
}

func (c *ComposeExecution) RecordSucceeded() error {
	state, done, err := c.begin()
	if err != nil {
		return err
	}
	defer done()
	return state.check(true, Succeeded, true)
}

func (c *ComposeExecution) Leave() {
	if c == nil || c.state == nil {
		return
	}
	state := c.state
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

func (c *ComposeExecution) begin() (*composeExecutionState, func(), error) {
	if c == nil || c.state == nil {
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	state := c.state
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

func (s *composeExecutionState) check(writer bool, target Status, mutate bool) error {
	tx, err := s.repo.db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: !writer})
	if err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	defer tx.Rollback()
	if writer {
		if _, err := tx.ExecContext(s.ctx, `UPDATE jobs SET id=id WHERE 0`); err != nil {
			return gateDatabaseError(s.ctx, err)
		}
	}
	if target == Applying {
		err = s.pinned.CheckComposeUpTx(tx, s.request.Claim, s.request.DeploymentID)
	} else {
		err = s.pinned.CheckComposeObservationTx(tx, s.request.Claim, s.request.DeploymentID)
	}
	if err != nil {
		return err
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	status, err := s.validateTx(tx)
	if err != nil {
		return err
	}
	if target == Applying && status != Applying {
		return ErrInvalidTransition
	}
	if target == WaitingHealth && status != Applying && status != WaitingHealth {
		return ErrInvalidTransition
	}
	if target == Succeeded && status != WaitingHealth {
		return ErrInvalidTransition
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if mutate && status != target {
		now := s.repo.now().UTC().Format(time.RFC3339Nano)
		finished := any(nil)
		if target == Succeeded {
			finished = now
		}
		result, err := tx.ExecContext(s.ctx, `UPDATE deployments SET status=?,started_at=COALESCE(started_at,?),finished_at=?,diagnostic_code=NULL,failure_code=NULL,failure_summary=NULL WHERE id=? AND app_id=? AND job_id=? AND status=?`, target, now, finished, s.request.DeploymentID, s.request.Claim.ApplicationID(), s.request.Claim.JobID(), status)
		if err != nil {
			return gateDatabaseError(s.ctx, err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return gateDatabaseError(s.ctx, err)
		}
		if count != 1 {
			return ErrInvalidTransition
		}
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	return nil
}

func (s *composeExecutionState) validateTx(tx *sql.Tx) (Status, error) {
	var status Status
	var strategy RuntimeStrategy
	var initialized bool
	var releaseID, configurationID string
	var configurationNumber int64
	err := tx.QueryRowContext(s.ctx, `SELECT status,runtime_strategy,provenance_initialized,
		COALESCE(release_id,''),COALESCE(actual_configuration_revision_id,''),actual_configuration_revision_number
		FROM deployments WHERE id=? AND app_id=? AND job_id=?`, s.request.DeploymentID,
		s.request.Claim.ApplicationID(), s.request.Claim.JobID()).
		Scan(&status, &strategy, &initialized, &releaseID, &configurationID, &configurationNumber)
	if s.repo.afterComposeIdentityRead != nil {
		s.repo.afterComposeIdentityRead()
	}
	if err != nil {
		classified := gateDatabaseError(s.ctx, err)
		if errors.Is(err, sql.ErrNoRows) {
			classified = errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
		if canceled := contextError(s.ctx); canceled != nil {
			return "", errors.Join(canceled, classified)
		}
		return "", classified
	}
	if !initialized || strategy != RuntimeCompose || releaseID != s.request.ReleaseID ||
		configurationID != s.request.ConfigurationRevisionID || configurationNumber != s.request.ConfigurationRevisionNumber {
		if canceled := contextError(s.ctx); canceled != nil {
			return "", errors.Join(canceled, ErrInvalidDeployment)
		}
		return "", ErrInvalidDeployment
	}
	persisted, err := findingsTx(s.ctx, tx, s.request.Claim.ApplicationID(), s.request.DeploymentID)
	if canceled := contextError(s.ctx); canceled != nil {
		return "", errors.Join(canceled, err)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidDeployment) {
			return "", err
		}
		return "", gateDatabaseError(s.ctx, err)
	}
	if !sameFindingSet(persisted, s.request.Findings) {
		return "", ErrInvalidDeployment
	}
	rejected, missingApproval, err := evaluatePolicyTx(s.ctx, tx, s.request.Claim.ApplicationID(), persisted)
	if err != nil {
		return "", gateDatabaseError(s.ctx, err)
	}
	if err := contextError(s.ctx); err != nil {
		return "", err
	}
	if rejected {
		return "", ErrRejectedCapability
	}
	if missingApproval {
		return "", ErrApprovalRequired
	}
	return status, nil
}
