package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentSelectionKind string

const (
	gatewayCurrentSelectionUpgrade gatewayCurrentSelectionKind = "gateway_upgrade"
	gatewayCurrentSelectionRebind  gatewayCurrentSelectionKind = "gateway_rebind"
)

type gatewayCurrentSelection struct {
	Kind          gatewayCurrentSelectionKind
	Lineage       appaccess.GatewayCurrentLineageRef
	Upgrade       *gatewayUpgradeGenerationSelection
	UpgradeSource *routeState
	Receipt       *gatewayRebindFinalHandoverTerminalReceipt
	Terminal      *gatewayRebindAttemptTerminalView
	Store         *gatewayCurrentRouteStateStore
	State         *gatewayCurrentRouteState
}

// MatchesResolution compares the full protected proof with the honest SQL
// authority projection and effective transfer chain. Legacy SQL has no
// protected generation, identity or journal columns, so those remain an
// independent protected validation rather than invented SQL evidence.
func (proof GatewayV2LANEffectiveBindingProof) MatchesResolution(value appaccess.GatewayBindingResolution) bool {
	if !validGatewayCurrentLineage(proof.ProtectedLineage) ||
		gatewayCurrentAuthority(proof.ProtectedLineage) != value.CurrentGatewaySource ||
		proof.TerminalReceiptDigest != value.TerminalReceiptDigest ||
		proof.TerminalReceiptDigest != proof.ProtectedLineage.TerminalReceiptDigest ||
		proof.TransferChainTipDigest != value.TransferChainTipDigest ||
		proof.EffectiveProfile.RevisionID != value.EffectiveProfile.ID ||
		proof.EffectiveProfile.RevisionNumber != value.EffectiveProfile.RevisionNumber ||
		proof.EffectiveProfile.SpecDigest != value.EffectiveProfile.SpecDigest ||
		proof.EffectiveProfile.SelectedIPv4 != value.EffectiveProfile.Spec.SelectedIPv4 ||
		proof.EffectiveProfile.InterfaceID != value.EffectiveProfile.Spec.InterfaceID ||
		proof.EffectiveProfile.PortStart != value.EffectiveProfile.Spec.PortStart ||
		proof.EffectiveProfile.PortEnd != value.EffectiveProfile.Spec.PortEnd {
		return false
	}
	if len(value.TransferChain) == 0 {
		return proof.TransferChainTipDigest == ""
	}
	previous := ""
	for index, transfer := range value.TransferChain {
		digest, err := appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if err != nil || transfer.TransferDigest != digest ||
			(index == 0 && transfer.PredecessorTransferDigest != nil) ||
			(index > 0 && (transfer.PredecessorTransferDigest == nil || *transfer.PredecessorTransferDigest != previous)) {
			return false
		}
		previous = digest
	}
	return previous == proof.TransferChainTipDigest
}

func gatewayCurrentProfileMatchesAuthority(profile *appaccess.GatewayProfileRevision,
	authority appaccess.GatewayCurrentAuthorityRef,
) bool {
	return profile != nil && profile.ID == authority.ProfileRevisionID &&
		profile.RevisionNumber == authority.ProfileRevisionNumber && profile.SpecDigest == authority.ProfileSpecDigest
}

func gatewayCurrentUpgradeSelection(history gatewayRebindProtectedIntentHistory,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) (gatewayCurrentSelection, error) {
	if snapshot.CurrentSource == nil || snapshot.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		!gatewayCurrentProfileMatchesAuthority(snapshot.CurrentProfile, *snapshot.CurrentSource) {
		return gatewayCurrentSelection{}, errors.New("generated ingress SQL current upgrade authority is invalid")
	}
	lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
	if err != nil || gatewayCurrentAuthority(lineage) != *snapshot.CurrentSource {
		return gatewayCurrentSelection{}, errors.New("generated ingress current upgrade authority disagrees with protected state")
	}
	selection := history.Predecessor
	source := cloneRouteState(history.Source)
	return gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: lineage,
		Upgrade: &selection, UpgradeSource: &source}, nil
}

func gatewayCurrentRebindSelection(dataRoot string, history gatewayRebindProtectedIntentHistory,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) (gatewayCurrentSelection, error) {
	if snapshot.CurrentSource == nil || snapshot.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
		!gatewayCurrentAuthorityHasDatabaseCommit(snapshot.History, snapshot.CurrentDatabaseCommittedEvent,
			*snapshot.CurrentSource) ||
		!gatewayCurrentProfileMatchesAuthority(snapshot.CurrentProfile, *snapshot.CurrentSource) {
		return gatewayCurrentSelection{}, errors.New("generated ingress SQL current rebind authority is invalid")
	}
	var receipt *gatewayRebindFinalHandoverTerminalReceipt
	var terminal *gatewayRebindAttemptTerminalView
	for index := range history.Terminals {
		candidate := history.Terminals[index].Receipt
		if candidate.OperationID != snapshot.CurrentSource.OperationID {
			continue
		}
		if terminal != nil || candidate.Disposition != gatewayRebindFinalHandoverTerminalCommit {
			return gatewayCurrentSelection{}, errors.New("generated ingress protected current rebind receipt is ambiguous")
		}
		copy := candidate
		receipt = &copy
		view, viewErr := newGatewayRebindAttemptTerminalViewLegacy(copy)
		if viewErr != nil {
			return gatewayCurrentSelection{}, errors.New("generated ingress protected current rebind receipt is invalid")
		}
		terminal = &view
	}
	for index := range history.TerminalsV2 {
		candidate := history.TerminalsV2[index].Receipt
		if candidate.OperationID != snapshot.CurrentSource.OperationID {
			continue
		}
		if terminal != nil || candidate.Disposition != appaccess.GatewayRebindDispositionCommit {
			return gatewayCurrentSelection{}, errors.New("generated ingress protected current rebind receipt is ambiguous")
		}
		view, viewErr := newGatewayRebindAttemptTerminalViewV2(candidate)
		if viewErr != nil {
			return gatewayCurrentSelection{}, errors.New("generated ingress protected current rebind receipt is invalid")
		}
		terminal = &view
	}
	if terminal == nil {
		return gatewayCurrentSelection{}, errors.New("generated ingress protected current rebind receipt is missing")
	}
	var lineage appaccess.GatewayCurrentLineageRef
	var err error
	if terminal.Format == gatewayRebindAttemptTerminalLegacyV1 {
		lineage, err = gatewayRebindCurrentLineage(*terminal.LegacyReceipt)
	} else {
		lineage, err = gatewayRebindCurrentLineageV2(*terminal.TypedReceipt)
	}
	if err != nil || gatewayCurrentAuthority(lineage) != *snapshot.CurrentSource {
		return gatewayCurrentSelection{}, errors.New("generated ingress current rebind authority disagrees with protected state")
	}
	store, err := newGatewayCurrentRouteStateStore(dataRoot, lineage)
	if err != nil {
		return gatewayCurrentSelection{}, err
	}
	state, err := store.load()
	if err != nil || state.Lineage != lineage {
		return gatewayCurrentSelection{}, errors.New("generated ingress current rebind operational state is invalid")
	}
	manifestDigest, err := appaccess.GatewayRebindTransferManifestDigest(snapshot.CurrentTransfers)
	if err != nil || manifestDigest != state.TransferManifestDigest || !gatewayCurrentTransfersMatchState(snapshot.CurrentTransfers, state) {
		return gatewayCurrentSelection{}, errors.New("generated ingress current rebind transfers disagree with protected state")
	}
	return gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: lineage,
		Receipt: receipt, Terminal: terminal, Store: store, State: &state}, nil
}

// gatewayCurrentAuthorityHasDatabaseCommit deliberately inspects retained
// history for the SQL-selected current operation. Snapshot phase and its
// database-commit fields describe the independently active recovery attempt;
// during a second prepared rebind they must not be mistaken for proof of the
// previously committed current operation.
func gatewayCurrentAuthorityHasDatabaseCommit(history []appaccess.GatewayRebindHistoryEntry,
	currentEvent *appaccess.GatewayRebindEvent,
	authority appaccess.GatewayCurrentAuthorityRef,
) bool {
	if currentEvent == nil || currentEvent.OperationID != authority.OperationID ||
		currentEvent.State != appaccess.GatewayRebindDatabaseCommitted || currentEvent.Sequence <= 0 {
		return false
	}
	found := false
	for _, entry := range history {
		operationID := ""
		switch {
		case entry.Claim.SpecVersion == 1 && entry.Claim.Legacy != nil && entry.Claim.V2 == nil:
			operationID = entry.Claim.Legacy.Spec.OperationID
		case entry.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && entry.Claim.Legacy == nil && entry.Claim.V2 != nil:
			operationID = entry.Claim.V2.Spec.OperationID
		default:
			return false
		}
		if operationID != authority.OperationID {
			continue
		}
		if found {
			return false
		}
		found = true
		events := 0
		for _, event := range entry.Events {
			if event.OperationID == authority.OperationID && event.State == appaccess.GatewayRebindDatabaseCommitted {
				if !reflect.DeepEqual(event, *currentEvent) {
					return false
				}
				events++
			}
		}
		if events != 1 {
			return false
		}
	}
	return found
}

func gatewayCurrentTransfersMatchState(transfers []appaccess.GatewayRebindAllocationTransfer,
	state gatewayCurrentRouteState,
) bool {
	byApp := make(map[string]appaccess.GatewayRebindAllocationTransfer, len(transfers))
	for _, transfer := range transfers {
		digest, err := appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if err != nil || transfer.TransferDigest != digest || transfer.OperationID != state.Lineage.OperationID ||
			transfer.TerminalReceiptDigest != state.Lineage.TerminalReceiptDigest {
			return false
		}
		if _, duplicate := byApp[transfer.AppID]; duplicate {
			return false
		}
		byApp[transfer.AppID] = transfer
	}
	for appID, app := range state.Apps {
		if app.LAN == nil || app.LAN.Transfer == nil {
			continue
		}
		transfer, ok := byApp[appID]
		if !ok || !reflect.DeepEqual(transfer, *app.LAN.Transfer) {
			return false
		}
	}
	// Transfers are immutable historical authorization. A later disable or
	// native regrant may remove an active transferred binding while SQL retains
	// the complete baseline manifest. The manifest digest above proves that
	// history; each active transfer-bearing route must still select its exact
	// retained row, but every retained row need not remain active.
	return true
}

// selectGatewayCurrentLocked uses SQL current authority to select one already
// validated protected lineage. A protected terminal receipt alone never moves
// the current gateway. The caller must hold Manager and OS gateway locks.
func (m *Manager) selectGatewayCurrentLocked(ctx context.Context,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) (gatewayCurrentSelection, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || snapshot.CurrentSource == nil {
		return gatewayCurrentSelection{}, errors.New("generated ingress current gateway selection input is invalid")
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return gatewayCurrentSelection{}, err
	}
	var selection gatewayCurrentSelection
	switch snapshot.CurrentSource.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		selection, err = gatewayCurrentUpgradeSelection(history, snapshot)
	case appaccess.GatewayRebindSourceGatewayRebind:
		selection, err = gatewayCurrentRebindSelection(m.options.DataRoot, history, snapshot)
	default:
		err = errors.New("generated ingress SQL current gateway kind is invalid")
	}
	if err != nil || ctx.Err() != nil {
		return gatewayCurrentSelection{}, errors.New("generated ingress current gateway selection failed")
	}
	// Repeat the complete protected scan and selected current read. This keeps
	// external protected-file replacement from crossing the SQL-led decision.
	confirmedHistory, scanErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || !sameGatewayRebindCurrentHistory(history, confirmedHistory) {
		return gatewayCurrentSelection{}, errors.New("generated ingress current protected history changed during selection")
	}
	if selection.Store != nil {
		confirmed, loadErr := selection.Store.load()
		if loadErr != nil || selection.State == nil || !reflect.DeepEqual(confirmed, *selection.State) {
			return gatewayCurrentSelection{}, errors.New("generated ingress current route state changed during selection")
		}
	}
	return selection, nil
}

// readGatewayCurrentSelectionLocked gives normal-operation and startup
// consumers one fresh SQL/protected selection while their caller holds the
// Manager and gateway OS locks. The second SQL read prevents a current-head
// change from crossing the protected selection. It performs no mutation and
// never falls back to the highest protected receipt when SQL is unavailable.
func (m *Manager) readGatewayCurrentSelectionLocked(ctx context.Context) (
	gatewayCurrentSelection, appaccess.GatewayRebindRecoverySnapshot, error,
) {
	if m == nil || ctx == nil || ctx.Err() != nil || m.options.RebindCurrentStateRepository == nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{},
			errors.New("generated ingress current gateway repository is unavailable")
	}
	first, err := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || first.CurrentSource == nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{},
			errors.New("generated ingress current gateway SQL selection is unavailable")
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, first)
	if err != nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, err
	}
	second, err := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(first, second) {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{},
			errors.New("generated ingress current gateway SQL selection changed")
	}
	return selection, first, nil
}

func sameGatewayRebindCurrentHistory(left, right gatewayRebindProtectedIntentHistory) bool {
	if len(left.Checkpoints) != len(right.Checkpoints) || len(left.Intents) != len(right.Intents) ||
		len(left.IntentsV2) != len(right.IntentsV2) || len(left.Progress) != len(right.Progress) ||
		len(left.Terminals) != len(right.Terminals) || len(left.TerminalsV2) != len(right.TerminalsV2) ||
		!sameObservedGatewayV2Selection(left.Predecessor, right.Predecessor) || !reflect.DeepEqual(left.Source, right.Source) {
		return false
	}
	for index := range left.Checkpoints {
		if !reflect.DeepEqual(left.Checkpoints[index].Checkpoint, right.Checkpoints[index].Checkpoint) {
			return false
		}
	}
	for index := range left.Intents {
		if !reflect.DeepEqual(left.Intents[index].Intent, right.Intents[index].Intent) {
			return false
		}
	}
	for index := range left.IntentsV2 {
		if !reflect.DeepEqual(left.IntentsV2[index].Intent, right.IntentsV2[index].Intent) {
			return false
		}
	}
	for index := range left.Progress {
		if !reflect.DeepEqual(left.Progress[index].Record, right.Progress[index].Record) {
			return false
		}
	}
	for index := range left.Terminals {
		if !reflect.DeepEqual(left.Terminals[index].Receipt, right.Terminals[index].Receipt) {
			return false
		}
	}
	for index := range left.TerminalsV2 {
		if !reflect.DeepEqual(left.TerminalsV2[index].Receipt, right.TerminalsV2[index].Receipt) {
			return false
		}
	}
	return true
}
