package generatedingress

import (
	"context"
	"net/http"
	"os"
	"sort"
)

const gatewayV2TransferMaximumSteps = 32

// gatewayV2TransferDriver keeps Docker and host-network mutations behind a
// small interface. The caller retains both gateway writer locks around every
// observation, protected write, and side effect.
type gatewayV2TransferDriver interface {
	observeTopology(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology
	observeRecovery(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayV2RecoveryTopology
	proveFinalHostRoutes(context.Context, gatewayV2RouteState, gatewayMigrationJournal) bool
	selectedInterfacePreflight(gatewayProfileBinding) error
	copyFinalConfigToStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal, []byte) error
	readFinalConfigFromStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) ([]byte, error)
	stopStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	removeStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	createFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) (string, error)
	stopV1(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) error
	startV1(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) error
	startFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	stopFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	removeFinal(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
}

// TransferGatewayV2 advances one exact staged migration through cutover and
// commit. The final restart config is installed through the still-running
// stage before transfer_intent, and v1 remains serving until a stopped,
// immutable-ID-bound final container has been fully attested.
func (m *Manager) TransferGatewayV2(ctx context.Context, operationID string) (resultErr error) {
	if m == nil || ctx == nil || !validCanonicalUUID(operationID) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)

	store, err := newGatewayUpgradeStateStore(m.options.DataRoot)
	if err != nil {
		return gatewayV2TransferError(ctx)
	}
	driver := m.gatewayV2TransferDriver
	if driver == nil {
		driver = managerGatewayV2TransferDriver{manager: m}
	}
	return m.transferGatewayV2Locked(ctx, store, driver, operationID)
}

func (m *Manager) transferGatewayV2Locked(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2TransferDriver, operationID string) error {
	for step := 0; step < gatewayV2TransferMaximumSteps; step++ {
		state, journal, err := store.loadBoundUpgrade(operationID)
		if err != nil {
			return gatewayV2TransferError(ctx)
		}
		source, err := m.store.load()
		if err != nil {
			return gatewayV2TransferError(ctx)
		}
		if ctx.Err() != nil {
			if journal.Phase == gatewayPhaseTransferIntent || journal.Phase == gatewayPhaseV2Serving {
				return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, &Error{Code: DiagnosticCancelled})
			}
			return &Error{Code: DiagnosticCancelled}
		}

		switch journal.Phase {
		case gatewayPhaseStaged:
			if err := prepareGatewayV2Transfer(ctx, store, driver, source, state, journal); err != nil {
				return err
			}
		case gatewayPhaseTransferIntent:
			if err := m.advanceGatewayV2Transfer(ctx, store, driver, source, state, journal); err != nil {
				return err
			}
		case gatewayPhaseV2Serving:
			if driver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactFinalV2 {
				return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, gatewayV2TransferError(ctx))
			}
			if !driver.proveFinalHostRoutes(ctx, state, journal) {
				return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, gatewayV2TransferError(ctx))
			}
			if err := driver.selectedInterfacePreflight(state.Profile); err != nil {
				return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, err)
			}
			if _, err := store.transitionMigrationJournal(operationID, gatewayPhaseV2Serving, gatewayPhaseCommitted); err != nil {
				return gatewayV2TransferError(ctx)
			}
		case gatewayPhaseCommitted:
			if driver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactFinalV2 ||
				!driver.proveFinalHostRoutes(ctx, state, journal) {
				return gatewayV2TransferError(ctx)
			}
			return nil
		case gatewayPhaseRollbackIntent:
			return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, gatewayV2TransferError(ctx))
		case gatewayPhaseRolledBack, gatewayPhaseUncertain:
			return gatewayV2TransferError(ctx)
		default:
			return gatewayV2TransferError(ctx)
		}
	}
	return gatewayV2TransferError(ctx)
}

func prepareGatewayV2Transfer(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2TransferDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	if driver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactV1WithStage {
		return markGatewayV2TransferUncertain(store, journal)
	}
	expected, err := expectedGatewayV2FinalConfig(state)
	if err != nil {
		return gatewayV2TransferError(ctx)
	}
	if err := driver.copyFinalConfigToStage(ctx, state, journal, append([]byte(nil), expected...)); err != nil {
		return err
	}
	installed, err := driver.readFinalConfigFromStage(ctx, state, journal)
	if err != nil || !sameCaddyConfig(expected, installed) {
		clear(installed)
		return gatewayV2TransferError(ctx)
	}
	clear(installed)
	if driver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactV1WithStage {
		return markGatewayV2TransferUncertain(store, journal)
	}
	if _, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseStaged, gatewayPhaseTransferIntent); err != nil {
		return gatewayV2TransferError(ctx)
	}
	return nil
}

func (m *Manager) advanceGatewayV2Transfer(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2TransferDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	if driver.observeTopology(ctx, source, state, journal) == gatewayTopologyExactFinalV2 {
		if !driver.proveFinalHostRoutes(ctx, state, journal) {
			return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, gatewayV2TransferError(ctx))
		}
		if err := driver.selectedInterfacePreflight(state.Profile); err != nil {
			return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, err)
		}
		if _, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseTransferIntent, gatewayPhaseV2Serving); err != nil {
			return gatewayV2TransferError(ctx)
		}
		return nil
	}

	recovery := driver.observeRecovery(ctx, source, state, journal)
	switch recovery {
	case gatewayV2RecoveryTransferIntentV1ServingStoppedStage:
		if err := driver.removeStage(ctx, state, journal); err != nil {
			return reconcileGatewayV2TransferFailure(ctx, store, driver, source, state, journal, err)
		}
		return nil
	case gatewayV2RecoveryTransferIntentV1ServingNoContainers:
		if err := driver.selectedInterfacePreflight(state.Profile); err != nil {
			return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, err)
		}
		id, err := driver.createFinal(ctx, state, journal)
		if err != nil {
			return reconcileGatewayV2TransferFailure(ctx, store, driver, source, state, journal, err)
		}
		if _, err := store.bindMigrationDockerResource(journal.OperationID, gatewayPhaseTransferIntent, gatewayV2ResourceFinalContainer, id); err != nil {
			// The protected write may have installed despite reporting a
			// durability failure. A fresh invocation must recover it.
			return gatewayV2TransferError(ctx)
		}
		return nil
	case gatewayV2RecoveryTransferIntentV1ServingStoppedFinal:
		if err := driver.selectedInterfacePreflight(state.Profile); err != nil {
			return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, err)
		}
		if err := driver.stopV1(ctx, source, state, journal); err != nil {
			return reconcileGatewayV2TransferFailure(ctx, store, driver, source, state, journal, err)
		}
		return nil
	case gatewayV2RecoveryTransferIntentStoppedFinal:
		if err := driver.selectedInterfacePreflight(state.Profile); err != nil {
			return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, err)
		}
		if err := driver.startFinal(ctx, state, journal); err != nil {
			return reconcileGatewayV2TransferFailure(ctx, store, driver, source, state, journal, err)
		}
		return nil
	case gatewayV2RecoveryTransferIntentStoppedV1, gatewayV2RecoveryTransferIntentV1StoppedStage,
		gatewayV2RecoveryTransferIntentNoContainers:
		// These are exact interruption states from the older stop-v1-first
		// ordering. Recover them only by restoring the attested v1 gateway.
		return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, gatewayV2TransferError(ctx))
	default:
		if driver.observeTopology(ctx, source, state, journal) == gatewayTopologyExactV1WithStage {
			if err := driver.stopStage(ctx, state, journal); err != nil {
				return reconcileGatewayV2TransferFailure(ctx, store, driver, source, state, journal, err)
			}
			return nil
		}
		return markGatewayV2TransferUncertain(store, journal)
	}
}

func reconcileGatewayV2TransferFailure(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2TransferDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, original error,
) error {
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if topology := driver.observeTopology(reconcileCtx, source, state, journal); topology == gatewayTopologyExactFinalV2 {
		return nil
	}
	if recovery := driver.observeRecovery(reconcileCtx, source, state, journal); recovery != gatewayV2RecoveryUnknown {
		return rollbackGatewayV2Transfer(ctx, store, driver, source, state, journal, original)
	}
	return markGatewayV2TransferUncertain(store, journal)
}

func rollbackGatewayV2Transfer(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2TransferDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, original error,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if journal.Phase != gatewayPhaseRollbackIntent {
		if journal.Phase != gatewayPhaseTransferIntent && journal.Phase != gatewayPhaseV2Serving {
			return markGatewayV2TransferUncertain(store, journal)
		}
		var err error
		journal, err = store.transitionMigrationJournal(journal.OperationID, journal.Phase, gatewayPhaseRollbackIntent)
		if err != nil {
			return gatewayV2TransferError(ctx)
		}
	}

	for step := 0; step < gatewayV2TransferMaximumSteps; step++ {
		topology := driver.observeTopology(rollbackCtx, source, state, journal)
		switch topology {
		case gatewayTopologyExactV1Only:
			if _, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseRollbackIntent, gatewayPhaseRolledBack); err != nil {
				return gatewayV2TransferError(ctx)
			}
			return original
		case gatewayTopologyExactV1WithStage:
			if err := driver.stopStage(rollbackCtx, state, journal); err != nil {
				return markGatewayV2TransferUncertain(store, journal)
			}
			continue
		case gatewayTopologyExactFinalV2:
			if err := driver.stopFinal(rollbackCtx, state, journal); err != nil {
				return markGatewayV2TransferUncertain(store, journal)
			}
			continue
		}

		switch driver.observeRecovery(rollbackCtx, source, state, journal) {
		case gatewayV2RecoveryTransferIntentV1ServingStoppedStage:
			if err := driver.removeStage(rollbackCtx, state, journal); err != nil {
				return markGatewayV2TransferUncertain(store, journal)
			}
		case gatewayV2RecoveryTransferIntentV1ServingStoppedFinal:
			if err := driver.removeFinal(rollbackCtx, state, journal); err != nil {
				return markGatewayV2TransferUncertain(store, journal)
			}
		case gatewayV2RecoveryTransferIntentStoppedV1, gatewayV2RecoveryTransferIntentV1StoppedStage,
			gatewayV2RecoveryTransferIntentNoContainers, gatewayV2RecoveryTransferIntentStoppedFinal:
			if err := driver.startV1(rollbackCtx, source, state, journal); err != nil {
				return markGatewayV2TransferUncertain(store, journal)
			}
		case gatewayV2RecoveryTransferIntentV1ServingNoContainers:
			// The next topology observation records exact v1-only rollback.
		default:
			return markGatewayV2TransferUncertain(store, journal)
		}
	}
	return markGatewayV2TransferUncertain(store, journal)
}

func markGatewayV2TransferUncertain(store *gatewayUpgradeStateStore, journal gatewayMigrationJournal) error {
	if store != nil && journal.Phase != gatewayPhaseUncertain && journal.Phase != gatewayPhaseCommitted && journal.Phase != gatewayPhaseRolledBack {
		_, _ = store.transitionMigrationJournal(journal.OperationID, journal.Phase, gatewayPhaseUncertain)
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

func gatewayV2TransferError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

type managerGatewayV2TransferDriver struct{ manager *Manager }

func (d managerGatewayV2TransferDriver) observeTopology(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayObservedTopology {
	return d.manager.observeV2Topology(ctx, source, state, journal)
}

func (d managerGatewayV2TransferDriver) observeRecovery(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayV2RecoveryTopology {
	return d.manager.observeGatewayV2RecoveryTopology(ctx, source, state, journal)
}

func (d managerGatewayV2TransferDriver) proveFinalHostRoutes(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	return d.manager != nil && proveGatewayV2FinalLoopbackRoutes(ctx, state, journal, probeGatewayV2HostStatus)
}

func proveGatewayV2FinalLoopbackRoutes(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal, probe gatewayV2HostStatusProbe) bool {
	if ctx == nil || probe == nil || !validGatewayV2RouteState(state) || !validGatewayMigrationJournal(journal) ||
		journal.OperationID != state.OperationID || journal.Source.LocalHostPort == 0 {
		return false
	}
	if result := probe(ctx, "127.0.0.1", journal.Source.LocalHostPort, "wrong.invalid", "/"); ctx.Err() != nil || !result.Connected || !result.Responded || result.Status != http.StatusNotFound {
		return false
	}
	apps := make([]string, 0, len(state.Apps))
	for appID := range state.Apps {
		apps = append(apps, appID)
	}
	sort.Strings(apps)
	baseChallenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		return false
	}
	for _, appID := range apps {
		challenge := gatewayV2AppChallenge(baseChallenge, appID)
		result := probe(ctx, "127.0.0.1", journal.Source.LocalHostPort, appID+".rig.localhost", gatewayV2ChallengePathPrefix+challenge)
		if ctx.Err() != nil || !result.Connected || !result.Responded || result.Status != http.StatusNotFound ||
			result.Body != gatewayV2ChallengeBodyPrefix+challenge {
			return false
		}
		rootResult := probe(ctx, "127.0.0.1", journal.Source.LocalHostPort, appID+".rig.localhost", "/")
		if ctx.Err() != nil || !rootResult.Connected || !rootResult.Responded || rootResult.Status < 200 || rootResult.Status > 599 {
			return false
		}
	}
	return true
}

func (managerGatewayV2TransferDriver) selectedInterfacePreflight(profile gatewayProfileBinding) error {
	return gatewayV2SelectedInterfacePreflight(profile)
}

func (d managerGatewayV2TransferDriver) copyFinalConfigToStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal, contents []byte) error {
	defer clear(contents)
	if journal.Phase != gatewayPhaseStaged {
		return gatewayV2TransferError(ctx)
	}
	stage, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(stage.ID) != journal.Resources.StageContainerID || !stage.Running || stage.Restarting {
		return gatewayV2TransferError(ctx)
	}
	return d.copyConfig(ctx, stage.ID, state.Identity.ActiveConfigFilename, contents)
}

func (d managerGatewayV2TransferDriver) readFinalConfigFromStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) ([]byte, error) {
	stage, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(stage.ID) != journal.Resources.StageContainerID || !stage.Running || stage.Restarting {
		return nil, gatewayV2TransferError(ctx)
	}
	return d.manager.inspectStoppedCaddyRestartConfig(ctx, stage.ID, state.Identity.ActiveConfigFilename)
}

func (d managerGatewayV2TransferDriver) copyConfig(ctx context.Context, containerID, filename string, contents []byte) error {
	if !validContainerID(containerID) || !validConfigFilename(filename) || len(contents) == 0 || len(contents) > gatewayV2MaxConfigBytes || !d.manager.validWorkingDirectory() {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	file, err := os.CreateTemp(d.manager.options.WorkingDirectory, ".rig-caddy-v2-transfer-*.json")
	if err != nil {
		return gatewayV2TransferError(ctx)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return gatewayV2TransferError(ctx)
	}
	if _, err := file.Write(contents); err != nil || file.Sync() != nil {
		file.Close()
		return gatewayV2TransferError(ctx)
	}
	created, err := file.Stat()
	if err != nil || !created.Mode().IsRegular() || file.Close() != nil {
		file.Close()
		return gatewayV2TransferError(ctx)
	}
	installed, err := os.Lstat(path)
	if err != nil || !installed.Mode().IsRegular() || installed.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(path) ||
		!os.SameFile(created, installed) || !d.manager.validWorkingDirectory() {
		return gatewayV2TransferError(ctx)
	}
	if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "cp", path, containerID+":/config/"+filename); err != nil {
		return gatewayV2TransferError(ctx)
	}
	return nil
}

func (d managerGatewayV2TransferDriver) stopStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	return (managerGatewayV2UpgradeDriver{manager: d.manager}).stopStage(ctx, state, journal)
}

func (d managerGatewayV2TransferDriver) removeStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	return (managerGatewayV2UpgradeDriver{manager: d.manager}).removeStage(ctx, state, journal)
}

func (d managerGatewayV2TransferDriver) createFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) (string, error) {
	before, err := d.manager.inspectGatewayRouteEndpointProof(ctx, gatewayV2RouteRecords(state))
	if err != nil {
		return "", gatewayV2TransferError(ctx)
	}
	args, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2FinalContainerRole, "sha256:"+journal.Resources.ImageID)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	clearResult(&result)
	if err != nil {
		return "", err
	}
	after, err := d.manager.inspectGatewayRouteEndpointProof(ctx, gatewayV2RouteRecords(state))
	if err != nil || before != after {
		return "", gatewayV2TransferError(ctx)
	}
	container, runtime, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if err != nil || !validGatewayV2StoppedContainer(state, journal, container, runtime, found, gatewayV2FinalContainerRole, "sha256:"+journal.Resources.ImageID) {
		return "", gatewayV2TransferError(ctx)
	}
	return container.ID, nil
}

func (d managerGatewayV2TransferDriver) stopV1(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	id, recovery, err := d.attestV1Mutation(ctx, source, state, journal)
	if err != nil || recovery != gatewayV2RecoveryTransferIntentV1ServingStoppedFinal {
		return gatewayV2TransferError(ctx)
	}
	return d.setContainerRunning(ctx, caddyContainerName, id, false)
}

func (d managerGatewayV2TransferDriver) startV1(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	id, recovery, err := d.attestV1Mutation(ctx, source, state, journal)
	if err != nil {
		return gatewayV2TransferError(ctx)
	}
	switch recovery {
	case gatewayV2RecoveryTransferIntentStoppedV1, gatewayV2RecoveryTransferIntentV1StoppedStage,
		gatewayV2RecoveryTransferIntentNoContainers, gatewayV2RecoveryTransferIntentStoppedFinal:
	default:
		return gatewayV2TransferError(ctx)
	}
	return d.setContainerRunning(ctx, caddyContainerName, id, true)
}

func (d managerGatewayV2TransferDriver) attestV1Mutation(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) (string, gatewayV2RecoveryTopology, error) {
	observation, err := d.manager.inspectGatewayV2Docker(ctx, source, state, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return "", gatewayV2RecoveryUnknown, err
	}
	defer clearGatewayV2DockerObservation(&observation)
	recovery := classifyGatewayV2RecoveryTopology(source, state, journal, observation)
	if recovery == gatewayV2RecoveryUnknown || !validContainerID(observation.V1Container.ID) {
		return "", gatewayV2RecoveryUnknown, gatewayV2TransferError(ctx)
	}
	return observation.V1Container.ID, recovery, nil
}

func (d managerGatewayV2TransferDriver) startFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if !validSHA256(journal.Resources.FinalContainerID) {
		return gatewayV2TransferError(ctx)
	}
	return d.setContainerRunning(ctx, state.Identity.FinalContainer, journal.Resources.FinalContainerID, true)
}

func (d managerGatewayV2TransferDriver) stopFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if !validSHA256(journal.Resources.FinalContainerID) {
		return gatewayV2TransferError(ctx)
	}
	return d.setContainerRunning(ctx, state.Identity.FinalContainer, journal.Resources.FinalContainerID, false)
}

func (d managerGatewayV2TransferDriver) setContainerRunning(ctx context.Context, name, expectedID string, running bool) error {
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, name)
	if err != nil || !found || !validContainerID(container.ID) ||
		(expectedID != "" && normalizeID(container.ID) != normalizeID(expectedID)) || container.Restarting || container.Running == running {
		return gatewayV2TransferError(ctx)
	}
	action := "start"
	args := []string{"container", action, container.ID}
	if !running {
		action = "stop"
		args = []string{"container", action, "--time", "10", container.ID}
	}
	runErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, args...)
	confirmed, _, confirmedFound, inspectErr := d.manager.inspectNamedGatewayContainer(context.WithoutCancel(ctx), name)
	if inspectErr != nil || !confirmedFound || normalizeID(confirmed.ID) != normalizeID(container.ID) || confirmed.Restarting || confirmed.Running != running {
		if runErr != nil {
			return runErr
		}
		return gatewayV2TransferError(ctx)
	}
	return nil
}

func (d managerGatewayV2TransferDriver) removeFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if err != nil || !found || normalizeID(container.ID) != journal.Resources.FinalContainerID || container.Running || container.Restarting {
		return gatewayV2TransferError(ctx)
	}
	if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "rm", container.ID); err != nil {
		return err
	}
	_, _, found, err = d.manager.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if err != nil || found {
		return gatewayV2TransferError(ctx)
	}
	return nil
}

var _ gatewayV2TransferDriver = managerGatewayV2TransferDriver{}
