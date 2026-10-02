package generatedingress

import (
	"reflect"
	"strings"
	"testing"
)

func TestGatewayV2CreateArgsBindExactResourcesAndPorts(t *testing.T) {
	source, input := upgradeTestPreparation(t)
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	journal.Phase = gatewayPhaseStageIntent

	network, err := gatewayV2NetworkCreateArgs(state, journal)
	if err != nil || network[len(network)-1] != state.Identity.IngressNetwork ||
		!reflect.DeepEqual(gatewayV2ArgValues(network, "--subnet"), []string{state.Network.Subnet}) ||
		!reflect.DeepEqual(gatewayV2ArgValues(network, "--gateway"), []string{state.Network.GatewayIPv4}) {
		t.Fatalf("network create args = %v, err = %v", network, err)
	}
	configVolume, err := gatewayV2VolumeCreateArgs(state, journal, gatewayV2ConfigVolumeRole)
	if err != nil || configVolume[len(configVolume)-1] != state.Identity.ConfigVolume {
		t.Fatalf("config volume create args = %v, err = %v", configVolume, err)
	}
	dataVolume, err := gatewayV2VolumeCreateArgs(state, journal, gatewayV2DataVolumeRole)
	if err != nil || dataVolume[len(dataVolume)-1] != state.Identity.DataVolume {
		t.Fatalf("data volume create args = %v, err = %v", dataVolume, err)
	}
	if reflect.DeepEqual(gatewayV2ArgValues(configVolume, "--label"), gatewayV2ArgValues(dataVolume, "--label")) {
		t.Fatal("config and data volumes have the same role labels")
	}

	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Resources.StageContainerID = ""
	journal.Resources.FinalContainerID = ""
	imageID := "sha256:" + journal.Resources.ImageID
	stage, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2StageContainerRole, imageID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gatewayV2ArgValues(stage, "--publish"), gatewayV2ExpectedLANPublishArgs(state)) ||
		!reflect.DeepEqual(gatewayV2ArgValues(stage, "--restart"), []string{"no"}) ||
		!reflect.DeepEqual(stage[len(stage)-4:], []string{imageID, "run", "--config", "/config/stage.json"}) {
		t.Fatalf("stage container args = %v", stage)
	}
	assertGatewayV2CreateHardening(t, stage)

	journal.Phase = gatewayPhaseTransferIntent
	journal.Resources.StageContainerID = strings.Repeat("c", 64)
	final, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2FinalContainerRole, imageID)
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := append(gatewayV2ExpectedLANPublishArgs(state), "127.0.0.1:8080:8080/tcp")
	if !reflect.DeepEqual(gatewayV2ArgValues(final, "--publish"), wantPorts) ||
		!reflect.DeepEqual(gatewayV2ArgValues(final, "--restart"), []string{"unless-stopped"}) ||
		!reflect.DeepEqual(final[len(final)-4:], []string{imageID, "run", "--config", "/config/active.json"}) {
		t.Fatalf("final container args = %v", final)
	}
	assertGatewayV2CreateHardening(t, final)
}

func TestGatewayV2CreateArgsRejectUnboundAndOutOfPhaseResources(t *testing.T) {
	source, input := upgradeTestPreparation(t)
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayV2NetworkCreateArgs(state, journal); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("network create before stage intent: %v", err)
	}
	journal.Phase = gatewayPhaseStageIntent
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Resources.StageContainerID = ""
	journal.Resources.FinalContainerID = ""
	if _, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2StageContainerRole, "sha256:"+strings.Repeat("f", 64)); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("unbound image ID: %v", err)
	}
	journal.Resources.IngressNetworkID = ""
	if _, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2StageContainerRole, "sha256:"+journal.Resources.ImageID); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("unbound network: %v", err)
	}
	journal.Resources.IngressNetworkID = strings.Repeat("6", 64)
	if _, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2FinalContainerRole, "sha256:"+journal.Resources.ImageID); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("final created during stage intent: %v", err)
	}
	if _, err := gatewayV2NetworkCreateArgs(state, journal); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("bound network recreated: %v", err)
	}
	if _, err := gatewayV2VolumeCreateArgs(state, journal, gatewayV2ConfigVolumeRole); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("bound config volume recreated: %v", err)
	}
}

func gatewayV2ArgValues(args []string, flag string) []string {
	var values []string
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			values = append(values, args[index+1])
		}
	}
	return values
}

func gatewayV2ExpectedLANPublishArgs(state gatewayV2RouteState) []string {
	var result []string
	for port := state.Profile.PortStart; ; port++ {
		value := strconvForGatewayTest(port)
		result = append(result, state.Profile.SelectedIPv4+":"+value+":"+value+"/tcp")
		if port == state.Profile.PortEnd {
			break
		}
	}
	return result
}

func assertGatewayV2CreateHardening(t *testing.T, args []string) {
	t.Helper()
	for flag, want := range map[string][]string{
		"--user": {"1000:1000"}, "--cap-drop": {"ALL"}, "--cap-add": {"NET_BIND_SERVICE"},
		"--security-opt": {"no-new-privileges"}, "--memory": {"268435456"}, "--memory-swap": {"268435456"},
		"--pids-limit": {"128"}, "--log-driver": {"local"},
	} {
		if got := gatewayV2ArgValues(args, flag); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %v, want %v", flag, got, want)
		}
	}
	if !reflect.DeepEqual(gatewayV2ArgValues(args, "--mount"), []string{
		"type=volume,src=rig-generated-caddy-config-v2,dst=/config",
		"type=volume,src=rig-generated-caddy-data-v2,dst=/data",
	}) || !reflect.DeepEqual(gatewayV2ArgValues(args, "--env"), []string{"XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"}) {
		t.Fatalf("mount or environment contract changed: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), " --read-only ") {
		t.Fatalf("read-only root missing: %v", args)
	}
}
