package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandHelper(t *testing.T) {
	if os.Getenv("HOSTD_PROCESS_HELPER") != "1" {
		return
	}
	switch os.Getenv("HOSTD_PROCESS_MODE") {
	case "output":
		fmt.Fprintf(os.Stdout, "only=%s|inherited=%s|", os.Getenv("ONLY"), os.Getenv("HOSTD_PARENT_SECRET"))
		fmt.Fprint(os.Stdout, strings.Repeat("o", 128))
		fmt.Fprint(os.Stderr, strings.Repeat("e", 128))
	case "tree":
		if os.Getenv("HOSTD_PROCESS_CHILD") == "1" {
			time.Sleep(1500 * time.Millisecond)
			_ = os.WriteFile(os.Getenv("HOSTD_PROCESS_MARKER"), []byte("survived"), 0o600)
			os.Exit(0)
		}
		command := exec.Command(os.Args[0], "-test.run=TestCommandHelper")
		command.Env = append(os.Environ(), "HOSTD_PROCESS_CHILD=1")
		if err := command.Start(); err != nil {
			os.Exit(3)
		}
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func TestExecRunnerUsesExactEnvAndBoundsOutput(t *testing.T) {
	t.Setenv("HOSTD_PARENT_SECRET", "must-not-inherit")
	result, err := (ExecRunner{}).Run(context.Background(), CommandRequest{
		Executable: os.Args[0], Args: []string{"-test.run=TestCommandHelper"}, Directory: t.TempDir(),
		Env:     []string{"HOSTD_PROCESS_HELPER=1", "HOSTD_PROCESS_MODE=output", "ONLY=scoped"},
		Timeout: 5 * time.Second, OutputLimit: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if len(result.Stdout) != 64 || len(result.Stderr) != 64 || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("bounded result = stdout %d/%t stderr %d/%t", len(result.Stdout), result.StdoutTruncated, len(result.Stderr), result.StderrTruncated)
	}
	if strings.Contains(string(result.Stdout), "must-not-inherit") {
		t.Fatal("runner inherited parent environment")
	}
}

func TestExecRunnerCancelsDescendantProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	_, err := (ExecRunner{}).Run(context.Background(), CommandRequest{
		Executable: os.Args[0], Args: []string{"-test.run=TestCommandHelper"}, Directory: t.TempDir(),
		Env:     []string{"HOSTD_PROCESS_HELPER=1", "HOSTD_PROCESS_MODE=tree", "HOSTD_PROCESS_MARKER=" + marker},
		Timeout: 150 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error = %v", err)
	}
	time.Sleep(1800 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant escaped process-tree cancellation: %v", err)
	}
}
