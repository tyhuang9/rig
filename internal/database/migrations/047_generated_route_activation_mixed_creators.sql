-- Sequential candidate creation may span worker attempts after a cold restart.
-- The immutable component receipts retain their truthful per-attempt creators;
-- the activation creator is the earliest attempt in the exact same-job roster,
-- while the explicit writer is the current worker that finishes activation.
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
      AND NEW.creator_worker_attempt=(
          SELECT MIN(i.creator_worker_attempt)
          FROM generated_runtime_components c
          JOIN generated_candidate_intents i
            ON i.deployment_id=c.deployment_id AND i.component_name=c.component_name
          WHERE c.deployment_id=d.deployment_id
      )
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
                  AND i.creator_worker_attempt<=CASE
                      WHEN NEW.writer_job_id IS NULL AND NEW.writer_worker_attempt IS NULL
                          THEN NEW.creator_worker_attempt
                      ELSE NEW.writer_worker_attempt
                  END
            )
      )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated route activation'); END;
