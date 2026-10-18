package generatedruntime

import (
	"context"
	"sync"
	"testing"
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

func TestReplacementReservationConsumesDistinctComponentSharesConcurrently(t *testing.T) {
	gate, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := gate.acquireCount(context.Background(), capacityRequest{memory: 25, disk: 25}, 2)
	if err != nil {
		t.Fatal(err)
	}
	reservation := &replacementReservation{owner: gate, lease: lease, limit: 2, components: make(map[string]struct{}, 2)}
	start := make(chan struct{})
	accepted := make(chan bool, 3)
	var wait sync.WaitGroup
	for _, component := range []string{"web", "api", "extra"} {
		component := component
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			ok, _ := reservation.consume(gate, component)
			accepted <- ok
		}()
	}
	close(start)
	wait.Wait()
	close(accepted)
	successes := 0
	for ok := range accepted {
		if ok {
			successes++
		}
	}
	if successes != 2 {
		t.Fatalf("consumed shares = %d", successes)
	}
	reservation.Release()
	reservation.Release()
	if ok, _ := reservation.consume(gate, "late"); ok {
		t.Fatal("released reservation accepted another component")
	}
}

func TestReplacementReservationRetentionIsIrreversibleAcrossAliasesAndReleases(t *testing.T) {
	gate, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := gate.acquireCount(context.Background(), capacityRequest{memory: 30, disk: 30}, 2)
	if err != nil {
		t.Fatal(err)
	}
	reservation := &replacementReservation{owner: gate, lease: lease, limit: 2, components: make(map[string]struct{}, 2)}
	if ok, newlyConsumed := reservation.consume(gate, "api"); !ok || !newlyConsumed {
		t.Fatal("first aggregate component was not consumed")
	}
	alias := reservation
	if !reservation.retain(gate) || !alias.retain(gate) {
		t.Fatal("same-owner retention was not idempotent")
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, action := range []func(){reservation.Release, alias.Release, func() { reservation.unconsume("api") }} {
		action := action
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			action()
		}()
	}
	close(start)
	wait.Wait()
	if ok, _ := reservation.consume(gate, "web"); ok {
		t.Fatal("retained aggregate accepted a later component")
	}
	if _, err := gate.acquire(context.Background(), capacityRequest{memory: 50, disk: 1}); !IsCode(err, DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("retained aggregate did not block competitor: %v", err)
	}
	gate.mu.Lock()
	reservedMemory, reservedDisk := gate.memory, gate.disk
	gate.mu.Unlock()
	if reservedMemory != 60 || reservedDisk != 60 {
		t.Fatalf("retained aggregate accounting changed: memory=%d disk=%d", reservedMemory, reservedDisk)
	}
}

func TestReplacementReservationRejectsForeignRetentionAndReleasesUnattemptedAggregate(t *testing.T) {
	owner, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := newCapacityGate(fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := owner.acquireCount(context.Background(), capacityRequest{memory: 30, disk: 30}, 2)
	if err != nil {
		t.Fatal(err)
	}
	reservation := &replacementReservation{owner: owner, lease: lease, limit: 2, components: make(map[string]struct{}, 2)}
	if (&Engine{capacity: foreign}).RetainReplacementCapacity(reservation) == nil {
		t.Fatal("foreign engine retained another engine's aggregate")
	}
	reservation.Release()
	if _, err := owner.acquireCount(context.Background(), capacityRequest{memory: 30, disk: 30}, 2); err != nil {
		t.Fatalf("all-unattempted aggregate did not release: %v", err)
	}
}
