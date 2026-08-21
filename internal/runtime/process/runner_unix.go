//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

func prepareProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachProcessTree(*exec.Cmd) (any, error) { return nil, nil }

func stopProcessTree(command *exec.Cmd, _ any) error {
	if command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
}

func killProcessTree(command *exec.Cmd, _ any) error {
	if command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

func closeProcessTree(any) {}
