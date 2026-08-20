// Package releasesnapshot materializes credential-free, immutable GitHub
// commit workspaces below the controller data root.
package releasesnapshot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const (
	WorkspaceStateMaterializing = "materializing"
	WorkspaceStateReady         = "ready"
	WorkspaceStateFailed        = "failed"
)

type Error struct{ Code string }

func (e *Error) Error() string           { return "release snapshot: " + e.Code }
func IsCode(err error, code string) bool { var e *Error; return errors.As(err, &e) && e.Code == code }

type Release struct {
	ID             string
	AppID          string
	RepositoryID   int64
	ResolvedSHA    string
	ComposePath    string
	ArchiveSHA256  string
	WorkspacePath  string
	WorkspaceState string
}

type SourceReader interface {
	Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error)
	ReadTree(context.Context, string, string, int64, string) (githubapp.Tree, error)
	DownloadArchive(context.Context, string, string, int64, string) (io.ReadCloser, error)
}

type Materializer struct {
	db       *sql.DB
	sources  SourceReader
	dataRoot string
	now      func() time.Time
	locks    keyedLocks
}

func New(db *sql.DB, sources SourceReader, dataRoot string) *Materializer {
	return &Materializer{db: db, sources: sources, dataRoot: dataRoot, now: time.Now}
}

// Materialize resolves the app's tracked branch exactly once, then installs an
// archive-backed workspace for that commit. It never materializes local apps.
func (m *Materializer) Materialize(ctx context.Context, owner, appID string) (Release, error) {
	if m == nil || m.db == nil || m.sources == nil || !validAppID(appID) || strings.TrimSpace(owner) == "" {
		return Release{}, &Error{Code: "invalid_source"}
	}
	unlock := m.locks.lock(appID)
	defer unlock()
	source, err := m.appSource(ctx, appID)
	if err != nil {
		return Release{}, internal(err)
	}
	if source.typeName != "github" {
		return Release{}, &Error{Code: "invalid_source"}
	}
	repository, branch, err := m.sources.Resolve(ctx, owner, source.connectionID, source.installationID, source.repositoryID, source.branch)
	if err != nil {
		return Release{}, sourceError(err)
	}
	if repository.ID != source.repositoryID || branch.Name != source.branch {
		return Release{}, &Error{Code: "invalid_source"}
	}
	tree, err := m.sources.ReadTree(ctx, owner, source.connectionID, source.repositoryID, branch.SHA)
	if err != nil {
		return Release{}, sourceError(err)
	}
	if tree.Truncated {
		return Release{}, &Error{Code: "source_too_large"}
	}
	for _, entry := range tree.Entries {
		if entry.Type == "commit" {
			return Release{}, &Error{Code: "invalid_source"}
		}
	}
	if ready, err := m.ready(ctx, appID, source.repositoryID, branch.SHA, source.composePath); err == nil {
		return ready, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Release{}, internal(err)
	}
	release, err := m.reserve(ctx, appID, source, repository, branch)
	if err != nil {
		return Release{}, internal(err)
	}
	staging, err := m.stagingPath(appID, release.ID)
	if err != nil {
		m.fail(context.Background(), release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		m.fail(context.Background(), release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	defer func() {
		if release.WorkspaceState != WorkspaceStateReady {
			_ = m.removeStaging(appID, release.ID)
		}
	}()
	body, err := m.sources.DownloadArchive(ctx, owner, source.connectionID, source.repositoryID, branch.SHA)
	if err != nil {
		m.fail(context.Background(), release.ID, sourceError(err).(*Error).Code)
		return Release{}, sourceError(err)
	}
	archivePath := filepath.Join(staging, "source.tar.gz")
	hash, err := downloadArchive(ctx, body, archivePath)
	if err != nil {
		code := archiveError(err)
		m.fail(context.Background(), release.ID, code)
		return Release{}, &Error{Code: code}
	}
	workspace := filepath.Join(staging, "workspace")
	if err := extractArchive(ctx, archivePath, workspace); err != nil {
		code := archiveError(err)
		m.fail(context.Background(), release.ID, code)
		return Release{}, &Error{Code: code}
	}
	if err := validateComposeWorkspace(workspace, source.composePath); err != nil {
		code := archiveError(err)
		m.fail(context.Background(), release.ID, code)
		return Release{}, &Error{Code: code}
	}
	final, err := m.workspacePath(appID, release.ID)
	if err != nil {
		m.fail(context.Background(), release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		m.fail(context.Background(), release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	if err := os.Rename(workspace, final); err != nil {
		m.fail(context.Background(), release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	if err := m.markReady(ctx, release.ID, hash, m.workspaceRelative(appID, release.ID)); err != nil {
		_ = m.removeWorkspace(appID, release.ID)
		if existing, lookupErr := m.ready(ctx, appID, source.repositoryID, branch.SHA, source.composePath); lookupErr == nil {
			return existing, nil
		}
		m.fail(context.Background(), release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	release.ArchiveSHA256, release.WorkspacePath, release.WorkspaceState = hash, final, WorkspaceStateReady
	return release, nil
}

func (m *Materializer) Recover() error {
	rows, err := m.db.Query(`SELECT id, app_id FROM releases WHERE workspace_state=?`, WorkspaceStateMaterializing)
	if err != nil {
		return err
	}
	var interrupted []Release
	for rows.Next() {
		var id, app string
		if err := rows.Scan(&id, &app); err != nil {
			return err
		}
		if !validID(id) || !validAppID(app) {
			return errors.New("invalid materialization row")
		}
		interrupted = append(interrupted, Release{ID: id, AppID: app})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, release := range interrupted {
		id, app := release.ID, release.AppID
		_ = m.removeStaging(app, id)
		_ = m.removeWorkspace(app, id)
		if err := m.fail(context.Background(), id, "internal_error"); err != nil {
			return err
		}
	}
	return nil
}

type appSource struct {
	typeName, connectionID, branch, composePath string
	installationID, repositoryID                int64
}

func (m *Materializer) appSource(ctx context.Context, appID string) (appSource, error) {
	var s appSource
	err := m.db.QueryRowContext(ctx, `SELECT source_type,COALESCE(connection_id,''),COALESCE(installation_id,0),COALESCE(repository_id,0),COALESCE(tracked_branch,''),COALESCE(compose_path,'') FROM application_sources WHERE application_id=?`, appID).Scan(&s.typeName, &s.connectionID, &s.installationID, &s.repositoryID, &s.branch, &s.composePath)
	return s, err
}
func (m *Materializer) ready(ctx context.Context, app string, repo int64, sha, compose string) (Release, error) {
	var r Release
	err := m.db.QueryRowContext(ctx, `SELECT id,app_id,repository_id,resolved_sha,compose_path,COALESCE(archive_sha256,''),COALESCE(workspace_path,''),workspace_state FROM releases WHERE app_id=? AND repository_id=? AND resolved_sha=? AND compose_path=? AND workspace_state='ready'`, app, repo, sha, compose).Scan(&r.ID, &r.AppID, &r.RepositoryID, &r.ResolvedSHA, &r.ComposePath, &r.ArchiveSHA256, &r.WorkspacePath, &r.WorkspaceState)
	if err != nil {
		return r, err
	}
	expected, pathErr := m.workspacePath(app, r.ID)
	if pathErr != nil || r.WorkspacePath != m.workspaceRelative(app, r.ID) || !safeWorkspace(expected, compose) {
		_, _ = m.db.ExecContext(ctx, `UPDATE releases SET status='failed',workspace_state='failed',materialization_error_code='invalid_source' WHERE id=? AND workspace_state='ready'`, r.ID)
		return Release{}, sql.ErrNoRows
	}
	r.WorkspacePath = expected
	return r, nil
}
func (m *Materializer) reserve(ctx context.Context, app string, source appSource, repository sourceconnections.SourceRepository, branch sourceconnections.Branch) (Release, error) {
	id, err := randomID()
	if err != nil {
		return Release{}, err
	}
	now := m.now().UTC().Format(time.RFC3339Nano)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO releases(id,app_id,source_commit_sha,source_branch,status,metadata_json,created_at,source_provider,repository_id,repository_owner,repository_name,tracked_ref,resolved_sha,compose_path,workspace_state) VALUES(?,?,?,?,'materializing','{}',?,'github',?,?,?,?,?,?,?)`, id, app, branch.SHA, branch.Name, now, repository.ID, repository.Owner, repository.Name, "refs/heads/"+branch.Name, branch.SHA, source.composePath, WorkspaceStateMaterializing)
	if err != nil {
		return Release{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE application_sources SET repository_owner=?,repository_name=?,resolved_sha=?,updated_at=? WHERE application_id=?`, repository.Owner, repository.Name, branch.SHA, now, app)
	if err != nil {
		return Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return Release{}, err
	}
	return Release{ID: id, AppID: app, RepositoryID: repository.ID, ResolvedSHA: branch.SHA, ComposePath: source.composePath, WorkspaceState: WorkspaceStateMaterializing}, nil
}
func (m *Materializer) markReady(ctx context.Context, id, hash, workspace string) error {
	r, err := m.db.ExecContext(ctx, `UPDATE releases SET status='ready',archive_sha256=?,workspace_path=?,workspace_state='ready',materialized_at=? WHERE id=? AND workspace_state='materializing'`, hash, workspace, m.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("snapshot state changed")
	}
	return nil
}
func (m *Materializer) fail(ctx context.Context, id, code string) error {
	_, err := m.db.ExecContext(ctx, `UPDATE releases SET status='failed',workspace_state='failed',materialization_error_code=? WHERE id=? AND workspace_state='materializing'`, code, id)
	return err
}
func (m *Materializer) workspacePath(app, id string) (string, error) {
	return managedPath(m.dataRoot, app, id, "releases")
}
func (m *Materializer) workspaceRelative(app, id string) string {
	return filepath.ToSlash(filepath.Join("apps", app, "releases", id))
}
func (m *Materializer) stagingPath(app, id string) (string, error) {
	return managedPath(m.dataRoot, app, id, ".staging")
}
func (m *Materializer) removeStaging(app, id string) error {
	p, err := m.stagingPath(app, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(p)
}
func (m *Materializer) removeWorkspace(app, id string) error {
	p, err := m.workspacePath(app, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(p)
}
func managedPath(root, app, id, kind string) (string, error) {
	if !validAppID(app) || !validID(id) || (kind != "releases" && kind != ".staging") {
		return "", errors.New("invalid managed path")
	}
	return filepath.Join(root, "apps", app, kind, id), nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func validID(v string) bool {
	if len(v) != 32 {
		return false
	}
	for _, c := range []byte(v) {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func validAppID(v string) bool {
	if validID(v) {
		return true
	}
	if len(v) != 36 {
		return false
	}
	for i, c := range []byte(v) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func internal(error) error { return &Error{Code: "internal_error"} }
func sourceError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "canceled"}
	}
	for _, code := range []string{"invalid_source", "source_access_lost", "provider_unavailable", "source_too_large"} {
		if sourceconnections.IsCode(err, code) {
			return &Error{Code: code}
		}
	}
	return &Error{Code: "provider_unavailable"}
}
func archiveError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "invalid_source"
}
func safeWorkspace(workspace, compose string) bool {
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	info, err = os.Lstat(filepath.Join(workspace, filepath.FromSlash(compose)))
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

type keyedLocks struct {
	mu     sync.Mutex
	values map[string]*sync.Mutex
}

func (k *keyedLocks) lock(id string) func() {
	k.mu.Lock()
	if k.values == nil {
		k.values = map[string]*sync.Mutex{}
	}
	v := k.values[id]
	if v == nil {
		v = &sync.Mutex{}
		k.values[id] = v
	}
	k.mu.Unlock()
	v.Lock()
	return v.Unlock
}
