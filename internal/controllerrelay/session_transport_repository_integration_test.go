package controllerrelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
)

func TestActiveSessionPersistsAndReACKsSourceDesiredWithRealRepository(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	firstSubscription := RelaySubscription{
		SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID,
		ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID,
		Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now,
	}
	secondSubscription := firstSubscription
	secondSubscription.SubscriptionID = uuid.NewString()
	secondSubscription.Ref = "refs/heads/release"
	for _, subscription := range []RelaySubscription{firstSubscription, secondSubscription} {
		if err := repository.CreateSubscription(context.Background(), subscription); err != nil {
			t.Fatalf("create subscription: %v", err)
		}
	}

	readyAt := now.Add(30 * time.Second)
	ready := SessionStatus{ControllerID: binding.ControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err := repository.AdvanceSessionStatus(context.Background(), 0, 0, ready); err != nil {
		t.Fatal(err)
	}
	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now.Add(time.Minute) }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x31}, 256))
	session := &activeControllerSession{transport: transport, controllerID: binding.ControllerID, keyID: repositoryTestKeyID, epoch: 1, fence: 1, expiresAt: now.Add(time.Hour)}
	deliveryID := uuid.NewString()
	first := &protocol.SourceDesired{
		Envelope:   protocol.NewEnvelope(protocol.TypeSourceDesired, uuid.NewString(), now),
		DeliveryID: deliveryID, SubscriptionID: firstSubscription.SubscriptionID, Generation: 1,
		InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: firstSubscription.Ref,
		ObservedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: now,
	}
	assertSourceACK(t, session, first, first.MessageID)

	retransmit := *first
	retransmit.MessageID = uuid.NewString()
	retransmit.SentAt = now.Add(time.Second)
	assertSourceACK(t, session, &retransmit, retransmit.MessageID)

	second := *first
	second.MessageID = uuid.NewString()
	second.SubscriptionID = secondSubscription.SubscriptionID
	second.Ref = secondSubscription.Ref
	assertSourceACK(t, session, &second, second.MessageID)

	var persisted int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_source_event_inbox WHERE controller_id=? AND delivery_id=?`, binding.ControllerID, deliveryID).Scan(&persisted); err != nil || persisted != 2 {
		t.Fatalf("durable same-delivery rows = %d, %v", persisted, err)
	}
}

func TestActiveSessionReACKsDurableAccessAfterBindingRemovalWithRealRepository(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	readyAt := now.Add(30 * time.Second)
	ready := SessionStatus{ControllerID: binding.ControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err := repository.AdvanceSessionStatus(context.Background(), 0, 0, ready); err != nil {
		t.Fatal(err)
	}
	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now.Add(time.Minute) }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x41}, 256))
	session := &activeControllerSession{transport: transport, controllerID: binding.ControllerID, keyID: repositoryTestKeyID, epoch: 1, fence: 1, expiresAt: now.Add(time.Hour)}
	removed := &protocol.AccessChange{
		Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), now),
		EventID:  uuid.NewString(), InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID,
		ChangeCode: "repository.removed", ObservedAt: now, AckRequired: true,
	}
	assertAccessACK(t, session, removed, removed.MessageID)
	if err := repository.MarkBindingRemovalPending(context.Background(), binding.OwnerUserID, binding.BindingID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("mark removal pending: %v", err)
	}
	if err := repository.MarkBindingRemoved(context.Background(), binding.OwnerUserID, binding.BindingID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("mark removed: %v", err)
	}

	retransmit := *removed
	retransmit.MessageID = uuid.NewString()
	retransmit.SentAt = now.Add(4 * time.Minute)
	assertAccessACK(t, session, &retransmit, retransmit.MessageID)

	newEvent := *removed
	newEvent.MessageID = uuid.NewString()
	newEvent.EventID = uuid.NewString()
	newEvent.SentAt = now.Add(5 * time.Minute)
	response, _, err := session.handleInbound(context.Background(), &newEvent, 0)
	reject, ok := response.(*protocol.Reject)
	if err != nil || !ok || reject.TargetMessageID != newEvent.MessageID || reject.Code != RejectInvalidEvent {
		t.Fatalf("new event after removal response=%#v err=%v", response, err)
	}
}

func TestStaleFencedReadySessionCannotMutateInboxAfterNewEpoch(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	subscription := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), subscription); err != nil {
		t.Fatal(err)
	}
	readyAt := now.Add(time.Minute)
	ready := SessionStatus{ControllerID: binding.ControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err := repository.AdvanceSessionStatus(context.Background(), 0, 0, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginSessionEpoch(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now.Add(3 * time.Minute) }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x51}, 256))
	stale := &activeControllerSession{transport: transport, controllerID: binding.ControllerID, keyID: repositoryTestKeyID, epoch: 1, fence: 1, expiresAt: now.Add(time.Hour)}
	source := &protocol.SourceDesired{Envelope: protocol.NewEnvelope(protocol.TypeSourceDesired, uuid.NewString(), now), DeliveryID: uuid.NewString(), SubscriptionID: subscription.SubscriptionID, Generation: 1, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: subscription.Ref, ObservedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: now}
	if _, _, err := stale.handleInbound(context.Background(), source, 0); !sessionErrorCode(err, sessionErrorPersistence) {
		t.Fatalf("stale source error=%v", err)
	}
	access := &protocol.AccessChange{Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), now), EventID: uuid.NewString(), InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, ChangeCode: "repository.removed", ObservedAt: now, AckRequired: true}
	if _, _, err := stale.handleInbound(context.Background(), access, 0); !sessionErrorCode(err, sessionErrorPersistence) {
		t.Fatalf("stale access error=%v", err)
	}
	for _, query := range []string{"SELECT COUNT(*) FROM relay_source_event_inbox", "SELECT COUNT(*) FROM relay_access_event_inbox"} {
		var count int
		if err := repository.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("stale session write query=%q count=%d err=%v", query, count, err)
		}
	}
}

func TestStaleFencedReadySessionCannotMutateInboundControlAfterNewEpoch(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	service := newTestControlService(t, repository, newMemoryControlCredentials(), now.Add(time.Minute))
	command, err := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	readyAt := now.Add(2 * time.Minute)
	ready := SessionStatus{ControllerID: binding.ControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err = repository.AdvanceSessionStatus(context.Background(), 0, 0, ready); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.BeginSessionEpoch(context.Background(), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.ControlHandler = service
	transport.config.Now = func() time.Time { return now.Add(4 * time.Minute) }
	stale := &activeControllerSession{transport: transport, controllerID: binding.ControllerID, keyID: repositoryTestKeyID, epoch: 1, fence: 1, sessionID: uuid.NewString(), expiresAt: now.Add(time.Hour)}
	response := &protocol.BindingRemoved{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemoved, uuid.NewString(), now), TargetMessageID: command.MessageID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID}
	if _, _, err = stale.handleInbound(context.Background(), response, 0); !sessionErrorCode(err, sessionErrorProtocol) {
		t.Fatalf("stale control error=%v", err)
	}
	stored, err := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil || stored.State != BindingRemovalPending || stored.CompletedAt != nil {
		t.Fatalf("stale control changed binding=%#v err=%v", stored, err)
	}
	loaded, err := repository.LoadControlCommand(context.Background(), binding.ControllerID, command.MessageID)
	if err != nil || loaded.State != CommandPrepared || loaded.CompletedAt != nil {
		t.Fatalf("stale control changed command=%#v err=%v", loaded, err)
	}
}

func TestFencedControlMutationsRejectMissingAndStaleOwnersWithoutWrites(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	rotationID := uuid.NewString()
	targetID := uuid.NewString()
	confirm := OutboundCommand{ControllerID: binding.ControllerID, MessageID: uuid.NewString(), CommandType: CommandRotationConfirm, RotationID: rotationID, Stage: "confirm", SentAt: now, Digest: sha256.Sum256([]byte("confirm")), State: CommandPrepared}
	finalize := OutboundCommand{ControllerID: binding.ControllerID, MessageID: uuid.NewString(), CommandType: CommandRotationFinalize, RotationID: rotationID, Stage: "finalize", SentAt: now, Digest: sha256.Sum256([]byte("finalize")), State: CommandPrepared}
	removed := protocol.BindingRemoved{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemoved, uuid.NewString(), now), TargetMessageID: targetID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID}
	finalized := protocol.KeyRotationFinalized{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalized, uuid.NewString(), now), TargetMessageID: targetID, RotationID: rotationID, RetiredKeyID: repositoryTestKeyID}
	cases := []struct {
		name    string
		missing func() error
		stale   func() error
	}{
		{
			name: "binding removed",
			missing: func() error {
				return repository.CompleteBindingRemovalFenced(context.Background(), binding.ControllerID, 0, 0, removed, now)
			},
			stale: func() error {
				return repository.CompleteBindingRemovalFenced(context.Background(), binding.ControllerID, 1, 1, removed, now)
			},
		},
		{
			name: "rotation finalized",
			missing: func() error {
				return repository.CompleteRotationFinalizedFenced(context.Background(), binding.ControllerID, 0, 0, finalized, now)
			},
			stale: func() error {
				return repository.CompleteRotationFinalizedFenced(context.Background(), binding.ControllerID, 1, 1, finalized, now)
			},
		},
		{
			name: "rotation challenge confirmation",
			missing: func() error {
				_, _, err := repository.PrepareRotationConfirmationFenced(context.Background(), targetID, 0, 0, confirm, now)
				return err
			},
			stale: func() error {
				_, _, err := repository.PrepareRotationConfirmationFenced(context.Background(), targetID, 1, 1, confirm, now)
				return err
			},
		},
		{
			name: "rotation confirmed finalize",
			missing: func() error {
				_, _, err := repository.ConfirmRotationAndPrepareFinalizeFenced(context.Background(), targetID, rotationID, repositoryTestKeyID, 0, 0, finalize, now)
				return err
			},
			stale: func() error {
				_, _, err := repository.ConfirmRotationAndPrepareFinalizeFenced(context.Background(), targetID, rotationID, repositoryTestKeyID, 1, 1, finalize, now)
				return err
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var beforeCommands int
			if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_outbound_commands`).Scan(&beforeCommands); err != nil {
				t.Fatal(err)
			}
			if err := test.missing(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("missing fence error=%v", err)
			}
			if err := test.stale(); !errors.Is(err, ErrState) {
				t.Fatalf("stale fence error=%v", err)
			}
			var afterCommands int
			if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_outbound_commands`).Scan(&afterCommands); err != nil || afterCommands != beforeCommands {
				t.Fatalf("fence failure changed commands before=%d after=%d err=%v", beforeCommands, afterCommands, err)
			}
			stored, err := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
			if err != nil || stored.State != BindingAuthorized || stored.CompletedAt != nil {
				t.Fatalf("fence failure changed binding=%#v err=%v", stored, err)
			}
		})
	}
}

func TestProductionControlServiceRejectsMissingFenceBeforeLegacyMutation(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	service := newTestControlService(t, repository, newMemoryControlCredentials(), now)
	command, err := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	response := &protocol.BindingRemoved{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemoved, uuid.NewString(), now), TargetMessageID: command.MessageID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID}
	_, err = service.Handle(context.Background(), testControlSession(now, repositoryTestKeyID), response)
	var controlErr *SessionControlError
	if !errors.As(err, &controlErr) || controlErr.Code != controlErrorState {
		t.Fatalf("missing control fence error=%v", err)
	}
	stored, err := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil || stored.State != BindingRemovalPending || stored.CompletedAt != nil {
		t.Fatalf("missing fence mutated binding=%#v err=%v", stored, err)
	}
}

func TestStaleFencedExpiredRotationCannotMutateOrDeleteCredential(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	pending := testPendingKey(now)
	if err := repository.CreateKey(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: pending.KeyID, State: RotationPrepare, ExpiresAt: now.Add(time.Minute), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	rotation.ExpiresAt = now.Add(-time.Minute)
	if _, err := repository.db.Exec(`UPDATE relay_key_rotations SET expires_at=? WHERE controller_id=? AND rotation_id=?`, timestamp(rotation.ExpiresAt), rotation.ControllerID, rotation.RotationID); err != nil {
		t.Fatal(err)
	}
	credentials := newMemoryControlCredentials()
	ref := ProtectedKeyRef(rotation.ControllerID, rotation.NewKeyID)
	credentials.keys[ref] = ControllerKeyBundle{Version: credentialVersion, ControllerID: rotation.ControllerID, KeyID: rotation.NewKeyID}
	service := newTestControlService(t, repository, credentials, now)
	stale := testControlSession(now, repositoryTestKeyID)
	stale.Epoch, stale.Fence = 1, 1
	if err := service.failExpiredRotationFenced(context.Background(), stale, rotation, now); err == nil {
		t.Fatal("stale expired rotation cleanup succeeded")
	}
	stored, err := repository.Rotation(context.Background(), rotation.ControllerID, rotation.RotationID)
	if err != nil || stored.State != RotationPrepare || stored.CompletedAt != nil {
		t.Fatalf("stale cleanup mutated rotation=%#v err=%v", stored, err)
	}
	key, err := repository.Key(context.Background(), rotation.ControllerID, rotation.NewKeyID)
	if err != nil || key.State != KeyPending || !credentials.has(ref) {
		t.Fatalf("stale cleanup mutated key=%#v retained=%t err=%v", key, credentials.has(ref), err)
	}
}

func assertSourceACK(t *testing.T, session *activeControllerSession, source *protocol.SourceDesired, target string) {
	t.Helper()
	response, _, err := session.handleInbound(context.Background(), source, 0)
	ack, ok := response.(*protocol.Ack)
	if err != nil || !ok || ack.TargetMessageID != target || ack.Source == nil || ack.Source.SubscriptionID != source.SubscriptionID || ack.Source.Generation != source.Generation {
		t.Fatalf("source ACK response=%#v err=%v", response, err)
	}
}

func assertAccessACK(t *testing.T, session *activeControllerSession, change *protocol.AccessChange, target string) {
	t.Helper()
	response, _, err := session.handleInbound(context.Background(), change, 0)
	ack, ok := response.(*protocol.Ack)
	if err != nil || !ok || ack.TargetMessageID != target || ack.Access == nil || ack.Access.EventID != change.EventID {
		t.Fatalf("access ACK response=%#v err=%v", response, err)
	}
}
