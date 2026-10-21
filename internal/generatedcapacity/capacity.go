// Package generatedcapacity provides the process-local raw host-capacity
// budget shared by generated runtime families. It deliberately has no
// dependency on runtime command or durable-state packages.
package generatedcapacity

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
)

var (
	ErrSourceRequired       = errors.New("generated capacity source is required")
	ErrInvalidLimits        = errors.New("generated capacity limits are invalid")
	ErrInvalidCount         = errors.New("generated capacity share count is invalid")
	ErrInsufficientCapacity = errors.New("generated capacity is insufficient")
)

// Snapshot reports the resources currently available to generated containers.
// The source must measure the same host budget used by every Budget consumer.
type Snapshot struct {
	MemoryAvailableBytes uint64
	DiskAvailableBytes   uint64
}

// Source observes current host capacity. Errors are returned unchanged so the
// caller that owns the diagnostic boundary can classify them.
type Source interface {
	Snapshot(context.Context) (Snapshot, error)
}

// Limits is the capacity charged by one share.
type Limits struct {
	MemoryBytes uint64
	DiskBytes   uint64
}

// Budget serializes snapshot, availability checks and reservation for one host
// budget in the current controller process.
type Budget struct {
	state *budgetState
}

type budgetState struct {
	source Source
	mu     sync.Mutex
	memory uint64
	disk   uint64
}

// New creates a raw budget around source. Consumers that contend for the same
// host capacity must use the same Budget instance.
func New(source Source) (*Budget, error) {
	if !configuredSource(source) {
		return nil, ErrSourceRequired
	}
	return &Budget{state: &budgetState{source: source}}, nil
}

// Configured reports whether the budget has an initialized capacity source.
// The source is immutable after New, so this check performs no snapshot or
// other external operation.
func (b *Budget) Configured() bool {
	return b != nil && b.state != nil && configuredSource(b.state.source)
}

func configuredSource(source Source) bool {
	if source == nil {
		return false
	}
	reflected := reflect.ValueOf(source)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !reflected.IsNil()
	default:
		return true
	}
}

// Acquire atomically reserves count equal shares. It returns one opaque lease
// per share only after every share has been reserved. Releasing any lease is
// safe to repeat and releases only that share.
func (b *Budget) Acquire(ctx context.Context, limits Limits, count int) ([]*Lease, error) {
	if b == nil || b.state == nil || b.state.source == nil {
		return nil, ErrSourceRequired
	}
	state := b.state
	if limits.MemoryBytes == 0 || limits.DiskBytes == 0 {
		return nil, ErrInvalidLimits
	}
	if count < 1 {
		return nil, ErrInvalidCount
	}
	shares := uint64(count)
	if shares > math.MaxUint64/limits.MemoryBytes || shares > math.MaxUint64/limits.DiskBytes {
		return nil, ErrInvalidCount
	}
	memory := limits.MemoryBytes * shares
	disk := limits.DiskBytes * shares

	state.mu.Lock()
	defer state.mu.Unlock()

	snapshot, err := state.source.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.MemoryAvailableBytes < state.memory || snapshot.DiskAvailableBytes < state.disk ||
		memory > snapshot.MemoryAvailableBytes-state.memory || disk > snapshot.DiskAvailableBytes-state.disk {
		return nil, ErrInsufficientCapacity
	}
	state.memory += memory
	state.disk += disk

	leases := make([]*Lease, count)
	for index := range leases {
		leases[index] = &Lease{state: &leaseState{budget: state, limits: limits}}
	}
	return leases, nil
}

// Lease is an opaque once-release charge for one capacity share.
type Lease struct {
	state *leaseState
}

type leaseState struct {
	budget *budgetState
	limits Limits
	once   sync.Once
}

// Release returns this one share to its originating budget. It is safe to call
// concurrently and more than once.
func (l *Lease) Release() {
	if l == nil || l.state == nil || l.state.budget == nil {
		return
	}
	state := l.state
	state.once.Do(func() {
		state.budget.mu.Lock()
		defer state.budget.mu.Unlock()
		state.budget.memory -= state.limits.MemoryBytes
		state.budget.disk -= state.limits.DiskBytes
	})
}
