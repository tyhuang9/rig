package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/generatedbuilder"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const (
	generatedDomainColdBuilderConfiguredMountsProjection = `[{{range $index, $mount := .HostConfig.Mounts}}{{if $index}},{{end}}` +
		`{"Type":{{json $mount.Type}},"Source":{{json $mount.Source}},"Target":{{json $mount.Target}},` +
		`"ReadOnly":{{json $mount.ReadOnly}},"TmpfsOptions":{{if $mount.TmpfsOptions}}` +
		`{"SizeBytes":{{json $mount.TmpfsOptions.SizeBytes}},"Mode":{{json $mount.TmpfsOptions.Mode}}}` +
		`{{else}}null{{end}}}{{end}}]`
	generatedDomainColdBuilderActiveMountsProjection = `[{{range $index, $mount := .Mounts}}{{if $index}},{{end}}` +
		`{"Type":{{json $mount.Type}},"Source":{{json $mount.Source}},` +
		`"Destination":{{json $mount.Destination}},"Mode":{{json $mount.Mode}},` +
		`"RW":{{json $mount.RW}},"Propagation":{{json $mount.Propagation}}}{{end}}]`
	generatedDomainColdBuilderNetworksProjection = `{ {{$first := true}}{{range $name, $network := .NetworkSettings.Networks}}` +
		`{{if not $first}},{{end}}{{$first = false}}{{json $name}}:{"NetworkID":{{json $network.NetworkID}}}{{end}}}`
	generatedDomainColdBuilderContainerInspectFormat = `{"Id":{{json .ID}},"Name":{{json .Name}},` +
		`"Image":{{json .Image}},"State":{"Running":{{json .State.Running}},` +
		`"Paused":{{json .State.Paused}},"Restarting":{{json .State.Restarting}}},` +
		`"Config":{"Image":{{json .Config.Image}},"Cmd":{{json .Config.Cmd}},` +
		`"Labels":{{json .Config.Labels}}},"HostConfig":{"Memory":{{json .HostConfig.Memory}},` +
		`"MemorySwap":{{json .HostConfig.MemorySwap}},"CpuPeriod":{{json .HostConfig.CpuPeriod}},` +
		`"CpuQuota":{{json .HostConfig.CpuQuota}},"PidsLimit":{{json .HostConfig.PidsLimit}},` +
		`"NetworkMode":{{json .HostConfig.NetworkMode}},"Privileged":{{json .HostConfig.Privileged}},` +
		`"Binds":{{json .HostConfig.Binds}},"Mounts":` + generatedDomainColdBuilderConfiguredMountsProjection + `,` +
		`"PortBindings":{{json .HostConfig.PortBindings}},"LogConfig":{"Type":{{json .HostConfig.LogConfig.Type}},` +
		`"Config":{{json .HostConfig.LogConfig.Config}}},"RestartPolicy":{"Name":` +
		`{{json .HostConfig.RestartPolicy.Name}}}},"NetworkSettings":{"Networks":` +
		generatedDomainColdBuilderNetworksProjection + `},"Mounts":` + generatedDomainColdBuilderActiveMountsProjection + `}`
	generatedDomainColdBuilderNetworkInspectFormat = `{"Id":{{json .ID}},"Name":{{json .Name}},` +
		`"Driver":{{json .Driver}},"Scope":{{json .Scope}},"Internal":{{json .Internal}},` +
		`"Labels":{{json .Labels}},"Options":{{json .Options}},"Containers":{ ` +
		`{{$first := true}}{{range $id, $container := .Containers}}{{if not $first}},{{end}}` +
		`{{$first = false}}{{json $id}}:{"Name":{{json $container.Name}},` +
		`"IPv4Address":{{json $container.IPv4Address}}}{{end}}}}`
	generatedDomainColdBuilderImageInspectFormat = `{"Id":{{json .ID}},"Labels":{{json .Config.Labels}}}`
)

type generatedDomainColdBuilderAccount struct {
	containerID, networkID string
}

type generatedDomainColdBuilderState struct {
	directory    *securetemp.GeneratedBuilderDirectory
	identityFile *securetemp.GeneratedBuilderFileSnapshot
	identity     generatedbuilder.Identity
	found        bool
}

type generatedDomainColdBuilderPhysical struct {
	container generatedbuilder.Container
	network   generatedbuilder.Network
	image     generatedbuilder.Image
}

func (a generatedDomainColdBuilderAccount) accounts(object generatedDomainDockerObject) bool {
	return object.kind == "container" && object.id == a.containerID ||
		object.kind == "network" && object.id == a.networkID
}

func attestGeneratedDomainColdBuilder(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority, snapshot generatedDomainDockerStartupSnapshot,
) (generatedDomainColdBuilderAccount, error) {
	if ctx == nil || manager == nil || authority == nil || manager.runner != authority.process ||
		manager.options.DataRoot == "" ||
		manager.options.DockerExecutable != authority.options.DockerExecutable ||
		manager.options.DockerEndpoint != authority.options.DockerEndpoint ||
		manager.options.DockerConfigDirectory != authority.options.DockerConfigDirectory ||
		manager.options.WorkingDirectory != authority.options.WorkingDirectory ||
		manager.options.CommandTimeout != authority.options.CommandTimeout ||
		manager.options.OutputLimit != authority.options.OutputLimit {
		return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
	}
	initial, err := readGeneratedDomainColdBuilderState(manager.options.DataRoot)
	if err != nil {
		return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
	}
	containerObject, networkObject, objectCount := generatedDomainColdBuilderObjects(snapshot.objects, initial.identity)
	if !initial.found {
		if objectCount != 0 {
			return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
		}
		final, finalErr := readGeneratedDomainColdBuilderState(manager.options.DataRoot)
		if finalErr != nil || final.found || !sameGeneratedDomainColdBuilderDirectory(initial.directory, final.directory) {
			return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
		}
		return generatedDomainColdBuilderAccount{}, nil
	}
	if objectCount != 2 || containerObject == nil || networkObject == nil ||
		!generatedDomainCanonicalDockerID(containerObject.id, false) ||
		!generatedDomainCanonicalDockerID(networkObject.id, false) ||
		!generatedDomainExactRigLabels(containerObject.labels,
			generatedbuilder.ContainerLabels(initial.identity, generatedbuilder.DefaultStateQuotaBytes)) ||
		!generatedDomainExactRigLabels(networkObject.labels, generatedbuilder.NetworkLabels(initial.identity)) ||
		networkObject.data.driver != "bridge" || networkObject.data.scope != "local" ||
		!reflect.DeepEqual(networkObject.data.options,
			map[string]string{"com.docker.network.bridge.enable_icc": "false"}) {
		return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
	}
	reader := &generatedDomainStartupReader{runner: manager.runner,
		options: generatedDomainOptions{DockerExecutable: manager.options.DockerExecutable,
			DockerEndpoint:        manager.options.DockerEndpoint,
			DockerConfigDirectory: manager.options.DockerConfigDirectory,
			WorkingDirectory:      manager.options.WorkingDirectory,
			DataRoot:              manager.options.DataRoot,
			CommandTimeout:        manager.options.CommandTimeout,
			OutputLimit:           manager.options.OutputLimit},
		environment: append([]string(nil), manager.dockerEnv...)}
	first, err := readGeneratedDomainColdBuilderPhysical(ctx, reader, *containerObject, *networkObject)
	if err != nil || !matchesGeneratedDomainColdBuilderPhysical(first, initial.identity,
		containerObject.id, networkObject.id) {
		return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
	}
	second, err := readGeneratedDomainColdBuilderPhysical(ctx, reader, *containerObject, *networkObject)
	if err != nil || !reflect.DeepEqual(first, second) {
		return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
	}
	final, err := readGeneratedDomainColdBuilderState(manager.options.DataRoot)
	if err != nil || !final.found || !initial.directory.Same(final.directory) ||
		!initial.identityFile.Same(final.identityFile) || initial.identity != final.identity {
		return generatedDomainColdBuilderAccount{}, errGeneratedDomainConstructionRefused
	}
	return generatedDomainColdBuilderAccount{containerID: containerObject.id, networkID: networkObject.id}, nil
}

func sameGeneratedDomainColdBuilderDirectory(left, right *securetemp.GeneratedBuilderDirectory) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Same(right)
}

func readGeneratedDomainColdBuilderState(dataRoot string) (generatedDomainColdBuilderState, error) {
	directory, err := securetemp.OpenGeneratedBuilderDirectory(dataRoot)
	if errors.Is(err, os.ErrNotExist) {
		return generatedDomainColdBuilderState{}, nil
	}
	if err != nil {
		return generatedDomainColdBuilderState{}, err
	}
	legacy, legacyErr := directory.ReadFile(generatedbuilder.LegacyIdentityFilename,
		generatedbuilder.IdentityMaximumBytes)
	clear(legacy)
	if legacyErr == nil || !errors.Is(legacyErr, os.ErrNotExist) {
		return generatedDomainColdBuilderState{}, errors.New("legacy generated builder identity is present or unreadable")
	}
	body, identityFile, err := directory.ReadFileSnapshot(generatedbuilder.IdentityFilename,
		generatedbuilder.IdentityMaximumBytes)
	if errors.Is(err, os.ErrNotExist) {
		return generatedDomainColdBuilderState{directory: directory}, nil
	}
	if err != nil {
		return generatedDomainColdBuilderState{}, err
	}
	defer clear(body)
	identity, err := generatedbuilder.DecodeIdentity(body)
	if err != nil {
		return generatedDomainColdBuilderState{}, err
	}
	return generatedDomainColdBuilderState{directory: directory, identityFile: identityFile,
		identity: identity, found: true}, nil
}

func generatedDomainColdBuilderObjects(objects []generatedDomainDockerObject,
	identity generatedbuilder.Identity,
) (*generatedDomainDockerObject, *generatedDomainDockerObject, int) {
	var container, network *generatedDomainDockerObject
	count := 0
	for index := range objects {
		object := &objects[index]
		if !generatedDomainColdBuilderObject(*object) {
			continue
		}
		count++
		switch {
		case object.kind == "container" && object.name == generatedbuilder.ContainerName(identity):
			if container == nil {
				container = object
			}
		case object.kind == "network" && object.name == identity.NetworkName:
			if network == nil {
				network = object
			}
		}
	}
	return container, network, count
}

func generatedDomainColdBuilderObject(object generatedDomainDockerObject) bool {
	if strings.HasPrefix(object.name, "rig-buildkitd-") || strings.HasPrefix(object.name, "rig-buildnet-") {
		return true
	}
	if object.labels["rig.controller"] == "generated-builder" {
		return true
	}
	for _, key := range []string{"rig.builder", "rig.network", "rig.quota.bytes"} {
		if _, exists := object.labels[key]; exists {
			return true
		}
	}
	return false
}

func readGeneratedDomainColdBuilderPhysical(ctx context.Context, reader *generatedDomainStartupReader,
	containerObject, networkObject generatedDomainDockerObject,
) (generatedDomainColdBuilderPhysical, error) {
	deadline := time.Now().Add(generatedDomainStartupObservationTimeout)
	containerBody, err := reader.runRead(ctx, deadline, "container", "inspect", "--format",
		generatedDomainColdBuilderContainerInspectFormat, containerObject.id)
	if err != nil {
		return generatedDomainColdBuilderPhysical{}, err
	}
	container, decodeErr := generatedbuilder.DecodeContainer(containerBody)
	clear(containerBody)
	if decodeErr != nil {
		return generatedDomainColdBuilderPhysical{}, decodeErr
	}
	if !generatedDomainCanonicalDockerID(container.Image, true) {
		return generatedDomainColdBuilderPhysical{}, errors.New("generated builder image identity is invalid")
	}
	networkBody, err := reader.runRead(ctx, deadline, "network", "inspect", "--format",
		generatedDomainColdBuilderNetworkInspectFormat, networkObject.id)
	if err != nil {
		return generatedDomainColdBuilderPhysical{}, err
	}
	network, decodeErr := generatedbuilder.DecodeNetwork(networkBody)
	clear(networkBody)
	if decodeErr != nil {
		return generatedDomainColdBuilderPhysical{}, decodeErr
	}
	imageBody, err := reader.runRead(ctx, deadline, "image", "inspect", "--format",
		generatedDomainColdBuilderImageInspectFormat, container.Image)
	if err != nil {
		return generatedDomainColdBuilderPhysical{}, err
	}
	image, decodeErr := generatedbuilder.DecodeImage(imageBody)
	clear(imageBody)
	if decodeErr != nil {
		return generatedDomainColdBuilderPhysical{}, decodeErr
	}
	return generatedDomainColdBuilderPhysical{container: container, network: network, image: image}, nil
}

func matchesGeneratedDomainColdBuilderPhysical(value generatedDomainColdBuilderPhysical,
	identity generatedbuilder.Identity, containerID, networkID string,
) bool {
	if value.container.ID != containerID || value.network.ID != networkID ||
		!generatedbuilder.MatchesContainerConfiguration(value.container, identity,
			generatedbuilder.DefaultStateQuotaBytes) || !generatedbuilder.ContainerReady(value.container) ||
		!generatedbuilder.MatchesContainerNetwork(value.container, identity, networkID) ||
		!generatedbuilder.MatchesNetwork(value.network, identity) ||
		!reflect.DeepEqual(value.network.Options,
			map[string]string{"com.docker.network.bridge.enable_icc": "false"}) ||
		!generatedDomainExactRigLabels(value.container.Config.Labels,
			generatedbuilder.ContainerLabels(identity, generatedbuilder.DefaultStateQuotaBytes)) ||
		!generatedDomainExactRigLabels(value.network.Labels, generatedbuilder.NetworkLabels(identity)) ||
		len(value.network.Containers) != 1 || value.image.ID != value.container.Image {
		return false
	}
	member, ok := value.network.Containers[containerID]
	if !ok || member.Name != generatedbuilder.ContainerName(identity) {
		return false
	}
	for key := range value.image.Labels {
		if strings.HasPrefix(key, "rig.") || strings.HasPrefix(key, "io.rig.") {
			return false
		}
	}
	return true
}
