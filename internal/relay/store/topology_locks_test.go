package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func topologyCollisionFixture(t *testing.T) (int64, string, int16) {
	t.Helper()
	const installationID = int64(1)
	shard := bindingTopologyShard(installationID)
	var repositoryID int64
	for candidate := int64(1); candidate < 10000; candidate++ {
		if routeTopologyShard(installationID, candidate, "refs/heads/main") == shard {
			repositoryID = candidate
			break
		}
	}
	var subscriptionID string
	for candidate := 1; candidate < 10000; candidate++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012x", candidate)
		if subscriptionTopologyShard(id) == shard {
			subscriptionID = id
			break
		}
	}
	if repositoryID == 0 || subscriptionID == "" {
		t.Fatal("failed to construct a cross-domain topology shard collision")
	}
	return repositoryID, subscriptionID, shard
}

func TestTopologyShardsBoundSortDeduplicateAndFailClosed(t *testing.T) {
	repositoryID, subscriptionID, collisionShard := topologyCollisionFixture(t)
	locks := newTopologyLockSet()
	locks.addBinding(1)
	locks.addBinding(1)
	locks.addRoute(1, repositoryID, "refs/heads/main")
	locks.addSubscription(subscriptionID)
	if shards := locks.shardIDs(); len(shards) != 1 || shards[0] != collisionShard {
		t.Fatalf("collision shards=%v want [%d]", shards, collisionShard)
	}

	maximum := newTopologyLockSet()
	for index := 0; index < 1000; index++ {
		id := fmt.Sprintf("00000000-0000-4000-8001-%012x", index+1)
		maximum.addBinding(int64(index + 1))
		maximum.addRoute(int64(index+1), int64(index+1001), "refs/heads/main")
		maximum.addSubscription(id)
	}
	if count := len(maximum.shardIDs()); count > topologyShardCount {
		t.Fatalf("maximum fanout locked %d shards, limit %d", count, topologyShardCount)
	}

	t.Run("missing seed row", func(t *testing.T) {
		s, m := mockStore(t)
		_ = s
		missing := newTopologyLockSet()
		missing.addBinding(1)
		for candidate := int64(1); ; candidate++ {
			missing.addRoute(1, candidate, "refs/heads/missing-seed")
			if len(missing.shardIDs()) == 2 {
				break
			}
			delete(missing.routes, routeTopologyKey{installationID: 1, repositoryID: candidate, ref: "refs/heads/missing-seed"})
		}
		shards := missing.shardIDs()
		m.ExpectBegin()
		tx, err := m.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		m.ExpectQuery("SELECT shard_id FROM relay_topology_lock_shards").WithArgs(shards).WillReturnRows(pgxmock.NewRows([]string{"shard_id"}).AddRow(shards[0]))
		m.ExpectRollback()
		if err = acquireTopologyLocks(context.Background(), tx, missing); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing seed error=%v", err)
		}
		if err = tx.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("binding-only bootstrap", func(t *testing.T) {
		_, m := mockStore(t)
		bootstrap := newTopologyLockSet()
		bootstrap.addBinding(99)
		m.ExpectBegin()
		tx, err := m.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		expectTopologyShards(m, bootstrap.shardIDs()...)
		m.ExpectRollback()
		if err = acquireTopologyLocks(context.Background(), tx, bootstrap); err != nil {
			t.Fatal(err)
		}
		if err = tx.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTopologyShardCollisionUsesOneGlobalFirstLockAcrossEveryPath(t *testing.T) {
	repositoryID, subscriptionID, shard := topologyCollisionFixture(t)
	stop := errors.New("stop after topology lock")
	ref := "refs/heads/main"
	routeRows := func() *pgxmock.Rows {
		return pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}).AddRow(int64(1), repositoryID, ref)
	}

	t.Run("complete enrollment", func(t *testing.T) {
		s, m := mockStore(t)
		publicKey := bytes.Repeat([]byte{2}, 32)
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id,key_id,public_key").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "key_id", "public_key", "installation_id", "repository_id", "expires_at", "status"}).AddRow(testController, testKey, publicKey, int64(1), repositoryID, fixedNow.Add(time.Minute), "state_claimed"))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), repositoryID).WillReturnRows(routeRows())
		expectTopologyShards(m, shard)
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), repositoryID).WillReturnError(stop)
		m.ExpectRollback()
		if err := s.CompleteEnrollment(context.Background(), testDelivery); !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("source fanout", func(t *testing.T) {
		s, m := mockStore(t)
		event := SourceEvent{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: repositoryID, Ref: ref, SHA: strings.Repeat("a", 40), ReceivedAt: fixedNow, ObservedAt: fixedNow}
		m.ExpectBegin()
		expectTopologyShards(m, shard)
		m.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(deliveryLockKey(testDelivery)).WillReturnError(stop)
		m.ExpectRollback()
		_, err := s.PushSourceEvent(context.Background(), event, []SourceRoute{{ControllerID: testController, SubscriptionID: subscriptionID}})
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("access fanout", func(t *testing.T) {
		s, m := mockStore(t)
		batchEvents := []AccessEventBatchItem{{InstallationID: 1, RepositoryID: repositoryID, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true}}
		m.ExpectBegin()
		expectAccessRouteSnapshot(m, batchEvents, routeRows())
		expectTopologyShards(m, shard)
		installations, repositories, removals := accessBatchTargetArgs(batchEvents)
		m.ExpectQuery("WITH targets AS .*SELECT DISTINCT s.installation_id").WithArgs(installations, repositories, removals).WillReturnError(stop)
		m.ExpectRollback()
		_, err := s.PushAccessEvent(context.Background(), AccessEventInput{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: repositoryID, ChangeCode: "repository.removed", ReceivedAt: fixedNow, ObservedAt: fixedNow, RemoveAccess: true}, nil)
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("subscription sync", func(t *testing.T) {
		s, m := mockStore(t)
		lease, command := commandTestLease(), subscriptionTestCommand()
		active := pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(subscriptionID, int64(1), repositoryID, ref)
		m.ExpectBegin()
		m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(active)
		expectTopologyShards(m, shard)
		m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnError(stop)
		m.ExpectRollback()
		_, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{{SubscriptionID: subscriptionID, InstallationID: 1, RepositoryID: repositoryID, Ref: ref}})
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("binding removal", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), repositoryID).WillReturnRows(routeRows())
		expectTopologyShards(m, shard)
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), repositoryID).WillReturnError(stop)
		m.ExpectRollback()
		_, err := s.ApplyBindingRemoval(context.Background(), commandTestLease(), SessionCommand{MessageID: testMessage, Type: CommandBindingRemove, Digest: [32]byte{1}}, 1, repositoryID)
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("controller revocation", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(int64(1)))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(routeRows())
		expectTopologyShards(m, shard)
		m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnError(stop)
		m.ExpectRollback()
		_, err := s.ApplyControllerRevocation(context.Background(), commandTestLease(), SessionCommand{MessageID: testMessage, Type: CommandControllerRevoke, Digest: [32]byte{1}}, testController)
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("source decision", func(t *testing.T) {
		s, m := mockStore(t)
		command := SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: [32]byte{1}}
		m.ExpectBegin()
		expectTopologyShards(m, shard)
		m.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sessionCommandLockKey(testController, testMessage)).WillReturnError(stop)
		m.ExpectRollback()
		_, err := s.ApplySourceDecision(context.Background(), commandTestLease(), command, subscriptionID, 1, testDelivery, true, "")
		if !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTopologyAdvisoryLockInventoryIsConstant(t *testing.T) {
	allowed := []string{"deliveryLockKey(", "sessionCommandLockKey(", "controllerSessionLockKey(", "recoveryScanClaimLock", "migrationAdvisoryLock"}
	err := filepath.Walk(".", func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "pg_advisory_xact_lock") {
				continue
			}
			known := false
			for _, token := range allowed {
				if strings.Contains(line, token) {
					known = true
					break
				}
			}
			if !known {
				t.Errorf("unbounded or unknown advisory lock in %s: %s", path, strings.TrimSpace(line))
			}
		}
		for _, legacy := range []string{"bindingLockKey", "routeLockKey", "subscriptionLockKey", "acquireAdvisoryLocks"} {
			if strings.Contains(string(body), legacy) {
				t.Errorf("legacy topology advisory helper %q remains in %s", legacy, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
