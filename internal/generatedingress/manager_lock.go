package generatedingress

import "context"

var managerAcquireGatewayOSLock = acquireGatewayOSLock

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
