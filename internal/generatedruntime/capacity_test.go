package generatedruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedingress"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
	"path/filepath"
)

type fixedDomainCapacity struct{ memory, disk uint64 }

func newCapacityDomainFixture(t *testing.T, capacity fixedDomainCapacity) runtimeDomainFixture {
	t.Helper()
	root := generateddomain.NewRetainedRoot(t)
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtimeFakeRunner{run: func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} }}
	environment := &runtimeFakeEnvironment{path: filepath.Join(root, "capacity.env")}
	return newRuntimeDomainFixture(t, root, runner, environment, EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second,
		Limits: defaultLimits(), ReplacementDiskBytes: 256 << 20,
	}, runtimeDomainOptionsForFixedCapacity(capacity))
}

func runtimeDomainOptionsForFixedCapacity(capacity fixedDomainCapacity) generateddomain.Options {
	if capacity.memory == 0 {
		capacity.memory = 4 << 30
	}
	if capacity.disk == 0 {
		capacity.disk = 4 << 30
	}
	return generateddomain.Options{MemoryAvailableBytes: capacity.memory, DiskAvailableBytes: capacity.disk}
}

func TestGeneratedDomainRetainedCapacitySerializesReservations(t *testing.T) {
	share := defaultLimits()
	fixture := newCapacityDomainFixture(t, fixedDomainCapacity{memory: uint64(share.MemoryBytes), disk: 256 << 20})
	candidates := fixture.engine.candidates
	start := make(chan struct{})
	var successes, rejected int
	type retainedReservation struct {
		operation   string
		reservation generatedingress.GeneratedRetainedCapacityReservation
	}
	reservations := make(chan retainedReservation, 2)
	var mu sync.Mutex
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			operation := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}[index]
			reservation, err := candidates.ReserveRetainedCapacity(context.Background(), operation, 1)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				reservations <- retainedReservation{operation: operation, reservation: reservation}
			} else if errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
				rejected++
			}
		}(index)
	}
	close(start)
	wait.Wait()
	if successes != 1 || rejected != 1 {
		t.Fatalf("successes=%d rejected=%d", successes, rejected)
	}
	close(reservations)
	for reservation := range reservations {
		if err := candidates.ReleaseRetainedCapacity(reservation.operation, reservation.reservation); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGeneratedDomainRetainedCapacityAtomicallyAdmitsTwoShares(t *testing.T) {
	share := defaultLimits()
	fixture := newCapacityDomainFixture(t, fixedDomainCapacity{memory: uint64(share.MemoryBytes) * 2, disk: 512 << 20})
	candidates := fixture.engine.candidates
	start := make(chan struct{})
	type aggregateReservation struct {
		operation   string
		reservation generatedingress.GeneratedRetainedCapacityReservation
	}
	leases := make(chan aggregateReservation, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	operations := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			lease, acquireErr := candidates.ReserveRetainedCapacity(context.Background(), operations[index], 2)
			if acquireErr != nil {
				errorsFound <- acquireErr
				return
			}
			leases <- aggregateReservation{operation: operations[index], reservation: lease}
		}(index)
	}
	close(start)
	wait.Wait()
	close(leases)
	close(errorsFound)
	if len(leases) != 1 || len(errorsFound) != 1 {
		t.Fatalf("successful reservations=%d rejected reservations=%d", len(leases), len(errorsFound))
	}
	for acquireErr := range errorsFound {
		if !errors.Is(acquireErr, generatedcapacity.ErrInsufficientCapacity) {
			t.Fatalf("aggregate admission error = %v", acquireErr)
		}
	}
	for lease := range leases {
		if err := candidates.ReleaseRetainedCapacity(lease.operation, lease.reservation); err != nil {
			t.Fatal(err)
		}
	}
	if later, err := candidates.ReserveRetainedCapacity(context.Background(), operations[0], 2); err != nil {
		t.Fatalf("released aggregate capacity was not reusable: %v", err)
	} else if err := candidates.ReleaseRetainedCapacity(operations[0], later); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedDomainRetainedCapacityRejectsCancelledCaller(t *testing.T) {
	share := defaultLimits()
	fixture := newCapacityDomainFixture(t, fixedDomainCapacity{memory: uint64(share.MemoryBytes), disk: 256 << 20})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reservation, err := fixture.engine.candidates.ReserveRetainedCapacity(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1)
	if reservation != nil || !generatedingress.IsCode(err, generatedingress.DiagnosticCancelled) {
		t.Fatalf("cancelled context result = %T, %v", reservation, err)
	}
	fresh, err := fixture.engine.candidates.ReserveRetainedCapacity(context.Background(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", 1)
	if err != nil {
		t.Fatalf("cancelled caller retained capacity: %v", err)
	}
	if err := fixture.engine.candidates.ReleaseRetainedCapacity("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", fresh); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedRuntimeCapacitySourceFailuresPreserveClosedDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want DiagnosticCode
	}{
		{name: "source failure", err: errors.New("source failed"), want: DiagnosticRuntimeUnavailable},
		{name: "cancelled source", err: context.Canceled, want: DiagnosticCancelled},
		{name: "deadline source", err: context.DeadlineExceeded, want: DiagnosticRuntimeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests [][]string
			spec := candidateSpec()
			spec.ActiveSlot = ""
			session := newRuntimeCandidateSessionWithConfigurationAndDomainOptions(t, spec, defaultLimits(), nil,
				generateddomain.Options{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30, DelegateCapacity: true},
				func(request runtimeprocess.CommandRequest) runtimeRequestResult {
					requests = append(requests, append([]string(nil), request.Args...))
					return runtimeRequestResult{err: test.err}
				})
			reservation, err := session.engine.ReserveReplacement(session.authority.context,
				generatedingress.GeneratedReplacementRequest(session.request))
			if reservation != nil || !IsCode(err, test.want) {
				t.Fatalf("reservation=%T diagnostic=%v want=%s", reservation, err, test.want)
			}
			if len(requests) != 1 || len(requests[0]) != 6 || requests[0][0] != "container" ||
				requests[0][1] != "exec" || requests[0][3] != "sh" || requests[0][4] != "-c" {
				t.Fatalf("capacity source requests=%#v", requests)
			}
		})
	}
}

func TestReplacementReservationConsumesDistinctComponentSharesConcurrently(t *testing.T) {
	limits := ContainerLimits{MemoryBytes: 64 << 20, MilliCPUs: 100, PIDs: 32, TmpfsBytes: 1 << 20, LogSize: "1m", LogFiles: 2}
	session := newRuntimeCandidateSession(t, candidateSpec(), limits,
		fixedDomainCapacity{memory: 128 << 20, disk: 512 << 20},
		func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} })
	session.reserveAndPin(t)
	first := session.beginCreate(t)
	alias, err := session.engine.ReserveReplacement(session.authority.context, generatedingress.GeneratedReplacementRequest(session.request))
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
	spec := candidateSpec()
	spec.ActiveSlot = ""
	session := newRuntimeCandidateSession(t, spec, limits,
		fixedDomainCapacity{memory: 64 << 20, disk: 256 << 20},
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
	reservation, err := session.engine.candidates.ReserveRetainedCapacity(context.Background(),
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1)
	if reservation != nil {
		_ = session.engine.candidates.ReleaseRetainedCapacity(
			"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", reservation)
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
	if _, err := owner.engine.ReserveReplacement(owner.authority.context, generatedingress.GeneratedReplacementRequest(owner.request)); err != nil {
		t.Fatalf("unattempted concrete reservation did not release: %v", err)
	}
}
