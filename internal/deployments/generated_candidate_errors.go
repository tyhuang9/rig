package deployments

import (
	"context"
	"errors"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// ErrGeneratedCandidateRetirementUnavailable preserves receipt-owned resources
// until the bound retirement owner can authorize both removal and accounting.
var ErrGeneratedCandidateRetirementUnavailable = &GeneratedCandidateError{Code: "retirement_unavailable"}

// GeneratedCandidateError exposes only a stable diagnostic. The private cause
// remains available to errors.Is/As without including command output, protected
// configuration, or filesystem details in the user-facing message.
type GeneratedCandidateError struct {
	Code  string
	cause error
}

func (e *GeneratedCandidateError) Error() string { return "generated candidate: " + e.Code }
func (e *GeneratedCandidateError) Unwrap() error { return e.cause }

func candidateFailure(code string, cause error) error {
	return &GeneratedCandidateError{Code: code, cause: cause}
}

// candidateProcessFailure never embeds a runner's text in the diagnostic.
// The caller separately latches mutation uncertainty before returning it.
func candidateProcessFailure(ctx context.Context, result runtimeprocess.CommandResult, runErr error, fallback string) error {
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		return candidateFailure("process_termination_failed", runErr)
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(contextError(ctx), context.Canceled) {
		return candidateFailure("cancelled", errors.Join(runErr, contextError(ctx)))
	}
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(contextError(ctx), context.DeadlineExceeded) {
		return candidateFailure("runtime_timeout", errors.Join(runErr, contextError(ctx)))
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return candidateFailure("runtime_output_truncated", runErr)
	}
	if runErr != nil {
		return candidateFailure(fallback, runErr)
	}
	return nil
}
