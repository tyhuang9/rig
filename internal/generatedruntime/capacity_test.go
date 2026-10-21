package generatedruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type fixedCapacitySource struct {
	snapshot CapacitySnapshot
	err      error
}

func (s fixedCapacitySource) Snapshot(context.Context) (CapacitySnapshot, error) {
	return s.snapshot, s.err
}

func TestCapacityGateReservesConcurrentReplacementCapacity(t *testing.T) {
	gate, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 1024, DiskAvailableBytes: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := gate.acquire(context.Background(), capacityRequest{memory: 600, disk: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.acquire(context.Background(), capacityRequest{memory: 600, disk: 1}); !IsCode(err, DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("expected capacity rejection, got %v", err)
	}
	first.Release()
	first.Release()
	if _, err := gate.acquire(context.Background(), capacityRequest{memory: 600, disk: 600}); err != nil {
		t.Fatalf("released capacity was not reusable: %v", err)
	}
}

func TestCapacityGateSerializesReservations(t *testing.T) {
	gate, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var successes, rejected int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := gate.acquire(context.Background(), capacityRequest{memory: 60, disk: 60})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if IsCode(err, DiagnosticInsufficientReplacementSpace) {
				rejected++
			}
		}()
	}
	close(start)
	wait.Wait()
	if successes != 1 || rejected != 1 {
		t.Fatalf("successes=%d rejected=%d", successes, rejected)
	}
}

func TestCapacityGateAtomicallyAdmitsTwoComponentReplacements(t *testing.T) {
	gate, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	leases := make(chan *capacityLease, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lease, acquireErr := gate.acquireCount(context.Background(), capacityRequest{memory: 30, disk: 30}, 2)
			if acquireErr != nil {
				errorsFound <- acquireErr
				return
			}
			leases <- lease
		}()
	}
	close(start)
	wait.Wait()
	close(leases)
	close(errorsFound)
	if len(leases) != 1 || len(errorsFound) != 1 {
		t.Fatalf("successful reservations=%d rejected reservations=%d", len(leases), len(errorsFound))
	}
	for acquireErr := range errorsFound {
		if !IsCode(acquireErr, DiagnosticInsufficientReplacementSpace) {
			t.Fatalf("aggregate admission error = %v", acquireErr)
		}
	}
	for lease := range leases {
		lease.Release()
		lease.Release()
	}
	if _, err := gate.acquireCount(context.Background(), capacityRequest{memory: 30, disk: 30}, 2); err != nil {
		t.Fatalf("released aggregate capacity was not reusable: %v", err)
	}
}

func TestCapacityGatePreservesSourceAndContextDiagnosticMapping(t *testing.T) {
	request := capacityRequest{memory: 1, disk: 1}
	for _, test := range []struct {
		name   string
		source fixedCapacitySource
		want   DiagnosticCode
	}{
		{name: "source failure", source: fixedCapacitySource{err: errors.New("source failed")}, want: DiagnosticRuntimeUnavailable},
		{name: "cancelled source", source: fixedCapacitySource{err: context.Canceled}, want: DiagnosticCancelled},
		{name: "deadline source", source: fixedCapacitySource{err: context.DeadlineExceeded}, want: DiagnosticRuntimeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate, err := newCapacityGate(test.source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.acquire(context.Background(), request); !IsCode(err, test.want) {
				t.Fatalf("diagnostic = %v, want %s", err, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gate, err := newCapacityGate(fixedCapacitySource{err: ctx.Err()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.acquire(ctx, request); !IsCode(err, DiagnosticCancelled) {
		t.Fatalf("cancelled context diagnostic = %v", err)
	}
}

func TestReplacementReservationConsumesDistinctComponentSharesConcurrently(t *testing.T) {
	limits := ContainerLimits{MemoryBytes: 64 << 20, MilliCPUs: 100, PIDs: 32, TmpfsBytes: 1 << 20, LogSize: "1m", LogFiles: 2}
	session := newRuntimeCandidateSession(t, candidateSpec(), limits,
		fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 128 << 20, DiskAvailableBytes: 512 << 20}},
		func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} })
	session.reserveAndPin(t)
	first := session.beginCreate(t)
	alias, err := session.engine.ReserveReplacement(session.authority.context, deployments.GeneratedReplacementRequest(session.request))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(alias.Release)
	second, err := session.engine.PinCandidates(session.authority.context, session.request, alias)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Leave)
	if _, err := second.BeginCreate(session.authority.spec.ComponentName); err == nil {
		t.Fatal("aliased execution minted a second receipt for the same component")
	}
	if first.Identity().ReceiptID == "" {
		t.Fatal("concrete candidate invocation did not carry its durable receipt")
	}
}

func TestReplacementReservationRetentionIsIrreversibleAcrossAliasesAndReleases(t *testing.T) {
	limits := ContainerLimits{MemoryBytes: 64 << 20, MilliCPUs: 100, PIDs: 32, TmpfsBytes: 1 << 20, LogSize: "1m", LogFiles: 2}
	session := newRuntimeCandidateSession(t, candidateSpec(), limits,
		fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 64 << 20, DiskAvailableBytes: 256 << 20}},
		func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} })
	session.reserveAndPin(t)
	_ = session.beginCreate(t)
	if err := session.engine.RetainReplacementCapacity(session.reservation); err != nil {
		t.Fatal(err)
	}
	if err := session.engine.RetainReplacementCapacity(session.reservation); err != nil {
		t.Fatalf("same owner retention was not idempotent: %v", err)
	}
	session.reservation.Release()
	leases, err := session.engine.capacity.budget.Acquire(context.Background(), generatedcapacity.Limits{MemoryBytes: 1, DiskBytes: 1}, 1)
	for _, lease := range leases {
		lease.Release()
	}
	if !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("retained receipt released shared budget: %v", err)
	}
}

func TestReplacementReservationRejectsForeignRetentionAndReleasesUnattemptedAggregate(t *testing.T) {
	owner := newDefaultRuntimeCandidateSession(t, candidateSpec(), func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} })
	owner.reserveAndPin(t)
	foreign := newDefaultRuntimeCandidateSession(t, candidateSpec(), func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} })
	if err := foreign.engine.RetainReplacementCapacity(owner.reservation); err == nil {
		t.Fatal("foreign candidate owner retained a sealed reservation")
	}
	owner.reservation.Release()
	if _, err := owner.engine.ReserveReplacement(owner.authority.context, deployments.GeneratedReplacementRequest(owner.request)); err != nil {
		t.Fatalf("unattempted concrete reservation did not release: %v", err)
	}
}
