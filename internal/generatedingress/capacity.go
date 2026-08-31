package generatedingress

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/generatedruntime"
)

// The command is controller-owned and fixed. It executes inside the pinned,
// network-isolated Caddy container, not a host shell. /config is a local Docker
// volume, so df measures the Docker data plane rather than Rig's source disk.
const capacityProbeCommand = `memory=$(awk '/MemAvailable:/ {print $2*1024}' /proc/meminfo); disk=$(df -Pk /config | awk 'NR==2 {print $4*1024}'); printf '%s %s\n' "$memory" "$disk"`

// Snapshot implements generatedruntime.CapacitySource. Provision must have
// succeeded at startup; this method is intentionally read-only apart from the
// fixed process executed inside the already-running Caddy container.
func (m *Manager) Snapshot(ctx context.Context) (generatedruntime.CapacitySnapshot, error) {
	if m == nil || ctx == nil {
		return generatedruntime.CapacitySnapshot{}, errors.New("generated ingress capacity source is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inspection, found, err := m.inspectCaddy(ctx)
	if err != nil || !found || !inspection.Running || inspection.Labels["io.rig.managed"] != "generated-ingress" || inspection.Labels["io.rig.identity-version"] != "v1" {
		return generatedruntime.CapacitySnapshot{}, errors.New("generated ingress capacity probe is unavailable")
	}
	result, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "sh", "-c", capacityProbeCommand)
	if err != nil {
		return generatedruntime.CapacitySnapshot{}, errors.New("generated ingress capacity probe failed")
	}
	fields := strings.Fields(string(result.Stdout))
	clearResult(&result)
	if len(fields) != 2 {
		return generatedruntime.CapacitySnapshot{}, errors.New("generated ingress capacity probe is invalid")
	}
	memory, memoryErr := strconv.ParseUint(fields[0], 10, 64)
	disk, diskErr := strconv.ParseUint(fields[1], 10, 64)
	if memoryErr != nil || diskErr != nil {
		return generatedruntime.CapacitySnapshot{}, errors.New("generated ingress capacity probe is invalid")
	}
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: memory, DiskAvailableBytes: disk}, nil
}
