package appaccess

import (
	"context"
	"errors"
)

var ErrGatewayRebindActive = errors.New("LAN gateway rebind fence is active")

// CheckGatewayRebindFence allows runtime effects only when a fresh, fully
// validated SQLite snapshot has no prepared rebind claim.
func (r *Repository) CheckGatewayRebindFence(ctx context.Context) error {
	snapshot, err := r.GatewayRebindStartupSnapshot(ctx)
	if err != nil {
		return err
	}
	if len(snapshot.Claims) != 0 {
		return ErrGatewayRebindActive
	}
	return nil
}
