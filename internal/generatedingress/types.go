// Package generatedingress owns the controller-managed Caddy boundary used by
// generated runtimes. It never receives application commands or configuration.
package generatedingress

import "errors"

type DiagnosticCode string

const (
	DiagnosticValidationFailed    DiagnosticCode = "validation_failed"
	DiagnosticIngressUnavailable  DiagnosticCode = "ingress_unavailable"
	DiagnosticIngressDrift        DiagnosticCode = "ingress_drift_detected"
	DiagnosticRouteInvalid        DiagnosticCode = "route_invalid"
	DiagnosticRouteValidateFailed DiagnosticCode = "route_validate_failed"
	DiagnosticRouteReloadFailed   DiagnosticCode = "route_reload_failed"
	DiagnosticRouteStateFailed    DiagnosticCode = "route_state_failed"
	DiagnosticRouteUnresolved     DiagnosticCode = "route_reconciliation_required"
	DiagnosticCancelled           DiagnosticCode = "cancelled"
)

// Error carries only an audit-safe diagnostic code and route safety outcome.
// Docker and Caddy output is deliberately discarded inside the package.
type Error struct {
	Code               DiagnosticCode
	candidateMayBeLive bool
}

func (e *Error) Error() string { return "generated ingress: " + string(e.Code) }

// CandidateMayBeLive tells the deployment coordinator that failed
// reconciliation could not prove the candidate is no longer serving traffic.
func (e *Error) CandidateMayBeLive() bool { return e != nil && e.candidateMayBeLive }

func IsCode(err error, code DiagnosticCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

func candidateMayBeLiveError() *Error {
	return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
}

func markCandidateMayBeLive(err error) *Error {
	var target *Error
	if errors.As(err, &target) {
		return &Error{Code: target.Code, candidateMayBeLive: true}
	}
	return candidateMayBeLiveError()
}
