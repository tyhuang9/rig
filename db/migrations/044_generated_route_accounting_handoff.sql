CREATE TABLE generated_route_accounting_handoff_authorizations (
    activation_receipt_id TEXT PRIMARY KEY REFERENCES generated_route_activations(id) ON DELETE RESTRICT,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    successor_deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    predecessor_deployment_id TEXT NOT NULL,
    candidate_roster_digest TEXT NOT NULL CHECK (length(candidate_roster_digest)=64 AND candidate_roster_digest NOT GLOB '*[^0-9a-f]*'),
    predecessor_roster_digest TEXT NOT NULL CHECK (length(predecessor_roster_digest)=64 AND predecessor_roster_digest NOT GLOB '*[^0-9a-f]*'),
    successor_component_count INTEGER NOT NULL CHECK (successor_component_count > 0),
    predecessor_component_count INTEGER NOT NULL CHECK (predecessor_component_count >= 0),
    authorized_at TEXT NOT NULL
);

CREATE TABLE generated_route_accounting_handoff_receipts (
    activation_receipt_id TEXT PRIMARY KEY REFERENCES generated_route_accounting_handoff_authorizations(activation_receipt_id) ON DELETE RESTRICT,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    successor_deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    predecessor_deployment_id TEXT NOT NULL,
    candidate_roster_digest TEXT NOT NULL CHECK (length(candidate_roster_digest)=64 AND candidate_roster_digest NOT GLOB '*[^0-9a-f]*'),
    predecessor_roster_digest TEXT NOT NULL CHECK (length(predecessor_roster_digest)=64 AND predecessor_roster_digest NOT GLOB '*[^0-9a-f]*'),
    successor_component_count INTEGER NOT NULL CHECK (successor_component_count > 0),
    predecessor_component_count INTEGER NOT NULL CHECK (predecessor_component_count >= 0),
    handed_off_at TEXT NOT NULL
);

CREATE TRIGGER generated_route_accounting_handoff_authorization_valid_insert
BEFORE INSERT ON generated_route_accounting_handoff_authorizations
WHEN NOT EXISTS (
    SELECT 1
    FROM generated_route_activations a
    JOIN generated_route_predecessor_retirement_receipts r
      ON r.activation_receipt_id=a.id
     AND r.app_id=a.app_id
     AND r.successor_deployment_id=a.successor_deployment_id
     AND r.predecessor_deployment_id=a.predecessor_deployment_id
     AND r.predecessor_roster_digest=a.predecessor_roster_digest
    JOIN generated_runtime_deployments d
      ON d.deployment_id=a.successor_deployment_id
     AND d.app_id=a.app_id
     AND d.phase='draining'
    JOIN deployments base
      ON base.id=d.deployment_id
     AND base.app_id=d.app_id
     AND base.status='waiting_health'
    JOIN generated_runtime_active_heads h
      ON h.app_id=a.app_id
     AND h.deployment_id=a.successor_deployment_id
     AND h.generation=a.successor_generation
     AND h.activation_receipt_id=a.id
    WHERE a.id=NEW.activation_receipt_id
      AND a.app_id=NEW.app_id
      AND a.successor_deployment_id=NEW.successor_deployment_id
      AND a.predecessor_deployment_id=NEW.predecessor_deployment_id
      AND a.candidate_roster_digest=NEW.candidate_roster_digest
      AND a.predecessor_roster_digest=NEW.predecessor_roster_digest
      AND NEW.successor_component_count=(
          SELECT COUNT(*) FROM generated_runtime_components c
          WHERE c.deployment_id=a.successor_deployment_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM generated_runtime_components c
          WHERE c.deployment_id=a.successor_deployment_id
            AND (c.state<>'active' OR NOT EXISTS (
                SELECT 1 FROM generated_candidate_intents i
                WHERE i.deployment_id=c.deployment_id
                  AND i.component_name=c.component_name
            ))
      )
      AND NEW.predecessor_component_count=r.component_count
      AND NEW.predecessor_component_count=(
          SELECT COUNT(*) FROM generated_runtime_components c
          WHERE c.deployment_id=a.predecessor_deployment_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM generated_runtime_components c
          WHERE c.deployment_id=a.predecessor_deployment_id
            AND (c.state<>'stopped' OR NOT EXISTS (
                SELECT 1 FROM generated_route_predecessor_retirements p
                WHERE p.activation_receipt_id=a.id
                  AND p.predecessor_deployment_id=c.deployment_id
                  AND p.component_name=c.component_name
                  AND p.container_id=c.container_id
            ))
      )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route accounting handoff authorization'); END;

CREATE TRIGGER generated_route_accounting_handoff_authorization_immutable
BEFORE UPDATE ON generated_route_accounting_handoff_authorizations
BEGIN SELECT RAISE(ABORT, 'generated route accounting handoff authorizations are immutable'); END;

CREATE TRIGGER generated_route_accounting_handoff_authorization_retain
BEFORE DELETE ON generated_route_accounting_handoff_authorizations
BEGIN SELECT RAISE(ABORT, 'generated route accounting handoff authorizations are retained'); END;

CREATE TRIGGER generated_route_accounting_handoff_receipt_valid_insert
BEFORE INSERT ON generated_route_accounting_handoff_receipts
WHEN NOT EXISTS (
    SELECT 1
    FROM generated_route_accounting_handoff_authorizations a
    JOIN generated_runtime_deployments d
      ON d.deployment_id=a.successor_deployment_id
     AND d.app_id=a.app_id
     AND d.phase='draining'
    JOIN deployments base
      ON base.id=d.deployment_id
     AND base.app_id=d.app_id
     AND base.status='waiting_health'
    JOIN generated_runtime_active_heads h
      ON h.app_id=a.app_id
     AND h.deployment_id=a.successor_deployment_id
     AND h.activation_receipt_id=a.activation_receipt_id
    WHERE a.activation_receipt_id=NEW.activation_receipt_id
      AND a.app_id=NEW.app_id
      AND a.successor_deployment_id=NEW.successor_deployment_id
      AND a.predecessor_deployment_id=NEW.predecessor_deployment_id
      AND a.candidate_roster_digest=NEW.candidate_roster_digest
      AND a.predecessor_roster_digest=NEW.predecessor_roster_digest
      AND a.successor_component_count=NEW.successor_component_count
      AND a.predecessor_component_count=NEW.predecessor_component_count
      AND NEW.successor_component_count=(
          SELECT COUNT(*) FROM generated_runtime_components c
          WHERE c.deployment_id=a.successor_deployment_id AND c.state='active'
      )
      AND NEW.predecessor_component_count=(
          SELECT COUNT(*) FROM generated_runtime_components c
          WHERE c.deployment_id=a.predecessor_deployment_id AND c.state='stopped'
      )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route accounting handoff receipt'); END;

CREATE TRIGGER generated_route_accounting_handoff_receipt_immutable
BEFORE UPDATE ON generated_route_accounting_handoff_receipts
BEGIN SELECT RAISE(ABORT, 'generated route accounting handoff receipts are immutable'); END;

CREATE TRIGGER generated_route_accounting_handoff_receipt_retain
BEFORE DELETE ON generated_route_accounting_handoff_receipts
BEGIN SELECT RAISE(ABORT, 'generated route accounting handoff receipts are retained'); END;
