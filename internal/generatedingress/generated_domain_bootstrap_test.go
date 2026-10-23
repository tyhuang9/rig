//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const generatedDomainConstructionCaseEnvironment = "RIG_GENERATED_DOMAIN_CONSTRUCTION_CASE"

const generatedDomainBootstrapVolumeIdentityFormat = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}},"labels":{{json .Labels}},"mountpoint":{{json .Mountpoint}},"createdAt":{{json .CreatedAt}}}`

type generatedDomainBootstrapTestRunner struct {
	mu sync.Mutex

	daemonID  string
	hostPort  uint16
	image     imageInspection
	volume    *gatewayVolumeIdentityInspection
	network   *gatewayNetworkIdentityInspection
	container *gatewayContainerInspection
	config    []byte

	commands      [][]string
	imageInspects int
	censuses      int
	challenges    int
	effects       int
	loseAck       string
	extra         []generatedDomainDockerObject
	hook          func(string, int) error
	hookErr       error

	exposeStoppedNetworkTopology bool
}

func newGeneratedDomainBootstrapTestRunner(hostPort uint16) *generatedDomainBootstrapTestRunner {
	return &generatedDomainBootstrapTestRunner{
		daemonID: "private-empty-bootstrap-daemon", hostPort: hostPort,
		image: gatewayV2IdentityTestImage(),
	}
}

func (r *generatedDomainBootstrapTestRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	args := append([]string(nil), request.Args...)
	r.commands = append(r.commands, args)
	if reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}) {
		return jsonResult(r.daemonID), nil
	}
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{"image", "inspect", "--format", imageInspectFormat}) {
		r.imageInspects++
		if err := r.runHookLocked("image-inspect", r.imageInspects); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		if args[4] != caddyImage && normalizeID(args[4]) != normalizeID(r.image.ID) {
			return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap image inspection")
		}
		return jsonResult(r.image), nil
	}
	if reflect.DeepEqual(args, []string{"container", "ls", "--all", "--no-trunc", "--format", generatedDomainContainerListFormat}) {
		r.censuses++
		if err := r.runHookLocked("census", r.censuses); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		return generatedDomainRecoveryCensusList(r.objectsLocked(), "container"), nil
	}
	if reflect.DeepEqual(args, []string{"network", "ls", "--no-trunc", "--format", generatedDomainNetworkListFormat}) {
		return generatedDomainRecoveryCensusList(r.objectsLocked(), "network"), nil
	}
	if reflect.DeepEqual(args, []string{"volume", "ls", "--format", generatedDomainVolumeListFormat}) {
		return generatedDomainRecoveryCensusList(r.objectsLocked(), "volume"), nil
	}
	if reflect.DeepEqual(args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"}) {
		return runtimeprocess.CommandResult{Stdout: []byte(r.image.ID + "\n")}, nil
	}
	if len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format" &&
		args[3] == generatedDomainImageInspectFormat && normalizeID(args[4]) == normalizeID(r.image.ID) {
		return jsonResult(generatedDomainDockerInspectProjection{ID: r.image.ID,
			Labels: map[string]string{}, ContentID: r.image.ID}), nil
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" {
		if args[3] == generatedDomainContainerInspectFormat {
			if r.container != nil && args[4] == r.container.ID {
				return jsonResult(generatedDomainDockerInspectProjection{ID: r.container.ID,
					Name: "/" + caddyContainerName, Labels: cloneStringMap(r.container.Labels)}), nil
			}
			for _, object := range r.extra {
				if object.kind == "container" && object.id == args[4] {
					return jsonResult(generatedDomainDockerInspectProjection{ID: object.id,
						Name: "/" + object.name, Labels: cloneStringMap(object.labels)}), nil
				}
			}
			return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap census container")
		}
		if args[3] == gatewayContainerInspectFormat && r.container != nil &&
			(args[4] == r.container.ID || args[4] == caddyContainerName) {
			return jsonResult(*r.container), nil
		}
	}
	if len(args) == 5 && args[0] == "volume" && args[1] == "inspect" && args[2] == "--format" &&
		(args[3] == generatedDomainVolumeInspectFormat || args[3] == generatedDomainBootstrapVolumeIdentityFormat) &&
		r.volume != nil && args[4] == caddyVolumeName {
		return jsonResult(*r.volume), nil
	}
	if len(args) == 5 && args[0] == "network" && args[1] == "inspect" && args[2] == "--format" {
		if args[3] == generatedDomainNetworkInspectFormat {
			return generatedDomainRecoveryCensusProjection(r.objectsLocked(), "network", args[4])
		}
		if args[3] == "json" && r.network != nil && (args[4] == r.network.ID || args[4] == caddyNetworkName) {
			return jsonResult([]gatewayNetworkIdentityInspection{*r.network}), nil
		}
	}
	if len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
		return r.createVolumeLocked(args)
	}
	if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
		return r.createNetworkLocked(args)
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "create" {
		return r.createContainerLocked(args)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && r.container != nil {
		if args[3] == r.container.ID+":/config/"+gatewayV2ActiveConfigFile {
			body, err := os.ReadFile(args[2])
			if err != nil {
				return runtimeprocess.CommandResult{}, err
			}
			r.effects++
			r.config = append(r.config[:0], body...)
			if r.loseAck == "copy" {
				return runtimeprocess.CommandResult{}, errors.New("injected lost config-copy acknowledgement")
			}
			return runtimeprocess.CommandResult{}, nil
		}
		if args[2] == r.container.ID+":/config/"+gatewayV2ActiveConfigFile && args[3] == "-" {
			return gatewayCurrentPhysicalTarResult(gatewayV2ActiveConfigFile, r.config)
		}
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" && r.container != nil &&
		args[2] == r.container.ID {
		r.effects++
		r.container.Running = true
		r.container.gatewayContainerRuntime.EffectivePortBindings =
			gatewayV2IdentityTestPortBindingsCopy(r.container.PortBindings)
		configured := r.container.ConfiguredNetworks[caddyNetworkName]
		configured.NetworkID = r.network.ID
		configured.IPAddress = configured.IPAMConfig.IPv4Address
		r.container.ConfiguredNetworks[caddyNetworkName] = configured
		r.network.Containers[r.container.ID] = caddyNetworkContainerInspection{
			Name: caddyContainerName, IPv4Address: configured.IPAddress + "/28"}
		if r.loseAck == "start" {
			return runtimeprocess.CommandResult{}, errors.New("injected lost start acknowledgement")
		}
		return runtimeprocess.CommandResult{Stdout: []byte(r.container.ID + "\n")}, nil
	}
	if r.container != nil && len(args) >= 3 && generatedDomainGatewayAdminCommand(args, r.container.ID) {
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.config...)}, nil
	}
	if r.container != nil && len(args) >= 3 && generatedDomainGatewayChallengeCommand(args, r.container.ID) {
		r.challenges++
		if err := r.runHookLocked("challenge", r.challenges); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		return runtimeprocess.CommandResult{Stdout: []byte("\n404")}, nil
	}
	return runtimeprocess.CommandResult{}, fmt.Errorf("unexpected private bootstrap command: %#v", args)
}

func (r *generatedDomainBootstrapTestRunner) runHookLocked(event string, count int) error {
	if r.hook == nil || r.hookErr != nil {
		return r.hookErr
	}
	if err := r.hook(event, count); err != nil {
		r.hookErr = err
		return err
	}
	return nil
}

func (r *generatedDomainBootstrapTestRunner) createVolumeLocked(args []string) (runtimeprocess.CommandResult, error) {
	if r.volume != nil || args[len(args)-1] != caddyVolumeName {
		return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap volume create")
	}
	var nonce string
	for _, arg := range args {
		if strings.HasPrefix(arg, generatedDomainBootstrapVolumeNonceLabel+"=") {
			nonce = strings.TrimPrefix(arg, generatedDomainBootstrapVolumeNonceLabel+"=")
		}
	}
	if !validGeneratedDomainBootstrapVolumeNonce(nonce) {
		return runtimeprocess.CommandResult{}, errors.New("invalid bootstrap volume nonce")
	}
	r.effects++
	r.volume = &gatewayVolumeIdentityInspection{Name: caddyVolumeName, Driver: "local", Scope: "local",
		Options: nil, Labels: map[string]string{
			gatewayV2ManagedLabelKey:                 gatewayV2ManagedContainerLabel,
			gatewayV2IdentityLabelKey:                gatewayV1IdentityVersion,
			generatedDomainBootstrapVolumeNonceLabel: nonce,
		}, Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data",
		CreatedAt: "2026-10-22T23:59:00Z"}
	if r.loseAck == "volume" {
		return runtimeprocess.CommandResult{}, errors.New("injected lost volume acknowledgement")
	}
	return runtimeprocess.CommandResult{Stdout: []byte(caddyVolumeName + "\n")}, nil
}

func (r *generatedDomainBootstrapTestRunner) createNetworkLocked(args []string) (runtimeprocess.CommandResult, error) {
	if r.network != nil || args[len(args)-1] != caddyNetworkName {
		return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap network create")
	}
	var subnet, gateway string
	for index := 0; index+1 < len(args); index++ {
		switch args[index] {
		case "--subnet":
			subnet = args[index+1]
		case "--gateway":
			gateway = args[index+1]
		}
	}
	expectedSubnet, expectedGateway, _ := ingressNetworkCandidate(0)
	if subnet != expectedSubnet || gateway != expectedGateway {
		return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap network plan")
	}
	r.effects++
	id := strings.Repeat("c", 64)
	r.network = &gatewayNetworkIdentityInspection{ID: id, Name: caddyNetworkName, Driver: "bridge", Scope: "local",
		Options: map[string]string{}, IPAM: caddyNetworkIPAM{Config: []networkIPAM{{Subnet: subnet, Gateway: gateway}}},
		Labels: map[string]string{gatewayV2ManagedLabelKey: gatewayV2ManagedNetworkLabel,
			gatewayV2IdentityLabelKey: gatewayV1IdentityVersion},
		Containers: map[string]caddyNetworkContainerInspection{}}
	if r.loseAck == "network" {
		return runtimeprocess.CommandResult{}, errors.New("injected lost network acknowledgement")
	}
	return runtimeprocess.CommandResult{Stdout: []byte(id + "\n")}, nil
}

func (r *generatedDomainBootstrapTestRunner) createContainerLocked(args []string) (runtimeprocess.CommandResult, error) {
	if r.container != nil || r.network == nil {
		return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap container create")
	}
	_, _, ingressIP := ingressNetworkCandidate(0)
	expected, err := caddyCreateArgs(r.image.ID, ingressIP, r.hostPort)
	if err != nil || !reflect.DeepEqual(args, expected) {
		return runtimeprocess.CommandResult{}, errors.New("unexpected bootstrap container specification")
	}
	r.effects++
	id := strings.Repeat("b", 64)
	container := gatewayV2IdentityTestContainer(caddyContainerName, id, r.image.ID, caddyNetworkName,
		map[string]string{gatewayV2ManagedLabelKey: gatewayV2ManagedContainerLabel,
			gatewayV2IdentityLabelKey:     gatewayV1IdentityVersion,
			gatewayV2ListenerIsolationKey: gatewayV1IdentityVersion},
		map[string]*networkAttachment{caddyNetworkName: {GwPriority: caddyGatewayPriority}},
		map[string][]map[string]string{"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": fmt.Sprint(r.hostPort)}}},
		[]mountInspection{{Type: "volume", Name: caddyVolumeName, Destination: "/config", RW: true}},
		gatewayV2FinalRestartPolicy, "/config/"+gatewayV2ActiveConfigFile)
	container.Running = false
	container.Tmpfs = map[string]string{"/data": "rw,noexec,nosuid,nodev,size=67108864"}
	configured := gatewayV2ConfiguredNetwork{
		IPAMConfig: &gatewayV2ConfiguredIPAM{IPv4Address: ingressIP}, GwPriority: caddyGatewayPriority}
	if r.exposeStoppedNetworkTopology {
		configured.NetworkID = r.network.ID
	}
	runtime := gatewayContainerRuntime{EffectivePortBindings: map[string][]map[string]string{},
		ConfiguredNetworks: map[string]gatewayV2ConfiguredNetwork{caddyNetworkName: configured}}
	r.container = &gatewayContainerInspection{caddyInspection: container, gatewayContainerRuntime: runtime}
	if r.exposeStoppedNetworkTopology {
		r.network.Containers[id] = caddyNetworkContainerInspection{Name: caddyContainerName, IPv4Address: ingressIP + "/28"}
	}
	if r.loseAck == "container" {
		return runtimeprocess.CommandResult{}, errors.New("injected lost container acknowledgement")
	}
	return runtimeprocess.CommandResult{Stdout: []byte(id + "\n")}, nil
}

func (r *generatedDomainBootstrapTestRunner) objectsLocked() []generatedDomainDockerObject {
	values := make([]generatedDomainDockerObject, 0, 3)
	if r.volume != nil {
		values = append(values, generatedDomainDockerObject{kind: "volume", name: r.volume.Name,
			labels: cloneStringMap(r.volume.Labels), data: generatedDomainDockerObjectData{driver: r.volume.Driver,
				scope: r.volume.Scope, options: cloneStringMap(r.volume.Options), mountpoint: r.volume.Mountpoint,
				createdAt: r.volume.CreatedAt}})
	}
	if r.network != nil {
		values = append(values, generatedDomainDockerObject{kind: "network", id: r.network.ID, name: r.network.Name,
			labels: cloneStringMap(r.network.Labels), data: generatedDomainDockerObjectData{driver: r.network.Driver,
				scope: r.network.Scope, options: cloneStringMap(r.network.Options)}})
	}
	if r.container != nil {
		values = append(values, generatedDomainDockerObject{kind: "container", id: r.container.ID,
			name: caddyContainerName, labels: cloneStringMap(r.container.Labels)})
	}
	values = append(values, r.extra...)
	return values
}

func (r *generatedDomainBootstrapTestRunner) snapshot() ([][]string, int, []byte, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	commands := make([][]string, len(r.commands))
	for index := range r.commands {
		commands[index] = append([]string(nil), r.commands[index]...)
	}
	var nonce string
	if r.volume != nil {
		nonce = r.volume.Labels[generatedDomainBootstrapVolumeNonceLabel]
	}
	return commands, r.effects, append([]byte(nil), r.config...), nonce
}

func generatedDomainConstructionCase(t *testing.T, run func(*testing.T)) {
	t.Helper()
	selected := os.Getenv(generatedDomainConstructionCaseEnvironment)
	if selected == t.Name() {
		run(t)
		return
	}
	if selected != "" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run", "^"+regexp.QuoteMeta(t.Name())+"$",
		"-test.count=1", "-test.v", "-test.timeout=90s")
	command.Env = append(os.Environ(), generatedDomainConstructionCaseEnvironment+"="+t.Name())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated private-construction case failed: context=%v error=%v\n%s", ctx.Err(), err, output)
	}
	t.Logf("isolated private-construction output:\n%s", output)
}

func newGeneratedDomainEmptyConstructionFixture(t *testing.T) (generatedDomainIdentityFixture,
	*generatedDomainBootstrapTestRunner, *candidateFactoryEnvironment,
) {
	t.Helper()
	identity := newGeneratedDomainIdentityFixture(t)
	identity.options.CommandTimeout = time.Minute
	identity.options.OutputLimit = 1 << 20
	runner := newGeneratedDomainBootstrapTestRunner(identity.options.HostPort)
	return identity, runner, &candidateFactoryEnvironment{path: t.TempDir()}
}

func TestGeneratedDomainEmptyBootstrapReceipts(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("sealed_empty_to_stable_with_exact_receipts", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner, environment,
				identity.options)
			if err != nil || !validGeneratedDomainConstructionResult(result.reservation, result) {
				t.Fatalf("sealed empty construction = %#v, %v", result, err)
			}
			classification, err := inspectGeneratedDomainStartup(context.Background(), identity.stores, runner, identity.options)
			if err != nil || classification.disposition != generatedDomainStartupStable ||
				classification.reason != generatedDomainStartupReasonStable {
				t.Fatalf("completed bootstrap classification = %#v, %v", classification, err)
			}
			commands, effects, config, nonce := runner.snapshot()
			_, _, ingressIP := ingressNetworkCandidate(0)
			wanted, err := buildCaddyConfig(map[string]routeRecord{}, ingressIP+":8080")
			if err != nil || !sameCaddyConfig(wanted, config) || effects != 5 ||
				!validGeneratedDomainBootstrapVolumeNonce(nonce) {
				t.Fatalf("bootstrap receipts: effects=%d nonce=%q config=%s error=%v", effects, nonce, config, err)
			}
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
	t.Run("sealed_empty_with_exposed_stopped_network_topology_to_stable", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			runner.exposeStoppedNetworkTopology = true
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner, environment,
				identity.options)
			if err != nil || !validGeneratedDomainConstructionResult(result.reservation, result) {
				t.Fatalf("sealed empty construction with stopped topology = %#v, %v", result, err)
			}
			commands, effects, _, _ := runner.snapshot()
			if effects != 5 || runner.container == nil || runner.network == nil ||
				normalizeID(runner.container.ConfiguredNetworks[caddyNetworkName].NetworkID) != runner.network.ID ||
				len(runner.network.Containers) != 1 {
				t.Fatalf("bootstrap did not reach exact running topology: effects=%d", effects)
			}
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
	t.Run("existing_name_with_distinct_nonce_is_never_adopted", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			runner.volume = &gatewayVolumeIdentityInspection{Name: caddyVolumeName, Driver: "local", Scope: "local",
				Options: map[string]string{}, Labels: map[string]string{
					gatewayV2ManagedLabelKey:                 gatewayV2ManagedContainerLabel,
					gatewayV2IdentityLabelKey:                gatewayV1IdentityVersion,
					generatedDomainBootstrapVolumeNonceLabel: strings.Repeat("b", 64),
				}, Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data",
				CreatedAt: "2026-10-23T00:00:00Z"}
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			commands, effects, _, _ := runner.snapshot()
			if err == nil || result != nil || effects != 0 {
				t.Fatalf("existing-nonce construction = %#v, %v; effects=%d", result, err, effects)
			}
			for _, command := range commands {
				if len(command) >= 2 && command[1] == "create" {
					t.Fatalf("existing volume name was adopted or replaced: %#v", command)
				}
			}
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
}

func TestGeneratedDomainBootstrapPhaseTopologyValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		exposed bool
		started bool
		mutate  func(*gatewayContainerInspection, *gatewayNetworkIdentityInspection)
	}{
		{name: "stopped omitted topology", started: false},
		{name: "stopped exposed topology", exposed: true, started: false},
		{name: "running exact topology", started: true},
		{name: "stopped wrong nonempty network id", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.NetworkID = strings.Repeat("9", 64)
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "stopped crossed configured name", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			delete(container.ConfiguredNetworks, caddyNetworkName)
			container.ConfiguredNetworks["crossed"] = configured
		}},
		{name: "stopped wrong ipam", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.IPAMConfig = &gatewayV2ConfiguredIPAM{IPv4Address: "10.255.255.1"}
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "stopped wrong gateway priority", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.GwPriority++
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "stopped endpoint id", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.EndpointID = strings.Repeat("8", 64)
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "stopped runtime ip", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.IPAddress = configured.IPAMConfig.IPv4Address
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "stopped effective binding", mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			container.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(container.PortBindings)
		}},
		{name: "stopped crossed network member", exposed: true, mutate: func(container *gatewayContainerInspection, network *gatewayNetworkIdentityInspection) {
			member := network.Containers[container.ID]
			delete(network.Containers, container.ID)
			network.Containers[strings.Repeat("7", 64)] = member
		}},
		{name: "stopped extra network member", exposed: true, mutate: func(_ *gatewayContainerInspection, network *gatewayNetworkIdentityInspection) {
			network.Containers[strings.Repeat("7", 64)] = caddyNetworkContainerInspection{
				Name: "crossed", IPv4Address: "10.255.255.1/28"}
		}},
		{name: "stopped wrong network member name", exposed: true, mutate: func(container *gatewayContainerInspection, network *gatewayNetworkIdentityInspection) {
			member := network.Containers[container.ID]
			member.Name = "crossed"
			network.Containers[container.ID] = member
		}},
		{name: "stopped wrong network member ip", exposed: true, mutate: func(container *gatewayContainerInspection, network *gatewayNetworkIdentityInspection) {
			member := network.Containers[container.ID]
			member.IPv4Address = "10.255.255.1/28"
			network.Containers[container.ID] = member
		}},
		{name: "running missing network id", started: true, mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.NetworkID = ""
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "running wrong network id", started: true, mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.NetworkID = strings.Repeat("9", 64)
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "running missing network member", started: true, mutate: func(_ *gatewayContainerInspection, network *gatewayNetworkIdentityInspection) {
			network.Containers = map[string]caddyNetworkContainerInspection{}
		}},
		{name: "running crossed network member", started: true, mutate: func(container *gatewayContainerInspection, network *gatewayNetworkIdentityInspection) {
			member := network.Containers[container.ID]
			delete(network.Containers, container.ID)
			network.Containers[strings.Repeat("7", 64)] = member
		}},
		{name: "running missing ip", started: true, mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.IPAddress = ""
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "running wrong ip", started: true, mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			configured := container.ConfiguredNetworks[caddyNetworkName]
			configured.IPAddress = "10.255.255.1"
			container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "running missing effective binding", started: true, mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			container.EffectivePortBindings = map[string][]map[string]string{}
		}},
		{name: "running crossed effective binding", started: true, mutate: func(container *gatewayContainerInspection, _ *gatewayNetworkIdentityInspection) {
			container.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(container.PortBindings)
			container.EffectivePortBindings["8080/tcp"][0]["HostPort"] = "1"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := generatedDomainBootstrapTopologyFixture(t, test.exposed, test.started)
			if test.mutate != nil {
				test.mutate(runner.container, runner.network)
			}
			valid := generatedDomainBootstrapTestTopologyValid(runner, test.started)
			if test.mutate == nil && !valid {
				t.Fatal("exact bootstrap phase topology was rejected")
			}
			if test.mutate != nil && valid {
				t.Fatal("drifted bootstrap phase topology was accepted")
			}
		})
	}
}

func TestGeneratedDomainBootstrapPhaseDriftIsSticky(t *testing.T) {
	for _, test := range []struct {
		name    string
		effects int
		mutate  func(*generatedDomainBootstrapTestRunner)
	}{
		{name: "stopped configured network identity drift", effects: 3, mutate: func(runner *generatedDomainBootstrapTestRunner) {
			configured := runner.container.ConfiguredNetworks[caddyNetworkName]
			configured.NetworkID = strings.Repeat("9", 64)
			runner.container.ConfiguredNetworks[caddyNetworkName] = configured
		}},
		{name: "running crossed network member", effects: 5, mutate: func(runner *generatedDomainBootstrapTestRunner) {
			member := runner.network.Containers[runner.container.ID]
			delete(runner.network.Containers, runner.container.ID)
			runner.network.Containers[strings.Repeat("7", 64)] = member
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
				drifted := false
				runner.hook = func(event string, _ int) error {
					if event == "census" && !drifted && runner.effects == test.effects {
						drifted = true
						test.mutate(runner)
					}
					return nil
				}
				result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
					environment, identity.options)
				commands, effects, _, _ := runner.snapshot()
				if err == nil || result != nil || !drifted || effects != test.effects || runner.challenges != 0 {
					t.Fatalf("bootstrap phase drift = %#v, %v; drifted=%t effects=%d challenges=%d",
						result, err, drifted, effects, runner.challenges)
				}
				generatedDomainAssertNoBootstrapRetryOrCleanup(t, commands)
			})
		})
	}
}

func generatedDomainBootstrapTopologyFixture(t *testing.T, exposeStopped, started bool) *generatedDomainBootstrapTestRunner {
	t.Helper()
	runner := newGeneratedDomainBootstrapTestRunner(8080)
	runner.exposeStoppedNetworkTopology = exposeStopped
	subnet, gateway, ingressIP := ingressNetworkCandidate(0)
	if _, err := runner.createNetworkLocked([]string{"network", "create", "--subnet", subnet, "--gateway", gateway, caddyNetworkName}); err != nil {
		t.Fatal(err)
	}
	args, err := caddyCreateArgs(runner.image.ID, ingressIP, runner.hostPort)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.createContainerLocked(args); err != nil {
		t.Fatal(err)
	}
	if started {
		if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"container", "start", runner.container.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	return runner
}

func generatedDomainBootstrapTestTopologyValid(runner *generatedDomainBootstrapTestRunner, started bool) bool {
	subnet := runner.network.IPAM.Config[0].Subnet
	_, _, ingressIP := ingressNetworkCandidate(0)
	network := caddyNetworkInspection{Name: runner.network.Name, Driver: runner.network.Driver, Scope: runner.network.Scope,
		Internal: runner.network.Internal, Options: cloneStringMap(runner.network.Options), IPAM: runner.network.IPAM,
		Labels: cloneStringMap(runner.network.Labels), Containers: runner.network.Containers}
	return validGeneratedDomainBootstrapNetwork(network, runner.network.ID, subnet,
		runner.network.IPAM.Config[0].Gateway, ingressIP, started, runner.container.ID) &&
		validGeneratedDomainBootstrapContainer(runner.container.caddyInspection, runner.container.gatewayContainerRuntime,
			runner.container.ID, runner.image.ID, runner.network.ID, ingressIP, runner.hostPort, started)
}

func TestGeneratedDomainEmptyBootstrapEffectBoundaries(t *testing.T) {
	for _, lost := range []struct {
		name    string
		effects int
	}{{"volume", 1}, {"network", 2}, {"container", 3}, {"copy", 4}, {"start", 5}} {
		lost := lost
		t.Run("lost_"+lost.name+"_acknowledgement_is_sticky_without_retry_or_cleanup", func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
				runner.loseAck = lost.name
				result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
					environment, identity.options)
				if err == nil || result != nil {
					t.Fatalf("lost acknowledgement construction = %#v, %v", result, err)
				}
				commands, effects, _, nonce := runner.snapshot()
				if effects != lost.effects || !validGeneratedDomainBootstrapVolumeNonce(nonce) {
					t.Fatalf("lost acknowledgement residual effects=%d nonce=%q", effects, nonce)
				}
				generatedDomainAssertNoBootstrapRetryOrCleanup(t, commands)
				before := len(commands)
				joined, joinErr := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
					environment, identity.options)
				after, _, _, _ := runner.snapshot()
				if joined != nil || !errors.Is(joinErr, err) ||
					!generatedDomainStickyJoinOnlyRecheckedDaemon(after, before) {
					t.Fatalf("sticky lost acknowledgement join = %#v, %v; want %v", joined, joinErr, err)
				}
			})
		})
	}
	for _, effect := range []int{1, 2, 3, 4, 5} {
		effect := effect
		t.Run(fmt.Sprintf("census_drift_after_effect_%d_is_terminal", effect), func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
				drifted := false
				runner.hook = func(event string, _ int) error {
					if event == "census" && !drifted && runner.effects == effect {
						drifted = true
						runner.extra = append(runner.extra, generatedDomainDockerObject{kind: "container",
							id: strings.Repeat(fmt.Sprintf("%x", effect), 64), name: fmt.Sprintf("renamed-drift-%d", effect),
							labels: map[string]string{"io.rig.unexpected": "bootstrap-drift"},
							data:   generatedDomainDockerObjectData{options: map[string]string{}},
						})
					}
					return nil
				}
				result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
					environment, identity.options)
				commands, effects, _, _ := runner.snapshot()
				if err == nil || result != nil || !drifted || effects != effect {
					t.Fatalf("census drift after effect %d = %#v, %v; drifted=%t effects=%d",
						effect, result, err, drifted, effects)
				}
				generatedDomainAssertNoBootstrapRetryOrCleanup(t, commands)
			})
		})
	}
	t.Run("fresh_SQL_fence_refuses_before_first_Docker_effect", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			mutated := false
			runner.hook = func(event string, count int) error {
				if event != "image-inspect" || count != 3 {
					return nil
				}
				_, err := identity.database.Exec(`DROP TABLE lan_gateway_rebind_claims`)
				mutated = err == nil
				return err
			}
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			commands, effects, _, _ := runner.snapshot()
			if err == nil || result != nil || effects != 0 || !mutated {
				t.Fatalf("SQL fence mutation construction = %#v, %v; Docker effects=%d mutated=%t",
					result, err, effects, mutated)
			}
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
}

func generatedDomainAssertNoBootstrapRetryOrCleanup(t *testing.T, commands [][]string) {
	t.Helper()
	generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
	counts := map[string]int{}
	for _, command := range commands {
		if len(command) < 2 {
			continue
		}
		if command[1] == "rm" || command[1] == "stop" {
			t.Fatalf("bootstrap attempted cleanup: %#v", command)
		}
		key := ""
		switch {
		case command[1] == "create":
			key = command[0] + " create"
		case command[0] == "container" && command[1] == "start":
			key = "container start"
		case command[0] == "container" && command[1] == "cp" && len(command) == 4 && command[3] != "-":
			key = "container cp"
		}
		if key != "" {
			counts[key]++
			if counts[key] > 1 {
				t.Fatalf("bootstrap retried %s: %#v", key, commands)
			}
		}
	}
}

func generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t *testing.T, commands [][]string) {
	t.Helper()
	containerID := strings.Repeat("b", 64)
	for _, command := range commands {
		if len(command) < 2 || command[0] != "container" || command[1] != "exec" {
			continue
		}
		if !generatedDomainGatewayAdminCommand(command, containerID) &&
			!generatedDomainGatewayChallengeCommand(command, containerID) {
			t.Fatalf("bootstrap dispatched application or unowned-listener traffic: %#v", command)
		}
	}
}

func TestGeneratedDomainConstructionFinalClassificationAndRelease(t *testing.T) {
	t.Run("final_SQL_fence_blocks_publication_with_retained_residuals", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			mutated := false
			runner.hook = func(event string, count int) error {
				if event != "challenge" || count != 2 {
					return nil
				}
				_, err := identity.database.Exec(`DROP TABLE lan_gateway_rebind_claims`)
				mutated = err == nil
				return err
			}
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			_, effects, _, _ := runner.snapshot()
			if err == nil || result != nil || effects != 5 || !mutated || runner.volume == nil || runner.network == nil ||
				runner.container == nil {
				t.Fatalf("final SQL mutation = %#v, %v; effects=%d mutated=%t", result, err, effects, mutated)
			}
			if _, statErr := os.Stat(filepath.Join(identity.dataRoot, "runtime", "generated-ingress", stateFilename)); statErr != nil {
				t.Fatalf("final SQL mutation removed protected residual: %v", statErr)
			}
			commands, _, _, _ := runner.snapshot()
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
	t.Run("final_process_latch_blocks_publication", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			latched := false
			runner.hook = func(event string, count int) error {
				if event == "challenge" && count == 2 {
					gatewayRebindProcessFailStop.Store(true)
					latched = true
				}
				return nil
			}
			defer gatewayRebindProcessFailStop.Store(false)
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			_, effects, _, _ := runner.snapshot()
			if err == nil || result != nil || effects != 5 || !latched {
				t.Fatalf("final latch = %#v, %v; effects=%d latched=%t", result, err, effects, latched)
			}
			commands, _, _, _ := runner.snapshot()
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
	t.Run("gateway_lock_release_uncertainty_is_sticky", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainEmptyConstructionFixture(t)
			original := managerAcquireGatewayOSLock
			managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
				release, err := original(ctx, store)
				if err != nil {
					return nil, err
				}
				return func() error { return errors.Join(release(), errors.New("injected release uncertainty")) }, nil
			}
			defer func() { managerAcquireGatewayOSLock = original }()
			result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			_, effects, _, _ := runner.snapshot()
			if err == nil || result != nil || effects != 5 {
				t.Fatalf("release uncertainty = %#v, %v; effects=%d", result, err, effects)
			}
			joined, joinErr := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			if joined != nil || joinErr == nil {
				t.Fatalf("release uncertainty was not sticky: %#v, %v", joined, joinErr)
			}
			commands, _, _, _ := runner.snapshot()
			generatedDomainAssertNoApplicationOrUnownedListenerTraffic(t, commands)
		})
	})
}
