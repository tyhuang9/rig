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
