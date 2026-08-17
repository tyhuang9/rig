//go:build windows

package docker

import (
	"errors"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type memoryStatusEx struct {
	Length                   uint32
	MemoryLoad               uint32
	TotalPhysical            uint64
	AvailablePhysical        uint64
	TotalPageFile            uint64
	AvailablePageFile        uint64
	TotalVirtual             uint64
	AvailableVirtual         uint64
	AvailableExtendedVirtual uint64
}

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func collectHostResources(root string) (HostResources, error) {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	result, _, callErr := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return HostResources{}, callErr
	}
	if root == "" {
		root = "."
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return HostResources{}, err
	}
	rootPath := filepath.VolumeName(absolute) + `\`
	rootUTF16, err := windows.UTF16PtrFromString(rootPath)
	if err != nil {
		return HostResources{}, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(rootUTF16, &available, &total, &free); err != nil {
		return HostResources{}, err
	}
	if total == 0 || status.TotalPhysical == 0 {
		return HostResources{}, errors.New("host resource totals are unavailable")
	}
	return HostResources{MemoryTotalBytes: status.TotalPhysical, MemoryAvailableBytes: status.AvailablePhysical, DiskTotalBytes: total, DiskAvailableBytes: available}, nil
}
