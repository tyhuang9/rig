package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayRebindCommittedServingRestorePurpose = "hostd/generated-ingress/rebind-committed-serving-restore/v1"

type gatewayRebindCommittedServingRestoreAction struct {
	Version             int
	Purpose             string
	Request             gatewayRebindPhysicalReconcileRequest
	State               gatewayCurrentRouteState
	AuthorizationDigest string
	Digest              string
}

type gatewayRebindCommittedServingRestoreDriver interface {
	restoreGatewayRebindCommittedServing(context.Context, gatewayRebindCommittedServingRestoreAction,
		func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

func gatewayRebindCommittedServingRestoreActionDigest(action gatewayRebindCommittedServingRestoreAction) (string, error) {
	action.Digest = ""
	return canonicalDigest(action)
}

func validGatewayRebindCommittedServingRestoreRequest(request gatewayRebindPhysicalReconcileRequest) bool {
	return validGatewayRebindPhysicalReconcileRequest(request) &&
		request.Mode == gatewayRebindPhysicalReconcileForwardOnly &&
		request.SQLPhase == appaccess.GatewayRebindDatabaseCommitted &&
		request.DatabaseCommitObserved && !request.RollbackAllowed
}

func validGatewayRebindCommittedServingRestoreAction(action gatewayRebindCommittedServingRestoreAction) bool {
	if action.Version != 1 || action.Purpose != gatewayRebindCommittedServingRestorePurpose ||
		!validSHA256(action.AuthorizationDigest) || !validGatewayRebindCommittedServingRestoreRequest(action.Request) ||
		!validGatewayCurrentRouteState(action.State) || action.State.Pending != nil || action.State.LANRecovery != nil {
		return false
	}
	lineage, err := gatewayRebindCurrentLineageV2(*action.Request.Terminal)
	digest, digestErr := gatewayRebindCommittedServingRestoreActionDigest(action)
	transfers, transfersErr := newGatewayRebindTransfersV2(action.Request.Attempt.Intent,
		*action.Request.Terminal, action.Request.Attempt.Checkpoint)
	baseline, baselineErr := newGatewayCurrentRouteBaselineFromV2Terminal(action.Request.Attempt.Intent,
		*action.Request.Terminal, action.Request.Attempt.Checkpoint, transfers)
	return err == nil && action.State.Lineage == lineage && digestErr == nil && digest == action.Digest &&
		transfersErr == nil && baselineErr == nil && reflect.DeepEqual(baseline, action.State)
}

func gatewayRebindCommittedServingSnapshotMatches(request gatewayRebindPhysicalReconcileRequest,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) bool {
	if !validGatewayRebindCommittedServingRestoreRequest(request) || snapshot.Active == nil ||
		snapshot.Active.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 || snapshot.Active.Claim.Legacy != nil ||
		snapshot.Active.Claim.V2 == nil || snapshot.Phase != appaccess.GatewayRebindDatabaseCommitted ||
		!snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed || snapshot.CurrentSource == nil ||
		snapshot.DatabaseCommittedEvent == nil || snapshot.CurrentDatabaseCommittedEvent == nil {
		return false
	}
	claim := *snapshot.Active.Claim.V2
	lineage, err := gatewayRebindCurrentLineageV2(*request.Terminal)
	return err == nil && claim.State == appaccess.GatewayRebindDatabaseCommitted &&
		sameGatewayRebindClaimV2Admission(request.Attempt.Claim, claim) &&
		sameGatewayRebindRosterV2(request.Attempt.Intent.Roster, snapshot.Active.RosterV2) &&
		sameGatewayRebindRuntimeHeads(request.Attempt.Intent.RuntimeHeads, snapshot.Active.RuntimeHeads) &&
		gatewayCurrentAuthority(lineage) == *snapshot.CurrentSource &&
		gatewayCurrentProfileMatchesAuthority(snapshot.CurrentProfile, *snapshot.CurrentSource) &&
		gatewayRebindTerminalMatchesActiveSnapshot(*request.Terminal, snapshot) &&
		reflect.DeepEqual(snapshot.DatabaseCommittedEvent, snapshot.CurrentDatabaseCommittedEvent) &&
		gatewayCurrentAuthorityHasDatabaseCommit(snapshot.History, snapshot.CurrentDatabaseCommittedEvent, *snapshot.CurrentSource)
}

// Caller owns the effects lease and both gateway locks, before taking its
// commit-barrier latch. SQL must still retain this exact database-committed
// attempt. This restores physical serving only: it never clears the SQL fence,
// advances protected/SQL history, or crosses either process admission latch.
func (m *Manager) restoreGatewayRebindCommittedServingLocked(ctx context.Context,
	repository GatewayCurrentServingStartupRepository, request gatewayRebindPhysicalReconcileRequest,
	authorizeAttempt func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	invalid := func() (gatewayCurrentPhysicalAttestation, error) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	if m == nil || ctx == nil || ctx.Err() != nil || repository == nil || authorizeAttempt == nil ||
		m.gatewayRebindAdmissionBlocked() || !validGatewayRebindCommittedServingRestoreRequest(request) {
		return invalid()
	}
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !gatewayRebindCommittedServingSnapshotMatches(request, snapshot.Rebind) {
		return invalid()
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot.Rebind)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil || selection.Store == nil {
		return invalid()
	}
	state := cloneGatewayCurrentRouteState(*selection.State)
	fail := func() (gatewayCurrentPhysicalAttestation, error) {
		m.gatewayRebindFailStopLatch().Store(true)
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
		defer cancel()
		installed, readErr := selection.Store.load()
		if readErr == nil && reflect.DeepEqual(state, installed) {
			target, targetErr := m.gatewayCurrentOwnedStopTargetForStateLocked(installed)
			if targetErr == nil && m.stopGatewayCurrentOwnedTargetLocked(stopCtx, target) == nil {
				return invalid()
			}
		}
		return gatewayCurrentPhysicalAttestation{}, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	authorizationDigest, err := canonicalDigest(snapshot)
	action := gatewayRebindCommittedServingRestoreAction{Version: 1, Purpose: gatewayRebindCommittedServingRestorePurpose,
		Request: request, State: state, AuthorizationDigest: authorizationDigest}
	action.Digest, _ = gatewayRebindCommittedServingRestoreActionDigest(action)
	if err != nil || !validGatewayRebindCommittedServingRestoreAction(action) {
		return fail()
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	attempt, attemptErr := gatewayRebindPreparedAttemptFromHistory(snapshot.Rebind, history)
	if err != nil || filesErr != nil || attemptErr != nil || !reflect.DeepEqual(attempt, request.Attempt) {
		return fail()
	}
	readAuthority := func(effectCtx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
		if effectCtx == nil || effectCtx.Err() != nil || m.gatewayRebindAdmissionBlocked() {
			return appaccess.HostingGatewayStartupSnapshot{}, gatewayCurrentRouteOperationError(effectCtx)
		}
		fresh, readErr := repository.HostingGatewayStartupSnapshot(effectCtx)
		digest, digestErr := canonicalDigest(fresh)
		if readErr != nil || digestErr != nil || digest != action.AuthorizationDigest ||
			!gatewayRebindCommittedServingSnapshotMatches(request, fresh.Rebind) {
			return fresh, gatewayCurrentRouteOperationError(effectCtx)
		}
		selected, selectErr := m.selectGatewayCurrentLocked(effectCtx, fresh.Rebind)
		if selectErr != nil || !sameGatewayCurrentSelection(selection, selected) ||
			!gatewayCurrentServingRuntimeComponentsMatch(state, fresh) || effectCtx.Err() != nil || m.gatewayRebindAdmissionBlocked() {
			return fresh, gatewayCurrentRouteOperationError(effectCtx)
		}
		return fresh, nil
	}
	guard := func(effectCtx context.Context) error {
		if effectCtx == nil || effectCtx.Err() != nil || m.gatewayRebindAdmissionBlocked() || authorizeAttempt(effectCtx) != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		fresh, readErr := readAuthority(effectCtx)
		if readErr != nil {
			return readErr
		}
		claims, claimsErr := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(fresh.Grants),
			GatewayLANDisableStartupClaims(fresh.Disables))
		inspection, inspectErr := gatewayCurrentLANStartupCensus(state, claims)
		if claimsErr != nil || inspectErr != nil || inspection.Disposition != GatewayV2LANStartupNormal ||
			m.validateGatewayCurrentLANRetainedHistoryLocked(effectCtx, claims, fresh.Rebind) != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		after, scanErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterFiles, readErr := readGatewayHistorySnapshotMode(m.store, true)
		if scanErr != nil || readErr != nil || !sameGatewayRebindCurrentHistory(history, after) ||
			!sameGatewayHistorySnapshot(files, afterFiles) || authorizeAttempt(effectCtx) != nil {
			return gatewayCurrentRouteOperationError(effectCtx)
		}
		_, readErr = readAuthority(effectCtx)
		return readErr
	}
	if guard(ctx) != nil {
		return fail()
	}
	driver, ok := m.currentPhysicalDriver().(gatewayRebindCommittedServingRestoreDriver)
	if !ok {
		return fail()
	}
	proof, err := driver.restoreGatewayRebindCommittedServing(ctx, action, guard)
	terminal, terminalErr := newGatewayRebindAttemptTerminalViewV2(*request.Terminal)
	if err != nil || terminalErr != nil || !validGatewayCurrentPhysicalAttestation(proof) ||
		proof.Outcome != gatewayCurrentPhysicalStableServing || proof.Runtime.ListenerAbsent ||
		!reflect.DeepEqual(proof.State, state) || !reflect.DeepEqual(proof.Terminal, terminal) ||
		proof.Lineage != state.Lineage || proof.Pending != nil || proof.LANRecovery != nil || guard(ctx) != nil {
		return fail()
	}
	return proof, nil
}
