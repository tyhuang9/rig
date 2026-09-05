// Package runtimeexecutor dispatches deployment jobs to their immutable
// runtime strategy. Compose remains the default for legacy releases without a
// deployment-plan revision.
package runtimeexecutor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
)

type deploymentReader interface {
	GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error)
}

type planReader interface {
	Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error)
	GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error)
}

type releaseReader interface {
	ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error)
}

type Router struct {
	deployments deploymentReader
	plans       planReader
	releases    releaseReader
	compose     jobs.Executor
	generated   jobs.Executor
}

var errInvalidSource = errors.New("runtime strategy provenance is invalid")

func New(deploymentRepository deploymentReader, plans planReader, releases releaseReader, compose, generated jobs.Executor) (*Router, error) {
	if deploymentRepository == nil || plans == nil || releases == nil || (compose == nil && generated == nil) {
		return nil, errors.New("runtime strategy router dependencies are required")
	}
	return &Router{deployments: deploymentRepository, plans: plans, releases: releases, compose: compose, generated: generated}, nil
}

func (r *Router) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if r == nil || ctx == nil || reporter == nil || job.Type != "deploy" || job.ResourceType != "application" || uuid.Validate(job.ResourceID) != nil || uuid.Validate(job.ID) != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "validation_failed"}
	}
	input, err := jobs.DeploymentInputFor(job)
	if err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "validation_failed"}
	}
	deployment, _, err := r.deployments.GetOrCreateByJob(ctx, job.ResourceID, job.ID, string(input.ConfigurationMode))
	if err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "internal_error"}
	}
	strategy, err := r.strategy(ctx, job.ResourceID, input.ReleaseID, deployment)
	if err != nil {
		code := "internal_error"
		if errors.Is(err, errInvalidSource) {
			code = "invalid_source"
		}
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: code}
	}
	var executor jobs.Executor
	switch strategy {
	case deployments.RuntimeGeneratedNode:
		executor = r.generated
	case deployments.RuntimeCompose:
		executor = r.compose
	default:
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "invalid_source"}
	}
	if executor == nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "runtime_unavailable"}
	}
	return executor.Execute(ctx, job, reporter)
}

func (r *Router) strategy(ctx context.Context, appID, releaseID string, deployment deployments.Deployment) (deployments.RuntimeStrategy, error) {
	if deployment.ProvenanceInitialized {
		if deployment.RuntimeStrategy != deployments.RuntimeCompose && deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode {
			return "", errInvalidSource
		}
		return deployment.RuntimeStrategy, nil
	}
	if releaseID != "" {
		release, err := r.releases.ReadyWorkspace(ctx, appID, releaseID)
		if err != nil {
			return "", err
		}
		if release.ID != releaseID || release.AppID != appID {
			return "", errInvalidSource
		}
		if release.DeploymentPlanRevisionID == "" && release.DeploymentPlanRevisionNumber == 0 {
			return deployments.RuntimeCompose, nil
		}
		return r.planStrategy(ctx, appID, release.DeploymentPlanRevisionID, release.DeploymentPlanRevisionNumber)
	}
	head, err := r.plans.Get(ctx, appID)
	if err != nil {
		return "", err
	}
	if head.ID == "" && head.RevisionNumber == 0 {
		return deployments.RuntimeCompose, nil
	}
	if head.ID == "" || head.AppID != appID || head.RevisionNumber < 1 {
		return "", errInvalidSource
	}
	return strategyFromPlan(head)
}

func (r *Router) planStrategy(ctx context.Context, appID, planID string, planNumber int64) (deployments.RuntimeStrategy, error) {
	if planID == "" || planNumber < 1 {
		return "", errInvalidSource
	}
	plan, err := r.plans.GetRevision(ctx, appID, planID, planNumber)
	if err != nil {
		return "", err
	}
	if plan.ID != planID || plan.AppID != appID || plan.RevisionNumber != planNumber {
		return "", errInvalidSource
	}
	return strategyFromPlan(plan)
}

func strategyFromPlan(plan deploymentplans.DeploymentPlanRevision) (deployments.RuntimeStrategy, error) {
	switch plan.Plan.Strategy {
	case deploymentplans.StrategyGeneratedNode:
		return deployments.RuntimeGeneratedNode, nil
	case deploymentplans.StrategyCompose:
		return deployments.RuntimeCompose, nil
	default:
		return "", errInvalidSource
	}
}
