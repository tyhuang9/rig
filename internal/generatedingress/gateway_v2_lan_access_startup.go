package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	GatewayV2LANRecoveryGrant   = "lan_grant"
	GatewayV2LANRecoveryDisable = "lan_disable"
)

// GatewayV2LANDisableStartupClaim is built from one validated, single-read
// database snapshot. No mutable HTTP state participates in startup selection.
type GatewayV2LANDisableStartupClaim struct {
	Request           GatewayV2LANDisableRequest
	State             appaccess.AppAccessDisableState
	StateSequence     int64
	RequiresRecovery  bool
	ClearAcknowledged bool
}

type GatewayV2LANAccessStartupInspection struct {
	Disposition  GatewayV2LANStartupDisposition
	RecoveryKind string
	OperationID  string
	AppID        string
}

type gatewayV2LANAccessStartupClaims struct {
	grants       gatewayV2LANStartupClaimSet
	disables     map[string]GatewayV2LANDisableStartupClaim
	byAllocation map[string]GatewayV2LANDisableStartupClaim
}

func validateGatewayV2LANAccessStartupClaims(grants []GatewayV2LANStartupClaim,
	disables []GatewayV2LANDisableStartupClaim,
) (gatewayV2LANAccessStartupClaims, error) {
	grantSet, err := validateGatewayV2LANStartupClaims(grants)
	if err != nil {
		return gatewayV2LANAccessStartupClaims{}, err
	}
	result := gatewayV2LANAccessStartupClaims{
		grants: grantSet, disables: make(map[string]GatewayV2LANDisableStartupClaim, len(disables)),
		byAllocation: make(map[string]GatewayV2LANDisableStartupClaim, len(disables)),
	}
	for _, claim := range disables {
		if !validGatewayV2LANDisableRequest(claim.Request) || !validGatewayV2LANDisableStartupSequence(claim.State, claim.StateSequence) ||
			(claim.ClearAcknowledged && claim.State != appaccess.AppAccessDisableCommitted) {
			return gatewayV2LANAccessStartupClaims{}, gatewayV2StartupInspectionError(nil)
		}
		if _, duplicate := result.disables[claim.Request.OperationID]; duplicate {
			return gatewayV2LANAccessStartupClaims{}, gatewayV2StartupInspectionError(nil)
		}
		if _, duplicate := result.byAllocation[claim.Request.AllocationID]; duplicate {
			return gatewayV2LANAccessStartupClaims{}, gatewayV2StartupInspectionError(nil)
		}
		if claim.Request.SourceGrant != nil {
			grant, exists := grantSet.byAttempt[claim.Request.SourceGrant.AttemptID]
			if !exists || grant.Request != *claim.Request.SourceGrant || grant.DisableIntentOperationID != claim.Request.OperationID {
				return gatewayV2LANAccessStartupClaims{}, gatewayV2StartupInspectionError(nil)
			}
		}
		result.disables[claim.Request.OperationID] = claim
		result.byAllocation[claim.Request.AllocationID] = claim
	}
	return result, nil
}

func validGatewayV2LANDisableStartupSequence(state appaccess.AppAccessDisableState, sequence int64) bool {
	switch state {
	case appaccess.AppAccessDisablePrepared:
		return sequence == 1
	case appaccess.AppAccessDisableWithdrawing:
		return sequence == 2
	case appaccess.AppAccessDisableUncertain:
		return sequence >= 2
	case appaccess.AppAccessDisableCommitted:
		return sequence >= 3
	default:
		return false
	}
}

func (m *Manager) InspectGatewayV2LANAccessStartup(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) (inspection GatewayV2LANAccessStartupInspection, resultErr error) {
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if m == nil || ctx == nil || err != nil {
		return GatewayV2LANAccessStartupInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return GatewayV2LANAccessStartupInspection{}, err
	}
	defer releaseGatewayLock(release, &resultErr)
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || !validGatewayV2RouteState(state) {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}
	inspection, err = inspectGatewayV2LANAccessStartupLocked(proofCtx, state, journal, claims, m.gatewayV2LANDisableDriver())
	if err != nil {
		return GatewayV2LANAccessStartupInspection{}, err
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}
	return inspection, nil
}

func inspectGatewayV2LANAccessStartupLocked(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, claims gatewayV2LANAccessStartupClaims, driver gatewayV2LANGrantDriver,
) (GatewayV2LANAccessStartupInspection, error) {
	if ctx == nil || driver == nil || driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	recovery := GatewayV2LANAccessStartupInspection{Disposition: GatewayV2LANStartupNormal}
	setRecovery := func(kind, operationID, appID string) bool {
		if recovery.RecoveryKind != "" && (recovery.RecoveryKind != kind || recovery.OperationID != operationID || recovery.AppID != appID) {
			return false
		}
		recovery = GatewayV2LANAccessStartupInspection{
			Disposition: GatewayV2LANStartupRecoveryOnly, RecoveryKind: kind, OperationID: operationID, AppID: appID,
		}
		return true
	}
	protected := make(map[string]struct{})
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, *app.LAN)
		grant, exists := claims.grants.byAttempt[request.AttemptID]
		if !exists || grant.Request != request {
			return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		protected[request.AttemptID] = struct{}{}
		if disable, exists := claims.byAllocation[request.AllocationID]; exists {
			if (disable.State == appaccess.AppAccessDisableCommitted &&
				(state.Pending == nil || state.Pending.Kind != gatewayV2PendingLANDisable ||
					state.Pending.Disable == nil || state.Pending.Disable.OperationID != disable.Request.OperationID)) ||
				disable.Request.SourceGrant == nil || *disable.Request.SourceGrant != request ||
				!setRecovery(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		} else if grant.State != appaccess.AppAccessGrantCommitted || grant.RequiresRecovery || grant.DisableIntentOperationID != "" {
			if !setRecovery(GatewayV2LANRecoveryGrant, request.AttemptID, request.AppID) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		}
	}
	if state.Pending != nil {
		switch state.Pending.Kind {
		case gatewayV2PendingLANDisable:
			if state.Pending.Disable == nil {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
			request := *state.Pending.Disable
			claim, exists := claims.disables[request.OperationID]
			original, withdrawn, pendingErr := gatewayV2LANDisablePendingStates(state, request)
			if !exists || claim.ClearAcknowledged || !reflect.DeepEqual(claim.Request, request) || pendingErr != nil ||
				!setRecovery(GatewayV2LANRecoveryDisable, request.OperationID, request.AppID) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
			switch driver.observePending(ctx, original, withdrawn, journal) {
			case gatewayV2PendingCommittedExact, gatewayV2PendingProposedExact, gatewayV2PendingReloadOnlyMixed:
			default:
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		case gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal:
			request, pendingErr := gatewayV2LANPendingRequest(*state.Pending)
			grant, exists := claims.grants.byAttempt[request.AttemptID]
			original, proposed, pending, statesErr := gatewayV2LANGrantStatesForRequest(state, request)
			if pendingErr != nil || !exists || grant.Request != request || statesErr != nil || !pending ||
				!setRecovery(GatewayV2LANRecoveryGrant, request.AttemptID, request.AppID) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
			switch driver.observePending(ctx, original, proposed, journal) {
			case gatewayV2PendingCommittedExact, gatewayV2PendingProposedExact, gatewayV2PendingReloadOnlyMixed:
			default:
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		default:
			return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
	} else if !driver.proveCommitted(ctx, state, journal) || !driver.proveAllGranted(ctx, state, journal) {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	for attemptID, grant := range claims.grants.byAttempt {
		if _, exists := protected[attemptID]; exists {
			continue
		}
		if state.Pending != nil && state.Pending.Kind == gatewayV2PendingLANGrant &&
			state.Pending.Proposed.LAN != nil && state.Pending.Proposed.LAN.GrantAttemptID == attemptID {
			continue
		}
		if disable, exists := claims.byAllocation[grant.Request.AllocationID]; exists && disable.Request.SourceGrant != nil &&
			*disable.Request.SourceGrant == grant.Request {
			continue
		}
		if grant.State != appaccess.AppAccessGrantRolledBack &&
			!setRecovery(GatewayV2LANRecoveryGrant, attemptID, grant.Request.AppID) {
			return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
	}
	for _, disable := range claims.disables {
		// A committed disable is immutable history. A later allocation may
		// deliberately reuse the same app and port, so its old 404 proof
		// must not be applied to the new grant. An exact old source binding
		// still present without the old pending marker is inconsistent.
		if disable.State == appaccess.AppAccessDisableCommitted &&
			(state.Pending == nil || state.Pending.Kind != gatewayV2PendingLANDisable ||
				state.Pending.Disable == nil || state.Pending.Disable.OperationID != disable.Request.OperationID) {
			if disable.Request.SourceGrant != nil {
				if _, live := protected[disable.Request.SourceGrant.AttemptID]; live {
					return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
				}
			}
			if !disable.ClearAcknowledged {
				if !gatewayV2LANDisableStateMatches(state, disable.Request) ||
					!proveGatewayV2LANDisabled(ctx, driver, state, journal, disable.Request) ||
					!setRecovery(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID) {
					return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
				}
			}
			continue
		}
		if !gatewayV2LANDisableStateMatches(state, disable.Request) {
			return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		if disable.State != appaccess.AppAccessDisableCommitted || disable.RequiresRecovery {
			if !setRecovery(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		}
		if state.Pending == nil && !gatewayV2LANDisableSourceBinding(disable.Request, state.Apps[disable.Request.AppID]) {
			if !proveGatewayV2LANDisabled(ctx, driver, state, journal, disable.Request) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		}
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil || ctx.Err() != nil {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	return recovery, nil
}

func (m *Manager) QuarantineGatewayV2LANAccessStartup(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) error {
	inspection, err := m.InspectGatewayV2LANAccessStartup(ctx, grants, disables)
	if err != nil || inspection.Disposition == GatewayV2LANStartupNormal {
		return err
	}
	if inspection.RecoveryKind == GatewayV2LANRecoveryGrant {
		filtered := make([]GatewayV2LANStartupClaim, 0, len(grants))
		retired := make(map[string]struct{})
		for _, disable := range disables {
			if disable.State == appaccess.AppAccessDisableCommitted && disable.Request.SourceGrant != nil {
				retired[disable.Request.SourceGrant.AttemptID] = struct{}{}
			}
		}
		for _, grant := range grants {
			if _, skip := retired[grant.Request.AttemptID]; !skip {
				filtered = append(filtered, grant)
			}
		}
		return m.QuarantineGatewayV2LANStartup(ctx, filtered)
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	claim, exists := claims.disables[inspection.OperationID]
	if !exists {
		return gatewayV2StartupInspectionError(ctx)
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer release()
	workCtx, cancelWork := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelWork()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil {
		return gatewayV2StartupInspectionError(workCtx)
	}
	confirmed, err := inspectGatewayV2LANAccessStartupLocked(workCtx, state, journal, claims, m.gatewayV2LANDisableDriver())
	if err != nil || confirmed != inspection {
		return gatewayV2StartupInspectionError(workCtx)
	}
	driver := m.gatewayV2LANDisableDriver()
	request := claim.Request
	if state.Pending == nil {
		app, exists := state.Apps[request.AppID]
		if !exists || app.LAN == nil {
			if !proveGatewayV2LANDisabled(workCtx, driver, state, journal, request) {
				return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
			}
			return nil
		}
		pending, _, pendingErr := gatewayV2LANDisablePendingState(state, request)
		if pendingErr != nil || store.saveCommittedV2State(pending, journal) != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		state = pending
	}
	original, withdrawn, err := gatewayV2LANDisablePendingStates(state, request)
	if err != nil {
		return gatewayV2StartupInspectionError(workCtx)
	}
	switch driver.observePending(workCtx, original, withdrawn, journal) {
	case gatewayV2PendingCommittedExact, gatewayV2PendingReloadOnlyMixed:
		if driver.apply(workCtx, withdrawn, "lan-disable-startup.json") != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
	case gatewayV2PendingProposedExact:
	default:
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if !proveGatewayV2LANDisabled(workCtx, driver, withdrawn, journal, request) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	retained, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	return nil
}
