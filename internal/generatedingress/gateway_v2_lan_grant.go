package generatedingress

import (
	"context"
	"net/http"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// gatewayV2LANGrantRequest is the complete immutable identity of one approved
// application access revision. The trusted controller authorizer must compare
// every field with its current database snapshot while the gateway lock is
// held.
type gatewayV2LANGrantRequest struct {
	AppID                        string
	AllocationID                 string
	Port                         uint16
	AccessRevisionID             string
	AccessRevisionNumber         int64
	AccessSpecDigest             string
	GatewayProfileRevisionID     string
	GatewayProfileRevisionNumber int64
	GatewayProfileSpecDigest     string
}

type gatewayV2LANGrantResult struct {
	Receipt gatewayV2LANGrantReceipt
}

// gatewayV2LANGrantReceipt proves that the gateway state was committed after
// the authorization lease activated the exact SQLite allocation. It carries
// no URL so callers cannot expose an address from an incomplete invocation.
type gatewayV2LANGrantReceipt struct {
	Request              gatewayV2LANGrantRequest
	GatewayOperationID   string
	ProtectedStateDigest string
}

// gatewayV2LANGrantAuthorizationLease is supplied by the authenticated
// controller. Its implementation must retain a SQLite mutation fence from
// acquisition through Activate, so a disable intent or owner change cannot
// commit between revalidation and the Caddy mutation. Activate must atomically
// transition only the exact allocation from reserved to active; Release must
// roll back an uncommitted lease and be safe after successful activation.
type gatewayV2LANGrantAuthorizationLease interface {
	Revalidate(context.Context, gatewayV2LANGrantRequest) error
	Activate(context.Context, gatewayV2LANGrantRequest) error
	Release() error
}

// gatewayV2LANGrantAuthorizer acquires an exact-owner/current-head lease while
// the gateway lock is held. Any error or nil lease denies the transaction.
type gatewayV2LANGrantAuthorizer func(context.Context, gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error)

type gatewayV2LANGrantDriver interface {
	proveCommitted(context.Context, gatewayV2RouteState, gatewayMigrationJournal) bool
	selectedInterfacePreflight(gatewayProfileBinding) error
	preflightCandidate(context.Context, gatewayV2RouteState, gatewayMigrationJournal, string, gatewayV2AppRoute) error
	apply(context.Context, gatewayV2RouteState, string) error
	proveGranted(context.Context, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2LANGrantRequest) bool
	proveRolledBack(context.Context, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2LANGrantRequest) bool
}

// grantGatewayV2LAN is a preparatory internal primitive. It must remain
// inaccessible to production callers until the controller owns a durable DB
// grant claim and startup reconciliation for every cross-store crash window.
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
	// Revalidation occurs while the controller-owned SQLite mutation fence is
	// still held, at the exact Docker mutation boundary.
	if lease.Revalidate(ctx, request) != nil {
		if store.saveCommittedV2State(state, journal) != nil {
			return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
		}
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		if store.saveCommittedV2State(state, journal) != nil {
			return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
		}
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	if driver.preflightCandidate(ctx, state, journal, request.AppID, proposedApp) != nil {
		if store.saveCommittedV2State(state, journal) != nil {
			return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
		}
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}

	proposedState := cloneGatewayV2RouteState(state)
	proposedState.Apps[request.AppID] = proposedApp
	if err := driver.apply(ctx, proposedState, "lan-grant.json"); err != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, store, driver, state, journal, request, err)
	}
	if !driver.proveGranted(ctx, proposedState, journal, request) {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, store, driver, state, journal, request, gatewayV2LANGrantError(ctx))
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, store, driver, state, journal, request, gatewayV2LANGrantError(ctx))
	}
	// Arm an uncertainty marker before the cross-store activation side effect.
	// A crash or ambiguous Activate error can then be made network-fail-closed
	// without allowing generic recovery to discard the unresolved DB outcome.
	activationPending := cloneGatewayV2RouteState(pendingState)
	activationPending.Pending.ActivationUncertain = true
	if store.saveCommittedV2State(activationPending, journal) != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrant(ctx, store, driver, state, journal, request, gatewayV2LANGrantError(ctx))
	}
	if lease.Activate(ctx, request) != nil {
		return gatewayV2LANGrantResult{}, rollbackGatewayV2LANGrantRetainingPending(ctx, store, driver, state,
			activationPending, journal, request)
	}
	if err := store.saveCommittedV2State(proposedState, journal); err != nil {
		// SQLite is active, but installation may have succeeded despite a
		// durability error. Keep the ambiguous pending/committed artifact for
		// locked restart recovery and never return a receipt from this invocation.
		return gatewayV2LANGrantResult{}, gatewayV2LANGrantError(ctx)
	}
	return newGatewayV2LANGrantResult(proposedState, request)
}

func gatewayV2LANBindingForRequest(request gatewayV2LANGrantRequest) (gatewayV2LANBinding, error) {
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: request.AppID, AllocationID: request.AllocationID, Port: request.Port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil || digest != request.AccessSpecDigest || !validCanonicalUUID(request.AccessRevisionID) ||
		request.AccessRevisionNumber <= 0 || !validSHA256(request.GatewayProfileSpecDigest) {
		return gatewayV2LANBinding{}, &Error{Code: DiagnosticValidationFailed}
	}
	return gatewayV2LANBinding{
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

func rollbackGatewayV2LANGrant(ctx context.Context, store *gatewayUpgradeStateStore, driver gatewayV2LANGrantDriver,
	committed gatewayV2RouteState, journal gatewayMigrationJournal, request gatewayV2LANGrantRequest, original error,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if driver.apply(rollbackCtx, committed, "lan-grant-rollback.json") != nil ||
		!driver.proveRolledBack(rollbackCtx, committed, journal, request) ||
		store.saveCommittedV2State(committed, journal) != nil {
		return gatewayV2LANGrantError(ctx)
	}
	return original
}

func rollbackGatewayV2LANGrantRetainingPending(ctx context.Context, store *gatewayUpgradeStateStore,
	driver gatewayV2LANGrantDriver, committed, pending gatewayV2RouteState, journal gatewayMigrationJournal,
	request gatewayV2LANGrantRequest,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if pending.Pending == nil || pending.Pending.Kind != gatewayV2PendingLANGrant ||
		!pending.Pending.ActivationUncertain || driver.apply(rollbackCtx, committed, "lan-grant-rollback.json") != nil ||
		!driver.proveRolledBack(rollbackCtx, committed, journal, request) ||
		store.saveCommittedV2State(pending, journal) != nil {
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
		Request: request, GatewayOperationID: state.OperationID, ProtectedStateDigest: digest,
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
	return d.manager.applyCommittedV2Routes(ctx, state, filename)
}

func (d managerGatewayV2LANGrantDriver) proveGranted(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	if d.manager == nil || d.manager.attestCommittedV2Locked(ctx, state, journal) != nil ||
		!validSHA256(journal.Resources.FinalContainerID) {
		return false
	}
	return proveGatewayV2LANGrantPublication(ctx, state, request, journal.Resources.FinalContainerID,
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
		probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge)
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
