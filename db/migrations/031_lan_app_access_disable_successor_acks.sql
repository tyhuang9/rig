-- A historical disable can remain unacknowledged after a newer committed
-- grant legitimately supersedes its old route. Retain a separate proof of
-- that successor; never reinterpret an exact-404 acknowledgment.
CREATE TABLE lan_app_access_disable_successor_acks (
    operation_id TEXT PRIMARY KEY REFERENCES lan_app_access_disable_claims(operation_id),
    gateway_operation_id TEXT NOT NULL,
    successor_attempt_id TEXT NOT NULL REFERENCES lan_app_access_grant_claims(attempt_id),
    successor_allocation_id TEXT NOT NULL REFERENCES lan_port_allocations(id),
    relation TEXT NOT NULL CHECK (relation IN ('same_app_new_port_404','same_port_successor')),
    final_protected_state_digest TEXT NOT NULL CHECK (
        length(final_protected_state_digest)=64
        AND final_protected_state_digest NOT GLOB '*[^0-9a-f]*'
    ),
    observed_at TEXT NOT NULL,
    acknowledged_at TEXT NOT NULL
);

CREATE TRIGGER lan_app_access_disable_successor_ack_exact
BEFORE INSERT ON lan_app_access_disable_successor_acks
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_clear_acks a
    WHERE a.operation_id=NEW.operation_id
) OR NEW.acknowledged_at<NEW.observed_at OR NOT EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    JOIN lan_port_allocations old_a ON old_a.id=d.allocation_id
    JOIN lan_app_access_grant_claims g ON g.attempt_id=NEW.successor_attempt_id
    JOIN lan_port_allocations a ON a.id=g.allocation_id
    JOIN applications app ON app.id=g.app_id
    JOIN lan_app_access_heads ah ON ah.app_id=g.app_id
    JOIN lan_app_access_revisions ar ON ar.app_id=g.app_id AND ar.id=g.access_revision_id
    JOIN lan_gateway_profile_heads gh ON gh.singleton=1
    JOIN lan_gateway_profile_revisions gr ON gr.id=g.gateway_profile_revision_id
    WHERE d.operation_id=NEW.operation_id AND d.state='committed'
      AND d.gateway_operation_id=NEW.gateway_operation_id
      AND NEW.observed_at>=d.resolved_at
      AND old_a.app_id=d.app_id AND old_a.owner_operation_id=d.allocation_owner_operation_id
      AND old_a.owner_revision_id=d.access_revision_id AND old_a.port=d.allocated_port
      AND old_a.gateway_profile_revision_id=d.gateway_profile_revision_id
      AND old_a.gateway_profile_revision_number=d.gateway_profile_revision_number
      AND old_a.disabled_at=d.resolved_at AND old_a.released_at IS NULL
      AND (d.source_grant_attempt_id IS NULL OR EXISTS (
          SELECT 1 FROM lan_app_access_grant_claims old_g
          WHERE old_g.attempt_id=d.source_grant_attempt_id
            AND old_g.app_id=d.app_id AND old_g.allocation_id=d.allocation_id
            AND old_g.allocation_owner_operation_id=d.allocation_owner_operation_id
            AND old_g.access_revision_id=d.access_revision_id
            AND old_g.state='committed' AND old_g.retired_at=d.resolved_at
            AND old_g.retired_by_disable_operation_id=d.operation_id
      ))
      AND g.allocation_id=NEW.successor_allocation_id
      AND g.allocation_id<>d.allocation_id
      AND g.state='committed' AND g.retired_at IS NULL
      AND g.retired_by_disable_operation_id IS NULL
      AND g.created_at>d.resolved_at
      AND g.resolved_at IS NOT NULL AND NEW.observed_at>=g.resolved_at
      AND (
          (NEW.relation='same_app_new_port_404'
              AND g.app_id=d.app_id AND g.allocated_port<>d.allocated_port)
          OR (NEW.relation='same_port_successor'
              AND g.allocated_port=d.allocated_port)
      )
      AND app.archived_at IS NULL
      AND ah.revision_id=g.access_revision_id
      AND ah.revision_number=g.access_revision_number
      AND ar.revision_number=g.access_revision_number
      AND ar.operation_id=g.allocation_owner_operation_id
      AND ar.approval_action=g.approval_action
      AND ar.spec_digest=g.access_spec_digest
      AND ar.approved_by=g.approved_by AND ar.approved_at=g.approved_at
      AND ar.allocation_id=g.allocation_id AND ar.allocated_port=g.allocated_port
      AND ar.gateway_profile_revision_id=g.gateway_profile_revision_id
      AND ar.gateway_profile_revision_number=g.gateway_profile_revision_number
      AND a.app_id=g.app_id AND a.owner_operation_id=g.allocation_owner_operation_id
      AND a.owner_revision_id=g.access_revision_id AND a.port=g.allocated_port
      AND a.gateway_profile_revision_id=g.gateway_profile_revision_id
      AND a.gateway_profile_revision_number=g.gateway_profile_revision_number
      AND a.state='active' AND a.released_at IS NULL AND a.disabled_at IS NULL
      AND gh.revision_id=g.gateway_profile_revision_id
      AND gh.revision_number=g.gateway_profile_revision_number
      AND gr.revision_number=g.gateway_profile_revision_number
      AND gr.spec_digest=g.gateway_profile_spec_digest
)
BEGIN SELECT RAISE(ABORT, 'LAN disable successor acknowledgment requires exact active successor'); END;

CREATE TRIGGER lan_app_access_disable_successor_ack_immutable
BEFORE UPDATE ON lan_app_access_disable_successor_acks
BEGIN SELECT RAISE(ABORT, 'LAN disable successor acknowledgments are immutable'); END;

CREATE TRIGGER lan_app_access_disable_successor_ack_retain
BEFORE DELETE ON lan_app_access_disable_successor_acks
BEGIN SELECT RAISE(ABORT, 'LAN disable successor acknowledgments are retained'); END;

DROP TRIGGER lan_app_access_disable_clear_ack_exact_commit;
CREATE TRIGGER lan_app_access_disable_clear_ack_exact_commit
BEFORE INSERT ON lan_app_access_disable_clear_acks
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_successor_acks s
    WHERE s.operation_id=NEW.operation_id
) OR NOT EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    WHERE d.operation_id=NEW.operation_id AND d.state='committed'
      AND d.gateway_operation_id=NEW.gateway_operation_id
      AND NEW.observed_at>=d.resolved_at
      AND EXISTS (
          SELECT 1 FROM lan_port_allocations a
          WHERE a.id=d.allocation_id AND a.app_id=d.app_id
            AND a.owner_operation_id=d.allocation_owner_operation_id
            AND a.owner_revision_id=d.access_revision_id
            AND a.port=d.allocated_port
            AND a.gateway_profile_revision_id=d.gateway_profile_revision_id
            AND a.gateway_profile_revision_number=d.gateway_profile_revision_number
            AND a.disabled_at=d.resolved_at AND a.released_at IS NULL
      )
      AND (
          d.source_grant_attempt_id IS NULL OR EXISTS (
              SELECT 1 FROM lan_app_access_grant_claims g
              WHERE g.attempt_id=d.source_grant_attempt_id
                AND g.app_id=d.app_id AND g.allocation_id=d.allocation_id
                AND g.allocation_owner_operation_id=d.allocation_owner_operation_id
                AND g.access_revision_id=d.access_revision_id
                AND g.state='committed' AND g.retired_at=d.resolved_at
                AND g.retired_by_disable_operation_id=d.operation_id
          )
      )
) OR NEW.acknowledged_at<NEW.observed_at
BEGIN SELECT RAISE(ABORT, 'LAN disable clear acknowledgment requires exact committed proof'); END;

-- Recreate every 030 admission gate so either immutable acknowledgment can
-- release it. Nonterminal disables still block all new LAN work.
DROP TRIGGER lan_app_access_grant_no_pending_disable_insert;
CREATE TRIGGER lan_app_access_grant_no_pending_disable_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_app_access_grant_no_uncleared_disable_apply;
CREATE TRIGGER lan_app_access_grant_no_uncleared_disable_apply
BEFORE UPDATE OF state ON lan_app_access_grant_claims
WHEN OLD.state='prepared' AND NEW.state='applying' AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_port_allocation_no_uncleared_disable_insert;
CREATE TRIGGER lan_port_allocation_no_uncleared_disable_insert
BEFORE INSERT ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_app_access_revision_no_uncleared_disable_insert;
CREATE TRIGGER lan_app_access_revision_no_uncleared_disable_insert
BEFORE INSERT ON lan_app_access_revisions
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_app_access_head_no_uncleared_disable_advance;
CREATE TRIGGER lan_app_access_head_no_uncleared_disable_advance
BEFORE UPDATE OF revision_id,revision_number ON lan_app_access_heads
WHEN (NEW.revision_id IS NOT OLD.revision_id OR NEW.revision_number<>OLD.revision_number)
  AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_port_allocation_no_uncleared_disable_owner;
CREATE TRIGGER lan_port_allocation_no_uncleared_disable_owner
BEFORE UPDATE OF owner_revision_id ON lan_port_allocations
WHEN OLD.owner_revision_id IS NULL AND NEW.owner_revision_id IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_port_allocation_no_uncleared_disable_activate;
CREATE TRIGGER lan_port_allocation_no_uncleared_disable_activate
BEFORE UPDATE OF state ON lan_port_allocations
WHEN NEW.state='active' AND OLD.state<>'active' AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_app_access_disable_intent_no_uncleared_disable_insert;
CREATE TRIGGER lan_app_access_disable_intent_no_uncleared_disable_insert
BEFORE INSERT ON lan_app_access_disable_intents
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'prior LAN gateway disable clear is unresolved'); END;

DROP TRIGGER lan_app_access_disable_claim_no_uncleared_disable_insert;
CREATE TRIGGER lan_app_access_disable_claim_no_uncleared_disable_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
    WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
)
BEGIN SELECT RAISE(ABORT, 'prior LAN gateway disable clear is unresolved'); END;
