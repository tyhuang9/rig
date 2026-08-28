CREATE TABLE relay_source_ack_heads (
    controller_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL CHECK (repository_id > 0),
    tracked_ref TEXT NOT NULL,
    observed_sha TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    PRIMARY KEY (controller_id, subscription_id),
    CHECK (length(delivery_id)=36 AND lower(delivery_id)=delivery_id AND delivery_id GLOB '????????-????-????-????-????????????' AND delivery_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (length(observed_sha)=40 AND lower(observed_sha)=observed_sha AND observed_sha NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(tracked_ref) BETWEEN 12 AND 255 AND substr(tracked_ref,1,11)='refs/heads/' AND tracked_ref NOT LIKE '%..%' AND tracked_ref NOT GLOB '*[[:space:]]*'),
    CHECK (received_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND julianday(substr(received_at,1,19) || 'Z') IS NOT NULL),
    FOREIGN KEY (subscription_id, controller_id) REFERENCES relay_controller_subscriptions(subscription_id, controller_id)
);

INSERT INTO relay_source_ack_heads(
    controller_id, subscription_id, delivery_id, generation,
    installation_id, repository_id, tracked_ref, observed_sha,
    observed_at, received_at
)
SELECT i.controller_id, i.subscription_id, i.delivery_id, i.generation,
       i.installation_id, i.repository_id, i.tracked_ref, i.observed_sha,
       i.observed_at,
       CASE
           WHEN length(i.received_at)=20 AND substr(i.received_at,-1)='Z'
               THEN substr(i.received_at,1,19) || '.000000000Z'
           WHEN length(i.received_at) BETWEEN 22 AND 30 AND instr(i.received_at,'.')=20 AND substr(i.received_at,-1)='Z'
               THEN substr(i.received_at,1,20)
                   || substr(i.received_at,21,length(i.received_at)-21)
                   || substr('000000000',1,30-length(i.received_at)) || 'Z'
           ELSE i.received_at
       END
FROM relay_source_event_inbox i
WHERE NOT EXISTS (
    SELECT 1 FROM relay_source_event_inbox newer
    WHERE newer.controller_id=i.controller_id
      AND newer.subscription_id=i.subscription_id
      AND newer.generation>i.generation
);

CREATE INDEX relay_source_ack_active ON relay_source_ack_heads(controller_id, subscription_id, generation);

DROP TRIGGER relay_source_inbox_no_delete;

DELETE FROM relay_source_event_inbox
WHERE rowid IN (
    SELECT rowid FROM (
        SELECT rowid,
               ROW_NUMBER() OVER (
                   PARTITION BY controller_id,subscription_id
                   ORDER BY generation DESC
               ) AS retention_rank
        FROM relay_source_event_inbox
    ) ranked
    WHERE retention_rank>32
);

CREATE TRIGGER relay_source_ack_head_identity_immutable
BEFORE UPDATE OF controller_id,subscription_id ON relay_source_ack_heads
BEGIN SELECT RAISE(ABORT,'relay source ACK head identity is immutable'); END;

CREATE TRIGGER relay_source_ack_head_advance
BEFORE UPDATE ON relay_source_ack_heads
WHEN NEW.generation<=OLD.generation
  OR NEW.installation_id<>OLD.installation_id
  OR NEW.repository_id<>OLD.repository_id
  OR NEW.tracked_ref<>OLD.tracked_ref
BEGIN SELECT RAISE(ABORT,'invalid relay source ACK head advance'); END;

CREATE TRIGGER relay_source_ack_head_no_delete
BEFORE DELETE ON relay_source_ack_heads
BEGIN SELECT RAISE(ABORT,'relay source ACK heads are retained'); END;

CREATE TRIGGER relay_source_inbox_head_guard
BEFORE DELETE ON relay_source_event_inbox
WHEN EXISTS (
    SELECT 1 FROM relay_source_ack_heads h
    WHERE h.controller_id=OLD.controller_id
      AND h.subscription_id=OLD.subscription_id
      AND h.delivery_id=OLD.delivery_id
      AND h.generation=OLD.generation
      AND h.installation_id=OLD.installation_id
      AND h.repository_id=OLD.repository_id
      AND h.tracked_ref=OLD.tracked_ref
      AND h.observed_sha=OLD.observed_sha
      AND h.observed_at=OLD.observed_at
)
BEGIN SELECT RAISE(ABORT,'current relay source ACK head cannot be pruned'); END;

CREATE TABLE github_auto_deploy_configs (
    application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    source_owner_user_id TEXT REFERENCES users(id),
    configured_by_user_id TEXT REFERENCES users(id),
    controller_id TEXT,
    binding_id TEXT,
    subscription_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (revision=0 AND enabled=0 AND source_owner_user_id IS NULL AND configured_by_user_id IS NULL
            AND controller_id IS NULL AND binding_id IS NULL AND subscription_id IS NULL)
        OR
        (revision>0 AND enabled=0 AND source_owner_user_id IS NOT NULL AND configured_by_user_id IS NOT NULL
            AND controller_id IS NULL AND binding_id IS NULL AND subscription_id IS NULL)
        OR
        (revision>0 AND enabled=1 AND source_owner_user_id IS NOT NULL AND configured_by_user_id IS NOT NULL
            AND controller_id IS NOT NULL AND binding_id IS NOT NULL AND subscription_id IS NOT NULL)
    ),
    FOREIGN KEY (binding_id, source_owner_user_id) REFERENCES relay_installation_bindings(binding_id, owner_user_id),
    FOREIGN KEY (subscription_id, controller_id) REFERENCES relay_controller_subscriptions(subscription_id, controller_id)
);

CREATE INDEX github_auto_deploy_owner ON github_auto_deploy_configs(source_owner_user_id, application_id);
CREATE INDEX github_auto_deploy_subscription ON github_auto_deploy_configs(subscription_id) WHERE enabled=1;

CREATE TABLE github_auto_deploy_heads (
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
    pause_code TEXT CHECK (pause_code IS NULL OR pause_code IN ('approval_required','deployment_failed','missing_configuration','source_access_lost','invalid_source','provider_unavailable','relay_unavailable')),
    paused_sha TEXT,
    retry_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt BETWEEN 0 AND 1000),
    next_retry_at TEXT,
    next_job_poll_at TEXT,
    last_reconciled_at TEXT,
    next_reconcile_at TEXT,
    lease_fence INTEGER NOT NULL DEFAULT 0 CHECK (lease_fence >= 0),
    lease_token TEXT,
    lease_expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
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

CREATE INDEX github_auto_deploy_dispatch_due ON github_auto_deploy_heads(updated_at, application_id) WHERE state='dispatching';
CREATE INDEX github_auto_deploy_job_poll_due ON github_auto_deploy_heads(next_job_poll_at, application_id) WHERE active_job_id IS NOT NULL AND state IN ('deploying','paused');
CREATE INDEX github_auto_deploy_unresolved_due ON github_auto_deploy_heads(updated_at, application_id) WHERE latest_resolved_generation<last_consumed_generation;
CREATE INDEX github_auto_deploy_reconcile_due ON github_auto_deploy_heads(next_reconcile_at, application_id) WHERE next_reconcile_at IS NOT NULL;
CREATE INDEX github_auto_deploy_retry_due ON github_auto_deploy_heads(next_retry_at, application_id) WHERE state='retry_wait';
CREATE INDEX github_auto_deploy_ack_live ON github_auto_deploy_heads(application_id, controller_id, subscription_id, last_consumed_generation) WHERE controller_id IS NOT NULL AND subscription_id IS NOT NULL;
CREATE UNIQUE INDEX github_auto_deploy_active_job ON github_auto_deploy_heads(active_job_id) WHERE active_job_id IS NOT NULL;

CREATE TRIGGER github_auto_deploy_config_validate_insert
BEFORE INSERT ON github_auto_deploy_configs
WHEN NEW.revision<>0 OR NEW.enabled<>0 OR NEW.source_owner_user_id IS NOT NULL OR NEW.configured_by_user_id IS NOT NULL
  OR NEW.controller_id IS NOT NULL OR NEW.binding_id IS NOT NULL OR NEW.subscription_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'invalid initial auto-deploy configuration'); END;

CREATE TRIGGER github_auto_deploy_config_validate_update
BEFORE UPDATE ON github_auto_deploy_configs
WHEN NEW.application_id<>OLD.application_id
  OR NEW.created_at<>OLD.created_at
  OR NEW.revision<>OLD.revision+1
  OR NEW.enabled=OLD.enabled
  OR NEW.updated_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
  OR NOT EXISTS (SELECT 1 FROM users u WHERE u.id=NEW.configured_by_user_id AND u.role='administrator')
  OR (NEW.enabled=0 AND NEW.source_owner_user_id IS NOT OLD.source_owner_user_id)
  OR (NEW.enabled=1 AND NOT EXISTS (
      SELECT 1
      FROM applications a
      JOIN application_sources s ON s.application_id=a.id
      JOIN source_connections c ON c.id=s.connection_id AND c.owner_user_id=NEW.source_owner_user_id AND c.status='connected'
      JOIN relay_installation_bindings b ON b.binding_id=NEW.binding_id
          AND b.owner_user_id=NEW.source_owner_user_id
          AND b.connection_id=s.connection_id
          AND b.controller_id=NEW.controller_id
          AND b.installation_id=s.installation_id
          AND b.repository_id=s.repository_id
          AND b.state='authorized'
      JOIN relay_controllers rc ON rc.controller_id=NEW.controller_id AND rc.state='active'
      JOIN relay_controller_subscriptions sub ON sub.subscription_id=NEW.subscription_id
          AND sub.owner_user_id=NEW.source_owner_user_id
          AND sub.binding_id=NEW.binding_id
          AND sub.controller_id=NEW.controller_id
          AND sub.installation_id=s.installation_id
          AND sub.repository_id=s.repository_id
          AND sub.tracked_ref=s.tracked_ref
          AND sub.state='active'
      WHERE a.id=NEW.application_id AND a.archived_at IS NULL AND s.source_type='github'
          AND length(s.tracked_ref) BETWEEN 12 AND 255 AND substr(s.tracked_ref,1,11)='refs/heads/'
  ))
BEGIN SELECT RAISE(ABORT,'invalid auto-deploy configuration transition'); END;

CREATE TRIGGER github_auto_deploy_config_identity_immutable
BEFORE DELETE ON github_auto_deploy_configs
WHEN EXISTS (SELECT 1 FROM applications WHERE id=OLD.application_id)
BEGIN SELECT RAISE(ABORT,'auto-deploy configuration is retained'); END;

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

CREATE TRIGGER github_auto_deploy_source_locked
BEFORE UPDATE OF application_id,source_type,connection_id,installation_id,repository_id,tracked_branch,tracked_ref,compose_path ON application_sources
WHEN EXISTS (SELECT 1 FROM github_auto_deploy_configs c WHERE c.application_id=OLD.application_id AND c.enabled=1)
BEGIN SELECT RAISE(ABORT,'disable auto-deploy before changing application source'); END;

CREATE TRIGGER github_auto_deploy_source_delete_locked
BEFORE DELETE ON application_sources
WHEN EXISTS (SELECT 1 FROM github_auto_deploy_configs c WHERE c.application_id=OLD.application_id AND c.enabled=1)
BEGIN SELECT RAISE(ABORT,'disable auto-deploy before deleting application source'); END;

CREATE TRIGGER github_auto_deploy_archive_locked
BEFORE UPDATE OF archived_at ON applications
WHEN NEW.archived_at IS NOT NULL AND OLD.archived_at IS NULL
  AND EXISTS (SELECT 1 FROM github_auto_deploy_configs c WHERE c.application_id=OLD.id AND c.enabled=1)
BEGIN SELECT RAISE(ABORT,'disable auto-deploy before archiving application'); END;

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

CREATE TRIGGER github_auto_deploy_application_default
AFTER INSERT ON applications
BEGIN
    INSERT INTO github_auto_deploy_configs(application_id,revision,enabled,created_at,updated_at)
    VALUES(NEW.id,0,0,NEW.created_at,NEW.updated_at);
END;

INSERT INTO github_auto_deploy_configs(application_id,revision,enabled,created_at,updated_at)
SELECT id,0,0,created_at,updated_at FROM applications;
