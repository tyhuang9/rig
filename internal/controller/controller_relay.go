package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/controllerrelay"
)

const maxRelayRetryAfter = 5 * time.Minute

const (
	operationGetRelayStatus              = "getRelayStatus"
	operationStartRelayEnrollment        = "startRelayEnrollment"
	operationPollRelayEnrollment         = "pollRelayEnrollment"
	operationRemoveRelayBinding          = "removeRelayBinding"
	operationStartRelayKeyRotation       = "startRelayKeyRotation"
	operationGetApplicationAutoDeploy    = "getApplicationAutoDeploy"
	operationUpdateApplicationAutoDeploy = "updateApplicationAutoDeploy"
	operationResumeApplicationAutoDeploy = "resumeApplicationAutoDeploy"
)

func (s *Server) getRelayStatus(w http.ResponseWriter, _ *http.Request) {
	status := controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementUnavailable, DiagnosticsUnavailable: true}
	if s.RelayManagement != nil {
		status = s.RelayManagement.Status()
	}
	writeJSON(w, http.StatusOK, contractRelayStatus(status))
}

func (s *Server) startRelayEnrollment(w http.ResponseWriter, r *http.Request) {
	var body apicontract.StartRelayEnrollmentRequest
	if err := readJSON(r, &body); err != nil || !validLowerHex(body.ConnectionID, 32) || body.InstallationID < 1 || body.RepositoryID < 1 {
		s.handlerProblem(w, r, operationStartRelayEnrollment, http.StatusUnprocessableEntity, "invalid_relay_request", "Invalid controller relay request", 0)
		return
	}
	management := s.RelayManagement
	if management == nil {
		s.relayUnavailableProblem(w, r, operationStartRelayEnrollment, 0)
		return
	}
	owner := r.Context().Value(principalKey{}).(principal).user.ID
	result, err := management.StartEnrollment(r.Context(), owner, controllerrelay.ManagementEnrollmentInput{
		ConnectionID: body.ConnectionID, InstallationID: body.InstallationID, RepositoryID: body.RepositoryID,
	})
	if err != nil {
		s.relayProblem(w, r, operationStartRelayEnrollment, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, apicontract.RelayEnrollmentStart{
		EnrollmentID: result.EnrollmentID, AuthorizationUrl: result.AuthorizationURL,
		Status: result.State, ExpiresAt: contractTime(result.ExpiresAt),
	})
}

func (s *Server) pollRelayEnrollment(w http.ResponseWriter, r *http.Request) {
	enrollmentID := r.PathValue("enrollmentId")
	if !validCanonicalUUID(enrollmentID) {
		s.handlerProblem(w, r, operationPollRelayEnrollment, http.StatusUnprocessableEntity, "invalid_relay_request", "Invalid controller relay request", 0)
		return
	}
	management := s.RelayManagement
	if management == nil {
		s.relayUnavailableProblem(w, r, operationPollRelayEnrollment, 0)
		return
	}
	owner := r.Context().Value(principalKey{}).(principal).user.ID
	result, err := management.PollEnrollment(r.Context(), owner, enrollmentID)
	if err != nil {
		s.relayProblem(w, r, operationPollRelayEnrollment, err)
		return
	}
	status := http.StatusOK
	if result.State == controllerrelay.EnrollmentPending {
		status = http.StatusAccepted
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, contractRelayEnrollmentStatus(result))
}

func (s *Server) removeRelayBinding(w http.ResponseWriter, r *http.Request) {
	bindingID := r.PathValue("bindingId")
	if !validCanonicalUUID(bindingID) {
		s.handlerProblem(w, r, operationRemoveRelayBinding, http.StatusUnprocessableEntity, "invalid_relay_request", "Invalid controller relay request", 0)
		return
	}
	management := s.RelayManagement
	if management == nil {
		s.relayUnavailableProblem(w, r, operationRemoveRelayBinding, 0)
		return
	}
	owner := r.Context().Value(principalKey{}).(principal).user.ID
	result, err := management.RemoveBinding(r.Context(), owner, bindingID)
	if err != nil {
		s.relayProblem(w, r, operationRemoveRelayBinding, err)
		return
	}
	status := http.StatusOK
	if result.State == controllerrelay.BindingRemovalPending {
		status = http.StatusAccepted
	} else if result.State != controllerrelay.BindingRemoved {
		s.handlerProblem(w, r, operationRemoveRelayBinding, http.StatusConflict, "relay_state_conflict", "Controller relay state does not allow this operation", 0)
		return
	}
	writeJSON(w, status, apicontract.RelayBindingStatus{
		BindingID: result.BindingID, State: result.State, UpdatedAt: contractTime(result.UpdatedAt),
	})
}

func (s *Server) startRelayKeyRotation(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationStartRelayKeyRotation, "relay_forbidden", "Administrator access is required") {
		return
	}
	management := s.RelayManagement
	if management == nil {
		s.relayUnavailableProblem(w, r, operationStartRelayKeyRotation, 0)
		return
	}
	result, err := management.RotateKey(r.Context())
	if err != nil {
		s.relayProblem(w, r, operationStartRelayKeyRotation, err)
		return
	}
	writeJSON(w, http.StatusAccepted, apicontract.RelayKeyRotationStatus{
		RotationID: result.RotationID, State: result.State, ExpiresAt: contractTime(result.ExpiresAt),
	})
}

func contractRelayStatus(value controllerrelay.ManagementStatus) apicontract.RelayStatus {
	availability := value.Availability
	if availability != controllerrelay.ManagementInitializing && availability != controllerrelay.ManagementAvailable && availability != controllerrelay.ManagementUnavailable {
		availability = controllerrelay.ManagementUnavailable
	}
	return apicontract.RelayStatus{
		Availability: availability, State: safeRelayDiagnostic(value.State), Paused: value.Paused,
		Outcome: safeRelayDiagnostic(value.Outcome), DiagnosticsUnavailable: value.DiagnosticsUnavailable,
		PendingCommands: nonnegativeInt(value.PendingCommands), ActiveLeases: nonnegativeInt(value.ActiveLeases),
		ExpiredLeases: nonnegativeInt(value.ExpiredLeases), OldestPendingAgeSeconds: boundedInt64ToInt(value.OldestPendingAgeSeconds),
		ObserverDropped: boundedUint64ToInt64(value.ObserverDropped),
	}
}

func contractRelayEnrollmentStatus(value controllerrelay.ManagementEnrollmentStatus) apicontract.RelayEnrollmentStatus {
	completedAt := ""
	if value.CompletedAt != nil {
		completedAt = contractTime(*value.CompletedAt)
	}
	return apicontract.RelayEnrollmentStatus{
		EnrollmentID: value.EnrollmentID, BindingID: value.BindingID, Status: value.State,
		CreatedAt: contractTime(value.CreatedAt), UpdatedAt: contractTime(value.UpdatedAt),
		ExpiresAt: contractTime(value.ExpiresAt), CompletedAt: completedAt,
	}
}

func (s *Server) relayProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	var managementError *controllerrelay.ManagementError
	if !errors.As(err, &managementError) {
		s.handlerProblem(w, r, operation, http.StatusInternalServerError, "internal_error", "Controller relay operation failed", 0)
		return
	}
	retryAfterSeconds := setRelayRetryAfter(w, managementError.RetryAfter)
	switch managementError.Code {
	case controllerrelay.ManagementErrorUnavailable, "relay_unavailable":
		s.relayUnavailableProblem(w, r, operation, retryAfterSeconds)
	case "provider_unavailable":
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "provider_unavailable", "GitHub is temporarily unavailable", retryAfterSeconds)
	case controllerrelay.ManagementErrorInvalidRequest:
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_relay_request", "Invalid controller relay request", retryAfterSeconds)
	case controllerrelay.ManagementErrorEnrollmentMissing:
		s.handlerProblem(w, r, operation, http.StatusNotFound, "relay_enrollment_not_found", "Controller relay enrollment was not found", retryAfterSeconds)
	case controllerrelay.ManagementErrorBindingMissing:
		s.handlerProblem(w, r, operation, http.StatusNotFound, "relay_binding_not_found", "Controller relay binding was not found", retryAfterSeconds)
	case controllerrelay.ManagementErrorBindingState, controllerrelay.ManagementErrorRotationConflict:
		s.handlerProblem(w, r, operation, http.StatusConflict, "relay_state_conflict", "Controller relay state does not allow this operation", retryAfterSeconds)
	case controllerrelay.ManagementErrorIdentity:
		s.handlerProblem(w, r, operation, http.StatusConflict, "relay_prerequisite_missing", "Controller relay prerequisites are missing", retryAfterSeconds)
	case "invalid_source":
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_source", "GitHub source is invalid or no longer exists", retryAfterSeconds)
	case "authentication_required":
		s.handlerProblem(w, r, operation, http.StatusForbidden, "authentication_required", "GitHub authorization does not permit this operation", retryAfterSeconds)
	case "source_access_lost":
		s.handlerProblem(w, r, operation, http.StatusConflict, "source_access_lost", "Source access must be restored before continuing", retryAfterSeconds)
	case "authorization_denied", "authorization_expired":
		s.handlerProblem(w, r, operation, http.StatusConflict, "source_access_lost", "Source access must be restored before continuing", retryAfterSeconds)
	default:
		s.handlerProblem(w, r, operation, http.StatusInternalServerError, "internal_error", "Controller relay operation failed", retryAfterSeconds)
	}
}

func (s *Server) relayUnavailableProblem(w http.ResponseWriter, r *http.Request, operation string, retryAfterSeconds int64) {
	s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "relay_unavailable", "Controller relay is unavailable", retryAfterSeconds)
}

func setRelayRetryAfter(w http.ResponseWriter, value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	if value > maxRelayRetryAfter {
		value = maxRelayRetryAfter
	}
	seconds := int64(value / time.Second)
	if value%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	return seconds
}

func (s *Server) handlerProblem(w http.ResponseWriter, r *http.Request, operation string, status int, code, detail string, retryAfterSeconds int64) {
	operation = safeHandlerOperation(operation)
	code = safeHandlerProblemCode(code)
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusInternalServerError
		code = "internal_error"
	}
	if retryAfterSeconds < 0 {
		retryAfterSeconds = 0
	}
	if retryAfterSeconds > int64(maxRelayRetryAfter/time.Second) {
		retryAfterSeconds = int64(maxRelayRetryAfter / time.Second)
	}
	if s.Logger != nil {
		s.Logger.Warn("api handler failure",
			"request_id", requestID(r),
			"operation", operation,
			"problem_code", code,
			"status", status,
			"retry_after_seconds", retryAfterSeconds,
		)
	}
	problem(w, r, status, code, detail, nil)
}

func safeHandlerOperation(operation string) string {
	switch operation {
	case operationGetRelayStatus, operationStartRelayEnrollment, operationPollRelayEnrollment,
		operationRemoveRelayBinding, operationStartRelayKeyRotation,
		operationGetApplicationAutoDeploy, operationUpdateApplicationAutoDeploy,
		operationResumeApplicationAutoDeploy:
		return operation
	default:
		return "unknown"
	}
}

func safeHandlerProblemCode(code string) string {
	switch code {
	case "unauthenticated", "csrf_failed", "invalid_relay_request", "relay_unavailable",
		"relay_enrollment_not_found", "relay_binding_not_found", "relay_state_conflict",
		"relay_prerequisite_missing", "relay_forbidden", "provider_unavailable",
		"invalid_source", "authentication_required", "source_access_lost",
		"invalid_auto_deploy_request", "auto_deploy_conflict", "auto_deploy_state_conflict",
		"application_busy", "auto_deploy_forbidden", "auto_deploy_prerequisite_missing",
		"app_not_found", "capability_unavailable", "internal_error":
		return code
	default:
		return "internal_error"
	}
}

func safeRelayDiagnostic(value string) string {
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return ""
		}
	}
	return value
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func contractTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nonnegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func boundedInt64ToInt(value int64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func boundedUint64ToInt64(value uint64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if value > uint64(maxInt64) {
		return maxInt64
	}
	return int64(value)
}
