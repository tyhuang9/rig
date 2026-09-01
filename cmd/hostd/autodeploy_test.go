package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/sourceconnections"
	"github.com/hostd/hostd/internal/sourceinspection"
)

type autoDeployRunnerFake struct {
	started    chan struct{}
	release    <-chan struct{}
	err        error
	mutex      sync.Mutex
	wakes      int
	reconciles int
}

func (runner *autoDeployRunnerFake) Run(ctx context.Context) error {
	if runner.started != nil {
		close(runner.started)
	}
	if runner.release != nil {
		select {
		case <-runner.release:
			return runner.err
		case <-ctx.Done():
			return nil
		}
	}
	return runner.err
}

func (runner *autoDeployRunnerFake) Wake() {
	runner.mutex.Lock()
	runner.wakes++
	runner.mutex.Unlock()
}

func (runner *autoDeployRunnerFake) Reconcile() {
	runner.mutex.Lock()
	runner.reconciles++
	runner.mutex.Unlock()
}

func (runner *autoDeployRunnerFake) wakeCount() int {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	return runner.wakes
}

func TestAutoDeployDefaultOffDoesNotConstruct(t *testing.T) {
	for _, cfg := range []config.Config{
		config.Defaults(),
		func() config.Config {
			value := config.Defaults()
			value.ComposeRuntime = true
			value.GitHubClientID, value.GitHubAppSlug = "", ""
			return value
		}(),
		func() config.Config {
			value := config.Defaults()
			value.GitHubClientID, value.GitHubAppSlug = "client", "app"
			return value
		}(),
		func() config.Config {
			value := config.Defaults()
			value.FakeRuntime = true
			value.GitHubClientID, value.GitHubAppSlug = "client", "app"
			return value
		}(),
	} {
		calls := 0
		done, wake, reconcile := startAutoDeploy(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (autoDeployRunner, error) {
			calls++
			return nil, nil
		})
		wake()
		reconcile()
		if calls != 0 || !waitForWorker(done, time.Second) {
			t.Fatalf("default-off factory calls=%d cfg=%#v", calls, cfg)
		}
	}
}

func TestAutoDeployEnabledIsAsyncIndependentAndForwardsPendingWake(t *testing.T) {
	cfg := enabledAutoDeployConfig()
	factoryStarted, releaseFactory := make(chan struct{}), make(chan struct{})
	runnerStarted, releaseRunner := make(chan struct{}), make(chan struct{})
	runner := &autoDeployRunnerFake{started: runnerStarted, release: releaseRunner}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	done, wake, reconcile := startAutoDeploy(ctx, cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (autoDeployRunner, error) {
		close(factoryStarted)
		<-releaseFactory
		return runner, nil
	})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("auto-deploy factory blocked host startup")
	}
	<-factoryStarted
	for index := 0; index < 100; index++ {
		wake()
	}
	reconcile()
	close(releaseFactory)
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("auto-deploy runner did not start")
	}
	if runner.wakeCount() != 0 || runner.reconciles != 1 {
		t.Fatalf("pending wakes=%d reconciles=%d", runner.wakeCount(), runner.reconciles)
	}
	close(releaseRunner)
	if !waitForWorker(done, time.Second) || ctx.Err() != nil {
		t.Fatalf("coordinator completion affected host ctx=%v", ctx.Err())
	}
}

func TestAutoDeployGeneratedRuntimeStartsFactory(t *testing.T) {
	cfg := enabledAutoDeployConfig()
	cfg.ComposeRuntime = false
	cfg.GeneratedRuntime = true
	calls := 0
	done, _, _ := startAutoDeploy(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (autoDeployRunner, error) {
		calls++
		return &autoDeployRunnerFake{}, nil
	})
	if !waitForWorker(done, time.Second) || calls != 1 {
		t.Fatalf("generated factory calls=%d", calls)
	}
}

func TestAutoDeployConstructionAndRunFailuresAreSanitized(t *testing.T) {
	const secret = "token=secret repository=private sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, factory := range []autoDeployFactory{
		func() (autoDeployRunner, error) { return nil, errors.New(secret) },
		func() (autoDeployRunner, error) { return &autoDeployRunnerFake{err: errors.New(secret)}, nil },
	} {
		var logs bytes.Buffer
		done, _, _ := startAutoDeploy(context.Background(), enabledAutoDeployConfig(), newStructuredLogger(&logs, "info"), factory)
		if !waitForWorker(done, time.Second) {
			t.Fatal("failure path did not drain")
		}
		output := logs.String()
		if strings.Contains(output, secret) || strings.Contains(output, "private") || strings.Contains(output, "aaaaaaaa") || !strings.Contains(output, autodeploy.OutcomePersistenceUnavailable) {
			t.Fatalf("unsafe coordinator log=%q", output)
		}
	}
}

type autoDeployProviderStub struct{}

func (autoDeployProviderStub) StartDevice(context.Context) (githubapp.DeviceAuthorization, error) {
	return githubapp.DeviceAuthorization{}, nil
}
func (autoDeployProviderStub) PollDevice(context.Context, string) (githubapp.TokenBundle, error) {
	return githubapp.TokenBundle{}, nil
}
func (autoDeployProviderStub) Refresh(context.Context, string) (githubapp.TokenBundle, error) {
	return githubapp.TokenBundle{}, nil
}
func (autoDeployProviderStub) CurrentUser(context.Context, string) (githubapp.User, error) {
	return githubapp.User{}, nil
}
func (autoDeployProviderStub) Installations(context.Context, string, int, int) (githubapp.InstallationPage, error) {
	return githubapp.InstallationPage{}, nil
}

func TestNewAutoDeployRunnerRetainsInjectedRepository(t *testing.T) {
	cfg := enabledAutoDeployConfig()
	if runner, err := newAutoDeployRunner(cfg, nil, nil, nil, newStructuredLogger(&bytes.Buffer{}, "info")); err == nil || runner != nil {
		t.Fatal("nil repository was accepted")
	}
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository := autodeploy.NewRepository(db)
	sources := sourceconnections.NewService(nil, autoDeployProviderStub{}, nil, "app", time.Now)
	runner, err := newAutoDeployRunner(cfg, repository, sources, jobs.New(db), newStructuredLogger(&bytes.Buffer{}, "info"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, ok := runner.(*autodeploy.Coordinator)
	if !ok {
		t.Fatalf("runner type=%T", runner)
	}
	retained := reflect.ValueOf(coordinator).Elem().FieldByName("repository")
	if !retained.IsValid() || retained.IsNil() || retained.Elem().Pointer() != reflect.ValueOf(repository).Pointer() {
		t.Fatal("coordinator did not retain the injected auto-deploy repository")
	}
}

type autoDeployPlanReaderStub struct {
	revisions []deploymentplans.DeploymentPlanRevision
	err       error
	calls     int
}

type autoDeployDispatchPreflightStub struct{}

func (autoDeployDispatchPreflightStub) Prepare(context.Context, autodeploy.DispatchPreflightRequest) (autodeploy.DispatchPreflightResult, error) {
	return autodeploy.DispatchPreflightResult{}, nil
}

func TestGeneratedAwareAutoDeployRunnerRequiresPreflight(t *testing.T) {
	cfg := enabledAutoDeployConfig()
	cfg.ComposeRuntime = false
	cfg.GeneratedRuntime = true
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository := autodeploy.NewRepository(db)
	sources := sourceconnections.NewService(nil, autoDeployProviderStub{}, nil, "app", time.Now)
	logger := newStructuredLogger(&bytes.Buffer{}, "info")
	if runner, err := newGeneratedAwareAutoDeployRunner(cfg, repository, sources, jobs.New(db), nil, logger); err == nil || runner != nil {
		t.Fatal("generated runtime accepted a nil dispatch preflight")
	}
	runner, err := newGeneratedAwareAutoDeployRunner(cfg, repository, sources, jobs.New(db), autoDeployDispatchPreflightStub{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(*autodeploy.Coordinator); !ok {
		t.Fatalf("runner type=%T", runner)
	}
	if legacy, err := newAutoDeployRunner(cfg, repository, sources, jobs.New(db), logger); err == nil || legacy != nil {
		t.Fatal("legacy compose constructor accepted generated runtime without preflight")
	}
}

func (reader *autoDeployPlanReaderStub) Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error) {
	reader.calls++
	if reader.err != nil {
		return deploymentplans.DeploymentPlanRevision{}, reader.err
	}
	if len(reader.revisions) == 0 {
		return deploymentplans.DeploymentPlanRevision{}, nil
	}
	index := reader.calls - 1
	if index >= len(reader.revisions) {
		index = len(reader.revisions) - 1
	}
	return reader.revisions[index], nil
}

type autoDeployReleaseMaterializerStub struct {
	materialized     releasesnapshot.Release
	ready            releasesnapshot.Release
	materializeErr   error
	readyErr         error
	materializeCalls int
	readyCalls       int
}

func (materializer *autoDeployReleaseMaterializerStub) Materialize(context.Context, string, string) (releasesnapshot.Release, error) {
	materializer.materializeCalls++
	return materializer.materialized, materializer.materializeErr
}

func (materializer *autoDeployReleaseMaterializerStub) ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error) {
	materializer.readyCalls++
	return materializer.ready, materializer.readyErr
}

func TestGeneratedAutoDeployPreflightMaterializesCompatiblePinnedRelease(t *testing.T) {
	revision, analysis := generatedAutoDeployPlanFixture(t, false)
	request := generatedAutoDeployPreflightRequest()
	release := generatedAutoDeployRelease(request, revision)
	plans := &autoDeployPlanReaderStub{revisions: []deploymentplans.DeploymentPlanRevision{revision, revision}}
	materializer := &autoDeployReleaseMaterializerStub{materialized: release, ready: release}
	preflight := generatedAutoDeployPreflightFixture(plans, materializer, sourceinspection.Result{
		Source: sourceinspection.SourceMetadata{
			Type: "github", ConnectionID: request.Source.ConnectionID, InstallationID: request.Source.InstallationID,
			RepositoryID: request.Source.RepositoryID, TrackedBranch: request.Source.Branch, TrackedRef: request.Source.Ref,
		},
		ResolvedSHA: request.ResolvedSHA,
		Analysis:    analysis,
	})

	result, err := preflight.Prepare(context.Background(), request)
	if err != nil || result.ReleaseID != release.ID {
		t.Fatalf("preflight result=%#v err=%v", result, err)
	}
	if plans.calls != 2 || materializer.materializeCalls != 1 || materializer.readyCalls != 1 {
		t.Fatalf("calls plans=%d materialize=%d ready=%d", plans.calls, materializer.materializeCalls, materializer.readyCalls)
	}
}

func TestGeneratedAutoDeployPreflightPreservesManualCommandOverride(t *testing.T) {
	revision, analysis := generatedAutoDeployPlanFixture(t, true)
	request := generatedAutoDeployPreflightRequest()
	release := generatedAutoDeployRelease(request, revision)
	plans := &autoDeployPlanReaderStub{revisions: []deploymentplans.DeploymentPlanRevision{revision, revision}}
	materializer := &autoDeployReleaseMaterializerStub{materialized: release, ready: release}
	preflight := generatedAutoDeployPreflightFixture(plans, materializer, sourceinspection.Result{
		Source: sourceinspection.SourceMetadata{
			Type: "github", ConnectionID: request.Source.ConnectionID, InstallationID: request.Source.InstallationID,
			RepositoryID: request.Source.RepositoryID, TrackedBranch: request.Source.Branch, TrackedRef: request.Source.Ref,
		},
		ResolvedSHA: request.ResolvedSHA,
		Analysis:    analysis,
	})

	if result, err := preflight.Prepare(context.Background(), request); err != nil || result.ReleaseID != release.ID {
		t.Fatalf("manual override result=%#v err=%v", result, err)
	}
}

func TestGeneratedAutoDeployPreflightPausesDriftBeforeMaterialization(t *testing.T) {
	revision, analysis := generatedAutoDeployPlanFixture(t, false)
	analysis.Candidates[0].Components[0].Run.Command = "npm run dev"
	request := generatedAutoDeployPreflightRequest()
	plans := &autoDeployPlanReaderStub{revisions: []deploymentplans.DeploymentPlanRevision{revision}}
	materializer := &autoDeployReleaseMaterializerStub{}
	preflight := generatedAutoDeployPreflightFixture(plans, materializer, sourceinspection.Result{
		Source: sourceinspection.SourceMetadata{
			Type: "github", ConnectionID: request.Source.ConnectionID, InstallationID: request.Source.InstallationID,
			RepositoryID: request.Source.RepositoryID, TrackedBranch: request.Source.Branch, TrackedRef: request.Source.Ref,
		},
		ResolvedSHA: request.ResolvedSHA,
		Analysis:    analysis,
	})

	_, err := preflight.Prepare(context.Background(), request)
	var preflightErr *autodeploy.PreflightError
	if !errors.As(err, &preflightErr) || preflightErr.Code != autodeploy.PreflightPlanReview {
		t.Fatalf("drift error=%v", err)
	}
	if materializer.materializeCalls != 0 || materializer.readyCalls != 0 {
		t.Fatalf("drift materialized calls=%d ready=%d", materializer.materializeCalls, materializer.readyCalls)
	}
}

func TestGeneratedAutoDeployPreflightDetectsHeadAndPlanRaces(t *testing.T) {
	t.Run("source head", func(t *testing.T) {
		revision, analysis := generatedAutoDeployPlanFixture(t, false)
		request := generatedAutoDeployPreflightRequest()
		preflight := generatedAutoDeployPreflightFixture(&autoDeployPlanReaderStub{revisions: []deploymentplans.DeploymentPlanRevision{revision}}, &autoDeployReleaseMaterializerStub{}, sourceinspection.Result{
			Source: sourceinspection.SourceMetadata{
				Type: "github", ConnectionID: request.Source.ConnectionID, InstallationID: request.Source.InstallationID,
				RepositoryID: request.Source.RepositoryID, TrackedBranch: request.Source.Branch, TrackedRef: request.Source.Ref,
			},
			ResolvedSHA: strings.Repeat("b", 40), Analysis: analysis,
		})
		_, err := preflight.Prepare(context.Background(), request)
		var preflightErr *autodeploy.PreflightError
		if !errors.As(err, &preflightErr) || preflightErr.Code != autodeploy.PreflightHeadChanged {
			t.Fatalf("head race error=%v", err)
		}
	})

	t.Run("accepted plan head", func(t *testing.T) {
		revision, analysis := generatedAutoDeployPlanFixture(t, false)
		changed := revision
		changed.ID = "55555555-5555-4555-8555-555555555555"
		changed.RevisionNumber = 2
		request := generatedAutoDeployPreflightRequest()
		release := generatedAutoDeployRelease(request, revision)
		preflight := generatedAutoDeployPreflightFixture(&autoDeployPlanReaderStub{revisions: []deploymentplans.DeploymentPlanRevision{revision, changed}}, &autoDeployReleaseMaterializerStub{materialized: release, ready: release}, sourceinspection.Result{
			Source: sourceinspection.SourceMetadata{
				Type: "github", ConnectionID: request.Source.ConnectionID, InstallationID: request.Source.InstallationID,
				RepositoryID: request.Source.RepositoryID, TrackedBranch: request.Source.Branch, TrackedRef: request.Source.Ref,
			},
			ResolvedSHA: request.ResolvedSHA, Analysis: analysis,
		})
		_, err := preflight.Prepare(context.Background(), request)
		var preflightErr *autodeploy.PreflightError
		if !errors.As(err, &preflightErr) || preflightErr.Code != autodeploy.PreflightHeadChanged {
			t.Fatalf("plan race error=%v", err)
		}
	})
}

func TestGeneratedAutoDeployPreflightLeavesLegacyAndComposeUnchanged(t *testing.T) {
	request := generatedAutoDeployPreflightRequest()
	for name, revision := range map[string]deploymentplans.DeploymentPlanRevision{
		"legacy":  {AppID: request.ApplicationID},
		"compose": {ID: "33333333-3333-4333-8333-333333333333", AppID: request.ApplicationID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyCompose}},
	} {
		t.Run(name, func(t *testing.T) {
			plans := &autoDeployPlanReaderStub{revisions: []deploymentplans.DeploymentPlanRevision{revision}}
			materializer := &autoDeployReleaseMaterializerStub{}
			preflight := generatedAutoDeployPreflightFixture(plans, materializer, sourceinspection.Result{})
			result, err := preflight.Prepare(context.Background(), request)
			if err != nil || result.ReleaseID != "" || plans.calls != 1 || materializer.materializeCalls != 0 {
				t.Fatalf("result=%#v plans=%d materialize=%d err=%v", result, plans.calls, materializer.materializeCalls, err)
			}
		})
	}
}

func generatedAutoDeployPreflightFixture(plans autoDeployPlanReader, materializer autoDeployReleaseMaterializer, inspection sourceinspection.Result) *generatedAutoDeployPreflight {
	return &generatedAutoDeployPreflight{
		plans: plans, sources: sourceconnections.NewService(nil, autoDeployProviderStub{}, nil, "app", time.Now), releases: materializer,
		inspect: func(context.Context, sourceinspection.GitHubReader, string, sourceinspection.GitHubSource) (sourceinspection.Result, error) {
			return inspection, nil
		},
	}
}

func generatedAutoDeployPreflightRequest() autodeploy.DispatchPreflightRequest {
	return autodeploy.DispatchPreflightRequest{
		ApplicationID: "11111111-1111-4111-8111-111111111111", OwnerUserID: "owner", ResolvedSHA: strings.Repeat("a", 40),
		Source: autodeploy.SourceScope{
			OwnerUserID: "owner", ConnectionID: "connection", InstallationID: 10, RepositoryID: 20,
			Branch: "main", Ref: "refs/heads/main",
		},
	}
}

func generatedAutoDeployPlanFixture(t *testing.T, manualRun bool) (deploymentplans.DeploymentPlanRevision, projectanalysis.SourceAnalysis) {
	t.Helper()
	component := deploymentplans.Component{
		Name: "app", Role: "server", RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci",
		NodeVersion: "22.14.0", RunCommand: "npm start", InternalPort: 3000, HealthProbe: "/health",
	}
	provenance := make([]deploymentplans.FieldProvenance, 0, 8)
	for _, field := range []string{"role", "rootDirectory", "packageManager", "installBehavior", "nodeVersion", "runCommand", "internalPort", "healthProbe"} {
		origin := deploymentplans.ProvenanceInferred
		if manualRun && field == "runCommand" {
			origin = deploymentplans.ProvenanceUser
			component.RunCommand = "node custom.js && echo ready"
		}
		provenance = append(provenance, deploymentplans.FieldProvenance{
			Field: "components.app." + field, Origin: origin, Confidence: 100, Evidence: []string{"package.json"},
		})
	}
	plan := deploymentplans.Plan{
		Strategy:   deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "projectanalysis", Version: projectanalysis.SchemaVersion, SourceStructuralFingerprint: strings.Repeat("1", 64)},
		Source:     deploymentplans.SourceIdentity{Provider: "github", RepositoryID: 20, ResolvedDigest: strings.Repeat("a", 40)},
		Components: []deploymentplans.Component{component}, FieldProvenance: provenance,
	}
	digest, err := deploymentplans.CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	revision := deploymentplans.DeploymentPlanRevision{
		ID: "22222222-2222-4222-8222-222222222222", AppID: "11111111-1111-4111-8111-111111111111",
		RevisionNumber: 1, Plan: plan, CanonicalDigest: digest, State: deploymentplans.RevisionAccepted,
	}
	analysis := projectanalysis.SourceAnalysis{
		SchemaVersion: projectanalysis.SchemaVersion, StructuralFingerprint: strings.Repeat("1", 64),
		Candidates: []projectanalysis.DeploymentPlanCandidate{{
			Kind: projectanalysis.PlanKindJavaScript, Status: projectanalysis.StatusReady,
			PackageManager: projectanalysis.PackageManager{Name: "npm"}, NodeVersion: projectanalysis.InferredValue{Value: "22.14.0"},
			Install: &projectanalysis.Command{Command: "npm ci"},
			Components: []projectanalysis.Component{{
				ID: "app", Kind: "server", RootDirectory: ".", Run: &projectanalysis.Command{Command: "npm start"},
				InternalPort: &projectanalysis.InferredValue{Value: "3000"}, HealthProbe: &projectanalysis.HealthProbe{Path: "/health"},
			}},
		}},
	}
	return revision, analysis
}

func generatedAutoDeployRelease(request autodeploy.DispatchPreflightRequest, revision deploymentplans.DeploymentPlanRevision) releasesnapshot.Release {
	return releasesnapshot.Release{
		ID: "44444444-4444-4444-8444-444444444444", AppID: request.ApplicationID,
		SourceProvider: "github", RepositoryID: request.Source.RepositoryID, ResolvedSHA: request.ResolvedSHA,
		WorkspaceState: releasesnapshot.WorkspaceStateReady, DeploymentPlanRevisionID: revision.ID,
		DeploymentPlanRevisionNumber: revision.RevisionNumber,
	}
}

func TestAutoDeployObserverLogUsesOnlyAggregateAllowlist(t *testing.T) {
	const secret = "app=identity repo=private sha=aaaaaaaa token=secret raw=provider-body"
	var logs bytes.Buffer
	logger := newStructuredLogger(&logs, "info")
	observeAutoDeployLifecycle(logger, autodeploy.CoordinatorEvent{
		Outcome: secret, State: secret, PauseCode: secret, NextAction: secret, RetryAttempt: ^uint32(0),
		Claimed: 1, Resolved: 2, Dispatched: 3, Paused: 4, Retried: 5,
	})
	output := logs.String()
	if strings.Contains(output, secret) || strings.Contains(output, "identity") || strings.Contains(output, "private") || strings.Contains(output, "aaaaaaaa") || strings.Contains(output, "provider-body") ||
		!strings.Contains(output, autodeploy.OutcomePersistenceUnavailable) || !strings.Contains(output, `"state":"none"`) || !strings.Contains(output, `"pause_code":"none"`) ||
		!strings.Contains(output, `"retry_attempt":1000`) || !strings.Contains(output, `"next_action":"none"`) || !strings.Contains(output, `"dispatched":3`) {
		t.Fatalf("unsafe aggregate log=%q", output)
	}
}

func TestAutoDeployObserverLogPreservesAllowlistedLifecycleFields(t *testing.T) {
	var logs bytes.Buffer
	observeAutoDeployLifecycle(newStructuredLogger(&logs, "info"), autodeploy.CoordinatorEvent{
		Outcome: autodeploy.OutcomeProviderUnavailable, State: autodeploy.StateRetryWait,
		PauseCode: autodeploy.ObservedPauseNone, RetryAttempt: 3, NextAction: autodeploy.NextActionRetry,
		Claimed: 1, Retried: 1,
	})
	output := logs.String()
	for _, expected := range []string{
		autodeploy.OutcomeProviderUnavailable, `"state":"retry_wait"`, `"pause_code":"none"`,
		`"retry_attempt":3`, `"next_action":"retry"`, `"claimed":1`, `"retried":1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("lifecycle log missing %q output=%q", expected, output)
		}
	}
}

func TestSafeSourceErrorTreatsLocalPersistenceAsTransient(t *testing.T) {
	for _, code := range []string{"internal_error", "provider_unavailable", "connection_not_found", "invalid_source"} {
		mapped := safeSourceError(&sourceconnections.Error{Code: code})
		var sourceErr *autodeploy.SourceError
		if !errors.As(mapped, &sourceErr) {
			t.Fatalf("code=%q mapped=%T", code, mapped)
		}
		want := autodeploy.OutcomeInvalidSource
		switch code {
		case "internal_error":
			want = autodeploy.OutcomePersistenceUnavailable
		case "provider_unavailable":
			want = autodeploy.OutcomeProviderUnavailable
		case "connection_not_found":
			want = autodeploy.OutcomeSourceAccessLost
		}
		if sourceErr.Code != want {
			t.Fatalf("code=%q mapped=%q want=%q", code, sourceErr.Code, want)
		}
	}
}

func enabledAutoDeployConfig() config.Config {
	cfg := config.Defaults()
	cfg.ComposeRuntime = true
	cfg.GitHubClientID = "client"
	cfg.GitHubAppSlug = "app"
	return cfg
}
