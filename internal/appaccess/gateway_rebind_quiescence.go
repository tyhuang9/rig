package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

var ErrGatewayRebindNotQuiescent = errors.New("gateway rebind requires all jobs and deployments to be terminal")

// GatewayRebindJobCensus is the complete status census of the jobs table from
// one SQLite read snapshot.
type GatewayRebindJobCensus struct {
	Total           int64
	Queued          int64
	Assigned        int64
	Running         int64
	WaitingExternal int64
	WaitingUser     int64
	Succeeded       int64
	Failed          int64
	Cancelled       int64
	Interrupted     int64
	NeedsAttention  int64
}

// GatewayRebindDeploymentCensus is the complete status census of the
// deployments table from the same SQLite read snapshot as Jobs.
type GatewayRebindDeploymentCensus struct {
	Total          int64
	Preparing      int64
	Applying       int64
	WaitingHealth  int64
	Succeeded      int64
	Failed         int64
	Cancelled      int64
	NeedsAttention int64
}

// GatewayRebindQuiescenceCensus contains a point-in-time census of every job
// and deployment row. A successful census contains only terminal rows.
type GatewayRebindQuiescenceCensus struct {
	Jobs        GatewayRebindJobCensus
	Deployments GatewayRebindDeploymentCensus
}

// GatewayRebindQuiescenceCensus reads every job and deployment in one SQLite
// read transaction and succeeds only when every recognized row is terminal.
//
// This standalone check is informational. A future rebind claim writer must
// hold the deployment-effects lease and repeat this full census in the same
// SQLite write transaction that inserts the claim. A prior successful result
// must never authorize a claim because work can become nonterminal afterward.
func (r *Repository) GatewayRebindQuiescenceCensus(ctx context.Context) (GatewayRebindQuiescenceCensus, error) {
	return r.gatewayRebindQuiescenceCensus(ctx, nil)
}

func (r *Repository) gatewayRebindQuiescenceCensus(ctx context.Context, afterJobsRead func()) (GatewayRebindQuiescenceCensus, error) {
	if r == nil || r.db == nil {
		return GatewayRebindQuiescenceCensus{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayRebindQuiescenceCensus{}, err
	}
	defer tx.Rollback()
	result, err := evaluateGatewayRebindQuiescence(ctx, tx, afterJobsRead)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayRebindQuiescenceCensus{}, err
	}
	return result, nil
}

type gatewayRebindQuiescenceQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// evaluateGatewayRebindQuiescence contains the transaction-scoped census and
// decision shared by the read-only inspection wrapper and a future claim
// writer. The caller owns the transaction and all required external leases.
func evaluateGatewayRebindQuiescence(ctx context.Context, query gatewayRebindQuiescenceQuerier, afterJobsRead func()) (GatewayRebindQuiescenceCensus, error) {
	var result GatewayRebindQuiescenceCensus
	invalidJobs, err := censusGatewayRebindJobs(ctx, query, &result.Jobs)
	if err != nil {
		return GatewayRebindQuiescenceCensus{}, err
	}
	if afterJobsRead != nil {
		afterJobsRead()
	}
	invalidDeployments, err := censusGatewayRebindDeployments(ctx, query, &result.Deployments)
	if err != nil {
		return GatewayRebindQuiescenceCensus{}, err
	}
	if err := ctx.Err(); err != nil {
		return GatewayRebindQuiescenceCensus{}, err
	}
	if invalidJobs || invalidDeployments {
		return result, ErrInvalidStoredState
	}
	if gatewayRebindNonterminalJobs(result.Jobs) != 0 || gatewayRebindNonterminalDeployments(result.Deployments) != 0 {
		return result, ErrGatewayRebindNotQuiescent
	}
	return result, nil
}

func censusGatewayRebindJobs(ctx context.Context, query gatewayRebindQuiescenceQuerier, result *GatewayRebindJobCensus) (bool, error) {
	rows, err := query.QueryContext(ctx, `SELECT status FROM jobs ORDER BY id`)
	if err != nil {
		return false, err
	}
	invalid := false
	for rows.Next() {
		var status sql.NullString
		if err := rows.Scan(&status); err != nil {
			_ = rows.Close()
			return false, err
		}
		result.Total++
		if !status.Valid {
			invalid = true
			continue
		}
		switch status.String {
		case "queued":
			result.Queued++
		case "assigned":
			result.Assigned++
		case "running":
			result.Running++
		case "waiting_external":
			result.WaitingExternal++
		case "waiting_user":
			result.WaitingUser++
		case "succeeded":
			result.Succeeded++
		case "failed":
			result.Failed++
		case "cancelled":
			result.Cancelled++
		case "interrupted":
			result.Interrupted++
		case "needs_attention":
			result.NeedsAttention++
		default:
			invalid = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	return invalid, nil
}

func censusGatewayRebindDeployments(ctx context.Context, query gatewayRebindQuiescenceQuerier, result *GatewayRebindDeploymentCensus) (bool, error) {
	rows, err := query.QueryContext(ctx, `SELECT status FROM deployments ORDER BY id`)
	if err != nil {
		return false, err
	}
	invalid := false
	for rows.Next() {
		var status sql.NullString
		if err := rows.Scan(&status); err != nil {
			_ = rows.Close()
			return false, err
		}
		result.Total++
		if !status.Valid {
			invalid = true
			continue
		}
		switch status.String {
		case "preparing":
			result.Preparing++
		case "applying":
			result.Applying++
		case "waiting_health":
			result.WaitingHealth++
		case "succeeded":
			result.Succeeded++
		case "failed":
			result.Failed++
		case "cancelled":
			result.Cancelled++
		case "needs_attention":
			result.NeedsAttention++
		default:
			invalid = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	return invalid, nil
}

func gatewayRebindNonterminalJobs(value GatewayRebindJobCensus) int64 {
	return value.Queued + value.Assigned + value.Running + value.WaitingExternal + value.WaitingUser
}

func gatewayRebindNonterminalDeployments(value GatewayRebindDeploymentCensus) int64 {
	return value.Preparing + value.Applying + value.WaitingHealth + value.NeedsAttention
}
