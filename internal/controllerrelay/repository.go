package controllerrelay

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("controller relay record not found")
	ErrConflict = errors.New("controller relay record conflicts with existing state")
	ErrInvalid  = errors.New("invalid controller relay record")
	ErrState    = errors.New("invalid controller relay state transition")
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) CreateIdentity(ctx context.Context, identity ControllerIdentity, key ControllerKey) error {
	if !validIdentityForCreate(identity) || !validKeyForCreate(key) || identity.ControllerID != key.ControllerID || key.State != KeyActive {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO relay_controllers(singleton,controller_id,state,last_error_code,created_at,updated_at,revoked_at) VALUES(1,?,?,?,?,?,?)`, identity.ControllerID, identity.State, nullable(identity.LastErrorCode), timestamp(identity.CreatedAt), timestamp(identity.UpdatedAt), nullableTime(identity.RevokedAt))
	if err != nil {
		return classifyConstraint(err)
	}
	if err = insertKey(ctx, tx, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ActiveIdentity(ctx context.Context) (ControllerIdentity, ControllerKey, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, err
	}
	defer tx.Rollback()
	identity, err := scanIdentity(tx.QueryRowContext(ctx, identitySelect+` WHERE singleton=1 AND state='active'`))
	if errors.Is(err, sql.ErrNoRows) {
		return ControllerIdentity{}, ControllerKey{}, ErrNotFound
	}
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, err
	}
	key, err := scanKey(tx.QueryRowContext(ctx, keySelect+` WHERE controller_id=? AND state='active'`, identity.ControllerID))
	if errors.Is(err, sql.ErrNoRows) {
		return ControllerIdentity{}, ControllerKey{}, ErrNotFound
	}
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return ControllerIdentity{}, ControllerKey{}, err
	}
	return identity, key, nil
}

func (r *Repository) Controller(ctx context.Context, controllerID string) (ControllerIdentity, error) {
	if !canonicalUUID(controllerID) {
		return ControllerIdentity{}, ErrInvalid
	}
	value, err := scanIdentity(r.db.QueryRowContext(ctx, identitySelect+` WHERE controller_id=?`, controllerID))
	if errors.Is(err, sql.ErrNoRows) {
		return ControllerIdentity{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) CASControllerState(ctx context.Context, controllerID, expected, next, errorCode string, changedAt time.Time) error {
	if !canonicalUUID(controllerID) || expected != ControllerActive || next != ControllerRevoked || !validErrorCode(errorCode) || changedAt.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_controllers SET state='revoked',last_error_code=?,updated_at=?,revoked_at=? WHERE controller_id=? AND state='active'`, nullable(errorCode), timestamp(changedAt), timestamp(changedAt), controllerID)
	return casResult(ctx, r.db, result, err, `SELECT COUNT(*) FROM relay_controllers WHERE controller_id=?`, controllerID)
}

func (r *Repository) CreateKey(ctx context.Context, key ControllerKey) error {
	if !validKeyForCreate(key) {
		return ErrInvalid
	}
	return insertKey(ctx, r.db, key)
}

func insertKey(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, key ControllerKey) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at,revoked_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, key.KeyID, key.ControllerID, append([]byte(nil), key.PublicKey...), key.Algorithm, key.State, key.ProtectedKeyRef, timestamp(key.CreatedAt), timestamp(key.UpdatedAt), nullableTime(key.ActivatedAt), nullableTime(key.PossessionConfirmedAt), nullableTime(key.RevokedAt))
	return classifyConstraint(err)
}

func (r *Repository) Key(ctx context.Context, controllerID, keyID string) (ControllerKey, error) {
	if !canonicalUUID(controllerID) || !canonicalUUID(keyID) {
		return ControllerKey{}, ErrInvalid
	}
	value, err := scanKey(r.db.QueryRowContext(ctx, keySelect+` WHERE controller_id=? AND key_id=?`, controllerID, keyID))
	if errors.Is(err, sql.ErrNoRows) {
		return ControllerKey{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) Keys(ctx context.Context, controllerID string) ([]ControllerKey, error) {
	if !canonicalUUID(controllerID) {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, keySelect+` WHERE controller_id=? ORDER BY created_at DESC,key_id DESC`, controllerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ControllerKey
	for rows.Next() {
		value, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) CASKeyState(ctx context.Context, controllerID, keyID, expected, next string, changedAt time.Time) error {
	if !canonicalUUID(controllerID) || !canonicalUUID(keyID) || changedAt.IsZero() || !allowedKeyTransition(expected, next) {
		return ErrInvalid
	}
	activated, confirmed, revoked := any(nil), any(nil), any(nil)
	if next == KeyActive {
		activated, confirmed = timestamp(changedAt), timestamp(changedAt)
	}
	if next == KeyRevoked {
		revoked = timestamp(changedAt)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_controller_keys SET state=?,updated_at=?,activated_at=COALESCE(activated_at,?),possession_confirmed_at=COALESCE(possession_confirmed_at,?),revoked_at=? WHERE controller_id=? AND key_id=? AND state=?`, next, timestamp(changedAt), activated, confirmed, revoked, controllerID, keyID, expected)
	return casResult(ctx, r.db, result, classifyConstraint(err), `SELECT COUNT(*) FROM relay_controller_keys WHERE controller_id=? AND key_id=?`, controllerID, keyID)
}

func (r *Repository) CreateRotation(ctx context.Context, rotation KeyRotation) error {
	if !validRotationForCreate(rotation) {
		return ErrInvalid
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,last_error_code,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, rotation.RotationID, rotation.ControllerID, rotation.OldKeyID, rotation.NewKeyID, rotation.State, timestamp(rotation.ExpiresAt), timestamp(rotation.StateChangedAt), nil, timestamp(rotation.CreatedAt), timestamp(rotation.UpdatedAt), nil)
	return classifyConstraint(err)
}

func (r *Repository) Rotation(ctx context.Context, controllerID, rotationID string) (KeyRotation, error) {
	if !canonicalUUID(controllerID) || !canonicalUUID(rotationID) {
		return KeyRotation{}, ErrInvalid
	}
	value, err := scanRotation(r.db.QueryRowContext(ctx, rotationSelect+` WHERE controller_id=? AND rotation_id=?`, controllerID, rotationID))
	if errors.Is(err, sql.ErrNoRows) {
		return KeyRotation{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) Rotations(ctx context.Context, controllerID string) ([]KeyRotation, error) {
	if !canonicalUUID(controllerID) {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, rotationSelect+` WHERE controller_id=? ORDER BY created_at DESC,rotation_id DESC`, controllerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []KeyRotation
	for rows.Next() {
		value, err := scanRotation(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) CASRotationState(ctx context.Context, controllerID, rotationID, expected, next, errorCode string, changedAt time.Time) error {
	if !canonicalUUID(controllerID) || !canonicalUUID(rotationID) || changedAt.IsZero() || !allowedRotationTransition(expected, next) || !validTransitionCode(next, errorCode) {
		return ErrInvalid
	}
	completed := any(nil)
	if next == RotationCompleted || next == RotationFailed {
		completed = timestamp(changedAt)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_key_rotations SET state=?,state_changed_at=?,updated_at=?,completed_at=?,last_error_code=? WHERE controller_id=? AND rotation_id=? AND state=?`, next, timestamp(changedAt), timestamp(changedAt), completed, nullable(errorCode), controllerID, rotationID, expected)
	return casResult(ctx, r.db, result, classifyConstraint(err), `SELECT COUNT(*) FROM relay_key_rotations WHERE controller_id=? AND rotation_id=?`, controllerID, rotationID)
}

func (r *Repository) CreateEnrollment(ctx context.Context, enrollment Enrollment) error {
	if !validEnrollmentForCreate(enrollment) {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO relay_enrollments(enrollment_id,owner_user_id,connection_id,controller_id,key_id,binding_id,installation_id,repository_id,purpose,protected_poll_ref,state,created_at,expires_at,state_changed_at,updated_at,last_polled_at,completed_at,poll_ref_cleared_at,last_error_code) SELECT ?,?,?,?,?,NULL,?,?,?,?,?,?,?,?,?,NULL,NULL,NULL,NULL FROM source_connections WHERE id=? AND owner_user_id=? AND status='connected'`, enrollment.EnrollmentID, enrollment.OwnerUserID, enrollment.ConnectionID, enrollment.ControllerID, enrollment.KeyID, enrollment.InstallationID, enrollment.RepositoryID, enrollment.Purpose, enrollment.ProtectedPollRef, enrollment.State, timestamp(enrollment.CreatedAt), timestamp(enrollment.ExpiresAt), timestamp(enrollment.StateChangedAt), timestamp(enrollment.UpdatedAt), enrollment.ConnectionID, enrollment.OwnerUserID)
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

func (r *Repository) Enrollment(ctx context.Context, owner, enrollmentID string) (Enrollment, error) {
	if !validOpaqueID(owner) || !canonicalUUID(enrollmentID) {
		return Enrollment{}, ErrInvalid
	}
	value, err := scanEnrollment(r.db.QueryRowContext(ctx, enrollmentSelect+` WHERE owner_user_id=? AND enrollment_id=?`, owner, enrollmentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) MarkEnrollmentPolled(ctx context.Context, owner, enrollmentID string, at time.Time) error {
	if !validOpaqueID(owner) || !canonicalUUID(enrollmentID) || at.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_enrollments SET last_polled_at=?,updated_at=? WHERE owner_user_id=? AND enrollment_id=? AND state='pending' AND protected_poll_ref IS NOT NULL`, timestamp(at), timestamp(at), owner, enrollmentID)
	return casResult(ctx, r.db, result, err, `SELECT COUNT(*) FROM relay_enrollments WHERE owner_user_id=? AND enrollment_id=?`, owner, enrollmentID)
}

func (r *Repository) CompleteEnrollment(ctx context.Context, owner, enrollmentID, terminalState, bindingID, errorCode string, changedAt time.Time) (Enrollment, error) {
	if !validOpaqueID(owner) || !canonicalUUID(enrollmentID) || changedAt.IsZero() || !validEnrollmentCompletion(terminalState, bindingID, errorCode) {
		return Enrollment{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback()
	current, err := scanEnrollment(tx.QueryRowContext(ctx, enrollmentSelect+` WHERE owner_user_id=? AND enrollment_id=?`, owner, enrollmentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrNotFound
	}
	if err != nil {
		return Enrollment{}, err
	}
	if current.State != EnrollmentPending {
		if current.State == terminalState && current.BindingID == bindingID && current.LastErrorCode == errorCode {
			return current, nil
		}
		return Enrollment{}, ErrState
	}
	if terminalState == EnrollmentExpired && changedAt.Before(current.ExpiresAt) {
		return Enrollment{}, ErrInvalid
	}
	if terminalState == EnrollmentAuthorized {
		_, err = tx.ExecContext(ctx, `INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,last_error_code,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,'authorized',?,NULL,?,?,NULL)`, bindingID, current.OwnerUserID, current.ConnectionID, current.ControllerID, current.InstallationID, current.RepositoryID, timestamp(changedAt), timestamp(changedAt), timestamp(changedAt))
		if err != nil {
			return Enrollment{}, classifyConstraint(err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE relay_enrollments SET state=?,binding_id=?,state_changed_at=?,updated_at=?,completed_at=?,last_error_code=? WHERE owner_user_id=? AND enrollment_id=? AND state='pending'`, terminalState, nullable(bindingID), timestamp(changedAt), timestamp(changedAt), timestamp(changedAt), nullable(errorCode), owner, enrollmentID)
	if err != nil {
		return Enrollment{}, classifyConstraint(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return Enrollment{}, err
		}
		return Enrollment{}, ErrState
	}
	completed, err := scanEnrollment(tx.QueryRowContext(ctx, enrollmentSelect+` WHERE owner_user_id=? AND enrollment_id=?`, owner, enrollmentID))
	if err != nil {
		return Enrollment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return completed, nil
}

func (r *Repository) ClearEnrollmentPollRef(ctx context.Context, owner, enrollmentID string, clearedAt time.Time) error {
	if !validOpaqueID(owner) || !canonicalUUID(enrollmentID) || clearedAt.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_enrollments SET protected_poll_ref=NULL,poll_ref_cleared_at=?,updated_at=? WHERE owner_user_id=? AND enrollment_id=? AND state<>'pending' AND protected_poll_ref IS NOT NULL`, timestamp(clearedAt), timestamp(clearedAt), owner, enrollmentID)
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
	var state string
	var ref sql.NullString
	var cleared sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT state,protected_poll_ref,poll_ref_cleared_at FROM relay_enrollments WHERE owner_user_id=? AND enrollment_id=?`, owner, enrollmentID).Scan(&state, &ref, &cleared); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if state != EnrollmentPending && !ref.Valid && cleared.Valid {
		return nil
	}
	return ErrState
}

// RecoverEnrollments atomically expires due pending rows, then returns a
// bounded list whose protected poll files must be deleted before their refs
// can be cleared. It never clears a ref before the external deletion succeeds.
func (r *Repository) RecoverEnrollments(ctx context.Context, now time.Time, limit int) ([]Enrollment, error) {
	if now.IsZero() {
		return nil, ErrInvalid
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stamp := timestamp(now)
	if _, err := tx.ExecContext(ctx, `UPDATE relay_enrollments SET state='expired',state_changed_at=?,updated_at=?,completed_at=? WHERE state='pending' AND expires_at<=?`, stamp, stamp, stamp, stamp); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, enrollmentSelect+` WHERE state<>'pending' AND protected_poll_ref IS NOT NULL ORDER BY updated_at,enrollment_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var values []Enrollment
	for rows.Next() {
		value, err := scanEnrollment(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func (r *Repository) Binding(ctx context.Context, owner, bindingID string) (InstallationBinding, error) {
	if !validOpaqueID(owner) || !canonicalUUID(bindingID) {
		return InstallationBinding{}, ErrInvalid
	}
	value, err := scanBinding(r.db.QueryRowContext(ctx, bindingSelect+` WHERE owner_user_id=? AND binding_id=?`, owner, bindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return InstallationBinding{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) Bindings(ctx context.Context, owner string) ([]InstallationBinding, error) {
	if !validOpaqueID(owner) {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, bindingSelect+` WHERE owner_user_id=? ORDER BY updated_at DESC,binding_id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []InstallationBinding
	for rows.Next() {
		value, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) MarkBindingRemovalPending(ctx context.Context, owner, bindingID string, changedAt time.Time) error {
	return r.transitionBinding(ctx, owner, bindingID, []string{BindingAuthorized, BindingAccessLost}, BindingRemovalPending, "", changedAt)
}

func (r *Repository) MarkBindingRemoved(ctx context.Context, owner, bindingID string, changedAt time.Time) error {
	return r.transitionBinding(ctx, owner, bindingID, []string{BindingRemovalPending}, BindingRemoved, "", changedAt)
}

func (r *Repository) MarkBindingAccessLost(ctx context.Context, owner, bindingID, errorCode string, changedAt time.Time) error {
	return r.transitionBinding(ctx, owner, bindingID, []string{BindingAuthorized, BindingRemovalPending}, BindingAccessLost, errorCode, changedAt)
}

func (r *Repository) RestoreBindingAccess(ctx context.Context, owner, bindingID string, changedAt time.Time) error {
	return r.transitionBinding(ctx, owner, bindingID, []string{BindingAccessLost}, BindingAuthorized, "", changedAt)
}

func (r *Repository) transitionBinding(ctx context.Context, owner, bindingID string, expected []string, next, errorCode string, changedAt time.Time) error {
	if !validOpaqueID(owner) || !canonicalUUID(bindingID) || changedAt.IsZero() || !validTransitionCode(next, errorCode) {
		return ErrInvalid
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(expected)), ",")
	args := []any{next, timestamp(changedAt), timestamp(changedAt), nullable(errorCode)}
	completed := any(nil)
	if next == BindingRemoved || next == BindingFailed {
		completed = timestamp(changedAt)
	}
	args = append(args, completed, owner, bindingID)
	for _, state := range expected {
		args = append(args, state)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_installation_bindings SET state=?,state_changed_at=?,updated_at=?,last_error_code=?,completed_at=? WHERE owner_user_id=? AND binding_id=? AND state IN (`+placeholders+`)`, args...)
	return casResult(ctx, r.db, result, classifyConstraint(err), `SELECT COUNT(*) FROM relay_installation_bindings WHERE owner_user_id=? AND binding_id=?`, owner, bindingID)
}

const identitySelect = `SELECT controller_id,state,COALESCE(last_error_code,''),created_at,updated_at,revoked_at FROM relay_controllers`
const keySelect = `SELECT key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at,revoked_at FROM relay_controller_keys`
const rotationSelect = `SELECT rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,COALESCE(last_error_code,''),created_at,updated_at,completed_at FROM relay_key_rotations`
const bindingSelect = `SELECT binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,COALESCE(last_error_code,''),created_at,updated_at,completed_at FROM relay_installation_bindings`
const enrollmentSelect = `SELECT enrollment_id,owner_user_id,connection_id,controller_id,key_id,COALESCE(binding_id,''),installation_id,repository_id,purpose,COALESCE(protected_poll_ref,''),state,created_at,expires_at,state_changed_at,updated_at,last_polled_at,completed_at,poll_ref_cleared_at,COALESCE(last_error_code,'') FROM relay_enrollments`

type scanner interface{ Scan(...any) error }

func scanIdentity(row scanner) (ControllerIdentity, error) {
	var value ControllerIdentity
	var created, updated string
	var revoked sql.NullString
	err := row.Scan(&value.ControllerID, &value.State, &value.LastErrorCode, &created, &updated, &revoked)
	if err != nil {
		return value, err
	}
	if value.CreatedAt, err = parseTimestamp(created); err != nil {
		return value, err
	}
	if value.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return value, err
	}
	value.RevokedAt, err = parseNullableTimestamp(revoked)
	return value, err
}

func scanKey(row scanner) (ControllerKey, error) {
	var value ControllerKey
	var created, updated string
	var activated, confirmed, revoked sql.NullString
	err := row.Scan(&value.KeyID, &value.ControllerID, &value.PublicKey, &value.Algorithm, &value.State, &value.ProtectedKeyRef, &created, &updated, &activated, &confirmed, &revoked)
	if err != nil {
		return value, err
	}
	value.PublicKey = append([]byte(nil), value.PublicKey...)
	if value.CreatedAt, err = parseTimestamp(created); err != nil {
		return value, err
	}
	if value.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return value, err
	}
	if value.ActivatedAt, err = parseNullableTimestamp(activated); err != nil {
		return value, err
	}
	if value.PossessionConfirmedAt, err = parseNullableTimestamp(confirmed); err != nil {
		return value, err
	}
	value.RevokedAt, err = parseNullableTimestamp(revoked)
	return value, err
}

func scanRotation(row scanner) (KeyRotation, error) {
	var value KeyRotation
	var expires, changed, created, updated string
	var completed sql.NullString
	err := row.Scan(&value.RotationID, &value.ControllerID, &value.OldKeyID, &value.NewKeyID, &value.State, &expires, &changed, &value.LastErrorCode, &created, &updated, &completed)
	if err != nil {
		return value, err
	}
	for raw, target := range map[string]*time.Time{expires: &value.ExpiresAt, changed: &value.StateChangedAt, created: &value.CreatedAt, updated: &value.UpdatedAt} {
		if *target, err = parseTimestamp(raw); err != nil {
			return value, err
		}
	}
	value.CompletedAt, err = parseNullableTimestamp(completed)
	return value, err
}

func scanBinding(row scanner) (InstallationBinding, error) {
	var value InstallationBinding
	var changed, created, updated string
	var completed sql.NullString
	err := row.Scan(&value.BindingID, &value.OwnerUserID, &value.ConnectionID, &value.ControllerID, &value.InstallationID, &value.RepositoryID, &value.State, &changed, &value.LastErrorCode, &created, &updated, &completed)
	if err != nil {
		return value, err
	}
	for raw, target := range map[string]*time.Time{changed: &value.StateChangedAt, created: &value.CreatedAt, updated: &value.UpdatedAt} {
		if *target, err = parseTimestamp(raw); err != nil {
			return value, err
		}
	}
	value.CompletedAt, err = parseNullableTimestamp(completed)
	return value, err
}

func scanEnrollment(row scanner) (Enrollment, error) {
	var value Enrollment
	var created, expires, changed, updated string
	var polled, completed, cleared sql.NullString
	err := row.Scan(&value.EnrollmentID, &value.OwnerUserID, &value.ConnectionID, &value.ControllerID, &value.KeyID, &value.BindingID, &value.InstallationID, &value.RepositoryID, &value.Purpose, &value.ProtectedPollRef, &value.State, &created, &expires, &changed, &updated, &polled, &completed, &cleared, &value.LastErrorCode)
	if err != nil {
		return value, err
	}
	for raw, target := range map[string]*time.Time{created: &value.CreatedAt, expires: &value.ExpiresAt, changed: &value.StateChangedAt, updated: &value.UpdatedAt} {
		if *target, err = parseTimestamp(raw); err != nil {
			return value, err
		}
	}
	if value.LastPolledAt, err = parseNullableTimestamp(polled); err != nil {
		return value, err
	}
	if value.CompletedAt, err = parseNullableTimestamp(completed); err != nil {
		return value, err
	}
	value.PollRefClearedAt, err = parseNullableTimestamp(cleared)
	return value, err
}

func validIdentityForCreate(value ControllerIdentity) bool {
	return canonicalUUID(value.ControllerID) && value.State == ControllerActive && value.LastErrorCode == "" && !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero() && value.RevokedAt == nil
}

func validKeyForCreate(value ControllerKey) bool {
	if !canonicalUUID(value.KeyID) || !canonicalUUID(value.ControllerID) || len(value.PublicKey) != ed25519.PublicKeySize || value.Algorithm != KeyAlgorithmEd25519 || value.ProtectedKeyRef != ProtectedKeyRef(value.ControllerID, value.KeyID) || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return false
	}
	switch value.State {
	case KeyPending:
		return value.ActivatedAt == nil && value.PossessionConfirmedAt == nil && value.RevokedAt == nil
	case KeyActive:
		return value.ActivatedAt != nil && value.PossessionConfirmedAt != nil && value.RevokedAt == nil
	case KeyRevoked:
		return value.RevokedAt != nil
	default:
		return false
	}
}

func validRotationForCreate(value KeyRotation) bool {
	return canonicalUUID(value.RotationID) && canonicalUUID(value.ControllerID) && canonicalUUID(value.OldKeyID) && canonicalUUID(value.NewKeyID) && value.OldKeyID != value.NewKeyID && value.State == RotationPrepare && !value.ExpiresAt.IsZero() && !value.StateChangedAt.IsZero() && value.LastErrorCode == "" && !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero() && value.CompletedAt == nil && value.ExpiresAt.After(value.CreatedAt)
}

func validEnrollmentForCreate(value Enrollment) bool {
	return canonicalUUID(value.EnrollmentID) && validOpaqueID(value.OwnerUserID) && validOpaqueID(value.ConnectionID) && canonicalUUID(value.ControllerID) && canonicalUUID(value.KeyID) && value.BindingID == "" && value.InstallationID > 0 && value.RepositoryID > 0 && value.Purpose == EnrollmentPurpose && value.ProtectedPollRef == ProtectedEnrollmentPollRef(value.ControllerID, value.EnrollmentID) && value.State == EnrollmentPending && !value.CreatedAt.IsZero() && value.ExpiresAt.After(value.CreatedAt) && !value.StateChangedAt.IsZero() && !value.UpdatedAt.IsZero() && value.LastPolledAt == nil && value.CompletedAt == nil && value.PollRefClearedAt == nil && value.LastErrorCode == ""
}

func validEnrollmentCompletion(state, bindingID, errorCode string) bool {
	switch state {
	case EnrollmentAuthorized:
		return canonicalUUID(bindingID) && errorCode == ""
	case EnrollmentDenied, EnrollmentExpired:
		return bindingID == "" && errorCode == ""
	case EnrollmentFailed:
		return bindingID == "" && validNonemptyErrorCode(errorCode)
	default:
		return false
	}
}

func allowedKeyTransition(current, next string) bool {
	return current == KeyPending && (next == KeyActive || next == KeyRevoked) || current == KeyActive && next == KeyRevoked
}

func allowedRotationTransition(current, next string) bool {
	if next == RotationFailed {
		return current == RotationPrepare || current == RotationPropose || current == RotationConfirm || current == RotationNewKeyAuth || current == RotationFinalize
	}
	return current == RotationPrepare && next == RotationPropose || current == RotationPropose && next == RotationConfirm || current == RotationConfirm && next == RotationNewKeyAuth || current == RotationNewKeyAuth && next == RotationFinalize || current == RotationFinalize && next == RotationCompleted
}

func validTransitionCode(next, code string) bool {
	if next == RotationFailed || next == BindingFailed || next == BindingAccessLost {
		return validNonemptyErrorCode(code)
	}
	return code == ""
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validOpaqueID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func validErrorCode(value string) bool { return value == "" || validNonemptyErrorCode(value) }

func validNonemptyErrorCode(value string) bool {
	switch value {
	case ErrorAuthorizationDenied, ErrorAuthorizationExpired, ErrorEnrollmentFailed,
		ErrorKeyRevoked, ErrorProtocol, ErrorProviderUnavailable, ErrorRelayUnavailable,
		ErrorRemovalFailed, ErrorRotationFailed, ErrorSourceAccessLost:
		return true
	default:
		return false
	}
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse controller relay timestamp: %w", err)
	}
	return parsed, nil
}

func parseNullableTimestamp(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTimestamp(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timestamp(*value)
}

func classifyConstraint(err error) error {
	if err == nil || errors.Is(err, ErrConflict) {
		return err
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "constraint failed") || strings.Contains(lower, "unique constraint") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func casResult(ctx context.Context, db *sql.DB, result sql.Result, err error, existenceQuery string, args ...any) error {
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
	var exists int
	if err := db.QueryRowContext(ctx, existenceQuery, args...).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrState
}
