-- Install the receipt-bound rebind writer and transfer-aware retained formats.
-- The process-local rig_gateway_rebind_consume_v1 function is deliberately
-- unavailable to external SQLite clients, so ordinary SQL fails closed.

ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN spec_format_version INTEGER NOT NULL DEFAULT 1
        CHECK (spec_format_version IN (1,2));
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN roster_format_version INTEGER NOT NULL DEFAULT 1
        CHECK (roster_format_version IN (1,2));
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_protected_generation INTEGER NOT NULL DEFAULT 0
        CHECK (predecessor_protected_generation >= 0);
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_protected_journal_digest TEXT CHECK (
        predecessor_protected_journal_digest IS NULL OR
        (length(predecessor_protected_journal_digest)=64 AND predecessor_protected_journal_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_protected_intent_digest TEXT CHECK (
        predecessor_protected_intent_digest IS NULL OR
        (length(predecessor_protected_intent_digest)=64 AND predecessor_protected_intent_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_source_state_version INTEGER CHECK (predecessor_source_state_version > 0);
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_source_state_revision INTEGER CHECK (predecessor_source_state_revision >= 0);
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_source_state_digest TEXT CHECK (
        predecessor_source_state_digest IS NULL OR
        (length(predecessor_source_state_digest)=64 AND predecessor_source_state_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN predecessor_checkpoint_digest TEXT CHECK (
        predecessor_checkpoint_digest IS NULL OR
        (length(predecessor_checkpoint_digest)=64 AND predecessor_checkpoint_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_claims
    ADD COLUMN successor_protected_generation INTEGER NOT NULL DEFAULT 0
        CHECK (successor_protected_generation >= 0);

ALTER TABLE lan_gateway_rebind_roster_entries
    ADD COLUMN roster_format_version INTEGER NOT NULL DEFAULT 1
        CHECK (roster_format_version IN (1,2));
ALTER TABLE lan_gateway_rebind_roster_entries ADD COLUMN source_profile_revision_id TEXT;
ALTER TABLE lan_gateway_rebind_roster_entries
    ADD COLUMN source_profile_revision_number INTEGER CHECK (source_profile_revision_number > 0);
ALTER TABLE lan_gateway_rebind_roster_entries
    ADD COLUMN source_profile_spec_digest TEXT CHECK (
        source_profile_spec_digest IS NULL OR
        (length(source_profile_spec_digest)=64 AND source_profile_spec_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_roster_entries
    ADD COLUMN predecessor_transfer_digest TEXT CHECK (
        predecessor_transfer_digest IS NULL OR
        (length(predecessor_transfer_digest)=64 AND predecessor_transfer_digest NOT GLOB '*[^0-9a-f]*')
    );

ALTER TABLE lan_gateway_rebind_allocation_transfers
    ADD COLUMN transfer_format_version INTEGER NOT NULL DEFAULT 1 CHECK (transfer_format_version=1);
ALTER TABLE lan_gateway_rebind_allocation_transfers
    ADD COLUMN roster_entry_digest TEXT CHECK (
        roster_entry_digest IS NULL OR
        (length(roster_entry_digest)=64 AND roster_entry_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_allocation_transfers ADD COLUMN source_profile_revision_id TEXT;
ALTER TABLE lan_gateway_rebind_allocation_transfers
    ADD COLUMN source_profile_revision_number INTEGER CHECK (source_profile_revision_number > 0);
ALTER TABLE lan_gateway_rebind_allocation_transfers
    ADD COLUMN source_profile_spec_digest TEXT CHECK (
        source_profile_spec_digest IS NULL OR
        (length(source_profile_spec_digest)=64 AND source_profile_spec_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_allocation_transfers
    ADD COLUMN predecessor_transfer_digest TEXT CHECK (
        predecessor_transfer_digest IS NULL OR
        (length(predecessor_transfer_digest)=64 AND predecessor_transfer_digest NOT GLOB '*[^0-9a-f]*')
    );

ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN proof_version INTEGER CHECK (proof_version=1);
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN terminal_disposition TEXT CHECK (terminal_disposition IN ('none','commit','abort'));
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN protected_generation INTEGER CHECK (protected_generation > 0);
ALTER TABLE lan_gateway_rebind_transition_commands ADD COLUMN protected_phase TEXT;
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN protected_record_sequence INTEGER CHECK (protected_record_sequence > 0);
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN predecessor_checkpoint_digest TEXT CHECK (
        predecessor_checkpoint_digest IS NULL OR
        (length(predecessor_checkpoint_digest)=64 AND predecessor_checkpoint_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN source_state_version INTEGER CHECK (source_state_version > 0);
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN source_state_revision INTEGER CHECK (source_state_revision >= 0);
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN source_state_digest TEXT CHECK (
        source_state_digest IS NULL OR
        (length(source_state_digest)=64 AND source_state_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN successor_operational_state_version INTEGER CHECK (successor_operational_state_version >= 0);
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN successor_operational_state_revision INTEGER CHECK (successor_operational_state_revision >= 0);
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN successor_operational_state_digest TEXT CHECK (
        successor_operational_state_digest IS NULL OR
        (length(successor_operational_state_digest)=64 AND successor_operational_state_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_transition_commands
    ADD COLUMN transfer_manifest_digest TEXT CHECK (
        transfer_manifest_digest IS NULL OR
        (length(transfer_manifest_digest)=64 AND transfer_manifest_digest NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE lan_gateway_rebind_transition_commands ADD COLUMN authorization_nonce TEXT;
ALTER TABLE lan_gateway_rebind_transition_commands ADD COLUMN canonical_payload TEXT;

DROP TRIGGER lan_gateway_rebind_claim_immutable_update;
DROP TRIGGER lan_gateway_rebind_event_direct_insert;
DROP TRIGGER lan_gateway_rebind_roster_exact_insert;
DROP TRIGGER lan_gateway_rebind_transition_command_insert_blocked;
DROP TRIGGER lan_gateway_rebind_allocation_transfer_insert_blocked;
DROP TRIGGER lan_gateway_profile_upgrade_claim_pin_insert;
DROP TRIGGER lan_gateway_profile_upgrade_claim_pin_head;
DROP TRIGGER lan_gateway_profile_head_grant_fence;
DROP TRIGGER lan_app_access_disable_claim_exact_owner_transition;

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

-- Migration 029 compared the immutable raw grant profile directly with the
-- current gateway head. A committed rebind deliberately advances that head
-- while the allocation and grant keep their raw profile identity. Preserve
-- the complete owner check and accept the current head only through a guarded,
-- receipt-bound transfer for the exact source grant.
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
      AND (
        (gr.id=NEW.gateway_profile_revision_id
         AND gr.revision_number=NEW.gateway_profile_revision_number)
        OR EXISTS (
          SELECT 1
          FROM lan_gateway_rebind_allocation_transfers t
          JOIN lan_gateway_rebind_claims c ON c.operation_id=t.operation_id
          JOIN lan_gateway_rebind_claim_events e ON e.operation_id=c.operation_id
            AND e.state='database_committed'
          JOIN lan_gateway_rebind_transition_commands cmd ON cmd.operation_id=e.operation_id
            AND cmd.sequence=e.sequence AND cmd.next_state='database_committed'
          WHERE NEW.source_grant_attempt_id IS NOT NULL
            AND t.app_id=NEW.app_id AND t.allocation_id=NEW.allocation_id
            AND t.grant_attempt_id=NEW.source_grant_attempt_id
            AND t.source_profile_revision_id=NEW.gateway_profile_revision_id
            AND t.source_profile_revision_number=NEW.gateway_profile_revision_number
            AND t.successor_profile_revision_id=gr.id
            AND t.successor_profile_revision_number=gr.revision_number
            AND t.successor_profile_spec_digest=gr.spec_digest
            AND t.terminal_receipt_digest=cmd.terminal_receipt_digest
            AND c.state IN ('database_committed','unresolved','committed')
        )
      )
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

CREATE TRIGGER lan_gateway_rebind_claim_v2_source_insert
BEFORE INSERT ON lan_gateway_rebind_claims
WHEN NOT (
    (NEW.spec_format_version=1 AND NEW.roster_format_version=1
     AND NEW.predecessor_source_kind='gateway_upgrade'
     AND NEW.predecessor_rebind_operation_id IS NULL
     AND NEW.predecessor_terminal_receipt_digest IS NULL
     AND NEW.predecessor_protected_generation=0
     AND NEW.predecessor_protected_journal_digest IS NULL
     AND NEW.predecessor_protected_intent_digest IS NULL
     AND NEW.predecessor_source_state_version IS NULL
     AND NEW.predecessor_source_state_revision IS NULL
     AND NEW.predecessor_source_state_digest IS NULL
     AND NEW.predecessor_checkpoint_digest IS NULL
     AND NEW.successor_protected_generation=0)
    OR
    (NEW.spec_format_version=2 AND NEW.roster_format_version=2
     AND NEW.predecessor_source_state_version IS NOT NULL
     AND NEW.predecessor_source_state_revision IS NOT NULL
     AND NEW.predecessor_source_state_digest IS NOT NULL
     AND NEW.predecessor_checkpoint_digest IS NOT NULL
     AND NEW.successor_protected_generation>NEW.predecessor_protected_generation
     AND NEW.successor_protected_generation>COALESCE((
       SELECT MAX(c.successor_protected_generation)
       FROM lan_gateway_rebind_claims c WHERE c.spec_format_version=2
     ),-1)
     AND (
      (NEW.predecessor_source_kind='gateway_upgrade'
       AND NEW.predecessor_protected_journal_digest IS NOT NULL
       AND NEW.predecessor_protected_intent_digest IS NULL
       AND NEW.predecessor_terminal_receipt_digest IS NULL
       AND NEW.predecessor_source_state_version=2
       AND NEW.predecessor_source_state_revision=0)
      OR
      (NEW.predecessor_source_kind='gateway_rebind'
       AND NEW.predecessor_protected_generation>0
       AND NEW.predecessor_protected_journal_digest IS NULL
       AND NEW.predecessor_protected_intent_digest IS NOT NULL
       AND NEW.predecessor_terminal_receipt_digest IS NOT NULL
       AND NEW.predecessor_source_state_version=1
       AND NEW.predecessor_source_state_revision>0)
     )
    )
  )
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind v2 source is invalid'); END;

CREATE TRIGGER lan_gateway_rebind_claim_immutable_update
BEFORE UPDATE OF operation_id,singleton,request_digest,approval_action,spec_digest,
    approved_by,approved_at,predecessor_profile_revision_id,
    predecessor_profile_revision_number,predecessor_profile_spec_digest,
    predecessor_upgrade_operation_id,predecessor_protected_identity_digest,
    predecessor_source_kind,predecessor_rebind_operation_id,
    predecessor_terminal_receipt_digest,successor_profile_revision_id,
    successor_profile_revision_number,successor_profile_operation_id,
    successor_profile_request_digest,successor_selected_ipv4,
    successor_interface_id,successor_port_start,successor_port_end,
    successor_profile_spec_digest,configure_approval_action,
    configure_approved_by,configure_approved_at,roster_digest,roster_count,
    created_at,spec_format_version,roster_format_version,
    predecessor_protected_generation,predecessor_protected_journal_digest,
    predecessor_protected_intent_digest,predecessor_source_state_version,
    predecessor_source_state_revision,predecessor_source_state_digest,
    predecessor_checkpoint_digest,successor_protected_generation
ON lan_gateway_rebind_claims
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind claim identity is immutable'); END;

CREATE TRIGGER lan_gateway_rebind_claim_state_guard
BEFORE UPDATE OF state,state_sequence,updated_at ON lan_gateway_rebind_claims
WHEN NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_transition_commands cmd
    WHERE cmd.operation_id=OLD.operation_id
      AND cmd.sequence=OLD.state_sequence+1
      AND cmd.previous_state=OLD.state
      AND cmd.previous_sequence=OLD.state_sequence
      AND cmd.next_state=NEW.state
      AND NEW.state_sequence=cmd.sequence
      AND NEW.updated_at=cmd.created_at
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind transition is not freshly authorized'); END;

CREATE TRIGGER lan_gateway_rebind_event_direct_insert
BEFORE INSERT ON lan_gateway_rebind_claim_events
WHEN NOT (
    NEW.sequence=1 AND NEW.state='prepared'
) AND NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id
     AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state
     AND cmd.previous_sequence=c.state_sequence
    WHERE c.operation_id=NEW.operation_id
      AND cmd.sequence=NEW.sequence
      AND cmd.next_state=NEW.state
      AND cmd.created_at=NEW.created_at
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind event requires a fresh command'); END;

CREATE TRIGGER lan_gateway_rebind_roster_exact_insert
BEFORE INSERT ON lan_gateway_rebind_roster_entries
WHEN NOT EXISTS (
    SELECT 1
    FROM lan_gateway_rebind_claims c
    JOIN lan_port_allocations a ON a.id=NEW.allocation_id AND a.app_id=NEW.app_id
    JOIN applications app ON app.id=a.app_id
    JOIN lan_app_access_heads ah ON ah.app_id=a.app_id
    JOIN lan_app_access_revisions ar
      ON ar.app_id=ah.app_id AND ar.id=ah.revision_id AND ar.revision_number=ah.revision_number
    JOIN lan_app_access_grant_claims g
      ON g.attempt_id=NEW.grant_attempt_id AND g.allocation_id=a.id
    JOIN generated_runtime_active_heads rh ON rh.app_id=a.app_id
    WHERE c.operation_id=NEW.operation_id AND c.state='prepared'
      AND a.port=NEW.allocated_port AND app.archived_at IS NULL
      AND a.owner_operation_id=NEW.allocation_owner_operation_id
      AND a.owner_revision_id=NEW.access_revision_id
      AND a.state=NEW.allocation_state AND a.state='active'
      AND a.released_at IS NULL AND a.disabled_at IS NULL
      AND ar.id=NEW.access_revision_id
      AND ar.revision_number=NEW.access_revision_number
      AND ar.operation_id=NEW.allocation_owner_operation_id
      AND ar.allocation_id=a.id AND ar.allocated_port=a.port
      AND ar.spec_digest=NEW.access_spec_digest
      AND g.access_revision_id=ar.id
      AND g.access_revision_number=ar.revision_number
      AND g.allocation_owner_operation_id=a.owner_operation_id
      AND g.state='committed' AND g.state_sequence=NEW.grant_state_sequence
      AND g.retired_at IS NULL
      AND g.protected_state_digest=NEW.grant_protected_state_digest
      AND rh.deployment_id=NEW.serving_deployment_id
      AND rh.release_id=NEW.serving_release_id
      AND rh.slot=NEW.serving_slot
      AND rh.generation=NEW.route_generation AND rh.generation>0
      AND NOT EXISTS (SELECT 1 FROM lan_app_access_disable_intents d WHERE d.allocation_id=a.id)
      AND (
        (c.roster_format_version=1 AND NEW.roster_format_version=1
         AND a.gateway_profile_revision_id=c.predecessor_profile_revision_id
         AND a.gateway_profile_revision_number=c.predecessor_profile_revision_number
         AND ar.gateway_profile_revision_id=c.predecessor_profile_revision_id
         AND ar.gateway_profile_revision_number=c.predecessor_profile_revision_number)
        OR
        (c.roster_format_version=2 AND NEW.roster_format_version=2
         AND NEW.source_profile_revision_id IS NOT NULL
         AND NEW.source_profile_revision_number IS NOT NULL
         AND NEW.source_profile_spec_digest IS NOT NULL
         AND a.gateway_profile_revision_id=NEW.source_profile_revision_id
         AND a.gateway_profile_revision_number=NEW.source_profile_revision_number
         AND ar.gateway_profile_revision_id=NEW.source_profile_revision_id
         AND ar.gateway_profile_revision_number=NEW.source_profile_revision_number
         AND g.gateway_profile_revision_id=NEW.source_profile_revision_id
         AND g.gateway_profile_revision_number=NEW.source_profile_revision_number
         AND g.gateway_profile_spec_digest=NEW.source_profile_spec_digest
         AND (
           (NEW.predecessor_transfer_digest IS NULL
            AND NEW.source_profile_revision_id=c.predecessor_profile_revision_id
            AND NEW.source_profile_revision_number=c.predecessor_profile_revision_number
            AND NEW.source_profile_spec_digest=c.predecessor_profile_spec_digest)
           OR EXISTS (
             SELECT 1
             FROM lan_gateway_rebind_allocation_transfers t
             JOIN lan_gateway_rebind_claims prior ON prior.operation_id=t.operation_id
             JOIN lan_gateway_rebind_claim_events pe
               ON pe.operation_id=prior.operation_id
              AND pe.sequence=prior.state_sequence AND pe.state='committed'
             JOIN lan_gateway_rebind_transition_commands pcmd
               ON pcmd.operation_id=prior.operation_id
              AND pcmd.sequence=prior.state_sequence
              AND pcmd.next_state='committed'
             WHERE t.transfer_digest=NEW.predecessor_transfer_digest
               AND t.app_id=NEW.app_id AND t.allocation_id=NEW.allocation_id
               AND t.grant_attempt_id=NEW.grant_attempt_id
               AND t.source_profile_revision_id=NEW.source_profile_revision_id
               AND t.source_profile_revision_number=NEW.source_profile_revision_number
               AND t.source_profile_spec_digest=NEW.source_profile_spec_digest
               AND t.successor_profile_revision_id=c.predecessor_profile_revision_id
               AND t.successor_profile_revision_number=c.predecessor_profile_revision_number
               AND t.successor_profile_spec_digest=c.predecessor_profile_spec_digest
               AND t.terminal_receipt_digest=c.predecessor_terminal_receipt_digest
               AND pcmd.terminal_receipt_digest=c.predecessor_terminal_receipt_digest
           )
         ))
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind roster entry is not exact'); END;

CREATE TRIGGER lan_gateway_rebind_transition_guard
BEFORE INSERT ON lan_gateway_rebind_transition_commands
BEGIN
    SELECT CASE WHEN rig_gateway_rebind_consume_v1(
        NEW.authorization_nonce,NEW.operation_id,NEW.sequence,NEW.previous_state,
        NEW.previous_sequence,NEW.next_state,NEW.purpose,
        NEW.protected_record_digest,COALESCE(NEW.terminal_receipt_digest,''),
        COALESCE(NEW.local_attestation_digest,''),NEW.terminal_disposition,
        NEW.command_digest,NEW.canonical_payload
    ) <> 1 THEN RAISE(ABORT, 'LAN gateway rebind transition capability is invalid or spent') END;

    SELECT CASE WHEN NEW.proof_version<>1 OR NEW.purpose<>'lan_gateway_rebind_transition'
      OR NEW.authorization_nonce IS NULL OR length(NEW.authorization_nonce)<>64
      OR NEW.canonical_payload IS NULL OR json_valid(NEW.canonical_payload)<>1
      OR NEW.terminal_disposition IS NULL
      OR NEW.protected_generation IS NULL OR NEW.protected_phase IS NULL
      OR NEW.protected_record_sequence IS NULL
      OR NEW.predecessor_checkpoint_digest IS NULL
      OR NEW.source_state_version IS NULL OR NEW.source_state_revision IS NULL
      OR NEW.source_state_digest IS NULL
      OR json_extract(NEW.canonical_payload,'$.version') IS NOT NEW.proof_version
      OR json_extract(NEW.canonical_payload,'$.purpose') IS NOT NEW.purpose
      OR json_extract(NEW.canonical_payload,'$.operationId') IS NOT NEW.operation_id
      OR json_extract(NEW.canonical_payload,'$.expectedState') IS NOT NEW.previous_state
      OR json_extract(NEW.canonical_payload,'$.expectedSequence') IS NOT NEW.previous_sequence
      OR json_extract(NEW.canonical_payload,'$.nextState') IS NOT NEW.next_state
      OR json_extract(NEW.canonical_payload,'$.protectedRecordDigest') IS NOT NEW.protected_record_digest
      OR json_extract(NEW.canonical_payload,'$.protectedGeneration') IS NOT NEW.protected_generation
      OR json_extract(NEW.canonical_payload,'$.protectedPhase') IS NOT NEW.protected_phase
      OR json_extract(NEW.canonical_payload,'$.protectedRecordSequence') IS NOT NEW.protected_record_sequence
      OR COALESCE(json_extract(NEW.canonical_payload,'$.terminalReceiptDigest'),'')<>COALESCE(NEW.terminal_receipt_digest,'')
      OR json_extract(NEW.canonical_payload,'$.terminalDisposition') IS NOT NEW.terminal_disposition
      OR json_extract(NEW.canonical_payload,'$.predecessorCheckpointDigest') IS NOT NEW.predecessor_checkpoint_digest
      OR json_extract(NEW.canonical_payload,'$.sourceStateVersion') IS NOT NEW.source_state_version
      OR json_extract(NEW.canonical_payload,'$.sourceStateRevision') IS NOT NEW.source_state_revision
      OR json_extract(NEW.canonical_payload,'$.sourceStateDigest') IS NOT NEW.source_state_digest
      OR json_extract(NEW.canonical_payload,'$.successorOperationalStateVersion') IS NOT NEW.successor_operational_state_version
      OR json_extract(NEW.canonical_payload,'$.successorOperationalStateRevision') IS NOT NEW.successor_operational_state_revision
      OR COALESCE(json_extract(NEW.canonical_payload,'$.successorOperationalStateDigest'),'')<>COALESCE(NEW.successor_operational_state_digest,'')
      OR COALESCE(json_extract(NEW.canonical_payload,'$.transferManifestDigest'),'')<>COALESCE(NEW.transfer_manifest_digest,'')
      OR COALESCE(json_extract(NEW.canonical_payload,'$.localAttestationDigest'),'')<>COALESCE(NEW.local_attestation_digest,'')
      OR NOT EXISTS (
          SELECT 1 FROM lan_gateway_rebind_claims c
          JOIN lan_gateway_profile_heads h ON h.singleton=1
          JOIN lan_gateway_profile_revisions p
            ON p.id=h.revision_id AND p.revision_number=h.revision_number
          WHERE c.operation_id=NEW.operation_id
            AND c.request_digest=json_extract(NEW.canonical_payload,'$.claimRequestDigest')
            AND c.spec_digest=json_extract(NEW.canonical_payload,'$.claimSpecDigest')
            AND c.state=NEW.previous_state AND c.state_sequence=NEW.previous_sequence
            AND h.revision_id=json_extract(NEW.canonical_payload,'$.expectedHeadRevisionId')
            AND h.revision_number=json_extract(NEW.canonical_payload,'$.expectedHeadRevisionNumber')
            AND p.spec_digest=json_extract(NEW.canonical_payload,'$.expectedHeadSpecDigest')
            AND (c.spec_format_version=1 OR (
              c.predecessor_checkpoint_digest=NEW.predecessor_checkpoint_digest
              AND c.predecessor_source_state_version=NEW.source_state_version
              AND c.predecessor_source_state_revision=NEW.source_state_revision
              AND c.predecessor_source_state_digest=NEW.source_state_digest
              AND c.successor_protected_generation=NEW.protected_generation
            ))
      )
      OR NOT (
          (NEW.next_state IN ('successor_ready','database_committed','committed')
           AND NEW.terminal_disposition='commit' AND NEW.terminal_receipt_digest IS NOT NULL)
          OR (NEW.next_state='rolled_back' AND NEW.terminal_disposition='abort'
              AND NEW.terminal_receipt_digest IS NOT NULL)
          OR (NEW.next_state='unresolved' AND (
              (NEW.terminal_disposition='none' AND NEW.terminal_receipt_digest IS NULL)
              OR (NEW.terminal_disposition IN ('commit','abort') AND NEW.terminal_receipt_digest IS NOT NULL)
          ))
      )
      OR (NEW.next_state='committed' AND NEW.local_attestation_digest IS NULL)
      OR (NEW.next_state<>'committed' AND NEW.local_attestation_digest IS NOT NULL)
      OR (NEW.next_state='database_committed' AND (
          NEW.successor_operational_state_version<>1
          OR NEW.successor_operational_state_revision<>1
          OR NEW.successor_operational_state_digest IS NULL
          OR NEW.transfer_manifest_digest IS NULL
          OR json_array_length(NEW.canonical_payload,'$.transfers')<>(
              SELECT roster_count FROM lan_gateway_rebind_claims WHERE operation_id=NEW.operation_id
          )))
      OR (NEW.next_state<>'database_committed' AND json_array_length(NEW.canonical_payload,'$.transfers')<>0)
    THEN RAISE(ABORT, 'LAN gateway rebind transition proof is not exact') END;

    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM lan_gateway_rebind_transition_commands retained
        WHERE retained.operation_id=NEW.operation_id
          AND retained.terminal_disposition<>'none'
          AND (retained.terminal_disposition IS NOT NEW.terminal_disposition
               OR retained.terminal_receipt_digest IS NOT NEW.terminal_receipt_digest)
    ) THEN RAISE(ABORT, 'LAN gateway rebind terminal decision is immutable') END;

    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM lan_gateway_rebind_claim_events e
        WHERE e.operation_id=NEW.operation_id AND e.state='database_committed'
    ) AND NEW.next_state IN ('successor_ready','rolled_back')
    THEN RAISE(ABORT, 'LAN gateway rebind is roll-forward-only after database commit') END;

    SELECT CASE WHEN NEW.next_state IN ('database_committed','committed') AND NOT (
        (
          NEW.next_state='database_committed'
          AND NOT EXISTS (SELECT 1 FROM lan_gateway_rebind_claim_events e WHERE e.operation_id=NEW.operation_id AND e.state='database_committed')
          AND EXISTS (
            SELECT 1 FROM lan_gateway_rebind_claims c
            JOIN lan_gateway_profile_heads h ON h.singleton=1
            WHERE c.operation_id=NEW.operation_id
              AND h.revision_id=c.predecessor_profile_revision_id
              AND h.revision_number=c.predecessor_profile_revision_number
              AND NOT EXISTS (SELECT 1 FROM lan_gateway_profile_revisions p WHERE p.id=c.successor_profile_revision_id)
              AND NOT EXISTS (SELECT 1 FROM lan_gateway_rebind_allocation_transfers t WHERE t.operation_id=c.operation_id)
          )
        ) OR (
          EXISTS (SELECT 1 FROM lan_gateway_rebind_claim_events e WHERE e.operation_id=NEW.operation_id AND e.state='database_committed')
          AND EXISTS (
            SELECT 1 FROM lan_gateway_rebind_claims c
            JOIN lan_gateway_profile_heads h ON h.singleton=1
            JOIN lan_gateway_profile_revisions p
              ON p.id=h.revision_id AND p.revision_number=h.revision_number
            WHERE c.operation_id=NEW.operation_id
              AND p.id=c.successor_profile_revision_id
              AND p.revision_number=c.successor_profile_revision_number
              AND p.spec_digest=c.successor_profile_spec_digest
              AND (SELECT COUNT(*) FROM lan_gateway_rebind_allocation_transfers t WHERE t.operation_id=c.operation_id)=c.roster_count
              AND NOT EXISTS (
                SELECT 1 FROM lan_gateway_rebind_roster_entries r
                WHERE r.operation_id=c.operation_id AND NOT EXISTS (
                  SELECT 1 FROM lan_gateway_rebind_allocation_transfers t
                  WHERE t.operation_id=r.operation_id AND t.allocation_id=r.allocation_id
                    AND t.roster_entry_digest=r.entry_digest
                    AND t.terminal_receipt_digest=NEW.terminal_receipt_digest
                )
              )
          )
        )
    ) THEN RAISE(ABORT, 'LAN gateway rebind database projection is incomplete') END;
END;

CREATE TRIGGER lan_gateway_profile_upgrade_claim_pin_insert
BEFORE INSERT ON lan_gateway_profile_revisions
WHEN EXISTS (SELECT 1 FROM lan_gateway_upgrade_claims WHERE state IN ('prepared','serving','unresolved','committed'))
 AND NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state AND cmd.next_state='database_committed'
    WHERE NEW.id=c.successor_profile_revision_id
      AND NEW.revision_number=c.successor_profile_revision_number
      AND NEW.operation_id=c.successor_profile_operation_id
      AND NEW.request_digest=c.successor_profile_request_digest
      AND NEW.selected_ipv4=c.successor_selected_ipv4
      AND NEW.interface_id=c.successor_interface_id
      AND NEW.port_start=c.successor_port_start AND NEW.port_end=c.successor_port_end
      AND NEW.spec_digest=c.successor_profile_spec_digest
      AND NEW.approved_by=c.configure_approved_by AND NEW.approved_at=c.configure_approved_at
 )
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile is pinned by an upgrade claim'); END;

CREATE TRIGGER lan_gateway_profile_upgrade_claim_pin_head
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_upgrade_claims WHERE state IN ('prepared','serving','unresolved','committed'))
 AND NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state AND cmd.next_state='database_committed'
    WHERE OLD.revision_id=c.predecessor_profile_revision_id
      AND OLD.revision_number=c.predecessor_profile_revision_number
      AND NEW.revision_id=c.successor_profile_revision_id
      AND NEW.revision_number=c.successor_profile_revision_number
 )
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by an upgrade claim'); END;

CREATE TRIGGER lan_gateway_profile_head_grant_fence
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (
    SELECT 1 FROM lan_app_access_grant_claims c
    WHERE c.gateway_profile_revision_id=OLD.revision_id
      AND c.gateway_profile_revision_number=OLD.revision_number
      AND c.retired_at IS NULL
      AND c.state IN ('applying','db_active','uncertain','committed')
)
AND NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state AND cmd.next_state='database_committed'
    WHERE OLD.revision_id=c.predecessor_profile_revision_id
      AND OLD.revision_number=c.predecessor_profile_revision_number
      AND NEW.revision_id=c.successor_profile_revision_id
      AND NEW.revision_number=c.successor_profile_revision_number
      AND json_array_length(cmd.canonical_payload,'$.transfers')=c.roster_count
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway profile head is pinned by grant claim'); END;

CREATE TRIGGER lan_gateway_rebind_fence_profile_insert
BEFORE INSERT ON lan_gateway_profile_revisions
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
 AND NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state AND cmd.next_state='database_committed'
    WHERE NEW.id=c.successor_profile_revision_id
 )
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;

CREATE TRIGGER lan_gateway_rebind_fence_profile_head_update
BEFORE UPDATE ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
 AND NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state AND cmd.next_state='database_committed'
    WHERE OLD.revision_id=c.predecessor_profile_revision_id
      AND NEW.revision_id=c.successor_profile_revision_id
 )
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;

CREATE TRIGGER lan_gateway_rebind_allocation_transfer_insert
BEFORE INSERT ON lan_gateway_rebind_allocation_transfers
WHEN NOT EXISTS (
    SELECT 1 FROM lan_gateway_rebind_claims c
    JOIN lan_gateway_rebind_transition_commands cmd
      ON cmd.operation_id=c.operation_id AND cmd.sequence=c.state_sequence+1
     AND cmd.previous_state=c.state AND cmd.previous_sequence=c.state_sequence
     AND cmd.next_state='database_committed'
    JOIN lan_gateway_rebind_roster_entries r
      ON r.operation_id=c.operation_id AND r.allocation_id=NEW.allocation_id
    WHERE c.operation_id=NEW.operation_id
      AND NEW.transfer_format_version=1
      AND NEW.ordinal=r.ordinal AND NEW.app_id=r.app_id
      AND NEW.grant_attempt_id=r.grant_attempt_id
      AND NEW.roster_entry_digest=r.entry_digest
      AND (
        (r.roster_format_version=2
         AND NEW.source_profile_revision_id=r.source_profile_revision_id
         AND NEW.source_profile_revision_number=r.source_profile_revision_number
         AND NEW.source_profile_spec_digest=r.source_profile_spec_digest
         AND NEW.predecessor_transfer_digest IS r.predecessor_transfer_digest)
        OR
        (r.roster_format_version=1
         AND NEW.source_profile_revision_id=c.predecessor_profile_revision_id
         AND NEW.source_profile_revision_number=c.predecessor_profile_revision_number
         AND NEW.source_profile_spec_digest=c.predecessor_profile_spec_digest
         AND NEW.predecessor_transfer_digest IS NULL)
      )
      AND NEW.successor_profile_revision_id=c.successor_profile_revision_id
      AND NEW.successor_profile_revision_number=c.successor_profile_revision_number
      AND NEW.successor_profile_spec_digest=c.successor_profile_spec_digest
      AND NEW.terminal_receipt_digest=cmd.terminal_receipt_digest
      AND EXISTS (
        SELECT 1 FROM json_each(cmd.canonical_payload,'$.transfers') j
        WHERE json_extract(j.value,'$.transferDigest')=NEW.transfer_digest
          AND json_extract(j.value,'$.ordinal')=NEW.ordinal
          AND json_extract(j.value,'$.appId')=NEW.app_id
          AND json_extract(j.value,'$.allocationId')=NEW.allocation_id
          AND json_extract(j.value,'$.grantAttemptId')=NEW.grant_attempt_id
          AND json_extract(j.value,'$.sourceBindingDigest')=NEW.source_binding_digest
          AND json_extract(j.value,'$.rosterEntryDigest')=NEW.roster_entry_digest
      )
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind transfer is not freshly authorized'); END;

CREATE TRIGGER lan_gateway_rebind_transition_apply
AFTER INSERT ON lan_gateway_rebind_transition_commands
BEGIN
    INSERT INTO lan_gateway_profile_revisions(
        id,revision_number,operation_id,request_digest,approval_action,
        selected_ipv4,interface_id,port_start,port_end,spec_digest,approved_by,approved_at
    )
    SELECT c.successor_profile_revision_id,c.successor_profile_revision_number,
        c.successor_profile_operation_id,c.successor_profile_request_digest,
        c.configure_approval_action,c.successor_selected_ipv4,c.successor_interface_id,
        c.successor_port_start,c.successor_port_end,c.successor_profile_spec_digest,
        c.configure_approved_by,c.configure_approved_at
    FROM lan_gateway_rebind_claims c
    WHERE c.operation_id=NEW.operation_id AND NEW.next_state='database_committed'
      AND NOT EXISTS (
        SELECT 1 FROM lan_gateway_rebind_claim_events e
        WHERE e.operation_id=NEW.operation_id AND e.state='database_committed'
      );

    UPDATE lan_gateway_profile_heads
    SET revision_id=(SELECT successor_profile_revision_id FROM lan_gateway_rebind_claims WHERE operation_id=NEW.operation_id),
        revision_number=(SELECT successor_profile_revision_number FROM lan_gateway_rebind_claims WHERE operation_id=NEW.operation_id),
        updated_at=NEW.created_at
    WHERE singleton=1 AND NEW.next_state='database_committed'
      AND NOT EXISTS (
        SELECT 1 FROM lan_gateway_rebind_claim_events e
        WHERE e.operation_id=NEW.operation_id AND e.state='database_committed'
      );

    INSERT INTO lan_gateway_rebind_allocation_transfers(
        operation_id,ordinal,app_id,allocation_id,grant_attempt_id,
        source_binding_digest,successor_profile_revision_id,
        successor_profile_revision_number,successor_profile_spec_digest,
        terminal_receipt_digest,transfer_digest,created_at,
        transfer_format_version,roster_entry_digest,source_profile_revision_id,
        source_profile_revision_number,source_profile_spec_digest,
        predecessor_transfer_digest
    )
    SELECT NEW.operation_id,
        json_extract(j.value,'$.ordinal'),json_extract(j.value,'$.appId'),
        json_extract(j.value,'$.allocationId'),json_extract(j.value,'$.grantAttemptId'),
        json_extract(j.value,'$.sourceBindingDigest'),
        json_extract(j.value,'$.successorProfileRevisionId'),
        json_extract(j.value,'$.successorProfileRevisionNumber'),
        json_extract(j.value,'$.successorProfileSpecDigest'),
        json_extract(j.value,'$.terminalReceiptDigest'),
        json_extract(j.value,'$.transferDigest'),NEW.created_at,
        json_extract(j.value,'$.version'),json_extract(j.value,'$.rosterEntryDigest'),
        json_extract(j.value,'$.sourceProfileRevisionId'),
        json_extract(j.value,'$.sourceProfileRevisionNumber'),
        json_extract(j.value,'$.sourceProfileSpecDigest'),
        json_extract(j.value,'$.predecessorTransferDigest')
    FROM json_each(NEW.canonical_payload,'$.transfers') j
    WHERE NEW.next_state='database_committed'
      AND NOT EXISTS (
        SELECT 1 FROM lan_gateway_rebind_claim_events e
        WHERE e.operation_id=NEW.operation_id AND e.state='database_committed'
      );

    INSERT INTO lan_gateway_rebind_claim_events(operation_id,sequence,state,created_at)
    VALUES(NEW.operation_id,NEW.sequence,NEW.next_state,NEW.created_at);

    UPDATE lan_gateway_rebind_claims
    SET state=NEW.next_state,state_sequence=NEW.sequence,updated_at=NEW.created_at
    WHERE operation_id=NEW.operation_id;
END;

-- All ordinary mutation fences remain active only while a claim is active.
CREATE TRIGGER lan_gateway_rebind_fence_allocation_insert BEFORE INSERT ON lan_port_allocations
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_allocation_update BEFORE UPDATE ON lan_port_allocations
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_revision_insert BEFORE INSERT ON lan_app_access_revisions
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_head_update BEFORE UPDATE ON lan_app_access_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_head_insert BEFORE INSERT ON lan_app_access_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_access_head_delete BEFORE DELETE ON lan_app_access_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_grant_insert BEFORE INSERT ON lan_app_access_grant_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_grant_update BEFORE UPDATE ON lan_app_access_grant_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_disable_intent_insert BEFORE INSERT ON lan_app_access_disable_intents
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_disable_claim_insert BEFORE INSERT ON lan_app_access_disable_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_disable_claim_update BEFORE UPDATE ON lan_app_access_disable_claims
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_profile_head_insert BEFORE INSERT ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_profile_head_delete BEFORE DELETE ON lan_gateway_profile_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_update BEFORE UPDATE ON generated_runtime_active_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_insert BEFORE INSERT ON generated_runtime_active_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_delete BEFORE DELETE ON generated_runtime_active_heads
WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims WHERE state IN ('prepared','successor_ready','database_committed','unresolved'))
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END;
