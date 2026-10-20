package generatedcapacity

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
)

type fixedSource struct {
	snapshot Snapshot
	err      error
}

func (s fixedSource) Snapshot(context.Context) (Snapshot, error) {
	return s.snapshot, s.err
}

func TestAcquireAtomicallyReservesEverySibling(t *testing.T) {
	budget, err := New(fixedSource{snapshot: Snapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 50, DiskBytes: 50}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 30, DiskBytes: 30}, 2); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("partial sibling reservation was accepted: %v", err)
	}
	second, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 50, DiskBytes: 50}, 1)
	if err != nil {
		t.Fatalf("rejected sibling reservation consumed capacity: %v", err)
	}
	first[0].Release()
	second[0].Release()
}

func TestAcquireRejectsInvalidAndOverflowRequestsBeforeSnapshot(t *testing.T) {
	called := false
	source := sourceFunc(func(context.Context) (Snapshot, error) {
		called = true
		return Snapshot{MemoryAvailableBytes: math.MaxUint64, DiskAvailableBytes: math.MaxUint64}, nil
	})
	budget, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		limits Limits
		count  int
		want   error
	}{
		{name: "zero memory", limits: Limits{DiskBytes: 1}, count: 1, want: ErrInvalidLimits},
		{name: "zero disk", limits: Limits{MemoryBytes: 1}, count: 1, want: ErrInvalidLimits},
		{name: "zero count", limits: Limits{MemoryBytes: 1, DiskBytes: 1}, want: ErrInvalidCount},
		{name: "memory overflow", limits: Limits{MemoryBytes: math.MaxUint64, DiskBytes: 1}, count: 2, want: ErrInvalidCount},
		{name: "disk overflow", limits: Limits{MemoryBytes: 1, DiskBytes: math.MaxUint64}, count: 2, want: ErrInvalidCount},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := budget.Acquire(context.Background(), test.limits, test.count)
			if !errors.Is(err, test.want) {
				t.Fatalf("Acquire error = %v, want %v", err, test.want)
			}
		})
	}
	if called {
		t.Fatal("invalid request reached capacity source")
	}
}

func TestAcquirePreservesSourceAndContextFailures(t *testing.T) {
	sourceFailure := errors.New("source failed")
	budget, err := New(fixedSource{err: sourceFailure})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 1, DiskBytes: 1}, 1); !errors.Is(err, sourceFailure) {
		t.Fatalf("source failure = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	budget, err = New(sourceFunc(func(received context.Context) (Snapshot, error) {
		if received != ctx {
			t.Fatal("capacity source received a different context")
		}
		return Snapshot{}, received.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Acquire(ctx, Limits{MemoryBytes: 1, DiskBytes: 1}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled source error = %v", err)
	}
}

func TestLeaseReleaseIsIndependentIdempotentAndConcurrent(t *testing.T) {
	budget, err := New(fixedSource{snapshot: Snapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	leases, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 25, DiskBytes: 25}, 2)
	if err != nil {
		t.Fatal(err)
	}
	copiedLease := *leases[0]

	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range 16 {
		release := leases[0].Release
		if index%2 != 0 {
			release = copiedLease.Release
		}
		wait.Add(1)
		go func(release func()) {
			defer wait.Done()
			<-start
			release()
		}(release)
	}
	close(start)
	wait.Wait()

	if _, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 76, DiskBytes: 76}, 1); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("repeated release changed the remaining sibling charge: %v", err)
	}
	competitor, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 75, DiskBytes: 75}, 1)
	if err != nil {
		t.Fatalf("one released share was not reusable: %v", err)
	}
	competitor[0].Release()
	leases[1].Release()
	if _, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 100, DiskBytes: 100}, 1); err != nil {
		t.Fatalf("all shares were not independently released: %v", err)
	}
}

func TestCopiedBudgetContendsAcrossConsumers(t *testing.T) {
	budget, err := New(fixedSource{snapshot: Snapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	copiedBudget := *budget

	type result struct {
		leases []*Lease
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, consumer := range []*Budget{budget, &copiedBudget} {
		consumer := consumer
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			leases, acquireErr := consumer.Acquire(context.Background(), Limits{MemoryBytes: 60, DiskBytes: 60}, 1)
			results <- result{leases: leases, err: acquireErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var accepted []*Lease
	rejected := 0
	for result := range results {
		if result.err == nil {
			accepted = result.leases
			continue
		}
		if !errors.Is(result.err, ErrInsufficientCapacity) {
			t.Fatalf("consumer admission error = %v", result.err)
		}
		rejected++
	}
	if len(accepted) != 1 || rejected != 1 {
		t.Fatalf("accepted shares=%d rejected consumers=%d", len(accepted), rejected)
	}
	accepted[0].Release()
	if _, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 100, DiskBytes: 100}, 1); err != nil {
		t.Fatalf("released consumer capacity was not reusable: %v", err)
	}
}

func TestNewRequiresSource(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrSourceRequired) {
		t.Fatalf("New(nil) error = %v", err)
	}
}

func TestZeroValuesFailOrReleaseSafely(t *testing.T) {
	var budget Budget
	if _, err := budget.Acquire(context.Background(), Limits{MemoryBytes: 1, DiskBytes: 1}, 1); !errors.Is(err, ErrSourceRequired) {
		t.Fatalf("zero Budget Acquire error = %v", err)
	}
	var lease Lease
	lease.Release()
	var nilLease *Lease
	nilLease.Release()
}

type sourceFunc func(context.Context) (Snapshot, error)

func (f sourceFunc) Snapshot(ctx context.Context) (Snapshot, error) {
	return f(ctx)
}
