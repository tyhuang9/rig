CREATE TABLE deployment_plan_heads (
    app_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    revision_id TEXT,
    revision_number INTEGER NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    updated_at TEXT,
    CHECK ((revision_number = 0 AND revision_id IS NULL AND updated_at IS NULL) OR (revision_number > 0 AND revision_id IS NOT NULL AND updated_at IS NOT NULL)),
    FOREIGN KEY(app_id, revision_id) REFERENCES deployment_plan_revisions(app_id, id)
);

CREATE TABLE deployment_plan_revisions (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    bundle_ref TEXT NOT NULL UNIQUE,
    strategy TEXT NOT NULL CHECK (strategy IN ('generated_node','compose')),
    detector TEXT NOT NULL,
    detector_version TEXT NOT NULL,
    source_structural_fingerprint TEXT NOT NULL CHECK (length(source_structural_fingerprint) = 64 AND source_structural_fingerprint NOT GLOB '*[^0-9a-f]*'),
    canonical_digest TEXT NOT NULL CHECK (length(canonical_digest) = 64 AND canonical_digest NOT GLOB '*[^0-9a-f]*'),
    component_count INTEGER NOT NULL CHECK (component_count >= 0),
    field_provenance_count INTEGER NOT NULL CHECK (field_provenance_count >= 0),
    migration_evidence_digest TEXT NOT NULL CHECK (migration_evidence_digest = '' OR (length(migration_evidence_digest) = 64 AND migration_evidence_digest NOT GLOB '*[^0-9a-f]*')),
    revised_by TEXT NOT NULL REFERENCES users(id),
    revised_at TEXT NOT NULL,
    acceptance_status TEXT NOT NULL CHECK (acceptance_status = 'accepted'),
    accepted_by TEXT NOT NULL REFERENCES users(id),
    accepted_at TEXT NOT NULL,
    UNIQUE(app_id, revision_number),
    UNIQUE(app_id, id)
);

CREATE TRIGGER deployment_plan_head_valid_insert BEFORE INSERT ON deployment_plan_heads
WHEN NEW.revision_number > 0 AND NOT EXISTS (
    SELECT 1 FROM deployment_plan_revisions r WHERE r.id=NEW.revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid deployment plan head'); END;

CREATE TRIGGER deployment_plan_head_valid_update BEFORE UPDATE ON deployment_plan_heads
WHEN NEW.revision_number > 0 AND NOT EXISTS (
    SELECT 1 FROM deployment_plan_revisions r WHERE r.id=NEW.revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid deployment plan head'); END;

CREATE TRIGGER deployment_plan_revision_immutable BEFORE UPDATE ON deployment_plan_revisions
BEGIN SELECT RAISE(ABORT, 'deployment plan revisions are immutable'); END;
CREATE TRIGGER deployment_plan_revision_retain BEFORE DELETE ON deployment_plan_revisions
BEGIN SELECT RAISE(ABORT, 'deployment plan revisions are retained'); END;

INSERT INTO deployment_plan_heads(app_id) SELECT id FROM applications;
CREATE TRIGGER deployment_plan_head_create AFTER INSERT ON applications
BEGIN INSERT INTO deployment_plan_heads(app_id) VALUES(NEW.id); END;

ALTER TABLE releases ADD COLUMN deployment_plan_revision_id TEXT;
ALTER TABLE releases ADD COLUMN deployment_plan_revision_number INTEGER;

DROP INDEX releases_ready_snapshot;
CREATE UNIQUE INDEX releases_ready_snapshot ON releases(app_id, repository_id, resolved_sha, compose_path, configuration_revision_number, COALESCE(deployment_plan_revision_number, 0))
    WHERE workspace_state = 'ready';

CREATE TRIGGER release_deployment_plan_valid_insert BEFORE INSERT ON releases
WHEN NOT (
    (NEW.deployment_plan_revision_id IS NULL AND NEW.deployment_plan_revision_number IS NULL)
    OR (NEW.deployment_plan_revision_id IS NOT NULL AND NEW.deployment_plan_revision_number > 0 AND EXISTS (
        SELECT 1 FROM deployment_plan_revisions r WHERE r.id=NEW.deployment_plan_revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.deployment_plan_revision_number
    ))
)
BEGIN SELECT RAISE(ABORT, 'invalid release deployment plan'); END;

CREATE TRIGGER release_deployment_plan_valid_update BEFORE UPDATE OF app_id, deployment_plan_revision_id, deployment_plan_revision_number ON releases
WHEN NOT (
    (NEW.deployment_plan_revision_id IS NULL AND NEW.deployment_plan_revision_number IS NULL)
    OR (NEW.deployment_plan_revision_id IS NOT NULL AND NEW.deployment_plan_revision_number > 0 AND EXISTS (
        SELECT 1 FROM deployment_plan_revisions r WHERE r.id=NEW.deployment_plan_revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.deployment_plan_revision_number
    ))
)
BEGIN SELECT RAISE(ABORT, 'invalid release deployment plan'); END;

CREATE TRIGGER release_deployment_plan_immutable BEFORE UPDATE OF deployment_plan_revision_id, deployment_plan_revision_number ON releases
WHEN NOT (NEW.deployment_plan_revision_id IS OLD.deployment_plan_revision_id AND NEW.deployment_plan_revision_number IS OLD.deployment_plan_revision_number)
BEGIN SELECT RAISE(ABORT, 'release deployment plan is immutable'); END;
