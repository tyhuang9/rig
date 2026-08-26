CREATE TABLE relay_controllers (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    controller_id TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL CHECK (state IN ('active','revoked')),
    last_error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    revoked_at TEXT,
    CHECK (length(controller_id) = 36 AND lower(controller_id) = controller_id AND controller_id GLOB '????????-????-????-????-????????????' AND controller_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (last_error_code IS NULL OR last_error_code IN ('authorization_denied','authorization_expired','enrollment_failed','key_revoked','protocol_error','provider_unavailable','relay_unavailable','removal_failed','rotation_failed','source_access_lost')),
    CHECK ((state = 'active' AND revoked_at IS NULL) OR (state = 'revoked' AND revoked_at IS NOT NULL))
);

CREATE TABLE relay_controller_keys (
    key_id TEXT PRIMARY KEY,
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    public_key BLOB NOT NULL,
    algorithm TEXT NOT NULL CHECK (algorithm = 'ed25519'),
    state TEXT NOT NULL CHECK (state IN ('pending','active','revoked')),
    protected_key_ref TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    activated_at TEXT,
    possession_confirmed_at TEXT,
    revoked_at TEXT,
    CHECK (length(key_id) = 36 AND lower(key_id) = key_id AND key_id GLOB '????????-????-????-????-????????????' AND key_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (typeof(public_key) = 'blob' AND length(public_key) = 32),
    CHECK (protected_key_ref = 'relay/controllers/' || controller_id || '/keys/' || key_id || '.key'),
    CHECK ((state = 'pending' AND activated_at IS NULL AND possession_confirmed_at IS NULL AND revoked_at IS NULL)
        OR (state = 'active' AND activated_at IS NOT NULL AND possession_confirmed_at IS NOT NULL AND revoked_at IS NULL)
        OR (state = 'revoked' AND revoked_at IS NOT NULL)),
    UNIQUE (controller_id, key_id)
);
CREATE UNIQUE INDEX relay_controller_one_active_key ON relay_controller_keys(controller_id) WHERE state = 'active';
CREATE UNIQUE INDEX relay_controller_one_pending_key ON relay_controller_keys(controller_id) WHERE state = 'pending';
CREATE INDEX relay_controller_key_history ON relay_controller_keys(controller_id, created_at DESC, key_id DESC);

CREATE TABLE relay_key_rotations (
    rotation_id TEXT PRIMARY KEY,
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    old_key_id TEXT NOT NULL,
    new_key_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepare','propose','confirm','new_key_auth','finalize','completed','failed')),
    expires_at TEXT NOT NULL,
    state_changed_at TEXT NOT NULL,
    last_error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    CHECK (length(rotation_id) = 36 AND lower(rotation_id) = rotation_id AND rotation_id GLOB '????????-????-????-????-????????????' AND rotation_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (old_key_id <> new_key_id),
    CHECK (last_error_code IS NULL OR last_error_code IN ('authorization_denied','authorization_expired','enrollment_failed','key_revoked','protocol_error','provider_unavailable','relay_unavailable','removal_failed','rotation_failed','source_access_lost')),
    CHECK ((state IN ('prepare','propose','confirm','new_key_auth','finalize') AND completed_at IS NULL AND last_error_code IS NULL)
        OR (state = 'completed' AND completed_at IS NOT NULL AND last_error_code IS NULL)
        OR (state = 'failed' AND completed_at IS NOT NULL AND last_error_code IS NOT NULL)),
    FOREIGN KEY (controller_id, old_key_id) REFERENCES relay_controller_keys(controller_id, key_id),
    FOREIGN KEY (controller_id, new_key_id) REFERENCES relay_controller_keys(controller_id, key_id)
);
CREATE UNIQUE INDEX relay_controller_one_live_rotation ON relay_key_rotations(controller_id)
    WHERE state IN ('prepare','propose','confirm','new_key_auth','finalize');
CREATE INDEX relay_key_rotation_history ON relay_key_rotations(controller_id, created_at DESC, rotation_id DESC);

CREATE TABLE relay_installation_bindings (
    binding_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    connection_id TEXT NOT NULL REFERENCES source_connections(id),
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL CHECK (repository_id > 0),
    state TEXT NOT NULL CHECK (state IN ('pending','authorized','denied','expired','removal_pending','removed','access_lost','failed')),
    state_changed_at TEXT NOT NULL,
    last_error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    CHECK (length(binding_id) = 36 AND lower(binding_id) = binding_id AND binding_id GLOB '????????-????-????-????-????????????' AND binding_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (last_error_code IS NULL OR last_error_code IN ('authorization_denied','authorization_expired','enrollment_failed','key_revoked','protocol_error','provider_unavailable','relay_unavailable','removal_failed','rotation_failed','source_access_lost')),
    CHECK ((state IN ('pending','authorized','removal_pending','access_lost') AND completed_at IS NULL)
        OR (state IN ('denied','expired','removed') AND completed_at IS NOT NULL AND last_error_code IS NULL)
        OR (state = 'failed' AND completed_at IS NOT NULL AND last_error_code IS NOT NULL)),
    CHECK ((state = 'access_lost' AND last_error_code IS NOT NULL) OR state <> 'access_lost'),
    CHECK ((state IN ('pending','authorized','removal_pending') AND last_error_code IS NULL) OR state NOT IN ('pending','authorized','removal_pending')),
    UNIQUE (binding_id, owner_user_id)
);
CREATE UNIQUE INDEX relay_binding_one_live_identity ON relay_installation_bindings(controller_id, connection_id, installation_id, repository_id)
    WHERE state IN ('pending','authorized','removal_pending','access_lost');
CREATE INDEX relay_binding_owner_history ON relay_installation_bindings(owner_user_id, updated_at DESC, binding_id DESC);
CREATE INDEX relay_binding_connection ON relay_installation_bindings(connection_id, state, updated_at DESC);

CREATE TABLE relay_enrollments (
    enrollment_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    connection_id TEXT NOT NULL REFERENCES source_connections(id),
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    key_id TEXT NOT NULL,
    binding_id TEXT,
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL CHECK (repository_id > 0),
    purpose TEXT NOT NULL CHECK (purpose = 'controller-relay-enrollment-poll'),
    protected_poll_ref TEXT,
    state TEXT NOT NULL CHECK (state IN ('pending','authorized','denied','expired','failed')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    state_changed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_polled_at TEXT,
    completed_at TEXT,
    poll_ref_cleared_at TEXT,
    last_error_code TEXT,
    CHECK (length(enrollment_id) = 36 AND lower(enrollment_id) = enrollment_id AND enrollment_id GLOB '????????-????-????-????-????????????' AND enrollment_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (protected_poll_ref IS NULL OR protected_poll_ref = 'relay/controllers/' || controller_id || '/enrollments/' || enrollment_id || '/poll'),
    CHECK ((protected_poll_ref IS NOT NULL AND poll_ref_cleared_at IS NULL) OR (protected_poll_ref IS NULL AND poll_ref_cleared_at IS NOT NULL)),
    CHECK (last_error_code IS NULL OR last_error_code IN ('authorization_denied','authorization_expired','enrollment_failed','key_revoked','protocol_error','provider_unavailable','relay_unavailable','removal_failed','rotation_failed','source_access_lost')),
    CHECK ((state = 'pending' AND binding_id IS NULL AND completed_at IS NULL AND poll_ref_cleared_at IS NULL AND protected_poll_ref IS NOT NULL AND last_error_code IS NULL)
        OR (state = 'authorized' AND binding_id IS NOT NULL AND completed_at IS NOT NULL AND last_error_code IS NULL)
        OR (state IN ('denied','expired') AND binding_id IS NULL AND completed_at IS NOT NULL AND last_error_code IS NULL)
        OR (state = 'failed' AND binding_id IS NULL AND completed_at IS NOT NULL AND last_error_code IS NOT NULL)),
    UNIQUE (enrollment_id, owner_user_id),
    FOREIGN KEY (controller_id, key_id) REFERENCES relay_controller_keys(controller_id, key_id),
    FOREIGN KEY (binding_id, owner_user_id) REFERENCES relay_installation_bindings(binding_id, owner_user_id)
);
CREATE UNIQUE INDEX relay_enrollment_one_pending_identity ON relay_enrollments(controller_id, connection_id, installation_id, repository_id) WHERE state = 'pending';
CREATE INDEX relay_enrollment_expiry ON relay_enrollments(expires_at, enrollment_id) WHERE state = 'pending';
CREATE INDEX relay_enrollment_cleanup ON relay_enrollments(state, updated_at, enrollment_id) WHERE state <> 'pending' AND protected_poll_ref IS NOT NULL;
CREATE INDEX relay_enrollment_owner_history ON relay_enrollments(owner_user_id, created_at DESC, enrollment_id DESC);

CREATE TRIGGER relay_controller_identity_immutable BEFORE UPDATE OF singleton,controller_id,created_at ON relay_controllers
BEGIN SELECT RAISE(ABORT, 'relay controller identity is immutable'); END;
CREATE TRIGGER relay_controller_transition BEFORE UPDATE OF state ON relay_controllers
WHEN OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'revoked')
BEGIN SELECT RAISE(ABORT, 'invalid relay controller transition'); END;
CREATE TRIGGER relay_controller_no_delete BEFORE DELETE ON relay_controllers
BEGIN SELECT RAISE(ABORT, 'relay controller history is retained'); END;

CREATE TRIGGER relay_key_identity_immutable BEFORE UPDATE OF key_id,controller_id,public_key,algorithm,protected_key_ref,created_at ON relay_controller_keys
BEGIN SELECT RAISE(ABORT, 'relay controller key identity is immutable'); END;
CREATE TRIGGER relay_key_transition BEFORE UPDATE OF state ON relay_controller_keys
WHEN OLD.state <> NEW.state AND NOT ((OLD.state = 'pending' AND NEW.state IN ('active','revoked')) OR (OLD.state = 'active' AND NEW.state = 'revoked'))
BEGIN SELECT RAISE(ABORT, 'invalid relay controller key transition'); END;
CREATE TRIGGER relay_key_no_delete BEFORE DELETE ON relay_controller_keys
BEGIN SELECT RAISE(ABORT, 'relay controller key history is retained'); END;

CREATE TRIGGER relay_rotation_identity_immutable BEFORE UPDATE OF rotation_id,controller_id,old_key_id,new_key_id,created_at ON relay_key_rotations
BEGIN SELECT RAISE(ABORT, 'relay key rotation identity is immutable'); END;
CREATE TRIGGER relay_rotation_transition BEFORE UPDATE OF state ON relay_key_rotations
WHEN OLD.state <> NEW.state AND NOT ((OLD.state = 'prepare' AND NEW.state IN ('propose','failed'))
    OR (OLD.state = 'propose' AND NEW.state IN ('confirm','failed'))
    OR (OLD.state = 'confirm' AND NEW.state IN ('new_key_auth','failed'))
    OR (OLD.state = 'new_key_auth' AND NEW.state IN ('finalize','failed'))
    OR (OLD.state = 'finalize' AND NEW.state IN ('completed','failed')))
BEGIN SELECT RAISE(ABORT, 'invalid relay key rotation transition'); END;
CREATE TRIGGER relay_rotation_no_delete BEFORE DELETE ON relay_key_rotations
BEGIN SELECT RAISE(ABORT, 'relay key rotation history is retained'); END;

CREATE TRIGGER relay_binding_owner_connection_insert BEFORE INSERT ON relay_installation_bindings
WHEN NOT EXISTS (SELECT 1 FROM source_connections c WHERE c.id = NEW.connection_id AND c.owner_user_id = NEW.owner_user_id)
BEGIN SELECT RAISE(ABORT, 'relay binding owner connection mismatch'); END;
CREATE TRIGGER relay_binding_identity_immutable BEFORE UPDATE OF binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,created_at ON relay_installation_bindings
BEGIN SELECT RAISE(ABORT, 'relay installation binding identity is immutable'); END;
CREATE TRIGGER relay_binding_transition BEFORE UPDATE OF state ON relay_installation_bindings
WHEN OLD.state <> NEW.state AND NOT ((OLD.state = 'pending' AND NEW.state IN ('authorized','denied','expired','failed'))
    OR (OLD.state = 'authorized' AND NEW.state IN ('removal_pending','access_lost'))
    OR (OLD.state = 'removal_pending' AND NEW.state IN ('removed','access_lost','failed'))
    OR (OLD.state = 'access_lost' AND NEW.state IN ('authorized','removal_pending','failed')))
BEGIN SELECT RAISE(ABORT, 'invalid relay installation binding transition'); END;
CREATE TRIGGER relay_binding_no_delete BEFORE DELETE ON relay_installation_bindings
BEGIN SELECT RAISE(ABORT, 'relay installation binding history is retained'); END;

CREATE TRIGGER relay_source_connection_owner_immutable BEFORE UPDATE OF owner_user_id ON source_connections
WHEN OLD.owner_user_id <> NEW.owner_user_id AND (EXISTS (SELECT 1 FROM relay_installation_bindings b WHERE b.connection_id = OLD.id) OR EXISTS (SELECT 1 FROM relay_enrollments e WHERE e.connection_id = OLD.id))
BEGIN SELECT RAISE(ABORT, 'relay source connection ownership is retained'); END;

CREATE TRIGGER relay_enrollment_owner_connection_insert BEFORE INSERT ON relay_enrollments
WHEN NOT EXISTS (SELECT 1 FROM source_connections c WHERE c.id = NEW.connection_id AND c.owner_user_id = NEW.owner_user_id AND c.status = 'connected')
BEGIN SELECT RAISE(ABORT, 'relay enrollment owner connection mismatch'); END;
CREATE TRIGGER relay_enrollment_binding_match BEFORE UPDATE OF binding_id ON relay_enrollments
WHEN NEW.binding_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM relay_installation_bindings b WHERE b.binding_id = NEW.binding_id AND b.owner_user_id = NEW.owner_user_id AND b.controller_id = NEW.controller_id AND b.connection_id = NEW.connection_id AND b.installation_id = NEW.installation_id AND b.repository_id = NEW.repository_id AND b.state = 'authorized')
BEGIN SELECT RAISE(ABORT, 'relay enrollment binding mismatch'); END;
CREATE TRIGGER relay_enrollment_identity_immutable BEFORE UPDATE OF enrollment_id,owner_user_id,connection_id,controller_id,key_id,installation_id,repository_id,purpose,created_at,expires_at ON relay_enrollments
BEGIN SELECT RAISE(ABORT, 'relay enrollment identity is immutable'); END;
CREATE TRIGGER relay_enrollment_transition BEFORE UPDATE OF state ON relay_enrollments
WHEN OLD.state <> NEW.state AND NOT (OLD.state = 'pending' AND NEW.state IN ('authorized','denied','expired','failed'))
BEGIN SELECT RAISE(ABORT, 'invalid relay enrollment transition'); END;
CREATE TRIGGER relay_enrollment_poll_ref_clear BEFORE UPDATE OF protected_poll_ref ON relay_enrollments
WHEN NOT (OLD.protected_poll_ref IS NOT NULL AND NEW.protected_poll_ref IS NULL AND OLD.state <> 'pending' AND NEW.state = OLD.state)
BEGIN SELECT RAISE(ABORT, 'invalid relay enrollment poll cleanup'); END;
CREATE TRIGGER relay_enrollment_no_delete BEFORE DELETE ON relay_enrollments
BEGIN SELECT RAISE(ABORT, 'relay enrollment history is retained'); END;
