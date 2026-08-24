package store

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const (
	testController   = "11111111-1111-4111-8111-111111111111"
	testKey          = "22222222-2222-4222-8222-222222222222"
	testSession      = "33333333-3333-4333-8333-333333333333"
	testMessage      = "44444444-4444-4444-8444-444444444444"
	testSubscription = "55555555-5555-4555-8555-555555555555"
	testDelivery     = "66666666-6666-4666-8666-666666666666"
	testLease        = "77777777-7777-4777-8777-777777777777"
	testController2  = "88888888-8888-4888-8888-888888888888"
	testEvent        = "99999999-9999-4999-8999-999999999999"
	testEvent2       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

var fixedNow = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func mockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mock.Close() })
	store, err := newWithDatabase(mock, Options{Now: func() time.Time { return fixedNow }, NewUUID: func() uuid.UUID { return uuid.MustParse(testLease) }, RandomBytes: func(dst []byte) error {
		for i := range dst {
			dst[i] = byte(i)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return store, mock
}

func expectSortedAdvisoryLocks(mock pgxmock.PgxPoolIface, keys ...int64) {
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys {
		mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(key).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	}
}

func TestCreateChallengeChecksAuthorizationRowsAndPropagatesOutage(t *testing.T) {
	input := ChallengeInput{SessionID: testSession, ControllerID: testController, KeyID: testKey, ClientNonce: make([]byte, 32), ServerNonce: make([]byte, 32), ACKDigest: make([]byte, 32), ExpiresAt: fixedNow.Add(time.Minute)}
	t.Run("unauthorized", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectExec("INSERT INTO relay_wss_challenges").WithArgs(input.SessionID, input.ControllerID, input.KeyID, input.ClientNonce, input.ServerNonce, input.ACKDigest, fixedNow, input.ExpiresAt).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		if err := s.CreateChallenge(context.Background(), input); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("outage", func(t *testing.T) {
		s, m := mockStore(t)
		outage := errors.New("database unavailable")
		m.ExpectExec("INSERT INTO relay_wss_challenges").WithArgs(input.SessionID, input.ControllerID, input.KeyID, input.ClientNonce, input.ServerNonce, input.ACKDigest, fixedNow, input.ExpiresAt).WillReturnError(outage)
		if err := s.CreateChallenge(context.Background(), input); !errors.Is(err, outage) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConstraintClassificationDoesNotHideOutages(t *testing.T) {
	if !errors.Is(conflictError(&pgconn.PgError{Code: "23505"}), ErrConflict) {
		t.Fatal("unique violation not classified")
	}
	outage := errors.New("network down")
	if !errors.Is(conflictError(outage), outage) {
		t.Fatal("outage hidden")
	}
}

func TestEmptyFanoutPersistsLedgerThenDeduplicatesWithoutChildren(t *testing.T) {
	event := SourceEvent{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReceivedAt: fixedNow, ObservedAt: fixedNow}
	s, m := mockStore(t)
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(event.InstallationID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(event.InstallationID, event.RepositoryID, event.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(event.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(event.DeliveryID, event.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(event.InstallationID, event.RepositoryID, event.Ref).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "subscription_id"}))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(event.DeliveryID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()
	result, err := s.PushSourceEvent(context.Background(), event, []SourceRoute{})
	if err != nil || result.Deduplicated {
		t.Fatalf("first=%#v %v", result, err)
	}
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(event.InstallationID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(event.InstallationID, event.RepositoryID, event.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(event.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(event.DeliveryID, event.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	m.ExpectQuery("SELECT delivery_kind").WithArgs(event.DeliveryID).WillReturnRows(pgxmock.NewRows([]string{"kind"}).AddRow("source"))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(event.DeliveryID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()
	result, err = s.PushSourceEvent(context.Background(), event, []SourceRoute{{ControllerID: testController, SubscriptionID: testSubscription}})
	if err != nil || !result.Deduplicated {
		t.Fatalf("dedupe=%#v %v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchFailureRollsBackAndNeverPartiallyCommits(t *testing.T) {
	s, m := mockStore(t)
	event := SourceEvent{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReceivedAt: fixedNow, ObservedAt: fixedNow}
	outage := errors.New("query failed")
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(event.InstallationID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(event.InstallationID, event.RepositoryID, event.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(event.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(event.DeliveryID, event.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(event.InstallationID, event.RepositoryID, event.Ref).WillReturnError(outage)
	m.ExpectRollback()
	_, err := s.PushSourceEvent(context.Background(), event, nil)
	if !errors.Is(err, outage) {
		t.Fatalf("error=%v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSameGenerationSyncIsExactIdempotent(t *testing.T) {
	s, m := mockStore(t)
	sub := Subscription{SubscriptionID: testSubscription, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main"}
	m.ExpectBegin()
	m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(sub.InstallationID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(sub.InstallationID, sub.RepositoryID, sub.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"generation"}).AddRow(int64(3)))
	m.ExpectQuery("SELECT s.subscription_id::text").WithArgs(testController, int64(3)).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(testSubscription, int64(1), int64(2), "refs/heads/main"))
	m.ExpectCommit()
	if err := s.SyncSubscriptions(context.Background(), testController, 3, []Subscription{sub}); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSameGenerationSyncFailsClosedOnRevokedBindingAndRowsError(t *testing.T) {
	sub := Subscription{SubscriptionID: testSubscription, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main"}
	for _, tc := range []struct {
		name    string
		rows    *pgxmock.Rows
		wantErr error
	}{
		{name: "revoked binding excludes persisted member", rows: pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}), wantErr: ErrConflict},
		{name: "row iteration outage", rows: pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(testSubscription, int64(1), int64(2), "refs/heads/main").RowError(0, errors.New("subscription rows unavailable"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectBegin()
			m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}))
			m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(sub.InstallationID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(sub.InstallationID, sub.RepositoryID, sub.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
			m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}))
			m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"generation"}).AddRow(int64(3)))
			m.ExpectQuery("SELECT s.subscription_id::text").WithArgs(testController, int64(3)).WillReturnRows(tc.rows)
			m.ExpectRollback()
			err := s.SyncSubscriptions(context.Background(), testController, 3, []Subscription{sub})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error=%v want=%v", err, tc.wantErr)
				}
			} else if err == nil || err.Error() != "subscription rows unavailable" {
				t.Fatalf("row iteration error=%v", err)
			}
			if err = m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSyncSubscriptionsLocksOldAndNewUnionBindingThenRouteThenController(t *testing.T) {
	s, m := mockStore(t)
	old := Subscription{SubscriptionID: testSubscription, InstallationID: 19, RepositoryID: 29, Ref: "refs/heads/old"}
	next := Subscription{SubscriptionID: testMessage, InstallationID: 11, RepositoryID: 21, Ref: "refs/heads/main"}
	m.ExpectBegin()
	m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(
		pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(old.SubscriptionID, old.InstallationID, old.RepositoryID, old.Ref),
	)
	expectSortedAdvisoryLocks(m, bindingLockKey(old.InstallationID), bindingLockKey(next.InstallationID))
	expectSortedAdvisoryLocks(m,
		routeLockKey(old.InstallationID, old.RepositoryID, old.Ref),
		routeLockKey(next.InstallationID, next.RepositoryID, next.Ref),
	)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(
		pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}).AddRow(old.SubscriptionID, old.InstallationID, old.RepositoryID, old.Ref),
	)
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"generation"}).AddRow(int64(3)))
	m.ExpectExec("UPDATE relay_subscriptions SET retired_generation").WithArgs(old.SubscriptionID, uint64(4)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectQuery("SELECT EXISTS").WithArgs(testController, next.InstallationID, next.RepositoryID).WillReturnRows(pgxmock.NewRows([]string{"authorized"}).AddRow(true))
	m.ExpectQuery("SELECT controller_id::text,installation_id,repository_id,tracked_ref,retired_generation").WithArgs(next.SubscriptionID).WillReturnError(pgx.ErrNoRows)
	m.ExpectExec("INSERT INTO relay_subscriptions").WithArgs(next.SubscriptionID, testController, next.InstallationID, next.RepositoryID, next.Ref, uint64(4), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_subscription_set_items").WithArgs(testController, uint64(4), next.SubscriptionID, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_subscription_heads").WithArgs(testController, uint64(4), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectCommit()
	if err := s.SyncSubscriptions(context.Background(), testController, 4, []Subscription{next}); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeControllerLocksBindingThenRouteThenControllerAndRejectsUncoveredBinding(t *testing.T) {
	t.Run("stable snapshot blanket revokes after ordered locks", func(t *testing.T) {
		s, m := mockStore(t)
		bindings := []int64{19, 11}
		routes := []Subscription{
			{InstallationID: 19, RepositoryID: 29, Ref: "refs/heads/old"},
			{InstallationID: 11, RepositoryID: 21, Ref: "refs/heads/main"},
		}
		m.ExpectBegin()
		m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(bindings[0]).AddRow(bindings[1]))
		expectSortedAdvisoryLocks(m, bindingLockKey(bindings[0]), bindingLockKey(bindings[1]))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}).AddRow(routes[0].InstallationID, routes[0].RepositoryID, routes[0].Ref).AddRow(routes[1].InstallationID, routes[1].RepositoryID, routes[1].Ref))
		expectSortedAdvisoryLocks(m, routeLockKey(routes[0].InstallationID, routes[0].RepositoryID, routes[0].Ref), routeLockKey(routes[1].InstallationID, routes[1].RepositoryID, routes[1].Ref))
		m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
		m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(bindings[1]).AddRow(bindings[0]))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}).AddRow(routes[1].InstallationID, routes[1].RepositoryID, routes[1].Ref).AddRow(routes[0].InstallationID, routes[0].RepositoryID, routes[0].Ref))
		m.ExpectExec("UPDATE relay_controllers SET state='revoked'").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec("UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec("UPDATE relay_bindings SET revoked_at").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 2))
		m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		if err := s.RevokeController(context.Background(), testController); err != nil {
			t.Fatal(err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("binding enrolled after snapshot forces retry without inverse lock", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(int64(11)))
		expectSortedAdvisoryLocks(m, bindingLockKey(11))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
		m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(int64(11)).AddRow(int64(22)))
		m.ExpectRollback()
		if err := s.RevokeController(context.Background(), testController); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestControllerBoundSourceDecisionUsesLockedCAS(t *testing.T) {
	s, m := mockStore(t)
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(subscriptionLockKey(testSubscription)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT controller_id::text,generation,decision").WithArgs(testSubscription).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "generation", "decision", "message", "code"}).AddRow(testController, int64(2), nil, nil, nil))
	m.ExpectExec("UPDATE relay_desired_states").WithArgs(testSubscription, uint64(2), "acked", nil, testMessage, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	if err := s.DecideSource(context.Background(), testController, testSubscription, 2, Decision{MessageID: testMessage, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseValidationLocksBeforeReplayMutation(t *testing.T) {
	s, m := mockStore(t)
	lease := Lease{ControllerID: testController, SessionID: testSession, LeaseID: testLease, Fence: 4, ExpiresAt: fixedNow.Add(time.Minute)}
	m.ExpectBegin()
	m.ExpectQuery("FOR UPDATE OF l,s").WithArgs(testController, testSession, testLease, uint64(4)).WillReturnRows(pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked"}).AddRow(fixedNow.Add(time.Minute), fixedNow.Add(time.Hour), nil))
	m.ExpectExec("INSERT INTO relay_session_messages").WithArgs(testSession, testMessage, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(testSession, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	if err := s.RecordSessionMessage(context.Background(), lease, testMessage); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeChallengeLocksAuthorizationRowsBeforeCreatingSession(t *testing.T) {
	for _, tc := range []struct {
		name            string
		controllerState string
		keyState        string
	}{
		{name: "revoked key", controllerState: "active", keyState: "revoked"},
		{name: "revoked controller", controllerState: "revoked", keyState: "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectBegin()
			m.ExpectQuery("FOR UPDATE OF ch,c,k").WithArgs(testSession).WillReturnRows(
				pgxmock.NewRows([]string{"controller", "key", "expires", "consumed", "controller_state", "key_state"}).AddRow(testController, testKey, fixedNow.Add(time.Minute), nil, tc.controllerState, tc.keyState),
			)
			m.ExpectRollback()
			if err := s.ConsumeChallenge(context.Background(), testSession, fixedNow.Add(time.Hour)); !errors.Is(err, ErrExpired) {
				t.Fatalf("error=%v", err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSourceRouteSetMustBeComplete(t *testing.T) {
	s, m := mockStore(t)
	event := SourceEvent{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReceivedAt: fixedNow, ObservedAt: fixedNow}
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(event.InstallationID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(event.InstallationID, event.RepositoryID, event.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(event.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(event.DeliveryID, event.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(event.InstallationID, event.RepositoryID, event.Ref).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "subscription_id"}).AddRow(testController, testSubscription))
	m.ExpectRollback()
	if _, err := s.PushSourceEvent(context.Background(), event, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAccessBatchRollbackAndDurablePendingAfterRevocation(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		s, m := mockStore(t)
		event := AccessEventInput{DeliveryID: testDelivery, InstallationID: 1, ChangeCode: "installation.removed", ReceivedAt: fixedNow, ObservedAt: fixedNow, RemoveAccess: true}
		routes := []AccessRoute{{EventID: testEvent, ControllerID: testController}, {EventID: testEvent2, ControllerID: testController2}}
		outage := errors.New("second target failed")
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(1)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(int64(1), int64(0)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectQuery("SELECT b.controller_id::text").WithArgs(int64(1), int64(0)).WillReturnRows(pgxmock.NewRows([]string{"controller_id"}).AddRow(testController).AddRow(testController2))
		m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testEvent, testDelivery, testController, int64(1), nil, event.ChangeCode, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testEvent2, testDelivery, testController2, int64(1), nil, event.ChangeCode, fixedNow).WillReturnError(outage)
		m.ExpectRollback()
		if _, err := s.PushAccessEvent(context.Background(), event, routes); !errors.Is(err, outage) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("pending does not join revoked binding", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectQuery("SELECT a.event_id::text").WithArgs(testController, 10).WillReturnRows(pgxmock.NewRows([]string{"event_id", "delivery_id", "controller_id", "installation_id", "repository_id", "change_code", "observed_at"}).AddRow(testEvent, testDelivery, testController, int64(1), int64(0), "installation.removed", fixedNow))
		items, err := s.PendingAccess(context.Background(), testController, 10)
		if err != nil || len(items) != 1 || items[0].EventID != testEvent {
			t.Fatalf("items=%#v err=%v", items, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAccessFanoutRowsErrorRollsBackBeforeChildrenOrRevocation(t *testing.T) {
	s, m := mockStore(t)
	event := AccessEventInput{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ReceivedAt: fixedNow, ObservedAt: fixedNow, RemoveAccess: true}
	rowsErr := errors.New("access route iteration failed")
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(1)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT b.controller_id::text").WithArgs(int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"controller_id"}).AddRow(testController).RowError(0, rowsErr))
	m.ExpectRollback()
	_, err := s.PushAccessEvent(context.Background(), event, []AccessRoute{{EventID: testEvent, ControllerID: testController}})
	if !errors.Is(err, rowsErr) {
		t.Fatalf("error=%v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAccessDecisionAuthorizationAndExactRejectIdempotency(t *testing.T) {
	decision := Decision{MessageID: testMessage, Code: "access.revoked"}
	for _, tc := range []struct {
		name       string
		controller string
		code       string
		wantErr    error
	}{
		{name: "same code", controller: testController, code: decision.Code},
		{name: "different code", controller: testController, code: "different.code", wantErr: ErrConflict},
		{name: "different controller", controller: testController2, code: decision.Code, wantErr: ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectExec("UPDATE relay_access_events").WithArgs(testEvent, testController, "rejected", decision.Code, testMessage, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			m.ExpectQuery("SELECT controller_id::text,decision").WithArgs(testEvent).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "decision", "message", "code"}).AddRow(tc.controller, "rejected", testMessage, tc.code))
			err := s.DecideAccess(context.Background(), testController, testEvent, decision)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want=%v", err, tc.wantErr)
			}
			if err = m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnrollmentClaimDoesNotAuthorizeBeforeCompletion(t *testing.T) {
	s, m := mockStore(t)
	stateHash := bytes.Repeat([]byte{1}, 32)
	publicKey := bytes.Repeat([]byte{2}, 32)
	ciphertext := bytes.Repeat([]byte{3}, 29)
	sealNonce := bytes.Repeat([]byte{4}, 12)
	requestNonce := bytes.Repeat([]byte{5}, 32)
	m.ExpectBegin()
	m.ExpectQuery("SELECT enrollment_id,controller_id,key_id").WithArgs(stateHash).WillReturnRows(pgxmock.NewRows([]string{"enrollment_id", "controller_id", "key_id", "public_key", "installation_id", "repository_id", "ciphertext", "seal_nonce", "request_nonce", "expires_at", "status"}).AddRow(testDelivery, testController, testKey, publicKey, int64(1), int64(2), ciphertext, sealNonce, requestNonce, fixedNow.Add(time.Minute), "pending"))
	m.ExpectExec("UPDATE relay_enrollments SET status='state_claimed'").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	claim, err := s.ClaimEnrollmentState(context.Background(), stateHash)
	if err != nil || claim.ControllerID != testController {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	claim.Destroy()
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateEnrollmentClassifiesOnlyExactSignedRequestReplay(t *testing.T) {
	input := EnrollmentInput{
		ControllerID: testController, KeyID: testKey, PublicKey: bytes.Repeat([]byte{2}, 32),
		InstallationID: 1, RepositoryID: 2, StateHash: bytes.Repeat([]byte{3}, 32),
		PollHash: bytes.Repeat([]byte{4}, 32), PKCECiphertext: bytes.Repeat([]byte{5}, 29),
		PKCESealNonce: bytes.Repeat([]byte{6}, 12), RequestNonce: bytes.Repeat([]byte{7}, 32),
		ExpiresAt: fixedNow.Add(time.Minute),
	}
	for _, test := range []struct {
		name          string
		databaseError error
		want          error
	}{
		{name: "signed request replay", databaseError: &pgconn.PgError{Code: "23505", ConstraintName: "relay_enrollment_request_replay"}, want: ErrReplay},
		{name: "unrelated unique violation", databaseError: &pgconn.PgError{Code: "23505", ConstraintName: "relay_enrollments_state_hash_key"}},
		{name: "database outage", databaseError: errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectExec("INSERT INTO relay_enrollments").WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).WillReturnError(test.databaseError)
			_, err := s.CreateEnrollment(context.Background(), input)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("error = %v, want %v", err, test.want)
				}
			} else if !errors.Is(err, test.databaseError) {
				t.Fatalf("error = %v, want original %v", err, test.databaseError)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPushAccessEventsPersistsAtomicMultiRepositoryFanout(t *testing.T) {
	batch := AccessEventBatchInput{
		DeliveryID: testDelivery, ReceivedAt: fixedNow,
		Events: []AccessEventBatchItem{
			{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}},
			{InstallationID: 1, RepositoryID: 3, ChangeCode: "repository.removed", ObservedAt: fixedNow, Routes: []AccessRoute{{EventID: testEvent2, ControllerID: testController2}}},
		},
	}
	s, m := mockStore(t)
	m.ExpectBegin()
	expectSortedAdvisoryLocks(m, bindingLockKey(1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT b.controller_id::text").WithArgs(int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"controller_id"}).AddRow(testController))
	m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testEvent, testDelivery, testController, int64(1), int64(2), "repository.removed", fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT b.controller_id::text").WithArgs(int64(1), int64(3)).WillReturnRows(pgxmock.NewRows([]string{"controller_id"}).AddRow(testController2))
	m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testEvent2, testDelivery, testController2, int64(1), int64(3), "repository.removed", fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()
	if result, err := s.PushAccessEvents(context.Background(), batch); err != nil || result.Deduplicated {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushAccessEventsRejectsAmbiguousTargetsBeforeDatabaseWork(t *testing.T) {
	for _, events := range [][]AccessEventBatchItem{
		{{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.added", ObservedAt: fixedNow}, {InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow}},
		{{InstallationID: 1, RepositoryID: 0, ChangeCode: "installation.removed", ObservedAt: fixedNow}, {InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow}},
	} {
		s, m := mockStore(t)
		_, err := s.PushAccessEvents(context.Background(), AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: events})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPushAccessEventsDuplicateDeliveryCannotAppendChildren(t *testing.T) {
	s, m := mockStore(t)
	batch := AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: []AccessEventBatchItem{{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.added", ObservedAt: fixedNow}}}
	m.ExpectBegin()
	expectSortedAdvisoryLocks(m, bindingLockKey(1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	m.ExpectQuery("SELECT delivery_kind").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"kind"}).AddRow("access"))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	result, err := s.PushAccessEvents(context.Background(), batch)
	if err != nil || !result.Deduplicated {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryLedgerRejectsCrossKindGUIDReuseAndIgnoredReasonExpansion(t *testing.T) {
	t.Run("ignored reason is closed enum", func(t *testing.T) {
		s, m := mockStore(t)
		if _, err := s.PushIgnoredDelivery(context.Background(), testDelivery, "provider.detail", fixedNow); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ignored exact replay", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT d.delivery_kind,i.reason_code").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"kind", "reason"}).AddRow("ignored", "push.deleted"))
		m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		m.ExpectCommit()
		deduplicated, err := s.PushIgnoredDelivery(context.Background(), testDelivery, "push.deleted", fixedNow)
		if err != nil || !deduplicated {
			t.Fatalf("deduplicated=%v error=%v", deduplicated, err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ignored rejects source GUID", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT d.delivery_kind,i.reason_code").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"kind", "reason"}).AddRow("source", nil))
		m.ExpectRollback()
		if _, err := s.PushIgnoredDelivery(context.Background(), testDelivery, "push.deleted", fixedNow); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ignored rejects access GUID", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT d.delivery_kind,i.reason_code").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"kind", "reason"}).AddRow("access", nil))
		m.ExpectRollback()
		if _, err := s.PushIgnoredDelivery(context.Background(), testDelivery, "push.deleted", fixedNow); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("source rejects ignored GUID", func(t *testing.T) {
		s, m := mockStore(t)
		event := SourceEvent{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReceivedAt: fixedNow, ObservedAt: fixedNow}
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(1)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(routeLockKey(1, 2, event.Ref)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT delivery_kind").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"kind"}).AddRow("ignored"))
		m.ExpectRollback()
		if _, err := s.PushSourceEvent(context.Background(), event, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("access rejects ignored GUID", func(t *testing.T) {
		s, m := mockStore(t)
		batch := AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: []AccessEventBatchItem{{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.added", ObservedAt: fixedNow}}}
		m.ExpectBegin()
		expectSortedAdvisoryLocks(m, bindingLockKey(1))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT delivery_kind").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"kind"}).AddRow("ignored"))
		m.ExpectRollback()
		if _, err := s.PushAccessEvents(context.Background(), batch); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEnrollmentOutagesRollbackWithoutCreatingIdentity(t *testing.T) {
	stateHash := bytes.Repeat([]byte{1}, 32)
	publicKey := bytes.Repeat([]byte{2}, 32)
	ciphertext := bytes.Repeat([]byte{3}, 29)
	sealNonce := bytes.Repeat([]byte{4}, 12)
	requestNonce := bytes.Repeat([]byte{5}, 32)

	t.Run("claim state transition outage", func(t *testing.T) {
		s, m := mockStore(t)
		outage := errors.New("claim update unavailable")
		m.ExpectBegin()
		m.ExpectQuery("SELECT enrollment_id,controller_id,key_id").WithArgs(stateHash).WillReturnRows(pgxmock.NewRows([]string{"enrollment_id", "controller_id", "key_id", "public_key", "installation_id", "repository_id", "ciphertext", "seal_nonce", "request_nonce", "expires_at", "status"}).AddRow(testDelivery, testController, testKey, publicKey, int64(1), int64(2), ciphertext, sealNonce, requestNonce, fixedNow.Add(time.Minute), "pending"))
		m.ExpectExec("UPDATE relay_enrollments SET status='state_claimed'").WithArgs(testDelivery, fixedNow).WillReturnError(outage)
		m.ExpectRollback()
		claim, err := s.ClaimEnrollmentState(context.Background(), stateHash)
		claim.Destroy()
		if !errors.Is(err, outage) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("identity creation outage", func(t *testing.T) {
		s, m := mockStore(t)
		outage := errors.New("key persistence unavailable")
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id,key_id,public_key").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "key_id", "public_key", "installation_id", "repository_id", "expires_at", "status"}).AddRow(testController, testKey, publicKey, int64(1), int64(2), fixedNow.Add(time.Minute), "state_claimed"))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(1)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
		m.ExpectExec("INSERT INTO relay_controllers").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectExec("INSERT INTO relay_controller_keys").WithArgs(testKey, testController, publicKey, fixedNow).WillReturnError(outage)
		m.ExpectRollback()
		if err := s.CompleteEnrollment(context.Background(), testDelivery); !errors.Is(err, outage) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCompleteEnrollmentCreatesIdentityOnlyAfterClaim(t *testing.T) {
	s, m := mockStore(t)
	publicKey := bytes.Repeat([]byte{2}, 32)
	m.ExpectBegin()
	m.ExpectQuery("SELECT controller_id,key_id,public_key").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "key_id", "public_key", "installation_id", "repository_id", "expires_at", "status"}).AddRow(testController, testKey, publicKey, int64(1), int64(2), fixedNow.Add(time.Minute), "state_claimed"))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(bindingLockKey(1)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectExec("INSERT INTO relay_controllers").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_controller_keys").WithArgs(testKey, testController, publicKey, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_bindings").WithArgs(testController, int64(1), int64(2), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_enrollments SET status='authorized'").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	if err := s.CompleteEnrollment(context.Background(), testDelivery); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseConflictAndReplayCAS(t *testing.T) {
	t.Run("active replacement conflict", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id::text,expires_at,revoked_at").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller", "expires", "revoked"}).AddRow(testController, fixedNow.Add(time.Hour), nil))
		m.ExpectQuery("SELECT session_id::text,fence,expires_at").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"session", "fence", "expires"}).AddRow(testMessage, int64(3), fixedNow.Add(time.Minute)))
		m.ExpectRollback()
		if _, err := s.AcquireLease(context.Background(), testSession, time.Minute); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("message replay", func(t *testing.T) {
		s, m := mockStore(t)
		lease := Lease{ControllerID: testController, SessionID: testSession, LeaseID: testLease, Fence: 4}
		m.ExpectBegin()
		m.ExpectQuery("FOR UPDATE OF l").WithArgs(testController, testSession, testLease, uint64(4)).WillReturnRows(pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked"}).AddRow(fixedNow.Add(time.Minute), fixedNow.Add(time.Hour), nil))
		m.ExpectExec("INSERT INTO relay_session_messages").WithArgs(testSession, testMessage, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectRollback()
		if err := s.RecordSessionMessage(context.Background(), lease, testMessage); !errors.Is(err, ErrReplay) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExpiredLeaseCanBeTakenOverAndStaleLeaseCannotRecordReplayState(t *testing.T) {
	t.Run("expired lease takeover increments fence", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id::text,expires_at,revoked_at").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller", "expires", "revoked"}).AddRow(testController, fixedNow.Add(time.Hour), nil))
		m.ExpectQuery("SELECT session_id::text,fence,expires_at").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"session", "fence", "expires"}).AddRow(testMessage, int64(3), fixedNow.Add(-time.Second)))
		m.ExpectExec("INSERT INTO relay_controller_leases").WithArgs(testController, testSession, testLease, int64(4), fixedNow.Add(time.Minute), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectCommit()
		lease, err := s.AcquireLease(context.Background(), testSession, time.Minute)
		if err != nil || lease.Fence != 4 || lease.LeaseID != testLease || lease.SessionID != testSession {
			t.Fatalf("lease=%#v err=%v", lease, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expired lease fails before replay insert", func(t *testing.T) {
		s, m := mockStore(t)
		lease := Lease{ControllerID: testController, SessionID: testSession, LeaseID: testLease, Fence: 4}
		m.ExpectBegin()
		m.ExpectQuery("FOR UPDATE OF l").WithArgs(testController, testSession, testLease, uint64(4)).WillReturnRows(pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked"}).AddRow(fixedNow, fixedNow.Add(time.Hour), nil))
		m.ExpectRollback()
		if err := s.RecordSessionMessage(context.Background(), lease, testMessage); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRotationPersistsOriginAndEnforcesExpiryOwnership(t *testing.T) {
	input := RotationInput{RotationID: testDelivery, ControllerID: testController, OldKeyID: testKey, NewKeyID: testSubscription, SessionID: testSession, NewPublicKey: bytes.Repeat([]byte{9}, 32)}
	t.Run("propose binds active session", func(t *testing.T) {
		s, m := mockStore(t)
		nonce := make([]byte, 32)
		for i := range nonce {
			nonce[i] = byte(i)
		}
		m.ExpectBegin()
		m.ExpectQuery("SELECT c.state,se.expires_at,se.revoked_at.*FOR UPDATE OF c,k,se").WithArgs(testController, testKey, testSession).WillReturnRows(pgxmock.NewRows([]string{"state", "expires", "revoked"}).AddRow("active", fixedNow.Add(time.Hour), nil))
		m.ExpectExec("INSERT INTO relay_controller_keys").WithArgs(input.NewKeyID, input.ControllerID, input.NewPublicKey, input.RotationID, input.OldKeyID, input.SessionID, nonce, fixedNow.Add(time.Minute), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectCommit()
		challenge, err := s.ProposeRotation(context.Background(), input, time.Minute)
		if err != nil || challenge.SessionID != testSession || challenge.OldKeyID != testKey || !bytes.Equal(challenge.NewPublicKey, input.NewPublicKey) {
			t.Fatalf("challenge=%#v err=%v", challenge, err)
		}
		challenge.Destroy()
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("confirm expires pending key", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT rotation_expires_at,possession_confirmed_at").WithArgs(testController, testDelivery).WillReturnRows(pgxmock.NewRows([]string{"expires", "confirmed"}).AddRow(fixedNow, nil))
		m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'").WithArgs(testController, testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		if err := s.ConfirmRotation(context.Background(), testController, testDelivery); !errors.Is(err, ErrExpired) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("finalize rejects different origin session", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT p.key_id::text,p.public_key,p.rotation_old_key_id::text.*FOR UPDATE OF p,o,se").WithArgs(testController, testDelivery).WillReturnRows(pgxmock.NewRows([]string{"pending", "public_key", "old", "session", "confirmed", "expires", "old_state", "session_expires", "session_revoked"}).AddRow(testSubscription, input.NewPublicKey, testKey, testMessage, true, fixedNow.Add(time.Minute), "active", fixedNow.Add(time.Hour), nil))
		m.ExpectRollback()
		if err := s.FinalizeRotation(context.Background(), input); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("finalize atomically activates new key and revokes old sessions", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT p.key_id::text,p.public_key,p.rotation_old_key_id::text.*FOR UPDATE OF p,o,se").WithArgs(testController, testDelivery).WillReturnRows(pgxmock.NewRows([]string{"pending", "public_key", "old", "session", "confirmed", "expires", "old_state", "session_expires", "session_revoked"}).AddRow(testSubscription, input.NewPublicKey, testKey, testSession, true, fixedNow.Add(time.Minute), "active", fixedNow.Add(time.Hour), nil))
		m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'").WithArgs(testController, testKey, testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec("UPDATE relay_controller_keys SET state='active'").WithArgs(testController, testSubscription).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 2))
		m.ExpectCommit()
		if err := s.FinalizeRotation(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("finalize rejects revoked origin session", func(t *testing.T) {
		s, m := mockStore(t)
		revokedAt := fixedNow.Add(-time.Second)
		m.ExpectBegin()
		m.ExpectQuery("SELECT p.key_id::text,p.public_key,p.rotation_old_key_id::text.*FOR UPDATE OF p,o,se").WithArgs(testController, testDelivery).WillReturnRows(pgxmock.NewRows([]string{"pending", "public_key", "old", "session", "confirmed", "expires", "old_state", "session_expires", "session_revoked"}).AddRow(testSubscription, input.NewPublicKey, testKey, testSession, true, fixedNow.Add(time.Minute), "active", fixedNow.Add(time.Hour), revokedAt))
		m.ExpectRollback()
		if err := s.FinalizeRotation(context.Background(), input); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("finalize rejects different persisted public key", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT p.key_id::text,p.public_key,p.rotation_old_key_id::text.*FOR UPDATE OF p,o,se").WithArgs(testController, testDelivery).WillReturnRows(pgxmock.NewRows([]string{"pending", "public_key", "old", "session", "confirmed", "expires", "old_state", "session_expires", "session_revoked"}).AddRow(testSubscription, bytes.Repeat([]byte{8}, 32), testKey, testSession, true, fixedNow.Add(time.Minute), "active", fixedNow.Add(time.Hour), nil))
		m.ExpectRollback()
		if err := s.FinalizeRotation(context.Background(), input); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRevokeKeyCancelsPendingRotationsAndSessionsAtomically(t *testing.T) {
	s, m := mockStore(t)
	m.ExpectBegin()
	m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'.*key_id=\\$2").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'.*rotation_old_key_id=\\$2").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	m.ExpectCommit()
	if err := s.RevokeKey(context.Background(), testController, testKey); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryFencesRetriesAndCursorTakeover(t *testing.T) {
	t.Run("claim and stale attempt", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(recoveryScanClaimLock).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("FOR UPDATE SKIP LOCKED").WithArgs(fixedNow, 1).WillReturnRows(pgxmock.NewRows([]string{"number", "id", "occurred", "attempts", "next", "code", "fence"}).AddRow(int64(100), testDelivery, fixedNow.Add(-time.Hour), 0, nil, "", int64(0)))
		m.ExpectExec("UPDATE relay_recovery_deliveries SET claim_id").WithArgs(testDelivery, int64(100), testLease, uint64(1), fixedNow.Add(time.Minute), int64(0)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		claims, err := s.ClaimRecovery(context.Background(), 1, time.Minute)
		if err != nil || len(claims) != 1 || claims[0].Fence != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		next := fixedNow.Add(time.Minute)
		m.ExpectExec("UPDATE relay_recovery_deliveries SET attempts").WithArgs(testDelivery, int64(100), testLease, uint64(1), next, "github.unavailable", fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		if err = s.RecordRecoveryAttempt(context.Background(), claims[0], next, "github.unavailable"); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expired scan takeover and cursor CAS", func(t *testing.T) {
		s, m := mockStore(t)
		start, end := fixedNow.Add(-time.Hour), fixedNow
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(recoveryScanClaimLock).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT scan_id::text,fence,window_started_at").WillReturnRows(pgxmock.NewRows([]string{"scan", "fence", "start", "end", "page", "complete", "lease"}).AddRow(testDelivery, int64(7), start, end, "opaque-old", false, fixedNow.Add(-time.Second)))
		m.ExpectExec(regexp.QuoteMeta("UPDATE relay_recovery_cursor SET fence=fence+1")).WithArgs(testDelivery, uint64(7), fixedNow.Add(recoveryScanLease), fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		cursor, err := s.StartRecoveryScan(context.Background(), start, end)
		if err != nil || cursor.ScanID != testDelivery || cursor.Fence != 8 || cursor.PageCursor != "opaque-old" || !cursor.WindowStartedAt.Equal(start) || !cursor.WindowEndsAt.Equal(end) {
			t.Fatalf("cursor=%+v err=%v", cursor, err)
		}
		m.ExpectExec("UPDATE relay_recovery_cursor SET page_cursor").WithArgs(testDelivery, uint64(8), "opaque-old", "page-2", fixedNow, fixedNow.Add(recoveryScanLease)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		next, err := s.AdvanceRecoveryCursor(context.Background(), cursor, "page-2")
		if err != nil || next.Fence != 9 {
			t.Fatalf("next=%#v err=%v", next, err)
		}
		m.ExpectExec("UPDATE relay_recovery_cursor SET page_cursor").WithArgs(testDelivery, uint64(8), "opaque-old", "stale", fixedNow, fixedNow.Add(recoveryScanLease)).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		if _, err = s.AdvanceRecoveryCursor(context.Background(), cursor, "stale"); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale=%v", err)
		}
		if _, err = s.AdvanceRecoveryCursor(context.Background(), next, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("rewind=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("mismatched discovery conflict", func(t *testing.T) {
		s, m := mockStore(t)
		item := RecoveryDelivery{DeliveryNumber: 100, DeliveryID: testDelivery, OccurredAt: fixedNow}
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"number", "occurred", "succeeded", "recovered"}).AddRow(int64(100), fixedNow, nil, nil))
		m.ExpectQuery("SELECT EXISTS").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		m.ExpectExec("INSERT INTO relay_recovery_delivery_attempts").WithArgs(int64(100), testDelivery, fixedNow, false, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT delivery_id::text,occurred_at,successful").WithArgs(int64(100)).WillReturnRows(pgxmock.NewRows([]string{"id", "occurred", "successful"}).AddRow(testController, fixedNow, false))
		m.ExpectRollback()
		if _, err := s.DiscoverRecoveryDelivery(context.Background(), item); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("exact delivery number and GUID is idempotent", func(t *testing.T) {
		s, m := mockStore(t)
		item := RecoveryDelivery{DeliveryNumber: 100, DeliveryID: testDelivery, OccurredAt: fixedNow}
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"number", "occurred", "succeeded", "recovered"}).AddRow(int64(100), fixedNow, nil, nil))
		m.ExpectQuery("SELECT EXISTS").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		m.ExpectExec("INSERT INTO relay_recovery_delivery_attempts").WithArgs(int64(100), testDelivery, fixedNow, false, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
		m.ExpectQuery("SELECT delivery_id::text,occurred_at,successful").WithArgs(int64(100)).WillReturnRows(pgxmock.NewRows([]string{"id", "occurred", "successful"}).AddRow(testDelivery, fixedNow, false))
		m.ExpectCommit()
		deduplicated, err := s.DiscoverRecoveryDelivery(context.Background(), item)
		if err != nil || !deduplicated {
			t.Fatalf("deduplicated=%t error=%v", deduplicated, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("claim row iteration failure rolls back", func(t *testing.T) {
		s, m := mockStore(t)
		rowsErr := errors.New("recovery rows unavailable")
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(recoveryScanClaimLock).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("FOR UPDATE SKIP LOCKED").WithArgs(fixedNow, 1).WillReturnRows(pgxmock.NewRows([]string{"number", "id", "occurred", "attempts", "next", "code", "fence"}).AddRow(int64(100), testDelivery, fixedNow.Add(-time.Hour), 0, nil, "", int64(0)).RowError(0, rowsErr))
		m.ExpectRollback()
		if _, err := s.ClaimRecovery(context.Background(), 1, time.Minute); !errors.Is(err, rowsErr) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("successful retry releases claim with exact fence", func(t *testing.T) {
		s, m := mockStore(t)
		claim := RecoveryClaim{RecoveryDelivery: RecoveryDelivery{DeliveryNumber: 100, DeliveryID: testDelivery}, ClaimID: testLease, Fence: 7}
		next := fixedNow.Add(time.Minute)
		m.ExpectExec("UPDATE relay_recovery_deliveries SET attempts").WithArgs(testDelivery, int64(100), testLease, uint64(7), next, "github.unavailable", fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		if err := s.RecordRecoveryAttempt(context.Background(), claim, next, "github.unavailable"); err != nil {
			t.Fatal(err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("mark recovered requires inbound delivery ledger", func(t *testing.T) {
		s, m := mockStore(t)
		claim := RecoveryClaim{RecoveryDelivery: RecoveryDelivery{DeliveryNumber: 100, DeliveryID: testDelivery}, ClaimID: testLease, Fence: 7}
		m.ExpectExec("UPDATE relay_recovery_deliveries r SET recovered_at").WithArgs(testDelivery, int64(100), testLease, uint64(7), fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		if err := s.MarkRecovered(context.Background(), claim); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRecoveryAttemptGroupsAdvanceNewestFailureFenceThenSuppressOnSuccess(t *testing.T) {
	s, m := mockStore(t)
	failureA := RecoveryDelivery{DeliveryNumber: 100, DeliveryID: testDelivery, OccurredAt: fixedNow.Add(-3 * time.Minute)}
	failureB := RecoveryDelivery{DeliveryNumber: 101, DeliveryID: testDelivery, OccurredAt: fixedNow.Add(-2 * time.Minute)}
	successC := RecoveryDelivery{DeliveryNumber: 102, DeliveryID: testDelivery, OccurredAt: fixedNow.Add(-time.Minute), Successful: true}

	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at").WithArgs(testDelivery).WillReturnError(pgx.ErrNoRows)
	m.ExpectQuery("SELECT EXISTS").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	m.ExpectExec("INSERT INTO relay_recovery_deliveries").WithArgs(int64(100), testDelivery, failureA.OccurredAt, fixedNow, nil, nil).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_recovery_delivery_attempts").WithArgs(int64(100), testDelivery, failureA.OccurredAt, false, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectCommit()
	if _, err := s.DiscoverRecoveryDelivery(context.Background(), failureA); err != nil {
		t.Fatal(err)
	}

	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"number", "occurred", "succeeded", "recovered"}).AddRow(int64(100), failureA.OccurredAt, nil, nil))
	m.ExpectQuery("SELECT EXISTS").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	m.ExpectExec("INSERT INTO relay_recovery_delivery_attempts").WithArgs(int64(101), testDelivery, failureB.OccurredAt, false, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_recovery_deliveries SET delivery_number").WithArgs(testDelivery, int64(101), failureB.OccurredAt).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	if _, err := s.DiscoverRecoveryDelivery(context.Background(), failureB); err != nil {
		t.Fatal(err)
	}

	stale := RecoveryClaim{RecoveryDelivery: failureA, ClaimID: testLease, Fence: 1}
	next := fixedNow.Add(time.Minute)
	m.ExpectExec("UPDATE relay_recovery_deliveries SET attempts").WithArgs(testDelivery, int64(100), testLease, uint64(1), next, "github.unavailable", fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if err := s.RecordRecoveryAttempt(context.Background(), stale, next, "github.unavailable"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale A claim error=%v", err)
	}

	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"number", "occurred", "succeeded", "recovered"}).AddRow(int64(101), failureB.OccurredAt, nil, nil))
	m.ExpectQuery("SELECT EXISTS").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	m.ExpectExec("INSERT INTO relay_recovery_delivery_attempts").WithArgs(int64(102), testDelivery, successC.OccurredAt, true, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_recovery_deliveries SET provider_succeeded_at").WithArgs(testDelivery, successC.OccurredAt, false, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	if _, err := s.DiscoverRecoveryDelivery(context.Background(), successC); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryFailureWithInboundLedgerIsImmediatelySuppressed(t *testing.T) {
	s, m := mockStore(t)
	item := RecoveryDelivery{DeliveryNumber: 101, DeliveryID: testDelivery, OccurredAt: fixedNow.Add(-time.Minute)}
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT delivery_number,occurred_at,provider_succeeded_at,recovered_at").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"number", "occurred", "succeeded", "recovered"}).AddRow(int64(100), fixedNow.Add(-2*time.Minute), nil, nil))
	m.ExpectQuery("SELECT EXISTS").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	m.ExpectExec("INSERT INTO relay_recovery_delivery_attempts").WithArgs(int64(101), testDelivery, item.OccurredAt, false, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_recovery_deliveries SET recovered_at").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	if _, err := s.DiscoverRecoveryDelivery(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationApplyRollsBackOnFailure(t *testing.T) {
	m, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	outage := errors.New("DDL failed")
	m.ExpectBegin()
	m.ExpectExec("CREATE TABLE broken").WillReturnError(outage)
	m.ExpectRollback()
	if err = applyMigration(context.Background(), m, "001.sql", []byte("CREATE TABLE broken"), bytes.Repeat([]byte{1}, 32)); !errors.Is(err, outage) {
		t.Fatalf("error=%v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
