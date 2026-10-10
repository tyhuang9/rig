package generatedingress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

func (d managerGatewayRebindFinalHandoverDriver) proveHandoverFinalRoutes(ctx context.Context,
	value gatewayRebindFinalHandoverContext, id string,
) bool {
	if value.Plan == nil || !validGatewayRebindFinalHandoverPlan(value, *value.Plan) || !validContainerID(id) {
		return false
	}
	plan := value.SequenceTwelve.Stage.FinalConfigIntent.RoutePlan
	body, err := gatewayRebindFinalConfigBytes(value.Intent, plan)
	if err != nil {
		return false
	}
	defer clear(body)
	live, err := d.liveConfig(ctx, id)
	defer clear(live)
	if err != nil || !sameCaddyConfig(body, live) || !d.manager.proveGatewayRouteEndpointTransports(ctx, id, gatewayRebindFinalHandoverRoutes(value)) {
		return false
	}
	token, err := gatewayRebindStageConfigProbeToken(value.Intent)
	if err != nil {
		return false
	}
	assignments := make(map[uint16]caddyV2LANAssignment)
	for _, binding := range plan.Routes {
		if binding.SourceLAN != nil {
			assignments[binding.SourceLAN.Port] = caddyV2LANAssignment{AppID: binding.AppID,
				AllocationID: binding.SourceLAN.AllocationID, AccessRevisionID: binding.SourceLAN.AccessRevisionID,
				AccessRevisionNumber: binding.SourceLAN.AccessRevisionNumber, AccessSpecDigest: binding.SourceLAN.AccessSpecDigest}
		}
		challenge := gatewayV2AppChallenge(token, binding.AppID)
		host := binding.AppID + ".rig.localhost"
		if !exactGatewayV2HostChallenge(ctx, d.hostProbe, "127.0.0.1", value.Plan.LocalHostPort, host, challenge) ||
			!d.manager.probeGatewayV2AnyStatus(ctx, id, value.Intent.Intent.Network.ContainerIPv4, gatewayV2ContainerPort, host) {
			return false
		}
		root := d.hostProbe(ctx, "127.0.0.1", value.Plan.LocalHostPort, host, "/")
		if ctx.Err() != nil || !root.Connected || !root.Responded || root.Status < 200 || root.Status > 599 {
			return false
		}
	}
	if !exactGatewayV2HostStatus(ctx, d.hostProbe, "127.0.0.1", value.Plan.LocalHostPort, "wrong.invalid", http.StatusNotFound) {
		return false
	}
	profile := plan.EffectiveSuccessorProfile
	for port := profile.PortStart; ; port++ {
		challenge := gatewayV2PortChallenge(token, port)
		if !exactGatewayV2HostChallenge(ctx, d.hostProbe, profile.SelectedIPv4, port, profile.SelectedIPv4, challenge) ||
			!d.containerProbe(ctx, id, value.Intent.Intent.Network.ContainerIPv4, port, profile.SelectedIPv4, challenge) ||
			!exactGatewayV2HostStatus(ctx, d.hostProbe, profile.SelectedIPv4, port, "wrong.invalid", http.StatusNotFound) ||
			!probeGatewayRebindHandoverListenerAbsent(ctx, "127.0.0.1", port) {
			return false
		}
		result := d.hostProbe(ctx, profile.SelectedIPv4, port, profile.SelectedIPv4, "/")
		if ctx.Err() != nil || !result.Connected || !result.Responded {
			return false
		}
		if assignment, exists := assignments[port]; exists {
			appChallenge := gatewayV2LANAppChallenge(token, port, assignment)
			if result.Status < 200 || result.Status > 599 ||
				!exactGatewayV2HostChallenge(ctx, d.hostProbe, profile.SelectedIPv4, port, profile.SelectedIPv4, appChallenge) ||
				!d.containerProbe(ctx, id, value.Intent.Intent.Network.ContainerIPv4, port, profile.SelectedIPv4, appChallenge) ||
				!d.manager.probeGatewayV2AnyStatus(ctx, id, value.Intent.Intent.Network.ContainerIPv4, port, profile.SelectedIPv4) {
				return false
			}
		} else if result.Status != http.StatusNotFound {
			return false
		}
		if port == profile.PortEnd {
			return true
		}
	}
}

// A stopped Docker state is insufficient when the host proxy retains a bind.
// Probe each withdrawn publication unless its exact address/port is currently
// owned by the other recorded gateway. Foreign listeners leave the operation
// unresolved instead of authorizing another start or claiming restoration.
func (d managerGatewayRebindFinalHandoverDriver) proveHandoverWithdrawnBindings(ctx context.Context,
	value gatewayRebindFinalHandoverContext, proof gatewayRebindFinalHandoverObservation,
) bool {
	return proveGatewayRebindHandoverWithdrawnBindings(ctx, value, proof, probeGatewayRebindHandoverListenerAbsent)
}

func proveGatewayRebindHandoverWithdrawnBindings(ctx context.Context, value gatewayRebindFinalHandoverContext,
	proof gatewayRebindFinalHandoverObservation, absent func(context.Context, string, uint16) bool,
) bool {
	if ctx == nil || absent == nil {
		return false
	}
	successor := value.Intent.Intent.SuccessorProfile
	predecessor := value.Predecessor.State.Profile
	successorRunning := proof.Stage == gatewayRebindHandoverContainerRunning || proof.Final == gatewayRebindHandoverContainerRunning
	if !successorRunning {
		for port := successor.PortStart; ; port++ {
			shared := proof.PredecessorRunning && predecessor.SelectedIPv4 == successor.SelectedIPv4 && port >= predecessor.PortStart && port <= predecessor.PortEnd
			if !shared && !absent(ctx, successor.SelectedIPv4, port) {
				return false
			}
			if port == successor.PortEnd {
				break
			}
		}
	}
	if !proof.PredecessorRunning {
		if proof.PredecessorAddress == gatewayRebindPredecessorAddressPresent {
			for port := predecessor.PortStart; ; port++ {
				shared := successorRunning && successor.SelectedIPv4 == predecessor.SelectedIPv4 && port >= successor.PortStart && port <= successor.PortEnd
				if !shared && !absent(ctx, predecessor.SelectedIPv4, port) {
					return false
				}
				if port == predecessor.PortEnd {
					break
				}
			}
		}
		if proof.Final != gatewayRebindHandoverContainerRunning && !absent(ctx, "127.0.0.1", value.Predecessor.Journal.Source.LocalHostPort) {
			return false
		}
	}
	return ctx.Err() == nil
}

func probeGatewayRebindHandoverListenerAbsent(ctx context.Context, address string, port uint16) bool {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return gatewayRebindHandoverListenerAbsentWithDial(ctx, address, port, dialer.DialContext)
}

func gatewayRebindHandoverListenerAbsentWithDial(ctx context.Context, address string, port uint16,
	dial func(context.Context, string, string) (net.Conn, error),
) bool {
	ip, err := netip.ParseAddr(address)
	if ctx == nil || ctx.Err() != nil || dial == nil || port == 0 || err != nil || !ip.Is4() ||
		ip.String() != address || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	connection, err := dial(probeCtx, "tcp4", net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10)))
	if connection != nil {
		_ = connection.Close()
		return false
	}
	return ctx.Err() == nil && probeCtx.Err() == nil && errors.Is(err, syscall.ECONNREFUSED)
}
