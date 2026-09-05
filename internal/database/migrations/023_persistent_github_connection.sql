CREATE TABLE github_default_connections (
    owner_user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL UNIQUE REFERENCES source_connections(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE github_connection_authorizations (
    id TEXT PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL REFERENCES source_connections(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('pending','connected','denied','expired','superseded','failed')),
    credential_generation INTEGER NOT NULL CHECK (credential_generation >= 0),
    pending_expires_at TEXT NOT NULL,
    poll_interval_seconds INTEGER NOT NULL CHECK (poll_interval_seconds BETWEEN 1 AND 300),
    next_poll_at TEXT NOT NULL,
    last_error_code TEXT CHECK (last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 64),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX github_connection_authorizations_pending
    ON github_connection_authorizations(connection_id) WHERE status = 'pending';
CREATE INDEX github_connection_authorizations_owner
    ON github_connection_authorizations(owner_user_id, connection_id, created_at DESC);

CREATE TRIGGER github_default_connection_owner_insert BEFORE INSERT ON github_default_connections
WHEN NOT EXISTS (
    SELECT 1 FROM source_connections c
    WHERE c.id = NEW.connection_id AND c.owner_user_id = NEW.owner_user_id AND c.provider = 'github'
)
BEGIN SELECT RAISE(ABORT, 'github default connection owner mismatch'); END;

CREATE TRIGGER github_default_connection_owner_update BEFORE UPDATE ON github_default_connections
WHEN OLD.owner_user_id <> NEW.owner_user_id OR OLD.connection_id <> NEW.connection_id
BEGIN SELECT RAISE(ABORT, 'github default connection mapping is immutable'); END;

CREATE TRIGGER github_connection_authorization_owner_insert BEFORE INSERT ON github_connection_authorizations
WHEN NOT EXISTS (
    SELECT 1 FROM github_default_connections d
    WHERE d.owner_user_id = NEW.owner_user_id AND d.connection_id = NEW.connection_id
)
BEGIN SELECT RAISE(ABORT, 'github authorization owner mismatch'); END;

CREATE TRIGGER github_connection_authorization_identity_update BEFORE UPDATE ON github_connection_authorizations
WHEN OLD.owner_user_id <> NEW.owner_user_id OR OLD.connection_id <> NEW.connection_id OR OLD.credential_generation <> NEW.credential_generation
BEGIN SELECT RAISE(ABORT, 'github authorization identity is immutable'); END;

INSERT INTO github_default_connections(owner_user_id, connection_id, created_at, updated_at)
SELECT owner_user_id, id, created_at, updated_at
FROM (
    SELECT c.*,
           ROW_NUMBER() OVER (
               PARTITION BY c.owner_user_id
               ORDER BY c.connected_at DESC, c.updated_at DESC, c.id DESC
           ) AS preference
    FROM source_connections c
    WHERE c.provider = 'github'
      AND c.status IN ('connected','access_lost')
      AND c.provider_user_id IS NOT NULL
)
WHERE preference = 1;
