package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
	"github.com/hostd/hostd/internal/relay/wss"
)

// TestSessionTransportReACKsDurablyCommittedSourceAfterRelayReconnect covers
// the controller/relay boundary as two real WSS sessions. The first controller
// ACK is deliberately lost after SQLite commits the envelope, so the next hello
// must advertise the durable ACK head before the relay re-delivers the same
// desired state and observes its one successful ACK.
func TestSessionTransportReACKsDurablyCommittedSourceAfterControllerReconnect(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	privateKey := deterministicSessionPrivateKey(0x73)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)

	repository, _, _ := newRepositoryHarness(t)
	binding, subscription := newRelayInteropBinding(t, repository, now, publicKey)
	deliveryID := uuid.NewString()
	desired := store.DesiredState{
		DeliveryID: deliveryID, ControllerID: binding.ControllerID, SubscriptionID: subscription.SubscriptionID,
		Generation: 1, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID,
		Ref: subscription.Ref, SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: now,
	}
	relayState := newRelayInteropState(now, publicKey, desired)

	relayConfig := wss.DefaultConfig()
	relayConfig.PollInterval = 10 * time.Millisecond
	relayConfig.StoreTimeout = time.Second
	relayConfig.WriteTimeout = time.Second
	handler, err := wss.NewHandler(relayState, relayConfig, wss.Options{Now: func() time.Time { return now }, Entropy: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	t.Cleanup(func() {
		handler.StopAdmissions()
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := handler.Wait(waitCtx); err != nil {
			t.Errorf("wait for relay handler: %v", err)
		}
	})

	credentials := &relayInteropCredentials{bundle: ControllerKeyBundle{
		Version: credentialVersion, ControllerID: binding.ControllerID, KeyID: repositoryTestKeyID,
		PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...),
	}}
	dial := &relayInteropDial{dropFirstSourceACK: make(chan struct{})}
	transportConfig := DefaultSessionTransportConfig()
	transportConfig.Now = func() time.Time { return now }
	transportConfig.Entropy = rand.Reader
	transportConfig.HandshakeTimeout = time.Second
	transportConfig.WriteTimeout = time.Second
	transportConfig.PersistenceTimeout = time.Second
	transport, err := NewSessionTransport(server.URL, repository, credentials, server.Client().Transport, dial.Dial, transportConfig)
	if err != nil {
		t.Fatal(err)
	}
	supervisorConfig := DefaultSupervisorConfig()
	supervisorConfig.Now = func() time.Time { return now }
	supervisorConfig.InitialBackoff = 5 * time.Millisecond
	supervisorConfig.MaximumBackoff = 25 * time.Millisecond
	supervisorConfig.Jitter = func(delay time.Duration, _ uint32) time.Duration { return delay }
	supervisor, err := NewSupervisor(transport, repository, &supervisorRecoveryFake{}, supervisorCompleterFake{}, supervisorConfig)
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- supervisor.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("supervisor run: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("supervisor did not stop")
		}
	})

	select {
	case <-dial.dropFirstSourceACK:
	case <-time.After(2 * time.Second):
		t.Fatal("first source ACK was not deliberately dropped")
	}
	assertRelayInteropInbox(t, repository, binding.ControllerID, desired, 1)

	select {
	case <-relayState.successfulSourceACK:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not receive re-ACK after reconnect")
	}
	if got := relayState.sourceACKCount(); got != 1 {
		t.Fatalf("successful relay source ACKs=%d want 1", got)
	}
	if got := relayState.helloACKDigests(); len(got) != 2 || !bytes.Equal(got[0], relayInteropACKDigest(t, nil)) || !bytes.Equal(got[1], relayInteropACKDigest(t, []protocol.ACKState{{SubscriptionID: subscription.SubscriptionID, Generation: 1}})) {
		t.Fatalf("hello ACK digests do not prove durable generation one after reconnect: %x", got)
	}
	assertRelayInteropInbox(t, repository, binding.ControllerID, desired, 1)
}

func newRelayInteropBinding(t *testing.T, repository *Repository, now time.Time, publicKey ed25519.PublicKey) (InstallationBinding, RelaySubscription) {
	t.Helper()
	activated := now
	identity := ControllerIdentity{ControllerID: repositoryTestControllerID, State: ControllerActive, CreatedAt: now, UpdatedAt: now}
	key := ControllerKey{KeyID: repositoryTestKeyID, ControllerID: repositoryTestControllerID, PublicKey: append([]byte(nil), publicKey...), Algorithm: KeyAlgorithmEd25519, State: KeyActive, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID), CreatedAt: now, UpdatedAt: now, ActivatedAt: &activated, PossessionConfirmedAt: &activated}
	persistTestIdentity(t, repository, identity, key, now)
	enrollment := testEnrollment(now)
	if err := repository.CreateEnrollment(context.Background(), enrollment); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteEnrollment(context.Background(), enrollment.OwnerUserID, enrollment.EnrollmentID, EnrollmentAuthorized, repositoryTestBindingID, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	binding, err := repository.Binding(context.Background(), enrollment.OwnerUserID, repositoryTestBindingID)
	if err != nil {
		t.Fatal(err)
	}
	subscription := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), subscription); err != nil {
		t.Fatal(err)
	}
	return binding, subscription
}

func assertRelayInteropInbox(t *testing.T, repository *Repository, controllerID string, desired store.DesiredState, wantRows int) {
	t.Helper()
	var rows int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=? AND delivery_id=?`, controllerID, desired.SubscriptionID, desired.DeliveryID).Scan(&rows); err != nil || rows != wantRows {
		t.Fatalf("source inbox rows=%d want=%d err=%v", rows, wantRows, err)
	}
	var generation, installationID, repositoryID int64
	var trackedRef, observedSHA string
	if err := repository.db.QueryRow(`SELECT generation,installation_id,repository_id,tracked_ref,observed_sha FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=? AND delivery_id=?`, controllerID, desired.SubscriptionID, desired.DeliveryID).Scan(&generation, &installationID, &repositoryID, &trackedRef, &observedSHA); err != nil {
		t.Fatal(err)
	}
	if uint64(generation) != desired.Generation || installationID != desired.InstallationID || repositoryID != desired.RepositoryID || trackedRef != desired.Ref || observedSHA != desired.SHA {
		t.Fatalf("persisted source mismatch: generation=%d installation=%d repository=%d ref=%q sha=%q", generation, installationID, repositoryID, trackedRef, observedSHA)
	}
	if err := repository.db.QueryRow(`SELECT generation FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, controllerID, desired.SubscriptionID).Scan(&generation); err != nil || uint64(generation) != desired.Generation {
		t.Fatalf("source ACK head generation=%d want=%d err=%v", generation, desired.Generation, err)
	}
}

func relayInteropACKDigest(t *testing.T, state []protocol.ACKState) []byte {
	t.Helper()
	if state == nil {
		state = []protocol.ACKState{}
	}
	digest, err := protocol.CanonicalACKDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), digest[:]...)
}

type relayInteropCredentials struct {
	bundle ControllerKeyBundle
}

func (credentials *relayInteropCredentials) ReadControllerKey(controllerID, keyID string, expectedPublicKey []byte) (ControllerKeyBundle, error) {
	if credentials == nil || credentials.bundle.ControllerID != controllerID || credentials.bundle.KeyID != keyID || !bytes.Equal(credentials.bundle.PublicKey, expectedPublicKey) {
		return ControllerKeyBundle{}, errors.New("test controller credential unavailable")
	}
	return ControllerKeyBundle{Version: credentials.bundle.Version, ControllerID: controllerID, KeyID: keyID, PrivateKey: append(ed25519.PrivateKey(nil), credentials.bundle.PrivateKey...), PublicKey: append(ed25519.PublicKey(nil), credentials.bundle.PublicKey...)}, nil
}

type relayInteropDial struct {
	dropped            atomic.Bool
	dropFirstSourceACK chan struct{}
}

func (dial *relayInteropDial) Dial(ctx context.Context, target string, options *websocket.DialOptions) (SessionSocket, *http.Response, error) {
	connection, response, err := websocket.Dial(ctx, target, options)
	if err != nil {
		return nil, response, err
	}
	return &relayInteropSocket{SessionSocket: connection, dial: dial}, response, nil
}

type relayInteropSocket struct {
	SessionSocket
	dial *relayInteropDial
}

func (socket *relayInteropSocket) Write(ctx context.Context, messageType websocket.MessageType, data []byte) error {
	frame, err := protocol.Decode(data, protocol.DefaultMaxEnvelopeBytes)
	if err == nil {
		if ack, ok := frame.(*protocol.Ack); ok && ack.Source != nil && socket.dial.dropped.CompareAndSwap(false, true) {
			_ = socket.SessionSocket.CloseNow()
			close(socket.dial.dropFirstSourceACK)
			return errors.New("test source ACK connection drop")
		}
	}
	return socket.SessionSocket.Write(ctx, messageType, data)
}

type relayInteropState struct {
	mu                  sync.Mutex
	now                 time.Time
	publicKey           ed25519.PublicKey
	desired             store.DesiredState
	challenges          map[string]store.AuthenticationChallenge
	sessions            map[string]string
	leaseByController   map[string]store.Lease
	nextFence           uint64
	ackDigests          [][]byte
	successfulACKs      int
	successfulSourceACK chan struct{}
}

func newRelayInteropState(now time.Time, publicKey ed25519.PublicKey, desired store.DesiredState) *relayInteropState {
	return &relayInteropState{now: now, publicKey: append(ed25519.PublicKey(nil), publicKey...), desired: desired, challenges: make(map[string]store.AuthenticationChallenge), sessions: make(map[string]string), leaseByController: make(map[string]store.Lease), successfulSourceACK: make(chan struct{})}
}

func (state *relayInteropState) CreateChallenge(_ context.Context, input store.ChallengeInput) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.ackDigests = append(state.ackDigests, append([]byte(nil), input.ACKDigest...))
	state.challenges[input.SessionID] = store.AuthenticationChallenge{ChallengeInput: store.ChallengeInput{SessionID: input.SessionID, ControllerID: input.ControllerID, KeyID: input.KeyID, ClientNonce: append([]byte(nil), input.ClientNonce...), ServerNonce: append([]byte(nil), input.ServerNonce...), ACKDigest: append([]byte(nil), input.ACKDigest...), ExpiresAt: input.ExpiresAt}, PublicKey: append([]byte(nil), state.publicKey...), CreatedAt: state.now}
	return nil
}

func (state *relayInteropState) LoadChallengeForAuthentication(_ context.Context, sessionID string) (store.AuthenticationChallenge, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	challenge, ok := state.challenges[sessionID]
	if !ok {
		return store.AuthenticationChallenge{}, store.ErrNotFound
	}
	challenge.ClientNonce = append([]byte(nil), challenge.ClientNonce...)
	challenge.ServerNonce = append([]byte(nil), challenge.ServerNonce...)
	challenge.ACKDigest = append([]byte(nil), challenge.ACKDigest...)
	challenge.PublicKey = append([]byte(nil), challenge.PublicKey...)
	return challenge, nil
}

func (state *relayInteropState) ConsumeChallenge(_ context.Context, sessionID string, _ time.Time) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	challenge, ok := state.challenges[sessionID]
	if !ok {
		return store.ErrNotFound
	}
	state.sessions[sessionID] = challenge.ControllerID
	return nil
}

func (state *relayInteropState) AcquireLease(_ context.Context, sessionID string, duration time.Duration) (store.Lease, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	controllerID, ok := state.sessions[sessionID]
	if !ok {
		return store.Lease{}, store.ErrNotFound
	}
	if _, exists := state.leaseByController[controllerID]; exists {
		return store.Lease{}, store.ErrConflict
	}
	state.nextFence++
	lease := store.Lease{ControllerID: controllerID, SessionID: sessionID, LeaseID: uuid.NewString(), Fence: state.nextFence, ExpiresAt: state.now.Add(duration)}
	state.leaseByController[controllerID] = lease
	return lease, nil
}

func (state *relayInteropState) RenewLease(_ context.Context, lease store.Lease, duration time.Duration) (store.Lease, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.hasLeaseLocked(lease) {
		return store.Lease{}, store.ErrConflict
	}
	lease.ExpiresAt = state.now.Add(duration)
	state.leaseByController[lease.ControllerID] = lease
	return lease, nil
}

func (state *relayInteropState) ReleaseLease(_ context.Context, lease store.Lease) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.hasLeaseLocked(lease) {
		return store.ErrConflict
	}
	delete(state.leaseByController, lease.ControllerID)
	return nil
}

func (state *relayInteropState) ApplySubscriptionsSync(_ context.Context, lease store.Lease, _ store.SessionCommand, generation uint64, subscriptions []store.Subscription) (store.SessionCommandResult, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.hasLeaseLocked(lease) {
		return store.SessionCommandResult{}, store.ErrConflict
	}
	return store.SessionCommandResult{Kind: store.ResultSubscriptionsSynced, Generation: generation, Count: uint32(len(subscriptions))}, nil
}

func (state *relayInteropState) ApplyDecisionProtocolError(_ context.Context, _ store.Lease, _ store.SessionCommand, code string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultProtocolError, ErrorCode: code}, nil
}

func (state *relayInteropState) ApplySourceDecision(_ context.Context, lease store.Lease, command store.SessionCommand, subscriptionID string, generation uint64, _ string, accepted bool, code string) (store.SessionCommandResult, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.hasLeaseLocked(lease) || command.Type != store.CommandAckSource || command.MessageID == "" || !accepted || code != "" || subscriptionID != state.desired.SubscriptionID || generation != state.desired.Generation {
		return store.SessionCommandResult{}, store.ErrConflict
	}
	state.successfulACKs++
	if state.successfulACKs == 1 {
		close(state.successfulSourceACK)
	}
	return store.SessionCommandResult{Kind: store.ResultDecisionApplied}, nil
}

func (state *relayInteropState) ApplyAccessDecision(context.Context, store.Lease, store.SessionCommand, string, string, bool, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultDecisionApplied}, nil
}
func (state *relayInteropState) ApplyBindingRemoval(context.Context, store.Lease, store.SessionCommand, int64, int64) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultBindingRemoved, InstallationID: 1, RepositoryID: 1}, nil
}
func (state *relayInteropState) ApplyKeyRevocation(context.Context, store.Lease, store.SessionCommand, string, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultKeyRevoked, ControllerID: repositoryTestControllerID, KeyID: repositoryTestKeyID}, nil
}
func (state *relayInteropState) ApplyControllerRevocation(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultControllerRevoked, ControllerID: repositoryTestControllerID}, nil
}
func (state *relayInteropState) ApplyRotationProposal(context.Context, store.Lease, store.SessionCommand, store.RotationInput, time.Duration) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, store.ErrNotFound
}
func (state *relayInteropState) ApplyRotationConfirmation(context.Context, store.Lease, store.SessionCommand, string, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, store.ErrNotFound
}
func (state *relayInteropState) ApplyRotationFinalization(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{}, store.ErrNotFound
}

func (state *relayInteropState) PendingDesired(_ context.Context, lease store.Lease, limit int) ([]store.DesiredState, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.hasLeaseLocked(lease) {
		return nil, store.ErrConflict
	}
	if limit < 1 || state.successfulACKs != 0 {
		return nil, nil
	}
	return []store.DesiredState{state.desired}, nil
}

func (*relayInteropState) PendingAccess(context.Context, store.Lease, int) ([]store.PendingAccess, error) {
	return nil, nil
}

func (state *relayInteropState) sourceACKCount() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.successfulACKs
}

func (state *relayInteropState) helloACKDigests() [][]byte {
	state.mu.Lock()
	defer state.mu.Unlock()
	result := make([][]byte, len(state.ackDigests))
	for index := range state.ackDigests {
		result[index] = append([]byte(nil), state.ackDigests[index]...)
	}
	return result
}

func (state *relayInteropState) hasLeaseLocked(lease store.Lease) bool {
	active, ok := state.leaseByController[lease.ControllerID]
	return ok && active.ControllerID == lease.ControllerID && active.SessionID == lease.SessionID && active.LeaseID == lease.LeaseID && active.Fence == lease.Fence
}
