package generatedingress

import (
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
	body, err := buildCaddyConfig(routes)
	if err != nil {
		t.Fatal(err)
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Apps.HTTP.Servers["generated"]
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
		if _, err := buildCaddyConfig(map[string]routeRecord{appID: route}); err == nil {
			t.Fatal("expected invalid topology to fail closed")
		}
	}
}

func endpoint(component, role, network, alias string, port uint16, id rune) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{Component: component, Role: role, ContainerID: strings.Repeat(string(id), 64), NetworkName: network, NetworkAlias: alias, InternalPort: port}
}
