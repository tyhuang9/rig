// Package generatedcontainer describes physical container observations. These
// values carry no deployment, command, cleanup, or capacity-release authority.
package generatedcontainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// InspectFormat projects only the physical fields used by candidate policy.
// In particular, it never returns Config.Env or the full container document.
const InspectFormat = `{"id":{{json .ID}},"name":{{json .Name}},"image":{{json .Image}},"configImage":{{json .Config.Image}},"labels":{{json .Config.Labels}},"user":{{json .Config.User}},"workingDir":{{json .Config.WorkingDir}},"entrypoint":{{json .Config.Entrypoint}},"cmd":{{json .Config.Cmd}},"volumes":{{json .Config.Volumes}},"healthTest":{{if .Config.Healthcheck}}{{json .Config.Healthcheck.Test}}{{else}}null{{end}},"healthInterval":{{if .Config.Healthcheck}}{{json .Config.Healthcheck.Interval}}{{else}}null{{end}},"healthTimeout":{{if .Config.Healthcheck}}{{json .Config.Healthcheck.Timeout}}{{else}}null{{end}},"healthStartPeriod":{{if .Config.Healthcheck}}{{json .Config.Healthcheck.StartPeriod}}{{else}}null{{end}},"healthRetries":{{if .Config.Healthcheck}}{{json .Config.Healthcheck.Retries}}{{else}}null{{end}},"memory":{{json .HostConfig.Memory}},"memorySwap":{{json .HostConfig.MemorySwap}},"nanoCpus":{{json .HostConfig.NanoCPUs}},"pidsLimit":{{json .HostConfig.PidsLimit}},"ulimits":{{json .HostConfig.Ulimits}},"init":{{json .HostConfig.Init}},"networkMode":{{json .HostConfig.NetworkMode}},"readonlyRootfs":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"capAdd":{{json .HostConfig.CapAdd}},"capDrop":{{json .HostConfig.CapDrop}},"securityOpt":{{json .HostConfig.SecurityOpt}},"binds":{{json .HostConfig.Binds}},"hostMounts":{{json .HostConfig.Mounts}},"portBindings":{{json .HostConfig.PortBindings}},"tmpfs":{{json .HostConfig.Tmpfs}},"logType":{{json .HostConfig.LogConfig.Type}},"logConfig":{{json .HostConfig.LogConfig.Config}},"restart":{{json .HostConfig.RestartPolicy.Name}},"status":{{json .State.Status}},"running":{{json .State.Running}},"paused":{{json .State.Paused}},"restarting":{{json .State.Restarting}},"dead":{{json .State.Dead}},"exitCode":{{json .State.ExitCode}},"startedAt":{{json .State.StartedAt}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"networks":{{if .NetworkSettings}}{{json .NetworkSettings.Networks}}{{else}}null{{end}},"mounts":{{json .Mounts}}}`

type Inspection struct {
	ID                string                       `json:"id"`
	Name              string                       `json:"name"`
	Image             string                       `json:"image"`
	ConfigImage       string                       `json:"configImage"`
	Labels            map[string]string            `json:"labels"`
	User              string                       `json:"user"`
	WorkingDirectory  string                       `json:"workingDir"`
	Entrypoint        []string                     `json:"entrypoint"`
	Command           []string                     `json:"cmd"`
	Volumes           map[string]json.RawMessage   `json:"volumes"`
	HealthTest        []string                     `json:"healthTest"`
	HealthInterval    int64                        `json:"healthInterval"`
	HealthTimeout     int64                        `json:"healthTimeout"`
	HealthStartPeriod int64                        `json:"healthStartPeriod"`
	HealthRetries     int                          `json:"healthRetries"`
	Memory            int64                        `json:"memory"`
	MemorySwap        int64                        `json:"memorySwap"`
	NanoCPUs          int64                        `json:"nanoCpus"`
	PIDs              int64                        `json:"pidsLimit"`
	Ulimits           []Ulimit                     `json:"ulimits"`
	Init              bool                         `json:"init"`
	NetworkMode       string                       `json:"networkMode"`
	ReadonlyRootfs    bool                         `json:"readonlyRootfs"`
	Privileged        bool                         `json:"privileged"`
	CapAdd            []string                     `json:"capAdd"`
	CapDrop           []string                     `json:"capDrop"`
	SecurityOptions   []string                     `json:"securityOpt"`
	Binds             []string                     `json:"binds"`
	HostMounts        []json.RawMessage            `json:"hostMounts"`
	PortBindings      map[string]json.RawMessage   `json:"portBindings"`
	Tmpfs             map[string]string            `json:"tmpfs"`
	LogType           string                       `json:"logType"`
	LogConfig         map[string]string            `json:"logConfig"`
	Restart           string                       `json:"restart"`
	Status            string                       `json:"status"`
	Running           bool                         `json:"running"`
	Paused            bool                         `json:"paused"`
	Restarting        bool                         `json:"restarting"`
	Dead              bool                         `json:"dead"`
	ExitCode          int                          `json:"exitCode"`
	StartedAt         string                       `json:"startedAt"`
	Health            string                       `json:"health"`
	Networks          map[string]NetworkAttachment `json:"networks"`
	Mounts            []Mount                      `json:"mounts"`
}

type Ulimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

type NetworkAttachment struct {
	Aliases []string `json:"Aliases"`
}

type Mount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

var ErrIncompleteInspection = errors.New("incomplete generated container inspection")

var inspectionFields = []string{
	"id", "name", "image", "configImage", "labels", "user", "workingDir", "entrypoint", "cmd", "volumes",
	"healthTest", "healthInterval", "healthTimeout", "healthStartPeriod", "healthRetries", "memory", "memorySwap", "nanoCpus", "pidsLimit", "ulimits",
	"init", "networkMode", "readonlyRootfs", "privileged", "capAdd", "capDrop", "securityOpt", "binds", "hostMounts", "portBindings", "tmpfs",
	"logType", "logConfig", "restart", "status", "running", "paused", "restarting", "dead", "exitCode", "startedAt", "health", "networks", "mounts",
}

var scalarFields = []string{
	"id", "name", "image", "configImage", "user", "workingDir", "healthInterval", "healthTimeout", "healthStartPeriod", "healthRetries",
	"memory", "memorySwap", "nanoCpus", "pidsLimit", "init", "networkMode", "readonlyRootfs", "privileged", "logType", "restart",
	"status", "running", "paused", "restarting", "dead", "exitCode", "startedAt", "health",
}

// Decode rejects incomplete or ambiguous JSON. It intentionally returns a
// constant error instead of echoing untrusted process output or field values.
// Callers must separately establish successful bounded process completion.
func Decode(data []byte) (Inspection, error) {
	if !utf8.Valid(data) || !uniqueJSON(data) {
		return Inspection{}, ErrIncompleteInspection
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(inspectionFields) {
		return Inspection{}, ErrIncompleteInspection
	}
	for _, name := range inspectionFields {
		if _, ok := fields[name]; !ok {
			return Inspection{}, ErrIncompleteInspection
		}
	}
	for _, name := range scalarFields {
		if jsonNull(fields[name]) {
			return Inspection{}, ErrIncompleteInspection
		}
	}
	for _, name := range []string{"entrypoint", "cmd", "healthTest", "capAdd", "capDrop", "securityOpt", "binds"} {
		if !stringArray(fields[name]) {
			return Inspection{}, ErrIncompleteInspection
		}
	}
	for _, name := range []string{"labels", "tmpfs", "logConfig"} {
		if !stringMap(fields[name]) {
			return Inspection{}, ErrIncompleteInspection
		}
	}
	if !completeObjectArray(fields["ulimits"], []string{"Name", "Soft", "Hard"}) ||
		!completeObjectArray(fields["mounts"], []string{"Type", "Source", "Destination"}) || !completeNetworks(fields["networks"]) {
		return Inspection{}, ErrIncompleteInspection
	}
	var value Inspection
	if json.Unmarshal(data, &value) != nil {
		return Inspection{}, ErrIncompleteInspection
	}
	return value, nil
}

func jsonNull(data []byte) bool { return bytes.Equal(bytes.TrimSpace(data), []byte("null")) }

func stringArray(data []byte) bool {
	var values []json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, raw := range values {
		var value string
		if jsonNull(raw) || json.Unmarshal(raw, &value) != nil {
			return false
		}
	}
	return true
}

func stringMap(data []byte) bool {
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, raw := range values {
		var value string
		if jsonNull(raw) || json.Unmarshal(raw, &value) != nil {
			return false
		}
	}
	return true
}

func completeObjectArray(data []byte, required []string) bool {
	var values []map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return false
	}
	for _, value := range values {
		for _, name := range required {
			raw, ok := value[name]
			if !ok || jsonNull(raw) {
				return false
			}
			for key := range value {
				if key != name && strings.EqualFold(key, name) {
					return false
				}
			}
		}
	}
	return true
}

func completeNetworks(data []byte) bool {
	var networks map[string]map[string]json.RawMessage
	if json.Unmarshal(data, &networks) != nil {
		return false
	}
	for _, network := range networks {
		aliases, ok := network["Aliases"]
		if !ok || !stringArray(aliases) {
			return false
		}
		for key := range network {
			if key != "Aliases" && strings.EqualFold(key, "Aliases") {
				return false
			}
		}
	}
	return true
}

func uniqueJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !uniqueValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func uniqueValue(decoder *json.Decoder, depth int) bool {
	if depth > 64 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !uniqueValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !uniqueValue(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
