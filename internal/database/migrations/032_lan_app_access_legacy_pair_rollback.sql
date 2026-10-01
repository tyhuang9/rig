-- Migration 029 deliberately preserves a legacy migration-028 prepared grant
-- that already coexists with a migration-027 disable intent. If its allocation
-- was active, the original rollback trigger tried to mark it uncertain, but
-- the disable-intent freeze correctly rejected that second writer. Keep the
-- freeze strict. For only the exact backfilled prepared pair, retain the active
-- allocation under recovery quarantine until the paired disable commit makes
-- the one permitted terminal disabled_at mutation.
DROP TRIGGER lan_app_access_grant_rollback_allocation;
CREATE TRIGGER lan_app_access_grant_rollback_allocation
AFTER UPDATE OF state ON lan_app_access_grant_claims
WHEN NEW.state = 'rolled_back'
BEGIN
    UPDATE lan_port_allocations SET state = 'uncertain'
    WHERE id = NEW.allocation_id AND app_id = NEW.app_id
      AND owner_operation_id = NEW.allocation_owner_operation_id
      AND owner_revision_id = NEW.access_revision_id
      AND released_at IS NULL AND disabled_at IS NULL AND state = 'active'
      AND NOT (
          OLD.state = 'prepared' AND OLD.retired_at IS NULL
          AND EXISTS (
              SELECT 1
              FROM lan_app_access_disable_intents d
              JOIN lan_app_access_disable_claims c ON c.operation_id = d.operation_id
              JOIN lan_port_allocations a ON a.id = d.allocation_id
              WHERE d.app_id = NEW.app_id
                AND d.allocation_id = NEW.allocation_id
                AND d.allocation_owner_operation_id = NEW.allocation_owner_operation_id
                AND d.access_revision_id = NEW.access_revision_id
                AND d.access_revision_number = NEW.access_revision_number
                AND d.allocated_port = NEW.allocated_port
                AND d.gateway_profile_revision_id = NEW.gateway_profile_revision_id
                AND d.gateway_profile_revision_number = NEW.gateway_profile_revision_number
                AND a.app_id = d.app_id
                AND a.owner_operation_id = d.allocation_owner_operation_id
                AND a.owner_revision_id = d.access_revision_id
                AND a.port = d.allocated_port
                AND a.gateway_profile_revision_id = d.gateway_profile_revision_id
                AND a.gateway_profile_revision_number = d.gateway_profile_revision_number
                AND a.state = 'active' AND a.released_at IS NULL
                AND a.disabled_at IS NULL
                AND c.request_digest = d.request_digest
                AND c.approval_action = d.approval_action
                AND c.app_id = d.app_id
                AND c.allocation_id = d.allocation_id
                AND c.allocation_owner_operation_id = d.allocation_owner_operation_id
                AND c.access_revision_id = d.access_revision_id
                AND c.access_revision_number = d.access_revision_number
                AND c.allocated_port = d.allocated_port
                AND c.gateway_profile_revision_id = d.gateway_profile_revision_id
                AND c.gateway_profile_revision_number = d.gateway_profile_revision_number
                AND c.spec_digest = d.spec_digest
                AND c.approved_by = d.approved_by
                AND c.approved_at = d.approved_at
                AND c.source_grant_attempt_id IS NULL
                AND c.state = 'prepared' AND c.state_sequence = 1
                AND c.created_at = d.approved_at AND c.updated_at = c.created_at
                AND c.gateway_operation_id IS NULL
                AND c.protected_state_digest IS NULL
                AND c.resolved_at IS NULL
          )
      );
END;
