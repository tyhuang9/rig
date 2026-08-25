package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestApplySubscriptionsSyncUsesConstantRoundTripsAtProtocolMaximum(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	subscriptions := make([]Subscription, 1000)
	for index := range subscriptions {
		subscriptions[index] = Subscription{
			SubscriptionID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1),
			InstallationID: 71,
			RepositoryID:   int64(1000 + index),
			Ref:            "refs/heads/main",
		}
	}
	ids, installations, repositories, refs := subscriptionArrays(subscriptions)
	topologyLocks := newTopologyLockSet()
	for _, sub := range subscriptions {
		topologyLocks.addBinding(sub.InstallationID)
		topologyLocks.addRoute(sub.InstallationID, sub.RepositoryID, sub.Ref)
	}
	if shards := topologyLocks.shardIDs(); len(shards) != topologyShardCount {
		t.Fatalf("maximum sync mapped to %d topology shards, want exactly %d for this fixture", len(shards), topologyShardCount)
	}

	// This maximum-size new-set path has a constant 15 SQL statements
	// (including the fenced command ledger and session touch). The ordered
	// mock rejects any hidden per-subscription query or write.
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectTopologyShards(m, topologyLocks.shardIDs()...)
	expectEmptySubscriptionLockSnapshot(m)
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectQuery("SELECT NOT EXISTS").WithArgs(testController, installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"authorized"}).AddRow(true))
	m.ExpectQuery("FROM unnest\\(\\$1::text\\[\\]\\) requested").WithArgs(ids).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "controller_id", "installation_id", "repository_id", "tracked_ref", "retired_generation"}))
	m.ExpectExec("INSERT INTO relay_subscriptions").WithArgs(testController, uint64(1), ids, installations, repositories, refs, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1000))
	m.ExpectExec("INSERT INTO relay_subscription_set_items").WithArgs(testController, uint64(1), ids, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1000))
	m.ExpectExec("INSERT INTO relay_subscription_heads").WithArgs(testController, uint64(1), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSubscriptionCommandInsert(m, lease, command, 1, 1000)
	m.ExpectCommit()

	result, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, subscriptions)
	if err != nil || result.Kind != ResultSubscriptionsSynced || result.Generation != 1 || result.Count != 1000 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySubscriptionsSyncRejectsBatchAuthorizationBeforeWrites(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	existing := Subscription{SubscriptionID: "00000000-0000-4000-8000-000000002001", InstallationID: 71, RepositoryID: 91, Ref: "refs/heads/main"}
	subscriptions := []Subscription{
		existing,
		{SubscriptionID: "00000000-0000-4000-8000-000000002003", InstallationID: 72, RepositoryID: 92, Ref: "refs/heads/main"},
	}
	ids, installations, repositories, _ := subscriptionArrays(subscriptions)
	topologyLocks := newTopologyLockSet()
	for _, sub := range subscriptions {
		topologyLocks.addBinding(sub.InstallationID)
		topologyLocks.addRoute(sub.InstallationID, sub.RepositoryID, sub.Ref)
	}
	expectActiveSnapshot := func() {
		m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(
			pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(existing.SubscriptionID, existing.InstallationID, existing.RepositoryID, existing.Ref),
		)
	}
	m.ExpectBegin()
	expectActiveSnapshot()
	expectTopologyShards(m, topologyLocks.shardIDs()...)
	expectActiveSnapshot()
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectQuery("SELECT NOT EXISTS").WithArgs(testController, installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"authorized"}).AddRow(false))
	m.ExpectRollback()
	if _, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, subscriptions); !errors.Is(err, ErrConflict) {
		t.Fatalf("ids=%v error=%v", ids, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySubscriptionsSyncRollsBackBatchWritesWhenSetPersistenceFails(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	subscriptions := []Subscription{{SubscriptionID: "00000000-0000-4000-8000-000000002002", InstallationID: 71, RepositoryID: 92, Ref: "refs/heads/main"}}
	ids, installations, repositories, refs := subscriptionArrays(subscriptions)
	outage := errors.New("set persistence unavailable")
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	topologyLocks := newTopologyLockSet()
	topologyLocks.addBinding(71)
	topologyLocks.addRoute(71, 92, "refs/heads/main")
	expectTopologyShards(m, topologyLocks.shardIDs()...)
	expectEmptySubscriptionLockSnapshot(m)
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectQuery("SELECT NOT EXISTS").WithArgs(testController, installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"authorized"}).AddRow(true))
	m.ExpectQuery("FROM unnest\\(\\$1::text\\[\\]\\) requested").WithArgs(ids).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "controller_id", "installation_id", "repository_id", "tracked_ref", "retired_generation"}))
	m.ExpectExec("INSERT INTO relay_subscriptions").WithArgs(testController, uint64(1), ids, installations, repositories, refs, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_subscription_set_items").WithArgs(testController, uint64(1), ids, fixedNow).WillReturnError(outage)
	m.ExpectRollback()
	if _, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, subscriptions); !errors.Is(err, outage) {
		t.Fatalf("error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySubscriptionsSyncUsesPostLockActiveSnapshotForSameRouteBootstrap(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	requested := Subscription{SubscriptionID: "00000000-0000-4000-8000-000000003001", InstallationID: 71, RepositoryID: 91, Ref: "refs/heads/main"}
	concurrent := Subscription{SubscriptionID: "00000000-0000-4000-8000-000000003002", InstallationID: requested.InstallationID, RepositoryID: requested.RepositoryID, Ref: requested.Ref}
	ids, installations, repositories, _ := subscriptionArrays([]Subscription{requested})
	locks := newTopologyLockSet()
	locks.addBinding(requested.InstallationID)
	locks.addRoute(requested.InstallationID, requested.RepositoryID, requested.Ref)
	stop := errors.New("post-lock active subscription reached retirement")

	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectTopologyShards(m, locks.shardIDs()...)
	m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(
		pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(concurrent.SubscriptionID, concurrent.InstallationID, concurrent.RepositoryID, concurrent.Ref),
	)
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectQuery("SELECT NOT EXISTS").WithArgs(testController, installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"authorized"}).AddRow(true))
	m.ExpectQuery("FROM unnest\\(\\$1::text\\[\\]\\) requested").WithArgs(ids).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "controller_id", "installation_id", "repository_id", "tracked_ref", "retired_generation"}))
	m.ExpectExec("UPDATE relay_subscriptions s SET retired_generation").WithArgs(testController, uint64(1), ids, fixedNow).WillReturnError(stop)
	m.ExpectRollback()
	_, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{requested})
	if !errors.Is(err, stop) {
		t.Fatalf("same-route bootstrap error=%v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
