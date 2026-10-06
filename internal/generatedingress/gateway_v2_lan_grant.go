package generatedingress

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

// GatewayV2LANGrantRequest is the complete immutable identity of one approved
// application access revision. The trusted controller authorizer must compare
// every field with its durable database claim.
type GatewayV2LANGrantRequest struct {
	AttemptID                    string
	ClaimRequestDigest           string
	AppID                        string
	AllocationID                 string
	OwnerOperationID             string
	Port                         uint16
	AccessRevisionID             string
	AccessRevisionNumber         int64
	AccessSpecDigest             string
	ApprovedBy                   string
	GatewayProfileRevisionID     string
	GatewayProfileRevisionNumber int64
	GatewayProfileSpecDigest     string
}

// GatewayV2LANGrantResult is returned only after the exact durable access
// allocation is active and the exact protected gateway publication commits.
// The controller must still use WithGatewayV2LANCommitResolution before it
// treats the cross-store claim as terminal or exposes an address.
type GatewayV2LANGrantResult struct {
	Receipt GatewayV2LANGrantReceipt
}

// GatewayV2LANGrantReceipt proves that gateway state committed after the
// authorization lease activated the exact SQLite allocation. It carries no
// URL and is not a terminal cross-store proof by itself.
type GatewayV2LANGrantReceipt struct {
	Request              GatewayV2LANGrantRequest
	GatewayOperationID   string
	ProtectedStateDigest string
	ObservedAt           time.Time
}

// GatewayV2LANGrantAuthorizationLease is supplied by the authenticated
// controller. Acquisition must create a durable logical lease in a short
// database transaction. Revalidate and Activate must each use their own short
// transaction; an implementation must never retain a SQLite write transaction
// or lock across Docker or Caddy work. Activate atomically advances only the
// exact durable claim. Release ends local use of the lease and must not erase
// or terminally resolve its durable claim.
type GatewayV2LANGrantAuthorizationLease interface {
	Revalidate(context.Context, GatewayV2LANGrantRequest) error
	Activate(context.Context, GatewayV2LANGrantRequest) error
	Release() error
}

// GatewayV2LANGrantAuthorizer acquires an exact-owner/current-head durable
// logical lease while the gateway lock is held. Any error or nil lease denies
// the transaction.
type GatewayV2LANGrantAuthorizer func(context.Context, GatewayV2LANGrantRequest) (GatewayV2LANGrantAuthorizationLease, error)

// GatewayV2LANGrantDisposition describes an exact, live-attested relationship
// between one protected grant binding and its Caddy publication.
type GatewayV2LANGrantDisposition string

const (
	GatewayV2LANGrantCommitted                      GatewayV2LANGrantDisposition = "committed"
	GatewayV2LANGrantPendingPublished               GatewayV2LANGrantDisposition = "pending_published"
	GatewayV2LANGrantPendingReloadOnly              GatewayV2LANGrantDisposition = "pending_reload_only"
	GatewayV2LANGrantWithdrawnPendingReconciliation GatewayV2LANGrantDisposition = "withdrawn_pending_reconciliation"
)

// GatewayV2LANGrantObservation contains no URL. A controller may expose an
// address only after it separately reconciles the matching database claim.
type GatewayV2LANGrantObservation struct {
	Request              GatewayV2LANGrantRequest
	Disposition          GatewayV2LANGrantDisposition
	ActivationUncertain  bool
	Slot                 generatedruntime.Slot
	Endpoints            []generatedruntime.RouteEndpoint
	GatewayOperationID   string
	ProtectedStateDigest string
	EffectiveBinding     GatewayV2LANEffectiveBindingProof
	ObservedAt           time.Time
}

// GatewayV2LANGrantRecovery is the result of a locked recovery attempt. A
// withdrawn pending grant remains protected until controller reconciliation.
type GatewayV2LANGrantRecovery struct {
	Observation GatewayV2LANGrantObservation
}

// Private aliases keep the state-machine implementation compact while its
// controller-facing contract remains explicit and exported.
type gatewayV2LANGrantRequest = GatewayV2LANGrantRequest
type gatewayV2LANGrantResult = GatewayV2LANGrantResult
type gatewayV2LANGrantReceipt = GatewayV2LANGrantReceipt
type gatewayV2LANGrantAuthorizationLease = GatewayV2LANGrantAuthorizationLease
type gatewayV2LANGrantAuthorizer = GatewayV2LANGrantAuthorizer

type gatewayV2LANGrantDriver interface {
	proveCommitted(context.Context, gatewayV2RouteState, gatewayMigrationJournal) bool
	selectedInterfacePreflight(gatewayProfileBinding) error
	preflightCandidate(context.Context, gatewayV2RouteState, gatewayMigrationJournal, string, gatewayV2AppRoute) error
	apply(context.Context, gatewayV2RouteState, string) error
	proveGranted(context.Context, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2LANGrantRequest) bool
	proveAllGranted(context.Context, gatewayV2RouteState, gatewayMigrationJournal) bool
	proveRolledBack(context.Context, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2LANGrantRequest) bool
	observePending(context.Context, gatewayV2RouteState, gatewayV2RouteState, gatewayMigrationJournal) gatewayV2PendingLiveTopology
	stopOwnedGateway(context.Context, gatewayMigrationJournal) error
}

// GrantGatewayV2LAN publishes one exact approved LAN binding. The authorizer
// owns all database transitions; this method owns only protected ingress state
// and Caddy mutation.
func (m *Manager) GrantGatewayV2LAN(ctx context.Context, request GatewayV2LANGrantRequest,
	authorize GatewayV2LANGrantAuthorizer,
) (GatewayV2LANGrantResult, error) {
	return m.grantGatewayV2LAN(ctx, request, authorize)
}

func (m *Manager) grantGatewayV2LAN(ctx context.Context, request gatewayV2LANGrantRequest,
	authorize gatewayV2LANGrantAuthorizer,
) (result gatewayV2LANGrantResult, resultErr error) {
	binding, err := gatewayV2LANBindingForRequest(request)
	if m == nil || ctx == nil || err != nil || authorize == nil {
		return gatewayV2LANGrantResult{}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGateway(ctx)
	if err != nil {
		return gatewayV2LANGrantResult{}, err
	}
	defer func() {
		if err := release(); err != nil {
			result = gatewayV2LANGrantResult{}
			resultErr = gatewayV2LANGrantError(ctx)
		}
	}()

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || state.Pending != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if state.Profile.RevisionID != request.GatewayProfileRevisionID ||
		state.Profile.RevisionNumber != request.GatewayProfileRevisionNumber ||
		state.Profile.SpecDigest != request.GatewayProfileSpecDigest {
		return gatewayV2LANGrantResult{}, &Error{Code: DiagnosticRouteInvalid}
	}
	current, exists := state.Apps[request.AppID]
	if !exists {
		return gatewayV2LANGrantResult{}, &Error{Code: DiagnosticRouteInvalid}
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}

	if current.LAN != nil && !reflect.DeepEqual(*current.LAN, binding) {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if current.LAN == nil && !driver.proveCommitted(ctx, state, journal) {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	lease, err := acquireGatewayV2LANGrantAuthorization(ctx, request, authorize)
	if err != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			result = gatewayV2LANGrantResult{}
			resultErr = gatewayV2LANGrantError(ctx)
		}
	}()

	if current.LAN != nil {
		if !driver.proveGranted(ctx, state, journal, request) || driver.selectedInterfacePreflight(state.Profile) != nil ||
			lease.Activate(ctx, request) != nil {
			if !failClosedGatewayV2LANCommittedLocked(ctx, m, store, state, journal, request, driver) {
				return gatewayV2LANGrantResult{}, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
			}
			return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
		}
		return newGatewayV2LANGrantResult(state, request)
	}

	proposedApp := cloneGatewayV2AppRoute(current)
	proposedApp.LAN = &binding
	pendingState := cloneGatewayV2RouteState(state)
	previous := cloneGatewayV2AppRoute(current)
	pendingState.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: request.AppID,
		Previous: &previous, Proposed: cloneGatewayV2AppRoute(proposedApp),
	}
	if !validGatewayV2RouteState(pendingState) || store.saveCommittedV2State(pendingState, journal) != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	// Revalidation is a short transaction at the Docker mutation boundary. The
	// durable applying claim fences disable/head/owner changes in SQLite; no
	// database transaction remains open while Caddy is reloaded. Activate uses
	// a second short transaction and rejects stale approval or owner state.
	if lease.Revalidate(ctx, request) != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if driver.preflightCandidate(ctx, state, journal, request.AppID, proposedApp) != nil {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}

	proposedState := cloneGatewayV2RouteState(state)
	proposedState.Apps[request.AppID] = proposedApp
	if err := driver.apply(ctx, proposedState, "lan-grant.json"); err != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, m, store, driver, state, pendingState, journal, request, err)
	}
	if !driver.proveGranted(ctx, proposedState, journal, request) {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, m, store, driver, state, pendingState, journal, request, gatewayV2LANGrantError(ctx))
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, m, store, driver, state, pendingState, journal, request, gatewayV2LANGrantError(ctx))
	}
	// Arm an uncertainty marker before the cross-store activation side effect.
	// A crash or ambiguous Activate error can then be made network-fail-closed
	// without allowing generic recovery to discard the unresolved DB outcome.
	activationPending := cloneGatewayV2RouteState(pendingState)
	activationPending.Pending.ActivationUncertain = true
	if store.saveCommittedV2State(activationPending, journal) != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, m, store, driver, state, pendingState, journal, request, gatewayV2LANGrantError(ctx))
	}
	if lease.Activate(ctx, request) != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrantRetainingPending(ctx, m, store, driver, state,
			activationPending, journal, request)
	}
	if err := store.saveCommittedV2State(proposedState, journal); err != nil {
		// SQLite is active and Caddy may already serve. Resolve either exact
		// protected state to 404 without using the cancelled request context.
		if !failClosedGatewayV2LANAfterActivationLocked(ctx, m, store, journal, request, driver) {
			return gatewayV2LANGrantResult{}, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	// The receipt timestamp must describe a proof made after both durable
	// stores committed. A failed final proof withholds the receipt and either
	// withdraws the route to a proven 404 or stops the exact owned gateway.
	if !driver.proveGranted(ctx, proposedState, journal, request) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		if !failClosedGatewayV2LANCommittedLocked(ctx, m, store, proposedState, journal, request, driver) {
			return gatewayV2LANGrantResult{}, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	return newGatewayV2LANGrantResult(proposedState, request)
}

// ObserveGatewayV2LAN attests one exact app, port, revision, allocation, and
// profile binding. It never returns an address and never mutates gateway state.
func (m *Manager) ObserveGatewayV2LAN(ctx context.Context, request GatewayV2LANGrantRequest) (
	GatewayV2LANGrantObservation, error,
) {
	var observation GatewayV2LANGrantObservation
	err := m.WithGatewayV2LANObservation(ctx, request, func(_ context.Context, value GatewayV2LANGrantObservation) error {
		observation = value
		return nil
	})
	if err != nil {
		return GatewayV2LANGrantObservation{}, err
	}
	return observation, nil
}

// WithGatewayV2LANObservation holds both gateway writer locks through exact
// protected-state and live-Caddy attestation and the caller's read-only
// cross-store check. The callback must not write or call Manager methods.
func (m *Manager) WithGatewayV2LANObservation(ctx context.Context, request GatewayV2LANGrantRequest,
	fn func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	return m.withGatewayV2LANObservation(ctx, request, fn)
}

// WithGatewayV2LANResolution is the observation-shaped compatibility form of
// WithGatewayV2LANCommitResolution. A failed terminal callback quarantines the
// exact binding to a proven 404 and retains protected uncertainty.
func (m *Manager) WithGatewayV2LANResolution(ctx context.Context, request GatewayV2LANGrantRequest,
	fn func(context.Context, GatewayV2LANGrantObservation) error,
) error {
	if fn == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	return m.withGatewayV2LANCommitResolution(ctx, request,
		func(callbackCtx context.Context, observation GatewayV2LANGrantObservation) error {
			return fn(callbackCtx, observation)
		})
}

// WithGatewayV2LANCommitResolution closes the cross-store commit window while
// holding the gateway lock. It freshly proves the exact binding and every
// other committed publication, then invokes one short terminal DB transaction
// through resolve. If resolve fails or is ambiguous, the binding is withdrawn
// to a proven 404 after first writing an exact protected uncertainty marker.
// The callback must not call Manager methods or retain a database lock.
func (m *Manager) WithGatewayV2LANCommitResolution(ctx context.Context, request GatewayV2LANGrantRequest,
	resolve func(context.Context, GatewayV2LANGrantReceipt) error,
) error {
	if resolve == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	return m.withGatewayV2LANCommitResolution(ctx, request,
		func(callbackCtx context.Context, observation GatewayV2LANGrantObservation) error {
			return resolve(callbackCtx, GatewayV2LANGrantReceipt{
				Request: observation.Request, GatewayOperationID: observation.GatewayOperationID,
				ProtectedStateDigest: observation.ProtectedStateDigest, ObservedAt: observation.ObservedAt,
			})
		})
}

func (m *Manager) withGatewayV2LANCommitResolution(ctx context.Context, request GatewayV2LANGrantRequest,
	resolve func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil || resolve == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil {
		if !stopLatestOwnedGatewayV2LANLocked(ctx, m, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantError(proofCtx)
	}
	if state.Pending != nil {
		if !failClosedGatewayV2LANAfterActivationLocked(ctx, m, store, journal, request, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantError(proofCtx)
	}
	observation, err := observeGatewayV2LANLocked(proofCtx, state, journal, request, driver)
	if err != nil || observation.Disposition != GatewayV2LANGrantCommitted ||
		!driver.proveAllGranted(proofCtx, state, journal) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		if !failClosedGatewayV2LANCommittedLocked(proofCtx, m, store, state, journal, request, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantError(proofCtx)
	}
	if err := resolve(proofCtx, observation); err != nil {
		if !failClosedGatewayV2LANCommittedLocked(ctx, m, store, state, journal, request, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return err
	}
	confirmed, confirmedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if proofCtx.Err() != nil || loadErr != nil || !reflect.DeepEqual(confirmed, state) ||
		!reflect.DeepEqual(confirmedJournal, journal) ||
		!driver.proveGranted(proofCtx, state, journal, request) ||
		!driver.proveAllGranted(proofCtx, state, journal) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		if !failClosedGatewayV2LANCommittedLocked(ctx, m, store, state, journal, request, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantError(proofCtx)
	}
	return nil
}

// A failed final proof may occur after Caddy is already serving. Withdraw the
// exact binding while the gateway lock is held. If selected-interface drift or
// another failure prevents a proven 404, stop only the journal-owned gateway.
// This interrupts local routes too, but never leaves a failed grant serving
// merely because the controller remains alive.
func failClosedGatewayV2LANCommittedLocked(ctx context.Context, m *Manager,
	store *gatewayUpgradeStateStore, state gatewayV2RouteState, journal gatewayMigrationJournal,
	request gatewayV2LANGrantRequest, driver gatewayV2LANGrantDriver,
) bool {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if quarantineGatewayV2LANCommittedLocked(recoveryCtx, store, state, journal, request, driver) == nil {
		return true
	}
	return stopJournalOwnedGatewayV2LANLocked(recoveryCtx, m, journal, driver)
}

func failClosedGatewayV2LANAfterActivationLocked(ctx context.Context, m *Manager,
	store *gatewayUpgradeStateStore, journal gatewayMigrationJournal,
	request gatewayV2LANGrantRequest, driver gatewayV2LANGrantDriver,
) bool {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	state, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err == nil && reflect.DeepEqual(retainedJournal, journal) {
		if state.Pending != nil {
			if _, _, err := withdrawGatewayV2LANPendingLocked(recoveryCtx, store, state, journal, request, driver); err == nil {
				return true
			}
		} else if quarantineGatewayV2LANCommittedLocked(recoveryCtx, store, state, journal, request, driver) == nil {
			return true
		}
	}
	return stopJournalOwnedGatewayV2LANLocked(recoveryCtx, m, journal, driver)
}

func stopJournalOwnedGatewayV2LANLocked(ctx context.Context, m *Manager,
	journal gatewayMigrationJournal, driver gatewayV2LANGrantDriver,
) bool {
	latestJournal, err := m.latestGatewayV2JournalForEmergencyStop()
	if err == nil && reflect.DeepEqual(latestJournal, journal) {
		return driver.stopOwnedGateway(ctx, latestJournal) == nil
	}
	return false
}

func stopLatestOwnedGatewayV2LANLocked(ctx context.Context, m *Manager, driver gatewayV2LANGrantDriver) bool {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	journal, err := m.latestGatewayV2JournalForEmergencyStop()
	return err == nil && driver.stopOwnedGateway(recoveryCtx, journal) == nil
}

// WithGatewayV2LANAbsenceResolution proves that a prepared attempt has no
// protected binding or pending record and that its reserved port is an exact
// 404 in the committed gateway topology. The app route itself may be absent.
// The callback may perform one short prepared-to-rolled-back DB transaction.
func (m *Manager) WithGatewayV2LANAbsenceResolution(ctx context.Context, request GatewayV2LANGrantRequest,
	fn func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil || fn == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || state.Pending != nil ||
		state.Profile.RevisionID != request.GatewayProfileRevisionID ||
		state.Profile.RevisionNumber != request.GatewayProfileRevisionNumber ||
		state.Profile.SpecDigest != request.GatewayProfileSpecDigest {
		return gatewayV2LANGrantError(proofCtx)
	}
	for _, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		binding := app.LAN
		if binding.GrantAttemptID == request.AttemptID || binding.GrantRequestDigest == request.ClaimRequestDigest ||
			binding.AllocationID == request.AllocationID || binding.AccessRevisionID == request.AccessRevisionID ||
			binding.Port == request.Port {
			return gatewayV2LANGrantError(proofCtx)
		}
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil || !driver.proveCommitted(proofCtx, state, journal) ||
		!driver.proveRolledBack(proofCtx, state, journal, request) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantError(proofCtx)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return gatewayV2LANGrantError(proofCtx)
	}
	digest, err := canonicalDigest(state)
	if err != nil || !validSHA256(digest) {
		return gatewayV2LANGrantError(proofCtx)
	}
	observation := GatewayV2LANGrantObservation{
		Request: request, Disposition: GatewayV2LANGrantWithdrawnPendingReconciliation,
		GatewayOperationID: state.OperationID, ProtectedStateDigest: digest, ObservedAt: time.Now().UTC(),
	}
	if err := fn(proofCtx, observation); err != nil {
		return err
	}
	if proofCtx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return nil
}

func (m *Manager) withGatewayV2LANObservation(ctx context.Context, request GatewayV2LANGrantRequest,
	fn func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil || fn == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil {
		return gatewayV2LANGrantError(proofCtx)
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	observation, err := observeGatewayV2LANLocked(proofCtx, state, journal, request, driver)
	if err != nil {
		return err
	}
	selection := gatewayUpgradeGenerationSelection{Store: store, Generation: store.generation,
		State: state, Journal: journal, Existing: true, operationID: state.OperationID}
	observation.EffectiveBinding, err = gatewayUpgradeEffectiveProof(selection)
	if err != nil {
		return gatewayV2LANGrantError(proofCtx)
	}
	if proofCtx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	if err := fn(proofCtx, observation); err != nil {
		return err
	}
	if proofCtx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return nil
}

// WithGatewayV2LANCommitRecovery restores a quarantined binding only after a
// lock-held callback freshly validates that its exact DB claim is terminally
// committed and current. The callback receives the protected 404 observation;
// it is read-only and must not call Manager methods. The binding is then
// reapplied, proved, and its matching uncertainty marker cleared.
func (m *Manager) WithGatewayV2LANCommitRecovery(ctx context.Context, request GatewayV2LANGrantRequest,
	validateCommitted func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil || validateCommitted == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelRecovery()

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || state.Pending == nil ||
		state.Pending.Kind != gatewayV2PendingLANWithdrawal {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	withdrawn, published, pending, err := gatewayV2LANGrantStatesForRequest(state, request)
	if err != nil || !pending || driver.selectedInterfacePreflight(state.Profile) != nil ||
		driver.observePending(recoveryCtx, withdrawn, published, journal) != gatewayV2PendingCommittedExact ||
		!driver.proveRolledBack(recoveryCtx, withdrawn, journal, request) {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	observation, err := newGatewayV2LANGrantObservation(
		state, request, GatewayV2LANGrantWithdrawnPendingReconciliation, true,
	)
	if err != nil {
		return err
	}
	if err := validateCommitted(recoveryCtx, observation); err != nil {
		return err
	}
	if recoveryCtx.Err() != nil || driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	if driver.apply(recoveryCtx, published, "lan-grant-commit-recovery.json") != nil ||
		!driver.proveGranted(recoveryCtx, published, journal, request) ||
		!driver.proveAllGranted(recoveryCtx, published, journal) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	if err := store.saveCommittedV2State(published, journal); err != nil {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	if !driver.proveGranted(recoveryCtx, published, journal, request) ||
		!driver.proveAllGranted(recoveryCtx, published, journal) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	return nil
}

// WithGatewayV2LANRollbackResolution withdraws one exact pending grant and
// proves 404 while retaining its protected pending marker. Only after the
// callback durably resolves and reads back the matching DB claim as rolled
// back does it clear that marker. A failed callback or protected clear leaves,
// or restores, recovery-only state.
func (m *Manager) WithGatewayV2LANRollbackResolution(ctx context.Context, request GatewayV2LANGrantRequest,
	fn func(context.Context, GatewayV2LANGrantObservation) error,
) (resultErr error) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil || fn == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelRecovery()

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	observation, committedState, err := withdrawGatewayV2LANPendingLocked(
		recoveryCtx, store, state, journal, request, driver,
	)
	if err != nil {
		return err
	}
	if err := fn(recoveryCtx, observation); err != nil {
		return err
	}
	if recoveryCtx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	retained, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		return gatewayV2LANGrantError(recoveryCtx)
	}
	if err := store.saveCommittedV2State(committedState, journal); err != nil {
		_ = restoreGatewayV2LANPending(store, state, committedState, journal)
		return gatewayV2LANGrantError(recoveryCtx)
	}
	if !driver.proveRolledBack(recoveryCtx, committedState, journal, request) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		_ = restoreGatewayV2LANPending(store, state, committedState, journal)
		return gatewayV2LANGrantError(recoveryCtx)
	}
	cleared, clearedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(cleared, committedState) || !reflect.DeepEqual(clearedJournal, journal) {
		_ = restoreGatewayV2LANPending(store, state, committedState, journal)
		return gatewayV2LANGrantError(recoveryCtx)
	}
	return nil
}

// RecoverGatewayV2LAN performs a locked, request-bound recovery. A committed
// grant is only reported after exact publication proof. A matching pending
// grant is withdrawn to an exact 404 configuration and its protected pending
// record is retained for controller/database reconciliation.
func (m *Manager) RecoverGatewayV2LAN(ctx context.Context, request GatewayV2LANGrantRequest) (
	recovery GatewayV2LANGrantRecovery, resultErr error,
) {
	if _, err := gatewayV2LANBindingForRequest(request); m == nil || ctx == nil || err != nil {
		return GatewayV2LANGrantRecovery{}, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return GatewayV2LANGrantRecovery{}, err
	}
	defer func() {
		if err := release(); err != nil {
			recovery = GatewayV2LANGrantRecovery{}
			resultErr = gatewayV2LANGrantError(ctx)
		}
	}()
	if ctx.Err() != nil {
		return GatewayV2LANGrantRecovery{}, &Error{Code: DiagnosticCancelled}
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelRecovery()

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil {
		return GatewayV2LANGrantRecovery{}, gatewayV2LANGrantError(recoveryCtx)
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	_, _, pending, err := gatewayV2LANGrantStatesForRequest(state, request)
	if err != nil || driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANGrantRecovery{}, gatewayV2LANGrantError(recoveryCtx)
	}
	if !pending {
		observation, err := observeGatewayV2LANLocked(recoveryCtx, state, journal, request, driver)
		if err != nil {
			return GatewayV2LANGrantRecovery{}, err
		}
		return GatewayV2LANGrantRecovery{Observation: observation}, nil
	}
	observation, _, err := withdrawGatewayV2LANPendingLocked(recoveryCtx, store, state, journal, request, driver)
	if err != nil {
		return GatewayV2LANGrantRecovery{}, err
	}
	return GatewayV2LANGrantRecovery{Observation: observation}, nil
}

func quarantineGatewayV2LANCommittedLocked(ctx context.Context, store *gatewayUpgradeStateStore,
	state gatewayV2RouteState, journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
	driver gatewayV2LANGrantDriver,
) error {
	binding, err := gatewayV2LANBindingForRequest(request)
	app, exists := state.Apps[request.AppID]
	if ctx == nil || store == nil || driver == nil || err != nil || state.Pending != nil || !exists ||
		app.LAN == nil || !reflect.DeepEqual(*app.LAN, binding) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantError(ctx)
	}
	previous := cloneGatewayV2AppRoute(app)
	proposed := cloneGatewayV2AppRoute(app)
	proposed.LAN = nil
	pending := cloneGatewayV2RouteState(state)
	pending.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANWithdrawal, AppID: request.AppID,
		Previous: &previous, Proposed: proposed, ActivationUncertain: true,
	}
	if !validGatewayV2RouteState(pending) || store.saveCommittedV2State(pending, journal) != nil {
		return gatewayV2LANGrantError(ctx)
	}
	withdrawn := cloneGatewayV2RouteState(state)
	withdrawn.Apps[request.AppID] = proposed
	if driver.apply(ctx, withdrawn, "lan-grant-quarantine.json") != nil ||
		!driver.proveRolledBack(ctx, withdrawn, journal, request) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantError(ctx)
	}
	retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(retained, pending) || !reflect.DeepEqual(retainedJournal, journal) {
		return gatewayV2LANGrantError(ctx)
	}
	return nil
}

func withdrawGatewayV2LANPendingLocked(ctx context.Context, store *gatewayUpgradeStateStore,
	state gatewayV2RouteState, journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
	driver gatewayV2LANGrantDriver,
) (GatewayV2LANGrantObservation, gatewayV2RouteState, error) {
	committed, proposed, pending, err := gatewayV2LANGrantStatesForRequest(state, request)
	if ctx == nil || store == nil || driver == nil || err != nil || !pending ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANGrantObservation{}, gatewayV2RouteState{}, gatewayV2LANGrantError(ctx)
	}
	switch driver.observePending(ctx, committed, proposed, journal) {
	case gatewayV2PendingCommittedExact:
	case gatewayV2PendingProposedExact, gatewayV2PendingReloadOnlyMixed:
		if driver.apply(ctx, committed, "lan-grant-recovery.json") != nil {
			return GatewayV2LANGrantObservation{}, gatewayV2RouteState{}, gatewayV2LANGrantError(ctx)
		}
	default:
		return GatewayV2LANGrantObservation{}, gatewayV2RouteState{}, gatewayV2LANGrantError(ctx)
	}
	if !driver.proveRolledBack(ctx, committed, journal, request) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANGrantObservation{}, gatewayV2RouteState{}, gatewayV2LANGrantError(ctx)
	}
	retained, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		return GatewayV2LANGrantObservation{}, gatewayV2RouteState{}, gatewayV2LANGrantError(ctx)
	}
	observation, err := newGatewayV2LANGrantObservation(
		state, request, GatewayV2LANGrantWithdrawnPendingReconciliation, state.Pending.ActivationUncertain,
	)
	if err != nil {
		return GatewayV2LANGrantObservation{}, gatewayV2RouteState{}, err
	}
	return observation, committed, nil
}

func restoreGatewayV2LANPending(store *gatewayUpgradeStateStore, pending, committed gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	current, currentJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(currentJournal, journal) {
		return gatewayV2LANGrantError(nil)
	}
	if reflect.DeepEqual(current, pending) {
		return nil
	}
	if !reflect.DeepEqual(current, committed) || store.saveCommittedV2State(pending, journal) != nil {
		return gatewayV2LANGrantError(nil)
	}
	restored, restoredJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(restored, pending) || !reflect.DeepEqual(restoredJournal, journal) {
		return gatewayV2LANGrantError(nil)
	}
	return nil
}

func observeGatewayV2LANLocked(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal,
	request gatewayV2LANGrantRequest, driver gatewayV2LANGrantDriver,
) (GatewayV2LANGrantObservation, error) {
	committed, proposed, pending, err := gatewayV2LANGrantStatesForRequest(state, request)
	if ctx == nil || driver == nil || err != nil || driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANGrantObservation{}, gatewayV2LANGrantError(ctx)
	}
	if !pending {
		if !driver.proveGranted(ctx, state, journal, request) {
			return GatewayV2LANGrantObservation{}, gatewayV2LANGrantError(ctx)
		}
		return newGatewayV2LANGrantObservation(state, request, GatewayV2LANGrantCommitted, false)
	}
	disposition := GatewayV2LANGrantDisposition("")
	switch driver.observePending(ctx, committed, proposed, journal) {
	case gatewayV2PendingCommittedExact:
		disposition = GatewayV2LANGrantWithdrawnPendingReconciliation
	case gatewayV2PendingProposedExact:
		disposition = GatewayV2LANGrantPendingPublished
	case gatewayV2PendingReloadOnlyMixed:
		disposition = GatewayV2LANGrantPendingReloadOnly
	default:
		return GatewayV2LANGrantObservation{}, gatewayV2LANGrantError(ctx)
	}
	return newGatewayV2LANGrantObservation(state, request, disposition, state.Pending.ActivationUncertain)
}

func gatewayV2LANGrantStatesForRequest(state gatewayV2RouteState, request gatewayV2LANGrantRequest) (
	committed, proposed gatewayV2RouteState, pending bool, err error,
) {
	binding, bindingErr := gatewayV2LANBindingForRequest(request)
	if bindingErr != nil || !validGatewayV2RouteState(state) || state.Profile.RevisionID != request.GatewayProfileRevisionID ||
		state.Profile.RevisionNumber != request.GatewayProfileRevisionNumber ||
		state.Profile.SpecDigest != request.GatewayProfileSpecDigest {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
	}
	app, exists := state.Apps[request.AppID]
	if !exists {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
	}
	if state.Pending == nil {
		if app.LAN == nil || !reflect.DeepEqual(*app.LAN, binding) {
			return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
		}
		return cloneGatewayV2RouteState(state), gatewayV2RouteState{}, false, nil
	}
	pendingRoute := state.Pending
	if pendingRoute.AppID != request.AppID || pendingRoute.Previous == nil {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
	}
	switch pendingRoute.Kind {
	case gatewayV2PendingLANGrant:
		if pendingRoute.Previous.LAN != nil || pendingRoute.Proposed.LAN == nil ||
			!reflect.DeepEqual(*pendingRoute.Proposed.LAN, binding) {
			return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
		}
		committed = cloneGatewayV2RouteState(state)
		committed.Pending = nil
		proposed = cloneGatewayV2RouteState(committed)
		proposed.Apps[request.AppID] = cloneGatewayV2AppRoute(pendingRoute.Proposed)
	case gatewayV2PendingLANWithdrawal:
		if pendingRoute.Previous.LAN == nil || !reflect.DeepEqual(*pendingRoute.Previous.LAN, binding) ||
			pendingRoute.Proposed.LAN != nil || !pendingRoute.ActivationUncertain {
			return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
		}
		proposed = cloneGatewayV2RouteState(state)
		proposed.Pending = nil
		committed = cloneGatewayV2RouteState(proposed)
		committed.Apps[request.AppID] = cloneGatewayV2AppRoute(pendingRoute.Proposed)
	default:
		return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
	}
	if !validGatewayV2RouteState(committed) || !validGatewayV2RouteState(proposed) {
		return gatewayV2RouteState{}, gatewayV2RouteState{}, false, gatewayV2LANGrantError(nil)
	}
	return committed, proposed, true, nil
}

func newGatewayV2LANGrantObservation(state gatewayV2RouteState, request gatewayV2LANGrantRequest,
	disposition GatewayV2LANGrantDisposition, activationUncertain bool,
) (GatewayV2LANGrantObservation, error) {
	if disposition != GatewayV2LANGrantCommitted && disposition != GatewayV2LANGrantPendingPublished &&
		disposition != GatewayV2LANGrantPendingReloadOnly &&
		disposition != GatewayV2LANGrantWithdrawnPendingReconciliation {
		return GatewayV2LANGrantObservation{}, gatewayV2LANGrantError(nil)
	}
	digest, err := canonicalDigest(state)
	app, exists := state.Apps[request.AppID]
	if err != nil || !validSHA256(digest) || !exists || validateRoute(app.Route) != nil {
		return GatewayV2LANGrantObservation{}, gatewayV2LANGrantError(nil)
	}
	return GatewayV2LANGrantObservation{
		Request: request, Disposition: disposition, ActivationUncertain: activationUncertain,
		Slot: app.Route.Slot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...),
		GatewayOperationID: state.OperationID, ProtectedStateDigest: digest, ObservedAt: time.Now().UTC(),
	}, nil
}

func gatewayV2LANBindingForRequest(request gatewayV2LANGrantRequest) (gatewayV2LANBinding, error) {
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: request.AppID, AllocationID: request.AllocationID, Port: request.Port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil || digest != request.AccessSpecDigest || !validCanonicalUUID(request.AttemptID) ||
		!validSHA256(request.ClaimRequestDigest) || !validCanonicalUUID(request.OwnerOperationID) ||
		!validCanonicalUUID(request.AccessRevisionID) || !validCanonicalUUID(request.ApprovedBy) ||
		request.AccessRevisionNumber <= 0 || !validSHA256(request.GatewayProfileSpecDigest) {
		return gatewayV2LANBinding{}, &Error{Code: DiagnosticValidationFailed}
	}
	return gatewayV2LANBinding{
		GrantAttemptID: request.AttemptID, GrantRequestDigest: request.ClaimRequestDigest,
		OwnerOperationID: request.OwnerOperationID, ApprovedBy: request.ApprovedBy,
		AccessRevisionID: request.AccessRevisionID, AccessRevisionNumber: request.AccessRevisionNumber,
		AccessSpecDigest: request.AccessSpecDigest, AllocationID: request.AllocationID, Port: request.Port,
		ProfileRevisionID: request.GatewayProfileRevisionID, ProfileRevisionNumber: request.GatewayProfileRevisionNumber,
		ProfileSpecDigest: request.GatewayProfileSpecDigest,
	}, nil
}

func acquireGatewayV2LANGrantAuthorization(ctx context.Context, request gatewayV2LANGrantRequest,
	authorize gatewayV2LANGrantAuthorizer,
) (gatewayV2LANGrantAuthorizationLease, error) {
	if ctx == nil || authorize == nil {
		return nil, gatewayV2LANGrantError(ctx)
	}
	lease, err := authorize(ctx, request)
	if err != nil || lease == nil {
		return nil, gatewayV2LANGrantError(ctx)
	}
	if err := lease.Revalidate(ctx, request); err != nil {
		_ = lease.Release()
		return nil, gatewayV2LANGrantError(ctx)
	}
	return lease, nil
}

func rollbackGatewayV2LANGrant(ctx context.Context, m *Manager, store *gatewayUpgradeStateStore, driver gatewayV2LANGrantDriver,
	committed, pending gatewayV2RouteState, journal gatewayMigrationJournal, request gatewayV2LANGrantRequest, original error,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if pending.Pending == nil || pending.Pending.Kind != gatewayV2PendingLANGrant ||
		driver.apply(rollbackCtx, committed, "lan-grant-rollback.json") != nil ||
		!driver.proveRolledBack(rollbackCtx, committed, journal, request) ||
		store.saveCommittedV2State(pending, journal) != nil {
		if !stopJournalOwnedGatewayV2LANLocked(rollbackCtx, m, journal, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantError(ctx)
	}
	return original
}

func rollbackGatewayV2LANGrantRetainingPending(ctx context.Context, m *Manager, store *gatewayUpgradeStateStore,
	driver gatewayV2LANGrantDriver, committed, pending gatewayV2RouteState, journal gatewayMigrationJournal,
	request gatewayV2LANGrantRequest,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if pending.Pending == nil || pending.Pending.Kind != gatewayV2PendingLANGrant ||
		!pending.Pending.ActivationUncertain || driver.apply(rollbackCtx, committed, "lan-grant-rollback.json") != nil ||
		!driver.proveRolledBack(rollbackCtx, committed, journal, request) ||
		store.saveCommittedV2State(pending, journal) != nil {
		if !stopJournalOwnedGatewayV2LANLocked(rollbackCtx, m, journal, driver) {
			return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return gatewayV2LANGrantError(ctx)
	}
	return gatewayV2LANGrantError(ctx)
}

func newGatewayV2LANGrantResult(state gatewayV2RouteState, request gatewayV2LANGrantRequest) (gatewayV2LANGrantResult, error) {
	binding, err := gatewayV2LANBindingForRequest(request)
	app, exists := state.Apps[request.AppID]
	if err != nil || !validGatewayV2RouteState(state) || state.Pending != nil || !exists || app.LAN == nil ||
		!reflect.DeepEqual(*app.LAN, binding) {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(nil)
	}
	digest, err := canonicalDigest(state)
	if err != nil || !validSHA256(digest) {
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(nil)
	}
	return gatewayV2LANGrantResult{Receipt: gatewayV2LANGrantReceipt{
		Request: request, GatewayOperationID: state.OperationID, ProtectedStateDigest: digest, ObservedAt: time.Now().UTC(),
	}}, nil
}

func gatewayV2LANGrantError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticRouteUnresolved}
}

type managerGatewayV2LANGrantDriver struct{ manager *Manager }

func (d managerGatewayV2LANGrantDriver) proveCommitted(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	return d.manager != nil && d.manager.attestCommittedV2Locked(ctx, state, journal) == nil
}

func (managerGatewayV2LANGrantDriver) selectedInterfacePreflight(profile gatewayProfileBinding) error {
	return gatewayV2SelectedInterfacePreflight(profile)
}

func (d managerGatewayV2LANGrantDriver) preflightCandidate(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, appID string, proposed gatewayV2AppRoute,
) error {
	if d.manager == nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return d.manager.preflightCommittedV2Candidate(ctx, state, journal, appID, proposed)
}

func (d managerGatewayV2LANGrantDriver) apply(ctx context.Context, state gatewayV2RouteState, filename string) error {
	if d.manager == nil {
		return gatewayV2LANGrantError(ctx)
	}
	_, durableState, journal, committed, err := d.manager.committedV2Locked()
	if err != nil || !committed || durableState.OperationID != state.OperationID ||
		journal.OperationID != state.OperationID || journal.Target.IdentityDigest != state.Identity.Digest ||
		!validSHA256(journal.Resources.FinalContainerID) {
		return gatewayV2LANGrantError(ctx)
	}
	return d.manager.applyCommittedV2Routes(ctx, state, journal.Resources.FinalContainerID, filename)
}

func (d managerGatewayV2LANGrantDriver) proveGranted(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	if d.manager == nil || d.manager.attestCommittedV2Locked(ctx, state, journal) != nil ||
		!validSHA256(journal.Resources.FinalContainerID) {
		return false
	}
	return proveGatewayV2LANGrantPublication(ctx, state, request, journal.Resources.FinalContainerID,
		probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge) &&
		proveGatewayV2LANCommittedPublications(ctx, state, journal.Resources.FinalContainerID,
			probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge)
}

func (d managerGatewayV2LANGrantDriver) proveAllGranted(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) bool {
	if d.manager == nil || d.manager.attestCommittedV2Locked(ctx, state, journal) != nil ||
		!validSHA256(journal.Resources.FinalContainerID) {
		return false
	}
	return proveGatewayV2LANCommittedPublications(ctx, state, journal.Resources.FinalContainerID,
		probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge)
}

func (d managerGatewayV2LANGrantDriver) proveRolledBack(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	if d.manager == nil || d.manager.attestCommittedV2Locked(ctx, state, journal) != nil ||
		!validSHA256(journal.Resources.FinalContainerID) {
		return false
	}
	return proveGatewayV2LANRollbackPublication(ctx, state, request.Port, journal.Resources.FinalContainerID,
		probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge) &&
		proveGatewayV2LANCommittedPublications(ctx, state, journal.Resources.FinalContainerID,
			probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge)
}

func (d managerGatewayV2LANGrantDriver) observePending(ctx context.Context, committed, proposed gatewayV2RouteState,
	journal gatewayMigrationJournal,
) gatewayV2PendingLiveTopology {
	if d.manager == nil {
		return gatewayV2PendingUnknown
	}
	source, err := d.manager.store.load()
	if err != nil {
		return gatewayV2PendingUnknown
	}
	return d.manager.observeCommittedV2PendingTopology(ctx, source, committed, proposed, journal)
}

func (d managerGatewayV2LANGrantDriver) stopOwnedGateway(ctx context.Context, journal gatewayMigrationJournal) error {
	if d.manager == nil || !validGatewayMigrationJournal(journal) || !validSHA256(journal.Resources.FinalContainerID) {
		return gatewayV2LANGrantError(ctx)
	}
	wantLabels := map[string]string{
		gatewayV2ManagedLabelKey:        gatewayV2ManagedContainerLabel,
		gatewayV2IdentityLabelKey:       gatewayV2IdentityVersion,
		gatewayV2OperationLabelKey:      journal.OperationID,
		gatewayV2IdentityDigestLabelKey: journal.Target.IdentityDigest,
		gatewayV2PlanDigestLabelKey:     journal.Target.PlanDigest,
		gatewayV2ResourceRoleLabelKey:   gatewayV2FinalContainerRole,
		gatewayV2ListenerIsolationKey:   gatewayV2ListenerIsolationVersion,
	}
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, gatewayV2ContainerName)
	if err != nil || !found || !validContainerID(container.ID) ||
		normalizeID(container.ID) != journal.Resources.FinalContainerID || container.Restarting ||
		strings.TrimPrefix(container.Name, "/") != gatewayV2ContainerName ||
		!validGatewayV2ContainerLabels(container.Labels, wantLabels) {
		return gatewayV2LANGrantError(ctx)
	}
	if container.Running {
		transfer := managerGatewayV2TransferDriver{manager: d.manager}
		if err := transfer.setContainerRunning(ctx, gatewayV2ContainerName, journal.Resources.FinalContainerID, false); err != nil {
			return gatewayV2LANGrantError(ctx)
		}
	}
	confirmed, confirmedRuntime, confirmedFound, err := d.manager.inspectNamedGatewayContainer(context.WithoutCancel(ctx), gatewayV2ContainerName)
	if err != nil || !confirmedFound || normalizeID(confirmed.ID) != journal.Resources.FinalContainerID ||
		confirmed.Running || confirmed.Restarting || !validGatewayV2ContainerLabels(confirmed.Labels, wantLabels) ||
		gatewayV2HasEffectivePortBinding(confirmedRuntime.EffectivePortBindings) {
		return gatewayV2LANGrantError(ctx)
	}
	return nil
}

func proveGatewayV2LANGrantPublication(ctx context.Context, state gatewayV2RouteState, request gatewayV2LANGrantRequest,
	caddyID string, probe gatewayV2HostStatusProbe, containerProbe gatewayV2ContainerChallengeProbe,
) bool {
	app, exists := state.Apps[request.AppID]
	binding, err := gatewayV2LANBindingForRequest(request)
	challenge, challengeErr := gatewayV2HostChallenge(state)
	if ctx == nil || probe == nil || containerProbe == nil || err != nil || challengeErr != nil || !exists ||
		app.LAN == nil || !reflect.DeepEqual(*app.LAN, binding) || !validContainerID(caddyID) {
		return false
	}
	assignment := caddyV2LANAssignment{
		AppID: request.AppID, AllocationID: request.AllocationID,
		AccessRevisionID: request.AccessRevisionID, AccessRevisionNumber: request.AccessRevisionNumber,
		AccessSpecDigest: request.AccessSpecDigest,
	}
	appChallenge := gatewayV2LANAppChallenge(challenge, request.Port, assignment)
	return exactGatewayV2HostChallenge(ctx, probe, state.Profile.SelectedIPv4, request.Port, state.Profile.SelectedIPv4, appChallenge) &&
		containerProbe(ctx, caddyID, state.Network.ContainerIPv4, request.Port, state.Profile.SelectedIPv4, appChallenge) &&
		exactGatewayV2HostStatus(ctx, probe, state.Profile.SelectedIPv4, request.Port, "wrong.invalid", http.StatusNotFound) &&
		gatewayV2HostResponded(ctx, probe, state.Profile.SelectedIPv4, request.Port, state.Profile.SelectedIPv4) &&
		!gatewayV2LoopbackPublished(ctx, probe, request.Port, state.Profile.SelectedIPv4)
}

func proveGatewayV2LANCommittedPublications(ctx context.Context, state gatewayV2RouteState, caddyID string,
	probe gatewayV2HostStatusProbe, containerProbe gatewayV2ContainerChallengeProbe,
) bool {
	if ctx == nil || probe == nil || containerProbe == nil || !validGatewayV2RouteState(state) || state.Pending != nil {
		return false
	}
	appIDs := make([]string, 0, len(state.Apps))
	for appID, app := range state.Apps {
		if app.LAN != nil {
			appIDs = append(appIDs, appID)
		}
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		request := gatewayV2LANGrantRequestForBinding(appID, *state.Apps[appID].LAN)
		if !proveGatewayV2LANGrantPublication(ctx, state, request, caddyID, probe, containerProbe) {
			return false
		}
	}
	return true
}

func proveGatewayV2LANRollbackPublication(ctx context.Context, state gatewayV2RouteState, port uint16, caddyID string,
	probe gatewayV2HostStatusProbe, containerProbe gatewayV2ContainerChallengeProbe,
) bool {
	challenge, err := gatewayV2HostChallenge(state)
	if ctx == nil || probe == nil || containerProbe == nil || err != nil || !validContainerID(caddyID) ||
		port < state.Profile.PortStart || port > state.Profile.PortEnd {
		return false
	}
	portChallenge := gatewayV2PortChallenge(challenge, port)
	return exactGatewayV2HostChallenge(ctx, probe, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, portChallenge) &&
		containerProbe(ctx, caddyID, state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, portChallenge) &&
		exactGatewayV2HostStatus(ctx, probe, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, http.StatusNotFound) &&
		exactGatewayV2HostStatus(ctx, probe, state.Profile.SelectedIPv4, port, "wrong.invalid", http.StatusNotFound) &&
		!gatewayV2LoopbackPublished(ctx, probe, port, state.Profile.SelectedIPv4)
}

func gatewayV2HostResponded(ctx context.Context, probe gatewayV2HostStatusProbe, address string, port uint16, host string) bool {
	result := probe(ctx, address, port, host, "/")
	return ctx.Err() == nil && result.Connected && result.Responded && result.Status >= 200 && result.Status <= 599
}

var _ gatewayV2LANGrantDriver = managerGatewayV2LANGrantDriver{}
