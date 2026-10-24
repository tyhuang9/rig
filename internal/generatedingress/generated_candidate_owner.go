package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedReplacementReservation is a sealed accounting handle. Release
// relinquishes this caller's unused shares; it cannot release a risky receipt,
// a live container, or capacity already handed to routing.
type GeneratedReplacementReservation interface {
	Release()
	generatedReplacementReservation()
}

type generatedReplacementReservationPermit struct {
	owner    *GeneratedCandidateAuthority
	pins     candidateValidationPins
	capacity *candidateCapacityReservation
	baseline candidateAdmissionSnapshot
	anchor   candidateHeadAnchor
}

func (*generatedReplacementReservationPermit) generatedReplacementReservation() {}

func (r *generatedReplacementReservationPermit) Release() {
	if r != nil && r.capacity != nil {
		_ = r.capacity.release()
	}
}

// ReserveReplacement admits the full protected roster before image creation.
// Durable risk is attached before policy, head, or phase refusal. Admission
// never grants permission to run a command or write a candidate result.
func (a *GeneratedCandidateAuthority) ReserveReplacement(ctx context.Context, request GeneratedReplacementRequest) (GeneratedReplacementReservation, error) {
	if !validGeneratedCandidateAuthority(a) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	pins, err := candidatePinsFromRequest(GeneratedCandidateRequest(request))
	if err != nil {
		return nil, err
	}
	pinned, err := a.stores.ExecutionGuard().PinScope(ctx)
	if err != nil {
		return nil, err
	}
	defer pinned.Leave()
	protected, err := a.loadCandidateProtectedSnapshot(ctx, pins)
	if err != nil {
		return nil, err
	}
	baseline, err := a.readCandidateAdmission(ctx, pins, protected)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(baseline.components))
	for index, component := range baseline.components {
		names[index] = component.plan.Name
	}
	capacity, err := a.ledger.reserve(ctx, candidateGroupSpec{
		appID: pins.claim.ApplicationID(), deploymentID: pins.deploymentID,
		pins: baseline.pins, components: names,
		limits: generatedcapacity.Limits{MemoryBytes: uint64(a.options.MemoryBytes), DiskBytes: a.options.ReplacementDiskBytes},
	})
	if err != nil {
		return nil, candidateCapacityFailure(ctx, err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = capacity.release()
		}
	}()
	if err := seedCandidateRisk(capacity, baseline); err != nil {
		return nil, err
	}
	current, err := a.checkCandidateAdmission(ctx, pinned, pins, protected, baseline)
	if riskErr := seedCandidateRisk(capacity, current); riskErr != nil {
		return nil, errors.Join(err, riskErr)
	}
	if err != nil {
		return nil, err
	}
	anchor, err := a.bindCandidateHead(current)
	if err != nil {
		return nil, err
	}
	accepted = true
	return &generatedReplacementReservationPermit{owner: a, pins: pins, capacity: capacity, baseline: current, anchor: anchor}, nil
}

func candidateCapacityFailure(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, generatedcapacity.ErrInvalidLimits):
		return candidateFailure("internal_error", err)
	case errors.Is(err, generatedcapacity.ErrInvalidCount):
		return candidateFailure("validation_failed", err)
	case errors.Is(err, generatedcapacity.ErrInsufficientCapacity):
		return candidateFailure("insufficient_replacement_capacity", err)
	case IsCode(err, DiagnosticCancelled), errors.Is(err, context.Canceled), ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		return candidateFailure("cancelled", errors.Join(err, contextError(ctx)))
	default:
		// The capacity source is a sealed Manager observation. Deadline and
		// other source failures retain the historical runtime-unavailable
		// diagnostic without disclosing process output.
		return candidateFailure("runtime_unavailable", err)
	}
}

func (a *GeneratedCandidateAuthority) RetainReplacementCapacity(value GeneratedReplacementReservation) error {
	permit, ok := value.(*generatedReplacementReservationPermit)
	if !ok || permit == nil || !validGeneratedCandidateAuthority(a) || permit.owner != a || permit.capacity == nil || permit.capacity.ledger != a.ledger {
		return deploymentexecution.ErrIdentityLost
	}
	return permit.capacity.retainAll()
}

func (a *GeneratedCandidateAuthority) readCandidateAdmission(ctx context.Context, pins candidateValidationPins, protected candidateProtectedSnapshot) (candidateAdmissionSnapshot, error) {
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return candidateAdmissionSnapshot{}, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	value, err := readCandidateAdmissionSnapshotTx(ctx, tx, pins, protected)
	if err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, gateDatabaseError(ctx, err)
	}
	return value, nil
}

func (a *GeneratedCandidateAuthority) checkCandidateAdmission(ctx context.Context, pinned *deploymentexecution.Pinned, pins candidateValidationPins, protected candidateProtectedSnapshot, baseline candidateAdmissionSnapshot) (candidateAdmissionSnapshot, error) {
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return candidateAdmissionSnapshot{}, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	value, validationErr := validateCandidateAdmissionSnapshotTx(ctx, a.stores, tx, pins, protected, baseline)
	if validationErr != nil {
		return value, validationErr
	}
	if value.runtime.phase == generatedruntimestate.PhaseSwitchingRoute && value.observedHead.deploymentID == value.headPins.deploymentID {
		value, validationErr = validateCandidateExecutionSnapshotTx(ctx, a.stores, tx, pins, protected, baseline)
		if validationErr != nil {
			return value, validationErr
		}
	}
	if err := pinned.CheckPolicyGateTx(tx, pins.claim, pins.deploymentID); err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, gateDatabaseError(ctx, err)
	}
	return value, nil
}

// Call only after closing the transaction that supplied value. Receipt
// attachment takes private ledger locks and must never run inside SQL.
func seedCandidateRisk(capacity *candidateCapacityReservation, value candidateAdmissionSnapshot) error {
	var firstErr error
	for _, component := range value.components {
		if component.receipt == nil || component.receipt.id == "" {
			if component.risky {
				firstErr = errors.Join(firstErr, capacity.retainAll())
			}
			continue
		}
		operation, err := capacity.enterReceiptAttachment(component.plan.Name)
		if err == nil {
			err = operation.attachExistingReceipt(component.receipt.id)
			operation.leave()
		}
		if err != nil {
			// An incomplete attachment cannot turn discovered risk into an
			// unused share, including when another alias currently owns it.
			firstErr = errors.Join(firstErr, err, capacity.retainAll())
		}
	}
	return firstErr
}

func (a *GeneratedCandidateAuthority) bindCandidateHead(value candidateAdmissionSnapshot) (candidateHeadAnchor, error) {
	if value.runtime.phase == generatedruntimestate.PhaseSwitchingRoute && value.observedHead.deploymentID == value.headPins.deploymentID {
		if len(value.components) == 0 {
			return candidateHeadAnchor{}, ErrInvalidDeployment
		}
		for _, component := range value.components {
			if component.state != generatedruntimestate.ComponentActive {
				return candidateHeadAnchor{}, deploymentexecution.ErrStageInvalid
			}
		}
		return a.heads.bindSelf(value.headPins, value.observedHead)
	}
	return a.heads.bindPredecessor(value.headPins, value.observedHead)
}

// GeneratedCandidateExecution holds the original scope until Leave joins all
// operations and cleanup children. No method accepts a replacement context.
type GeneratedCandidateExecution struct {
	state *generatedCandidateExecutionState
}

type generatedCandidateExecutionState struct {
	mu                    sync.Mutex
	changed               *sync.Cond
	authority             *GeneratedCandidateAuthority
	ctx                   context.Context
	pinned                *deploymentexecution.Pinned
	reservation           *generatedReplacementReservationPermit
	pins                  candidateValidationPins
	protected             candidateProtectedSnapshot
	baseline              candidateAdmissionSnapshot
	anchor                candidateHeadAnchor
	invocations           map[string]*generatedCandidateInvocationPermit
	active, closing, left bool
}

// GeneratedCandidateInvocation is sealed. Even an interface wrapper embedding
// a genuine handle is rejected by the concrete pointer identity checks.
type GeneratedCandidateInvocation interface {
	Identity() GeneratedCandidateInvocationIdentity
	generatedCandidateInvocation()
}

type generatedCandidateInvocationPermit struct {
	owner              *generatedCandidateExecutionState
	identity           GeneratedCandidateInvocationIdentity
	invocationID       string
	cleanup            *deploymentexecution.GeneratedCandidateCleanup
	cleanupCtx         context.Context
	cancelCleanup      context.CancelFunc
	createdHere        bool
	environment        GeneratedCandidateEnvironmentLease
	environmentClean   bool
	containerID        string
	policy             candidatePolicy
	receipt            candidateIntentSnapshot
	createInvoked      bool
	mutationUnquiesced bool
	absenceConfirmed   bool
	observedFailure    generatedruntimestate.DiagnosticCode
}

func (*generatedCandidateInvocationPermit) generatedCandidateInvocation() {}
func (p *generatedCandidateInvocationPermit) Identity() GeneratedCandidateInvocationIdentity {
	if p == nil {
		return GeneratedCandidateInvocationIdentity{}
	}
	return p.identity
}

func (a *GeneratedCandidateAuthority) PinCandidates(ctx context.Context, request GeneratedCandidateRequest, value GeneratedReplacementReservation) (*GeneratedCandidateExecution, error) {
	permit, ok := value.(*generatedReplacementReservationPermit)
	if !ok || permit == nil || !validGeneratedCandidateAuthority(a) || permit.owner != a || permit.capacity == nil || permit.capacity.ledger != a.ledger {
		return nil, deploymentexecution.ErrIdentityLost
	}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		return nil, err
	}
	if !sameCandidatePins(pins, permit.pins) {
		return nil, deploymentexecution.ErrIdentityLost
	}
	if err := permit.capacity.enterActivity(); err != nil {
		return nil, err
	}
	defer permit.capacity.leaveActivity()
	pinned, err := a.stores.ExecutionGuard().PinScope(ctx)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			pinned.Leave()
		}
	}()
	protected, err := a.loadCandidateProtectedSnapshot(ctx, pins)
	if err != nil {
		return nil, err
	}
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, gateDatabaseError(ctx, err)
	}
	current, validationErr := validateCandidateExecutionSnapshotTx(ctx, a.stores, tx, pins, protected, permit.baseline)
	if validationErr == nil {
		validationErr = pinned.CheckCandidateObservationTx(tx, pins.claim, pins.deploymentID)
	}
	if validationErr == nil {
		validationErr = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	// All locks and budget work occur after the read transaction is closed,
	// including the refusal path for newly discovered durable risk.
	_ = tx.Rollback()
	if riskErr := seedCandidateRisk(permit.capacity, current); riskErr != nil {
		return nil, errors.Join(validationErr, riskErr)
	}
	if validationErr != nil {
		return nil, validationErr
	}
	anchor, err := a.bindCandidateHead(current)
	if err != nil {
		return nil, err
	}
	s := &generatedCandidateExecutionState{authority: a, ctx: ctx, pinned: pinned, reservation: permit, pins: pins, protected: protected, baseline: current, anchor: anchor, invocations: make(map[string]*generatedCandidateInvocationPermit)}
	s.changed = sync.NewCond(&s.mu)
	accepted = true
	return &GeneratedCandidateExecution{state: s}, nil
}

func sameCandidatePins(a, b candidateValidationPins) bool {
	return a.claim == b.claim && a.deploymentID == b.deploymentID && a.releaseID == b.releaseID &&
		a.configurationRevisionID == b.configurationRevisionID && a.configurationRevisionNumber == b.configurationRevisionNumber &&
		a.deploymentPlanRevisionID == b.deploymentPlanRevisionID && a.deploymentPlanRevisionNumber == b.deploymentPlanRevisionNumber &&
		a.candidateSlot == b.candidateSlot && deployments.SameGeneratedOwnerFindingSet(a.findings, b.findings)
}

func (e *GeneratedCandidateExecution) begin(cleanup bool) (*generatedCandidateExecutionState, func(), error) {
	if e == nil || e.state == nil {
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	s := e.state
	s.mu.Lock()
	if s.closing || s.left || s.active || s.pinned == nil {
		s.mu.Unlock()
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	s.active = true
	s.mu.Unlock()
	done := func() { s.mu.Lock(); s.active = false; s.changed.Broadcast(); s.mu.Unlock() }
	if !cleanup {
		if err := contextError(s.ctx); err != nil {
			done()
			return nil, nil, err
		}
	}
	return s, done, nil
}

func (s *generatedCandidateExecutionState) invocation(value GeneratedCandidateInvocation) (*generatedCandidateInvocationPermit, error) {
	p, ok := value.(*generatedCandidateInvocationPermit)
	if !ok || p == nil || p.owner != s || s.invocations[p.identity.ComponentName] != p || p.identity.CurrentClaim != s.pins.claim || p.identity.DeploymentID != s.pins.deploymentID {
		return nil, deploymentexecution.ErrIdentityLost
	}
	return p, nil
}

func (e *GeneratedCandidateExecution) Leave() {
	if e == nil || e.state == nil {
		return
	}
	s := e.state
	s.mu.Lock()
	if s.closing {
		for !s.left {
			s.changed.Wait()
		}
		s.mu.Unlock()
		return
	}
	s.closing = true
	for s.active {
		s.changed.Wait()
	}
	s.mu.Unlock()
	for _, p := range s.invocations {
		if !p.mutationUnquiesced {
			_ = p.cleanupEnvironment()
		}
		if p.cancelCleanup != nil {
			p.cancelCleanup()
		}
		if p.cleanup != nil {
			p.cleanup.Leave()
		}
	}
	s.pinned.Leave()
	s.mu.Lock()
	s.pinned = nil
	s.left = true
	s.changed.Broadcast()
	s.mu.Unlock()
}

// BeginCreate commits receipt and Starting together. Only an acknowledged
// commit can return the original create invocation; commit ambiguity can
// never be repaired by minting a second invocation from durable intent.
func (e *GeneratedCandidateExecution) BeginCreate(component string) (GeneratedCandidateInvocation, error) {
	s, done, err := e.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	if s.invocations[component] != nil {
		return nil, ErrInvalidTransition
	}
	operation, err := s.reservation.capacity.enterReceiptAttachment(component)
	if err != nil {
		return nil, err
	}
	defer operation.leave()
	if operation.receipt != nil {
		return nil, ErrInvalidTransition
	}
	current, err := s.observeCandidateState(false)
	if err != nil {
		return nil, err
	}
	member, err := candidateMember(current, component)
	if err != nil {
		return nil, err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate || member.state != generatedruntimestate.ComponentImageReady || member.receipt != nil || member.containerName != "" || member.containerID != "" || member.artifact == nil {
		return nil, deploymentexecution.ErrStageInvalid
	}
	receipt := candidateIntentSnapshot{
		id: uuid.NewString(), creatorJobID: s.pins.claim.JobID(), creatorWorkerAttempt: s.pins.claim.Attempt(),
		configurationRevisionID: s.pins.configurationRevisionID, configurationRevisionNumber: s.pins.configurationRevisionNumber,
		imageArtifactID: member.artifact.id, imageArtifactAttempt: member.artifact.attemptNumber,
		environmentOperationID: member.artifact.id, environmentStagingAttempt: s.pins.claim.Attempt(),
	}
	policy, err := buildCandidatePolicy(s.pins, member.plan, *member.artifact, receipt, s.authority.options)
	if err != nil {
		return nil, err
	}
	receipt.policyVersion, receipt.policyDigest = policy.version, policy.digest
	invocationID := uuid.NewString()
	cleanup, err := s.pinned.DeriveGeneratedCandidateCleanup(s.pins.claim, s.pins.deploymentID, component, receipt.id, invocationID)
	if err != nil {
		return nil, err
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(s.ctx), 70*time.Minute)
	accepted := false
	defer func() {
		if !accepted {
			cancelCleanup()
			cleanup.Leave()
		}
	}()
	tx, current, err := s.candidateWriter(candidateBoundaryCreate, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	member, err = candidateMember(current, component)
	if err != nil {
		return nil, err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate || member.state != generatedruntimestate.ComponentImageReady || member.receipt != nil || member.containerName != "" || member.containerID != "" {
		return nil, deploymentexecution.ErrStageInvalid
	}
	input := generatedruntimestate.CandidateIntentInput{
		ReceiptID: receipt.id, AppID: s.pins.claim.ApplicationID(), DeploymentID: s.pins.deploymentID, ComponentName: component,
		ContainerName: policy.identity.ContainerName, CreatorJobID: receipt.creatorJobID, CreatorWorkerAttempt: receipt.creatorWorkerAttempt,
		ConfigurationRevisionID: receipt.configurationRevisionID, ConfigurationRevisionNumber: receipt.configurationRevisionNumber,
		ImageArtifactID: receipt.imageArtifactID, ImageArtifactAttempt: receipt.imageArtifactAttempt,
		EnvironmentOperationID: receipt.environmentOperationID, EnvironmentStagingAttempt: receipt.environmentStagingAttempt,
		CandidatePolicyVersion: receipt.policyVersion, CandidatePolicyDigest: receipt.policyDigest,
	}
	if err := generatedruntimestate.New(s.authority.stores.Database()).BeginCandidateTx(s.ctx, tx, input); err != nil {
		return nil, err
	}
	commitErr := tx.Commit()
	commit := candidateReceiptCommitAcknowledged
	if commitErr != nil {
		commit = candidateReceiptCommitUnknown
	}
	// The SQL transaction is closed before touching the accounting mutex.
	if err := operation.recordNewReceipt(receipt.id, commit); err != nil {
		_ = s.reservation.capacity.retainAll()
		return nil, errors.Join(commitErr, err)
	}
	if commitErr != nil {
		return nil, gateDatabaseError(s.ctx, commitErr)
	}
	p := &generatedCandidateInvocationPermit{
		owner: s, identity: GeneratedCandidateInvocationIdentity{CurrentClaim: s.pins.claim, DeploymentID: s.pins.deploymentID, ComponentName: component,
			ReceiptID: receipt.id, CreatorJobID: receipt.creatorJobID, CreatorWorkerAttempt: receipt.creatorWorkerAttempt},
		invocationID: invocationID, cleanup: cleanup, cleanupCtx: cleanupCtx, cancelCleanup: cancelCleanup, createdHere: true,
		policy: policy, receipt: receipt, environmentClean: true,
	}
	s.invocations[component] = p
	accepted = true
	return p, nil
}

type candidateBoundary uint8

const (
	candidateBoundaryCreate candidateBoundary = iota + 1
	candidateBoundaryStart
	candidateBoundaryObservation
	candidateBoundaryResult
)

func candidateMember(value candidateAdmissionSnapshot, name string) (candidateAdmissionComponent, error) {
	for _, member := range value.components {
		if member.plan.Name == name {
			return member, nil
		}
	}
	return candidateAdmissionComponent{}, deploymentexecution.ErrIdentityLost
}

// Protected file reads finish before opening SQL. This helper grants no
// command authority and performs no accounting transition.
func (s *generatedCandidateExecutionState) observeCandidateState(route bool) (candidateAdmissionSnapshot, error) {
	protected, err := s.authority.loadCandidateProtectedSnapshot(s.ctx, s.pins)
	if err != nil {
		return candidateAdmissionSnapshot{}, err
	}
	tx, err := s.authority.stores.Database().BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return candidateAdmissionSnapshot{}, gateDatabaseError(s.ctx, err)
	}
	defer tx.Rollback()
	current, err := validateCandidateExecutionSnapshotTx(s.ctx, s.authority.stores, tx, s.pins, protected, s.baseline)
	if err != nil {
		return current, err
	}
	if err := s.pinned.CheckCandidateObservationTx(tx, s.pins.claim, s.pins.deploymentID); err != nil {
		return current, err
	}
	if err := s.validateCandidateHeadAndPolicy(current, route); err != nil {
		return current, err
	}
	if err := tx.Commit(); err != nil {
		return current, gateDatabaseError(s.ctx, err)
	}
	return current, nil
}

// candidateWriter acquires SQLite's writer before authority reads. Callers
// may only execute their fixed result helper and commit before any I/O,
// ledger transition, or command entry.
func (s *generatedCandidateExecutionState) candidateWriter(boundary candidateBoundary, route bool) (*sql.Tx, candidateAdmissionSnapshot, error) {
	protected, err := s.authority.loadCandidateProtectedSnapshot(s.ctx, s.pins)
	if err != nil {
		return nil, candidateAdmissionSnapshot{}, err
	}
	tx, err := s.authority.stores.Database().BeginTx(s.ctx, nil)
	if err != nil {
		return nil, candidateAdmissionSnapshot{}, gateDatabaseError(s.ctx, err)
	}
	fail := func(value candidateAdmissionSnapshot, err error) (*sql.Tx, candidateAdmissionSnapshot, error) {
		_ = tx.Rollback()
		return nil, value, err
	}
	if _, err := tx.ExecContext(s.ctx, `UPDATE jobs SET id=id WHERE 0`); err != nil {
		return fail(candidateAdmissionSnapshot{}, gateDatabaseError(s.ctx, err))
	}
	switch boundary {
	case candidateBoundaryCreate:
		err = s.pinned.CheckBeforeCandidateCreateTx(tx, s.pins.claim, s.pins.deploymentID)
	case candidateBoundaryStart:
		err = s.pinned.CheckBeforeCandidateStartTx(tx, s.pins.claim, s.pins.deploymentID)
	case candidateBoundaryObservation:
		err = s.pinned.CheckCandidateObservationTx(tx, s.pins.claim, s.pins.deploymentID)
	case candidateBoundaryResult:
		err = s.pinned.CheckCandidateResultTx(tx, s.pins.claim, s.pins.deploymentID)
	default:
		err = deploymentexecution.ErrStageInvalid
	}
	if err != nil {
		return fail(candidateAdmissionSnapshot{}, err)
	}
	current, err := validateCandidateExecutionSnapshotTx(s.ctx, s.authority.stores, tx, s.pins, protected, s.baseline)
	if err != nil {
		return fail(current, err)
	}
	if err := s.validateCandidateHeadAndPolicy(current, route); err != nil {
		return fail(current, err)
	}
	if err := contextError(s.ctx); err != nil {
		return fail(current, err)
	}
	return tx, current, nil
}

func (s *generatedCandidateExecutionState) validateCandidateHeadAndPolicy(current candidateAdmissionSnapshot, route bool) error {
	var err error
	if route {
		err = s.anchor.checkRouteObservation(current.observedHead)
	} else {
		err = s.anchor.checkPredecessor(current.observedHead)
	}
	if err != nil {
		return err
	}
	for _, member := range current.components {
		if member.receipt == nil {
			continue
		}
		if member.artifact == nil {
			return deploymentexecution.ErrIdentityLost
		}
		policy, err := buildCandidatePolicy(s.pins, member.plan, *member.artifact, *member.receipt, s.authority.options)
		if err != nil {
			return err
		}
		if member.receipt.policyVersion != policy.version || member.receipt.policyDigest != policy.digest {
			return deploymentexecution.ErrIdentityLost
		}
	}
	return nil
}
