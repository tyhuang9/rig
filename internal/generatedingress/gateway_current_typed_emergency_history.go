package generatedingress

import (
	"errors"
	"path/filepath"
	"strings"
)

// gatewayCurrentOwnedStopHistoryLocked is reserved for withdrawal. The typed
// fallback validates immutable ownership without reading today's native route
// state. Its Source is deliberately empty and must never authorize serving.
func (m *Manager) gatewayCurrentOwnedStopHistoryLocked() (gatewayRebindProtectedIntentHistory, error) {
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil {
		return history, nil
	}
	return m.scanGatewayCurrentTypedOwnedStopHistoryLocked()
}

func (m *Manager) scanGatewayCurrentTypedOwnedStopHistoryLocked() (gatewayRebindProtectedIntentHistory, error) {
	return m.scanGatewayCurrentTypedWithdrawalHistoryLocked(func(dataRoot string) (gatewayRebindProtectedPresenceSnapshot, error) {
		return readGatewayRebindProtectedPresenceMode(dataRoot, false)
	})
}

func (m *Manager) scanGatewayCurrentTypedWithdrawalHistoryLocked(readPresence func(string) (gatewayRebindProtectedPresenceSnapshot, error)) (gatewayRebindProtectedIntentHistory, error) {
	invalid := errors.New("generated ingress typed withdrawal history is incomplete")
	if m == nil || m.store == nil {
		return gatewayRebindProtectedIntentHistory{}, invalid
	}
	presence, err := readPresence(m.options.DataRoot)
	if err != nil || !presence.present {
		return gatewayRebindProtectedIntentHistory{}, invalid
	}
	before, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || len(before.rebindIntents) == 0 {
		return gatewayRebindProtectedIntentHistory{}, invalid
	}
	predecessor, seen, err := m.gatewayCurrentTypedOwnedStopNativeOriginLocked(before)
	if err != nil {
		return gatewayRebindProtectedIntentHistory{}, err
	}
	result := gatewayRebindProtectedIntentHistory{Predecessor: predecessor}
	for offset := 0; offset < len(before.rebindIntents); offset++ {
		generation := predecessor.Generation + 1 + uint64(offset)
		artifact, exists := before.rebindIntents[generation]
		if !exists || seen[artifact.operationID] || artifact.checkpoint.path == "" {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		seen[artifact.operationID] = true
		checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(m.options.DataRoot, generation, artifact.operationID)
		if err != nil || checkpointStore.path != artifact.checkpoint.path {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		checkpoint, err := checkpointStore.load()
		if err != nil || !gatewayRebindCheckpointMatchesProtectedHistory(checkpoint, predecessor, result) ||
			(checkpoint.UpgradeState != nil && !historicallyBoundGatewayUpgrade(*checkpoint.UpgradeState, predecessor.Journal)) {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		result.Checkpoints = append(result.Checkpoints, gatewayRebindPredecessorCheckpointSelection{
			Store: checkpointStore, Generation: generation, Checkpoint: checkpoint, Existing: true,
		})
		var intent gatewayRebindProtectedIntentV2
		var progress []gatewayRebindProgressSelection
		if artifact.intent.path == "" {
			if len(artifact.progress) != 0 {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
		} else {
			if !strings.HasPrefix(filepath.Base(artifact.intent.path), gatewayRebindProtectedIntentFilenamePrefixV2) {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
			store, err := newGatewayRebindProtectedIntentV2Store(m.options.DataRoot, generation, artifact.operationID)
			if err != nil || store.path != artifact.intent.path {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
			intent, err = store.load()
			if err != nil || intent.Generation != checkpoint.Generation || intent.Predecessor != checkpoint.sourceRef() {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
			result.IntentsV2 = append(result.IntentsV2, gatewayRebindProtectedIntentV2Selection{
				Store: store, Generation: generation, Intent: intent, Existing: true,
			})
			progress, err = scanGatewayRebindProgressForIntentV2(m.options.DataRoot, intent, checkpoint, artifact)
			if err != nil || !gatewayRebindTypedProgressMatchesPredecessorLocalPort(result, intent, progress) {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
			result.Progress = append(result.Progress, progress...)
		}
		if artifact.terminal.path == "" {
			if offset != len(before.rebindIntents)-1 {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
			// Retain a valid active tail as unresolved so exact older owners may
			// be withdrawn without declaring that all possible traffic is absent.
			continue
		}
		if !gatewayRebindTerminalArtifactV2(artifact.terminal.path) {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		terminalStore, err := newGatewayRebindTerminalStoreV2(m.options.DataRoot, generation, artifact.operationID)
		if err != nil || terminalStore.path != artifact.terminal.path {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		terminal, err := terminalStore.load()
		if err != nil {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		if artifact.intent.path == "" {
			if !gatewayRebindTerminalV2MatchesNoIntentHistory(terminal, checkpoint) {
				return gatewayRebindProtectedIntentHistory{}, invalid
			}
		} else if !gatewayRebindTerminalV2MatchesIntentHistory(terminal, intent, checkpoint, progress) {
			return gatewayRebindProtectedIntentHistory{}, invalid
		}
		result.TerminalsV2 = append(result.TerminalsV2, gatewayRebindTerminalSelectionV2{
			Store: terminalStore, Generation: generation, Receipt: terminal, Existing: true,
		})
	}
	after, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		return gatewayRebindProtectedIntentHistory{}, invalid
	}
	confirmed, err := readPresence(m.options.DataRoot)
	if err != nil || !sameGatewayRebindProtectedPresence(presence, confirmed) {
		return gatewayRebindProtectedIntentHistory{}, invalid
	}
	return result, nil
}

// Retired native generations retain their normal historical validation. Only
// the committed native tail substitutes its exact frozen checkpoint state for
// mutable route bytes; its journal and complete prior history stay mandatory.
func (m *Manager) gatewayCurrentTypedOwnedStopNativeOriginLocked(snapshot gatewayHistorySnapshot) (
	gatewayUpgradeGenerationSelection, map[string]bool, error,
) {
	invalid := errors.New("generated ingress typed withdrawal native origin is invalid")
	if len(snapshot.generations) == 0 {
		return gatewayUpgradeGenerationSelection{}, nil, invalid
	}
	seen := make(map[string]bool, len(snapshot.generations))
	for index := 0; index < len(snapshot.generations); index++ {
		generation := uint64(index)
		artifact, exists := snapshot.generations[generation]
		if !exists {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		var store *gatewayUpgradeStateStore
		var err error
		if generation == 0 {
			store, err = newGatewayUpgradeStateStore(m.options.DataRoot)
		} else {
			store, err = newGatewayUpgradeGenerationStore(m.options.DataRoot, generation, artifact.operationID)
		}
		if err != nil || (artifact.state.path != "" && artifact.state.path != store.v2Path) ||
			(artifact.journal.path != "" && artifact.journal.path != store.journalPath) ||
			(artifact.receipt.path != "" && artifact.receipt.path != store.receiptPath) ||
			(artifact.abort.path != "" && artifact.abort.path != store.abortPath) {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		last := index == len(snapshot.generations)-1
		if artifact.abort.path != "" {
			receipt, err := store.loadPreJournalAbortReceipt()
			if err != nil || last || seen[receipt.OperationID] {
				return gatewayUpgradeGenerationSelection{}, nil, invalid
			}
			seen[receipt.OperationID] = true
			continue
		}
		if artifact.state.path == "" || artifact.journal.path == "" {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		journal, err := store.loadMigrationJournal()
		if err != nil || seen[journal.OperationID] ||
			(artifact.operationID != "" && journal.OperationID != artifact.operationID) {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		seen[journal.OperationID] = true
		if !last {
			state, historical, err := store.loadHistoricalBoundUpgrade(journal.OperationID)
			if err != nil || historical.Phase != gatewayPhaseRolledBack || artifact.receipt.path == "" {
				return gatewayUpgradeGenerationSelection{}, nil, invalid
			}
			if _, err := store.loadRollbackRetirementReceipt(state, historical); err != nil {
				return gatewayUpgradeGenerationSelection{}, nil, invalid
			}
			continue
		}
		first, exists := snapshot.rebindIntents[generation+1]
		if !exists || first.checkpoint.path == "" || journal.Phase != gatewayPhaseCommitted || artifact.receipt.path != "" {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(m.options.DataRoot, generation+1, first.operationID)
		if err != nil || checkpointStore.path != first.checkpoint.path {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		checkpoint, err := checkpointStore.load()
		if err != nil || checkpoint.UpgradeState == nil || !historicallyBoundGatewayUpgrade(*checkpoint.UpgradeState, journal) {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		predecessor := gatewayUpgradeGenerationSelection{Store: store, Generation: generation,
			State: *checkpoint.UpgradeState, Journal: journal, Existing: true, operationID: journal.OperationID}
		if !gatewayRebindCheckpointMatchesUpgradePredecessor(checkpoint, predecessor) {
			return gatewayUpgradeGenerationSelection{}, nil, invalid
		}
		return predecessor, seen, nil
	}
	return gatewayUpgradeGenerationSelection{}, nil, invalid
}
