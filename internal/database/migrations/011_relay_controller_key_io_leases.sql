CREATE TABLE relay_controller_key_io_leases (
    scope_key TEXT NOT NULL PRIMARY KEY,
    controller_id TEXT NOT NULL UNIQUE,
    lease_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation IN ('identity_write','write','revoked_cleanup','key_cleanup','temp_cleanup')),
    phase TEXT NOT NULL CHECK (phase IN ('active','recovery')),
    fence INTEGER NOT NULL CHECK (fence > 0),
    lease_expires_at TEXT NOT NULL,
    key_id TEXT,
    rotation_id TEXT,
    old_key_id TEXT,
    public_key BLOB,
    protected_key_ref TEXT,
    artifact_name TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((operation = 'identity_write' AND scope_key = '0/identity')
        OR (operation <> 'identity_write' AND scope_key = '1/controller/' || controller_id)),
    CHECK (length(controller_id) = 36 AND lower(controller_id) = controller_id AND controller_id GLOB '????????-????-????-????-????????????' AND controller_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (length(lease_id) = 36 AND lower(lease_id) = lease_id AND lease_id GLOB '????????-????-????-????-????????????' AND lease_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (julianday(lease_expires_at) IS NOT NULL AND julianday(created_at) IS NOT NULL AND julianday(updated_at) IS NOT NULL),
    CHECK (julianday(updated_at) >= julianday(created_at) AND julianday(lease_expires_at) > julianday(updated_at)),
    CHECK ((operation = 'identity_write' AND phase IN ('active','recovery')
            AND key_id IS NOT NULL AND rotation_id IS NULL AND old_key_id IS NULL
            AND public_key IS NOT NULL AND protected_key_ref IS NOT NULL AND artifact_name IS NULL
            AND length(key_id) = 36 AND lower(key_id) = key_id AND key_id GLOB '????????-????-????-????-????????????' AND key_id NOT GLOB '*[^0-9a-f-]*'
            AND typeof(public_key) = 'blob' AND length(public_key) = 32
            AND protected_key_ref = 'relay/controllers/' || controller_id || '/keys/' || key_id || '.key')
        OR (operation = 'write' AND phase IN ('active','recovery')
            AND key_id IS NOT NULL AND rotation_id IS NOT NULL AND old_key_id IS NOT NULL
            AND public_key IS NOT NULL AND protected_key_ref IS NOT NULL AND artifact_name IS NULL
            AND length(key_id) = 36 AND lower(key_id) = key_id AND key_id GLOB '????????-????-????-????-????????????' AND key_id NOT GLOB '*[^0-9a-f-]*'
            AND length(rotation_id) = 36 AND lower(rotation_id) = rotation_id AND rotation_id GLOB '????????-????-????-????-????????????' AND rotation_id NOT GLOB '*[^0-9a-f-]*'
            AND length(old_key_id) = 36 AND lower(old_key_id) = old_key_id AND old_key_id GLOB '????????-????-????-????-????????????' AND old_key_id NOT GLOB '*[^0-9a-f-]*'
            AND key_id <> old_key_id AND typeof(public_key) = 'blob' AND length(public_key) = 32
            AND protected_key_ref = 'relay/controllers/' || controller_id || '/keys/' || key_id || '.key')
        OR (operation = 'key_cleanup' AND phase = 'recovery'
            AND key_id IS NOT NULL AND rotation_id IS NULL AND old_key_id IS NULL
            AND public_key IS NULL AND protected_key_ref = 'relay/controllers/' || controller_id || '/keys/' || key_id || '.key' AND artifact_name IS NULL
            AND length(key_id) = 36 AND lower(key_id) = key_id AND key_id GLOB '????????-????-????-????-????????????' AND key_id NOT GLOB '*[^0-9a-f-]*')
        OR (operation = 'revoked_cleanup' AND phase = 'recovery'
            AND key_id IS NOT NULL AND rotation_id IS NULL AND old_key_id IS NULL
            AND public_key IS NULL AND protected_key_ref = 'relay/controllers/' || controller_id || '/keys/' || key_id || '.key' AND artifact_name IS NULL
            AND length(key_id) = 36 AND lower(key_id) = key_id AND key_id GLOB '????????-????-????-????-????????????' AND key_id NOT GLOB '*[^0-9a-f-]*')
        OR (operation = 'temp_cleanup' AND phase = 'recovery'
            AND key_id IS NULL AND rotation_id IS NULL AND old_key_id IS NULL
            AND public_key IS NULL AND protected_key_ref IS NULL AND artifact_name IS NOT NULL
            AND length(artifact_name) BETWEEN 15 AND 128
            AND substr(artifact_name,1,14) = '.hostd-secret-'
            AND substr(artifact_name,15) NOT GLOB '*[^0-9]*'))
);
CREATE INDEX relay_controller_key_io_expiry ON relay_controller_key_io_leases(lease_expires_at,scope_key);

CREATE INDEX relay_key_rotation_old_reference ON relay_key_rotations(controller_id,old_key_id,state);
CREATE INDEX relay_key_rotation_new_reference ON relay_key_rotations(controller_id,new_key_id,state);

CREATE TRIGGER relay_controller_key_io_identity_immutable BEFORE UPDATE OF scope_key,controller_id,operation,key_id,rotation_id,old_key_id,public_key,protected_key_ref,artifact_name,created_at ON relay_controller_key_io_leases
BEGIN SELECT RAISE(ABORT, 'relay controller key IO lease identity is immutable'); END;
CREATE TRIGGER relay_controller_key_io_write_old_key BEFORE INSERT ON relay_controller_key_io_leases
WHEN NEW.operation = 'write' AND NOT EXISTS (SELECT 1 FROM relay_controller_keys WHERE controller_id=NEW.controller_id AND key_id=NEW.old_key_id)
BEGIN SELECT RAISE(ABORT, 'relay controller key IO write requires an existing old key'); END;
CREATE TRIGGER relay_controller_key_io_fence BEFORE UPDATE OF lease_id,phase,fence,lease_expires_at ON relay_controller_key_io_leases
WHEN NEW.fence <= OLD.fence OR NEW.lease_id = OLD.lease_id
BEGIN SELECT RAISE(ABORT, 'relay controller key IO lease fence must advance'); END;
