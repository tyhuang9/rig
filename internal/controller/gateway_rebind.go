package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	operationInspectLANGatewayRebind = "inspectLANGatewayRebind"
	operationCommitLANGatewayRebind  = "commitLANGatewayRebind"
)

// GatewayRebindRuntime owns admission, writer locks and physical proof. These
// handlers are dormant: no route or production capability is registered here.
type GatewayRebindRuntime interface {
	InspectGatewayRebindCommitReview(context.Context, generatedingress.GatewayRebindProposalRepository, generatedingress.GatewayRebindProposalInput) (generatedingress.GatewayRebindCommitReview, error)
	CommitGatewayRebind(context.Context, generatedingress.GatewayRebindCommitRepository, generatedingress.GatewayRebindCommitRequest, string, func(context.Context) error) (generatedingress.GatewayRebindCommitResponse, error)
}

// The proposal uses the existing versioned public spec and roster formats. It
// omits internal inspections, protected paths, session data and physical proof.
// These private wire types will enter the generated API contract with routes.
type lanGatewayRebindReview struct {
	Version                 int                                    `json:"version"`
	Spec                    appaccess.GatewayRebindSpecV2          `json:"spec"`
	Roster                  []appaccess.GatewayRebindRosterEntryV2 `json:"roster"`
	RuntimeHeads            []lanGatewayRebindRuntimeHead          `json:"runtimeHeads"`
	ProposalDigest          string                                 `json:"proposalDigest"`
	ConfigureApprovalDigest string                                 `json:"configureApprovalDigest"`
	RebindApprovalDigest    string                                 `json:"rebindApprovalDigest"`
}

type lanGatewayRebindRuntimeHead struct {
	AppID        string `json:"appId"`
	DeploymentID string `json:"deploymentId"`
	ReleaseID    string `json:"releaseId"`
	Slot         string `json:"slot"`
	Generation   int64  `json:"generation"`
	UpdatedAt    string `json:"updatedAt"`
}

type lanGatewayRebindOperation struct {
	OperationID            string `json:"operationId"`
	StoredState            string `json:"storedState"`
	StateSequence          int64  `json:"stateSequence"`
	DatabaseCommitObserved bool   `json:"databaseCommitObserved"`
	TerminalDisposition    string `json:"terminalDisposition,omitempty"`
	TerminalReceiptDigest  string `json:"terminalReceiptDigest,omitempty"`
}

type lanGatewayRebindCurrentAuthority struct {
	Kind                  string `json:"kind"`
	OperationID           string `json:"operationId"`
	ProfileRevisionID     string `json:"profileRevisionId"`
	ProfileRevisionNumber int64  `json:"profileRevisionNumber"`
	ProfileSpecDigest     string `json:"profileSpecDigest"`
	TerminalReceiptDigest string `json:"terminalReceiptDigest,omitempty"`
}

type lanGatewayRebindMutation struct {
	Created                  bool                              `json:"created"`
	RecoveryRequired         bool                              `json:"recoveryRequired"`
	Operation                lanGatewayRebindOperation         `json:"operation"`
	TerminalProof            string                            `json:"terminalProof"`
	TerminalProofDigest      string                            `json:"terminalProofDigest,omitempty"`
	SelectedCurrentAuthority *lanGatewayRebindCurrentAuthority `json:"selectedCurrentAuthority,omitempty"`
	CurrentProof             string                            `json:"currentProof"`
	CurrentAttestationDigest string                            `json:"currentAttestationDigest,omitempty"`
	FenceReleased            bool                              `json:"fenceReleased"`
}

func (s *Server) inspectLANGatewayRebind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, guard, ok := s.gatewayRebindRequestGuard(w, r, operationInspectLANGatewayRebind)
	if !ok {
		return
	}
	input, err := readLANGatewayRebindProposalRequest(r)
	if err != nil {
		s.gatewayRebindProblem(w, r, operationInspectLANGatewayRebind, err)
		return
	}
	review, err := s.GatewayRebindRuntime.InspectGatewayRebindCommitReview(r.Context(), s.GatewayRebinds, input)
	if err == nil {
		err = guard(r.Context())
	}
	if err != nil {
		s.gatewayRebindProblem(w, r, operationInspectLANGatewayRebind, err)
		return
	}
	spec := review.Inspection.Spec
	returnedInput := generatedingress.GatewayRebindProposalInput{
		OperationID: spec.OperationID, SuccessorProfileRevisionID: spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    spec.SuccessorProfileOperationID, SuccessorProfile: spec.SuccessorProfile,
	}
	if generatedingress.ValidateGatewayRebindCommitReview(review) != nil || returnedInput != input {
		s.gatewayRebindProblem(w, r, operationInspectLANGatewayRebind, appaccess.ErrInvalidStoredState)
		return
	}
	result := lanGatewayRebindReview{
		Version: review.Version, Spec: review.Inspection.Spec,
		Roster:         append([]appaccess.GatewayRebindRosterEntryV2{}, review.Inspection.Roster...),
		RuntimeHeads:   make([]lanGatewayRebindRuntimeHead, 0, len(review.Inspection.RuntimeHeads)),
		ProposalDigest: review.ProposalDigest, ConfigureApprovalDigest: review.ConfigureApprovalDigest,
		RebindApprovalDigest: review.RebindApprovalDigest,
	}
	for _, head := range review.Inspection.RuntimeHeads {
		result.RuntimeHeads = append(result.RuntimeHeads, lanGatewayRebindRuntimeHead{
			AppID: head.AppID, DeploymentID: head.DeploymentID, ReleaseID: head.ReleaseID,
			Slot: head.Slot, Generation: head.Generation, UpdatedAt: contractTime(head.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) commitLANGatewayRebind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, guard, ok := s.gatewayRebindRequestGuard(w, r, operationCommitLANGatewayRebind)
	if !ok {
		return
	}
	body, err := readLANGatewayRebindCommitRequest(r)
	if err != nil {
		s.gatewayRebindProblem(w, r, operationCommitLANGatewayRebind, err)
		return
	}
	result, err := s.GatewayRebindRuntime.CommitGatewayRebind(r.Context(), s.GatewayRebinds,
		generatedingress.GatewayRebindCommitRequest{
			Version: body.Version, Proposal: body.Proposal, ProposalDigest: body.ProposalDigest,
			ConfigureApprovalDigest: body.ConfigureApprovalDigest, RebindApprovalDigest: body.RebindApprovalDigest,
		}, actor, guard)
	if err == nil {
		err = guard(r.Context())
	}
	if err != nil {
		s.gatewayRebindProblem(w, r, operationCommitLANGatewayRebind, err)
		return
	}
	if !validLANGatewayRebindMutation(result, body.Proposal.OperationID) {
		s.gatewayRebindProblem(w, r, operationCommitLANGatewayRebind, appaccess.ErrInvalidStoredState)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, contractLANGatewayRebindMutation(result))
}

func (s *Server) gatewayRebindRequestGuard(w http.ResponseWriter, r *http.Request, operation string) (string, func(context.Context) error, bool) {
	if !s.requireAdministrator(w, r, operation, "lan_gateway_forbidden", "Administrator access is required") {
		return "", nil, false
	}
	if !s.GeneratedRuntime || s.RecoveryOnly || s.GatewayRebinds == nil || s.GatewayRebindRuntime == nil {
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "capability_unavailable", "LAN gateway rebind is unavailable", 0)
		return "", nil, false
	}
	actor, guard, err := s.bindLANGatewayRebindSession(r)
	if err != nil {
		s.gatewayRebindProblem(w, r, operation, err)
		return "", nil, false
	}
	return actor, guard, true
}

func validLANGatewayRebindMutation(value generatedingress.GatewayRebindCommitResponse, operationID string) bool {
	if value.Operation.OperationID != operationID || value.Operation.StateSequence < 1 ||
		!validLANGatewayRebindProof(value.TerminalProof, value.TerminalProofDigest) ||
		!validLANGatewayRebindProof(value.CurrentProof, value.CurrentAttestationDigest) {
		return false
	}
	if value.RecoveryRequired {
		switch value.Operation.StoredState {
		case appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady:
			if value.Operation.DatabaseCommitObserved {
				return false
			}
		case appaccess.GatewayRebindDatabaseCommitted:
			if !value.Operation.DatabaseCommitObserved {
				return false
			}
		case appaccess.GatewayRebindUnresolved:
		default:
			return false
		}
		return !value.Created && !value.FenceReleased && value.Operation.TerminalDisposition == "" &&
			value.Operation.TerminalReceiptDigest == "" && value.SelectedCurrentAuthority == nil &&
			value.TerminalProof == generatedingress.GatewayRebindProofUnknown && value.CurrentProof == generatedingress.GatewayRebindProofUnknown
	}
	committed := value.Operation.StoredState == appaccess.GatewayRebindCommitted &&
		value.Operation.TerminalDisposition == appaccess.GatewayRebindDispositionCommit && value.Operation.DatabaseCommitObserved
	rolledBack := value.Operation.StoredState == appaccess.GatewayRebindRolledBack &&
		value.Operation.TerminalDisposition == appaccess.GatewayRebindDispositionAbort && !value.Operation.DatabaseCommitObserved
	if (!committed && !rolledBack) || !value.FenceReleased || !validLowerHex(value.Operation.TerminalReceiptDigest, 64) {
		return false
	}
	if value.TerminalProof == generatedingress.GatewayRebindProofUnknown || value.CurrentProof == generatedingress.GatewayRebindProofUnknown ||
		(value.SelectedCurrentAuthority != nil && !validLANGatewayRebindCurrentAuthority(*value.SelectedCurrentAuthority)) {
		return false
	}
	if value.CurrentProof == generatedingress.GatewayRebindProofVerified && value.SelectedCurrentAuthority == nil {
		return false
	}
	return !value.Created || (value.TerminalProof == generatedingress.GatewayRebindProofVerified &&
		value.CurrentProof == generatedingress.GatewayRebindProofVerified)
}

func validLANGatewayRebindCurrentAuthority(value appaccess.GatewayCurrentAuthorityRef) bool {
	if !validCanonicalUUID(value.OperationID) || !validCanonicalUUID(value.ProfileRevisionID) ||
		value.ProfileRevisionNumber < 1 || !validLowerHex(value.ProfileSpecDigest, 64) {
		return false
	}
	switch value.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return value.TerminalReceiptDigest == ""
	case appaccess.GatewayRebindSourceGatewayRebind:
		return validLowerHex(value.TerminalReceiptDigest, 64)
	default:
		return false
	}
}

func validLANGatewayRebindProof(status generatedingress.GatewayRebindProofStatus, digest string) bool {
	switch status {
	case generatedingress.GatewayRebindProofVerified:
		return validLowerHex(digest, 64)
	case generatedingress.GatewayRebindProofUnknown, generatedingress.GatewayRebindProofUnavailable:
		return digest == ""
	default:
		return false
	}
}

func contractLANGatewayRebindMutation(value generatedingress.GatewayRebindCommitResponse) lanGatewayRebindMutation {
	result := lanGatewayRebindMutation{
		Created: value.Created, RecoveryRequired: value.RecoveryRequired,
		Operation: lanGatewayRebindOperation{
			OperationID: value.Operation.OperationID, StoredState: string(value.Operation.StoredState),
			StateSequence: value.Operation.StateSequence, DatabaseCommitObserved: value.Operation.DatabaseCommitObserved,
			TerminalDisposition: string(value.Operation.TerminalDisposition), TerminalReceiptDigest: value.Operation.TerminalReceiptDigest,
		},
		TerminalProof: string(value.TerminalProof), TerminalProofDigest: value.TerminalProofDigest,
		CurrentProof: string(value.CurrentProof), CurrentAttestationDigest: value.CurrentAttestationDigest,
		FenceReleased: value.FenceReleased,
	}
	if current := value.SelectedCurrentAuthority; current != nil {
		result.SelectedCurrentAuthority = &lanGatewayRebindCurrentAuthority{
			Kind: string(current.Kind), OperationID: current.OperationID, ProfileRevisionID: current.ProfileRevisionID,
			ProfileRevisionNumber: current.ProfileRevisionNumber, ProfileSpecDigest: current.ProfileSpecDigest,
			TerminalReceiptDigest: current.TerminalReceiptDigest,
		}
	}
	return result
}

func (s *Server) gatewayRebindProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	var oversized *http.MaxBytesError
	switch {
	case errors.As(err, &oversized):
		s.handlerProblem(w, r, operation, http.StatusRequestEntityTooLarge, "invalid_lan_gateway_request", "LAN gateway request is too large", 0)
	case errors.Is(err, appaccess.ErrInvalidInput):
		s.handlerProblem(w, r, operation, http.StatusBadRequest, "invalid_lan_gateway_request", "Invalid LAN gateway rebind request", 0)
	case errors.Is(err, appaccess.ErrApprovalRequired):
		s.handlerProblem(w, r, operation, http.StatusForbidden, "lan_gateway_approval_mismatch", "Current administrator confirmation is required", 0)
	case errors.Is(err, appaccess.ErrIdempotencyMismatch), errors.Is(err, appaccess.ErrConflict):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_conflict", "LAN gateway review or operation changed", 0)
	default:
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "lan_gateway_unavailable", "LAN gateway rebind state is unavailable or requires reconciliation", 0)
	}
}
