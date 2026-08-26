package controllerrelay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
)

func TestSessionStatusUsesEpochFenceAndSanitizedRestartState(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	ctx := context.Background()
	status := SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 1, State: SessionDisconnected, StateChangedAt: now, UpdatedAt: now}
	if err := repository.AdvanceSessionStatus(ctx, 0, 0, status); err != nil {
		t.Fatal(err)
	}
	if err := repository.AdvanceSessionStatus(ctx, 0, 0, status); !errors.Is(err, ErrState) {
		t.Fatalf("stale initial fence = %v", err)
	}
	states := []SessionStatus{
		{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 2, State: SessionConnecting, KeyID: repositoryTestKeyID, Attempt: 1, StateChangedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
		{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 3, State: SessionAuthenticating, KeyID: repositoryTestKeyID, Attempt: 1, StateChangedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
	}
	readyAt := now.Add(3 * time.Second)
	states = append(states, SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 4, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt})
	next := now.Add(time.Minute)
	states = append(states,
		SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 5, State: SessionBackoff, ErrorCode: ErrorRelayUnavailable, Attempt: 2, NextAttemptAt: &next, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: now.Add(4 * time.Second), UpdatedAt: now.Add(4 * time.Second)},
		SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 6, State: SessionNeedsAttention, ErrorCode: ErrorProtocol, Attempt: 2, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: now.Add(5 * time.Second), UpdatedAt: now.Add(5 * time.Second)},
		SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 2, Fence: 1, State: SessionStopped, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: now.Add(6 * time.Second), UpdatedAt: now.Add(6 * time.Second)},
	)
	previousEpoch, previousFence := uint64(1), uint64(1)
	for _, candidate := range states {
		if err := repository.AdvanceSessionStatus(ctx, previousEpoch, previousFence, candidate); err != nil {
			t.Fatalf("advance to %s: %v", candidate.State, err)
		}
		previousEpoch, previousFence = candidate.Epoch, candidate.Fence
	}
	got, err := repository.SessionStatus(ctx, repositoryTestControllerID)
	if err != nil || got.State != SessionStopped || got.Epoch != 2 || got.Fence != 1 || got.LastSeenAt == nil {
		t.Fatalf("restart status = %#v, %v", got, err)
	}
	invalid := got
	invalid.Epoch, invalid.Fence, invalid.State, invalid.ErrorCode = 2, 2, SessionBackoff, "raw provider body"
	invalid.Attempt, invalid.NextAttemptAt = 1, &next
	if err := repository.AdvanceSessionStatus(ctx, 2, 1, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe status error = %v", err)
	}
	invalid = got
	invalid.Fence, invalid.State, invalid.KeyID, invalid.Attempt = 2, SessionConnecting, repositoryTestKeyID, 1
	invalid.StateChangedAt, invalid.UpdatedAt = now.Add(7*time.Second), now.Add(7*time.Second)
	if err := repository.AdvanceSessionStatus(ctx, 2, 1, invalid); err == nil {
		t.Fatal("stopped session resumed without a new epoch")
	}
}

func TestReadySessionCanAdvanceLastSeenWithoutPersistingSessionIdentity(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	readyAt := now
	status := SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err := repository.AdvanceSessionStatus(context.Background(), 0, 0, status); err != nil {
		t.Fatal(err)
	}
	seen := now.Add(time.Minute)
	status.Fence, status.LastSeenAt, status.UpdatedAt = 2, &seen, seen
	if err := repository.AdvanceSessionStatus(context.Background(), 1, 1, status); err != nil {
		t.Fatalf("ready heartbeat status: %v", err)
	}
	got, err := repository.SessionStatus(context.Background(), repositoryTestControllerID)
	if err != nil || got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen) || got.Fence != 2 {
		t.Fatalf("ready heartbeat = %#v %v", got, err)
	}
}

func TestSubscriptionSyncIsSequentialImmutableAndDirtyCoalesced(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	ctx := context.Background()
	sub := RelaySubscription{SubscriptionID: "77777777-7777-4777-8777-777777777777", OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	message1 := "88888888-8888-4888-8888-888888888888"
	first, err := repository.PrepareSubscriptionSync(ctx, sub.ControllerID, message1, now.Add(time.Minute))
	if err != nil || first.Generation != 1 || len(first.Items) != 1 {
		t.Fatalf("first sync = %#v %v", first, err)
	}
	repeated, err := repository.PrepareSubscriptionSync(ctx, sub.ControllerID, "99999999-9999-4999-8999-999999999999", now.Add(2*time.Minute))
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatalf("repeat sync changed = %#v %v", repeated, err)
	}
	loaded, err := repository.LoadSubscriptionSync(ctx, sub.ControllerID)
	if err != nil || !reflect.DeepEqual(first, loaded) {
		t.Fatalf("reconnect snapshot = %#v %v", loaded, err)
	}
	if err := repository.RetireSubscription(ctx, sub.OwnerUserID, sub.SubscriptionID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stillFirst, err := repository.PrepareSubscriptionSync(ctx, sub.ControllerID, uuid.NewString(), now.Add(4*time.Minute))
	if err != nil || !reflect.DeepEqual(first, stillFirst) {
		t.Fatalf("catalog mutation replaced inflight = %#v %v", stillFirst, err)
	}
	if err := repository.AcknowledgeSubscriptionSync(ctx, sub.ControllerID, message1, 1, 0, now.Add(5*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong count ack = %v", err)
	}
	if err := repository.AcknowledgeSubscriptionSync(ctx, sub.ControllerID, message1, 1, 1, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AcknowledgeSubscriptionSync(ctx, sub.ControllerID, message1, 1, 1, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("repeat ack = %v", err)
	}
	second, err := repository.PrepareSubscriptionSync(ctx, sub.ControllerID, uuid.NewString(), now.Add(7*time.Minute))
	if err != nil || second.Generation != 2 || len(second.Items) != 0 {
		t.Fatalf("dirty follow-up = %#v %v", second, err)
	}
	if err := repository.AcknowledgeSubscriptionSync(ctx, sub.ControllerID, second.MessageID, second.Generation, 0, now.Add(8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	reconnect, err := repository.PrepareSubscriptionSync(ctx, sub.ControllerID, uuid.NewString(), now.Add(9*time.Minute))
	if err != nil || reconnect.Generation != 3 || len(reconnect.Items) != 0 {
		t.Fatalf("reconnect full set = %#v %v", reconnect, err)
	}
	loadedEmpty, err := repository.LoadSubscriptionSync(ctx, sub.ControllerID)
	if err != nil || !reflect.DeepEqual(reconnect, loadedEmpty) {
		t.Fatalf("empty reconnect snapshot = %#v %v", loadedEmpty, err)
	}
}

func TestLoadSubscriptionSyncRejectsCorruptDigest(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	sub := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.PrepareSubscriptionSync(context.Background(), binding.ControllerID, uuid.NewString(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`DROP TRIGGER relay_sync_set_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`UPDATE relay_subscription_sync_sets SET canonical_digest=? WHERE controller_id=? AND generation=?`, bytes.Repeat([]byte{0xff}, 32), binding.ControllerID, snapshot.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.LoadSubscriptionSync(context.Background(), binding.ControllerID); !errors.Is(err, ErrConflict) {
		t.Fatalf("corrupt digest load = %v", err)
	}
}

func TestConcurrentPrepareSubscriptionSyncCreatesOneGeneration(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	sub := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	const workers = 12
	start := make(chan struct{})
	results := make(chan SyncSnapshot, workers)
	errorsCh := make(chan error, workers)
	for index := 0; index < workers; index++ {
		go func() {
			<-start
			value, err := repository.PrepareSubscriptionSync(context.Background(), binding.ControllerID, uuid.NewString(), now.Add(time.Minute))
			results <- value
			errorsCh <- err
		}()
	}
	close(start)
	var expected SyncSnapshot
	for index := 0; index < workers; index++ {
		value, err := <-results, <-errorsCh
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			expected = value
		} else if !reflect.DeepEqual(expected, value) {
			t.Fatalf("concurrent snapshot %d differs: %#v != %#v", index, value, expected)
		}
	}
	var sets int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_subscription_sync_sets WHERE controller_id=?`, binding.ControllerID).Scan(&sets); err != nil || sets != 1 {
		t.Fatalf("sync sets=%d err=%v", sets, err)
	}
}

func TestSubscriptionCapAndAllOwnerAuthorization(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	ctx := context.Background()
	otherEnrollment := testEnrollment(now)
	otherEnrollment.EnrollmentID = uuid.NewString()
	otherEnrollment.OwnerUserID = "other-owner"
	otherEnrollment.ConnectionID = "connection-b"
	otherEnrollment.InstallationID = 303
	otherEnrollment.RepositoryID = 404
	otherEnrollment.ProtectedPollRef = ProtectedEnrollmentPollRef(otherEnrollment.ControllerID, otherEnrollment.EnrollmentID)
	if err := repository.CreateEnrollment(ctx, otherEnrollment); err != nil {
		t.Fatal(err)
	}
	otherBindingID := uuid.NewString()
	if _, err := repository.CompleteEnrollment(ctx, otherEnrollment.OwnerUserID, otherEnrollment.EnrollmentID, EnrollmentAuthorized, otherBindingID, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < protocol.MaxArrayItems; index++ {
		sub := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: fmt.Sprintf("refs/heads/b%04d", index), State: SubscriptionActive, CreatedAt: now.Add(time.Duration(index) * time.Millisecond)}
		if err := repository.CreateSubscription(ctx, sub); err != nil {
			t.Fatalf("subscription %d: %v", index, err)
		}
	}
	overflow := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/overflow", State: SubscriptionActive, CreatedAt: now.Add(time.Hour)}
	if err := repository.CreateSubscription(ctx, overflow); !errors.Is(err, ErrConflict) {
		t.Fatalf("1001st subscription = %v", err)
	}
	bindings, err := repository.AuthorizedBindings(ctx, 1000)
	if err != nil || len(bindings) != 2 || bindings[0].OwnerUserID != "other-owner" || bindings[1].OwnerUserID != "owner" {
		t.Fatalf("authorized bindings = %#v %v", bindings, err)
	}
	wrongOwner := overflow
	wrongOwner.SubscriptionID, wrongOwner.OwnerUserID, wrongOwner.Ref = uuid.NewString(), "other-owner", "refs/heads/wrong-owner"
	if err := repository.CreateSubscription(ctx, wrongOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner subscription = %v", err)
	}
}

func TestSourceInboxDeduplicatesAfterDurabilityAndSortsACKState(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	ctx := context.Background()
	subs := []RelaySubscription{
		{SubscriptionID: "77777777-7777-4777-8777-777777777777", OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now},
		{SubscriptionID: "66666666-6666-4666-8666-666666666666", OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/release", State: SubscriptionActive, CreatedAt: now},
	}
	for _, sub := range subs {
		if err := repository.CreateSubscription(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	delivery := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, sub := range subs {
		source := testSourceDesired(sub, delivery, 1, now)
		decision, err := repository.CommitSourceDesired(ctx, repositoryTestControllerID, source, now.Add(time.Minute))
		if err != nil || decision.Kind != DecisionAck {
			t.Fatalf("commit source = %#v %v", decision, err)
		}
		source.MessageID = uuid.NewString()
		decision, err = repository.CommitSourceDesired(ctx, repositoryTestControllerID, source, now.Add(2*time.Minute))
		if err != nil || decision.Kind != DecisionAck {
			t.Fatalf("fresh frame target replay = %#v %v", decision, err)
		}
	}
	conflict := testSourceDesired(subs[0], uuid.NewString(), 1, now)
	conflict.ObservedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	decision, err := repository.CommitSourceDesired(ctx, repositoryTestControllerID, conflict, now.Add(3*time.Minute))
	if err != nil || decision != RejectDecision(RejectGenerationConflict) {
		t.Fatalf("generation conflict = %#v %v", decision, err)
	}
	higher := testSourceDesired(subs[0], uuid.NewString(), 3, now.Add(4*time.Minute))
	if decision, err = repository.CommitSourceDesired(ctx, repositoryTestControllerID, higher, now.Add(4*time.Minute)); err != nil || decision.Kind != DecisionAck {
		t.Fatalf("higher generation = %#v %v", decision, err)
	}
	lower := testSourceDesired(subs[0], uuid.NewString(), 2, now.Add(5*time.Minute))
	if decision, err = repository.CommitSourceDesired(ctx, repositoryTestControllerID, lower, now.Add(5*time.Minute)); err != nil || decision != RejectDecision(RejectGenerationConflict) {
		t.Fatalf("lower after higher = %#v %v", decision, err)
	}
	state, err := repository.DurableACKState(ctx, repositoryTestControllerID)
	if err != nil || len(state) != 2 || state[0].SubscriptionID >= state[1].SubscriptionID || state[1].Generation != 3 {
		t.Fatalf("ack state = %#v %v", state, err)
	}
	if err := repository.RetireSubscription(ctx, subs[0].OwnerUserID, subs[0].SubscriptionID, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	higher.MessageID = uuid.NewString()
	if decision, err = repository.CommitSourceDesired(ctx, repositoryTestControllerID, higher, now.Add(7*time.Minute)); err != nil || decision.Kind != DecisionAck {
		t.Fatalf("lost ACK replay after retirement = %#v %v", decision, err)
	}
}

func TestAccessRemovalIsAtomicAndRestorationIsAdvisory(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	ctx := context.Background()
	removed := protocol.AccessChange{Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), now), EventID: uuid.NewString(), InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, ChangeCode: "repository.removed", ObservedAt: now, AckRequired: true}
	decision, err := repository.CommitAccessChange(ctx, repositoryTestControllerID, removed, now.Add(time.Minute))
	if err != nil || decision.Kind != DecisionAck {
		t.Fatalf("remove decision = %#v %v", decision, err)
	}
	got, err := repository.Binding(ctx, binding.OwnerUserID, binding.BindingID)
	if err != nil || got.State != BindingAccessLost {
		t.Fatalf("binding after removal = %#v %v", got, err)
	}
	restored := protocol.AccessChange{Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), now.Add(2*time.Minute)), EventID: uuid.NewString(), InstallationID: binding.InstallationID, ChangeCode: "installation.restored", ObservedAt: now.Add(2 * time.Minute), AckRequired: true}
	if decision, err = repository.CommitAccessChange(ctx, repositoryTestControllerID, restored, now.Add(2*time.Minute)); err != nil || decision.Kind != DecisionAck {
		t.Fatalf("restore advisory = %#v %v", decision, err)
	}
	got, err = repository.Binding(ctx, binding.OwnerUserID, binding.BindingID)
	if err != nil || got.State != BindingAccessLost {
		t.Fatalf("advisory restored binding = %#v %v", got, err)
	}
	if err := repository.MarkBindingRemovalPending(ctx, binding.OwnerUserID, binding.BindingID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkBindingRemoved(ctx, binding.OwnerUserID, binding.BindingID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	removed.MessageID = uuid.NewString()
	if decision, err = repository.CommitAccessChange(ctx, repositoryTestControllerID, removed, now.Add(5*time.Minute)); err != nil || decision.Kind != DecisionAck {
		t.Fatalf("durable replay after topology removal = %#v %v", decision, err)
	}
	newAfterRemoval := removed
	newAfterRemoval.MessageID = uuid.NewString()
	newAfterRemoval.EventID = uuid.NewString()
	if decision, err = repository.CommitAccessChange(ctx, repositoryTestControllerID, newAfterRemoval, now.Add(6*time.Minute)); err != nil || decision != RejectDecision(RejectInvalidEvent) {
		t.Fatalf("new event after topology removal = %#v %v", decision, err)
	}
	var inboxRows int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_access_event_inbox WHERE controller_id=?`, repositoryTestControllerID).Scan(&inboxRows); err != nil || inboxRows != 2 {
		t.Fatalf("access inbox rows=%d err=%v", inboxRows, err)
	}
}

func TestAccessRemovalRollsBackInboxAndBindingTogether(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	sub := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: binding.OwnerUserID, BindingID: binding.BindingID, ControllerID: binding.ControllerID, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`CREATE TRIGGER test_abort_subscription_retire BEFORE UPDATE OF state ON relay_controller_subscriptions WHEN OLD.state='active' BEGIN SELECT RAISE(ABORT,'forced rollback'); END`); err != nil {
		t.Fatal(err)
	}
	removed := protocol.AccessChange{Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), now), EventID: uuid.NewString(), InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, ChangeCode: "repository.removed", ObservedAt: now, AckRequired: true}
	if _, err := repository.CommitAccessChange(context.Background(), repositoryTestControllerID, removed, now.Add(time.Minute)); err == nil {
		t.Fatal("forced access transaction unexpectedly committed")
	}
	got, err := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil || got.State != BindingAuthorized {
		t.Fatalf("binding escaped rollback = %#v %v", got, err)
	}
	var events int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_access_event_inbox WHERE event_id=?`, removed.EventID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("access inbox escaped rollback = %d %v", events, err)
	}
}

func TestInboxCommitsAreControllerScoped(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	bindingA := createSessionBinding(t, repository, now)
	subscriptionA := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: bindingA.OwnerUserID, BindingID: bindingA.BindingID, ControllerID: bindingA.ControllerID, InstallationID: bindingA.InstallationID, RepositoryID: bindingA.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), subscriptionA); err != nil {
		t.Fatal(err)
	}
	controllerB := "99999999-9999-4999-8999-999999999999"
	bindingBID := uuid.NewString()
	if _, err := repository.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(2,?,'active',?,?)`, controllerB, timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, bindingBID, "other-owner", "connection-b", controllerB, bindingA.InstallationID, bindingA.RepositoryID, timestamp(now), timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	subscriptionB := RelaySubscription{SubscriptionID: uuid.NewString(), OwnerUserID: "other-owner", BindingID: bindingBID, ControllerID: controllerB, InstallationID: bindingA.InstallationID, RepositoryID: bindingA.RepositoryID, Ref: "refs/heads/main", State: SubscriptionActive, CreatedAt: now}
	if err := repository.CreateSubscription(context.Background(), subscriptionB); err != nil {
		t.Fatal(err)
	}

	misrouted := testSourceDesired(subscriptionA, uuid.NewString(), 1, now)
	decision, err := repository.CommitSourceDesired(context.Background(), controllerB, misrouted, now.Add(time.Minute))
	if err != nil || decision != RejectDecision(RejectUnknownSubscription) {
		t.Fatalf("misrouted source = %#v %v", decision, err)
	}
	var sourceRows int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_source_event_inbox`).Scan(&sourceRows); err != nil || sourceRows != 0 {
		t.Fatalf("misrouted source rows=%d err=%v", sourceRows, err)
	}

	removed := protocol.AccessChange{Envelope: protocol.NewEnvelope(protocol.TypeAccessChange, uuid.NewString(), now), EventID: uuid.NewString(), InstallationID: bindingA.InstallationID, RepositoryID: bindingA.RepositoryID, ChangeCode: "repository.removed", ObservedAt: now, AckRequired: true}
	decision, err = repository.CommitAccessChange(context.Background(), controllerB, removed, now.Add(2*time.Minute))
	if err != nil || decision.Kind != DecisionAck {
		t.Fatalf("controller B access = %#v %v", decision, err)
	}
	gotA, err := repository.Binding(context.Background(), bindingA.OwnerUserID, bindingA.BindingID)
	if err != nil || gotA.State != BindingAuthorized {
		t.Fatalf("controller A binding mutated = %#v %v", gotA, err)
	}
	gotB, err := repository.Binding(context.Background(), "other-owner", bindingBID)
	if err != nil || gotB.State != BindingAccessLost {
		t.Fatalf("controller B binding = %#v %v", gotB, err)
	}
	var activeA, activeB, accessRows int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_subscriptions WHERE controller_id=? AND state='active'`, bindingA.ControllerID).Scan(&activeA); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_subscriptions WHERE controller_id=? AND state='active'`, controllerB).Scan(&activeB); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_access_event_inbox WHERE controller_id=?`, controllerB).Scan(&accessRows); err != nil {
		t.Fatal(err)
	}
	if activeA != 1 || activeB != 0 || accessRows != 1 {
		t.Fatalf("controller scope activeA=%d activeB=%d accessRows=%d", activeA, activeB, accessRows)
	}
	sameEventOtherController := removed
	sameEventOtherController.MessageID = uuid.NewString()
	decision, err = repository.CommitAccessChange(context.Background(), bindingA.ControllerID, sameEventOtherController, now.Add(3*time.Minute))
	if err != nil || decision.Kind != DecisionAck {
		t.Fatalf("same event ID for controller A = %#v %v", decision, err)
	}
	var sameEventRows int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_access_event_inbox WHERE event_id=?`, removed.EventID).Scan(&sameEventRows); err != nil || sameEventRows != 2 {
		t.Fatalf("controller-scoped same event rows=%d err=%v", sameEventRows, err)
	}
	if _, err := repository.CommitAccessChange(context.Background(), "88888888-8888-4888-8888-888888888888", removed, now.Add(3*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown controller access = %v", err)
	}
}

func TestOutboundCommandReplayAndAtomicRotationCompletion(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	ctx := context.Background()
	command := OutboundCommand{ControllerID: binding.ControllerID, MessageID: uuid.NewString(), CommandType: CommandBindingRemove, BindingID: binding.BindingID, Stage: "remove", SentAt: now, Digest: sha256.Sum256([]byte("binding remove")), State: CommandPrepared}
	if _, err := repository.PrepareControlCommand(ctx, command); !errors.Is(err, ErrConflict) {
		t.Fatalf("authorized binding command = %v", err)
	}
	if err := repository.MarkBindingRemovalPending(ctx, binding.OwnerUserID, binding.BindingID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	prepared, err := repository.PrepareControlCommand(ctx, command)
	if err != nil || prepared != command {
		t.Fatalf("prepare command = %#v %v", prepared, err)
	}
	if replay, err := repository.PrepareControlCommand(ctx, command); err != nil || replay != command {
		t.Fatalf("command replay = %#v %v", replay, err)
	}
	restarted := command
	restarted.MessageID = uuid.NewString()
	restarted.SentAt = now.Add(2 * time.Minute)
	if replay, err := repository.PrepareControlCommand(ctx, restarted); err != nil || replay != command {
		t.Fatalf("new-message crash replay = %#v %v", replay, err)
	}
	changed := command
	changed.MessageID = uuid.NewString()
	changed.Digest = sha256.Sum256([]byte("changed"))
	if _, err := repository.PrepareControlCommand(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	if _, err := repository.db.Exec(`UPDATE relay_outbound_commands SET canonical_digest=? WHERE controller_id=? AND message_id=?`, changed.Digest[:], command.ControllerID, command.MessageID); err == nil {
		t.Fatal("immutable command digest was updated")
	}
	if err := repository.CompleteControlCommand(ctx, command.ControllerID, command.MessageID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var commandCount int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_outbound_commands WHERE controller_id=? AND binding_id=? AND stage='remove'`, command.ControllerID, command.BindingID).Scan(&commandCount); err != nil || commandCount != 1 {
		t.Fatalf("binding command count=%d err=%v", commandCount, err)
	}

	if err := repository.CreateKey(ctx, testPendingKey(now)); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: repositoryTestNewKeyID, State: RotationPrepare, ExpiresAt: now.Add(time.Hour), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateRotation(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	stages := [][2]string{{RotationPrepare, RotationPropose}, {RotationPropose, RotationConfirm}, {RotationConfirm, RotationNewKeyAuth}, {RotationNewKeyAuth, RotationFinalize}}
	for index, stage := range stages {
		if err := repository.CASRotationState(ctx, repositoryTestControllerID, repositoryTestRotationID, stage[0], stage[1], "", now.Add(time.Duration(index+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.CompleteRotationAfterReady(ctx, repositoryTestControllerID, repositoryTestRotationID, 1, 1, now.Add(10*time.Minute)); !errors.Is(err, ErrState) {
		t.Fatalf("undurable ready = %v", err)
	}
	readyAt := now.Add(9 * time.Minute)
	readyStatus := SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestNewKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err := repository.AdvanceSessionStatus(ctx, 0, 0, readyStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`CREATE TRIGGER test_abort_new_key_activation BEFORE UPDATE OF state ON relay_controller_keys WHEN OLD.key_id='` + repositoryTestNewKeyID + `' AND NEW.state='active' BEGIN SELECT RAISE(ABORT,'forced rotation rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteRotationAfterReady(ctx, repositoryTestControllerID, repositoryTestRotationID, 1, 1, now.Add(10*time.Minute)); err == nil {
		t.Fatal("forced rotation transaction unexpectedly committed")
	}
	oldKey, err := repository.Key(ctx, repositoryTestControllerID, repositoryTestKeyID)
	if err != nil || oldKey.State != KeyActive {
		t.Fatalf("old key escaped rollback = %#v %v", oldKey, err)
	}
	if _, err := repository.db.Exec(`DROP TRIGGER test_abort_new_key_activation`); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteRotationAfterReady(ctx, repositoryTestControllerID, repositoryTestRotationID, 1, 1, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, active, err := repository.ActiveIdentity(ctx)
	if err != nil || active.KeyID != repositoryTestNewKeyID {
		t.Fatalf("active rotated key = %#v %v", active, err)
	}
}

func TestPendingControlCommandsUsesPendingReplayIndexWithoutSort(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	ctx := context.Background()

	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bindingStatement, err := tx.PrepareContext(ctx, `INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,'owner','connection-a',?,?,?,'removal_pending',?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	commandStatement, err := tx.PrepareContext(ctx, `INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest,state,completed_at) VALUES(?,?,'binding.remove',?,NULL,'remove',?,?,'completed',?)`)
	if err != nil {
		_ = bindingStatement.Close()
		t.Fatal(err)
	}
	for index := 0; index < 512; index++ {
		bindingID := uuid.NewString()
		messageID := uuid.NewString()
		sentAt := now.Add(-time.Duration(512-index) * time.Minute)
		digest := sha256.Sum256([]byte(fmt.Sprintf("completed replay history %d", index)))
		if _, err := bindingStatement.ExecContext(ctx, bindingID, binding.ControllerID, 10_000+index, 20_000+index, timestamp(sentAt), timestamp(sentAt), timestamp(sentAt)); err != nil {
			t.Fatalf("insert completed history binding %d: %v", index, err)
		}
		if _, err := commandStatement.ExecContext(ctx, binding.ControllerID, messageID, bindingID, timestamp(sentAt), digest[:], timestamp(sentAt.Add(time.Second))); err != nil {
			t.Fatalf("insert completed history command %d: %v", index, err)
		}
	}
	if err := bindingStatement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := commandStatement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	prepared := []OutboundCommand{
		{ControllerID: binding.ControllerID, MessageID: "70000000-0000-4000-8000-000000000002", CommandType: CommandBindingRemove, BindingID: "80000000-0000-4000-8000-000000000002", Stage: "remove", SentAt: now.Add(2 * time.Minute), Digest: sha256.Sum256([]byte("pending replay second")), State: CommandPrepared},
		{ControllerID: binding.ControllerID, MessageID: "70000000-0000-4000-8000-000000000001", CommandType: CommandBindingRemove, BindingID: "80000000-0000-4000-8000-000000000001", Stage: "remove", SentAt: now.Add(time.Minute), Digest: sha256.Sum256([]byte("pending replay first")), State: CommandPrepared},
		{ControllerID: binding.ControllerID, MessageID: "70000000-0000-4000-8000-000000000003", CommandType: CommandBindingRemove, BindingID: "80000000-0000-4000-8000-000000000003", Stage: "remove", SentAt: now.Add(2 * time.Minute), Digest: sha256.Sum256([]byte("pending replay tie")), State: CommandPrepared},
	}
	for index, command := range prepared {
		if _, err := repository.db.ExecContext(ctx, `INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,'owner','connection-a',?,?,?,'removal_pending',?,?,?)`, command.BindingID, command.ControllerID, 20_000+index, 30_000+index, timestamp(command.SentAt), timestamp(command.SentAt), timestamp(command.SentAt)); err != nil {
			t.Fatalf("insert prepared binding %d: %v", index, err)
		}
		if got, err := repository.PrepareControlCommand(ctx, command); err != nil || got != command {
			t.Fatalf("prepare pending command %d = %#v %v", index, got, err)
		}
	}

	rows, err := repository.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND state='prepared' AND command_type<>'key.rotation.confirm' ORDER BY sent_at,message_id LIMIT ?`, binding.ControllerID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, ignored int
		var detail string
		if err := rows.Scan(&id, &parent, &ignored, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joinedPlan := strings.ToLower(strings.Join(plan, "\n"))
	if !strings.Contains(joinedPlan, "using index relay_outbound_pending_replay") {
		t.Fatalf("pending replay plan did not select partial index: %s", joinedPlan)
	}
	if strings.Contains(joinedPlan, "temp b-tree") {
		t.Fatalf("pending replay plan requires temporary sort: %s", joinedPlan)
	}

	got, err := repository.PendingControlCommands(ctx, binding.ControllerID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].MessageID != prepared[1].MessageID || got[1].MessageID != prepared[0].MessageID {
		t.Fatalf("bounded pending replay = %#v", got)
	}
	for _, command := range got {
		if command.State != CommandPrepared {
			t.Fatalf("replayed non-pending command = %#v", command)
		}
	}
	all, err := repository.PendingControlCommands(ctx, binding.ControllerID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].MessageID != prepared[1].MessageID || all[1].MessageID != prepared[0].MessageID || all[2].MessageID != prepared[2].MessageID {
		t.Fatalf("ordered pending replay = %#v", all)
	}
}

func TestConcurrentControlCommandPreparationReturnsOneOriginalEnvelope(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	if err := repository.MarkBindingRemovalPending(context.Background(), binding.OwnerUserID, binding.BindingID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("concurrent binding removal"))
	type outcome struct {
		command OutboundCommand
		err     error
	}
	const workers = 16
	start := make(chan struct{})
	results := make(chan outcome, workers)
	for index := 0; index < workers; index++ {
		index := index
		go func() {
			<-start
			requested := OutboundCommand{ControllerID: binding.ControllerID, MessageID: uuid.NewString(), CommandType: CommandBindingRemove, BindingID: binding.BindingID, Stage: "remove", SentAt: now.Add(time.Duration(index) * time.Second), Digest: digest, State: CommandPrepared}
			command, err := repository.PrepareControlCommand(context.Background(), requested)
			results <- outcome{command: command, err: err}
		}()
	}
	close(start)
	var original OutboundCommand
	for index := 0; index < workers; index++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if index == 0 {
			original = result.command
		} else if result.command != original {
			t.Fatalf("concurrent command changed: %#v != %#v", result.command, original)
		}
	}
	var count int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_outbound_commands WHERE controller_id=? AND binding_id=? AND stage='remove'`, binding.ControllerID, binding.BindingID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent command count=%d err=%v", count, err)
	}
	conflict := original
	conflict.MessageID = uuid.NewString()
	conflict.Digest = sha256.Sum256([]byte("conflicting digest"))
	if _, err := repository.PrepareControlCommand(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting aggregate retry = %v", err)
	}
	invalidType := original
	invalidType.MessageID = uuid.NewString()
	invalidType.CommandType = CommandRotationPropose
	if _, err := repository.PrepareControlCommand(context.Background(), invalidType); !errors.Is(err, ErrInvalid) {
		t.Fatalf("conflicting command type = %v", err)
	}
}

func createSessionBinding(t *testing.T, repository *Repository, now time.Time) InstallationBinding {
	t.Helper()
	createTestIdentity(t, repository, now)
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
	return binding
}

func testSourceDesired(sub RelaySubscription, deliveryID string, generation uint64, at time.Time) protocol.SourceDesired {
	return protocol.SourceDesired{Envelope: protocol.NewEnvelope(protocol.TypeSourceDesired, uuid.NewString(), at), DeliveryID: deliveryID, SubscriptionID: sub.SubscriptionID, Generation: generation, InstallationID: sub.InstallationID, RepositoryID: sub.RepositoryID, Ref: sub.Ref, ObservedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAt: at}
}

func TestSyncDigestIsCanonicalForOrderedItems(t *testing.T) {
	items := []protocol.Subscription{{SubscriptionID: uuid.NewString(), InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main"}}
	one, err := syncDigest(items)
	if err != nil {
		t.Fatal(err)
	}
	two, err := syncDigest(append([]protocol.Subscription(nil), items...))
	if err != nil || !bytes.Equal(one[:], two[:]) {
		t.Fatalf("digest mismatch %x %x %v", one, two, err)
	}
}
