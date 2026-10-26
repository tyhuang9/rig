package generatedingress

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedcapacity"
)

func TestGeneratedDomainCapacityDebtBootstrapIsAtomicAndSaturating(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.capacityOutput = []byte("100 90\n")
	coordinator, err := newGeneratedDomainCapacityCoordinator(manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Snapshot(context.Background()); !errors.Is(err, errGeneratedDomainCapacityDebtState) {
		t.Fatalf("bootstrap-open snapshot error=%v", err)
	}
	if tokens, err := coordinator.installBootstrapBatch([]generatedcapacity.Limits{
		{MemoryBytes: math.MaxUint64, DiskBytes: 1}, {MemoryBytes: 1, DiskBytes: 1},
	}); tokens != nil || !errors.Is(err, errGeneratedDomainCapacityDebtState) {
		t.Fatalf("overflow batch tokens=%d error=%v", len(tokens), err)
	}
	if coordinator.memory != 0 || coordinator.disk != 0 || coordinator.epoch != 0 || len(coordinator.tokens) != 0 {
		t.Fatalf("overflow batch mutated coordinator: memory=%d disk=%d epoch=%d tokens=%d",
			coordinator.memory, coordinator.disk, coordinator.epoch, len(coordinator.tokens))
	}

	tokens, err := coordinator.installBootstrapBatch([]generatedcapacity.Limits{
		{MemoryBytes: 40, DiskBytes: 30}, {MemoryBytes: 70, DiskBytes: 70},
	})
	if err != nil || len(tokens) != 2 || tokens[0] == tokens[1] || tokens[0].epoch == 0 ||
		tokens[1].epoch <= tokens[0].epoch {
		t.Fatalf("installed batch tokens=%#v error=%v", tokens, err)
	}
	if err := coordinator.sealBootstrap(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := coordinator.Snapshot(context.Background())
	if err != nil || snapshot.MemoryAvailableBytes != 0 || snapshot.DiskAvailableBytes != 0 {
		t.Fatalf("saturating F-D snapshot=%#v error=%v", snapshot, err)
	}
	if _, err := coordinator.installBootstrapBatch([]generatedcapacity.Limits{{MemoryBytes: 1, DiskBytes: 1}}); !errors.Is(err, errGeneratedDomainCapacityDebtState) {
		t.Fatalf("second bootstrap batch error=%v", err)
	}
}

func TestGeneratedDomainCapacityDebtAbortFencesBorrowsAndClosesOnce(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	coordinator, err := newGeneratedDomainCapacityCoordinator(manager)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := coordinator.installBootstrapBatch([]generatedcapacity.Limits{{MemoryBytes: 10, DiskBytes: 20}})
	if err != nil || len(tokens) != 1 {
		t.Fatalf("bootstrap tokens=%d error=%v", len(tokens), err)
	}
	if err := coordinator.sealBootstrap(); err != nil {
		t.Fatal(err)
	}
	borrow, err := coordinator.borrowAcquire()
	if err != nil {
		t.Fatal(err)
	}
	var released atomic.Int32
	aborted := make(chan error, 1)
	go func() { aborted <- coordinator.abortUnpublished(func() { released.Add(1) }) }()
	deadline := time.After(5 * time.Second)
	for {
		coordinator.mu.Lock()
		phase := coordinator.phase
		coordinator.mu.Unlock()
		if phase == generatedDomainCapacityAborting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("abort did not enter its fenced phase")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if next, err := coordinator.borrowAcquire(); next != nil || !errors.Is(err, errGeneratedDomainCapacityDebtState) {
		t.Fatalf("aborting coordinator borrow=%#v error=%v", next, err)
	}
	select {
	case err := <-aborted:
		t.Fatalf("abort returned before the admitted borrow drained: %v", err)
	default:
	}
	borrow.release()
	borrow.release()
	if err := <-aborted; err != nil {
		t.Fatal(err)
	}
	if released.Load() != 1 || tokens[0].configured() || tokens[0].published() {
		t.Fatalf("closed abort release=%d configured=%t published=%t",
			released.Load(), tokens[0].configured(), tokens[0].published())
	}
	if err := coordinator.abortUnpublished(func() { released.Add(1) }); err != nil || released.Load() != 1 {
		t.Fatalf("idempotent abort error=%v release=%d", err, released.Load())
	}
}

func TestGeneratedDomainCapacityDebtPublicationAndAliasesReleaseExactlyOnce(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.capacityOutput = []byte("50 60\n")
	coordinator, err := newGeneratedDomainCapacityCoordinator(manager)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := coordinator.installBootstrapBatch([]generatedcapacity.Limits{{MemoryBytes: 20, DiskBytes: 30}})
	if err != nil || len(tokens) != 1 {
		t.Fatalf("bootstrap tokens=%d error=%v", len(tokens), err)
	}
	alias := tokens[0]
	alias.release()
	if !tokens[0].configured() {
		t.Fatal("pending-publication alias released configured debt")
	}
	if err := coordinator.sealBootstrap(); err != nil {
		t.Fatal(err)
	}
	borrow, err := coordinator.borrowAcquire()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.publish(); !errors.Is(err, errGeneratedDomainCapacityDebtState) {
		t.Fatalf("published with an admitted borrow: %v", err)
	}
	borrow.release()
	if err := coordinator.publish(); err != nil || !tokens[0].published() {
		t.Fatalf("publish error=%v tokenPublished=%t", err, tokens[0].published())
	}
	snapshot, err := coordinator.Snapshot(context.Background())
	if err != nil || snapshot.MemoryAvailableBytes != 30 || snapshot.DiskAvailableBytes != 30 {
		t.Fatalf("published F-D snapshot=%#v error=%v", snapshot, err)
	}
	alias.release()
	tokens[0].release()
	if tokens[0].configured() || coordinator.memory != 0 || coordinator.disk != 0 || len(coordinator.tokens) != 0 {
		t.Fatalf("alias release left debt: memory=%d disk=%d tokens=%d configured=%t",
			coordinator.memory, coordinator.disk, len(coordinator.tokens), tokens[0].configured())
	}
	if err := coordinator.abortUnpublished(nil); !errors.Is(err, errGeneratedDomainCapacityDebtState) {
		t.Fatalf("published coordinator aborted: %v", err)
	}
}
