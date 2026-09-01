DROP TRIGGER IF EXISTS github_auto_deploy_head_config_fence;
DROP TRIGGER IF EXISTS github_auto_deploy_config_seed_head;
DROP TRIGGER IF EXISTS github_auto_deploy_config_reset_head;
DROP TRIGGER IF EXISTS github_auto_deploy_subscription_retire_locked;
DROP TRIGGER IF EXISTS github_auto_deploy_head_monotonic;
DROP TRIGGER IF EXISTS github_auto_deploy_head_job_scope;
DROP TRIGGER IF EXISTS github_auto_deploy_head_no_delete;
DROP TRIGGER IF EXISTS github_auto_deploy_resolution_reservation_guard;
DROP TRIGGER IF EXISTS github_auto_deploy_config_clear_resolution_reservation;
DROP TRIGGER IF EXISTS github_auto_deploy_access_loss_clear_resolution_reservation;

CREATE TABLE github_auto_deploy_heads_v2 (
    application_id TEXT PRIMARY KEY REFERENCES github_auto_deploy_configs(application_id) ON DELETE CASCADE,
    config_revision INTEGER NOT NULL CHECK (config_revision >= 0),
    controller_id TEXT,
    subscription_id TEXT,
    last_consumed_generation INTEGER NOT NULL DEFAULT 0 CHECK (last_consumed_generation >= 0),
    latest_resolved_generation INTEGER NOT NULL DEFAULT 0 CHECK (latest_resolved_generation >= 0),
    latest_resolved_sha TEXT,
    dispatch_sequence INTEGER NOT NULL DEFAULT 0 CHECK (dispatch_sequence >= 0),
    prepared_dispatch_sequence INTEGER,
    prepared_dispatch_generation INTEGER,
    prepared_dispatch_sha TEXT,
    active_job_id TEXT REFERENCES jobs(id),
    active_dispatch_sequence INTEGER,
    active_generation INTEGER,
    active_sha TEXT,
    last_successful_deployed_sha TEXT,
    state TEXT NOT NULL CHECK (state IN ('disabled','idle','dispatching','deploying','paused','retry_wait')),
    pause_code TEXT CHECK (pause_code IS NULL OR pause_code IN (
        'approval_required','migration_approval_required','insufficient_replacement_capacity','deployment_plan_review_required',
        'deployment_failed','missing_configuration','source_access_lost','invalid_source','provider_unavailable','relay_unavailable'
    )),
    paused_sha TEXT,
    retry_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt BETWEEN 0 AND 1000),
    next_retry_at TEXT,
    next_job_poll_at TEXT,
    last_reconciled_at TEXT,
    next_reconcile_at TEXT,
    lease_fence INTEGER NOT NULL DEFAULT 0 CHECK (lease_fence >= 0),
    lease_token TEXT,
    lease_expires_at TEXT,
    resolving_generation INTEGER CHECK (resolving_generation IS NULL OR resolving_generation >= 0),
    resolving_lease_fence INTEGER,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((resolving_generation IS NULL AND resolving_lease_fence IS NULL)
        OR (resolving_generation IS NOT NULL AND resolving_lease_fence IS NOT NULL AND resolving_lease_fence > 0)),
    CHECK ((controller_id IS NULL AND subscription_id IS NULL) OR (controller_id IS NOT NULL AND subscription_id IS NOT NULL)),
    CHECK ((latest_resolved_sha IS NULL AND latest_resolved_generation=0) OR (latest_resolved_sha IS NOT NULL AND latest_resolved_generation<=last_consumed_generation)),
    CHECK (latest_resolved_sha IS NULL OR (length(latest_resolved_sha)=40 AND lower(latest_resolved_sha)=latest_resolved_sha AND latest_resolved_sha NOT GLOB '*[^0-9a-f]*')),
    CHECK ((prepared_dispatch_sequence IS NULL AND prepared_dispatch_generation IS NULL AND prepared_dispatch_sha IS NULL)
        OR (prepared_dispatch_sequence>0 AND prepared_dispatch_sequence<=dispatch_sequence AND prepared_dispatch_generation>=0 AND prepared_dispatch_sha IS NOT NULL)),
    CHECK (prepared_dispatch_sha IS NULL OR (length(prepared_dispatch_sha)=40 AND lower(prepared_dispatch_sha)=prepared_dispatch_sha AND prepared_dispatch_sha NOT GLOB '*[^0-9a-f]*')),
    CHECK ((active_job_id IS NULL AND active_dispatch_sequence IS NULL AND active_generation IS NULL AND active_sha IS NULL)
        OR (active_job_id IS NOT NULL AND active_dispatch_sequence>0 AND active_dispatch_sequence<=dispatch_sequence AND active_generation>=0 AND active_sha IS NOT NULL)),
    CHECK ((active_job_id IS NULL AND next_job_poll_at IS NULL) OR (active_job_id IS NOT NULL AND next_job_poll_at IS NOT NULL)),
    CHECK (active_sha IS NULL OR (length(active_sha)=40 AND lower(active_sha)=active_sha AND active_sha NOT GLOB '*[^0-9a-f]*')),
    CHECK (last_successful_deployed_sha IS NULL OR (length(last_successful_deployed_sha)=40 AND lower(last_successful_deployed_sha)=last_successful_deployed_sha AND last_successful_deployed_sha NOT GLOB '*[^0-9a-f]*')),
    CHECK ((pause_code IS NULL AND paused_sha IS NULL) OR (pause_code IS NOT NULL AND paused_sha IS NOT NULL)),
    CHECK (paused_sha IS NULL OR (length(paused_sha)=40 AND lower(paused_sha)=paused_sha AND paused_sha NOT GLOB '*[^0-9a-f]*')),
    CHECK (next_retry_at IS NULL OR next_retry_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (next_job_poll_at IS NULL OR next_job_poll_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (last_reconciled_at IS NULL OR last_reconciled_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (next_reconcile_at IS NULL OR next_reconcile_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK ((lease_token IS NULL AND lease_expires_at IS NULL) OR (lease_token IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_fence>0
        AND length(lease_token)=36 AND lower(lease_token)=lease_token AND lease_token GLOB '????????-????-????-????-????????????' AND lease_token NOT GLOB '*[^0-9a-f-]*')),
    CHECK (lease_expires_at IS NULL OR lease_expires_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (
        (state='disabled' AND controller_id IS NULL AND subscription_id IS NULL AND prepared_dispatch_sequence IS NULL AND active_job_id IS NULL
            AND last_consumed_generation=0 AND latest_resolved_generation=0 AND latest_resolved_sha IS NULL
            AND dispatch_sequence=0 AND last_successful_deployed_sha IS NULL AND pause_code IS NULL
            AND retry_attempt=0 AND next_retry_at IS NULL AND next_job_poll_at IS NULL AND last_reconciled_at IS NULL AND next_reconcile_at IS NULL AND lease_token IS NULL)
        OR
        (state='idle' AND controller_id IS NOT NULL AND prepared_dispatch_sequence IS NULL AND active_job_id IS NULL
            AND pause_code IS NULL AND retry_attempt=0 AND next_retry_at IS NULL AND next_job_poll_at IS NULL)
        OR
        (state='dispatching' AND controller_id IS NOT NULL AND prepared_dispatch_sequence IS NOT NULL AND active_job_id IS NULL
            AND pause_code IS NULL AND retry_attempt=0 AND next_retry_at IS NULL AND next_job_poll_at IS NULL)
        OR
        (state='deploying' AND controller_id IS NOT NULL AND prepared_dispatch_sequence IS NULL AND active_job_id IS NOT NULL
            AND pause_code IS NULL AND retry_attempt=0 AND next_retry_at IS NULL AND next_job_poll_at IS NOT NULL)
        OR
        (state='paused' AND controller_id IS NOT NULL AND prepared_dispatch_sequence IS NULL AND pause_code IS NOT NULL
            AND retry_attempt=0 AND next_retry_at IS NULL)
        OR
        (state='retry_wait' AND controller_id IS NOT NULL AND prepared_dispatch_sequence IS NULL AND active_job_id IS NULL
            AND pause_code IS NULL AND retry_attempt>0 AND next_retry_at IS NOT NULL AND next_job_poll_at IS NULL)
    ),
    FOREIGN KEY (subscription_id, controller_id) REFERENCES relay_controller_subscriptions(subscription_id, controller_id)
);

INSERT INTO github_auto_deploy_heads_v2(
    application_id,config_revision,controller_id,subscription_id,last_consumed_generation,latest_resolved_generation,
    latest_resolved_sha,dispatch_sequence,prepared_dispatch_sequence,prepared_dispatch_generation,prepared_dispatch_sha,
    active_job_id,active_dispatch_sequence,active_generation,active_sha,last_successful_deployed_sha,state,pause_code,
    paused_sha,retry_attempt,next_retry_at,next_job_poll_at,last_reconciled_at,next_reconcile_at,lease_fence,lease_token,
    lease_expires_at,resolving_generation,resolving_lease_fence,created_at,updated_at
)
SELECT
    application_id,config_revision,controller_id,subscription_id,last_consumed_generation,latest_resolved_generation,
    latest_resolved_sha,dispatch_sequence,prepared_dispatch_sequence,prepared_dispatch_generation,prepared_dispatch_sha,
    active_job_id,active_dispatch_sequence,active_generation,active_sha,last_successful_deployed_sha,state,pause_code,
    paused_sha,retry_attempt,next_retry_at,next_job_poll_at,last_reconciled_at,next_reconcile_at,lease_fence,lease_token,
    lease_expires_at,resolving_generation,resolving_lease_fence,created_at,updated_at
FROM github_auto_deploy_heads;

DROP TABLE github_auto_deploy_heads;
ALTER TABLE github_auto_deploy_heads_v2 RENAME TO github_auto_deploy_heads;

CREATE TRIGGER github_auto_deploy_config_seed_head
AFTER INSERT ON github_auto_deploy_configs
BEGIN
    INSERT INTO github_auto_deploy_heads(
        application_id,config_revision,state,created_at,updated_at
    ) VALUES(NEW.application_id,NEW.revision,'disabled',NEW.created_at,NEW.updated_at);
END;

CREATE TRIGGER github_auto_deploy_config_reset_head
AFTER UPDATE ON github_auto_deploy_configs
BEGIN
    UPDATE github_auto_deploy_heads
    SET config_revision=NEW.revision,
        controller_id=CASE WHEN NEW.enabled=1 THEN NEW.controller_id ELSE NULL END,
        subscription_id=CASE WHEN NEW.enabled=1 THEN NEW.subscription_id ELSE NULL END,
        last_consumed_generation=0,
        latest_resolved_generation=0,
        latest_resolved_sha=NULL,
        dispatch_sequence=0,
        prepared_dispatch_sequence=NULL,
        prepared_dispatch_generation=NULL,
        prepared_dispatch_sha=NULL,
        active_job_id=NULL,
        active_dispatch_sequence=NULL,
        active_generation=NULL,
        active_sha=NULL,
        last_successful_deployed_sha=CASE
            WHEN NEW.enabled=1 AND OLD.enabled=1 AND NEW.subscription_id=OLD.subscription_id THEN last_successful_deployed_sha
            ELSE NULL
        END,
        state=CASE WHEN NEW.enabled=1 THEN 'idle' ELSE 'disabled' END,
        pause_code=NULL,
        paused_sha=NULL,
        retry_attempt=0,
        next_retry_at=NULL,
        next_job_poll_at=NULL,
        last_reconciled_at=NULL,
        next_reconcile_at=CASE WHEN NEW.enabled=1 THEN NEW.updated_at ELSE NULL END,
        lease_token=NULL,
        lease_expires_at=NULL,
        updated_at=NEW.updated_at
    WHERE application_id=NEW.application_id;
END;

CREATE INDEX github_auto_deploy_dispatch_due ON github_auto_deploy_heads(updated_at, application_id) WHERE state='dispatching';
CREATE INDEX github_auto_deploy_job_poll_due ON github_auto_deploy_heads(next_job_poll_at, application_id) WHERE active_job_id IS NOT NULL AND state IN ('deploying','paused');
CREATE INDEX github_auto_deploy_unresolved_due ON github_auto_deploy_heads(updated_at, application_id) WHERE latest_resolved_generation<last_consumed_generation;
CREATE INDEX github_auto_deploy_reconcile_due ON github_auto_deploy_heads(next_reconcile_at, application_id) WHERE next_reconcile_at IS NOT NULL;
CREATE INDEX github_auto_deploy_retry_due ON github_auto_deploy_heads(next_retry_at, application_id) WHERE state='retry_wait';
CREATE INDEX github_auto_deploy_ack_live ON github_auto_deploy_heads(application_id, controller_id, subscription_id, last_consumed_generation) WHERE controller_id IS NOT NULL AND subscription_id IS NOT NULL;
CREATE UNIQUE INDEX github_auto_deploy_active_job ON github_auto_deploy_heads(active_job_id) WHERE active_job_id IS NOT NULL;

CREATE TRIGGER github_auto_deploy_head_config_fence
BEFORE UPDATE ON github_auto_deploy_heads
WHEN NEW.application_id<>OLD.application_id
  OR NEW.created_at<>OLD.created_at
  OR NEW.config_revision<>(SELECT revision FROM github_auto_deploy_configs WHERE application_id=NEW.application_id)
  OR NEW.controller_id IS NOT (SELECT controller_id FROM github_auto_deploy_configs WHERE application_id=NEW.application_id)
  OR NEW.subscription_id IS NOT (SELECT subscription_id FROM github_auto_deploy_configs WHERE application_id=NEW.application_id)
  OR (SELECT enabled FROM github_auto_deploy_configs WHERE application_id=NEW.application_id)=0
     AND NEW.state<>'disabled'
BEGIN SELECT RAISE(ABORT,'auto-deploy work head crossed its configuration fence'); END;

CREATE TRIGGER github_auto_deploy_head_monotonic
BEFORE UPDATE ON github_auto_deploy_heads
WHEN NEW.config_revision=OLD.config_revision AND (
    NEW.last_consumed_generation<OLD.last_consumed_generation
    OR NEW.latest_resolved_generation<OLD.latest_resolved_generation
    OR NEW.dispatch_sequence<OLD.dispatch_sequence
    OR NEW.dispatch_sequence>OLD.dispatch_sequence+1
    OR NEW.lease_fence<OLD.lease_fence
    OR NEW.lease_fence>OLD.lease_fence+1
    OR (NEW.lease_fence=OLD.lease_fence AND OLD.lease_token IS NULL AND NEW.lease_token IS NOT NULL)
    OR (NEW.lease_fence=OLD.lease_fence AND OLD.lease_token IS NOT NULL AND NEW.lease_token IS NOT NULL AND NEW.lease_token<>OLD.lease_token)
    OR (NEW.lease_fence=OLD.lease_fence+1 AND NEW.lease_token IS NULL
        AND NOT (NEW.state='paused' AND NEW.pause_code='source_access_lost' AND NEW.lease_expires_at IS NULL))
)
BEGIN SELECT RAISE(ABORT,'auto-deploy work head must advance monotonically'); END;

CREATE TRIGGER github_auto_deploy_head_job_scope
BEFORE UPDATE OF active_job_id ON github_auto_deploy_heads
WHEN NEW.active_job_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM jobs j
    WHERE j.id=NEW.active_job_id AND j.type='deploy'
      AND j.resource_type='application' AND j.resource_id=NEW.application_id
)
BEGIN SELECT RAISE(ABORT,'auto-deploy active job scope mismatch'); END;

CREATE TRIGGER github_auto_deploy_head_no_delete
BEFORE DELETE ON github_auto_deploy_heads
WHEN EXISTS (SELECT 1 FROM github_auto_deploy_configs WHERE application_id=OLD.application_id)
BEGIN SELECT RAISE(ABORT,'auto-deploy work head is retained'); END;

CREATE TRIGGER github_auto_deploy_resolution_reservation_guard
BEFORE UPDATE OF resolving_generation,resolving_lease_fence ON github_auto_deploy_heads
WHEN NEW.resolving_generation IS NOT NULL
  AND (
      NEW.resolving_lease_fence<>NEW.lease_fence
      OR NEW.lease_token IS NULL
      OR NEW.lease_expires_at IS NULL
  )
BEGIN SELECT RAISE(ABORT,'auto-deploy resolution reservation requires the current lease'); END;

CREATE TRIGGER github_auto_deploy_config_clear_resolution_reservation
AFTER UPDATE OF config_revision ON github_auto_deploy_heads
WHEN NEW.config_revision<>OLD.config_revision AND NEW.resolving_generation IS NOT NULL
BEGIN
    UPDATE github_auto_deploy_heads
    SET resolving_generation=NULL,resolving_lease_fence=NULL
    WHERE application_id=NEW.application_id AND config_revision=NEW.config_revision;
END;

CREATE TRIGGER github_auto_deploy_access_loss_clear_resolution_reservation
AFTER UPDATE OF state,pause_code ON github_auto_deploy_heads
WHEN NEW.state='paused' AND NEW.pause_code='source_access_lost' AND NEW.resolving_generation IS NOT NULL
BEGIN
    UPDATE github_auto_deploy_heads
    SET resolving_generation=NULL,resolving_lease_fence=NULL
    WHERE application_id=NEW.application_id;
END;

CREATE TRIGGER github_auto_deploy_subscription_retire_locked
BEFORE UPDATE OF state ON relay_controller_subscriptions
WHEN OLD.state='active' AND NEW.state='retired'
  AND EXISTS (
      SELECT 1
      FROM github_auto_deploy_configs c
      LEFT JOIN github_auto_deploy_heads h ON h.application_id=c.application_id
      WHERE c.subscription_id=OLD.subscription_id AND c.enabled=1
        AND (h.application_id IS NULL OR h.config_revision<>c.revision OR h.subscription_id<>c.subscription_id
             OR h.state<>'paused' OR h.pause_code<>'source_access_lost')
  )
BEGIN SELECT RAISE(ABORT,'active auto-deploy subscription cannot be retired'); END;
