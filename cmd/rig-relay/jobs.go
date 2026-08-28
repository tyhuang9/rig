package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/relay/store"
)

const (
	recoverySuccessInterval = 6 * time.Hour
	expiryInterval          = time.Minute
	maintenanceInterval     = 15 * time.Minute
)

var recoveryRetryIntervals = [...]time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute}

type recoveryJobs interface {
	RunRecoveryScan(context.Context) error
	RunRedeliveryBatch(context.Context) error
}

type maintenanceJobs interface {
	ExpireEnrollments(context.Context) (int64, error)
	ExpireRotations(context.Context) (int64, error)
	PruneDurableState(context.Context, store.DurableRetentionPolicy) (store.DurablePruneResult, error)
}

type jobTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realJobTimer struct{ timer *time.Timer }

func (t realJobTimer) C() <-chan time.Time { return t.timer.C }
func (t realJobTimer) Stop() bool          { return t.timer.Stop() }

type scheduler struct {
	recovery         recoveryJobs
	maintenance      maintenanceJobs
	metrics          *metrics
	recoveryInterval time.Duration
	newTimer         func(time.Duration) jobTimer
	wg               sync.WaitGroup
}

func newScheduler(recovery recoveryJobs, maintenance maintenanceJobs, recoveryInterval time.Duration, m *metrics) *scheduler {
	return &scheduler{
		recovery: recovery, maintenance: maintenance, recoveryInterval: recoveryInterval, metrics: m,
		newTimer: func(delay time.Duration) jobTimer { return realJobTimer{timer: time.NewTimer(delay)} },
	}
}

func (s *scheduler) Start(ctx context.Context) {
	s.wg.Add(4)
	go func() { defer s.wg.Done(); s.runRecovery(ctx) }()
	go func() {
		defer s.wg.Done()
		s.runPeriodic(ctx, "redelivery", s.recoveryInterval, 5*time.Minute, s.recovery.RunRedeliveryBatch)
	}()
	go func() { defer s.wg.Done(); s.runPeriodic(ctx, "expiry", 0, 30*time.Second, s.expire) }()
	go func() { defer s.wg.Done(); s.runPeriodic(ctx, "maintenance", 0, 2*time.Minute, s.prune) }()
}

func (s *scheduler) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *scheduler) runRecovery(ctx context.Context) {
	delay := time.Duration(0)
	retry := 0
	for {
		if !waitJobTimer(ctx, s.newTimer, delay) {
			return
		}
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		started := time.Now()
		err := s.recovery.RunRecoveryScan(runCtx)
		duration := time.Since(started)
		canceled := ctx.Err() != nil
		cancel()
		if canceled {
			return
		}
		if err == nil {
			s.metrics.observe("recovery_scan", "success", duration)
			delay, retry = recoverySuccessInterval, 0
			continue
		}
		result := "failure"
		if errors.Is(err, store.ErrConflict) {
			result = "contended"
		}
		s.metrics.observe("recovery_scan", result, duration)
		delay = recoveryRetryIntervals[retry]
		if retry < len(recoveryRetryIntervals)-1 {
			retry++
		}
	}
}

func (s *scheduler) runPeriodic(ctx context.Context, job string, firstDelay, timeout time.Duration, run func(context.Context) error) {
	delay := firstDelay
	for {
		if !waitJobTimer(ctx, s.newTimer, delay) {
			return
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		started := time.Now()
		err := run(runCtx)
		duration := time.Since(started)
		canceled := ctx.Err() != nil
		cancel()
		if canceled {
			return
		}
		result := "success"
		if errors.Is(err, store.ErrConflict) {
			result = "contended"
		} else if err != nil {
			result = "failure"
		}
		s.metrics.observe(job, result, duration)
		switch job {
		case "redelivery":
			delay = s.recoveryInterval
		case "expiry":
			delay = expiryInterval
		case "maintenance":
			delay = maintenanceInterval
		}
	}
}

func (s *scheduler) expire(ctx context.Context) error {
	enrollments, enrollmentErr := s.maintenance.ExpireEnrollments(ctx)
	rotations, rotationErr := s.maintenance.ExpireRotations(ctx)
	if enrollments > 0 {
		s.metrics.observeBackgroundItems("expired_enrollments", uint64(enrollments))
	}
	if rotations > 0 {
		s.metrics.observeBackgroundItems("expired_rotations", uint64(rotations))
	}
	return errors.Join(enrollmentErr, rotationErr)
}

func (s *scheduler) prune(ctx context.Context) error {
	result, err := s.maintenance.PruneDurableState(ctx, store.DefaultDurableRetentionPolicy())
	if total := result.Total(); total > 0 {
		s.metrics.observeBackgroundItems("pruned_durable", uint64(total))
	}
	return err
}

func waitJobTimer(ctx context.Context, create func(time.Duration) jobTimer, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := create(delay)
	defer timer.Stop()
	select {
	case <-timer.C():
		return true
	case <-ctx.Done():
		return false
	}
}
