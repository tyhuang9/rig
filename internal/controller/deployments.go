package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
)

func (s *Server) appExists(w http.ResponseWriter, r *http.Request) bool {
	if s.Apps == nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Application storage is unavailable", nil)
		return false
	}
	if _, err := s.Apps.Get(r.PathValue("appId")); err != nil {
		problem(w, r, http.StatusNotFound, "app_not_found", "Application was not found", nil)
		return false
	}
	return true
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	if s.Deployments == nil {
		problem(w, r, http.StatusServiceUnavailable, "deployment_history_unavailable", "Deployment history is unavailable", nil)
		return
	}
	values, err := s.Deployments.List(r.Context(), r.PathValue("appId"), 100)
	if err != nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not list deployments", nil)
		return
	}
	items := make([]apicontract.Deployment, 0, len(values))
	for _, value := range values {
		items = append(items, contractDeployment(value))
	}
	writeJSON(w, http.StatusOK, apicontract.DeploymentList{Items: items})
}

func (s *Server) listReleases(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	if s.Deployments == nil {
		problem(w, r, http.StatusServiceUnavailable, "release_history_unavailable", "Release history is unavailable", nil)
		return
	}
	values, err := s.Deployments.Releases(r.Context(), r.PathValue("appId"), 100)
	if err != nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not list releases", nil)
		return
	}
	items := make([]apicontract.Release, 0, len(values))
	for _, value := range values {
		items = append(items, contractRelease(value))
	}
	writeJSON(w, http.StatusOK, apicontract.ReleaseList{Items: items})
}

func (s *Server) deployApplication(w http.ResponseWriter, r *http.Request) {
	s.createDeploymentJob(w, r, "", jobs.ConfigurationCurrent)
}

func (s *Server) deployRelease(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	if !s.ComposeRuntime {
		problem(w, r, http.StatusConflict, "capability_unavailable", "Runtime actions are unavailable in this configuration", nil)
		return
	}
	if s.Deployments == nil {
		problem(w, r, http.StatusServiceUnavailable, "release_history_unavailable", "Release history is unavailable", nil)
		return
	}
	releaseID := r.PathValue("releaseId")
	if _, err := s.Deployments.Release(r.Context(), r.PathValue("appId"), releaseID); err != nil {
		problem(w, r, http.StatusNotFound, "release_not_found", "Release was not found", nil)
		return
	}
	var request apicontract.DeployReleaseRequest
	if err := readJSON(r, &request); err != nil || (request.ConfigurationMode != string(jobs.ConfigurationCurrent) && request.ConfigurationMode != string(jobs.ConfigurationOriginal)) {
		problem(w, r, http.StatusUnprocessableEntity, "invalid_deployment", "Deployment request is invalid", map[string]string{"configurationMode": "Must be current or original"})
		return
	}
	s.enqueueDeployment(w, r, releaseID, jobs.ConfigurationMode(request.ConfigurationMode))
}

func (s *Server) createDeploymentJob(w http.ResponseWriter, r *http.Request, releaseID string, mode jobs.ConfigurationMode) {
	if !s.appExists(w, r) {
		return
	}
	if !s.runtimeAvailable() {
		problem(w, r, http.StatusConflict, "capability_unavailable", "Runtime actions are unavailable in this configuration", nil)
		return
	}
	s.enqueueDeployment(w, r, releaseID, mode)
}

func (s *Server) enqueueDeployment(w http.ResponseWriter, r *http.Request, releaseID string, mode jobs.ConfigurationMode) {
	if s.Jobs == nil {
		problem(w, r, http.StatusServiceUnavailable, "jobs_unavailable", "Job processing is unavailable", nil)
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if len(idempotencyKey) > 200 {
		problem(w, r, http.StatusUnprocessableEntity, "invalid_idempotency_key", "Idempotency key is invalid", map[string]string{"Idempotency-Key": "Must be at most 200 bytes"})
		return
	}
	actorID := r.Context().Value(principalKey{}).(principal).user.ID
	job, created, err := s.Jobs.CreateWithInput(jobs.CreateRequest{
		Type:           "deploy",
		ResourceType:   "application",
		ResourceID:     r.PathValue("appId"),
		IdempotencyKey: idempotencyKey,
		RequestedBy:    actorID,
		Input:          jobs.DeploymentInput{ReleaseID: releaseID, ConfigurationMode: mode},
	})
	switch {
	case err == nil:
		writeJSON(w, map[bool]int{true: http.StatusAccepted, false: http.StatusOK}[created], apicontract.JobMutationResponse{Job: contractJob(job), Created: created})
	case errors.Is(err, jobs.ErrIdempotency):
		problem(w, r, http.StatusConflict, "idempotency_conflict", "Idempotency key conflicts with the original deployment request", nil)
	case errors.Is(err, jobs.ErrApplicationBusy):
		problem(w, r, http.StatusConflict, "application_busy", "Application already has an active mutation", nil)
	case errors.Is(err, jobs.ErrInvalidInput):
		problem(w, r, http.StatusUnprocessableEntity, "invalid_deployment", "Deployment request is invalid", nil)
	default:
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not create deployment job", nil)
	}
}

func (s *Server) runtimeAvailable() bool { return s.ComposeRuntime || s.FakeRuntime }

func (s *Server) listRuntimeApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	if s.Deployments == nil {
		problem(w, r, http.StatusServiceUnavailable, "runtime_approvals_unavailable", "Runtime approvals are unavailable", nil)
		return
	}
	values, err := s.Deployments.Approvals(r.Context(), r.PathValue("appId"))
	if err != nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not list runtime approvals", nil)
		return
	}
	items := make([]apicontract.RuntimeApproval, 0, len(values))
	for _, value := range values {
		items = append(items, contractRuntimeApproval(value))
	}
	writeJSON(w, http.StatusOK, apicontract.RuntimeApprovalList{Items: items})
}

func (s *Server) grantRuntimeApproval(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	if s.Deployments == nil {
		problem(w, r, http.StatusServiceUnavailable, "runtime_approvals_unavailable", "Runtime approvals are unavailable", nil)
		return
	}
	var request apicontract.GrantRuntimeApprovalRequest
	if err := readJSON(r, &request); err != nil || !controllerLowerHex(request.Fingerprint, 64) {
		problem(w, r, http.StatusUnprocessableEntity, "invalid_approval", "Runtime approval request is invalid", map[string]string{"fingerprint": "Must be a lowercase SHA-256 fingerprint"})
		return
	}
	actorID := r.Context().Value(principalKey{}).(principal).user.ID
	approval, created, err := s.Deployments.Grant(r.Context(), r.PathValue("appId"), actorID, request.Fingerprint)
	switch {
	case err == nil:
		writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], apicontract.RuntimeApprovalMutationResponse{Approval: contractRuntimeApproval(approval), Created: created})
	case errors.Is(err, deployments.ErrInvalidDeployment):
		problem(w, r, http.StatusNotFound, "finding_not_found", "Approval-required finding was not found", nil)
	default:
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not grant runtime approval", nil)
	}
}

func (s *Server) revokeRuntimeApproval(w http.ResponseWriter, r *http.Request) {
	if !s.appExists(w, r) {
		return
	}
	if s.Deployments == nil {
		problem(w, r, http.StatusServiceUnavailable, "runtime_approvals_unavailable", "Runtime approvals are unavailable", nil)
		return
	}
	if uuid.Validate(r.PathValue("approvalId")) != nil {
		problem(w, r, http.StatusNotFound, "approval_not_found", "Runtime approval was not found", nil)
		return
	}
	actorID := r.Context().Value(principalKey{}).(principal).user.ID
	approval, err := s.Deployments.Revoke(r.Context(), r.PathValue("appId"), r.PathValue("approvalId"), actorID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, apicontract.RuntimeApprovalResponse{Approval: contractRuntimeApproval(approval)})
	case errors.Is(err, deployments.ErrApprovalInUse):
		problem(w, r, http.StatusConflict, "approval_in_use", "Runtime approval is in use by an active deployment", nil)
	case errors.Is(err, deployments.ErrNotFound):
		problem(w, r, http.StatusNotFound, "approval_not_found", "Runtime approval was not found", nil)
	default:
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not revoke runtime approval", nil)
	}
}

func (s *Server) resumeJob(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
		return
	}
	actorID := r.Context().Value(principalKey{}).(principal).user.ID
	job, err := s.Jobs.Get(r.PathValue("jobId"))
	if err != nil || job.Type != "deploy" || job.ResourceType != "application" || job.RequestedBy != actorID {
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
		return
	}
	if s.Apps == nil {
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
		return
	}
	if _, err := s.Apps.Get(job.ResourceID); err != nil {
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
		return
	}
	if !s.ComposeRuntime {
		problem(w, r, http.StatusConflict, "capability_unavailable", "Compose runtime is unavailable in this configuration", nil)
		return
	}
	job, err = s.Jobs.Resume(job.ID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, apicontract.JobResponse{Job: contractJob(job)})
	case errors.Is(err, jobs.ErrJobNotPaused):
		problem(w, r, http.StatusConflict, "job_not_paused", "Job is not waiting for user action", nil)
	case errors.Is(err, jobs.ErrApprovalRequired):
		problem(w, r, http.StatusConflict, "approval_required", "Deployment still requires runtime approval", nil)
	case errors.Is(err, jobs.ErrJobNotFound):
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
	default:
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not resume job", nil)
	}
}

func contractDeployment(value deployments.Deployment) apicontract.Deployment {
	findings := make([]apicontract.PolicyFinding, 0, len(value.Findings))
	for _, finding := range value.Findings {
		findings = append(findings, apicontract.PolicyFinding{ID: finding.ID, DeploymentID: finding.DeploymentID, PolicyVersion: finding.PolicyVersion, Capability: finding.Capability, Scope: finding.Scope, Fingerprint: finding.Fingerprint, Disposition: finding.Disposition, CreatedAt: formatContractTime(finding.CreatedAt)})
	}
	return apicontract.Deployment{ID: value.ID, AppID: value.AppID, ReleaseID: value.ReleaseID, JobID: value.JobID, MachineID: value.MachineID, Status: string(value.Status), ConfigurationMode: value.ConfigurationMode, ActualConfigurationRevisionID: value.ActualConfigurationRevisionID, ActualConfigurationRevisionNumber: value.ActualConfigurationRevisionNumber, StartedAt: formatContractTime(value.StartedAt), FinishedAt: formatContractTime(value.FinishedAt), DiagnosticCode: value.DiagnosticCode, FailureSummary: value.FailureSummary, Findings: findings}
}

func contractRelease(value deployments.Release) apicontract.Release {
	return apicontract.Release{ID: value.ID, AppID: value.AppID, SourceProvider: value.SourceProvider, RepositoryID: value.RepositoryID, RepositoryOwner: value.RepositoryOwner, RepositoryName: value.RepositoryName, TrackedRef: value.TrackedRef, ResolvedSha: value.ResolvedSHA, SourceCommitSha: value.SourceCommitSHA, SourceBranch: value.SourceBranch, ComposePath: value.ComposePath, ArchiveSha256: value.ArchiveSHA256, WorkspaceState: value.WorkspaceState, ConfigurationRevisionID: value.ConfigurationRevisionID, ConfigurationRevisionNumber: value.ConfigurationRevisionNumber, CreatedAt: formatContractTime(value.CreatedAt)}
}

func contractRuntimeApproval(value deployments.Approval) apicontract.RuntimeApproval {
	return apicontract.RuntimeApproval{ID: value.ID, AppID: value.AppID, PolicyVersion: value.PolicyVersion, Capability: value.Capability, Scope: value.Scope, Fingerprint: value.Fingerprint, GrantedBy: value.GrantedBy, GrantedAt: formatContractTime(value.GrantedAt), RevokedBy: value.RevokedBy, RevokedAt: formatContractTime(value.RevokedAt)}
}

func formatContractTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func controllerLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
