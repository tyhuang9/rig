package autodeploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	maxListLimit           = 500
	maxLeaseTTL            = 15 * time.Minute
	activeJobPollInterval  = 5 * time.Second
	waitingJobPollInterval = 30 * time.Second
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Get(ctx context.Context, applicationID string) (Status, error) {
	if !validOpaqueID(applicationID) {
		return Status{}, ErrInvalid
	}
	value, err := scanStatus(r.db.QueryRowContext(ctx, statusSelect+` WHERE c.application_id=?`, applicationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	return value, err
}

func (r *Repository) List(ctx context.Context, afterApplicationID string, limit int) ([]Status, error) {
	if limit < 1 || limit > maxListLimit || (afterApplicationID != "" && !validOpaqueID(afterApplicationID)) {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, statusSelect+` WHERE c.application_id>? ORDER BY c.application_id LIMIT ?`, afterApplicationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Status, 0, limit)
	for rows.Next() {
		value, scanErr := scanStatus(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// Configure derives the complete relay scope from the application's GitHub
// source and an authorized binding. Installation, repository, ref, and SHA are
// deliberately absent from ConfigureRequest.
func (r *Repository) Configure(ctx context.Context, request ConfigureRequest, at time.Time) (Status, error) {
	if ctx == nil || !validOpaqueID(request.ApplicationID) || !validOpaqueID(request.ActorUserID) || request.ExpectedRevision > math.MaxInt64 || at.IsZero() {
		return Status{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback()
	if err = requireAdministrator(ctx, tx, request.ActorUserID); err != nil {
		return Status{}, err
	}
	var revision int64
	var enabled bool
	var owner, subscriptionID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT revision,enabled,source_owner_user_id,subscription_id FROM github_auto_deploy_configs WHERE application_id=?`, request.ApplicationID).Scan(&revision, &enabled, &owner, &subscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, err
	}
	if enabled && (!owner.Valid || owner.String != request.ActorUserID) {
		return Status{}, ErrNotFound
	}
	if uint64(revision) != request.ExpectedRevision {
		return Status{}, ErrConflict
	}
	if enabled == request.Enabled {
		value, loadErr := loadStatusTx(ctx, tx, request.ApplicationID)
		if loadErr != nil {
			return Status{}, loadErr
		}
		if err = tx.Commit(); err != nil {
			return Status{}, err
		}
		return value, nil
	}
	stamp := timestamp(at)
	if request.Enabled {
		var connectionID, trackedRef, sourceOwner, bindingID, controllerID string
		var installationID, repositoryID int64
		err = tx.QueryRowContext(ctx, `SELECT s.connection_id,s.installation_id,s.repository_id,s.tracked_ref,c.owner_user_id,b.binding_id,b.controller_id
			FROM applications a
			JOIN application_sources s ON s.application_id=a.id AND s.source_type='github'
			JOIN source_connections c ON c.id=s.connection_id AND c.status='connected'
			JOIN relay_installation_bindings b ON b.owner_user_id=c.owner_user_id AND b.connection_id=s.connection_id
				AND b.installation_id=s.installation_id AND b.repository_id=s.repository_id AND b.state='authorized'
			JOIN relay_controllers rc ON rc.controller_id=b.controller_id AND rc.state='active'
			WHERE a.id=? AND c.owner_user_id=? AND a.archived_at IS NULL
			ORDER BY b.binding_id LIMIT 1`, request.ApplicationID, request.ActorUserID).Scan(&connectionID, &installationID, &repositoryID, &trackedRef, &sourceOwner, &bindingID, &controllerID)
		if errors.Is(err, sql.ErrNoRows) {
			return Status{}, ErrNotFound
		}
		if err != nil {
			return Status{}, err
		}
		if !validGitRef(trackedRef) || installationID <= 0 || repositoryID <= 0 || !validOpaqueID(connectionID) || !validOpaqueID(sourceOwner) || !canonicalUUID(bindingID) || !canonicalUUID(controllerID) {
			return Status{}, ErrState
		}
		if sourceOwner != request.ActorUserID {
			return Status{}, ErrUnauthorized
		}
		err = tx.QueryRowContext(ctx, `SELECT subscription_id FROM relay_controller_subscriptions
			WHERE owner_user_id=? AND binding_id=? AND controller_id=? AND installation_id=? AND repository_id=? AND tracked_ref=? AND state='active'`, sourceOwner, bindingID, controllerID, installationID, repositoryID, trackedRef).Scan(&subscriptionID)
		if errors.Is(err, sql.ErrNoRows) {
			subscriptionID.String, subscriptionID.Valid = uuid.NewString(), true
			_, err = tx.ExecContext(ctx, `INSERT INTO relay_controller_subscriptions(subscription_id,owner_user_id,binding_id,controller_id,installation_id,repository_id,tracked_ref,state,created_at,retired_at)
				VALUES(?,?,?,?,?,?,?,'active',?,NULL)`, subscriptionID.String, sourceOwner, bindingID, controllerID, installationID, repositoryID, trackedRef, stamp)
			if err != nil {
				return Status{}, classifyConstraint(err)
			}
		} else if err != nil {
			return Status{}, err
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE github_auto_deploy_configs
			SET revision=revision+1,enabled=1,source_owner_user_id=?,configured_by_user_id=?,controller_id=?,binding_id=?,subscription_id=?,updated_at=?
			WHERE application_id=? AND revision=? AND enabled=0`, sourceOwner, request.ActorUserID, controllerID, bindingID, subscriptionID.String, stamp, request.ApplicationID, revision)
		if updateErr != nil {
			return Status{}, classifyConstraint(updateErr)
		}
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
			if rowsErr != nil {
				return Status{}, rowsErr
			}
			return Status{}, ErrConflict
		}
	} else {
		if !owner.Valid || !subscriptionID.Valid {
			return Status{}, ErrState
		}
		var activeJobID, activeJobState sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT h.active_job_id,j.status
			FROM github_auto_deploy_heads h
			LEFT JOIN jobs j ON j.id=h.active_job_id
			WHERE h.application_id=? AND h.config_revision=?`, request.ApplicationID, revision).Scan(&activeJobID, &activeJobState); err != nil {
			return Status{}, err
		}
		if activeJobID.Valid && (!activeJobState.Valid || !terminalJobState(activeJobState.String)) {
			return Status{}, ErrApplicationBusy
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE github_auto_deploy_configs
			SET revision=revision+1,enabled=0,configured_by_user_id=?,controller_id=NULL,binding_id=NULL,subscription_id=NULL,updated_at=?
			WHERE application_id=? AND revision=? AND enabled=1`, request.ActorUserID, stamp, request.ApplicationID, revision)
		if updateErr != nil {
			return Status{}, classifyConstraint(updateErr)
		}
		if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
			if rowsErr != nil {
				return Status{}, rowsErr
			}
			return Status{}, ErrConflict
		}
		var users int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_auto_deploy_configs WHERE enabled=1 AND subscription_id=?`, subscriptionID.String).Scan(&users); err != nil {
			return Status{}, err
		}
		if users == 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE relay_controller_subscriptions SET state='retired',retired_at=? WHERE subscription_id=? AND state='active'`, stamp, subscriptionID.String); err != nil {
				return Status{}, classifyConstraint(err)
			}
		}
	}
	value, err := loadStatusTx(ctx, tx, request.ApplicationID)
	if err != nil {
		return Status{}, err
	}
	if err = tx.Commit(); err != nil {
		return Status{}, err
	}
	return value, nil
}

// Each branch contributes only its earliest eligible candidate. The final
// sort therefore considers at most one row per due reason and cannot order an
// application by a future deadline when another reason is already due.
const claimDueACKCandidateSQL = `SELECT application_id,due_at FROM (
	SELECT h.application_id,a.received_at AS due_at
	FROM github_auto_deploy_heads AS h INDEXED BY github_auto_deploy_ack_live
	CROSS JOIN github_auto_deploy_configs AS c
	CROSS JOIN relay_controller_subscriptions AS s INDEXED BY relay_subscription_active_set
	CROSS JOIN relay_source_ack_heads AS a INDEXED BY relay_source_ack_active
	WHERE c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
	  AND s.controller_id=h.controller_id AND s.subscription_id=h.subscription_id AND s.state='active'
	  AND a.controller_id=h.controller_id AND a.subscription_id=h.subscription_id
	  AND a.generation>h.last_consumed_generation
	  AND NOT (h.state='paused' AND h.pause_code='source_access_lost')
	  AND (h.last_reconciled_at IS NULL OR h.last_reconciled_at<=?2)
	  AND (h.lease_token IS NULL OR h.lease_expires_at<=?1)
	ORDER BY a.received_at,h.application_id LIMIT 1
)`

const claimDueCandidateSQL = `WITH due_candidates(application_id,due_at) AS (
	SELECT application_id,due_at FROM (
		SELECT h.application_id,h.updated_at AS due_at
		FROM github_auto_deploy_heads AS h INDEXED BY github_auto_deploy_dispatch_due
		JOIN github_auto_deploy_configs AS c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		WHERE h.state='dispatching' AND (h.lease_token IS NULL OR h.lease_expires_at<=?1)
		ORDER BY h.updated_at,h.application_id LIMIT 1
	)
	UNION ALL
	SELECT application_id,due_at FROM (
		SELECT h.application_id,h.next_job_poll_at AS due_at
		FROM github_auto_deploy_heads AS h INDEXED BY github_auto_deploy_job_poll_due
		JOIN github_auto_deploy_configs AS c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		WHERE h.active_job_id IS NOT NULL AND h.state IN ('deploying','paused')
		  AND h.next_job_poll_at<=?1 AND (h.lease_token IS NULL OR h.lease_expires_at<=?1)
		ORDER BY h.next_job_poll_at,h.application_id LIMIT 1
	)
	UNION ALL
	` + claimDueACKCandidateSQL + `
	UNION ALL
	SELECT application_id,due_at FROM (
		SELECT h.application_id,h.updated_at AS due_at
		FROM github_auto_deploy_heads AS h INDEXED BY github_auto_deploy_unresolved_due
		JOIN github_auto_deploy_configs AS c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		WHERE h.latest_resolved_generation<h.last_consumed_generation
		  AND NOT (h.state='paused' AND h.pause_code='source_access_lost')
		  AND (h.last_reconciled_at IS NULL OR h.last_reconciled_at<=?2)
		  AND (h.lease_token IS NULL OR h.lease_expires_at<=?1)
		ORDER BY h.updated_at,h.application_id LIMIT 1
	)
	UNION ALL
	SELECT application_id,due_at FROM (
		SELECT h.application_id,h.next_reconcile_at AS due_at
		FROM github_auto_deploy_heads AS h INDEXED BY github_auto_deploy_reconcile_due
		JOIN github_auto_deploy_configs AS c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		WHERE h.next_reconcile_at IS NOT NULL AND h.next_reconcile_at<=?1
		  AND (h.last_reconciled_at IS NULL OR h.last_reconciled_at<=?2)
		  AND (h.lease_token IS NULL OR h.lease_expires_at<=?1)
		ORDER BY h.next_reconcile_at,h.application_id LIMIT 1
	)
	UNION ALL
	SELECT application_id,due_at FROM (
		SELECT h.application_id,h.next_retry_at AS due_at
		FROM github_auto_deploy_heads AS h INDEXED BY github_auto_deploy_retry_due
		JOIN github_auto_deploy_configs AS c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		WHERE h.state='retry_wait' AND h.next_retry_at<=?1
		  AND (h.last_reconciled_at IS NULL OR h.last_reconciled_at<=?2)
		  AND (h.lease_token IS NULL OR h.lease_expires_at<=?1)
		ORDER BY h.next_retry_at,h.application_id LIMIT 1
	)
)
SELECT h.application_id,h.config_revision,h.lease_fence
FROM due_candidates AS d
JOIN github_auto_deploy_heads AS h ON h.application_id=d.application_id
ORDER BY d.due_at,d.application_id LIMIT 1`

func (r *Repository) ClaimDue(ctx context.Context, token string, now time.Time, ttl time.Duration) (Status, WorkLease, error) {
	return r.claimDue(ctx, token, now, ttl, now)
}

// ClaimDueWithResolveCutoff leaves newer ACK generations unconsumed until the
// persisted authoritative-resolution cooldown is eligible.
func (r *Repository) ClaimDueWithResolveCutoff(ctx context.Context, token string, now time.Time, ttl time.Duration, resolveCutoff time.Time) (Status, WorkLease, error) {
	if resolveCutoff.IsZero() || resolveCutoff.After(now) || now.Sub(resolveCutoff) > 24*time.Hour {
		return Status{}, WorkLease{}, ErrInvalid
	}
	return r.claimDue(ctx, token, now, ttl, resolveCutoff)
}

func (r *Repository) claimDue(ctx context.Context, token string, now time.Time, ttl time.Duration, resolveCutoff time.Time) (Status, WorkLease, error) {
	if ctx == nil || !canonicalUUID(token) || now.IsZero() || resolveCutoff.IsZero() || ttl < time.Second || ttl > maxLeaseTTL {
		return Status{}, WorkLease{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Status{}, WorkLease{}, err
	}
	defer tx.Rollback()
	stamp, expires := timestamp(now), timestamp(now.Add(ttl))
	var applicationID string
	var revision, fence int64
	err = tx.QueryRowContext(ctx, claimDueCandidateSQL, stamp, timestamp(resolveCutoff)).Scan(&applicationID, &revision, &fence)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, WorkLease{}, ErrNotFound
	}
	if err != nil {
		return Status{}, WorkLease{}, err
	}
	if fence == math.MaxInt64 {
		return Status{}, WorkLease{}, ErrState
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET lease_fence=lease_fence+1,lease_token=?,lease_expires_at=?,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND (lease_token IS NULL OR lease_expires_at<=?)`, token, expires, stamp, applicationID, revision, fence, stamp)
	if err != nil {
		return Status{}, WorkLease{}, classifyConstraint(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return Status{}, WorkLease{}, rowsErr
		}
		return Status{}, WorkLease{}, ErrConflict
	}
	status, err := loadStatusTx(ctx, tx, applicationID)
	if err != nil {
		return Status{}, WorkLease{}, err
	}
	if err = tx.Commit(); err != nil {
		return Status{}, WorkLease{}, err
	}
	lease := WorkLease{ApplicationID: applicationID, ConfigRevision: uint64(revision), Fence: uint64(fence + 1), Token: token, ExpiresAt: now.Add(ttl).UTC()}
	return status, lease, nil
}

func (r *Repository) ReleaseLease(ctx context.Context, lease WorkLease, at time.Time) error {
	if !validLease(lease) || at.IsZero() {
		return ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET lease_token=NULL,lease_expires_at=NULL,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND lease_expires_at=?`, timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, timestamp(lease.ExpiresAt))
	return mutationResult(result, err)
}

func (r *Repository) RecoverExpiredLeases(ctx context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit < 1 || limit > maxListLimit {
		return 0, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT application_id,lease_fence,lease_token FROM github_auto_deploy_heads
		WHERE lease_token IS NOT NULL AND lease_expires_at<=? ORDER BY lease_expires_at,application_id LIMIT ?`, timestamp(now), limit)
	if err != nil {
		return 0, err
	}
	type expired struct {
		applicationID string
		fence         int64
		token         string
	}
	var values []expired
	for rows.Next() {
		var value expired
		if err = rows.Scan(&value.applicationID, &value.fence, &value.token); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	recovered := 0
	for _, value := range values {
		result, updateErr := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET lease_token=NULL,lease_expires_at=NULL,updated_at=?
			WHERE application_id=? AND lease_fence=? AND lease_token=? AND lease_expires_at<=?`, timestamp(now), value.applicationID, value.fence, value.token, timestamp(now))
		if updateErr != nil {
			return 0, updateErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return 0, rowsErr
		}
		recovered += int(changed)
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return recovered, nil
}

// ForceReconcile makes a bounded set of enabled configurations immediately
// eligible for authoritative source reconciliation. It deliberately preserves
// coordinator state, including pauses, and does not disturb live leases.
func (r *Repository) ForceReconcile(ctx context.Context, now time.Time, limit int) (int, error) {
	return r.ForceReconcileEligible(ctx, now, now, limit, true)
}

// ForceReconcileEligible rearms only idle, unlinked work. Ready signals obey
// the persisted resolution cooldown; startup may rearm a crash-recovery row
// even while its prior lease remains live, without modifying that lease.
func (r *Repository) ForceReconcileEligible(ctx context.Context, now, resolveCutoff time.Time, limit int, startup bool) (int, error) {
	if ctx == nil || now.IsZero() || resolveCutoff.IsZero() || resolveCutoff.After(now) || now.Sub(resolveCutoff) > 24*time.Hour || limit < 1 || limit > maxListLimit {
		return 0, ErrInvalid
	}
	stamp := timestamp(now)
	result, err := r.db.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET next_reconcile_at=?,updated_at=?
		WHERE application_id IN (
			SELECT h.application_id
			FROM github_auto_deploy_heads h
			JOIN github_auto_deploy_configs c ON c.application_id=h.application_id AND c.revision=h.config_revision
			WHERE c.enabled=1 AND h.state='idle' AND h.active_job_id IS NULL
			  AND (h.next_reconcile_at IS NULL OR h.next_reconcile_at>?)
			  AND (? OR h.last_reconciled_at IS NULL OR h.last_reconciled_at<=?)
			ORDER BY h.application_id LIMIT ?
		)`, stamp, stamp, stamp, startup, timestamp(resolveCutoff), limit)
	if err != nil {
		return 0, classifyConstraint(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(changed), nil
}

// DeferReconcile records a bounded future reconciliation time without changing
// deployment or pause state. It prevents provider/access failures from turning
// a durable pause into a hot loop.
func (r *Repository) DeferReconcile(ctx context.Context, lease WorkLease, nextReconcileAt, at time.Time) error {
	if !validLease(lease) || at.IsZero() || !nextReconcileAt.After(at) || nextReconcileAt.Sub(at) > 24*time.Hour {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET resolving_generation=NULL,resolving_lease_fence=NULL,next_reconcile_at=?,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND state<>'disabled'`, timestamp(nextReconcileAt), timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token)
	if err = mutationResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

// CountRecentProviderFailures returns only a bounded aggregate. It does not
// expose job input, source identity, or provider diagnostics to the coordinator.
func (r *Repository) CountRecentProviderFailures(ctx context.Context, lease WorkLease, since, at time.Time) (uint32, error) {
	if !validLease(lease) || since.IsZero() || at.IsZero() || since.After(at) || at.Sub(since) > 24*time.Hour {
		return 0, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return 0, err
	}
	var count int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT 1 FROM jobs j JOIN github_auto_deploy_configs c ON c.application_id=j.resource_id
		WHERE j.resource_type='application' AND j.resource_id=? AND j.type='deploy' AND j.requested_by=c.source_owner_user_id
		  AND j.status IN ('failed','cancelled','interrupted','needs_attention') AND j.error_code='provider_unavailable'
		  AND j.updated_at>=? AND j.updated_at<=? LIMIT 500
	)`, lease.ApplicationID, timestamp(since), timestamp(at)).Scan(&count)
	if err != nil {
		return 0, err
	}
	if count < 0 || count > math.MaxUint32 {
		return 0, ErrState
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return uint32(count), nil
}

// PeekNewestACK returns the newest durable ACK without consuming it. It lets
// the coordinator decide whether a provider call is needed before reserving
// the durable cooldown.
func (r *Repository) PeekNewestACK(ctx context.Context, lease WorkLease, at time.Time) (SourceACKHead, error) {
	if !validLease(lease) || at.IsZero() {
		return SourceACKHead{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SourceACKHead{}, err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return SourceACKHead{}, err
	}
	head, err := loadNewestACKTx(ctx, tx, lease.ApplicationID)
	if err != nil {
		return SourceACKHead{}, err
	}
	if err = tx.Commit(); err != nil {
		return SourceACKHead{}, err
	}
	return head, nil
}

func loadNewestACKTx(ctx context.Context, tx *sql.Tx, applicationID string) (SourceACKHead, error) {
	var head SourceACKHead
	var generation int64
	var observed, received string
	err := tx.QueryRowContext(ctx, `SELECT a.controller_id,a.subscription_id,a.delivery_id,a.generation,a.installation_id,a.repository_id,a.tracked_ref,a.observed_sha,a.observed_at,a.received_at
		FROM github_auto_deploy_heads h JOIN relay_source_ack_heads a ON a.controller_id=h.controller_id AND a.subscription_id=h.subscription_id
		WHERE h.application_id=?`, applicationID).Scan(&head.ControllerID, &head.SubscriptionID, &head.DeliveryID, &generation, &head.InstallationID, &head.RepositoryID, &head.Ref, &head.ObservedSHA, &observed, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceACKHead{}, ErrNotFound
	}
	if err != nil {
		return SourceACKHead{}, err
	}
	head.Generation = uint64(generation)
	if head.ObservedAt, err = parseTimestamp(observed); err != nil {
		return SourceACKHead{}, err
	}
	if head.ReceivedAt, err = parseTimestamp(received); err != nil {
		return SourceACKHead{}, err
	}
	return head, nil
}

// ReserveResolve durably binds one authoritative provider call to the current
// configuration and lease without consuming any source generation.
func (r *Repository) ReserveResolve(ctx context.Context, lease WorkLease, generation uint64, at time.Time) error {
	if !validLease(lease) || generation > math.MaxInt64 || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	if err = requireResolveScope(ctx, tx, lease, generation); err != nil {
		if !errors.Is(err, ErrSourceAccessLost) {
			return err
		}
		if err = pauseSourceAccessLostTx(ctx, tx, lease, at); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrSourceAccessLost
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET resolving_generation=?,resolving_lease_fence=?,last_reconciled_at=?,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=?
		  AND last_consumed_generation<=? AND latest_resolved_generation<=?`, generation, lease.Fence, timestamp(at), timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, generation, generation)
	if err = mutationResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

// FinalizeResolvedHead consumes only the generation reserved before provider
// I/O. Scope and compact-head checks are repeated in the same transaction;
// newer generations remain pending and raw inbox retention is irrelevant.
func (r *Repository) FinalizeResolvedHead(ctx context.Context, lease WorkLease, generation uint64, sha string, nextReconcileAt, at time.Time) error {
	if !validLease(lease) || generation > math.MaxInt64 || !validSHA(sha) || nextReconcileAt.IsZero() || nextReconcileAt.Before(at) || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	if err = requireResolveScope(ctx, tx, lease, generation); err != nil {
		if !errors.Is(err, ErrSourceAccessLost) {
			return err
		}
		if err = pauseSourceAccessLostTx(ctx, tx, lease, at); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrSourceAccessLost
	}
	var consumed, currentGeneration int64
	var reservedGeneration, reservedFence sql.NullInt64
	var state, pausedSHA string
	var activeJob sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT last_consumed_generation,latest_resolved_generation,resolving_generation,resolving_lease_fence,state,COALESCE(paused_sha,''),active_job_id
		FROM github_auto_deploy_heads WHERE application_id=?`, lease.ApplicationID).Scan(&consumed, &currentGeneration, &reservedGeneration, &reservedFence, &state, &pausedSHA, &activeJob)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrState
	}
	if err != nil {
		return err
	}
	if !reservedGeneration.Valid || !reservedFence.Valid || uint64(reservedGeneration.Int64) != generation || uint64(reservedFence.Int64) != lease.Fence || generation < uint64(consumed) || generation < uint64(currentGeneration) {
		return ErrState
	}
	resumeNewHead := state == StatePaused && !activeJob.Valid && sha != pausedSHA
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET last_consumed_generation=?,latest_resolved_generation=?,latest_resolved_sha=?,resolving_generation=NULL,resolving_lease_fence=NULL,
			last_reconciled_at=?,next_reconcile_at=?,state=CASE WHEN ? THEN 'idle' ELSE state END,
			pause_code=CASE WHEN ? THEN NULL ELSE pause_code END,paused_sha=CASE WHEN ? THEN NULL ELSE paused_sha END,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=?
		  AND resolving_generation=? AND resolving_lease_fence=? AND last_consumed_generation=?`, generation, generation, sha, timestamp(at), timestamp(nextReconcileAt), resumeNewHead, resumeNewHead, resumeNewHead, timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, generation, lease.Fence, consumed)
	if err = mutationResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func requireResolveScope(ctx context.Context, tx *sql.Tx, lease WorkLease, generation uint64) error {
	return requireCurrentSourceScope(ctx, tx, lease.ApplicationID, lease.ConfigRevision, generation)
}

func requireCurrentSourceScope(ctx context.Context, tx *sql.Tx, applicationID string, configRevision, generation uint64) error {
	var compactGeneration sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT ack.generation
		FROM github_auto_deploy_heads h
		JOIN github_auto_deploy_configs c ON c.application_id=h.application_id AND c.revision=h.config_revision AND c.enabled=1
		JOIN applications a ON a.id=c.application_id AND a.archived_at IS NULL
		JOIN application_sources src ON src.application_id=c.application_id AND src.source_type='github'
		JOIN source_connections sc ON sc.id=src.connection_id AND sc.owner_user_id=c.source_owner_user_id AND sc.status='connected'
		JOIN relay_installation_bindings b ON b.binding_id=c.binding_id AND b.owner_user_id=c.source_owner_user_id
			AND b.connection_id=src.connection_id AND b.controller_id=c.controller_id
			AND b.installation_id=src.installation_id AND b.repository_id=src.repository_id AND b.state='authorized'
		JOIN relay_controllers rc ON rc.controller_id=c.controller_id AND rc.state='active'
		JOIN relay_controller_subscriptions sub ON sub.subscription_id=c.subscription_id AND sub.controller_id=c.controller_id
			AND sub.owner_user_id=c.source_owner_user_id AND sub.binding_id=c.binding_id
			AND sub.installation_id=src.installation_id AND sub.repository_id=src.repository_id
			AND sub.tracked_ref=src.tracked_ref AND sub.state='active'
		LEFT JOIN relay_source_ack_heads ack ON ack.controller_id=c.controller_id AND ack.subscription_id=c.subscription_id
		WHERE h.application_id=? AND h.config_revision=? AND h.controller_id=c.controller_id AND h.subscription_id=c.subscription_id`, applicationID, configRevision).Scan(&compactGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSourceAccessLost
	}
	if err != nil {
		return err
	}
	if generation > 0 && (!compactGeneration.Valid || uint64(compactGeneration.Int64) < generation) {
		return ErrState
	}
	return nil
}

func pauseSourceAccessLostTx(ctx context.Context, tx *sql.Tx, lease WorkLease, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='paused',pause_code='source_access_lost',paused_sha=COALESCE(active_sha,prepared_dispatch_sha,latest_resolved_sha,
			(SELECT resolved_sha FROM application_sources WHERE application_id=github_auto_deploy_heads.application_id AND source_type='github')),
			prepared_dispatch_sequence=NULL,prepared_dispatch_generation=NULL,prepared_dispatch_sha=NULL,
			resolving_generation=NULL,resolving_lease_fence=NULL,retry_attempt=0,next_retry_at=NULL,next_reconcile_at=NULL,
			next_job_poll_at=CASE WHEN active_job_id IS NOT NULL THEN ? ELSE NULL END,
			lease_fence=lease_fence+1,lease_token=NULL,lease_expires_at=NULL,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND lease_expires_at=?
		  AND lease_fence<? AND state<>'disabled'
		  AND COALESCE(active_sha,prepared_dispatch_sha,latest_resolved_sha,
			(SELECT resolved_sha FROM application_sources WHERE application_id=github_auto_deploy_heads.application_id AND source_type='github')) IS NOT NULL`,
		timestamp(at), timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, timestamp(lease.ExpiresAt), int64(math.MaxInt64))
	return mutationResult(result, err)
}

func (r *Repository) PrepareDispatch(ctx context.Context, lease WorkLease, at time.Time) (PreparedDispatch, error) {
	if !validLease(lease) || at.IsZero() {
		return PreparedDispatch{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PreparedDispatch{}, err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return PreparedDispatch{}, err
	}
	var state string
	var dispatchSequence, resolvedGeneration int64
	var resolvedSHA sql.NullString
	var preparedSequence, preparedGeneration sql.NullInt64
	var preparedSHA, nextRetry sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT state,dispatch_sequence,latest_resolved_generation,latest_resolved_sha,prepared_dispatch_sequence,prepared_dispatch_generation,prepared_dispatch_sha,next_retry_at
		FROM github_auto_deploy_heads WHERE application_id=?`, lease.ApplicationID).Scan(&state, &dispatchSequence, &resolvedGeneration, &resolvedSHA, &preparedSequence, &preparedGeneration, &preparedSHA, &nextRetry)
	if err != nil {
		return PreparedDispatch{}, err
	}
	if state == StateDispatching {
		if !preparedSequence.Valid || !preparedGeneration.Valid || !preparedSHA.Valid {
			return PreparedDispatch{}, ErrState
		}
		value := PreparedDispatch{ApplicationID: lease.ApplicationID, Sequence: uint64(preparedSequence.Int64), Generation: uint64(preparedGeneration.Int64), SHA: preparedSHA.String}
		if err = tx.Commit(); err != nil {
			return PreparedDispatch{}, err
		}
		return value, nil
	}
	if state != StateIdle && state != StateRetryWait || !resolvedSHA.Valid || dispatchSequence == math.MaxInt64 {
		return PreparedDispatch{}, ErrState
	}
	if state == StateRetryWait {
		dueAt, parseErr := parseNullableTimestamp(nextRetry)
		if parseErr != nil || dueAt == nil || dueAt.After(at) {
			return PreparedDispatch{}, ErrState
		}
	}
	sequence := dispatchSequence + 1
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='dispatching',dispatch_sequence=?,prepared_dispatch_sequence=?,prepared_dispatch_generation=?,prepared_dispatch_sha=?,retry_attempt=0,next_retry_at=NULL,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND state IN ('idle','retry_wait') AND active_job_id IS NULL`, sequence, sequence, resolvedGeneration, resolvedSHA.String, timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token)
	if err != nil {
		return PreparedDispatch{}, classifyConstraint(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return PreparedDispatch{}, rowsErr
		}
		return PreparedDispatch{}, ErrState
	}
	if err = tx.Commit(); err != nil {
		return PreparedDispatch{}, err
	}
	return PreparedDispatch{ApplicationID: lease.ApplicationID, Sequence: uint64(sequence), Generation: uint64(resolvedGeneration), SHA: resolvedSHA.String}, nil
}

func (r *Repository) LinkDispatchJob(ctx context.Context, lease WorkLease, sequence, generation uint64, jobID string, at time.Time) error {
	if !validLease(lease) || sequence == 0 || sequence > math.MaxInt64 || generation > math.MaxInt64 || !validOpaqueID(jobID) || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = r.LinkDispatchJobTx(ctx, tx, lease, sequence, generation, jobID, at); err != nil {
		return err
	}
	return tx.Commit()
}

// LinkDispatchJobTx validates and links an exact coordinator job using the
// caller's transaction. Existing linkage is idempotent only while the exact
// same job and dispatch remain actively deploying.
func (r *Repository) LinkDispatchJobTx(ctx context.Context, tx *sql.Tx, lease WorkLease, sequence, generation uint64, jobID string, at time.Time) error {
	if ctx == nil || tx == nil || !validLease(lease) || sequence == 0 || sequence > math.MaxInt64 || generation > math.MaxInt64 || !validOpaqueID(jobID) || at.IsZero() {
		return ErrInvalid
	}
	if err := requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	if err := requireCurrentSourceScope(ctx, tx, lease.ApplicationID, lease.ConfigRevision, 0); err != nil {
		return err
	}
	job, err := loadCoordinatorJobTx(ctx, tx, lease.ApplicationID, lease.ConfigRevision, sequence, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var state, activeJob, activeSHA, preparedSHA string
	var activeSequence, activeGeneration, preparedSequence, preparedGeneration sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT state,COALESCE(active_job_id,''),active_dispatch_sequence,active_generation,COALESCE(active_sha,''),prepared_dispatch_sequence,prepared_dispatch_generation,COALESCE(prepared_dispatch_sha,'')
		FROM github_auto_deploy_heads WHERE application_id=?`, lease.ApplicationID).Scan(&state, &activeJob, &activeSequence, &activeGeneration, &activeSHA, &preparedSequence, &preparedGeneration, &preparedSHA)
	if err != nil {
		return err
	}
	if state == StateDeploying && activeJob == jobID && activeSequence.Valid && activeGeneration.Valid && activeSequence.Int64 == int64(sequence) && activeGeneration.Int64 == int64(generation) {
		return nil
	}
	if state != StateDispatching || activeJob != "" || activeSHA != "" || !preparedSequence.Valid || !preparedGeneration.Valid || preparedSequence.Int64 != int64(sequence) || preparedGeneration.Int64 != int64(generation) || preparedSHA == "" {
		return ErrState
	}
	if err := validateDispatchRuntimeTx(ctx, tx, lease.ApplicationID, preparedSHA, job.ReleaseID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='deploying',pause_code=NULL,paused_sha=NULL,
			active_job_id=?,active_dispatch_sequence=prepared_dispatch_sequence,active_generation=prepared_dispatch_generation,active_sha=prepared_dispatch_sha,
			prepared_dispatch_sequence=NULL,prepared_dispatch_generation=NULL,prepared_dispatch_sha=NULL,next_job_poll_at=?,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND state='dispatching' AND prepared_dispatch_sequence=? AND prepared_dispatch_generation=?`, jobID, timestamp(at), timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, sequence, generation)
	if err != nil {
		return classifyConstraint(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return rowsErr
		}
		return ErrState
	}
	if err = applyJobStateTx(ctx, tx, lease.ApplicationID, job, at); err != nil {
		return err
	}
	return nil
}

func (r *Repository) RestartPreparedDispatch(ctx context.Context, lease WorkLease, sequence, generation uint64, at time.Time) error {
	if !validLease(lease) || sequence == 0 || sequence > math.MaxInt64 || generation > math.MaxInt64 || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='idle',prepared_dispatch_sequence=NULL,prepared_dispatch_generation=NULL,prepared_dispatch_sha=NULL,
			resolving_generation=NULL,resolving_lease_fence=NULL,next_reconcile_at=?,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND state='dispatching'
		  AND prepared_dispatch_sequence=? AND prepared_dispatch_generation=? AND active_job_id IS NULL`,
		timestamp(at), timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, sequence, generation)
	if err = mutationResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func validateDispatchRuntimeTx(ctx context.Context, tx *sql.Tx, applicationID, resolvedSHA, releaseID string) error {
	var planID, strategy string
	var planNumber int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(h.revision_id,''),h.revision_number,COALESCE(p.strategy,'')
		FROM deployment_plan_heads h LEFT JOIN deployment_plan_revisions p ON p.id=h.revision_id AND p.app_id=h.app_id AND p.revision_number=h.revision_number
		WHERE h.app_id=?`, applicationID).Scan(&planID, &planNumber, &strategy)
	if err != nil {
		return err
	}
	if planNumber == 0 || strategy == "compose" {
		if releaseID != "" {
			return ErrDispatchPreflightChanged
		}
		return nil
	}
	if strategy != "generated_node" || releaseID == "" {
		return ErrDispatchPreflightChanged
	}
	var found int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases r
		JOIN application_sources s ON s.application_id=r.app_id AND s.source_type='github'
		WHERE r.id=? AND r.app_id=? AND r.status='ready' AND r.workspace_state='ready' AND r.source_provider='github'
		  AND r.repository_id=s.repository_id AND r.tracked_ref=s.tracked_ref AND r.resolved_sha=?
		  AND r.deployment_plan_revision_id=? AND r.deployment_plan_revision_number=?`,
		releaseID, applicationID, resolvedSHA, planID, planNumber).Scan(&found)
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrDispatchPreflightChanged
	}
	return nil
}

// RefreshActiveJob derives coordinator state from jobs without mutating the
// jobs table. In particular, waiting_user remains authoritative until the
// existing jobs approval/resume path advances it.
func (r *Repository) RefreshActiveJob(ctx context.Context, lease WorkLease, at time.Time) (Status, error) {
	if !validLease(lease) || at.IsZero() {
		return Status{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return Status{}, err
	}
	var jobID string
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT active_job_id,active_dispatch_sequence FROM github_auto_deploy_heads WHERE application_id=? AND state IN ('deploying','paused') AND active_job_id IS NOT NULL`, lease.ApplicationID).Scan(&jobID, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrState
	}
	if err != nil {
		return Status{}, err
	}
	job, err := loadCoordinatorJobTx(ctx, tx, lease.ApplicationID, lease.ConfigRevision, uint64(sequence), jobID)
	if err != nil {
		return Status{}, ErrState
	}
	if err = applyJobStateTx(ctx, tx, lease.ApplicationID, job, at); err != nil {
		return Status{}, err
	}
	value, err := loadStatusTx(ctx, tx, lease.ApplicationID)
	if err != nil {
		return Status{}, err
	}
	if err = tx.Commit(); err != nil {
		return Status{}, err
	}
	return value, nil
}

func (r *Repository) Pause(ctx context.Context, lease WorkLease, code string, at time.Time) error {
	if !validLease(lease) || !validPauseCode(code) || at.IsZero() {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='paused',pause_code=?,paused_sha=COALESCE(active_sha,prepared_dispatch_sha,latest_resolved_sha,
			(SELECT resolved_sha FROM application_sources WHERE application_id=github_auto_deploy_heads.application_id AND source_type='github')),
			prepared_dispatch_sequence=NULL,prepared_dispatch_generation=NULL,prepared_dispatch_sha=NULL,resolving_generation=NULL,resolving_lease_fence=NULL,
			retry_attempt=0,next_retry_at=NULL,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND state IN ('idle','dispatching','deploying','retry_wait')
		  AND COALESCE(active_sha,prepared_dispatch_sha,latest_resolved_sha,
			(SELECT resolved_sha FROM application_sources WHERE application_id=github_auto_deploy_heads.application_id AND source_type='github')) IS NOT NULL`, code, timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token)
	if err = mutationResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ScheduleRetry(ctx context.Context, lease WorkLease, nextRetryAt, at time.Time) error {
	if !validLease(lease) || nextRetryAt.IsZero() || at.IsZero() || !nextRetryAt.After(at) || nextRetryAt.Sub(at) > 24*time.Hour {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireLease(ctx, tx, lease, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
		SET state='retry_wait',prepared_dispatch_sequence=NULL,prepared_dispatch_generation=NULL,prepared_dispatch_sha=NULL,
			resolving_generation=NULL,resolving_lease_fence=NULL,retry_attempt=retry_attempt+1,next_retry_at=?,pause_code=NULL,paused_sha=NULL,
			next_reconcile_at=CASE WHEN next_reconcile_at IS NULL OR next_reconcile_at<? THEN ? ELSE next_reconcile_at END,updated_at=?
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=?
		  AND (state IN ('idle','dispatching','retry_wait') OR (state='paused' AND pause_code='provider_unavailable'))
		  AND active_job_id IS NULL AND retry_attempt<1000`, timestamp(nextRetryAt), timestamp(nextRetryAt), timestamp(nextRetryAt), timestamp(at), lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token)
	if err = mutationResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) Resume(ctx context.Context, applicationID, actorUserID string, expectedRevision uint64, at time.Time) (Status, error) {
	if !validOpaqueID(applicationID) || !validOpaqueID(actorUserID) || expectedRevision > math.MaxInt64 || at.IsZero() {
		return Status{}, ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback()
	if err = requireAdministrator(ctx, tx, actorUserID); err != nil {
		return Status{}, err
	}
	var revision int64
	var owner, configuredBy string
	var state, pauseCode string
	var activeJob sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.revision,c.source_owner_user_id,c.configured_by_user_id,h.state,COALESCE(h.pause_code,''),h.active_job_id
		FROM github_auto_deploy_configs c JOIN github_auto_deploy_heads h ON h.application_id=c.application_id
		WHERE c.application_id=? AND c.enabled=1`, applicationID).Scan(&revision, &owner, &configuredBy, &state, &pauseCode, &activeJob)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, err
	}
	if expectedRevision != uint64(revision) {
		return Status{}, ErrConflict
	}
	if actorUserID != owner && actorUserID != configuredBy {
		return Status{}, ErrUnauthorized
	}
	if state != StatePaused {
		return Status{}, ErrState
	}
	if pauseCode == PauseSourceAccessLost {
		if err = requireCurrentSourceScope(ctx, tx, applicationID, uint64(revision), 0); err != nil {
			return Status{}, err
		}
	}
	nextState := StateIdle
	if activeJob.Valid {
		var jobState string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id=? AND type='deploy' AND resource_type='application' AND resource_id=?`, activeJob.String, applicationID).Scan(&jobState); err != nil {
			return Status{}, ErrState
		}
		if jobState == "waiting_user" || jobState == "needs_attention" {
			return Status{}, ErrState
		}
		if !activeJobState(jobState) {
			return Status{}, ErrState
		}
		nextState = StateDeploying
	}
	reconcileNow := pauseCode == PauseSourceAccessLost && !activeJob.Valid
	result, err := tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET state=?,pause_code=NULL,paused_sha=NULL,resolving_generation=NULL,resolving_lease_fence=NULL,
		next_job_poll_at=CASE WHEN active_job_id IS NOT NULL THEN ? ELSE NULL END,
		next_reconcile_at=CASE WHEN ? THEN ? ELSE next_reconcile_at END,
		lease_token=NULL,lease_expires_at=NULL,updated_at=? WHERE application_id=? AND config_revision=? AND state='paused'`, nextState, timestamp(at), reconcileNow, timestamp(at), timestamp(at), applicationID, revision)
	if err != nil {
		return Status{}, classifyConstraint(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return Status{}, rowsErr
		}
		return Status{}, ErrState
	}
	value, err := loadStatusTx(ctx, tx, applicationID)
	if err != nil {
		return Status{}, err
	}
	if err = tx.Commit(); err != nil {
		return Status{}, err
	}
	return value, nil
}

const statusSelect = `SELECT
	c.application_id,c.revision,c.enabled,COALESCE(c.source_owner_user_id,''),COALESCE(c.configured_by_user_id,''),
	COALESCE(c.controller_id,''),COALESCE(c.binding_id,''),COALESCE(c.subscription_id,''),
	COALESCE(s.connection_id,''),COALESCE(s.installation_id,0),COALESCE(s.repository_id,0),COALESCE(s.tracked_branch,''),COALESCE(s.tracked_ref,''),COALESCE(s.compose_path,''),
	CASE WHEN EXISTS (
		SELECT 1 FROM relay_controller_subscriptions rs
		JOIN relay_installation_bindings rb ON rb.binding_id=c.binding_id AND rb.controller_id=c.controller_id
		WHERE rs.subscription_id=c.subscription_id AND rs.controller_id=c.controller_id AND rs.state='active' AND rb.state='authorized'
	) THEN 1 ELSE 0 END,
	h.state,h.last_consumed_generation,h.latest_resolved_generation,COALESCE(h.latest_resolved_sha,''),h.dispatch_sequence,
	h.prepared_dispatch_sequence,h.prepared_dispatch_generation,COALESCE(h.prepared_dispatch_sha,''),
	COALESCE(h.active_job_id,''),h.active_dispatch_sequence,h.active_generation,COALESCE(h.active_sha,''),
	COALESCE(h.last_successful_deployed_sha,''),COALESCE(h.pause_code,''),COALESCE(h.paused_sha,''),h.retry_attempt,
	h.next_retry_at,h.next_job_poll_at,h.last_reconciled_at,h.next_reconcile_at,h.lease_fence,h.lease_expires_at,h.created_at,h.updated_at
	FROM github_auto_deploy_configs c
	JOIN github_auto_deploy_heads h ON h.application_id=c.application_id AND h.config_revision=c.revision
	JOIN application_sources s ON s.application_id=c.application_id`

type scanner interface{ Scan(...any) error }

func scanStatus(row scanner) (Status, error) {
	var value Status
	var revision, consumed, resolved, dispatch, leaseFence int64
	var preparedSequence, preparedGeneration, activeSequence, activeGeneration sql.NullInt64
	var enabled, sourceScopeActive int
	var retryAttempt int64
	var nextRetry, nextJobPoll, lastReconciled, nextReconcile, leaseExpires sql.NullString
	var created, updated string
	err := row.Scan(
		&value.ApplicationID, &revision, &enabled, &value.SourceOwnerUserID, &value.ConfiguredByUserID,
		&value.ControllerID, &value.BindingID, &value.SubscriptionID,
		&value.SourceConnectionID, &value.InstallationID, &value.RepositoryID, &value.TrackedBranch, &value.TrackedRef, &value.ComposePath, &sourceScopeActive,
		&value.State, &consumed, &resolved, &value.LatestResolvedSHA, &dispatch,
		&preparedSequence, &preparedGeneration, &value.PreparedDispatchSHA,
		&value.ActiveJobID, &activeSequence, &activeGeneration, &value.ActiveSHA,
		&value.LastSuccessfulDeployedSHA, &value.PauseCode, &value.PausedSHA, &retryAttempt,
		&nextRetry, &nextJobPoll, &lastReconciled, &nextReconcile, &leaseFence, &leaseExpires, &created, &updated,
	)
	if err != nil {
		return value, err
	}
	if revision < 0 || consumed < 0 || resolved < 0 || dispatch < 0 || retryAttempt < 0 || retryAttempt > math.MaxUint32 || leaseFence < 0 {
		return Status{}, ErrState
	}
	value.Revision = uint64(revision)
	value.Enabled = enabled == 1
	value.SourceScopeActive = sourceScopeActive == 1
	value.LastConsumedGeneration = uint64(consumed)
	value.LatestResolvedGeneration = uint64(resolved)
	value.DispatchSequence = uint64(dispatch)
	value.RetryAttempt = uint32(retryAttempt)
	value.LeaseFence = uint64(leaseFence)
	if preparedSequence.Valid {
		if !preparedGeneration.Valid || preparedSequence.Int64 < 0 || preparedGeneration.Int64 < 0 {
			return Status{}, ErrState
		}
		value.PreparedDispatchSequence = uint64(preparedSequence.Int64)
		value.PreparedDispatchGeneration = uint64(preparedGeneration.Int64)
	} else if preparedGeneration.Valid {
		return Status{}, ErrState
	}
	if activeSequence.Valid {
		if !activeGeneration.Valid || activeSequence.Int64 < 0 || activeGeneration.Int64 < 0 {
			return Status{}, ErrState
		}
		value.ActiveDispatchSequence = uint64(activeSequence.Int64)
		value.ActiveGeneration = uint64(activeGeneration.Int64)
	} else if activeGeneration.Valid {
		return Status{}, ErrState
	}
	if value.NextRetryAt, err = parseNullableTimestamp(nextRetry); err != nil {
		return Status{}, err
	}
	if value.NextJobPollAt, err = parseNullableTimestamp(nextJobPoll); err != nil {
		return Status{}, err
	}
	if value.LastReconciledAt, err = parseNullableTimestamp(lastReconciled); err != nil {
		return Status{}, err
	}
	if value.NextReconcileAt, err = parseNullableTimestamp(nextReconcile); err != nil {
		return Status{}, err
	}
	if value.LeaseExpiresAt, err = parseNullableTimestamp(leaseExpires); err != nil {
		return Status{}, err
	}
	if value.CreatedAt, err = parseTimestamp(created); err != nil {
		return Status{}, err
	}
	if value.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return Status{}, err
	}
	return value, nil
}

func loadStatusTx(ctx context.Context, tx *sql.Tx, applicationID string) (Status, error) {
	value, err := scanStatus(tx.QueryRowContext(ctx, statusSelect+` WHERE c.application_id=?`, applicationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	return value, err
}

func requireAdministrator(ctx context.Context, tx *sql.Tx, actor string) error {
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, actor).Scan(&role); errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	} else if err != nil {
		return err
	}
	if role != "administrator" {
		return ErrUnauthorized
	}
	return nil
}

func requireLease(ctx context.Context, tx *sql.Tx, lease WorkLease, at time.Time) error {
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_auto_deploy_heads
		WHERE application_id=? AND config_revision=? AND lease_fence=? AND lease_token=? AND lease_expires_at=? AND lease_expires_at>?`, lease.ApplicationID, lease.ConfigRevision, lease.Fence, lease.Token, timestamp(lease.ExpiresAt), timestamp(at)).Scan(&found); err != nil {
		return err
	}
	if found != 1 {
		return ErrState
	}
	return nil
}

type coordinatorJob struct {
	ID               string
	Status           string
	ErrorCode        string
	PauseDisposition string
	ReleaseID        string
}

func loadCoordinatorJobTx(ctx context.Context, tx *sql.Tx, applicationID string, configRevision, sequence uint64, jobID string) (coordinatorJob, error) {
	var value coordinatorJob
	var requestedBy, sourceOwner, input, idempotency string
	err := tx.QueryRowContext(ctx, `SELECT j.id,j.status,COALESCE(j.error_code,''),COALESCE(j.pause_disposition,''),COALESCE(j.requested_by,''),j.input_json,COALESCE(j.idempotency_key,''),c.source_owner_user_id
		FROM jobs j JOIN github_auto_deploy_configs c ON c.application_id=j.resource_id
		WHERE j.id=? AND j.type='deploy' AND j.resource_type='application' AND j.resource_id=?`, jobID, applicationID).Scan(&value.ID, &value.Status, &value.ErrorCode, &value.PauseDisposition, &requestedBy, &input, &idempotency, &sourceOwner)
	if err != nil {
		return coordinatorJob{}, err
	}
	if requestedBy == "" || requestedBy != sourceOwner || idempotency != DispatchIdempotencyKey(configRevision, sequence) {
		return coordinatorJob{}, ErrUnauthorized
	}
	decoded, decodeErr := jobs.DeploymentInputFor(jobs.Job{Type: "deploy", Input: []byte(input)})
	if decodeErr != nil || decoded.ConfigurationMode != jobs.ConfigurationCurrent {
		return coordinatorJob{}, ErrState
	}
	value.ReleaseID = decoded.ReleaseID
	if value.Status == "waiting_user" && !validJobPauseDisposition(value.PauseDisposition) {
		return coordinatorJob{}, ErrState
	}
	return value, nil
}

func applyJobStateTx(ctx context.Context, tx *sql.Tx, applicationID string, job coordinatorJob, at time.Time) error {
	stamp := timestamp(at)
	var result sql.Result
	var err error
	var currentState, currentPause, currentPausedSHA, activeSHA, latestSHA string
	if err = tx.QueryRowContext(ctx, `SELECT state,COALESCE(pause_code,''),COALESCE(paused_sha,''),COALESCE(active_sha,''),COALESCE(latest_resolved_sha,'')
		FROM github_auto_deploy_heads WHERE application_id=? AND active_job_id=?`, applicationID, job.ID).Scan(&currentState, &currentPause, &currentPausedSHA, &activeSHA, &latestSHA); err != nil {
		return err
	}
	sourceAccessOverlay := currentState == StatePaused && currentPause == PauseSourceAccessLost && validSHA(currentPausedSHA)
	switch job.Status {
	case "queued", "assigned", "running", "waiting_external":
		if sourceAccessOverlay {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET next_job_poll_at=?,updated_at=? WHERE application_id=? AND active_job_id=? AND state='paused' AND pause_code='source_access_lost'`, timestamp(at.Add(activeJobPollInterval)), stamp, applicationID, job.ID)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET state='deploying',pause_code=NULL,paused_sha=NULL,next_job_poll_at=?,updated_at=? WHERE application_id=? AND active_job_id=?`, timestamp(at.Add(activeJobPollInterval)), stamp, applicationID, job.ID)
		}
	case "waiting_user":
		if sourceAccessOverlay {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET next_job_poll_at=?,updated_at=? WHERE application_id=? AND active_job_id=? AND state='paused' AND pause_code='source_access_lost'`, timestamp(at.Add(waitingJobPollInterval)), stamp, applicationID, job.ID)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads SET state='paused',pause_code=?,paused_sha=active_sha,retry_attempt=0,next_retry_at=NULL,next_job_poll_at=?,updated_at=? WHERE application_id=? AND active_job_id=?`, autoDeployPauseForJob(job.PauseDisposition), timestamp(at.Add(waitingJobPollInterval)), stamp, applicationID, job.ID)
		}
	case "succeeded":
		var actualSHA string
		err = tx.QueryRowContext(ctx, `SELECT r.resolved_sha
			FROM deployments d
			JOIN releases r ON r.id=d.release_id AND r.app_id=d.app_id
			JOIN application_sources s ON s.application_id=d.app_id AND s.source_type='github'
			WHERE d.job_id=? AND d.app_id=? AND d.status='succeeded'
			  AND r.source_provider='github' AND r.repository_id=s.repository_id AND r.tracked_ref=s.tracked_ref`, job.ID, applicationID).Scan(&actualSHA)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !validSHA(actualSHA)) {
			if sourceAccessOverlay {
				result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
					SET active_job_id=NULL,active_dispatch_sequence=NULL,active_generation=NULL,active_sha=NULL,next_job_poll_at=NULL,updated_at=?
					WHERE application_id=? AND active_job_id=? AND state='paused' AND pause_code='source_access_lost'`, stamp, applicationID, job.ID)
			} else {
				result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
					SET state='paused',paused_sha=active_sha,pause_code='invalid_source',active_job_id=NULL,active_dispatch_sequence=NULL,active_generation=NULL,active_sha=NULL,
						retry_attempt=0,next_retry_at=NULL,next_job_poll_at=NULL,updated_at=? WHERE application_id=? AND active_job_id=?`, stamp, applicationID, job.ID)
			}
			break
		}
		if err != nil {
			return err
		}
		if sourceAccessOverlay {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
				SET active_job_id=NULL,active_dispatch_sequence=NULL,active_generation=NULL,active_sha=NULL,last_successful_deployed_sha=?,next_job_poll_at=NULL,updated_at=?
				WHERE application_id=? AND active_job_id=? AND state='paused' AND pause_code='source_access_lost'`, actualSHA, stamp, applicationID, job.ID)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
				SET state='idle',active_job_id=NULL,active_dispatch_sequence=NULL,active_generation=NULL,active_sha=NULL,
					last_successful_deployed_sha=?,pause_code=NULL,paused_sha=NULL,retry_attempt=0,next_retry_at=NULL,
					next_job_poll_at=NULL,next_reconcile_at=CASE WHEN latest_resolved_sha<>? THEN ? ELSE next_reconcile_at END,updated_at=?
				WHERE application_id=? AND active_job_id=?`, actualSHA, actualSHA, stamp, stamp, applicationID, job.ID)
		}
	case "failed", "cancelled", "interrupted", "needs_attention":
		if sourceAccessOverlay {
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
				SET active_job_id=NULL,active_dispatch_sequence=NULL,active_generation=NULL,active_sha=NULL,next_job_poll_at=NULL,updated_at=?
				WHERE application_id=? AND active_job_id=? AND state='paused' AND pause_code='source_access_lost'`, stamp, applicationID, job.ID)
		} else {
			var actualSHA sql.NullString
			if queryErr := tx.QueryRowContext(ctx, `SELECT r.resolved_sha FROM deployments d
				JOIN releases r ON r.id=d.release_id AND r.app_id=d.app_id
				JOIN application_sources s ON s.application_id=d.app_id AND s.source_type='github'
				WHERE d.job_id=? AND d.app_id=? AND r.source_provider='github' AND r.repository_id=s.repository_id AND r.tracked_ref=s.tracked_ref
				ORDER BY d.started_at DESC LIMIT 1`, job.ID, applicationID).Scan(&actualSHA); queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
				return queryErr
			}
			newerKnown := latestSHA != activeSHA || (actualSHA.Valid && validSHA(actualSHA.String) && latestSHA != actualSHA.String)
			pauseCode := pauseCodeForJobError(job.ErrorCode)
			result, err = tx.ExecContext(ctx, `UPDATE github_auto_deploy_heads
				SET state='paused',paused_sha=active_sha,pause_code=?,active_job_id=NULL,active_dispatch_sequence=NULL,active_generation=NULL,active_sha=NULL,
					retry_attempt=0,next_retry_at=NULL,next_job_poll_at=NULL,next_reconcile_at=CASE WHEN ? THEN ? ELSE next_reconcile_at END,updated_at=?
				WHERE application_id=? AND active_job_id=?`, pauseCode, newerKnown, stamp, stamp, applicationID, job.ID)
		}
	default:
		return ErrState
	}
	if err != nil {
		return classifyConstraint(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrState
	}
	return nil
}

func pauseCodeForJobError(code string) string {
	switch code {
	case "configuration_unavailable":
		return PauseMissingConfig
	case "source_access_lost":
		return PauseSourceAccessLost
	case "provider_unavailable":
		return PauseProviderUnavailable
	case "invalid_source":
		return PauseInvalidSource
	case "approval_required":
		return PauseApprovalRequired
	default:
		return PauseDeploymentFailed
	}
}

func validJobPauseDisposition(value string) bool {
	switch value {
	case jobs.PauseApprovalRequired, jobs.PauseMigrationApprovalRequired, jobs.PauseInsufficientReplacementCapacity:
		return true
	default:
		return false
	}
}

func autoDeployPauseForJob(value string) string {
	switch value {
	case jobs.PauseMigrationApprovalRequired:
		return PauseMigrationApprovalRequired
	case jobs.PauseInsufficientReplacementCapacity:
		return PauseInsufficientReplacementCapacity
	default:
		return PauseApprovalRequired
	}
}

func mutationResult(result sql.Result, err error) error {
	if err != nil {
		return classifyConstraint(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrState
	}
	return nil
}

func classifyConstraint(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "constraint failed") || strings.Contains(lower, "unique constraint") || strings.Contains(lower, "invalid auto-deploy") || strings.Contains(lower, "subscription cannot be retired") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func validLease(value WorkLease) bool {
	return validOpaqueID(value.ApplicationID) && value.ConfigRevision <= math.MaxInt64 && value.Fence > 0 && value.Fence <= math.MaxInt64 && canonicalUUID(value.Token) && !value.ExpiresAt.IsZero()
}

func validPauseCode(value string) bool {
	switch value {
	case PauseApprovalRequired, PauseMigrationApprovalRequired, PauseInsufficientReplacementCapacity, PauseDeploymentPlanReviewRequired,
		PauseDeploymentFailed, PauseMissingConfig, PauseSourceAccessLost, PauseInvalidSource, PauseProviderUnavailable, PauseRelayUnavailable:
		return true
	default:
		return false
	}
}

func activeJobState(value string) bool {
	switch value {
	case "queued", "assigned", "running", "waiting_external", "waiting_user":
		return true
	default:
		return false
	}
}

func terminalJobState(value string) bool {
	switch value {
	case "succeeded", "failed", "cancelled", "interrupted", "needs_attention":
		return true
	default:
		return false
	}
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

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validSHA(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validGitRef(value string) bool {
	if len(value) < 12 || len(value) > 255 || !strings.HasPrefix(value, "refs/heads/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "//") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".lock") || strings.Contains(value, ".lock/") {
		return false
	}
	branch := strings.TrimPrefix(value, "refs/heads/")
	if strings.HasPrefix(branch, ".") || strings.Contains(branch, "/.") {
		return false
	}
	return !strings.ContainsAny(value, " ~^:?*[\\")
}

const coordinationTimestampLayout = "2006-01-02T15:04:05.000000000Z"

func timestamp(value time.Time) string { return value.UTC().Format(coordinationTimestampLayout) }

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse auto-deploy timestamp: %w", err)
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
