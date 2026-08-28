package database

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(dataRoot string) (*sql.DB, error) {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataRoot, "control.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Migrate(db *sql.DB) error {
	return migrateFS(db, migrations)
}

func migrateFS(db *sql.DB, migrationFS fs.FS) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, entry.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(body)); err == nil {
			_, err = tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, datetime('now'))`, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
