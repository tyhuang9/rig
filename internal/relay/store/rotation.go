package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"time"
)

type RotationInput struct {
	RotationID   string
	ControllerID string
	OldKeyID     string
	NewKeyID     string
	SessionID    string
	NewPublicKey []byte
}
type RotationChallenge struct {
	RotationID   string
	ControllerID string
	OldKeyID     string
	NewKeyID     string
	SessionID    string
	NewPublicKey []byte
	Nonce        []byte
	ExpiresAt    time.Time
}

func (c *RotationChallenge) Destroy() {
	if c != nil {
		clear(c.Nonce)
		clear(c.NewPublicKey)
	}
}
func (s *Store) ProposeRotation(ctx context.Context, input RotationInput, lifetime time.Duration) (RotationChallenge, error) {
	if !validUUID(input.RotationID) || !validUUID(input.ControllerID) || !validUUID(input.OldKeyID) || !validUUID(input.NewKeyID) || !validUUID(input.SessionID) || input.OldKeyID == input.NewKeyID || len(input.NewPublicKey) != 32 || lifetime < time.Second || lifetime > 5*time.Minute {
		return RotationChallenge{}, ErrInvalid
	}
	nonce := make([]byte, 32)
	if err := s.randomBytes(nonce); err != nil {
		return RotationChallenge{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		clear(nonce)
		return RotationChallenge{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var state string
	var sessionExpires time.Time
	var sessionRevoked sql.NullTime
	err = tx.QueryRow(ctx, `SELECT c.state,se.expires_at,se.revoked_at FROM relay_controllers c JOIN relay_controller_keys k ON k.controller_id=c.controller_id AND k.key_id=$2 AND k.state='active' JOIN relay_sessions se ON se.session_id=$3 AND se.controller_id=c.controller_id AND se.key_id=k.key_id WHERE c.controller_id=$1 FOR UPDATE OF c,k,se`, input.ControllerID, input.OldKeyID, input.SessionID).Scan(&state, &sessionExpires, &sessionRevoked)
	if isNoRows(err) {
		clear(nonce)
		return RotationChallenge{}, ErrNotFound
	}
	if err != nil {
		clear(nonce)
		return RotationChallenge{}, err
	}
	if state != "active" || sessionRevoked.Valid || !sessionExpires.After(now) {
		clear(nonce)
		return RotationChallenge{}, ErrConflict
	}
	expires := now.Add(lifetime)
	_, err = tx.Exec(ctx, `INSERT INTO relay_controller_keys(key_id,controller_id,public_key,state,rotation_id,rotation_old_key_id,rotation_session_id,rotation_nonce,rotation_expires_at,created_at) VALUES($1,$2,$3,'pending',$4,$5,$6,$7,$8,$9,$10)`, input.NewKeyID, input.ControllerID, input.NewPublicKey, input.RotationID, input.OldKeyID, input.SessionID, nonce, expires, now)
	if err != nil {
		clear(nonce)
		return RotationChallenge{}, conflictError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		clear(nonce)
		return RotationChallenge{}, err
	}
	return RotationChallenge{RotationID: input.RotationID, ControllerID: input.ControllerID, OldKeyID: input.OldKeyID, NewKeyID: input.NewKeyID, SessionID: input.SessionID, NewPublicKey: append([]byte(nil), input.NewPublicKey...), Nonce: nonce, ExpiresAt: expires}, nil
}
func (s *Store) ConfirmRotation(ctx context.Context, controllerID, rotationID string) error {
	if !validUUID(controllerID) || !validUUID(rotationID) {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var expires time.Time
	var confirmed *time.Time
	err = tx.QueryRow(ctx, `SELECT rotation_expires_at,possession_confirmed_at FROM relay_controller_keys WHERE controller_id=$1 AND rotation_id=$2 AND state='pending' FOR UPDATE`, controllerID, rotationID).Scan(&expires, &confirmed)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if confirmed != nil {
		return tx.Commit(ctx)
	}
	if !expires.After(now) {
		if _, updateErr := tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=$3 WHERE controller_id=$1 AND rotation_id=$2 AND state='pending'`, controllerID, rotationID, now); updateErr != nil {
			return updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return ErrExpired
	}
	_, err = tx.Exec(ctx, `UPDATE relay_controller_keys SET possession_confirmed_at=$3 WHERE controller_id=$1 AND rotation_id=$2 AND state='pending' AND possession_confirmed_at IS NULL`, controllerID, rotationID, now)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LoadRotationChallenge(ctx context.Context, controllerID, rotationID string) (RotationChallenge, error) {
	if !validUUID(controllerID) || !validUUID(rotationID) {
		return RotationChallenge{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RotationChallenge{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var out RotationChallenge
	out.RotationID = rotationID
	out.ControllerID = controllerID
	err = tx.QueryRow(ctx, `SELECT rotation_old_key_id::text,key_id::text,rotation_session_id::text,public_key,rotation_nonce,rotation_expires_at FROM relay_controller_keys WHERE controller_id=$1 AND rotation_id=$2 AND state='pending' FOR UPDATE`, controllerID, rotationID).Scan(&out.OldKeyID, &out.NewKeyID, &out.SessionID, &out.NewPublicKey, &out.Nonce, &out.ExpiresAt)
	if isNoRows(err) {
		return RotationChallenge{}, ErrNotFound
	}
	if err != nil {
		return RotationChallenge{}, err
	}
	if !out.ExpiresAt.After(now) {
		out.Destroy()
		if _, err = tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=$3 WHERE controller_id=$1 AND rotation_id=$2 AND state='pending'`, controllerID, rotationID, now); err != nil {
			return RotationChallenge{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return RotationChallenge{}, err
		}
		return RotationChallenge{}, ErrExpired
	}
	if err = tx.Commit(ctx); err != nil {
		clear(out.Nonce)
		return RotationChallenge{}, err
	}
	return out, nil
}
func (s *Store) ExpireRotations(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=$1 WHERE state='pending' AND rotation_expires_at<=$1`, s.now().UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
func (s *Store) FinalizeRotation(ctx context.Context, input RotationInput) error {
	if !validUUID(input.RotationID) || !validUUID(input.ControllerID) || !validUUID(input.OldKeyID) || !validUUID(input.NewKeyID) || !validUUID(input.SessionID) || input.OldKeyID == input.NewKeyID || len(input.NewPublicKey) != 32 {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var pendingKey, storedOldKey, storedSession, oldState string
	var pendingPublicKey []byte
	var confirmed bool
	var expires, sessionExpires time.Time
	var sessionRevoked sql.NullTime
	err = tx.QueryRow(ctx, `SELECT p.key_id::text,p.public_key,p.rotation_old_key_id::text,p.rotation_session_id::text,(p.possession_confirmed_at IS NOT NULL),p.rotation_expires_at,o.state,se.expires_at,se.revoked_at FROM relay_controller_keys p JOIN relay_controller_keys o ON o.controller_id=p.controller_id AND o.key_id=p.rotation_old_key_id JOIN relay_sessions se ON se.session_id=p.rotation_session_id AND se.controller_id=p.controller_id AND se.key_id=p.rotation_old_key_id WHERE p.controller_id=$1 AND p.rotation_id=$2 AND p.state='pending' FOR UPDATE OF p,o,se`, input.ControllerID, input.RotationID).Scan(&pendingKey, &pendingPublicKey, &storedOldKey, &storedSession, &confirmed, &expires, &oldState, &sessionExpires, &sessionRevoked)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer clear(pendingPublicKey)
	if !expires.After(now) {
		if _, updateErr := tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=$3 WHERE controller_id=$1 AND rotation_id=$2 AND state='pending'`, input.ControllerID, input.RotationID, now); updateErr != nil {
			return updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return ErrExpired
	}
	if pendingKey != input.NewKeyID || subtle.ConstantTimeCompare(pendingPublicKey, input.NewPublicKey) != 1 || storedOldKey != input.OldKeyID || storedSession != input.SessionID || !confirmed || oldState != "active" || sessionRevoked.Valid || !sessionExpires.After(now) {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_id=$3,revoked_at=$4 WHERE controller_id=$1 AND key_id=$2 AND state='active'`, input.ControllerID, input.OldKeyID, input.RotationID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_controller_keys SET state='active',rotation_id=NULL,rotation_old_key_id=NULL,rotation_session_id=NULL,rotation_nonce=NULL,rotation_expires_at=NULL WHERE controller_id=$1 AND key_id=$2 AND state='pending'`, input.ControllerID, input.NewKeyID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_sessions SET revoked_at=COALESCE(revoked_at,$3) WHERE controller_id=$1 AND key_id=$2`, input.ControllerID, input.OldKeyID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
