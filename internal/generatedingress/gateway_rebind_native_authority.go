package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// scanGatewayRebindCompletedProtectedHistoryLocked is the only production
// entry point for a terminal-complete protected census. The max-generation
// comparison relies on the scanner having already enforced contiguous rebind
// generations, terminal completion of every prior generation, and matching
// intent/progress identities for the last generation.
func (m *Manager) scanGatewayRebindCompletedProtectedHistoryLocked() (gatewayRebindProtectedIntentHistory, error) {
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !gatewayRebindScannedHistoryTerminalComplete(history) {
		return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind history is not terminal")
	}
	return history, nil
}

// gatewayRebindScannedHistoryTerminalComplete accepts only output from
// scanGatewayRebindProtectedIntentHistoryLocked. It is not a standalone
// validator for caller-constructed history.
func gatewayRebindScannedHistoryTerminalComplete(history gatewayRebindProtectedIntentHistory) bool {
	var latestAttempt, latestTerminal uint64
	for _, checkpoint := range history.Checkpoints {
		latestAttempt = max(latestAttempt, checkpoint.Generation)
	}
	for _, intent := range history.Intents {
		latestAttempt = max(latestAttempt, intent.Generation)
	}
	for _, intent := range history.IntentsV2 {
		latestAttempt = max(latestAttempt, intent.Generation)
	}
	for _, terminal := range history.Terminals {
		latestTerminal = max(latestTerminal, terminal.Generation)
	}
	for _, terminal := range history.TerminalsV2 {
		latestTerminal = max(latestTerminal, terminal.Generation)
	}
	return latestAttempt != 0 && latestAttempt == latestTerminal
}

func (m *Manager) committedV2AfterNativeRebindAbortLocked(ctx context.Context) (
	*gatewayUpgradeStateStore, gatewayV2RouteState, gatewayMigrationJournal, bool, error,
) {
	invalid := func() (*gatewayUpgradeStateStore, gatewayV2RouteState, gatewayMigrationJournal, bool, error) {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false,
			errors.New("generated ingress native current authority is unavailable")
	}
	if m == nil || ctx == nil || ctx.Err() != nil || m.options.RebindCurrentStateRepository == nil ||
		m.options.RebindFenceCheck == nil {
		return invalid()
	}
	presence, err := readGatewayRebindProtectedPresenceReadOnly(m.options.DataRoot)
	if err != nil || gatewayRebindProtectedPresenceHasCurrentRoute(presence) {
		return invalid()
	}
	repository := gatewayV2PreparedRebindStartupRepository{
		GatewayRebindCurrentStateRepository: m.options.RebindCurrentStateRepository,
		fenceCheck:                          m.options.RebindFenceCheck,
	}
	current, err := m.inspectGatewayRebindCurrentLocked(ctx, repository)
	if err != nil || !gatewayRebindNativeAbortCurrentInspection(current) {
		return invalid()
	}
	protected, err := m.scanGatewayRebindCompletedProtectedHistoryLocked()
	if err != nil || !gatewayRebindNativeAbortHistoryMatchesCurrent(protected, current) {
		return invalid()
	}
	history, _, err := m.scanGatewayUpgradeHistoryLockedModeRebindAware(false, "")
	if err != nil || !history.committed || history.store == nil || len(history.generations) == 0 {
		return invalid()
	}
	selected := history.generations[len(history.generations)-1]
	lineage, err := gatewayUpgradeCurrentLineage(selected)
	if err != nil || current.SelectedCurrentAuthority != gatewayCurrentAuthority(lineage) ||
		current.SelectedCurrentEvidence.Lineage != lineage ||
		current.SelectedCurrentEvidence.StateVersion != current.CurrentStateVersion ||
		current.SelectedCurrentEvidence.StateRevision != current.CurrentStateRevision ||
		current.SelectedCurrentEvidence.StateDigest != current.CurrentStateDigest {
		return invalid()
	}
	confirmedCurrent, err := m.inspectGatewayRebindCurrentLocked(ctx, repository)
	if err != nil || !reflect.DeepEqual(current, confirmedCurrent) {
		return invalid()
	}
	confirmedProtected, err := m.scanGatewayRebindCompletedProtectedHistoryLocked()
	if err != nil || !sameGatewayRebindCurrentHistory(protected, confirmedProtected) || ctx.Err() != nil {
		return invalid()
	}
	confirmedPresence, err := readGatewayRebindProtectedPresenceReadOnly(m.options.DataRoot)
	if err != nil || gatewayRebindProtectedPresenceHasCurrentRoute(confirmedPresence) ||
		!sameGatewayRebindProtectedPresence(presence, confirmedPresence) {
		return invalid()
	}
	return history.store, history.state, history.journal, true, nil
}

func gatewayRebindProtectedPresenceHasCurrentRoute(presence gatewayRebindProtectedPresenceSnapshot) bool {
	for name := range presence.files {
		_, _, current, err := parseGatewayCurrentRoutePresenceName(name)
		if err != nil || current {
			return true
		}
	}
	return false
}

func gatewayRebindNativeAbortCurrentInspection(current GatewayRebindCurrentInspection) bool {
	if current.SelectedCurrentAuthority.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		current.SelectedCurrentAuthority.TerminalReceiptDigest != "" ||
		current.CurrentRecoveryMode != GatewayCurrentRecoveryNative || current.CurrentStateVersion != 2 ||
		current.CurrentStateRevision != 0 || current.CurrentStateDigest == "" ||
		current.ActiveOperationID != "" || current.ActiveSpecVersion != 0 || current.ActivePhase != "" ||
		current.ActiveDatabaseCommittedEvent != nil || current.ActiveDatabaseCommitObserved ||
		current.ActiveRollbackAllowed || current.ActiveRecoveryPolicy != "" ||
		current.ActiveProtectedEvidence != (appaccess.GatewayRebindProtectedRecoveryEvidence{}) ||
		!current.FenceReleased || len(current.Retained) == 0 {
		return false
	}
	for _, retained := range current.Retained {
		if retained.Disposition != appaccess.GatewayRebindDispositionAbort ||
			retained.PredecessorOperationID != current.SelectedCurrentAuthority.OperationID {
			return false
		}
	}
	return true
}

func gatewayRebindNativeAbortHistoryMatchesCurrent(history gatewayRebindProtectedIntentHistory,
	current GatewayRebindCurrentInspection,
) bool {
	lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
	if err != nil || gatewayCurrentAuthority(lineage) != current.SelectedCurrentAuthority ||
		lineage != current.SelectedCurrentEvidence.Lineage ||
		len(history.Terminals)+len(history.TerminalsV2) != len(current.Retained) {
		return false
	}
	for _, terminal := range history.Terminals {
		if terminal.Receipt.Disposition != gatewayRebindFinalHandoverTerminalAbort ||
			!gatewayRebindLegacyPredecessorMatchesNativeLineage(terminal.Receipt.Predecessor, lineage) {
			return false
		}
	}
	for _, terminal := range history.TerminalsV2 {
		if terminal.Receipt.Disposition != appaccess.GatewayRebindDispositionAbort ||
			terminal.Receipt.Predecessor.Lineage != lineage {
			return false
		}
	}
	return true
}

func gatewayRebindLegacyPredecessorMatchesNativeLineage(predecessor gatewayRebindSuccessorSourceBinding,
	lineage appaccess.GatewayCurrentLineageRef,
) bool {
	return lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade &&
		predecessor.Kind == gatewayRebindInitialSourceKind && predecessor.Generation == lineage.ProtectedGeneration &&
		predecessor.OperationID == lineage.OperationID &&
		predecessor.Profile.RevisionID == lineage.ProfileRevisionID &&
		predecessor.Profile.RevisionNumber == lineage.ProfileRevisionNumber &&
		predecessor.Profile.SpecDigest == lineage.ProfileSpecDigest &&
		predecessor.JournalDigest == lineage.ProtectedJournalDigest &&
		predecessor.IdentityDigest == lineage.ProtectedIdentityDigest
}
