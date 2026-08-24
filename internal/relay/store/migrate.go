package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationAdvisoryLock int64 = 0x52494752454c4159

func Migrate(ctx context.Context, pool *pgxpool.Pool) (returnErr error) {
	if pool == nil {
		return fmt.Errorf("relay store migrate: nil pool")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("relay store migrate: acquire: %w", err)
	}
	locked := false
	discard := false
	defer func() {
		if !locked {
			if discard {
				if closeErr := discardMigrationConnection(conn); closeErr != nil {
					if returnErr == nil {
						returnErr = closeErr
					} else {
						returnErr = errors.Join(returnErr, closeErr)
					}
				}
			} else {
				conn.Release()
			}
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		var unlocked bool
		unlockErr := conn.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLock).Scan(&unlocked)
		cancel()
		if unlockErr == nil && unlocked {
			conn.Release()
			return
		}
		if unlockErr == nil {
			unlockErr = errors.New("advisory lock was not held")
		}
		closeErr := discardMigrationConnection(conn)
		cleanupErr := fmt.Errorf("relay store migrate: unlock: %w", unlockErr)
		if closeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("close locked connection: %w", closeErr))
		}
		if returnErr == nil {
			returnErr = cleanupErr
		} else {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLock); err != nil {
		discard = true
		return fmt.Errorf("relay store migrate: lock: %w", err)
	}
	locked = true
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS relay_schema_migrations(version text PRIMARY KEY, checksum bytea NOT NULL CHECK(octet_length(checksum)=32), applied_at timestamptz NOT NULL)`); err != nil {
		return fmt.Errorf("relay store migrate: metadata: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		var existing []byte
		err = conn.QueryRow(ctx, `SELECT checksum FROM relay_schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if err == nil {
			if !bytes.Equal(existing, digest[:]) {
				return fmt.Errorf("relay store migrate: checksum mismatch for %s", entry.Name())
			}
			continue
		}
		if !isNoRows(err) {
			return fmt.Errorf("relay store migrate: inspect %s: %w", entry.Name(), err)
		}
		if err = applyMigration(ctx, conn, entry.Name(), body, digest[:]); err != nil {
			return fmt.Errorf("relay store migrate: apply %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func discardMigrationConnection(conn *pgxpool.Conn) error {
	underlying := conn.Hijack()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := underlying.Close(closeCtx); err != nil {
		return fmt.Errorf("relay store migrate: close locked connection: %w", err)
	}
	return nil
}

type migrationBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func applyMigration(ctx context.Context, conn migrationBeginner, name string, body, digest []byte) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, string(body)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_schema_migrations(version,checksum,applied_at) VALUES($1,$2,clock_timestamp())`, name, digest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
