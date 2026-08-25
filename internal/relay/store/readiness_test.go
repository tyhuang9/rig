package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestReadyExecutesOnlySelectOne(t *testing.T) {
	s, mock := mockStore(t)
	mock.ExpectQuery(`SELECT 1`).WillReturnRows(pgxmock.NewRows([]string{"one"}).AddRow(1))
	if err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadyCollapsesErrorsAndHonorsCallerDeadline(t *testing.T) {
	s, mock := mockStore(t)
	databaseFailure := errors.New("postgres://user:secret@database/relay")
	mock.ExpectQuery(`SELECT 1`).WillDelayFor(50 * time.Millisecond).WillReturnError(databaseFailure)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := s.Ready(ctx)
	if err != ErrUnavailable || errors.Is(err, databaseFailure) {
		t.Fatalf("readiness error=%v", err)
	}
}

func TestNilStoreIsNotReady(t *testing.T) {
	var s *Store
	if err := s.Ready(context.Background()); err != ErrUnavailable {
		t.Fatalf("nil readiness error=%v", err)
	}
}

type readinessPlan struct {
	entered chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}
type readinessDB struct {
	mu    sync.Mutex
	plans []*readinessPlan
	calls atomic.Int64
}
type readinessRow struct {
	ctx  context.Context
	plan *readinessPlan
}

type readinessObserverStub struct {
	mu     sync.Mutex
	states []string
}

func (o *readinessObserverStub) ObserveReadiness(_, state string) {
	o.mu.Lock()
	o.states = append(o.states, state)
	o.mu.Unlock()
}

func TestReadyObserverUsesClosedProbeAndCacheStates(t *testing.T) {
	base := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	observer := new(readinessObserverStub)
	db := &readinessDB{plans: []*readinessPlan{{}, {err: errors.New("postgres://user:ghp_secret@db/relay")}}}
	s, err := newWithDatabase(db, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }, ReadinessObserver: observer})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed.Store(int64(2 * time.Second))
	if err = s.Ready(context.Background()); err != ErrUnavailable {
		t.Fatalf("failure=%v", err)
	}
	if err = s.Ready(context.Background()); err != ErrUnavailable {
		t.Fatalf("cached failure=%v", err)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	want := []string{"probe_success", "cached_success", "probe_failure", "cached_failure"}
	if len(observer.states) != len(want) {
		t.Fatalf("states=%v", observer.states)
	}
	for i := range want {
		if observer.states[i] != want[i] || strings.Contains(observer.states[i], "secret") || strings.Contains(observer.states[i], "postgres") {
			t.Fatalf("states=%v", observer.states)
		}
	}
}

func (r readinessRow) Scan(dest ...any) error {
	r.plan.once.Do(func() {
		if r.plan.entered != nil {
			close(r.plan.entered)
		}
	})
	if r.plan.release != nil {
		select {
		case <-r.plan.release:
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
	if r.plan.err != nil {
		return r.plan.err
	}
	*(dest[0].(*int)) = 1
	return nil
}
func (d *readinessDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	index := int(d.calls.Add(1)) - 1
	d.mu.Lock()
	plan := d.plans[index]
	d.mu.Unlock()
	return readinessRow{ctx: ctx, plan: plan}
}
func (*readinessDB) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("unused") }
func (*readinessDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unused")
}
func (*readinessDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}

func TestReadySingleflightTTLsAndFailedRefreshInvalidation(t *testing.T) {
	base := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	plan1 := &readinessPlan{entered: make(chan struct{}), release: make(chan struct{})}
	plan2 := &readinessPlan{err: errors.New("down")}
	plan3 := &readinessPlan{}
	plan4 := &readinessPlan{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("refresh failed")}
	db := &readinessDB{plans: []*readinessPlan{plan1, plan2, plan3, plan4}}
	s, err := newWithDatabase(db, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { results <- s.Ready(context.Background()) }()
	}
	<-plan1.entered
	if db.calls.Load() != 1 {
		t.Fatalf("coalesced probes=%d", db.calls.Load())
	}
	close(plan1.release)
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	elapsed.Store(int64(time.Second - time.Nanosecond))
	if err = s.Ready(context.Background()); err != nil || db.calls.Load() != 1 {
		t.Fatalf("success cache err=%v calls=%d", err, db.calls.Load())
	}
	elapsed.Store(int64(time.Second))
	if err = s.Ready(context.Background()); err != ErrUnavailable || db.calls.Load() != 2 {
		t.Fatalf("success TTL err=%v calls=%d", err, db.calls.Load())
	}
	elapsed.Store(int64(time.Second + 250*time.Millisecond - time.Nanosecond))
	if err = s.Ready(context.Background()); err != ErrUnavailable || db.calls.Load() != 2 {
		t.Fatalf("failure cache err=%v calls=%d", err, db.calls.Load())
	}
	elapsed.Store(int64(time.Second + 250*time.Millisecond))
	if err = s.Ready(context.Background()); err != nil || db.calls.Load() != 3 {
		t.Fatalf("failure TTL err=%v calls=%d", err, db.calls.Load())
	}
	elapsed.Store(int64(2*time.Second + 250*time.Millisecond))
	leader := make(chan error, 1)
	go func() { leader <- s.Ready(context.Background()) }()
	<-plan4.entered
	if err = s.Ready(context.Background()); err != nil {
		t.Fatalf("in-flight stale success err=%v", err)
	}
	close(plan4.release)
	if err = <-leader; err != ErrUnavailable {
		t.Fatalf("refresh result=%v", err)
	}
	if err = s.Ready(context.Background()); err != ErrUnavailable {
		t.Fatalf("known failure served stale success: %v", err)
	}
}

func TestReadyStaleBoundCallerCancellationAndCloseCancellation(t *testing.T) {
	base := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	initial := &readinessPlan{}
	oldRefresh := &readinessPlan{entered: make(chan struct{}), release: make(chan struct{})}
	db := &readinessDB{plans: []*readinessPlan{initial, oldRefresh}}
	s, _ := newWithDatabase(db, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
	if err := s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed.Store(int64(5*time.Second + time.Nanosecond))
	leader := make(chan error, 1)
	go func() { leader <- s.Ready(context.Background()) }()
	<-oldRefresh.entered
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Ready(canceled); err != ErrUnavailable {
		t.Fatalf("stale beyond five seconds=%v", err)
	}
	s.Close()
	if err := <-leader; err != ErrUnavailable {
		t.Fatalf("close cancellation=%v", err)
	}
}

func TestReadyCanceledCallerDoesNotCancelSharedProbe(t *testing.T) {
	plan := &readinessPlan{entered: make(chan struct{}), release: make(chan struct{})}
	db := &readinessDB{plans: []*readinessPlan{plan}}
	s, err := newWithDatabase(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	shortContext, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- s.Ready(shortContext) }()
	<-plan.entered
	second := make(chan error, 1)
	go func() { second <- s.Ready(context.Background()) }()
	cancel()
	if err = <-first; err != ErrUnavailable {
		t.Fatalf("canceled caller result=%v", err)
	}
	if db.calls.Load() != 1 {
		t.Fatalf("shared probe count=%d", db.calls.Load())
	}
	close(plan.release)
	if err = <-second; err != nil {
		t.Fatalf("remaining waiter result=%v", err)
	}
}

func TestReadyClockRollbackInvalidatesSuccessFailureAndStaleInFlight(t *testing.T) {
	base := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	inFlight := &readinessPlan{entered: make(chan struct{}), release: make(chan struct{})}
	db := &readinessDB{plans: []*readinessPlan{
		{},
		{err: errors.New("down")},
		{},
		inFlight,
	}}
	s, err := newWithDatabase(db, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed.Store(int64(-time.Second))
	if err = s.Ready(context.Background()); err != ErrUnavailable || db.calls.Load() != 2 {
		t.Fatalf("success rollback result=%v calls=%d", err, db.calls.Load())
	}
	elapsed.Store(int64(-2 * time.Second))
	if err = s.Ready(context.Background()); err != nil || db.calls.Load() != 3 {
		t.Fatalf("failure rollback result=%v calls=%d", err, db.calls.Load())
	}
	elapsed.Store(int64(-3 * time.Second))
	leader := make(chan error, 1)
	go func() { leader <- s.Ready(context.Background()) }()
	<-inFlight.entered
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Ready(canceled); err != ErrUnavailable {
		t.Fatalf("clock rollback served stale success during refresh: %v", err)
	}
	close(inFlight.release)
	if err = <-leader; err != nil {
		t.Fatalf("rollback refresh result=%v", err)
	}
}
