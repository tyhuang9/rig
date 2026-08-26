DROP TRIGGER relay_session_status_transition;

CREATE TRIGGER relay_session_status_transition BEFORE UPDATE OF epoch,state ON relay_controller_session_state
WHEN (NEW.epoch=OLD.epoch AND NOT (
        OLD.state=NEW.state OR
        (OLD.state='disconnected' AND NEW.state IN ('connecting','needs_attention','stopped')) OR
        (OLD.state='connecting' AND NEW.state IN ('authenticating','backoff','needs_attention','stopped')) OR
        (OLD.state='authenticating' AND NEW.state IN ('ready','backoff','needs_attention','stopped')) OR
        (OLD.state='ready' AND NEW.state IN ('disconnected','backoff','needs_attention','stopped')) OR
        (OLD.state='backoff' AND NEW.state IN ('connecting','needs_attention','stopped')) OR
        (OLD.state='needs_attention' AND NEW.state IN ('connecting','stopped')) OR
        (OLD.state='stopped' AND NEW.state='stopped')
    )) OR (NEW.epoch=OLD.epoch+1 AND NEW.state NOT IN ('disconnected','connecting','stopped')) OR NEW.epoch>OLD.epoch+1
BEGIN SELECT RAISE(ABORT,'invalid relay session status transition'); END;
