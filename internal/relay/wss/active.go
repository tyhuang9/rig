package wss

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

type deliveryTargetKind uint8

const (
	targetSource deliveryTargetKind = iota + 1
	targetAccess
)

const maxDeliveryFramesPerPoll = 8

type deliveryTargetState uint8

const (
	targetCurrent deliveryTargetState = iota + 1
	targetStale
	targetTerminal
)

type deliveryTarget struct {
	kind           deliveryTargetKind
	state          deliveryTargetState
	messageID      string
	subscriptionID string
	generation     uint64
	eventID        string
}

type activeState struct {
	targets       map[string]*deliveryTarget
	targetOrder   []string
	sourceCurrent map[string]string
	accessCurrent map[string]string
	preferSource  bool
	heartbeatOut  uint64
	heartbeatIn   uint64
}

func newActiveState() *activeState {
	return &activeState{targets: make(map[string]*deliveryTarget), sourceCurrent: make(map[string]string), accessCurrent: make(map[string]string)}
}

func (s *session) active(ctx context.Context) *sessionFailure {
	state := newActiveState()
	now := s.handler.now().UTC()
	heartbeatDue := now.Add(s.handler.config.HeartbeatInterval)
	renewalDue := now.Add(s.handler.config.LeaseRenewInterval)
	inboundWindowStart := now
	inboundCount := 0
	if readFailure := s.requestRead(ctx); readFailure != nil {
		return readFailure
	}
	heartbeat := s.handler.timers.NewTicker(s.handler.config.HeartbeatInterval)
	poll := s.handler.timers.NewTicker(s.handler.config.PollInterval)
	renew := s.handler.timers.NewTicker(s.handler.config.LeaseRenewInterval)
	idle := s.handler.timers.NewTimer(s.handler.config.IdleTimeout)
	sessionTimer := s.handler.timers.NewTimer(s.sessionUntil.Sub(s.handler.now().UTC()))
	defer heartbeat.Stop()
	defer poll.Stop()
	defer renew.Stop()
	defer idle.Stop()
	defer sessionTimer.Stop()
	sendHeartbeat := func() *sessionFailure {
		state.heartbeatOut++
		envelope, err := s.envelope(protocol.TypeHeartbeat)
		if err != nil || s.sendFrame(ctx, &protocol.Heartbeat{Envelope: envelope, Sequence: state.heartbeatOut}) != nil {
			return failure("write_failed", websocket.StatusInternalError, false)
		}
		heartbeatDue = s.handler.now().UTC().Add(s.handler.config.HeartbeatInterval)
		return nil
	}
	ensureDeadlines := func() *sessionFailure {
		current := s.handler.now().UTC()
		if !current.Before(renewalDue) {
			if renewFailure := s.renewActiveLease(ctx); renewFailure != nil {
				return renewFailure
			}
			renewalDue = s.handler.now().UTC().Add(s.handler.config.LeaseRenewInterval)
			select {
			case <-renew.Chan():
			default:
			}
		}
		current = s.handler.now().UTC()
		if !current.Before(heartbeatDue) {
			if heartbeatFailure := sendHeartbeat(); heartbeatFailure != nil {
				return heartbeatFailure
			}
			select {
			case <-heartbeat.Chan():
			default:
			}
		}
		return nil
	}

	if deadlineFailure := ensureDeadlines(); deadlineFailure != nil {
		return deadlineFailure
	}
	if failure := s.pollDeliveries(ctx, state); failure != nil {
		return failure
	}
	for {
		if deadlineFailure := ensureDeadlines(); deadlineFailure != nil {
			return deadlineFailure
		}
		// A queued renewal is consumed before the general select so a ready
		// delivery poll or inbound frame cannot win a random select and spend
		// the remaining renewal budget in store or socket work.
		select {
		case <-renew.Chan():
			if renewFailure := s.renewActiveLease(ctx); renewFailure != nil {
				return renewFailure
			}
			renewalDue = s.handler.now().UTC().Add(s.handler.config.LeaseRenewInterval)
			continue
		default:
		}
		select {
		case event := <-s.reads:
			if event.err != nil {
				return failure("connection_closed", websocket.StatusNormalClosure, false)
			}
			if deadlineFailure := ensureDeadlines(); deadlineFailure != nil {
				clear(event.data)
				return deadlineFailure
			}
			current := s.handler.now().UTC()
			if current.Sub(inboundWindowStart) >= s.handler.config.InboundWindow {
				inboundWindowStart, inboundCount = current, 0
			}
			if inboundCount >= s.handler.config.MaxInboundPerWindow {
				clear(event.data)
				return failure("inbound_rate_limited", websocket.StatusPolicyViolation, true)
			}
			inboundCount++
			resetTimer(idle, s.handler.config.IdleTimeout)
			frame, decodeFailure := s.decodeActiveFrame(event)
			if decodeFailure != nil {
				return decodeFailure
			}
			terminal, handleFailure := s.handleActiveFrame(ctx, state, frame)
			if handleFailure != nil {
				return handleFailure
			}
			if terminal {
				return failure("terminal_command", websocket.StatusNormalClosure, false)
			}
			if deadlineFailure := ensureDeadlines(); deadlineFailure != nil {
				return deadlineFailure
			}
			if readFailure := s.requestRead(ctx); readFailure != nil {
				return readFailure
			}
		case <-heartbeat.Chan():
			if heartbeatFailure := sendHeartbeat(); heartbeatFailure != nil {
				return heartbeatFailure
			}
		case <-poll.Chan():
			if pollFailure := s.pollDeliveries(ctx, state); pollFailure != nil {
				return pollFailure
			}
		case <-renew.Chan():
			if renewFailure := s.renewActiveLease(ctx); renewFailure != nil {
				return renewFailure
			}
			renewalDue = s.handler.now().UTC().Add(s.handler.config.LeaseRenewInterval)
		case <-idle.Chan():
			return failure("idle_timeout", websocket.StatusPolicyViolation, true)
		case <-sessionTimer.Chan():
			return failure("session_expired", websocket.StatusNormalClosure, true)
		case <-s.writerFailed:
			return failure("write_failed", websocket.StatusInternalError, false)
		case <-s.writerDone:
			return failure("write_failed", websocket.StatusInternalError, false)
		case <-ctx.Done():
			return failure("server_shutdown", websocket.StatusGoingAway, false)
		}
	}
}

func (s *session) decodeActiveFrame(event readEvent) (protocol.Frame, *sessionFailure) {
	if event.messageType != websocket.MessageText {
		clear(event.data)
		return nil, failure("binary_not_supported", websocket.StatusUnsupportedData, true)
	}
	frame, err := protocol.Decode(event.data, s.handler.config.MaxEnvelopeBytes)
	clear(event.data)
	if err != nil {
		return nil, failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	direction, ok := protocol.DirectionFor(frameType(frame))
	if !ok || (direction != protocol.ControllerToRelay && direction != protocol.Bidirectional) {
		return nil, failure("invalid_direction", websocket.StatusPolicyViolation, true)
	}
	return frame, nil
}

func (s *session) handleActiveFrame(ctx context.Context, state *activeState, frame protocol.Frame) (bool, *sessionFailure) {
	switch message := frame.(type) {
	case *protocol.Heartbeat:
		if message.Sequence <= state.heartbeatIn {
			return false, failure("heartbeat_replay", websocket.StatusPolicyViolation, true)
		}
		state.heartbeatIn = message.Sequence
		return false, nil
	case *protocol.Ack:
		return false, s.handleDecision(ctx, state, message, true, "")
	case *protocol.Reject:
		return false, s.handleDecision(ctx, state, message, false, message.Code)
	case *protocol.SubscriptionsSync:
		return false, s.handleSubscriptionSync(ctx, state, message)
	case *protocol.BindingRemove:
		return false, s.handleBindingRemoval(ctx, message)
	case *protocol.KeyRevoke:
		return s.handleKeyRevocation(ctx, message)
	case *protocol.ControllerRevoke:
		return s.handleControllerRevocation(ctx, message)
	case *protocol.KeyRotationPropose:
		return false, s.handleRotationProposal(ctx, message)
	case *protocol.KeyRotationConfirm:
		return false, s.handleRotationConfirmation(ctx, message)
	case *protocol.KeyRotationFinalize:
		return s.handleRotationFinalization(ctx, message)
	case *protocol.ProtocolError:
		if message.Fatal {
			return true, nil
		}
		return false, nil
	default:
		return false, failure("unexpected_frame", websocket.StatusPolicyViolation, true)
	}
}

func (s *session) handleSubscriptionSync(ctx context.Context, state *activeState, message *protocol.SubscriptionsSync) *sessionFailure {
	if len(message.Subscriptions) > s.handler.config.MaxSubscriptions {
		return failure("subscriptions_sync_failed", websocket.StatusPolicyViolation, true)
	}
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	subscriptions := make([]store.Subscription, len(message.Subscriptions))
	next := make(map[string]struct{}, len(message.Subscriptions))
	for index, subscription := range message.Subscriptions {
		subscriptions[index] = store.Subscription{SubscriptionID: subscription.SubscriptionID, InstallationID: subscription.InstallationID, RepositoryID: subscription.RepositoryID, Ref: subscription.Ref}
		next[subscription.SubscriptionID] = struct{}{}
	}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplySubscriptionsSync(storeCtx, s.lease, command, message.Generation, subscriptions)
		return applyErr
	}); err != nil || result.Kind != store.ResultSubscriptionsSynced {
		return failure("subscriptions_sync_failed", websocket.StatusPolicyViolation, true)
	}
	for subscriptionID, messageID := range state.sourceCurrent {
		if _, exists := next[subscriptionID]; !exists {
			state.targets[messageID].state = targetStale
			delete(state.sourceCurrent, subscriptionID)
		}
	}
	s.subscriptions = next
	s.syncComplete = true
	response, err := s.subscriptionsSyncedFrame(message.MessageID, result.Generation, result.Count)
	if err != nil || s.sendFrame(ctx, response) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}
	return nil
}

func (s *session) handleDecision(ctx context.Context, state *activeState, frame protocol.Frame, accepted bool, code string) *sessionFailure {
	command, err := canonicalSessionCommand(frame, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	var targetMessageID string
	var source *protocol.SourceTarget
	var access *protocol.AccessTarget
	switch value := frame.(type) {
	case *protocol.Ack:
		targetMessageID, source, access = value.TargetMessageID, value.Source, value.Access
	case *protocol.Reject:
		targetMessageID, source, access = value.TargetMessageID, value.Source, value.Access
	}
	target, exists := state.targets[targetMessageID]
	if !exists {
		return s.handleDecisionProtocolError(ctx, frameMessageID(frame), command, "unknown_target")
	}
	if (target.kind == targetSource && (source == nil || access != nil || source.SubscriptionID != target.subscriptionID || source.Generation != target.generation)) || (target.kind == targetAccess && (access == nil || source != nil || access.EventID != target.eventID)) {
		return s.handleDecisionProtocolError(ctx, frameMessageID(frame), command, "target_mismatch")
	}
	if target.state == targetTerminal {
		return s.handleDecisionProtocolError(ctx, frameMessageID(frame), command, "unknown_target")
	}
	if target.state == targetStale {
		return s.handleDecisionProtocolError(ctx, frameMessageID(frame), command, "stale_target")
	}
	var result store.SessionCommandResult
	if target.kind == targetSource {
		err = s.withStore(ctx, func(storeCtx context.Context) error {
			var applyErr error
			result, applyErr = s.handler.store.ApplySourceDecision(storeCtx, s.lease, command, target.subscriptionID, target.generation, target.messageID, accepted, code)
			return applyErr
		})
	} else {
		err = s.withStore(ctx, func(storeCtx context.Context) error {
			var applyErr error
			result, applyErr = s.handler.store.ApplyAccessDecision(storeCtx, s.lease, command, target.eventID, target.messageID, accepted, code)
			return applyErr
		})
	}
	if err != nil {
		return failure("decision_failed", websocket.StatusInternalError, true)
	}
	if result.Kind == store.ResultProtocolError {
		if result.ErrorCode == "unknown_target" {
			s.markTerminal(state, target)
		}
		return s.nonfatalProtocolError(ctx, frameMessageID(frame), result.ErrorCode)
	}
	if result.Kind != store.ResultDecisionApplied {
		return failure("decision_failed", websocket.StatusInternalError, true)
	}
	s.markTerminal(state, target)
	return nil
}

func (s *session) handleDecisionProtocolError(ctx context.Context, messageID string, command store.SessionCommand, code string) *sessionFailure {
	var result store.SessionCommandResult
	err := s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyDecisionProtocolError(storeCtx, s.lease, command, code)
		return applyErr
	})
	if errors.Is(err, store.ErrConflict) {
		return failure("replay_mismatch", websocket.StatusPolicyViolation, true)
	}
	if err != nil {
		return failure("decision_failed", websocket.StatusInternalError, true)
	}
	switch result.Kind {
	case store.ResultDecisionApplied:
		return nil
	case store.ResultProtocolError:
		return s.nonfatalProtocolError(ctx, messageID, result.ErrorCode)
	default:
		return failure("decision_failed", websocket.StatusInternalError, true)
	}
}

func (s *session) markTerminal(state *activeState, target *deliveryTarget) {
	target.state = targetTerminal
	if target.kind == targetSource {
		if state.sourceCurrent[target.subscriptionID] == target.messageID {
			delete(state.sourceCurrent, target.subscriptionID)
		}
	} else if state.accessCurrent[target.eventID] == target.messageID {
		delete(state.accessCurrent, target.eventID)
	}
}

func (s *session) nonfatalProtocolError(ctx context.Context, target, code string) *sessionFailure {
	if s.sendProtocolError(ctx, target, code, false) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}
	return nil
}

func (s *session) pollDeliveries(ctx context.Context, state *activeState) *sessionFailure {
	if !s.syncComplete {
		return nil
	}
	pollCtx, cancel := context.WithTimeout(ctx, s.handler.config.WriteTimeout)
	defer cancel()
	sent := 0
	var access []store.PendingAccess
	available := s.handler.config.MaxOutstanding - state.outstanding()
	oneSlotSourceFirst := available == 1 && state.preferSource
	if available > 0 {
		state.preferSource = !state.preferSource
		limit := min(protocol.MaxArrayItems, available+len(state.accessCurrent))
		if err := s.withStore(pollCtx, func(storeCtx context.Context) error {
			var queryErr error
			access, queryErr = s.handler.store.PendingAccess(storeCtx, s.lease, limit)
			return queryErr
		}); err != nil {
			return failure("delivery_unavailable", websocket.StatusTryAgainLater, true)
		}
	}
	sendAccess := func(item store.PendingAccess) (bool, *sessionFailure) {
		if _, exists := state.accessCurrent[item.EventID]; exists || state.outstanding() >= s.handler.config.MaxOutstanding || sent >= maxDeliveryFramesPerPoll {
			return false, nil
		}
		envelope, err := s.envelope(protocol.TypeAccessChange)
		if err != nil {
			return false, failure("internal_error", websocket.StatusInternalError, true)
		}
		frame := &protocol.AccessChange{Envelope: envelope, EventID: item.EventID, InstallationID: item.InstallationID, RepositoryID: item.RepositoryID, ChangeCode: item.ChangeCode, ObservedAt: item.ObservedAt, AckRequired: true}
		if err = s.sendFrame(pollCtx, frame); err != nil {
			return false, failure("write_failed", websocket.StatusInternalError, false)
		}
		target := &deliveryTarget{kind: targetAccess, state: targetCurrent, messageID: frame.MessageID, eventID: item.EventID}
		state.remember(target, s.handler.config.MaxOutstanding)
		state.accessCurrent[item.EventID] = frame.MessageID
		sent++
		return true, nil
	}
	accessIndex, accessSent := 0, 0
	accessQuota := min((maxDeliveryFramesPerPoll+1)/2, s.handler.config.MaxOutstanding-state.outstanding())
	if oneSlotSourceFirst {
		accessQuota = 0
	}
	for accessIndex < len(access) && accessSent < accessQuota {
		delivered, sendFailure := sendAccess(access[accessIndex])
		accessIndex++
		if sendFailure != nil {
			return sendFailure
		}
		if delivered {
			accessSent++
		}
	}

	var desired []store.DesiredState
	if err := s.withStore(pollCtx, func(storeCtx context.Context) error {
		var queryErr error
		desired, queryErr = s.handler.store.PendingDesired(storeCtx, s.lease, min(protocol.MaxArrayItems, s.handler.config.MaxOutstanding))
		return queryErr
	}); err != nil {
		return failure("delivery_unavailable", websocket.StatusTryAgainLater, true)
	}
	for _, item := range desired {
		if sent >= maxDeliveryFramesPerPoll {
			break
		}
		if _, subscribed := s.subscriptions[item.SubscriptionID]; !subscribed {
			continue
		}
		if messageID, exists := state.sourceCurrent[item.SubscriptionID]; exists {
			current := state.targets[messageID]
			if current.generation >= item.Generation {
				continue
			}
			current.state = targetStale
			delete(state.sourceCurrent, item.SubscriptionID)
		}
		if state.outstanding() >= s.handler.config.MaxOutstanding {
			break
		}
		envelope, err := s.envelope(protocol.TypeSourceDesired)
		if err != nil {
			return failure("internal_error", websocket.StatusInternalError, true)
		}
		frame := &protocol.SourceDesired{Envelope: envelope, DeliveryID: item.DeliveryID, SubscriptionID: item.SubscriptionID, Generation: item.Generation, InstallationID: item.InstallationID, RepositoryID: item.RepositoryID, Ref: item.Ref, ObservedSHA: item.SHA, ObservedAt: item.ObservedAt}
		if err = s.sendFrame(pollCtx, frame); err != nil {
			return failure("write_failed", websocket.StatusInternalError, false)
		}
		target := &deliveryTarget{kind: targetSource, state: targetCurrent, messageID: frame.MessageID, subscriptionID: item.SubscriptionID, generation: item.Generation}
		state.remember(target, s.handler.config.MaxOutstanding)
		state.sourceCurrent[item.SubscriptionID] = frame.MessageID
		sent++
	}
	for accessIndex < len(access) && sent < maxDeliveryFramesPerPoll && state.outstanding() < s.handler.config.MaxOutstanding {
		_, sendFailure := sendAccess(access[accessIndex])
		accessIndex++
		if sendFailure != nil {
			return sendFailure
		}
	}
	return nil
}

func (state *activeState) outstanding() int {
	return len(state.sourceCurrent) + len(state.accessCurrent)
}

func (state *activeState) remember(target *deliveryTarget, maximum int) {
	state.targets[target.messageID] = target
	state.targetOrder = append(state.targetOrder, target.messageID)
	limit := maximum * 2
	for len(state.targets) > limit {
		removed := false
		for index, messageID := range state.targetOrder {
			candidate := state.targets[messageID]
			if candidate != nil && candidate.state != targetCurrent {
				delete(state.targets, messageID)
				state.targetOrder = append(state.targetOrder[:index], state.targetOrder[index+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			break
		}
	}
}

func (s *session) renewActiveLease(ctx context.Context) *sessionFailure {
	remaining := s.lease.ExpiresAt.Sub(s.handler.now().UTC())
	if remaining <= 0 {
		return failure("lease_lost", websocket.StatusPolicyViolation, true)
	}
	timeout := s.handler.config.StoreTimeout
	if remaining < timeout {
		timeout = remaining
	}
	renewCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lease, err := s.handler.store.RenewLease(renewCtx, s.lease, s.handler.config.LeaseDuration)
	if err != nil {
		return failure("lease_lost", websocket.StatusPolicyViolation, true)
	}
	s.lease = lease
	return nil
}

func resetTimer(timer Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.Chan():
		default:
		}
	}
	timer.Reset(duration)
}

func frameMessageID(frame protocol.Frame) string {
	switch value := frame.(type) {
	case *protocol.Ack:
		return value.MessageID
	case *protocol.Reject:
		return value.MessageID
	default:
		return ""
	}
}

func (s *session) handleBindingRemoval(ctx context.Context, message *protocol.BindingRemove) *sessionFailure {
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyBindingRemoval(storeCtx, s.lease, command, message.InstallationID, message.RepositoryID)
		return applyErr
	}); err != nil || result.Kind != store.ResultBindingRemoved {
		return failure("binding_remove_failed", websocket.StatusPolicyViolation, true)
	}
	envelope, err := s.envelope(protocol.TypeBindingRemoved)
	if err != nil || s.sendFrame(ctx, &protocol.BindingRemoved{Envelope: envelope, TargetMessageID: message.MessageID, InstallationID: result.InstallationID, RepositoryID: result.RepositoryID}) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}
	return nil
}

func (s *session) handleKeyRevocation(ctx context.Context, message *protocol.KeyRevoke) (bool, *sessionFailure) {
	if message.ControllerID != s.controllerID {
		return false, failure("identity_mismatch", websocket.StatusPolicyViolation, true)
	}
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return false, failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyKeyRevocation(storeCtx, s.lease, command, message.ControllerID, message.KeyID)
		return applyErr
	}); err != nil || result.Kind != store.ResultKeyRevoked {
		return false, failure("key_revoke_failed", websocket.StatusPolicyViolation, true)
	}
	envelope, err := s.envelope(protocol.TypeKeyRevoked)
	if err != nil || s.sendFrame(ctx, &protocol.KeyRevoked{Envelope: envelope, TargetMessageID: message.MessageID, ControllerID: result.ControllerID, KeyID: result.KeyID}) != nil {
		return false, failure("write_failed", websocket.StatusInternalError, false)
	}
	return message.KeyID == s.keyID, nil
}

func (s *session) handleControllerRevocation(ctx context.Context, message *protocol.ControllerRevoke) (bool, *sessionFailure) {
	if message.ControllerID != s.controllerID {
		return false, failure("identity_mismatch", websocket.StatusPolicyViolation, true)
	}
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return false, failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyControllerRevocation(storeCtx, s.lease, command, message.ControllerID)
		return applyErr
	}); err != nil || result.Kind != store.ResultControllerRevoked {
		return false, failure("controller_revoke_failed", websocket.StatusPolicyViolation, true)
	}
	envelope, err := s.envelope(protocol.TypeControllerRevoked)
	if err != nil || s.sendFrame(ctx, &protocol.ControllerRevoked{Envelope: envelope, TargetMessageID: message.MessageID, ControllerID: result.ControllerID}) != nil {
		return false, failure("write_failed", websocket.StatusInternalError, false)
	}
	return true, nil
}

func (s *session) handleRotationProposal(ctx context.Context, message *protocol.KeyRotationPropose) *sessionFailure {
	if message.ControllerID != s.controllerID || message.OldKeyID != s.keyID {
		return failure("identity_mismatch", websocket.StatusPolicyViolation, true)
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(message.NewPublicKey)
	if err != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	defer clear(publicKey)
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	input := store.RotationInput{RotationID: message.RotationID, ControllerID: message.ControllerID, OldKeyID: message.OldKeyID, NewKeyID: message.NewKeyID, SessionID: s.sessionID, NewPublicKey: publicKey}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyRotationProposal(storeCtx, s.lease, command, input, s.handler.config.ChallengeLifetime)
		return applyErr
	}); err != nil || result.Kind != store.ResultRotationChallenge {
		return failure("rotation_proposal_failed", websocket.StatusPolicyViolation, true)
	}
	defer result.Destroy()
	envelope, err := s.envelope(protocol.TypeKeyRotationChallenge)
	if err != nil || s.sendFrame(ctx, &protocol.KeyRotationChallenge{Envelope: envelope, TargetMessageID: message.MessageID, RotationID: result.RotationID, ServerNonce: base64.RawURLEncoding.EncodeToString(result.Nonce), ExpiresAt: result.ExpiresAt}) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}
	return nil
}

func (s *session) handleRotationConfirmation(ctx context.Context, message *protocol.KeyRotationConfirm) *sessionFailure {
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyRotationConfirmation(storeCtx, s.lease, command, message.RotationID, message.Signature)
		return applyErr
	}); err != nil || result.Kind != store.ResultRotationConfirmed {
		return failure("rotation_confirmation_failed", websocket.StatusPolicyViolation, true)
	}
	envelope, err := s.envelope(protocol.TypeKeyRotationConfirmed)
	if err != nil || s.sendFrame(ctx, &protocol.KeyRotationConfirmed{Envelope: envelope, TargetMessageID: message.MessageID, RotationID: result.RotationID}) != nil {
		return failure("write_failed", websocket.StatusInternalError, false)
	}
	return nil
}

func (s *session) handleRotationFinalization(ctx context.Context, message *protocol.KeyRotationFinalize) (bool, *sessionFailure) {
	command, err := canonicalSessionCommand(message, s.handler.config.MaxEnvelopeBytes)
	if err != nil {
		return false, failure("invalid_frame", websocket.StatusPolicyViolation, true)
	}
	var result store.SessionCommandResult
	if err = s.withStore(ctx, func(storeCtx context.Context) error {
		var applyErr error
		result, applyErr = s.handler.store.ApplyRotationFinalization(storeCtx, s.lease, command, message.RotationID)
		return applyErr
	}); err != nil || result.Kind != store.ResultRotationFinalized {
		return false, failure("rotation_finalization_failed", websocket.StatusPolicyViolation, true)
	}
	envelope, err := s.envelope(protocol.TypeKeyRotationFinalized)
	if err != nil || s.sendFrame(ctx, &protocol.KeyRotationFinalized{Envelope: envelope, TargetMessageID: message.MessageID, RotationID: result.RotationID, RetiredKeyID: result.RetiredKeyID}) != nil {
		return false, failure("write_failed", websocket.StatusInternalError, false)
	}
	return s.keyID == result.RetiredKeyID, nil
}
