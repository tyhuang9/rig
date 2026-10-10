package generatedingress

// newGatewayRebindRuntimeDrivers composes the concrete adapters against one
// Manager and its Docker runner. Construction grants no authority and does not
// install either adapter. Production effect factories remain closed until the
// startup ordering and live Docker acceptance gates are complete.
func newGatewayRebindRuntimeDrivers(m *Manager) (managerGatewayRebindCrossStoreDriver, gatewayCurrentPhysicalDriver) {
	handover := newGatewayRebindTypedHandoverRuntime(m)
	return managerGatewayRebindCrossStoreDriver{manager: m, stage: handover.stage, handover: handover},
		newManagedGatewayCurrentPhysicalDriver(m)
}
