package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// GatewayRebindRuntimeHead is the complete active generated-runtime identity
// used by the private rebind coordinator. It includes loopback-only apps,
// which are deliberately absent from the LAN allocation roster.
type GatewayRebindRuntimeHead struct {
	AppID        string
	DeploymentID string
	ReleaseID    string
	Slot         string
	Generation   int64
	UpdatedAt    time.Time
}

// GatewayRebindRuntimeHeads returns every active generated-runtime head for a
// non-archived application from one read-only SQLite snapshot. The result is
// sorted by app ID. An inactive generation-zero row is not a serving head.
func (r *Repository) GatewayRebindRuntimeHeads(ctx context.Context) ([]GatewayRebindRuntimeHead, error) {
	if r == nil || r.db == nil || ctx == nil {
		return nil, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT h.app_id,h.deployment_id,h.release_id,h.slot,h.generation,h.updated_at,
		a.id,a.archived_at,d.deployment_id,r.id
		FROM generated_runtime_active_heads h
		LEFT JOIN applications a ON a.id=h.app_id
		LEFT JOIN generated_runtime_deployments d ON d.app_id=h.app_id AND d.deployment_id=h.deployment_id
			AND d.release_id=h.release_id AND d.candidate_slot=h.slot
		LEFT JOIN releases r ON r.app_id=h.app_id AND r.id=h.release_id
		ORDER BY h.app_id`)
	if err != nil {
		return nil, err
	}
	result := make([]GatewayRebindRuntimeHead, 0)
	for rows.Next() {
		var value GatewayRebindRuntimeHead
		var deploymentID, releaseID, slot, updatedAt sql.NullString
		var applicationID, archivedAt, deploymentParent, releaseParent sql.NullString
		if err := rows.Scan(&value.AppID, &deploymentID, &releaseID, &slot,
			&value.Generation, &updatedAt, &applicationID, &archivedAt, &deploymentParent, &releaseParent); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if uuid.Validate(value.AppID) != nil || !applicationID.Valid || applicationID.String != value.AppID ||
			value.Generation < 0 {
			_ = rows.Close()
			return nil, ErrInvalidStoredState
		}
		if value.Generation == 0 {
			if deploymentID.Valid || releaseID.Valid || slot.Valid || updatedAt.Valid ||
				deploymentParent.Valid || releaseParent.Valid {
				_ = rows.Close()
				return nil, ErrInvalidStoredState
			}
			continue
		}
		if !deploymentID.Valid || !releaseID.Valid || !slot.Valid || !updatedAt.Valid ||
			!deploymentParent.Valid || deploymentParent.String != deploymentID.String ||
			!releaseParent.Valid || releaseParent.String != releaseID.String {
			_ = rows.Close()
			return nil, ErrInvalidStoredState
		}
		value.DeploymentID, value.ReleaseID, value.Slot = deploymentID.String, releaseID.String, slot.String
		value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt.String)
		if err != nil || uuid.Validate(value.DeploymentID) != nil || uuid.Validate(value.ReleaseID) != nil ||
			(value.Slot != "blue" && value.Slot != "green") || value.UpdatedAt.IsZero() {
			_ = rows.Close()
			return nil, ErrInvalidStoredState
		}
		if archivedAt.Valid {
			continue
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for index := 1; index < len(result); index++ {
		if result[index-1].AppID >= result[index].AppID {
			return nil, errors.New("generated runtime active heads are not unique")
		}
	}
	return result, nil
}
