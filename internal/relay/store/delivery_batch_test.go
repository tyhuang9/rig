package store

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestPushSourceEventMaxFanoutUsesTenRoundTripsAndBatchedWrites(t *testing.T) {
	if freshSourcePushRoundTrips != 10 {
		t.Fatalf("fresh source round trips=%d want 10", freshSourcePushRoundTrips)
	}
	event := SourceEvent{DeliveryID: testDelivery, InstallationID: 41, RepositoryID: 42, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), ReceivedAt: fixedNow, ObservedAt: fixedNow}
	routes := make([]SourceRoute, 1000)
	subscriptionIDs := make([]string, len(routes))
	controllerIDs := make([]string, len(routes))
	currentGenerations := make([]int64, len(routes))
	nextGenerations := make([]int64, len(routes))
	authorized := pgxmock.NewRows([]string{"controller_id", "subscription_id"})
	current := pgxmock.NewRows([]string{"subscription_id", "generation"})
	upserted := pgxmock.NewRows([]string{"subscription_id", "generation"})
	locks := newTopologyLockSet()
	locks.addBinding(event.InstallationID)
	locks.addRoute(event.InstallationID, event.RepositoryID, event.Ref)
	for i := range routes {
		controllerID := fmt.Sprintf("10000000-0000-4000-8000-%012x", i+1)
		subscriptionID := fmt.Sprintf("20000000-0000-4000-8000-%012x", i+1)
		routes[i] = SourceRoute{ControllerID: controllerID, SubscriptionID: subscriptionID}
		controllerIDs[i] = controllerID
		subscriptionIDs[i] = subscriptionID
		currentGenerations[i] = int64(i + 1)
		nextGenerations[i] = currentGenerations[i] + 1
		authorized.AddRow(controllerID, subscriptionID)
		current.AddRow(subscriptionID, currentGenerations[i])
		upserted.AddRow(subscriptionID, nextGenerations[i])
		locks.addSubscription(subscriptionID)
	}

	s, mock := mockStore(t)
	mock.ExpectBegin()
	expectTopologyShards(mock, locks.shardIDs()...)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(event.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(event.DeliveryID, event.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(event.InstallationID, event.RepositoryID, event.Ref).WillReturnRows(authorized)
	mock.ExpectQuery("SELECT subscription_id::text,generation FROM relay_desired_states").WithArgs(subscriptionIDs).WillReturnRows(current)
	mock.ExpectExec("INSERT INTO relay_source_delivery_targets").WithArgs(event.DeliveryID, subscriptionIDs, nextGenerations, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1000))
	mock.ExpectQuery("WITH input AS.*INSERT INTO relay_desired_states").WithArgs(subscriptionIDs, controllerIDs, nextGenerations, event.DeliveryID, event.InstallationID, event.RepositoryID, event.Ref, event.SHA, event.ObservedAt).WillReturnRows(upserted)
	mock.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(event.DeliveryID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	result, err := s.PushSourceEvent(context.Background(), event, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Desired) != len(routes) {
		t.Fatalf("desired=%d want %d", len(result.Desired), len(routes))
	}
	for i, desired := range result.Desired {
		if desired.SubscriptionID != subscriptionIDs[i] || desired.ControllerID != controllerIDs[i] || desired.Generation != uint64(nextGenerations[i]) {
			t.Fatalf("desired[%d]=%+v", i, desired)
		}
	}
	if result.Desired[len(result.Desired)-1].Generation != uint64(currentGenerations[len(currentGenerations)-1]+1) {
		t.Fatal("highest prior generation did not increment exactly once")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushAccessEventsMaxFanoutUsesTwelveRoundTripsAndBatchedWrites(t *testing.T) {
	if freshAccessPushRoundTrips != 12 {
		t.Fatalf("fresh access round trips=%d want 12", freshAccessPushRoundTrips)
	}
	batch := AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: make([]AccessEventBatchItem, 1000)}
	accessEventIDs := make([]string, len(batch.Events))
	controllerIDs := make([]string, len(batch.Events))
	installationIDs := make([]int64, len(batch.Events))
	repositoryIDs := make([]int64, len(batch.Events))
	changeCodes := make([]string, len(batch.Events))
	observedAt := make([]time.Time, len(batch.Events))
	removals := make([]bool, len(batch.Events))
	routeRowsBefore := pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"})
	routeRowsAfter := pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"})
	authorized := pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"})
	locks := newTopologyLockSet()
	for i := range batch.Events {
		installationID := int64(i + 1)
		repositoryID := int64(i + 1001)
		controllerID := fmt.Sprintf("30000000-0000-4000-8000-%012x", i+1)
		eventID := fmt.Sprintf("40000000-0000-4000-8000-%012x", i+1)
		batch.Events[i] = AccessEventBatchItem{InstallationID: installationID, RepositoryID: repositoryID, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: eventID, ControllerID: controllerID}}}
		accessEventIDs[i] = eventID
		controllerIDs[i] = controllerID
		installationIDs[i] = installationID
		repositoryIDs[i] = repositoryID
		changeCodes[i] = "repository.removed"
		observedAt[i] = fixedNow
		removals[i] = true
		routeRowsBefore.AddRow(installationID, repositoryID, "refs/heads/main")
		routeRowsAfter.AddRow(installationID, repositoryID, "refs/heads/main")
		authorized.AddRow(int64(i+1), controllerID, repositoryID)
		locks.addBinding(installationID)
		locks.addRoute(installationID, repositoryID, "refs/heads/main")
	}

	s, mock := mockStore(t)
	mock.ExpectBegin()
	expectAccessRouteSnapshot(mock, batch.Events, routeRowsBefore)
	expectTopologyShards(mock, locks.shardIDs()...)
	expectAccessRouteSnapshot(mock, batch.Events, routeRowsAfter)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(deliveryLockKey(batch.DeliveryID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(batch.DeliveryID, batch.ReceivedAt, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs(installationIDs, repositoryIDs).WillReturnRows(authorized)
	mock.ExpectExec("INSERT INTO relay_access_events").WithArgs(batch.DeliveryID, accessEventIDs, controllerIDs, installationIDs, repositoryIDs, changeCodes, observedAt).WillReturnResult(pgxmock.NewResult("INSERT", 1000))
	mock.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installationIDs, repositoryIDs, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1000))
	mock.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installationIDs, repositoryIDs, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(batch.DeliveryID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	result, err := s.PushAccessEvents(context.Background(), batch)
	if err != nil || result.Deduplicated {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceBatchAuthorizationAndBulkCardinalityFailClosed(t *testing.T) {
	event := SourceEvent{DeliveryID: testDelivery, InstallationID: 1, RepositoryID: 2, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), ReceivedAt: fixedNow, ObservedAt: fixedNow}
	route := SourceRoute{ControllerID: testController, SubscriptionID: testSubscription}

	t.Run("wrong controller", func(t *testing.T) {
		s, mock := mockStore(t)
		mock.ExpectBegin()
		expectTopologyShards(mock, bindingTopologyShard(1), routeTopologyShard(1, 2, event.Ref), subscriptionTopologyShard(testSubscription))
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(int64(1), int64(2), event.Ref).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "subscription_id"}).AddRow(testController2, testSubscription))
		mock.ExpectRollback()
		if _, err := s.PushSourceEvent(context.Background(), event, []SourceRoute{route}); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name       string
		targetRows int64
		upsertRows *pgxmock.Rows
		upsertErr  error
	}{
		{name: "target cardinality", targetRows: 0},
		{name: "upsert cardinality", targetRows: 1, upsertRows: pgxmock.NewRows([]string{"subscription_id", "generation"})},
		{name: "upsert failure rolls back target", targetRows: 1, upsertErr: errors.New("upsert unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, mock := mockStore(t)
			mock.ExpectBegin()
			expectTopologyShards(mock, bindingTopologyShard(1), routeTopologyShard(1, 2, event.Ref), subscriptionTopologyShard(testSubscription))
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectQuery("SELECT s.controller_id::text,s.subscription_id::text").WithArgs(int64(1), int64(2), event.Ref).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "subscription_id"}).AddRow(testController, testSubscription))
			mock.ExpectQuery("SELECT subscription_id::text,generation FROM relay_desired_states").WithArgs([]string{testSubscription}).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "generation"}).AddRow(testSubscription, int64(7)))
			mock.ExpectExec("INSERT INTO relay_source_delivery_targets").WithArgs(testDelivery, []string{testSubscription}, []int64{8}, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", test.targetRows))
			if test.targetRows == 1 {
				expectation := mock.ExpectQuery("WITH input AS.*INSERT INTO relay_desired_states").WithArgs([]string{testSubscription}, []string{testController}, []int64{8}, testDelivery, int64(1), int64(2), event.Ref, event.SHA, fixedNow)
				if test.upsertErr != nil {
					expectation.WillReturnError(test.upsertErr)
				} else {
					expectation.WillReturnRows(test.upsertRows)
				}
			}
			mock.ExpectRollback()
			_, err := s.PushSourceEvent(context.Background(), event, []SourceRoute{route})
			if test.upsertErr != nil {
				if !errors.Is(err, test.upsertErr) {
					t.Fatalf("error=%v", err)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Fatalf("error=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccessBatchAuthorizationDedupeAndBulkFailures(t *testing.T) {
	t.Run("installation-wide controller is deduplicated across repositories", func(t *testing.T) {
		batch := AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: []AccessEventBatchItem{{InstallationID: 1, ChangeCode: "installation.removed", ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}}}}
		installations, repositories, removals := accessBatchTargetArgs(batch.Events)
		s, mock := mockStore(t)
		mock.ExpectBegin()
		expectAccessRouteSnapshot(mock, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		expectTopologyShards(mock, bindingTopologyShard(1))
		expectAccessRouteSnapshot(mock, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs(installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(1), testController, int64(2)).AddRow(int64(1), testController, int64(3)))
		mock.ExpectExec("INSERT INTO relay_access_events").WithArgs(testDelivery, []string{testEvent}, []string{testController}, []int64{1}, []int64{0}, []string{"installation.removed"}, []time.Time{fixedNow}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installations, repositories, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		mock.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installations, repositories, removals, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 2))
		mock.ExpectExec("UPDATE relay_recovery_deliveries").WithArgs(testDelivery, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		mock.ExpectCommit()
		if _, err := s.PushAccessEvents(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unauthorized route", func(t *testing.T) {
		batch := AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: []AccessEventBatchItem{{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}}}}
		installations, repositories, _ := accessBatchTargetArgs(batch.Events)
		s, mock := mockStore(t)
		mock.ExpectBegin()
		expectAccessRouteSnapshot(mock, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		expectTopologyShards(mock, bindingTopologyShard(1))
		expectAccessRouteSnapshot(mock, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs(installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(1), testController2, int64(2)))
		mock.ExpectRollback()
		if _, err := s.PushAccessEvents(context.Background(), batch); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name            string
		insertRows      int64
		insertErr       error
		repositoryErr   error
		installationErr error
		wantConflict    bool
	}{
		{name: "access insert cardinality", insertRows: 0, wantConflict: true},
		{name: "access insert failure", insertErr: errors.New("access insert unavailable")},
		{name: "repository revoke failure rolls back event", insertRows: 1, repositoryErr: errors.New("repository revoke unavailable")},
		{name: "installation revoke failure rolls back prior mutations", insertRows: 1, installationErr: errors.New("installation revoke unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			batch := AccessEventBatchInput{DeliveryID: testDelivery, ReceivedAt: fixedNow, Events: []AccessEventBatchItem{{InstallationID: 1, RepositoryID: 2, ChangeCode: "repository.removed", ObservedAt: fixedNow, RemoveAccess: true, Routes: []AccessRoute{{EventID: testEvent, ControllerID: testController}}}}}
			installations, repositories, removals := accessBatchTargetArgs(batch.Events)
			s, mock := mockStore(t)
			mock.ExpectBegin()
			expectAccessRouteSnapshot(mock, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
			expectTopologyShards(mock, bindingTopologyShard(1))
			expectAccessRouteSnapshot(mock, batch.Events, pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(deliveryLockKey(testDelivery)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec("INSERT INTO relay_github_deliveries").WithArgs(testDelivery, fixedNow, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectQuery("WITH targets AS .*SELECT t.target_index").WithArgs(installations, repositories).WillReturnRows(pgxmock.NewRows([]string{"target_index", "controller_id", "repository_id"}).AddRow(int64(1), testController, int64(2)))
			insert := mock.ExpectExec("INSERT INTO relay_access_events").WithArgs(testDelivery, []string{testEvent}, []string{testController}, []int64{1}, []int64{2}, []string{"repository.removed"}, []time.Time{fixedNow})
			if test.insertErr != nil {
				insert.WillReturnError(test.insertErr)
			} else {
				insert.WillReturnResult(pgxmock.NewResult("INSERT", test.insertRows))
			}
			if test.insertErr == nil && test.insertRows == 1 {
				repositoryUpdate := mock.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installations, repositories, removals, fixedNow)
				if test.repositoryErr != nil {
					repositoryUpdate.WillReturnError(test.repositoryErr)
				} else {
					repositoryUpdate.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					installationUpdate := mock.ExpectExec("WITH targets AS .*UPDATE relay_bindings b SET revoked_at").WithArgs(installations, repositories, removals, fixedNow)
					if test.installationErr != nil {
						installationUpdate.WillReturnError(test.installationErr)
					} else {
						installationUpdate.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
					}
				}
			}
			mock.ExpectRollback()
			_, err := s.PushAccessEvents(context.Background(), batch)
			if test.wantConflict {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("error=%v", err)
				}
			} else {
				want := test.insertErr
				if want == nil {
					want = test.repositoryErr
				}
				if want == nil {
					want = test.installationErr
				}
				if !errors.Is(err, want) {
					t.Fatalf("error=%v want %v", err, want)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeliveryFanoutSQLIsSetBasedAndUUIDTextArraysAreExplicitlyCast(t *testing.T) {
	file := "delivery.go"
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || (function.Name.Name != "PushSourceEvent" && function.Name.Name != "PushAccessEvents") {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			loop, ok := node.(*ast.RangeStmt)
			if !ok {
				return true
			}
			ast.Inspect(loop.Body, func(loopNode ast.Node) bool {
				call, ok := loopNode.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if ok && identifier.Name == "tx" {
					t.Errorf("%s issues tx.%s inside a fanout loop", function.Name.Name, selector.Sel.Name)
				}
				return true
			})
			return false
		})
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, "::uuid[]") {
		t.Fatal("delivery batching relies on driver-specific []string to uuid[] encoding")
	}
	if strings.Contains(source, "FOR UPDATE OF s,b,c") || strings.Contains(source, "FOR UPDATE OF b,c") {
		t.Fatal("fanout auth locks controller rows outside the topology-shard order")
	}
	for _, required := range []string{"unnest($1::text[])", "input.subscription_id::uuid", "input.controller_id::uuid", "input.event_id::uuid"} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing explicit text-array UUID cast %q", required)
		}
	}
}
