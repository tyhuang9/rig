package generatedingress

import (
	"context"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayV2LANDisableRequest is the immutable, administrator-approved identity
// of one disable operation. SourceGrant identifies the original binding, when
// one existed, and remains part of immutable history after withdrawal.
type GatewayV2LANDisableRequest struct {
	OperationID                  string                    `json:"operationId"`
	RequestDigest                string                    `json:"requestDigest"`
	SpecDigest                   string                    `json:"specDigest"`
	AppID                        string                    `json:"appId"`
	AllocationID                 string                    `json:"allocationId"`
	OwnerOperationID             string                    `json:"ownerOperationId"`
	Port                         uint16                    `json:"port"`
	AccessRevisionID             string                    `json:"accessRevisionId"`
	AccessRevisionNumber         int64                     `json:"accessRevisionNumber"`
	AccessSpecDigest             string                    `json:"accessSpecDigest"`
	ApprovedBy                   string                    `json:"approvedBy"`
	GatewayProfileRevisionID     string                    `json:"gatewayProfileRevisionId"`
	GatewayProfileRevisionNumber int64                     `json:"gatewayProfileRevisionNumber"`
	GatewayProfileSpecDigest     string                    `json:"gatewayProfileSpecDigest"`
	SourceGrant                  *GatewayV2LANGrantRequest `json:"sourceGrant,omitempty"`
}

type GatewayV2LANDisableReceipt struct {
	Request              GatewayV2LANDisableRequest
	GatewayOperationID   string
	ProtectedStateDigest string
	ObservedAt           time.Time
}

type GatewayV2LANDisableResult struct {
	Receipt GatewayV2LANDisableReceipt
}

type GatewayV2LANDisableDisposition string

const (
	GatewayV2LANDisablePendingPublished GatewayV2LANDisableDisposition = "pending_published"
	GatewayV2LANDisableWithdrawnPending GatewayV2LANDisableDisposition = "withdrawn_pending_resolution"
	GatewayV2LANDisableDisabled         GatewayV2LANDisableDisposition = "disabled"
)

type GatewayV2LANDisableObservation struct {
	Request              GatewayV2LANDisableRequest
	Disposition          GatewayV2LANDisableDisposition
	GatewayOperationID   string
	ProtectedStateDigest string
	ObservedAt           time.Time
}

// A disable lease revalidates exact durable authorization and transitions the
// claim from prepared to withdrawing before the first Docker mutation. No
// method may retain a SQLite write transaction across gateway or Docker work.
type GatewayV2LANDisableAuthorizationLease interface {
	Revalidate(context.Context, GatewayV2LANDisableRequest) error
	Withdraw(context.Context, GatewayV2LANDisableRequest) error
	Release() error
}

type GatewayV2LANDisableAuthorizer func(context.Context, GatewayV2LANDisableRequest) (GatewayV2LANDisableAuthorizationLease, error)

// A resolution authorizer rechecks the current administrator and exact claim
// under the gateway lock before any protected-state or Docker mutation.
type GatewayV2LANDisableResolutionAuthorizer func(context.Context, GatewayV2LANDisableRequest) error

func validGatewayV2LANDisableRequest(request GatewayV2LANDisableRequest) bool {
	if !validCanonicalUUID(request.OperationID) || !validSHA256(request.RequestDigest) ||
		!validSHA256(request.SpecDigest) || !validAppID(request.AppID) ||
		!validCanonicalUUID(request.AllocationID) || !validCanonicalUUID(request.OwnerOperationID) ||
		!validCanonicalUUID(request.AccessRevisionID) || request.AccessRevisionNumber <= 0 ||
		!validSHA256(request.AccessSpecDigest) || !validCanonicalUUID(request.ApprovedBy) ||
		!validCanonicalUUID(request.GatewayProfileRevisionID) || request.GatewayProfileRevisionNumber <= 0 ||
		!validSHA256(request.GatewayProfileSpecDigest) {
		return false
	}
	disableDigest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpec{
		AppID: request.AppID, AllocationID: request.AllocationID,
		OwnerOperationID: request.OwnerOperationID, AccessRevisionID: request.AccessRevisionID,
		AccessRevisionNumber: request.AccessRevisionNumber, Port: request.Port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil || disableDigest != request.SpecDigest {
		return false
	}
	accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: request.AppID, AllocationID: request.AllocationID, Port: request.Port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil || accessDigest != request.AccessSpecDigest {
		return false
	}
	if request.SourceGrant == nil {
		return true
	}
	grant := request.SourceGrant
	if _, err := gatewayV2LANBindingForRequest(*grant); err != nil {
		return false
	}
	return grant.AppID == request.AppID && grant.AllocationID == request.AllocationID &&
		grant.OwnerOperationID == request.OwnerOperationID && grant.Port == request.Port &&
		grant.AccessRevisionID == request.AccessRevisionID && grant.AccessRevisionNumber == request.AccessRevisionNumber &&
		grant.AccessSpecDigest == request.AccessSpecDigest &&
		grant.GatewayProfileRevisionID == request.GatewayProfileRevisionID &&
		grant.GatewayProfileRevisionNumber == request.GatewayProfileRevisionNumber &&
		grant.GatewayProfileSpecDigest == request.GatewayProfileSpecDigest
}

func gatewayV2LANDisableSourceBinding(request GatewayV2LANDisableRequest, app gatewayV2AppRoute) bool {
	if app.LAN == nil {
		return request.SourceGrant == nil
	}
	if request.SourceGrant == nil {
		return false
	}
	binding, err := gatewayV2LANBindingForRequest(*request.SourceGrant)
	return err == nil && reflect.DeepEqual(*app.LAN, binding)
}

func validGatewayV2LANDisablePending(state gatewayV2RouteState, pending gatewayV2PendingRoute) bool {
	if !pending.ActivationUncertain || pending.Disable == nil || !validGatewayV2LANDisableRequest(*pending.Disable) ||
		pending.Disable.AppID != pending.AppID || pending.Previous == nil ||
		!reflect.DeepEqual(pending.Previous.Route, pending.Proposed.Route) || pending.Proposed.LAN != nil ||
		pending.Disable.GatewayProfileRevisionID != state.Profile.RevisionID ||
		pending.Disable.GatewayProfileRevisionNumber != state.Profile.RevisionNumber ||
		pending.Disable.GatewayProfileSpecDigest != state.Profile.SpecDigest ||
		pending.Disable.Port < state.Profile.PortStart || pending.Disable.Port > state.Profile.PortEnd {
		return false
	}
	return gatewayV2LANDisableSourceBinding(*pending.Disable, *pending.Previous)
}

func gatewayV2LANDisableError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

// The proof uses the same selected-interface and wrong-host isolation probes
// as a grant rollback. No URL is returned by any disable operation.
func proveGatewayV2LANDisabled(ctx context.Context, driver gatewayV2LANGrantDriver,
	state gatewayV2RouteState, journal gatewayMigrationJournal, request GatewayV2LANDisableRequest,
) bool {
	if ctx == nil || driver == nil || state.Pending != nil ||
		!validGatewayV2RouteState(state) || driver.selectedInterfacePreflight(state.Profile) != nil {
		return false
	}
	if app, exists := state.Apps[request.AppID]; exists && app.LAN != nil {
		return false
	}
	grantRequest := GatewayV2LANGrantRequest{Port: request.Port}
	return driver.proveRolledBack(ctx, state, journal, grantRequest) &&
		driver.proveAllGranted(ctx, state, journal) &&
		driver.selectedInterfacePreflight(state.Profile) == nil
}

func gatewayV2LANDisableStateMatches(state gatewayV2RouteState, request GatewayV2LANDisableRequest) bool {
	if !validGatewayV2RouteState(state) || !validGatewayV2LANDisableRequest(request) ||
		state.Profile.RevisionID != request.GatewayProfileRevisionID ||
		state.Profile.RevisionNumber != request.GatewayProfileRevisionNumber ||
		state.Profile.SpecDigest != request.GatewayProfileSpecDigest ||
		request.Port < state.Profile.PortStart || request.Port > state.Profile.PortEnd {
		return false
	}
	for appID, app := range state.Apps {
		if app.LAN != nil && app.LAN.Port == request.Port && appID != request.AppID {
			return false
		}
	}
	app, exists := state.Apps[request.AppID]
	if !exists || app.LAN == nil {
		return true
	}
	return gatewayV2LANDisableSourceBinding(request, app)
}

func gatewayV2LANDisablePendingState(state gatewayV2RouteState, request GatewayV2LANDisableRequest) (gatewayV2RouteState, gatewayV2RouteState, error) {
	if state.Pending != nil || !gatewayV2LANDisableStateMatches(state, request) {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, gatewayV2LANDisableError(nil)
	}
	app, exists := state.Apps[request.AppID]
	if !exists || app.LAN == nil {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, gatewayV2LANDisableError(nil)
	}
	previous := cloneGatewayV2AppRoute(app)
	proposed := cloneGatewayV2AppRoute(app)
	proposed.LAN = nil
	pending := cloneGatewayV2RouteState(state)
	identity := request
	if request.SourceGrant != nil {
		source := *request.SourceGrant
		identity.SourceGrant = &source
	}
	pending.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANDisable, AppID: request.AppID, Previous: &previous,
		Proposed: proposed, ActivationUncertain: true, Disable: &identity,
	}
	withdrawn := cloneGatewayV2RouteState(state)
	withdrawn.Apps[request.AppID] = proposed
	if !validGatewayV2RouteState(pending) || !validGatewayV2RouteState(withdrawn) {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, gatewayV2LANDisableError(nil)
	}
	return pending, withdrawn, nil
}

func gatewayV2LANDisablePendingStates(state gatewayV2RouteState, request GatewayV2LANDisableRequest) (gatewayV2RouteState, gatewayV2RouteState, error) {
	if !validGatewayV2RouteState(state) || state.Pending == nil || state.Pending.Kind != gatewayV2PendingLANDisable ||
		state.Pending.Disable == nil || !reflect.DeepEqual(*state.Pending.Disable, request) ||
		!gatewayV2LANDisableStateMatches(state, request) {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, gatewayV2LANDisableError(nil)
	}
	committed := cloneGatewayV2RouteState(state)
	committed.Pending = nil
	withdrawn := cloneGatewayV2RouteState(committed)
	withdrawn.Apps[request.AppID] = cloneGatewayV2AppRoute(state.Pending.Proposed)
	if !validGatewayV2RouteState(committed) || !validGatewayV2RouteState(withdrawn) {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, gatewayV2LANDisableError(nil)
	}
	return committed, withdrawn, nil
}

func gatewayV2LANDisableObservation(state gatewayV2RouteState, request GatewayV2LANDisableRequest,
	disposition GatewayV2LANDisableDisposition,
) (GatewayV2LANDisableObservation, error) {
	digest, err := canonicalDigest(state)
	if err != nil || !validSHA256(digest) {
		return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(nil)
	}
	return GatewayV2LANDisableObservation{
		Request: request, Disposition: disposition, GatewayOperationID: state.OperationID,
		ProtectedStateDigest: digest, ObservedAt: time.Now().UTC(),
	}, nil
}

func gatewayV2LANDisableReceipt(observation GatewayV2LANDisableObservation) GatewayV2LANDisableReceipt {
	return GatewayV2LANDisableReceipt{
		Request: observation.Request, GatewayOperationID: observation.GatewayOperationID,
		ProtectedStateDigest: observation.ProtectedStateDigest, ObservedAt: observation.ObservedAt,
	}
}

func (m *Manager) gatewayV2LANDisableDriver() gatewayV2LANGrantDriver {
	if m.gatewayV2LANGrantDriver != nil {
		return m.gatewayV2LANGrantDriver
	}
	return managerGatewayV2LANGrantDriver{manager: m}
}

func acquireGatewayV2LANDisableAuthorization(ctx context.Context, request GatewayV2LANDisableRequest,
	authorize GatewayV2LANDisableAuthorizer,
) (GatewayV2LANDisableAuthorizationLease, error) {
	if authorize == nil {
		return nil, gatewayV2LANDisableError(ctx)
	}
	lease, err := authorize(ctx, request)
	if err != nil || lease == nil {
		return nil, gatewayV2LANDisableError(ctx)
	}
	if err := lease.Revalidate(ctx, request); err != nil {
		_ = lease.Release()
		return nil, gatewayV2LANDisableError(ctx)
	}
	return lease, nil
}

// DisableGatewayV2LAN starts a fail-closed disable. It advances the durable
// claim before mutation, retains a distinct protected pending record, and
// proves the selected port is 404 while other grants remain exact. Terminal
// DB resolution is intentionally separate and must use the locked method below.
func (m *Manager) DisableGatewayV2LAN(ctx context.Context, request GatewayV2LANDisableRequest,
	authorize GatewayV2LANDisableAuthorizer,
) (result GatewayV2LANDisableResult, resultErr error) {
	if m == nil || ctx == nil || !validGatewayV2LANDisableRequest(request) || authorize == nil {
		return GatewayV2LANDisableResult{}, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return GatewayV2LANDisableResult{}, err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return GatewayV2LANDisableResult{}, &Error{Code: DiagnosticCancelled}
	}
	workCtx, cancelWork := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelWork()
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || state.Pending != nil ||
		!gatewayV2LANDisableStateMatches(state, request) {
		return GatewayV2LANDisableResult{}, gatewayV2LANDisableError(workCtx)
	}
	lease, err := acquireGatewayV2LANDisableAuthorization(workCtx, request, authorize)
	if err != nil {
		return GatewayV2LANDisableResult{}, err
	}
	defer func() {
		if err := lease.Release(); err != nil {
			result = GatewayV2LANDisableResult{}
			resultErr = gatewayV2LANDisableError(ctx)
		}
	}()
	driver := m.gatewayV2LANDisableDriver()
	if driver.selectedInterfacePreflight(state.Profile) != nil || !driver.proveCommitted(workCtx, state, journal) ||
		!driver.proveAllGranted(workCtx, state, journal) {
		return GatewayV2LANDisableResult{}, emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if err := lease.Withdraw(workCtx, request); err != nil {
		return GatewayV2LANDisableResult{}, emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	app, exists := state.Apps[request.AppID]
	if !exists || app.LAN == nil {
		if !proveGatewayV2LANDisabled(workCtx, driver, state, journal, request) {
			return GatewayV2LANDisableResult{}, emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		observation, err := gatewayV2LANDisableObservation(state, request, GatewayV2LANDisableDisabled)
		return GatewayV2LANDisableResult{Receipt: gatewayV2LANDisableReceipt(observation)}, err
	}
	pending, withdrawn, err := gatewayV2LANDisablePendingState(state, request)
	if err != nil || store.saveCommittedV2State(pending, journal) != nil {
		return GatewayV2LANDisableResult{}, emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if driver.apply(workCtx, withdrawn, "lan-disable.json") != nil ||
		!proveGatewayV2LANDisabled(workCtx, driver, withdrawn, journal, request) {
		return GatewayV2LANDisableResult{}, emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, pending) || !reflect.DeepEqual(confirmedJournal, journal) {
		return GatewayV2LANDisableResult{}, emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	observation, err := gatewayV2LANDisableObservation(pending, request, GatewayV2LANDisableWithdrawnPending)
	return GatewayV2LANDisableResult{Receipt: gatewayV2LANDisableReceipt(observation)}, err
}

func emergencyGatewayV2LANDisableStop(ctx context.Context, m *Manager, journal gatewayMigrationJournal,
	driver gatewayV2LANGrantDriver,
) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if !stopJournalOwnedGatewayV2LANLocked(stopCtx, m, journal, driver) {
		return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	return gatewayV2LANDisableError(ctx)
}

// ObserveGatewayV2LANDisable reports only a freshly proved, exact protected
// relationship. It does not imply that the matching database claim resolved.
func (m *Manager) ObserveGatewayV2LANDisable(ctx context.Context, request GatewayV2LANDisableRequest) (
	observation GatewayV2LANDisableObservation, resultErr error,
) {
	if m == nil || ctx == nil || !validGatewayV2LANDisableRequest(request) {
		return GatewayV2LANDisableObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return GatewayV2LANDisableObservation{}, err
	}
	defer releaseGatewayLock(release, &resultErr)
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()
	_, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || !gatewayV2LANDisableStateMatches(state, request) {
		return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
	}
	driver := m.gatewayV2LANDisableDriver()
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
	}
	if state.Pending != nil {
		original, withdrawn, err := gatewayV2LANDisablePendingStates(state, request)
		if err != nil {
			return GatewayV2LANDisableObservation{}, err
		}
		switch driver.observePending(proofCtx, original, withdrawn, journal) {
		case gatewayV2PendingCommittedExact:
			if !driver.proveAllGranted(proofCtx, original, journal) {
				return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
			}
			return gatewayV2LANDisableObservation(state, request, GatewayV2LANDisablePendingPublished)
		case gatewayV2PendingProposedExact:
			if !proveGatewayV2LANDisabled(proofCtx, driver, withdrawn, journal, request) {
				return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
			}
			return gatewayV2LANDisableObservation(state, request, GatewayV2LANDisableWithdrawnPending)
		default:
			return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
		}
	}
	app, exists := state.Apps[request.AppID]
	if exists && app.LAN != nil {
		if request.SourceGrant == nil || !driver.proveGranted(proofCtx, state, journal, *request.SourceGrant) {
			return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
		}
		return gatewayV2LANDisableObservation(state, request, GatewayV2LANDisablePendingPublished)
	}
	if !proveGatewayV2LANDisabled(proofCtx, driver, state, journal, request) {
		return GatewayV2LANDisableObservation{}, gatewayV2LANDisableError(proofCtx)
	}
	return gatewayV2LANDisableObservation(state, request, GatewayV2LANDisableDisabled)
}

// WithGatewayV2LANDisableResolution is the only terminal ingress boundary.
// It can resume a prepared or withdrawing disable after a crash. The callback
// must resolve and read back the exact database claim while the gateway lock
// remains held. A failed callback retains the protected marker and 404 route.
func (m *Manager) WithGatewayV2LANDisableResolution(ctx context.Context, request GatewayV2LANDisableRequest,
	authorize GatewayV2LANDisableResolutionAuthorizer,
	resolve func(context.Context, GatewayV2LANDisableObservation) error,
) (resultErr error) {
	if m == nil || ctx == nil || !validGatewayV2LANDisableRequest(request) || authorize == nil || resolve == nil {
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
	if err != nil || !committed || store == nil || !gatewayV2LANDisableStateMatches(state, request) {
		return gatewayV2LANDisableError(workCtx)
	}
	driver := m.gatewayV2LANDisableDriver()
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if err := authorize(workCtx, request); err != nil {
		return err
	}
	if state.Pending == nil {
		app, exists := state.Apps[request.AppID]
		if !exists || app.LAN == nil {
			if !proveGatewayV2LANDisabled(workCtx, driver, state, journal, request) {
				return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
			}
			observation, err := gatewayV2LANDisableObservation(state, request, GatewayV2LANDisableDisabled)
			if err != nil {
				return err
			}
			if err := resolve(workCtx, observation); err != nil {
				return err
			}
			if !proveGatewayV2LANDisabled(workCtx, driver, state, journal, request) {
				return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
			}
			confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
				return gatewayV2LANDisableError(workCtx)
			}
			return nil
		}
		// The DB may have advanced before the process could persist the
		// write-ahead marker. Recreate it under the same exact gateway lock.
		pending, _, err := gatewayV2LANDisablePendingState(state, request)
		if err != nil {
			return gatewayV2LANDisableError(workCtx)
		}
		if err := authorize(workCtx, request); err != nil {
			return err
		}
		if store.saveCommittedV2State(pending, journal) != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
		state = pending
	}
	original, withdrawn, err := gatewayV2LANDisablePendingStates(state, request)
	if err != nil {
		return gatewayV2LANDisableError(workCtx)
	}
	switch driver.observePending(workCtx, original, withdrawn, journal) {
	case gatewayV2PendingCommittedExact, gatewayV2PendingReloadOnlyMixed:
		if err := authorize(workCtx, request); err != nil {
			return err
		}
		if driver.apply(workCtx, withdrawn, "lan-disable-recovery.json") != nil {
			return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
		}
	case gatewayV2PendingProposedExact:
		// Already withdrawn; live and restart topology were proved exact.
	default:
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	if !proveGatewayV2LANDisabled(workCtx, driver, withdrawn, journal, request) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	observation, err := gatewayV2LANDisableObservation(state, request, GatewayV2LANDisableWithdrawnPending)
	if err != nil {
		return err
	}
	if err := resolve(workCtx, observation); err != nil {
		return err
	}
	if err := store.saveCommittedV2State(withdrawn, journal); err != nil {
		_ = restoreGatewayV2LANPending(store, state, withdrawn, journal)
		return gatewayV2LANDisableError(workCtx)
	}
	if !proveGatewayV2LANDisabled(workCtx, driver, withdrawn, journal, request) {
		_ = restoreGatewayV2LANPending(store, state, withdrawn, journal)
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	cleared, clearedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(cleared, withdrawn) || !reflect.DeepEqual(clearedJournal, journal) {
		_ = restoreGatewayV2LANPending(store, state, withdrawn, journal)
		return emergencyGatewayV2LANDisableStop(ctx, m, journal, driver)
	}
	return nil
}
