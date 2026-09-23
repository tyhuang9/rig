DROP TRIGGER IF EXISTS github_auto_deploy_source_locked;
DROP TRIGGER IF EXISTS github_auto_deploy_source_delete_locked;
DROP TRIGGER IF EXISTS github_auto_deploy_config_validate_update;

CREATE TABLE application_sources_v2 (
    application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('local','github')),
    connection_id TEXT REFERENCES source_connections(id),
    installation_id INTEGER,
    repository_id INTEGER,
    repository_owner TEXT,
    repository_name TEXT,
    tracked_branch TEXT,
    tracked_ref TEXT,
    compose_path TEXT,
    resolved_sha TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (source_type = 'local' AND connection_id IS NULL AND installation_id IS NULL AND repository_id IS NULL AND repository_owner IS NULL AND repository_name IS NULL AND tracked_branch IS NULL AND tracked_ref IS NULL AND compose_path IS NULL AND resolved_sha IS NULL)
        OR
        (
            source_type = 'github'
            AND connection_id IS NOT NULL
            AND installation_id > 0
            AND repository_id > 0
            AND length(repository_owner) BETWEEN 1 AND 255
            AND length(repository_name) BETWEEN 1 AND 255
            AND length(tracked_branch) BETWEEN 1 AND 255
            AND tracked_ref = 'refs/heads/' || tracked_branch
            AND (
                compose_path IS NULL
                OR (
                    length(compose_path) BETWEEN 1 AND 1024
                    AND compose_path NOT LIKE '/%'
                    AND compose_path NOT LIKE '\%'
                    AND instr(compose_path, '\') = 0
                    AND instr(compose_path, ':') = 0
                    AND compose_path NOT LIKE '../%'
                    AND compose_path NOT LIKE '%/../%'
                )
            )
            AND length(resolved_sha) = 40
            AND resolved_sha NOT GLOB '*[^0-9a-f]*'
        )
    )
);

INSERT INTO application_sources_v2(
    application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,
    tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at
)
SELECT
    application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,
    tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at
FROM application_sources;

DROP TABLE application_sources;
ALTER TABLE application_sources_v2 RENAME TO application_sources;
CREATE INDEX application_sources_connection ON application_sources(connection_id, installation_id, repository_id);

CREATE TRIGGER github_auto_deploy_config_validate_update
BEFORE UPDATE ON github_auto_deploy_configs
WHEN NEW.application_id<>OLD.application_id
  OR NEW.created_at<>OLD.created_at
  OR NEW.revision<>OLD.revision+1
  OR NEW.enabled=OLD.enabled
  OR NEW.updated_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'
  OR NOT EXISTS (SELECT 1 FROM users u WHERE u.id=NEW.configured_by_user_id AND u.role='administrator')
  OR (NEW.enabled=0 AND NEW.source_owner_user_id IS NOT OLD.source_owner_user_id)
  OR (NEW.enabled=1 AND NOT EXISTS (
      SELECT 1
      FROM applications a
      JOIN application_sources s ON s.application_id=a.id
      JOIN source_connections c ON c.id=s.connection_id AND c.owner_user_id=NEW.source_owner_user_id AND c.status='connected'
      JOIN relay_installation_bindings b ON b.binding_id=NEW.binding_id
          AND b.owner_user_id=NEW.source_owner_user_id
          AND b.connection_id=s.connection_id
          AND b.controller_id=NEW.controller_id
          AND b.installation_id=s.installation_id
          AND b.repository_id=s.repository_id
          AND b.state='authorized'
      JOIN relay_controllers rc ON rc.controller_id=NEW.controller_id AND rc.state='active'
      JOIN relay_controller_subscriptions sub ON sub.subscription_id=NEW.subscription_id
          AND sub.owner_user_id=NEW.source_owner_user_id
          AND sub.binding_id=NEW.binding_id
          AND sub.controller_id=NEW.controller_id
          AND sub.installation_id=s.installation_id
          AND sub.repository_id=s.repository_id
          AND sub.tracked_ref=s.tracked_ref
          AND sub.state='active'
      WHERE a.id=NEW.application_id AND a.archived_at IS NULL AND s.source_type='github'
          AND length(s.tracked_ref) BETWEEN 12 AND 255 AND substr(s.tracked_ref,1,11)='refs/heads/'
  ))
BEGIN SELECT RAISE(ABORT,'invalid auto-deploy configuration transition'); END;


CREATE TRIGGER github_auto_deploy_source_locked
BEFORE UPDATE OF application_id,source_type,connection_id,installation_id,repository_id,tracked_branch,tracked_ref,compose_path ON application_sources
WHEN EXISTS (SELECT 1 FROM github_auto_deploy_configs c WHERE c.application_id=OLD.application_id AND c.enabled=1)
BEGIN SELECT RAISE(ABORT,'disable auto-deploy before changing application source'); END;

CREATE TRIGGER github_auto_deploy_source_delete_locked
BEFORE DELETE ON application_sources
WHEN EXISTS (SELECT 1 FROM github_auto_deploy_configs c WHERE c.application_id=OLD.application_id AND c.enabled=1)
BEGIN SELECT RAISE(ABORT,'disable auto-deploy before deleting application source'); END;
