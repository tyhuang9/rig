package controllerrelay

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

func (r *Repository) AdvanceSessionStatus(ctx context.Context, expectedEpoch, expectedFence uint64, next SessionStatus) error {
	if !validSessionStatus(next) || next.Epoch > math.MaxInt64 || next.Fence > math.MaxInt64 || !validNextFence(expectedEpoch, expectedFence, next.Epoch, next.Fence) {
		return ErrInvalid
	}
	var result sql.Result
	var err error
	args := []any{next.Epoch, next.Fence, next.State, nullable(next.KeyID), nullable(next.ErrorCode), next.Attempt, nullableTime(next.NextAttemptAt), nullableTime(next.LastReadyAt), nullableTime(next.LastSeenAt), timestamp(next.StateChangedAt), timestamp(next.UpdatedAt)}
	if expectedEpoch == 0 && expectedFence == 0 {
		result, err = r.db.ExecContext(ctx, `INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,key_id,last_error_code,attempt,next_attempt_at,last_ready_at,last_seen_at,state_changed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, append([]any{next.ControllerID}, args...)...)
	} else {
		args = append(args, next.ControllerID, expectedEpoch, expectedFence)
		result, err = r.db.ExecContext(ctx, `UPDATE relay_controller_session_state SET epoch=?,fence=?,state=?,key_id=?,last_error_code=?,attempt=?,next_attempt_at=?,last_ready_at=?,last_seen_at=?,state_changed_at=?,updated_at=? WHERE controller_id=? AND epoch=? AND fence=?`, args...)
	}
	if err != nil {
		if expectedEpoch == 0 && expectedFence == 0 && errors.Is(classifyConstraint(err), ErrConflict) {
			return ErrState
		}
		return classifyConstraint(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrState
	}
	return nil
}

func (r *Repository) SessionStatus(ctx context.Context, controllerID string) (SessionStatus, error) {
	if !canonicalUUID(controllerID) {
		return SessionStatus{}, ErrInvalid
	}
	var value SessionStatus
	var epoch, fence int64
	var changed, updated string
	var nextAttempt, lastReady, lastSeen sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT controller_id,epoch,fence,state,COALESCE(key_id,''),COALESCE(last_error_code,''),attempt,next_attempt_at,last_ready_at,last_seen_at,state_changed_at,updated_at FROM relay_controller_session_state WHERE controller_id=?`, controllerID).Scan(&value.ControllerID, &epoch, &fence, &value.State, &value.KeyID, &value.ErrorCode, &value.Attempt, &nextAttempt, &lastReady, &lastSeen, &changed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionStatus{}, ErrNotFound
	}
	if err != nil {
		return SessionStatus{}, err
	}
	value.Epoch, value.Fence = uint64(epoch), uint64(fence)
	if value.StateChangedAt, err = parseTimestamp(changed); err != nil {
		return SessionStatus{}, err
	}
	if value.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return SessionStatus{}, err
	}
	if value.NextAttemptAt, err = parseNullableTimestamp(nextAttempt); err != nil {
		return SessionStatus{}, err
	}
	if value.LastReadyAt, err = parseNullableTimestamp(lastReady); err != nil {
		return SessionStatus{}, err
	}
	value.LastSeenAt, err = parseNullableTimestamp(lastSeen)
	return value, err
}

func (r *Repository) CreateSubscription(ctx context.Context, value RelaySubscription) error {
	if !validSubscription(value) || value.State != SubscriptionActive || value.RetiredAt != nil {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO relay_controller_subscriptions(subscription_id,owner_user_id,binding_id,controller_id,installation_id,repository_id,tracked_ref,state,created_at,retired_at)
		SELECT ?,?,?,?,?,?,?,'active',?,NULL FROM relay_installation_bindings b
		WHERE b.binding_id=? AND b.owner_user_id=? AND b.controller_id=? AND b.installation_id=? AND b.repository_id=? AND b.state='authorized'`,
		value.SubscriptionID, value.OwnerUserID, value.BindingID, value.ControllerID, value.InstallationID, value.RepositoryID, value.Ref, timestamp(value.CreatedAt),
		value.BindingID, value.OwnerUserID, value.ControllerID, value.InstallationID, value.RepositoryID)
	if err != nil {
		return classifyConstraint(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) RetireSubscription(ctx context.Context, owner, subscriptionID string, at time.Time) error {
	if !validOpaqueID(owner) || !canonicalUUID(subscriptionID) || at.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_controller_subscriptions SET state='retired',retired_at=? WHERE owner_user_id=? AND subscription_id=? AND state='active'`, timestamp(at), owner, subscriptionID)
	return casResult(ctx, r.db, result, classifyConstraint(err), `SELECT COUNT(*) FROM relay_controller_subscriptions WHERE owner_user_id=? AND subscription_id=?`, owner, subscriptionID)
}

// AuthorizedBindings returns a bounded, deterministic all-owner view used by
// the single controller session. It never authorizes from cached provider data.
func (r *Repository) AuthorizedBindings(ctx context.Context, limit int) ([]InstallationBinding, error) {
	if limit < 1 || limit > protocol.MaxArrayItems {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, bindingSelect+` WHERE state='authorized' ORDER BY owner_user_id,binding_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var values []InstallationBinding
	for rows.Next() {
		value, scanErr := scanBinding(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return values, nil
}

// SessionAuthenticationCandidates returns at most two ordered keys. While
// Finalize is ambiguous the old active key is tried first and the pending key
// second; an exact completed Finalized response selects only the pending key.
func (r *Repository) SessionAuthenticationCandidates(ctx context.Context) (ControllerIdentity, []ControllerKey, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ControllerIdentity{}, nil, err
	}
	defer tx.Rollback()
	identity, err := scanIdentity(tx.QueryRowContext(ctx, identitySelect+` WHERE singleton=1 AND state='active'`))
	if errors.Is(err, sql.ErrNoRows) {
		return ControllerIdentity{}, nil, ErrNotFound
	}
	if err != nil {
		return ControllerIdentity{}, nil, err
	}
	var oldKeyID, newKeyID, commandState string
	err = tx.QueryRowContext(ctx, `SELECT r.old_key_id,r.new_key_id,c.state FROM relay_key_rotations r JOIN relay_outbound_commands c ON c.controller_id=r.controller_id AND c.rotation_id=r.rotation_id AND c.command_type='key.rotation.finalize' AND c.stage='finalize' WHERE r.controller_id=? AND r.state='finalize'`, identity.ControllerID).Scan(&oldKeyID, &newKeyID, &commandState)
	if errors.Is(err, sql.ErrNoRows) {
		active, keyErr := scanKey(tx.QueryRowContext(ctx, keySelect+` WHERE controller_id=? AND state='active'`, identity.ControllerID))
		if keyErr != nil {
			return ControllerIdentity{}, nil, keyErr
		}
		if err = tx.Commit(); err != nil {
			return ControllerIdentity{}, nil, err
		}
		return identity, []ControllerKey{active}, nil
	}
	if err != nil {
		return ControllerIdentity{}, nil, err
	}
	oldKey, err := scanKey(tx.QueryRowContext(ctx, keySelect+` WHERE controller_id=? AND key_id=?`, identity.ControllerID, oldKeyID))
	if err != nil {
		return ControllerIdentity{}, nil, err
	}
	newKey, err := scanKey(tx.QueryRowContext(ctx, keySelect+` WHERE controller_id=? AND key_id=?`, identity.ControllerID, newKeyID))
	if err != nil {
		return ControllerIdentity{}, nil, err
	}
	var candidates []ControllerKey
	switch commandState {
	case CommandPrepared:
		if oldKey.State != KeyActive || newKey.State != KeyPending {
			return ControllerIdentity{}, nil, ErrState
		}
		candidates = []ControllerKey{oldKey, newKey}
	case CommandCompleted:
		if newKey.State != KeyPending {
			return ControllerIdentity{}, nil, ErrState
		}
		candidates = []ControllerKey{newKey}
	default:
		return ControllerIdentity{}, nil, ErrState
	}
	if err = tx.Commit(); err != nil {
		return ControllerIdentity{}, nil, err
	}
	return identity, candidates, nil
}

func (r *Repository) SessionAuthenticationIdentity(ctx context.Context) (ControllerIdentity, ControllerKey, error) {
	identity, candidates, err := r.SessionAuthenticationCandidates(ctx)
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, err
	}
	if len(candidates) == 0 {
		return ControllerIdentity{}, ControllerKey{}, ErrNotFound
	}
	return identity, candidates[0], nil
}

func (r *Repository) PrepareSubscriptionSync(ctx context.Context, controllerID, messageID string, sentAt time.Time) (SyncSnapshot, error) {
	if !canonicalUUID(controllerID) || !canonicalUUID(messageID) || sentAt.IsZero() {
		return SyncSnapshot{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncSnapshot{}, err
	}
	defer tx.Rollback()
	var controllerState string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM relay_controllers WHERE controller_id=?`, controllerID).Scan(&controllerState); errors.Is(err, sql.ErrNoRows) {
		return SyncSnapshot{}, ErrNotFound
	} else if err != nil {
		return SyncSnapshot{}, err
	} else if controllerState != ControllerActive {
		return SyncSnapshot{}, ErrState
	}

	var acknowledged int64
	var inflightGeneration sql.NullInt64
	var inflightMessage sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT acknowledged_generation,inflight_generation,inflight_message_id FROM relay_subscription_sync_heads WHERE controller_id=?`, controllerID).Scan(&acknowledged, &inflightGeneration, &inflightMessage)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO relay_subscription_sync_heads(controller_id,acknowledged_generation,dirty,updated_at) SELECT ?,0,1,? FROM relay_controllers WHERE controller_id=? AND state='active'`, controllerID, timestamp(sentAt), controllerID); err != nil {
			return SyncSnapshot{}, classifyConstraint(err)
		}
		acknowledged = 0
	} else if err != nil {
		return SyncSnapshot{}, err
	}
	if inflightGeneration.Valid {
		value, err := loadSyncTx(ctx, tx, controllerID, uint64(inflightGeneration.Int64))
		if err != nil {
			return SyncSnapshot{}, err
		}
		if err = tx.Commit(); err != nil {
			return SyncSnapshot{}, err
		}
		return value, nil
	}
	items, err := activeProtocolSubscriptions(ctx, tx, controllerID)
	if err != nil {
		return SyncSnapshot{}, err
	}
	if len(items) > protocol.MaxArrayItems {
		return SyncSnapshot{}, ErrConflict
	}
	generation := acknowledged + 1
	digest, err := syncDigest(items)
	if err != nil {
		return SyncSnapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relay_subscription_sync_sets(controller_id,generation,message_id,sent_at,item_count,canonical_digest,state) VALUES(?,?,?,?,?,?,'inflight')`, controllerID, generation, messageID, timestamp(sentAt), len(items), digest[:]); err != nil {
		return SyncSnapshot{}, classifyConstraint(err)
	}
	for ordinal, item := range items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO relay_subscription_sync_items(controller_id,generation,ordinal,subscription_id,installation_id,repository_id,tracked_ref) VALUES(?,?,?,?,?,?,?)`, controllerID, generation, ordinal, item.SubscriptionID, item.InstallationID, item.RepositoryID, item.Ref); err != nil {
			return SyncSnapshot{}, classifyConstraint(err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE relay_subscription_sync_heads SET dirty=0,inflight_generation=?,inflight_message_id=?,updated_at=? WHERE controller_id=? AND acknowledged_generation=? AND inflight_generation IS NULL`, generation, messageID, timestamp(sentAt), controllerID, acknowledged)
	if err != nil {
		return SyncSnapshot{}, classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return SyncSnapshot{}, rowsErr
		}
		return SyncSnapshot{}, ErrState
	}
	if err = tx.Commit(); err != nil {
		return SyncSnapshot{}, err
	}
	return SyncSnapshot{ControllerID: controllerID, Generation: uint64(generation), MessageID: messageID, SentAt: sentAt.UTC(), Digest: digest, State: SyncInflight, Items: items}, nil
}

func (r *Repository) LoadSubscriptionSync(ctx context.Context, controllerID string) (SyncSnapshot, error) {
	if !canonicalUUID(controllerID) {
		return SyncSnapshot{}, ErrInvalid
	}
	var generation int64
	if err := r.db.QueryRowContext(ctx, `SELECT inflight_generation FROM relay_subscription_sync_heads WHERE controller_id=? AND inflight_generation IS NOT NULL`, controllerID).Scan(&generation); errors.Is(err, sql.ErrNoRows) {
		return SyncSnapshot{}, ErrNotFound
	} else if err != nil {
		return SyncSnapshot{}, err
	}
	return loadSyncTx(ctx, r.db, controllerID, uint64(generation))
}

func (r *Repository) AcknowledgeSubscriptionSync(ctx context.Context, controllerID, targetMessageID string, generation uint64, count uint32, at time.Time) error {
	if !canonicalUUID(controllerID) || !canonicalUUID(targetMessageID) || generation == 0 || generation > math.MaxInt64 || count > protocol.MaxArrayItems || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var storedCount int64
	err = tx.QueryRowContext(ctx, `SELECT state,item_count FROM relay_subscription_sync_sets WHERE controller_id=? AND generation=? AND message_id=?`, controllerID, generation, targetMessageID).Scan(&state, &storedCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if storedCount != int64(count) {
		return ErrConflict
	}
	if state == SyncAcked {
		return tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE relay_subscription_sync_sets SET state='acked',acked_at=? WHERE controller_id=? AND generation=? AND message_id=? AND state='inflight'`, timestamp(at), controllerID, generation, targetMessageID)
	if err != nil {
		return classifyConstraint(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	result, err = tx.ExecContext(ctx, `UPDATE relay_subscription_sync_heads SET acknowledged_generation=?,inflight_generation=NULL,inflight_message_id=NULL,updated_at=? WHERE controller_id=? AND acknowledged_generation=? AND inflight_generation=? AND inflight_message_id=?`, generation, timestamp(at), controllerID, generation-1, generation, targetMessageID)
	if err != nil {
		return classifyConstraint(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	return tx.Commit()
}

func (r *Repository) DurableACKState(ctx context.Context, controllerID string) ([]protocol.ACKState, error) {
	if !canonicalUUID(controllerID) {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT i.subscription_id,MAX(i.generation) FROM relay_source_event_inbox i JOIN relay_controller_subscriptions s ON s.subscription_id=i.subscription_id AND s.controller_id=i.controller_id WHERE i.controller_id=? AND s.state='active' GROUP BY i.subscription_id ORDER BY i.subscription_id`, controllerID)
	if err != nil {
		return nil, err
	}
	state := make([]protocol.ACKState, 0)
	for rows.Next() {
		var value protocol.ACKState
		var generation int64
		if err = rows.Scan(&value.SubscriptionID, &generation); err != nil {
			rows.Close()
			return nil, err
		}
		value.Generation = uint64(generation)
		state = append(state, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = protocol.ValidateACKState(state); err != nil {
		return nil, fmt.Errorf("stored relay ack state: %w", err)
	}
	return state, nil
}

func (r *Repository) CommitSourceDesired(ctx context.Context, controllerID string, source protocol.SourceDesired, receivedAt time.Time) (InboxDecision, error) {
	if !canonicalUUID(controllerID) || receivedAt.IsZero() || source.Generation > math.MaxInt64 || protocol.Validate(&source) != nil {
		return InboxDecision{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return InboxDecision{}, err
	}
	defer tx.Rollback()
	if err = requireActiveController(ctx, tx, controllerID); err != nil {
		return InboxDecision{}, err
	}
	decision, found, err := existingSourceDecision(ctx, tx, controllerID, source)
	if err != nil {
		return InboxDecision{}, err
	}
	if found {
		if err = tx.Commit(); err != nil {
			return InboxDecision{}, err
		}
		return decision, nil
	}
	var installationID, repositoryID int64
	var ref, state string
	err = tx.QueryRowContext(ctx, `SELECT installation_id,repository_id,tracked_ref,state FROM relay_controller_subscriptions WHERE controller_id=? AND subscription_id=?`, controllerID, source.SubscriptionID).Scan(&installationID, &repositoryID, &ref, &state)
	if errors.Is(err, sql.ErrNoRows) || state != SubscriptionActive {
		return RejectDecision(RejectUnknownSubscription), tx.Commit()
	}
	if err != nil {
		return InboxDecision{}, err
	}
	if installationID != source.InstallationID || repositoryID != source.RepositoryID || ref != source.Ref {
		return RejectDecision(RejectScopeMismatch), tx.Commit()
	}

	var durableMax sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(generation) FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=?`, controllerID, source.SubscriptionID).Scan(&durableMax); err != nil {
		return InboxDecision{}, err
	}
	if durableMax.Valid && source.Generation <= uint64(durableMax.Int64) {
		if err = tx.Commit(); err != nil {
			return InboxDecision{}, err
		}
		return RejectDecision(RejectGenerationConflict), nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relay_source_event_inbox(controller_id,delivery_id,subscription_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at,received_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, controllerID, source.DeliveryID, source.SubscriptionID, source.Generation, source.InstallationID, source.RepositoryID, source.Ref, source.ObservedSHA, timestamp(source.ObservedAt), timestamp(receivedAt))
	if err != nil {
		return InboxDecision{}, classifyConstraint(err)
	}
	if err = tx.Commit(); err != nil {
		return InboxDecision{}, err
	}
	return AckDecision(), nil
}

func (r *Repository) CommitAccessChange(ctx context.Context, controllerID string, change protocol.AccessChange, receivedAt time.Time) (InboxDecision, error) {
	if !canonicalUUID(controllerID) || receivedAt.IsZero() || protocol.Validate(&change) != nil || !validAccessCode(change.ChangeCode) || !validAccessScope(change.ChangeCode, change.RepositoryID) {
		return InboxDecision{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return InboxDecision{}, err
	}
	defer tx.Rollback()
	if err = requireActiveController(ctx, tx, controllerID); err != nil {
		return InboxDecision{}, err
	}
	var installationID, repositoryID int64
	var code, observed string
	err = tx.QueryRowContext(ctx, `SELECT installation_id,repository_id,change_code,observed_at FROM relay_access_event_inbox WHERE controller_id=? AND event_id=?`, controllerID, change.EventID).Scan(&installationID, &repositoryID, &code, &observed)
	if err == nil {
		if installationID != change.InstallationID || repositoryID != change.RepositoryID || code != change.ChangeCode || observed != timestamp(change.ObservedAt) {
			return RejectDecision(RejectInvalidEvent), tx.Commit()
		}
		if err = tx.Commit(); err != nil {
			return InboxDecision{}, err
		}
		return AckDecision(), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return InboxDecision{}, err
	}
	applicabilityQuery := `SELECT COUNT(*) FROM relay_installation_bindings WHERE controller_id=? AND installation_id=? AND state IN ('authorized','removal_pending','access_lost')`
	applicabilityArgs := []any{controllerID, change.InstallationID}
	if change.RepositoryID > 0 {
		applicabilityQuery += ` AND repository_id=?`
		applicabilityArgs = append(applicabilityArgs, change.RepositoryID)
	}
	var applicable int
	if err = tx.QueryRowContext(ctx, applicabilityQuery, applicabilityArgs...).Scan(&applicable); err != nil {
		return InboxDecision{}, err
	}
	if applicable == 0 {
		if err = tx.Commit(); err != nil {
			return InboxDecision{}, err
		}
		return RejectDecision(RejectInvalidEvent), nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relay_access_event_inbox(controller_id,event_id,installation_id,repository_id,change_code,observed_at,received_at) VALUES(?,?,?,?,?,?,?)`, controllerID, change.EventID, change.InstallationID, change.RepositoryID, change.ChangeCode, timestamp(change.ObservedAt), timestamp(receivedAt)); err != nil {
		return InboxDecision{}, classifyConstraint(err)
	}
	if accessRemoval(change.ChangeCode) {
		query := `UPDATE relay_installation_bindings SET state='access_lost',state_changed_at=?,last_error_code='source_access_lost',updated_at=? WHERE controller_id=? AND installation_id=? AND state IN ('authorized','removal_pending')`
		args := []any{timestamp(receivedAt), timestamp(receivedAt), controllerID, change.InstallationID}
		if change.RepositoryID > 0 {
			query += ` AND repository_id=?`
			args = append(args, change.RepositoryID)
		}
		if _, err = tx.ExecContext(ctx, query, args...); err != nil {
			return InboxDecision{}, classifyConstraint(err)
		}
		retireQuery := `UPDATE relay_controller_subscriptions SET state='retired',retired_at=? WHERE controller_id=? AND installation_id=? AND state='active'`
		retireArgs := []any{timestamp(receivedAt), controllerID, change.InstallationID}
		if change.RepositoryID > 0 {
			retireQuery += ` AND repository_id=?`
			retireArgs = append(retireArgs, change.RepositoryID)
		}
		if _, err = tx.ExecContext(ctx, retireQuery, retireArgs...); err != nil {
			return InboxDecision{}, classifyConstraint(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return InboxDecision{}, err
	}
	return AckDecision(), nil
}

func (r *Repository) PrepareControlCommand(ctx context.Context, value OutboundCommand) (OutboundCommand, error) {
	if !validOutboundCommand(value) || value.State != CommandPrepared || value.CompletedAt != nil {
		return OutboundCommand{}, ErrInvalid
	}
	if existing, err := r.loadControlCommandForAggregate(ctx, value); err == nil {
		return r.compatibleControlCommand(ctx, existing, value)
	} else if !errors.Is(err, ErrNotFound) {
		return OutboundCommand{}, err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest,state,completed_at) VALUES(?,?,?,?,?,?,?,?, 'prepared',NULL)`, value.ControllerID, value.MessageID, value.CommandType, nullable(value.BindingID), nullable(value.RotationID), value.Stage, timestamp(value.SentAt), value.Digest[:])
	if err == nil {
		return value, nil
	}
	if !errors.Is(classifyConstraint(err), ErrConflict) {
		return OutboundCommand{}, err
	}
	existing, loadErr := r.loadControlCommandForAggregate(ctx, value)
	if loadErr == nil {
		return r.compatibleControlCommand(ctx, existing, value)
	}
	if !errors.Is(loadErr, ErrNotFound) {
		return OutboundCommand{}, loadErr
	}
	return OutboundCommand{}, ErrConflict
}

// PrepareBindingRemoval atomically fences the owner-scoped binding and creates
// (or returns) its single immutable removal command.
func (r *Repository) PrepareBindingRemoval(ctx context.Context, owner, bindingID string, candidate OutboundCommand, at time.Time) (InstallationBinding, OutboundCommand, error) {
	if !validOpaqueID(owner) || !canonicalUUID(bindingID) || at.IsZero() || !validOutboundCommand(candidate) || candidate.CommandType != CommandBindingRemove || candidate.BindingID != bindingID || candidate.State != CommandPrepared {
		return InstallationBinding{}, OutboundCommand{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return InstallationBinding{}, OutboundCommand{}, err
	}
	defer tx.Rollback()
	binding, err := scanBinding(tx.QueryRowContext(ctx, bindingSelect+` WHERE owner_user_id=? AND binding_id=?`, owner, bindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return InstallationBinding{}, OutboundCommand{}, ErrNotFound
	}
	if err != nil {
		return InstallationBinding{}, OutboundCommand{}, err
	}
	if candidate.ControllerID != binding.ControllerID {
		return InstallationBinding{}, OutboundCommand{}, ErrInvalid
	}

	existing, existingErr := loadControlCommandTx(ctx, tx, candidate.ControllerID, bindingID, "", "remove")
	if existingErr == nil {
		if existing.CommandType != CommandBindingRemove || existing.BindingID != bindingID {
			return InstallationBinding{}, OutboundCommand{}, ErrConflict
		}
		if binding.State != BindingRemovalPending && !(binding.State == BindingRemoved && existing.State == CommandCompleted) {
			return InstallationBinding{}, OutboundCommand{}, ErrState
		}
		if err = tx.Commit(); err != nil {
			return InstallationBinding{}, OutboundCommand{}, err
		}
		return binding, existing, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return InstallationBinding{}, OutboundCommand{}, existingErr
	}
	if binding.State != BindingAuthorized && binding.State != BindingAccessLost && binding.State != BindingRemovalPending {
		return InstallationBinding{}, OutboundCommand{}, ErrState
	}
	if binding.State != BindingRemovalPending {
		result, updateErr := tx.ExecContext(ctx, `UPDATE relay_installation_bindings SET state='removal_pending',state_changed_at=?,updated_at=?,last_error_code=NULL WHERE owner_user_id=? AND binding_id=? AND state IN ('authorized','access_lost')`, timestamp(at), timestamp(at), owner, bindingID)
		if updateErr != nil {
			return InstallationBinding{}, OutboundCommand{}, classifyConstraint(updateErr)
		}
		if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
			if rowsErr != nil {
				return InstallationBinding{}, OutboundCommand{}, rowsErr
			}
			return InstallationBinding{}, OutboundCommand{}, ErrState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE relay_controller_subscriptions SET state='retired',retired_at=? WHERE controller_id=? AND binding_id=? AND state='active'`, timestamp(at), binding.ControllerID, bindingID); err != nil {
			return InstallationBinding{}, OutboundCommand{}, classifyConstraint(err)
		}
		binding.State = BindingRemovalPending
		binding.StateChangedAt = at.UTC()
		binding.UpdatedAt = at.UTC()
		binding.LastErrorCode = ""
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest,state,completed_at) VALUES(?,?,?,?,NULL,'remove',?,?,'prepared',NULL)`, candidate.ControllerID, candidate.MessageID, candidate.CommandType, bindingID, timestamp(candidate.SentAt), candidate.Digest[:]); err != nil {
		return InstallationBinding{}, OutboundCommand{}, classifyConstraint(err)
	}
	if err = tx.Commit(); err != nil {
		return InstallationBinding{}, OutboundCommand{}, err
	}
	return binding, candidate, nil
}

// CompleteBindingRemoval validates the exact relay response and commits both
// the command ledger and binding terminal state in the same transaction.
func (r *Repository) CompleteBindingRemoval(ctx context.Context, controllerID string, response protocol.BindingRemoved, at time.Time) error {
	if !canonicalUUID(controllerID) || protocol.Validate(&response) != nil || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	command, err := scanOutboundCommand(tx.QueryRowContext(ctx, `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND message_id=?`, controllerID, response.TargetMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if command.CommandType != CommandBindingRemove || command.Stage != "remove" || !canonicalUUID(command.BindingID) {
		return ErrState
	}
	binding, err := scanBinding(tx.QueryRowContext(ctx, bindingSelect+` WHERE controller_id=? AND binding_id=?`, controllerID, command.BindingID))
	if err != nil {
		return err
	}
	if binding.InstallationID != response.InstallationID || binding.RepositoryID != response.RepositoryID {
		return ErrConflict
	}
	if command.State == CommandCompleted && binding.State == BindingRemoved {
		return tx.Commit()
	}
	if command.State != CommandPrepared || binding.State != BindingRemovalPending {
		return ErrState
	}
	if err = completeControlCommandTx(ctx, tx, controllerID, response.TargetMessageID, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE relay_installation_bindings SET state='removed',state_changed_at=?,updated_at=?,completed_at=?,last_error_code=NULL WHERE controller_id=? AND binding_id=? AND state='removal_pending'`, timestamp(at), timestamp(at), timestamp(at), controllerID, binding.BindingID)
	if err != nil {
		return classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	return tx.Commit()
}

// PendingControlCommands returns a deterministic bounded replay set. Callers
// reconstruct protocol frames from public aggregate metadata; no raw frame or
// sensitive protocol material is persisted here.
func (r *Repository) PendingControlCommands(ctx context.Context, controllerID string, limit int) ([]OutboundCommand, error) {
	if !canonicalUUID(controllerID) || limit < 1 || limit > protocol.MaxArrayItems {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND state='prepared' AND command_type<>'key.rotation.confirm' ORDER BY sent_at,message_id LIMIT ?`, controllerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commands := make([]OutboundCommand, 0)
	for rows.Next() {
		command, scanErr := scanOutboundCommand(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (r *Repository) BindingForController(ctx context.Context, controllerID, bindingID string) (InstallationBinding, error) {
	if !canonicalUUID(controllerID) || !canonicalUUID(bindingID) {
		return InstallationBinding{}, ErrInvalid
	}
	value, err := scanBinding(r.db.QueryRowContext(ctx, bindingSelect+` WHERE controller_id=? AND binding_id=?`, controllerID, bindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return InstallationBinding{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) RotationByNewKey(ctx context.Context, controllerID, keyID string) (KeyRotation, error) {
	if !canonicalUUID(controllerID) || !canonicalUUID(keyID) {
		return KeyRotation{}, ErrInvalid
	}
	value, err := scanRotation(r.db.QueryRowContext(ctx, rotationSelect+` WHERE controller_id=? AND new_key_id=? ORDER BY created_at DESC LIMIT 1`, controllerID, keyID))
	if errors.Is(err, sql.ErrNoRows) {
		return KeyRotation{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) ControlCommandForAggregate(ctx context.Context, controllerID, bindingID, rotationID, stage string) (OutboundCommand, error) {
	if !canonicalUUID(controllerID) || (bindingID == "") == (rotationID == "") || bindingID != "" && !canonicalUUID(bindingID) || rotationID != "" && !canonicalUUID(rotationID) {
		return OutboundCommand{}, ErrInvalid
	}
	if stage != "remove" && stage != "propose" && stage != "confirm" && stage != "finalize" {
		return OutboundCommand{}, ErrInvalid
	}
	query := `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND binding_id=? AND stage=?`
	aggregateID := bindingID
	if aggregateID == "" {
		query = `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND rotation_id=? AND stage=?`
		aggregateID = rotationID
	}
	value, err := scanOutboundCommand(r.db.QueryRowContext(ctx, query, controllerID, aggregateID, stage))
	if errors.Is(err, sql.ErrNoRows) {
		return OutboundCommand{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) loadControlCommandForAggregate(ctx context.Context, value OutboundCommand) (OutboundCommand, error) {
	query := `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND binding_id=? AND stage=?`
	aggregateID := value.BindingID
	if aggregateID == "" {
		query = `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND rotation_id=? AND stage=?`
		aggregateID = value.RotationID
	}
	return scanOutboundCommand(r.db.QueryRowContext(ctx, query, value.ControllerID, aggregateID, value.Stage))
}

func (r *Repository) compatibleControlCommand(ctx context.Context, existing, requested OutboundCommand) (OutboundCommand, error) {
	if existing.ControllerID != requested.ControllerID || existing.CommandType != requested.CommandType || existing.BindingID != requested.BindingID || existing.RotationID != requested.RotationID || existing.Stage != requested.Stage || existing.Digest != requested.Digest {
		return OutboundCommand{}, ErrConflict
	}
	if existing.MessageID == requested.MessageID {
		if !existing.SentAt.Equal(requested.SentAt) {
			return OutboundCommand{}, ErrConflict
		}
		return existing, nil
	}
	byMessage, err := r.LoadControlCommand(ctx, requested.ControllerID, requested.MessageID)
	if err == nil && byMessage.MessageID != existing.MessageID {
		return OutboundCommand{}, ErrConflict
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return OutboundCommand{}, err
	}
	return existing, nil
}

func (r *Repository) LoadControlCommand(ctx context.Context, controllerID, messageID string) (OutboundCommand, error) {
	if !canonicalUUID(controllerID) || !canonicalUUID(messageID) {
		return OutboundCommand{}, ErrInvalid
	}
	return scanOutboundCommand(r.db.QueryRowContext(ctx, `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND message_id=?`, controllerID, messageID))
}

func (r *Repository) CompleteControlCommand(ctx context.Context, controllerID, messageID string, at time.Time) error {
	if !canonicalUUID(controllerID) || !canonicalUUID(messageID) || at.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_outbound_commands SET state='completed',completed_at=? WHERE controller_id=? AND message_id=? AND state='prepared'`, timestamp(at), controllerID, messageID)
	if err != nil {
		return classifyConstraint(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	existing, err := r.LoadControlCommand(ctx, controllerID, messageID)
	if err != nil {
		return err
	}
	if existing.State == CommandCompleted {
		return nil
	}
	return ErrState
}

func (r *Repository) PrepareRotationProposal(ctx context.Context, candidate OutboundCommand, at time.Time) (KeyRotation, OutboundCommand, error) {
	return r.prepareRotationStage(ctx, candidate, RotationPrepare, RotationPropose, "", at)
}

func (r *Repository) PrepareRotationConfirmation(ctx context.Context, targetMessageID string, candidate OutboundCommand, at time.Time) (KeyRotation, OutboundCommand, error) {
	return r.prepareRotationStage(ctx, candidate, RotationPropose, RotationConfirm, targetMessageID, at)
}

func (r *Repository) prepareRotationStage(ctx context.Context, candidate OutboundCommand, expectedState, nextState, targetMessageID string, at time.Time) (KeyRotation, OutboundCommand, error) {
	if !validOutboundCommand(candidate) || candidate.State != CommandPrepared || candidate.RotationID == "" || at.IsZero() ||
		(nextState == RotationPropose && candidate.CommandType != CommandRotationPropose) ||
		(nextState == RotationConfirm && (candidate.CommandType != CommandRotationConfirm || !canonicalUUID(targetMessageID))) {
		return KeyRotation{}, OutboundCommand{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	defer tx.Rollback()
	rotation, err := scanRotation(tx.QueryRowContext(ctx, rotationSelect+` WHERE controller_id=? AND rotation_id=?`, candidate.ControllerID, candidate.RotationID))
	if errors.Is(err, sql.ErrNoRows) {
		return KeyRotation{}, OutboundCommand{}, ErrNotFound
	}
	if err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	if !rotation.ExpiresAt.After(at) {
		return KeyRotation{}, OutboundCommand{}, ErrState
	}
	if targetMessageID != "" {
		target, targetErr := scanOutboundCommand(tx.QueryRowContext(ctx, `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND message_id=?`, candidate.ControllerID, targetMessageID))
		if errors.Is(targetErr, sql.ErrNoRows) {
			return KeyRotation{}, OutboundCommand{}, ErrNotFound
		}
		if targetErr != nil {
			return KeyRotation{}, OutboundCommand{}, targetErr
		}
		if target.CommandType != CommandRotationPropose || target.RotationID != candidate.RotationID || target.Stage != "propose" || target.State != CommandPrepared {
			return KeyRotation{}, OutboundCommand{}, ErrState
		}
	}
	existing, existingErr := loadControlCommandTx(ctx, tx, candidate.ControllerID, "", candidate.RotationID, candidate.Stage)
	if existingErr == nil {
		if existing.CommandType != candidate.CommandType {
			return KeyRotation{}, OutboundCommand{}, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return KeyRotation{}, OutboundCommand{}, err
		}
		return rotation, existing, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return KeyRotation{}, OutboundCommand{}, existingErr
	}
	if rotation.State != expectedState && rotation.State != nextState {
		return KeyRotation{}, OutboundCommand{}, ErrState
	}
	if rotation.State == expectedState {
		result, updateErr := tx.ExecContext(ctx, `UPDATE relay_key_rotations SET state=?,state_changed_at=?,updated_at=? WHERE controller_id=? AND rotation_id=? AND state=?`, nextState, timestamp(at), timestamp(at), candidate.ControllerID, candidate.RotationID, expectedState)
		if updateErr != nil {
			return KeyRotation{}, OutboundCommand{}, classifyConstraint(updateErr)
		}
		if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
			if rowsErr != nil {
				return KeyRotation{}, OutboundCommand{}, rowsErr
			}
			return KeyRotation{}, OutboundCommand{}, ErrState
		}
		rotation.State = nextState
		rotation.StateChangedAt = at.UTC()
		rotation.UpdatedAt = at.UTC()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest,state,completed_at) VALUES(?,?,?,NULL,?,?,?,?, 'prepared',NULL)`, candidate.ControllerID, candidate.MessageID, candidate.CommandType, candidate.RotationID, candidate.Stage, timestamp(candidate.SentAt), candidate.Digest[:]); err != nil {
		return KeyRotation{}, OutboundCommand{}, classifyConstraint(err)
	}
	if err = tx.Commit(); err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	return rotation, candidate, nil
}

// ConfirmRotationAndPrepareFinalize records the exact confirmation response,
// advances through the local recovery-armed state, and prepares Finalize in a
// single transaction. Finalize is still sent on the old-key session.
func (r *Repository) ConfirmRotationAndPrepareFinalize(ctx context.Context, targetMessageID, rotationID, sessionKeyID string, candidate OutboundCommand, at time.Time) (KeyRotation, OutboundCommand, error) {
	if !canonicalUUID(targetMessageID) || !canonicalUUID(rotationID) || !canonicalUUID(sessionKeyID) || at.IsZero() || !validOutboundCommand(candidate) || candidate.CommandType != CommandRotationFinalize || candidate.RotationID != rotationID || candidate.State != CommandPrepared {
		return KeyRotation{}, OutboundCommand{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	defer tx.Rollback()
	rotation, err := scanRotation(tx.QueryRowContext(ctx, rotationSelect+` WHERE controller_id=? AND rotation_id=?`, candidate.ControllerID, rotationID))
	if errors.Is(err, sql.ErrNoRows) {
		return KeyRotation{}, OutboundCommand{}, ErrNotFound
	}
	if err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	if rotation.OldKeyID != sessionKeyID {
		return KeyRotation{}, OutboundCommand{}, ErrState
	}
	confirm, err := scanOutboundCommand(tx.QueryRowContext(ctx, `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND message_id=?`, candidate.ControllerID, targetMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return KeyRotation{}, OutboundCommand{}, ErrNotFound
	}
	if err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	if confirm.CommandType != CommandRotationConfirm || confirm.Stage != "confirm" || confirm.RotationID != rotationID {
		return KeyRotation{}, OutboundCommand{}, ErrConflict
	}
	existing, existingErr := loadControlCommandTx(ctx, tx, candidate.ControllerID, "", rotationID, "finalize")
	if existingErr == nil {
		if rotation.State != RotationFinalize || confirm.State != CommandCompleted || existing.CommandType != CommandRotationFinalize {
			return KeyRotation{}, OutboundCommand{}, ErrState
		}
		if err = tx.Commit(); err != nil {
			return KeyRotation{}, OutboundCommand{}, err
		}
		return rotation, existing, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return KeyRotation{}, OutboundCommand{}, existingErr
	}
	if rotation.State != RotationConfirm || confirm.State != CommandPrepared || !rotation.ExpiresAt.After(at) {
		return KeyRotation{}, OutboundCommand{}, ErrState
	}
	completedCommands, err := tx.ExecContext(ctx, `UPDATE relay_outbound_commands SET state='completed',completed_at=? WHERE controller_id=? AND rotation_id=? AND stage IN ('propose','confirm') AND state='prepared'`, timestamp(at), candidate.ControllerID, rotationID)
	if err != nil {
		return KeyRotation{}, OutboundCommand{}, classifyConstraint(err)
	}
	if count, rowsErr := completedCommands.RowsAffected(); rowsErr != nil || count != 2 {
		if rowsErr != nil {
			return KeyRotation{}, OutboundCommand{}, rowsErr
		}
		return KeyRotation{}, OutboundCommand{}, ErrState
	}
	for _, transition := range [][2]string{{RotationConfirm, RotationNewKeyAuth}, {RotationNewKeyAuth, RotationFinalize}} {
		result, updateErr := tx.ExecContext(ctx, `UPDATE relay_key_rotations SET state=?,state_changed_at=?,updated_at=? WHERE controller_id=? AND rotation_id=? AND state=?`, transition[1], timestamp(at), timestamp(at), candidate.ControllerID, rotationID, transition[0])
		if updateErr != nil {
			return KeyRotation{}, OutboundCommand{}, classifyConstraint(updateErr)
		}
		if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
			if rowsErr != nil {
				return KeyRotation{}, OutboundCommand{}, rowsErr
			}
			return KeyRotation{}, OutboundCommand{}, ErrState
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest,state,completed_at) VALUES(?,?,?,NULL,?,?,?,?, 'prepared',NULL)`, candidate.ControllerID, candidate.MessageID, candidate.CommandType, rotationID, candidate.Stage, timestamp(candidate.SentAt), candidate.Digest[:]); err != nil {
		return KeyRotation{}, OutboundCommand{}, classifyConstraint(err)
	}
	rotation.State = RotationFinalize
	rotation.StateChangedAt = at.UTC()
	rotation.UpdatedAt = at.UTC()
	if err = tx.Commit(); err != nil {
		return KeyRotation{}, OutboundCommand{}, err
	}
	return rotation, candidate, nil
}

const revokedRotationKeyCleanupEligibility = `k.state='revoked'
		AND k.protected_key_cleared_at IS NULL
		AND (EXISTS (SELECT 1 FROM relay_key_rotations completed WHERE completed.controller_id=k.controller_id AND completed.old_key_id=k.key_id AND completed.state='completed')
			OR EXISTS (SELECT 1 FROM relay_key_rotations failed WHERE failed.controller_id=k.controller_id AND failed.new_key_id=k.key_id AND failed.state='failed')
			OR (k.activated_at IS NULL AND k.possession_confirmed_at IS NULL AND NOT EXISTS (SELECT 1 FROM relay_key_rotations referenced WHERE referenced.controller_id=k.controller_id AND (referenced.old_key_id=k.key_id OR referenced.new_key_id=k.key_id))))
		AND NOT EXISTS (SELECT 1 FROM relay_key_rotations live WHERE live.controller_id=k.controller_id AND live.state IN ('prepare','propose','confirm','new_key_auth','finalize') AND (live.old_key_id=k.key_id OR live.new_key_id=k.key_id))
		AND NOT EXISTS (SELECT 1 FROM relay_controller_session_state session WHERE session.controller_id=k.controller_id AND session.key_id=k.key_id)`

const revokedRotationKeyCleanupSelect = `SELECT k.controller_id,k.key_id,k.protected_key_ref FROM relay_controller_keys k WHERE ` + revokedRotationKeyCleanupEligibility

// RevokedRotationKeyCleanupCandidate revalidates one exact deletion target.
// Callers hold the per-controller cleanup lease while using this result.
func (r *Repository) RevokedRotationKeyCleanupCandidate(ctx context.Context, candidate RevokedKeyCleanupCandidate) (RevokedKeyCleanupCandidate, error) {
	if !canonicalUUID(candidate.ControllerID) || !canonicalUUID(candidate.KeyID) || candidate.ProtectedKeyRef != ProtectedKeyRef(candidate.ControllerID, candidate.KeyID) {
		return RevokedKeyCleanupCandidate{}, ErrInvalid
	}
	var value RevokedKeyCleanupCandidate
	err := r.db.QueryRowContext(ctx, revokedRotationKeyCleanupSelect+` AND k.controller_id=? AND k.key_id=? AND k.protected_key_ref=?`, candidate.ControllerID, candidate.KeyID, candidate.ProtectedKeyRef).Scan(&value.ControllerID, &value.KeyID, &value.ProtectedKeyRef)
	if errors.Is(err, sql.ErrNoRows) {
		return RevokedKeyCleanupCandidate{}, ErrNotFound
	}
	if err != nil {
		return RevokedKeyCleanupCandidate{}, err
	}
	if value != candidate {
		return RevokedKeyCleanupCandidate{}, ErrConflict
	}
	return value, nil
}

// RevokedRotationKeyCleanupCandidates pages over only keys whose completed or
// failed rotation makes their protected private material obsolete. The cursor
// is caller-owned so failures cannot permanently starve later keys.
func (r *Repository) RevokedRotationKeyCleanupCandidates(ctx context.Context, cursor string, limit int) (RevokedKeyCleanupPage, error) {
	if limit < 1 || limit > protocol.MaxArrayItems {
		return RevokedKeyCleanupPage{}, ErrInvalid
	}
	cursorControllerID, cursorKeyID, err := parseRevokedKeyCleanupCursor(cursor)
	if err != nil {
		return RevokedKeyCleanupPage{}, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, revokedRotationKeyCleanupSelect+`
		AND (?='' OR k.controller_id>? OR (k.controller_id=? AND k.key_id>?))
		ORDER BY k.controller_id,k.key_id LIMIT ?`, cursorControllerID, cursorControllerID, cursorControllerID, cursorKeyID, limit)
	if err != nil {
		return RevokedKeyCleanupPage{}, err
	}
	defer rows.Close()
	page := RevokedKeyCleanupPage{Candidates: make([]RevokedKeyCleanupCandidate, 0, limit)}
	for rows.Next() {
		var candidate RevokedKeyCleanupCandidate
		if err = rows.Scan(&candidate.ControllerID, &candidate.KeyID, &candidate.ProtectedKeyRef); err != nil {
			return RevokedKeyCleanupPage{}, err
		}
		if candidate.ProtectedKeyRef != ProtectedKeyRef(candidate.ControllerID, candidate.KeyID) {
			return RevokedKeyCleanupPage{}, ErrConflict
		}
		page.Candidates = append(page.Candidates, candidate)
	}
	if err = rows.Err(); err != nil {
		return RevokedKeyCleanupPage{}, err
	}
	if len(page.Candidates) < limit {
		page.Complete = true
	} else {
		last := page.Candidates[len(page.Candidates)-1]
		page.NextCursor = revokedKeyCleanupCursor(last.ControllerID, last.KeyID)
	}
	return page, nil
}

// MarkRevokedControllerKeyCleared records the absence of one exact protected
// file only after the credential store has reported successful deletion.
func (r *Repository) MarkRevokedControllerKeyCleared(ctx context.Context, candidate RevokedKeyCleanupCandidate, at time.Time) error {
	if !canonicalUUID(candidate.ControllerID) || !canonicalUUID(candidate.KeyID) || candidate.ProtectedKeyRef != ProtectedKeyRef(candidate.ControllerID, candidate.KeyID) || at.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_controller_keys AS k SET protected_key_cleared_at=?
		WHERE k.controller_id=? AND k.key_id=? AND k.protected_key_ref=? AND k.state='revoked' AND k.protected_key_cleared_at IS NULL
		AND (EXISTS (SELECT 1 FROM relay_key_rotations completed WHERE completed.controller_id=k.controller_id AND completed.old_key_id=k.key_id AND completed.state='completed')
			OR EXISTS (SELECT 1 FROM relay_key_rotations failed WHERE failed.controller_id=k.controller_id AND failed.new_key_id=k.key_id AND failed.state='failed')
			OR (k.activated_at IS NULL AND k.possession_confirmed_at IS NULL AND NOT EXISTS (SELECT 1 FROM relay_key_rotations referenced WHERE referenced.controller_id=k.controller_id AND (referenced.old_key_id=k.key_id OR referenced.new_key_id=k.key_id))))
		AND NOT EXISTS (SELECT 1 FROM relay_key_rotations live WHERE live.controller_id=k.controller_id AND live.state IN ('prepare','propose','confirm','new_key_auth','finalize') AND (live.old_key_id=k.key_id OR live.new_key_id=k.key_id))
		AND NOT EXISTS (SELECT 1 FROM relay_controller_session_state session WHERE session.controller_id=k.controller_id AND session.key_id=k.key_id)`, timestamp(at), candidate.ControllerID, candidate.KeyID, candidate.ProtectedKeyRef)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	var state, protectedRef string
	var cleared sql.NullString
	err = r.db.QueryRowContext(ctx, `SELECT state,protected_key_ref,protected_key_cleared_at FROM relay_controller_keys WHERE controller_id=? AND key_id=?`, candidate.ControllerID, candidate.KeyID).Scan(&state, &protectedRef, &cleared)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state == KeyRevoked && protectedRef == candidate.ProtectedKeyRef && cleared.Valid {
		return nil
	}
	return ErrState
}

func revokedKeyCleanupCursor(controllerID, keyID string) string {
	return "v1:" + controllerID + ":" + keyID
}

func parseRevokedKeyCleanupCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	const cursorLength = len("v1:") + 36 + 1 + 36
	if len(cursor) != cursorLength || !strings.HasPrefix(cursor, "v1:") || cursor[39] != ':' {
		return "", "", ErrInvalid
	}
	controllerID, keyID := cursor[3:39], cursor[40:]
	if !canonicalUUID(controllerID) || !canonicalUUID(keyID) || cursor != revokedKeyCleanupCursor(controllerID, keyID) {
		return "", "", ErrInvalid
	}
	return controllerID, keyID, nil
}

// CompleteRotationFinalized records only the relay-side terminal response. The
// local key swap remains gated by a separately durable fenced Ready.
func (r *Repository) CompleteRotationFinalized(ctx context.Context, controllerID string, response protocol.KeyRotationFinalized, at time.Time) error {
	if !canonicalUUID(controllerID) || protocol.Validate(&response) != nil || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	command, err := scanOutboundCommand(tx.QueryRowContext(ctx, `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND message_id=?`, controllerID, response.TargetMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if command.CommandType != CommandRotationFinalize || command.Stage != "finalize" || command.RotationID != response.RotationID {
		return ErrConflict
	}
	rotation, err := scanRotation(tx.QueryRowContext(ctx, rotationSelect+` WHERE controller_id=? AND rotation_id=?`, controllerID, response.RotationID))
	if err != nil {
		return err
	}
	if rotation.State != RotationFinalize || rotation.OldKeyID != response.RetiredKeyID {
		return ErrConflict
	}
	if command.State == CommandCompleted {
		return tx.Commit()
	}
	if err = completeControlCommandTx(ctx, tx, controllerID, response.TargetMessageID, at); err != nil {
		return err
	}
	return tx.Commit()
}

// FailExpiredRotation releases a rotation that is still safely recoverable
// under the old active key. Finalize is intentionally excluded because relay
// activation may already be externally committed at that stage.
func (r *Repository) FailExpiredRotation(ctx context.Context, controllerID, rotationID string, at time.Time) error {
	if !canonicalUUID(controllerID) || !canonicalUUID(rotationID) || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rotation, err := scanRotation(tx.QueryRowContext(ctx, rotationSelect+` WHERE controller_id=? AND rotation_id=?`, controllerID, rotationID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if rotation.State == RotationFailed {
		return tx.Commit()
	}
	if rotation.ExpiresAt.After(at) || (rotation.State != RotationPrepare && rotation.State != RotationPropose && rotation.State != RotationConfirm && rotation.State != RotationNewKeyAuth) {
		return ErrState
	}
	stamp := timestamp(at)
	result, err := tx.ExecContext(ctx, `UPDATE relay_key_rotations SET state='failed',state_changed_at=?,updated_at=?,completed_at=?,last_error_code='rotation_failed' WHERE controller_id=? AND rotation_id=? AND state IN ('prepare','propose','confirm','new_key_auth')`, stamp, stamp, stamp, controllerID, rotationID)
	if err != nil {
		return classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relay_outbound_commands SET state='completed',completed_at=? WHERE controller_id=? AND rotation_id=? AND state='prepared'`, stamp, controllerID, rotationID); err != nil {
		return classifyConstraint(err)
	}
	result, err = tx.ExecContext(ctx, `UPDATE relay_controller_keys SET state='revoked',updated_at=?,revoked_at=? WHERE controller_id=? AND key_id=? AND state='pending'`, stamp, stamp, controllerID, rotation.NewKeyID)
	if err != nil {
		return classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	return tx.Commit()
}

func loadControlCommandTx(ctx context.Context, tx *sql.Tx, controllerID, bindingID, rotationID, stage string) (OutboundCommand, error) {
	query := `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND binding_id=? AND stage=?`
	aggregateID := bindingID
	if aggregateID == "" {
		query = `SELECT controller_id,message_id,command_type,COALESCE(binding_id,''),COALESCE(rotation_id,''),stage,sent_at,canonical_digest,state,completed_at FROM relay_outbound_commands WHERE controller_id=? AND rotation_id=? AND stage=?`
		aggregateID = rotationID
	}
	value, err := scanOutboundCommand(tx.QueryRowContext(ctx, query, controllerID, aggregateID, stage))
	if errors.Is(err, sql.ErrNoRows) {
		return OutboundCommand{}, ErrNotFound
	}
	return value, err
}

func completeControlCommandTx(ctx context.Context, tx *sql.Tx, controllerID, messageID string, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE relay_outbound_commands SET state='completed',completed_at=? WHERE controller_id=? AND message_id=? AND state='prepared'`, timestamp(at), controllerID, messageID)
	if err != nil {
		return classifyConstraint(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrState
	}
	return nil
}

// CompleteRotationAfterReady performs the local half of two-phase rotation.
// The exact fenced Ready observation for the pending key must already be
// durable; no network operation occurs in this method.
func (r *Repository) CompleteRotationAfterReady(ctx context.Context, controllerID, rotationID string, readyEpoch, readyFence uint64, at time.Time) error {
	if !canonicalUUID(controllerID) || !canonicalUUID(rotationID) || readyEpoch == 0 || readyFence == 0 || readyEpoch > math.MaxInt64 || readyFence > math.MaxInt64 || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldKeyID, newKeyID, state string
	err = tx.QueryRowContext(ctx, `SELECT old_key_id,new_key_id,state FROM relay_key_rotations WHERE controller_id=? AND rotation_id=?`, controllerID, rotationID).Scan(&oldKeyID, &newKeyID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state == RotationCompleted {
		return tx.Commit()
	}
	if state != RotationFinalize {
		return ErrState
	}
	var ready int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_controller_session_state WHERE controller_id=? AND epoch=? AND fence=? AND state='ready' AND key_id=?`, controllerID, readyEpoch, readyFence, newKeyID).Scan(&ready); err != nil {
		return err
	}
	if ready != 1 {
		return ErrState
	}
	result, err := tx.ExecContext(ctx, `UPDATE relay_controller_keys SET state='revoked',updated_at=?,revoked_at=? WHERE controller_id=? AND key_id=? AND state='active'`, timestamp(at), timestamp(at), controllerID, oldKeyID)
	if err != nil {
		return classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	result, err = tx.ExecContext(ctx, `UPDATE relay_controller_keys SET state='active',updated_at=?,activated_at=?,possession_confirmed_at=? WHERE controller_id=? AND key_id=? AND state='pending'`, timestamp(at), timestamp(at), timestamp(at), controllerID, newKeyID)
	if err != nil {
		return classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	result, err = tx.ExecContext(ctx, `UPDATE relay_key_rotations SET state='completed',state_changed_at=?,updated_at=?,completed_at=? WHERE controller_id=? AND rotation_id=? AND state='finalize'`, timestamp(at), timestamp(at), timestamp(at), controllerID, rotationID)
	if err != nil {
		return classifyConstraint(err)
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	return tx.Commit()
}

func validSessionStatus(value SessionStatus) bool {
	if !canonicalUUID(value.ControllerID) || value.Epoch == 0 || value.Fence == 0 || value.Attempt > 1000000 || value.StateChangedAt.IsZero() || value.UpdatedAt.IsZero() || !validSessionError(value.ErrorCode) || value.LastSeenAt != nil && value.LastReadyAt == nil {
		return false
	}
	switch value.State {
	case SessionReady:
		return canonicalUUID(value.KeyID) && value.ErrorCode == "" && value.Attempt == 0 && value.NextAttemptAt == nil && value.LastReadyAt != nil
	case SessionConnecting, SessionAuthenticating:
		return canonicalUUID(value.KeyID) && value.ErrorCode == "" && value.NextAttemptAt == nil
	case SessionDisconnected:
		return value.KeyID == "" && value.ErrorCode == "" && value.Attempt == 0 && value.NextAttemptAt == nil
	case SessionBackoff:
		return value.KeyID == "" && value.ErrorCode != "" && value.Attempt > 0 && value.NextAttemptAt != nil
	case SessionNeedsAttention:
		return value.KeyID == "" && value.ErrorCode != "" && value.NextAttemptAt == nil
	case SessionStopped:
		return value.KeyID == "" && value.ErrorCode == "" && value.Attempt == 0 && value.NextAttemptAt == nil
	default:
		return false
	}
}

func validNextFence(expectedEpoch, expectedFence, nextEpoch, nextFence uint64) bool {
	if expectedEpoch == 0 && expectedFence == 0 {
		return nextEpoch == 1 && nextFence == 1
	}
	return nextEpoch == expectedEpoch && nextFence == expectedFence+1 || nextEpoch == expectedEpoch+1 && nextFence == 1
}

func validSessionError(code string) bool {
	switch code {
	case "", ErrorKeyRevoked, ErrorProtocol, ErrorRelayUnavailable, ErrorSourceAccessLost, ErrorRotationFailed:
		return true
	default:
		return false
	}
}

func validSubscription(value RelaySubscription) bool {
	return canonicalUUID(value.SubscriptionID) && validOpaqueID(value.OwnerUserID) && canonicalUUID(value.BindingID) && canonicalUUID(value.ControllerID) && value.InstallationID > 0 && value.RepositoryID > 0 && protocol.ValidRef(value.Ref) == nil && !value.CreatedAt.IsZero()
}

func activeProtocolSubscriptions(ctx context.Context, tx *sql.Tx, controllerID string) ([]protocol.Subscription, error) {
	rows, err := tx.QueryContext(ctx, `SELECT subscription_id,installation_id,repository_id,tracked_ref FROM relay_controller_subscriptions WHERE controller_id=? AND state='active' ORDER BY subscription_id`, controllerID)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.Subscription, 0)
	for rows.Next() {
		var item protocol.Subscription
		if err = rows.Scan(&item.SubscriptionID, &item.InstallationID, &item.RepositoryID, &item.Ref); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return items, nil
}

func syncDigest(items []protocol.Subscription) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(items)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func loadSyncTx(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, controllerID string, generation uint64) (SyncSnapshot, error) {
	var value SyncSnapshot
	var rawGeneration int64
	var sent string
	var digest []byte
	var count int
	var acked sql.NullString
	err := query.QueryRowContext(ctx, `SELECT controller_id,generation,message_id,sent_at,item_count,canonical_digest,state,acked_at FROM relay_subscription_sync_sets WHERE controller_id=? AND generation=?`, controllerID, generation).Scan(&value.ControllerID, &rawGeneration, &value.MessageID, &sent, &count, &digest, &value.State, &acked)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncSnapshot{}, ErrNotFound
	}
	if err != nil {
		return SyncSnapshot{}, err
	}
	value.Generation = uint64(rawGeneration)
	if value.SentAt, err = parseTimestamp(sent); err != nil {
		return SyncSnapshot{}, err
	}
	if len(digest) != len(value.Digest) {
		return SyncSnapshot{}, ErrConflict
	}
	copy(value.Digest[:], digest)
	if value.AckedAt, err = parseNullableTimestamp(acked); err != nil {
		return SyncSnapshot{}, err
	}
	value.Items = make([]protocol.Subscription, 0, count)
	rows, err := query.QueryContext(ctx, `SELECT subscription_id,installation_id,repository_id,tracked_ref FROM relay_subscription_sync_items WHERE controller_id=? AND generation=? ORDER BY ordinal`, controllerID, generation)
	if err != nil {
		return SyncSnapshot{}, err
	}
	for rows.Next() {
		var item protocol.Subscription
		if err = rows.Scan(&item.SubscriptionID, &item.InstallationID, &item.RepositoryID, &item.Ref); err != nil {
			rows.Close()
			return SyncSnapshot{}, err
		}
		value.Items = append(value.Items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return SyncSnapshot{}, err
	}
	if err = rows.Close(); err != nil {
		return SyncSnapshot{}, err
	}
	if len(value.Items) != count {
		return SyncSnapshot{}, ErrConflict
	}
	wantDigest, err := syncDigest(value.Items)
	if err != nil {
		return SyncSnapshot{}, err
	}
	if wantDigest != value.Digest {
		return SyncSnapshot{}, ErrConflict
	}
	return value, nil
}

func existingSourceDecision(ctx context.Context, tx *sql.Tx, controllerID string, source protocol.SourceDesired) (InboxDecision, bool, error) {
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`SELECT delivery_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at FROM relay_source_event_inbox WHERE controller_id=? AND delivery_id=? AND subscription_id=?`, []any{controllerID, source.DeliveryID, source.SubscriptionID}},
		{`SELECT delivery_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=? AND generation=?`, []any{controllerID, source.SubscriptionID, source.Generation}},
	} {
		var delivery, ref, sha, observed string
		var generation uint64
		var installation, repository int64
		err := tx.QueryRowContext(ctx, query.sql, query.args...).Scan(&delivery, &generation, &installation, &repository, &ref, &sha, &observed)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return InboxDecision{}, false, err
		}
		if delivery == source.DeliveryID && generation == source.Generation && installation == source.InstallationID && repository == source.RepositoryID && ref == source.Ref && sha == source.ObservedSHA && observed == timestamp(source.ObservedAt) {
			return AckDecision(), true, nil
		}
		return RejectDecision(RejectGenerationConflict), true, nil
	}
	return InboxDecision{}, false, nil
}

func requireActiveController(ctx context.Context, tx *sql.Tx, controllerID string) error {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM relay_controllers WHERE controller_id=?`, controllerID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if state != ControllerActive {
		return ErrState
	}
	return nil
}

func validAccessCode(code string) bool {
	switch code {
	case "installation.created", "installation.removed", "installation.restored", "installation.permissions_updated", "installation.repositories_reconciled", "repository.added", "repository.removed":
		return true
	default:
		return false
	}
}

func accessRemoval(code string) bool {
	return code == "installation.removed" || code == "repository.removed"
}

func validAccessScope(code string, repositoryID int64) bool {
	if len(code) >= len("installation.") && code[:len("installation.")] == "installation." {
		return repositoryID == 0
	}
	return repositoryID > 0
}

func validOutboundCommand(value OutboundCommand) bool {
	if !canonicalUUID(value.ControllerID) || !canonicalUUID(value.MessageID) || value.SentAt.IsZero() {
		return false
	}
	switch value.CommandType {
	case CommandBindingRemove:
		return canonicalUUID(value.BindingID) && value.RotationID == "" && value.Stage == "remove"
	case CommandRotationPropose:
		return value.BindingID == "" && canonicalUUID(value.RotationID) && value.Stage == "propose"
	case CommandRotationConfirm:
		return value.BindingID == "" && canonicalUUID(value.RotationID) && value.Stage == "confirm"
	case CommandRotationFinalize:
		return value.BindingID == "" && canonicalUUID(value.RotationID) && value.Stage == "finalize"
	default:
		return false
	}
}

func scanOutboundCommand(row scanner) (OutboundCommand, error) {
	var value OutboundCommand
	var sent string
	var digest []byte
	var completed sql.NullString
	err := row.Scan(&value.ControllerID, &value.MessageID, &value.CommandType, &value.BindingID, &value.RotationID, &value.Stage, &sent, &digest, &value.State, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboundCommand{}, ErrNotFound
	}
	if err != nil {
		return OutboundCommand{}, err
	}
	if len(digest) != len(value.Digest) {
		return OutboundCommand{}, ErrConflict
	}
	copy(value.Digest[:], digest)
	if value.SentAt, err = parseTimestamp(sent); err != nil {
		return OutboundCommand{}, err
	}
	value.CompletedAt, err = parseNullableTimestamp(completed)
	return value, err
}
