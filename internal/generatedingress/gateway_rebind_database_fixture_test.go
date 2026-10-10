package generatedingress

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

const gatewayRebindFixtureControlDatabase = "control.db"

var (
	gatewayRebindFixtureDatabaseTemplateOnce   sync.Once
	gatewayRebindFixtureDatabaseTemplate       []byte
	gatewayRebindFixtureDatabaseTemplateErr    error
	gatewayRebindFixtureDatabaseTemplateBuilds atomic.Uint32
)

func openGatewayRebindFixtureDatabase(dataRoot string) (*sql.DB, error) {
	template, err := gatewayRebindFixtureDatabaseTemplateBytes()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create fixture database root: %w", err)
	}
	if err := gatewayRebindFixtureDatabaseTargetAbsent(dataRoot); err != nil {
		return nil, err
	}

	target := filepath.Join(dataRoot, gatewayRebindFixtureControlDatabase)
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

	// Preserve the production connection settings and migration-ledger checks.
	db, err := database.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open fixture control database: %w", err)
	}
	return db, nil
}

func gatewayRebindFixtureDatabaseTemplateBytes() ([]byte, error) {
	gatewayRebindFixtureDatabaseTemplateOnce.Do(func() {
		gatewayRebindFixtureDatabaseTemplateBuilds.Add(1)
		root, err := os.MkdirTemp("", "rig-gateway-rebind-empty-control-")
		if err != nil {
			gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("create empty fixture database root: %w", err)
			return
		}
		defer func() {
			if removeErr := os.RemoveAll(root); removeErr != nil && gatewayRebindFixtureDatabaseTemplateErr == nil {
				gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("remove empty fixture database root: %w", removeErr)
			}
		}()

		db, err := database.Open(root)
		if err != nil {
			gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("migrate empty fixture database: %w", err)
			return
		}
		var busy, logFrames, checkpointedFrames int
		if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
			_ = db.Close()
			gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("checkpoint empty fixture database: %w", err)
			return
		}
		if busy != 0 {
			_ = db.Close()
			gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("checkpoint empty fixture database: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
			return
		}
		if err := db.Close(); err != nil {
			gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("close empty fixture database: %w", err)
			return
		}

		body, err := os.ReadFile(filepath.Join(root, gatewayRebindFixtureControlDatabase))
		if err != nil {
			gatewayRebindFixtureDatabaseTemplateErr = fmt.Errorf("read empty fixture database: %w", err)
			return
		}
		if len(body) == 0 {
			gatewayRebindFixtureDatabaseTemplateErr = errors.New("read empty fixture database: empty template")
			return
		}
		gatewayRebindFixtureDatabaseTemplate = bytes.Clone(body)
	})
	if gatewayRebindFixtureDatabaseTemplateErr != nil {
		return nil, gatewayRebindFixtureDatabaseTemplateErr
	}
	return bytes.Clone(gatewayRebindFixtureDatabaseTemplate), nil
}

func gatewayRebindFixtureDatabaseTargetAbsent(dataRoot string) error {
	for _, name := range []string{
		gatewayRebindFixtureControlDatabase,
		gatewayRebindFixtureControlDatabase + "-wal",
		gatewayRebindFixtureControlDatabase + "-shm",
		gatewayRebindFixtureControlDatabase + "-journal",
	} {
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

func TestGatewayRebindDatabaseFixtureMatchesProductionOpen(t *testing.T) {
	ctx := context.Background()
	freshRoot := t.TempDir()
	freshStarted := time.Now()
	fresh, err := database.Open(freshRoot)
	if err != nil {
		t.Fatal(err)
	}
	freshOpen := time.Since(freshStarted)
	t.Cleanup(func() { _ = fresh.Close() })

	fixtureRoot := t.TempDir()
	fixtureStarted := time.Now()
	fixture, err := openGatewayRebindFixtureDatabase(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	fixtureOpen := time.Since(fixtureStarted)
	t.Cleanup(func() { _ = fixture.Close() })
	warmRoot := t.TempDir()
	warmStarted := time.Now()
	warm, err := openGatewayRebindFixtureDatabase(warmRoot)
	if err != nil {
		t.Fatal(err)
	}
	warmOpen := time.Since(warmStarted)
	t.Cleanup(func() { _ = warm.Close() })

	freshSnapshot, err := snapshotGatewayRebindFixtureDatabase(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSnapshot, err := snapshotGatewayRebindFixtureDatabase(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixtureSnapshot, freshSnapshot) {
		t.Fatalf("fixture database differs from a fresh production open\nfixture=%#v\nfresh=%#v", fixtureSnapshot, freshSnapshot)
	}
	warmSnapshot, err := snapshotGatewayRebindFixtureDatabase(ctx, warm)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(warmSnapshot, freshSnapshot) {
		t.Fatalf("warm fixture database differs from a fresh production open\nwarm=%#v\nfresh=%#v", warmSnapshot, freshSnapshot)
	}
	freshSettings, err := readGatewayRebindFixtureDatabaseSettings(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSettings, err := readGatewayRebindFixtureDatabaseSettings(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixtureSettings, freshSettings) {
		t.Fatalf("fixture settings=%#v, want fresh production settings=%#v", fixtureSettings, freshSettings)
	}
	warmSettings, err := readGatewayRebindFixtureDatabaseSettings(ctx, warm)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(warmSettings, freshSettings) {
		t.Fatalf("warm fixture settings=%#v, want fresh production settings=%#v", warmSettings, freshSettings)
	}
	for _, table := range []string{"users", "applications", "lan_gateway_rebind_claims", "lan_app_access_revisions"} {
		var count int
		if err := fixture.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteGatewayRebindFixtureIdentifier(table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("fixture %s rows=%d, want empty migrated baseline", table, count)
		}
	}
	if _, err := fixture.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES('cold-clone','cold-clone','hash','administrator',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	var warmUsers int
	if err := warm.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id='cold-clone'`).Scan(&warmUsers); err != nil {
		t.Fatal(err)
	}
	if warmUsers != 0 {
		t.Fatalf("warm clone observed %d rows written to cold clone", warmUsers)
	}
	t.Logf("database fixture open timing: production=%s template-cold=%s template-warm=%s", freshOpen, fixtureOpen, warmOpen)
}

func TestGatewayRebindDatabaseFixtureConcurrentClonesAreIndependent(t *testing.T) {
	const clones = 8
	base := t.TempDir()
	start := make(chan struct{})
	errorsByClone := make(chan error, clones)
	var wait sync.WaitGroup
	for index := 0; index < clones; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			root := filepath.Join(base, fmt.Sprintf("clone-%d", index))
			db, err := openGatewayRebindFixtureDatabase(root)
			if err != nil {
				errorsByClone <- fmt.Errorf("clone %d open: %w", index, err)
				return
			}
			userID := fmt.Sprintf("fixture-user-%d", index)
			if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
				VALUES(?,?,?,'administrator',datetime('now'),datetime('now'))`, userID, userID, "hash"); err != nil {
				_ = db.Close()
				errorsByClone <- fmt.Errorf("clone %d insert: %w", index, err)
				return
			}
			tx, err := db.Begin()
			if err == nil {
				_, err = tx.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
					VALUES(?,?,?,'administrator',datetime('now'),datetime('now'))`, "rollback-"+userID, "rollback-"+userID, "hash")
				if rollbackErr := tx.Rollback(); err == nil {
					err = rollbackErr
				}
			}
			if err != nil {
				_ = db.Close()
				errorsByClone <- fmt.Errorf("clone %d rollback: %w", index, err)
				return
			}
			if err := db.Close(); err != nil {
				errorsByClone <- fmt.Errorf("clone %d close: %w", index, err)
				return
			}
			reopened, err := database.Open(root)
			if err != nil {
				errorsByClone <- fmt.Errorf("clone %d reopen: %w", index, err)
				return
			}
			defer reopened.Close()
			var committed, rolledBack, total int
			err = reopened.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&committed)
			if err == nil {
				err = reopened.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, "rollback-"+userID).Scan(&rolledBack)
			}
			if err == nil {
				err = reopened.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&total)
			}
			if err != nil {
				errorsByClone <- fmt.Errorf("clone %d verify: %w", index, err)
				return
			}
			if committed != 1 || rolledBack != 0 || total != 1 {
				errorsByClone <- fmt.Errorf("clone %d data committed=%d rolled-back=%d total=%d", index, committed, rolledBack, total)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByClone)
	for err := range errorsByClone {
		t.Error(err)
	}
	if builds := gatewayRebindFixtureDatabaseTemplateBuilds.Load(); builds != 1 {
		t.Fatalf("template builds=%d, want one process-wide initialization", builds)
	}
}

func TestGatewayRebindDatabaseFixtureRefusesExistingControlFiles(t *testing.T) {
	for _, name := range []string{
		gatewayRebindFixtureControlDatabase,
		gatewayRebindFixtureControlDatabase + "-wal",
		gatewayRebindFixtureControlDatabase + "-shm",
		gatewayRebindFixtureControlDatabase + "-journal",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			original := []byte("existing-" + name)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := openGatewayRebindFixtureDatabase(root)
			if db != nil {
				_ = db.Close()
				t.Fatal("fixture open accepted an existing control database artifact")
			}
			if err == nil {
				t.Fatal("fixture open accepted an existing control database artifact")
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("existing artifact changed: got %q want %q", got, original)
			}
			if name != gatewayRebindFixtureControlDatabase {
				if _, statErr := os.Stat(filepath.Join(root, gatewayRebindFixtureControlDatabase)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("fixture wrote control database beside existing %s: %v", name, statErr)
				}
			}
		})
	}
}

type gatewayRebindFixtureDatabaseSnapshot struct {
	Schema []gatewayRebindFixtureSchemaEntry
	Tables map[string][][]gatewayRebindFixtureCell
}

type gatewayRebindFixtureSchemaEntry struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

type gatewayRebindFixtureCell struct {
	Type  string
	Value string
}

func snapshotGatewayRebindFixtureDatabase(ctx context.Context, db *sql.DB) (gatewayRebindFixtureDatabaseSnapshot, error) {
	snapshot := gatewayRebindFixtureDatabaseSnapshot{Tables: map[string][][]gatewayRebindFixtureCell{}}
	objects, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,COALESCE(sql,'')
		FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name,tbl_name`)
	if err != nil {
		return snapshot, err
	}
	defer objects.Close()
	for objects.Next() {
		var entry gatewayRebindFixtureSchemaEntry
		if err := objects.Scan(&entry.Type, &entry.Name, &entry.Table, &entry.SQL); err != nil {
			return snapshot, err
		}
		snapshot.Schema = append(snapshot.Schema, entry)
	}
	if err := objects.Err(); err != nil {
		return snapshot, err
	}
	for _, entry := range snapshot.Schema {
		if entry.Type != "table" {
			continue
		}
		columns, err := gatewayRebindFixtureColumns(ctx, db, entry.Name)
		if err != nil {
			return snapshot, err
		}
		query := "SELECT * FROM " + quoteGatewayRebindFixtureIdentifier(entry.Name)
		if len(columns) > 0 {
			quoted := make([]string, len(columns))
			for index, column := range columns {
				quoted[index] = quoteGatewayRebindFixtureIdentifier(column)
			}
			query += " ORDER BY " + strings.Join(quoted, ",")
		}
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return snapshot, err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				return snapshot, err
			}
			row := make([]gatewayRebindFixtureCell, len(values))
			for index, value := range values {
				row[index] = gatewayRebindFixtureDatabaseCell(value)
				if entry.Name == "schema_migrations" && columns[index] == "applied_at" {
					row[index].Value = "migration-timestamp"
				}
			}
			snapshot.Tables[entry.Name] = append(snapshot.Tables[entry.Name], row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		if err := rows.Close(); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func gatewayRebindFixtureColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteGatewayRebindFixtureIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var ordinal int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func gatewayRebindFixtureDatabaseCell(value any) gatewayRebindFixtureCell {
	switch value := value.(type) {
	case nil:
		return gatewayRebindFixtureCell{Type: "null"}
	case []byte:
		return gatewayRebindFixtureCell{Type: "blob", Value: hex.EncodeToString(value)}
	case string:
		return gatewayRebindFixtureCell{Type: "text", Value: value}
	default:
		return gatewayRebindFixtureCell{Type: fmt.Sprintf("%T", value), Value: fmt.Sprint(value)}
	}
}

func quoteGatewayRebindFixtureIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

type gatewayRebindFixtureDatabaseSettings struct {
	ForeignKeys        int
	JournalMode        string
	Synchronous        int
	BusyTimeout        int
	MaxOpenConnections int
}

func readGatewayRebindFixtureDatabaseSettings(ctx context.Context, db *sql.DB) (gatewayRebindFixtureDatabaseSettings, error) {
	settings := gatewayRebindFixtureDatabaseSettings{MaxOpenConnections: db.Stats().MaxOpenConnections}
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&settings.ForeignKeys); err != nil {
		return settings, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&settings.JournalMode); err != nil {
		return settings, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&settings.Synchronous); err != nil {
		return settings, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&settings.BusyTimeout); err != nil {
		return settings, err
	}
	return settings, nil
}
