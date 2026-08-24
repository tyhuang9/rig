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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, bindingLockKey(event.InstallationID)); err != nil {
		return SourcePushResult{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, routeLockKey(event.InstallationID, event.RepositoryID, event.Ref)); err != nil {
		return SourcePushResult{}, err
	}
	// Global order is binding -> route -> delivery -> rows. Recovery takes only
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
	expectedRows, err := tx.Query(ctx, `SELECT s.controller_id::text,s.subscription_id::text FROM relay_subscriptions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_bindings b ON b.controller_id=s.controller_id AND b.installation_id=s.installation_id AND b.repository_id=s.repository_id WHERE s.installation_id=$1 AND s.repository_id=$2 AND s.tracked_ref=$3 AND s.retired_generation IS NULL AND c.state='active' AND b.revoked_at IS NULL FOR SHARE OF s`, event.InstallationID, event.RepositoryID, event.Ref)
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
	for _, route := range routes {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, subscriptionLockKey(route.SubscriptionID)); err != nil {
			return SourcePushResult{}, err
		}
	}
	result := SourcePushResult{Desired: make([]DesiredState, 0, len(routes))}
	for _, route := range routes {
		var controller string
		var installation, repository int64
		var ref, controllerState string
		var retired *int64
		var bindingRevoked *time.Time
		err = tx.QueryRow(ctx, `SELECT s.controller_id::text,s.installation_id,s.repository_id,s.tracked_ref,s.retired_generation,c.state,b.revoked_at FROM relay_subscriptions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_bindings b ON b.controller_id=s.controller_id AND b.installation_id=s.installation_id AND b.repository_id=s.repository_id WHERE s.subscription_id=$1`, route.SubscriptionID).Scan(&controller, &installation, &repository, &ref, &retired, &controllerState, &bindingRevoked)
		if isNoRows(err) {
			return SourcePushResult{}, ErrNotFound
		}
		if err != nil {
			return SourcePushResult{}, err
		}
		if controller != route.ControllerID || installation != event.InstallationID || repository != event.RepositoryID || ref != event.Ref || retired != nil || controllerState != "active" || bindingRevoked != nil {
			return SourcePushResult{}, ErrConflict
		}
		var generation int64
		err = tx.QueryRow(ctx, `SELECT generation FROM relay_desired_states WHERE subscription_id=$1 FOR UPDATE`, route.SubscriptionID).Scan(&generation)
		if isNoRows(err) {
			generation = 0
		} else if err != nil {
			return SourcePushResult{}, err
		}
		generation++
		if _, err = tx.Exec(ctx, `INSERT INTO relay_source_delivery_targets(delivery_id,subscription_id,generation,persisted_at) VALUES($1,$2,$3,$4)`, event.DeliveryID, route.SubscriptionID, generation, now); err != nil {
			return SourcePushResult{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_desired_states(subscription_id,delivery_id,controller_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(subscription_id) DO UPDATE SET delivery_id=EXCLUDED.delivery_id,generation=EXCLUDED.generation,observed_sha=EXCLUDED.observed_sha,observed_at=EXCLUDED.observed_at,decision=NULL,decision_code=NULL,decision_message_id=NULL,decided_at=NULL`, route.SubscriptionID, event.DeliveryID, route.ControllerID, generation, event.InstallationID, event.RepositoryID, event.Ref, event.SHA, event.ObservedAt.UTC()); err != nil {
			return SourcePushResult{}, err
		}
		result.Desired = append(result.Desired, DesiredState{DeliveryID: event.DeliveryID, ControllerID: route.ControllerID, SubscriptionID: route.SubscriptionID, Generation: uint64(generation), InstallationID: event.InstallationID, RepositoryID: event.RepositoryID, Ref: event.Ref, SHA: event.SHA, ObservedAt: event.ObservedAt.UTC()})
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
	bindingLocks := make(map[int64]struct{})
	for _, item := range events {
		bindingLocks[bindingLockKey(item.InstallationID)] = struct{}{}
	}
	if err = acquireAdvisoryLocks(ctx, tx, bindingLocks); err != nil {
		return AccessPushResult{}, err
	}
	routeLocks := make(map[int64]struct{})
	for _, item := range events {
		if item.RemoveAccess {
			keys, routeErr := queryRouteLockKeys(ctx, tx, `SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE installation_id=$1 AND ($2::bigint=0 OR repository_id=$2) AND retired_generation IS NULL`, item.InstallationID, item.RepositoryID)
			if routeErr != nil {
				return AccessPushResult{}, routeErr
			}
			for key := range keys {
				routeLocks[key] = struct{}{}
			}
		}
	}
	if err = acquireAdvisoryLocks(ctx, tx, routeLocks); err != nil {
		return AccessPushResult{}, err
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
	for _, item := range events {
		rows, queryErr := tx.Query(ctx, `SELECT b.controller_id::text FROM relay_bindings b JOIN relay_controllers c ON c.controller_id=b.controller_id WHERE b.installation_id=$1 AND ($2::bigint=0 OR b.repository_id=$2) AND b.revoked_at IS NULL AND c.state='active' ORDER BY b.controller_id,b.repository_id FOR UPDATE OF b`, item.InstallationID, item.RepositoryID)
		if queryErr != nil {
			return AccessPushResult{}, queryErr
		}
		expected := map[string]struct{}{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return AccessPushResult{}, err
			}
			expected[id] = struct{}{}
		}
		rows.Close()
		if rows.Err() != nil {
			return AccessPushResult{}, rows.Err()
		}
		if len(expected) != len(item.Routes) {
			return AccessPushResult{}, ErrConflict
		}
		for _, route := range item.Routes {
			if _, ok := expected[route.ControllerID]; !ok {
				return AccessPushResult{}, ErrConflict
			}
		}
		var repository any
		if item.RepositoryID > 0 {
			repository = item.RepositoryID
		}
		for _, route := range item.Routes {
			if _, err = tx.Exec(ctx, `INSERT INTO relay_access_events(event_id,delivery_id,controller_id,installation_id,repository_id,change_code,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, route.EventID, batch.DeliveryID, route.ControllerID, item.InstallationID, repository, item.ChangeCode, item.ObservedAt.UTC()); err != nil {
				return AccessPushResult{}, err
			}
		}
		if item.RemoveAccess {
			if item.RepositoryID > 0 {
				_, err = tx.Exec(ctx, `UPDATE relay_bindings SET revoked_at=$3 WHERE installation_id=$1 AND repository_id=$2 AND revoked_at IS NULL`, item.InstallationID, item.RepositoryID, now)
			} else {
				_, err = tx.Exec(ctx, `UPDATE relay_bindings SET revoked_at=$2 WHERE installation_id=$1 AND revoked_at IS NULL`, item.InstallationID, now)
			}
			if err != nil {
				return AccessPushResult{}, err
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE relay_recovery_deliveries SET recovered_at=$2,next_attempt_at=NULL,last_error_code=NULL,claim_id=NULL,claim_expires_at=NULL WHERE delivery_id=$1 AND recovered_at IS NULL`, batch.DeliveryID, now); err != nil {
		return AccessPushResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AccessPushResult{}, err
	}
	return AccessPushResult{}, nil
}

type Decision struct {
	MessageID string
	Accepted  bool
	Code      string
}

func validateDecision(d Decision) error {
	if !validUUID(d.MessageID) || (!d.Accepted && !validCode(d.Code)) || (d.Accepted && d.Code != "") {
		return ErrInvalid
	}
	return nil
}
func (s *Store) DecideSource(ctx context.Context, controllerID, subscriptionID string, generation uint64, decision Decision) error {
	if !validUUID(controllerID) || !validUUID(subscriptionID) || generation == 0 || validateDecision(decision) != nil {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, subscriptionLockKey(subscriptionID)); err != nil {
		return err
	}
	var storedController string
	var existingGeneration int64
	var existingDecision, existingMessage, existingCode *string
	err = tx.QueryRow(ctx, `SELECT controller_id::text,generation,decision,decision_message_id::text,decision_code FROM relay_desired_states WHERE subscription_id=$1 FOR UPDATE`, subscriptionID).Scan(&storedController, &existingGeneration, &existingDecision, &existingMessage, &existingCode)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if storedController != controllerID || uint64(existingGeneration) != generation {
		return ErrConflict
	}
	state := "rejected"
	var code any = decision.Code
	if decision.Accepted {
		state = "acked"
		code = nil
	}
	if existingDecision != nil {
		codeMatches := (decision.Accepted && existingCode == nil) || (!decision.Accepted && existingCode != nil && *existingCode == decision.Code)
		if existingMessage != nil && *existingMessage == decision.MessageID && *existingDecision == state && codeMatches {
			return tx.Commit(ctx)
		}
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE relay_desired_states SET decision=$3,decision_code=$4,decision_message_id=$5,decided_at=$6 WHERE subscription_id=$1 AND generation=$2 AND decision IS NULL`, subscriptionID, generation, state, code, decision.MessageID, s.now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
func (s *Store) DecideAccess(ctx context.Context, controllerID, eventID string, decision Decision) error {
	if !validUUID(controllerID) || !validUUID(eventID) || validateDecision(decision) != nil {
		return ErrInvalid
	}
	state := "rejected"
	var code any = decision.Code
	if decision.Accepted {
		state = "acked"
		code = nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE relay_access_events SET decision=$3,decision_code=$4,decision_message_id=$5,decided_at=$6 WHERE event_id=$1 AND controller_id=$2 AND decision IS NULL`, eventID, controllerID, state, code, decision.MessageID, s.now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var existingController string
	var existingState, existingMessage, existingCode sql.NullString
	err = s.pool.QueryRow(ctx, `SELECT controller_id::text,decision,decision_message_id::text,decision_code FROM relay_access_events WHERE event_id=$1`, eventID).Scan(&existingController, &existingState, &existingMessage, &existingCode)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if existingController != controllerID {
		return ErrConflict
	}
	codeMatches := (decision.Accepted && !existingCode.Valid) || (!decision.Accepted && existingCode.Valid && existingCode.String == decision.Code)
	if existingState.Valid && existingMessage.Valid && existingState.String == state && existingMessage.String == decision.MessageID && codeMatches {
		return nil
	}
	return ErrConflict
}

func (s *Store) PendingDesired(ctx context.Context, controllerID string, limit int) ([]DesiredState, error) {
	if !validUUID(controllerID) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT d.delivery_id::text,d.controller_id::text,d.subscription_id::text,d.generation,d.installation_id,d.repository_id,d.tracked_ref,d.observed_sha,d.observed_at FROM relay_desired_states d JOIN relay_controllers c ON c.controller_id=d.controller_id JOIN relay_subscriptions s ON s.subscription_id=d.subscription_id JOIN relay_bindings b ON b.controller_id=d.controller_id AND b.installation_id=d.installation_id AND b.repository_id=d.repository_id WHERE d.controller_id=$1 AND d.decision IS NULL AND c.state='active' AND s.retired_generation IS NULL AND b.revoked_at IS NULL ORDER BY d.observed_at,d.delivery_id LIMIT $2`, controllerID, limit)
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

func (s *Store) PendingAccess(ctx context.Context, controllerID string, limit int) ([]PendingAccess, error) {
	if !validUUID(controllerID) || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT a.event_id::text,a.delivery_id::text,a.controller_id::text,a.installation_id,COALESCE(a.repository_id,0),a.change_code,a.observed_at FROM relay_access_events a JOIN relay_controllers c ON c.controller_id=a.controller_id WHERE a.controller_id=$1 AND a.decision IS NULL AND c.state='active' ORDER BY a.observed_at,a.event_id LIMIT $2`, controllerID, limit)
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
