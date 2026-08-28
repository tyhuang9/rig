CREATE TABLE relay_controller_session_state (
    controller_id TEXT PRIMARY KEY REFERENCES relay_controllers(controller_id),
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    fence INTEGER NOT NULL CHECK (fence > 0),
    state TEXT NOT NULL CHECK (state IN ('disconnected','connecting','authenticating','ready','backoff','needs_attention','stopped')),
    key_id TEXT,
    last_error_code TEXT,
    attempt INTEGER NOT NULL CHECK (attempt BETWEEN 0 AND 1000000),
    next_attempt_at TEXT,
    last_ready_at TEXT,
    last_seen_at TEXT,
    state_changed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (key_id IS NULL OR (length(key_id) = 36 AND lower(key_id) = key_id AND key_id GLOB '????????-????-????-????-????????????' AND key_id NOT GLOB '*[^0-9a-f-]*')),
    CHECK (last_error_code IS NULL OR last_error_code IN ('key_revoked','protocol_error','relay_unavailable','source_access_lost','rotation_failed')),
    CHECK (last_seen_at IS NULL OR last_ready_at IS NOT NULL),
    CHECK (updated_at >= state_changed_at),
    CHECK ((state = 'ready' AND key_id IS NOT NULL AND last_error_code IS NULL AND attempt=0 AND next_attempt_at IS NULL AND last_ready_at IS NOT NULL)
        OR (state IN ('connecting','authenticating') AND key_id IS NOT NULL AND last_error_code IS NULL AND next_attempt_at IS NULL)
        OR (state = 'disconnected' AND key_id IS NULL AND last_error_code IS NULL AND attempt=0 AND next_attempt_at IS NULL)
        OR (state = 'backoff' AND key_id IS NULL AND last_error_code IS NOT NULL AND attempt>0 AND next_attempt_at IS NOT NULL)
        OR (state = 'needs_attention' AND key_id IS NULL AND last_error_code IS NOT NULL AND next_attempt_at IS NULL)
        OR (state = 'stopped' AND key_id IS NULL AND last_error_code IS NULL AND attempt=0 AND next_attempt_at IS NULL)),
    FOREIGN KEY (controller_id, key_id) REFERENCES relay_controller_keys(controller_id, key_id)
);

CREATE TABLE relay_controller_subscriptions (
    subscription_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    binding_id TEXT NOT NULL,
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL CHECK (repository_id > 0),
    tracked_ref TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active','retired')),
    created_at TEXT NOT NULL,
    retired_at TEXT,
    CHECK (length(subscription_id) = 36 AND lower(subscription_id) = subscription_id AND subscription_id GLOB '????????-????-????-????-????????????' AND subscription_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (length(tracked_ref) BETWEEN 12 AND 255 AND substr(tracked_ref,1,11) = 'refs/heads/'
        AND substr(tracked_ref,-1) NOT IN ('/','.') AND tracked_ref NOT LIKE '%..%' AND tracked_ref NOT LIKE '%//%' AND tracked_ref NOT LIKE '%@{%'
        AND tracked_ref NOT LIKE 'refs/heads/.%' AND tracked_ref NOT LIKE '%/.%' AND tracked_ref NOT LIKE '%.lock' AND tracked_ref NOT LIKE '%.lock/%'
        AND instr(tracked_ref,' ') = 0 AND instr(tracked_ref,'~') = 0 AND instr(tracked_ref,'^') = 0 AND instr(tracked_ref,':') = 0
        AND instr(tracked_ref,'?') = 0 AND instr(tracked_ref,'*') = 0 AND instr(tracked_ref,'[') = 0 AND instr(tracked_ref,'\') = 0),
    CHECK ((state = 'active' AND retired_at IS NULL) OR (state = 'retired' AND retired_at IS NOT NULL)),
    UNIQUE (subscription_id, controller_id),
    FOREIGN KEY (binding_id, owner_user_id) REFERENCES relay_installation_bindings(binding_id, owner_user_id)
);
CREATE UNIQUE INDEX relay_subscription_one_active_scope ON relay_controller_subscriptions(owner_user_id,binding_id,tracked_ref) WHERE state='active';
CREATE INDEX relay_subscription_active_set ON relay_controller_subscriptions(controller_id,subscription_id) WHERE state='active';

CREATE TABLE relay_subscription_sync_heads (
    controller_id TEXT PRIMARY KEY REFERENCES relay_controllers(controller_id),
    acknowledged_generation INTEGER NOT NULL DEFAULT 0 CHECK (acknowledged_generation >= 0),
    dirty INTEGER NOT NULL DEFAULT 1 CHECK (dirty IN (0,1)),
    inflight_generation INTEGER,
    inflight_message_id TEXT,
    updated_at TEXT NOT NULL,
    CHECK ((inflight_generation IS NULL AND inflight_message_id IS NULL) OR
        (inflight_generation = acknowledged_generation + 1 AND inflight_message_id IS NOT NULL AND length(inflight_message_id)=36 AND lower(inflight_message_id)=inflight_message_id AND inflight_message_id GLOB '????????-????-????-????-????????????' AND inflight_message_id NOT GLOB '*[^0-9a-f-]*'))
);

CREATE TABLE relay_subscription_sync_sets (
    controller_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    message_id TEXT NOT NULL,
    sent_at TEXT NOT NULL,
    item_count INTEGER NOT NULL CHECK (item_count BETWEEN 0 AND 1000),
    canonical_digest BLOB NOT NULL CHECK (typeof(canonical_digest)='blob' AND length(canonical_digest)=32),
    state TEXT NOT NULL CHECK (state IN ('inflight','acked')),
    acked_at TEXT,
    PRIMARY KEY (controller_id,generation),
    UNIQUE (controller_id,message_id),
    CHECK (length(message_id)=36 AND lower(message_id)=message_id AND message_id GLOB '????????-????-????-????-????????????' AND message_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK ((state='inflight' AND acked_at IS NULL) OR (state='acked' AND acked_at IS NOT NULL)),
    FOREIGN KEY (controller_id) REFERENCES relay_controllers(controller_id)
);

CREATE TABLE relay_subscription_sync_items (
    controller_id TEXT NOT NULL,
    generation INTEGER NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 999),
    subscription_id TEXT NOT NULL,
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL CHECK (repository_id > 0),
    tracked_ref TEXT NOT NULL,
    PRIMARY KEY (controller_id,generation,ordinal),
    UNIQUE (controller_id,generation,subscription_id),
    CHECK (length(subscription_id)=36 AND lower(subscription_id)=subscription_id AND subscription_id GLOB '????????-????-????-????-????????????' AND subscription_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (length(tracked_ref) BETWEEN 12 AND 255 AND substr(tracked_ref,1,11)='refs/heads/'
        AND substr(tracked_ref,-1) NOT IN ('/','.') AND tracked_ref NOT LIKE '%..%' AND tracked_ref NOT LIKE '%//%' AND tracked_ref NOT LIKE '%@{%'
        AND tracked_ref NOT LIKE 'refs/heads/.%' AND tracked_ref NOT LIKE '%/.%' AND tracked_ref NOT LIKE '%.lock' AND tracked_ref NOT LIKE '%.lock/%'
        AND instr(tracked_ref,' ') = 0 AND instr(tracked_ref,'~') = 0 AND instr(tracked_ref,'^') = 0 AND instr(tracked_ref,':') = 0
        AND instr(tracked_ref,'?') = 0 AND instr(tracked_ref,'*') = 0 AND instr(tracked_ref,'[') = 0 AND instr(tracked_ref,'\') = 0),
    FOREIGN KEY (controller_id,generation) REFERENCES relay_subscription_sync_sets(controller_id,generation)
);

CREATE TABLE relay_source_event_inbox (
    controller_id TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL CHECK (repository_id > 0),
    tracked_ref TEXT NOT NULL,
    observed_sha TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    PRIMARY KEY (controller_id,delivery_id,subscription_id),
    UNIQUE (controller_id,subscription_id,generation),
    CHECK (length(delivery_id)=36 AND lower(delivery_id)=delivery_id AND delivery_id GLOB '????????-????-????-????-????????????' AND delivery_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK (length(observed_sha)=40 AND lower(observed_sha)=observed_sha AND observed_sha NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(tracked_ref) BETWEEN 12 AND 255 AND substr(tracked_ref,1,11)='refs/heads/' AND tracked_ref NOT LIKE '%..%' AND tracked_ref NOT GLOB '*[[:space:]]*'),
    FOREIGN KEY (subscription_id,controller_id) REFERENCES relay_controller_subscriptions(subscription_id,controller_id)
);
CREATE INDEX relay_source_ack_state ON relay_source_event_inbox(controller_id,subscription_id,generation DESC);

CREATE TABLE relay_access_event_inbox (
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    event_id TEXT NOT NULL,
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    repository_id INTEGER NOT NULL DEFAULT 0 CHECK (repository_id >= 0),
    change_code TEXT NOT NULL CHECK (change_code IN ('installation.created','installation.removed','installation.restored','installation.permissions_updated','installation.repositories_reconciled','repository.added','repository.removed')),
    observed_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    PRIMARY KEY (controller_id,event_id),
    CHECK (length(event_id)=36 AND lower(event_id)=event_id AND event_id GLOB '????????-????-????-????-????????????' AND event_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK ((substr(change_code,1,13)='installation.' AND repository_id=0) OR (substr(change_code,1,11)='repository.' AND repository_id>0))
);

CREATE UNIQUE INDEX relay_binding_controller_reference ON relay_installation_bindings(controller_id,binding_id);
CREATE UNIQUE INDEX relay_rotation_controller_reference ON relay_key_rotations(controller_id,rotation_id);

CREATE TABLE relay_outbound_commands (
    controller_id TEXT NOT NULL REFERENCES relay_controllers(controller_id),
    message_id TEXT NOT NULL,
    command_type TEXT NOT NULL CHECK (command_type IN ('binding.remove','key.rotation.propose','key.rotation.confirm','key.rotation.finalize')),
    binding_id TEXT,
    rotation_id TEXT,
    stage TEXT NOT NULL CHECK (stage IN ('remove','propose','confirm','finalize')),
    sent_at TEXT NOT NULL,
    canonical_digest BLOB NOT NULL CHECK (typeof(canonical_digest)='blob' AND length(canonical_digest)=32),
    state TEXT NOT NULL CHECK (state IN ('prepared','completed')),
    completed_at TEXT,
    PRIMARY KEY (controller_id,message_id),
    CHECK (length(message_id)=36 AND lower(message_id)=message_id AND message_id GLOB '????????-????-????-????-????????????' AND message_id NOT GLOB '*[^0-9a-f-]*'),
    CHECK ((command_type='binding.remove' AND binding_id IS NOT NULL AND rotation_id IS NULL AND stage='remove') OR
        (command_type='key.rotation.propose' AND binding_id IS NULL AND rotation_id IS NOT NULL AND stage='propose') OR
        (command_type='key.rotation.confirm' AND binding_id IS NULL AND rotation_id IS NOT NULL AND stage='confirm') OR
        (command_type='key.rotation.finalize' AND binding_id IS NULL AND rotation_id IS NOT NULL AND stage='finalize')),
    CHECK ((state='prepared' AND completed_at IS NULL) OR (state='completed' AND completed_at IS NOT NULL)),
    FOREIGN KEY (controller_id,binding_id) REFERENCES relay_installation_bindings(controller_id,binding_id),
    FOREIGN KEY (controller_id,rotation_id) REFERENCES relay_key_rotations(controller_id,rotation_id)
);
CREATE UNIQUE INDEX relay_outbound_one_binding_stage ON relay_outbound_commands(controller_id,binding_id,stage) WHERE binding_id IS NOT NULL;
CREATE UNIQUE INDEX relay_outbound_one_rotation_stage ON relay_outbound_commands(controller_id,rotation_id,stage) WHERE rotation_id IS NOT NULL;

CREATE TRIGGER relay_session_status_fence BEFORE UPDATE ON relay_controller_session_state
WHEN NEW.epoch < OLD.epoch OR (NEW.epoch=OLD.epoch AND NEW.fence<=OLD.fence)
BEGIN SELECT RAISE(ABORT,'relay session epoch and fence must advance'); END;
CREATE TRIGGER relay_session_status_transition BEFORE UPDATE OF epoch,state ON relay_controller_session_state
WHEN (NEW.epoch=OLD.epoch AND NOT (
        OLD.state=NEW.state OR
        (OLD.state='disconnected' AND NEW.state IN ('connecting','stopped')) OR
        (OLD.state='connecting' AND NEW.state IN ('authenticating','backoff','needs_attention','stopped')) OR
        (OLD.state='authenticating' AND NEW.state IN ('ready','backoff','needs_attention','stopped')) OR
        (OLD.state='ready' AND NEW.state IN ('disconnected','backoff','needs_attention','stopped')) OR
        (OLD.state='backoff' AND NEW.state IN ('connecting','needs_attention','stopped')) OR
        (OLD.state='needs_attention' AND NEW.state IN ('connecting','stopped')) OR
        (OLD.state='stopped' AND NEW.state='stopped')
    )) OR (NEW.epoch=OLD.epoch+1 AND NEW.state NOT IN ('disconnected','connecting','stopped')) OR NEW.epoch>OLD.epoch+1
BEGIN SELECT RAISE(ABORT,'invalid relay session status transition'); END;

CREATE TRIGGER relay_subscription_authorized_insert BEFORE INSERT ON relay_controller_subscriptions
WHEN NOT EXISTS (SELECT 1 FROM relay_installation_bindings b WHERE b.binding_id=NEW.binding_id AND b.owner_user_id=NEW.owner_user_id AND b.controller_id=NEW.controller_id AND b.installation_id=NEW.installation_id AND b.repository_id=NEW.repository_id AND b.state='authorized')
BEGIN SELECT RAISE(ABORT,'relay subscription binding is not authorized'); END;
CREATE TRIGGER relay_subscription_cap BEFORE INSERT ON relay_controller_subscriptions
WHEN NEW.state='active' AND (SELECT COUNT(*) FROM relay_controller_subscriptions WHERE controller_id=NEW.controller_id AND state='active') >= 1000
BEGIN SELECT RAISE(ABORT,'relay subscription limit exceeded'); END;
CREATE TRIGGER relay_subscription_identity_immutable BEFORE UPDATE OF subscription_id,owner_user_id,binding_id,controller_id,installation_id,repository_id,tracked_ref,created_at ON relay_controller_subscriptions
BEGIN SELECT RAISE(ABORT,'relay subscription identity is immutable'); END;
CREATE TRIGGER relay_subscription_transition BEFORE UPDATE OF state ON relay_controller_subscriptions
WHEN OLD.state<>NEW.state AND NOT (OLD.state='active' AND NEW.state='retired')
BEGIN SELECT RAISE(ABORT,'invalid relay subscription transition'); END;
CREATE TRIGGER relay_subscription_no_delete BEFORE DELETE ON relay_controller_subscriptions
BEGIN SELECT RAISE(ABORT,'relay subscription history is retained'); END;
CREATE TRIGGER relay_subscription_dirty_insert AFTER INSERT ON relay_controller_subscriptions
BEGIN INSERT INTO relay_subscription_sync_heads(controller_id,acknowledged_generation,dirty,updated_at) VALUES(NEW.controller_id,0,1,NEW.created_at) ON CONFLICT(controller_id) DO UPDATE SET dirty=1,updated_at=NEW.created_at; END;
CREATE TRIGGER relay_subscription_dirty_retire AFTER UPDATE OF state ON relay_controller_subscriptions
WHEN OLD.state='active' AND NEW.state='retired'
BEGIN UPDATE relay_subscription_sync_heads SET dirty=1,updated_at=NEW.retired_at WHERE controller_id=NEW.controller_id; END;

CREATE TRIGGER relay_sync_set_immutable BEFORE UPDATE OF controller_id,generation,message_id,sent_at,item_count,canonical_digest ON relay_subscription_sync_sets
BEGIN SELECT RAISE(ABORT,'relay sync set is immutable'); END;
CREATE TRIGGER relay_sync_set_transition BEFORE UPDATE OF state ON relay_subscription_sync_sets
WHEN OLD.state<>NEW.state AND NOT (OLD.state='inflight' AND NEW.state='acked')
BEGIN SELECT RAISE(ABORT,'invalid relay sync transition'); END;
CREATE TRIGGER relay_sync_set_no_delete BEFORE DELETE ON relay_subscription_sync_sets
BEGIN SELECT RAISE(ABORT,'relay sync history is retained'); END;
CREATE TRIGGER relay_sync_item_immutable BEFORE UPDATE ON relay_subscription_sync_items
BEGIN SELECT RAISE(ABORT,'relay sync item is immutable'); END;
CREATE TRIGGER relay_sync_item_no_delete BEFORE DELETE ON relay_subscription_sync_items
BEGIN SELECT RAISE(ABORT,'relay sync item history is retained'); END;

CREATE TRIGGER relay_source_inbox_immutable BEFORE UPDATE ON relay_source_event_inbox
BEGIN SELECT RAISE(ABORT,'relay source inbox is immutable'); END;
CREATE TRIGGER relay_source_inbox_no_delete BEFORE DELETE ON relay_source_event_inbox
BEGIN SELECT RAISE(ABORT,'relay source inbox history is retained'); END;
CREATE TRIGGER relay_access_inbox_immutable BEFORE UPDATE ON relay_access_event_inbox
BEGIN SELECT RAISE(ABORT,'relay access inbox is immutable'); END;
CREATE TRIGGER relay_access_inbox_no_delete BEFORE DELETE ON relay_access_event_inbox
BEGIN SELECT RAISE(ABORT,'relay access inbox history is retained'); END;

CREATE TRIGGER relay_outbound_command_immutable BEFORE UPDATE OF controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest ON relay_outbound_commands
BEGIN SELECT RAISE(ABORT,'relay outbound command is immutable'); END;
CREATE TRIGGER relay_outbound_command_aggregate_state BEFORE INSERT ON relay_outbound_commands
WHEN (NEW.command_type='binding.remove' AND NOT EXISTS (
        SELECT 1 FROM relay_installation_bindings b WHERE b.controller_id=NEW.controller_id AND b.binding_id=NEW.binding_id AND b.state='removal_pending'
    )) OR (NEW.command_type='key.rotation.propose' AND NOT EXISTS (
        SELECT 1 FROM relay_key_rotations r WHERE r.controller_id=NEW.controller_id AND r.rotation_id=NEW.rotation_id AND r.state='propose'
    )) OR (NEW.command_type='key.rotation.confirm' AND NOT EXISTS (
        SELECT 1 FROM relay_key_rotations r WHERE r.controller_id=NEW.controller_id AND r.rotation_id=NEW.rotation_id AND r.state='confirm'
    )) OR (NEW.command_type='key.rotation.finalize' AND NOT EXISTS (
        SELECT 1 FROM relay_key_rotations r WHERE r.controller_id=NEW.controller_id AND r.rotation_id=NEW.rotation_id AND r.state='finalize'
    ))
BEGIN SELECT RAISE(ABORT,'relay outbound command aggregate state mismatch'); END;
CREATE TRIGGER relay_outbound_command_transition BEFORE UPDATE OF state ON relay_outbound_commands
WHEN OLD.state<>NEW.state AND NOT (OLD.state='prepared' AND NEW.state='completed')
BEGIN SELECT RAISE(ABORT,'invalid relay outbound command transition'); END;
CREATE TRIGGER relay_outbound_command_no_delete BEFORE DELETE ON relay_outbound_commands
BEGIN SELECT RAISE(ABORT,'relay outbound command history is retained'); END;
