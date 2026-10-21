package generatedcontainer

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	ReceiptLabel        = "io.rig.candidate-receipt"
	User                = "node"
	Entrypoint          = "/usr/local/bin/rig-entrypoint"
	NextCacheTmpfsBytes = 32 << 20
)

// HealthCommand is fixed controller policy. Plan values are read from the
// protected container environment, not interpolated into a shell command.
const HealthCommand = `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`

type Limits struct {
	MemoryBytes int64
	MilliCPUs   int64
	PIDs        int64
	TmpfsBytes  int64
	LogSize     string
	LogFiles    int
}

// Identity describes expected physical facts, not an executable permit.
// A command owner must obtain these facts from protected durable provenance.
type Identity struct {
	AppID, ReleaseID, DeploymentID, ArtifactID, DeploymentPlanRevisionID string
	Component, Role, Technology, Slot, ReceiptID                         string
	ContainerID, ContainerName, NetworkName, NetworkAlias                string
	ImageContentID, WorkingDirectory, RunCommandDigest                   string
}

// MatchesOwnership is a generic physical identity comparison, including legacy
// observations without a receipt. It grants no candidate mutation authority.
// Candidate policy uses MatchesConfigured, which requires the exact receipt.
func MatchesOwnership(actual Inspection, expected Identity) bool {
	if expected.ContainerID == "" || expected.ContainerName == "" || actual.ID != expected.ContainerID ||
		strings.TrimPrefix(actual.Name, "/") != expected.ContainerName {
		return false
	}
	labels := map[string]string{
		"io.rig.managed": "generated-runtime", "io.rig.application": expected.AppID,
		"io.rig.release": expected.ReleaseID, "io.rig.deployment": expected.DeploymentID,
		"io.rig.artifact": expected.ArtifactID, "io.rig.plan": expected.DeploymentPlanRevisionID,
		"io.rig.component": expected.Component, "io.rig.slot": expected.Slot, "io.rig.role": expected.Role,
	}
	if expected.ReceiptID != "" {
		labels[ReceiptLabel] = expected.ReceiptID
	}
	for key, value := range labels {
		if value == "" || actual.Labels[key] != value {
			return false
		}
	}
	return true
}

// MatchesConfigured validates immutable create-time policy. Docker need not
// have materialized the endpoint or tmpfs mounts of a never-started container.
// Any mounts it does report must still be within the configured tmpfs set.
func MatchesConfigured(actual Inspection, expected Identity, limits Limits) bool {
	if expected.ReceiptID == "" || !MatchesOwnership(actual, expected) || actual.Image != expected.ImageContentID || actual.ConfigImage != expected.ImageContentID ||
		actual.User != User || actual.WorkingDirectory != expected.WorkingDirectory || len(actual.Entrypoint) != 1 || actual.Entrypoint[0] != Entrypoint ||
		len(actual.Command) != 3 || actual.Command[0] != "/bin/sh" || actual.Command[1] != "-lc" || digest(actual.Command[2]) != expected.RunCommandDigest {
		return false
	}
	if len(actual.HealthTest) != 2 || actual.HealthTest[0] != "CMD-SHELL" || actual.HealthTest[1] != HealthCommand ||
		actual.HealthInterval != int64(2*time.Second) || actual.HealthTimeout != int64(2*time.Second) ||
		actual.HealthStartPeriod != int64(5*time.Second) || actual.HealthRetries != 3 {
		return false
	}
	if limits.MemoryBytes <= 0 || limits.MilliCPUs <= 0 || limits.MilliCPUs > 16000 || limits.PIDs <= 0 || limits.TmpfsBytes <= 0 ||
		actual.Memory != limits.MemoryBytes || actual.MemorySwap != limits.MemoryBytes || actual.NanoCPUs != limits.MilliCPUs*1_000_000 || actual.PIDs != limits.PIDs ||
		len(actual.Ulimits) != 1 || actual.Ulimits[0] != (Ulimit{Name: "nofile", Soft: 1024, Hard: 1024}) ||
		!actual.Init || actual.NetworkMode != expected.NetworkName || !actual.ReadonlyRootfs || actual.Privileged ||
		len(actual.CapAdd) != 0 || len(actual.CapDrop) != 1 || !strings.EqualFold(actual.CapDrop[0], "ALL") || !noNewPrivileges(actual.SecurityOptions) {
		return false
	}
	if len(actual.Binds) != 0 || len(actual.HostMounts) != 0 || len(actual.Volumes) != 0 || len(actual.PortBindings) != 0 ||
		actual.Restart != "no" || actual.LogType != "local" || actual.LogConfig["max-size"] != limits.LogSize || actual.LogConfig["max-file"] != strconv.Itoa(limits.LogFiles) {
		return false
	}
	if !exactCommaValues(actual.Tmpfs["/tmp"], []string{"rw", "noexec", "nosuid", "nodev", "size=" + strconv.FormatInt(limits.TmpfsBytes, 10)}) {
		return false
	}
	if expected.Technology == "nextjs" {
		if len(actual.Tmpfs) != 2 || !exactCommaValues(actual.Tmpfs[expected.WorkingDirectory+"/.next/cache"],
			[]string{"rw", "noexec", "nosuid", "nodev", "uid=1000", "gid=1000", "mode=0700", "size=" + strconv.FormatInt(NextCacheTmpfsBytes, 10)}) {
			return false
		}
	} else if len(actual.Tmpfs) != 1 {
		return false
	}
	if len(actual.Networks) != 0 {
		attachment, ok := actual.Networks[expected.NetworkName]
		if !ok || len(actual.Networks) != 1 {
			return false
		}
		if len(attachment.Aliases) != 0 && !hasAlias(attachment.Aliases, expected.NetworkAlias) {
			return false
		}
	}
	seen := map[string]bool{}
	for _, mount := range actual.Mounts {
		if mount.Type != "tmpfs" || mount.Source != "" || actual.Tmpfs[mount.Destination] == "" || seen[mount.Destination] {
			return false
		}
		seen[mount.Destination] = true
	}
	return true
}

// MatchesRunning additionally checks the realized application network. State
// and health are separate observations and must be checked by the owner.
func MatchesRunning(actual Inspection, expected Identity, limits Limits) bool {
	if !MatchesConfigured(actual, expected, limits) {
		return false
	}
	attachment, ok := actual.Networks[expected.NetworkName]
	if !ok || len(actual.Networks) != 1 || expected.NetworkAlias == "" {
		return false
	}
	return hasAlias(attachment.Aliases, expected.NetworkAlias)
}

func hasAlias(aliases []string, expected string) bool {
	if expected == "" {
		return false
	}
	for _, alias := range aliases {
		if alias == expected {
			return true
		}
	}
	return false
}

// NeverStarted requires Docker's exact created state and its zero timestamp.
// Missing timestamps, contradictory flags, and former running states refuse.
func NeverStarted(actual Inspection) bool {
	started, err := time.Parse(time.RFC3339Nano, actual.StartedAt)
	return err == nil && started.IsZero() && actual.Status == "created" && !actual.Running && !actual.Paused && !actual.Restarting && !actual.Dead && actual.ExitCode == 0
}

func CurrentlyRunning(actual Inspection) bool {
	started, err := time.Parse(time.RFC3339Nano, actual.StartedAt)
	return err == nil && !started.IsZero() && actual.Status == "running" && actual.Running && !actual.Paused && !actual.Restarting && !actual.Dead && actual.ExitCode == 0
}

func noNewPrivileges(values []string) bool {
	if len(values) != 1 {
		return false
	}
	switch strings.ToLower(values[0]) {
	case "no-new-privileges", "no-new-privileges:true", "no-new-privileges=true":
		return true
	default:
		return false
	}
}

func exactCommaValues(value string, expected []string) bool {
	parts := strings.Split(value, ",")
	if len(parts) != len(expected) {
		return false
	}
	counts := make(map[string]int, len(parts))
	for _, item := range parts {
		counts[item]++
	}
	for _, item := range expected {
		if counts[item] != 1 {
			return false
		}
	}
	return true
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
