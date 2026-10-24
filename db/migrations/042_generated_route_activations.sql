CREATE TABLE generated_route_activations (
    id TEXT PRIMARY KEY
        CHECK (length(id)=36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-'),
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    successor_deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE RESTRICT,
    successor_release_id TEXT NOT NULL REFERENCES releases(id) ON DELETE RESTRICT,
    successor_slot TEXT NOT NULL CHECK (successor_slot IN ('blue','green')),
    successor_generation INTEGER NOT NULL CHECK (successor_generation > 0),
    predecessor_deployment_id TEXT NOT NULL,
    predecessor_release_id TEXT NOT NULL,
    predecessor_slot TEXT NOT NULL CHECK (predecessor_slot IN ('','blue','green')),
    predecessor_generation INTEGER NOT NULL CHECK (predecessor_generation >= 0),
    predecessor_activation_receipt_id TEXT NOT NULL,
    creator_job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE RESTRICT,
    creator_worker_attempt INTEGER NOT NULL CHECK (creator_worker_attempt > 0),
    candidate_roster_digest TEXT NOT NULL CHECK (length(candidate_roster_digest)=64 AND candidate_roster_digest NOT GLOB '*[^0-9a-f]*'),
    predecessor_roster_digest TEXT NOT NULL CHECK (length(predecessor_roster_digest)=64 AND predecessor_roster_digest NOT GLOB '*[^0-9a-f]*'),
    route_intent_digest TEXT NOT NULL CHECK (length(route_intent_digest)=64 AND route_intent_digest NOT GLOB '*[^0-9a-f]*'),
    committed_route_proof_digest TEXT NOT NULL CHECK (length(committed_route_proof_digest)=64 AND committed_route_proof_digest NOT GLOB '*[^0-9a-f]*'),
    activated_at TEXT NOT NULL,
    UNIQUE(app_id, successor_generation),
    UNIQUE(successor_deployment_id),
    CHECK (
        (predecessor_generation=0 AND predecessor_deployment_id='' AND predecessor_release_id='' AND predecessor_slot='' AND predecessor_activation_receipt_id='')
        OR
        (predecessor_generation>0 AND predecessor_deployment_id<>'' AND predecessor_release_id<>'' AND predecessor_slot IN ('blue','green'))
    )
);

ALTER TABLE generated_runtime_active_heads
ADD COLUMN activation_receipt_id TEXT REFERENCES generated_route_activations(id) ON DELETE RESTRICT;

CREATE TRIGGER generated_route_activation_valid_insert
BEFORE INSERT ON generated_route_activations
WHEN NOT EXISTS (
    SELECT 1
    FROM generated_runtime_active_heads h
    JOIN generated_runtime_deployments d
      ON d.deployment_id=NEW.successor_deployment_id AND d.app_id=NEW.app_id
    JOIN deployments base
      ON base.id=d.deployment_id AND base.app_id=d.app_id AND base.job_id=NEW.creator_job_id
    JOIN jobs j
      ON j.id=NEW.creator_job_id AND j.type='deploy' AND j.resource_type='application'
     AND j.resource_id=NEW.app_id AND j.attempt=NEW.creator_worker_attempt
     AND j.status='running' AND j.phase<>'cancelling'
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

CREATE TRIGGER generated_route_activation_immutable
BEFORE UPDATE ON generated_route_activations
BEGIN SELECT RAISE(ABORT, 'generated route activations are immutable'); END;

CREATE TRIGGER generated_route_activation_retain
BEFORE DELETE ON generated_route_activations
BEGIN SELECT RAISE(ABORT, 'generated route activations are retained'); END;

CREATE TRIGGER generated_runtime_active_head_activation_guard
BEFORE UPDATE OF deployment_id,release_id,slot,generation,updated_at,activation_receipt_id
ON generated_runtime_active_heads
WHEN NOT EXISTS (
    SELECT 1 FROM generated_route_activations a
    WHERE a.id=NEW.activation_receipt_id
      AND a.app_id=NEW.app_id
      AND a.successor_deployment_id=NEW.deployment_id
      AND a.successor_release_id=NEW.release_id
      AND a.successor_slot=NEW.slot
      AND a.successor_generation=NEW.generation
      AND a.predecessor_deployment_id=COALESCE(OLD.deployment_id,'')
      AND a.predecessor_release_id=COALESCE(OLD.release_id,'')
      AND a.predecessor_slot=COALESCE(OLD.slot,'')
      AND a.predecessor_generation=OLD.generation
      AND a.predecessor_activation_receipt_id=COALESCE(OLD.activation_receipt_id,'')
)
BEGIN SELECT RAISE(ABORT, 'generated runtime active head requires an exact activation receipt'); END;

CREATE TRIGGER generated_runtime_draining_activation_guard
BEFORE UPDATE OF phase ON generated_runtime_deployments
WHEN OLD.phase='switching_route' AND NEW.phase='draining' AND NOT EXISTS (
    SELECT 1
    FROM generated_runtime_active_heads h
    JOIN generated_route_activations a ON a.id=h.activation_receipt_id
    WHERE h.app_id=NEW.app_id
      AND h.deployment_id=NEW.deployment_id
      AND h.release_id=NEW.release_id
      AND h.slot=NEW.candidate_slot
      AND a.app_id=h.app_id
      AND a.successor_deployment_id=h.deployment_id
      AND a.successor_release_id=h.release_id
      AND a.successor_slot=h.slot
      AND a.successor_generation=h.generation
)
BEGIN SELECT RAISE(ABORT, 'generated runtime draining phase requires an exact activation receipt'); END;
