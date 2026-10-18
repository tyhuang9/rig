CREATE TABLE application_deployment_lifecycle_bindings (
    job_id TEXT PRIMARY KEY REFERENCES jobs(id),
    app_id TEXT NOT NULL CHECK (length(app_id) > 0),
    desired_revision INTEGER NOT NULL CHECK (desired_revision >= 0),
    mode TEXT NOT NULL CHECK (mode IN ('legacy_uninitialized','running')),
    authorizing_lifecycle_job_id TEXT REFERENCES application_lifecycle_operations(job_id),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    CHECK (
        (mode='legacy_uninitialized' AND desired_revision=0 AND authorizing_lifecycle_job_id IS NULL)
        OR
        (mode='running' AND desired_revision>0 AND authorizing_lifecycle_job_id IS NOT NULL)
    )
);

-- This is the sole legacy adapter. It executes once while upgrading and does
-- not queue, wake, or otherwise rewrite retained jobs and their history.
INSERT INTO application_deployment_lifecycle_bindings(
    job_id,app_id,desired_revision,mode,authorizing_lifecycle_job_id,created_at
)
SELECT j.id,j.resource_id,0,'legacy_uninitialized',NULL,j.created_at
FROM jobs j
JOIN applications a ON a.id=j.resource_id
JOIN application_desired_lifecycle_heads h ON h.app_id=a.id
WHERE j.type='deploy'
  AND j.resource_type='application'
  AND length(j.resource_id)>0
  AND h.desired_state IS NULL
  AND h.revision_number=0
  AND h.updated_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM application_desired_lifecycle_transitions t
      WHERE t.app_id=j.resource_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM application_lifecycle_operations o
      WHERE o.app_id=j.resource_id
  );

-- After the migration seed, only a freshly queued attempt-zero deployment may
-- be bound. Canonical lifecycle graph validation remains mandatory in Go; the
-- trigger independently enforces exact SQL identity and completed authority.
CREATE TRIGGER application_deployment_lifecycle_binding_valid_insert
BEFORE INSERT ON application_deployment_lifecycle_bindings
WHEN EXISTS (
        SELECT 1 FROM application_deployment_lifecycle_bindings b
        WHERE b.job_id=NEW.job_id
    )
    OR NOT EXISTS (
        SELECT 1 FROM jobs j
        WHERE j.id=NEW.job_id
          AND j.type='deploy'
          AND j.resource_type='application'
          AND j.resource_id=NEW.app_id
          AND j.status='queued'
          AND j.phase='queued'
          AND j.attempt=0
          AND j.started_at IS NULL
          AND j.finished_at IS NULL
    )
    OR (
        NEW.mode='legacy_uninitialized'
        AND NOT EXISTS (
            SELECT 1
            FROM applications a
            JOIN application_desired_lifecycle_heads h ON h.app_id=a.id
            WHERE a.id=NEW.app_id
              AND h.desired_state IS NULL
              AND h.revision_number=0
              AND h.updated_at IS NULL
              AND NOT EXISTS (
                  SELECT 1 FROM application_desired_lifecycle_transitions t
                  WHERE t.app_id=a.id
              )
              AND NOT EXISTS (
                  SELECT 1 FROM application_lifecycle_operations o
                  WHERE o.app_id=a.id
              )
        )
    )
    OR (
        NEW.mode='running'
        AND NOT EXISTS (
            SELECT 1
            FROM applications a
            JOIN application_desired_lifecycle_heads h ON h.app_id=a.id
            JOIN application_lifecycle_operations o
              ON o.app_id=h.app_id AND o.desired_revision=h.revision_number
            JOIN application_desired_lifecycle_transitions t
              ON t.app_id=o.app_id
             AND t.revision_number=o.desired_revision
             AND t.job_id=o.job_id
             AND t.action=o.action
            JOIN jobs lifecycle_job ON lifecycle_job.id=o.job_id
            JOIN application_lifecycle_effect_receipts completion
              ON completion.job_id=o.job_id
             AND completion.boundary='complete'
             AND completion.component_name=''
             AND completion.outcome='complete_running'
            WHERE a.id=NEW.app_id
              AND h.desired_state='running'
              AND h.revision_number=NEW.desired_revision
              AND h.updated_at IS NOT NULL
              AND o.job_id=NEW.authorizing_lifecycle_job_id
              AND o.action IN ('start','restart')
              AND t.expected_revision=o.expected_revision
              AND t.desired_state='running'
              AND t.actor_id=o.actor_id
              AND lifecycle_job.type=o.action
              AND lifecycle_job.resource_type='application'
              AND lifecycle_job.resource_id=o.app_id
              AND lifecycle_job.requested_by=o.actor_id
        )
    )
BEGIN SELECT RAISE(ABORT, 'invalid application deployment lifecycle binding'); END;

CREATE TRIGGER application_deployment_lifecycle_binding_immutable
BEFORE UPDATE ON application_deployment_lifecycle_bindings
BEGIN SELECT RAISE(ABORT, 'application deployment lifecycle bindings are immutable'); END;

CREATE TRIGGER application_deployment_lifecycle_binding_retain
BEFORE DELETE ON application_deployment_lifecycle_bindings
BEGIN SELECT RAISE(ABORT, 'application deployment lifecycle bindings are retained'); END;
