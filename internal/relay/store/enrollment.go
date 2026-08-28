package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type EnrollmentInput struct {
	EnrollmentID   string
	ControllerID   string
	KeyID          string
	PublicKey      []byte
	InstallationID int64
	RepositoryID   int64
	StateHash      []byte
	PollHash       []byte
	PKCECiphertext []byte
	PKCESealNonce  []byte
	RequestNonce   []byte
	ExpiresAt      time.Time
}
type EnrollmentClaim struct {
	EnrollmentID   string
	ControllerID   string
	KeyID          string
	PublicKey      []byte
	InstallationID int64
	RepositoryID   int64
	PKCECiphertext []byte
	PKCESealNonce  []byte
	RequestNonce   []byte
	ExpiresAt      time.Time
}

func (c *EnrollmentClaim) Destroy() {
	if c == nil {
		return
	}
	clear(c.PublicKey)
	clear(c.PKCECiphertext)
	clear(c.PKCESealNonce)
	clear(c.RequestNonce)
}

type EnrollmentStatus struct {
	Status      string
	FailureCode string
	CompletedAt *time.Time
}

func (s *Store) CreateEnrollment(ctx context.Context, input EnrollmentInput) (string, error) {
	now := s.now().UTC()
	if input.EnrollmentID != "" && !validUUID(input.EnrollmentID) {
		return "", fmt.Errorf("%w: enrollment ID", ErrInvalid)
	}
	if !validUUID(input.ControllerID) || !validUUID(input.KeyID) || len(input.PublicKey) != 32 || input.InstallationID <= 0 || input.RepositoryID <= 0 || !validateHash(input.StateHash) || !validateHash(input.PollHash) || len(input.PKCECiphertext) < 29 || len(input.PKCECiphertext) > 4096 || len(input.PKCESealNonce) != 12 || len(input.RequestNonce) != 32 || !input.ExpiresAt.After(now) || input.ExpiresAt.Sub(now) > 30*time.Minute {
		return "", fmt.Errorf("%w: enrollment", ErrInvalid)
	}
	id := input.EnrollmentID
	if id == "" {
		id = s.newUUID().String()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, enrollmentCapacityLock); err != nil {
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	// Expiration, replay classification, capacity check, and insertion share
	// one authoritative transaction. Replay deliberately precedes capacity so
	// load cannot change the externally stable classification of a signed replay.
	if _, err = tx.Exec(ctx, `UPDATE relay_enrollments SET status='expired',completed_at=$1,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE status IN ('pending','state_claimed') AND expires_at<=$1`, now); err != nil {
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	var replay bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_enrollments WHERE controller_id=$1 AND key_id=$2 AND request_nonce=$3)`, input.ControllerID, input.KeyID, input.RequestNonce).Scan(&replay); err != nil {
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	if replay {
		return "", ErrReplay
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM relay_enrollments WHERE status IN ('pending','state_claimed') AND expires_at>$1`, now).Scan(&active); err != nil {
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	if active >= MaximumActiveEnrollments {
		return "", ErrCapacity
	}
	_, err = tx.Exec(ctx, `INSERT INTO relay_enrollments(enrollment_id,controller_id,key_id,public_key,installation_id,repository_id,state_hash,poll_hash,pkce_ciphertext,pkce_seal_nonce,request_nonce,status,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending',$12,$13)`, id, input.ControllerID, input.KeyID, input.PublicKey, input.InstallationID, input.RepositoryID, input.StateHash, input.PollHash, input.PKCECiphertext, input.PKCESealNonce, input.RequestNonce, now, input.ExpiresAt.UTC())
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" && databaseError.ConstraintName == "relay_enrollment_request_replay" {
			return "", ErrReplay
		}
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("create enrollment: %w", err)
	}
	return id, nil
}

// ClaimEnrollmentState is callback-only. It atomically claims OAuth state and
// returns encrypted PKCE material; it does not authorize any controller.
func (s *Store) ClaimEnrollmentState(ctx context.Context, stateHash []byte) (EnrollmentClaim, error) {
	if !validateHash(stateHash) {
		return EnrollmentClaim{}, fmt.Errorf("%w: state hash", ErrInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EnrollmentClaim{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var out EnrollmentClaim
	var status string
	err = tx.QueryRow(ctx, `SELECT enrollment_id,controller_id,key_id,public_key,installation_id,repository_id,pkce_ciphertext,pkce_seal_nonce,request_nonce,expires_at,status FROM relay_enrollments WHERE state_hash=$1 FOR UPDATE`, stateHash).Scan(&out.EnrollmentID, &out.ControllerID, &out.KeyID, &out.PublicKey, &out.InstallationID, &out.RepositoryID, &out.PKCECiphertext, &out.PKCESealNonce, &out.RequestNonce, &out.ExpiresAt, &status)
	if isNoRows(err) {
		out.Destroy()
		return EnrollmentClaim{}, ErrNotFound
	}
	if err != nil {
		out.Destroy()
		return EnrollmentClaim{}, err
	}
	if status != "pending" {
		out.Destroy()
		return EnrollmentClaim{}, ErrReplay
	}
	if !out.ExpiresAt.After(now) {
		if _, err = tx.Exec(ctx, `UPDATE relay_enrollments SET status='expired',state_claimed_at=$2,completed_at=$2,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE enrollment_id=$1`, out.EnrollmentID, now); err != nil {
			out.Destroy()
			return EnrollmentClaim{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			out.Destroy()
			return EnrollmentClaim{}, err
		}
		out.Destroy()
		return EnrollmentClaim{}, ErrExpired
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_enrollments SET status='state_claimed',state_claimed_at=$2 WHERE enrollment_id=$1 AND status='pending'`, out.EnrollmentID, now)
	if err != nil {
		out.Destroy()
		return EnrollmentClaim{}, err
	}
	if tag.RowsAffected() != 1 {
		out.Destroy()
		return EnrollmentClaim{}, ErrReplay
	}
	if err = tx.Commit(ctx); err != nil {
		out.Destroy()
		return EnrollmentClaim{}, err
	}
	return out, nil
}

// CompleteEnrollment is called only after the service has exchanged the code
// and live-verified the GitHub user, installation, and repository.
func (s *Store) CompleteEnrollment(ctx context.Context, enrollmentID string) error {
	if !validUUID(enrollmentID) {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var controllerID, keyID string
	var publicKey []byte
	var installationID, repositoryID int64
	var expires time.Time
	var status string
	err = tx.QueryRow(ctx, `SELECT controller_id,key_id,public_key,installation_id,repository_id,expires_at,status FROM relay_enrollments WHERE enrollment_id=$1`, enrollmentID).Scan(&controllerID, &keyID, &publicKey, &installationID, &repositoryID, &expires, &status)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer clear(publicKey)
	if status != "state_claimed" {
		return ErrConflict
	}
	if !expires.After(now) {
		tag, updateErr := tx.Exec(ctx, `UPDATE relay_enrollments SET status='expired',completed_at=$2,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE enrollment_id=$1 AND status='state_claimed' AND expires_at<=$2`, enrollmentID, now)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return ErrExpired
	}
	topologyLocks := newTopologyLockSet()
	topologyLocks.addBinding(installationID)
	routeLocks, err := queryRouteTopologyKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND installation_id=$2 AND repository_id=$3 AND retired_generation IS NULL`, controllerID, installationID, repositoryID)
	if err != nil {
		return err
	}
	topologyLocks.addRoutes(routeLocks)
	if err = acquireTopologyLocks(ctx, tx, topologyLocks); err != nil {
		return err
	}
	currentRoutes, err := queryRouteTopologyKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND installation_id=$2 AND repository_id=$3 AND retired_generation IS NULL`, controllerID, installationID, repositoryID)
	if err != nil {
		return err
	}
	if !topologyRoutesCovered(topologyLocks, currentRoutes) {
		return ErrConflict
	}
	var lockedControllerID, lockedKeyID, lockedStatus string
	var lockedPublicKey []byte
	var lockedInstallationID, lockedRepositoryID int64
	var lockedExpires time.Time
	err = tx.QueryRow(ctx, `SELECT controller_id,key_id,public_key,installation_id,repository_id,expires_at,status FROM relay_enrollments WHERE enrollment_id=$1 FOR UPDATE`, enrollmentID).Scan(&lockedControllerID, &lockedKeyID, &lockedPublicKey, &lockedInstallationID, &lockedRepositoryID, &lockedExpires, &lockedStatus)
	if isNoRows(err) {
		clear(lockedPublicKey)
		return ErrConflict
	}
	if err != nil {
		clear(lockedPublicKey)
		return err
	}
	defer clear(lockedPublicKey)
	if lockedControllerID != controllerID || lockedKeyID != keyID || !bytes.Equal(lockedPublicKey, publicKey) || lockedInstallationID != installationID || lockedRepositoryID != repositoryID || !lockedExpires.Equal(expires) || lockedStatus != "state_claimed" {
		return ErrConflict
	}
	if !lockedExpires.After(now) {
		if _, err = tx.Exec(ctx, `UPDATE relay_enrollments SET status='expired',completed_at=$2,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE enrollment_id=$1 AND status='state_claimed'`, enrollmentID, now); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return ErrExpired
	}
	var controllerState string
	err = tx.QueryRow(ctx, `SELECT state FROM relay_controllers WHERE controller_id=$1 FOR UPDATE`, controllerID).Scan(&controllerState)
	if isNoRows(err) {
		if _, err = tx.Exec(ctx, `INSERT INTO relay_controllers(controller_id,state,created_at) VALUES($1,'active',$2)`, controllerID, now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_controller_keys(key_id,controller_id,public_key,state,created_at,possession_confirmed_at) VALUES($1,$2,$3,'active',$4,$4)`, keyID, controllerID, publicKey, now); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if controllerState != "active" {
			return ErrConflict
		}
		var storedKey []byte
		var keyState string
		if err = tx.QueryRow(ctx, `SELECT public_key,state FROM relay_controller_keys WHERE controller_id=$1 AND key_id=$2 FOR UPDATE`, controllerID, keyID).Scan(&storedKey, &keyState); isNoRows(err) {
			return ErrConflict
		} else if err != nil {
			return err
		}
		if keyState != "active" || !bytes.Equal(storedKey, publicKey) {
			return ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_bindings(controller_id,installation_id,repository_id,created_at,revoked_at) VALUES($1,$2,$3,$4,NULL) ON CONFLICT(controller_id,installation_id,repository_id) DO UPDATE SET revoked_at=NULL`, controllerID, installationID, repositoryID, now); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_enrollments SET status='authorized',completed_at=$2,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE enrollment_id=$1 AND status='state_claimed'`, enrollmentID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) FailEnrollment(ctx context.Context, enrollmentID, code string) error {
	if !validUUID(enrollmentID) || !validCode(code) {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_enrollments SET status='failed',completed_at=$2,failure_code=$3,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE enrollment_id=$1 AND status='state_claimed'`, enrollmentID, s.now().UTC(), code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// PollEnrollment never returns verifier material. Terminal status remains
// idempotently readable until enrollment expiry; polled_at is audit-only.
func (s *Store) PollEnrollment(ctx context.Context, pollHash []byte) (EnrollmentStatus, error) {
	if !validateHash(pollHash) {
		return EnrollmentStatus{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EnrollmentStatus{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var out EnrollmentStatus
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT status,COALESCE(failure_code,''),completed_at,expires_at FROM relay_enrollments WHERE poll_hash=$1 FOR UPDATE`, pollHash).Scan(&out.Status, &out.FailureCode, &out.CompletedAt, &expires)
	if isNoRows(err) {
		return EnrollmentStatus{}, ErrNotFound
	}
	if err != nil {
		return EnrollmentStatus{}, err
	}
	if !expires.After(now) {
		if out.Status == "pending" || out.Status == "state_claimed" {
			if _, err = tx.Exec(ctx, `UPDATE relay_enrollments SET status='expired',completed_at=$2,pkce_ciphertext=NULL,pkce_seal_nonce=NULL,polled_at=$2 WHERE poll_hash=$1`, pollHash, now); err != nil {
				return EnrollmentStatus{}, err
			}
			out.Status = "expired"
			out.CompletedAt = &now
		} else {
			if _, err = tx.Exec(ctx, `UPDATE relay_enrollments SET polled_at=$2 WHERE poll_hash=$1`, pollHash, now); err != nil {
				return EnrollmentStatus{}, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return EnrollmentStatus{}, err
		}
		return out, ErrExpired
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_enrollments SET polled_at=$2 WHERE poll_hash=$1`, pollHash, now); err != nil {
		return EnrollmentStatus{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EnrollmentStatus{}, err
	}
	return out, nil
}

func (s *Store) ExpireEnrollments(ctx context.Context) (int64, error) {
	now := s.now().UTC()
	tag, err := s.pool.Exec(ctx, `UPDATE relay_enrollments SET status='expired',completed_at=$1,pkce_ciphertext=NULL,pkce_seal_nonce=NULL WHERE status IN ('pending','state_claimed') AND expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
