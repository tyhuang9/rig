CREATE TABLE source_connections (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider = 'github'),
    status TEXT NOT NULL CHECK (status IN ('pending','connected','denied','expired','disconnected','access_lost')),
    provider_user_id TEXT,
    provider_login TEXT,
    credential_generation INTEGER NOT NULL DEFAULT 0 CHECK (credential_generation >= 0),
    pending_expires_at TEXT,
    poll_interval_seconds INTEGER CHECK (poll_interval_seconds IS NULL OR poll_interval_seconds BETWEEN 1 AND 300),
    next_poll_at TEXT,
    access_expires_at TEXT,
    refresh_expires_at TEXT,
    last_error_code TEXT,
    connected_at TEXT,
    disconnected_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (status = 'pending' AND pending_expires_at IS NOT NULL AND poll_interval_seconds IS NOT NULL AND next_poll_at IS NOT NULL AND provider_user_id IS NULL AND provider_login IS NULL AND credential_generation = 0 AND access_expires_at IS NULL AND refresh_expires_at IS NULL AND connected_at IS NULL)
        OR (status = 'connected' AND pending_expires_at IS NULL AND poll_interval_seconds IS NULL AND next_poll_at IS NULL AND provider_user_id IS NOT NULL AND provider_login IS NOT NULL AND credential_generation > 0 AND access_expires_at IS NOT NULL AND refresh_expires_at IS NOT NULL AND connected_at IS NOT NULL)
        OR (status NOT IN ('pending','connected') AND pending_expires_at IS NULL AND poll_interval_seconds IS NULL AND next_poll_at IS NULL)
    ),
    CHECK (provider_user_id IS NULL OR length(provider_user_id) BETWEEN 1 AND 128),
    CHECK (provider_login IS NULL OR length(provider_login) BETWEEN 1 AND 255),
    CHECK (last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 64)
);
CREATE INDEX source_connections_owner ON source_connections(owner_user_id, created_at, id);
CREATE UNIQUE INDEX source_connections_active_identity ON source_connections(owner_user_id, provider, provider_user_id)
    WHERE provider_user_id IS NOT NULL AND status IN ('connected','access_lost');

CREATE TABLE github_installations (
    connection_id TEXT NOT NULL REFERENCES source_connections(id) ON DELETE CASCADE,
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    account_login TEXT NOT NULL CHECK (length(account_login) BETWEEN 1 AND 255),
    account_type TEXT NOT NULL CHECK (account_type IN ('User','Organization','Enterprise','Bot')),
    target_type TEXT NOT NULL CHECK (target_type IN ('User','Organization')),
    repository_selection TEXT NOT NULL CHECK (repository_selection IN ('all','selected')),
    suspended_at TEXT,
    cached_at TEXT NOT NULL,
    PRIMARY KEY (connection_id, installation_id)
);
CREATE INDEX github_installations_connection_cache ON github_installations(connection_id, cached_at, installation_id);
