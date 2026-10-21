// Package generatedmigration runs one approved deployment migration through
// the deployments repository's sealed command authority. It owns no durable
// result or retry policy.
package generatedmigration

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/hostd/hostd/internal/deployments"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	diagnosticValidationFailed   = "validation_failed"
	diagnosticCreateFailed       = "migration_create_failed"
	diagnosticStartFailed        = "migration_start_failed"
	diagnosticMigrationFailed    = "migration_failed"
	diagnosticCleanupFailed      = "migration_cleanup_failed"
	diagnosticCancelled          = "cancelled"
	diagnosticTimeout            = "migration_timeout"
	diagnosticTerminationFailed  = "process_termination_failed"
	diagnosticRuntimeUnavailable = "runtime_unavailable"
)

type Runner struct {
	repository *deployments.Repository
}

// Error exposes only a finite diagnostic code. Its wrapped cause remains
// available to errors.Is/errors.As without being copied into Error's message.
type Error struct {
	Code  string
	cause error
}

func (e *Error) Error() string { return "generated migration: " + e.Code }

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func IsCode(err error, code string) bool {
	if err == nil {
		return false
	}
	if typed, ok := err.(*Error); ok && typed.Code == code {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if IsCode(cause, code) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return IsCode(wrapped.Unwrap(), code)
	}
	return false
}

func New(repository *deployments.Repository) (*Runner, error) {
	if repository == nil {
		return nil, errors.New("generated migration repository is required")
	}
	return &Runner{repository: repository}, nil
}

func (r *Runner) Run(ctx context.Context, invocation deployments.GeneratedMigrationInvocation) error {
	if r == nil || r.repository == nil || ctx == nil || invocation == nil {
		return migrationFailure(ctx, diagnosticValidationFailed, nil)
	}
	command, err := r.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		return migrationFailure(ctx, diagnosticValidationFailed, err)
	}
	return runCommand(ctx, command)
}

type migrationCommand interface {
	CreateAndInspect() error
	StartAndInspect() error
	WaitAndInspectExited() (int, error)
	RemoveAndConfirm() error
	Leave()
}

func runCommand(ctx context.Context, command migrationCommand) error {
	if command == nil {
		return migrationFailure(ctx, diagnosticValidationFailed, nil)
	}
	defer command.Leave()

	if err := command.CreateAndInspect(); err != nil {
		return finishWithCleanup(ctx, migrationFailure(ctx, diagnosticCreateFailed, err), command)
	}
	if err := command.StartAndInspect(); err != nil {
		return finishWithCleanup(ctx, migrationFailure(ctx, diagnosticStartFailed, err), command)
	}
	exitCode, err := command.WaitAndInspectExited()
	if err != nil {
		return finishWithCleanup(ctx, migrationFailure(ctx, diagnosticMigrationFailed, err), command)
	}
	var primary error
	if exitCode != 0 {
		primary = migrationFailure(ctx, diagnosticMigrationFailed, nil)
	}
	return finishWithCleanup(ctx, primary, command)
}

func finishWithCleanup(ctx context.Context, primary error, command migrationCommand) error {
	cleanupErr := command.RemoveAndConfirm()
	result := primary
	if cleanupErr != nil {
		// The command handle owns a separate bounded cleanup context. Classify
		// its failure from its own cause before rechecking the caller context.
		result = errors.Join(result, migrationFailure(nil, diagnosticCleanupFailed, cleanupErr))
	}
	if ctx == nil || ctx.Err() == nil {
		return result
	}
	// Cancellation can race with a successful cleanup. Recheck only after the
	// single cleanup attempt and retain any earlier phase or cleanup failure as
	// the cause of the final finite cancellation/timeout boundary.
	return migrationFailure(ctx, diagnosticMigrationFailed, result)
}

func migrationFailure(ctx context.Context, fallback string, cause error) error {
	code := fallback
	contextCause := contextFailure(ctx, cause)
	switch {
	case errors.Is(contextCause, context.Canceled):
		code = diagnosticCancelled
	case errors.Is(contextCause, context.DeadlineExceeded):
		code = diagnosticTimeout
	case errors.Is(contextCause, runtimeprocess.ErrTerminationFailed):
		code = diagnosticTerminationFailed
	default:
		var executableError *exec.Error
		var pathError *os.PathError
		if errors.As(contextCause, &executableError) || errors.As(contextCause, &pathError) {
			code = diagnosticRuntimeUnavailable
		}
	}
	return &Error{Code: code, cause: contextCause}
}

func contextFailure(ctx context.Context, cause error) error {
	if ctx == nil || ctx.Err() == nil {
		return cause
	}
	return errors.Join(cause, ctx.Err(), context.Cause(ctx))
}
