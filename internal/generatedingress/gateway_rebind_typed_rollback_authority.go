package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// Retained ownership authorizes withdrawal. Serving additionally requires a
// complete current Hosting census; revocation must never gate successor cleanup.
func gatewayRebindTypedPrecommitServingSnapshotMatches(attempt gatewayRebindPreparedAttempt,
	snapshot appaccess.HostingGatewayStartupSnapshot,
) bool {
	rebind := snapshot.Rebind
	if rebind.Active == nil || rebind.Active.Claim.V2 == nil ||
		rebind.DatabaseCommitObserved || rebind.DatabaseCommittedEvent != nil ||
		(rebind.Phase != appaccess.GatewayRebindPrepared && rebind.Phase != appaccess.GatewayRebindSuccessorReady) ||
		!sameGatewayRebindClaimV2Admission(*rebind.Active.Claim.V2, attempt.Claim) ||
		rebind.CurrentSource == nil || *rebind.CurrentSource != gatewayCurrentAuthority(attempt.Checkpoint.Lineage) ||
		!snapshot.ActiveRebindApprovalsAuthorizeServing() ||
		!sameGatewayRebindRuntimeHeads(snapshot.RuntimeHeads, attempt.Intent.RuntimeHeads) ||
		!gatewayRebindTypedRosterApprovalsCurrent(attempt, snapshot.Grants) {
		return false
	}
	apps, err := gatewayRebindV2CheckpointApps(attempt.Checkpoint)
	if err != nil || !gatewayServingRuntimeComponentsMatch(apps, snapshot) {
		return false
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(snapshot.Grants),
		GatewayLANDisableStartupClaims(snapshot.Disables))
	if err != nil {
		return false
	}
	if attempt.Checkpoint.CurrentState != nil {
		inspection, err := gatewayCurrentLANStartupCensus(*attempt.Checkpoint.CurrentState, claims)
		return err == nil && inspection.Disposition == GatewayV2LANStartupNormal
	}
	// Native routes have no transfer lineage. Validate their exact raw grants
	// and retain only terminal, acknowledged disable history outside live routes.
	state := attempt.Checkpoint.UpgradeState
	if state == nil || state.Pending != nil || state.LANRecovery != nil {
		return false
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
			grant.RetainedBinding != nil || grant.CurrentBinding == nil ||
			grant.CurrentBinding.GatewaySource != *rebind.CurrentSource ||
			grant.CurrentBinding.EffectiveProfile != GatewayV2ProfileBinding(state.Profile) ||
			len(grant.CurrentBinding.TransferChain) != 0 || grant.CurrentBinding.TransferChainTipDigest != "" ||
			grant.CurrentBinding.TerminalReceiptDigest != "" {
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

func (d gatewayRebindTypedHandoverRuntime) authorizedRollbackPredecessor(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedPredecessorObservation, error) {
	invalid := func() (gatewayRebindTypedPredecessorObservation, error) {
		return gatewayRebindTypedPredecessorObservation{}, d.refuseRollbackServing(ctx, attempt, current, guard)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil || guard == nil || guard(ctx) != nil {
		return invalid()
	}
	repository, ok := d.manager.options.RebindCurrentStateRepository.(GatewayCurrentServingStartupRepository)
	if !ok {
		return invalid()
	}
	first, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !first.Rebind.RollbackAllowed || !gatewayRebindTypedPrecommitServingSnapshotMatches(attempt, first) {
		return invalid()
	}
	ownedSQL, err := d.manager.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first.Rebind, ownedSQL) {
		return invalid()
	}
	predecessor, err := d.predecessor(ctx, attempt)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, predecessor.Selection, attempt) ||
		predecessor.Address != gatewayRebindPredecessorAddressPresent || guard(ctx) != nil {
		return invalid()
	}
	second, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(first, second) {
		return invalid()
	}
	return predecessor, nil
}

func (d gatewayRebindTypedHandoverRuntime) confirmRollbackServing(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection, guard gatewayRebindTypedEffectGuard,
) error {
	predecessor, err := d.authorizedRollbackPredecessor(ctx, attempt, current, guard)
	if err != nil {
		return err
	}
	if !predecessor.Running || !validSHA256(predecessor.Routes) {
		return d.refuseRollbackServing(ctx, attempt, current, guard)
	}
	absent := func(stage gatewayRebindTypedStageObservation) bool {
		return !stage.NetworkFound && !stage.ConfigVolumeFound && !stage.DataVolumeFound &&
			!stage.StageContainerFound && !stage.FinalContainerFound && len(stage.OwnedContainers) == 0 &&
			len(stage.OwnedVolumes) == 0 && len(stage.OwnedNetworks) == 0 &&
			d.stage.networkTopologyMatches(ctx, attempt.Intent, stage)
	}
	stage, err := d.stage.read(ctx, attempt.Intent)
	if err != nil || !absent(stage) ||
		!d.withdrawn(ctx, attempt, gatewayRebindTypedEffectProgress{}, predecessor, false, false) {
		return d.refuseRollbackServing(ctx, attempt, current, guard)
	}
	// Inventory and probes may take time; recheck permissions after those reads.
	confirmed, err := d.authorizedRollbackPredecessor(ctx, attempt, current, guard)
	if err != nil {
		return err
	}
	if !confirmed.Running || !validSHA256(confirmed.Routes) || confirmed.ContainerID != predecessor.ContainerID ||
		confirmed.Routes != predecessor.Routes {
		return d.refuseRollbackServing(ctx, attempt, current, guard)
	}
	after, err := d.stage.read(ctx, attempt.Intent)
	if err != nil || !absent(after) || !reflect.DeepEqual(stage, after) || guard(ctx) != nil {
		return d.refuseRollbackServing(ctx, attempt, current, guard)
	}
	return nil
}

// Never use serving permission to authorize this stop. Re-read exact retained
// ownership before touching Docker and prove every predecessor listener absent.
// An ambiguous identity or failed withdrawal remains explicitly potentially live.
func (d gatewayRebindTypedHandoverRuntime) refuseRollbackServing(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection, guard gatewayRebindTypedEffectGuard,
) error {
	unresolved := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	if d.manager == nil || ctx == nil || guard == nil {
		return unresolved
	}
	d.manager.gatewayRebindFailStopLatch().Store(true)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if guard(stopCtx) != nil {
		return unresolved
	}
	before, err := d.predecessor(stopCtx, attempt)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, before.Selection, attempt) {
		return unresolved
	}
	if before.Running {
		// A lost command acknowledgement is reconciled by exact readback below.
		_ = d.manager.runDiscard(stopCtx, d.manager.options.CommandTimeout,
			"container", "stop", "--time", "10", before.ContainerID)
	}
	after, err := d.predecessor(stopCtx, attempt)
	if err != nil || after.Running || after.ContainerID != before.ContainerID ||
		!gatewayRebindTypedSelectionAuthorizesAttempt(current, after.Selection, attempt) || guard(stopCtx) != nil {
		return unresolved
	}
	for port := after.Profile.PortStart; ; port++ {
		if !d.proveListenerAbsent(stopCtx, after.Profile.SelectedIPv4, port) {
			return unresolved
		}
		if port == after.Profile.PortEnd {
			break
		}
	}
	if !d.proveListenerAbsent(stopCtx, "127.0.0.1", after.LocalHostPort) || guard(stopCtx) != nil {
		return unresolved
	}
	// A cleanup failure may have left a successor behind. Stopping the
	// predecessor alone is not evidence that the whole attempt is withdrawn.
	stage, err := d.stage.read(stopCtx, attempt.Intent)
	if err != nil || stage.StageContainerFound || stage.FinalContainerFound || len(stage.OwnedContainers) != 0 ||
		!d.withdrawn(stopCtx, attempt, gatewayRebindTypedEffectProgress{}, after, false, false) {
		return unresolved
	}
	confirmed, err := d.predecessor(stopCtx, attempt)
	if err != nil || confirmed.Running || confirmed.ContainerID != after.ContainerID ||
		!gatewayRebindTypedSelectionAuthorizesAttempt(current, confirmed.Selection, attempt) || guard(stopCtx) != nil {
		return unresolved
	}
	return gatewayRebindEffectBoundaryError(ctx)
}
