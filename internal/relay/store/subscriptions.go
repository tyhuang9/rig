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

func (s *Store) SyncSubscriptions(ctx context.Context, controllerID string, generation uint64, subscriptions []Subscription) error {
	seen, err := validateSubscriptions(controllerID, generation, subscriptions)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	locks, err := s.prepareSubscriptionLocks(ctx, tx, controllerID, subscriptions)
	if err != nil {
		return err
	}
	if err = s.syncSubscriptionsLocked(ctx, tx, controllerID, generation, subscriptions, seen, locks); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type subscriptionLocks struct {
	bindings map[int64]struct{}
	routes   map[int64]struct{}
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
	bindingLocks := make(map[int64]struct{}, len(activeSnapshot)+len(subscriptions))
	routeLocks := make(map[int64]struct{}, len(activeSnapshot)+len(subscriptions))
	for _, sub := range activeSnapshot {
		bindingLocks[bindingLockKey(sub.InstallationID)] = struct{}{}
		routeLocks[routeLockKey(sub.InstallationID, sub.RepositoryID, sub.Ref)] = struct{}{}
	}
	for _, sub := range subscriptions {
		bindingLocks[bindingLockKey(sub.InstallationID)] = struct{}{}
		routeLocks[routeLockKey(sub.InstallationID, sub.RepositoryID, sub.Ref)] = struct{}{}
	}
	if err = acquireAdvisoryLocks(ctx, tx, bindingLocks); err != nil {
		return subscriptionLocks{}, err
	}
	if err = acquireAdvisoryLocks(ctx, tx, routeLocks); err != nil {
		return subscriptionLocks{}, err
	}
	return subscriptionLocks{bindings: bindingLocks, routes: routeLocks}, nil
}

func (s *Store) syncSubscriptionsLocked(ctx context.Context, tx pgx.Tx, controllerID string, generation uint64, subscriptions []Subscription, seen map[string]Subscription, locks subscriptionLocks) error {
	now := s.now().UTC()
	var controllerState string
	if err := tx.QueryRow(ctx, `SELECT state FROM relay_controllers WHERE controller_id=$1 FOR UPDATE`, controllerID).Scan(&controllerState); isNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if controllerState != "active" {
		return ErrConflict
	}
	active, err := activeSubscriptions(ctx, tx, controllerID)
	if err != nil {
		return err
	}
	for _, sub := range active {
		if _, ok := locks.bindings[bindingLockKey(sub.InstallationID)]; !ok {
			return ErrConflict
		}
		if _, ok := locks.routes[routeLockKey(sub.InstallationID, sub.RepositoryID, sub.Ref)]; !ok {
			return ErrConflict
		}
	}
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
	for _, existing := range active {
		if _, ok := seen[existing.SubscriptionID]; !ok {
			if _, err = tx.Exec(ctx, `UPDATE relay_subscriptions SET retired_generation=$2 WHERE subscription_id=$1 AND retired_generation IS NULL`, existing.SubscriptionID, generation); err != nil {
				return err
			}
		}
	}
	for _, sub := range subscriptions {
		var authorized bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_bindings WHERE controller_id=$1 AND installation_id=$2 AND repository_id=$3 AND revoked_at IS NULL)`, controllerID, sub.InstallationID, sub.RepositoryID).Scan(&authorized); err != nil {
			return err
		}
		if !authorized {
			return ErrConflict
		}
		var existingController string
		var installationID, repositoryID int64
		var ref string
		var retired *int64
		err = tx.QueryRow(ctx, `SELECT controller_id::text,installation_id,repository_id,tracked_ref,retired_generation FROM relay_subscriptions WHERE subscription_id=$1`, sub.SubscriptionID).Scan(&existingController, &installationID, &repositoryID, &ref, &retired)
		if err == nil {
			if existingController != controllerID || installationID != sub.InstallationID || repositoryID != sub.RepositoryID || ref != sub.Ref || retired != nil {
				return ErrConflict
			}
		} else if isNoRows(err) {
			if _, err = tx.Exec(ctx, `INSERT INTO relay_subscriptions(subscription_id,controller_id,installation_id,repository_id,tracked_ref,activated_generation,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, sub.SubscriptionID, controllerID, sub.InstallationID, sub.RepositoryID, sub.Ref, generation, now); err != nil {
				return err
			}
		} else {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_subscription_set_items(controller_id,set_generation,subscription_id,created_at) VALUES($1,$2,$3,$4)`, controllerID, generation, sub.SubscriptionID, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO relay_subscription_heads(controller_id,generation,updated_at) VALUES($1,$2,$3) ON CONFLICT(controller_id) DO UPDATE SET generation=EXCLUDED.generation,updated_at=EXCLUDED.updated_at`, controllerID, generation, now)
	if err != nil {
		return err
	}
	return nil
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
