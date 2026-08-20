package controller

import (
	"errors"
	"net/http"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appconfig"
)

type configurationEntryResponse struct {
	Key       string  `json:"key"`
	Sensitive bool    `json:"sensitive"`
	Value     *string `json:"value,omitempty"`
}

type applicationConfigurationResponse struct {
	RevisionID     string                       `json:"revisionId,omitempty"`
	RevisionNumber int64                        `json:"revisionNumber"`
	UpdatedAt      string                       `json:"updatedAt,omitempty"`
	Entries        []configurationEntryResponse `json:"entries"`
}

func (s *Server) getApplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.Configuration == nil {
		problem(w, r, http.StatusServiceUnavailable, "configuration_unavailable", "Application configuration is unavailable", nil)
		return
	}
	configuration, err := s.Configuration.Get(r.Context(), r.PathValue("appId"))
	if err != nil {
		configurationProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, configurationResponse(configuration))
}

func (s *Server) replaceApplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.Configuration == nil {
		problem(w, r, http.StatusServiceUnavailable, "configuration_unavailable", "Application configuration is unavailable", nil)
		return
	}
	var body apicontract.ReplaceApplicationConfigurationRequest
	if err := readJSON(r, &body); err != nil {
		problem(w, r, http.StatusUnprocessableEntity, "invalid_configuration", "Configuration input is invalid", map[string]string{"body": "Must be a valid request object"})
		return
	}
	input := appconfig.ReplaceInput{ExpectedRevisionNumber: body.ExpectedRevisionNumber, Remove: body.Remove}
	input.Variables = configurationInputs(body.Variables)
	input.Secrets = secretConfigurationInputs(body.Secrets)
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	configuration, err := s.Configuration.Replace(r.Context(), r.PathValue("appId"), actor, input)
	if err != nil {
		configurationProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, configurationResponse(configuration))
}

func secretConfigurationInputs(values []apicontract.SecretConfigurationValueInput) []appconfig.ValueInput {
	result := make([]appconfig.ValueInput, 0, len(values))
	for _, value := range values {
		result = append(result, appconfig.ValueInput{Key: value.Key, Value: value.Value})
	}
	return result
}

func configurationInputs(values []apicontract.ConfigurationValueInput) []appconfig.ValueInput {
	result := make([]appconfig.ValueInput, 0, len(values))
	for _, value := range values {
		result = append(result, appconfig.ValueInput{Key: value.Key, Value: value.Value})
	}
	return result
}

func configurationResponse(value appconfig.Configuration) applicationConfigurationResponse {
	response := applicationConfigurationResponse{RevisionID: value.RevisionID, RevisionNumber: value.RevisionNumber, Entries: make([]configurationEntryResponse, 0, len(value.Entries))}
	if !value.UpdatedAt.IsZero() {
		response.UpdatedAt = value.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	for _, entry := range value.Entries {
		item := configurationEntryResponse{Key: entry.Key, Sensitive: entry.Sensitive}
		if !entry.Sensitive {
			visible := entry.Value
			item.Value = &visible
		}
		response.Entries = append(response.Entries, item)
	}
	return response
}

func configurationProblem(w http.ResponseWriter, r *http.Request, err error) {
	var fields map[string]string
	var typed *appconfig.Error
	if appconfig.IsCode(err, "app_not_found") {
		problem(w, r, http.StatusNotFound, "app_not_found", "Application was not found", nil)
		return
	}
	if appconfig.IsCode(err, "invalid_configuration") {
		if errors.As(err, &typed) {
			fields = typed.Fields
		}
		problem(w, r, http.StatusUnprocessableEntity, "invalid_configuration", "Configuration input is invalid", fields)
		return
	}
	if appconfig.IsCode(err, "configuration_conflict") {
		problem(w, r, http.StatusConflict, "configuration_conflict", "Application configuration changed; reload and try again", nil)
		return
	}
	if appconfig.IsCode(err, "configuration_unavailable") {
		problem(w, r, http.StatusServiceUnavailable, "configuration_unavailable", "Application configuration is unavailable", nil)
		return
	}
	problem(w, r, http.StatusInternalServerError, "internal_error", "Could not update application configuration", nil)
}
