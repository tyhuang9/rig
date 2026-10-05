package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindFinalConfigCopyVersion = 1
	gatewayRebindFinalConfigCopyContext = "hostd/generated-ingress/rebind/final-config-copy/v1"
)

// gatewayRebindFinalConfigCopyBinding is the immutable sequence-twelve
// receipt. It references the complete sequence-eleven intent by digest rather
// than copying its route plan into protected history a second time.
type gatewayRebindFinalConfigCopyBinding struct {
	Version                    int    `json:"version"`
	Context                    string `json:"context"`
	FinalConfigIntentDigest    string `json:"finalConfigIntentDigest"`
	ProtectedIntentDigest      string `json:"protectedIntentDigest"`
	ProtectedPredecessorDigest string `json:"protectedPredecessorDigest"`
	SuccessorIdentityDigest    string `json:"successorIdentityDigest"`
	SequenceElevenDigest       string `json:"sequenceElevenDigest"`
	PriorProgressDigest        string `json:"priorProgressDigest"`
	CopyEffectDigest           string `json:"copyEffectDigest"`
}

type gatewayRebindFinalConfigCopyDriver interface {
	gatewayRebindStageServingAttestor
	finalConfigVolumeInventory(context.Context, gatewayRebindProtectedIntent,
		gatewayRebindStageIntent, []byte, []byte) (gatewayRebindFinalConfigInventory, error)
	copyFinalConfig(context.Context, gatewayRebindProtectedIntent, gatewayRebindStageIntent, []byte) error
}

func gatewayRebindFinalConfigCopyBindingFor(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord,
) (gatewayRebindFinalConfigCopyBinding, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Sequence != 11 || previous.Phase != gatewayRebindProgressFinalConfigIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.StageServing == nil || previous.Stage.FinalConfigIntent == nil ||
		previous.Stage.FinalConfigCopy != nil {
		return gatewayRebindFinalConfigCopyBinding{}, errors.New("invalid generated ingress rebind final config copy input")
	}
	finalIntentDigest, err := canonicalDigest(*previous.Stage.FinalConfigIntent)
	if err != nil {
		return gatewayRebindFinalConfigCopyBinding{}, errors.New("invalid generated ingress rebind final config copy input")
	}
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil {
		return gatewayRebindFinalConfigCopyBinding{}, errors.New("invalid generated ingress rebind final config copy input")
	}
	value := gatewayRebindFinalConfigCopyBinding{
		Version: gatewayRebindFinalConfigCopyVersion, Context: gatewayRebindFinalConfigCopyContext,
		FinalConfigIntentDigest: finalIntentDigest, ProtectedIntentDigest: intent.Digest,
		ProtectedPredecessorDigest: predecessorDigest,
		SuccessorIdentityDigest:    intent.Intent.Identity.Digest,
		SequenceElevenDigest:       previous.Digest, PriorProgressDigest: previous.Digest,
	}
	value.CopyEffectDigest, err = gatewayRebindFinalConfigCopyEffectDigest(value)
	if err != nil || !validGatewayRebindFinalConfigCopyBindingValue(value) {
		return gatewayRebindFinalConfigCopyBinding{}, errors.New("invalid generated ingress rebind final config copy input")
	}
	return value, nil
}

func gatewayRebindFinalConfigCopyEffectDigest(value gatewayRebindFinalConfigCopyBinding) (string, error) {
	value.CopyEffectDigest = ""
	return canonicalDigest(value)
}

func validGatewayRebindFinalConfigCopyBindingValue(value gatewayRebindFinalConfigCopyBinding) bool {
	if value.Version != gatewayRebindFinalConfigCopyVersion || value.Context != gatewayRebindFinalConfigCopyContext ||
		!validSHA256(value.FinalConfigIntentDigest) || !validSHA256(value.ProtectedIntentDigest) ||
		!validSHA256(value.ProtectedPredecessorDigest) || !validSHA256(value.SuccessorIdentityDigest) ||
		!validSHA256(value.SequenceElevenDigest) || value.PriorProgressDigest != value.SequenceElevenDigest ||
		!validSHA256(value.CopyEffectDigest) {
		return false
	}
	digest, err := gatewayRebindFinalConfigCopyEffectDigest(value)
	return err == nil && digest == value.CopyEffectDigest
}

func validGatewayRebindFinalConfigCopyBinding(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalConfigCopyBinding,
) bool {
	expected, err := gatewayRebindFinalConfigCopyBindingFor(intent, previous)
	return err == nil && validGatewayRebindFinalConfigCopyBindingValue(value) && value == expected
}

type gatewayRebindFinalConfigCopyAttestation struct {
	Serving      gatewayRebindStageServingAttestation
	RuntimeHeads []appaccess.GatewayRebindRuntimeHead
	Inventory    gatewayRebindFinalConfigInventory
	Intent       gatewayRebindFinalConfigIntentBinding
	Receipt      gatewayRebindFinalConfigCopyBinding
}

// copyGatewayRebindSuccessorFinalConfig is private. It copies only the exact
// sequence-eleven active configuration into the inactive active.json file in
// the serving stage container and
// appends sequence twelve after exact readback. It does not reload or restart
// Caddy, probe an application response, publish a route, or write SQLite.
func (m *Manager) copyGatewayRebindSuccessorFinalConfig(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.copyGatewayRebindSuccessorFinalConfigWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindFinalConfigCopyDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) copyGatewayRebindSuccessorFinalConfigWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindFinalConfigCopyDriver,
	occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	if m == nil || ctx == nil || repository == nil || inspectDocker == nil || driver == nil ||
		reads.network.candidates == nil || reads.network.host == nil || reads.network.docker == nil ||
		reads.dockerIDs == nil || !validGatewayRebindProgressTime(occurredAt) {
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

	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 11 || len(history.Progress) > 12 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.StageServing == nil || stage.FinalConfigIntent == nil ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest || !validGatewayRebindStageIntent(*stage) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[10].Record
	binding, err := gatewayRebindFinalConfigCopyBindingFor(intent, previous)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 12 {
		if stage.FinalConfigCopy == nil || *stage.FinalConfigCopy != binding ||
			m.attestGatewayRebindFinalConfigCopyLocked(ctx, repository, reads, inspectDocker, driver,
				intent, *stage, binding, 12, gatewayRebindFinalConfigInventoryExactPair) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.FinalConfigCopy != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	active, err := gatewayRebindFinalConfigBytes(intent, stage.FinalConfigIntent.RoutePlan)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(active)
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 12)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindFinalConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 11)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindFinalConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 11)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindFinalConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 11)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindFinalConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 11)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	switch first.Inventory {
	case gatewayRebindFinalConfigInventoryStageOnly:
		if err := driver.copyFinalConfig(ctx, intent, *stage, append([]byte(nil), active...)); err != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
	case gatewayRebindFinalConfigInventoryExactPair:
		// A fresh coordinator may adopt an exact copy whose acknowledgement was
		// lost. Every protected, SQL, Docker, host and runtime-head proof above
		// was repeated before accepting this state.
	default:
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := m.attestGatewayRebindFinalConfigCopyLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *stage, binding, 11, gatewayRebindFinalConfigInventoryExactPair); err != nil {
		return err
	}
	record, err := newGatewayRebindFinalConfigCopyProgress(intent, previous, binding, occurredAt)
	if err != nil || progressStore.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindFinalConfigCopyLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *record.Stage, binding, 12, gatewayRebindFinalConfigInventoryExactPair)
}

func (m *Manager) attestGatewayRebindFinalConfigCopyLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindFinalConfigCopyDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindFinalConfigCopyBinding, progressCount uint64,
	want gatewayRebindFinalConfigInventory,
) error {
	first, err := m.readGatewayRebindFinalConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil || first.Inventory != want {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := m.readGatewayRebindFinalConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil || second.Inventory != want || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	finalAnchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !gatewayRebindEffectBoundaryAnchorMatchesObservation(finalAnchor,
		gatewayRebindEffectBoundaryObservation{database: first.Serving.Anchor.database,
			predecessor: first.Serving.Anchor.predecessor, intent: first.Serving.Anchor.intent,
			source: first.Serving.Anchor.source, databaseDigest: first.Serving.Anchor.databaseDigest,
			sourceDigest: first.Serving.Anchor.sourceDigest, progressCount: first.Serving.Anchor.progressCount,
			progressDigest: first.Serving.Anchor.progressDigest}) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	finalHeads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(first.RuntimeHeads, finalHeads) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (m *Manager) readGatewayRebindFinalConfigCopyAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindFinalConfigCopyDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindFinalConfigCopyBinding, progressCount uint64,
) (gatewayRebindFinalConfigCopyAttestation, error) {
	if progressCount != 11 && progressCount != 12 || !validGatewayRebindStageIntent(stage) {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) != int(progressCount) || len(history.Progress) < 11 ||
		!reflect.DeepEqual(history.Intents[0].Intent, intent) ||
		history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	sequenceEleven := history.Progress[10].Record
	if !validGatewayRebindFinalConfigCopyBinding(intent, sequenceEleven, binding) ||
		sequenceEleven.Stage == nil || sequenceEleven.Stage.FinalConfigIntent == nil {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	switch progressCount {
	case 11:
		if stage.FinalConfigCopy != nil || !reflect.DeepEqual(stage, *sequenceEleven.Stage) {
			return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
		}
	case 12:
		if stage.FinalConfigCopy == nil || *stage.FinalConfigCopy != binding {
			return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
		}
		withoutReceipt := stage
		withoutReceipt.FinalConfigCopy = nil
		if !reflect.DeepEqual(withoutReceipt, *sequenceEleven.Stage) {
			return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
		}
	}
	serving, err := m.readGatewayRebindStageServingAttestationForFinalConfigCopy(ctx, repository, reads, inspectDocker,
		driver, intent, stage, stage.StageServing, progressCount)
	if err != nil {
		return gatewayRebindFinalConfigCopyAttestation{}, err
	}
	heads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	intentBinding, err := gatewayRebindFinalConfigIntentBindingFor(intent, serving.Anchor.predecessor,
		heads, history.Progress[9].Record)
	if err != nil || !reflect.DeepEqual(intentBinding, *sequenceEleven.Stage.FinalConfigIntent) ||
		!reflect.DeepEqual(intentBinding, *stage.FinalConfigIntent) {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	stageBody, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(stageBody)
	activeBody, err := gatewayRebindFinalConfigBytes(intent, intentBinding.RoutePlan)
	if err != nil {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(activeBody)
	inventory, err := driver.finalConfigVolumeInventory(ctx, intent, stage, stageBody, activeBody)
	if err != nil || (inventory != gatewayRebindFinalConfigInventoryStageOnly &&
		inventory != gatewayRebindFinalConfigInventoryExactPair) || ctx.Err() != nil {
		return gatewayRebindFinalConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindFinalConfigCopyAttestation{
		Serving: serving, RuntimeHeads: heads, Inventory: inventory,
		Intent: intentBinding, Receipt: binding,
	}, nil
}
