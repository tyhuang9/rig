package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// LocalRouteIngress exposes only a fresh, live observation of generated ingress.
type LocalRouteIngress interface {
	WithObservation(context.Context, string, func(context.Context, generatedingress.Observation) error) error
}

// LocalRouteRuntimeState exposes only durable generated deployment and active
// head reads. It does not grant the controller a route mutation path.
type LocalRouteRuntimeState interface {
	Active(context.Context, string) (generatedruntimestate.ActiveHead, error)
	Get(context.Context, string, string) (generatedruntimestate.Deployment, error)
}

func (s *Server) getApplicationLocalRoute(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	response := apicontract.LocalRoute{Status: "unverified"}
	respond := func(status, reason string) {
		response.Status, response.Reason = status, reason
		response.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
		writeJSON(w, http.StatusOK, response)
	}
	if !s.GeneratedRuntime {
		respond("unavailable", "generated_runtime_disabled")
		return
	}
	if s.GeneratedRuntimeState == nil || s.GeneratedIngress == nil || s.Deployments == nil {
		respond("unverified", "runtime_state_unavailable")
		return
	}
	appID := r.PathValue("appId")
	head, err := s.GeneratedRuntimeState.Active(r.Context(), appID)
	if errors.Is(err, generatedruntimestate.ErrNotFound) || (err == nil && head.DeploymentID == "") {
		respond("unavailable", "no_active_deployment")
		return
	}
	if err != nil {
		respond("unverified", "runtime_state_unavailable")
		return
	}
	runtimeDeployment, err := s.GeneratedRuntimeState.Get(r.Context(), appID, head.DeploymentID)
	if err != nil {
		respond("unverified", "runtime_state_unavailable")
		return
	}
	deployment, err := s.Deployments.Get(r.Context(), appID, head.DeploymentID)
	if err != nil {
		respond("unverified", "runtime_state_unavailable")
		return
	}
	if !localRouteProvenanceMatches(appID, head, runtimeDeployment, deployment) {
		respond("unverified", "provenance_mismatch")
		return
	}
	var verified verifiedLocalRoute
	// The final active-head read must happen under the same ingress mutex that
	// protects a Caddy switch. Otherwise a new route can go live after Observe
	// returns but before its head is persisted, and the old head could stamp the
	// new Caddy state as verified.
	err = s.GeneratedIngress.WithObservation(r.Context(), appID, func(observationContext context.Context, observation generatedingress.Observation) error {
		if observation.ObservedAt.IsZero() || !localRouteObservationMatches(appID, head, runtimeDeployment, observation) {
			return errLocalRouteAttestation
		}
		current, readErr := s.GeneratedRuntimeState.Active(observationContext, appID)
		if readErr != nil {
			return errLocalRouteRuntimeState
		}
		if current.Generation != head.Generation || current.DeploymentID != head.DeploymentID || current.ReleaseID != head.ReleaseID || current.Slot != head.Slot {
			return errLocalRouteProvenance
		}
		verified = verifiedLocalRoute{
			Status: "verified", ObservedAt: observation.ObservedAt.UTC().Format(time.RFC3339Nano), Scope: "controller_loopback", URL: observation.URL,
			DeploymentID: deployment.ID, ReleaseID: deployment.ReleaseID,
			ConfigurationRevisionID: deployment.ActualConfigurationRevisionID, ConfigurationRevisionNumber: deployment.ActualConfigurationRevisionNumber,
			PlanRevisionID: deployment.DeploymentPlanRevisionID, PlanRevisionNumber: deployment.DeploymentPlanRevisionNumber,
		}
		return nil
	})
	switch {
	case errors.Is(err, errLocalRouteRuntimeState):
		respond("unverified", "runtime_state_unavailable")
	case errors.Is(err, errLocalRouteProvenance):
		respond("unverified", "provenance_mismatch")
	case err != nil || verified.Status != "verified":
		respond("unverified", "attestation_failed")
	default:
		writeJSON(w, http.StatusOK, verified)
	}
}

var (
	errLocalRouteAttestation  = errors.New("local route attestation failed")
	errLocalRouteRuntimeState = errors.New("local route runtime state unavailable")
	errLocalRouteProvenance   = errors.New("local route provenance changed")
)

// The generated OpenAPI model omits optional zero-valued fields. A verified
// route must include configuration revision zero and its empty ID when the app
// has not saved scoped configuration, so serialize that variant explicitly.
type verifiedLocalRoute struct {
	Status                      string `json:"status"`
	ObservedAt                  string `json:"observedAt"`
	Scope                       string `json:"scope"`
	URL                         string `json:"url"`
	DeploymentID                string `json:"deploymentId"`
	ReleaseID                   string `json:"releaseId"`
	ConfigurationRevisionID     string `json:"configurationRevisionId"`
	ConfigurationRevisionNumber int64  `json:"configurationRevisionNumber"`
	PlanRevisionID              string `json:"planRevisionId"`
	PlanRevisionNumber          int64  `json:"planRevisionNumber"`
}

func localRouteProvenanceMatches(appID string, head generatedruntimestate.ActiveHead, runtimeDeployment generatedruntimestate.Deployment, deployment deployments.Deployment) bool {
	return head.AppID == appID && head.DeploymentID != "" && head.ReleaseID != "" && head.Generation > 0 && (head.Slot == string(generatedruntime.SlotBlue) || head.Slot == string(generatedruntime.SlotGreen)) &&
		runtimeDeployment.AppID == appID && runtimeDeployment.DeploymentID == head.DeploymentID && runtimeDeployment.ReleaseID == head.ReleaseID && runtimeDeployment.CandidateSlot == head.Slot && runtimeDeployment.Phase == generatedruntimestate.PhaseSucceeded &&
		deployment.AppID == appID && deployment.ID == head.DeploymentID && deployment.ReleaseID == head.ReleaseID && deployment.Status == deployments.Succeeded && deployment.RuntimeStrategy == deployments.RuntimeGeneratedNode && deployment.ProvenanceInitialized &&
		runtimeDeployment.DeploymentPlanRevisionID != "" && runtimeDeployment.DeploymentPlanRevisionID == deployment.DeploymentPlanRevisionID && runtimeDeployment.DeploymentPlanRevisionNumber > 0 && runtimeDeployment.DeploymentPlanRevisionNumber == deployment.DeploymentPlanRevisionNumber &&
		deployment.ActualConfigurationRevisionNumber >= 0 && (deployment.ActualConfigurationRevisionID == "") == (deployment.ActualConfigurationRevisionNumber == 0)
}

func localRouteObservationMatches(appID string, head generatedruntimestate.ActiveHead, runtimeDeployment generatedruntimestate.Deployment, observation generatedingress.Observation) bool {
	if observation.Slot != generatedruntime.Slot(head.Slot) || !validControllerLoopbackURL(appID, observation.URL) || len(observation.Endpoints) == 0 || len(observation.Endpoints) != len(runtimeDeployment.Components) {
		return false
	}
	opposite, err := generatedruntime.InactiveSlot(generatedruntime.Slot(head.Slot))
	if err != nil {
		return false
	}
	components := make(map[string]generatedruntimestate.Component, len(runtimeDeployment.Components))
	for _, component := range runtimeDeployment.Components {
		description, err := generatedruntime.DescribeInactiveCandidate(appID, component.Name, opposite)
		if err != nil || component.DeploymentID != runtimeDeployment.DeploymentID || component.ContainerID == "" || component.ContainerName != description.ContainerName || component.Slot != head.Slot || component.State != generatedruntimestate.ComponentActive {
			return false
		}
		if _, exists := components[component.Name]; exists {
			return false
		}
		components[component.Name] = component
	}
	seen := make(map[string]bool, len(observation.Endpoints))
	for _, endpoint := range observation.Endpoints {
		component, ok := components[endpoint.Component]
		description, err := generatedruntime.DescribeInactiveCandidate(appID, endpoint.Component, opposite)
		if err != nil || !ok || seen[endpoint.Component] || component.ContainerID != endpoint.ContainerID || endpoint.NetworkName != description.NetworkName || endpoint.NetworkAlias != description.NetworkAlias || endpoint.InternalPort == 0 || (endpoint.Role != generatedruntime.RoleServer && endpoint.Role != generatedruntime.RoleStatic) {
			return false
		}
		seen[endpoint.Component] = true
	}
	return true
}

func validControllerLoopbackURL(appID, raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != appID+".rig.localhost" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	port := parsed.Port()
	if port == "" || strings.HasPrefix(port, "0") {
		return false
	}
	value, err := strconv.Atoi(port)
	return err == nil && value > 0 && value <= 65535 && parsed.Host == parsed.Hostname()+":"+port
}
