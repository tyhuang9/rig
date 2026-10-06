package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GatewayV2LANStartupDisposition controls whether normal controller workers
// may start after one locked comparison of every durable grant claim and every
// protected LAN binding.
type GatewayV2LANStartupDisposition string

const (
	GatewayV2LANStartupNormal       GatewayV2LANStartupDisposition = "normal"
	GatewayV2LANStartupRecoveryOnly GatewayV2LANStartupDisposition = "recovery_only"
)

// GatewayV2LANStartupClaim is constructed from a validated, single-transaction
// appaccess startup snapshot. DisableIntentOperationID is set when a later
// approved disable freezes this exact grant.
type GatewayV2LANStartupClaim struct {
	Request                  GatewayV2LANGrantRequest
	CurrentBinding           *GatewayV2LANStartupBindingProjection
	RetainedBinding          *GatewayV2LANStartupBindingProjection
	State                    appaccess.AppAccessGrantState
	StateSequence            int64
	DisableIntentOperationID string
	RequiresRecovery         bool
}

// GatewayV2LANStartupInspection contains no address. AttemptID is set only for
// one exact attempt that requires recovery; competing recovery work fails
// closed rather than selecting an arbitrary claim.
type GatewayV2LANStartupInspection struct {
	Disposition GatewayV2LANStartupDisposition
	AttemptID   string
}

type gatewayV2LANStartupClaimSet struct {
	byAttempt map[string]GatewayV2LANStartupClaim
}

// InspectGatewayV2LANStartup is read-only. It must run before normal workers
// and after gateway-upgrade startup has selected one committed v2 generation.
func (m *Manager) InspectGatewayV2LANStartup(ctx context.Context, claims []GatewayV2LANStartupClaim) (
	inspection GatewayV2LANStartupInspection, resultErr error,
) {
	claimSet, err := validateGatewayV2LANStartupClaims(claims)
	if m == nil || ctx == nil || err != nil {
		return GatewayV2LANStartupInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGateway(lockCtx)
	if err != nil {
		return GatewayV2LANStartupInspection{}, err
	}
	defer func() {
		if err := release(); err != nil {
			inspection = GatewayV2LANStartupInspection{}
			resultErr = gatewayV2StartupInspectionError(ctx)
		}
	}()
	if ctx.Err() != nil {
		return GatewayV2LANStartupInspection{}, &Error{Code: DiagnosticCancelled}
	}
	proofCtx, cancelProof := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelProof()
	if current, handled, currentErr := m.inspectGatewayCurrentLANStartupLocked(proofCtx,
		gatewayV2LANAccessStartupClaims{grants: claimSet}); currentErr != nil || handled {
		if currentErr != nil {
			return GatewayV2LANStartupInspection{}, currentErr
		}
		if len(current.Recoveries) != 0 || (current.RecoveryKind != "" && current.RecoveryKind != GatewayV2LANRecoveryGrant) {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
		return GatewayV2LANStartupInspection{Disposition: current.Disposition, AttemptID: current.OperationID}, nil
	}

	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || !validGatewayV2RouteState(state) {
		return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}
	authority, err := m.readGatewayV2LANStartupAuthorityLocked(proofCtx, store, state, journal, claimSet, nil)
	if err != nil {
		return GatewayV2LANStartupInspection{}, err
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}

	recoveryAttempts := make(map[string]struct{})
	protected := make(map[string]GatewayV2LANGrantRequest)
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, *app.LAN)
		if _, duplicate := protected[request.AttemptID]; duplicate {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
		claim, exists := claimSet.byAttempt[request.AttemptID]
		if !exists || claim.Request != request {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
		protected[request.AttemptID] = request
		if claim.State != appaccess.AppAccessGrantCommitted || claim.DisableIntentOperationID != "" || claim.RequiresRecovery {
			recoveryAttempts[request.AttemptID] = struct{}{}
		}
	}

	pendingAttempt := ""
	var committedState, proposedState gatewayV2RouteState
	if state.Pending != nil {
		request, requestErr := gatewayV2LANPendingRequest(*state.Pending)
		if requestErr != nil {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
		claim, exists := claimSet.byAttempt[request.AttemptID]
		if !exists || claim.Request != request {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
		var pending bool
		committedState, proposedState, pending, err = gatewayV2LANGrantStatesForRequest(state, request)
		if err != nil || !pending {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
		pendingAttempt = request.AttemptID
		recoveryAttempts[pendingAttempt] = struct{}{}
	}

	for attemptID, claim := range claimSet.byAttempt {
		_, isProtected := protected[attemptID]
		isPending := attemptID == pendingAttempt
		switch {
		case isProtected:
			// Any state other than an undisabled committed claim was already
			// added to recoveryAttempts above.
		case isPending:
			recoveryAttempts[attemptID] = struct{}{}
		case claim.State == appaccess.AppAccessGrantRolledBack:
			// Retained terminal history is distinguishable by AttemptID and
			// does not own a current protected binding.
		default:
			// A prepared claim may legitimately precede its protected pending
			// record. Later states without an artifact are also recovery work;
			// none may permit normal startup.
			recoveryAttempts[attemptID] = struct{}{}
		}
	}
	if len(recoveryAttempts) > 1 {
		return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}

	if state.Pending != nil {
		switch driver.observePending(proofCtx, committedState, proposedState, journal) {
		case gatewayV2PendingCommittedExact, gatewayV2PendingProposedExact, gatewayV2PendingReloadOnlyMixed:
		default:
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
	} else {
		if !driver.proveCommitted(proofCtx, state, journal) || !driver.proveAllGranted(proofCtx, state, journal) {
			return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
		}
	}
	if driver.selectedInterfacePreflight(state.Profile) != nil || proofCtx.Err() != nil {
		return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}
	confirmed, confirmedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(confirmed, state) || !reflect.DeepEqual(confirmedJournal, journal) {
		return GatewayV2LANStartupInspection{}, gatewayV2StartupInspectionError(proofCtx)
	}
	if err := m.confirmGatewayV2LANStartupAuthorityLocked(proofCtx, authority, store, state, journal, claimSet, nil); err != nil {
		return GatewayV2LANStartupInspection{}, err
	}

	if len(recoveryAttempts) == 1 {
		for attemptID := range recoveryAttempts {
			return GatewayV2LANStartupInspection{
				Disposition: GatewayV2LANStartupRecoveryOnly,
				AttemptID:   attemptID,
			}, nil
		}
	}
	return GatewayV2LANStartupInspection{Disposition: GatewayV2LANStartupNormal}, nil
}

// QuarantineGatewayV2LANStartup runs before controller workers. It withdraws
// an exact protected LAN publication whose durable claim is nonterminal,
// disabled, or otherwise requires recovery. The exact AttemptID remains in a
// protected write-ahead marker, and success is returned only after the port is
// proved 404 while all unrelated committed publications remain exact.
func (m *Manager) QuarantineGatewayV2LANStartup(ctx context.Context, claims []GatewayV2LANStartupClaim) (
	resultErr error,
) {
	claimSet, err := validateGatewayV2LANStartupClaims(claims)
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
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, v2ObservationTimeout)
	defer cancelRecovery()

	if handled, err := m.quarantineGatewayCurrentLANStartupLocked(recoveryCtx, gatewayV2LANAccessStartupClaims{
		grants: claimSet,
	}); err != nil || handled {
		return err
	}
	store, state, journal, committed, err := m.committedV2Locked()
	if err != nil || !committed || store == nil || !validGatewayV2RouteState(state) {
		return gatewayV2StartupInspectionError(recoveryCtx)
	}
	authority, err := m.readGatewayV2LANStartupAuthorityLocked(recoveryCtx, store, state, journal, claimSet, nil)
	if err != nil {
		return err
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	mutationDriver := &gatewayV2LANStartupMutationDriver{gatewayV2LANGrantDriver: driver, manager: m,
		authority: authority, store: store, journal: journal, grants: claimSet}
	driver = mutationDriver
	if driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2StartupInspectionError(recoveryCtx)
	}

	// Every protected publication must still have one exact DB identity before
	// any mutation is attempted. This prevents a partial snapshot from choosing
	// an arbitrary live route to withdraw.
	unresolved := make([]GatewayV2LANGrantRequest, 0, 1)
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, *app.LAN)
		claim, exists := claimSet.byAttempt[request.AttemptID]
		if !exists || claim.Request != request {
			return gatewayV2StartupInspectionError(recoveryCtx)
		}
		if claim.State != appaccess.AppAccessGrantCommitted || claim.DisableIntentOperationID != "" || claim.RequiresRecovery {
			unresolved = append(unresolved, request)
		}
	}

	if state.Pending != nil {
		request, requestErr := gatewayV2LANPendingRequest(*state.Pending)
		claim, exists := claimSet.byAttempt[request.AttemptID]
		if requestErr != nil || !exists || claim.Request != request {
			return gatewayV2StartupInspectionError(recoveryCtx)
		}
		for _, candidate := range unresolved {
			if candidate.AttemptID != request.AttemptID {
				// The protected format intentionally serializes gateway mutations.
				// Multiple unresolved live grants cannot be represented without
				// erasing an uncertainty marker, so refuse startup.
				return gatewayV2StartupInspectionError(recoveryCtx)
			}
		}
		if err := m.confirmGatewayV2LANStartupAuthorityLocked(recoveryCtx, authority, store, state, journal, claimSet, nil); err != nil {
			return err
		}
		if _, _, err := withdrawGatewayV2LANPendingLocked(
			recoveryCtx, store, state, journal, request, driver,
		); err != nil {
			return err
		}
		return m.confirmGatewayV2LANStartupMutationResultLocked(recoveryCtx, authority, store, state, journal, claimSet, nil)
	}
	if len(unresolved) > 1 {
		return gatewayV2StartupInspectionError(recoveryCtx)
	}
	if len(unresolved) == 1 {
		if err := m.confirmGatewayV2LANStartupAuthorityLocked(recoveryCtx, authority, store, state, journal, claimSet, nil); err != nil {
			return err
		}
		if err := quarantineGatewayV2LANCommittedLocked(
			recoveryCtx, store, state, journal, unresolved[0], driver,
		); err != nil {
			return err
		}
		if mutationDriver.appliedPending == nil {
			return gatewayV2StartupInspectionError(recoveryCtx)
		}
		return m.confirmGatewayV2LANStartupMutationResultLocked(recoveryCtx, authority, store, *mutationDriver.appliedPending, journal, claimSet, nil)
	}
	if !driver.proveCommitted(recoveryCtx, state, journal) ||
		!driver.proveAllGranted(recoveryCtx, state, journal) ||
		driver.selectedInterfacePreflight(state.Profile) != nil {
		return gatewayV2StartupInspectionError(recoveryCtx)
	}
	return m.confirmGatewayV2LANStartupAuthorityLocked(recoveryCtx, authority, store, state, journal, claimSet, nil)
}

// StopOwnedGatewayV2OnStartupFailure is the last-resort fail-closed path for a
// DB snapshot or protected route-state inspection failure. It does not trust
// route state. Instead it re-reads the immutable history directory, validates
// the latest purpose-bound migration journal, and stops only the final
// container whose ID and complete ownership-label set match that journal.
// Stopping the gateway also interrupts local routes; callers must refuse
// normal startup and require explicit recovery.
// It intentionally constructs no generally usable Manager: the only operation
// available through this entry point is the journal-bound exact-owned stop.
func StopOwnedGatewayV2OnStartupFailure(ctx context.Context, runner runtimeprocess.CommandRunner,
	options Options,
) error {
	manager, err := newManager(runner, options)
	if err != nil {
		return err
	}
	return manager.stopOwnedGatewayV2OnStartupFailure(ctx)
}

func (m *Manager) stopOwnedGatewayV2OnStartupFailure(ctx context.Context) (resultErr error) {
	if m == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, observationTimeout)
	defer cancelLock()
	release, err := m.lockGatewayRaw(lockCtx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancelRecovery()
	journal, err := m.latestGatewayV2JournalForEmergencyStop()
	if err != nil {
		return gatewayV2StartupInspectionError(recoveryCtx)
	}
	driver := m.gatewayV2LANGrantDriver
	if driver == nil {
		driver = managerGatewayV2LANGrantDriver{manager: m}
	}
	if err := driver.stopOwnedGateway(recoveryCtx, journal); err != nil {
		return gatewayV2StartupInspectionError(recoveryCtx)
	}
	confirmed, err := m.latestGatewayV2JournalForEmergencyStop()
	if err != nil || !reflect.DeepEqual(confirmed, journal) {
		m.gatewayRebindFailStopLatch().Store(true)
		return gatewayV2StartupInspectionError(recoveryCtx)
	}
	return nil
}

func (m *Manager) latestGatewayV2JournalForEmergencyStop() (gatewayMigrationJournal, error) {
	if m == nil || m.store == nil {
		return gatewayMigrationJournal{}, errors.New("generated ingress store is unavailable")
	}
	targets, census, err := m.gatewayCurrentOwnedStopTargetsProtectedPartialLocked()
	if err != nil || census.UnresolvedAttempt || census.CommittedOwnership || len(targets) != 0 {
		return gatewayMigrationJournal{}, errors.New("generated ingress native emergency selection has unresolved rebind ownership")
	}
	// Retained terminal aborts are allowed only after their complete protected
	// census proved that no successor/current ownership needs withdrawal.
	before, err := readGatewayHistorySnapshotMode(m.store, census.ProtectedRebindHistory)
	if err != nil || len(before.generations) == 0 {
		return gatewayMigrationJournal{}, errors.New("generated ingress history is unavailable")
	}
	generations := make([]uint64, 0, len(before.generations))
	for generation := range before.generations {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	for index, generation := range generations {
		if generation != uint64(index) {
			return gatewayMigrationJournal{}, errors.New("generated ingress history has a generation gap")
		}
	}
	latestGeneration := generations[len(generations)-1]
	artifacts := before.generations[latestGeneration]
	if artifacts.journal.path == "" || artifacts.abort.path != "" {
		return gatewayMigrationJournal{}, errors.New("latest generated ingress generation has no stoppable journal")
	}
	var store *gatewayUpgradeStateStore
	if latestGeneration == 0 {
		store, err = newGatewayUpgradeStateStore(m.options.DataRoot)
	} else {
		store, err = newGatewayUpgradeGenerationStore(m.options.DataRoot, latestGeneration, artifacts.operationID)
	}
	if err != nil || store.journalPath != artifacts.journal.path {
		return gatewayMigrationJournal{}, errors.New("latest generated ingress journal path is invalid")
	}
	journal, err := store.loadMigrationJournal()
	if err != nil || !validGatewayMigrationJournal(journal) || !validSHA256(journal.Resources.FinalContainerID) ||
		(artifacts.operationID != "" && artifacts.operationID != journal.OperationID) {
		return gatewayMigrationJournal{}, errors.New("latest generated ingress journal is invalid")
	}
	after, err := readGatewayHistorySnapshotMode(m.store, census.ProtectedRebindHistory)
	if err != nil || !sameGatewayHistorySnapshot(before, after) ||
		m.confirmGatewayCurrentEmergencyCensusLocked(targets, census) != nil {
		return gatewayMigrationJournal{}, errors.New("generated ingress history changed during emergency selection")
	}
	return journal, nil
}

func validateGatewayV2LANStartupClaims(claims []GatewayV2LANStartupClaim) (gatewayV2LANStartupClaimSet, error) {
	result := gatewayV2LANStartupClaimSet{byAttempt: make(map[string]GatewayV2LANStartupClaim, len(claims))}
	for _, claim := range claims {
		if _, err := gatewayV2LANBindingForRequest(claim.Request); err != nil ||
			!gatewayV2LANStartupClaimSequenceValid(claim.State, claim.StateSequence) ||
			!validGatewayV2LANStartupGrantBindings(claim) ||
			(claim.DisableIntentOperationID != "" && !validCanonicalUUID(claim.DisableIntentOperationID)) {
			return gatewayV2LANStartupClaimSet{}, gatewayV2StartupInspectionError(nil)
		}
		if _, duplicate := result.byAttempt[claim.Request.AttemptID]; duplicate {
			return gatewayV2LANStartupClaimSet{}, gatewayV2StartupInspectionError(nil)
		}
		result.byAttempt[claim.Request.AttemptID] = claim
	}
	return result, nil
}

func gatewayV2LANStartupClaimSequenceValid(state appaccess.AppAccessGrantState, sequence int64) bool {
	switch state {
	case appaccess.AppAccessGrantPrepared:
		return sequence == 1
	case appaccess.AppAccessGrantApplying:
		return sequence == 2
	case appaccess.AppAccessGrantDBActive:
		return sequence == 3
	case appaccess.AppAccessGrantUncertain:
		return sequence >= 3
	case appaccess.AppAccessGrantCommitted:
		return sequence >= 4
	case appaccess.AppAccessGrantRolledBack:
		return sequence >= 2
	default:
		return false
	}
}

func gatewayV2LANGrantRequestForBinding(appID string, binding gatewayV2LANBinding) GatewayV2LANGrantRequest {
	return GatewayV2LANGrantRequest{
		AttemptID: binding.GrantAttemptID, ClaimRequestDigest: binding.GrantRequestDigest,
		AppID: appID, AllocationID: binding.AllocationID, OwnerOperationID: binding.OwnerOperationID,
		Port: binding.Port, AccessRevisionID: binding.AccessRevisionID,
		AccessRevisionNumber: binding.AccessRevisionNumber, AccessSpecDigest: binding.AccessSpecDigest,
		ApprovedBy: binding.ApprovedBy, GatewayProfileRevisionID: binding.ProfileRevisionID,
		GatewayProfileRevisionNumber: binding.ProfileRevisionNumber,
		GatewayProfileSpecDigest:     binding.ProfileSpecDigest,
	}
}

func gatewayV2LANPendingRequest(pending gatewayV2PendingRoute) (GatewayV2LANGrantRequest, error) {
	var binding *gatewayV2LANBinding
	switch pending.Kind {
	case gatewayV2PendingLANGrant:
		binding = pending.Proposed.LAN
	case gatewayV2PendingLANWithdrawal:
		if pending.Previous != nil {
			binding = pending.Previous.LAN
		}
	}
	if binding == nil {
		return GatewayV2LANGrantRequest{}, gatewayV2StartupInspectionError(nil)
	}
	request := gatewayV2LANGrantRequestForBinding(pending.AppID, *binding)
	if _, err := gatewayV2LANBindingForRequest(request); err != nil {
		return GatewayV2LANGrantRequest{}, err
	}
	return request, nil
}
