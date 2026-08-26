package controllerrelay

import (
	"bytes"
	"context"
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

	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now.Add(time.Minute) }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x31}, 256))
	session := &activeControllerSession{transport: transport, controllerID: binding.ControllerID, expiresAt: now.Add(time.Hour)}
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
	transport := &SessionTransport{store: repository, config: DefaultSessionTransportConfig()}
	transport.config.Now = func() time.Time { return now.Add(time.Minute) }
	transport.config.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x41}, 256))
	session := &activeControllerSession{transport: transport, controllerID: binding.ControllerID, expiresAt: now.Add(time.Hour)}
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
