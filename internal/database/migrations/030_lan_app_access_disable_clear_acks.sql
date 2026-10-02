-- A committed disable releases the database allocation before generated
-- ingress has necessarily cleared its protected pending marker. The clear
-- acknowledgment is a separate immutable fact, written only after the
-- controller has freshly inspected and finalized that exact route.
CREATE TABLE lan_app_access_disable_clear_acks (
    operation_id TEXT PRIMARY KEY REFERENCES lan_app_access_disable_claims(operation_id),
    proof_kind TEXT NOT NULL CHECK (proof_kind='exact_404'),
    gateway_operation_id TEXT NOT NULL,
    final_protected_state_digest TEXT NOT NULL CHECK (
        length(final_protected_state_digest)=64
        AND final_protected_state_digest NOT GLOB '*[^0-9a-f]*'
    ),
    observed_at TEXT NOT NULL,
    acknowledged_at TEXT NOT NULL
);

CREATE TRIGGER lan_app_access_disable_clear_ack_exact_commit
BEFORE INSERT ON lan_app_access_disable_clear_acks
WHEN NOT EXISTS (
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

CREATE TRIGGER lan_app_access_disable_clear_ack_immutable
BEFORE UPDATE ON lan_app_access_disable_clear_acks
BEGIN SELECT RAISE(ABORT, 'LAN disable clear acknowledgments are immutable'); END;

CREATE TRIGGER lan_app_access_disable_clear_ack_retain
BEFORE DELETE ON lan_app_access_disable_clear_acks
BEGIN SELECT RAISE(ABORT, 'LAN disable clear acknowledgments are retained'); END;

-- Existing 029 committed claims intentionally receive no synthetic ack:
-- startup must verify their final protected state before admitting new work.
DROP TRIGGER lan_app_access_grant_no_pending_disable_insert;
CREATE TRIGGER lan_app_access_grant_no_pending_disable_insert
BEFORE INSERT ON lan_app_access_grant_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_app_access_grant_no_uncleared_disable_apply
BEFORE UPDATE OF state ON lan_app_access_grant_claims
WHEN OLD.state='prepared' AND NEW.state='applying' AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_port_allocation_no_uncleared_disable_insert
BEFORE INSERT ON lan_port_allocations
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

-- A reservation made before a disable may still exist when the disable commits.
-- It cannot acquire a new access head or become active until the clear proof
-- has been retained. Safety transitions toward uncertainty remain available.
CREATE TRIGGER lan_app_access_revision_no_uncleared_disable_insert
BEFORE INSERT ON lan_app_access_revisions
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_app_access_head_no_uncleared_disable_advance
BEFORE UPDATE OF revision_id,revision_number ON lan_app_access_heads
WHEN (NEW.revision_id IS NOT OLD.revision_id OR NEW.revision_number<>OLD.revision_number)
  AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_port_allocation_no_uncleared_disable_owner
BEFORE UPDATE OF owner_revision_id ON lan_port_allocations
WHEN OLD.owner_revision_id IS NULL AND NEW.owner_revision_id IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_port_allocation_no_uncleared_disable_activate
BEFORE UPDATE OF state ON lan_port_allocations
WHEN NEW.state='active' AND OLD.state<>'active' AND EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_app_access_disable_intent_no_uncleared_disable_insert
BEFORE INSERT ON lan_app_access_disable_intents
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'prior LAN gateway disable clear is unresolved'); END;

CREATE TRIGGER lan_app_access_disable_claim_no_uncleared_disable_insert
BEFORE INSERT ON lan_app_access_disable_claims
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_disable_claims d
    LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
    WHERE d.state<>'committed' OR a.operation_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'prior LAN gateway disable clear is unresolved'); END;
