//go:build windows

package hostnetwork

import (
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentIPv4Routes() ([]netip.Prefix, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	if table == nil {
		return nil, fmt.Errorf("%w: GetIpForwardTable2 returned no table", ErrIncompleteHostSnapshot)
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	return prefixesFromWindowsRouteRows(table.Rows())
}

func prefixesFromWindowsRouteRows(rows []windows.MibIpForwardRow2) ([]netip.Prefix, error) {
	routes := make([]netip.Prefix, 0, len(rows))
	for _, row := range rows {
		destination := row.DestinationPrefix
		if destination.Prefix.Family != windows.AF_INET || destination.PrefixLength > 32 {
			return nil, fmt.Errorf("%w: invalid IPv4 route returned by GetIpForwardTable2", ErrIncompleteHostSnapshot)
		}
		raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&destination.Prefix))
		prefix := netip.PrefixFrom(netip.AddrFrom4(raw.Addr), int(destination.PrefixLength))
		if prefix != prefix.Masked() {
			return nil, fmt.Errorf("%w: non-canonical IPv4 route %s", ErrIncompleteHostSnapshot, prefix)
		}
		routes = append(routes, prefix)
	}
	sortPrefixes(routes)
	return routes, nil
}
