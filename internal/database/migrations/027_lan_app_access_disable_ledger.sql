-- A disable approval is durable intent only. Migration 025 continues to own
-- allocation shape, state transitions, and the rule that an owned allocation
-- cannot be released. A later trusted route-removal slice must add any
-- terminal release representation without weakening those existing guards.
CREATE TABLE lan_app_access_disable_intents (
    operation_id TEXT PRIMARY KEY CHECK (length(operation_id) BETWEEN 1 AND 128),
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'disable_lan_access'),
    allocation_id TEXT NOT NULL UNIQUE REFERENCES lan_port_allocations(id),
    allocation_owner_operation_id TEXT NOT NULL CHECK (length(allocation_owner_operation_id) BETWEEN 1 AND 128),
    access_revision_id TEXT NOT NULL UNIQUE,
    access_revision_number INTEGER NOT NULL CHECK (access_revision_number > 0),
    allocated_port INTEGER NOT NULL CHECK (allocated_port BETWEEN 8100 AND 8119),
    gateway_profile_revision_id TEXT NOT NULL,
    gateway_profile_revision_number INTEGER NOT NULL CHECK (gateway_profile_revision_number > 0),
    spec_digest TEXT NOT NULL CHECK (length(spec_digest) = 64 AND spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,
    UNIQUE(app_id, operation_id),
    FOREIGN KEY(app_id, access_revision_id) REFERENCES lan_app_access_revisions(app_id, id),
    FOREIGN KEY(gateway_profile_revision_id, gateway_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number)
);

CREATE INDEX lan_app_access_disable_intents_app_history
ON lan_app_access_disable_intents(app_id, approved_at DESC, operation_id DESC);

CREATE TRIGGER lan_app_access_disable_administrator_insert
BEFORE INSERT ON lan_app_access_disable_intents
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.approved_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'LAN application access disable approval requires an administrator'); END;

CREATE TRIGGER lan_app_access_disable_exact_owner_insert
BEFORE INSERT ON lan_app_access_disable_intents
WHEN NOT EXISTS (
    SELECT 1
    FROM lan_app_access_heads h
    JOIN lan_app_access_revisions r
      ON r.app_id = h.app_id AND r.id = h.revision_id AND r.revision_number = h.revision_number
    JOIN lan_port_allocations a
      ON a.id = r.allocation_id AND a.app_id = r.app_id
    JOIN applications app
      ON app.id = r.app_id AND app.archived_at IS NULL
    WHERE h.app_id = NEW.app_id
      AND r.id = NEW.access_revision_id
      AND r.revision_number = NEW.access_revision_number
      AND r.operation_id = NEW.allocation_owner_operation_id
      AND r.allocated_port = NEW.allocated_port
      AND r.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND r.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.id = NEW.allocation_id
      AND a.owner_operation_id = NEW.allocation_owner_operation_id
      AND a.owner_revision_id = NEW.access_revision_id
      AND a.port = NEW.allocated_port
      AND a.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.released_at IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN application access disable requires the exact current owner'); END;

CREATE TRIGGER lan_port_allocation_disable_intent_freeze
BEFORE UPDATE OF state, owner_revision_id, released_at ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_intents d
    WHERE d.allocation_id = OLD.id
      AND d.app_id = OLD.app_id
      AND d.allocation_owner_operation_id = OLD.owner_operation_id
      AND d.access_revision_id = OLD.owner_revision_id
      AND d.allocated_port = OLD.port
      AND d.gateway_profile_revision_id = OLD.gateway_profile_revision_id
      AND d.gateway_profile_revision_number = OLD.gateway_profile_revision_number
)
BEGIN SELECT RAISE(ABORT, 'LAN port allocation is frozen by disable intent'); END;

CREATE TRIGGER lan_app_access_disable_intent_immutable_update
BEFORE UPDATE ON lan_app_access_disable_intents
BEGIN SELECT RAISE(ABORT, 'LAN application access disable intents are immutable'); END;

CREATE TRIGGER lan_app_access_disable_intent_retain
BEFORE DELETE ON lan_app_access_disable_intents
BEGIN SELECT RAISE(ABORT, 'LAN application access disable intents are retained'); END;

CREATE TRIGGER lan_app_access_disable_audit_immutable_update
BEFORE UPDATE ON audit_events
WHEN OLD.resource_type = 'lan_app_access_disable'
BEGIN SELECT RAISE(ABORT, 'LAN application access disable audit events are immutable'); END;

CREATE TRIGGER lan_app_access_disable_audit_retain
BEFORE DELETE ON audit_events
WHEN OLD.resource_type = 'lan_app_access_disable'
BEGIN SELECT RAISE(ABORT, 'LAN application access disable audit events are retained'); END;
