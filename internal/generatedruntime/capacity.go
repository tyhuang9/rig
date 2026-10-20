package generatedruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/hostd/hostd/internal/generatedcapacity"
)

type capacityRequest struct {
	memory uint64
	disk   uint64
}

// capacityGate retains the generatedruntime diagnostic boundary while
// delegating raw host-budget accounting to generatedcapacity.
type capacityGate struct {
	budget *generatedcapacity.Budget
}

func newCapacityGate(source CapacitySource) (*capacityGate, error) {
	budget, err := generatedcapacity.New(source)
	if errors.Is(err, generatedcapacity.ErrSourceRequired) {
		return nil, errors.New("generated runtime capacity source is required")
	}
	if err != nil {
		return nil, err
	}
	return &capacityGate{budget: budget}, nil
}

func (g *capacityGate) acquire(ctx context.Context, request capacityRequest) (*capacityLease, error) {
	return g.acquireCount(ctx, request, 1)
}

func (g *capacityGate) acquireCount(ctx context.Context, request capacityRequest, count int) (*capacityLease, error) {
	if g == nil || g.budget == nil {
		return nil, &Error{Code: DiagnosticInternalError}
	}
	leases, err := g.budget.Acquire(ctx, generatedcapacity.Limits{
		MemoryBytes: request.memory,
		DiskBytes:   request.disk,
	}, count)
	if err != nil {
		return nil, capacityDiagnostic(ctx, err)
	}
	return &capacityLease{release: func() {
		for _, lease := range leases {
			lease.Release()
		}
	}}, nil
}

func capacityDiagnostic(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, generatedcapacity.ErrInvalidLimits):
		return &Error{Code: DiagnosticInternalError}
	case errors.Is(err, generatedcapacity.ErrInvalidCount):
		return &Error{Code: DiagnosticValidationFailed}
	case errors.Is(err, generatedcapacity.ErrInsufficientCapacity):
		return &Error{Code: DiagnosticInsufficientReplacementSpace}
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return &Error{Code: DiagnosticCancelled}
	default:
		return &Error{Code: DiagnosticRuntimeUnavailable}
	}
}

type replacementReservation struct {
	owner      *capacityGate
	lease      *capacityLease
	limit      int
	mu         sync.Mutex
	components map[string]struct{}
	released   bool
	retained   bool
}

func (r *replacementReservation) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.released || r.retained {
		r.mu.Unlock()
		return
	}
	r.released = true
	lease := r.lease
	r.mu.Unlock()
	lease.Release()
}

func (r *replacementReservation) consume(owner *capacityGate, component string) (accepted, newlyConsumed bool) {
	if r == nil || owner == nil || r.owner != owner || component == "" {
		return false, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released || r.retained {
		return false, false
	}
	if _, exists := r.components[component]; exists {
		return true, false
	}
	if len(r.components) >= r.limit {
		return false, false
	}
	r.components[component] = struct{}{}
	return true, true
}

func (r *replacementReservation) unconsume(component string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.released && !r.retained {
		delete(r.components, component)
	}
}

func (r *replacementReservation) retain(owner *capacityGate) bool {
	if r == nil || owner == nil || r.owner != owner {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return false
	}
	r.retained = true
	return true
}
