//go:build !windows && !linux

package docker

import "errors"

func collectHostResources(string) (HostResources, error) {
	return HostResources{}, errors.New("host resource collection is unsupported on this operating system")
}
