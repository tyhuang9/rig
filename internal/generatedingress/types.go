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

// Error carries only an audit-safe diagnostic code. Docker and Caddy output is
// deliberately discarded inside the package.
type Error struct{ Code DiagnosticCode }

func (e *Error) Error() string { return "generated ingress: " + string(e.Code) }

func IsCode(err error, code DiagnosticCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
