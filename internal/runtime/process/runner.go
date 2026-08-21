package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

const (
	DefaultOutputLimit = 1 << 20

	terminationGracePeriod = 500 * time.Millisecond
	terminationReapPeriod  = 500 * time.Millisecond
)

// ErrTerminationFailed marks a command that could not be reliably terminated
// and reaped. Callers can use errors.Is without depending on platform errors.
var ErrTerminationFailed = errors.New("process termination failed")

// TerminationError records failures to stop or reap a process tree. Its stable
// message deliberately excludes command arguments and collected output.
type TerminationError struct {
	GracefulErr  error
	HardKillErr  error
	ReapTimedOut bool
}

func (e *TerminationError) Error() string { return ErrTerminationFailed.Error() }

func (e *TerminationError) Is(target error) bool { return target == ErrTerminationFailed }

func (e *TerminationError) Unwrap() []error {
	errors := make([]error, 0, 2)
	if e.GracefulErr != nil {
		errors = append(errors, e.GracefulErr)
	}
	if e.HardKillErr != nil {
		errors = append(errors, e.HardKillErr)
	}
	return errors
}

type CommandRequest struct {
	Executable  string
	Args        []string
	Directory   string
	Env         []string
	Timeout     time.Duration
	OutputLimit int
}

type CommandResult struct {
	// Stdout and Stderr are owned by the caller and may contain secrets. The
	// caller must clear both slices as soon as it has parsed sanitized state.
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
}

type CommandRunner interface {
	Run(context.Context, CommandRequest) (CommandResult, error)
}

type ExecRunner struct {
	attach      func(*exec.Cmd) (any, error)
	stop        func(*exec.Cmd, any) error
	hardKill    func(*exec.Cmd, any) error
	closeTree   func(any)
	wait        func(*exec.Cmd) error
	beforeClear func(*boundedBuffer, *boundedBuffer)
	gracePeriod time.Duration
	reapPeriod  time.Duration
}

type commandOperations struct {
	attach    func(*exec.Cmd) (any, error)
	stop      func(*exec.Cmd, any) error
	hardKill  func(*exec.Cmd, any) error
	closeTree func(any)
	wait      func(*exec.Cmd) error
}

func (r ExecRunner) operations() commandOperations {
	operations := commandOperations{
		attach:    attachProcessTree,
		stop:      stopProcessTree,
		hardKill:  killProcessTree,
		closeTree: closeProcessTree,
		wait:      func(command *exec.Cmd) error { return command.Wait() },
	}
	if r.attach != nil {
		operations.attach = r.attach
	}
	if r.stop != nil {
		operations.stop = r.stop
	}
	if r.hardKill != nil {
		operations.hardKill = r.hardKill
	}
	if r.closeTree != nil {
		operations.closeTree = r.closeTree
	}
	if r.wait != nil {
		operations.wait = r.wait
	}
	return operations
}

func (r ExecRunner) terminationPeriods() (time.Duration, time.Duration) {
	grace, reap := r.gracePeriod, r.reapPeriod
	if grace <= 0 {
		grace = terminationGracePeriod
	}
	if reap <= 0 {
		reap = terminationReapPeriod
	}
	return grace, reap
}

func (r ExecRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	if request.Executable == "" || request.Directory == "" || request.Timeout <= 0 {
		return CommandResult{}, errors.New("invalid command request")
	}
	limit := request.OutputLimit
	if limit <= 0 || limit > DefaultOutputLimit {
		limit = DefaultOutputLimit
	}
	commandCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	command := exec.Command(request.Executable, request.Args...)
	command.Dir = request.Directory
	command.Env = append([]string(nil), request.Env...)
	operations := r.operations()
	prepareProcess(command)
	stdout := newBoundedBuffer(limit)
	stderr := newBoundedBuffer(limit)
	outputs := outputOwnership{stdout: stdout, stderr: stderr, beforeClear: r.beforeClear}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return CommandResult{}, fmt.Errorf("start command: %w", err)
	}
	processHandle, err := operations.attach(command)
	if err != nil {
		done := make(chan error, 1)
		go waitForCommand(done, command, operations.wait, &outputs)
		_, reapFailed := terminate(command, nil, done, operations, terminationGracePeriod, terminationReapPeriod)
		outputs.discard()
		if reapFailed != nil {
			return CommandResult{}, errors.Join(fmt.Errorf("protect process tree: %w", err), reapFailed)
		}
		return CommandResult{}, fmt.Errorf("protect process tree: %w", err)
	}
	defer operations.closeTree(processHandle)
	done := make(chan error, 1)
	go waitForCommand(done, command, operations.wait, &outputs)
	var waitErr error
	select {
	case waitErr = <-done:
	case <-commandCtx.Done():
		// Prefer a completed command if it raced cancellation so an exited
		// process is never reported as a failed termination.
		select {
		case waitErr = <-done:
		default:
			grace, reap := r.terminationPeriods()
			if _, terminationErr := terminate(command, processHandle, done, operations, grace, reap); terminationErr != nil {
				// Wait has not necessarily finished, so the output buffers remain
				// collector-owned and are not read concurrently or returned.
				outputs.discard()
				return CommandResult{}, errors.Join(commandCtx.Err(), terminationErr)
			}
			waitErr = commandCtx.Err()
		}
	}
	result := CommandResult{Stdout: stdout.TakeBytes(), Stderr: stderr.TakeBytes(), StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated()}
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
}

func waitForCommand(done chan<- error, command *exec.Cmd, wait func(*exec.Cmd) error, outputs *outputOwnership) {
	err := wait(command)
	outputs.complete()
	done <- err
}

// outputOwnership serializes disposal with the sole command waiter. command.Wait
// completes after os/exec's output collectors, so clearing here cannot race a
// writer. A caller that returns before Wait only marks the buffers for later
// disposal; it neither reads nor clears them concurrently.
type outputOwnership struct {
	mu          sync.Mutex
	stdout      *boundedBuffer
	stderr      *boundedBuffer
	beforeClear func(*boundedBuffer, *boundedBuffer)
	completed   bool
	discarded   bool
}

func (o *outputOwnership) complete() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.completed = true
	if o.discarded {
		o.clearLocked()
	}
}

func (o *outputOwnership) discard() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.discarded = true
	if o.completed {
		o.clearLocked()
	}
}

func (o *outputOwnership) clearLocked() {
	if o.beforeClear != nil {
		o.beforeClear(o.stdout, o.stderr)
	}
	o.stdout.Clear()
	o.stderr.Clear()
}

// terminate waits for the process collector after each escalation step. A
// buffered done channel ensures a late waiter can always finish without being
// blocked by a caller that has already returned.
func terminate(command *exec.Cmd, handle any, done <-chan error, operations commandOperations, grace, reap time.Duration) (error, *TerminationError) {
	gracefulErr := operations.stop(command, handle)
	if waitErr, completed := waitForDone(done, grace); completed {
		return waitErr, terminationFailure(gracefulErr, nil, false)
	}
	hardKillErr := operations.hardKill(command, handle)
	if waitErr, completed := waitForDone(done, reap); completed {
		return waitErr, terminationFailure(gracefulErr, hardKillErr, false)
	}
	return nil, terminationFailure(gracefulErr, hardKillErr, true)
}

func waitForDone(done <-chan error, period time.Duration) (error, bool) {
	timer := time.NewTimer(period)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

func terminationFailure(gracefulErr, hardKillErr error, reapTimedOut bool) *TerminationError {
	if gracefulErr == nil && hardKillErr == nil && !reapTimedOut {
		return nil
	}
	return &TerminationError{GracefulErr: gracefulErr, HardKillErr: hardKillErr, ReapTimedOut: reapTimedOut}
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{remaining: limit} }

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if len(value) > b.remaining {
		value = value[:b.remaining]
		b.truncated = true
	}
	if len(value) > 0 {
		_, _ = b.buffer.Write(value)
		b.remaining -= len(value)
	}
	return original, nil
}

func (b *boundedBuffer) TakeBytes() []byte {
	value := b.buffer.Bytes()
	b.buffer = bytes.Buffer{}
	return value
}

func (b *boundedBuffer) Clear() {
	clear(b.buffer.Bytes())
	b.buffer.Reset()
}

func (b *boundedBuffer) Truncated() bool { return b.truncated }
