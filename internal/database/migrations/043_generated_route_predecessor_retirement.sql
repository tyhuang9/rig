CREATE TABLE generated_route_predecessor_retirements (
    activation_receipt_id TEXT NOT NULL REFERENCES generated_route_activations(id) ON DELETE RESTRICT,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    successor_deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    predecessor_deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    component_name TEXT NOT NULL,
    candidate_receipt_id TEXT NOT NULL REFERENCES generated_candidate_intents(id) ON DELETE RESTRICT,
    container_id TEXT NOT NULL CHECK (length(container_id)=64 AND container_id NOT GLOB '*[^0-9a-f]*'),
    candidate_policy_digest TEXT NOT NULL CHECK (length(candidate_policy_digest)=64 AND candidate_policy_digest NOT GLOB '*[^0-9a-f]*'),
    observed_absent_at TEXT NOT NULL,
    PRIMARY KEY (activation_receipt_id, component_name),
    FOREIGN KEY (predecessor_deployment_id, component_name)
        REFERENCES generated_runtime_components(deployment_id, component_name) ON DELETE RESTRICT
);

CREATE TABLE generated_route_predecessor_retirement_receipts (
    activation_receipt_id TEXT PRIMARY KEY REFERENCES generated_route_activations(id) ON DELETE RESTRICT,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    successor_deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    predecessor_deployment_id TEXT NOT NULL,
    predecessor_roster_digest TEXT NOT NULL CHECK (length(predecessor_roster_digest)=64 AND predecessor_roster_digest NOT GLOB '*[^0-9a-f]*'),
    component_count INTEGER NOT NULL CHECK (component_count >= 0),
    retired_at TEXT NOT NULL
);

CREATE TRIGGER generated_route_predecessor_retirement_valid_insert
BEFORE INSERT ON generated_route_predecessor_retirements
WHEN NOT EXISTS (
    SELECT 1
    FROM generated_route_activations a
    JOIN generated_runtime_components c
      ON c.deployment_id=NEW.predecessor_deployment_id
     AND c.component_name=NEW.component_name
    JOIN generated_candidate_intents i
      ON i.id=NEW.candidate_receipt_id
     AND i.deployment_id=c.deployment_id
     AND i.component_name=c.component_name
    WHERE a.id=NEW.activation_receipt_id
      AND a.app_id=NEW.app_id
      AND a.successor_deployment_id=NEW.successor_deployment_id
      AND a.predecessor_deployment_id=NEW.predecessor_deployment_id
      AND a.predecessor_deployment_id<>''
      AND c.state='stopped'
      AND c.container_id=NEW.container_id
      AND i.candidate_policy_digest=NEW.candidate_policy_digest
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route predecessor retirement'); END;

CREATE TRIGGER generated_route_predecessor_retirement_immutable
BEFORE UPDATE ON generated_route_predecessor_retirements
BEGIN SELECT RAISE(ABORT, 'generated route predecessor retirements are immutable'); END;

CREATE TRIGGER generated_route_predecessor_retirement_retain
BEFORE DELETE ON generated_route_predecessor_retirements
BEGIN SELECT RAISE(ABORT, 'generated route predecessor retirements are retained'); END;

CREATE TRIGGER generated_route_predecessor_retirement_receipt_valid_insert
BEFORE INSERT ON generated_route_predecessor_retirement_receipts
WHEN NOT EXISTS (
    SELECT 1 FROM generated_route_activations a
    WHERE a.id=NEW.activation_receipt_id
      AND a.app_id=NEW.app_id
      AND a.successor_deployment_id=NEW.successor_deployment_id
      AND a.predecessor_deployment_id=NEW.predecessor_deployment_id
      AND a.predecessor_roster_digest=NEW.predecessor_roster_digest
      AND NEW.component_count=(
          SELECT COUNT(*) FROM generated_runtime_components c
          WHERE c.deployment_id=a.predecessor_deployment_id
      )
      AND NEW.component_count=(
          SELECT COUNT(*) FROM generated_route_predecessor_retirements r
          WHERE r.activation_receipt_id=a.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM generated_runtime_components c
          WHERE c.deployment_id=a.predecessor_deployment_id
            AND (c.state<>'stopped' OR NOT EXISTS (
                SELECT 1 FROM generated_route_predecessor_retirements r
                WHERE r.activation_receipt_id=a.id
                  AND r.predecessor_deployment_id=c.deployment_id
                  AND r.component_name=c.component_name
                  AND r.container_id=c.container_id
            ))
      )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route predecessor retirement receipt'); END;

CREATE TRIGGER generated_route_predecessor_retirement_receipt_immutable
BEFORE UPDATE ON generated_route_predecessor_retirement_receipts
BEGIN SELECT RAISE(ABORT, 'generated route predecessor retirement receipts are immutable'); END;

CREATE TRIGGER generated_route_predecessor_retirement_receipt_retain
BEFORE DELETE ON generated_route_predecessor_retirement_receipts
BEGIN SELECT RAISE(ABORT, 'generated route predecessor retirement receipts are retained'); END;
