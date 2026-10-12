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
	return m.attestGatewayRebindCurrentWithSessionLocked(ctx, selection, nil)
}

func (m *Manager) attestGatewayRebindCurrentWithSessionLocked(ctx context.Context, selection gatewayCurrentSelection,
	forwardSessionGuard gatewayRebindForwardSessionGuard,
) (string, error) {
	if m == nil {
		return "", newGatewayCurrentAttestationError(ctx, gatewayCurrentAttestationCheckpointManagerPrecondition)
	}
	repository, ok := m.options.RebindCurrentStateRepository.(gatewayRebindTypedBoundaryRepository)
	if !ok {
		return "", newGatewayCurrentAttestationError(ctx, gatewayCurrentAttestationCheckpointManagerAuthority)
	}
	return m.attestGatewayRebindCurrentWithRepositoryAndSessionLocked(ctx, selection, repository,
		forwardSessionGuard)
}

func (m *Manager) attestGatewayRebindCurrentWithRepositoryAndSessionLocked(ctx context.Context,
	selection gatewayCurrentSelection, repository gatewayRebindTypedBoundaryRepository,
	forwardSessionGuard gatewayRebindForwardSessionGuard,
) (string, error) {
	invalid := func(checkpoint gatewayCurrentAttestationCheckpoint) (string, error) {
		return "", newGatewayCurrentAttestationError(ctx, checkpoint)
	}
	invalidFrom := func(err error, checkpoint gatewayCurrentAttestationCheckpoint) (string, error) {
		return "", gatewayCurrentAttestationErrorFrom(ctx, err, checkpoint)
	}
	if m == nil || ctx == nil || ctx.Err() != nil || repository == nil ||
		selection.Kind != gatewayCurrentSelectionRebind ||
		selection.State == nil || selection.Store == nil || m.gatewayRebindFailStopLatch().Load() {
		return invalid(gatewayCurrentAttestationCheckpointManagerPrecondition)
	}
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointManagerAuthority)
	}
	active := snapshot.Rebind.Active != nil
	barrier := m.gatewayRebindCommitBarrierLatch().Load()
	if active && barrier {
		return invalid(gatewayCurrentAttestationCheckpointManagerAuthority)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil || target.Pending != nil || target.LANRecovery != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointManagerPrecondition)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || filesErr != nil {
		if err != nil {
			return invalidFrom(err, gatewayCurrentAttestationCheckpointManagerHistoryStability)
		}
		return invalidFrom(filesErr, gatewayCurrentAttestationCheckpointManagerHistoryStability)
	}
	var request gatewayRebindPhysicalReconcileRequest
	if active {
		attempt, attemptErr := gatewayRebindPreparedAttemptFromHistory(snapshot.Rebind, history)
		if attemptErr != nil {
			return invalidFrom(attemptErr, gatewayCurrentAttestationCheckpointManagerForwardBoundary)
		}
		receipt, receiptErr := gatewayRebindActiveTerminalV2(history, attempt.Claim)
		if receiptErr != nil || receipt == nil {
			return invalidFrom(receiptErr, gatewayCurrentAttestationCheckpointManagerForwardBoundary)
		}
		request = gatewayRebindForwardConfirmationRequest(attempt, *receipt, snapshot.Rebind)
		if !validGatewayRebindCommittedServingRestoreRequest(request) {
			return invalid(gatewayCurrentAttestationCheckpointManagerForwardBoundary)
		}
	}
	readAuthority := func(readCtx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
		fail := func(err error) (appaccess.HostingGatewayStartupSnapshot, error) {
			return appaccess.HostingGatewayStartupSnapshot{}, gatewayCurrentAttestationErrorFrom(readCtx, err,
				gatewayCurrentAttestationCheckpointManagerAuthority)
		}
		if readCtx == nil || readCtx.Err() != nil || m.gatewayRebindFailStopLatch().Load() ||
			m.gatewayRebindCommitBarrierLatch().Load() != barrier {
			return fail(nil)
		}
		if forwardSessionGuard != nil {
			if forwardSessionGuard(readCtx) != nil || readCtx.Err() != nil {
				return fail(nil)
			}
		}
		fresh, err := repository.HostingGatewayStartupSnapshot(readCtx)
		if err != nil || !reflect.DeepEqual(snapshot, fresh) || !fresh.ActiveRebindApprovalsAuthorizeServing() {
			return fail(err)
		}
		selected, err := m.selectGatewayCurrentLocked(readCtx, fresh.Rebind)
		if err != nil || !sameGatewayCurrentSelection(selection, selected) {
			return fail(err)
		}
		if !active && (!gatewayCurrentRouteMutationSnapshotReady(fresh.Rebind) || repository.CheckGatewayRebindFence(readCtx) != nil) {
			return fail(nil)
		}
		if readCtx.Err() != nil || m.gatewayRebindFailStopLatch().Load() ||
			m.gatewayRebindCommitBarrierLatch().Load() != barrier {
			return fail(nil)
		}
		if forwardSessionGuard != nil {
			if forwardSessionGuard(readCtx) != nil || readCtx.Err() != nil {
				return fail(nil)
			}
		}
		return fresh, nil
	}
	guard := func(proofCtx context.Context) error {
		fresh, err := readAuthority(proofCtx)
		if err != nil {
			return err
		}
		if active {
			boundary, boundaryErr := (managerGatewayRebindCrossStoreDriver{
				manager: m, repository: repository, forwardSessionGuard: forwardSessionGuard,
			}).readGatewayRebindTypedForwardBoundaryLocked(proofCtx, request)
			if boundaryErr != nil || !sameGatewayCurrentSelection(selection, boundary.Selection) ||
				!sameGatewayRebindCurrentHistory(history, boundary.History) {
				return gatewayCurrentAttestationErrorFrom(proofCtx, boundaryErr,
					gatewayCurrentAttestationCheckpointManagerForwardBoundary)
			}
			if !m.gatewayRebindCommittedServingCensusMatchesLocked(proofCtx, request, target.State, fresh) {
				return newGatewayCurrentAttestationError(proofCtx,
					gatewayCurrentAttestationCheckpointManagerServingCensus)
			}
		} else {
			if !gatewayCurrentServingRuntimeCensusMatches(target.State, fresh) {
				return newGatewayCurrentAttestationError(proofCtx,
					gatewayCurrentAttestationCheckpointManagerServingCensus)
			}
			claims, claimsErr := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(fresh.Grants),
				GatewayLANDisableStartupClaims(fresh.Disables))
			inspection, inspectErr := gatewayCurrentLANStartupCensus(target.State, claims)
			if claimsErr != nil || inspectErr != nil || inspection.Disposition != GatewayV2LANStartupNormal ||
				m.validateGatewayCurrentLANRetainedHistoryLocked(proofCtx, claims, fresh.Rebind) != nil {
				return newGatewayCurrentAttestationError(proofCtx,
					gatewayCurrentAttestationCheckpointManagerServingCensus)
			}
		}
		after, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
		if historyErr != nil || filesErr != nil || !sameGatewayRebindCurrentHistory(history, after) ||
			!sameGatewayHistorySnapshot(files, afterFiles) {
			if historyErr != nil {
				return gatewayCurrentAttestationErrorFrom(proofCtx, historyErr,
					gatewayCurrentAttestationCheckpointManagerHistoryStability)
			}
			return gatewayCurrentAttestationErrorFrom(proofCtx, filesErr,
				gatewayCurrentAttestationCheckpointManagerHistoryStability)
		}
		_, err = readAuthority(proofCtx)
		return err
	}
	driver := m.gatewayRebindCurrentObserver()
	if driver == nil {
		return invalid(gatewayCurrentAttestationCheckpointManagerDriver)
	}
	if err := guard(ctx); err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointManagerPreDriverGuard)
	}
	proof, err := driver.attestGatewayRebindCurrent(ctx, target, guard)
	if err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointManagerDriver)
	}
	if !gatewayCurrentServingRestoreProofMatches(proof, target, gatewayCurrentPhysicalStableServing) {
		return invalid(gatewayCurrentAttestationCheckpointManagerProof)
	}
	if err := guard(ctx); err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointManagerPostDriverGuard)
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
