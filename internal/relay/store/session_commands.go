package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"math"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/jackc/pgx/v5"
)

type SessionCommandType string

const (
	CommandSubscriptionsSync SessionCommandType = "subscriptions.sync"
	CommandAckSource         SessionCommandType = "ack.source"
	CommandRejectSource      SessionCommandType = "reject.source"
	CommandAckAccess         SessionCommandType = "ack.access"
	CommandRejectAccess      SessionCommandType = "reject.access"
	CommandBindingRemove     SessionCommandType = "binding.remove"
	CommandControllerRevoke  SessionCommandType = "controller.revoke"
	CommandKeyRevoke         SessionCommandType = "key.revoke"
	CommandRotationPropose   SessionCommandType = "key.rotation.propose"
	CommandRotationConfirm   SessionCommandType = "key.rotation.confirm"
	CommandRotationFinalize  SessionCommandType = "key.rotation.finalize"
)

type SessionCommand struct {
	MessageID string
	Type      SessionCommandType
	Digest    [sha256.Size]byte
}

type SessionCommandResultKind string

const (
	ResultSubscriptionsSynced SessionCommandResultKind = "subscriptions_synced"
	ResultDecisionApplied     SessionCommandResultKind = "decision_applied"
	ResultProtocolError       SessionCommandResultKind = "protocol_error"
	ResultBindingRemoved      SessionCommandResultKind = "binding_removed"
	ResultControllerRevoked   SessionCommandResultKind = "controller_revoked"
	ResultKeyRevoked          SessionCommandResultKind = "key_revoked"
	ResultRotationChallenge   SessionCommandResultKind = "rotation_challenge"
	ResultRotationConfirmed   SessionCommandResultKind = "rotation_confirmed"
	ResultRotationFinalized   SessionCommandResultKind = "rotation_finalized"
)

// SessionCommandResult is deliberately a closed set of scalar fields mirrored
// by migration 005. It cannot carry an opaque frame, JSON payload, or secret.
type SessionCommandResult struct {
	Kind           SessionCommandResultKind
	ErrorCode      string
	Generation     uint64
	Count          uint32
	InstallationID int64
	RepositoryID   int64
	ControllerID   string
	KeyID          string
	RotationID     string
	RetiredKeyID   string
	Nonce          []byte
	ExpiresAt      time.Time
}

func (r *SessionCommandResult) Destroy() {
	if r != nil {
		clear(r.Nonce)
	}
}

type commandPreparation struct {
	keyID  string
	replay bool
	result SessionCommandResult
}

// prepareSessionCommand must be called only after the command's topology
// locks. It establishes the global command -> controller-session -> lease ->
// session/controller/key row order before replay or mutation.
func (s *Store) prepareSessionCommand(ctx context.Context, tx pgx.Tx, lease Lease, command SessionCommand) (commandPreparation, error) {
	if !validLease(lease) || !validUUID(command.MessageID) || !validSessionCommandType(command.Type) || command.Digest == ([sha256.Size]byte{}) {
		return commandPreparation{}, ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, sessionCommandLockKey(lease.ControllerID, command.MessageID)); err != nil {
		return commandPreparation{}, err
	}
	if err := acquireControllerSessionLock(ctx, tx, lease.ControllerID); err != nil {
		return commandPreparation{}, err
	}
	now := s.now().UTC()
	var leaseExpires time.Time
	err := tx.QueryRow(ctx, `SELECT expires_at FROM relay_controller_leases WHERE controller_id=$1 AND session_id=$2 AND lease_id=$3 AND fence=$4 FOR UPDATE`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).Scan(&leaseExpires)
	if isNoRows(err) {
		return commandPreparation{}, ErrConflict
	}
	if err != nil {
		return commandPreparation{}, err
	}
	var sessionExpires time.Time
	var sessionRevoked sql.NullTime
	var controllerState, keyState, keyID string
	err = tx.QueryRow(ctx, `SELECT s.expires_at,s.revoked_at,c.state,k.state,s.key_id::text FROM relay_sessions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_controller_keys k ON k.controller_id=s.controller_id AND k.key_id=s.key_id WHERE s.controller_id=$1 AND s.session_id=$2 FOR UPDATE OF s,c,k`, lease.ControllerID, lease.SessionID).Scan(&sessionExpires, &sessionRevoked, &controllerState, &keyState, &keyID)
	if isNoRows(err) {
		return commandPreparation{}, ErrConflict
	}
	if err != nil {
		return commandPreparation{}, err
	}
	if !leaseExpires.After(now) || !sessionExpires.After(now) || sessionRevoked.Valid || controllerState != "active" || keyState != "active" {
		return commandPreparation{}, ErrConflict
	}

	var storedType, kind string
	var storedDigest, nonce []byte
	var errorCode, controllerID, resultKeyID, rotationID, retiredKeyID sql.NullString
	var generation, count, installationID, repositoryID sql.NullInt64
	var expiresAt sql.NullTime
	err = tx.QueryRow(ctx, `SELECT command_type,command_digest,result_kind,result_error_code,result_generation,result_count,result_installation_id,result_repository_id,result_controller_id::text,result_key_id::text,result_rotation_id::text,result_retired_key_id::text,result_nonce,result_expires_at FROM relay_session_commands WHERE controller_id=$1 AND message_id=$2 FOR UPDATE`, lease.ControllerID, command.MessageID).Scan(&storedType, &storedDigest, &kind, &errorCode, &generation, &count, &installationID, &repositoryID, &controllerID, &resultKeyID, &rotationID, &retiredKeyID, &nonce, &expiresAt)
	if isNoRows(err) {
		return commandPreparation{keyID: keyID}, nil
	}
	if err != nil {
		return commandPreparation{}, err
	}
	if storedType != string(command.Type) || !bytes.Equal(storedDigest, command.Digest[:]) {
		clear(nonce)
		return commandPreparation{}, ErrConflict
	}
	result := SessionCommandResult{Kind: SessionCommandResultKind(kind), Nonce: nonce}
	if errorCode.Valid {
		result.ErrorCode = errorCode.String
	}
	if generation.Valid {
		result.Generation = uint64(generation.Int64)
	}
	if count.Valid {
		result.Count = uint32(count.Int64)
	}
	if installationID.Valid {
		result.InstallationID = installationID.Int64
	}
	if repositoryID.Valid {
		result.RepositoryID = repositoryID.Int64
	}
	if controllerID.Valid {
		result.ControllerID = controllerID.String
	}
	if resultKeyID.Valid {
		result.KeyID = resultKeyID.String
	}
	if rotationID.Valid {
		result.RotationID = rotationID.String
	}
	if retiredKeyID.Valid {
		result.RetiredKeyID = retiredKeyID.String
	}
	if expiresAt.Valid {
		result.ExpiresAt = expiresAt.Time
	}
	if validateSessionCommandResult(command.Type, result) != nil {
		result.Destroy()
		return commandPreparation{}, ErrConflict
	}
	return commandPreparation{keyID: keyID, replay: true, result: result}, nil
}

func (s *Store) commitSessionCommand(ctx context.Context, tx pgx.Tx, lease Lease, command SessionCommand, result SessionCommandResult) (SessionCommandResult, error) {
	if err := validateSessionCommandResult(command.Type, result); err != nil {
		result.Destroy()
		return SessionCommandResult{}, err
	}
	now := s.now().UTC()
	_, err := tx.Exec(ctx, `INSERT INTO relay_session_commands(controller_id,message_id,session_id,lease_id,lease_fence,command_digest,command_type,result_kind,result_error_code,result_generation,result_count,result_installation_id,result_repository_id,result_controller_id,result_key_id,result_rotation_id,result_retired_key_id,result_nonce,result_expires_at,applied_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, lease.ControllerID, command.MessageID, lease.SessionID, lease.LeaseID, lease.Fence, command.Digest[:], string(command.Type), string(result.Kind), optionalString(result.ErrorCode), optionalUint64(result.Generation), optionalCount(result.Kind, result.Count), optionalPositive(result.InstallationID), optionalPositive(result.RepositoryID), optionalUUID(result.ControllerID), optionalUUID(result.KeyID), optionalUUID(result.RotationID), optionalUUID(result.RetiredKeyID), optionalBytes(result.Nonce), optionalTime(result.ExpiresAt), now)
	if err != nil {
		result.Destroy()
		return SessionCommandResult{}, conflictError(err)
	}
	return s.finishSessionCommand(ctx, tx, lease, result)
}

func (s *Store) commitSessionCommandReplay(ctx context.Context, tx pgx.Tx, lease Lease, result SessionCommandResult) (SessionCommandResult, error) {
	return s.finishSessionCommand(ctx, tx, lease, result)
}

func (s *Store) finishSessionCommand(ctx context.Context, tx pgx.Tx, lease Lease, result SessionCommandResult) (SessionCommandResult, error) {
	tag, err := tx.Exec(ctx, `UPDATE relay_sessions SET last_seen_at=$2 WHERE session_id=$1`, lease.SessionID, s.now().UTC())
	if err != nil {
		result.Destroy()
		return SessionCommandResult{}, err
	}
	if tag.RowsAffected() != 1 {
		result.Destroy()
		return SessionCommandResult{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		result.Destroy()
		return SessionCommandResult{}, err
	}
	return result, nil
}

func (s *Store) ApplySubscriptionsSync(ctx context.Context, lease Lease, command SessionCommand, generation uint64, subscriptions []Subscription) (SessionCommandResult, error) {
	if command.Type != CommandSubscriptionsSync {
		return SessionCommandResult{}, ErrInvalid
	}
	seen, err := validateSubscriptions(lease.ControllerID, generation, subscriptions)
	if err != nil {
		return SessionCommandResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	locks, err := s.prepareSubscriptionLocks(ctx, tx, lease.ControllerID, subscriptions)
	if err != nil {
		return SessionCommandResult{}, err
	}
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	if err = s.syncSubscriptionsLocked(ctx, tx, lease.ControllerID, generation, subscriptions, seen, locks); err != nil {
		return SessionCommandResult{}, err
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultSubscriptionsSynced, Generation: generation, Count: uint32(len(subscriptions))})
}

// ApplyDecisionProtocolError atomically records a locally detected decision
// protocol error. Exact command replays return their original durable result,
// including a previously applied decision, without replacing it with error.
func (s *Store) ApplyDecisionProtocolError(ctx context.Context, lease Lease, command SessionCommand, code string) (SessionCommandResult, error) {
	if !validLease(lease) || !validUUID(command.MessageID) || !isDecisionCommand(command.Type) || command.Digest == ([sha256.Size]byte{}) || !validDecisionProtocolErrorCode(code) {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: code})
}

// ApplySourceDecision records the controller command envelope ID as the
// durable decision identity. targetMessageID is validated at this boundary but
// intentionally is not persisted: the WSS engine checks it against its bounded
// outstanding map, and the canonical command digest binds that check to this
// transaction.
func (s *Store) ApplySourceDecision(ctx context.Context, lease Lease, command SessionCommand, subscriptionID string, generation uint64, targetMessageID string, accepted bool, code string) (SessionCommandResult, error) {
	wantType := CommandRejectSource
	if accepted {
		wantType = CommandAckSource
	}
	if command.Type != wantType || !validUUID(subscriptionID) || generation == 0 || generation > math.MaxInt64 || !validUUID(targetMessageID) || (accepted && code != "") || (!accepted && !validCode(code)) {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	topologyLocks := newTopologyLockSet()
	topologyLocks.addSubscription(subscriptionID)
	if err = acquireTopologyLocks(ctx, tx, topologyLocks); err != nil {
		return SessionCommandResult{}, err
	}
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	var storedController string
	var currentGeneration int64
	var existingDecision sql.NullString
	var retiredGeneration sql.NullInt64
	err = tx.QueryRow(ctx, `SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation FROM relay_desired_states d JOIN relay_subscriptions s ON s.subscription_id=d.subscription_id WHERE d.subscription_id=$1 FOR UPDATE OF d,s`, subscriptionID).Scan(&storedController, &currentGeneration, &existingDecision, &retiredGeneration)
	if isNoRows(err) {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "unknown_target"})
	}
	if err != nil {
		return SessionCommandResult{}, err
	}
	if storedController != lease.ControllerID {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "unknown_target"})
	}
	if retiredGeneration.Valid {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "stale_target"})
	}
	if uint64(currentGeneration) != generation {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "stale_target"})
	}
	if existingDecision.Valid {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "unknown_target"})
	}
	state := "rejected"
	var decisionCode any = code
	if accepted {
		state = "acked"
		decisionCode = nil
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_desired_states SET decision=$3,decision_code=$4,decision_message_id=$5,decided_at=$6 WHERE subscription_id=$1 AND generation=$2 AND decision IS NULL`, subscriptionID, generation, state, decisionCode, command.MessageID, s.now().UTC())
	if err != nil {
		return SessionCommandResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return SessionCommandResult{}, ErrConflict
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultDecisionApplied})
}

// ApplyAccessDecision is the access-event counterpart to ApplySourceDecision.
// Row locking makes distinct decision command IDs converge on one terminal
// event state; the command ledger makes exact retransmission idempotent.
func (s *Store) ApplyAccessDecision(ctx context.Context, lease Lease, command SessionCommand, eventID, targetMessageID string, accepted bool, code string) (SessionCommandResult, error) {
	wantType := CommandRejectAccess
	if accepted {
		wantType = CommandAckAccess
	}
	if command.Type != wantType || !validUUID(eventID) || !validUUID(targetMessageID) || (accepted && code != "") || (!accepted && !validCode(code)) {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	var storedController string
	var existingDecision sql.NullString
	err = tx.QueryRow(ctx, `SELECT controller_id::text,decision FROM relay_access_events WHERE event_id=$1 FOR UPDATE`, eventID).Scan(&storedController, &existingDecision)
	if isNoRows(err) {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "unknown_target"})
	}
	if err != nil {
		return SessionCommandResult{}, err
	}
	if storedController != lease.ControllerID {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "unknown_target"})
	}
	if existingDecision.Valid {
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultProtocolError, ErrorCode: "unknown_target"})
	}
	state := "rejected"
	var decisionCode any = code
	if accepted {
		state = "acked"
		decisionCode = nil
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_access_events SET decision=$3,decision_code=$4,decision_message_id=$5,decided_at=$6 WHERE event_id=$1 AND controller_id=$2 AND decision IS NULL`, eventID, lease.ControllerID, state, decisionCode, command.MessageID, s.now().UTC())
	if err != nil {
		return SessionCommandResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return SessionCommandResult{}, ErrConflict
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultDecisionApplied})
}

func (s *Store) ApplyBindingRemoval(ctx context.Context, lease Lease, command SessionCommand, installationID, repositoryID int64) (SessionCommandResult, error) {
	if command.Type != CommandBindingRemove || installationID <= 0 || repositoryID <= 0 {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	topologyLocks := newTopologyLockSet()
	topologyLocks.addBinding(installationID)
	routeLocks, err := queryRouteTopologyKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND installation_id=$2 AND repository_id=$3 AND retired_generation IS NULL`, lease.ControllerID, installationID, repositoryID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	topologyLocks.addRoutes(routeLocks)
	if err = acquireTopologyLocks(ctx, tx, topologyLocks); err != nil {
		return SessionCommandResult{}, err
	}
	currentRoutes, err := queryRouteTopologyKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND installation_id=$2 AND repository_id=$3 AND retired_generation IS NULL`, lease.ControllerID, installationID, repositoryID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if !topologyRoutesCovered(topologyLocks, currentRoutes) {
		return SessionCommandResult{}, ErrConflict
	}
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_bindings SET revoked_at=COALESCE(revoked_at,$4) WHERE controller_id=$1 AND installation_id=$2 AND repository_id=$3`, lease.ControllerID, installationID, repositoryID, s.now().UTC())
	if err != nil {
		return SessionCommandResult{}, err
	}
	if tag.RowsAffected() == 0 {
		return SessionCommandResult{}, ErrNotFound
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultBindingRemoved, InstallationID: installationID, RepositoryID: repositoryID})
}

// ApplyKeyRevocation is terminal when it targets the authenticating key. The
// command result is committed in the same transaction as revocation so the WSS
// engine can send exactly that terminal response and then close. No later
// session can authenticate with the revoked key to replay it.
func (s *Store) ApplyKeyRevocation(ctx context.Context, lease Lease, command SessionCommand, controllerID, keyID string) (SessionCommandResult, error) {
	if command.Type != CommandKeyRevoke || controllerID != lease.ControllerID || !validUUID(keyID) {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	now := s.now().UTC()
	if prepared.keyID != keyID {
		var targetState string
		var rotationOldKey sql.NullString
		err = tx.QueryRow(ctx, `SELECT state,rotation_old_key_id::text FROM relay_controller_keys WHERE controller_id=$1 AND key_id=$2 FOR UPDATE`, controllerID, keyID).Scan(&targetState, &rotationOldKey)
		if isNoRows(err) {
			return SessionCommandResult{}, ErrNotFound
		}
		if err != nil {
			return SessionCommandResult{}, err
		}
		if targetState != "pending" || !rotationOldKey.Valid || rotationOldKey.String != prepared.keyID {
			return SessionCommandResult{}, ErrConflict
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=$3 WHERE controller_id=$1 AND key_id=$2 AND state='pending'`, controllerID, keyID, now)
		if updateErr != nil {
			return SessionCommandResult{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return SessionCommandResult{}, ErrConflict
		}
		return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultKeyRevoked, ControllerID: controllerID, KeyID: keyID})
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=COALESCE(revoked_at,$3) WHERE controller_id=$1 AND key_id=$2 AND state='active'`, controllerID, keyID, now)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return SessionCommandResult{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=COALESCE(revoked_at,$3) WHERE controller_id=$1 AND rotation_old_key_id=$2 AND state='pending'`, controllerID, keyID, now); err != nil {
		return SessionCommandResult{}, err
	}
	sessionTag, err := tx.Exec(ctx, `UPDATE relay_sessions SET revoked_at=COALESCE(revoked_at,$3) WHERE controller_id=$1 AND key_id=$2`, controllerID, keyID, now)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if sessionTag.RowsAffected() < 1 {
		return SessionCommandResult{}, ErrConflict
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultKeyRevoked, ControllerID: controllerID, KeyID: keyID})
}

// ApplyControllerRevocation uses the established topology shard ->
// controller/session lock hierarchy. Like self-key revocation, it returns a
// transactionally durable terminal result for the current connection only.
func (s *Store) ApplyControllerRevocation(ctx context.Context, lease Lease, command SessionCommand, controllerID string) (SessionCommandResult, error) {
	if command.Type != CommandControllerRevoke || controllerID != lease.ControllerID {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	rows, err := tx.Query(ctx, `SELECT DISTINCT installation_id FROM relay_bindings WHERE controller_id=$1 AND revoked_at IS NULL ORDER BY installation_id`, controllerID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	topologyLocks := newTopologyLockSet()
	for rows.Next() {
		var installationID int64
		if err = rows.Scan(&installationID); err != nil {
			rows.Close()
			return SessionCommandResult{}, err
		}
		topologyLocks.addBinding(installationID)
	}
	rows.Close()
	if rows.Err() != nil {
		return SessionCommandResult{}, rows.Err()
	}
	routeLocks, err := queryRouteTopologyKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND retired_generation IS NULL`, controllerID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	topologyLocks.addRoutes(routeLocks)
	if err = acquireTopologyLocks(ctx, tx, topologyLocks); err != nil {
		return SessionCommandResult{}, err
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT installation_id FROM relay_bindings WHERE controller_id=$1 AND revoked_at IS NULL`, controllerID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	for rows.Next() {
		var installationID int64
		if err = rows.Scan(&installationID); err != nil {
			rows.Close()
			return SessionCommandResult{}, err
		}
		if _, ok := topologyLocks.bindings[installationID]; !ok {
			rows.Close()
			return SessionCommandResult{}, ErrConflict
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return SessionCommandResult{}, rows.Err()
	}
	currentRoutes, err := queryRouteTopologyKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND retired_generation IS NULL`, controllerID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if !topologyRoutesCovered(topologyLocks, currentRoutes) {
		return SessionCommandResult{}, ErrConflict
	}
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_controllers SET state='revoked',revoked_at=COALESCE(revoked_at,$2) WHERE controller_id=$1 AND state='active'`, controllerID, now)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return SessionCommandResult{}, ErrConflict
	}
	for _, query := range []string{`UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=COALESCE(revoked_at,$2) WHERE controller_id=$1`, `UPDATE relay_bindings SET revoked_at=COALESCE(revoked_at,$2) WHERE controller_id=$1`, `UPDATE relay_sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE controller_id=$1`} {
		if _, err = tx.Exec(ctx, query, controllerID, now); err != nil {
			return SessionCommandResult{}, err
		}
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultControllerRevoked, ControllerID: controllerID})
}

// ApplyRotationProposal looks up controller-global replay before obtaining
// entropy. An exact retransmission therefore remains available during an
// entropy outage and returns the original durable nonce and expiry.
func (s *Store) ApplyRotationProposal(ctx context.Context, lease Lease, command SessionCommand, input RotationInput, lifetime time.Duration) (SessionCommandResult, error) {
	if command.Type != CommandRotationPropose || input.ControllerID != lease.ControllerID || input.SessionID != lease.SessionID || !validRotationCommandInput(input) || lifetime < time.Second || lifetime > 5*time.Minute {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	if prepared.keyID != input.OldKeyID {
		return SessionCommandResult{}, ErrConflict
	}
	nonce := make([]byte, protocol.NonceBytes)
	if err = s.randomBytes(nonce); err != nil {
		clear(nonce)
		return SessionCommandResult{}, err
	}
	now := s.now().UTC()
	expires := now.Add(lifetime)
	_, err = tx.Exec(ctx, `INSERT INTO relay_controller_keys(key_id,controller_id,public_key,state,rotation_id,rotation_old_key_id,rotation_session_id,rotation_nonce,rotation_expires_at,created_at) VALUES($1,$2,$3,'pending',$4,$5,$6,$7,$8,$9)`, input.NewKeyID, input.ControllerID, input.NewPublicKey, input.RotationID, input.OldKeyID, input.SessionID, nonce, expires, now)
	if err != nil {
		clear(nonce)
		return SessionCommandResult{}, conflictError(err)
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultRotationChallenge, RotationID: input.RotationID, Nonce: nonce, ExpiresAt: expires})
}

// ApplyRotationConfirmation verifies new-key possession under the same row
// lock that records the confirmation and the controller command result.
func (s *Store) ApplyRotationConfirmation(ctx context.Context, lease Lease, command SessionCommand, rotationID, signature string) (SessionCommandResult, error) {
	if command.Type != CommandRotationConfirm || !validUUID(rotationID) {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	var oldKeyID, newKeyID, sessionID string
	var publicKey, nonce []byte
	var expires time.Time
	var confirmed sql.NullTime
	err = tx.QueryRow(ctx, `SELECT rotation_old_key_id::text,key_id::text,rotation_session_id::text,public_key,rotation_nonce,rotation_expires_at,possession_confirmed_at FROM relay_controller_keys WHERE controller_id=$1 AND rotation_id=$2 AND state='pending' FOR UPDATE`, lease.ControllerID, rotationID).Scan(&oldKeyID, &newKeyID, &sessionID, &publicKey, &nonce, &expires, &confirmed)
	if isNoRows(err) {
		return SessionCommandResult{}, ErrNotFound
	}
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer clear(publicKey)
	defer clear(nonce)
	if oldKeyID != prepared.keyID || sessionID != lease.SessionID || len(publicKey) != protocol.PublicKeyBytes || len(nonce) != protocol.NonceBytes || !expires.After(s.now().UTC()) {
		return SessionCommandResult{}, ErrConflict
	}
	transcript, err := protocol.KeyRotationTranscript(protocol.RotationProof{RotationID: rotationID, ControllerID: lease.ControllerID, OldKeyID: oldKeyID, NewKeyID: newKeyID, NewPublicKey: base64.RawURLEncoding.EncodeToString(publicKey), SessionID: sessionID, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: expires})
	if err != nil || !protocol.Verify(ed25519.PublicKey(publicKey), transcript, signature) {
		return SessionCommandResult{}, ErrConflict
	}
	if !confirmed.Valid {
		tag, updateErr := tx.Exec(ctx, `UPDATE relay_controller_keys SET possession_confirmed_at=$3 WHERE controller_id=$1 AND rotation_id=$2 AND state='pending' AND possession_confirmed_at IS NULL`, lease.ControllerID, rotationID, s.now().UTC())
		if updateErr != nil {
			return SessionCommandResult{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return SessionCommandResult{}, ErrConflict
		}
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultRotationConfirmed, RotationID: rotationID})
}

// ApplyRotationFinalization revokes the current session and activates the
// confirmed new key in the same transaction as its terminal result. An exact
// replay can later be authenticated by the new key: prepareSessionCommand sees
// the controller-global ledger result before old-key-specific domain checks.
func (s *Store) ApplyRotationFinalization(ctx context.Context, lease Lease, command SessionCommand, rotationID string) (SessionCommandResult, error) {
	if command.Type != CommandRotationFinalize || !validUUID(rotationID) {
		return SessionCommandResult{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionCommandResult{}, err
	}
	defer rollback(ctx, tx)
	prepared, err := s.prepareSessionCommand(ctx, tx, lease, command)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if prepared.replay {
		return s.commitSessionCommandReplay(ctx, tx, lease, prepared.result)
	}
	var oldKeyID, newKeyID, sessionID, oldState string
	var expires time.Time
	var confirmed bool
	err = tx.QueryRow(ctx, `SELECT p.rotation_old_key_id::text,p.key_id::text,p.rotation_session_id::text,p.rotation_expires_at,(p.possession_confirmed_at IS NOT NULL),o.state FROM relay_controller_keys p JOIN relay_controller_keys o ON o.controller_id=p.controller_id AND o.key_id=p.rotation_old_key_id WHERE p.controller_id=$1 AND p.rotation_id=$2 AND p.state='pending' FOR UPDATE OF p,o`, lease.ControllerID, rotationID).Scan(&oldKeyID, &newKeyID, &sessionID, &expires, &confirmed, &oldState)
	if isNoRows(err) {
		return SessionCommandResult{}, ErrNotFound
	}
	if err != nil {
		return SessionCommandResult{}, err
	}
	if oldKeyID != prepared.keyID || sessionID != lease.SessionID || !expires.After(s.now().UTC()) || !confirmed || oldState != "active" {
		return SessionCommandResult{}, ErrConflict
	}
	now := s.now().UTC()
	oldTag, err := tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_id=$3,revoked_at=$4 WHERE controller_id=$1 AND key_id=$2 AND state='active'`, lease.ControllerID, oldKeyID, rotationID, now)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if oldTag.RowsAffected() != 1 {
		return SessionCommandResult{}, ErrConflict
	}
	newTag, err := tx.Exec(ctx, `UPDATE relay_controller_keys SET state='active',rotation_id=NULL,rotation_old_key_id=NULL,rotation_session_id=NULL,rotation_nonce=NULL,rotation_expires_at=NULL WHERE controller_id=$1 AND key_id=$2 AND state='pending'`, lease.ControllerID, newKeyID)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if newTag.RowsAffected() != 1 {
		return SessionCommandResult{}, ErrConflict
	}
	sessionTag, err := tx.Exec(ctx, `UPDATE relay_sessions SET revoked_at=COALESCE(revoked_at,$3) WHERE controller_id=$1 AND key_id=$2`, lease.ControllerID, oldKeyID, now)
	if err != nil {
		return SessionCommandResult{}, err
	}
	if sessionTag.RowsAffected() < 1 {
		return SessionCommandResult{}, ErrConflict
	}
	return s.commitSessionCommand(ctx, tx, lease, command, SessionCommandResult{Kind: ResultRotationFinalized, RotationID: rotationID, RetiredKeyID: oldKeyID})
}

func validRotationCommandInput(input RotationInput) bool {
	return validUUID(input.RotationID) && validUUID(input.ControllerID) && validUUID(input.OldKeyID) && validUUID(input.NewKeyID) && validUUID(input.SessionID) && input.OldKeyID != input.NewKeyID && len(input.NewPublicKey) == protocol.PublicKeyBytes
}

func sessionCommandLockKey(controllerID, messageID string) int64 {
	digest := sha256.Sum256([]byte("rig.relay.command.v1\x00" + controllerID + "\x00" + messageID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func controllerSessionLockKey(controllerID string) int64 {
	digest := sha256.Sum256([]byte("rig.relay.controller-session.v1\x00" + controllerID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func acquireControllerSessionLock(ctx context.Context, tx pgx.Tx, controllerID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, controllerSessionLockKey(controllerID))
	return err
}

func validSessionCommandType(value SessionCommandType) bool {
	switch value {
	case CommandSubscriptionsSync, CommandAckSource, CommandRejectSource, CommandAckAccess, CommandRejectAccess, CommandBindingRemove, CommandControllerRevoke, CommandKeyRevoke, CommandRotationPropose, CommandRotationConfirm, CommandRotationFinalize:
		return true
	default:
		return false
	}
}

func validateSessionCommandResult(commandType SessionCommandType, result SessionCommandResult) error {
	if len(result.Nonce) != 0 && len(result.Nonce) != protocol.NonceBytes {
		return ErrInvalid
	}
	switch result.Kind {
	case ResultSubscriptionsSynced:
		if commandType != CommandSubscriptionsSync || result.Generation == 0 || result.Generation > math.MaxInt64 || result.Count > protocol.MaxArrayItems || hasOtherResultFields(result, "generation", "count") {
			return ErrInvalid
		}
	case ResultDecisionApplied:
		if !isDecisionCommand(commandType) || hasOtherResultFields(result) {
			return ErrInvalid
		}
	case ResultProtocolError:
		if !isDecisionCommand(commandType) || (result.ErrorCode != "stale_target" && result.ErrorCode != "unknown_target" && result.ErrorCode != "target_mismatch") || hasOtherResultFields(result, "error") {
			return ErrInvalid
		}
	case ResultBindingRemoved:
		if commandType != CommandBindingRemove || result.InstallationID <= 0 || result.RepositoryID <= 0 || hasOtherResultFields(result, "installation", "repository") {
			return ErrInvalid
		}
	case ResultControllerRevoked:
		if commandType != CommandControllerRevoke || !validUUID(result.ControllerID) || hasOtherResultFields(result, "controller") {
			return ErrInvalid
		}
	case ResultKeyRevoked:
		if commandType != CommandKeyRevoke || !validUUID(result.ControllerID) || !validUUID(result.KeyID) || hasOtherResultFields(result, "controller", "key") {
			return ErrInvalid
		}
	case ResultRotationChallenge:
		if commandType != CommandRotationPropose || !validUUID(result.RotationID) || len(result.Nonce) != protocol.NonceBytes || result.ExpiresAt.IsZero() || hasOtherResultFields(result, "rotation", "nonce", "expires") {
			return ErrInvalid
		}
	case ResultRotationConfirmed:
		if commandType != CommandRotationConfirm || !validUUID(result.RotationID) || hasOtherResultFields(result, "rotation") {
			return ErrInvalid
		}
	case ResultRotationFinalized:
		if commandType != CommandRotationFinalize || !validUUID(result.RotationID) || !validUUID(result.RetiredKeyID) || hasOtherResultFields(result, "rotation", "retired") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func isDecisionCommand(value SessionCommandType) bool {
	return value == CommandAckSource || value == CommandRejectSource || value == CommandAckAccess || value == CommandRejectAccess
}

func validDecisionProtocolErrorCode(code string) bool {
	return code == "unknown_target" || code == "target_mismatch" || code == "stale_target"
}

func hasOtherResultFields(result SessionCommandResult, allowed ...string) bool {
	set := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		set[field] = true
	}
	return (!set["error"] && result.ErrorCode != "") || (!set["generation"] && result.Generation != 0) || (!set["count"] && result.Count != 0) || (!set["installation"] && result.InstallationID != 0) || (!set["repository"] && result.RepositoryID != 0) || (!set["controller"] && result.ControllerID != "") || (!set["key"] && result.KeyID != "") || (!set["rotation"] && result.RotationID != "") || (!set["retired"] && result.RetiredKeyID != "") || (!set["nonce"] && len(result.Nonce) != 0) || (!set["expires"] && !result.ExpiresAt.IsZero())
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func optionalUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}
func optionalCount(kind SessionCommandResultKind, value uint32) any {
	if kind != ResultSubscriptionsSynced {
		return nil
	}
	return value
}
func optionalPositive(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
func optionalUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func optionalBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
func optionalTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
