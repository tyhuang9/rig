package generatedexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

const (
	testAppID        = "11111111-1111-4111-8111-111111111111"
	testJobID        = "22222222-2222-4222-8222-222222222222"
	testActorID      = "33333333-3333-4333-8333-333333333333"
	testDeploymentID = "44444444-4444-4444-8444-444444444444"
	testReleaseID    = "55555555-5555-4555-8555-555555555555"
	testPlanID       = "66666666-6666-4666-8666-666666666666"
	testConfigID     = "77777777-7777-4777-8777-777777777777"
	testArtifactID   = "88888888-8888-4888-8888-888888888888"
	testArtifactID2  = "99999999-9999-4999-8999-999999999999"
)

type fakeApplications struct {
	app apps.Application
	err error
}

func (f *fakeApplications) Get(string) (apps.Application, error) { return f.app, f.err }

type fakeReleases struct {
	release           releasesnapshot.Release
	onMaterialize     func()
	materializeCalls  int
	readyWorkspaceIDs []string
}

func (f *fakeReleases) Materialize(context.Context, string, string) (releasesnapshot.Release, error) {
	f.materializeCalls++
	if f.onMaterialize != nil {
		f.onMaterialize()
	}
	return f.release, nil
}
func (f *fakeReleases) MaterializeLocal(context.Context, string, string) (releasesnapshot.Release, error) {
	return f.Materialize(context.Background(), "", "")
}
func (f *fakeReleases) ReadyWorkspace(_ context.Context, _, releaseID string) (releasesnapshot.Release, error) {
	f.readyWorkspaceIDs = append(f.readyWorkspaceIDs, releaseID)
	return f.release, nil
}

type fakeConfiguration struct {
	exports        int
	value          []byte
	currentID      string
	currentNumber  int64
	planID         string
	planNumber     int64
	exportMismatch bool
}

func (f *fakeConfiguration) RevisionIdentity(context.Context, string) (appconfig.RevisionIdentity, error) {
	id, number := f.currentID, f.currentNumber
	if id == "" {
		id, number = testConfigID, 3
	}
	planID, planNumber := f.planID, f.planNumber
	if planID == "" {
		planID, planNumber = testPlanID, 2
	}
	return appconfig.RevisionIdentity{RevisionID: id, RevisionNumber: number, FormatVersion: 2, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: planNumber}, nil
}
func (f *fakeConfiguration) ExactRevisionIdentity(_ context.Context, _, revisionID string, revisionNumber int64) (appconfig.RevisionIdentity, error) {
	planID, planNumber := f.planID, f.planNumber
	if planID == "" {
		planID, planNumber = testPlanID, 2
	}
	return appconfig.RevisionIdentity{RevisionID: revisionID, RevisionNumber: revisionNumber, FormatVersion: 2, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: planNumber}, nil
}
func (f *fakeConfiguration) ExportComponentRuntimeForExecution(_ context.Context, _, revisionID string, revisionNumber int64, _ string, _ int64, _ string) (appconfig.ExecutionConfiguration, error) {
	if f.exportMismatch {
		revisionNumber++
	}
	return f.export(revisionID, revisionNumber), nil
}
func (f *fakeConfiguration) export(revisionID string, revisionNumber int64) appconfig.ExecutionConfiguration {
	f.exports++
	return appconfig.ExecutionConfiguration{RevisionID: revisionID, RevisionNumber: revisionNumber, Environment: append([]byte(nil), f.value...)}
}

type fakeDeployments struct {
	deployment          deployments.Deployment
	getErr              error
	transitionErr       error
	transitionErrStatus deployments.Status
	state               *fakeRuntimeState
	finishErr           error
	finishRequests      []deployments.GeneratedNetworkRequest
	migrationRepository *deployments.Repository
	migrationContext    func(context.Context) context.Context
	migrationRequests   []deployments.GeneratedMigrationRequest
	migrationErr        error
}

func (f *fakeDeployments) GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error) {
	return f.deployment, false, nil
}
func (f *fakeDeployments) Get(context.Context, string, string) (deployments.Deployment, error) {
	if f.getErr != nil {
		return deployments.Deployment{}, f.getErr
	}
	return f.deployment, nil
}
func (f *fakeDeployments) Gate(_ context.Context, request deployments.GateRequest) error {
	if request.DeploymentID != f.deployment.ID || request.Claim.ApplicationID() != f.deployment.AppID {
		return deployments.ErrInvalidDeployment
	}
	switch f.deployment.Status {
	case deployments.Preparing, deployments.NeedsAttention:
		f.deployment.Status = deployments.Applying
		f.deployment.DiagnosticCode = ""
		return nil
	case deployments.Applying, deployments.WaitingHealth:
		return nil
	default:
		return deployments.ErrInvalidTransition
	}
}
func (f *fakeDeployments) InitializeRuntime(_ context.Context, _, _, releaseID, configurationID string, configurationNumber int64, strategy deployments.RuntimeStrategy, planID string, planNumber int64) (deployments.Deployment, error) {
	f.deployment.ReleaseID = releaseID
	f.deployment.ActualConfigurationRevisionID = configurationID
	f.deployment.ActualConfigurationRevisionNumber = configurationNumber
	f.deployment.RuntimeStrategy = strategy
	f.deployment.DeploymentPlanRevisionID = planID
	f.deployment.DeploymentPlanRevisionNumber = planNumber
	f.deployment.ProvenanceInitialized = true
	return f.deployment, nil
}
func (f *fakeDeployments) Transition(_ context.Context, _, _ string, status deployments.Status, diagnostic string) (deployments.Deployment, error) {
	if f.transitionErr != nil && status == f.transitionErrStatus {
		return deployments.Deployment{}, f.transitionErr
	}
	f.deployment.Status = status
	f.deployment.DiagnosticCode = diagnostic
	return f.deployment, nil
}

func (f *fakeDeployments) FinishGeneratedImagePreparation(_ context.Context, request deployments.GeneratedNetworkRequest) (generatedruntimestate.Phase, error) {
	f.finishRequests = append(f.finishRequests, request)
	if f.finishErr != nil {
		return "", f.finishErr
	}
	if f.state == nil {
		return "", deployments.ErrInvalidDeployment
	}
	next := generatedruntimestate.PhaseStartingCandidate
	if f.state.deployment.MigrationState == generatedruntimestate.MigrationPending {
		next = generatedruntimestate.PhaseMigrating
	}
	f.state.deployment.Phase = next
	return next, nil
}

func (f *fakeDeployments) PinGeneratedMigration(ctx context.Context, request deployments.GeneratedMigrationRequest) (*deployments.GeneratedMigrationExecution, error) {
	f.migrationRequests = append(f.migrationRequests, request)
	if f.migrationErr != nil {
		return nil, f.migrationErr
	}
	if f.migrationRepository == nil || f.migrationContext == nil {
		return nil, deployments.ErrInvalidDeployment
	}
	return f.migrationRepository.PinGeneratedMigration(f.migrationContext(ctx), request)
}

type fakePlans struct {
	revision deploymentplans.DeploymentPlanRevision
	head     *deploymentplans.DeploymentPlanRevision
	err      error
}

func (f *fakePlans) Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error) {
	if f.head != nil {
		return *f.head, f.err
	}
	return f.revision, f.err
}

func (f *fakePlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return f.revision, f.err
}

type fakeCompiler struct {
	events     *[]string
	artifacts  []generatedimage.Artifact
	errs       []error
	calls      int
	pins       []appconfig.RevisionIdentity
	requests   []generatedimage.CompileRequest
	before     func(generatedimage.CompileRequest)
	state      *fakeRuntimeState
	repository *deployments.Repository
	release    releasesnapshot.Release
	plan       deploymentplans.DeploymentPlanRevision
}

func (f *fakeCompiler) Compile(ctx context.Context, request generatedimage.CompileRequest) (generatedimage.Artifact, error) {
	*f.events = append(*f.events, "compile")
	f.requests = append(f.requests, request)
	f.pins = append(f.pins, appconfig.RevisionIdentity{RevisionID: request.ConfigurationRevisionID, RevisionNumber: request.ConfigurationRevisionNumber})
	if f.before != nil {
		f.before(request)
	}
	index := f.calls
	f.calls++
	if index < len(f.errs) && f.errs[index] != nil {
		return generatedimage.Artifact{}, f.errs[index]
	}
	if index >= len(f.artifacts) {
		index = len(f.artifacts) - 1
	}
	artifact := f.artifacts[index]
	if f.repository != nil {
		execution, err := f.repository.PinGeneratedImage(ctx, deployments.GeneratedImageRequest{
			Claim: request.Claim, DeploymentID: request.DeploymentID, ReleaseID: request.ReleaseID,
			ConfigurationRevisionID: request.ConfigurationRevisionID, ConfigurationRevisionNumber: request.ConfigurationRevisionNumber,
			DeploymentPlanRevisionID: request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: request.DeploymentPlanRevisionNumber,
			ComponentName: request.ComponentName, CompilerVersion: artifact.CompilerVersion, BuildDefinitionDigest: artifact.BuildDefinitionDigest,
			PlanCanonicalDigest: f.plan.CanonicalDigest,
			SourceProvider:      f.release.SourceProvider, SourceRepositoryID: f.release.RepositoryID, SourceResolvedDigest: f.release.ResolvedSHA,
			PlanSourceProvider: f.plan.Plan.Source.Provider, PlanSourceRepositoryID: f.plan.Plan.Source.RepositoryID, PlanSourceResolvedDigest: f.plan.Plan.Source.ResolvedDigest,
			ArchiveSHA256: f.release.ArchiveSHA256, WorkspaceTreeSHA256: f.release.WorkspaceTreeSHA256,
			Findings: append([]deployments.Finding(nil), request.Findings...),
		})
		if err != nil {
			return generatedimage.Artifact{}, err
		}
		defer execution.Leave()
		selected, created, err := execution.SelectArtifact()
		if err != nil || selected.ID != artifact.ID {
			return generatedimage.Artifact{}, errors.Join(err, deployments.ErrInvalidDeployment)
		}
		if created {
			err = execution.RecordBuiltImageReady(artifact.ImageContentID)
		} else {
			err = execution.RecordReusedImageReady()
		}
		if err != nil {
			return generatedimage.Artifact{}, err
		}
		selected, _ = execution.SelectedArtifact()
		if selected.ID == "" {
			return generatedimage.Artifact{}, deployments.ErrInvalidDeployment
		}
		return selected, nil
	}
	if f.state != nil {
		component := f.state.component(request.ComponentName)
		component.State, component.ImageArtifactID = generatedruntimestate.ComponentImageReady, artifact.ID
	}
	return artifact, nil
}

type fakeArtifacts struct {
	values            map[string]generatedimage.Artifact
	markedUnavailable []string
}

func (f *fakeArtifacts) Get(_ context.Context, id string) (generatedimage.Artifact, error) {
	value, exists := f.values[id]
	if !exists {
		return generatedimage.Artifact{}, generatedimage.ErrArtifactNotFound
	}
	return value, nil
}
func (f *fakeArtifacts) MarkUnavailable(_ context.Context, id string) (generatedimage.Artifact, error) {
	f.markedUnavailable = append(f.markedUnavailable, id)
	value := f.values[id]
	value.State = generatedimage.ArtifactUnavailable
	f.values[id] = value
	return value, nil
}

type fakeRuntimeState struct {
	deployment        generatedruntimestate.Deployment
	previous          map[string]generatedruntimestate.Deployment
	active            generatedruntimestate.ActiveHead
	beginErr          error
	getErr            error
	migrationRequired bool
	advanceErrTo      generatedruntimestate.Phase
	advanceErr        error
}

func (f *fakeRuntimeState) Begin(_ context.Context, input generatedruntimestate.BeginInput) (generatedruntimestate.Deployment, bool, error) {
	if f.beginErr != nil {
		return generatedruntimestate.Deployment{}, false, f.beginErr
	}
	if f.deployment.DeploymentID == "" {
		migrationState := generatedruntimestate.MigrationNotRequired
		if f.migrationRequired {
			migrationState = generatedruntimestate.MigrationPending
		}
		candidateSlot := "blue"
		if f.active.Slot == "blue" {
			candidateSlot = "green"
		}
		f.deployment = generatedruntimestate.Deployment{
			DeploymentID: input.DeploymentID, AppID: input.AppID, ReleaseID: input.ReleaseID,
			DeploymentPlanRevisionID: input.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: input.DeploymentPlanRevisionNumber,
			CandidateSlot: candidateSlot, PreviousActiveDeploymentID: f.active.DeploymentID, PreviousActiveSlot: f.active.Slot,
			Phase: generatedruntimestate.PhasePreflight, MigrationState: migrationState,
		}
		for _, name := range input.ComponentNames {
			f.deployment.Components = append(f.deployment.Components, generatedruntimestate.Component{DeploymentID: input.DeploymentID, Name: name, Slot: candidateSlot, State: generatedruntimestate.ComponentPending})
		}
	}
	return f.deployment, true, nil
}
func (f *fakeRuntimeState) Get(_ context.Context, _, deploymentID string) (generatedruntimestate.Deployment, error) {
	if f.getErr != nil {
		return generatedruntimestate.Deployment{}, f.getErr
	}
	if deploymentID == f.deployment.DeploymentID {
		return f.deployment, nil
	}
	if previous, exists := f.previous[deploymentID]; exists {
		return previous, nil
	}
	return generatedruntimestate.Deployment{}, generatedruntimestate.ErrNotFound
}
func (f *fakeRuntimeState) Active(context.Context, string) (generatedruntimestate.ActiveHead, error) {
	return f.active, nil
}
func (f *fakeRuntimeState) Advance(_ context.Context, _, _ string, _, next generatedruntimestate.Phase, diagnostic generatedruntimestate.DiagnosticCode) (generatedruntimestate.Deployment, error) {
	if next == f.advanceErrTo && f.advanceErr != nil {
		return generatedruntimestate.Deployment{}, f.advanceErr
	}
	f.deployment.Phase = next
	f.deployment.DiagnosticCode = diagnostic
	return f.deployment, nil
}
func (f *fakeRuntimeState) SetImageReady(_ context.Context, _, _, name, artifactID string) (generatedruntimestate.Component, error) {
	component := f.component(name)
	component.State, component.ImageArtifactID = generatedruntimestate.ComponentImageReady, artifactID
	return *component, nil
}
func (f *fakeRuntimeState) SetContainerStarting(_ context.Context, _, _, name, containerName string) (generatedruntimestate.Component, error) {
	component := f.component(name)
	component.State, component.ContainerName = generatedruntimestate.ComponentStarting, containerName
	return *component, nil
}
func (f *fakeRuntimeState) SetContainerRunning(_ context.Context, _, _, name, containerID string) (generatedruntimestate.Component, error) {
	component := f.component(name)
	component.State, component.ContainerID = generatedruntimestate.ComponentRunning, containerID
	return *component, nil
}
func (f *fakeRuntimeState) AdvanceComponent(_ context.Context, _, deploymentID, name string, _, next generatedruntimestate.ComponentState) (generatedruntimestate.Component, error) {
	if deploymentID == f.deployment.DeploymentID {
		component := f.component(name)
		component.State = next
		return *component, nil
	}
	previous, exists := f.previous[deploymentID]
	if !exists {
		return generatedruntimestate.Component{}, generatedruntimestate.ErrNotFound
	}
	for index := range previous.Components {
		if previous.Components[index].Name == name {
			previous.Components[index].State = next
			f.previous[deploymentID] = previous
			return previous.Components[index], nil
		}
	}
	return generatedruntimestate.Component{}, generatedruntimestate.ErrNotFound
}
func (f *fakeRuntimeState) FailComponent(context.Context, string, string, string, generatedruntimestate.ComponentState, generatedruntimestate.DiagnosticCode) (generatedruntimestate.Component, error) {
	return generatedruntimestate.Component{}, nil
}
func (f *fakeRuntimeState) component(name string) *generatedruntimestate.Component {
	for index := range f.deployment.Components {
		if f.deployment.Components[index].Name == name {
			return &f.deployment.Components[index]
		}
	}
	panic("missing fake component")
}

type fakeRuntime struct {
	delegate        runtimeEngine
	events          *[]string
	stopped         int
	stopErrs        []error
	networkRequests []deployments.GeneratedNetworkRequest
	networkErrs     []error
	networkCalls    int
	retained        int
}

func (f *fakeRuntime) ReserveReplacement(ctx context.Context, request generatedingress.GeneratedReplacementRequest) (generatedruntime.ReplacementReservation, error) {
	if f.delegate != nil {
		return f.delegate.ReserveReplacement(ctx, request)
	}
	return nil, errors.New("fake runtime cannot mint a generated replacement reservation")
}
func (f *fakeRuntime) RetainReplacementCapacity(reservation generatedruntime.ReplacementReservation) error {
	f.retained++
	if f.delegate != nil {
		return f.delegate.RetainReplacementCapacity(reservation)
	}
	return nil
}

func (f *fakeRuntime) EnsureAppNetwork(ctx context.Context, request deployments.GeneratedNetworkRequest) error {
	*f.events = append(*f.events, "ensure_network")
	f.networkRequests = append(f.networkRequests, request)
	index := f.networkCalls
	f.networkCalls++
	if index < len(f.networkErrs) {
		return f.networkErrs[index]
	}
	if f.delegate != nil {
		return f.delegate.EnsureAppNetwork(ctx, request)
	}
	return nil
}
func (f *fakeRuntime) PinCandidates(ctx context.Context, request generatedingress.GeneratedCandidateRequest, reservation generatedruntime.ReplacementReservation) (*generatedingress.GeneratedCandidateExecution, error) {
	if f.delegate != nil {
		return f.delegate.PinCandidates(ctx, request, reservation)
	}
	return nil, errors.New("fake runtime cannot mint a generated candidate execution")
}
func (f *fakeRuntime) StopAndRemove(ctx context.Context, candidate generatedruntime.Candidate, timeout time.Duration) error {
	call := f.stopped
	f.stopped++
	if call < len(f.stopErrs) {
		return f.stopErrs[call]
	}
	if f.delegate != nil {
		return f.delegate.StopAndRemove(ctx, candidate, timeout)
	}
	return nil
}

type fakeAuthorization struct {
	delegate     AuthorizationGate
	events       *[]string
	err          error
	deployments  *fakeDeployments
	calls        int
	after        func()
	reservation  generatedruntime.ReplacementReservation
	reservations []generatedruntime.ReplacementReservation
}

func (f *fakeAuthorization) Authorize(ctx context.Context, request AuthorizationRequest) (generatedruntime.ReplacementReservation, error) {
	f.calls++
	*f.events = append(*f.events, "authorize")
	if f.after != nil {
		f.after()
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.delegate != nil {
		reservation, err := f.delegate.Authorize(ctx, request)
		if reservation != nil {
			f.reservations = append(f.reservations, reservation)
		}
		return reservation, err
	}
	if f.deployments.deployment.Status == deployments.Preparing || f.deployments.deployment.Status == deployments.NeedsAttention {
		f.deployments.deployment.Status = deployments.Applying
	}
	if f.reservation == nil {
		return nil, errors.New("fake authorization cannot mint a generated replacement reservation")
	}
	f.reservations = append(f.reservations, f.reservation)
	return f.reservation, nil
}

type fakeRoutes struct {
	events            *[]string
	requests          []generatedruntime.RouteSwitchRequest
	inputs            []generatedruntimestate.Deployment
	delegate          routeController
	lastErr           error
	err               error
	errs              []error
	postActivateErr   error
	lastActivation    routeActivation
	delegateCalls     int
	attestErr         error
	attestCalls       int
	retirementCalls   int
	retirementErr     error
	retirementBlock   error
	retirementContext func(context.Context) context.Context
}

func (f *fakeRoutes) attestPublished(ctx context.Context, claim deploymentexecution.Claim, deploymentID string) error {
	f.attestCalls++
	if f.attestErr != nil {
		return f.attestErr
	}
	if f.delegate != nil {
		return f.delegate.attestPublished(ctx, claim, deploymentID)
	}
	return nil
}

func (f *fakeRoutes) Switch(_ context.Context, request generatedruntime.RouteSwitchRequest) error {
	*f.events = append(*f.events, "route")
	call := len(f.requests)
	f.requests = append(f.requests, request)
	if call < len(f.errs) && f.errs[call] != nil {
		return f.errs[call]
	}
	return f.err
}

func (f *fakeRoutes) activate(ctx context.Context, execution *generatedingress.GeneratedCandidateExecution,
	request generatedruntime.RouteSwitchRequest, current generatedruntimestate.Deployment,
) (routeActivation, error) {
	f.inputs = append(f.inputs, current)
	call := len(f.requests)
	if call < len(f.errs) && f.errs[call] != nil {
		*f.events = append(*f.events, "route")
		f.requests = append(f.requests, request)
		return routeActivation{}, f.errs[call]
	}
	if f.err != nil {
		*f.events = append(*f.events, "route")
		f.requests = append(f.requests, request)
		return routeActivation{}, f.err
	}
	if f.delegate != nil {
		*f.events = append(*f.events, "route")
		f.requests = append(f.requests, request)
		f.delegateCalls++
		updated, err := f.delegate.activate(ctx, execution, request, current)
		if updated.retirement != nil {
			updated.retirement = &recordingPredecessorRetirement{delegate: updated.retirement, owner: f}
		}
		f.lastActivation = updated
		f.lastErr = err
		if err == nil && f.postActivateErr != nil {
			err, f.postActivateErr = f.postActivateErr, nil
			f.lastErr = err
		}
		return updated, err
	}
	if err := f.Switch(context.Background(), request); err != nil {
		return routeActivation{}, err
	}
	current.Phase = generatedruntimestate.PhaseDraining
	return routeActivation{deployment: current, retirement: &fakePredecessorRetirement{}}, nil
}

type recordingPredecessorRetirement struct {
	delegate predecessorRetirement
	owner    *fakeRoutes
}

func (r *recordingPredecessorRetirement) Retire(ctx context.Context) (generatedruntimestate.PredecessorRetirementReceipt, error) {
	r.owner.retirementCalls++
	if r.owner.retirementBlock != nil {
		r.owner.retirementErr = r.owner.retirementBlock
		return generatedruntimestate.PredecessorRetirementReceipt{}, r.owner.retirementBlock
	}
	if r.owner.retirementContext != nil {
		ctx = r.owner.retirementContext(ctx)
	}
	receipt, err := r.delegate.Retire(ctx)
	r.owner.retirementErr = err
	return receipt, err
}

func (r *recordingPredecessorRetirement) Complete(ctx context.Context) error {
	r.owner.retirementCalls++
	if r.owner.retirementBlock != nil {
		r.owner.retirementErr = r.owner.retirementBlock
		return r.owner.retirementBlock
	}
	if r.owner.retirementContext != nil {
		ctx = r.owner.retirementContext(ctx)
	}
	err := r.delegate.Complete(ctx)
	r.owner.retirementErr = err
	return err
}

func (r *recordingPredecessorRetirement) Leave() { r.delegate.Leave() }

type fakePredecessorRetirement struct {
	err   error
	calls int
	left  bool
}

func (f *fakePredecessorRetirement) Retire(context.Context) (generatedruntimestate.PredecessorRetirementReceipt, error) {
	f.calls++
	return generatedruntimestate.PredecessorRetirementReceipt{}, f.err
}

func (f *fakePredecessorRetirement) Complete(context.Context) error {
	f.calls++
	return f.err
}

func (f *fakePredecessorRetirement) Leave() { f.left = true }

type candidateMayBeLiveRouteError struct{}

func (candidateMayBeLiveRouteError) Error() string            { return "route outcome unresolved" }
func (candidateMayBeLiveRouteError) CandidateMayBeLive() bool { return true }

type gatewayReadinessRouteError struct{}

func (gatewayReadinessRouteError) Error() string                { return "gateway probe failed" }
func (gatewayReadinessRouteError) GatewayReadinessFailed() bool { return true }

type fakeMigrations struct {
	events   *[]string
	requests []deployments.GeneratedMigrationInvocation
	delegate generatedruntime.MigrationRunner
	err      error
}

func (f *fakeMigrations) Run(ctx context.Context, request deployments.GeneratedMigrationInvocation) error {
	*f.events = append(*f.events, "migration")
	f.requests = append(f.requests, request)
	if f.err != nil {
		return f.err
	}
	if f.delegate == nil {
		return nil
	}
	return f.delegate.Run(ctx, request)
}

type fakeReporter struct {
	phases    []string
	failPhase string
	err       error
}

func (f *fakeReporter) Report(update jobs.ProgressUpdate) error {
	f.phases = append(f.phases, update.Phase)
	if update.Phase == f.failPhase {
		return f.err
	}
	return nil
}

type executorFixture struct {
	executor           *Executor
	claim              deploymentexecution.Claim
	configuration      *fakeConfiguration
	deployments        *fakeDeployments
	state              *fakeRuntimeState
	compiler           *fakeCompiler
	artifacts          *fakeArtifacts
	runtime            *fakeRuntime
	authorization      *fakeAuthorization
	routes             *fakeRoutes
	migrations         *fakeMigrations
	reporter           *fakeReporter
	events             *[]string
	plan               deploymentplans.DeploymentPlanRevision
	migrationAuthority *executorMigrationAuthority
}

func newExecutorFixture(t *testing.T, migration bool) *executorFixture {
	t.Helper()
	events := []string{}
	component := deploymentplans.Component{Name: "api", Role: generatedruntime.RoleServer, RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "22", BuildCommand: "npm run build", RunCommand: "npm start && echo $READY", InternalPort: 3000, HealthProbe: "/health"}
	plan := deploymentplans.DeploymentPlanRevision{ID: testPlanID, AppID: testAppID, RevisionNumber: 2, State: deploymentplans.RevisionAccepted, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{component}}}
	if migration {
		plan.Plan.Migration = &deploymentplans.Migration{ComponentName: "api", RootDirectory: ".", Command: "npm run migrate && echo $DATABASE_URL", EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: string(make([]byte, 64)), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalApproved}}
	}
	release := releasesnapshot.Release{ID: testReleaseID, AppID: testAppID, DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2, ConfigurationRevisionID: testConfigID, ConfigurationRevisionNumber: 3}
	artifact := readyArtifact(testArtifactID)
	claim := newGeneratedExecutorClaim(t)
	var authority *executorMigrationAuthority
	if migration {
		authority = newExecutorMigrationAuthority(t, component)
		plan, release, artifact, claim = authority.plan, authority.release, authority.artifact, authority.claim
	}
	deploymentsFake := &fakeDeployments{deployment: deployments.Deployment{ID: testDeploymentID, AppID: testAppID, JobID: testJobID, Status: deployments.Preparing, ConfigurationMode: "current"}}
	if authority != nil {
		deploymentsFake.deployment.ID = authority.deploymentID
		deploymentsFake.migrationRepository = authority.repository
		deploymentsFake.migrationContext = authority.withScope
	}
	state := &fakeRuntimeState{active: generatedruntimestate.ActiveHead{AppID: testAppID}, migrationRequired: migration}
	deploymentsFake.state = state
	compiler := &fakeCompiler{events: &events, artifacts: []generatedimage.Artifact{artifact}, state: state}
	configuration := &fakeConfiguration{value: []byte("DATABASE_URL=secret\n")}
	if authority != nil {
		configuration.currentID = release.ConfigurationRevisionID
		configuration.currentNumber = release.ConfigurationRevisionNumber
		configuration.planID = plan.ID
		configuration.planNumber = plan.RevisionNumber
	}
	artifacts := &fakeArtifacts{values: map[string]generatedimage.Artifact{artifact.ID: artifact}}
	runtime := &fakeRuntime{events: &events}
	authorization := &fakeAuthorization{events: &events, deployments: deploymentsFake}
	planReader := &fakePlans{revision: plan}
	if authority != nil {
		runtime.delegate = authority.engine
		gate, err := NewAuthorizationGate(deploymentsFake, planReader, authority.engine)
		if err != nil {
			t.Fatal(err)
		}
		authorization.delegate = gate
	}
	routes := &fakeRoutes{events: &events}
	migrations := &fakeMigrations{events: &events}
	if authority != nil {
		migrations.delegate = authority.runner
	}
	executor, err := newExecutor(
		&fakeApplications{app: apps.Application{ID: testAppID, Source: apps.Source{Type: apps.SourceLocal, Path: `C:\source`}}},
		&fakeReleases{release: release}, configuration, deploymentsFake,
		planReader, compiler, artifacts, state, runtime, authorization, routes, migrations, Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &executorFixture{executor: executor, claim: claim, configuration: configuration, deployments: deploymentsFake, state: state, compiler: compiler, artifacts: artifacts, runtime: runtime, authorization: authorization, routes: routes, migrations: migrations, reporter: &fakeReporter{}, events: &events, plan: plan, migrationAuthority: authority}
}

func (f *executorFixture) execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if f.migrationAuthority != nil {
		ctx = f.migrationAuthority.withScope(ctx)
	}
	return f.executor.ExecuteBound(ctx, job, f.claim, reporter)
}

func (f *executorFixture) deploymentJob() jobs.Job {
	job := deploymentJob()
	job.Attempt = f.claim.Attempt()
	return job
}

func TestGeneratedExecutorRejectsZeroOrMismatchedClaimBeforePreparation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*jobs.Job, *deploymentexecution.Claim)
	}{
		{name: "zero claim", mutate: func(_ *jobs.Job, claim *deploymentexecution.Claim) { *claim = deploymentexecution.Claim{} }},
		{name: "job mismatch", mutate: func(job *jobs.Job, _ *deploymentexecution.Claim) { job.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" }},
		{name: "application mismatch", mutate: func(job *jobs.Job, _ *deploymentexecution.Claim) {
			job.ResourceID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		}},
		{name: "attempt mismatch", mutate: func(job *jobs.Job, _ *deploymentexecution.Claim) { job.Attempt++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, false)
			job, claim := deploymentJob(), fixture.claim
			test.mutate(&job, &claim)
			result, err := fixture.executor.ExecuteBound(context.Background(), job, claim, fixture.reporter)
			var executionErr *jobs.ExecutionError
			if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || executionErr.Code != "validation_failed" || !errors.Is(err, deploymentexecution.ErrInvalidClaim) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if len(fixture.reporter.phases) != 0 || len(*fixture.events) != 0 || fixture.configuration.exports != 0 ||
				fixture.compiler.calls != 0 || fixture.authorization.calls != 0 || fixture.state.deployment.DeploymentID != "" {
				t.Fatalf("claim refusal performed work: progress=%#v events=%#v exports=%d compiler=%d authorization=%d runtime=%#v", fixture.reporter.phases, *fixture.events, fixture.configuration.exports, fixture.compiler.calls, fixture.authorization.calls, fixture.state.deployment)
			}
		})
	}
}

func TestExecutorPreservesAuthorizationRefusalAndCancellationWithoutFailureMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  error
		cancel bool
	}{
		{name: "cancelled approval", cause: deployments.ErrApprovalRequired, cancel: true},
		{name: "cancelled rejection", cause: deployments.ErrRejectedCapability, cancel: true},
		{name: "cancelled authority", cause: deploymentexecution.ErrAuthorityStale, cancel: true},
		{name: "live authority refusal", cause: deploymentexecution.ErrAuthorityStale},
		{name: "live evidence refusal", cause: deployments.ErrInvalidDeployment},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, false)
			fixture.authorization.err = test.cause
			var ctx context.Context = context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				ctx = cancelled
				fixture.authorization.after = cancel
			}
			job := deploymentJob()
			result, err := fixture.execute(ctx, job, fixture.reporter)
			var executionErr *jobs.ExecutionError
			if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" ||
				!errors.Is(err, test.cause) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if test.cancel && (!errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancellation cause missing: %v", err)
			}
			if !test.cancel && errors.Is(err, deploymentexecution.ErrCanceled) {
				t.Fatalf("live refusal was relabeled as cancellation: %v", err)
			}
			if fixture.deployments.deployment.Status != deployments.Preparing || fixture.deployments.deployment.DiagnosticCode != "" ||
				fixture.state.deployment.Phase != generatedruntimestate.PhaseBuilding || fixture.state.deployment.DiagnosticCode != "" ||
				len(fixture.state.deployment.Components) != 1 || fixture.state.deployment.Components[0].State != generatedruntimestate.ComponentPending ||
				fixture.compiler.calls != 0 || fixture.authorization.calls != 1 || fixture.runtime.networkCalls != 0 || fixture.runtime.stopped != 0 {
				t.Fatalf("deployment=%#v runtime=%#v compiler=%d authorization=%d runtime calls=%#v", fixture.deployments.deployment, fixture.state.deployment, fixture.compiler.calls, fixture.authorization.calls, fixture.runtime)
			}
		})
	}
}

func newGeneratedExecutorClaim(t *testing.T) deploymentexecution.Claim {
	t.Helper()
	db, err := databasefixture.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stamp := time.Now().UTC()
	stampText := stamp.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, testActorID, testActorID, "hash", stampText, stampText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, testAppID, testAppID, testAppID, stampText, stampText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',0,?,'{}',?,?)`, testJobID, testAppID, testActorID, stampText, stampText); err != nil {
		t.Fatal(err)
	}
	validator, err := applifecycle.NewDeploymentBindingValidator(applifecycle.New(db))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindings.InsertNewTx(context.Background(), tx, testJobID, stamp)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stampText, stampText, testJobID); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, testJobID, testAppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func readyArtifact(id string) generatedimage.Artifact {
	return generatedimage.Artifact{
		ID: id, ReleaseID: testReleaseID, DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
		ComponentID: "api", BuildDefinitionDigest: string(make([]byte, 64)), ImageContentID: "sha256:" + string(make([]byte, 64)), State: generatedimage.ArtifactReady,
	}
}

func deploymentJob() jobs.Job {
	input, _ := json.Marshal(jobs.DeploymentInput{ReleaseID: testReleaseID, ConfigurationMode: jobs.ConfigurationCurrent})
	return jobs.Job{ID: testJobID, Type: "deploy", ResourceType: "application", ResourceID: testAppID, RequestedBy: testActorID, Attempt: 1, Input: input}
}

func reviewedDeploymentJob() jobs.Job {
	job := deploymentJob()
	job.Input, _ = json.Marshal(jobs.DeploymentInput{
		ConfigurationMode:      jobs.ConfigurationCurrent,
		ExpectedPlanRevisionID: testPlanID, ExpectedPlanRevisionNumber: 2,
		ExpectedConfigurationRevisionID: testConfigID, ExpectedConfigurationRevisionNumber: 3,
	})
	return job
}

func TestGeneratedExecutorRejectsReviewedRevisionDriftBeforeRuntimeSideEffects(t *testing.T) {
	for _, testCase := range []struct {
		name                 string
		mutate               func(*executorFixture)
		wantMaterializations int
	}{
		{"plan changed before worker", func(f *executorFixture) {
			f.executor.plans = &fakePlans{revision: deploymentplans.DeploymentPlanRevision{ID: testArtifactID, AppID: testAppID, RevisionNumber: 3, State: deploymentplans.RevisionAccepted, Plan: f.plan.Plan}}
		}, 0},
		{"materialized release has a different plan", func(f *executorFixture) {
			f.executor.releases.(*fakeReleases).release.DeploymentPlanRevisionID = testArtifactID
		}, 1},
		{"configuration changed during materialization", func(f *executorFixture) {
			f.executor.releases.(*fakeReleases).onMaterialize = func() { f.configuration.currentID, f.configuration.currentNumber = testArtifactID, 4 }
		}, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, false)
			testCase.mutate(fixture)
			_, err := fixture.execute(context.Background(), reviewedDeploymentJob(), fixture.reporter)
			requireExecutionErrorCode(t, err, "reviewed_revision_stale")
			if fixture.compiler.calls != 0 || len(fixture.routes.requests) != 0 || len(*fixture.events) != 0 || fixture.executor.releases.(*fakeReleases).materializeCalls != testCase.wantMaterializations {
				t.Fatalf("side effects: compiler=%d routes=%d events=%d materializations=%d", fixture.compiler.calls, len(fixture.routes.requests), len(*fixture.events), fixture.executor.releases.(*fakeReleases).materializeCalls)
			}
			if fixture.deployments.deployment.Status != deployments.Failed || fixture.deployments.deployment.DiagnosticCode != "reviewed_revision_stale" {
				t.Fatalf("deployment state = %+v", fixture.deployments.deployment)
			}
		})
	}
}

func TestGeneratedExecutorRejectsUnpinnedLatestJobBeforeRuntimeSideEffects(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	job := deploymentJob()
	job.Input, _ = json.Marshal(jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent})
	_, err := fixture.execute(context.Background(), job, fixture.reporter)
	requireExecutionErrorCode(t, err, "reviewed_revisions_required")
	if fixture.executor.releases.(*fakeReleases).materializeCalls != 0 || fixture.compiler.calls != 0 || len(*fixture.events) != 0 || fixture.deployments.deployment.Status != deployments.Failed {
		t.Fatalf("unpinned worker made side effects: release=%d compile=%d events=%v deployment=%+v", fixture.executor.releases.(*fakeReleases).materializeCalls, fixture.compiler.calls, *fixture.events, fixture.deployments.deployment)
	}
}

func TestGeneratedExecutorUsesReviewedPinsOnSuccessfulDeployment(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	prepared, _ := fixture.prepareAuthorization(t)
	canonical := []byte(`{"policyVersion":"compose-runtime-v1","capability":"network","scope":"app"}`)
	fingerprint := sha256.Sum256(canonical)
	finding := deployments.Finding{PolicyVersion: "compose-runtime-v1", Capability: "network", Scope: "app", Fingerprint: hex.EncodeToString(fingerprint[:]), Disposition: "allowed"}
	if err := fixture.repository.Gate(fixture.context, deployments.GateRequest{Claim: fixture.claim, DeploymentID: prepared.ID, Findings: []deployments.Finding{finding}}); err != nil {
		t.Fatal(err)
	}
	expectedFindings := fixture.deployment(t).Findings
	if len(expectedFindings) != 1 || expectedFindings[0].Fingerprint != finding.Fingerprint {
		t.Fatalf("policy gate did not persist the finding: %+v", expectedFindings)
	}
	result, err := fixture.execute()
	if err != nil || result.CompletionCode != "deployment_completed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	deployment := fixture.deployment(t)
	if deployment.Status != deployments.Succeeded || deployment.DeploymentPlanRevisionID != fixture.plan.ID || deployment.DeploymentPlanRevisionNumber != fixture.plan.RevisionNumber ||
		deployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID || deployment.ActualConfigurationRevisionNumber != fixture.configuration.RevisionNumber || fixture.compiler.calls != 1 ||
		fixture.compiler.pins[0].RevisionID != fixture.configuration.RevisionID || fixture.compiler.pins[0].RevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("reviewed deployment=%+v compiler pins=%+v", deployment, fixture.compiler.pins)
	}
	if len(fixture.compiler.requests) != 1 || len(fixture.deployments.finishRequests) != 1 || fixture.runner.createCalls != 1 || fixture.runner.startCalls != 1 || fixture.environment.stageCalls != 1 || fixture.environment.cleanupCalls != 1 {
		t.Fatalf("compiler=%d creates=%d starts=%d stage=%d cleanup=%d", len(fixture.compiler.requests), fixture.runner.createCalls, fixture.runner.startCalls, fixture.environment.stageCalls, fixture.environment.cleanupCalls)
	}
	compileRequest := fixture.compiler.requests[0]
	if !reflect.DeepEqual(compileRequest.Claim, fixture.claim) || compileRequest.DeploymentID != deployment.ID || compileRequest.ReleaseID != fixture.release.ID ||
		compileRequest.ConfigurationRevisionID != fixture.configuration.RevisionID || compileRequest.ConfigurationRevisionNumber != fixture.configuration.RevisionNumber ||
		compileRequest.DeploymentPlanRevisionID != fixture.plan.ID || compileRequest.DeploymentPlanRevisionNumber != fixture.plan.RevisionNumber || compileRequest.ComponentName != "api" || !reflect.DeepEqual(compileRequest.Findings, expectedFindings) {
		t.Fatalf("compiler request=%+v", compileRequest)
	}
	wantFinish := deployments.GeneratedNetworkRequest{
		Claim: fixture.claim, DeploymentID: deployment.ID, ReleaseID: fixture.release.ID,
		ConfigurationRevisionID: fixture.configuration.RevisionID, ConfigurationRevisionNumber: fixture.configuration.RevisionNumber,
		DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber,
		Findings: expectedFindings,
	}
	if actual := fixture.deployments.finishRequests[0]; !reflect.DeepEqual(actual, wantFinish) {
		t.Fatalf("finish request=%+v want=%+v", actual, wantFinish)
	}
	mutated := 0
	for _, source := range fixture.deployments.readFindings {
		if len(source) != 0 {
			source[0].Capability = "mutated source"
			mutated++
		}
	}
	if mutated == 0 || !reflect.DeepEqual(compileRequest.Findings, expectedFindings) || !reflect.DeepEqual(fixture.deployments.finishRequests[0].Findings, expectedFindings) {
		t.Fatal("compiler or finish request aliases the repository findings")
	}
	compileRequest.Findings[0].Capability = "mutated compiler request"
	if !reflect.DeepEqual(fixture.deployments.finishRequests[0].Findings, expectedFindings) || !reflect.DeepEqual(fixture.deployment(t).Findings, expectedFindings) {
		t.Fatal("compiler findings mutation changed finish findings or persisted policy")
	}
	runtimeDeployment := fixture.runtimeDeployment(t)
	if runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded || len(runtimeDeployment.Components) != 1 || runtimeDeployment.Components[0].State != generatedruntimestate.ComponentActive {
		t.Fatalf("runtime deployment=%+v", runtimeDeployment)
	}
}

func TestGeneratedExecutorReceiptlessSucceededRuntimeRemainsClosed(t *testing.T) {
	for _, test := range []struct {
		name           string
		invalidateHead bool
	}{
		{name: "completed deployment"},
		{name: "active head mismatch", invalidateHead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			first, err := fixture.execute()
			if err != nil || first.CompletionCode != "deployment_completed" {
				t.Fatalf("first result=%+v err=%v", first, err)
			}
			if test.invalidateHead {
				fixture.state.activeOverride = &generatedruntimestate.ActiveHead{AppID: fixture.app.ID, DeploymentID: uuid.NewString(), ReleaseID: fixture.release.ID, Slot: string(generatedruntime.SlotBlue), Generation: 2}
			}
			fixture.deployments.transitionErrStatus = deployments.Succeeded
			fixture.deployments.transitionErr = errors.New("generic success transition must remain unused")
			builds, switches, removals := fixture.compiler.calls, len(fixture.routes.requests), fixture.runtime.stopCalls
			creates, starts, processRemovals := fixture.runner.createCalls, fixture.runner.startCalls, fixture.runner.removeCalls
			result, err := fixture.execute()
			deployment := fixture.deployment(t)
			if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired ||
				result.CompletionCode != "" || deployment.Status != deployments.Succeeded {
				t.Fatalf("receiptless replay result=%+v err=%v deployment=%+v", result, err, deployment)
			}
			if fixture.compiler.calls != builds || len(fixture.routes.requests) != switches || fixture.runtime.stopCalls != removals ||
				fixture.runner.createCalls != creates || fixture.runner.startCalls != starts || fixture.runner.removeCalls != processRemovals ||
				generatedExecutorCompletionReceiptCount(t, fixture) != 1 || generatedExecutorCompletionEventCount(t, fixture, fixture.job.ID) != 1 {
				t.Fatal("receiptless replay changed resources or durable completion facts")
			}
		})
	}
}

func TestGeneratedExecutorResumesReviewedLatestJobWithRecordedProvenanceAfterHeadDrift(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.authorization.err = ErrInsufficientReplacementCapacity
	result, err := fixture.execute()
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseInsufficientReplacementCapacity {
		t.Fatalf("first result=%+v err=%v", result, err)
	}
	deployment := fixture.deployment(t)
	if !deployment.ProvenanceInitialized || deployment.ReleaseID != fixture.release.ID || deployment.DeploymentPlanRevisionID != fixture.plan.ID ||
		deployment.DeploymentPlanRevisionNumber != fixture.plan.RevisionNumber || deployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID || deployment.ActualConfigurationRevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("recorded provenance=%+v", deployment)
	}
	if fixture.compiler.calls != 0 || fixture.releases.materializeCalls != 1 {
		t.Fatalf("work before pause: compiler=%d materializations=%d", fixture.compiler.calls, fixture.releases.materializeCalls)
	}

	if _, err := fixture.plans.Replace(context.Background(), fixture.app.ID, fixture.actorID, deploymentplans.ReplaceInput{ExpectedRevisionNumber: fixture.plan.RevisionNumber, Plan: fixture.plan.Plan}); err != nil {
		t.Fatal(err)
	}
	fixture.authorization.err = nil
	result, err = fixture.execute()
	if err != nil || result.CompletionCode != "deployment_completed" {
		t.Fatalf("resume result=%+v err=%v", result, err)
	}
	deployment = fixture.deployment(t)
	if deployment.Status != deployments.Succeeded || deployment.ReleaseID != fixture.release.ID || deployment.DeploymentPlanRevisionID != fixture.plan.ID ||
		deployment.DeploymentPlanRevisionNumber != fixture.plan.RevisionNumber || deployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID || deployment.ActualConfigurationRevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("resumed deployment=%+v", deployment)
	}
	if fixture.releases.materializeCalls != 1 || !reflect.DeepEqual(fixture.releases.readyWorkspaceIDs, []string{fixture.release.ID}) ||
		fixture.compiler.calls != 1 || !reflect.DeepEqual(fixture.compiler.pins, []appconfig.RevisionIdentity{{RevisionID: fixture.configuration.RevisionID, RevisionNumber: fixture.configuration.RevisionNumber}}) || len(fixture.routes.requests) != 1 {
		t.Fatalf("resume sources: materializations=%d ready=%v compiler pins=%+v routes=%d", fixture.releases.materializeCalls, fixture.releases.readyWorkspaceIDs, fixture.compiler.pins, len(fixture.routes.requests))
	}
}

func TestGeneratedExecutorRejectsChangedReviewedProvenanceOnResume(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.authorization.err = ErrInsufficientReplacementCapacity
	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseInsufficientReplacementCapacity || !fixture.deployment(t).ProvenanceInitialized {
		t.Fatalf("first result=%+v err=%v deployment=%+v", result, err, fixture.deployment(t))
	}
	input, _ := json.Marshal(jobs.DeploymentInput{
		ConfigurationMode:      jobs.ConfigurationCurrent,
		ExpectedPlanRevisionID: fixture.plan.ID, ExpectedPlanRevisionNumber: fixture.plan.RevisionNumber,
		ExpectedConfigurationRevisionID: uuid.NewString(), ExpectedConfigurationRevisionNumber: fixture.configuration.RevisionNumber + 1,
	})
	fixture.job.Input = input
	fixture.authorization.err = nil
	_, err = fixture.execute()
	requireExecutionErrorCode(t, err, "invalid_source")
	deployment := fixture.deployment(t)
	if deployment.Status != deployments.Failed || fixture.compiler.calls != 0 || len(fixture.routes.requests) != 0 ||
		fixture.releases.materializeCalls != 1 || len(fixture.releases.readyWorkspaceIDs) != 0 || fixture.authorization.calls != 1 {
		t.Fatalf("mismatched provenance side effects: deployment=%+v compiler=%d routes=%d materializations=%d ready=%v authorization=%d",
			deployment, fixture.compiler.calls, len(fixture.routes.requests), fixture.releases.materializeCalls,
			fixture.releases.readyWorkspaceIDs, fixture.authorization.calls)
	}
}

func requireExecutionErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != code {
		t.Fatalf("execution error=%v want code=%q", err, code)
	}
}

func TestGeneratedExecutorRepositoryFailuresPreserveDurableTerminalState(t *testing.T) {
	tests := []struct {
		name         string
		inject       func(*sealedExecutorFixture)
		wantTerminal bool
		code         string
	}{
		{
			name: "runtime advance",
			inject: func(fixture *sealedExecutorFixture) {
				fixture.state.advanceErrTo = generatedruntimestate.PhaseBuilding
				fixture.state.advanceErr = errors.New("runtime state storage unavailable")
			},
			wantTerminal: true,
			code:         "internal_error",
		},
		{
			name: "deployment reload",
			inject: func(fixture *sealedExecutorFixture) {
				fixture.deployments.getErr = errors.New("deployment storage unavailable")
			},
			wantTerminal: true,
			code:         "internal_error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			test.inject(fixture)
			_, err := fixture.execute()
			requireExecutionErrorCode(t, err, test.code)
			if test.name == "deployment reload" {
				fixture.deployments.getErr = nil
			}
			deployment := fixture.deployment(t)
			runtimeDeployment := fixture.runtimeDeployment(t)
			if test.wantTerminal && (runtimeDeployment.Phase != generatedruntimestate.PhaseFailed || deployment.Status != deployments.Failed || deployment.DiagnosticCode != "internal_error") {
				t.Fatalf("runtime=%+v deployment=%+v", runtimeDeployment, deployment)
			} else if !test.wantTerminal && (runtimeDeployment.Phase == generatedruntimestate.PhaseFailed || deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled) {
				t.Fatalf("storage uncertainty terminalized candidate boundary: runtime=%+v deployment=%+v", runtimeDeployment, deployment)
			}
		})
	}
}

func TestGeneratedExecutorCompletionPrecommitFailureRetainsReceiptsAndCapacity(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.capacity.SetCapacity(128<<20, 128<<20)
	checkCompetitor := fixture.capacityCompetitor(t)
	if _, err := fixture.db.Exec(`CREATE TRIGGER generated_executor_completion_receipt_fault
		BEFORE INSERT ON generated_route_completion_receipts
		BEGIN SELECT RAISE(ABORT, 'generated completion receipt unavailable'); END`); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.execute()
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t)
	var jobStatus string
	var jobAttempt int
	if err := fixture.db.QueryRow(`SELECT status,attempt FROM jobs WHERE id=?`, fixture.job.ID).Scan(&jobStatus, &jobAttempt); err != nil {
		t.Fatal(err)
	}
	if runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		deployment.Status != deployments.WaitingHealth || jobStatus != string(jobs.Running) || jobAttempt != fixture.claim.Attempt() ||
		generatedExecutorActivationReceiptCount(t, fixture) != 1 ||
		generatedExecutorRetirementReceiptCount(t, fixture) != 1 ||
		generatedExecutorAccountingHandoffReceiptCount(t, fixture) != 1 ||
		generatedExecutorCompletionReceiptCount(t, fixture) != 0 ||
		generatedExecutorCompletionEventCount(t, fixture, fixture.job.ID) != 0 ||
		fixture.routes.retirementCalls != 1 || fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
		t.Fatalf("runtime=%+v deployment=%+v job=%s/%d activation=%d retirement=%d accounting=%d completion=%d events=%d calls=%d stops=%d/%d/%d",
			runtimeDeployment, deployment, jobStatus, jobAttempt, generatedExecutorActivationReceiptCount(t, fixture),
			generatedExecutorRetirementReceiptCount(t, fixture), generatedExecutorAccountingHandoffReceiptCount(t, fixture),
			generatedExecutorCompletionReceiptCount(t, fixture), generatedExecutorCompletionEventCount(t, fixture, fixture.job.ID),
			fixture.routes.retirementCalls, fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
	}
	checkCompetitor(true)
}

func TestGeneratedExecutorDistinguishesPlanStorageAndValidationFailures(t *testing.T) {
	t.Run("application repository error", func(t *testing.T) {
		fixture := newExecutorFixture(t, false)
		fixture.executor.applications = &fakeApplications{err: errors.New("application storage unavailable")}
		_, err := fixture.execute(context.Background(), deploymentJob(), fixture.reporter)
		requireExecutionErrorCode(t, err, "internal_error")
	})

	t.Run("repository error", func(t *testing.T) {
		fixture := newExecutorFixture(t, false)
		fixture.executor.plans = &fakePlans{err: errors.New("plan storage unavailable")}
		_, err := fixture.execute(context.Background(), deploymentJob(), fixture.reporter)
		requireExecutionErrorCode(t, err, "internal_error")
	})

	for _, test := range []struct {
		name   string
		mutate func(*deploymentplans.DeploymentPlanRevision)
	}{
		{name: "mismatched provenance", mutate: func(plan *deploymentplans.DeploymentPlanRevision) { plan.AppID = testActorID }},
		{name: "unsupported strategy", mutate: func(plan *deploymentplans.DeploymentPlanRevision) { plan.Plan.Strategy = "unsupported" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, false)
			plan := fixture.plan
			test.mutate(&plan)
			fixture.executor.plans = &fakePlans{revision: plan}
			_, err := fixture.execute(context.Background(), deploymentJob(), fixture.reporter)
			requireExecutionErrorCode(t, err, "invalid_source")
		})
	}
}

func TestGeneratedExecutorOrdersGateBeforeMutationAndCompletesMigrationBlueGreen(t *testing.T) {
	fixture := newSealedExecutorFixtureWithMigration(t)
	result, err := fixture.execute()
	if err != nil {
		t.Fatal(err)
	}
	deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
	if result.CompletionCode != "deployment_completed" || deployment.Status != deployments.Succeeded || runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded || runtimeDeployment.MigrationState != generatedruntimestate.MigrationSucceeded {
		t.Fatalf("result=%+v deployment=%+v runtime=%+v", result, deployment, runtimeDeployment)
	}
	if len(fixture.migrations.requests) != 1 || fixture.runner.createCalls != 2 || fixture.runner.startCalls != 2 || len(fixture.routes.requests) != 1 {
		t.Fatalf("migration runs=%d creates=%d starts=%d routes=%d", len(fixture.migrations.requests), fixture.runner.createCalls, fixture.runner.startCalls, len(fixture.routes.requests))
	}
	identity := fixture.migrations.requests[0].Identity()
	if !reflect.DeepEqual(identity.Claim, fixture.claim) || identity.DeploymentID != deployment.ID || identity.ReleaseID != fixture.release.ID ||
		identity.DeploymentPlanRevisionID != fixture.plan.ID {
		t.Fatalf("migration identity=%+v", identity)
	}
	if endpoints := fixture.routes.requests[0].Endpoints; len(endpoints) != 1 || endpoints[0].Role != generatedruntime.RoleServer {
		t.Fatalf("route endpoints=%+v", endpoints)
	}
}

func TestGeneratedExecutorNetworkRefusalRetainsDurableStateWithoutFailureCleanup(t *testing.T) {
	boundary := generatedNetworkBoundaryTestError()
	for _, test := range []struct {
		name         string
		seed         sealedCandidateSeed
		wantRetained int
	}{
		{name: "building"},
		{name: "starting candidate receipt", seed: sealedSeedReceiptOnly, wantRetained: 1},
		{name: "running candidate", seed: sealedSeedRunning, wantRetained: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			if test.seed != 0 {
				fixture.seedCandidate(t, test.seed)
			}
			fixture.runtime.networkErrs = []error{boundary}
			result, err := fixture.execute()
			var executionErr *jobs.ExecutionError
			if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
			if deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled || runtimeDeployment.Phase == generatedruntimestate.PhaseFailed || runtimeDeployment.Phase == generatedruntimestate.PhaseCancelled {
				t.Fatalf("network refusal terminalized state: deployment=%#v runtime=%#v", deployment, runtimeDeployment)
			}
			if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runtime.retained != test.wantRetained {
				t.Fatalf("refusal cleanup=%d physical stops=%d retention=%d want=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runtime.retained, test.wantRetained)
			}
		})
	}
}

func TestGeneratedExecutorSeedsResumedCandidateRiskBeforeNetworkRefusal(t *testing.T) {
	boundary := generatedNetworkBoundaryTestError()
	for _, test := range []struct {
		name         string
		seed         sealedCandidateSeed
		wantRetained int
	}{
		{name: "all unattempted"},
		{name: "name only receipt", seed: sealedSeedReceiptOnly, wantRetained: 1},
		{name: "configured", seed: sealedSeedCreated, wantRetained: 1},
		{name: "running", seed: sealedSeedRunning, wantRetained: 1},
		{name: "attempt unknown", seed: sealedSeedAttemptUnknown, wantRetained: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			if test.seed != 0 {
				fixture.seedCandidate(t, test.seed)
			}
			fixture.runtime.networkErrs = []error{boundary}
			_, err := fixture.execute()
			if err == nil {
				t.Fatalf("network refusal=%v", err)
			}
			if fixture.runtime.retained != test.wantRetained || fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 {
				t.Fatalf("retention=%d want=%d cleanup=%d physical=%d", fixture.runtime.retained, test.wantRetained, fixture.runtime.stopCalls, fixture.runner.stopCalls)
			}
			if deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t); deployment.Status == deployments.Failed || runtimeDeployment.Phase == generatedruntimestate.PhaseFailed {
				t.Fatalf("resumed network refusal terminalized state: deployment=%+v runtime=%+v", deployment, runtimeDeployment)
			}
		})
	}
}

func TestGeneratedExecutorRetainsWholeAggregateAfterSecondHiddenNetworkRefusal(t *testing.T) {
	api := deploymentplans.Component{Name: "api", Role: generatedruntime.RoleServer, Technology: "node", RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "22", BuildCommand: "npm run build", RunCommand: "node api.js", InternalPort: 3000, HealthProbe: "/health"}
	web := api
	web.Name, web.Role, web.Technology, web.RootDirectory, web.StaticOutputDirectory, web.InternalPort = "web", generatedruntime.RoleStatic, "static", "apps/web", "dist", 3001
	web.RunCommand = projectanalysis.ManagedStaticCommand(web.StaticOutputDirectory, int(web.InternalPort))
	web.InstallDirectory = web.RootDirectory
	fixture := newSealedExecutorFixtureWithComponents(t, []deploymentplans.Component{api, web})
	fixture.capacity.SetCapacity(256<<20, 256<<20)
	fixture.runner.createErrs = []error{nil, runtimeprocess.ErrTerminationFailed}

	result, err := fixture.execute()
	var executionErr *jobs.ExecutionError
	if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || !errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if fixture.runner.createCalls != 2 || fixture.runner.startCalls != 1 {
		t.Fatalf("creates=%d starts=%d", fixture.runner.createCalls, fixture.runner.startCalls)
	}
	deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
	if deployment.Status != deployments.Applying || runtimeDeployment.Phase != generatedruntimestate.PhaseStartingCandidate {
		t.Fatalf("uncertain second create changed lifecycle: deployment=%#v runtime=%#v", deployment, runtimeDeployment)
	}
	if len(runtimeDeployment.Components) != 2 || runtimeDeployment.Components[1].State != generatedruntimestate.ComponentStarting || runtimeDeployment.Components[1].ContainerName == "" {
		t.Fatalf("partial candidate evidence was not retained: %+v", runtimeDeployment.Components)
	}
	var receipts int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`, deployment.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("candidate receipts=%d err=%v", receipts, err)
	}
}

func generatedNetworkBoundaryTestError() error {
	var engine *generatedruntime.Engine
	return engine.EnsureAppNetwork(context.Background(), deployments.GeneratedNetworkRequest{})
}

func TestGeneratedExecutorPassesAcceptedTechnologyToRuntime(t *testing.T) {
	for _, technology := range []string{"", "node", "nextjs"} {
		name := technology
		if name == "" {
			name = "unversioned"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newSealedExecutorFixtureWithTechnology(t, technology)
			result, err := fixture.execute()
			if err != nil || result.CompletionCode != "deployment_completed" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			containers := fixture.runner.containers
			if fixture.runner.createCalls != 1 || fixture.runner.startCalls != 1 {
				t.Fatalf("technology %q: creates=%d starts=%d containers=%+v", technology, fixture.runner.createCalls, fixture.runner.startCalls, containers)
			}
			if technology == "nextjs" {
				for _, container := range containers {
					if _, ok := container.tmpfs["/workspace/.next/cache"]; !ok {
						t.Fatalf("nextjs candidate omitted cache tmpfs: %+v", container.tmpfs)
					}
					break
				}
			}
		})
	}
}

func TestGeneratedExecutorUsesCurrentConfigurationOnSameSourceRelease(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	result, err := fixture.execute()
	if err != nil || result.CompletionCode != "deployment_completed" {
		t.Fatalf("same-source redeploy: result=%+v err=%v", result, err)
	}
	deployment := fixture.deployment(t)
	if deployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID || deployment.ActualConfigurationRevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("deployment did not pin current configuration: %+v", deployment)
	}
	if len(fixture.compiler.pins) != 1 || fixture.compiler.pins[0].RevisionID != fixture.configuration.RevisionID || fixture.compiler.pins[0].RevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("compiler used release configuration rather than selected current revision: %+v", fixture.compiler.pins)
	}
}

func TestGeneratedExecutorRejectsRuntimeExportWithDifferentConfigurationPin(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.configurationView.mismatch = true
	_, err := fixture.execute()
	requireExecutionErrorCode(t, err, "configuration_review_required")
	if fixture.runner.createCalls != 0 {
		t.Fatalf("mismatched runtime configuration reached Docker: creates=%d", fixture.runner.createCalls)
	}
}

func TestGeneratedExecutorWaitsBeforeMutationWhenMigrationApprovalIsMissing(t *testing.T) {
	fixture := newExecutorFixture(t, true)
	fixture.state.beginErr = generatedruntimestate.ErrMigrationApprovalRequired
	result, err := fixture.execute(context.Background(), fixture.deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseMigrationApprovalRequired {
		t.Fatalf("result=%+v", result)
	}
	if fixture.authorization.calls != 0 || fixture.compiler.calls != 0 || len(*fixture.events) != 0 {
		t.Fatalf("unexpected pre-approval work: calls=%d events=%v", fixture.compiler.calls, *fixture.events)
	}
}

func TestGeneratedExecutorWaitsBeforeMutationWhenCapacityAdmissionFails(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	fixture.authorization.err = ErrInsufficientReplacementCapacity
	result, err := fixture.execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseInsufficientReplacementCapacity || fixture.compiler.calls != 0 {
		t.Fatalf("result=%+v compile calls=%d", result, fixture.compiler.calls)
	}
	if !reflect.DeepEqual(*fixture.events, []string{"authorize"}) {
		t.Fatalf("events=%v", *fixture.events)
	}
}

func TestGeneratedExecutorPreservesRuntimePolicyApprovalPause(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	fixture.authorization.err = deployments.ErrApprovalRequired
	result, err := fixture.execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseApprovalRequired || fixture.compiler.calls != 0 {
		t.Fatalf("result=%+v compile calls=%d", result, fixture.compiler.calls)
	}
	if !reflect.DeepEqual(*fixture.events, []string{"authorize"}) {
		t.Fatalf("events=%v", *fixture.events)
	}
}

func TestGeneratedExecutorUsesCompilerOwnedImageAttestation(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	if _, err := fixture.execute(); err != nil {
		t.Fatal(err)
	}
	if fixture.compiler.calls != 1 || fixture.runner.createCalls != 1 {
		t.Fatalf("compile calls=%d create calls=%d", fixture.compiler.calls, fixture.runner.createCalls)
	}
	if component := fixture.runtimeDeployment(t).Components[0]; component.ImageArtifactID != fixture.artifact.ID {
		t.Fatalf("persisted artifact=%s", component.ImageArtifactID)
	}
}

func TestGeneratedExecutorPreservesCompilerImageAuthorityRefusal(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	cause := &generatedimage.CompileError{Code: "image_validation_uncertain"}
	fixture.compiler.errs = []error{cause}
	_, err := fixture.execute()
	if !errors.Is(err, cause) || fixture.compiler.calls != 1 || fixture.runner.createCalls != 0 {
		t.Fatalf("err=%v compile calls=%d create calls=%d", err, fixture.compiler.calls, fixture.runner.createCalls)
	}
	deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
	if deployment.Status != deployments.Applying || runtimeDeployment.Phase != generatedruntimestate.PhaseBuilding || runtimeDeployment.Components[0].State != generatedruntimestate.ComponentPending {
		t.Fatalf("compiler refusal mutated terminal state: deployment=%+v runtime=%+v", deployment, runtimeDeployment)
	}
	if fixture.runtime.retained != 0 || fixture.runtime.stopCalls != 0 {
		t.Fatalf("retained=%d stopped=%d", fixture.runtime.retained, fixture.runtime.stopCalls)
	}
}

func TestGeneratedExecutorRetainsCandidateCapacityOnImageRefusalWithPriorCandidateRisk(t *testing.T) {
	for _, test := range []struct {
		name string
		seed sealedCandidateSeed
	}{
		{name: "receipt only", seed: sealedSeedReceiptOnly},
		{name: "configured", seed: sealedSeedCreated},
		{name: "running", seed: sealedSeedRunning},
		{name: "attempt unknown", seed: sealedSeedAttemptUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			fixture.capacity.SetCapacity(128<<20, 128<<20)
			checkCompetitor := fixture.capacityCompetitor(t)
			fixture.seedCandidate(t, test.seed)
			fixture.runner.mu.Lock()
			delete(fixture.runner.images, fixture.artifact.ImageContentID)
			for _, container := range fixture.runner.containers {
				container.image = "sha256:" + strings.Repeat("0", 64)
			}
			fixture.runner.mu.Unlock()
			_, err := fixture.execute()
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("image refusal error=%v", err)
			}
			deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
			if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || deployment.Status == deployments.Failed || runtimeDeployment.Phase == generatedruntimestate.PhaseFailed {
				t.Fatalf("cleanup=%d physical=%d deployment=%+v runtime=%+v", fixture.runtime.stopCalls, fixture.runner.stopCalls, deployment, runtimeDeployment)
			}
			checkCompetitor(true)
		})
	}
}

func TestGeneratedExecutorPreservesImageFinishAuthorityRefusal(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.deployments.finishErr = deploymentexecution.ErrAuthorityStale

	result, err := fixture.execute()
	var executionErr *jobs.ExecutionError
	if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
	if deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled ||
		runtimeDeployment.Phase != generatedruntimestate.PhaseBuilding || len(runtimeDeployment.Components) != 1 ||
		runtimeDeployment.Components[0].State != generatedruntimestate.ComponentImageReady ||
		runtimeDeployment.Components[0].ImageArtifactID != fixture.artifact.ID {
		t.Fatalf("deployment=%+v runtime=%+v", deployment, runtimeDeployment)
	}
	if fixture.compiler.calls != 1 || fixture.runner.createCalls != 0 || fixture.runner.startCalls != 0 {
		t.Fatalf("compiler=%d creates=%d starts=%d", fixture.compiler.calls, fixture.runner.createCalls, fixture.runner.startCalls)
	}
}

func TestGeneratedExecutorHealthFailurePreservesOldActiveHead(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	assertPreviousUnchanged := fixture.beginReplacement(t)
	fixture.runner.startHealth = "unhealthy"
	_, err := fixture.execute()
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "health_failed" {
		t.Fatalf("err=%v", err)
	}
	assertPreviousUnchanged()
	if len(fixture.routes.requests) != 0 {
		t.Fatalf("route switched before candidate health: routes=%+v", fixture.routes.requests)
	}
	if fixture.runner.stopCalls != 1 || fixture.runner.removeCalls != 1 {
		t.Fatalf("candidate cleanup stop=%d remove=%d", fixture.runner.stopCalls, fixture.runner.removeCalls)
	}
	deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
	if deployment.Status != deployments.Failed || runtimeDeployment.Phase != generatedruntimestate.PhaseFailed {
		t.Fatalf("observed health failure was not recorded: deployment=%+v runtime=%+v", deployment, runtimeDeployment)
	}
}

func TestGeneratedExecutorDoesNotRerunInterruptedOrFailedMigration(t *testing.T) {
	for _, migrationState := range []generatedruntimestate.MigrationState{generatedruntimestate.MigrationRunning, generatedruntimestate.MigrationFailed} {
		t.Run(string(migrationState), func(t *testing.T) {
			fixture := newExecutorFixture(t, true)
			release := fixture.executor.releases.(*fakeReleases).release
			fixture.deployments.deployment = deployments.Deployment{
				ID: fixture.migrationAuthority.deploymentID, AppID: testAppID, JobID: testJobID, Status: deployments.Applying,
				ConfigurationMode: "current", ReleaseID: release.ID, ActualConfigurationRevisionID: release.ConfigurationRevisionID,
				ActualConfigurationRevisionNumber: release.ConfigurationRevisionNumber, RuntimeStrategy: deployments.RuntimeGeneratedNode,
				DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber, ProvenanceInitialized: true,
			}
			fixture.state.deployment = generatedruntimestate.Deployment{
				DeploymentID: fixture.migrationAuthority.deploymentID, AppID: testAppID, ReleaseID: release.ID,
				DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber, CandidateSlot: "blue",
				Phase: generatedruntimestate.PhaseMigrating, MigrationState: migrationState,
				Components: []generatedruntimestate.Component{{DeploymentID: fixture.migrationAuthority.deploymentID, Name: "api", Slot: "blue", ImageArtifactID: fixture.migrationAuthority.artifact.ID, State: generatedruntimestate.ComponentImageReady}},
			}
			_, err := fixture.execute(context.Background(), deploymentJob(), fixture.reporter)
			if err == nil || len(fixture.migrations.requests) != 0 || len(fixture.deployments.migrationRequests) != 0 {
				t.Fatalf("err=%v migration calls=%d pin calls=%d", err, len(fixture.migrations.requests), len(fixture.deployments.migrationRequests))
			}
			if fixture.state.deployment.Phase != generatedruntimestate.PhaseMigrating || fixture.state.deployment.MigrationState != migrationState || fixture.deployments.deployment.Status != deployments.Applying {
				t.Fatalf("migration evidence was reset: runtime=%+v deployment=%+v", fixture.state.deployment, fixture.deployments.deployment)
			}
		})
	}
}

func TestGeneratedExecutorPreservesExistingMigrationWhenRuntimeReadIsUncertain(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{name: "live context"},
		{name: "cancelled context", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, false)
			fixture.deployments.deployment = initializedDeployment(deployments.Applying)
			fixture.state.deployment = generatedruntimestate.Deployment{
				DeploymentID: testDeploymentID, AppID: testAppID, ReleaseID: testReleaseID,
				DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
				CandidateSlot: "blue", Phase: generatedruntimestate.PhaseMigrating, MigrationState: generatedruntimestate.MigrationRunning,
				Components: []generatedruntimestate.Component{{DeploymentID: testDeploymentID, Name: "api", Slot: "blue", ImageArtifactID: testArtifactID, State: generatedruntimestate.ComponentImageReady}},
			}
			resolveCause := errors.New("deployment resolution unavailable")
			readCause := errors.New("runtime state read unavailable")
			fixture.reporter.failPhase = "prepare_workspace"
			fixture.reporter.err = resolveCause
			fixture.state.getErr = readCause
			beforeDeployment := fixture.deployments.deployment
			beforeRuntime := fixture.state.deployment
			var ctx context.Context = context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}

			result, err := fixture.execute(ctx, deploymentJob(), fixture.reporter)
			var executionErr *jobs.ExecutionError
			if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || !errors.Is(err, resolveCause) || !errors.Is(err, readCause) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if test.cancel && (!errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancellation causes missing: %v", err)
			}
			if !reflect.DeepEqual(fixture.deployments.deployment, beforeDeployment) || !reflect.DeepEqual(fixture.state.deployment, beforeRuntime) || fixture.runtime.stopped != 0 || fixture.runtime.retained != 0 {
				t.Fatalf("read uncertainty terminalized or cleaned migration: deployment=%+v runtime=%+v stopped=%d retained=%d", fixture.deployments.deployment, fixture.state.deployment, fixture.runtime.stopped, fixture.runtime.retained)
			}
		})
	}
}

func TestGeneratedExecutorRecoveryAcquiresAndConsumesNewProcessReservation(t *testing.T) {
	fixture := newSealedExecutorFixtureWithTechnology(t, "nextjs")
	fixture.seedCandidate(t, sealedSeedRunning)
	createdBefore, startedBefore, compiledBefore := fixture.runner.createCalls, fixture.runner.startCalls, fixture.compiler.calls
	result, err := fixture.execute()
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletionCode != "deployment_completed" || fixture.compiler.calls != compiledBefore || fixture.runner.createCalls != createdBefore || fixture.runner.startCalls != startedBefore {
		t.Fatalf("result=%+v compiler=%d creates=%d starts=%d", result, fixture.compiler.calls, fixture.runner.createCalls, fixture.runner.startCalls)
	}
	if fixture.authorization.calls != 1 || fixture.runtime.pinCalls != 1 {
		t.Fatalf("authorization=%d candidate pins=%d", fixture.authorization.calls, fixture.runtime.pinCalls)
	}
	if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded || runtimeDeployment.Components[0].State != generatedruntimestate.ComponentActive {
		t.Fatalf("recovered candidate=%+v", runtimeDeployment)
	}
}

func TestGeneratedExecutorCleansCandidateWhenActiveCASRollbackSucceeds(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	if _, err := fixture.execute(); err != nil {
		t.Fatal(err)
	}
	oldDeploymentID := fixture.deployment(t).ID
	oldComponent := fixture.runtimeDeployment(t).Components[0]
	fixture.nextDeployment(t)
	fixture.routes.requests = nil
	fixture.routes.delegateCalls = 0
	reloadsBefore := fixture.capacity.ReloadCalls()
	installGeneratedExecutorActivationHeadFault(t, fixture, "executor rollback succeeds")

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 {
		t.Fatalf("candidate cleanup calls=%d docker stops=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls)
	}
	active, activeErr := fixture.stateStore.Active(context.Background(), fixture.app.ID)
	if activeErr != nil || active.DeploymentID != oldDeploymentID {
		t.Fatalf("active head changed: %+v err=%v", active, activeErr)
	}
	if runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseSwitchingRoute || deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		t.Fatalf("runtime=%+v deployment=%+v", runtimeDeployment, deployment)
	}
	if reloads := fixture.capacity.ReloadCalls() - reloadsBefore; fixture.routes.delegateCalls != 1 || reloads != 4 {
		t.Fatalf("real activation/restore calls=%d reload delta=%d", fixture.routes.delegateCalls, reloads)
	}
	observation, observationErr := fixture.domain.Gateway().Observe(context.Background(), fixture.app.ID)
	if observationErr != nil || len(observation.Endpoints) != 1 || observation.Endpoints[0].ContainerID != oldComponent.ContainerID {
		t.Fatalf("restored predecessor observation=%+v error=%v", observation, observationErr)
	}
	if count := generatedExecutorActivationReceiptCount(t, fixture); count != 1 {
		t.Fatalf("rolled-back activation receipt count=%d", count)
	}
}

func TestGeneratedExecutorPreservesFirstSlotWhenActiveCASFails(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	installGeneratedExecutorActivationHeadFault(t, fixture, "executor first rollback")

	result, err := fixture.execute()
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 {
		t.Fatalf("candidate cleanup calls=%d docker stops=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls)
	}
	active, _ := fixture.stateStore.Active(context.Background(), fixture.app.ID)
	if active.DeploymentID != "" || active.Generation != 0 {
		t.Fatalf("active head changed: %+v", active)
	}
	if runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseSwitchingRoute || deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		t.Fatalf("runtime=%+v deployment=%+v", runtimeDeployment, deployment)
	}
	if len(fixture.routes.requests) != 1 || fixture.routes.requests[0].ToSlot != generatedruntime.SlotBlue || fixture.routes.delegateCalls != 1 {
		t.Fatalf("route requests=%+v", fixture.routes.requests)
	}
	if count := generatedExecutorActivationReceiptCount(t, fixture); count != 0 {
		t.Fatalf("first rolled-back activation receipt count=%d", count)
	}
}

func TestGeneratedExecutorPreservesBothSlotsWhenActiveCASRollbackFails(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	if _, err := fixture.execute(); err != nil {
		t.Fatal(err)
	}
	oldDeploymentID := fixture.deployment(t).ID
	oldContainerID := fixture.runtimeDeployment(t).Components[0].ContainerID
	fixture.nextDeployment(t)
	fixture.routes.requests = nil
	fixture.routes.delegateCalls = 0
	installGeneratedExecutorActivationHeadFault(t, fixture, "executor rollback unavailable")
	fixture.capacity.SetReloadErrors(nil, nil, errors.New("rollback route unavailable"))

	result, err := fixture.execute()
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 {
		t.Fatalf("candidate cleanup calls=%d docker stops=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls)
	}
	active, _ := fixture.stateStore.Active(context.Background(), fixture.app.ID)
	if active.DeploymentID != oldDeploymentID {
		t.Fatalf("active head changed: %+v", active)
	}
	if runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseSwitchingRoute || deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		t.Fatalf("runtime=%+v deployment=%+v", runtimeDeployment, deployment)
	}
	previous, previousErr := fixture.stateStore.Get(context.Background(), fixture.app.ID, oldDeploymentID)
	if previousErr != nil || len(previous.Components) != 1 || previous.Components[0].State != generatedruntimestate.ComponentActive || previous.Components[0].ContainerID != oldContainerID {
		t.Fatalf("previous slot was not preserved: %+v", previous)
	}
	if fixture.routes.delegateCalls != 1 || fixture.capacity.ReloadCalls() != 3 {
		t.Fatalf("real activation/failed restore calls=%d reloads=%d", fixture.routes.delegateCalls, fixture.capacity.ReloadCalls())
	}
	successor := fixture.runtimeDeployment(t).Components[0]
	observation, observationErr := fixture.domain.Gateway().Observe(context.Background(), fixture.app.ID)
	if observationErr != nil || observation.Slot != generatedruntime.SlotGreen || len(observation.Endpoints) != 1 || observation.Endpoints[0].ContainerID != successor.ContainerID {
		t.Fatalf("failed restore observation=%+v error=%v", observation, observationErr)
	}
	if count := generatedExecutorActivationReceiptCount(t, fixture); count != 1 {
		t.Fatalf("ambiguous activation receipt count=%d", count)
	}
}

func TestGeneratedExecutorNeverCleansNewSlotAfterActiveCAS(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")
	result, err := fixture.execute()
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	active, _ := fixture.stateStore.Active(context.Background(), fixture.app.ID)
	if active.DeploymentID != fixture.deployment(t).ID || fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 {
		t.Fatalf("active=%+v candidate cleanup=%d docker stops=%d", active, fixture.runtime.stopCalls, fixture.runner.stopCalls)
	}
	if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("durable phase=%s", runtimeDeployment.Phase)
	}
	if deployment := fixture.deployment(t); deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		t.Fatalf("main deployment became terminal: %+v", deployment)
	}
	if count := generatedExecutorActivationReceiptCount(t, fixture); count != 1 {
		t.Fatalf("durable activation receipt count=%d", count)
	}
}

func installGeneratedExecutorActivationHeadFault(t *testing.T, fixture *sealedExecutorFixture, marker string) {
	t.Helper()
	if _, err := fixture.db.Exec(`CREATE TRIGGER generated_executor_activation_head_fault
		BEFORE UPDATE ON generated_runtime_active_heads BEGIN SELECT RAISE(ABORT, '` + marker + `'); END`); err != nil {
		t.Fatal(err)
	}
}

func generatedExecutorActivationReceiptCount(t *testing.T, fixture *sealedExecutorFixture) int {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func generatedExecutorRetirementReceiptCount(t *testing.T, fixture *sealedExecutorFixture) int {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func generatedExecutorAccountingHandoffReceiptCount(t *testing.T, fixture *sealedExecutorFixture) int {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func generatedExecutorCompletionReceiptCount(t *testing.T, fixture *sealedExecutorFixture) int {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_route_completion_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func generatedExecutorCompletionEventCount(t *testing.T, fixture *sealedExecutorFixture, jobID string) int {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='job_succeeded'`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func requireGeneratedExecutorCompletionAuthority(t *testing.T, fixture *sealedExecutorFixture, deploymentID, jobID string, attempt int) {
	t.Helper()
	var actualJobID string
	var actualAttempt int
	if err := fixture.db.QueryRow(`SELECT current_job_id,current_worker_attempt
		FROM generated_route_completion_receipts WHERE successor_deployment_id=?`, deploymentID).
		Scan(&actualJobID, &actualAttempt); err != nil {
		t.Fatal(err)
	}
	if actualJobID != jobID || actualAttempt != attempt {
		t.Fatalf("completion authority job=%s attempt=%d want=%s/%d", actualJobID, actualAttempt, jobID, attempt)
	}
}

func TestGeneratedExecutorReceiptOwnedCompletion(t *testing.T) {
	t.Run("empty predecessor", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		fixture.capacity.SetCapacity(128<<20, 128<<20)
		checkCompetitor := fixture.capacityCompetitor(t)

		result, err := fixture.execute()
		if err != nil || result.CompletionCode != "deployment_completed" || result.PauseDisposition != "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t)
		if runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded || deployment.Status != deployments.Succeeded ||
			fixture.routes.delegateCalls != 1 || len(fixture.routes.requests) != 1 ||
			generatedExecutorActivationReceiptCount(t, fixture) != 1 || generatedExecutorRetirementReceiptCount(t, fixture) != 1 ||
			generatedExecutorAccountingHandoffReceiptCount(t, fixture) != 1 || generatedExecutorCompletionReceiptCount(t, fixture) != 1 ||
			generatedExecutorCompletionEventCount(t, fixture, fixture.job.ID) != 1 ||
			fixture.routes.retirementCalls != 1 || fixture.routes.retirementErr != nil ||
			fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
			t.Fatalf("runtime=%+v deployment=%+v routes=%d/%d receipts=%d/%d/%d/%d events=%d retirement=%d/%v stops=%d/%d/%d",
				runtimeDeployment, deployment, fixture.routes.delegateCalls, len(fixture.routes.requests),
				generatedExecutorActivationReceiptCount(t, fixture), generatedExecutorRetirementReceiptCount(t, fixture),
				generatedExecutorAccountingHandoffReceiptCount(t, fixture), generatedExecutorCompletionReceiptCount(t, fixture),
				generatedExecutorCompletionEventCount(t, fixture, fixture.job.ID), fixture.routes.retirementCalls,
				fixture.routes.retirementErr, fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
		}
		if fixture.routes.lastActivation.retirement == nil {
			t.Fatal("completed route activation did not retain its closed retirement handle")
		}
		if _, err := fixture.routes.lastActivation.retirement.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("executor return leaked a live retirement handle: %v", err)
		}
		requireGeneratedExecutorCompletionAuthority(t, fixture, deployment.ID, fixture.job.ID, fixture.job.Attempt)
		checkCompetitor(true)
	})

	t.Run("distinct second deployment with full predecessor", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		fixture.capacity.SetCapacity(256<<20, 256<<20)
		checkCompetitor := fixture.capacityCompetitor(t)
		first, err := fixture.execute()
		if err != nil || first.CompletionCode != "deployment_completed" {
			t.Fatalf("first result=%+v err=%v", first, err)
		}
		firstDeploymentID, firstJobID := fixture.deployment(t).ID, fixture.job.ID
		fixture.nextDeployment(t)
		secondJobID := fixture.job.ID

		result, err := fixture.execute()
		if err != nil || result.CompletionCode != "deployment_completed" || result.PauseDisposition != "" {
			t.Fatalf("replacement result=%+v err=%v", result, err)
		}
		previous, getErr := fixture.stateStore.Get(context.Background(), fixture.app.ID, firstDeploymentID)
		if getErr != nil || len(previous.Components) != 1 || previous.Components[0].State != generatedruntimestate.ComponentStopped {
			t.Fatalf("retired predecessor=%+v err=%v", previous, getErr)
		}
		runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t)
		if runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded || deployment.Status != deployments.Succeeded ||
			deployment.ID == firstDeploymentID || secondJobID == firstJobID ||
			generatedExecutorActivationReceiptCount(t, fixture) != 2 || generatedExecutorRetirementReceiptCount(t, fixture) != 2 ||
			generatedExecutorAccountingHandoffReceiptCount(t, fixture) != 2 || generatedExecutorCompletionReceiptCount(t, fixture) != 2 ||
			generatedExecutorCompletionEventCount(t, fixture, firstJobID) != 1 || generatedExecutorCompletionEventCount(t, fixture, secondJobID) != 1 ||
			fixture.routes.retirementCalls != 2 || fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 1 || fixture.runner.removeCalls != 1 {
			t.Fatalf("runtime=%+v deployment=%+v distinct=%s/%s receipts=%d/%d/%d/%d events=%d/%d retirement=%d stops=%d/%d/%d",
				runtimeDeployment, deployment, firstDeploymentID, deployment.ID,
				generatedExecutorActivationReceiptCount(t, fixture), generatedExecutorRetirementReceiptCount(t, fixture),
				generatedExecutorAccountingHandoffReceiptCount(t, fixture), generatedExecutorCompletionReceiptCount(t, fixture),
				generatedExecutorCompletionEventCount(t, fixture, firstJobID), generatedExecutorCompletionEventCount(t, fixture, secondJobID),
				fixture.routes.retirementCalls, fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
		}
		requireGeneratedExecutorCompletionAuthority(t, fixture, deployment.ID, secondJobID, fixture.job.Attempt)
		checkCompetitor(false)
		fixture.capacity.SetCapacity(128<<20, 128<<20)
		checkCompetitor(true)
	})
}

func TestGeneratedExecutorPostActivationProofLossRemainsClosedWithoutRepublishingRoute(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired || len(fixture.routes.requests) != 1 {
		t.Fatalf("first result=%+v err=%v routes=%+v", result, err, fixture.routes.requests)
	}
	if fixture.routes.lastActivation.retirement == nil {
		t.Fatal("post-activation failure did not return the private retirement handle")
	}
	if _, err := fixture.routes.lastActivation.retirement.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("escaped post-activation retirement remained live: %v", err)
	}
	// A scheduler retry has a fresh worker attempt, while the durable route
	// receipt remains bound to the original activation attempt.
	binding, err := fixture.bindings.Read(context.Background(), fixture.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=2,updated_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), fixture.job.ID); err != nil {
		t.Fatal(err)
	}
	fixture.job.Attempt = 2
	fixture.claim, err = deploymentexecution.NewClaim(binding, fixture.job.ID, fixture.app.ID, 2)
	if err != nil {
		t.Fatal(err)
	}

	result, err = fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired || result.CompletionCode != "" {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	if runtimeDeployment := fixture.runtimeDeployment(t); len(fixture.routes.requests) != 1 ||
		fixture.routes.delegateCalls != 1 || fixture.routes.attestCalls != 0 ||
		runtimeDeployment.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("routes=%+v runtime=%+v", fixture.routes.requests, runtimeDeployment)
	}
	var receiptAttempt int
	if err := fixture.db.QueryRow(`SELECT creator_worker_attempt FROM generated_route_activations`).Scan(&receiptAttempt); err != nil || receiptAttempt != 1 {
		t.Fatalf("durable receipt creator attempt=%d err=%v", receiptAttempt, err)
	}
}

func TestGeneratedExecutorRefusesDurablePostActivationWhenPublishedRouteDrifts(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("initial result=%+v err=%v", result, err)
	}
	reloads := fixture.capacity.ReloadCalls()
	drift := filepath.Join(t.TempDir(), "drift.json")
	if err := os.WriteFile(drift, []byte(`{"apps":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, request := range [][]string{
		{"container", "cp", drift, "rig-generated-caddy-v1:/config/drift.json"},
		{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/drift.json"},
	} {
		if _, err := fixture.capacity.Run(context.Background(), runtimeprocess.CommandRequest{Args: request}); err != nil {
			t.Fatal(err)
		}
	}

	result, err = fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	runtimeDeployment := fixture.runtimeDeployment(t)
	if runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		fixture.routes.delegateCalls != 1 || fixture.routes.attestCalls != 1 ||
		fixture.capacity.ReloadCalls() != reloads+1 || fixture.runtime.stopCalls != 0 ||
		fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
		generatedExecutorActivationReceiptCount(t, fixture) != 1 {
		t.Fatalf("runtime=%+v delegate=%d attest=%d reloads=%d/%d stops=%d/%d removals=%d",
			runtimeDeployment, fixture.routes.delegateCalls, fixture.routes.attestCalls,
			fixture.capacity.ReloadCalls(), reloads, fixture.runtime.stopCalls,
			fixture.runner.stopCalls, fixture.runner.removeCalls)
	}
	if deployment := fixture.deployment(t); deployment.Status == deployments.Succeeded {
		t.Fatalf("drifted deployment completed: %+v", deployment)
	}
}

func TestGeneratedExecutorDurablePostActivationRejectsFutureWorkerClaim(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")
	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("initial result=%+v err=%v", result, err)
	}
	binding, err := fixture.bindings.Read(context.Background(), fixture.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.job.Attempt = 2
	fixture.claim, err = deploymentexecution.NewClaim(binding, fixture.job.ID, fixture.app.ID, 2)
	if err != nil {
		t.Fatal(err)
	}

	result, err = fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("future worker claim result=%+v err=%v", result, err)
	}
	if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		fixture.routes.delegateCalls != 1 || fixture.routes.attestCalls != 1 ||
		fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
		generatedExecutorActivationReceiptCount(t, fixture) != 1 {
		t.Fatalf("runtime=%+v delegate=%d attest=%d stops=%d/%d removals=%d receipts=%d",
			runtimeDeployment, fixture.routes.delegateCalls, fixture.routes.attestCalls,
			fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls,
			generatedExecutorActivationReceiptCount(t, fixture))
	}
}

func TestGeneratedExecutorDurablePostActivationRejectsStaleWorkerClaim(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")
	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("initial result=%+v err=%v", result, err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=2,updated_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), fixture.job.ID); err != nil {
		t.Fatal(err)
	}

	result, err = fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("stale worker claim result=%+v err=%v", result, err)
	}
	if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		fixture.routes.delegateCalls != 1 || fixture.routes.attestCalls != 1 ||
		fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
		generatedExecutorActivationReceiptCount(t, fixture) != 1 {
		t.Fatalf("runtime=%+v delegate=%d attest=%d stops=%d/%d removals=%d receipts=%d",
			runtimeDeployment, fixture.routes.delegateCalls, fixture.routes.attestCalls,
			fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls,
			generatedExecutorActivationReceiptCount(t, fixture))
	}
}

func TestGeneratedExecutorDurablePostActivationRejectsCrossedReceiptLineage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sealedExecutorFixture)
	}{
		{name: "zero creator attempt", mutate: func(t *testing.T, fixture *sealedExecutorFixture) {
			if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`UPDATE generated_route_activations SET creator_worker_attempt=0`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "future creator attempt", mutate: func(t *testing.T, fixture *sealedExecutorFixture) {
			if _, err := fixture.db.Exec(`UPDATE generated_route_activations SET creator_worker_attempt=2`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "crossed creator job", mutate: func(t *testing.T, fixture *sealedExecutorFixture) {
			crossedJobID := uuid.NewString()
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,
				requested_by,input_json,created_at,updated_at)
				VALUES(?,'deploy','application',?,'running','running',1,?,'{}',?,?)`,
				crossedJobID, uuid.NewString(), fixture.actorID, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`UPDATE generated_route_activations SET creator_job_id=?`, crossedJobID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")
			result, err := fixture.execute()
			if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
				t.Fatalf("initial result=%+v err=%v", result, err)
			}
			if _, err := fixture.db.Exec(`DROP TRIGGER generated_route_activation_immutable`); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture)

			result, err = fixture.execute()
			if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
				t.Fatalf("crossed lineage result=%+v err=%v", result, err)
			}
			if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
				fixture.routes.delegateCalls != 1 || fixture.routes.attestCalls != 1 ||
				fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
				generatedExecutorActivationReceiptCount(t, fixture) != 1 {
				t.Fatalf("runtime=%+v delegate=%d attest=%d stops=%d/%d removals=%d receipts=%d",
					runtimeDeployment, fixture.routes.delegateCalls, fixture.routes.attestCalls,
					fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls,
					generatedExecutorActivationReceiptCount(t, fixture))
			}
			if deployment := fixture.deployment(t); deployment.Status == deployments.Succeeded {
				t.Fatalf("crossed lineage deployment completed: %+v", deployment)
			}
		})
	}
}

func TestGeneratedExecutorDoesNotRetirePredecessorWhenReceiptOwnedStopRefuses(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.capacity.SetCapacity(128<<20, 128<<20)
	checkCompetitor := fixture.capacityCompetitor(t)
	refusal := errors.New("route-owned predecessor retirement unavailable")
	fixture.routes.retirementBlock = refusal

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	runtimeDeployment := fixture.runtimeDeployment(t)
	if len(fixture.routes.requests) != 1 || fixture.routes.delegateCalls != 1 ||
		fixture.routes.retirementCalls != 1 || !errors.Is(fixture.routes.retirementErr, refusal) ||
		fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
		runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		generatedExecutorRetirementReceiptCount(t, fixture) != 0 {
		t.Fatalf("routes=%+v retirement=%d/%v stopped=%d docker=%d/%d runtime=%+v receipts=%d",
			fixture.routes.requests, fixture.routes.retirementCalls, fixture.routes.retirementErr,
			fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls,
			runtimeDeployment, generatedExecutorRetirementReceiptCount(t, fixture))
	}
	if fixture.routes.lastActivation.retirement == nil {
		t.Fatal("refused executor path lost the receipt-owned retirement handle")
	}
	checkCompetitor(true)
}

func TestGeneratedExecutorReceiptOwnedRetirementOwnsSingleDrainWaitAndCancellation(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.executor.options.DrainPeriod = time.Second
	executorDrainCalls := 0
	fixture.executor.waitDrain = func(context.Context, time.Duration) error {
		executorDrainCalls++
		return errors.New("executor attempted a duplicate drain wait")
	}
	fixture.routes.retirementContext = func(ctx context.Context) context.Context {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	runtimeDeployment := fixture.runtimeDeployment(t)
	if executorDrainCalls != 0 || fixture.routes.retirementCalls != 1 ||
		!errors.Is(fixture.routes.retirementErr, context.Canceled) ||
		fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
		runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		generatedExecutorRetirementReceiptCount(t, fixture) != 0 {
		t.Fatalf("executor waits=%d retirement=%d/%v stopped=%d docker=%d/%d runtime=%+v receipts=%d",
			executorDrainCalls, fixture.routes.retirementCalls, fixture.routes.retirementErr,
			fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls,
			runtimeDeployment, generatedExecutorRetirementReceiptCount(t, fixture))
	}
}

func TestGeneratedExecutorPreservesCandidateWhenFailedRouteMayBeLive(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	assertPreviousUnchanged := fixture.beginReplacement(t)
	fixture.routes.err = candidateMayBeLiveRouteError{}

	result, err := fixture.execute()
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
		t.Fatalf("candidate cleanup calls=%d physical stops=%d removals=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
	}
	assertPreviousUnchanged()
	if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatalf("durable phase=%s", runtimeDeployment.Phase)
	}
	if deployment := fixture.deployment(t); deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		t.Fatalf("main deployment became terminal: %+v", deployment)
	}
}

func TestGeneratedExecutorCleansCandidateAfterRolledBackRouteFailure(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	fixture.routes.err = errors.New("route rolled back")

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 {
		t.Fatalf("candidate cleanup calls=%d physical stops=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls)
	}
	if runtimeDeployment := fixture.runtimeDeployment(t); runtimeDeployment.Phase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatalf("durable phase=%s", runtimeDeployment.Phase)
	}
}

func TestGeneratedExecutorPausesGatewayReadinessFailureAndPreservesOldHead(t *testing.T) {
	fixture := newSealedExecutorFixture(t)
	assertPreviousUnchanged := fixture.beginReplacement(t)
	fixture.routes.err = gatewayReadinessRouteError{}

	result, err := fixture.execute()
	if err != nil || result.PauseDisposition != jobs.PauseRouteReconciliationRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertPreviousUnchanged()
	if fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
		t.Fatalf("candidate cleanup=%d physical stops=%d removals=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
	}
	runtimeDeployment := fixture.runtimeDeployment(t)
	if runtimeDeployment.Phase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatalf("runtime deployment=%+v", runtimeDeployment)
	}
	if deployment := fixture.deployment(t); deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		t.Fatalf("deployment=%+v", deployment)
	}
}

func initializedDeployment(status deployments.Status) deployments.Deployment {
	return deployments.Deployment{
		ID: testDeploymentID, AppID: testAppID, JobID: testJobID, Status: status, ConfigurationMode: "current",
		ReleaseID: testReleaseID, ActualConfigurationRevisionID: testConfigID, ActualConfigurationRevisionNumber: 3,
		RuntimeStrategy: deployments.RuntimeGeneratedNode, DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
		ProvenanceInitialized: true,
	}
}

func configurePreviousActive(t *testing.T, fixture *executorFixture) (string, string) {
	t.Helper()
	const (
		oldDeploymentID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		oldReleaseID    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		oldContainerID  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	description, err := generatedruntime.DescribeInactiveCandidate(testAppID, "api", generatedruntime.SlotBlue)
	if err != nil {
		t.Fatal(err)
	}
	oldArtifact := readyArtifact(testArtifactID2)
	oldArtifact.ReleaseID = oldReleaseID
	fixture.artifacts.values[oldArtifact.ID] = oldArtifact
	fixture.state.active = generatedruntimestate.ActiveHead{
		AppID: testAppID, DeploymentID: oldDeploymentID, ReleaseID: oldReleaseID,
		Slot: string(generatedruntime.SlotGreen), Generation: 4,
	}
	fixture.state.previous = map[string]generatedruntimestate.Deployment{
		oldDeploymentID: {
			DeploymentID: oldDeploymentID, AppID: testAppID, ReleaseID: oldReleaseID,
			DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
			CandidateSlot: string(generatedruntime.SlotGreen), Phase: generatedruntimestate.PhaseSucceeded,
			Components: []generatedruntimestate.Component{{
				DeploymentID: oldDeploymentID, Name: "api", Slot: string(generatedruntime.SlotGreen),
				ImageArtifactID: oldArtifact.ID, ContainerID: oldContainerID,
				ContainerName: description.ContainerName, State: generatedruntimestate.ComponentActive,
			}},
		},
	}
	return oldDeploymentID, oldContainerID
}
