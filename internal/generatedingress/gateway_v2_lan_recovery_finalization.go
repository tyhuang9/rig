package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayV2LANRecoveryHead is the only batch item a controller may dispatch.
// Head is zero based and Count is the immutable batch length.
type GatewayV2LANRecoveryHead struct {
	Kind        string
	OperationID string
	AppID       string
	Head        int
	Count       int
}

// HasGatewayV2LANRecoveryBatch reads only the exact journal-bound protected
// state. It lets startup choose the quarantine resume path before any topology
// proof or listener work. Malformed or partial history is an error.
func (m *Manager) HasGatewayV2LANRecoveryBatch(ctx context.Context) (present bool, resultErr error) {
	if m == nil || ctx == nil {
		return false, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return false, err
	}
	defer releaseGatewayLock(release, &resultErr)
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil {
		return false, gatewayV2LANDisableError(ctx)
	}
	if !committed {
		return false, nil
	}
	if store == nil || !validGatewayV2RouteState(state) {
		return false, gatewayV2LANDisableError(ctx)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return false, gatewayV2LANDisableError(ctx)
	}
	return state.LANRecovery != nil, nil
}

// ObserveGatewayV2LANRecoveryHead validates one complete database snapshot and
// returns the exact protected head only after proving the whole quarantined
// gateway. A completed batch is present with Head == Count and empty identity;
// it must pass through RetireGatewayV2LANRecoveryBatch before normal admission.
func (m *Manager) ObserveGatewayV2LANRecoveryHead(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) (head GatewayV2LANRecoveryHead, present bool, resultErr error) {
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if m == nil || ctx == nil || err != nil {
		return GatewayV2LANRecoveryHead{}, false, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return GatewayV2LANRecoveryHead{}, false, err
	}
	defer releaseGatewayLock(release, &resultErr)
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil {
		return GatewayV2LANRecoveryHead{}, false, gatewayV2LANDisableError(proofCtx)
	}
	if !committed {
		return GatewayV2LANRecoveryHead{}, false, nil
	}
	if store == nil || !validGatewayV2RouteState(state) {
		return GatewayV2LANRecoveryHead{}, false, gatewayV2LANDisableError(proofCtx)
	}
	if state.LANRecovery == nil {
		return GatewayV2LANRecoveryHead{}, false, nil
	}
	driver, ok := m.gatewayV2LANDisableDriver().(gatewayV2LANRecoveryBatchDriver)
	if !ok || !gatewayV2LANRecoveryCensusMatchesHead(state, claims) ||
		!proveGatewayV2LANRecoveryProtected(proofCtx, state, journal, driver) {
		return GatewayV2LANRecoveryHead{}, true, gatewayV2LANDisableError(proofCtx)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return GatewayV2LANRecoveryHead{}, true, gatewayV2LANDisableError(proofCtx)
	}
	head = gatewayV2LANRecoveryHeadIdentity(state)
	return head, true, nil
}

// WithGatewayV2LANRecoveryGrantFinalization resolves only the exact protected
// grant head. resolve must terminally roll back and read back that exact claim;
// a committed stale grant has no safe automatic publication policy here. The
// callback runs under the gateway lock and must not call Manager methods.
func (m *Manager) WithGatewayV2LANRecoveryGrantFinalization(ctx context.Context,
	request GatewayV2LANGrantRequest,
	resolve func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil || resolve == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	return m.withGatewayV2LANRecoveryHead(ctx, gatewayV2PendingLANGrant, request.AttemptID, request.AppID,
		func(item gatewayV2LANRecoveryItem) bool {
			return item.Grant != nil &&
				gatewayV2LANGrantRequestForBinding(item.AppID, *item.Grant) == request
		},
		func(workCtx context.Context, state gatewayV2RouteState, _ gatewayV2LANRecoveryItem) error {
			observation, err := newGatewayV2LANGrantObservation(
				state, request, GatewayV2LANGrantWithdrawnPendingReconciliation, true,
			)
			if err != nil {
				return err
			}
			return resolve(workCtx, observation)
		}, nil)
}

// WithGatewayV2LANRecoveryDisableFinalization resolves only the exact protected
// disable head. resolve terminally commits the disable. After the exact
// protected binding clear and a fresh whole-gateway proof, acknowledge must
// durably record that clear before the head can advance. Both callbacks run
// under the gateway lock and must not call Manager methods.
func (m *Manager) WithGatewayV2LANRecoveryDisableFinalization(ctx context.Context,
	request GatewayV2LANDisableRequest,
	resolve func(context.Context, GatewayV2LANDisableObservation) error,
	acknowledge func(context.Context, GatewayV2LANDisableObservation) error,
) (resultErr error) {
	if m == nil || ctx == nil || !validGatewayV2LANDisableRequest(request) || resolve == nil || acknowledge == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	return m.withGatewayV2LANRecoveryHead(ctx, gatewayV2PendingLANDisable, request.OperationID, request.AppID,
		func(item gatewayV2LANRecoveryItem) bool {
			return item.Disable != nil && reflect.DeepEqual(*item.Disable, request)
		},
		func(workCtx context.Context, state gatewayV2RouteState, _ gatewayV2LANRecoveryItem) error {
			observation, err := gatewayV2LANDisableObservation(state, request, GatewayV2LANDisableWithdrawnPending)
			if err != nil {
				return err
			}
			return resolve(workCtx, observation)
		}, func(workCtx context.Context, cleared gatewayV2RouteState, _ gatewayV2LANRecoveryItem) error {
			observation, err := gatewayV2LANDisableObservation(cleared, request, GatewayV2LANDisableDisabled)
			if err != nil {
				return err
			}
			return acknowledge(workCtx, observation)
		})
}

func (m *Manager) withGatewayV2LANRecoveryHead(ctx context.Context, kind gatewayV2PendingKind,
	operationID, appID string,
	matches func(gatewayV2LANRecoveryItem) bool,
	resolve func(context.Context, gatewayV2RouteState, gatewayV2LANRecoveryItem) error,
	afterClear func(context.Context, gatewayV2RouteState, gatewayV2LANRecoveryItem) error,
) (resultErr error) {
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	workCtx, cancelWork := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelWork()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || state.LANRecovery == nil ||
		state.LANRecovery.Head >= len(state.LANRecovery.Items) {
		return gatewayV2LANDisableError(workCtx)
	}
	driver, ok := m.gatewayV2LANDisableDriver().(gatewayV2LANRecoveryBatchDriver)
	if !ok {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, m.gatewayV2LANDisableDriver())
	}
	item := state.LANRecovery.Items[state.LANRecovery.Head]
	_, protectedOperationID := gatewayV2LANRecoveryItemIdentity(item)
	if item.Kind != kind || item.AppID != appID || protectedOperationID != operationID ||
		matches == nil || !matches(item) {
		return gatewayV2LANDisableError(workCtx)
	}
	if !proveGatewayV2LANRecoveryProtected(workCtx, state, journal, driver) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if err := resolve(workCtx, state, item); err != nil {
		return err
	}
	if !proveGatewayV2LANRecoveryProtected(workCtx, state, journal, driver) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	confirmed, confirmedJournal, err = store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	cleared, err := gatewayV2LANRecoveryClearedHeadState(state)
	if err != nil || store.saveCommittedV2State(cleared, journal) != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if !proveGatewayV2LANRecoveryProtected(workCtx, cleared, journal, driver) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	installed, installedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(installed, cleared) || !reflect.DeepEqual(installedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if afterClear != nil {
		if err := afterClear(workCtx, cleared, item); err != nil {
			return err
		}
		if !proveGatewayV2LANRecoveryProtected(workCtx, cleared, journal, driver) {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		installed, installedJournal, err = store.loadBoundUpgrade(journal.OperationID)
		if err != nil || !reflect.DeepEqual(installed, cleared) || !reflect.DeepEqual(installedJournal, journal) {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
	}
	advanced, err := gatewayV2LANRecoveryAdvanceHeadState(cleared)
	if err != nil || store.saveCommittedV2State(advanced, journal) != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if !proveGatewayV2LANRecoveryProtected(workCtx, advanced, journal, driver) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	installed, installedJournal, err = store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(installed, advanced) || !reflect.DeepEqual(installedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	return nil
}

// RetireGatewayV2LANRecoveryBatch removes a completed startup fence only after
// one complete database snapshot proves every immutable item terminal and a
// fresh ingress proof shows every retired port absent.
func (m *Manager) RetireGatewayV2LANRecoveryBatch(ctx context.Context,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) (resultErr error) {
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if m == nil || ctx == nil || err != nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	workCtx, cancelWork := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelWork()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || state.LANRecovery == nil ||
		state.LANRecovery.Head != len(state.LANRecovery.Items) ||
		!gatewayV2LANRecoveryCensusMatchesHead(state, claims) {
		return gatewayV2LANDisableError(workCtx)
	}
	driver, ok := m.gatewayV2LANDisableDriver().(gatewayV2LANRecoveryBatchDriver)
	if !ok || !proveGatewayV2LANRecoveryProtected(workCtx, state, journal, driver) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, m.gatewayV2LANDisableDriver())
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	retired := cloneGatewayV2RouteState(state)
	retired.LANRecovery = nil
	if !validGatewayV2RouteState(retired) || store.saveCommittedV2State(retired, journal) != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if !proveGatewayV2LANRecoveryRetired(workCtx, state, retired, journal, driver) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	installed, installedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(installed, retired) || !reflect.DeepEqual(installedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	return nil
}

func gatewayV2LANRecoveryHeadIdentity(state gatewayV2RouteState) GatewayV2LANRecoveryHead {
	batch := state.LANRecovery
	if batch == nil {
		return GatewayV2LANRecoveryHead{}
	}
	result := GatewayV2LANRecoveryHead{Head: batch.Head, Count: len(batch.Items)}
	if batch.Head == len(batch.Items) {
		return result
	}
	item := batch.Items[batch.Head]
	_, result.OperationID = gatewayV2LANRecoveryItemIdentity(item)
	result.AppID = item.AppID
	switch item.Kind {
	case gatewayV2PendingLANGrant:
		result.Kind = GatewayV2LANRecoveryGrant
	case gatewayV2PendingLANDisable:
		result.Kind = GatewayV2LANRecoveryDisable
	}
	return result
}

func proveGatewayV2LANRecoveryProtected(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, driver gatewayV2LANRecoveryBatchDriver,
) bool {
	if ctx == nil || driver == nil || state.LANRecovery == nil || !validGatewayV2RouteState(state) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return false
	}
	effective, _, err := gatewayV2LANRecoveryEffectiveProjection(state)
	if err != nil || driver.observeLANRecoveryBatch(ctx, state, effective, journal) != gatewayV2LANRecoveryBatchEffectiveExact {
		return false
	}
	allPorts, err := gatewayV2LANRecoveryAllPorts(state)
	if err != nil || len(allPorts) == 0 ||
		!proveGatewayV2LANRecoveryBatch(ctx, driver, effective, journal, allPorts) {
		return false
	}
	// The bulk proof above already attests every immutable batch port and each
	// retained grant, including when the final head has advanced. Repeating a
	// per-port retirement proof here would reattest the whole Docker topology
	// once for every port without observing an intervening mutation.
	return driver.selectedInterfacePreflight(state.Profile) == nil && ctx.Err() == nil
}

func proveGatewayV2LANRecoveryRetired(ctx context.Context, protected, effective gatewayV2RouteState,
	journal gatewayMigrationJournal, driver gatewayV2LANGrantDriver,
) bool {
	if ctx == nil || driver == nil || protected.LANRecovery == nil ||
		protected.LANRecovery.Head != len(protected.LANRecovery.Items) ||
		!validGatewayV2RouteState(protected) || !validGatewayV2RouteState(effective) || effective.LANRecovery != nil ||
		driver.selectedInterfacePreflight(effective.Profile) != nil {
		return false
	}
	ports, err := gatewayV2LANRecoveryAllPorts(protected)
	if err != nil {
		return false
	}
	// The production batch driver attests the committed Docker topology once,
	// then proves every retired port is 404 and all unrelated grants remain.
	// Generic test drivers retain the separate committed-state proof.
	if _, bulk := driver.(gatewayV2LANRecoveryBatchProof); !bulk &&
		!driver.proveCommitted(ctx, effective, journal) {
		return false
	}
	return proveGatewayV2LANRecoveryBatch(ctx, driver, effective, journal, ports) && ctx.Err() == nil
}

func gatewayV2LANRecoveryCensusMatchesHead(state gatewayV2RouteState,
	claims gatewayV2LANAccessStartupClaims,
) bool {
	if !validGatewayV2RouteState(state) || state.LANRecovery == nil {
		return false
	}
	batch := state.LANRecovery
	grantItems := make(map[string]int, len(batch.Items))
	disableItems := make(map[string]int, len(batch.Items))
	disableSourceGrants := make(map[string]string, len(batch.Items))
	for index, item := range batch.Items {
		switch item.Kind {
		case gatewayV2PendingLANGrant:
			request := gatewayV2LANGrantRequestForBinding(item.AppID, *item.Grant)
			claim, exists := claims.grants.byAttempt[request.AttemptID]
			pairedDisableIndex, legacyPair := gatewayV2LANRecoveryLegacyDisableIndex(batch, index)
			if !exists || claim.Request != request ||
				(claim.DisableIntentOperationID != "" && (!legacyPair ||
					claim.DisableIntentOperationID != batch.Items[pairedDisableIndex].Disable.OperationID)) ||
				(claim.DisableIntentOperationID == "" && legacyPair) ||
				!gatewayV2LANRecoveryGrantStateMatchesIndex(claim, index, batch.Head) {
				return false
			}
			if disabled, exists := claims.byAllocation[request.AllocationID]; exists {
				if !legacyPair || disabled.Request.OperationID !=
					batch.Items[pairedDisableIndex].Disable.OperationID {
					return false
				}
			}
			grantItems[request.AttemptID] = index
		case gatewayV2PendingLANDisable:
			request := *item.Disable
			claim, exists := claims.disables[request.OperationID]
			if !exists || !reflect.DeepEqual(claim.Request, request) ||
				!gatewayV2LANRecoveryDisableStateMatchesIndex(claim, index, batch.Head) {
				return false
			}
			if index > 0 && gatewayV2LANRecoveryItemsAreLegacyPair(batch.Items[index-1], item) {
				grantRequest := gatewayV2LANGrantRequestForBinding(item.AppID, *batch.Items[index-1].Grant)
				grantClaim, grantExists := claims.grants.byAttempt[grantRequest.AttemptID]
				if !grantExists || grantClaim.Request != grantRequest ||
					grantClaim.DisableIntentOperationID != request.OperationID {
					return false
				}
				// While the paired grant is still the current head, the disable
				// remains the exact untouched prepared tail. This prevents an
				// out-of-order database commit from being adopted by recovery.
				if index > batch.Head && (claim.State != appaccess.AppAccessDisablePrepared ||
					claim.StateSequence != 1 || claim.ClearAcknowledged) {
					return false
				}
			}
			disableItems[request.OperationID] = index
			if request.SourceGrant != nil {
				source, exists := claims.grants.byAttempt[request.SourceGrant.AttemptID]
				if !exists || source.Request != *request.SourceGrant ||
					source.State != appaccess.AppAccessGrantCommitted ||
					source.DisableIntentOperationID != request.OperationID {
					return false
				}
				disableSourceGrants[request.SourceGrant.AttemptID] = request.OperationID
			}
		default:
			return false
		}
	}
	liveGrants := make(map[string]struct{})
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, *app.LAN)
		claim, exists := claims.grants.byAttempt[request.AttemptID]
		if !exists || claim.Request != request {
			return false
		}
		liveGrants[request.AttemptID] = struct{}{}
		if _, batched := grantItems[request.AttemptID]; batched {
			continue
		}
		if disable, batched := claims.byAllocation[request.AllocationID]; batched {
			if _, exact := disableItems[disable.Request.OperationID]; exact {
				continue
			}
			return false
		}
		if claim.State != appaccess.AppAccessGrantCommitted || claim.RequiresRecovery || claim.DisableIntentOperationID != "" {
			return false
		}
	}
	for attemptID, claim := range claims.grants.byAttempt {
		if _, batched := grantItems[attemptID]; batched {
			continue
		}
		if operationID, source := disableSourceGrants[attemptID]; source &&
			claim.DisableIntentOperationID == operationID {
			continue
		}
		if _, live := liveGrants[attemptID]; live {
			continue
		}
		if claim.State == appaccess.AppAccessGrantRolledBack && claim.DisableIntentOperationID == "" {
			continue
		}
		disable, exists := claims.byAllocation[claim.Request.AllocationID]
		if !exists || claim.State != appaccess.AppAccessGrantCommitted ||
			disable.Request.SourceGrant == nil || *disable.Request.SourceGrant != claim.Request ||
			disable.State != appaccess.AppAccessDisableCommitted || !disable.ClearAcknowledged {
			return false
		}
	}
	for operationID, claim := range claims.disables {
		if _, batched := disableItems[operationID]; batched {
			continue
		}
		if claim.State != appaccess.AppAccessDisableCommitted || !claim.ClearAcknowledged {
			return false
		}
	}
	return true
}

func gatewayV2LANRecoveryLegacyDisableIndex(batch *gatewayV2LANRecoveryBatch, grantIndex int) (int, bool) {
	if batch == nil || grantIndex < 0 || grantIndex+1 >= len(batch.Items) ||
		!gatewayV2LANRecoveryItemsAreLegacyPair(batch.Items[grantIndex], batch.Items[grantIndex+1]) {
		return 0, false
	}
	return grantIndex + 1, true
}

func gatewayV2LANRecoveryGrantStateMatchesIndex(claim GatewayV2LANStartupClaim, index, head int) bool {
	if index < head {
		return claim.State == appaccess.AppAccessGrantRolledBack
	}
	if claim.State == appaccess.AppAccessGrantCommitted {
		return false
	}
	// A terminal rollback is valid only for the current head after a crash
	// between its database callback and protected clear. Accepting it later in
	// the tail would allow out-of-order resolution.
	return claim.State != appaccess.AppAccessGrantRolledBack || index == head
}

func gatewayV2LANRecoveryDisableStateMatchesIndex(claim GatewayV2LANDisableStartupClaim, index, head int) bool {
	if index < head {
		return claim.State == appaccess.AppAccessDisableCommitted && claim.ClearAcknowledged
	}
	// The current head may be replayed after its acknowledgment callback but
	// before protected head advancement. A later item may never be acknowledged.
	return !claim.ClearAcknowledged || index == head
}
