package controller

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	operationGetApplicationLANAccess     = "getApplicationLANAccess"
	operationReserveApplicationLANAccess = "reserveApplicationLANAccess"
	operationApproveApplicationLANAccess = "approveApplicationLANAccess"
	maxLANAccessBodyBytes                = 4 << 10
)

// LANAppAccessService stores desired access and allocations. None of its reads
// are evidence of a live gateway route or a reachable LAN address.
type LANAppAccessService interface {
	ReadAppAccessOperatorSnapshot(context.Context, string) (appaccess.AppAccessOperatorSnapshot, error)
	ReserveAppAccess(context.Context, appaccess.ReserveAppAccessInput) (appaccess.Allocation, bool, error)
	ApproveAppAccess(context.Context, appaccess.ApproveAppAccessInput) (appaccess.AppAccessRevision, bool, error)
}

type lanAllocationRead struct {
	ID                           string                    `json:"id"`
	AppID                        string                    `json:"appId"`
	Port                         uint16                    `json:"port"`
	OwnerOperationID             string                    `json:"ownerOperationId"`
	GatewayProfileRevisionID     string                    `json:"gatewayProfileRevisionId"`
	GatewayProfileRevisionNumber int64                     `json:"gatewayProfileRevisionNumber"`
	State                        appaccess.AllocationState `json:"state"`
	ReleasedAt                   *time.Time                `json:"releasedAt,omitempty"`
}

type lanAccessRevisionRead struct {
	ID             string            `json:"id"`
	AppID          string            `json:"appId"`
	RevisionNumber int64             `json:"revisionNumber"`
	OperationID    string            `json:"operationId"`
	SpecDigest     string            `json:"specDigest"`
	ApprovedBy     string            `json:"approvedBy"`
	ApprovedAt     time.Time         `json:"approvedAt"`
	Allocation     lanAllocationRead `json:"allocation"`
}

type lanAccessRead struct {
	ExpectedRevisionNumber int64                  `json:"expectedRevisionNumber"`
	DesiredAccess          *lanAccessRevisionRead `json:"desiredAccess,omitempty"`
	PendingReservation     *lanReservationReview  `json:"pendingReservation,omitempty"`
	GrantClaim             *lanGrantClaimRead     `json:"grantClaim,omitempty"`
	DisableClaim           *lanDisableClaimRead   `json:"disableClaim,omitempty"`
	DisableReview          *lanDisableReview      `json:"disableReview,omitempty"`
	Availability           string                 `json:"availability"`
	ObservedAt             *time.Time             `json:"observedAt,omitempty"`
	URL                    string                 `json:"url,omitempty"`
}

type lanReservationReview struct {
	Allocation             lanAllocationRead `json:"allocation"`
	ExpectedRevisionNumber int64             `json:"expectedRevisionNumber"`
	ApprovalDigest         string            `json:"approvalDigest"`
}

type lanDisableReview struct {
	AppID                        string `json:"appId"`
	AccessRevisionID             string `json:"accessRevisionId"`
	AccessRevisionNumber         int64  `json:"accessRevisionNumber"`
	AllocationID                 string `json:"allocationId"`
	OwnerOperationID             string `json:"ownerOperationId"`
	Port                         uint16 `json:"port"`
	GatewayProfileRevisionID     string `json:"gatewayProfileRevisionId"`
	GatewayProfileRevisionNumber int64  `json:"gatewayProfileRevisionNumber"`
	ApprovalDigest               string `json:"approvalDigest"`
}

type lanReservationMutation struct {
	Allocation     lanAllocationRead `json:"allocation"`
	ApprovalDigest string            `json:"approvalDigest"`
	Created        bool              `json:"created"`
}

type lanApprovalMutation struct {
	Revision lanAccessRevisionRead `json:"revision"`
	Created  bool                  `json:"created"`
}

func contractLANAllocation(value appaccess.Allocation) lanAllocationRead {
	return lanAllocationRead{
		ID: value.ID, AppID: value.AppID, Port: value.Port, OwnerOperationID: value.OwnerOperationID,
		GatewayProfileRevisionID:     value.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: value.GatewayProfileRevisionNumber,
		State:                        value.State, ReleasedAt: value.ReleasedAt,
	}
}

func contractLANAccessRevision(value appaccess.AppAccessRevision) lanAccessRevisionRead {
	return lanAccessRevisionRead{
		ID: value.ID, AppID: value.AppID, RevisionNumber: value.RevisionNumber,
		OperationID: value.OperationID, SpecDigest: value.SpecDigest,
		ApprovedBy: value.ApprovedBy, ApprovedAt: value.ApprovedAt,
		Allocation: contractLANAllocation(value.Allocation),
	}
}

func (s *Server) getApplicationLANAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetApplicationLANAccess, "lan_access_forbidden", "Administrator access is required to inspect LAN application access") {
		return
	}
	if r.URL.RawQuery != "" {
		s.lanAccessProblem(w, r, operationGetApplicationLANAccess, appaccess.ErrInvalidInput)
		return
	}
	if !s.appExists(w, r) {
		return
	}
	if !s.GeneratedRuntime || s.AppAccess == nil {
		s.lanAccessUnavailable(w, r, operationGetApplicationLANAccess)
		return
	}
	snapshot, err := s.AppAccess.ReadAppAccessOperatorSnapshot(r.Context(), r.PathValue("appId"))
	if err != nil {
		s.lanAccessProblem(w, r, operationGetApplicationLANAccess, err)
		return
	}
	result := lanAccessRead{ExpectedRevisionNumber: snapshot.ExpectedRevisionNumber, Availability: "local_only"}
	if snapshot.PendingReservation != nil {
		result.PendingReservation = &lanReservationReview{
			Allocation:             contractLANAllocation(snapshot.PendingReservation.Allocation),
			ExpectedRevisionNumber: snapshot.PendingReservation.ExpectedRevisionNumber,
			ApprovalDigest:         snapshot.PendingReservation.ApprovalDigest,
		}
	}
	if snapshot.GrantClaim != nil {
		claim := contractLANGrantClaim(*snapshot.GrantClaim)
		result.GrantClaim = &claim
	}
	if snapshot.DisableClaim != nil {
		claim := contractLANDisableClaim(*snapshot.DisableClaim)
		result.DisableClaim = &claim
	}
	if snapshot.DisableReview != nil {
		spec := snapshot.DisableReview.Spec
		result.DisableReview = &lanDisableReview{
			AppID: spec.AppID, AccessRevisionID: spec.AccessRevisionID,
			AccessRevisionNumber: spec.AccessRevisionNumber, AllocationID: spec.AllocationID,
			OwnerOperationID: spec.OwnerOperationID, Port: spec.Port,
			GatewayProfileRevisionID:     spec.GatewayProfileRevisionID,
			GatewayProfileRevisionNumber: spec.GatewayProfileRevisionNumber,
			ApprovalDigest:               snapshot.DisableReview.ApprovalDigest,
		}
	}
	if snapshot.DesiredAccess != nil {
		value := contractLANAccessRevision(*snapshot.DesiredAccess)
		result.DesiredAccess = &value
		result.Availability = "unverified"
		if snapshot.DisableClaim == nil {
			s.attestApplicationLANAccess(r.Context(), *snapshot.DesiredAccess, &result)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) attestApplicationLANAccess(ctx context.Context, revision appaccess.AppAccessRevision, result *lanAccessRead) {
	if revision.Allocation.State != appaccess.AllocationActive || s.AppGrants == nil || s.LANGrantRuntime == nil ||
		s.GeneratedRuntimeState == nil || s.Deployments == nil {
		return
	}
	owner := appaccess.AllocationOwner{AllocationID: revision.Allocation.ID, AppID: revision.AppID,
		OperationID: revision.OperationID, AccessRevisionID: revision.ID}
	claim, err := s.AppGrants.CurrentAppAccessGrantClaim(ctx, owner)
	if err != nil || claim.State != appaccess.AppAccessGrantCommitted || claim.Spec.AccessRevisionID != revision.ID ||
		claim.Spec.AccessSpecDigest != revision.SpecDigest {
		return
	}
	request := lanGrantRequestForClaim(claim)
	err = s.LANGrantRuntime.WithGatewayV2LANObservation(ctx, request, func(observationCtx context.Context, observation generatedingress.GatewayV2LANGrantObservation) error {
		if observation.Request != request || observation.Disposition != generatedingress.GatewayV2LANGrantCommitted ||
			observation.ActivationUncertain || observation.ObservedAt.IsZero() {
			return appaccess.ErrInvalidStoredState
		}
		authorized, err := s.AppGrants.AuthorizeAppAccessGrant(observationCtx, appaccess.AppAccessGrantAuthorizationInput{
			Owner: appaccess.AppAccessGrantClaimOwnerFor(claim), PermittedStates: []appaccess.AppAccessGrantState{appaccess.AppAccessGrantCommitted},
		})
		if err != nil || authorized.Claim.RequestDigest != claim.RequestDigest || authorized.Claim.StateSequence != claim.StateSequence ||
			authorized.Revision.ID != revision.ID || authorized.Allocation.ID != revision.Allocation.ID || authorized.Allocation.State != appaccess.AllocationActive ||
			lanGrantRequestForClaim(authorized.Claim) != request {
			return appaccess.ErrInvalidStoredState
		}
		effective := authorized.EffectiveProfile
		effectiveDigest, err := appaccess.GatewayProfileSpecDigest(effective.Spec)
		if err != nil || !validCanonicalUUID(effective.ID) || effective.RevisionNumber <= 0 || effective.SpecDigest != effectiveDigest ||
			revision.Allocation.Port < effective.Spec.PortStart || revision.Allocation.Port > effective.Spec.PortEnd {
			return appaccess.ErrInvalidStoredState
		}
		if !observation.EffectiveBinding.MatchesResolution(appaccess.GatewayBindingResolution{
			RawAllocation: authorized.Allocation, RawAccessRevision: authorized.Revision,
			RawGrant: authorized.Claim, RawProfile: authorized.Profile, EffectiveProfile: effective,
			CurrentGatewaySource: authorized.CurrentGatewaySource, TransferChain: authorized.TransferChain,
			TransferChainTipDigest: authorized.TransferChainTipDigest, TerminalReceiptDigest: authorized.TerminalReceiptDigest,
		}) {
			return appaccess.ErrInvalidStoredState
		}
		head, err := s.GeneratedRuntimeState.Active(observationCtx, revision.AppID)
		if err != nil || head.DeploymentID == "" {
			return appaccess.ErrInvalidStoredState
		}
		runtimeDeployment, err := s.GeneratedRuntimeState.Get(observationCtx, revision.AppID, head.DeploymentID)
		if err != nil {
			return err
		}
		deployment, err := s.Deployments.Get(observationCtx, revision.AppID, head.DeploymentID)
		if err != nil || !localRouteProvenanceMatches(revision.AppID, head, runtimeDeployment, deployment) ||
			!appRouteObservationMatches(revision.AppID, head, runtimeDeployment, observation.Slot, observation.Endpoints) {
			return appaccess.ErrInvalidStoredState
		}
		confirmedHead, err := s.GeneratedRuntimeState.Active(observationCtx, revision.AppID)
		if err != nil || confirmedHead.Generation != head.Generation || confirmedHead.DeploymentID != head.DeploymentID ||
			confirmedHead.ReleaseID != head.ReleaseID || confirmedHead.Slot != head.Slot {
			return appaccess.ErrInvalidStoredState
		}
		at := observation.ObservedAt.UTC()
		result.Availability = "verified"
		result.ObservedAt = &at
		result.URL = "http://" + net.JoinHostPort(effective.Spec.SelectedIPv4, strconv.FormatUint(uint64(revision.Allocation.Port), 10)) + "/"
		return nil
	})
	if err != nil || result.Availability != "verified" {
		result.Availability = "unverified"
		result.URL = ""
		result.ObservedAt = nil
	}
}

func (s *Server) reserveApplicationLANAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationReserveApplicationLANAccess, "lan_access_forbidden", "Administrator access is required to reserve LAN access") {
		return
	}
	if !s.appExists(w, r) {
		return
	}
	if !s.GeneratedRuntime || s.AppAccess == nil {
		s.lanAccessUnavailable(w, r, operationReserveApplicationLANAccess)
		return
	}
	var body struct {
		OperationID                  string
		ExpectedRevisionNumber       int64
		GatewayProfileRevisionID     string
		GatewayProfileRevisionNumber int64
	}
	if r.URL.RawQuery != "" || readLANAccessObject(r, []string{"operationId", "expectedRevisionNumber", "gatewayProfileRevisionId", "gatewayProfileRevisionNumber"},
		[]any{&body.OperationID, &body.ExpectedRevisionNumber, &body.GatewayProfileRevisionID, &body.GatewayProfileRevisionNumber}) != nil ||
		!validCanonicalUUID(body.OperationID) || !validCanonicalUUID(body.GatewayProfileRevisionID) || body.ExpectedRevisionNumber < 0 || body.GatewayProfileRevisionNumber <= 0 {
		s.lanAccessProblem(w, r, operationReserveApplicationLANAccess, appaccess.ErrInvalidInput)
		return
	}
	allocation, created, err := s.AppAccess.ReserveAppAccess(r.Context(), appaccess.ReserveAppAccessInput{
		AppID: r.PathValue("appId"), OperationID: body.OperationID,
		ExpectedRevisionNumber:       body.ExpectedRevisionNumber,
		GatewayProfileRevisionID:     body.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: body.GatewayProfileRevisionNumber,
	})
	if err != nil {
		s.lanAccessProblem(w, r, operationReserveApplicationLANAccess, err)
		return
	}
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		s.lanAccessProblem(w, r, operationReserveApplicationLANAccess, appaccess.ErrInvalidStoredState)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, lanReservationMutation{Allocation: contractLANAllocation(allocation), ApprovalDigest: digest, Created: created})
}

func (s *Server) approveApplicationLANAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationApproveApplicationLANAccess, "lan_access_forbidden", "Administrator access is required to approve LAN access") {
		return
	}
	if !s.appExists(w, r) {
		return
	}
	if !s.GeneratedRuntime || s.AppAccess == nil {
		s.lanAccessUnavailable(w, r, operationApproveApplicationLANAccess)
		return
	}
	var body struct {
		OperationID            string
		AllocationID           string
		ExpectedRevisionNumber int64
		ApprovalDigest         string
	}
	if r.URL.RawQuery != "" || readLANAccessObject(r, []string{"operationId", "allocationId", "expectedRevisionNumber", "approvalDigest"},
		[]any{&body.OperationID, &body.AllocationID, &body.ExpectedRevisionNumber, &body.ApprovalDigest}) != nil ||
		!validCanonicalUUID(body.OperationID) || !validCanonicalUUID(body.AllocationID) || body.ExpectedRevisionNumber < 0 || !validLowerHex(body.ApprovalDigest, 64) {
		s.lanAccessProblem(w, r, operationApproveApplicationLANAccess, appaccess.ErrInvalidInput)
		return
	}
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	revision, created, err := s.AppAccess.ApproveAppAccess(r.Context(), appaccess.ApproveAppAccessInput{
		AppID: r.PathValue("appId"), OperationID: body.OperationID, AllocationID: body.AllocationID,
		ExpectedRevisionNumber: body.ExpectedRevisionNumber,
		Approval:               appaccess.Approval{Action: appaccess.ActionEnableAppAccess, SpecDigest: body.ApprovalDigest, ActorID: actor},
	})
	if err != nil {
		s.lanAccessProblem(w, r, operationApproveApplicationLANAccess, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, lanApprovalMutation{Revision: contractLANAccessRevision(revision), Created: created})
}

func readLANAccessObject(r *http.Request, fields []string, values []any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") || len(fields) != len(values) {
		return appaccess.ErrInvalidInput
	}
	defer r.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxLANAccessBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxLANAccessBodyBytes {
		return appaccess.ErrInvalidInput
	}
	decoded, err := decodeRequiredGatewayObject(payload, fields...)
	if err != nil {
		return err
	}
	for i, field := range fields {
		if err := decodeRequiredGatewayScalar(decoded[field], values[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) lanAccessUnavailable(w http.ResponseWriter, r *http.Request, operation string) {
	s.handlerProblem(w, r, operation, http.StatusConflict, "capability_unavailable", "LAN application access requires the generated runtime", 0)
}

func (s *Server) lanAccessProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, appaccess.ErrInvalidInput):
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_lan_access_request", "Invalid LAN application access request", 0)
	case errors.Is(err, appaccess.ErrIdempotencyMismatch):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_access_replay_conflict", "LAN access operation identifier was reused with different input", 0)
	case errors.Is(err, appaccess.ErrApprovalRequired):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_access_approval_mismatch", "Confirmation does not approve this exact LAN application access", 0)
	case errors.Is(err, appaccess.ErrPoolExhausted):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_access_pool_exhausted", "No LAN gateway port is available for this application", 0)
	case errors.Is(err, appaccess.ErrConflict), errors.Is(err, appaccess.ErrReservationReleased), errors.Is(err, appaccess.ErrInvalidTransition), errors.Is(err, appaccess.ErrNotFound):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_access_conflict", "LAN access state changed or the reservation is unavailable", 0)
	default:
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "lan_access_unavailable", "LAN application access state is unavailable", 0)
	}
}
