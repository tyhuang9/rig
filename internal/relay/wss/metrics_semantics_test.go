package wss

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

func TestStableFailureCategoriesAreExhaustiveAndIgnoreCloseStatus(t *testing.T) {
	tests := map[string]sessionFailureCategory{
		"expected_hello":               failureCategoryProtocolReject,
		"invalid_frame":                failureCategoryProtocolReject,
		"expected_subscriptions_sync":  failureCategoryProtocolReject,
		"binary_not_supported":         failureCategoryProtocolReject,
		"invalid_direction":            failureCategoryProtocolReject,
		"handshake_timeout":            failureCategoryProtocolReject,
		"inbound_rate_limited":         failureCategoryProtocolReject,
		"idle_timeout":                 failureCategoryProtocolReject,
		"heartbeat_replay":             failureCategoryProtocolReject,
		"unexpected_frame":             failureCategoryProtocolReject,
		"replay_mismatch":              failureCategoryProtocolReject,
		"identity_mismatch":            failureCategoryProtocolReject,
		"subscriptions_sync_failed":    failureCategoryProtocolReject,
		"authentication_failed":        failureCategoryAuthReject,
		"authentication_timeout":       failureCategoryAuthReject,
		"lease_unavailable":            failureCategoryStore,
		"decision_failed":              failureCategoryStore,
		"delivery_unavailable":         failureCategoryStore,
		"lease_lost":                   failureCategoryStore,
		"binding_remove_failed":        failureCategoryStore,
		"key_revoke_failed":            failureCategoryStore,
		"controller_revoke_failed":     failureCategoryStore,
		"rotation_proposal_failed":     failureCategoryStore,
		"rotation_confirmation_failed": failureCategoryStore,
		"rotation_finalization_failed": failureCategoryStore,
		"write_failed":                 failureCategoryWrite,
		"connection_closed":            failureCategoryNone,
		"internal_error":               failureCategoryNone,
		"server_shutdown":              failureCategoryNone,
		"terminal_command":             failureCategoryNone,
		"session_expired":              failureCategoryNone,
	}
	for code, want := range tests {
		t.Run(code, func(t *testing.T) {
			// A deliberately misleading status proves the metric category is
			// derived from the closed code, never from a WebSocket status.
			got := failure(code, websocket.StatusPolicyViolation, true).category
			if got != want {
				t.Fatalf("category=%q want=%q", got, want)
			}
		})
	}
	t.Run("unknown code fails closed", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("unclassified failure code did not panic")
			}
		}()
		_ = failure("new_unclassified_code", websocket.StatusInternalError, true)
	})
}

func TestTerminalFailureObservationUsesExplicitCategoryOnly(t *testing.T) {
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		failure  *sessionFailure
		category string
	}{
		{name: "peer protocol", failure: failure("invalid_frame", websocket.StatusPolicyViolation, true), category: "protocol_reject"},
		{name: "peer auth", failure: failure("authentication_failed", websocket.StatusPolicyViolation, true), category: "auth_reject"},
		{name: "store during authentication", failure: categorizedFailure("authentication_failed", websocket.StatusPolicyViolation, true, failureCategoryStore), category: "store_failure"},
		{name: "store during subscription sync", failure: categorizedFailure("subscriptions_sync_failed", websocket.StatusPolicyViolation, true, failureCategoryStore), category: "store_failure"},
		{name: "lease expiry", failure: failure("lease_lost", websocket.StatusPolicyViolation, true), category: "store_failure"},
		{name: "lease renewal", failure: failure("lease_lost", websocket.StatusPolicyViolation, true), category: "store_failure"},
		{name: "read", failure: categorizedFailure("connection_closed", websocket.StatusNormalClosure, false, failureCategoryRead), category: "read_failure"},
		{name: "write", failure: failure("write_failed", websocket.StatusInternalError, false), category: "write_failure"},
		{name: "server", failure: failure("server_shutdown", websocket.StatusPolicyViolation, true)},
		{name: "normal peer close", failure: failure("connection_closed", websocket.StatusPolicyViolation, true)},
		{name: "session expiry", failure: failure("session_expired", websocket.StatusPolicyViolation, true)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := handler.Stats().LifecycleOutcomes
			handlerSession := &session{handler: handler}
			handlerSession.observeTerminalFailure(test.failure)
			after := handler.Stats().LifecycleOutcomes
			for index, name := range LifecycleOutcomeNames() {
				wantDelta := uint64(0)
				if name == test.category {
					wantDelta = 1
				}
				if after[index]-before[index] != wantDelta {
					t.Fatalf("outcome %s delta=%d want=%d", name, after[index]-before[index], wantDelta)
				}
			}
		})
	}
}

func TestReadLoopClassifiesLivePeerErrorsIndependentlyOfCloseStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "normal close", err: websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: "complete"}},
		{name: "going away", err: websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "leaving"}},
		{name: "policy close", err: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "policy"}},
		{name: "abnormal close", err: websocket.CloseError{Code: websocket.StatusAbnormalClosure, Reason: "abnormal"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			conn := &fakeSocket{read: func(context.Context, int) (websocket.MessageType, []byte, error) {
				return 0, nil, test.err
			}}
			session := newSession(handler, conn)
			done := make(chan struct{})
			go func() {
				session.readLoop(context.Background())
				close(done)
			}()

			session.readRequests <- struct{}{}
			event := <-session.reads
			<-done
			if !errors.Is(event.err, test.err) {
				t.Fatalf("read error=%v want=%v", event.err, test.err)
			}
			if event.category != failureCategoryRead {
				t.Fatalf("category=%q want=%q", event.category, failureCategoryRead)
			}

			session.observeTerminalFailure(categorizedFailure("connection_closed", websocket.StatusNormalClosure, false, event.category))
			stats := handler.Stats()
			if got := lifecycleOutcome(stats, string(failureCategoryRead)); got != 1 {
				t.Fatalf("read failure outcomes=%d want=1", got)
			}
			var total uint64
			for _, count := range stats.LifecycleOutcomes {
				total += count
			}
			if total != 1 {
				t.Fatalf("total lifecycle outcomes=%d want=1", total)
			}
		})
	}
}

func TestReadLoopDoesNotClassifyLocalCancellationAsPeerFailure(t *testing.T) {
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	conn := &fakeSocket{read: func(context.Context, int) (websocket.MessageType, []byte, error) {
		cancel()
		return 0, nil, context.Canceled
	}}
	session := newSession(handler, conn)
	// An unbuffered result channel proves a locally canceled read is discarded
	// rather than surfaced as a peer read failure.
	session.reads = make(chan readEvent)
	done := make(chan struct{})
	go func() {
		session.readLoop(ctx)
		close(done)
	}()

	session.readRequests <- struct{}{}
	<-done
	select {
	case event := <-session.reads:
		t.Fatalf("unexpected read event after local cancellation: %+v", event)
	default:
	}
	if got := lifecycleOutcome(handler.Stats(), string(failureCategoryRead)); got != 0 {
		t.Fatalf("read failure outcomes=%d want=0", got)
	}
}

type leaseMetricStore struct {
	*fakeStateStore
	acquireErr     error
	releaseErr     error
	releaseStarted chan struct{}
	releaseGate    chan struct{}
	releaseOnce    sync.Once
}

func (s *leaseMetricStore) AcquireLease(ctx context.Context, sessionID string, duration time.Duration) (store.Lease, error) {
	if s.acquireErr != nil {
		return store.Lease{}, s.acquireErr
	}
	return s.fakeStateStore.AcquireLease(ctx, sessionID, duration)
}

func (s *leaseMetricStore) ReleaseLease(ctx context.Context, lease store.Lease) error {
	if s.releaseStarted != nil {
		s.releaseOnce.Do(func() { close(s.releaseStarted) })
	}
	if s.releaseGate != nil {
		select {
		case <-s.releaseGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := s.fakeStateStore.ReleaseLease(ctx, lease); err != nil {
		return err
	}
	return s.releaseErr
}

type leaseMetricSocket struct {
	*fakeSocket
	closeStarted chan struct{}
	closeGate    chan struct{}
	terminate    chan struct{}
	closeOnce    sync.Once
}

func (s *leaseMetricSocket) Close(websocket.StatusCode, string) error {
	if s.closeStarted != nil {
		s.closeOnce.Do(func() { close(s.closeStarted) })
	}
	if s.closeGate != nil {
		<-s.closeGate
	}
	return nil
}

func TestLeaseGaugeTracksLocalLeaseIndependentlyFromAuthenticatedClose(t *testing.T) {
	for _, releaseErr := range []error{nil, errors.New("release unavailable")} {
		name := "release success"
		if releaseErr != nil {
			name = "release failure"
		}
		t.Run(name, func(t *testing.T) {
			handler, state, conn, cancel, syncDone := newLeaseMetricSession(t, nil, releaseErr)
			defer cancel()
			runDone := make(chan struct{})
			session := newSession(handler, conn)
			go func() {
				session.run(handler.lifecycle)
				close(runDone)
			}()
			<-syncDone
			assertSessionGauges(t, handler, 1, 1)

			close(conn.terminate)
			<-state.releaseStarted
			assertSessionGauges(t, handler, 1, 1)
			close(state.releaseGate)
			<-conn.closeStarted
			assertSessionGauges(t, handler, 1, 0)
			close(conn.closeGate)
			<-runDone
			assertSessionGauges(t, handler, 0, 0)

			// The exit fail-safe is idempotent and cannot underflow the gauge.
			session.releaseLeaseCount()
			session.releaseLeaseCount()
			assertSessionGauges(t, handler, 0, 0)
			stats := handler.Stats()
			if releaseErr != nil && lifecycleOutcome(stats, "store_failure") != 1 {
				t.Fatalf("store failure outcomes=%d", lifecycleOutcome(stats, "store_failure"))
			}
		})
	}
}

func TestLeaseGaugeDoesNotIncrementWhenAcquireFails(t *testing.T) {
	handler, _, conn, _, _ := newLeaseMetricSession(t, store.ErrConflict, nil)
	close(conn.closeGate)
	done := make(chan struct{})
	go func() {
		newSession(handler, conn).run(handler.lifecycle)
		close(done)
	}()
	<-done
	assertSessionGauges(t, handler, 0, 0)
	if got := lifecycleOutcome(handler.Stats(), "store_failure"); got != 1 {
		t.Fatalf("store failure outcomes=%d", got)
	}
}

func newLeaseMetricSession(t *testing.T, acquireErr, releaseErr error) (*Handler, *leaseMetricStore, *leaseMetricSocket, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	now := commandTime
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{18}, ed25519.SeedSize))
	state := &leaseMetricStore{
		fakeStateStore: &fakeStateStore{publicKey: privateKey.Public().(ed25519.PublicKey)},
		acquireErr:     acquireErr,
		releaseErr:     releaseErr,
		releaseStarted: make(chan struct{}),
		releaseGate:    make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	config := DefaultConfig()
	config.HandshakeTimeout = time.Second
	config.StoreTimeout = time.Second
	config.WriteTimeout = time.Second
	config.CloseTimeout = time.Second
	handler, err := NewHandler(state, config, Options{Now: func() time.Time { return now }, Entropy: bytes.NewReader(bytes.Repeat([]byte{19}, 4096)), Lifecycle: ctx})
	if err != nil {
		t.Fatal(err)
	}
	hello := &protocol.Hello{Envelope: protocol.NewEnvelope(protocol.TypeHello, commandMessage1, now), ControllerID: commandID1, KeyID: commandID2, ClientNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{20}, protocol.NonceBytes)), ACKState: []protocol.ACKState{}}
	syncFrame := &protocol.SubscriptionsSync{Envelope: protocol.NewEnvelope(protocol.TypeSubscriptionsSync, commandMessage2, now), Generation: 1, Subscriptions: []protocol.Subscription{{SubscriptionID: commandID4, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}}}
	challengeReady := make(chan *protocol.Challenge, 1)
	syncDone := make(chan struct{})
	terminate := make(chan struct{})
	var syncOnce sync.Once
	baseSocket := &fakeSocket{subprotocol: protocol.Subprotocol}
	baseSocket.read = func(readCtx context.Context, index int) (websocket.MessageType, []byte, error) {
		switch index {
		case 0:
			data, encodeErr := protocol.Encode(hello, config.MaxEnvelopeBytes)
			return websocket.MessageText, data, encodeErr
		case 1:
			select {
			case challenge := <-challengeReady:
				transcript, transcriptErr := protocol.WSSAuthenticationTranscript(protocol.AuthenticationBinding{ControllerID: hello.ControllerID, KeyID: hello.KeyID, SessionID: challenge.SessionID, ClientNonce: hello.ClientNonce, ServerNonce: challenge.ServerNonce, ACKDigest: challenge.ACKDigest, ExpiresAt: challenge.ExpiresAt})
				if transcriptErr != nil {
					return 0, nil, transcriptErr
				}
				signature, signErr := protocol.Sign(privateKey, transcript)
				if signErr != nil {
					return 0, nil, signErr
				}
				authenticate := &protocol.Authenticate{Envelope: protocol.NewEnvelope(protocol.TypeAuthenticate, commandID3, now), SessionID: challenge.SessionID, Signature: signature}
				data, encodeErr := protocol.Encode(authenticate, config.MaxEnvelopeBytes)
				return websocket.MessageText, data, encodeErr
			case <-readCtx.Done():
				return 0, nil, readCtx.Err()
			}
		case 2:
			data, encodeErr := protocol.Encode(syncFrame, config.MaxEnvelopeBytes)
			return websocket.MessageText, data, encodeErr
		case 3:
			select {
			case <-terminate:
				terminal := &protocol.ProtocolError{Envelope: protocol.NewEnvelope(protocol.TypeProtocolError, activeUUID(2399), now), Code: "controller.stopped", Fatal: true}
				data, encodeErr := protocol.Encode(terminal, config.MaxEnvelopeBytes)
				return websocket.MessageText, data, encodeErr
			case <-readCtx.Done():
				return 0, nil, readCtx.Err()
			}
		default:
			<-readCtx.Done()
			return 0, nil, readCtx.Err()
		}
	}
	baseSocket.onWrite = func(frame protocol.Frame) {
		switch value := frame.(type) {
		case *protocol.Challenge:
			challengeReady <- value
		case *protocol.SubscriptionsSynced:
			syncOnce.Do(func() { close(syncDone) })
		}
	}
	conn := &leaseMetricSocket{fakeSocket: baseSocket, closeStarted: make(chan struct{}), closeGate: make(chan struct{}), terminate: terminate}
	return handler, state, conn, cancel, syncDone
}

func assertSessionGauges(t *testing.T, handler *Handler, authenticated, leases int64) {
	t.Helper()
	stats := handler.Stats()
	if stats.Authenticated != authenticated || stats.LeasesActive != leases {
		t.Fatalf("authenticated=%d leases=%d want=%d/%d", stats.Authenticated, stats.LeasesActive, authenticated, leases)
	}
}

func lifecycleOutcome(stats Stats, name string) uint64 {
	for index, candidate := range LifecycleOutcomeNames() {
		if candidate == name {
			return stats.LifecycleOutcomes[index]
		}
	}
	return 0
}
