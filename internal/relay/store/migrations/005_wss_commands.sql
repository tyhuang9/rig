CREATE TABLE relay_session_commands (
    controller_id uuid NOT NULL REFERENCES relay_controllers(controller_id),
    message_id uuid NOT NULL,
    session_id uuid NOT NULL,
    lease_id uuid NOT NULL,
    lease_fence bigint NOT NULL CHECK (lease_fence > 0),
    command_digest bytea NOT NULL CHECK (octet_length(command_digest) = 32),
    command_type text NOT NULL CHECK (command_type IN (
        'subscriptions.sync',
        'ack.source',
        'reject.source',
        'ack.access',
        'reject.access',
        'binding.remove',
        'controller.revoke',
        'key.revoke',
        'key.rotation.propose',
        'key.rotation.confirm',
        'key.rotation.finalize'
    )),
    result_kind text NOT NULL CHECK (result_kind IN (
        'subscriptions_synced',
        'decision_applied',
        'protocol_error',
        'binding_removed',
        'controller_revoked',
        'key_revoked',
        'rotation_challenge',
        'rotation_confirmed',
        'rotation_finalized'
    )),
    result_error_code text CHECK (result_error_code IN ('stale_target','unknown_target','target_mismatch')),
    result_generation bigint CHECK (result_generation IS NULL OR result_generation > 0),
    result_count integer CHECK (result_count IS NULL OR result_count BETWEEN 0 AND 1000),
    result_installation_id bigint CHECK (result_installation_id IS NULL OR result_installation_id > 0),
    result_repository_id bigint CHECK (result_repository_id IS NULL OR result_repository_id > 0),
    result_controller_id uuid,
    result_key_id uuid,
    result_rotation_id uuid,
    result_retired_key_id uuid,
    result_nonce bytea CHECK (result_nonce IS NULL OR octet_length(result_nonce) = 32),
    result_expires_at timestamptz,
    applied_at timestamptz NOT NULL,
    PRIMARY KEY (controller_id, message_id),
    FOREIGN KEY (session_id, controller_id) REFERENCES relay_sessions(session_id, controller_id),
    CHECK (
        (result_kind = 'subscriptions_synced' AND result_generation IS NOT NULL AND result_count IS NOT NULL AND result_error_code IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'decision_applied' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'protocol_error' AND result_error_code IS NOT NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'binding_removed' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NOT NULL AND result_repository_id IS NOT NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'controller_revoked' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NOT NULL AND result_key_id IS NULL AND result_rotation_id IS NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'key_revoked' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NOT NULL AND result_key_id IS NOT NULL AND result_rotation_id IS NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'rotation_challenge' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NOT NULL AND result_retired_key_id IS NULL AND result_nonce IS NOT NULL AND result_expires_at IS NOT NULL)
        OR
        (result_kind = 'rotation_confirmed' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NOT NULL AND result_retired_key_id IS NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
        OR
        (result_kind = 'rotation_finalized' AND result_error_code IS NULL AND result_generation IS NULL AND result_count IS NULL AND result_installation_id IS NULL AND result_repository_id IS NULL AND result_controller_id IS NULL AND result_key_id IS NULL AND result_rotation_id IS NOT NULL AND result_retired_key_id IS NOT NULL AND result_nonce IS NULL AND result_expires_at IS NULL)
    ),
    CHECK (
        (command_type = 'subscriptions.sync' AND result_kind = 'subscriptions_synced')
        OR
        (command_type IN ('ack.source','reject.source','ack.access','reject.access') AND result_kind IN ('decision_applied','protocol_error'))
        OR
        (command_type = 'binding.remove' AND result_kind = 'binding_removed')
        OR
        (command_type = 'controller.revoke' AND result_kind = 'controller_revoked')
        OR
        (command_type = 'key.revoke' AND result_kind = 'key_revoked')
        OR
        (command_type = 'key.rotation.propose' AND result_kind = 'rotation_challenge')
        OR
        (command_type = 'key.rotation.confirm' AND result_kind = 'rotation_confirmed')
        OR
        (command_type = 'key.rotation.finalize' AND result_kind = 'rotation_finalized')
    )
);

CREATE INDEX relay_session_commands_session
    ON relay_session_commands(session_id, applied_at);
