package generatedingress

// newGatewayRebindRuntimeDrivers composes the concrete adapters against one
// Manager and its Docker runner. Construction grants no authority. New installs
// the returned pair on that same Manager; request-scoped commit and reconcile
// facades may wrap the rebind value with invocation-local authorization.
func newGatewayRebindRuntimeDrivers(m *Manager) (managerGatewayRebindCrossStoreDriver, gatewayCurrentPhysicalDriver) {
	handover := newGatewayRebindTypedHandoverRuntime(m)
	return managerGatewayRebindCrossStoreDriver{manager: m, stage: handover.stage, handover: handover},
		newManagedGatewayCurrentPhysicalDriver(m)
}
