package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestInspectGatewayRebindPreparedDockerPredecessorReadsTwiceWithoutEffects(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := "running"
		if stopped {
			name = "stopped"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			ctx := context.Background()
			beforeDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
			if err != nil {
				t.Fatal(err)
			}
			beforeSource, err := fixture.manager.store.load()
			if err != nil {
				t.Fatal(err)
			}
			beforeCommands := len(fixture.runner.commands)
			reads := 0
			inspect := func(_ context.Context, source routeState, state gatewayV2RouteState,
				journal gatewayMigrationJournal,
			) (gatewayV2DockerObservation, error) {
				reads++
				if fixture.manager.mu.TryLock() {
					fixture.manager.mu.Unlock()
					t.Fatal("Docker inspection ran without Manager lock")
				}
				if !reflect.DeepEqual(source, beforeSource) ||
					!reflect.DeepEqual(state, fixture.state) || !reflect.DeepEqual(journal, fixture.journal) {
					t.Fatal("Docker inspection received a drifting input")
				}
				observation := gatewayRebindFixtureDockerObservation(t, fixture)
				if stopped {
					makeGatewayRebindPreparedStoppedObservation(&observation)
				}
				if observation.FinalHostPublicationProven ||
					!validGatewayRebindPredecessorDocker(source, state, journal, observation) {
					t.Fatal("test predecessor is not valid without the old host probe")
				}
				return observation, nil
			}
			if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
				ctx, fixture.repository, nil, inspect,
			); err != nil || reads != 2 {
				t.Fatalf("prepared Docker inspection error=%v reads=%d", err, reads)
			}
			afterDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) {
				t.Fatalf("SQLite changed: %v", err)
			}
			afterArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
			if err != nil || !sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) {
				t.Fatalf("protected history changed: %v", err)
			}
			afterSource, err := fixture.manager.store.load()
			if err != nil || !reflect.DeepEqual(beforeSource, afterSource) ||
				len(fixture.runner.commands) != beforeCommands {
				t.Fatalf("source or Docker changed: source=%t commands=%d err=%v",
					reflect.DeepEqual(beforeSource, afterSource), len(fixture.runner.commands)-beforeCommands, err)
			}
		})
	}
}

func makeGatewayRebindPreparedStoppedObservation(observation *gatewayV2DockerObservation) {
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
}

func TestInspectGatewayRebindPreparedDockerPredecessorRejectsClaimAndDockerDrift(t *testing.T) {
	t.Run("missing claim", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		reads := 0
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			reads++
			return gatewayV2DockerObservation{}, nil
		}
		if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
			context.Background(), fixture.repository, nil, inspect,
		); !IsCode(err, DiagnosticRouteUnresolved) || reads != 0 {
			t.Fatalf("missing claim error=%v reads=%d", err, reads)
		}
	})
	t.Run("tampered claim", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		if _, err := fixture.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
		if err := fixture.manager.InspectGatewayRebindPreparedDockerPredecessor(context.Background(), fixture.repository); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("tampered claim error=%v", err)
		}
	})
	t.Run("valid inter-read Docker drift", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		reads := 0
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			reads++
			observation := gatewayRebindFixtureDockerObservation(t, fixture)
			if reads == 2 {
				observation.FinalContainer.Labels["org.opencontainers.image.title"] = "changed between reads"
			}
			return observation, nil
		}
		if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
			context.Background(), fixture.repository, nil, inspect,
		); !IsCode(err, DiagnosticRouteUnresolved) || reads != 2 {
			t.Fatalf("Docker drift error=%v reads=%d", err, reads)
		}
	})
	t.Run("invalid Docker ownership", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			observation := gatewayRebindFixtureDockerObservation(t, fixture)
			observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
			return observation, nil
		}
		if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
			context.Background(), fixture.repository, nil, inspect,
		); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("wrong Docker owner error=%v", err)
		}
	})
}

func TestInspectGatewayRebindPreparedDockerPredecessorRejectsInspectorErrorsAndReleasesLock(t *testing.T) {
	for _, failOnRead := range []int{1, 2} {
		name := "first observation"
		if failOnRead == 2 {
			name = "second observation"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			reads := 0
			inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				reads++
				if reads == failOnRead {
					return gatewayV2DockerObservation{}, errors.New("injected Docker inspection failure")
				}
				return gatewayRebindFixtureDockerObservation(t, fixture), nil
			}
			err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
				context.Background(), fixture.repository, nil, inspect,
			)
			if !IsCode(err, DiagnosticRouteUnresolved) || reads != failOnRead {
				t.Fatalf("inspector error=%v reads=%d", err, reads)
			}
			if !fixture.manager.mu.TryLock() {
				t.Fatal("Manager lock remained held after Docker inspection failure")
			}
			fixture.manager.mu.Unlock()
		})
	}
}

func TestInspectGatewayRebindPreparedDockerPredecessorRejectsDriftDuringSecondDockerRead(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, gatewayRebindPredecessorFixture)
	}{
		{"SQLite claim", func(t *testing.T, fixture gatewayRebindPredecessorFixture) {
			if _, err := fixture.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
				t.Fatal(err)
			}
		}},
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
		{"source", func(t *testing.T, fixture gatewayRebindPredecessorFixture) {
			source, err := fixture.manager.store.load()
			if err != nil {
				t.Fatal(err)
			}
			source.Active = map[string]routeRecord{}
			if err := fixture.manager.store.save(source); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			reads := 0
			inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				reads++
				observation := gatewayRebindFixtureDockerObservation(t, fixture)
				if reads == 2 {
					test.mutate(t, fixture)
				}
				return observation, nil
			}
			if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
				context.Background(), fixture.repository, nil, inspect,
			); !IsCode(err, DiagnosticRouteUnresolved) || reads != 2 {
				t.Fatalf("post-Docker drift error=%v reads=%d", err, reads)
			}
		})
	}
}

func TestInspectGatewayRebindPreparedDockerPredecessorFailsOnCancellationAndRelease(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		reads := 0
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			reads++
			return gatewayRebindFixtureDockerObservation(t, fixture), nil
		}
		if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
			ctx, fixture.repository, cancel, inspect,
		); !IsCode(err, DiagnosticCancelled) || reads != 1 {
			t.Fatalf("cancellation error=%v reads=%d", err, reads)
		}
	})
	t.Run("lock release failure", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		originalAcquire := managerAcquireGatewayOSLock
		acquired, released := false, false
		managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
			acquired = true
			if fixture.manager.mu.TryLock() {
				fixture.manager.mu.Unlock()
				t.Fatal("OS lock acquired before Manager lock")
			}
			return func() error {
				released = true
				return errors.New("injected release failure")
			}, nil
		}
		t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
		reads := 0
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			reads++
			return gatewayRebindFixtureDockerObservation(t, fixture), nil
		}
		if err := fixture.manager.inspectGatewayRebindPreparedDockerPredecessor(
			context.Background(), fixture.repository, nil, inspect,
		); !IsCode(err, DiagnosticRouteUnresolved) || !acquired || !released || reads != 2 {
			t.Fatalf("release failure error=%v acquired=%t released=%t reads=%d", err, acquired, released, reads)
		}
		if !fixture.manager.mu.TryLock() {
			t.Fatal("Manager lock remained held after release failure")
		}
		fixture.manager.mu.Unlock()
	})
}
