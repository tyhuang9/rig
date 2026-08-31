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
