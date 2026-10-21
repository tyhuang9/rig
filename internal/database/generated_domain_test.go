//go:build linux || windows

package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"weak"

	"github.com/hostd/hostd/internal/controllerowner"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestOpenForGeneratedDomainCreatesInitializedProvenDatabase(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	identity, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot)
	if err != nil || identity == nil || !identity.Mode().IsRegular() {
		t.Fatalf("generated-domain identity = %v, %v", identity, err)
	}
	assertGeneratedDomainDatabaseInitialization(t, database)
	var migrations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil || migrations == 0 {
		t.Fatalf("schema migrations = %d, %v", migrations, err)
	}
	var rejected int
	if err := database.QueryRow(`SELECT ` + gatewayRebindGuardFunction + `('', '', '', '', '', '', '', '', '', '', '', '', '')`).Scan(&rejected); err != nil || rejected != 0 {
		t.Fatalf("registered gateway rebind guard = %d, %v", rejected, err)
	}
}

func TestOpenForGeneratedDomainPreservesExistingDatabaseHistory(t *testing.T) {
	for _, startingMode := range []string{"DELETE", "WAL"} {
		t.Run(strings.ToLower(startingMode), func(t *testing.T) {
			dataRoot := filepath.Join(t.TempDir(), "state")
			existing, err := Open(dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := existing.Exec(`CREATE TABLE generated_domain_history(id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if _, err := existing.Exec(`INSERT INTO generated_domain_history(id,value) VALUES(1,'preserved')`); err != nil {
				t.Fatal(err)
			}
			var mode string
			if err := existing.QueryRow(`PRAGMA journal_mode = ` + startingMode).Scan(&mode); err != nil || !strings.EqualFold(mode, startingMode) {
				t.Fatalf("seed journal mode = %q, %v", mode, err)
			}
			before := generatedDomainMigrationVersions(t, existing)
			if err := existing.Close(); err != nil {
				t.Fatal(err)
			}
			directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lease.Close() })
			database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			var value string
			if err := database.QueryRow(`SELECT value FROM generated_domain_history WHERE id=1`).Scan(&value); err != nil || value != "preserved" {
				t.Fatalf("preserved row = %q, %v", value, err)
			}
			if after := generatedDomainMigrationVersions(t, database); !reflect.DeepEqual(before, after) {
				t.Fatalf("migration history changed: before=%v after=%v", before, after)
			}
			assertGeneratedDomainDatabaseInitialization(t, database)
			beforeLookup := generatedDomainDatabaseSnapshot(t, database)
			if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot); err != nil {
				t.Fatal(err)
			}
			if afterLookup := generatedDomainDatabaseSnapshot(t, database); !reflect.DeepEqual(beforeLookup, afterLookup) {
				t.Fatalf("read-only provenance lookup changed database: before=%v after=%v", beforeLookup, afterLookup)
			}
		})
	}
}

func TestGeneratedDomainDatabaseProvenanceRefusesUnprovedOrChangedLifetime(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	generic, err := Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), generic, dataRoot); err == nil {
		t.Fatal("generic database acquired generated-domain provenance")
	}
	if err := generic.Close(); err != nil {
		t.Fatal(err)
	}
	var zero sql.DB
	if _, err := GeneratedDomainFileIdentity(context.Background(), &zero, dataRoot); err == nil {
		t.Fatal("zero database acquired generated-domain provenance")
	}

	database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot+"-foreign"); err == nil {
		t.Fatal("proven database validated for a foreign root")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot); err == nil {
		t.Fatal("closed database retained usable provenance")
	}
}

func TestGeneratedDomainDatabaseProvenanceRequiresLiveOwnerLease(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot); err == nil {
		t.Fatal("database provenance survived owner lease release")
	}
}

func TestGeneratedDomainDatabaseProvenanceChecksRawConnection(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	first, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	firstKey, secondKey := weak.Make(first), weak.Make(second)
	generatedDomainProvenances.Lock()
	firstProof := generatedDomainProvenances.values[firstKey]
	secondProof := generatedDomainProvenances.values[secondKey]
	generatedDomainProvenances.values[secondKey] = firstProof
	generatedDomainProvenances.Unlock()
	t.Cleanup(func() {
		generatedDomainProvenances.Lock()
		generatedDomainProvenances.values[secondKey] = secondProof
		generatedDomainProvenances.Unlock()
	})
	if _, err := GeneratedDomainFileIdentity(context.Background(), second, dataRoot); err == nil {
		t.Fatal("provenance associated with a different real connection was accepted")
	}
}

func TestGeneratedDomainConnectorPermanentlyRefusesReconnectAndDriverOpen(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	capture, err := captureGeneratedDomainDatabase(dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.close() })
	pending, err := connectGeneratedDomainDatabase(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pending.database.Close() })
	if pending.connector.connection == nil || strings.ContainsAny(pending.connector.dsn, "?#") || pending.connector.dsn != filepath.Join(dataRoot, generatedDomainDatabaseFilename) {
		t.Fatalf("connector state is not fixed: dsn=%q connection=%v", pending.connector.dsn, pending.connector.connection)
	}
	if connection, err := pending.connector.Connect(context.Background()); connection != nil || !errors.Is(err, errGeneratedDomainConnectionSpent) {
		t.Fatalf("second Connect = %v, %v", connection, err)
	}
	if connection, err := pending.connector.Driver().Open(pending.connector.dsn); connection != nil || !errors.Is(err, errGeneratedDomainConnectionSpent) {
		t.Fatalf("Driver.Open bypass = %v, %v", connection, err)
	}
	if err := pending.database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pending.validateConnection(context.Background()); err == nil {
		t.Fatal("closed database silently reconnected")
	}
}

func TestGeneratedDomainDatabasePoolCannotReconnectAfterBadConnection(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("discard raw connection = %v", err)
	}
	if err := connection.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal(err)
	}
	var value int
	if err := database.QueryRow(`SELECT 1`).Scan(&value); err == nil {
		t.Fatal("database/sql silently reconnected after ErrBadConn")
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot); err == nil {
		t.Fatal("discarded database connection retained usable provenance")
	}
}

func TestConnectGeneratedDomainDatabaseRejectsMutableCapturePath(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	capture, err := captureGeneratedDomainDatabase(dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.close() })
	capture.databasePath = capture.databasePath + "?mode=memory"
	if pending, err := connectGeneratedDomainDatabase(context.Background(), capture); pending != nil || err == nil {
		t.Fatalf("mutable capture DSN = %v, %v", pending, err)
	}
}

func TestGeneratedDomainInitializationCancellationPublishesNoProvenance(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	capture, err := captureGeneratedDomainDatabase(dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.close() })
	pending, err := connectGeneratedDomainDatabase(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pending.database.Close() })
	if err := pending.bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pending.initialize(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled initialization = %v", err)
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), pending.database, dataRoot); err == nil {
		t.Fatal("cancelled initialization published provenance")
	}
}

func TestOpenForGeneratedDomainSerializesConcurrentCaptureAndDriverOpen(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	start := make(chan struct{})
	type result struct {
		database *sql.DB
		err      error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
			results <- result{database: database, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result.err != nil {
			if result.database != nil {
				t.Fatalf("refused concurrent open returned database: %v, %v", result.database, result.err)
			}
			continue
		}
		if result.database == nil {
			t.Fatal("successful concurrent open returned nil database")
		}
		succeeded++
		if _, err := GeneratedDomainFileIdentity(context.Background(), result.database, dataRoot); err != nil {
			t.Fatalf("concurrent provenance: %v", err)
		}
		if err := result.database.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if succeeded == 0 {
		t.Fatal("all serialized opener attempts failed")
	}
}

func TestOpenForGeneratedDomainConstructionGateHonorsCancellation(t *testing.T) {
	if err := acquireGeneratedDomainConstruction(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer releaseGeneratedDomainConstruction()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := acquireGeneratedDomainConstruction(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked construction cancellation = %v", err)
	}
}

func TestOpenForGeneratedDomainRejectsInvalidLeaseAndCancellation(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	foreignRoot := filepath.Join(t.TempDir(), "foreign")
	if err := os.Mkdir(foreignRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignDirectories, err := runtimedocker.PrepareControllerDirectories(foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := controllerowner.Acquire(context.Background(), foreignDirectories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	if database, err := OpenForGeneratedDomain(context.Background(), dataRoot, nil); database != nil || err == nil {
		t.Fatalf("nil lease open = %v, %v", database, err)
	}
	var zero controllerowner.Lease
	if database, err := OpenForGeneratedDomain(context.Background(), dataRoot, &zero); database != nil || err == nil {
		t.Fatalf("zero lease open = %v, %v", database, err)
	}
	if database, err := OpenForGeneratedDomain(context.Background(), dataRoot, foreign); database != nil || err == nil {
		t.Fatalf("foreign lease open = %v, %v", database, err)
	}
	closed, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if database, err := OpenForGeneratedDomain(context.Background(), dataRoot, closed); database != nil || err == nil {
		t.Fatalf("closed lease open = %v, %v", database, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if database, err := OpenForGeneratedDomain(ctx, dataRoot, foreign); database != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open = %v, %v", database, err)
	}
}

func newGeneratedDomainDatabaseBoundary(t *testing.T) (string, *controllerowner.Lease) {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return dataRoot, lease
}

func assertGeneratedDomainDatabaseInitialization(t *testing.T, database *sql.DB) {
	t.Helper()
	var foreignKeys, synchronous, busyTimeout int
	var journalMode string
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, %v", foreignKeys, err)
	}
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil || journalMode != "wal" {
		t.Fatalf("journal_mode = %q, %v", journalMode, err)
	}
	if err := database.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous != 1 {
		t.Fatalf("synchronous = %d, %v", synchronous, err)
	}
	if err := database.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil || busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, %v", busyTimeout, err)
	}
}

func generatedDomainMigrationVersions(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(`SELECT version, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var version, appliedAt string
		if err := rows.Scan(&version, &appliedAt); err != nil {
			t.Fatal(err)
		}
		values = append(values, version+"\x00"+appliedAt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(values)
	return values
}

func generatedDomainDatabaseSnapshot(t *testing.T, database *sql.DB) []any {
	t.Helper()
	versions := generatedDomainMigrationVersions(t, database)
	var value, journal string
	var foreignKeys, synchronous, busyTimeout int
	if err := database.QueryRow(`SELECT value FROM generated_domain_history WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	return []any{versions, value, journal, foreignKeys, synchronous, busyTimeout}
}

func TestGeneratedDomainDatabaseErrorsDoNotExposeRoot(t *testing.T) {
	marker := "private-generated-domain-marker"
	dataRoot := filepath.Join(t.TempDir(), marker)
	if _, err := OpenForGeneratedDomain(context.Background(), dataRoot, nil); err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("error exposed private root: %v", err)
	}
	if _, err := GeneratedDomainFileIdentity(context.Background(), &sql.DB{}, dataRoot); err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("provenance error exposed private root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, generatedDomainDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid boundary created database state: %v", err)
	}
}
