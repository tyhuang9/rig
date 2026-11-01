-- Activation creation and activation writing have different durable
-- provenance after a worker reclaims an already-created candidate. Historical
-- 042 rows keep a NULL/NULL writer pair, which denotes creator-as-writer.
ALTER TABLE generated_route_activations
ADD COLUMN writer_job_id TEXT REFERENCES jobs(id);

ALTER TABLE generated_route_activations
ADD COLUMN writer_worker_attempt INTEGER CHECK (writer_worker_attempt IS NULL OR writer_worker_attempt > 0);

DROP TRIGGER generated_route_activation_valid_insert;

CREATE TRIGGER generated_route_activation_valid_insert
BEFORE INSERT ON generated_route_activations
WHEN (NEW.writer_job_id IS NULL) <> (NEW.writer_worker_attempt IS NULL)
OR NOT EXISTS (
    SELECT 1
    FROM generated_runtime_active_heads h
    JOIN generated_runtime_deployments d
      ON d.deployment_id=NEW.successor_deployment_id AND d.app_id=NEW.app_id
    JOIN deployments base
      ON base.id=d.deployment_id AND base.app_id=d.app_id
     AND base.job_id=NEW.creator_job_id
     AND base.job_id=CASE
         WHEN NEW.writer_job_id IS NULL AND NEW.writer_worker_attempt IS NULL THEN NEW.creator_job_id
         ELSE NEW.writer_job_id
     END
    JOIN jobs writer
      ON writer.id=base.job_id AND writer.type='deploy' AND writer.resource_type='application'
     AND writer.resource_id=NEW.app_id
     AND writer.attempt=CASE
         WHEN NEW.writer_job_id IS NULL AND NEW.writer_worker_attempt IS NULL THEN NEW.creator_worker_attempt
         ELSE NEW.writer_worker_attempt
     END
     AND writer.status='running' AND writer.phase<>'cancelling'
    JOIN application_deployment_lifecycle_bindings binding
      ON binding.job_id=base.job_id AND binding.app_id=NEW.app_id
    WHERE h.app_id=NEW.app_id
      AND h.generation=NEW.predecessor_generation
      AND COALESCE(h.deployment_id,'')=NEW.predecessor_deployment_id
      AND COALESCE(h.release_id,'')=NEW.predecessor_release_id
      AND COALESCE(h.slot,'')=NEW.predecessor_slot
      AND COALESCE(h.activation_receipt_id,'')=NEW.predecessor_activation_receipt_id
      AND NEW.successor_generation=h.generation+1
      AND d.release_id=NEW.successor_release_id
      AND d.candidate_slot=NEW.successor_slot
      AND d.previous_active_deployment_id IS
          CASE WHEN NEW.predecessor_deployment_id='' THEN NULL ELSE NEW.predecessor_deployment_id END
      AND d.previous_active_slot IS
          CASE WHEN NEW.predecessor_slot='' THEN NULL ELSE NEW.predecessor_slot END
      AND d.phase='switching_route'
      AND base.status='waiting_health'
      AND base.provenance_initialized=1
      AND NEW.creator_worker_attempt<=CASE
          WHEN NEW.writer_job_id IS NULL AND NEW.writer_worker_attempt IS NULL THEN NEW.creator_worker_attempt
          ELSE NEW.writer_worker_attempt
      END
      AND EXISTS (
          SELECT 1 FROM generated_runtime_components c
          WHERE c.deployment_id=d.deployment_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM generated_runtime_components c
          WHERE c.deployment_id=d.deployment_id AND c.state<>'healthy'
      )
      AND NOT EXISTS (
          SELECT 1 FROM generated_runtime_components c
          WHERE c.deployment_id=d.deployment_id
            AND NOT EXISTS (
                SELECT 1 FROM generated_candidate_intents i
                WHERE i.deployment_id=c.deployment_id AND i.component_name=c.component_name
                  AND i.creator_job_id=NEW.creator_job_id
                  AND i.creator_worker_attempt=NEW.creator_worker_attempt
            )
      )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route activation'); END;

DROP TRIGGER generated_route_completion_receipt_valid_insert;

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
      AND ((a.writer_job_id IS NULL AND a.writer_worker_attempt IS NULL)
           OR (a.writer_job_id IS NOT NULL AND a.writer_worker_attempt IS NOT NULL))
      AND NEW.current_job_id=deployment.job_id
      AND NEW.current_job_id=CASE
          WHEN a.writer_job_id IS NULL AND a.writer_worker_attempt IS NULL THEN a.creator_job_id
          ELSE a.writer_job_id
      END
      AND NEW.current_worker_attempt=job.attempt
      AND NEW.creator_worker_attempt<=NEW.current_worker_attempt
      AND CASE
          WHEN a.writer_job_id IS NULL AND a.writer_worker_attempt IS NULL THEN a.creator_worker_attempt
          ELSE a.writer_worker_attempt
      END<=NEW.current_worker_attempt
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
                WHERE i.deployment_id=c.deployment_id AND i.component_name=c.component_name
            ))
      )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route completion receipt'); END;
