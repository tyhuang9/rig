package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainFailingRunner struct {
	calls  int
	result runtimeprocess.CommandResult
	err    error
}

type generatedDomainOuterRunner struct {
	projection         *generatedDomainDockerInspectProjection
	driftOnSecondList  bool
	containerListCalls int
	commands           [][]string
}

func (r *generatedDomainOuterRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.commands = append(r.commands, append([]string(nil), request.Args...))
	args := request.Args
	switch {
	case reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}):
		return runtimeprocess.CommandResult{Stdout: []byte(`"daemon-one"`)}, nil
	case len(args) == 6 && reflect.DeepEqual(args[:4], []string{"container", "ls", "--all", "--no-trunc"}):
		r.containerListCalls++
		visible := r.projection != nil
		if r.driftOnSecondList {
			visible = r.containerListCalls >= 2
		}
		if visible {
			return runtimeprocess.CommandResult{Stdout: []byte(`{"id":"` + r.projection.ID + `"}` + "\n")}, nil
		}
		return runtimeprocess.CommandResult{}, nil
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"network", "ls", "--no-trunc", "--format"}):
		return runtimeprocess.CommandResult{}, nil
	case len(args) == 4 && reflect.DeepEqual(args[:3], []string{"volume", "ls", "--format"}):
		return runtimeprocess.CommandResult{}, nil
	case reflect.DeepEqual(args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"}):
		return runtimeprocess.CommandResult{}, nil
	case len(args) == 5 && r.projection != nil &&
		reflect.DeepEqual(args[:4], []string{"container", "inspect", "--format", generatedDomainContainerInspectFormat}) &&
		args[4] == r.projection.ID:
		return jsonResult(*r.projection), nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected outer startup command")
	}
}

func (r *generatedDomainFailingRunner) Run(context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.calls++
	return r.result, r.err
}

func TestGeneratedDomainStartupRequiresCanonicalConcreteInputs(t *testing.T) {
	runner := &generatedDomainFailingRunner{}
	for _, test := range []struct {
		ctx    context.Context
		stores *deployments.GeneratedOwnerStores
		runner runtimeprocess.CommandRunner
	}{
		{ctx: nil, stores: &deployments.GeneratedOwnerStores{}, runner: runner},
		{ctx: context.Background(), stores: nil, runner: runner},
		{ctx: context.Background(), stores: &deployments.GeneratedOwnerStores{}, runner: nil},
	} {
		got, err := inspectGeneratedDomainStartup(test.ctx, test.stores, test.runner, generatedDomainOptions{})
		if !errors.Is(err, errGeneratedDomainStartupInvalid) || !reflect.DeepEqual(got, generatedDomainStartupClassification{}) {
			t.Fatalf("invalid input result = %#v, %v", got, err)
		}
	}
	if runner.calls != 0 {
		t.Fatalf("invalid inputs dispatched %d commands", runner.calls)
	}
}

func TestGeneratedDomainStartupClassifiesProvedEmpty(t *testing.T) {
	got, err := classifyGeneratedDomainStartup(deployments.GeneratedDomainStartupSnapshot{},
		generatedDomainProtectedStartupSnapshot{effectEmpty: true},
		generatedDomainDockerStartupSnapshot{effectEmpty: true})
	if err != nil || got.disposition != generatedDomainStartupEmpty || got.reason != generatedDomainStartupReasonEmpty ||
		len(got.recovery) != 0 {
		t.Fatalf("proved empty = %#v, %v", got, err)
	}
	for _, changed := range []generatedDomainDockerStartupSnapshot{
		{effectEmpty: false}, {effectEmpty: true, cold: true}, {effectEmpty: true, ambiguous: true},
	} {
		got, err = classifyGeneratedDomainStartup(deployments.GeneratedDomainStartupSnapshot{},
			generatedDomainProtectedStartupSnapshot{effectEmpty: true}, changed)
		if err != nil || got.disposition != generatedDomainStartupRefused {
			t.Fatalf("unproved empty = %#v, %v", got, err)
		}
	}

	t.Run("sealed_outer_empty", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		runner := &generatedDomainOuterRunner{}
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
		if err != nil || got.disposition != generatedDomainStartupEmpty || got.reason != generatedDomainStartupReasonEmpty {
			t.Fatalf("outer empty = %#v, %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.dataRoot, "runtime", "generated-ingress")); !os.IsNotExist(err) {
			t.Fatalf("outer empty created protected state: %v", err)
		}
		generatedDomainAssertNoOuterExec(t, runner.commands)
	})
}

func TestGeneratedDomainStartupRejectsObservationChanges(t *testing.T) {
	left := generatedDomainDockerStartupSnapshot{daemonID: "daemon-a", effectEmpty: true}
	right := left
	if !sameGeneratedDomainDockerStartup(left, right) {
		t.Fatal("identical Docker observations changed")
	}
	right.daemonID = "daemon-b"
	if sameGeneratedDomainDockerStartup(left, right) {
		t.Fatal("daemon replacement was hidden")
	}
	protectedLeft := generatedDomainProtectedStartupSnapshot{effectEmpty: true,
		currentV1: map[string]gatewayCurrentRouteState{}, currentV2: gatewayCurrentV2Census{files: map[string]gatewayCurrentV2CensusEntry{}},
		terminals: map[string]gatewayRebindAttemptTerminalView{}}
	protectedRight := protectedLeft
	protectedRight.ambiguous = true
	if sameGeneratedDomainProtectedStartup(protectedLeft, protectedRight) {
		t.Fatal("protected ambiguity change was hidden")
	}
	if !errors.Is(errGeneratedDomainStartupChanged, errGeneratedDomainStartupChanged) {
		t.Fatal("changed observation sentinel is not stable")
	}
	t.Run("sealed_outer_docker_drift", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		projection := generatedDomainRenamedBuilderProjection()
		runner := &generatedDomainOuterRunner{projection: &projection, driftOnSecondList: true}
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
		if !errors.Is(err, errGeneratedDomainStartupRead) || !reflect.DeepEqual(got, generatedDomainStartupClassification{}) {
			t.Fatalf("outer drift = %#v, %v", got, err)
		}
		generatedDomainAssertNoOuterExec(t, runner.commands)
	})
}

func TestGeneratedDomainStartupFailsClosedOnReadFailures(t *testing.T) {
	for _, runner := range []*generatedDomainFailingRunner{
		{err: errors.New("read failed")},
		{result: runtimeprocess.CommandResult{Stdout: []byte("{}"), Stderr: []byte("warning")}},
		{result: runtimeprocess.CommandResult{Stdout: []byte("{}"), StdoutTruncated: true}},
	} {
		reader := &generatedDomainStartupReader{runner: runner,
			options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
				CommandTimeout: time.Minute, OutputLimit: 1 << 20}}
		if _, err := reader.runRead(context.Background(), time.Now().Add(time.Minute), "version"); err == nil {
			t.Fatal("failed or incomplete read was accepted")
		}
		if runner.calls != 1 {
			t.Fatalf("read failure calls = %d", runner.calls)
		}
	}
	t.Run("sealed_outer_daemon_failure", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		runner := &generatedDomainFailingRunner{err: errors.New("daemon unavailable")}
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
		if !errors.Is(err, errGeneratedDomainStartupRead) || !reflect.DeepEqual(got, generatedDomainStartupClassification{}) {
			t.Fatalf("outer read failure = %#v, %v", got, err)
		}
	})
}

func TestGeneratedDomainStartupReadOnlyTraceAndNoAuthority(t *testing.T) {
	t.Run("retained_application_no_endpoint_or_app_host_traffic", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		_ = newGeneratedMigrationFixtureOnDatabaseWithComponents(t, fixture.database, []string{"api"}, []string{"api"})
		sealed, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil || len(sealed.Runtime.Deployments) != 1 || len(sealed.Runtime.Components) != 1 {
			t.Fatalf("sealed retained application census = %#v, %v", sealed, err)
		}
		runner := &generatedDomainOuterRunner{}
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
		if err != nil || got.disposition != generatedDomainStartupRefused ||
			got.reason != generatedDomainStartupReasonColdAccounting {
			t.Fatalf("outer retained app = %#v, %v", got, err)
		}
		generatedDomainAssertNoOuterExec(t, runner.commands)
	})

	t.Run("lan_assignment_no_assigned_root_traffic", func(t *testing.T) {
		retained := newGeneratedDomainRetainedAppUpgradeFixture(t)
		predecessor := retained.gatewayRebindPredecessorFixture
		fixture := retained.identity
		sealed, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil || sealed.Gateway.Rebind.CurrentSource == nil || len(sealed.Runtime.ActiveHeads) != 1 {
			t.Fatalf("sealed LAN assignment census = %#v, %v", sealed, err)
		}
		protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		if err != nil || !protected.upgrade.committed || len(protected.upgrade.state.Apps) != 1 {
			t.Fatalf("protected LAN assignment census = %#v, %v", protected, err)
		}
		for _, app := range protected.upgrade.state.Apps {
			if app.LAN == nil {
				t.Fatal("protected LAN assignment is missing")
			}
		}
		generatedDomainAssertRetainedAppCompleteGatewayNoTrafficForFixture(t, predecessor, retained.identity, true)
	})

	t.Run("retained_application_complete_gateway_census_has_no_application_traffic", func(t *testing.T) {
		generatedDomainAssertRetainedAppCompleteGatewayNoTraffic(t)
	})

	t.Run("unproven_listener_has_zero_http_requests", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		projection := generatedDomainRenamedBuilderProjection()
		runner := &generatedDomainOuterRunner{projection: &projection}
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
		if err != nil || got.disposition != generatedDomainStartupRefused {
			t.Fatalf("unproven listener outer result = %#v, %v", got, err)
		}
		generatedDomainAssertNoOuterExec(t, runner.commands)
	})

	t.Run("admin_and_challenge_require_attestation_order", func(t *testing.T) {
		reader, snapshot, runner := generatedDomainNativeV1ReaderFixture(t)
		got, err := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot)
		if err != nil || !got.exact || !generatedDomainGatewayCommandsInSafeOrder(runner.commands) {
			t.Fatalf("ordered passive attestation = %#v, %v, commands=%#v", got, err, runner.commands)
		}
		for _, args := range runner.commands {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, " HEAD ") || strings.HasSuffix(joined, " /") ||
				(strings.Contains(joined, "http://") && !strings.Contains(joined, "127.0.0.1:2019/config/") &&
					!strings.Contains(joined, gatewayV2ChallengePathPrefix)) {
				t.Fatalf("forbidden application request: %#v", args)
			}
		}
	})

	t.Run("invalid_identity_or_config_never_reaches_challenge", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*generatedDomainGatewayAttestationRunner)
			admin  bool
		}{
			{"invalid_identity", func(r *generatedDomainGatewayAttestationRunner) { r.image.ID = "sha256:" + strings.Repeat("f", 64) }, false},
			{"invalid_restart", func(r *generatedDomainGatewayAttestationRunner) { r.restart = []byte(`{"apps":{}}`) }, false},
			{"mismatched_admin", func(r *generatedDomainGatewayAttestationRunner) { r.live = []byte(`{"apps":{}}`) }, true},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				reader, snapshot, runner := generatedDomainNativeV1ReaderFixture(t)
				test.mutate(runner)
				if got, err := reader.attestGateway(context.Background(), time.Now().Add(time.Minute), snapshot); err == nil || got.exact {
					t.Fatalf("invalid evidence attested: %#v, %v", got, err)
				}
				for _, args := range runner.commands {
					if generatedDomainGatewayChallengeCommand(args, runner.container.ID) {
						t.Fatalf("invalid evidence reached challenge: %#v", runner.commands)
					}
				}
				if test.admin != generatedDomainTraceHasAdmin(runner.commands, runner.container.ID) {
					t.Fatalf("admin trace mismatch: %#v", runner.commands)
				}
			})
		}
	})

	classificationType := reflect.TypeOf(generatedDomainStartupClassification{})
	for index := 0; index < classificationType.NumField(); index++ {
		field := classificationType.Field(index)
		if field.Type.Kind() == reflect.Interface || strings.Contains(field.Type.String(), "Manager") ||
			strings.Contains(field.Type.String(), "Store") || strings.Contains(field.Type.String(), "Driver") {
			t.Fatalf("classification exposes authority field %s %s", field.Name, field.Type)
		}
	}
}

func generatedDomainNativeV1ReaderFixture(t *testing.T) (*generatedDomainStartupReader,
	generatedDomainDockerStartupSnapshot, *generatedDomainGatewayAttestationRunner,
) {
	t.Helper()
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
		volumes: map[string]gatewayVolumeIdentityInspection{observation.V1Volume.Name: {
			Name: observation.V1Volume.Name, Driver: observation.V1Volume.Driver, Scope: observation.V1Volume.Scope,
			Options: observation.V1Volume.Options, Labels: observation.V1Volume.Labels,
			Mountpoint: observation.V1VolumeIdentity.Mountpoint, CreatedAt: observation.V1VolumeIdentity.CreatedAt}},
		network: gatewayNetworkIdentityInspection{ID: observation.V1NetworkID, Name: observation.V1Network.Name,
			Driver: observation.V1Network.Driver, Scope: observation.V1Network.Scope,
			Options: observation.V1Network.Options, IPAM: observation.V1Network.IPAM,
			Labels: observation.V1Network.Labels, Containers: observation.V1Network.Containers},
		restartName: gatewayV2ActiveConfigFile, restart: observation.V1RestartConfig, live: observation.V1Config,
	}
	reader := &generatedDomainStartupReader{runner: runner,
		options: generatedDomainOptions{DockerExecutable: `C:\docker.exe`, WorkingDirectory: `C:\work`,
			HostPort: journal.Source.LocalHostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20}, expectation: expected}
	snapshot := generatedDomainDockerStartupSnapshot{objects: []generatedDomainDockerObject{
		{kind: "container", id: observation.V1Container.ID, name: caddyContainerName},
		{kind: "volume", name: caddyVolumeName},
		{kind: "network", id: observation.V1NetworkID, name: caddyNetworkName},
	}}
	return reader, snapshot, runner
}

func generatedDomainTraceHasAdmin(commands [][]string, containerID string) bool {
	for _, args := range commands {
		if generatedDomainGatewayAdminCommand(args, containerID) {
			return true
		}
	}
	return false
}

func generatedDomainRenamedBuilderProjection() generatedDomainDockerInspectProjection {
	return generatedDomainDockerInspectProjection{ID: strings.Repeat("a", 64), Name: "/renamed-builder",
		Labels: map[string]string{"rig.controller": "generated-builder", "rig.builder": "builder-a"}}
}

func generatedDomainAssertNoOuterExec(t *testing.T, commands [][]string) {
	t.Helper()
	for _, args := range commands {
		joined := strings.Join(args, " ")
		if len(args) >= 2 && args[0] == "container" && (args[1] == "exec" || args[1] == "cp") ||
			strings.Contains(joined, "http://") || strings.Contains(joined, "https://") ||
			strings.Contains(joined, "curl") || strings.Contains(joined, "wget") || strings.Contains(joined, " HEAD ") {
			t.Fatalf("outer refusal dispatched application or unowned-listener traffic: %#v", commands)
		}
	}
}
