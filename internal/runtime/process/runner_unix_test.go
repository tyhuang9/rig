//go:build !windows

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func runResistantTreeHelper() {
	if os.Getenv("HOSTD_PROCESS_CHILD") == "1" {
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(1500 * time.Millisecond)
		_ = os.WriteFile(os.Getenv("HOSTD_PROCESS_MARKER"), []byte("survived"), 0o600)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=TestCommandHelper")
	command.Env = append(os.Environ(), "HOSTD_PROCESS_CHILD=1")
	if err := command.Start(); err != nil {
		os.Exit(3)
	}
	time.Sleep(10 * time.Second)
}

func TestExecRunnerHardKillsResistantUnixDescendantAfterLeaderExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "resistant-descendant-survived")
	_, err := (ExecRunner{}).Run(context.Background(), CommandRequest{
		Executable: os.Args[0], Args: []string{"-test.run=TestCommandHelper"}, Directory: t.TempDir(),
		Env:     []string{"HOSTD_PROCESS_HELPER=1", "HOSTD_PROCESS_MODE=resistant-tree", "HOSTD_PROCESS_MARKER=" + marker},
		Timeout: 150 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTerminationFailed) {
		t.Fatalf("termination result = %v", err)
	}
	time.Sleep(1800 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resistant descendant escaped group hard kill: %v", err)
	}
}
