// Package generateddomain provides a fixed Docker transcript for tests that
// must construct the real generated domain. It models only the native-v1
// empty ingress bootstrap and delegates every non-ingress command to an
// optional caller-owned runner.
package generateddomain

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	DaemonID    = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	ImageID     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	NetworkID   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ContainerID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	CaddyImage  = "caddy@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648"
)

type Options struct {
	Fallback             runtimeprocess.CommandRunner
	DaemonID             string
	HostPort             uint16
	MemoryAvailableBytes uint64
	DiskAvailableBytes   uint64
	MergeFallbackCensus  bool
	DelegateCapacity     bool
}

type Runner struct {
	mu sync.Mutex

	options       Options
	calls         int
	capacityCalls int
	reloadCalls   int
	commands      [][]string
	reloadErrors  []error
	volume        bool
	network       bool
	container     bool
	running       bool
	config        []byte
	files         map[string][]byte
	volumeNonce   string
	attachments   map[string]struct{}
}

var runnerSequence atomic.Uint64

func NewRunner(options Options) *Runner {
	if options.DaemonID == "" {
		options.DaemonID = fmt.Sprintf("%064x", runnerSequence.Add(1))
	}
	if options.HostPort == 0 {
		options.HostPort = 8080
	}
	if options.MemoryAvailableBytes == 0 {
		options.MemoryAvailableBytes = 2 << 30
	}
	if options.DiskAvailableBytes == 0 {
		options.DiskAvailableBytes = 8 << 30
	}
	return &Runner{options: options, attachments: make(map[string]struct{}), files: make(map[string][]byte)}
}

func (r *Runner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// SetCapacity changes only the next fixed Caddy capacity observation. It is a
// process transcript control for tests; it does not expose or replace the
// domain's sealed budget.
func (r *Runner) SetCapacity(memoryAvailableBytes, diskAvailableBytes uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.options.MemoryAvailableBytes = memoryAvailableBytes
	r.options.DiskAvailableBytes = diskAvailableBytes
}

func (r *Runner) CapacityCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capacityCalls
}

// SetReloadErrors replaces the deterministic error sequence for subsequent
// Caddy reload commands. It controls only the process transcript so tests can
// exercise the real route owner's physical rollback behavior.
func (r *Runner) SetReloadErrors(values ...error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadCalls = 0
	r.reloadErrors = append(r.reloadErrors[:0], values...)
}

func (r *Runner) ReloadCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloadCalls
}

// Commands returns only copied argv from the controlled process transcript.
// It omits environment variables, working directories, and command output.
func (r *Runner) Commands() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([][]string, len(r.commands))
	for index := range r.commands {
		result[index] = append([]string(nil), r.commands[index]...)
	}
	return result
}

func (r *Runner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	r.calls++
	r.commands = append(r.commands, append([]string(nil), request.Args...))
	result, err, handled := r.runLocked(request.Args)
	fallback := r.options.Fallback
	r.mu.Unlock()
	if handled && isGeneratedDomainCensusCommand(request.Args) && fallback != nil && r.options.MergeFallbackCensus {
		observed, observedErr := fallback.Run(ctx, request)
		if err != nil {
			return result, err
		}
		if observedErr != nil {
			return observed, observedErr
		}
		return mergeGeneratedDomainCensus(result, observed), nil
	}
	if handled {
		return result, err
	}
	if fallback != nil {
		return fallback.Run(ctx, request)
	}
	return runtimeprocess.CommandResult{}, fmt.Errorf("unsupported generated-domain command %#v", request.Args)
}

func (r *Runner) runLocked(args []string) (runtimeprocess.CommandResult, error, bool) {
	result := func(value runtimeprocess.CommandResult, err error) (runtimeprocess.CommandResult, error, bool) {
		return value, err, true
	}
	if reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}) {
		value, err := jsonResult(r.options.DaemonID)
		return result(value, err)
	}
	if reflect.DeepEqual(args, []string{"container", "ls", "--all", "--no-trunc", "--format", `{"id":{{json .ID}}}`}) {
		if !r.container {
			return result(runtimeprocess.CommandResult{}, nil)
		}
		value, err := jsonResult(map[string]string{"id": ContainerID})
		return result(value, err)
	}
	if reflect.DeepEqual(args, []string{"network", "ls", "--no-trunc", "--format", `{"id":{{json .ID}}}`}) {
		if !r.network {
			return result(runtimeprocess.CommandResult{}, nil)
		}
		value, err := jsonResult(map[string]string{"id": NetworkID})
		return result(value, err)
	}
	if reflect.DeepEqual(args, []string{"volume", "ls", "--format", `{"name":{{json .Name}}}`}) {
		if !r.volume {
			return result(runtimeprocess.CommandResult{}, nil)
		}
		value, err := jsonResult(map[string]string{"name": "rig-generated-caddy-config-v1"})
		return result(value, err)
	}
	if reflect.DeepEqual(args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"}) {
		return result(runtimeprocess.CommandResult{Stdout: []byte(ImageID + "\n")}, nil)
	}
	if len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format" {
		if args[4] != CaddyImage && args[4] != ImageID {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if args[3] == `{"id":{{json .ID}},"os":{{json .Os}},"repoDigests":{{json .RepoDigests}}}` {
			value, err := jsonResult(map[string]any{"id": ImageID, "os": "linux", "repoDigests": []string{CaddyImage}})
			return result(value, err)
		}
		value, err := jsonResult(map[string]any{"id": ImageID, "labels": map[string]string{}, "contentId": ImageID})
		return result(value, err)
	}
	if len(args) == 5 && args[0] == "volume" && args[1] == "inspect" && args[2] == "--format" {
		if args[4] != "rig-generated-caddy-config-v1" {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if !r.volume {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain volume is unavailable"))
		}
		value, err := jsonResult(r.volumeInspection())
		return result(value, err)
	}
	if len(args) == 5 && args[0] == "network" && args[1] == "inspect" && args[2] == "--format" {
		if args[4] != "rig-generated-caddy-ingress-v1" && args[4] != NetworkID {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if !r.network {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain network is unavailable"))
		}
		if args[3] == "json" {
			value, err := jsonResult([]map[string]any{r.networkInspection()})
			return result(value, err)
		}
		if generatedDomainStartupNetworkProjection(args[3]) {
			value, err := jsonResult(r.networkInspection())
			return result(value, err)
		}
		if args[3] == `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Labels}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}}}` {
			value, err := jsonResult(map[string]any{"id": NetworkID, "name": "rig-generated-caddy-ingress-v1", "labels": r.networkLabels(), "driver": "bridge", "scope": "local", "options": map[string]string{}})
			return result(value, err)
		}
		value, err := jsonResult(map[string]any{"name": "rig-generated-caddy-ingress-v1", "labels": r.networkLabels(), "driver": "bridge", "scope": "local", "internal": false, "options": map[string]string{}, "ipam": []map[string]string{{"Subnet": "10.203.0.0/28", "Gateway": "10.203.0.1"}}})
		return result(value, err)
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" {
		if args[4] != "rig-generated-caddy-v1" && args[4] != ContainerID {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if !r.container {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain container is unavailable"))
		}
		if args[3] == `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Config.Labels}}}` {
			value, err := jsonResult(map[string]any{"id": ContainerID, "name": "/rig-generated-caddy-v1", "labels": r.containerLabels()})
			return result(value, err)
		}
		value, err := jsonResult(r.containerInspection())
		return result(value, err)
	}
	if len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
		if args[len(args)-1] != "rig-generated-caddy-config-v1" {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if r.volume {
			return result(runtimeprocess.CommandResult{}, errors.New("invalid generated domain volume create"))
		}
		for _, value := range args {
			if strings.HasPrefix(value, "io.rig.bootstrap-volume-nonce=") {
				r.volumeNonce = strings.TrimPrefix(value, "io.rig.bootstrap-volume-nonce=")
			}
		}
		if len(r.volumeNonce) != 64 {
			return result(runtimeprocess.CommandResult{}, errors.New("missing generated domain volume nonce"))
		}
		r.volume = true
		return result(runtimeprocess.CommandResult{Stdout: []byte("rig-generated-caddy-config-v1\n")}, nil)
	}
	if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
		if args[len(args)-1] != "rig-generated-caddy-ingress-v1" {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if r.network {
			return result(runtimeprocess.CommandResult{}, errors.New("invalid generated domain network create"))
		}
		r.network = true
		return result(runtimeprocess.CommandResult{Stdout: []byte(NetworkID + "\n")}, nil)
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "create" {
		imageIndex := -1
		for index, value := range args {
			if value == ImageID {
				imageIndex = index
				break
			}
		}
		if imageIndex < 0 {
			return runtimeprocess.CommandResult{}, nil, false
		}
		if r.container || !r.network || imageIndex+3 >= len(args) ||
			!reflect.DeepEqual(args[imageIndex+1:], []string{"run", "--config", "/config/active.json"}) {
			return result(runtimeprocess.CommandResult{}, errors.New("invalid generated domain container create"))
		}
		r.container = true
		return result(runtimeprocess.CommandResult{Stdout: []byte(ContainerID + "\n")}, nil)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && r.container {
		if name, ok := generatedDomainConfigTarget(args[3]); ok {
			contents, err := os.ReadFile(args[2])
			if err != nil {
				return result(runtimeprocess.CommandResult{}, err)
			}
			r.files[name] = append(r.files[name][:0], contents...)
			return result(runtimeprocess.CommandResult{}, nil)
		}
		if name, ok := generatedDomainConfigTarget(args[2]); ok && args[3] == "-" {
			contents, found := r.files[name]
			if !found {
				return result(runtimeprocess.CommandResult{}, errors.New("generated domain config is unavailable"))
			}
			value, err := tarResult(name, contents)
			return result(value, err)
		}
		return runtimeprocess.CommandResult{}, nil, false
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" && args[2] == ContainerID {
		active, found := r.files["active.json"]
		if !found {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain active config is unavailable"))
		}
		r.config = append(r.config[:0], active...)
		r.running = true
		return result(runtimeprocess.CommandResult{Stdout: []byte(ContainerID + "\n")}, nil)
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" && args[2] != ContainerID {
		return runtimeprocess.CommandResult{}, nil, false
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "connect" && args[3] == ContainerID {
		if !r.container || args[2] == "" || args[2] == "rig-generated-caddy-ingress-v1" {
			return result(runtimeprocess.CommandResult{}, errors.New("invalid generated domain network attachment"))
		}
		r.attachments[args[2]] = struct{}{}
		return result(runtimeprocess.CommandResult{}, nil)
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "disconnect" && args[3] == ContainerID {
		if _, attached := r.attachments[args[2]]; !attached {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain network attachment is unavailable"))
		}
		delete(r.attachments, args[2])
		return result(runtimeprocess.CommandResult{}, nil)
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "exec" && generatedDomainCaddyTarget(args[2]) {
		last := args[len(args)-1]
		if last == "http://127.0.0.1:2019/config/" {
			return result(runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.config...)}, nil)
		}
		if network, probe := generatedDomainGatewayReadinessProbe(args); probe {
			if _, attached := r.attachments[network]; !attached {
				return result(runtimeprocess.CommandResult{}, errors.New("generated domain gateway readiness network is unavailable"))
			}
			return result(runtimeprocess.CommandResult{Stdout: []byte("204")}, nil)
		}
		if strings.Contains(last, "/.well-known/rig-gateway/") {
			return result(runtimeprocess.CommandResult{Stdout: []byte("\n404")}, nil)
		}
		if len(args) == 6 && args[3] == "sh" && args[4] == "-c" {
			if r.options.DelegateCapacity {
				return runtimeprocess.CommandResult{}, nil, false
			}
			r.capacityCalls++
			capacity := strconv.FormatUint(r.options.MemoryAvailableBytes, 10) + " " +
				strconv.FormatUint(r.options.DiskAvailableBytes, 10) + "\n"
			return result(runtimeprocess.CommandResult{Stdout: []byte(capacity)}, nil)
		}
	}
	if len(args) == 7 && args[0] == "container" && args[1] == "exec" && generatedDomainCaddyTarget(args[2]) &&
		args[3] == "caddy" && (args[4] == "validate" || args[4] == "reload") && args[5] == "--config" {
		name, ok := generatedDomainConfigPath(args[6])
		contents, found := r.files[name]
		if !ok || !found {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain proposed config is unavailable"))
		}
		if args[4] == "reload" {
			call := r.reloadCalls
			r.reloadCalls++
			if call < len(r.reloadErrors) && r.reloadErrors[call] != nil {
				return result(runtimeprocess.CommandResult{}, r.reloadErrors[call])
			}
			r.config = append(r.config[:0], contents...)
		}
		return result(runtimeprocess.CommandResult{}, nil)
	}
	if len(args) == 8 && args[0] == "container" && args[1] == "exec" && args[2] == "--user" &&
		args[3] == "0:0" && generatedDomainCaddyTarget(args[4]) && (args[5] == "cp" || args[5] == "mv") {
		source, sourceOK := generatedDomainConfigPath(args[6])
		destination, destinationOK := generatedDomainConfigPath(args[7])
		contents, found := r.files[source]
		if !sourceOK || !destinationOK || !found {
			return result(runtimeprocess.CommandResult{}, errors.New("generated domain config promotion is unavailable"))
		}
		r.files[destination] = append(r.files[destination][:0], contents...)
		if args[5] == "mv" {
			delete(r.files, source)
		}
		return result(runtimeprocess.CommandResult{}, nil)
	}
	return runtimeprocess.CommandResult{}, nil, false
}

func generatedDomainStartupNetworkProjection(format string) bool {
	for _, required := range []string{
		`"Id":{{json .ID}}`,
		`"Name":{{json .Name}}`,
		`"IPAM":{"Config":[`,
		`"Containers":{`,
		`"Name":{{json $container.Name}}`,
		`"IPv4Address":{{json $container.IPv4Address}}`,
	} {
		if !strings.Contains(format, required) {
			return false
		}
	}
	return true
}

func generatedDomainGatewayReadinessProbe(args []string) (string, bool) {
	prefix := []string{
		"container", "exec", ContainerID,
		"curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
	}
	if len(args) != len(prefix)+1 || !reflect.DeepEqual(args[:len(prefix)], prefix) {
		return "", false
	}
	endpoint, err := url.Parse(args[len(prefix)])
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "/" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", false
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	if err != nil {
		return "", false
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	separator := strings.LastIndexByte(host, '.')
	if err != nil || portNumber == 0 || separator <= 0 || separator == len(host)-1 {
		return "", false
	}
	return host[separator+1:], true
}

func generatedDomainCaddyTarget(value string) bool {
	return value == ContainerID || value == "rig-generated-caddy-v1"
}

func generatedDomainConfigTarget(value string) (string, bool) {
	target, path, ok := strings.Cut(value, ":")
	if !ok || !generatedDomainCaddyTarget(target) {
		return "", false
	}
	return generatedDomainConfigPath(path)
}

func generatedDomainConfigPath(value string) (string, bool) {
	const prefix = "/config/"
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(value, prefix)
	return name, name != "" && strings.HasSuffix(name, ".json") && !strings.ContainsAny(name, `/\\`)
}

func (r *Runner) volumeInspection() map[string]any {
	return map[string]any{"name": "rig-generated-caddy-config-v1", "driver": "local", "scope": "local", "options": map[string]string{}, "labels": map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.bootstrap-volume-nonce": r.volumeNonce}, "mountpoint": "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data", "createdAt": "2026-10-22T23:59:00Z"}
}

func (*Runner) networkLabels() map[string]string {
	return map[string]string{"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v1"}
}

func (*Runner) containerLabels() map[string]string {
	return map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.listener-isolation": "v1"}
}

func (r *Runner) networkInspection() map[string]any {
	containers := map[string]any{}
	if r.container {
		containers[ContainerID] = map[string]any{"Name": "rig-generated-caddy-v1", "IPv4Address": "10.203.0.2/28"}
	}
	return map[string]any{"Id": NetworkID, "Name": "rig-generated-caddy-ingress-v1", "Driver": "bridge", "Scope": "local", "Internal": false, "Options": map[string]string{}, "IPAM": map[string]any{"Config": []map[string]string{{"Subnet": "10.203.0.0/28", "Gateway": "10.203.0.1"}}}, "Labels": r.networkLabels(), "Containers": containers}
}

func (r *Runner) containerInspection() map[string]any {
	port := strconv.FormatUint(uint64(r.options.HostPort), 10)
	effectiveBindings := map[string][]map[string]string{}
	if r.running {
		effectiveBindings["8080/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": port}}
	}
	address := ""
	if r.running {
		address = "10.203.0.2"
	}
	liveNetworks := map[string]any{"rig-generated-caddy-ingress-v1": map[string]any{"Aliases": []string{}, "IPAddress": address, "GwPriority": 1, "IPv6Gateway": ""}}
	configuredNetworks := map[string]any{"rig-generated-caddy-ingress-v1": map[string]any{"IPAMConfig": map[string]string{"IPv4Address": "10.203.0.2", "IPv6Address": ""}, "NetworkID": NetworkID, "EndpointID": "", "IPAddress": address, "GwPriority": 1, "IPv6Gateway": ""}}
	for name := range r.attachments {
		liveNetworks[name] = map[string]any{"Aliases": []string{}, "IPAddress": "", "GwPriority": 0, "IPv6Gateway": ""}
		configuredNetworks[name] = map[string]any{"IPAMConfig": map[string]string{"IPv4Address": "", "IPv6Address": ""}, "NetworkID": "", "EndpointID": "", "IPAddress": "", "GwPriority": 0, "IPv6Gateway": ""}
	}
	return map[string]any{"id": ContainerID, "name": "/rig-generated-caddy-v1", "image": ImageID, "labels": r.containerLabels(), "hostname": "rig-generated-caddy-v1", "user": "1000:1000", "env": []string{"PATH=/usr/local/bin", "XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"}, "entrypoint": []string{"/usr/bin/caddy"}, "cmd": []string{"run", "--config", "/config/active.json"}, "readOnly": true, "privileged": false, "capAdd": []string{"NET_BIND_SERVICE"}, "capDrop": []string{"ALL"}, "securityOpt": []string{"no-new-privileges"}, "binds": []string{}, "mounts": []map[string]any{{"Type": "volume", "Name": "rig-generated-caddy-config-v1", "Destination": "/config", "RW": true}, {"Type": "tmpfs", "Destination": "/data", "RW": true}}, "tmpfs": map[string]string{"/data": "rw,noexec,nosuid,nodev,size=67108864"}, "memory": int64(268435456), "memorySwap": int64(268435456), "nanoCpus": int64(1000000000), "pidsLimit": int64(128), "logType": "local", "logConfig": map[string]string{"max-size": "10m", "max-file": "3"}, "restart": "unless-stopped", "networkMode": "rig-generated-caddy-ingress-v1", "ulimits": []map[string]any{{"Name": "nofile", "Hard": int64(1024), "Soft": int64(1024)}}, "running": r.running, "restarting": false, "portBindings": map[string][]map[string]string{"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": port}}}, "networks": liveNetworks, "paused": false, "dead": false, "restartCount": 0, "effectivePortBindings": effectiveBindings, "configuredNetworks": configuredNetworks}
}

func isGeneratedDomainCensusCommand(args []string) bool {
	return reflect.DeepEqual(args, []string{"container", "ls", "--all", "--no-trunc", "--format", `{"id":{{json .ID}}}`}) ||
		reflect.DeepEqual(args, []string{"network", "ls", "--no-trunc", "--format", `{"id":{{json .ID}}}`}) ||
		reflect.DeepEqual(args, []string{"volume", "ls", "--format", `{"name":{{json .Name}}}`}) ||
		reflect.DeepEqual(args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"})
}

func mergeGeneratedDomainCensus(owned, observed runtimeprocess.CommandResult) runtimeprocess.CommandResult {
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	for _, body := range [][]byte{owned.Stdout, observed.Stdout} {
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, duplicate := seen[line]; duplicate {
				continue
			}
			seen[line] = struct{}{}
			lines = append(lines, line)
		}
	}
	result := observed
	if len(lines) == 0 {
		result.Stdout = nil
	} else {
		result.Stdout = []byte(strings.Join(lines, "\n") + "\n")
	}
	result.StdoutTruncated = owned.StdoutTruncated || observed.StdoutTruncated
	result.StderrTruncated = owned.StderrTruncated || observed.StderrTruncated
	result.Stderr = append(append([]byte(nil), owned.Stderr...), observed.Stderr...)
	return result
}

func jsonResult(value any) (runtimeprocess.CommandResult, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return runtimeprocess.CommandResult{Stdout: body}, nil
}

func tarResult(name string, body []byte) (runtimeprocess.CommandResult, error) {
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if _, err := writer.Write(body); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if err := writer.Close(); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return runtimeprocess.CommandResult{Stdout: output.Bytes()}, nil
}
