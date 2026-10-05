package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// InspectGatewayRebindPreparedDockerPredecessor attests exactly one prepared
// rebind claim against the committed protected and Docker predecessor. It is
// read-only and does not probe the predecessor's old host publication or
// forward requests to hosted applications. Its
// result must be repeated under the effects lease before any successor effect.
func (m *Manager) InspectGatewayRebindPreparedDockerPredecessor(ctx context.Context,
	repository *appaccess.Repository,
) error {
	return m.inspectGatewayRebindPreparedDockerPredecessor(ctx, repository, nil, m.inspectGatewayRebindDocker)
}

func (m *Manager) inspectGatewayRebindPreparedDockerPredecessor(ctx context.Context,
	repository *appaccess.Repository, checkpoint func(), inspectDocker gatewayRebindDockerInspector,
) (resultErr error) {
	if m == nil || ctx == nil || repository == nil || inspectDocker == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGatewayRaw(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)

	firstDatabase, err := repository.GatewayRebindStartupSnapshot(ctx)
	if err != nil || len(firstDatabase.Claims) != 1 {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	firstStore, firstState, firstJournal, committed, err := m.committedV2Locked()
	if err != nil || !committed || firstStore == nil ||
		!gatewayRebindPredecessorMatches(firstDatabase, firstState, firstJournal) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	firstArtifacts, err := readGatewayHistorySnapshot(m.store)
	if err != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	firstSource, err := m.store.load()
	if err != nil || ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	firstDocker, err := inspectDocker(ctx, firstSource, firstState, firstJournal)
	defer clearGatewayV2DockerObservation(&firstDocker)
	if err != nil || ctx.Err() != nil ||
		!validGatewayRebindPredecessorDocker(firstSource, firstState, firstJournal, firstDocker) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	if ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}

	secondDatabase, err := repository.GatewayRebindStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(firstDatabase, secondDatabase) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	secondStore, secondState, secondJournal, committed, err := m.committedV2Locked()
	if err != nil || !committed || secondStore == nil ||
		!sameGatewayV2PreparationAbortStore(firstStore, secondStore) ||
		!reflect.DeepEqual(firstState, secondState) || !reflect.DeepEqual(firstJournal, secondJournal) ||
		!gatewayRebindPredecessorMatches(secondDatabase, secondState, secondJournal) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	secondArtifacts, err := readGatewayHistorySnapshot(m.store)
	if err != nil || !sameGatewayHistorySnapshot(firstArtifacts, secondArtifacts) || ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	secondSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(firstSource, secondSource) || ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	secondDocker, err := inspectDocker(ctx, secondSource, secondState, secondJournal)
	defer clearGatewayV2DockerObservation(&secondDocker)
	if err != nil || ctx.Err() != nil ||
		!validGatewayRebindPredecessorDocker(secondSource, secondState, secondJournal, secondDocker) ||
		!sameGatewayRebindPredecessorDockerObservation(firstDocker, secondDocker, secondState.Identity) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}

	// SQLite and protected files can change while Docker is being inspected.
	finalDatabase, err := repository.GatewayRebindStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(firstDatabase, finalDatabase) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	finalStore, finalState, finalJournal, committed, err := m.committedV2Locked()
	if err != nil || !committed || finalStore == nil ||
		!sameGatewayV2PreparationAbortStore(firstStore, finalStore) ||
		!reflect.DeepEqual(firstState, finalState) || !reflect.DeepEqual(firstJournal, finalJournal) ||
		!gatewayRebindPredecessorMatches(finalDatabase, finalState, finalJournal) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	finalArtifacts, err := readGatewayHistorySnapshot(m.store)
	if err != nil || !sameGatewayHistorySnapshot(firstArtifacts, finalArtifacts) || ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	finalSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(firstSource, finalSource) || ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	return nil
}
