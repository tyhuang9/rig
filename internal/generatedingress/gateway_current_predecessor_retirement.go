package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

func gatewayCurrentPredecessorRetirementFailure(prior, cleanup error) error {
	uncertain := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	return errors.Join(uncertain, prior, cleanup)
}

type gatewayCurrentNativePredecessorRetirement struct {
	Lineage appaccess.GatewayCurrentLineageRef
	State   gatewayV2RouteState
	Journal gatewayMigrationJournal
}

type gatewayCurrentPredecessorRetirementTarget struct{ Facts gatewayFinalOwnershipFacts }

type gatewayCurrentPredecessorRetirementAction struct {
	CurrentLineage appaccess.GatewayCurrentLineageRef
	CurrentFinalID string
	CurrentState   gatewayCurrentRouteState
	CurrentFacts   gatewayFinalOwnershipFacts
	CurrentTargets []gatewayCurrentPhysicalTarget
	Native         gatewayCurrentNativePredecessorRetirement
	Predecessors   []gatewayCurrentPredecessorRetirementTarget
}

type gatewayCurrentPredecessorRetirementDriver interface {
	retireGatewayCurrentPredecessors(context.Context, gatewayCurrentPredecessorRetirementAction, func(context.Context) error) error
	observeGatewayCurrentPredecessorsRetired(context.Context, gatewayCurrentPredecessorRetirementAction) error
}

func (m *Manager) observeGatewayCurrentPredecessorsRetiredLocked(ctx context.Context, selection gatewayCurrentSelection,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) error {
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || filesErr != nil {
		return candidateMayBeLiveError()
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentPredecessorRetirementDriver)
	confirmedSelection, confirmedSnapshot, confirmErr := m.readGatewayCurrentSelectionLocked(ctx)
	confirmedHistory, confirmedHistoryErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	confirmedFiles, confirmedFilesErr := readGatewayHistorySnapshotMode(m.store, true)
	confirmedAction, confirmedActionErr := gatewayCurrentPredecessorRetirementActionFor(confirmedSelection, confirmedHistory)
	if err != nil || !ok || confirmErr != nil || confirmedHistoryErr != nil || confirmedFilesErr != nil ||
		confirmedActionErr != nil || !reflect.DeepEqual(snapshot, confirmedSnapshot) ||
		!sameGatewayCurrentSelection(selection, confirmedSelection) ||
		!sameGatewayRebindCurrentHistory(history, confirmedHistory) || !sameGatewayHistorySnapshot(files, confirmedFiles) ||
		!reflect.DeepEqual(action, confirmedAction) || driver.observeGatewayCurrentPredecessorsRetired(ctx, action) != nil {
		return candidateMayBeLiveError()
	}
	freshSelection, freshSnapshot, freshErr := m.readGatewayCurrentSelectionLocked(ctx)
	freshHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	freshFiles, freshFilesErr := readGatewayHistorySnapshotMode(m.store, true)
	freshAction, actionErr := gatewayCurrentPredecessorRetirementActionFor(freshSelection, freshHistory)
	if freshErr != nil || historyErr != nil || freshFilesErr != nil || actionErr != nil ||
		!reflect.DeepEqual(snapshot, freshSnapshot) || !sameGatewayCurrentSelection(selection, freshSelection) ||
		!sameGatewayRebindCurrentHistory(history, freshHistory) || !sameGatewayHistorySnapshot(files, freshFiles) ||
		!reflect.DeepEqual(action, freshAction) || ctx.Err() != nil {
		return candidateMayBeLiveError()
	}
	return nil
}

func gatewayCurrentAttemptFromHistory(selection gatewayCurrentSelection, history gatewayRebindProtectedIntentHistory) (gatewayRebindAttemptView, error) {
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil {
		return gatewayRebindAttemptView{}, err
	}
	switch terminal.Format {
	case gatewayRebindAttemptTerminalLegacyV1:
		var found *gatewayRebindProtectedIntent
		for _, candidate := range history.Intents {
			if candidate.Intent.Digest == terminal.ProtectedIntentDigest {
				if found != nil {
					return gatewayRebindAttemptView{}, errors.New("ambiguous legacy predecessor intent")
				}
				value := candidate.Intent
				found = &value
			}
		}
		if found == nil {
			return gatewayRebindAttemptView{}, errors.New("missing legacy predecessor intent")
		}
		return newGatewayRebindAttemptViewLegacy(*found, history.Source, history.Predecessor)
	case gatewayRebindAttemptTerminalTypedV2:
		var intent *gatewayRebindProtectedIntentV2
		var checkpoint *gatewayRebindPredecessorCheckpoint
		for _, candidate := range history.IntentsV2 {
			if candidate.Intent.Digest == terminal.ProtectedIntentDigest {
				if intent != nil {
					return gatewayRebindAttemptView{}, errors.New("ambiguous typed predecessor intent")
				}
				value := candidate.Intent
				intent = &value
			}
		}
		for _, candidate := range history.Checkpoints {
			if candidate.Checkpoint.Generation == terminal.Generation && candidate.Checkpoint.OperationID == terminal.OperationID {
				if checkpoint != nil {
					return gatewayRebindAttemptView{}, errors.New("ambiguous typed predecessor checkpoint")
				}
				value := candidate.Checkpoint
				checkpoint = &value
			}
		}
		if intent == nil || checkpoint == nil {
			return gatewayRebindAttemptView{}, errors.New("missing typed predecessor evidence")
		}
		attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(*intent, history)
		if err != nil || attempt.Checkpoint.Digest != checkpoint.Digest {
			return gatewayRebindAttemptView{}, errors.New("invalid typed predecessor evidence")
		}
		predecessor, err := gatewayRebindTypedPredecessorSelection(attempt, history)
		if err != nil {
			return gatewayRebindAttemptView{}, errors.New("invalid typed predecessor selection")
		}
		return newGatewayRebindAttemptViewV2(*intent, *checkpoint, predecessor)
	default:
		return gatewayRebindAttemptView{}, errors.New("unknown predecessor terminal format")
	}
}

func gatewayCurrentPredecessorRetirementActionFor(selection gatewayCurrentSelection, history gatewayRebindProtectedIntentHistory) (gatewayCurrentPredecessorRetirementAction, error) {
	invalid := errors.New("generated ingress predecessor retirement chain is invalid")
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || terminal.Resources.FinalContainer == nil {
		return gatewayCurrentPredecessorRetirementAction{}, invalid
	}
	currentAttempt, err := gatewayCurrentAttemptFromHistory(selection, history)
	if err != nil {
		return gatewayCurrentPredecessorRetirementAction{}, invalid
	}
	action := gatewayCurrentPredecessorRetirementAction{CurrentLineage: selection.Lineage, CurrentFinalID: terminal.Resources.FinalContainer.ID,
		CurrentState: cloneGatewayCurrentRouteState(*selection.State),
		CurrentFacts: gatewayFinalOwnershipFacts{Lineage: selection.Lineage, Terminal: terminal, Network: gatewayV2NetworkPlan(currentAttempt.Network), LocalHostPort: gatewayRebindRetainedHandoverLocalPort(history.Progress, terminal.Generation, terminal.OperationID)}}
	if !validGatewayFinalOwnershipFacts(action.CurrentFacts) {
		return gatewayCurrentPredecessorRetirementAction{}, invalid
	}
	action.CurrentTargets, err = gatewayCurrentPhysicalTargetsForSelection(selection)
	if err != nil {
		return gatewayCurrentPredecessorRetirementAction{}, invalid
	}
	current := selection
	seenLineage := map[appaccess.GatewayCurrentLineageRef]bool{selection.Lineage: true}
	seenIDs := map[string]bool{action.CurrentFinalID: true}
	for {
		attempt, attemptErr := gatewayCurrentAttemptFromHistory(current, history)
		if attemptErr != nil || attempt.Source.Lineage.ProtectedGeneration >= current.Lineage.ProtectedGeneration || seenLineage[attempt.Source.Lineage] {
			return gatewayCurrentPredecessorRetirementAction{}, invalid
		}
		seenLineage[attempt.Source.Lineage] = true
		switch attempt.Source.Lineage.Kind {
		case appaccess.GatewayRebindSourceGatewayUpgrade:
			if attempt.Source.Upgrade == nil || !validGatewayV2RouteState(attempt.Source.Upgrade.State) || !validGatewayMigrationJournal(attempt.Source.Upgrade.Journal) || seenIDs[attempt.Source.Upgrade.Journal.Resources.FinalContainerID] {
				return gatewayCurrentPredecessorRetirementAction{}, invalid
			}
			action.Native = gatewayCurrentNativePredecessorRetirement{Lineage: attempt.Source.Lineage, State: cloneGatewayV2RouteState(attempt.Source.Upgrade.State), Journal: attempt.Source.Upgrade.Journal}
			return action, nil
		case appaccess.GatewayRebindSourceGatewayRebind:
			if attempt.Source.Rebind == nil || attempt.Source.Rebind.Terminal.Resources.FinalContainer == nil {
				return gatewayCurrentPredecessorRetirementAction{}, invalid
			}
			predecessor := attempt.Source.Rebind
			id := predecessor.Terminal.Resources.FinalContainer.ID
			if seenIDs[id] {
				return gatewayCurrentPredecessorRetirementAction{}, invalid
			}
			seenIDs[id] = true
			state := cloneGatewayCurrentRouteState(predecessor.State)
			terminal := predecessor.Terminal
			current = gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: attempt.Source.Lineage, Terminal: &terminal, State: &state}
			predecessorAttempt, predecessorErr := gatewayCurrentAttemptFromHistory(current, history)
			facts := gatewayFinalOwnershipFacts{Lineage: attempt.Source.Lineage, Terminal: predecessor.Terminal, LocalHostPort: gatewayRebindRetainedHandoverLocalPort(history.Progress, predecessor.Terminal.Generation, predecessor.Terminal.OperationID)}
			if predecessorErr != nil {
				return gatewayCurrentPredecessorRetirementAction{}, invalid
			}
			facts.Network = gatewayV2NetworkPlan(predecessorAttempt.Network)
			if !validGatewayFinalOwnershipFacts(facts) {
				return gatewayCurrentPredecessorRetirementAction{}, invalid
			}
			action.Predecessors = append(action.Predecessors, gatewayCurrentPredecessorRetirementTarget{Facts: facts})
		default:
			return gatewayCurrentPredecessorRetirementAction{}, invalid
		}
	}
}

func (m *Manager) retireGatewayCurrentPredecessorsLocked(ctx context.Context, selection gatewayCurrentSelection, guard func(context.Context) error) error {
	if m == nil || ctx == nil || ctx.Err() != nil || guard == nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil || reflect.DeepEqual(action.CurrentLineage, action.Native.Lineage) {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentPredecessorRetirementDriver)
	if !ok || guard(ctx) != nil || driver.retireGatewayCurrentPredecessors(ctx, action, guard) != nil || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	return nil
}

// RetireGatewayCurrentPredecessorsStartup withdraws exact committed ancestors
// before hostd dispatches any ordinary current-mode recovery. It owns the
// deployment effects lease and both gateway locks and never authorizes serving.
func (m *Manager) RetireGatewayCurrentPredecessorsStartup(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository,
) (handled bool, resultErr error) {
	if m == nil || ctx == nil || repository == nil {
		return false, &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return false, gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGateway(ctx)
	if err != nil {
		if releaseEffects() != nil {
			m.gatewayRebindFailStopLatch().Store(true)
		}
		return false, err
	}
	defer func() { resultErr = m.releaseGatewayRebindTerminalLocks(releaseEffects, releaseGateway, resultErr) }()
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active != nil {
		return false, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		return false, err
	}
	if selection.Kind == gatewayCurrentSelectionUpgrade {
		return false, nil
	}
	if selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil {
		return false, gatewayRebindProposalError(ctx)
	}
	fail := func(prior error) (bool, error) {
		cleanup := m.withdrawGatewayRebindTerminalCurrentLocked(ctx, selection)
		return true, gatewayCurrentPredecessorRetirementFailure(prior, cleanup)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || filesErr != nil {
		return fail(gatewayRebindProposalError(ctx))
	}
	guard := func(effectCtx context.Context) error {
		fresh, snapshotErr := repository.GatewayRebindRecoverySnapshot(effectCtx)
		freshSelection, selectionErr := m.selectGatewayCurrentLocked(effectCtx, fresh)
		freshHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		freshFiles, fileErr := readGatewayHistorySnapshotMode(m.store, true)
		if snapshotErr != nil || selectionErr != nil || historyErr != nil || fileErr != nil || fresh.Active != nil ||
			!reflect.DeepEqual(snapshot, fresh) || !sameGatewayCurrentSelection(selection, freshSelection) ||
			!sameGatewayRebindCurrentHistory(history, freshHistory) || !sameGatewayHistorySnapshot(files, freshFiles) ||
			repository.CheckGatewayRebindFence(effectCtx) != nil || effectCtx.Err() != nil {
			return gatewayRebindProposalError(effectCtx)
		}
		return nil
	}
	if guard(ctx) == nil && m.retireGatewayCurrentPredecessorsLocked(ctx, selection, guard) == nil && guard(ctx) == nil {
		return true, nil
	}
	return fail(gatewayRebindProposalError(ctx))
}
