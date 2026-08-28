package controller

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/autodeploy"
)

const maxAutoDeployRequestBodyBytes = 1 << 20

func (s *Server) getApplicationAutoDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetApplicationAutoDeploy, "auto_deploy_forbidden", "Administrator access is required") {
		return
	}
	application, ok := s.autoDeployApplication(w, r, operationGetApplicationAutoDeploy)
	if !ok {
		return
	}
	if s.AutoDeploy == nil {
		s.handlerProblem(w, r, operationGetApplicationAutoDeploy, http.StatusConflict, "capability_unavailable", "GitHub auto-deploy is unavailable in this configuration", 0)
		return
	}
	status, err := s.AutoDeploy.Get(r.Context(), application.ID)
	if err != nil {
		s.autoDeployProblem(w, r, operationGetApplicationAutoDeploy, err)
		return
	}
	writeJSON(w, http.StatusOK, contractApplicationAutoDeploy(status, application.Source))
}

func (s *Server) updateApplicationAutoDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationUpdateApplicationAutoDeploy, "auto_deploy_forbidden", "Administrator access is required") {
		return
	}
	var body apicontract.UpdateApplicationAutoDeployRequest
	if err := readUpdateAutoDeployJSON(r, &body); err != nil || body.ExpectedRevision < 0 {
		s.handlerProblem(w, r, operationUpdateApplicationAutoDeploy, http.StatusUnprocessableEntity, "invalid_auto_deploy_request", "Invalid auto-deploy request", 0)
		return
	}
	application, ok := s.autoDeployApplication(w, r, operationUpdateApplicationAutoDeploy)
	if !ok {
		return
	}
	if body.Enabled && !s.AutoDeployAvailable {
		s.handlerProblem(w, r, operationUpdateApplicationAutoDeploy, http.StatusConflict, "capability_unavailable", "GitHub auto-deploy is unavailable in this configuration", 0)
		return
	}
	if s.AutoDeploy == nil {
		s.handlerProblem(w, r, operationUpdateApplicationAutoDeploy, http.StatusConflict, "capability_unavailable", "GitHub auto-deploy is unavailable in this configuration", 0)
		return
	}
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	status, err := s.AutoDeploy.Configure(r.Context(), autodeploy.ConfigureRequest{
		ApplicationID: application.ID, ActorUserID: actor,
		ExpectedRevision: uint64(body.ExpectedRevision), Enabled: body.Enabled,
	}, time.Now().UTC())
	if err != nil {
		s.autoDeployProblem(w, r, operationUpdateApplicationAutoDeploy, err)
		return
	}
	if s.RelayReconcile != nil {
		s.RelayReconcile()
	}
	if s.AutoDeployReconcile != nil {
		s.AutoDeployReconcile()
	}
	writeJSON(w, http.StatusOK, contractApplicationAutoDeploy(status, application.Source))
}

func (s *Server) resumeApplicationAutoDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationResumeApplicationAutoDeploy, "auto_deploy_forbidden", "Administrator access is required") {
		return
	}
	var body apicontract.ResumeApplicationAutoDeployRequest
	if err := readResumeAutoDeployJSON(r, &body); err != nil || body.ExpectedRevision < 0 {
		s.handlerProblem(w, r, operationResumeApplicationAutoDeploy, http.StatusUnprocessableEntity, "invalid_auto_deploy_request", "Invalid auto-deploy request", 0)
		return
	}
	application, ok := s.autoDeployApplication(w, r, operationResumeApplicationAutoDeploy)
	if !ok {
		return
	}
	if s.AutoDeploy == nil {
		s.handlerProblem(w, r, operationResumeApplicationAutoDeploy, http.StatusConflict, "capability_unavailable", "GitHub auto-deploy is unavailable in this configuration", 0)
		return
	}
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	status, err := s.AutoDeploy.Resume(r.Context(), application.ID, actor, uint64(body.ExpectedRevision), time.Now().UTC())
	if err != nil {
		s.autoDeployProblem(w, r, operationResumeApplicationAutoDeploy, err)
		return
	}
	if s.AutoDeployReconcile != nil {
		s.AutoDeployReconcile()
	}
	writeJSON(w, http.StatusOK, contractApplicationAutoDeploy(status, application.Source))
}

func (s *Server) autoDeployApplication(w http.ResponseWriter, r *http.Request, operation string) (apps.Application, bool) {
	applicationID := r.PathValue("appId")
	if !validCanonicalUUID(applicationID) {
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_auto_deploy_request", "Invalid auto-deploy request", 0)
		return apps.Application{}, false
	}
	if s.Apps == nil {
		s.handlerProblem(w, r, operation, http.StatusInternalServerError, "internal_error", "Could not inspect application", 0)
		return apps.Application{}, false
	}
	application, err := s.Apps.Get(applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		s.handlerProblem(w, r, operation, http.StatusNotFound, "app_not_found", "Application not found", 0)
		return apps.Application{}, false
	}
	if err != nil {
		s.handlerProblem(w, r, operation, http.StatusInternalServerError, "internal_error", "Could not inspect application", 0)
		return apps.Application{}, false
	}
	return application, true
}

func contractApplicationAutoDeploy(value autodeploy.Status, source apps.Source) apicontract.ApplicationAutoDeployStatus {
	result := apicontract.ApplicationAutoDeployStatus{
		ApplicationID: value.ApplicationID, Revision: boundedUint64ToInt64(value.Revision), Enabled: value.Enabled,
		State: value.State, Source: contractAppSource(source), SourceScopeActive: value.SourceScopeActive,
		PauseCode: value.PauseCode, ActiveJobID: value.ActiveJobID,
		LatestResolvedSha: value.LatestResolvedSHA, ActiveSha: value.ActiveSHA,
		LastSuccessfulDeployedSha: value.LastSuccessfulDeployedSHA, PausedSha: value.PausedSHA,
		RetryAttempt: boundedUint32ToInt(value.RetryAttempt), UpdatedAt: contractTime(value.UpdatedAt),
	}
	if value.NextRetryAt != nil {
		result.NextRetryAt = contractTime(*value.NextRetryAt)
	}
	return result
}

func (s *Server) autoDeployProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, autodeploy.ErrInvalid):
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_auto_deploy_request", "Invalid auto-deploy request", 0)
	case errors.Is(err, autodeploy.ErrConflict):
		s.handlerProblem(w, r, operation, http.StatusConflict, "auto_deploy_conflict", "Auto-deploy was changed by another request", 0)
	case errors.Is(err, autodeploy.ErrState):
		s.handlerProblem(w, r, operation, http.StatusConflict, "auto_deploy_state_conflict", "Auto-deploy state does not allow this operation", 0)
	case errors.Is(err, autodeploy.ErrApplicationBusy):
		s.handlerProblem(w, r, operation, http.StatusConflict, "application_busy", "Application has an active auto-deploy job", 0)
	case errors.Is(err, autodeploy.ErrSourceAccessLost):
		s.handlerProblem(w, r, operation, http.StatusConflict, "source_access_lost", "Source access must be restored before continuing", 0)
	case errors.Is(err, autodeploy.ErrUnauthorized):
		s.handlerProblem(w, r, operation, http.StatusForbidden, "auto_deploy_forbidden", "Administrator is not permitted to manage this auto-deploy configuration", 0)
	case errors.Is(err, autodeploy.ErrNotFound):
		s.handlerProblem(w, r, operation, http.StatusConflict, "auto_deploy_prerequisite_missing", "Auto-deploy prerequisites are missing", 0)
	default:
		s.handlerProblem(w, r, operation, http.StatusInternalServerError, "internal_error", "Auto-deploy operation failed", 0)
	}
}

func (s *Server) requireAdministrator(w http.ResponseWriter, r *http.Request, operation, code, detail string) bool {
	value, ok := r.Context().Value(principalKey{}).(principal)
	if !ok || value.user.Role != "administrator" {
		s.handlerProblem(w, r, operation, http.StatusForbidden, code, detail, 0)
		return false
	}
	return true
}

func readUpdateAutoDeployJSON(r *http.Request, destination *apicontract.UpdateApplicationAutoDeployRequest) error {
	fields, err := readRequiredAutoDeployObject(r, "expectedRevision", "enabled")
	if err != nil {
		return err
	}
	if err = decodeRequiredAutoDeployScalar(fields["expectedRevision"], &destination.ExpectedRevision); err != nil {
		return err
	}
	return decodeRequiredAutoDeployScalar(fields["enabled"], &destination.Enabled)
}

func readResumeAutoDeployJSON(r *http.Request, destination *apicontract.ResumeApplicationAutoDeployRequest) error {
	fields, err := readRequiredAutoDeployObject(r, "expectedRevision")
	if err != nil {
		return err
	}
	return decodeRequiredAutoDeployScalar(fields["expectedRevision"], &destination.ExpectedRevision)
}

func readRequiredAutoDeployObject(r *http.Request, required ...string) (map[string]json.RawMessage, error) {
	defer r.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxAutoDeployRequestBodyBytes+1))
	if err != nil {
		return nil, errors.New("could not read auto-deploy request")
	}
	if len(payload) > maxAutoDeployRequestBodyBytes {
		return nil, errors.New("auto-deploy request is too large")
	}
	allowed := make(map[string]struct{}, len(required))
	for _, name := range required {
		allowed[name] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("auto-deploy request must be a JSON object")
	}
	fields := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return nil, errors.New("invalid auto-deploy request field")
		}
		if _, ok = allowed[name]; !ok {
			return nil, errors.New("unknown auto-deploy request field")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, errors.New("duplicate auto-deploy request field")
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return nil, errors.New("invalid auto-deploy request value")
		}
		fields[name] = raw
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid auto-deploy request object")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("auto-deploy request must contain a single JSON object")
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return nil, errors.New("missing auto-deploy request field")
		}
	}
	return fields, nil
}

func decodeRequiredAutoDeployScalar(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("auto-deploy request field is required")
	}
	if err := json.Unmarshal(trimmed, destination); err != nil {
		return errors.New("invalid auto-deploy request field type")
	}
	return nil
}

func boundedUint32ToInt(value uint32) int {
	if uint64(value) > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(value)
}
