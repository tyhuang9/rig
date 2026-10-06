package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindTypedAttemptBoundary struct {
	Snapshot  appaccess.GatewayRebindRecoverySnapshot
	Selection gatewayCurrentSelection
	History   gatewayRebindProtectedIntentHistory
	Progress  []gatewayRebindProgressRecord
}

// readGatewayRebindTypedAttemptBoundaryLocked is the active-attempt authority
// check used immediately before and after every typed physical effect. It is
// deliberately separate from the ordinary current driver: an admitted
// successor keeps the predecessor SQL-selected while ordinary mutation must
// remain fenced. This reader proves that exact active claim, typed protected
// prefix, SQL recovery direction, and predecessor selection together.
func (m *Manager) readGatewayRebindTypedAttemptBoundaryLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) (gatewayRebindTypedAttemptBoundary, error) {
	invalid := func() (gatewayRebindTypedAttemptBoundary, error) {
		return gatewayRebindTypedAttemptBoundary{}, errors.New("generated ingress typed rebind effect authority changed")
	}
	if m == nil || ctx == nil || ctx.Err() != nil || !validGatewayRebindPhysicalReconcileRequest(request) ||
		m.options.RebindCurrentStateRepository == nil {
		return invalid()
	}
	first, err := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !gatewayRebindTypedSnapshotMatchesRequest(first, request) {
		return invalid()
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, first)
	if err != nil || selection.Lineage != request.Attempt.Claim.Spec.Predecessor.Lineage {
		return invalid()
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return invalid()
	}
	progress, err := gatewayRebindTypedAttemptHistory(request, history)
	if err != nil {
		return invalid()
	}
	second, err := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, second) || ctx.Err() != nil {
		return invalid()
	}
	confirmedSelection, err := m.selectGatewayCurrentLocked(ctx, second)
	if err != nil || !sameGatewayCurrentSelection(selection, confirmedSelection) {
		return invalid()
	}
	confirmedHistory, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !sameGatewayRebindCurrentHistory(history, confirmedHistory) {
		return invalid()
	}
	return gatewayRebindTypedAttemptBoundary{Snapshot: first, Selection: selection,
		History: history, Progress: progress}, nil
}

func gatewayRebindTypedSnapshotMatchesRequest(snapshot appaccess.GatewayRebindRecoverySnapshot,
	request gatewayRebindPhysicalReconcileRequest,
) bool {
	if snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		!sameGatewayRebindClaimV2Admission(*snapshot.Active.Claim.V2, request.Attempt.Claim) ||
		snapshot.Phase != request.SQLPhase || snapshot.RollbackAllowed != request.RollbackAllowed ||
		snapshot.DatabaseCommitObserved != request.DatabaseCommitObserved {
		return false
	}
	if request.DatabaseCommitObserved {
		if snapshot.DatabaseCommittedEvent == nil ||
			snapshot.DatabaseCommittedEvent.OperationID != request.Attempt.Claim.Spec.OperationID ||
			snapshot.DatabaseCommittedEvent.State != appaccess.GatewayRebindDatabaseCommitted {
			return false
		}
	} else if snapshot.DatabaseCommittedEvent != nil {
		return false
	}
	return snapshot.CurrentSource != nil &&
		*snapshot.CurrentSource == gatewayCurrentAuthority(request.Attempt.Claim.Spec.Predecessor.Lineage)
}

func gatewayRebindTypedAttemptHistory(request gatewayRebindPhysicalReconcileRequest,
	history gatewayRebindProtectedIntentHistory,
) ([]gatewayRebindProgressRecord, error) {
	claim := request.Attempt.Claim
	generation, operationID := claim.Spec.SuccessorProtectedGeneration, claim.Spec.OperationID
	checkpointCount, intentCount := 0, 0
	for _, selected := range history.Checkpoints {
		if selected.Generation == generation && selected.Checkpoint.OperationID == operationID {
			checkpointCount++
			if !reflect.DeepEqual(selected.Checkpoint, request.Attempt.Checkpoint) {
				return nil, errors.New("typed predecessor checkpoint changed")
			}
		}
	}
	for _, selected := range history.IntentsV2 {
		if selected.Generation == generation && selected.Intent.OperationID == operationID {
			intentCount++
			if !reflect.DeepEqual(selected.Intent, request.Attempt.Intent) {
				return nil, errors.New("typed protected intent changed")
			}
		}
	}
	if checkpointCount != 1 || intentCount != 1 {
		return nil, errors.New("typed attempt history is incomplete")
	}
	progress := make([]gatewayRebindProgressRecord, 0, 17)
	for _, selected := range history.Progress {
		if selected.Generation == generation && selected.Record.OperationID == operationID {
			progress = append(progress, selected.Record)
		}
	}
	sort.Slice(progress, func(i, j int) bool { return progress[i].Sequence < progress[j].Sequence })
	if len(progress) == 0 || !reflect.DeepEqual(progress[0], request.Attempt.Progress) ||
		!gatewayRebindTypedProgressMatchesCheckpoint(request.Attempt.Intent, request.Attempt.Checkpoint, progress) {
		return nil, errors.New("typed progress prefix is invalid")
	}
	var terminals []gatewayRebindTerminalReceiptV2
	for _, selected := range history.TerminalsV2 {
		if selected.Generation == generation && selected.Receipt.OperationID == operationID {
			terminals = append(terminals, selected.Receipt)
		}
	}
	if request.Terminal == nil {
		if len(terminals) != 0 {
			return nil, errors.New("typed terminal appeared before physical decision")
		}
	} else if len(terminals) != 1 || !reflect.DeepEqual(terminals[0], *request.Terminal) {
		return nil, errors.New("typed terminal does not match recovery direction")
	}
	return progress, nil
}
