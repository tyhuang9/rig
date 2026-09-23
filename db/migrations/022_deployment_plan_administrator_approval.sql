CREATE TRIGGER deployment_plan_revision_administrator_insert
BEFORE INSERT ON deployment_plan_revisions
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.revised_by AND u.role = 'administrator'
) OR NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.accepted_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'deployment plan acceptance requires an administrator'); END;

CREATE TRIGGER deployment_plan_migration_administrator_insert
BEFORE INSERT ON deployment_plan_migration_approvals
WHEN NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id = NEW.approved_by AND u.role = 'administrator'
)
BEGIN SELECT RAISE(ABORT, 'deployment plan migration approval requires an administrator'); END;
