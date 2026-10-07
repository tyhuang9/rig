package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindTypedCompletedServingRestorer interface {
	restoreCompletedServing(context.Context, gatewayRebindPreparedAttempt, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) error
}

// Only a cached, completed physical handover may enter this effectful recovery.
// SQL transition confirmation remains observational. The caller owns all locks
// and routes failures through completed-successor withdrawal, never rollback.
func (d managerGatewayRebindCrossStoreDriver) restoreCompletedServingLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	invalid := func() error { return gatewayRebindEffectBoundaryError(ctx) }
	if d.manager == nil || ctx == nil || ctx.Err() != nil || d.manager.gatewayRebindAdmissionBlocked() {
		return invalid()
	}
	boundary, err := d.manager.readGatewayRebindTypedForwardBoundaryLocked(ctx, request)
	if err != nil {
		return invalid()
	}
	last := boundary.Progress[len(boundary.Progress)-1]
	result, found := gatewayRebindTypedPhysicalResultFromProgress(last)
	if !found || result.Disposition != appaccess.GatewayRebindDispositionCommit ||
		(request.Terminal != nil && !gatewayRebindPhysicalMatchesTerminal(result, *request.Terminal)) {
		return invalid()
	}
	repository, ok := d.manager.options.RebindCurrentStateRepository.(GatewayCurrentServingStartupRepository)
	if !ok {
		return invalid()
	}
	authority, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(authority.Rebind, boundary.Snapshot) ||
		(!request.DatabaseCommitObserved && !gatewayRebindTypedPrecommitServingSnapshotMatches(request.Attempt, authority)) {
		return invalid()
	}
	guard := func(effectCtx context.Context) error {
		if effectCtx == nil || effectCtx.Err() != nil || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		fresh, err := d.manager.readGatewayRebindTypedForwardBoundaryLocked(effectCtx, request)
		if err != nil || !reflect.DeepEqual(boundary.Progress, fresh.Progress) ||
			!sameGatewayCurrentSelection(boundary.Selection, fresh.Selection) ||
			!sameGatewayRebindCurrentHistory(boundary.History, fresh.History) {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		confirmed, err := repository.HostingGatewayStartupSnapshot(effectCtx)
		if err != nil || !reflect.DeepEqual(authority, confirmed) || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		if request.DatabaseCommitObserved && (fresh.Selection.State == nil ||
			!d.manager.gatewayRebindCommittedServingCensusMatchesLocked(effectCtx, request, *fresh.Selection.State, confirmed)) {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		return nil
	}
	if guard(ctx) != nil {
		return invalid()
	}
	if d.confirmForwardServingLocked(ctx, request) == nil {
		return guard(ctx)
	}
	if request.DatabaseCommitObserved {
		_, err := d.manager.restoreGatewayRebindCommittedServingLocked(ctx, repository, request, guard)
		return err
	}
	runtime, ok := d.handover.(gatewayRebindTypedCompletedServingRestorer)
	if !ok {
		return invalid()
	}
	return runtime.restoreCompletedServing(ctx, request.Attempt, *last.TypedEffect, guard)
}

func (d gatewayRebindTypedHandoverRuntime) restoreCompletedServing(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	invalid := func() error { return gatewayRebindEffectBoundaryError(ctx) }
	if d.manager == nil || effect.HandoverIntent == nil || effect.FinalContainer == nil ||
		effect.SuccessorServing == nil || effect.Resources == nil || effect.PhysicalProof == nil {
		return invalid()
	}
	proof, physical, err := d.stable(ctx, attempt, effect, guard)
	if err != nil || physical.Predecessor.Running {
		return invalid()
	}
	if gatewayRebindFinalHandoverSuccessorIsServing(proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
		return nil
	}
	plan := effect.HandoverIntent.Plan
	if !validGatewayRebindFinalHandoverObservationValue(proof) ||
		proof.Stage != gatewayRebindHandoverContainerAbsent || proof.Final != gatewayRebindHandoverContainerStopped ||
		proof.FinalID != effect.FinalContainer.ID || proof.PredecessorRunning ||
		proof.PredecessorAddress == gatewayRebindPredecessorAddressAmbiguous || !validSHA256(proof.PredecessorStopDigest) ||
		!proof.ConfigVolumePresent || !proof.DataVolumePresent || !proof.IngressNetworkPresent ||
		proof.ConfigDigest != plan.FinalConfigDigest || proof.RoutesDigest != "" ||
		!reflect.DeepEqual(proof.ApplicationNetworks, plan.ApplicationNetworks) || guard(ctx) != nil {
		return invalid()
	}
	// The exact stopped inventory/config and every listener were proved above.
	// A lost start acknowledgment must resolve through fresh serving readback.
	_ = d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "start", effect.FinalContainer.ID)
	return d.confirmForwardServing(ctx, attempt, effect, guard)
}
