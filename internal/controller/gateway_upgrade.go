package controller

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	operationGetLANGatewayUpgrade = "getLANGatewayUpgrade"
	operationUpgradeLANGateway    = "upgradeLANGateway"
	maxGatewayUpgradeBodyBytes    = 4 << 10
)

// GatewayUpgradeService is the controller's durable approval boundary. Its
// authorization read is deliberately invoked from the ingress Manager while
// the gateway's in-process and cross-process writer locks are held.
type GatewayUpgradeService interface {
	CurrentGatewayProfile(context.Context) (appaccess.GatewayProfileRevision, error)
	ClaimGatewayProfileUpgrade(context.Context, appaccess.ClaimGatewayProfileUpgradeInput) (appaccess.GatewayProfileUpgradeClaim, bool, error)
	AuthorizeGatewayProfileUpgrade(context.Context, appaccess.GatewayProfileUpgradeAuthorizationInput) (appaccess.GatewayProfileUpgradeAuthorization, error)
	AdvanceGatewayProfileUpgradeClaim(context.Context, appaccess.GatewayProfileUpgradeClaimOwner, appaccess.GatewayProfileUpgradeState, appaccess.GatewayProfileUpgradeState) (appaccess.GatewayProfileUpgradeClaim, bool, error)
	CurrentGatewayProfileUpgradeClaim(context.Context) (appaccess.GatewayProfileUpgradeClaim, error)
	GatewayProfileUpgradeClaim(context.Context, string) (appaccess.GatewayProfileUpgradeClaim, error)
}

// GatewayUpgradeRuntime is available only with the generated runtime. The
// implementation owns the full gateway lock and all Docker observations.
type GatewayUpgradeRuntime interface {
	UpgradeGatewayV2(context.Context, generatedingress.GatewayV2UpgradeRequest, generatedingress.GatewayV2UpgradeAuthorizer) (generatedingress.GatewayV2UpgradeResult, error)
	ObserveGatewayV2Operation(context.Context, string) (generatedingress.GatewayV2OperationStatus, error)
}

func (s *Server) getLANGatewayUpgrade(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetLANGatewayUpgrade, "lan_gateway_forbidden", "Administrator access is required") {
		return
	}
	if r.URL.RawQuery != "" {
		s.gatewayUpgradeProblem(w, r, operationGetLANGatewayUpgrade, appaccess.ErrInvalidInput)
		return
	}
	if !s.gatewayUpgradeAvailable() {
		s.gatewayUpgradeCapabilityProblem(w, r, operationGetLANGatewayUpgrade)
		return
	}

	result := apicontract.LANGatewayUpgradeRead{
		Observed: apicontract.LANGatewayUpgradeObservation{Availability: string(generatedingress.GatewayV2OperationUnknown)},
	}
	profile, err := s.GatewayUpgrades.CurrentGatewayProfile(r.Context())
	switch {
	case err == nil:
		digest, digestErr := gatewayUpgradeActionDigest(profile)
		if digestErr != nil {
			s.gatewayUpgradeProblem(w, r, operationGetLANGatewayUpgrade, digestErr)
			return
		}
		result.Proposal = &apicontract.LANGatewayUpgradeProposal{
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ActionDigest: digest,
		}
	case errors.Is(err, appaccess.ErrNotFound):
		// An empty profile head has no upgrade proposal or desired claim.
	default:
		s.gatewayUpgradeProblem(w, r, operationGetLANGatewayUpgrade, err)
		return
	}

	claim, err := s.gatewayUpgradeClaim(r.Context())
	if errors.Is(err, appaccess.ErrNotFound) {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err != nil || result.Proposal == nil || claim.ProfileRevisionID != result.Proposal.ProfileRevisionID ||
		claim.ProfileRevisionNumber != result.Proposal.ProfileRevisionNumber {
		s.gatewayUpgradeProblem(w, r, operationGetLANGatewayUpgrade, firstGatewayUpgradeError(err, appaccess.ErrInvalidStoredState))
		return
	}
	contractClaim := contractLANGatewayUpgradeClaim(claim)
	result.DesiredClaim = &contractClaim
	result.Observed.OperationID = claim.OperationID
	observed, observeErr := s.GatewayUpgradeRuntime.ObserveGatewayV2Operation(r.Context(), claim.OperationID)
	if observeErr == nil && observed.OperationID == claim.OperationID && validGatewayUpgradeAvailability(observed.Availability) {
		result.Observed.Availability = string(observed.Availability)
	} else {
		result.Observed.Availability = string(generatedingress.GatewayV2OperationUnknown)
	}
	confirmedClaim, confirmErr := s.gatewayUpgradeClaim(r.Context())
	if confirmErr != nil || confirmedClaim != claim {
		s.gatewayUpgradeProblem(w, r, operationGetLANGatewayUpgrade, errors.New("gateway upgrade observation changed"))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) upgradeLANGateway(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationUpgradeLANGateway, "lan_gateway_forbidden", "Administrator access is required") {
		return
	}
	if r.URL.RawQuery != "" {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, appaccess.ErrInvalidInput)
		return
	}
	// Capability is checked before any durable claim can pin the profile.
	if !s.gatewayUpgradeAvailable() {
		s.gatewayUpgradeCapabilityProblem(w, r, operationUpgradeLANGateway)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || sessionCookie.Value == "" {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, appaccess.ErrApprovalRequired)
		return
	}
	sessionToken := sessionCookie.Value
	submittedCSRF := r.Header.Get("X-CSRF-Token")

	body, err := readUpgradeLANGatewayRequest(r)
	if err != nil || !validCanonicalUUID(body.OperationID) || !validCanonicalUUID(body.ProfileRevisionID) ||
		body.ProfileRevisionNumber <= 0 || !validLowerHex(body.ActionDigest, 64) {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, appaccess.ErrInvalidInput)
		return
	}
	if s.RecoveryOnly && (s.RecoveryOperationID == "" || body.OperationID != s.RecoveryOperationID) {
		w.Header().Set("Cache-Control", "no-store")
		problem(w, r, http.StatusServiceUnavailable, "gateway_reconciliation_required", "Only the startup gateway upgrade can be reconciled", nil)
		return
	}
	profile, err := s.GatewayUpgrades.CurrentGatewayProfile(r.Context())
	if err != nil {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, err)
		return
	}
	if profile.ID != body.ProfileRevisionID || profile.RevisionNumber != body.ProfileRevisionNumber {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, appaccess.ErrConflict)
		return
	}
	actionDigest, err := gatewayUpgradeActionDigest(profile)
	if err != nil {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(actionDigest), []byte(body.ActionDigest)) != 1 {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, appaccess.ErrApprovalRequired)
		return
	}

	actor := r.Context().Value(principalKey{}).(principal).user.ID
	claim, created, err := s.GatewayUpgrades.ClaimGatewayProfileUpgrade(r.Context(), appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: body.OperationID,
		Spec: appaccess.GatewayProfileUpgradeSpec{
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
		},
		Approval: appaccess.Approval{Action: appaccess.ActionUpgradeGateway, SpecDigest: actionDigest, ActorID: actor},
	})
	if err != nil {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, err)
		return
	}
	request, err := gatewayV2RequestFromAuthorization(claim, profile, actionDigest)
	if err != nil {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, err)
		return
	}
	authorize := func(ctx context.Context, candidate generatedingress.GatewayV2UpgradeRequest) error {
		currentActor, csrfHash, sessionErr := s.Auth.Authenticate(sessionToken)
		if sessionErr != nil || currentActor.ID != actor || currentActor.Role != "administrator" ||
			!s.Auth.CheckCSRF(csrfHash, submittedCSRF) {
			return appaccess.ErrApprovalRequired
		}
		return s.authorizeGatewayUpgradeRequest(ctx, claim, actionDigest, candidate)
	}
	upgradeResult, upgradeErr := s.GatewayUpgradeRuntime.UpgradeGatewayV2(r.Context(), request, authorize)
	unresolvedInvocation := gatewayUpgradeInvocationUnresolved(upgradeResult, upgradeErr)
	claim, responseReady := s.persistGatewayUpgradeOutcome(r.Context(), claim, upgradeResult, upgradeErr)
	if !responseReady {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, appaccess.ErrInvalidStoredState)
		return
	}
	if unresolvedInvocation {
		s.gatewayUpgradeProblem(w, r, operationUpgradeLANGateway, errors.New("gateway upgrade unresolved"))
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, apicontract.LANGatewayUpgradeMutation{Claim: contractLANGatewayUpgradeClaim(claim), Created: created})
}

func (s *Server) gatewayUpgradeClaim(ctx context.Context) (appaccess.GatewayProfileUpgradeClaim, error) {
	if s.RecoveryOnly {
		if s.RecoveryOperationID == "" {
			return appaccess.GatewayProfileUpgradeClaim{}, appaccess.ErrInvalidStoredState
		}
		return s.GatewayUpgrades.GatewayProfileUpgradeClaim(ctx, s.RecoveryOperationID)
	}
	return s.GatewayUpgrades.CurrentGatewayProfileUpgradeClaim(ctx)
}

func gatewayUpgradeInvocationUnresolved(result generatedingress.GatewayV2UpgradeResult, resultErr error) bool {
	switch result.Outcome {
	case generatedingress.GatewayV2UpgradeRolledBack:
		return false
	case generatedingress.GatewayV2UpgradeCommitted:
		return resultErr != nil
	case generatedingress.GatewayV2UpgradeUnresolved:
		return true
	default:
		return true
	}
}

func (s *Server) authorizeGatewayUpgradeRequest(ctx context.Context, claim appaccess.GatewayProfileUpgradeClaim,
	actionDigest string, candidate generatedingress.GatewayV2UpgradeRequest,
) error {
	authorized, err := s.GatewayUpgrades.AuthorizeGatewayProfileUpgrade(ctx, appaccess.GatewayProfileUpgradeAuthorizationInput{
		OperationID: claim.OperationID, RequestDigest: claim.RequestDigest, ActionDigest: actionDigest,
		ActorID: claim.ApprovedBy, ProfileRevisionID: claim.ProfileRevisionID,
		ProfileRevisionNumber: claim.ProfileRevisionNumber, ProfileSpecDigest: claim.ProfileSpecDigest,
		PermittedStates: []appaccess.GatewayProfileUpgradeState{claim.State},
	})
	if err != nil {
		return err
	}
	expected, err := gatewayV2RequestFromAuthorization(authorized.Claim, authorized.Profile, actionDigest)
	if err != nil || authorized.Claim != claim || candidate != expected {
		return appaccess.ErrConflict
	}
	return nil
}

func (s *Server) persistGatewayUpgradeOutcome(ctx context.Context, claim appaccess.GatewayProfileUpgradeClaim,
	result generatedingress.GatewayV2UpgradeResult, resultErr error,
) (appaccess.GatewayProfileUpgradeClaim, bool) {
	target := appaccess.GatewayProfileUpgradeUnresolved
	switch result.Outcome {
	case generatedingress.GatewayV2UpgradeCommitted:
		if resultErr != nil {
			return s.persistGatewayUpgradeUnresolved(ctx, claim)
		}
		target = appaccess.GatewayProfileUpgradeCommitted
	case generatedingress.GatewayV2UpgradeRolledBack:
		// A RolledBack result is emitted only after the Manager has installed and
		// reloaded durable protected evidence and successfully released its lock.
		target = appaccess.GatewayProfileUpgradeRolledBack
	case generatedingress.GatewayV2UpgradeUnresolved:
		return s.persistGatewayUpgradeUnresolved(ctx, claim)
	default:
		return s.persistGatewayUpgradeUnresolved(ctx, claim)
	}
	if claim.State == target {
		return claim, true
	}
	updated, _, err := s.GatewayUpgrades.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeOwner(claim), claim.State, target)
	if err != nil {
		return claim, false
	}
	return updated, true
}

func (s *Server) persistGatewayUpgradeUnresolved(ctx context.Context, claim appaccess.GatewayProfileUpgradeClaim) (appaccess.GatewayProfileUpgradeClaim, bool) {
	if claim.State == appaccess.GatewayProfileUpgradeUnresolved || claim.State == appaccess.GatewayProfileUpgradeCommitted ||
		claim.State == appaccess.GatewayProfileUpgradeRolledBack {
		return claim, true
	}
	updated, _, err := s.GatewayUpgrades.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeOwner(claim), claim.State, appaccess.GatewayProfileUpgradeUnresolved)
	if err != nil {
		return claim, false
	}
	return updated, true
}

func gatewayUpgradeOwner(claim appaccess.GatewayProfileUpgradeClaim) appaccess.GatewayProfileUpgradeClaimOwner {
	return appaccess.GatewayProfileUpgradeClaimOwner{
		OperationID: claim.OperationID, ProfileRevisionID: claim.ProfileRevisionID, ProfileRevisionNumber: claim.ProfileRevisionNumber,
	}
}

func gatewayV2RequestFromAuthorization(claim appaccess.GatewayProfileUpgradeClaim, profile appaccess.GatewayProfileRevision,
	actionDigest string,
) (generatedingress.GatewayV2UpgradeRequest, error) {
	if claim.ProfileRevisionID != profile.ID || claim.ProfileRevisionNumber != profile.RevisionNumber ||
		claim.ProfileSpecDigest != profile.SpecDigest || claim.ApprovedBy == "" {
		return generatedingress.GatewayV2UpgradeRequest{}, appaccess.ErrInvalidStoredState
	}
	wantDigest, err := gatewayUpgradeActionDigest(profile)
	if err != nil || subtle.ConstantTimeCompare([]byte(wantDigest), []byte(actionDigest)) != 1 {
		return generatedingress.GatewayV2UpgradeRequest{}, appaccess.ErrInvalidStoredState
	}
	return generatedingress.GatewayV2UpgradeRequest{
		OperationID: claim.OperationID,
		Profile: generatedingress.GatewayV2ProfileBinding{
			RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
			SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
			PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd,
		},
		ApprovedBy: claim.ApprovedBy, ApprovedActionDigest: actionDigest,
	}, nil
}

func gatewayUpgradeActionDigest(profile appaccess.GatewayProfileRevision) (string, error) {
	return appaccess.GatewayProfileUpgradeSpecDigest(appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
	})
}

func contractLANGatewayUpgradeClaim(value appaccess.GatewayProfileUpgradeClaim) apicontract.LANGatewayUpgradeClaim {
	return apicontract.LANGatewayUpgradeClaim{
		OperationID: value.OperationID, ProfileRevisionID: value.ProfileRevisionID,
		ProfileRevisionNumber: value.ProfileRevisionNumber, ProfileSpecDigest: value.ProfileSpecDigest,
		State: string(value.State), UpdatedAt: contractTime(value.UpdatedAt),
	}
}

func validGatewayUpgradeAvailability(value generatedingress.GatewayV2OperationAvailability) bool {
	switch value {
	case generatedingress.GatewayV2OperationServing, generatedingress.GatewayV2OperationUnavailable, generatedingress.GatewayV2OperationUnknown:
		return true
	default:
		return false
	}
}

func (s *Server) gatewayUpgradeAvailable() bool {
	return s != nil && s.GeneratedRuntime && s.GatewayUpgrades != nil && s.GatewayUpgradeRuntime != nil
}

func (s *Server) gatewayUpgradeCapabilityProblem(w http.ResponseWriter, r *http.Request, operation string) {
	s.handlerProblem(w, r, operation, http.StatusConflict, "capability_unavailable", "LAN gateway upgrade requires the generated runtime", 0)
}

func (s *Server) gatewayUpgradeProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, appaccess.ErrInvalidInput):
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_lan_gateway_request", "Invalid LAN gateway upgrade request", 0)
	case errors.Is(err, appaccess.ErrIdempotencyMismatch):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_replay_conflict", "LAN gateway operation identifier was already used for different input", 0)
	case errors.Is(err, appaccess.ErrApprovalRequired):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_approval_mismatch", "Confirmation does not approve this exact LAN gateway upgrade", 0)
	case errors.Is(err, appaccess.ErrConflict), errors.Is(err, appaccess.ErrNotFound):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_conflict", "LAN gateway profile or upgrade state changed", 0)
	default:
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "lan_gateway_unavailable", "LAN gateway upgrade state is unavailable or unresolved", 0)
	}
}

func readUpgradeLANGatewayRequest(r *http.Request) (apicontract.UpgradeLANGatewayRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return apicontract.UpgradeLANGatewayRequest{}, appaccess.ErrInvalidInput
	}
	defer r.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayUpgradeBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxGatewayUpgradeBodyBytes {
		return apicontract.UpgradeLANGatewayRequest{}, appaccess.ErrInvalidInput
	}
	fields, err := decodeRequiredGatewayObject(payload, "operationId", "profileRevisionId", "profileRevisionNumber", "actionDigest")
	if err != nil {
		return apicontract.UpgradeLANGatewayRequest{}, err
	}
	var result apicontract.UpgradeLANGatewayRequest
	if err = decodeRequiredGatewayScalar(fields["operationId"], &result.OperationID); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(fields["profileRevisionId"], &result.ProfileRevisionID); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(fields["profileRevisionNumber"], &result.ProfileRevisionNumber); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(fields["actionDigest"], &result.ActionDigest); err != nil {
		return result, err
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return result, appaccess.ErrInvalidInput
	}
	return result, nil
}

func firstGatewayUpgradeError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
