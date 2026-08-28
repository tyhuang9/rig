package process

import (
	"bytes"
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
	case "sleep":
		time.Sleep(10 * time.Second)
	case "outputsleep":
		fmt.Fprint(os.Stdout, "collector-secret")
		time.Sleep(10 * time.Second)
	case "resistant-tree":
		runResistantTreeHelper()
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
	if errors.Is(err, ErrTerminationFailed) {
		t.Fatalf("successful descendant termination reported failure: %v", err)
	}
	time.Sleep(1800 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant escaped process-tree cancellation: %v", err)
	}
}

func TestExecRunnerPreservesGracefulTerminationFailure(t *testing.T) {
	gracefulFailure := errors.New("graceful signal failed")
	runner := ExecRunner{
		stop: func(command *exec.Cmd, handle any) error {
			if err := killProcessTree(command, handle); err != nil {
				t.Fatalf("kill process tree: %v", err)
			}
			return gracefulFailure
		},
		gracePeriod: 100 * time.Millisecond,
		reapPeriod:  100 * time.Millisecond,
	}
	_, err := runner.Run(context.Background(), sleepingCommandRequest(t, 20*time.Millisecond))
	assertTerminationFailure(t, err, gracefulFailure, nil, false, context.DeadlineExceeded)
}

func TestExecRunnerPreservesHardKillFailure(t *testing.T) {
	hardKillFailure := errors.New("hard kill failed")
	runner := ExecRunner{
		stop: func(*exec.Cmd, any) error { return nil },
		hardKill: func(command *exec.Cmd, handle any) error {
			if err := killProcessTree(command, handle); err != nil {
				t.Fatalf("kill process tree: %v", err)
			}
			return hardKillFailure
		},
		gracePeriod: 10 * time.Millisecond,
		reapPeriod:  100 * time.Millisecond,
	}
	_, err := runner.Run(context.Background(), sleepingCommandRequest(t, 20*time.Millisecond))
	assertTerminationFailure(t, err, nil, hardKillFailure, false, context.DeadlineExceeded)
}

func TestExecRunnerBoundsReapAfterHardKill(t *testing.T) {
	releaseWait := make(chan struct{})
	runner := ExecRunner{
		stop: func(*exec.Cmd, any) error { return nil },
		hardKill: func(command *exec.Cmd, handle any) error {
			return killProcessTree(command, handle)
		},
		wait: func(command *exec.Cmd) error {
			<-releaseWait
			return command.Wait()
		},
		gracePeriod: 10 * time.Millisecond,
		reapPeriod:  10 * time.Millisecond,
	}
	started := time.Now()
	result, err := runner.Run(context.Background(), sleepingCommandRequest(t, 20*time.Millisecond))
	elapsed := time.Since(started)
	close(releaseWait)
	assertTerminationFailure(t, err, nil, nil, true, context.DeadlineExceeded)
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("unreaped command returned collector-owned output: %#v", result)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("reap wait was not bounded: %s", elapsed)
	}
}

func TestExecRunnerClearsOutputAfterReapedTerminationFailure(t *testing.T) {
	gracefulFailure := errors.New("graceful signal failed")
	var captured, backing []byte
	runner := ExecRunner{
		stop: func(command *exec.Cmd, handle any) error {
			if err := killProcessTree(command, handle); err != nil {
				t.Fatalf("kill process tree: %v", err)
			}
			return gracefulFailure
		},
		beforeClear: func(stdout, _ *boundedBuffer) {
			captured = append([]byte(nil), stdout.buffer.Bytes()...)
			backing = stdout.buffer.Bytes()
		},
		gracePeriod: 100 * time.Millisecond,
		reapPeriod:  100 * time.Millisecond,
	}
	_, err := runner.Run(context.Background(), outputSleepingCommandRequest(t, 150*time.Millisecond))
	assertTerminationFailure(t, err, gracefulFailure, nil, false, context.DeadlineExceeded)
	assertClearedSentinelOutput(t, captured, backing)
}

func TestExecRunnerClearsOutputAfterLateReap(t *testing.T) {
	releaseWait := make(chan struct{})
	cleared := make(chan struct{})
	var captured, backing []byte
	runner := ExecRunner{
		stop: func(*exec.Cmd, any) error { return nil },
		hardKill: func(command *exec.Cmd, handle any) error {
			return killProcessTree(command, handle)
		},
		wait: func(command *exec.Cmd) error {
			<-releaseWait
			return command.Wait()
		},
		beforeClear: func(stdout, _ *boundedBuffer) {
			captured = append([]byte(nil), stdout.buffer.Bytes()...)
			backing = stdout.buffer.Bytes()
			close(cleared)
		},
		gracePeriod: 10 * time.Millisecond,
		reapPeriod:  10 * time.Millisecond,
	}
	_, err := runner.Run(context.Background(), outputSleepingCommandRequest(t, 150*time.Millisecond))
	assertTerminationFailure(t, err, nil, nil, true, context.DeadlineExceeded)
	close(releaseWait)
	select {
	case <-cleared:
	case <-time.After(time.Second):
		t.Fatal("late waiter did not clear captured output")
	}
	assertClearedSentinelOutput(t, captured, backing)
}

func TestExecRunnerPropagatesParentCancellationAfterTermination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := (ExecRunner{}).Run(ctx, sleepingCommandRequest(t, time.Second))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestExecRunnerHardKillsTreeAfterLeaderReaped(t *testing.T) {
	probes, hardKills := 0, 0
	runner := ExecRunner{
		stop: func(command *exec.Cmd, _ any) error { return command.Process.Kill() },
		hardKill: func(*exec.Cmd, any) error {
			hardKills++
			return nil
		},
		treeAlive: func(*exec.Cmd, any) (bool, error) {
			probes++
			return hardKills == 0, nil
		},
		gracePeriod: 100 * time.Millisecond,
		reapPeriod:  100 * time.Millisecond,
		treePoll:    time.Millisecond,
	}
	_, err := runner.Run(context.Background(), sleepingCommandRequest(t, 20*time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTerminationFailed) {
		t.Fatalf("termination result = %v", err)
	}
	if hardKills != 1 || probes < 2 {
		t.Fatalf("tree escalation probes=%d hard kills=%d", probes, hardKills)
	}
}

func TestExecRunnerFailsWhenTreeCannotBeVerifiedAfterLeaderReaped(t *testing.T) {
	hardKills := 0
	runner := ExecRunner{
		stop: func(command *exec.Cmd, _ any) error { return command.Process.Kill() },
		hardKill: func(*exec.Cmd, any) error {
			hardKills++
			return nil
		},
		treeAlive:   func(*exec.Cmd, any) (bool, error) { return true, nil },
		gracePeriod: 100 * time.Millisecond,
		reapPeriod:  10 * time.Millisecond,
		treePoll:    time.Millisecond,
	}
	_, err := runner.Run(context.Background(), sleepingCommandRequest(t, 20*time.Millisecond))
	assertTerminationFailure(t, err, nil, nil, true, context.DeadlineExceeded)
	if hardKills != 1 {
		t.Fatalf("hard kills=%d", hardKills)
	}
}

func sleepingCommandRequest(t *testing.T, timeout time.Duration) CommandRequest {
	t.Helper()
	return CommandRequest{
		Executable: os.Args[0], Args: []string{"-test.run=TestCommandHelper"}, Directory: t.TempDir(),
		Env: []string{"HOSTD_PROCESS_HELPER=1", "HOSTD_PROCESS_MODE=sleep"}, Timeout: timeout,
	}
}

func outputSleepingCommandRequest(t *testing.T, timeout time.Duration) CommandRequest {
	t.Helper()
	return CommandRequest{
		Executable: os.Args[0], Args: []string{"-test.run=TestCommandHelper"}, Directory: t.TempDir(),
		Env: []string{"HOSTD_PROCESS_HELPER=1", "HOSTD_PROCESS_MODE=outputsleep"}, Timeout: timeout,
	}
}

func assertClearedSentinelOutput(t *testing.T, captured, backing []byte) {
	t.Helper()
	if !bytes.Contains(captured, []byte("collector-secret")) {
		t.Fatalf("sentinel was not captured before disposal: %q", captured)
	}
	for _, value := range backing {
		if value != 0 {
			t.Fatalf("captured output was not cleared: %q", backing)
		}
	}
}

func assertTerminationFailure(t *testing.T, err, gracefulWant, hardKillWant error, reapTimedOut bool, contextWant error) {
	t.Helper()
	if !errors.Is(err, ErrTerminationFailed) || !errors.Is(err, contextWant) {
		t.Fatalf("termination error = %v", err)
	}
	var terminationError *TerminationError
	if !errors.As(err, &terminationError) {
		t.Fatalf("error does not expose TerminationError: %v", err)
	}
	if !errors.Is(terminationError.GracefulErr, gracefulWant) || !errors.Is(terminationError.HardKillErr, hardKillWant) || terminationError.ReapTimedOut != reapTimedOut {
		t.Fatalf("termination error = %#v", terminationError)
	}
}
