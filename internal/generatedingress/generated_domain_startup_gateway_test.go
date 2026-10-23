package generatedingress

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGeneratedDomainStartupRequiresExactTypedRecovery(t *testing.T) {
	t.Run("bootstrap_nonce_native_v1_recovery_descriptor", func(t *testing.T) {
		source, state, journal := generatedDomainGatewayOnlyUpgradeTestState(t)
		selection := gatewayUpgradeGenerationSelection{Store: &gatewayUpgradeStateStore{},
			State: state, Journal: journal, Existing: true, operationID: state.OperationID}
		request := gatewayV2UpgradeRequestFromStateForTest(state)
		database := generatedDomainUpgradeDatabaseSnapshot(request, appaccess.GatewayProfileUpgradePrepared, 1)
		protected := generatedDomainProtectedStartupSnapshot{effectEmpty: false, nativeV1: &source,
			upgrade: gatewayUpgradeHistory{generations: []gatewayUpgradeGenerationSelection{selection}}}
		expected := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
		if expected.kind != generatedDomainRecoveryDockerUpgrade || len(expected.alternatives) != 1 {
			t.Fatalf("upgrade recovery expectation = %#v", expected)
		}
		objects := generatedDomainDockerObjectsForDescriptors(expected.alternatives[0])
		volume := -1
		for index := range objects {
			if objects[index].kind == "volume" && objects[index].name == caddyVolumeName {
				volume = index
				break
			}
		}
		if volume < 0 {
			t.Fatal("prepared recovery omitted native-v1 volume")
		}
		objects[volume].labels = cloneStringMap(objects[volume].labels)
		objects[volume].labels[generatedDomainBootstrapVolumeNonceLabel] = strings.Repeat("a", 64)
		if !generatedDomainRecoveryDockerObjectsAccounted(objects, expected) {
			t.Fatal("exact native-v1 recovery with bootstrap nonce was rejected")
		}
		objects[volume].labels[generatedDomainBootstrapVolumeNonceLabel] = strings.Repeat("A", 64)
		if generatedDomainRecoveryDockerObjectsAccounted(objects, expected) {
			t.Fatal("malformed native-v1 recovery bootstrap nonce was accounted")
		}
	})

	t.Run("recovery_observation_never_probes_applications", func(t *testing.T) {
		source, state, journal := generatedDomainGatewayOnlyUpgradeTestState(t)
		selection := gatewayUpgradeGenerationSelection{Store: &gatewayUpgradeStateStore{},
			State: state, Journal: journal, Existing: true, operationID: state.OperationID}
		request := gatewayV2UpgradeRequestFromStateForTest(state)
		database := generatedDomainUpgradeDatabaseSnapshot(request, appaccess.GatewayProfileUpgradePrepared, 1)
		protected := generatedDomainProtectedStartupSnapshot{effectEmpty: false, nativeV1: &source,
			upgrade: gatewayUpgradeHistory{generations: []gatewayUpgradeGenerationSelection{selection}}}
		expected := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
		if expected.kind != generatedDomainRecoveryDockerUpgrade || len(expected.alternatives) != 1 {
			t.Fatalf("upgrade recovery expectation = %#v", expected)
		}
		retained := generatedDomainDockerObjectsForDescriptors(expected.alternatives[0])
		docker := generatedDomainDockerStartupSnapshot{objects: retained,
			recoveryAccounted: generatedDomainRecoveryDockerObjectsAccounted(retained, expected)}
		got, err := classifyGeneratedDomainStartup(database, protected, docker)
		if err != nil || got.disposition != generatedDomainStartupRecoveryRequired ||
			len(got.recovery) != 1 || got.recovery[0] != (generatedDomainStartupRecovery{
			kind: generatedDomainStartupRecoveryUpgrade, operationID: state.OperationID}) {
			t.Fatalf("typed recovery = %#v, %v", got, err)
		}

		crossed := selection
		crossed.State.Profile.SelectedIPv4 = "192.0.2.99"
		protected.upgrade.generations[0] = crossed
		got, err = classifyGeneratedDomainStartup(database, protected, docker)
		if err != nil || got.disposition != generatedDomainStartupRefused || got.reason != generatedDomainStartupReasonAmbiguous {
			t.Fatalf("same-ID crossed recovery = %#v, %v", got, err)
		}

		unmatched := protected
		unmatched.upgrade.generations = nil
		got, err = classifyGeneratedDomainStartup(database, unmatched, docker)
		if err != nil || got.disposition != generatedDomainStartupRefused {
			t.Fatalf("SQL-only recovery = %#v, %v", got, err)
		}

		protected.upgrade.generations[0] = selection
		assertObjectsRefused := func(name string, objects []generatedDomainDockerObject) {
			t.Helper()
			t.Run(name, func(t *testing.T) {
				if generatedDomainRecoveryDockerObjectsAccounted(objects, expected) {
					t.Fatal("non-exact recovery object set was accounted")
				}
				got, classifyErr := classifyGeneratedDomainStartup(database, protected,
					generatedDomainDockerStartupSnapshot{objects: objects})
				if classifyErr != nil || got.disposition != generatedDomainStartupRefused ||
					got.reason != generatedDomainStartupReasonAmbiguous {
					t.Fatalf("non-exact recovery object set classified as %#v, %v", got, classifyErr)
				}
			})
		}
		assertObjectsRefused("missing_required_resource", append([]generatedDomainDockerObject(nil), retained[1:]...))
		wrongName := append([]generatedDomainDockerObject(nil), retained...)
		wrongName[0].name = "renamed-expected-container"
		assertObjectsRefused("same_id_wrong_name", wrongName)
		partialLabels := append([]generatedDomainDockerObject(nil), retained...)
		partialLabels[0].labels = cloneStringMap(retained[0].labels)
		delete(partialLabels[0].labels, gatewayV2IdentityLabelKey)
		assertObjectsRefused("same_id_partial_labels", partialLabels)
		wrongStatic := append([]generatedDomainDockerObject(nil), retained...)
		wrongStatic[1].data.driver = "overlay"
		assertObjectsRefused("same_id_wrong_static_fields", wrongStatic)

		for _, extra := range []generatedDomainDockerObject{
			{kind: "container", id: fmt.Sprintf("%064x", 101), name: "renamed-builder", labels: map[string]string{"rig.controller": "generated-builder"}},
			{kind: "network", id: fmt.Sprintf("%064x", 102), name: "renamed-network", labels: map[string]string{"io.rig.managed": "unexpected"}},
			{kind: "volume", name: "renamed-volume", labels: map[string]string{"rig.builder": "crossed"}},
			{kind: "network", id: fmt.Sprintf("%064x", 103), name: gatewayV2NetworkName,
				labels: map[string]string{gatewayV2ManagedLabelKey: gatewayV2ManagedNetworkLabel,
					gatewayV2IdentityLabelKey: gatewayV2IdentityVersion},
				data: generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
		} {
			objects := append(append([]generatedDomainDockerObject(nil), retained...), extra)
			withExtra := generatedDomainDockerStartupSnapshot{objects: objects,
				recoveryAccounted: generatedDomainRecoveryDockerObjectsAccounted(objects, expected)}
			got, err = classifyGeneratedDomainStartup(database, protected, withExtra)
			if err != nil || got.disposition != generatedDomainStartupRefused || got.reason != generatedDomainStartupReasonAmbiguous {
				t.Fatalf("recovery with crossed extra %#v = %#v, %v", extra, got, err)
			}
		}
	})

	t.Run("upgrade_phase_descriptor_rows", func(t *testing.T) {
		source, state, baseJournal := generatedDomainGatewayOnlyUpgradeTestState(t)
		request := gatewayV2UpgradeRequestFromStateForTest(state)
		database := generatedDomainUpgradeDatabaseSnapshot(request, appaccess.GatewayProfileUpgradePrepared, 1)
		expectation := func(t *testing.T, journal gatewayMigrationJournal) generatedDomainRecoveryDockerExpectation {
			t.Helper()
			selection := gatewayUpgradeGenerationSelection{Store: &gatewayUpgradeStateStore{}, State: state,
				Journal: journal, Existing: true, operationID: state.OperationID}
			protected := generatedDomainProtectedStartupSnapshot{nativeV1: &source,
				upgrade: gatewayUpgradeHistory{generations: []gatewayUpgradeGenerationSelection{selection}}}
			got := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
			if got.kind != generatedDomainRecoveryDockerUpgrade {
				t.Fatalf("phase %q recovery expectation = %#v", journal.Phase, got)
			}
			return got
		}
		assertCounts := func(t *testing.T, got generatedDomainRecoveryDockerExpectation, counts ...int) {
			t.Helper()
			if len(got.alternatives) != len(counts) {
				t.Fatalf("alternatives=%d want=%d: %#v", len(got.alternatives), len(counts), got.alternatives)
			}
			actual := make([]int, len(got.alternatives))
			for index := range got.alternatives {
				actual[index] = len(got.alternatives[index])
			}
			sort.Ints(actual)
			sort.Ints(counts)
			if !reflect.DeepEqual(actual, counts) {
				t.Fatalf("descriptor counts=%v want=%v", actual, counts)
			}
		}

		assertCounts(t, expectation(t, baseJournal), 3)
		partialJournal := baseJournal
		partialJournal.Phase = gatewayPhaseStageIntent
		partialJournal.Resources.ImageID = fmt.Sprintf("%064x", 200)
		partialJournal.Resources.IngressNetworkID = fmt.Sprintf("%064x", 201)
		partial := expectation(t, partialJournal)
		assertCounts(t, partial, 4)
		objects := generatedDomainDockerObjectsForDescriptors(partial.alternatives[0])
		objects = append(objects, generatedDomainDockerObject{kind: "image", id: "sha256:" + partial.imageID,
			labels: map[string]string{}, data: generatedDomainDockerObjectData{contentID: "sha256:" + partial.imageID}})
		if !generatedDomainRecoveryDockerObjectsAccounted(objects, partial) {
			t.Fatal("exact partial stage-intent set with optional pinned image was rejected")
		}
		wrongID := append([]generatedDomainDockerObject(nil), objects...)
		for index := range wrongID {
			if wrongID[index].name == state.Identity.IngressNetwork {
				wrongID[index].id = fmt.Sprintf("%064x", 202)
			}
		}
		if generatedDomainRecoveryDockerObjectsAccounted(wrongID, partial) {
			t.Fatal("bound recovery resource with wrong immutable ID was accounted")
		}

		complete := baseJournal
		complete.Resources = gatewayV2IdentityTestBoundResources(t)
		complete.Resources.FinalContainerID = ""
		complete.Phase = gatewayPhaseStageIntent
		assertCounts(t, expectation(t, complete), 7)
		complete.Phase = gatewayPhaseStaged
		assertCounts(t, expectation(t, complete), 7)
		complete.Resources.FinalContainerID = fmt.Sprintf("%064x", 203)
		complete.Phase = gatewayPhaseTransferIntent
		assertCounts(t, expectation(t, complete), 6, 7, 7)
		complete.Phase = gatewayPhaseRollbackIntent
		assertCounts(t, expectation(t, complete), 6, 7, 7)
		complete.Phase = gatewayPhaseV2Serving
		assertCounts(t, expectation(t, complete), 7)
		complete.Phase = gatewayPhaseUncertain
		selection := gatewayUpgradeGenerationSelection{Store: &gatewayUpgradeStateStore{}, State: state,
			Journal: complete, Existing: true, operationID: state.OperationID}
		protected := generatedDomainProtectedStartupSnapshot{nativeV1: &source,
			upgrade: gatewayUpgradeHistory{generations: []gatewayUpgradeGenerationSelection{selection}}}
		if got := resolveGeneratedDomainRecoveryDockerExpectation(database, protected); got.kind != generatedDomainRecoveryDockerNone {
			t.Fatalf("uncertain upgrade received recovery descriptors: %#v", got)
		}
	})

	t.Run("real_reader_prepared_retained_effects", func(t *testing.T) {
		source, state, journal := generatedDomainGatewayOnlyUpgradeTestState(t)
		selection := gatewayUpgradeGenerationSelection{Store: &gatewayUpgradeStateStore{}, State: state,
			Journal: journal, Existing: true, operationID: state.OperationID}
		request := gatewayV2UpgradeRequestFromStateForTest(state)
		database := generatedDomainUpgradeDatabaseSnapshot(request, appaccess.GatewayProfileUpgradePrepared, 1)
		protected := generatedDomainProtectedStartupSnapshot{nativeV1: &source,
			upgrade: gatewayUpgradeHistory{generations: []gatewayUpgradeGenerationSelection{selection}}}
		expected := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
		if expected.kind != generatedDomainRecoveryDockerUpgrade || len(expected.alternatives) != 1 {
			t.Fatalf("prepared recovery expectation = %#v", expected)
		}

		observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
		observation.V1Container.ID = fmt.Sprintf("%064x", 11)
		observation.V1NetworkID = fmt.Sprintf("%064x", 14)
		for key, value := range observation.V1Network.Containers {
			delete(observation.V1Network.Containers, key)
			observation.V1Network.Containers[observation.V1Container.ID] = value
		}
		objects := generatedDomainDockerObjectsForDescriptors(expected.alternatives[0])
		for index := range objects {
			switch objects[index].name {
			case caddyContainerName:
				objects[index].id = observation.V1Container.ID
			case caddyNetworkName:
				objects[index].id = observation.V1NetworkID
				objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
					scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
			case caddyVolumeName:
				objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
					scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
					mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
			}
		}
		gatewayRunner := &generatedDomainGatewayAttestationRunner{
			image: observation.Image,
			container: gatewayContainerInspection{caddyInspection: observation.V1Container,
				gatewayContainerRuntime: observation.V1Runtime},
			volumes: map[string]gatewayVolumeIdentityInspection{caddyVolumeName: generatedDomainGatewayVolumeIdentity(
				observation.V1Volume, observation.V1VolumeIdentity)},
			network: gatewayNetworkIdentityInspection{ID: observation.V1NetworkID, Name: observation.V1Network.Name,
				Driver: observation.V1Network.Driver, Scope: observation.V1Network.Scope,
				Internal: observation.V1Network.Internal, Options: observation.V1Network.Options,
				IPAM: observation.V1Network.IPAM, Labels: observation.V1Network.Labels,
				Containers: observation.V1Network.Containers},
			restartName: gatewayV2ActiveConfigFile, restart: observation.V1RestartConfig,
			live: observation.V1Config,
		}
		runner := &generatedDomainRecoveryCensusRunner{daemonID: "startup-recovery-daemon",
			objects: objects, imageID: observation.Image.ID, gateway: gatewayRunner}
		options := generatedDomainOptions{DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerConfigDirectory: t.TempDir(),
			WorkingDirectory: t.TempDir(), HostPort: journal.Source.LocalHostPort,
			CommandTimeout: time.Minute, OutputLimit: 1 << 20}
		reader, err := newGeneratedDomainStartupReader(runner, options, runner.daemonID)
		if err != nil {
			t.Fatal(err)
		}
		reader.recovery = expected
		reader.census, err = resolveGeneratedDomainDockerCensus(protected)
		if err != nil {
			t.Fatal(err)
		}
		docker, err := readGeneratedDomainDockerStartup(context.Background(), reader)
		if err != nil || !docker.recoveryAccounted || docker.cold || docker.ambiguous {
			attested, attestErr := reader.attestUpgradeRecovery(context.Background(), time.Now().Add(time.Minute), docker)
			t.Fatalf("real retained recovery observation = %#v, %v; exact=%t attested=%t attestErr=%v commands=%#v",
				docker, err, generatedDomainRecoveryDockerObjectsAccounted(docker.objects, expected),
				attested, attestErr, gatewayRunner.commands)
		}
		got, err := classifyGeneratedDomainStartup(database, protected, docker)
		if err != nil || got.disposition != generatedDomainStartupRecoveryRequired || len(got.recovery) != 1 ||
			got.recovery[0] != (generatedDomainStartupRecovery{kind: generatedDomainStartupRecoveryUpgrade,
				operationID: state.OperationID}) {
			t.Fatalf("real retained recovery classification = %#v, %v", got, err)
		}
		if !generatedDomainRecoveryCommandsInSafeOrder(gatewayRunner.commands) {
			t.Fatalf("real retained recovery command order = %#v", gatewayRunner.commands)
		}
	})

	t.Run("sealed_outer_prepared_retained_effects_and_crossed_extra", func(t *testing.T) {
		fixture := newGeneratedDomainGatewayOnlyUpgradeFixtureAtPhase(t, gatewayPhasePrepared)
		source, err := fixture.manager.store.load()
		if err != nil {
			t.Fatal(err)
		}
		database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil || len(database.Gateway.Upgrades.Claims) != 1 ||
			database.Gateway.Upgrades.Claims[0].Claim.State != appaccess.GatewayProfileUpgradePrepared {
			t.Fatalf("sealed prepared SQL snapshot = %#v, %v", database, err)
		}
		protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.dataRoot)
		if err != nil || len(protected.upgrade.generations) != 1 ||
			protected.upgrade.generations[0].Journal.Phase != gatewayPhasePrepared {
			t.Fatalf("real prepared protected snapshot = %#v, %v", protected, err)
		}
		expected := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
		if expected.kind != generatedDomainRecoveryDockerUpgrade || len(expected.alternatives) != 1 {
			t.Fatalf("real prepared recovery expectation = %#v", expected)
		}
		observation := gatewayV2IdentityTestObservation(t, source, fixture.state, fixture.journal,
			gatewayTopologyExactV1Only)
		observation.V1Container.ID = strings.Repeat("b", 64)
		observation.V1NetworkID = strings.Repeat("e", 64)
		for id, member := range observation.V1Network.Containers {
			delete(observation.V1Network.Containers, id)
			observation.V1Network.Containers[observation.V1Container.ID] = member
		}
		objects := generatedDomainDockerObjectsForDescriptors(expected.alternatives[0])
		for index := range objects {
			switch objects[index].name {
			case caddyContainerName:
				objects[index].id = observation.V1Container.ID
			case caddyNetworkName:
				objects[index].id = observation.V1NetworkID
				objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
					scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
			case caddyVolumeName:
				objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
					scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
					mountpoint: observation.V1VolumeIdentity.Mountpoint,
					createdAt:  observation.V1VolumeIdentity.CreatedAt}
			}
		}
		newRunner := func(values []generatedDomainDockerObject) (*generatedDomainRecoveryCensusRunner,
			*generatedDomainGatewayAttestationRunner) {
			gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
			return &generatedDomainRecoveryCensusRunner{daemonID: "sealed-prepared-recovery-daemon",
				objects: values, imageID: observation.Image.ID, gateway: gateway}, gateway
		}
		fixture.identity.options.HostPort = fixture.manager.options.HostPort
		fixture.identity.options.CommandTimeout = time.Minute
		fixture.identity.options.OutputLimit = 1 << 20
		positive, positiveGateway := newRunner(objects)
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.identity.stores,
			positive, fixture.identity.options)
		if err != nil || got.disposition != generatedDomainStartupRecoveryRequired || len(got.recovery) != 1 ||
			got.recovery[0] != (generatedDomainStartupRecovery{kind: generatedDomainStartupRecoveryUpgrade,
				operationID: fixture.state.OperationID}) {
			reader, readerErr := newGeneratedDomainStartupReader(positive, fixture.identity.options, positive.daemonID)
			var docker generatedDomainDockerStartupSnapshot
			var dockerErr error
			if readerErr == nil {
				reader.recovery, reader.census = expected, generatedDomainGatewayExpectation{}
				reader.census, readerErr = resolveGeneratedDomainDockerCensus(protected)
				if readerErr == nil {
					docker, dockerErr = readGeneratedDomainDockerStartup(context.Background(), reader)
				}
			}
			t.Fatalf("sealed prepared retained-effects recovery = %#v, %v; readerErr=%v docker=%#v dockerErr=%v commands=%#v",
				got, err, readerErr, docker, dockerErr, positiveGateway.commands)
		}
		if !generatedDomainRecoveryCommandsInSafeOrder(positiveGateway.commands) {
			t.Fatalf("sealed prepared recovery read order = %#v", positiveGateway.commands)
		}
		for _, command := range positiveGateway.commands {
			if len(command) >= 2 && command[0] == "container" && command[1] == "exec" &&
				!generatedDomainGatewayAdminCommand(command, observation.V1Container.ID) {
				t.Fatalf("sealed recovery dispatched application traffic: %#v", command)
			}
		}

		extra := generatedDomainDockerObject{kind: "container", id: fmt.Sprintf("%064x", 919),
			name: "renamed-crossed-builder", labels: map[string]string{"rig.controller": "generated-builder"}}
		crossedObjects := append(append([]generatedDomainDockerObject(nil), objects...), extra)
		crossed, crossedGateway := newRunner(crossedObjects)
		got, err = inspectGeneratedDomainStartup(context.Background(), fixture.identity.stores,
			crossed, fixture.identity.options)
		if err != nil || got.disposition != generatedDomainStartupRefused ||
			got.reason != generatedDomainStartupReasonAmbiguous {
			t.Fatalf("sealed prepared recovery with crossed extra = %#v, %v", got, err)
		}
		generatedDomainAssertNoOuterExec(t, crossedGateway.commands)
	})

	t.Run("upgrade_recovery_prevalidates_stage_and_final_before_traffic", func(t *testing.T) {
		cases := []struct {
			name     string
			phase    gatewayMigrationPhase
			topology gatewayObservedTopology
		}{
			{name: "stage", phase: gatewayPhaseStaged, topology: gatewayTopologyExactV1WithStage},
			{name: "final", phase: gatewayPhaseV2Serving, topology: gatewayTopologyExactFinalV2},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				t.Run("valid", func(t *testing.T) {
					reader, docker, runner, target := generatedDomainUpgradeRecoveryTrafficFixture(
						t, test.phase, test.topology)
					attested, err := reader.attestUpgradeRecovery(context.Background(), time.Now().Add(time.Minute), docker)
					if err != nil || !attested || !generatedDomainRecoveryTargetTrafficOrdered(runner.commands, target) {
						t.Fatalf("valid %s recovery = %t, %v commands=%#v", test.name, attested, err, runner.commands)
					}
				})
				t.Run("noncanonical_restart_has_zero_target_traffic", func(t *testing.T) {
					reader, docker, runner, target := generatedDomainUpgradeRecoveryTrafficFixture(
						t, test.phase, test.topology)
					restart := runner.restarts[target]
					restart.body = []byte(`{"admin":{"listen":"localhost:2019"}}`)
					runner.restarts[target] = restart
					attested, err := reader.attestUpgradeRecovery(context.Background(), time.Now().Add(time.Minute), docker)
					if err != nil || attested || generatedDomainTargetAdminSeen(runner.commands, target) ||
						generatedDomainTargetChallengeSeen(runner.commands, target) {
						t.Fatalf("noncanonical %s restart reached traffic: attested=%t err=%v commands=%#v",
							test.name, attested, err, runner.commands)
					}
				})
				t.Run("noncanonical_live_has_admin_but_zero_challenge", func(t *testing.T) {
					reader, docker, runner, target := generatedDomainUpgradeRecoveryTrafficFixture(
						t, test.phase, test.topology)
					runner.lives[target] = []byte(`{"admin":{"listen":"localhost:2019"}}`)
					attested, err := reader.attestUpgradeRecovery(context.Background(), time.Now().Add(time.Minute), docker)
					if err != nil || attested || !generatedDomainTargetAdminSeen(runner.commands, target) ||
						generatedDomainTargetChallengeSeen(runner.commands, target) {
						t.Fatalf("noncanonical %s live config reached challenge: attested=%t err=%v commands=%#v",
							test.name, attested, err, runner.commands)
					}
				})
			})
		}
	})

	t.Run("typed_successor_and_rollback_descriptor_boundaries", func(t *testing.T) {
		seed := newGatewayCurrentStateFixture(t)
		current := generatedDomainCurrentAttestationFixtureForTest(t)
		seed.baseline = current.stateV1
		seed.receipt = current.receipt
		seed.intent.Intent.Roster = nil
		checkpoint, intent := gatewayRebindAttemptTypedFixture(t, seed)
		first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		forward := generatedDomainTypedForwardPrefix(t, seed, checkpoint, intent, first, 6)
		entry := appaccess.GatewayRebindHistoryEntry{
			Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: appaccess.GatewayRebindSpecVersionV2,
				V2: &intent.Claim},
			RosterV2:     append([]appaccess.GatewayRebindRosterEntryV2(nil), intent.Roster...),
			RuntimeHeads: append([]appaccess.GatewayRebindRuntimeHead(nil), intent.RuntimeHeads...),
			Events: []appaccess.GatewayRebindEvent{{OperationID: intent.OperationID, Sequence: 1,
				State: appaccess.GatewayRebindPrepared, CreatedAt: intent.Claim.CreatedAt}},
		}
		source := gatewayCurrentAuthority(current.stateV1.Lineage)
		profile := gatewayCurrentFixtureProfile(current.stateV1.Lineage, current.stateV1.Profile)
		database := deployments.GeneratedDomainStartupSnapshot{Gateway: appaccess.HostingGatewayStartupSnapshot{
			Rebind: appaccess.GatewayRebindRecoverySnapshot{History: []appaccess.GatewayRebindHistoryEntry{entry},
				Active: &entry, Phase: appaccess.GatewayRebindPrepared, CurrentSource: &source, CurrentProfile: &profile},
		}}
		baseHistory := gatewayRebindProtectedIntentHistory{
			Checkpoints: []gatewayRebindPredecessorCheckpointSelection{{Generation: checkpoint.Generation,
				Checkpoint: checkpoint, Existing: true}},
			IntentsV2: []gatewayRebindProtectedIntentV2Selection{{Generation: intent.Generation,
				Intent: intent, Existing: true}},
		}
		currentName, _ := gatewayCurrentRouteStateName(current.stateV1.Lineage.ProtectedGeneration,
			current.stateV1.Lineage.OperationID)
		baseProtected := generatedDomainProtectedStartupSnapshot{currentV1: map[string]gatewayCurrentRouteState{
			currentName: current.stateV1},
			terminals: map[string]gatewayRebindAttemptTerminalView{current.stateV1.Lineage.OperationID: current.terminal},
			hasRebind: true}
		expectPrefix := func(t *testing.T, records []gatewayRebindProgressRecord, want int) generatedDomainRecoveryDockerExpectation {
			t.Helper()
			history := baseHistory
			for _, record := range records {
				history.Progress = append(history.Progress, gatewayRebindProgressSelection{Generation: record.Generation,
					Sequence: record.Sequence, Record: record, Existing: true})
			}
			protected := baseProtected
			protected.rebind = history
			got := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
			if got.kind != generatedDomainRecoveryDockerRebind || len(got.alternatives) != 1 ||
				len(got.alternatives[0]) != want {
				t.Fatalf("typed prefix %d expectation = %#v", records[len(records)-1].Sequence, got)
			}
			return got
		}
		expectPrefix(t, forward[:1], 4)
		expectPrefix(t, forward[:3], 5)
		sequenceSix := expectPrefix(t, forward, 8)
		objects := generatedDomainDockerObjectsForDescriptors(sequenceSix.alternatives[0])
		if !generatedDomainRecoveryDockerObjectsAccounted(objects, sequenceSix) {
			t.Fatal("exact typed sequence-six descriptors were rejected")
		}

		routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		lastAt, err := parseGatewayRebindProgressTime(forward[len(forward)-1].OccurredAt)
		if err != nil {
			t.Fatal(err)
		}
		owned := gatewayRebindTypedRollbackOwnedFromEffect(*forward[len(forward)-1].TypedEffect)
		rollbackIntent, err := newGatewayRebindTypedRollbackIntentV2(intent, forward, owned, routesDigest, "",
			lastAt.Add(time.Nanosecond))
		if err != nil {
			t.Fatal(err)
		}
		withRollback := append(append([]gatewayRebindProgressRecord(nil), forward...), rollbackIntent)
		expectPrefix(t, withRollback, 8)
		rollbackProof := gatewayRebindTypedRollbackProofFixture(t, seed, routesDigest, rollbackIntent.Digest)
		rollbackComplete, err := newGatewayRebindTypedRollbackCompleteV2(intent, withRollback, rollbackProof,
			lastAt.Add(2*time.Nanosecond))
		if err != nil {
			t.Fatal(err)
		}
		expectPrefix(t, append(withRollback, rollbackComplete), 4)
	})

	t.Run("exact_native_v2_grant_recovery", func(t *testing.T) {
		_, _, _, pending, journal, request, _ := pendingGatewayV2LANGrantFixture(t, false)
		database, protected := generatedDomainNativeV2RecoveryFixture(t, pending, journal)
		database.Gateway.Grants = generatedDomainGrantStartupSnapshot([]GatewayV2LANStartupClaim{
			gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantPrepared, 1),
		})
		generatedDomainAssertTypedRecovery(t, database, protected, generatedDomainStartupRecovery{
			kind: generatedDomainStartupRecoveryGrant, operationID: request.AttemptID})

		crossed := database
		crossed.Gateway.Grants.Claims[0].Claim.Spec.AllocationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		generatedDomainAssertRecoveryRefused(t, crossed, protected)
	})

	t.Run("exact_native_v2_disable_recovery", func(t *testing.T) {
		_, store, journal, grant, _ := grantedLANForDisable(t)
		state, _, err := store.loadBoundUpgrade(journal.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		disable := disableRequestForGrant(t, grant)
		pending, _, err := gatewayV2LANDisablePendingState(state, disable)
		if err != nil {
			t.Fatal(err)
		}
		database, protected := generatedDomainNativeV2RecoveryFixture(t, pending, journal)
		grantClaim := gatewayV2LANAccessClaimWithDisable(grant, disable.OperationID)
		disableClaim := GatewayV2LANDisableStartupClaim{Request: disable,
			State: appaccess.AppAccessDisablePrepared, StateSequence: 1}
		database.Gateway.Grants = generatedDomainGrantStartupSnapshot([]GatewayV2LANStartupClaim{grantClaim})
		database.Gateway.Disables = generatedDomainDisableStartupSnapshot([]GatewayV2LANDisableStartupClaim{disableClaim})
		projectedGrants := GatewayLANGrantStartupClaims(database.Gateway.Grants)
		projectedDisables := GatewayLANDisableStartupClaims(database.Gateway.Disables)
		claims, claimsErr := validateGatewayV2LANAccessStartupClaims(projectedGrants, projectedDisables)
		values, valuesValid := generatedDomainNativeV2LANRecoveries(pending, claims)
		if claimsErr != nil || !valuesValid {
			t.Fatalf("disable projection grants=%#v disables=%#v claims=%#v values=%#v valid=%t err=%v",
				projectedGrants, projectedDisables, claims, values, valuesValid, claimsErr)
		}
		generatedDomainAssertTypedRecovery(t, database, protected, generatedDomainStartupRecovery{
			kind: generatedDomainStartupRecoveryDisable, operationID: disable.OperationID})

		crossed := database
		crossed.Gateway.Disables.Claims[0].Claim.Spec.Port++
		generatedDomainAssertRecoveryRefused(t, crossed, protected)
	})

	t.Run("exact_native_v2_batch_recovery", func(t *testing.T) {
		manager, store, journal, _, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
		batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
		database, protected := generatedDomainNativeV2RecoveryFixture(t, batch, journal)
		database.Gateway.Grants = generatedDomainGrantStartupSnapshot(grants)
		database.Gateway.Disables = generatedDomainDisableStartupSnapshot(disables)
		generatedDomainAssertTypedRecovery(t, database, protected, generatedDomainStartupRecovery{
			kind: generatedDomainStartupRecoveryBatch, operationID: batch.OperationID})

		crossed := database
		crossed.Gateway.Disables.Claims[0].Claim.OperationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		generatedDomainAssertRecoveryRefused(t, crossed, protected)
	})

	t.Run("exact_rebind_recovery", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
		history := fixture.history
		history.Checkpoints = append(history.Checkpoints, gatewayRebindPredecessorCheckpointSelection{
			Generation: checkpoint.Generation, Checkpoint: checkpoint, Existing: true,
		})
		history.IntentsV2 = append(history.IntentsV2, gatewayRebindProtectedIntentV2Selection{
			Generation: intent.Generation, Intent: intent, Existing: true,
		})
		entry := appaccess.GatewayRebindHistoryEntry{
			Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: appaccess.GatewayRebindSpecVersionV2,
				V2: &intent.Claim},
			RosterV2:     append([]appaccess.GatewayRebindRosterEntryV2(nil), intent.Roster...),
			RuntimeHeads: append([]appaccess.GatewayRebindRuntimeHead(nil), intent.RuntimeHeads...),
			Events: []appaccess.GatewayRebindEvent{{OperationID: intent.OperationID, Sequence: 1,
				State: appaccess.GatewayRebindPrepared, CreatedAt: intent.Claim.CreatedAt}},
		}
		database := deployments.GeneratedDomainStartupSnapshot{Gateway: appaccess.HostingGatewayStartupSnapshot{
			Rebind: appaccess.GatewayRebindRecoverySnapshot{History: []appaccess.GatewayRebindHistoryEntry{entry},
				Active: &entry, Phase: appaccess.GatewayRebindPrepared},
		}}
		protected := generatedDomainProtectedStartupSnapshot{upgrade: gatewayUpgradeHistory{
			state: history.Predecessor.State, journal: history.Predecessor.Journal, committed: true,
			generations: []gatewayUpgradeGenerationSelection{history.Predecessor},
		}, rebind: history, hasRebind: true}
		generatedDomainAssertRecoveryRefused(t, database, protected)

		progress, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		history.Progress = append(history.Progress, gatewayRebindProgressSelection{
			Generation: progress.Generation, Sequence: progress.Sequence, Record: progress, Existing: true,
		})
		protected.rebind = history
		evidence, _, _, projectionErr := gatewayRebindProtectedRecoveryProjection(history, *database.Gateway.Rebind.Active)
		if projectionErr != nil {
			t.Fatalf("rebind projection phase=%q active=%#v evidence=%#v history=%#v error=%v",
				database.Gateway.Rebind.Phase, database.Gateway.Rebind.Active, evidence, history, projectionErr)
		}
		generatedDomainAssertTypedRecovery(t, database, protected, generatedDomainStartupRecovery{
			kind: generatedDomainStartupRecoveryRebind, operationID: intent.OperationID})

		crossed := database
		crossed.Gateway.Rebind.Active = cloneGatewayRebindHistoryEntry(database.Gateway.Rebind.Active)
		crossed.Gateway.Rebind.Active.Claim.V2.Spec.SuccessorProfileRevisionNumber++
		generatedDomainAssertRecoveryRefused(t, crossed, protected)
	})
}

func generatedDomainUpgradeRecoveryTrafficFixture(t *testing.T, phase gatewayMigrationPhase,
	topology gatewayObservedTopology,
) (*generatedDomainStartupReader, generatedDomainDockerStartupSnapshot,
	*generatedDomainGatewayAttestationRunner, string,
) {
	t.Helper()
	source, state, journal := generatedDomainGatewayOnlyUpgradeTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = phase
	if topology == gatewayTopologyExactV1WithStage {
		journal.Resources.FinalContainerID = ""
	}
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, topology)
	observation.V1Container.ID = fmt.Sprintf("%064x", 11)
	observation.V1NetworkID = fmt.Sprintf("%064x", 14)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	if observation.V1Runtime.ConfiguredNetworks != nil {
		configuredV1 := observation.V1Runtime.ConfiguredNetworks[caddyNetworkName]
		configuredV1.NetworkID = observation.V1NetworkID
		observation.V1Runtime.ConfiguredNetworks[caddyNetworkName] = configuredV1
	}
	observation.IngressNetworkID = journal.Resources.IngressNetworkID
	target := ""
	if topology == gatewayTopologyExactV1WithStage {
		target = journal.Resources.StageContainerID
		observation.StageContainer.ID = target
		for id, member := range observation.IngressNetwork.Containers {
			delete(observation.IngressNetwork.Containers, id)
			observation.IngressNetwork.Containers[target] = member
		}
		configured := observation.StageRuntime.ConfiguredNetworks[state.Identity.IngressNetwork]
		configured.NetworkID = journal.Resources.IngressNetworkID
		observation.StageRuntime.ConfiguredNetworks[state.Identity.IngressNetwork] = configured
	} else {
		target = journal.Resources.FinalContainerID
		observation.FinalContainer.ID = target
		for id, member := range observation.IngressNetwork.Containers {
			delete(observation.IngressNetwork.Containers, id)
			observation.IngressNetwork.Containers[target] = member
		}
		configured := observation.FinalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork]
		configured.NetworkID = journal.Resources.IngressNetworkID
		observation.FinalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork] = configured
	}

	containers := map[string]gatewayContainerInspection{
		observation.V1Container.ID: {caddyInspection: observation.V1Container,
			gatewayContainerRuntime: observation.V1Runtime},
	}
	restarts := map[string]generatedDomainRestartFixture{
		observation.V1Container.ID: {name: gatewayV2ActiveConfigFile, body: observation.V1RestartConfig},
	}
	lives := map[string][]byte{observation.V1Container.ID: observation.V1Config}
	if topology == gatewayTopologyExactV1WithStage {
		containers[target] = gatewayContainerInspection{caddyInspection: observation.StageContainer,
			gatewayContainerRuntime: observation.StageRuntime}
		restarts[target] = generatedDomainRestartFixture{name: state.Identity.StageConfigFilename,
			body: observation.StageRestartConfig}
		lives[target] = observation.StageConfig
	} else {
		containers[target] = gatewayContainerInspection{caddyInspection: observation.FinalContainer,
			gatewayContainerRuntime: observation.FinalRuntime}
		restarts[target] = generatedDomainRestartFixture{name: state.Identity.ActiveConfigFilename,
			body: observation.FinalRestartConfig}
		lives[target] = observation.FinalConfig
	}
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	runner := &generatedDomainGatewayAttestationRunner{
		image:      observation.Image,
		containers: containers,
		volumes: map[string]gatewayVolumeIdentityInspection{
			caddyVolumeName: generatedDomainGatewayVolumeIdentity(observation.V1Volume, observation.V1VolumeIdentity),
			state.Identity.ConfigVolume: generatedDomainGatewayVolumeIdentity(
				observation.ConfigVolume, observation.ConfigVolumeIdentity),
			state.Identity.DataVolume: generatedDomainGatewayVolumeIdentity(
				observation.DataVolume, observation.DataVolumeIdentity),
		},
		networks: map[string]gatewayNetworkIdentityInspection{
			observation.V1NetworkID: {ID: observation.V1NetworkID, Name: observation.V1Network.Name,
				Driver: observation.V1Network.Driver, Scope: observation.V1Network.Scope,
				Internal: observation.V1Network.Internal, Options: observation.V1Network.Options,
				IPAM: observation.V1Network.IPAM, Labels: observation.V1Network.Labels,
				Containers: observation.V1Network.Containers},
			observation.IngressNetworkID: {ID: observation.IngressNetworkID, Name: observation.IngressNetwork.Name,
				Driver: observation.IngressNetwork.Driver, Scope: observation.IngressNetwork.Scope,
				Internal: observation.IngressNetwork.Internal, Options: observation.IngressNetwork.Options,
				IPAM: observation.IngressNetwork.IPAM, Labels: observation.IngressNetwork.Labels,
				Containers: observation.IngressNetwork.Containers},
		},
		restarts: restarts,
		lives:    lives,
		challenges: map[string]string{target: gatewayV2ChallengeBodyPrefix +
			gatewayV2PortChallenge(challenge, state.Profile.PortStart)},
	}
	selection := gatewayUpgradeGenerationSelection{State: state, Journal: journal, Existing: true,
		operationID: state.OperationID}
	reader := &generatedDomainStartupReader{runner: runner,
		options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: t.TempDir(),
			HostPort: journal.Source.LocalHostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
		recovery: generatedDomainRecoveryDockerExpectation{source: &source, upgrade: &selection}}
	objects := []generatedDomainDockerObject{
		{kind: "container", id: observation.V1Container.ID, name: caddyContainerName},
		{kind: "volume", name: caddyVolumeName},
		{kind: "network", id: observation.V1NetworkID, name: caddyNetworkName},
		{kind: "volume", name: state.Identity.ConfigVolume},
		{kind: "volume", name: state.Identity.DataVolume},
		{kind: "network", id: observation.IngressNetworkID, name: state.Identity.IngressNetwork},
	}
	if topology == gatewayTopologyExactV1WithStage {
		objects = append(objects, generatedDomainDockerObject{kind: "container", id: target,
			name: state.Identity.StageContainer})
	} else {
		objects = append(objects, generatedDomainDockerObject{kind: "container", id: target,
			name: state.Identity.FinalContainer})
	}
	role := gatewayV2FinalContainerRole
	if topology == gatewayTopologyExactV1WithStage {
		role = gatewayV2StageContainerRole
	}
	if _, valid := generatedDomainUpgradeRunningConfig(state, journal, observation, role); !valid {
		container, runtime, found := observation.FinalContainer, observation.FinalRuntime, observation.FinalContainerFound
		if role == gatewayV2StageContainerRole {
			container, runtime, found = observation.StageContainer, observation.StageRuntime, observation.StageContainerFound
		}
		t.Fatalf("invalid recovery traffic fixture: resources=%t v1=%t volumes=%t owned=%t container=%t ingress=%t",
			gatewayV2ObservedResourcesMatchJournal(journal, observation),
			validGatewayV1Base(source, journal, observation, true), validGatewayV2Volumes(state, journal, observation),
			validOwnedNameSet(observation.OwnedContainers, strings.TrimPrefix(container.Name, "/")),
			validGatewayV2Container(state, journal, container, runtime, found, role, observation.Image.ID),
			validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound,
				container.ID, strings.TrimPrefix(container.Name, "/")))
	}
	return reader, generatedDomainDockerStartupSnapshot{objects: objects}, runner, target
}

func generatedDomainTargetAdminSeen(commands [][]string, target string) bool {
	for _, command := range commands {
		if generatedDomainGatewayAdminCommand(command, target) {
			return true
		}
	}
	return false
}

func generatedDomainTargetChallengeSeen(commands [][]string, target string) bool {
	for _, command := range commands {
		if generatedDomainGatewayChallengeCommand(command, target) {
			return true
		}
	}
	return false
}

func generatedDomainRecoveryTargetTrafficOrdered(commands [][]string, target string) bool {
	restart, admin, challenge := -1, -1, -1
	for index, command := range commands {
		switch {
		case len(command) == 4 && command[0] == "container" && command[1] == "cp" &&
			strings.HasPrefix(command[2], target+":"):
			restart = index
		case generatedDomainGatewayAdminCommand(command, target):
			admin = index
		case generatedDomainGatewayChallengeCommand(command, target):
			challenge = index
		}
	}
	return restart >= 0 && admin > restart && challenge > admin
}

func generatedDomainDockerObjectsForDescriptors(values []generatedDomainRecoveryDockerDescriptor) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, 0, len(values))
	for index, value := range values {
		id := value.id
		if id == "" && value.kind != "volume" {
			id = fmt.Sprintf("%064x", index+1)
		}
		result = append(result, generatedDomainDockerObject{kind: value.kind, id: id, name: value.name,
			labels: cloneStringMap(value.labels), data: generatedDomainDockerObjectData{driver: value.data.driver,
				scope: value.data.scope, options: cloneStringMap(value.data.options), mountpoint: value.data.mountpoint,
				createdAt: value.data.createdAt, contentID: value.data.contentID}})
	}
	return result
}

func generatedDomainGatewayOnlyUpgradeTestState(t *testing.T) (routeState, gatewayV2RouteState,
	gatewayMigrationJournal,
) {
	t.Helper()
	source, preparation := upgradeTestPreparation(t)
	source.Active = map[string]routeRecord{}
	identityDigest, err := gatewayV1ObservedIdentityDigest(gatewayV2DockerObservation{
		Image:            gatewayV2IdentityTestImage(),
		V1Container:      caddyInspection{ID: "sha256:" + fmt.Sprintf("%064x", 11)},
		V1NetworkID:      "sha256:" + fmt.Sprintf("%064x", 14),
		V1VolumeIdentity: gatewayV1VolumeIdentity{Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data", CreatedAt: "2026-09-29T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	preparation.SourceIdentityDigest = identityDigest
	state, journal, err := prepareGatewayV2State(source, preparation)
	if err != nil {
		t.Fatal(err)
	}
	return source, state, journal
}

func generatedDomainTypedForwardPrefix(t *testing.T, fixture gatewayCurrentStateFixture,
	checkpoint gatewayRebindPredecessorCheckpoint, intent gatewayRebindProtectedIntentV2,
	first gatewayRebindProgressRecord, through uint64,
) []gatewayRebindProgressRecord {
	t.Helper()
	if through < 1 || through > 6 {
		t.Fatal("invalid typed prefix boundary")
	}
	resources := fixture.receipt.Resources
	var err error
	resources.IngressNetwork, err = gatewayRebindTypedStageNetworkBindingFor(intent, resources.IngressNetwork.ID)
	if err != nil {
		t.Fatal(err)
	}
	resources.ConfigVolume, err = gatewayRebindTypedStageConfigVolumeBindingFor(intent,
		resources.ConfigVolume.Mountpoint, resources.ConfigVolume.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	resources.DataVolume, err = gatewayRebindTypedStageDataVolumeBindingFor(intent,
		resources.DataVolume.Mountpoint, resources.DataVolume.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	stagePlanDigest, err := gatewayRebindTypedStagePlanDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	effect := gatewayRebindTypedEffectProgress{ImageID: resources.ImageID, StagePlanDigest: stagePlanDigest}
	records := []gatewayRebindProgressRecord{first}
	firstAt, err := parseGatewayRebindProgressTime(first.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(2); sequence <= through; sequence++ {
		switch sequence {
		case 3:
			network := resources.IngressNetwork
			effect.Network = &network
		case 4:
			volume := resources.ConfigVolume
			effect.ConfigVolume = &volume
		case 5:
			volume := resources.DataVolume
			effect.DataVolume = &volume
		case 6:
			container, buildErr := gatewayRebindTypedStageContainerBindingFor(intent, effect,
				resources.StageContainer.ID)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			effect.StageContainer = &container
		}
		record, recordErr := newGatewayRebindTypedEffectProgressV2(intent, checkpoint, records, effect,
			firstAt.Add(time.Duration(sequence-1)*time.Nanosecond))
		if recordErr != nil {
			t.Fatalf("build typed progress %d: %v", sequence, recordErr)
		}
		records = append(records, record)
		effect = *record.TypedEffect
	}
	return records
}

func generatedDomainNativeV2RecoveryFixture(t *testing.T, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (deployments.GeneratedDomainStartupSnapshot, generatedDomainProtectedStartupSnapshot) {
	t.Helper()
	request := gatewayV2UpgradeRequestFromStateForTest(state)
	database := generatedDomainUpgradeDatabaseSnapshot(request, appaccess.GatewayProfileUpgradeCommitted, 3)
	profile := database.Gateway.Upgrades.Claims[0].Profile
	source := appaccess.GatewayCurrentAuthorityRef{
		Kind: appaccess.GatewayRebindSourceGatewayUpgrade, OperationID: state.OperationID,
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
		ProfileSpecDigest: profile.SpecDigest,
	}
	database.Gateway.Rebind.CurrentProfile = &profile
	database.Gateway.Rebind.CurrentSource = &source
	return database, generatedDomainProtectedStartupSnapshot{upgrade: gatewayUpgradeHistory{
		state: state, journal: journal, committed: true,
	}}
}

func generatedDomainGrantStartupSnapshot(values []GatewayV2LANStartupClaim) appaccess.AppAccessGrantStartupSnapshot {
	result := appaccess.AppAccessGrantStartupSnapshot{Claims: make([]appaccess.AppAccessGrantStartupClaim, 0, len(values))}
	for _, value := range values {
		request := value.Request
		entry := appaccess.AppAccessGrantStartupClaim{Claim: appaccess.AppAccessGrantClaim{
			AttemptID: request.AttemptID, RequestDigest: request.ClaimRequestDigest,
			Spec: appaccess.AppAccessGrantSpec{
				AppID: request.AppID, AllocationID: request.AllocationID, OwnerOperationID: request.OwnerOperationID,
				AccessRevisionID: request.AccessRevisionID, AccessRevisionNumber: request.AccessRevisionNumber,
				AccessSpecDigest: request.AccessSpecDigest, Port: request.Port,
				GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
				GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
				GatewayProfileSpecDigest:     request.GatewayProfileSpecDigest, ApprovedBy: request.ApprovedBy,
			}, State: value.State, StateSequence: value.StateSequence,
		}, AccessHeadCurrent: true, ProfileHeadCurrent: true, ApproverIsAdministrator: true}
		if value.DisableIntentOperationID != "" {
			entry.DisableIntent = &appaccess.AppAccessDisableIntent{OperationID: value.DisableIntentOperationID}
		}
		result.Claims = append(result.Claims, entry)
	}
	return result
}

func generatedDomainDisableStartupSnapshot(values []GatewayV2LANDisableStartupClaim) appaccess.AppAccessDisableStartupSnapshot {
	result := appaccess.AppAccessDisableStartupSnapshot{Claims: make([]appaccess.AppAccessDisableStartupClaim, 0, len(values))}
	for _, value := range values {
		request := value.Request
		entry := appaccess.AppAccessDisableStartupClaim{Claim: appaccess.AppAccessDisableClaim{
			OperationID: request.OperationID, RequestDigest: request.RequestDigest,
			Spec: appaccess.AppAccessDisableSpec{
				AppID: request.AppID, AllocationID: request.AllocationID, OwnerOperationID: request.OwnerOperationID,
				AccessRevisionID: request.AccessRevisionID, AccessRevisionNumber: request.AccessRevisionNumber,
				Port: request.Port, GatewayProfileRevisionID: request.GatewayProfileRevisionID,
				GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
			}, SpecDigest: request.SpecDigest, ApprovedBy: request.ApprovedBy,
			State: value.State, StateSequence: value.StateSequence,
		}, Revision: appaccess.AppAccessRevision{SpecDigest: request.AccessSpecDigest},
			Profile:           appaccess.GatewayProfileRevision{SpecDigest: request.GatewayProfileSpecDigest},
			AccessHeadCurrent: true, ProfileHeadCurrent: true, ApproverIsAdministrator: true}
		if request.SourceGrant != nil {
			grant := generatedDomainGrantStartupSnapshot([]GatewayV2LANStartupClaim{{Request: *request.SourceGrant}}).Claims[0].Claim
			entry.SourceGrant = &grant
		}
		result.Claims = append(result.Claims, entry)
	}
	return result
}

func generatedDomainAssertTypedRecovery(t *testing.T, database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, want generatedDomainStartupRecovery,
) {
	t.Helper()
	got, err := classifyGeneratedDomainStartup(database, protected,
		generatedDomainDockerStartupSnapshot{recoveryAccounted: true})
	if err != nil || got.disposition != generatedDomainStartupRecoveryRequired ||
		len(got.recovery) != 1 || got.recovery[0] != want {
		t.Fatalf("typed recovery = %#v, %v want=%#v", got, err, want)
	}
}

func generatedDomainAssertRecoveryRefused(t *testing.T, database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) {
	t.Helper()
	got, err := classifyGeneratedDomainStartup(database, protected,
		generatedDomainDockerStartupSnapshot{recoveryAccounted: true})
	if err != nil || got.disposition != generatedDomainStartupRefused || got.reason != generatedDomainStartupReasonAmbiguous {
		t.Fatalf("crossed recovery = %#v, %v", got, err)
	}
}

func cloneGatewayRebindHistoryEntry(value *appaccess.GatewayRebindHistoryEntry) *appaccess.GatewayRebindHistoryEntry {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func TestGeneratedDomainStartupRefusesEveryColdCensus(t *testing.T) {
	cases := []struct {
		name      string
		database  deployments.GeneratedDomainStartupSnapshot
		protected generatedDomainProtectedStartupSnapshot
		docker    generatedDomainDockerStartupSnapshot
	}{
		{name: "runtime_head", database: deployments.GeneratedDomainStartupSnapshot{
			Runtime: generatedruntimestate.GeneratedDomainStartupSnapshot{ActiveHeads: []generatedruntimestate.GeneratedDomainStartupActiveHead{{AppID: "app-a"}}}},
			protected: generatedDomainProtectedStartupSnapshot{effectEmpty: true}, docker: generatedDomainDockerStartupSnapshot{effectEmpty: true}},
		{name: "generated_deployment", database: deployments.GeneratedDomainStartupSnapshot{
			GeneratedDeployments: []deployments.GeneratedDomainStartupDeployment{{ID: "deployment-a"}}},
			protected: generatedDomainProtectedStartupSnapshot{effectEmpty: true}, docker: generatedDomainDockerStartupSnapshot{effectEmpty: true}},
		{name: "docker_cold", protected: generatedDomainProtectedStartupSnapshot{effectEmpty: true},
			docker: generatedDomainDockerStartupSnapshot{cold: true}},
		{name: "protected_ambiguous", protected: generatedDomainProtectedStartupSnapshot{ambiguous: true},
			docker: generatedDomainDockerStartupSnapshot{effectEmpty: true}},
		{name: "docker_ambiguous", protected: generatedDomainProtectedStartupSnapshot{effectEmpty: true},
			docker: generatedDomainDockerStartupSnapshot{ambiguous: true}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifyGeneratedDomainStartup(test.database, test.protected, test.docker)
			if err != nil || got.disposition != generatedDomainStartupRefused {
				t.Fatalf("cold census classified as %#v, %v", got, err)
			}
		})
	}
}

func generatedDomainUpgradeDatabaseSnapshot(request GatewayV2UpgradeRequest,
	state appaccess.GatewayProfileUpgradeState, sequence int64,
) deployments.GeneratedDomainStartupSnapshot {
	profile := appaccess.GatewayProfileRevision{ID: request.Profile.RevisionID,
		RevisionNumber: request.Profile.RevisionNumber, OperationID: request.OperationID,
		Spec: appaccess.GatewayProfileSpec{SelectedIPv4: request.Profile.SelectedIPv4,
			InterfaceID: request.Profile.InterfaceID, PortStart: request.Profile.PortStart, PortEnd: request.Profile.PortEnd},
		SpecDigest: request.Profile.SpecDigest, ApprovedBy: request.ApprovedBy}
	claim := appaccess.GatewayProfileUpgradeClaim{OperationID: request.OperationID,
		ProfileRevisionID: request.Profile.RevisionID, ProfileRevisionNumber: request.Profile.RevisionNumber,
		ProfileSpecDigest: request.Profile.SpecDigest, ApprovedBy: request.ApprovedBy,
		State: state, StateSequence: sequence}
	return deployments.GeneratedDomainStartupSnapshot{Gateway: appaccess.HostingGatewayStartupSnapshot{
		Upgrades: appaccess.GatewayUpgradeStartupSnapshot{Claims: []appaccess.GatewayUpgradeStartupClaim{{
			Claim: claim, Profile: profile, ApprovedActionDigest: request.ApprovedActionDigest,
		}}},
	}}
}
