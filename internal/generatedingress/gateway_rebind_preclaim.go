package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// InspectGatewayRebindPreclaimPredecessor compares an unpersisted, approved
// proposal with the current SQLite and protected v2 predecessor. It requires
// zero rebind claims and never writes a claim or causes a Docker effect. A
// future writer must repeat this proof under its effects lease and in the
// claim-insert transaction, then attest the inserted claim before effects.
func (m *Manager) InspectGatewayRebindPreclaimPredecessor(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
) error {
	return m.inspectGatewayRebindPreclaimPredecessor(ctx, repository, proposal, nil)
}

func (m *Manager) inspectGatewayRebindPreclaimPredecessor(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
	checkpoint func(),
) (resultErr error) {
	return m.inspectGatewayRebindPreclaimWithDocker(ctx, repository, proposal, checkpoint, nil)
}

type gatewayRebindDockerInspector func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error)

// InspectGatewayRebindPreclaimDockerPredecessor adds exact, journal-bound Docker
// ownership to the zero-claim protected/SQLite proof. It deliberately skips
// host publication probes: the predecessor LAN address may no longer exist.
// This read-only result is advisory until repeated under the writer's effects
// lease and claim-insert transaction.
func (m *Manager) InspectGatewayRebindPreclaimDockerPredecessor(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
) error {
	return m.inspectGatewayRebindPreclaimWithDocker(ctx, repository, proposal, nil, m.inspectGatewayRebindDocker)
}

func (m *Manager) inspectGatewayRebindDocker(ctx context.Context, source routeState,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) (gatewayV2DockerObservation, error) {
	return m.inspectGatewayV2DockerWithStageConfig(ctx, source, state, journal, true, false)
}

func (m *Manager) inspectGatewayRebindPreclaimWithDocker(ctx context.Context,
	repository *appaccess.Repository, proposal appaccess.GatewayRebindPreclaimProposal,
	checkpoint func(), inspectDocker gatewayRebindDockerInspector,
) (resultErr error) {
	if m == nil || ctx == nil || repository == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGatewayRaw(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)

	firstDatabase, err := repository.GatewayRebindPreclaimSnapshot(ctx, proposal)
	if err != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	firstStore, firstState, firstJournal, committed, err := m.committedV2Locked()
	if err != nil || !committed || firstStore == nil ||
		!gatewayRebindPreclaimMatches(firstDatabase, firstState, firstJournal) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	firstArtifacts, err := readGatewayHistorySnapshot(m.store)
	if err != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	var firstSource routeState
	var firstDocker gatewayV2DockerObservation
	if inspectDocker != nil {
		firstSource, err = m.store.load()
		if err != nil {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
		firstDocker, err = inspectDocker(ctx, firstSource, firstState, firstJournal)
		defer clearGatewayV2DockerObservation(&firstDocker)
		if err != nil || !validGatewayRebindPredecessorDocker(firstSource, firstState, firstJournal, firstDocker) {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
	}
	if checkpoint != nil {
		checkpoint()
	}
	if ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}

	secondDatabase, err := repository.GatewayRebindPreclaimSnapshot(ctx, proposal)
	if err != nil || !reflect.DeepEqual(firstDatabase, secondDatabase) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	secondStore, secondState, secondJournal, committed, err := m.committedV2Locked()
	if err != nil || !committed || secondStore == nil ||
		!sameGatewayV2PreparationAbortStore(firstStore, secondStore) ||
		!reflect.DeepEqual(firstState, secondState) || !reflect.DeepEqual(firstJournal, secondJournal) ||
		!gatewayRebindPreclaimMatches(secondDatabase, secondState, secondJournal) {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	secondArtifacts, err := readGatewayHistorySnapshot(m.store)
	if err != nil || !sameGatewayHistorySnapshot(firstArtifacts, secondArtifacts) || ctx.Err() != nil {
		return gatewayRebindPredecessorInspectionError(ctx)
	}
	if inspectDocker != nil {
		secondSource, loadErr := m.store.load()
		if loadErr != nil || !reflect.DeepEqual(firstSource, secondSource) {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
		secondDocker, inspectErr := inspectDocker(ctx, secondSource, secondState, secondJournal)
		defer clearGatewayV2DockerObservation(&secondDocker)
		if inspectErr != nil || ctx.Err() != nil ||
			!validGatewayRebindPredecessorDocker(secondSource, secondState, secondJournal, secondDocker) ||
			!reflect.DeepEqual(firstDocker, secondDocker) {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
		// A protected file or SQLite head may change while the second Docker
		// observation is in flight. Recheck both after that observation, not
		// merely before it, while the gateway locks are still held.
		finalDatabase, snapshotErr := repository.GatewayRebindPreclaimSnapshot(ctx, proposal)
		if snapshotErr != nil || !reflect.DeepEqual(firstDatabase, finalDatabase) {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
		finalArtifacts, historyErr := readGatewayHistorySnapshot(m.store)
		if historyErr != nil || !sameGatewayHistorySnapshot(firstArtifacts, finalArtifacts) || ctx.Err() != nil {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
		finalSource, sourceErr := m.store.load()
		if sourceErr != nil || !reflect.DeepEqual(firstSource, finalSource) || ctx.Err() != nil {
			return gatewayRebindPredecessorInspectionError(ctx)
		}
	}
	return nil
}

func gatewayRebindPreclaimMatches(snapshot appaccess.GatewayRebindPreclaimSnapshot,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) bool {
	profile := snapshot.CurrentProfile
	return gatewayRebindPredecessorMatches(appaccess.GatewayRebindStartupSnapshot{
		CurrentProfile: &profile,
		Claims: []appaccess.GatewayRebindStartupClaim{{
			Claim: snapshot.ProposedClaim, PredecessorProfile: profile,
			PredecessorUpgrade: snapshot.PredecessorClaim,
			Roster:             snapshot.Proposal.Roster,
			GrantBindings:      snapshot.GrantBindings,
		}},
	}, state, journal)
}
