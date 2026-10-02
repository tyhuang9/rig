-- Each gateway mutation attempt has a durable identity and retained state
-- history. The claim is a logical cross-process lease: it never expires and
-- is never deleted. A retry after a proved rollback must use a new attempt_id.
CREATE TABLE lan_app_access_grant_claims (
    attempt_id TEXT PRIMARY KEY CHECK (length(attempt_id) BETWEEN 1 AND 128),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'enable_lan_access'),
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    allocation_id TEXT NOT NULL REFERENCES lan_port_allocations(id),
    allocation_owner_operation_id TEXT NOT NULL CHECK (length(allocation_owner_operation_id) BETWEEN 1 AND 128),
    access_revision_id TEXT NOT NULL,
    access_revision_number INTEGER NOT NULL CHECK (access_revision_number > 0),
    access_spec_digest TEXT NOT NULL CHECK (length(access_spec_digest) = 64 AND access_spec_digest NOT GLOB '*[^0-9a-f]*'),
    allocated_port INTEGER NOT NULL CHECK (allocated_port BETWEEN 8100 AND 8119),
    gateway_profile_revision_id TEXT NOT NULL,
    gateway_profile_revision_number INTEGER NOT NULL CHECK (gateway_profile_revision_number > 0),
    gateway_profile_spec_digest TEXT NOT NULL CHECK (length(gateway_profile_spec_digest) = 64 AND gateway_profile_spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepared','applying','db_active','uncertain','committed','rolled_back')),
    state_sequence INTEGER NOT NULL DEFAULT 1 CHECK (state_sequence > 0),
    updated_at TEXT NOT NULL,
    resolution_outcome TEXT CHECK (resolution_outcome IN ('committed','rolled_back')),
    gateway_operation_id TEXT,
    protected_state_digest TEXT CHECK (protected_state_digest IS NULL OR (length(protected_state_digest) = 64 AND protected_state_digest NOT GLOB '*[^0-9a-f]*')),
    resolved_at TEXT,
    UNIQUE(app_id, attempt_id),
    FOREIGN KEY(app_id, access_revision_id) REFERENCES lan_app_access_revisions(app_id, id),
    FOREIGN KEY(gateway_profile_revision_id, gateway_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number),
    CHECK (state_sequence > 1 OR state = 'prepared'),
    CHECK (
        (state IN ('prepared','applying','db_active','uncertain')
            AND resolution_outcome IS NULL AND gateway_operation_id IS NULL
            AND protected_state_digest IS NULL AND resolved_at IS NULL)
        OR
        (state IN ('committed','rolled_back')
            AND resolution_outcome = state AND gateway_operation_id IS NOT NULL
            AND protected_state_digest IS NOT NULL AND resolved_at IS NOT NULL)
    )
);

-- A non-rolled-back attempt owns the logical lease for one exact allocation.
-- Committed claims remain current until the later disable finalization flow
-- explicitly retires them; there is no time based takeover.
CREATE UNIQUE INDEX lan_app_access_grant_claims_current_allocation
ON lan_app_access_grant_claims(allocation_id)
WHERE state IN ('prepared','applying','db_active','uncertain','committed');

-- Protected ingress has one pending transition slot. Permit many committed
-- app bindings, but never start a second unresolved grant before the first
-- has reached a proved terminal state.
CREATE UNIQUE INDEX lan_app_access_grant_claims_one_unresolved
ON lan_app_access_grant_claims(approval_action)
WHERE state IN ('prepared','applying','db_active','uncertain');

CREATE INDEX lan_app_access_grant_claims_startup
ON lan_app_access_grant_claims(state, app_id, attempt_id);

CREATE TABLE lan_app_access_grant_claim_events (
    attempt_id TEXT NOT NULL REFERENCES lan_app_access_grant_claims(attempt_id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    state TEXT NOT NULL CHECK (state IN ('prepared','applying','db_active','uncertain','committed','rolled_back')),
    gateway_operation_id TEXT,
    protected_state_digest TEXT CHECK (protected_state_digest IS NULL OR (length(protected_state_digest) = 64 AND protected_state_digest NOT GLOB '*[^0-9a-f]*')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(attempt_id, sequence),
    UNIQUE(attempt_id, state),
    CHECK (
        (state IN ('committed','rolled_back') AND gateway_operation_id IS NOT NULL AND protected_state_digest IS NOT NULL)
        OR
        (state IN ('prepared','applying','db_active','uncertain') AND gateway_operation_id IS NULL AND protected_state_digest IS NULL)
    )
);

CREATE TRIGGER lan_app_access_grant_administrator_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.approved_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant requires the approving administrator'); END;

-- The database independently checks every identity supplied by the
-- controller against the approved current heads and allocation owner.
CREATE TRIGGER lan_app_access_grant_exact_owner_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN NOT EXISTS (
    SELECT 1
    FROM applications app
    JOIN lan_app_access_heads ah ON ah.app_id = app.id
    JOIN lan_app_access_revisions ar
      ON ar.app_id = ah.app_id AND ar.id = ah.revision_id AND ar.revision_number = ah.revision_number
    JOIN lan_port_allocations a
      ON a.id = ar.allocation_id AND a.app_id = ar.app_id
    JOIN lan_gateway_profile_heads gh ON gh.singleton = 1
    JOIN lan_gateway_profile_revisions gr
      ON gr.id = gh.revision_id AND gr.revision_number = gh.revision_number
    WHERE app.id = NEW.app_id AND app.archived_at IS NULL
      AND ar.id = NEW.access_revision_id
      AND ar.revision_number = NEW.access_revision_number
      AND ar.operation_id = NEW.allocation_owner_operation_id
      AND ar.approval_action = NEW.approval_action
      AND ar.spec_digest = NEW.access_spec_digest
      AND ar.approved_by = NEW.approved_by
      AND ar.approved_at = NEW.approved_at
      AND ar.allocation_id = NEW.allocation_id
      AND ar.allocated_port = NEW.allocated_port
      AND ar.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND ar.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.owner_operation_id = NEW.allocation_owner_operation_id
      AND a.owner_revision_id = NEW.access_revision_id
      AND a.port = NEW.allocated_port
      AND a.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.state IN ('reserved','active','uncertain')
      AND a.released_at IS NULL
      AND gr.id = NEW.gateway_profile_revision_id
      AND gr.revision_number = NEW.gateway_profile_revision_number
      AND gr.spec_digest = NEW.gateway_profile_spec_digest
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant requires the exact current owner'); END;

CREATE TRIGGER lan_app_access_grant_no_disable_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_intents d
    WHERE d.allocation_id = NEW.allocation_id
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant conflicts with disable intent'); END;

CREATE TRIGGER lan_app_access_grant_prepared_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN NEW.state <> 'prepared' OR NEW.state_sequence <> 1
  OR NEW.updated_at <> NEW.created_at
BEGIN SELECT RAISE(ABORT, 'LAN application access grant claim must begin prepared'); END;

CREATE TRIGGER lan_app_access_grant_identity_immutable
BEFORE UPDATE OF attempt_id,request_digest,approval_action,app_id,allocation_id,
    allocation_owner_operation_id,access_revision_id,access_revision_number,
    access_spec_digest,allocated_port,gateway_profile_revision_id,
    gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
    approved_at,created_at
ON lan_app_access_grant_claims
BEGIN SELECT RAISE(ABORT, 'LAN application access grant claim identity is immutable'); END;

CREATE TRIGGER lan_app_access_grant_state_transition
BEFORE UPDATE OF state,state_sequence,updated_at,resolution_outcome,
    gateway_operation_id,protected_state_digest,resolved_at
ON lan_app_access_grant_claims
WHEN NOT (
    NEW.state_sequence = OLD.state_sequence + 1
    AND NEW.updated_at >= OLD.updated_at
    AND (
        (OLD.state = 'prepared' AND NEW.state IN ('applying','rolled_back'))
        OR (OLD.state = 'applying' AND NEW.state IN ('db_active','uncertain','rolled_back'))
        OR (OLD.state = 'db_active' AND NEW.state IN ('committed','uncertain'))
        OR (OLD.state = 'uncertain' AND NEW.state IN ('committed','rolled_back'))
    )
    AND (
        (NEW.state IN ('prepared','applying','db_active','uncertain')
            AND NEW.resolution_outcome IS NULL AND NEW.gateway_operation_id IS NULL
            AND NEW.protected_state_digest IS NULL AND NEW.resolved_at IS NULL)
        OR
        (NEW.state IN ('committed','rolled_back')
            AND NEW.resolution_outcome = NEW.state AND NEW.gateway_operation_id IS NOT NULL
            AND NEW.protected_state_digest IS NOT NULL AND NEW.resolved_at = NEW.updated_at)
    )
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN application access grant transition'); END;

-- Entering the mutation window rechecks the complete owner under the same
-- short SQLite transaction. The durable state then fences competing writers
-- while the controller performs gateway work without holding a DB lock.
CREATE TRIGGER lan_app_access_grant_applying_exact_owner
BEFORE UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state = 'applying' AND NOT EXISTS (
    SELECT 1
    FROM applications app
    JOIN users u ON u.id = NEW.approved_by AND u.role = 'administrator'
    JOIN lan_app_access_heads ah ON ah.app_id = app.id
    JOIN lan_app_access_revisions ar
      ON ar.app_id = ah.app_id AND ar.id = ah.revision_id AND ar.revision_number = ah.revision_number
    JOIN lan_port_allocations a ON a.id = ar.allocation_id AND a.app_id = ar.app_id
    JOIN lan_gateway_profile_heads gh ON gh.singleton = 1
    JOIN lan_gateway_profile_revisions gr
      ON gr.id = gh.revision_id AND gr.revision_number = gh.revision_number
    WHERE app.id = NEW.app_id AND app.archived_at IS NULL
      AND ar.id = NEW.access_revision_id AND ar.revision_number = NEW.access_revision_number
      AND ar.operation_id = NEW.allocation_owner_operation_id
      AND ar.spec_digest = NEW.access_spec_digest AND ar.approved_by = NEW.approved_by
      AND ar.allocation_id = NEW.allocation_id AND ar.allocated_port = NEW.allocated_port
      AND a.owner_operation_id = NEW.allocation_owner_operation_id
      AND a.owner_revision_id = NEW.access_revision_id AND a.port = NEW.allocated_port
      AND a.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.state IN ('reserved','active','uncertain') AND a.released_at IS NULL
      AND gr.id = NEW.gateway_profile_revision_id
      AND gr.revision_number = NEW.gateway_profile_revision_number
      AND gr.spec_digest = NEW.gateway_profile_spec_digest
      AND NOT EXISTS (
          SELECT 1 FROM lan_app_access_disable_intents d
          WHERE d.allocation_id = NEW.allocation_id
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant owner changed before apply'); END;

CREATE TRIGGER lan_app_access_grant_db_active_exact_owner
BEFORE UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state = 'db_active' AND NOT EXISTS (
    SELECT 1
    FROM applications app
    JOIN users u ON u.id = NEW.approved_by AND u.role = 'administrator'
    JOIN lan_app_access_heads ah ON ah.app_id = app.id
    JOIN lan_app_access_revisions ar
      ON ar.app_id = ah.app_id AND ar.id = ah.revision_id AND ar.revision_number = ah.revision_number
    JOIN lan_port_allocations a ON a.id = ar.allocation_id AND a.app_id = ar.app_id
    JOIN lan_gateway_profile_heads gh ON gh.singleton = 1
    JOIN lan_gateway_profile_revisions gr
      ON gr.id = gh.revision_id AND gr.revision_number = gh.revision_number
    WHERE app.id = NEW.app_id AND app.archived_at IS NULL
      AND ar.id = NEW.access_revision_id AND ar.revision_number = NEW.access_revision_number
      AND ar.operation_id = NEW.allocation_owner_operation_id
      AND ar.spec_digest = NEW.access_spec_digest AND ar.approved_by = NEW.approved_by
      AND ar.allocation_id = NEW.allocation_id AND ar.allocated_port = NEW.allocated_port
      AND a.owner_operation_id = NEW.allocation_owner_operation_id
      AND a.owner_revision_id = NEW.access_revision_id AND a.port = NEW.allocated_port
      AND a.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.state IN ('reserved','active','uncertain') AND a.released_at IS NULL
      AND gr.id = NEW.gateway_profile_revision_id
      AND gr.revision_number = NEW.gateway_profile_revision_number
      AND gr.spec_digest = NEW.gateway_profile_spec_digest
      AND NOT EXISTS (
          SELECT 1 FROM lan_app_access_disable_intents d
          WHERE d.allocation_id = NEW.allocation_id
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant owner changed before activation'); END;

-- Publication is not final if the original approval, exact owner, or desired
-- heads changed after activation. A failed terminal write must cause the
-- gateway transaction to withdraw the route and retain uncertainty.
CREATE TRIGGER lan_app_access_grant_committed_exact_owner
BEFORE UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state = 'committed' AND NOT EXISTS (
    SELECT 1
    FROM applications app
    JOIN users u ON u.id = NEW.approved_by AND u.role = 'administrator'
    JOIN lan_app_access_heads ah ON ah.app_id = app.id
    JOIN lan_app_access_revisions ar
      ON ar.app_id = ah.app_id AND ar.id = ah.revision_id AND ar.revision_number = ah.revision_number
    JOIN lan_port_allocations a ON a.id = ar.allocation_id AND a.app_id = ar.app_id
    JOIN lan_gateway_profile_heads gh ON gh.singleton = 1
    JOIN lan_gateway_profile_revisions gr
      ON gr.id = gh.revision_id AND gr.revision_number = gh.revision_number
    WHERE app.id = NEW.app_id AND app.archived_at IS NULL
      AND ar.id = NEW.access_revision_id AND ar.revision_number = NEW.access_revision_number
      AND ar.operation_id = NEW.allocation_owner_operation_id
      AND ar.spec_digest = NEW.access_spec_digest AND ar.approved_by = NEW.approved_by
      AND ar.allocation_id = NEW.allocation_id AND ar.allocated_port = NEW.allocated_port
      AND a.owner_operation_id = NEW.allocation_owner_operation_id
      AND a.owner_revision_id = NEW.access_revision_id AND a.port = NEW.allocated_port
      AND a.gateway_profile_revision_id = NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number = NEW.gateway_profile_revision_number
      AND a.state IN ('active','uncertain') AND a.released_at IS NULL
      AND gr.id = NEW.gateway_profile_revision_id
      AND gr.revision_number = NEW.gateway_profile_revision_number
      AND gr.spec_digest = NEW.gateway_profile_spec_digest
      AND NOT EXISTS (
          SELECT 1 FROM lan_app_access_disable_intents d
          WHERE d.allocation_id = NEW.allocation_id
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant owner changed before commit'); END;

-- State coupling is performed by triggers so the claim transition and exact
-- allocation transition commit together. This removes the SQLite/Docker
-- dual-write gap without holding a write transaction during Docker work.
CREATE TRIGGER lan_app_access_grant_activate_allocation
AFTER UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state IN ('db_active','committed')
BEGIN
    UPDATE lan_port_allocations SET state = 'active'
    WHERE id = NEW.allocation_id AND app_id = NEW.app_id
      AND owner_operation_id = NEW.allocation_owner_operation_id
      AND owner_revision_id = NEW.access_revision_id
      AND released_at IS NULL AND state IN ('reserved','uncertain');
END;

CREATE TRIGGER lan_app_access_grant_uncertain_allocation
AFTER UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state = 'uncertain'
BEGIN
    UPDATE lan_port_allocations SET state = 'uncertain'
    WHERE id = NEW.allocation_id AND app_id = NEW.app_id
      AND owner_operation_id = NEW.allocation_owner_operation_id
      AND owner_revision_id = NEW.access_revision_id
      AND released_at IS NULL AND state IN ('reserved','active');
END;

-- A proved rollback never leaves an allocation claiming active service. An
-- allocation that was already active (for example, legacy or ambiguous state)
-- remains owned but becomes uncertain; reserved and uncertain allocations are
-- retained as-is for a distinct retry attempt.
CREATE TRIGGER lan_app_access_grant_rollback_allocation
AFTER UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state = 'rolled_back'
BEGIN
    UPDATE lan_port_allocations SET state = 'uncertain'
    WHERE id = NEW.allocation_id AND app_id = NEW.app_id
      AND owner_operation_id = NEW.allocation_owner_operation_id
      AND owner_revision_id = NEW.access_revision_id
      AND released_at IS NULL AND state = 'active';
END;

-- Only claim-driven state coupling is allowed inside an unresolved/current
-- lease. The current claim state constrains the only acceptable allocation
-- state, so an unrelated writer cannot race the controller.
CREATE TRIGGER lan_port_allocation_grant_state_fence
BEFORE UPDATE OF state ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.allocation_id = OLD.id
      AND c.state IN ('applying','db_active','uncertain','committed')
      AND NOT (
          (c.state IN ('db_active','committed') AND NEW.state = 'active')
          OR (c.state = 'uncertain' AND NEW.state = 'uncertain')
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN port allocation is fenced by grant claim'); END;

CREATE TRIGGER lan_port_allocation_grant_owner_fence
BEFORE UPDATE OF owner_revision_id,released_at ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.allocation_id = OLD.id
      AND c.state IN ('prepared','applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN port allocation owner is fenced by grant claim'); END;

CREATE TRIGGER lan_app_access_grant_disable_fence
BEFORE INSERT ON lan_app_access_disable_intents
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.allocation_id = NEW.allocation_id
      AND c.app_id = NEW.app_id
      AND c.allocation_owner_operation_id = NEW.allocation_owner_operation_id
      AND c.access_revision_id = NEW.access_revision_id
      AND c.state IN ('applying','db_active','uncertain')
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant is in progress'); END;

CREATE TRIGGER lan_app_access_head_grant_fence
BEFORE UPDATE ON lan_app_access_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.app_id = OLD.app_id
      AND c.state IN ('applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN application access head is pinned by grant claim'); END;

CREATE TRIGGER lan_app_access_head_grant_delete_fence
BEFORE DELETE ON lan_app_access_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.app_id = OLD.app_id
      AND c.state IN ('prepared','applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN application access head is pinned by grant claim'); END;

CREATE TRIGGER lan_gateway_profile_head_grant_fence
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.gateway_profile_revision_id = OLD.revision_id
      AND c.gateway_profile_revision_number = OLD.revision_number
      AND c.state IN ('applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by grant claim'); END;

CREATE TRIGGER lan_gateway_profile_head_grant_delete_fence
BEFORE DELETE ON lan_gateway_profile_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.gateway_profile_revision_id = OLD.revision_id
      AND c.gateway_profile_revision_number = OLD.revision_number
      AND c.state IN ('prepared','applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by grant claim'); END;

CREATE TRIGGER lan_app_access_grant_initial_event
AFTER INSERT ON lan_app_access_grant_claims
BEGIN
    INSERT INTO lan_app_access_grant_claim_events(
        attempt_id,sequence,state,gateway_operation_id,protected_state_digest,created_at
    ) VALUES(
        NEW.attempt_id,NEW.state_sequence,NEW.state,NEW.gateway_operation_id,
        NEW.protected_state_digest,NEW.updated_at
    );
END;

CREATE TRIGGER lan_app_access_grant_event_matches_head
BEFORE INSERT ON lan_app_access_grant_claim_events
WHEN NOT EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.attempt_id = NEW.attempt_id
      AND c.state_sequence = NEW.sequence
      AND c.state = NEW.state
      AND c.gateway_operation_id IS NEW.gateway_operation_id
      AND c.protected_state_digest IS NEW.protected_state_digest
      AND c.updated_at = NEW.created_at
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant event does not match claim head'); END;

CREATE TRIGGER lan_app_access_grant_transition_event
AFTER UPDATE OF state,state_sequence,updated_at,resolution_outcome,
    gateway_operation_id,protected_state_digest,resolved_at
ON lan_app_access_grant_claims
BEGIN
    INSERT INTO lan_app_access_grant_claim_events(
        attempt_id,sequence,state,gateway_operation_id,protected_state_digest,created_at
    ) VALUES(
        NEW.attempt_id,NEW.state_sequence,NEW.state,NEW.gateway_operation_id,
        NEW.protected_state_digest,NEW.updated_at
    );
END;

CREATE TRIGGER lan_app_access_grant_claim_retain
BEFORE DELETE ON lan_app_access_grant_claims
BEGIN SELECT RAISE(ABORT, 'LAN application access grant claims are retained'); END;

CREATE TRIGGER lan_app_access_grant_event_immutable_update
BEFORE UPDATE ON lan_app_access_grant_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN application access grant events are immutable'); END;

CREATE TRIGGER lan_app_access_grant_event_retain
BEFORE DELETE ON lan_app_access_grant_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN application access grant events are retained'); END;

CREATE TRIGGER lan_app_access_grant_audit_immutable_update
BEFORE UPDATE ON audit_events
WHEN OLD.resource_type = 'lan_app_access_grant'
BEGIN SELECT RAISE(ABORT, 'LAN application access grant audit events are immutable'); END;

CREATE TRIGGER lan_app_access_grant_audit_retain
BEFORE DELETE ON audit_events
WHEN OLD.resource_type = 'lan_app_access_grant'
BEGIN SELECT RAISE(ABORT, 'LAN application access grant audit events are retained'); END;
