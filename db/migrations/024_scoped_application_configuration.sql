ALTER TABLE application_configuration_revisions
    ADD COLUMN bundle_version INTEGER NOT NULL DEFAULT 1 CHECK (bundle_version IN (1,2));
ALTER TABLE application_configuration_revisions
    ADD COLUMN deployment_plan_revision_id TEXT;
ALTER TABLE application_configuration_revisions
    ADD COLUMN deployment_plan_revision_number INTEGER;

CREATE TABLE application_configuration_scoped_entries (
    revision_id TEXT NOT NULL REFERENCES application_configuration_revisions(id) ON DELETE CASCADE,
    phase TEXT NOT NULL CHECK (phase IN ('runtime','build','migration')),
    target_component TEXT NOT NULL,
    key TEXT NOT NULL,
    sensitivity TEXT NOT NULL CHECK (sensitivity IN ('public','secret')),
    PRIMARY KEY(revision_id, phase, target_component, key)
);

CREATE TRIGGER configuration_revision_scope_valid_insert BEFORE INSERT ON application_configuration_revisions
WHEN NOT (
    (NEW.bundle_version = 1 AND NEW.deployment_plan_revision_id IS NULL AND NEW.deployment_plan_revision_number IS NULL)
    OR
    (NEW.bundle_version = 2 AND NEW.deployment_plan_revision_id IS NOT NULL AND NEW.deployment_plan_revision_number > 0 AND EXISTS (
        SELECT 1 FROM deployment_plan_revisions p
        WHERE p.id=NEW.deployment_plan_revision_id
          AND p.app_id=NEW.app_id
          AND p.revision_number=NEW.deployment_plan_revision_number
          AND p.acceptance_status='accepted'
    ))
)
BEGIN SELECT RAISE(ABORT, 'invalid scoped configuration revision'); END;

CREATE TRIGGER configuration_scoped_entry_revision_insert BEFORE INSERT ON application_configuration_scoped_entries
WHEN NOT EXISTS (
    SELECT 1 FROM application_configuration_revisions r
    WHERE r.id=NEW.revision_id AND r.bundle_version=2
)
BEGIN SELECT RAISE(ABORT, 'invalid scoped configuration entry revision'); END;

CREATE TRIGGER configuration_legacy_entry_revision_insert BEFORE INSERT ON application_configuration_entries
WHEN EXISTS (
    SELECT 1 FROM application_configuration_revisions r
    WHERE r.id=NEW.revision_id AND r.bundle_version<>1
)
BEGIN SELECT RAISE(ABORT, 'invalid legacy configuration entry revision'); END;

CREATE TRIGGER configuration_scoped_entry_immutable_update BEFORE UPDATE ON application_configuration_scoped_entries
BEGIN SELECT RAISE(ABORT, 'scoped configuration entries are immutable'); END;
CREATE TRIGGER configuration_scoped_entry_immutable_delete BEFORE DELETE ON application_configuration_scoped_entries
BEGIN SELECT RAISE(ABORT, 'scoped configuration entries are immutable'); END;
