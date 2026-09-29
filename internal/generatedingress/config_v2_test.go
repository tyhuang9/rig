package generatedingress

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestBuildCaddyConfigV2KeepsLocalRoutesAndIsolatesLANPorts(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	selectedIPv4 := "192.168.50.20"
	routes := map[string]routeRecord{
		appB: {Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("web", "server", "net-b", "web-green", 3000, 'b'),
		}},
		appA: {Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-blue", 4173, 'a'),
			endpoint("api", "server", "net-a", "api-blue", 3000, 'c'),
		}},
	}
	localListen := "10.203.0.2:8080"
	body, err := buildCaddyConfigV2(routes, localListen, caddyV2Profile{SelectedIPv4: selectedIPv4, PortStart: 8100, PortEnd: 8102}, map[uint16]string{
		8101: appB,
		8100: appA,
	})
	if err != nil {
		t.Fatal(err)
	}

	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if config.Admin.Listen != "localhost:2019" {
		t.Fatalf("admin listener = %q, want loopback-only v1 listener", config.Admin.Listen)
	}
	if len(config.Apps.HTTP.Servers) != 4 {
		t.Fatalf("servers = %d, want local plus three LAN listeners", len(config.Apps.HTTP.Servers))
	}

	v1Body, err := buildCaddyConfig(routes, localListen)
	if err != nil {
		t.Fatal(err)
	}
	var v1 caddyConfig
	if err := json.Unmarshal(v1Body, &v1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Apps.HTTP.Servers["generated"], v1.Apps.HTTP.Servers["generated"]) {
		t.Fatal("v2 changed the existing .rig.localhost server")
	}

	for name, server := range config.Apps.HTTP.Servers {
		if !server.AutomaticHTTPS.Disable {
			t.Fatalf("server %q did not disable automatic HTTPS", name)
		}
	}

	lanA := config.Apps.HTTP.Servers["lan-8100"]
	if !reflect.DeepEqual(lanA.Listen, []string{"10.203.0.2:8100"}) {
		t.Fatalf("app A listener = %#v", lanA.Listen)
	}
	if len(lanA.Routes) != 3 {
		t.Fatalf("app A routes = %d, want API, frontend, and wrong-Host 404", len(lanA.Routes))
	}
	if got := lanA.Routes[0].Match; len(got) != 1 || !reflect.DeepEqual(got[0].Host, []string{selectedIPv4}) || !reflect.DeepEqual(got[0].Path, []string{"/api", "/api/*"}) {
		t.Fatalf("app A API matcher = %#v", got)
	}
	if got := lanA.Routes[0].Handle[0].Upstreams[0].Dial; got != "api-blue.net-a:3000" {
		t.Fatalf("app A API upstream = %q", got)
	}
	if got := lanA.Routes[1].Match; len(got) != 1 || !reflect.DeepEqual(got[0].Host, []string{selectedIPv4}) || len(got[0].Path) != 0 {
		t.Fatalf("app A frontend matcher = %#v", got)
	}
	if got := lanA.Routes[1].Handle[0].Upstreams[0].Dial; got != "frontend-blue.net-a:4173" {
		t.Fatalf("app A frontend upstream = %q", got)
	}
	assertLANServerHostPolicy(t, lanA, selectedIPv4, 2)
	assertLANServerExcludesNetwork(t, lanA, ".net-b:")

	lanB := config.Apps.HTTP.Servers["lan-8101"]
	if !reflect.DeepEqual(lanB.Listen, []string{"10.203.0.2:8101"}) || len(lanB.Routes) != 2 {
		t.Fatalf("app B server = %#v", lanB)
	}
	if got := lanB.Routes[0].Handle[0].Upstreams[0].Dial; got != "web-green.net-b:3000" {
		t.Fatalf("app B upstream = %q", got)
	}
	assertLANServerHostPolicy(t, lanB, selectedIPv4, 1)
	assertLANServerExcludesNetwork(t, lanB, ".net-a:")

	unassigned := config.Apps.HTTP.Servers["lan-8102"]
	if !reflect.DeepEqual(unassigned.Listen, []string{"10.203.0.2:8102"}) || len(unassigned.Routes) != 1 || len(unassigned.Routes[0].Match) != 0 {
		t.Fatalf("unassigned server = %#v", unassigned)
	}
	handle := unassigned.Routes[0].Handle[0]
	if handle.Handler != "static_response" || handle.StatusCode != 404 || len(handle.Upstreams) != 0 {
		t.Fatalf("unassigned response = %#v, want generic 404", handle)
	}

	var raw struct {
		Apps struct {
			HTTP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		proxyRoutes int
	}{{name: "lan-8100", proxyRoutes: 2}, {name: "lan-8101", proxyRoutes: 1}} {
		serialized := raw.Apps.HTTP.Servers[test.name]
		if got := bytes.Count(serialized, []byte(`"host":["192.168.50.20"]`)); got != test.proxyRoutes {
			t.Fatalf("%s approved IPv4 Host matchers = %d, want %d: %s", test.name, got, test.proxyRoutes, serialized)
		}
		if got := bytes.Count(serialized, []byte(`"match"`)); got != test.proxyRoutes {
			t.Fatalf("%s conditional routes = %d, want only %d backend routes; wrong-Host 404 must be unconditional: %s", test.name, got, test.proxyRoutes, serialized)
		}
		if bytes.Contains(serialized, []byte(selectedIPv4+":")) {
			t.Fatalf("%s included a port in its Host matcher: %s", test.name, serialized)
		}
	}
	if bytes.Contains(raw.Apps.HTTP.Servers["lan-8102"], []byte(`"host"`)) {
		t.Fatalf("unassigned listener serialized a Host matcher: %s", raw.Apps.HTTP.Servers["lan-8102"])
	}
	if bytes.Contains(raw.Apps.HTTP.Servers["lan-8102"], []byte(`"match"`)) {
		t.Fatalf("unassigned listener serialized a conditional matcher: %s", raw.Apps.HTTP.Servers["lan-8102"])
	}
}

func TestBuildCaddyConfigV2IsDeterministic(t *testing.T) {
	appA := "11111111-1111-4111-8111-111111111111"
	appB := "22222222-2222-4222-8222-222222222222"
	routeA := routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("api", "server", "net-a", "api-blue", 3000, 'a'),
	}}
	routeB := routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", "net-b", "web-green", 3000, 'b'),
	}}
	profile := caddyV2Profile{SelectedIPv4: "10.20.30.40", PortStart: 8100, PortEnd: 8119}
	first, err := buildCaddyConfigV2(
		map[string]routeRecord{appB: routeB, appA: routeA},
		"10.203.0.2:8080",
		profile,
		map[uint16]string{8119: appB, 8100: appA},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildCaddyConfigV2(
		map[string]routeRecord{appA: routeA, appB: routeB},
		"10.203.0.2:8080",
		profile,
		map[uint16]string{8100: appA, 8119: appB},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("equivalent v2 inputs produced different Caddy JSON")
	}
}

func TestBuildCaddyConfigV2RejectsInvalidOrConflictingInput(t *testing.T) {
	appID := "11111111-1111-4111-8111-111111111111"
	otherAppID := "22222222-2222-4222-8222-222222222222"
	validRoute := routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", "net-a", "web-blue", 3000, 'a'),
	}}
	validRoutes := map[string]routeRecord{appID: validRoute}
	validProfile := caddyV2Profile{SelectedIPv4: "172.20.30.40", PortStart: 8100, PortEnd: 8119}

	tests := []struct {
		name        string
		routes      map[string]routeRecord
		listen      string
		profile     caddyV2Profile
		assignments map[uint16]string
	}{
		{name: "nil routes", listen: "10.203.0.2:8080", profile: validProfile},
		{name: "wrong local port", routes: validRoutes, listen: "10.203.0.2:8100", profile: validProfile},
		{name: "wildcard local address", routes: validRoutes, listen: ":8080", profile: validProfile},
		{name: "IPv4 unspecified local address", routes: validRoutes, listen: "0.0.0.0:8080", profile: validProfile},
		{name: "IPv4 loopback local address", routes: validRoutes, listen: "127.0.0.1:8080", profile: validProfile},
		{name: "IPv4 link local address", routes: validRoutes, listen: "169.254.1.2:8080", profile: validProfile},
		{name: "IPv4 multicast local address", routes: validRoutes, listen: "224.0.0.1:8080", profile: validProfile},
		{name: "IPv6 local address", routes: validRoutes, listen: "[fd00::2]:8080", profile: validProfile},
		{name: "IPv4 mapped IPv6 local address", routes: validRoutes, listen: "[::ffff:10.203.0.2]:8080", profile: validProfile},
		{name: "IPv6 unspecified local address", routes: validRoutes, listen: "[::]:8080", profile: validProfile},
		{name: "IPv6 loopback local address", routes: validRoutes, listen: "[::1]:8080", profile: validProfile},
		{name: "missing selected IPv4", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{PortStart: 8100, PortEnd: 8119}},
		{name: "public selected IPv4", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: "8.8.8.8", PortStart: 8100, PortEnd: 8119}},
		{name: "loopback selected IPv4", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: "127.0.0.1", PortStart: 8100, PortEnd: 8119}},
		{name: "link local selected IPv4", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: "169.254.1.2", PortStart: 8100, PortEnd: 8119}},
		{name: "private IPv6 selected address", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: "fd00::2", PortStart: 8100, PortEnd: 8119}},
		{name: "selected IPv4 with port", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: "192.168.1.2:8100", PortStart: 8100, PortEnd: 8119}},
		{name: "profile below pool", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: validProfile.SelectedIPv4, PortStart: 8099, PortEnd: 8100}},
		{name: "profile above pool", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: validProfile.SelectedIPv4, PortStart: 8119, PortEnd: 8120}},
		{name: "profile reversed", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: validProfile.SelectedIPv4, PortStart: 8101, PortEnd: 8100}},
		{name: "assignment below profile", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: validProfile.SelectedIPv4, PortStart: 8101, PortEnd: 8119}, assignments: map[uint16]string{8100: appID}},
		{name: "assignment above profile", routes: validRoutes, listen: "10.203.0.2:8080", profile: caddyV2Profile{SelectedIPv4: validProfile.SelectedIPv4, PortStart: 8100, PortEnd: 8118}, assignments: map[uint16]string{8119: appID}},
		{name: "unknown app", routes: validRoutes, listen: "10.203.0.2:8080", profile: validProfile, assignments: map[uint16]string{8100: otherAppID}},
		{name: "invalid app id", routes: validRoutes, listen: "10.203.0.2:8080", profile: validProfile, assignments: map[uint16]string{8100: "not-an-app"}},
		{name: "same app on two ports", routes: validRoutes, listen: "10.203.0.2:8080", profile: validProfile, assignments: map[uint16]string{8100: appID, 8101: appID}},
		{name: "invalid active route", routes: map[string]routeRecord{appID: {Slot: generatedruntime.SlotBlue}}, listen: "10.203.0.2:8080", profile: validProfile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildCaddyConfigV2(test.routes, test.listen, test.profile, test.assignments); err == nil {
				t.Fatal("invalid v2 configuration was accepted")
			}
		})
	}
}

func assertLANServerHostPolicy(t *testing.T, server caddyServer, selectedIPv4 string, proxyRoutes int) {
	t.Helper()
	if len(server.Routes) != proxyRoutes+1 {
		t.Fatalf("LAN server routes = %d, want %d proxy routes plus 404", len(server.Routes), proxyRoutes)
	}
	for index, route := range server.Routes[:proxyRoutes] {
		if len(route.Match) != 1 || !reflect.DeepEqual(route.Match[0].Host, []string{selectedIPv4}) {
			t.Fatalf("LAN proxy route %d Host matcher = %#v", index, route.Match)
		}
	}
	fallback := server.Routes[proxyRoutes]
	if len(fallback.Match) != 0 || len(fallback.Handle) != 1 || fallback.Handle[0].Handler != "static_response" || fallback.Handle[0].StatusCode != 404 || len(fallback.Handle[0].Upstreams) != 0 {
		t.Fatalf("wrong-Host fallback = %#v, want unconditional generic 404", fallback)
	}
}

func assertLANServerExcludesNetwork(t *testing.T, server caddyServer, forbidden string) {
	t.Helper()
	for _, route := range server.Routes {
		for _, handle := range route.Handle {
			for _, upstream := range handle.Upstreams {
				if strings.Contains(upstream.Dial, forbidden) {
					t.Fatalf("LAN server crossed application networks through %q", upstream.Dial)
				}
			}
		}
	}
}
