package generatedingress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strconv"
)

const (
	minimumLANPort = uint16(8100)
	maximumLANPort = uint16(8119)
)

// caddyV2Profile is the bounded set of container ports that the gateway
// manager may publish after it separately proves the approved host binding.
// This builder only creates Caddy JSON; it does not publish host ports.
type caddyV2Profile struct {
	SelectedIPv4 string
	PortStart    uint16
	PortEnd      uint16
	ProbeToken   string
}

type caddyV2LANAssignment struct {
	AppID                string
	AllocationID         string
	AccessRevisionID     string
	AccessRevisionNumber int64
	AccessSpecDigest     string
}

// buildCaddyConfigV2 preserves the v1 .rig.localhost app routes, adds an
// explicit wrong-Host 404 fallback, and adds one dedicated server for every
// LAN pool port. A port can select at most one app, and that app is served
// only for the approved IPv4 Host. All other requests and all unassigned
// ports terminate with a generic 404.
func buildCaddyConfigV2(routes map[string]routeRecord, localListenAddress string, profile caddyV2Profile, assignments map[uint16]caddyV2LANAssignment) ([]byte, error) {
	if !validCaddyV2Profile(profile) {
		return nil, errors.New("invalid generated ingress LAN profile")
	}

	// Building the local server first deliberately reuses every v1 validation
	// and app routing rule. The v2 server adds only an unmatched-Host fallback.
	localBody, err := buildCaddyConfig(routes, localListenAddress)
	if err != nil {
		return nil, err
	}
	var result caddyConfig
	if err := json.Unmarshal(localBody, &result); err != nil {
		return nil, errors.New("invalid generated ingress local configuration")
	}

	listenHost, _, err := net.SplitHostPort(localListenAddress)
	listenIP, parseErr := netip.ParseAddr(listenHost)
	if err != nil || parseErr != nil || !listenIP.Is4() || !listenIP.IsGlobalUnicast() ||
		listenIP.IsLinkLocalUnicast() || listenIP.String() != listenHost {
		return nil, errors.New("invalid generated ingress LAN listener")
	}
	if assignments == nil {
		assignments = map[uint16]caddyV2LANAssignment{}
	}
	if err := validateCaddyV2Assignments(routes, profile, assignments); err != nil {
		return nil, err
	}
	local := result.Apps.HTTP.Servers["generated"]
	if profile.ProbeToken != "" {
		appIDs := make([]string, 0, len(routes))
		for appID := range routes {
			appIDs = append(appIDs, appID)
		}
		sort.Strings(appIDs)
		probes := make([]caddyRoute, 0, len(appIDs)+len(local.Routes))
		for _, appID := range appIDs {
			probes = append(probes, gatewayV2ProbeRoute(appID+".rig.localhost", gatewayV2AppChallenge(profile.ProbeToken, appID)))
		}
		local.Routes = append(probes, local.Routes...)
	}
	local.Routes = append(local.Routes, notFoundRoute())
	result.Apps.HTTP.Servers["generated"] = local

	for port := profile.PortStart; ; port++ {
		server := caddyServer{
			Listen:         []string{net.JoinHostPort(listenHost, strconv.FormatUint(uint64(port), 10))},
			AutomaticHTTPS: caddyAutomaticHTTPS{Disable: true},
		}
		if profile.ProbeToken != "" {
			server.Routes = append(server.Routes, gatewayV2ProbeRoute(profile.SelectedIPv4, gatewayV2PortChallenge(profile.ProbeToken, port)))
		}
		assignment, assigned := assignments[port]
		if assigned {
			if profile.ProbeToken != "" {
				server.Routes = append(server.Routes, gatewayV2ProbeRoute(profile.SelectedIPv4, gatewayV2LANAppChallenge(profile.ProbeToken, port, assignment)))
			}
			server.Routes = append(server.Routes, hostRestrictedRoutes(routes[assignment.AppID], profile.SelectedIPv4)...)
		} else {
			server.Routes = append(server.Routes, notFoundRoute())
		}
		result.Apps.HTTP.Servers[lanServerName(port)] = server
		if port == profile.PortEnd {
			break
		}
	}

	return json.Marshal(result)
}

// A distinct response per published port detects host-side forwarding to the
// wrong Caddy listener even when both ports belong to the same gateway.
func gatewayV2PortChallenge(base string, port uint16) string {
	sum := sha256.Sum256([]byte("rig-gateway-v2-port\x00" + base + "\x00" + strconv.FormatUint(uint64(port), 10)))
	return hex.EncodeToString(sum[:])
}

func gatewayV2AppChallenge(base, appID string) string {
	sum := sha256.Sum256([]byte("rig-gateway-v2-app\x00" + base + "\x00" + appID))
	return hex.EncodeToString(sum[:])
}

func gatewayV2LANAppChallenge(base string, port uint16, assignment caddyV2LANAssignment) string {
	sum := sha256.Sum256([]byte("rig-gateway-v2-lan-app\x00" + base + "\x00" + strconv.FormatUint(uint64(port), 10) + "\x00" +
		assignment.AppID + "\x00" + assignment.AllocationID + "\x00" + assignment.AccessRevisionID + "\x00" +
		strconv.FormatInt(assignment.AccessRevisionNumber, 10) + "\x00" + assignment.AccessSpecDigest))
	return hex.EncodeToString(sum[:])
}

func validCaddyV2Profile(profile caddyV2Profile) bool {
	address, err := netip.ParseAddr(profile.SelectedIPv4)
	return err == nil && address.Is4() && address.IsPrivate() && address.String() == profile.SelectedIPv4 &&
		profile.PortStart >= minimumLANPort && profile.PortStart <= profile.PortEnd && profile.PortEnd <= maximumLANPort &&
		(profile.ProbeToken == "" || validSHA256(profile.ProbeToken))
}

// A gateway-specific 404 body distinguishes an attested Caddy listener from
// an unrelated host process that happens to return an ordinary 404. The Host
// matcher prevents requests for another authority from reaching this route.
func gatewayV2ProbeRoute(selectedIPv4, token string) caddyRoute {
	return caddyRoute{
		Match:  []caddyMatch{{Host: []string{selectedIPv4}, Path: []string{"/.well-known/rig-gateway/" + token}}},
		Handle: []caddyHandle{{Handler: "static_response", StatusCode: 404, Body: "rig-gateway-v2:" + token}},
	}
}

func validateCaddyV2Assignments(routes map[string]routeRecord, profile caddyV2Profile, assignments map[uint16]caddyV2LANAssignment) error {
	ports := make([]int, 0, len(assignments))
	for port := range assignments {
		ports = append(ports, int(port))
	}
	sort.Ints(ports)

	seenApps := make(map[string]struct{}, len(assignments))
	for _, rawPort := range ports {
		port := uint16(rawPort)
		assignment := assignments[port]
		appID := assignment.AppID
		if port < profile.PortStart || port > profile.PortEnd || !validAppID(appID) {
			return errors.New("invalid generated ingress LAN assignment")
		}
		if !validCanonicalUUID(assignment.AllocationID) ||
			!validCanonicalUUID(assignment.AccessRevisionID) || assignment.AccessRevisionNumber <= 0 ||
			!validSHA256(assignment.AccessSpecDigest) {
			return errors.New("invalid generated ingress LAN assignment proof")
		}
		if _, exists := routes[appID]; !exists {
			return errors.New("generated ingress LAN assignment has no active route")
		}
		if _, duplicate := seenApps[appID]; duplicate {
			return errors.New("generated ingress app has conflicting LAN assignments")
		}
		seenApps[appID] = struct{}{}
	}
	return nil
}

func hostRestrictedRoutes(route routeRecord, selectedIPv4 string) []caddyRoute {
	// Caddy's modules/caddyhttp.MatchHost.MatchWithError removes an optional
	// request port before comparison. Store only the approved canonical IPv4
	// in each matcher. The final unconditional 404 ensures every other Host
	// terminates without app content;
	// a live Caddy request matrix remains part of the gateway integration gate.
	if len(route.Endpoints) == 1 {
		return []caddyRoute{
			proxyRoute(selectedIPv4, nil, route.Endpoints[0]),
			notFoundRoute(),
		}
	}

	var apiIndex, staticIndex int
	for index, endpoint := range route.Endpoints {
		if endpoint.Role == "server" {
			apiIndex = index
		} else {
			staticIndex = index
		}
	}
	return []caddyRoute{
		proxyRoute(selectedIPv4, []string{"/api", "/api/*"}, route.Endpoints[apiIndex]),
		proxyRoute(selectedIPv4, nil, route.Endpoints[staticIndex]),
		notFoundRoute(),
	}
}

func lanServerName(port uint16) string {
	return "lan-" + strconv.FormatUint(uint64(port), 10)
}

func notFoundRoute() caddyRoute {
	return caddyRoute{Handle: []caddyHandle{{Handler: "static_response", StatusCode: 404}}}
}
