//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedidentity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedDomainColdRetainedV2PresenceProof(t *testing.T) {
	_, state, journal, observation := generatedDomainColdRetainedV2TestObservation(t)
	objects := generatedDomainColdRetainedV2TestObjects(state, journal, observation)

	t.Run("complete_owned_envelope_without_retained_v1_objects", func(t *testing.T) {
		for _, object := range objects {
			if object.name == caddyContainerName || object.name == caddyNetworkName ||
				object.name == caddyVolumeName {
				t.Fatal("V2-only presence control contains a retained V1 object")
			}
		}
		got, presence, err := generatedDomainColdRetainedV2Objects(
			generatedDomainDockerStartupSnapshot{objects: objects}, state, journal)
		projection := generatedDomainRetainedV2Projection{state: state, journal: journal}
		if err != nil || presence != generatedDomainColdRetainedV2Present || len(got) != 4 ||
			!generatedDomainColdRetainedV2CensusMatches(got, projection, observation) {
			t.Fatalf("complete retained V2 envelope presence=%d objects=%d error=%v", presence, len(got), err)
		}
	})

	t.Run("complete_owned_absence", func(t *testing.T) {
		got, presence, err := generatedDomainColdRetainedV2Objects(generatedDomainDockerStartupSnapshot{}, state, journal)
		if err != nil || presence != generatedDomainColdRetainedV2Absent || len(got) != 0 {
			t.Fatalf("absent retained V2 envelope presence=%d objects=%d error=%v", presence, len(got), err)
		}
	})

	t.Run("partial_or_extra_owned_refused", func(t *testing.T) {
		partial := append([]generatedDomainDockerObject(nil), objects[:3]...)
		if _, _, err := generatedDomainColdRetainedV2Objects(
			generatedDomainDockerStartupSnapshot{objects: partial}, state, journal); !errors.Is(err, errGeneratedDomainConstructionRefused) {
			t.Fatalf("partial retained V2 envelope error=%v", err)
		}
		stage := generatedDomainDockerObject{kind: "container", id: strings.Repeat("9", 64),
			name: state.Identity.StageContainer,
			labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel,
				gatewayV2StageContainerRole, true)}
		if _, _, err := generatedDomainColdRetainedV2Objects(
			generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{stage}}, state, journal); !errors.Is(err, errGeneratedDomainConstructionRefused) {
			t.Fatalf("stage-only retained V2 envelope error=%v", err)
		}
		foreign := objects[0]
		foreign.id = strings.Repeat("8", 64)
		foreign.name += "-foreign"
		if _, _, err := generatedDomainColdRetainedV2Objects(
			generatedDomainDockerStartupSnapshot{objects: append(objects, foreign)}, state, journal); !errors.Is(err, errGeneratedDomainConstructionRefused) {
			t.Fatalf("extra owned retained V2 envelope error=%v", err)
		}
	})

	t.Run("passive_reader_adapter_contract", func(t *testing.T) {
		generatedDomainColdRetainedV2PassiveReaderAdapterContract(t)
	})

	t.Run("current_application_network_attestation", func(t *testing.T) {
		generatedDomainColdRetainedV2CurrentApplicationNetworkAttestation(t)
	})

	t.Run("exact_current_attestation_command_delta", func(t *testing.T) {
		current := generatedDomainCurrentAttestationFixtureForTest(t)
		restart, admin, challenge, valid := generatedDomainRetainedV2CurrentAttestationCommands(
			current.stateV1, current.container.ID,
		)
		retainedV1ID, retainedV2ID := strings.Repeat("a", 64), strings.Repeat("b", 64)
		retainedV1Restart, retainedV1Valid := generatedDomainRetainedV2RestartConfigCommand(
			retainedV1ID, gatewayV2ActiveConfigFile)
		retainedV2Restart, retainedV2Valid := generatedDomainRetainedV2RestartConfigCommand(
			retainedV2ID, gatewayV2ActiveConfigFile)
		accepted := make([][]string, 0, generatedDomainRetainedV2ExpectedV1RestartReads+
			generatedDomainRetainedV2ExpectedV2RestartReads+3)
		for range generatedDomainRetainedV2ExpectedV1RestartReads {
			accepted = append(accepted, retainedV1Restart)
		}
		for range generatedDomainRetainedV2ExpectedV2RestartReads {
			accepted = append(accepted, retainedV2Restart)
		}
		accepted = append(accepted, restart, admin, challenge)
		if !valid || !retainedV1Valid || !retainedV2Valid ||
			!generatedDomainRetainedV2ExactCurrentAttestationDelta(
				current.stateV1, current.container.ID, retainedV1ID, retainedV2ID,
				gatewayV2ActiveConfigFile, accepted,
			) {
			t.Fatal("exact current attestation command delta was refused")
		}
		duplicateAdmin := append(append([][]string(nil), accepted...), admin)
		crossedOrder := append([][]string(nil), accepted...)
		crossedOrder[len(crossedOrder)-2], crossedOrder[len(crossedOrder)-1] =
			crossedOrder[len(crossedOrder)-1], crossedOrder[len(crossedOrder)-2]
		unknownExec := append(append([][]string(nil), accepted...),
			[]string{"container", "exec", normalizeID(current.container.ID), "curl", "http://127.0.0.1/status"})
		unknownArchive := append(append([][]string(nil), accepted...),
			[]string{"container", "cp", retainedV1ID + ":/config/crossed.json", "-"})
		overboundRetainedV2 := append(append([][]string(nil), accepted...), retainedV2Restart)
		for name, commands := range map[string][][]string{
			"duplicate_admin":       duplicateAdmin,
			"crossed_order":         crossedOrder,
			"unknown_exec":          unknownExec,
			"unknown_archive":       unknownArchive,
			"overbound_retained_v2": overboundRetainedV2,
		} {
			t.Run(name, func(t *testing.T) {
				if generatedDomainRetainedV2ExactCurrentAttestationDelta(
					current.stateV1, current.container.ID, retainedV1ID, retainedV2ID,
					gatewayV2ActiveConfigFile, commands,
				) {
					t.Fatal("invalid current attestation command delta was accepted")
				}
			})
		}
	})
}

type generatedDomainCurrentNetworkControl struct {
	state        gatewayCurrentRouteState
	final        gatewayRebindSuccessorIdentity
	plan         gatewayV2NetworkPlan
	container    caddyInspection
	runtime      gatewayContainerRuntime
	applications []gatewayRebindHandoverApplicationNetwork
	owners       map[string]string
	snapshot     generatedDomainDockerStartupSnapshot
	runner       *generatedDomainGatewayAttestationRunner
}

func generatedDomainColdRetainedV2CurrentApplicationNetworkAttestation(t *testing.T) {
	t.Helper()
	for _, changed := range []bool{false, true} {
		name := "selected_roster"
		if changed {
			name = "selected_roster_changed_after_terminal"
		}
		t.Run(name, func(t *testing.T) {
			control := generatedDomainColdRetainedV2CurrentNetworkControl(t, changed)
			reader := generatedDomainStartupReader{runner: control.runner, options: generatedDomainOptions{
				DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Minute,
				OutputLimit: defaultOutputLimit,
			}}
			got, err := reader.readCurrentApplicationNetworks(context.Background(), time.Now().Add(time.Minute),
				control.snapshot, control.container, control.owners)
			if err != nil || !reflect.DeepEqual(got, control.applications) ||
				!generatedDomainCurrentStartupNetworksMatch(control.final, control.plan,
					control.container, control.runtime, control.runtime.ConfiguredNetworks[control.final.IngressNetwork].NetworkID, got) {
				t.Fatalf("current application networks changed=%t count=%d error=%v", changed, len(got), err)
			}
		})
	}

	t.Run("recovered_physical_binding_preserves_inputs", func(t *testing.T) {
		control := generatedDomainColdRetainedV2CurrentNetworkControl(t, false)
		observation := gatewayV2DockerObservation{
			ApplicationNetworks:   make(map[string]caddyNetworkInspection, len(control.applications)),
			ApplicationNetworkIDs: make(map[string]string, len(control.applications)),
		}
		current := generatedDomainCurrentAttestationFixture{stateV1: cloneGatewayCurrentRouteState(control.state),
			container: cloneGeneratedDomainColdRetainedGatewayContainer(control.container),
			runtime:   cloneGeneratedDomainColdRetainedGatewayRuntime(control.runtime)}
		for _, application := range control.applications {
			network, found := control.runner.networks[application.ID]
			if !found {
				t.Fatalf("recovered physical binding network %q is absent", application.Name)
			}
			observation.ApplicationNetworkIDs[application.Name] = application.ID
			observation.ApplicationNetworks[application.Name] = caddyNetworkInspection{
				Name: network.Name, Driver: network.Driver, Scope: network.Scope, Internal: network.Internal,
				Options: cloneGeneratedDomainColdRetainedGatewayStringMap(network.Options), IPAM: network.IPAM,
				Labels:     cloneGeneratedDomainColdRetainedGatewayStringMap(network.Labels),
				Containers: cloneGatewayRebindMultiNetworkMembers(network.Containers),
			}
			delete(current.container.Networks, application.Name)
			delete(current.runtime.ConfiguredNetworks, application.Name)
		}
		originalContainer := cloneGeneratedDomainColdRetainedGatewayContainer(control.container)
		originalRuntime := cloneGeneratedDomainColdRetainedGatewayRuntime(control.runtime)
		if !generatedDomainRetainedV2BindCurrentPhysicalNetworks(&current, control.container,
			control.runtime, observation) ||
			!reflect.DeepEqual(control.container, originalContainer) ||
			!reflect.DeepEqual(control.runtime, originalRuntime) ||
			!reflect.DeepEqual(current.container.Networks, control.container.Networks) ||
			!reflect.DeepEqual(current.runtime.ConfiguredNetworks, control.runtime.ConfiguredNetworks) {
			t.Fatal("recovered physical network binding changed its authenticated inputs or lost the live roster")
		}
	})

	t.Run("false_path_preserves_ingress_singleton", func(t *testing.T) {
		control := generatedDomainColdRetainedV2CurrentNetworkControl(t, false)
		for name := range control.container.Networks {
			if name != control.final.IngressNetwork {
				delete(control.container.Networks, name)
			}
		}
		for name := range control.runtime.ConfiguredNetworks {
			if name != control.final.IngressNetwork {
				delete(control.runtime.ConfiguredNetworks, name)
			}
		}
		ingressID := control.runtime.ConfiguredNetworks[control.final.IngressNetwork].NetworkID
		if !generatedDomainCurrentStartupNetworksMatch(control.final, control.plan,
			control.container, control.runtime, ingressID, nil) {
			t.Fatal("gateway-only current singleton was refused")
		}
		control.runtime.ConfiguredNetworks["unexpected-application"] = gatewayV2ConfiguredNetwork{
			NetworkID: strings.Repeat("9", 64), EndpointID: strings.Repeat("8", 64), IPAddress: "10.88.0.9"}
		control.container.Networks["unexpected-application"] = &networkAttachment{IPAddress: "10.88.0.9"}
		if generatedDomainCurrentStartupNetworksMatch(control.final, control.plan,
			control.container, control.runtime, ingressID, nil) {
			t.Fatal("gateway-only current accepted an application network")
		}
	})

	t.Run("crossed_application_network_owner_refused", func(t *testing.T) {
		control := generatedDomainColdRetainedV2CurrentNetworkControl(t, false)
		if len(control.applications) == 0 {
			t.Fatal("current application network control has no applications")
		}
		id := control.applications[0].ID
		network := control.runner.networks[id]
		network.Labels = cloneStringMap(network.Labels)
		network.Labels["io.rig.application"] = "11111111-1111-4111-8111-111111111111"
		control.runner.networks[id] = network
		reader := generatedDomainStartupReader{runner: control.runner, options: generatedDomainOptions{
			DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Minute,
			OutputLimit: defaultOutputLimit,
		}}
		if _, err := reader.readCurrentApplicationNetworks(context.Background(), time.Now().Add(time.Minute),
			control.snapshot, control.container, control.owners); err == nil {
			t.Fatal("crossed current application network owner was accepted")
		}
	})

	t.Run("missing_extra_crossed_or_wrong_attachment_refused", func(t *testing.T) {
		control := generatedDomainColdRetainedV2CurrentNetworkControl(t, false)
		if len(control.applications) == 0 {
			t.Fatal("current application network control has no applications")
		}
		name := control.applications[0].Name
		ingressID := control.runtime.ConfiguredNetworks[control.final.IngressNetwork].NetworkID
		for _, test := range []struct {
			name   string
			mutate func(*generatedDomainCurrentNetworkControl)
		}{
			{name: "missing", mutate: func(value *generatedDomainCurrentNetworkControl) {
				delete(value.runtime.ConfiguredNetworks, name)
			}},
			{name: "extra", mutate: func(value *generatedDomainCurrentNetworkControl) {
				value.runtime.ConfiguredNetworks["unexpected-application"] = gatewayV2ConfiguredNetwork{
					NetworkID: strings.Repeat("9", 64), EndpointID: strings.Repeat("8", 64), IPAddress: "10.88.0.9"}
				value.container.Networks["unexpected-application"] = &networkAttachment{IPAddress: "10.88.0.9"}
			}},
			{name: "crossed_id", mutate: func(value *generatedDomainCurrentNetworkControl) {
				configured := value.runtime.ConfiguredNetworks[name]
				configured.NetworkID = strings.Repeat("7", 64)
				value.runtime.ConfiguredNetworks[name] = configured
			}},
			{name: "attachment_ip", mutate: func(value *generatedDomainCurrentNetworkControl) {
				value.container.Networks[name].IPAddress = "10.88.0.254"
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				copy := generatedDomainColdRetainedV2CloneCurrentNetworkControl(control)
				test.mutate(&copy)
				if generatedDomainCurrentStartupNetworksMatch(copy.final, copy.plan, copy.container,
					copy.runtime, ingressID, copy.applications) {
					t.Fatalf("current application network %s was accepted", test.name)
				}
			})
		}
	})
}

func generatedDomainColdRetainedV2CurrentNetworkControl(t *testing.T,
	changeRoster bool,
) generatedDomainCurrentNetworkControl {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
	originalTerminal := target.Terminal
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	if changeRoster {
		changed := false
		for appID, app := range state.Apps {
			if len(app.Route.Endpoints) == 0 {
				continue
			}
			app.Route.Endpoints[0].NetworkName += "-current"
			state.Apps[appID] = app
			changed = true
			break
		}
		if !changed {
			t.Fatal("current roster change control has no endpoint")
		}
		state.Revision++
		state.Digest = ""
		var err error
		state.Digest, err = gatewayCurrentRouteStateDigest(state)
		if err != nil || !validGatewayCurrentRouteState(state) {
			t.Fatalf("changed current roster is invalid: %v", err)
		}
	}
	target.State = state
	applications := gatewayCurrentPhysicalRuntimeTestApplicationNetworks(t, state)
	container, runtime := gatewayCurrentPhysicalRuntimeTestContainer(target, 19877, true)
	gatewayCurrentPhysicalRuntimeInstallNetworks(target, &container, &runtime, applications, true)
	if !reflect.DeepEqual(target.Terminal, originalTerminal) {
		t.Fatal("selected current roster change modified the immutable terminal")
	}
	owners, valid := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(state))
	if !valid || len(owners) != len(applications) {
		t.Fatal("selected current application owners are invalid")
	}
	byName := make(map[string]gatewayRebindHandoverApplicationNetwork, len(applications))
	for _, application := range applications {
		byName[application.Name] = application
	}
	networks := make(map[string]gatewayNetworkIdentityInspection, len(owners))
	objects := make([]generatedDomainDockerObject, 0, len(owners))
	for name, owner := range owners {
		application, found := byName[name]
		configured := runtime.ConfiguredNetworks[name]
		if !found || configured.IPAddress == "" {
			t.Fatalf("current application network %q has no runtime binding", name)
		}
		networks[application.ID] = gatewayNetworkIdentityInspection{ID: application.ID, Name: name,
			Driver: "bridge", Scope: "local", Labels: map[string]string{
				"io.rig.managed":     generatedidentity.NetworkOwnershipLabelValue,
				"io.rig.application": owner,
			}, Containers: map[string]caddyNetworkContainerInspection{container.ID: {
				Name: strings.TrimPrefix(container.Name, "/"), IPv4Address: configured.IPAddress + "/24",
			}}}
		objects = append(objects, generatedDomainDockerObject{kind: "network", id: application.ID, name: name})
	}
	return generatedDomainCurrentNetworkControl{state: state, final: *target.Identity.Rebind, plan: state.Network,
		container: container, runtime: runtime, applications: applications, owners: owners,
		snapshot: generatedDomainDockerStartupSnapshot{objects: objects},
		runner:   &generatedDomainGatewayAttestationRunner{networks: networks},
	}
}

func generatedDomainColdRetainedV2CloneCurrentNetworkControl(
	value generatedDomainCurrentNetworkControl,
) generatedDomainCurrentNetworkControl {
	result := value
	result.container = cloneGatewayCurrentPhysicalTestContainer(value.container)
	result.runtime = cloneGatewayCurrentPhysicalTestRuntime(value.runtime)
	result.applications = append([]gatewayRebindHandoverApplicationNetwork(nil), value.applications...)
	return result
}

func TestGeneratedDomainColdRetainedV2IntegrityAndLifetime(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedDomainRetainedV1RebindFixture(t)
		expected, _ := generatedDomainRetainedV1RebindExpectation(t, fixture, nil)
		projection, status := generatedDomainColdRetainedV2SelectedProjection(expected)
		if status != generatedDomainColdRetainedV2SelectionSupported ||
			!generatedDomainRetainedV2ProjectionValid(&projection) {
			t.Fatalf("authenticated retained V2 projection status=%d valid=%t", status,
				generatedDomainRetainedV2ProjectionValid(&projection))
		}

		t.Run("crossed_authority_refused_even_when_absent", func(t *testing.T) {
			malformed := expected
			copy := cloneGeneratedDomainRetainedV2Projection(*expected.retainedV2)
			copy.current.Digest = strings.Repeat("f", 64)
			malformed.retainedV2 = &copy
			if _, status := generatedDomainColdRetainedV2SelectedProjection(malformed); status != generatedDomainColdRetainedV2SelectionInconsistent {
				t.Fatalf("crossed retained V2 authority status=%d", status)
			}
			if _, err := attestGeneratedDomainColdRetainedV2(context.Background(), &Manager{},
				generatedDomainDockerStartupSnapshot{}, malformed); !errors.Is(err, errGeneratedDomainConstructionRefused) {
				t.Fatalf("crossed absent retained V2 authority error=%v", err)
			}
		})

		t.Run("projection_and_proof_clones_do_not_alias", func(t *testing.T) {
			original := cloneGeneratedDomainRetainedV2Projection(projection)
			cloned := cloneGeneratedDomainRetainedV2Projection(projection)
			generatedDomainRetainedV1MutateExistingRoute(t, projection.source.Active, "-source-mutated")
			generatedDomainRetainedV1MutateExistingV2App(t, projection.state.Apps, "-state-mutated")
			generatedDomainRetainedV1MutateExistingCurrentApp(t, projection.current.Apps, "-current-mutated")
			if !reflect.DeepEqual(cloned, original) {
				t.Fatal("retained V2 projection clone aliases source")
			}
			_, state, journal, observation := generatedDomainColdRetainedV2TestObservation(t)
			proof := generatedDomainColdRetainedV2Proof{presence: generatedDomainColdRetainedV2Present,
				authority: &cloned, objects: generatedDomainColdRetainedV2TestObjects(state, journal, observation),
				observed: projectGeneratedDomainColdRetainedV2Observation(observation), digest: strings.Repeat("a", 64)}
			proofClone := cloneGeneratedDomainColdRetainedV2Proof(proof)
			proof.observed.ingressNetwork.Labels["io.rig.mutated"] = "source"
			proofClone.observed.finalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork] = gatewayV2ConfiguredNetwork{}
			if proofClone.observed.ingressNetwork.Labels["io.rig.mutated"] != "" ||
				reflect.DeepEqual(proof.observed.finalRuntime, proofClone.observed.finalRuntime) {
				t.Fatal("retained V2 proof and clone alias nested maps")
			}
		})

		t.Run("fresh_drift_changes_equality_and_digest", func(t *testing.T) {
			_, state, journal, observation := generatedDomainColdRetainedV2TestObservation(t)
			if !sameGeneratedDomainColdRetainedV2Observation(observation, observation, state.Identity) {
				t.Fatal("identical retained V2 observations differ")
			}
			drift := observation
			drift.FinalRuntime = cloneGeneratedDomainColdRetainedGatewayRuntime(observation.FinalRuntime)
			drift.FinalRuntime.RestartCount++
			if sameGeneratedDomainColdRetainedV2Observation(observation, drift, state.Identity) {
				t.Fatal("retained V2 runtime drift was accepted")
			}
			proof := generatedDomainColdRetainedV2Proof{presence: generatedDomainColdRetainedV2Present,
				objects:  generatedDomainColdRetainedV2TestObjects(state, journal, observation),
				observed: projectGeneratedDomainColdRetainedV2Observation(observation)}
			first, err := generatedDomainColdRetainedV2Digest(proof)
			proof.observed.configVolumeIdentity.CreatedAt += "-drift"
			second, secondErr := generatedDomainColdRetainedV2Digest(proof)
			if err != nil || secondErr != nil || first == second {
				t.Fatalf("retained V2 digest drift first=%q second=%q errors=%v/%v", first, second, err, secondErr)
			}
		})

		t.Run("validated_mount_order_is_canonical", func(t *testing.T) {
			_, state, journal, observation := generatedDomainColdRetainedV2TestObservation(t)
			if len(observation.FinalContainer.Mounts) != 2 {
				t.Fatalf("retained V2 mount control count=%d", len(observation.FinalContainer.Mounts))
			}
			reordered := observation
			reordered.FinalContainer = cloneGeneratedDomainColdRetainedGatewayContainer(observation.FinalContainer)
			reordered.FinalContainer.Mounts[0], reordered.FinalContainer.Mounts[1] =
				reordered.FinalContainer.Mounts[1], reordered.FinalContainer.Mounts[0]
			if !sameGeneratedDomainColdRetainedV2Observation(observation, reordered, state.Identity) {
				t.Fatal("valid retained V2 mount reorder changed the observation")
			}
			left := generatedDomainColdRetainedV2Proof{presence: generatedDomainColdRetainedV2Present,
				objects:  generatedDomainColdRetainedV2TestObjects(state, journal, observation),
				observed: projectGeneratedDomainColdRetainedV2Observation(observation)}
			right := left
			right.observed = projectGeneratedDomainColdRetainedV2Observation(reordered)
			leftDigest, leftErr := generatedDomainColdRetainedV2Digest(left)
			rightDigest, rightErr := generatedDomainColdRetainedV2Digest(right)
			left.digest, right.digest = leftDigest, rightDigest
			if leftErr != nil || rightErr != nil || leftDigest != rightDigest || !reflect.DeepEqual(left, right) {
				t.Fatalf("valid retained V2 mount reorder changed stored proof: errors=%v/%v digest_equal=%t proof_equal=%t",
					leftErr, rightErr, leftDigest == rightDigest, reflect.DeepEqual(left, right))
			}
			invalid := observation
			invalid.FinalContainer = cloneGeneratedDomainColdRetainedGatewayContainer(observation.FinalContainer)
			invalid.FinalContainer.Mounts[0].Destination += "-invalid"
			if sameGeneratedDomainColdRetainedV2Observation(observation, invalid, state.Identity) {
				t.Fatal("invalid retained V2 mount projection was accepted")
			}
		})
	})
}

func generatedDomainColdRetainedV2TestObservation(t *testing.T) (routeState, gatewayV2RouteState,
	gatewayMigrationJournal, gatewayV2DockerObservation,
) {
	t.Helper()
	source, state, journal, observation := gatewayRebindDockerTestObservation(t)
	observation = generatedDomainColdRetainedV2ContractObservation(state, observation)
	makeGatewayRebindPreparedStoppedObservation(&observation)
	if !validGatewayV2StoppedFinal(state, journal, observation, true) {
		t.Fatal("retained V2 unit observation is not an exact stopped final")
	}
	return source, state, journal, observation
}

func generatedDomainColdRetainedV2TestObjects(state gatewayV2RouteState, journal gatewayMigrationJournal,
	observation gatewayV2DockerObservation,
) []generatedDomainDockerObject {
	return []generatedDomainDockerObject{
		{kind: "container", id: normalizeID(observation.FinalContainer.ID), name: state.Identity.FinalContainer,
			labels: cloneGeneratedDomainColdRetainedGatewayStringMap(observation.FinalContainer.Labels)},
		{kind: "network", id: normalizeID(observation.IngressNetworkID), name: state.Identity.IngressNetwork,
			labels: cloneGeneratedDomainColdRetainedGatewayStringMap(observation.IngressNetwork.Labels),
			data: generatedDomainDockerObjectData{driver: observation.IngressNetwork.Driver,
				scope:   observation.IngressNetwork.Scope,
				options: cloneGeneratedDomainColdRetainedGatewayStringMap(observation.IngressNetwork.Options)}},
		{kind: "volume", name: state.Identity.ConfigVolume,
			labels: cloneGeneratedDomainColdRetainedGatewayStringMap(observation.ConfigVolume.Labels),
			data: generatedDomainDockerObjectData{driver: observation.ConfigVolume.Driver,
				scope:      observation.ConfigVolume.Scope,
				options:    cloneGeneratedDomainColdRetainedGatewayStringMap(observation.ConfigVolume.Options),
				mountpoint: observation.ConfigVolumeIdentity.Mountpoint,
				createdAt:  observation.ConfigVolumeIdentity.CreatedAt}},
		{kind: "volume", name: state.Identity.DataVolume,
			labels: cloneGeneratedDomainColdRetainedGatewayStringMap(observation.DataVolume.Labels),
			data: generatedDomainDockerObjectData{driver: observation.DataVolume.Driver,
				scope:      observation.DataVolume.Scope,
				options:    cloneGeneratedDomainColdRetainedGatewayStringMap(observation.DataVolume.Options),
				mountpoint: observation.DataVolumeIdentity.Mountpoint,
				createdAt:  observation.DataVolumeIdentity.CreatedAt}},
	}
}

func generatedDomainColdRetainedV2PassiveReaderAdapterContract(t *testing.T) {
	t.Helper()
	source, state, journal, observation := gatewayRebindDockerTestObservation(t)
	observation = generatedDomainColdRetainedV2ContractObservation(state, observation)
	observation, valid := generatedDomainRetainedV2CanonicalTransport(observation)
	if !valid {
		t.Fatal("retained V2 passive-reader synthetic transport is not bijective")
	}
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	generatedDomainRetainedV2ConfigureRunner(t, gateway, state, observation)
	startupReader := generatedDomainStartupReader{runner: gateway, options: generatedDomainOptions{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Minute,
		OutputLimit: defaultOutputLimit,
	}}
	owners, ownersValid := gatewayV2ApplicationNetworkOwners(state)
	applicationCensus := make([]generatedDomainDockerObject, 0, len(owners))
	for name, owner := range owners {
		network, found := observation.ApplicationNetworks[name]
		if !found {
			t.Fatalf("retained V2 startup application network %q is absent", name)
		}
		applicationCensus = append(applicationCensus, generatedDomainDockerObject{
			kind: "network", id: generatedDomainA0NetworkID(owner), name: name,
			labels: cloneGeneratedDomainColdRetainedGatewayStringMap(network.Labels),
			data: generatedDomainDockerObjectData{driver: network.Driver, scope: network.Scope,
				options: cloneGeneratedDomainColdRetainedGatewayStringMap(network.Options)},
		})
	}
	if !ownersValid || len(applicationCensus) != len(observation.ApplicationNetworks) {
		t.Fatal("retained V2 startup application network census is incomplete")
	}
	applicationCensus = generatedDomainRetainedV2BindApplicationObjects(t, applicationCensus, observation)
	for _, object := range applicationCensus {
		expected, found := observation.ApplicationNetworks[object.name]
		actual, actualID, err := startupReader.readDetailedNetwork(
			context.Background(), time.Now().Add(time.Minute), object.id)
		if !found || err != nil || actualID != object.id || actual.Name != object.name ||
			!reflect.DeepEqual(actual, expected) {
			t.Fatalf("retained V2 startup application network projection %q id_match=%t error=%v",
				object.name, actualID == object.id, err)
		}
	}
	gateway.restarts[observation.V1Container.ID] = generatedDomainRestartFixture{
		name: gatewayV2ActiveConfigFile, body: append([]byte(nil), observation.V1RestartConfig...)}
	gateway.lives = map[string][]byte{
		observation.V1Container.ID:    append([]byte(nil), observation.V1Config...),
		observation.FinalContainer.ID: append([]byte(nil), observation.FinalConfig...),
	}
	objects := generatedDomainColdRetainedV2TestObjects(state, journal, observation)
	construction := &generatedDomainConstructionRunner{delegate: &generatedDomainRecoveryCensusRunner{
		daemonID: "generated-domain-retained-v2-reader-contract", objects: objects,
		imageID: observation.Image.ID, gateway: gateway,
	}}
	endpoints, valid := generatedDomainColdRetainedV2ContractEndpoints(state, observation)
	if !valid {
		t.Fatal("retained V2 passive-reader endpoint contract is invalid")
	}
	runner := &generatedDomainRetainedV2InspectionRunner{
		delegate: construction, objects: cloneGeneratedDomainRetainedV2Objects(objects),
		state: state, observation: observation, endpoints: endpoints,
	}
	manager := &Manager{runner: runner, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(),
		CommandTimeout: time.Minute, OutputLimit: defaultOutputLimit,
	}}
	observed, err := manager.inspectGatewayV2DockerWithStageConfig(context.Background(), source,
		state, journal, true, gatewayV2DockerProbePassiveRebind)
	observationValid := validGatewayRebindPredecessorDocker(source, state, journal, observed)
	if err != nil || !observationValid {
		commands := construction.snapshot()
		lastFamily := "none"
		if len(commands) > 0 {
			lastFamily = generatedDomainColdRetainedV2CommandFamily(commands[len(commands)-1])
		}
		failureCategory := runner.lastFailure
		if failureCategory == "" {
			failureCategory = "none"
		}
		t.Fatalf("retained V2 passive-reader contract error=%v observation_valid=%t runner_failure=%s last_command_family=%s command_count=%d",
			err, observationValid,
			failureCategory, lastFamily, len(commands))
	}
	commands := construction.snapshot()
	generatedDomainColdRetainedV2AssertPassiveCommandTable(t, commands, state, observation, endpoints)
	unknown := strings.Repeat("f", 64)
	if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{
		"container", "inspect", "--format", endpointInspectFormat, unknown,
	}}); err == nil {
		t.Fatal("retained V2 passive-reader adapter accepted an unknown endpoint identity")
	}
}

func generatedDomainColdRetainedV2CommandFamily(args []string) string {
	switch {
	case len(args) == 5 && args[0] == "image" && args[1] == "inspect":
		return "image_inspect"
	case len(args) == 5 && args[0] == "container" && args[1] == "inspect" &&
		args[3] == gatewayContainerInspectFormat:
		return "container_detail"
	case len(args) == 5 && args[0] == "container" && args[1] == "inspect" &&
		args[3] == endpointInspectFormat:
		return "endpoint_identity"
	case len(args) == 5 && args[0] == "volume" && args[1] == "inspect":
		return "volume_detail"
	case len(args) == 5 && args[0] == "network" && args[1] == "inspect":
		return "network_detail"
	case len(args) >= 2 && args[0] == "container" && args[1] == "ls":
		return "owned_containers"
	case len(args) >= 2 && args[0] == "volume" && args[1] == "ls":
		return "owned_volumes"
	case len(args) >= 2 && args[0] == "network" && args[1] == "ls":
		return "owned_networks"
	case len(args) == 4 && args[0] == "container" && args[1] == "cp":
		return "restart_config"
	case len(args) >= 3 && args[0] == "container" && args[1] == "exec":
		return "admin_config"
	default:
		return "unknown"
	}
}

func generatedDomainColdRetainedV2ContractEndpoints(state gatewayV2RouteState,
	observation gatewayV2DockerObservation,
) (map[string]endpointInspection, bool) {
	result := make(map[string]endpointInspection)
	finalID := normalizeID(observation.FinalContainer.ID)
	if finalID == "" {
		return nil, false
	}
	result[finalID] = endpointInspection{ID: observation.FinalContainer.ID,
		Labels:  cloneGeneratedDomainColdRetainedGatewayStringMap(observation.FinalContainer.Labels),
		Running: observation.FinalContainer.Running, Health: "healthy",
		Networks: make(map[string]*networkAttachment)}
	for name, network := range observation.ApplicationNetworks {
		for memberID, member := range network.Containers {
			if normalizeID(memberID) != finalID {
				continue
			}
			prefix, err := netip.ParsePrefix(member.IPv4Address)
			if err != nil || !prefix.Addr().Is4() {
				return nil, false
			}
			result[finalID].Networks[name] = &networkAttachment{IPAddress: prefix.Addr().String()}
		}
	}
	for appID, route := range gatewayV2RouteRecords(state) {
		for _, endpoint := range route.Endpoints {
			network, found := observation.ApplicationNetworks[endpoint.NetworkName]
			if !found {
				return nil, false
			}
			var member caddyNetworkContainerInspection
			memberFound := false
			for memberID, candidate := range network.Containers {
				if normalizeID(memberID) == normalizeID(endpoint.ContainerID) {
					member, memberFound = candidate, true
				}
			}
			prefix, err := netip.ParsePrefix(member.IPv4Address)
			if !memberFound || err != nil || !prefix.Addr().Is4() {
				return nil, false
			}
			id := normalizeID(endpoint.ContainerID)
			inspection, exists := result[id]
			if !exists {
				inspection = endpointInspection{ID: endpoint.ContainerID, Running: true, Health: "healthy",
					Labels: map[string]string{
						"io.rig.managed": "generated-runtime", "io.rig.application": appID,
						"io.rig.component": endpoint.Component, "io.rig.role": endpoint.Role,
						"io.rig.slot": string(route.Slot),
					}, Networks: make(map[string]*networkAttachment)}
			}
			if _, duplicate := inspection.Networks[endpoint.NetworkName]; duplicate {
				return nil, false
			}
			inspection.Networks[endpoint.NetworkName] = &networkAttachment{
				Aliases: []string{endpoint.NetworkAlias}, IPAddress: prefix.Addr().String()}
			result[id] = inspection
		}
	}
	return result, len(result) > 0
}

func generatedDomainColdRetainedV2ContractObservation(state gatewayV2RouteState,
	observation gatewayV2DockerObservation,
) gatewayV2DockerObservation {
	result := observation
	result.ApplicationNetworks = make(map[string]caddyNetworkInspection,
		len(observation.ApplicationNetworks))
	for name, network := range observation.ApplicationNetworks {
		result.ApplicationNetworks[name] = cloneGeneratedDomainColdRetainedGatewayNetwork(network)
	}
	nextHost := 3
	for _, route := range gatewayV2RouteRecords(state) {
		for _, endpoint := range route.Endpoints {
			network := result.ApplicationNetworks[endpoint.NetworkName]
			found := false
			for memberID := range network.Containers {
				if normalizeID(memberID) == normalizeID(endpoint.ContainerID) {
					found = true
					break
				}
			}
			if !found {
				network.Containers[endpoint.ContainerID] = caddyNetworkContainerInspection{
					Name: endpoint.Component, IPv4Address: "172.30.0." + strconv.Itoa(nextHost) + "/24"}
				nextHost++
				result.ApplicationNetworks[endpoint.NetworkName] = network
			}
		}
	}
	return result
}

func generatedDomainColdRetainedV2AssertPassiveCommandTable(t *testing.T, commands [][]string,
	state gatewayV2RouteState, observation gatewayV2DockerObservation,
	endpoints map[string]endpointInspection,
) {
	t.Helper()
	counts := make(map[string]int)
	volumeFormat := `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}},"labels":{{json .Labels}},"mountpoint":{{json .Mountpoint}},"createdAt":{{json .CreatedAt}}}`
	for _, args := range commands {
		kind := ""
		switch {
		case reflect.DeepEqual(args, []string{"image", "inspect", "--format", imageInspectFormat, caddyImage}):
			kind = "image"
		case len(args) == 5 && reflect.DeepEqual(args[:4], []string{
			"container", "inspect", "--format", gatewayContainerInspectFormat}) &&
			(args[4] == caddyContainerName || args[4] == state.Identity.StageContainer ||
				args[4] == state.Identity.FinalContainer):
			kind = "container:" + args[4]
		case len(args) == 5 && reflect.DeepEqual(args[:4], []string{
			"volume", "inspect", "--format", volumeFormat}) &&
			(args[4] == caddyVolumeName || args[4] == state.Identity.ConfigVolume ||
				args[4] == state.Identity.DataVolume):
			kind = "volume:" + args[4]
		case len(args) == 5 && reflect.DeepEqual(args[:4], []string{
			"network", "inspect", "--format", "json"}):
			kind = "network:" + args[4]
		case reflect.DeepEqual(args, []string{"container", "ls", "--all",
			"--filter", "label=" + gatewayV2ManagedLabelKey,
			"--filter", "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion,
			"--format", "{{.Names}}"}):
			kind = "owned:container"
		case reflect.DeepEqual(args, []string{"volume", "ls",
			"--filter", "label=" + gatewayV2ManagedLabelKey,
			"--filter", "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion,
			"--format", "{{.Name}}"}):
			kind = "owned:volume"
		case reflect.DeepEqual(args, []string{"network", "ls",
			"--filter", "label=" + gatewayV2ManagedLabelKey,
			"--filter", "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion,
			"--format", "{{.Name}}"}):
			kind = "owned:network"
		case len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] == "-":
			kind = "restart"
		case len(args) >= 3 && generatedDomainGatewayAdminCommand(args, args[2]):
			kind = "admin"
		case len(args) == 5 && reflect.DeepEqual(args[:4], []string{
			"container", "inspect", "--format", endpointInspectFormat}):
			if _, found := endpoints[normalizeID(args[4])]; found {
				kind = "endpoint"
			}
		}
		if kind == "" || generatedDomainGatewayChallengeCommand(args, func() string {
			if len(args) > 2 {
				return args[2]
			}
			return ""
		}()) {
			t.Fatalf("retained V2 passive reader issued an unsupported or serving command: %#v", args)
		}
		counts[kind]++
	}
	requiredTwice := []string{
		"container:" + caddyContainerName, "container:" + state.Identity.StageContainer,
		"container:" + state.Identity.FinalContainer, "volume:" + caddyVolumeName,
		"volume:" + state.Identity.ConfigVolume, "volume:" + state.Identity.DataVolume,
		"network:" + caddyNetworkName, "network:" + state.Identity.IngressNetwork,
		"owned:container", "owned:volume", "owned:network", "restart", "endpoint",
	}
	for name := range observation.ApplicationNetworks {
		requiredTwice = append(requiredTwice, "network:"+name)
	}
	if counts["image"] != 1 {
		t.Fatalf("retained V2 passive reader image reads=%d", counts["image"])
	}
	for _, name := range requiredTwice {
		if counts[name] < 2 {
			t.Fatalf("retained V2 passive reader %s reads=%d, want confirmation", name, counts[name])
		}
	}
}
