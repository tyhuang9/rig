//go:build linux

package docker

import (
	"errors"

	"golang.org/x/sys/unix"
)

func collectHostResources(root string) (HostResources, error) {
	if root == "" {
		root = "."
	}
	var memory unix.Sysinfo_t
	if err := unix.Sysinfo(&memory); err != nil {
		return HostResources{}, err
	}
	var disk unix.Statfs_t
	if err := unix.Statfs(root, &disk); err != nil {
		return HostResources{}, err
	}
	unit := uint64(memory.Unit)
	resources := HostResources{MemoryTotalBytes: memory.Totalram * unit, MemoryAvailableBytes: (memory.Freeram + memory.Bufferram) * unit, DiskTotalBytes: disk.Blocks * uint64(disk.Bsize), DiskAvailableBytes: disk.Bavail * uint64(disk.Bsize)}
	if resources.MemoryTotalBytes == 0 || resources.DiskTotalBytes == 0 {
		return HostResources{}, errors.New("host resource totals are unavailable")
	}
	return resources, nil
}
