package generatedingress

import (
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// RecordObservedFailure records only a determinate pre-route failure. Every
// receipt-backed member must be an original invocation whose private cleanup
// proved the candidate absent and removed its staged environment.
func (e *GeneratedCandidateExecution) RecordObservedFailure() (generatedruntimestate.Deployment, error) {
	s, done, err := e.begin(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer done()

	current, err := s.observeCandidateState(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	diagnostic, err := candidateObservedFailureDiagnostic(current)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}

	expected := make(map[string]string, len(current.components))
	attempted := 0
	failureObserved := false
	for _, member := range current.components {
		name := member.plan.Name
		if member.receipt == nil {
			if member.state != generatedruntimestate.ComponentImageReady || s.invocations[name] != nil {
				return generatedruntimestate.Deployment{}, deploymentexecution.ErrIdentityLost
			}
			expected[name] = ""
			continue
		}

		p := s.invocations[name]
		if p == nil {
			return generatedruntimestate.Deployment{}, deploymentexecution.ErrIdentityLost
		}
		if _, err := s.invocation(p); err != nil {
			return generatedruntimestate.Deployment{}, err
		}
		if !p.createdHere || p.cleanup == nil || p.cleanupCtx == nil || p.invocationID == "" ||
			!p.absenceConfirmed || p.mutationUnquiesced || !p.environmentClean || p.environment != nil ||
			p.receipt != *member.receipt || p.identity.ReceiptID != member.receipt.id ||
			p.policy.identity.ContainerName != member.containerName ||
			(member.containerID != "" && p.containerID != member.containerID) ||
			(p.observedFailure != "" && p.observedFailure != diagnostic) {
			return generatedruntimestate.Deployment{}, ErrInvalidTransition
		}
		failureObserved = failureObserved || p.observedFailure == diagnostic
		expected[name] = member.receipt.id
		attempted++
	}
	if attempted == 0 || !failureObserved || len(s.invocations) != attempted {
		return generatedruntimestate.Deployment{}, ErrInvalidTransition
	}

	// This is the failure path's single whole-roster winner point. It refuses
	// an existing cleanup/route latch and does not infer absence from SQL.
	roster, err := s.reservation.capacity.enterObservedFailureRoster(expected)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer roster.leave()

	tx, latest, err := s.candidateWriter(candidateBoundaryResult, false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer tx.Rollback()
	latestDiagnostic, err := candidateObservedFailureDiagnostic(latest)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if latestDiagnostic != diagnostic || !sameCandidateFailureRoster(current, latest) {
		return generatedruntimestate.Deployment{}, deploymentexecution.ErrIdentityLost
	}

	state := generatedruntimestate.New(s.authority.stores.Database())
	if err := state.RecordCandidateObservedFailureTx(
		s.ctx,
		tx,
		s.pins.claim.ApplicationID(),
		s.pins.deploymentID,
		diagnostic,
	); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.Deployment{}, gateDatabaseError(s.ctx, err)
	}
	return state.Get(s.ctx, s.pins.claim.ApplicationID(), s.pins.deploymentID)
}

func candidateObservedFailureDiagnostic(value candidateAdmissionSnapshot) (generatedruntimestate.DiagnosticCode, error) {
	switch {
	case value.runtime.phase == generatedruntimestate.PhaseStartingCandidate && value.main.status == Applying:
		return generatedruntimestate.DiagnosticStartFailed, nil
	case value.runtime.phase == generatedruntimestate.PhaseWaitingHealth && value.main.status == WaitingHealth:
		return generatedruntimestate.DiagnosticHealthFailed, nil
	default:
		return "", deploymentexecution.ErrStageInvalid
	}
}

func sameCandidateFailureRoster(before, after candidateAdmissionSnapshot) bool {
	if len(before.components) == 0 || len(before.components) != len(after.components) {
		return false
	}
	for index, first := range before.components {
		last := after.components[index]
		if first.plan != last.plan || first.slot != last.slot || first.state != last.state ||
			first.selectedArtifactID != last.selectedArtifactID || first.containerName != last.containerName ||
			first.containerID != last.containerID || first.risky != last.risky ||
			!sameCandidateFailureArtifact(first.artifact, last.artifact) ||
			!sameCandidateFailureReceipt(first.receipt, last.receipt) {
			return false
		}
	}
	return true
}

func sameCandidateFailureArtifact(first, last *candidateArtifactSnapshot) bool {
	return first == nil && last == nil || first != nil && last != nil && *first == *last
}

func sameCandidateFailureReceipt(first, last *candidateIntentSnapshot) bool {
	return first == nil && last == nil || first != nil && last != nil && *first == *last
}

// enterObservedFailureRoster converts a complete private absence proof into a
// one-shot whole-roster exclusion. It never changes a receipt, physical fact,
// or charge and never releases capacity.
func (r *candidateCapacityReservation) enterObservedFailureRoster(
	expected map[string]string,
) (*candidateRosterOperation, error) {
	if len(expected) == 0 {
		return nil, errCandidateCapacityInvalid
	}
	if err := r.enterActivity(); err != nil {
		return nil, err
	}

	g := r.group
	g.mu.Lock()
	fail := func(err error) (*candidateRosterOperation, error) {
		g.mu.Unlock()
		r.leaveActivity()
		return nil, err
	}
	if g.reserving || g.cleanupClosed || g.activeCleanup != 0 || len(expected) != len(g.components) {
		return fail(errCandidateCapacityBusy)
	}
	attempted := 0
	for _, name := range g.components {
		receiptID, ok := expected[name]
		if !ok {
			return fail(errCandidateReceiptMismatch)
		}
		binding := r.bindings[name]
		if binding == nil || binding.component != g.byName[name] || binding.component.tokenHeld {
			return fail(errCandidateCapacityStale)
		}
		if receiptID == "" {
			if !validCandidateBindingLocked(binding) || binding.component.receipt != nil {
				return fail(errCandidateCapacityStale)
			}
			continue
		}

		attempted++
		receipt := binding.component.receipt
		if !exactCandidateBindingLocked(binding) || binding.attached || binding.creatorReceipt != receipt ||
			receipt == nil || receipt.id != receiptID || !binding.charge.logicallyRetired || binding.charge.refs != 0 ||
			binding.charge.state == candidateChargeRetiring {
			return fail(errCandidateCapacityStale)
		}
		word := receipt.state.Load()
		if word&candidateReceiptAcknowledged == 0 || word&candidateReceiptUnknown != 0 ||
			word&candidateReceiptUnquiesced != 0 || candidatePhysicalFromWord(word) != candidatePhysicalConfirmedAbsent {
			return fail(errCandidatePhysicalTransition)
		}
	}
	if attempted == 0 {
		return fail(errCandidateCapacityInvalid)
	}

	// Closing only after complete validation makes refusal side-effect free.
	// The latch prevents another execution alias from handing this roster to
	// route after the absence proof and before the failure transaction.
	g.cleanupClosed = true
	g.changed.Broadcast()
	tokens := make([]candidateRosterToken, 0, len(g.components))
	for _, name := range g.components {
		component := r.bindings[name].component
		component.tokenHeld = true
		component.tokenGeneration++
		if component.tokenGeneration == 0 {
			component.tokenGeneration++
		}
		tokens = append(tokens, candidateRosterToken{component: component, generation: component.tokenGeneration})
	}
	operation := &candidateRosterOperation{
		reservation: r,
		group:       g,
		lifecycle:   &candidateOperationLifecycle{},
		tokens:      tokens,
	}
	g.mu.Unlock()
	return operation, nil
}
