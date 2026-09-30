CREATE TABLE lan_gateway_upgrade_claims (
    operation_id TEXT PRIMARY KEY CHECK (length(operation_id) BETWEEN 1 AND 128),
    singleton INTEGER NOT NULL DEFAULT 1 CHECK (singleton = 1),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'upgrade_generated_ingress'),
    profile_revision_id TEXT NOT NULL,
    profile_revision_number INTEGER NOT NULL CHECK (profile_revision_number > 0),
    profile_spec_digest TEXT NOT NULL CHECK (length(profile_spec_digest) = 64 AND profile_spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepared','serving','unresolved','committed','rolled_back')),
    state_sequence INTEGER NOT NULL DEFAULT 1 CHECK (state_sequence > 0),
    updated_at TEXT NOT NULL,
    FOREIGN KEY(profile_revision_id, profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number),
    CHECK (state_sequence > 1 OR state = 'prepared')
);

-- Every non-rolled-back claim keeps the exact approved profile pinned. A
-- future migration or disable flow must add an explicit terminal release
-- state rather than silently weakening this invariant.
CREATE UNIQUE INDEX lan_gateway_upgrade_claims_blocking_singleton
ON lan_gateway_upgrade_claims(singleton)
WHERE state IN ('prepared','serving','unresolved','committed');

CREATE TABLE lan_gateway_upgrade_claim_events (
    operation_id TEXT NOT NULL REFERENCES lan_gateway_upgrade_claims(operation_id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    state TEXT NOT NULL CHECK (state IN ('prepared','serving','unresolved','committed','rolled_back')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(operation_id, sequence),
    UNIQUE(operation_id, state)
);

CREATE TRIGGER lan_gateway_upgrade_claim_administrator_insert
BEFORE INSERT ON lan_gateway_upgrade_claims
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.approved_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade approval requires an administrator'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_current_profile_insert
BEFORE INSERT ON lan_gateway_upgrade_claims
WHEN NOT EXISTS (
    SELECT 1
    FROM lan_gateway_profile_heads h
    JOIN lan_gateway_profile_revisions r
      ON r.id = h.revision_id AND r.revision_number = h.revision_number
    WHERE h.singleton = 1
      AND r.id = NEW.profile_revision_id
      AND r.revision_number = NEW.profile_revision_number
      AND r.spec_digest = NEW.profile_spec_digest
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade requires the current profile'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_prepared_insert
BEFORE INSERT ON lan_gateway_upgrade_claims
WHEN NEW.state <> 'prepared' OR NEW.state_sequence <> 1
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade claim must begin prepared'); END;

CREATE TRIGGER lan_gateway_profile_upgrade_claim_pin_insert
BEFORE INSERT ON lan_gateway_profile_revisions
WHEN EXISTS (
    SELECT 1 FROM lan_gateway_upgrade_claims
    WHERE state IN ('prepared','serving','unresolved','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile is pinned by an upgrade claim'); END;

CREATE TRIGGER lan_gateway_profile_upgrade_claim_pin_head
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (
    SELECT 1 FROM lan_gateway_upgrade_claims
    WHERE state IN ('prepared','serving','unresolved','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by an upgrade claim'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_identity_immutable
BEFORE UPDATE OF operation_id, singleton, request_digest, approval_action,
    profile_revision_id, profile_revision_number, profile_spec_digest,
    approved_by, approved_at
ON lan_gateway_upgrade_claims
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade claim identity is immutable'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_state_transition
BEFORE UPDATE OF state, state_sequence, updated_at ON lan_gateway_upgrade_claims
WHEN NOT (
    NEW.state_sequence = OLD.state_sequence + 1
    AND (
        (OLD.state = 'prepared' AND NEW.state IN ('serving','unresolved','committed','rolled_back'))
        OR (OLD.state = 'serving' AND NEW.state IN ('committed','unresolved','rolled_back'))
        OR (OLD.state = 'unresolved' AND NEW.state IN ('committed','rolled_back'))
    )
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN gateway upgrade claim transition'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_initial_event
AFTER INSERT ON lan_gateway_upgrade_claims
BEGIN
    INSERT INTO lan_gateway_upgrade_claim_events(operation_id,sequence,state,created_at)
    VALUES(NEW.operation_id,NEW.state_sequence,NEW.state,NEW.updated_at);
END;

CREATE TRIGGER lan_gateway_upgrade_claim_event_matches_head
BEFORE INSERT ON lan_gateway_upgrade_claim_events
WHEN NOT EXISTS (
    SELECT 1 FROM lan_gateway_upgrade_claims c
    WHERE c.operation_id = NEW.operation_id
      AND c.state_sequence = NEW.sequence
      AND c.state = NEW.state
      AND c.updated_at = NEW.created_at
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade event does not match claim head'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_transition_event
AFTER UPDATE OF state, state_sequence, updated_at ON lan_gateway_upgrade_claims
BEGIN
    INSERT INTO lan_gateway_upgrade_claim_events(operation_id,sequence,state,created_at)
    VALUES(NEW.operation_id,NEW.state_sequence,NEW.state,NEW.updated_at);
END;

CREATE TRIGGER lan_gateway_upgrade_claim_retain
BEFORE DELETE ON lan_gateway_upgrade_claims
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade claims are retained'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_event_immutable_update
BEFORE UPDATE ON lan_gateway_upgrade_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade claim events are immutable'); END;

CREATE TRIGGER lan_gateway_upgrade_claim_event_retain
BEFORE DELETE ON lan_gateway_upgrade_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade claim events are retained'); END;

CREATE TRIGGER lan_gateway_upgrade_audit_immutable_update
BEFORE UPDATE ON audit_events
WHEN OLD.resource_type = 'lan_gateway_upgrade'
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade audit events are immutable'); END;

CREATE TRIGGER lan_gateway_upgrade_audit_retain
BEFORE DELETE ON audit_events
WHEN OLD.resource_type = 'lan_gateway_upgrade'
BEGIN SELECT RAISE(ABORT, 'LAN gateway upgrade audit events are retained'); END;
