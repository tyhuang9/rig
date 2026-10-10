package generatedingress

import (
	"context"
	"reflect"
)

// The optional driver stops only the exact protected final container. Success
// requires fresh exact-ID and listener absence evidence, including when SQL is
// unreadable or ordinary admission is latched. It never authorizes serving.
type gatewayCurrentOwnedStopDriver interface {
	stopGatewayCurrentOwnedTarget(context.Context, gatewayCurrentOwnedStopTarget) error
}

// The caller holds the effects lease and both gateway locks. This state-only
// capability deliberately does not synthesize an ordinary physical transition
// for batch recovery, and does not consult SQL for withdrawal authority.
func (m *Manager) stopGatewayCurrentOwnedTargetLocked(ctx context.Context,
	target gatewayCurrentOwnedStopTarget,
) error {
	invalid := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	if m == nil || ctx == nil || ctx.Err() != nil || target.Transition != nil ||
		!validGatewayCurrentOwnedStopTarget(target) {
		return invalid
	}
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentOwnedStopDriver)
	if !ok {
		return invalid
	}
	before, err := m.revalidateGatewayCurrentOwnedStopTargetLocked(target)
	if err != nil || !reflect.DeepEqual(before, target) || ctx.Err() != nil {
		return invalid
	}
	if err := driver.stopGatewayCurrentOwnedTarget(ctx, target); err != nil {
		return invalid
	}
	after, err := m.revalidateGatewayCurrentOwnedStopTargetLocked(target)
	if err != nil || !reflect.DeepEqual(after, target) || ctx.Err() != nil {
		return invalid
	}
	return nil
}
