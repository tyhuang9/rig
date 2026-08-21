package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const DefaultOutputLimit = 1 << 20

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

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
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
	prepareProcess(command)
	stdout := newBoundedBuffer(limit)
	stderr := newBoundedBuffer(limit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return CommandResult{}, fmt.Errorf("start command: %w", err)
	}
	processHandle, err := attachProcessTree(command)
	if err != nil {
		_ = killProcessTree(command, nil)
		_ = command.Wait()
		return CommandResult{}, fmt.Errorf("protect process tree: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-commandCtx.Done():
		_ = stopProcessTree(command, processHandle)
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			_ = killProcessTree(command, processHandle)
			<-done
		}
		waitErr = commandCtx.Err()
	}
	closeProcessTree(processHandle)
	result := CommandResult{Stdout: stdout.TakeBytes(), Stderr: stderr.TakeBytes(), StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated()}
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
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
func (b *boundedBuffer) Truncated() bool { return b.truncated }
