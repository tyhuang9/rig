package autodeploy

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/controllerrelay"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/relay/protocol"
)

const coordinatorThirdSHA = "cccccccccccccccccccccccccccccccccccccccc"

type coordinatorTestClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (clock *coordinatorTestClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *coordinatorTestClock) NewTimer(delay time.Duration) Timer {
	return realTimer{Timer: time.NewTimer(delay)}
}

func (clock *coordinatorTestClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	clock.now = clock.now.Add(duration)
	clock.mutex.Unlock()
}

type coordinatorTestResolver struct {
	mutex  sync.Mutex
	sha    string
	err    error
	calls  int
	scopes []SourceScope
}

type advancingCoordinatorResolver struct {
	mutex    sync.Mutex
	clock    *coordinatorTestClock
	advance  time.Duration
	advances []time.Duration
	sha      string
	calls    int
	called   chan int
}

func (resolver *advancingCoordinatorResolver) ResolveHead(context.Context, SourceScope) (string, error) {
	resolver.mutex.Lock()
	resolver.calls++
	call := resolver.calls
	advance := resolver.advance
	if call <= len(resolver.advances) {
		advance = resolver.advances[call-1]
	}
	sha := resolver.sha
	called := resolver.called
	resolver.mutex.Unlock()
	resolver.clock.Advance(advance)
	if called != nil {
		called <- call
	}
	return sha, nil
}

func (resolver *advancingCoordinatorResolver) Calls() int {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return resolver.calls
}

type barrierCoordinatorResolver struct {
	mutex        sync.Mutex
	shas         []string
	calls        int
	started      chan int
	firstRelease <-chan struct{}
}

func (resolver *barrierCoordinatorResolver) ResolveHead(ctx context.Context, _ SourceScope) (string, error) {
	resolver.mutex.Lock()
	resolver.calls++
	call := resolver.calls
	sha := resolver.shas[len(resolver.shas)-1]
	if call <= len(resolver.shas) {
		sha = resolver.shas[call-1]
	}
	started := resolver.started
	firstRelease := resolver.firstRelease
	resolver.mutex.Unlock()
	if started != nil {
		started <- call
	}
	if call == 1 && firstRelease != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-firstRelease:
		}
	}
	return sha, nil
}

func (resolver *barrierCoordinatorResolver) Calls() int {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return resolver.calls
}

func (resolver *coordinatorTestResolver) ResolveHead(_ context.Context, scope SourceScope) (string, error) {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	resolver.calls++
	resolver.scopes = append(resolver.scopes, scope)
	return resolver.sha, resolver.err
}

func (resolver *coordinatorTestResolver) Set(sha string, err error) {
	resolver.mutex.Lock()
	resolver.sha, resolver.err = sha, err
	resolver.mutex.Unlock()
}

func (resolver *coordinatorTestResolver) Calls() int {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return resolver.calls
}

type failLinkOnceRepository struct {
	*Repository
	mutex sync.Mutex
	fail  bool
}

type finalizeBarrierRepository struct {
	*Repository
	mutex   sync.Mutex
	blocked bool
	reached chan struct{}
	release <-chan struct{}
}

type reserveBarrierRepository struct {
	*Repository
	once    sync.Once
	reached chan struct{}
	release <-chan struct{}
}

func (repository *reserveBarrierRepository) ReserveResolve(ctx context.Context, lease WorkLease, generation uint64, at time.Time) error {
	blocked := false
	repository.once.Do(func() {
		blocked = true
		close(repository.reached)
	})
	if blocked {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-repository.release:
		}
	}
	return repository.Repository.ReserveResolve(ctx, lease, generation, at)
}

type coordinatorEventRecorder struct {
	mutex  sync.Mutex
	events []CoordinatorEvent
}

type failReleaseRepository struct {
	*Repository
	mutex sync.Mutex
	calls int
	err   error
}

func (repository *failReleaseRepository) ReleaseLease(context.Context, WorkLease, time.Time) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.calls++
	return repository.err
}

func (repository *failReleaseRepository) Calls() int {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	return repository.calls
}

func (recorder *coordinatorEventRecorder) Observe(_ context.Context, event CoordinatorEvent) {
	recorder.mutex.Lock()
	recorder.events = append(recorder.events, event)
	recorder.mutex.Unlock()
}

func (recorder *coordinatorEventRecorder) Events() []CoordinatorEvent {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]CoordinatorEvent(nil), recorder.events...)
}

func (recorder *coordinatorEventRecorder) Last() (CoordinatorEvent, bool) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	if len(recorder.events) == 0 {
		return CoordinatorEvent{}, false
	}
	return recorder.events[len(recorder.events)-1], true
}

func (recorder *coordinatorEventRecorder) OutcomeCount(outcome string) int {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	var count int
	for _, event := range recorder.events {
		if event.Outcome == outcome {
			count++
		}
	}
	return count
}

func (recorder *coordinatorEventRecorder) Paused() uint64 {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	var paused uint64
	for _, event := range recorder.events {
		paused += event.Paused
	}
	return paused
}

func (repository *finalizeBarrierRepository) FinalizeResolvedHead(ctx context.Context, lease WorkLease, generation uint64, sha string, nextReconcileAt, at time.Time) error {
	repository.mutex.Lock()
	block := !repository.blocked
	repository.blocked = true
	repository.mutex.Unlock()
	if block {
		close(repository.reached)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-repository.release:
		}
	}
	return repository.Repository.FinalizeResolvedHead(ctx, lease, generation, sha, nextReconcileAt, at)
}

type busyJobCreator struct{ calls int }

func (creator *busyJobCreator) CreateWithInputFinalized(jobs.CreateRequest, jobs.CreateFinalizer) (jobs.Job, bool, error) {
	creator.calls++
	return jobs.Job{}, false, jobs.ErrApplicationBusy
}

type beforeCreateJobCreator struct {
	delegate JobCreator
	before   func() error
}

func (creator *beforeCreateJobCreator) CreateWithInputFinalized(request jobs.CreateRequest, finalize jobs.CreateFinalizer) (jobs.Job, bool, error) {
	if err := creator.before(); err != nil {
		return jobs.Job{}, false, err
	}
	return creator.delegate.CreateWithInputFinalized(request, finalize)
}

type sourceAccessLostLinkRepository struct{ *Repository }

func (repository *sourceAccessLostLinkRepository) LinkDispatchJobTx(context.Context, *sql.Tx, WorkLease, uint64, uint64, string, time.Time) error {
	return ErrSourceAccessLost
}

func (repository *failLinkOnceRepository) LinkDispatchJob(ctx context.Context, lease WorkLease, sequence, generation uint64, jobID string, at time.Time) error {
	repository.mutex.Lock()
	if repository.fail {
		repository.fail = false
		repository.mutex.Unlock()
		return errors.New("simulated persistence failure")
	}
	repository.mutex.Unlock()
	return repository.Repository.LinkDispatchJob(ctx, lease, sequence, generation, jobID, at)
}

func (repository *failLinkOnceRepository) LinkDispatchJobTx(ctx context.Context, tx *sql.Tx, lease WorkLease, sequence, generation uint64, jobID string, at time.Time) error {
	repository.mutex.Lock()
	if repository.fail {
		repository.fail = false
		repository.mutex.Unlock()
		return errors.New("simulated persistence failure")
	}
	repository.mutex.Unlock()
	return repository.Repository.LinkDispatchJobTx(ctx, tx, lease, sequence, generation, jobID, at)
}

func TestCoordinatorOfflineStartupUsesPersistedScopeAndCreatesSanitizedJob(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: secondSHA}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)

	claimed, event := coordinator.processOne(context.Background())
	if !claimed || event.Dispatched != 1 || event.Resolved != 1 {
		t.Fatalf("startup event=%#v claimed=%v", event, claimed)
	}
	resolved, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || resolved.ActiveSHA != secondSHA || resolved.LatestResolvedSHA != secondSHA {
		t.Fatalf("resolved status=%#v err=%v", resolved, err)
	}
	if len(resolver.scopes) != 1 || resolver.scopes[0] != (SourceScope{OwnerUserID: testOwner, ConnectionID: testConnection, InstallationID: testInstallation, RepositoryID: testRepository, Branch: "main", Ref: testRef}) {
		t.Fatalf("resolver scope=%#v configured=%#v", resolver.scopes, status)
	}
	assertSingleCoordinatorJob(t, fixture, resolved, 1)
}

func TestCoordinatorConsumesNewestACKAndCoalescesSeveralPushes(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now.Add(time.Second))
	commitDesired(t, fixture, status, 2, secondSHA, fixture.now.Add(2*time.Second))
	commitDesired(t, fixture, status, 3, coordinatorThirdSHA, fixture.now.Add(3*time.Second))
	clock := &coordinatorTestClock{now: fixture.now.Add(4 * time.Second)}
	resolver := &coordinatorTestResolver{sha: coordinatorThirdSHA}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)

	if claimed, event := coordinator.processOne(context.Background()); !claimed || event.Dispatched != 1 {
		t.Fatalf("coalesced event=%#v claimed=%v", event, claimed)
	}
	got, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || got.LastConsumedGeneration != 3 || got.LatestResolvedGeneration != 3 || got.ActiveSHA != coordinatorThirdSHA {
		t.Fatalf("coalesced status=%#v err=%v", got, err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d", resolver.calls)
	}
	assertSingleCoordinatorJob(t, fixture, got, 1)
}

func TestCoordinatorSequentialACKStormHonorsDurableResolveCooldown(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Minute
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, event := coordinator.processOne(context.Background()); event.Dispatched != 1 || resolver.calls != 1 {
		t.Fatalf("initial event=%#v resolver_calls=%d", event, resolver.calls)
	}
	resolver.Set(secondSHA, nil)
	for generation := uint64(1); generation <= 11; generation++ {
		clock.Advance(5 * time.Second)
		commitDesired(t, fixture, status, generation, secondSHA, clock.Now())
		coordinator.processOne(context.Background())
		if resolver.calls != 1 {
			t.Fatalf("generation=%d bypassed cooldown calls=%d", generation, resolver.calls)
		}
	}
	before, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || before.LastConsumedGeneration != 0 {
		t.Fatalf("cooldown consumed durable ACK early status=%#v err=%v", before, err)
	}
	clock.Advance(5 * time.Second)
	commitDesired(t, fixture, status, 12, secondSHA, clock.Now())
	if _, event := coordinator.processOne(context.Background()); event.Resolved != 1 || resolver.calls != 2 {
		t.Fatalf("eligible latest convergence event=%#v calls=%d", event, resolver.calls)
	}
	after, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || after.LastConsumedGeneration != 12 || after.LatestResolvedSHA != secondSHA || after.ActiveSHA != testSHA {
		t.Fatalf("latest storm state=%#v err=%v", after, err)
	}
	assertSingleCoordinatorJob(t, fixture, after, 1)
}

func TestCoordinatorRepeatedReadyStormHonorsPersistedCooldown(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Minute
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.processOne(context.Background())
	active, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := insertActualRelease(t, fixture, testSHA, active.ActiveDispatchSequence)
	insertSucceededDeployment(t, fixture, testApp, releaseID, active.ActiveJobID)
	clock.Advance(5 * time.Second)
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',updated_at=?,finished_at=? WHERE id=?`, timestamp(clock.Now()), timestamp(clock.Now()), active.ActiveJobID); err != nil {
		t.Fatal(err)
	}
	coordinator.processOne(context.Background())
	for range 1000 {
		coordinator.forceReconcile(context.Background(), false)
	}
	coordinator.processOne(context.Background())
	if resolver.calls != 1 {
		t.Fatalf("Ready storm bypassed cooldown calls=%d", resolver.calls)
	}
	clock.Advance(55 * time.Second)
	coordinator.forceReconcile(context.Background(), false)
	if _, event := coordinator.processOne(context.Background()); event.Resolved != 1 || resolver.calls != 2 {
		t.Fatalf("eligible Ready reconciliation event=%#v calls=%d", event, resolver.calls)
	}
	got, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || got.State != StateIdle || got.ActiveJobID != "" || got.DispatchSequence != 1 {
		t.Fatalf("Ready convergence status=%#v err=%v", got, err)
	}
}

func TestCoordinatorSlowResolverCannotMutateAfterLeaseExpiry(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now)
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &advancingCoordinatorResolver{clock: clock, advance: time.Second, sha: testSHA}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.LeaseTTL = time.Second
	config.MinResolveInterval = time.Nanosecond
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, event := coordinator.processOne(context.Background()); event.Outcome != OutcomePersistenceUnavailable || event.Resolved != 0 || event.Dispatched != 0 {
		t.Fatalf("expired resolver event=%#v", event)
	}
	got, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || got.LatestResolvedSHA != "" || got.ActiveJobID != "" || got.LastConsumedGeneration != 0 || got.LastReconciledAt == nil || !got.LastReconciledAt.Equal(fixture.now) || len(coordinator.wake) != 1 {
		t.Fatalf("expired resolver mutated state=%#v wake=%d err=%v", got, len(coordinator.wake), err)
	}
	reservedGeneration, reservedFence := loadResolveReservation(t, fixture.db, testApp)
	if !reservedGeneration.Valid || reservedGeneration.Int64 != 1 || !reservedFence.Valid || uint64(reservedFence.Int64) != got.LeaseFence {
		t.Fatalf("expired resolver lost reservation generation=%#v fence=%#v status=%#v", reservedGeneration, reservedFence, got)
	}
	assertSingleCoordinatorJob(t, fixture, got, 0)
}

func TestCoordinatorRunReservesCooldownBeforeSlowResolve(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, secondSHA, fixture.now)
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &advancingCoordinatorResolver{
		clock:    clock,
		advances: []time.Duration{time.Second, 0},
		sha:      secondSHA,
		called:   make(chan int, 2),
	}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.PollInterval = time.Hour
	config.LeaseTTL = time.Second
	config.MinResolveInterval = time.Minute
	config.BatchSize = 32
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		coordinator.Wake()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("coordinator did not stop")
		}
	})

	select {
	case call := <-resolver.called:
		if call != 1 {
			t.Fatalf("first resolver call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("slow resolver was not called")
	}
	waitCoordinatorStatus(t, fixture.repository, func(value Status) bool {
		return value.LeaseExpiresAt == nil && value.LastReconciledAt != nil && value.LastReconciledAt.Equal(fixture.now)
	})
	for range 1000 {
		coordinator.Wake()
	}
	select {
	case call := <-resolver.called:
		t.Fatalf("cooldown allowed resolver call %d before eligibility", call)
	case <-time.After(100 * time.Millisecond):
	}
	reserved, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || reserved.LastConsumedGeneration != 0 || reserved.LatestResolvedSHA != "" || resolver.Calls() != 1 {
		t.Fatalf("reservation state=%#v calls=%d err=%v", reserved, resolver.Calls(), err)
	}

	clock.Advance(59 * time.Second)
	coordinator.Wake()
	select {
	case call := <-resolver.called:
		if call != 2 {
			t.Fatalf("eligible resolver call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("eligible resolver call was not made")
	}
	resolved := waitCoordinatorStatus(t, fixture.repository, func(value Status) bool {
		return value.LastConsumedGeneration == 1 && value.LatestResolvedSHA == secondSHA && value.ActiveJobID != ""
	})
	if resolver.Calls() != 2 {
		t.Fatalf("resolver calls=%d status=%#v", resolver.Calls(), resolved)
	}
}

func TestCoordinatorRunHonorsCooldownReservationAcrossRestart(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, secondSHA, fixture.now)
	_, crashedLease, err := fixture.repository.ClaimDueWithResolveCutoff(context.Background(), uuid.NewString(), fixture.now, time.Second, fixture.now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	head, err := fixture.repository.PeekNewestACK(context.Background(), crashedLease, fixture.now)
	if err != nil || head.Generation != 1 {
		t.Fatalf("peek head=%#v err=%v", head, err)
	}
	if err = fixture.repository.ReserveResolve(context.Background(), crashedLease, head.Generation, fixture.now); err != nil {
		t.Fatal(err)
	}
	reservedGeneration, reservedFence := loadResolveReservation(t, fixture.db, testApp)
	if !reservedGeneration.Valid || reservedGeneration.Int64 != 1 || !reservedFence.Valid || uint64(reservedFence.Int64) != crashedLease.Fence {
		t.Fatalf("crash reservation generation=%#v fence=%#v lease=%#v", reservedGeneration, reservedFence, crashedLease)
	}

	clock := &coordinatorTestClock{now: fixture.now.Add(time.Second)}
	resolver := &advancingCoordinatorResolver{clock: clock, sha: secondSHA, called: make(chan int, 1)}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.PollInterval = time.Hour
	config.LeaseTTL = time.Second
	config.MinResolveInterval = time.Minute
	config.BatchSize = 32
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		coordinator.Wake()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("coordinator did not stop")
		}
	})
	waitCoordinatorStatus(t, fixture.repository, func(value Status) bool {
		return value.LeaseExpiresAt == nil && value.LastReconciledAt != nil && value.LastReconciledAt.Equal(fixture.now)
	})
	select {
	case call := <-resolver.called:
		t.Fatalf("restart bypassed durable cooldown with resolver call %d", call)
	case <-time.After(100 * time.Millisecond):
	}
	reserved, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || reserved.LastConsumedGeneration != 0 || reserved.LatestResolvedSHA != "" || resolver.Calls() != 0 {
		t.Fatalf("restart reservation state=%#v calls=%d err=%v", reserved, resolver.Calls(), err)
	}
	reservedGeneration, reservedFence = loadResolveReservation(t, fixture.db, testApp)
	if !reservedGeneration.Valid || reservedGeneration.Int64 != 1 || !reservedFence.Valid || uint64(reservedFence.Int64) != crashedLease.Fence {
		t.Fatalf("restart lost durable reservation generation=%#v fence=%#v", reservedGeneration, reservedFence)
	}

	clock.Advance(59 * time.Second)
	coordinator.Wake()
	select {
	case call := <-resolver.called:
		if call != 1 {
			t.Fatalf("restart eligible resolver call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("restart never resolved newest ACK after cooldown")
	}
	resolved := waitCoordinatorStatus(t, fixture.repository, func(value Status) bool {
		return value.LastConsumedGeneration == 1 && value.LatestResolvedGeneration == 1 && value.LatestResolvedSHA == secondSHA
	})
	if resolver.Calls() != 1 {
		t.Fatalf("restart resolver calls=%d status=%#v", resolver.Calls(), resolved)
	}
	reservedGeneration, reservedFence = loadResolveReservation(t, fixture.db, testApp)
	if reservedGeneration.Valid || reservedFence.Valid {
		t.Fatalf("successful restart retained reservation generation=%#v fence=%#v", reservedGeneration, reservedFence)
	}
}

func TestCoordinatorFinalizesReservedGenerationPrunedDuringResolve(t *testing.T) {
	runCoordinatorExactGenerationBarrier(t, true)
}

func TestCoordinatorFinalizesReservedGenerationPrunedBeforeFinalize(t *testing.T) {
	runCoordinatorExactGenerationBarrier(t, false)
}

func runCoordinatorExactGenerationBarrier(t *testing.T, duringResolve bool) {
	t.Helper()
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now)
	clock := &coordinatorTestClock{now: fixture.now}
	resolverRelease := make(chan struct{})
	resolver := &barrierCoordinatorResolver{
		shas:    []string{secondSHA, coordinatorThirdSHA},
		started: make(chan int, 2),
	}
	var repository CoordinatorRepository = fixture.repository
	var finalizeReached chan struct{}
	var finalizeRelease chan struct{}
	if duringResolve {
		resolver.firstRelease = resolverRelease
	} else {
		finalizeReached = make(chan struct{})
		finalizeRelease = make(chan struct{})
		repository = &finalizeBarrierRepository{Repository: fixture.repository, reached: finalizeReached, release: finalizeRelease}
	}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Minute
	coordinator, err := NewCoordinator(repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	type processResult struct {
		claimed bool
		event   CoordinatorEvent
	}
	result := make(chan processResult, 1)
	go func() {
		claimed, event := coordinator.processOne(context.Background())
		result <- processResult{claimed: claimed, event: event}
	}()

	select {
	case call := <-resolver.started:
		if call != 1 {
			t.Fatalf("first resolver call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("first resolver call did not start")
	}
	if !duringResolve {
		select {
		case <-finalizeReached:
		case <-time.After(time.Second):
			t.Fatal("provider returned but exact finalization was not reached")
		}
	}
	for generation := uint64(2); generation <= 33; generation++ {
		commitDesired(t, fixture, status, generation, coordinatorThirdSHA, fixture.now.Add(time.Duration(generation)*time.Second))
	}
	var retained int
	if err = fixture.db.QueryRow(`SELECT COUNT(*) FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=? AND generation=1`, testController, status.SubscriptionID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("reserved raw generation was not pruned count=%d err=%v", retained, err)
	}
	if duringResolve {
		close(resolverRelease)
	} else {
		close(finalizeRelease)
	}
	select {
	case processed := <-result:
		if !processed.claimed || processed.event.Resolved != 1 || processed.event.Dispatched != 1 {
			t.Fatalf("first exact-generation result=%#v claimed=%t", processed.event, processed.claimed)
		}
	case <-time.After(time.Second):
		t.Fatal("first exact-generation reconciliation did not finish")
	}
	first, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || first.LastConsumedGeneration != 1 || first.LatestResolvedGeneration != 1 || first.LatestResolvedSHA != secondSHA || first.ActiveGeneration != 1 || first.ActiveSHA != secondSHA || resolver.Calls() != 1 {
		t.Fatalf("newer ACK was lost or mis-associated status=%#v calls=%d err=%v", first, resolver.Calls(), err)
	}
	assertSingleCoordinatorJob(t, fixture, first, 1)

	clock.Advance(59 * time.Second)
	if claimed, event := coordinator.processOne(context.Background()); event.Resolved != 0 || event.Dispatched != 0 || resolver.Calls() != 1 {
		t.Fatalf("cooldown bypassed claimed=%t event=%#v calls=%d", claimed, event, resolver.Calls())
	}
	stillPending, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || stillPending.LastConsumedGeneration != 1 || stillPending.LatestResolvedGeneration != 1 || stillPending.LatestResolvedSHA != secondSHA {
		t.Fatalf("pending generation changed during cooldown status=%#v err=%v", stillPending, err)
	}

	clock.Advance(time.Second)
	claimed, event := coordinator.processOne(context.Background())
	if !claimed || event.Resolved != 1 || event.Dispatched != 0 || resolver.Calls() != 2 {
		t.Fatalf("eligible exact convergence claimed=%t event=%#v calls=%d", claimed, event, resolver.Calls())
	}
	converged, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || converged.LastConsumedGeneration != 33 || converged.LatestResolvedGeneration != 33 || converged.LatestResolvedSHA != coordinatorThirdSHA || converged.ActiveGeneration != 1 || converged.ActiveSHA != secondSHA {
		t.Fatalf("exact latest convergence status=%#v err=%v", converged, err)
	}
	assertSingleCoordinatorJob(t, fixture, converged, 1)
}

func TestCoordinatorStartupRearmsResolvedCrashWithoutStealingLiveLease(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	_, crashedLease, err := fixture.repository.ClaimDue(context.Background(), uuid.NewString(), fixture.now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = reserveAndFinalize(context.Background(), fixture.repository, crashedLease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now.Add(30 * time.Second)}
	resolver := &coordinatorTestResolver{sha: testSHA}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Minute
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.forceReconcile(context.Background(), true)
	live, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || live.LeaseExpiresAt == nil || !live.LeaseExpiresAt.Equal(crashedLease.ExpiresAt) || live.LeaseFence != crashedLease.Fence {
		t.Fatalf("startup altered live lease status=%#v lease=%#v err=%v", live, crashedLease, err)
	}
	if claimed, _ := coordinator.processOne(context.Background()); claimed {
		t.Fatal("startup stole live lease")
	}
	clock.Advance(31 * time.Second)
	if claimed, event := coordinator.processOne(context.Background()); !claimed || event.Dispatched != 1 || resolver.calls != 1 {
		t.Fatalf("expired crash recovery claimed=%t event=%#v calls=%d", claimed, event, resolver.calls)
	}
	recovered, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || recovered.State != StateDeploying || recovered.ActiveJobID == "" || recovered.DispatchSequence != 1 {
		t.Fatalf("resolved crash recovery=%#v err=%v", recovered, err)
	}
}

func TestCoordinatorPreparedDispatchReplaysCreateAndLinkCrashSafely(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	repository := &failLinkOnceRepository{Repository: fixture.repository, fail: true}
	recorder := &coordinatorEventRecorder{}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Nanosecond
	config.BatchSize = 1
	config.Observer = recorder.Observe
	coordinator, err := NewCoordinator(repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}

	coordinator.drain(context.Background())
	event, ok := recorder.Last()
	if !ok || event.Outcome != OutcomePersistenceUnavailable || event.State != StateDispatching || event.PauseCode != ObservedPauseNone || event.RetryAttempt != 0 || event.NextAction != NextActionDispatch || event.Dispatched != 0 {
		t.Fatalf("failed link event=%#v", event)
	}
	prepared, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || prepared.State != StateDispatching || prepared.PreparedDispatchSequence != 1 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	assertSingleCoordinatorJob(t, fixture, prepared, 0)
	coordinator.drain(context.Background())
	event, ok = recorder.Last()
	if !ok || event.Dispatched != 1 || event.State != StateDeploying || event.NextAction != NextActionPollJob {
		t.Fatalf("replay event=%#v", event)
	}
	linked, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || linked.State != StateDeploying || linked.ActiveJobID == "" {
		t.Fatalf("linked=%#v err=%v", linked, err)
	}
	assertSingleCoordinatorJob(t, fixture, linked, 1)
}

func TestCoordinatorAccessLossDuringDispatchIsObservedWithoutOrphan(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	repository := &sourceAccessLostLinkRepository{Repository: fixture.repository}
	creator := &beforeCreateJobCreator{
		delegate: jobs.New(fixture.db),
		before: func() error {
			removedAt := clock.Now().Add(time.Nanosecond)
			decision, removeErr := controllerrelay.NewRepository(fixture.db).CommitAccessChange(context.Background(), testController, protocol.AccessChange{
				Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), removedAt), EventID: uuid.NewString(),
				InstallationID: testInstallation, RepositoryID: testRepository, ChangeCode: "repository.removed", ObservedAt: removedAt, AckRequired: true,
			}, removedAt)
			if removeErr != nil {
				return removeErr
			}
			if decision.Kind != controllerrelay.DecisionAck {
				return errors.New("access loss was not durably acknowledged")
			}
			return nil
		},
	}
	recorder := &coordinatorEventRecorder{}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Nanosecond
	config.BatchSize = 1
	config.Observer = recorder.Observe
	coordinator, err := NewCoordinator(repository, &coordinatorTestResolver{sha: testSHA}, creator, config)
	if err != nil {
		t.Fatal(err)
	}

	coordinator.drain(context.Background())
	event, ok := recorder.Last()
	if !ok || event.Outcome != OutcomeSourceAccessLost || event.State != StatePaused || event.PauseCode != PauseSourceAccessLost || event.RetryAttempt != 0 || event.NextAction != NextActionResumeRequired || event.Paused != 1 || event.Dispatched != 0 {
		t.Fatalf("access-loss link event=%#v", event)
	}
	paused, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || paused.State != StatePaused || paused.PauseCode != PauseSourceAccessLost || paused.LeaseExpiresAt != nil || paused.PreparedDispatchSequence != 0 || paused.ActiveJobID != "" || paused.Revision != status.Revision {
		t.Fatalf("access-loss durable state=%#v err=%v", paused, err)
	}
	assertSingleCoordinatorJob(t, fixture, paused, 0)
}

func TestAtomicDispatchAccessLossOrderingNeverCreatesOrphan(t *testing.T) {
	for _, accessFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "access_first", false: "transaction_first"}[accessFirst], func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			ctx := context.Background()
			status, err := fixture.repository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			_, lease, err := fixture.repository.ClaimDue(ctx, uuid.NewString(), fixture.now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = reserveAndFinalize(ctx, fixture.repository, lease, 0, testSHA, fixture.now.Add(6*time.Hour), fixture.now); err != nil {
				t.Fatal(err)
			}
			dispatch, err := fixture.repository.PrepareDispatch(ctx, lease, fixture.now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			request := jobs.CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: testApp, IdempotencyKey: DispatchIdempotencyKey(status.Revision, dispatch.Sequence), RequestedBy: status.SourceOwnerUserID, Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}}
			removeAccess := func() (controllerrelay.InboxDecision, error) {
				removedAt := fixture.now.Add(3 * time.Second)
				return controllerrelay.NewRepository(fixture.db).CommitAccessChange(ctx, testController, protocol.AccessChange{
					Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), removedAt), EventID: uuid.NewString(),
					InstallationID: testInstallation, RepositoryID: testRepository, ChangeCode: "repository.removed", ObservedAt: removedAt, AckRequired: true,
				}, removedAt)
			}
			service := jobs.New(fixture.db)
			if accessFirst {
				decision, removeErr := removeAccess()
				if removeErr != nil || decision.Kind != controllerrelay.DecisionAck {
					t.Fatalf("access removal=%#v err=%v", decision, removeErr)
				}
				_, _, createErr := service.CreateWithInputFinalized(request, func(tx *sql.Tx, job jobs.Job) error {
					return fixture.repository.LinkDispatchJobTx(ctx, tx, lease, dispatch.Sequence, dispatch.Generation, job.ID, fixture.now.Add(4*time.Second))
				})
				if createErr == nil {
					t.Fatal("access-first dispatch unexpectedly committed")
				}
				assertSingleCoordinatorJob(t, fixture, status, 0)
				return
			}

			linked := make(chan struct{})
			releaseFinalize := make(chan struct{})
			type createResult struct {
				job jobs.Job
				err error
			}
			created := make(chan createResult, 1)
			go func() {
				job, _, createErr := service.CreateWithInputFinalized(request, func(tx *sql.Tx, job jobs.Job) error {
					if linkErr := fixture.repository.LinkDispatchJobTx(ctx, tx, lease, dispatch.Sequence, dispatch.Generation, job.ID, fixture.now.Add(2*time.Second)); linkErr != nil {
						return linkErr
					}
					close(linked)
					<-releaseFinalize
					return nil
				})
				created <- createResult{job: job, err: createErr}
			}()
			<-linked
			type accessResult struct {
				decision controllerrelay.InboxDecision
				err      error
			}
			removed := make(chan accessResult, 1)
			go func() {
				decision, removeErr := removeAccess()
				removed <- accessResult{decision: decision, err: removeErr}
			}()
			close(releaseFinalize)
			create := <-created
			access := <-removed
			if create.err != nil || create.job.ID == "" || access.err != nil || access.decision.Kind != controllerrelay.DecisionAck {
				t.Fatalf("ordered create=%#v access=%#v", create, access)
			}
			tracked, err := fixture.repository.Get(ctx, testApp)
			if err != nil || tracked.State != StatePaused || tracked.PauseCode != PauseSourceAccessLost || tracked.ActiveJobID != create.job.ID {
				t.Fatalf("transaction-first job not tracked status=%#v job=%#v err=%v", tracked, create.job, err)
			}
			assertSingleCoordinatorJob(t, fixture, tracked, 1)
		})
	}
}

func TestCoordinatorPushDuringActiveSchedulesExactlyOneFollowup(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)
	coordinator.processOne(context.Background())
	active, _ := fixture.repository.Get(context.Background(), testApp)
	commitDesired(t, fixture, status, 1, coordinatorThirdSHA, fixture.now.Add(time.Second))
	resolver.Set(coordinatorThirdSHA, nil)
	clock.Advance(time.Second)
	coordinator.processOne(context.Background())

	releaseID := insertActualRelease(t, fixture, secondSHA, active.ActiveDispatchSequence)
	insertSucceededDeployment(t, fixture, testApp, releaseID, active.ActiveJobID)
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',updated_at=? WHERE id=?`, timestamp(clock.Now()), active.ActiveJobID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	coordinator.processOne(context.Background())
	coordinator.processOne(context.Background())
	coordinator.processOne(context.Background())

	got, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || got.LastSuccessfulDeployedSHA != secondSHA || got.ActiveSHA != coordinatorThirdSHA || got.DispatchSequence != 2 {
		t.Fatalf("followup status=%#v err=%v", got, err)
	}
	assertSingleCoordinatorJob(t, fixture, got, 2)
}

func TestCoordinatorKnownNewerSHAFollowsActiveFailureExactlyOnce(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)
	coordinator.processOne(context.Background())
	first, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Second)
	commitDesired(t, fixture, status, 1, secondSHA, clock.Now())
	resolver.Set(secondSHA, nil)
	coordinator.processOne(context.Background())
	knownNewer, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || knownNewer.ActiveSHA != testSHA || knownNewer.LatestResolvedSHA != secondSHA {
		t.Fatalf("known newer status=%#v err=%v", knownNewer, err)
	}
	clock.Advance(5 * time.Second)
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='failed',phase='failed',error_code='compose_apply_failed',updated_at=?,finished_at=? WHERE id=?`, timestamp(clock.Now()), timestamp(clock.Now()), first.ActiveJobID); err != nil {
		t.Fatal(err)
	}
	coordinator.processOne(context.Background())
	coordinator.processOne(context.Background())
	coordinator.processOne(context.Background())
	followup, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || followup.State != StateDeploying || followup.ActiveSHA != secondSHA || followup.DispatchSequence != 2 {
		t.Fatalf("failure followup=%#v err=%v", followup, err)
	}
	assertSingleCoordinatorJob(t, fixture, followup, 2)
}

func TestCoordinatorActualNewerHeadConvergesWithoutDuplicate(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)
	coordinator.processOne(context.Background())
	active, _ := fixture.repository.Get(context.Background(), testApp)
	releaseID := insertActualRelease(t, fixture, secondSHA, active.ActiveDispatchSequence)
	insertSucceededDeployment(t, fixture, testApp, releaseID, active.ActiveJobID)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',updated_at=? WHERE id=?`, timestamp(clock.Now()), active.ActiveJobID); err != nil {
		t.Fatal(err)
	}
	resolver.Set(secondSHA, nil)
	clock.Advance(5 * time.Second)
	coordinator.processOne(context.Background())
	coordinator.processOne(context.Background())

	got, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || got.LastSuccessfulDeployedSHA != secondSHA || got.ActiveJobID != "" || got.DispatchSequence != 1 {
		t.Fatalf("newer actual status=%#v err=%v", got, err)
	}
	assertSingleCoordinatorJob(t, fixture, got, 1)
}

func TestCoordinatorProviderRetryExhaustionAndNewSHAResume(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{err: &SourceError{Code: OutcomeProviderUnavailable}}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)
	if _, event := coordinator.processOne(context.Background()); event.Retried != 1 {
		t.Fatalf("first retry event=%#v", event)
	}
	assertNoResolveReservation(t, fixture.db, testApp)
	clock.Advance(15 * time.Second)
	if _, event := coordinator.processOne(context.Background()); event.Paused != 1 || event.Outcome != OutcomeProviderUnavailable {
		current, currentErr := fixture.repository.Get(context.Background(), testApp)
		t.Fatalf("exhausted event=%#v status=%#v err=%v", event, current, currentErr)
	}
	paused, _ := fixture.repository.Get(context.Background(), testApp)
	if paused.State != StatePaused || paused.PausedSHA != testSHA {
		t.Fatalf("paused=%#v", paused)
	}
	assertNoResolveReservation(t, fixture.db, testApp)

	resolver.Set(testSHA, nil)
	commitDesired(t, fixture, status, 1, secondSHA, clock.Now().Add(time.Second))
	clock.Advance(time.Second)
	coordinator.processOne(context.Background())
	stillPaused, _ := fixture.repository.Get(context.Background(), testApp)
	if stillPaused.State != StatePaused || stillPaused.ActiveJobID != "" {
		t.Fatalf("same authoritative SHA resumed=%#v", stillPaused)
	}

	resolver.Set(coordinatorThirdSHA, nil)
	commitDesired(t, fixture, status, 2, secondSHA, clock.Now().Add(time.Second))
	clock.Advance(time.Second)
	coordinator.processOne(context.Background())
	resumed, _ := fixture.repository.Get(context.Background(), testApp)
	if resumed.State != StateDeploying || resumed.ActiveSHA != coordinatorThirdSHA {
		t.Fatalf("new authoritative SHA did not resume=%#v", resumed)
	}
}

func TestCoordinatorPersistenceSourceErrorRetriesWithoutInvalidSourcePause(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{err: &SourceError{Code: OutcomePersistenceUnavailable}}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobs.New(fixture.db), clock)
	if _, event := coordinator.processOne(context.Background()); event.Retried != 1 || event.Outcome != OutcomePersistenceUnavailable {
		t.Fatalf("persistence retry event=%#v", event)
	}
	got, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || got.State != StateRetryWait || got.PauseCode != "" || got.LastReconciledAt == nil {
		t.Fatalf("persistence retry status=%#v err=%v", got, err)
	}
	assertNoResolveReservation(t, fixture.db, testApp)
}

func TestCoordinatorObserverEmitsOnlyAllowlistedLifecycleValues(t *testing.T) {
	recorder := &coordinatorEventRecorder{}
	coordinator := &Coordinator{config: CoordinatorConfig{Observer: recorder.Observe}}
	valid := []CoordinatorEvent{
		{Outcome: OutcomeIdle, State: StateIdle, NextAction: NextActionResolve},
		{Outcome: OutcomeIdle, State: StateDispatching, NextAction: NextActionDispatch},
		{Outcome: OutcomeIdle, State: StateDeploying, NextAction: NextActionPollJob},
		{Outcome: OutcomeProviderUnavailable, State: StateRetryWait, RetryAttempt: 7, NextAction: NextActionRetry},
		{Outcome: OutcomeIdle, State: StateIdle, NextAction: NextActionResolveCooldown},
	}
	for _, pauseCode := range []string{
		PauseApprovalRequired, PauseDeploymentFailed, PauseMissingConfig, PauseSourceAccessLost,
		PauseInvalidSource, PauseProviderUnavailable, PauseRelayUnavailable,
	} {
		action := NextActionResumeRequired
		if pauseCode == PauseApprovalRequired {
			action = NextActionApprovalRequired
		}
		valid = append(valid, CoordinatorEvent{Outcome: OutcomeDeploymentFailed, State: StatePaused, PauseCode: pauseCode, NextAction: action})
	}
	for _, event := range valid {
		coordinator.observe(context.Background(), event)
	}

	const secret = "token=secret repository=private sha=aaaaaaaa raw=provider-body"
	coordinator.observe(context.Background(), CoordinatorEvent{
		Outcome: secret, State: secret, PauseCode: secret, NextAction: secret, RetryAttempt: ^uint32(0),
	})

	allowedOutcomes := map[string]bool{
		OutcomeIdle: true, OutcomePersistenceUnavailable: true, OutcomeProviderUnavailable: true,
		OutcomeSourceAccessLost: true, OutcomeInvalidSource: true, OutcomeApplicationBusy: true,
		OutcomeDeploymentFailed: true,
	}
	allowedStates := map[string]bool{
		ObservedStateNone: true, StateDisabled: true, StateIdle: true, StateDispatching: true,
		StateDeploying: true, StatePaused: true, StateRetryWait: true,
	}
	allowedPauses := map[string]bool{
		ObservedPauseNone: true, PauseApprovalRequired: true, PauseDeploymentFailed: true,
		PauseMissingConfig: true, PauseSourceAccessLost: true, PauseInvalidSource: true,
		PauseProviderUnavailable: true, PauseRelayUnavailable: true,
	}
	allowedActions := map[string]bool{
		NextActionNone: true, NextActionResolve: true, NextActionResolveCooldown: true,
		NextActionDispatch: true, NextActionPollJob: true, NextActionRetry: true,
		NextActionApprovalRequired: true, NextActionResumeRequired: true,
	}
	events := recorder.Events()
	if len(events) != len(valid)+1 {
		t.Fatalf("observer events=%d want=%d", len(events), len(valid)+1)
	}
	for _, event := range events {
		if !allowedOutcomes[event.Outcome] || !allowedStates[event.State] || !allowedPauses[event.PauseCode] || !allowedActions[event.NextAction] || event.RetryAttempt > 1000 {
			t.Fatalf("non-allowlisted coordinator event=%#v", event)
		}
		for _, value := range []string{event.Outcome, event.State, event.PauseCode, event.NextAction} {
			if value == secret {
				t.Fatalf("observer exposed sentinel in event=%#v", event)
			}
		}
	}
	last := events[len(events)-1]
	if last.Outcome != OutcomePersistenceUnavailable || last.State != ObservedStateNone || last.PauseCode != ObservedPauseNone || last.NextAction != NextActionNone || last.RetryAttempt != 0 {
		t.Fatalf("unsafe values were not normalized event=%#v", last)
	}
}

func TestCoordinatorObservesJobPauseLifecycle(t *testing.T) {
	tests := []struct {
		name        string
		jobStatus   string
		phase       string
		errorCode   any
		disposition any
		finished    any
		wantPause   string
		wantAction  string
	}{
		{name: "approval", jobStatus: "waiting_user", phase: PauseApprovalRequired, disposition: PauseApprovalRequired, wantPause: PauseApprovalRequired, wantAction: NextActionApprovalRequired},
		{name: "missing configuration", jobStatus: "needs_attention", phase: "needs_attention", errorCode: "configuration_unavailable", finished: true, wantPause: PauseMissingConfig, wantAction: NextActionResumeRequired},
		{name: "deployment failed", jobStatus: "failed", phase: "failed", errorCode: "compose_apply_failed", finished: true, wantPause: PauseDeploymentFailed, wantAction: NextActionResumeRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
				t.Fatal(err)
			}
			clock := &coordinatorTestClock{now: fixture.now}
			recorder := &coordinatorEventRecorder{}
			config := DefaultCoordinatorConfig()
			config.Clock = clock
			config.MinResolveInterval = time.Nanosecond
			config.Observer = recorder.Observe
			coordinator, err := NewCoordinator(fixture.repository, &coordinatorTestResolver{sha: testSHA}, jobs.New(fixture.db), config)
			if err != nil {
				t.Fatal(err)
			}
			coordinator.drain(context.Background())
			active, err := fixture.repository.Get(context.Background(), testApp)
			if err != nil || active.ActiveJobID == "" {
				t.Fatalf("active status=%#v err=%v", active, err)
			}
			clock.Advance(activeJobPollInterval)
			var finishedAt any
			if test.finished != nil {
				finishedAt = timestamp(clock.Now())
			}
			if _, err = fixture.db.Exec(`UPDATE jobs SET status=?,phase=?,error_code=?,pause_disposition=?,updated_at=?,finished_at=? WHERE id=?`,
				test.jobStatus, test.phase, test.errorCode, test.disposition, timestamp(clock.Now()), finishedAt, active.ActiveJobID); err != nil {
				t.Fatal(err)
			}
			coordinator.drain(context.Background())
			event, ok := recorder.Last()
			if !ok || event.State != StatePaused || event.PauseCode != test.wantPause || event.NextAction != test.wantAction || event.RetryAttempt != 0 || event.Paused != 0 {
				t.Fatalf("pause lifecycle event=%#v", event)
			}
		})
	}
}

func TestCoordinatorObservesAccessRetryAndCooldownLifecycle(t *testing.T) {
	t.Run("source access lost", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
			t.Fatal(err)
		}
		clock := &coordinatorTestClock{now: fixture.now}
		recorder := &coordinatorEventRecorder{}
		config := DefaultCoordinatorConfig()
		config.Clock = clock
		config.Observer = recorder.Observe
		coordinator, err := NewCoordinator(fixture.repository, &coordinatorTestResolver{err: &SourceError{Code: OutcomeSourceAccessLost}}, jobs.New(fixture.db), config)
		if err != nil {
			t.Fatal(err)
		}
		coordinator.drain(context.Background())
		event, ok := recorder.Last()
		if !ok || event.State != StatePaused || event.PauseCode != PauseSourceAccessLost || event.NextAction != NextActionResumeRequired || event.Paused != 1 {
			t.Fatalf("source access lifecycle event=%#v", event)
		}
	})

	t.Run("retry wait", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
			t.Fatal(err)
		}
		clock := &coordinatorTestClock{now: fixture.now}
		recorder := &coordinatorEventRecorder{}
		config := DefaultCoordinatorConfig()
		config.Clock = clock
		config.Observer = recorder.Observe
		coordinator, err := NewCoordinator(fixture.repository, &coordinatorTestResolver{err: &SourceError{Code: OutcomeProviderUnavailable}}, jobs.New(fixture.db), config)
		if err != nil {
			t.Fatal(err)
		}
		coordinator.drain(context.Background())
		event, ok := recorder.Last()
		if !ok || event.State != StateRetryWait || event.PauseCode != ObservedPauseNone || event.RetryAttempt != 1 || event.NextAction != NextActionRetry || event.Retried != 1 {
			t.Fatalf("retry lifecycle event=%#v", event)
		}
	})

	t.Run("resolve cooldown", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
			t.Fatal(err)
		}
		clock := &coordinatorTestClock{now: fixture.now}
		recorder := &coordinatorEventRecorder{}
		config := DefaultCoordinatorConfig()
		config.Clock = clock
		config.MinResolveInterval = time.Minute
		config.Observer = recorder.Observe
		coordinator, err := NewCoordinator(fixture.repository, &coordinatorTestResolver{sha: testSHA}, jobs.New(fixture.db), config)
		if err != nil {
			t.Fatal(err)
		}
		coordinator.drain(context.Background())
		active, err := fixture.repository.Get(context.Background(), testApp)
		if err != nil {
			t.Fatal(err)
		}
		releaseID := insertActualRelease(t, fixture, testSHA, active.ActiveDispatchSequence)
		insertSucceededDeployment(t, fixture, testApp, releaseID, active.ActiveJobID)
		clock.Advance(activeJobPollInterval)
		if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',updated_at=?,finished_at=? WHERE id=?`, timestamp(clock.Now()), timestamp(clock.Now()), active.ActiveJobID); err != nil {
			t.Fatal(err)
		}
		coordinator.drain(context.Background())
		event, ok := recorder.Last()
		if !ok || event.State != StateIdle || event.PauseCode != ObservedPauseNone || event.NextAction != NextActionResolveCooldown || event.RetryAttempt != 0 {
			t.Fatalf("cooldown lifecycle event=%#v", event)
		}
	})
}

func TestCoordinatorReleaseFailureEmitsOneSanitizedObservation(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	const secret = "token=secret repository=private sha=aaaaaaaa raw=provider-body"
	repository := &failReleaseRepository{Repository: fixture.repository, err: errors.New(secret)}
	recorder := &coordinatorEventRecorder{}
	config := DefaultCoordinatorConfig()
	config.PollInterval = time.Hour
	config.MinResolveInterval = time.Nanosecond
	config.Clock = &coordinatorTestClock{now: fixture.now}
	config.Observer = recorder.Observe
	coordinator, err := NewCoordinator(repository, &coordinatorTestResolver{sha: testSHA}, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		coordinator.Wake()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for recorder.OutcomeCount(OutcomePersistenceUnavailable) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if recorder.OutcomeCount(OutcomePersistenceUnavailable) != 1 {
		cancel()
		t.Fatalf("release persistence observations=%d events=%#v", recorder.OutcomeCount(OutcomePersistenceUnavailable), recorder.Events())
	}
	for range 1000 {
		coordinator.Wake()
	}
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after release failure")
	}
	if repository.Calls() != 1 || recorder.OutcomeCount(OutcomePersistenceUnavailable) != 1 {
		t.Fatalf("release failure hot-looped calls=%d observations=%d events=%#v", repository.Calls(), recorder.OutcomeCount(OutcomePersistenceUnavailable), recorder.Events())
	}
	for _, event := range recorder.Events() {
		for _, value := range []string{event.Outcome, event.State, event.PauseCode, event.NextAction} {
			if value == secret {
				t.Fatalf("release failure exposed sentinel event=%#v", event)
			}
		}
	}
}

func TestNewCoordinatorRejectsRepositoryIncompatibleIntervals(t *testing.T) {
	fixture := newRepositoryFixture(t)
	resolver := &coordinatorTestResolver{sha: testSHA}
	for _, mutate := range []func(*CoordinatorConfig){
		func(config *CoordinatorConfig) { config.ReconcileInterval = 24*time.Hour + time.Nanosecond },
		func(config *CoordinatorConfig) { config.MinResolveInterval = 24*time.Hour + time.Nanosecond },
		func(config *CoordinatorConfig) { config.RetryMaximum = 24*time.Hour + time.Nanosecond },
		func(config *CoordinatorConfig) { config.RetryWindow = 24*time.Hour + time.Nanosecond },
	} {
		config := DefaultCoordinatorConfig()
		mutate(&config)
		if _, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid interval config=%#v err=%v", config, err)
		}
	}
}

func TestCoordinatorActiveProviderFailureRetriesThenPausesWithinWindow(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Nanosecond
	config.MaxRetryAttempts = 3
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.processOne(context.Background())
	clock.Advance(5 * time.Second)

	for attempt := 1; attempt <= 3; attempt++ {
		active, err := fixture.repository.Get(context.Background(), testApp)
		if err != nil || active.ActiveJobID == "" {
			t.Fatalf("attempt %d active=%#v err=%v", attempt, active, err)
		}
		if _, err = fixture.db.Exec(`UPDATE jobs SET status='failed',phase='failed',error_code='provider_unavailable',updated_at=?,finished_at=? WHERE id=?`, timestamp(clock.Now()), timestamp(clock.Now()), active.ActiveJobID); err != nil {
			t.Fatal(err)
		}
		claimed, event := coordinator.processOne(context.Background())
		if !claimed {
			t.Fatalf("attempt %d was not claimed", attempt)
		}
		if attempt < 3 {
			retrying, retryErr := fixture.repository.Get(context.Background(), testApp)
			if retryErr != nil || retrying.State != StateRetryWait || event.State != StateRetryWait || event.NextAction != NextActionRetry || event.Retried != 1 || event.RetryAttempt != retrying.RetryAttempt {
				t.Fatalf("attempt %d retry event=%#v durable=%#v err=%v", attempt, event, retrying, retryErr)
			}
			clock.Advance(coordinator.retryDelay(uint32(attempt)))
			if _, dispatchEvent := coordinator.processOne(context.Background()); dispatchEvent.Dispatched != 1 {
				t.Fatalf("attempt %d provider retry dispatch=%#v", attempt, dispatchEvent)
			}
			clock.Advance(5 * time.Second)
		} else if event.Paused != 1 || event.Outcome != OutcomeProviderUnavailable {
			t.Fatalf("exhausted active provider event=%#v", event)
		}
	}
	paused, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || paused.State != StatePaused || paused.PauseCode != PauseProviderUnavailable || paused.ActiveJobID != "" {
		t.Fatalf("provider paused=%#v err=%v", paused, err)
	}
}

func TestCoordinatorApplicationBusyRetriesOnceThenPauses(t *testing.T) {
	fixture := newRepositoryFixture(t)
	if _, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now); err != nil {
		t.Fatal(err)
	}
	clock := &coordinatorTestClock{now: fixture.now}
	resolver := &coordinatorTestResolver{sha: testSHA}
	jobCreator := &busyJobCreator{}
	coordinator := newCoordinatorForTest(t, fixture.repository, resolver, jobCreator, clock)
	if _, event := coordinator.processOne(context.Background()); event.Retried != 1 || event.Outcome != OutcomeApplicationBusy {
		t.Fatalf("busy retry event=%#v", event)
	}
	clock.Advance(15 * time.Second)
	if _, event := coordinator.processOne(context.Background()); event.Paused != 1 || event.Outcome != OutcomeApplicationBusy {
		t.Fatalf("busy exhausted event=%#v", event)
	}
	paused, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || paused.State != StatePaused || paused.PauseCode != PauseDeploymentFailed || jobCreator.calls != 2 {
		t.Fatalf("busy paused=%#v calls=%d err=%v", paused, jobCreator.calls, err)
	}
}

func TestCoordinatorWakeCoalescesAndRunCancels(t *testing.T) {
	fixture := newRepositoryFixture(t)
	resolver := &coordinatorTestResolver{sha: testSHA}
	config := DefaultCoordinatorConfig()
	config.PollInterval = time.Hour
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		coordinator.Wake()
	}
	if len(coordinator.wake) != 1 {
		t.Fatalf("coalesced wake depth=%d", len(coordinator.wake))
	}
	if coordinator.takeReconcile() {
		t.Fatal("ordinary ACK wake requested global reconciliation")
	}
	coordinator.Reconcile()
	if !coordinator.takeReconcile() || len(coordinator.wake) != 1 {
		t.Fatalf("ready reconcile was lost depth=%d", len(coordinator.wake))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("cancel run=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after cancellation")
	}
}

func TestCoordinatorRunPausesOnceWhenSourceDisconnectsBeforeReserve(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now)

	clock := &coordinatorTestClock{now: fixture.now}
	release := make(chan struct{})
	repository := &reserveBarrierRepository{Repository: fixture.repository, reached: make(chan struct{}), release: release}
	resolver := &coordinatorTestResolver{sha: secondSHA}
	recorder := &coordinatorEventRecorder{}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.PollInterval = time.Hour
	config.MinResolveInterval = time.Nanosecond
	config.BatchSize = 32
	config.Observer = recorder.Observe
	coordinator, err := NewCoordinator(repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		coordinator.Wake()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	select {
	case <-repository.reached:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not reach resolve reservation")
	}
	setSourceConnectionStatus(t, fixture, "disconnected", fixture.now.Add(time.Second))
	close(release)
	paused := waitCoordinatorStatus(t, fixture.repository, func(value Status) bool {
		return value.State == StatePaused && value.PauseCode == PauseSourceAccessLost && value.LeaseExpiresAt == nil
	})
	if resolver.Calls() != 0 || paused.LastConsumedGeneration != 0 || paused.NextRetryAt != nil || paused.NextReconcileAt != nil {
		t.Fatalf("disconnect-before-reserve status=%#v resolver_calls=%d", paused, resolver.Calls())
	}
	assertNoResolveReservation(t, fixture.db, testApp)

	setSourceConnectionStatus(t, fixture, "connected", fixture.now.Add(2*time.Second))
	for index := 0; index < 1000; index++ {
		coordinator.Wake()
		coordinator.Reconcile()
	}
	waitCoordinatorWakeDrained(t, coordinator)
	afterStorm, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || afterStorm.State != StatePaused || afterStorm.PauseCode != PauseSourceAccessLost || afterStorm.LeaseFence != paused.LeaseFence || resolver.Calls() != 0 || recorder.Paused() != 1 {
		t.Fatalf("wake/reconnect storm changed durable pause status=%#v calls=%d paused_events=%d err=%v", afterStorm, resolver.Calls(), recorder.Paused(), err)
	}
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop")
	}
}

func TestCoordinatorRunPausesWhenSourceDisconnectsDuringResolve(t *testing.T) {
	fixture := newRepositoryFixture(t)
	status, err := fixture.repository.Configure(context.Background(), ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, Enabled: true}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	commitDesired(t, fixture, status, 1, testSHA, fixture.now)

	clock := &coordinatorTestClock{now: fixture.now}
	release := make(chan struct{})
	resolver := &barrierCoordinatorResolver{shas: []string{secondSHA}, started: make(chan int, 1), firstRelease: release}
	recorder := &coordinatorEventRecorder{}
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.PollInterval = time.Hour
	config.MinResolveInterval = time.Nanosecond
	config.BatchSize = 32
	config.Observer = recorder.Observe
	coordinator, err := NewCoordinator(fixture.repository, resolver, jobs.New(fixture.db), config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	select {
	case call := <-resolver.started:
		if call != 1 {
			t.Fatalf("resolver call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("provider resolve did not start")
	}
	reservedGeneration, reservedFence := loadResolveReservation(t, fixture.db, testApp)
	if !reservedGeneration.Valid || reservedGeneration.Int64 != 1 || !reservedFence.Valid {
		t.Fatalf("missing live reservation generation=%#v fence=%#v", reservedGeneration, reservedFence)
	}
	setSourceConnectionStatus(t, fixture, "disconnected", fixture.now.Add(time.Second))
	close(release)
	paused := waitCoordinatorStatus(t, fixture.repository, func(value Status) bool {
		return value.State == StatePaused && value.PauseCode == PauseSourceAccessLost && value.LeaseExpiresAt == nil
	})
	if resolver.Calls() != 1 || paused.LastConsumedGeneration != 0 || paused.LatestResolvedSHA != "" || paused.LeaseFence != uint64(reservedFence.Int64)+1 || paused.NextRetryAt != nil || paused.NextReconcileAt != nil {
		t.Fatalf("disconnect-during-resolve status=%#v resolver_calls=%d", paused, resolver.Calls())
	}
	assertNoResolveReservation(t, fixture.db, testApp)

	for index := 0; index < 1000; index++ {
		coordinator.Wake()
		coordinator.Reconcile()
	}
	waitCoordinatorWakeDrained(t, coordinator)
	afterStorm, err := fixture.repository.Get(context.Background(), testApp)
	if err != nil || afterStorm.LeaseFence != paused.LeaseFence || resolver.Calls() != 1 || recorder.Paused() != 1 {
		t.Fatalf("disconnect wake storm changed pause status=%#v calls=%d paused_events=%d err=%v", afterStorm, resolver.Calls(), recorder.Paused(), err)
	}
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop")
	}
}

func newCoordinatorForTest(t *testing.T, repository CoordinatorRepository, resolver SourceResolver, jobService JobCreator, clock Clock) *Coordinator {
	t.Helper()
	config := DefaultCoordinatorConfig()
	config.Clock = clock
	config.MinResolveInterval = time.Nanosecond
	coordinator, err := NewCoordinator(repository, resolver, jobService, config)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func waitCoordinatorStatus(t *testing.T, repository *Repository, ready func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		value, err := repository.Get(context.Background(), testApp)
		if err != nil {
			t.Fatal(err)
		}
		if ready(value) {
			return value
		}
		if time.Now().After(deadline) {
			t.Fatalf("coordinator state did not converge: %#v", value)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitCoordinatorWakeDrained(t *testing.T, coordinator *Coordinator) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(coordinator.wake) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("coordinator wake did not drain depth=%d", len(coordinator.wake))
		}
		time.Sleep(time.Millisecond)
	}
	// Let the drain triggered by the consumed signal finish before checking
	// durable state and aggregate events.
	time.Sleep(10 * time.Millisecond)
}

func loadResolveReservation(t *testing.T, db *sql.DB, applicationID string) (sql.NullInt64, sql.NullInt64) {
	t.Helper()
	var generation, fence sql.NullInt64
	if err := db.QueryRow(`SELECT resolving_generation,resolving_lease_fence FROM github_auto_deploy_heads WHERE application_id=?`, applicationID).Scan(&generation, &fence); err != nil {
		t.Fatal(err)
	}
	return generation, fence
}

func assertNoResolveReservation(t *testing.T, db *sql.DB, applicationID string) {
	t.Helper()
	generation, fence := loadResolveReservation(t, db, applicationID)
	if generation.Valid || fence.Valid {
		t.Fatalf("resolution reservation retained generation=%#v fence=%#v", generation, fence)
	}
}

func assertSingleCoordinatorJob(t *testing.T, fixture *repositoryFixture, status Status, expected int) {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE resource_id=?`, testApp).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("job count=%d want=%d", count, expected)
	}
	if expected == 0 {
		return
	}
	var requestedBy, input string
	if err := fixture.db.QueryRow(`SELECT requested_by,input_json FROM jobs WHERE resource_id=? ORDER BY created_at DESC LIMIT 1`, testApp).Scan(&requestedBy, &input); err != nil {
		t.Fatal(err)
	}
	if requestedBy != status.SourceOwnerUserID || input != `{"releaseId":"","configurationMode":"current"}` {
		t.Fatalf("unsafe coordinator job actor=%q input=%q status=%#v", requestedBy, input, status)
	}
}
