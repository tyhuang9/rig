-- Disable approval remains the immutable intent recorded by migration 027.
-- This migration adds the crash-safe execution claim and the narrow database
-- coupling needed to retire a proved grant and release its exact allocation.

-- Keep migration 025's table and owner history intact. disabled_at is the
-- terminal release timestamp for an owned allocation; repository reads expose
-- COALESCE(released_at,disabled_at) as Allocation.ReleasedAt.
ALTER TABLE lan_port_allocations ADD COLUMN disabled_at TEXT
    CHECK (disabled_at IS NULL OR length(disabled_at) > 0);

DROP INDEX lan_port_allocations_live_port;
DROP INDEX lan_port_allocations_live_app;
CREATE UNIQUE INDEX lan_port_allocations_live_port
ON lan_port_allocations(port) WHERE released_at IS NULL AND disabled_at IS NULL;
CREATE UNIQUE INDEX lan_port_allocations_live_app
ON lan_port_allocations(app_id) WHERE released_at IS NULL AND disabled_at IS NULL;

ALTER TABLE lan_app_access_grant_claims ADD COLUMN retired_at TEXT;
ALTER TABLE lan_app_access_grant_claims ADD COLUMN retired_by_disable_operation_id TEXT
    REFERENCES lan_app_access_disable_intents(operation_id);

DROP INDEX lan_app_access_grant_claims_current_allocation;
CREATE UNIQUE INDEX lan_app_access_grant_claims_current_allocation
ON lan_app_access_grant_claims(allocation_id)
WHERE state IN ('prepared','applying','db_active','uncertain','committed') AND retired_at IS NULL;

CREATE TABLE lan_app_access_disable_claims (
    operation_id TEXT PRIMARY KEY REFERENCES lan_app_access_disable_intents(operation_id),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    approval_action TEXT NOT NULL CHECK (approval_action = 'disable_lan_access'),
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    allocation_id TEXT NOT NULL UNIQUE REFERENCES lan_port_allocations(id),
    allocation_owner_operation_id TEXT NOT NULL CHECK (length(allocation_owner_operation_id) BETWEEN 1 AND 128),
    access_revision_id TEXT NOT NULL,
    access_revision_number INTEGER NOT NULL CHECK (access_revision_number > 0),
    allocated_port INTEGER NOT NULL CHECK (allocated_port BETWEEN 8100 AND 8119),
    gateway_profile_revision_id TEXT NOT NULL,
    gateway_profile_revision_number INTEGER NOT NULL CHECK (gateway_profile_revision_number > 0),
    spec_digest TEXT NOT NULL CHECK (length(spec_digest) = 64 AND spec_digest NOT GLOB '*[^0-9a-f]*'),
    approved_by TEXT NOT NULL REFERENCES users(id),
    approved_at TEXT NOT NULL,
    source_grant_attempt_id TEXT REFERENCES lan_app_access_grant_claims(attempt_id),
    created_at TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepared','withdrawing','uncertain','committed')),
    state_sequence INTEGER NOT NULL DEFAULT 1 CHECK (state_sequence > 0),
    updated_at TEXT NOT NULL,
    gateway_operation_id TEXT,
    protected_state_digest TEXT CHECK (protected_state_digest IS NULL OR (length(protected_state_digest) = 64 AND protected_state_digest NOT GLOB '*[^0-9a-f]*')),
    resolved_at TEXT,
    UNIQUE(app_id, operation_id),
    FOREIGN KEY(app_id, access_revision_id) REFERENCES lan_app_access_revisions(app_id, id),
    FOREIGN KEY(gateway_profile_revision_id, gateway_profile_revision_number)
        REFERENCES lan_gateway_profile_revisions(id, revision_number),
    CHECK (state_sequence > 1 OR state = 'prepared'),
    CHECK (
        (state IN ('prepared','withdrawing','uncertain')
            AND gateway_operation_id IS NULL AND protected_state_digest IS NULL AND resolved_at IS NULL)
        OR
        (state = 'committed'
            AND gateway_operation_id IS NOT NULL AND protected_state_digest IS NOT NULL AND resolved_at IS NOT NULL)
    )
);

CREATE INDEX lan_app_access_disable_claims_startup
ON lan_app_access_disable_claims(state, app_id, operation_id);

-- Existing migration-027 intents may be present for several apps. Prepared
-- claims are retained queued work; only one claim may occupy the external
-- mutation window, including uncertainty, across disable attempts.
CREATE UNIQUE INDEX lan_app_access_disable_claims_one_mutation
ON lan_app_access_disable_claims(approval_action)
WHERE state IN ('withdrawing','uncertain');

CREATE TABLE lan_app_access_disable_claim_events (
    operation_id TEXT NOT NULL REFERENCES lan_app_access_disable_claims(operation_id),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    state TEXT NOT NULL CHECK (state IN ('prepared','withdrawing','uncertain','committed')),
    gateway_operation_id TEXT,
    protected_state_digest TEXT CHECK (protected_state_digest IS NULL OR (length(protected_state_digest) = 64 AND protected_state_digest NOT GLOB '*[^0-9a-f]*')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(operation_id, sequence),
    UNIQUE(operation_id, state),
    CHECK (
        (state = 'committed' AND gateway_operation_id IS NOT NULL AND protected_state_digest IS NOT NULL)
        OR
        (state IN ('prepared','withdrawing','uncertain') AND gateway_operation_id IS NULL AND protected_state_digest IS NULL)
    )
);

CREATE TRIGGER lan_app_access_disable_claim_initial_event
AFTER INSERT ON lan_app_access_disable_claims
BEGIN
    INSERT INTO lan_app_access_disable_claim_events(
        operation_id,sequence,state,gateway_operation_id,protected_state_digest,created_at
    ) VALUES(
        NEW.operation_id,NEW.state_sequence,NEW.state,NEW.gateway_operation_id,
        NEW.protected_state_digest,NEW.updated_at
    );
END;

-- Every migration-027 intent becomes a prepared claim. A matching committed
-- grant is linked when one exists; older installations may legitimately have
-- applied LAN state without a migration-028 grant claim. Legacy intents may
-- coexist with unfinished grant attempts, so the new-insert fence follows
-- this backfill. Startup inspection must then quarantine or fail closed.
INSERT INTO lan_app_access_disable_claims(
    operation_id,request_digest,approval_action,app_id,allocation_id,
    allocation_owner_operation_id,access_revision_id,access_revision_number,
    allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
    spec_digest,approved_by,approved_at,source_grant_attempt_id,
    created_at,state,state_sequence,updated_at
)
SELECT d.operation_id,d.request_digest,d.approval_action,d.app_id,d.allocation_id,
    d.allocation_owner_operation_id,d.access_revision_id,d.access_revision_number,
    d.allocated_port,d.gateway_profile_revision_id,d.gateway_profile_revision_number,
    d.spec_digest,d.approved_by,d.approved_at,
    (
        SELECT g.attempt_id FROM lan_app_access_grant_claims g
        WHERE g.app_id=d.app_id AND g.allocation_id=d.allocation_id
          AND g.allocation_owner_operation_id=d.allocation_owner_operation_id
          AND g.access_revision_id=d.access_revision_id
          AND g.state='committed' AND g.retired_at IS NULL
        ORDER BY g.updated_at DESC,g.attempt_id DESC LIMIT 1
    ),
    d.approved_at,'prepared',1,d.approved_at
FROM lan_app_access_disable_intents d;

CREATE TRIGGER lan_app_access_disable_claim_no_unresolved_grant_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims g
    WHERE g.state IN ('prepared','applying','db_active','uncertain')
      AND g.retired_at IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway grant operation is unresolved'); END;

CREATE TRIGGER lan_app_access_disable_claim_matches_intent_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN NOT EXISTS (
    SELECT 1 FROM lan_app_access_disable_intents d
    WHERE d.operation_id=NEW.operation_id AND d.request_digest=NEW.request_digest
      AND d.approval_action=NEW.approval_action AND d.app_id=NEW.app_id
      AND d.allocation_id=NEW.allocation_id
      AND d.allocation_owner_operation_id=NEW.allocation_owner_operation_id
      AND d.access_revision_id=NEW.access_revision_id
      AND d.access_revision_number=NEW.access_revision_number
      AND d.allocated_port=NEW.allocated_port
      AND d.gateway_profile_revision_id=NEW.gateway_profile_revision_id
      AND d.gateway_profile_revision_number=NEW.gateway_profile_revision_number
      AND d.spec_digest=NEW.spec_digest AND d.approved_by=NEW.approved_by
      AND d.approved_at=NEW.approved_at
)
BEGIN SELECT RAISE(ABORT, 'LAN application access disable claim must match immutable intent'); END;

CREATE TRIGGER lan_app_access_disable_claim_source_grant_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN (
    NEW.source_grant_attempt_id IS NULL AND EXISTS (
        SELECT 1 FROM lan_app_access_grant_claims g
        WHERE g.app_id=NEW.app_id AND g.allocation_id=NEW.allocation_id
          AND g.allocation_owner_operation_id=NEW.allocation_owner_operation_id
          AND g.access_revision_id=NEW.access_revision_id
          AND g.state='committed' AND g.retired_at IS NULL
    )
) OR (
    NEW.source_grant_attempt_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM lan_app_access_grant_claims g
        WHERE g.attempt_id=NEW.source_grant_attempt_id AND g.app_id=NEW.app_id
          AND g.allocation_id=NEW.allocation_id
          AND g.allocation_owner_operation_id=NEW.allocation_owner_operation_id
          AND g.access_revision_id=NEW.access_revision_id
          AND g.state='committed' AND g.retired_at IS NULL
    )
)
BEGIN SELECT RAISE(ABORT, 'LAN application access disable claim source grant is not exact and committed'); END;

CREATE TRIGGER lan_app_access_disable_claim_prepared_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN NEW.state <> 'prepared' OR NEW.state_sequence <> 1
  OR NEW.updated_at <> NEW.created_at
  OR NEW.gateway_operation_id IS NOT NULL OR NEW.protected_state_digest IS NOT NULL OR NEW.resolved_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'LAN application access disable claim must begin prepared'); END;

CREATE TRIGGER lan_app_access_disable_claim_identity_immutable
BEFORE UPDATE OF operation_id,request_digest,approval_action,app_id,allocation_id,
    allocation_owner_operation_id,access_revision_id,access_revision_number,
    allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
    spec_digest,approved_by,approved_at,source_grant_attempt_id,created_at
ON lan_app_access_disable_claims
BEGIN SELECT RAISE(ABORT, 'LAN application access disable claim identity is immutable'); END;

CREATE TRIGGER lan_app_access_disable_claim_state_transition
BEFORE UPDATE OF state,state_sequence,updated_at,gateway_operation_id,
    protected_state_digest,resolved_at
ON lan_app_access_disable_claims
WHEN NOT (
    NEW.state_sequence = OLD.state_sequence + 1
    AND NEW.updated_at >= OLD.updated_at
    AND (
        (OLD.state='prepared' AND NEW.state='withdrawing')
        OR (OLD.state='withdrawing' AND NEW.state IN ('uncertain','committed'))
        OR (OLD.state='uncertain' AND NEW.state='committed')
    )
    AND (
        (NEW.state IN ('withdrawing','uncertain')
            AND NEW.gateway_operation_id IS NULL AND NEW.protected_state_digest IS NULL AND NEW.resolved_at IS NULL)
        OR
        (NEW.state='committed'
            AND NEW.gateway_operation_id IS NOT NULL AND NEW.protected_state_digest IS NOT NULL
            AND NEW.resolved_at=NEW.updated_at)
    )
)
BEGIN SELECT RAISE(ABORT, 'invalid LAN application access disable transition'); END;

-- Entering or recovering the gateway mutation window requires the exact live
-- head, owner, profile, and (when present) committed source grant. The
-- repository validates the current executing administrator separately from
-- the immutable original approver.
CREATE TRIGGER lan_app_access_disable_claim_exact_owner_transition
BEFORE UPDATE OF state ON lan_app_access_disable_claims
WHEN NEW.state IN ('withdrawing','uncertain','committed') AND NOT EXISTS (
    SELECT 1
    FROM applications app
    JOIN lan_app_access_heads ah ON ah.app_id=app.id
    JOIN lan_app_access_revisions ar
      ON ar.app_id=ah.app_id AND ar.id=ah.revision_id AND ar.revision_number=ah.revision_number
    JOIN lan_port_allocations a ON a.id=ar.allocation_id AND a.app_id=ar.app_id
    JOIN lan_gateway_profile_heads gh ON gh.singleton=1
    JOIN lan_gateway_profile_revisions gr
      ON gr.id=gh.revision_id AND gr.revision_number=gh.revision_number
    WHERE app.id=NEW.app_id AND app.archived_at IS NULL
      AND ar.id=NEW.access_revision_id AND ar.revision_number=NEW.access_revision_number
      AND ar.operation_id=NEW.allocation_owner_operation_id
      AND ar.allocation_id=NEW.allocation_id AND ar.allocated_port=NEW.allocated_port
      AND a.owner_operation_id=NEW.allocation_owner_operation_id
      AND a.owner_revision_id=NEW.access_revision_id AND a.port=NEW.allocated_port
      AND a.gateway_profile_revision_id=NEW.gateway_profile_revision_id
      AND a.gateway_profile_revision_number=NEW.gateway_profile_revision_number
      AND a.released_at IS NULL AND a.disabled_at IS NULL
      AND gr.id=NEW.gateway_profile_revision_id
      AND gr.revision_number=NEW.gateway_profile_revision_number
      AND (
          (NEW.source_grant_attempt_id IS NULL AND NOT EXISTS (
              SELECT 1 FROM lan_app_access_grant_claims g
              WHERE g.app_id=NEW.app_id AND g.allocation_id=NEW.allocation_id
                AND g.allocation_owner_operation_id=NEW.allocation_owner_operation_id
                AND g.access_revision_id=NEW.access_revision_id
                AND g.state='committed' AND g.retired_at IS NULL
          )) OR EXISTS (
              SELECT 1 FROM lan_app_access_grant_claims g
              WHERE g.attempt_id=NEW.source_grant_attempt_id AND g.app_id=NEW.app_id
                AND g.allocation_id=NEW.allocation_id
                AND g.allocation_owner_operation_id=NEW.allocation_owner_operation_id
                AND g.access_revision_id=NEW.access_revision_id
                AND g.state='committed' AND g.retired_at IS NULL
          )
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN application access disable owner changed before withdrawal'); END;

CREATE TRIGGER lan_app_access_disable_claim_no_grant_mutation
BEFORE UPDATE OF state ON lan_app_access_disable_claims
WHEN NEW.state IN ('withdrawing','uncertain') AND EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims g
    WHERE g.state IN ('applying','db_active','uncertain') AND g.retired_at IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway grant mutation is unresolved'); END;

CREATE TRIGGER lan_app_access_grant_no_disable_mutation
BEFORE UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state IN ('applying','db_active','uncertain') AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    WHERE d.state IN ('withdrawing','uncertain')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable mutation is unresolved'); END;

CREATE TRIGGER lan_app_access_grant_no_pending_disable_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    WHERE d.state IN ('prepared','withdrawing','uncertain')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable operation is unresolved'); END;

CREATE TRIGGER lan_app_access_disable_claim_event_matches_head
BEFORE INSERT ON lan_app_access_disable_claim_events
WHEN NOT EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims c
    WHERE c.operation_id=NEW.operation_id AND c.state_sequence=NEW.sequence
      AND c.state=NEW.state AND c.gateway_operation_id IS NEW.gateway_operation_id
      AND c.protected_state_digest IS NEW.protected_state_digest
      AND c.updated_at=NEW.created_at
)
BEGIN SELECT RAISE(ABORT, 'LAN application access disable event does not match claim head'); END;

CREATE TRIGGER lan_app_access_disable_claim_transition_event
AFTER UPDATE OF state,state_sequence,updated_at,gateway_operation_id,
    protected_state_digest,resolved_at
ON lan_app_access_disable_claims
BEGIN
    INSERT INTO lan_app_access_disable_claim_events(
        operation_id,sequence,state,gateway_operation_id,protected_state_digest,created_at
    ) VALUES(
        NEW.operation_id,NEW.state_sequence,NEW.state,NEW.gateway_operation_id,
        NEW.protected_state_digest,NEW.updated_at
    );
END;

-- This is the only path that retires an applied grant and releases an owned
-- allocation. The exact checks are repeated in the update guards below.
CREATE TRIGGER lan_app_access_disable_claim_commit
AFTER UPDATE OF state ON lan_app_access_disable_claims
WHEN NEW.state='committed'
BEGIN
    UPDATE lan_app_access_grant_claims
    SET retired_at=NEW.resolved_at,retired_by_disable_operation_id=NEW.operation_id
    WHERE attempt_id=NEW.source_grant_attempt_id AND app_id=NEW.app_id
      AND allocation_id=NEW.allocation_id
      AND allocation_owner_operation_id=NEW.allocation_owner_operation_id
      AND access_revision_id=NEW.access_revision_id
      AND state='committed' AND retired_at IS NULL;

    UPDATE lan_port_allocations SET disabled_at=NEW.resolved_at
    WHERE id=NEW.allocation_id AND app_id=NEW.app_id
      AND owner_operation_id=NEW.allocation_owner_operation_id
      AND owner_revision_id=NEW.access_revision_id AND port=NEW.allocated_port
      AND gateway_profile_revision_id=NEW.gateway_profile_revision_id
      AND gateway_profile_revision_number=NEW.gateway_profile_revision_number
      AND released_at IS NULL AND disabled_at IS NULL;
END;

CREATE TRIGGER lan_app_access_grant_retirement_guard
BEFORE UPDATE OF retired_at,retired_by_disable_operation_id ON lan_app_access_grant_claims
WHEN NOT (
    OLD.retired_at IS NULL AND OLD.retired_by_disable_operation_id IS NULL
    AND NEW.retired_at IS NOT NULL AND NEW.retired_by_disable_operation_id IS NOT NULL
    AND OLD.state='committed' AND NEW.state=OLD.state
    AND EXISTS (
        SELECT 1 FROM lan_app_access_disable_claims d
        WHERE d.operation_id=NEW.retired_by_disable_operation_id
          AND d.state='committed' AND d.resolved_at=NEW.retired_at
          AND d.source_grant_attempt_id=OLD.attempt_id
          AND d.app_id=OLD.app_id AND d.allocation_id=OLD.allocation_id
          AND d.allocation_owner_operation_id=OLD.allocation_owner_operation_id
          AND d.access_revision_id=OLD.access_revision_id
    )
)
BEGIN SELECT RAISE(ABORT, 'LAN application access grant retirement requires exact disable commit'); END;

CREATE TRIGGER lan_app_access_disable_claim_retain
BEFORE DELETE ON lan_app_access_disable_claims
BEGIN SELECT RAISE(ABORT, 'LAN application access disable claims are retained'); END;

CREATE TRIGGER lan_app_access_disable_claim_event_immutable_update
BEFORE UPDATE ON lan_app_access_disable_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN application access disable events are immutable'); END;

CREATE TRIGGER lan_app_access_disable_claim_event_retain
BEFORE DELETE ON lan_app_access_disable_claim_events
BEGIN SELECT RAISE(ABORT, 'LAN application access disable events are retained'); END;

DROP TRIGGER lan_port_allocation_disable_intent_freeze;
CREATE TRIGGER lan_port_allocation_disable_intent_freeze
BEFORE UPDATE OF state, owner_revision_id, released_at, disabled_at ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_intents d
    WHERE d.allocation_id=OLD.id AND d.app_id=OLD.app_id
      AND d.allocation_owner_operation_id=OLD.owner_operation_id
      AND d.access_revision_id=OLD.owner_revision_id AND d.allocated_port=OLD.port
      AND d.gateway_profile_revision_id=OLD.gateway_profile_revision_id
      AND d.gateway_profile_revision_number=OLD.gateway_profile_revision_number
) AND NOT (
    OLD.state=NEW.state AND OLD.owner_revision_id IS NEW.owner_revision_id
    AND OLD.released_at IS NEW.released_at
    AND OLD.disabled_at IS NULL AND NEW.disabled_at IS NOT NULL
    AND EXISTS (
        SELECT 1 FROM lan_app_access_disable_claims c
        WHERE c.operation_id IN (
            SELECT operation_id FROM lan_app_access_disable_intents WHERE allocation_id=OLD.id
        ) AND c.state='committed' AND c.resolved_at=NEW.disabled_at
    )
)
BEGIN SELECT RAISE(ABORT, 'LAN port allocation is frozen by disable intent'); END;

DROP TRIGGER lan_port_allocation_grant_owner_fence;
CREATE TRIGGER lan_port_allocation_grant_owner_fence
BEFORE UPDATE OF owner_revision_id,released_at,disabled_at ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.allocation_id=OLD.id AND c.retired_at IS NULL
      AND c.state IN ('prepared','applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN port allocation owner is fenced by grant claim'); END;

CREATE TRIGGER lan_port_allocation_disable_release_guard
BEFORE UPDATE OF disabled_at ON lan_port_allocations
WHEN NOT (
    OLD.disabled_at IS NULL AND NEW.disabled_at IS NOT NULL
    AND OLD.released_at IS NULL
    AND EXISTS (
        SELECT 1 FROM lan_app_access_disable_claims d
        WHERE d.state='committed' AND d.resolved_at=NEW.disabled_at
          AND d.app_id=OLD.app_id AND d.allocation_id=OLD.id
          AND d.allocation_owner_operation_id=OLD.owner_operation_id
          AND d.access_revision_id=OLD.owner_revision_id
          AND d.allocated_port=OLD.port
          AND d.gateway_profile_revision_id=OLD.gateway_profile_revision_id
          AND d.gateway_profile_revision_number=OLD.gateway_profile_revision_number
    )
)
BEGIN SELECT RAISE(ABORT, 'LAN port allocation disable release requires exact disable commit'); END;

DROP TRIGGER lan_app_access_archive_locked;
CREATE TRIGGER lan_app_access_archive_locked
BEFORE UPDATE OF archived_at ON applications
WHEN NEW.archived_at IS NOT NULL AND OLD.archived_at IS NULL
  AND EXISTS (
      SELECT 1 FROM lan_port_allocations a
      WHERE a.app_id=OLD.id AND a.released_at IS NULL AND a.disabled_at IS NULL
  )
BEGIN SELECT RAISE(ABORT, 'release LAN access before archiving application'); END;

-- Retired grants no longer pin desired heads; immutable revisions and the
-- released allocation remain available for audit and exact replay.
DROP TRIGGER lan_app_access_head_grant_fence;
DROP TRIGGER lan_app_access_head_grant_delete_fence;
DROP TRIGGER lan_gateway_profile_head_grant_fence;
DROP TRIGGER lan_gateway_profile_head_grant_delete_fence;

CREATE TRIGGER lan_app_access_head_grant_fence
BEFORE UPDATE ON lan_app_access_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.app_id=OLD.app_id AND c.retired_at IS NULL
      AND c.state IN ('applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN application access head is pinned by grant claim'); END;

CREATE TRIGGER lan_app_access_head_grant_delete_fence
BEFORE DELETE ON lan_app_access_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.app_id=OLD.app_id AND c.retired_at IS NULL
      AND c.state IN ('prepared','applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN application access head is pinned by grant claim'); END;

CREATE TRIGGER lan_gateway_profile_head_grant_fence
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.gateway_profile_revision_id=OLD.revision_id
      AND c.gateway_profile_revision_number=OLD.revision_number
      AND c.retired_at IS NULL
      AND c.state IN ('applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by grant claim'); END;

CREATE TRIGGER lan_gateway_profile_head_grant_delete_fence
BEFORE DELETE ON lan_gateway_profile_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.gateway_profile_revision_id=OLD.revision_id
      AND c.gateway_profile_revision_number=OLD.revision_number
      AND c.retired_at IS NULL
      AND c.state IN ('prepared','applying','db_active','uncertain','committed')
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by grant claim'); END;
