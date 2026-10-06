package appaccess

import (
	"context"
	"strings"
	"time"
)

// GatewayStartupRuntimeComponent binds one recorded component to the complete
// active runtime head census. It is an invocation-only read projection, not
// Docker evidence or permission to start a container. Non-serving states are
// retained so a recovery consumer can refuse them explicitly.
type GatewayStartupRuntimeComponent struct {
	AppID           string
	DeploymentID    string
	ReleaseID       string
	Name            string
	Slot            string
	ImageArtifactID string
	ContainerName   string
	ContainerID     string
	State           string
	DiagnosticCode  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      time.Time
}

func readGatewayStartupRuntimeComponents(ctx context.Context, query gatewayRebindQuiescenceQuerier,
	heads []GatewayRebindRuntimeHead,
) ([]GatewayStartupRuntimeComponent, error) {
	rows, err := query.QueryContext(ctx, `SELECT h.app_id,h.deployment_id,h.release_id,c.component_name,c.slot,
		COALESCE(c.image_artifact_id,''),COALESCE(c.container_name,''),COALESCE(c.container_id,''),
		c.state,COALESCE(c.diagnostic_code,''),c.created_at,c.updated_at,COALESCE(c.finished_at,'')
		FROM generated_runtime_active_heads h
		JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL
		JOIN generated_runtime_components c ON c.deployment_id=h.deployment_id
		WHERE h.generation>0 ORDER BY h.app_id,c.component_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byApp := make(map[string]GatewayRebindRuntimeHead, len(heads))
	counts := make(map[string]int, len(heads))
	for _, head := range heads {
		byApp[head.AppID] = head
	}
	result := make([]GatewayStartupRuntimeComponent, 0)
	for rows.Next() {
		var value GatewayStartupRuntimeComponent
		var created, updated, finished string
		if err := rows.Scan(&value.AppID, &value.DeploymentID, &value.ReleaseID, &value.Name, &value.Slot,
			&value.ImageArtifactID, &value.ContainerName, &value.ContainerID, &value.State, &value.DiagnosticCode,
			&created, &updated, &finished); err != nil {
			return nil, err
		}
		parse := func(stamp string) (time.Time, bool) {
			value, err := time.Parse(time.RFC3339Nano, stamp)
			return value, err == nil && !value.IsZero() && strings.HasSuffix(stamp, "Z")
		}
		var valid bool
		value.CreatedAt, valid = parse(created)
		if !valid {
			return nil, ErrInvalidStoredState
		}
		value.UpdatedAt, valid = parse(updated)
		if !valid || value.UpdatedAt.Before(value.CreatedAt) {
			return nil, ErrInvalidStoredState
		}
		if finished != "" {
			value.FinishedAt, valid = parse(finished)
			if !valid || value.FinishedAt.Before(value.CreatedAt) || value.FinishedAt.After(value.UpdatedAt) {
				return nil, ErrInvalidStoredState
			}
		}
		head, found := byApp[value.AppID]
		if !found || value.DeploymentID != head.DeploymentID || value.ReleaseID != head.ReleaseID ||
			value.Slot != head.Slot || !validGatewayStartupRuntimeComponent(value) || counts[value.AppID] >= 64 {
			return nil, ErrInvalidStoredState
		}
		if len(result) > 0 {
			previous := result[len(result)-1]
			if previous.AppID > value.AppID || (previous.AppID == value.AppID && previous.Name >= value.Name) {
				return nil, ErrInvalidStoredState
			}
		}
		counts[value.AppID]++
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, head := range heads {
		if counts[head.AppID] == 0 {
			return nil, ErrInvalidStoredState
		}
	}
	return result, nil
}

func validGatewayStartupRuntimeComponent(value GatewayStartupRuntimeComponent) bool {
	if !validText(value.Name, 256) || (value.ImageArtifactID != "" && !validUUID(value.ImageArtifactID)) ||
		(value.ContainerName != "" && !validText(value.ContainerName, 128)) ||
		(value.ContainerID != "" && !validDigest(value.ContainerID)) {
		return false
	}
	terminal := value.State == "stopped" || value.State == "failed"
	if terminal == value.FinishedAt.IsZero() {
		return false
	}
	if value.State == "failed" {
		switch value.DiagnosticCode {
		case "start_failed", "health_failed", "runtime_unavailable", "daemon_restarted", "cancelled", "internal_error":
		default:
			return false
		}
	} else if value.DiagnosticCode != "" {
		return false
	}
	switch value.State {
	case "pending":
		return value.ImageArtifactID == "" && value.ContainerName == "" && value.ContainerID == ""
	case "image_ready":
		return value.ImageArtifactID != "" && value.ContainerName == "" && value.ContainerID == ""
	case "starting":
		return value.ImageArtifactID != "" && value.ContainerName != "" && value.ContainerID == ""
	case "running", "healthy", "active", "draining":
		return value.ImageArtifactID != "" && value.ContainerName != "" && value.ContainerID != ""
	case "stopped", "failed":
		return true
	default:
		return false
	}
}
