package releasesnapshot

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
)

var (
	retentionLocks      keyedLocks
	errWorkspaceMissing = errors.New("release workspace missing")
)

type retainedWorkspace struct {
	id, appID, storedPath, state, createdAt string
	size                                    int64
}

// enforceRetention prunes only validated, unprotected retained workspaces. An
// incoming size is accounted before its materializing release becomes ready.
func (m *Materializer) enforceRetention(ctx context.Context, appID string, incoming int64) error {
	unlock := retentionLocks.lock(m.dataRoot)
	defer unlock()
	return m.enforceRetentionLocked(ctx, appID, incoming)
}

func (m *Materializer) admitRetention(ctx context.Context, appID string, incoming int64, install func() error) error {
	unlock := retentionLocks.lock(m.dataRoot)
	defer unlock()
	if err := m.enforceRetentionLocked(ctx, appID, incoming); err != nil {
		return err
	}
	return install()
}

func (m *Materializer) enforceRetentionLocked(ctx context.Context, appID string, incoming int64) error {
	if incoming < 0 || incoming > m.retention.PerAppBytes || incoming > m.retention.GlobalBytes {
		return &Error{Code: ErrorCodeSourceStorageFull}
	}
	if err := m.recoverPruning(ctx); err != nil {
		return internal(err)
	}
	if err := m.backfillWorkspaceSizes(ctx); err != nil {
		return internal(err)
	}
	skipped := make(map[string]bool)
	for {
		candidate, needed, err := m.selectPruneCandidate(ctx, appID, incoming, skipped)
		if err != nil {
			return internal(err)
		}
		if !needed {
			return nil
		}
		if candidate.id == "" {
			return &Error{Code: ErrorCodeSourceStorageFull}
		}
		if _, err := m.workspaceLogicalSize(candidate); err != nil {
			skipped[candidate.id] = true
			continue
		}
		marked, err := m.markPruning(ctx, candidate)
		if err != nil {
			return internal(err)
		}
		if !marked {
			continue
		}
		if err := m.removePruningWorkspace(ctx, candidate); err != nil {
			return internal(err)
		}
	}
}

func (m *Materializer) backfillWorkspaceSizes(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `SELECT id,app_id,workspace_path,workspace_state,created_at FROM releases WHERE workspace_state IN ('ready','failed') AND COALESCE(workspace_path,'')<>'' AND workspace_size_bytes IS NULL ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	var workspaces []retainedWorkspace
	for rows.Next() {
		var workspace retainedWorkspace
		if err := rows.Scan(&workspace.id, &workspace.appID, &workspace.storedPath, &workspace.state, &workspace.createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, workspace := range workspaces {
		size, inspectErr := m.workspaceLogicalSize(workspace)
		if inspectErr != nil {
			// Unsafe or missing legacy workspaces remain unknown and therefore
			// count as unprunable during quota admission.
			continue
		}
		if _, err := m.db.ExecContext(ctx, `UPDATE releases SET workspace_size_bytes=? WHERE id=? AND app_id=? AND workspace_state=? AND workspace_size_bytes IS NULL`, size, workspace.id, workspace.appID, workspace.state); err != nil {
			return err
		}
	}
	return nil
}

func (m *Materializer) selectPruneCandidate(ctx context.Context, appID string, incoming int64, skipped map[string]bool) (retainedWorkspace, bool, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return retainedWorkspace{}, false, err
	}
	defer tx.Rollback()
	var appUsage, globalUsage int64
	var unknown int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN app_id=? THEN workspace_size_bytes ELSE 0 END),0),COALESCE(SUM(workspace_size_bytes),0),COALESCE(SUM(CASE WHEN workspace_size_bytes IS NULL THEN 1 ELSE 0 END),0) FROM releases WHERE workspace_state IN ('ready','failed','pruning') AND COALESCE(workspace_path,'')<>''`, appID).Scan(&appUsage, &globalUsage, &unknown); err != nil {
		return retainedWorkspace{}, false, err
	}
	appNeeded := exceedsQuota(appUsage, incoming, m.retention.PerAppBytes)
	globalNeeded := exceedsQuota(globalUsage, incoming, m.retention.GlobalBytes)
	if !appNeeded && !globalNeeded && unknown == 0 {
		if err := tx.Commit(); err != nil {
			return retainedWorkspace{}, false, err
		}
		return retainedWorkspace{}, false, nil
	}
	protected, err := protectedReleaseIDs(ctx, tx)
	if err != nil {
		return retainedWorkspace{}, false, err
	}
	query := `SELECT id,app_id,workspace_path,workspace_state,workspace_size_bytes,created_at FROM releases WHERE workspace_state IN ('failed','ready') AND COALESCE(workspace_path,'')<>'' AND workspace_size_bytes IS NOT NULL`
	args := []any{}
	if appNeeded {
		query += ` AND app_id=?`
		args = append(args, appID)
	}
	query += ` ORDER BY created_at,id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return retainedWorkspace{}, false, err
	}
	var candidate retainedWorkspace
	for rows.Next() {
		var value retainedWorkspace
		if err := rows.Scan(&value.id, &value.appID, &value.storedPath, &value.state, &value.size, &value.createdAt); err != nil {
			_ = rows.Close()
			return retainedWorkspace{}, false, err
		}
		if !protected[value.id] && !skipped[value.id] {
			candidate = value
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return retainedWorkspace{}, false, err
	}
	if err := rows.Close(); err != nil {
		return retainedWorkspace{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return retainedWorkspace{}, false, err
	}
	return candidate, true, nil
}

func exceedsQuota(current, incoming, quota int64) bool {
	return current > quota || incoming > quota-current
}

func protectedReleaseIDs(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	protected := make(map[string]bool)
	queries := []string{
		`SELECT DISTINCT release_id FROM deployments WHERE release_id IS NOT NULL AND status IN ('preparing','applying','waiting_health','needs_attention')`,
		`SELECT release_id FROM (SELECT d.app_id,d.release_id,ROW_NUMBER() OVER (PARTITION BY d.app_id ORDER BY COALESCE(j.created_at,d.started_at,'') DESC,d.id DESC) AS position FROM deployments d LEFT JOIN jobs j ON j.id=d.job_id WHERE d.release_id IS NOT NULL) WHERE position=1`,
		`SELECT release_id FROM (SELECT app_id,release_id,ROW_NUMBER() OVER (PARTITION BY app_id ORDER BY COALESCE(finished_at,started_at,'') DESC,id DESC) AS position FROM deployments WHERE release_id IS NOT NULL AND status='succeeded') WHERE position<=2`,
		`SELECT json_extract(input_json,'$.releaseId') FROM jobs WHERE status IN ('queued','assigned','running','waiting_external','waiting_user','needs_attention') AND CASE WHEN json_valid(input_json) THEN COALESCE(json_type(input_json,'$.releaseId'),'')='text' ELSE 0 END`,
	}
	for _, query := range queries {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if validID(id) {
				protected[id] = true
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return protected, nil
}

func (m *Materializer) markPruning(ctx context.Context, workspace retainedWorkspace) (bool, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	protected, err := protectedReleaseIDs(ctx, tx)
	if err != nil {
		return false, err
	}
	if protected[workspace.id] {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE releases SET workspace_state='pruning',workspace_prune_from_state=? WHERE id=? AND app_id=? AND workspace_state=? AND workspace_path=? AND workspace_size_bytes=?`, workspace.state, workspace.id, workspace.appID, workspace.state, workspace.storedPath, workspace.size)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (m *Materializer) removePruningWorkspace(ctx context.Context, workspace retainedWorkspace) error {
	if _, err := m.workspaceLogicalSize(workspace); err != nil {
		_, restoreErr := m.db.ExecContext(ctx, `UPDATE releases SET workspace_state=workspace_prune_from_state,workspace_prune_from_state=NULL WHERE id=? AND app_id=? AND workspace_state='pruning'`, workspace.id, workspace.appID)
		if restoreErr != nil {
			return restoreErr
		}
		return errors.New("unsafe retained workspace")
	}
	path, _ := m.workspacePath(workspace.appID, workspace.id)
	if err := m.fs.removeAll(path); err != nil {
		return errors.New("remove retained workspace")
	}
	return m.completePruning(ctx, workspace)
}

func (m *Materializer) recoverPruning(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `SELECT id,app_id,workspace_path,workspace_prune_from_state,COALESCE(workspace_size_bytes,0),created_at FROM releases WHERE workspace_state='pruning' ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	var workspaces []retainedWorkspace
	for rows.Next() {
		var workspace retainedWorkspace
		if err := rows.Scan(&workspace.id, &workspace.appID, &workspace.storedPath, &workspace.state, &workspace.size, &workspace.createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, workspace := range workspaces {
		path, pathErr := m.workspacePath(workspace.appID, workspace.id)
		if pathErr != nil || workspace.storedPath != m.workspaceRelative(workspace.appID, workspace.id) {
			return errors.New("unsafe interrupted pruning state")
		}
		if _, statErr := os.Lstat(path); errors.Is(statErr, fs.ErrNotExist) {
			if err := m.completePruning(ctx, workspace); err != nil {
				return err
			}
			continue
		}
		if workspace.state != WorkspaceStateReady && workspace.state != WorkspaceStateFailed {
			return errors.New("invalid interrupted pruning state")
		}
		if err := m.removePruningWorkspace(ctx, workspace); err != nil {
			return err
		}
	}
	return nil
}

func (m *Materializer) completePruning(ctx context.Context, workspace retainedWorkspace) error {
	result, err := m.db.ExecContext(ctx, `UPDATE releases SET workspace_state='pruned',workspace_path=NULL,workspace_size_bytes=0,workspace_prune_from_state=NULL,workspace_pruned_at=? WHERE id=? AND app_id=? AND workspace_state='pruning'`, m.now().UTC().Format(timeFormat), workspace.id, workspace.appID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("pruning state changed")
	}
	return nil
}

func (m *Materializer) workspaceLogicalSize(workspace retainedWorkspace) (int64, error) {
	if !validAppID(workspace.appID) || !validID(workspace.id) || workspace.storedPath != m.workspaceRelative(workspace.appID, workspace.id) {
		return 0, errors.New("unsafe retained workspace")
	}
	expected, err := m.workspacePath(workspace.appID, workspace.id)
	if err != nil || !m.managedWorkspaceComponentsSafe(expected) {
		return 0, errors.New("unsafe retained workspace")
	}
	return logicalTreeSize(expected)
}

func (m *Materializer) stagingWorkspaceSize(appID, releaseID string) (int64, error) {
	staging, err := m.stagingPath(appID, releaseID)
	if err != nil {
		return 0, err
	}
	workspace := filepath.Join(staging, "workspace")
	if !m.managedWorkspaceComponentsSafe(workspace) {
		return 0, errors.New("unsafe staged workspace")
	}
	return logicalTreeSize(workspace)
}

func logicalTreeSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || localPathIsReparsePoint(path) {
			return errors.New("unsafe retained workspace")
		}
		info, err := os.Lstat(path)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("unsafe retained workspace")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !sameFilesystemPath(resolved, path) {
			return errors.New("unsafe retained workspace")
		}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || size > math.MaxInt64-info.Size() {
				return errors.New("invalid retained workspace size")
			}
			size += info.Size()
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, errWorkspaceMissing
	}
	return size, err
}

func (m *Materializer) managedWorkspaceComponentsSafe(workspace string) bool {
	relative, err := filepath.Rel(m.dataRoot, workspace)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	current := m.dataRoot
	parts := append([]string{"."}, strings.Split(relative, string(filepath.Separator))...)
	for _, part := range parts {
		if part != "." {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || localPathIsReparsePoint(current) {
			return false
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil || !sameFilesystemPath(resolved, current) {
			return false
		}
	}
	return true
}
