package autodeploystate

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
)

var (
	ErrInvalid = errors.New("invalid auto-deploy state request")
	ErrState   = errors.New("auto-deploy source access transition failed")
)

const coordinationTimestampLayout = "2006-01-02T15:04:05.000000000Z"

// PauseConnectionSourceAccessLostTx overlays source access loss on every
// enabled GitHub auto-deploy head bound to one owner-scoped connection. The
// caller owns the transaction that changes the source connection itself.
func PauseConnectionSourceAccessLostTx(ctx context.Context, tx *sql.Tx, ownerUserID, connectionID string, at time.Time) (int64, error) {
	if ctx == nil || tx == nil || !validIdentifier(ownerUserID) || !validIdentifier(connectionID) || at.IsZero() {
		return 0, ErrInvalid
	}
	headScope := `
		FROM github_auto_deploy_heads h
		JOIN github_auto_deploy_configs c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		JOIN application_sources src ON src.application_id=c.application_id AND src.source_type='github'
		JOIN source_connections sc ON sc.id=src.connection_id AND sc.owner_user_id=c.source_owner_user_id
		WHERE c.source_owner_user_id=? AND src.connection_id=?`
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) `+headScope, ownerUserID, connectionID).Scan(&expected); err != nil {
		return 0, err
	}
	if expected == 0 {
		return 0, nil
	}
	stamp := at.UTC().Format(coordinationTimestampLayout)
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='paused',pause_code='source_access_lost',paused_sha=COALESCE(active_sha,prepared_dispatch_sha,latest_resolved_sha,
			(SELECT resolved_sha FROM application_sources WHERE application_id=github_auto_deploy_heads.application_id AND source_type='github')),
			prepared_dispatch_sequence=NULL,prepared_dispatch_generation=NULL,prepared_dispatch_sha=NULL,
			resolving_generation=NULL,resolving_lease_fence=NULL,retry_attempt=0,next_retry_at=NULL,next_reconcile_at=NULL,
			next_job_poll_at=CASE WHEN active_job_id IS NOT NULL THEN ? ELSE NULL END,
			lease_fence=lease_fence+1,lease_token=NULL,lease_expires_at=NULL,updated_at=?
		WHERE lease_fence<? AND application_id IN (SELECT h.application_id `+headScope+`)
		  AND COALESCE(active_sha,prepared_dispatch_sha,latest_resolved_sha,
			(SELECT resolved_sha FROM application_sources WHERE application_id=github_auto_deploy_heads.application_id AND source_type='github')) IS NOT NULL`,
		stamp, stamp, int64(math.MaxInt64), ownerUserID, connectionID)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed != expected {
		return 0, ErrState
	}
	return changed, nil
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 255 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
