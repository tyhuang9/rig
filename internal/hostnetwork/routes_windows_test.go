//go:build windows

package hostnetwork

import (
	"errors"
	"net/netip"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPrefixesFromWindowsRouteRows(t *testing.T) {
	rows := []windows.MibIpForwardRow2{
		windowsRouteRow([4]byte{}, 0),
		windowsRouteRow([4]byte{192, 168, 40, 0}, 24),
	}
	routes, err := prefixesFromWindowsRouteRows(rows)
	if err != nil || len(routes) != 2 || routes[0] != netip.MustParsePrefix("0.0.0.0/0") || routes[1] != netip.MustParsePrefix("192.168.40.0/24") {
		t.Fatalf("routes = %v, err = %v", routes, err)
	}

	invalid := windowsRouteRow([4]byte{192, 168, 40, 1}, 24)
	if _, err := prefixesFromWindowsRouteRows([]windows.MibIpForwardRow2{invalid}); !errors.Is(err, ErrIncompleteHostSnapshot) {
		t.Fatalf("non-canonical route error = %v", err)
	}
}

func windowsRouteRow(address [4]byte, bits uint8) windows.MibIpForwardRow2 {
	var row windows.MibIpForwardRow2
	raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.DestinationPrefix.Prefix))
	raw.Family = windows.AF_INET
	raw.Addr = address
	row.DestinationPrefix.PrefixLength = bits
	return row
}
