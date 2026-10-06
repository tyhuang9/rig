package generatedingress

import "errors"

// proveGatewayCurrentNativeNoEffectAbortHistoryLocked is a withdrawal-only
// fallback when the ordinary history reader cannot load mutable native routes.
// It proves only a generation-zero native origin followed entirely by typed
// no-effect aborts. Other histories still require the ordinary strict scanner.
// No route selection or serving authority is returned.
func (m *Manager) proveGatewayCurrentNativeNoEffectAbortHistoryLocked(
	presence gatewayRebindProtectedPresenceSnapshot,
) error {
	invalid := errors.New("generated ingress immutable native abort history is incomplete")
	if m == nil || m.store == nil || !presence.present {
		return invalid
	}
	for name := range presence.files {
		_, _, current, err := parseGatewayCurrentRoutePresenceName(name)
		if err != nil || current {
			return invalid
		}
	}
	before, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || len(before.generations) != 1 || len(before.rebindIntents) == 0 {
		return invalid
	}
	native, ok := before.generations[0]
	if !ok || native.state.path == "" || native.journal.path == "" ||
		native.receipt.path != "" || native.abort.path != "" {
		return invalid
	}
	store, err := newGatewayUpgradeStateStore(m.options.DataRoot)
	if err != nil || native.state.path != store.v2Path || native.journal.path != store.journalPath {
		return invalid
	}
	journal, err := store.loadMigrationJournal()
	if err != nil || journal.Phase != gatewayPhaseCommitted ||
		(native.operationID != "" && native.operationID != journal.OperationID) {
		return invalid
	}
	seen := map[string]bool{journal.OperationID: true}
	for generation := uint64(1); generation <= uint64(len(before.rebindIntents)); generation++ {
		artifact, ok := before.rebindIntents[generation]
		if !ok || seen[artifact.operationID] || artifact.checkpoint.path == "" ||
			artifact.intent.path != "" || len(artifact.progress) != 0 ||
			!gatewayRebindTerminalArtifactV2(artifact.terminal.path) {
			return invalid
		}
		seen[artifact.operationID] = true
		checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(m.options.DataRoot,
			generation, artifact.operationID)
		if err != nil || checkpointStore.path != artifact.checkpoint.path {
			return invalid
		}
		checkpoint, err := checkpointStore.load()
		if err != nil || checkpoint.UpgradeState == nil ||
			!historicallyBoundGatewayUpgrade(*checkpoint.UpgradeState, journal) {
			return invalid
		}
		lineage, err := gatewayUpgradeCurrentLineage(gatewayUpgradeGenerationSelection{
			Store: store, Generation: 0, Existing: true, operationID: journal.OperationID,
			State: *checkpoint.UpgradeState, Journal: journal,
		})
		if err != nil || lineage != checkpoint.Lineage {
			return invalid
		}
		terminalStore, err := newGatewayRebindTerminalStoreV2(m.options.DataRoot, generation, artifact.operationID)
		if err != nil || terminalStore.path != artifact.terminal.path {
			return invalid
		}
		terminal, err := terminalStore.load()
		if err != nil || !gatewayRebindTerminalV2MatchesNoIntentHistory(terminal, checkpoint) {
			return invalid
		}
	}
	after, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		return invalid
	}
	confirmed, err := readGatewayRebindProtectedPresenceMode(m.options.DataRoot, false)
	if err != nil || !sameGatewayRebindProtectedPresence(presence, confirmed) {
		return invalid
	}
	return nil
}
