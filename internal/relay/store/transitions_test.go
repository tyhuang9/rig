package store

import (
	"bytes"
	"context"
	"errors"
	"reflect"
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

func expectTopologyShards(mock pgxmock.PgxPoolIface, shards ...int16) {
	unique := make(map[int16]struct{}, len(shards))
	for _, shard := range shards {
		unique[shard] = struct{}{}
	}
	ordered := make([]int16, 0, len(unique))
	for shard := range unique {
		ordered = append(ordered, shard)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	rows := pgxmock.NewRows([]string{"shard_id"})
	for _, shard := range ordered {
		rows.AddRow(shard)
	}
	mock.ExpectQuery("SELECT shard_id FROM relay_topology_lock_shards").WithArgs(ordered).WillReturnRows(rows)
}

func accessBatchTargetArgs(events []AccessEventBatchItem) ([]int64, []int64, []bool) {
	events = append([]AccessEventBatchItem(nil), events...)
	sort.Slice(events, func(i, j int) bool {
		if events[i].InstallationID != events[j].InstallationID {
			return events[i].InstallationID < events[j].InstallationID
		}
		return events[i].RepositoryID < events[j].RepositoryID
	})
	installations := make([]int64, len(events))
	repositories := make([]int64, len(events))
	removals := make([]bool, len(events))
	for i, event := range events {
		installations[i] = event.InstallationID
		repositories[i] = event.RepositoryID
		removals[i] = event.RemoveAccess
	}
	return installations, repositories, removals
}

func expectAccessRouteSnapshot(mock pgxmock.PgxPoolIface, events []AccessEventBatchItem, rows *pgxmock.Rows) {
	installations, repositories, removals := accessBatchTargetArgs(events)
	mock.ExpectQuery("WITH targets AS .*SELECT DISTINCT s.installation_id").WithArgs(installations, repositories, removals).WillReturnRows(rows)
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

func TestLoadChallengeForAuthenticationBindsSessionAndReturnsChallenge(t *testing.T) {
	s, m := mockStore(t)
	clientNonce := bytes.Repeat([]byte{1}, 32)
	serverNonce := bytes.Repeat([]byte{2}, 32)
	ackDigest := bytes.Repeat([]byte{3}, 32)
	publicKey := bytes.Repeat([]byte{4}, 32)
	createdAt := fixedNow.Add(-time.Minute)
	expiresAt := fixedNow.Add(time.Minute)

	m.ExpectQuery("SELECT ch.controller_id::text").WithArgs(testSession).WillReturnRows(
		pgxmock.NewRows([]string{"controller_id", "key_id", "client_nonce", "server_nonce", "ack_digest", "created_at", "expires_at", "public_key"}).
			AddRow(testController, testKey, clientNonce, serverNonce, ackDigest, createdAt, expiresAt, publicKey),
	)

	got, err := s.LoadChallengeForAuthentication(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	want := AuthenticationChallenge{
		ChallengeInput: ChallengeInput{
			SessionID:    testSession,
			ControllerID: testController,
			KeyID:        testKey,
			ClientNonce:  clientNonce,
			ServerNonce:  serverNonce,
			ACKDigest:    ackDigest,
			ExpiresAt:    expiresAt,
		},
		PublicKey: publicKey,
		CreatedAt: createdAt,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("challenge=%#v, want %#v", got, want)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
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
	expectTopologyShards(m, bindingTopologyShard(event.InstallationID), routeTopologyShard(event.InstallationID, event.RepositoryID, event.Ref))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(event.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(event.DeliveryID, event.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(event.InstallationID, event.RepositoryID, event.Ref).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "subscription_id"}))
	m.ExpectQuery("SELECT subscription_id::text,generation FROM relay_desired_states").WithArgs([]string{}).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "generation"}))
	m.ExpectExec("INSERT INTO relay_source_delivery_targets").WithArgs(event.DeliveryID, []string{}, []int64{}, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	m.ExpectQuery("WITH input AS.*INSERT INTO relay_desired_states").WithArgs([]string{}, []string{}, []int64{}, event.DeliveryID, event.InstallationID, event.RepositoryID, event.Ref, event.SHA, event.ObservedAt).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "generation"}))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(event.DeliveryID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()
	result, err := s.PushSourceEvent(context.Background(), event, []SourceRoute{})
	if err != nil || result.Deduplicated {
		t.Fatalf("first=%#v %v", result, err)
	}
	m.ExpectBegin()
	expectTopologyShards(m, bindingTopologyShard(event.InstallationID), routeTopologyShard(event.InstallationID, event.RepositoryID, event.Ref), subscriptionTopologyShard(testSubscription))
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
	expectTopologyShards(m, bindingTopologyShard(event.InstallationID), routeTopologyShard(event.InstallationID, event.RepositoryID, event.Ref))
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
	expectTopologyShards(m, bindingTopologyShard(event.InstallationID), routeTopologyShard(event.InstallationID, event.RepositoryID, event.Ref))
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
		batchEvents := []AccessEventBatchItem{{InstallationID: 1, ChangeCode: event.ChangeCode, ObservedAt: fixedNow, RemoveAccess: true, Routes: routes}}
		m.ExpectBegin()
		expectAccessRouteSnapshot(m, batchEvents, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		expectTopologyShards(m, bindingTopologyShard(1))
		expectAccessRouteSnapshot(m, batchEvents, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs([]int64{1}, []int64{0}, []bool{true}).WillReturnRows(pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(1), testController, int64(2)).AddRow(int64(1), testController2, int64(3)))
		m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testDelivery, []string{testEvent, testEvent2}, []string{testController, testController2}, []int64{1, 1}, []int64{0, 0}, []string{event.ChangeCode, event.ChangeCode}, []time.Time{fixedNow, fixedNow}).WillReturnError(outage)
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
		lease := Lease{ControllerID: testController, SessionID: testSession, LeaseID: testLease, Fence: 1, ExpiresAt: fixedNow.Add(time.Minute)}
		m.ExpectQuery("SELECT a.event_id::text").WithArgs(testController, testSession, testLease, uint64(1), fixedNow, 10).WillReturnRows(pgxmock.NewRows([]string{"event_id", "delivery_id", "controller_id", "installation_id", "repository_id", "change_code", "observed_at"}).AddRow(testEvent, testDelivery, testController, int64(1), int64(0), "installation.removed", fixedNow))
		items, err := s.PendingAccess(context.Background(), lease, 10)
		if err != nil || len(items) != 1 || items[0].EventID != testEvent {
			t.Fatalf("items=%#v err=%v", items, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPendingDeliveriesRequireExactLiveLeaseFence(t *testing.T) {
	lease := Lease{ControllerID: testController, SessionID: testSession, LeaseID: testLease, Fence: 7, ExpiresAt: fixedNow.Add(time.Minute)}
	for _, test := range []struct {
		name string
		call func(*Store) (int, error)
	}{
		{name: "source", call: func(store *Store) (int, error) {
			items, err := store.PendingDesired(context.Background(), lease, 10)
			return len(items), err
		}},
		{name: "access", call: func(store *Store) (int, error) {
			items, err := store.PendingAccess(context.Background(), lease, 10)
			return len(items), err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectQuery("FROM relay_controller_leases l").WithArgs(testController, testSession, testLease, uint64(7), fixedNow, 10).WillReturnRows(pgxmock.NewRows([]string{"id"}))
			count, err := test.call(s)
			if err != nil || count != 0 {
				t.Fatalf("count=%d error=%v", count, err)
			}
			if err = m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	invalid := lease
	invalid.Fence = 0
	s, m := mockStore(t)
	if _, err := s.PendingDesired(context.Background(), invalid, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid lease error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAccessFanoutRowsErrorRollsBackBeforeChildrenOrRevocation(t *testing.T) {
	s, m := mockStore(t)
	event := AccessEventInput{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ReceivedAt: fixedNow, ObservedAt: fixedNow, RemoveAccess: true}
	batchEvents := []AccessEventBatchItem{{InstallationID: 1, RepositoryID: 2, ChangeCode: event.ChangeCode, ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}}}
	rowsErr := errors.New("access route iteration failed")
	m.ExpectBegin()
	expectAccessRouteSnapshot(m, batchEvents, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	expectTopologyShards(m, bindingTopologyShard(1))
	expectAccessRouteSnapshot(m, batchEvents, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs([]int64{1}, []int64{2}, []bool{true}).WillReturnRows(pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(1), testController, int64(2)).RowError(0, rowsErr))
	m.ExpectRollback()
	_, err := s.PushAccessEvent(context.Background(), event, []AccessRoute{{EventID: testEvent, ControllerID: testController}})
	if !errors.Is(err, rowsErr) {
		t.Fatalf("error=%v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
		replay        bool
	}{
		{name: "signed request replay", want: ErrReplay, replay: true},
		{name: "unrelated unique violation", databaseError: &pgconn.PgError{Code: "23505", ConstraintName: "relay_enrollments_state_hash_key"}},
		{name: "database outage", databaseError: errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectBegin()
			m.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(enrollmentCapacityLock).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			m.ExpectExec("UPDATE relay_enrollments SET status='expired'").WithArgs(fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			m.ExpectQuery("SELECT EXISTS").WithArgs(input.ControllerID, input.KeyID, input.RequestNonce).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(test.replay))
			if !test.replay {
				m.ExpectQuery("SELECT count").WithArgs(fixedNow).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
				m.ExpectExec("INSERT INTO relay_enrollments").WithArgs(
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				).WillReturnError(test.databaseError)
			}
			m.ExpectRollback()
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
			{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}},
			{InstallationID: 1, RepositoryID: 3, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: testEvent2, ControllerID: testController2}}},
		},
	}
	s, m := mockStore(t)
	installations, repositories, removals := accessBatchTargetArgs(batch.Events)
	m.ExpectBegin()
	expectAccessRouteSnapshot(m, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	expectTopologyShards(m, bindingTopologyShard(1))
	expectAccessRouteSnapshot(m, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs(installations, repositories, removals).WillReturnRows(pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(1), testController, int64(2)).AddRow(int64(2), testController2, int64(3)))
	m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testDelivery, []string{testEvent, testEvent2}, []string{testController, testController2}, []int64{1, 1}, []int64{2, 3}, []string{"repository.removed", "repository.removed"}, []time.Time{fixedNow, fixedNow}).WillReturnResult(pgxmock.NewResult("INSERT", 2))
	m.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installations, repositories, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installations, repositories, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()
	if result, err := s.PushAccessEvents(context.Background(), batch); err != nil || result.Deduplicated {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushAccessEventsPersistsInformationalDeliveryWithoutFanout(t *testing.T) {
	batch := AccessEventBatchInput{
		DeliveryID: testDelivery,
		ReceivedAt: fixedNow,
		Events: []AccessEventBatchItem{{
			InstallationID: 1,
			RepositoryID:   2,
			ChangeCode:     "repository.added",
			ObservedAt:     fixedNow,
		}},
	}
	s, m := mockStore(t)
	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()

	result, err := s.PushAccessEvents(context.Background(), batch)
	if err != nil || result.Deduplicated {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushAccessEventsFiltersInformationalTargetsFromRemovalFanout(t *testing.T) {
	batch := AccessEventBatchInput{
		DeliveryID: testDelivery,
		ReceivedAt: fixedNow,
		Events: []AccessEventBatchItem{
			{InstallationID: 3, RepositoryID: 4, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}},
			{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.added", ObservedAt: fixedNow},
		},
	}
	installations := []int64{1, 3}
	repositories := []int64{2, 4}
	removals := []bool{false, true}
	s, m := mockStore(t)
	m.ExpectBegin()
	expectAccessRouteSnapshot(m, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	expectTopologyShards(m, bindingTopologyShard(3))
	expectAccessRouteSnapshot(m, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectQuery("WITH targets AS .*remove_access.*WHERE t.remove_access AND b.revoked_at").WithArgs(installations, repositories, removals).WillReturnRows(
		pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(2), testController, int64(4)),
	)
	m.ExpectExec("INSERT INTO relay_access_events").WithArgs(testDelivery, []string{testEvent}, []string{testController}, []int64{3}, []int64{4}, []string{"repository.removed"}, []time.Time{fixedNow}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("WITH targets AS .*WHERE t.remove_access AND t.repository_id>0").WithArgs(installations, repositories, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("WITH targets AS .*WHERE t.remove_access AND t.repository_id=0").WithArgs(installations, repositories, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectCommit()

	result, err := s.PushAccessEvents(context.Background(), batch)
	if err != nil || result.Deduplicated {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushAccessEventsRejectsRoutesForInformationalEvent(t *testing.T) {
	s, m := mockStore(t)
	_, err := s.PushAccessEvents(context.Background(), AccessEventBatchInput{
		DeliveryID: testDelivery,
		ReceivedAt: fixedNow,
		Events: []AccessEventBatchItem{{
			InstallationID: 1,
			ChangeCode:     "installation.restored",
			ObservedAt:     fixedNow,
			Routes:         []AccessRoute{{EventID: testEvent, ControllerID: testController}},
		}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushAccessEventsRejectsAmbiguousTargetsBeforeDatabaseWork(t *testing.T) {
	for _, events := range [][]AccessEventBatchItem{
		{{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.added", ObservedAt: fixedNow}, {InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true}},
		{{InstallationID: 1, RepositoryID: 0, ChangeCode: "installation.removed", ObservedAt: fixedNow, RemoveAccess: true}, {InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true}},
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
		expectTopologyShards(m, bindingTopologyShard(1), routeTopologyShard(1, 2, event.Ref))
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
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		expectTopologyShards(m, bindingTopologyShard(1))
		m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		m.ExpectQuery("SELECT controller_id,key_id,public_key").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "key_id", "public_key", "installation_id", "repository_id", "expires_at", "status"}).AddRow(testController, testKey, publicKey, int64(1), int64(2), fixedNow.Add(time.Minute), "state_claimed"))
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
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	expectTopologyShards(m, bindingTopologyShard(1))
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(1), int64(2)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectQuery("SELECT controller_id,key_id,public_key").WithArgs(testDelivery).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "key_id", "public_key", "installation_id", "repository_id", "expires_at", "status"}).AddRow(testController, testKey, publicKey, int64(1), int64(2), fixedNow.Add(time.Minute), "state_claimed"))
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
		m.ExpectQuery("SELECT controller_id::text FROM relay_sessions").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller"}).AddRow(testController))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(testController)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT l.session_id::text,l.fence,l.expires_at,s.expires_at,s.revoked_at").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"session", "fence", "lease_expires", "session_expires", "session_revoked"}).AddRow(testMessage, int64(3), fixedNow.Add(time.Minute), fixedNow.Add(time.Hour), nil))
		m.ExpectQuery("SELECT s.controller_id::text,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller", "expires", "revoked", "controller_state", "key_state"}).AddRow(testController, fixedNow.Add(time.Hour), nil, "active", "active"))
		m.ExpectRollback()
		if _, err := s.AcquireLease(context.Background(), testSession, time.Minute); !errors.Is(err, ErrConflict) {
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
		m.ExpectQuery("SELECT controller_id::text FROM relay_sessions").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller"}).AddRow(testController))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(testController)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT l.session_id::text,l.fence,l.expires_at,s.expires_at,s.revoked_at").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"session", "fence", "lease_expires", "session_expires", "session_revoked"}).AddRow(testMessage, int64(3), fixedNow.Add(-time.Second), fixedNow.Add(time.Hour), nil))
		m.ExpectQuery("SELECT s.controller_id::text,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller", "expires", "revoked", "controller_state", "key_state"}).AddRow(testController, fixedNow.Add(time.Hour), nil, "active", "active"))
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
	t.Run("revoked prior session permits unexpired lease takeover", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id::text FROM relay_sessions").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller"}).AddRow(testController))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(testController)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT l.session_id::text,l.fence,l.expires_at,s.expires_at,s.revoked_at").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"session", "fence", "lease_expires", "session_expires", "session_revoked"}).AddRow(testMessage, int64(3), fixedNow.Add(time.Minute), fixedNow.Add(time.Hour), fixedNow.Add(-time.Second)))
		m.ExpectQuery("SELECT s.controller_id::text,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller", "expires", "revoked", "controller_state", "key_state"}).AddRow(testController, fixedNow.Add(time.Hour), nil, "active", "active"))
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
	t.Run("expired prior session permits unexpired lease takeover", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id::text FROM relay_sessions").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller"}).AddRow(testController))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(testController)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT l.session_id::text,l.fence,l.expires_at,s.expires_at,s.revoked_at").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"session", "fence", "lease_expires", "session_expires", "session_revoked"}).AddRow(testMessage, int64(3), fixedNow.Add(time.Minute), fixedNow.Add(-time.Second), nil))
		m.ExpectQuery("SELECT s.controller_id::text,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(testSession).WillReturnRows(pgxmock.NewRows([]string{"controller", "expires", "revoked", "controller_state", "key_state"}).AddRow(testController, fixedNow.Add(time.Hour), nil, "active", "active"))
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
