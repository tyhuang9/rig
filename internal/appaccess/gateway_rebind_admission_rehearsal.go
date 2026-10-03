package appaccess

import (
	"context"
	"errors"
)

// GatewayRebindImmediatePreclaimResult is an informational observation made
// under one SQLite BEGIN IMMEDIATE transaction. It is not a claim or an
// authorization to perform an external effect.
type GatewayRebindImmediatePreclaimResult struct {
	Predecessor GatewayRebindPreclaimSnapshot
	Quiescence  GatewayRebindQuiescenceCensus
}

// GatewayRebindImmediatePreclaim repeats the complete zero-claim proposal,
// approval, roster, and job/deployment checks while holding SQLite's writer
// reservation. It always rolls back. A future writer must acquire the
// deployment-effects lease and gateway locks before invoking the same checks
// in its claim-insert transaction, then insert and attest a real claim.
func (r *Repository) GatewayRebindImmediatePreclaim(ctx context.Context,
	proposal GatewayRebindPreclaimProposal,
) (result GatewayRebindImmediatePreclaimResult, resultErr error) {
	if r == nil || r.db == nil || ctx == nil {
		return GatewayRebindImmediatePreclaimResult{}, ErrInvalidInput
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayRebindImmediatePreclaimResult{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			result = GatewayRebindImmediatePreclaimResult{}
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	predecessor, err := r.readGatewayRebindPreclaimSnapshot(ctx, tx, proposal)
	if err != nil {
		return GatewayRebindImmediatePreclaimResult{}, err
	}
	census, err := evaluateGatewayRebindQuiescence(ctx, tx, nil)
	if err != nil {
		return GatewayRebindImmediatePreclaimResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return GatewayRebindImmediatePreclaimResult{}, err
	}
	return GatewayRebindImmediatePreclaimResult{Predecessor: predecessor, Quiescence: census}, nil
}
