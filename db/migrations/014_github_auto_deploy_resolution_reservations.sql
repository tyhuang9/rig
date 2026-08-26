ALTER TABLE github_auto_deploy_heads
ADD COLUMN resolving_generation INTEGER
    CHECK (resolving_generation IS NULL OR resolving_generation >= 0);

ALTER TABLE github_auto_deploy_heads
ADD COLUMN resolving_lease_fence INTEGER
    CHECK (
        (resolving_generation IS NULL AND resolving_lease_fence IS NULL)
        OR (resolving_generation IS NOT NULL AND resolving_lease_fence IS NOT NULL AND resolving_lease_fence > 0)
    );

CREATE TRIGGER github_auto_deploy_resolution_reservation_guard
BEFORE UPDATE OF resolving_generation,resolving_lease_fence ON github_auto_deploy_heads
WHEN NEW.resolving_generation IS NOT NULL
  AND (
      NEW.resolving_lease_fence<>NEW.lease_fence
      OR NEW.lease_token IS NULL
      OR NEW.lease_expires_at IS NULL
  )
BEGIN SELECT RAISE(ABORT,'auto-deploy resolution reservation requires the current lease'); END;

CREATE TRIGGER github_auto_deploy_config_clear_resolution_reservation
AFTER UPDATE OF config_revision ON github_auto_deploy_heads
WHEN NEW.config_revision<>OLD.config_revision AND NEW.resolving_generation IS NOT NULL
BEGIN
    UPDATE github_auto_deploy_heads
    SET resolving_generation=NULL,resolving_lease_fence=NULL
    WHERE application_id=NEW.application_id AND config_revision=NEW.config_revision;
END;

CREATE TRIGGER github_auto_deploy_access_loss_clear_resolution_reservation
AFTER UPDATE OF state,pause_code ON github_auto_deploy_heads
WHEN NEW.state='paused' AND NEW.pause_code='source_access_lost' AND NEW.resolving_generation IS NOT NULL
BEGIN
    UPDATE github_auto_deploy_heads
    SET resolving_generation=NULL,resolving_lease_fence=NULL
    WHERE application_id=NEW.application_id;
END;
