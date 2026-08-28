ALTER TABLE releases ADD COLUMN workspace_tree_sha256 TEXT CHECK (
    workspace_tree_sha256 IS NULL OR (
        length(workspace_tree_sha256) = 64
        AND workspace_tree_sha256 NOT GLOB '*[^0-9a-f]*'
    )
);

UPDATE releases
SET workspace_tree_sha256 = archive_sha256
WHERE source_provider = 'local'
  AND workspace_state = 'ready'
  AND archive_sha256 IS NOT NULL
  AND length(archive_sha256) = 64
  AND archive_sha256 NOT GLOB '*[^0-9a-f]*';

CREATE TRIGGER releases_ready_requires_workspace_tree_digest
BEFORE UPDATE OF workspace_state, workspace_tree_sha256 ON releases
WHEN NEW.workspace_state = 'ready'
 AND (
    NEW.workspace_tree_sha256 IS NULL
    OR length(NEW.workspace_tree_sha256) != 64
    OR NEW.workspace_tree_sha256 GLOB '*[^0-9a-f]*'
 )
BEGIN SELECT RAISE(ABORT, 'ready workspace tree digest required'); END;

CREATE TRIGGER releases_ready_insert_requires_workspace_tree_digest
BEFORE INSERT ON releases
WHEN NEW.workspace_state = 'ready'
 AND (
    NEW.workspace_tree_sha256 IS NULL
    OR length(NEW.workspace_tree_sha256) != 64
    OR NEW.workspace_tree_sha256 GLOB '*[^0-9a-f]*'
 )
BEGIN SELECT RAISE(ABORT, 'ready workspace tree digest required'); END;

CREATE TRIGGER releases_workspace_tree_digest_immutable
BEFORE UPDATE OF workspace_tree_sha256 ON releases
WHEN OLD.workspace_tree_sha256 IS NOT NULL
 AND NEW.workspace_tree_sha256 IS NOT OLD.workspace_tree_sha256
BEGIN SELECT RAISE(ABORT, 'workspace tree digest is immutable'); END;
