package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindRecoveryProjection struct {
	status  GatewayRebindOperationInspection
	sql     appaccess.GatewayRebindRecoverySnapshot
	startup appaccess.HostingGatewayStartupSnapshot
	trusted *appaccess.GatewayRebindTrustedRecoverySnapshot
}

// InspectGatewayRebindOperation is a diagnostic, read-only view. It uses the
// raw inspection lock so an existing fence or process fail-stop does not hide
// durable status. It never advertises an executable action or creates consent.
func (m *Manager) InspectGatewayRebindOperation(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, operationID string,
) (result GatewayRebindOperationInspection, resultErr error) {
	if m == nil || ctx == nil || repository == nil || !validCanonicalUUID(operationID) {
		return GatewayRebindOperationInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGatewayRawForInspection(ctx)
	if err != nil {
		return GatewayRebindOperationInspection{}, err
	}
	defer releaseGatewayLock(release, &resultErr)
	projection, err := m.projectGatewayRebindRecoveryLocked(ctx, repository, operationID)
	if err != nil {
		return GatewayRebindOperationInspection{}, err
	}
	return projection.status, nil
}

// InspectGatewayRebindRecoveryReview adds request-session, original-approver,
// and current physical checks to the diagnostic projection. Unsupported or
// unavailable recovery remains reviewable with no action and no consent
// digest. The method is advisory and performs no audit or effect.
func (m *Manager) InspectGatewayRebindRecoveryReview(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, operationID, actorID string,
	liveSession func(context.Context) error,
) (result GatewayRebindRecoveryReview, resultErr error) {
	if m == nil || ctx == nil || repository == nil || liveSession == nil ||
		!validCanonicalUUID(operationID) || !validCanonicalUUID(actorID) {
		return GatewayRebindRecoveryReview{}, &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return GatewayRebindRecoveryReview{}, gatewayRebindEffectBoundaryError(ctx)
	}
	release, err := m.lockGatewayRawForInspection(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			m.gatewayRebindFailStopLatch().Store(true)
		}
		return GatewayRebindRecoveryReview{}, err
	}
	defer func() {
		if releaseErr := releaseEffects(); releaseErr != nil {
			resultErr = errors.Join(resultErr, gatewayRebindEffectBoundaryError(ctx))
			m.gatewayRebindFailStopLatch().Store(true)
		}
		if releaseErr := release(); releaseErr != nil {
			resultErr = errors.Join(resultErr, gatewayRebindEffectBoundaryError(ctx))
			m.gatewayRebindFailStopLatch().Store(true)
		}
		if resultErr != nil {
			result = GatewayRebindRecoveryReview{}
		}
	}()
	return m.inspectGatewayRebindRecoveryReviewLocked(ctx, repository, operationID, actorID, liveSession)
}

func (m *Manager) inspectGatewayRebindRecoveryReviewLocked(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, operationID, actorID string,
	liveSession func(context.Context) error,
) (GatewayRebindRecoveryReview, error) {
	return m.inspectGatewayRebindRecoveryReviewWithCurrentProofLocked(ctx, repository,
		operationID, actorID, liveSession, m.readGatewayRebindRecoveryCurrentProofLocked)
}

func (m *Manager) inspectGatewayRebindRecoveryReviewWithCurrentProofLocked(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, operationID, actorID string,
	liveSession func(context.Context) error, readCurrent gatewayRebindRecoveryCurrentProofReader,
) (GatewayRebindRecoveryReview, error) {
	if m == nil || ctx == nil || repository == nil || liveSession == nil ||
		readCurrent == nil || !validCanonicalUUID(operationID) || !validCanonicalUUID(actorID) {
		return GatewayRebindRecoveryReview{}, &Error{Code: DiagnosticValidationFailed}
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return GatewayRebindRecoveryReview{}, err
	}
	projection, err := m.projectGatewayRebindRecoveryLocked(ctx, repository, operationID)
	if err != nil {
		return GatewayRebindRecoveryReview{}, err
	}
	result := GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
		Status: projection.status}
	if projection.status.Availability != GatewayRebindOperationAvailable ||
		!projection.status.Active || projection.trusted == nil || projection.status.RecoveryPolicy == "" ||
		m.gatewayRebindAdmissionBlocked() ||
		!projection.startup.ActiveRebindApprovalsAuthorizeServing() {
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return GatewayRebindRecoveryReview{}, err
		}
		if ValidateGatewayRebindRecoveryReview(result, operationID, actorID) != nil {
			return GatewayRebindRecoveryReview{}, gatewayRebindProposalError(ctx)
		}
		return result, nil
	}
	current, err := readCurrent(ctx, repository, projection.sql, liveSession)
	if err != nil {
		if sessionErr := checkGatewayRebindAdmissionSession(ctx, liveSession); sessionErr != nil {
			return GatewayRebindRecoveryReview{}, sessionErr
		}
		if ctx.Err() != nil {
			return GatewayRebindRecoveryReview{}, err
		}
		if ValidateGatewayRebindRecoveryReview(result, operationID, actorID) != nil {
			return GatewayRebindRecoveryReview{}, gatewayRebindProposalError(ctx)
		}
		return result, nil
	}
	selected, err := gatewayRebindSelectedCurrentEvidence(current.Selection)
	if err != nil || !reflect.DeepEqual(selected, projection.trusted.SelectedCurrent) ||
		projection.status.SelectedCurrentAuthority == nil ||
		gatewayCurrentAuthority(current.Selection.Lineage) != *projection.status.SelectedCurrentAuthority ||
		!validSHA256(current.Digest) {
		return GatewayRebindRecoveryReview{}, gatewayRebindProposalError(ctx)
	}
	result.Status.CurrentProof = GatewayRebindProofVerified
	result.Status.CurrentAttestationDigest = current.Digest
	review, err := appaccess.BuildGatewayRebindReconcileReview(projection.sql, *projection.trusted, actorID)
	if err != nil || review.Snapshot.Policy != result.Status.RecoveryPolicy {
		return GatewayRebindRecoveryReview{}, gatewayRebindProposalError(ctx)
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return GatewayRebindRecoveryReview{}, err
	}
	result.Status.AllowedActions = []GatewayRebindRecoveryAction{GatewayRebindRecoveryActionReconcile}
	result.AuthorizationReview = review
	if ValidateGatewayRebindRecoveryReview(result, operationID, actorID) != nil {
		return GatewayRebindRecoveryReview{}, gatewayRebindProposalError(ctx)
	}
	return result, nil
}

func (m *Manager) projectGatewayRebindRecoveryLocked(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, operationID string,
) (gatewayRebindRecoveryProjection, error) {
	invalid := func() (gatewayRebindRecoveryProjection, error) {
		return gatewayRebindRecoveryProjection{}, gatewayRebindProposalError(ctx)
	}
	if m == nil || ctx == nil || repository == nil || ctx.Err() != nil ||
		!validCanonicalUUID(operationID) {
		return invalid()
	}
	first, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return invalid()
	}
	if gatewayRebindRecoveryPristineSQL(first) {
		projection, err := m.projectGatewayRebindPristineAbsenceLocked(ctx, repository,
			operationID, first)
		if err != nil {
			return invalid()
		}
		return projection, nil
	}
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if filesErr != nil {
		return invalid()
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return invalid()
	}
	if !gatewayRebindRecoveryFenceMatches(first.Rebind, repository.CheckGatewayRebindFence(ctx)) {
		return invalid()
	}

	match := -1
	for index, entry := range first.Rebind.History {
		candidate, candidateErr := gatewayRebindHistoryOperationID(entry)
		if candidateErr != nil || !validCanonicalUUID(candidate) {
			return invalid()
		}
		if candidate == operationID {
			if match >= 0 {
				return invalid()
			}
			match = index
		}
	}
	protectedPresent := gatewayRebindProtectedHistoryHasOperation(history, operationID)
	if match < 0 {
		if protectedPresent {
			return invalid()
		}
		status := GatewayRebindOperationInspection{
			Version: GatewayRebindOperationInspectionVersion, OperationID: operationID,
			Availability:  GatewayRebindOperationValidatedAbsent,
			TerminalProof: GatewayRebindProofUnknown, CurrentProof: GatewayRebindProofUnknown,
			OperationFence: GatewayRebindOperationFenceUnavailable,
		}
		if err := m.confirmGatewayRebindRecoveryProjectionLocked(ctx, repository, first,
			history, files, nil); err != nil || ValidateGatewayRebindOperationInspection(status, operationID) != nil {
			return invalid()
		}
		return gatewayRebindRecoveryProjection{status: status, sql: first.Rebind, startup: first}, nil
	}

	entry := first.Rebind.History[match]
	if entry.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 || entry.Claim.Legacy != nil ||
		entry.Claim.V2 == nil {
		return invalid()
	}
	claim := *entry.Claim.V2
	active := first.Rebind.Active != nil && reflect.DeepEqual(entry, *first.Rebind.Active)
	if gatewayRebindCommitActiveState(claim.State) != active ||
		(active && (first.Rebind.Phase != claim.State ||
			!reflect.DeepEqual(entry, *first.Rebind.Active))) {
		return invalid()
	}
	databaseEvent, databaseCommitted, eventOK := gatewayRebindCommitDatabaseEvent(entry)
	if !eventOK || active && (first.Rebind.DatabaseCommitObserved != databaseCommitted ||
		!sameGatewayRebindOptionalEvent(first.Rebind.DatabaseCommittedEvent,
			gatewayRebindOptionalEvent(databaseEvent, databaseCommitted))) {
		return invalid()
	}
	operation := GatewayRebindOperationStatus{
		OperationID: operationID, StoredState: claim.State, StateSequence: claim.StateSequence,
		DatabaseCommitObserved: databaseCommitted, TerminalDisposition: appaccess.GatewayRebindDispositionNone,
	}
	status := GatewayRebindOperationInspection{
		Version: GatewayRebindOperationInspectionVersion, OperationID: operationID,
		Availability: GatewayRebindOperationAvailable, Operation: operation, Active: active,
		TerminalProof: GatewayRebindProofUnavailable, CurrentProof: GatewayRebindProofUnavailable,
	}
	if databaseCommitted {
		copy := databaseEvent
		status.DatabaseCommittedEvent = &copy
	}
	if active {
		status.OperationFence = GatewayRebindOperationFenceHeld
	} else {
		command, _, ok := gatewayRebindCommitTerminalProjection(claim, entry)
		if !ok {
			return invalid()
		}
		status.HistoricalTerminal = true
		status.Operation.TerminalDisposition = command.TerminalDisposition
		status.Operation.TerminalReceiptDigest = command.TerminalReceiptDigest
		status.OperationFence = GatewayRebindOperationFenceReleased
	}

	selection, err := m.selectGatewayCurrentLocked(ctx, first.Rebind)
	if err != nil || first.Rebind.CurrentSource == nil {
		return invalid()
	}
	selected, err := gatewayRebindSelectedCurrentEvidence(selection)
	if err != nil || gatewayCurrentAuthority(selection.Lineage) != *first.Rebind.CurrentSource {
		return invalid()
	}
	currentAuthority := *first.Rebind.CurrentSource
	status.SelectedCurrentAuthority = &currentAuthority
	protected, phase, complete, err := gatewayRebindProtectedRecoveryProjection(history, entry)
	if err != nil {
		return invalid()
	}
	var trusted *appaccess.GatewayRebindTrustedRecoverySnapshot
	if complete {
		copy := protected
		status.Protected = &copy
		status.ProtectedPhase = phase
		value := appaccess.GatewayRebindTrustedRecoverySnapshot{
			Protected: protected, SelectedCurrent: selected,
		}
		trusted = &value
		if active {
			if policy, policyErr := appaccess.GatewayRebindRecoveryPolicyForSnapshot(first.Rebind, value); policyErr == nil {
				status.RecoveryPolicy = policy
			}
		}
	}
	if err := m.confirmGatewayRebindRecoveryProjectionLocked(ctx, repository, first,
		history, files, &selection); err != nil ||
		ValidateGatewayRebindOperationInspection(status, operationID) != nil {
		return invalid()
	}
	return gatewayRebindRecoveryProjection{status: status, sql: first.Rebind, startup: first,
		trusted: trusted}, nil
}

func gatewayRebindRecoveryPristineSQL(value appaccess.HostingGatewayStartupSnapshot) bool {
	snapshot := value.Rebind
	return len(value.Upgrades.Claims) == 0 && value.Upgrades.CurrentProfile == nil &&
		len(snapshot.History) == 0 && snapshot.Active == nil && snapshot.CurrentProfile == nil &&
		snapshot.CurrentSource == nil && snapshot.CurrentDatabaseCommittedEvent == nil &&
		len(snapshot.CurrentTransfers) == 0 && snapshot.DatabaseCommittedEvent == nil &&
		snapshot.Phase == "" && !snapshot.DatabaseCommitObserved && !snapshot.RollbackAllowed
}

func (m *Manager) projectGatewayRebindPristineAbsenceLocked(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, operationID string,
	first appaccess.HostingGatewayStartupSnapshot,
) (gatewayRebindRecoveryProjection, error) {
	invalid := func() (gatewayRebindRecoveryProjection, error) {
		return gatewayRebindRecoveryProjection{}, gatewayRebindProposalError(ctx)
	}
	presence, presenceErr := readGatewayRebindProtectedPresenceReadOnly(m.options.DataRoot)
	history, historyErr := readGatewayHistorySnapshotMode(m.store, true)
	state, stateErr := m.store.load()
	if presenceErr != nil || historyErr != nil || stateErr != nil || presence.present ||
		!gatewayRebindRecoveryPristineFiles(presence, history) ||
		!gatewayRebindRecoveryPristineRouteState(state) ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		return invalid()
	}
	second, secondErr := repository.HostingGatewayStartupSnapshot(ctx)
	confirmedPresence, confirmedPresenceErr := readGatewayRebindProtectedPresenceReadOnly(m.options.DataRoot)
	confirmedHistory, confirmedHistoryErr := readGatewayHistorySnapshotMode(m.store, true)
	confirmedState, confirmedStateErr := m.store.load()
	if secondErr != nil || confirmedPresenceErr != nil || confirmedHistoryErr != nil || confirmedStateErr != nil ||
		ctx.Err() != nil || !reflect.DeepEqual(first, second) ||
		!sameGatewayRebindProtectedPresence(presence, confirmedPresence) ||
		!sameGatewayHistorySnapshot(history, confirmedHistory) ||
		!reflect.DeepEqual(state, confirmedState) ||
		!gatewayRebindRecoveryPristineSQL(second) || confirmedPresence.present ||
		!gatewayRebindRecoveryPristineFiles(confirmedPresence, confirmedHistory) ||
		!gatewayRebindRecoveryPristineRouteState(confirmedState) ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		return invalid()
	}
	status := GatewayRebindOperationInspection{
		Version: GatewayRebindOperationInspectionVersion, OperationID: operationID,
		Availability:  GatewayRebindOperationValidatedAbsent,
		TerminalProof: GatewayRebindProofUnknown, CurrentProof: GatewayRebindProofUnknown,
		OperationFence: GatewayRebindOperationFenceUnavailable,
	}
	if ValidateGatewayRebindOperationInspection(status, operationID) != nil {
		return invalid()
	}
	return gatewayRebindRecoveryProjection{status: status, sql: first.Rebind, startup: first}, nil
}

func gatewayRebindRecoveryPristineFiles(presence gatewayRebindProtectedPresenceSnapshot,
	history gatewayHistorySnapshot,
) bool {
	if len(history.generations) != 0 || len(history.rebindIntents) != 0 || len(history.files) != 0 {
		return false
	}
	for name := range presence.files {
		if name != stateFilename && name != gatewayLockFilename {
			return false
		}
	}
	return true
}

func gatewayRebindRecoveryPristineRouteState(value routeState) bool {
	return value.Version == stateVersion && value.Pending == nil && value.Active != nil && len(value.Active) == 0
}

func (m *Manager) confirmGatewayRebindRecoveryProjectionLocked(ctx context.Context,
	repository GatewayRebindRecoveryReadRepository, first appaccess.HostingGatewayStartupSnapshot,
	history gatewayRebindProtectedIntentHistory, files gatewayHistorySnapshot,
	selection *gatewayCurrentSelection,
) error {
	second, err := repository.HostingGatewayStartupSnapshot(ctx)
	confirmedHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	confirmedFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || historyErr != nil || filesErr != nil || ctx.Err() != nil ||
		!reflect.DeepEqual(first, second) || !sameGatewayRebindCurrentHistory(history, confirmedHistory) ||
		!sameGatewayHistorySnapshot(files, confirmedFiles) ||
		!gatewayRebindRecoveryFenceMatches(second.Rebind, repository.CheckGatewayRebindFence(ctx)) {
		return gatewayRebindProposalError(ctx)
	}
	if selection != nil {
		confirmed, selectionErr := m.selectGatewayCurrentLocked(ctx, second.Rebind)
		if selectionErr != nil || !sameGatewayCurrentSelection(*selection, confirmed) {
			return gatewayRebindProposalError(ctx)
		}
	}
	return nil
}

func gatewayRebindRecoveryFenceMatches(snapshot appaccess.GatewayRebindRecoverySnapshot, err error) bool {
	if snapshot.Active == nil {
		return err == nil
	}
	return errors.Is(err, appaccess.ErrGatewayRebindActive)
}

func gatewayRebindOptionalEvent(value appaccess.GatewayRebindEvent, present bool) *appaccess.GatewayRebindEvent {
	if !present {
		return nil
	}
	copy := value
	return &copy
}

func sameGatewayRebindOptionalEvent(left, right *appaccess.GatewayRebindEvent) bool {
	return reflect.DeepEqual(left, right)
}

func gatewayRebindSelectedCurrentEvidence(selection gatewayCurrentSelection) (
	appaccess.GatewayRebindSelectedCurrentEvidence, error,
) {
	value := appaccess.GatewayRebindSelectedCurrentEvidence{Lineage: selection.Lineage}
	var err error
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil {
			return appaccess.GatewayRebindSelectedCurrentEvidence{}, errors.New("missing selected upgrade")
		}
		value.StateVersion = v2RouteStateVersion
		value.StateDigest, err = canonicalDigest(selection.Upgrade.State)
	case gatewayCurrentSelectionRebind:
		if selection.State == nil {
			return appaccess.GatewayRebindSelectedCurrentEvidence{}, errors.New("missing selected rebind")
		}
		value.StateVersion, value.StateRevision, value.StateDigest = selection.State.Version,
			selection.State.Revision, selection.State.Digest
	default:
		return appaccess.GatewayRebindSelectedCurrentEvidence{}, errors.New("unknown selected current")
	}
	if err != nil || !validSHA256(value.StateDigest) {
		return appaccess.GatewayRebindSelectedCurrentEvidence{}, errors.New("invalid selected current")
	}
	return value, nil
}

func gatewayRebindProtectedHistoryHasOperation(history gatewayRebindProtectedIntentHistory,
	operationID string,
) bool {
	for _, value := range history.Checkpoints {
		if value.Checkpoint.OperationID == operationID {
			return true
		}
	}
	for _, value := range history.Intents {
		if value.Intent.OperationID == operationID {
			return true
		}
	}
	for _, value := range history.IntentsV2 {
		if value.Intent.OperationID == operationID {
			return true
		}
	}
	for _, value := range history.Progress {
		if value.Record.OperationID == operationID {
			return true
		}
	}
	for _, value := range history.Terminals {
		if value.Receipt.OperationID == operationID {
			return true
		}
	}
	for _, value := range history.TerminalsV2 {
		if value.Receipt.OperationID == operationID {
			return true
		}
	}
	return false
}

func gatewayRebindProtectedRecoveryProjection(history gatewayRebindProtectedIntentHistory,
	entry appaccess.GatewayRebindHistoryEntry,
) (appaccess.GatewayRebindProtectedRecoveryEvidence, string, bool, error) {
	if entry.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		entry.Claim.Legacy != nil || entry.Claim.V2 == nil {
		return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
			errors.New("protected recovery projection requires typed claim")
	}
	claim := *entry.Claim.V2
	digest, err := gatewayRebindProtectedRecoveryScanDigest(history)
	if err != nil {
		return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false, err
	}
	operationID, generation := claim.Spec.OperationID, claim.Spec.SuccessorProtectedGeneration
	var intent *gatewayRebindProtectedIntentV2
	var progress []gatewayRebindProgressRecord
	var terminal *gatewayRebindTerminalReceiptV2
	for index := range history.Intents {
		if history.Intents[index].Intent.OperationID == operationID {
			return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
				errors.New("legacy protected evidence uses typed operation")
		}
	}
	for index := range history.Terminals {
		if history.Terminals[index].Receipt.OperationID == operationID {
			return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
				errors.New("legacy protected terminal uses typed operation")
		}
	}
	for index := range history.IntentsV2 {
		candidate := history.IntentsV2[index].Intent
		if candidate.OperationID == operationID {
			if intent != nil || candidate.Generation != generation {
				return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
					errors.New("ambiguous protected intent")
			}
			copy := candidate
			intent = &copy
		}
	}
	for index := range history.Progress {
		candidate := history.Progress[index].Record
		if candidate.OperationID == operationID {
			if candidate.Generation != generation {
				return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
					errors.New("cross-generation protected progress")
			}
			progress = append(progress, candidate)
		}
	}
	for index := range history.TerminalsV2 {
		candidate := history.TerminalsV2[index].Receipt
		if candidate.OperationID == operationID {
			if terminal != nil || candidate.Generation != generation {
				return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
					errors.New("ambiguous protected terminal")
			}
			copy := candidate
			terminal = &copy
		}
	}
	if intent == nil && len(progress) == 0 && terminal == nil {
		return appaccess.GatewayRebindProtectedRecoveryEvidence{
			Presence: appaccess.GatewayRebindProtectedEvidenceAbsent, OperationID: operationID,
			Generation: generation, ScanDigest: digest,
		}, "", true, nil
	}
	if intent == nil && len(progress) == 0 && terminal != nil {
		checkpoint, checkpointErr := gatewayRebindActiveCheckpoint(history, claim)
		if checkpointErr != nil || !gatewayRebindTerminalV2MatchesNoIntentHistory(*terminal, checkpoint) ||
			!gatewayRebindTerminalMatchesHistoryEntry(*terminal, entry) ||
			terminal.ProtectedIntentDigest != "" || terminal.NoEffectProof == nil ||
			terminal.ProtectedPhase != gatewayRebindTerminalPhaseNoEffectAbort {
			return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
				errors.New("invalid checkpoint terminal recovery evidence")
		}
		return appaccess.GatewayRebindProtectedRecoveryEvidence{
			Presence:    appaccess.GatewayRebindProtectedEvidenceCheckpointTerminal,
			OperationID: operationID, Generation: generation, ScanDigest: digest,
			FixedDisposition:      appaccess.GatewayRebindDispositionAbort,
			TerminalReceiptDigest: terminal.Digest,
		}, "", true, nil
	}
	if intent != nil && len(progress) == 0 && terminal == nil {
		checkpoint, checkpointErr := gatewayRebindActiveCheckpoint(history, claim)
		if checkpointErr != nil || !sameGatewayRebindClaimV2Admission(intent.Claim, claim) ||
			!sameGatewayRebindRosterV2(intent.Roster, entry.RosterV2) ||
			!sameGatewayRebindRuntimeHeads(intent.RuntimeHeads, entry.RuntimeHeads) ||
			intent.Predecessor != checkpoint.sourceRef() {
			return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
				errors.New("partial protected intent does not match retained SQL history")
		}
		return appaccess.GatewayRebindProtectedRecoveryEvidence{
			Presence: appaccess.GatewayRebindProtectedEvidencePresent, OperationID: operationID,
			Generation: generation, ScanDigest: digest, IntentDigest: intent.Digest,
			FixedDisposition: appaccess.GatewayRebindDispositionNone,
		}, "", false, nil
	}
	if intent == nil || len(progress) == 0 {
		return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false, nil
	}
	retained := appaccess.GatewayRebindRecoverySnapshot{Active: &entry}
	prepared, preparedErr := gatewayRebindPreparedAttemptFromHistory(retained, history)
	if preparedErr != nil || !reflect.DeepEqual(prepared.Intent, *intent) ||
		!sameGatewayRebindClaimV2Admission(prepared.Claim, claim) ||
		!sameGatewayRebindRosterV2(prepared.Intent.Roster, entry.RosterV2) ||
		!sameGatewayRebindRuntimeHeads(prepared.Intent.RuntimeHeads, entry.RuntimeHeads) {
		return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
			errors.New("protected intent does not match retained SQL history")
	}
	last := progress[len(progress)-1]
	value := appaccess.GatewayRebindProtectedRecoveryEvidence{
		Presence: appaccess.GatewayRebindProtectedEvidencePresent, OperationID: operationID,
		Generation: generation, ScanDigest: digest, IntentDigest: intent.Digest,
		RecordSequence: last.Sequence, RecordDigest: last.Digest,
		FixedDisposition: appaccess.GatewayRebindDispositionNone,
	}
	switch last.Phase {
	case gatewayRebindProgressHandoverCommitted:
		value.FixedDisposition = appaccess.GatewayRebindDispositionCommit
	case gatewayRebindProgressRollbackIntent, gatewayRebindProgressHandoverRolledBack:
		value.FixedDisposition = appaccess.GatewayRebindDispositionAbort
	}
	if terminal != nil {
		if value.FixedDisposition != appaccess.GatewayRebindDispositionNone &&
			value.FixedDisposition != terminal.Disposition {
			return appaccess.GatewayRebindProtectedRecoveryEvidence{}, "", false,
				errors.New("protected disposition conflicts")
		}
		value.FixedDisposition = terminal.Disposition
		value.TerminalReceiptDigest = terminal.Digest
	}
	return value, string(last.Phase), true, nil
}

func gatewayRebindProtectedRecoveryScanDigest(history gatewayRebindProtectedIntentHistory) (string, error) {
	checkpoints := make([]gatewayRebindPredecessorCheckpoint, len(history.Checkpoints))
	legacyIntents := make([]gatewayRebindProtectedIntent, len(history.Intents))
	typedIntents := make([]gatewayRebindProtectedIntentV2, len(history.IntentsV2))
	progress := make([]gatewayRebindProgressRecord, len(history.Progress))
	legacyTerminals := make([]gatewayRebindFinalHandoverTerminalReceipt, len(history.Terminals))
	typedTerminals := make([]gatewayRebindTerminalReceiptV2, len(history.TerminalsV2))
	for index := range history.Checkpoints {
		checkpoints[index] = history.Checkpoints[index].Checkpoint
	}
	for index := range history.Intents {
		legacyIntents[index] = history.Intents[index].Intent
	}
	for index := range history.IntentsV2 {
		typedIntents[index] = history.IntentsV2[index].Intent
	}
	for index := range history.Progress {
		progress[index] = history.Progress[index].Record
	}
	for index := range history.Terminals {
		legacyTerminals[index] = history.Terminals[index].Receipt
	}
	for index := range history.TerminalsV2 {
		typedTerminals[index] = history.TerminalsV2[index].Receipt
	}
	return canonicalDigest(struct {
		Version         int                                         `json:"version"`
		Purpose         string                                      `json:"purpose"`
		Predecessor     gatewayRebindRecoveryPredecessorDigestInput `json:"predecessor"`
		Source          routeState                                  `json:"source"`
		Checkpoints     []gatewayRebindPredecessorCheckpoint        `json:"checkpoints"`
		LegacyIntents   []gatewayRebindProtectedIntent              `json:"legacyIntents"`
		TypedIntents    []gatewayRebindProtectedIntentV2            `json:"typedIntents"`
		Progress        []gatewayRebindProgressRecord               `json:"progress"`
		LegacyTerminals []gatewayRebindFinalHandoverTerminalReceipt `json:"legacyTerminals"`
		TypedTerminals  []gatewayRebindTerminalReceiptV2            `json:"typedTerminals"`
	}{1, "hostd/generated-ingress/rebind/recovery-protected-scan/v1",
		gatewayRebindRecoveryPredecessorDigestInput{
			Generation: history.Predecessor.Generation, State: history.Predecessor.State,
			Journal: history.Predecessor.Journal, Existing: history.Predecessor.Existing,
			PartialState: history.Predecessor.PartialState, Retired: history.Predecessor.Retired,
			Aborted: history.Predecessor.Aborted, OperationID: history.Predecessor.operationID,
		}, history.Source, checkpoints, legacyIntents, typedIntents, progress, legacyTerminals, typedTerminals})
}

type gatewayRebindRecoveryPredecessorDigestInput struct {
	Generation   uint64                  `json:"generation"`
	State        gatewayV2RouteState     `json:"state"`
	Journal      gatewayMigrationJournal `json:"journal"`
	Existing     bool                    `json:"existing"`
	PartialState bool                    `json:"partialState"`
	Retired      bool                    `json:"retired"`
	Aborted      bool                    `json:"aborted"`
	OperationID  string                  `json:"operationId"`
}
