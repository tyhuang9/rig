package generatedingress

import (
	"context"
	"os"
)

const gatewayV2StageMaximumSteps = 24

// gatewayV2UpgradeDriver isolates Docker and host-network side effects from
// the durable phase machine. Tests replace it; production always uses the
// exact Manager adapter below while the gateway writer lock is held.
type gatewayV2UpgradeDriver interface {
	observeTopology(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology
	observeRecovery(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayV2RecoveryTopology
	hostPreflight(context.Context, gatewayProfileBinding, gatewayV2NetworkPlan) error
	selectedInterfacePreflight(gatewayProfileBinding) error
	pinnedImage(context.Context) (string, error)
	createIngressNetwork(context.Context, gatewayV2RouteState, gatewayMigrationJournal) (string, error)
	createVolume(context.Context, gatewayV2RouteState, gatewayMigrationJournal, string) (gatewayV1VolumeIdentity, error)
	createStageContainer(context.Context, gatewayV2RouteState, gatewayMigrationJournal) (string, error)
	copyStageConfig(context.Context, gatewayV2RouteState, gatewayMigrationJournal, []byte) error
	readStageRestartConfig(context.Context, gatewayV2RouteState, gatewayMigrationJournal) ([]byte, error)
	attestStoppedStage(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) bool
	attestStoppedStageForCompensation(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) bool
	startStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	stopStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	removeStage(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
}

// StageGatewayV2 advances an already-prepared, explicitly approved migration
// only through the staged phase. It never stops v1 or enters transfer_intent.
// The Manager mutex and cross-process gateway lock remain held across every
// protected read/write, observation, host check, and Docker mutation.
func (m *Manager) StageGatewayV2(ctx context.Context, operationID string) (resultErr error) {
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
		return gatewayV2StageError(ctx)
	}
	driver := m.gatewayV2UpgradeDriver
	if driver == nil {
		driver = managerGatewayV2UpgradeDriver{manager: m}
	}
	return m.stageGatewayV2Locked(ctx, store, driver, operationID)
}

func (m *Manager) stageGatewayV2Locked(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2UpgradeDriver, operationID string) error {
	for step := 0; step < gatewayV2StageMaximumSteps; step++ {
		if ctx.Err() != nil {
			return &Error{Code: DiagnosticCancelled}
		}
		state, journal, err := store.loadBoundUpgrade(operationID)
		if err != nil {
			return gatewayV2StageError(ctx)
		}
		source, err := m.store.load()
		if err != nil {
			return gatewayV2StageError(ctx)
		}

		switch journal.Phase {
		case gatewayPhasePrepared:
			if driver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactV1Only {
				return markGatewayV2StageUncertain(store, journal)
			}
			if err := driver.hostPreflight(ctx, state.Profile, state.Network); err != nil {
				return err
			}
			if _, err := store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseStageIntent); err != nil {
				return gatewayV2StageError(ctx)
			}
		case gatewayPhaseStageIntent:
			if err := m.advanceGatewayV2StageIntent(ctx, store, driver, source, state, journal); err != nil {
				return err
			}
		case gatewayPhaseStaged:
			if driver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactV1WithStage {
				return markGatewayV2StageUncertain(store, journal)
			}
			return nil
		case gatewayPhaseRolledBack, gatewayPhaseUncertain:
			return gatewayV2StageError(ctx)
		default:
			// Transfer and committed ownership belong to later cutover units.
			return gatewayV2StageError(ctx)
		}
	}
	return gatewayV2StageError(ctx)
}

func (m *Manager) advanceGatewayV2StageIntent(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2UpgradeDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	if driver.observeTopology(ctx, source, state, journal) == gatewayTopologyExactV1WithStage {
		_, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseStageIntent, gatewayPhaseStaged)
		if err != nil {
			return gatewayV2StageError(ctx)
		}
		return nil
	}

	switch driver.observeRecovery(ctx, source, state, journal) {
	case gatewayV2RecoveryStageIntentStoppedStage:
		return m.startAndAttestGatewayV2Stage(ctx, store, driver, source, state, journal)
	case gatewayV2RecoveryStageIntentPartialInfrastructure:
		return m.advanceGatewayV2StageResources(ctx, store, driver, source, state, journal)
	default:
		if driver.attestStoppedStageForCompensation(ctx, source, state, journal) {
			return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, gatewayV2StageError(ctx))
		}
		return markGatewayV2StageUncertain(store, journal)
	}
}

func (m *Manager) advanceGatewayV2StageResources(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2UpgradeDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	if journal.Resources.ImageID == "" {
		id, err := driver.pinnedImage(ctx)
		if err != nil {
			return err
		}
		if _, err := store.bindMigrationDockerResource(journal.OperationID, gatewayPhaseStageIntent, gatewayV2ResourceImage, id); err != nil {
			return gatewayV2StageError(ctx)
		}
		return nil
	}
	if journal.Resources.IngressNetworkID == "" {
		if err := driver.hostPreflight(ctx, state.Profile, state.Network); err != nil {
			return err
		}
		id, err := driver.createIngressNetwork(ctx, state, journal)
		if err != nil {
			return handleGatewayV2StageCreateFailure(ctx, store, driver, source, state, journal, err)
		}
		if _, err := store.bindMigrationDockerResource(journal.OperationID, gatewayPhaseStageIntent, gatewayV2ResourceIngressNetwork, id); err != nil {
			return gatewayV2StageError(ctx)
		}
		return nil
	}
	if journal.Resources.ConfigVolume == (gatewayV2VolumeResourceBinding{}) {
		identity, err := driver.createVolume(ctx, state, journal, gatewayV2ConfigVolumeRole)
		if err != nil {
			return handleGatewayV2StageCreateFailure(ctx, store, driver, source, state, journal, err)
		}
		if _, err := store.bindMigrationVolumeResource(journal.OperationID, gatewayPhaseStageIntent, gatewayV2ResourceConfigVolume, identity); err != nil {
			return gatewayV2StageError(ctx)
		}
		return nil
	}
	if journal.Resources.DataVolume == (gatewayV2VolumeResourceBinding{}) {
		identity, err := driver.createVolume(ctx, state, journal, gatewayV2DataVolumeRole)
		if err != nil {
			return handleGatewayV2StageCreateFailure(ctx, store, driver, source, state, journal, err)
		}
		if _, err := store.bindMigrationVolumeResource(journal.OperationID, gatewayPhaseStageIntent, gatewayV2ResourceDataVolume, identity); err != nil {
			return gatewayV2StageError(ctx)
		}
		return nil
	}
	if journal.Resources.StageContainerID != "" {
		return markGatewayV2StageUncertain(store, journal)
	}
	id, err := driver.createStageContainer(ctx, state, journal)
	if err != nil {
		return handleGatewayV2StageCreateFailure(ctx, store, driver, source, state, journal, err)
	}
	bound, err := store.bindMigrationDockerResource(journal.OperationID, gatewayPhaseStageIntent, gatewayV2ResourceStageContainer, id)
	if err != nil {
		// The protected write may have installed despite reporting a durability
		// failure. Stop immediately and recover only from a fresh invocation.
		return gatewayV2StageError(ctx)
	}
	return m.startAndAttestGatewayV2Stage(ctx, store, driver, source, state, bound)
}

func (m *Manager) startAndAttestGatewayV2Stage(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2UpgradeDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	expected, err := buildGatewayV2StageConfig(state)
	if err != nil {
		return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, gatewayV2StageError(ctx))
	}
	if err := driver.copyStageConfig(ctx, state, journal, append([]byte(nil), expected...)); err != nil {
		if isGatewayV2UpgradeAuthorizationDenied(err) {
			return err
		}
		return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, err)
	}
	restart, err := driver.readStageRestartConfig(ctx, state, journal)
	if err != nil || !sameCaddyConfig(expected, restart) {
		clear(restart)
		return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, gatewayV2StageError(ctx))
	}
	clear(restart)
	if !driver.attestStoppedStage(ctx, source, state, journal) {
		return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, gatewayV2StageError(ctx))
	}
	if err := driver.selectedInterfacePreflight(state.Profile); err != nil {
		return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, err)
	}
	startErr := driver.startStage(ctx, state, journal)
	if isGatewayV2UpgradeAuthorizationDenied(startErr) {
		return startErr
	}
	if driver.observeTopology(ctx, source, state, journal) == gatewayTopologyExactV1WithStage {
		if _, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseStageIntent, gatewayPhaseStaged); err != nil {
			return gatewayV2StageError(ctx)
		}
		return nil
	}
	reconcileCtx, cancelReconcile := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancelReconcile()
	if err := driver.stopStage(reconcileCtx, state, journal); err != nil {
		return markGatewayV2StageUncertain(store, journal)
	}
	if driver.observeRecovery(reconcileCtx, source, state, journal) != gatewayV2RecoveryStageIntentStoppedStage {
		return markGatewayV2StageUncertain(store, journal)
	}
	if startErr == nil {
		startErr = gatewayV2StageError(ctx)
	}
	return rollbackGatewayV2Stage(ctx, store, driver, source, state, journal, startErr)
}

func rollbackGatewayV2Stage(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2UpgradeDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, original error,
) error {
	rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancelRollback()
	rollback, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseStageIntent, gatewayPhaseRollbackIntent)
	if err != nil {
		return gatewayV2StageError(ctx)
	}
	if rollback.Resources.StageContainerID != "" {
		if err := driver.stopStage(rollbackCtx, state, rollback); err != nil {
			if isGatewayV2UpgradeAuthorizationDenied(err) {
				return err
			}
			return markGatewayV2StageUncertain(store, rollback)
		}
		if err := driver.removeStage(rollbackCtx, state, rollback); err != nil {
			if isGatewayV2UpgradeAuthorizationDenied(err) {
				return err
			}
			return markGatewayV2StageUncertain(store, rollback)
		}
	}
	if driver.observeTopology(rollbackCtx, source, state, rollback) != gatewayTopologyExactV1Only {
		return markGatewayV2StageUncertain(store, rollback)
	}
	if _, err := store.transitionMigrationJournal(journal.OperationID, gatewayPhaseRollbackIntent, gatewayPhaseRolledBack); err != nil {
		return gatewayV2StageError(ctx)
	}
	return original
}

func handleGatewayV2StageCreateFailure(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2UpgradeDriver,
	source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, original error,
) error {
	if isGatewayV2UpgradeAuthorizationDenied(original) {
		return original
	}
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	if driver.observeRecovery(ctx, source, state, journal) == gatewayV2RecoveryStageIntentPartialInfrastructure {
		return original
	}
	return markGatewayV2StageUncertain(store, journal)
}

func markGatewayV2StageUncertain(store *gatewayUpgradeStateStore, journal gatewayMigrationJournal) error {
	if store != nil && journal.Phase != gatewayPhaseUncertain && journal.Phase != gatewayPhaseCommitted && journal.Phase != gatewayPhaseRolledBack {
		_, _ = store.transitionMigrationJournal(journal.OperationID, journal.Phase, gatewayPhaseUncertain)
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

func gatewayV2StageError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

type managerGatewayV2UpgradeDriver struct{ manager *Manager }

func (d managerGatewayV2UpgradeDriver) observeTopology(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayObservedTopology {
	return d.manager.observeV2Topology(ctx, source, state, journal)
}

func (d managerGatewayV2UpgradeDriver) observeRecovery(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayV2RecoveryTopology {
	return d.manager.observeGatewayV2RecoveryTopology(ctx, source, state, journal)
}

func (d managerGatewayV2UpgradeDriver) hostPreflight(ctx context.Context, profile gatewayProfileBinding, plan gatewayV2NetworkPlan) error {
	return gatewayV2HostPreflight(ctx, d.manager, profile, plan)
}

func (managerGatewayV2UpgradeDriver) selectedInterfacePreflight(profile gatewayProfileBinding) error {
	return gatewayV2SelectedInterfacePreflight(profile)
}

func (d managerGatewayV2UpgradeDriver) pinnedImage(ctx context.Context) (string, error) {
	image, found, err := d.manager.inspectImage(ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		return "", gatewayV2StageError(ctx)
	}
	return image.ID, nil
}

func (d managerGatewayV2UpgradeDriver) createIngressNetwork(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) (string, error) {
	args, err := gatewayV2NetworkCreateArgs(state, journal)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	clearResult(&result)
	if err != nil {
		return "", err
	}
	network, id, found, err := d.manager.inspectNamedGatewayNetwork(ctx, state.Identity.IngressNetwork)
	defer clearCaddyNetworkInspection(&network)
	if err != nil || !found || !validContainerID(id) || !validGatewayV2IngressNetwork(state, journal, network, true, "", "") {
		return "", gatewayV2StageError(ctx)
	}
	return id, nil
}

func (d managerGatewayV2UpgradeDriver) createVolume(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal, role string) (gatewayV1VolumeIdentity, error) {
	args, err := gatewayV2VolumeCreateArgs(state, journal, role)
	if err != nil {
		return gatewayV1VolumeIdentity{}, err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	clearResult(&result)
	if err != nil {
		return gatewayV1VolumeIdentity{}, err
	}
	name := state.Identity.ConfigVolume
	if role == gatewayV2DataVolumeRole {
		name = state.Identity.DataVolume
	}
	volume, identity, found, err := d.manager.inspectNamedVolumeWithIdentity(ctx, name)
	if err != nil || !found || !validGatewayVolumeResourceIdentity(identity) || !validGatewayV2Volume(state, journal, volume, true, role) {
		return gatewayV1VolumeIdentity{}, gatewayV2StageError(ctx)
	}
	return identity, nil
}

func (d managerGatewayV2UpgradeDriver) createStageContainer(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) (string, error) {
	args, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2StageContainerRole, "sha256:"+journal.Resources.ImageID)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	clearResult(&result)
	if err != nil {
		return "", err
	}
	container, runtime, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || !validGatewayV2StoppedContainer(state, journal, container, runtime, true, gatewayV2StageContainerRole, "sha256:"+journal.Resources.ImageID) {
		return "", gatewayV2StageError(ctx)
	}
	return container.ID, nil
}

func (d managerGatewayV2UpgradeDriver) copyStageConfig(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal, contents []byte) error {
	defer clear(contents)
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(container.ID) != journal.Resources.StageContainerID || container.Running || container.Restarting {
		return gatewayV2StageError(ctx)
	}
	if len(contents) == 0 || len(contents) > gatewayV2MaxConfigBytes {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	workingDirectoryGuard, ok := d.manager.acquireWorkingDirectoryGuard()
	if !ok {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	defer workingDirectoryGuard.Close()
	file, err := os.CreateTemp(d.manager.options.WorkingDirectory, ".rig-caddy-v2-stage-*.json")
	if err != nil {
		return gatewayV2StageError(ctx)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return gatewayV2StageError(ctx)
	}
	if _, err := file.Write(contents); err != nil || file.Sync() != nil {
		file.Close()
		return gatewayV2StageError(ctx)
	}
	created, err := file.Stat()
	if err != nil || !created.Mode().IsRegular() || file.Close() != nil {
		file.Close()
		return gatewayV2StageError(ctx)
	}
	installed, err := os.Lstat(path)
	if err != nil || !installed.Mode().IsRegular() || installed.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(path) ||
		!os.SameFile(created, installed) || !d.manager.validWorkingDirectory() {
		return gatewayV2StageError(ctx)
	}
	if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "cp", path, container.ID+":/config/"+state.Identity.StageConfigFilename); err != nil {
		return gatewayV2StageError(ctx)
	}
	return nil
}

func (d managerGatewayV2UpgradeDriver) readStageRestartConfig(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) ([]byte, error) {
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(container.ID) != journal.Resources.StageContainerID || container.Running || container.Restarting {
		return nil, gatewayV2StageError(ctx)
	}
	return d.manager.inspectStoppedCaddyRestartConfig(ctx, container.ID, state.Identity.StageConfigFilename)
}

func (d managerGatewayV2UpgradeDriver) attestStoppedStage(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	return d.manager.observeGatewayV2RecoveryTopology(ctx, source, state, journal) == gatewayV2RecoveryStageIntentStoppedStage
}

func (d managerGatewayV2UpgradeDriver) attestStoppedStageForCompensation(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	return d.manager.observeGatewayV2StoppedStageForCompensation(ctx, source, state, journal)
}

func (d managerGatewayV2UpgradeDriver) startStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if !validGatewayV2RouteState(state) || journal.Phase != gatewayPhaseStageIntent || !validSHA256(journal.Resources.StageContainerID) {
		return gatewayV2StageError(ctx)
	}
	return d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "start", journal.Resources.StageContainerID)
}

func (d managerGatewayV2UpgradeDriver) stopStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(container.ID) != journal.Resources.StageContainerID {
		return gatewayV2StageError(ctx)
	}
	if container.Running || container.Restarting {
		if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "stop", "--time", "10", container.ID); err != nil {
			return err
		}
	}
	confirmed, runtime, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(confirmed.ID) != journal.Resources.StageContainerID || confirmed.Running || confirmed.Restarting ||
		!validGatewayContainerRuntime(runtime, false) || gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings) {
		return gatewayV2StageError(ctx)
	}
	return nil
}

func (d managerGatewayV2UpgradeDriver) removeStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || !found || normalizeID(container.ID) != journal.Resources.StageContainerID || container.Running || container.Restarting {
		return gatewayV2StageError(ctx)
	}
	if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "rm", container.ID); err != nil {
		return err
	}
	_, _, found, err = d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil || found {
		return gatewayV2StageError(ctx)
	}
	return nil
}

var _ gatewayV2UpgradeDriver = managerGatewayV2UpgradeDriver{}
