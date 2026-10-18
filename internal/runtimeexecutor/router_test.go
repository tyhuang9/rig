package runtimeexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/applifecycle"
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

type countedRouterDeployments struct{ calls int }

func (r *countedRouterDeployments) GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error) {
	r.calls++
	return deployments.Deployment{}, false, nil
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

type countedRouterPlans struct {
	delegate         routerPlans
	getCalls         int
	getRevisionCalls int
}

func (r *countedRouterPlans) Get(ctx context.Context, appID string) (deploymentplans.DeploymentPlanRevision, error) {
	r.getCalls++
	return r.delegate.Get(ctx, appID)
}

func (r *countedRouterPlans) GetRevision(ctx context.Context, appID, planID string, revision int64) (deploymentplans.DeploymentPlanRevision, error) {
	r.getRevisionCalls++
	return r.delegate.GetRevision(ctx, appID, planID, revision)
}

type countedRouterReleases struct {
	delegate routerReleases
	calls    int
}

func (r *countedRouterReleases) ReadyWorkspace(ctx context.Context, appID, releaseID string) (releasesnapshot.Release, error) {
	r.calls++
	return r.delegate.ReadyWorkspace(ctx, appID, releaseID)
}

type routerAuthority struct {
	snapshot       applifecycle.DeploymentAuthoritySnapshot
	snapshotErr    error
	revalidateErr  error
	snapshots      int
	revalidations  int
	onSnapshot     func()
	onRevalidation func()
}

func (a *routerAuthority) Snapshot(context.Context, string) (applifecycle.DeploymentAuthoritySnapshot, error) {
	a.snapshots++
	if a.onSnapshot != nil {
		a.onSnapshot()
	}
	return a.snapshot, a.snapshotErr
}

func (a *routerAuthority) Revalidate(context.Context, applifecycle.DeploymentAuthoritySnapshot) error {
	a.revalidations++
	if a.onRevalidation != nil {
		a.onRevalidation()
	}
	return a.revalidateErr
}

type recordingExecutor struct{ calls int }

func (e *recordingExecutor) Execute(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	e.calls++
	return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
}

type noopReporter struct{}

func (noopReporter) Report(jobs.ProgressUpdate) error { return nil }

func clearRebindFence(context.Context) error { return nil }

func newRouter(deployments deploymentReader, plans planReader, releases releaseReader, compose, generated jobs.Executor) (*Router, error) {
	return New(deployments, plans, releases, compose, generated, &routerAuthority{}, clearRebindFence)
}

func TestRouterRequiresRebindFenceCheck(t *testing.T) {
	router, err := New(routerDeployments{}, routerPlans{}, routerReleases{}, &recordingExecutor{}, nil, &routerAuthority{}, nil)
	if err == nil || router != nil {
		t.Fatalf("missing rebind fence router=%v error=%v", router, err)
	}
}

func TestRouterRequiresDesiredLifecycleAuthority(t *testing.T) {
	router, err := New(routerDeployments{}, routerPlans{}, routerReleases{}, &recordingExecutor{}, nil, nil, clearRebindFence)
	if err == nil || router != nil {
		t.Fatalf("missing desired lifecycle authority router=%v error=%v", router, err)
	}
}

func TestRouterRebindFenceBlocksBeforeDeploymentAndRuntimeEffects(t *testing.T) {
	for _, strategy := range []string{"compose", "generated"} {
		for _, failure := range []string{"prepared claim", "snapshot read failure"} {
			t.Run(strategy+"/"+failure, func(t *testing.T) {
				deployments := &countedRouterDeployments{}
				compose, generated := &recordingExecutor{}, &recordingExecutor{}
				checks := 0
				fence := func(context.Context) error {
					checks++
					return errors.New(failure)
				}
				plans := routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: routerAppID}}
				if strategy == "generated" {
					plans.head = generatedPlan()
				}
				router, err := New(deployments, plans, routerReleases{}, compose, generated, &routerAuthority{}, fence)
				if err != nil {
					t.Fatal(err)
				}
				_, err = router.Execute(context.Background(), routerJob(""), noopReporter{})
				var executionErr *jobs.ExecutionError
				if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" ||
					checks != 1 || deployments.calls != 0 || compose.calls != 0 || generated.calls != 0 {
					t.Fatalf("blocked result err=%v checks=%d deployments=%d compose=%d generated=%d",
						err, checks, deployments.calls, compose.calls, generated.calls)
				}
			})
		}
	}
}

func TestRouterCancelledBeforeRebindFenceAndDeployment(t *testing.T) {
	deployments := &countedRouterDeployments{}
	checks := 0
	router, err := New(deployments, routerPlans{}, routerReleases{}, &recordingExecutor{}, nil, &routerAuthority{},
		func(context.Context) error { checks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := router.Execute(ctx, routerJob(""), noopReporter{}); !errors.Is(err, context.Canceled) ||
		checks != 0 || deployments.calls != 0 {
		t.Fatalf("canceled result err=%v checks=%d deployments=%d", err, checks, deployments.calls)
	}
}

func TestRouterCancellationDuringRebindFencePreservesCancellation(t *testing.T) {
	deployments := &countedRouterDeployments{}
	ctx, cancel := context.WithCancel(context.Background())
	authority := &routerAuthority{}
	router, err := New(deployments, routerPlans{}, routerReleases{}, &recordingExecutor{}, nil, authority,
		func(context.Context) error {
			cancel()
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Execute(ctx, routerJob(""), noopReporter{}); !errors.Is(err, context.Canceled) ||
		authority.snapshots != 0 || deployments.calls != 0 {
		t.Fatalf("canceled during successful fence err=%v snapshots=%d deployments=%d", err, authority.snapshots, deployments.calls)
	}
}

func TestRouterDesiredLifecycleAuthorityBlocksBeforeDeploymentPreparation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "stopped", err: applifecycle.ErrDeploymentNotAuthorized},
		{name: "missing or malformed", err: applifecycle.ErrDeploymentAuthorityInvalid},
		{name: "read failure", err: applifecycle.ErrDeploymentAuthorityUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployments := &countedRouterDeployments{}
			plans := &countedRouterPlans{}
			releases := &countedRouterReleases{}
			compose, generated := &recordingExecutor{}, &recordingExecutor{}
			authority := &routerAuthority{snapshotErr: test.err}
			router, err := New(deployments, plans, releases, compose, generated, authority, clearRebindFence)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Execute(context.Background(), routerJob(routerReleaseID), noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("blocked error=%v", err)
			}
			if authority.snapshots != 1 || authority.revalidations != 0 || deployments.calls != 0 ||
				plans.getCalls != 0 || plans.getRevisionCalls != 0 || releases.calls != 0 ||
				compose.calls != 0 || generated.calls != 0 {
				t.Fatalf("snapshot=%d revalidation=%d deployment=%d plan=%d revision=%d release=%d compose=%d generated=%d",
					authority.snapshots, authority.revalidations, deployments.calls, plans.getCalls,
					plans.getRevisionCalls, releases.calls, compose.calls, generated.calls)
			}
		})
	}
}

func TestRouterDesiredLifecycleAuthorityDriftBlocksEachDelegate(t *testing.T) {
	for _, strategy := range []string{"compose", "generated"} {
		t.Run(strategy, func(t *testing.T) {
			deployments := &countedRouterDeployments{}
			plans := &countedRouterPlans{delegate: routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: routerAppID}}}
			if strategy == "generated" {
				plans.delegate.head = generatedPlan()
			}
			releases := &countedRouterReleases{}
			compose, generated := &recordingExecutor{}, &recordingExecutor{}
			authority := &routerAuthority{revalidateErr: applifecycle.ErrDeploymentAuthorityChanged}
			router, err := New(deployments, plans, releases, compose, generated, authority, clearRebindFence)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Execute(context.Background(), routerJob(""), noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("drift error=%v", err)
			}
			if authority.snapshots != 1 || authority.revalidations != 1 || deployments.calls != 1 ||
				plans.getCalls != 1 || plans.getRevisionCalls != 0 || releases.calls != 0 ||
				compose.calls != 0 || generated.calls != 0 {
				t.Fatalf("snapshot=%d revalidation=%d deployment=%d plan=%d revision=%d release=%d compose=%d generated=%d",
					authority.snapshots, authority.revalidations, deployments.calls, plans.getCalls,
					plans.getRevisionCalls, releases.calls, compose.calls, generated.calls)
			}
		})
	}
}

func TestRouterDesiredLifecycleAuthorityPreservesCancellation(t *testing.T) {
	for _, stage := range []string{"snapshot", "revalidation"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			deployments := &countedRouterDeployments{}
			compose := &recordingExecutor{}
			authority := &routerAuthority{}
			if stage == "snapshot" {
				authority.onSnapshot = cancel
			} else {
				authority.onRevalidation = cancel
			}
			router, err := New(deployments, routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: routerAppID}},
				routerReleases{}, compose, nil, authority, clearRebindFence)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Execute(ctx, routerJob(""), noopReporter{})
			if !errors.Is(err, context.Canceled) || compose.calls != 0 {
				t.Fatalf("cancellation error=%v compose=%d", err, compose.calls)
			}
			if stage == "snapshot" && (deployments.calls != 0 || authority.revalidations != 0) {
				t.Fatalf("snapshot cancellation deployment=%d revalidation=%d", deployments.calls, authority.revalidations)
			}
			if stage == "revalidation" && (deployments.calls != 1 || authority.revalidations != 1) {
				t.Fatalf("revalidation cancellation deployment=%d revalidation=%d", deployments.calls, authority.revalidations)
			}
		})
	}
}

func TestRouterPreservesLegacyComposeAndUsesAcceptedGeneratedHead(t *testing.T) {
	compose, generated := &recordingExecutor{}, &recordingExecutor{}
	router, err := newRouter(routerDeployments{}, routerPlans{head: generatedPlan()}, routerReleases{}, compose, generated)
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
	router, _ = newRouter(routerDeployments{}, routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: routerAppID}}, routerReleases{}, compose, generated)
	if _, err := router.Execute(context.Background(), routerJob(""), noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if compose.calls != 1 || generated.calls != 0 {
		t.Fatalf("legacy compose=%d generated=%d", compose.calls, generated.calls)
	}
}

func TestRouterNeverSendsReviewedGeneratedJobToComposeAfterHeadDrift(t *testing.T) {
	compose, generated := &recordingExecutor{}, &recordingExecutor{}
	composeHead := deploymentplans.DeploymentPlanRevision{ID: routerPlanID, AppID: routerAppID, RevisionNumber: 2, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyCompose}}
	router, err := newRouter(routerDeployments{}, routerPlans{head: composeHead}, routerReleases{}, compose, generated)
	if err != nil {
		t.Fatal(err)
	}
	job := routerJob("")
	job.Input, _ = json.Marshal(jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: routerPlanID, ExpectedPlanRevisionNumber: 1, ExpectedConfigurationRevisionID: "", ExpectedConfigurationRevisionNumber: 0})
	if _, err := router.Execute(context.Background(), job, noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if compose.calls != 0 || generated.calls != 1 {
		t.Fatalf("compose=%d generated=%d", compose.calls, generated.calls)
	}

	compose, generated = &recordingExecutor{}, &recordingExecutor{}
	router, _ = newRouter(routerDeployments{deployment: deployments.Deployment{ProvenanceInitialized: true, RuntimeStrategy: deployments.RuntimeCompose}}, routerPlans{head: composeHead}, routerReleases{}, compose, generated)
	_, err = router.Execute(context.Background(), job, noopReporter{})
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "invalid_source" || compose.calls != 0 || generated.calls != 0 {
		t.Fatalf("initialized mismatch err=%v compose=%d generated=%d", err, compose.calls, generated.calls)
	}
}

func TestRouterPinsPriorReleaseAndExistingDeploymentStrategy(t *testing.T) {
	compose, generated := &recordingExecutor{}, &recordingExecutor{}
	release := releasesnapshot.Release{ID: routerReleaseID, AppID: routerAppID, DeploymentPlanRevisionID: routerPlanID, DeploymentPlanRevisionNumber: 1}
	router, _ := newRouter(routerDeployments{}, routerPlans{revision: generatedPlan()}, routerReleases{release: release}, compose, generated)
	if _, err := router.Execute(context.Background(), routerJob(routerReleaseID), noopReporter{}); err != nil {
		t.Fatal(err)
	}
	if generated.calls != 1 {
		t.Fatal("prior release did not use its generated plan")
	}

	compose, generated = &recordingExecutor{}, &recordingExecutor{}
	initialized := deployments.Deployment{RuntimeStrategy: deployments.RuntimeCompose, ProvenanceInitialized: true}
	router, _ = newRouter(routerDeployments{deployment: initialized}, routerPlans{err: errors.New("must not read current plan")}, routerReleases{err: errors.New("must not read release")}, compose, generated)
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
	router, _ := newRouter(routerDeployments{}, routerPlans{revision: generatedPlan()}, routerReleases{release: release}, compose, nil)
	_, err := router.Execute(context.Background(), routerJob(routerReleaseID), noopReporter{})
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || compose.calls != 0 {
		t.Fatalf("error=%v compose calls=%d", err, compose.calls)
	}
}

func TestRouterDistinguishesRepositoryAndStrategyValidationFailures(t *testing.T) {
	storageErr := errors.New("repository unavailable")
	tests := []struct {
		name      string
		releaseID string
		plans     routerPlans
		releases  routerReleases
		wantCode  string
	}{
		{name: "head repository error", plans: routerPlans{err: storageErr}, wantCode: "internal_error"},
		{name: "release repository error", releaseID: routerReleaseID, releases: routerReleases{err: storageErr}, wantCode: "internal_error"},
		{
			name:      "revision repository error",
			releaseID: routerReleaseID,
			releases:  routerReleases{release: releasesnapshot.Release{ID: routerReleaseID, AppID: routerAppID, DeploymentPlanRevisionID: routerPlanID, DeploymentPlanRevisionNumber: 1}},
			plans:     routerPlans{err: storageErr},
			wantCode:  "internal_error",
		},
		{
			name:      "malformed release provenance",
			releaseID: routerReleaseID,
			releases:  routerReleases{release: releasesnapshot.Release{ID: "different", AppID: routerAppID}},
			wantCode:  "invalid_source",
		},
		{
			name:      "mismatched plan provenance",
			releaseID: routerReleaseID,
			releases:  routerReleases{release: releasesnapshot.Release{ID: routerReleaseID, AppID: routerAppID, DeploymentPlanRevisionID: routerPlanID, DeploymentPlanRevisionNumber: 1}},
			plans:     routerPlans{revision: deploymentplans.DeploymentPlanRevision{ID: routerPlanID, AppID: routerActorID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode}}},
			wantCode:  "invalid_source",
		},
		{
			name:     "unsupported strategy",
			plans:    routerPlans{head: deploymentplans.DeploymentPlanRevision{ID: routerPlanID, AppID: routerAppID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: "unsupported"}}},
			wantCode: "invalid_source",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compose, generated := &recordingExecutor{}, &recordingExecutor{}
			router, err := newRouter(routerDeployments{}, test.plans, test.releases, compose, generated)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Execute(context.Background(), routerJob(test.releaseID), noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != test.wantCode {
				t.Fatalf("error=%v want code=%q", err, test.wantCode)
			}
			if compose.calls != 0 || generated.calls != 0 {
				t.Fatalf("executors called after routing failure: compose=%d generated=%d", compose.calls, generated.calls)
			}
		})
	}
}

func generatedPlan() deploymentplans.DeploymentPlanRevision {
	return deploymentplans.DeploymentPlanRevision{ID: routerPlanID, AppID: routerAppID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode}}
}

func routerJob(releaseID string) jobs.Job {
	input, _ := json.Marshal(jobs.DeploymentInput{ReleaseID: releaseID, ConfigurationMode: jobs.ConfigurationCurrent})
	return jobs.Job{ID: routerJobID, Type: "deploy", ResourceType: "application", ResourceID: routerAppID, RequestedBy: routerActorID, Attempt: 1, Input: input}
}
