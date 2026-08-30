package main

import (
	"errors"
	"strings"
)

type hostdMode uint8

const (
	hostdModeUI hostdMode = iota
	hostdModeServe
)

type hostdInvocation struct {
	mode             hostdMode
	args             []string
	legacyServerArgs bool
}

func classifyHostdInvocation(args []string) (hostdInvocation, error) {
	if len(args) == 0 {
		return hostdInvocation{mode: hostdModeUI}, nil
	}

	switch args[0] {
	case "ui":
		return hostdInvocation{mode: hostdModeUI, args: append([]string(nil), args[1:]...)}, nil
	case "serve":
		return hostdInvocation{mode: hostdModeServe, args: append([]string(nil), args[1:]...)}, nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return hostdInvocation{
				mode:             hostdModeServe,
				args:             append([]string(nil), args...),
				legacyServerArgs: true,
			}, nil
		}
		return hostdInvocation{}, errors.New("unknown hostd command; use hostd, hostd ui, or hostd serve")
	}
}
