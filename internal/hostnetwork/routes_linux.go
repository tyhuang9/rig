//go:build linux

package hostnetwork

import (
	"fmt"
	"net/netip"
	"syscall"
)

const netlinkDumpInterrupted = 0x10

func currentIPv4Routes() ([]netip.Prefix, error) {
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_INET)
	if err != nil {
		return nil, err
	}
	return parseLinuxRouteRIB(rib)
}

func parseLinuxRouteRIB(rib []byte) ([]netip.Prefix, error) {
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, fmt.Errorf("parse netlink route dump: %w", err)
	}
	consumed := 0
	done := false
	var routes []netip.Prefix
	for index, message := range messages {
		consumed += netlinkAlignedLength(int(message.Header.Len))
		if message.Header.Flags&netlinkDumpInterrupted != 0 {
			return nil, fmt.Errorf("%w: netlink route dump was interrupted", ErrIncompleteHostSnapshot)
		}
		switch message.Header.Type {
		case syscall.RTM_NEWROUTE:
			if done {
				return nil, fmt.Errorf("%w: route follows netlink completion", ErrIncompleteHostSnapshot)
			}
			prefix, err := linuxRoutePrefix(message)
			if err != nil {
				return nil, err
			}
			routes = append(routes, prefix)
		case syscall.NLMSG_DONE:
			if done || index != len(messages)-1 {
				return nil, fmt.Errorf("%w: invalid netlink completion", ErrIncompleteHostSnapshot)
			}
			done = true
		default:
			return nil, fmt.Errorf("%w: unexpected netlink message type %d", ErrIncompleteHostSnapshot, message.Header.Type)
		}
	}
	if consumed != len(rib) || !done {
		return nil, fmt.Errorf("%w: truncated netlink route dump", ErrIncompleteHostSnapshot)
	}
	sortPrefixes(routes)
	return routes, nil
}

func linuxRoutePrefix(message syscall.NetlinkMessage) (netip.Prefix, error) {
	if len(message.Data) < syscall.SizeofRtMsg {
		return netip.Prefix{}, fmt.Errorf("%w: truncated netlink route", ErrIncompleteHostSnapshot)
	}
	if message.Data[0] != syscall.AF_INET {
		return netip.Prefix{}, fmt.Errorf("%w: non-IPv4 route in IPv4 dump", ErrIncompleteHostSnapshot)
	}
	prefixBits := int(message.Data[1])
	if prefixBits < 0 || prefixBits > 32 {
		return netip.Prefix{}, fmt.Errorf("%w: invalid IPv4 route prefix length %d", ErrIncompleteHostSnapshot, prefixBits)
	}
	attributes, err := syscall.ParseNetlinkRouteAttr(&message)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: parse netlink route attributes: %v", ErrIncompleteHostSnapshot, err)
	}
	attributeBytes := 0
	foundDestination := false
	var destination [4]byte
	for _, attribute := range attributes {
		attributeBytes += netlinkAlignedLength(int(attribute.Attr.Len))
		if attribute.Attr.Type&0x3fff != syscall.RTA_DST {
			continue
		}
		if foundDestination || len(attribute.Value) != len(destination) {
			return netip.Prefix{}, fmt.Errorf("%w: invalid IPv4 route destination", ErrIncompleteHostSnapshot)
		}
		copy(destination[:], attribute.Value)
		foundDestination = true
	}
	if attributeBytes != len(message.Data)-syscall.SizeofRtMsg {
		return netip.Prefix{}, fmt.Errorf("%w: truncated netlink route attributes", ErrIncompleteHostSnapshot)
	}
	if prefixBits > 0 && !foundDestination {
		return netip.Prefix{}, fmt.Errorf("%w: IPv4 route has no destination", ErrIncompleteHostSnapshot)
	}
	prefix := netip.PrefixFrom(netip.AddrFrom4(destination), prefixBits)
	if prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("%w: non-canonical IPv4 route %s", ErrIncompleteHostSnapshot, prefix)
	}
	return prefix, nil
}

func netlinkAlignedLength(length int) int {
	return (length + syscall.NLMSG_ALIGNTO - 1) & ^(syscall.NLMSG_ALIGNTO - 1)
}
