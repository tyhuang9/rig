package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

// gatewayV2PreparationAbortDriver performs a purpose-built v1/no-v2 proof.
// It deliberately does not select or invent a v2 network plan.
type gatewayV2PreparationAbortDriver interface {
	attestPreparationAbort(context.Context, routeState, gatewayUpgradePreparation) (string, error)
}

// FinalizeGatewayV2PreparationAbort explicitly records that one approved
// operation stopped before a migration journal or any v2 Docker side effect
// existed. It is never called by startup recovery.
func (m *Manager) FinalizeGatewayV2PreparationAbort(ctx context.Context, request GatewayV2UpgradeRequest) (resultErr error) {
	if m == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	if _, err := gatewayV2UpgradePreparation(request); err != nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	return m.finalizeGatewayV2PreparationAbortLocked(ctx, request)
}

// finalizeGatewayV2PreparationAbortLocked requires both gateway writer locks.
// It accepts only the latest exact generation when that generation is empty,
// contains only its immutable initial state, or already has the exact abort
// receipt. A complete journal belongs to the normal migration recovery path.
func (m *Manager) finalizeGatewayV2PreparationAbortLocked(ctx context.Context, request GatewayV2UpgradeRequest) error {
	preparation, err := gatewayV2UpgradePreparation(request)
	if m == nil || ctx == nil || err != nil {
		return gatewayV2PreparationAbortError(ctx)
	}
	preparation.LocalHostPort = m.options.HostPort

	selection, err := m.resolveGatewayUpgradeGenerationLocked(request.OperationID)
	if err != nil || selection.Store == nil ||
		(selection.Existing && !selection.Aborted) ||
		(selection.PartialState && selection.Aborted) {
		return gatewayV2PreparationAbortError(ctx)
	}
	statePresent := selection.PartialState || (selection.Aborted && !reflect.DeepEqual(selection.State, gatewayV2RouteState{}))
	if statePresent {
		if !gatewayV2PreparationAbortRequestMatchesState(request, selection.State) {
			return gatewayV2PreparationAbortError(ctx)
		}
	}

	var installed gatewayPreJournalAbortReceipt
	if selection.Aborted {
		installed, err = selection.Store.loadPreJournalAbortReceipt()
		if err != nil || !gatewayV2PreparationAbortRequestMatchesReceipt(request, installed) ||
			installed.Source.LocalHostPort != m.options.HostPort {
			return gatewayV2PreparationAbortError(ctx)
		}
	}

	proofCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	source, err := m.store.load()
	if err != nil {
		return gatewayV2PreparationAbortError(ctx)
	}
	driver := m.gatewayV2PreparationAbortDriver()
	if driver == nil {
		return gatewayV2PreparationAbortError(ctx)
	}
	if !selection.Aborted && statePresent {
		sourceDigest, digestErr := canonicalDigest(source)
		if digestErr != nil || sourceDigest != selection.State.SourceV1StateDigest {
			return gatewayV2PreparationAbortError(ctx)
		}
	}
	sourceIdentityDigest, err := driver.attestPreparationAbort(proofCtx, source, preparation)
	if err != nil || !validSHA256(sourceIdentityDigest) {
		return gatewayV2PreparationAbortError(ctx)
	}
	confirmedSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(source, confirmedSource) {
		return gatewayV2PreparationAbortError(ctx)
	}
	confirmedSelection, err := m.resolveGatewayUpgradeGenerationLocked(request.OperationID)
	if err != nil || !sameGatewayV2PreparationAbortSelection(selection, confirmedSelection) {
		return gatewayV2PreparationAbortError(ctx)
	}
	if selection.Aborted {
		// Receipt replay is deliberately read-only. The historical source route
		// digest may differ after a legitimate v1 route change, but this call has
		// just re-proved the currently protected v1 service and complete v2
		// absence under both writer locks.
		return nil
	}

	receipt, err := newGatewayV2PreparationAbortReceipt(selection, request, source, sourceIdentityDigest, m.options.HostPort)
	if err != nil {
		return gatewayV2PreparationAbortError(ctx)
	}
	if _, err := selection.Store.installPreJournalAbortReceipt(receipt); err != nil {
		return gatewayV2PreparationAbortError(ctx)
	}
	loaded, err := selection.Store.loadPreJournalAbortReceipt()
	if err != nil || !reflect.DeepEqual(loaded, receipt) {
		return gatewayV2PreparationAbortError(ctx)
	}
	finalSelection, err := m.resolveGatewayUpgradeGenerationLocked(request.OperationID)
	if err != nil || !finalSelection.Aborted || !sameGatewayV2PreparationAbortStore(selection.Store, finalSelection.Store) ||
		finalSelection.Generation != selection.Generation || !reflect.DeepEqual(finalSelection.State, selection.State) {
		return gatewayV2PreparationAbortError(ctx)
	}
	return nil
}

func newGatewayV2PreparationAbortReceipt(selection gatewayUpgradeGenerationSelection, request GatewayV2UpgradeRequest,
	source routeState, sourceIdentityDigest string, localHostPort uint16,
) (gatewayPreJournalAbortReceipt, error) {
	preparation, err := gatewayV2UpgradePreparation(request)
	if selection.Store == nil || selection.Existing || selection.Aborted || err != nil ||
		!validRouteState(source) || source.Pending != nil || !validSHA256(sourceIdentityDigest) || localHostPort == 0 {
		return gatewayPreJournalAbortReceipt{}, errors.New("invalid generated ingress pre-journal abort")
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil {
		return gatewayPreJournalAbortReceipt{}, err
	}
	identity, err := newGatewayV2Identity(request.OperationID)
	if err != nil {
		return gatewayPreJournalAbortReceipt{}, err
	}
	receipt := gatewayPreJournalAbortReceipt{
		Version: gatewayPreJournalAbortVersion, Generation: selection.Generation,
		OperationID: request.OperationID, Outcome: GatewayV2UpgradeRolledBack, Boundary: gatewayPreJournalAbortBoundary,
		Profile: preparation.Profile,
		Action:  gatewayUpgradeActionRef{Name: gatewayUpgradeActionName, Digest: request.ApprovedActionDigest, ApprovedBy: request.ApprovedBy},
		Source: gatewayMigrationSourceRef{
			Format: stateVersion, StateDigest: sourceDigest, IdentityVersion: gatewayV1IdentityVersion,
			IdentityDigest: sourceIdentityDigest, LocalHostPort: localHostPort,
		},
		V2IdentityDigest: identity.Digest,
	}
	if selection.PartialState {
		if !gatewayV2PreparationAbortRequestMatchesState(request, selection.State) || selection.State.SourceV1StateDigest != sourceDigest {
			return gatewayPreJournalAbortReceipt{}, errors.New("generated ingress pre-journal abort state is stale")
		}
		receipt.InitialStateDigest, err = canonicalDigest(selection.State)
		if err != nil {
			return gatewayPreJournalAbortReceipt{}, err
		}
		receipt.NetworkPlanDigest, err = gatewayV2PlanDigest(selection.State)
		if err != nil {
			return gatewayPreJournalAbortReceipt{}, err
		}
	}
	return receipt, nil
}

func gatewayV2PreparationAbortRequestMatchesState(request GatewayV2UpgradeRequest, state gatewayV2RouteState) bool {
	preparation, err := gatewayV2UpgradePreparation(request)
	identity, identityErr := newGatewayV2Identity(request.OperationID)
	return err == nil && identityErr == nil && validPreJournalAbortInitialState(state) &&
		state.OperationID == request.OperationID && state.Profile == preparation.Profile && state.Identity == identity &&
		state.UpgradeAction == (gatewayUpgradeActionRef{
			Name: gatewayUpgradeActionName, Digest: request.ApprovedActionDigest, ApprovedBy: request.ApprovedBy,
		})
}

func gatewayV2PreparationAbortRequestMatchesReceipt(request GatewayV2UpgradeRequest, receipt gatewayPreJournalAbortReceipt) bool {
	preparation, err := gatewayV2UpgradePreparation(request)
	identity, identityErr := newGatewayV2Identity(request.OperationID)
	return err == nil && identityErr == nil && receipt.Version == gatewayPreJournalAbortVersion &&
		receipt.OperationID == request.OperationID && receipt.Outcome == GatewayV2UpgradeRolledBack &&
		receipt.Boundary == gatewayPreJournalAbortBoundary && receipt.Profile == preparation.Profile &&
		receipt.Action == (gatewayUpgradeActionRef{
			Name: gatewayUpgradeActionName, Digest: request.ApprovedActionDigest, ApprovedBy: request.ApprovedBy,
		}) && receipt.V2IdentityDigest == identity.Digest
}

func sameGatewayV2PreparationAbortSelection(a, b gatewayUpgradeGenerationSelection) bool {
	return a.Existing == b.Existing && a.PartialState == b.PartialState && a.Aborted == b.Aborted &&
		a.Generation == b.Generation && reflect.DeepEqual(a.State, b.State) &&
		sameGatewayV2PreparationAbortStore(a.Store, b.Store)
}

func sameGatewayV2PreparationAbortStore(a, b *gatewayUpgradeStateStore) bool {
	return a != nil && b != nil && a.generation == b.generation && a.operationID == b.operationID &&
		a.v2Path == b.v2Path && a.v2Purpose == b.v2Purpose &&
		a.journalPath == b.journalPath && a.journalPurpose == b.journalPurpose &&
		a.receiptPath == b.receiptPath && a.receiptPurpose == b.receiptPurpose &&
		a.abortPath == b.abortPath && a.abortPurpose == b.abortPurpose
}

func gatewayV2PreparationAbortError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

func (m *Manager) gatewayV2PreparationAbortDriver() gatewayV2PreparationAbortDriver {
	if m != nil && m.gatewayV2CoordinatorDriver != nil {
		if driver, ok := m.gatewayV2CoordinatorDriver.(gatewayV2PreparationAbortDriver); ok {
			return driver
		}
	}
	return managerGatewayV2CoordinatorDriver{manager: m}
}

func (d managerGatewayV2CoordinatorDriver) attestPreparationAbort(ctx context.Context, source routeState,
	preparation gatewayUpgradePreparation,
) (string, error) {
	if d.manager == nil || ctx == nil || !validRouteState(source) || source.Pending != nil ||
		!validCanonicalUUID(preparation.OperationID) || !validGatewayProfileBinding(preparation.Profile) ||
		preparation.LocalHostPort == 0 || !validCanonicalUUID(preparation.ApprovedBy) {
		return "", gatewayV2PreparationAbortError(ctx)
	}
	identity, err := newGatewayV2Identity(preparation.OperationID)
	if err != nil {
		return "", gatewayV2PreparationAbortError(ctx)
	}
	observation, err := d.manager.inspectGatewayV2PreparationAbortDocker(ctx, source, identity)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return "", gatewayV2PreparationAbortError(ctx)
	}
	defer clearGatewayV2DockerObservation(&observation)
	identityDigest, valid := classifyGatewayV2PreparationAbort(source, preparation, observation)
	if !valid {
		return "", gatewayV2PreparationAbortError(ctx)
	}
	return identityDigest, nil
}

func classifyGatewayV2PreparationAbort(source routeState, preparation gatewayUpgradePreparation,
	observation gatewayV2DockerObservation,
) (string, bool) {
	if !validRouteState(source) || source.Pending != nil || !validCanonicalUUID(preparation.OperationID) ||
		!validGatewayProfileBinding(preparation.Profile) || preparation.LocalHostPort == 0 {
		return "", false
	}
	identityDigest, err := gatewayV1ObservedIdentityDigest(observation)
	if err != nil {
		return "", false
	}
	journal := gatewayMigrationJournal{Source: gatewayMigrationSourceRef{
		IdentityDigest: identityDigest, LocalHostPort: preparation.LocalHostPort,
	}}
	valid := validGatewayPinnedImage(observation.Image, observation.ImageFound) &&
		validGatewayV1Base(source, journal, observation, true) &&
		observation.V1Stable && observation.V1ResourcesStable && observation.V1EndpointIdentityProven &&
		observation.V1Container.Running && !observation.V1Container.Restarting &&
		observation.V2ResourcesStable && observation.StageStable && observation.FinalStable &&
		observation.OwnedInventoriesStable && gatewayV2SourceAttestationHasNoV2Resources(observation)
	return identityDigest, valid
}

// inspectGatewayV2PreparationAbortDocker collects only the observations needed
// to prove current v1 service and complete deterministic/owned v2 absence. It
// never synthesizes a target network plan merely to reuse the migration
// observer.
func (m *Manager) inspectGatewayV2PreparationAbortDocker(ctx context.Context, source routeState,
	identity gatewayV2Identity,
) (gatewayV2DockerObservation, error) {
	var observation gatewayV2DockerObservation
	if m == nil || ctx == nil || !validRouteState(source) || source.Pending != nil ||
		!validGatewayV2Identity(strings.TrimPrefix(identity.StageContainer, gatewayV2StageContainerBase), identity) {
		return observation, gatewayV2PreparationAbortError(ctx)
	}
	var err error
	observation.Image, observation.ImageFound, err = m.inspectImage(ctx)
	if err != nil {
		return observation, err
	}
	observation.V1Container, observation.V1Runtime, observation.V1ContainerFound, err = m.inspectNamedGatewayContainer(ctx, caddyContainerName)
	if err != nil {
		return observation, err
	}
	observation.V1Volume, observation.V1VolumeIdentity, observation.V1VolumeFound, err = m.inspectNamedVolumeWithIdentity(ctx, caddyVolumeName)
	if err != nil {
		return observation, err
	}
	observation.V1Network, observation.V1NetworkID, observation.V1NetworkFound, err = m.inspectNamedGatewayNetwork(ctx, caddyNetworkName)
	if err != nil {
		return observation, err
	}
	_, _, observation.ConfigVolumeFound, err = m.inspectNamedVolumeWithIdentity(ctx, identity.ConfigVolume)
	if err != nil {
		return observation, err
	}
	_, _, observation.DataVolumeFound, err = m.inspectNamedVolumeWithIdentity(ctx, identity.DataVolume)
	if err != nil {
		return observation, err
	}
	_, _, observation.IngressFound, err = m.inspectNamedGatewayNetwork(ctx, identity.IngressNetwork)
	if err != nil {
		return observation, err
	}
	_, _, observation.StageContainerFound, err = m.inspectNamedGatewayContainer(ctx, identity.StageContainer)
	if err != nil {
		return observation, err
	}
	_, _, observation.FinalContainerFound, err = m.inspectNamedGatewayContainer(ctx, identity.FinalContainer)
	if err != nil {
		return observation, err
	}
	observation.OwnedContainers, err = m.inspectGatewayV2OwnedNames(ctx, "container", "ls", "--all")
	if err != nil {
		return observation, err
	}
	observation.OwnedVolumes, err = m.inspectGatewayV2OwnedNames(ctx, "volume", "ls")
	if err != nil {
		return observation, err
	}
	observation.OwnedNetworks, err = m.inspectGatewayV2OwnedNames(ctx, "network", "ls")
	if err != nil {
		return observation, err
	}

	if observation.V1ContainerFound {
		if observation.V1Container.Running && !observation.V1Container.Restarting {
			observation.V1Config, err = m.inspectLiveCaddyConfig(ctx, observation.V1Container.ID)
			if err != nil {
				return observation, err
			}
		}
		observation.V1RestartConfig, err = m.inspectStoppedCaddyRestartConfig(ctx, observation.V1Container.ID, gatewayV2ActiveConfigFile)
		if err != nil {
			return observation, err
		}
	}
	owners, valid := gatewayRouteNetworkOwners(source.Active)
	if !valid {
		return observation, errors.New("invalid generated ingress v1 application networks")
	}
	observation.V1ApplicationNetworks = make(map[string]caddyNetworkInspection, len(owners))
	observation.V1ApplicationNetworkIDs = make(map[string]string, len(owners))
	for name := range owners {
		full, id, found, inspectErr := m.inspectNamedGatewayNetwork(ctx, name)
		if inspectErr != nil || !found {
			return observation, errors.New("generated ingress v1 application network is unavailable")
		}
		observation.V1ApplicationNetworks[name] = full
		observation.V1ApplicationNetworkIDs[name] = id
	}
	endpointIdentityBefore, err := m.inspectGatewayEndpointIdentitySnapshot(ctx, source.Active, observation.V1ApplicationNetworks)
	if err != nil {
		return observation, err
	}

	v1ConfigStable := true
	if observation.V1ContainerFound {
		var confirmedLive []byte
		if observation.V1Container.Running && !observation.V1Container.Restarting {
			confirmedLive, err = m.inspectLiveCaddyConfig(ctx, observation.V1Container.ID)
			if err != nil {
				return observation, err
			}
		}
		confirmedRestart, inspectErr := m.inspectStoppedCaddyRestartConfig(ctx, observation.V1Container.ID, gatewayV2ActiveConfigFile)
		if inspectErr != nil {
			clear(confirmedLive)
			return observation, inspectErr
		}
		v1ConfigStable = sameOptionalCaddyConfig(observation.V1Config, confirmedLive) &&
			sameOptionalCaddyConfig(observation.V1RestartConfig, confirmedRestart)
		clear(confirmedLive)
		clear(confirmedRestart)
	}
	confirmedV1, confirmedV1Runtime, confirmedV1Found, err := m.inspectNamedGatewayContainer(ctx, caddyContainerName)
	if err != nil {
		return observation, err
	}
	observation.V1Stable = v1ConfigStable && observation.V1ContainerFound == confirmedV1Found && (!confirmedV1Found ||
		(reflect.DeepEqual(observation.V1Container, confirmedV1) && reflect.DeepEqual(observation.V1Runtime, confirmedV1Runtime)))
	observation.V1ResourcesStable = m.confirmGatewayV1Resources(ctx, observation)
	observation.V2ResourcesStable, observation.StageStable, observation.FinalStable =
		m.confirmGatewayV2PreparationAbortAbsence(ctx, identity)
	observation.OwnedInventoriesStable = m.confirmGatewayV2OwnedInventories(ctx, observation)
	confirmedNetworks, err := m.reinspectGatewayApplicationNetworks(ctx, observation.V1ApplicationNetworks, observation.V1ApplicationNetworkIDs)
	if err != nil {
		return observation, err
	}
	endpointIdentityAfter, err := m.inspectGatewayEndpointIdentitySnapshot(ctx, source.Active, confirmedNetworks)
	if err != nil {
		return observation, err
	}
	observation.V1EndpointIdentityProven = endpointIdentityBefore == endpointIdentityAfter
	return observation, nil
}

func (m *Manager) confirmGatewayV2PreparationAbortAbsence(ctx context.Context, identity gatewayV2Identity) (resources, stage, final bool) {
	_, _, configFound, configErr := m.inspectNamedVolumeWithIdentity(ctx, identity.ConfigVolume)
	_, _, dataFound, dataErr := m.inspectNamedVolumeWithIdentity(ctx, identity.DataVolume)
	_, _, networkFound, networkErr := m.inspectNamedGatewayNetwork(ctx, identity.IngressNetwork)
	_, _, stageFound, stageErr := m.inspectNamedGatewayContainer(ctx, identity.StageContainer)
	_, _, finalFound, finalErr := m.inspectNamedGatewayContainer(ctx, identity.FinalContainer)
	return configErr == nil && dataErr == nil && networkErr == nil && !configFound && !dataFound && !networkFound,
		stageErr == nil && !stageFound, finalErr == nil && !finalFound
}

var _ gatewayV2PreparationAbortDriver = managerGatewayV2CoordinatorDriver{}
