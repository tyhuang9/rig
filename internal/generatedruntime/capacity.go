package generatedruntime

import (
	"context"
	"errors"

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
