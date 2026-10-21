package generatedmigration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/runtime/process"
)

type fakeCommand struct {
	calls        []string
	createErr    error
	startErr     error
	waitErr      error
	cleanupErr   error
	exitCode     int
	afterCleanup func()
}

func (f *fakeCommand) CreateAndInspect() error {
	f.calls = append(f.calls, "create")
	return f.createErr
}

func (f *fakeCommand) StartAndInspect() error {
	f.calls = append(f.calls, "start")
	return f.startErr
}

func (f *fakeCommand) WaitAndInspectExited() (int, error) {
	f.calls = append(f.calls, "wait")
	return f.exitCode, f.waitErr
}

func (f *fakeCommand) RemoveAndConfirm() error {
	f.calls = append(f.calls, "remove")
	if f.afterCleanup != nil {
		f.afterCleanup()
	}
	return f.cleanupErr
}

func (f *fakeCommand) Leave() { f.calls = append(f.calls, "leave") }

func TestRunnerRejectsMissingAuthority(t *testing.T) {
	if runner, err := New(nil); runner != nil || err == nil {
		t.Fatalf("nil repository runner=%#v error=%v", runner, err)
	}
	runner, err := New(deployments.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), nil); !IsCode(err, diagnosticValidationFailed) {
		t.Fatalf("nil invocation error=%v", err)
	}
	var zero *Runner
	if err := zero.Run(context.Background(), nil); !IsCode(err, diagnosticValidationFailed) {
		t.Fatalf("nil runner error=%v", err)
	}
	if err := runner.Run(nil, nil); !IsCode(err, diagnosticValidationFailed) {
		t.Fatalf("nil context error=%v", err)
	}
}

func TestRunCommandUsesFixedOrderAndLeavesHandle(t *testing.T) {
	command := &fakeCommand{}
	if err := runCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "start", "wait", "remove", "leave"}
	if !reflect.DeepEqual(command.calls, want) {
		t.Fatalf("calls=%v want=%v", command.calls, want)
	}
}

func TestRunCommandMapsAttestedNonzeroExitAfterCleanup(t *testing.T) {
	command := &fakeCommand{exitCode: 23}
	err := runCommand(context.Background(), command)
	if !IsCode(err, diagnosticMigrationFailed) {
		t.Fatalf("nonzero exit error=%v", err)
	}
	want := []string{"create", "start", "wait", "remove", "leave"}
	if !reflect.DeepEqual(command.calls, want) {
		t.Fatalf("calls=%v want=%v", command.calls, want)
	}
}

func TestRunCommandNeverReplaysAndJoinsCleanupFailure(t *testing.T) {
	primary := errors.New("private start failure")
	cleanup := errors.New("private cleanup failure")
	command := &fakeCommand{startErr: primary, cleanupErr: cleanup}
	err := runCommand(context.Background(), command)
	if !IsCode(err, diagnosticStartFailed) || !IsCode(err, diagnosticCleanupFailed) {
		t.Fatalf("joined error=%v", err)
	}
	if !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("causes were not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("private process error escaped: %v", err)
	}
	want := []string{"create", "start", "remove", "leave"}
	if !reflect.DeepEqual(command.calls, want) {
		t.Fatalf("calls=%v want=%v", command.calls, want)
	}
}

func TestRunCommandAttemptsCleanupAfterUncertainCreate(t *testing.T) {
	create := errors.New("create acknowledgement uncertain")
	command := &fakeCommand{createErr: create}
	err := runCommand(context.Background(), command)
	if !IsCode(err, diagnosticCreateFailed) || !errors.Is(err, create) {
		t.Fatalf("create error=%v", err)
	}
	want := []string{"create", "remove", "leave"}
	if !reflect.DeepEqual(command.calls, want) {
		t.Fatalf("calls=%v want=%v", command.calls, want)
	}
}

func TestRunCommandPreservesPrimaryWhenCleanupCancelsContext(t *testing.T) {
	privatePrimary := errors.New("private start failure")
	privateCancellation := errors.New("private cancellation cause")
	ctx, cancel := context.WithCancelCause(context.Background())
	command := &fakeCommand{
		startErr: privatePrimary,
		afterCleanup: func() {
			cancel(privateCancellation)
		},
	}
	err := runCommand(ctx, command)
	if !IsCode(err, diagnosticCancelled) || !IsCode(err, diagnosticStartFailed) {
		t.Fatalf("canceled primary error=%v", err)
	}
	for _, cause := range []error{privatePrimary, privateCancellation, context.Canceled} {
		if !errors.Is(err, cause) {
			t.Fatalf("missing cause %v from %v", cause, err)
		}
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("private cause escaped: %v", err)
	}
	want := []string{"create", "start", "remove", "leave"}
	if !reflect.DeepEqual(command.calls, want) {
		t.Fatalf("calls=%v want=%v", command.calls, want)
	}
}

func TestRunCommandReturnsCancellationWhenSuccessfulCleanupCancelsContext(t *testing.T) {
	privateCancellation := errors.New("private cancellation cause")
	ctx, cancel := context.WithCancelCause(context.Background())
	command := &fakeCommand{afterCleanup: func() { cancel(privateCancellation) }}
	err := runCommand(ctx, command)
	if !IsCode(err, diagnosticCancelled) || !errors.Is(err, context.Canceled) || !errors.Is(err, privateCancellation) {
		t.Fatalf("cleanup cancellation error=%v", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("private cancellation cause escaped: %v", err)
	}
	want := []string{"create", "start", "wait", "remove", "leave"}
	if !reflect.DeepEqual(command.calls, want) {
		t.Fatalf("calls=%v want=%v", command.calls, want)
	}
}

func TestMigrationFailureClassifiesDeadlineFromJoinedContextCause(t *testing.T) {
	privateDeadline := errors.New("private deadline cause")
	ctx, cancel := context.WithDeadlineCause(context.Background(), time.Unix(0, 0), privateDeadline)
	defer cancel()
	err := migrationFailure(ctx, diagnosticMigrationFailed, nil)
	if !IsCode(err, diagnosticTimeout) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, privateDeadline) {
		t.Fatalf("deadline error=%v", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("private deadline cause escaped: %v", err)
	}
}

func TestRunCommandPreservesFiniteContextAndTerminationDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "cancelled", err: context.Canceled, code: diagnosticCancelled},
		{name: "timeout", err: context.DeadlineExceeded, code: diagnosticTimeout},
		{name: "termination", err: process.ErrTerminationFailed, code: diagnosticTerminationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := &fakeCommand{waitErr: test.err}
			err := runCommand(context.Background(), command)
			if !IsCode(err, test.code) || !errors.Is(err, test.err) {
				t.Fatalf("error=%v", err)
			}
			want := []string{"create", "start", "wait", "remove", "leave"}
			if !reflect.DeepEqual(command.calls, want) {
				t.Fatalf("calls=%v want=%v", command.calls, want)
			}
		})
	}
}
