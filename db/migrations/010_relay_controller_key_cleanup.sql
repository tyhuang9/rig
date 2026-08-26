ALTER TABLE relay_controller_keys ADD COLUMN protected_key_cleared_at TEXT
    CHECK (protected_key_cleared_at IS NULL OR state = 'revoked');

CREATE TRIGGER relay_key_protected_clear_insert BEFORE INSERT ON relay_controller_keys
WHEN NEW.protected_key_cleared_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'relay controller key protected clear marker is update-only'); END;

CREATE TRIGGER relay_key_protected_clear_transition BEFORE UPDATE OF protected_key_cleared_at ON relay_controller_keys
WHEN OLD.protected_key_cleared_at IS NOT NULL
    OR NEW.protected_key_cleared_at IS NULL
    OR OLD.state <> 'revoked'
    OR length(NEW.protected_key_cleared_at) NOT BETWEEN 20 AND 30
    OR substr(NEW.protected_key_cleared_at, 5, 1) <> '-'
    OR substr(NEW.protected_key_cleared_at, 8, 1) <> '-'
    OR substr(NEW.protected_key_cleared_at, 11, 1) <> 'T'
    OR substr(NEW.protected_key_cleared_at, 14, 1) <> ':'
    OR substr(NEW.protected_key_cleared_at, 17, 1) <> ':'
    OR substr(NEW.protected_key_cleared_at, -1, 1) <> 'Z'
    OR (length(NEW.protected_key_cleared_at) > 20 AND (
        substr(NEW.protected_key_cleared_at, 20, 1) <> '.'
        OR substr(NEW.protected_key_cleared_at, 21, length(NEW.protected_key_cleared_at) - 21) = ''
        OR substr(NEW.protected_key_cleared_at, 21, length(NEW.protected_key_cleared_at) - 21) GLOB '*[^0-9]*'
    ))
    OR julianday(NEW.protected_key_cleared_at) IS NULL
BEGIN SELECT RAISE(ABORT, 'invalid relay controller key protected clear transition'); END;
