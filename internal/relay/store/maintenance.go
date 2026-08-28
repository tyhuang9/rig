package store

import (
	"context"
	"database/sql"
	"time"
)

const (
	MinimumCommandReplayRetention   = 24 * time.Hour
	MinimumExpiredSessionRetention  = time.Hour
	MinimumSubscriptionSetRetention = time.Hour
	MinimumDeliveryHistoryRetention = 24 * time.Hour
	MaximumMaintenanceBatch         = 1000
	MaximumActiveEnrollments        = 256
	EnrollmentTerminalRetention     = 7 * 24 * time.Hour
)

// DurableRetentionPolicy defines the minimum durable replay and diagnostic
// windows preserved by PruneDurableState. One invocation deletes at most
// BatchSize rows from each result field below; the relay lifecycle must invoke
// it periodically. Keeping scheduling outside Store makes maintenance wakeups
// non-authoritative; each monotonic operation is its own short, retryable
// transaction and committed progress is returned if a later operation fails.
type DurableRetentionPolicy struct {
	CommandReplayRetention   time.Duration
	ExpiredSessionRetention  time.Duration
	SubscriptionSetRetention time.Duration
	DeliveryHistoryRetention time.Duration
	BatchSize                int
}

func DefaultDurableRetentionPolicy() DurableRetentionPolicy {
	return DurableRetentionPolicy{
		CommandReplayRetention:   7 * 24 * time.Hour,
		ExpiredSessionRetention:  24 * time.Hour,
		SubscriptionSetRetention: 24 * time.Hour,
		DeliveryHistoryRetention: 7 * 24 * time.Hour,
		BatchSize:                200,
	}
}

type DurablePruneResult struct {
	SubscriptionSetItems  int64
	SourceDeliveryTargets int64
	RetiredDesiredStates  int64
	RetiredSubscriptions  int64
	TerminalAccessEvents  int64
	IgnoredDeliveries     int64
	RecoveryAttempts      int64
	RecoveryDeliveries    int64
	OrphanDeliveries      int64
	ExpiredLeases         int64
	SessionCommands       int64
	SessionMessages       int64
	RotationReferences    int64
	ExpiredSessions       int64
	ExpiredChallenges     int64
	TerminalEnrollments   int64
}

func (r DurablePruneResult) Total() int64 {
	return r.SubscriptionSetItems + r.SourceDeliveryTargets + r.RetiredDesiredStates + r.RetiredSubscriptions + r.TerminalAccessEvents + r.IgnoredDeliveries + r.RecoveryAttempts + r.RecoveryDeliveries + r.OrphanDeliveries + r.ExpiredLeases + r.SessionCommands + r.SessionMessages + r.RotationReferences + r.ExpiredSessions + r.ExpiredChallenges + r.TerminalEnrollments
}

// PruneDurableState removes only bounded, unreachable history. It preserves
// the current subscription head/set and its pending desired state, pending
// access rows, every live or recently expired session/lease/challenge, command replay
// through the configured horizon, and every unexpired rotation challenge
// result. Retirement cancels delivery, so desired state for an old
// subscription becomes eligible only after the delivery-history horizon.
func (s *Store) PruneDurableState(ctx context.Context, policy DurableRetentionPolicy) (DurablePruneResult, error) {
	if policy.CommandReplayRetention < MinimumCommandReplayRetention || policy.ExpiredSessionRetention < MinimumExpiredSessionRetention || policy.SubscriptionSetRetention < MinimumSubscriptionSetRetention || policy.DeliveryHistoryRetention < MinimumDeliveryHistoryRetention || policy.BatchSize < 1 || policy.BatchSize > MaximumMaintenanceBatch {
		return DurablePruneResult{}, ErrInvalid
	}
	now := s.now().UTC()
	commandBefore := now.Add(-policy.CommandReplayRetention)
	sessionBefore := now.Add(-policy.ExpiredSessionRetention)
	setBefore := now.Add(-policy.SubscriptionSetRetention)
	historyBefore := now.Add(-policy.DeliveryHistoryRetention)
	enrollmentBefore := now.Add(-EnrollmentTerminalRetention)
	var result DurablePruneResult

	queries := []struct {
		destination *int64
		query       string
		args        []any
	}{
		{
			&result.TerminalEnrollments,
			// Enrollment polling is guaranteed for its live lifetime. Terminal
			// records are retained for at least seven additional days and are
			// deleted in bounded 1000-row maintenance batches only after both
			// expires_at and completed_at are safely in the past.
			`WITH candidates AS (SELECT e.enrollment_id FROM relay_enrollments e WHERE e.status IN ('authorized','failed','expired') AND e.expires_at<$1 AND e.completed_at<$2 ORDER BY e.completed_at,e.enrollment_id LIMIT $3 FOR UPDATE OF e SKIP LOCKED) DELETE FROM relay_enrollments e USING candidates c WHERE e.enrollment_id=c.enrollment_id`,
			[]any{now, enrollmentBefore, MaximumMaintenanceBatch},
		},
		{
			&result.SourceDeliveryTargets,
			`WITH candidates AS (SELECT t.delivery_id,t.subscription_id FROM relay_source_delivery_targets t JOIN relay_subscriptions s ON s.subscription_id=t.subscription_id WHERE t.persisted_at<$1 AND ((s.retired_generation IS NOT NULL AND s.retired_at<$1) OR NOT EXISTS(SELECT 1 FROM relay_desired_states d WHERE d.subscription_id=t.subscription_id AND d.generation=t.generation AND d.decision IS NULL)) ORDER BY t.persisted_at,t.delivery_id,t.subscription_id LIMIT $2 FOR UPDATE OF t SKIP LOCKED) DELETE FROM relay_source_delivery_targets t USING candidates c WHERE t.delivery_id=c.delivery_id AND t.subscription_id=c.subscription_id`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.RetiredDesiredStates,
			`WITH candidates AS (SELECT d.subscription_id FROM relay_desired_states d JOIN relay_subscriptions s ON s.subscription_id=d.subscription_id WHERE s.retired_generation IS NOT NULL AND s.retired_at<$1 AND d.observed_at<$1 ORDER BY d.observed_at,d.subscription_id LIMIT $2 FOR UPDATE OF d SKIP LOCKED) DELETE FROM relay_desired_states d USING candidates c WHERE d.subscription_id=c.subscription_id`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.SubscriptionSetItems,
			`WITH candidates AS (SELECT i.controller_id,i.set_generation,i.subscription_id FROM relay_subscription_set_items i JOIN relay_subscription_heads h ON h.controller_id=i.controller_id WHERE i.set_generation<>h.generation AND i.created_at<$1 ORDER BY i.created_at,i.controller_id,i.set_generation,i.subscription_id LIMIT $2 FOR UPDATE OF i SKIP LOCKED) DELETE FROM relay_subscription_set_items i USING candidates c WHERE i.controller_id=c.controller_id AND i.set_generation=c.set_generation AND i.subscription_id=c.subscription_id`,
			[]any{setBefore, policy.BatchSize},
		},
		{
			&result.RetiredSubscriptions,
			`WITH candidates AS (SELECT s.subscription_id FROM relay_subscriptions s WHERE s.retired_generation IS NOT NULL AND s.retired_at<$1 AND NOT EXISTS(SELECT 1 FROM relay_subscription_set_items i WHERE i.subscription_id=s.subscription_id) AND NOT EXISTS(SELECT 1 FROM relay_desired_states d WHERE d.subscription_id=s.subscription_id) AND NOT EXISTS(SELECT 1 FROM relay_source_delivery_targets t WHERE t.subscription_id=s.subscription_id) ORDER BY s.retired_at,s.subscription_id LIMIT $2 FOR UPDATE OF s SKIP LOCKED) DELETE FROM relay_subscriptions s USING candidates c WHERE s.subscription_id=c.subscription_id`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.TerminalAccessEvents,
			`WITH candidates AS (SELECT a.event_id FROM relay_access_events a WHERE a.decision IS NOT NULL AND a.decided_at<$1 ORDER BY a.decided_at,a.event_id LIMIT $2 FOR UPDATE OF a SKIP LOCKED) DELETE FROM relay_access_events a USING candidates c WHERE a.event_id=c.event_id`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.IgnoredDeliveries,
			`WITH candidates AS (SELECT i.delivery_id FROM relay_ignored_deliveries i WHERE i.persisted_at<$1 ORDER BY i.persisted_at,i.delivery_id LIMIT $2 FOR UPDATE OF i SKIP LOCKED) DELETE FROM relay_ignored_deliveries i USING candidates c WHERE i.delivery_id=c.delivery_id`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.RecoveryAttempts,
			`WITH candidates AS (SELECT a.delivery_number FROM relay_recovery_delivery_attempts a JOIN relay_recovery_deliveries r ON r.delivery_id=a.delivery_id WHERE r.recovered_at<$1 AND r.claim_id IS NULL AND r.claim_expires_at IS NULL AND (r.delivery_number IS NULL OR a.delivery_number<>r.delivery_number) ORDER BY r.recovered_at,a.delivery_number LIMIT $2 FOR UPDATE OF a SKIP LOCKED) DELETE FROM relay_recovery_delivery_attempts a USING candidates c WHERE a.delivery_number=c.delivery_number`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.RecoveryDeliveries,
			`SELECT r.delivery_id::text,r.delivery_number FROM relay_recovery_deliveries r WHERE r.recovered_at<$1 AND r.claim_id IS NULL AND r.claim_expires_at IS NULL AND NOT EXISTS(SELECT 1 FROM relay_recovery_delivery_attempts a WHERE a.delivery_id=r.delivery_id AND (r.delivery_number IS NULL OR a.delivery_number<>r.delivery_number)) ORDER BY r.recovered_at,r.delivery_id LIMIT $2 FOR UPDATE OF r SKIP LOCKED`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.OrphanDeliveries,
			`WITH candidates AS (SELECT g.delivery_id FROM relay_github_deliveries g WHERE g.persisted_at<$1 AND NOT EXISTS(SELECT 1 FROM relay_desired_states d WHERE d.delivery_id=g.delivery_id) AND NOT EXISTS(SELECT 1 FROM relay_source_delivery_targets t WHERE t.delivery_id=g.delivery_id) AND NOT EXISTS(SELECT 1 FROM relay_access_events a WHERE a.delivery_id=g.delivery_id) AND NOT EXISTS(SELECT 1 FROM relay_ignored_deliveries i WHERE i.delivery_id=g.delivery_id) AND NOT EXISTS(SELECT 1 FROM relay_recovery_deliveries r WHERE r.delivery_id=g.delivery_id) ORDER BY g.persisted_at,g.delivery_id LIMIT $2 FOR UPDATE OF g SKIP LOCKED) DELETE FROM relay_github_deliveries g USING candidates c WHERE g.delivery_id=c.delivery_id`,
			[]any{historyBefore, policy.BatchSize},
		},
		{
			&result.ExpiredLeases,
			`WITH candidates AS (SELECT l.controller_id FROM relay_controller_leases l JOIN relay_sessions s ON s.session_id=l.session_id AND s.controller_id=l.controller_id WHERE l.expires_at<$1 AND (s.expires_at<$1 OR (s.revoked_at IS NOT NULL AND s.revoked_at<$1)) ORDER BY l.expires_at,l.controller_id LIMIT $2 FOR UPDATE OF l SKIP LOCKED) DELETE FROM relay_controller_leases l USING candidates c WHERE l.controller_id=c.controller_id`,
			[]any{sessionBefore, policy.BatchSize},
		},
		{
			&result.SessionCommands,
			`WITH candidates AS (SELECT c.controller_id,c.message_id FROM relay_session_commands c JOIN relay_sessions s ON s.session_id=c.session_id AND s.controller_id=c.controller_id WHERE c.applied_at<$1 AND (s.expires_at<$2 OR (s.revoked_at IS NOT NULL AND s.revoked_at<$2)) AND NOT EXISTS(SELECT 1 FROM relay_controller_leases l WHERE l.session_id=c.session_id) AND NOT (c.result_kind='rotation_challenge' AND c.result_expires_at>=$3) ORDER BY c.applied_at,c.controller_id,c.message_id LIMIT $4 FOR UPDATE OF c SKIP LOCKED) DELETE FROM relay_session_commands c USING candidates p WHERE c.controller_id=p.controller_id AND c.message_id=p.message_id`,
			[]any{commandBefore, sessionBefore, now, policy.BatchSize},
		},
		{
			&result.SessionMessages,
			`WITH candidates AS (SELECT m.session_id,m.message_id FROM relay_session_messages m JOIN relay_sessions s ON s.session_id=m.session_id WHERE (s.expires_at<$1 OR (s.revoked_at IS NOT NULL AND s.revoked_at<$1)) AND NOT EXISTS(SELECT 1 FROM relay_controller_leases l WHERE l.session_id=m.session_id) ORDER BY m.seen_at,m.session_id,m.message_id LIMIT $2 FOR UPDATE OF m SKIP LOCKED) DELETE FROM relay_session_messages m USING candidates c WHERE m.session_id=c.session_id AND m.message_id=c.message_id`,
			[]any{sessionBefore, policy.BatchSize},
		},
		{
			&result.RotationReferences,
			`WITH candidates AS (SELECT k.key_id FROM relay_controller_keys k WHERE k.state='revoked' AND k.rotation_session_id IS NOT NULL AND k.revoked_at<$1 ORDER BY k.revoked_at,k.key_id LIMIT $2 FOR UPDATE OF k SKIP LOCKED) UPDATE relay_controller_keys k SET rotation_old_key_id=NULL,rotation_session_id=NULL,rotation_nonce=NULL,rotation_expires_at=NULL FROM candidates c WHERE k.key_id=c.key_id`,
			[]any{sessionBefore, policy.BatchSize},
		},
		{
			&result.ExpiredSessions,
			`WITH candidates AS (SELECT s.session_id FROM relay_sessions s WHERE (s.expires_at<$1 OR (s.revoked_at IS NOT NULL AND s.revoked_at<$1)) AND NOT EXISTS(SELECT 1 FROM relay_controller_leases l WHERE l.session_id=s.session_id) AND NOT EXISTS(SELECT 1 FROM relay_session_commands c WHERE c.session_id=s.session_id) AND NOT EXISTS(SELECT 1 FROM relay_session_messages m WHERE m.session_id=s.session_id) AND NOT EXISTS(SELECT 1 FROM relay_controller_keys k WHERE k.rotation_session_id=s.session_id) ORDER BY s.expires_at,s.session_id LIMIT $2 FOR UPDATE OF s SKIP LOCKED) DELETE FROM relay_sessions s USING candidates c WHERE s.session_id=c.session_id`,
			[]any{sessionBefore, policy.BatchSize},
		},
		{
			&result.ExpiredChallenges,
			`WITH candidates AS (SELECT c.session_id FROM relay_wss_challenges c WHERE (c.expires_at<$1 OR (c.consumed_at IS NOT NULL AND c.consumed_at<$1)) AND NOT EXISTS(SELECT 1 FROM relay_sessions s WHERE s.session_id=c.session_id) ORDER BY c.expires_at,c.session_id LIMIT $2 FOR UPDATE OF c SKIP LOCKED) DELETE FROM relay_wss_challenges c USING candidates p WHERE c.session_id=p.session_id`,
			[]any{sessionBefore, policy.BatchSize},
		},
	}
	for _, operation := range queries {
		var count int64
		var err error
		if operation.destination == &result.RecoveryDeliveries {
			count, err = s.pruneRecoveryDeliveryGroups(ctx, operation.query, operation.args...)
		} else {
			count, err = s.runDurablePruneOperation(ctx, operation.query, operation.args...)
		}
		if err != nil {
			return result, err
		}
		*operation.destination = count
	}
	return result, nil
}

func (s *Store) runDurablePruneOperation(ctx context.Context, query string, args ...any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollback(ctx, tx)
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	count := tag.RowsAffected()
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

// pruneRecoveryDeliveryGroups deliberately uses two data mutations in one
// explicit transaction. relay_recovery_deliveries selects an attempt through
// a deferred circular foreign key, so a data-modifying CTE would have
// unpredictable statement ordering and could expose an invalid intermediate
// state at commit.
func (s *Store) pruneRecoveryDeliveryGroups(ctx context.Context, selectQuery string, args ...any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollback(ctx, tx)
	rows, err := tx.Query(ctx, selectQuery, args...)
	if err != nil {
		return 0, err
	}
	var deliveryIDs []string
	selectedAttempts := int64(0)
	for rows.Next() {
		var deliveryID string
		var deliveryNumber sql.NullInt64
		if err = rows.Scan(&deliveryID, &deliveryNumber); err != nil {
			rows.Close()
			return 0, err
		}
		deliveryIDs = append(deliveryIDs, deliveryID)
		if deliveryNumber.Valid {
			selectedAttempts++
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if len(deliveryIDs) > 0 {
		attemptTag, deleteErr := tx.Exec(ctx, `DELETE FROM relay_recovery_delivery_attempts a USING relay_recovery_deliveries r WHERE r.delivery_id IN (SELECT selected.delivery_id::uuid FROM unnest($1::text[]) AS selected(delivery_id)) AND a.delivery_id=r.delivery_id AND a.delivery_number=r.delivery_number`, deliveryIDs)
		if deleteErr != nil {
			return 0, deleteErr
		}
		if attemptTag.RowsAffected() != selectedAttempts {
			return 0, ErrConflict
		}
		groupTag, deleteErr := tx.Exec(ctx, `DELETE FROM relay_recovery_deliveries r WHERE r.delivery_id IN (SELECT selected.delivery_id::uuid FROM unnest($1::text[]) AS selected(delivery_id))`, deliveryIDs)
		if deleteErr != nil {
			return 0, deleteErr
		}
		if groupTag.RowsAffected() != int64(len(deliveryIDs)) {
			return 0, ErrConflict
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(deliveryIDs)), nil
}
