package wss

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

type sourceDecisionCall struct {
	command        store.SessionCommand
	subscriptionID string
	generation     uint64
	targetMessage  string
	accepted       bool
	code           string
}

type accessDecisionCall struct {
	command       store.SessionCommand
	eventID       string
	targetMessage string
	accepted      bool
	code          string
}

type bindingCall struct {
	command                      store.SessionCommand
	installationID, repositoryID int64
}

type keyCall struct {
	command             store.SessionCommand
	controllerID, keyID string
}

type proposalCall struct {
	command store.SessionCommand
	input   store.RotationInput
}

type confirmationCall struct {
	command               store.SessionCommand
	rotationID, signature string
}

type decisionProtocolErrorCall struct {
	command store.SessionCommand
	code    string
}

type durableDecisionCommand struct {
	command store.SessionCommand
	result  store.SessionCommandResult
}

type activeStore struct {
	*fakeStateStore
	mu                 sync.Mutex
	desired            []store.DesiredState
	access             []store.PendingAccess
	sourceResult       store.SessionCommandResult
	accessResult       store.SessionCommandResult
	sourceCalls        []sourceDecisionCall
	accessCalls        []accessDecisionCall
	protocolCalls      []decisionProtocolErrorCall
	protocolLedger     map[string]durableDecisionCommand
	protocolResult     store.SessionCommandResult
	protocolErr        error
	controllerResult   store.SessionCommandResult
	controllerErr      error
	controllerCalls    int
	bindingResult      store.SessionCommandResult
	bindingErr         error
	bindingCalls       []bindingCall
	keyResult          store.SessionCommandResult
	keyErr             error
	keyCalls           []keyCall
	proposalResult     store.SessionCommandResult
	proposalErr        error
	proposalCalls      []proposalCall
	confirmationResult store.SessionCommandResult
	confirmationErr    error
	confirmationCalls  []confirmationCall
	rotationResult     store.SessionCommandResult
	rotationErr        error
	rotationCalls      int
	renewResult        store.Lease
	renewErr           error
	renewCalls         int
	desiredLease       store.Lease
	accessLease        store.Lease
	denyPending        bool
	pendingAccessHook  func(context.Context) error
	bindingHook        func(context.Context) error
	callOrder          []string
}

func newActiveStore() *activeStore {
	return &activeStore{
		fakeStateStore: &fakeStateStore{},
		sourceResult:   store.SessionCommandResult{Kind: store.ResultDecisionApplied},
		accessResult:   store.SessionCommandResult{Kind: store.ResultDecisionApplied},
		protocolLedger: make(map[string]durableDecisionCommand),
	}
}

func (s *activeStore) PendingDesired(_ context.Context, lease store.Lease, limit int) ([]store.DesiredState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callOrder = append(s.callOrder, "desired")
	s.desiredLease = lease
	if s.denyPending {
		return nil, nil
	}
	limit = min(limit, len(s.desired))
	return append([]store.DesiredState(nil), s.desired[:limit]...), nil
}

func (s *activeStore) PendingAccess(ctx context.Context, lease store.Lease, limit int) ([]store.PendingAccess, error) {
	if s.pendingAccessHook != nil {
		if err := s.pendingAccessHook(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callOrder = append(s.callOrder, "access")
	s.accessLease = lease
	if s.denyPending {
		return nil, nil
	}
	limit = min(limit, len(s.access))
	return append([]store.PendingAccess(nil), s.access[:limit]...), nil
}

func (s *activeStore) ApplySubscriptionsSync(_ context.Context, _ store.Lease, _ store.SessionCommand, generation uint64, subscriptions []store.Subscription) (store.SessionCommandResult, error) {
	return store.SessionCommandResult{Kind: store.ResultSubscriptionsSynced, Generation: generation, Count: uint32(len(subscriptions))}, nil
}

func (s *activeStore) ApplySourceDecision(_ context.Context, _ store.Lease, command store.SessionCommand, subscriptionID string, generation uint64, targetMessage string, accepted bool, code string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceCalls = append(s.sourceCalls, sourceDecisionCall{command: command, subscriptionID: subscriptionID, generation: generation, targetMessage: targetMessage, accepted: accepted, code: code})
	s.protocolLedger[command.MessageID] = durableDecisionCommand{command: command, result: s.sourceResult}
	return s.sourceResult, nil
}

func (s *activeStore) ApplyAccessDecision(_ context.Context, _ store.Lease, command store.SessionCommand, eventID, targetMessage string, accepted bool, code string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessCalls = append(s.accessCalls, accessDecisionCall{command: command, eventID: eventID, targetMessage: targetMessage, accepted: accepted, code: code})
	s.protocolLedger[command.MessageID] = durableDecisionCommand{command: command, result: s.accessResult}
	return s.accessResult, nil
}

func (s *activeStore) ApplyDecisionProtocolError(_ context.Context, _ store.Lease, command store.SessionCommand, code string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.protocolCalls = append(s.protocolCalls, decisionProtocolErrorCall{command: command, code: code})
	if s.protocolErr != nil {
		return store.SessionCommandResult{}, s.protocolErr
	}
	if durable, ok := s.protocolLedger[command.MessageID]; ok {
		if durable.command != command {
			return store.SessionCommandResult{}, store.ErrConflict
		}
		return durable.result, nil
	}
	result := s.protocolResult
	if result.Kind == "" {
		result = store.SessionCommandResult{Kind: store.ResultProtocolError, ErrorCode: code}
	}
	s.protocolLedger[command.MessageID] = durableDecisionCommand{command: command, result: result}
	return result, nil
}

func (s *activeStore) ApplyControllerRevocation(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controllerCalls++
	return s.controllerResult, s.controllerErr
}

func (s *activeStore) ApplyRotationFinalization(context.Context, store.Lease, store.SessionCommand, string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotationCalls++
	return s.rotationResult, s.rotationErr
}

func (s *activeStore) ApplyBindingRemoval(ctx context.Context, _ store.Lease, command store.SessionCommand, installationID, repositoryID int64) (store.SessionCommandResult, error) {
	if s.bindingHook != nil {
		if err := s.bindingHook(ctx); err != nil {
			return store.SessionCommandResult{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindingCalls = append(s.bindingCalls, bindingCall{command: command, installationID: installationID, repositoryID: repositoryID})
	return s.bindingResult, s.bindingErr
}

func (s *activeStore) ApplyKeyRevocation(_ context.Context, _ store.Lease, command store.SessionCommand, controllerID, keyID string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyCalls = append(s.keyCalls, keyCall{command: command, controllerID: controllerID, keyID: keyID})
	return s.keyResult, s.keyErr
}

func (s *activeStore) ApplyRotationProposal(_ context.Context, _ store.Lease, command store.SessionCommand, input store.RotationInput, _ time.Duration) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	input.NewPublicKey = append([]byte(nil), input.NewPublicKey...)
	s.proposalCalls = append(s.proposalCalls, proposalCall{command: command, input: input})
	return s.proposalResult, s.proposalErr
}

func (s *activeStore) ApplyRotationConfirmation(_ context.Context, _ store.Lease, command store.SessionCommand, rotationID, signature string) (store.SessionCommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confirmationCalls = append(s.confirmationCalls, confirmationCall{command: command, rotationID: rotationID, signature: signature})
	return s.confirmationResult, s.confirmationErr
}

func (s *activeStore) RenewLease(_ context.Context, lease store.Lease, _ time.Duration) (store.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callOrder = append(s.callOrder, "renew")
	s.renewCalls++
	if s.renewResult.LeaseID == "" {
		s.renewResult = lease
		s.renewResult.ExpiresAt = lease.ExpiresAt.Add(time.Minute)
	}
	return s.renewResult, s.renewErr
}

type activeHarness struct {
	session *session
	state   *activeState
	frames  []protocol.Frame
	mu      sync.Mutex
	cancel  context.CancelFunc
}

func newActiveHarness(t *testing.T, state StateStore, maximum int) *activeHarness {
	t.Helper()
	config := DefaultConfig()
	config.StoreTimeout = 100 * time.Millisecond
	config.WriteTimeout = 100 * time.Millisecond
	config.MaxOutstanding = maximum
	config.OutboundQueue = maximum
	handler, err := NewHandler(state, config, Options{Now: func() time.Time { return commandTime }})
	if err != nil {
		t.Fatal(err)
	}
	harness := &activeHarness{}
	conn := &fakeSocket{subprotocol: protocol.Subprotocol, read: func(ctx context.Context, _ int) (websocket.MessageType, []byte, error) {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}}
	conn.onWrite = func(frame protocol.Frame) {
		harness.mu.Lock()
		harness.frames = append(harness.frames, frame)
		harness.mu.Unlock()
	}
	harness.session = newSession(handler, conn)
	harness.session.controllerID = activeUUID(1)
	harness.session.keyID = activeUUID(2)
	harness.session.sessionID = activeUUID(3)
	harness.session.lease = store.Lease{ControllerID: activeUUID(1), SessionID: activeUUID(3), LeaseID: activeUUID(4), Fence: 1, ExpiresAt: commandTime.Add(time.Minute)}
	harness.session.sessionUntil = commandTime.Add(config.SessionLifetime)
	harness.state = newActiveState()
	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	go func() {
		defer close(harness.session.writerDone)
		harness.session.writeLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-harness.session.writerDone
	})
	return harness
}

func (h *activeHarness) snapshot() []protocol.Frame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]protocol.Frame(nil), h.frames...)
}

func activeUUID(value int) string {
	return fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012x", value)
}

func desiredState(subscriptionID string, generation uint64) store.DesiredState {
	return store.DesiredState{
		DeliveryID: activeUUID(100 + int(generation)), ControllerID: activeUUID(1), SubscriptionID: subscriptionID,
		Generation: generation, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main",
		SHA: strings.Repeat(fmt.Sprintf("%x", generation%16), 40), ObservedAt: commandTime,
	}
}

func pendingAccess(eventID string) store.PendingAccess {
	return store.PendingAccess{EventID: eventID, DeliveryID: activeUUID(300), ControllerID: activeUUID(1), InstallationID: 10, RepositoryID: 20, ChangeCode: "repository.removed", ObservedAt: commandTime}
}

func TestPollDeliveriesRequiresSubscriptionAndKeepsOneOutstandingPerTarget(t *testing.T) {
	state := newActiveStore()
	subscriptionID, eventID := activeUUID(10), activeUUID(20)
	state.desired = []store.DesiredState{desiredState(subscriptionID, 1)}
	state.access = []store.PendingAccess{pendingAccess(eventID)}
	harness := newActiveHarness(t, state, 4)

	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	if frames := harness.snapshot(); len(frames) != 0 {
		t.Fatalf("frames before full sync=%d", len(frames))
	}

	harness.session.subscriptions[subscriptionID] = struct{}{}
	harness.session.syncComplete = true
	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	frames := harness.snapshot()
	if len(frames) != 2 {
		t.Fatalf("repeated polls wrote %d frames, want one source and one access", len(frames))
	}
	if harness.state.outstanding() != 2 || len(harness.state.sourceCurrent) != 1 || len(harness.state.accessCurrent) != 1 {
		t.Fatalf("outstanding=%d source=%d access=%d", harness.state.outstanding(), len(harness.state.sourceCurrent), len(harness.state.accessCurrent))
	}
}

func TestPollDeliveriesUsesExactLeaseAndSendsNothingAfterTakeoverOrRevocation(t *testing.T) {
	for _, name := range []string{"lease takeover", "session or key revocation"} {
		t.Run(name, func(t *testing.T) {
			state := newActiveStore()
			subscriptionID := activeUUID(25)
			state.desired = []store.DesiredState{desiredState(subscriptionID, 1)}
			state.access = []store.PendingAccess{pendingAccess(activeUUID(26))}
			state.denyPending = true
			harness := newActiveHarness(t, state, 2)
			harness.session.syncComplete = true
			harness.session.subscriptions[subscriptionID] = struct{}{}
			wantLease := harness.session.lease
			if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
				t.Fatal(failure)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.desiredLease != wantLease || state.accessLease != wantLease || len(harness.snapshot()) != 0 {
				t.Fatalf("desired lease=%+v access lease=%+v frames=%d", state.desiredLease, state.accessLease, len(harness.snapshot()))
			}
		})
	}
}

func TestPollDeliveriesEnforcesGlobalOutstandingAndPerCycleBounds(t *testing.T) {
	state := newActiveStore()
	for index := 0; index < 12; index++ {
		subscriptionID := activeUUID(30 + index)
		state.desired = append(state.desired, desiredState(subscriptionID, 1))
	}
	harness := newActiveHarness(t, state, 10)
	for _, item := range state.desired {
		harness.session.subscriptions[item.SubscriptionID] = struct{}{}
	}
	harness.session.syncComplete = true
	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	if frames := harness.snapshot(); len(frames) != maxDeliveryFramesPerPoll {
		t.Fatalf("poll frames=%d want=%d", len(frames), maxDeliveryFramesPerPoll)
	}
	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	if frames := harness.snapshot(); len(frames) != 10 || harness.state.outstanding() != 10 {
		t.Fatalf("frames=%d outstanding=%d", len(frames), harness.state.outstanding())
	}
}

func TestPollDeliveriesKeepsAccessFairUnderSustainedSourceBacklog(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 8)
	harness.session.syncComplete = true
	for cycle := 0; cycle < 10; cycle++ {
		storeState.mu.Lock()
		storeState.desired = nil
		storeState.access = nil
		for index := 0; index < 8; index++ {
			subscriptionID := activeUUID(2000 + cycle*20 + index)
			storeState.desired = append(storeState.desired, desiredState(subscriptionID, 1))
			storeState.access = append(storeState.access, pendingAccess(activeUUID(2010+cycle*20+index)))
			harness.session.subscriptions[subscriptionID] = struct{}{}
		}
		storeState.mu.Unlock()
		before := len(harness.snapshot())
		if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
			t.Fatal(failure)
		}
		cycleFrames := harness.snapshot()[before:]
		accessCount, sourceCount := 0, 0
		for _, frame := range cycleFrames {
			switch frame.(type) {
			case *protocol.AccessChange:
				accessCount++
			case *protocol.SourceDesired:
				sourceCount++
			}
		}
		if len(cycleFrames) != maxDeliveryFramesPerPoll || accessCount == 0 || sourceCount == 0 {
			t.Fatalf("cycle=%d frames=%d access=%d source=%d", cycle, len(cycleFrames), accessCount, sourceCount)
		}
		for _, target := range harness.state.targets {
			if target.state == targetCurrent {
				harness.session.markTerminal(harness.state, target)
			}
		}
	}
}

func TestPollDeliveriesSkipsPersistentAccessPrefixUnderPartialCapacity(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 2)
	harness.session.syncComplete = true
	oldEventID := activeUUID(2150)
	firstSubscriptionID := activeUUID(2151)
	storeState.access = []store.PendingAccess{pendingAccess(oldEventID)}
	storeState.desired = []store.DesiredState{desiredState(firstSubscriptionID, 1)}
	harness.session.subscriptions[firstSubscriptionID] = struct{}{}

	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	if harness.state.accessCurrent[oldEventID] == "" || harness.state.sourceCurrent[firstSubscriptionID] == "" {
		t.Fatal("initial poll did not fill one persistent access and one source slot")
	}
	harness.session.markTerminal(harness.state, harness.state.targets[harness.state.sourceCurrent[firstSubscriptionID]])

	newEventID := activeUUID(2152)
	storeState.mu.Lock()
	storeState.access = append(storeState.access, pendingAccess(newEventID))
	storeState.mu.Unlock()
	for cycle := 0; cycle < 2 && harness.state.accessCurrent[newEventID] == ""; cycle++ {
		subscriptionID := activeUUID(2160 + cycle)
		storeState.mu.Lock()
		storeState.desired = []store.DesiredState{desiredState(subscriptionID, 1)}
		storeState.mu.Unlock()
		harness.session.subscriptions[subscriptionID] = struct{}{}
		if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
			t.Fatal(failure)
		}
		if messageID := harness.state.sourceCurrent[subscriptionID]; messageID != "" {
			harness.session.markTerminal(harness.state, harness.state.targets[messageID])
		}
	}
	if harness.state.accessCurrent[oldEventID] == "" {
		t.Fatal("persistent old access target was unexpectedly terminalized")
	}
	if harness.state.accessCurrent[newEventID] == "" {
		t.Fatal("new access target remained hidden behind the persistent ordered prefix")
	}
}

func TestPollDeliveriesAlternatesClassesWithOneAvailableSlot(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 1)
	harness.session.syncComplete = true
	sourceFrames, accessFrames := 0, 0
	for cycle := 0; cycle < 8; cycle++ {
		subscriptionID := activeUUID(2200 + cycle)
		storeState.mu.Lock()
		storeState.desired = []store.DesiredState{desiredState(subscriptionID, 1)}
		storeState.access = []store.PendingAccess{pendingAccess(activeUUID(2300 + cycle))}
		storeState.mu.Unlock()
		harness.session.subscriptions[subscriptionID] = struct{}{}
		before := len(harness.snapshot())
		if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
			t.Fatal(failure)
		}
		frames := harness.snapshot()
		if len(frames) != before+1 {
			t.Fatalf("cycle=%d wrote %d frames, want one", cycle, len(frames)-before)
		}
		switch frames[before].(type) {
		case *protocol.SourceDesired:
			sourceFrames++
		case *protocol.AccessChange:
			accessFrames++
		default:
			t.Fatalf("cycle=%d frame=%T", cycle, frames[before])
		}
		for _, target := range harness.state.targets {
			if target.state == targetCurrent {
				harness.session.markTerminal(harness.state, target)
			}
		}
	}
	if sourceFrames != 4 || accessFrames != 4 {
		t.Fatalf("source=%d access=%d, want exact alternation", sourceFrames, accessFrames)
	}
}

func TestSourceSupersessionLateACKMismatchAndDurableReplay(t *testing.T) {
	storeState := newActiveStore()
	subscriptionID := activeUUID(50)
	storeState.desired = []store.DesiredState{desiredState(subscriptionID, 1)}
	harness := newActiveHarness(t, storeState, 1)
	harness.session.subscriptions[subscriptionID] = struct{}{}
	harness.session.syncComplete = true
	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	first := harness.snapshot()[0].(*protocol.SourceDesired)

	storeState.mu.Lock()
	storeState.desired = []store.DesiredState{desiredState(subscriptionID, 2)}
	storeState.mu.Unlock()
	if failure := harness.session.pollDeliveries(context.Background(), harness.state); failure != nil {
		t.Fatal(failure)
	}
	second := harness.snapshot()[1].(*protocol.SourceDesired)
	if first.MessageID == second.MessageID || harness.state.targets[first.MessageID].state != targetStale {
		t.Fatal("new generation did not supersede the old in-memory target")
	}

	mismatch := &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(500), commandTime), TargetMessageID: second.MessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 1}}
	if failure := harness.session.handleDecision(context.Background(), harness.state, mismatch, true, ""); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	if len(storeState.sourceCalls) != 0 || len(storeState.protocolCalls) != 1 || storeState.protocolCalls[0].code != "target_mismatch" {
		t.Fatal("target mismatch reached the durable decision mutation")
	}
	storeState.mu.Unlock()

	late := &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(501), commandTime), TargetMessageID: first.MessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 1}}
	if failure := harness.session.handleDecision(context.Background(), harness.state, late, true, ""); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	if len(storeState.sourceCalls) != 0 || len(storeState.protocolCalls) != 2 || storeState.protocolCalls[1].code != "stale_target" {
		t.Fatalf("late source calls=%+v protocol=%+v", storeState.sourceCalls, storeState.protocolCalls)
	}
	storeState.mu.Unlock()

	// A restart loses the tombstone and proposes unknown_target locally, but the
	// exact durable command replay must preserve the earlier stale_target code.
	if failure := harness.session.handleDecision(context.Background(), newActiveState(), late, true, ""); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	if len(storeState.sourceCalls) != 0 || len(storeState.protocolCalls) != 3 || storeState.protocolCalls[2].code != "unknown_target" {
		t.Fatalf("restart source calls=%+v protocol=%+v", storeState.sourceCalls, storeState.protocolCalls)
	}
	storeState.mu.Unlock()

	ack := &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(502), commandTime), TargetMessageID: second.MessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 2}}
	if failure := harness.session.handleDecision(context.Background(), harness.state, ack, true, ""); failure != nil {
		t.Fatal(failure)
	}
	if _, exists := harness.state.sourceCurrent[subscriptionID]; exists {
		t.Fatal("durably ACKed source remained outstanding")
	}

	command, err := canonicalSessionCommand(ack, harness.session.handler.config.MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if failure := harness.session.handleDecision(context.Background(), newActiveState(), ack, true, ""); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	defer storeState.mu.Unlock()
	if len(storeState.protocolCalls) != 4 || storeState.protocolCalls[3].command != command || storeState.protocolCalls[3].code != "unknown_target" {
		t.Fatal("post-restart exact decision replay was not checked durably")
	}
}

func TestAccessDecisionAndReconnectResendUseFreshTarget(t *testing.T) {
	storeState := newActiveStore()
	eventID := activeUUID(60)
	storeState.access = []store.PendingAccess{pendingAccess(eventID)}
	firstHarness := newActiveHarness(t, storeState, 2)
	firstHarness.session.syncComplete = true
	if failure := firstHarness.session.pollDeliveries(context.Background(), firstHarness.state); failure != nil {
		t.Fatal(failure)
	}
	first := firstHarness.snapshot()[0].(*protocol.AccessChange)

	secondHarness := newActiveHarness(t, storeState, 2)
	secondHarness.session.syncComplete = true
	if failure := secondHarness.session.pollDeliveries(context.Background(), secondHarness.state); failure != nil {
		t.Fatal(failure)
	}
	second := secondHarness.snapshot()[0].(*protocol.AccessChange)
	if first.MessageID == second.MessageID {
		t.Fatal("reconnect reused an outbound envelope ID")
	}
	ack := &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(601), commandTime), TargetMessageID: second.MessageID, Access: &protocol.AccessTarget{EventID: eventID}}
	if failure := secondHarness.session.handleDecision(context.Background(), secondHarness.state, ack, true, ""); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	defer storeState.mu.Unlock()
	if len(storeState.accessCalls) != 1 || storeState.accessCalls[0].targetMessage != second.MessageID || !storeState.accessCalls[0].accepted {
		t.Fatalf("access calls=%+v", storeState.accessCalls)
	}
}

func TestActiveTargetTombstonesStayBoundedUnderLongLivedCurrentChurn(t *testing.T) {
	state := newActiveState()
	const maximum = 4
	for index := 0; index < maximum; index++ {
		target := &deliveryTarget{kind: targetSource, state: targetCurrent, messageID: fmt.Sprintf("current-%d", index), subscriptionID: fmt.Sprintf("subscription-%d", index), generation: 1}
		state.remember(target, maximum)
		state.sourceCurrent[target.subscriptionID] = target.messageID
	}
	for generation := uint64(2); generation < 500; generation++ {
		oldID := state.sourceCurrent["subscription-0"]
		state.targets[oldID].state = targetStale
		newID := fmt.Sprintf("churn-%d", generation)
		target := &deliveryTarget{kind: targetSource, state: targetCurrent, messageID: newID, subscriptionID: "subscription-0", generation: generation}
		state.remember(target, maximum)
		state.sourceCurrent["subscription-0"] = newID
	}
	if len(state.targets) > maximum*2 || len(state.targetOrder) > maximum*2 {
		t.Fatalf("targets=%d order=%d", len(state.targets), len(state.targetOrder))
	}
	for index := 1; index < maximum; index++ {
		if _, exists := state.targets[fmt.Sprintf("current-%d", index)]; !exists {
			t.Fatalf("long-lived current target %d was evicted", index)
		}
	}
	if current := state.sourceCurrent["subscription-0"]; state.targets[current] == nil || state.targets[current].state != targetCurrent {
		t.Fatal("latest current target was not retained")
	}
}

type slowSocket struct {
	delay time.Duration
}

func (s *slowSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (s *slowSocket) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*slowSocket) Close(websocket.StatusCode, string) error { return nil }
func (*slowSocket) CloseNow() error                          { return nil }
func (*slowSocket) SetReadLimit(int64)                       {}
func (*slowSocket) Subprotocol() string                      { return protocol.Subprotocol }

func TestPollDeliveriesUsesOneDeadlineForSlowConsumer(t *testing.T) {
	storeState := newActiveStore()
	for index := 0; index < 8; index++ {
		subscriptionID := activeUUID(700 + index)
		storeState.desired = append(storeState.desired, desiredState(subscriptionID, 1))
	}
	config := DefaultConfig()
	config.StoreTimeout = 40 * time.Millisecond
	config.WriteTimeout = 40 * time.Millisecond
	config.MaxOutstanding = 8
	config.OutboundQueue = 8
	handler, err := NewHandler(storeState, config, Options{Now: func() time.Time { return commandTime }})
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(handler, &slowSocket{delay: 30 * time.Millisecond})
	session.controllerID = activeUUID(1)
	for _, item := range storeState.desired {
		session.subscriptions[item.SubscriptionID] = struct{}{}
	}
	session.syncComplete = true
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(session.writerDone)
		session.writeLoop(ctx)
	}()
	started := time.Now()
	failure := session.pollDeliveries(context.Background(), newActiveState())
	elapsed := time.Since(started)
	cancel()
	<-session.writerDone
	if failure == nil || failure.code != "write_failed" {
		t.Fatalf("failure=%#v", failure)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("poll blocked for %s instead of one write window", elapsed)
	}
}

func TestTerminalRevocationAndRotationFinalizationSemantics(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 2)
	storeState.controllerResult = store.SessionCommandResult{Kind: store.ResultControllerRevoked, ControllerID: harness.session.controllerID}
	controller := &protocol.ControllerRevoke{Envelope: protocol.NewEnvelope(protocol.TypeControllerRevoke, activeUUID(800), commandTime), ControllerID: harness.session.controllerID}
	terminal, failure := harness.session.handleControllerRevocation(context.Background(), controller)
	if failure != nil || !terminal {
		t.Fatalf("controller terminal=%v failure=%#v", terminal, failure)
	}

	rotationID := activeUUID(801)
	storeState.rotationResult = store.SessionCommandResult{Kind: store.ResultRotationFinalized, RotationID: rotationID, RetiredKeyID: harness.session.keyID}
	finalize := &protocol.KeyRotationFinalize{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalize, activeUUID(802), commandTime), RotationID: rotationID, RetireOldKey: true}
	terminal, failure = harness.session.handleRotationFinalization(context.Background(), finalize)
	if failure != nil || !terminal {
		t.Fatalf("old-key terminal=%v failure=%#v", terminal, failure)
	}

	harness.session.keyID = activeUUID(803)
	terminal, failure = harness.session.handleRotationFinalization(context.Background(), finalize)
	if failure != nil || terminal {
		t.Fatalf("new-key replay terminal=%v failure=%#v", terminal, failure)
	}
}

func TestBindingKeyAndRotationCommandResponses(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 4)

	storeState.bindingResult = store.SessionCommandResult{Kind: store.ResultBindingRemoved, InstallationID: 10, RepositoryID: 20}
	binding := &protocol.BindingRemove{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemove, activeUUID(850), commandTime), InstallationID: 10, RepositoryID: 20}
	if failure := harness.session.handleBindingRemoval(context.Background(), binding); failure != nil {
		t.Fatal(failure)
	}

	pendingKeyID := activeUUID(851)
	storeState.keyResult = store.SessionCommandResult{Kind: store.ResultKeyRevoked, ControllerID: harness.session.controllerID, KeyID: pendingKeyID}
	key := &protocol.KeyRevoke{Envelope: protocol.NewEnvelope(protocol.TypeKeyRevoke, activeUUID(852), commandTime), ControllerID: harness.session.controllerID, KeyID: pendingKeyID}
	terminal, failure := harness.session.handleKeyRevocation(context.Background(), key)
	if failure != nil || terminal {
		t.Fatalf("pending key terminal=%v failure=%#v", terminal, failure)
	}
	key.MessageID = activeUUID(853)
	key.KeyID = harness.session.keyID
	storeState.keyResult.KeyID = harness.session.keyID
	terminal, failure = harness.session.handleKeyRevocation(context.Background(), key)
	if failure != nil || !terminal {
		t.Fatalf("session key terminal=%v failure=%#v", terminal, failure)
	}

	rotationID := activeUUID(854)
	storeState.proposalResult = store.SessionCommandResult{Kind: store.ResultRotationChallenge, RotationID: rotationID, Nonce: bytes.Repeat([]byte{7}, protocol.NonceBytes), ExpiresAt: commandTime.Add(time.Minute)}
	proposal := &protocol.KeyRotationPropose{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationPropose, activeUUID(855), commandTime), RotationID: rotationID, ControllerID: harness.session.controllerID, OldKeyID: harness.session.keyID, NewKeyID: activeUUID(856), NewPublicKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, protocol.PublicKeyBytes))}
	if failure = harness.session.handleRotationProposal(context.Background(), proposal); failure != nil {
		t.Fatal(failure)
	}
	storeState.confirmationResult = store.SessionCommandResult{Kind: store.ResultRotationConfirmed, RotationID: rotationID}
	confirmation := &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, activeUUID(857), commandTime), RotationID: rotationID, Signature: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, protocol.SignatureBytes))}
	if failure = harness.session.handleRotationConfirmation(context.Background(), confirmation); failure != nil {
		t.Fatal(failure)
	}

	frames := harness.snapshot()
	if len(frames) != 5 {
		t.Fatalf("response frames=%d", len(frames))
	}
	bindingResponse, ok := frames[0].(*protocol.BindingRemoved)
	if !ok || bindingResponse.TargetMessageID != binding.MessageID || bindingResponse.InstallationID != 10 || bindingResponse.RepositoryID != 20 {
		t.Fatalf("binding response=%#v", frames[0])
	}
	pendingResponse, ok := frames[1].(*protocol.KeyRevoked)
	if !ok || pendingResponse.TargetMessageID != activeUUID(852) || pendingResponse.ControllerID != harness.session.controllerID || pendingResponse.KeyID != pendingKeyID {
		t.Fatalf("pending key response=%#v", frames[1])
	}
	selfResponse, ok := frames[2].(*protocol.KeyRevoked)
	if !ok || selfResponse.TargetMessageID != key.MessageID || selfResponse.ControllerID != harness.session.controllerID || selfResponse.KeyID != harness.session.keyID {
		t.Fatalf("self key response=%#v", frames[2])
	}
	challenge, ok := frames[3].(*protocol.KeyRotationChallenge)
	if !ok || challenge.TargetMessageID != proposal.MessageID || challenge.RotationID != rotationID || challenge.ServerNonce != base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, protocol.NonceBytes)) || !challenge.ExpiresAt.Equal(commandTime.Add(time.Minute)) {
		t.Fatalf("rotation challenge=%#v", frames[3])
	}
	confirmed, ok := frames[4].(*protocol.KeyRotationConfirmed)
	if !ok || confirmed.TargetMessageID != confirmation.MessageID || confirmed.RotationID != rotationID {
		t.Fatalf("rotation confirmation=%#v", frames[4])
	}

	storeState.mu.Lock()
	defer storeState.mu.Unlock()
	if len(storeState.bindingCalls) != 1 || storeState.bindingCalls[0].installationID != 10 || storeState.bindingCalls[0].repositoryID != 20 || storeState.bindingCalls[0].command.Type != store.CommandBindingRemove {
		t.Fatalf("binding calls=%+v", storeState.bindingCalls)
	}
	if len(storeState.keyCalls) != 2 || storeState.keyCalls[0].keyID != pendingKeyID || storeState.keyCalls[1].keyID != harness.session.keyID || storeState.keyCalls[0].controllerID != harness.session.controllerID {
		t.Fatalf("key calls=%+v", storeState.keyCalls)
	}
	if len(storeState.proposalCalls) != 1 || storeState.proposalCalls[0].command.Type != store.CommandRotationPropose || storeState.proposalCalls[0].input.RotationID != rotationID || storeState.proposalCalls[0].input.OldKeyID != harness.session.keyID || storeState.proposalCalls[0].input.NewKeyID != proposal.NewKeyID || !bytes.Equal(storeState.proposalCalls[0].input.NewPublicKey, bytes.Repeat([]byte{8}, protocol.PublicKeyBytes)) {
		t.Fatalf("proposal calls=%+v", storeState.proposalCalls)
	}
	if len(storeState.confirmationCalls) != 1 || storeState.confirmationCalls[0].command.Type != store.CommandRotationConfirm || storeState.confirmationCalls[0].rotationID != rotationID || storeState.confirmationCalls[0].signature != confirmation.Signature {
		t.Fatalf("confirmation calls=%+v", storeState.confirmationCalls)
	}
	for _, value := range storeState.proposalResult.Nonce {
		if value != 0 {
			t.Fatal("rotation challenge nonce was not destroyed after response encoding")
		}
	}
}

func TestRotationAndIdentityFailuresDoNotEmitSuccessResponses(t *testing.T) {
	rotationID := activeUUID(870)
	newPublicKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, protocol.PublicKeyBytes))
	signature := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, protocol.SignatureBytes))
	for _, test := range []struct {
		name      string
		configure func(*activeStore)
		invoke    func(*session) *sessionFailure
		wantCode  string
		wantCalls int
	}{
		{
			name: "proposal conflict", configure: func(state *activeStore) { state.proposalErr = store.ErrConflict },
			invoke: func(session *session) *sessionFailure {
				return session.handleRotationProposal(context.Background(), &protocol.KeyRotationPropose{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationPropose, activeUUID(871), commandTime), RotationID: rotationID, ControllerID: session.controllerID, OldKeyID: session.keyID, NewKeyID: activeUUID(872), NewPublicKey: newPublicKey})
			}, wantCode: "rotation_proposal_failed", wantCalls: 1,
		},
		{
			name: "confirmation expired", configure: func(state *activeStore) { state.confirmationErr = store.ErrExpired },
			invoke: func(session *session) *sessionFailure {
				return session.handleRotationConfirmation(context.Background(), &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, activeUUID(873), commandTime), RotationID: rotationID, Signature: signature})
			}, wantCode: "rotation_confirmation_failed", wantCalls: 1,
		},
		{
			name: "proposal identity mismatch",
			invoke: func(session *session) *sessionFailure {
				return session.handleRotationProposal(context.Background(), &protocol.KeyRotationPropose{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationPropose, activeUUID(874), commandTime), RotationID: rotationID, ControllerID: activeUUID(999), OldKeyID: session.keyID, NewKeyID: activeUUID(875), NewPublicKey: newPublicKey})
			}, wantCode: "identity_mismatch", wantCalls: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeState := newActiveStore()
			if test.configure != nil {
				test.configure(storeState)
			}
			harness := newActiveHarness(t, storeState, 2)
			failure := test.invoke(harness.session)
			if failure == nil || failure.code != test.wantCode {
				t.Fatalf("failure=%#v", failure)
			}
			storeState.mu.Lock()
			calls := len(storeState.proposalCalls) + len(storeState.confirmationCalls)
			storeState.mu.Unlock()
			if calls != test.wantCalls || len(harness.snapshot()) != 0 {
				t.Fatalf("store calls=%d response frames=%d", calls, len(harness.snapshot()))
			}
		})
	}
}

func TestRejectBindsCodeAndTargetBeforeDurableDecision(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 1)
	subscriptionID := activeUUID(880)
	targetMessageID := activeUUID(881)
	target := &deliveryTarget{kind: targetSource, state: targetCurrent, messageID: targetMessageID, subscriptionID: subscriptionID, generation: 7}
	harness.state.remember(target, 1)
	harness.state.sourceCurrent[subscriptionID] = targetMessageID
	reject := &protocol.Reject{Envelope: protocol.NewEnvelope(protocol.TypeReject, activeUUID(882), commandTime), TargetMessageID: targetMessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 7}, Code: "deployment.failed"}
	if failure := harness.session.handleDecision(context.Background(), harness.state, reject, false, reject.Code); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	defer storeState.mu.Unlock()
	if len(storeState.sourceCalls) != 1 {
		t.Fatalf("source calls=%d", len(storeState.sourceCalls))
	}
	call := storeState.sourceCalls[0]
	if call.command.Type != store.CommandRejectSource || call.command.MessageID != reject.MessageID || call.targetMessage != targetMessageID || call.subscriptionID != subscriptionID || call.generation != 7 || call.accepted || call.code != reject.Code {
		t.Fatalf("reject call=%+v", call)
	}
}

func TestPeerProtocolErrorFatalityIsExplicit(t *testing.T) {
	harness := newActiveHarness(t, newActiveStore(), 1)
	for _, test := range []struct {
		fatal        bool
		wantTerminal bool
	}{
		{fatal: false, wantTerminal: false},
		{fatal: true, wantTerminal: true},
	} {
		frame := &protocol.ProtocolError{Envelope: protocol.NewEnvelope(protocol.TypeProtocolError, activeUUID(890+int(boolInt(test.fatal))), commandTime), Code: "frame.invalid", Fatal: test.fatal}
		terminal, failure := harness.session.handleActiveFrame(context.Background(), harness.state, frame)
		if failure != nil || terminal != test.wantTerminal {
			t.Fatalf("fatal=%v terminal=%v failure=%#v", test.fatal, terminal, failure)
		}
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestTerminalResponseWriteFailureDoesNotRetryMutation(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*activeStore, *session)
		frame     func(*session, int) protocol.Frame
		calls     func(*activeStore) int
	}{
		{
			name: "controller revoke",
			configure: func(state *activeStore, session *session) {
				state.controllerResult = store.SessionCommandResult{Kind: store.ResultControllerRevoked, ControllerID: session.controllerID}
			},
			frame: func(session *session, offset int) protocol.Frame {
				return &protocol.ControllerRevoke{Envelope: protocol.NewEnvelope(protocol.TypeControllerRevoke, activeUUID(895+offset), commandTime), ControllerID: session.controllerID}
			},
			calls: func(state *activeStore) int { return state.controllerCalls },
		},
		{
			name: "self key revoke",
			configure: func(state *activeStore, session *session) {
				state.keyResult = store.SessionCommandResult{Kind: store.ResultKeyRevoked, ControllerID: session.controllerID, KeyID: session.keyID}
			},
			frame: func(session *session, offset int) protocol.Frame {
				return &protocol.KeyRevoke{Envelope: protocol.NewEnvelope(protocol.TypeKeyRevoke, activeUUID(905+offset), commandTime), ControllerID: session.controllerID, KeyID: session.keyID}
			},
			calls: func(state *activeStore) int { return len(state.keyCalls) },
		},
		{
			name: "rotation finalize",
			configure: func(state *activeStore, session *session) {
				state.rotationResult = store.SessionCommandResult{Kind: store.ResultRotationFinalized, RotationID: activeUUID(915), RetiredKeyID: session.keyID}
			},
			frame: func(_ *session, offset int) protocol.Frame {
				return &protocol.KeyRotationFinalize{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalize, activeUUID(916+offset), commandTime), RotationID: activeUUID(915), RetireOldKey: true}
			},
			calls: func(state *activeStore) int { return state.rotationCalls },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeState := newActiveStore()
			config := DefaultConfig()
			config.WriteTimeout = 50 * time.Millisecond
			handler, err := NewHandler(storeState, config, Options{Now: func() time.Time { return commandTime }})
			if err != nil {
				t.Fatal(err)
			}
			conn := newBlockingSocket()
			conn.writeErr = errors.New("peer stopped reading")
			session := newSession(handler, conn)
			session.controllerID = activeUUID(1)
			session.keyID = activeUUID(2)
			session.sessionID = activeUUID(3)
			session.lease = store.Lease{ControllerID: activeUUID(1), SessionID: activeUUID(3), LeaseID: activeUUID(4), Fence: 1, ExpiresAt: commandTime.Add(time.Minute)}
			session.sessionUntil = commandTime.Add(config.SessionLifetime)
			session.syncComplete = true
			test.configure(storeState, session)
			first, err := protocol.Encode(test.frame(session, 0), config.MaxEnvelopeBytes)
			if err != nil {
				t.Fatal(err)
			}
			session.reads <- readEvent{messageType: websocket.MessageText, data: first}
			ctx, cancel := context.WithCancel(context.Background())
			writerStopped := false
			go func() {
				defer close(session.writerDone)
				session.writeLoop(ctx)
			}()
			t.Cleanup(func() {
				cancel()
				_ = conn.CloseNow()
				if writerStopped {
					return
				}
				select {
				case <-session.writerDone:
				case <-time.After(time.Second):
					t.Error("writer teardown did not complete during cleanup")
				}
			})
			failure := session.active(ctx)
			if failure == nil || failure.code != "write_failed" {
				t.Fatalf("failure=%#v", failure)
			}
			second, err := protocol.Encode(test.frame(session, 1), config.MaxEnvelopeBytes)
			if err != nil {
				t.Fatal(err)
			}
			session.reads <- readEvent{messageType: websocket.MessageText, data: second}
			select {
			case <-session.writerDone:
				writerStopped = true
			case <-time.After(time.Second):
				t.Fatal("writer teardown did not complete")
			}
			storeState.mu.Lock()
			defer storeState.mu.Unlock()
			if calls := test.calls(storeState); calls != 1 {
				t.Fatalf("terminal mutation attempts=%d", calls)
			}
			if len(session.reads) != 1 {
				t.Fatal("a later inbound frame was consumed after terminal write failure")
			}
		})
	}
}

func TestUnknownDecisionOnlyUsesExactReplay(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 1)
	ack := &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(900), commandTime), TargetMessageID: activeUUID(901), Access: &protocol.AccessTarget{EventID: activeUUID(902)}}
	if failure := harness.session.handleDecision(context.Background(), harness.state, ack, true, ""); failure != nil {
		t.Fatal(failure)
	}
	storeState.mu.Lock()
	defer storeState.mu.Unlock()
	if len(storeState.protocolCalls) != 1 || storeState.protocolCalls[0].code != "unknown_target" || len(storeState.accessCalls) != 0 || len(storeState.sourceCalls) != 0 {
		t.Fatalf("protocol=%d source=%d access=%d", len(storeState.protocolCalls), len(storeState.sourceCalls), len(storeState.accessCalls))
	}
}

func TestLocalDecisionProtocolErrorsSurviveTombstoneEvictionAndRestart(t *testing.T) {
	for index, test := range []struct {
		name     string
		state    deliveryTargetState
		absent   bool
		mismatch bool
		wantCode string
	}{
		{name: "absent target", absent: true, wantCode: "unknown_target"},
		{name: "structural mismatch", state: targetCurrent, mismatch: true, wantCode: "target_mismatch"},
		{name: "stale after unsubscribe", state: targetStale, wantCode: "stale_target"},
		{name: "distinct command against terminal", state: targetTerminal, wantCode: "unknown_target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeState := newActiveStore()
			harness := newActiveHarness(t, storeState, 2)
			targetMessageID := activeUUID(2000 + index*10)
			subscriptionID := activeUUID(2001 + index*10)
			state := newActiveState()
			if !test.absent {
				state.targets[targetMessageID] = &deliveryTarget{kind: targetSource, state: test.state, messageID: targetMessageID, subscriptionID: subscriptionID, generation: 7}
			}
			generation := uint64(7)
			if test.mismatch {
				generation = 6
			}
			ack := &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(2002+index*10), commandTime), TargetMessageID: targetMessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: generation}}
			if failure := harness.session.handleDecision(context.Background(), state, ack, true, ""); failure != nil {
				t.Fatal(failure)
			}
			// A fresh activeState models both tombstone eviction and process-local
			// restart. The store sees unknown_target locally on this retry but must
			// replay the exact first durable result.
			if failure := harness.session.handleDecision(context.Background(), newActiveState(), ack, true, ""); failure != nil {
				t.Fatal(failure)
			}
			frames := harness.snapshot()
			if len(frames) != 2 {
				t.Fatalf("frames=%d", len(frames))
			}
			for frameIndex, frame := range frames {
				protocolError, ok := frame.(*protocol.ProtocolError)
				if !ok || protocolError.Code != test.wantCode || protocolError.Fatal {
					t.Fatalf("frame[%d]=%#v want nonfatal %s", frameIndex, frame, test.wantCode)
				}
			}
			storeState.mu.Lock()
			defer storeState.mu.Unlock()
			if len(storeState.sourceCalls) != 0 || len(storeState.accessCalls) != 0 || len(storeState.protocolCalls) != 2 || storeState.protocolCalls[1].code != "unknown_target" {
				t.Fatalf("source=%d access=%d protocol=%+v", len(storeState.sourceCalls), len(storeState.accessCalls), storeState.protocolCalls)
			}
		})
	}
}

func TestDurableDecisionReplayKeepsAckOrRejectMetricAfterTombstoneAndRestart(t *testing.T) {
	tests := []struct {
		name       string
		access     bool
		accepted   bool
		wantMetric string
	}{
		{name: "source ack", accepted: true, wantMetric: "ack"},
		{name: "source reject", wantMetric: "reject"},
		{name: "access ack", access: true, accepted: true, wantMetric: "ack"},
		{name: "access reject", access: true, wantMetric: "reject"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storeState := newActiveStore()
			harness := newActiveHarness(t, storeState, 2)
			state := newActiveState()
			targetMessageID := activeUUID(2300 + index*10)
			messageID := activeUUID(2301 + index*10)
			var frame protocol.Frame
			decisionCode := ""
			if test.access {
				eventID := activeUUID(2302 + index*10)
				state.targets[targetMessageID] = &deliveryTarget{kind: targetAccess, state: targetCurrent, messageID: targetMessageID, eventID: eventID}
				state.accessCurrent[eventID] = targetMessageID
				if test.accepted {
					frame = &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, messageID, commandTime), TargetMessageID: targetMessageID, Access: &protocol.AccessTarget{EventID: eventID}}
				} else {
					decisionCode = "access.denied"
					frame = &protocol.Reject{Envelope: protocol.NewEnvelope(protocol.TypeReject, messageID, commandTime), TargetMessageID: targetMessageID, Access: &protocol.AccessTarget{EventID: eventID}, Code: decisionCode}
				}
			} else {
				subscriptionID := activeUUID(2302 + index*10)
				state.targets[targetMessageID] = &deliveryTarget{kind: targetSource, state: targetCurrent, messageID: targetMessageID, subscriptionID: subscriptionID, generation: 3}
				state.sourceCurrent[subscriptionID] = targetMessageID
				if test.accepted {
					frame = &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, messageID, commandTime), TargetMessageID: targetMessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 3}}
				} else {
					decisionCode = "source.rejected"
					frame = &protocol.Reject{Envelope: protocol.NewEnvelope(protocol.TypeReject, messageID, commandTime), TargetMessageID: targetMessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 3}, Code: decisionCode}
				}
			}

			if failure := harness.session.handleDecision(context.Background(), state, frame, test.accepted, decisionCode); failure != nil {
				t.Fatal(failure)
			}
			// A fresh process-local state has neither the target nor its tombstone.
			// The durable command ledger still replays the original applied result.
			if failure := harness.session.handleDecision(context.Background(), newActiveState(), frame, test.accepted, decisionCode); failure != nil {
				t.Fatal(failure)
			}
			stats := harness.session.handler.Stats()
			for metricIndex, name := range DecisionNames() {
				want := uint64(0)
				if name == test.wantMetric {
					want = 2
				}
				if stats.Decisions[metricIndex] != want {
					t.Fatalf("decision %s=%d want=%d", name, stats.Decisions[metricIndex], want)
				}
			}
		})
	}
}

func TestDecisionProtocolMetricIsReservedForDurableProtocolError(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 2)
	ack := &protocol.Ack{
		Envelope:        protocol.NewEnvelope(protocol.TypeAck, activeUUID(2390), commandTime),
		TargetMessageID: activeUUID(2391),
		Source:          &protocol.SourceTarget{SubscriptionID: activeUUID(2392), Generation: 1},
	}
	if failure := harness.session.handleDecision(context.Background(), newActiveState(), ack, true, ""); failure != nil {
		t.Fatal(failure)
	}
	stats := harness.session.handler.Stats()
	for index, name := range DecisionNames() {
		want := uint64(0)
		if name == "protocol" {
			want = 1
		}
		if stats.Decisions[index] != want {
			t.Fatalf("decision %s=%d want=%d", name, stats.Decisions[index], want)
		}
	}
}

func TestAuthoritativeUnsubscribeMakesLateACKAndRejectDurablyStale(t *testing.T) {
	for index, accepted := range []bool{true, false} {
		name := "reject"
		if accepted {
			name = "ack"
		}
		t.Run(name, func(t *testing.T) {
			storeState := newActiveStore()
			harness := newActiveHarness(t, storeState, 2)
			subscriptionID := activeUUID(2100 + index*10)
			targetMessageID := activeUUID(2101 + index*10)
			target := &deliveryTarget{kind: targetSource, state: targetCurrent, messageID: targetMessageID, subscriptionID: subscriptionID, generation: 9}
			harness.state.targets[targetMessageID] = target
			harness.state.sourceCurrent[subscriptionID] = targetMessageID
			harness.session.subscriptions[subscriptionID] = struct{}{}
			syncFrame := &protocol.SubscriptionsSync{Envelope: protocol.NewEnvelope(protocol.TypeSubscriptionsSync, activeUUID(2102+index*10), commandTime), Generation: 1, Subscriptions: []protocol.Subscription{}}
			if failure := harness.session.handleSubscriptionSync(context.Background(), harness.state, syncFrame); failure != nil {
				t.Fatal(failure)
			}
			if target.state != targetStale {
				t.Fatal("authoritative unsubscribe did not stale outstanding target")
			}
			var frame protocol.Frame
			decisionCode := "deployment.stopped"
			if accepted {
				frame = &protocol.Ack{Envelope: protocol.NewEnvelope(protocol.TypeAck, activeUUID(2103+index*10), commandTime), TargetMessageID: targetMessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 9}}
				decisionCode = ""
			} else {
				frame = &protocol.Reject{Envelope: protocol.NewEnvelope(protocol.TypeReject, activeUUID(2103+index*10), commandTime), TargetMessageID: targetMessageID, Source: &protocol.SourceTarget{SubscriptionID: subscriptionID, Generation: 9}, Code: "deployment.stopped"}
			}
			if failure := harness.session.handleDecision(context.Background(), harness.state, frame, accepted, decisionCode); failure != nil {
				t.Fatal(failure)
			}
			storeState.mu.Lock()
			defer storeState.mu.Unlock()
			if len(storeState.sourceCalls) != 0 || len(storeState.protocolCalls) != 1 || storeState.protocolCalls[0].code != "stale_target" {
				t.Fatalf("source=%d protocol=%+v", len(storeState.sourceCalls), storeState.protocolCalls)
			}
		})
	}
}

type manualTimer struct {
	ch       chan time.Time
	duration time.Duration
}

func (t *manualTimer) Chan() <-chan time.Time            { return t.ch }
func (t *manualTimer) Stop() bool                        { return true }
func (t *manualTimer) Reset(duration time.Duration) bool { t.duration = duration; return true }
func (t *manualTimer) fire(now time.Time)                { t.ch <- now }

type manualTicker struct {
	ch chan time.Time
}

func (t *manualTicker) Chan() <-chan time.Time { return t.ch }
func (t *manualTicker) Stop()                  {}
func (t *manualTicker) fire(now time.Time)     { t.ch <- now }

type manualTimerSource struct {
	mu      sync.Mutex
	timers  []*manualTimer
	tickers []*manualTicker
	created chan struct{}
}

func newManualTimerSource() *manualTimerSource {
	return &manualTimerSource{created: make(chan struct{}, 8)}
}

func (s *manualTimerSource) NewTimer(duration time.Duration) Timer {
	s.mu.Lock()
	timer := &manualTimer{ch: make(chan time.Time, 1), duration: duration}
	s.timers = append(s.timers, timer)
	s.mu.Unlock()
	s.created <- struct{}{}
	return timer
}

func (s *manualTimerSource) NewTicker(time.Duration) Ticker {
	s.mu.Lock()
	ticker := &manualTicker{ch: make(chan time.Time, 1)}
	s.tickers = append(s.tickers, ticker)
	s.mu.Unlock()
	s.created <- struct{}{}
	return ticker
}

func (s *manualTimerSource) wait(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		ready := len(s.tickers) == 3 && len(s.timers) == 2
		s.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-s.created:
		case <-deadline.C:
			t.Fatal("active timers were not created")
		}
	}
}

func waitForFrames(t *testing.T, harness *activeHarness, count int) []protocol.Frame {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		frames := harness.snapshot()
		if len(frames) >= count {
			return frames
		}
		select {
		case <-deadline.C:
			t.Fatalf("received %d frames, want %d", len(frames), count)
		case <-time.After(time.Millisecond):
		}
	}
}

func waitForReadPermit(t *testing.T, session *session) {
	t.Helper()
	select {
	case <-session.readRequests:
	case <-time.After(time.Second):
		t.Fatal("active session did not permit the next read")
	}
}

func TestActiveHeartbeatSequenceAndReplayDetection(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 2)
	harness.session.syncComplete = true
	timers := newManualTimerSource()
	harness.session.handler.timers = timers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan *sessionFailure, 1)
	go func() { result <- harness.session.active(ctx) }()
	timers.wait(t)

	timers.tickers[0].fire(commandTime)
	waitForFrames(t, harness, 1)
	timers.tickers[0].fire(commandTime.Add(time.Second))
	frames := waitForFrames(t, harness, 2)
	for index, sequence := range []uint64{1, 2} {
		heartbeat, ok := frames[index].(*protocol.Heartbeat)
		if !ok || heartbeat.Sequence != sequence {
			t.Fatalf("heartbeat %d=%#v", index, frames[index])
		}
	}

	incoming := &protocol.Heartbeat{Envelope: protocol.NewEnvelope(protocol.TypeHeartbeat, activeUUID(950), commandTime), Sequence: 1}
	encoded, err := protocol.Encode(incoming, harness.session.handler.config.MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	harness.session.reads <- readEvent{messageType: websocket.MessageText, data: encoded}
	incoming.MessageID = activeUUID(951)
	encoded, err = protocol.Encode(incoming, harness.session.handler.config.MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	harness.session.reads <- readEvent{messageType: websocket.MessageText, data: encoded}
	if failure := <-result; failure == nil || failure.code != "heartbeat_replay" {
		t.Fatalf("failure=%#v", failure)
	}
}

func TestActiveIdleAndSessionExpiryUseInjectedTimers(t *testing.T) {
	for _, test := range []struct {
		name       string
		timerIndex int
		wantCode   string
	}{
		{name: "idle", timerIndex: 0, wantCode: "idle_timeout"},
		{name: "session", timerIndex: 1, wantCode: "session_expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newActiveHarness(t, newActiveStore(), 1)
			harness.session.syncComplete = true
			timers := newManualTimerSource()
			harness.session.handler.timers = timers
			result := make(chan *sessionFailure, 1)
			go func() { result <- harness.session.active(context.Background()) }()
			timers.wait(t)
			if got := timers.timers[1].duration; got != harness.session.handler.config.SessionLifetime {
				t.Fatalf("session duration=%s", got)
			}
			timers.timers[test.timerIndex].fire(commandTime)
			if failure := <-result; failure == nil || failure.code != test.wantCode {
				t.Fatalf("failure=%#v", failure)
			}
		})
	}
}

func TestActiveLeaseRenewalUpdatesFenceAndFailureTerminates(t *testing.T) {
	for _, test := range []struct {
		name     string
		renewErr error
		wantCode string
	}{
		{name: "success", wantCode: "server_shutdown"},
		{name: "lost fence", renewErr: store.ErrConflict, wantCode: "lease_lost"},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeState := newActiveStore()
			harness := newActiveHarness(t, storeState, 1)
			harness.session.syncComplete = true
			original := harness.session.lease
			storeState.renewResult = original
			storeState.renewResult.ExpiresAt = original.ExpiresAt.Add(time.Minute)
			storeState.renewErr = test.renewErr
			timers := newManualTimerSource()
			harness.session.handler.timers = timers
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan *sessionFailure, 1)
			go func() { result <- harness.session.active(ctx) }()
			timers.wait(t)
			timers.tickers[2].fire(commandTime)
			deadline := time.NewTimer(time.Second)
			for {
				storeState.mu.Lock()
				calls := storeState.renewCalls
				storeState.mu.Unlock()
				if calls == 1 {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("lease renewal was not attempted")
				case <-time.After(time.Millisecond):
				}
			}
			deadline.Stop()
			if test.renewErr == nil {
				cancel()
			}
			if failure := <-result; failure == nil || failure.code != test.wantCode {
				t.Fatalf("failure=%#v", failure)
			}
			if test.renewErr == nil {
				if harness.session.lease.ExpiresAt != storeState.renewResult.ExpiresAt || harness.session.lease.Fence != original.Fence {
					t.Fatalf("renewed lease=%+v", harness.session.lease)
				}
			}
			cancel()
		})
	}
}

func TestActivePrioritizesDueRenewalBeforeQueuedPoll(t *testing.T) {
	storeState := newActiveStore()
	harness := newActiveHarness(t, storeState, 1)
	harness.session.syncComplete = true
	timers := newManualTimerSource()
	harness.session.handler.timers = timers
	var nowMu sync.Mutex
	current := commandTime
	harness.session.handler.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return current
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var initial sync.Once
	storeState.pendingAccessHook = func(context.Context) error {
		initial.Do(func() {
			close(entered)
			<-release
		})
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan *sessionFailure, 1)
	go func() { result <- harness.session.active(ctx) }()
	timers.wait(t)
	<-entered
	nowMu.Lock()
	current = commandTime.Add(harness.session.handler.config.LeaseRenewInterval)
	nowMu.Unlock()
	timers.tickers[1].fire(current)
	timers.tickers[2].fire(current)
	close(release)

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		storeState.mu.Lock()
		order := append([]string(nil), storeState.callOrder...)
		storeState.mu.Unlock()
		if len(order) >= 5 {
			want := []string{"access", "desired", "renew", "access", "desired"}
			if !slices.Equal(order[:5], want) {
				t.Fatalf("store order=%v want prefix=%v", order, want)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("store order=%v", order)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if failure := <-result; failure == nil || failure.code != "server_shutdown" {
		t.Fatalf("failure=%#v", failure)
	}
}

func TestActiveTerminatesWhenCommandWorkCrossesLeaseExpiry(t *testing.T) {
	storeState := newActiveStore()
	storeState.bindingResult = store.SessionCommandResult{Kind: store.ResultBindingRemoved, InstallationID: 10, RepositoryID: 20}
	harness := newActiveHarness(t, storeState, 1)
	harness.session.syncComplete = true
	var nowMu sync.Mutex
	current := commandTime
	harness.session.handler.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return current
	}
	harness.session.lease.ExpiresAt = commandTime.Add(20 * time.Millisecond)
	storeState.bindingHook = func(context.Context) error {
		nowMu.Lock()
		current = harness.session.lease.ExpiresAt
		nowMu.Unlock()
		return nil
	}
	frame := &protocol.BindingRemove{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemove, activeUUID(980), commandTime), InstallationID: 10, RepositoryID: 20}
	encoded, err := protocol.Encode(frame, harness.session.handler.config.MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *sessionFailure, 1)
	go func() { result <- harness.session.active(context.Background()) }()
	harness.session.reads <- readEvent{messageType: websocket.MessageText, data: encoded}
	if failure := <-result; failure == nil || failure.code != "lease_lost" {
		t.Fatalf("failure=%#v", failure)
	}
	storeState.mu.Lock()
	defer storeState.mu.Unlock()
	if len(storeState.bindingCalls) != 1 || storeState.renewCalls != 0 {
		t.Fatalf("binding calls=%d renew calls=%d", len(storeState.bindingCalls), storeState.renewCalls)
	}
}

func TestActiveInboundRateLimitRejectsBurstAndResetsAfterWindow(t *testing.T) {
	for _, resetWindow := range []bool{false, true} {
		name := "rejects limit plus one"
		if resetWindow {
			name = "resets after window"
		}
		t.Run(name, func(t *testing.T) {
			storeState := newActiveStore()
			storeState.bindingResult = store.SessionCommandResult{Kind: store.ResultBindingRemoved, InstallationID: 10, RepositoryID: 20}
			harness := newActiveHarness(t, storeState, 1)
			harness.session.syncComplete = true
			harness.session.handler.config.MaxInboundPerWindow = 2
			harness.session.handler.config.InboundWindow = time.Second
			timers := newManualTimerSource()
			harness.session.handler.timers = timers
			var nowMu sync.Mutex
			current := commandTime
			harness.session.handler.now = func() time.Time {
				nowMu.Lock()
				defer nowMu.Unlock()
				return current
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan *sessionFailure, 1)
			go func() { result <- harness.session.active(ctx) }()
			timers.wait(t)
			for index := 0; index < 3; index++ {
				waitForReadPermit(t, harness.session)
				if resetWindow && index == 2 {
					nowMu.Lock()
					current = current.Add(time.Second)
					nowMu.Unlock()
				}
				frame := &protocol.BindingRemove{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemove, activeUUID(990+index), commandTime), InstallationID: 10, RepositoryID: 20}
				encoded, err := protocol.Encode(frame, harness.session.handler.config.MaxEnvelopeBytes)
				if err != nil {
					t.Fatal(err)
				}
				harness.session.reads <- readEvent{messageType: websocket.MessageText, data: encoded}
				if index < 2 || resetWindow {
					waitForFrames(t, harness, index+1)
				}
			}
			if resetWindow {
				waitForReadPermit(t, harness.session)
				cancel()
				if failure := <-result; failure == nil || failure.code != "server_shutdown" {
					t.Fatalf("failure=%#v", failure)
				}
			} else {
				if failure := <-result; failure == nil || failure.code != "inbound_rate_limited" {
					t.Fatalf("failure=%#v", failure)
				}
				select {
				case <-harness.session.readRequests:
					t.Fatal("rate-limited session permitted another read")
				default:
				}
			}
			storeState.mu.Lock()
			calls := len(storeState.bindingCalls)
			storeState.mu.Unlock()
			wantCalls := 2
			if resetWindow {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("store mutations=%d want=%d", calls, wantCalls)
			}
		})
	}
}
