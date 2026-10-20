package deploymentexecution

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

const (
	generatedCandidateCleanupTimeout = 70 * time.Minute
	generatedCandidateCleanupLimit   = 2
)

type generatedCandidateCleanupKey struct {
	deploymentID  string
	componentName string
}

// GeneratedCandidateCleanup is a bounded, process-local cleanup capability
// for one exact generated-candidate create invocation. It grants no positive
// create, start, observation, result, or route authority.
type GeneratedCandidateCleanup struct {
	state *generatedCandidateCleanupState
}

type generatedCandidateCleanupState struct {
	mu            sync.Mutex
	changed       *sync.Cond
	parent        *pinnedState
	guard         *Guard
	ctx           context.Context
	cancel        context.CancelFunc
	claim         Claim
	deploymentID  string
	componentName string
	receiptID     string
	invocationID  string
	active        int
	closing       bool
	left          bool
}

type generatedCandidateCleanupIdentity struct {
	claim         Claim
	deploymentID  string
	componentName string
	receiptID     string
	invocationID  string
}

// CheckBeforeCandidateCreateTx revalidates the original execution authority
// immediately before candidate create entry. The concrete owner adds the
// exact runtime phase, roster, receipt, policy, and physical predicates.
func (p *Pinned) CheckBeforeCandidateCreateTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, candidateCreateStatus)
}

// CheckBeforeCandidateStartTx revalidates the original execution authority
// immediately before candidate start entry.
func (p *Pinned) CheckBeforeCandidateStartTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, candidateCreateStatus)
}

// CheckCandidateObservationTx revalidates authority before accepting a
// physical candidate observation in either positive pre-route main stage.
func (p *Pinned) CheckCandidateObservationTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, candidateObservationStatus)
}

// CheckCandidateResultTx revalidates authority before persisting a candidate
// result in either positive pre-route main stage.
func (p *Pinned) CheckCandidateResultTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, candidateObservationStatus)
}

func candidateCreateStatus(status string) bool { return status == "applying" }

func candidateObservationStatus(status string) bool {
	return status == "applying" || status == "waiting_health"
}

// DeriveGeneratedCandidateCleanup captures one exact candidate identity while
// the original positive scope is live. Each pin permanently spends at most two
// sibling component slots for one deployment. Leaving, expiry, or changing a
// receipt or invocation cannot renew a spent component slot.
func (p *Pinned) DeriveGeneratedCandidateCleanup(claim Claim, deploymentID, componentName, receiptID, invocationID string) (*GeneratedCandidateCleanup, error) {
	if !claim.valid() || !canonicalCleanupUUID(deploymentID) || !validCandidateComponentName(componentName) ||
		!canonicalCleanupUUID(receiptID) || !canonicalCleanupUUID(invocationID) {
		return nil, ErrInvalidClaim
	}
	guard, originalContext, done, err := p.beginCheck()
	if err != nil {
		return nil, err
	}
	defer done()
	state := p.state
	key := generatedCandidateCleanupKey{deploymentID: deploymentID, componentName: componentName}
	identity := generatedCandidateCleanupIdentity{
		claim: claim, deploymentID: deploymentID, componentName: componentName,
		receiptID: receiptID, invocationID: invocationID,
	}
	state.mu.Lock()
	if state.closing || state.left || state.guard != guard || state.ctx != originalContext {
		state.mu.Unlock()
		return nil, ErrScopeUnavailable
	}
	if err := cancellationError(originalContext, nil); err != nil {
		state.mu.Unlock()
		return nil, err
	}
	if state.candidateCleanupDeploymentID != "" && state.candidateCleanupDeploymentID != deploymentID {
		state.mu.Unlock()
		return nil, ErrScopeUnavailable
	}
	if _, exists := state.candidateCleanupDerived[key]; exists || len(state.candidateCleanupDerived) >= generatedCandidateCleanupLimit {
		state.mu.Unlock()
		return nil, ErrScopeUnavailable
	}
	for _, prior := range state.candidateCleanupDerived {
		if !sameClaim(prior.claim, claim) || prior.receiptID == receiptID || prior.invocationID == invocationID {
			state.mu.Unlock()
			return nil, ErrScopeUnavailable
		}
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(originalContext), generatedCandidateCleanupTimeout)
	childState := &generatedCandidateCleanupState{
		parent: state, guard: guard, ctx: cleanupContext, cancel: cancel,
		claim: claim, deploymentID: deploymentID, componentName: componentName,
		receiptID: receiptID, invocationID: invocationID,
	}
	childState.changed = sync.NewCond(&childState.mu)
	if state.candidateCleanupDerived == nil {
		state.candidateCleanupDerived = make(map[generatedCandidateCleanupKey]generatedCandidateCleanupIdentity, generatedCandidateCleanupLimit)
	}
	state.candidateCleanupDeploymentID = deploymentID
	state.candidateCleanupDerived[key] = identity
	state.children++
	state.mu.Unlock()
	return &GeneratedCandidateCleanup{state: childState}, nil
}

// CheckBeforeStopTx authorizes only the exact stop boundary captured at
// derivation. The concrete owner adds runtime and physical ownership checks in
// this same transaction.
func (c *GeneratedCandidateCleanup) CheckBeforeStopTx(tx *sql.Tx, claim Claim, deploymentID, componentName, receiptID, invocationID string) error {
	return c.checkTx(tx, claim, deploymentID, componentName, receiptID, invocationID)
}

// CheckBeforeRemoveTx authorizes only the exact remove boundary captured at
// derivation.
func (c *GeneratedCandidateCleanup) CheckBeforeRemoveTx(tx *sql.Tx, claim Claim, deploymentID, componentName, receiptID, invocationID string) error {
	return c.checkTx(tx, claim, deploymentID, componentName, receiptID, invocationID)
}

// CheckRemovedObservationTx authorizes only the exact post-remove absence
// observation captured at derivation.
func (c *GeneratedCandidateCleanup) CheckRemovedObservationTx(tx *sql.Tx, claim Claim, deploymentID, componentName, receiptID, invocationID string) error {
	return c.checkTx(tx, claim, deploymentID, componentName, receiptID, invocationID)
}

func (c *GeneratedCandidateCleanup) checkTx(tx *sql.Tx, claim Claim, deploymentID, componentName, receiptID, invocationID string) error {
	guard, ctx, captured, done, err := c.beginCheck()
	if err != nil {
		return err
	}
	defer done()
	if tx == nil || !guard.UsesDatabase(guard.db) {
		return ErrInvalidConfiguration
	}
	if !sameClaim(captured.claim, claim) || captured.deploymentID != deploymentID ||
		captured.componentName != componentName || captured.receiptID != receiptID || captured.invocationID != invocationID {
		return ErrIdentityLost
	}
	if err := c.checkCurrent(ctx); err != nil {
		return err
	}
	classification, err := guard.bindings.CheckTx(ctx, tx, captured.claim.binding)
	if err != nil {
		return bindingError(ctx, err)
	}
	if err := c.checkCurrent(ctx); err != nil {
		return err
	}
	if classification == deploymentbinding.ClassificationStale {
		return ErrAuthorityStale
	}
	if classification != deploymentbinding.ClassificationCurrent {
		return ErrInvalidConfiguration
	}
	var jobType, resourceType, resourceID, status, phase string
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT type,resource_type,resource_id,status,phase,attempt FROM jobs WHERE id=?`, captured.claim.jobID).
		Scan(&jobType, &resourceType, &resourceID, &status, &phase, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if err := c.checkCurrent(ctx); err != nil {
		return err
	}
	jobAllowsCleanup := status == "running" && phase != "cancelling" || status == "waiting_external" && phase == "cancelling"
	if jobType != "deploy" || resourceType != "application" || resourceID != captured.claim.applicationID ||
		attempt != captured.claim.attempt || !jobAllowsCleanup {
		return ErrIdentityLost
	}
	var deploymentStatus string
	var provenanceInitialized bool
	err = tx.QueryRowContext(ctx, `SELECT status,provenance_initialized FROM deployments
		WHERE id=? AND app_id=? AND job_id=?`, captured.deploymentID, captured.claim.applicationID, captured.claim.jobID).
		Scan(&deploymentStatus, &provenanceInitialized)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if err := c.checkCurrent(ctx); err != nil {
		return err
	}
	if !provenanceInitialized || !candidateObservationStatus(deploymentStatus) {
		return ErrStageInvalid
	}
	pending, err := guard.positiveFence.PendingStartExistsTx(ctx, tx)
	if err != nil {
		return validatorError(ctx, err)
	}
	if err := c.checkCurrent(ctx); err != nil {
		return err
	}
	if pending {
		return ErrPositiveFence
	}
	return c.checkCurrent(ctx)
}

func (c *GeneratedCandidateCleanup) beginCheck() (*Guard, context.Context, generatedCandidateCleanupIdentity, func(), error) {
	if c == nil || c.state == nil {
		return nil, nil, generatedCandidateCleanupIdentity{}, nil, ErrScopeUnavailable
	}
	state := c.state
	state.mu.Lock()
	if state.closing || state.left || state.guard == nil || state.ctx == nil {
		state.mu.Unlock()
		return nil, nil, generatedCandidateCleanupIdentity{}, nil, ErrScopeUnavailable
	}
	state.active++
	guard, ctx := state.guard, state.ctx
	captured := generatedCandidateCleanupIdentity{
		claim: state.claim, deploymentID: state.deploymentID, componentName: state.componentName,
		receiptID: state.receiptID, invocationID: state.invocationID,
	}
	state.mu.Unlock()
	done := func() {
		state.mu.Lock()
		state.active--
		if state.active == 0 {
			state.changed.Broadcast()
		}
		state.mu.Unlock()
	}
	if err := cancellationError(ctx, nil); err != nil {
		done()
		return nil, nil, generatedCandidateCleanupIdentity{}, nil, err
	}
	return guard, ctx, captured, done, nil
}

func (c *GeneratedCandidateCleanup) checkCurrent(ctx context.Context) error {
	if err := cancellationError(ctx, nil); err != nil {
		return err
	}
	if c == nil || c.state == nil {
		return ErrScopeUnavailable
	}
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing || state.left || state.ctx == nil || state.guard == nil {
		return ErrScopeUnavailable
	}
	return nil
}

// Leave closes new cleanup checks, cancels the bounded child context, joins
// active checks, and releases its parent scope reference exactly once. Copies
// share this liveness state.
func (c *GeneratedCandidateCleanup) Leave() {
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
	cancel := state.cancel
	state.cancel = nil
	if cancel != nil {
		cancel()
	}
	for state.active != 0 {
		state.changed.Wait()
	}
	parent := state.parent
	if parent != nil {
		parent.mu.Lock()
		if parent.children > 0 {
			parent.children--
		}
		parent.changed.Broadcast()
		state.parent = nil
		parent.mu.Unlock()
	}
	state.left = true
	state.guard = nil
	state.ctx = nil
	state.changed.Broadcast()
	state.mu.Unlock()
}

func validCandidateComponentName(value string) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}
