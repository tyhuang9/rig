// Package databasefixture provides isolated, migrated control databases for tests.
package databasefixture

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/hostd/hostd/internal/database"
)

const controlDatabase = "control.db"

var (
	controlDatabaseTemplateOnce   sync.Once
	controlDatabaseTemplate       []byte
	controlDatabaseTemplateErr    error
	controlDatabaseTemplateBuilds atomic.Uint32
)

// Open creates an isolated control database with the current production schema.
// The process-wide template contains only the migrated schema and static migration
// data. Each call writes a private copy before opening it through database.Open.
func Open(dataRoot string) (*sql.DB, error) {
	template, err := controlDatabaseTemplateBytes()
	if err != nil {
		return nil, err
	}
	if err := createEmptyRoot(dataRoot); err != nil {
		return nil, err
	}
	if err := controlDatabaseArtifactsAbsent(dataRoot); err != nil {
		return nil, err
	}

	target := filepath.Join(dataRoot, controlDatabase)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create fixture control database exclusively: %w", err)
	}
	created := true
	defer func() {
		if created {
			_ = file.Close()
			_ = os.Remove(target)
		}
	}()
	if written, err := file.Write(template); err != nil {
		return nil, fmt.Errorf("write fixture control database: %w", err)
	} else if written != len(template) {
		return nil, fmt.Errorf("write fixture control database: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync fixture control database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close fixture control database: %w", err)
	}
	created = false

	// Keep the production connection pragmas and migration-ledger validation.
	db, err := database.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open fixture control database: %w", err)
	}
	return db, nil
}

func controlDatabaseTemplateBytes() ([]byte, error) {
	controlDatabaseTemplateOnce.Do(func() {
		controlDatabaseTemplateBuilds.Add(1)
		root, err := os.MkdirTemp("", "rig-empty-control-database-")
		if err != nil {
			controlDatabaseTemplateErr = fmt.Errorf("create empty fixture database root: %w", err)
			return
		}
		defer func() {
			if removeErr := os.RemoveAll(root); removeErr != nil && controlDatabaseTemplateErr == nil {
				controlDatabaseTemplateErr = fmt.Errorf("remove empty fixture database root: %w", removeErr)
			}
		}()

		db, err := database.Open(root)
		if err != nil {
			controlDatabaseTemplateErr = fmt.Errorf("migrate empty fixture database: %w", err)
			return
		}
		var busy, logFrames, checkpointedFrames int
		if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
			_ = db.Close()
			controlDatabaseTemplateErr = fmt.Errorf("checkpoint empty fixture database: %w", err)
			return
		}
		if busy != 0 {
			_ = db.Close()
			controlDatabaseTemplateErr = fmt.Errorf("checkpoint empty fixture database: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
			return
		}
		if err := db.Close(); err != nil {
			controlDatabaseTemplateErr = fmt.Errorf("close empty fixture database: %w", err)
			return
		}
		if err := controlDatabaseSidecarsAbsent(root); err != nil {
			controlDatabaseTemplateErr = fmt.Errorf("validate checkpointed fixture database: %w", err)
			return
		}

		path := filepath.Join(root, controlDatabase)
		info, err := os.Lstat(path)
		if err != nil {
			controlDatabaseTemplateErr = fmt.Errorf("inspect empty fixture database: %w", err)
			return
		}
		if !info.Mode().IsRegular() {
			controlDatabaseTemplateErr = fmt.Errorf("inspect empty fixture database: control database is not a regular file")
			return
		}
		body, err := os.ReadFile(path)
		if err != nil {
			controlDatabaseTemplateErr = fmt.Errorf("read empty fixture database: %w", err)
			return
		}
		if len(body) == 0 {
			controlDatabaseTemplateErr = errors.New("read empty fixture database: empty template")
			return
		}
		controlDatabaseTemplate = bytes.Clone(body)
	})
	if controlDatabaseTemplateErr != nil {
		return nil, controlDatabaseTemplateErr
	}
	return bytes.Clone(controlDatabaseTemplate), nil
}

func createEmptyRoot(dataRoot string) error {
	info, err := os.Lstat(dataRoot)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dataRoot, 0o700); err != nil {
			return fmt.Errorf("create fixture database root: %w", err)
		}
		info, err = os.Lstat(dataRoot)
	}
	if err != nil {
		return fmt.Errorf("inspect fixture database root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("fixture database root is not a private directory: %s", dataRoot)
	}
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		return fmt.Errorf("read fixture database root: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("fixture database root is not empty: %s", dataRoot)
	}
	return nil
}

func controlDatabaseArtifactsAbsent(dataRoot string) error {
	for _, name := range controlDatabaseArtifacts() {
		path := filepath.Join(dataRoot, name)
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			return fmt.Errorf("fixture control database target already exists: %s", path)
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return fmt.Errorf("inspect fixture control database target %s: %w", path, err)
		}
	}
	return nil
}

func controlDatabaseSidecarsAbsent(dataRoot string) error {
	for _, name := range controlDatabaseArtifacts()[1:] {
		path := filepath.Join(dataRoot, name)
		_, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err == nil:
			return fmt.Errorf("fixture control database sidecar remains after checkpoint: %s", path)
		default:
			return fmt.Errorf("inspect fixture control database sidecar %s: %w", path, err)
		}
	}
	return nil
}

func controlDatabaseArtifacts() []string {
	return []string{
		controlDatabase,
		controlDatabase + "-wal",
		controlDatabase + "-shm",
		controlDatabase + "-journal",
	}
}
