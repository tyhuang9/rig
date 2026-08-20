CREATE TABLE application_configuration_heads (
    app_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    revision_id TEXT,
    revision_number INTEGER NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    updated_at TEXT,
    CHECK ((revision_number = 0 AND revision_id IS NULL AND updated_at IS NULL) OR (revision_number > 0 AND revision_id IS NOT NULL AND updated_at IS NOT NULL)),
    FOREIGN KEY(app_id, revision_id) REFERENCES application_configuration_revisions(app_id, id)
);

CREATE TABLE application_configuration_revisions (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    bundle_ref TEXT NOT NULL UNIQUE,
    created_by TEXT REFERENCES users(id),
    created_at TEXT NOT NULL,
    variable_count INTEGER NOT NULL CHECK (variable_count >= 0),
    secret_count INTEGER NOT NULL CHECK (secret_count >= 0),
    UNIQUE(app_id, revision_number),
    UNIQUE(app_id, id)
);

CREATE TABLE application_configuration_entries (
    revision_id TEXT NOT NULL REFERENCES application_configuration_revisions(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    sensitive INTEGER NOT NULL CHECK (sensitive IN (0,1)),
    PRIMARY KEY(revision_id, key)
);

CREATE TRIGGER configuration_head_valid_insert BEFORE INSERT ON application_configuration_heads
WHEN NEW.revision_number > 0 AND NOT EXISTS (
    SELECT 1 FROM application_configuration_revisions r WHERE r.id=NEW.revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid configuration head'); END;

CREATE TRIGGER configuration_head_valid_update BEFORE UPDATE ON application_configuration_heads
WHEN NEW.revision_number > 0 AND NOT EXISTS (
    SELECT 1 FROM application_configuration_revisions r WHERE r.id=NEW.revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid configuration head'); END;

CREATE TRIGGER configuration_revision_immutable BEFORE UPDATE ON application_configuration_revisions
BEGIN SELECT RAISE(ABORT, 'configuration revision is immutable'); END;
CREATE TRIGGER configuration_revision_retain BEFORE DELETE ON application_configuration_revisions
BEGIN SELECT RAISE(ABORT, 'configuration revisions are retained'); END;
CREATE TRIGGER configuration_entry_immutable_update BEFORE UPDATE ON application_configuration_entries
BEGIN SELECT RAISE(ABORT, 'configuration entry is immutable'); END;
CREATE TRIGGER configuration_entry_immutable_delete BEFORE DELETE ON application_configuration_entries
BEGIN SELECT RAISE(ABORT, 'configuration entries are immutable'); END;

INSERT INTO application_configuration_heads(app_id) SELECT id FROM applications;
CREATE TRIGGER application_configuration_head_create AFTER INSERT ON applications
BEGIN INSERT INTO application_configuration_heads(app_id) VALUES(NEW.id); END;

ALTER TABLE releases ADD COLUMN configuration_revision_id TEXT;
ALTER TABLE releases ADD COLUMN configuration_revision_number INTEGER NOT NULL DEFAULT 0;

DROP INDEX releases_ready_snapshot;
CREATE UNIQUE INDEX releases_ready_snapshot ON releases(app_id, repository_id, resolved_sha, compose_path, configuration_revision_number)
    WHERE workspace_state = 'ready';

CREATE TRIGGER release_configuration_valid_insert BEFORE INSERT ON releases
WHEN NEW.configuration_revision_number < 0
  OR (NEW.configuration_revision_number = 0 AND NEW.configuration_revision_id IS NOT NULL)
  OR (NEW.configuration_revision_number > 0 AND NOT EXISTS (
      SELECT 1 FROM application_configuration_revisions r WHERE r.id=NEW.configuration_revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.configuration_revision_number
  ))
BEGIN SELECT RAISE(ABORT, 'invalid release configuration'); END;

CREATE TRIGGER release_configuration_valid_update BEFORE UPDATE OF app_id, configuration_revision_id, configuration_revision_number ON releases
WHEN NEW.configuration_revision_number < 0
  OR (NEW.configuration_revision_number = 0 AND NEW.configuration_revision_id IS NOT NULL)
  OR (NEW.configuration_revision_number > 0 AND NOT EXISTS (
      SELECT 1 FROM application_configuration_revisions r WHERE r.id=NEW.configuration_revision_id AND r.app_id=NEW.app_id AND r.revision_number=NEW.configuration_revision_number
  ))
BEGIN SELECT RAISE(ABORT, 'invalid release configuration'); END;

CREATE TRIGGER release_configuration_immutable BEFORE UPDATE OF configuration_revision_id, configuration_revision_number ON releases
WHEN NOT (NEW.configuration_revision_id IS OLD.configuration_revision_id AND NEW.configuration_revision_number=OLD.configuration_revision_number)
BEGIN SELECT RAISE(ABORT, 'release configuration is immutable'); END;
