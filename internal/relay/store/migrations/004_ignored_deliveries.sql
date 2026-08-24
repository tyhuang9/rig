ALTER TABLE relay_github_deliveries
    DROP CONSTRAINT relay_github_deliveries_delivery_kind_check,
    ADD CHECK (delivery_kind IN ('source','access','ignored'));

CREATE TABLE relay_ignored_deliveries (
    delivery_id uuid PRIMARY KEY REFERENCES relay_github_deliveries(delivery_id),
    reason_code text NOT NULL CHECK (reason_code IN ('push.deleted','push.untracked_ref','installation.unsupported_action','installation_repositories.unsupported_action')),
    persisted_at timestamptz NOT NULL
);
