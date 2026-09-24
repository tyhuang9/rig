package controller

import (
	"net/http"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentplans"
)

type scopedConfigurationRequest struct {
	ExpectedRevisionNumber            *int64                           `json:"expectedRevisionNumber"`
	PlanRevisionID                    *string                          `json:"planRevisionId"`
	PlanRevisionNumber                *int64                           `json:"planRevisionNumber"`
	Entries                           *[]scopedConfigurationValueInput `json:"entries"`
	Remove                            *[]scopedConfigurationKey        `json:"remove"`
	PublicBuildDisclosureAcknowledged *bool                            `json:"publicBuildDisclosureAcknowledged"`
}

type scopedConfigurationKey struct {
	Key             string `json:"key"`
	Phase           string `json:"phase"`
	TargetComponent string `json:"targetComponent"`
}

type scopedConfigurationValueInput struct {
	scopedConfigurationKey
	Sensitive            *bool   `json:"sensitive"`
	Value                *string `json:"value"`
	PreserveStoredSecret bool    `json:"preserveStoredSecret"`
}

func (s *Server) replaceScopedApplicationConfiguration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireAdministrator(w, r, "replaceScopedApplicationConfiguration", "configuration_forbidden", "Administrator access is required") {
		return
	}
	if s.Configuration == nil || s.DeploymentPlans == nil {
		problem(w, r, http.StatusServiceUnavailable, "configuration_unavailable", "Application configuration is unavailable", nil)
		return
	}
	var body scopedConfigurationRequest
	if err := readJSON(r, &body); err != nil || body.ExpectedRevisionNumber == nil || body.PlanRevisionID == nil || body.PlanRevisionNumber == nil || body.Entries == nil || body.Remove == nil || body.PublicBuildDisclosureAcknowledged == nil {
		problem(w, r, http.StatusUnprocessableEntity, "invalid_configuration", "Configuration input is invalid", map[string]string{"body": "Must be a complete request object"})
		return
	}
	appID := r.PathValue("appId")
	plan, err := s.DeploymentPlans.Get(r.Context(), appID)
	if err != nil {
		deploymentPlanProblem(w, r, err)
		return
	}
	if plan.ID != *body.PlanRevisionID || plan.RevisionNumber != *body.PlanRevisionNumber || plan.State != deploymentplans.RevisionAccepted {
		problem(w, r, http.StatusConflict, "configuration_review_required", "Review the accepted deployment plan before saving scoped configuration", nil)
		return
	}
	if plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode {
		problem(w, r, http.StatusUnprocessableEntity, "invalid_configuration", "Scoped configuration requires a generated deployment plan", nil)
		return
	}
	input := appconfig.ScopedReplaceInput{
		ExpectedRevisionNumber: *body.ExpectedRevisionNumber,
		PlanRevisionID:         plan.ID,
		PlanRevisionNumber:     plan.RevisionNumber,
		Components:             make([]appconfig.ComponentTarget, 0, len(plan.Plan.Components)),
		Entries:                make([]appconfig.ScopedValueInput, 0, len(*body.Entries)),
		Remove:                 make([]appconfig.ScopedKey, 0, len(*body.Remove)),
	}
	for _, component := range plan.Plan.Components {
		input.Components = append(input.Components, appconfig.ComponentTarget{Name: component.Name, Role: component.Role})
	}
	for _, entry := range *body.Entries {
		if entry.Sensitive == nil || entry.Value == nil || (entry.PreserveStoredSecret && (!*entry.Sensitive || *entry.Value != "")) {
			problem(w, r, http.StatusUnprocessableEntity, "invalid_configuration", "Configuration input is invalid", map[string]string{"entries": "Each entry needs a valid sensitivity and value"})
			return
		}
		if entry.Phase == string(appconfig.PhaseBuild) && !*body.PublicBuildDisclosureAcknowledged {
			problem(w, r, http.StatusUnprocessableEntity, "invalid_configuration", "Configuration input is invalid", map[string]string{"entries": "Acknowledge that public build values may appear in browser assets"})
			return
		}
		value := entry.Value
		if entry.PreserveStoredSecret {
			value = nil
		}
		sensitivity := appconfig.SensitivityPublic
		if *entry.Sensitive {
			sensitivity = appconfig.SensitivitySecret
		}
		input.Entries = append(input.Entries, appconfig.ScopedValueInput{
			ScopedKey:   appconfig.ScopedKey{Key: entry.Key, Phase: appconfig.Phase(entry.Phase), Component: entry.TargetComponent},
			Sensitivity: sensitivity,
			Value:       value,
		})
	}
	for _, key := range *body.Remove {
		input.Remove = append(input.Remove, appconfig.ScopedKey{Key: key.Key, Phase: appconfig.Phase(key.Phase), Component: key.TargetComponent})
	}
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	configuration, err := s.Configuration.ReplaceScoped(r.Context(), appID, actor, input)
	if err != nil {
		configurationProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, configurationResponse(configuration))
}
