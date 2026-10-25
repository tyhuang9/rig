CREATE TABLE generated_route_completion_receipts (
    activation_receipt_id TEXT PRIMARY KEY
        REFERENCES generated_route_accounting_handoff_receipts(activation_receipt_id) ON DELETE RESTRICT,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    successor_deployment_id TEXT NOT NULL
        REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    creator_job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE RESTRICT,
    creator_worker_attempt INTEGER NOT NULL CHECK (creator_worker_attempt > 0),
    current_job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE RESTRICT,
    current_worker_attempt INTEGER NOT NULL CHECK (current_worker_attempt > 0),
    completion_code TEXT NOT NULL CHECK (completion_code='deployment_completed'),
    completed_at TEXT NOT NULL,
    CHECK (creator_job_id=current_job_id),
    CHECK (creator_worker_attempt<=current_worker_attempt)
);

CREATE TRIGGER generated_route_completion_receipt_valid_insert
BEFORE INSERT ON generated_route_completion_receipts
WHEN NOT EXISTS (
    SELECT 1
    FROM generated_route_activations a
    JOIN generated_route_accounting_handoff_receipts h
      ON h.activation_receipt_id=a.id
     AND h.app_id=a.app_id
     AND h.successor_deployment_id=a.successor_deployment_id
     AND h.predecessor_deployment_id=a.predecessor_deployment_id
     AND h.candidate_roster_digest=a.candidate_roster_digest
     AND h.predecessor_roster_digest=a.predecessor_roster_digest
    JOIN generated_runtime_active_heads head
      ON head.app_id=a.app_id
     AND head.deployment_id=a.successor_deployment_id
     AND head.release_id=a.successor_release_id
     AND head.slot=a.successor_slot
     AND head.generation=a.successor_generation
     AND head.activation_receipt_id=a.id
    JOIN generated_runtime_deployments runtime
      ON runtime.deployment_id=a.successor_deployment_id
     AND runtime.app_id=a.app_id
     AND runtime.release_id=a.successor_release_id
     AND runtime.candidate_slot=a.successor_slot
     AND runtime.phase='succeeded'
    JOIN deployments deployment
      ON deployment.id=runtime.deployment_id
     AND deployment.app_id=runtime.app_id
     AND deployment.job_id=a.creator_job_id
     AND deployment.status='succeeded'
    JOIN jobs job
      ON job.id=deployment.job_id
     AND job.type='deploy'
     AND job.resource_type='application'
     AND job.resource_id=a.app_id
     AND job.status='running'
     AND job.phase<>'cancelling'
    WHERE a.id=NEW.activation_receipt_id
      AND a.app_id=NEW.app_id
      AND a.successor_deployment_id=NEW.successor_deployment_id
      AND a.creator_job_id=NEW.creator_job_id
      AND a.creator_worker_attempt=NEW.creator_worker_attempt
      AND NEW.current_job_id=deployment.job_id
      AND NEW.current_worker_attempt=job.attempt
      AND NEW.creator_worker_attempt<=NEW.current_worker_attempt
      AND NEW.completion_code='deployment_completed'
      AND h.successor_component_count=(
          SELECT COUNT(*) FROM generated_runtime_components c
          WHERE c.deployment_id=a.successor_deployment_id AND c.state='active'
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
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route completion receipt'); END;

CREATE TRIGGER generated_route_completion_receipt_immutable
BEFORE UPDATE ON generated_route_completion_receipts
BEGIN SELECT RAISE(ABORT, 'generated route completion receipts are immutable'); END;

CREATE TRIGGER generated_route_completion_receipt_retain
BEFORE DELETE ON generated_route_completion_receipts
BEGIN SELECT RAISE(ABORT, 'generated route completion receipts are retained'); END;
