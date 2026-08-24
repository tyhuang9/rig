package store

import (
	"context"
	"database/sql"
	"time"
)

type RecoveryCursor struct {
	ScanID          string
	Fence           uint64
	WindowStartedAt time.Time
	WindowEndsAt    time.Time
	PageCursor      string
	Completed       bool
	LeaseExpiresAt  time.Time
}

const (
	recoveryScanLease = 5 * time.Minute
	// StartRecoveryScan and ClaimRecovery take this transaction lock before
	// inspecting the cursor. It closes the no-row/bootstrap race and establishes
	// recovery scan -> cursor/group row as the global lock order.
	recoveryScanClaimLock int64 = 0x5249475245434f56
)

func (s *Store) StartRecoveryScan(ctx context.Context, start, end time.Time) (RecoveryCursor, error) {
	if !validTime(start) || !end.After(start) || end.Sub(start) > 72*time.Hour {
		return RecoveryCursor{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecoveryCursor{}, err
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, recoveryScanClaimLock); err != nil {
		return RecoveryCursor{}, err
	}
	now := s.now().UTC()
	var existing RecoveryCursor
	var page sql.NullString
	err = tx.QueryRow(ctx, `SELECT scan_id::text,fence,window_started_at,window_ends_at,page_cursor,completed,lease_expires_at FROM relay_recovery_cursor WHERE singleton FOR UPDATE`).Scan(&existing.ScanID, &existing.Fence, &existing.WindowStartedAt, &existing.WindowEndsAt, &page, &existing.Completed, &existing.LeaseExpiresAt)
	if err == nil && !existing.Completed && existing.LeaseExpiresAt.After(now) {
		return RecoveryCursor{}, ErrConflict
	}
	if err != nil && !isNoRows(err) {
		return RecoveryCursor{}, err
	}
	if err == nil && !existing.Completed {
		existing.PageCursor = page.String
		nextLease := now.Add(recoveryScanLease)
		tag, updateErr := tx.Exec(ctx, `UPDATE relay_recovery_cursor SET fence=fence+1,lease_expires_at=$3,updated_at=$4 WHERE singleton AND scan_id=$1 AND fence=$2 AND completed=false AND lease_expires_at<=$4`, existing.ScanID, existing.Fence, nextLease, now)
		if updateErr != nil {
			return RecoveryCursor{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return RecoveryCursor{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return RecoveryCursor{}, err
		}
		existing.Fence++
		existing.LeaseExpiresAt = nextLease
		return existing, nil
	}
	out := RecoveryCursor{ScanID: s.newUUID().String(), Fence: 1, WindowStartedAt: start.UTC(), WindowEndsAt: end.UTC(), LeaseExpiresAt: now.Add(recoveryScanLease)}
	_, err = tx.Exec(ctx, `INSERT INTO relay_recovery_cursor(singleton,scan_id,fence,window_started_at,window_ends_at,page_cursor,completed,lease_expires_at,updated_at) VALUES(true,$1,1,$2,$3,NULL,false,$4,$5) ON CONFLICT(singleton) DO UPDATE SET scan_id=EXCLUDED.scan_id,fence=1,window_started_at=EXCLUDED.window_started_at,window_ends_at=EXCLUDED.window_ends_at,page_cursor=NULL,completed=false,lease_expires_at=EXCLUDED.lease_expires_at,updated_at=EXCLUDED.updated_at`, out.ScanID, out.WindowStartedAt, out.WindowEndsAt, out.LeaseExpiresAt, now)
	if err != nil {
		return RecoveryCursor{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RecoveryCursor{}, err
	}
	return out, nil
}
func (s *Store) AdvanceRecoveryCursor(ctx context.Context, current RecoveryCursor, nextPage string) (RecoveryCursor, error) {
	if !validUUID(current.ScanID) || current.Fence == 0 || current.Completed || nextPage == "" || len(nextPage) > 1024 {
		return RecoveryCursor{}, ErrInvalid
	}
	now := s.now().UTC()
	var currentPage any
	if current.PageCursor != "" {
		currentPage = current.PageCursor
	}
	nextLease := now.Add(recoveryScanLease)
	tag, err := s.pool.Exec(ctx, `UPDATE relay_recovery_cursor SET page_cursor=$4,fence=fence+1,lease_expires_at=$6,updated_at=$5 WHERE singleton AND scan_id=$1 AND fence=$2 AND page_cursor IS NOT DISTINCT FROM $3 AND completed=false AND lease_expires_at>$5`, current.ScanID, current.Fence, currentPage, nextPage, now, nextLease)
	if err != nil {
		return RecoveryCursor{}, err
	}
	if tag.RowsAffected() != 1 {
		return RecoveryCursor{}, ErrConflict
	}
	current.Fence++
	current.PageCursor = nextPage
	current.LeaseExpiresAt = nextLease
	return current, nil
}
func (s *Store) CompleteRecoveryScan(ctx context.Context, current RecoveryCursor) error {
	if !validUUID(current.ScanID) || current.Fence == 0 || current.Completed {
		return ErrInvalid
	}
	now := s.now().UTC()
	var currentPage any
	if current.PageCursor != "" {
		currentPage = current.PageCursor
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_recovery_cursor SET completed=true,fence=fence+1,lease_expires_at=$4,updated_at=$4 WHERE singleton AND scan_id=$1 AND fence=$2 AND page_cursor IS NOT DISTINCT FROM $3 AND completed=false AND lease_expires_at>$4`, current.ScanID, current.Fence, currentPage, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

type RecoveryDelivery struct {
	DeliveryNumber int64
	DeliveryID     string
	OccurredAt     time.Time
	Successful     bool
	Attempts       int
	NextAttemptAt  *time.Time
	LastErrorCode  string
}

func (s *Store) DiscoverRecoveryDelivery(ctx context.Context, item RecoveryDelivery) (bool, error) {
	if item.DeliveryNumber <= 0 || !validUUID(item.DeliveryID) || !validTime(item.OccurredAt) {
		return false, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, deliveryLockKey(item.DeliveryID)); err != nil {
		return false, err
	}
	now := s.now().UTC()
	var selectedNumber sql.NullInt64
	var selectedAt, providerSucceededAt, recoveredAt sql.NullTime
	err = tx.QueryRow(ctx, `SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at FROM relay_recovery_deliveries WHERE delivery_id=$1 FOR UPDATE`, item.DeliveryID).Scan(&selectedNumber, &selectedAt, &providerSucceededAt, &recoveredAt)
	newGroup := isNoRows(err)
	if err != nil && !newGroup {
		return false, err
	}
	inboundRecorded := false
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_github_deliveries WHERE delivery_id=$1)`, item.DeliveryID).Scan(&inboundRecorded); err != nil {
		return false, err
	}
	if newGroup {
		var number, occurred, succeeded, recovered any
		if !item.Successful {
			number, occurred = item.DeliveryNumber, item.OccurredAt.UTC()
		}
		if item.Successful {
			succeeded = item.OccurredAt.UTC()
		}
		if inboundRecorded {
			recovered = now
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_recovery_deliveries(delivery_number,delivery_id,occurred_at,discovered_at,provider_succeeded_at,recovered_at) VALUES($1,$2,$3,$4,$5,$6)`, number, item.DeliveryID, occurred, now, succeeded, recovered); err != nil {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO relay_recovery_delivery_attempts(delivery_number,delivery_id,occurred_at,successful,discovered_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(delivery_number) DO NOTHING`, item.DeliveryNumber, item.DeliveryID, item.OccurredAt.UTC(), item.Successful, now)
	if err != nil {
		return false, err
	}
	deduplicated := tag.RowsAffected() == 0
	if deduplicated {
		var storedID string
		var storedAt time.Time
		var storedSuccessful bool
		if err = tx.QueryRow(ctx, `SELECT delivery_id::text,occurred_at,successful FROM relay_recovery_delivery_attempts WHERE delivery_number=$1`, item.DeliveryNumber).Scan(&storedID, &storedAt, &storedSuccessful); err != nil {
			return false, err
		}
		if storedID != item.DeliveryID || !storedAt.Equal(item.OccurredAt.UTC()) || storedSuccessful != item.Successful {
			return false, ErrConflict
		}
	}
	if !newGroup {
		if inboundRecorded {
			tag, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=COALESCE(recovered_at,$2),next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL,claim_fence=claim_fence+1 WHERE delivery_id=$1`, item.DeliveryID, now)
			if err != nil || tag.RowsAffected() != 1 {
				if err != nil {
					return false, err
				}
				return false, ErrConflict
			}
		} else if item.Successful {
			tag, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET provider_succeeded_at=COALESCE(provider_succeeded_at,$2),recovered_at=CASE WHEN $3 THEN COALESCE(recovered_at,$4) ELSE recovered_at END,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL,claim_fence=claim_fence+1 WHERE delivery_id=$1`, item.DeliveryID, item.OccurredAt.UTC(), inboundRecorded, now)
			if err != nil || tag.RowsAffected() != 1 {
				if err != nil {
					return false, err
				}
				return false, ErrConflict
			}
		} else if !providerSucceededAt.Valid && (!selectedAt.Valid || item.OccurredAt.After(selectedAt.Time) || (item.OccurredAt.Equal(selectedAt.Time) && (!selectedNumber.Valid || item.DeliveryNumber > selectedNumber.Int64))) {
			tag, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET delivery_number=$2,occurred_at=$3,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL,claim_fence=claim_fence+1 WHERE delivery_id=$1 AND provider_succeeded_at IS NULL`, item.DeliveryID, item.DeliveryNumber, item.OccurredAt.UTC())
			if err != nil || tag.RowsAffected() != 1 {
				if err != nil {
					return false, err
				}
				return false, ErrConflict
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return deduplicated, nil
}

type RecoveryClaim struct {
	RecoveryDelivery
	ClaimID        string
	Fence          uint64
	ClaimExpiresAt time.Time
}

func (s *Store) ClaimRecovery(ctx context.Context, limit int, lease time.Duration) ([]RecoveryClaim, error) {
	if limit < 1 || limit > 1000 || lease < time.Second || lease > 10*time.Minute {
		return nil, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, recoveryScanClaimLock); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	rows, err := tx.Query(ctx, `SELECT delivery_number,delivery_id::text,occurred_at,attempts,next_attempt_at,COALESCE(last_error_code,''),claim_fence FROM relay_recovery_deliveries WHERE recovered_at IS NULL AND provider_succeeded_at IS NULL AND delivery_number IS NOT NULL AND (next_attempt_at IS NULL OR next_attempt_at<=$1) AND (claim_id IS NULL OR claim_expires_at<=$1) AND NOT EXISTS(SELECT 1 FROM relay_recovery_cursor c WHERE c.singleton AND c.completed=false) ORDER BY occurred_at,delivery_number FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	type selected struct {
		claim    RecoveryClaim
		oldFence int64
	}
	var pending []selected
	for rows.Next() {
		var p selected
		if err = rows.Scan(&p.claim.DeliveryNumber, &p.claim.DeliveryID, &p.claim.OccurredAt, &p.claim.Attempts, &p.claim.NextAttemptAt, &p.claim.LastErrorCode, &p.oldFence); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	out := make([]RecoveryClaim, 0, len(pending))
	for _, p := range pending {
		p.claim.ClaimID = s.newUUID().String()
		p.claim.Fence = uint64(p.oldFence + 1)
		p.claim.ClaimExpiresAt = now.Add(lease)
		tag, err := tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET claim_id=$3,claim_fence=$4,claim_expires_at=$5 WHERE delivery_id=$1 AND delivery_number=$2 AND claim_fence=$6 AND provider_succeeded_at IS NULL`, p.claim.DeliveryID, p.claim.DeliveryNumber, p.claim.ClaimID, p.claim.Fence, p.claim.ClaimExpiresAt, p.oldFence)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrConflict
		}
		out = append(out, p.claim)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Store) RecordRecoveryAttempt(ctx context.Context, claim RecoveryClaim, next time.Time, code string) error {
	now := s.now().UTC()
	if claim.DeliveryNumber <= 0 || !validUUID(claim.DeliveryID) || !validUUID(claim.ClaimID) || claim.Fence == 0 || !next.After(now) || next.Sub(now) > 24*time.Hour || !validCode(code) {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_recovery_deliveries SET attempts=attempts+1,next_attempt_at=$5,last_error_code=$6,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND delivery_number=$2 AND claim_id=$3 AND claim_fence=$4 AND claim_expires_at>$7 AND recovered_at IS NULL AND provider_succeeded_at IS NULL AND attempts<1000000`, claim.DeliveryID, claim.DeliveryNumber, claim.ClaimID, claim.Fence, next.UTC(), code, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) MarkRecovered(ctx context.Context, claim RecoveryClaim) error {
	now := s.now().UTC()
	if claim.DeliveryNumber <= 0 || !validUUID(claim.DeliveryID) || !validUUID(claim.ClaimID) || claim.Fence == 0 {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_recovery_deliveries r SET recovered_at=$5,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND delivery_number=$2 AND claim_id=$3 AND claim_fence=$4 AND claim_expires_at>$5 AND recovered_at IS NULL AND EXISTS(SELECT 1 FROM relay_github_deliveries d WHERE d.delivery_id=r.delivery_id)`, claim.DeliveryID, claim.DeliveryNumber, claim.ClaimID, claim.Fence, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
