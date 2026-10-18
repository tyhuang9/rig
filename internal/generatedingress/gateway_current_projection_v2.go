package generatedingress

import (
	"errors"
	"net"
	"sort"
	"strconv"
)

// gatewayCurrentProjectionV2 separates the complete retained ownership census
// from the routes which are currently allowed to serve traffic. StateDigest
// binds both views to the exact protected stable-state record.
type gatewayCurrentProjectionV2 struct {
	StateDigest        string
	RetainedApps       map[string]gatewayCurrentAppRouteV2
	NetworkOwners      map[string]string
	ReservedPorts      map[uint16]string
	ServingRoutes      map[string]routeRecord
	ServingAssignments map[uint16]caddyV2LANAssignment
	DormantApps        []string
}

// gatewayCurrentDormantExpectationV2 describes challenges which a future
// physical observer must find absent for one dormant application. It is an
// expected view only; it does not attest traffic withdrawal or runtime state.
type gatewayCurrentDormantExpectationV2 struct {
	AppID             string
	LocalHost         string
	LocalAppChallenge string
	LAN               *gatewayCurrentDormantLANExpectationV2
}

type gatewayCurrentDormantLANExpectationV2 struct {
	Port                 uint16
	Assignment           caddyV2LANAssignment
	LANAppChallenge      string
	GenericPortChallenge string
}

func projectGatewayCurrentStateV2(state gatewayCurrentRouteStateV2) (gatewayCurrentProjectionV2, error) {
	if !validGatewayCurrentRouteStateV2(state) {
		return gatewayCurrentProjectionV2{}, errors.New("invalid generated ingress current V2 projection state")
	}

	retainedApps := make(map[string]gatewayCurrentAppRouteV2, len(state.Apps))
	retainedRoutes := make(map[string]routeRecord, len(state.Apps))
	reservedPorts := make(map[uint16]string)
	servingRoutes := make(map[string]routeRecord, len(state.Apps))
	servingAssignments := make(map[uint16]caddyV2LANAssignment)
	dormantApps := make([]string, 0, len(state.Apps))
	for appID, app := range state.Apps {
		retainedApps[appID] = cloneGatewayCurrentAppRouteV2(app)
		retainedRoutes[appID] = cloneGatewayCurrentAppRouteV2(app).Route
		if app.LAN != nil {
			reservedPorts[app.LAN.Raw.Port] = appID
		}

		switch app.Traffic {
		case gatewayCurrentTrafficServingV2:
			servingRoutes[appID] = cloneGatewayCurrentAppRouteV2(app).Route
			if app.LAN != nil {
				servingAssignments[app.LAN.Raw.Port] = gatewayCurrentLANAssignmentV2(appID, *app.LAN)
			}
		case gatewayCurrentTrafficDormantV2:
			dormantApps = append(dormantApps, appID)
		default:
			return gatewayCurrentProjectionV2{}, errors.New("invalid generated ingress current V2 traffic projection")
		}
	}

	networkOwners, valid := gatewayRouteNetworkOwners(retainedRoutes)
	if !valid {
		return gatewayCurrentProjectionV2{}, errors.New("invalid generated ingress current V2 network ownership")
	}
	sort.Strings(dormantApps)
	return gatewayCurrentProjectionV2{
		StateDigest: state.Digest, RetainedApps: retainedApps, NetworkOwners: networkOwners,
		ReservedPorts: reservedPorts, ServingRoutes: servingRoutes,
		ServingAssignments: servingAssignments, DormantApps: dormantApps,
	}, nil
}

func gatewayCurrentPhysicalConfigBytesV2(state gatewayCurrentRouteStateV2) ([]byte, error) {
	projection, err := projectGatewayCurrentStateV2(state)
	if err != nil {
		return nil, err
	}
	token, err := gatewayCurrentPhysicalProbeTokenV2Validated(state)
	if err != nil {
		return nil, err
	}
	body, err := buildCaddyConfigV2(projection.ServingRoutes,
		net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart,
			PortEnd: state.Profile.PortEnd, ProbeToken: token}, projection.ServingAssignments)
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes {
		clear(body)
		return nil, errors.New("invalid generated ingress current V2 config")
	}
	return body, nil
}

func gatewayCurrentDormantExpectationsV2(state gatewayCurrentRouteStateV2) ([]gatewayCurrentDormantExpectationV2, error) {
	projection, err := projectGatewayCurrentStateV2(state)
	if err != nil {
		return nil, err
	}
	token, err := gatewayCurrentPhysicalProbeTokenV2Validated(state)
	if err != nil {
		return nil, err
	}
	result := make([]gatewayCurrentDormantExpectationV2, 0, len(projection.DormantApps))
	for _, appID := range projection.DormantApps {
		app := projection.RetainedApps[appID]
		expectation := gatewayCurrentDormantExpectationV2{
			AppID: appID, LocalHost: appID + ".rig.localhost",
			LocalAppChallenge: gatewayV2AppChallenge(token, appID),
		}
		if app.LAN != nil {
			assignment := gatewayCurrentLANAssignmentV2(appID, *app.LAN)
			expectation.LAN = &gatewayCurrentDormantLANExpectationV2{
				Port: app.LAN.Raw.Port, Assignment: assignment,
				LANAppChallenge:      gatewayV2LANAppChallenge(token, app.LAN.Raw.Port, assignment),
				GenericPortChallenge: gatewayV2PortChallenge(token, app.LAN.Raw.Port),
			}
		}
		result = append(result, expectation)
	}
	return result, nil
}

func gatewayCurrentPhysicalProbeTokenV2(state gatewayCurrentRouteStateV2) (string, error) {
	if !validGatewayCurrentRouteStateV2(state) {
		return "", errors.New("invalid generated ingress current V2 physical state")
	}
	return gatewayCurrentPhysicalProbeTokenV2Validated(state)
}

func gatewayCurrentPhysicalProbeTokenV2Validated(state gatewayCurrentRouteStateV2) (string, error) {
	switch state.Identity.Kind {
	case gatewayCurrentIdentityRebind:
		if state.Identity.Rebind == nil {
			return "", errors.New("unsupported generated ingress current V2 physical identity")
		}
		networkDigest, err := gatewayCurrentPhysicalNetworkDigest(state.Network)
		if err != nil {
			return "", err
		}
		return gatewayRebindStageConfigProbeTokenV1(state.Lineage.ProtectedIntentDigest, state.Lineage.OperationID,
			state.Identity.Rebind.Digest, networkDigest)
	case gatewayCurrentIdentityUpgrade:
		if state.Identity.Upgrade == nil {
			return "", errors.New("unsupported generated ingress current V2 physical identity")
		}
		return gatewayV2HostChallengeFor(state.Lineage.OperationID, state.Profile, *state.Identity.Upgrade, state.Network)
	default:
		return "", errors.New("unsupported generated ingress current V2 physical identity")
	}
}

func gatewayCurrentLANAssignmentV2(appID string, binding gatewayCurrentLANBinding) caddyV2LANAssignment {
	return caddyV2LANAssignment{
		AppID: appID, AllocationID: binding.Raw.AllocationID,
		AccessRevisionID: binding.Raw.AccessRevisionID, AccessRevisionNumber: binding.Raw.AccessRevisionNumber,
		AccessSpecDigest: binding.Raw.AccessSpecDigest,
	}
}
