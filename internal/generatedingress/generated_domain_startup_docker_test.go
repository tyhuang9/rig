package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainGatewayAttestationRunner struct {
	image       imageInspection
	container   gatewayContainerInspection
	containers  map[string]gatewayContainerInspection
	volumes     map[string]gatewayVolumeIdentityInspection
	network     gatewayNetworkIdentityInspection
	networks    map[string]gatewayNetworkIdentityInspection
	restartName string
	restart     []byte
	restarts    map[string]generatedDomainRestartFixture
	live        []byte
	lives       map[string][]byte
	challenge   string
	challenges  map[string]string
	commands    [][]string
}

type generatedDomainRestartFixture struct {
	name string
	body []byte
}

type generatedDomainRecoveryCensusRunner struct {
	daemonID string
	objects  []generatedDomainDockerObject
	imageID  string
	gateway  *generatedDomainGatewayAttestationRunner
}

func (r *generatedDomainRecoveryCensusRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	switch {
	case reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}):
		return jsonResult(r.daemonID), nil
	case reflect.DeepEqual(args, []string{"container", "ls", "--all", "--no-trunc", "--format", generatedDomainContainerListFormat}):
		return generatedDomainRecoveryCensusList(r.objects, "container"), nil
	case reflect.DeepEqual(args, []string{"network", "ls", "--no-trunc", "--format", generatedDomainNetworkListFormat}):
		return generatedDomainRecoveryCensusList(r.objects, "network"), nil
	case reflect.DeepEqual(args, []string{"volume", "ls", "--format", generatedDomainVolumeListFormat}):
		return generatedDomainRecoveryCensusList(r.objects, "volume"), nil
	case reflect.DeepEqual(args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"}):
		ids := make([]string, 0, 1+len(r.objects))
		seen := make(map[string]struct{})
		if r.imageID != "" {
			ids = append(ids, r.imageID)
			seen[r.imageID] = struct{}{}
		}
		for _, object := range r.objects {
			if object.kind != "image" {
				continue
			}
			if _, duplicate := seen[object.id]; duplicate {
				continue
			}
			ids = append(ids, object.id)
			seen[object.id] = struct{}{}
		}
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(ids, "\n") + "\n")}, nil
	case len(args) == 5 && args[1] == "inspect" && args[2] == "--format" &&
		args[3] == generatedDomainContainerInspectFormat && args[0] == "container":
		return generatedDomainRecoveryCensusProjection(r.objects, "container", args[4])
	case len(args) == 5 && args[1] == "inspect" && args[2] == "--format" &&
		args[3] == generatedDomainNetworkInspectFormat && args[0] == "network":
		return generatedDomainRecoveryCensusProjection(r.objects, "network", args[4])
	case len(args) == 5 && args[1] == "inspect" && args[2] == "--format" &&
		args[3] == generatedDomainVolumeInspectFormat && args[0] == "volume":
		return generatedDomainRecoveryCensusProjection(r.objects, "volume", args[4])
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"image", "inspect", "--format", generatedDomainImageInspectFormat}) &&
		args[4] == r.imageID:
		return jsonResult(generatedDomainDockerInspectProjection{ID: r.imageID,
			Labels: map[string]string{}, ContentID: r.imageID}), nil
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"image", "inspect", "--format", generatedDomainImageInspectFormat}):
		return generatedDomainRecoveryCensusProjection(r.objects, "image", args[4])
	default:
		if r.gateway == nil {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain recovery command")
		}
		return r.gateway.Run(ctx, request)
	}
}

func generatedDomainRecoveryCensusList(objects []generatedDomainDockerObject, kind string) runtimeprocess.CommandResult {
	var body []byte
	for _, object := range objects {
		if object.kind != kind {
			continue
		}
		entry := generatedDomainDockerListEntry{ID: object.id, Name: object.name}
		encoded := jsonResult(entry).Stdout
		body = append(body, encoded...)
		body = append(body, '\n')
	}
	return runtimeprocess.CommandResult{Stdout: body}
}

func generatedDomainRecoveryCensusProjection(objects []generatedDomainDockerObject, kind, identifier string) (
	runtimeprocess.CommandResult, error,
) {
	for _, object := range objects {
		if object.kind != kind || kind == "volume" && object.name != identifier || kind != "volume" && object.id != identifier {
			continue
		}
		name := object.name
		if kind == "container" {
			name = "/" + name
		}
		if kind == "volume" {
			return jsonResult(gatewayVolumeIdentityInspection{Name: object.name, Labels: cloneStringMap(object.labels),
				Driver: object.data.driver, Scope: object.data.scope, Options: cloneStringMap(object.data.options),
				Mountpoint: object.data.mountpoint, CreatedAt: object.data.createdAt}), nil
		}
		return jsonResult(generatedDomainDockerInspectProjection{ID: object.id, Name: name,
			Labels: cloneStringMap(object.labels), Driver: object.data.driver, Scope: object.data.scope,
			Options: cloneStringMap(object.data.options), Mountpoint: object.data.mountpoint,
			CreatedAt: object.data.createdAt, ContentID: object.data.contentID}), nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain recovery census object")
}

func (r *generatedDomainGatewayAttestationRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.commands = append(r.commands, append([]string(nil), request.Args...))
	args := request.Args
	switch {
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"image", "inspect", "--format", imageInspectFormat}):
		return jsonResult(r.image), nil
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"container", "inspect", "--format", gatewayContainerInspectFormat}):
		container := r.container
		if r.containers != nil {
			var ok bool
			container, ok = r.containers[args[4]]
			if !ok {
				return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup container")
			}
		}
		if args[4] != container.ID {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup container")
		}
		return jsonResult(container), nil
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"volume", "inspect", "--format", generatedDomainVolumeInspectFormat}):
		volume, ok := r.volumes[args[4]]
		if !ok {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup volume")
		}
		return jsonResult(volume), nil
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"network", "inspect", "--format", "json"}):
		network := r.network
		if r.networks != nil {
			var ok bool
			network, ok = r.networks[args[4]]
			if !ok {
				return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup network")
			}
		}
		if args[4] != network.ID {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup network")
		}
		return jsonResult([]gatewayNetworkIdentityInspection{network}), nil
	case len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] == "-":
		containerID := strings.SplitN(args[2], ":", 2)[0]
		restart := generatedDomainRestartFixture{name: r.restartName, body: r.restart}
		if r.restarts != nil {
			var ok bool
			restart, ok = r.restarts[containerID]
			if !ok {
				return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup restart config")
			}
		}
		if args[2] != containerID+":/config/"+restart.name {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup restart config")
		}
		return gatewayCurrentPhysicalTarResult(restart.name, restart.body)
	case len(args) >= 3 && generatedDomainGatewayAdminCommand(args, args[2]):
		live := r.live
		if r.lives != nil {
			var ok bool
			live, ok = r.lives[args[2]]
			if !ok {
				return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup live config")
			}
		}
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), live...)}, nil
	case len(args) >= 3 && generatedDomainGatewayChallengeCommand(args, args[2]):
		challenge := r.challenge
		if r.challenges != nil {
			var ok bool
			challenge, ok = r.challenges[args[2]]
			if !ok {
				return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup challenge")
			}
		}
		for _, arg := range args {
			if arg == "Host: wrong.invalid" {
				challenge = ""
			}
		}
		return runtimeprocess.CommandResult{Stdout: []byte(challenge + "\n404")}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected generated-domain startup command")
	}
}

func generatedDomainGatewayAdminCommand(args []string, containerID string) bool {
	return len(args) >= 4 && args[0] == "container" && args[1] == "exec" && args[2] == containerID &&
		args[len(args)-1] == "http://127.0.0.1:2019/config/"
}

func generatedDomainGatewayChallengeCommand(args []string, containerID string) bool {
	return len(args) >= 4 && args[0] == "container" && args[1] == "exec" && args[2] == containerID &&
		strings.Contains(args[len(args)-1], gatewayV2ChallengePathPrefix)
}

func TestGeneratedDomainStartupClassifiesStableGateway(t *testing.T) {
	t.Run("passive_gateway_only_supported_forms", func(t *testing.T) {
		state, journal, observation := generatedDomainNativeV1AttestationFixture(t)
		digest, err := canonicalDigest(state)
		if err != nil {
			t.Fatal(err)
		}
		expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
			stateDigest: digest, nativeV1: &state}
		expected.names = generatedDomainGatewayExpectedNames(expected)
		expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
		if err != nil {
			t.Fatal(err)
		}
		runner := &generatedDomainGatewayAttestationRunner{
			image: observation.Image,
			container: gatewayContainerInspection{caddyInspection: observation.V1Container,
				gatewayContainerRuntime: observation.V1Runtime},
			volumes: map[string]gatewayVolumeIdentityInspection{observation.V1Volume.Name: {Name: observation.V1Volume.Name,
				Driver: observation.V1Volume.Driver, Scope: observation.V1Volume.Scope,
				Options: observation.V1Volume.Options, Labels: observation.V1Volume.Labels,
				Mountpoint: observation.V1VolumeIdentity.Mountpoint, CreatedAt: observation.V1VolumeIdentity.CreatedAt}},
			network: gatewayNetworkIdentityInspection{ID: observation.V1NetworkID,
				Name: observation.V1Network.Name, Driver: observation.V1Network.Driver,
				Scope: observation.V1Network.Scope, Internal: observation.V1Network.Internal,
				Options: observation.V1Network.Options, IPAM: observation.V1Network.IPAM,
				Labels: observation.V1Network.Labels, Containers: observation.V1Network.Containers},
			restartName: gatewayV2ActiveConfigFile, restart: observation.V1RestartConfig,
			live: observation.V1Config,
		}
		reader := &generatedDomainStartupReader{runner: runner,
			options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
				HostPort: journal.Source.LocalHostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
			expectation: expected}
		snapshot := generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{
			{kind: "container", id: observation.V1Container.ID, name: caddyContainerName},
			{kind: "volume", name: caddyVolumeName},
			{kind: "network", id: observation.V1NetworkID, name: caddyNetworkName},
		}}
		got, err := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot)
		if err != nil || got.kind != generatedDomainGatewayNativeV1 || !got.exact || got.stateDigest != digest {
			t.Fatalf("native V1 passive attestation = %#v, %v", got, err)
		}
		snapshot.gateway = got
		generatedDomainAssertStableClassification(t, deployments.GeneratedDomainStartupSnapshot{},
			generatedDomainProtectedStartupSnapshot{nativeV1: &state}, snapshot)
		if !generatedDomainGatewayCommandsInSafeOrder(runner.commands) {
			t.Fatalf("passive read order = %#v", runner.commands)
		}

		t.Run("native_v2", func(t *testing.T) {
			state, journal, observation := generatedDomainNativeV2AttestationFixture(t)
			digest, err := canonicalDigest(state)
			if err != nil {
				t.Fatal(err)
			}
			expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV2,
				stateDigest: digest, nativeV2: &state, journal: &journal}
			expected.names = generatedDomainGatewayExpectedNames(expected)
			expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
			if err != nil {
				t.Fatal(err)
			}
			token, err := gatewayV2HostChallenge(state)
			if err != nil {
				t.Fatal(err)
			}
			portToken := gatewayV2PortChallenge(token, state.Profile.PortStart)
			runner := &generatedDomainGatewayAttestationRunner{
				image: observation.Image,
				container: gatewayContainerInspection{caddyInspection: observation.FinalContainer,
					gatewayContainerRuntime: observation.FinalRuntime},
				volumes: map[string]gatewayVolumeIdentityInspection{
					observation.ConfigVolume.Name: generatedDomainGatewayVolumeIdentity(observation.ConfigVolume, observation.ConfigVolumeIdentity),
					observation.DataVolume.Name:   generatedDomainGatewayVolumeIdentity(observation.DataVolume, observation.DataVolumeIdentity),
				},
				network: gatewayNetworkIdentityInspection{ID: observation.IngressNetworkID,
					Name: observation.IngressNetwork.Name, Driver: observation.IngressNetwork.Driver,
					Scope: observation.IngressNetwork.Scope, Internal: observation.IngressNetwork.Internal,
					Options: observation.IngressNetwork.Options, IPAM: observation.IngressNetwork.IPAM,
					Labels: observation.IngressNetwork.Labels, Containers: observation.IngressNetwork.Containers},
				restartName: state.Identity.ActiveConfigFilename, restart: observation.FinalRestartConfig,
				live: observation.FinalConfig, challenge: gatewayV2ChallengeBodyPrefix + portToken,
			}
			reader := &generatedDomainStartupReader{runner: runner,
				options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
					HostPort: journal.Source.LocalHostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
				expectation: expected}
			snapshot := generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{
				{kind: "container", id: observation.FinalContainer.ID, name: state.Identity.FinalContainer},
				{kind: "volume", name: state.Identity.ConfigVolume},
				{kind: "volume", name: state.Identity.DataVolume},
				{kind: "network", id: observation.IngressNetworkID, name: state.Identity.IngressNetwork},
			}}
			got, err := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot)
			if err != nil || got.kind != generatedDomainGatewayNativeV2 || !got.exact || got.stateDigest != digest ||
				got.operationID != state.OperationID {
				t.Fatalf("native V2 passive attestation = %#v, %v", got, err)
			}
			database, protected := generatedDomainNativeV2RecoveryFixture(t, state, journal)
			snapshot.gateway = got
			generatedDomainAssertStableClassification(t, database, protected, snapshot)
			if !generatedDomainGatewayCommandsInSafeOrder(runner.commands) {
				t.Fatalf("passive read order = %#v", runner.commands)
			}
		})

		t.Run("current_v1", func(t *testing.T) {
			fixture := generatedDomainCurrentAttestationFixtureForTest(t)
			expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV1,
				stateDigest: fixture.stateV1.Digest, currentV1: &fixture.stateV1, terminal: &fixture.terminal}
			generatedDomainAssertCurrentAttestation(t, expected, fixture, false)
		})

		t.Run("current_v2", func(t *testing.T) {
			fixture := generatedDomainCurrentAttestationFixtureForTest(t)
			expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV2,
				stateDigest: fixture.stateV2.Digest, currentV2: &fixture.stateV2, terminal: &fixture.terminal}
			generatedDomainAssertCurrentAttestation(t, expected, fixture, true)
		})
	})

	t.Run("sealed_outer_supported_forms", func(t *testing.T) {
		generatedDomainAssertOuterStableForms(t)
	})

	t.Run("native_v1_bootstrap_nonce_compatibility", func(t *testing.T) {
		state, journal, observation := generatedDomainNativeV1AttestationFixture(t)
		digest, err := canonicalDigest(state)
		if err != nil {
			t.Fatal(err)
		}
		expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
			stateDigest: digest, nativeV1: &state}
		expected.names = generatedDomainGatewayExpectedNames(expected)
		expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
		if err != nil {
			t.Fatal(err)
		}
		assert := func(t *testing.T, nonce string, wantExact bool) {
			t.Helper()
			candidate := observation
			candidate.V1Volume.Labels = cloneStringMap(observation.V1Volume.Labels)
			candidate.V1Volume.Labels[generatedDomainBootstrapVolumeNonceLabel] = nonce
			runner := generatedDomainNativeV1OuterGatewayRunner(candidate)
			reader := &generatedDomainStartupReader{runner: runner,
				options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
					HostPort: journal.Source.LocalHostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
				expectation: expected}
			snapshot := generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{
				{kind: "container", id: candidate.V1Container.ID, name: caddyContainerName},
				{kind: "volume", name: caddyVolumeName},
				{kind: "network", id: candidate.V1NetworkID, name: caddyNetworkName},
			}}
			got, attestErr := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot)
			if wantExact != (attestErr == nil && got.kind == generatedDomainGatewayNativeV1 && got.exact) {
				t.Fatalf("nonce %q attestation = %#v, %v", nonce, got, attestErr)
			}
		}
		assert(t, strings.Repeat("a", 64), true)
		assert(t, strings.Repeat("A", 64), false)
		assert(t, strings.Repeat("a", 63), false)
	})
}

func generatedDomainAssertOuterStableForms(t *testing.T) {
	t.Helper()
	t.Run("native_v1", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		state, journal, observation := generatedDomainNativeV1AttestationFixture(t)
		store, err := newStateStore(fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.save(state); err != nil {
			t.Fatalf("install native V1 protected state: %v", err)
		}
		digest, err := canonicalDigest(state)
		if err != nil {
			t.Fatal(err)
		}
		expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
			stateDigest: digest, nativeV1: &state}
		gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
		fixture.options.HostPort = journal.Source.LocalHostPort
		generatedDomainAssertOuterStable(t, fixture, expected, gateway)
	})

	t.Run("native_v2", func(t *testing.T) {
		predecessor := newGeneratedDomainGatewayOnlyUpgradeFixture(t)
		fixture := predecessor.identity
		source, err := predecessor.manager.store.load()
		if err != nil {
			t.Fatal(err)
		}
		state, journal := predecessor.state, predecessor.journal
		observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
		generatedDomainAdaptNativeV2Observation(state, journal, &observation)
		digest, err := canonicalDigest(state)
		if err != nil {
			t.Fatal(err)
		}
		expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV2,
			stateDigest: digest, nativeV2: &state, journal: &journal}
		gateway := generatedDomainNativeV2OuterGatewayRunner(t, state, journal, observation)
		fixture.options.HostPort = predecessor.manager.options.HostPort
		generatedDomainAssertOuterStable(t, fixture, expected, gateway)
	})

	t.Run("current_v2_upgrade_origin", func(t *testing.T) {
		predecessor := newGeneratedDomainGatewayOnlyUpgradeFixture(t)
		source, selection, current := generatedDomainUpgradeCurrentV2State(t, predecessor)
		gatewayCurrentInstallV2TestState(t, predecessor.manager.options.DataRoot, current)
		observation := gatewayV2IdentityTestObservation(t, source, predecessor.state,
			predecessor.journal, gatewayTopologyExactFinalV2)
		generatedDomainAdaptNativeV2Observation(predecessor.state, predecessor.journal, &observation)
		gateway := generatedDomainUpgradeCurrentV2OuterGatewayRunner(t, current, predecessor.state,
			predecessor.journal, observation)
		fixture := predecessor.identity
		fixture.options.HostPort = predecessor.manager.options.HostPort
		expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV2,
			stateDigest: current.Digest, nativeV1: &source, nativeV2: &predecessor.state,
			journal: &predecessor.journal, upgrade: &selection, currentV2: &current}
		generatedDomainAssertOuterStable(t, fixture, expected, gateway)

		expected.names = generatedDomainGatewayExpectedNames(expected)
		var err error
		expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
		if err != nil {
			t.Fatal(err)
		}
		descriptors, _, ok := generatedDomainGatewayDockerDescriptors(expected)
		if !ok {
			t.Fatal("upgrade-origin current V2 descriptors are unavailable")
		}
		snapshot := generatedDomainDockerStartupSnapshot{objects: generatedDomainDockerObjectsForDescriptors(descriptors)}
		for _, test := range []struct {
			name   string
			mutate func(*imageInspection)
		}{
			{name: "non_linux_image", mutate: func(image *imageInspection) { image.OS = "windows" }},
			{name: "missing_canonical_digest", mutate: func(image *imageInspection) { image.RepoDigests = nil }},
			{name: "crossed_canonical_digest", mutate: func(image *imageInspection) {
				image.RepoDigests = []string{"caddy@sha256:" + strings.Repeat("f", 64)}
			}},
		} {
			t.Run(test.name+"_has_zero_admin_or_challenge", func(t *testing.T) {
				control := generatedDomainUpgradeCurrentV2OuterGatewayRunner(t, current, predecessor.state,
					predecessor.journal, observation)
				test.mutate(&control.image)
				reader := &generatedDomainStartupReader{runner: control,
					options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
						HostPort: predecessor.manager.options.HostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
					expectation: expected}
				if got, attestErr := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot); attestErr == nil || got.exact ||
					generatedDomainTargetAdminSeen(control.commands, observation.FinalContainer.ID) ||
					generatedDomainTargetChallengeSeen(control.commands, observation.FinalContainer.ID) {
					t.Fatalf("invalid pinned image reached current V2 traffic: got=%#v err=%v commands=%#v",
						got, attestErr, control.commands)
				}
			})
		}
	})

	t.Run("current_v2_upgrade_origin_with_completed_abort_history", func(t *testing.T) {
		predecessor := newGeneratedDomainGatewayOnlyUpgradeFixture(t)
		generatedDomainAbortGatewayOnlyRebind(t, predecessor)
		aborted := predecessor.gatewayRebindPredecessorFixture
		source, selection, current := generatedDomainUpgradeCurrentV2State(t, predecessor)
		gatewayCurrentInstallV2TestState(t, aborted.manager.options.DataRoot, current)
		observation := gatewayV2IdentityTestObservation(t, source, aborted.state,
			aborted.journal, gatewayTopologyExactFinalV2)
		generatedDomainAdaptNativeV2Observation(aborted.state, aborted.journal, &observation)
		gateway := generatedDomainUpgradeCurrentV2OuterGatewayRunner(t, current, aborted.state,
			aborted.journal, observation)
		fixture := newGeneratedDomainIdentityFixtureOnDatabase(t, aborted.db, aborted.manager.options.DataRoot)
		fixture.options.HostPort = aborted.manager.options.HostPort
		expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV2,
			stateDigest: current.Digest, nativeV1: &source, nativeV2: &aborted.state,
			journal: &aborted.journal, upgrade: &selection, currentV2: &current}
		generatedDomainAssertOuterStable(t, fixture, expected, gateway)

		database, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		if err != nil || !generatedDomainCompletedRebindHistoryMatchesCurrent(database, protected) {
			t.Fatalf("completed native abort history was not bound: err=%v protected=%#v", err, protected.rebind)
		}
		t.Run("unrelated_history_refuses", func(t *testing.T) {
			crossed := database
			crossed.Gateway.Rebind.History = append([]appaccess.GatewayRebindHistoryEntry(nil),
				database.Gateway.Rebind.History...)
			crossed.Gateway.Rebind.History = append(crossed.Gateway.Rebind.History,
				database.Gateway.Rebind.History[0])
			if generatedDomainCompletedRebindHistoryMatchesCurrent(crossed, protected) {
				t.Fatal("unrelated duplicate retained history was accepted")
			}
		})
		t.Run("unresolved_history_refuses", func(t *testing.T) {
			crossed := database
			entry := database.Gateway.Rebind.History[0]
			crossed.Gateway.Rebind.Active = &entry
			if generatedDomainCompletedRebindHistoryMatchesCurrent(crossed, protected) {
				t.Fatal("active retained history was accepted as completed")
			}
		})
		t.Run("crossed_terminal_refuses", func(t *testing.T) {
			crossed := database
			crossed.Gateway.Rebind.History = append([]appaccess.GatewayRebindHistoryEntry(nil),
				database.Gateway.Rebind.History...)
			entry := crossed.Gateway.Rebind.History[0]
			entry.Commands = append([]appaccess.GatewayRebindTransitionCommand(nil), entry.Commands...)
			entry.Commands[len(entry.Commands)-1].TerminalReceiptDigest = strings.Repeat("f", 64)
			crossed.Gateway.Rebind.History[0] = entry
			if generatedDomainCompletedRebindHistoryMatchesCurrent(crossed, protected) {
				t.Fatal("crossed retained terminal was accepted")
			}
		})
	})

	t.Run("current_v2_upgrade_origin_missing_and_crossed_source_refuse", func(t *testing.T) {
		predecessor := newGeneratedDomainGatewayOnlyUpgradeFixture(t)
		source, selection, current := generatedDomainUpgradeCurrentV2State(t, predecessor)
		currentCandidate := generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV2,
			stateDigest: current.Digest, currentV2: &current}
		if _, valid := generatedDomainCollapseCurrentV2Sources([]generatedDomainGatewayExpectation{currentCandidate}); valid {
			t.Fatal("current V2 upgrade projection without its exact source was selected")
		}
		crossed := newGeneratedDomainGatewayOnlyUpgradeFixture(t)
		crossedSource, crossedSelection, _ := generatedDomainUpgradeCurrentV2State(t, crossed)
		crossedCandidate := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV2,
			nativeV1: &crossedSource, nativeV2: &crossed.state, journal: &crossed.journal,
			upgrade: &crossedSelection}
		if _, valid := generatedDomainCollapseCurrentV2Sources(
			[]generatedDomainGatewayExpectation{crossedCandidate, currentCandidate}); valid {
			t.Fatal("current V2 upgrade projection selected a crossed protected source")
		}
		exactCandidate := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV2,
			nativeV1: &source, nativeV2: &predecessor.state, journal: &predecessor.journal,
			upgrade: &selection}
		collapsed, valid := generatedDomainCollapseCurrentV2Sources(
			[]generatedDomainGatewayExpectation{exactCandidate, currentCandidate})
		if !valid || len(collapsed) != 1 || collapsed[0].currentV2 == nil ||
			!generatedDomainCurrentV2UpgradeExpectationValid(collapsed[0]) {
			t.Fatalf("exact current V2 upgrade projection was not selected: %#v, valid=%v", collapsed, valid)
		}
	})

	for _, v2 := range []bool{false, true} {
		name := "current_v1"
		if v2 {
			name = "current_v2"
		}
		t.Run(name, func(t *testing.T) {
			predecessor := newGeneratedDomainGatewayOnlyUpgradeFixture(t)
			selection := generatedDomainCommitGatewayOnlyRebind(t, predecessor)
			current := generatedDomainCurrentAttestationFixtureFromSelection(t, selection,
				predecessor.manager.options.HostPort)
			if v2 {
				gatewayCurrentInstallV2TestState(t, predecessor.manager.options.DataRoot, current.stateV2)
			}
			fixture := predecessor.identity
			fixture.options.HostPort = predecessor.manager.options.HostPort
			expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV1,
				stateDigest: current.stateV1.Digest, currentV1: &current.stateV1, terminal: &current.terminal}
			if v2 {
				expected = generatedDomainGatewayExpectation{kind: generatedDomainGatewayCurrentV2,
					stateDigest: current.stateV2.Digest, currentV2: &current.stateV2, terminal: &current.terminal}
			}
			gateway := generatedDomainCurrentOuterGatewayRunner(t, current, v2)
			generatedDomainAssertOuterStable(t, fixture, expected, gateway)
		})
	}
}

func generatedDomainAssertOuterStable(t *testing.T, fixture generatedDomainIdentityFixture,
	expected generatedDomainGatewayExpectation, gateway *generatedDomainGatewayAttestationRunner,
) {
	t.Helper()
	expected.names = generatedDomainGatewayExpectedNames(expected)
	ids, err := generatedDomainGatewayExpectedIDs(expected)
	if err != nil {
		t.Fatal(err)
	}
	expected.ids = ids
	descriptors, expectedImageID, ok := generatedDomainGatewayDockerDescriptors(expected)
	if !ok {
		t.Fatal("gateway-only expectation has no exact Docker descriptor set")
	}
	if expectedImageID != "" && normalizeID(gateway.image.ID) != normalizeID(expectedImageID) {
		t.Fatal("gateway-only expectation and physical image differ")
	}
	objects := generatedDomainDockerObjectsForDescriptors(descriptors)
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = gateway.container.ID
		case caddyNetworkName:
			objects[index].id = gateway.network.ID
		}
	}
	runner := &generatedDomainRecoveryCensusRunner{daemonID: "generated-domain-stable-daemon",
		objects: objects, imageID: gateway.image.ID, gateway: gateway}
	fixture.options.CommandTimeout = time.Minute
	fixture.options.OutputLimit = 1 << 20
	got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
	if err != nil || got.disposition != generatedDomainStartupStable ||
		got.reason != generatedDomainStartupReasonStable || len(got.recovery) != 0 {
		database, databaseErr := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		protected, protectedErr := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		resolved, resolveErr := resolveGeneratedDomainGatewayExpectation(database, protected)
		recovery := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
		census, censusErr := resolveGeneratedDomainDockerCensus(protected)
		reader, readerErr := newGeneratedDomainStartupReader(runner, fixture.options, runner.daemonID)
		var docker generatedDomainDockerStartupSnapshot
		var dockerErr error
		var classified generatedDomainStartupClassification
		var classifyErr error
		stable := false
		var recoveryValues []generatedDomainStartupRecovery
		validRecoveries := false
		diagnostic := "not-run"
		if readerErr == nil && resolveErr == nil && censusErr == nil {
			reader.expectation = generatedDomainStartupObservationGateway(resolved, recovery)
			reader.recovery, reader.census = recovery, census
			docker, dockerErr = readGeneratedDomainDockerStartup(context.Background(), reader)
			if dockerErr == nil {
				classified, classifyErr = classifyGeneratedDomainStartup(database, protected, docker)
				stable = generatedDomainStartupStableGateway(database, protected, docker)
				recoveryValues, validRecoveries = generatedDomainStartupRecoveries(database, protected, docker)
			}
		}
		diagnostic = generatedDomainDiagnoseOuterStable(t, fixture, runner)
		t.Fatalf("sealed outer stable = %#v, %v; database=%v protected=%v resolve=%v kind=%d reader=%v census=%v docker=%#v/%v classified=%#v/%v stable=%t recoveries=%#v/%t current=%#v diagnostic=%s",
			got, err, databaseErr, protectedErr, resolveErr, resolved.kind, readerErr, censusErr, docker, dockerErr,
			classified, classifyErr, stable, recoveryValues, validRecoveries, protected.currentV1, diagnostic)
	}
	if !generatedDomainGatewayCommandsInSafeOrder(gateway.commands) {
		t.Fatalf("sealed outer passive read order = %#v", gateway.commands)
	}
}

func generatedDomainDiagnoseOuterStable(t *testing.T, fixture generatedDomainIdentityFixture,
	runner *generatedDomainRecoveryCensusRunner,
) string {
	t.Helper()
	options, ok := normalizeGeneratedDomainOptions(fixture.options)
	if !ok {
		return "normalize"
	}
	ctx, cancel := context.WithTimeout(context.Background(), generatedDomainStartupObservationTimeout)
	defer cancel()
	stores := *fixture.stores
	identity, paths, err := inspectGeneratedDomainPhysicalIdentity(ctx, &stores, runner, options)
	if err != nil {
		return "identity: " + err.Error()
	}
	if _, err := paths.recheck(false); err != nil {
		return "initial paths: " + err.Error()
	}
	reader, err := newGeneratedDomainStartupReader(runner, options, identity.manager.daemonID)
	if err != nil {
		return "reader: " + err.Error()
	}
	db1, err := stores.GeneratedDomainStartupSnapshot(ctx)
	if err != nil {
		return "database1: " + err.Error()
	}
	protected1, err := readGeneratedDomainProtectedStartup(ctx, options.DataRoot)
	if err != nil {
		return "protected1: " + err.Error()
	}
	expected1, err := resolveGeneratedDomainGatewayExpectation(db1, protected1)
	if err != nil {
		return "expectation1: " + err.Error()
	}
	recovery1 := resolveGeneratedDomainRecoveryDockerExpectation(db1, protected1)
	census1, err := resolveGeneratedDomainDockerCensus(protected1)
	if err != nil {
		return "census1: " + err.Error()
	}
	reader.expectation = generatedDomainStartupObservationGateway(expected1, recovery1)
	reader.recovery, reader.census = recovery1, census1
	docker1, err := readGeneratedDomainDockerStartup(ctx, reader)
	if err != nil {
		return "docker1: " + err.Error()
	}
	db2, err := stores.GeneratedDomainStartupSnapshot(ctx)
	if err != nil {
		return "database2: " + err.Error()
	}
	protected2, err := readGeneratedDomainProtectedStartup(ctx, options.DataRoot)
	if err != nil {
		return "protected2: " + err.Error()
	}
	expected2, expectationErr := resolveGeneratedDomainGatewayExpectation(db2, protected2)
	recovery2 := resolveGeneratedDomainRecoveryDockerExpectation(db2, protected2)
	census2, censusErr := resolveGeneratedDomainDockerCensus(protected2)
	if expectationErr != nil || censusErr != nil || !reflect.DeepEqual(expected1, expected2) ||
		!reflect.DeepEqual(recovery1, recovery2) || !reflect.DeepEqual(census1, census2) {
		return "resolved observations changed"
	}
	reader.expectation = generatedDomainStartupObservationGateway(expected2, recovery2)
	reader.recovery, reader.census = recovery2, census2
	docker2, err := readGeneratedDomainDockerStartup(ctx, reader)
	if err != nil {
		return "docker2: " + err.Error()
	}
	if !reflect.DeepEqual(db1, db2) {
		return "database changed"
	}
	if !sameGeneratedDomainProtectedStartup(protected1, protected2) {
		return "protected changed"
	}
	if !sameGeneratedDomainDockerStartup(docker1, docker2) {
		return "docker changed"
	}
	if _, err := paths.recheck(false); err != nil {
		return "final paths: " + err.Error()
	}
	return "passed repeated observation"
}

func generatedDomainNativeV1OuterGatewayRunner(observation gatewayV2DockerObservation) *generatedDomainGatewayAttestationRunner {
	return &generatedDomainGatewayAttestationRunner{
		image: observation.Image,
		container: gatewayContainerInspection{caddyInspection: observation.V1Container,
			gatewayContainerRuntime: observation.V1Runtime},
		volumes: map[string]gatewayVolumeIdentityInspection{observation.V1Volume.Name: {
			Name: observation.V1Volume.Name, Driver: observation.V1Volume.Driver, Scope: observation.V1Volume.Scope,
			Options: observation.V1Volume.Options, Labels: observation.V1Volume.Labels,
			Mountpoint: observation.V1VolumeIdentity.Mountpoint, CreatedAt: observation.V1VolumeIdentity.CreatedAt,
		}},
		network: gatewayNetworkIdentityInspection{ID: observation.V1NetworkID,
			Name: observation.V1Network.Name, Driver: observation.V1Network.Driver, Scope: observation.V1Network.Scope,
			Internal: observation.V1Network.Internal, Options: observation.V1Network.Options,
			IPAM: observation.V1Network.IPAM, Labels: observation.V1Network.Labels,
			Containers: observation.V1Network.Containers},
		restartName: gatewayV2ActiveConfigFile, restart: observation.V1RestartConfig, live: observation.V1Config,
	}
}

func generatedDomainNativeV2OuterGatewayRunner(t *testing.T, state gatewayV2RouteState,
	journal gatewayMigrationJournal, observation gatewayV2DockerObservation,
) *generatedDomainGatewayAttestationRunner {
	t.Helper()
	token, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	return &generatedDomainGatewayAttestationRunner{
		image: observation.Image,
		container: gatewayContainerInspection{caddyInspection: observation.FinalContainer,
			gatewayContainerRuntime: observation.FinalRuntime},
		volumes: map[string]gatewayVolumeIdentityInspection{
			observation.ConfigVolume.Name: generatedDomainGatewayVolumeIdentity(observation.ConfigVolume, observation.ConfigVolumeIdentity),
			observation.DataVolume.Name:   generatedDomainGatewayVolumeIdentity(observation.DataVolume, observation.DataVolumeIdentity),
		},
		network: gatewayNetworkIdentityInspection{ID: observation.IngressNetworkID,
			Name: observation.IngressNetwork.Name, Driver: observation.IngressNetwork.Driver,
			Scope: observation.IngressNetwork.Scope, Internal: observation.IngressNetwork.Internal,
			Options: observation.IngressNetwork.Options, IPAM: observation.IngressNetwork.IPAM,
			Labels: observation.IngressNetwork.Labels, Containers: observation.IngressNetwork.Containers},
		restartName: state.Identity.ActiveConfigFilename, restart: observation.FinalRestartConfig,
		live:      observation.FinalConfig,
		challenge: gatewayV2ChallengeBodyPrefix + gatewayV2PortChallenge(token, state.Profile.PortStart),
	}
}

func generatedDomainAdaptNativeV2Observation(state gatewayV2RouteState, journal gatewayMigrationJournal,
	observation *gatewayV2DockerObservation,
) {
	observation.Image.ID = "sha256:" + normalizeID(journal.Resources.ImageID)
	observation.FinalContainer.ID = journal.Resources.FinalContainerID
	observation.FinalContainer.Image = observation.Image.ID
	observation.IngressNetworkID = journal.Resources.IngressNetworkID
	var ingressContainer caddyNetworkContainerInspection
	for key, value := range observation.IngressNetwork.Containers {
		delete(observation.IngressNetwork.Containers, key)
		ingressContainer = value
	}
	observation.IngressNetwork.Containers[observation.FinalContainer.ID] = ingressContainer
	for name, configured := range observation.FinalRuntime.ConfiguredNetworks {
		if name == state.Identity.IngressNetwork {
			configured.NetworkID = journal.Resources.IngressNetworkID
		}
		observation.FinalRuntime.ConfiguredNetworks[name] = configured
	}
	observation.ConfigVolumeIdentity = gatewayV1VolumeIdentity{
		Mountpoint: journal.Resources.ConfigVolume.Mountpoint, CreatedAt: journal.Resources.ConfigVolume.CreatedAt}
	observation.DataVolumeIdentity = gatewayV1VolumeIdentity{
		Mountpoint: journal.Resources.DataVolume.Mountpoint, CreatedAt: journal.Resources.DataVolume.CreatedAt}
}

type generatedDomainGatewayOnlyUpgradeFixture struct {
	gatewayRebindPredecessorFixture
	identity generatedDomainIdentityFixture
}

func newGeneratedDomainGatewayOnlyUpgradeFixture(t *testing.T) generatedDomainGatewayOnlyUpgradeFixture {
	return newGeneratedDomainGatewayOnlyUpgradeFixtureAtPhase(t, gatewayPhaseCommitted)
}

func newGeneratedDomainGatewayOnlyUpgradeFixtureAtPhase(t *testing.T,
	target gatewayMigrationPhase,
) generatedDomainGatewayOnlyUpgradeFixture {
	return newGeneratedDomainUpgradeFixtureAtPhase(t, target, false)
}

func newGeneratedDomainRetainedAppUpgradeFixture(t *testing.T) generatedDomainGatewayOnlyUpgradeFixture {
	return newGeneratedDomainUpgradeFixtureAtPhase(t, gatewayPhaseCommitted, true)
}

func newGeneratedDomainUpgradeFixtureAtPhase(t *testing.T, target gatewayMigrationPhase,
	retainedApp bool,
) generatedDomainGatewayOnlyUpgradeFixture {
	t.Helper()
	if target != gatewayPhasePrepared && target != gatewayPhaseCommitted {
		t.Fatalf("unsupported gateway-only upgrade fixture phase %q", target)
	}
	identity := newGeneratedDomainIdentityFixture(t)
	runner := &ingressRunner{files: map[string][]byte{}}
	manager, err := newManager(runner, Options{
		DockerExecutable:             identity.options.DockerExecutable,
		DockerEndpoint:               identity.options.DockerEndpoint,
		DockerConfigDirectory:        identity.options.DockerConfigDirectory,
		WorkingDirectory:             identity.options.WorkingDirectory,
		DataRoot:                     identity.dataRoot,
		HostPort:                     identity.options.HostPort,
		CommandTimeout:               identity.options.CommandTimeout,
		PullTimeout:                  identity.options.PullTimeout,
		OutputLimit:                  identity.options.OutputLimit,
		RebindFenceCheck:             func(context.Context) error { return nil },
		RebindCurrentStateRepository: gatewayCurrentSelectionRepositoryFunc(emptyStartupPresenceRepository),
	})
	if err != nil {
		t.Fatal(err)
	}
	db := identity.database
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'startup-admin','hash','administrator',datetime('now'),datetime('now'))`,
		gatewayRebindTestAdministrator); err != nil {
		t.Fatal(err)
	}
	repository := appaccess.New(db)
	ctx := context.Background()
	profileSpec := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.96.8",
		InterfaceID: "startup-gateway-only", PortStart: 8100, PortEnd: 8119}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := repository.ConfigureGatewayProfile(ctx, appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), Spec: profileSpec,
		Approval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: profileDigest, ActorID: gatewayRebindTestAdministrator},
	})
	if err != nil {
		t.Fatal(err)
	}
	upgradeOperationID := uuid.NewString()
	source := routeState{Version: stateVersion, Active: map[string]routeRecord{}}
	var installRetainedLAN func(*gatewayUpgradeStateStore, gatewayMigrationJournal, gatewayV2RouteState) gatewayV2RouteState
	if retainedApp {
		migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
		appID := migration.guarded.appID
		allocation, _, err := repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
			AppID: appID, OperationID: uuid.NewString(), GatewayProfileRevisionID: profile.ID,
			GatewayProfileRevisionNumber: profile.RevisionNumber,
		})
		if err != nil {
			t.Fatal(err)
		}
		accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
		if err != nil {
			t.Fatal(err)
		}
		revision, _, err := repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{
			AppID: appID, OperationID: allocation.OwnerOperationID, AllocationID: allocation.ID,
			Approval: appaccess.Approval{Action: appaccess.ActionEnableAppAccess,
				SpecDigest: accessDigest, ActorID: gatewayRebindTestAdministrator},
		})
		if err != nil {
			t.Fatal(err)
		}
		grant, _, err := repository.ClaimAppAccessGrant(ctx, appaccess.ClaimAppAccessGrantInput{
			AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, profile),
			ActorID: gatewayRebindTestAdministrator,
		})
		if err != nil {
			t.Fatal(err)
		}
		grantOwner := appaccess.AppAccessGrantClaimOwnerFor(grant)
		if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, grantOwner,
			appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, grantOwner,
			appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.ResolveAppAccessGrantClaim(ctx, grantOwner,
			appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted, appaccess.AppAccessGrantProof{
				GatewayOperationID: upgradeOperationID, ProtectedStateDigest: strings.Repeat("a", 64),
			}); err != nil {
			t.Fatal(err)
		}
		active, activeEndpoint := seedGatewayRebindPredecessorRuntime(t, manager, runner, migration)
		source.Active[appID] = routeRecord{Slot: generatedidentity.Slot(active.Slot),
			Endpoints: []generatedroute.RouteEndpoint{activeEndpoint}}
		installRetainedLAN = func(store *gatewayUpgradeStateStore, journal gatewayMigrationJournal,
			state gatewayV2RouteState,
		) gatewayV2RouteState {
			binding, err := gatewayV2LANBindingForRequest(gatewayV2LANGrantRequest{
				AttemptID: grant.AttemptID, ClaimRequestDigest: grant.RequestDigest, AppID: appID,
				AllocationID: revision.Allocation.ID, OwnerOperationID: revision.OperationID,
				Port: revision.Allocation.Port, AccessRevisionID: revision.ID,
				AccessRevisionNumber: revision.RevisionNumber, AccessSpecDigest: revision.SpecDigest,
				ApprovedBy: grant.Spec.ApprovedBy, GatewayProfileRevisionID: profile.ID,
				GatewayProfileRevisionNumber: profile.RevisionNumber,
				GatewayProfileSpecDigest:     profile.SpecDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			previous := cloneGatewayV2AppRoute(state.Apps[appID])
			proposed := cloneGatewayV2AppRoute(previous)
			proposed.LAN = &binding
			pending := cloneGatewayV2RouteState(state)
			pending.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingLANGrant, AppID: appID,
				Previous: &previous, Proposed: proposed}
			if err := store.saveCommittedV2State(pending, journal); err != nil {
				t.Fatal(err)
			}
			state = cloneGatewayV2RouteState(pending)
			state.Pending = nil
			state.Apps[appID] = proposed
			if err := store.saveCommittedV2State(state, journal); err != nil {
				t.Fatal(err)
			}
			return state
		}
	}
	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{ProfileRevisionID: profile.ID,
		ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(ctx, appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: upgradeOperationID, Spec: upgradeSpec,
		Approval: appaccess.Approval{Action: appaccess.ActionUpgradeGateway,
			SpecDigest: upgradeDigest, ActorID: gatewayRebindTestAdministrator},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := appaccess.GatewayProfileUpgradeClaimOwner{OperationID: upgrade.OperationID,
		ProfileRevisionID: upgrade.ProfileRevisionID, ProfileRevisionNumber: upgrade.ProfileRevisionNumber}
	if target == gatewayPhaseCommitted {
		if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner,
			appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeServing); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner,
			appaccess.GatewayProfileUpgradeServing, appaccess.GatewayProfileUpgradeCommitted); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	profileBinding := gatewayProfileBinding{RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber,
		SpecDigest: profile.SpecDigest, SelectedIPv4: profile.Spec.SelectedIPv4,
		InterfaceID: profile.Spec.InterfaceID, PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd}
	identityDigest, err := gatewayV1ObservedIdentityDigest(gatewayV2DockerObservation{
		Image: gatewayV2IdentityTestImage(), V1Container: caddyInspection{ID: "sha256:" + strings.Repeat("b", 64)},
		V1NetworkID: "sha256:" + strings.Repeat("e", 64),
		V1VolumeIdentity: gatewayV1VolumeIdentity{
			Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data",
			CreatedAt:  "2026-09-29T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, gatewayUpgradePreparation{
		OperationID: upgrade.OperationID, Profile: profileBinding,
		Network:              gatewayV2NetworkPlan{Subnet: "10.241.0.0/28", GatewayIPv4: "10.241.0.1", ContainerIPv4: "10.241.0.2"},
		SourceIdentityDigest: identityDigest, LocalHostPort: manager.options.HostPort,
		ApprovedBy: upgrade.ApprovedBy, ApprovedActionDigest: upgradeDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	if target == gatewayPhaseCommitted {
		phase := gatewayPhasePrepared
		for _, next := range []gatewayMigrationPhase{gatewayPhaseStageIntent, gatewayPhaseStaged,
			gatewayPhaseTransferIntent, gatewayPhaseV2Serving, gatewayPhaseCommitted} {
			if next == gatewayPhaseStaged {
				bindUpgradeStageResources(t, store, upgrade.OperationID)
			}
			if next == gatewayPhaseV2Serving {
				bindUpgradeFinalResource(t, store, upgrade.OperationID)
			}
			journal, err = store.transitionMigrationJournal(upgrade.OperationID, phase, next)
			if err != nil {
				t.Fatal(err)
			}
			phase = next
		}
	}
	state, journal, err = store.loadBoundUpgrade(upgrade.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if installRetainedLAN != nil {
		state = installRetainedLAN(store, journal, state)
		state, journal, err = store.loadBoundUpgrade(upgrade.OperationID)
		if err != nil {
			t.Fatal(err)
		}
	}
	rosterDigest, err := appaccess.GatewayRebindRosterDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	successor := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.97.8",
		InterfaceID: "startup-current", PortStart: 8100, PortEnd: 8119}
	spec := appaccess.GatewayRebindSpec{OperationID: uuid.NewString(),
		PredecessorProfileRevisionID: profile.ID, PredecessorProfileRevisionNumber: profile.RevisionNumber,
		PredecessorProfileSpecDigest: profile.SpecDigest, PredecessorUpgradeOperationID: upgrade.OperationID,
		PredecessorProtectedIdentityDigest: state.Identity.Digest,
		SuccessorProfileRevisionID:         uuid.NewString(), SuccessorProfileRevisionNumber: profile.RevisionNumber + 1,
		SuccessorProfileOperationID: uuid.NewString(), SuccessorProfile: successor,
		RosterDigest: rosterDigest, RosterCount: 0}
	specDigest, err := appaccess.GatewayRebindSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	successorDigest, err := appaccess.GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	predecessor := gatewayRebindPredecessorFixture{manager: manager, store: store, repository: repository,
		db: db, runner: runner, state: state, journal: journal,
		proposal: appaccess.GatewayRebindPreclaimProposal{Spec: spec,
			RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
				SpecDigest: specDigest, ActorID: gatewayRebindTestAdministrator},
			ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
				SpecDigest: successorDigest, ActorID: gatewayRebindTestAdministrator}},
	}
	return generatedDomainGatewayOnlyUpgradeFixture{gatewayRebindPredecessorFixture: predecessor, identity: identity}
}

func generatedDomainCommitGatewayOnlyRebind(t *testing.T, fixture generatedDomainGatewayOnlyUpgradeFixture) gatewayCurrentSelection {
	t.Helper()
	fixture.manager.options.RebindFenceCheck = fixture.repository.CheckGatewayRebindFence
	fixture.manager.options.RebindCurrentStateRepository = fixture.repository
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	input := GatewayRebindProposalInput{OperationID: fixture.proposal.Spec.OperationID,
		SuccessorProfileRevisionID:     fixture.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: fixture.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    fixture.proposal.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               fixture.proposal.Spec.SuccessorProfile}
	inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), fixture.repository, input)
	if err != nil || len(inspection.Roster) != 0 || len(inspection.RuntimeHeads) != 0 {
		t.Fatalf("inspect gateway-only rebind = %#v, %v", inspection, err)
	}
	commitInput := gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator}}
	fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindTypedAuthorizationNetworkObservation(t, claim)
	}
	template := newGatewayCurrentStateFixture(t)
	driver := &gatewayRebindBoundedPhysicalDriver{t: t, template: template}
	result, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository, commitInput, driver)
	if err != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit {
		history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		t.Fatalf("commit gateway-only rebind = %#v, %v; calls=%d/%d/%d/%d history=%d/%d/%d/%d historyErr=%v snapshot=%#v snapshotErr=%v",
			result, err, driver.commitCalls, driver.attestCalls, driver.abortCalls, driver.withdrawCalls,
			len(history.Checkpoints), len(history.IntentsV2), len(history.Progress), len(history.TerminalsV2),
			historyErr, snapshot, snapshotErr)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := fixture.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil || selection.State == nil || selection.Terminal == nil || len(selection.State.Apps) != 0 {
		t.Fatalf("select gateway-only current = %#v, %v", selection, err)
	}
	return selection
}

func generatedDomainAbortGatewayOnlyRebind(t *testing.T, fixture generatedDomainGatewayOnlyUpgradeFixture) {
	t.Helper()
	fixture.manager.options.RebindFenceCheck = fixture.repository.CheckGatewayRebindFence
	fixture.manager.options.RebindCurrentStateRepository = fixture.repository
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	input := GatewayRebindProposalInput{OperationID: fixture.proposal.Spec.OperationID,
		SuccessorProfileRevisionID:     fixture.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: fixture.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    fixture.proposal.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               fixture.proposal.Spec.SuccessorProfile}
	inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), fixture.repository, input)
	if err != nil || len(inspection.Roster) != 0 || len(inspection.RuntimeHeads) != 0 {
		t.Fatalf("inspect gateway-only abort = %#v, %v", inspection, err)
	}
	commitInput := gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator}}
	fixture.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindTypedAuthorizationNetworkObservation(t, claim)
	}
	fixture.manager.gatewayRebindAfterPreparedCheckpoint = func(context.Context) error {
		return errors.New("injected gateway-only no-effect abort")
	}
	driver := &gatewayRebindBoundedPhysicalDriver{t: t, template: newGatewayCurrentStateFixture(t)}
	result, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository, commitInput, driver)
	if err != nil || result.FinalPhase != appaccess.GatewayRebindRolledBack ||
		result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased {
		t.Fatalf("abort gateway-only rebind = %#v, %v", result, err)
	}
	snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if snapshotErr != nil || historyErr != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		len(history.TerminalsV2) != 1 || history.TerminalsV2[0].Receipt.NoEffectProof == nil ||
		len(history.IntentsV2) != 0 || len(history.Progress) != 0 {
		t.Fatalf("gateway-only retained abort snapshot=%#v history=%d/%d/%d errors=%v/%v",
			snapshot, len(history.IntentsV2), len(history.Progress), len(history.TerminalsV2), snapshotErr, historyErr)
	}
}

func generatedDomainCurrentAttestationFixtureFromSelection(t *testing.T, selection gatewayCurrentSelection,
	hostPort uint16,
) generatedDomainCurrentAttestationFixture {
	t.Helper()
	state := cloneGatewayCurrentRouteState(*selection.State)
	terminal := *selection.Terminal
	stateV2 := gatewayCurrentRouteStateV2{Version: gatewayCurrentRouteStateVersionV2, Revision: 1,
		Lineage: state.Lineage, Profile: state.Profile, Identity: state.Identity, Network: state.Network,
		TransferManifestDigest: state.TransferManifestDigest,
		SourceV1:               &gatewayCurrentRouteSourceV1{Revision: state.Revision, Digest: state.Digest},
		Apps:                   map[string]gatewayCurrentAppRouteV2{}}
	var err error
	stateV2.Digest, err = gatewayCurrentRouteStateDigestV2(stateV2)
	if err != nil || !validGatewayCurrentRouteStateV2(stateV2) {
		t.Fatalf("gateway-only current V2 projection: %v", err)
	}
	target := gatewayCurrentPhysicalTarget{State: state, Lineage: state.Lineage, Terminal: terminal,
		Identity: state.Identity, Resources: terminal.Resources}
	container, runtime := gatewayCurrentPhysicalRuntimeTestContainer(target, hostPort, true)
	facts := gatewayFinalOwnershipFacts{Lineage: state.Lineage, Terminal: terminal,
		Network: state.Network, LocalHostPort: hostPort}
	labels := gatewayFinalOwnershipLabels(facts, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole)
	network := gatewayNetworkIdentityInspection{ID: terminal.Resources.IngressNetwork.ID,
		Name: terminal.SuccessorIdentity.IngressNetwork, Driver: "bridge", Scope: "local",
		Options: map[string]string{gatewayRebindBridgeNameOptionKey: "rig" + terminal.SuccessorIdentity.Digest[:12]},
		IPAM:    caddyNetworkIPAM{Config: []networkIPAM{{Subnet: state.Network.Subnet, Gateway: state.Network.GatewayIPv4}}},
		Labels:  labels, Containers: map[string]caddyNetworkContainerInspection{container.ID: {
			Name: terminal.SuccessorIdentity.FinalContainer, IPv4Address: state.Network.ContainerIPv4 + "/28"}}}
	config, data := terminal.Resources.ConfigVolume, terminal.Resources.DataVolume
	volumes := map[string]gatewayVolumeIdentityInspection{
		config.Name: {Name: config.Name, Driver: "local", Scope: "local", Options: map[string]string{},
			Labels:     gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole),
			Mountpoint: config.Mountpoint, CreatedAt: config.CreatedAt},
		data.Name: {Name: data.Name, Driver: "local", Scope: "local", Options: map[string]string{},
			Labels:     gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole),
			Mountpoint: data.Mountpoint, CreatedAt: data.CreatedAt},
	}
	return generatedDomainCurrentAttestationFixture{stateV1: state, stateV2: stateV2, terminal: terminal,
		image: imageInspection{ID: "sha256:" + terminal.Resources.ImageID, OS: "linux",
			RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}},
		container: container, runtime: runtime, volumes: volumes, network: network}
}

func generatedDomainCurrentOuterGatewayRunner(t *testing.T, fixture generatedDomainCurrentAttestationFixture,
	v2 bool,
) *generatedDomainGatewayAttestationRunner {
	t.Helper()
	var wanted []byte
	var token string
	var err error
	if v2 {
		wanted, err = gatewayCurrentPhysicalConfigBytesV2(fixture.stateV2)
		token, _ = gatewayCurrentPhysicalProbeTokenV2(fixture.stateV2)
	} else {
		wanted, err = gatewayCurrentPhysicalConfigBytes(fixture.stateV1)
		token, _ = gatewayCurrentPhysicalProbeToken(fixture.stateV1)
	}
	if err != nil || !validSHA256(token) {
		t.Fatalf("gateway-only current config: %v", err)
	}
	return &generatedDomainGatewayAttestationRunner{image: fixture.image,
		container: gatewayContainerInspection{caddyInspection: fixture.container,
			gatewayContainerRuntime: fixture.runtime},
		volumes: fixture.volumes, network: fixture.network,
		restartName: fixture.stateV1.Identity.Rebind.ActiveConfigFilename,
		restart:     wanted, live: wanted,
		challenge: gatewayV2ChallengeBodyPrefix + gatewayV2PortChallenge(token, fixture.stateV1.Profile.PortStart)}
}

func generatedDomainUpgradeCurrentV2State(t *testing.T,
	fixture generatedDomainGatewayOnlyUpgradeFixture,
) (routeState, gatewayUpgradeGenerationSelection, gatewayCurrentRouteStateV2) {
	t.Helper()
	source, err := fixture.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	upgrade := gatewayUpgradeGenerationSelection{Store: fixture.store, Generation: 0,
		State: fixture.state, Journal: fixture.journal, Existing: true, operationID: fixture.state.OperationID}
	lineage, err := gatewayUpgradeCurrentLineage(upgrade)
	if err != nil {
		t.Fatal(err)
	}
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: lineage,
		Upgrade: &upgrade, UpgradeSource: &source}
	current, err := gatewayCurrentV2BaselineFor(selection, gatewayCurrentV2PromotionAuthority{})
	if err != nil || len(current.Apps) != 0 || current.SourceUpgrade == nil || current.SourceV1 != nil {
		t.Fatalf("build gateway-only upgrade-origin current V2 state = %#v, %v", current, err)
	}
	return source, upgrade, current
}

func generatedDomainUpgradeCurrentV2OuterGatewayRunner(t *testing.T, current gatewayCurrentRouteStateV2,
	source gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation,
) *generatedDomainGatewayAttestationRunner {
	t.Helper()
	runner := generatedDomainNativeV2OuterGatewayRunner(t, source, journal, observation)
	wanted, err := gatewayCurrentPhysicalConfigBytesV2(current)
	token, tokenErr := gatewayCurrentPhysicalProbeTokenV2(current)
	if err != nil || tokenErr != nil || !validSHA256(token) {
		t.Fatalf("upgrade-origin current V2 config: configError=%v tokenError=%v", err, tokenErr)
	}
	runner.restartName = source.Identity.ActiveConfigFilename
	runner.restart, runner.live = append([]byte(nil), wanted...), append([]byte(nil), wanted...)
	runner.challenge = gatewayV2ChallengeBodyPrefix + gatewayV2PortChallenge(token, current.Profile.PortStart)
	return runner
}

func generatedDomainAssertRetainedAppCompleteGatewayNoTraffic(t *testing.T) {
	t.Helper()
	retained := newGeneratedDomainRetainedAppUpgradeFixture(t)
	generatedDomainAssertRetainedAppCompleteGatewayNoTrafficForFixture(t,
		retained.gatewayRebindPredecessorFixture, retained.identity, false)
}

func generatedDomainAssertRetainedAppCompleteGatewayNoTrafficForFixture(t *testing.T,
	predecessor gatewayRebindPredecessorFixture, fixture generatedDomainIdentityFixture, requireLAN bool,
) {
	t.Helper()
	fixture.options.HostPort = predecessor.manager.options.HostPort
	fixture.options.CommandTimeout = time.Minute
	fixture.options.OutputLimit = 1 << 20
	source, err := predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	state, journal := predecessor.state, predecessor.journal
	if len(state.Apps) == 0 {
		t.Fatal("retained application fixture has an empty roster")
	}
	if requireLAN {
		for _, app := range state.Apps {
			if app.LAN == nil {
				t.Fatal("complete-census LAN fixture has no assigned application")
			}
		}
	}
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	generatedDomainAdaptNativeV2Observation(state, journal, &observation)
	gateway := generatedDomainNativeV2OuterGatewayRunner(t, state, journal, observation)
	descriptors := []generatedDomainRecoveryDockerDescriptor{
		generatedDomainGatewayV2ContainerDescriptor(state, journal, gatewayV2FinalContainerRole,
			journal.Resources.FinalContainerID),
		generatedDomainGatewayV2NetworkDescriptor(state, journal),
		generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2ConfigVolumeRole,
			journal.Resources.ConfigVolume),
		generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2DataVolumeRole,
			journal.Resources.DataVolume),
	}
	runner := &generatedDomainRecoveryCensusRunner{daemonID: "generated-domain-retained-app-daemon",
		objects: generatedDomainDockerObjectsForDescriptors(descriptors),
		imageID: observation.Image.ID, gateway: gateway}
	got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
	if err != nil || got.disposition != generatedDomainStartupRefused ||
		got.reason != generatedDomainStartupReasonColdAccounting {
		database, databaseErr := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		protected, protectedErr := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		resolved, resolveErr := resolveGeneratedDomainGatewayExpectation(database, protected)
		recovery := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
		census, censusErr := resolveGeneratedDomainDockerCensus(protected)
		t.Fatalf("retained app with complete gateway census = %#v, %v; databaseErr=%v protectedErr=%v resolved=%#v resolveErr=%v recovery=%#v census=%#v censusErr=%v commands=%#v",
			got, err, databaseErr, protectedErr, resolved, resolveErr, recovery, census, censusErr, gateway.commands)
	}
	generatedDomainAssertNoOuterExec(t, gateway.commands)
}

type generatedDomainImageListRunner struct {
	body []byte
	err  error
}

func (r *generatedDomainImageListRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if !reflect.DeepEqual(request.Args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"}) {
		return runtimeprocess.CommandResult{}, errors.New("unexpected image census command")
	}
	return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.body...)}, r.err
}

func TestGeneratedDomainDockerStartupCensusCannotHideResources(t *testing.T) {
	reader := &generatedDomainStartupReader{}
	classifySelected := func(t *testing.T, object generatedDomainDockerObject) {
		t.Helper()
		got, err := classifyGeneratedDomainStartup(deployments.GeneratedDomainStartupSnapshot{},
			generatedDomainProtectedStartupSnapshot{effectEmpty: true},
			generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{object}})
		if err != nil || got.disposition != generatedDomainStartupRefused || got.reason != generatedDomainStartupReasonAmbiguous {
			t.Fatalf("selected foreign object classified as %#v, %v", got, err)
		}
	}

	t.Run("renamed_builder_without_database_parent", func(t *testing.T) {
		labels := map[string]string{"rig.controller": "generated-builder", "rig.builder": "builder-a",
			"rig.network": "network-a", "rig.quota.bytes": "1048576"}
		if !reader.generatedDomainDockerObjectSelected("container", strings.Repeat("a", 64), "renamed-foreign", labels) {
			t.Fatal("renamed builder was filtered out before accounting")
		}
		classifySelected(t, generatedDomainDockerObject{kind: "container", id: strings.Repeat("a", 64),
			name: "renamed-foreign", labels: labels})
	})

	t.Run("partial_wrong_and_crossed_builder_labels", func(t *testing.T) {
		cases := []struct {
			kind   string
			labels map[string]string
		}{
			{"container", map[string]string{"rig.controller": ""}},
			{"container", map[string]string{"rig.controller": "wrong"}},
			{"network", map[string]string{"rig.builder": "builder-a"}},
			{"volume", map[string]string{"rig.network": "network-b"}},
			{"image", map[string]string{"rig.quota.bytes": "not-a-number"}},
			{"network", map[string]string{"rig.controller": "generated-builder", "rig.builder": "builder-a", "rig.network": "crossed", "rig.quota.bytes": "7"}},
		}
		for index, test := range cases {
			name := "foreign-" + string(rune('a'+index))
			id := strings.Repeat(string(rune('a'+index)), 64)
			if !reader.generatedDomainDockerObjectSelected(test.kind, id, name, test.labels) {
				t.Fatalf("case %d was filtered out before label validation", index)
			}
			classifySelected(t, generatedDomainDockerObject{kind: test.kind, id: id, name: name, labels: test.labels})
		}
		for _, body := range [][]byte{
			[]byte(`{"id":"` + strings.Repeat("a", 64) + `","ID":"` + strings.Repeat("b", 64) + `"}`),
			[]byte(`{"id":"` + strings.Repeat("a", 64) + `","name":"x","labels":{"rig.builder":"a","RIG.BUILDER":"b"}}`),
		} {
			if rejectGeneratedDomainDuplicateJSONKeys(body) == nil {
				t.Fatalf("duplicate or case-folded key accepted: %s", body)
			}
		}
	})

	t.Run("all_image_ids_and_builder_labels", func(t *testing.T) {
		first := "sha256:" + strings.Repeat("a", 64)
		second := "sha256:" + strings.Repeat("b", 64)
		runner := &generatedDomainImageListRunner{body: []byte(first + "\n" + first + "\n" + second + "\n")}
		imageReader := &generatedDomainStartupReader{runner: runner,
			options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
				CommandTimeout: time.Minute, OutputLimit: 1 << 20}}
		ids, err := imageReader.readImageIDs(context.Background(), time.Now().Add(time.Minute), &generatedDomainDockerCensusBudget{})
		if err != nil || !reflect.DeepEqual(ids, []string{first, second}) {
			t.Fatalf("all image IDs = %#v, %v", ids, err)
		}
		for _, labels := range []map[string]string{{"io.rig.managed": "generated-image"}, {"rig.builder": "builder-a"}} {
			if !reader.generatedDomainDockerObjectSelected("image", first, "", labels) {
				t.Fatalf("labeled image was filtered out: %#v", labels)
			}
		}
		for _, malformed := range [][]byte{[]byte("sha256:" + strings.Repeat("a", 12)), []byte("not-an-image-id\n")} {
			runner.body = malformed
			if _, err := imageReader.readImageIDs(context.Background(), time.Now().Add(time.Minute), &generatedDomainDockerCensusBudget{}); err == nil {
				t.Fatalf("malformed image census accepted: %q", malformed)
			}
		}
	})

	t.Run("bootstrap_nonce_is_native_v1_volume_only", func(t *testing.T) {
		nonce := strings.Repeat("a", 64)
		labels := map[string]string{
			gatewayV2ManagedLabelKey:                 gatewayV2ManagedContainerLabel,
			gatewayV2IdentityLabelKey:                gatewayV1IdentityVersion,
			generatedDomainBootstrapVolumeNonceLabel: nonce,
		}
		if !validGeneratedDomainNativeV1VolumeLabels(labels) {
			t.Fatal("exact native-v1 bootstrap nonce labels were rejected")
		}
		for name, mutate := range map[string]func(map[string]string){
			"uppercase_nonce": func(value map[string]string) {
				value[generatedDomainBootstrapVolumeNonceLabel] = strings.Repeat("A", 64)
			},
			"extra_Rig_label": func(value map[string]string) { value["io.rig.unexpected"] = "present" },
			"wrong_identity":  func(value map[string]string) { value[gatewayV2IdentityLabelKey] = gatewayV2IdentityVersion },
		} {
			t.Run(name, func(t *testing.T) {
				candidate := cloneStringMap(labels)
				mutate(candidate)
				if validGeneratedDomainNativeV1VolumeLabels(candidate) {
					t.Fatalf("noncanonical nonce labels were accepted: %#v", candidate)
				}
			})
		}
		wrongVolume := generatedDomainDockerObject{kind: "volume", name: "renamed-bootstrap-volume",
			labels: labels, data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{}}}
		if !reader.generatedDomainDockerObjectSelected(wrongVolume.kind, wrongVolume.id, wrongVolume.name, wrongVolume.labels) {
			t.Fatal("nonce-labeled wrong volume was hidden from the census")
		}
		classifySelected(t, wrongVolume)
	})
}

func generatedDomainNativeV1AttestationFixture(t *testing.T) (routeState, gatewayMigrationJournal, gatewayV2DockerObservation) {
	t.Helper()
	_, v2, journal := gatewayV2IdentityTestState(t)
	state := routeState{Version: 1, Active: map[string]routeRecord{}}
	observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
	containerID := strings.Repeat("b", 64)
	networkID := strings.Repeat("e", 64)
	observation.V1Container.ID = containerID
	observation.V1NetworkID = networkID
	for key, value := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, key)
		observation.V1Network.Containers[containerID] = value
	}
	return state, journal, observation
}

func generatedDomainNativeV2AttestationFixture(t *testing.T) (gatewayV2RouteState, gatewayMigrationJournal, gatewayV2DockerObservation) {
	t.Helper()
	source, state, journal := gatewayV2IdentityTestState(t)
	state.Apps = map[string]gatewayV2AppRoute{}
	state.Pending = nil
	state.LANRecovery = nil
	journal.Phase = gatewayPhaseCommitted
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	containerID := journal.Resources.FinalContainerID
	networkID := journal.Resources.IngressNetworkID
	observation.FinalContainer.ID = containerID
	observation.IngressNetworkID = networkID
	for key, value := range observation.IngressNetwork.Containers {
		delete(observation.IngressNetwork.Containers, key)
		observation.IngressNetwork.Containers[containerID] = value
	}
	configured := observation.FinalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork]
	configured.NetworkID = networkID
	observation.FinalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork] = configured
	return state, journal, observation
}

func generatedDomainGatewayVolumeIdentity(volume volumeInspection,
	identity gatewayV1VolumeIdentity,
) gatewayVolumeIdentityInspection {
	return gatewayVolumeIdentityInspection{Name: volume.Name, Driver: volume.Driver, Scope: volume.Scope,
		Options: volume.Options, Labels: volume.Labels, Mountpoint: identity.Mountpoint, CreatedAt: identity.CreatedAt}
}

type generatedDomainCurrentAttestationFixture struct {
	stateV1   gatewayCurrentRouteState
	stateV2   gatewayCurrentRouteStateV2
	receipt   gatewayRebindFinalHandoverTerminalReceipt
	terminal  gatewayRebindAttemptTerminalView
	image     imageInspection
	container caddyInspection
	runtime   gatewayContainerRuntime
	volumes   map[string]gatewayVolumeIdentityInspection
	network   gatewayNetworkIdentityInspection
}

func generatedDomainCurrentAttestationFixtureForTest(t *testing.T) generatedDomainCurrentAttestationFixture {
	t.Helper()
	const hostPort uint16 = 19877
	original := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(original.baseline)
	state.Apps = map[string]gatewayCurrentAppRoute{}
	state.Pending = nil
	state.LANRecovery = nil
	transferDigest, err := appaccess.GatewayRebindTransferManifestDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	state.TransferManifestDigest = transferDigest

	receipt := original.receipt
	receipt.Resources.ApplicationNetworks = nil
	final := *receipt.Resources.FinalContainer
	final.ConfigurationDigest = strings.Repeat("0", 64)
	receipt.Resources.FinalContainer = &final
	generatedDomainRehashLegacyReceipt(t, &receipt)
	preliminary, err := newGatewayRebindAttemptTerminalViewLegacy(receipt)
	if err != nil {
		t.Fatal(err)
	}
	state.Lineage.TerminalReceiptDigest = receipt.Digest
	facts := gatewayFinalOwnershipFacts{Lineage: state.Lineage, Terminal: preliminary,
		Network: state.Network, LocalHostPort: hostPort}
	final.ConfigurationDigest, err = gatewayFinalOwnershipConfigurationDigest(facts)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Resources.FinalContainer = &final
	generatedDomainRehashLegacyReceipt(t, &receipt)
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(receipt)
	if err != nil {
		t.Fatal(err)
	}
	state.Lineage.TerminalReceiptDigest = receipt.Digest
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) || !gatewayRebindAttemptTerminalMatchesLineage(terminal, state.Lineage) {
		t.Fatalf("gateway-only current V1 fixture: %v", err)
	}

	stateV2 := gatewayCurrentRouteStateV2{
		Version: gatewayCurrentRouteStateVersionV2, Revision: 1, Lineage: state.Lineage,
		Profile: state.Profile, Identity: state.Identity, Network: state.Network,
		TransferManifestDigest: state.TransferManifestDigest,
		SourceV1:               &gatewayCurrentRouteSourceV1{Revision: state.Revision, Digest: state.Digest},
		Apps:                   map[string]gatewayCurrentAppRouteV2{},
	}
	stateV2.Digest, err = gatewayCurrentRouteStateDigestV2(stateV2)
	if err != nil || !validGatewayCurrentRouteStateV2(stateV2) {
		t.Fatalf("gateway-only current V2 fixture: %v", err)
	}
	target := gatewayCurrentPhysicalTarget{State: state, Lineage: state.Lineage, Terminal: terminal,
		Identity: state.Identity, Resources: terminal.Resources}
	container, runtime := gatewayCurrentPhysicalRuntimeTestContainer(target, hostPort, true)
	facts = gatewayFinalOwnershipFacts{Lineage: state.Lineage, Terminal: terminal,
		Network: state.Network, LocalHostPort: hostPort}
	labels := gatewayFinalOwnershipLabels(facts, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole)
	bridge := "rig" + terminal.SuccessorIdentity.Digest[:12]
	network := gatewayNetworkIdentityInspection{
		ID: terminal.Resources.IngressNetwork.ID, Name: terminal.SuccessorIdentity.IngressNetwork,
		Driver: "bridge", Scope: "local", Options: map[string]string{gatewayRebindBridgeNameOptionKey: bridge},
		IPAM:   caddyNetworkIPAM{Config: []networkIPAM{{Subnet: state.Network.Subnet, Gateway: state.Network.GatewayIPv4}}},
		Labels: labels, Containers: map[string]caddyNetworkContainerInspection{container.ID: {
			Name: terminal.SuccessorIdentity.FinalContainer, IPv4Address: state.Network.ContainerIPv4 + "/28"}},
	}
	config := terminal.Resources.ConfigVolume
	data := terminal.Resources.DataVolume
	volumes := map[string]gatewayVolumeIdentityInspection{
		config.Name: {Name: config.Name, Driver: "local", Scope: "local", Options: map[string]string{},
			Labels:     gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole),
			Mountpoint: config.Mountpoint, CreatedAt: config.CreatedAt},
		data.Name: {Name: data.Name, Driver: "local", Scope: "local", Options: map[string]string{},
			Labels:     gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole),
			Mountpoint: data.Mountpoint, CreatedAt: data.CreatedAt},
	}
	return generatedDomainCurrentAttestationFixture{stateV1: state, stateV2: stateV2, receipt: receipt, terminal: terminal,
		image: imageInspection{ID: "sha256:" + terminal.Resources.ImageID, OS: "linux",
			RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}},
		container: container, runtime: runtime, volumes: volumes, network: network}
}

func generatedDomainRehashLegacyReceipt(t *testing.T, receipt *gatewayRebindFinalHandoverTerminalReceipt) {
	t.Helper()
	var err error
	receipt.FinalBindingDigest, err = gatewayRebindFinalContainerBindingDigest(*receipt.Resources.FinalContainer)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(receipt.Resources)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Digest, err = gatewayRebindFinalHandoverTerminalDigest(*receipt)
	if err != nil {
		t.Fatal(err)
	}
}

func generatedDomainLegacyTerminalView(receipt gatewayRebindFinalHandoverTerminalReceipt) gatewayRebindAttemptTerminalView {
	copy := receipt
	return gatewayRebindAttemptTerminalView{Format: gatewayRebindAttemptTerminalLegacyV1,
		Generation: receipt.Generation, OperationID: receipt.OperationID, Disposition: receipt.Disposition,
		ProtectedIntentDigest: receipt.ProtectedIntentDigest, SuccessorProfile: receipt.SuccessorProfile,
		SuccessorIdentity: receipt.SuccessorIdentity, Resources: receipt.Resources, Digest: receipt.Digest,
		LegacyReceipt: &copy}
}

func generatedDomainAssertCurrentAttestation(t *testing.T, expected generatedDomainGatewayExpectation,
	fixture generatedDomainCurrentAttestationFixture, v2 bool,
) {
	t.Helper()
	var wanted []byte
	var token string
	var err error
	if v2 {
		wanted, err = gatewayCurrentPhysicalConfigBytesV2(fixture.stateV2)
		token, _ = gatewayCurrentPhysicalProbeTokenV2(fixture.stateV2)
	} else {
		wanted, err = gatewayCurrentPhysicalConfigBytes(fixture.stateV1)
		token, _ = gatewayCurrentPhysicalProbeToken(fixture.stateV1)
	}
	if err != nil || !validSHA256(token) {
		t.Fatalf("current expected config: %v", err)
	}
	expected.names = generatedDomainGatewayExpectedNames(expected)
	expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
	if err != nil {
		t.Fatal(err)
	}
	portToken := gatewayV2PortChallenge(token, fixture.stateV1.Profile.PortStart)
	hostPort := generatedDomainCurrentFixtureHostPort(t, fixture)
	runner := &generatedDomainGatewayAttestationRunner{
		image: fixture.image,
		container: gatewayContainerInspection{caddyInspection: fixture.container,
			gatewayContainerRuntime: fixture.runtime},
		volumes: fixture.volumes, network: fixture.network,
		restartName: fixture.stateV1.Identity.Rebind.ActiveConfigFilename,
		restart:     wanted, live: wanted, challenge: gatewayV2ChallengeBodyPrefix + portToken,
	}
	reader := &generatedDomainStartupReader{runner: runner,
		options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
			HostPort: hostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
		expectation: expected}
	snapshot := generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{
		{kind: "container", id: fixture.container.ID, name: fixture.terminal.SuccessorIdentity.FinalContainer},
		{kind: "volume", name: fixture.terminal.SuccessorIdentity.ConfigVolume},
		{kind: "volume", name: fixture.terminal.SuccessorIdentity.DataVolume},
		{kind: "network", id: fixture.network.ID, name: fixture.terminal.SuccessorIdentity.IngressNetwork},
	}}
	got, err := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot)
	if err != nil || got.kind != expected.kind || !got.exact || got.stateDigest != expected.stateDigest ||
		got.operationID != fixture.stateV1.Lineage.OperationID ||
		got.protectedGeneration != fixture.stateV1.Lineage.ProtectedGeneration {
		t.Fatalf("current passive attestation = %#v, %v", got, err)
	}
	source := gatewayCurrentAuthority(fixture.stateV1.Lineage)
	profile := gatewayCurrentFixtureProfile(fixture.stateV1.Lineage, fixture.stateV1.Profile)
	database := deployments.GeneratedDomainStartupSnapshot{Gateway: appaccess.HostingGatewayStartupSnapshot{
		Rebind: appaccess.GatewayRebindRecoverySnapshot{CurrentSource: &source, CurrentProfile: &profile}}}
	protected := generatedDomainProtectedStartupSnapshot{
		terminals: map[string]gatewayRebindAttemptTerminalView{fixture.stateV1.Lineage.OperationID: fixture.terminal}}
	if v2 {
		name, _ := gatewayCurrentRouteStateNameV2(fixture.stateV2.Lineage.ProtectedGeneration,
			fixture.stateV2.Lineage.OperationID)
		protected.currentV2 = gatewayCurrentV2Census{files: map[string]gatewayCurrentV2CensusEntry{name: {
			Generation:  fixture.stateV2.Lineage.ProtectedGeneration,
			OperationID: fixture.stateV2.Lineage.OperationID, State: fixture.stateV2}}}
	} else {
		name, _ := gatewayCurrentRouteStateName(fixture.stateV1.Lineage.ProtectedGeneration,
			fixture.stateV1.Lineage.OperationID)
		protected.currentV1 = map[string]gatewayCurrentRouteState{name: fixture.stateV1}
	}
	snapshot.gateway = got
	generatedDomainAssertStableClassification(t, database, protected, snapshot)
	if !generatedDomainGatewayCommandsInSafeOrder(runner.commands) {
		t.Fatalf("passive read order = %#v", runner.commands)
	}
}

func generatedDomainAssertStableClassification(t *testing.T, database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, docker generatedDomainDockerStartupSnapshot,
) {
	t.Helper()
	got, err := classifyGeneratedDomainStartup(database, protected, docker)
	if err != nil || got.disposition != generatedDomainStartupStable || got.reason != generatedDomainStartupReasonStable ||
		len(got.recovery) != 0 {
		t.Fatalf("attested stable classification = %#v, %v", got, err)
	}
}

func generatedDomainCurrentFixtureHostPort(t *testing.T, fixture generatedDomainCurrentAttestationFixture) uint16 {
	t.Helper()
	bindings := fixture.container.PortBindings["8080/tcp"]
	if len(bindings) != 1 {
		t.Fatal("gateway-only current fixture has no loopback binding")
	}
	var port uint64
	for _, character := range bindings[0]["HostPort"] {
		if character < '0' || character > '9' {
			t.Fatal("gateway-only current fixture has invalid loopback binding")
		}
		port = port*10 + uint64(character-'0')
	}
	if port == 0 || port > 65535 {
		t.Fatal("gateway-only current fixture has invalid loopback port")
	}
	return uint16(port)
}

func generatedDomainGatewayCommandsInSafeOrder(commands [][]string) bool {
	copyIndex, adminIndex, challengeIndex := -1, -1, -1
	for index, args := range commands {
		switch {
		case len(args) > 1 && args[0] == "container" && args[1] == "cp":
			copyIndex = index
		case generatedDomainGatewayAdminCommand(args, args[2]):
			adminIndex = index
		case generatedDomainGatewayChallengeCommand(args, args[2]):
			challengeIndex = index
		}
	}
	return copyIndex >= 0 && adminIndex > copyIndex && challengeIndex > adminIndex
}

func generatedDomainRecoveryCommandsInSafeOrder(commands [][]string) bool {
	inspected := make(map[string]bool)
	restartRead := make(map[string]bool)
	adminRead := make(map[string]bool)
	adminCount := 0
	for _, args := range commands {
		if len(args) < 3 {
			continue
		}
		containerID := args[2]
		switch {
		case len(args) == 5 && args[0] == "container" && args[1] == "inspect":
			containerID = args[4]
			inspected[containerID] = true
		case args[0] == "container" && args[1] == "cp":
			containerID = strings.SplitN(args[2], ":", 2)[0]
			if !inspected[containerID] {
				return false
			}
			restartRead[containerID] = true
		case generatedDomainGatewayAdminCommand(args, containerID):
			if !restartRead[containerID] {
				return false
			}
			adminRead[containerID] = true
			adminCount++
		case generatedDomainGatewayChallengeCommand(args, containerID):
			if !adminRead[containerID] {
				return false
			}
		case args[0] == "container" && args[1] == "exec":
			return false
		}
	}
	return adminCount != 0
}
