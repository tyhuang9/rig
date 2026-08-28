package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/store"
)

type jobStub struct {
	mu                sync.Mutex
	recoveryErrors    []error
	recoveryCalls     int
	redeliveryCalls   int
	expireEnrollCalls int
	expireRotateCalls int
	pruneCalls        int
	policy            store.DurableRetentionPolicy
}

func (s *jobStub) RunRecoveryScan(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.recoveryCalls
	s.recoveryCalls++
	if index < len(s.recoveryErrors) {
		return s.recoveryErrors[index]
	}
	return nil
}
func (s *jobStub) RunRedeliveryBatch(context.Context) error {
	s.mu.Lock()
	s.redeliveryCalls++
	s.mu.Unlock()
	return nil
}
func (s *jobStub) ExpireEnrollments(context.Context) (int64, error) {
	s.mu.Lock()
	s.expireEnrollCalls++
	s.mu.Unlock()
	return 2, errors.New("enrollment failed")
}
func (s *jobStub) ExpireRotations(context.Context) (int64, error) {
	s.mu.Lock()
	s.expireRotateCalls++
	s.mu.Unlock()
	return 3, nil
}
func (s *jobStub) PruneDurableState(_ context.Context, policy store.DurableRetentionPolicy) (store.DurablePruneResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneCalls++
	s.policy = policy
	return store.DurablePruneResult{TerminalEnrollments: 4}, nil
}

type controlledTimer struct {
	delay time.Duration
	ch    chan time.Time
}

func (t *controlledTimer) C() <-chan time.Time { return t.ch }
func (*controlledTimer) Stop() bool            { return true }

func TestExpiryAlwaysCallsBothAndMaintenanceUsesDefaultPolicy(t *testing.T) {
	jobs := &jobStub{}
	s := newScheduler(jobs, jobs, 30*time.Second, &metrics{})
	if err := s.expire(context.Background()); err == nil {
		t.Fatal("joined expiry failure missing")
	}
	if jobs.expireEnrollCalls != 1 || jobs.expireRotateCalls != 1 {
		t.Fatalf("expiry calls enroll=%d rotate=%d", jobs.expireEnrollCalls, jobs.expireRotateCalls)
	}
	if err := s.prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs.pruneCalls != 1 || jobs.policy != store.DefaultDurableRetentionPolicy() {
		t.Fatalf("maintenance calls=%d policy=%+v", jobs.pruneCalls, jobs.policy)
	}
	if s.metrics.backgroundItems[0].Load() != 2 || s.metrics.backgroundItems[1].Load() != 3 || s.metrics.backgroundItems[2].Load() != 4 {
		t.Fatalf("background item metrics enrollment=%d rotations=%d pruned=%d", s.metrics.backgroundItems[0].Load(), s.metrics.backgroundItems[1].Load(), s.metrics.backgroundItems[2].Load())
	}
}

func TestRecoveryBackoffClassificationAndCancellation(t *testing.T) {
	jobs := &jobStub{recoveryErrors: []error{store.ErrConflict, errors.New("provider failed"), errors.New("provider failed"), errors.New("provider failed"), errors.New("provider failed"), nil}}
	m := &metrics{}
	s := newScheduler(jobs, jobs, time.Second, m)
	created := make(chan *controlledTimer, 8)
	s.newTimer = func(delay time.Duration) jobTimer {
		timer := &controlledTimer{delay: delay, ch: make(chan time.Time, 1)}
		created <- timer
		return timer
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.runRecovery(ctx); close(done) }()
	wants := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute, 6 * time.Hour}
	for index, want := range wants {
		timer := <-created
		if timer.delay != want {
			t.Fatalf("retry delay=%v want=%v", timer.delay, want)
		}
		if index < len(wants)-1 {
			timer.ch <- time.Now()
		}
	}
	cancel()
	<-done
	if m.jobs[0][2].Load() != 1 || m.jobs[0][1].Load() != 4 || m.jobs[0][0].Load() != 1 {
		t.Fatalf("recovery metrics success=%d failure=%d contended=%d", m.jobs[0][0].Load(), m.jobs[0][1].Load(), m.jobs[0][2].Load())
	}
}

func TestPeriodicCadenceIsSynchronousAndCancellationIsNotFailure(t *testing.T) {
	m := &metrics{}
	s := newScheduler(&jobStub{}, &jobStub{}, 30*time.Second, m)
	created := make(chan *controlledTimer, 2)
	s.newTimer = func(delay time.Duration) jobTimer {
		timer := &controlledTimer{delay: delay, ch: make(chan time.Time, 1)}
		created <- timer
		return timer
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	go func() {
		s.runPeriodic(ctx, "redelivery", 30*time.Second, time.Minute, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
		close(done)
	}()
	first := <-created
	if first.delay != 30*time.Second {
		t.Fatalf("redelivery first delay=%v", first.delay)
	}
	first.ch <- time.Now()
	<-entered
	select {
	case timer := <-created:
		t.Fatalf("overlapping run scheduled timer=%v", timer.delay)
	default:
	}
	close(release)
	second := <-created
	if second.delay != 30*time.Second {
		t.Fatalf("redelivery cadence=%v", second.delay)
	}
	cancel()
	<-done
	if m.jobs[1][0].Load() != 1 || m.jobs[1][1].Load() != 0 || m.jobs[1][2].Load() != 0 {
		t.Fatalf("periodic metrics success=%d failure=%d contended=%d", m.jobs[1][0].Load(), m.jobs[1][1].Load(), m.jobs[1][2].Load())
	}

	canceledMetrics := &metrics{}
	s = newScheduler(&jobStub{}, &jobStub{}, time.Second, canceledMetrics)
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	entered = make(chan struct{})
	go func() {
		s.runPeriodic(ctx, "expiry", 0, time.Minute, func(runCtx context.Context) error {
			close(entered)
			<-runCtx.Done()
			return runCtx.Err()
		})
		close(done)
	}()
	<-entered
	cancel()
	<-done
	if canceledMetrics.jobs[2][0].Load()+canceledMetrics.jobs[2][1].Load()+canceledMetrics.jobs[2][2].Load() != 0 {
		t.Fatal("cancellation was recorded as a job outcome")
	}
}

func TestJobLocalCancellationIsFailureAndReschedules(t *testing.T) {
	m := &metrics{}
	s := newScheduler(&jobStub{}, &jobStub{}, time.Second, m)
	created := make(chan *controlledTimer, 2)
	s.newTimer = func(delay time.Duration) jobTimer {
		timer := &controlledTimer{delay: delay, ch: make(chan time.Time, 1)}
		created <- timer
		return timer
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.runPeriodic(ctx, "redelivery", 0, time.Minute, func(context.Context) error { return context.Canceled })
		close(done)
	}()
	next := <-created
	if next.delay != time.Second {
		t.Fatalf("retry delay=%v", next.delay)
	}
	if m.jobs[1][1].Load() != 1 {
		t.Fatalf("failure count=%d", m.jobs[1][1].Load())
	}
	cancel()
	<-done
}
