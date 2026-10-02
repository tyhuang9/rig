package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayV2ProfileBinding binds a gateway upgrade to one exact approved
// profile revision and to the canonical digest of its complete specification.
type GatewayV2ProfileBinding struct {
	RevisionID     string
	RevisionNumber int64
	SpecDigest     string
	SelectedIPv4   string
	InterfaceID    string
	PortStart      uint16
	PortEnd        uint16
}

// GatewayV2UpgradeRequest is the complete authorization and topology binding
// for one generated-ingress v1-to-v2 migration. The controller must construct
// it only from a freshly loaded durable appaccess upgrade claim and the exact
// claimed profile revision. The Manager selects the Docker network under its
// gateway locks; no network value is accepted from this request. None of the
// fields, including ApprovedBy, may be bound directly from an HTTP payload.
type GatewayV2UpgradeRequest struct {
	OperationID          string
	Profile              GatewayV2ProfileBinding
	ApprovedBy           string
	ApprovedActionDigest string
}

// GatewayV2UpgradeOutcome reports only outcomes safe for an authenticated
// caller to persist. Unresolved requires a fresh coordinator invocation.
type GatewayV2UpgradeOutcome string

const (
	GatewayV2UpgradeCommitted  GatewayV2UpgradeOutcome = "committed"
	GatewayV2UpgradeRolledBack GatewayV2UpgradeOutcome = "rolled_back"
	GatewayV2UpgradeUnresolved GatewayV2UpgradeOutcome = "unresolved"
)

// GatewayV2UpgradeResult separates an attested rollback from an ambiguous
// failure without converting any underlying error into success.
type GatewayV2UpgradeResult struct {
	Outcome GatewayV2UpgradeOutcome
}

// gatewayV2CoordinatorDriver isolates the read-only source-v1 identity proof.
// The stage and transfer machines retain their own narrower mutation drivers.
type gatewayV2CoordinatorDriver interface {
	selectNetworkPlan(context.Context, gatewayProfileBinding) (gatewayV2NetworkPlan, error)
	attestSourceV1(context.Context, routeState, gatewayUpgradePreparation) (string, error)
}

// UpgradeGatewayV2 prepares, stages, transfers, and finally reattests one
// approved gateway migration while holding the in-process and cross-process
// gateway writer locks. Only the same exact operation binding can resume.
//
// This method validates the canonical claim values but deliberately does not
// read the appaccess database. Its authenticated controller caller must load
// and recheck the durable claim before invoking it and must persist this
// method's outcome afterward. It must never be exposed as a direct HTTP model.
func (m *Manager) UpgradeGatewayV2(ctx context.Context, request GatewayV2UpgradeRequest,
	authorize GatewayV2UpgradeAuthorizer,
) (result GatewayV2UpgradeResult, resultErr error) {
	preparation, err := gatewayV2UpgradePreparation(request)
	if m == nil || ctx == nil || err != nil || authorize == nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, err
	}
	defer func() {
		if err := release(); err != nil {
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
			result.Outcome = GatewayV2UpgradeUnresolved
		}
	}()
	if authorizeGatewayV2Upgrade(ctx, request, authorize) != nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}
	preparation.LocalHostPort = m.options.HostPort
	mutationGate := &gatewayV2MutationAuthorizationGate{request: request, authorize: authorize}

	selection, err := m.resolveGatewayUpgradeGenerationLocked(request.OperationID)
	if err != nil || selection.Store == nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}
	store := selection.Store
	if selection.Aborted {
		failure := gatewayV2CoordinatorError(ctx)
		if m.finalizeGatewayV2PreparationAbortLocked(ctx, request) == nil {
			return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeRolledBack}, failure
		}
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, failure
	}
	stageDriver := m.gatewayV2UpgradeDriver
	if stageDriver == nil {
		stageDriver = managerGatewayV2UpgradeDriver{manager: m}
	}
	stageDriver = authorizedGatewayV2UpgradeDriver{gatewayV2UpgradeDriver: stageDriver, gate: mutationGate}
	transferDriver := m.gatewayV2TransferDriver
	if transferDriver == nil {
		transferDriver = managerGatewayV2TransferDriver{manager: m}
	}
	transferDriver = authorizedGatewayV2TransferDriver{gatewayV2TransferDriver: transferDriver, gate: mutationGate}

	state, journal := selection.State, selection.Journal
	if !selection.Existing {
		source, sourceErr := m.store.load()
		if sourceErr != nil {
			return m.gatewayV2PreparationFailureResult(ctx, request, gatewayV2CoordinatorError(ctx))
		}
		coordinatorDriver := m.gatewayV2CoordinatorDriver
		if coordinatorDriver == nil {
			coordinatorDriver = managerGatewayV2CoordinatorDriver{manager: m}
		}
		if selection.PartialState {
			// A protected state create may have installed before reporting a
			// durability failure. Reuse its already-approved network plan; a new
			// selection would make exact recovery impossible and could reinterpret
			// the immutable partial artifact.
			preparation.Network = selection.State.Network
		} else {
			preparation.Network, err = coordinatorDriver.selectNetworkPlan(ctx, preparation.Profile)
			if err != nil {
				return m.gatewayV2PreparationFailureResult(ctx, request, err)
			}
		}
		preparation.SourceIdentityDigest, err = coordinatorDriver.attestSourceV1(ctx, source, preparation)
		if err != nil {
			return m.gatewayV2PreparationFailureResult(ctx, request, err)
		}
		if err := stageDriver.hostPreflight(ctx, preparation.Profile, preparation.Network); err != nil {
			return m.gatewayV2PreparationFailureResult(ctx, request, err)
		}
		state, journal, err = prepareGatewayV2State(source, preparation)
		if err != nil {
			return m.gatewayV2PreparationFailureResult(ctx, request, gatewayV2CoordinatorError(ctx))
		}
		if selection.PartialState && !reflect.DeepEqual(state, selection.State) {
			return m.gatewayV2PreparationFailureResult(ctx, request, gatewayV2CoordinatorError(ctx))
		}
		if err := prepareGatewayV2ProtectedState(store, state, journal); err != nil {
			return m.gatewayV2PreparationFailureResult(ctx, request, gatewayV2CoordinatorError(ctx))
		}
	}
	if !gatewayV2RequestMatchesState(request, state, journal) || journal.Source.LocalHostPort != m.options.HostPort {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}
	if journal.Phase == gatewayPhaseRolledBack {
		if m.finalizeGatewayV2RollbackAuthorizedLocked(ctx, store, state, journal, mutationGate) == nil {
			return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeRolledBack}, gatewayV2CoordinatorError(ctx)
		}
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}
	if journal.Phase == gatewayPhaseUncertain {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}

	if journal.Phase == gatewayPhasePrepared || journal.Phase == gatewayPhaseStageIntent || journal.Phase == gatewayPhaseStaged {
		if err := m.stageGatewayV2Locked(ctx, store, stageDriver, request.OperationID); err != nil {
			if isGatewayV2UpgradeAuthorizationDenied(err) {
				return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
			}
			return m.gatewayV2CoordinatorFailureResult(ctx, store, request.OperationID, mutationGate), err
		}
	}
	if err := m.transferGatewayV2Locked(ctx, store, transferDriver, request.OperationID); err != nil {
		if isGatewayV2UpgradeAuthorizationDenied(err) {
			return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
		}
		return m.gatewayV2CoordinatorFailureResult(ctx, store, request.OperationID, mutationGate), err
	}
	state, journal, err = store.loadBoundUpgrade(request.OperationID)
	if err != nil || !gatewayV2RequestMatchesState(request, state, journal) || journal.Phase != gatewayPhaseCommitted {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}
	source, err := m.store.load()
	if err != nil || transferDriver.observeTopology(ctx, source, state, journal) != gatewayTopologyExactFinalV2 ||
		!transferDriver.proveFinalHostRoutes(ctx, state, journal) || transferDriver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, gatewayV2CoordinatorError(ctx)
	}
	return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeCommitted}, nil
}

func (m *Manager) gatewayV2PreparationFailureResult(ctx context.Context, request GatewayV2UpgradeRequest, original error) (GatewayV2UpgradeResult, error) {
	if original == nil {
		original = gatewayV2CoordinatorError(ctx)
	}
	if m.finalizeGatewayV2PreparationAbortLocked(ctx, request) == nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeRolledBack}, original
	}
	return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}, original
}

func gatewayV2UpgradePreparation(request GatewayV2UpgradeRequest) (gatewayUpgradePreparation, error) {
	profile := gatewayProfileBinding{
		RevisionID: request.Profile.RevisionID, RevisionNumber: request.Profile.RevisionNumber,
		SpecDigest: request.Profile.SpecDigest, SelectedIPv4: request.Profile.SelectedIPv4,
		InterfaceID: request.Profile.InterfaceID, PortStart: request.Profile.PortStart, PortEnd: request.Profile.PortEnd,
	}
	if !validCanonicalUUID(request.OperationID) || !validCanonicalUUID(request.ApprovedBy) ||
		!validGatewayProfileBinding(profile) {
		return gatewayUpgradePreparation{}, errors.New("invalid generated ingress v2 upgrade request")
	}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(appaccess.GatewayProfileSpec{
		SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
	})
	if err != nil || profileDigest != profile.SpecDigest {
		return gatewayUpgradePreparation{}, errors.New("invalid generated ingress v2 profile binding")
	}
	actionDigest, err := gatewayUpgradeActionDigest(profile, gatewayV2IdentityVersion)
	if err != nil || actionDigest != request.ApprovedActionDigest {
		return gatewayUpgradePreparation{}, errors.New("invalid generated ingress v2 upgrade approval")
	}
	return gatewayUpgradePreparation{
		OperationID: request.OperationID, Profile: profile,
		LocalHostPort: mappableLocalHostPortPlaceholder, ApprovedActionDigest: request.ApprovedActionDigest, ApprovedBy: request.ApprovedBy,
	}, nil
}

// The actual host port is Manager-owned and replaces this validation-only
// placeholder immediately after the gateway lock is acquired.
const mappableLocalHostPortPlaceholder = uint16(1)

func gatewayV2RequestMatchesState(request GatewayV2UpgradeRequest, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	preparation, err := gatewayV2UpgradePreparation(request)
	if err != nil {
		return false
	}
	return state.OperationID == request.OperationID && journal.OperationID == request.OperationID &&
		state.Profile == preparation.Profile && journal.Profile == preparation.Profile &&
		state.UpgradeAction == (gatewayUpgradeActionRef{Name: gatewayUpgradeActionName, Digest: request.ApprovedActionDigest, ApprovedBy: request.ApprovedBy}) &&
		journal.UpgradeAction == state.UpgradeAction
}

func prepareGatewayV2ProtectedState(store *gatewayUpgradeStateStore, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	installedState, stateErr := store.loadV2State()
	if stateErr == nil {
		if !reflect.DeepEqual(installedState, state) {
			return errors.New("generated ingress v2 state belongs to another operation")
		}
	} else if err := store.createV2State(state); err != nil {
		// A protected create may have installed before reporting a durability
		// failure. Never continue in that invocation; a fresh call must reload it.
		return err
	}

	installedJournal, journalErr := store.loadMigrationJournal()
	if journalErr == nil {
		if !reflect.DeepEqual(installedJournal, journal) {
			return errors.New("generated ingress migration journal belongs to another operation")
		}
	} else if err := store.createMigrationJournal(journal); err != nil {
		return err
	}
	installedState, installedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(installedState, state) || !reflect.DeepEqual(installedJournal, journal) {
		return errors.New("generated ingress protected preparation is unresolved")
	}
	return nil
}

func (m *Manager) gatewayV2CoordinatorFailureResult(ctx context.Context, store *gatewayUpgradeStateStore, operationID string,
	mutationGate *gatewayV2MutationAuthorizationGate,
) GatewayV2UpgradeResult {
	state, journal, err := store.loadHistoricalBoundUpgrade(operationID)
	if err == nil && journal.Phase == gatewayPhaseRolledBack &&
		m.finalizeGatewayV2RollbackAuthorizedLocked(ctx, store, state, journal, mutationGate) == nil {
		return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeRolledBack}
	}
	return GatewayV2UpgradeResult{Outcome: GatewayV2UpgradeUnresolved}
}

func gatewayV2CoordinatorError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

type managerGatewayV2CoordinatorDriver struct{ manager *Manager }

func (d managerGatewayV2CoordinatorDriver) selectNetworkPlan(ctx context.Context, profile gatewayProfileBinding) (gatewayV2NetworkPlan, error) {
	return gatewayV2SelectNetworkPlan(ctx, d.manager, profile)
}

func (d managerGatewayV2CoordinatorDriver) attestSourceV1(ctx context.Context, source routeState, input gatewayUpgradePreparation) (string, error) {
	if d.manager == nil || ctx == nil {
		return "", gatewayV2CoordinatorError(ctx)
	}
	probeInput := input
	probeInput.SourceIdentityDigest = strings.Repeat("0", 64)
	state, journal, err := prepareGatewayV2State(source, probeInput)
	if err != nil {
		return "", gatewayV2CoordinatorError(ctx)
	}
	observation, err := d.manager.inspectGatewayV2Docker(ctx, source, state, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return "", gatewayV2CoordinatorError(ctx)
	}
	defer clearGatewayV2DockerObservation(&observation)
	identityDigest, err := gatewayV1ObservedIdentityDigest(observation)
	if err != nil {
		return "", gatewayV2CoordinatorError(ctx)
	}
	journal.Source.IdentityDigest = identityDigest
	if !validGatewayMigrationJournal(journal) || classifyGatewayV2Topology(source, state, journal, observation) != gatewayTopologyExactV1Only ||
		!gatewayV2SourceAttestationHasNoV2Resources(observation) {
		return "", gatewayV2CoordinatorError(ctx)
	}
	return identityDigest, nil
}

// gatewayV2SourceAttestationHasNoV2Resources distinguishes a truly fresh or
// state-only preparation from a rolled-back topology that retained exact idle
// infrastructure. Before the migration journal exists, no Docker side effect
// is authorized, so even correctly labelled idle v2 resources fail closed.
func gatewayV2SourceAttestationHasNoV2Resources(observation gatewayV2DockerObservation) bool {
	return !observation.ConfigVolumeFound && !observation.DataVolumeFound && !observation.IngressFound &&
		!observation.StageContainerFound && !observation.FinalContainerFound &&
		len(observation.OwnedContainers) == 0 && len(observation.OwnedVolumes) == 0 && len(observation.OwnedNetworks) == 0
}

var _ gatewayV2CoordinatorDriver = managerGatewayV2CoordinatorDriver{}
