package controllerrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

type supervisorStoreFake struct {
	mu             sync.Mutex
	status         SessionStatus
	beginErr       error
	advanceErr     error
	advanceCalls   int
	diagnostics    SessionLifecycleDiagnostics
	diagnosticsErr error
}

func (store *supervisorStoreFake) BeginSessionEpoch(_ context.Context, at time.Time) (SessionStatus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.beginErr != nil {
		return SessionStatus{}, store.beginErr
	}
	if store.status.ControllerID == "" {
		store.status = SessionStatus{ControllerID: sessionTestControllerID, Epoch: 1, Fence: 1, State: SessionDisconnected, StateChangedAt: at, UpdatedAt: at}
		return store.status, nil
	}
	store.status.Epoch++
	store.status.Fence = 1
	store.status.State = SessionDisconnected
	store.status.KeyID = ""
	store.status.ErrorCode = ""
	store.status.Attempt = 0
	store.status.NextAttemptAt = nil
	store.status.StateChangedAt = at
	store.status.UpdatedAt = at
	return store.status, nil
}

func (store *supervisorStoreFake) AdvanceSessionStatus(_ context.Context, epoch, fence uint64, next SessionStatus) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.advanceCalls++
	if store.advanceErr != nil {
		return store.advanceErr
	}
	if store.status.Epoch != epoch || store.status.Fence != fence {
		return ErrState
	}
	store.status = next
	return nil
}

func (store *supervisorStoreFake) SessionLifecycleDiagnostics(context.Context, string, time.Time) (SessionLifecycleDiagnostics, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.diagnostics, store.diagnosticsErr
}

func (store *supervisorStoreFake) snapshot() (SessionStatus, int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.status, store.advanceCalls
}

type supervisorRecoveryFake struct {
	pages []ControllerKeyRecoveryPage
	calls int
	err   error
	mu    sync.Mutex
}

type supervisorRecoveryFunc func(context.Context, ControllerKeyRecoveryCursor, int) (ControllerKeyRecoveryPage, error)

func (fn supervisorRecoveryFunc) RecoverControllerKeysPage(ctx context.Context, cursor ControllerKeyRecoveryCursor, limit int) (ControllerKeyRecoveryPage, error) {
	return fn(ctx, cursor, limit)
}

type supervisorCompleterFake struct{}

func (supervisorCompleterFake) CompleteRotationAfterFencedReady(context.Context, string, string, uint64, uint64) error {
	return nil
}

type supervisorCompleterErrorFake struct{ err error }

func (fake supervisorCompleterErrorFake) CompleteRotationAfterFencedReady(context.Context, string, string, uint64, uint64) error {
	return fake.err
}

type supervisorCompleterCapture struct {
	mu    sync.Mutex
	calls []sessionReadyOwner
	err   error
}

// legacyOnlySessionControlRepository intentionally exposes the historical
// control repository surface while hiding the fenced extension. It verifies
// that supervised production construction cannot silently downgrade fencing.
type legacyOnlySessionControlRepository struct{ sessionControlRepository }

// supervisorHandshakeStore adds the production fenced inbound mutation surface
// to the existing handshake fixture without changing direct-transport tests.
type supervisorHandshakeStore struct{ *fakeSessionTransportStore }

func (store *supervisorHandshakeStore) CommitSourceDesiredFenced(ctx context.Context, controllerID string, _ uint64, _ uint64, source protocol.SourceDesired, receivedAt time.Time) (InboxDecision, error) {
	return store.CommitSourceDesired(ctx, controllerID, source, receivedAt)
}

func (store *supervisorHandshakeStore) CommitAccessChangeFenced(ctx context.Context, controllerID string, _ uint64, _ uint64, change protocol.AccessChange, receivedAt time.Time) (InboxDecision, error) {
	return store.CommitAccessChange(ctx, controllerID, change, receivedAt)
}

func (fake *supervisorCompleterCapture) CompleteRotationAfterFencedReady(_ context.Context, _ string, _ string, epoch, fence uint64) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, sessionReadyOwner{Epoch: epoch, Fence: fence})
	return fake.err
}

func testLifecycleEvent(stage, keyID string, fallback bool) sessionTransportLifecycleEvent {
	return sessionTransportLifecycleEvent{SessionTransportEvent: SessionTransportEvent{Stage: stage, Fallback: fallback}, ControllerID: sessionTestControllerID, KeyID: keyID}
}

func observeTestLifecycle(ctx context.Context, transport *SessionTransport, event sessionTransportLifecycleEvent) error {
	return transport.observeLifecycle(ctx, &event)
}

func (recovery *supervisorRecoveryFake) RecoverControllerKeysPage(context.Context, ControllerKeyRecoveryCursor, int) (ControllerKeyRecoveryPage, error) {
	recovery.mu.Lock()
	defer recovery.mu.Unlock()
	recovery.calls++
	index := recovery.calls - 1
	if index >= len(recovery.pages) {
		return ControllerKeyRecoveryPage{Complete: true}, recovery.err
	}
	page := recovery.pages[index]
	return page, recovery.err
}

func (recovery *supervisorRecoveryFake) callCount() int {
	recovery.mu.Lock()
	defer recovery.mu.Unlock()
	return recovery.calls
}

type supervisorRunnerFake struct {
	mu     sync.Mutex
	calls  int
	run    func(context.Context, int) error
	dialed chan struct{}
}

func (runner *supervisorRunnerFake) RunOnce(ctx context.Context) error {
	runner.mu.Lock()
	runner.calls++
	call := runner.calls
	runner.mu.Unlock()
	if runner.dialed != nil {
		select {
		case runner.dialed <- struct{}{}:
		default:
		}
	}
	return runner.run(ctx, call)
}

func (runner *supervisorRunnerFake) callCount() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.calls
}

func waitSupervisorState(t *testing.T, supervisor *Supervisor, paused bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if supervisor.Snapshot().Paused == paused {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("supervisor pause=%t snapshot=%#v", paused, supervisor.Snapshot())
		case <-time.After(time.Millisecond):
		}
	}
}

func waitRunnerCalls(t *testing.T, runner *supervisorRunnerFake, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if runner.callCount() >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("runner calls=%d want at least %d", runner.callCount(), want)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestSupervisorFencesTransportStagesAndFallbackFromOwnedStatus(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{}
	recovery := &supervisorRecoveryFake{pages: []ControllerKeyRecoveryPage{{NextCursor: ControllerKeyRecoveryCursor{LeasesComplete: true}, Scanned: 2}, {Complete: true, Cleaned: 1}}}
	transport := &SessionTransport{}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.Jitter = func(delay time.Duration, _ uint32) time.Duration { return delay }
	supervisor, err := NewSupervisor(transport, store, recovery, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = supervisor.recover(context.Background()); err != nil || recovery.calls != 2 {
		t.Fatalf("recovery=%v calls=%d", err, recovery.calls)
	}
	for _, event := range []sessionTransportLifecycleEvent{
		testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false),
		testLifecycleEvent(SessionTransportAuthenticating, sessionTestKeyID, false),
		{SessionTransportEvent: SessionTransportEvent{Stage: SessionTransportReady, Reconnect: SessionTransportReconnect{InitialDelay: time.Second, MaximumDelay: 2 * time.Second, Multiplier: 2}}, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID},
	} {
		if err = observeTestLifecycle(context.Background(), transport, event); err != nil {
			t.Fatalf("observe %s: %v", event.Stage, err)
		}
	}
	if store.status.State != SessionReady || store.status.Epoch != 2 || store.status.Fence != 3 || store.status.Attempt != 0 {
		t.Fatalf("ready status=%#v", store.status)
	}
	if err = observeTestLifecycle(context.Background(), transport, testLifecycleEvent(SessionTransportConnecting, sessionTestChallengeID, true)); err != nil {
		t.Fatal(err)
	}
	if store.status.State != SessionConnecting || store.status.Epoch != 3 || store.status.Fence != 1 || store.status.Attempt != 1 {
		t.Fatalf("fallback status=%#v", store.status)
	}
	store.status.Fence++ // Simulate a newer lifecycle owner winning the CAS.
	if err = observeTestLifecycle(context.Background(), transport, testLifecycleEvent(SessionTransportAuthenticating, sessionTestChallengeID, true)); !sessionErrorCode(err, sessionErrorPersistence) {
		t.Fatalf("stale lifecycle observer error=%v", err)
	}
}

func TestSupervisorLifecycleObserverIsInternalFirstAndSafe(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{}
	transport := &SessionTransport{observer: func(_ context.Context, _ SessionTransportEvent) error {
		if store.status.State != SessionConnecting {
			return errors.New("lifecycle did not run first")
		}
		return nil
	}}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	supervisor, err := NewSupervisor(transport, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = supervisor.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = observeTestLifecycle(context.Background(), transport, testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false)); err != nil {
		t.Fatalf("observer ordering=%v", err)
	}
	transport.lifecycleMu.Lock()
	transport.lifecycle = func(context.Context, *sessionTransportLifecycleEvent) error { return nil }
	transport.lifecycleMu.Unlock()
	if _, err = NewSupervisor(transport, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config); err == nil {
		t.Fatal("second lifecycle owner was accepted")
	}
	if _, found := reflect.TypeOf(SupervisorEvent{}).FieldByName("ControllerID"); found {
		t.Fatal("supervisor event exposes controller identity")
	}
}

func TestSupervisorRejectsSessionControlServiceWithoutFencedRepository(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	service, err := NewSessionControlService(legacyOnlySessionControlRepository{sessionControlRepository: repository}, newMemoryControlCredentials(), DefaultSessionControlConfig())
	if err != nil {
		t.Fatal(err)
	}
	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now }
	transport.config.ControlHandler = service
	if _, err = NewSupervisor(transport, repository, &supervisorRecoveryFake{}, supervisorCompleterFake{}, DefaultSupervisorConfig()); err == nil {
		t.Fatal("supervisor accepted unfenced SessionControlService")
	}
}

func TestSupervisorRunsRealTransportHandshakeWithOwnedReady(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	transport, socket, _ := newSessionHandshakeFixture(t, now, nil, nil)
	fixture, ok := transport.store.(*fakeSessionTransportStore)
	if !ok {
		t.Fatalf("handshake store=%T", transport.store)
	}
	transport.store = &supervisorHandshakeStore{fakeSessionTransportStore: fixture}
	store := &supervisorStoreFake{}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	supervisor, err := NewSupervisor(transport, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = transport.RunOnce(context.Background()); !sessionErrorCode(err, sessionErrorIdentity) {
		t.Fatalf("direct RunOnce bypassed supervisor claim: %v", err)
	}
	if err = supervisor.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = supervisor.runner.RunOnce(context.Background()); !sessionErrorCode(err, sessionErrorConnectionClosed) {
		t.Fatalf("supervised handshake run=%v", err)
	}
	status, _ := store.snapshot()
	if status.State != SessionReady || status.Epoch != 2 || status.Fence != 3 || status.KeyID != sessionTestKeyID {
		t.Fatalf("real handshake did not retain owned ready status: %#v", status)
	}
	if snapshot := supervisor.Snapshot(); snapshot.State != SessionReady || snapshot.Epoch != 2 || snapshot.Fence != 3 {
		t.Fatalf("real handshake snapshot=%#v", snapshot)
	}
	if !socket.closed {
		t.Fatal("real handshake session was not closed after disconnect")
	}
}

func TestSupervisorBackoffClampsJitterAndMapsSafeOutcomes(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{status: SessionStatus{ControllerID: sessionTestControllerID, Epoch: 2, Fence: 3, State: SessionReady, KeyID: sessionTestKeyID, StateChangedAt: now, UpdatedAt: now}}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.Jitter = func(time.Duration, uint32) time.Duration { return -time.Second }
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.setStatus(store.status, "")
	if got, err := supervisor.persistBackoff(context.Background(), sessionErrorQueueSaturated); err != nil || got != config.InitialBackoff {
		t.Fatalf("jittered delay=%s err=%v want %s", got, err, config.InitialBackoff)
	}
	if store.status.State != SessionBackoff || store.status.ErrorCode != ErrorRelayUnavailable {
		t.Fatalf("backoff status=%#v", store.status)
	}
}

func TestSupervisorRunRecoversBeforeDialRetriesWithoutRecoveryAndResumesWithRecovery(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{}
	recovery := &supervisorRecoveryFake{}
	transport := &SessionTransport{}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.Jitter = func(delay time.Duration, _ uint32) time.Duration { return delay }
	sleeps := make(chan time.Duration, 2)
	config.Sleep = func(ctx context.Context, delay time.Duration) error {
		sleeps <- delay
		return nil
	}
	supervisor, err := NewSupervisor(transport, store, recovery, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	runner := &supervisorRunnerFake{run: func(ctx context.Context, call int) error {
		switch call {
		case 1:
			for _, event := range []sessionTransportLifecycleEvent{
				testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false),
				testLifecycleEvent(SessionTransportAuthenticating, sessionTestKeyID, false),
				{SessionTransportEvent: SessionTransportEvent{Stage: SessionTransportReady, Reconnect: SessionTransportReconnect{InitialDelay: time.Second, MaximumDelay: 2 * time.Second, Multiplier: 2}}, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID},
			} {
				if err := observeTestLifecycle(ctx, transport, event); err != nil {
					return err
				}
			}
			return sessionFailure(sessionErrorRelayUnavailable, false)
		case 2:
			return sessionFailure(sessionErrorProtocol, true)
		default:
			<-ctx.Done()
			return ctx.Err()
		}
	}}
	supervisor.runner = runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	waitRunnerCalls(t, runner, 2)
	if recovery.callCount() != 1 {
		t.Fatalf("automatic retry reran recovery calls=%d", recovery.callCount())
	}
	if got := <-sleeps; got != time.Second {
		t.Fatalf("ready reconnect delay=%s want 1s", got)
	}
	waitSupervisorState(t, supervisor, true)
	if err = supervisor.Resume(); err != nil {
		t.Fatal(err)
	}
	waitRunnerCalls(t, runner, 3)
	if recovery.callCount() != 2 {
		t.Fatalf("explicit resume recovery calls=%d want 2", recovery.callCount())
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatalf("run=%v", err)
	}
	if store.status.State != SessionStopped {
		t.Fatalf("canceled run did not persist stopped: %#v", store.status)
	}
}

func TestSupervisorRunRecoveryFailuresNeverDial(t *testing.T) {
	for _, test := range []struct {
		name     string
		store    *supervisorStoreFake
		recovery *supervisorRecoveryFake
		config   func(*SupervisorConfig)
	}{
		{name: "recovery error", store: &supervisorStoreFake{}, recovery: &supervisorRecoveryFake{err: controlFailure(controlErrorCredential)}},
		{name: "missing identity", store: &supervisorStoreFake{beginErr: ErrNotFound}, recovery: &supervisorRecoveryFake{}},
		{name: "cursor stall", store: &supervisorStoreFake{}, recovery: &supervisorRecoveryFake{pages: []ControllerKeyRecoveryPage{{}}}},
		{name: "cursor cycle", store: &supervisorStoreFake{}, recovery: &supervisorRecoveryFake{pages: []ControllerKeyRecoveryPage{{NextCursor: ControllerKeyRecoveryCursor{LeaseCursor: "a"}}, {NextCursor: ControllerKeyRecoveryCursor{}}}}},
		{name: "page limit", store: &supervisorStoreFake{}, recovery: &supervisorRecoveryFake{pages: []ControllerKeyRecoveryPage{{NextCursor: ControllerKeyRecoveryCursor{LeaseCursor: "a"}}, {NextCursor: ControllerKeyRecoveryCursor{LeaseCursor: "b"}}}}, config: func(config *SupervisorConfig) { config.MaxRecoveryPages = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultSupervisorConfig()
			config.Now = func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) }
			if test.config != nil {
				test.config(&config)
			}
			supervisor, err := NewSupervisor(&SessionTransport{}, test.store, test.recovery, supervisorCompleterFake{}, config)
			if err != nil {
				t.Fatal(err)
			}
			runner := &supervisorRunnerFake{run: func(context.Context, int) error { return nil }}
			supervisor.runner = runner
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- supervisor.Run(ctx) }()
			waitSupervisorState(t, supervisor, true)
			if runner.callCount() != 0 {
				t.Fatalf("recovery failure dialed %d times", runner.callCount())
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSupervisorShutdownClearsStaleResumeToken(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{status: SessionStatus{ControllerID: sessionTestControllerID, Epoch: 1, Fence: 1, State: SessionDisconnected, StateChangedAt: now, UpdatedAt: now}}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.ShutdownTimeout = time.Second
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.setStatus(store.status, "")
	supervisor.mu.Lock()
	supervisor.running = true
	supervisor.runGeneration = 1
	supervisor.mu.Unlock()
	supervisor.pause(context.Background(), sessionErrorProtocol)
	if err = supervisor.Resume(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	supervisor.stop(ctx)
	supervisor.mu.RLock()
	queued, pending := supervisor.resumeQueued, len(supervisor.resumeCh)
	supervisor.mu.RUnlock()
	if supervisor.Snapshot().Paused || queued || pending != 0 {
		t.Fatalf("shutdown retained resume state snapshot=%#v queued=%t pending=%d", supervisor.Snapshot(), queued, pending)
	}
	supervisor.pause(context.Background(), sessionErrorProtocol)
	if !supervisor.Snapshot().Paused {
		t.Fatalf("later fatal pause was consumed by stale resume state: %#v", supervisor.Snapshot())
	}
}

func TestSupervisorLifecycleOwnershipIsAtomic(t *testing.T) {
	const workers = 32
	transport := &SessionTransport{}
	config := DefaultSupervisorConfig()
	var wait sync.WaitGroup
	results := make(chan bool, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := NewSupervisor(transport, &supervisorStoreFake{}, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
			results <- err == nil
		}()
	}
	wait.Wait()
	close(results)
	var successes int
	for result := range results {
		if result {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("lifecycle owners=%d want 1", successes)
	}
}

func TestClassifySessionTransportErrorIsTypedAndSecretSafe(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		code  string
		fatal bool
		ok    bool
	}{
		{name: "retryable queue", err: sessionFailure(sessionErrorQueueSaturated, false), code: sessionErrorQueueSaturated, fatal: false, ok: true},
		{name: "fatal credential", err: sessionFailure(sessionErrorCredential, true), code: sessionErrorCredential, fatal: true, ok: true},
		{name: "unknown safe fallback", err: &SessionTransportError{Code: "provider-body-secret", Fatal: true}, code: sessionErrorProtocol, fatal: true, ok: true},
		{name: "untyped", err: errors.New("provider-body-secret")},
		{name: "typed nil", err: func() error { var value *SessionTransportError; return value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			info, ok := ClassifySessionTransportError(test.err)
			if ok != test.ok || info.Code != test.code || info.Fatal != test.fatal {
				t.Fatalf("classify=%#v,%t want code=%q fatal=%t ok=%t", info, ok, test.code, test.fatal, test.ok)
			}
		})
	}
}

func TestPublicLifecycleSurfacesNeverExposeIdentifiersOrRawSentinels(t *testing.T) {
	const sentinel = "controller-id=11111111-1111-4111-8111-111111111111 provider-body=secret frame=nonce"
	for _, value := range []any{SessionTransportEvent{}, SupervisorEvent{}, SupervisorSnapshot{}} {
		typeOf := reflect.TypeOf(value)
		for _, name := range []string{"ControllerID", "KeyID", "SessionID", "Nonce", "Signature", "Frame", "Body"} {
			if _, found := typeOf.FieldByName(name); found {
				t.Fatalf("%s exposes %s", typeOf.Name(), name)
			}
		}
	}
	values := []any{
		SessionTransportEvent{Stage: sentinel},
		SupervisorEvent{Kind: sentinel, Stage: sentinel, Outcome: sentinel},
		SupervisorSnapshot{State: sentinel, Outcome: sentinel},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), sentinel) {
			t.Fatalf("json leaked sentinel value=%T encoded=%q err=%v", value, encoded, err)
		}
		var output bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&output, nil))
		logger.Info("safe lifecycle", "value", value)
		if strings.Contains(output.String(), sentinel) {
			t.Fatalf("slog leaked sentinel value=%T output=%q", value, output.String())
		}
	}
}

func TestSupervisorBlockingObserverCannotDelayShutdown(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.Observer = func(context.Context, SupervisorEvent) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil
	}
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.runner = &supervisorRunnerFake{run: func(context.Context, int) error { return sessionFailure(sessionErrorProtocol, true) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("observer was not scheduled")
	}
	waitSupervisorState(t, supervisor, true)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking observer delayed shutdown")
	}
	if store.status.State != SessionStopped {
		t.Fatalf("shutdown state=%#v", store.status)
	}
	if supervisor.Snapshot().ObserverDropped == 0 {
		t.Fatal("coalesced observer events were not counted")
	}
	close(release)
}

func TestSupervisorRunGenerationRejectsStaleResumeAcrossCanceledRun(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.ResumeInterval = 0
	store := &supervisorStoreFake{}
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	runner := &supervisorRunnerFake{run: func(context.Context, int) error { return sessionFailure(sessionErrorProtocol, true) }}
	supervisor.runner = runner
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- supervisor.Run(firstCtx) }()
	waitSupervisorState(t, supervisor, true)
	resumeDone := make(chan error, 1)
	go func() { resumeDone <- supervisor.Resume() }()
	firstCancel()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	<-resumeDone // Either accepted before cancellation or rejected after it; neither may survive.

	before := runner.callCount()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- supervisor.Run(secondCtx) }()
	waitRunnerCalls(t, runner, before+1)
	waitSupervisorState(t, supervisor, true)
	select {
	case <-time.After(25 * time.Millisecond):
		if runner.callCount() != before+1 {
			t.Fatalf("stale resume advanced second run calls=%d want=%d", runner.callCount(), before+1)
		}
	case <-secondCtx.Done():
		t.Fatal("unexpected second context cancellation")
	}
	secondCancel()
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRunRejectsConcurrentOwner(t *testing.T) {
	config := DefaultSupervisorConfig()
	store := &supervisorStoreFake{}
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	runner := &supervisorRunnerFake{run: func(ctx context.Context, _ int) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	supervisor.runner = runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	waitRunnerCalls(t, runner, 1)
	if err := supervisor.Run(context.Background()); !sessionErrorCode(err, sessionErrorIdentity) {
		t.Fatalf("concurrent Run error=%v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorCancellationDuringRecoveryDialAndBackoffPersistsStopped(t *testing.T) {
	for _, test := range []struct {
		name     string
		recovery controllerKeyRecovery
		runner   func(*Supervisor, *supervisorRunnerFake)
	}{
		{
			name: "recovery",
			recovery: supervisorRecoveryFunc(func(ctx context.Context, _ ControllerKeyRecoveryCursor, _ int) (ControllerKeyRecoveryPage, error) {
				<-ctx.Done()
				return ControllerKeyRecoveryPage{}, ctx.Err()
			}),
			runner: func(*Supervisor, *supervisorRunnerFake) {},
		},
		{
			name: "dial",
			recovery: supervisorRecoveryFunc(func(context.Context, ControllerKeyRecoveryCursor, int) (ControllerKeyRecoveryPage, error) {
				return ControllerKeyRecoveryPage{Complete: true}, nil
			}),
			runner: func(_ *Supervisor, runner *supervisorRunnerFake) {
				runner.run = func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }
			},
		},
		{
			name: "backoff",
			recovery: supervisorRecoveryFunc(func(context.Context, ControllerKeyRecoveryCursor, int) (ControllerKeyRecoveryPage, error) {
				return ControllerKeyRecoveryPage{Complete: true}, nil
			}),
			runner: func(supervisor *Supervisor, runner *supervisorRunnerFake) {
				runner.run = func(context.Context, int) error { return sessionFailure(sessionErrorRelayUnavailable, false) }
				supervisor.config.Sleep = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultSupervisorConfig()
			config.Now = func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) }
			store := &supervisorStoreFake{}
			supervisor, err := NewSupervisor(&SessionTransport{}, store, test.recovery, supervisorCompleterFake{}, config)
			if err != nil {
				t.Fatal(err)
			}
			runner := &supervisorRunnerFake{run: func(context.Context, int) error { return nil }}
			test.runner(supervisor, runner)
			supervisor.runner = runner
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- supervisor.Run(ctx) }()
			if test.name == "recovery" {
				waitSupervisorState(t, supervisor, false)
			} else {
				waitRunnerCalls(t, runner, 1)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if store.status.ControllerID != "" && store.status.State != SessionStopped {
				t.Fatalf("cancellation did not persist stopped: %#v", store.status)
			}
		})
	}
}

func TestSupervisorDiagnosticsNeverRetainsStaleValues(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{status: SessionStatus{ControllerID: sessionTestControllerID, Epoch: 1, Fence: 1, State: SessionReady}, diagnostics: SessionLifecycleDiagnostics{PendingCommands: 3, OldestPendingAge: time.Minute, ActiveLeases: 2, ExpiredLeases: 1}}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := supervisor.Snapshot(); !snapshot.DiagnosticsUnavailable || snapshot.Diagnostics != (SessionLifecycleDiagnostics{}) {
		t.Fatalf("new diagnostics snapshot=%#v", snapshot)
	}
	supervisor.setStatus(store.status, "")
	supervisor.sampleDiagnostics(context.Background())
	if snapshot := supervisor.Snapshot(); snapshot.DiagnosticsUnavailable || snapshot.Diagnostics != store.diagnostics {
		t.Fatalf("successful diagnostics snapshot=%#v", snapshot)
	}
	store.mu.Lock()
	store.diagnosticsErr = errors.New("database diagnostics failure")
	store.mu.Unlock()
	supervisor.sampleDiagnostics(context.Background())
	if snapshot := supervisor.Snapshot(); !snapshot.DiagnosticsUnavailable || snapshot.Diagnostics != (SessionLifecycleDiagnostics{}) {
		t.Fatalf("failed diagnostics retained data snapshot=%#v", snapshot)
	}
	supervisor.mu.Lock()
	supervisor.snapshot.Diagnostics = SessionLifecycleDiagnostics{PendingCommands: 9}
	supervisor.snapshot.DiagnosticsUnavailable = false
	supervisor.mu.Unlock()
	store.beginErr = controlFailure(controlErrorCredential)
	if err = supervisor.recover(context.Background()); err == nil {
		t.Fatal("recovery failure succeeded")
	}
	if snapshot := supervisor.Snapshot(); !snapshot.DiagnosticsUnavailable || snapshot.Diagnostics != (SessionLifecycleDiagnostics{}) {
		t.Fatalf("recovery start retained diagnostics snapshot=%#v", snapshot)
	}
}

func TestSupervisorBeginEpochPersistenceFailureClearsOwnedState(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	owned := SessionStatus{ControllerID: sessionTestControllerID, Epoch: 3, Fence: 5, State: SessionReady, KeyID: sessionTestKeyID, StateChangedAt: now, UpdatedAt: now}
	store := &supervisorStoreFake{status: owned, beginErr: errors.New("database unavailable"), diagnostics: SessionLifecycleDiagnostics{PendingCommands: 2}}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.setStatus(owned, "")
	supervisor.sampleDiagnostics(context.Background())
	if err = supervisor.recover(context.Background()); err == nil {
		t.Fatal("BeginSessionEpoch persistence failure succeeded")
	} else if info, ok := ClassifySessionTransportError(err); !ok || info.Code != sessionErrorPersistence || !info.Fatal {
		t.Fatalf("BeginSessionEpoch failure classified as %#v, %t", info, ok)
	}
	if snapshot := supervisor.Snapshot(); snapshot.State != "" || snapshot.Epoch != 0 || snapshot.Fence != 0 || snapshot.Diagnostics != (SessionLifecycleDiagnostics{}) || !snapshot.DiagnosticsUnavailable {
		t.Fatalf("BeginSessionEpoch failure retained owned lifecycle state: %#v", snapshot)
	}
}

func TestSupervisorBackoffPersistenceFailurePausesExactlyOnceAndResumes(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{advanceErr: ErrState}
	attention := make(chan SupervisorEvent, 2)
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.ResumeInterval = 0
	config.Observer = func(_ context.Context, event SupervisorEvent) error {
		if event.Kind == SupervisorAttention {
			attention <- event
		}
		return nil
	}
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	runner := &supervisorRunnerFake{run: func(ctx context.Context, call int) error {
		if call == 1 {
			return sessionFailure(sessionErrorRelayUnavailable, false)
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	supervisor.runner = runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	waitSupervisorState(t, supervisor, true)
	supervisor.mu.RLock()
	pauseGeneration := supervisor.pauseGeneration
	supervisor.mu.RUnlock()
	if pauseGeneration != 1 {
		t.Fatalf("pause generation=%d want 1", pauseGeneration)
	}
	select {
	case <-attention:
	case <-time.After(time.Second):
		t.Fatal("attention event was not emitted")
	}
	if err = supervisor.Resume(); err != nil {
		t.Fatalf("immediate Resume after persistence failure: %v", err)
	}
	waitRunnerCalls(t, runner, 2)
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-attention:
		t.Fatalf("duplicate attention event=%#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestSessionTransportLifecycleClaimAndObservationSynchronize(t *testing.T) {
	transport := &SessionTransport{}
	const observers = 64
	start := make(chan struct{})
	var wait sync.WaitGroup
	var claims int
	var claimsMu sync.Mutex
	for index := 0; index < observers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if _, claimed := transport.claimLifecycle(func(context.Context, *sessionTransportLifecycleEvent) error { return nil }); claimed {
				claimsMu.Lock()
				claims++
				claimsMu.Unlock()
			}
			_ = observeTestLifecycle(context.Background(), transport, testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false))
		}()
	}
	close(start)
	wait.Wait()
	if claims != 1 || !transport.hasLifecycle() {
		t.Fatalf("claims=%d lifecycle=%t", claims, transport.hasLifecycle())
	}
}

func TestSupervisorClaimRejectsDirectTransportRunOnceDuringPause(t *testing.T) {
	transport := &SessionTransport{}
	supervisor, err := NewSupervisor(transport, &supervisorStoreFake{}, &supervisorRecoveryFake{}, supervisorCompleterFake{}, DefaultSupervisorConfig())
	if err != nil {
		t.Fatal(err)
	}
	supervisor.runner = &supervisorRunnerFake{run: func(context.Context, int) error { return sessionFailure(sessionErrorProtocol, true) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	waitSupervisorState(t, supervisor, true)
	if err := transport.RunOnce(context.Background()); !sessionErrorCode(err, sessionErrorIdentity) {
		t.Fatalf("direct RunOnce after supervisor claim=%v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReadyOwnersArePerAttemptAndCannotAliasFallback(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	transport := &SessionTransport{}
	supervisor, err := NewSupervisor(transport, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = supervisor.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	primary := []sessionTransportLifecycleEvent{
		testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false),
		testLifecycleEvent(SessionTransportAuthenticating, sessionTestKeyID, false),
		{SessionTransportEvent: SessionTransportEvent{Stage: SessionTransportReady}, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID},
	}
	for index := range primary {
		if err = transport.observeLifecycle(context.Background(), &primary[index]); err != nil {
			t.Fatal(err)
		}
	}
	primaryOwner := primary[2].readyOwner
	fallback := []sessionTransportLifecycleEvent{
		testLifecycleEvent(SessionTransportConnecting, sessionTestChallengeID, true),
		testLifecycleEvent(SessionTransportAuthenticating, sessionTestChallengeID, true),
		{SessionTransportEvent: SessionTransportEvent{Stage: SessionTransportReady, Fallback: true}, ControllerID: sessionTestControllerID, KeyID: sessionTestChallengeID},
	}
	for index := range fallback {
		if err = transport.observeLifecycle(context.Background(), &fallback[index]); err != nil {
			t.Fatal(err)
		}
	}
	if primaryOwner.Epoch != 2 || primaryOwner.Fence != 3 || primary[2].readyOwner != primaryOwner {
		t.Fatalf("primary owner aliased after fallback: before=%#v after=%#v", primaryOwner, primary[2].readyOwner)
	}
	if fallback[2].readyOwner.Epoch != 3 || fallback[2].readyOwner.Fence != 3 {
		t.Fatalf("fallback owner=%#v", fallback[2].readyOwner)
	}
}

func TestBlockingTransportObserverIsBoundedAndFailsClosed(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	transport := &SessionTransport{observer: func(context.Context, SessionTransportEvent) error {
		started <- struct{}{}
		<-release
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		event := testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false)
		done <- transport.observeLifecycle(ctx, &event)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("observer did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !sessionErrorCode(err, sessionErrorPersistence) {
			t.Fatalf("blocking observer error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking observer held transport lifecycle")
	}
	event := testLifecycleEvent(SessionTransportAuthenticating, sessionTestKeyID, false)
	if err := transport.observeLifecycle(context.Background(), &event); !sessionErrorCode(err, sessionErrorPersistence) {
		t.Fatalf("second observer while blocked error=%v", err)
	}
	close(release)
}

func TestSupervisorPendingReadyCompleterUsesExactFenceAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		complete error
		wantErr  bool
	}{
		{name: "success"},
		{name: "failure", complete: controlFailure(controlErrorPersistence), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &supervisorStoreFake{}
			completer := &supervisorCompleterCapture{err: test.complete}
			config := DefaultSupervisorConfig()
			config.Now = func() time.Time { return now }
			transport := &SessionTransport{}
			supervisor, err := NewSupervisor(transport, store, &supervisorRecoveryFake{}, completer, config)
			if err != nil {
				t.Fatal(err)
			}
			if err = supervisor.recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, event := range []sessionTransportLifecycleEvent{testLifecycleEvent(SessionTransportConnecting, sessionTestKeyID, false), testLifecycleEvent(SessionTransportAuthenticating, sessionTestKeyID, false)} {
				if err = transport.observeLifecycle(context.Background(), &event); err != nil {
					t.Fatal(err)
				}
			}
			ready := sessionTransportLifecycleEvent{SessionTransportEvent: SessionTransportEvent{Stage: SessionTransportReady, Pending: true}, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID}
			err = transport.observeLifecycle(context.Background(), &ready)
			if test.wantErr {
				if !sessionErrorCode(err, sessionErrorPersistence) {
					t.Fatalf("completion failure=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			completer.mu.Lock()
			calls := append([]sessionReadyOwner(nil), completer.calls...)
			completer.mu.Unlock()
			if len(calls) != 1 || calls[0].Epoch != 2 || calls[0].Fence != 3 {
				t.Fatalf("completion calls=%#v", calls)
			}
		})
	}
}

func TestSupervisorBackoffExponentCapAndReadyReset(t *testing.T) {
	config := DefaultSupervisorConfig()
	config.InitialBackoff = time.Second
	config.MaximumBackoff = 8 * time.Second
	config.Jitter = func(delay time.Duration, _ uint32) time.Duration { return delay }
	supervisor, err := NewSupervisor(&SessionTransport{}, &supervisorStoreFake{}, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	for attempt, want := range map[uint32]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second, 20: 8 * time.Second} {
		if got := supervisor.backoff(attempt); got != want {
			t.Fatalf("attempt %d backoff=%s want=%s", attempt, got, want)
		}
	}
	supervisor.mu.Lock()
	supervisor.reconnect = SessionTransportReconnect{}
	supervisor.mu.Unlock()
	if got := supervisor.backoff(1); got != config.InitialBackoff {
		t.Fatalf("ready reset first backoff=%s", got)
	}
}

func TestSupervisorCancellationDuringRecoveryWaitsForEntry(t *testing.T) {
	entered := make(chan struct{}, 1)
	recovery := supervisorRecoveryFunc(func(ctx context.Context, _ ControllerKeyRecoveryCursor, _ int) (ControllerKeyRecoveryPage, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return ControllerKeyRecoveryPage{}, ctx.Err()
	})
	supervisor, err := NewSupervisor(&SessionTransport{}, &supervisorStoreFake{}, recovery, supervisorCompleterFake{}, DefaultSupervisorConfig())
	if err != nil {
		t.Fatal(err)
	}
	runner := &supervisorRunnerFake{run: func(context.Context, int) error { return nil }}
	supervisor.runner = runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
	if runner.callCount() != 0 {
		t.Fatalf("dialed before canceled recovery: %d", runner.callCount())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorResumeCoalescesAndRateLimitsAcceptedAttempts(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	config.ResumeInterval = time.Minute
	supervisor, err := NewSupervisor(&SessionTransport{}, &supervisorStoreFake{}, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.mu.Lock()
	supervisor.running = true
	supervisor.runGeneration = 1
	supervisor.mu.Unlock()
	supervisor.pause(context.Background(), sessionErrorProtocol)
	const callers = 32
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); results <- supervisor.Resume() }()
	}
	wait.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatalf("coalesced Resume=%v", result)
		}
	}
	supervisor.mu.RLock()
	queued := len(supervisor.resumeCh)
	supervisor.mu.RUnlock()
	if queued != 1 {
		t.Fatalf("resume queue length=%d", queued)
	}
	if !supervisor.waitResume(context.Background()) {
		t.Fatal("queued resume was not accepted")
	}
	supervisor.pause(context.Background(), sessionErrorProtocol)
	if err = supervisor.Resume(); !errors.Is(err, ErrSupervisorRateLimited) {
		t.Fatalf("rate limited Resume=%v", err)
	}
}

func TestSupervisorStopReportsPersistenceFailureWithoutRawError(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store := &supervisorStoreFake{status: SessionStatus{ControllerID: sessionTestControllerID, Epoch: 1, Fence: 1, State: SessionReady, StateChangedAt: now, UpdatedAt: now}, advanceErr: errors.New("database provider response secret")}
	config := DefaultSupervisorConfig()
	config.Now = func() time.Time { return now }
	supervisor, err := NewSupervisor(&SessionTransport{}, store, &supervisorRecoveryFake{}, supervisorCompleterFake{}, config)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.setStatus(store.status, "")
	supervisor.stop(context.Background())
	if snapshot := supervisor.Snapshot(); snapshot.Outcome != sessionErrorPersistence || snapshot.Paused {
		t.Fatalf("stop failure snapshot=%#v", snapshot)
	}
}
