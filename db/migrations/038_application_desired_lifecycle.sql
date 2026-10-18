CREATE TABLE application_desired_lifecycle_heads (
    app_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    desired_state TEXT CHECK (desired_state IS NULL OR desired_state IN ('running','stopped')),
    revision_number INTEGER NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    updated_at TEXT,
    CHECK (
        (revision_number = 0 AND desired_state IS NULL AND updated_at IS NULL)
        OR (revision_number > 0 AND desired_state IS NOT NULL AND updated_at IS NOT NULL)
    )
);

CREATE TABLE application_desired_lifecycle_transitions (
    app_id TEXT NOT NULL REFERENCES applications(id),
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    expected_revision INTEGER NOT NULL CHECK (expected_revision >= 0),
    previous_state TEXT CHECK (previous_state IS NULL OR previous_state IN ('running','stopped')),
    desired_state TEXT NOT NULL CHECK (desired_state IN ('running','stopped')),
    job_id TEXT NOT NULL REFERENCES jobs(id),
    action TEXT NOT NULL CHECK (action IN ('start','stop','restart')),
    actor_id TEXT NOT NULL CHECK (length(actor_id) > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    PRIMARY KEY(app_id, revision_number),
    UNIQUE(job_id, action),
    CHECK (revision_number = expected_revision + 1),
    CHECK (
        (action IN ('start','restart') AND desired_state = 'running')
        OR (action = 'stop' AND desired_state = 'stopped')
    )
);

CREATE TRIGGER application_desired_lifecycle_head_initial_insert
BEFORE INSERT ON application_desired_lifecycle_heads
WHEN NEW.revision_number <> 0 OR NEW.desired_state IS NOT NULL OR NEW.updated_at IS NOT NULL
    OR EXISTS (
        SELECT 1 FROM application_desired_lifecycle_heads h WHERE h.app_id = NEW.app_id
    )
    OR EXISTS (
        SELECT 1 FROM application_desired_lifecycle_transitions t WHERE t.app_id = NEW.app_id
    )
BEGIN SELECT RAISE(ABORT, 'application desired lifecycle head must be uninitialized'); END;

CREATE TRIGGER application_desired_lifecycle_transition_valid_insert
BEFORE INSERT ON application_desired_lifecycle_transitions
WHEN NOT EXISTS (
        SELECT 1 FROM application_desired_lifecycle_heads h
        WHERE h.app_id = NEW.app_id
          AND h.revision_number = NEW.expected_revision
          AND h.desired_state IS NEW.previous_state
    )
    OR NOT EXISTS (
        SELECT 1 FROM jobs j
        WHERE j.id = NEW.job_id
          AND j.type = NEW.action
          AND j.resource_type = 'application'
          AND j.resource_id = NEW.app_id
          AND j.requested_by = NEW.actor_id
    )
    OR EXISTS (
        SELECT 1 FROM application_desired_lifecycle_transitions t
        WHERE (t.job_id = NEW.job_id AND t.action = NEW.action)
           OR (t.app_id = NEW.app_id AND t.revision_number = NEW.revision_number)
    )
BEGIN SELECT RAISE(ABORT, 'invalid application desired lifecycle transition'); END;

CREATE TRIGGER application_desired_lifecycle_head_valid_update
BEFORE UPDATE ON application_desired_lifecycle_heads
WHEN NEW.app_id IS NOT OLD.app_id
    OR NEW.revision_number <> OLD.revision_number + 1
    OR NEW.desired_state IS NULL
    OR NEW.updated_at IS NULL
    OR NOT EXISTS (
        SELECT 1 FROM application_desired_lifecycle_transitions t
        WHERE t.app_id = OLD.app_id
          AND t.revision_number = NEW.revision_number
          AND t.expected_revision = OLD.revision_number
          AND t.previous_state IS OLD.desired_state
          AND t.desired_state = NEW.desired_state
          AND t.created_at = NEW.updated_at
    )
BEGIN SELECT RAISE(ABORT, 'invalid application desired lifecycle head advance'); END;

CREATE TRIGGER application_desired_lifecycle_transition_advance_head
AFTER INSERT ON application_desired_lifecycle_transitions
BEGIN
    UPDATE application_desired_lifecycle_heads
    SET desired_state = NEW.desired_state,
        revision_number = NEW.revision_number,
        updated_at = NEW.created_at
    WHERE app_id = NEW.app_id
      AND revision_number = NEW.expected_revision
      AND desired_state IS NEW.previous_state;
    SELECT CASE WHEN changes() <> 1
        THEN RAISE(ABORT, 'application desired lifecycle head advance failed') END;
END;

CREATE TRIGGER application_desired_lifecycle_transition_immutable
BEFORE UPDATE ON application_desired_lifecycle_transitions
BEGIN SELECT RAISE(ABORT, 'application desired lifecycle transitions are immutable'); END;

CREATE TRIGGER application_desired_lifecycle_transition_retain
BEFORE DELETE ON application_desired_lifecycle_transitions
BEGIN SELECT RAISE(ABORT, 'application desired lifecycle transitions are retained'); END;

CREATE TRIGGER application_desired_lifecycle_head_retain_while_application_exists
BEFORE DELETE ON application_desired_lifecycle_heads
WHEN EXISTS (SELECT 1 FROM applications a WHERE a.id = OLD.app_id)
BEGIN SELECT RAISE(ABORT, 'application desired lifecycle head is retained'); END;

INSERT INTO application_desired_lifecycle_heads(app_id)
SELECT id FROM applications;

CREATE TRIGGER application_desired_lifecycle_head_create
AFTER INSERT ON applications
BEGIN
    INSERT INTO application_desired_lifecycle_heads(app_id) VALUES(NEW.id);
END;
