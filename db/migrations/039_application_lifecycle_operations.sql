CREATE TABLE application_lifecycle_operations (
    job_id TEXT PRIMARY KEY REFERENCES jobs(id),
    app_id TEXT NOT NULL REFERENCES applications(id),
    desired_revision INTEGER NOT NULL CHECK (desired_revision > 0),
    expected_revision INTEGER NOT NULL CHECK (expected_revision >= 0),
    action TEXT NOT NULL CHECK (action IN ('start','stop','restart')),
    actor_id TEXT NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    version INTEGER NOT NULL CHECK (version = 1),
    runtime_strategy TEXT NOT NULL CHECK (runtime_strategy = 'generated_node'),
    baseline_disposition TEXT NOT NULL CHECK (baseline_disposition IN ('running','stopped')),
    deployment_id TEXT NOT NULL,
    active_generation INTEGER NOT NULL CHECK (active_generation > 0),
    release_id TEXT NOT NULL,
    slot TEXT NOT NULL CHECK (slot IN ('blue','green')),
    plan_revision_id TEXT NOT NULL,
    plan_revision_number INTEGER NOT NULL CHECK (plan_revision_number > 0),
    configuration_revision_id TEXT NOT NULL,
    configuration_revision_number INTEGER NOT NULL CHECK (configuration_revision_number >= 0),
    configuration_format INTEGER NOT NULL CHECK (configuration_format IN (0,1,2)),
    pins_json TEXT NOT NULL CHECK (length(pins_json) BETWEEN 2 AND 65536 AND json_valid(pins_json)),
    pins_digest TEXT NOT NULL CHECK (length(pins_digest)=64 AND pins_digest NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    UNIQUE(app_id, desired_revision),
    UNIQUE(app_id, action, idempotency_key),
    FOREIGN KEY(app_id, desired_revision)
        REFERENCES application_desired_lifecycle_transitions(app_id, revision_number),
    FOREIGN KEY(app_id, deployment_id)
        REFERENCES generated_runtime_deployments(app_id, deployment_id)
);

-- Components are inserted first inside the acceptance savepoint. The deferred
-- parent key lets the operation INSERT verify the complete set atomically.
CREATE TABLE application_lifecycle_operation_components (
    job_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal IN (0,1)),
    component_name TEXT NOT NULL CHECK (length(component_name) BETWEEN 1 AND 256),
    PRIMARY KEY(job_id, component_name),
    UNIQUE(job_id, ordinal),
    FOREIGN KEY(job_id) REFERENCES application_lifecycle_operations(job_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TRIGGER application_lifecycle_operation_component_valid_insert
BEFORE INSERT ON application_lifecycle_operation_components
WHEN EXISTS (
        SELECT 1 FROM application_lifecycle_operation_components c
        WHERE c.job_id=NEW.job_id AND (c.ordinal=NEW.ordinal OR c.component_name=NEW.component_name)
    )
    OR EXISTS (SELECT 1 FROM application_lifecycle_operations o WHERE o.job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'invalid application lifecycle operation component'); END;

CREATE TRIGGER application_lifecycle_operation_valid_insert
BEFORE INSERT ON application_lifecycle_operations
WHEN EXISTS (
        SELECT 1 FROM application_lifecycle_operations o
        WHERE o.job_id=NEW.job_id
           OR (o.app_id=NEW.app_id AND o.desired_revision=NEW.desired_revision)
           OR (o.app_id=NEW.app_id AND o.action=NEW.action AND o.idempotency_key=NEW.idempotency_key)
    )
    OR NOT EXISTS (
        SELECT 1
        FROM jobs j
        JOIN application_desired_lifecycle_transitions t
          ON t.job_id=j.id AND t.action=j.type
        JOIN application_desired_lifecycle_heads h
          ON h.app_id=t.app_id AND h.revision_number=t.revision_number
        JOIN deployments d
          ON d.id=NEW.deployment_id AND d.app_id=t.app_id
        JOIN generated_runtime_deployments rd
          ON rd.deployment_id=d.id AND rd.app_id=d.app_id
        JOIN generated_runtime_active_heads ah
          ON ah.app_id=d.app_id AND ah.deployment_id=d.id
        JOIN deployment_plan_revisions p
          ON p.id=d.deployment_plan_revision_id AND p.app_id=d.app_id
             AND p.revision_number=d.deployment_plan_revision_number
        WHERE j.id=NEW.job_id AND j.type=NEW.action
          AND j.resource_type='application' AND j.resource_id=NEW.app_id
          AND j.requested_by=NEW.actor_id AND j.idempotency_key=NEW.idempotency_key
          AND j.input_json='{}'
          AND t.app_id=NEW.app_id AND t.revision_number=NEW.desired_revision
          AND t.expected_revision=NEW.expected_revision AND t.actor_id=NEW.actor_id
          AND d.status='succeeded' AND d.provenance_initialized=1
          AND d.runtime_strategy='generated_node'
          AND d.release_id=NEW.release_id
          AND d.deployment_plan_revision_id=NEW.plan_revision_id
          AND d.deployment_plan_revision_number=NEW.plan_revision_number
          AND COALESCE(d.actual_configuration_revision_id,'')=NEW.configuration_revision_id
          AND d.actual_configuration_revision_number=NEW.configuration_revision_number
          AND rd.release_id=NEW.release_id AND rd.phase='succeeded'
          AND rd.candidate_slot=NEW.slot
          AND ah.release_id=NEW.release_id AND ah.slot=NEW.slot
          AND ah.generation=NEW.active_generation
          AND p.strategy='generated_node' AND p.component_count IN (1,2)
          AND p.component_count=(SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=NEW.job_id)
          AND p.component_count=(SELECT COUNT(*) FROM generated_runtime_components rc WHERE rc.deployment_id=NEW.deployment_id)
          AND (
             (NEW.configuration_revision_number=0 AND NEW.configuration_revision_id='' AND NEW.configuration_format=0)
             OR EXISTS (
                SELECT 1 FROM application_configuration_revisions cr
                WHERE cr.id=NEW.configuration_revision_id AND cr.app_id=NEW.app_id
                  AND cr.revision_number=NEW.configuration_revision_number
                  AND cr.bundle_version=NEW.configuration_format
             )
          )
    )
    OR json_type(NEW.pins_json)<>'object'
    OR (SELECT COUNT(*) FROM json_each(NEW.pins_json))<>10
    OR (SELECT COUNT(DISTINCT key) FROM json_each(NEW.pins_json))<>10
    OR EXISTS (SELECT 1 FROM json_each(NEW.pins_json) WHERE key NOT IN (
        'version','deploymentId','releaseId','slot','activeGeneration','planRevision',
        'configuration','baselineDisposition','components','route'))
    OR json_type(NEW.pins_json,'$.version')<>'integer'
    OR json_type(NEW.pins_json,'$.deploymentId')<>'text'
    OR json_type(NEW.pins_json,'$.releaseId')<>'text'
    OR json_type(NEW.pins_json,'$.slot')<>'text'
    OR json_type(NEW.pins_json,'$.activeGeneration')<>'integer'
    OR json_type(NEW.pins_json,'$.baselineDisposition')<>'text'
    OR json_extract(NEW.pins_json,'$.version')<>1
    OR json_extract(NEW.pins_json,'$.deploymentId')<>NEW.deployment_id
    OR json_extract(NEW.pins_json,'$.releaseId')<>NEW.release_id
    OR json_extract(NEW.pins_json,'$.slot')<>NEW.slot
    OR json_extract(NEW.pins_json,'$.activeGeneration')<>NEW.active_generation
    OR json_extract(NEW.pins_json,'$.baselineDisposition')<>NEW.baseline_disposition
    OR json_type(NEW.pins_json,'$.planRevision')<>'object'
    OR (SELECT COUNT(*) FROM json_each(json_extract(NEW.pins_json,'$.planRevision')))<>2
    OR (SELECT COUNT(DISTINCT key) FROM json_each(json_extract(NEW.pins_json,'$.planRevision')))<>2
    OR EXISTS (SELECT 1 FROM json_each(json_extract(NEW.pins_json,'$.planRevision')) WHERE key NOT IN ('id','number'))
    OR json_type(NEW.pins_json,'$.planRevision.id')<>'text'
    OR json_type(NEW.pins_json,'$.planRevision.number')<>'integer'
    OR json_extract(NEW.pins_json,'$.planRevision.id')<>NEW.plan_revision_id
    OR json_extract(NEW.pins_json,'$.planRevision.number')<>NEW.plan_revision_number
    OR json_type(NEW.pins_json,'$.configuration')<>'object'
    OR (SELECT COUNT(*) FROM json_each(json_extract(NEW.pins_json,'$.configuration')))<>5
    OR (SELECT COUNT(DISTINCT key) FROM json_each(json_extract(NEW.pins_json,'$.configuration')))<>5
    OR EXISTS (SELECT 1 FROM json_each(json_extract(NEW.pins_json,'$.configuration')) WHERE key NOT IN (
        'revisionId','revisionNumber','formatVersion','planRevision','runtimeComponents'))
    OR json_type(NEW.pins_json,'$.configuration.revisionId')<>'text'
    OR json_type(NEW.pins_json,'$.configuration.revisionNumber')<>'integer'
    OR json_type(NEW.pins_json,'$.configuration.formatVersion')<>'integer'
    OR json_extract(NEW.pins_json,'$.configuration.revisionId')<>NEW.configuration_revision_id
    OR json_extract(NEW.pins_json,'$.configuration.revisionNumber')<>NEW.configuration_revision_number
    OR json_extract(NEW.pins_json,'$.configuration.formatVersion')<>NEW.configuration_format
    OR json_extract(NEW.pins_json,'$.configuration.planRevision.id')<>NEW.plan_revision_id
    OR json_extract(NEW.pins_json,'$.configuration.planRevision.number')<>NEW.plan_revision_number
    OR json_type(NEW.pins_json,'$.configuration.planRevision')<>'object'
    OR (SELECT COUNT(*) FROM json_each(json_extract(NEW.pins_json,'$.configuration.planRevision')))<>2
    OR (SELECT COUNT(DISTINCT key) FROM json_each(json_extract(NEW.pins_json,'$.configuration.planRevision')))<>2
    OR EXISTS (SELECT 1 FROM json_each(json_extract(NEW.pins_json,'$.configuration.planRevision')) WHERE key NOT IN ('id','number'))
    OR json_type(NEW.pins_json,'$.configuration.planRevision.id')<>'text'
    OR json_type(NEW.pins_json,'$.configuration.planRevision.number')<>'integer'
    OR json_type(NEW.pins_json,'$.configuration.runtimeComponents')<>'array'
    OR json_array_length(NEW.pins_json,'$.configuration.runtimeComponents')<>
       (SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=NEW.job_id)
    OR json_type(NEW.pins_json,'$.components')<>'array'
    OR json_array_length(NEW.pins_json,'$.components')<>
       (SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=NEW.job_id)
    OR EXISTS (
        SELECT 1 FROM json_each(NEW.pins_json,'$.components') jc
        WHERE json_type(jc.value)<>'object'
           OR (SELECT COUNT(*) FROM json_each(jc.value))<>14
           OR (SELECT COUNT(DISTINCT key) FROM json_each(jc.value))<>14
           OR EXISTS (SELECT 1 FROM json_each(jc.value) WHERE key NOT IN (
              'name','role','technology','containerId','containerName','artifactId','imageContentId',
              'buildDefinitionDigest','networkName','networkAlias','internalPort','workingDirectory',
              'runCommandDigest','limits'))
           OR json_type(jc.value,'$.name')<>'text'
           OR json_type(jc.value,'$.role')<>'text'
           OR json_type(jc.value,'$.technology')<>'text'
           OR json_type(jc.value,'$.containerId')<>'text'
           OR json_type(jc.value,'$.containerName')<>'text'
           OR json_type(jc.value,'$.artifactId')<>'text'
           OR json_type(jc.value,'$.imageContentId')<>'text'
           OR json_type(jc.value,'$.buildDefinitionDigest')<>'text'
           OR json_type(jc.value,'$.networkName')<>'text'
           OR json_type(jc.value,'$.networkAlias')<>'text'
           OR json_type(jc.value,'$.internalPort')<>'integer'
           OR json_type(jc.value,'$.workingDirectory')<>'text'
           OR json_type(jc.value,'$.runCommandDigest')<>'text'
           OR length(json_extract(jc.value,'$.name')) NOT BETWEEN 1 AND 256
           OR length(json_extract(jc.value,'$.role')) NOT BETWEEN 1 AND 64
           OR length(json_extract(jc.value,'$.technology')) NOT BETWEEN 1 AND 64
           OR length(json_extract(jc.value,'$.networkName')) NOT BETWEEN 1 AND 128
           OR length(json_extract(jc.value,'$.networkAlias')) NOT BETWEEN 1 AND 128
           OR json_extract(jc.value,'$.internalPort') NOT BETWEEN 1 AND 65535
           OR length(json_extract(jc.value,'$.workingDirectory'))>1024
           OR length(json_extract(jc.value,'$.runCommandDigest'))<>64
           OR json_extract(jc.value,'$.runCommandDigest') GLOB '*[^0-9a-f]*'
           OR NOT EXISTS (
              SELECT 1
              FROM application_lifecycle_operation_components oc
              JOIN generated_runtime_components rc
                ON rc.deployment_id=NEW.deployment_id AND rc.component_name=oc.component_name
              JOIN generated_image_artifacts a ON a.id=rc.image_artifact_id
              WHERE oc.job_id=NEW.job_id AND oc.ordinal=CAST(jc.key AS INTEGER)
                AND oc.component_name=json_extract(jc.value,'$.name')
                AND rc.slot=NEW.slot AND rc.container_id=json_extract(jc.value,'$.containerId')
                AND rc.container_name=json_extract(jc.value,'$.containerName')
                AND rc.image_artifact_id=json_extract(jc.value,'$.artifactId')
                AND a.image_content_id=json_extract(jc.value,'$.imageContentId')
                AND a.build_definition_digest=json_extract(jc.value,'$.buildDefinitionDigest')
                AND a.release_id=NEW.release_id AND a.component_id=oc.component_name
                AND a.state='ready'
           )
           OR json_type(jc.value,'$.limits')<>'object'
           OR (SELECT COUNT(*) FROM json_each(json_extract(jc.value,'$.limits')))<>10
           OR (SELECT COUNT(DISTINCT key) FROM json_each(json_extract(jc.value,'$.limits')))<>10
           OR EXISTS (SELECT 1 FROM json_each(json_extract(jc.value,'$.limits')) WHERE key NOT IN (
              'memoryBytes','memorySwapBytes','cpuQuota','cpuPeriod','pidsLimit','tmpfsBytes',
              'logDriver','logMaxSize','logMaxFiles','hardeningPolicyVersion'))
           OR json_type(jc.value,'$.limits.memoryBytes')<>'integer'
           OR json_type(jc.value,'$.limits.memorySwapBytes')<>'integer'
           OR json_type(jc.value,'$.limits.cpuQuota')<>'integer'
           OR json_type(jc.value,'$.limits.cpuPeriod')<>'integer'
           OR json_type(jc.value,'$.limits.pidsLimit')<>'integer'
           OR json_type(jc.value,'$.limits.tmpfsBytes')<>'integer'
           OR json_type(jc.value,'$.limits.logDriver')<>'text'
           OR json_type(jc.value,'$.limits.logMaxSize')<>'text'
           OR json_type(jc.value,'$.limits.logMaxFiles')<>'integer'
           OR json_type(jc.value,'$.limits.hardeningPolicyVersion')<>'text'
           OR json_extract(jc.value,'$.limits.memoryBytes')<1
           OR json_extract(jc.value,'$.limits.memorySwapBytes')<json_extract(jc.value,'$.limits.memoryBytes')
           OR json_extract(jc.value,'$.limits.cpuQuota')<1
           OR json_extract(jc.value,'$.limits.cpuPeriod')<1
           OR json_extract(jc.value,'$.limits.pidsLimit')<1
           OR json_extract(jc.value,'$.limits.tmpfsBytes')<0
           OR length(json_extract(jc.value,'$.limits.logDriver')) NOT BETWEEN 1 AND 64
           OR length(json_extract(jc.value,'$.limits.logMaxSize')) NOT BETWEEN 1 AND 64
           OR json_extract(jc.value,'$.limits.logMaxFiles')<1
           OR length(json_extract(jc.value,'$.limits.hardeningPolicyVersion')) NOT BETWEEN 1 AND 128
    )
    OR EXISTS (
        SELECT 1 FROM application_lifecycle_operation_components oc
        WHERE oc.job_id=NEW.job_id AND (
          json_extract(NEW.pins_json,'$.configuration.runtimeComponents['||oc.ordinal||']') IS NOT oc.component_name
          OR json_extract(NEW.pins_json,'$.components['||oc.ordinal||'].name') IS NOT oc.component_name
        )
    )
    OR EXISTS (
        SELECT 1 FROM application_lifecycle_operation_components first
        JOIN application_lifecycle_operation_components second
          ON second.job_id=first.job_id AND second.ordinal>first.ordinal
        WHERE first.job_id=NEW.job_id AND first.component_name>=second.component_name
    )
    OR json_type(NEW.pins_json,'$.route')<>'object'
    OR (SELECT COUNT(*) FROM json_each(json_extract(NEW.pins_json,'$.route')))<>6
    OR (SELECT COUNT(DISTINCT key) FROM json_each(json_extract(NEW.pins_json,'$.route')))<>6
    OR EXISTS (SELECT 1 FROM json_each(json_extract(NEW.pins_json,'$.route')) WHERE key NOT IN (
       'selectedLineageKind','selectedLineageGeneration','selectedLineageOperationId',
       'selectedLineageTerminalDigest','protectedFormatVersion','targetRecordDigest'))
    OR json_type(NEW.pins_json,'$.route.selectedLineageKind')<>'text'
    OR json_type(NEW.pins_json,'$.route.selectedLineageGeneration')<>'integer'
    OR json_type(NEW.pins_json,'$.route.selectedLineageOperationId')<>'text'
    OR json_type(NEW.pins_json,'$.route.selectedLineageTerminalDigest')<>'text'
    OR json_type(NEW.pins_json,'$.route.protectedFormatVersion')<>'integer'
    OR json_type(NEW.pins_json,'$.route.targetRecordDigest')<>'text'
    OR json_extract(NEW.pins_json,'$.route.selectedLineageKind') NOT IN ('gateway_upgrade','gateway_rebind')
    OR json_extract(NEW.pins_json,'$.route.selectedLineageGeneration')<1
    OR length(json_extract(NEW.pins_json,'$.route.selectedLineageOperationId'))<1
    OR length(json_extract(NEW.pins_json,'$.route.selectedLineageTerminalDigest'))<>64
    OR json_extract(NEW.pins_json,'$.route.selectedLineageTerminalDigest') GLOB '*[^0-9a-f]*'
    OR json_extract(NEW.pins_json,'$.route.protectedFormatVersion')<1
    OR length(json_extract(NEW.pins_json,'$.route.targetRecordDigest'))<>64
    OR json_extract(NEW.pins_json,'$.route.targetRecordDigest') GLOB '*[^0-9a-f]*'
BEGIN SELECT RAISE(ABORT, 'invalid application lifecycle operation'); END;

CREATE TRIGGER application_lifecycle_operation_immutable
BEFORE UPDATE ON application_lifecycle_operations
BEGIN SELECT RAISE(ABORT, 'application lifecycle operations are immutable'); END;
CREATE TRIGGER application_lifecycle_operation_retain
BEFORE DELETE ON application_lifecycle_operations
BEGIN SELECT RAISE(ABORT, 'application lifecycle operations are retained'); END;
CREATE TRIGGER application_lifecycle_operation_component_immutable
BEFORE UPDATE ON application_lifecycle_operation_components
BEGIN SELECT RAISE(ABORT, 'application lifecycle operation components are immutable'); END;
CREATE TRIGGER application_lifecycle_operation_component_retain
BEFORE DELETE ON application_lifecycle_operation_components
BEGIN SELECT RAISE(ABORT, 'application lifecycle operation components are retained'); END;

CREATE TABLE application_lifecycle_effect_intents (
    job_id TEXT NOT NULL REFERENCES application_lifecycle_operations(job_id),
    boundary TEXT NOT NULL CHECK (boundary IN ('withdraw','stop','start','health','publish','complete')),
    component_name TEXT NOT NULL,
    writer_attempt INTEGER NOT NULL CHECK (writer_attempt > 0),
    request_version INTEGER NOT NULL CHECK (request_version=1),
    request_json TEXT NOT NULL CHECK (length(request_json) BETWEEN 2 AND 65536 AND json_valid(request_json)),
    request_digest TEXT NOT NULL CHECK (length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    created_at TEXT NOT NULL CHECK (length(created_at)>0),
    PRIMARY KEY(job_id,boundary,component_name)
);

CREATE TABLE application_lifecycle_effect_receipts (
    job_id TEXT NOT NULL,
    boundary TEXT NOT NULL,
    component_name TEXT NOT NULL,
    writer_attempt INTEGER NOT NULL CHECK (writer_attempt > 0),
    outcome TEXT NOT NULL CHECK (outcome IN (
      'routes_absent','container_stopped','container_running','healthy','serving','complete_stopped','complete_running')),
    proof_version INTEGER NOT NULL CHECK (proof_version=1),
    proof_json TEXT NOT NULL CHECK (length(proof_json) BETWEEN 2 AND 65536 AND json_valid(proof_json)),
    proof_digest TEXT NOT NULL CHECK (length(proof_digest)=64 AND proof_digest NOT GLOB '*[^0-9a-f]*'),
    observed_at TEXT NOT NULL CHECK (length(observed_at)>0),
    PRIMARY KEY(job_id,boundary,component_name),
    FOREIGN KEY(job_id,boundary,component_name)
      REFERENCES application_lifecycle_effect_intents(job_id,boundary,component_name)
);

CREATE TRIGGER application_lifecycle_effect_intent_valid_insert
BEFORE INSERT ON application_lifecycle_effect_intents
WHEN EXISTS (SELECT 1 FROM application_lifecycle_effect_intents i
             WHERE i.job_id=NEW.job_id AND i.boundary=NEW.boundary AND i.component_name=NEW.component_name)
 OR NOT EXISTS (
    SELECT 1 FROM application_lifecycle_operations o
    JOIN jobs j ON j.id=o.job_id
    JOIN application_desired_lifecycle_heads h
      ON h.app_id=o.app_id AND h.revision_number=o.desired_revision
    WHERE o.job_id=NEW.job_id AND j.status='running' AND j.attempt=NEW.writer_attempt
      AND j.type=o.action AND j.resource_type='application' AND j.resource_id=o.app_id
      AND j.requested_by=o.actor_id
 )
 OR json_type(NEW.request_json)<>'object'
 OR (SELECT COUNT(*) FROM json_each(NEW.request_json))<>8
 OR (SELECT COUNT(DISTINCT key) FROM json_each(NEW.request_json))<>8
 OR EXISTS (SELECT 1 FROM json_each(NEW.request_json) WHERE key NOT IN (
      'version','operationId','applicationId','desiredRevision','boundary','componentName','mode','pinsDigest'))
 OR json_type(NEW.request_json,'$.version')<>'integer'
 OR json_type(NEW.request_json,'$.operationId')<>'text'
 OR json_type(NEW.request_json,'$.applicationId')<>'text'
 OR json_type(NEW.request_json,'$.desiredRevision')<>'integer'
 OR json_type(NEW.request_json,'$.boundary')<>'text'
 OR json_type(NEW.request_json,'$.componentName')<>'text'
 OR json_type(NEW.request_json,'$.mode')<>'text'
 OR json_type(NEW.request_json,'$.pinsDigest')<>'text'
 OR NOT EXISTS (
    SELECT 1 FROM application_lifecycle_operations o
    WHERE o.job_id=NEW.job_id
      AND json_extract(NEW.request_json,'$.version')=1
      AND json_extract(NEW.request_json,'$.operationId')=o.job_id
      AND json_extract(NEW.request_json,'$.applicationId')=o.app_id
      AND json_extract(NEW.request_json,'$.desiredRevision')=o.desired_revision
      AND json_extract(NEW.request_json,'$.boundary')=NEW.boundary
      AND json_extract(NEW.request_json,'$.componentName')=NEW.component_name
      AND json_extract(NEW.request_json,'$.pinsDigest')=o.pins_digest
      AND json_extract(NEW.request_json,'$.mode') IN ('command','observation_only')
      AND (
        (NEW.boundary='withdraw' AND NEW.component_name='' AND NOT (o.action='start' AND o.baseline_disposition='running'))
        OR (NEW.boundary='stop' AND o.action IN ('stop','restart')
            AND NOT (o.action='restart' AND o.baseline_disposition='stopped')
            AND EXISTS (SELECT 1 FROM application_lifecycle_operation_components c
                        WHERE c.job_id=o.job_id AND c.component_name=NEW.component_name))
        OR (NEW.boundary='start' AND o.action IN ('start','restart')
            AND NOT (o.action='start' AND o.baseline_disposition='running')
            AND EXISTS (SELECT 1 FROM application_lifecycle_operation_components c
                        WHERE c.job_id=o.job_id AND c.component_name=NEW.component_name))
        OR (NEW.boundary IN ('health','publish') AND NEW.component_name='' AND o.action IN ('start','restart'))
        OR (NEW.boundary='complete' AND NEW.component_name='')
      )
      AND json_extract(NEW.request_json,'$.mode') = CASE
        WHEN NEW.boundary IN ('health','complete') THEN 'observation_only'
        WHEN NEW.boundary='withdraw' AND o.baseline_disposition='stopped' THEN 'observation_only'
        WHEN NEW.boundary='stop' AND o.baseline_disposition='stopped' THEN 'observation_only'
        ELSE 'command' END
      AND (NEW.boundary<>'stop' OR EXISTS (
          SELECT 1 FROM application_lifecycle_effect_receipts r
          WHERE r.job_id=o.job_id AND r.boundary='withdraw' AND r.component_name='' AND r.outcome='routes_absent'))
      AND (NEW.boundary<>'start' OR (
          EXISTS (SELECT 1 FROM application_lifecycle_effect_receipts r
                  WHERE r.job_id=o.job_id AND r.boundary='withdraw' AND r.component_name='' AND r.outcome='routes_absent')
          AND (o.action<>'restart' OR o.baseline_disposition<>'running' OR
               (SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=o.job_id)=
               (SELECT COUNT(*) FROM application_lifecycle_effect_receipts r
                WHERE r.job_id=o.job_id AND r.boundary='stop' AND r.outcome='container_stopped'))))
      AND (NEW.boundary<>'health' OR (o.action='start' AND o.baseline_disposition='running') OR
           (SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=o.job_id)=
           (SELECT COUNT(*) FROM application_lifecycle_effect_receipts r
            WHERE r.job_id=o.job_id AND r.boundary='start' AND r.outcome='container_running'))
      AND (NEW.boundary<>'publish' OR EXISTS (
          SELECT 1 FROM application_lifecycle_effect_receipts r
          WHERE r.job_id=o.job_id AND r.boundary='health' AND r.component_name='' AND r.outcome='healthy'))
      AND (NEW.boundary<>'complete' OR (
          (o.action='stop' AND EXISTS (SELECT 1 FROM application_lifecycle_effect_receipts r
                                      WHERE r.job_id=o.job_id AND r.boundary='withdraw' AND r.component_name='' AND r.outcome='routes_absent')
           AND (SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=o.job_id)=
               (SELECT COUNT(*) FROM application_lifecycle_effect_receipts r WHERE r.job_id=o.job_id AND r.boundary='stop' AND r.outcome='container_stopped'))
          OR (o.action IN ('start','restart') AND EXISTS (
              SELECT 1 FROM application_lifecycle_effect_receipts r
              WHERE r.job_id=o.job_id AND r.boundary='publish' AND r.component_name='' AND r.outcome='serving'))))
 )
BEGIN SELECT RAISE(ABORT, 'invalid application lifecycle effect intent'); END;

CREATE TRIGGER application_lifecycle_effect_receipt_valid_insert
BEFORE INSERT ON application_lifecycle_effect_receipts
WHEN EXISTS (SELECT 1 FROM application_lifecycle_effect_receipts r
             WHERE r.job_id=NEW.job_id AND r.boundary=NEW.boundary AND r.component_name=NEW.component_name)
 OR NOT EXISTS (
    SELECT 1 FROM application_lifecycle_effect_intents i
    JOIN application_lifecycle_operations o ON o.job_id=i.job_id
    JOIN jobs j ON j.id=o.job_id
    JOIN application_desired_lifecycle_heads h
      ON h.app_id=o.app_id AND h.revision_number=o.desired_revision
    WHERE i.job_id=NEW.job_id AND i.boundary=NEW.boundary AND i.component_name=NEW.component_name
      AND j.status='running' AND j.attempt=NEW.writer_attempt
      AND j.type=o.action AND j.resource_type='application' AND j.resource_id=o.app_id AND j.requested_by=o.actor_id
 )
 OR json_type(NEW.proof_json)<>'object'
 OR (SELECT COUNT(*) FROM json_each(NEW.proof_json))<>10
 OR (SELECT COUNT(DISTINCT key) FROM json_each(NEW.proof_json))<>10
 OR EXISTS (SELECT 1 FROM json_each(NEW.proof_json) WHERE key NOT IN (
      'version','operationId','applicationId','desiredRevision','boundary','componentName',
      'outcome','intentDigest','evidenceDigest','pinsDigest'))
 OR json_type(NEW.proof_json,'$.version')<>'integer'
 OR json_type(NEW.proof_json,'$.operationId')<>'text'
 OR json_type(NEW.proof_json,'$.applicationId')<>'text'
 OR json_type(NEW.proof_json,'$.desiredRevision')<>'integer'
 OR json_type(NEW.proof_json,'$.boundary')<>'text'
 OR json_type(NEW.proof_json,'$.componentName')<>'text'
 OR json_type(NEW.proof_json,'$.outcome')<>'text'
 OR json_type(NEW.proof_json,'$.intentDigest')<>'text'
 OR json_type(NEW.proof_json,'$.evidenceDigest')<>'text'
 OR json_type(NEW.proof_json,'$.pinsDigest')<>'text'
 OR NOT EXISTS (
    SELECT 1 FROM application_lifecycle_effect_intents i
    JOIN application_lifecycle_operations o ON o.job_id=i.job_id
    WHERE i.job_id=NEW.job_id AND i.boundary=NEW.boundary AND i.component_name=NEW.component_name
      AND json_extract(NEW.proof_json,'$.version')=1
      AND json_extract(NEW.proof_json,'$.operationId')=o.job_id
      AND json_extract(NEW.proof_json,'$.applicationId')=o.app_id
      AND json_extract(NEW.proof_json,'$.desiredRevision')=o.desired_revision
      AND json_extract(NEW.proof_json,'$.boundary')=NEW.boundary
      AND json_extract(NEW.proof_json,'$.componentName')=NEW.component_name
      AND json_extract(NEW.proof_json,'$.outcome')=NEW.outcome
      AND json_extract(NEW.proof_json,'$.intentDigest')=i.request_digest
      AND json_extract(NEW.proof_json,'$.pinsDigest')=o.pins_digest
      AND length(json_extract(NEW.proof_json,'$.evidenceDigest'))=64
      AND json_extract(NEW.proof_json,'$.evidenceDigest') NOT GLOB '*[^0-9a-f]*'
      AND NEW.outcome=CASE NEW.boundary
          WHEN 'withdraw' THEN 'routes_absent'
          WHEN 'stop' THEN 'container_stopped'
          WHEN 'start' THEN 'container_running'
          WHEN 'health' THEN 'healthy'
          WHEN 'publish' THEN 'serving'
          WHEN 'complete' THEN CASE WHEN o.action='stop' THEN 'complete_stopped' ELSE 'complete_running' END
      END
      AND (NEW.boundary<>'complete' OR (
          (o.action='stop' AND EXISTS (SELECT 1 FROM application_lifecycle_effect_receipts r
                                      WHERE r.job_id=o.job_id AND r.boundary='withdraw' AND r.component_name='' AND r.outcome='routes_absent')
           AND (SELECT COUNT(*) FROM application_lifecycle_operation_components c WHERE c.job_id=o.job_id)=
               (SELECT COUNT(*) FROM application_lifecycle_effect_receipts r WHERE r.job_id=o.job_id AND r.boundary='stop' AND r.outcome='container_stopped'))
          OR (o.action IN ('start','restart') AND EXISTS (
              SELECT 1 FROM application_lifecycle_effect_receipts r
              WHERE r.job_id=o.job_id AND r.boundary='publish' AND r.component_name='' AND r.outcome='serving'))))
 )
BEGIN SELECT RAISE(ABORT, 'invalid application lifecycle effect receipt'); END;

CREATE TRIGGER application_lifecycle_effect_intent_immutable BEFORE UPDATE ON application_lifecycle_effect_intents
BEGIN SELECT RAISE(ABORT, 'application lifecycle effect intents are immutable'); END;
CREATE TRIGGER application_lifecycle_effect_intent_retain BEFORE DELETE ON application_lifecycle_effect_intents
BEGIN SELECT RAISE(ABORT, 'application lifecycle effect intents are retained'); END;
CREATE TRIGGER application_lifecycle_effect_receipt_immutable BEFORE UPDATE ON application_lifecycle_effect_receipts
BEGIN SELECT RAISE(ABORT, 'application lifecycle effect receipts are immutable'); END;
CREATE TRIGGER application_lifecycle_effect_receipt_retain BEFORE DELETE ON application_lifecycle_effect_receipts
BEGIN SELECT RAISE(ABORT, 'application lifecycle effect receipts are retained'); END;

CREATE INDEX application_lifecycle_effect_intents_pending_start
ON application_lifecycle_effect_intents(boundary,job_id,component_name)
WHERE boundary='start';
