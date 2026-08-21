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

func processTreeAlive(command *exec.Cmd, _ any) (bool, error) {
	if command.Process == nil {
		return false, nil
	}
	err := syscall.Kill(-command.Process.Pid, 0)
	if err == nil || err == syscall.EPERM {
		return true, nil
	}
	if err == syscall.ESRCH {
		return false, nil
	}
	return false, err
}

func closeProcessTree(any) {}
