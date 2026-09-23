package controller

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func (s *Server) getApplicationDeploymentPlan(w http.ResponseWriter, r *http.Request) {
	if s.DeploymentPlans == nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Deployment plan storage is unavailable", nil)
		return
	}
	revision, err := s.DeploymentPlans.Get(r.Context(), r.PathValue("appId"))
	if err != nil {
		deploymentPlanProblem(w, r, err)
		return
	}
	if revision.RevisionNumber == 0 {
		problem(w, r, http.StatusNotFound, "deployment_plan_not_found", "No deployment plan has been accepted", nil)
		return
	}
	writeJSON(w, http.StatusOK, contractDeploymentPlanRevision(revision))
}

func (s *Server) acceptApplicationDeploymentPlan(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationAcceptDeploymentPlan, "deployment_plan_forbidden", "Administrator access is required") {
		return
	}
	if s.DeploymentPlans == nil || s.Apps == nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Deployment plan storage is unavailable", nil)
		return
	}
	var body apicontract.AcceptDeploymentPlanRequest
	if err := readJSON(r, &body); err != nil {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid deployment plan request", nil)
		return
	}
	application, err := s.Apps.Get(r.PathValue("appId"))
	if err != nil {
		problem(w, r, http.StatusNotFound, "app_not_found", "Application was not found", nil)
		return
	}
	inspection, err := s.inspectApplicationSource(r, application)
	if err != nil {
		inspectionProblem(w, r, err)
		return
	}
	if inspection.Analysis.StructuralFingerprint != body.ExpectedSourceStructuralFingerprint {
		problem(w, r, http.StatusConflict, "deployment_plan_review_required", "Project structure changed; review how Rig will run it again", nil)
		return
	}
	candidate, ok := findPlanCandidate(inspection.Analysis.Candidates, body.CandidateID)
	if !ok || candidate.Digest != body.ExpectedCandidateDigest || candidate.Kind != projectanalysis.PlanKindJavaScript || candidate.Status == projectanalysis.StatusUnsupported {
		problem(w, r, http.StatusConflict, "deployment_plan_review_required", "The inferred deployment plan changed; review it again", nil)
		return
	}
	plan, err := acceptedGeneratedPlan(candidate, inspection, body)
	if err != nil {
		var planErr *deploymentplans.Error
		if errors.As(err, &planErr) {
			problem(w, r, http.StatusUnprocessableEntity, planErr.Code, "Deployment plan fields are invalid", planErr.Fields)
			return
		}
		problem(w, r, http.StatusConflict, "deployment_plan_review_required", "The inferred deployment plan needs more information", nil)
		return
	}
	revision, err := s.DeploymentPlans.Replace(r.Context(), application.ID, sourceOwner(r), deploymentplans.ReplaceInput{ExpectedRevisionNumber: body.ExpectedRevisionNumber, Plan: plan})
	if err != nil {
		deploymentPlanProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contractDeploymentPlanRevision(revision))
}

func (s *Server) approveApplicationDeploymentPlanMigration(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationApproveDeploymentMigration, "deployment_plan_forbidden", "Administrator access is required") {
		return
	}
	if s.DeploymentPlans == nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Deployment plan storage is unavailable", nil)
		return
	}
	var body apicontract.ApproveDeploymentPlanMigrationRequest
	if err := readJSON(r, &body); err != nil {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid migration approval request", nil)
		return
	}
	appID := r.PathValue("appId")
	if err := s.DeploymentPlans.ApproveMigration(r.Context(), appID, body.RevisionID, body.RevisionNumber, body.ExpectedApprovalRevision, sourceOwner(r)); err != nil {
		deploymentPlanProblem(w, r, err)
		return
	}
	revision, err := s.DeploymentPlans.Get(r.Context(), appID)
	if err != nil {
		deploymentPlanProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contractDeploymentPlanRevision(revision))
}

func (s *Server) inspectApplicationSource(r *http.Request, application apps.Application) (sourceinspection.Result, error) {
	if application.Source.Type == apps.SourceLocal {
		return sourceinspection.InspectLocalContext(r.Context(), application.Source.Path)
	}
	if application.Source.Type != apps.SourceGitHub || s.Sources == nil {
		return sourceinspection.Result{}, &sourceinspection.Error{Code: "invalid_source"}
	}
	return sourceinspection.InspectGitHub(r.Context(), s.Sources, sourceOwner(r), sourceinspection.GitHubSource{
		ConnectionID: application.Source.ConnectionID, InstallationID: application.Source.InstallationID,
		RepositoryID: application.Source.RepositoryID, Branch: application.Source.TrackedBranch, ComposePath: application.Source.ComposePath,
	})
}

func findPlanCandidate(candidates []projectanalysis.DeploymentPlanCandidate, id string) (projectanalysis.DeploymentPlanCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return projectanalysis.DeploymentPlanCandidate{}, false
}

func acceptedGeneratedPlan(candidate projectanalysis.DeploymentPlanCandidate, inspection sourceinspection.Result, body apicontract.AcceptDeploymentPlanRequest) (deploymentplans.Plan, error) {
	for _, field := range candidate.MissingFields {
		if deploymentPlanFieldCanBeOverridden(field) {
			continue
		}
		return deploymentplans.Plan{}, errors.New("unresolved inferred topology")
	}
	if body.PackageManager != "npm" && body.PackageManager != "pnpm" && body.PackageManager != "yarn" {
		return deploymentplans.Plan{}, &deploymentplans.Error{Code: "invalid_deployment_plan", Fields: map[string]string{"packageManager": "Use npm, pnpm, or yarn"}}
	}
	if err := deploymentplans.ValidateCommand(body.InstallBehavior); err != nil {
		return deploymentplans.Plan{}, &deploymentplans.Error{Code: "invalid_deployment_plan", Fields: map[string]string{"installBehavior": "Install command must be a bounded single line"}}
	}
	inputs := make(map[string]apicontract.DeploymentPlanComponentInput, len(body.Components))
	for _, input := range body.Components {
		if input.ComponentID == "" {
			return deploymentplans.Plan{}, errors.New("missing component identity")
		}
		if _, duplicate := inputs[input.ComponentID]; duplicate {
			return deploymentplans.Plan{}, errors.New("duplicate component input")
		}
		inputs[input.ComponentID] = input
	}
	if len(inputs) != len(candidate.Components) || len(inputs) == 0 {
		return deploymentplans.Plan{}, errors.New("component inputs do not match analysis")
	}
	plan := deploymentplans.Plan{
		Strategy: deploymentplans.StrategyGeneratedNode,
		Detector: deploymentplans.Detector{Name: "projectanalysis", Version: inspection.Analysis.SchemaVersion, SourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint},
	}
	resolvedDigest := inspection.ResolvedSHA
	if resolvedDigest == "" {
		resolvedDigest = inspection.Analysis.StructuralFingerprint
	}
	plan.Source = deploymentplans.SourceIdentity{Provider: inspection.Source.Type, RepositoryID: inspection.Source.RepositoryID, ResolvedDigest: resolvedDigest}
	packageEvidence := evidenceStrings(candidate.PackageManager.Evidence, "package-manager")
	installEvidence := []string{"analysis:install-behavior"}
	packageOrigin := deploymentplans.ProvenanceInferred
	packageConfidence := confidenceScore(candidate.PackageManager.Confidence)
	if body.PackageManager != candidate.PackageManager.Name {
		packageOrigin, packageConfidence, packageEvidence = deploymentplans.ProvenanceUser, 100, []string{"user:package-manager"}
	} else if slices.Contains(candidate.MissingFields, "package_manager.version") {
		packageOrigin, packageConfidence, packageEvidence = deploymentplans.ProvenanceUser, 100, []string{"user:package-manager-review"}
	}
	installOrigin, installConfidence := deploymentplans.ProvenanceInferred, 90
	if candidate.Install == nil || body.InstallBehavior != candidate.Install.Command {
		installOrigin, installConfidence, installEvidence = deploymentplans.ProvenanceUser, 100, []string{"user:install-behavior"}
	} else {
		installConfidence = confidenceScore(candidate.Install.Confidence)
		installEvidence = evidenceStrings(candidate.Install.Evidence, "install-behavior")
	}
	installDirectory := candidate.RootDirectory
	if candidate.Install != nil {
		installDirectory = candidate.Install.WorkingDirectory
	}
	if installDirectory == "" {
		installDirectory = "."
	}
	migrationCount := 0
	for _, component := range candidate.Components {
		input, exists := inputs[component.ID]
		if !exists || input.InternalPort < 1 || input.InternalPort > 65535 || !strings.HasPrefix(input.HealthProbe, "/") {
			return deploymentplans.Plan{}, errors.New("component input is incomplete")
		}
		if err := deploymentplans.ValidateCommand(input.RunCommand); err != nil {
			return deploymentplans.Plan{}, &deploymentplans.Error{Code: "invalid_deployment_plan", Fields: map[string]string{"runCommand": "Run command must be a bounded single line"}}
		}
		if input.BuildCommand != "" {
			if err := deploymentplans.ValidateCommand(input.BuildCommand); err != nil {
				return deploymentplans.Plan{}, &deploymentplans.Error{Code: "invalid_deployment_plan", Fields: map[string]string{"buildCommand": "Build command must be a bounded single line"}}
			}
		}
		if slices.Contains(candidate.MissingFields, "components."+component.ID+".build") && input.BuildCommand == "" {
			return deploymentplans.Plan{}, errors.New("required build command is missing")
		}
		root := component.RootDirectory
		if root == "" {
			root = "."
		}
		plan.Components = append(plan.Components, deploymentplans.Component{
			Name: component.ID, Role: component.Kind, RootDirectory: root, PackageManager: body.PackageManager,
			InstallBehavior: body.InstallBehavior, InstallDirectory: installDirectory, NodeVersion: input.NodeVersion, BuildCommand: input.BuildCommand,
			RunCommand: input.RunCommand, InternalPort: uint16(input.InternalPort), HealthProbe: input.HealthProbe,
		})
		plan.FieldProvenance = append(plan.FieldProvenance,
			deploymentplans.FieldProvenance{Field: "components." + component.ID + ".packageManager", Origin: packageOrigin, Confidence: packageConfidence, Evidence: packageEvidence},
			deploymentplans.FieldProvenance{Field: "components." + component.ID + ".installBehavior", Origin: installOrigin, Confidence: installConfidence, Evidence: installEvidence},
			deploymentplans.FieldProvenance{Field: "components." + component.ID + ".installDirectory", Origin: deploymentplans.ProvenanceInferred, Confidence: 90, Evidence: []string{"analysis:install-directory"}},
		)
		appendComponentProvenance(&plan, component, candidate.NodeVersion, input)
		if component.Migration != nil {
			migrationCount++
			if component.MigrationFingerprint == "" {
				return deploymentplans.Plan{}, errors.New("migration evidence is incomplete")
			}
			command := body.MigrationCommand
			if command == "" {
				command = component.Migration.Command
			}
			plan.Migration = &deploymentplans.Migration{
				ComponentName: component.ID, RootDirectory: root, Command: command,
				EnvironmentKeys: append([]string(nil), component.Migration.EnvironmentKeys...), EvidenceDigest: component.MigrationFingerprint,
				Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending},
			}
		}
	}
	if migrationCount > 1 || (migrationCount == 0 && body.MigrationCommand != "") {
		return deploymentplans.Plan{}, errors.New("migration input does not match analysis")
	}
	if _, err := deploymentplans.CanonicalDigest(plan); err != nil {
		return deploymentplans.Plan{}, err
	}
	return plan, nil
}

func deploymentPlanFieldCanBeOverridden(field string) bool {
	if field == projectanalysis.FieldPackageManager || field == "package_manager.version" || field == projectanalysis.FieldInstallBehavior || field == "node_version" {
		return true
	}
	return strings.HasSuffix(field, ".build") || strings.HasSuffix(field, ".run") || strings.HasSuffix(field, ".internal_port") || strings.HasSuffix(field, ".health_probe")
}

func appendComponentProvenance(plan *deploymentplans.Plan, component projectanalysis.Component, nodeVersion projectanalysis.InferredValue, input apicontract.DeploymentPlanComponentInput) {
	plan.FieldProvenance = append(plan.FieldProvenance,
		deploymentplans.FieldProvenance{Field: "components." + component.ID + ".role", Origin: deploymentplans.ProvenanceInferred, Confidence: 90, Evidence: evidenceStrings(component.Evidence, "analysis:role")},
		deploymentplans.FieldProvenance{Field: "components." + component.ID + ".rootDirectory", Origin: deploymentplans.ProvenanceInferred, Confidence: 90, Evidence: []string{"analysis:root-directory"}},
	)
	values := []struct {
		field, actual, inferred string
		evidence                []projectanalysis.Evidence
		confidence              string
	}{
		{"nodeVersion", input.NodeVersion, nodeVersion.Value, nodeVersion.Evidence, nodeVersion.Confidence},
		{"runCommand", input.RunCommand, commandValue(component.Run), commandEvidence(component.Run), commandConfidence(component.Run)},
		{"internalPort", stringInt(input.InternalPort), inferredValue(component.InternalPort), valueEvidence(component.InternalPort), valueConfidence(component.InternalPort)},
		{"healthProbe", input.HealthProbe, probeValue(component.HealthProbe), probeEvidence(component.HealthProbe), probeConfidence(component.HealthProbe)},
	}
	if input.BuildCommand != "" {
		values = append(values, struct {
			field, actual, inferred string
			evidence                []projectanalysis.Evidence
			confidence              string
		}{"buildCommand", input.BuildCommand, commandValue(component.Build), commandEvidence(component.Build), commandConfidence(component.Build)})
	}
	for _, value := range values {
		origin, confidence, evidence := deploymentplans.ProvenanceInferred, confidenceScore(value.confidence), evidenceStrings(value.evidence, "analysis:"+value.field)
		if value.inferred == "" || value.actual != value.inferred {
			origin, confidence, evidence = deploymentplans.ProvenanceUser, 100, []string{"user:" + value.field}
		}
		plan.FieldProvenance = append(plan.FieldProvenance, deploymentplans.FieldProvenance{Field: "components." + component.ID + "." + value.field, Origin: origin, Confidence: confidence, Evidence: evidence})
	}
}

func commandValue(value *projectanalysis.Command) string {
	if value == nil {
		return ""
	}
	return value.Command
}
func commandEvidence(value *projectanalysis.Command) []projectanalysis.Evidence {
	if value == nil {
		return nil
	}
	return value.Evidence
}
func commandConfidence(value *projectanalysis.Command) string {
	if value == nil {
		return ""
	}
	return value.Confidence
}
func inferredValue(value *projectanalysis.InferredValue) string {
	if value == nil {
		return ""
	}
	return value.Value
}
func valueEvidence(value *projectanalysis.InferredValue) []projectanalysis.Evidence {
	if value == nil {
		return nil
	}
	return value.Evidence
}
func valueConfidence(value *projectanalysis.InferredValue) string {
	if value == nil {
		return ""
	}
	return value.Confidence
}
func probeValue(value *projectanalysis.HealthProbe) string {
	if value == nil {
		return ""
	}
	return value.Path
}
func probeEvidence(value *projectanalysis.HealthProbe) []projectanalysis.Evidence {
	if value == nil {
		return nil
	}
	return value.Evidence
}
func probeConfidence(value *projectanalysis.HealthProbe) string {
	if value == nil {
		return ""
	}
	return value.Confidence
}
func stringInt(value int) string {
	return strconv.Itoa(value)
}

func evidenceStrings(values []projectanalysis.Evidence, fallback string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		entry := value.Code
		if value.Path != "" {
			entry += ":" + value.Path
		}
		if value.Field != "" {
			entry += ":" + value.Field
		}
		if entry != "" {
			result = append(result, entry)
		}
	}
	if len(result) == 0 {
		result = append(result, fallback)
	}
	sort.Strings(result)
	return slices.Compact(result)
}

func confidenceScore(value string) int {
	switch value {
	case projectanalysis.ConfidenceHigh:
		return 90
	case projectanalysis.ConfidenceMedium:
		return 60
	case projectanalysis.ConfidenceLow:
		return 30
	default:
		return 50
	}
}

func deploymentPlanProblem(w http.ResponseWriter, r *http.Request, err error) {
	if deploymentplans.IsCode(err, "app_not_found") {
		problem(w, r, http.StatusNotFound, "app_not_found", "Application was not found", nil)
		return
	}
	if deploymentplans.IsCode(err, "deployment_plan_conflict") {
		problem(w, r, http.StatusConflict, "deployment_plan_conflict", "The accepted deployment plan changed; reload and try again", nil)
		return
	}
	if deploymentplans.IsCode(err, "migration_approval_conflict") {
		problem(w, r, http.StatusConflict, "migration_approval_conflict", "The migration approval changed; reload and try again", nil)
		return
	}
	if deploymentplans.IsCode(err, "deployment_plan_unavailable") {
		problem(w, r, http.StatusNotFound, "deployment_plan_not_found", "No matching accepted deployment plan exists", nil)
		return
	}
	if deploymentplans.IsCode(err, "invalid_deployment_plan") {
		var planErr *deploymentplans.Error
		errors.As(err, &planErr)
		problem(w, r, http.StatusUnprocessableEntity, planErr.Code, "Deployment plan fields are invalid", planErr.Fields)
		return
	}
	problem(w, r, http.StatusInternalServerError, "internal_error", "Could not access the deployment plan", nil)
}

func contractDeploymentPlanRevision(value deploymentplans.DeploymentPlanRevision) apicontract.DeploymentPlanRevision {
	result := apicontract.DeploymentPlanRevision{
		RevisionID: value.ID, RevisionNumber: value.RevisionNumber, Strategy: string(value.Plan.Strategy), State: string(value.State),
		CanonicalDigest: value.CanonicalDigest, AcceptedBy: value.AcceptedBy, AcceptedAt: value.AcceptedAt,
		Source:     apicontract.DeploymentPlanSource{Provider: value.Plan.Source.Provider, RepositoryID: value.Plan.Source.RepositoryID, ResolvedDigest: value.Plan.Source.ResolvedDigest},
		Detector:   apicontract.DeploymentPlanDetector{Name: value.Plan.Detector.Name, Version: value.Plan.Detector.Version, SourceStructuralFingerprint: value.Plan.Detector.SourceStructuralFingerprint},
		Components: make([]apicontract.DeploymentPlanComponent, 0, len(value.Plan.Components)), FieldProvenance: make([]apicontract.DeploymentPlanFieldProvenance, 0, len(value.Plan.FieldProvenance)),
	}
	for _, component := range value.Plan.Components {
		result.Components = append(result.Components, apicontract.DeploymentPlanComponent{Name: component.Name, Role: component.Role, RootDirectory: component.RootDirectory, PackageManager: component.PackageManager, InstallBehavior: component.InstallBehavior, InstallDirectory: component.InstallDirectory, NodeVersion: component.NodeVersion, BuildCommand: component.BuildCommand, RunCommand: component.RunCommand, InternalPort: int(component.InternalPort), HealthProbe: component.HealthProbe})
	}
	for _, field := range value.Plan.FieldProvenance {
		result.FieldProvenance = append(result.FieldProvenance, apicontract.DeploymentPlanFieldProvenance{Field: field.Field, Origin: string(field.Origin), Confidence: field.Confidence, Evidence: append([]string(nil), field.Evidence...)})
	}
	if value.Plan.Migration != nil {
		result.Migration = apicontract.DeploymentPlanMigration{
			Present: true, ComponentName: value.Plan.Migration.ComponentName, RootDirectory: value.Plan.Migration.RootDirectory,
			Command: value.Plan.Migration.Command, EnvironmentKeys: append([]string(nil), value.Plan.Migration.EnvironmentKeys...), EvidenceDigest: value.Plan.Migration.EvidenceDigest,
			ApprovalStatus: string(value.Plan.Migration.Approval.Status), ApprovedBy: value.Plan.Migration.Approval.ActorID, ApprovedAt: value.Plan.Migration.Approval.At,
		}
	} else {
		result.Migration = apicontract.DeploymentPlanMigration{Present: false}
	}
	return result
}
