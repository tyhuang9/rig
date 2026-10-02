//go:build !linux && !windows

package hostnetwork

import (
	"fmt"
	"net/netip"
	"runtime"
)

func currentIPv4Routes() ([]netip.Prefix, error) {
	return nil, fmt.Errorf("%w: IPv4 route enumeration is unsupported on %s", ErrIncompleteHostSnapshot, runtime.GOOS)
}
