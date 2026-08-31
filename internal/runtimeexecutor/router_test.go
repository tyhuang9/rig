package runtimeexecutor

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
)

const (
	routerAppID     = "11111111-1111-4111-8111-111111111111"
	routerJobID     = "22222222-2222-4222-8222-222222222222"
	routerActorID   = "33333333-3333-4333-8333-333333333333"
	routerPlanID    = "44444444-4444-4444-8444-444444444444"
	routerReleaseID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type routerDeployments struct{ deployment deployments.Deployment }

func (r routerDeployments) GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error) {
	return r.deployment, false, nil
}

type routerPlans struct {
	head     deploymentplans.DeploymentPlanRevision
	revision deploymentplans.DeploymentPlanRevision
	err      error
}

func (r routerPlans) Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error) {
	return r.head, r.err
}
func (r routerPlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return r.revision, r.err
}

type routerReleases struct {
	release releasesnapshot.Release
	err     error
}

func (r routerReleases) ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error) {
	return r.release, r.err
}

type recordingExecutor struct{ calls int }

func (e *recordingExecutor) Execute(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	e.calls++
	return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
}

type noopReporter struct{}

func (noopReporter) Report(jobs.ProgressUpdate) error { return nil }

func TestRouterPreservesLegacyComposeAndUsesAcceptedGeneratedHead(t *testing.T) {
	compose, generated := &recordingExecutor{}, &recordingExecutor{}
	router, err := New(routerDeployments{}, routerPlans{head: generatedPlan()}, routerReleases{}, compose, generated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Execute(context.Background(), routerJob(""), noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if compose.calls != 0 || generated.calls != 1 {
		t.Fatalf("compose=%d generated=%d", compose.calls, generated.calls)
	}

	compose, generated = &recordingExecutor{}, &recordingExecutor{}
	router, _ = New(routerDeployments{}, routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: routerAppID}}, routerReleases{}, compose, generated)
	if _, err := router.Execute(context.Background(), routerJob(""), noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if compose.calls != 1 || generated.calls != 0 {
		t.Fatalf("legacy compose=%d generated=%d", compose.calls, generated.calls)
	}
}

func TestRouterPinsPriorReleaseAndExistingDeploymentStrategy(t *testing.T) {
	compose, generated := &recordingExecutor{}, &recordingExecutor{}
	release := releasesnapshot.Release{ID: routerReleaseID, AppID: routerAppID, DeploymentPlanRevisionID: routerPlanID, DeploymentPlanRevisionNumber: 1}
	router, _ := New(routerDeployments{}, routerPlans{revision: generatedPlan()}, routerReleases{release: release}, compose, generated)
	if _, err := router.Execute(context.Background(), routerJob(routerReleaseID), noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if generated.calls != 1 {
		t.Fatal("prior release did not use its generated plan")
	}

	compose, generated = &recordingExecutor{}, &recordingExecutor{}
	initialized := deployments.Deployment{RuntimeStrategy: deployments.RuntimeCompose, ProvenanceInitialized: true}
	router, _ = New(routerDeployments{deployment: initialized}, routerPlans{err: errors.New("must not read current plan")}, routerReleases{err: errors.New("must not read release")}, compose, generated)
	if _, err := router.Execute(context.Background(), routerJob(""), noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if compose.calls != 1 || generated.calls != 0 {
		t.Fatal("existing provenance was not authoritative")
	}
}

func TestRouterDoesNotFallBackWhenPinnedGeneratedRuntimeIsUnavailable(t *testing.T) {
	compose := &recordingExecutor{}
	release := releasesnapshot.Release{ID: routerReleaseID, AppID: routerAppID, DeploymentPlanRevisionID: routerPlanID, DeploymentPlanRevisionNumber: 1}
	router, _ := New(routerDeployments{}, routerPlans{revision: generatedPlan()}, routerReleases{release: release}, compose, nil)
	_, err := router.Execute(context.Background(), routerJob(routerReleaseID), noopReporter{})
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || compose.calls != 0 {
		t.Fatalf("error=%v compose calls=%d", err, compose.calls)
	}
}

func generatedPlan() deploymentplans.DeploymentPlanRevision {
	return deploymentplans.DeploymentPlanRevision{ID: routerPlanID, AppID: routerAppID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode}}
}

func routerJob(releaseID string) jobs.Job {
	return jobs.Job{ID: routerJobID, Type: "deploy", ResourceType: "application", ResourceID: routerAppID, RequestedBy: routerActorID, Attempt: 1, Input: jobs.DeploymentInput{ReleaseID: releaseID, ConfigurationMode: jobs.ConfigurationCurrent}}
}
