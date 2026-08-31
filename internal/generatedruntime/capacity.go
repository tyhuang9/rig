package generatedruntime

import (
	"context"
	"errors"
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
	if g == nil || request.memory == 0 || request.disk == 0 {
		return nil, &Error{Code: DiagnosticInternalError}
	}
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
