package controller

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	operationGetApplicationLANDisable    = "getApplicationLANDisable"
	operationDisableApplicationLANAccess = "disableApplicationLANAccess"
)

// LANAppDisableService is the durable side of an exact, approved LAN disable.
// Its terminal transaction is the only path that may release an owned port.
type LANAppDisableService interface {
	CurrentAppAccess(context.Context, string) (appaccess.AppAccessRevision, error)
	AppAccessHead(context.Context, string) (appaccess.AppAccessRevision, error)
	ClaimAppAccessDisable(context.Context, appaccess.ApproveAppAccessDisableInput) (appaccess.AppAccessDisableClaim, bool, error)
	AppAccessDisableClaim(context.Context, string) (appaccess.AppAccessDisableClaim, error)
	AuthorizeAppAccessDisable(context.Context, appaccess.AppAccessDisableAuthorizationInput) (appaccess.AppAccessDisableAuthorization, error)
	AdvanceAppAccessDisableClaim(context.Context, appaccess.AppAccessDisableClaimOwner, appaccess.AppAccessDisableState, appaccess.AppAccessDisableState) (appaccess.AppAccessDisableClaim, bool, error)
	ResolveAppAccessDisableClaim(context.Context, appaccess.AppAccessDisableClaimOwner, appaccess.AppAccessDisableState, appaccess.AppAccessDisableState, appaccess.AppAccessDisableProof) (appaccess.AppAccessDisableClaim, bool, error)
	AcknowledgeAppAccessDisableProtectedClear(context.Context, string, string, string, time.Time) (appaccess.AppAccessDisableProtectedClearAck, bool, error)
}

type LANAppDisableRuntime interface {
	DisableGatewayV2LAN(context.Context, generatedingress.GatewayV2LANDisableRequest, generatedingress.GatewayV2LANDisableAuthorizer) (generatedingress.GatewayV2LANDisableResult, error)
	ObserveGatewayV2LANDisable(context.Context, generatedingress.GatewayV2LANDisableRequest) (generatedingress.GatewayV2LANDisableObservation, error)
	WithGatewayV2LANDisableFinalization(context.Context, generatedingress.GatewayV2LANDisableRequest, generatedingress.GatewayV2LANDisableResolutionAuthorizer, func(context.Context, generatedingress.GatewayV2LANDisableObservation) error, func(context.Context, generatedingress.GatewayV2LANDisableObservation) error) error
}

type lanBatchDisableRuntime interface {
	WithGatewayV2LANRecoveryDisableFinalization(context.Context, generatedingress.GatewayV2LANDisableRequest,
		func(context.Context, generatedingress.GatewayV2LANDisableObservation) error,
		func(context.Context, generatedingress.GatewayV2LANDisableObservation) error) error
}

type lanDisableClaimRead struct {
	OperationID          string                          `json:"operationId"`
	AppID                string                          `json:"appId"`
	AllocationID         string                          `json:"allocationId"`
	AccessRevisionID     string                          `json:"accessRevisionId"`
	AccessRevisionNumber int64                           `json:"accessRevisionNumber"`
	Port                 uint16                          `json:"port"`
	ApprovalDigest       string                          `json:"approvalDigest"`
	State                appaccess.AppAccessDisableState `json:"state"`
	UpdatedAt            time.Time                       `json:"updatedAt"`
	ReleasedAt           *time.Time                      `json:"releasedAt,omitempty"`
}

type lanDisableObservationRead struct {
	Availability string     `json:"availability"`
	ObservedAt   *time.Time `json:"observedAt,omitempty"`
}

type lanDisableRead struct {
	Claim    lanDisableClaimRead       `json:"claim"`
	Observed lanDisableObservationRead `json:"observed"`
}

type lanDisableMutation struct {
	Claim   lanDisableClaimRead `json:"claim"`
	Created bool                `json:"created"`
}

func contractLANDisableClaim(claim appaccess.AppAccessDisableClaim) lanDisableClaimRead {
	result := lanDisableClaimRead{
		OperationID: claim.OperationID, AppID: claim.Spec.AppID,
		AllocationID: claim.Spec.AllocationID, AccessRevisionID: claim.Spec.AccessRevisionID,
		AccessRevisionNumber: claim.Spec.AccessRevisionNumber, Port: claim.Spec.Port,
		ApprovalDigest: claim.SpecDigest,
		State:          claim.State, UpdatedAt: claim.UpdatedAt,
	}
	if claim.State == appaccess.AppAccessDisableCommitted && claim.Proof != nil {
		at := claim.Proof.ObservedAt.UTC()
		result.ReleasedAt = &at
	}
	return result
}

func (s *Server) lanDisableAvailable() bool {
	return s != nil && s.GeneratedRuntime && s.AppDisables != nil && s.LANDisableRuntime != nil
}

func (s *Server) pinnedLANDisable(appID, operationID string) bool {
	return !s.RecoveryOnly || (s.RecoveryKind == RecoveryLANDisable &&
		s.RecoveryOperationID == operationID && s.RecoveryAppID == appID)
}

func (s *Server) getApplicationLANDisable(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetApplicationLANDisable, "lan_access_forbidden", "Administrator access is required to inspect LAN disable state") {
		return
	}
	appID, operationID := r.PathValue("appId"), r.PathValue("operationId")
	if r.URL.RawQuery != "" || !validCanonicalUUID(appID) || !validCanonicalUUID(operationID) {
		s.lanDisableProblem(w, r, operationGetApplicationLANDisable, appaccess.ErrInvalidInput)
		return
	}
	if !s.pinnedLANDisable(appID, operationID) {
		s.lanDisableRecoveryOnlyProblem(w, r)
		return
	}
	if !s.RecoveryOnly && !s.appExists(w, r) {
		return
	}
	if !s.lanDisableAvailable() {
		s.lanAccessUnavailable(w, r, operationGetApplicationLANDisable)
		return
	}
	claim, err := s.AppDisables.AppAccessDisableClaim(r.Context(), operationID)
	if err != nil || claim.Spec.AppID != appID {
		s.lanDisableProblem(w, r, operationGetApplicationLANDisable, firstGatewayUpgradeError(err, appaccess.ErrNotFound))
		return
	}
	response := lanDisableRead{Claim: contractLANDisableClaim(claim), Observed: lanDisableObservationRead{Availability: "unknown"}}
	actorID := r.Context().Value(principalKey{}).(principal).user.ID
	request, err := s.lanDisableRequest(r.Context(), claim, actorID)
	if err == nil {
		observed, observeErr := s.LANDisableRuntime.ObserveGatewayV2LANDisable(r.Context(), request)
		if observeErr == nil && observed.Request.OperationID == operationID && observed.ObservedAt.After(time.Time{}) {
			response.Observed.Availability = string(observed.Disposition)
			at := observed.ObservedAt.UTC()
			response.Observed.ObservedAt = &at
		}
	}
	confirmed, err := s.AppDisables.AppAccessDisableClaim(r.Context(), operationID)
	if err != nil || confirmed.StateSequence != claim.StateSequence || confirmed.RequestDigest != claim.RequestDigest {
		s.lanDisableProblem(w, r, operationGetApplicationLANDisable, appaccess.ErrInvalidStoredState)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) disableApplicationLANAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationDisableApplicationLANAccess, "lan_access_forbidden", "Administrator access is required to disable LAN sharing") {
		return
	}
	appID := r.PathValue("appId")
	var body struct {
		OperationID          string
		AccessRevisionID     string
		AccessRevisionNumber int64
		AllocationID         string
		ApprovalDigest       string
	}
	if r.URL.RawQuery != "" || readLANAccessObject(r,
		[]string{"operationId", "accessRevisionId", "accessRevisionNumber", "allocationId", "approvalDigest"},
		[]any{&body.OperationID, &body.AccessRevisionID, &body.AccessRevisionNumber, &body.AllocationID, &body.ApprovalDigest}) != nil ||
		!validCanonicalUUID(appID) || !validCanonicalUUID(body.OperationID) || !validCanonicalUUID(body.AccessRevisionID) ||
		!validCanonicalUUID(body.AllocationID) || body.AccessRevisionNumber <= 0 || !validLowerHex(body.ApprovalDigest, 64) {
		s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, appaccess.ErrInvalidInput)
		return
	}
	if !s.pinnedLANDisable(appID, body.OperationID) {
		s.lanDisableRecoveryOnlyProblem(w, r)
		return
	}
	if !s.RecoveryOnly && !s.appExists(w, r) {
		return
	}
	if !s.lanDisableAvailable() {
		s.lanAccessUnavailable(w, r, operationDisableApplicationLANAccess)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || sessionCookie.Value == "" {
		s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, appaccess.ErrApprovalRequired)
		return
	}
	actorID := r.Context().Value(principalKey{}).(principal).user.ID
	claim, err := s.AppDisables.AppAccessDisableClaim(r.Context(), body.OperationID)
	created := false
	if errors.Is(err, appaccess.ErrNotFound) && !s.RecoveryOnly {
		current, readErr := s.AppDisables.CurrentAppAccess(r.Context(), appID)
		if readErr != nil || current.ID != body.AccessRevisionID || current.RevisionNumber != body.AccessRevisionNumber ||
			current.Allocation.ID != body.AllocationID {
			s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, firstGatewayUpgradeError(readErr, appaccess.ErrConflict))
			return
		}
		specDigest, digestErr := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(current))
		if digestErr != nil || subtle.ConstantTimeCompare([]byte(specDigest), []byte(body.ApprovalDigest)) != 1 {
			s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, appaccess.ErrApprovalRequired)
			return
		}
		claim, created, err = s.AppDisables.ClaimAppAccessDisable(r.Context(), appaccess.ApproveAppAccessDisableInput{
			OperationID: body.OperationID, ExpectedRevisionNumber: body.AccessRevisionNumber,
			Owner: appaccess.AllocationOwner{AllocationID: body.AllocationID, AppID: appID,
				OperationID: current.OperationID, AccessRevisionID: current.ID},
			Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess, SpecDigest: body.ApprovalDigest, ActorID: actorID},
		})
	}
	if err != nil || claim.Spec.AppID != appID || claim.Spec.AccessRevisionID != body.AccessRevisionID ||
		claim.Spec.AccessRevisionNumber != body.AccessRevisionNumber || claim.Spec.AllocationID != body.AllocationID ||
		subtle.ConstantTimeCompare([]byte(claim.SpecDigest), []byte(body.ApprovalDigest)) != 1 {
		s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, firstGatewayUpgradeError(err, appaccess.ErrConflict))
		return
	}
	if claim.State == appaccess.AppAccessDisableCommitted && !s.RecoveryOnly {
		current, currentErr := s.AppDisables.AppAccessHead(r.Context(), appID)
		if currentErr != nil && !errors.Is(currentErr, appaccess.ErrNotFound) {
			s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, currentErr)
			return
		}
		if currentErr == nil && current.ID != claim.Spec.AccessRevisionID {
			owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
			owner.ActorID = actorID
			authorized, authErr := s.AppDisables.AuthorizeAppAccessDisable(r.Context(), appaccess.AppAccessDisableAuthorizationInput{
				Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{appaccess.AppAccessDisableCommitted},
			})
			if authErr != nil || authorized.Claim.RequestDigest != claim.RequestDigest {
				s.lanDisableProblem(w, r, operationDisableApplicationLANAccess,
					firstGatewayUpgradeError(authErr, appaccess.ErrInvalidStoredState))
				return
			}
			writeJSON(w, http.StatusOK, lanDisableMutation{Claim: contractLANDisableClaim(authorized.Claim), Created: false})
			return
		}
	}
	// Withdrawal and terminal reconciliation must continue after the client
	// disconnects. A pending claim keeps the port owned until exact 404 proof.
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	resolved, err := s.reconcileLANDisable(reconcileCtx, claim, actorID, sessionCookie.Value, r.Header.Get("X-CSRF-Token"))
	if err != nil {
		s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, err)
		return
	}
	if resolved.State != appaccess.AppAccessDisableCommitted {
		s.lanDisableProblem(w, r, operationDisableApplicationLANAccess, appaccess.ErrConflict)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, lanDisableMutation{Claim: contractLANDisableClaim(resolved), Created: created})
}

func (s *Server) lanDisableRequest(ctx context.Context, claim appaccess.AppAccessDisableClaim, actorID string) (generatedingress.GatewayV2LANDisableRequest, error) {
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	owner.ActorID = actorID
	authorized, err := s.AppDisables.AuthorizeAppAccessDisable(ctx, appaccess.AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{claim.State},
	})
	if err != nil || authorized.Claim.OperationID != claim.OperationID || authorized.Claim.RequestDigest != claim.RequestDigest {
		return generatedingress.GatewayV2LANDisableRequest{}, firstGatewayUpgradeError(err, appaccess.ErrConflict)
	}
	return lanDisableRequestForAuthorization(claim, authorized), nil
}

func (s *Server) reconcileLANDisable(ctx context.Context, claim appaccess.AppAccessDisableClaim,
	actorID, sessionToken, csrf string,
) (appaccess.AppAccessDisableClaim, error) {
	request, err := s.lanDisableRequest(ctx, claim, actorID)
	if err != nil {
		return appaccess.AppAccessDisableClaim{}, err
	}
	if s.RecoveryOnly && s.RecoveryLANBatch {
		return s.reconcileLANBatchDisable(ctx, claim, request, actorID, sessionToken, csrf)
	}
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	owner.ActorID = actorID
	var withdrawalErr error
	if claim.State == appaccess.AppAccessDisablePrepared {
		lease := &lanDisableAuthorizationLease{
			server: s, service: s.AppDisables, owner: owner, expected: request,
			actorID: actorID, sessionToken: sessionToken, csrf: csrf,
		}
		_, withdrawalErr = s.LANDisableRuntime.DisableGatewayV2LAN(ctx, request,
			func(lockCtx context.Context, candidate generatedingress.GatewayV2LANDisableRequest) (generatedingress.GatewayV2LANDisableAuthorizationLease, error) {
				if err := lease.Revalidate(lockCtx, candidate); err != nil {
					return nil, err
				}
				return lease, nil
			})
	}
	var resolved appaccess.AppAccessDisableClaim
	err = s.LANDisableRuntime.WithGatewayV2LANDisableFinalization(ctx, request,
		func(lockCtx context.Context, candidate generatedingress.GatewayV2LANDisableRequest) error {
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) || !reflect.DeepEqual(candidate, request) {
				return appaccess.ErrApprovalRequired
			}
			current, readErr := s.AppDisables.AppAccessDisableClaim(lockCtx, claim.OperationID)
			if readErr != nil || current.RequestDigest != claim.RequestDigest || current.Spec != claim.Spec {
				return firstGatewayUpgradeError(readErr, appaccess.ErrConflict)
			}
			actual, authErr := s.lanDisableRequest(lockCtx, current, actorID)
			if authErr != nil || !reflect.DeepEqual(actual, request) {
				return firstGatewayUpgradeError(authErr, appaccess.ErrConflict)
			}
			if current.State == appaccess.AppAccessDisablePrepared {
				owner := appaccess.AppAccessDisableClaimOwnerFor(current)
				owner.ActorID = actorID
				if _, _, authErr = s.AppDisables.AdvanceAppAccessDisableClaim(lockCtx, owner,
					appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing); authErr != nil {
					return authErr
				}
			}
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
				return appaccess.ErrApprovalRequired
			}
			return nil
		},
		func(lockCtx context.Context, observation generatedingress.GatewayV2LANDisableObservation) error {
			if !reflect.DeepEqual(observation.Request, request) || observation.ObservedAt.IsZero() ||
				observation.GatewayOperationID == "" || observation.ProtectedStateDigest == "" ||
				observation.Disposition != generatedingress.GatewayV2LANDisableWithdrawnPending &&
					observation.Disposition != generatedingress.GatewayV2LANDisableDisabled {
				return appaccess.ErrInvalidStoredState
			}
			current, readErr := s.AppDisables.AppAccessDisableClaim(lockCtx, claim.OperationID)
			if readErr != nil || current.RequestDigest != claim.RequestDigest || current.Spec != claim.Spec {
				return firstGatewayUpgradeError(readErr, appaccess.ErrInvalidStoredState)
			}
			if current.State == appaccess.AppAccessDisableCommitted {
				if current.Proof == nil || current.Proof.GatewayOperationID != observation.GatewayOperationID ||
					(observation.Disposition == generatedingress.GatewayV2LANDisableWithdrawnPending &&
						current.Proof.ProtectedStateDigest != observation.ProtectedStateDigest) {
					return appaccess.ErrInvalidStoredState
				}
				resolved = current
				return nil
			}
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
				return appaccess.ErrApprovalRequired
			}
			owner := appaccess.AppAccessDisableClaimOwnerFor(current)
			owner.ActorID = actorID
			if current.State == appaccess.AppAccessDisablePrepared {
				current, _, readErr = s.AppDisables.AdvanceAppAccessDisableClaim(lockCtx, owner,
					appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing)
				if readErr != nil {
					return readErr
				}
			}
			authorized, authErr := s.AppDisables.AuthorizeAppAccessDisable(lockCtx, appaccess.AppAccessDisableAuthorizationInput{
				Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{current.State},
			})
			if authErr != nil || authorized.Claim.RequestDigest != claim.RequestDigest {
				return firstGatewayUpgradeError(authErr, appaccess.ErrConflict)
			}
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
				return appaccess.ErrApprovalRequired
			}
			resolved, _, authErr = s.AppDisables.ResolveAppAccessDisableClaim(lockCtx, owner, current.State,
				appaccess.AppAccessDisableCommitted, appaccess.AppAccessDisableProof{
					GatewayOperationID:   observation.GatewayOperationID,
					ProtectedStateDigest: observation.ProtectedStateDigest,
					ObservedAt:           observation.ObservedAt,
				})
			return authErr
		},
		func(lockCtx context.Context, observation generatedingress.GatewayV2LANDisableObservation) error {
			if !reflect.DeepEqual(observation.Request, request) ||
				observation.Disposition != generatedingress.GatewayV2LANDisableDisabled ||
				observation.ObservedAt.IsZero() || observation.GatewayOperationID == "" ||
				observation.ProtectedStateDigest == "" {
				return appaccess.ErrInvalidStoredState
			}
			if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
				return appaccess.ErrApprovalRequired
			}
			current, readErr := s.AppDisables.AppAccessDisableClaim(lockCtx, claim.OperationID)
			if readErr != nil || current.State != appaccess.AppAccessDisableCommitted ||
				current.Proof == nil || current.Proof.GatewayOperationID != observation.GatewayOperationID ||
				current.RequestDigest != claim.RequestDigest || current.Spec != claim.Spec {
				return firstGatewayUpgradeError(readErr, appaccess.ErrInvalidStoredState)
			}
			_, _, ackErr := s.AppDisables.AcknowledgeAppAccessDisableProtectedClear(lockCtx,
				claim.OperationID, observation.GatewayOperationID,
				observation.ProtectedStateDigest, observation.ObservedAt)
			return ackErr
		})
	if err != nil {
		return appaccess.AppAccessDisableClaim{}, firstGatewayUpgradeError(withdrawalErr, err)
	}
	return resolved, nil
}

// The batch gateway has already quarantined every unsafe LAN route before
// this endpoint can serve. Only the exact pinned disable head may commit and
// acknowledge its protected clear; advancing the batch remains ingress-owned.
func (s *Server) reconcileLANBatchDisable(ctx context.Context, claim appaccess.AppAccessDisableClaim,
	request generatedingress.GatewayV2LANDisableRequest, actorID, sessionToken, csrf string,
) (appaccess.AppAccessDisableClaim, error) {
	runtime, ok := s.LANDisableRuntime.(lanBatchDisableRuntime)
	if !ok || !s.RecoveryOnly || !s.RecoveryLANBatch ||
		!s.pinnedLANDisable(claim.Spec.AppID, claim.OperationID) {
		return appaccess.AppAccessDisableClaim{}, appaccess.ErrInvalidStoredState
	}
	var resolved appaccess.AppAccessDisableClaim
	resolve := func(lockCtx context.Context, observation generatedingress.GatewayV2LANDisableObservation) error {
		if !reflect.DeepEqual(observation.Request, request) ||
			observation.Disposition != generatedingress.GatewayV2LANDisableWithdrawnPending ||
			observation.ObservedAt.IsZero() || observation.GatewayOperationID == "" ||
			observation.ProtectedStateDigest == "" {
			return appaccess.ErrInvalidStoredState
		}
		if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
			return appaccess.ErrApprovalRequired
		}
		current, readErr := s.AppDisables.AppAccessDisableClaim(lockCtx, claim.OperationID)
		if readErr != nil || current.RequestDigest != claim.RequestDigest || current.Spec != claim.Spec {
			return firstGatewayUpgradeError(readErr, appaccess.ErrInvalidStoredState)
		}
		actual, authErr := s.lanDisableRequest(lockCtx, current, actorID)
		if authErr != nil || !reflect.DeepEqual(actual, request) {
			return firstGatewayUpgradeError(authErr, appaccess.ErrInvalidStoredState)
		}
		if current.State == appaccess.AppAccessDisableCommitted {
			if current.Proof == nil || current.Proof.GatewayOperationID != observation.GatewayOperationID ||
				current.Proof.ProtectedStateDigest == "" {
				return appaccess.ErrInvalidStoredState
			}
			resolved = current
			return nil
		}
		owner := appaccess.AppAccessDisableClaimOwnerFor(current)
		owner.ActorID = actorID
		if current.State == appaccess.AppAccessDisablePrepared {
			current, _, readErr = s.AppDisables.AdvanceAppAccessDisableClaim(lockCtx, owner,
				appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing)
			if readErr != nil {
				return readErr
			}
		}
		if current.State != appaccess.AppAccessDisableWithdrawing &&
			current.State != appaccess.AppAccessDisableUncertain {
			return appaccess.ErrInvalidStoredState
		}
		authorized, authErr := s.AppDisables.AuthorizeAppAccessDisable(lockCtx, appaccess.AppAccessDisableAuthorizationInput{
			Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{current.State},
		})
		if authErr != nil || authorized.Claim.RequestDigest != claim.RequestDigest ||
			authorized.Claim.Spec != claim.Spec ||
			!reflect.DeepEqual(lanDisableRequestForAuthorization(authorized.Claim, authorized), request) {
			return firstGatewayUpgradeError(authErr, appaccess.ErrInvalidStoredState)
		}
		if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
			return appaccess.ErrApprovalRequired
		}
		resolved, _, readErr = s.AppDisables.ResolveAppAccessDisableClaim(lockCtx, owner, current.State,
			appaccess.AppAccessDisableCommitted, appaccess.AppAccessDisableProof{
				GatewayOperationID:   observation.GatewayOperationID,
				ProtectedStateDigest: observation.ProtectedStateDigest,
				ObservedAt:           observation.ObservedAt,
			})
		if readErr != nil {
			return readErr
		}
		confirmed, readErr := s.AppDisables.AppAccessDisableClaim(lockCtx, claim.OperationID)
		if readErr != nil || confirmed.RequestDigest != claim.RequestDigest || confirmed.Spec != claim.Spec ||
			confirmed.State != appaccess.AppAccessDisableCommitted ||
			confirmed.StateSequence != resolved.StateSequence || confirmed.Proof == nil ||
			confirmed.Proof.GatewayOperationID != observation.GatewayOperationID ||
			confirmed.Proof.ProtectedStateDigest != observation.ProtectedStateDigest {
			return firstGatewayUpgradeError(readErr, appaccess.ErrInvalidStoredState)
		}
		resolved = confirmed
		return nil
	}
	acknowledge := func(lockCtx context.Context, observation generatedingress.GatewayV2LANDisableObservation) error {
		if !reflect.DeepEqual(observation.Request, request) ||
			observation.Disposition != generatedingress.GatewayV2LANDisableDisabled ||
			observation.ObservedAt.IsZero() || observation.GatewayOperationID == "" ||
			observation.ProtectedStateDigest == "" {
			return appaccess.ErrInvalidStoredState
		}
		if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
			return appaccess.ErrApprovalRequired
		}
		current, readErr := s.AppDisables.AppAccessDisableClaim(lockCtx, claim.OperationID)
		if readErr != nil || current.RequestDigest != claim.RequestDigest || current.Spec != claim.Spec ||
			current.State != appaccess.AppAccessDisableCommitted || current.Proof == nil ||
			current.Proof.GatewayOperationID != observation.GatewayOperationID {
			return firstGatewayUpgradeError(readErr, appaccess.ErrInvalidStoredState)
		}
		actual, authErr := s.lanDisableRequest(lockCtx, current, actorID)
		if authErr != nil || !reflect.DeepEqual(actual, request) {
			return firstGatewayUpgradeError(authErr, appaccess.ErrInvalidStoredState)
		}
		if !s.lanGrantActorCurrent(actorID, sessionToken, csrf) {
			return appaccess.ErrApprovalRequired
		}
		ack, _, ackErr := s.AppDisables.AcknowledgeAppAccessDisableProtectedClear(lockCtx,
			claim.OperationID, observation.GatewayOperationID,
			observation.ProtectedStateDigest, observation.ObservedAt)
		if ackErr != nil {
			return ackErr
		}
		if ack.OperationID != claim.OperationID || ack.GatewayOperationID != observation.GatewayOperationID ||
			ack.FinalProtectedStateDigest != observation.ProtectedStateDigest || ack.ObservedAt.IsZero() ||
			ack.AcknowledgedAt.Before(ack.ObservedAt) {
			return appaccess.ErrInvalidStoredState
		}
		return nil
	}
	if err := runtime.WithGatewayV2LANRecoveryDisableFinalization(ctx, request, resolve, acknowledge); err != nil {
		return appaccess.AppAccessDisableClaim{}, err
	}
	return resolved, nil
}

func lanDisableRequestForAuthorization(claim appaccess.AppAccessDisableClaim,
	authorized appaccess.AppAccessDisableAuthorization,
) generatedingress.GatewayV2LANDisableRequest {
	request := generatedingress.GatewayV2LANDisableRequest{
		OperationID: claim.OperationID, RequestDigest: claim.RequestDigest, SpecDigest: claim.SpecDigest,
		AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID, OwnerOperationID: claim.Spec.OwnerOperationID,
		Port: claim.Spec.Port, AccessRevisionID: claim.Spec.AccessRevisionID,
		AccessRevisionNumber: claim.Spec.AccessRevisionNumber, AccessSpecDigest: authorized.Revision.SpecDigest,
		ApprovedBy: claim.ApprovedBy, GatewayProfileRevisionID: claim.Spec.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: claim.Spec.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     authorized.Profile.SpecDigest,
	}
	if authorized.SourceGrant != nil {
		grant := lanGrantRequestForClaim(*authorized.SourceGrant)
		request.SourceGrant = &grant
	}
	return request
}

type lanDisableAuthorizationLease struct {
	server       *Server
	service      LANAppDisableService
	owner        appaccess.AppAccessDisableClaimOwner
	expected     generatedingress.GatewayV2LANDisableRequest
	actorID      string
	sessionToken string
	csrf         string
	released     bool
}

func (lease *lanDisableAuthorizationLease) Revalidate(ctx context.Context, candidate generatedingress.GatewayV2LANDisableRequest) error {
	if lease == nil || lease.released || !reflect.DeepEqual(candidate, lease.expected) {
		return appaccess.ErrConflict
	}
	if !lease.server.lanGrantActorCurrent(lease.actorID, lease.sessionToken, lease.csrf) {
		return appaccess.ErrApprovalRequired
	}
	claim, err := lease.service.AppAccessDisableClaim(ctx, lease.owner.OperationID)
	if err != nil || claim.RequestDigest != lease.expected.RequestDigest ||
		(claim.State != appaccess.AppAccessDisablePrepared && claim.State != appaccess.AppAccessDisableWithdrawing) {
		return firstGatewayUpgradeError(err, appaccess.ErrConflict)
	}
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	owner.ActorID = lease.actorID
	authorized, err := lease.service.AuthorizeAppAccessDisable(ctx, appaccess.AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{claim.State},
	})
	if err != nil || authorized.Claim.RequestDigest != claim.RequestDigest {
		return firstGatewayUpgradeError(err, appaccess.ErrConflict)
	}
	return nil
}

func (lease *lanDisableAuthorizationLease) Withdraw(ctx context.Context, candidate generatedingress.GatewayV2LANDisableRequest) error {
	if err := lease.Revalidate(ctx, candidate); err != nil {
		return err
	}
	claim, err := lease.service.AppAccessDisableClaim(ctx, lease.owner.OperationID)
	if err != nil {
		return err
	}
	if claim.State == appaccess.AppAccessDisableWithdrawing {
		return nil
	}
	_, _, err = lease.service.AdvanceAppAccessDisableClaim(ctx, lease.owner,
		appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing)
	return err
}

func (lease *lanDisableAuthorizationLease) Release() error {
	if lease != nil {
		lease.released = true
	}
	return nil
}

func (s *Server) lanDisableRecoveryOnlyProblem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	problem(w, r, http.StatusServiceUnavailable, "gateway_reconciliation_required", "Only the startup LAN disable can be reconciled", nil)
}

func (s *Server) lanDisableProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, appaccess.ErrInvalidInput):
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_lan_access_request", "Invalid LAN disable request", 0)
	case errors.Is(err, appaccess.ErrIdempotencyMismatch):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_disable_replay_conflict", "LAN disable operation identifier was reused with different input", 0)
	case errors.Is(err, appaccess.ErrApprovalRequired):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_disable_approval_mismatch", "Confirmation does not approve this exact LAN disable", 0)
	case errors.Is(err, appaccess.ErrConflict), errors.Is(err, appaccess.ErrInvalidTransition), errors.Is(err, appaccess.ErrNotFound):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_disable_conflict", "LAN disable or route ownership changed", 0)
	default:
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "lan_disable_unavailable", "LAN disable could not be safely reconciled", 0)
	}
}
