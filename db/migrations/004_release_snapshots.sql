ALTER TABLE releases ADD COLUMN source_provider TEXT;
ALTER TABLE releases ADD COLUMN repository_id INTEGER;
ALTER TABLE releases ADD COLUMN repository_owner TEXT;
ALTER TABLE releases ADD COLUMN repository_name TEXT;
ALTER TABLE releases ADD COLUMN tracked_ref TEXT;
ALTER TABLE releases ADD COLUMN resolved_sha TEXT;
ALTER TABLE releases ADD COLUMN compose_path TEXT;
ALTER TABLE releases ADD COLUMN archive_sha256 TEXT;
ALTER TABLE releases ADD COLUMN workspace_path TEXT;
ALTER TABLE releases ADD COLUMN workspace_state TEXT;
ALTER TABLE releases ADD COLUMN materialization_error_code TEXT;
ALTER TABLE releases ADD COLUMN materialized_at TEXT;

CREATE INDEX releases_snapshot_lookup ON releases(app_id, repository_id, resolved_sha, compose_path, workspace_state, created_at DESC);
CREATE UNIQUE INDEX releases_ready_snapshot ON releases(app_id, repository_id, resolved_sha, compose_path)
    WHERE workspace_state = 'ready';
