//go:build windows

package process

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareProcess(command *exec.Cmd) {
	command.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func attachProcessTree(command *exec.Cmd) (any, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return job, nil
}

func stopProcessTree(command *exec.Cmd, handle any) error {
	return killProcessTree(command, handle)
}

func killProcessTree(command *exec.Cmd, handle any) error {
	if job, ok := handle.(windows.Handle); ok {
		return windows.TerminateJobObject(job, 1)
	}
	if command.Process != nil {
		return command.Process.Kill()
	}
	return nil
}

// Job Object termination owns the whole tree on Windows. Command wait is the
// supported completion signal, so no additional Unix-style group probe runs.
func processTreeAlive(*exec.Cmd, any) (bool, error) { return false, nil }

func closeProcessTree(handle any) {
	if job, ok := handle.(windows.Handle); ok {
		_ = windows.CloseHandle(job)
	}
}
