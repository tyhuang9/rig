package generatedingress

import (
	"context"
	"sync/atomic"
)

var managerAcquireGatewayOSLock = acquireGatewayOSLock

// gatewayRebindProcessFailStop is deliberately process scoped. Once final SQL
// release has committed but a required local lock or lease cannot be proved
// released, constructing another Manager in the same process is not recovery.
// Only a fresh process may reacquire the OS resources and reattest current
// state.
var gatewayRebindProcessFailStop atomic.Bool

func (m *Manager) gatewayRebindFailStopLatch() *atomic.Bool {
	if m != nil && m.gatewayRebindFailStop != nil {
		return m.gatewayRebindFailStop
	}
	return &gatewayRebindProcessFailStop
}

// lockGateway serializes the entire observation or mutation across Manager
// instances sharing one data root. The local mutex is acquired first so a
// single Manager cannot race its own callbacks while waiting for the OS lock.
func (m *Manager) lockGateway(ctx context.Context) (func() error, error) {
	release, err := m.lockGatewayRaw(ctx)
	if err != nil {
		return nil, err
	}
	if m.options.RebindFenceCheck == nil {
		_ = release()
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	if err := m.options.RebindFenceCheck(ctx); err != nil {
		_ = release()
		if ctx.Err() != nil {
			return nil, &Error{Code: DiagnosticCancelled}
		}
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	if ctx.Err() != nil {
		_ = release()
		return nil, &Error{Code: DiagnosticCancelled}
	}
	return release, nil
}

// lockGatewayRaw is reserved for the journal-bound emergency-stop path,
// read-only rebind attestors, and the private prepared-claim rebind network
// stage that holds the deployment-effects lease and freshly proves the SQL
// fence under this lock. All ordinary observation and mutation must call
// lockGateway so SQLite can fence an in-progress LAN gateway rebind before any
// Docker inspection or effect.
func (m *Manager) lockGatewayRaw(ctx context.Context) (func() error, error) {
	return m.lockGatewayRawMode(ctx, false)
}

// lockGatewayRawForInspection permits the one read-only phase/current
// inspection to report a latched fail-stop. It must never be used by an effect
// or by ordinary route authorization.
func (m *Manager) lockGatewayRawForInspection(ctx context.Context) (func() error, error) {
	return m.lockGatewayRawMode(ctx, true)
}

func (m *Manager) lockGatewayRawMode(ctx context.Context, allowFailStop bool) (func() error, error) {
	if !allowFailStop && m.gatewayRebindFailStopLatch().Load() {
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	if err := m.mu.LockContext(ctx); err != nil {
		return nil, &Error{Code: DiagnosticCancelled}
	}
	releaseOS, err := managerAcquireGatewayOSLock(ctx, m.store)
	if err != nil {
		m.mu.Unlock()
		if ctx.Err() != nil {
			return nil, &Error{Code: DiagnosticCancelled}
		}
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	if ctx.Err() != nil {
		_ = releaseOS()
		m.mu.Unlock()
		return nil, &Error{Code: DiagnosticCancelled}
	}
	if !allowFailStop && m.gatewayRebindFailStopLatch().Load() {
		_ = releaseOS()
		m.mu.Unlock()
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	return func() error {
		defer m.mu.Unlock()
		return releaseOS()
	}, nil
}

func releaseGatewayLock(release func() error, result *error) {
	if release == nil {
		return
	}
	if err := release(); err != nil {
		*result = &Error{Code: DiagnosticRouteUnresolved}
	}
}

// A failed release after Switch cannot prove that a candidate route is no
// longer serving. Preserve the executor's safety signal so it retains the
// candidate container for reconciliation.
func releaseGatewaySwitchLock(release func() error, result *error) {
	if release == nil {
		return
	}
	if err := release(); err != nil {
		*result = candidateMayBeLiveError()
	}
}
