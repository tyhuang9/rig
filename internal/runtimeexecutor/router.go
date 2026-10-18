// Package runtimeexecutor dispatches deployment jobs to their immutable
// runtime strategy. Compose remains the default for legacy releases without a
// deployment-plan revision.
package runtimeexecutor

import (
	"context"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentbinding"
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

type deploymentBindings interface {
	Read(context.Context, string) (deploymentbinding.Binding, error)
	Check(context.Context, deploymentbinding.Binding) (deploymentbinding.Classification, error)
}

type Router struct {
	deployments deploymentReader
	plans       planReader
	releases    releaseReader
	compose     jobs.Executor
	generated   jobs.Executor
	bindings    deploymentBindings
	rebindFence func(context.Context) error
}

var errInvalidSource = errors.New("runtime strategy provenance is invalid")

func New(deploymentRepository deploymentReader, plans planReader, releases releaseReader, compose, generated jobs.Executor,
	bindings deploymentBindings, rebindFence func(context.Context) error,
) (*Router, error) {
	if deploymentRepository == nil || plans == nil || releases == nil || (compose == nil && generated == nil) || nilDeploymentBindings(bindings) || rebindFence == nil {
		return nil, errors.New("runtime strategy router dependencies are required")
	}
	return &Router{
		deployments: deploymentRepository, plans: plans, releases: releases,
		compose: compose, generated: generated, bindings: bindings, rebindFence: rebindFence,
	}, nil
}

func (r *Router) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if r == nil || nilDeploymentBindings(r.bindings) || r.rebindFence == nil || ctx == nil || reporter == nil || job.Type != "deploy" || job.ResourceType != "application" || uuid.Validate(job.ResourceID) != nil || uuid.Validate(job.ID) != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "validation_failed"}
	}
	if err := ctx.Err(); err != nil {
		return jobs.ExecutionResult{}, err
	}
	// The deployment executor can create networks, containers, and migrations
	// before ingress Switch. Check the durable rebind fence before any job state
	// or workspace operation; a future claim writer must also drain in-flight
	// executors before it can activate a claim.
	if err := r.rebindFence(ctx); err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return jobs.ExecutionResult{}, canceled
		}
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "runtime_unavailable"}
	}
	if canceled := ctx.Err(); canceled != nil {
		return jobs.ExecutionResult{}, canceled
	}
	// Read the exact immutable authority accepted with this job. This boundary
	// refuses legacy adoption and newer desired heads; it is not durable effect
	// permission for a later runtime command.
	binding, err := r.bindings.Read(ctx, job.ID)
	if err != nil {
		return jobs.ExecutionResult{}, deploymentBindingRefusal(ctx, err)
	}
	if binding.JobID() != job.ID || binding.ApplicationID() != job.ResourceID {
		return jobs.ExecutionResult{}, deploymentBindingRefusal(ctx, deploymentbinding.ErrInvalidBinding)
	}
	if err := r.checkBinding(ctx, binding); err != nil {
		return jobs.ExecutionResult{}, err
	}
	input, err := jobs.DeploymentInputFor(job)
	if err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "validation_failed"}
	}
	deployment, _, err := r.deployments.GetOrCreateByJob(ctx, job.ResourceID, job.ID, string(input.ConfigurationMode))
	if err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "internal_error"}
	}
	var strategy deployments.RuntimeStrategy
	if input.HasReviewedRevisions() {
		if deployment.ProvenanceInitialized && deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode {
			return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "invalid_source"}
		}
		strategy = deployments.RuntimeGeneratedNode
	} else {
		strategy, err = r.strategy(ctx, job.ResourceID, input.ReleaseID, deployment)
		if err != nil {
			code := "internal_error"
			if errors.Is(err, errInvalidSource) {
				code = "invalid_source"
			}
			return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: code}
		}
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
	// Revalidate the same immutable value after all routing and workspace
	// preparation, immediately before delegation. Retain any preparing row when
	// authority changed; recovery owns interpretation of that durable evidence.
	if err := r.checkBinding(ctx, binding); err != nil {
		return jobs.ExecutionResult{}, err
	}
	if canceled := ctx.Err(); canceled != nil {
		return jobs.ExecutionResult{}, canceled
	}
	return executor.Execute(ctx, job, reporter)
}

func nilDeploymentBindings(bindings deploymentBindings) bool {
	if bindings == nil {
		return true
	}
	value := reflect.ValueOf(bindings)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *Router) checkBinding(ctx context.Context, binding deploymentbinding.Binding) error {
	classification, err := r.bindings.Check(ctx, binding)
	if err != nil {
		return deploymentBindingRefusal(ctx, err)
	}
	switch classification {
	case deploymentbinding.ClassificationCurrent:
		return nil
	case deploymentbinding.ClassificationStale:
		return deploymentBindingRefusal(ctx, nil)
	default:
		return deploymentBindingRefusal(ctx, deploymentbinding.ErrInvalidConfiguration)
	}
}

func deploymentBindingRefusal(ctx context.Context, cause error) error {
	if ctx != nil {
		if canceled := ctx.Err(); canceled != nil {
			if cause != nil {
				return errors.Join(cause, canceled)
			}
			return canceled
		}
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, deploymentbinding.ErrCanceled) {
		return cause
	}
	refusal := &jobs.ExecutionError{Code: "runtime_unavailable"}
	if cause == nil {
		return refusal
	}
	return errors.Join(refusal, cause)
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
