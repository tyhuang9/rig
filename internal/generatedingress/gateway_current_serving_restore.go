package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentServingRestoreMode string

const (
	gatewayCurrentServingRestoreStable         gatewayCurrentServingRestoreMode = "stable_current"
	gatewayCurrentServingRestoreCompletedBatch gatewayCurrentServingRestoreMode = "completed_batch"
	gatewayCurrentServingRestorePurpose                                         = "hostd/generated-ingress/current-serving-restore/v1"
)

type gatewayCurrentServingRestoreAction struct {
	Version             int
	Purpose             string
	Mode                gatewayCurrentServingRestoreMode
	Lineage             appaccess.GatewayCurrentLineageRef
	Terminal            gatewayRebindAttemptTerminalView
	Selected            gatewayCurrentRouteState
	Target              gatewayCurrentRouteState
	AuthorizationDigest string
	Digest              string
}

type gatewayCurrentServingRestoreDriver interface {
	restoreGatewayCurrentServing(context.Context, gatewayCurrentServingRestoreAction,
		func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

func gatewayCurrentServingRestoreActionDigest(value gatewayCurrentServingRestoreAction) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayCurrentServingRestoreActionForSelection(selection gatewayCurrentSelection,
	authorizationDigest string,
) (gatewayCurrentServingRestoreAction, error) {
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil {
		return gatewayCurrentServingRestoreAction{}, gatewayCurrentRouteOperationError(nil)
	}
	selected := cloneGatewayCurrentRouteState(*selection.State)
	target := cloneGatewayCurrentRouteState(selected)
	target.Pending, target.LANRecovery = nil, nil
	target.Digest, err = gatewayCurrentRouteStateDigest(target)
	if err != nil {
		return gatewayCurrentServingRestoreAction{}, err
	}
	mode := gatewayCurrentServingRestoreStable
	if selected.LANRecovery != nil {
		mode = gatewayCurrentServingRestoreCompletedBatch
	}
	action := gatewayCurrentServingRestoreAction{Version: 1, Purpose: gatewayCurrentServingRestorePurpose,
		Mode: mode, Lineage: selection.Lineage, Terminal: terminal, Selected: selected, Target: target,
		AuthorizationDigest: authorizationDigest}
	action.Digest, err = gatewayCurrentServingRestoreActionDigest(action)
	if err != nil || !validGatewayCurrentServingRestoreAction(action) {
		return gatewayCurrentServingRestoreAction{}, gatewayCurrentRouteOperationError(nil)
	}
	return action, nil
}

func validGatewayCurrentServingRestoreAction(action gatewayCurrentServingRestoreAction) bool {
	if action.Version != 1 || action.Purpose != gatewayCurrentServingRestorePurpose ||
		!validSHA256(action.AuthorizationDigest) || !validGatewayCurrentRouteState(action.Selected) ||
		action.Selected.Pending != nil || action.Lineage != action.Selected.Lineage ||
		!gatewayRebindAttemptTerminalMatchesLineage(action.Terminal, action.Lineage) {
		return false
	}
	switch action.Mode {
	case gatewayCurrentServingRestoreStable:
		if action.Selected.LANRecovery != nil {
			return false
		}
	case gatewayCurrentServingRestoreCompletedBatch:
		batch := action.Selected.LANRecovery
		if batch == nil || len(batch.Items) == 0 || batch.Head != len(batch.Items) {
			return false
		}
	default:
		return false
	}
	want := cloneGatewayCurrentRouteState(action.Selected)
	want.LANRecovery = nil
	want.Digest, _ = gatewayCurrentRouteStateDigest(want)
	digest, err := gatewayCurrentServingRestoreActionDigest(action)
	return validGatewayCurrentRouteState(action.Target) && reflect.DeepEqual(want, action.Target) &&
		err == nil && digest == action.Digest
}

// GatewayCurrentServingStartupRepository must read all authority from one SQL
// transaction. The read completes before any Docker effect; a fresh snapshot
// and exact protected selection are required again at every effect boundary.
type GatewayCurrentServingStartupRepository interface {
	HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
	CheckGatewayRebindFence(context.Context) error
}

// RestoreGatewayCurrentServingStartup owns the effects lease and both gateway
// locks. Invoke before ordinary startup admission. It restores only stable
// current state or a completed LAN batch whose full SQL census is terminal.
// It does not retire the batch, resolve claims, or change protected history.
func (m *Manager) RestoreGatewayCurrentServingStartup(ctx context.Context,
	repository GatewayCurrentServingStartupRepository,
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
	return m.restoreGatewayCurrentServingLocked(ctx, repository)
}

func (m *Manager) restoreGatewayCurrentServingLocked(ctx context.Context,
	repository GatewayCurrentServingStartupRepository,
) (bool, error) {
	selection, selectedSQL, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return handled, err
	}
	state := cloneGatewayCurrentRouteState(*selection.State)
	// Incomplete operations have their own recovery path. They cannot be
	// normalized into a serving target by removing their intent.
	if state.Pending != nil || (state.LANRecovery != nil && state.LANRecovery.Head != len(state.LANRecovery.Items)) {
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	retirementUncertain := false
	fail := func() (bool, error) {
		m.gatewayRebindFailStopLatch().Store(true)
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancel()
		installed, err := selection.Store.load()
		if err != nil || !reflect.DeepEqual(state, installed) {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		target, err := m.gatewayCurrentOwnedStopTargetForStateLocked(installed)
		if err != nil || m.stopGatewayCurrentOwnedTargetLocked(stopCtx, target) != nil {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		if retirementUncertain {
			return true, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return true, gatewayCurrentRouteOperationError(ctx)
	}
	retirementUncertain = true
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return fail()
	}
	files, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil {
		return fail()
	}
	withdrawalGuard := func(effectCtx context.Context) error {
		fresh, err := repository.HostingGatewayStartupSnapshot(effectCtx)
		selected, currentSQL, selectedCurrent, selectionErr := m.gatewayCurrentSelectedStateForRecoveryLocked(effectCtx)
		after, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
		if err != nil || selectionErr != nil || historyErr != nil || filesErr != nil ||
			!selectedCurrent || !sameGatewayCurrentSelection(selection, selected) ||
			!reflect.DeepEqual(selectedSQL, currentSQL) || !reflect.DeepEqual(currentSQL, fresh.Rebind) ||
			!sameGatewayRebindCurrentHistory(history, after) || !sameGatewayHistorySnapshot(files, afterFiles) ||
			repository.CheckGatewayRebindFence(effectCtx) != nil || effectCtx.Err() != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		return nil
	}
	if withdrawalGuard(ctx) != nil || m.retireGatewayCurrentPredecessorsLocked(ctx, selection, withdrawalGuard) != nil ||
		withdrawalGuard(ctx) != nil {
		return fail()
	}
	retirementUncertain = false
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !snapshot.ActiveRebindApprovalsAuthorizeServing() || !reflect.DeepEqual(snapshot.Rebind, selectedSQL) {
		return fail()
	}
	authorizationDigest, err := canonicalDigest(snapshot)
	if err != nil {
		return fail()
	}
	action, err := gatewayCurrentServingRestoreActionForSelection(selection, authorizationDigest)
	if err != nil {
		return fail()
	}
	guard := func(effectCtx context.Context) error {
		fresh, err := repository.HostingGatewayStartupSnapshot(effectCtx)
		freshDigest, digestErr := canonicalDigest(fresh)
		if err != nil || digestErr != nil || freshDigest != action.AuthorizationDigest ||
			repository.CheckGatewayRebindFence(effectCtx) != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		selected, currentSQL, selectedCurrent, err := m.gatewayCurrentSelectedStateForRecoveryLocked(effectCtx)
		if err != nil || !selectedCurrent || !sameGatewayCurrentSelection(selection, selected) ||
			!reflect.DeepEqual(currentSQL, fresh.Rebind) || !gatewayCurrentServingRuntimeCensusMatches(action.Target, fresh) {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		claims, err := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(fresh.Grants),
			GatewayLANDisableStartupClaims(fresh.Disables))
		if err != nil || (action.Mode == gatewayCurrentServingRestoreCompletedBatch &&
			!gatewayCurrentLANRecoveryCensusMatchesHead(state, claims, fresh.Rebind.CurrentTransfers)) {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		inspection, err := gatewayCurrentLANStartupCensus(action.Target, claims)
		if err != nil || inspection.Disposition != GatewayV2LANStartupNormal ||
			m.validateGatewayCurrentLANRetainedHistoryLocked(effectCtx, claims, fresh.Rebind) != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		after, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
		if err != nil || !sameGatewayRebindCurrentHistory(history, after) || filesErr != nil ||
			!sameGatewayHistorySnapshot(files, afterFiles) || effectCtx.Err() != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		// Current route bundles are mutable and intentionally excluded from
		// the immutable-history fingerprint. Confirm the exact selection after
		// those reads as well, before granting the next physical effect.
		confirmed, confirmErr := repository.HostingGatewayStartupSnapshot(effectCtx)
		confirmedDigest, digestErr := canonicalDigest(confirmed)
		if confirmErr != nil || digestErr != nil || confirmedDigest != action.AuthorizationDigest ||
			repository.CheckGatewayRebindFence(effectCtx) != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		selected, currentSQL, selectedCurrent, err = m.gatewayCurrentSelectedStateForRecoveryLocked(effectCtx)
		if err != nil || !selectedCurrent || !sameGatewayCurrentSelection(selection, selected) ||
			!reflect.DeepEqual(currentSQL, confirmed.Rebind) || effectCtx.Err() != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		return nil
	}
	if err := guard(ctx); err != nil {
		return fail()
	}
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentServingRestoreDriver)
	if !ok {
		return fail()
	}
	proof, err := driver.restoreGatewayCurrentServing(ctx, action, guard)
	wantOutcome := gatewayCurrentPhysicalStableServing
	if action.Mode == gatewayCurrentServingRestoreCompletedBatch {
		wantOutcome = gatewayCurrentPhysicalRecoveryMixed
	}
	if err != nil || !validGatewayCurrentPhysicalAttestation(proof) || proof.Outcome != wantOutcome ||
		proof.Runtime.ListenerAbsent || !reflect.DeepEqual(proof.State, action.Target) ||
		proof.Lineage != action.Lineage || !reflect.DeepEqual(proof.Terminal, action.Terminal) ||
		proof.Pending != nil || !reflect.DeepEqual(proof.LANRecovery, state.LANRecovery) || guard(ctx) != nil {
		return fail()
	}
	return true, nil
}
