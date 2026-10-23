package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainStartupReader struct {
	runner      runtimeprocess.CommandRunner
	options     generatedDomainOptions
	environment []string
	daemonID    string
	expectation generatedDomainGatewayExpectation
	census      generatedDomainGatewayExpectation
	recovery    generatedDomainRecoveryDockerExpectation
}

type generatedDomainRecoveryDockerKind uint8

const (
	generatedDomainRecoveryDockerNone generatedDomainRecoveryDockerKind = iota
	generatedDomainRecoveryDockerUpgrade
	generatedDomainRecoveryDockerCurrent
	generatedDomainRecoveryDockerRebind
)

type generatedDomainRecoveryDockerExpectation struct {
	kind         generatedDomainRecoveryDockerKind
	recoveries   []generatedDomainStartupRecovery
	alternatives [][]generatedDomainRecoveryDockerDescriptor
	imageID      string
	gateway      generatedDomainGatewayExpectation
	source       *routeState
	upgrade      *gatewayUpgradeGenerationSelection
	rebindIntent *gatewayRebindProtectedIntentV2
	rebindEffect *gatewayRebindTypedEffectProgress
}

type generatedDomainRecoveryDockerDescriptor struct {
	kind   string
	id     string
	name   string
	labels map[string]string
	data   generatedDomainDockerObjectData
}

type generatedDomainGatewayExpectation struct {
	kind        generatedDomainGatewayKind
	stateDigest string
	nativeV1    *routeState
	nativeV2    *gatewayV2RouteState
	journal     *gatewayMigrationJournal
	upgrade     *gatewayUpgradeGenerationSelection
	currentV1   *gatewayCurrentRouteState
	currentV2   *gatewayCurrentRouteStateV2
	terminal    *gatewayRebindAttemptTerminalView
	names       map[string]map[string]struct{}
	ids         map[string]map[string]struct{}
}

type generatedDomainDockerObject struct {
	kind   string
	id     string
	name   string
	labels map[string]string
	data   generatedDomainDockerObjectData
}

type generatedDomainDockerObjectData struct {
	driver     string
	scope      string
	options    map[string]string
	mountpoint string
	createdAt  string
	contentID  string
}

type generatedDomainDockerStartupSnapshot struct {
	daemonID          string
	objects           []generatedDomainDockerObject
	gateway           generatedDomainGatewayAttestation
	effectEmpty       bool
	cold              bool
	ambiguous         bool
	recoveryAccounted bool
}

type generatedDomainDockerCensusBudget struct {
	entries int
	bytes   int
}

// Startup observations use a private projection instead of decoding Docker's
// full inspect payload. Docker may add fields to MountPoint, EndpointSettings,
// and NetworkResource without changing the fields that form Rig's ownership
// proof. Projecting the exact consumed fields keeps strict JSON decoding useful
// while preventing unrelated Docker API growth from changing that proof.
const (
	generatedDomainStartupMountsProjection = `[{{range $index, $mount := .Mounts}}{{if $index}},{{end}}` +
		`{"Type":{{json $mount.Type}},"Name":{{json $mount.Name}},"Destination":{{json $mount.Destination}},"RW":{{json $mount.RW}}}{{end}}]`
	generatedDomainStartupNetworkAttachmentsProjection = `{{if .NetworkSettings}}{ ` +
		`{{$networkFirst := true}}{{range $name, $network := .NetworkSettings.Networks}}` +
		`{{if not $networkFirst}},{{end}}{{$networkFirst = false}}{{json $name}}:` +
		`{"Aliases":{{json $network.Aliases}},"IPAddress":{{json $network.IPAddress}},` +
		`"GwPriority":{{json $network.GwPriority}},"IPv6Gateway":{{json $network.IPv6Gateway}}}{{end}}` +
		`}{{else}}null{{end}}`
	generatedDomainStartupConfiguredNetworksProjection = `{{if .NetworkSettings}}{ ` +
		`{{$configuredFirst := true}}{{range $name, $network := .NetworkSettings.Networks}}` +
		`{{if not $configuredFirst}},{{end}}{{$configuredFirst = false}}{{json $name}}:{"IPAMConfig":` +
		`{{if $network.IPAMConfig}}{"IPv4Address":{{json $network.IPAMConfig.IPv4Address}},` +
		`"IPv6Address":{{json $network.IPAMConfig.IPv6Address}}}{{else}}null{{end}},` +
		`"NetworkID":{{json $network.NetworkID}},"EndpointID":{{json $network.EndpointID}},` +
		`"IPAddress":{{json $network.IPAddress}},"GwPriority":{{json $network.GwPriority}},` +
		`"IPv6Gateway":{{json $network.IPv6Gateway}}}{{end}}}{{else}}null{{end}}`
	generatedDomainStartupContainerInspectFormat = `{"id":{{json .ID}},"name":{{json .Name}},` +
		`"image":{{json .Image}},"labels":{{json .Config.Labels}},"hostname":{{json .Config.Hostname}},` +
		`"user":{{json .Config.User}},"env":{{json .Config.Env}},"entrypoint":{{json .Config.Entrypoint}},` +
		`"cmd":{{json .Config.Cmd}},"readOnly":{{json .HostConfig.ReadonlyRootfs}},` +
		`"privileged":{{json .HostConfig.Privileged}},"capAdd":{{json .HostConfig.CapAdd}},` +
		`"capDrop":{{json .HostConfig.CapDrop}},"securityOpt":{{json .HostConfig.SecurityOpt}},` +
		`"binds":{{json .HostConfig.Binds}},"mounts":` + generatedDomainStartupMountsProjection + `,` +
		`"tmpfs":{{json .HostConfig.Tmpfs}},"memory":{{json .HostConfig.Memory}},` +
		`"memorySwap":{{json .HostConfig.MemorySwap}},"nanoCpus":{{json .HostConfig.NanoCPUs}},` +
		`"pidsLimit":{{json .HostConfig.PidsLimit}},"logType":{{json .HostConfig.LogConfig.Type}},` +
		`"logConfig":{{json .HostConfig.LogConfig.Config}},"restart":{{json .HostConfig.RestartPolicy.Name}},` +
		`"networkMode":{{json .HostConfig.NetworkMode}},"ulimits":{{json .HostConfig.Ulimits}},` +
		`"running":{{json .State.Running}},"restarting":{{json .State.Restarting}},` +
		`"portBindings":{{json .HostConfig.PortBindings}},"networks":` +
		generatedDomainStartupNetworkAttachmentsProjection + `,"paused":{{json .State.Paused}},` +
		`"dead":{{json .State.Dead}},"restartCount":{{json .RestartCount}},"effectivePortBindings":` +
		`{{if .NetworkSettings}}{{json .NetworkSettings.Ports}}{{else}}null{{end}},"configuredNetworks":` +
		generatedDomainStartupConfiguredNetworksProjection + `}`
	generatedDomainStartupNetworkInspectFormat = `{"Id":{{json .ID}},"Name":{{json .Name}},` +
		`"Driver":{{json .Driver}},"Scope":{{json .Scope}},"Internal":{{json .Internal}},` +
		`"Options":{{json .Options}},"IPAM":{"Config":[{{range $index, $config := .IPAM.Config}}` +
		`{{if $index}},{{end}}{"Subnet":{{json $config.Subnet}},"Gateway":{{json $config.Gateway}}}{{end}}]},` +
		`"Labels":{{json .Labels}},"Containers":{ ` +
		`{{$containerFirst := true}}{{range $id, $container := .Containers}}` +
		`{{if not $containerFirst}},{{end}}{{$containerFirst = false}}{{json $id}}:` +
		`{"Name":{{json $container.Name}},"IPv4Address":{{json $container.IPv4Address}}}{{end}}}}`
)

func (b *generatedDomainDockerCensusBudget) charge(body []byte, entries int) error {
	if b == nil || entries < 0 || len(body) > generatedDomainStartupMaximumBytes-b.bytes ||
		entries > generatedDomainStartupMaximumEntries-b.entries {
		return errors.New("generated domain Docker census exceeds its bound")
	}
	b.bytes += len(body)
	b.entries += entries
	return nil
}

type generatedDomainGatewayKind uint8

const (
	generatedDomainGatewayNone generatedDomainGatewayKind = iota
	generatedDomainGatewayNativeV1
	generatedDomainGatewayNativeV2
	generatedDomainGatewayCurrentV1
	generatedDomainGatewayCurrentV2
)

// generatedDomainGatewayAttestation contains only descriptive correlation
// fields. exact is set solely by the concrete passive reader after static,
// restart, live-config and gateway-owned response checks complete in order.
type generatedDomainGatewayAttestation struct {
	kind                generatedDomainGatewayKind
	operationID         string
	protectedGeneration uint64
	stateDigest         string
	exact               bool
}

type generatedDomainDockerListEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type generatedDomainDockerInspectProjection struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Options    map[string]string `json:"options"`
	Mountpoint string            `json:"mountpoint"`
	CreatedAt  string            `json:"createdAt"`
	ContentID  string            `json:"contentId"`
}

const (
	generatedDomainContainerListFormat    = `{"id":{{json .ID}}}`
	generatedDomainNetworkListFormat      = `{"id":{{json .ID}}}`
	generatedDomainVolumeListFormat       = `{"name":{{json .Name}}}`
	generatedDomainContainerInspectFormat = `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Config.Labels}}}`
	generatedDomainNetworkInspectFormat   = `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Labels}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}}}`
	generatedDomainVolumeInspectFormat    = `{"name":{{json .Name}},"labels":{{json .Labels}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}},"mountpoint":{{json .Mountpoint}},"createdAt":{{json .CreatedAt}}}`
	generatedDomainImageInspectFormat     = `{"id":{{json .ID}},"labels":{{json .Config.Labels}},"contentId":{{json .ID}}}`
)

func newGeneratedDomainStartupReader(runner runtimeprocess.CommandRunner, options generatedDomainOptions,
	daemonID string,
) (*generatedDomainStartupReader, error) {
	environment, err := dockerEnvironment(options.DockerEndpoint, options.DockerConfigDirectory)
	if err != nil || !generatedCandidatePointerIdentity(runner) || daemonID == "" {
		return nil, errors.New("generated domain Docker reader is invalid")
	}
	return &generatedDomainStartupReader{runner: runner, options: options,
		environment: append([]string(nil), environment...), daemonID: daemonID}, nil
}

func readGeneratedDomainDockerStartup(ctx context.Context, reader *generatedDomainStartupReader) (generatedDomainDockerStartupSnapshot, error) {
	if ctx == nil || reader == nil || !generatedCandidatePointerIdentity(reader.runner) || reader.daemonID == "" {
		return generatedDomainDockerStartupSnapshot{}, errors.New("generated domain Docker startup input is invalid")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(generatedDomainStartupObservationTimeout)
	}
	firstDaemon, err := inspectGeneratedDockerDaemonIdentity(ctx, reader.runner, reader.options.DockerExecutable,
		reader.environment, generatedDomainStartupCommandTimeout(reader.options, time.Until(deadline)))
	if err != nil || firstDaemon != reader.daemonID {
		return generatedDomainDockerStartupSnapshot{}, errors.New("generated domain Docker daemon changed")
	}
	first, inventories, err := reader.readDockerCensus(ctx, deadline)
	if err != nil {
		return generatedDomainDockerStartupSnapshot{}, err
	}
	second, confirmedInventories, err := reader.readDockerCensus(ctx, deadline)
	if err != nil || !reflect.DeepEqual(inventories, confirmedInventories) || !sameGeneratedDomainDockerStartup(first, second) {
		return generatedDomainDockerStartupSnapshot{}, errors.New("generated domain Docker census changed")
	}
	lastDaemon, err := inspectGeneratedDockerDaemonIdentity(ctx, reader.runner, reader.options.DockerExecutable,
		reader.environment, generatedDomainStartupCommandTimeout(reader.options, time.Until(deadline)))
	if err != nil || lastDaemon != firstDaemon {
		return generatedDomainDockerStartupSnapshot{}, errors.New("generated domain Docker daemon changed")
	}
	second.daemonID = lastDaemon
	firstGateway, err := reader.attestGateway(ctx, deadline, second)
	if err != nil {
		return generatedDomainDockerStartupSnapshot{}, err
	}
	secondGateway, err := reader.attestGateway(ctx, deadline, second)
	if err != nil || firstGateway != secondGateway {
		return generatedDomainDockerStartupSnapshot{}, errors.New("generated domain gateway attestation changed")
	}
	second.gateway = secondGateway
	if reader.recovery.kind != generatedDomainRecoveryDockerNone {
		firstRecovery, firstErr := reader.attestRecovery(ctx, deadline, second)
		secondRecovery, secondErr := reader.attestRecovery(ctx, deadline, second)
		second.recoveryAccounted = second.recoveryAccounted && firstErr == nil && secondErr == nil &&
			firstRecovery && secondRecovery
		if reader.recovery.gateway.kind != generatedDomainGatewayNone {
			second.recoveryAccounted = second.recoveryAccounted && second.gateway.exact
		}
	}
	return second, nil
}

func (r *generatedDomainStartupReader) readDockerCensus(ctx context.Context, deadline time.Time) (
	generatedDomainDockerStartupSnapshot, map[string][]string, error,
) {
	type censusKind struct {
		kind       string
		list       []string
		format     string
		inspect    []string
		identifier func(generatedDomainDockerListEntry) string
	}
	kinds := []censusKind{
		{"container", []string{"container", "ls", "--all", "--no-trunc", "--format", generatedDomainContainerListFormat}, generatedDomainContainerInspectFormat, []string{"container", "inspect"}, func(v generatedDomainDockerListEntry) string { return v.ID }},
		{"network", []string{"network", "ls", "--no-trunc", "--format", generatedDomainNetworkListFormat}, generatedDomainNetworkInspectFormat, []string{"network", "inspect"}, func(v generatedDomainDockerListEntry) string { return v.ID }},
		{"volume", []string{"volume", "ls", "--format", generatedDomainVolumeListFormat}, generatedDomainVolumeInspectFormat, []string{"volume", "inspect"}, func(v generatedDomainDockerListEntry) string { return v.Name }},
	}
	result := generatedDomainDockerStartupSnapshot{daemonID: r.daemonID, effectEmpty: true}
	budget := &generatedDomainDockerCensusBudget{}
	inventories := make(map[string][]string, len(kinds)+1)
	seenObjects := make(map[string]struct{})
	for _, kind := range kinds {
		body, err := r.runRead(ctx, deadline, kind.list...)
		if err != nil {
			return generatedDomainDockerStartupSnapshot{}, nil, err
		}
		entryCount := bytes.Count(body, []byte{'\n'})
		if len(bytes.TrimSpace(body)) != 0 && (len(body) == 0 || body[len(body)-1] != '\n') {
			entryCount++
		}
		if err := budget.charge(body, entryCount); err != nil {
			clear(body)
			return generatedDomainDockerStartupSnapshot{}, nil, err
		}
		entries, err := decodeGeneratedDomainDockerList(body)
		clear(body)
		if err != nil || len(entries) > generatedDomainStartupMaximumEntries {
			return generatedDomainDockerStartupSnapshot{}, nil, errors.New("generated domain Docker inventory is invalid")
		}
		ids := make([]string, 0, len(entries))
		for _, entry := range entries {
			id := kind.identifier(entry)
			if id == "" || strings.TrimSpace(id) != id ||
				(kind.kind != "volume" && !generatedDomainCanonicalDockerID(id, false)) {
				return generatedDomainDockerStartupSnapshot{}, nil, errors.New("generated domain Docker identity is invalid")
			}
			key := kind.kind + "\x00" + id
			if _, duplicate := seenObjects[key]; duplicate {
				return generatedDomainDockerStartupSnapshot{}, nil, errors.New("generated domain Docker inventory is duplicated")
			}
			seenObjects[key] = struct{}{}
			ids = append(ids, id)
			projection, err := r.inspectDockerProjection(ctx, deadline, budget, kind.inspect, kind.format, id)
			if err != nil || (kind.kind != "volume" && projection.ID != id) ||
				(kind.kind == "volume" && projection.Name != id) {
				return generatedDomainDockerStartupSnapshot{}, nil, errors.New("generated domain Docker listed object changed")
			}
			if r.generatedDomainDockerObjectSelected(kind.kind, id, projection.Name, projection.Labels) {
				object := generatedDomainDockerObject{kind: kind.kind, id: projection.ID,
					name: strings.TrimPrefix(projection.Name, "/"), labels: cloneStringMap(projection.Labels),
					data: generatedDomainDockerObjectData{driver: projection.Driver, scope: projection.Scope,
						options: cloneStringMap(projection.Options), mountpoint: projection.Mountpoint,
						createdAt: projection.CreatedAt, contentID: projection.ContentID}}
				result.objects = append(result.objects, object)
				result.effectEmpty = false
				result.cold = result.cold || generatedDomainDockerObjectCold(object)
				result.ambiguous = result.ambiguous || generatedDomainDockerObjectAmbiguous(object)
			}
		}
		sort.Strings(ids)
		inventories[kind.kind] = ids
	}
	imageIDs, err := r.readImageIDs(ctx, deadline, budget)
	if err != nil {
		return generatedDomainDockerStartupSnapshot{}, nil, err
	}
	inventories["image"] = imageIDs
	for _, id := range imageIDs {
		projection, err := r.inspectDockerProjection(ctx, deadline, budget, []string{"image", "inspect"}, generatedDomainImageInspectFormat, id)
		if err != nil || projection.ID != id {
			return generatedDomainDockerStartupSnapshot{}, nil, errors.New("generated domain Docker listed image changed")
		}
		if r.generatedDomainDockerObjectSelected("image", id, "", projection.Labels) {
			object := generatedDomainDockerObject{kind: "image", id: id, labels: cloneStringMap(projection.Labels),
				data: generatedDomainDockerObjectData{contentID: projection.ContentID}}
			result.objects = append(result.objects, object)
			if generatedDomainDockerObjectCold(object) {
				result.effectEmpty, result.cold = false, true
			}
			result.ambiguous = result.ambiguous || generatedDomainDockerObjectAmbiguous(object)
		}
	}
	sort.Slice(result.objects, func(i, j int) bool {
		if result.objects[i].kind != result.objects[j].kind {
			return result.objects[i].kind < result.objects[j].kind
		}
		if result.objects[i].name != result.objects[j].name {
			return result.objects[i].name < result.objects[j].name
		}
		return result.objects[i].id < result.objects[j].id
	})
	result.recoveryAccounted = generatedDomainRecoveryDockerObjectsAccounted(result.objects, r.recovery)
	return result, inventories, nil
}

func (r *generatedDomainStartupReader) readImageIDs(ctx context.Context, deadline time.Time,
	budget *generatedDomainDockerCensusBudget,
) ([]string, error) {
	body, err := r.runRead(ctx, deadline, "image", "ls", "--all", "--no-trunc", "--quiet")
	if err != nil {
		return nil, err
	}
	defer clear(body)
	lineCount := bytes.Count(body, []byte{'\n'})
	if len(bytes.TrimSpace(body)) != 0 && (len(body) == 0 || body[len(body)-1] != '\n') {
		lineCount++
	}
	if err := budget.charge(body, lineCount); err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(body)), "\r\n", "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, nil
	}
	unique := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if line == "" || strings.TrimSpace(line) != line || !generatedDomainCanonicalDockerID(line, true) {
			return nil, errors.New("generated domain Docker image inventory is invalid")
		}
		unique[line] = struct{}{}
	}
	if len(unique) > generatedDomainStartupMaximumEntries {
		return nil, errors.New("generated domain Docker image inventory exceeds its bound")
	}
	result := make([]string, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func (r *generatedDomainStartupReader) inspectDockerProjection(ctx context.Context, deadline time.Time,
	budget *generatedDomainDockerCensusBudget, prefix []string, format, id string,
) (generatedDomainDockerInspectProjection, error) {
	args := append(append([]string(nil), prefix...), "--format", format, id)
	body, err := r.runRead(ctx, deadline, args...)
	if err != nil {
		return generatedDomainDockerInspectProjection{}, err
	}
	defer clear(body)
	if err := budget.charge(body, 0); err != nil {
		return generatedDomainDockerInspectProjection{}, err
	}
	var value generatedDomainDockerInspectProjection
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if rejectGeneratedDomainDuplicateJSONKeys(body) != nil || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return generatedDomainDockerInspectProjection{}, errors.New("generated domain Docker inspection is invalid")
	}
	if value.Labels == nil {
		value.Labels = map[string]string{}
	}
	if value.Options == nil {
		value.Options = map[string]string{}
	}
	return value, nil
}

func generatedDomainCanonicalDockerID(value string, image bool) bool {
	raw := value
	if image {
		if !strings.HasPrefix(value, "sha256:") {
			return false
		}
		raw = strings.TrimPrefix(value, "sha256:")
	} else if strings.HasPrefix(value, "sha256:") {
		return false
	}
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return false
	}
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == 32
}

func (r *generatedDomainStartupReader) runRead(ctx context.Context, deadline time.Time, args ...string) ([]byte, error) {
	timeout := generatedDomainStartupCommandTimeout(r.options, time.Until(deadline))
	if timeout <= 0 || ctx.Err() != nil {
		return nil, errors.New("generated domain Docker read deadline expired")
	}
	result, err := r.runner.Run(ctx, runtimeprocess.CommandRequest{Executable: r.options.DockerExecutable,
		Args: append([]string(nil), args...), Directory: r.options.WorkingDirectory,
		Env: append([]string(nil), r.environment...), Timeout: timeout, OutputLimit: r.options.OutputLimit})
	if err != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		clearResult(&result)
		return nil, errors.New("generated domain Docker read failed")
	}
	body := append([]byte(nil), result.Stdout...)
	clearResult(&result)
	return body, nil
}

func decodeGeneratedDomainDockerList(body []byte) ([]generatedDomainDockerListEntry, error) {
	if len(body) > generatedDomainStartupMaximumBytes ||
		bytes.Count(body, []byte{'\n'})+1 > generatedDomainStartupMaximumEntries {
		return nil, errors.New("generated domain Docker inventory exceeds its bound")
	}
	lines := bytes.Split(bytes.TrimSpace(body), []byte{'\n'})
	if len(lines) == 1 && len(lines[0]) == 0 {
		return []generatedDomainDockerListEntry{}, nil
	}
	result := make([]generatedDomainDockerListEntry, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		var value generatedDomainDockerListEntry
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if rejectGeneratedDomainDuplicateJSONKeys(line) != nil || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, errors.New("generated domain Docker inventory is malformed")
		}
		result = append(result, value)
	}
	return result, nil
}

func (r *generatedDomainStartupReader) generatedDomainDockerObjectSelected(kind, id, name string,
	labels map[string]string,
) bool {
	name = strings.TrimPrefix(name, "/")
	for _, expectation := range []generatedDomainGatewayExpectation{r.expectation, r.census} {
		if names := expectation.names[kind]; names != nil {
			if _, selected := names[name]; selected {
				return true
			}
		}
		if ids := expectation.ids[kind]; ids != nil && id != "" {
			if _, selected := ids[normalizeID(id)]; selected {
				return true
			}
		}
	}
	if strings.HasPrefix(name, "rig-generated-caddy") || strings.HasPrefix(name, "rig-a-") ||
		strings.HasPrefix(name, "rig-migration-") || strings.HasPrefix(name, "rig-buildkitd-") ||
		strings.HasPrefix(name, "rig-buildnet-") {
		return true
	}
	for key := range labels {
		if strings.HasPrefix(key, "io.rig.") || strings.HasPrefix(key, "rig.") {
			return true
		}
	}
	return false
}

func generatedDomainRecoveryDockerObjectsAccounted(objects []generatedDomainDockerObject,
	expected generatedDomainRecoveryDockerExpectation,
) bool {
	if expected.kind == generatedDomainRecoveryDockerNone || len(expected.alternatives) == 0 {
		return false
	}
	for _, alternative := range expected.alternatives {
		if generatedDomainRecoveryDockerAlternativeMatches(objects, alternative, expected.imageID) {
			return true
		}
	}
	return false
}

func generatedDomainRecoveryDockerAlternativeMatches(objects []generatedDomainDockerObject,
	expected []generatedDomainRecoveryDockerDescriptor, imageID string,
) bool {
	remaining := make(map[string]generatedDomainRecoveryDockerDescriptor, len(expected))
	for _, descriptor := range expected {
		key := descriptor.kind + "\x00" + descriptor.name
		if descriptor.kind == "" || descriptor.name == "" || descriptor.kind == "image" {
			return false
		}
		if _, duplicate := remaining[key]; duplicate {
			return false
		}
		remaining[key] = descriptor
	}
	imageSeen := false
	for _, object := range objects {
		if object.kind == "image" {
			if imageSeen || imageID == "" || normalizeID(object.id) != normalizeID(imageID) ||
				object.data.contentID != object.id || !generatedDomainExactRigLabels(object.labels, nil) {
				return false
			}
			imageSeen = true
			continue
		}
		name := strings.TrimPrefix(object.name, "/")
		key := object.kind + "\x00" + name
		descriptor, exists := remaining[key]
		if !exists || !generatedDomainRecoveryDockerObjectMatches(object, descriptor) {
			return false
		}
		delete(remaining, key)
	}
	return len(remaining) == 0
}

func generatedDomainRecoveryDockerObjectMatches(object generatedDomainDockerObject,
	expected generatedDomainRecoveryDockerDescriptor,
) bool {
	if object.kind != expected.kind || strings.TrimPrefix(object.name, "/") != expected.name ||
		!(object.kind == "volume" && expected.name == caddyVolumeName &&
			validGeneratedDomainNativeV1VolumeLabels(object.labels)) &&
			!generatedDomainExactRigLabels(object.labels, expected.labels) {
		return false
	}
	if object.kind != "volume" {
		if !generatedDomainCanonicalDockerID(object.id, false) ||
			expected.id != "" && normalizeID(object.id) != normalizeID(expected.id) {
			return false
		}
	}
	switch object.kind {
	case "container":
		return true
	case "network":
		return object.data.driver == expected.data.driver && object.data.scope == expected.data.scope &&
			reflect.DeepEqual(object.data.options, expected.data.options)
	case "volume":
		return object.data.driver == expected.data.driver && object.data.scope == expected.data.scope &&
			reflect.DeepEqual(object.data.options, expected.data.options) &&
			(expected.data.mountpoint == "" || object.data.mountpoint == expected.data.mountpoint) &&
			(expected.data.createdAt == "" || object.data.createdAt == expected.data.createdAt)
	default:
		return false
	}
}

func generatedDomainExactRigLabels(actual, expected map[string]string) bool {
	for key, value := range actual {
		if !strings.HasPrefix(key, "io.rig.") && !strings.HasPrefix(key, "rig.") {
			continue
		}
		if expected == nil || expected[key] != value {
			return false
		}
	}
	for key, value := range expected {
		if (!strings.HasPrefix(key, "io.rig.") && !strings.HasPrefix(key, "rig.")) || actual[key] != value {
			return false
		}
	}
	return true
}

func rejectGeneratedDomainDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, ok := keyToken.(string)
				folded := strings.ToLower(key)
				if keyErr != nil || !ok {
					return errors.New("generated domain Docker JSON object is invalid")
				}
				if _, duplicate := seen[folded]; duplicate {
					return errors.New("generated domain Docker JSON key is duplicated")
				}
				seen[folded] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("generated domain Docker JSON delimiter is invalid")
		}
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("generated domain Docker JSON has trailing data")
	}
	return nil
}

func generatedDomainDockerObjectCold(value generatedDomainDockerObject) bool {
	if value.kind == "image" {
		for key := range value.labels {
			if strings.HasPrefix(key, "io.rig.") || strings.HasPrefix(key, "rig.") {
				return true
			}
		}
		return false
	}
	return strings.HasPrefix(value.name, "rig-a-") || strings.HasPrefix(value.name, "rig-migration-") ||
		strings.HasPrefix(value.name, "rig-buildkitd-") || strings.HasPrefix(value.name, "rig-buildnet-") ||
		false
}

func generatedDomainDockerObjectAmbiguous(value generatedDomainDockerObject) bool {
	if value.kind == "image" {
		return value.data.contentID == "" || value.data.contentID != value.id
	}
	return value.name == "" || (value.kind != "volume" && value.id == "")
}

func sameGeneratedDomainDockerStartup(left, right generatedDomainDockerStartupSnapshot) bool {
	return reflect.DeepEqual(left, right)
}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func (r *generatedDomainStartupReader) attestGateway(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot,
) (generatedDomainGatewayAttestation, error) {
	expected := r.expectation
	if expected.kind == generatedDomainGatewayNone {
		return generatedDomainGatewayAttestation{}, nil
	}
	if r.recovery.kind == generatedDomainRecoveryDockerNone {
		if !generatedDomainExpectedDockerObjects(snapshot.objects, expected) {
			return generatedDomainGatewayAttestation{}, errors.New("generated domain gateway namespace is not exact")
		}
		applicationRoutes, valid := generatedDomainGatewayApplicationRoutes(expected)
		if !valid {
			return generatedDomainGatewayAttestation{}, errors.New("generated domain gateway expectation is invalid")
		}
		if applicationRoutes {
			// The final classifier derives cold accounting independently from
			// the sealed SQL and Docker snapshots. Once the complete passive
			// census proves the exact namespace, do not dispatch admin or route
			// traffic into an app-bearing gateway that cannot be Stable.
			return generatedDomainGatewayAttestation{}, nil
		}
	}
	switch expected.kind {
	case generatedDomainGatewayNativeV1:
		return r.attestNativeV1(ctx, deadline, snapshot, expected, false)
	case generatedDomainGatewayNativeV2:
		return r.attestNativeV2(ctx, deadline, snapshot, expected, false)
	case generatedDomainGatewayCurrentV1, generatedDomainGatewayCurrentV2:
		return r.attestCurrent(ctx, deadline, snapshot, expected, false)
	default:
		return generatedDomainGatewayAttestation{}, errors.New("generated domain gateway expectation is invalid")
	}
}

func generatedDomainGatewayApplicationRoutes(expected generatedDomainGatewayExpectation) (bool, bool) {
	switch expected.kind {
	case generatedDomainGatewayNativeV1:
		return expected.nativeV1 != nil && len(expected.nativeV1.Active) != 0, expected.nativeV1 != nil
	case generatedDomainGatewayNativeV2:
		return expected.nativeV2 != nil && len(expected.nativeV2.Apps) != 0, expected.nativeV2 != nil
	case generatedDomainGatewayCurrentV1:
		return expected.currentV1 != nil && len(expected.currentV1.Apps) != 0, expected.currentV1 != nil
	case generatedDomainGatewayCurrentV2:
		return expected.currentV2 != nil && len(expected.currentV2.Apps) != 0, expected.currentV2 != nil
	default:
		return false, false
	}
}

func generatedDomainExpectedDockerObjects(objects []generatedDomainDockerObject,
	expected generatedDomainGatewayExpectation,
) bool {
	seen := make(map[string]struct{})
	for _, object := range objects {
		name := strings.TrimPrefix(object.name, "/")
		_, nameExpected := expected.names[object.kind][name]
		_, idExpected := expected.ids[object.kind][normalizeID(object.id)]
		if !nameExpected && !idExpected {
			return false
		}
		key := object.kind + "\x00" + name
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	for kind, names := range expected.names {
		if kind == "image" {
			continue
		}
		for name := range names {
			if _, ok := seen[kind+"\x00"+name]; !ok {
				return false
			}
		}
	}
	return true
}

func generatedDomainObjectByName(snapshot generatedDomainDockerStartupSnapshot, kind, name string) (generatedDomainDockerObject, bool) {
	var result generatedDomainDockerObject
	found := false
	for _, object := range snapshot.objects {
		if object.kind != kind || strings.TrimPrefix(object.name, "/") != name {
			continue
		}
		if found {
			return generatedDomainDockerObject{}, false
		}
		result, found = object, true
	}
	return result, found
}

func (r *generatedDomainStartupReader) readDetailedContainer(ctx context.Context, deadline time.Time,
	id string,
) (caddyInspection, gatewayContainerRuntime, error) {
	body, err := r.runRead(ctx, deadline, "container", "inspect", "--format",
		generatedDomainStartupContainerInspectFormat, id)
	if err != nil {
		return caddyInspection{}, gatewayContainerRuntime{}, err
	}
	defer clear(body)
	var value gatewayContainerInspection
	if decodeGeneratedDomainStrictJSON(body, &value) != nil ||
		!generatedDomainCanonicalDockerID(value.ID, false) || value.ID != id {
		return caddyInspection{}, gatewayContainerRuntime{}, errors.New("generated domain gateway container inspection is invalid")
	}
	return value.caddyInspection, value.gatewayContainerRuntime, nil
}

func (r *generatedDomainStartupReader) readDetailedVolume(ctx context.Context, deadline time.Time,
	name string,
) (volumeInspection, gatewayV1VolumeIdentity, error) {
	body, err := r.runRead(ctx, deadline, "volume", "inspect", "--format", generatedDomainVolumeInspectFormat, name)
	if err != nil {
		return volumeInspection{}, gatewayV1VolumeIdentity{}, err
	}
	defer clear(body)
	var value gatewayVolumeIdentityInspection
	if decodeGeneratedDomainStrictJSON(body, &value) != nil || value.Name != name {
		return volumeInspection{}, gatewayV1VolumeIdentity{}, errors.New("generated domain gateway volume inspection is invalid")
	}
	return volumeInspection{Name: value.Name, Driver: value.Driver, Scope: value.Scope,
		Options: value.Options, Labels: value.Labels}, gatewayV1VolumeIdentity{Mountpoint: value.Mountpoint,
		CreatedAt: value.CreatedAt}, nil
}

func (r *generatedDomainStartupReader) readDetailedNetwork(ctx context.Context, deadline time.Time,
	id string,
) (caddyNetworkInspection, string, error) {
	body, err := r.runRead(ctx, deadline, "network", "inspect", "--format",
		generatedDomainStartupNetworkInspectFormat, id)
	if err != nil {
		return caddyNetworkInspection{}, "", err
	}
	defer clear(body)
	var value gatewayNetworkIdentityInspection
	if decodeGeneratedDomainStrictJSON(body, &value) != nil ||
		!generatedDomainCanonicalDockerID(value.ID, false) || value.ID != id {
		return caddyNetworkInspection{}, "", errors.New("generated domain gateway network inspection is invalid")
	}
	return value.caddy(), normalizeID(value.ID), nil
}

func (r *generatedDomainStartupReader) attestRecovery(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot,
) (bool, error) {
	if !generatedDomainRecoveryDockerObjectsAccounted(snapshot.objects, r.recovery) {
		return false, nil
	}
	switch r.recovery.kind {
	case generatedDomainRecoveryDockerCurrent:
		return r.recovery.gateway.kind != generatedDomainGatewayNone, nil
	case generatedDomainRecoveryDockerUpgrade:
		return r.attestUpgradeRecovery(ctx, deadline, snapshot)
	case generatedDomainRecoveryDockerRebind:
		return r.attestRebindRecovery(ctx, deadline, snapshot)
	default:
		return false, nil
	}
}

func (r *generatedDomainStartupReader) attestUpgradeRecovery(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot,
) (bool, error) {
	if r.recovery.source == nil || r.recovery.upgrade == nil || len(r.recovery.source.Active) != 0 ||
		len(r.recovery.upgrade.State.Apps) != 0 {
		return false, nil
	}
	source, state, journal := *r.recovery.source, r.recovery.upgrade.State, r.recovery.upgrade.Journal
	observation := gatewayV2DockerObservation{
		V1ApplicationNetworks: map[string]caddyNetworkInspection{}, V1ApplicationNetworkIDs: map[string]string{},
		ApplicationNetworks: map[string]caddyNetworkInspection{}, ApplicationNetworkIDs: map[string]string{},
		V1Stable: true, V1ResourcesStable: true, V1EndpointIdentityProven: true,
		V2ResourcesStable: true, StageStable: true, FinalStable: true, OwnedInventoriesStable: true,
	}
	image, err := r.readPinnedImage(ctx, deadline, journal.Resources.ImageID)
	if err != nil {
		return false, err
	}
	observation.Image, observation.ImageFound = image, true
	v1ContainerObject, v1ContainerFound := generatedDomainObjectByName(snapshot, "container", caddyContainerName)
	v1VolumeObject, v1VolumeFound := generatedDomainObjectByName(snapshot, "volume", caddyVolumeName)
	v1NetworkObject, v1NetworkFound := generatedDomainObjectByName(snapshot, "network", caddyNetworkName)
	if !v1ContainerFound || !v1VolumeFound || !v1NetworkFound {
		return false, nil
	}
	observation.V1Container, observation.V1Runtime, err = r.readDetailedContainer(ctx, deadline, v1ContainerObject.id)
	if err != nil {
		return false, err
	}
	observation.V1ContainerFound = true
	observation.V1Volume, observation.V1VolumeIdentity, err = r.readDetailedVolume(ctx, deadline, v1VolumeObject.name)
	if err != nil {
		return false, err
	}
	observation.V1VolumeFound = true
	observation.V1Network, observation.V1NetworkID, err = r.readDetailedNetwork(ctx, deadline, v1NetworkObject.id)
	if err != nil {
		return false, err
	}
	observation.V1NetworkFound = true
	observation.V1RestartConfig, err = r.readRestartConfig(ctx, deadline, observation.V1Container.ID, gatewayV2ActiveConfigFile)
	if err != nil {
		return false, err
	}
	if observation.V1Container.Running && !observation.V1Container.Restarting {
		if !generatedDomainUpgradeV1StaticValid(source, journal, observation, r.options.HostPort) {
			return false, nil
		}
		observation.V1Config, err = r.readLiveConfig(ctx, deadline, observation.V1Container.ID)
		if err != nil {
			return false, err
		}
	}

	if object, found := generatedDomainObjectByName(snapshot, "volume", state.Identity.ConfigVolume); found {
		observation.ConfigVolume, observation.ConfigVolumeIdentity, err = r.readDetailedVolume(ctx, deadline, object.name)
		if err != nil {
			return false, err
		}
		observation.ConfigVolumeFound = true
		observation.OwnedVolumes = append(observation.OwnedVolumes, object.name)
	}
	if object, found := generatedDomainObjectByName(snapshot, "volume", state.Identity.DataVolume); found {
		observation.DataVolume, observation.DataVolumeIdentity, err = r.readDetailedVolume(ctx, deadline, object.name)
		if err != nil {
			return false, err
		}
		observation.DataVolumeFound = true
		observation.OwnedVolumes = append(observation.OwnedVolumes, object.name)
	}
	if object, found := generatedDomainObjectByName(snapshot, "network", state.Identity.IngressNetwork); found {
		observation.IngressNetwork, observation.IngressNetworkID, err = r.readDetailedNetwork(ctx, deadline, object.id)
		if err != nil {
			return false, err
		}
		observation.IngressFound = true
		observation.OwnedNetworks = append(observation.OwnedNetworks, object.name)
	}
	if object, found := generatedDomainObjectByName(snapshot, "container", state.Identity.StageContainer); found {
		observation.StageContainer, observation.StageRuntime, err = r.readDetailedContainer(ctx, deadline, object.id)
		if err != nil {
			return false, err
		}
		observation.StageContainerFound = true
		observation.OwnedContainers = append(observation.OwnedContainers, object.name)
		observation.StageRestartConfig, err = r.readRestartConfig(ctx, deadline, observation.StageContainer.ID,
			state.Identity.StageConfigFilename)
		if err != nil {
			return false, err
		}
	}
	if object, found := generatedDomainObjectByName(snapshot, "container", state.Identity.FinalContainer); found {
		observation.FinalContainer, observation.FinalRuntime, err = r.readDetailedContainer(ctx, deadline, object.id)
		if err != nil {
			return false, err
		}
		observation.FinalContainerFound = true
		observation.OwnedContainers = append(observation.OwnedContainers, object.name)
		observation.FinalRestartConfig, err = r.readRestartConfig(ctx, deadline, observation.FinalContainer.ID,
			state.Identity.ActiveConfigFilename)
		if err != nil {
			return false, err
		}
	}
	sort.Strings(observation.OwnedContainers)
	sort.Strings(observation.OwnedVolumes)
	sort.Strings(observation.OwnedNetworks)
	if !validGatewayPinnedImage(observation.Image, observation.ImageFound) ||
		!gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV1Base(source, journal, observation, true) {
		return false, nil
	}
	if observation.StageContainer.Running && !observation.StageContainer.Restarting {
		expected, valid := generatedDomainUpgradeRunningConfig(state, journal, observation,
			gatewayV2StageContainerRole)
		if !valid {
			return false, nil
		}
		observation.StageConfig, err = r.readLiveConfig(ctx, deadline, observation.StageContainer.ID)
		if err != nil {
			return false, err
		}
		if !sameCaddyConfig(expected, observation.StageConfig) {
			return false, nil
		}
		observation.Stage404Proven, observation.StageHostPublicationProven =
			r.proveUpgradeGatewayOnly(ctx, deadline, state, observation.StageContainer)
	}
	if observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
		expected, valid := generatedDomainUpgradeRunningConfig(state, journal, observation,
			gatewayV2FinalContainerRole)
		if !valid {
			return false, nil
		}
		observation.FinalConfig, err = r.readLiveConfig(ctx, deadline, observation.FinalContainer.ID)
		if err != nil {
			return false, err
		}
		if !sameCaddyConfig(expected, observation.FinalConfig) {
			return false, nil
		}
		observation.Final404Proven, observation.FinalHostPublicationProven =
			r.proveUpgradeGatewayOnly(ctx, deadline, state, observation.FinalContainer)
		observation.FinalRoutesProven = observation.Final404Proven && observation.FinalHostPublicationProven
		observation.FinalEndpointIdentityProven = true
	}
	switch journal.Phase {
	case gatewayPhasePrepared:
		return classifyGatewayV2Topology(source, state, journal, observation) == gatewayTopologyExactV1Only, nil
	case gatewayPhaseStageIntent:
		return classifyGatewayV2Topology(source, state, journal, observation) == gatewayTopologyExactV1WithStage ||
			classifyGatewayV2RecoveryTopology(source, state, journal, observation) != gatewayV2RecoveryUnknown, nil
	case gatewayPhaseStaged:
		return classifyGatewayV2Topology(source, state, journal, observation) == gatewayTopologyExactV1WithStage, nil
	case gatewayPhaseTransferIntent, gatewayPhaseRollbackIntent:
		return classifyGatewayV2Topology(source, state, journal, observation) == gatewayTopologyExactFinalV2 ||
			classifyGatewayV2RecoveryTopology(source, state, journal, observation) != gatewayV2RecoveryUnknown, nil
	case gatewayPhaseV2Serving:
		return classifyGatewayV2Topology(source, state, journal, observation) == gatewayTopologyExactFinalV2, nil
	default:
		return false, nil
	}
}

func generatedDomainUpgradeRunningConfig(state gatewayV2RouteState, journal gatewayMigrationJournal,
	observation gatewayV2DockerObservation, role string,
) ([]byte, bool) {
	if !validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) ||
		!validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) ||
		!validGatewayV2Volumes(state, journal, observation) ||
		len(observation.ApplicationNetworks) != 0 || len(observation.ApplicationNetworkIDs) != 0 {
		return nil, false
	}
	switch role {
	case gatewayV2StageContainerRole:
		expected, err := buildGatewayV2StageConfig(state)
		return expected, err == nil && !observation.FinalContainerFound &&
			validOwnedNameSet(observation.OwnedContainers, state.Identity.StageContainer) &&
			validGatewayV2Container(state, journal, observation.StageContainer, observation.StageRuntime,
				observation.StageContainerFound, role, observation.Image.ID) &&
			validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound,
				observation.StageContainer.ID, state.Identity.StageContainer) &&
			sameCaddyConfig(expected, observation.StageRestartConfig)
	case gatewayV2FinalContainerRole:
		expected, err := expectedGatewayV2FinalConfig(state)
		return expected, err == nil && !observation.StageContainerFound &&
			validOwnedNameSet(observation.OwnedContainers, state.Identity.FinalContainer) &&
			validGatewayV2Container(state, journal, observation.FinalContainer, observation.FinalRuntime,
				observation.FinalContainerFound, role, observation.Image.ID) &&
			validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound,
				observation.FinalContainer.ID, state.Identity.FinalContainer) &&
			sameCaddyConfig(expected, observation.FinalRestartConfig)
	default:
		return nil, false
	}
}

func generatedDomainUpgradeV1StaticValid(source routeState, journal gatewayMigrationJournal,
	observation gatewayV2DockerObservation, hostPort uint16,
) bool {
	if !validCaddyInspection(observation.V1Container, observation.Image.ID, hostPort) ||
		observation.V1Volume.Name != caddyVolumeName || observation.V1Volume.Driver != "local" ||
		observation.V1Volume.Scope != "local" || len(observation.V1Volume.Options) != 0 ||
		!validGeneratedDomainNativeV1VolumeLabels(observation.V1Volume.Labels) {
		return false
	}
	listenIP, valid := caddyIngressAddress(observation.V1Network, observation.V1Container.ID)
	expected, err := buildCaddyConfig(source.Active,
		net.JoinHostPort(listenIP, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)))
	return valid && err == nil && sameCaddyConfig(expected, observation.V1RestartConfig) &&
		journal.Source.LocalHostPort == hostPort
}

func (r *generatedDomainStartupReader) proveUpgradeGatewayOnly(ctx context.Context, deadline time.Time,
	state gatewayV2RouteState, container caddyInspection,
) (bool, bool) {
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil || !container.Running || container.Restarting || len(state.Apps) != 0 {
		return false, false
	}
	token := gatewayV2PortChallenge(challenge, state.Profile.PortStart)
	wrongHost := r.proveGatewayStaticResponse(ctx, deadline, container.ID, state.Network.ContainerIPv4,
		state.Profile.PortStart, "wrong.invalid", token, "") == nil
	host := r.proveGatewayStaticResponse(ctx, deadline, container.ID, state.Network.ContainerIPv4,
		state.Profile.PortStart, state.Profile.SelectedIPv4, token, gatewayV2ChallengeBodyPrefix+token) == nil
	return wrongHost, host
}

func (r *generatedDomainStartupReader) attestRebindRecovery(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot,
) (bool, error) {
	if r.recovery.rebindIntent == nil {
		return r.recovery.rebindEffect == nil, nil
	}
	if r.recovery.rebindEffect == nil {
		return true, nil
	}
	intent, effect := *r.recovery.rebindIntent, *r.recovery.rebindEffect
	observation := gatewayRebindTypedStageObservation{}
	image, err := r.readPinnedImage(ctx, deadline, effect.ImageID)
	if err != nil {
		return false, err
	}
	image.ID = normalizeID(image.ID)
	observation.Image, observation.ImageFound = image, true
	if object, found := generatedDomainObjectByName(snapshot, "network", intent.Identity.IngressNetwork); found {
		observation.Network, observation.NetworkID, err = r.readDetailedNetwork(ctx, deadline, object.id)
		if err != nil {
			return false, err
		}
		observation.NetworkFound = true
		observation.OwnedNetworks = append(observation.OwnedNetworks, object.name)
	}
	if object, found := generatedDomainObjectByName(snapshot, "volume", intent.Identity.ConfigVolume); found {
		observation.ConfigVolume, observation.ConfigVolumeIdentity, err = r.readDetailedVolume(ctx, deadline, object.name)
		if err != nil {
			return false, err
		}
		observation.ConfigVolumeFound = true
		observation.OwnedVolumes = append(observation.OwnedVolumes, object.name)
	}
	if object, found := generatedDomainObjectByName(snapshot, "volume", intent.Identity.DataVolume); found {
		observation.DataVolume, observation.DataVolumeIdentity, err = r.readDetailedVolume(ctx, deadline, object.name)
		if err != nil {
			return false, err
		}
		observation.DataVolumeFound = true
		observation.OwnedVolumes = append(observation.OwnedVolumes, object.name)
	}
	if object, found := generatedDomainObjectByName(snapshot, "container", intent.Identity.StageContainer); found {
		observation.StageContainer, observation.StageRuntime, err = r.readDetailedContainer(ctx, deadline, object.id)
		if err != nil {
			return false, err
		}
		observation.StageContainerFound = true
		observation.OwnedContainers = append(observation.OwnedContainers, object.name)
	}
	var final caddyInspection
	var finalRuntime gatewayContainerRuntime
	finalFound := false
	if object, found := generatedDomainObjectByName(snapshot, "container", intent.Identity.FinalContainer); found {
		final, finalRuntime, err = r.readDetailedContainer(ctx, deadline, object.id)
		if err != nil {
			return false, err
		}
		finalFound = true
		observation.FinalContainerFound = true
		observation.OwnedContainers = append(observation.OwnedContainers, object.name)
	}
	sort.Strings(observation.OwnedContainers)
	sort.Strings(observation.OwnedVolumes)
	sort.Strings(observation.OwnedNetworks)
	if !gatewayRebindTypedStageImageMatches(intent, observation) || observation.Image.ID != effect.ImageID ||
		!gatewayRebindTypedStageNetworkMatches(intent, observation, effect.Network) {
		return false, nil
	}
	var configBinding, dataBinding *gatewayRebindStageConfigVolumeBinding
	if effect.ConfigVolume != nil {
		copy := gatewayRebindStageConfigVolumeBinding(*effect.ConfigVolume)
		configBinding = &copy
	}
	if effect.DataVolume != nil {
		copy := gatewayRebindStageConfigVolumeBinding{Name: effect.DataVolume.Name,
			Mountpoint: effect.DataVolume.Mountpoint, CreatedAt: effect.DataVolume.CreatedAt,
			OwnershipDigest: effect.DataVolume.OwnershipDigest}
		dataBinding = &copy
	}
	if !gatewayRebindTypedStageVolumeMatches(intent, observation.ConfigVolume, observation.ConfigVolumeIdentity,
		observation.ConfigVolumeFound, intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole, configBinding) ||
		!gatewayRebindTypedStageVolumeMatches(intent, observation.DataVolume, observation.DataVolumeIdentity,
			observation.DataVolumeFound, intent.Identity.DataVolume, gatewayV2DataVolumeRole, dataBinding) {
		return false, nil
	}
	if effect.StageContainer == nil {
		if observation.StageContainerFound {
			return false, nil
		}
	} else if !gatewayRebindTypedStoppedStageContainerMatches(intent, effect, observation) &&
		!gatewayRebindTypedRunningStageMatches(intent, effect, observation) {
		return false, nil
	}
	if effect.FinalContainer == nil {
		if finalFound {
			return false, nil
		}
	} else if !finalFound || !gatewayRebindTypedFinalContainerMatches(intent, effect, final, finalRuntime,
		normalizeID(final.ID)) {
		return false, nil
	}
	return true, nil
}

func (r *generatedDomainStartupReader) readPinnedImage(ctx context.Context, deadline time.Time,
	id string,
) (imageInspection, error) {
	if id == "" {
		id = caddyImage
	}
	body, err := r.runRead(ctx, deadline, "image", "inspect", "--format", imageInspectFormat, id)
	if err != nil {
		return imageInspection{}, err
	}
	defer clear(body)
	var value imageInspection
	if decodeGeneratedDomainStrictJSON(body, &value) != nil || !generatedDomainCanonicalDockerID(value.ID, true) ||
		!validGatewayPinnedImage(value, true) ||
		(id != caddyImage && normalizeID(value.ID) != normalizeID(id)) {
		return imageInspection{}, errors.New("generated domain gateway image inspection is invalid")
	}
	return value, nil
}

func decodeGeneratedDomainStrictJSON(body []byte, destination any) error {
	if len(body) == 0 || len(body) > generatedDomainStartupMaximumBytes || rejectGeneratedDomainDuplicateJSONKeys(body) != nil {
		return errors.New("generated domain Docker JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("generated domain Docker JSON is invalid")
	}
	return nil
}

func (r *generatedDomainStartupReader) readRestartConfig(ctx context.Context, deadline time.Time,
	containerID, filename string,
) ([]byte, error) {
	body, err := r.runRead(ctx, deadline, "container", "cp", containerID+":/config/"+filename, "-")
	if err != nil {
		return nil, err
	}
	defer clear(body)
	reader := tar.NewReader(bytes.NewReader(body))
	header, err := reader.Next()
	if err != nil || header == nil || header.Typeflag != tar.TypeReg || header.Size <= 0 ||
		header.Size > gatewayV2MaxConfigBytes || strings.TrimPrefix(header.Name, "./") != filename {
		return nil, errors.New("generated domain gateway restart config archive is invalid")
	}
	config, err := io.ReadAll(io.LimitReader(reader, gatewayV2MaxConfigBytes+1))
	if err != nil || int64(len(config)) != header.Size || !json.Valid(config) {
		clear(config)
		return nil, errors.New("generated domain gateway restart config is invalid")
	}
	if _, err := reader.Next(); err != io.EOF {
		clear(config)
		return nil, errors.New("generated domain gateway restart config archive has extra entries")
	}
	return config, nil
}

func (r *generatedDomainStartupReader) readLiveConfig(ctx context.Context, deadline time.Time,
	containerID string,
) ([]byte, error) {
	body, err := r.runRead(ctx, deadline, "container", "exec", containerID,
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/")
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes || !json.Valid(body) {
		clear(body)
		return nil, errors.New("generated domain gateway live config is invalid")
	}
	return body, nil
}

func (r *generatedDomainStartupReader) proveGatewayStaticResponse(ctx context.Context, deadline time.Time,
	containerID, address string, port uint16, host, token, expectedBody string,
) error {
	if !generatedDomainCanonicalDockerID(containerID, false) || net.ParseIP(address) == nil || port == 0 ||
		!validName(host, 253) || !validSHA256(token) {
		return errors.New("generated domain gateway challenge input is invalid")
	}
	body, err := r.runRead(ctx, deadline, "container", "exec", containerID,
		"curl", "--disable", "--silent", "--show-error", "--output", "-", "--write-out", "\n%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
		"--header", "Host: "+host,
		"http://"+net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10))+gatewayV2ChallengePathPrefix+token)
	defer clear(body)
	if err != nil || string(body) != expectedBody+"\n404" {
		return errors.New("generated domain gateway static response is invalid")
	}
	return nil
}

func (r *generatedDomainStartupReader) attestNativeV1(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
	allowApplicationRoutes bool,
) (generatedDomainGatewayAttestation, error) {
	if expected.nativeV1 == nil || !allowApplicationRoutes && len(expected.nativeV1.Active) != 0 {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 gateway is unsupported")
	}
	containerObject, containerFound := generatedDomainObjectByName(snapshot, "container", caddyContainerName)
	volumeObject, volumeFound := generatedDomainObjectByName(snapshot, "volume", caddyVolumeName)
	networkObject, networkFound := generatedDomainObjectByName(snapshot, "network", caddyNetworkName)
	if !containerFound || !volumeFound || !networkFound {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 gateway is incomplete")
	}
	image, err := r.readPinnedImage(ctx, deadline, "")
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	container, runtime, err := r.readDetailedContainer(ctx, deadline, containerObject.id)
	if err != nil || !container.Running || container.Restarting || !validGatewayContainerRuntime(runtime, true) ||
		!gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, container.PortBindings) ||
		!validCaddyInspection(container, image.ID, r.options.HostPort) {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 container is invalid")
	}
	volume, _, err := r.readDetailedVolume(ctx, deadline, volumeObject.name)
	if err != nil || volume.Name != caddyVolumeName || volume.Driver != "local" || volume.Scope != "local" ||
		len(volume.Options) != 0 || !validGeneratedDomainNativeV1VolumeLabels(volume.Labels) {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 volume is invalid")
	}
	network, networkID, err := r.readDetailedNetwork(ctx, deadline, networkObject.id)
	listenIP, valid := caddyIngressAddress(network, container.ID)
	if err != nil || networkID != normalizeID(networkObject.id) || !valid {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 network is invalid")
	}
	wanted, err := buildCaddyConfig(expected.nativeV1.Active,
		net.JoinHostPort(listenIP, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)))
	if err != nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 config is invalid")
	}
	defer clear(wanted)
	restart, err := r.readRestartConfig(ctx, deadline, container.ID, gatewayV2ActiveConfigFile)
	if err != nil || !sameCaddyConfig(wanted, restart) {
		clear(restart)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 restart config is invalid")
	}
	defer clear(restart)
	live, err := r.readLiveConfig(ctx, deadline, container.ID)
	if err != nil || !sameCaddyConfig(wanted, live) {
		clear(live)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 live config is invalid")
	}
	defer clear(live)
	token, err := canonicalDigest(struct {
		Version int    `json:"version"`
		State   string `json:"state"`
	}{1, expected.stateDigest})
	if err != nil || r.proveGatewayStaticResponse(ctx, deadline, container.ID, listenIP,
		gatewayV2ContainerPort, "wrong.invalid", token, "") != nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V1 static response is invalid")
	}
	return generatedDomainGatewayAttestation{kind: generatedDomainGatewayNativeV1,
		stateDigest: expected.stateDigest, exact: true}, nil
}

func (r *generatedDomainStartupReader) attestNativeV2(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
	allowApplicationRoutes bool,
) (generatedDomainGatewayAttestation, error) {
	if expected.nativeV2 == nil || expected.journal == nil || !allowApplicationRoutes && len(expected.nativeV2.Apps) != 0 ||
		expected.journal.Phase != gatewayPhaseCommitted {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V2 gateway is unsupported")
	}
	state, journal := *expected.nativeV2, *expected.journal
	containerObject, containerFound := generatedDomainObjectByName(snapshot, "container", state.Identity.FinalContainer)
	configObject, configFound := generatedDomainObjectByName(snapshot, "volume", state.Identity.ConfigVolume)
	dataObject, dataFound := generatedDomainObjectByName(snapshot, "volume", state.Identity.DataVolume)
	networkObject, networkFound := generatedDomainObjectByName(snapshot, "network", state.Identity.IngressNetwork)
	if !containerFound || !configFound || !dataFound || !networkFound {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V2 gateway is incomplete")
	}
	image, err := r.readPinnedImage(ctx, deadline, journal.Resources.ImageID)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	container, runtime, err := r.readDetailedContainer(ctx, deadline, containerObject.id)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	config, configID, err := r.readDetailedVolume(ctx, deadline, configObject.name)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	data, dataID, err := r.readDetailedVolume(ctx, deadline, dataObject.name)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	network, networkID, err := r.readDetailedNetwork(ctx, deadline, networkObject.id)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	observation := gatewayV2DockerObservation{
		Image: image, ImageFound: true,
		ConfigVolume: config, ConfigVolumeIdentity: configID, ConfigVolumeFound: true,
		DataVolume: data, DataVolumeIdentity: dataID, DataVolumeFound: true,
		IngressNetwork: network, IngressNetworkID: networkID, IngressFound: true,
		FinalContainer: container, FinalRuntime: runtime, FinalContainerFound: true,
		OwnedVolumes: []string{state.Identity.ConfigVolume, state.Identity.DataVolume},
	}
	if !gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV2Volumes(state, journal, observation) ||
		!validGatewayV2Container(state, journal, container, runtime, true, gatewayV2FinalContainerRole, image.ID) ||
		!validGatewayV2IngressNetwork(state, journal, network, true, container.ID, state.Identity.FinalContainer) {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V2 static identity is invalid")
	}
	wanted, err := expectedGatewayV2FinalConfig(state)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	defer clear(wanted)
	restart, err := r.readRestartConfig(ctx, deadline, container.ID, state.Identity.ActiveConfigFilename)
	if err != nil || !sameCaddyConfig(wanted, restart) {
		clear(restart)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V2 restart config is invalid")
	}
	defer clear(restart)
	live, err := r.readLiveConfig(ctx, deadline, container.ID)
	if err != nil || !sameCaddyConfig(wanted, live) {
		clear(live)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V2 live config is invalid")
	}
	defer clear(live)
	challenge, err := gatewayV2HostChallenge(state)
	token := gatewayV2PortChallenge(challenge, state.Profile.PortStart)
	if err != nil || r.proveGatewayStaticResponse(ctx, deadline, container.ID, state.Network.ContainerIPv4,
		state.Profile.PortStart, state.Profile.SelectedIPv4, token, gatewayV2ChallengeBodyPrefix+token) != nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain native V2 challenge is invalid")
	}
	return generatedDomainGatewayAttestation{kind: generatedDomainGatewayNativeV2,
		operationID: state.OperationID, stateDigest: expected.stateDigest, exact: true}, nil
}

func (r *generatedDomainStartupReader) attestCurrent(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
	allowApplicationRoutes bool,
) (generatedDomainGatewayAttestation, error) {
	if expected.currentV2 != nil && expected.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade {
		return r.attestCurrentV2Upgrade(ctx, deadline, snapshot, expected, allowApplicationRoutes)
	}
	if expected.terminal == nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway terminal is unavailable")
	}
	terminal := *expected.terminal
	var lineage appaccess.GatewayCurrentLineageRef
	var profile gatewayProfileBinding
	var networkPlan gatewayV2NetworkPlan
	var identity gatewayCurrentIdentity
	var wanted []byte
	var token string
	var err error
	if expected.currentV1 != nil {
		state := *expected.currentV1
		if !allowApplicationRoutes && len(state.Apps) != 0 {
			return generatedDomainGatewayAttestation{}, errors.New("generated domain current V1 gateway has application routes")
		}
		lineage, profile, networkPlan, identity = state.Lineage, state.Profile, state.Network, state.Identity
		wanted, err = gatewayCurrentPhysicalConfigBytes(state)
		token, _ = gatewayCurrentPhysicalProbeToken(state)
	} else if expected.currentV2 != nil {
		state := *expected.currentV2
		if !allowApplicationRoutes && len(state.Apps) != 0 {
			return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 gateway has application routes")
		}
		lineage, profile, networkPlan, identity = state.Lineage, state.Profile, state.Network, state.Identity
		wanted, err = gatewayCurrentPhysicalConfigBytesV2(state)
		token, _ = gatewayCurrentPhysicalProbeTokenV2(state)
	} else {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway state is unavailable")
	}
	if err != nil || identity.Rebind == nil || !validSHA256(token) {
		clear(wanted)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway expectation is invalid")
	}
	defer clear(wanted)
	final := identity.Rebind
	containerObject, containerFound := generatedDomainObjectByName(snapshot, "container", final.FinalContainer)
	configObject, configFound := generatedDomainObjectByName(snapshot, "volume", final.ConfigVolume)
	dataObject, dataFound := generatedDomainObjectByName(snapshot, "volume", final.DataVolume)
	networkObject, networkFound := generatedDomainObjectByName(snapshot, "network", final.IngressNetwork)
	if !containerFound || !configFound || !dataFound || !networkFound {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway is incomplete")
	}
	image, err := r.readPinnedImage(ctx, deadline, terminal.Resources.ImageID)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	container, runtime, err := r.readDetailedContainer(ctx, deadline, containerObject.id)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	config, configID, err := r.readDetailedVolume(ctx, deadline, configObject.name)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	data, dataID, err := r.readDetailedVolume(ctx, deadline, dataObject.name)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	ingress, ingressID, err := r.readDetailedNetwork(ctx, deadline, networkObject.id)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	facts := gatewayFinalOwnershipFacts{Lineage: lineage, Terminal: terminal, Network: networkPlan,
		LocalHostPort: r.options.HostPort}
	if !validGatewayPinnedImage(image, true) || !container.Running ||
		!gatewayFinalOwnershipContainerMatches(facts, container, runtime) ||
		!gatewayFinalOwnershipVolumeMatches(facts, config, configID, terminal.Resources.ConfigVolume, gatewayV2ConfigVolumeRole) ||
		!gatewayFinalOwnershipVolumeMatches(facts, data, dataID, terminal.Resources.DataVolume, gatewayV2DataVolumeRole) ||
		!gatewayFinalOwnershipIngressMatches(facts, ingress, ingressID) || len(container.Networks) != 1 ||
		len(runtime.ConfiguredNetworks) != 1 || container.Networks[final.IngressNetwork] == nil ||
		runtime.ConfiguredNetworks[final.IngressNetwork].NetworkID != ingressID ||
		container.Networks[final.IngressNetwork].IPAddress != networkPlan.ContainerIPv4 {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway static identity is invalid")
	}
	restart, err := r.readRestartConfig(ctx, deadline, container.ID, final.ActiveConfigFilename)
	if err != nil || !sameCaddyConfig(wanted, restart) {
		clear(restart)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway restart config is invalid")
	}
	defer clear(restart)
	live, err := r.readLiveConfig(ctx, deadline, container.ID)
	if err != nil || !sameCaddyConfig(wanted, live) {
		clear(live)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway live config is invalid")
	}
	defer clear(live)
	portToken := gatewayV2PortChallenge(token, profile.PortStart)
	if r.proveGatewayStaticResponse(ctx, deadline, container.ID, networkPlan.ContainerIPv4,
		profile.PortStart, profile.SelectedIPv4, portToken, gatewayV2ChallengeBodyPrefix+portToken) != nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current gateway challenge is invalid")
	}
	return generatedDomainGatewayAttestation{kind: expected.kind, operationID: lineage.OperationID,
		protectedGeneration: lineage.ProtectedGeneration, stateDigest: expected.stateDigest, exact: true}, nil
}

func (r *generatedDomainStartupReader) attestCurrentV2Upgrade(ctx context.Context, deadline time.Time,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
	allowApplicationRoutes bool,
) (generatedDomainGatewayAttestation, error) {
	if !generatedDomainCurrentV2UpgradeExpectationValid(expected) || expected.currentV2 == nil ||
		expected.nativeV2 == nil || expected.journal == nil ||
		!allowApplicationRoutes && (len(expected.currentV2.Apps) != 0 || len(expected.nativeV2.Apps) != 0) ||
		expected.journal.Phase != gatewayPhaseCommitted {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade gateway is unsupported")
	}
	current, source, journal := *expected.currentV2, *expected.nativeV2, *expected.journal
	containerObject, containerFound := generatedDomainObjectByName(snapshot, "container", source.Identity.FinalContainer)
	configObject, configFound := generatedDomainObjectByName(snapshot, "volume", source.Identity.ConfigVolume)
	dataObject, dataFound := generatedDomainObjectByName(snapshot, "volume", source.Identity.DataVolume)
	networkObject, networkFound := generatedDomainObjectByName(snapshot, "network", source.Identity.IngressNetwork)
	if !containerFound || !configFound || !dataFound || !networkFound {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade gateway is incomplete")
	}
	image, err := r.readPinnedImage(ctx, deadline, journal.Resources.ImageID)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	container, runtime, err := r.readDetailedContainer(ctx, deadline, containerObject.id)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	config, configID, err := r.readDetailedVolume(ctx, deadline, configObject.name)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	data, dataID, err := r.readDetailedVolume(ctx, deadline, dataObject.name)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	ingress, ingressID, err := r.readDetailedNetwork(ctx, deadline, networkObject.id)
	if err != nil {
		return generatedDomainGatewayAttestation{}, err
	}
	observation := gatewayV2DockerObservation{
		Image: image, ImageFound: true,
		ConfigVolume: config, ConfigVolumeIdentity: configID, ConfigVolumeFound: true,
		DataVolume: data, DataVolumeIdentity: dataID, DataVolumeFound: true,
		IngressNetwork: ingress, IngressNetworkID: ingressID, IngressFound: true,
		FinalContainer: container, FinalRuntime: runtime, FinalContainerFound: true,
		OwnedVolumes: []string{source.Identity.ConfigVolume, source.Identity.DataVolume},
	}
	if !validGatewayPinnedImage(image, true) || !gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV2Volumes(source, journal, observation) ||
		!validGatewayV2Container(source, journal, container, runtime, true, gatewayV2FinalContainerRole, image.ID) ||
		!validGatewayV2IngressNetwork(source, journal, ingress, true, container.ID, source.Identity.FinalContainer) {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade static identity is invalid")
	}
	wanted, err := gatewayCurrentPhysicalConfigBytesV2(current)
	if err != nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade config is invalid")
	}
	defer clear(wanted)
	restart, err := r.readRestartConfig(ctx, deadline, container.ID, source.Identity.ActiveConfigFilename)
	if err != nil || !sameCaddyConfig(wanted, restart) {
		clear(restart)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade restart config is invalid")
	}
	defer clear(restart)
	live, err := r.readLiveConfig(ctx, deadline, container.ID)
	if err != nil || !sameCaddyConfig(wanted, live) {
		clear(live)
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade live config is invalid")
	}
	defer clear(live)
	token, err := gatewayCurrentPhysicalProbeTokenV2(current)
	portToken := gatewayV2PortChallenge(token, current.Profile.PortStart)
	if err != nil || r.proveGatewayStaticResponse(ctx, deadline, container.ID, current.Network.ContainerIPv4,
		current.Profile.PortStart, current.Profile.SelectedIPv4, portToken,
		gatewayV2ChallengeBodyPrefix+portToken) != nil {
		return generatedDomainGatewayAttestation{}, errors.New("generated domain current V2 upgrade challenge is invalid")
	}
	return generatedDomainGatewayAttestation{kind: generatedDomainGatewayCurrentV2,
		operationID: current.Lineage.OperationID, protectedGeneration: current.Lineage.ProtectedGeneration,
		stateDigest: expected.stateDigest, exact: true}, nil
}
