CREATE TABLE lan_gateway_profile_revisions (
    id TEXT PRIMARY KEY,
    revision_number INTEGER NOT NULL UNIQUE CHECK (revision_number > 0),
    operation_id TEXT NOT NULL UNIQUE CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'configure_lan_gateway'),
    selected_ipv4 TEXT NOT NULL CHECK (length(selected_ipv4) BETWEEN 7 AND 15),
    interface_id TEXT NOT NULL CHECK (length(interface_id) BETWEEN 1 AND 512),
    port_start INTEGER NOT NULL CHECK (port_start BETWEEN 8100 AND 8119),
    port_end INTEGER NOT NULL CHECK (port_end BETWEEN port_start AND 8119),
    spec_digest TEXT NOT NULL CHECK (length(spec_digest) = 64 AND spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,
    UNIQUE(id, revision_number)
);

CREATE TABLE lan_gateway_profile_heads (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision_id TEXT,
    revision_number INTEGER NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    updated_at TEXT,
    CHECK (
        (revision_number = 0 AND revision_id IS NULL AND updated_at IS NULL)
        OR (revision_number > 0 AND revision_id IS NOT NULL AND updated_at IS NOT NULL)
    ),
    FOREIGN KEY(revision_id, revision_number) REFERENCES lan_gateway_profile_revisions(id, revision_number)
);

INSERT INTO lan_gateway_profile_heads(singleton) VALUES(1);

CREATE TABLE lan_app_access_revisions (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    operation_id TEXT NOT NULL CHECK (length(operation_id) BETWEEN 1 AND 128),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'enable_lan_access'),
    allocation_id TEXT NOT NULL UNIQUE,
    allocated_port INTEGER NOT NULL CHECK (allocated_port BETWEEN 8100 AND 8119),
    gateway_profile_revision_id TEXT NOT NULL,
    gateway_profile_revision_number INTEGER NOT NULL CHECK (gateway_profile_revision_number > 0),
    spec_digest TEXT NOT NULL CHECK (length(spec_digest) = 64 AND spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,
    UNIQUE(app_id, revision_number),
    UNIQUE(app_id, id),
    UNIQUE(app_id, operation_id),
    FOREIGN KEY(gateway_profile_revision_id, gateway_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number)
);

CREATE TABLE lan_app_access_heads (
    app_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    revision_id TEXT,
    revision_number INTEGER NOT NULL DEFAULT 0 CHECK (revision_number >= 0),
    updated_at TEXT,
    CHECK (
        (revision_number = 0 AND revision_id IS NULL AND updated_at IS NULL)
        OR (revision_number > 0 AND revision_id IS NOT NULL AND updated_at IS NOT NULL)
    ),
    FOREIGN KEY(app_id, revision_id) REFERENCES lan_app_access_revisions(app_id, id)
);

INSERT INTO lan_app_access_heads(app_id) SELECT id FROM applications;

CREATE TRIGGER lan_app_access_head_create AFTER INSERT ON applications
BEGIN INSERT INTO lan_app_access_heads(app_id) VALUES(NEW.id); END;

CREATE TRIGGER lan_app_access_head_valid_insert BEFORE INSERT ON lan_app_access_heads
WHEN NEW.revision_number > 0 AND NOT EXISTS (
    SELECT 1 FROM lan_app_access_revisions r
    WHERE r.id = NEW.revision_id AND r.app_id = NEW.app_id AND r.revision_number = NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN application access head'); END;

CREATE TABLE lan_port_allocations (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    port INTEGER NOT NULL CHECK (port BETWEEN 8100 AND 8119),
    owner_operation_id TEXT NOT NULL UNIQUE CHECK (length(owner_operation_id) BETWEEN 1 AND 128),
    owner_revision_id TEXT UNIQUE REFERENCES lan_app_access_revisions(id),
    reservation_digest TEXT NOT NULL CHECK (length(reservation_digest) = 64 AND reservation_digest NOT GLOB '*[^0-9a-f]*'),
    gateway_profile_revision_id TEXT NOT NULL,
    gateway_profile_revision_number INTEGER NOT NULL CHECK (gateway_profile_revision_number > 0),
    state TEXT NOT NULL CHECK (state IN ('reserved','active','uncertain')),
    reserved_at TEXT NOT NULL,
    released_at TEXT,
    CHECK (released_at IS NULL OR (state = 'reserved' AND owner_revision_id IS NULL)),
    FOREIGN KEY(gateway_profile_revision_id, gateway_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number)
);

CREATE UNIQUE INDEX lan_port_allocations_live_port
ON lan_port_allocations(port) WHERE released_at IS NULL;
CREATE UNIQUE INDEX lan_port_allocations_live_app
ON lan_port_allocations(app_id) WHERE released_at IS NULL;
CREATE INDEX lan_port_allocations_owner
ON lan_port_allocations(app_id, owner_operation_id, id);

CREATE TRIGGER lan_app_access_archive_locked
BEFORE UPDATE OF archived_at ON applications
WHEN NEW.archived_at IS NOT NULL AND OLD.archived_at IS NULL
  AND EXISTS (
      SELECT 1 FROM lan_port_allocations a
      WHERE a.app_id = OLD.id AND a.released_at IS NULL
  )
BEGIN SELECT RAISE(ABORT, 'release LAN access before archiving application'); END;

CREATE TRIGGER lan_gateway_profile_administrator_insert BEFORE INSERT ON lan_gateway_profile_revisions
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.approved_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile approval requires an administrator'); END;

CREATE TRIGGER lan_app_access_administrator_insert BEFORE INSERT ON lan_app_access_revisions
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.approved_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'LAN application access approval requires an administrator'); END;

CREATE TRIGGER lan_gateway_profile_head_valid_update BEFORE UPDATE ON lan_gateway_profile_heads
WHEN NEW.revision_number <> OLD.revision_number + 1 OR NOT EXISTS (
    SELECT 1 FROM lan_gateway_profile_revisions r
    WHERE r.id = NEW.revision_id AND r.revision_number = NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN gateway profile head'); END;

CREATE TRIGGER lan_app_access_head_valid_update BEFORE UPDATE ON lan_app_access_heads
WHEN NEW.revision_number <> OLD.revision_number + 1 OR NOT EXISTS (
    SELECT 1 FROM lan_app_access_revisions r
    WHERE r.id = NEW.revision_id AND r.app_id = NEW.app_id AND r.revision_number = NEW.revision_number
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN application access head'); END;

CREATE TRIGGER lan_app_access_revision_allocation_insert BEFORE INSERT ON lan_app_access_revisions
WHEN NOT EXISTS (
    SELECT 1 FROM lan_port_allocations a
    WHERE a.id = NEW.allocation_id
      AND a.app_id = NEW.app_id
      AND a.port = NEW.allocated_port
      AND a.owner_operation_id = NEW.operation_id
      AND a.owner_revision_id IS NULL
      AND a.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.state = 'reserved'
      AND a.released_at IS NULL
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN application access allocation'); END;

CREATE TRIGGER lan_gateway_profile_revision_immutable_update BEFORE UPDATE ON lan_gateway_profile_revisions
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile revisions are immutable'); END;
CREATE TRIGGER lan_gateway_profile_revision_retain BEFORE DELETE ON lan_gateway_profile_revisions
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile revisions are retained'); END;
CREATE TRIGGER lan_app_access_revision_immutable_update BEFORE UPDATE ON lan_app_access_revisions
BEGIN SELECT RAISE(ABORT, 'LAN application access revisions are immutable'); END;
CREATE TRIGGER lan_app_access_revision_retain BEFORE DELETE ON lan_app_access_revisions
BEGIN SELECT RAISE(ABORT, 'LAN application access revisions are retained'); END;

CREATE TRIGGER lan_port_allocation_identity_immutable BEFORE UPDATE OF
    id, app_id, port, owner_operation_id, reservation_digest,
    gateway_profile_revision_id, gateway_profile_revision_number, reserved_at
ON lan_port_allocations
BEGIN SELECT RAISE(ABORT, 'LAN port allocation identity is immutable'); END;

CREATE TRIGGER lan_port_allocation_owner_update BEFORE UPDATE OF owner_revision_id ON lan_port_allocations
WHEN NOT (
    OLD.owner_revision_id IS NULL
    AND NEW.owner_revision_id IS NOT NULL
    AND OLD.state = 'reserved'
    AND OLD.released_at IS NULL
    AND EXISTS (
        SELECT 1 FROM lan_app_access_revisions r
        JOIN lan_app_access_heads h
          ON h.app_id = r.app_id AND h.revision_id = r.id AND h.revision_number = r.revision_number
        WHERE r.id = NEW.owner_revision_id
          AND r.app_id = OLD.app_id
          AND r.operation_id = OLD.owner_operation_id
          AND r.allocation_id = OLD.id
          AND r.allocated_port = OLD.port
          AND r.gateway_profile_revision_id = OLD.gateway_profile_revision_id
          AND r.gateway_profile_revision_number = OLD.gateway_profile_revision_number
    )
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN port allocation owner'); END;

CREATE TRIGGER lan_port_allocation_state_update BEFORE UPDATE OF state ON lan_port_allocations
WHEN NOT (
    (OLD.state = 'reserved' AND NEW.state IN ('active','uncertain')
        AND OLD.owner_revision_id IS NOT NULL AND OLD.released_at IS NULL)
    OR (OLD.state IN ('active','uncertain') AND NEW.state IN ('active','uncertain') AND OLD.state <> NEW.state
        AND OLD.owner_revision_id IS NOT NULL AND OLD.released_at IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN port allocation state transition'); END;

CREATE TRIGGER lan_port_allocation_release_update BEFORE UPDATE OF released_at ON lan_port_allocations
WHEN NOT (
    OLD.released_at IS NULL
    AND NEW.released_at IS NOT NULL
    AND OLD.state = 'reserved'
    AND OLD.owner_revision_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'only unowned LAN port reservations can be released'); END;

CREATE TRIGGER lan_port_allocation_retain BEFORE DELETE ON lan_port_allocations
BEGIN SELECT RAISE(ABORT, 'LAN port allocations are retained'); END;
