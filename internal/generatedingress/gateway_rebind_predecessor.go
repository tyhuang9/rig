package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// InspectGatewayRebindPredecessor proves that the one prepared SQLite rebind
// claim describes the exact committed protected predecessor. It is strictly
// read-only: it does not inspect Docker, expose an address, recover state, or
// create, replace, or retire any protected artifact.
func (m *Manager) InspectGatewayRebindPredecessor(ctx context.Context, repository *appaccess.Repository) (
	resultErr error,
) {
	return m.inspectGatewayRebindPredecessor(ctx, repository, nil)
}

func (m *Manager) inspectGatewayRebindPredecessor(ctx context.Context, repository *appaccess.Repository,
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
	if checkpoint != nil {
		checkpoint()
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
	return nil
}

func gatewayRebindPredecessorMatches(snapshot appaccess.GatewayRebindStartupSnapshot,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) bool {
	if snapshot.CurrentProfile == nil || len(snapshot.Claims) != 1 ||
		state.Pending != nil || state.LANRecovery != nil || journal.Phase != gatewayPhaseCommitted {
		return false
	}
	claim := snapshot.Claims[0]
	if claim.Claim.State != appaccess.GatewayRebindPrepared || claim.Claim.StateSequence != 1 ||
		*snapshot.CurrentProfile != claim.PredecessorProfile ||
		len(claim.Roster) != len(claim.GrantBindings) {
		return false
	}

	profile := gatewayProfileBinding{
		RevisionID: claim.PredecessorProfile.ID, RevisionNumber: claim.PredecessorProfile.RevisionNumber,
		SpecDigest:   claim.PredecessorProfile.SpecDigest,
		SelectedIPv4: claim.PredecessorProfile.Spec.SelectedIPv4,
		InterfaceID:  claim.PredecessorProfile.Spec.InterfaceID,
		PortStart:    claim.PredecessorProfile.Spec.PortStart,
		PortEnd:      claim.PredecessorProfile.Spec.PortEnd,
	}
	actionDigest, err := gatewayUpgradeActionDigest(profile, gatewayV2IdentityVersion)
	if err != nil || claim.Claim.Spec.PredecessorProfileRevisionID != profile.RevisionID ||
		claim.Claim.Spec.PredecessorProfileRevisionNumber != profile.RevisionNumber ||
		claim.Claim.Spec.PredecessorProfileSpecDigest != profile.SpecDigest ||
		claim.PredecessorUpgrade.State != appaccess.GatewayProfileUpgradeCommitted ||
		claim.PredecessorUpgrade.OperationID != claim.Claim.Spec.PredecessorUpgradeOperationID ||
		claim.PredecessorUpgrade.ProfileRevisionID != profile.RevisionID ||
		claim.PredecessorUpgrade.ProfileRevisionNumber != profile.RevisionNumber ||
		claim.PredecessorUpgrade.ProfileSpecDigest != profile.SpecDigest ||
		state.Identity.Digest != claim.Claim.Spec.PredecessorProtectedIdentityDigest ||
		!gatewayV2RequestMatchesState(GatewayV2UpgradeRequest{
			OperationID: claim.PredecessorUpgrade.OperationID,
			Profile: GatewayV2ProfileBinding{
				RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
				SpecDigest: profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4,
				InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
			},
			ApprovedBy: claim.PredecessorUpgrade.ApprovedBy, ApprovedActionDigest: actionDigest,
		}, state, journal) {
		return false
	}

	seenApps := make(map[string]struct{}, len(claim.Roster))
	seenAttempts := make(map[string]struct{}, len(claim.Roster))
	seenAllocations := make(map[string]struct{}, len(claim.Roster))
	for index, entry := range claim.Roster {
		grant := claim.GrantBindings[index]
		if entry.Ordinal != int64(index+1) || entry.OperationID != claim.Claim.Spec.OperationID ||
			grant.AppID != entry.AppID || grant.AttemptID != entry.GrantAttemptID ||
			grant.GatewayOperationID != state.OperationID {
			return false
		}
		if _, duplicate := seenApps[entry.AppID]; duplicate {
			return false
		}
		if _, duplicate := seenAttempts[entry.GrantAttemptID]; duplicate {
			return false
		}
		if _, duplicate := seenAllocations[entry.AllocationID]; duplicate {
			return false
		}
		seenApps[entry.AppID] = struct{}{}
		seenAttempts[entry.GrantAttemptID] = struct{}{}
		seenAllocations[entry.AllocationID] = struct{}{}

		app, exists := state.Apps[entry.AppID]
		expected := gatewayV2LANBinding{
			GrantAttemptID: entry.GrantAttemptID, GrantRequestDigest: grant.RequestDigest,
			OwnerOperationID: entry.AllocationOwnerOperationID,
			AccessRevisionID: entry.AccessRevisionID, AccessRevisionNumber: entry.AccessRevisionNumber,
			AccessSpecDigest: entry.AccessSpecDigest, AllocationID: entry.AllocationID, Port: entry.Port,
			ProfileRevisionID: profile.RevisionID, ProfileRevisionNumber: profile.RevisionNumber,
			ProfileSpecDigest: profile.SpecDigest, ApprovedBy: grant.ApprovedBy,
		}
		if !exists || app.LAN == nil || *app.LAN != expected || string(app.Route.Slot) != entry.ServingSlot {
			return false
		}
	}

	protectedCount := 0
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		protectedCount++
		if _, expected := seenApps[appID]; !expected {
			return false
		}
	}
	return protectedCount == len(claim.Roster)
}

func gatewayRebindPredecessorInspectionError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}
