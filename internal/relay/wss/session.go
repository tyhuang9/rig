package wss

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

type readEvent struct {
	messageType websocket.MessageType
	data        []byte
	err         error
}

type writeRequest struct {
	data []byte
	done chan error
}

type closeRequest struct {
	code   websocket.StatusCode
	reason string
	done   chan error
}

type session struct {
	handler      *Handler
	conn         socket
	issuer       protocol.Issuer
	reads        chan readEvent
	readRequests chan struct{}
	writes       chan writeRequest
	closes       chan closeRequest
	writerFailed chan error
	readerDone   chan struct{}
	writerDone   chan struct{}

	controllerID  string
	keyID         string
	sessionID     string
	lease         store.Lease
	leaseHeld     bool
	sessionUntil  time.Time
	subscriptions map[string]struct{}
	syncComplete  bool
}

func newSession(handler *Handler, conn socket) *session {
	return &session{
		handler:       handler,
		conn:          conn,
		issuer:        protocol.Issuer{Entropy: handler.entropy, Now: handler.now},
		reads:         make(chan readEvent, 1),
		readRequests:  make(chan struct{}, 1),
		writes:        make(chan writeRequest, handler.config.OutboundQueue),
		closes:        make(chan closeRequest, 1),
		writerFailed:  make(chan error, 1),
		readerDone:    make(chan struct{}),
		writerDone:    make(chan struct{}),
		subscriptions: make(map[string]struct{}),
	}
}

func (s *session) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		defer close(s.readerDone)
		s.readLoop(ctx)
	}()
	go func() {
		defer close(s.writerDone)
		s.writeLoop(ctx)
	}()

	status, reason := websocket.StatusNormalClosure, "session complete"
	if sessionErr := s.handshake(ctx); sessionErr != nil {
		status, reason = sessionErr.status, sessionErr.code
		if sessionErr.send {
			_ = s.sendProtocolError(ctx, "", sessionErr.code, true)
		}
	} else if sessionErr := s.active(ctx); sessionErr != nil {
		status, reason = sessionErr.status, sessionErr.code
		if sessionErr.send {
			_ = s.sendProtocolError(ctx, "", sessionErr.code, true)
		}
	}

	if s.leaseHeld {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), s.handler.config.StoreTimeout)
		_ = s.handler.store.ReleaseLease(releaseCtx, s.lease)
		releaseCancel()
	}
	s.requestClose(status, reason)
	cancel()
	s.joinWorkers()
}

type sessionFailure struct {
	code   string
	status websocket.StatusCode
	send   bool
}

func (e *sessionFailure) Error() string { return e.code }

func failure(code string, status websocket.StatusCode, send bool) *sessionFailure {
	return &sessionFailure{code: code, status: status, send: send}
}

func (s *session) handshake(ctx context.Context) *sessionFailure {
	helloFrame, err := s.nextControllerFrame(ctx, s.handler.config.HandshakeTimeout, s.handler.config.HandshakeMaxBytes)
	if err != nil {
		return err
	}
	hello, ok := helloFrame.(*protocol.Hello)
	if !ok {
		return failure("expected_hello", websocket.StatusPolicyViolation, true)
	}
	challenge, issueErr := s.issuer.NewChallenge(s.handler.config.ChallengeLifetime, hello.ACKState)
	if issueErr != nil {
		return failure("internal_error", websocket.StatusInternalError, true)
	}
	clientNonce, decodeErr := base64.RawURLEncoding.DecodeString(hello.ClientNonce)
	if decodeErr != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	serverNonce, decodeErr := base64.RawURLEncoding.DecodeString(challenge.ServerNonce)
	if decodeErr != nil {
		clear(clientNonce)
		return failure("internal_error", websocket.StatusInternalError, true)
	}
	ackDigest, decodeErr := base64.RawURLEncoding.DecodeString(challenge.ACKDigest)
	if decodeErr != nil {
		clear(clientNonce)
		clear(serverNonce)
		return failure("internal_error", websocket.StatusInternalError, true)
	}
	defer clear(clientNonce)
	defer clear(serverNonce)
	defer clear(ackDigest)
	challengeInput := store.ChallengeInput{SessionID: challenge.SessionID, ControllerID: hello.ControllerID, KeyID: hello.KeyID, ClientNonce: clientNonce, ServerNonce: serverNonce, ACKDigest: ackDigest, ExpiresAt: challenge.ExpiresAt}
	storeErr := s.withStore(ctx, func(storeCtx context.Context) error { return s.handler.store.CreateChallenge(storeCtx, challengeInput) })
	if storeErr != nil {
		return failure("authentication_failed", websocket.StatusPolicyViolation, true)
	}
	if sendErr := s.sendFrame(ctx, challenge); sendErr != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}

	authFrame, authFailure := s.nextControllerFrame(ctx, s.handler.config.HandshakeTimeout, s.handler.config.HandshakeMaxBytes)
	if authFailure != nil {
		if authFailure.code == "handshake_timeout" {
			authFailure.code = "authentication_timeout"
		}
		return authFailure
	}
	authenticate, ok := authFrame.(*protocol.Authenticate)
	if !ok || authenticate.SessionID != challenge.SessionID {
		return failure("authentication_failed", websocket.StatusPolicyViolation, true)
	}
	var durable store.AuthenticationChallenge
	storeErr = s.withStore(ctx, func(storeCtx context.Context) error {
		var loadErr error
		durable, loadErr = s.handler.store.LoadChallengeForAuthentication(storeCtx, challenge.SessionID)
		return loadErr
	})
	if storeErr != nil {
		return failure("authentication_failed", websocket.StatusPolicyViolation, true)
	}
	defer durable.Destroy()
	if durable.ControllerID != hello.ControllerID || durable.KeyID != hello.KeyID || durable.SessionID != challenge.SessionID || !bytes.Equal(durable.ClientNonce, challengeInput.ClientNonce) || !bytes.Equal(durable.ServerNonce, challengeInput.ServerNonce) || !bytes.Equal(durable.ACKDigest, challengeInput.ACKDigest) || !durable.ExpiresAt.Equal(challenge.ExpiresAt) || len(durable.PublicKey) != ed25519.PublicKeySize {
		return failure("authentication_failed", websocket.StatusPolicyViolation, true)
	}
	transcript, transcriptErr := protocol.WSSAuthenticationTranscript(protocol.AuthenticationBinding{
		ControllerID: durable.ControllerID,
		KeyID:        durable.KeyID,
		SessionID:    durable.SessionID,
		ClientNonce:  base64.RawURLEncoding.EncodeToString(durable.ClientNonce),
		ServerNonce:  base64.RawURLEncoding.EncodeToString(durable.ServerNonce),
		ACKDigest:    base64.RawURLEncoding.EncodeToString(durable.ACKDigest),
		ExpiresAt:    durable.ExpiresAt,
	})
	if transcriptErr != nil || !protocol.Verify(ed25519.PublicKey(durable.PublicKey), transcript, authenticate.Signature) {
		return failure("authentication_failed", websocket.StatusPolicyViolation, true)
	}
	sessionUntil := s.handler.now().UTC().Add(s.handler.config.SessionLifetime)
	storeErr = s.withStore(ctx, func(storeCtx context.Context) error {
		return s.handler.store.ConsumeChallenge(storeCtx, challenge.SessionID, sessionUntil)
	})
	if storeErr != nil {
		return failure("authentication_failed", websocket.StatusPolicyViolation, true)
	}
	var lease store.Lease
	storeErr = s.withStore(ctx, func(storeCtx context.Context) error {
		var leaseErr error
		lease, leaseErr = s.handler.store.AcquireLease(storeCtx, challenge.SessionID, s.handler.config.LeaseDuration)
		return leaseErr
	})
	if storeErr != nil {
		return failure("lease_unavailable", websocket.StatusTryAgainLater, true)
	}
	s.controllerID, s.keyID, s.sessionID, s.sessionUntil = hello.ControllerID, hello.KeyID, challenge.SessionID, sessionUntil
	s.lease, s.leaseHeld = lease, true
	// Reads are supervisor-permitted one at a time. Authentication and the
	// fenced lease are durable before the larger allowance is installed and
	// the mandatory full subscription sync receives its permit.
	s.conn.SetReadLimit(int64(s.handler.config.MaxEnvelopeBytes))
	ready, frameErr := s.readyFrame()
	if frameErr != nil || s.sendFrame(ctx, ready) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}

	syncFrame, syncFailure := s.nextControllerFrame(ctx, s.handler.config.HandshakeTimeout, s.handler.config.MaxEnvelopeBytes)
	if syncFailure != nil {
		return syncFailure
	}
	syncMessage, ok := syncFrame.(*protocol.SubscriptionsSync)
	if !ok || len(syncMessage.Subscriptions) > s.handler.config.MaxSubscriptions {
		return failure("expected_subscriptions_sync", websocket.StatusPolicyViolation, true)
	}
	command, commandErr := canonicalSessionCommand(syncMessage, s.handler.config.MaxEnvelopeBytes)
	if commandErr != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	subscriptions := make([]store.Subscription, len(syncMessage.Subscriptions))
	for index, subscription := range syncMessage.Subscriptions {
		subscriptions[index] = store.Subscription{SubscriptionID: subscription.SubscriptionID, InstallationID: subscription.InstallationID, RepositoryID: subscription.RepositoryID, Ref: subscription.Ref}
	}
	var result store.SessionCommandResult
	storeErr = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplySubscriptionsSync(storeCtx, lease, command, syncMessage.Generation, subscriptions)
		return applyErr
	})
	if storeErr != nil || result.Kind != store.ResultSubscriptionsSynced {
		return failure("subscriptions_sync_failed", websocket.StatusPolicyViolation, true)
	}
	for _, subscription := range syncMessage.Subscriptions {
		s.subscriptions[subscription.SubscriptionID] = struct{}{}
	}
	response, frameErr := s.subscriptionsSyncedFrame(syncMessage.MessageID, result.Generation, result.Count)
	if frameErr != nil || s.sendFrame(ctx, response) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}
	s.syncComplete = true
	return nil
}

func (s *session) nextControllerFrame(ctx context.Context, timeout time.Duration, maxBytes int) (protocol.Frame, *sessionFailure) {
	if readFailure := s.requestRead(ctx); readFailure != nil {
		return nil, readFailure
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case event := <-s.reads:
		if event.err != nil {
			return nil, failure("connection_closed", websocket.StatusNormalClosure, false)
		}
		if event.messageType != websocket.MessageText {
			return nil, failure("binary_not_supported", websocket.StatusUnsupportedData, true)
		}
		frame, err := protocol.Decode(event.data, maxBytes)
		clear(event.data)
		if err != nil {
			return nil, failure("invalid_frame", websocket.StatusPolicyViolation, true)
		}
		direction, ok := protocol.DirectionFor(frameType(frame))
		if !ok || (direction != protocol.ControllerToRelay && direction != protocol.Bidirectional) {
			return nil, failure("invalid_direction", websocket.StatusPolicyViolation, true)
		}
		return frame, nil
	case <-timer.C:
		return nil, failure("handshake_timeout", websocket.StatusPolicyViolation, true)
	case <-ctx.Done():
		return nil, failure("server_shutdown", websocket.StatusGoingAway, false)
	case <-s.writerFailed:
		return nil, failure("write_failed", websocket.StatusInternalError, false)
	}
}

func (s *session) requestRead(ctx context.Context) *sessionFailure {
	select {
	case s.readRequests <- struct{}{}:
		return nil
	case <-ctx.Done():
		return failure("server_shutdown", websocket.StatusGoingAway, false)
	default:
		return nil
	}
}

func frameType(frame protocol.Frame) protocol.MessageType {
	switch value := frame.(type) {
	case *protocol.Hello:
		return value.Type
	case *protocol.Authenticate:
		return value.Type
	case *protocol.SubscriptionsSync:
		return value.Type
	case *protocol.Ack:
		return value.Type
	case *protocol.Reject:
		return value.Type
	case *protocol.Heartbeat:
		return value.Type
	case *protocol.ProtocolError:
		return value.Type
	case *protocol.BindingRemove:
		return value.Type
	case *protocol.ControllerRevoke:
		return value.Type
	case *protocol.KeyRevoke:
		return value.Type
	case *protocol.KeyRotationPropose:
		return value.Type
	case *protocol.KeyRotationConfirm:
		return value.Type
	case *protocol.KeyRotationFinalize:
		return value.Type
	default:
		return ""
	}
}

func (s *session) readyFrame() (*protocol.Ready, error) {
	envelope, err := s.envelope(protocol.TypeReady)
	if err != nil {
		return nil, err
	}
	return &protocol.Ready{
		Envelope: envelope, SessionID: s.sessionID,
		HeartbeatIntervalSeconds: uint32(s.handler.config.HeartbeatInterval / time.Second),
		MaxEnvelopeBytes:         uint32(s.handler.config.MaxEnvelopeBytes), MaxSubscriptions: uint32(s.handler.config.MaxSubscriptions), MaxOutstanding: uint32(s.handler.config.MaxOutstanding),
		SessionExpiresAt: s.sessionUntil,
		Reconnect:        protocol.ReconnectPolicy{InitialDelayMillis: 500, MaximumDelayMillis: 30000, Multiplier: 2, JitterPercent: 20},
	}, nil
}

func (s *session) subscriptionsSyncedFrame(target string, generation uint64, count uint32) (*protocol.SubscriptionsSynced, error) {
	envelope, err := s.envelope(protocol.TypeSubscriptionsSynced)
	if err != nil {
		return nil, err
	}
	return &protocol.SubscriptionsSynced{Envelope: envelope, TargetMessageID: target, Generation: generation, AcceptedCount: count}, nil
}

func (s *session) sendProtocolError(ctx context.Context, target, code string, fatal bool) error {
	envelope, err := s.envelope(protocol.TypeProtocolError)
	if err != nil {
		return err
	}
	return s.sendFrame(ctx, &protocol.ProtocolError{Envelope: envelope, TargetMessageID: target, Code: code, Fatal: fatal})
}

func (s *session) envelope(messageType protocol.MessageType) (protocol.Envelope, error) {
	id, err := uuid.NewRandomFromReader(s.handler.entropy)
	if err != nil {
		return protocol.Envelope{}, err
	}
	return protocol.NewEnvelope(messageType, id.String(), s.handler.now()), nil
}

func (s *session) sendFrame(ctx context.Context, frame protocol.Frame) error {
	encoded, err := protocol.Encode(frame, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return err
	}
	request := writeRequest{data: encoded, done: make(chan error, 1)}
	select {
	case s.writes <- request:
	case <-s.writerDone:
		clear(encoded)
		return errors.New("relay WSS writer stopped")
	case <-ctx.Done():
		clear(encoded)
		return ctx.Err()
	default:
		clear(encoded)
		return errors.New("relay WSS outbound queue full")
	}
	select {
	case err = <-request.done:
		return err
	case err = <-s.writerFailed:
		if err == nil {
			err = errors.New("relay WSS writer stopped")
		}
		return err
	case <-s.writerDone:
		return errors.New("relay WSS writer stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) withStore(ctx context.Context, call func(context.Context) error) error {
	storeCtx, cancel := context.WithTimeout(ctx, s.handler.config.StoreTimeout)
	defer cancel()
	return call(storeCtx)
}

func (s *session) readLoop(ctx context.Context) {
	for {
		select {
		case <-s.readRequests:
		case <-ctx.Done():
			return
		}
		messageType, data, err := s.conn.Read(ctx)
		event := readEvent{messageType: messageType, data: data, err: err}
		select {
		case s.reads <- event:
		case <-ctx.Done():
			clear(data)
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *session) writeLoop(ctx context.Context) {
	for {
		select {
		case request := <-s.writes:
			writeCtx, cancel := context.WithTimeout(ctx, s.handler.config.WriteTimeout)
			err := s.conn.Write(writeCtx, websocket.MessageText, request.data)
			cancel()
			clear(request.data)
			request.done <- err
			if err != nil {
				select {
				case s.writerFailed <- err:
				default:
				}
				return
			}
		case request := <-s.closes:
			request.done <- s.conn.Close(request.code, request.reason)
			return
		case <-ctx.Done():
			_ = s.conn.Close(websocket.StatusGoingAway, "server_shutdown")
			return
		}
	}
}

func (s *session) requestClose(code websocket.StatusCode, reason string) {
	request := closeRequest{code: code, reason: reason, done: make(chan error, 1)}
	select {
	case s.closes <- request:
	case <-time.After(s.handler.config.CloseTimeout):
		return
	}
	select {
	case <-request.done:
	case <-time.After(s.handler.config.CloseTimeout):
		_ = s.conn.CloseNow()
	}
}

func (s *session) joinWorkers() {
	timer := time.NewTimer(s.handler.config.CloseTimeout)
	defer timer.Stop()
	readerDone, writerDone := s.readerDone, s.writerDone
	for readerDone != nil || writerDone != nil {
		select {
		case <-readerDone:
			readerDone = nil
		case <-writerDone:
			writerDone = nil
		case <-timer.C:
			_ = s.conn.CloseNow()
			return
		}
	}
}

func (s *session) String() string {
	return fmt.Sprintf("relay WSS session state=%s", map[bool]string{true: "authenticated", false: "unauthenticated"}[s.leaseHeld])
}
