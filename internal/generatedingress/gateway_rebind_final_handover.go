package generatedingress

import (
	"context"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindFinalHandoverMode uint8

const (
	gatewayRebindFinalHandoverAdvance gatewayRebindFinalHandoverMode = iota + 1
	gatewayRebindFinalHandoverRollback
)

type gatewayRebindFinalHandoverAttestation struct {
	History      gatewayRebindProtectedIntentHistory
	Anchor       gatewayRebindEffectBoundaryAnchor
	RuntimeHeads []appaccess.GatewayRebindRuntimeHead
	Observation  gatewayRebindFinalHandoverObservation
}

type gatewayRebindFinalHandoverRun struct {
	manager        *Manager
	repository     *appaccess.Repository
	driver         gatewayRebindFinalHandoverDriver
	requestedAt    time.Time
	wroteProgress  bool
	checkpoint     func()
	checkpointUsed bool
}

func (m *Manager) handoverGatewayRebindFinal(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) error {
	return m.handoverGatewayRebindFinalWithDriver(ctx, repository,
		newManagerGatewayRebindFinalHandoverDriver(m, reads, inspectDocker), occurredAt, checkpoint)
}

func (m *Manager) rollbackGatewayRebindFinalHandover(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) error {
	return m.rollbackGatewayRebindFinalHandoverWithDriver(ctx, repository,
		newManagerGatewayRebindFinalHandoverDriver(m, reads, inspectDocker), occurredAt, checkpoint)
}

func (m *Manager) handoverGatewayRebindFinalWithDriver(ctx context.Context,
	repository *appaccess.Repository, driver gatewayRebindFinalHandoverDriver,
	occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.runGatewayRebindFinalHandoverWithDriver(ctx, repository, driver, occurredAt,
		checkpoint, gatewayRebindFinalHandoverAdvance)
}

func (m *Manager) rollbackGatewayRebindFinalHandoverWithDriver(ctx context.Context,
	repository *appaccess.Repository, driver gatewayRebindFinalHandoverDriver,
	occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.runGatewayRebindFinalHandoverWithDriver(ctx, repository, driver, occurredAt,
		checkpoint, gatewayRebindFinalHandoverRollback)
}

func (m *Manager) runGatewayRebindFinalHandoverWithDriver(ctx context.Context,
	repository *appaccess.Repository, driver gatewayRebindFinalHandoverDriver,
	occurredAt time.Time, checkpoint func(), mode gatewayRebindFinalHandoverMode,
) (resultErr error) {
	if m == nil || ctx == nil || repository == nil || driver == nil ||
		!validGatewayRebindProgressTime(occurredAt) ||
		(mode != gatewayRebindFinalHandoverAdvance && mode != gatewayRebindFinalHandoverRollback) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRaw(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			return &Error{Code: DiagnosticRouteUnresolved}
		}
		return err
	}
	defer func() {
		if releaseErr := releaseGateway(); releaseErr != nil {
			resultErr = gatewayRebindEffectBoundaryError(ctx)
		}
		if releaseErr := releaseEffects(); releaseErr != nil {
			resultErr = gatewayRebindEffectBoundaryError(ctx)
		}
	}()

	run := &gatewayRebindFinalHandoverRun{
		manager: m, repository: repository, driver: driver, requestedAt: occurredAt, checkpoint: checkpoint,
	}
	return run.execute(ctx, mode)
}

func (r *gatewayRebindFinalHandoverRun) execute(ctx context.Context,
	mode gatewayRebindFinalHandoverMode,
) error {
	for step := 0; step < 32; step++ {
		history, err := r.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(history.Intents) != 1 || len(history.Progress) < 12 ||
			len(history.Progress) > gatewayRebindProgressMaximumSequence || len(history.Terminals) > 1 {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		last := history.Progress[len(history.Progress)-1].Record
		if len(history.Terminals) == 1 {
			return r.replayTerminal(ctx, history, last)
		}
		if last.Phase == gatewayRebindProgressHandoverCommitted ||
			last.Phase == gatewayRebindProgressHandoverRolledBack {
			return r.installTerminalReceipt(ctx, history, last)
		}
		if last.Phase == gatewayRebindProgressRollbackIntent {
			if err := r.advanceRollback(ctx, last); err != nil {
				return err
			}
			continue
		}
		if mode == gatewayRebindFinalHandoverRollback {
			if !gatewayRebindFinalHandoverRollbackSourcePhase(last.Phase) {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			attestation, err := r.attestStable(ctx, last, "", true)
			predecessorRoutesDigest, digestErr := gatewayRebindFinalHandoverPredecessorRoutesDigest(history.Predecessor)
			if err != nil || digestErr != nil ||
				!gatewayRebindFinalHandoverRollbackAdmission(attestation.Observation, last, predecessorRoutesDigest) {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			record, err := newGatewayRebindFinalHandoverRollbackProgress(
				history.Intents[0].Intent, last, r.nextProgressTime(last))
			if err != nil || r.installProgress(ctx, record) != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			r.wroteProgress = true
			continue
		}
		if err := r.advanceForward(ctx, history, last); err != nil {
			return err
		}
	}
	return gatewayRebindEffectBoundaryError(ctx)
}

func (r *gatewayRebindFinalHandoverRun) advanceForward(ctx context.Context,
	history gatewayRebindProtectedIntentHistory, last gatewayRebindProgressRecord,
) error {
	intent := history.Intents[0].Intent
	switch last.Phase {
	case gatewayRebindProgressFinalConfigCopied:
		attestation, err := r.attestStable(ctx, last, "", true)
		if err != nil || attestation.Observation.PredecessorAddress == gatewayRebindPredecessorAddressAmbiguous {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		handoverContext := gatewayRebindFinalHandoverContext{
			Intent: intent, SequenceTwelve: last, Predecessor: history.Predecessor, Source: history.Source,
			Phase: last.Phase,
		}
		plan, err := gatewayRebindFinalHandoverPlanFor(handoverContext, attestation.Observation)
		if err != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		record, err := newGatewayRebindFinalHandoverIntentProgress(intent, last, plan, r.nextProgressTime(last))
		if err != nil || r.installProgress(ctx, record) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		r.wroteProgress = true
		return nil

	case gatewayRebindProgressFinalHandoverIntent:
		attestation, err := r.attestStable(ctx, last, "", true)
		if err != nil || last.Handover == nil || last.Handover.Plan == nil ||
			!gatewayRebindFinalHandoverUnboundState(attestation.Observation, *last.Handover.Plan) {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		switch attestation.Observation.Stage {
		case gatewayRebindHandoverContainerRunning:
			if err := r.driver.stopStage(ctx, r.handoverContext(history, last, "")); err != nil || ctx.Err() != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
				return gatewayRebindFinalHandoverUnboundState(value, *last.Handover.Plan) &&
					value.Stage == gatewayRebindHandoverContainerStopped
			})
		case gatewayRebindHandoverContainerStopped:
			if err := r.driver.removeStage(ctx, r.handoverContext(history, last, "")); err != nil || ctx.Err() != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
				return gatewayRebindFinalHandoverUnboundState(value, *last.Handover.Plan) &&
					value.Stage == gatewayRebindHandoverContainerAbsent
			})
		case gatewayRebindHandoverContainerAbsent:
			if attestation.Observation.Final != gatewayRebindHandoverContainerAbsent {
				// A final container whose create acknowledgement was lost has no
				// durable ID binding and cannot be recovered by name.
				return gatewayRebindEffectBoundaryError(ctx)
			}
			createdID, createErr := r.driver.createFinal(ctx, r.handoverContext(history, last, ""))
			if createErr != nil || ctx.Err() != nil || !validContainerID(createdID) || normalizeID(createdID) != createdID {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			post, err := r.attestStable(ctx, last, createdID, false)
			if err != nil || post.Observation.Stage != gatewayRebindHandoverContainerAbsent ||
				post.Observation.Final != gatewayRebindHandoverContainerStopped || post.Observation.FinalID != createdID ||
				!gatewayRebindFinalHandoverPredecessorIsInitial(post.Observation, *last.Handover.Plan) {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			binding, err := gatewayRebindFinalContainerBindingFor(r.handoverContext(history, last, createdID), createdID)
			if err != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			record, err := newGatewayRebindFinalContainerBoundProgress(intent, last, binding, r.nextProgressTime(last))
			if err != nil || r.installProgress(ctx, record) != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			r.wroteProgress = true
			return nil
		default:
			return gatewayRebindEffectBoundaryError(ctx)
		}

	case gatewayRebindProgressFinalContainerBound:
		attestation, err := r.attestStable(ctx, last, "", true)
		if err != nil || last.Handover == nil || last.Handover.Plan == nil || last.Handover.Final == nil ||
			!gatewayRebindFinalHandoverReadyForCutover(attestation.Observation,
				*last.Handover.Plan, *last.Handover.Final) {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		record, err := newGatewayRebindFinalHandoverCutoverProgress(
			intent, last, attestation.Observation, r.nextProgressTime(last))
		if err != nil || r.installProgress(ctx, record) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		r.wroteProgress = true
		return nil

	case gatewayRebindProgressCutoverIntent:
		attestation, err := r.attestStable(ctx, last, "", true)
		if err != nil || last.Handover == nil || last.Handover.Plan == nil || last.Handover.Final == nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		if gatewayRebindFinalHandoverSuccessorIsServing(attestation.Observation,
			*last.Handover.Plan, *last.Handover.Final) {
			return r.installServingProgress(ctx, intent, last, attestation.Observation)
		}
		if gatewayRebindFinalHandoverReadyForCutover(attestation.Observation,
			*last.Handover.Plan, *last.Handover.Final) && attestation.Observation.PredecessorRunning {
			if err := r.driver.stopPredecessor(ctx, r.handoverContext(history, last, "")); err != nil || ctx.Err() != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
				return gatewayRebindFinalHandoverReadyToStart(value, *last.Handover.Plan, *last.Handover.Final)
			})
		}
		if !gatewayRebindFinalHandoverReadyToStart(attestation.Observation,
			*last.Handover.Plan, *last.Handover.Final) {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		if err := r.driver.startFinal(ctx, r.handoverContext(history, last, "")); err != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		post, err := r.attestStable(ctx, last, "", false)
		if err != nil || !gatewayRebindFinalHandoverSuccessorIsServing(post.Observation,
			*last.Handover.Plan, *last.Handover.Final) {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.installServingProgress(ctx, intent, last, post.Observation)

	case gatewayRebindProgressSuccessorServing:
		attestation, err := r.attestStable(ctx, last, "", true)
		if err != nil || last.Handover == nil || last.Handover.Plan == nil || last.Handover.Final == nil ||
			!gatewayRebindFinalHandoverSuccessorIsServing(attestation.Observation,
				*last.Handover.Plan, *last.Handover.Final) {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		record, err := newGatewayRebindFinalHandoverTerminalProgress(intent, last,
			gatewayRebindFinalHandoverOutcomeCommit, attestation.Observation, "", r.nextProgressTime(last))
		if err != nil || r.installProgress(ctx, record) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		r.wroteProgress = true
		return nil
	default:
		return gatewayRebindEffectBoundaryError(ctx)
	}
}

func (r *gatewayRebindFinalHandoverRun) advanceRollback(ctx context.Context,
	last gatewayRebindProgressRecord,
) error {
	history, err := r.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || last.Handover == nil || last.Handover.Plan == nil ||
		last.Handover.Rollback == nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	attestation, err := r.attestStable(ctx, last, "", true)
	predecessorRoutesDigest, digestErr := gatewayRebindFinalHandoverPredecessorRoutesDigest(history.Predecessor)
	if err != nil || digestErr != nil ||
		!gatewayRebindFinalHandoverRollbackAdmission(attestation.Observation, last, predecessorRoutesDigest) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	observation := attestation.Observation
	contextValue := r.handoverContext(history, last, "")
	if observation.Final == gatewayRebindHandoverContainerRunning {
		if last.Handover.Final == nil || observation.FinalID != last.Handover.Final.ID ||
			r.driver.stopFinal(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) &&
				value.Final == gatewayRebindHandoverContainerStopped
		})
	}
	if observation.Stage == gatewayRebindHandoverContainerRunning {
		if r.driver.stopStage(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) &&
				value.Stage == gatewayRebindHandoverContainerStopped
		})
	}
	if observation.Stage == gatewayRebindHandoverContainerStopped {
		if r.driver.removeStage(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) &&
				value.Stage == gatewayRebindHandoverContainerAbsent
		})
	}
	if last.Handover.Rollback.CutoverBegun && !observation.PredecessorRunning {
		if observation.Final == gatewayRebindHandoverContainerRunning ||
			observation.PredecessorAddress != gatewayRebindPredecessorAddressPresent ||
			r.driver.startPredecessor(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) && value.PredecessorRunning &&
				value.PredecessorAddress == gatewayRebindPredecessorAddressPresent &&
				value.PredecessorRoutesDigest == predecessorRoutesDigest
		})
	}
	if observation.Final == gatewayRebindHandoverContainerStopped {
		if last.Handover.Final == nil || observation.FinalID != last.Handover.Final.ID ||
			r.driver.removeFinal(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) &&
				value.Final == gatewayRebindHandoverContainerAbsent
		})
	}
	if observation.Stage != gatewayRebindHandoverContainerAbsent ||
		observation.Final != gatewayRebindHandoverContainerAbsent {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if observation.ConfigVolumePresent {
		if r.driver.removeConfigVolume(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) && !value.ConfigVolumePresent
		})
	}
	if observation.DataVolumePresent {
		if r.driver.removeDataVolume(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) && !value.DataVolumePresent
		})
	}
	if observation.IngressNetworkPresent {
		if r.driver.removeIngressNetwork(ctx, contextValue) != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return r.requirePostEffect(ctx, last, func(value gatewayRebindFinalHandoverObservation) bool {
			return gatewayRebindFinalHandoverRollbackAdmission(value, last, predecessorRoutesDigest) && !value.IngressNetworkPresent
		})
	}
	if !gatewayRebindFinalHandoverSuccessorIsAbsent(observation, *last.Handover.Plan,
		last.Handover.Rollback.CutoverBegun, predecessorRoutesDigest) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	record, err := newGatewayRebindFinalHandoverTerminalProgress(history.Intents[0].Intent, last,
		gatewayRebindFinalHandoverOutcomeAbort, observation, predecessorRoutesDigest, r.nextProgressTime(last))
	if err != nil || r.installProgress(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	r.wroteProgress = true
	return nil
}

func (r *gatewayRebindFinalHandoverRun) installServingProgress(ctx context.Context,
	intent gatewayRebindProtectedIntent, last gatewayRebindProgressRecord,
	observation gatewayRebindFinalHandoverObservation,
) error {
	record, err := newGatewayRebindFinalHandoverServingProgress(intent, last, observation, r.nextProgressTime(last))
	if err != nil || r.installProgress(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	r.wroteProgress = true
	return nil
}

func (r *gatewayRebindFinalHandoverRun) installProgress(ctx context.Context,
	record gatewayRebindProgressRecord,
) error {
	store, err := newGatewayRebindProgressStore(r.manager.options.DataRoot,
		record.Generation, record.OperationID, record.Sequence)
	if err != nil {
		return err
	}
	return store.installExact(ctx, record)
}

func (r *gatewayRebindFinalHandoverRun) replayTerminal(ctx context.Context,
	history gatewayRebindProtectedIntentHistory, last gatewayRebindProgressRecord,
) error {
	if len(history.Terminals) != 1 || !gatewayRebindFinalHandoverTerminalInstallPermitted(history,
		history.Terminals[0].Receipt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	attestation, err := r.attestStable(ctx, last, "", true)
	if err != nil || last.Handover == nil || last.Handover.Outcome == nil ||
		!reflect.DeepEqual(attestation.Observation, last.Handover.Outcome.Observation) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (r *gatewayRebindFinalHandoverRun) installTerminalReceipt(ctx context.Context,
	history gatewayRebindProtectedIntentHistory, last gatewayRebindProgressRecord,
) error {
	attestation, err := r.attestStable(ctx, last, "", true)
	if err != nil || last.Handover == nil || last.Handover.Outcome == nil ||
		!reflect.DeepEqual(attestation.Observation, last.Handover.Outcome.Observation) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	receipt, err := gatewayRebindFinalHandoverTerminalReceiptFor(history, r.nextProgressTime(last))
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	store, err := newGatewayRebindFinalHandoverTerminalStore(r.manager.options.DataRoot,
		receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(ctx, receipt) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	installed, err := r.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(installed.Terminals) != 1 ||
		!reflect.DeepEqual(installed.Terminals[0].Receipt, receipt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	readback, err := r.attestStable(ctx, last, "", false)
	if err != nil || !reflect.DeepEqual(readback.Observation, last.Handover.Outcome.Observation) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (r *gatewayRebindFinalHandoverRun) requirePostEffect(ctx context.Context,
	last gatewayRebindProgressRecord, predicate func(gatewayRebindFinalHandoverObservation) bool,
) error {
	attestation, err := r.attestStable(ctx, last, "", false)
	if err != nil || !predicate(attestation.Observation) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (r *gatewayRebindFinalHandoverRun) attestStable(ctx context.Context,
	last gatewayRebindProgressRecord, createdFinalID string, allowCheckpoint bool,
) (gatewayRebindFinalHandoverAttestation, error) {
	first, err := r.readAttestation(ctx, last, createdFinalID)
	if err != nil {
		return gatewayRebindFinalHandoverAttestation{}, err
	}
	if allowCheckpoint && !r.checkpointUsed && r.checkpoint != nil {
		r.checkpointUsed = true
		r.checkpoint()
	}
	if ctx.Err() != nil {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := r.readAttestation(ctx, last, createdFinalID)
	if err != nil || !gatewayRebindFinalHandoverAttestationsEqual(first, second) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return second, nil
}

func (r *gatewayRebindFinalHandoverRun) readAttestation(ctx context.Context,
	last gatewayRebindProgressRecord, createdFinalID string,
) (gatewayRebindFinalHandoverAttestation, error) {
	if ctx.Err() != nil {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := r.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 12 ||
		!reflect.DeepEqual(history.Progress[len(history.Progress)-1].Record, last) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	anchor, err := r.manager.readGatewayRebindEffectBoundaryAnchor(ctx, r.repository)
	if err != nil || !reflect.DeepEqual(anchor.intent, intent) ||
		!gatewayRebindGenerationSelectionEqual(anchor.predecessor, history.Predecessor) ||
		!reflect.DeepEqual(anchor.source, history.Source) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	count, digest, err := gatewayRebindEffectBoundaryProgressDigest(intent, history.Progress)
	if err != nil || anchor.progressCount != count || anchor.progressDigest != digest {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	heads, err := r.repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || len(history.Progress) < 11 || history.Progress[10].Record.Stage == nil ||
		history.Progress[10].Record.Stage.FinalConfigIntent == nil {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindFinalConfigIntentBindingFor(intent, history.Predecessor,
		heads, history.Progress[9].Record)
	if err != nil || !reflect.DeepEqual(binding, *history.Progress[10].Record.Stage.FinalConfigIntent) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	contextValue := r.handoverContext(history, last, createdFinalID)
	observation, err := r.driver.observeHandover(ctx, contextValue)
	if err != nil || ctx.Err() != nil || !validGatewayRebindFinalHandoverObservationValue(observation) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	finalAnchor, err := r.manager.readGatewayRebindEffectBoundaryAnchor(ctx, r.repository)
	if err != nil || !gatewayRebindFinalHandoverAnchorsEqual(anchor, finalAnchor) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	finalHeads, err := r.repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(heads, finalHeads) {
		return gatewayRebindFinalHandoverAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindFinalHandoverAttestation{
		History: history, Anchor: finalAnchor, RuntimeHeads: finalHeads, Observation: observation,
	}, nil
}

func (r *gatewayRebindFinalHandoverRun) handoverContext(history gatewayRebindProtectedIntentHistory,
	last gatewayRebindProgressRecord, createdFinalID string,
) gatewayRebindFinalHandoverContext {
	value := gatewayRebindFinalHandoverContext{
		Intent: history.Intents[0].Intent, SequenceTwelve: history.Progress[11].Record,
		Predecessor: history.Predecessor, Source: history.Source, Phase: last.Phase,
		CreatedFinalID: createdFinalID,
	}
	if last.Handover != nil {
		value.Plan = last.Handover.Plan
		value.Final = last.Handover.Final
	}
	return value
}

func gatewayRebindFinalHandoverAttestationsEqual(left, right gatewayRebindFinalHandoverAttestation) bool {
	return reflect.DeepEqual(left.History.Intents, right.History.Intents) &&
		reflect.DeepEqual(left.History.Progress, right.History.Progress) &&
		reflect.DeepEqual(left.History.Terminals, right.History.Terminals) &&
		gatewayRebindGenerationSelectionEqual(left.History.Predecessor, right.History.Predecessor) &&
		reflect.DeepEqual(left.History.Source, right.History.Source) &&
		gatewayRebindGenerationSelectionEqual(left.Anchor.predecessor, right.Anchor.predecessor) &&
		reflect.DeepEqual(left.Anchor.database, right.Anchor.database) &&
		reflect.DeepEqual(left.Anchor.intent, right.Anchor.intent) && reflect.DeepEqual(left.Anchor.source, right.Anchor.source) &&
		left.Anchor.databaseDigest == right.Anchor.databaseDigest && left.Anchor.sourceDigest == right.Anchor.sourceDigest &&
		left.Anchor.progressCount == right.Anchor.progressCount && left.Anchor.progressDigest == right.Anchor.progressDigest &&
		reflect.DeepEqual(left.RuntimeHeads, right.RuntimeHeads) && reflect.DeepEqual(left.Observation, right.Observation)
}

func gatewayRebindFinalHandoverAnchorsEqual(left, right gatewayRebindEffectBoundaryAnchor) bool {
	return reflect.DeepEqual(left.database, right.database) &&
		gatewayRebindGenerationSelectionEqual(left.predecessor, right.predecessor) &&
		reflect.DeepEqual(left.intent, right.intent) && reflect.DeepEqual(left.source, right.source) &&
		left.databaseDigest == right.databaseDigest && left.sourceDigest == right.sourceDigest &&
		left.progressCount == right.progressCount && left.progressDigest == right.progressDigest
}

func (r *gatewayRebindFinalHandoverRun) nextProgressTime(previous gatewayRebindProgressRecord) time.Time {
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil {
		return time.Time{}
	}
	if !r.wroteProgress {
		if r.requestedAt.After(previousAt) {
			return r.requestedAt
		}
		return time.Time{}
	}
	return previousAt.Add(time.Nanosecond)
}

func gatewayRebindFinalHandoverUnboundState(observation gatewayRebindFinalHandoverObservation,
	plan gatewayRebindFinalHandoverPlan,
) bool {
	if !validGatewayRebindFinalHandoverObservationValue(observation) ||
		!gatewayRebindFinalHandoverPredecessorIsInitial(observation, plan) ||
		observation.Final != gatewayRebindHandoverContainerAbsent || observation.FinalID != "" ||
		!observation.ConfigVolumePresent || !observation.DataVolumePresent || !observation.IngressNetworkPresent ||
		observation.RoutesDigest != "" ||
		!reflect.DeepEqual(observation.ApplicationNetworks, plan.ApplicationNetworks) {
		return false
	}
	if observation.Stage == gatewayRebindHandoverContainerAbsent {
		// No unbound helper container is created merely to read the volume.
		// Exact volume identity and zero foreign users remain proved; the newly
		// bound stopped final must reprove the exact config pair before cutover.
		return observation.ConfigDigest == ""
	}
	return observation.ConfigDigest == plan.FinalConfigDigest
}

func gatewayRebindFinalHandoverReadyToStart(observation gatewayRebindFinalHandoverObservation,
	plan gatewayRebindFinalHandoverPlan, final gatewayRebindFinalContainerBinding,
) bool {
	return validGatewayRebindFinalHandoverObservationValue(observation) &&
		observation.Stage == gatewayRebindHandoverContainerAbsent &&
		observation.Final == gatewayRebindHandoverContainerStopped && observation.FinalID == final.ID &&
		!observation.PredecessorRunning && observation.PredecessorAddress != gatewayRebindPredecessorAddressAmbiguous &&
		observation.ConfigVolumePresent && observation.DataVolumePresent && observation.IngressNetworkPresent &&
		observation.ConfigDigest == plan.FinalConfigDigest && observation.RoutesDigest == "" &&
		validSHA256(observation.PredecessorStopDigest) &&
		reflect.DeepEqual(observation.ApplicationNetworks, plan.ApplicationNetworks)
}

func gatewayRebindFinalHandoverRollbackAdmission(observation gatewayRebindFinalHandoverObservation,
	record gatewayRebindProgressRecord, predecessorRoutesDigest string,
) bool {
	if !validGatewayRebindFinalHandoverObservationValue(observation) || record.Handover == nil ||
		record.Handover.Plan == nil ||
		!reflect.DeepEqual(observation.ApplicationNetworks, record.Handover.Plan.ApplicationNetworks) {
		return false
	}
	if observation.Final != gatewayRebindHandoverContainerAbsent {
		if record.Handover.Final == nil || observation.FinalID != record.Handover.Final.ID {
			return false
		}
	}
	cutoverBegun := record.Handover.Cutover != nil
	if record.Handover.Rollback != nil {
		cutoverBegun = record.Handover.Rollback.CutoverBegun
	}
	if !cutoverBegun {
		return gatewayRebindFinalHandoverPredecessorIsInitial(observation, *record.Handover.Plan)
	}
	if observation.PredecessorAddress == gatewayRebindPredecessorAddressAmbiguous {
		return false
	}
	return !observation.PredecessorRunning ||
		(observation.PredecessorAddress == gatewayRebindPredecessorAddressPresent &&
			observation.PredecessorRoutesDigest == predecessorRoutesDigest)
}
