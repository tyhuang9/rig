package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindProofStatus describes whether a purpose-bound physical proof
// was established for this response. SQL history alone never verifies one.
type GatewayRebindProofStatus string

const (
	GatewayRebindProofUnknown     GatewayRebindProofStatus = "unknown"
	GatewayRebindProofVerified    GatewayRebindProofStatus = "verified"
	GatewayRebindProofUnavailable GatewayRebindProofStatus = "unavailable"
)

// GatewayRebindOperationStatus is the durable SQL projection for one retained
// operation. It remains historical when a later operation becomes current.
type GatewayRebindOperationStatus struct {
	OperationID            string
	StoredState            appaccess.GatewayRebindState
	StateSequence          int64
	DatabaseCommitObserved bool
	TerminalDisposition    appaccess.GatewayRebindTerminalDisposition
	TerminalReceiptDigest  string
}

// GatewayRebindCommitResponse separates retained operation history from the
// selected current authority and from physical proof. Neither SQL projection
// grants effect authority.
type GatewayRebindCommitResponse struct {
	Created                  bool
	RecoveryRequired         bool
	Operation                GatewayRebindOperationStatus
	SelectedCurrentAuthority *appaccess.GatewayCurrentAuthorityRef
	TerminalProof            GatewayRebindProofStatus
	TerminalProofDigest      string
	CurrentProof             GatewayRebindProofStatus
	CurrentAttestationDigest string
	FenceReleased            bool
}

type gatewayRebindCommitReplayProjection struct {
	Claim           appaccess.GatewayRebindClaimV2
	History         appaccess.GatewayRebindHistoryEntry
	TerminalCommand *appaccess.GatewayRebindTransitionCommand
	Response        GatewayRebindCommitResponse
}

// readGatewayRebindCommitReplay classifies a request against a repository-
// validated recovery snapshot. It is read-only: it does not claim, recover,
// abort, inspect protected state, or perform a Docker effect.
func readGatewayRebindCommitReplay(ctx context.Context, snapshot appaccess.GatewayRebindRecoverySnapshot,
	request GatewayRebindCommitRequest, actorID string,
) (gatewayRebindCommitReplayProjection, bool, error) {
	zero := gatewayRebindCommitReplayProjection{}
	if ctx == nil || ctx.Err() != nil {
		return zero, false, gatewayRebindProposalError(ctx)
	}
	if request.Version != gatewayRebindCommitReviewVersion ||
		!validCanonicalUUID(request.Proposal.OperationID) || !validCanonicalUUID(actorID) {
		return zero, false, &Error{Code: DiagnosticValidationFailed}
	}

	match := -1
	for index, entry := range snapshot.History {
		operationID, err := gatewayRebindHistoryOperationID(entry)
		if err != nil || !validCanonicalUUID(operationID) {
			return zero, false, gatewayRebindProposalError(ctx)
		}
		if operationID == request.Proposal.OperationID {
			if match >= 0 {
				return zero, false, gatewayRebindProposalError(ctx)
			}
			match = index
		}
	}
	if match < 0 {
		return zero, false, nil
	}

	history := snapshot.History[match]
	if history.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		history.Claim.Legacy != nil || history.Claim.V2 == nil {
		return zero, false, appaccess.ErrConflict
	}
	claim := *history.Claim.V2
	if !gatewayRebindRetainedClaimMatchesRequest(claim, history, request, actorID) {
		return zero, false, appaccess.ErrConflict
	}
	if !gatewayRebindRetainedHistoryShapeValid(claim, history) {
		return zero, false, gatewayRebindProposalError(ctx)
	}

	projection := gatewayRebindCommitReplayProjection{Claim: claim, History: history}
	projection.Response.Operation = GatewayRebindOperationStatus{
		OperationID: request.Proposal.OperationID, StoredState: claim.State,
		StateSequence: claim.StateSequence,
	}
	projection.Response.TerminalProof = GatewayRebindProofUnknown
	projection.Response.CurrentProof = GatewayRebindProofUnknown

	activeMatch := false
	if snapshot.Active != nil {
		activeOperationID, err := gatewayRebindHistoryOperationID(*snapshot.Active)
		if err != nil || !validCanonicalUUID(activeOperationID) {
			return zero, false, gatewayRebindProposalError(ctx)
		}
		activeMatch = activeOperationID == request.Proposal.OperationID
	}
	if gatewayRebindCommitActiveState(claim.State) {
		databaseEvent, databaseCommitted, eventOK := gatewayRebindCommitDatabaseEvent(history)
		if !activeMatch || !reflect.DeepEqual(history, *snapshot.Active) ||
			snapshot.Phase != claim.State || !eventOK ||
			snapshot.DatabaseCommitObserved != databaseCommitted ||
			(databaseCommitted && (snapshot.DatabaseCommittedEvent == nil ||
				!reflect.DeepEqual(*snapshot.DatabaseCommittedEvent, databaseEvent))) ||
			(!databaseCommitted && snapshot.DatabaseCommittedEvent != nil) {
			return zero, false, gatewayRebindProposalError(ctx)
		}
		projection.Response.RecoveryRequired = true
		projection.Response.Operation.DatabaseCommitObserved = databaseCommitted
		return projection, true, nil
	}
	if activeMatch {
		return zero, false, gatewayRebindProposalError(ctx)
	}

	command, databaseCommitted, ok := gatewayRebindCommitTerminalProjection(claim, history)
	if !ok {
		return zero, false, gatewayRebindProposalError(ctx)
	}
	projection.TerminalCommand = &command
	projection.Response.Operation.DatabaseCommitObserved = databaseCommitted
	projection.Response.Operation.TerminalDisposition = command.TerminalDisposition
	projection.Response.Operation.TerminalReceiptDigest = command.TerminalReceiptDigest
	projection.Response.TerminalProof = GatewayRebindProofUnavailable
	projection.Response.CurrentProof = GatewayRebindProofUnavailable
	projection.Response.FenceReleased = true
	if snapshot.CurrentSource != nil {
		current := *snapshot.CurrentSource
		projection.Response.SelectedCurrentAuthority = &current
	}
	return projection, true, nil
}

func gatewayRebindRetainedClaimMatchesRequest(claim appaccess.GatewayRebindClaimV2,
	history appaccess.GatewayRebindHistoryEntry, request GatewayRebindCommitRequest, actorID string,
) bool {
	inspection := GatewayRebindProposalInspection{
		Spec: claim.Spec, Roster: append([]appaccess.GatewayRebindRosterEntryV2{}, history.RosterV2...),
		RuntimeHeads:                append([]appaccess.GatewayRebindRuntimeHead{}, history.RuntimeHeads...),
		SpecDigest:                  claim.RebindApproval.SpecDigest,
		SuccessorProfileSpecDigest:  claim.ConfigureApproval.SpecDigest,
		ProtectedGeneration:         claim.Spec.SuccessorProtectedGeneration,
		PredecessorCheckpointDigest: claim.Spec.Predecessor.PredecessorCheckpointDigest,
		SourceStateVersion:          claim.Spec.Predecessor.SourceStateVersion,
		SourceStateRevision:         claim.Spec.Predecessor.SourceStateRevision,
		SourceStateDigest:           claim.Spec.Predecessor.SourceStateDigest,
	}
	review, err := newGatewayRebindCommitReview(inspection)
	if err != nil {
		return false
	}
	wantProposal := GatewayRebindProposalInput{
		OperationID:                    claim.Spec.OperationID,
		SuccessorProfileRevisionID:     claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    claim.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               claim.Spec.SuccessorProfile,
	}
	return reflect.DeepEqual(request.Proposal, wantProposal) &&
		request.ProposalDigest == review.ProposalDigest &&
		request.ConfigureApprovalDigest == review.ConfigureApprovalDigest &&
		request.RebindApprovalDigest == review.RebindApprovalDigest &&
		claim.RebindApproval.Action == appaccess.ActionRebindGateway &&
		claim.ConfigureApproval.Action == appaccess.ActionConfigureGateway &&
		claim.RebindApproval.ActorID == actorID && claim.ConfigureApproval.ActorID == actorID &&
		claim.RebindApproval.SpecDigest == review.RebindApprovalDigest &&
		claim.ConfigureApproval.SpecDigest == review.ConfigureApprovalDigest &&
		validSHA256(claim.RequestDigest) && validSHA256(claim.SuccessorProfileRequestDigest)
}

func gatewayRebindRetainedHistoryShapeValid(claim appaccess.GatewayRebindClaimV2,
	history appaccess.GatewayRebindHistoryEntry,
) bool {
	if claim.StateSequence < 1 || len(history.Events) != int(claim.StateSequence) ||
		len(history.Commands) != len(history.Events)-1 || len(history.RosterV1) != 0 ||
		claim.CreatedAt.IsZero() || claim.UpdatedAt.Before(claim.CreatedAt) {
		return false
	}
	for index, event := range history.Events {
		sequence := int64(index + 1)
		if event.OperationID != claim.Spec.OperationID || event.Sequence != sequence {
			return false
		}
		if index == 0 {
			if event.State != appaccess.GatewayRebindPrepared || !event.CreatedAt.Equal(claim.CreatedAt) {
				return false
			}
			continue
		}
		command := history.Commands[index-1]
		if command.OperationID != claim.Spec.OperationID || command.Sequence != sequence ||
			command.PreviousSequence != sequence-1 || command.PreviousState != history.Events[index-1].State ||
			command.NextState != event.State || !command.CreatedAt.Equal(event.CreatedAt) ||
			!validSHA256(command.ProtectedRecordDigest) || !validSHA256(command.CommandDigest) ||
			command.CanonicalPayload == "" {
			return false
		}
	}
	last := history.Events[len(history.Events)-1]
	return last.State == claim.State && last.Sequence == claim.StateSequence &&
		last.CreatedAt.Equal(claim.UpdatedAt)
}

func gatewayRebindCommitActiveState(state appaccess.GatewayRebindState) bool {
	switch state {
	case appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady,
		appaccess.GatewayRebindDatabaseCommitted, appaccess.GatewayRebindUnresolved:
		return true
	default:
		return false
	}
}

func gatewayRebindCommitTerminalProjection(claim appaccess.GatewayRebindClaimV2,
	history appaccess.GatewayRebindHistoryEntry,
) (appaccess.GatewayRebindTransitionCommand, bool, bool) {
	if claim.State != appaccess.GatewayRebindCommitted && claim.State != appaccess.GatewayRebindRolledBack {
		return appaccess.GatewayRebindTransitionCommand{}, false, false
	}
	_, databaseCommitted, eventOK := gatewayRebindCommitDatabaseEvent(history)
	if !eventOK || databaseCommitted != (claim.State == appaccess.GatewayRebindCommitted) || len(history.Commands) == 0 {
		return appaccess.GatewayRebindTransitionCommand{}, false, false
	}
	command := history.Commands[len(history.Commands)-1]
	wantDisposition := appaccess.GatewayRebindDispositionCommit
	if claim.State == appaccess.GatewayRebindRolledBack {
		wantDisposition = appaccess.GatewayRebindDispositionAbort
	}
	if command.Sequence != claim.StateSequence || command.NextState != claim.State ||
		command.TerminalDisposition != wantDisposition || !validSHA256(command.TerminalReceiptDigest) {
		return appaccess.GatewayRebindTransitionCommand{}, false, false
	}
	for index := range history.Commands[:len(history.Commands)-1] {
		prior := history.Commands[index]
		if prior.TerminalDisposition == appaccess.GatewayRebindDispositionNone {
			if prior.TerminalReceiptDigest != "" {
				return appaccess.GatewayRebindTransitionCommand{}, false, false
			}
			continue
		}
		if prior.TerminalDisposition != wantDisposition ||
			prior.TerminalReceiptDigest != command.TerminalReceiptDigest {
			return appaccess.GatewayRebindTransitionCommand{}, false, false
		}
	}
	return command, databaseCommitted, true
}

func gatewayRebindCommitDatabaseEvent(history appaccess.GatewayRebindHistoryEntry) (
	appaccess.GatewayRebindEvent, bool, bool,
) {
	var result appaccess.GatewayRebindEvent
	found := false
	for _, event := range history.Events {
		if event.State != appaccess.GatewayRebindDatabaseCommitted {
			continue
		}
		if found {
			return appaccess.GatewayRebindEvent{}, false, false
		}
		result, found = event, true
	}
	return result, found, true
}
