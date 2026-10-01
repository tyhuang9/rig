package generatedingress

import (
	"context"
	"reflect"
	"sort"

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
	// Recoveries is the complete deterministic recovery census when more than
	// one operation requires recovery. The legacy singular fields above remain
	// populated for the one-operation case, so existing startup consumers retain
	// their contract. A caller must not select an arbitrary item when this list
	// is populated.
	Recoveries []GatewayV2LANAccessStartupRecovery
}

// GatewayV2LANAccessStartupRecovery identifies one exact durable recovery
// operation. OperationID is a grant AttemptID for lan_grant and a disable
// OperationID for lan_disable.
type GatewayV2LANAccessStartupRecovery struct {
	Kind        string
	OperationID string
	AppID       string
}

type gatewayV2LANAccessStartupRecoveryCandidate struct {
	GatewayV2LANAccessStartupRecovery
	port uint16
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
			if !exists || grant.Request != *claim.Request.SourceGrant ||
				grant.State != appaccess.AppAccessGrantCommitted ||
				grant.DisableIntentOperationID != claim.Request.OperationID {
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
	if ctx == nil || driver == nil || state.LANRecovery != nil || driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	recoveryCandidates := make(map[string]gatewayV2LANAccessStartupRecoveryCandidate)
	addRecovery := func(kind, operationID, appID string, port uint16) bool {
		if (kind != GatewayV2LANRecoveryGrant && kind != GatewayV2LANRecoveryDisable) ||
			!validCanonicalUUID(operationID) || !validAppID(appID) ||
			port < state.Profile.PortStart || port > state.Profile.PortEnd {
			return false
		}
		candidate := gatewayV2LANAccessStartupRecoveryCandidate{
			GatewayV2LANAccessStartupRecovery: GatewayV2LANAccessStartupRecovery{
				Kind: kind, OperationID: operationID, AppID: appID,
			},
			port: port,
		}
		key := kind + "\x00" + operationID + "\x00" + appID
		if existing, exists := recoveryCandidates[key]; exists {
			return existing.port == candidate.port
		}
		recoveryCandidates[key] = candidate
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
			legacyPair := gatewayV2LANRecoveryLegacyPreparedPair(grant, disable)
			if (disable.State == appaccess.AppAccessDisableCommitted &&
				(state.Pending == nil || state.Pending.Kind != gatewayV2PendingLANDisable ||
					state.Pending.Disable == nil || state.Pending.Disable.OperationID != disable.Request.OperationID)) ||
				(!legacyPair && (disable.Request.SourceGrant == nil || *disable.Request.SourceGrant != request)) ||
				(legacyPair && !addRecovery(GatewayV2LANRecoveryGrant, request.AttemptID, request.AppID, request.Port)) ||
				!addRecovery(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID, disable.Request.Port) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		} else if grant.State != appaccess.AppAccessGrantCommitted || grant.RequiresRecovery || grant.DisableIntentOperationID != "" {
			if !addRecovery(GatewayV2LANRecoveryGrant, request.AttemptID, request.AppID, request.Port) {
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
				!addRecovery(GatewayV2LANRecoveryDisable, request.OperationID, request.AppID, request.Port) {
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
				!addRecovery(GatewayV2LANRecoveryGrant, request.AttemptID, request.AppID, request.Port) {
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
			!addRecovery(GatewayV2LANRecoveryGrant, attemptID, grant.Request.AppID, grant.Request.Port) {
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
					!addRecovery(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID, disable.Request.Port) {
					return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
				}
			}
			continue
		}
		legacyGrant, legacyPair := gatewayV2LANRecoveryLegacyGrantForDisable(claims, disable)
		if !gatewayV2LANDisableStateMatches(state, disable.Request) &&
			(!legacyPair || state.Apps[disable.Request.AppID].LAN == nil ||
				!gatewayV2LANBindingMatchesRequest(state.Apps[disable.Request.AppID].LAN, legacyGrant.Request)) {
			return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
		}
		if disable.State != appaccess.AppAccessDisableCommitted || disable.RequiresRecovery {
			if !addRecovery(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID, disable.Request.Port) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		}
		if state.Pending == nil && !legacyPair &&
			!gatewayV2LANDisableSourceBinding(disable.Request, state.Apps[disable.Request.AppID]) {
			if !proveGatewayV2LANDisabled(ctx, driver, state, journal, disable.Request) {
				return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
			}
		}
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil || ctx.Err() != nil {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	if len(recoveryCandidates) == 0 {
		return GatewayV2LANAccessStartupInspection{Disposition: GatewayV2LANStartupNormal}, nil
	}
	if !gatewayV2LANRecoveryCandidatesCompatible(recoveryCandidates, claims) {
		return GatewayV2LANAccessStartupInspection{}, gatewayV2StartupInspectionError(ctx)
	}
	ordered := make([]gatewayV2LANAccessStartupRecoveryCandidate, 0, len(recoveryCandidates))
	for _, candidate := range recoveryCandidates {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].AppID != ordered[right].AppID {
			return ordered[left].AppID < ordered[right].AppID
		}
		if ordered[left].port != ordered[right].port {
			return ordered[left].port < ordered[right].port
		}
		if ordered[left].Kind != ordered[right].Kind {
			return gatewayV2LANRecoveryKindOrder(gatewayV2PendingKind(ordered[left].Kind)) <
				gatewayV2LANRecoveryKindOrder(gatewayV2PendingKind(ordered[right].Kind))
		}
		return ordered[left].OperationID < ordered[right].OperationID
	})
	if len(ordered) == 1 {
		return GatewayV2LANAccessStartupInspection{
			Disposition:  GatewayV2LANStartupRecoveryOnly,
			RecoveryKind: ordered[0].Kind,
			OperationID:  ordered[0].OperationID,
			AppID:        ordered[0].AppID,
		}, nil
	}
	recoveries := make([]GatewayV2LANAccessStartupRecovery, 0, len(ordered))
	for _, candidate := range ordered {
		recoveries = append(recoveries, candidate.GatewayV2LANAccessStartupRecovery)
	}
	return GatewayV2LANAccessStartupInspection{
		Disposition: GatewayV2LANStartupRecoveryOnly,
		Recoveries:  recoveries,
	}, nil
}

// Migration 029 could backfill a prepared disable beside the exact prepared
// migration-028 grant it froze. That is the sole recovery collision allowed:
// the grant must roll back before the disable can commit the same immutable
// allocation lineage. New writes are fenced from creating this shape.
func gatewayV2LANRecoveryLegacyPreparedPair(grant GatewayV2LANStartupClaim,
	disable GatewayV2LANDisableStartupClaim,
) bool {
	return grant.State == appaccess.AppAccessGrantPrepared && grant.StateSequence == 1 &&
		grant.DisableIntentOperationID == disable.Request.OperationID &&
		disable.State == appaccess.AppAccessDisablePrepared && disable.StateSequence == 1 &&
		!disable.ClearAcknowledged && disable.Request.SourceGrant == nil &&
		gatewayV2LANDisableMatchesGrantIdentity(disable.Request, grant.Request)
}

func gatewayV2LANDisableMatchesGrantIdentity(disable GatewayV2LANDisableRequest,
	grant GatewayV2LANGrantRequest,
) bool {
	return disable.AppID == grant.AppID && disable.AllocationID == grant.AllocationID &&
		disable.OwnerOperationID == grant.OwnerOperationID && disable.Port == grant.Port &&
		disable.AccessRevisionID == grant.AccessRevisionID &&
		disable.AccessRevisionNumber == grant.AccessRevisionNumber &&
		disable.AccessSpecDigest == grant.AccessSpecDigest &&
		disable.GatewayProfileRevisionID == grant.GatewayProfileRevisionID &&
		disable.GatewayProfileRevisionNumber == grant.GatewayProfileRevisionNumber &&
		disable.GatewayProfileSpecDigest == grant.GatewayProfileSpecDigest
}

func gatewayV2LANBindingMatchesRequest(binding *gatewayV2LANBinding, request GatewayV2LANGrantRequest) bool {
	if binding == nil {
		return false
	}
	want, err := gatewayV2LANBindingForRequest(request)
	return err == nil && reflect.DeepEqual(*binding, want)
}

func gatewayV2LANRecoveryLegacyGrantForDisable(claims gatewayV2LANAccessStartupClaims,
	disable GatewayV2LANDisableStartupClaim,
) (GatewayV2LANStartupClaim, bool) {
	if disable.Request.SourceGrant != nil {
		return GatewayV2LANStartupClaim{}, false
	}
	for _, grant := range claims.grants.byAttempt {
		if gatewayV2LANRecoveryLegacyPreparedPair(grant, disable) {
			return grant, true
		}
	}
	return GatewayV2LANStartupClaim{}, false
}

func gatewayV2LANRecoveryCandidatesCompatible(
	candidates map[string]gatewayV2LANAccessStartupRecoveryCandidate,
	claims gatewayV2LANAccessStartupClaims,
) bool {
	byApp := make(map[string][]gatewayV2LANAccessStartupRecoveryCandidate)
	byPort := make(map[uint16][]gatewayV2LANAccessStartupRecoveryCandidate)
	for _, candidate := range candidates {
		byApp[candidate.AppID] = append(byApp[candidate.AppID], candidate)
		byPort[candidate.port] = append(byPort[candidate.port], candidate)
	}
	for _, collisions := range byApp {
		if len(collisions) > 1 && !gatewayV2LANRecoveryCandidatesAreLegacyPair(collisions, claims) {
			return false
		}
	}
	for _, collisions := range byPort {
		if len(collisions) > 1 && !gatewayV2LANRecoveryCandidatesAreLegacyPair(collisions, claims) {
			return false
		}
	}
	return true
}

func gatewayV2LANRecoveryCandidatesAreLegacyPair(
	candidates []gatewayV2LANAccessStartupRecoveryCandidate,
	claims gatewayV2LANAccessStartupClaims,
) bool {
	if len(candidates) != 2 || candidates[0].AppID != candidates[1].AppID ||
		candidates[0].port != candidates[1].port {
		return false
	}
	var grantCandidate, disableCandidate *gatewayV2LANAccessStartupRecoveryCandidate
	for index := range candidates {
		switch candidates[index].Kind {
		case GatewayV2LANRecoveryGrant:
			grantCandidate = &candidates[index]
		case GatewayV2LANRecoveryDisable:
			disableCandidate = &candidates[index]
		}
	}
	if grantCandidate == nil || disableCandidate == nil {
		return false
	}
	grant, grantExists := claims.grants.byAttempt[grantCandidate.OperationID]
	disable, disableExists := claims.disables[disableCandidate.OperationID]
	return grantExists && disableExists && gatewayV2LANRecoveryLegacyPreparedPair(grant, disable)
}

func (m *Manager) QuarantineGatewayV2LANAccessStartup(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) error {
	inspection, err := m.InspectGatewayV2LANAccessStartup(ctx, grants, disables)
	if err != nil || inspection.Disposition == GatewayV2LANStartupNormal {
		return err
	}
	if len(inspection.Recoveries) > 1 {
		// A legacy one-operation protected marker cannot represent a batch
		// without erasing uncertainty. The batch quarantine path must install
		// its immutable record before any recovery listener can be exposed.
		return gatewayV2StartupInspectionError(ctx)
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
	if err != nil || !reflect.DeepEqual(confirmed, inspection) {
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
