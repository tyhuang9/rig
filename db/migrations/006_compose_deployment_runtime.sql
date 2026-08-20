ALTER TABLE deployments ADD COLUMN job_id TEXT REFERENCES jobs(id);
ALTER TABLE deployments ADD COLUMN configuration_mode TEXT NOT NULL DEFAULT 'original' CHECK (configuration_mode IN ('current','original'));
ALTER TABLE deployments ADD COLUMN actual_configuration_revision_id TEXT;
ALTER TABLE deployments ADD COLUMN actual_configuration_revision_number INTEGER NOT NULL DEFAULT 0 CHECK (actual_configuration_revision_number >= 0);
ALTER TABLE deployments ADD COLUMN diagnostic_code TEXT;

CREATE UNIQUE INDEX deployments_job ON deployments(job_id) WHERE job_id IS NOT NULL;
CREATE INDEX deployments_history ON deployments(app_id, started_at DESC, id DESC);

CREATE TABLE deployment_policy_findings (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    policy_version TEXT NOT NULL,
    capability TEXT NOT NULL,
    scope TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    disposition TEXT NOT NULL CHECK (disposition IN ('allowed','approval_required','rejected')),
    created_at TEXT NOT NULL,
    UNIQUE(deployment_id, fingerprint)
);
CREATE INDEX deployment_policy_findings_lookup ON deployment_policy_findings(deployment_id, disposition, fingerprint);

CREATE TABLE runtime_approvals (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES applications(id),
    policy_version TEXT NOT NULL,
    capability TEXT NOT NULL,
    scope TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    granted_by TEXT NOT NULL REFERENCES users(id),
    granted_at TEXT NOT NULL,
    revoked_by TEXT REFERENCES users(id),
    revoked_at TEXT,
    CHECK ((revoked_at IS NULL AND revoked_by IS NULL) OR (revoked_at IS NOT NULL AND revoked_by IS NOT NULL))
);
CREATE UNIQUE INDEX runtime_approvals_active ON runtime_approvals(app_id, fingerprint) WHERE revoked_at IS NULL;
CREATE INDEX runtime_approvals_history ON runtime_approvals(app_id, granted_at DESC, id DESC);

ALTER TABLE jobs ADD COLUMN pause_disposition TEXT;
ALTER TABLE job_events ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
CREATE INDEX job_events_attempt ON job_events(job_id, attempt, sequence);

CREATE TRIGGER deployment_linkage_valid_insert BEFORE INSERT ON deployments
WHEN (NEW.release_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM releases r WHERE r.id=NEW.release_id AND r.app_id=NEW.app_id))
  OR (NEW.job_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id=NEW.job_id AND j.type='deploy' AND j.resource_type='application' AND j.resource_id=NEW.app_id))
  OR (NEW.actual_configuration_revision_number=0 AND NEW.actual_configuration_revision_id IS NOT NULL)
  OR (NEW.actual_configuration_revision_number>0 AND NOT EXISTS (SELECT 1 FROM application_configuration_revisions c WHERE c.id=NEW.actual_configuration_revision_id AND c.app_id=NEW.app_id AND c.revision_number=NEW.actual_configuration_revision_number))
BEGIN SELECT RAISE(ABORT, 'invalid deployment linkage'); END;

CREATE TRIGGER deployment_linkage_valid_update BEFORE UPDATE OF app_id,release_id,job_id,actual_configuration_revision_id,actual_configuration_revision_number ON deployments
WHEN (NEW.release_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM releases r WHERE r.id=NEW.release_id AND r.app_id=NEW.app_id))
  OR (NEW.job_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id=NEW.job_id AND j.type='deploy' AND j.resource_type='application' AND j.resource_id=NEW.app_id))
  OR (NEW.actual_configuration_revision_number=0 AND NEW.actual_configuration_revision_id IS NOT NULL)
  OR (NEW.actual_configuration_revision_number>0 AND NOT EXISTS (SELECT 1 FROM application_configuration_revisions c WHERE c.id=NEW.actual_configuration_revision_id AND c.app_id=NEW.app_id AND c.revision_number=NEW.actual_configuration_revision_number))
BEGIN SELECT RAISE(ABORT, 'invalid deployment linkage'); END;

CREATE TRIGGER deployment_identity_immutable BEFORE UPDATE OF app_id,release_id,job_id,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number ON deployments
WHEN NEW.app_id IS NOT OLD.app_id OR NEW.job_id IS NOT OLD.job_id
  OR NEW.configuration_mode IS NOT OLD.configuration_mode
  OR (NEW.release_id IS NOT OLD.release_id AND NOT (OLD.status='preparing' AND OLD.release_id IS NULL AND NEW.release_id IS NOT NULL))
  OR ((NEW.actual_configuration_revision_id IS NOT OLD.actual_configuration_revision_id OR NEW.actual_configuration_revision_number IS NOT OLD.actual_configuration_revision_number)
      AND NOT (OLD.status='preparing' AND OLD.actual_configuration_revision_id IS NULL AND OLD.actual_configuration_revision_number=0))
BEGIN SELECT RAISE(ABORT, 'deployment identity is immutable'); END;

CREATE TRIGGER deployment_finding_valid_insert BEFORE INSERT ON deployment_policy_findings
WHEN length(NEW.policy_version)=0 OR length(NEW.capability)=0 OR length(NEW.scope)=0
  OR length(NEW.fingerprint)<>64 OR NEW.fingerprint GLOB '*[^0-9a-f]*'
BEGIN SELECT RAISE(ABORT, 'invalid deployment policy finding'); END;
CREATE TRIGGER deployment_finding_immutable BEFORE UPDATE ON deployment_policy_findings
BEGIN SELECT RAISE(ABORT, 'deployment policy findings are immutable'); END;

CREATE TRIGGER runtime_approval_valid_insert BEFORE INSERT ON runtime_approvals
WHEN length(NEW.policy_version)=0 OR length(NEW.capability)=0 OR length(NEW.scope)=0
  OR length(NEW.fingerprint)<>64 OR NEW.fingerprint GLOB '*[^0-9a-f]*'
BEGIN SELECT RAISE(ABORT, 'invalid runtime approval'); END;
CREATE TRIGGER runtime_approval_identity_immutable BEFORE UPDATE OF app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at ON runtime_approvals
BEGIN SELECT RAISE(ABORT, 'runtime approval identity is immutable'); END;
CREATE TRIGGER runtime_approval_no_delete BEFORE DELETE ON runtime_approvals
BEGIN SELECT RAISE(ABORT, 'runtime approvals are retained'); END;
CREATE TRIGGER runtime_approval_revoke_active BEFORE UPDATE OF revoked_at,revoked_by ON runtime_approvals
WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL AND EXISTS (
    SELECT 1 FROM deployments d
    JOIN deployment_policy_findings f ON f.deployment_id=d.id
    WHERE d.app_id=OLD.app_id AND d.status IN ('applying','waiting_health') AND f.fingerprint=OLD.fingerprint
)
BEGIN SELECT RAISE(ABORT, 'approval is in use by an active deployment'); END;

CREATE TRIGGER job_pause_valid_insert BEFORE INSERT ON jobs
WHEN (NEW.status='waiting_user' AND NEW.pause_disposition IS NULL)
  OR (NEW.status<>'waiting_user' AND NEW.pause_disposition IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'invalid job pause disposition'); END;
CREATE TRIGGER job_pause_valid_update BEFORE UPDATE OF status,pause_disposition ON jobs
WHEN (NEW.status='waiting_user' AND NEW.pause_disposition IS NULL)
  OR (NEW.status<>'waiting_user' AND NEW.pause_disposition IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'invalid job pause disposition'); END;

CREATE TRIGGER job_event_attempt_valid BEFORE INSERT ON job_events
WHEN NEW.attempt<>(SELECT attempt FROM jobs WHERE id=NEW.job_id)
  OR NEW.attempt<0
  OR (SELECT COUNT(*) FROM job_events WHERE job_id=NEW.job_id AND attempt=NEW.attempt)>=32
BEGIN SELECT RAISE(ABORT, 'invalid job event attempt'); END;
