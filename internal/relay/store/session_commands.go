package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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

// prepareSessionCommand must be called only after the command's domain locks.
// Its advisory lock serializes a controller-global message ID even before a
// ledger row exists. The fenced lease is then locked and revalidated before a
// replay can be read or a mutation can proceed.
func (s *Store) prepareSessionCommand(ctx context.Context, tx pgx.Tx, lease Lease, command SessionCommand) (commandPreparation, error) {
	if !validLease(lease) || !validUUID(command.MessageID) || !validSessionCommandType(command.Type) || command.Digest == ([sha256.Size]byte{}) {
		return commandPreparation{}, ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, sessionCommandLockKey(lease.ControllerID, command.MessageID)); err != nil {
		return commandPreparation{}, err
	}
	now := s.now().UTC()
	var leaseExpires, sessionExpires time.Time
	var sessionRevoked sql.NullTime
	var controllerState, keyState, keyID string
	err := tx.QueryRow(ctx, `SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state,s.key_id::text FROM relay_controller_leases l JOIN relay_sessions s ON s.session_id=l.session_id AND s.controller_id=l.controller_id JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_controller_keys k ON k.controller_id=s.controller_id AND k.key_id=s.key_id WHERE l.controller_id=$1 AND l.session_id=$2 AND l.lease_id=$3 AND l.fence=$4 FOR UPDATE OF l,s,c,k`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).Scan(&leaseExpires, &sessionExpires, &sessionRevoked, &controllerState, &keyState, &keyID)
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

func sessionCommandLockKey(controllerID, messageID string) int64 {
	digest := sha256.Sum256([]byte("rig.relay.command.v1\x00" + controllerID + "\x00" + messageID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
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
