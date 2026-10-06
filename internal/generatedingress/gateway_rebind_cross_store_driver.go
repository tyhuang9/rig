package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// managerGatewayRebindCrossStoreDriver is the production physical adapter.
// The individual effect cores are filled by the normalized driver checkpoint;
// returning an error here keeps the private commit path fail closed until that
// adapter has proved the complete physical sequence.
type managerGatewayRebindCrossStoreDriver struct {
	manager *Manager
	stage   gatewayRebindTypedStageDriver
}

func (d managerGatewayRebindCrossStoreDriver) reconcileSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	if d.manager == nil || ctx == nil || appendProgress == nil || !validGatewayRebindPhysicalReconcileRequest(request) ||
		request.Mode == gatewayRebindPhysicalReconcileRollbackOnly {
		return gatewayRebindTypedPhysicalResult{}, errors.New("generated ingress typed physical rebind is unavailable")
	}
	stage := d.stage
	if stage == nil {
		return gatewayRebindTypedPhysicalResult{}, errors.New("generated ingress typed physical rebind is unavailable")
	}
	boundary, err := d.manager.readGatewayRebindTypedAttemptBoundaryLocked(ctx, request)
	if err != nil {
		return gatewayRebindTypedPhysicalResult{}, gatewayRebindEffectBoundaryError(ctx)
	}
	progress := append([]gatewayRebindProgressRecord(nil), boundary.Progress...)
	guard := func(guardCtx context.Context) error {
		fresh, guardErr := d.manager.readGatewayRebindTypedAttemptBoundaryLocked(guardCtx, request)
		if guardErr != nil || !reflect.DeepEqual(progress, fresh.Progress) {
			return gatewayRebindEffectBoundaryError(guardCtx)
		}
		return nil
	}
	appendEffect := func(effect gatewayRebindTypedEffectProgress) error {
		record, buildErr := newGatewayRebindTypedEffectProgressV2(request.Attempt.Intent,
			request.Attempt.Checkpoint, progress, effect,
			gatewayRebindTimeStrictlyAfter(d.manager.gatewayRebindProgressTime(), progress[len(progress)-1].OccurredAt))
		if buildErr != nil || appendProgress(ctx, record) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		progress = append(progress, record)
		return guard(ctx)
	}
	for len(progress) < 12 {
		last := progress[len(progress)-1]
		var effect gatewayRebindTypedEffectProgress
		if last.TypedEffect != nil {
			effect = *last.TypedEffect
		}
		switch last.Sequence + 1 {
		case 2:
			imageID, observeErr := stage.observeImage(ctx, request.Attempt.Intent, guard)
			planDigest, digestErr := gatewayRebindTypedStagePlanDigest(request.Attempt.Intent)
			if observeErr != nil || digestErr != nil {
				return gatewayRebindTypedPhysicalResult{}, gatewayRebindEffectBoundaryError(ctx)
			}
			effect = gatewayRebindTypedEffectProgress{ImageID: imageID, StagePlanDigest: planDigest}
		case 3:
			binding, bindErr := stage.bindNetwork(ctx, request.Attempt.Intent, effect, guard)
			if bindErr != nil {
				return gatewayRebindTypedPhysicalResult{}, bindErr
			}
			effect.Network = &binding
		case 4:
			binding, bindErr := stage.bindConfigVolume(ctx, request.Attempt.Intent, effect, guard)
			if bindErr != nil {
				return gatewayRebindTypedPhysicalResult{}, bindErr
			}
			effect.ConfigVolume = &binding
		case 5:
			binding, bindErr := stage.bindDataVolume(ctx, request.Attempt.Intent, effect, guard)
			if bindErr != nil {
				return gatewayRebindTypedPhysicalResult{}, bindErr
			}
			effect.DataVolume = &binding
		case 6:
			binding, bindErr := stage.bindStageContainer(ctx, request.Attempt.Intent, effect, guard)
			if bindErr != nil {
				return gatewayRebindTypedPhysicalResult{}, bindErr
			}
			effect.StageContainer = &binding
		case 7:
			binding, buildErr := gatewayRebindTypedStageConfigIntentFor(request.Attempt.Intent, last, effect)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.StageConfigIntent = &binding
		case 8:
			if copyErr := stage.copyStageConfig(ctx, request.Attempt.Intent, effect, guard); copyErr != nil {
				return gatewayRebindTypedPhysicalResult{}, copyErr
			}
			binding, buildErr := gatewayRebindTypedStageConfigCopyFor(request.Attempt.Intent, last, effect)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.StageConfigCopy = &binding
		case 9:
			binding, buildErr := gatewayRebindTypedStageStartIntentFor(request.Attempt.Intent, last, effect)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.StageStartIntent = &binding
		case 10:
			endpoint, serveErr := stage.serveStage(ctx, request.Attempt.Intent, effect, guard)
			if serveErr != nil {
				return gatewayRebindTypedPhysicalResult{}, serveErr
			}
			binding, buildErr := gatewayRebindTypedStageServingFor(request.Attempt.Intent, last, effect, endpoint)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.StageServing = &binding
		case 11:
			binding, buildErr := gatewayRebindTypedFinalConfigIntentFor(request.Attempt.Intent,
				request.Attempt.Checkpoint, last, effect, request.Attempt.Intent.RuntimeHeads)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.FinalConfigIntent = &binding
		case 12:
			if copyErr := stage.copyFinalConfig(ctx, request.Attempt.Intent, request.Attempt.Checkpoint,
				effect, guard); copyErr != nil {
				return gatewayRebindTypedPhysicalResult{}, copyErr
			}
			binding, buildErr := gatewayRebindTypedFinalConfigCopyFor(request.Attempt.Intent, last, effect)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.FinalConfigCopy = &binding
		default:
			return gatewayRebindTypedPhysicalResult{}, gatewayRebindEffectBoundaryError(ctx)
		}
		if err := appendEffect(effect); err != nil {
			return gatewayRebindTypedPhysicalResult{}, err
		}
	}
	return gatewayRebindTypedPhysicalResult{}, errors.New("generated ingress typed final handover is unavailable")
}

func (d managerGatewayRebindCrossStoreDriver) proveNoSuccessorEffectsLocked(context.Context,
	appaccess.GatewayRebindClaimV2, []appaccess.GatewayRebindRosterEntryV2,
	gatewayRebindPredecessorCheckpoint,
) (gatewayRebindNoEffectAbortProof, error) {
	return gatewayRebindNoEffectAbortProof{}, errors.New("generated ingress typed no-effect proof is unavailable")
}

func (d managerGatewayRebindCrossStoreDriver) attestCommittedCurrentLocked(context.Context,
	gatewayCurrentSelection,
) (string, error) {
	return "", errors.New("generated ingress typed current attestation is unavailable")
}
