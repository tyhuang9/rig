package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayV2LANRecoveryBatchTopology string

const (
	gatewayV2LANRecoveryBatchUnknown        gatewayV2LANRecoveryBatchTopology = "unknown_or_identity_drift"
	gatewayV2LANRecoveryBatchBeforeExact    gatewayV2LANRecoveryBatchTopology = "before_live_and_restart_exact"
	gatewayV2LANRecoveryBatchEffectiveExact gatewayV2LANRecoveryBatchTopology = "effective_live_and_restart_exact"
	gatewayV2LANRecoveryBatchReloadMixed    gatewayV2LANRecoveryBatchTopology = "effective_live_before_restart_exact"
)

type gatewayV2LANRecoveryBatchDriver interface {
	gatewayV2LANGrantDriver
	observeLANRecoveryBatch(context.Context, gatewayV2RouteState, gatewayV2RouteState,
		gatewayMigrationJournal) gatewayV2LANRecoveryBatchTopology
}

type gatewayV2LANRecoveryBatchProof interface {
	proveLANRecoveryBatch(context.Context, gatewayV2RouteState, gatewayMigrationJournal, []uint16) bool
}

// QuarantineGatewayV2LANAccessRecoveryBatch installs an immutable recovery
// census before withdrawing every route whose database operation is not yet
// terminal. The protected batch remains unchanged while Caddy receives a
// transient projection with all unsafe bindings removed.
func (m *Manager) QuarantineGatewayV2LANAccessRecoveryBatch(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) (resultErr error) {
	if m == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)

	workCtx, cancelWork := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelWork()
	if handled, currentErr := m.quarantineGatewayCurrentLANRecoveryBatchLocked(workCtx, grants, disables); currentErr != nil || handled {
		return currentErr
	}
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || !validGatewayV2RouteState(state) {
		return gatewayV2StartupInspectionError(workCtx)
	}
	baseDriver := m.gatewayV2LANDisableDriver()
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, baseDriver)
	}
	driver, ok := baseDriver.(gatewayV2LANRecoveryBatchDriver)
	if !ok || baseDriver.selectedInterfacePreflight(state.Profile) != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, baseDriver)
	}

	batchInstalled := state.LANRecovery != nil
	if batchInstalled {
		if !gatewayV2LANRecoveryCensusMatchesHead(state, claims) {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
	} else {
		inspection, inspectErr := inspectGatewayV2LANAccessStartupLocked(workCtx, state, journal, claims, driver)
		if inspectErr != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		items, itemsErr := gatewayV2LANRecoveryItems(inspection, claims)
		if itemsErr != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		batchState := cloneGatewayV2RouteState(state)
		batchState.Pending = nil
		batchState.LANRecovery = &gatewayV2LANRecoveryBatch{
			Items: items, LegacyPending: cloneGatewayV2PendingRoute(state.Pending),
		}
		// A failed protected write is ambiguous. From this point onward, stop
		// only the journal-bound gateway on every failure path.
		if store.saveCommittedV2State(batchState, journal) != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		state = batchState
		batchInstalled = true
	}
	if !batchInstalled {
		return gatewayV2StartupInspectionError(workCtx)
	}

	effective, _, err := gatewayV2LANRecoveryEffectiveProjection(state)
	if err != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	unsafePorts, err := gatewayV2LANRecoveryAllPorts(state)
	if err != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	switch driver.observeLANRecoveryBatch(workCtx, state, effective, journal) {
	case gatewayV2LANRecoveryBatchEffectiveExact:
		// The reload and restart configuration already match the projection.
	case gatewayV2LANRecoveryBatchBeforeExact, gatewayV2LANRecoveryBatchReloadMixed:
		if driver.apply(workCtx, effective, "lan-recovery-batch.json") != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
	default:
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if !proveGatewayV2LANRecoveryBatch(workCtx, driver, effective, journal, unsafePorts) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	retained, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	return nil
}

func gatewayV2LANRecoveryItems(inspection GatewayV2LANAccessStartupInspection,
	claims gatewayV2LANAccessStartupClaims,
) ([]gatewayV2LANRecoveryItem, error) {
	recoveries := append([]GatewayV2LANAccessStartupRecovery(nil), inspection.Recoveries...)
	if len(recoveries) == 0 && inspection.RecoveryKind != "" {
		recoveries = append(recoveries, GatewayV2LANAccessStartupRecovery{
			Kind: inspection.RecoveryKind, OperationID: inspection.OperationID, AppID: inspection.AppID,
		})
	}
	if inspection.Disposition != GatewayV2LANStartupRecoveryOnly || len(recoveries) == 0 || len(recoveries) > maxStateApps {
		return nil, errors.New("invalid generated ingress LAN recovery census")
	}
	items := make([]gatewayV2LANRecoveryItem, 0, len(recoveries))
	for _, recovery := range recoveries {
		item := gatewayV2LANRecoveryItem{AppID: recovery.AppID}
		switch recovery.Kind {
		case GatewayV2LANRecoveryGrant:
			claim, exists := claims.grants.byAttempt[recovery.OperationID]
			binding, err := gatewayV2LANBindingForRequest(claim.Request)
			if !exists || err != nil || claim.Request.AppID != recovery.AppID ||
				claim.State == appaccess.AppAccessGrantCommitted {
				return nil, errors.New("invalid generated ingress LAN grant recovery census")
			}
			item.Kind = gatewayV2PendingLANGrant
			item.Grant = &binding
		case GatewayV2LANRecoveryDisable:
			claim, exists := claims.disables[recovery.OperationID]
			if !exists || claim.Request.AppID != recovery.AppID {
				return nil, errors.New("invalid generated ingress LAN disable recovery census")
			}
			request := claim.Request
			if request.SourceGrant != nil {
				source := *request.SourceGrant
				request.SourceGrant = &source
			}
			item.Kind = gatewayV2PendingLANDisable
			item.Disable = &request
		default:
			return nil, errors.New("invalid generated ingress LAN recovery kind")
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return gatewayV2LANRecoveryItemLess(items[i], items[j]) })
	apps := make(map[string]int, len(items))
	ports := make(map[uint16]int, len(items))
	for index, item := range items {
		port, _ := gatewayV2LANRecoveryItemIdentity(item)
		if previous, duplicate := apps[item.AppID]; duplicate {
			if previous != index-1 || !gatewayV2LANRecoveryItemsAreLegacyPreparedPair(items[previous], item, claims) {
				return nil, errors.New("ambiguous generated ingress LAN recovery app")
			}
		} else {
			apps[item.AppID] = index
		}
		if previous, duplicate := ports[port]; duplicate {
			if previous != index-1 || !gatewayV2LANRecoveryItemsAreLegacyPreparedPair(items[previous], item, claims) {
				return nil, errors.New("ambiguous generated ingress LAN recovery port")
			}
		} else {
			ports[port] = index
		}
	}
	return items, nil
}

func gatewayV2LANRecoveryItemsAreLegacyPreparedPair(grant, disable gatewayV2LANRecoveryItem,
	claims gatewayV2LANAccessStartupClaims,
) bool {
	if !gatewayV2LANRecoveryItemsAreLegacyPair(grant, disable) {
		return false
	}
	_, grantOperationID := gatewayV2LANRecoveryItemIdentity(grant)
	_, disableOperationID := gatewayV2LANRecoveryItemIdentity(disable)
	grantClaim, grantExists := claims.grants.byAttempt[grantOperationID]
	disableClaim, disableExists := claims.disables[disableOperationID]
	return grantExists && disableExists && gatewayV2LANRecoveryLegacyPreparedPair(grantClaim, disableClaim)
}

// Every item port remains absent while a recovery batch is installed,
// including items already traversed by Head. The completed batch retains its
// immutable port identities until a separate terminal retirement proof.
func gatewayV2LANRecoveryAllPorts(state gatewayV2RouteState) ([]uint16, error) {
	if !validGatewayV2RouteState(state) || state.LANRecovery == nil {
		return nil, errors.New("invalid generated ingress LAN recovery batch")
	}
	seen := make(map[uint16]struct{}, len(state.LANRecovery.Items))
	ports := make([]uint16, 0, len(state.LANRecovery.Items))
	for _, item := range state.LANRecovery.Items {
		port, _ := gatewayV2LANRecoveryItemIdentity(item)
		if port == 0 {
			return nil, errors.New("invalid generated ingress LAN recovery port")
		}
		if _, duplicate := seen[port]; !duplicate {
			seen[port] = struct{}{}
			ports = append(ports, port)
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, nil
}

func gatewayV2LANRecoveryCommittedProjection(state gatewayV2RouteState) gatewayV2RouteState {
	result := cloneGatewayV2RouteState(state)
	result.Pending = nil
	result.LANRecovery = nil
	return result
}

func gatewayV2LANRecoveryEffectiveProjection(state gatewayV2RouteState) (gatewayV2RouteState, []uint16, error) {
	if !validGatewayV2RouteState(state) || state.LANRecovery == nil {
		return gatewayV2RouteState{}, nil, errors.New("invalid generated ingress LAN recovery batch")
	}
	effective := gatewayV2LANRecoveryCommittedProjection(state)
	unfinished := state.LANRecovery.Items[state.LANRecovery.Head:]
	ports := make([]uint16, 0, len(unfinished))
	seen := make(map[uint16]gatewayV2LANRecoveryItem, len(unfinished))
	for _, item := range unfinished {
		app, exists := effective.Apps[item.AppID]
		port, _ := gatewayV2LANRecoveryItemIdentity(item)
		if !exists || port == 0 {
			return gatewayV2RouteState{}, nil, errors.New("invalid generated ingress LAN recovery item")
		}
		if previous, duplicate := seen[port]; duplicate {
			if !gatewayV2LANRecoveryItemsAreLegacyPair(previous, item) {
				return gatewayV2RouteState{}, nil, errors.New("ambiguous generated ingress LAN recovery port")
			}
		} else {
			seen[port] = item
			ports = append(ports, port)
		}
		app.LAN = nil
		effective.Apps[item.AppID] = app
	}
	if !validGatewayV2RouteState(effective) {
		return gatewayV2RouteState{}, nil, errors.New("invalid generated ingress LAN recovery projection")
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return effective, ports, nil
}

func gatewayV2LANRecoveryBeforeProjections(state gatewayV2RouteState) ([]gatewayV2RouteState, error) {
	if !validGatewayV2RouteState(state) || state.LANRecovery == nil {
		return nil, errors.New("invalid generated ingress LAN recovery batch")
	}
	committed := gatewayV2LANRecoveryCommittedProjection(state)
	result := []gatewayV2RouteState{committed}
	legacy := state.LANRecovery.LegacyPending
	if legacy != nil {
		proposed := cloneGatewayV2RouteState(committed)
		proposed.Apps[legacy.AppID] = cloneGatewayV2AppRoute(legacy.Proposed)
		if !validGatewayV2RouteState(proposed) {
			return nil, errors.New("invalid generated ingress legacy recovery projection")
		}
		if !reflect.DeepEqual(proposed, committed) {
			result = append(result, proposed)
		}
	}
	return result, nil
}

func proveGatewayV2LANRecoveryBatch(ctx context.Context, driver gatewayV2LANGrantDriver,
	effective gatewayV2RouteState, journal gatewayMigrationJournal, unsafePorts []uint16,
) bool {
	if ctx == nil || driver == nil || len(unsafePorts) == 0 || driver.selectedInterfacePreflight(effective.Profile) != nil {
		return false
	}
	if batchProof, ok := driver.(gatewayV2LANRecoveryBatchProof); ok {
		return batchProof.proveLANRecoveryBatch(ctx, effective, journal, unsafePorts) &&
			driver.selectedInterfacePreflight(effective.Profile) == nil
	}
	for _, port := range unsafePorts {
		if !driver.proveRolledBack(ctx, effective, journal, gatewayV2LANGrantRequest{Port: port}) {
			return false
		}
	}
	return driver.proveAllGranted(ctx, effective, journal) && driver.selectedInterfacePreflight(effective.Profile) == nil
}

// The production proof attests the owned gateway once, then checks each
// withdrawn listener and every retained grant. The test driver uses the
// generic per-port path above so missing or duplicated port proofs are visible.
func (d managerGatewayV2LANGrantDriver) proveLANRecoveryBatch(ctx context.Context,
	effective gatewayV2RouteState, journal gatewayMigrationJournal, unsafePorts []uint16,
) bool {
	if d.manager == nil || !validSHA256(journal.Resources.FinalContainerID) ||
		d.manager.attestCommittedV2Locked(ctx, effective, journal) != nil {
		return false
	}
	for _, port := range unsafePorts {
		if !proveGatewayV2LANRollbackPublication(ctx, effective, port, journal.Resources.FinalContainerID,
			probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge) {
			return false
		}
	}
	return proveGatewayV2LANCommittedPublications(ctx, effective, journal.Resources.FinalContainerID,
		probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge) && ctx.Err() == nil
}

func (d managerGatewayV2LANGrantDriver) observeLANRecoveryBatch(ctx context.Context, state,
	effective gatewayV2RouteState, journal gatewayMigrationJournal,
) gatewayV2LANRecoveryBatchTopology {
	if d.manager == nil || ctx == nil || !validGatewayV2RouteState(effective) || effective.Pending != nil || effective.LANRecovery != nil {
		return gatewayV2LANRecoveryBatchUnknown
	}
	before, err := gatewayV2LANRecoveryBeforeProjections(state)
	if err != nil {
		return gatewayV2LANRecoveryBatchUnknown
	}
	source, err := d.manager.store.load()
	if err != nil {
		return gatewayV2LANRecoveryBatchUnknown
	}
	if d.manager.observeV2Topology(ctx, source, effective, journal) == gatewayTopologyExactFinalV2 {
		return gatewayV2LANRecoveryBatchEffectiveExact
	}
	for _, candidate := range before {
		if d.manager.observeV2Topology(ctx, source, candidate, journal) == gatewayTopologyExactFinalV2 {
			return gatewayV2LANRecoveryBatchBeforeExact
		}
	}
	if d.manager.observeGatewayV2LANRecoveryBatchMixed(ctx, source, before, effective, journal) {
		return gatewayV2LANRecoveryBatchReloadMixed
	}
	return gatewayV2LANRecoveryBatchUnknown
}

func (m *Manager) observeGatewayV2LANRecoveryBatchMixed(ctx context.Context, source routeState,
	before []gatewayV2RouteState, effective gatewayV2RouteState, journal gatewayMigrationJournal,
) bool {
	if m == nil || ctx == nil || len(before) == 0 || !validGatewayTopologyInputs(source, effective, journal) {
		return false
	}
	for _, candidate := range before {
		if !validGatewayTopologyInputs(source, candidate, journal) {
			return false
		}
	}
	endpointsBefore, err := m.inspectGatewayRouteEndpointProof(ctx, gatewayV2RouteRecords(effective))
	if err != nil {
		return false
	}
	container, _, found, err := m.inspectNamedGatewayContainer(ctx, effective.Identity.FinalContainer)
	if err != nil || !found || !validContainerID(container.ID) ||
		!m.proveGatewayRouteEndpointTransports(ctx, container.ID, gatewayV2RouteRecords(effective)) {
		return false
	}
	endpointsAfter, err := m.inspectGatewayRouteEndpointProof(ctx, gatewayV2RouteRecords(effective))
	if err != nil || endpointsBefore != endpointsAfter {
		return false
	}
	observation, err := m.inspectGatewayV2Docker(ctx, source, effective, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return false
	}
	defer clearGatewayV2DockerObservation(&observation)
	configProven := false
	for _, candidate := range before {
		if validGatewayV2MixedFinalConfig(candidate, effective, observation.FinalConfig, observation.FinalRestartConfig) {
			configProven = true
			break
		}
	}
	v1Stopped := observation.V1Stable && observation.V1ResourcesStable &&
		!observation.V1Container.Running && !observation.V1Container.Restarting
	return normalizeID(container.ID) == normalizeID(observation.FinalContainer.ID) &&
		validGatewayPinnedImage(observation.Image, observation.ImageFound) &&
		gatewayV2ObservedResourcesMatchJournal(journal, observation) &&
		validGatewayV1Base(source, journal, observation, false) && observation.StageStable &&
		observation.FinalStable && observation.OwnedInventoriesStable &&
		validGatewayV2FinalTopology(effective, journal, observation, v1Stopped, false, configProven)
}

var _ gatewayV2LANRecoveryBatchDriver = managerGatewayV2LANGrantDriver{}
