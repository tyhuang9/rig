//go:build linux

package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/database"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	modernsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestGeneratedDomainPathsRejectUnreadableAncestor(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	runtimeRoot := filepath.Join(fixture.dataRoot, "runtime")
	if err := os.MkdirAll(filepath.Join(runtimeRoot, "generated-ingress"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtimeRoot, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(runtimeRoot, 0o700); err != nil {
			t.Errorf("restore runtime directory permissions: %v", err)
		}
	})
	if _, err := inspectGeneratedDomainPaths(fixture.dataRoot); err == nil {
		t.Fatal("ingress below an unreadable ancestor was accepted")
	}
}

func TestGeneratedDomainPathsRejectUnreadableDatabase(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	databasePath := filepath.Join(fixture.dataRoot, "control.db")
	if err := os.Chmod(databasePath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(databasePath, 0o600); err != nil {
			t.Errorf("restore database permissions: %v", err)
		}
	})
	if _, err := inspectGeneratedDomainPaths(fixture.dataRoot); err == nil {
		t.Fatal("unreadable control database was accepted")
	}
}

func TestGeneratedDomainPhysicalIdentityRejectsReplacedOpenDatabase(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	var journalMode string
	if err := fixture.database.QueryRow(`PRAGMA journal_mode = DELETE`).Scan(&journalMode); err != nil || journalMode != "delete" {
		t.Fatalf("disable fixture WAL: mode=%q error=%v", journalMode, err)
	}
	databasePath := fixture.dataRoot + string(os.PathSeparator) + "control.db"
	if err := os.Rename(databasePath, databasePath+".open-original"); err != nil {
		t.Fatal(err)
	}
	replacement, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{
		result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon"`)},
	}}}
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, runner, fixture.options); err == nil {
		t.Fatal("sealed database handle remained bound to the renamed original but replacement pathname was accepted")
	}
	if len(runner.requests) != 0 {
		t.Fatal("daemon was inspected before the replaced open database was refused")
	}
}

func TestGeneratedDomainIdentityInspectionPreservesSQLiteProcessLocks(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	var journalMode string
	if err := fixture.database.QueryRow(`PRAGMA journal_mode = DELETE`).Scan(&journalMode); err != nil || journalMode != "delete" {
		t.Fatalf("set DELETE journal mode: mode=%q error=%v", journalMode, err)
	}
	databasePath := filepath.Join(fixture.dataRoot, "control.db")
	holder, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	holder.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = holder.Close() })
	if _, err := holder.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	transactionOpen := true
	t.Cleanup(func() {
		if transactionOpen {
			_, _ = holder.Exec(`ROLLBACK`)
		}
	})
	generatedDomainRunSQLiteLockProbe(t, databasePath, true)
	if _, err := database.GeneratedDomainFileIdentity(context.Background(), fixture.database, fixture.dataRoot); err != nil {
		t.Fatalf("direct provenance inspection: %v", err)
	}
	runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{
		result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon"`)},
	}}}
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, runner, fixture.options); err != nil {
		t.Fatalf("combined identity inspection: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("combined identity daemon requests = %d", len(runner.requests))
	}
	generatedDomainRunSQLiteLockProbe(t, databasePath, true)
	if _, err := holder.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	transactionOpen = false
	generatedDomainRunSQLiteLockProbe(t, databasePath, false)
}

func TestGeneratedDomainSQLiteLockProbeChild(t *testing.T) {
	databasePath := os.Getenv("RIG_GENERATED_DOMAIN_LOCK_PROBE")
	if databasePath == "" {
		return
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`BEGIN IMMEDIATE`)
	expectBusy := os.Getenv("RIG_GENERATED_DOMAIN_EXPECT_BUSY") == "1"
	if expectBusy {
		var sqliteError *modernsqlite.Error
		if !errors.As(err, &sqliteError) || sqliteError == nil ||
			(sqliteError.Code()&0xff != sqlite3.SQLITE_BUSY && sqliteError.Code()&0xff != sqlite3.SQLITE_LOCKED) {
			t.Fatalf("expected SQLite busy/locked result, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("lock remained after release: %v", err)
	}
	if _, err := database.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
}

func generatedDomainRunSQLiteLockProbe(t *testing.T, databasePath string, expectBusy bool) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestGeneratedDomainSQLiteLockProbeChild$")
	command.Env = append(os.Environ(), "RIG_GENERATED_DOMAIN_LOCK_PROBE="+databasePath)
	if expectBusy {
		command.Env = append(command.Env, "RIG_GENERATED_DOMAIN_EXPECT_BUSY=1")
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("SQLite lock probe failed: %v: %s", err, output)
	}
}
