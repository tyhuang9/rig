package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindCommitCurrentProof struct {
	Selection gatewayCurrentSelection
	Digest    string
}

type gatewayRebindCommitPredecessorReader func(context.Context, gatewayCurrentSelection) (
	gatewayRebindAdmissionPredecessorProof, error)

// Caller holds the effects lease and both gateway locks. This proves the
// selected physical state against the complete current SQL census. It neither
// authorizes serving nor establishes a historical operation's terminal outcome.
// A stopped current remains stopped; no recovery or withdrawal occurs here.
func (m *Manager) readGatewayRebindCommitCurrentProofLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
	liveSession func(context.Context) error,
) (gatewayRebindCommitCurrentProof, error) {
	if m == nil {
		return gatewayRebindCommitCurrentProof{}, gatewayRebindProposalError(ctx)
	}
	return m.readGatewayRebindCommitCurrentProofWithInspectLocked(ctx, repository, snapshot,
		liveSession, m.inspectGatewayRebindDocker)
}

func (m *Manager) readGatewayRebindCommitCurrentProofWithInspectLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
	liveSession func(context.Context) error, inspectDocker gatewayRebindDockerInspector,
) (gatewayRebindCommitCurrentProof, error) {
	if m == nil || inspectDocker == nil {
		return gatewayRebindCommitCurrentProof{}, gatewayRebindProposalError(ctx)
	}
	return m.readGatewayRebindCommitCurrentProofWithPredecessorLocked(ctx, repository, snapshot,
		liveSession, func(readCtx context.Context, selection gatewayCurrentSelection) (
			gatewayRebindAdmissionPredecessorProof, error,
		) {
			return m.readGatewayRebindAdmissionPredecessorWithInspectLocked(readCtx, selection, inspectDocker)
		})
}

func (m *Manager) readGatewayRebindCommitCurrentProofWithPredecessorLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
	liveSession func(context.Context) error, predecessor gatewayRebindCommitPredecessorReader,
) (gatewayRebindCommitCurrentProof, error) {
	invalid := func() (gatewayRebindCommitCurrentProof, error) {
		return gatewayRebindCommitCurrentProof{}, gatewayRebindProposalError(ctx)
	}
	if m == nil || ctx == nil || ctx.Err() != nil || repository == nil || liveSession == nil ||
		predecessor == nil || !gatewayCurrentRouteMutationSnapshotReady(snapshot) ||
		snapshot.CurrentSource == nil || m.gatewayRebindFailStopLatch().Load() {
		return invalid()
	}
	startup, ok := repository.(GatewayCurrentServingStartupRepository)
	if !ok {
		return invalid()
	}
	barrier := m.gatewayRebindCommitBarrierLatch().Load()
	guard := func() error {
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return err
		}
		if m.gatewayRebindFailStopLatch().Load() || m.gatewayRebindCommitBarrierLatch().Load() != barrier ||
			repository.CheckGatewayRebindFence(ctx) != nil || ctx.Err() != nil {
			return gatewayRebindProposalError(ctx)
		}
		return nil
	}
	if err := guard(); err != nil {
		return gatewayRebindCommitCurrentProof{}, err
	}
	first, err := startup.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first.Rebind, snapshot) {
		return invalid()
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || filesErr != nil {
		return invalid()
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil || gatewayCurrentAuthority(selection.Lineage) != *snapshot.CurrentSource ||
		!m.gatewayRebindCommitCurrentCensusMatchesLocked(ctx, selection, first) {
		return invalid()
	}
	if err := guard(); err != nil {
		return gatewayRebindCommitCurrentProof{}, err
	}
	physical, err := predecessor(ctx, selection)
	defer clearGatewayRebindCommitCurrentPhysical(&physical)
	if err != nil {
		return invalid()
	}
	// SQL is read before the final protected reads so a repository callback or
	// concurrent SQL writer cannot replace protected state after its last check.
	second, err := startup.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, second) ||
		!m.gatewayRebindCommitCurrentCensusMatchesLocked(ctx, selection, second) {
		return invalid()
	}
	if err := guard(); err != nil {
		return gatewayRebindCommitCurrentProof{}, err
	}
	confirmed, err := m.selectGatewayCurrentLocked(ctx, second.Rebind)
	afterHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || !sameGatewayCurrentSelection(selection, confirmed) || historyErr != nil || filesErr != nil ||
		!sameGatewayRebindCurrentHistory(history, afterHistory) || !sameGatewayHistorySnapshot(files, afterFiles) ||
		ctx.Err() != nil || m.gatewayRebindFailStopLatch().Load() || m.gatewayRebindCommitBarrierLatch().Load() != barrier {
		return invalid()
	}
	digest, err := canonicalDigest(struct {
		Version  int                                     `json:"version"`
		Purpose  string                                  `json:"purpose"`
		Lineage  appaccess.GatewayCurrentLineageRef      `json:"lineage"`
		Census   appaccess.HostingGatewayStartupSnapshot `json:"census"`
		Physical gatewayRebindAdmissionPredecessorProof  `json:"physical"`
	}{1, "hostd/generated-ingress/rebind/commit-current-proof/v1", selection.Lineage, first, physical})
	if err != nil || !validSHA256(digest) || ctx.Err() != nil {
		return invalid()
	}
	// Protected scans can take time. Bind the result to one last complete SQL
	// read and live request check, then detect protected replacement during that
	// read without performing any recovery or changing either admission latch.
	last, err := startup.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, last) ||
		!m.gatewayRebindCommitCurrentCensusMatchesLocked(ctx, selection, last) {
		return invalid()
	}
	if err := guard(); err != nil {
		return gatewayRebindCommitCurrentProof{}, err
	}
	lastFiles, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || !sameGatewayHistorySnapshot(files, lastFiles) || ctx.Err() != nil ||
		m.gatewayRebindFailStopLatch().Load() || m.gatewayRebindCommitBarrierLatch().Load() != barrier {
		return invalid()
	}
	if err := guard(); err != nil {
		return gatewayRebindCommitCurrentProof{}, err
	}
	return gatewayRebindCommitCurrentProof{Selection: selection, Digest: digest}, nil
}

func clearGatewayRebindCommitCurrentPhysical(proof *gatewayRebindAdmissionPredecessorProof) {
	if proof.Upgrade != nil {
		clearGatewayV2DockerObservation(&proof.Upgrade.Docker)
	}
	if proof.Current != nil {
		clearGatewayCurrentPhysicalInventory(&proof.Current.Inventory)
	}
	*proof = gatewayRebindAdmissionPredecessorProof{}
}

func (m *Manager) gatewayRebindCommitCurrentCensusMatchesLocked(ctx context.Context,
	selection gatewayCurrentSelection, snapshot appaccess.HostingGatewayStartupSnapshot,
) bool {
	if !gatewayCurrentRouteMutationSnapshotReady(snapshot.Rebind) {
		return false
	}
	routes, err := gatewayRebindProposalRoutes(selection)
	if err != nil {
		return false
	}
	apps := make(map[string]gatewayCurrentAppRoute, len(routes))
	for id, route := range routes {
		apps[id] = gatewayCurrentAppRoute{Route: route.route, LAN: route.lan}
	}
	if !gatewayServingRuntimeComponentsMatch(apps, snapshot) {
		return false
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(snapshot.Grants),
		GatewayLANDisableStartupClaims(snapshot.Disables))
	if err != nil {
		return false
	}
	switch selection.Kind {
	case gatewayCurrentSelectionRebind:
		inspection, err := gatewayCurrentLANStartupCensus(*selection.State, claims)
		return err == nil && inspection.Disposition == GatewayV2LANStartupNormal &&
			m.validateGatewayCurrentLANRetainedHistoryLocked(ctx, claims, snapshot.Rebind) == nil
	case gatewayCurrentSelectionUpgrade:
		return gatewayRebindCommitNativeLANCensusMatches(selection, apps, claims)
	default:
		return false
	}
}

func gatewayRebindCommitNativeLANCensusMatches(selection gatewayCurrentSelection,
	apps map[string]gatewayCurrentAppRoute, claims gatewayV2LANAccessStartupClaims,
) bool {
	proof, err := gatewayUpgradeEffectiveProof(*selection.Upgrade)
	if err != nil {
		return false
	}
	for _, grant := range claims.grants.byAttempt {
		required := grant.State != appaccess.AppAccessGrantPrepared && grant.State != appaccess.AppAccessGrantRolledBack
		if !gatewayV2LANStartupProjectionsMatchNative(grant.CurrentBinding, grant.RetainedBinding, proof, true, required) {
			return false
		}
	}
	for _, disable := range claims.disables {
		required := disable.Request.SourceGrant != nil || disable.State == appaccess.AppAccessDisableWithdrawing ||
			disable.State == appaccess.AppAccessDisableUncertain
		if !gatewayV2LANStartupProjectionsMatchNative(disable.CurrentBinding, disable.RetainedBinding,
			proof, true, required) {
			return false
		}
	}
	live := make(map[string]bool)
	for appID, app := range apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw)
		grant, found := claims.grants.byAttempt[request.AttemptID]
		if !found || grant.Request != request || grant.RequiresRecovery ||
			grant.State != appaccess.AppAccessGrantCommitted || grant.DisableIntentOperationID != "" ||
			grant.RetainedBinding != nil || grant.CurrentBinding == nil {
			return false
		}
		if _, disabled := claims.byAllocation[request.AllocationID]; disabled {
			return false
		}
		live[request.AttemptID] = true
	}
	for _, disable := range claims.disables {
		if disable.State != appaccess.AppAccessDisableCommitted || !disable.ClearAcknowledged ||
			(disable.Request.SourceGrant != nil && live[disable.Request.SourceGrant.AttemptID]) {
			return false
		}
	}
	for id, grant := range claims.grants.byAttempt {
		if live[id] || grant.State == appaccess.AppAccessGrantRolledBack {
			continue
		}
		disable, found := claims.byAllocation[grant.Request.AllocationID]
		if !found || disable.Request.SourceGrant == nil || *disable.Request.SourceGrant != grant.Request {
			return false
		}
	}
	return true
}
