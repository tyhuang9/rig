package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
)

const gatewayV2RollbackRetirementMaximumSteps = 4

// gatewayV2RollbackRetirementObservation is emitted only after one complete,
// stable Docker inventory proves the exact journal-bound v1 gateway is serving
// and restartable, both v2 containers are absent, and every remaining v2
// infrastructure object is the exact idle object bound in the journal.
type gatewayV2RollbackRetirementObservation struct {
	IngressNetworkPresent bool
	ConfigVolumePresent   bool
	DataVolumePresent     bool
}

func (o gatewayV2RollbackRetirementObservation) complete() bool {
	return !o.IngressNetworkPresent && !o.ConfigVolumePresent && !o.DataVolumePresent
}

// gatewayV2RollbackRetirementDriver isolates the read-only full topology
// proof and the three narrowly authorized Docker deletions. Every deletion
// adapter must immediately reinspect immutable identity before issuing a
// non-force removal.
type gatewayV2RollbackRetirementDriver interface {
	observeRollbackRetirement(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2RollbackRetirementObservation, error)
	removeRollbackIngressNetwork(context.Context, gatewayV2RouteState, gatewayMigrationJournal) error
	removeRollbackVolume(context.Context, gatewayV2RouteState, gatewayMigrationJournal, string) error
}

// FinalizeGatewayV2Rollback explicitly retires the latest exact rolled-back
// operation. It is never called by startup recovery. Exact replay remains
// read-only and still requires a fresh current-v1 serving proof.
func (m *Manager) FinalizeGatewayV2Rollback(ctx context.Context, operationID string) (resultErr error) {
	if m == nil || ctx == nil || !validCanonicalUUID(operationID) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	selection, err := m.selectGatewayUpgradeGenerationLocked(operationID)
	if err != nil || !selection.Existing || selection.Store == nil || selection.Journal.Phase != gatewayPhaseRolledBack {
		return gatewayV2RollbackRetirementError(ctx)
	}
	return m.finalizeGatewayV2RollbackLocked(ctx, selection.Store, selection.State, selection.Journal)
}

// finalizeGatewayV2RollbackLocked retires one terminal rolled-back generation.
// The caller must hold both gateway writer locks. It intentionally removes at
// most one resource between complete observations so a crash, Docker error, or
// replacement object can never authorize the next deletion or the receipt.
func (m *Manager) finalizeGatewayV2RollbackLocked(ctx context.Context, store *gatewayUpgradeStateStore,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) error {
	if m == nil || ctx == nil || store == nil || journal.Phase != gatewayPhaseRolledBack ||
		state.OperationID != journal.OperationID || !historicallyBoundGatewayUpgrade(state, journal) {
		return gatewayV2RollbackRetirementError(ctx)
	}
	_, receiptErr := store.loadRollbackRetirementReceipt(state, journal)
	receiptInstalled := receiptErr == nil
	// An existing but invalid protected receipt is evidence drift. Never clean
	// resources and then overwrite or reinterpret it.
	if _, err := os.Lstat(store.receiptPath); !receiptInstalled && (err == nil || !errors.Is(err, os.ErrNotExist)) {
		return gatewayV2RollbackRetirementError(ctx)
	}
	if !m.gatewayV2RollbackRetirementSelectionMatches(store, state, journal, receiptInstalled) {
		return gatewayV2RollbackRetirementError(ctx)
	}

	driver := m.gatewayV2RollbackRetirementDriver()
	if driver == nil {
		return gatewayV2RollbackRetirementError(ctx)
	}
	retireCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()

	for step := 0; step < gatewayV2RollbackRetirementMaximumSteps; step++ {
		if !m.gatewayV2RollbackRetirementSelectionMatches(store, state, journal, receiptInstalled) {
			return gatewayV2RollbackRetirementError(ctx)
		}
		source, err := m.store.load()
		if err != nil {
			return gatewayV2RollbackRetirementError(ctx)
		}
		observation, err := driver.observeRollbackRetirement(retireCtx, source, state, journal)
		if err != nil {
			return gatewayV2RollbackRetirementError(ctx)
		}
		confirmedSource, err := m.store.load()
		if err != nil || !reflect.DeepEqual(source, confirmedSource) {
			return gatewayV2RollbackRetirementError(ctx)
		}
		if receiptInstalled {
			// A receipt proves the original retirement instant, but the controller
			// may still need to release its durable claim after a crash. Reattest
			// the currently protected v1 route set and complete v2 absence before
			// reporting rolled_back. Do not mutate Docker after retirement.
			if !observation.complete() {
				return gatewayV2RollbackRetirementError(ctx)
			}
			if !m.gatewayV2RollbackRetirementSelectionMatches(store, state, journal, true) {
				return gatewayV2RollbackRetirementError(ctx)
			}
			return nil
		}
		if observation.complete() {
			if _, err := store.installRollbackRetirementReceipt(state, journal); err != nil {
				return gatewayV2RollbackRetirementError(ctx)
			}
			if _, err := store.loadRollbackRetirementReceipt(state, journal); err != nil {
				return gatewayV2RollbackRetirementError(ctx)
			}
			if !m.gatewayV2RollbackRetirementSelectionMatches(store, state, journal, true) {
				return gatewayV2RollbackRetirementError(ctx)
			}
			return nil
		}

		var mutationErr error
		switch {
		case observation.IngressNetworkPresent:
			mutationErr = driver.removeRollbackIngressNetwork(retireCtx, state, journal)
		case observation.ConfigVolumePresent:
			mutationErr = driver.removeRollbackVolume(retireCtx, state, journal, gatewayV2ConfigVolumeRole)
		case observation.DataVolumePresent:
			mutationErr = driver.removeRollbackVolume(retireCtx, state, journal, gatewayV2DataVolumeRole)
		}
		if mutationErr != nil {
			// Docker errors are ambiguous: the command may have taken effect.
			// Stop now and require a fresh explicit invocation to reobserve.
			return gatewayV2RollbackRetirementError(ctx)
		}
	}
	return gatewayV2RollbackRetirementError(ctx)
}

func (m *Manager) gatewayV2RollbackRetirementSelectionMatches(store *gatewayUpgradeStateStore,
	state gatewayV2RouteState, journal gatewayMigrationJournal, retired bool,
) bool {
	selection, err := m.selectGatewayUpgradeGenerationLocked(journal.OperationID)
	return err == nil && selection.Existing && selection.Retired == retired && selection.Store != nil &&
		selection.Generation == store.generation && selection.Store.v2Path == store.v2Path &&
		selection.Store.journalPath == store.journalPath && selection.Store.receiptPath == store.receiptPath &&
		reflect.DeepEqual(selection.State, state) && reflect.DeepEqual(selection.Journal, journal)
}

func (m *Manager) gatewayV2RollbackRetirementDriver() gatewayV2RollbackRetirementDriver {
	if m != nil && m.gatewayV2CoordinatorDriver != nil {
		if driver, ok := m.gatewayV2CoordinatorDriver.(gatewayV2RollbackRetirementDriver); ok {
			return driver
		}
	}
	return managerGatewayV2CoordinatorDriver{manager: m}
}

func gatewayV2RollbackRetirementError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

func (d managerGatewayV2CoordinatorDriver) observeRollbackRetirement(ctx context.Context, source routeState,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) (gatewayV2RollbackRetirementObservation, error) {
	if d.manager == nil || ctx == nil {
		return gatewayV2RollbackRetirementObservation{}, gatewayV2RollbackRetirementError(ctx)
	}
	proofState, proofJournal, err := gatewayV2CurrentV1RetirementBindings(source, state, journal)
	if err != nil {
		return gatewayV2RollbackRetirementObservation{}, gatewayV2RollbackRetirementError(ctx)
	}
	observed, err := d.manager.inspectGatewayV2Docker(ctx, source, proofState, proofJournal)
	if err != nil {
		clearGatewayV2DockerObservation(&observed)
		return gatewayV2RollbackRetirementObservation{}, gatewayV2RollbackRetirementError(ctx)
	}
	defer clearGatewayV2DockerObservation(&observed)
	identityDigest, err := gatewayV1ObservedIdentityDigest(observed)
	if err != nil {
		return gatewayV2RollbackRetirementObservation{}, gatewayV2RollbackRetirementError(ctx)
	}
	proofJournal.Source.IdentityDigest = identityDigest
	result, valid := classifyGatewayV2RollbackRetirement(source, proofState, proofJournal, observed)
	if !valid {
		return gatewayV2RollbackRetirementObservation{}, gatewayV2RollbackRetirementError(ctx)
	}
	return result, nil
}

// gatewayV2CurrentV1RetirementBindings keeps the immutable operation identity,
// resource bindings, network plan, and approval while rebinding only the v1
// route snapshot used for a live proof. This lets an exact retirement receipt
// be replayed after legitimate v1 route changes without accepting a stale,
// stopped, or drifted gateway.
func gatewayV2CurrentV1RetirementBindings(source routeState, historicalState gatewayV2RouteState,
	historicalJournal gatewayMigrationJournal,
) (gatewayV2RouteState, gatewayMigrationJournal, error) {
	if !validRouteState(source) || source.Pending != nil || historicalJournal.Phase != gatewayPhaseRolledBack ||
		!historicallyBoundGatewayUpgrade(historicalState, historicalJournal) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid generated ingress rollback retirement binding")
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	state := cloneGatewayV2RouteState(historicalState)
	state.SourceV1StateDigest = sourceDigest
	state.Pending = nil
	state.Apps = make(map[string]gatewayV2AppRoute, len(source.Active))
	for appID, route := range cloneRoutes(source.Active) {
		state.Apps[appID] = gatewayV2AppRoute{Route: route}
	}
	if !validGatewayV2RouteState(state) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid current generated ingress v1 retirement state")
	}
	targetDigest, err := canonicalDigest(state)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	journal := historicalJournal
	journal.Source.StateDigest = sourceDigest
	journal.Source.IdentityDigest = ""
	journal.Target.StateDigest = targetDigest
	if !journalMatchesInitialV2State(journal, state) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid current generated ingress v1 retirement journal")
	}
	return state, journal, nil
}

// classifyGatewayV2RollbackRetirement accepts partial cleanup only when each
// remaining object is still the exact journal-bound idle object and the owned
// inventories contain no extras. Historical bindings may remain after their
// objects have been removed.
func classifyGatewayV2RollbackRetirement(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal,
	observed gatewayV2DockerObservation,
) (gatewayV2RollbackRetirementObservation, bool) {
	if journal.Phase != gatewayPhaseRolledBack || !validGatewayTopologyInputs(source, state, journal) ||
		!validGatewayPinnedImage(observed.Image, observed.ImageFound) ||
		!gatewayV2ObservedResourcesMatchJournal(journal, observed) ||
		!validGatewayV1Base(source, journal, observed, true) ||
		!observed.V1Stable || !observed.V1ResourcesStable || !observed.V1EndpointIdentityProven ||
		!observed.V1Container.Running || observed.V1Container.Restarting ||
		!observed.V2ResourcesStable || !observed.StageStable || !observed.FinalStable || !observed.OwnedInventoriesStable ||
		observed.StageContainerFound || observed.FinalContainerFound || len(observed.OwnedContainers) != 0 ||
		len(observed.ApplicationNetworks) != 0 || len(observed.ApplicationNetworkIDs) != 0 {
		return gatewayV2RollbackRetirementObservation{}, false
	}

	result := gatewayV2RollbackRetirementObservation{
		IngressNetworkPresent: observed.IngressFound,
		ConfigVolumePresent:   observed.ConfigVolumeFound,
		DataVolumePresent:     observed.DataVolumeFound,
	}
	expectedNetworks := make([]string, 0, 1)
	if result.IngressNetworkPresent {
		if !validContainerID(observed.IngressNetworkID) || normalizeID(observed.IngressNetworkID) != journal.Resources.IngressNetworkID ||
			!validGatewayV2IngressNetwork(state, journal, observed.IngressNetwork, true, "", "") {
			return gatewayV2RollbackRetirementObservation{}, false
		}
		expectedNetworks = append(expectedNetworks, state.Identity.IngressNetwork)
	}
	if !validOwnedNameSet(observed.OwnedNetworks, expectedNetworks...) {
		return gatewayV2RollbackRetirementObservation{}, false
	}

	expectedVolumes := make([]string, 0, 2)
	if result.ConfigVolumePresent {
		if !validGatewayV2RetirementVolume(state, journal, observed.ConfigVolume, observed.ConfigVolumeIdentity, gatewayV2ConfigVolumeRole) {
			return gatewayV2RollbackRetirementObservation{}, false
		}
		expectedVolumes = append(expectedVolumes, state.Identity.ConfigVolume)
	}
	if result.DataVolumePresent {
		if !validGatewayV2RetirementVolume(state, journal, observed.DataVolume, observed.DataVolumeIdentity, gatewayV2DataVolumeRole) {
			return gatewayV2RollbackRetirementObservation{}, false
		}
		expectedVolumes = append(expectedVolumes, state.Identity.DataVolume)
	}
	if !validOwnedNameSet(observed.OwnedVolumes, expectedVolumes...) {
		return gatewayV2RollbackRetirementObservation{}, false
	}
	return result, true
}

func validGatewayV2RetirementVolume(state gatewayV2RouteState, journal gatewayMigrationJournal,
	volume volumeInspection, identity gatewayV1VolumeIdentity, role string,
) bool {
	if !validGatewayVolumeResourceIdentity(identity) || !validGatewayV2Volume(state, journal, volume, true, role) {
		return false
	}
	bound := journal.Resources.ConfigVolume
	if role == gatewayV2DataVolumeRole {
		bound = journal.Resources.DataVolume
	} else if role != gatewayV2ConfigVolumeRole {
		return false
	}
	observed, err := newGatewayV2VolumeResourceBinding(identity)
	return err == nil && reflect.DeepEqual(observed, bound)
}

func (d managerGatewayV2CoordinatorDriver) removeRollbackIngressNetwork(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	if d.manager == nil || ctx == nil || journal.Phase != gatewayPhaseRolledBack || !validContainerID(journal.Resources.IngressNetworkID) {
		return gatewayV2RollbackRetirementError(ctx)
	}
	network, id, found, err := d.manager.inspectNamedGatewayNetwork(ctx, state.Identity.IngressNetwork)
	if err != nil || !found || normalizeID(id) != journal.Resources.IngressNetworkID ||
		!validGatewayV2IngressNetwork(state, journal, network, true, "", "") {
		clearCaddyNetworkInspection(&network)
		return gatewayV2RollbackRetirementError(ctx)
	}
	clearCaddyNetworkInspection(&network)
	if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "network", "rm", journal.Resources.IngressNetworkID); err != nil {
		return gatewayV2RollbackRetirementError(ctx)
	}
	confirmed, _, confirmedFound, err := d.manager.inspectNamedGatewayNetwork(ctx, state.Identity.IngressNetwork)
	clearCaddyNetworkInspection(&confirmed)
	if err != nil || confirmedFound {
		return gatewayV2RollbackRetirementError(ctx)
	}
	return nil
}

func (d managerGatewayV2CoordinatorDriver) removeRollbackVolume(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, role string,
) error {
	if d.manager == nil || ctx == nil || journal.Phase != gatewayPhaseRolledBack {
		return gatewayV2RollbackRetirementError(ctx)
	}
	name := state.Identity.ConfigVolume
	if role == gatewayV2DataVolumeRole {
		name = state.Identity.DataVolume
	} else if role != gatewayV2ConfigVolumeRole {
		return gatewayV2RollbackRetirementError(ctx)
	}
	volume, identity, found, err := d.manager.inspectNamedVolumeWithIdentity(ctx, name)
	if err != nil || !found || !validGatewayV2RetirementVolume(state, journal, volume, identity, role) {
		return gatewayV2RollbackRetirementError(ctx)
	}
	// Docker volumes have no immutable object ID. Remove by the exact name only
	// after the immediately preceding label, mountpoint, and creation-time proof;
	// omitting --force makes concurrent use fail closed.
	if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "volume", "rm", name); err != nil {
		return gatewayV2RollbackRetirementError(ctx)
	}
	_, _, confirmedFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, name)
	if err != nil || confirmedFound {
		return gatewayV2RollbackRetirementError(ctx)
	}
	return nil
}

var _ gatewayV2RollbackRetirementDriver = managerGatewayV2CoordinatorDriver{}
