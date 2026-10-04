-- This migration deliberately creates only a dormant, fail-closed rebind
-- fence. There is no production claim writer, transition writer, profile-head
-- exception, transfer, or fence-release surface. A later guarded-writer
-- migration must replace the immutable prepared-claim lock and migration 026's
-- profile pins atomically before a rebind can change durable or physical state.

CREATE TABLE lan_gateway_rebind_claims (
    operation_id TEXT PRIMARY KEY CHECK (length(operation_id) BETWEEN 1 AND 128),
    singleton INTEGER NOT NULL DEFAULT 1 CHECK (singleton = 1),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'rebind_lan_gateway'),
    spec_digest TEXT NOT NULL CHECK (length(spec_digest) = 64 AND spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,

    predecessor_profile_revision_id TEXT NOT NULL,
    predecessor_profile_revision_number INTEGER NOT NULL CHECK (predecessor_profile_revision_number > 0),
    predecessor_profile_spec_digest TEXT NOT NULL CHECK (length(predecessor_profile_spec_digest) = 64 AND predecessor_profile_spec_digest NOT GLOB '*[^0-9a-f]*'),
    predecessor_upgrade_operation_id TEXT NOT NULL REFERENCES lan_gateway_upgrade_claims(operation_id),
    predecessor_protected_identity_digest TEXT NOT NULL CHECK (length(predecessor_protected_identity_digest) = 64 AND predecessor_protected_identity_digest NOT GLOB '*[^0-9a-f]*'),

    successor_profile_revision_id TEXT NOT NULL UNIQUE,
    successor_profile_revision_number INTEGER NOT NULL CHECK (successor_profile_revision_number > 1),
    successor_profile_operation_id TEXT NOT NULL UNIQUE CHECK (length(successor_profile_operation_id) BETWEEN 1 AND 128),
    successor_profile_request_digest TEXT NOT NULL CHECK (length(successor_profile_request_digest) = 64 AND successor_profile_request_digest NOT GLOB '*[^0-9a-f]*'),
    successor_selected_ipv4 TEXT NOT NULL CHECK (length(successor_selected_ipv4) BETWEEN 7 AND 15),
    successor_interface_id TEXT NOT NULL CHECK (length(successor_interface_id) BETWEEN 1 AND 512),
    successor_port_start INTEGER NOT NULL CHECK (successor_port_start BETWEEN 8100 AND 8119),
    successor_port_end INTEGER NOT NULL CHECK (successor_port_end BETWEEN successor_port_start AND 8119),
    successor_profile_spec_digest TEXT NOT NULL CHECK (length(successor_profile_spec_digest) = 64 AND successor_profile_spec_digest NOT GLOB '*[^0-9a-f]*'),
    configure_approval_action TEXT NOT NULL CHECK (configure_approval_action = 'configure_lan_gateway'),
    configure_approved_by TEXT NOT NULL REFERENCES users(id),
    configure_approved_at TEXT NOT NULL,

    roster_digest TEXT NOT NULL CHECK (length(roster_digest) = 64 AND roster_digest NOT GLOB '*[^0-9a-f]*'),
    roster_count INTEGER NOT NULL CHECK (roster_count >= 0),
    state TEXT NOT NULL CHECK (state = 'prepared'),
    state_sequence INTEGER NOT NULL CHECK (state_sequence = 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,

    UNIQUE(singleton),
    FOREIGN KEY(predecessor_profile_revision_id, predecessor_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number),
    CHECK (successor_profile_revision_number = predecessor_profile_revision_number + 1),
    CHECK (successor_profile_revision_id <> predecessor_profile_revision_id),
    CHECK (successor_profile_operation_id <> operation_id),
    CHECK (updated_at = created_at),
    CHECK (approved_at <= created_at AND configure_approved_at <= created_at)
);

CREATE TABLE lan_gateway_rebind_claim_events (
    operation_id TEXT NOT NULL REFERENCES lan_gateway_rebind_claims(operation_id),
    sequence INTEGER NOT NULL CHECK (sequence = 1),
    state TEXT NOT NULL CHECK (state = 'prepared'),
    created_at TEXT NOT NULL,
    PRIMARY KEY(operation_id, sequence),
    UNIQUE(operation_id, state)
);

CREATE TABLE lan_gateway_rebind_roster_entries (
    operation_id TEXT NOT NULL REFERENCES lan_gateway_rebind_claims(operation_id),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    app_id TEXT NOT NULL REFERENCES applications(id),
    allocation_id TEXT NOT NULL REFERENCES lan_port_allocations(id),
    allocated_port INTEGER NOT NULL CHECK (allocated_port BETWEEN 8100 AND 8119),
    allocation_owner_operation_id TEXT NOT NULL CHECK (length(allocation_owner_operation_id) BETWEEN 1 AND 128),
    allocation_state TEXT NOT NULL CHECK (allocation_state = 'active'),
    access_revision_id TEXT NOT NULL,
    access_revision_number INTEGER NOT NULL CHECK (access_revision_number > 0),
    access_spec_digest TEXT NOT NULL CHECK (length(access_spec_digest) = 64 AND access_spec_digest NOT GLOB '*[^0-9a-f]*'),
    grant_attempt_id TEXT NOT NULL REFERENCES lan_app_access_grant_claims(attempt_id),
    grant_state_sequence INTEGER NOT NULL CHECK (grant_state_sequence > 0),
    grant_protected_state_digest TEXT NOT NULL CHECK (length(grant_protected_state_digest) = 64 AND grant_protected_state_digest NOT GLOB '*[^0-9a-f]*'),
    serving_deployment_id TEXT NOT NULL,
    serving_release_id TEXT NOT NULL,
    serving_slot TEXT NOT NULL CHECK (serving_slot IN ('blue','green')),
    route_generation INTEGER NOT NULL CHECK (route_generation > 0),
    entry_digest TEXT NOT NULL CHECK (length(entry_digest) = 64 AND entry_digest NOT GLOB '*[^0-9a-f]*'),
    PRIMARY KEY(operation_id, allocation_id),
    UNIQUE(operation_id, ordinal),
    UNIQUE(operation_id, app_id),
    UNIQUE(operation_id, allocated_port),
    FOREIGN KEY(app_id, access_revision_id) REFERENCES lan_app_access_revisions(app_id, id),
    FOREIGN KEY(app_id, serving_deployment_id) REFERENCES generated_runtime_deployments(app_id, deployment_id),
    FOREIGN KEY(serving_release_id) REFERENCES releases(id)
);

CREATE TRIGGER lan_gateway_rebind_claim_exact_predecessor_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NOT EXISTS (
    SELECT 1
    FROM lan_gateway_profile_heads h
    JOIN lan_gateway_profile_revisions p
      ON p.id=h.revision_id AND p.revision_number=h.revision_number
    JOIN lan_gateway_upgrade_claims u
      ON u.operation_id=NEW.predecessor_upgrade_operation_id
     AND u.profile_revision_id=p.id
     AND u.profile_revision_number=p.revision_number
     AND u.profile_spec_digest=p.spec_digest
    WHERE h.singleton=1
      AND p.id=NEW.predecessor_profile_revision_id
      AND p.revision_number=NEW.predecessor_profile_revision_number
      AND p.spec_digest=NEW.predecessor_profile_spec_digest
      AND u.state='committed'
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind requires the committed current predecessor'); END;

CREATE TRIGGER lan_gateway_rebind_claim_dual_administrator_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.approved_by AND role='administrator')
  OR NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.configure_approved_by AND role='administrator')
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind requires both administrator approvals'); END;

CREATE TRIGGER lan_gateway_rebind_claim_successor_absent_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN EXISTS (
    SELECT 1 FROM lan_gateway_profile_revisions p
    WHERE p.id=NEW.successor_profile_revision_id
       OR p.revision_number=NEW.successor_profile_revision_number
       OR p.operation_id=NEW.successor_profile_operation_id
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind successor profile already exists'); END;

-- A prepared fence may cover only a fully active, settled roster. The exact
-- rows are copied separately after this claim acquires the fence.
CREATE TRIGGER lan_gateway_rebind_claim_settled_roster_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NEW.roster_count <> (
        SELECT COUNT(*) FROM lan_port_allocations a
        WHERE a.released_at IS NULL AND a.disabled_at IS NULL
    )
  OR EXISTS (
        SELECT 1 FROM lan_port_allocations a
        WHERE a.released_at IS NULL AND a.disabled_at IS NULL AND a.state <> 'active'
    )
  OR EXISTS (
        SELECT 1 FROM lan_port_allocations a
        WHERE a.released_at IS NULL AND a.disabled_at IS NULL
          AND (a.port < NEW.successor_port_start OR a.port > NEW.successor_port_end)
    )
  OR EXISTS (
        SELECT 1 FROM lan_port_allocations a
        WHERE a.released_at IS NULL AND a.disabled_at IS NULL
          AND NOT EXISTS (
              SELECT 1 FROM lan_app_access_grant_claims g
              WHERE g.allocation_id=a.id AND g.state='committed'
                AND g.retired_at IS NULL AND g.protected_state_digest IS NOT NULL
          )
    )
  OR EXISTS (
        SELECT 1 FROM lan_port_allocations a
        WHERE a.released_at IS NULL AND a.disabled_at IS NULL
          AND EXISTS (
              SELECT 1 FROM lan_app_access_disable_intents d WHERE d.allocation_id=a.id
          )
    )
  OR EXISTS (
        SELECT 1 FROM lan_port_allocations a
        WHERE a.released_at IS NULL AND a.disabled_at IS NULL
          AND NOT EXISTS (
              SELECT 1 FROM generated_runtime_active_heads h
              WHERE h.app_id=a.app_id AND h.generation > 0
                AND h.deployment_id IS NOT NULL AND h.release_id IS NOT NULL AND h.slot IS NOT NULL
          )
    )
  OR EXISTS (
        SELECT 1 FROM lan_app_access_grant_claims
        WHERE state IN ('prepared','applying','db_active','uncertain') AND retired_at IS NULL
    )
  OR EXISTS (
        SELECT 1 FROM lan_app_access_disable_claims
        WHERE state IN ('prepared','withdrawing','uncertain')
    )
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind requires an exact settled roster'); END;

CREATE TRIGGER lan_gateway_rebind_claim_initial_event
AFTER INSERT ON lan_gateway_rebind_claims
BEGIN
    INSERT INTO lan_gateway_rebind_claim_events(operation_id,sequence,state,created_at)
    VALUES(NEW.operation_id,NEW.state_sequence,NEW.state,NEW.updated_at);
END;

CREATE TRIGGER lan_gateway_rebind_roster_exact_insert
BEFORE INSERT ON lan_gateway_rebind_roster_entries
WHEN NOT EXISTS (
    SELECT 1
    FROM lan_gateway_rebind_claims c
    JOIN lan_port_allocations a
      ON a.id=NEW.allocation_id AND a.app_id=NEW.app_id
    JOIN applications app ON app.id=a.app_id
    JOIN lan_app_access_heads ah ON ah.app_id=a.app_id
    JOIN lan_app_access_revisions ar
      ON ar.app_id=ah.app_id AND ar.id=ah.revision_id AND ar.revision_number=ah.revision_number
    JOIN lan_app_access_grant_claims g
      ON g.attempt_id=NEW.grant_attempt_id AND g.allocation_id=a.id
    JOIN generated_runtime_active_heads rh ON rh.app_id=a.app_id
    WHERE c.operation_id=NEW.operation_id
      AND a.port=NEW.allocated_port
      AND app.archived_at IS NULL
      AND a.owner_operation_id=NEW.allocation_owner_operation_id
      AND a.owner_revision_id=NEW.access_revision_id
      AND a.state=NEW.allocation_state AND a.state='active'
      AND a.released_at IS NULL AND a.disabled_at IS NULL
      AND a.gateway_profile_revision_id=c.predecessor_profile_revision_id
      AND a.gateway_profile_revision_number=c.predecessor_profile_revision_number
      AND ar.id=NEW.access_revision_id
      AND ar.revision_number=NEW.access_revision_number
      AND ar.operation_id=NEW.allocation_owner_operation_id
      AND ar.allocation_id=a.id AND ar.allocated_port=a.port
      AND ar.spec_digest=NEW.access_spec_digest
      AND ar.gateway_profile_revision_id=c.predecessor_profile_revision_id
      AND ar.gateway_profile_revision_number=c.predecessor_profile_revision_number
      AND g.access_revision_id=ar.id
      AND g.access_revision_number=ar.revision_number
      AND g.allocation_owner_operation_id=a.owner_operation_id
      AND g.state='committed' AND g.state_sequence=NEW.grant_state_sequence
      AND g.retired_at IS NULL
      AND g.protected_state_digest=NEW.grant_protected_state_digest
      AND rh.deployment_id=NEW.serving_deployment_id
      AND rh.release_id=NEW.serving_release_id
      AND rh.slot=NEW.serving_slot
      AND rh.generation=NEW.route_generation AND rh.generation > 0
      AND NOT EXISTS (
          SELECT 1 FROM lan_app_access_disable_intents d WHERE d.allocation_id=a.id
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind roster entry is not current'); END;

CREATE TRIGGER lan_gateway_rebind_claim_immutable_update
BEFORE UPDATE ON lan_gateway_rebind_claims
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind claim is dormant and immutable'); END;
CREATE TRIGGER lan_gateway_rebind_claim_retain
BEFORE DELETE ON lan_gateway_rebind_claims
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind claims are retained'); END;
CREATE TRIGGER lan_gateway_rebind_event_immutable_update
BEFORE UPDATE ON lan_gateway_rebind_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind events are immutable'); END;
CREATE TRIGGER lan_gateway_rebind_event_retain
BEFORE DELETE ON lan_gateway_rebind_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind events are retained'); END;
CREATE TRIGGER lan_gateway_rebind_roster_immutable_update
BEFORE UPDATE ON lan_gateway_rebind_roster_entries
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind roster is immutable'); END;
CREATE TRIGGER lan_gateway_rebind_roster_retain
BEFORE DELETE ON lan_gateway_rebind_roster_entries
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind roster is retained'); END;

-- A prepared claim is intentionally unreleasable in migration 033. Every
-- relevant durable writer fails before mutation until a later migration adds
-- the protected commit and release contract.
CREATE TRIGGER lan_gateway_rebind_fence_allocation_insert
BEFORE INSERT ON lan_port_allocations
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_allocation_update
BEFORE UPDATE ON lan_port_allocations
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_revision_insert
BEFORE INSERT ON lan_app_access_revisions
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_head_update
BEFORE UPDATE ON lan_app_access_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_head_insert
BEFORE INSERT ON lan_app_access_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_head_delete
BEFORE DELETE ON lan_app_access_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_grant_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_grant_update
BEFORE UPDATE ON lan_app_access_grant_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_disable_intent_insert
BEFORE INSERT ON lan_app_access_disable_intents
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_disable_claim_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_disable_claim_update
BEFORE UPDATE ON lan_app_access_disable_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_profile_insert
BEFORE INSERT ON lan_gateway_profile_revisions
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_profile_head_update
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_profile_head_insert
BEFORE INSERT ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_profile_head_delete
BEFORE DELETE ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_update
BEFORE UPDATE ON generated_runtime_active_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_insert
BEFORE INSERT ON generated_runtime_active_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_delete
BEFORE DELETE ON generated_runtime_active_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
