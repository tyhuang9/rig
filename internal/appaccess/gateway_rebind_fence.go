package appaccess

import (
	"context"
	"errors"
)

var ErrGatewayRebindActive = errors.New("LAN gateway rebind fence is active")

// CheckGatewayRebindFence allows runtime effects only when a fresh, fully
// validated SQLite recovery snapshot has no active rebind claim. Terminal
// history is allowed only after the complete retained history and selected
// current authority have passed recovery validation.
func (r *Repository) CheckGatewayRebindFence(ctx context.Context) error {
	snapshot, err := r.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Active != nil {
		return ErrGatewayRebindActive
	}
	return nil
}
