package controller

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	operationGetApplicationLANGrant    = "getApplicationLANGrant"
	operationGrantApplicationLANAccess = "grantApplicationLANAccess"
)

type LANAppGrantService interface {
	CurrentAppAccess(context.Context, string) (appaccess.AppAccessRevision, error)
	CurrentGatewayProfile(context.Context) (appaccess.GatewayProfileRevision, error)
	ClaimAppAccessGrant(context.Context, appaccess.ClaimAppAccessGrantInput) (appaccess.AppAccessGrantClaim, bool, error)
	AuthorizeAppAccessGrant(context.Context, appaccess.AppAccessGrantAuthorizationInput) (appaccess.AppAccessGrantAuthorization, error)
	AdvanceAppAccessGrantClaim(context.Context, appaccess.AppAccessGrantClaimOwner, appaccess.AppAccessGrantState, appaccess.AppAccessGrantState) (appaccess.AppAccessGrantClaim, bool, error)
	ResolveAppAccessGrantClaim(context.Context, appaccess.AppAccessGrantClaimOwner, appaccess.AppAccessGrantState, appaccess.AppAccessGrantState, appaccess.AppAccessGrantProof) (appaccess.AppAccessGrantClaim, bool, error)
	AppAccessGrantClaim(context.Context, string) (appaccess.AppAccessGrantClaim, error)
	CurrentAppAccessGrantClaim(context.Context, appaccess.AllocationOwner) (appaccess.AppAccessGrantClaim, error)
}

type LANAppGrantRuntime interface {
	GrantGatewayV2LAN(context.Context, generatedingress.GatewayV2LANGrantRequest, generatedingress.GatewayV2LANGrantAuthorizer) (generatedingress.GatewayV2LANGrantResult, error)
	ObserveGatewayV2LAN(context.Context, generatedingress.GatewayV2LANGrantRequest) (generatedingress.GatewayV2LANGrantObservation, error)
	WithGatewayV2LANObservation(context.Context, generatedingress.GatewayV2LANGrantRequest, func(context.Context, generatedingress.GatewayV2LANGrantObservation) error) error
	WithGatewayV2LANCommitResolution(context.Context, generatedingress.GatewayV2LANGrantRequest, func(context.Context, generatedingress.GatewayV2LANGrantReceipt) error) error
	WithGatewayV2LANCommitRecovery(context.Context, generatedingress.GatewayV2LANGrantRequest, func(context.Context, generatedingress.GatewayV2LANGrantObservation) error) error
	WithGatewayV2LANAbsenceResolution(context.Context, generatedingress.GatewayV2LANGrantRequest, func(context.Context, generatedingress.GatewayV2LANGrantObservation) error) error
	WithGatewayV2LANRollbackResolution(context.Context, generatedingress.GatewayV2LANGrantRequest, func(context.Context, generatedingress.GatewayV2LANGrantObservation) error) error
}

type lanGrantClaimRead struct {
	AttemptID            string                        `json:"attemptId"`
	AppID                string                        `json:"appId"`
	AllocationID         string                        `json:"allocationId"`
	OwnerOperationID     string                        `json:"ownerOperationId"`
	AccessRevisionID     string                        `json:"accessRevisionId"`
	AccessRevisionNumber int64                         `json:"accessRevisionNumber"`
	Port                 uint16                        `json:"port"`
	State                appaccess.AppAccessGrantState `json:"state"`
	UpdatedAt            time.Time                     `json:"updatedAt"`
}

type lanGrantObservationRead struct {
	Availability string     `json:"availability"`
	ObservedAt   *time.Time `json:"observedAt,omitempty"`
}

type lanGrantRead struct {
	Claim    lanGrantClaimRead       `json:"claim"`
	Observed lanGrantObservationRead `json:"observed"`
}

type lanGrantMutation struct {
	Claim   lanGrantClaimRead `json:"claim"`
	Created bool              `json:"created"`
}

func contractLANGrantClaim(claim appaccess.AppAccessGrantClaim) lanGrantClaimRead {
	return lanGrantClaimRead{
		AttemptID: claim.AttemptID, AppID: claim.Spec.AppID,
		AllocationID: claim.Spec.AllocationID, OwnerOperationID: claim.Spec.OwnerOperationID,
		AccessRevisionID: claim.Spec.AccessRevisionID, AccessRevisionNumber: claim.Spec.AccessRevisionNumber,
		Port: claim.Spec.Port, State: claim.State, UpdatedAt: claim.UpdatedAt,
	}
}

func lanGrantRequestForClaim(claim appaccess.AppAccessGrantClaim) generatedingress.GatewayV2LANGrantRequest {
	return generatedingress.GatewayV2LANGrantRequest{
		AttemptID: claim.AttemptID, ClaimRequestDigest: claim.RequestDigest,
		AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID, Port: claim.Spec.Port,
		AccessRevisionID: claim.Spec.AccessRevisionID, AccessRevisionNumber: claim.Spec.AccessRevisionNumber,
		AccessSpecDigest: claim.Spec.AccessSpecDigest, ApprovedBy: claim.Spec.ApprovedBy,
		GatewayProfileRevisionID:     claim.Spec.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: claim.Spec.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     claim.Spec.GatewayProfileSpecDigest,
	}
}

func (s *Server) lanGrantAvailable() bool {
	return s != nil && s.GeneratedRuntime && s.AppGrants != nil && s.LANGrantRuntime != nil
}

func (s *Server) pinnedLANGrant(appID, attemptID string) bool {
	return !s.RecoveryOnly || (s.RecoveryKind == RecoveryLANGrant &&
		s.RecoveryOperationID != "" && s.RecoveryOperationID == attemptID &&
		s.RecoveryAppID != "" && s.RecoveryAppID == appID)
}

func (s *Server) getApplicationLANGrant(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetApplicationLANGrant, "lan_access_forbidden", "Administrator access is required to inspect LAN grant state") {
		return
	}
	appID, attemptID := r.PathValue("appId"), r.PathValue("attemptId")
	if r.URL.RawQuery != "" || !validCanonicalUUID(appID) || !validCanonicalUUID(attemptID) {
		s.lanAccessProblem(w, r, operationGetApplicationLANGrant, appaccess.ErrInvalidInput)
		return
	}
	if !s.pinnedLANGrant(appID, attemptID) {
		s.lanGrantRecoveryOnlyProblem(w, r)
		return
	}
	if !s.RecoveryOnly && !s.appExists(w, r) {
		return
	}
	if !s.lanGrantAvailable() {
		s.lanAccessUnavailable(w, r, operationGetApplicationLANGrant)
		return
	}
	claim, err := s.AppGrants.AppAccessGrantClaim(r.Context(), attemptID)
	if err != nil || claim.Spec.AppID != appID {
		s.lanAccessProblem(w, r, operationGetApplicationLANGrant, firstGatewayUpgradeError(err, appaccess.ErrNotFound))
		return
	}
	response := lanGrantRead{Claim: contractLANGrantClaim(claim), Observed: lanGrantObservationRead{Availability: "unknown"}}
	observed, err := s.LANGrantRuntime.ObserveGatewayV2LAN(r.Context(), lanGrantRequestForClaim(claim))
	if err == nil && observed.Request == lanGrantRequestForClaim(claim) && observed.Disposition != "" && !observed.ObservedAt.IsZero() {
		response.Observed.Availability = string(observed.Disposition)
		at := observed.ObservedAt.UTC()
		response.Observed.ObservedAt = &at
	}
	confirmed, err := s.AppGrants.AppAccessGrantClaim(r.Context(), attemptID)
	if err != nil || confirmed.StateSequence != claim.StateSequence || confirmed.RequestDigest != claim.RequestDigest || confirmed.State != claim.State {
		s.lanAccessProblem(w, r, operationGetApplicationLANGrant, appaccess.ErrInvalidStoredState)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) grantApplicationLANAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGrantApplicationLANAccess, "lan_access_forbidden", "Administrator access is required to share an application on LAN") {
		return
	}
	appID := r.PathValue("appId")
	var body struct {
		AttemptID            string
		AccessRevisionID     string
		AccessRevisionNumber int64
		ApprovalDigest       string
	}
	if r.URL.RawQuery != "" || readLANAccessObject(r,
		[]string{"attemptId", "accessRevisionId", "accessRevisionNumber", "approvalDigest"},
		[]any{&body.AttemptID, &body.AccessRevisionID, &body.AccessRevisionNumber, &body.ApprovalDigest}) != nil ||
		!validCanonicalUUID(appID) || !validCanonicalUUID(body.AttemptID) || !validCanonicalUUID(body.AccessRevisionID) ||
		body.AccessRevisionNumber <= 0 || !validLowerHex(body.ApprovalDigest, 64) {
		s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, appaccess.ErrInvalidInput)
		return
	}
	if !s.pinnedLANGrant(appID, body.AttemptID) {
		s.lanGrantRecoveryOnlyProblem(w, r)
		return
	}
	if !s.RecoveryOnly && !s.appExists(w, r) {
		return
	}
	if !s.lanGrantAvailable() {
		s.lanAccessUnavailable(w, r, operationGrantApplicationLANAccess)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || sessionCookie.Value == "" {
		s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, appaccess.ErrApprovalRequired)
		return
	}
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	var claim appaccess.AppAccessGrantClaim
	var created bool
	if s.RecoveryOnly {
		claim, err = s.AppGrants.AppAccessGrantClaim(r.Context(), body.AttemptID)
		if err != nil || claim.Spec.AppID != appID || claim.Spec.AccessRevisionID != body.AccessRevisionID ||
			claim.Spec.AccessRevisionNumber != body.AccessRevisionNumber ||
			subtle.ConstantTimeCompare([]byte(claim.Spec.AccessSpecDigest), []byte(body.ApprovalDigest)) != 1 {
			s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, firstGatewayUpgradeError(err, appaccess.ErrConflict))
			return
		}
	} else {
		current, readErr := s.AppGrants.CurrentAppAccess(r.Context(), appID)
		if readErr != nil || current.ID != body.AccessRevisionID || current.RevisionNumber != body.AccessRevisionNumber {
			s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, firstGatewayUpgradeError(readErr, appaccess.ErrConflict))
			return
		}
		if subtle.ConstantTimeCompare([]byte(current.SpecDigest), []byte(body.ApprovalDigest)) != 1 {
			s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, appaccess.ErrApprovalRequired)
			return
		}
		profile, readErr := s.AppGrants.CurrentGatewayProfile(r.Context())
		if readErr != nil {
			s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, readErr)
			return
		}
		claim, created, err = s.AppGrants.ClaimAppAccessGrant(r.Context(), appaccess.ClaimAppAccessGrantInput{
			AttemptID: body.AttemptID, Spec: appaccess.AppAccessGrantSpecFor(current, profile), ActorID: actor,
		})
		if err != nil {
			s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, err)
			return
		}
	}
	owner := appaccess.AppAccessGrantClaimOwnerFor(claim)
	owner.ActorID = actor
	request := lanGrantRequestForClaim(claim)
	grantSucceeded := false
	if !s.RecoveryOnly && claim.State == appaccess.AppAccessGrantPrepared {
		claim, _, err = s.AppGrants.AdvanceAppAccessGrantClaim(r.Context(), owner, appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying)
		if err != nil {
			s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, err)
			return
		}
		authorize := func(ctx context.Context, candidate generatedingress.GatewayV2LANGrantRequest) (generatedingress.GatewayV2LANGrantAuthorizationLease, error) {
			lease := &lanGrantAuthorizationLease{server: s, service: s.AppGrants, owner: owner, expected: request,
				actorID: actor, sessionToken: sessionCookie.Value, csrf: r.Header.Get("X-CSRF-Token")}
			if err := lease.Revalidate(ctx, candidate); err != nil {
				return nil, err
			}
			return lease, nil
		}
		result, grantErr := s.LANGrantRuntime.GrantGatewayV2LAN(r.Context(), request, authorize)
		grantSucceeded = grantErr == nil && result.Receipt.Request == request &&
			result.Receipt.GatewayOperationID != "" && result.Receipt.ProtectedStateDigest != "" && !result.Receipt.ObservedAt.IsZero()
	}
	// Caddy may already serve this port after Grant returns. A disconnected
	// client cannot cancel the mandatory terminal commit or 404 withdrawal.
	reconcileCtx, cancelReconcile := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancelReconcile()
	resolved, err := s.reconcileLANGrant(reconcileCtx, claim, request, grantSucceeded, actor,
		sessionCookie.Value, r.Header.Get("X-CSRF-Token"))
	if err != nil {
		s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, err)
		return
	}
	if resolved.State != appaccess.AppAccessGrantCommitted {
		s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, appaccess.ErrConflict)
		return
	}
	if _, err := s.AppGrants.AuthorizeAppAccessGrant(reconcileCtx, appaccess.AppAccessGrantAuthorizationInput{
		Owner:           appaccess.AppAccessGrantClaimOwnerFor(resolved),
		PermittedStates: []appaccess.AppAccessGrantState{appaccess.AppAccessGrantCommitted},
	}); err != nil {
		s.lanAccessProblem(w, r, operationGrantApplicationLANAccess, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, lanGrantMutation{Claim: contractLANGrantClaim(resolved), Created: created})
}

func (s *Server) reconcileLANGrant(ctx context.Context, claim appaccess.AppAccessGrantClaim,
	request generatedingress.GatewayV2LANGrantRequest, grantSucceeded bool, actorID, sessionToken, csrf string,
) (appaccess.AppAccessGrantClaim, error) {
	// A successful mutation goes straight to locked terminal resolution. An
	// ambiguous result needs a preliminary observation to choose a recovery path.
	var preliminary generatedingress.GatewayV2LANGrantObservation
	var observationErr error
	if !grantSucceeded {
		preliminary, observationErr = s.LANGrantRuntime.ObserveGatewayV2LAN(ctx, request)
	}
	latest := claim
	if !grantSucceeded {
		var latestErr error
		latest, latestErr = s.AppGrants.AppAccessGrantClaim(ctx, claim.AttemptID)
		if latestErr != nil || latest.RequestDigest != claim.RequestDigest || latest.Spec != claim.Spec {
			return appaccess.AppAccessGrantClaim{}, firstGatewayUpgradeError(latestErr, appaccess.ErrInvalidStoredState)
		}
	}
	var resolved appaccess.AppAccessGrantClaim
	resolve := func(lockCtx context.Context, observed generatedingress.GatewayV2LANGrantObservation) error {
		if observed.Request != request || observed.ObservedAt.IsZero() || observed.GatewayOperationID == "" || observed.ProtectedStateDigest == "" {
			return appaccess.ErrInvalidStoredState
		}
		current, err := s.AppGrants.AppAccessGrantClaim(lockCtx, claim.AttemptID)
		if err != nil || current.RequestDigest != claim.RequestDigest || current.Spec != claim.Spec {
			return firstGatewayUpgradeError(err, appaccess.ErrInvalidStoredState)
		}
		if current.State == appaccess.AppAccessGrantCommitted && observed.Disposition == generatedingress.GatewayV2LANGrantCommitted {
			resolved = current
			return nil
		}
		if current.State == appaccess.AppAccessGrantRolledBack && observed.Disposition == generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation {
			resolved = current
			return nil
		}
		owner := appaccess.AppAccessGrantClaimOwnerFor(current)
		owner.ActorID = actorID
		proof := appaccess.AppAccessGrantProof{GatewayOperationID: observed.GatewayOperationID,
			ProtectedStateDigest: observed.ProtectedStateDigest}
		switch observed.Disposition {
		case generatedingress.GatewayV2LANGrantCommitted:
			if current.State != appaccess.AppAccessGrantDBActive && current.State != appaccess.AppAccessGrantUncertain {
				return appaccess.ErrInvalidStoredState
			}
			authorized, authErr := s.AppGrants.AuthorizeAppAccessGrant(lockCtx, appaccess.AppAccessGrantAuthorizationInput{
				Owner: owner, PermittedStates: []appaccess.AppAccessGrantState{current.State},
			})
			if authErr != nil || lanGrantRequestForClaim(authorized.Claim) != request {
				return firstGatewayUpgradeError(authErr, appaccess.ErrConflict)
			}
			resolved, _, err = s.AppGrants.ResolveAppAccessGrantClaim(lockCtx, owner, current.State, appaccess.AppAccessGrantCommitted, proof)
			if err != nil {
				return err
			}
		case generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation:
			if current.State == appaccess.AppAccessGrantDBActive {
				current, _, err = s.AppGrants.AdvanceAppAccessGrantClaim(lockCtx, owner, appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantUncertain)
				if err != nil {
					return err
				}
			}
			if current.State != appaccess.AppAccessGrantApplying && current.State != appaccess.AppAccessGrantUncertain && current.State != appaccess.AppAccessGrantPrepared {
				return appaccess.ErrInvalidStoredState
			}
			resolved, _, err = s.AppGrants.ResolveAppAccessGrantClaim(lockCtx, owner, current.State, appaccess.AppAccessGrantRolledBack, proof)
			if err != nil {
				return err
			}
		default:
			return appaccess.ErrInvalidStoredState
		}
		confirmed, err := s.AppGrants.AppAccessGrantClaim(lockCtx, claim.AttemptID)
		if err != nil || confirmed.RequestDigest != claim.RequestDigest || confirmed.State != resolved.State ||
			confirmed.StateSequence != resolved.StateSequence || confirmed.Proof == nil {
			return appaccess.ErrInvalidStoredState
		}
		return nil
	}
	var err error
	switch {
	case latest.State == appaccess.AppAccessGrantPrepared:
		err = s.LANGrantRuntime.WithGatewayV2LANAbsenceResolution(ctx, request, resolve)
	case grantSucceeded || (observationErr == nil && preliminary.Request == request && preliminary.Disposition == generatedingress.GatewayV2LANGrantCommitted):
		err = s.LANGrantRuntime.WithGatewayV2LANCommitResolution(ctx, request, func(lockCtx context.Context, receipt generatedingress.GatewayV2LANGrantReceipt) error {
			return resolve(lockCtx, generatedingress.GatewayV2LANGrantObservation{
				Request: receipt.Request, Disposition: generatedingress.GatewayV2LANGrantCommitted,
				GatewayOperationID: receipt.GatewayOperationID, ProtectedStateDigest: receipt.ProtectedStateDigest,
				ObservedAt: receipt.ObservedAt,
			})
		})
	case observationErr == nil && preliminary.Request == request && latest.State == appaccess.AppAccessGrantCommitted:
		err = s.LANGrantRuntime.WithGatewayV2LANCommitRecovery(ctx, request, func(lockCtx context.Context, observed generatedingress.GatewayV2LANGrantObservation) error {
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
				return appaccess.ErrApprovalRequired
			}
			if observed.Request != request || observed.Disposition != generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation || observed.ObservedAt.IsZero() {
				return appaccess.ErrInvalidStoredState
			}
			current, readErr := s.AppGrants.AppAccessGrantClaim(lockCtx, claim.AttemptID)
			if readErr != nil || current.State != appaccess.AppAccessGrantCommitted || current.Proof == nil ||
				current.RequestDigest != claim.RequestDigest || current.Proof.GatewayOperationID != observed.GatewayOperationID {
				return firstGatewayUpgradeError(readErr, appaccess.ErrInvalidStoredState)
			}
			authorized, authErr := s.AppGrants.AuthorizeAppAccessGrant(lockCtx, appaccess.AppAccessGrantAuthorizationInput{
				Owner:           appaccess.AppAccessGrantClaimOwnerFor(current),
				PermittedStates: []appaccess.AppAccessGrantState{appaccess.AppAccessGrantCommitted},
			})
			if authErr != nil || lanGrantRequestForClaim(authorized.Claim) != request {
				return firstGatewayUpgradeError(authErr, appaccess.ErrConflict)
			}
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
				return appaccess.ErrApprovalRequired
			}
			resolved = current
			return nil
		})
	case observationErr == nil && preliminary.Request == request:
		err = s.LANGrantRuntime.WithGatewayV2LANRollbackResolution(ctx, request, resolve)
	case latest.State == appaccess.AppAccessGrantApplying || latest.State == appaccess.AppAccessGrantRolledBack:
		err = s.LANGrantRuntime.WithGatewayV2LANAbsenceResolution(ctx, request, resolve)
	default:
		return appaccess.AppAccessGrantClaim{}, firstGatewayUpgradeError(observationErr, appaccess.ErrInvalidStoredState)
	}
	if err != nil {
		return appaccess.AppAccessGrantClaim{}, err
	}
	return resolved, nil
}

type lanGrantAuthorizationLease struct {
	server       *Server
	service      LANAppGrantService
	owner        appaccess.AppAccessGrantClaimOwner
	expected     generatedingress.GatewayV2LANGrantRequest
	actorID      string
	sessionToken string
	csrf         string
	released     bool
}

func (lease *lanGrantAuthorizationLease) Revalidate(ctx context.Context, candidate generatedingress.GatewayV2LANGrantRequest) error {
	if lease == nil || lease.released || candidate != lease.expected {
		return appaccess.ErrConflict
	}
	if !lease.server.lanGrantActorCurrent(lease.actorID, lease.sessionToken, lease.csrf) {
		return appaccess.ErrApprovalRequired
	}
	authorized, err := lease.service.AuthorizeAppAccessGrant(ctx, appaccess.AppAccessGrantAuthorizationInput{
		Owner: lease.owner, PermittedStates: []appaccess.AppAccessGrantState{appaccess.AppAccessGrantApplying},
	})
	if err != nil || lanGrantRequestForClaim(authorized.Claim) != candidate {
		return firstGatewayUpgradeError(err, appaccess.ErrConflict)
	}
	return nil
}

func (s *Server) lanGrantActorCurrent(actorID, sessionToken, csrf string) bool {
	if s == nil || s.Auth == nil || actorID == "" || sessionToken == "" || csrf == "" {
		return false
	}
	user, csrfHash, err := s.Auth.Authenticate(sessionToken)
	return err == nil && user.ID == actorID && user.Role == "administrator" && s.Auth.CheckCSRF(csrfHash, csrf)
}

func (lease *lanGrantAuthorizationLease) Activate(ctx context.Context, candidate generatedingress.GatewayV2LANGrantRequest) error {
	if err := lease.Revalidate(ctx, candidate); err != nil {
		return err
	}
	_, _, err := lease.service.AdvanceAppAccessGrantClaim(ctx, lease.owner, appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive)
	return err
}

func (lease *lanGrantAuthorizationLease) Release() error {
	if lease != nil {
		lease.released = true
	}
	return nil
}

func (s *Server) lanGrantRecoveryOnlyProblem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	problem(w, r, http.StatusServiceUnavailable, "gateway_reconciliation_required", "Only the startup LAN grant can be reconciled", nil)
}
