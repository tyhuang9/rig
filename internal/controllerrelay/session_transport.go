package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	sessionErrorRelayUnavailable     = "relay_unavailable"
	sessionErrorConnectionClosed     = "connection_closed"
	sessionErrorProtocol             = "protocol_error"
	sessionErrorPersistence          = "persistence_unavailable"
	sessionErrorCredential           = "credential_unavailable"
	sessionErrorIdentity             = "identity_unavailable"
	sessionErrorQueueSaturated       = "queue_saturated"
	sessionErrorExpired              = "session_expired"
	defaultSessionIdleMultiplier     = 3
	maximumSessionTransportQueueSize = 1000
)

var errSessionExpired = errors.New("controller relay session expired")

type SessionTransportError struct {
	Code  string
	Fatal bool
}

func (err *SessionTransportError) Error() string {
	if err == nil {
		return "controller relay session failed"
	}
	return "controller relay session failed: " + safeSessionErrorCode(err.Code)
}

func (err *SessionTransportError) String() string   { return err.Error() }
func (err *SessionTransportError) GoString() string { return err.Error() }
func (err *SessionTransportError) LogValue() slog.Value {
	if err == nil {
		return slog.GroupValue(slog.String("code", sessionErrorRelayUnavailable))
	}
	return slog.GroupValue(slog.String("code", safeSessionErrorCode(err.Code)), slog.Bool("fatal", err.Fatal))
}

func safeSessionErrorCode(code string) string {
	switch code {
	case sessionErrorRelayUnavailable, sessionErrorConnectionClosed, sessionErrorProtocol,
		sessionErrorPersistence, sessionErrorCredential, sessionErrorIdentity,
		sessionErrorQueueSaturated, sessionErrorExpired:
		return code
	default:
		return sessionErrorProtocol
	}
}

type SessionSocket interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
	CloseNow() error
	SetReadLimit(int64)
	Subprotocol() string
}

type SessionDialFunc func(context.Context, string, *websocket.DialOptions) (SessionSocket, *http.Response, error)

type SessionTicker interface {
	C() <-chan time.Time
	Stop()
}

type SessionTimer interface {
	C() <-chan time.Time
	Stop()
}

type SessionTimerSource interface {
	NewTicker(time.Duration) SessionTicker
	NewTimer(time.Duration) SessionTimer
}

type sessionTransportStore interface {
	SessionAuthenticationCandidates(context.Context) (ControllerIdentity, []ControllerKey, error)
	DurableACKState(context.Context, string) ([]protocol.ACKState, error)
	PrepareSubscriptionSync(context.Context, string, string, time.Time) (SyncSnapshot, error)
	AcknowledgeSubscriptionSync(context.Context, string, string, uint64, uint32, time.Time) error
	CommitSourceDesired(context.Context, string, protocol.SourceDesired, time.Time) (InboxDecision, error)
	CommitAccessChange(context.Context, string, protocol.AccessChange, time.Time) (InboxDecision, error)
}

type SessionControlHandler interface {
	Pending(context.Context, SessionControlContext, int) ([]protocol.Frame, error)
	Handle(context.Context, SessionControlContext, protocol.Frame) (SessionControlResult, error)
}

type sessionTransportCredentials interface {
	ReadControllerKey(controllerID, keyID string, expectedPublicKey []byte) (ControllerKeyBundle, error)
}

type SessionTransportConfig struct {
	HandshakeTimeout     time.Duration
	WriteTimeout         time.Duration
	HandshakeMaxBytes    int
	MaxEnvelopeBytes     int
	MaxSubscriptions     int
	MaxOutstanding       int
	MaxChallengeLifetime time.Duration
	MaxSessionLifetime   time.Duration
	PersistenceTimeout   time.Duration
	Now                  func() time.Time
	Entropy              io.Reader
	Timers               SessionTimerSource
	ControlHandler       SessionControlHandler
}

func DefaultSessionTransportConfig() SessionTransportConfig {
	return SessionTransportConfig{
		HandshakeTimeout:     10 * time.Second,
		WriteTimeout:         10 * time.Second,
		HandshakeMaxBytes:    256 << 10,
		MaxEnvelopeBytes:     protocol.DefaultMaxEnvelopeBytes,
		MaxSubscriptions:     protocol.MaxArrayItems,
		MaxOutstanding:       256,
		MaxChallengeLifetime: 5 * time.Minute,
		MaxSessionLifetime:   24 * time.Hour,
		PersistenceTimeout:   10 * time.Second,
		Now:                  time.Now,
		Entropy:              rand.Reader,
		Timers:               realSessionTimerSource{},
	}
}

type SessionTransport struct {
	store       sessionTransportStore
	credentials sessionTransportCredentials
	dial        SessionDialFunc
	httpClient  *http.Client
	sessionURL  string
	config      SessionTransportConfig
	issuer      protocol.Issuer
}

func NewSessionTransport(rawOrigin string, store sessionTransportStore, credentials sessionTransportCredentials, transport http.RoundTripper, dial SessionDialFunc, config SessionTransportConfig) (*SessionTransport, error) {
	origin, err := parseCanonicalHTTPSOrigin(rawOrigin)
	if err != nil || store == nil || credentials == nil || !validSessionTransportConfig(config) {
		return nil, errors.New("invalid controller relay session configuration")
	}
	sessionURL, err := relayControllerSessionURL(origin)
	if err != nil {
		return nil, errors.New("invalid controller relay session configuration")
	}
	if dial == nil {
		dial = defaultSessionDial
	}
	return &SessionTransport{
		store:       store,
		credentials: credentials,
		dial:        dial,
		httpClient:  newRelayHTTPClient(transport, 0),
		sessionURL:  sessionURL,
		config:      config,
		issuer:      protocol.Issuer{Entropy: config.Entropy, Now: config.Now},
	}, nil
}

func (transport *SessionTransport) RunOnce(ctx context.Context) error {
	if transport == nil || ctx == nil {
		return sessionFailure(sessionErrorIdentity, true)
	}
	active, err := transport.connect(ctx)
	if err != nil {
		return err
	}
	defer active.closeNow()
	return active.run(ctx)
}

type activeControllerSession struct {
	transport         *SessionTransport
	conn              SessionSocket
	controllerID      string
	keyID             string
	sessionID         string
	maxEnvelopeBytes  int
	maxOutstanding    int
	heartbeatInterval time.Duration
	expiresAt         time.Time
}

func (transport *SessionTransport) connect(ctx context.Context) (_ *activeControllerSession, resultErr error) {
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, transport.config.HandshakeTimeout)
	defer cancelHandshake()
	identity, candidates, err := transport.store.SessionAuthenticationCandidates(handshakeCtx)
	if err != nil {
		return nil, sessionFailure(sessionErrorIdentity, true)
	}
	if identity.State != ControllerActive || len(candidates) < 1 || len(candidates) > 2 {
		return nil, sessionFailure(sessionErrorIdentity, true)
	}
	ackState, err := transport.store.DurableACKState(handshakeCtx, identity.ControllerID)
	if err != nil || protocol.ValidateACKState(ackState) != nil {
		return nil, sessionFailure(sessionErrorPersistence, true)
	}
	for index, key := range candidates {
		if (key.State != KeyActive && key.State != KeyPending) || identity.ControllerID != key.ControllerID || index > 0 && key.State != KeyPending {
			return nil, sessionFailure(sessionErrorIdentity, true)
		}
		active, attemptErr := transport.connectCandidate(handshakeCtx, identity, key, ackState)
		if attemptErr == nil {
			return active, nil
		}
		resultErr = attemptErr
		if index+1 == len(candidates) || !retryAuthenticationCandidate(attemptErr) || handshakeCtx.Err() != nil {
			return nil, attemptErr
		}
	}
	return nil, resultErr
}

func (transport *SessionTransport) connectCandidate(handshakeCtx context.Context, identity ControllerIdentity, key ControllerKey, ackState []protocol.ACKState) (_ *activeControllerSession, resultErr error) {
	hello, err := transport.issuer.NewHello(identity.ControllerID, key.KeyID, ackState)
	if err != nil {
		return nil, sessionFailure(sessionErrorIdentity, true)
	}

	conn, _, err := transport.dial(handshakeCtx, transport.sessionURL, &websocket.DialOptions{
		HTTPClient:      transport.httpClient,
		Subprotocols:    []string{protocol.Subprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil || conn == nil {
		return nil, sessionFailure(sessionErrorRelayUnavailable, false)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = conn.CloseNow()
		}
	}()
	if conn.Subprotocol() != protocol.Subprotocol {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	conn.SetReadLimit(int64(transport.config.HandshakeMaxBytes))
	if err = transport.writeHandshake(handshakeCtx, conn, hello); err != nil {
		return nil, err
	}
	frame, err := transport.readHandshake(handshakeCtx, conn)
	if err != nil {
		return nil, err
	}
	challenge, ok := frame.(*protocol.Challenge)
	if !ok || !transport.validChallenge(challenge, hello, ackState) {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}

	bundle, err := transport.credentials.ReadControllerKey(identity.ControllerID, key.KeyID, key.PublicKey)
	if err != nil {
		return nil, sessionFailure(sessionErrorCredential, true)
	}
	if bundle.Version != credentialVersion || bundle.ControllerID != identity.ControllerID || bundle.KeyID != key.KeyID || len(bundle.PrivateKey) != ed25519.PrivateKeySize || !bytes.Equal(bundle.PublicKey, key.PublicKey) {
		bundle.Destroy()
		return nil, sessionFailure(sessionErrorCredential, true)
	}
	transcript, err := protocol.WSSAuthenticationTranscript(protocol.AuthenticationBinding{
		ControllerID: identity.ControllerID,
		KeyID:        key.KeyID,
		SessionID:    challenge.SessionID,
		ClientNonce:  hello.ClientNonce,
		ServerNonce:  challenge.ServerNonce,
		ACKDigest:    challenge.ACKDigest,
		ExpiresAt:    challenge.ExpiresAt,
	})
	if err != nil {
		bundle.Destroy()
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	signature := ed25519.Sign(bundle.PrivateKey, transcript)
	bundle.Destroy()
	clear(transcript)
	messageID, err := transport.messageID()
	if err != nil {
		clear(signature)
		return nil, sessionFailure(sessionErrorIdentity, true)
	}
	authenticate := &protocol.Authenticate{
		Envelope:  protocol.NewEnvelope(protocol.TypeAuthenticate, messageID, transport.now()),
		SessionID: challenge.SessionID,
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	}
	clear(signature)
	if err = transport.writeHandshake(handshakeCtx, conn, authenticate); err != nil {
		authenticate.Signature = ""
		return nil, err
	}
	authenticate.Signature = ""
	frame, err = transport.readHandshake(handshakeCtx, conn)
	if err != nil {
		return nil, err
	}
	ready, ok := frame.(*protocol.Ready)
	if !ok || !transport.validReady(ready, challenge.SessionID) {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	maxEnvelope := minInt(transport.config.MaxEnvelopeBytes, int(ready.MaxEnvelopeBytes))
	conn.SetReadLimit(int64(maxEnvelope))

	syncMessageID, err := transport.messageID()
	if err != nil {
		return nil, sessionFailure(sessionErrorIdentity, true)
	}
	snapshot, err := transport.store.PrepareSubscriptionSync(handshakeCtx, identity.ControllerID, syncMessageID, transport.now())
	if err != nil {
		return nil, sessionFailure(sessionErrorPersistence, true)
	}
	if len(snapshot.Items) > transport.config.MaxSubscriptions || len(snapshot.Items) > int(ready.MaxSubscriptions) || snapshot.State != SyncInflight || snapshot.ControllerID != identity.ControllerID {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	syncFrame := &protocol.SubscriptionsSync{
		Envelope:      protocol.NewEnvelope(protocol.TypeSubscriptionsSync, snapshot.MessageID, snapshot.SentAt),
		Generation:    snapshot.Generation,
		Subscriptions: append([]protocol.Subscription{}, snapshot.Items...),
	}
	if err = transport.writeFrame(handshakeCtx, conn, syncFrame, maxEnvelope); err != nil {
		return nil, err
	}
	frame, err = transport.readFrameWithTimeout(handshakeCtx, conn, transport.config.HandshakeTimeout, maxEnvelope)
	if err != nil {
		return nil, err
	}
	synced, ok := frame.(*protocol.SubscriptionsSynced)
	if !ok || synced.TargetMessageID != snapshot.MessageID || synced.Generation != snapshot.Generation || synced.AcceptedCount != uint32(len(snapshot.Items)) {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	if err = transport.store.AcknowledgeSubscriptionSync(handshakeCtx, identity.ControllerID, synced.TargetMessageID, synced.Generation, synced.AcceptedCount, transport.now()); err != nil {
		return nil, sessionFailure(sessionErrorPersistence, true)
	}

	succeeded = true
	return &activeControllerSession{
		transport:         transport,
		conn:              conn,
		controllerID:      identity.ControllerID,
		keyID:             key.KeyID,
		sessionID:         ready.SessionID,
		maxEnvelopeBytes:  maxEnvelope,
		maxOutstanding:    minInt(transport.config.MaxOutstanding, int(ready.MaxOutstanding)),
		heartbeatInterval: time.Duration(ready.HeartbeatIntervalSeconds) * time.Second,
		expiresAt:         ready.SessionExpiresAt.UTC(),
	}, nil
}

func (session *activeControllerSession) run(ctx context.Context) error {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	defer session.closeNow()
	expiresIn := session.expiresAt.Sub(session.transport.now())
	if expiresIn <= 0 {
		return sessionFailure(sessionErrorExpired, false)
	}
	expiry := session.transport.config.Timers.NewTimer(expiresIn)
	defer expiry.Stop()
	expiryDone := make(chan struct{})
	go func() {
		defer close(expiryDone)
		select {
		case <-expiry.C():
			cancel(errSessionExpired)
		case <-runCtx.Done():
		}
	}()

	reads := make(chan sessionRead, 1)
	readerDone := make(chan struct{})
	go session.readLoop(runCtx, reads, readerDone)

	writes := make(chan protocol.Frame, maxInt(1, session.maxOutstanding))
	writeErrors := make(chan error, 1)
	writerDone := make(chan struct{})
	go session.writeLoop(runCtx, writes, writeErrors, writerDone)
	finish := func(err error) error {
		cancel(context.Canceled)
		_ = session.conn.CloseNow()
		<-readerDone
		<-writerDone
		<-expiryDone
		return err
	}
	if session.transport.config.ControlHandler != nil {
		pendingCtx, cancelPending := context.WithTimeout(runCtx, session.transport.config.PersistenceTimeout)
		pending, err := session.transport.config.ControlHandler.Pending(pendingCtx, session.controlContext(), minInt(session.maxOutstanding, session.transport.config.MaxOutstanding))
		cancelPending()
		if err != nil {
			destroySessionControlFrames(pending)
			return finish(controlSessionFailure(err))
		}
		for index, frame := range pending {
			if !enqueueSessionFrame(writes, frame) {
				destroySessionControlFrames(pending[index:])
				return finish(sessionFailure(sessionErrorQueueSaturated, false))
			}
		}
	}

	heartbeat := session.transport.config.Timers.NewTicker(session.heartbeatInterval)
	defer heartbeat.Stop()
	lastSeen := session.transport.now()
	var inboundHeartbeat, outboundHeartbeat uint64
	for {
		select {
		case <-runCtx.Done():
			if errors.Is(context.Cause(runCtx), errSessionExpired) {
				return finish(sessionFailure(sessionErrorExpired, false))
			}
			return finish(nil)
		case err := <-writeErrors:
			if err == nil {
				err = sessionFailure(sessionErrorConnectionClosed, false)
			}
			return finish(err)
		case event := <-reads:
			if event.err != nil {
				return finish(sessionFailure(sessionErrorConnectionClosed, false))
			}
			lastSeen = session.transport.now()
			remaining := session.expiresAt.Sub(lastSeen)
			if remaining <= 0 {
				return finish(sessionFailure(sessionErrorExpired, false))
			}
			persistenceTimeout := session.transport.config.PersistenceTimeout
			if remaining < persistenceTimeout {
				persistenceTimeout = remaining
			}
			inboundCtx, cancelInbound := context.WithTimeout(runCtx, persistenceTimeout)
			response, nextHeartbeat, action, err := session.handleInboundWithAction(inboundCtx, event.frame, inboundHeartbeat)
			cancelInbound()
			if errors.Is(context.Cause(runCtx), errSessionExpired) || !session.expiresAt.After(session.transport.now()) {
				return finish(sessionFailure(sessionErrorExpired, false))
			}
			if ctx.Err() != nil {
				return finish(nil)
			}
			if err != nil {
				return finish(err)
			}
			inboundHeartbeat = nextHeartbeat
			if response != nil && !enqueueSessionFrame(writes, response) {
				destroySessionControlFrame(response)
				return finish(sessionFailure(sessionErrorQueueSaturated, false))
			}
			switch action {
			case ControlContinue:
			case ControlReconnect:
				return finish(sessionFailure(sessionErrorConnectionClosed, false))
			case ControlStop:
				return finish(sessionFailure(sessionErrorIdentity, true))
			default:
				return finish(sessionFailure(sessionErrorProtocol, true))
			}
		case <-heartbeat.C():
			now := session.transport.now()
			if !session.expiresAt.After(now) {
				return finish(sessionFailure(sessionErrorExpired, false))
			}
			if now.Sub(lastSeen) > time.Duration(defaultSessionIdleMultiplier)*session.heartbeatInterval {
				return finish(sessionFailure(sessionErrorConnectionClosed, false))
			}
			outboundHeartbeat++
			messageID, err := session.transport.messageID()
			if err != nil {
				return finish(sessionFailure(sessionErrorIdentity, true))
			}
			frame := &protocol.Heartbeat{Envelope: protocol.NewEnvelope(protocol.TypeHeartbeat, messageID, now), Sequence: outboundHeartbeat}
			if !enqueueSessionFrame(writes, frame) {
				return finish(sessionFailure(sessionErrorQueueSaturated, false))
			}
		}
	}
}

func (session *activeControllerSession) handleInbound(ctx context.Context, frame protocol.Frame, heartbeat uint64) (protocol.Frame, uint64, error) {
	response, nextHeartbeat, _, err := session.handleInboundWithAction(ctx, frame, heartbeat)
	return response, nextHeartbeat, err
}

func (session *activeControllerSession) handleInboundWithAction(ctx context.Context, frame protocol.Frame, heartbeat uint64) (protocol.Frame, uint64, SessionControlAction, error) {
	switch value := frame.(type) {
	case *protocol.SourceDesired:
		if !session.expiresAt.After(session.transport.now()) {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorExpired, false)
		}
		decision, err := session.transport.store.CommitSourceDesired(ctx, session.controllerID, *value, session.transport.now())
		if err != nil {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorPersistence, true)
		}
		if !session.expiresAt.After(session.transport.now()) {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorExpired, false)
		}
		response, err := session.sourceDecision(value, decision)
		return response, heartbeat, ControlContinue, err
	case *protocol.AccessChange:
		if !session.expiresAt.After(session.transport.now()) {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorExpired, false)
		}
		decision, err := session.transport.store.CommitAccessChange(ctx, session.controllerID, *value, session.transport.now())
		if err != nil {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorPersistence, true)
		}
		if !session.expiresAt.After(session.transport.now()) {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorExpired, false)
		}
		response, err := session.accessDecision(value, decision)
		return response, heartbeat, ControlContinue, err
	case *protocol.Heartbeat:
		if value.Sequence <= heartbeat {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorProtocol, true)
		}
		return nil, value.Sequence, ControlContinue, nil
	case *protocol.ProtocolError:
		return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorProtocol, value.Fatal)
	default:
		if session.transport.config.ControlHandler == nil {
			return nil, heartbeat, ControlContinue, sessionFailure(sessionErrorProtocol, true)
		}
		result, err := session.transport.config.ControlHandler.Handle(ctx, session.controlContext(), frame)
		if err != nil {
			return nil, heartbeat, result.Action, controlSessionFailure(err)
		}
		return result.Response, heartbeat, result.Action, nil
	}
}

func (session *activeControllerSession) controlContext() SessionControlContext {
	return SessionControlContext{ControllerID: session.controllerID, KeyID: session.keyID, SessionID: session.sessionID, ExpiresAt: session.expiresAt}
}

func (session *activeControllerSession) sourceDecision(source *protocol.SourceDesired, decision InboxDecision) (protocol.Frame, error) {
	envelope, err := session.transport.envelope(protocol.TypeAck)
	if err != nil {
		return nil, sessionFailure(sessionErrorIdentity, true)
	}
	target := &protocol.SourceTarget{SubscriptionID: source.SubscriptionID, Generation: source.Generation}
	switch decision.Kind {
	case DecisionAck:
		return &protocol.Ack{Envelope: envelope, TargetMessageID: source.MessageID, Source: target}, nil
	case DecisionReject:
		envelope.Type = protocol.TypeReject
		return &protocol.Reject{Envelope: envelope, TargetMessageID: source.MessageID, Source: target, Code: decision.Code}, nil
	default:
		return nil, sessionFailure(sessionErrorPersistence, true)
	}
}

func (session *activeControllerSession) accessDecision(change *protocol.AccessChange, decision InboxDecision) (protocol.Frame, error) {
	envelope, err := session.transport.envelope(protocol.TypeAck)
	if err != nil {
		return nil, sessionFailure(sessionErrorIdentity, true)
	}
	target := &protocol.AccessTarget{EventID: change.EventID}
	switch decision.Kind {
	case DecisionAck:
		return &protocol.Ack{Envelope: envelope, TargetMessageID: change.MessageID, Access: target}, nil
	case DecisionReject:
		envelope.Type = protocol.TypeReject
		return &protocol.Reject{Envelope: envelope, TargetMessageID: change.MessageID, Access: target, Code: decision.Code}, nil
	default:
		return nil, sessionFailure(sessionErrorPersistence, true)
	}
}

type sessionRead struct {
	frame protocol.Frame
	err   error
}

func (session *activeControllerSession) readLoop(ctx context.Context, output chan<- sessionRead, done chan<- struct{}) {
	defer close(done)
	for {
		frame, err := session.transport.readFrame(ctx, session.conn, session.maxEnvelopeBytes)
		select {
		case output <- sessionRead{frame: frame, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (session *activeControllerSession) writeLoop(ctx context.Context, input <-chan protocol.Frame, failures chan<- error, done chan<- struct{}) {
	defer func() {
		drainSessionControlFrames(input)
		close(done)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-input:
			if !ok {
				return
			}
			if err := session.transport.writeFrame(ctx, session.conn, frame, session.maxEnvelopeBytes); err != nil {
				destroySessionControlFrame(frame)
				select {
				case failures <- err:
				default:
				}
				<-ctx.Done()
				return
			}
			destroySessionControlFrame(frame)
		}
	}
}

func drainSessionControlFrames(input <-chan protocol.Frame) {
	for {
		select {
		case frame, ok := <-input:
			if !ok {
				return
			}
			destroySessionControlFrame(frame)
		default:
			return
		}
	}
}

func (transport *SessionTransport) readHandshake(ctx context.Context, conn SessionSocket) (protocol.Frame, error) {
	return transport.readFrameWithTimeout(ctx, conn, transport.config.HandshakeTimeout, transport.config.HandshakeMaxBytes)
}

func (transport *SessionTransport) readFrameWithTimeout(ctx context.Context, conn SessionSocket, timeout time.Duration, maximum int) (protocol.Frame, error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return transport.readFrame(readCtx, conn, maximum)
}

func (transport *SessionTransport) readFrame(ctx context.Context, conn SessionSocket, maximum int) (protocol.Frame, error) {
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		clear(data)
		return nil, sessionFailure(sessionErrorConnectionClosed, false)
	}
	defer clear(data)
	if messageType != websocket.MessageText || len(data) == 0 || len(data) > maximum {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	frame, err := protocol.Decode(data, maximum)
	if err != nil || !relayToControllerFrame(frame) {
		return nil, sessionFailure(sessionErrorProtocol, true)
	}
	return frame, nil
}

func (transport *SessionTransport) writeHandshake(ctx context.Context, conn SessionSocket, frame protocol.Frame) error {
	return transport.writeFrame(ctx, conn, frame, transport.config.HandshakeMaxBytes)
}

func (transport *SessionTransport) writeFrame(ctx context.Context, conn SessionSocket, frame protocol.Frame, maximum int) error {
	data, err := protocol.Encode(frame, maximum)
	if err != nil {
		clear(data)
		return sessionFailure(sessionErrorProtocol, true)
	}
	defer clear(data)
	writeCtx, cancel := context.WithTimeout(ctx, transport.config.WriteTimeout)
	defer cancel()
	if err = conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return sessionFailure(sessionErrorConnectionClosed, false)
	}
	return nil
}

func (transport *SessionTransport) validChallenge(challenge *protocol.Challenge, hello *protocol.Hello, state []protocol.ACKState) bool {
	if challenge == nil || hello == nil {
		return false
	}
	now := transport.now()
	if !challenge.ExpiresAt.After(now) || challenge.ExpiresAt.Sub(now) > transport.config.MaxChallengeLifetime || !challenge.ExpiresAt.After(challenge.SentAt) || challenge.ExpiresAt.Sub(challenge.SentAt) > transport.config.MaxChallengeLifetime || challenge.SentAt.After(now.Add(transport.config.MaxChallengeLifetime)) || now.Sub(challenge.SentAt) > transport.config.MaxChallengeLifetime {
		return false
	}
	expected, err := protocol.CanonicalACKDigest(state)
	if err != nil {
		return false
	}
	actual, err := base64.RawURLEncoding.DecodeString(challenge.ACKDigest)
	if err != nil || len(actual) != len(expected) {
		clear(actual)
		return false
	}
	defer clear(actual)
	return subtle.ConstantTimeCompare(expected[:], actual) == 1
}

func (transport *SessionTransport) validReady(ready *protocol.Ready, sessionID string) bool {
	if ready == nil || ready.SessionID != sessionID {
		return false
	}
	now := transport.now()
	return ready.SessionExpiresAt.After(now) && ready.SessionExpiresAt.Sub(now) <= transport.config.MaxSessionLifetime
}

func (transport *SessionTransport) envelope(messageType protocol.MessageType) (protocol.Envelope, error) {
	messageID, err := transport.messageID()
	if err != nil {
		return protocol.Envelope{}, err
	}
	return protocol.NewEnvelope(messageType, messageID, transport.now()), nil
}

func (transport *SessionTransport) messageID() (string, error) {
	value, err := uuid.NewRandomFromReader(transport.config.Entropy)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

func (transport *SessionTransport) now() time.Time { return transport.config.Now().UTC() }

func (session *activeControllerSession) closeNow() {
	if session != nil && session.conn != nil {
		_ = session.conn.CloseNow()
	}
}

func relayToControllerFrame(frame protocol.Frame) bool {
	switch frame.(type) {
	case *protocol.Challenge, *protocol.Ready, *protocol.SubscriptionsSynced, *protocol.SourceDesired,
		*protocol.AccessChange, *protocol.Heartbeat, *protocol.ProtocolError, *protocol.BindingRemoved,
		*protocol.ControllerRevoked, *protocol.KeyRevoked, *protocol.KeyRotationChallenge,
		*protocol.KeyRotationConfirmed, *protocol.KeyRotationFinalized:
		return true
	default:
		return false
	}
}

func enqueueSessionFrame(queue chan<- protocol.Frame, frame protocol.Frame) bool {
	select {
	case queue <- frame:
		return true
	default:
		return false
	}
}

func validSessionTransportConfig(config SessionTransportConfig) bool {
	return config.HandshakeTimeout > 0 && config.HandshakeTimeout <= time.Minute &&
		config.WriteTimeout > 0 && config.WriteTimeout <= time.Minute &&
		config.HandshakeMaxBytes >= 4096 && config.HandshakeMaxBytes <= 256<<10 &&
		config.MaxEnvelopeBytes >= config.HandshakeMaxBytes && config.MaxEnvelopeBytes <= protocol.DefaultMaxEnvelopeBytes &&
		config.MaxSubscriptions > 0 && config.MaxSubscriptions <= protocol.MaxArrayItems &&
		config.MaxOutstanding > 0 && config.MaxOutstanding <= maximumSessionTransportQueueSize &&
		config.MaxChallengeLifetime >= config.HandshakeTimeout && config.MaxChallengeLifetime <= 5*time.Minute &&
		config.MaxSessionLifetime > 0 && config.MaxSessionLifetime <= 24*time.Hour &&
		config.PersistenceTimeout > 0 && config.PersistenceTimeout <= time.Minute &&
		config.Now != nil && config.Entropy != nil && config.Timers != nil
}

func defaultSessionDial(ctx context.Context, target string, options *websocket.DialOptions) (SessionSocket, *http.Response, error) {
	connection, response, err := websocket.Dial(ctx, target, options)
	if err != nil {
		return nil, response, err
	}
	return connection, response, nil
}

func sessionFailure(code string, fatal bool) error {
	return &SessionTransportError{Code: code, Fatal: fatal}
}

func controlSessionFailure(err error) error {
	var controlErr *SessionControlError
	if !errors.As(err, &controlErr) {
		return sessionFailure(sessionErrorProtocol, true)
	}
	switch controlErr.Code {
	case controlErrorPersistence:
		return sessionFailure(sessionErrorPersistence, true)
	case controlErrorCredential:
		return sessionFailure(sessionErrorCredential, true)
	case controlErrorExpired:
		return sessionFailure(sessionErrorExpired, false)
	case controlErrorRevoked:
		return sessionFailure(sessionErrorIdentity, true)
	default:
		return sessionFailure(sessionErrorProtocol, true)
	}
}

func retryAuthenticationCandidate(err error) bool {
	var transportErr *SessionTransportError
	if !errors.As(err, &transportErr) || transportErr.Fatal && transportErr.Code != sessionErrorProtocol {
		return false
	}
	switch transportErr.Code {
	case sessionErrorRelayUnavailable, sessionErrorConnectionClosed, sessionErrorProtocol, sessionErrorExpired:
		return true
	default:
		return false
	}
}

type realSessionTimerSource struct{}
type realSessionTicker struct{ ticker *time.Ticker }
type realSessionTimer struct{ timer *time.Timer }

func (realSessionTimerSource) NewTicker(duration time.Duration) SessionTicker {
	return &realSessionTicker{ticker: time.NewTicker(duration)}
}
func (ticker *realSessionTicker) C() <-chan time.Time { return ticker.ticker.C }
func (ticker *realSessionTicker) Stop()               { ticker.ticker.Stop() }
func (realSessionTimerSource) NewTimer(duration time.Duration) SessionTimer {
	return &realSessionTimer{timer: time.NewTimer(duration)}
}
func (timer *realSessionTimer) C() <-chan time.Time { return timer.timer.C }
func (timer *realSessionTimer) Stop()               { timer.timer.Stop() }

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
