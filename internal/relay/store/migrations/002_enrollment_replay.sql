ALTER TABLE relay_enrollments
    ADD CONSTRAINT relay_enrollment_request_replay
    UNIQUE (controller_id, key_id, request_nonce);
