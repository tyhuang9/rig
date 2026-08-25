package store

import (
	"context"
	"math"

	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/jackc/pgx/v5"
)

type Subscription struct {
	SubscriptionID string
	InstallationID int64
	RepositoryID   int64
	Ref            string
}

type subscriptionLocks struct {
	topologyLockSet
	active []Subscription
}

func validateSubscriptions(controllerID string, generation uint64, subscriptions []Subscription) (map[string]Subscription, error) {
	if !validUUID(controllerID) || generation == 0 || generation > math.MaxInt64 || subscriptions == nil || len(subscriptions) > protocol.MaxArrayItems {
		return nil, ErrInvalid
	}
	seen := map[string]Subscription{}
	for _, sub := range subscriptions {
		if !validUUID(sub.SubscriptionID) || sub.InstallationID <= 0 || sub.RepositoryID <= 0 || protocol.ValidRef(sub.Ref) != nil {
			return nil, ErrInvalid
		}
		if _, ok := seen[sub.SubscriptionID]; ok {
			return nil, ErrInvalid
		}
		seen[sub.SubscriptionID] = sub
	}
	return seen, nil
}

func (s *Store) prepareSubscriptionLocks(ctx context.Context, tx pgx.Tx, controllerID string, subscriptions []Subscription) (subscriptionLocks, error) {
	activeSnapshot, err := activeSubscriptions(ctx, tx, controllerID)
	if err != nil {
		return subscriptionLocks{}, err
	}
	locks := newTopologyLockSet()
	for _, sub := range activeSnapshot {
		locks.addBinding(sub.InstallationID)
		locks.addRoute(sub.InstallationID, sub.RepositoryID, sub.Ref)
	}
	for _, sub := range subscriptions {
		locks.addBinding(sub.InstallationID)
		locks.addRoute(sub.InstallationID, sub.RepositoryID, sub.Ref)
	}
	if err = acquireTopologyLocks(ctx, tx, locks); err != nil {
		return subscriptionLocks{}, err
	}
	current, err := activeSubscriptions(ctx, tx, controllerID)
	if err != nil {
		return subscriptionLocks{}, err
	}
	for _, sub := range current {
		if _, ok := locks.bindings[sub.InstallationID]; !ok {
			return subscriptionLocks{}, ErrConflict
		}
		if _, ok := locks.routes[routeTopologyKey{installationID: sub.InstallationID, repositoryID: sub.RepositoryID, ref: sub.Ref}]; !ok {
			return subscriptionLocks{}, ErrConflict
		}
	}
	return subscriptionLocks{topologyLockSet: locks, active: current}, nil
}

func (s *Store) syncSubscriptionsLocked(ctx context.Context, tx pgx.Tx, controllerID string, generation uint64, subscriptions []Subscription, seen map[string]Subscription, locks subscriptionLocks) error {
	now := s.now().UTC()
	var err error
	var controllerState string
	if err = tx.QueryRow(ctx, `SELECT state FROM relay_controllers WHERE controller_id=$1 FOR UPDATE`, controllerID).Scan(&controllerState); isNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if controllerState != "active" {
		return ErrConflict
	}
	active := locks.active
	var current int64
	err = tx.QueryRow(ctx, `SELECT generation FROM relay_subscription_heads WHERE controller_id=$1`, controllerID).Scan(&current)
	if isNoRows(err) {
		current = 0
	} else if err != nil {
		return err
	}
	if generation == uint64(current) {
		rows, err := tx.Query(ctx, `SELECT s.subscription_id::text,s.installation_id,s.repository_id,s.tracked_ref FROM relay_subscription_set_items i JOIN relay_subscriptions s ON s.subscription_id=i.subscription_id JOIN relay_bindings b ON b.controller_id=s.controller_id AND b.installation_id=s.installation_id AND b.repository_id=s.repository_id AND b.revoked_at IS NULL WHERE i.controller_id=$1 AND i.set_generation=$2`, controllerID, current)
		if err != nil {
			return err
		}
		matched := 0
		for rows.Next() {
			var sub Subscription
			if err = rows.Scan(&sub.SubscriptionID, &sub.InstallationID, &sub.RepositoryID, &sub.Ref); err != nil {
				rows.Close()
				return err
			}
			want, ok := seen[sub.SubscriptionID]
			if !ok || want != sub {
				rows.Close()
				return ErrConflict
			}
			matched++
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		if matched != len(subscriptions) {
			return ErrConflict
		}
		return nil
	}
	if generation != uint64(current+1) {
		return ErrConflict
	}
	ids, installations, repositories, _ := subscriptionArrays(subscriptions)
	if len(subscriptions) > 0 {
		var authorized bool
		if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM (SELECT DISTINCT installation_id,repository_id FROM unnest($2::bigint[],$3::bigint[]) AS requested(installation_id,repository_id)) requested WHERE NOT EXISTS(SELECT 1 FROM relay_bindings b WHERE b.controller_id=$1 AND b.installation_id=requested.installation_id AND b.repository_id=requested.repository_id AND b.revoked_at IS NULL))`, controllerID, installations, repositories).Scan(&authorized); err != nil {
			return err
		}
		if !authorized {
			return ErrConflict
		}
	}

	existingByID := make(map[string]Subscription, len(subscriptions))
	if len(ids) > 0 {
		rows, queryErr := tx.Query(ctx, `SELECT s.subscription_id::text,s.controller_id::text,s.installation_id,s.repository_id,s.tracked_ref,s.retired_generation FROM unnest($1::text[]) requested(subscription_id) JOIN relay_subscriptions s ON s.subscription_id=requested.subscription_id::uuid FOR UPDATE OF s`, ids)
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			var subscriptionID, existingController, ref string
			var installationID, repositoryID int64
			var retired *int64
			if err = rows.Scan(&subscriptionID, &existingController, &installationID, &repositoryID, &ref, &retired); err != nil {
				rows.Close()
				return err
			}
			if existingController != controllerID || retired != nil {
				rows.Close()
				return ErrConflict
			}
			existingByID[subscriptionID] = Subscription{SubscriptionID: subscriptionID, InstallationID: installationID, RepositoryID: repositoryID, Ref: ref}
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		for subscriptionID, existing := range existingByID {
			if seen[subscriptionID] != existing {
				return ErrConflict
			}
		}
	}

	if len(active) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE relay_subscriptions s SET retired_generation=$2,retired_at=$4 WHERE s.controller_id=$1 AND s.retired_generation IS NULL AND NOT EXISTS(SELECT 1 FROM unnest($3::text[]) requested(subscription_id) WHERE requested.subscription_id::uuid=s.subscription_id)`, controllerID, generation, ids, now); err != nil {
			return err
		}
	}
	newSubscriptions := make([]Subscription, 0, len(subscriptions)-len(existingByID))
	for _, sub := range subscriptions {
		if _, exists := existingByID[sub.SubscriptionID]; !exists {
			newSubscriptions = append(newSubscriptions, sub)
		}
	}
	if len(newSubscriptions) > 0 {
		newIDs, newInstallations, newRepositories, newRefs := subscriptionArrays(newSubscriptions)
		if _, err = tx.Exec(ctx, `INSERT INTO relay_subscriptions(subscription_id,controller_id,installation_id,repository_id,tracked_ref,activated_generation,created_at) SELECT subscription_id::uuid,$1,installation_id,repository_id,tracked_ref,$2,$7 FROM unnest($3::text[],$4::bigint[],$5::bigint[],$6::text[]) input(subscription_id,installation_id,repository_id,tracked_ref)`, controllerID, generation, newIDs, newInstallations, newRepositories, newRefs, now); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO relay_subscription_set_items(controller_id,set_generation,subscription_id,created_at) SELECT $1,$2,subscription_id::uuid,$4 FROM unnest($3::text[]) input(subscription_id)`, controllerID, generation, ids, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO relay_subscription_heads(controller_id,generation,updated_at) VALUES($1,$2,$3) ON CONFLICT(controller_id) DO UPDATE SET generation=EXCLUDED.generation,updated_at=EXCLUDED.updated_at`, controllerID, generation, now)
	if err != nil {
		return err
	}
	return nil
}

func subscriptionArrays(subscriptions []Subscription) ([]string, []int64, []int64, []string) {
	ids := make([]string, len(subscriptions))
	installations := make([]int64, len(subscriptions))
	repositories := make([]int64, len(subscriptions))
	refs := make([]string, len(subscriptions))
	for index, sub := range subscriptions {
		ids[index] = sub.SubscriptionID
		installations[index] = sub.InstallationID
		repositories[index] = sub.RepositoryID
		refs[index] = sub.Ref
	}
	return ids, installations, repositories, refs
}

func activeSubscriptions(ctx context.Context, tx interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, controllerID string) ([]Subscription, error) {
	rows, err := tx.Query(ctx, `SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions WHERE controller_id=$1 AND retired_generation IS NULL ORDER BY subscription_id`, controllerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err = rows.Scan(&sub.SubscriptionID, &sub.InstallationID, &sub.RepositoryID, &sub.Ref); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}
