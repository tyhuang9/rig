package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
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
	result, err := readGatewayRebindRuntimeHeads(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func readGatewayRebindRuntimeHeads(ctx context.Context,
	query gatewayRebindQuiescenceQuerier,
) ([]GatewayRebindRuntimeHead, error) {
	rows, err := query.QueryContext(ctx, `SELECT h.app_id,h.deployment_id,h.release_id,h.slot,h.generation,h.updated_at,
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
		if !validUUID(value.AppID) || !applicationID.Valid || applicationID.String != value.AppID ||
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
		if err != nil || !validUUID(value.DeploymentID) || !validUUID(value.ReleaseID) ||
			(value.Slot != "blue" && value.Slot != "green") || value.UpdatedAt.IsZero() ||
			!strings.HasSuffix(updatedAt.String, "Z") {
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
	for index := 1; index < len(result); index++ {
		if result[index-1].AppID >= result[index].AppID {
			return nil, errors.New("generated runtime active heads are not unique")
		}
	}
	return result, nil
}

func sameGatewayRebindRuntimeHeads(left, right []GatewayRebindRuntimeHead) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].AppID != right[index].AppID ||
			left[index].DeploymentID != right[index].DeploymentID ||
			left[index].ReleaseID != right[index].ReleaseID ||
			left[index].Slot != right[index].Slot ||
			left[index].Generation != right[index].Generation ||
			!left[index].UpdatedAt.Equal(right[index].UpdatedAt) {
			return false
		}
	}
	return true
}

func readGatewayRebindRetainedRuntimeHeads(ctx context.Context,
	query gatewayRebindQuiescenceQuerier, operationID string,
) ([]GatewayRebindRuntimeHead, error) {
	rows, err := query.QueryContext(ctx, `SELECT ordinal,app_id,deployment_id,release_id,slot,
		generation,updated_at,entry_digest FROM lan_gateway_rebind_runtime_heads
		WHERE operation_id=? ORDER BY ordinal`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]GatewayRebindRuntimeHead, 0)
	for rows.Next() {
		var ordinal int64
		var stamp, entryDigest string
		var value GatewayRebindRuntimeHead
		if err := rows.Scan(&ordinal, &value.AppID, &value.DeploymentID, &value.ReleaseID,
			&value.Slot, &value.Generation, &stamp, &entryDigest); err != nil {
			return nil, err
		}
		value.UpdatedAt, err = time.Parse(time.RFC3339Nano, stamp)
		digest, digestErr := gatewayRebindRuntimeHeadV2Digest(operationID, ordinal, value)
		if err != nil || formatTime(value.UpdatedAt) != stamp || ordinal != int64(len(values)+1) ||
			digestErr != nil || digest != entryDigest ||
			(len(values) > 0 && values[len(values)-1].AppID >= value.AppID) {
			return nil, ErrInvalidStoredState
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}
