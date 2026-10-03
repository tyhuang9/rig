package main

import (
	"context"
	"database/sql"

	"github.com/hostd/hostd/internal/appaccess"
)

var errGatewayRebindFenceActive = appaccess.ErrGatewayRebindActive

// rebindFenceCheck is shared by startup, ingress, and deployment dispatch.
// Each call validates one fresh SQLite snapshot before allowing an effect.
func rebindFenceCheck(db *sql.DB) func(context.Context) error {
	if db == nil {
		return nil
	}
	repository := appaccess.New(db)
	return repository.CheckGatewayRebindFence
}
