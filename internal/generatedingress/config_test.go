package generatedingress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestBuildCaddyConfigSupportsServerStaticAndStaticAPI(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	routes := map[string]routeRecord{
		appB: {Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "net-b", "frontend-green", 4173, 'b'),
			endpoint("api", "server", "net-b", "api-green", 3000, 'c'),
		}},
		appA: {Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
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
	if len(server.Routes) != 3 {
		t.Fatalf("routes = %d, want 3", len(server.Routes))
	}
	if got := server.Routes[0].Match[0].Host[0]; got != appA+".rig.localhost" {
		t.Fatalf("first host = %q", got)
	}
	if got := server.Routes[1].Match[0].Path; len(got) != 2 || got[0] != "/api" || got[1] != "/api/*" {
		t.Fatalf("API paths = %#v", got)
	}
	if got := server.Routes[1].Handle[0].Upstreams[0].Dial; got != "api-green:3000" {
		t.Fatalf("API upstream = %q", got)
	}
	if got := server.Routes[2].Handle[0].Upstreams[0].Dial; got != "frontend-green:4173" {
		t.Fatalf("static upstream = %q", got)
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
			appID: {Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
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

func TestBuildCaddyConfigRemainsDeterministic(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	routeA := routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", "net-a", "web-blue", 3000, 'a'),
	}}
	routeB := routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
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
		{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("one", "server", "net-a", "one-blue", 3000, 'a'),
			endpoint("two", "server", "net-a", "two-blue", 3001, 'b'),
		}},
		{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
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

func endpoint(component, role, network, alias string, port uint16, id rune) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{Component: component, Role: role, ContainerID: strings.Repeat(string(id), 64), NetworkName: network, NetworkAlias: alias, InternalPort: port}
}
