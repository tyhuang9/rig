CREATE TABLE generated_candidate_intents (
    id TEXT NOT NULL PRIMARY KEY CHECK (
        length(id)=36
        AND substr(id,9,1)='-'
        AND substr(id,14,1)='-'
        AND substr(id,19,1)='-'
        AND substr(id,24,1)='-'
        AND length(replace(id,'-',''))=32
        AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*'
    ),
    deployment_id TEXT NOT NULL,
    component_name TEXT NOT NULL CHECK (length(component_name) BETWEEN 1 AND 256),
    creator_job_id TEXT NOT NULL REFERENCES jobs(id),
    creator_worker_attempt INTEGER NOT NULL CHECK (creator_worker_attempt > 0),
    configuration_revision_id TEXT NOT NULL REFERENCES application_configuration_revisions(id),
    configuration_revision_number INTEGER NOT NULL CHECK (configuration_revision_number > 0),
    image_artifact_id TEXT NOT NULL REFERENCES generated_image_artifacts(id),
    image_artifact_attempt INTEGER NOT NULL CHECK (image_artifact_attempt > 0),
    environment_operation_id TEXT NOT NULL REFERENCES generated_image_artifacts(id),
    environment_staging_attempt INTEGER NOT NULL CHECK (environment_staging_attempt > 0),
    candidate_policy_version TEXT NOT NULL CHECK (length(candidate_policy_version) BETWEEN 1 AND 128),
    candidate_policy_digest TEXT NOT NULL CHECK (
        length(candidate_policy_digest)=64
        AND candidate_policy_digest NOT GLOB '*[^0-9a-f]*'
    ),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    CHECK (environment_operation_id=image_artifact_id),
    CHECK (environment_staging_attempt=creator_worker_attempt),
    UNIQUE(deployment_id,component_name),
    FOREIGN KEY(deployment_id,component_name)
        REFERENCES generated_runtime_components(deployment_id,component_name)
);

-- The protected owner inserts this receipt while the component is still
-- image_ready, then advances it to starting in the same guarded transaction.
-- Current creator eligibility is checked only at insertion: the retained
-- receipt continues to describe the original creator after a later attempt.
CREATE TRIGGER generated_candidate_intent_valid_insert
BEFORE INSERT ON generated_candidate_intents
WHEN EXISTS (
    SELECT 1 FROM generated_candidate_intents i
    WHERE i.id=NEW.id
       OR (i.deployment_id=NEW.deployment_id AND i.component_name=NEW.component_name)
)
OR NOT EXISTS (
    SELECT 1
    FROM generated_runtime_components c
    JOIN generated_runtime_deployments rd
      ON rd.deployment_id=c.deployment_id
    JOIN deployments d
      ON d.id=rd.deployment_id AND d.app_id=rd.app_id
    JOIN releases r
      ON r.id=rd.release_id AND r.app_id=rd.app_id
    JOIN jobs j
      ON j.id=d.job_id
    JOIN application_configuration_revisions cr
      ON cr.id=NEW.configuration_revision_id AND cr.app_id=rd.app_id
    JOIN generated_image_artifacts a
      ON a.id=NEW.image_artifact_id
    WHERE c.deployment_id=NEW.deployment_id
      AND c.component_name=NEW.component_name
      AND c.slot=rd.candidate_slot
      AND c.state='image_ready'
      AND c.image_artifact_id=NEW.image_artifact_id
      AND c.container_name IS NULL
      AND c.container_id IS NULL
      AND rd.phase='starting_candidate'
      AND d.release_id=rd.release_id
      AND d.runtime_strategy='generated_node'
      AND d.provenance_initialized=1
      AND d.status='applying'
      AND d.deployment_plan_revision_id=rd.deployment_plan_revision_id
      AND d.deployment_plan_revision_number=rd.deployment_plan_revision_number
      AND d.actual_configuration_revision_id=NEW.configuration_revision_id
      AND d.actual_configuration_revision_number=NEW.configuration_revision_number
      AND r.deployment_plan_revision_id=rd.deployment_plan_revision_id
      AND r.deployment_plan_revision_number=rd.deployment_plan_revision_number
      AND cr.revision_number=NEW.configuration_revision_number
      AND cr.bundle_version=2
      AND cr.deployment_plan_revision_id=rd.deployment_plan_revision_id
      AND cr.deployment_plan_revision_number=rd.deployment_plan_revision_number
      AND j.id=NEW.creator_job_id
      AND j.type='deploy'
      AND j.resource_type='application'
      AND j.resource_id=rd.app_id
      AND j.status='running'
      AND j.phase<>'cancelling'
      AND j.attempt=NEW.creator_worker_attempt
      AND a.release_id=rd.release_id
      AND a.deployment_plan_revision_id=rd.deployment_plan_revision_id
      AND a.deployment_plan_revision_number=rd.deployment_plan_revision_number
      AND a.component_id=NEW.component_name
      AND a.attempt_number=NEW.image_artifact_attempt
      AND a.state='ready'
)
BEGIN SELECT RAISE(ABORT, 'invalid generated candidate intent'); END;

CREATE TRIGGER generated_candidate_intent_immutable
BEFORE UPDATE ON generated_candidate_intents
BEGIN SELECT RAISE(ABORT, 'generated candidate intents are immutable'); END;

CREATE TRIGGER generated_candidate_intent_retain
BEFORE DELETE ON generated_candidate_intents
BEGIN SELECT RAISE(ABORT, 'generated candidate intents are retained'); END;
