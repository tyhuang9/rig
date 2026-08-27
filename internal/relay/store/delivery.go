package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

type SourceEvent struct {
	DeliveryID     string
	InstallationID int64
	RepositoryID   int64
	Ref            string
	SHA            string
	ReceivedAt     time.Time
	ObservedAt     time.Time
}
type SourceRoute struct {
	ControllerID   string
	SubscriptionID string
}
type DesiredState struct {
	DeliveryID     string
	ControllerID   string
	SubscriptionID string
	Generation     uint64
	InstallationID int64
	RepositoryID   int64
	Ref            string
	SHA            string
	ObservedAt     time.Time
}
type SourcePushResult struct {
	Deduplicated bool
	Desired      []DesiredState
}

// A fresh source push uses exactly ten database round trips: Begin, eight
// statements, and Commit. The count does not vary with the number of routes.
const freshSourcePushRoundTrips = 10

// A fresh access push uses exactly twelve database round trips: Begin, ten
// statements, and Commit. The count does not vary with the number of events or
// access routes.
const freshAccessPushRoundTrips = 12

func (s *Store) PushIgnoredDelivery(ctx context.Context, deliveryID, reasonCode string, receivedAt time.Time) (bool, error) {
	if !validUUID(deliveryID) || !validIgnoredReason(reasonCode) || !validTime(receivedAt) {
		return false, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, deliveryLockKey(deliveryID)); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO relay_github_deliveries(delivery_id,delivery_kind,received_at,persisted_at) VALUES($1,'ignored',$2,$3) ON CONFLICT(delivery_id) DO NOTHING`, deliveryID, receivedAt.UTC(), now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		var kind string
		var storedReason sql.NullString
		if err = tx.QueryRow(ctx, `SELECT d.delivery_kind,i.reason_code FROM relay_github_deliveries d LEFT JOIN relay_ignored_deliveries i ON i.delivery_id=d.delivery_id WHERE d.delivery_id=$1`, deliveryID).Scan(&kind, &storedReason); err != nil {
			return false, err
		}
		if kind != "ignored" || !storedReason.Valid || storedReason.String != reasonCode {
			return false, ErrConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, deliveryID, now); err != nil {
			return false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO relay_ignored_deliveries(delivery_id,reason_code,persisted_at) VALUES($1,$2,$3)`, deliveryID, reasonCode, now); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, deliveryID, now); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return false, nil
}

func validIgnoredReason(reason string) bool {
	switch reason {
	case "push.deleted", "push.untracked_ref", "installation.unsupported_action", "installation_repositories.unsupported_action":
		return true
	default:
		return false
	}
}

// PushSourceEvent persists a complete fan-out in one transaction. A repeated
// GitHub GUID is a no-op; it can never append or alter child targets.
func (s *Store) PushSourceEvent(ctx context.Context, event SourceEvent, routes []SourceRoute) (SourcePushResult, error) {
	if !validUUID(event.DeliveryID) || event.InstallationID <= 0 || event.RepositoryID <= 0 || !validTime(event.ReceivedAt) || !validTime(event.ObservedAt) || validateRefSHA(event.Ref, event.SHA) != nil || len(routes) > 1000 {
		return SourcePushResult{}, ErrInvalid
	}
	routes = append([]SourceRoute(nil), routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].SubscriptionID < routes[j].SubscriptionID })
	for i, route := range routes {
		if !validUUID(route.ControllerID) || !validUUID(route.SubscriptionID) || (i > 0 && routes[i-1].SubscriptionID == route.SubscriptionID) {
			return SourcePushResult{}, ErrInvalid
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SourcePushResult{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	topologyLocks := newTopologyLockSet()
	topologyLocks.addBinding(event.InstallationID)
	topologyLocks.addRoute(event.InstallationID, event.RepositoryID, event.Ref)
	for _, route := range routes {
		topologyLocks.addSubscription(route.SubscriptionID)
	}
	if err = acquireTopologyLocks(ctx, tx, topologyLocks); err != nil {
		return SourcePushResult{}, err
	}
	// Global order is topology shards -> delivery -> rows. Recovery takes only
	// the delivery lock, closing the new-GUID inbound/discovery lost-update race.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, deliveryLockKey(event.DeliveryID)); err != nil {
		return SourcePushResult{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO relay_github_deliveries(delivery_id,delivery_kind,received_at,persisted_at) VALUES($1,'source',$2,$3) ON CONFLICT(delivery_id) DO NOTHING`, event.DeliveryID, event.ReceivedAt.UTC(), now)
	if err != nil {
		return SourcePushResult{}, err
	}
	if tag.RowsAffected() == 0 {
		var kind string
		if err = tx.QueryRow(ctx, `SELECT delivery_kind FROM relay_github_deliveries WHERE delivery_id=$1`, event.DeliveryID).Scan(&kind); err != nil {
			return SourcePushResult{}, err
		}
		if kind != "source" {
			return SourcePushResult{}, ErrConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, event.DeliveryID, now); err != nil {
			return SourcePushResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return SourcePushResult{}, err
		}
		return SourcePushResult{Deduplicated: true}, nil
	}
	expectedRows, err := tx.Query(ctx, `SELECT s.controller_id::text,s.subscription_id::text FROM relay_subscriptions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_bindings b ON b.controller_id=s.controller_id AND b.installation_id=s.installation_id AND b.repository_id=s.repository_id WHERE s.installation_id=$1 AND s.repository_id=$2 AND s.tracked_ref=$3 AND s.retired_generation IS NULL AND c.state='active' AND b.revoked_at IS NULL ORDER BY s.subscription_id FOR UPDATE OF s,b`, event.InstallationID, event.RepositoryID, event.Ref)
	if err != nil {
		return SourcePushResult{}, err
	}
	expected := map[string]string{}
	for expectedRows.Next() {
		var controllerID, subscriptionID string
		if err = expectedRows.Scan(&controllerID, &subscriptionID); err != nil {
			expectedRows.Close()
			return SourcePushResult{}, err
		}
		expected[subscriptionID] = controllerID
	}
	expectedRows.Close()
	if expectedRows.Err() != nil {
		return SourcePushResult{}, expectedRows.Err()
	}
	if len(expected) != len(routes) {
		return SourcePushResult{}, ErrConflict
	}
	for _, route := range routes {
		if expected[route.SubscriptionID] != route.ControllerID {
			return SourcePushResult{}, ErrConflict
		}
	}
	subscriptionIDs := make([]string, len(routes))
	controllerIDs := make([]string, len(routes))
	for i, route := range routes {
		subscriptionIDs[i] = route.SubscriptionID
		controllerIDs[i] = route.ControllerID
	}
	generations := make([]int64, len(routes))
	generationRows, err := tx.Query(ctx, `SELECT subscription_id::text,generation FROM relay_desired_states WHERE subscription_id=ANY(SELECT value::uuid FROM unnest($1::text[]) AS requested(value)) ORDER BY subscription_id FOR UPDATE`, subscriptionIDs)
	if err != nil {
		return SourcePushResult{}, err
	}
	currentGenerations := make(map[string]int64, len(routes))
	for generationRows.Next() {
		var subscriptionID string
		var generation int64
		if err = generationRows.Scan(&subscriptionID, &generation); err != nil {
			generationRows.Close()
			return SourcePushResult{}, err
		}
		currentGenerations[subscriptionID] = generation
	}
	generationRows.Close()
	if err = generationRows.Err(); err != nil {
		return SourcePushResult{}, err
	}
	for i, subscriptionID := range subscriptionIDs {
		generations[i] = currentGenerations[subscriptionID] + 1
		if generations[i] <= 0 {
			return SourcePushResult{}, ErrConflict
		}
	}
	tag, err = tx.Exec(ctx, `INSERT INTO relay_source_delivery_targets(delivery_id,subscription_id,generation,persisted_at) SELECT $1,input.subscription_id::uuid,input.generation,$4 FROM unnest($2::text[],$3::bigint[]) AS input(subscription_id,generation)`, event.DeliveryID, subscriptionIDs, generations, now)
	if err != nil {
		return SourcePushResult{}, conflictError(err)
	}
	if tag.RowsAffected() != int64(len(routes)) {
		return SourcePushResult{}, ErrConflict
	}
	desiredRows, err := tx.Query(ctx, `WITH input AS (SELECT subscription_id::uuid AS subscription_id,controller_id::uuid AS controller_id,generation,ordinality FROM unnest($1::text[],$2::text[],$3::bigint[]) WITH ORDINALITY AS requested(subscription_id,controller_id,generation,ordinality)), upserted AS (INSERT INTO relay_desired_states(subscription_id,delivery_id,controller_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at) SELECT input.subscription_id,$4,input.controller_id,input.generation,$5,$6,$7,$8,$9 FROM input WHERE true ON CONFLICT(subscription_id) DO UPDATE SET delivery_id=EXCLUDED.delivery_id,generation=EXCLUDED.generation,observed_sha=EXCLUDED.observed_sha,observed_at=EXCLUDED.observed_at,decision=NULL,decision_code=NULL,decision_message_id=NULL,decided_at=NULL RETURNING subscription_id,generation) SELECT upserted.subscription_id::text,upserted.generation FROM upserted JOIN input USING(subscription_id) ORDER BY input.ordinality`, subscriptionIDs, controllerIDs, generations, event.DeliveryID, event.InstallationID, event.RepositoryID, event.Ref, event.SHA, event.ObservedAt.UTC())
	if err != nil {
		return SourcePushResult{}, conflictError(err)
	}
	result := SourcePushResult{Desired: make([]DesiredState, 0, len(routes))}
	for desiredRows.Next() {
		var subscriptionID string
		var generation int64
		if err = desiredRows.Scan(&subscriptionID, &generation); err != nil {
			desiredRows.Close()
			return SourcePushResult{}, err
		}
		index := len(result.Desired)
		if index >= len(routes) || subscriptionID != routes[index].SubscriptionID || generation != generations[index] {
			desiredRows.Close()
			return SourcePushResult{}, ErrConflict
		}
		result.Desired = append(result.Desired, DesiredState{DeliveryID: event.DeliveryID, ControllerID: routes[index].ControllerID, SubscriptionID: subscriptionID, Generation: uint64(generation), InstallationID: event.InstallationID, RepositoryID: event.RepositoryID, Ref: event.Ref, SHA: event.SHA, ObservedAt: event.ObservedAt.UTC()})
	}
	desiredRows.Close()
	if err = desiredRows.Err(); err != nil {
		return SourcePushResult{}, err
	}
	if len(result.Desired) != len(routes) {
		return SourcePushResult{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, event.DeliveryID, now); err != nil {
		return SourcePushResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SourcePushResult{}, err
	}
	return result, nil
}

type AccessEventInput struct {
	DeliveryID     string
	InstallationID int64
	RepositoryID   int64
	ChangeCode     string
	ReceivedAt     time.Time
	ObservedAt     time.Time
	RemoveAccess   bool
}
type AccessRoute struct {
	EventID      string
	ControllerID string
}
type AccessPushResult struct{ Deduplicated bool }

type AccessEventBatchItem struct {
	InstallationID int64
	RepositoryID   int64
	ChangeCode     string
	ObservedAt     time.Time
	RemoveAccess   bool
	Routes         []AccessRoute
}

type AccessEventBatchInput struct {
	DeliveryID string
	ReceivedAt time.Time
	Events     []AccessEventBatchItem
}

func (s *Store) PushAccessEvent(ctx context.Context, event AccessEventInput, routes []AccessRoute) (AccessPushResult, error) {
	return s.PushAccessEvents(ctx, AccessEventBatchInput{
		DeliveryID: event.DeliveryID,
		ReceivedAt: event.ReceivedAt,
		Events:     []AccessEventBatchItem{{InstallationID: event.InstallationID, RepositoryID: event.RepositoryID, ChangeCode: event.ChangeCode, ObservedAt: event.ObservedAt, RemoveAccess: event.RemoveAccess, Routes: routes}},
	})
}

func (s *Store) PushAccessEvents(ctx context.Context, batch AccessEventBatchInput) (AccessPushResult, error) {
	if !validUUID(batch.DeliveryID) || !validTime(batch.ReceivedAt) || len(batch.Events) == 0 || len(batch.Events) > 1000 {
		return AccessPushResult{}, ErrInvalid
	}
	events := append([]AccessEventBatchItem(nil), batch.Events...)
	targets := make(map[[2]int64]struct{}, len(events))
	installationWide := make(map[int64]bool)
	repositorySpecific := make(map[int64]bool)
	eventIDs := make(map[string]struct{})
	totalRoutes := 0
	for i := range events {
		events[i].Routes = append([]AccessRoute(nil), events[i].Routes...)
		item := &events[i]
		if item.InstallationID <= 0 || item.RepositoryID < 0 || !validCode(item.ChangeCode) || !validTime(item.ObservedAt) {
			return AccessPushResult{}, ErrInvalid
		}
		if !item.RemoveAccess && len(item.Routes) != 0 {
			return AccessPushResult{}, ErrInvalid
		}
		target := [2]int64{item.InstallationID, item.RepositoryID}
		if _, exists := targets[target]; exists {
			return AccessPushResult{}, ErrInvalid
		}
		targets[target] = struct{}{}
		if item.RepositoryID == 0 {
			installationWide[item.InstallationID] = true
		} else {
			repositorySpecific[item.InstallationID] = true
		}
		if installationWide[item.InstallationID] && repositorySpecific[item.InstallationID] {
			return AccessPushResult{}, ErrInvalid
		}
		controllerSet := map[string]struct{}{}
		for _, route := range item.Routes {
			if !validUUID(route.EventID) || !validUUID(route.ControllerID) {
				return AccessPushResult{}, ErrInvalid
			}
			if _, exists := controllerSet[route.ControllerID]; exists {
				return AccessPushResult{}, ErrInvalid
			}
			if _, exists := eventIDs[route.EventID]; exists {
				return AccessPushResult{}, ErrInvalid
			}
			controllerSet[route.ControllerID] = struct{}{}
			eventIDs[route.EventID] = struct{}{}
			totalRoutes++
			if totalRoutes > 1000 {
				return AccessPushResult{}, ErrInvalid
			}
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].InstallationID != events[j].InstallationID {
			return events[i].InstallationID < events[j].InstallationID
		}
		return events[i].RepositoryID < events[j].RepositoryID
	})
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AccessPushResult{}, err
	}
	defer rollback(ctx, tx)
	now := s.now().UTC()
	installationIDs := make([]int64, len(events))
	repositoryIDs := make([]int64, len(events))
	removeAccess := make([]bool, len(events))
	topologyLocks := newTopologyLockSet()
	hasRemovals := false
	for i, item := range events {
		installationIDs[i] = item.InstallationID
		repositoryIDs[i] = item.RepositoryID
		removeAccess[i] = item.RemoveAccess
		if item.RemoveAccess {
			hasRemovals = true
			topologyLocks.addBinding(item.InstallationID)
		}
	}
	if hasRemovals {
		keys, routeErr := queryRouteTopologyKeys(ctx, tx, `WITH targets AS (SELECT installation_id,repository_id,remove_access FROM unnest($1::bigint[],$2::bigint[],$3::boolean[]) AS input(installation_id,repository_id,remove_access)) SELECT DISTINCT s.installation_id,s.repository_id,s.tracked_ref FROM targets t JOIN relay_subscriptions s ON s.installation_id=t.installation_id AND (t.repository_id=0 OR s.repository_id=t.repository_id) WHERE t.remove_access AND s.retired_generation IS NULL ORDER BY s.installation_id,s.repository_id,s.tracked_ref`, installationIDs, repositoryIDs, removeAccess)
		if routeErr != nil {
			return AccessPushResult{}, routeErr
		}
		topologyLocks.addRoutes(keys)
		if err = acquireTopologyLocks(ctx, tx, topologyLocks); err != nil {
			return AccessPushResult{}, err
		}
		currentRoutes, routeErr := queryRouteTopologyKeys(ctx, tx, `WITH targets AS (SELECT installation_id,repository_id,remove_access FROM unnest($1::bigint[],$2::bigint[],$3::boolean[]) AS input(installation_id,repository_id,remove_access)) SELECT DISTINCT s.installation_id,s.repository_id,s.tracked_ref FROM targets t JOIN relay_subscriptions s ON s.installation_id=t.installation_id AND (t.repository_id=0 OR s.repository_id=t.repository_id) WHERE t.remove_access AND s.retired_generation IS NULL ORDER BY s.installation_id,s.repository_id,s.tracked_ref`, installationIDs, repositoryIDs, removeAccess)
		if routeErr != nil {
			return AccessPushResult{}, routeErr
		}
		if !topologyRoutesCovered(topologyLocks, currentRoutes) {
			return AccessPushResult{}, ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, deliveryLockKey(batch.DeliveryID)); err != nil {
		return AccessPushResult{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO relay_github_deliveries(delivery_id,delivery_kind,received_at,persisted_at) VALUES($1,'access',$2,$3) ON CONFLICT(delivery_id) DO NOTHING`, batch.DeliveryID, batch.ReceivedAt.UTC(), now)
	if err != nil {
		return AccessPushResult{}, err
	}
	if tag.RowsAffected() == 0 {
		var kind string
		if err = tx.QueryRow(ctx, `SELECT delivery_kind FROM relay_github_deliveries WHERE delivery_id=$1`, batch.DeliveryID).Scan(&kind); err != nil {
			return AccessPushResult{}, err
		}
		if kind != "access" {
			return AccessPushResult{}, ErrConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, batch.DeliveryID, now); err != nil {
			return AccessPushResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return AccessPushResult{}, err
		}
		return AccessPushResult{Deduplicated: true}, nil
	}
	if !hasRemovals {
		if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, batch.DeliveryID, now); err != nil {
			return AccessPushResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return AccessPushResult{}, err
		}
		return AccessPushResult{}, nil
	}
	rows, queryErr := tx.Query(ctx, `WITH targets AS (SELECT ordinality::bigint AS target_index,installation_id,repository_id,remove_access FROM unnest($1::bigint[],$2::bigint[],$3::boolean[]) WITH ORDINALITY AS input(installation_id,repository_id,remove_access,ordinality)) SELECT t.target_index,b.controller_id::text,b.repository_id FROM targets t JOIN relay_bindings b ON b.installation_id=t.installation_id AND (t.repository_id=0 OR b.repository_id=t.repository_id) JOIN relay_controllers c ON c.controller_id=b.controller_id WHERE t.remove_access AND b.revoked_at IS NULL AND c.state='active' ORDER BY t.target_index,b.controller_id,b.repository_id FOR UPDATE OF b`, installationIDs, repositoryIDs, removeAccess)
	if queryErr != nil {
		return AccessPushResult{}, queryErr
	}
	expected := make([]map[string]struct{}, len(events))
	for i := range expected {
		expected[i] = make(map[string]struct{})
	}
	for rows.Next() {
		var targetIndex, bindingRepositoryID int64
		var controllerID string
		if err = rows.Scan(&targetIndex, &controllerID, &bindingRepositoryID); err != nil {
			rows.Close()
			return AccessPushResult{}, err
		}
		if targetIndex < 1 || targetIndex > int64(len(events)) || bindingRepositoryID <= 0 {
			rows.Close()
			return AccessPushResult{}, ErrConflict
		}
		expected[targetIndex-1][controllerID] = struct{}{}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return AccessPushResult{}, err
	}
	for i, item := range events {
		if len(expected[i]) != len(item.Routes) {
			return AccessPushResult{}, ErrConflict
		}
		for _, route := range item.Routes {
			if _, ok := expected[i][route.ControllerID]; !ok {
				return AccessPushResult{}, ErrConflict
			}
		}
	}
	accessEventIDs := make([]string, 0, totalRoutes)
	eventControllerIDs := make([]string, 0, totalRoutes)
	eventInstallationIDs := make([]int64, 0, totalRoutes)
	eventRepositoryIDs := make([]int64, 0, totalRoutes)
	changeCodes := make([]string, 0, totalRoutes)
	observedAt := make([]time.Time, 0, totalRoutes)
	for _, item := range events {
		for _, route := range item.Routes {
			accessEventIDs = append(accessEventIDs, route.EventID)
			eventControllerIDs = append(eventControllerIDs, route.ControllerID)
			eventInstallationIDs = append(eventInstallationIDs, item.InstallationID)
			eventRepositoryIDs = append(eventRepositoryIDs, item.RepositoryID)
			changeCodes = append(changeCodes, item.ChangeCode)
			observedAt = append(observedAt, item.ObservedAt.UTC())
		}
	}
	tag, err = tx.Exec(ctx, `INSERT INTO relay_access_events(event_id,delivery_id,controller_id,installation_id,repository_id,change_code,observed_at) SELECT input.event_id::uuid,$1,input.controller_id::uuid,input.installation_id,NULLIF(input.repository_id,0),input.change_code,input.observed_at FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::text[],$7::timestamptz[]) AS input(event_id,controller_id,installation_id,repository_id,change_code,observed_at)`, batch.DeliveryID, accessEventIDs, eventControllerIDs, eventInstallationIDs, eventRepositoryIDs, changeCodes, observedAt)
	if err != nil {
		return AccessPushResult{}, conflictError(err)
	}
	if tag.RowsAffected() != int64(totalRoutes) {
		return AccessPushResult{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `WITH targets AS (SELECT installation_id,repository_id,remove_access FROM unnest($1::bigint[],$2::bigint[],$3::boolean[]) AS input(installation_id,repository_id,remove_access)) UPDATE relay_bindings b SET revoked_at=$4 FROM targets t WHERE t.remove_access AND t.repository_id>0 AND b.installation_id=t.installation_id AND b.repository_id=t.repository_id AND b.revoked_at IS NULL`, installationIDs, repositoryIDs, removeAccess, now); err != nil {
		return AccessPushResult{}, err
	}
	if _, err = tx.Exec(ctx, `WITH targets AS (SELECT installation_id,repository_id,remove_access FROM unnest($1::bigint[],$2::bigint[],$3::boolean[]) AS input(installation_id,repository_id,remove_access)) UPDATE relay_bindings b SET revoked_at=$4 FROM targets t WHERE t.remove_access AND t.repository_id=0 AND b.installation_id=t.installation_id AND b.revoked_at IS NULL`, installationIDs, repositoryIDs, removeAccess, now); err != nil {
		return AccessPushResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, batch.DeliveryID, now); err != nil {
		return AccessPushResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AccessPushResult{}, err
	}
	return AccessPushResult{}, nil
}

func (s *Store) PendingDesired(ctx context.Context, lease Lease, limit int) ([]DesiredState, error) {
	if !validLease(lease) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	rows, err := s.pool.Query(ctx, `SELECT d.delivery_id::text,d.controller_id::text,d.subscription_id::text,d.generation,d.installation_id,d.repository_id,d.tracked_ref,d.observed_sha,d.observed_at FROM relay_controller_leases l JOIN relay_sessions se ON se.session_id=l.session_id AND se.controller_id=l.controller_id JOIN relay_controllers c ON c.controller_id=l.controller_id AND c.state='active' JOIN relay_controller_keys k ON k.controller_id=se.controller_id AND k.key_id=se.key_id AND k.state='active' JOIN relay_desired_states d ON d.controller_id=l.controller_id JOIN relay_subscriptions s ON s.subscription_id=d.subscription_id JOIN relay_bindings b ON b.controller_id=d.controller_id AND b.installation_id=d.installation_id AND b.repository_id=d.repository_id WHERE l.controller_id=$1 AND l.session_id=$2 AND l.lease_id=$3 AND l.fence=$4 AND l.expires_at>$5 AND se.expires_at>$5 AND se.revoked_at IS NULL AND d.decision IS NULL AND s.retired_generation IS NULL AND b.revoked_at IS NULL ORDER BY d.observed_at,d.delivery_id LIMIT $6`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DesiredState
	for rows.Next() {
		var item DesiredState
		if err = rows.Scan(&item.DeliveryID, &item.ControllerID, &item.SubscriptionID, &item.Generation, &item.InstallationID, &item.RepositoryID, &item.Ref, &item.SHA, &item.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type PendingAccess struct {
	EventID        string
	DeliveryID     string
	ControllerID   string
	InstallationID int64
	RepositoryID   int64
	ChangeCode     string
	ObservedAt     time.Time
}

func (s *Store) PendingAccess(ctx context.Context, lease Lease, limit int) ([]PendingAccess, error) {
	if !validLease(lease) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	rows, err := s.pool.Query(ctx, `SELECT a.event_id::text,a.delivery_id::text,a.controller_id::text,a.installation_id,COALESCE(a.repository_id,0),a.change_code,a.observed_at FROM relay_controller_leases l JOIN relay_sessions se ON se.session_id=l.session_id AND se.controller_id=l.controller_id JOIN relay_controllers c ON c.controller_id=l.controller_id AND c.state='active' JOIN relay_controller_keys k ON k.controller_id=se.controller_id AND k.key_id=se.key_id AND k.state='active' JOIN relay_access_events a ON a.controller_id=l.controller_id WHERE l.controller_id=$1 AND l.session_id=$2 AND l.lease_id=$3 AND l.fence=$4 AND l.expires_at>$5 AND se.expires_at>$5 AND se.revoked_at IS NULL AND a.decision IS NULL ORDER BY a.observed_at,a.event_id LIMIT $6`, lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingAccess
	for rows.Next() {
		var item PendingAccess
		if err = rows.Scan(&item.EventID, &item.DeliveryID, &item.ControllerID, &item.InstallationID, &item.RepositoryID, &item.ChangeCode, &item.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
