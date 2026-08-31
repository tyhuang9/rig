CREATE UNIQUE INDEX releases_generated_image_plan_provenance
ON releases(id, deployment_plan_revision_id, deployment_plan_revision_number);

CREATE TABLE generated_image_artifacts (
    id TEXT PRIMARY KEY,
    release_id TEXT NOT NULL,
    deployment_plan_revision_id TEXT NOT NULL,
    deployment_plan_revision_number INTEGER NOT NULL CHECK (deployment_plan_revision_number > 0),
    component_id TEXT NOT NULL CHECK (length(component_id) BETWEEN 1 AND 256),
    compiler_version TEXT NOT NULL CHECK (length(compiler_version) BETWEEN 1 AND 128),
    build_definition_digest TEXT NOT NULL CHECK (
        length(build_definition_digest) = 64
        AND build_definition_digest NOT GLOB '*[^0-9a-f]*'
    ),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    image_content_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('building','ready','failed','cancelled','unavailable')),
    diagnostic_code TEXT CHECK (diagnostic_code IS NULL OR diagnostic_code IN (
        'build_failed',
        'build_timeout',
        'build_context_invalid',
        'build_context_too_large',
        'build_output_truncated',
        'build_disk_exhausted',
        'build_capacity_exceeded',
        'runtime_unavailable',
        'source_unavailable',
        'source_integrity_failed',
        'process_termination_failed',
        'daemon_restarted',
        'internal_error',
        'build_cancelled',
        'image_unavailable'
    )),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT,
    CHECK (
        (state = 'building' AND image_content_id IS NULL AND diagnostic_code IS NULL AND finished_at IS NULL)
        OR
        (
            state = 'ready'
            AND length(image_content_id) = 71
            AND substr(image_content_id, 1, 7) = 'sha256:'
            AND substr(image_content_id, 8) NOT GLOB '*[^0-9a-f]*'
            AND diagnostic_code IS NULL
            AND finished_at IS NOT NULL
            AND updated_at = finished_at
        )
        OR
        (
            state = 'failed'
            AND image_content_id IS NULL
            AND diagnostic_code IS NOT NULL
            AND diagnostic_code != 'build_cancelled'
            AND finished_at IS NOT NULL
            AND updated_at = finished_at
        )
        OR
        (
            state = 'cancelled'
            AND image_content_id IS NULL
            AND diagnostic_code = 'build_cancelled'
            AND finished_at IS NOT NULL
            AND updated_at = finished_at
        )
        OR
        (
            state = 'unavailable'
            AND length(image_content_id) = 71
            AND substr(image_content_id, 1, 7) = 'sha256:'
            AND substr(image_content_id, 8) NOT GLOB '*[^0-9a-f]*'
            AND diagnostic_code = 'image_unavailable'
            AND finished_at IS NOT NULL
        )
    ),
    UNIQUE(release_id, component_id, compiler_version, build_definition_digest, attempt_number),
    FOREIGN KEY(release_id, deployment_plan_revision_id, deployment_plan_revision_number)
        REFERENCES releases(id, deployment_plan_revision_id, deployment_plan_revision_number)
);

CREATE UNIQUE INDEX generated_image_one_building_definition
ON generated_image_artifacts(release_id, component_id, compiler_version, build_definition_digest)
WHERE state = 'building';

CREATE UNIQUE INDEX generated_image_one_ready_definition
ON generated_image_artifacts(release_id, component_id, compiler_version, build_definition_digest)
WHERE state = 'ready';

CREATE INDEX generated_image_artifacts_release
ON generated_image_artifacts(release_id, component_id, attempt_number DESC);

CREATE TRIGGER generated_image_artifact_provenance_immutable
BEFORE UPDATE OF id, release_id, deployment_plan_revision_id, deployment_plan_revision_number, component_id, compiler_version, build_definition_digest, attempt_number, image_content_id, created_at
ON generated_image_artifacts
WHEN NEW.id IS NOT OLD.id
  OR NEW.release_id IS NOT OLD.release_id
  OR NEW.deployment_plan_revision_id IS NOT OLD.deployment_plan_revision_id
  OR NEW.deployment_plan_revision_number IS NOT OLD.deployment_plan_revision_number
  OR NEW.component_id IS NOT OLD.component_id
  OR NEW.compiler_version IS NOT OLD.compiler_version
  OR NEW.build_definition_digest IS NOT OLD.build_definition_digest
  OR NEW.attempt_number IS NOT OLD.attempt_number
  OR (OLD.image_content_id IS NOT NULL AND NEW.image_content_id IS NOT OLD.image_content_id)
  OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'generated image artifact provenance is immutable'); END;

CREATE TRIGGER generated_image_artifact_transition
BEFORE UPDATE ON generated_image_artifacts
WHEN NOT (
    (OLD.state = 'building' AND NEW.state IN ('ready','failed','cancelled'))
    OR (OLD.state = 'ready' AND NEW.state = 'unavailable')
)
BEGIN SELECT RAISE(ABORT, 'invalid generated image artifact transition'); END;

CREATE TRIGGER generated_image_artifact_retain
BEFORE DELETE ON generated_image_artifacts
BEGIN SELECT RAISE(ABORT, 'generated image artifacts are retained'); END;
