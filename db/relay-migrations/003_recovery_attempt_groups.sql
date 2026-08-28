ALTER TABLE relay_recovery_deliveries
    DROP CONSTRAINT relay_recovery_deliveries_pkey,
    DROP CONSTRAINT relay_recovery_deliveries_delivery_id_key,
    ALTER COLUMN delivery_number DROP NOT NULL,
    ALTER COLUMN occurred_at DROP NOT NULL,
    ADD COLUMN provider_succeeded_at timestamptz,
	ADD CHECK ((delivery_number IS NULL) = (occurred_at IS NULL)),
    ADD PRIMARY KEY (delivery_id);

CREATE TABLE relay_recovery_delivery_attempts (
    delivery_number bigint PRIMARY KEY CHECK (delivery_number > 0),
    delivery_id uuid NOT NULL REFERENCES relay_recovery_deliveries(delivery_id),
    occurred_at timestamptz NOT NULL,
    successful boolean NOT NULL,
    discovered_at timestamptz NOT NULL,
    UNIQUE (delivery_number, delivery_id)
);

INSERT INTO relay_recovery_delivery_attempts(delivery_number,delivery_id,occurred_at,successful,discovered_at)
SELECT delivery_number,delivery_id,occurred_at,false,discovered_at
FROM relay_recovery_deliveries;

ALTER TABLE relay_recovery_deliveries
    ADD CONSTRAINT relay_recovery_selected_attempt
    FOREIGN KEY (delivery_number,delivery_id)
    REFERENCES relay_recovery_delivery_attempts(delivery_number,delivery_id)
    DEFERRABLE INITIALLY DEFERRED;

DROP INDEX relay_recovery_due;
CREATE INDEX relay_recovery_due
    ON relay_recovery_deliveries(next_attempt_at, delivery_id)
    WHERE recovered_at IS NULL AND provider_succeeded_at IS NULL AND delivery_number IS NOT NULL;
