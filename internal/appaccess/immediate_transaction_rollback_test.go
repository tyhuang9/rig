package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

func immediateRollbackTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE rollback_probe(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestImmediateTransactionRollbackKeepsCleanConnectionReusable(t *testing.T) {
	db := immediateRollbackTestDB(t)
	tx, err := beginImmediateTransaction(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO rollback_probe(value) VALUES('uncommitted')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if stats := db.Stats(); stats.OpenConnections != 1 || stats.Idle != 1 {
		t.Fatalf("successful rollback did not return a clean connection: %#v", stats)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rollback_probe`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("uncommitted row survived successful rollback: count=%d err=%v", count, err)
	}
}

func TestImmediateTransactionRollbackFailureDiscardsUnknownConnection(t *testing.T) {
	db := immediateRollbackTestDB(t)
	tx, err := beginImmediateTransaction(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO rollback_probe(value) VALUES('uncommitted')`); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected SQL rollback failure")
	rollbackCalls := 0
	err = tx.rollbackWith(func(_ context.Context, statement string, _ ...any) (sql.Result, error) {
		rollbackCalls++
		if statement != "ROLLBACK" {
			t.Fatalf("unexpected rollback statement %q", statement)
		}
		return nil, failure
	})
	if !errors.Is(err, failure) || rollbackCalls != 1 {
		t.Fatalf("rollback failure=%v calls=%d", err, rollbackCalls)
	}
	if stats := db.Stats(); stats.OpenConnections != 0 || stats.Idle != 0 {
		t.Fatalf("unknown-state connection returned to pool: %#v", stats)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("completed rollback retried: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rollback_probe`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh connection observed uncommitted row: count=%d err=%v", count, err)
	}
}
