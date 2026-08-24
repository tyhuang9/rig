CREATE TABLE relay_controllers (
    controller_id uuid PRIMARY KEY,
    state text NOT NULL CHECK (state IN ('active','revoked')),
    created_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK ((state = 'active' AND revoked_at IS NULL) OR (state = 'revoked' AND revoked_at IS NOT NULL))
);

CREATE TABLE relay_controller_keys (
    key_id uuid PRIMARY KEY,
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
    state text NOT NULL CHECK (state IN ('active','pending','revoked')),
    rotation_id uuid,
    rotation_old_key_id uuid,
    rotation_session_id uuid,
    rotation_nonce bytea CHECK (rotation_nonce IS NULL OR octet_length(rotation_nonce) = 32),
    rotation_expires_at timestamptz,
    created_at timestamptz NOT NULL,
    possession_confirmed_at timestamptz,
    revoked_at timestamptz,
    CHECK ((state = 'pending' AND rotation_id IS NOT NULL AND rotation_old_key_id IS NOT NULL AND rotation_session_id IS NOT NULL AND rotation_nonce IS NOT NULL AND rotation_expires_at IS NOT NULL) OR (state = 'active' AND rotation_id IS NULL AND rotation_old_key_id IS NULL AND rotation_session_id IS NULL AND rotation_nonce IS NULL AND rotation_expires_at IS NULL) OR state = 'revoked'),
    CHECK (state <> 'revoked' OR revoked_at IS NOT NULL),
    UNIQUE (controller_id, key_id),
    UNIQUE (public_key),
    FOREIGN KEY (controller_id, rotation_old_key_id) REFERENCES relay_controller_keys(controller_id, key_id)
);
CREATE UNIQUE INDEX relay_one_active_key ON relay_controller_keys(controller_id) WHERE state = 'active';
CREATE UNIQUE INDEX relay_one_pending_key ON relay_controller_keys(controller_id) WHERE state = 'pending';
CREATE UNIQUE INDEX relay_pending_rotation_id ON relay_controller_keys(controller_id, rotation_id) WHERE state = 'pending';

CREATE TABLE relay_bindings (
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    installation_id bigint NOT NULL CHECK (installation_id > 0),
    repository_id bigint NOT NULL CHECK (repository_id > 0),
    created_at timestamptz NOT NULL,
    revoked_at timestamptz,
    PRIMARY KEY (controller_id, installation_id, repository_id)
);
CREATE INDEX relay_active_bindings ON relay_bindings(installation_id, repository_id, controller_id) WHERE revoked_at IS NULL;

CREATE TABLE relay_enrollments (
    enrollment_id uuid PRIMARY KEY,
    controller_id uuid NOT NULL,
    key_id uuid NOT NULL,
    public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
    installation_id bigint NOT NULL CHECK (installation_id > 0),
    repository_id bigint NOT NULL CHECK (repository_id > 0),
    state_hash bytea NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    poll_hash bytea NOT NULL UNIQUE CHECK (octet_length(poll_hash) = 32),
    pkce_ciphertext bytea CHECK (pkce_ciphertext IS NULL OR octet_length(pkce_ciphertext) BETWEEN 29 AND 4096),
    pkce_seal_nonce bytea CHECK (pkce_seal_nonce IS NULL OR octet_length(pkce_seal_nonce) = 12),
    request_nonce bytea NOT NULL CHECK (octet_length(request_nonce) = 32),
    status text NOT NULL CHECK (status IN ('pending','state_claimed','authorized','failed','expired')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state_claimed_at timestamptz,
    completed_at timestamptz,
    polled_at timestamptz,
    failure_code text CHECK (failure_code IS NULL OR length(failure_code) BETWEEN 1 AND 128),
    CHECK (expires_at > created_at),
    CHECK ((status IN ('pending','state_claimed') AND pkce_ciphertext IS NOT NULL AND pkce_seal_nonce IS NOT NULL AND completed_at IS NULL AND failure_code IS NULL) OR (status IN ('authorized','expired') AND pkce_ciphertext IS NULL AND pkce_seal_nonce IS NULL AND completed_at IS NOT NULL AND failure_code IS NULL) OR (status = 'failed' AND pkce_ciphertext IS NULL AND pkce_seal_nonce IS NULL AND completed_at IS NOT NULL AND failure_code IS NOT NULL)),
    CHECK ((status = 'pending' AND state_claimed_at IS NULL) OR (status IN ('state_claimed','authorized','failed') AND state_claimed_at IS NOT NULL) OR status = 'expired')
);
CREATE INDEX relay_enrollment_expiry ON relay_enrollments(expires_at) WHERE status IN ('pending','state_claimed');

CREATE TABLE relay_subscription_heads (
    controller_id uuid PRIMARY KEY REFERENCES relay_controllers(controller_id),
    generation bigint NOT NULL CHECK (generation > 0),
    updated_at timestamptz NOT NULL
);
CREATE TABLE relay_subscriptions (
    subscription_id uuid PRIMARY KEY,
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    installation_id bigint NOT NULL CHECK (installation_id > 0),
    repository_id bigint NOT NULL CHECK (repository_id > 0),
    tracked_ref text NOT NULL CHECK (length(tracked_ref) BETWEEN 12 AND 255 AND tracked_ref LIKE 'refs/heads/%'),
    activated_generation bigint NOT NULL CHECK (activated_generation > 0),
    retired_generation bigint CHECK (retired_generation IS NULL OR retired_generation > activated_generation),
    created_at timestamptz NOT NULL,
    FOREIGN KEY (controller_id, installation_id, repository_id) REFERENCES relay_bindings(controller_id, installation_id, repository_id),
    UNIQUE (controller_id, subscription_id),
    UNIQUE (subscription_id, controller_id, installation_id, repository_id, tracked_ref)
);
CREATE INDEX relay_current_subscriptions ON relay_subscriptions(controller_id, subscription_id) WHERE retired_generation IS NULL;
CREATE TABLE relay_subscription_set_items (
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    set_generation bigint NOT NULL CHECK (set_generation > 0),
    subscription_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (controller_id, set_generation, subscription_id),
    FOREIGN KEY (controller_id, subscription_id) REFERENCES relay_subscriptions(controller_id, subscription_id)
);
CREATE INDEX relay_subscription_current_lookup ON relay_subscription_set_items(controller_id, subscription_id, set_generation);

CREATE TABLE relay_github_deliveries (
    delivery_id uuid PRIMARY KEY,
    delivery_kind text NOT NULL CHECK (delivery_kind IN ('source','access')),
    received_at timestamptz NOT NULL,
    persisted_at timestamptz NOT NULL
);

CREATE TABLE relay_desired_states (
	 subscription_id uuid PRIMARY KEY REFERENCES relay_subscriptions(subscription_id),
    delivery_id uuid NOT NULL REFERENCES relay_github_deliveries(delivery_id),
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    generation bigint NOT NULL CHECK (generation > 0),
    installation_id bigint NOT NULL CHECK (installation_id > 0),
    repository_id bigint NOT NULL CHECK (repository_id > 0),
    tracked_ref text NOT NULL CHECK (length(tracked_ref) BETWEEN 12 AND 255 AND tracked_ref LIKE 'refs/heads/%'),
    observed_sha char(40) NOT NULL CHECK (observed_sha ~ '^[0-9a-f]{40}$'),
    observed_at timestamptz NOT NULL,
    decision text CHECK (decision IS NULL OR decision IN ('acked','rejected')),
    decision_code text CHECK (decision_code IS NULL OR length(decision_code) BETWEEN 1 AND 128),
    decision_message_id uuid UNIQUE,
    decided_at timestamptz,
    CHECK ((decision IS NULL AND decision_code IS NULL AND decision_message_id IS NULL AND decided_at IS NULL) OR (decision = 'acked' AND decision_code IS NULL AND decision_message_id IS NOT NULL AND decided_at IS NOT NULL) OR (decision = 'rejected' AND decision_code IS NOT NULL AND decision_message_id IS NOT NULL AND decided_at IS NOT NULL)),
    FOREIGN KEY (subscription_id, controller_id, installation_id, repository_id, tracked_ref) REFERENCES relay_subscriptions(subscription_id, controller_id, installation_id, repository_id, tracked_ref)
);
CREATE INDEX relay_pending_desired_order ON relay_desired_states(controller_id, observed_at, delivery_id) WHERE decision IS NULL;

CREATE TABLE relay_source_delivery_targets (
    delivery_id uuid NOT NULL REFERENCES relay_github_deliveries(delivery_id),
    subscription_id uuid NOT NULL REFERENCES relay_subscriptions(subscription_id),
    generation bigint NOT NULL CHECK (generation > 0),
    persisted_at timestamptz NOT NULL,
    PRIMARY KEY (delivery_id, subscription_id),
    UNIQUE (subscription_id, generation)
);

CREATE TABLE relay_access_events (
    event_id uuid PRIMARY KEY,
    delivery_id uuid NOT NULL REFERENCES relay_github_deliveries(delivery_id),
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    installation_id bigint NOT NULL CHECK (installation_id > 0),
    repository_id bigint CHECK (repository_id IS NULL OR repository_id > 0),
    change_code text NOT NULL CHECK (length(change_code) BETWEEN 1 AND 128),
    observed_at timestamptz NOT NULL,
    decision text CHECK (decision IS NULL OR decision IN ('acked','rejected')),
    decision_code text CHECK (decision_code IS NULL OR length(decision_code) BETWEEN 1 AND 128),
    decision_message_id uuid UNIQUE,
    decided_at timestamptz,
    CHECK ((decision IS NULL AND decision_code IS NULL AND decision_message_id IS NULL AND decided_at IS NULL) OR (decision = 'acked' AND decision_code IS NULL AND decision_message_id IS NOT NULL AND decided_at IS NOT NULL) OR (decision = 'rejected' AND decision_code IS NOT NULL AND decision_message_id IS NOT NULL AND decided_at IS NOT NULL))
);
CREATE UNIQUE INDEX relay_access_delivery_target ON relay_access_events(delivery_id, controller_id, installation_id, COALESCE(repository_id, 0));

CREATE TABLE relay_wss_challenges (
    session_id uuid PRIMARY KEY,
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    key_id uuid NOT NULL REFERENCES relay_controller_keys(key_id),
    client_nonce bytea NOT NULL CHECK (octet_length(client_nonce) = 32),
    server_nonce bytea NOT NULL CHECK (octet_length(server_nonce) = 32),
    ack_digest bytea NOT NULL CHECK (octet_length(ack_digest) = 32),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (expires_at > created_at),
    UNIQUE (controller_id, key_id, client_nonce),
    FOREIGN KEY (controller_id, key_id) REFERENCES relay_controller_keys(controller_id, key_id),
    UNIQUE (session_id, controller_id, key_id)
);
CREATE INDEX relay_challenge_expiry ON relay_wss_challenges(expires_at) WHERE consumed_at IS NULL;

CREATE TABLE relay_sessions (
    session_id uuid PRIMARY KEY REFERENCES relay_wss_challenges(session_id),
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    key_id uuid NOT NULL REFERENCES relay_controller_keys(key_id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    FOREIGN KEY (controller_id, key_id) REFERENCES relay_controller_keys(controller_id, key_id),
    FOREIGN KEY (session_id, controller_id, key_id) REFERENCES relay_wss_challenges(session_id, controller_id, key_id),
    UNIQUE (session_id, controller_id),
    UNIQUE (session_id, controller_id, key_id)
);
CREATE INDEX relay_active_sessions ON relay_sessions(controller_id, expires_at) WHERE revoked_at IS NULL;
ALTER TABLE relay_controller_keys ADD CONSTRAINT relay_rotation_origin_session
    FOREIGN KEY (rotation_session_id, controller_id, rotation_old_key_id)
    REFERENCES relay_sessions(session_id, controller_id, key_id);
CREATE TABLE relay_session_messages (
    session_id uuid NOT NULL REFERENCES relay_sessions(session_id),
    message_id uuid NOT NULL,
    seen_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, message_id)
);

CREATE TABLE relay_controller_leases (
    controller_id uuid PRIMARY KEY REFERENCES relay_controllers(controller_id),
    session_id uuid NOT NULL UNIQUE,
    lease_id uuid NOT NULL UNIQUE,
    fence bigint NOT NULL CHECK (fence > 0),
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (session_id, controller_id) REFERENCES relay_sessions(session_id, controller_id)
);

CREATE TABLE relay_recovery_cursor (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    scan_id uuid NOT NULL UNIQUE,
    fence bigint NOT NULL CHECK (fence > 0),
    window_started_at timestamptz NOT NULL,
    window_ends_at timestamptz NOT NULL,
    page_cursor text CHECK (page_cursor IS NULL OR length(page_cursor) BETWEEN 1 AND 1024),
    completed boolean NOT NULL DEFAULT false,
    lease_expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (window_ends_at > window_started_at)
);

CREATE TABLE relay_recovery_deliveries (
    delivery_number bigint PRIMARY KEY CHECK (delivery_number > 0),
    delivery_id uuid NOT NULL UNIQUE,
    occurred_at timestamptz NOT NULL,
    discovered_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 1000000),
    next_attempt_at timestamptz,
    last_error_code text CHECK (last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 128),
    recovered_at timestamptz,
    claim_id uuid,
    claim_fence bigint NOT NULL DEFAULT 0 CHECK (claim_fence >= 0),
    claim_expires_at timestamptz,
    CHECK ((claim_id IS NULL) = (claim_expires_at IS NULL))
);
CREATE INDEX relay_recovery_due ON relay_recovery_deliveries(next_attempt_at, delivery_number) WHERE recovered_at IS NULL;
