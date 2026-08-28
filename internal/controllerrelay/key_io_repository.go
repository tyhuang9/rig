package controllerrelay

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	ControllerKeyIOIdentityWrite  = "identity_write"
	ControllerKeyIOWrite          = "write"
	ControllerKeyIORevokedCleanup = "revoked_cleanup"
	ControllerKeyIOKeyCleanup     = "key_cleanup"
	ControllerKeyIOTempCleanup    = "temp_cleanup"

	ControllerKeyIOActive   = "active"
	ControllerKeyIORecovery = "recovery"
)

// ControllerKeyIOLease is the durable, fenced ownership record for every
// controller-key directory mutation. It intentionally contains public key
// metadata only; private material remains exclusively in the protected file.
type ControllerKeyIOLease struct {
	ScopeKey        string
	ControllerID    string
	LeaseID         string
	Operation       string
	Phase           string
	Fence           uint64
	LeaseExpiresAt  time.Time
	KeyID           string
	RotationID      string
	OldKeyID        string
	PublicKey       []byte
	ProtectedKeyRef string
	ArtifactName    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ControllerKeyIOLeasePage struct {
	Leases     []ControllerKeyIOLease
	Cleaned    int
	NextCursor string
	Complete   bool
}

const controllerKeyIOLeaseSelect = `SELECT scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,COALESCE(key_id,''),COALESCE(rotation_id,''),COALESCE(old_key_id,''),public_key,COALESCE(protected_key_ref,''),COALESCE(artifact_name,''),created_at,updated_at FROM relay_controller_key_io_leases`

func (r *Repository) BeginControllerIdentityWrite(ctx context.Context, lease ControllerKeyIOLease) error {
	if !validControllerKeyIOLease(lease) || lease.Operation != ControllerKeyIOIdentityWrite || lease.Phase != ControllerKeyIOActive || lease.Fence != 1 {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var identities int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_controllers WHERE singleton=1`).Scan(&identities); err != nil {
		return err
	}
	if identities != 0 {
		return ErrState
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,rotation_id,old_key_id,public_key,protected_key_ref,artifact_name,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,NULL,NULL,?,?,NULL,?,?)`, lease.ScopeKey, lease.ControllerID, lease.LeaseID, lease.Operation, lease.Phase, lease.Fence, timestamp(lease.LeaseExpiresAt), lease.KeyID, append([]byte(nil), lease.PublicKey...), lease.ProtectedKeyRef, timestamp(lease.CreatedAt), timestamp(lease.UpdatedAt))
	if err != nil {
		return classifyConstraint(err)
	}
	return tx.Commit()
}

func (r *Repository) BeginControllerKeyWrite(ctx context.Context, lease ControllerKeyIOLease) error {
	if !validControllerKeyIOLease(lease) || lease.Operation != ControllerKeyIOWrite || lease.Phase != ControllerKeyIOActive || lease.Fence != 1 {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var controllerState, oldKeyState string
	err = tx.QueryRowContext(ctx, `SELECT c.state,k.state FROM relay_controllers c JOIN relay_controller_keys k ON k.controller_id=c.controller_id AND k.key_id=? WHERE c.controller_id=?`, lease.OldKeyID, lease.ControllerID).Scan(&controllerState, &oldKeyState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if controllerState != ControllerActive || oldKeyState != KeyActive {
		return ErrState
	}
	var live int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_key_rotations WHERE controller_id=? AND state IN ('prepare','propose','confirm','new_key_auth','finalize')`, lease.ControllerID).Scan(&live); err != nil {
		return err
	}
	if live != 0 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,rotation_id,old_key_id,public_key,protected_key_ref,artifact_name,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`, lease.ScopeKey, lease.ControllerID, lease.LeaseID, lease.Operation, lease.Phase, lease.Fence, timestamp(lease.LeaseExpiresAt), lease.KeyID, lease.RotationID, lease.OldKeyID, append([]byte(nil), lease.PublicKey...), lease.ProtectedKeyRef, timestamp(lease.CreatedAt), timestamp(lease.UpdatedAt))
	if err != nil {
		return classifyConstraint(err)
	}
	return tx.Commit()
}

// MaterializePendingKeyAndRotation consumes the exact unexpired write lease in
// the same transaction that makes its public metadata authoritative.
func (r *Repository) MaterializePendingKeyAndRotation(ctx context.Context, lease ControllerKeyIOLease, key ControllerKey, rotation KeyRotation, at time.Time) error {
	if !validControllerKeyIOLease(lease) || lease.Operation != ControllerKeyIOWrite || lease.Phase != ControllerKeyIOActive || at.IsZero() || !validKeyForCreate(key) || key.State != KeyPending || !validRotationForCreate(rotation) ||
		key.ControllerID != lease.ControllerID || key.KeyID != lease.KeyID || key.ProtectedKeyRef != lease.ProtectedKeyRef || !bytes.Equal(key.PublicKey, lease.PublicKey) ||
		rotation.ControllerID != lease.ControllerID || rotation.RotationID != lease.RotationID || rotation.OldKeyID != lease.OldKeyID || rotation.NewKeyID != lease.KeyID {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	persisted, err := scanControllerKeyIOLease(tx.QueryRowContext(ctx, controllerKeyIOLeaseSelect+` WHERE scope_key=?`, lease.ScopeKey))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer clear(persisted.PublicKey)
	if !sameControllerKeyIOLeaseIdentity(persisted, lease) || persisted.Operation != ControllerKeyIOWrite || persisted.Phase != ControllerKeyIOActive || !persisted.LeaseExpiresAt.After(at) {
		return ErrState
	}
	var controllerState, oldKeyState string
	if err = tx.QueryRowContext(ctx, `SELECT c.state,k.state FROM relay_controllers c JOIN relay_controller_keys k ON k.controller_id=c.controller_id AND k.key_id=? WHERE c.controller_id=?`, rotation.OldKeyID, rotation.ControllerID).Scan(&controllerState, &oldKeyState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if controllerState != ControllerActive || oldKeyState != KeyActive {
		return ErrState
	}
	var live int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_key_rotations WHERE controller_id=? AND state IN ('prepare','propose','confirm','new_key_auth','finalize')`, lease.ControllerID).Scan(&live); err != nil {
		return err
	}
	if live != 0 {
		return ErrConflict
	}
	if err = insertKey(ctx, tx, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,last_error_code,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, rotation.RotationID, rotation.ControllerID, rotation.OldKeyID, rotation.NewKeyID, rotation.State, timestamp(rotation.ExpiresAt), timestamp(rotation.StateChangedAt), nil, timestamp(rotation.CreatedAt), timestamp(rotation.UpdatedAt), nil); err != nil {
		return classifyConstraint(err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM relay_controller_key_io_leases WHERE scope_key=? AND lease_id=? AND fence=? AND operation='write' AND phase='active'`, lease.ScopeKey, lease.LeaseID, lease.Fence)
	if err != nil {
		return err
	}
	if count, rowsErr := result.RowsAffected(); rowsErr != nil || count != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	return tx.Commit()
}

func (r *Repository) LiveKeyRotation(ctx context.Context, controllerID string) (KeyRotation, ControllerKey, error) {
	if !canonicalUUID(controllerID) {
		return KeyRotation{}, ControllerKey{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return KeyRotation{}, ControllerKey{}, err
	}
	defer tx.Rollback()
	rotation, err := liveRotationTx(ctx, tx, controllerID)
	if err != nil {
		return KeyRotation{}, ControllerKey{}, err
	}
	key, err := scanKey(tx.QueryRowContext(ctx, keySelect+` WHERE controller_id=? AND key_id=?`, controllerID, rotation.NewKeyID))
	if err != nil {
		return KeyRotation{}, ControllerKey{}, err
	}
	if err = tx.Commit(); err != nil {
		clear(key.PublicKey)
		return KeyRotation{}, ControllerKey{}, err
	}
	return rotation, key, nil
}

func (r *Repository) ExpiredControllerKeyIOLeases(ctx context.Context, cursor string, at time.Time, limit int) (ControllerKeyIOLeasePage, error) {
	if at.IsZero() || limit < 1 || limit > 1000 {
		return ControllerKeyIOLeasePage{}, ErrInvalid
	}
	cursorScopeKey, err := parseControllerKeyIOCursor(cursor)
	if err != nil {
		return ControllerKeyIOLeasePage{}, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, controllerKeyIOLeaseSelect+` WHERE lease_expires_at<=? AND (?='' OR scope_key>?) ORDER BY scope_key LIMIT ?`, timestamp(at), cursorScopeKey, cursorScopeKey, limit)
	if err != nil {
		return ControllerKeyIOLeasePage{}, err
	}
	defer rows.Close()
	page := ControllerKeyIOLeasePage{Leases: make([]ControllerKeyIOLease, 0, limit)}
	for rows.Next() {
		lease, scanErr := scanControllerKeyIOLease(rows)
		if scanErr != nil {
			return ControllerKeyIOLeasePage{}, scanErr
		}
		page.Leases = append(page.Leases, lease)
	}
	if err = rows.Err(); err != nil {
		return ControllerKeyIOLeasePage{}, err
	}
	if len(page.Leases) < limit {
		page.Complete = true
	} else {
		page.NextCursor = controllerKeyIOCursor(page.Leases[len(page.Leases)-1].ScopeKey)
	}
	return page, nil
}

func (r *Repository) ClaimExpiredControllerKeyIOLease(ctx context.Context, candidate ControllerKeyIOLease, recoveryLeaseID string, at, expiresAt time.Time) (ControllerKeyIOLease, error) {
	if !validControllerKeyIOLease(candidate) || !canonicalUUID(recoveryLeaseID) || recoveryLeaseID == candidate.LeaseID || at.IsZero() || !expiresAt.After(at) || candidate.LeaseExpiresAt.After(at) {
		return ControllerKeyIOLease{}, ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE relay_controller_key_io_leases SET lease_id=?,phase='recovery',fence=fence+1,lease_expires_at=?,updated_at=? WHERE scope_key=? AND lease_id=? AND fence=? AND lease_expires_at<=?`, recoveryLeaseID, timestamp(expiresAt), timestamp(at), candidate.ScopeKey, candidate.LeaseID, candidate.Fence, timestamp(at))
	if err != nil {
		return ControllerKeyIOLease{}, classifyConstraint(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ControllerKeyIOLease{}, err
	}
	if count != 1 {
		return ControllerKeyIOLease{}, ErrConflict
	}
	claimed, err := scanControllerKeyIOLease(r.db.QueryRowContext(ctx, controllerKeyIOLeaseSelect+` WHERE scope_key=?`, candidate.ScopeKey))
	if err != nil {
		return ControllerKeyIOLease{}, err
	}
	if claimed.LeaseID != recoveryLeaseID || claimed.Fence != candidate.Fence+1 || claimed.Phase != ControllerKeyIORecovery {
		clear(claimed.PublicKey)
		return ControllerKeyIOLease{}, ErrState
	}
	return claimed, nil
}

func (r *Repository) AcquireControllerKeyCleanupLease(ctx context.Context, lease ControllerKeyIOLease) error {
	if !validControllerKeyIOLease(lease) || lease.Phase != ControllerKeyIORecovery || lease.Fence != 1 || (lease.Operation != ControllerKeyIORevokedCleanup && lease.Operation != ControllerKeyIOKeyCleanup && lease.Operation != ControllerKeyIOTempCleanup) {
		return ErrInvalid
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,rotation_id,old_key_id,public_key,protected_key_ref,artifact_name,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,NULL,NULL,NULL,?,?,?,?)`, lease.ScopeKey, lease.ControllerID, lease.LeaseID, lease.Operation, lease.Phase, lease.Fence, timestamp(lease.LeaseExpiresAt), nullable(lease.KeyID), nullable(lease.ProtectedKeyRef), nullable(lease.ArtifactName), timestamp(lease.CreatedAt), timestamp(lease.UpdatedAt))
	return classifyConstraint(err)
}

func (r *Repository) FinishControllerKeyIOLease(ctx context.Context, lease ControllerKeyIOLease) error {
	if !validControllerKeyIOLease(lease) {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM relay_controller_key_io_leases WHERE scope_key=? AND lease_id=? AND fence=? AND operation=? AND phase=?`, lease.ScopeKey, lease.LeaseID, lease.Fence, lease.Operation, lease.Phase)
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
	var remaining int
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_controller_key_io_leases WHERE scope_key=?`, lease.ScopeKey).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		return nil
	}
	return ErrState
}

func scanControllerKeyIOLease(row scanner) (ControllerKeyIOLease, error) {
	var value ControllerKeyIOLease
	var expires, created, updated string
	var publicKey []byte
	if err := row.Scan(&value.ScopeKey, &value.ControllerID, &value.LeaseID, &value.Operation, &value.Phase, &value.Fence, &expires, &value.KeyID, &value.RotationID, &value.OldKeyID, &publicKey, &value.ProtectedKeyRef, &value.ArtifactName, &created, &updated); err != nil {
		return value, err
	}
	value.PublicKey = append([]byte(nil), publicKey...)
	var err error
	if value.LeaseExpiresAt, err = parseTimestamp(expires); err != nil {
		clear(value.PublicKey)
		return ControllerKeyIOLease{}, err
	}
	if value.CreatedAt, err = parseTimestamp(created); err != nil {
		clear(value.PublicKey)
		return ControllerKeyIOLease{}, err
	}
	if value.UpdatedAt, err = parseTimestamp(updated); err != nil {
		clear(value.PublicKey)
		return ControllerKeyIOLease{}, err
	}
	if !validControllerKeyIOLease(value) {
		clear(value.PublicKey)
		return ControllerKeyIOLease{}, ErrConflict
	}
	return value, nil
}

func validControllerKeyIOLease(value ControllerKeyIOLease) bool {
	if !canonicalUUID(value.ControllerID) || !canonicalUUID(value.LeaseID) || value.Fence == 0 || value.LeaseExpiresAt.IsZero() || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) || !value.LeaseExpiresAt.After(value.UpdatedAt) || (value.Phase != ControllerKeyIOActive && value.Phase != ControllerKeyIORecovery) {
		return false
	}
	switch value.Operation {
	case ControllerKeyIOIdentityWrite:
		return value.ScopeKey == controllerIdentityIOScope && canonicalUUID(value.KeyID) && value.RotationID == "" && value.OldKeyID == "" && len(value.PublicKey) == 32 && value.ProtectedKeyRef == ProtectedKeyRef(value.ControllerID, value.KeyID) && value.ArtifactName == ""
	case ControllerKeyIOWrite:
		return value.ScopeKey == controllerKeyIOScope(value.ControllerID) && canonicalUUID(value.KeyID) && canonicalUUID(value.RotationID) && canonicalUUID(value.OldKeyID) && value.KeyID != value.OldKeyID && len(value.PublicKey) == 32 && value.ProtectedKeyRef == ProtectedKeyRef(value.ControllerID, value.KeyID) && value.ArtifactName == ""
	case ControllerKeyIOKeyCleanup:
		return value.ScopeKey == controllerKeyIOScope(value.ControllerID) && value.Phase == ControllerKeyIORecovery && canonicalUUID(value.KeyID) && value.RotationID == "" && value.OldKeyID == "" && len(value.PublicKey) == 0 && value.ProtectedKeyRef == ProtectedKeyRef(value.ControllerID, value.KeyID) && value.ArtifactName == ""
	case ControllerKeyIORevokedCleanup:
		return value.ScopeKey == controllerKeyIOScope(value.ControllerID) && value.Phase == ControllerKeyIORecovery && canonicalUUID(value.KeyID) && value.RotationID == "" && value.OldKeyID == "" && len(value.PublicKey) == 0 && value.ProtectedKeyRef == ProtectedKeyRef(value.ControllerID, value.KeyID) && value.ArtifactName == ""
	case ControllerKeyIOTempCleanup:
		return value.ScopeKey == controllerKeyIOScope(value.ControllerID) && value.Phase == ControllerKeyIORecovery && value.KeyID == "" && value.RotationID == "" && value.OldKeyID == "" && len(value.PublicKey) == 0 && value.ProtectedKeyRef == "" && validControllerKeyTemporaryArtifactName(value.ArtifactName)
	default:
		return false
	}
}

func sameControllerKeyIOLeaseIdentity(left, right ControllerKeyIOLease) bool {
	return left.ScopeKey == right.ScopeKey && left.ControllerID == right.ControllerID && left.LeaseID == right.LeaseID && left.Operation == right.Operation && left.Phase == right.Phase && left.Fence == right.Fence && left.KeyID == right.KeyID && left.RotationID == right.RotationID && left.OldKeyID == right.OldKeyID && left.ProtectedKeyRef == right.ProtectedKeyRef && left.ArtifactName == right.ArtifactName && bytes.Equal(left.PublicKey, right.PublicKey)
}

const controllerIdentityIOScope = "0/identity"

func controllerKeyIOScope(controllerID string) string { return "1/controller/" + controllerID }

func controllerKeyIOCursor(scopeKey string) string { return "v2:" + scopeKey }

func parseControllerKeyIOCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if !strings.HasPrefix(cursor, "v2:") || (cursor[3:] != controllerIdentityIOScope && (!strings.HasPrefix(cursor[3:], "1/controller/") || !canonicalUUID(strings.TrimPrefix(cursor[3:], "1/controller/")))) || cursor != controllerKeyIOCursor(cursor[3:]) {
		return "", ErrInvalid
	}
	return cursor[3:], nil
}
