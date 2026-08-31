package generatedingress

import (
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type caddyConfig struct {
	Admin caddyAdmin `json:"admin"`
	Apps  caddyApps  `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen"`
}
type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
}
type caddyHTTP struct {
	Servers map[string]caddyServer `json:"servers"`
}
type caddyServer struct {
	Listen []string     `json:"listen"`
	Routes []caddyRoute `json:"routes"`
}
type caddyRoute struct {
	Match  []caddyMatch  `json:"match"`
	Handle []caddyHandle `json:"handle"`
}
type caddyMatch struct {
	Host []string `json:"host"`
	Path []string `json:"path,omitempty"`
}
type caddyHandle struct {
	Handler   string          `json:"handler"`
	Upstreams []caddyUpstream `json:"upstreams"`
}
type caddyUpstream struct {
	Dial string `json:"dial"`
}

func buildCaddyConfig(routes map[string]routeRecord) ([]byte, error) {
	if routes == nil || len(routes) > maxStateApps {
		return nil, errors.New("invalid generated ingress routes")
	}
	appIDs := make([]string, 0, len(routes))
	for appID, route := range routes {
		if !validAppID(appID) || validateRoute(route) != nil {
			return nil, errors.New("invalid generated ingress route")
		}
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	result := caddyConfig{Admin: caddyAdmin{Listen: "localhost:2019"}, Apps: caddyApps{HTTP: caddyHTTP{Servers: map[string]caddyServer{
		"generated": {Listen: []string{":8080"}, Routes: make([]caddyRoute, 0, len(routes)*2)},
	}}}}
	server := result.Apps.HTTP.Servers["generated"]
	for _, appID := range appIDs {
		host := appID + ".rig.localhost"
		route := routes[appID]
		if len(route.Endpoints) == 1 {
			server.Routes = append(server.Routes, proxyRoute(host, nil, route.Endpoints[0]))
			continue
		}
		var api, static generatedruntime.RouteEndpoint
		for _, endpoint := range route.Endpoints {
			if endpoint.Role == "server" {
				api = endpoint
			} else {
				static = endpoint
			}
		}
		server.Routes = append(server.Routes,
			proxyRoute(host, []string{"/api", "/api/*"}, api),
			proxyRoute(host, nil, static),
		)
	}
	result.Apps.HTTP.Servers["generated"] = server
	return json.Marshal(result)
}

func proxyRoute(host string, paths []string, endpoint generatedruntime.RouteEndpoint) caddyRoute {
	return caddyRoute{
		Match: []caddyMatch{{Host: []string{host}, Path: paths}},
		Handle: []caddyHandle{{Handler: "reverse_proxy", Upstreams: []caddyUpstream{{
			Dial: net.JoinHostPort(endpoint.NetworkAlias, strconv.FormatUint(uint64(endpoint.InternalPort), 10)),
		}}}},
	}
}

func validateRoute(route routeRecord) error {
	if route.Slot != generatedruntime.SlotBlue && route.Slot != generatedruntime.SlotGreen || len(route.Endpoints) < 1 || len(route.Endpoints) > 2 {
		return errors.New("invalid generated ingress route")
	}
	seenComponents := map[string]bool{}
	roles := map[string]int{}
	network := ""
	for _, endpoint := range route.Endpoints {
		if !validName(endpoint.Component, 64) || (endpoint.Role != "server" && endpoint.Role != "static") ||
			!validContainerID(endpoint.ContainerID) || !validName(endpoint.NetworkName, 96) || !validName(endpoint.NetworkAlias, 96) || endpoint.InternalPort == 0 || seenComponents[endpoint.Component] {
			return errors.New("invalid generated ingress endpoint")
		}
		if network == "" {
			network = endpoint.NetworkName
		} else if network != endpoint.NetworkName {
			return errors.New("generated ingress endpoints must share one app network")
		}
		seenComponents[endpoint.Component] = true
		roles[endpoint.Role]++
	}
	if len(route.Endpoints) == 2 && (roles["server"] != 1 || roles["static"] != 1) {
		return errors.New("generated ingress topology is ambiguous")
	}
	return nil
}

func validAppID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validContainerID(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validName(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return !strings.Contains(value, "..")
}
