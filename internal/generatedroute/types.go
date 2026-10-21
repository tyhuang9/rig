// Package generatedroute defines the data-only route contract shared by the
// generated runtime and ingress gateway.
package generatedroute

import (
	"time"

	"github.com/hostd/hostd/internal/generatedidentity"
)

// RouteEndpoint identifies one container endpoint that the ingress gateway
// may publish. The fields are immutable route data; ownership and readiness
// remain enforced by the runtime and ingress implementations.
type RouteEndpoint struct {
	Component    string
	Role         string
	ContainerID  string
	NetworkName  string
	NetworkAlias string
	InternalPort uint16
}

// RouteSwitchRequest carries the complete route proposal across the runtime
// and ingress boundary.
type RouteSwitchRequest struct {
	AppID       string
	FromSlot    generatedidentity.Slot
	ToSlot      generatedidentity.Slot
	Endpoints   []RouteEndpoint
	DrainPeriod time.Duration
}
