//go:build linux

package hostnetwork

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"syscall"
	"testing"
)

func TestParseLinuxRouteRIBRequiresCompleteDump(t *testing.T) {
	routeData := make([]byte, syscall.SizeofRtMsg+8)
	routeData[0] = syscall.AF_INET
	routeData[1] = 24
	binary.NativeEndian.PutUint16(routeData[syscall.SizeofRtMsg:], 8)
	binary.NativeEndian.PutUint16(routeData[syscall.SizeofRtMsg+2:], syscall.RTA_DST)
	copy(routeData[syscall.SizeofRtMsg+4:], []byte{10, 20, 30, 0})
	route := linuxNetlinkMessage(syscall.RTM_NEWROUTE, 0, routeData)
	done := linuxNetlinkMessage(syscall.NLMSG_DONE, 0, nil)

	routes, err := parseLinuxRouteRIB(append(route, done...))
	if err != nil || len(routes) != 1 || routes[0] != netip.MustParsePrefix("10.20.30.0/24") {
		t.Fatalf("routes = %v, err = %v", routes, err)
	}
	if _, err := parseLinuxRouteRIB(route); !errors.Is(err, ErrIncompleteHostSnapshot) {
		t.Fatalf("missing completion error = %v", err)
	}
	interrupted := linuxNetlinkMessage(syscall.NLMSG_DONE, netlinkDumpInterrupted, nil)
	if _, err := parseLinuxRouteRIB(append(route, interrupted...)); !errors.Is(err, ErrIncompleteHostSnapshot) {
		t.Fatalf("interrupted dump error = %v", err)
	}
}

func linuxNetlinkMessage(messageType, flags uint16, data []byte) []byte {
	length := syscall.NLMSG_HDRLEN + len(data)
	message := make([]byte, netlinkAlignedLength(length))
	binary.NativeEndian.PutUint32(message[0:4], uint32(length))
	binary.NativeEndian.PutUint16(message[4:6], messageType)
	binary.NativeEndian.PutUint16(message[6:8], flags)
	copy(message[syscall.NLMSG_HDRLEN:], data)
	return message
}
