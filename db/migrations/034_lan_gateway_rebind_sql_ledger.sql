-- Migration 034 widens the dormant migration-033 lineage into the retained
-- schema needed by a future guarded cutover writer. It does not install that
-- writer: claims still begin prepared and remain immutable, command and
-- transfer inserts are rejected, and every migration-033 mutation fence stays
-- active for nonterminal work. Migration 026/029 pins are left untouched.

DROP TRIGGER lan_gateway_rebind_claim_exact_predecessor_insert;
DROP TRIGGER lan_gateway_rebind_claim_dual_administrator_insert;
DROP TRIGGER lan_gateway_rebind_claim_successor_absent_insert;
DROP TRIGGER lan_gateway_rebind_claim_settled_roster_insert;
DROP TRIGGER lan_gateway_rebind_claim_initial_event;
DROP TRIGGER lan_gateway_rebind_roster_exact_insert;
DROP TRIGGER lan_gateway_rebind_claim_immutable_update;
DROP TRIGGER lan_gateway_rebind_claim_retain;
DROP TRIGGER lan_gateway_rebind_event_immutable_update;
DROP TRIGGER lan_gateway_rebind_event_retain;
DROP TRIGGER lan_gateway_rebind_roster_immutable_update;
DROP TRIGGER lan_gateway_rebind_roster_retain;
DROP TRIGGER lan_gateway_rebind_fence_allocation_insert;
DROP TRIGGER lan_gateway_rebind_fence_allocation_update;
DROP TRIGGER lan_gateway_rebind_fence_access_revision_insert;
DROP TRIGGER lan_gateway_rebind_fence_access_head_update;
DROP TRIGGER lan_gateway_rebind_fence_access_head_insert;
DROP TRIGGER lan_gateway_rebind_fence_access_head_delete;
DROP TRIGGER lan_gateway_rebind_fence_grant_insert;
DROP TRIGGER lan_gateway_rebind_fence_grant_update;
DROP TRIGGER lan_gateway_rebind_fence_disable_intent_insert;
DROP TRIGGER lan_gateway_rebind_fence_disable_claim_insert;
DROP TRIGGER lan_gateway_rebind_fence_disable_claim_update;
DROP TRIGGER lan_gateway_rebind_fence_profile_insert;
DROP TRIGGER lan_gateway_rebind_fence_profile_head_update;
DROP TRIGGER lan_gateway_rebind_fence_profile_head_insert;
DROP TRIGGER lan_gateway_rebind_fence_profile_head_delete;
DROP TRIGGER lan_gateway_rebind_fence_runtime_head_update;
DROP TRIGGER lan_gateway_rebind_fence_runtime_head_insert;
DROP TRIGGER lan_gateway_rebind_fence_runtime_head_delete;

ALTER TABLE lan_gateway_rebind_claim_events RENAME TO lan_gateway_rebind_claim_events_033;
ALTER TABLE lan_gateway_rebind_roster_entries RENAME TO lan_gateway_rebind_roster_entries_033;
ALTER TABLE lan_gateway_rebind_claims RENAME TO lan_gateway_rebind_claims_033;

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
    predecessor_upgrade_operation_id TEXT REFERENCES lan_gateway_upgrade_claims(operation_id),
    predecessor_protected_identity_digest TEXT NOT NULL CHECK (length(predecessor_protected_identity_digest) = 64 AND predecessor_protected_identity_digest NOT GLOB '*[^0-9a-f]*'),
    predecessor_source_kind TEXT NOT NULL DEFAULT 'gateway_upgrade'
        CHECK (predecessor_source_kind IN ('gateway_upgrade','gateway_rebind')),
    predecessor_rebind_operation_id TEXT REFERENCES lan_gateway_rebind_claims(operation_id),
    predecessor_terminal_receipt_digest TEXT CHECK (
        predecessor_terminal_receipt_digest IS NULL OR
        (length(predecessor_terminal_receipt_digest) = 64 AND predecessor_terminal_receipt_digest NOT GLOB '*[^0-9a-f]*')
    ),

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
    state TEXT NOT NULL CHECK (state IN ('prepared','successor_ready','database_committed','committed','rolled_back','unresolved')),
    state_sequence INTEGER NOT NULL CHECK (state_sequence > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,

    FOREIGN KEY(predecessor_profile_revision_id, predecessor_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number),
    CHECK (successor_profile_revision_number = predecessor_profile_revision_number + 1),
    CHECK (successor_profile_revision_id <> predecessor_profile_revision_id),
    CHECK (successor_profile_operation_id <> operation_id),
    CHECK (state_sequence > 1 OR state = 'prepared'),
    CHECK (approved_at <= created_at AND configure_approved_at <= created_at),
    CHECK (
        (predecessor_source_kind = 'gateway_upgrade'
            AND predecessor_upgrade_operation_id IS NOT NULL
            AND predecessor_rebind_operation_id IS NULL
            AND predecessor_terminal_receipt_digest IS NULL)
        OR
        (predecessor_source_kind = 'gateway_rebind'
            AND predecessor_upgrade_operation_id IS NULL
            AND predecessor_rebind_operation_id IS NOT NULL
            AND predecessor_terminal_receipt_digest IS NOT NULL)
    )
);

CREATE UNIQUE INDEX lan_gateway_rebind_claims_one_active
ON lan_gateway_rebind_claims(singleton)
WHERE state IN ('prepared','successor_ready','database_committed','unresolved');

INSERT INTO lan_gateway_rebind_claims(
    operation_id,singleton,request_digest,approval_action,spec_digest,approved_by,approved_at,
    predecessor_profile_revision_id,predecessor_profile_revision_number,
    predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
    predecessor_protected_identity_digest,predecessor_source_kind,
    predecessor_rebind_operation_id,predecessor_terminal_receipt_digest,
    successor_profile_revision_id,successor_profile_revision_number,
    successor_profile_operation_id,successor_profile_request_digest,
    successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
    successor_profile_spec_digest,configure_approval_action,configure_approved_by,
    configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at
)
SELECT
    operation_id,singleton,request_digest,approval_action,spec_digest,approved_by,approved_at,
    predecessor_profile_revision_id,predecessor_profile_revision_number,
    predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
    predecessor_protected_identity_digest,'gateway_upgrade',NULL,NULL,
    successor_profile_revision_id,successor_profile_revision_number,
    successor_profile_operation_id,successor_profile_request_digest,
    successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
    successor_profile_spec_digest,configure_approval_action,configure_approved_by,
    configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at
FROM lan_gateway_rebind_claims_033;

CREATE TABLE lan_gateway_rebind_claim_events (
    operation_id TEXT NOT NULL REFERENCES lan_gateway_rebind_claims(operation_id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    state TEXT NOT NULL CHECK (state IN ('prepared','successor_ready','database_committed','committed','rolled_back','unresolved')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(operation_id, sequence)
);

INSERT INTO lan_gateway_rebind_claim_events(operation_id,sequence,state,created_at)
SELECT operation_id,sequence,state,created_at
FROM lan_gateway_rebind_claim_events_033;

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

INSERT INTO lan_gateway_rebind_roster_entries(
    operation_id,ordinal,app_id,allocation_id,allocated_port,
    allocation_owner_operation_id,allocation_state,access_revision_id,
    access_revision_number,access_spec_digest,grant_attempt_id,grant_state_sequence,
    grant_protected_state_digest,serving_deployment_id,serving_release_id,
    serving_slot,route_generation,entry_digest
)
SELECT
    operation_id,ordinal,app_id,allocation_id,allocated_port,
    allocation_owner_operation_id,allocation_state,access_revision_id,
    access_revision_number,access_spec_digest,grant_attempt_id,grant_state_sequence,
    grant_protected_state_digest,serving_deployment_id,serving_release_id,
    serving_slot,route_generation,entry_digest
FROM lan_gateway_rebind_roster_entries_033;

DROP TABLE lan_gateway_rebind_claim_events_033;
DROP TABLE lan_gateway_rebind_roster_entries_033;
DROP TABLE lan_gateway_rebind_claims_033;

-- These command rows will eventually bind a protected record/receipt to one
-- exact state advance. Migration 034 reserves the immutable format while the
-- insert barrier below keeps it unreachable from ordinary SQL.
CREATE TABLE lan_gateway_rebind_transition_commands (
    operation_id TEXT NOT NULL REFERENCES lan_gateway_rebind_claims(operation_id),
    sequence INTEGER NOT NULL CHECK (sequence > 1),
    previous_state TEXT NOT NULL CHECK (previous_state IN ('prepared','successor_ready','database_committed','unresolved')),
    previous_sequence INTEGER NOT NULL CHECK (previous_sequence > 0),
    next_state TEXT NOT NULL CHECK (next_state IN ('successor_ready','database_committed','committed','rolled_back','unresolved')),
    purpose TEXT NOT NULL CHECK (purpose = 'lan_gateway_rebind_transition'),
    protected_record_digest TEXT NOT NULL CHECK (length(protected_record_digest) = 64 AND protected_record_digest NOT GLOB '*[^0-9a-f]*'),
    terminal_receipt_digest TEXT CHECK (
        terminal_receipt_digest IS NULL OR
        (length(terminal_receipt_digest) = 64 AND terminal_receipt_digest NOT GLOB '*[^0-9a-f]*')
    ),
    local_attestation_digest TEXT CHECK (
        local_attestation_digest IS NULL OR
        (length(local_attestation_digest) = 64 AND local_attestation_digest NOT GLOB '*[^0-9a-f]*')
    ),
    command_digest TEXT NOT NULL CHECK (length(command_digest) = 64 AND command_digest NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL,
    PRIMARY KEY(operation_id, sequence),
    UNIQUE(operation_id, command_digest),
    CHECK (sequence = previous_sequence + 1),
    CHECK (
        (previous_state = 'prepared' AND next_state IN ('successor_ready','rolled_back','unresolved'))
        OR (previous_state = 'successor_ready' AND next_state IN ('database_committed','unresolved'))
        OR (previous_state = 'database_committed' AND next_state IN ('committed','unresolved'))
        OR (previous_state = 'unresolved' AND next_state IN ('successor_ready','database_committed','committed','rolled_back'))
    ),
    CHECK (
        (next_state IN ('successor_ready','database_committed','committed','rolled_back') AND terminal_receipt_digest IS NOT NULL)
        OR (next_state = 'unresolved')
    ),
    CHECK ((next_state = 'committed' AND local_attestation_digest IS NOT NULL) OR next_state <> 'committed')
);

-- Each future row maps one immutable source grant/allocation proof onto the
-- approved successor profile. It does not rewrite any source history.
CREATE TABLE lan_gateway_rebind_allocation_transfers (
    operation_id TEXT NOT NULL REFERENCES lan_gateway_rebind_claims(operation_id),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    app_id TEXT NOT NULL REFERENCES applications(id),
    allocation_id TEXT NOT NULL REFERENCES lan_port_allocations(id),
    grant_attempt_id TEXT NOT NULL REFERENCES lan_app_access_grant_claims(attempt_id),
    source_binding_digest TEXT NOT NULL CHECK (length(source_binding_digest) = 64 AND source_binding_digest NOT GLOB '*[^0-9a-f]*'),
    successor_profile_revision_id TEXT NOT NULL,
    successor_profile_revision_number INTEGER NOT NULL CHECK (successor_profile_revision_number > 1),
    successor_profile_spec_digest TEXT NOT NULL CHECK (length(successor_profile_spec_digest) = 64 AND successor_profile_spec_digest NOT GLOB '*[^0-9a-f]*'),
    terminal_receipt_digest TEXT NOT NULL CHECK (length(terminal_receipt_digest) = 64 AND terminal_receipt_digest NOT GLOB '*[^0-9a-f]*'),
    transfer_digest TEXT NOT NULL CHECK (length(transfer_digest) = 64 AND transfer_digest NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL,
    PRIMARY KEY(operation_id, allocation_id),
    UNIQUE(operation_id, ordinal),
    UNIQUE(operation_id, app_id),
    UNIQUE(operation_id, transfer_digest),
    FOREIGN KEY(operation_id, allocation_id)
        REFERENCES lan_gateway_rebind_roster_entries(operation_id, allocation_id),
    FOREIGN KEY(successor_profile_revision_id, successor_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number)
);

CREATE TRIGGER lan_gateway_rebind_claim_exact_predecessor_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NOT (
    NEW.predecessor_source_kind = 'gateway_upgrade'
    AND EXISTS (
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
) AND NOT (
    NEW.predecessor_source_kind = 'gateway_rebind'
    AND EXISTS (
        SELECT 1
        FROM lan_gateway_profile_heads h
        JOIN lan_gateway_profile_revisions p
          ON p.id=h.revision_id AND p.revision_number=h.revision_number
        JOIN lan_gateway_rebind_claims c
          ON c.operation_id=NEW.predecessor_rebind_operation_id
         AND c.successor_profile_revision_id=p.id
         AND c.successor_profile_revision_number=p.revision_number
         AND c.successor_profile_spec_digest=p.spec_digest
        JOIN lan_gateway_rebind_claim_events e
          ON e.operation_id=c.operation_id
         AND e.sequence=c.state_sequence
         AND e.state='committed'
        JOIN lan_gateway_rebind_transition_commands cmd
          ON cmd.operation_id=c.operation_id
         AND cmd.sequence=e.sequence
         AND cmd.next_state='committed'
         AND cmd.terminal_receipt_digest=NEW.predecessor_terminal_receipt_digest
        WHERE h.singleton=1
          AND p.id=NEW.predecessor_profile_revision_id
          AND p.revision_number=NEW.predecessor_profile_revision_number
          AND p.spec_digest=NEW.predecessor_profile_spec_digest
          AND c.state='committed'
    )
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind requires the committed current predecessor'); END;

CREATE TRIGGER lan_gateway_rebind_claim_dual_administrator_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.approved_by AND role='administrator')
  OR NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.configure_approved_by AND role='administrator')
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind requires both administrator approvals'); END;

CREATE TRIGGER lan_gateway_rebind_claim_prepared_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NEW.state <> 'prepared' OR NEW.state_sequence <> 1 OR NEW.updated_at <> NEW.created_at
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind claim must begin prepared'); END;

CREATE TRIGGER lan_gateway_rebind_claim_successor_absent_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN EXISTS (
    SELECT 1 FROM lan_gateway_profile_revisions p
    WHERE p.id=NEW.successor_profile_revision_id
       OR p.revision_number=NEW.successor_profile_revision_number
       OR p.operation_id=NEW.successor_profile_operation_id
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind successor profile already exists'); END;

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
      AND c.state='prepared'
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
CREATE TRIGGER lan_gateway_rebind_event_direct_insert
BEFORE INSERT ON lan_gateway_rebind_claim_events
WHEN NEW.sequence <> 1 OR NEW.state <> 'prepared'
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind event writer is not installed'); END;
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

CREATE TRIGGER lan_gateway_rebind_transition_command_insert_blocked
BEFORE INSERT ON lan_gateway_rebind_transition_commands
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind guarded transition writer is not installed'); END;
CREATE TRIGGER lan_gateway_rebind_transition_command_immutable_update
BEFORE UPDATE ON lan_gateway_rebind_transition_commands
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind transition commands are immutable'); END;
CREATE TRIGGER lan_gateway_rebind_transition_command_retain
BEFORE DELETE ON lan_gateway_rebind_transition_commands
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind transition commands are retained'); END;
CREATE TRIGGER lan_gateway_rebind_allocation_transfer_insert_blocked
BEFORE INSERT ON lan_gateway_rebind_allocation_transfers
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind guarded transfer writer is not installed'); END;
CREATE TRIGGER lan_gateway_rebind_allocation_transfer_immutable_update
BEFORE UPDATE ON lan_gateway_rebind_allocation_transfers
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind allocation transfers are immutable'); END;
CREATE TRIGGER lan_gateway_rebind_allocation_transfer_retain
BEFORE DELETE ON lan_gateway_rebind_allocation_transfers
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind allocation transfers are retained'); END;

-- Every claim remains a hard cross-subsystem fence in this dormant slice,
-- including future terminal history. A later guarded migration must replace
-- these literal EXISTS(any claim) predicates only after receipt-bound readers,
-- transitions, and release semantics have been reviewed together.
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
