package deployments

// ErrGeneratedCandidateRetirementUnavailable preserves receipt-owned resources
// until the bound retirement owner can authorize both removal and accounting.
var ErrGeneratedCandidateRetirementUnavailable = &GeneratedCandidateError{Code: "retirement_unavailable"}

// GeneratedCandidateError remains the source-compatible legacy diagnostic for
// the read-only retirement barrier retained in deployments.
type GeneratedCandidateError struct {
	Code  string
	cause error
}

func (e *GeneratedCandidateError) Error() string { return "generated candidate: " + e.Code }
func (e *GeneratedCandidateError) Unwrap() error { return e.cause }
