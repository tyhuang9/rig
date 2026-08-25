CREATE INDEX relay_pending_access_order
    ON relay_access_events(controller_id, observed_at, event_id)
    WHERE decision IS NULL;

CREATE TABLE relay_topology_lock_shards (
    shard_id smallint PRIMARY KEY,
    CONSTRAINT relay_topology_lock_shard_range CHECK (shard_id>=0 AND shard_id<64)
);

INSERT INTO relay_topology_lock_shards(shard_id)
SELECT shard_id::smallint
FROM generate_series(0,63) AS shard_id;

ALTER TABLE relay_subscriptions
    ADD COLUMN retired_at timestamptz;

UPDATE relay_subscriptions
SET retired_at=CURRENT_TIMESTAMP
WHERE retired_generation IS NOT NULL;

ALTER TABLE relay_subscriptions
    ADD CONSTRAINT relay_subscription_retirement_time
    CHECK ((retired_generation IS NULL AND retired_at IS NULL) OR (retired_generation IS NOT NULL AND retired_at IS NOT NULL AND retired_at>=created_at));

CREATE INDEX relay_retired_subscriptions_prune
    ON relay_subscriptions(retired_at, subscription_id)
    WHERE retired_generation IS NOT NULL;

CREATE INDEX relay_source_delivery_targets_prune
    ON relay_source_delivery_targets(persisted_at, delivery_id, subscription_id);

CREATE INDEX relay_desired_states_prune
    ON relay_desired_states(observed_at, subscription_id);

CREATE INDEX relay_desired_states_delivery_lookup
    ON relay_desired_states(delivery_id);

CREATE INDEX relay_subscription_set_items_prune
    ON relay_subscription_set_items(created_at, controller_id, set_generation, subscription_id);

CREATE INDEX relay_subscription_set_items_subscription_lookup
    ON relay_subscription_set_items(subscription_id);

CREATE INDEX relay_terminal_access_events_prune
    ON relay_access_events(decided_at, event_id)
    WHERE decision IS NOT NULL;

CREATE INDEX relay_ignored_deliveries_prune
    ON relay_ignored_deliveries(persisted_at, delivery_id);

CREATE INDEX relay_github_deliveries_prune
    ON relay_github_deliveries(persisted_at, delivery_id);

CREATE INDEX relay_recovery_deliveries_completed_prune
    ON relay_recovery_deliveries(recovered_at, delivery_id)
    WHERE recovered_at IS NOT NULL AND claim_id IS NULL AND claim_expires_at IS NULL;

CREATE INDEX relay_recovery_attempts_delivery_lookup
    ON relay_recovery_delivery_attempts(delivery_id, delivery_number);

CREATE INDEX relay_controller_leases_expiry_prune
    ON relay_controller_leases(expires_at, controller_id);

CREATE INDEX relay_session_commands_prune
    ON relay_session_commands(applied_at, session_id, controller_id, message_id);

CREATE INDEX relay_session_messages_prune
    ON relay_session_messages(seen_at, session_id, message_id);

CREATE INDEX relay_sessions_expiry_prune
    ON relay_sessions(expires_at, session_id);

CREATE INDEX relay_sessions_revoked_prune
    ON relay_sessions(revoked_at, session_id)
    WHERE revoked_at IS NOT NULL;

CREATE INDEX relay_rotation_references_prune
    ON relay_controller_keys(revoked_at, key_id)
    WHERE state='revoked' AND rotation_session_id IS NOT NULL;

CREATE INDEX relay_rotation_session_lookup
    ON relay_controller_keys(rotation_session_id)
    WHERE rotation_session_id IS NOT NULL;

CREATE INDEX relay_wss_challenges_expiry_prune
    ON relay_wss_challenges(expires_at, session_id);

CREATE INDEX relay_wss_challenges_consumed_prune
    ON relay_wss_challenges(consumed_at, session_id)
    WHERE consumed_at IS NOT NULL;
