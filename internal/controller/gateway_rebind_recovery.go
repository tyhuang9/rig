package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	operationGetLANGatewayRebind             = "getLANGatewayRebind"
	operationInspectLANGatewayRebindRecovery = "inspectLANGatewayRebindRecovery"
	operationReconcileLANGatewayRebind       = "reconcileLANGatewayRebind"
)

// GatewayRebindRecoveryRuntime owns protected-state inspection, lock ordering,
// physical proof and deterministic reconciliation. Recovery-only requests are
// additionally restricted to the startup-validated kind and operation pin.
type GatewayRebindRecoveryRuntime interface {
	InspectGatewayRebindOperation(context.Context, generatedingress.GatewayRebindRecoveryReadRepository, string) (generatedingress.GatewayRebindOperationInspection, error)
	InspectGatewayRebindRecoveryReview(context.Context, generatedingress.GatewayRebindRecoveryReadRepository, string, string, func(context.Context) error) (generatedingress.GatewayRebindRecoveryReview, error)
	ReconcileGatewayRebind(context.Context, generatedingress.GatewayRebindRecoveryRepository, generatedingress.GatewayRebindReconcileRequest, string, func(context.Context) error) (generatedingress.GatewayRebindReconcileResponse, error)
}

// These private wire projections intentionally omit the raw SQL event,
// protected digests other than the terminal receipt, authorization review and
// audit receipt. None of those values grants client authority.
type lanGatewayRebindStatus struct {
	Version                  int                                `json:"version"`
	OperationID              string                             `json:"operationId"`
	Availability             string                             `json:"availability"`
	Active                   bool                               `json:"active"`
	Operation                *lanGatewayRebindOperation         `json:"operation,omitempty"`
	Protected                *lanGatewayRebindProtectedRecovery `json:"protected,omitempty"`
	HistoricalTerminal       bool                               `json:"historicalTerminal"`
	SelectedCurrentAuthority *lanGatewayRebindCurrentAuthority  `json:"selectedCurrentAuthority,omitempty"`
	TerminalProof            string                             `json:"terminalProof"`
	TerminalProofDigest      string                             `json:"terminalProofDigest,omitempty"`
	CurrentProof             string                             `json:"currentProof"`
	CurrentAttestationDigest string                             `json:"currentAttestationDigest,omitempty"`
	OperationFence           string                             `json:"operationFence"`
	RecoveryPolicy           string                             `json:"recoveryPolicy,omitempty"`
	AllowedActions           []string                           `json:"allowedActions"`
}

type lanGatewayRebindProtectedRecovery struct {
	Presence              string `json:"presence"`
	Generation            uint64 `json:"generation"`
	Phase                 string `json:"phase,omitempty"`
	RecordSequence        uint64 `json:"recordSequence"`
	FixedDisposition      string `json:"fixedDisposition,omitempty"`
	TerminalReceiptDigest string `json:"terminalReceiptDigest,omitempty"`
}

type lanGatewayRebindRecoveryReview struct {
	OperationID            string                 `json:"operationId"`
	Version                int                    `json:"version"`
	Status                 lanGatewayRebindStatus `json:"status"`
	RecoveryDigest         string                 `json:"recoveryDigest,omitempty"`
	RecoveryApprovalDigest string                 `json:"recoveryApprovalDigest,omitempty"`
}

type lanGatewayRebindReconcileResponse struct {
	Version int                    `json:"version"`
	Status  lanGatewayRebindStatus `json:"status"`
}

func (s *Server) getLANGatewayRebind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.gatewayRebindRecoveryCapability(w, r, operationGetLANGatewayRebind) {
		return
	}
	operationID, err := readLANGatewayRebindOperationPath(r, http.MethodGet)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationGetLANGatewayRebind, err)
		return
	}
	if !s.gatewayRebindRecoveryOperationAllowed(w, r, operationGetLANGatewayRebind, operationID) {
		return
	}
	if err := readLANGatewayRebindStatusBody(r); err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationGetLANGatewayRebind, err)
		return
	}
	_, guard, err := s.bindLANGatewayRebindReadSession(r)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationGetLANGatewayRebind, err)
		return
	}
	result, err := s.GatewayRebindRecoveryRuntime.InspectGatewayRebindOperation(
		r.Context(), s.GatewayRebindRecoveries, operationID)
	if err == nil {
		err = guard(r.Context())
	}
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationGetLANGatewayRebind, err)
		return
	}
	if err := generatedingress.ValidateGatewayRebindOperationInspection(result, operationID); err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationGetLANGatewayRebind, err)
		return
	}
	if result.Availability == generatedingress.GatewayRebindOperationValidatedAbsent {
		s.handlerProblem(w, r, operationGetLANGatewayRebind, http.StatusNotFound,
			"lan_gateway_rebind_not_found", "LAN gateway rebind operation was not found", 0)
		return
	}
	if result.Availability == generatedingress.GatewayRebindOperationUnavailable {
		s.gatewayRebindRecoveryProblem(w, r, operationGetLANGatewayRebind, appaccess.ErrInvalidStoredState)
		return
	}
	writeJSON(w, http.StatusOK, contractLANGatewayRebindStatus(result))
}

func (s *Server) inspectLANGatewayRebindRecovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.gatewayRebindRecoveryCapability(w, r, operationInspectLANGatewayRebindRecovery) {
		return
	}
	operationID, err := readLANGatewayRebindOperationPath(r, http.MethodPost)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationInspectLANGatewayRebindRecovery, err)
		return
	}
	if !s.gatewayRebindRecoveryOperationAllowed(w, r, operationInspectLANGatewayRebindRecovery, operationID) {
		return
	}
	if err := readLANGatewayRebindRecoveryPreviewBody(r); err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationInspectLANGatewayRebindRecovery, err)
		return
	}
	actorID, guard, err := s.bindLANGatewayRebindSession(r)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationInspectLANGatewayRebindRecovery, err)
		return
	}
	result, err := s.GatewayRebindRecoveryRuntime.InspectGatewayRebindRecoveryReview(
		r.Context(), s.GatewayRebindRecoveries, operationID, actorID, guard)
	if err == nil {
		err = guard(r.Context())
	}
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationInspectLANGatewayRebindRecovery, err)
		return
	}
	if err := generatedingress.ValidateGatewayRebindRecoveryReview(result, operationID, actorID); err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationInspectLANGatewayRebindRecovery, err)
		return
	}
	if result.Status.Availability == generatedingress.GatewayRebindOperationValidatedAbsent {
		s.handlerProblem(w, r, operationInspectLANGatewayRebindRecovery, http.StatusNotFound,
			"lan_gateway_rebind_not_found", "LAN gateway rebind operation was not found", 0)
		return
	}
	if result.Status.Availability == generatedingress.GatewayRebindOperationUnavailable {
		s.gatewayRebindRecoveryProblem(w, r, operationInspectLANGatewayRebindRecovery, appaccess.ErrInvalidStoredState)
		return
	}
	writeJSON(w, http.StatusOK, lanGatewayRebindRecoveryReview{
		OperationID: operationID, Version: result.Version, Status: contractLANGatewayRebindStatus(result.Status),
		RecoveryDigest:         result.AuthorizationReview.RecoveryDigest,
		RecoveryApprovalDigest: result.AuthorizationReview.RecoveryApprovalDigest,
	})
}

func (s *Server) reconcileLANGatewayRebind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.gatewayRebindRecoveryCapability(w, r, operationReconcileLANGatewayRebind) {
		return
	}
	operationID, err := readLANGatewayRebindOperationPath(r, http.MethodPost)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationReconcileLANGatewayRebind, err)
		return
	}
	if !s.gatewayRebindRecoveryOperationAllowed(w, r, operationReconcileLANGatewayRebind, operationID) {
		return
	}
	body, err := readLANGatewayRebindReconcileRequest(r, operationID)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationReconcileLANGatewayRebind, err)
		return
	}
	actorID, guard, err := s.bindLANGatewayRebindSession(r)
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationReconcileLANGatewayRebind, err)
		return
	}
	result, err := s.GatewayRebindRecoveryRuntime.ReconcileGatewayRebind(r.Context(), s.GatewayRebindRecoveries,
		generatedingress.GatewayRebindReconcileRequest{
			Version: body.Version, OperationID: body.OperationID,
			RecoveryDigest: body.RecoveryDigest, RecoveryApprovalDigest: body.RecoveryApprovalDigest,
		}, actorID, guard)
	if err == nil {
		err = guard(r.Context())
	}
	if err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationReconcileLANGatewayRebind, err)
		return
	}
	request := generatedingress.GatewayRebindReconcileRequest{
		Version: body.Version, OperationID: body.OperationID,
		RecoveryDigest: body.RecoveryDigest, RecoveryApprovalDigest: body.RecoveryApprovalDigest,
	}
	if err := generatedingress.ValidateGatewayRebindReconcileResponse(result, request, actorID); err != nil {
		s.gatewayRebindRecoveryProblem(w, r, operationReconcileLANGatewayRebind, err)
		return
	}
	if result.Status.Availability != generatedingress.GatewayRebindOperationAvailable {
		s.gatewayRebindRecoveryProblem(w, r, operationReconcileLANGatewayRebind, appaccess.ErrInvalidStoredState)
		return
	}
	writeJSON(w, http.StatusOK, lanGatewayRebindReconcileResponse{
		Version: result.Version, Status: contractLANGatewayRebindStatus(result.Status),
	})
}

func (s *Server) gatewayRebindRecoveryCapability(w http.ResponseWriter, r *http.Request, operation string) bool {
	if !s.requireAdministrator(w, r, operation, "lan_gateway_forbidden", "Administrator access is required") {
		return false
	}
	if s.RecoveryOnly && (s.RecoveryKind != RecoveryGatewayRebind || !validCanonicalUUID(s.RecoveryOperationID)) {
		s.gatewayRebindRecoveryProblem(w, r, operation, appaccess.ErrConflict)
		return false
	}
	if !s.GeneratedRuntime || s.GatewayRebindRecoveries == nil || s.GatewayRebindRecoveryRuntime == nil {
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable,
			"capability_unavailable", "LAN gateway rebind recovery is unavailable", 0)
		return false
	}
	return true
}

// gatewayRebindRecoveryOperationAllowed binds recovery-only HTTP authority to
// the one operation validated during startup. This check deliberately runs
// before session binding and every repository, runtime, audit or effect call.
// Normal mode may inspect or reconcile any exact retained operation using the
// same fresh runtime validation and interactive authorization requirements.
func (s *Server) gatewayRebindRecoveryOperationAllowed(w http.ResponseWriter, r *http.Request,
	operation, operationID string,
) bool {
	if !s.RecoveryOnly {
		return true
	}
	if s.RecoveryKind != RecoveryGatewayRebind || !validCanonicalUUID(s.RecoveryOperationID) ||
		operationID != s.RecoveryOperationID {
		s.gatewayRebindRecoveryProblem(w, r, operation, appaccess.ErrConflict)
		return false
	}
	return true
}

func contractLANGatewayRebindStatus(value generatedingress.GatewayRebindOperationInspection) lanGatewayRebindStatus {
	result := lanGatewayRebindStatus{
		Version: value.Version, OperationID: value.OperationID,
		Availability: string(value.Availability), Active: value.Active,
		HistoricalTerminal: value.HistoricalTerminal, TerminalProof: string(value.TerminalProof),
		TerminalProofDigest: value.TerminalProofDigest, CurrentProof: string(value.CurrentProof),
		CurrentAttestationDigest: value.CurrentAttestationDigest, OperationFence: string(value.OperationFence),
		RecoveryPolicy: string(value.RecoveryPolicy), AllowedActions: make([]string, 0, len(value.AllowedActions)),
	}
	if value.Operation.OperationID != "" {
		result.Operation = &lanGatewayRebindOperation{
			OperationID: value.Operation.OperationID, StoredState: string(value.Operation.StoredState),
			StateSequence: value.Operation.StateSequence, DatabaseCommitObserved: value.Operation.DatabaseCommitObserved,
			TerminalDisposition:   string(value.Operation.TerminalDisposition),
			TerminalReceiptDigest: value.Operation.TerminalReceiptDigest,
		}
	}
	if protected := value.Protected; protected != nil {
		result.Protected = &lanGatewayRebindProtectedRecovery{
			Presence: string(protected.Presence), Generation: protected.Generation,
			Phase: value.ProtectedPhase, RecordSequence: protected.RecordSequence,
			FixedDisposition:      string(protected.FixedDisposition),
			TerminalReceiptDigest: protected.TerminalReceiptDigest,
		}
	}
	if current := value.SelectedCurrentAuthority; current != nil {
		result.SelectedCurrentAuthority = &lanGatewayRebindCurrentAuthority{
			Kind: string(current.Kind), OperationID: current.OperationID,
			ProfileRevisionID: current.ProfileRevisionID, ProfileRevisionNumber: current.ProfileRevisionNumber,
			ProfileSpecDigest: current.ProfileSpecDigest, TerminalReceiptDigest: current.TerminalReceiptDigest,
		}
	}
	for _, action := range value.AllowedActions {
		result.AllowedActions = append(result.AllowedActions, string(action))
	}
	return result
}

func (s *Server) gatewayRebindRecoveryProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	var oversized *http.MaxBytesError
	switch {
	case errors.As(err, &oversized):
		s.handlerProblem(w, r, operation, http.StatusRequestEntityTooLarge,
			"invalid_lan_gateway_request", "LAN gateway request is too large", 0)
	case errors.Is(err, appaccess.ErrInvalidInput):
		s.handlerProblem(w, r, operation, http.StatusBadRequest,
			"invalid_lan_gateway_request", "Invalid LAN gateway rebind recovery request", 0)
	case errors.Is(err, appaccess.ErrApprovalRequired):
		s.handlerProblem(w, r, operation, http.StatusForbidden,
			"lan_gateway_approval_mismatch", "Current administrator confirmation is required", 0)
	case errors.Is(err, appaccess.ErrIdempotencyMismatch), errors.Is(err, appaccess.ErrConflict):
		s.handlerProblem(w, r, operation, http.StatusConflict,
			"lan_gateway_conflict", "LAN gateway rebind recovery review or operation changed", 0)
	default:
		// ErrNotFound is deliberately included here. Only a validated pure
		// inspection can establish absence; an arbitrary storage error cannot.
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable,
			"lan_gateway_unavailable", "LAN gateway rebind state is unavailable or requires reconciliation", 0)
	}
}
