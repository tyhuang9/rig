package wss

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

type fakeStateStore struct {
	publicKey       ed25519.PublicKey
	challenge       store.ChallengeInput
	loadedExpiry    time.Time
	challengeStored bool
	truncateExpiry  bool
	consumed        bool
	leaseAcquired   bool
	syncApplied     bool
	released        bool
	mu              sync.Mutex
}

func (f *fakeStateStore) CreateChallenge(_ context.Context, input store.ChallengeInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	input.ClientNonce = append([]byte(nil), input.ClientNonce...)
	input.ServerNonce = append([]byte(nil), input.ServerNonce...)
	input.ACKDigest = append([]byte(nil), input.ACKDigest...)
	f.challenge, f.challengeStored = input, true
	return nil
}
func (f *fakeStateStore) LoadChallengeForAuthentication(context.Context, string) (store.AuthenticationChallenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.challengeStored {
		return store.AuthenticationChallenge{}, store.ErrNotFound
	}
	expiresAt := f.challenge.ExpiresAt
	if f.truncateExpiry {
		expiresAt = expiresAt.Truncate(time.Microsecond)
	}
	f.loadedExpiry = expiresAt
	return store.AuthenticationChallenge{ChallengeInput: store.ChallengeInput{SessionID: f.challenge.SessionID, ControllerID: f.challenge.ControllerID, KeyID: f.challenge.KeyID, ClientNonce: append([]byte(nil), f.challenge.ClientNonce...), ServerNonce: append([]byte(nil), f.challenge.ServerNonce...), ACKDigest: append([]byte(nil), f.challenge.ACKDigest...), ExpiresAt: expiresAt}, PublicKey: append([]byte(nil), f.publicKey...), CreatedAt: commandTime}, nil
}
func (f *fakeStateStore) ConsumeChallenge(context.Context, string, time.Time) error {
	f.mu.Lock()
	f.consumed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeStateStore) AcquireLease(_ context.Context, sessionID string, _ time.Duration) (store.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.consumed {
		return store.Lease{}, errors.New("challenge was not consumed")
	}
	f.leaseAcquired = true
	return store.Lease{ControllerID: commandID1, SessionID: sessionID, LeaseID: commandID3, Fence: 1, ExpiresAt: commandTime.Add(time.Minute)}, nil
}
func (f *fakeStateStore) RenewLease(context.Context, store.Lease, time.Duration) (store.Lease, error) {
	return store.Lease{}, errors.New("not implemented")
}
func (f *fakeStateStore) ValidateLease(context.Context, store.Lease) error { return nil }
func (f *fakeStateStore) ReleaseLease(context.Context, store.Lease) error {
	f.mu.Lock()
	f.released = true
	f.mu.Unlock()
	return nil
}
func (f *fakeStateStore) ApplySubscriptionsSync(_ context.Context, _ store.Lease, _ store.SessionCommand, generation uint64, subscriptions []store.Subscription) (store.SessionCommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.leaseAcquired || generation != 1 || len(subscriptions) != 1 {
		return store.SessionCommandResult{}, errors.New("invalid sync ordering")
	}
	f.syncApplied = true
	return store.SessionCommandResult{Kind: store.ResultSubscriptionsSynced, Generation: generation, Count: uint32(len(subscriptions))}, nil
}
func (f *fakeStateStore) ApplyDecisionProtocolError(_ context.Context, _ store.Lease, _ store.SessionCommand, code string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultProtocolError, ErrorCode: code}, nil
}
func (f *fakeStateStore) ApplySourceDecision(context.Context, store.Lease, store.SessionCommand, string, uint64, string, bool, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyAccessDecision(context.Context, store.Lease, store.SessionCommand, string, string, bool, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyBindingRemoval(context.Context, store.Lease, store.SessionCommand, int64, int64) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyKeyRevocation(context.Context, store.Lease, store.SessionCommand, string, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyControllerRevocation(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyRotationProposal(context.Context, store.Lease, store.SessionCommand, store.RotationInput, time.Duration) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyRotationConfirmation(context.Context, store.Lease, store.SessionCommand, string, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) ApplyRotationFinalization(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, errors.New("not implemented")
}
func (f *fakeStateStore) PendingDesired(context.Context, store.Lease, int) ([]store.DesiredState, error) {
	return nil, nil
}
func (f *fakeStateStore) PendingAccess(context.Context, store.Lease, int) ([]store.PendingAccess, error) {
	return nil, nil
}

type fakeSocket struct {
	subprotocol  string
	readLimit    int64
	readLimits   []int64
	readObserved []int64
	read         func(context.Context, int) (websocket.MessageType, []byte, error)
	onWrite      func(protocol.Frame)
	mu           sync.Mutex
	readCount    int
	closed       bool
	closeCalls   int
	abortCalls   int
}

func (f *fakeSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	f.mu.Lock()
	index := f.readCount
	f.readCount++
	f.readObserved = append(f.readObserved, f.readLimit)
	f.mu.Unlock()
	return f.read(ctx, index)
}
func (f *fakeSocket) Write(_ context.Context, messageType websocket.MessageType, data []byte) error {
	if messageType != websocket.MessageText {
		return errors.New("non-text write")
	}
	frame, err := protocol.Decode(data, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		return err
	}
	if f.onWrite != nil {
		f.onWrite(frame)
	}
	return nil
}
func (f *fakeSocket) Close(websocket.StatusCode, string) error {
	f.mu.Lock()
	f.closed = true
	f.closeCalls++
	f.mu.Unlock()
	return nil
}
func (f *fakeSocket) CloseNow() error {
	f.mu.Lock()
	f.closed = true
	f.abortCalls++
	f.mu.Unlock()
	return nil
}
func (f *fakeSocket) SetReadLimit(limit int64) {
	f.mu.Lock()
	f.readLimit = limit
	f.readLimits = append(f.readLimits, limit)
	f.mu.Unlock()
}
func (f *fakeSocket) Subprotocol() string { return f.subprotocol }

func TestHandlerRejectsMethodOriginPresenceAndMissingSubprotocol(t *testing.T) {
	config := DefaultConfig()
	handler, err := NewHandler(&fakeStateStore{}, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		method string
		header http.Header
		status int
	}{
		{name: "method", method: http.MethodPost, header: http.Header{}, status: http.StatusMethodNotAllowed},
		{name: "empty origin is present", method: http.MethodGet, header: http.Header{"Origin": []string{""}, "Sec-Websocket-Protocol": []string{protocol.Subprotocol}}, status: http.StatusForbidden},
		{name: "origin", method: http.MethodGet, header: http.Header{"Origin": []string{"https://example.test"}, "Sec-Websocket-Protocol": []string{protocol.Subprotocol}}, status: http.StatusForbidden},
		{name: "subprotocol", method: http.MethodGet, header: http.Header{}, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "http://relay.test/ws", nil)
			request.Header = tc.header
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestStopAdmissionsIsStableAndWaitSafe(t *testing.T) {
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	handler.StopAdmissions()
	request := httptest.NewRequest(http.MethodGet, "http://relay.test/ws", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerForcesTeardownWhenAcceptedSubprotocolDoesNotMatch(t *testing.T) {
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	conn := &fakeSocket{subprotocol: ""}
	handler.accept = func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error) {
		return conn, nil
	}
	request := httptest.NewRequest(http.MethodGet, "http://relay.test/ws", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.abortCalls != 1 || conn.closeCalls != 0 {
		t.Fatalf("close calls=%d forced aborts=%d", conn.closeCalls, conn.abortCalls)
	}
	if len(handler.admissions) != 0 {
		t.Fatal("admission was not released after subprotocol rejection")
	}
}

func TestHandlerAdmissionCapacityAndReleasePaths(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 1
	handler, err := NewHandler(&fakeStateStore{}, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *http.Request {
		value := httptest.NewRequest(http.MethodGet, "http://relay.test/ws", nil)
		value.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
		return value
	}

	handler.admissions <- struct{}{}
	called := false
	handler.accept = func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error) {
		called = true
		return nil, errors.New("must not accept at capacity")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request())
	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status=%d accept called=%v", response.Code, called)
	}
	if stats := handler.Stats(); stats.Active != 0 || stats.Capacity != 1 || stats.CapacityRejections != 1 {
		t.Fatalf("capacity rejection stats=%+v", stats)
	}
	<-handler.admissions

	handler.accept = func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error) {
		return nil, errors.New("upgrade failed")
	}
	handler.ServeHTTP(httptest.NewRecorder(), request())
	if len(handler.admissions) != 0 {
		t.Fatal("admission leaked after accept failure")
	}

	conn := &fakeSocket{subprotocol: protocol.Subprotocol, read: func(context.Context, int) (websocket.MessageType, []byte, error) {
		return 0, nil, errors.New("peer closed before hello")
	}}
	handler.accept = func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error) { return conn, nil }
	handler.ServeHTTP(httptest.NewRecorder(), request())
	if len(handler.admissions) != 0 {
		t.Fatal("admission leaked after session exit")
	}
}

func TestHandlerUpgradeFailureLogContainsOnlyFixedOperationalFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	handler.accept = func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error) {
		return nil, errors.New("secret-upgrade-token")
	}
	request := httptest.NewRequest(http.MethodGet, "http://relay.test/v1/controllers/connect?token=secret-query", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
	request.Header.Set("Authorization", "Bearer secret-header")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	logged := output.String()
	if !strings.Contains(logged, `"msg":"relay WSS admission failed"`) || !strings.Contains(logged, `"code":"upgrade_failed"`) {
		t.Fatalf("missing fixed WSS log fields: %q", logged)
	}
	for _, forbidden := range []string{"secret-upgrade-token", "secret-query", "secret-header", "Authorization", request.URL.String()} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("WSS log leaked %q: %q", forbidden, logged)
		}
	}
}

func TestHandlerStatsIncludeHandshakeAndWaitForEveryExit(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 2
	handler, err := NewHandler(&fakeStateStore{}, config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler.accept = func(http.ResponseWriter, *http.Request, *websocket.AcceptOptions) (socket, error) {
		entered <- struct{}{}
		<-release
		return nil, errors.New("upgrade stopped")
	}
	request := httptest.NewRequest(http.MethodGet, "http://relay.test/v1/controllers/connect", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	<-entered
	if stats := handler.Stats(); stats.Active != 1 || stats.Capacity != 2 || stats.CapacityRejections != 0 {
		t.Fatalf("handshake stats=%+v", stats)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := handler.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait while active=%v", err)
	}
	close(release)
	<-done
	if err := handler.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := handler.Stats(); stats.Active != 0 || stats.Capacity != 2 || stats.CapacityRejections != 0 {
		t.Fatalf("exit stats=%+v", stats)
	}
}

func TestHandshakePersistsBeforeChallengeAuthenticatesAndRequiresFullSync(t *testing.T) {
	now := time.Date(2030, time.February, 3, 4, 5, 6, 123, time.FixedZone("challenge-test", -7*60*60))
	if now.Nanosecond()%int(time.Microsecond) == 0 {
		t.Fatal("test clock has microsecond precision")
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	state := &fakeStateStore{publicKey: publicKey, truncateExpiry: true}
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := DefaultConfig()
	config.HandshakeTimeout = time.Second
	config.StoreTimeout = time.Second
	config.WriteTimeout = time.Second
	config.CloseTimeout = time.Second
	handler, err := NewHandler(state, config, Options{Now: func() time.Time { return now }, Entropy: bytes.NewReader(bytes.Repeat([]byte{9}, 1024)), Lifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	hello := &protocol.Hello{Envelope: protocol.NewEnvelope(protocol.TypeHello, commandMessage1, now), ControllerID: commandID1, KeyID: commandID2, ClientNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, protocol.NonceBytes)), ACKState: []protocol.ACKState{}}
	syncFrame := &protocol.SubscriptionsSync{Envelope: protocol.NewEnvelope(protocol.TypeSubscriptionsSync, commandMessage2, now), Generation: 1, Subscriptions: []protocol.Subscription{{SubscriptionID: commandID4, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}}}
	challengeReady := make(chan *protocol.Challenge, 1)
	readyWritten := make(chan struct{}, 1)
	var wireChallengeExpiry time.Time
	fakeConn := &fakeSocket{subprotocol: protocol.Subprotocol}
	fakeConn.read = func(ctx context.Context, index int) (websocket.MessageType, []byte, error) {
		switch index {
		case 0:
			data, encodeErr := protocol.Encode(hello, config.MaxEnvelopeBytes)
			return websocket.MessageText, data, encodeErr
		case 1:
			var challenge *protocol.Challenge
			select {
			case challenge = <-challengeReady:
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
			state.mu.Lock()
			storedBeforeWrite := state.challengeStored
			state.mu.Unlock()
			if !storedBeforeWrite {
				return 0, nil, errors.New("challenge was written before durable storage")
			}
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
		case 2:
			select {
			case <-readyWritten:
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
			state.mu.Lock()
			leaseBeforeReady := state.leaseAcquired
			state.mu.Unlock()
			if !leaseBeforeReady {
				return 0, nil, errors.New("ready was written before lease acquisition")
			}
			data, encodeErr := protocol.Encode(syncFrame, config.MaxEnvelopeBytes)
			return websocket.MessageText, data, encodeErr
		default:
			<-ctx.Done()
			return 0, nil, ctx.Err()
		}
	}
	fakeConn.onWrite = func(frame protocol.Frame) {
		switch value := frame.(type) {
		case *protocol.Challenge:
			state.mu.Lock()
			wireChallengeExpiry = value.ExpiresAt
			state.mu.Unlock()
			challengeReady <- value
		case *protocol.Ready:
			readyWritten <- struct{}{}
		case *protocol.SubscriptionsSynced:
			cancel()
		}
	}
	handler.accept = func(_ http.ResponseWriter, _ *http.Request, options *websocket.AcceptOptions) (socket, error) {
		if len(options.Subprotocols) != 1 || options.Subprotocols[0] != protocol.Subprotocol || options.CompressionMode != websocket.CompressionDisabled {
			return nil, errors.New("unsafe accept options")
		}
		return fakeConn, nil
	}
	request := httptest.NewRequest(http.MethodGet, "http://relay.test/ws", nil)
	request.Header.Set("Sec-WebSocket-Protocol", protocol.Subprotocol)
	request = request.WithContext(func() context.Context {
		canceled, stop := context.WithCancel(context.Background())
		stop()
		return canceled
	}())
	handler.ServeHTTP(httptest.NewRecorder(), request)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.challengeStored || !state.consumed || !state.leaseAcquired || !state.syncApplied || !state.released {
		t.Fatalf("state=%+v", state)
	}
	wantChallengeExpiry := now.UTC().Add(config.ChallengeLifetime).Truncate(time.Microsecond)
	if state.challenge.ExpiresAt.Location() != time.UTC || state.loadedExpiry.Location() != time.UTC || wireChallengeExpiry.Location() != time.UTC || !state.challenge.ExpiresAt.Equal(wantChallengeExpiry) || !state.loadedExpiry.Equal(wantChallengeExpiry) || !wireChallengeExpiry.Equal(wantChallengeExpiry) || !wireChallengeExpiry.Equal(state.loadedExpiry) {
		t.Fatalf("challenge expiry stored=%s loaded=%s wire=%s want=%s", state.challenge.ExpiresAt, state.loadedExpiry, wireChallengeExpiry, wantChallengeExpiry)
	}
	if len(fakeConn.readLimits) != 2 || fakeConn.readLimits[0] != int64(config.HandshakeMaxBytes) || fakeConn.readLimits[1] != int64(config.MaxEnvelopeBytes) {
		t.Fatalf("read limit transitions=%v", fakeConn.readLimits)
	}
	if len(fakeConn.readObserved) < 3 || fakeConn.readObserved[0] != int64(config.HandshakeMaxBytes) || fakeConn.readObserved[1] != int64(config.HandshakeMaxBytes) || fakeConn.readObserved[2] != int64(config.MaxEnvelopeBytes) {
		t.Fatalf("permitted read limits=%v", fakeConn.readObserved)
	}
}

type blockingSocket struct {
	writeErr  error
	abortOnce sync.Once
	aborted   chan struct{}
}

func newBlockingSocket() *blockingSocket { return &blockingSocket{aborted: make(chan struct{})} }
func (s *blockingSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-s.aborted:
		return 0, nil, errors.New("aborted")
	}
}
func (s *blockingSocket) Write(context.Context, websocket.MessageType, []byte) error {
	return s.writeErr
}
func (s *blockingSocket) Close(websocket.StatusCode, string) error {
	<-s.aborted
	return nil
}
func (s *blockingSocket) CloseNow() error {
	s.abortOnce.Do(func() { close(s.aborted) })
	return nil
}
func (s *blockingSocket) SetReadLimit(int64)  {}
func (s *blockingSocket) Subprotocol() string { return protocol.Subprotocol }

func TestWriterFailurePropagatesAndForcedCloseUnblocksWriter(t *testing.T) {
	config := DefaultConfig()
	config.WriteTimeout = 20 * time.Millisecond
	config.CloseTimeout = 20 * time.Millisecond
	handler, err := NewHandler(&fakeStateStore{}, config, Options{Now: func() time.Time { return commandTime }, Entropy: bytes.NewReader(bytes.Repeat([]byte{1}, 128))})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("writer error reaches every sender", func(t *testing.T) {
		writeFailure := errors.New("blocked peer")
		conn := newBlockingSocket()
		conn.writeErr = writeFailure
		session := newSession(handler, conn)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			defer close(session.writerDone)
			session.writeLoop(ctx)
		}()
		frame := &protocol.Heartbeat{Envelope: protocol.NewEnvelope(protocol.TypeHeartbeat, commandMessage1, commandTime), Sequence: 1}
		started := time.Now()
		if err := session.sendFrame(ctx, frame); !errors.Is(err, writeFailure) {
			t.Fatalf("first error=%v", err)
		}
		if time.Since(started) > 200*time.Millisecond {
			t.Fatal("writer failure did not propagate promptly")
		}
		started = time.Now()
		if err := session.sendFrame(ctx, frame); err == nil {
			t.Fatal("second send to exited writer succeeded")
		}
		if time.Since(started) > 200*time.Millisecond {
			t.Fatal("second send did not observe durable writer exit")
		}
		cancel()
		<-session.writerDone
	})

	t.Run("close timeout forces teardown", func(t *testing.T) {
		conn := newBlockingSocket()
		session := newSession(handler, conn)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			defer close(session.writerDone)
			session.writeLoop(ctx)
		}()
		started := time.Now()
		session.requestClose(websocket.StatusNormalClosure, "complete")
		if time.Since(started) > 200*time.Millisecond {
			t.Fatal("forced close exceeded bounded window")
		}
		select {
		case <-conn.aborted:
		default:
			t.Fatal("CloseNow was not used after blocked close handshake")
		}
		<-session.writerDone
	})
}

func TestConfigRequiresLeaseHalfLifeAndWholeSecondHeartbeat(t *testing.T) {
	config := DefaultConfig()
	config.LeaseRenewInterval = config.LeaseDuration/2 + time.Nanosecond
	if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err == nil {
		t.Fatal("renewal after lease half-life accepted")
	}
	config = DefaultConfig()
	config.HeartbeatInterval += time.Millisecond
	if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err == nil {
		t.Fatal("fractional heartbeat interval accepted")
	}
	for _, mutate := range []func(*Config){
		func(config *Config) { config.MaxConnections = 0 },
		func(config *Config) { config.HandshakeMaxBytes = 256<<10 + 1 },
		func(config *Config) { config.HandshakeMaxBytes = config.MaxEnvelopeBytes + 1 },
		func(config *Config) { config.MaxEnvelopeBytes = protocol.DefaultMaxEnvelopeBytes + 1 },
	} {
		config = DefaultConfig()
		mutate(&config)
		if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err == nil {
			t.Fatal("unsafe admission/envelope configuration accepted")
		}
	}
}

func TestConfigEnforcesTimeoutUpperAndLeaseSlackBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "handshake upper bound", mutate: func(config *Config) {
			config.HandshakeTimeout = maxHandshakeTimeout + time.Nanosecond
			config.ChallengeLifetime = config.HandshakeTimeout
		}},
		{name: "store upper bound", mutate: func(config *Config) {
			config.StoreTimeout = maxStoreTimeout + time.Nanosecond
			config.LeaseRenewInterval = 2 * config.StoreTimeout
			config.LeaseDuration = 4 * config.StoreTimeout
		}},
		{name: "write upper bound", mutate: func(config *Config) {
			config.WriteTimeout = maxWriteTimeout + time.Nanosecond
			config.LeaseDuration = 2 * time.Minute
		}},
		{name: "close upper bound", mutate: func(config *Config) { config.CloseTimeout = maxCloseTimeout + time.Nanosecond }},
		{name: "handshake exceeds challenge", mutate: func(config *Config) { config.HandshakeTimeout = config.ChallengeLifetime + time.Nanosecond }},
		{name: "lease slack equality", mutate: func(config *Config) {
			config.HandshakeTimeout = time.Second
			config.LeaseDuration = config.StoreTimeout + config.WriteTimeout + config.LeaseRenewInterval
		}},
		{name: "post-auth lease budget equality", mutate: func(config *Config) {
			config.LeaseDuration = config.HandshakeTimeout + config.StoreTimeout + 2*config.WriteTimeout
		}},
		{name: "outstanding protocol bound", mutate: func(config *Config) {
			config.MaxOutstanding = protocol.MaxArrayItems + 1
			config.OutboundQueue = config.MaxOutstanding
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			tc.mutate(&config)
			if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err == nil {
				t.Fatal("unsafe boundary accepted")
			}
		})
	}

	config := DefaultConfig()
	config.HandshakeTimeout = time.Second
	config.LeaseDuration = config.StoreTimeout + config.WriteTimeout + config.LeaseRenewInterval + time.Nanosecond
	if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err != nil {
		t.Fatalf("strict lease slack boundary rejected: %v", err)
	}
	config = DefaultConfig()
	config.LeaseDuration = config.HandshakeTimeout + config.StoreTimeout + 2*config.WriteTimeout + time.Nanosecond
	if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err != nil {
		t.Fatalf("strict post-auth lease budget boundary rejected: %v", err)
	}
	config = DefaultConfig()
	config.HandshakeTimeout = config.ChallengeLifetime
	config.LeaseDuration = 2 * time.Minute
	if _, err := NewHandler(&fakeStateStore{}, config, Options{}); err != nil {
		t.Fatalf("handshake/challenge equality rejected: %v", err)
	}
}

func TestHandshakeFrameBoundaryRejectsBinaryOversizeAndNonStrictJSON(t *testing.T) {
	config := DefaultConfig()
	handler, err := NewHandler(&fakeStateStore{}, config, Options{Now: func() time.Time { return commandTime }})
	if err != nil {
		t.Fatal(err)
	}
	validHello := &protocol.Hello{Envelope: protocol.NewEnvelope(protocol.TypeHello, commandMessage1, commandTime), ControllerID: commandID1, KeyID: commandID2, ClientNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, protocol.NonceBytes)), ACKState: []protocol.ACKState{}}
	validBytes, err := protocol.Encode(validHello, config.MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	unknownField := append([]byte(nil), validBytes[:len(validBytes)-1]...)
	unknownField = append(unknownField, []byte(`,"unexpected":true}`)...)
	for _, tc := range []struct {
		name        string
		messageType websocket.MessageType
		data        []byte
		wantCode    string
	}{
		{name: "binary", messageType: websocket.MessageBinary, data: validBytes, wantCode: "binary_not_supported"},
		{name: "oversize", messageType: websocket.MessageText, data: bytes.Repeat([]byte{'x'}, config.HandshakeMaxBytes+1), wantCode: "invalid_frame"},
		{name: "unknown field", messageType: websocket.MessageText, data: unknownField, wantCode: "invalid_frame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newSession(handler, newBlockingSocket())
			session.reads <- readEvent{messageType: tc.messageType, data: append([]byte(nil), tc.data...)}
			_, failure := session.nextControllerFrame(context.Background(), time.Second, config.HandshakeMaxBytes)
			if failure == nil || failure.code != tc.wantCode {
				t.Fatalf("failure=%#v", failure)
			}
		})
	}
}

func TestHandshakeFrameWaitIsBounded(t *testing.T) {
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(handler, newBlockingSocket())
	started := time.Now()
	_, failure := session.nextControllerFrame(context.Background(), 10*time.Millisecond, handler.config.HandshakeMaxBytes)
	if failure == nil || failure.code != "handshake_timeout" {
		t.Fatalf("failure=%#v", failure)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("handshake wait exceeded bound")
	}
}

func TestAuthenticatedFullSyncUsesActiveFrameBound(t *testing.T) {
	config := DefaultConfig()
	handler, err := NewHandler(&fakeStateStore{}, config, Options{Now: func() time.Time { return commandTime }})
	if err != nil {
		t.Fatal(err)
	}
	subscriptions := make([]protocol.Subscription, protocol.MaxArrayItems)
	longRef := "refs/heads/" + string(bytes.Repeat([]byte{'a'}, 240))
	for index := range subscriptions {
		subscriptions[index] = protocol.Subscription{SubscriptionID: activeUUID(5000 + index), InstallationID: 10, RepositoryID: 20, Ref: longRef}
	}
	syncFrame := &protocol.SubscriptionsSync{Envelope: protocol.NewEnvelope(protocol.TypeSubscriptionsSync, activeUUID(4999), commandTime), Generation: 1, Subscriptions: subscriptions}
	encoded, err := protocol.Encode(syncFrame, config.MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= config.HandshakeMaxBytes {
		t.Fatalf("test sync frame=%d is not above handshake cap=%d", len(encoded), config.HandshakeMaxBytes)
	}
	session := newSession(handler, newBlockingSocket())
	session.reads <- readEvent{messageType: websocket.MessageText, data: append([]byte(nil), encoded...)}
	decoded, frameFailure := session.nextControllerFrame(context.Background(), time.Second, config.MaxEnvelopeBytes)
	if frameFailure != nil {
		t.Fatal(frameFailure)
	}
	if _, ok := decoded.(*protocol.SubscriptionsSync); !ok {
		t.Fatalf("decoded frame=%T", decoded)
	}
	if _, err = canonicalSessionCommand(decoded, config.MaxEnvelopeBytes); err != nil {
		t.Fatalf("canonical full sync: %v", err)
	}
}

func TestStatsExposeOnlyClosedAggregateDimensionsConcurrently(t *testing.T) {
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	handler.observeLifecycle("ghp_secret_dynamic")
	handler.observeDelivery("https://repository.example")
	handler.observeDecision("raw_error")
	done := make(chan struct{}, 6)
	for i := 0; i < 6; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				handler.observeLifecycle("handshake")
				handler.observeDelivery("desired")
				handler.observeDecision("ack")
				_ = handler.Stats()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 6; i++ {
		<-done
	}
	stats := handler.Stats()
	if stats.LifecycleOutcomes[0] != 600 || stats.Deliveries[0] != 600 || stats.Decisions[0] != 600 {
		t.Fatalf("stats=%+v", stats)
	}
	for _, values := range [][]uint64{stats.LifecycleOutcomes[:], stats.Deliveries[:], stats.Decisions[:]} {
		for index := 1; index < len(values); index++ {
			if values[index] != 0 {
				t.Fatalf("unexpected dynamic dimension accepted: %+v", stats)
			}
		}
	}
}
