package databasefixture

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

func TestOpenMatchesFreshProductionDatabase(t *testing.T) {
	ctx := context.Background()
	freshRoot := t.TempDir()
	freshStarted := time.Now()
	fresh, err := database.Open(freshRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	freshDuration := time.Since(freshStarted)

	coldRoot := t.TempDir()
	coldStarted := time.Now()
	cold, err := Open(coldRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	coldDuration := time.Since(coldStarted)

	warmRoot := t.TempDir()
	warmStarted := time.Now()
	warm, err := Open(warmRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	warmDuration := time.Since(warmStarted)

	freshSnapshot, err := snapshotDatabase(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	coldSnapshot, err := snapshotDatabase(ctx, cold)
	if err != nil {
		t.Fatal(err)
	}
	warmSnapshot, err := snapshotDatabase(ctx, warm)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(coldSnapshot, freshSnapshot) {
		t.Fatalf("cold fixture database differs from fresh production database\ncold=%#v\nfresh=%#v", coldSnapshot, freshSnapshot)
	}
	if !reflect.DeepEqual(warmSnapshot, freshSnapshot) {
		t.Fatalf("warm fixture database differs from fresh production database\nwarm=%#v\nfresh=%#v", warmSnapshot, freshSnapshot)
	}

	freshSettings, err := databaseSettings(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	coldSettings, err := databaseSettings(ctx, cold)
	if err != nil {
		t.Fatal(err)
	}
	warmSettings, err := databaseSettings(ctx, warm)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(coldSettings, freshSettings) {
		t.Fatalf("cold fixture settings=%#v, want fresh production settings=%#v", coldSettings, freshSettings)
	}
	if !reflect.DeepEqual(warmSettings, freshSettings) {
		t.Fatalf("warm fixture settings=%#v, want fresh production settings=%#v", warmSettings, freshSettings)
	}
	assertNoTestState(t, cold)
	assertNoTestState(t, warm)
	t.Logf("database fixture setup timing: fresh=%s cold=%s warm=%s", freshDuration, coldDuration, warmDuration)
}

func TestOpenConcurrentClonesAreIndependent(t *testing.T) {
	const clones = 8
	base := t.TempDir()
	start := make(chan struct{})
	errorsByClone := make(chan error, clones)
	var wait sync.WaitGroup
	for index := range clones {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			root := filepath.Join(base, fmt.Sprintf("clone-%d", index))
			db, err := Open(root)
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
				errorsByClone <- fmt.Errorf("clone %d production reopen: %w", index, err)
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
	if builds := controlDatabaseTemplateBuilds.Load(); builds != 1 {
		t.Fatalf("template builds=%d, want one process-wide initialization", builds)
	}
	freshAfterClones, err := Open(filepath.Join(base, "fresh-after-clones"))
	if err != nil {
		t.Fatalf("open fresh clone after concurrent writes: %v", err)
	}
	defer freshAfterClones.Close()
	assertNoTestState(t, freshAfterClones)
}

func TestOpenRefusesExistingControlArtifacts(t *testing.T) {
	for _, name := range controlDatabaseArtifacts() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			original := []byte("existing-" + name)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := Open(root)
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
			if string(got) != string(original) {
				t.Fatalf("existing artifact changed: got %q want %q", got, original)
			}
			if name != controlDatabase {
				if _, statErr := os.Lstat(filepath.Join(root, controlDatabase)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("fixture wrote control database beside existing %s: %v", name, statErr)
				}
			}
		})
	}
}

func TestOpenRejectsControlDatabaseSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "preserved-target.db")
	original := []byte("existing-control-database-target")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, controlDatabase)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink capability unavailable: %v", err)
	}

	db, err := Open(root)
	if db != nil {
		_ = db.Close()
		t.Fatal("fixture open accepted a control database symlink")
	}
	if err == nil {
		t.Fatal("fixture open accepted a control database symlink")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("symlink target changed: got %q want %q", got, original)
	}
	info, statErr := os.Lstat(link)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("fixture replaced the control database symlink")
	}
	for _, name := range controlDatabaseArtifacts()[1:] {
		if _, statErr := os.Lstat(filepath.Join(root, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("fixture created control database artifact beside symlink %s: %v", name, statErr)
		}
	}
}

type databaseSnapshot struct {
	Schema []schemaEntry
	Tables map[string][][]databaseCell
}

type schemaEntry struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

type databaseCell struct {
	Type  string
	Value string
}

func snapshotDatabase(ctx context.Context, db *sql.DB) (databaseSnapshot, error) {
	snapshot := databaseSnapshot{Tables: map[string][][]databaseCell{}}
	objects, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,COALESCE(sql,'')
		FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' OR name = 'sqlite_sequence' ORDER BY type,name,tbl_name`)
	if err != nil {
		return snapshot, err
	}
	defer objects.Close()
	for objects.Next() {
		var entry schemaEntry
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
		columns, err := tableColumns(ctx, db, entry.Name)
		if err != nil {
			return snapshot, err
		}
		query := "SELECT * FROM " + quoteIdentifier(entry.Name)
		if len(columns) > 0 {
			quoted := make([]string, len(columns))
			for index, column := range columns {
				quoted[index] = quoteIdentifier(column)
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
			row := make([]databaseCell, len(values))
			for index, value := range values {
				row[index] = databaseValue(value)
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

func tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
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

func databaseValue(value any) databaseCell {
	switch value := value.(type) {
	case nil:
		return databaseCell{Type: "null"}
	case []byte:
		return databaseCell{Type: "blob", Value: hex.EncodeToString(value)}
	case string:
		return databaseCell{Type: "text", Value: value}
	default:
		return databaseCell{Type: fmt.Sprintf("%T", value), Value: fmt.Sprint(value)}
	}
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

type settings struct {
	ForeignKeys        int
	JournalMode        string
	Synchronous        int
	BusyTimeout        int
	MaxOpenConnections int
}

func databaseSettings(ctx context.Context, db *sql.DB) (settings, error) {
	settings := settings{MaxOpenConnections: db.Stats().MaxOpenConnections}
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

func assertNoTestState(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"users", "applications", "application_sources", "manifest_revisions", "audit_events", "job_events",
		"lan_gateway_profile_revisions", "lan_app_access_revisions", "lan_port_allocations",
		"lan_gateway_upgrade_claims", "lan_gateway_upgrade_claim_events",
		"lan_app_access_disable_intents", "lan_app_access_grant_claims", "lan_app_access_grant_claim_events",
		"lan_app_access_disable_claims", "lan_app_access_disable_claim_events",
		"lan_app_access_disable_clear_acks", "lan_app_access_disable_successor_acks",
		"lan_gateway_rebind_claims", "lan_gateway_rebind_claim_events", "lan_gateway_rebind_roster_entries",
		"lan_gateway_rebind_transition_commands", "lan_gateway_rebind_allocation_transfers",
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + quoteIdentifier(table)).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("fixture %s rows=%d, want no user, app, grant, or history state", table, count)
		}
	}
}
