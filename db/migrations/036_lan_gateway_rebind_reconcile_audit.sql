-- Rebind audits are retained evidence of attempted administrator actions. They
-- are never replayable authorization, but later writes must not be able to
-- rewrite or remove their actor, action, bound snapshot, or timestamp.
CREATE TRIGGER lan_gateway_rebind_audit_immutable_update
BEFORE UPDATE ON audit_events
WHEN OLD.resource_type = 'lan_gateway_rebind'
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind audit events are immutable'); END;

CREATE TRIGGER lan_gateway_rebind_audit_retain
BEFORE DELETE ON audit_events
WHEN OLD.resource_type = 'lan_gateway_rebind'
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind audit events are retained'); END;
