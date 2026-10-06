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
	manager  *Manager
	stage    gatewayRebindTypedStageDriver
	handover gatewayRebindTypedHandoverDriver
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
		if guard(ctx) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
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
	if result, ok := gatewayRebindTypedPhysicalResultFromProgress(progress[len(progress)-1]); ok {
		return result, nil
	}
	if d.handover == nil || request.Mode == gatewayRebindPhysicalReconcileRollbackOnly {
		return gatewayRebindTypedPhysicalResult{}, errors.New("generated ingress typed final handover is unavailable")
	}
	for len(progress) < 17 {
		last := progress[len(progress)-1]
		if last.TypedEffect == nil {
			return gatewayRebindTypedPhysicalResult{}, gatewayRebindEffectBoundaryError(ctx)
		}
		effect := *last.TypedEffect
		switch last.Sequence + 1 {
		case 13:
			prepared, prepareErr := d.handover.prepareHandover(ctx, request.Attempt, boundary.Selection, effect, guard)
			if prepareErr != nil {
				return gatewayRebindTypedPhysicalResult{}, prepareErr
			}
			value, buildErr := gatewayRebindTypedHandoverIntentFor(request.Attempt.Intent, last, effect,
				prepared.LocalHostPort, prepared.PredecessorObservationDigest,
				prepared.PredecessorInitiallyRunning, prepared.ApplicationNetworks)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.HandoverIntent = &value
			effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil),
				prepared.ApplicationNetworks...)
		case 14:
			value, bindErr := d.handover.bindFinalContainer(ctx, request.Attempt.Intent, effect, guard)
			if bindErr != nil {
				return gatewayRebindTypedPhysicalResult{}, bindErr
			}
			effect.FinalContainer = &value
		case 15:
			observation, observeErr := d.handover.prepareCutover(ctx, request.Attempt,
				boundary.Selection, effect, guard)
			if observeErr != nil {
				return gatewayRebindTypedPhysicalResult{}, observeErr
			}
			value, buildErr := gatewayRebindTypedCutoverIntentFor(request.Attempt.Intent, last, effect,
				observation.Digest)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.CutoverIntent = &value
		case 16:
			observation, serveErr := d.handover.serveSuccessor(ctx, request.Attempt,
				boundary.Selection, effect, request.Mode, guard)
			if serveErr != nil {
				return gatewayRebindTypedPhysicalResult{}, serveErr
			}
			value, buildErr := gatewayRebindTypedSuccessorServingFor(last, effect, observation)
			if buildErr != nil {
				return gatewayRebindTypedPhysicalResult{}, buildErr
			}
			effect.SuccessorServing = &value
		case 17:
			resources, proof, finalizeErr := d.handover.finalizeSuccessor(ctx, request.Attempt,
				boundary.Selection, last, effect, guard)
			if finalizeErr != nil {
				return gatewayRebindTypedPhysicalResult{}, finalizeErr
			}
			effect.Resources, effect.PhysicalProof = &resources, &proof
		default:
			return gatewayRebindTypedPhysicalResult{}, gatewayRebindEffectBoundaryError(ctx)
		}
		if err := appendEffect(effect); err != nil {
			return gatewayRebindTypedPhysicalResult{}, err
		}
	}
	result, ok := gatewayRebindTypedPhysicalResultFromProgress(progress[len(progress)-1])
	if !ok || result.Disposition != appaccess.GatewayRebindDispositionCommit {
		return gatewayRebindTypedPhysicalResult{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return result, nil
}

func gatewayRebindTypedPhysicalResultFromProgress(last gatewayRebindProgressRecord) (gatewayRebindTypedPhysicalResult, bool) {
	if last.Phase == gatewayRebindProgressHandoverCommitted && last.Sequence == 17 && last.TypedEffect != nil &&
		last.TypedEffect.Resources != nil && last.TypedEffect.PhysicalProof != nil {
		return gatewayRebindTypedPhysicalResult{Disposition: appaccess.GatewayRebindDispositionCommit,
			Last: last, Resources: *last.TypedEffect.Resources, Proof: *last.TypedEffect.PhysicalProof}, true
	}
	if last.Phase == gatewayRebindProgressHandoverRolledBack && last.TypedRollback != nil &&
		last.TypedRollback.PhysicalProof != nil {
		return gatewayRebindTypedPhysicalResult{Disposition: appaccess.GatewayRebindDispositionAbort, Last: last}, true
	}
	return gatewayRebindTypedPhysicalResult{}, false
}

func (d managerGatewayRebindCrossStoreDriver) proveNoSuccessorEffectsLocked(context.Context,
	appaccess.GatewayRebindClaimV2, []appaccess.GatewayRebindRosterEntryV2,
	gatewayRebindPredecessorCheckpoint,
) (gatewayRebindNoEffectAbortProof, error) {
	return gatewayRebindNoEffectAbortProof{}, errors.New("generated ingress typed no-effect proof is unavailable")
}

func (d managerGatewayRebindCrossStoreDriver) attestCommittedCurrentLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (string, error) {
	if d.manager == nil || ctx == nil || ctx.Err() != nil || selection.Kind != gatewayCurrentSelectionRebind ||
		selection.State == nil || d.manager.options.RebindCurrentStateRepository == nil {
		return "", errors.New("generated ingress typed current attestation is unavailable")
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	runtime := managerGatewayCurrentPhysicalRuntime{manager: d.manager, hostProbe: probeGatewayV2HostStatus,
		containerProbe: func(probeCtx context.Context, id, address string, port uint16, host, challenge string) bool {
			return d.manager.probeGatewayV2ContainerChallenge(probeCtx, id, address, port, host, challenge)
		}}
	firstSnapshot, err := d.manager.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || firstSnapshot.Active == nil || firstSnapshot.Phase != appaccess.GatewayRebindDatabaseCommitted ||
		!firstSnapshot.DatabaseCommitObserved {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := runtime.observe(ctx, target)
	if err != nil || first.Outcome != gatewayCurrentPhysicalStableServing || first.Runtime.ListenerAbsent {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	secondSnapshot, err := d.manager.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(firstSnapshot, secondSnapshot) {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	confirmed, err := d.manager.selectGatewayCurrentLocked(ctx, secondSnapshot)
	if err != nil || !sameGatewayCurrentSelection(selection, confirmed) {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := runtime.observe(ctx, target)
	if err != nil || !reflect.DeepEqual(first, second) {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	return canonicalDigest(struct {
		Purpose     string                             `json:"purpose"`
		Lineage     appaccess.GatewayCurrentLineageRef `json:"lineage"`
		StateDigest string                             `json:"stateDigest"`
		Physical    gatewayCurrentPhysicalAttestation  `json:"physical"`
	}{"hostd/generated-ingress/rebind/committed-current-attestation/v1", selection.Lineage,
		selection.State.Digest, second})
}
