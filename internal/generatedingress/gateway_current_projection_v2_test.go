package generatedingress

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
)

func TestGatewayCurrentProjectionV2(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	baseV1 := cloneGatewayCurrentRouteState(fixture.baseline)
	baseV2 := gatewayCurrentRouteStateV2ForTest(t, fixture)

	t.Run("all_serving_promotion_is_byte_identical", func(t *testing.T) {
		beforeV1, err := json.Marshal(baseV1)
		if err != nil {
			t.Fatal(err)
		}
		beforeV2, err := json.Marshal(baseV2)
		if err != nil {
			t.Fatal(err)
		}
		v1Body, err := gatewayCurrentPhysicalConfigBytes(baseV1)
		if err != nil {
			t.Fatal(err)
		}
		v2Body, err := gatewayCurrentPhysicalConfigBytesV2(baseV2)
		if err != nil {
			t.Fatal(err)
		}
		v1Token, err := gatewayCurrentPhysicalProbeToken(baseV1)
		if err != nil {
			t.Fatal(err)
		}
		v2Token, err := gatewayCurrentPhysicalProbeTokenV2(baseV2)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := projectGatewayCurrentStateV2(baseV2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(v1Body, v2Body) || v1Token != v2Token ||
			len(projection.RetainedApps) != len(baseV2.Apps) || len(projection.ServingRoutes) != len(baseV2.Apps) ||
			len(projection.DormantApps) != 0 {
			t.Fatal("all-serving V2 promotion changed physical bytes, token, or traffic membership")
		}
		afterV1, _ := json.Marshal(baseV1)
		afterV2, _ := json.Marshal(baseV2)
		if !bytes.Equal(beforeV1, afterV1) || !bytes.Equal(beforeV2, afterV2) ||
			baseV1.Digest != fixture.baseline.Digest || baseV2.Digest != projection.StateDigest {
			t.Fatal("projection or rendering mutated protected V1/V2 source state")
		}
	})

	t.Run("mixed_dormant_retains_ownership_and_preserves_serving_peer", func(t *testing.T) {
		_, state, appA, appB := gatewayCurrentProjectionV2TwoAppFixture(t, baseV1, baseV2)
		beforeBody := gatewayCurrentProjectionV2ConfigBytes(t, state)
		before := gatewayCurrentProjectionV2Config(t, beforeBody)
		state = gatewayCurrentProjectionV2Dormant(t, state, appA, gatewayCurrentDesiredStoppedV2)
		afterBody := gatewayCurrentProjectionV2ConfigBytes(t, state)
		after := gatewayCurrentProjectionV2Config(t, afterBody)
		projection, err := projectGatewayCurrentStateV2(state)
		if err != nil {
			t.Fatal(err)
		}

		beforeLocal := before.Apps.HTTP.Servers["generated"]
		afterLocal := after.Apps.HTTP.Servers["generated"]
		if got := gatewayCurrentProjectionV2HostRoutes(afterLocal, appA+".rig.localhost"); len(got) != 0 {
			t.Fatalf("dormant local host retained routes: %#v", got)
		}
		if !reflect.DeepEqual(gatewayCurrentProjectionV2HostRoutes(beforeLocal, appB+".rig.localhost"),
			gatewayCurrentProjectionV2HostRoutes(afterLocal, appB+".rig.localhost")) {
			t.Fatal("serving peer local routes changed when another app became dormant")
		}
		portA := state.Apps[appA].LAN.Raw.Port
		portB := state.Apps[appB].LAN.Raw.Port
		if !reflect.DeepEqual(before.Apps.HTTP.Servers[lanServerName(portB)], after.Apps.HTTP.Servers[lanServerName(portB)]) {
			t.Fatal("serving peer LAN server changed when another app became dormant")
		}
		token, err := gatewayCurrentPhysicalProbeTokenV2(state)
		if err != nil {
			t.Fatal(err)
		}
		for port := state.Profile.PortStart; ; port++ {
			challenge := gatewayV2PortChallenge(token, port)
			beforeRoute, beforeFound := gatewayCurrentProjectionV2ChallengeRoute(before.Apps.HTTP.Servers[lanServerName(port)],
				state.Profile.SelectedIPv4, challenge)
			afterRoute, afterFound := gatewayCurrentProjectionV2ChallengeRoute(after.Apps.HTTP.Servers[lanServerName(port)],
				state.Profile.SelectedIPv4, challenge)
			if !beforeFound || !afterFound || !reflect.DeepEqual(beforeRoute, afterRoute) {
				t.Fatalf("generic port challenge changed at %d", port)
			}
			if port == state.Profile.PortEnd {
				break
			}
		}
		portAServer := after.Apps.HTTP.Servers[lanServerName(portA)]
		if len(portAServer.Routes) != 2 || gatewayCurrentProjectionV2HasReverseProxy(portAServer) ||
			!reflect.DeepEqual(portAServer.Routes[1], notFoundRoute()) {
			t.Fatalf("dormant LAN port was not reduced to generic challenge plus deny: %#v", portAServer.Routes)
		}
		retainedA := projection.RetainedApps[appA]
		networkA := retainedA.Route.Endpoints[0].NetworkName
		if _, serving := projection.ServingRoutes[appA]; serving || projection.ReservedPorts[portA] != appA ||
			projection.NetworkOwners[networkA] != appA || retainedA.Runtime != state.Apps[appA].Runtime ||
			!reflect.DeepEqual(retainedA.LAN, state.Apps[appA].LAN) {
			t.Fatal("dormant app lost retained runtime, LAN reservation, or network ownership")
		}
	})

	t.Run("all_dormant_keeps_census_and_deny_listeners", func(t *testing.T) {
		_, state, _, _ := gatewayCurrentProjectionV2TwoAppFixture(t, baseV1, baseV2)
		for appID := range state.Apps {
			state = gatewayCurrentProjectionV2Dormant(t, state, appID, gatewayCurrentDesiredStoppedV2)
		}
		projection, err := projectGatewayCurrentStateV2(state)
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.RetainedApps) != len(state.Apps) || len(projection.ReservedPorts) != len(state.Apps) ||
			len(projection.ServingRoutes) != 0 || len(projection.ServingAssignments) != 0 ||
			len(projection.DormantApps) != len(state.Apps) {
			t.Fatalf("all-dormant census was collapsed: %#v", projection)
		}
		config := gatewayCurrentProjectionV2Config(t, gatewayCurrentProjectionV2ConfigBytes(t, state))
		local := config.Apps.HTTP.Servers["generated"]
		if !reflect.DeepEqual(local.Routes, []caddyRoute{notFoundRoute()}) {
			t.Fatalf("all-dormant local listener was not deny-only: %#v", local.Routes)
		}
		token, err := gatewayCurrentPhysicalProbeTokenV2(state)
		if err != nil {
			t.Fatal(err)
		}
		for port := state.Profile.PortStart; ; port++ {
			server := config.Apps.HTTP.Servers[lanServerName(port)]
			want := []caddyRoute{
				gatewayV2ProbeRoute(state.Profile.SelectedIPv4, gatewayV2PortChallenge(token, port)),
				notFoundRoute(),
			}
			if !reflect.DeepEqual(server.Routes, want) || gatewayCurrentProjectionV2HasReverseProxy(server) {
				t.Fatalf("all-dormant LAN listener %d was not exact challenge plus deny: %#v", port, server.Routes)
			}
			if port == state.Profile.PortEnd {
				break
			}
		}
	})

	t.Run("local_only_dormant_preserves_one_and_two_component_serving_shapes", func(t *testing.T) {
		v1, state, appA, appB := gatewayCurrentProjectionV2TwoAppFixture(t, baseV1, baseV2)
		v1AppA := v1.Apps[appA]
		v1AppA.LAN = nil
		v1.Apps[appA] = v1AppA
		gatewayCurrentProjectionV2RefreshV1(t, &v1)
		app := state.Apps[appA]
		app.LAN = nil
		app.Traffic = gatewayCurrentTrafficDormantV2
		app.Authority = gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredStoppedV2, gatewayCurrentLifecycleWithdrawV2)
		state.Apps[appA] = app
		state.SourceV1 = &gatewayCurrentRouteSourceV1{Revision: v1.Revision, Digest: v1.Digest}
		gatewayCurrentProjectionV2Refresh(t, &state)

		v1Config := gatewayCurrentProjectionV2Config(t, gatewayCurrentProjectionV1ConfigBytes(t, v1))
		v2Config := gatewayCurrentProjectionV2Config(t, gatewayCurrentProjectionV2ConfigBytes(t, state))
		if !reflect.DeepEqual(gatewayCurrentProjectionV2HostRoutes(v1Config.Apps.HTTP.Servers["generated"], appB+".rig.localhost"),
			gatewayCurrentProjectionV2HostRoutes(v2Config.Apps.HTTP.Servers["generated"], appB+".rig.localhost")) {
			t.Fatal("serving two-component peer changed after local-only dormant withdrawal")
		}
		proxyRoutes := gatewayCurrentProjectionV2ProxyRoutesForHost(v2Config.Apps.HTTP.Servers["generated"], appB+".rig.localhost")
		if len(proxyRoutes) != 2 || !reflect.DeepEqual(proxyRoutes[0].Match[0].Path, []string{"/api", "/api/*"}) ||
			len(proxyRoutes[1].Match[0].Path) != 0 {
			t.Fatalf("serving API/static split changed: %#v", proxyRoutes)
		}
		projection, err := projectGatewayCurrentStateV2(state)
		if err != nil {
			t.Fatal(err)
		}
		for port, owner := range projection.ReservedPorts {
			if owner == appA {
				t.Fatalf("local-only dormant app acquired synthetic LAN reservation %d", port)
			}
		}
		if _, exists := projection.ServingAssignments[state.Apps[appB].LAN.Raw.Port]; !exists {
			t.Fatal("serving peer lost its LAN assignment")
		}
	})

	t.Run("dormant_desired_modes_share_bytes_but_retain_distinct_authority", func(t *testing.T) {
		appID := gatewayCurrentRouteStateV2FirstApp(t, baseV2)
		running := gatewayCurrentProjectionV2Dormant(t, baseV2, appID, gatewayCurrentDesiredRunningV2)
		stopped := gatewayCurrentProjectionV2Dormant(t, baseV2, appID, gatewayCurrentDesiredStoppedV2)
		runningBody := gatewayCurrentProjectionV2ConfigBytes(t, running)
		stoppedBody := gatewayCurrentProjectionV2ConfigBytes(t, stopped)
		runningProjection, err := projectGatewayCurrentStateV2(running)
		if err != nil {
			t.Fatal(err)
		}
		stoppedProjection, err := projectGatewayCurrentStateV2(stopped)
		if err != nil {
			t.Fatal(err)
		}
		runningExpected, err := gatewayCurrentDormantExpectationsV2(running)
		if err != nil {
			t.Fatal(err)
		}
		stoppedExpected, err := gatewayCurrentDormantExpectationsV2(stopped)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(runningBody, stoppedBody) || runningProjection.StateDigest == stoppedProjection.StateDigest ||
			reflect.DeepEqual(runningProjection.RetainedApps[appID].Authority, stoppedProjection.RetainedApps[appID].Authority) ||
			!reflect.DeepEqual(runningExpected, stoppedExpected) {
			t.Fatal("dormant desired modes did not preserve identical physical expectations and distinct protected authority")
		}
	})

	t.Run("same_deny_config_keeps_changed_retained_pins_distinct", func(t *testing.T) {
		appID := gatewayCurrentRouteStateV2FirstApp(t, baseV2)
		first := gatewayCurrentProjectionV2Dormant(t, baseV2, appID, gatewayCurrentDesiredStoppedV2)
		second := cloneGatewayCurrentRouteStateV2(first)
		app := second.Apps[appID]
		app.Runtime.ActiveGeneration++
		app.Authority = gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredStoppedV2, gatewayCurrentLifecycleWithdrawV2)
		second.Apps[appID] = app
		gatewayCurrentProjectionV2Refresh(t, &second)
		firstProjection, err := projectGatewayCurrentStateV2(first)
		if err != nil {
			t.Fatal(err)
		}
		secondProjection, err := projectGatewayCurrentStateV2(second)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gatewayCurrentProjectionV2ConfigBytes(t, first), gatewayCurrentProjectionV2ConfigBytes(t, second)) ||
			firstProjection.StateDigest == secondProjection.StateDigest ||
			reflect.DeepEqual(firstProjection.RetainedApps[appID], secondProjection.RetainedApps[appID]) {
			t.Fatal("identical deny config erased changed protected runtime or authority pins")
		}
	})

	t.Run("determinism_and_defensive_copies", func(t *testing.T) {
		_, serving, appA, appB := gatewayCurrentProjectionV2TwoAppFixture(t, baseV1, baseV2)
		original := cloneGatewayCurrentRouteStateV2(serving)
		projection, err := projectGatewayCurrentStateV2(serving)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := projectGatewayCurrentStateV2(serving)
		if err != nil {
			t.Fatal(err)
		}
		retainedA := projection.RetainedApps[appA]
		retainedA.Route.Endpoints[0].ContainerID = strings.Repeat("f", 64)
		retainedA.LAN.Raw.AllocationID = uuid.NewString()
		projection.RetainedApps[appA] = retainedA
		if projection.ServingRoutes[appA].Endpoints[0].ContainerID != serving.Apps[appA].Route.Endpoints[0].ContainerID ||
			projection.ServingAssignments[serving.Apps[appA].LAN.Raw.Port].AllocationID != serving.Apps[appA].LAN.Raw.AllocationID ||
			projection.RetainedApps[appB].Route.Endpoints[0].ContainerID != serving.Apps[appB].Route.Endpoints[0].ContainerID {
			t.Fatal("retained projection aliases a serving route, assignment, or sibling app")
		}
		servingA := projection.ServingRoutes[appA]
		servingA.Endpoints[0].NetworkAlias = "mutated-serving-alias"
		projection.ServingRoutes[appA] = servingA
		assignment := projection.ServingAssignments[serving.Apps[appA].LAN.Raw.Port]
		assignment.AccessSpecDigest = strings.Repeat("f", 64)
		projection.ServingAssignments[serving.Apps[appA].LAN.Raw.Port] = assignment
		if serving.Apps[appA].Route.Endpoints[0].NetworkAlias != original.Apps[appA].Route.Endpoints[0].NetworkAlias ||
			serving.Apps[appA].LAN.Raw.AccessSpecDigest != original.Apps[appA].LAN.Raw.AccessSpecDigest {
			t.Fatal("projection mutation changed protected input")
		}
		reprojected, err := projectGatewayCurrentStateV2(serving)
		if err != nil || !reflect.DeepEqual(reprojected, fresh) {
			t.Fatalf("projection mutation changed a subsequent view: %v", err)
		}

		dormant := gatewayCurrentProjectionV2Dormant(t, serving, appA, gatewayCurrentDesiredStoppedV2)
		dormant = gatewayCurrentProjectionV2Dormant(t, dormant, appB, gatewayCurrentDesiredStoppedV2)
		reordered := cloneGatewayCurrentRouteStateV2(dormant)
		reordered.Apps = map[string]gatewayCurrentAppRouteV2{
			appB: cloneGatewayCurrentAppRouteV2(dormant.Apps[appB]),
			appA: cloneGatewayCurrentAppRouteV2(dormant.Apps[appA]),
		}
		gatewayCurrentProjectionV2Refresh(t, &reordered)
		firstBody := gatewayCurrentProjectionV2ConfigBytes(t, dormant)
		secondBody := gatewayCurrentProjectionV2ConfigBytes(t, reordered)
		firstExpected, err := gatewayCurrentDormantExpectationsV2(dormant)
		if err != nil {
			t.Fatal(err)
		}
		secondExpected, err := gatewayCurrentDormantExpectationsV2(reordered)
		if err != nil {
			t.Fatal(err)
		}
		wantOrder := []string{appA, appB}
		sort.Strings(wantOrder)
		gotOrder := []string{firstExpected[0].AppID, firstExpected[1].AppID}
		if !bytes.Equal(firstBody, secondBody) || !reflect.DeepEqual(firstExpected, secondExpected) ||
			!reflect.DeepEqual(gotOrder, wantOrder) {
			t.Fatal("map insertion order changed config bytes or dormant expectation order")
		}
		originalAllocation := firstExpected[0].LAN.Assignment.AllocationID
		firstExpected[0].LAN.Assignment.AllocationID = uuid.NewString()
		again, err := gatewayCurrentDormantExpectationsV2(dormant)
		if err != nil || again[0].LAN.Assignment.AllocationID != originalAllocation {
			t.Fatalf("dormant expectation aliases a subsequent view: %v", err)
		}
	})

	t.Run("closed_invalid_inputs_return_no_partial_output", func(t *testing.T) {
		_, valid, appA, appB := gatewayCurrentProjectionV2TwoAppFixture(t, baseV1, baseV2)
		tests := []struct {
			name         string
			storageValid bool
			mutate       func(*gatewayCurrentRouteStateV2)
		}{
			{name: "zero_version", mutate: func(value *gatewayCurrentRouteStateV2) {
				value.Version = 0
				gatewayCurrentProjectionV2RefreshDigest(t, value)
			}},
			{name: "unknown_version", mutate: func(value *gatewayCurrentRouteStateV2) {
				value.Version++
				gatewayCurrentProjectionV2RefreshDigest(t, value)
			}},
			{name: "stale_digest", mutate: func(value *gatewayCurrentRouteStateV2) { value.Revision++ }},
			{name: "invalid_traffic_authority", mutate: func(value *gatewayCurrentRouteStateV2) {
				app := value.Apps[appA]
				app.Traffic = gatewayCurrentTrafficDormantV2
				value.Apps[appA] = app
				gatewayCurrentProjectionV2RefreshDigest(t, value)
			}},
			{name: "duplicate_port", mutate: func(value *gatewayCurrentRouteStateV2) {
				app := value.Apps[appB]
				app.LAN.Raw.Port = value.Apps[appA].LAN.Raw.Port
				value.Apps[appB] = app
				gatewayCurrentProjectionV2RefreshDigest(t, value)
			}},
			{name: "duplicate_allocation", mutate: func(value *gatewayCurrentRouteStateV2) {
				app := value.Apps[appB]
				app.LAN.Raw.AllocationID = value.Apps[appA].LAN.Raw.AllocationID
				value.Apps[appB] = app
				gatewayCurrentProjectionV2RefreshDigest(t, value)
			}},
			{name: "conflicting_retained_network_owner", storageValid: true, mutate: func(value *gatewayCurrentRouteStateV2) {
				app := value.Apps[appB]
				for index := range app.Route.Endpoints {
					app.Route.Endpoints[index].NetworkName = value.Apps[appA].Route.Endpoints[0].NetworkName
				}
				value.Apps[appB] = app
				gatewayCurrentProjectionV2RefreshDigest(t, value)
			}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				value := cloneGatewayCurrentRouteStateV2(valid)
				test.mutate(&value)
				if got := validGatewayCurrentRouteStateV2(value); got != test.storageValid {
					t.Fatalf("storage validity=%v want=%v", got, test.storageValid)
				}
				projection, err := projectGatewayCurrentStateV2(value)
				if err == nil || !reflect.DeepEqual(projection, gatewayCurrentProjectionV2{}) {
					t.Fatalf("invalid input returned partial projection: %#v error=%v", projection, err)
				}
				body, err := gatewayCurrentPhysicalConfigBytesV2(value)
				if err == nil || body != nil {
					t.Fatalf("invalid input returned config bytes: %x error=%v", body, err)
				}
				expected, err := gatewayCurrentDormantExpectationsV2(value)
				if err == nil || expected != nil {
					t.Fatalf("invalid input returned dormant expectations: %#v error=%v", expected, err)
				}
			})
		}
	})

	t.Run("genuine_upgrade_renders_exact_physical_config_and_dormant_tokens", func(t *testing.T) {
		oldBefore := gatewayCurrentProjectionV1ConfigBytes(t, baseV1)
		upgrade := gatewayCurrentProjectionV2UpgradeFixture(t, fixture, baseV2)
		if _, err := projectGatewayCurrentStateV2(upgrade); err != nil {
			t.Fatalf("valid retained upgrade projection was refused: %v", err)
		}
		body, err := gatewayCurrentPhysicalConfigBytesV2(upgrade)
		wantBody, wantErr := expectedGatewayV2FinalConfig(fixture.history.Predecessor.State)
		if err != nil || wantErr != nil || !bytes.Equal(body, wantBody) {
			t.Fatalf("upgrade physical config mismatch: error=%v wantError=%v equal=%v", err, wantErr, bytes.Equal(body, wantBody))
		}
		token, err := gatewayCurrentPhysicalProbeTokenV2(upgrade)
		wantToken, wantTokenErr := gatewayV2HostChallenge(fixture.history.Predecessor.State)
		if err != nil || wantTokenErr != nil || token != wantToken {
			t.Fatalf("upgrade physical token=%q want=%q error=%v wantError=%v", token, wantToken, err, wantTokenErr)
		}
		if expected, err := gatewayCurrentDormantExpectationsV2(upgrade); err != nil || len(expected) != 0 {
			t.Fatalf("serving upgrade dormant expectations: %#v error=%v", expected, err)
		}
		oldAfter := gatewayCurrentProjectionV1ConfigBytes(t, baseV1)
		if !bytes.Equal(oldBefore, oldAfter) {
			t.Fatal("V2 upgrade refusal changed legacy V1 physical rendering")
		}

		_, mixed, appA, _ := gatewayCurrentProjectionV2TwoAppFixture(t, baseV1, baseV2)
		mixed = gatewayCurrentProjectionV2Dormant(t, mixed, appA, gatewayCurrentDesiredStoppedV2)
		dormantExpected, err := gatewayCurrentDormantExpectationsV2(mixed)
		if err != nil || len(dormantExpected) != 1 || dormantExpected[0].LAN == nil {
			t.Fatalf("dormant expectation=%#v error=%v", dormantExpected, err)
		}
		token, err = gatewayCurrentPhysicalProbeTokenV2(mixed)
		if err != nil {
			t.Fatal(err)
		}
		assignment := gatewayCurrentLANAssignmentV2(appA, *mixed.Apps[appA].LAN)
		want := gatewayCurrentDormantExpectationV2{
			AppID: appA, LocalHost: appA + ".rig.localhost", LocalAppChallenge: gatewayV2AppChallenge(token, appA),
			LAN: &gatewayCurrentDormantLANExpectationV2{
				Port: mixed.Apps[appA].LAN.Raw.Port, Assignment: assignment,
				LANAppChallenge:      gatewayV2LANAppChallenge(token, mixed.Apps[appA].LAN.Raw.Port, assignment),
				GenericPortChallenge: gatewayV2PortChallenge(token, mixed.Apps[appA].LAN.Raw.Port),
			},
		}
		if !reflect.DeepEqual(dormantExpected[0], want) {
			t.Fatalf("dormant challenge descriptor=%#v want=%#v", dormantExpected[0], want)
		}
		config := gatewayCurrentProjectionV2Config(t, gatewayCurrentProjectionV2ConfigBytes(t, mixed))
		local := config.Apps.HTTP.Servers["generated"]
		if _, found := gatewayCurrentProjectionV2ChallengeRoute(local, want.LocalHost,
			want.LocalAppChallenge); found {
			t.Fatal("removed local app challenge remained in dormant local listener")
		}
		server := config.Apps.HTTP.Servers[lanServerName(want.LAN.Port)]
		if _, found := gatewayCurrentProjectionV2ChallengeRoute(server, mixed.Profile.SelectedIPv4,
			want.LAN.GenericPortChallenge); !found {
			t.Fatal("preserved generic port challenge is absent from deny listener")
		}
		if _, found := gatewayCurrentProjectionV2ChallengeRoute(server, mixed.Profile.SelectedIPv4,
			want.LAN.LANAppChallenge); found {
			t.Fatal("removed LAN app challenge remained in dormant deny listener")
		}
	})
}

func gatewayCurrentProjectionV2TwoAppFixture(t *testing.T, baseV1 gatewayCurrentRouteState,
	baseV2 gatewayCurrentRouteStateV2,
) (gatewayCurrentRouteState, gatewayCurrentRouteStateV2, string, string) {
	t.Helper()
	appA := gatewayCurrentRouteStateV2FirstApp(t, baseV2)
	appB := "22222222-2222-4222-8222-222222222222"
	if appB == appA {
		appB = "33333333-3333-4333-8333-333333333333"
	}
	routeB := routeRecord{Slot: generatedidentity.SlotGreen, Endpoints: []generatedroute.RouteEndpoint{
		endpoint("api", "server", "projection-b-network", "api-green", 3000, '8'),
		endpoint("frontend", "static", "projection-b-network", "frontend-green", 4173, '9'),
	}}
	bindingB := gatewayCurrentProjectionV2LANBinding(t, baseV1, appA)

	v1 := cloneGatewayCurrentRouteState(baseV1)
	v1.Apps[appB] = gatewayCurrentAppRoute{Route: routeB, LAN: bindingB}
	gatewayCurrentProjectionV2RefreshV1(t, &v1)
	v2 := cloneGatewayCurrentRouteStateV2(baseV2)
	v2.SourceV1 = &gatewayCurrentRouteSourceV1{Revision: v1.Revision, Digest: v1.Digest}
	v2.Apps[appB] = gatewayCurrentAppRouteV2{
		Route: routeB,
		Runtime: gatewayCurrentRuntimePinV2{
			DeploymentID: uuid.NewString(), ReleaseID: uuid.NewString(), Slot: routeB.Slot, ActiveGeneration: 2,
		},
		Traffic: gatewayCurrentTrafficServingV2,
		Authority: gatewayCurrentAppliedAuthorityV2{
			DesiredMode: gatewayCurrentDesiredUninitializedV2,
		},
		LAN: cloneGatewayCurrentAppRoute(gatewayCurrentAppRoute{LAN: bindingB}).LAN,
	}
	gatewayCurrentProjectionV2Refresh(t, &v2)
	return v1, v2, appA, appB
}

func gatewayCurrentProjectionV2LANBinding(t *testing.T, state gatewayCurrentRouteState,
	appA string,
) *gatewayCurrentLANBinding {
	t.Helper()
	used := make(map[uint16]struct{})
	for _, app := range state.Apps {
		if app.LAN != nil {
			used[app.LAN.Raw.Port] = struct{}{}
		}
	}
	port := state.Profile.PortStart
	for {
		if _, exists := used[port]; !exists {
			break
		}
		if port == state.Profile.PortEnd {
			t.Fatal("fixture has no second LAN port")
		}
		port++
	}
	if state.Apps[appA].LAN == nil {
		t.Fatal("fixture app A has no LAN binding")
	}
	return &gatewayCurrentLANBinding{Raw: gatewayV2LANBinding{
		GrantAttemptID: uuid.NewString(), GrantRequestDigest: strings.Repeat("1", 64),
		OwnerOperationID: uuid.NewString(), AccessRevisionID: uuid.NewString(), AccessRevisionNumber: 2,
		AccessSpecDigest: strings.Repeat("2", 64), AllocationID: uuid.NewString(), Port: port,
		ProfileRevisionID: state.Profile.RevisionID, ProfileRevisionNumber: state.Profile.RevisionNumber,
		ProfileSpecDigest: state.Profile.SpecDigest, ApprovedBy: uuid.NewString(),
	}}
}

func gatewayCurrentProjectionV2Dormant(t *testing.T, value gatewayCurrentRouteStateV2, appID string,
	mode gatewayCurrentDesiredModeV2,
) gatewayCurrentRouteStateV2 {
	t.Helper()
	result := cloneGatewayCurrentRouteStateV2(value)
	app, exists := result.Apps[appID]
	if !exists {
		t.Fatalf("missing app %s", appID)
	}
	app.Traffic = gatewayCurrentTrafficDormantV2
	app.Authority = gatewayCurrentRouteStateV2AuthoredAuthority(mode, gatewayCurrentLifecycleWithdrawV2)
	result.Apps[appID] = app
	gatewayCurrentProjectionV2Refresh(t, &result)
	return result
}

func gatewayCurrentProjectionV2UpgradeFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	template gatewayCurrentRouteStateV2,
) gatewayCurrentRouteStateV2 {
	t.Helper()
	selection := fixture.history.Predecessor
	journalDigest, err := canonicalDigest(selection.Journal)
	if err != nil {
		t.Fatal(err)
	}
	lineage := appaccess.GatewayCurrentLineageRef{
		Kind:        appaccess.GatewayRebindSourceGatewayUpgrade,
		OperationID: selection.State.OperationID, ProfileRevisionID: selection.State.Profile.RevisionID,
		ProfileRevisionNumber: selection.State.Profile.RevisionNumber,
		ProfileSpecDigest:     selection.State.Profile.SpecDigest, ProtectedGeneration: selection.Generation,
		ProtectedIdentityDigest: selection.State.Identity.Digest, ProtectedJournalDigest: journalDigest,
	}
	apps := make(map[string]gatewayCurrentAppRouteV2, len(selection.State.Apps))
	for appID, source := range selection.State.Apps {
		runtime, exists := template.Apps[appID]
		if !exists {
			runtime.Runtime = gatewayCurrentRuntimePinV2{
				DeploymentID: uuid.NewString(), ReleaseID: uuid.NewString(), Slot: source.Route.Slot, ActiveGeneration: 1,
			}
		}
		cloned := cloneGatewayV2AppRoute(source)
		var lan *gatewayCurrentLANBinding
		if cloned.LAN != nil {
			lan = &gatewayCurrentLANBinding{Raw: *cloned.LAN}
		}
		apps[appID] = gatewayCurrentAppRouteV2{
			Route: cloned.Route, Runtime: runtime.Runtime, Traffic: gatewayCurrentTrafficServingV2,
			Authority: gatewayCurrentAppliedAuthorityV2{DesiredMode: gatewayCurrentDesiredUninitializedV2}, LAN: lan,
		}
	}
	identity := selection.State.Identity
	origin, err := gatewayCurrentUpgradeRouteOriginV2(selection.State, fixture.history.Source)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := appaccess.GatewayRebindTransferManifestDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	value := gatewayCurrentRouteStateV2{
		Version: gatewayCurrentRouteStateVersionV2, Revision: 1, Lineage: lineage,
		Profile:  selection.State.Profile,
		Identity: gatewayCurrentIdentity{Kind: gatewayCurrentIdentityUpgrade, Upgrade: &identity},
		Network:  selection.State.Network, TransferManifestDigest: manifestDigest,
		SourceUpgrade: origin.SourceUpgrade, Apps: apps,
	}
	gatewayCurrentProjectionV2Refresh(t, &value)
	return value
}

func gatewayCurrentProjectionV2Refresh(t *testing.T, value *gatewayCurrentRouteStateV2) {
	t.Helper()
	gatewayCurrentProjectionV2RefreshDigest(t, value)
	if !validGatewayCurrentRouteStateV2(*value) {
		t.Fatal("fixture is not a valid stable current V2 state")
	}
}

func gatewayCurrentProjectionV2RefreshDigest(t *testing.T, value *gatewayCurrentRouteStateV2) {
	t.Helper()
	var err error
	value.Digest, err = gatewayCurrentRouteStateDigestV2(*value)
	if err != nil {
		t.Fatal(err)
	}
}

func gatewayCurrentProjectionV2RefreshV1(t *testing.T, value *gatewayCurrentRouteState) {
	t.Helper()
	var err error
	value.Digest, err = gatewayCurrentRouteStateDigest(*value)
	if err != nil || !validGatewayCurrentRouteState(*value) {
		t.Fatalf("fixture is not a valid stable current V1 state: %v", err)
	}
}

func gatewayCurrentProjectionV2ConfigBytes(t *testing.T, state gatewayCurrentRouteStateV2) []byte {
	t.Helper()
	body, err := gatewayCurrentPhysicalConfigBytesV2(state)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func gatewayCurrentProjectionV1ConfigBytes(t *testing.T, state gatewayCurrentRouteState) []byte {
	t.Helper()
	body, err := gatewayCurrentPhysicalConfigBytes(state)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func gatewayCurrentProjectionV2Config(t *testing.T, body []byte) caddyConfig {
	t.Helper()
	var result caddyConfig
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func gatewayCurrentProjectionV2HostRoutes(server caddyServer, host string) []caddyRoute {
	result := make([]caddyRoute, 0)
	for _, route := range server.Routes {
		for _, match := range route.Match {
			for _, candidate := range match.Host {
				if candidate == host {
					result = append(result, route)
					break
				}
			}
		}
	}
	return result
}

func gatewayCurrentProjectionV2ProxyRoutesForHost(server caddyServer, host string) []caddyRoute {
	result := make([]caddyRoute, 0)
	for _, route := range gatewayCurrentProjectionV2HostRoutes(server, host) {
		if len(route.Handle) == 1 && route.Handle[0].Handler == "reverse_proxy" {
			result = append(result, route)
		}
	}
	return result
}

func gatewayCurrentProjectionV2HasReverseProxy(server caddyServer) bool {
	for _, route := range server.Routes {
		for _, handle := range route.Handle {
			if handle.Handler == "reverse_proxy" {
				return true
			}
		}
	}
	return false
}

func gatewayCurrentProjectionV2ChallengeRoute(server caddyServer, host, challenge string) (caddyRoute, bool) {
	wantPath := gatewayV2ChallengePathPrefix + challenge
	wantBody := gatewayV2ChallengeBodyPrefix + challenge
	for _, route := range server.Routes {
		if len(route.Match) == 1 && reflect.DeepEqual(route.Match[0].Host, []string{host}) &&
			reflect.DeepEqual(route.Match[0].Path, []string{wantPath}) && len(route.Handle) == 1 &&
			route.Handle[0].Handler == "static_response" && route.Handle[0].StatusCode == 404 &&
			route.Handle[0].Body == wantBody {
			return route, true
		}
	}
	return caddyRoute{}, false
}
