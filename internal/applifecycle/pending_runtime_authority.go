package applifecycle

import (
	"context"
	"sort"
)

// RuntimePendingAuthority is a canonical repository read of the immutable
// operation, exact unreceipted intent and current running-worker authority.
// Callers must hold the shared effects exclusion and re-read it immediately
// before and after a physical effect.
type RuntimePendingAuthority struct {
	Operation Operation
	Intent    Intent
	Pending   PendingAuthority
}

// RuntimeStartAuthority is one member of the exact canonical start roster.
// Receipt is non-nil for immutable history and Pending is valid only when the
// receipt is absent. Historical members can be observed but never reissued.
type RuntimeStartAuthority struct {
	Operation Operation
	Intent    Intent
	Pending   PendingAuthority
	Receipt   *Receipt
}

func (a RuntimeStartAuthority) IsPending() bool {
	return a.Receipt == nil && a.Pending.JobID() != "" && a.Pending.IntentDigest() == a.Intent.RequestDigest
}

// PendingRuntimeBoundary authorizes only retained container boundaries. It
// never accepts caller-provided pins or an intent object as authority.
func (r *Repository) PendingRuntimeBoundary(ctx context.Context, claim WorkerClaim, boundary Boundary, component string) (RuntimePendingAuthority, error) {
	if r == nil || claim.JobID() == "" || claim.Attempt() < 1 {
		return RuntimePendingAuthority{}, ErrEffectNotAuthorized
	}
	switch boundary {
	case BoundaryStop, BoundaryStart:
		if component == "" {
			return RuntimePendingAuthority{}, ErrEffectNotAuthorized
		}
	case BoundaryHealth:
		if component != "" {
			return RuntimePendingAuthority{}, ErrEffectNotAuthorized
		}
	default:
		return RuntimePendingAuthority{}, ErrEffectNotAuthorized
	}
	operation, err := r.Operation(ctx, claim.JobID())
	if err != nil {
		return RuntimePendingAuthority{}, err
	}
	intent, err := intentByKey(ctx, r.db, operation.JobID, boundary, component)
	if err != nil {
		return RuntimePendingAuthority{}, err
	}
	spec, ok := effectSpecFor(operation, intent.Key)
	if !ok || validateIntentForOperation(operation, intent, spec) != nil {
		return RuntimePendingAuthority{}, ErrEffectNotAuthorized
	}
	pending, err := r.AuthorizePending(ctx, claim, intent)
	if err != nil {
		return RuntimePendingAuthority{}, err
	}
	return RuntimePendingAuthority{Operation: operation, Intent: intent, Pending: pending}, nil
}

// PendingRuntimeStarts authorizes the complete start roster for one running
// operation. Any unreceipted start owned by another operation fails closed;
// malformed history also fails. The exact expected roster must already exist.
func (r *Repository) PendingRuntimeStarts(ctx context.Context, claim WorkerClaim) ([]RuntimeStartAuthority, error) {
	if r == nil || claim.JobID() == "" || claim.Attempt() < 1 {
		return nil, ErrEffectNotAuthorized
	}
	operation, err := r.Operation(ctx, claim.JobID())
	if err != nil {
		return nil, err
	}
	expected := make([]string, 0, len(operation.Pins.Components))
	for _, component := range operation.Pins.Components {
		if _, ok := effectSpecFor(operation, EffectKey{Boundary: BoundaryStart, ComponentName: component.Name}); ok {
			expected = append(expected, component.Name)
		}
	}
	sort.Strings(expected)
	if len(expected) == 0 {
		return nil, ErrEffectNotAuthorized
	}
	result := make([]RuntimeStartAuthority, 0, len(expected))
	err = r.scanStartIntents(ctx, func(owner Operation, intent Intent, receipt *Receipt) (bool, error) {
		if owner.JobID != operation.JobID {
			if receipt == nil {
				return false, ErrEffectNotAuthorized
			}
			return false, nil
		}
		member := RuntimeStartAuthority{Operation: owner, Intent: intent, Receipt: receipt}
		if receipt == nil {
			pending, loadErr := r.AuthorizePending(ctx, claim, intent)
			if loadErr != nil {
				return false, loadErr
			}
			member.Pending = pending
		}
		result = append(result, member)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Intent.Key.ComponentName < result[j].Intent.Key.ComponentName })
	if len(result) != len(expected) {
		return nil, ErrEffectNotAuthorized
	}
	for index := range result {
		member := result[index]
		if member.Intent.Key.ComponentName != expected[index] || member.Operation.JobID != operation.JobID ||
			member.Intent.JobID != operation.JobID || member.Intent.Key.Boundary != BoundaryStart {
			return nil, ErrEffectNotAuthorized
		}
		persisted, loadErr := intentByKey(ctx, r.db, operation.JobID, BoundaryStart, expected[index])
		if loadErr != nil || !sameIntentIdentity(member.Intent, persisted) {
			return nil, ErrEffectNotAuthorized
		}
	}
	return result, nil
}

func SameRuntimeStartAuthority(left, right RuntimeStartAuthority) bool {
	if left.Operation.JobID != right.Operation.JobID || left.Operation.ApplicationID != right.Operation.ApplicationID ||
		left.Operation.DesiredRevision != right.Operation.DesiredRevision || left.Operation.ExpectedRevision != right.Operation.ExpectedRevision ||
		left.Operation.Action != right.Operation.Action || left.Operation.ActorID != right.Operation.ActorID ||
		left.Operation.IdempotencyKey != right.Operation.IdempotencyKey || left.Operation.PinsDigest != right.Operation.PinsDigest ||
		left.Intent.JobID != right.Intent.JobID || left.Intent.Key != right.Intent.Key || left.Intent.WriterAttempt != right.Intent.WriterAttempt ||
		left.Intent.RequestDigest != right.Intent.RequestDigest ||
		(left.Receipt == nil) != (right.Receipt == nil) {
		return false
	}
	if left.Receipt != nil {
		return left.Receipt.JobID == right.Receipt.JobID && left.Receipt.Key == right.Receipt.Key &&
			left.Receipt.WriterAttempt == right.Receipt.WriterAttempt && left.Receipt.ProofDigest == right.Receipt.ProofDigest &&
			left.Receipt.Outcome == right.Receipt.Outcome
	}
	return left.Pending.JobID() == right.Pending.JobID() && left.Pending.Attempt() == right.Pending.Attempt() &&
		left.Pending.IntentDigest() == right.Pending.IntentDigest()
}

// SameRuntimePendingAuthority compares only canonical authority identity. It
// intentionally ignores timestamps while rejecting operation, attempt,
// intent, revision or pins drift.
func SameRuntimePendingAuthority(left, right RuntimePendingAuthority) bool {
	return left.Operation.JobID == right.Operation.JobID &&
		left.Operation.ApplicationID == right.Operation.ApplicationID &&
		left.Operation.DesiredRevision == right.Operation.DesiredRevision &&
		left.Operation.ExpectedRevision == right.Operation.ExpectedRevision &&
		left.Operation.PinsDigest == right.Operation.PinsDigest &&
		left.Operation.Action == right.Operation.Action &&
		left.Operation.ActorID == right.Operation.ActorID &&
		left.Operation.IdempotencyKey == right.Operation.IdempotencyKey &&
		left.Intent.JobID == right.Intent.JobID && left.Intent.Key == right.Intent.Key &&
		left.Intent.WriterAttempt == right.Intent.WriterAttempt && left.Intent.RequestDigest == right.Intent.RequestDigest &&
		left.Pending.JobID() == right.Pending.JobID() &&
		left.Pending.Attempt() == right.Pending.Attempt() &&
		left.Pending.IntentDigest() == right.Pending.IntentDigest()
}
