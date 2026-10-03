package generatedingress

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func gatewayRebindDockerTestObservation(t *testing.T) (routeState, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2DockerObservation) {
	t.Helper()
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseCommitted
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	// The old LAN address is deliberately unverified during rebind admission.
	observation.FinalHostPublicationProven = false
	observation.Final404Proven = false
	observation.FinalRoutesProven = false
	return source, state, journal, observation
}

func TestGatewayRebindDockerAcceptsExactRunningPredecessorWithoutOldHostProbe(t *testing.T) {
	source, state, journal, observation := gatewayRebindDockerTestObservation(t)
	if !validGatewayRebindPredecessorDocker(source, state, journal, observation) {
		t.Fatal("exact committed predecessor rejected when old host publication is unverified")
	}
	if got := classifyGatewayV2Topology(source, state, journal, observation); got != gatewayTopologyUnknownOrDrift {
		t.Fatalf("normal serving topology incorrectly accepted absent host proof: %q", got)
	}
}

func TestGatewayRebindDockerAcceptsExactStoppedPredecessorWithoutEffectiveBind(t *testing.T) {
	source, state, journal, observation := gatewayRebindDockerTestObservation(t)
	observation.FinalContainer.Running = false
	observation.FinalConfig = nil
	observation.FinalRuntime.EffectivePortBindings = nil
	for _, attachment := range observation.FinalContainer.Networks {
		attachment.IPAddress = ""
	}
	for name, network := range observation.ApplicationNetworks {
		delete(network.Containers, observation.FinalContainer.ID)
		observation.ApplicationNetworks[name] = network
	}
	observation.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	observation.FinalRoutesProven = false
	observation.Final404Proven = false
	observation.FinalRuntime.RestartCount = 3
	if !validGatewayRebindPredecessorDocker(source, state, journal, observation) {
		t.Fatal("exact stopped predecessor after failed automatic restarts rejected")
	}
	if validGatewayV2StoppedFinalForTransfer(state, journal, observation) {
		t.Fatal("rebind-only restart tolerance leaked into transfer recovery")
	}
	observation.FinalRuntime.EffectivePortBindings = map[string][]map[string]string{
		"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": "18080"}},
	}
	if validGatewayRebindPredecessorDocker(source, state, journal, observation) {
		t.Fatal("stopped predecessor with an effective host binding accepted")
	}
}

func TestGatewayRebindDockerRejectsOwnershipAndInventoryDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*gatewayMigrationJournal, *gatewayV2DockerObservation)
	}{
		{"replaced final ID", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
		}},
		{"extra Rig label", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.FinalContainer.Labels["io.rig.unexpected"] = "drift"
		}},
		{"wrong image", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.FinalContainer.Image = "sha256:" + strings.Repeat("9", 64)
		}},
		{"replaced volume", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.ConfigVolumeIdentity.CreatedAt = "2026-10-03T00:00:00Z"
		}},
		{"extra owned container", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.OwnedContainers = append(value.OwnedContainers, "rig-rogue")
		}},
		{"extra owned network", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.OwnedNetworks = append(value.OwnedNetworks, "rig-rogue")
		}},
		{"missing application network", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			for name := range value.ApplicationNetworks {
				delete(value.ApplicationNetworks, name)
				break
			}
		}},
		{"wrong configured host binding", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			for port := range value.FinalContainer.PortBindings {
				if port != "8080/tcp" {
					value.FinalContainer.PortBindings[port][0]["HostIp"] = "0.0.0.0"
					break
				}
			}
		}},
		{"route config changed", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.FinalConfig = []byte("wrong route config")
		}},
		{"running restart count", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.FinalRuntime.RestartCount = 1
		}},
		{"unstable inventory", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.OwnedInventoriesStable = false
		}},
		{"stage reappeared", func(_ *gatewayMigrationJournal, value *gatewayV2DockerObservation) {
			value.StageContainerFound = true
		}},
		{"uncommitted journal", func(journal *gatewayMigrationJournal, _ *gatewayV2DockerObservation) {
			journal.Phase = gatewayPhaseV2Serving
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, state, journal, observation := gatewayRebindDockerTestObservation(t)
			test.mutate(&journal, &observation)
			if validGatewayRebindPredecessorDocker(source, state, journal, observation) {
				t.Fatal("drifted predecessor accepted")
			}
		})
	}
}

func gatewayRebindFixtureDockerObservation(t *testing.T, fixture gatewayRebindPredecessorFixture) gatewayV2DockerObservation {
	t.Helper()
	source, err := fixture.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	observation := gatewayV2IdentityTestObservation(t, source, fixture.state, fixture.journal, gatewayTopologyExactFinalV2)
	observation.IngressNetworkID = "sha256:" + strings.Repeat("b", 64)
	configuredIngress := observation.FinalRuntime.ConfiguredNetworks[fixture.state.Identity.IngressNetwork]
	configuredIngress.NetworkID = observation.IngressNetworkID
	observation.FinalRuntime.ConfiguredNetworks[fixture.state.Identity.IngressNetwork] = configuredIngress
	previousFinalID := observation.FinalContainer.ID
	observation.FinalContainer.ID = "sha256:" + strings.Repeat("e", 64)
	member := observation.IngressNetwork.Containers[previousFinalID]
	delete(observation.IngressNetwork.Containers, previousFinalID)
	observation.IngressNetwork.Containers[observation.FinalContainer.ID] = member
	for name, network := range observation.ApplicationNetworks {
		member := network.Containers[previousFinalID]
		delete(network.Containers, previousFinalID)
		network.Containers[observation.FinalContainer.ID] = member
		observation.ApplicationNetworks[name] = network
	}
	observation.ConfigVolumeIdentity = upgradeConfigVolumeIdentity()
	observation.DataVolumeIdentity = gatewayV1VolumeIdentity{
		Mountpoint: "C:\\ProgramData\\Docker\\volumes\\rig-generated-caddy-data-v2\\_data",
		CreatedAt:  "2026-09-29T12:00:00Z",
	}
	observation.FinalHostPublicationProven = false
	observation.Final404Proven = false
	observation.FinalRoutesProven = false
	if !validGatewayRebindPredecessorDocker(source, fixture.state, fixture.journal, observation) {
		t.Fatal("Docker fixture does not match protected predecessor")
	}
	return observation
}

func TestInspectGatewayRebindPreclaimWithDockerReadsTwiceWithoutEffects(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	beforeDatabase, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
	if err != nil {
		t.Fatal(err)
	}
	beforeArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(fixture.runner.commands)
	reads := 0
	inspect := func(_ context.Context, _ routeState, _ gatewayV2RouteState, _ gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
		reads++
		if fixture.manager.mu.TryLock() {
			fixture.manager.mu.Unlock()
			t.Fatal("Docker inspection ran without Manager lock")
		}
		return gatewayRebindFixtureDockerObservation(t, fixture), nil
	}
	if err := fixture.manager.inspectGatewayRebindPreclaimWithDocker(
		context.Background(), fixture.repository, fixture.proposal, nil, inspect,
	); err != nil || reads != 2 {
		t.Fatalf("stable Docker preclaim error=%v reads=%d", err, reads)
	}
	afterDatabase, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
	if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) {
		t.Fatalf("Docker preclaim changed SQLite: %v", err)
	}
	afterArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
	if err != nil || !sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) || len(fixture.runner.commands) != beforeCommands {
		t.Fatalf("Docker preclaim changed protected/Docker state: artifacts=%t commands=%d err=%v",
			sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts), len(fixture.runner.commands)-beforeCommands, err)
	}
}

func TestInspectGatewayRebindPreclaimWithDockerRejectsValidInterReadDrift(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	reads := 0
	inspect := func(_ context.Context, _ routeState, _ gatewayV2RouteState, _ gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
		reads++
		observation := gatewayRebindFixtureDockerObservation(t, fixture)
		if reads == 2 {
			observation.FinalContainer.Labels["org.opencontainers.image.title"] = "changed between reads"
		}
		return observation, nil
	}
	if err := fixture.manager.inspectGatewayRebindPreclaimWithDocker(
		context.Background(), fixture.repository, fixture.proposal, nil, inspect,
	); !IsCode(err, DiagnosticRouteUnresolved) || reads != 2 {
		t.Fatalf("inter-read Docker drift error=%v reads=%d", err, reads)
	}
}

func TestInspectGatewayRebindPreclaimWithDockerRejectsDriftDuringSecondObservation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, gatewayRebindPredecessorFixture)
	}{
		{"protected history replacement", func(t *testing.T, fixture gatewayRebindPredecessorFixture) {
			body, err := os.ReadFile(fixture.store.v2Path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(fixture.store.v2Path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.store.v2Path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"v1 source changed", func(t *testing.T, fixture gatewayRebindPredecessorFixture) {
			source, err := fixture.manager.store.load()
			if err != nil {
				t.Fatal(err)
			}
			source.Active = map[string]routeRecord{}
			if err := fixture.manager.store.save(source); err != nil {
				t.Fatal(err)
			}
		}},
		{"SQLite approval changed", func(t *testing.T, fixture gatewayRebindPredecessorFixture) {
			if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, gatewayRebindTestAdministrator); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
			reads := 0
			inspect := func(_ context.Context, _ routeState, _ gatewayV2RouteState, _ gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				reads++
				observation := gatewayRebindFixtureDockerObservation(t, fixture)
				if reads == 2 {
					test.mutate(t, fixture)
				}
				return observation, nil
			}
			if err := fixture.manager.inspectGatewayRebindPreclaimWithDocker(
				context.Background(), fixture.repository, fixture.proposal, nil, inspect,
			); !IsCode(err, DiagnosticRouteUnresolved) || reads != 2 {
				t.Fatalf("drift during second Docker observation error=%v reads=%d", err, reads)
			}
		})
	}
}
