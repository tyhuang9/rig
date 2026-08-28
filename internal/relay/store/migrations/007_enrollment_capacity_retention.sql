CREATE INDEX relay_enrollment_active_capacity
    ON relay_enrollments(expires_at, enrollment_id)
    WHERE status IN ('pending','state_claimed');

CREATE INDEX relay_enrollment_terminal_retention
    ON relay_enrollments(completed_at, enrollment_id)
    WHERE status IN ('authorized','failed','expired');
