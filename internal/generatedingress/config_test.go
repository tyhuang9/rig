package generatedingress

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
)

func TestBuildCaddyConfigSupportsServerStaticAndStaticAPI(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	routes := map[string]routeRecord{
		appB: {Slot: generatedidentity.SlotGreen, Endpoints: []generatedroute.RouteEndpoint{
			endpoint("frontend", "static", "net-b", "frontend-green", 4173, 'b'),
			endpoint("api", "server", "net-b", "api-green", 3000, 'c'),
		}},
		appA: {Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
			endpoint("web", "server", "net-a", "web-blue", 3000, 'a'),
		}},
	}
	body, err := buildCaddyConfig(routes, "10.203.0.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Apps.HTTP.Servers["generated"]
	if len(server.Listen) != 1 || server.Listen[0] != "10.203.0.2:8080" {
		t.Fatalf("listener = %#v, want dedicated ingress address", server.Listen)
	}
	if len(server.Routes) != 4 {
		t.Fatalf("routes = %d, want 3 proxy routes and terminal 404", len(server.Routes))
	}
	if got := server.Routes[0].Match[0].Host[0]; got != appA+".rig.localhost" {
		t.Fatalf("first host = %q", got)
	}
	if got := server.Routes[1].Match[0].Path; len(got) != 2 || got[0] != "/api" || got[1] != "/api/*" {
		t.Fatalf("API paths = %#v", got)
	}
	if got := server.Routes[1].Handle[0].Upstreams[0].Dial; got != "api-green.net-b:3000" {
		t.Fatalf("API upstream = %q", got)
	}
	if got := server.Routes[2].Handle[0].Upstreams[0].Dial; got != "frontend-green.net-b:4173" {
		t.Fatalf("static upstream = %q", got)
	}
	if !reflect.DeepEqual(server.Routes[3], notFoundRoute()) {
		t.Fatalf("terminal route = %#v, want empty 404", server.Routes[3])
	}
}

func TestBuildCaddyConfigTerminatesUnmatchedHostsWithEmpty404(t *testing.T) {
	appID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name        string
		routes      map[string]routeRecord
		proxyRoutes int
	}{
		{name: "empty", routes: map[string]routeRecord{}, proxyRoutes: 0},
		{name: "routed", routes: map[string]routeRecord{
			appID: {Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
				endpoint("web", "server", "net-a", "web-blue", 3000, 'a'),
			}},
		}, proxyRoutes: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := buildCaddyConfig(test.routes, "10.203.0.2:8080")
			if err != nil {
				t.Fatal(err)
			}
			var config caddyConfig
			if err := json.Unmarshal(body, &config); err != nil {
				t.Fatal(err)
			}
			routes := config.Apps.HTTP.Servers["generated"].Routes
			if len(routes) != test.proxyRoutes+1 || !reflect.DeepEqual(routes[len(routes)-1], notFoundRoute()) {
				t.Fatalf("routes = %#v, want %d matched proxies followed by one empty 404", routes, test.proxyRoutes)
			}
			for index, route := range routes[:test.proxyRoutes] {
				if len(route.Match) != 1 || len(route.Match[0].Host) != 1 || route.Match[0].Host[0] != appID+".rig.localhost" ||
					len(route.Handle) != 1 || route.Handle[0].Handler != "reverse_proxy" {
					t.Fatalf("route %d = %#v, want intended-host proxy before fallback", index, route)
				}
			}
		})
	}
}

func TestBuildCaddyConfigExplicitlyDisablesAutomaticHTTPS(t *testing.T) {
	appID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name   string
		routes map[string]routeRecord
	}{
		{name: "empty recovery", routes: map[string]routeRecord{}},
		{name: "routed", routes: map[string]routeRecord{
			appID: {Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
				endpoint("web", "server", "net-a", "web-blue", 3000, 'a'),
			}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := buildCaddyConfig(test.routes, "10.203.0.2:8080")
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatal(err)
			}
			var apps map[string]json.RawMessage
			if err := json.Unmarshal(document["apps"], &apps); err != nil {
				t.Fatal(err)
			}
			var httpApp map[string]json.RawMessage
			if err := json.Unmarshal(apps["http"], &httpApp); err != nil {
				t.Fatal(err)
			}
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(httpApp["servers"], &servers); err != nil {
				t.Fatal(err)
			}
			var generated map[string]json.RawMessage
			if err := json.Unmarshal(servers["generated"], &generated); err != nil {
				t.Fatal(err)
			}
			if raw, exists := generated["automatic_https"]; !exists || !bytes.Equal(raw, []byte(`{"disable":true}`)) {
				t.Fatalf("serialized automatic_https = %s, want exact disabled object", raw)
			}
		})
	}
}

func TestBuildCaddyConfigScopesSharedAliasesToEachApplicationNetwork(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	routes := map[string]routeRecord{
		appA: {Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
			endpoint("api", "server", "rig-a-first", "api-blue", 3000, 'a'),
		}},
		appB: {Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
			endpoint("api", "server", "rig-a-second", "api-blue", 3000, 'b'),
		}},
	}
	body, err := buildCaddyConfig(routes, "10.203.0.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	actual := map[string]string{}
	generatedRoutes := config.Apps.HTTP.Servers["generated"].Routes
	if !reflect.DeepEqual(generatedRoutes[len(generatedRoutes)-1], notFoundRoute()) {
		t.Fatalf("terminal route = %#v, want empty 404", generatedRoutes[len(generatedRoutes)-1])
	}
	for _, route := range generatedRoutes[:len(generatedRoutes)-1] {
		actual[route.Match[0].Host[0]] = route.Handle[0].Upstreams[0].Dial
	}
	for host, want := range map[string]string{
		appA + ".rig.localhost": "api-blue.rig-a-first:3000",
		appB + ".rig.localhost": "api-blue.rig-a-second:3000",
	} {
		if actual[host] != want {
			t.Fatalf("upstream for %s = %q, want network-scoped %q", host, actual[host], want)
		}
	}
	if routes[appA].Endpoints[0].NetworkAlias != "api-blue" || routes[appB].Endpoints[0].NetworkAlias != "api-blue" {
		t.Fatal("config generation mutated accepted endpoint aliases")
	}
}

func TestBuildCaddyConfigRemainsDeterministic(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	routeA := routeRecord{Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
		endpoint("web", "server", "net-a", "web-blue", 3000, 'a'),
	}}
	routeB := routeRecord{Slot: generatedidentity.SlotGreen, Endpoints: []generatedroute.RouteEndpoint{
		endpoint("frontend", "static", "net-b", "frontend-green", 4173, 'b'),
		endpoint("api", "server", "net-b", "api-green", 3000, 'c'),
	}}
	first, err := buildCaddyConfig(map[string]routeRecord{appB: routeB, appA: routeA}, "10.203.0.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildCaddyConfig(map[string]routeRecord{appA: routeA, appB: routeB}, "10.203.0.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("equivalent route maps produced different Caddy JSON")
	}
}

func TestBuildCaddyConfigRejectsWildcardOrApplicationSideListeners(t *testing.T) {
	for _, address := range []string{"", ":8080", "0.0.0.0:3000", "rig-app-network:8080"} {
		if _, err := buildCaddyConfig(map[string]routeRecord{}, address); err == nil {
			t.Fatalf("listener %q was accepted", address)
		}
	}
}

func TestBuildCaddyConfigRejectsAmbiguousOrCrossNetworkTopologies(t *testing.T) {
	appID := "11111111-1111-4111-8111-111111111111"
	tests := []routeRecord{
		{Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
			endpoint("one", "server", "net-a", "one-blue", 3000, 'a'),
			endpoint("two", "server", "net-a", "two-blue", 3001, 'b'),
		}},
		{Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{
			endpoint("web", "static", "net-a", "web-blue", 4173, 'a'),
			endpoint("api", "server", "net-b", "api-blue", 3000, 'b'),
		}},
	}
	for _, route := range tests {
		if _, err := buildCaddyConfig(map[string]routeRecord{appID: route}, "10.203.0.2:8080"); err == nil {
			t.Fatal("expected invalid topology to fail closed")
		}
	}
}

func endpoint(component, role, network, alias string, port uint16, id rune) generatedroute.RouteEndpoint {
	return generatedroute.RouteEndpoint{Component: component, Role: role, ContainerID: strings.Repeat(string(id), 64), NetworkName: network, NetworkAlias: alias, InternalPort: port}
}
