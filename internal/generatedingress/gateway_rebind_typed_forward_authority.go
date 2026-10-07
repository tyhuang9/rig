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

// Completed physical history forbids rollback even before a COMMIT receipt is
// installed. Serving permission must never authorize ownership-only withdrawal.
func (d managerGatewayRebindCrossStoreDriver) withdrawForwardSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	handled, err := d.withdrawCompletedSuccessorLocked(ctx, request)
	if !handled {
		if d.manager != nil {
			d.manager.gatewayRebindFailStopLatch().Store(true)
		}
		return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	return err
}

// A false result proves an incomplete, undecided prefix for ordinary rollback.
// Uncertain history must never be mistaken for permission to roll back.
func (d managerGatewayRebindCrossStoreDriver) withdrawCompletedSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) (bool, error) {
	unresolved := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	fail := func() (bool, error) {
		if d.manager != nil {
			d.manager.gatewayRebindFailStopLatch().Store(true)
		}
		return true, unresolved
	}
	if d.manager == nil || ctx == nil || request.Mode == gatewayRebindPhysicalReconcileRollbackOnly ||
		!validGatewayRebindPhysicalReconcileRequest(request) {
		return fail()
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if d.manager.options.RebindCurrentStateRepository == nil {
		return fail()
	}
	snapshot, err := d.manager.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(stopCtx)
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		!sameGatewayRebindClaimV2Admission(*snapshot.Active.Claim.V2, request.Attempt.Claim) {
		return fail()
	}
	expectedTerminal := request.Terminal
	if expectedTerminal != nil {
		history, scanErr := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil {
			return fail()
		}
		retained, terminalErr := gatewayRebindActiveTerminalV2(history, request.Attempt.Claim)
		if terminalErr != nil {
			return fail()
		}
		if retained == nil {
			// A failed install may have written nothing. Require the complete
			// pre-receipt ownership boundary, still pinned to the intended result.
			request.Mode, request.Terminal = gatewayRebindPhysicalReconcileUndecided, nil
		} else if !reflect.DeepEqual(*retained, *expectedTerminal) ||
			!gatewayRebindTerminalMatchesActiveSnapshot(*retained, snapshot) {
			return fail()
		}
	}
	request.SQLPhase = snapshot.Phase
	request.RollbackAllowed, request.DatabaseCommitObserved = snapshot.RollbackAllowed, snapshot.DatabaseCommitObserved
	boundary, err := d.manager.readGatewayRebindTypedAttemptBoundaryLocked(stopCtx, request)
	if err != nil {
		return fail()
	}
	last := boundary.Progress[len(boundary.Progress)-1]
	result, found := gatewayRebindTypedPhysicalResultFromProgress(last)
	if expectedTerminal == nil && request.Mode == gatewayRebindPhysicalReconcileUndecided &&
		last.Phase != gatewayRebindProgressHandoverCommitted {
		return false, nil
	}
	if !found || result.Disposition != appaccess.GatewayRebindDispositionCommit || last.TypedEffect == nil ||
		(expectedTerminal != nil && !gatewayRebindPhysicalMatchesTerminal(result, *expectedTerminal)) {
		return fail()
	}
	d.manager.gatewayRebindFailStopLatch().Store(true)
	runtime, ok := d.handover.(gatewayRebindTypedForwardServingDriver)
	if !ok {
		return fail()
	}
	guard := func(effectCtx context.Context) error {
		fresh, err := d.manager.readGatewayRebindTypedAttemptBoundaryLocked(effectCtx, request)
		if err != nil || !reflect.DeepEqual(boundary.Progress, fresh.Progress) {
			return gatewayRebindEffectBoundaryError(effectCtx)
		}
		return nil
	}
	if runtime.withdrawForwardSuccessor(stopCtx, request.Attempt, *last.TypedEffect, guard) != nil {
		return fail()
	}
	return true, gatewayRebindEffectBoundaryError(ctx)
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
