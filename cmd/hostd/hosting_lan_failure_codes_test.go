package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

func TestLanTwoAppDeploymentFailureObservationRetainsSafeCodes(t *testing.T) {
	for _, test := range []struct {
		name, status, code, runtimeCode, wantStatus, wantCode, wantRuntime string
	}{
		{"start failure", "failed", "apply_failed", "start_failed", "failed", "apply_failed", "start_failed"},
		{"health failure", "failed", "health_failed", "health_failed", "failed", "health_failed", "health_failed"},
		{"route failure", "failed", "gateway_readiness_failed", "route_switch_failed", "failed", "gateway_readiness_failed", "route_switch_failed"},
		{"storage failure", "failed", "source_storage_full", "internal_error", "failed", "source_storage_full", "internal_error"},
		{"daemon failure", "failed", "runtime_unavailable", "daemon_restarted", "failed", "runtime_unavailable", "daemon_restarted"},
		{"invalid executor result", "failed", "executor_invalid_result", "", "failed", "executor_invalid_result", "none"},
		{"waiting without code", "waiting_user", "", "", "waiting_user", "none", "none"},
		{"attention without code", "needs_attention", "", "insufficient_replacement_capacity", "needs_attention", "none", "insufficient_replacement_capacity"},
		{"unknown values", "status-secret", "code-secret", "runtime-secret", "unknown", "unknown", "unknown"},
		{"embedded known code", "failed\nstatus-secret", "apply_failed\ncode-secret", "start_failed\nruntime-secret", "unknown", "unknown", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := jobs.Job{
				ID: "job-secret", Type: "deploy", ResourceType: "application", ResourceID: "app-secret",
				Status: test.status, ErrorCode: test.code, ErrorDetail: "detail-secret",
				Checkpoint: `{"phase":"checkpoint-secret"}`, RequestedBy: "actor-secret",
				Phase: "phase-secret",
			}
			lists, gets := 0, 0
			observation := lanTwoAppDeploymentFailureObservationFor(context.Background(), "app-secret", "job-secret", job,
				func(_ context.Context, app string, limit int) ([]deployments.Deployment, error) {
					lists++
					if app != "app-secret" || limit != lanTwoAppFailureHistoryLimit {
						t.Fatal("diagnostic history scope changed")
					}
					return []deployments.Deployment{{ID: "deployment-secret", AppID: app, JobID: job.ID, Status: deployments.Failed}}, nil
				},
				func(_ context.Context, app, deployment string) (generatedruntimestate.Deployment, error) {
					gets++
					if app != "app-secret" || deployment != "deployment-secret" {
						t.Fatal("diagnostic runtime scope changed")
					}
					return generatedruntimestate.Deployment{
						AppID: app, DeploymentID: deployment, Phase: generatedruntimestate.PhaseFailed,
						DiagnosticCode: generatedruntimestate.DiagnosticCode(test.runtimeCode),
						Components:     []generatedruntimestate.Component{{Name: "component-secret", State: generatedruntimestate.ComponentFailed}},
					}, nil
				})
			if lists != 1 || gets != 1 || observation.JobRead != "ok" || observation.RuntimeRead != "ok" {
				t.Fatal("diagnostic did not retain the exact one-list/one-read observation")
			}
			if observation.JobStatus != test.wantStatus || observation.JobCode != test.wantCode || observation.RuntimeDiagnostic != test.wantRuntime {
				t.Fatal("diagnostic lost or invented a failure classification")
			}
			line := lanTwoAppDeploymentFailureObservationLine(observation)
			for _, field := range []string{"job_read=ok", "job_status=" + test.wantStatus, "job_code=" + test.wantCode,
				"runtime_diagnostic=" + test.wantRuntime, "failed:1"} {
				if !strings.Contains(line, field) {
					t.Fatalf("diagnostic omitted safe field %q", field)
				}
			}
			if strings.Contains(line, "secret") || strings.ContainsAny(line, "\r\n") {
				t.Fatal("diagnostic disclosed untrusted job or runtime content")
			}
		})
	}
}

func TestLanTwoAppDeploymentFailureObservationRejectsCrossedJob(t *testing.T) {
	for _, field := range []string{"job", "app", "type", "resource_type", "expected_app", "expected_job"} {
		t.Run(field, func(t *testing.T) {
			appID, jobID := "app", "job"
			job := jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: appID,
				Status: "failed", ErrorCode: "apply_failed"}
			switch field {
			case "job":
				job.ID = "other-job-secret"
			case "app":
				job.ResourceID = "other-app-secret"
			case "type":
				job.Type = "other-type-secret"
			case "resource_type":
				job.ResourceType = "other-resource-secret"
			case "expected_app":
				appID, job.ResourceID = "", ""
			case "expected_job":
				jobID, job.ID = "", ""
			}
			observation := lanTwoAppDeploymentFailureObservationFor(context.Background(), appID, jobID, job,
				func(context.Context, string, int) ([]deployments.Deployment, error) {
					t.Fatal("crossed job caused a history read")
					return nil, nil
				}, func(context.Context, string, string) (generatedruntimestate.Deployment, error) {
					t.Fatal("crossed job caused a runtime read")
					return generatedruntimestate.Deployment{}, nil
				})
			if observation.JobRead != "identity_mismatch" || observation.JobCode != "unavailable" ||
				observation.JobStatus != "unavailable" || observation.RuntimeDiagnostic != "unavailable" ||
				observation.DeploymentRead != "unavailable" || observation.RuntimeRead != "unavailable" {
				t.Fatal("crossed job contributed a failure classification")
			}
			if strings.Contains(lanTwoAppDeploymentFailureObservationLine(observation), "secret") {
				t.Fatal("crossed job diagnostic disclosed untrusted content")
			}
		})
	}
}

func TestLanTwoAppDeploymentFailureObservationKeepsJobCodeWithoutRuntime(t *testing.T) {
	for _, stage := range []string{"history", "runtime", "runtime_identity"} {
		t.Run(stage, func(t *testing.T) {
			job := jobs.Job{ID: "job", Type: "deploy", ResourceType: "application", ResourceID: "app",
				Status: "failed", ErrorCode: "source_storage_full"}
			gets := 0
			observation := lanTwoAppDeploymentFailureObservationFor(context.Background(), "app", "job", job,
				func(context.Context, string, int) ([]deployments.Deployment, error) {
					if stage == "history" {
						return nil, errors.New("history-secret")
					}
					return []deployments.Deployment{{ID: "deployment", AppID: "app", JobID: "job"}}, nil
				}, func(context.Context, string, string) (generatedruntimestate.Deployment, error) {
					gets++
					if stage == "runtime" {
						return generatedruntimestate.Deployment{}, errors.New("runtime-secret")
					}
					return generatedruntimestate.Deployment{AppID: "app", DeploymentID: "other-deployment-secret",
						DiagnosticCode: generatedruntimestate.DiagnosticStartFailed}, nil
				})
			if observation.JobRead != "ok" || observation.JobStatus != "failed" || observation.JobCode != "source_storage_full" ||
				observation.RuntimeDiagnostic != "unavailable" || (stage == "history" && gets != 0) || (stage != "history" && gets != 1) {
				t.Fatal("unavailable runtime lost the bound job classification or invented a runtime cause")
			}
			if strings.Contains(lanTwoAppDeploymentFailureObservationLine(observation), "secret") {
				t.Fatal("read refusal disclosed raw evidence")
			}
		})
	}
}
