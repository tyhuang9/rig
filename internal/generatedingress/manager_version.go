package generatedingress

// fenceLegacyV1Locked permits legacy operations only when the immutable
// history is empty or every attempted v2 generation has a valid rollback
// retirement receipt. The caller must hold the gateway lock so a new
// generation cannot cross this decision.
func (m *Manager) fenceLegacyV1Locked() error {
	history, err := m.scanGatewayUpgradeHistoryLocked()
	if err != nil || history.committed {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}
