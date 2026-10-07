package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindTypedForwardServingDriver interface {
	confirmForwardServing(context.Context, gatewayRebindPreparedAttempt, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) error
	withdrawForwardSuccessor(context.Context, gatewayRebindPreparedAttempt, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) error
}

func gatewayRebindForwardConfirmationRequest(attempt gatewayRebindPreparedAttempt,
	receipt gatewayRebindTerminalReceiptV2, snapshot appaccess.GatewayRebindRecoverySnapshot,
) gatewayRebindPhysicalReconcileRequest {
	return gatewayRebindPhysicalReconcileRequest{Attempt: attempt, Terminal: &receipt,
		Mode: gatewayRebindPhysicalReconcileForwardOnly, SQLPhase: snapshot.Phase,
		RollbackAllowed: snapshot.RollbackAllowed, DatabaseCommitObserved: snapshot.DatabaseCommitObserved}
}

func (d managerGatewayRebindCrossStoreDriver) confirmForwardServingLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	fail := func() error { return gatewayRebindEffectBoundaryError(ctx) }
	runtime, ok := d.handover.(gatewayRebindTypedForwardServingDriver)
	if !ok || d.manager == nil {
		return fail()
	}
	boundary, err := d.manager.readGatewayRebindTypedForwardBoundaryLocked(ctx, request)
	if err != nil {
		return fail()
	}
	last := boundary.Progress[len(boundary.Progress)-1]
	result, found := gatewayRebindTypedPhysicalResultFromProgress(last)
	if !found || result.Disposition != appaccess.GatewayRebindDispositionCommit ||
		(request.Terminal != nil && !gatewayRebindPhysicalMatchesTerminal(result, *request.Terminal)) {
		return fail()
	}
	guard := func(effectCtx context.Context) error {
		fresh, err := d.manager.readGatewayRebindTypedForwardBoundaryLocked(effectCtx, request)
		if err != nil || !reflect.DeepEqual(boundary.Progress, fresh.Progress) {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		return nil
	}
	if runtime.confirmForwardServing(ctx, request.Attempt, *last.TypedEffect, guard) != nil {
		return fail()
	}
	return nil
}

// A COMMIT receipt forbids rollback even before SQL commits. Refresh SQL phase
// after uncertain acknowledgments, while pinning the original claim and receipt.
// Serving permission must never authorize this ownership-only withdrawal.
func (d managerGatewayRebindCrossStoreDriver) withdrawForwardSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	unresolved := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	if d.manager == nil || ctx == nil || request.Mode != gatewayRebindPhysicalReconcileForwardOnly ||
		!validGatewayRebindPhysicalReconcileRequest(request) {
		return unresolved
	}
	d.manager.gatewayRebindFailStopLatch().Store(true)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	runtime, ok := d.handover.(gatewayRebindTypedForwardServingDriver)
	if !ok || d.manager.options.RebindCurrentStateRepository == nil {
		return unresolved
	}
	snapshot, err := d.manager.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(stopCtx)
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		!sameGatewayRebindClaimV2Admission(*snapshot.Active.Claim.V2, request.Attempt.Claim) ||
		!gatewayRebindTerminalMatchesActiveSnapshot(*request.Terminal, snapshot) {
		return unresolved
	}
	request.SQLPhase = snapshot.Phase
	request.RollbackAllowed, request.DatabaseCommitObserved = snapshot.RollbackAllowed, snapshot.DatabaseCommitObserved
	boundary, err := d.manager.readGatewayRebindTypedAttemptBoundaryLocked(stopCtx, request)
	if err != nil {
		return unresolved
	}
	last := boundary.Progress[len(boundary.Progress)-1]
	result, found := gatewayRebindTypedPhysicalResultFromProgress(last)
	if !found || !gatewayRebindPhysicalMatchesTerminal(result, *request.Terminal) || last.TypedEffect == nil {
		return unresolved
	}
	guard := func(effectCtx context.Context) error {
		fresh, err := d.manager.readGatewayRebindTypedAttemptBoundaryLocked(effectCtx, request)
		if err != nil || !reflect.DeepEqual(boundary.Progress, fresh.Progress) {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		return nil
	}
	if runtime.withdrawForwardSuccessor(stopCtx, request.Attempt, *last.TypedEffect, guard) != nil {
		return unresolved
	}
	return gatewayRebindEffectBoundaryError(ctx)
}

func (d gatewayRebindTypedHandoverRuntime) confirmForwardServing(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	if effect.HandoverIntent == nil || effect.FinalContainer == nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	proof, physical, err := d.stable(ctx, attempt, effect, guard)
	if err != nil || physical.Predecessor.Running ||
		!gatewayRebindFinalHandoverSuccessorIsServing(proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (d gatewayRebindTypedHandoverRuntime) withdrawForwardSuccessor(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	unresolved := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	if d.manager == nil || effect.FinalContainer == nil || guard == nil {
		return unresolved
	}
	before, err := d.stableResources(ctx, attempt, effect, false, true, guard)
	if err != nil || guard(ctx) != nil {
		return unresolved
	}
	if before.Final.Running {
		// Lost acknowledgments are resolved by exact readback, never by name.
		_ = d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"container", "stop", "--time", "10", effect.FinalContainer.ID)
	}
	after, err := d.stableResources(ctx, attempt, effect, false, true, guard)
	if err != nil || after.Final.Running || after.Predecessor.Running ||
		!d.withdrawn(ctx, attempt, effect, after.Predecessor, false, false) {
		return unresolved
	}
	confirmed, err := d.stableResources(ctx, attempt, effect, false, true, guard)
	if err != nil || confirmed.Final.Running || confirmed.Predecessor.Running || !reflect.DeepEqual(after, confirmed) {
		return unresolved
	}
	return nil
}
