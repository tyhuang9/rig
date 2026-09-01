package generatedruntime

import (
	"context"
	"errors"
	"math"
	"sync"
)

type capacityRequest struct {
	memory uint64
	disk   uint64
}

// capacityGate makes the read/check/reserve sequence atomic inside one
// controller process. After a restart, live containers are already reflected
// in the host's available resource snapshot, so in-memory reservations can be
// reconstructed by new deployment attempts rather than persisted.
type capacityGate struct {
	source CapacitySource
	mu     sync.Mutex
	memory uint64
	disk   uint64
}

func newCapacityGate(source CapacitySource) (*capacityGate, error) {
	if source == nil {
		return nil, errors.New("generated runtime capacity source is required")
	}
	return &capacityGate{source: source}, nil
}

func (g *capacityGate) acquire(ctx context.Context, request capacityRequest) (*capacityLease, error) {
	return g.acquireCount(ctx, request, 1)
}

func (g *capacityGate) acquireCount(ctx context.Context, request capacityRequest, count int) (*capacityLease, error) {
	if g == nil || request.memory == 0 || request.disk == 0 {
		return nil, &Error{Code: DiagnosticInternalError}
	}
	if count < 1 || uint64(count) > math.MaxUint64/request.memory || uint64(count) > math.MaxUint64/request.disk {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	request.memory *= uint64(count)
	request.disk *= uint64(count)
	g.mu.Lock()
	defer g.mu.Unlock()

	snapshot, err := g.source.Snapshot(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return nil, &Error{Code: DiagnosticCancelled}
		}
		return nil, &Error{Code: DiagnosticRuntimeUnavailable}
	}
	if snapshot.MemoryAvailableBytes < g.memory || snapshot.DiskAvailableBytes < g.disk ||
		request.memory > snapshot.MemoryAvailableBytes-g.memory || request.disk > snapshot.DiskAvailableBytes-g.disk {
		return nil, &Error{Code: DiagnosticInsufficientReplacementSpace}
	}
	g.memory += request.memory
	g.disk += request.disk
	return &capacityLease{release: func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.memory -= request.memory
		g.disk -= request.disk
	}}, nil
}

type replacementReservation struct {
	owner      *capacityGate
	lease      *capacityLease
	limit      int
	mu         sync.Mutex
	components map[string]struct{}
	released   bool
}

func (r *replacementReservation) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.released {
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
	if r.released {
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
	if !r.released {
		delete(r.components, component)
	}
}
