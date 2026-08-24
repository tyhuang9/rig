package store

import (
	"context"

	"github.com/hostd/hostd/internal/relay/protocol"
)

// SourceRoutes returns a bounded routing snapshot. Callers must pass the
// snapshot to PushSourceEvent and retry the resolve/push pair a bounded number
// of times when PushSourceEvent returns ErrConflict; the push transaction
// rechecks the exact locked set to close the snapshot race.
func (s *Store) SourceRoutes(ctx context.Context, installationID, repositoryID int64, ref string) ([]SourceRoute, error) {
	if installationID <= 0 || repositoryID <= 0 || protocol.ValidRef(ref) != nil {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT s.controller_id::text,s.subscription_id::text FROM relay_subscriptions s JOIN relay_controllers c ON c.controller_id=s.controller_id JOIN relay_bindings b ON b.controller_id=s.controller_id AND b.installation_id=s.installation_id AND b.repository_id=s.repository_id WHERE s.installation_id=$1 AND s.repository_id=$2 AND s.tracked_ref=$3 AND s.retired_generation IS NULL AND c.state='active' AND b.revoked_at IS NULL ORDER BY s.subscription_id LIMIT 1001`, installationID, repositoryID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []SourceRoute
	for rows.Next() {
		var route SourceRoute
		if err = rows.Scan(&route.ControllerID, &route.SubscriptionID); err != nil {
			return nil, err
		}
		routes = append(routes, route)
		if len(routes) > 1000 {
			return nil, ErrConflict
		}
	}
	return routes, rows.Err()
}

// AccessRoutes returns the bounded controller routing snapshot for an access
// change. repositoryID zero denotes an installation-wide event. Callers assign
// event IDs, then retry resolve/push on ErrConflict as described by
// PushAccessEvent's exact-set check.
func (s *Store) AccessRoutes(ctx context.Context, installationID, repositoryID int64) ([]string, error) {
	if installationID <= 0 || repositoryID < 0 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT b.controller_id::text FROM relay_bindings b JOIN relay_controllers c ON c.controller_id=b.controller_id WHERE b.installation_id=$1 AND ($2::bigint=0 OR b.repository_id=$2) AND b.revoked_at IS NULL AND c.state='active' ORDER BY b.controller_id LIMIT 1001`, installationID, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var controllerIDs []string
	for rows.Next() {
		var controllerID string
		if err = rows.Scan(&controllerID); err != nil {
			return nil, err
		}
		controllerIDs = append(controllerIDs, controllerID)
		if len(controllerIDs) > 1000 {
			return nil, ErrConflict
		}
	}
	return controllerIDs, rows.Err()
}
