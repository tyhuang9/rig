package generatedbuilder

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	IdentityFilename        = "builder-identity-v2.json"
	LegacyIdentityFilename  = "builder-identity.json"
	StateSchema             = 2
	IdentityMaximumBytes    = int64(8 << 10)
	ObservationMaximumBytes = int64(64 << 10)

	ImageReference         = "moby/buildkit@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8"
	StatePath              = "/var/lib/buildkit"
	DefaultStateQuotaBytes = int64(2 << 30)
	MinimumStateQuotaBytes = int64(512 << 20)
	MaximumStateQuotaBytes = int64(16 << 30)
	PIDsLimit              = int64(512)
	MemoryHeadroom         = int64(1 << 30)
)

type Identity struct {
	Schema      int    `json:"schema"`
	BuilderName string `json:"builderName"`
	NodeName    string `json:"nodeName"`
	NetworkName string `json:"networkName"`
}

type NetworkContainer struct {
	Name        string `json:"Name"`
	IPv4Address string `json:"IPv4Address"`
}

type Network struct {
	ID         string                      `json:"Id"`
	Name       string                      `json:"Name"`
	Driver     string                      `json:"Driver"`
	Scope      string                      `json:"Scope"`
	Internal   bool                        `json:"Internal"`
	Labels     map[string]string           `json:"Labels"`
	Options    map[string]string           `json:"Options"`
	Containers map[string]NetworkContainer `json:"Containers"`
}

type Mount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}

type ConfiguredMount struct {
	Type         string `json:"Type"`
	Source       string `json:"Source"`
	Target       string `json:"Target"`
	ReadOnly     bool   `json:"ReadOnly"`
	TmpfsOptions *struct {
		SizeBytes int64 `json:"SizeBytes"`
		Mode      int64 `json:"Mode"`
	} `json:"TmpfsOptions"`
}

type NetworkAttachment struct {
	NetworkID string `json:"NetworkID"`
}

type Container struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Running    bool `json:"Running"`
		Paused     bool `json:"Paused"`
		Restarting bool `json:"Restarting"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Cmd    []string          `json:"Cmd"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Memory       int64                      `json:"Memory"`
		MemorySwap   int64                      `json:"MemorySwap"`
		CPUPeriod    int64                      `json:"CpuPeriod"`
		CPUQuota     int64                      `json:"CpuQuota"`
		PidsLimit    int64                      `json:"PidsLimit"`
		NetworkMode  string                     `json:"NetworkMode"`
		Privileged   bool                       `json:"Privileged"`
		Binds        []string                   `json:"Binds"`
		Mounts       []ConfiguredMount          `json:"Mounts"`
		PortBindings map[string]json.RawMessage `json:"PortBindings"`
		LogConfig    struct {
			Type   string            `json:"Type"`
			Config map[string]string `json:"Config"`
		} `json:"LogConfig"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []Mount `json:"Mounts"`
}

type Image struct {
	ID     string            `json:"Id"`
	Labels map[string]string `json:"Labels"`
}

func NewIdentity() (Identity, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return Identity{}, err
	}
	suffix := hex.EncodeToString(random)
	return Identity{Schema: StateSchema, BuilderName: "rig-buildkit-" + suffix,
		NodeName: "rig-node-" + suffix, NetworkName: "rig-buildnet-" + suffix}, nil
}

func EncodeIdentity(identity Identity) ([]byte, error) {
	if !ValidIdentity(identity) {
		return nil, errors.New("generated builder identity is invalid")
	}
	return json.Marshal(identity)
}

func DecodeIdentity(body []byte) (Identity, error) {
	var identity Identity
	if err := decodeStrict(body, IdentityMaximumBytes, &identity); err != nil || !ValidIdentity(identity) {
		return Identity{}, errors.New("generated builder identity is invalid")
	}
	return identity, nil
}

func ValidIdentity(identity Identity) bool {
	return identity.Schema == StateSchema && validControllerName(identity.BuilderName, "rig-buildkit-") &&
		validControllerName(identity.NodeName, "rig-node-") &&
		validControllerName(identity.NetworkName, "rig-buildnet-")
}

func ContainerName(identity Identity) string {
	return "rig-buildkitd-" + strings.TrimPrefix(identity.NodeName, "rig-node-")
}

func MemoryLimit(quotaBytes int64) int64 { return quotaBytes + MemoryHeadroom }

func Command(quotaBytes int64) []string {
	return []string{
		"--oci-worker-net", "bridge",
		"--oci-worker-gc",
		"--oci-worker-gc-keepstorage=" + fmt.Sprintf("%d", quotaBytes/(2*1_000_000)),
		"--oci-max-parallelism=1",
	}
}

func ContainerLabels(identity Identity, quotaBytes int64) map[string]string {
	return map[string]string{
		"rig.controller":  "generated-builder",
		"rig.builder":     identity.BuilderName,
		"rig.network":     identity.NetworkName,
		"rig.quota.bytes": strconv.FormatInt(quotaBytes, 10),
	}
}

func NetworkLabels(identity Identity) map[string]string {
	return map[string]string{
		"rig.controller": "generated-builder",
		"rig.builder":    identity.BuilderName,
		"rig.network":    identity.NetworkName,
	}
}

func MatchesNetwork(network Network, identity Identity) bool {
	return network.Name == identity.NetworkName && network.Driver == "bridge" && network.Scope == "local" &&
		!network.Internal &&
		network.Options["com.docker.network.bridge.enable_icc"] == "false" &&
		containsLabels(network.Labels, NetworkLabels(identity))
}

func MatchesContainerConfiguration(container Container, identity Identity, quotaBytes int64) bool {
	if !lowerHex(container.ID, 64) || container.Name != "/"+ContainerName(identity) ||
		!validImageContentID(container.Image) || container.Config.Image != ImageReference ||
		!equalStrings(container.Config.Cmd, Command(quotaBytes)) ||
		!containsLabels(container.Config.Labels, ContainerLabels(identity, quotaBytes)) {
		return false
	}
	if container.HostConfig.Memory != MemoryLimit(quotaBytes) ||
		container.HostConfig.MemorySwap != MemoryLimit(quotaBytes) ||
		container.HostConfig.CPUPeriod != 100000 || container.HostConfig.CPUQuota != 100000 ||
		container.HostConfig.PidsLimit != PIDsLimit || !container.HostConfig.Privileged ||
		container.HostConfig.NetworkMode != identity.NetworkName || len(container.HostConfig.Binds) != 0 ||
		len(container.HostConfig.PortBindings) != 0 || container.HostConfig.RestartPolicy.Name != "unless-stopped" ||
		container.HostConfig.LogConfig.Type != "json-file" || len(container.HostConfig.LogConfig.Config) != 2 ||
		container.HostConfig.LogConfig.Config["max-size"] != "10m" ||
		container.HostConfig.LogConfig.Config["max-file"] != "1" ||
		len(container.NetworkSettings.Networks) != 1 {
		return false
	}
	if _, connected := container.NetworkSettings.Networks[identity.NetworkName]; !connected ||
		len(container.HostConfig.Mounts) != 1 || len(container.Mounts) != 1 {
		return false
	}
	configured := container.HostConfig.Mounts[0]
	active := container.Mounts[0]
	return configured.Type == "tmpfs" && configured.Source == "" && configured.Target == StatePath &&
		!configured.ReadOnly && configured.TmpfsOptions != nil &&
		configured.TmpfsOptions.SizeBytes == quotaBytes && configured.TmpfsOptions.Mode == 0o700 &&
		active.Type == "tmpfs" && active.Source == "" && active.Destination == StatePath && active.RW &&
		active.Mode == "" && active.Propagation == ""
}

func MatchesContainerNetwork(container Container, identity Identity, networkID string) bool {
	raw, connected := container.NetworkSettings.Networks[identity.NetworkName]
	var attachment NetworkAttachment
	return connected && lowerHex(networkID, 64) &&
		decodeStrict(raw, ObservationMaximumBytes, &attachment) == nil && attachment.NetworkID == networkID
}

func ContainerReady(container Container) bool {
	return container.State.Running && !container.State.Paused && !container.State.Restarting
}

func DecodeContainer(body []byte) (Container, error) {
	var value Container
	if err := decodeStrict(body, ObservationMaximumBytes, &value); err != nil {
		return Container{}, errors.New("generated builder container observation is invalid")
	}
	return value, nil
}

func DecodeNetwork(body []byte) (Network, error) {
	var value Network
	if err := decodeStrict(body, ObservationMaximumBytes, &value); err != nil {
		return Network{}, errors.New("generated builder network observation is invalid")
	}
	return value, nil
}

func DecodeImage(body []byte) (Image, error) {
	var value Image
	if err := decodeStrict(body, ObservationMaximumBytes, &value); err != nil {
		return Image{}, errors.New("generated builder image observation is invalid")
	}
	return value, nil
}

func containsLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func lowerHex(value string, width int) bool {
	if len(value) != width || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == width/2
}

func validImageContentID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func validControllerName(value, prefix string) bool {
	if len(value) < len(prefix)+24 || len(value) > 63 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func decodeStrict(body []byte, maximum int64, target any) error {
	if maximum <= 0 || len(body) == 0 || int64(len(body)) > maximum || rejectDuplicateKeys(body) != nil {
		return errors.New("generated builder JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("generated builder JSON has trailing data")
	}
	return nil
}

func rejectDuplicateKeys(body []byte) error {
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
				if keyErr != nil || !ok {
					return errors.New("generated builder JSON object is invalid")
				}
				folded := strings.ToLower(key)
				if _, duplicate := seen[folded]; duplicate {
					return errors.New("generated builder JSON key is duplicated")
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
			return errors.New("generated builder JSON delimiter is invalid")
		}
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("generated builder JSON has trailing data")
	}
	return nil
}
