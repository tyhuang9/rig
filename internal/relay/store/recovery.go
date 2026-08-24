package store

import (
	"context"
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

const recoveryScanLease = 5 * time.Minute

func (s *Store) StartRecoveryScan(ctx context.Context, start, end time.Time) (RecoveryCursor, error) {
	if !validTime(start) || !end.After(start) || end.Sub(start) > 72*time.Hour {
		return RecoveryCursor{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecoveryCursor{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	var complete bool
	var leaseExpires time.Time
	err = tx.QueryRow(ctx, `SELECT completed,lease_expires_at FROM relay_recovery_cursor WHERE singleton FOR UPDATE`).Scan(&complete, &leaseExpires)
	if err == nil && !complete && leaseExpires.After(now) {
		return RecoveryCursor{}, ErrConflict
	}
	if err != nil && !isNoRows(err) {
		return RecoveryCursor{}, err
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
	Attempts       int
	NextAttemptAt  *time.Time
	LastErrorCode  string
}

func (s *Store) DiscoverRecoveryDelivery(ctx context.Context, item RecoveryDelivery) (bool, error) {
	if item.DeliveryNumber <= 0 || !validUUID(item.DeliveryID) || !validTime(item.OccurredAt) {
		return false, ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO relay_recovery_deliveries(delivery_number,delivery_id,occurred_at,discovered_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, item.DeliveryNumber, item.DeliveryID, item.OccurredAt.UTC(), s.now().UTC())
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return false, nil
	}
	var storedNumber int64
	var storedID string
	err = s.pool.QueryRow(ctx, `SELECT delivery_number,delivery_id::text FROM relay_recovery_deliveries WHERE delivery_number=$1 OR delivery_id=$2`, item.DeliveryNumber, item.DeliveryID).Scan(&storedNumber, &storedID)
	if err != nil {
		return false, err
	}
	if storedNumber != item.DeliveryNumber || storedID != item.DeliveryID {
		return false, ErrConflict
	}
	return true, nil
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
	now := s.now().UTC()
	rows, err := tx.Query(ctx, `SELECT delivery_number,delivery_id::text,occurred_at,attempts,next_attempt_at,COALESCE(last_error_code,''),claim_fence FROM relay_recovery_deliveries WHERE recovered_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at<=$1) AND (claim_id IS NULL OR claim_expires_at<=$1) ORDER BY delivery_number FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit)
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
		tag, err := tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET claim_id=$2,claim_fence=$3,claim_expires_at=$4 WHERE delivery_number=$1 AND claim_fence=$5`, p.claim.DeliveryNumber, p.claim.ClaimID, p.claim.Fence, p.claim.ClaimExpiresAt, p.oldFence)
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
	if claim.DeliveryNumber <= 0 || !validUUID(claim.ClaimID) || claim.Fence == 0 || !next.After(now) || next.Sub(now) > 24*time.Hour || !validCode(code) {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_recovery_deliveries SET attempts=attempts+1,next_attempt_at=$4,last_error_code=$5,claim_id=NULL,claim_expires_at=NULL WHERE delivery_number=$1 AND claim_id=$2 AND claim_fence=$3 AND claim_expires_at>$6 AND recovered_at IS NULL AND attempts<1000000`, claim.DeliveryNumber, claim.ClaimID, claim.Fence, next.UTC(), code, now)
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
	if claim.DeliveryNumber <= 0 || !validUUID(claim.ClaimID) || claim.Fence == 0 {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_recovery_deliveries r SET recovered_at=$4,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_number=$1 AND claim_id=$2 AND claim_fence=$3 AND claim_expires_at>$4 AND recovered_at IS NULL AND EXISTS(SELECT 1 FROM relay_github_deliveries d WHERE d.delivery_id=r.delivery_id)`, claim.DeliveryNumber, claim.ClaimID, claim.Fence, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
