//go:build live_docker

package generatedingress

import "context"

// TraceGatewayV2LANGrantForLiveDocker records only closed stage outcomes in
// live Docker tests. It delegates every operation to the production driver.
// Call before the manager is used; the callback must not log request data.
func (m *Manager) TraceGatewayV2LANGrantForLiveDocker(record func(stage string, ok bool)) {
	if m == nil || record == nil {
		return
	}
	// Keep the concrete production driver's optional recovery and port-proof
	// methods available to type assertions in other gateway paths.
	if m.gatewayV2LANGrantDriver != nil {
		return
	}
	m.gatewayV2LANGrantDriver = liveDockerLANGrantTraceDriver{
		managerGatewayV2LANGrantDriver: managerGatewayV2LANGrantDriver{manager: m}, record: record,
	}
}

type liveDockerLANGrantTraceDriver struct {
	managerGatewayV2LANGrantDriver
	record func(string, bool)
}

func (d liveDockerLANGrantTraceDriver) proveCommitted(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	ok := d.managerGatewayV2LANGrantDriver.proveCommitted(ctx, state, journal)
	d.record("prove_committed", ok)
	return ok
}

func (d liveDockerLANGrantTraceDriver) selectedInterfacePreflight(profile gatewayProfileBinding) error {
	err := d.managerGatewayV2LANGrantDriver.selectedInterfacePreflight(profile)
	d.record("selected_interface", err == nil)
	return err
}

func (d liveDockerLANGrantTraceDriver) preflightCandidate(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, appID string, proposed gatewayV2AppRoute,
) error {
	err := d.managerGatewayV2LANGrantDriver.preflightCandidate(ctx, state, journal, appID, proposed)
	d.record("candidate_preflight", err == nil)
	return err
}

func (d liveDockerLANGrantTraceDriver) apply(ctx context.Context, state gatewayV2RouteState, filename string) error {
	err := d.managerGatewayV2LANGrantDriver.apply(ctx, state, filename)
	stage := "apply"
	if err != nil {
		switch {
		case IsCode(err, DiagnosticRouteInvalid):
			stage = "apply_route_invalid"
		case IsCode(err, DiagnosticIngressUnavailable):
			stage = "apply_ingress_unavailable"
		case IsCode(err, DiagnosticIngressDrift):
			stage = "apply_ingress_drift"
		case IsCode(err, DiagnosticRouteValidateFailed):
			stage = "apply_validate_failed"
		case IsCode(err, DiagnosticRouteReloadFailed):
			stage = "apply_reload_failed"
		case IsCode(err, DiagnosticRouteUnresolved):
			stage = "apply_unresolved"
		default:
			stage = "apply_other"
		}
	}
	d.record(stage, err == nil)
	return err
}

func (d liveDockerLANGrantTraceDriver) proveGranted(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	ok := d.managerGatewayV2LANGrantDriver.proveGranted(ctx, state, journal, request)
	d.record("prove_granted", ok)
	return ok
}

func (d liveDockerLANGrantTraceDriver) proveRolledBack(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	ok := d.managerGatewayV2LANGrantDriver.proveRolledBack(ctx, state, journal, request)
	d.record("prove_rolled_back", ok)
	return ok
}

func (d liveDockerLANGrantTraceDriver) observePending(ctx context.Context, committed, proposed gatewayV2RouteState,
	journal gatewayMigrationJournal,
) gatewayV2PendingLiveTopology {
	topology := d.managerGatewayV2LANGrantDriver.observePending(ctx, committed, proposed, journal)
	d.record("observe_pending", topology != gatewayV2PendingUnknown)
	return topology
}

var (
	_ gatewayV2LANRecoveryBatchDriver = liveDockerLANGrantTraceDriver{}
	_ gatewayV2LANRecoveryBatchProof  = liveDockerLANGrantTraceDriver{}
	_ gatewayV2LANPortAbsenceProver   = liveDockerLANGrantTraceDriver{}
)
