package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
)

func TestSessionTransportRejectsMissingOrWrongSubprotocolBeforeHello(t *testing.T) {
	for _, negotiated := range []string{"", "other.protocol"} {
		t.Run(negotiated, func(t *testing.T) {
			now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
			privateKey := deterministicSessionPrivateKey(10)
			publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
			store := &fakeSessionTransportStore{
				identity: ControllerIdentity{ControllerID: sessionTestControllerID, State: ControllerActive},
				key:      ControllerKey{ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, State: KeyActive, PublicKey: publicKey},
				ack:      []protocol.ACKState{},
			}
			credentials := &fakeSessionTransportCredentials{}
			socket := &scriptedSessionSocket{subprotocol: negotiated}
			config := DefaultSessionTransportConfig()
			config.Now = func() time.Time { return now }
			config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xc1}, 128))
			transport, err := NewSessionTransport("https://relay.example", store, credentials, nil, func(context.Context, string, *websocket.DialOptions) (SessionSocket, *http.Response, error) {
				return socket, nil, nil
			}, config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = transport.connect(context.Background()); !sessionErrorCode(err, sessionErrorProtocol) {
				t.Fatalf("negotiated subprotocol %q = %v", negotiated, err)
			}
			if len(socket.writes) != 0 || credentials.reads != 0 || !socket.closed {
				t.Fatalf("protocol failure leaked into handshake: writes=%d credentialReads=%d closed=%v", len(socket.writes), credentials.reads, socket.closed)
			}
		})
	}
}

func TestSessionTransportRejectsUnsafeWireFrames(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	validChallenge := &protocol.Challenge{
		Envelope:    protocol.NewEnvelope(protocol.TypeChallenge, sessionTestChallengeID, now),
		SessionID:   sessionTestSessionID,
		ServerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, protocol.NonceBytes)),
		ACKDigest:   base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, protocol.DigestBytes)),
		ExpiresAt:   now.Add(time.Minute),
	}
	challengeJSON, err := protocol.Encode(validChallenge, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	controllerFrame := &protocol.Hello{
		Envelope:     protocol.NewEnvelope(protocol.TypeHello, sessionTestReadyID, now),
		ControllerID: sessionTestControllerID,
		KeyID:        sessionTestKeyID,
		ClientNonce:  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, protocol.NonceBytes)),
		ACKState:     []protocol.ACKState{},
	}
	controllerJSON, err := protocol.Encode(controllerFrame, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		messageType websocket.MessageType
		data        []byte
		maximum     int
	}{
		{name: "binary", messageType: websocket.MessageBinary, data: challengeJSON, maximum: protocol.DefaultMaxEnvelopeBytes},
		{name: "empty", messageType: websocket.MessageText, data: []byte{}, maximum: protocol.DefaultMaxEnvelopeBytes},
		{name: "oversized", messageType: websocket.MessageText, data: bytes.Repeat([]byte{'x'}, 4097), maximum: 4096},
		{name: "duplicate", messageType: websocket.MessageText, data: []byte(`{"version":1,"type":"challenge","type":"challenge"}`), maximum: 4096},
		{name: "unknown", messageType: websocket.MessageText, data: []byte(`{"version":1,"type":"unknown"}`), maximum: 4096},
		{name: "trailing", messageType: websocket.MessageText, data: append(append([]byte{}, challengeJSON...), []byte(` {}`)...), maximum: protocol.DefaultMaxEnvelopeBytes},
		{name: "wrong_direction", messageType: websocket.MessageText, data: controllerJSON, maximum: protocol.DefaultMaxEnvelopeBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := &rawSessionSocket{messageType: test.messageType, data: append([]byte(nil), test.data...), subprotocol: protocol.Subprotocol}
			transport := &SessionTransport{config: DefaultSessionTransportConfig()}
			frame, err := transport.readFrame(context.Background(), socket, test.maximum)
			if frame != nil || !sessionErrorCode(err, sessionErrorProtocol) {
				t.Fatalf("unsafe frame result=%#v err=%v", frame, err)
			}
		})
	}
}

func TestSessionTransportValidatesChallengeAndReadyLifetimes(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		mutateChallenge    func(*protocol.Challenge)
		mutateReady        func(*protocol.Ready)
		wantCredentialRead bool
	}{
		{name: "challenge_expired", mutateChallenge: func(frame *protocol.Challenge) { frame.ExpiresAt = now }},
		{name: "challenge_over_max", mutateChallenge: func(frame *protocol.Challenge) { frame.ExpiresAt = now.Add(6 * time.Minute) }},
		{name: "challenge_sent_in_future", mutateChallenge: func(frame *protocol.Challenge) {
			frame.SentAt = now.Add(6 * time.Minute)
			frame.ExpiresAt = now.Add(7 * time.Minute)
		}},
		{name: "ready_session_mismatch", mutateReady: func(frame *protocol.Ready) { frame.SessionID = sessionTestSubscriptionID }, wantCredentialRead: true},
		{name: "ready_expired", mutateReady: func(frame *protocol.Ready) { frame.SessionExpiresAt = now }, wantCredentialRead: true},
		{name: "ready_over_max", mutateReady: func(frame *protocol.Ready) { frame.SessionExpiresAt = now.Add(25 * time.Hour) }, wantCredentialRead: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport, socket, credentials := newSessionHandshakeFixture(t, now, test.mutateChallenge, test.mutateReady)
			if _, err := transport.connect(context.Background()); !sessionErrorCode(err, sessionErrorProtocol) {
				t.Fatalf("invalid handshake = %v", err)
			}
			wantReads := 0
			if test.wantCredentialRead {
				wantReads = 1
			}
			if credentials.reads != wantReads || !socket.closed {
				t.Fatalf("credentialReads=%d want=%d closed=%v", credentials.reads, wantReads, socket.closed)
			}
			if wantReads == 1 {
				for _, value := range credentials.bundle.PrivateKey {
					if value != 0 {
						t.Fatal("private key was not destroyed after post-read validation failure")
					}
				}
			}
		})
	}
}

func TestSessionTransportDestroysKeyAfterPostReadFailures(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		prepare  func(*SessionTransport, *scriptedSessionSocket)
		wantCode string
	}{
		{
			name: "authenticate_message_id_entropy",
			prepare: func(transport *SessionTransport, _ *scriptedSessionSocket) {
				// Hello consumes 16 bytes for its ID and 32 for its nonce. The next
				// message ID therefore fails only after the protected key was read.
				transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xe1}, 48))
				transport.issuer.Entropy = transport.config.Entropy
			},
			wantCode: sessionErrorIdentity,
		},
		{
			name: "authenticate_write",
			prepare: func(_ *SessionTransport, socket *scriptedSessionSocket) {
				previous := socket.onWrite
				socket.onWrite = func(frame protocol.Frame) error {
					if _, ok := frame.(*protocol.Authenticate); ok {
						return errors.New("write failed with provider body ghu_secret")
					}
					return previous(frame)
				}
			},
			wantCode: sessionErrorConnectionClosed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport, socket, credentials := newSessionHandshakeFixture(t, now, nil, nil)
			test.prepare(transport, socket)
			if _, err := transport.connect(context.Background()); !sessionErrorCode(err, test.wantCode) || strings.Contains(err.Error(), "ghu_secret") {
				t.Fatalf("post-read failure = %v, want sanitized %s", err, test.wantCode)
			}
			if credentials.reads != 1 || !socket.closed {
				t.Fatalf("credentialReads=%d closed=%v", credentials.reads, socket.closed)
			}
			for _, value := range credentials.bundle.PrivateKey {
				if value != 0 {
					t.Fatal("private key was not destroyed after post-read failure")
				}
			}
		})
	}
}

func TestSessionTransportRejectsMismatchedProtectedKeyMetadataBeforeSigning(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*ControllerKeyBundle)
	}{
		{name: "version", mutate: func(bundle *ControllerKeyBundle) { bundle.Version++ }},
		{name: "controller", mutate: func(bundle *ControllerKeyBundle) { bundle.ControllerID = sessionTestSubscriptionID }},
		{name: "key", mutate: func(bundle *ControllerKeyBundle) { bundle.KeyID = sessionTestSubscriptionID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport, socket, credentials := newSessionHandshakeFixture(t, now, nil, nil)
			test.mutate(&credentials.bundle)
			if _, err := transport.connect(context.Background()); !sessionErrorCode(err, sessionErrorCredential) {
				t.Fatalf("mismatched protected key metadata = %v", err)
			}
			if credentials.reads != 1 || len(socket.writes) != 1 || !socket.closed {
				t.Fatalf("credentialReads=%d writes=%d closed=%v", credentials.reads, len(socket.writes), socket.closed)
			}
			for _, value := range credentials.bundle.PrivateKey {
				if value != 0 {
					t.Fatal("mismatched protected key was not destroyed")
				}
			}
		})
	}
}

func TestSessionTransportUsesOneOverallHandshakeDeadline(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	transport, socket, credentials := newSessionHandshakeFixture(t, now, nil, nil)
	transport.config.HandshakeTimeout = 100 * time.Millisecond
	delayed := &delayedSessionSocket{SessionSocket: socket, delay: 70 * time.Millisecond}
	transport.dial = func(context.Context, string, *websocket.DialOptions) (SessionSocket, *http.Response, error) {
		return delayed, nil, nil
	}
	started := time.Now()
	if _, err := transport.connect(context.Background()); !sessionErrorCode(err, sessionErrorConnectionClosed) {
		t.Fatalf("cumulative handshake deadline = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("handshake exceeded its overall deadline: %s", elapsed)
	}
	if credentials.reads != 0 || !socket.closed {
		t.Fatalf("credentialReads=%d closed=%v", credentials.reads, socket.closed)
	}
}

func TestSessionTransportSynchronizesAnEmptyFullSet(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	transport, socket, _ := newSessionHandshakeFixture(t, now, nil, nil)
	store := transport.store.(*fakeSessionTransportStore)
	store.subscriptions = []protocol.Subscription{}
	active, err := transport.connect(context.Background())
	if err != nil {
		t.Fatalf("connect with no subscriptions: %v", err)
	}
	defer active.closeNow()
	if store.acknowledged != 1 {
		t.Fatalf("empty full set was not acknowledged: %#v", store)
	}
	last, ok := socket.writes[len(socket.writes)-1].(*protocol.SubscriptionsSync)
	if !ok || last.Subscriptions == nil || len(last.Subscriptions) != 0 {
		t.Fatalf("empty full set was not preserved on the wire: %#v", last)
	}
}

func TestActiveSessionRejectsHeartbeatReplay(t *testing.T) {
	transport := &SessionTransport{config: DefaultSessionTransportConfig()}
	session := &activeControllerSession{transport: transport}
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	frame := &protocol.Heartbeat{Envelope: protocol.NewEnvelope(protocol.TypeHeartbeat, sessionTestChallengeID, now), Sequence: 7}
	response, heartbeat, err := session.handleInbound(context.Background(), frame, 6)
	if err != nil || response != nil || heartbeat != 7 {
		t.Fatalf("advance heartbeat response=%#v heartbeat=%d err=%v", response, heartbeat, err)
	}
	for _, sequence := range []uint64{7, 6} {
		frame.Sequence = sequence
		if response, heartbeat, err = session.handleInbound(context.Background(), frame, 7); response != nil || heartbeat != 7 || !sessionErrorCode(err, sessionErrorProtocol) {
			t.Fatalf("replayed heartbeat %d response=%#v heartbeat=%d err=%v", sequence, response, heartbeat, err)
		}
	}
}

func TestActiveSessionCancellationAndExpiryCloseAndJoin(t *testing.T) {
	for _, test := range []struct {
		name         string
		trigger      func(context.CancelFunc, *manualSessionTimerSource)
		wantCode     string
		wantCanceled bool
	}{
		{name: "cancel", trigger: func(cancel context.CancelFunc, _ *manualSessionTimerSource) { cancel() }, wantCanceled: true},
		{name: "expiry", trigger: func(_ context.CancelFunc, timers *manualSessionTimerSource) { timers.timer <- time.Now() }, wantCode: sessionErrorExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
			timers := newManualSessionTimerSource()
			transport := &SessionTransport{config: DefaultSessionTransportConfig()}
			transport.config.Now = func() time.Time { return now }
			transport.config.Timers = timers
			socket := newBlockingSessionSocket()
			session := &activeControllerSession{
				transport: transport, conn: socket, maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes,
				maxOutstanding: 1, heartbeatInterval: 15 * time.Second, expiresAt: now.Add(time.Hour),
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- session.run(ctx) }()
			timers.waitStarted(t)
			test.trigger(cancel, timers)
			select {
			case err := <-result:
				if test.wantCanceled && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel = %v, want observed cancellation", err)
				}
				if !test.wantCanceled && !sessionErrorCode(err, test.wantCode) {
					t.Fatalf("session result = %v, want %s", err, test.wantCode)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("session did not join reader and writer")
			}
			if !socket.isClosed() || !timers.stopped() {
				t.Fatalf("closed=%v timersStopped=%v", socket.isClosed(), timers.stopped())
			}
		})
	}
}

func TestAlreadyExpiredSessionStartsNoLoopsOrTimers(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	timers := &countingSessionTimerSource{}
	socket := &countingSessionSocket{}
	transport := &SessionTransport{config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now }
	transport.config.Timers = timers
	session := &activeControllerSession{
		transport: transport, conn: socket, maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes,
		maxOutstanding: 1, heartbeatInterval: 15 * time.Second, expiresAt: now,
	}
	if err := session.run(context.Background()); !sessionErrorCode(err, sessionErrorExpired) {
		t.Fatalf("already-expired session = %v", err)
	}
	if socket.reads.Load() != 0 || socket.writes.Load() != 0 || timers.tickers.Load() != 0 || timers.timers.Load() != 0 || socket.closes.Load() != 1 {
		t.Fatalf("expired session started work: reads=%d writes=%d tickers=%d timers=%d closes=%d", socket.reads.Load(), socket.writes.Load(), timers.tickers.Load(), timers.timers.Load(), socket.closes.Load())
	}
}

func TestSessionSetupLatencyCannotExtendAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	timers := newSetupLatencySessionTimerSource(&clock, 2*time.Second)
	store := &fakeSessionTransportStore{sourceDecision: AckDecision()}
	transport := &SessionTransport{store: store, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x91}, 256))
	transport.config.Timers = timers
	socket := newSlowWriteSessionSocket()
	socket.reads <- sessionSourceFrame(now, 1)
	session := &activeControllerSession{
		transport: transport, conn: socket, controllerID: sessionTestControllerID,
		maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes, maxOutstanding: 1,
		heartbeatInterval: 15 * time.Second, expiresAt: now.Add(time.Second),
	}

	if err := session.run(context.Background()); !sessionErrorCode(err, sessionErrorExpired) {
		t.Fatalf("setup-latency session = %v", err)
	}
	if store.sourceCommits != 0 || store.accessCommits != 0 {
		t.Fatalf("expired setup reached durable sinks: source=%d access=%d", store.sourceCommits, store.accessCommits)
	}
	select {
	case <-socket.writeStarted:
		t.Fatal("expired setup emitted an ACK")
	default:
	}
	if order := timers.orderSnapshot(); len(order) != 2 || order[0] != "timer" || order[1] != "ticker" {
		t.Fatalf("session timer setup order = %v, want timer before ticker", order)
	}
	if !socket.isClosed() || !timers.stopped() {
		t.Fatalf("closed=%v timersStopped=%v", socket.isClosed(), timers.stopped())
	}
}

func TestInboundPersistenceSuppressesACKWhenAbsoluteExpiryWins(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	store := &expiryAdvancingSessionStore{fakeSessionTransportStore: fakeSessionTransportStore{sourceDecision: AckDecision()}, clock: &clock, expiresAt: now.Add(time.Second)}
	transport := &SessionTransport{store: store, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x92}, 256))
	session := &activeControllerSession{transport: transport, controllerID: sessionTestControllerID, expiresAt: store.expiresAt}

	response, _, err := session.handleInbound(context.Background(), sessionSourceFrame(now, 1), 0)
	if response != nil || !sessionErrorCode(err, sessionErrorExpired) || store.sourceCommits != 1 {
		t.Fatalf("expiry-winning persistence response=%#v err=%v commits=%d", response, err, store.sourceCommits)
	}
}

func TestActiveSessionStopsWhenSlowPeerSaturatesBoundedQueue(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	timers := newManualSessionTimerSource()
	store := &fakeSessionTransportStore{sourceDecision: AckDecision()}
	transport := &SessionTransport{store: store, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xf1}, 256))
	transport.config.Timers = timers
	socket := newSlowWriteSessionSocket()
	session := &activeControllerSession{
		transport: transport, conn: socket, controllerID: sessionTestControllerID,
		maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes, maxOutstanding: 1,
		heartbeatInterval: 15 * time.Second, expiresAt: now.Add(time.Hour),
	}
	result := make(chan error, 1)
	go func() { result <- session.run(context.Background()) }()
	timers.waitStarted(t)

	first := sessionSourceFrame(now, 1)
	socket.reads <- first
	select {
	case <-socket.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not block on the slow peer")
	}
	socket.reads <- sessionSourceFrame(now, 2)
	socket.reads <- sessionSourceFrame(now, 3)
	select {
	case err := <-result:
		if !sessionErrorCode(err, sessionErrorQueueSaturated) {
			t.Fatalf("slow peer result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session did not stop after its bounded queue saturated")
	}
	if store.sourceCommits != 3 || !socket.isClosed() || !timers.stopped() {
		t.Fatalf("commits=%d closed=%v timersStopped=%v", store.sourceCommits, socket.isClosed(), timers.stopped())
	}
}

func TestActiveSessionBoundsBlockedPersistenceAndSuppressesACK(t *testing.T) {
	for _, test := range []struct {
		name             string
		persistenceLimit time.Duration
		triggerExpiry    bool
		wantCode         string
	}{
		{name: "persistence_timeout", persistenceLimit: 50 * time.Millisecond, wantCode: sessionErrorPersistence},
		{name: "session_expiry", persistenceLimit: time.Minute, triggerExpiry: true, wantCode: sessionErrorExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
			timers := newManualSessionTimerSource()
			store := &fakeSessionTransportStore{sourceDecision: AckDecision(), sourceBlock: true, sourceStarted: make(chan struct{})}
			transport := &SessionTransport{store: store, config: DefaultSessionTransportConfig()}
			transport.config.Now = func() time.Time { return now }
			transport.config.PersistenceTimeout = test.persistenceLimit
			transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x21}, 256))
			transport.config.Timers = timers
			socket := newSlowWriteSessionSocket()
			session := &activeControllerSession{
				transport: transport, conn: socket, controllerID: sessionTestControllerID,
				maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes, maxOutstanding: 1,
				heartbeatInterval: 15 * time.Second, expiresAt: now.Add(time.Hour),
			}
			result := make(chan error, 1)
			go func() { result <- session.run(context.Background()) }()
			timers.waitStarted(t)
			socket.reads <- sessionSourceFrame(now, 1)
			select {
			case <-store.sourceStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("durable commit did not start")
			}
			if test.triggerExpiry {
				timers.timer <- now.Add(time.Hour)
			}
			select {
			case err := <-result:
				if !sessionErrorCode(err, test.wantCode) || strings.Contains(err.Error(), "context") {
					t.Fatalf("blocked persistence result = %v, want sanitized %s", err, test.wantCode)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("blocked durable commit outlived its session deadline")
			}
			select {
			case <-socket.writeStarted:
				t.Fatal("ACK was written before the durable commit completed")
			default:
			}
			if !socket.isClosed() || !timers.stopped() {
				t.Fatalf("closed=%v timersStopped=%v", socket.isClosed(), timers.stopped())
			}
		})
	}
}

func TestSessionTransportErrorsAreSanitized(t *testing.T) {
	secret := "ghu_secret-do-not-log"
	err := &SessionTransportError{Code: sessionErrorCredential, Fatal: true}
	var output bytes.Buffer
	slog.New(slog.NewTextHandler(&output, nil)).Error("relay session", "error", err)
	for _, rendering := range []string{err.Error(), err.String(), err.GoString()} {
		if strings.Contains(rendering, secret) || strings.Contains(rendering, "private") || rendering != "controller relay session failed: credential_unavailable" {
			t.Fatalf("unsafe session error rendering %q", rendering)
		}
	}
	if strings.Contains(err.LogValue().String(), secret) {
		t.Fatal("structured error leaked a secret")
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "private") {
		t.Fatalf("structured log leaked sensitive detail: %q", output.String())
	}
	malicious := &SessionTransportError{Code: secret, Fatal: true}
	output.Reset()
	slog.New(slog.NewTextHandler(&output, nil)).Error("relay session", "error", malicious)
	if strings.Contains(malicious.Error(), secret) || strings.Contains(malicious.LogValue().String(), secret) || strings.Contains(output.String(), secret) {
		t.Fatalf("unrecognized error code leaked through a public error value: %q", output.String())
	}
}

func TestDefaultRelayHTTPTransportDisablesProxyAndRequiresTLS12(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := newRelayHTTPClient(nil, 0)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport = %T", client.Transport)
	}
	if transport.Proxy != nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unsafe default relay transport proxyConfigured=%t TLS=%#v", transport.Proxy != nil, transport.TLSClientConfig)
	}
	if transport.MaxResponseHeaderBytes != 16<<10 || client.Timeout != 0 {
		t.Fatalf("unbounded relay headers/incorrect WSS client timeout: %#v", transport)
	}
}

func newSessionHandshakeFixture(t *testing.T, now time.Time, mutateChallenge func(*protocol.Challenge), mutateReady func(*protocol.Ready)) (*SessionTransport, *scriptedSessionSocket, *fakeSessionTransportCredentials) {
	t.Helper()
	privateKey := deterministicSessionPrivateKey(11)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	store := &fakeSessionTransportStore{
		identity: ControllerIdentity{ControllerID: sessionTestControllerID, State: ControllerActive},
		key:      ControllerKey{ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, State: KeyActive, PublicKey: publicKey},
		ack:      []protocol.ACKState{},
	}
	credentials := &fakeSessionTransportCredentials{bundle: ControllerKeyBundle{
		Version: credentialVersion, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID,
		PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...),
	}}
	socket := &scriptedSessionSocket{subprotocol: protocol.Subprotocol}
	socket.onWrite = func(frame protocol.Frame) error {
		switch value := frame.(type) {
		case *protocol.Hello:
			digest, err := protocol.CanonicalACKDigest(value.ACKState)
			if err != nil {
				return err
			}
			challenge := &protocol.Challenge{
				Envelope:    protocol.NewEnvelope(protocol.TypeChallenge, sessionTestChallengeID, now),
				SessionID:   sessionTestSessionID,
				ServerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, protocol.NonceBytes)),
				ACKDigest:   base64.RawURLEncoding.EncodeToString(digest[:]),
				ExpiresAt:   now.Add(time.Minute),
			}
			if mutateChallenge != nil {
				mutateChallenge(challenge)
			}
			socket.enqueue(challenge)
		case *protocol.Authenticate:
			ready := &protocol.Ready{
				Envelope: protocol.NewEnvelope(protocol.TypeReady, sessionTestReadyID, now), SessionID: sessionTestSessionID,
				HeartbeatIntervalSeconds: 15, MaxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes,
				MaxSubscriptions: protocol.MaxArrayItems, MaxOutstanding: 32, SessionExpiresAt: now.Add(time.Hour),
				Reconnect: protocol.ReconnectPolicy{InitialDelayMillis: 500, MaximumDelayMillis: 30000, Multiplier: 2, JitterPercent: 20},
			}
			if mutateReady != nil {
				mutateReady(ready)
			}
			socket.enqueue(ready)
		case *protocol.SubscriptionsSync:
			socket.enqueue(&protocol.SubscriptionsSynced{
				Envelope:        protocol.NewEnvelope(protocol.TypeSubscriptionsSynced, sessionTestSyncedID, now),
				TargetMessageID: value.MessageID, Generation: value.Generation, AcceptedCount: uint32(len(value.Subscriptions)),
			})
		}
		return nil
	}
	config := DefaultSessionTransportConfig()
	config.Now = func() time.Time { return now }
	config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xd1}, 256))
	transport, err := NewSessionTransport("https://relay.example", store, credentials, nil, func(context.Context, string, *websocket.DialOptions) (SessionSocket, *http.Response, error) {
		return socket, nil, nil
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	return transport, socket, credentials
}

type rawSessionSocket struct {
	messageType websocket.MessageType
	data        []byte
	err         error
	subprotocol string
}

func (socket *rawSessionSocket) Read(context.Context) (websocket.MessageType, []byte, error) {
	return socket.messageType, append([]byte(nil), socket.data...), socket.err
}
func (*rawSessionSocket) Write(context.Context, websocket.MessageType, []byte) error { return nil }
func (*rawSessionSocket) Close(websocket.StatusCode, string) error                   { return nil }
func (*rawSessionSocket) CloseNow() error                                            { return nil }
func (*rawSessionSocket) SetReadLimit(int64)                                         {}
func (socket *rawSessionSocket) Subprotocol() string                                 { return socket.subprotocol }

type countingSessionSocket struct {
	reads  atomic.Int32
	writes atomic.Int32
	closes atomic.Int32
}

func (socket *countingSessionSocket) Read(context.Context) (websocket.MessageType, []byte, error) {
	socket.reads.Add(1)
	return 0, nil, errors.New("unexpected read")
}
func (socket *countingSessionSocket) Write(context.Context, websocket.MessageType, []byte) error {
	socket.writes.Add(1)
	return errors.New("unexpected write")
}
func (*countingSessionSocket) Close(websocket.StatusCode, string) error { return nil }
func (socket *countingSessionSocket) CloseNow() error {
	socket.closes.Add(1)
	return nil
}
func (*countingSessionSocket) SetReadLimit(int64)  {}
func (*countingSessionSocket) Subprotocol() string { return protocol.Subprotocol }

type countingSessionTimerSource struct {
	tickers atomic.Int32
	timers  atomic.Int32
}

type setupLatencySessionTimerSource struct {
	*manualSessionTimerSource
	clock *atomic.Int64
	delay time.Duration
	mu    sync.Mutex
	order []string
}

func newSetupLatencySessionTimerSource(clock *atomic.Int64, delay time.Duration) *setupLatencySessionTimerSource {
	return &setupLatencySessionTimerSource{manualSessionTimerSource: newManualSessionTimerSource(), clock: clock, delay: delay}
}

func (source *setupLatencySessionTimerSource) NewTicker(duration time.Duration) SessionTicker {
	source.mu.Lock()
	source.order = append(source.order, "ticker")
	source.mu.Unlock()
	source.clock.Add(int64(source.delay))
	return source.manualSessionTimerSource.NewTicker(duration)
}

func (source *setupLatencySessionTimerSource) NewTimer(duration time.Duration) SessionTimer {
	source.mu.Lock()
	source.order = append(source.order, "timer")
	source.mu.Unlock()
	return source.manualSessionTimerSource.NewTimer(duration)
}

func (source *setupLatencySessionTimerSource) orderSnapshot() []string {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]string(nil), source.order...)
}

type expiryAdvancingSessionStore struct {
	fakeSessionTransportStore
	clock     *atomic.Int64
	expiresAt time.Time
}

func (store *expiryAdvancingSessionStore) CommitSourceDesired(ctx context.Context, controllerID string, source protocol.SourceDesired, receivedAt time.Time) (InboxDecision, error) {
	decision, err := store.fakeSessionTransportStore.CommitSourceDesired(ctx, controllerID, source, receivedAt)
	store.clock.Store(store.expiresAt.UnixNano())
	return decision, err
}

func (source *countingSessionTimerSource) NewTicker(time.Duration) SessionTicker {
	source.tickers.Add(1)
	return &manualSessionTicker{source: newManualSessionTimerSource()}
}
func (source *countingSessionTimerSource) NewTimer(time.Duration) SessionTimer {
	source.timers.Add(1)
	return &manualSessionTimer{source: newManualSessionTimerSource()}
}

type delayedSessionSocket struct {
	SessionSocket
	delay time.Duration
}

func (socket *delayedSessionSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if err := waitSessionDelay(ctx, socket.delay); err != nil {
		return 0, nil, err
	}
	return socket.SessionSocket.Read(ctx)
}
func (socket *delayedSessionSocket) Write(ctx context.Context, messageType websocket.MessageType, data []byte) error {
	if err := waitSessionDelay(ctx, socket.delay); err != nil {
		return err
	}
	return socket.SessionSocket.Write(ctx, messageType, data)
}

func waitSessionDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type blockingSessionSocket struct {
	mu     sync.Mutex
	closed bool
}

type slowWriteSessionSocket struct {
	*blockingSessionSocket
	reads        chan protocol.Frame
	writeStarted chan struct{}
	writeOnce    sync.Once
}

func newSlowWriteSessionSocket() *slowWriteSessionSocket {
	return &slowWriteSessionSocket{
		blockingSessionSocket: newBlockingSessionSocket(),
		reads:                 make(chan protocol.Frame, 3),
		writeStarted:          make(chan struct{}),
	}
}
func (socket *slowWriteSessionSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case frame := <-socket.reads:
		encoded, err := protocol.Encode(frame, protocol.DefaultMaxEnvelopeBytes)
		return websocket.MessageText, encoded, err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}
func (socket *slowWriteSessionSocket) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	socket.writeOnce.Do(func() { close(socket.writeStarted) })
	<-ctx.Done()
	return ctx.Err()
}

func sessionSourceFrame(now time.Time, generation uint64) *protocol.SourceDesired {
	suffix := byte('0' + generation)
	return &protocol.SourceDesired{
		Envelope:       protocol.NewEnvelope(protocol.TypeSourceDesired, "88888888-8888-4888-8888-88888888888"+string(suffix), now),
		DeliveryID:     "99999999-9999-4999-8999-99999999999" + string(suffix),
		SubscriptionID: sessionTestSubscriptionID, Generation: generation,
		InstallationID: 7, RepositoryID: 8, Ref: "refs/heads/main",
		ObservedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: now,
	}
}

func newBlockingSessionSocket() *blockingSessionSocket { return &blockingSessionSocket{} }
func (*blockingSessionSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (*blockingSessionSocket) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func (socket *blockingSessionSocket) Close(websocket.StatusCode, string) error {
	return socket.CloseNow()
}
func (socket *blockingSessionSocket) CloseNow() error {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.closed = true
	return nil
}
func (*blockingSessionSocket) SetReadLimit(int64)  {}
func (*blockingSessionSocket) Subprotocol() string { return protocol.Subprotocol }
func (socket *blockingSessionSocket) isClosed() bool {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	return socket.closed
}

type manualSessionTimerSource struct {
	ticker      chan time.Time
	timer       chan time.Time
	started     chan struct{}
	stopMu      sync.Mutex
	tickerStop  bool
	timerStop   bool
	startedOnce sync.Once
}

func newManualSessionTimerSource() *manualSessionTimerSource {
	return &manualSessionTimerSource{ticker: make(chan time.Time, 1), timer: make(chan time.Time, 1), started: make(chan struct{})}
}
func (source *manualSessionTimerSource) NewTicker(time.Duration) SessionTicker {
	source.startedOnce.Do(func() { close(source.started) })
	return &manualSessionTicker{source: source}
}
func (source *manualSessionTimerSource) NewTimer(time.Duration) SessionTimer {
	return &manualSessionTimer{source: source}
}
func (source *manualSessionTimerSource) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-source.started:
	case <-time.After(2 * time.Second):
		t.Fatal("session timers did not start")
	}
}
func (source *manualSessionTimerSource) stopped() bool {
	source.stopMu.Lock()
	defer source.stopMu.Unlock()
	return source.tickerStop && source.timerStop
}

type manualSessionTicker struct{ source *manualSessionTimerSource }

func (ticker *manualSessionTicker) C() <-chan time.Time { return ticker.source.ticker }
func (ticker *manualSessionTicker) Stop() {
	ticker.source.stopMu.Lock()
	defer ticker.source.stopMu.Unlock()
	ticker.source.tickerStop = true
}

type manualSessionTimer struct{ source *manualSessionTimerSource }

func (timer *manualSessionTimer) C() <-chan time.Time { return timer.source.timer }
func (timer *manualSessionTimer) Stop() {
	timer.source.stopMu.Lock()
	defer timer.source.stopMu.Unlock()
	timer.source.timerStop = true
}
