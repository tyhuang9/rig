package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// Caller owns the effects lease and both gateway locks. Active recovery must
// attest the exact commit baseline; terminal startup may attest newer current
// revisions. A held commit barrier permits only this terminal observation.
func (m *Manager) attestGatewayRebindCurrentLocked(ctx context.Context, selection gatewayCurrentSelection) (string, error) {
	invalid := func() (string, error) { return "", gatewayRebindEffectBoundaryError(ctx) }
	if m == nil || ctx == nil || ctx.Err() != nil || selection.Kind != gatewayCurrentSelectionRebind ||
		selection.State == nil || selection.Store == nil || m.gatewayRebindFailStopLatch().Load() {
		return invalid()
	}
	repository, ok := m.options.RebindCurrentStateRepository.(GatewayCurrentServingStartupRepository)
	if !ok {
		return invalid()
	}
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return invalid()
	}
	active := snapshot.Rebind.Active != nil
	barrier := m.gatewayRebindCommitBarrierLatch().Load()
	if active && barrier {
		return invalid()
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil || target.Pending != nil || target.LANRecovery != nil {
		return invalid()
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || filesErr != nil {
		return invalid()
	}
	var request gatewayRebindPhysicalReconcileRequest
	if active {
		attempt, attemptErr := gatewayRebindPreparedAttemptFromHistory(snapshot.Rebind, history)
		if attemptErr != nil {
			return invalid()
		}
		receipt, receiptErr := gatewayRebindActiveTerminalV2(history, attempt.Claim)
		if receiptErr != nil || receipt == nil {
			return invalid()
		}
		request = gatewayRebindForwardConfirmationRequest(attempt, *receipt, snapshot.Rebind)
		if !validGatewayRebindCommittedServingRestoreRequest(request) {
			return invalid()
		}
	}
	readAuthority := func(readCtx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
		fail := func() (appaccess.HostingGatewayStartupSnapshot, error) {
			return appaccess.HostingGatewayStartupSnapshot{}, gatewayRebindEffectBoundaryError(readCtx)
		}
		if readCtx == nil || readCtx.Err() != nil || m.gatewayRebindFailStopLatch().Load() ||
			m.gatewayRebindCommitBarrierLatch().Load() != barrier {
			return fail()
		}
		fresh, err := repository.HostingGatewayStartupSnapshot(readCtx)
		if err != nil || !reflect.DeepEqual(snapshot, fresh) || !fresh.ActiveRebindApprovalsAuthorizeServing() {
			return fail()
		}
		selected, err := m.selectGatewayCurrentLocked(readCtx, fresh.Rebind)
		if err != nil || !sameGatewayCurrentSelection(selection, selected) {
			return fail()
		}
		if !active && (!gatewayCurrentRouteMutationSnapshotReady(fresh.Rebind) || repository.CheckGatewayRebindFence(readCtx) != nil) {
			return fail()
		}
		if readCtx.Err() != nil || m.gatewayRebindFailStopLatch().Load() || m.gatewayRebindCommitBarrierLatch().Load() != barrier {
			return fail()
		}
		return fresh, nil
	}
	guard := func(proofCtx context.Context) error {
		fresh, err := readAuthority(proofCtx)
		if err != nil {
			return err
		}
		if active {
			boundary, boundaryErr := m.readGatewayRebindTypedForwardBoundaryLocked(proofCtx, request)
			if boundaryErr != nil || !sameGatewayCurrentSelection(selection, boundary.Selection) ||
				!sameGatewayRebindCurrentHistory(history, boundary.History) ||
				!m.gatewayRebindCommittedServingCensusMatchesLocked(proofCtx, request, target.State, fresh) {
				return gatewayRebindEffectBoundaryError(proofCtx)
			}
		} else {
			if !gatewayCurrentServingRuntimeCensusMatches(target.State, fresh) {
				return gatewayRebindEffectBoundaryError(proofCtx)
			}
			claims, claimsErr := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(fresh.Grants),
				GatewayLANDisableStartupClaims(fresh.Disables))
			inspection, inspectErr := gatewayCurrentLANStartupCensus(target.State, claims)
			if claimsErr != nil || inspectErr != nil || inspection.Disposition != GatewayV2LANStartupNormal ||
				m.validateGatewayCurrentLANRetainedHistoryLocked(proofCtx, claims, fresh.Rebind) != nil {
				return gatewayRebindEffectBoundaryError(proofCtx)
			}
		}
		after, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
		if historyErr != nil || filesErr != nil || !sameGatewayRebindCurrentHistory(history, after) ||
			!sameGatewayHistorySnapshot(files, afterFiles) {
			return gatewayRebindEffectBoundaryError(proofCtx)
		}
		_, err = readAuthority(proofCtx)
		return err
	}
	driver := m.gatewayRebindCurrentObserver()
	if driver == nil || guard(ctx) != nil {
		return invalid()
	}
	proof, err := driver.attestGatewayRebindCurrent(ctx, target, guard)
	if err != nil || !gatewayCurrentServingRestoreProofMatches(proof, target, gatewayCurrentPhysicalStableServing) || guard(ctx) != nil {
		return invalid()
	}
	return canonicalDigest(struct {
		Purpose     string                             `json:"purpose"`
		Lineage     appaccess.GatewayCurrentLineageRef `json:"lineage"`
		StateDigest string                             `json:"stateDigest"`
		Physical    gatewayCurrentPhysicalAttestation  `json:"physical"`
	}{"hostd/generated-ingress/rebind/committed-current-attestation/v1", selection.Lineage, selection.State.Digest, proof})
}

// Terminal-current recovery no longer has an active-attempt withdrawal defer.
// Failure may stop only this captured protected state, independent of serving
// permission. An ownership change cannot authorize a different current target.
func (m *Manager) withdrawGatewayRebindTerminalCurrentLocked(ctx context.Context, selection gatewayCurrentSelection) error {
	uncertain := &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	if m == nil || ctx == nil {
		return uncertain
	}
	m.gatewayRebindFailStopLatch().Store(true)
	if selection.State == nil || selection.Store == nil || selection.Terminal == nil {
		return uncertain
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	installed, err := selection.Store.load()
	if err != nil || !reflect.DeepEqual(*selection.State, installed) {
		return uncertain
	}
	owned, err := m.gatewayCurrentOwnedStopTargetForStateLocked(installed)
	if err != nil || !reflect.DeepEqual(owned.Terminal, *selection.Terminal) ||
		m.stopGatewayCurrentOwnedTargetLocked(stopCtx, owned) != nil {
		return uncertain
	}
	return gatewayRebindEffectBoundaryError(ctx)
}
