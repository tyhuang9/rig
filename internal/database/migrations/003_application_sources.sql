CREATE TABLE application_sources (
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
        (source_type = 'github' AND connection_id IS NOT NULL AND installation_id > 0 AND repository_id > 0 AND length(repository_owner) BETWEEN 1 AND 255 AND length(repository_name) BETWEEN 1 AND 255 AND length(tracked_branch) BETWEEN 1 AND 255 AND tracked_ref = 'refs/heads/' || tracked_branch AND length(compose_path) BETWEEN 1 AND 1024 AND compose_path NOT LIKE '/%' AND compose_path NOT LIKE '\\%' AND instr(compose_path, '\\') = 0 AND instr(compose_path, ':') = 0 AND compose_path NOT LIKE '../%' AND compose_path NOT LIKE '%/../%' AND length(resolved_sha) = 40 AND resolved_sha NOT GLOB '*[^0-9a-f]*')
    )
);

CREATE INDEX application_sources_connection ON application_sources(connection_id, installation_id, repository_id);

INSERT INTO application_sources(application_id, source_type, created_at, updated_at)
SELECT id, 'local', created_at, updated_at FROM applications;
