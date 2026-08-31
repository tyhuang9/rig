ALTER TABLE deployments ADD COLUMN runtime_strategy TEXT NOT NULL DEFAULT 'compose'
    CHECK (runtime_strategy IN ('compose','generated_node'));
ALTER TABLE deployments ADD COLUMN deployment_plan_revision_id TEXT;
ALTER TABLE deployments ADD COLUMN deployment_plan_revision_number INTEGER
    CHECK (deployment_plan_revision_number IS NULL OR deployment_plan_revision_number > 0);

CREATE INDEX deployments_runtime_provenance
ON deployments(app_id, runtime_strategy, deployment_plan_revision_number, id);

CREATE TRIGGER deployment_runtime_provenance_valid_insert BEFORE INSERT ON deployments
WHEN
    ((NEW.deployment_plan_revision_id IS NULL) <> (NEW.deployment_plan_revision_number IS NULL))
    OR (NEW.deployment_plan_revision_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM deployment_plan_revisions p
        WHERE p.id=NEW.deployment_plan_revision_id AND p.app_id=NEW.app_id
          AND p.revision_number=NEW.deployment_plan_revision_number AND p.strategy=NEW.runtime_strategy
    ))
    OR (NEW.provenance_initialized=1 AND NEW.runtime_strategy='generated_node' AND (
        NEW.release_id IS NULL OR NEW.deployment_plan_revision_id IS NULL OR NOT EXISTS (
            SELECT 1 FROM releases r
            WHERE r.id=NEW.release_id AND r.app_id=NEW.app_id
              AND r.deployment_plan_revision_id=NEW.deployment_plan_revision_id
              AND r.deployment_plan_revision_number=NEW.deployment_plan_revision_number
        )
    ))
    OR (NEW.provenance_initialized=1 AND NEW.runtime_strategy='compose' AND NEW.release_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM releases r
        WHERE r.id=NEW.release_id AND r.app_id=NEW.app_id AND (
            (r.deployment_plan_revision_id IS NULL AND NEW.deployment_plan_revision_id IS NULL)
            OR (r.deployment_plan_revision_id=NEW.deployment_plan_revision_id AND r.deployment_plan_revision_number=NEW.deployment_plan_revision_number)
        )
    ))
BEGIN SELECT RAISE(ABORT, 'invalid deployment runtime provenance'); END;

CREATE TRIGGER deployment_runtime_provenance_valid_update BEFORE UPDATE OF app_id,release_id,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number,provenance_initialized ON deployments
WHEN
    ((NEW.deployment_plan_revision_id IS NULL) <> (NEW.deployment_plan_revision_number IS NULL))
    OR (NEW.deployment_plan_revision_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM deployment_plan_revisions p
        WHERE p.id=NEW.deployment_plan_revision_id AND p.app_id=NEW.app_id
          AND p.revision_number=NEW.deployment_plan_revision_number AND p.strategy=NEW.runtime_strategy
    ))
    OR (NEW.provenance_initialized=1 AND NEW.runtime_strategy='generated_node' AND (
        NEW.release_id IS NULL OR NEW.deployment_plan_revision_id IS NULL OR NOT EXISTS (
            SELECT 1 FROM releases r
            WHERE r.id=NEW.release_id AND r.app_id=NEW.app_id
              AND r.deployment_plan_revision_id=NEW.deployment_plan_revision_id
              AND r.deployment_plan_revision_number=NEW.deployment_plan_revision_number
        )
    ))
    OR (NEW.provenance_initialized=1 AND NEW.runtime_strategy='compose' AND NEW.release_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM releases r
        WHERE r.id=NEW.release_id AND r.app_id=NEW.app_id AND (
            (r.deployment_plan_revision_id IS NULL AND NEW.deployment_plan_revision_id IS NULL)
            OR (r.deployment_plan_revision_id=NEW.deployment_plan_revision_id AND r.deployment_plan_revision_number=NEW.deployment_plan_revision_number)
        )
    ))
BEGIN SELECT RAISE(ABORT, 'invalid deployment runtime provenance'); END;

CREATE TRIGGER deployment_runtime_provenance_immutable BEFORE UPDATE OF runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number ON deployments
WHEN (NEW.runtime_strategy IS NOT OLD.runtime_strategy
      OR NEW.deployment_plan_revision_id IS NOT OLD.deployment_plan_revision_id
      OR NEW.deployment_plan_revision_number IS NOT OLD.deployment_plan_revision_number)
  AND NOT (OLD.status='preparing' AND OLD.provenance_initialized=0 AND NEW.provenance_initialized=1)
BEGIN SELECT RAISE(ABORT, 'deployment runtime provenance is immutable'); END;

CREATE TABLE generated_runtime_deployments (
    deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    release_id TEXT NOT NULL REFERENCES releases(id),
    deployment_plan_revision_id TEXT NOT NULL,
    deployment_plan_revision_number INTEGER NOT NULL CHECK (deployment_plan_revision_number > 0),
    candidate_slot TEXT NOT NULL CHECK (candidate_slot IN ('blue','green')),
    previous_active_deployment_id TEXT REFERENCES generated_runtime_deployments(deployment_id),
    previous_active_slot TEXT CHECK (previous_active_slot IS NULL OR previous_active_slot IN ('blue','green')),
    phase TEXT NOT NULL CHECK (phase IN (
        'preflight','building','migrating','starting_candidate','waiting_health',
        'switching_route','draining','succeeded','failed','cancelled'
    )),
    migration_state TEXT NOT NULL CHECK (migration_state IN ('not_required','pending','running','succeeded','failed')),
    diagnostic_code TEXT CHECK (diagnostic_code IS NULL OR diagnostic_code IN (
        'insufficient_replacement_capacity','deployment_plan_review_required','migration_failed',
        'start_failed','health_failed','route_switch_failed','runtime_unavailable',
        'daemon_restarted','cancelled','internal_error'
    )),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    migration_started_at TEXT,
    migration_finished_at TEXT,
    finished_at TEXT,
    CHECK ((previous_active_deployment_id IS NULL) = (previous_active_slot IS NULL)),
    CHECK (
        (phase IN ('succeeded','failed','cancelled') AND finished_at IS NOT NULL)
        OR (phase NOT IN ('succeeded','failed','cancelled') AND finished_at IS NULL)
    ),
    CHECK (
        (phase='failed' AND diagnostic_code IS NOT NULL AND diagnostic_code<>'cancelled')
        OR (phase='cancelled' AND diagnostic_code='cancelled')
        OR (phase NOT IN ('failed','cancelled') AND diagnostic_code IS NULL)
    ),
    CHECK (
        (migration_state IN ('not_required','pending') AND migration_started_at IS NULL AND migration_finished_at IS NULL)
        OR (migration_state='running' AND migration_started_at IS NOT NULL AND migration_finished_at IS NULL)
        OR (migration_state IN ('succeeded','failed') AND migration_started_at IS NOT NULL AND migration_finished_at IS NOT NULL)
    ),
    UNIQUE(app_id, deployment_id),
    FOREIGN KEY(app_id, deployment_plan_revision_id) REFERENCES deployment_plan_revisions(app_id, id)
);
CREATE INDEX generated_runtime_deployments_recovery
ON generated_runtime_deployments(phase, updated_at, deployment_id);

CREATE TABLE generated_runtime_components (
    deployment_id TEXT NOT NULL REFERENCES generated_runtime_deployments(deployment_id) ON DELETE CASCADE,
    component_name TEXT NOT NULL CHECK (length(component_name) BETWEEN 1 AND 256),
    slot TEXT NOT NULL CHECK (slot IN ('blue','green')),
    image_artifact_id TEXT REFERENCES generated_image_artifacts(id),
    container_name TEXT CHECK (container_name IS NULL OR length(container_name) BETWEEN 1 AND 128),
    container_id TEXT CHECK (container_id IS NULL OR (length(container_id)=64 AND container_id NOT GLOB '*[^0-9a-f]*')),
    state TEXT NOT NULL CHECK (state IN ('pending','image_ready','starting','running','healthy','active','draining','stopped','failed')),
    diagnostic_code TEXT CHECK (diagnostic_code IS NULL OR diagnostic_code IN ('start_failed','health_failed','runtime_unavailable','daemon_restarted','cancelled','internal_error')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT,
    CHECK ((state IN ('stopped','failed') AND finished_at IS NOT NULL) OR (state NOT IN ('stopped','failed') AND finished_at IS NULL)),
    CHECK ((state='failed' AND diagnostic_code IS NOT NULL) OR (state<>'failed' AND diagnostic_code IS NULL)),
    CHECK ((state='pending' AND image_artifact_id IS NULL AND container_name IS NULL AND container_id IS NULL)
        OR (state='image_ready' AND image_artifact_id IS NOT NULL AND container_name IS NULL AND container_id IS NULL)
        OR (state='starting' AND image_artifact_id IS NOT NULL AND container_name IS NOT NULL AND container_id IS NULL)
        OR (state IN ('running','healthy','active','draining') AND image_artifact_id IS NOT NULL AND container_name IS NOT NULL AND container_id IS NOT NULL)
        OR (state IN ('stopped','failed'))),
    PRIMARY KEY(deployment_id, component_name)
);

CREATE TABLE generated_runtime_active_heads (
    app_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    deployment_id TEXT,
    release_id TEXT,
    slot TEXT CHECK (slot IS NULL OR slot IN ('blue','green')),
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    updated_at TEXT,
    CHECK (
        (generation=0 AND deployment_id IS NULL AND release_id IS NULL AND slot IS NULL AND updated_at IS NULL)
        OR (generation>0 AND deployment_id IS NOT NULL AND release_id IS NOT NULL AND slot IS NOT NULL AND updated_at IS NOT NULL)
    ),
    FOREIGN KEY(app_id, deployment_id) REFERENCES generated_runtime_deployments(app_id, deployment_id),
    FOREIGN KEY(release_id) REFERENCES releases(id)
);

CREATE TRIGGER generated_runtime_deployment_valid_insert BEFORE INSERT ON generated_runtime_deployments
WHEN NOT EXISTS (
    SELECT 1 FROM deployments d
    JOIN deployment_plan_revisions p
      ON p.id=d.deployment_plan_revision_id AND p.app_id=d.app_id AND p.revision_number=d.deployment_plan_revision_number
    WHERE d.id=NEW.deployment_id AND d.app_id=NEW.app_id AND d.release_id=NEW.release_id
      AND d.runtime_strategy='generated_node' AND d.provenance_initialized=1
      AND d.deployment_plan_revision_id=NEW.deployment_plan_revision_id
      AND d.deployment_plan_revision_number=NEW.deployment_plan_revision_number
      AND p.strategy='generated_node'
) OR (
    (NEW.migration_state='not_required' AND EXISTS (
        SELECT 1 FROM deployment_plan_revisions p
        WHERE p.id=NEW.deployment_plan_revision_id AND p.app_id=NEW.app_id AND p.migration_evidence_digest<>''
    ))
    OR (NEW.migration_state='pending' AND NOT EXISTS (
        SELECT 1 FROM deployment_plan_revisions p
        JOIN deployment_plan_migration_approvals a ON a.revision_id=p.id AND a.app_id=p.app_id
        WHERE p.id=NEW.deployment_plan_revision_id AND p.app_id=NEW.app_id AND p.migration_evidence_digest<>''
    ))
) OR NOT EXISTS (
    SELECT 1 FROM generated_runtime_active_heads h
    WHERE h.app_id=NEW.app_id AND (
        (h.deployment_id IS NULL AND NEW.previous_active_deployment_id IS NULL AND NEW.previous_active_slot IS NULL AND NEW.candidate_slot='blue')
        OR (h.deployment_id=NEW.previous_active_deployment_id AND h.slot=NEW.previous_active_slot AND h.slot<>NEW.candidate_slot)
    )
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime deployment'); END;

CREATE TRIGGER generated_runtime_deployment_identity_immutable BEFORE UPDATE OF app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,candidate_slot,previous_active_deployment_id,previous_active_slot,created_at ON generated_runtime_deployments
BEGIN SELECT RAISE(ABORT, 'generated runtime deployment identity is immutable'); END;
CREATE TRIGGER generated_runtime_deployment_no_delete BEFORE DELETE ON generated_runtime_deployments
BEGIN SELECT RAISE(ABORT, 'generated runtime deployments are retained'); END;

CREATE TRIGGER generated_runtime_deployment_phase_transition BEFORE UPDATE OF phase ON generated_runtime_deployments
WHEN NOT (
    (OLD.phase='preflight' AND NEW.phase IN ('building','failed','cancelled'))
    OR (OLD.phase='building' AND NEW.phase='migrating' AND OLD.migration_state='pending')
    OR (OLD.phase='building' AND NEW.phase='starting_candidate' AND OLD.migration_state='not_required')
    OR (OLD.phase='building' AND NEW.phase IN ('failed','cancelled'))
    OR (OLD.phase='migrating' AND NEW.phase='starting_candidate' AND OLD.migration_state='succeeded')
    OR (OLD.phase='migrating' AND NEW.phase IN ('failed','cancelled'))
    OR (OLD.phase='starting_candidate' AND NEW.phase IN ('waiting_health','failed','cancelled'))
    OR (OLD.phase='waiting_health' AND NEW.phase IN ('switching_route','failed','cancelled'))
    OR (OLD.phase='switching_route' AND NEW.phase IN ('draining','failed','cancelled'))
    OR (OLD.phase='draining' AND NEW.phase IN ('succeeded','failed','cancelled'))
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime deployment phase transition'); END;

CREATE TRIGGER generated_runtime_migration_transition BEFORE UPDATE OF migration_state ON generated_runtime_deployments
WHEN NOT (
    (OLD.migration_state='pending' AND NEW.migration_state='running' AND NEW.phase='migrating')
    OR (OLD.migration_state='running' AND NEW.migration_state IN ('succeeded','failed') AND OLD.phase='migrating')
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime migration transition'); END;

CREATE TRIGGER generated_runtime_component_valid_insert BEFORE INSERT ON generated_runtime_components
WHEN NOT EXISTS (
    SELECT 1 FROM generated_runtime_deployments d
    WHERE d.deployment_id=NEW.deployment_id AND d.candidate_slot=NEW.slot
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime component'); END;

CREATE TRIGGER generated_runtime_component_artifact_valid BEFORE UPDATE OF image_artifact_id ON generated_runtime_components
WHEN NEW.image_artifact_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM generated_image_artifacts a
    JOIN generated_runtime_deployments d ON d.deployment_id=NEW.deployment_id
    WHERE a.id=NEW.image_artifact_id AND a.release_id=d.release_id
      AND a.deployment_plan_revision_id=d.deployment_plan_revision_id
      AND a.deployment_plan_revision_number=d.deployment_plan_revision_number
      AND a.component_id=NEW.component_name AND a.state='ready'
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime component artifact'); END;

CREATE TRIGGER generated_runtime_component_artifact_valid_insert BEFORE INSERT ON generated_runtime_components
WHEN NEW.image_artifact_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM generated_image_artifacts a
    JOIN generated_runtime_deployments d ON d.deployment_id=NEW.deployment_id
    WHERE a.id=NEW.image_artifact_id AND a.release_id=d.release_id
      AND a.deployment_plan_revision_id=d.deployment_plan_revision_id
      AND a.deployment_plan_revision_number=d.deployment_plan_revision_number
      AND a.component_id=NEW.component_name AND a.state='ready'
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime component artifact'); END;

CREATE TRIGGER generated_runtime_component_runtime_identity_immutable BEFORE UPDATE OF image_artifact_id,container_name,container_id ON generated_runtime_components
WHEN (NEW.image_artifact_id IS NOT OLD.image_artifact_id AND NOT (OLD.state='pending' AND NEW.state='image_ready' AND OLD.image_artifact_id IS NULL AND NEW.image_artifact_id IS NOT NULL))
  OR (NEW.container_name IS NOT OLD.container_name AND NOT (OLD.state='image_ready' AND NEW.state='starting' AND OLD.container_name IS NULL AND NEW.container_name IS NOT NULL))
  OR (NEW.container_id IS NOT OLD.container_id AND NOT (OLD.state='starting' AND NEW.state='running' AND OLD.container_id IS NULL AND NEW.container_id IS NOT NULL))
BEGIN SELECT RAISE(ABORT, 'generated runtime component identity is immutable'); END;

CREATE TRIGGER generated_runtime_component_identity_immutable BEFORE UPDATE OF deployment_id,component_name,slot,created_at ON generated_runtime_components
BEGIN SELECT RAISE(ABORT, 'generated runtime component identity is immutable'); END;
CREATE TRIGGER generated_runtime_component_no_delete BEFORE DELETE ON generated_runtime_components
BEGIN SELECT RAISE(ABORT, 'generated runtime components are retained'); END;

CREATE TRIGGER generated_runtime_component_state_transition BEFORE UPDATE OF state ON generated_runtime_components
WHEN NOT (
    (OLD.state='pending' AND NEW.state IN ('image_ready','failed'))
    OR (OLD.state='image_ready' AND NEW.state IN ('starting','failed'))
    OR (OLD.state='starting' AND NEW.state IN ('running','failed'))
    OR (OLD.state='running' AND NEW.state IN ('healthy','failed'))
    OR (OLD.state='healthy' AND NEW.state IN ('active','failed'))
    OR (OLD.state='active' AND NEW.state IN ('draining','stopped','failed'))
    OR (OLD.state='draining' AND NEW.state IN ('stopped','failed'))
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime component state transition'); END;

CREATE TRIGGER generated_runtime_active_head_valid_update BEFORE UPDATE OF deployment_id,release_id,slot,generation,updated_at ON generated_runtime_active_heads
WHEN NEW.generation<>OLD.generation+1 OR NEW.deployment_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM generated_runtime_deployments d
    WHERE d.deployment_id=NEW.deployment_id AND d.app_id=NEW.app_id
      AND d.release_id=NEW.release_id AND d.candidate_slot=NEW.slot
      AND d.phase IN ('switching_route','draining','succeeded')
)
BEGIN SELECT RAISE(ABORT, 'invalid generated runtime active head'); END;
INSERT INTO generated_runtime_active_heads(app_id) SELECT id FROM applications;
CREATE TRIGGER generated_runtime_active_head_create AFTER INSERT ON applications
BEGIN INSERT INTO generated_runtime_active_heads(app_id) VALUES(NEW.id); END;
