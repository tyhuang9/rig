package deploymentexecution

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

type PositiveFenceTxValidator interface {
	ConfiguredFor(*sql.DB) bool
	PendingStartExistsTx(context.Context, *sql.Tx) (bool, error)
}

type Guard struct {
	db               *sql.DB
	bindings         *deploymentbinding.Repository
	positiveFence    PositiveFenceTxValidator
	workingDirectory string
}

func NewGuard(db *sql.DB, bindings *deploymentbinding.Repository, positiveFence PositiveFenceTxValidator, workingDirectory string) (*Guard, error) {
	guard := &Guard{db: db, bindings: bindings, positiveFence: positiveFence, workingDirectory: workingDirectory}
	if !guard.UsesDatabase(db) {
		return nil, ErrInvalidConfiguration
	}
	return guard, nil
}

func (g *Guard) UsesDatabase(db *sql.DB) bool {
	return g != nil && db != nil && g.db == db && g.bindings != nil && g.bindings.UsesDatabase(db) &&
		!nilInterface(g.positiveFence) && g.positiveFence.ConfiguredFor(db) && validWorkingDirectory(g.workingDirectory)
}

func validWorkingDirectory(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type Pinned struct {
	state *pinnedState
}

type pinnedState struct {
	mu      sync.Mutex
	changed *sync.Cond
	guard   *Guard
	ctx     context.Context
	leave   func()
	active  int
	closing bool
	left    bool
}

func (g *Guard) PinScope(ctx context.Context) (*Pinned, error) {
	if err := cancellationError(ctx, nil); err != nil {
		return nil, err
	}
	if g == nil || !g.UsesDatabase(g.db) {
		return nil, ErrInvalidConfiguration
	}
	leave, err := deploymenteffects.EnterScope(ctx, g.workingDirectory)
	if err != nil {
		if canceled := cancellationError(ctx, err); canceled != nil {
			return nil, canceled
		}
		return nil, ErrScopeUnavailable
	}
	if err := cancellationError(ctx, nil); err != nil {
		leave()
		return nil, err
	}
	state := &pinnedState{guard: g, ctx: ctx, leave: leave}
	state.changed = sync.NewCond(&state.mu)
	return &Pinned{state: state}, nil
}

func (p *Pinned) Leave() {
	if p == nil || p.state == nil {
		return
	}
	state := p.state
	state.mu.Lock()
	if state.closing {
		for !state.left {
			state.changed.Wait()
		}
		state.mu.Unlock()
		return
	}
	state.closing = true
	for state.active != 0 {
		state.changed.Wait()
	}
	leave := state.leave
	state.leave = nil
	state.mu.Unlock()
	if leave != nil {
		leave()
	}
	state.mu.Lock()
	state.left = true
	state.guard = nil
	state.ctx = nil
	state.changed.Broadcast()
	state.mu.Unlock()
}

func (p *Pinned) CheckPolicyGateTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, policyGateStatus)
}

// CheckComposeUpTx revalidates the original execution authority at the fixed
// Compose mutation boundary. An acknowledged waiting-health deployment can
// never authorize another up command.
func (p *Pinned) CheckComposeUpTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, composeUpStatus)
}

// CheckComposeObservationTx revalidates the original execution authority for
// the two fixed post-command observations. The repository applies the exact
// transition-specific status check in the same transaction.
func (p *Pinned) CheckComposeObservationTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, composeObservationStatus)
}

// CheckGeneratedNetworkCreateTx revalidates the original execution authority
// at the fixed generated application-network create boundary.
func (p *Pinned) CheckGeneratedNetworkCreateTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, generatedNetworkStatus)
}

// CheckGeneratedNetworkObservationTx revalidates the original execution
// authority before accepting an exact generated application network.
func (p *Pinned) CheckGeneratedNetworkObservationTx(tx *sql.Tx, claim Claim, deploymentID string) error {
	return p.checkTx(tx, claim, deploymentID, generatedNetworkStatus)
}

func (p *Pinned) checkTx(tx *sql.Tx, claim Claim, deploymentID string, statusAllowed func(string) bool) error {
	guard, ctx, done, err := p.beginCheck()
	if err != nil {
		return err
	}
	defer done()
	if tx == nil || !guard.UsesDatabase(guard.db) {
		return ErrInvalidConfiguration
	}
	if deploymentID == "" || !claim.valid() {
		return ErrInvalidClaim
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}
	classification, err := guard.bindings.CheckTx(ctx, tx, claim.binding)
	if err != nil {
		return bindingError(ctx, err)
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}
	switch classification {
	case deploymentbinding.ClassificationCurrent:
	case deploymentbinding.ClassificationStale:
		return ErrAuthorityStale
	default:
		return ErrInvalidConfiguration
	}
	var jobType, resourceType, resourceID, status, phase string
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT type,resource_type,resource_id,status,phase,attempt FROM jobs WHERE id=?`, claim.jobID).
		Scan(&jobType, &resourceType, &resourceID, &status, &phase, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}
	if jobType != "deploy" || resourceType != "application" || resourceID != claim.applicationID ||
		attempt != claim.attempt || status != "running" || phase == "cancelling" {
		return ErrIdentityLost
	}
	var deploymentStatus string
	var provenanceInitialized bool
	err = tx.QueryRowContext(ctx, `SELECT status,provenance_initialized FROM deployments
		WHERE id=? AND app_id=? AND job_id=?`, deploymentID, claim.applicationID, claim.jobID).
		Scan(&deploymentStatus, &provenanceInitialized)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIdentityLost
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}
	if !provenanceInitialized || statusAllowed == nil || !statusAllowed(deploymentStatus) {
		return ErrStageInvalid
	}
	pending, err := guard.positiveFence.PendingStartExistsTx(ctx, tx)
	if err != nil {
		return validatorError(ctx, err)
	}
	if err := p.checkCurrent(ctx); err != nil {
		return err
	}
	if pending {
		return ErrPositiveFence
	}
	return p.checkCurrent(ctx)
}

func composeUpStatus(status string) bool { return status == "applying" }

func composeObservationStatus(status string) bool {
	return status == "applying" || status == "waiting_health"
}

func generatedNetworkStatus(status string) bool { return status == "applying" }

func policyGateStatus(status string) bool {
	switch status {
	case "preparing", "needs_attention", "applying", "waiting_health":
		return true
	default:
		return false
	}
}

func (p *Pinned) beginCheck() (*Guard, context.Context, func(), error) {
	if p == nil || p.state == nil {
		return nil, nil, nil, ErrScopeUnavailable
	}
	state := p.state
	state.mu.Lock()
	if state.closing || state.left || state.guard == nil || state.ctx == nil {
		state.mu.Unlock()
		return nil, nil, nil, ErrScopeUnavailable
	}
	state.active++
	guard, ctx := state.guard, state.ctx
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
		return nil, nil, nil, err
	}
	return guard, ctx, done, nil
}

func (p *Pinned) checkCurrent(ctx context.Context) error {
	if err := cancellationError(ctx, nil); err != nil {
		return err
	}
	if p == nil || p.state == nil {
		return ErrScopeUnavailable
	}
	state := p.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing || state.left || state.ctx == nil || state.guard == nil {
		return ErrScopeUnavailable
	}
	return nil
}

func bindingError(ctx context.Context, err error) error {
	if canceled := cancellationError(ctx, err); canceled != nil {
		return canceled
	}
	switch {
	case errors.Is(err, deploymentbinding.ErrLifecycleUnresolved):
		return errors.Join(ErrAuthorityUnresolved, deploymentbinding.ErrLifecycleUnresolved)
	case errors.Is(err, deploymentbinding.ErrBindingNotFound):
		return errors.Join(ErrAuthorityInvalid, deploymentbinding.ErrBindingNotFound)
	case errors.Is(err, deploymentbinding.ErrInvalidBinding):
		return errors.Join(ErrAuthorityInvalid, deploymentbinding.ErrInvalidBinding)
	case errors.Is(err, deploymentbinding.ErrUnavailable):
		return errors.Join(ErrUnavailable, deploymentbinding.ErrUnavailable)
	case errors.Is(err, deploymentbinding.ErrInvalidConfiguration):
		return errors.Join(ErrInvalidConfiguration, deploymentbinding.ErrInvalidConfiguration)
	default:
		return ErrInvalidConfiguration
	}
}

func validatorError(ctx context.Context, err error) error {
	if canceled := cancellationError(ctx, err); canceled != nil {
		return canceled
	}
	for _, known := range []error{ErrAuthorityInvalid, ErrUnavailable, ErrInvalidConfiguration} {
		if errors.Is(err, known) {
			return err
		}
	}
	return ErrInvalidConfiguration
}

func storageError(ctx context.Context, err error) error {
	if canceled := cancellationError(ctx, err); canceled != nil {
		return canceled
	}
	return ErrUnavailable
}

func cancellationError(ctx context.Context, err error) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	if ctx.Err() != nil {
		return errors.Join(ErrCanceled, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, deploymentbinding.ErrCanceled) || errors.Is(err, ErrCanceled) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return errors.Join(ErrCanceled, err)
		}
		return ErrCanceled
	}
	return nil
}
