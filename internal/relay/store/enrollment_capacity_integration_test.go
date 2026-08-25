package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLConcurrentEnrollmentCapAndTerminalPrune(t *testing.T) {
	dsn := os.Getenv("RIG_RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RIG_RELAY_TEST_DATABASE_URL is unset; live enrollment capacity/prune test not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "relay_enrollment_cap_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s, err := New(pool, Options{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var successes, capacityErrors, unexpected atomic.Int64
	var wg sync.WaitGroup
	for index := 0; index < MaximumActiveEnrollments+44; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			controllerID, keyID := uuid.NewString(), uuid.NewString()
			state := sha256.Sum256([]byte(fmt.Sprintf("state-%d", index)))
			poll := sha256.Sum256([]byte(fmt.Sprintf("poll-%d", index)))
			requestNonce := sha256.Sum256([]byte(fmt.Sprintf("request-%d", index)))
			_, createErr := s.CreateEnrollment(ctx, EnrollmentInput{
				ControllerID: controllerID, KeyID: keyID, PublicKey: bytes.Repeat([]byte{byte(index)}, 32), InstallationID: int64(index + 1), RepositoryID: int64(index + 1001),
				StateHash: state[:], PollHash: poll[:], PKCECiphertext: bytes.Repeat([]byte{1}, 29), PKCESealNonce: bytes.Repeat([]byte{2}, 12), RequestNonce: requestNonce[:], ExpiresAt: now.Add(10 * time.Minute),
			})
			switch {
			case createErr == nil:
				successes.Add(1)
			case errors.Is(createErr, ErrCapacity):
				capacityErrors.Add(1)
			default:
				unexpected.Add(1)
			}
		}(index)
	}
	wg.Wait()
	if successes.Load() != MaximumActiveEnrollments || capacityErrors.Load() != 44 || unexpected.Load() != 0 {
		t.Fatalf("success=%d capacity=%d unexpected=%d", successes.Load(), capacityErrors.Load(), unexpected.Load())
	}
	oldCompleted := now.Add(-EnrollmentTerminalRetention - time.Hour)
	if _, err = pool.Exec(ctx, `UPDATE relay_enrollments SET status='expired',created_at=$1,expires_at=$2,completed_at=$3,pkce_ciphertext=NULL,pkce_seal_nonce=NULL`, now.Add(-10*24*time.Hour), now.Add(-9*24*time.Hour), oldCompleted); err != nil {
		t.Fatal(err)
	}
	result, err := s.PruneDurableState(ctx, DefaultDurableRetentionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if result.TerminalEnrollments != MaximumActiveEnrollments {
		t.Fatalf("pruned=%d", result.TerminalEnrollments)
	}
}
