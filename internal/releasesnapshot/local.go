package releasesnapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const localTreeHashDomain = "hostd/local-release-tree/v1\x00"

type localManifestEntry struct {
	info os.FileInfo
	mode fs.FileMode
	size int64
	dir  bool
}

// MaterializeLocal copies a legacy local source into a retained managed release
// workspace. Docker Compose never observes the mutable user-owned tree.
func (m *Materializer) MaterializeLocal(ctx context.Context, appID, sourcePath string) (Release, error) {
	if m == nil || m.db == nil || !validAppID(appID) || strings.TrimSpace(sourcePath) == "" || pathsecurity.RejectWindowsNamespace(sourcePath) {
		return Release{}, &Error{Code: "invalid_source"}
	}
	unlock := m.locks.lock(appID)
	defer unlock()

	var storedPath, sourceType string
	if err := m.db.QueryRowContext(ctx, `SELECT a.source_path,s.source_type FROM applications a JOIN application_sources s ON s.application_id=a.id WHERE a.id=? AND a.archived_at IS NULL`, appID).Scan(&storedPath, &sourceType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Release{}, &Error{Code: "invalid_source"}
		}
		return Release{}, internal(err)
	}
	if sourceType != "local" || strings.TrimSpace(storedPath) != strings.TrimSpace(sourcePath) {
		return Release{}, &Error{Code: "invalid_source"}
	}
	inspection, err := sourceinspection.InspectLocal(sourcePath)
	if err != nil || inspection.Source.ComposePath == "" || len(inspection.Findings) != 0 {
		return Release{}, &Error{Code: "invalid_source"}
	}
	sourceRoot := inspection.Source.Path
	info, err := os.Lstat(sourceRoot)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || localPathIsReparsePoint(sourceRoot) {
		return Release{}, &Error{Code: "invalid_source"}
	}
	if !info.IsDir() {
		sourceRoot = filepath.Dir(sourceRoot)
	}
	sourceRoot, err = filepath.Abs(sourceRoot)
	if err != nil || pathsecurity.RejectWindowsNamespace(sourceRoot) {
		return Release{}, &Error{Code: "invalid_source"}
	}
	sourceRoot = filepath.Clean(sourceRoot)

	release, err := m.reserveLocal(ctx, appID, inspection.Source.ComposePath)
	if err != nil {
		return Release{}, internal(err)
	}
	staging, err := m.stagingPath(appID, release.ID)
	if err != nil {
		m.finalize(ctx, release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	if err := m.fs.mkdirAll(filepath.Join(staging, "workspace"), 0o700); err != nil {
		_ = m.abort(ctx, appID, release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	digest, manifest, err := copyLocalTree(ctx, sourceRoot, filepath.Join(staging, "workspace"))
	if err == nil && m.afterLocalCopy != nil {
		m.afterLocalCopy()
	}
	if err == nil {
		err = verifyLocalTree(ctx, sourceRoot, manifest, digest)
	}
	if err == nil {
		err = validateComposeWorkspace(filepath.Join(staging, "workspace"), inspection.Source.ComposePath)
	}
	if err != nil {
		code := "invalid_source"
		if errors.Is(err, errTooLarge) {
			code = "source_too_large"
		} else if errors.Is(err, context.Canceled) {
			code = "internal_error"
		}
		if m.abort(ctx, appID, release.ID, code) != nil {
			return Release{}, &Error{Code: "internal_error"}
		}
		return Release{}, &Error{Code: code}
	}

	if existing, lookupErr := m.ready(ctx, appID, 0, digest, inspection.Source.ComposePath, release.ConfigurationRevisionNumber); lookupErr == nil {
		if abortErr := m.abort(ctx, appID, release.ID, "superseded"); abortErr != nil {
			return Release{}, &Error{Code: "internal_error"}
		}
		return existing, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		_ = m.abort(ctx, appID, release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}

	final, err := m.workspacePath(appID, release.ID)
	if err != nil || m.fs.mkdirAll(filepath.Dir(filepath.Dir(final)), 0o700) != nil || m.fs.rename(staging, filepath.Dir(final)) != nil {
		_ = m.abort(ctx, appID, release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	if err := m.markLocalReady(ctx, release.ID, digest, m.workspaceRelative(appID, release.ID)); err != nil {
		if existing, lookupErr := m.ready(ctx, appID, 0, digest, inspection.Source.ComposePath, release.ConfigurationRevisionNumber); lookupErr == nil {
			_ = m.abort(ctx, appID, release.ID, "superseded")
			return existing, nil
		}
		_ = m.abort(ctx, appID, release.ID, "internal_error")
		return Release{}, &Error{Code: "internal_error"}
	}
	release.SourceProvider = "local"
	release.RepositoryID = 0
	release.ResolvedSHA = digest
	release.ArchiveSHA256 = digest
	release.WorkspacePath = final
	release.WorkspaceState = WorkspaceStateReady
	return release, nil
}

func (m *Materializer) reserveLocal(ctx context.Context, appID, composePath string) (Release, error) {
	id, err := randomID()
	if err != nil {
		return Release{}, err
	}
	configurationID, configurationNumber, err := m.currentConfiguration(ctx, appID)
	if err != nil {
		return Release{}, err
	}
	now := m.now().UTC().Format(timeFormat)
	_, err = m.db.ExecContext(ctx, `INSERT INTO releases(id,app_id,source_commit_sha,source_branch,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state,configuration_revision_id,configuration_revision_number) VALUES(?,?, '', '', 'materializing','{}',?,'local',0,?,?, 'materializing',?,?)`, id, appID, now, id, composePath, nullableConfigurationID(configurationID), configurationNumber)
	if err != nil {
		return Release{}, err
	}
	return Release{ID: id, AppID: appID, SourceProvider: "local", ComposePath: composePath, WorkspaceState: WorkspaceStateMaterializing, ConfigurationRevisionID: configurationID, ConfigurationRevisionNumber: configurationNumber}, nil
}

func (m *Materializer) markLocalReady(ctx context.Context, id, digest, workspace string) error {
	result, err := m.db.ExecContext(ctx, `UPDATE releases SET status='ready',source_commit_sha=?,resolved_sha=?,archive_sha256=?,workspace_path=?,workspace_state='ready',materialized_at=? WHERE id=? AND source_provider='local' AND workspace_state='materializing'`, digest, digest, digest, workspace, m.now().UTC().Format(timeFormat), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("local snapshot state changed")
	}
	return nil
}

func copyLocalTree(ctx context.Context, sourceRoot, destinationRoot string) (string, map[string]localManifestEntry, error) {
	manifest := make(map[string]localManifestEntry)
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, localTreeHashDomain)
	var total int64
	entries := 0
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > MaxArchiveEntries {
			return errTooLarge
		}
		relative, err := localRelative(sourceRoot, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || localPathIsReparsePoint(path) {
			return errors.New("unsafe local source entry")
		}
		isRoot := relative == "."
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("unsupported local source entry")
		}
		manifest[relative] = localManifestEntry{info: info, mode: info.Mode().Perm(), size: info.Size(), dir: info.IsDir()}
		if !isRoot {
			writeLocalHashHeader(hasher, relative, info.IsDir(), localStoredMode(info), localStoredSize(info))
		}
		destination := destinationRoot
		if !isRoot {
			destination = filepath.Join(destinationRoot, filepath.FromSlash(relative))
		}
		if info.IsDir() {
			if !isRoot {
				return os.Mkdir(destination, 0o700)
			}
			return nil
		}
		if info.Size() < 0 || info.Size() > MaxFileBytes || total+info.Size() > MaxExtractedBytes {
			return errTooLarge
		}
		total += info.Size()
		return copyLocalFile(ctx, path, destination, info, hasher)
	})
	if err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), manifest, nil
}

func verifyLocalTree(ctx context.Context, sourceRoot string, manifest map[string]localManifestEntry, expected string) error {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, localTreeHashDomain)
	seen := 0
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := localRelative(sourceRoot, path)
		if err != nil {
			return err
		}
		before, ok := manifest[relative]
		if !ok {
			return errors.New("local source changed during materialization")
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || localPathIsReparsePoint(path) || !os.SameFile(before.info, info) || info.IsDir() != before.dir || info.Mode().Perm() != before.mode || info.Size() != before.size || !info.ModTime().Equal(before.info.ModTime()) {
			return errors.New("local source changed during materialization")
		}
		seen++
		if relative == "." {
			return nil
		}
		writeLocalHashHeader(hasher, relative, info.IsDir(), localStoredMode(info), localStoredSize(info))
		if info.IsDir() {
			return nil
		}
		return hashLocalFile(ctx, path, info, hasher)
	})
	if err != nil {
		return err
	}
	if seen != len(manifest) || hex.EncodeToString(hasher.Sum(nil)) != expected {
		return errors.New("local source changed during materialization")
	}
	return nil
}

func hashLocalTree(ctx context.Context, root string) (string, error) {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, localTreeHashDomain)
	entries := 0
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > MaxArchiveEntries {
			return errTooLarge
		}
		relative, err := localRelative(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || localPathIsReparsePoint(path) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("unsafe local release entry")
		}
		if relative == "." {
			return nil
		}
		writeLocalHashHeader(hasher, relative, info.IsDir(), localStoredMode(info), localStoredSize(info))
		if info.IsDir() {
			return nil
		}
		if info.Size() < 0 || info.Size() > MaxFileBytes || total+info.Size() > MaxExtractedBytes {
			return errTooLarge
		}
		total += info.Size()
		return hashLocalFile(ctx, path, info, hasher)
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyLocalFile(ctx context.Context, source, destination string, before os.FileInfo, hasher hash.Hash) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() != before.Size() {
		return errors.New("local source file changed")
	}
	mode := fs.FileMode(0o600)
	if before.Mode().Perm()&0o111 != 0 {
		mode = 0o700
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	written, copyErr := copyContext(ctx, io.MultiWriter(output, hasher), io.LimitReader(input, before.Size()+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil || written != before.Size() {
		return errors.New("local source file changed")
	}
	return nil
}

func hashLocalFile(ctx context.Context, source string, before os.FileInfo, hasher hash.Hash) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() != before.Size() {
		return errors.New("local source file changed")
	}
	written, err := copyContext(ctx, hasher, io.LimitReader(input, before.Size()+1))
	if err != nil || written != before.Size() {
		return errors.New("local source file changed")
	}
	return nil
}

func localRelative(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || pathsecurity.RejectWindowsNamespace(relative) {
		return "", errors.New("unsafe local source path")
	}
	if relative == "." {
		return relative, nil
	}
	canonical := filepath.ToSlash(relative)
	if len(canonical) > MaxPathBytes || strings.Count(canonical, "/")+1 > MaxPathDepth {
		return "", errTooLarge
	}
	for _, segment := range strings.Split(canonical, "/") {
		if len(segment) == 0 || len(segment) > MaxSegmentBytes || !safeSegment(segment) {
			return "", errors.New("unsafe local source path")
		}
	}
	return canonical, nil
}

func writeLocalHashHeader(destination hash.Hash, relative string, directory bool, mode fs.FileMode, size int64) {
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(len(relative)))
	_, _ = destination.Write(number[:])
	_, _ = io.WriteString(destination, relative)
	if directory {
		_, _ = destination.Write([]byte{'d'})
	} else {
		_, _ = destination.Write([]byte{'f'})
	}
	binary.BigEndian.PutUint64(number[:], uint64(mode.Perm()))
	_, _ = destination.Write(number[:])
	binary.BigEndian.PutUint64(number[:], uint64(size))
	_, _ = destination.Write(number[:])
}

func localStoredMode(info os.FileInfo) fs.FileMode {
	if info.IsDir() {
		return 0o700
	}
	if info.Mode().Perm()&0o111 != 0 {
		return 0o700
	}
	return 0o600
}

func localStoredSize(info os.FileInfo) int64 {
	if info.IsDir() {
		return 0
	}
	return info.Size()
}

func nullableConfigurationID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"
