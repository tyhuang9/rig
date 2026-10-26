package generatedingress

import (
	"context"
	"errors"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GeneratedCandidateError exposes only a stable diagnostic. The private cause
// remains available to errors.Is/As without including command output, protected
// configuration, or filesystem details in the user-facing message.
type GeneratedCandidateError struct {
	Code  string
	cause error
}

func (e *GeneratedCandidateError) Error() string {
	if e == nil {
		return "generated candidate: unavailable"
	}
	return "generated candidate: " + e.Code
}
func (e *GeneratedCandidateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func candidateFailure(code string, cause error) error {
	return &GeneratedCandidateError{Code: code, cause: cause}
}

// IsGeneratedRouteAccountingError reports whether err contains one of the
// private accounting failures that can leave an authenticated route completion.
// It exposes no ledger state or authority and leaves the supplied error tree
// unchanged.
func IsGeneratedRouteAccountingError(err error) bool {
	return errors.Is(err, errCandidateCapacityInvalid) ||
		errors.Is(err, errCandidateCapacityClosing) ||
		errors.Is(err, errCandidateCapacityStale) ||
		errors.Is(err, errCandidateCapacityBusy) ||
		errors.Is(err, errCandidateReceiptMismatch) ||
		errors.Is(err, errCandidateMutationUnquiesced) ||
		errors.Is(err, errCandidatePhysicalTransition) ||
		errors.Is(err, errCandidateCommandEntered)
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
