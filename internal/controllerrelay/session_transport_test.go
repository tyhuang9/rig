package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	sessionTestControllerID   = "11111111-1111-4111-8111-111111111111"
	sessionTestKeyID          = "22222222-2222-4222-8222-222222222222"
	sessionTestSessionID      = "33333333-3333-4333-8333-333333333333"
	sessionTestChallengeID    = "44444444-4444-4444-8444-444444444444"
	sessionTestReadyID        = "55555555-5555-4555-8555-555555555555"
	sessionTestSyncedID       = "66666666-6666-4666-8666-666666666666"
	sessionTestSubscriptionID = "77777777-7777-4777-8777-777777777777"
)

func TestSessionTransportHandshakeBindsIdentityAndSynchronizesBeforeReady(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	privateKey := deterministicSessionPrivateKey(9)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	store := &fakeSessionTransportStore{
		identity: ControllerIdentity{ControllerID: sessionTestControllerID, State: ControllerActive},
		key:      ControllerKey{ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, State: KeyActive, PublicKey: publicKey},
		ack:      []protocol.ACKState{{SubscriptionID: sessionTestSubscriptionID, Generation: 4}},
	}
	credentials := &fakeSessionTransportCredentials{bundle: ControllerKeyBundle{
		Version: credentialVersion, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID,
		PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...),
	}}
	socket := &scriptedSessionSocket{subprotocol: protocol.Subprotocol}
	var hello *protocol.Hello
	var challenge *protocol.Challenge
	socket.onWrite = func(frame protocol.Frame) error {
		switch value := frame.(type) {
		case *protocol.Hello:
			hello = value
			digest, err := protocol.CanonicalACKDigest(value.ACKState)
			if err != nil {
				return err
			}
			challenge = &protocol.Challenge{
				Envelope:  protocol.NewEnvelope(protocol.TypeChallenge, sessionTestChallengeID, now),
				SessionID: sessionTestSessionID, ServerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, protocol.NonceBytes)),
				ACKDigest: base64.RawURLEncoding.EncodeToString(digest[:]), ExpiresAt: now.Add(time.Minute),
			}
			socket.enqueue(challenge)
		case *protocol.Authenticate:
			if hello == nil || challenge == nil || value.SessionID != challenge.SessionID {
				return errors.New("authenticate preceded challenge")
			}
			transcript, err := protocol.WSSAuthenticationTranscript(protocol.AuthenticationBinding{
				ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, SessionID: challenge.SessionID,
				ClientNonce: hello.ClientNonce, ServerNonce: challenge.ServerNonce, ACKDigest: challenge.ACKDigest, ExpiresAt: challenge.ExpiresAt,
			})
			if err != nil {
				return err
			}
			signature, err := base64.RawURLEncoding.DecodeString(value.Signature)
			if err != nil || !ed25519.Verify(publicKey, transcript, signature) {
				return errors.New("invalid authentication signature")
			}
			socket.enqueue(&protocol.Ready{
				Envelope: protocol.NewEnvelope(protocol.TypeReady, sessionTestReadyID, now), SessionID: sessionTestSessionID,
				HeartbeatIntervalSeconds: 15, MaxEnvelopeBytes: 2 << 20, MaxSubscriptions: protocol.MaxArrayItems,
				MaxOutstanding: 32, SessionExpiresAt: now.Add(time.Hour),
				Reconnect: protocol.ReconnectPolicy{InitialDelayMillis: 500, MaximumDelayMillis: 30000, Multiplier: 2, JitterPercent: 20},
			})
		case *protocol.SubscriptionsSync:
			if value.Generation != 1 || len(value.Subscriptions) != 1 {
				return errors.New("unexpected full subscription sync")
			}
			socket.enqueue(&protocol.SubscriptionsSynced{
				Envelope:        protocol.NewEnvelope(protocol.TypeSubscriptionsSynced, sessionTestSyncedID, now),
				TargetMessageID: value.MessageID, Generation: value.Generation, AcceptedCount: uint32(len(value.Subscriptions)),
			})
		default:
			return errors.New("unexpected controller frame")
		}
		return nil
	}

	dialed := false
	dial := func(_ context.Context, target string, options *websocket.DialOptions) (SessionSocket, *http.Response, error) {
		dialed = true
		if target != "https://relay.example/v1/controllers/connect" || options.HTTPClient == nil || options.HTTPHeader.Get("Origin") != "" || len(options.Subprotocols) != 1 || options.Subprotocols[0] != protocol.Subprotocol || options.CompressionMode != websocket.CompressionDisabled {
			t.Fatalf("unsafe websocket dial target=%q options=%#v", target, options)
		}
		return socket, nil, nil
	}
	config := DefaultSessionTransportConfig()
	config.Now = func() time.Time { return now }
	config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x81}, 256))
	transport, err := NewSessionTransport("https://relay.example", store, credentials, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), dial, config)
	if err != nil {
		t.Fatal(err)
	}
	active, err := transport.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer active.closeNow()
	if !dialed || hello == nil || active.sessionID != sessionTestSessionID || active.maxEnvelopeBytes != config.MaxEnvelopeBytes || active.maxOutstanding != 32 {
		t.Fatalf("unexpected active session %#v hello=%#v", active, hello)
	}
	if store.acknowledged != 1 || store.ackTarget == "" || store.prepareMessageID == "" {
		t.Fatalf("subscription sync was not durably acknowledged: %#v", store)
	}
	for _, value := range credentials.bundle.PrivateKey {
		if value != 0 {
			t.Fatal("decrypted controller private key was not destroyed immediately after signing")
		}
	}
}

func TestSessionTransportInteroperatesWithTLSWebSocketRelay(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	privateKey := deterministicSessionPrivateKey(7)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	store := &fakeSessionTransportStore{
		identity: ControllerIdentity{ControllerID: sessionTestControllerID, State: ControllerActive},
		key:      ControllerKey{ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, State: KeyActive, PublicKey: publicKey},
		ack:      make([]protocol.ACKState, 0),
	}
	credentials := &fakeSessionTransportCredentials{bundle: ControllerKeyBundle{
		Version: credentialVersion, ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID,
		PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...),
	}}
	serverErrors := make(chan error, 1)
	var serverStage atomic.Value
	serverStage.Store("accept")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != relayControllerSessionPath || request.URL.RawQuery != "" || request.Header.Get("Origin") != "" {
			serverErrors <- errors.New("unsafe websocket request shape")
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			serverErrors <- err
			return
		}
		defer connection.CloseNow()
		connection.SetReadLimit(protocol.DefaultMaxEnvelopeBytes)

		serverStage.Store("read hello subprotocol=" + connection.Subprotocol())
		helloFrame, err := readSessionTestFrame(request.Context(), connection)
		hello, ok := helloFrame.(*protocol.Hello)
		if err != nil || !ok {
			serverErrors <- errors.New("expected hello")
			return
		}
		digest, err := protocol.CanonicalACKDigest(hello.ACKState)
		if err != nil {
			serverErrors <- err
			return
		}
		challenge := &protocol.Challenge{
			Envelope: protocol.NewEnvelope(protocol.TypeChallenge, sessionTestChallengeID, now), SessionID: sessionTestSessionID,
			ServerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, protocol.NonceBytes)),
			ACKDigest:   base64.RawURLEncoding.EncodeToString(digest[:]), ExpiresAt: now.Add(time.Minute),
		}
		serverStage.Store("write challenge")
		if err = writeSessionTestFrame(request.Context(), connection, challenge); err != nil {
			serverErrors <- err
			return
		}
		serverStage.Store("read authenticate")
		authFrame, err := readSessionTestFrame(request.Context(), connection)
		auth, ok := authFrame.(*protocol.Authenticate)
		if err != nil || !ok {
			serverErrors <- errors.New("expected authenticate")
			return
		}
		transcript, err := protocol.WSSAuthenticationTranscript(protocol.AuthenticationBinding{
			ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, SessionID: sessionTestSessionID,
			ClientNonce: hello.ClientNonce, ServerNonce: challenge.ServerNonce, ACKDigest: challenge.ACKDigest, ExpiresAt: challenge.ExpiresAt,
		})
		if err != nil {
			serverErrors <- err
			return
		}
		signature, decodeErr := base64.RawURLEncoding.DecodeString(auth.Signature)
		if decodeErr != nil || !ed25519.Verify(publicKey, transcript, signature) {
			serverErrors <- errors.New("authentication signature mismatch")
			return
		}
		ready := &protocol.Ready{
			Envelope: protocol.NewEnvelope(protocol.TypeReady, sessionTestReadyID, now), SessionID: sessionTestSessionID,
			HeartbeatIntervalSeconds: 15, MaxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes,
			MaxSubscriptions: protocol.MaxArrayItems, MaxOutstanding: 32, SessionExpiresAt: now.Add(time.Hour),
			Reconnect: protocol.ReconnectPolicy{InitialDelayMillis: 500, MaximumDelayMillis: 30000, Multiplier: 2, JitterPercent: 20},
		}
		serverStage.Store("write ready")
		if err = writeSessionTestFrame(request.Context(), connection, ready); err != nil {
			serverErrors <- err
			return
		}
		serverStage.Store("read sync")
		syncFrame, err := readSessionTestFrame(request.Context(), connection)
		subscriptions, ok := syncFrame.(*protocol.SubscriptionsSync)
		if err != nil || !ok {
			serverErrors <- errors.New("expected subscriptions sync")
			return
		}
		synced := &protocol.SubscriptionsSynced{
			Envelope:        protocol.NewEnvelope(protocol.TypeSubscriptionsSynced, sessionTestSyncedID, now),
			TargetMessageID: subscriptions.MessageID, Generation: subscriptions.Generation, AcceptedCount: uint32(len(subscriptions.Subscriptions)),
		}
		serverStage.Store("write synced")
		if err = writeSessionTestFrame(request.Context(), connection, synced); err != nil {
			serverErrors <- err
			return
		}
		serverStage.Store("complete")
		serverErrors <- nil
	}))
	defer server.Close()

	config := DefaultSessionTransportConfig()
	config.Now = func() time.Time { return now }
	config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xb1}, 256))
	transport, err := NewSessionTransport(server.URL, store, credentials, server.Client().Transport, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	active, err := transport.connect(context.Background())
	if err != nil {
		select {
		case serverErr := <-serverErrors:
			t.Fatalf("connect to TLS relay at %s: %v (server: %v)", serverStage.Load(), err, serverErr)
		default:
			t.Fatalf("connect to TLS relay at %s: %v", serverStage.Load(), err)
		}
	}
	active.closeNow()
	if err = <-serverErrors; err != nil {
		t.Fatalf("TLS relay: %v", err)
	}
}

func TestSessionTransportRejectsChallengeACKDigestMismatchBeforeCredentialRead(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	privateKey := deterministicSessionPrivateKey(4)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	store := &fakeSessionTransportStore{
		identity: ControllerIdentity{ControllerID: sessionTestControllerID, State: ControllerActive},
		key:      ControllerKey{ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, State: KeyActive, PublicKey: publicKey},
		ack:      []protocol.ACKState{},
	}
	credentials := &fakeSessionTransportCredentials{}
	socket := &scriptedSessionSocket{subprotocol: protocol.Subprotocol}
	socket.onWrite = func(frame protocol.Frame) error {
		if _, ok := frame.(*protocol.Hello); ok {
			socket.enqueue(&protocol.Challenge{
				Envelope: protocol.NewEnvelope(protocol.TypeChallenge, sessionTestChallengeID, now), SessionID: sessionTestSessionID,
				ServerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, protocol.NonceBytes)),
				ACKDigest:   base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, protocol.DigestBytes)), ExpiresAt: now.Add(time.Minute),
			})
		}
		return nil
	}
	config := DefaultSessionTransportConfig()
	config.Now = func() time.Time { return now }
	config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x91}, 128))
	transport, err := NewSessionTransport("https://relay.example", store, credentials, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), func(context.Context, string, *websocket.DialOptions) (SessionSocket, *http.Response, error) {
		return socket, nil, nil
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transport.connect(context.Background()); !sessionErrorCode(err, sessionErrorProtocol) {
		t.Fatalf("ACK digest mismatch = %v", err)
	}
	if credentials.reads != 0 {
		t.Fatalf("credential read before challenge validation: %d", credentials.reads)
	}
}

func TestSessionTransportBoundsAmbiguousFinalizeAuthenticationFallback(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	pendingKeyID := "99999999-9999-4999-8999-999999999999"
	privateKey := deterministicSessionPrivateKey(12)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	store := &fakeSessionTransportStore{
		identity: ControllerIdentity{ControllerID: sessionTestControllerID, State: ControllerActive},
		candidates: []ControllerKey{
			{ControllerID: sessionTestControllerID, KeyID: sessionTestKeyID, State: KeyActive, PublicKey: bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize)},
			{ControllerID: sessionTestControllerID, KeyID: pendingKeyID, State: KeyPending, PublicKey: publicKey},
		},
		ack:           []protocol.ACKState{},
		subscriptions: []protocol.Subscription{},
	}
	credentials := &fakeSessionTransportCredentials{bundle: ControllerKeyBundle{Version: credentialVersion, ControllerID: sessionTestControllerID, KeyID: pendingKeyID, PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...)}}
	socket := &scriptedSessionSocket{subprotocol: protocol.Subprotocol}
	var hello *protocol.Hello
	socket.onWrite = func(frame protocol.Frame) error {
		switch value := frame.(type) {
		case *protocol.Hello:
			hello = value
			if value.KeyID != pendingKeyID {
				return errors.New("fallback did not use pending key")
			}
			digest, err := protocol.CanonicalACKDigest(value.ACKState)
			if err != nil {
				return err
			}
			socket.enqueue(&protocol.Challenge{Envelope: protocol.NewEnvelope(protocol.TypeChallenge, sessionTestChallengeID, now), SessionID: sessionTestSessionID, ServerNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, protocol.NonceBytes)), ACKDigest: base64.RawURLEncoding.EncodeToString(digest[:]), ExpiresAt: now.Add(time.Minute)})
		case *protocol.Authenticate:
			if hello == nil {
				return errors.New("authenticate before hello")
			}
			socket.enqueue(&protocol.Ready{Envelope: protocol.NewEnvelope(protocol.TypeReady, sessionTestReadyID, now), SessionID: sessionTestSessionID, HeartbeatIntervalSeconds: 15, MaxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes, MaxSubscriptions: protocol.MaxArrayItems, MaxOutstanding: 32, SessionExpiresAt: now.Add(time.Hour), Reconnect: protocol.ReconnectPolicy{InitialDelayMillis: 500, MaximumDelayMillis: 30000, Multiplier: 2, JitterPercent: 20}})
		case *protocol.SubscriptionsSync:
			socket.enqueue(&protocol.SubscriptionsSynced{Envelope: protocol.NewEnvelope(protocol.TypeSubscriptionsSynced, sessionTestSyncedID, now), TargetMessageID: value.MessageID, Generation: value.Generation, AcceptedCount: uint32(len(value.Subscriptions))})
		default:
			return errors.New("unexpected fallback controller frame")
		}
		return nil
	}
	var dialCount int
	dial := func(context.Context, string, *websocket.DialOptions) (SessionSocket, *http.Response, error) {
		dialCount++
		if dialCount == 1 {
			return nil, nil, errors.New("old key rejected after relay finalization")
		}
		if dialCount == 2 {
			return socket, nil, nil
		}
		return nil, nil, errors.New("unbounded authentication attempt")
	}
	config := DefaultSessionTransportConfig()
	config.Now = func() time.Time { return now }
	config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xd1}, 256))
	transport, err := NewSessionTransport("https://relay.example", store, credentials, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), dial, config)
	if err != nil {
		t.Fatal(err)
	}
	active, err := transport.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer active.closeNow()
	if dialCount != 2 || active.keyID != pendingKeyID || credentials.reads != 1 {
		t.Fatalf("fallback dialCount=%d activeKey=%q credentialReads=%d", dialCount, active.keyID, credentials.reads)
	}

	tooMany := &fakeSessionTransportStore{identity: store.identity, candidates: append(append([]ControllerKey(nil), store.candidates...), ControllerKey{ControllerID: sessionTestControllerID, KeyID: sessionTestReadyID, State: KeyPending, PublicKey: publicKey}), ack: []protocol.ACKState{}, subscriptions: []protocol.Subscription{}}
	transport.store = tooMany
	dialCount = 0
	if _, err = transport.connect(context.Background()); !sessionErrorCode(err, sessionErrorIdentity) || dialCount != 0 {
		t.Fatalf("unbounded candidate set err=%v dials=%d", err, dialCount)
	}
}

func TestActiveSessionCommitsBeforeACKAndSuppressesACKOnStoreFailure(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	store := &fakeSessionTransportStore{commitSourceErr: errors.New("database unavailable")}
	transport := &SessionTransport{store: store, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0xa1}, 64))
	session := &activeControllerSession{transport: transport, controllerID: sessionTestControllerID, expiresAt: now.Add(time.Hour)}
	source := &protocol.SourceDesired{
		Envelope:   protocol.NewEnvelope(protocol.TypeSourceDesired, sessionTestChallengeID, now),
		DeliveryID: sessionTestSessionID, SubscriptionID: sessionTestSubscriptionID, Generation: 1,
		InstallationID: 7, RepositoryID: 8, Ref: "refs/heads/main", ObservedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: now,
	}
	response, _, err := session.handleInbound(context.Background(), source, 0)
	if response != nil || !sessionErrorCode(err, sessionErrorPersistence) || store.sourceCommits != 1 || store.sourceController != sessionTestControllerID {
		t.Fatalf("commit failure response=%#v err=%v commits=%d controller=%q", response, err, store.sourceCommits, store.sourceController)
	}

	store.commitSourceErr = nil
	store.sourceDecision = AckDecision()
	response, _, err = session.handleInbound(context.Background(), source, 0)
	ack, ok := response.(*protocol.Ack)
	if err != nil || !ok || ack.TargetMessageID != source.MessageID || store.sourceCommits != 2 {
		t.Fatalf("durable ACK response=%#v err=%v commits=%d", response, err, store.sourceCommits)
	}

	access := &protocol.AccessChange{
		Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, sessionTestSyncedID, now),
		EventID:  sessionTestReadyID, InstallationID: 7, RepositoryID: 8,
		ChangeCode: "repository.removed", ObservedAt: now, AckRequired: true,
	}
	response, _, err = session.handleInbound(context.Background(), access, 0)
	accessACK, ok := response.(*protocol.Ack)
	if err != nil || !ok || accessACK.TargetMessageID != access.MessageID || store.accessController != sessionTestControllerID {
		t.Fatalf("controller-scoped access ACK response=%#v err=%v controller=%q", response, err, store.accessController)
	}
}

func TestActiveSessionRoutesControlsOnlyAfterHandlerPersistence(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	response := &protocol.KeyRotationFinalize{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalize, sessionTestSyncedID, now), RotationID: sessionTestSubscriptionID, RetireOldKey: true}
	handler := &fakeSessionControlHandler{handleResult: SessionControlResult{Response: response, Action: ControlContinue}}
	transport := &SessionTransport{config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now }
	transport.config.ControlHandler = handler
	session := &activeControllerSession{transport: transport, controllerID: sessionTestControllerID, keyID: sessionTestKeyID, sessionID: sessionTestSessionID, expiresAt: now.Add(time.Hour)}
	inbound := &protocol.KeyRotationConfirmed{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirmed, sessionTestChallengeID, now), TargetMessageID: sessionTestReadyID, RotationID: sessionTestSubscriptionID}
	got, heartbeat, action, err := session.handleInboundWithAction(context.Background(), inbound, 7)
	if err != nil || got != response || heartbeat != 7 || action != ControlContinue || handler.handleCalls != 1 || !handler.persisted {
		t.Fatalf("control route got=%#v heartbeat=%d action=%d err=%v handler=%#v", got, heartbeat, action, err, handler)
	}

	handler.handleErr = controlFailure(controlErrorPersistence)
	handler.persisted = false
	got, _, _, err = session.handleInboundWithAction(context.Background(), inbound, 7)
	if got != nil || !sessionErrorCode(err, sessionErrorPersistence) || handler.persisted {
		t.Fatalf("failed persistence emitted response=%#v err=%v", got, err)
	}
}

func TestActiveSessionReplaysPendingControlsThroughSoleWriterAndClearsScratch(t *testing.T) {
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	confirm := &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, sessionTestChallengeID, now), RotationID: sessionTestSubscriptionID, Signature: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5c}, protocol.SignatureBytes))}
	handler := &fakeSessionControlHandler{pending: []protocol.Frame{confirm}}
	socket := &blockingControlSocket{written: make(chan protocol.Frame, 1)}
	transport := &SessionTransport{config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now }
	transport.config.ControlHandler = handler
	session := &activeControllerSession{transport: transport, conn: socket, controllerID: sessionTestControllerID, keyID: sessionTestKeyID, sessionID: sessionTestSessionID, maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes, maxOutstanding: 4, heartbeatInterval: time.Minute, expiresAt: now.Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- session.run(ctx) }()
	select {
	case written := <-socket.written:
		if _, ok := written.(*protocol.KeyRotationConfirm); !ok {
			t.Fatalf("pending writer frame=%T", written)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending control was not written")
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("cancelled session = %v", err)
	}
	if handler.pendingCalls != 1 || handler.handleCalls != 0 {
		t.Fatalf("handler calls pending=%d handle=%d", handler.pendingCalls, handler.handleCalls)
	}
	if confirm.Signature != "" {
		t.Fatal("confirmation signature scratch survived writer")
	}
}

func TestSessionWriterClearsQueuedControlScratchOnCancelAndWriteFailure(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		socket SessionSocket
	}{
		{name: "cancel", socket: &blockingWriteControlSocket{started: make(chan struct{})}},
		{name: "write_failure", socket: &failingWriteControlSocket{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
			first := &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, sessionTestChallengeID, now), RotationID: sessionTestSubscriptionID, Signature: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, protocol.SignatureBytes))}
			second := &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, sessionTestReadyID, now), RotationID: sessionTestSubscriptionID, Signature: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, protocol.SignatureBytes))}
			input := make(chan protocol.Frame, 2)
			input <- first
			input <- second
			failures := make(chan error, 1)
			done := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			transport := &SessionTransport{config: DefaultSessionTransportConfig()}
			session := &activeControllerSession{transport: transport, conn: testCase.socket, maxEnvelopeBytes: protocol.DefaultMaxEnvelopeBytes}
			go session.writeLoop(ctx, input, failures, done)
			if blocking, ok := testCase.socket.(*blockingWriteControlSocket); ok {
				select {
				case <-blocking.started:
				case <-time.After(2 * time.Second):
					t.Fatal("writer did not start blocked write")
				}
				cancel()
			} else {
				select {
				case <-failures:
					cancel()
				case <-time.After(2 * time.Second):
					t.Fatal("writer did not report failure")
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("writer did not stop")
			}
			if first.Signature != "" || second.Signature != "" {
				t.Fatalf("queued signature scratch survived: first=%t second=%t", first.Signature != "", second.Signature != "")
			}
		})
	}
}

type fakeSessionControlHandler struct {
	pending      []protocol.Frame
	pendingCalls int
	handleCalls  int
	handleResult SessionControlResult
	handleErr    error
	persisted    bool
}

func (handler *fakeSessionControlHandler) Pending(_ context.Context, _ SessionControlContext, _ int) ([]protocol.Frame, error) {
	handler.pendingCalls++
	return append([]protocol.Frame(nil), handler.pending...), nil
}

func (handler *fakeSessionControlHandler) Handle(_ context.Context, _ SessionControlContext, _ protocol.Frame) (SessionControlResult, error) {
	handler.handleCalls++
	if handler.handleErr != nil {
		return SessionControlResult{}, handler.handleErr
	}
	handler.persisted = true
	return handler.handleResult, nil
}

type blockingControlSocket struct {
	written chan protocol.Frame
}

func (socket *blockingControlSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (socket *blockingControlSocket) Write(_ context.Context, messageType websocket.MessageType, data []byte) error {
	if messageType != websocket.MessageText {
		return errors.New("unexpected binary control frame")
	}
	frame, err := protocol.Decode(data, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		return err
	}
	socket.written <- frame
	return nil
}
func (*blockingControlSocket) Close(websocket.StatusCode, string) error { return nil }
func (*blockingControlSocket) CloseNow() error                          { return nil }
func (*blockingControlSocket) SetReadLimit(int64)                       {}
func (*blockingControlSocket) Subprotocol() string                      { return protocol.Subprotocol }

type blockingWriteControlSocket struct{ started chan struct{} }

func (*blockingWriteControlSocket) Read(context.Context) (websocket.MessageType, []byte, error) {
	return 0, nil, errors.New("unused")
}
func (socket *blockingWriteControlSocket) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	close(socket.started)
	<-ctx.Done()
	return ctx.Err()
}
func (*blockingWriteControlSocket) Close(websocket.StatusCode, string) error { return nil }
func (*blockingWriteControlSocket) CloseNow() error                          { return nil }
func (*blockingWriteControlSocket) SetReadLimit(int64)                       {}
func (*blockingWriteControlSocket) Subprotocol() string                      { return protocol.Subprotocol }

type failingWriteControlSocket struct{}

func (*failingWriteControlSocket) Read(context.Context) (websocket.MessageType, []byte, error) {
	return 0, nil, errors.New("unused")
}
func (*failingWriteControlSocket) Write(context.Context, websocket.MessageType, []byte) error {
	return errors.New("forced write failure")
}
func (*failingWriteControlSocket) Close(websocket.StatusCode, string) error { return nil }
func (*failingWriteControlSocket) CloseNow() error                          { return nil }
func (*failingWriteControlSocket) SetReadLimit(int64)                       {}
func (*failingWriteControlSocket) Subprotocol() string                      { return protocol.Subprotocol }

type fakeSessionTransportStore struct {
	identity   ControllerIdentity
	key        ControllerKey
	candidates []ControllerKey
	ack        []protocol.ACKState
	// A non-nil slice overrides the default single test subscription, including
	// with a valid empty full set.
	subscriptions []protocol.Subscription

	prepareMessageID string
	ackTarget        string
	acknowledged     uint64

	sourceDecision   InboxDecision
	commitSourceErr  error
	sourceCommits    int
	sourceController string
	accessController string
	accessCommits    int
	sourceBlock      bool
	sourceStarted    chan struct{}
	sourceStartOnce  sync.Once
}

func (store *fakeSessionTransportStore) SessionAuthenticationCandidates(context.Context) (ControllerIdentity, []ControllerKey, error) {
	if store.candidates != nil {
		return store.identity, append([]ControllerKey(nil), store.candidates...), nil
	}
	return store.identity, []ControllerKey{store.key}, nil
}
func (store *fakeSessionTransportStore) DurableACKState(context.Context, string) ([]protocol.ACKState, error) {
	result := make([]protocol.ACKState, len(store.ack))
	copy(result, store.ack)
	return result, nil
}
func (store *fakeSessionTransportStore) PrepareSubscriptionSync(_ context.Context, controllerID, messageID string, sentAt time.Time) (SyncSnapshot, error) {
	store.prepareMessageID = messageID
	items := []protocol.Subscription{{SubscriptionID: sessionTestSubscriptionID, InstallationID: 7, RepositoryID: 8, Ref: "refs/heads/main"}}
	if store.subscriptions != nil {
		items = append([]protocol.Subscription{}, store.subscriptions...)
	}
	digest := sha256.Sum256([]byte("test snapshot"))
	return SyncSnapshot{ControllerID: controllerID, Generation: 1, MessageID: messageID, SentAt: sentAt, Digest: digest, State: SyncInflight, Items: items}, nil
}
func (store *fakeSessionTransportStore) AcknowledgeSubscriptionSync(_ context.Context, _ string, target string, generation uint64, _ uint32, _ time.Time) error {
	store.ackTarget = target
	store.acknowledged = generation
	return nil
}
func (store *fakeSessionTransportStore) CommitSourceDesired(ctx context.Context, controllerID string, _ protocol.SourceDesired, _ time.Time) (InboxDecision, error) {
	store.sourceCommits++
	store.sourceController = controllerID
	if store.sourceBlock {
		store.sourceStartOnce.Do(func() { close(store.sourceStarted) })
		<-ctx.Done()
		return InboxDecision{}, ctx.Err()
	}
	if store.commitSourceErr != nil {
		return InboxDecision{}, store.commitSourceErr
	}
	return store.sourceDecision, nil
}
func (store *fakeSessionTransportStore) CommitAccessChange(_ context.Context, controllerID string, _ protocol.AccessChange, _ time.Time) (InboxDecision, error) {
	store.accessCommits++
	store.accessController = controllerID
	return AckDecision(), nil
}

type fakeSessionTransportCredentials struct {
	bundle ControllerKeyBundle
	reads  int
}

func (credentials *fakeSessionTransportCredentials) ReadControllerKey(_, _ string, _ []byte) (ControllerKeyBundle, error) {
	credentials.reads++
	if len(credentials.bundle.PrivateKey) == 0 {
		return ControllerKeyBundle{}, errors.New("credential unavailable")
	}
	return credentials.bundle, nil
}

type scriptedSessionSocket struct {
	mu          sync.Mutex
	subprotocol string
	readLimit   int64
	reads       []protocol.Frame
	writes      []protocol.Frame
	onWrite     func(protocol.Frame) error
	closed      bool
}

func (socket *scriptedSessionSocket) enqueue(frame protocol.Frame) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.reads = append(socket.reads, frame)
}

func (socket *scriptedSessionSocket) Read(context.Context) (websocket.MessageType, []byte, error) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	if len(socket.reads) == 0 {
		return 0, nil, errors.New("no scripted relay frame")
	}
	frame := socket.reads[0]
	socket.reads = socket.reads[1:]
	encoded, err := protocol.Encode(frame, protocol.DefaultMaxEnvelopeBytes)
	return websocket.MessageText, encoded, err
}

func (socket *scriptedSessionSocket) Write(_ context.Context, messageType websocket.MessageType, data []byte) error {
	if messageType != websocket.MessageText {
		return errors.New("binary controller frame")
	}
	frame, err := protocol.Decode(data, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		return err
	}
	socket.mu.Lock()
	socket.writes = append(socket.writes, frame)
	socket.mu.Unlock()
	if socket.onWrite != nil {
		return socket.onWrite(frame)
	}
	return nil
}
func (socket *scriptedSessionSocket) Close(websocket.StatusCode, string) error {
	socket.closed = true
	return nil
}
func (socket *scriptedSessionSocket) CloseNow() error {
	socket.closed = true
	return nil
}
func (socket *scriptedSessionSocket) SetReadLimit(limit int64) { socket.readLimit = limit }
func (socket *scriptedSessionSocket) Subprotocol() string      { return socket.subprotocol }

func deterministicSessionPrivateKey(fill byte) ed25519.PrivateKey {
	seed := bytes.Repeat([]byte{fill}, ed25519.SeedSize)
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed)
}

func sessionErrorCode(err error, code string) bool {
	var sessionErr *SessionTransportError
	return errors.As(err, &sessionErr) && sessionErr.Code == code
}

func readSessionTestFrame(ctx context.Context, connection *websocket.Conn) (protocol.Frame, error) {
	messageType, data, err := connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	if messageType != websocket.MessageText {
		return nil, errors.New("binary relay test frame")
	}
	return protocol.Decode(data, protocol.DefaultMaxEnvelopeBytes)
}

func writeSessionTestFrame(ctx context.Context, connection *websocket.Conn, frame protocol.Frame) error {
	data, err := protocol.Encode(frame, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, data)
}
