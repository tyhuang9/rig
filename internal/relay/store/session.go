package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

type ChallengeInput struct {
	SessionID    string
	ControllerID string
	KeyID        string
	ClientNonce  []byte
	ServerNonce  []byte
	ACKDigest    []byte
	ExpiresAt    time.Time
}

// AuthenticationChallenge contains the durable values required to verify an
// authenticate frame after a process restart. Destroy clears caller-owned key
// and transcript material.
type AuthenticationChallenge struct {
	ChallengeInput
	PublicKey []byte
	CreatedAt time.Time
}

func (c *AuthenticationChallenge) Destroy() {
	if c != nil {
		clear(c.ClientNonce)
		clear(c.ServerNonce)
		clear(c.ACKDigest)
		clear(c.PublicKey)
	}
}

func (s *Store) CreateChallenge(ctx context.Context, input ChallengeInput) error {
	now := s.now().UTC()
	if !validUUID(input.SessionID) || !validUUID(input.ControllerID) || !validUUID(input.KeyID) || len(input.ClientNonce) != 32 || len(input.ServerNonce) != 32 || len(input.ACKDigest) != 32 || !input.ExpiresAt.After(now) || input.ExpiresAt.Sub(now) > 5*time.Minute {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO relay_wss_challenges(session_id,controller_id,key_id,client_nonce,server_nonce,ack_digest,created_at,expires_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8 FROM relay_controllers c JOIN relay_controller_keys k ON k.controller_id=c.controller_id AND k.key_id=$3 WHERE c.controller_id=$2 AND c.state='active' AND k.state='active'`, input.SessionID, input.ControllerID, input.KeyID, input.ClientNonce, input.ServerNonce, input.ACKDigest, now, input.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("create challenge: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) LoadChallengeForAuthentication(ctx context.Context, sessionID string) (AuthenticationChallenge, error) {
	if !validUUID(sessionID) {
		return AuthenticationChallenge{}, ErrInvalid
	}
	var out AuthenticationChallenge
	out.SessionID = sessionID
	err := s.pool.QueryRow(ctx, `SELECT ch.controller_id::text,ch.key_id::text,ch.client_nonce,ch.server_nonce,ch.ack_digest,ch.created_at,ch.expires_at,k.public_key FROM relay_wss_challenges ch JOIN relay_controllers c ON c.controller_id=ch.controller_id JOIN relay_controller_keys k ON k.controller_id=ch.controller_id AND k.key_id=ch.key_id WHERE ch.session_id=$1 AND ch.consumed_at IS NULL AND c.state='active' AND k.state='active'`, sessionID).Scan(&out.ControllerID, &out.KeyID, &out.ClientNonce, &out.ServerNonce, &out.ACKDigest, &out.CreatedAt, &out.ExpiresAt, &out.PublicKey)
	if isNoRows(err) {
		return AuthenticationChallenge{}, ErrNotFound
	}
	if err != nil {
		out.Destroy()
		return AuthenticationChallenge{}, err
	}
	if !out.ExpiresAt.After(s.now().UTC()) {
		out.Destroy()
		return AuthenticationChallenge{}, ErrExpired
	}
	if len(out.ClientNonce) != 32 || len(out.ServerNonce) != 32 || len(out.ACKDigest) != 32 || len(out.PublicKey) != 32 {
		out.Destroy()
		return AuthenticationChallenge{}, ErrConflict
	}
	return out, nil
}

// ConsumeChallenge is the authorization linearization point. Challenge
// creation may race revocation because it grants no session authority; this
// transaction locks and rechecks the challenge, controller, and key together,
// so a challenge created before revocation cannot create a session afterward.
func (s *Store) ConsumeChallenge(ctx context.Context, sessionID string, sessionExpiresAt time.Time) error {
	if !validUUID(sessionID) {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var controllerID, keyID, state, keyState string
	var challengeExpires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx, `SELECT ch.controller_id::text,ch.key_id::text,ch.expires_at,ch.consumed_at,c.state,k.state FROM relay_wss_challenges ch JOIN relay_controllers c ON c.controller_id=ch.controller_id JOIN relay_controller_keys k ON k.controller_id=ch.controller_id AND k.key_id=ch.key_id WHERE ch.session_id=$1 FOR UPDATE OF ch,c,k`, sessionID).Scan(&controllerID, &keyID, &challengeExpires, &consumed, &state, &keyState)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if consumed != nil {
		return ErrReplay
	}
	if !challengeExpires.After(now) || state != "active" || keyState != "active" {
		return ErrExpired
	}
	if !sessionExpiresAt.After(now) || sessionExpiresAt.Sub(now) > 24*time.Hour {
		return ErrInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_wss_challenges SET consumed_at=$2 WHERE session_id=$1 AND consumed_at IS NULL`, sessionID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_sessions(session_id,controller_id,key_id,created_at,expires_at,last_seen_at) VALUES($1,$2,$3,$4,$5,$4)`, sessionID, controllerID, keyID, now, sessionExpiresAt.UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type Lease struct {
	ControllerID string
	SessionID    string
	LeaseID      string
	Fence        uint64
	ExpiresAt    time.Time
}

// RenewLease extends exactly the caller's fenced lease and rechecks the
// session, controller, and authenticating key. A superseded lease can never be
// renewed, even when an old connection wakes after its replacement is ready.
func (s *Store) RenewLease(ctx context.Context, lease Lease, duration time.Duration) (Lease, error) {
	if !validLease(lease) || duration < time.Second || duration > 10*time.Minute {
		return Lease{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	if err = acquireControllerSessionLock(ctx, tx, lease.ControllerID); err != nil {
		return Lease{}, err
	}
	var leaseExpires time.Time
	err = tx.QueryRow(ctx, `SELECT expires_at FROM relay_controller_leases WHERE controller_id=$1 AND session_id=$2 AND lease_id=$3 AND fence=$4 FOR UPDATE`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).Scan(&leaseExpires)
	if isNoRows(err) {
		return Lease{}, ErrConflict
	}
	if err != nil {
		return Lease{}, err
	}
	var sessionExpires time.Time
	var sessionRevoked sql.NullTime
	var controllerState, keyState string
	err = tx.QueryRow(ctx, `SELECT s.expires_at,s.revoked_at,c.state,k.state FROM relay_sessions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_controller_keys k ON k.controller_id=s.controller_id AND k.key_id=s.key_id WHERE s.controller_id=$1 AND s.session_id=$2 FOR UPDATE OF s,c,k`, lease.ControllerID, lease.SessionID).Scan(&sessionExpires, &sessionRevoked, &controllerState, &keyState)
	if isNoRows(err) {
		return Lease{}, ErrConflict
	}
	if err != nil {
		return Lease{}, err
	}
	if !leaseExpires.After(now) || !sessionExpires.After(now) || sessionRevoked.Valid || controllerState != "active" || keyState != "active" {
		return Lease{}, ErrConflict
	}
	expires := now.Add(duration)
	if expires.After(sessionExpires) {
		expires = sessionExpires
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_controller_leases SET expires_at=$5,updated_at=$6 WHERE controller_id=$1 AND session_id=$2 AND lease_id=$3 AND fence=$4 AND expires_at=$7`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence, expires, now, leaseExpires)
	if err != nil {
		return Lease{}, err
	}
	if tag.RowsAffected() != 1 {
		return Lease{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return Lease{}, err
	}
	lease.ExpiresAt = expires
	return lease, nil
}

// ValidateLease is a non-mutating liveness check used by a session supervisor.
// Every state-changing WSS command performs the same check again under lock.
func (s *Store) ValidateLease(ctx context.Context, lease Lease) error {
	if !validLease(lease) {
		return ErrInvalid
	}
	var leaseExpires, sessionExpires time.Time
	var sessionRevoked sql.NullTime
	var controllerState, keyState string
	err := s.pool.QueryRow(ctx, `SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state FROM relay_controller_leases l JOIN relay_sessions s ON s.session_id=l.session_id AND s.controller_id=l.controller_id JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_controller_keys k ON k.controller_id=s.controller_id AND k.key_id=s.key_id WHERE l.controller_id=$1 AND l.session_id=$2 AND l.lease_id=$3 AND l.fence=$4`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).Scan(&leaseExpires, &sessionExpires, &sessionRevoked, &controllerState, &keyState)
	if isNoRows(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if !leaseExpires.After(now) || !sessionExpires.After(now) || sessionRevoked.Valid || controllerState != "active" || keyState != "active" {
		return ErrConflict
	}
	return nil
}

func (s *Store) AcquireLease(ctx context.Context, sessionID string, duration time.Duration) (Lease, error) {
	if !validUUID(sessionID) || duration < time.Second || duration > 10*time.Minute {
		return Lease{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var controllerID string
	err = tx.QueryRow(ctx, `SELECT controller_id::text FROM relay_sessions WHERE session_id=$1`, sessionID).Scan(&controllerID)
	if isNoRows(err) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	if err = acquireControllerSessionLock(ctx, tx, controllerID); err != nil {
		return Lease{}, err
	}
	var oldSession string
	var oldFence int64
	var oldExpiry time.Time
	var oldSessionExpiry time.Time
	var oldSessionRevoked sql.NullTime
	err = tx.QueryRow(ctx, `SELECT l.session_id::text,l.fence,l.expires_at,s.expires_at,s.revoked_at FROM relay_controller_leases l JOIN relay_sessions s ON s.controller_id=l.controller_id AND s.session_id=l.session_id WHERE l.controller_id=$1 FOR UPDATE OF l,s`, controllerID).Scan(&oldSession, &oldFence, &oldExpiry, &oldSessionExpiry, &oldSessionRevoked)
	if err != nil && !isNoRows(err) {
		return Lease{}, err
	}
	var lockedControllerID, controllerState, keyState string
	var sessionExpires time.Time
	var sessionRevoked sql.NullTime
	err = tx.QueryRow(ctx, `SELECT s.controller_id::text,s.expires_at,s.revoked_at,c.state,k.state FROM relay_sessions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_controller_keys k ON k.controller_id=s.controller_id AND k.key_id=s.key_id WHERE s.session_id=$1 FOR UPDATE OF s,c,k`, sessionID).Scan(&lockedControllerID, &sessionExpires, &sessionRevoked, &controllerState, &keyState)
	if isNoRows(err) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	if lockedControllerID != controllerID {
		return Lease{}, ErrConflict
	}
	if sessionRevoked.Valid || !sessionExpires.After(now) || controllerState != "active" || keyState != "active" {
		return Lease{}, ErrExpired
	}
	if oldSession != sessionID && oldExpiry.After(now) && oldSessionExpiry.After(now) && !oldSessionRevoked.Valid {
		return Lease{}, ErrConflict
	}
	if oldFence < 0 || oldFence == math.MaxInt64 {
		return Lease{}, ErrConflict
	}
	expires := now.Add(duration)
	if expires.After(sessionExpires) {
		expires = sessionExpires
	}
	fence := oldFence + 1
	leaseID := s.newUUID().String()
	_, err = tx.Exec(ctx, `INSERT INTO relay_controller_leases(controller_id,session_id,lease_id,fence,expires_at,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(controller_id) DO UPDATE SET session_id=EXCLUDED.session_id,lease_id=EXCLUDED.lease_id,fence=EXCLUDED.fence,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at`, controllerID, sessionID, leaseID, fence, expires, now)
	if err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Lease{}, err
	}
	return Lease{ControllerID: controllerID, SessionID: sessionID, LeaseID: leaseID, Fence: uint64(fence), ExpiresAt: expires}, nil
}
func (s *Store) ReleaseLease(ctx context.Context, lease Lease) error {
	if !validUUID(lease.ControllerID) || !validUUID(lease.LeaseID) || lease.Fence == 0 {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM relay_controller_leases WHERE controller_id=$1 AND lease_id=$2 AND fence=$3`, lease.ControllerID, lease.LeaseID, lease.Fence)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func validLease(lease Lease) bool {
	return validUUID(lease.ControllerID) && validUUID(lease.SessionID) && validUUID(lease.LeaseID) && lease.Fence > 0
}
