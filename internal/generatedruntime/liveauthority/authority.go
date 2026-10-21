// Package liveauthority prepares real persisted deployment authority for
// direct generated-runtime fixtures. The package is ordinary test support so
// default-tag integration tests can share the same protected fixture history.
package liveauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/hostd/hostd/internal/generatedingress"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

const fixtureCompilerVersion = "liveauthority-fixture-v1"

// Authority owns only same-database stores and repositories. Effects scopes
// are acquired per CandidateSession so sequential fixtures cannot retain a
// process-wide lease through constructor lifetime.
type Authority struct {
	repository    *deployments.Repository
	db            *sql.DB
	jobs          *jobs.Service
	runtime       *generatedruntimestate.Repository
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	working       string
	actorID       string
	machineID     string

	mu    sync.Mutex
	bound map[string]*boundCandidate
}

type boundCandidate struct {
	spec    generatedruntime.CandidateSpec
	claim   deploymentexecution.Claim
	jobID   string
	runtime generatedruntimestate.Deployment
}

// CandidateSession owns one effects scope, one replacement reservation and,
// after Start, the original execution and invocation handles.
type CandidateSession struct {
	authority *Authority
	engine    *generatedruntime.Engine
	ctx       context.Context
	release   func() error
	spec      generatedruntime.CandidateSpec
	jobID     string

	reservation generatedingress.GeneratedReplacementReservation
	execution   *generatedingress.GeneratedCandidateExecution
	invocation  generatedingress.GeneratedCandidateInvocation
	candidate   generatedruntime.Candidate
	closed      sync.Once
}

// New creates private protected stores and composes them with db.
func New(t *testing.T, db *sql.DB, workingDirectory string) *Authority {
	t.Helper()
	plans, err := deploymentplans.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewWithStores(t, db, workingDirectory, plans, configuration)
}

// NewWithStores uses exact caller-provided protected stores. It never acquires
// an effects scope and rejects cross-database composition.
func NewWithStores(t *testing.T, db *sql.DB, workingDirectory string, plans *deploymentplans.Store, configuration *appconfig.Store) *Authority {
	t.Helper()
	if db == nil || plans == nil || !plans.ConfiguredFor(db) || configuration == nil || !configuration.ConfiguredFor(db) {
		t.Fatal("live generated authority requires exact same-database protected stores")
	}
	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	authority := &Authority{
		repository: repository, db: db, jobs: jobService,
		runtime: generatedruntimestate.New(db), plans: plans, configuration: configuration,
		working: workingDirectory, actorID: uuid.NewString(), machineID: uuid.NewString(),
		bound: make(map[string]*boundCandidate),
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, authority.actorID, "direct-live-"+authority.actorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, authority.machineID, "direct-live-"+authority.machineID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return authority
}

// Repository is the concrete guarded repository required by Engine.
func (a *Authority) Repository() *deployments.Repository { return a.repository }

// PrepareCandidate persists a genuine protected single-component plan and
// scoped configuration before image construction. Raw environment bytes are
// consumed and cleared; the returned spec never carries them.
func (a *Authority) PrepareCandidate(t *testing.T, ctx context.Context, spec generatedruntime.CandidateSpec) generatedruntime.CandidateSpec {
	t.Helper()
	if ctx == nil {
		t.Fatal("live candidate preparation requires a context")
	}
	// Preparation always describes a new deployment attempt. Opaque execution
	// handles and the concrete deployment identity are minted only by Bind.
	spec.DeploymentID = ""
	spec.Network = deployments.GeneratedNetworkRequest{}
	spec.Reservation = nil
	if spec.AppID == "" {
		spec.AppID = uuid.NewString()
	}
	if spec.ReleaseID == "" {
		spec.ReleaseID = uuid.NewString()
	}
	if spec.ArtifactID == "" {
		spec.ArtifactID = uuid.NewString()
	}
	if spec.ComponentName == "" {
		t.Fatal("live candidate preparation requires one component")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,'draft',?,?)`, spec.AppID, spec.AppID, spec.AppID, a.machineID, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	desired := liveComponent(t, spec)
	plan, err := a.plans.Get(ctx, spec.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RevisionNumber == 0 || !sameLiveComponentPlan(plan, desired) {
		plan, err = a.plans.Replace(ctx, spec.AppID, a.actorID, deploymentplans.ReplaceInput{
			ExpectedRevisionNumber: plan.RevisionNumber,
			Plan: deploymentplans.Plan{
				SetupVersion: projectanalysis.SetupVersion, Strategy: deploymentplans.StrategyGeneratedNode,
				Detector:   deploymentplans.Detector{Name: "manual-setup", Version: "1", SourceStructuralFingerprint: fixtureDigest("structure:" + spec.AppID)},
				Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: fixtureDigest("source:" + spec.AppID)},
				Components: []deploymentplans.Component{desired}, FieldProvenance: liveComponentProvenance(desired),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	desired = plan.Plan.Components[0]
	spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber = plan.ID, plan.RevisionNumber
	spec.Role, spec.Technology = desired.Role, desired.Technology
	spec.RootDirectory, spec.RunCommand = desired.RootDirectory, desired.RunCommand
	spec.InternalPort, spec.HealthProbe = desired.InternalPort, desired.HealthProbe

	rawEnvironment := append([]byte(nil), spec.Environment...)
	clear(spec.Environment)
	spec.Environment = nil
	spec.EnvironmentOperationID = ""
	spec.EnvironmentOperationAttempt = 0
	defer clear(rawEnvironment)
	entries := liveEnvironmentEntries(t, desired.Name, rawEnvironment)
	expectedExport := []byte("# hostd application configuration\n")
	updatedKeys := make(map[string]bool, len(entries))
	for _, entry := range entries {
		updatedKeys[entry.Key] = true
		expectedExport = append(expectedExport, entry.Key...)
		expectedExport = append(expectedExport, '=')
		expectedExport = append(expectedExport, (*entry.Value)...)
		expectedExport = append(expectedExport, '\n')
	}
	defer clear(expectedExport)

	current, err := a.configuration.Get(ctx, spec.AppID)
	if err != nil {
		t.Fatal(err)
	}
	reuse := current.FormatVersion == 2 && current.DeploymentPlanRevisionID == plan.ID && current.DeploymentPlanRevisionNumber == plan.RevisionNumber
	if reuse {
		exported, exportErr := a.configuration.ExportComponentRuntimeForExecution(ctx, spec.AppID, current.RevisionID, current.RevisionNumber, plan.ID, plan.RevisionNumber, desired.Name)
		if exportErr != nil {
			t.Fatal(exportErr)
		}
		reuse = bytes.Equal(exported.Environment, expectedExport)
		exported.Clear()
	}
	if !reuse {
		remove := make([]appconfig.ScopedKey, 0, len(current.Entries))
		for _, entry := range current.Entries {
			if entry.Phase == appconfig.PhaseRuntime && entry.TargetComponent == desired.Name && !updatedKeys[entry.Key] {
				remove = append(remove, appconfig.ScopedKey{Phase: entry.Phase, Component: entry.TargetComponent, Key: entry.Key})
			}
		}
		current, err = a.configuration.ReplaceScoped(ctx, spec.AppID, a.actorID, appconfig.ScopedReplaceInput{
			ExpectedRevisionNumber: current.RevisionNumber,
			PlanRevisionID:         plan.ID, PlanRevisionNumber: plan.RevisionNumber,
			Components: []appconfig.ComponentTarget{{Name: desired.Name, Role: desired.Role}},
			Entries:    entries, Remove: remove,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber = current.RevisionID, current.RevisionNumber
	return spec
}

// BindCandidate creates real job, claim, release, main-deployment and runtime
// Building history. scopedCtx remains caller-owned.
func (a *Authority) BindCandidate(t *testing.T, scopedCtx context.Context, spec generatedruntime.CandidateSpec) generatedruntime.CandidateSpec {
	t.Helper()
	return a.bind(t, scopedCtx, spec)
}

// BindNetwork creates the same genuine pre-candidate history without requiring
// or fabricating an image artifact.
func (a *Authority) BindNetwork(t *testing.T, scopedCtx context.Context, spec generatedruntime.CandidateSpec) generatedruntime.CandidateSpec {
	t.Helper()
	spec.ArtifactID, spec.ImageContentID, spec.BuildDefinitionDigest = "", "", ""
	return a.bind(t, scopedCtx, spec)
}

func (a *Authority) bind(t *testing.T, scopedCtx context.Context, spec generatedruntime.CandidateSpec) generatedruntime.CandidateSpec {
	t.Helper()
	if scopedCtx == nil {
		t.Fatal("live candidate binding requires a scoped context")
	}
	leave, err := deploymenteffects.EnterScope(scopedCtx, a.working)
	if err != nil {
		t.Fatal("live candidate binding requires the current effects scope:", err)
	}
	defer leave()
	if spec.DeploymentID != "" {
		a.mu.Lock()
		existing := a.bound[spec.DeploymentID]
		a.mu.Unlock()
		if existing == nil || !sameBoundSpec(existing.spec, spec) {
			t.Fatal("direct live deployment identity was reused with different immutable authority")
		}
		return copySpec(existing.spec)
	}
	plan, err := a.plans.GetRevision(scopedCtx, spec.AppID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber)
	if err != nil || !sameLiveComponentPlan(plan, liveComponent(t, spec)) {
		t.Fatalf("load exact live candidate plan: %v", err)
	}
	configuration, err := a.configuration.ExportComponentRuntimeForExecution(scopedCtx, spec.AppID, spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, plan.ID, plan.RevisionNumber, spec.ComponentName)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Clear()
	if err := a.ensureRelease(scopedCtx, spec, plan); err != nil {
		t.Fatal(err)
	}

	created, err := a.jobs.CreateDeploymentWithInputTransaction(scopedCtx, jobs.DeploymentCreateRequest{
		ApplicationID: spec.AppID, IdempotencyKey: "direct-live-" + uuid.NewString(), RequestedBy: a.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create direct live deployment job: created=%t error=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := a.db.ExecContext(scopedCtx, `UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=? AND status='queued'`, stamp, stamp, created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatal("direct live deployment job state changed before binding")
	}
	deployment, made, err := a.repository.GetOrCreateByJob(scopedCtx, spec.AppID, created.Job.ID, "current")
	if err != nil || !made {
		t.Fatalf("create direct live deployment row: created=%t error=%v", made, err)
	}
	deployment, err = a.repository.InitializeRuntime(scopedCtx, spec.AppID, deployment.ID, spec.ReleaseID,
		spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, deployments.RuntimeGeneratedNode,
		spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDeployment, made, err := a.runtime.Begin(scopedCtx, generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: spec.AppID, ReleaseID: spec.ReleaseID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
		ComponentNames: []string{spec.ComponentName},
	})
	if err != nil || !made {
		t.Fatalf("begin direct live generated runtime: created=%t error=%v", made, err)
	}
	runtimeDeployment, err = a.runtime.Advance(scopedCtx, spec.AppID, deployment.ID, generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, spec.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.DeploymentID = deployment.ID
	spec.ActiveSlot = generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot)
	spec.Network = deployments.GeneratedNetworkRequest{
		Claim: claim, DeploymentID: deployment.ID, ReleaseID: spec.ReleaseID,
		ConfigurationRevisionID: spec.ConfigurationRevisionID, ConfigurationRevisionNumber: spec.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
	}
	a.mu.Lock()
	a.bound[deployment.ID] = &boundCandidate{spec: spec, claim: claim, jobID: created.Job.ID, runtime: runtimeDeployment}
	a.mu.Unlock()
	return spec
}

// Begin acquires one session effects scope, binds genuine Building history and
// reserves replacement capacity before any Ready artifact is selected.
func (a *Authority) Begin(t *testing.T, parent context.Context, engine *generatedruntime.Engine, spec generatedruntime.CandidateSpec) *CandidateSession {
	t.Helper()
	if parent == nil || engine == nil {
		t.Fatal("live candidate session requires a context and runtime engine")
	}
	scope, release, err := deploymenteffects.AcquireScoped(parent, a.working)
	if err != nil {
		t.Fatal(err)
	}
	ctx := deploymenteffects.WithScope(parent, scope)
	var releaseOnce sync.Once
	var releaseErr error
	releaseAndReport := func() error {
		releaseOnce.Do(func() {
			releaseErr = release()
			if releaseErr != nil {
				t.Errorf("release direct live generated authority: %v", releaseErr)
			}
		})
		return releaseErr
	}
	// This cleanup is installed before BindCandidate, whose helper failures use
	// Goexit and still must drop the just-acquired OS lease.
	t.Cleanup(func() { _ = releaseAndReport() })
	session := &CandidateSession{authority: a, engine: engine, ctx: ctx, release: releaseAndReport}
	bound := a.BindCandidate(t, ctx, spec)
	request := generatedingress.GeneratedReplacementRequest{
		Claim: bound.Network.Claim, DeploymentID: bound.DeploymentID, ReleaseID: bound.ReleaseID,
		ConfigurationRevisionID: bound.ConfigurationRevisionID, ConfigurationRevisionNumber: bound.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: bound.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: bound.DeploymentPlanRevisionNumber,
		CandidateSlot: string(inactiveSlot(t, bound.ActiveSlot)), Findings: append([]deployments.Finding(nil), bound.Network.Findings...),
	}
	reservation, err := engine.ReserveReplacement(ctx, request)
	if err != nil {
		session.Close()
		t.Fatal(err)
	}
	bound.Reservation = reservation
	session.spec, session.jobID, session.reservation = bound, bound.Network.Claim.JobID(), reservation
	t.Cleanup(session.Close)
	return session
}

func (s *CandidateSession) BoundSpec() generatedruntime.CandidateSpec { return copySpec(s.spec) }
func (s *CandidateSession) Candidate() generatedruntime.Candidate     { return s.candidate }

// Start validates the fixture-built image, records its exact Ready artifact,
// then crosses the real gate and candidate owner boundaries.
func (s *CandidateSession) Start(t *testing.T, ready generatedruntime.CandidateSpec) (generatedruntime.Candidate, error) {
	t.Helper()
	if s == nil || s.execution != nil || s.reservation == nil || !sameReadySpec(s.spec, ready) {
		return generatedruntime.Candidate{}, errors.New("live candidate ready identity changed")
	}
	if err := s.engine.ValidateImage(s.ctx, generatedruntime.ImageSpec{
		AppID: ready.AppID, ReleaseID: ready.ReleaseID, ArtifactID: ready.ArtifactID,
		DeploymentPlanRevisionID: ready.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: ready.DeploymentPlanRevisionNumber,
		ConfigurationRevisionID: ready.ConfigurationRevisionID, ConfigurationRevisionNumber: ready.ConfigurationRevisionNumber,
		ComponentName: ready.ComponentName, Role: ready.Role,
		ImageContentID: ready.ImageContentID, BuildDefinitionDigest: ready.BuildDefinitionDigest,
	}); err != nil {
		return generatedruntime.Candidate{}, err
	}
	if err := s.authority.recordFixtureArtifact(s.ctx, ready); err != nil {
		return generatedruntime.Candidate{}, err
	}
	if _, err := s.authority.runtime.SetImageReady(s.ctx, ready.AppID, ready.DeploymentID, ready.ComponentName, ready.ArtifactID); err != nil {
		return generatedruntime.Candidate{}, err
	}
	if err := s.authority.repository.Gate(s.ctx, deployments.GateRequest{Claim: ready.Network.Claim, DeploymentID: ready.DeploymentID, Findings: ready.Network.Findings}); err != nil {
		return generatedruntime.Candidate{}, err
	}
	if _, err := s.authority.runtime.Advance(s.ctx, ready.AppID, ready.DeploymentID, generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
		return generatedruntime.Candidate{}, err
	}
	execution, err := s.engine.PinCandidates(s.ctx, generatedingress.GeneratedCandidateRequest{
		Claim: ready.Network.Claim, DeploymentID: ready.DeploymentID, ReleaseID: ready.ReleaseID,
		ConfigurationRevisionID: ready.ConfigurationRevisionID, ConfigurationRevisionNumber: ready.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: ready.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: ready.DeploymentPlanRevisionNumber,
		CandidateSlot: string(inactiveSlot(t, ready.ActiveSlot)), Findings: append([]deployments.Finding(nil), ready.Network.Findings...),
	}, s.reservation)
	if err != nil {
		return generatedruntime.Candidate{}, err
	}
	s.execution = execution
	if err := s.engine.EnsureAppNetwork(s.ctx, ready.Network); err != nil {
		return generatedruntime.Candidate{}, err
	}
	invocation, err := execution.BeginCreate(ready.ComponentName)
	if err != nil {
		return generatedruntime.Candidate{}, err
	}
	s.invocation = invocation
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		return generatedruntime.Candidate{}, err
	}
	s.candidate = candidateFromIdentity(ready, identity)
	if err := execution.StartCandidate(invocation); err != nil {
		return s.candidate, err
	}
	if _, err := execution.FinishStarting(); err != nil {
		return s.candidate, err
	}
	s.spec = copySpec(ready)
	s.spec.Reservation = s.reservation
	return s.candidate, nil
}

// WaitHealthyAndHandoff records the exact physical health observation and
// closes cleanup before handing the complete roster to route.
func (s *CandidateSession) WaitHealthyAndHandoff() error {
	if s == nil || s.execution == nil || s.invocation == nil {
		return errors.New("live candidate has not started")
	}
	if err := s.execution.WaitHealthyAndRecord(s.invocation); err != nil {
		return err
	}
	_, err := s.execution.FinishHealth()
	return err
}

// RejectObservedFailure cleans only the original invocation and asks the owner
// to terminalize from its private determinate observation proof.
func (s *CandidateSession) RejectObservedFailure() error {
	if s == nil || s.execution == nil || s.invocation == nil {
		return errors.New("live candidate has no original invocation")
	}
	if err := s.execution.RemoveInactive(s.invocation); err != nil {
		return err
	}
	failed, err := s.execution.RecordObservedFailure()
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.authority.db.ExecContext(s.ctx, `UPDATE jobs SET status='failed',phase='failed',error_code=?,updated_at=?,finished_at=? WHERE id=? AND status='running'`, failed.DiagnosticCode, stamp, stamp, s.jobID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("live failed job state changed")
	}
	return nil
}

// RoutePublished accepts only the exact observed slot and complete endpoint
// roster derived afresh through the candidate owner.
func (s *CandidateSession) RoutePublished(slot generatedruntime.Slot, endpoints []generatedruntime.RouteEndpoint) error {
	if s == nil || s.execution == nil {
		return errors.New("live candidate has no route authority")
	}
	identity, err := s.execution.ObserveRouteCandidate(s.spec.ComponentName)
	if err != nil {
		return err
	}
	expected := []generatedruntime.RouteEndpoint{endpointFromIdentity(identity)}
	if slot != generatedruntime.Slot(identity.Slot) || !reflect.DeepEqual(canonicalEndpoints(endpoints), canonicalEndpoints(expected)) {
		return errors.New("published route does not match the exact candidate roster")
	}
	current, err := s.authority.runtime.Get(s.ctx, s.spec.AppID, s.spec.DeploymentID)
	if err != nil {
		return err
	}
	hadPredecessor := current.PreviousActiveDeploymentID != ""
	active, err := s.authority.runtime.Active(s.ctx, s.spec.AppID)
	if err != nil {
		return err
	}
	published, switched, err := s.authority.runtime.SwitchActive(s.ctx, s.spec.AppID, s.spec.DeploymentID, active.Generation)
	if err != nil || (!switched && published.DeploymentID != s.spec.DeploymentID) {
		return errors.Join(err, errors.New("live route did not switch exact active head"))
	}
	if _, err := s.authority.runtime.Advance(s.ctx, s.spec.AppID, s.spec.DeploymentID, generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, ""); err != nil {
		return err
	}
	if hadPredecessor {
		return nil
	}
	if _, err := s.authority.runtime.Advance(s.ctx, s.spec.AppID, s.spec.DeploymentID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		return err
	}
	if _, err := s.authority.repository.Transition(s.ctx, s.spec.AppID, s.spec.DeploymentID, deployments.Succeeded, ""); err != nil {
		return err
	}
	return s.finishJob("succeeded", "")
}

// Close joins execution and cleanup children before releasing accounting and
// finally the per-session effects scope.
func (s *CandidateSession) Close() {
	if s == nil {
		return
	}
	s.closed.Do(func() {
		if s.execution != nil {
			s.execution.Leave()
		}
		if s.reservation != nil {
			s.reservation.Release()
		}
		if s.release != nil {
			_ = s.release()
		}
	})
}

// CancelNetwork terminalizes a network-only fixture before any candidate
// intent or physical candidate state exists.
func (a *Authority) CancelNetwork(t *testing.T, ctx context.Context, deploymentID string) {
	t.Helper()
	leave, err := deploymenteffects.EnterScope(ctx, a.working)
	if err != nil {
		t.Fatal("live network cancellation requires the current effects scope:", err)
	}
	defer leave()
	a.mu.Lock()
	bound := a.bound[deploymentID]
	a.mu.Unlock()
	if bound == nil {
		t.Fatal("live network deployment is not bound")
	}
	var intentCount, changedComponentCount int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`, deploymentID).Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND (state<>'pending' OR image_artifact_id IS NOT NULL OR container_name IS NOT NULL OR container_id IS NOT NULL)`, deploymentID).Scan(&changedComponentCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 0 || changedComponentCount != 0 {
		t.Fatal("network-only cancellation found candidate authority or physical state")
	}
	if _, err := a.runtime.Advance(ctx, bound.spec.AppID, deploymentID, generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseCancelled, generatedruntimestate.DiagnosticCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := a.repository.Transition(ctx, bound.spec.AppID, deploymentID, deployments.Cancelled, "cancelled"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := a.db.ExecContext(ctx, `UPDATE jobs SET status='cancelled',phase='cancelled',error_code='cancelled',updated_at=?,finished_at=? WHERE id=? AND status='running'`, stamp, stamp, bound.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatal("network-only fixture job state changed")
	}
}

func (a *Authority) ensureRelease(ctx context.Context, spec generatedruntime.CandidateSpec, plan deploymentplans.DeploymentPlanRevision) error {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,?,?,?, 'ready',?,?,?,?,?)`, spec.ReleaseID, spec.AppID, stamp, plan.Plan.Source.Provider, plan.Plan.Source.RepositoryID, plan.Plan.Source.ResolvedDigest, fixtureDigest("workspace:"+spec.ReleaseID), spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, plan.ID, plan.RevisionNumber)
	if err != nil {
		return err
	}
	var appID, configurationID, planID, state, sourceProvider, sourceDigest string
	var configurationNumber, planNumber, sourceRepositoryID int64
	if err := a.db.QueryRowContext(ctx, `SELECT app_id,workspace_state,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number,source_provider,repository_id,resolved_sha FROM releases WHERE id=?`, spec.ReleaseID).Scan(&appID, &state, &configurationID, &configurationNumber, &planID, &planNumber, &sourceProvider, &sourceRepositoryID, &sourceDigest); err != nil {
		return err
	}
	if appID != spec.AppID || state != "ready" || planID != plan.ID || planNumber != plan.RevisionNumber ||
		sourceProvider != plan.Plan.Source.Provider || sourceRepositoryID != plan.Plan.Source.RepositoryID || sourceDigest != plan.Plan.Source.ResolvedDigest {
		return errors.New("live release identity changed")
	}
	// The selected runtime configuration can advance after release creation.
	// Re-read the release-original protected revision only to prove that its own
	// immutable identity is still real and bound to the accepted plan.
	original, err := a.configuration.ExportComponentRuntimeForExecution(ctx, spec.AppID, configurationID, configurationNumber, plan.ID, plan.RevisionNumber, spec.ComponentName)
	if err != nil {
		return err
	}
	original.Clear()
	return nil
}

func (a *Authority) recordFixtureArtifact(ctx context.Context, spec generatedruntime.CandidateSpec) error {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,'ready',?,?,?)`, spec.ArtifactID, spec.ReleaseID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber, spec.ComponentName, fixtureCompilerVersion, spec.BuildDefinitionDigest, 1, spec.ImageContentID, stamp, stamp, stamp)
	if err != nil {
		return err
	}
	artifact, err := generatedimagestate.NewRepository(a.db).Get(ctx, spec.ArtifactID)
	if err != nil {
		return err
	}
	if artifact.ReleaseID != spec.ReleaseID || artifact.DeploymentPlanRevisionID != spec.DeploymentPlanRevisionID || artifact.DeploymentPlanRevisionNumber != spec.DeploymentPlanRevisionNumber || artifact.ComponentID != spec.ComponentName || artifact.CompilerVersion != fixtureCompilerVersion || artifact.BuildDefinitionDigest != spec.BuildDefinitionDigest || artifact.AttemptNumber != 1 || artifact.ImageContentID != spec.ImageContentID || artifact.State != generatedimagestate.ArtifactReady {
		return errors.New("live fixture artifact identity changed")
	}
	return nil
}

func (s *CandidateSession) finishJob(status, code string) error {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.authority.db.ExecContext(s.ctx, `UPDATE jobs SET status=?,phase=?,progress_percent=100,error_code=?,updated_at=?,finished_at=? WHERE id=? AND status='running'`, status, status, nullable(code), stamp, stamp, s.jobID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("live job state changed")
	}
	return nil
}

func liveComponent(t *testing.T, spec generatedruntime.CandidateSpec) deploymentplans.Component {
	t.Helper()
	technology := spec.Technology
	if technology == "" {
		technology = "node"
	}
	component := deploymentplans.Component{
		Name: spec.ComponentName, Role: spec.Role, Technology: technology,
		RootDirectory: spec.RootDirectory, PackageManager: "npm", InstallBehavior: "npm ci",
		InstallDirectory: spec.RootDirectory, NodeVersion: "22", BuildCommand: "npm run build",
		RunCommand: spec.RunCommand, InternalPort: spec.InternalPort, HealthProbe: spec.HealthProbe,
	}
	// The direct Next cache fixture builds its reviewed image on Node 24;
	// the plain Node fixtures use Node 22. Keep protected metadata truthful.
	if technology == "nextjs" {
		component.NodeVersion = "24"
	}
	if technology == "static" {
		component.StaticOutputDirectory = "dist"
		if component.RunCommand != projectanalysis.ManagedStaticCommand(component.StaticOutputDirectory, int(component.InternalPort)) {
			t.Fatal("static live candidate must use the managed dist command")
		}
	}
	return component
}

func sameLiveComponentPlan(plan deploymentplans.DeploymentPlanRevision, component deploymentplans.Component) bool {
	if plan.State != deploymentplans.RevisionAccepted || plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode ||
		plan.Plan.Migration != nil || len(plan.Plan.Components) != 1 {
		return false
	}
	actual := plan.Plan.Components[0]
	return actual.Name == component.Name && actual.Role == component.Role && actual.Technology == component.Technology &&
		actual.RootDirectory == component.RootDirectory && actual.RunCommand == component.RunCommand &&
		actual.InternalPort == component.InternalPort && actual.HealthProbe == component.HealthProbe
}

func liveComponentProvenance(component deploymentplans.Component) []deploymentplans.FieldProvenance {
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe", "technology", "staticOutputDirectory"}
	result := make([]deploymentplans.FieldProvenance, 0, len(fields))
	for _, field := range fields {
		result = append(result, deploymentplans.FieldProvenance{Field: "components." + component.Name + "." + field, Origin: deploymentplans.ProvenanceUser, Confidence: 100, Evidence: []string{"live-fixture"}})
	}
	return result
}

func liveEnvironmentEntries(t *testing.T, component string, raw []byte) []appconfig.ScopedValueInput {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	entries := make([]appconfig.ScopedValueInput, 0, len(lines))
	seen := make(map[string]bool)
	for _, line := range lines {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || seen[key] {
			t.Fatal("live candidate environment must contain unique KEY=VALUE lines")
		}
		seen[key] = true
		copied := value
		entries = append(entries, appconfig.ScopedValueInput{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: component, Key: key},
			Sensitivity: appconfig.SensitivitySecret, Value: &copied,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

func sameReadySpec(bound, ready generatedruntime.CandidateSpec) bool {
	want, got := copySpec(bound), copySpec(ready)
	want.ImageContentID, want.BuildDefinitionDigest = got.ImageContentID, got.BuildDefinitionDigest
	want.Reservation, got.Reservation = nil, nil
	want.Environment, got.Environment = nil, nil
	return got.ImageContentID != "" && got.BuildDefinitionDigest != "" && reflect.DeepEqual(want, got)
}

func sameBoundSpec(first, second generatedruntime.CandidateSpec) bool {
	want, got := copySpec(first), copySpec(second)
	want.Reservation, got.Reservation = nil, nil
	return reflect.DeepEqual(want, got)
}

func copySpec(spec generatedruntime.CandidateSpec) generatedruntime.CandidateSpec {
	spec.Environment = nil
	spec.Network.Findings = append([]deployments.Finding(nil), spec.Network.Findings...)
	return spec
}

func inactiveSlot(t *testing.T, active generatedruntime.Slot) generatedruntime.Slot {
	t.Helper()
	slot, err := generatedruntime.InactiveSlot(active)
	if err != nil {
		t.Fatal(err)
	}
	return slot
}

func candidateFromIdentity(spec generatedruntime.CandidateSpec, identity generatedingress.CandidateIdentity) generatedruntime.Candidate {
	working := "/workspace"
	if spec.RootDirectory != "." {
		working += "/" + spec.RootDirectory
	}
	return generatedruntime.Candidate{
		AppID: spec.AppID, ReleaseID: identity.ReleaseID, DeploymentID: identity.DeploymentID,
		ArtifactID: identity.ArtifactID, DeploymentPlanRevisionID: identity.DeploymentPlanRevisionID,
		Component: identity.ComponentName, Role: identity.Role, Technology: identity.Technology,
		Slot: generatedruntime.Slot(identity.Slot), ContainerID: identity.ContainerID, ContainerName: identity.ContainerName,
		NetworkName: identity.NetworkName, NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort,
		ImageContentID: spec.ImageContentID, WorkingDirectory: working, RunCommandDigest: fixtureDigest(spec.RunCommand),
	}
}

func endpointFromIdentity(identity generatedingress.CandidateIdentity) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{Component: identity.ComponentName, Role: identity.Role, ContainerID: identity.ContainerID, NetworkName: identity.NetworkName, NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort}
}

func canonicalEndpoints(values []generatedruntime.RouteEndpoint) []generatedruntime.RouteEndpoint {
	result := append([]generatedruntime.RouteEndpoint(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		first := fmt.Sprintf("%s\x00%s\x00%s", result[i].Component, result[i].Role, result[i].ContainerID)
		last := fmt.Sprintf("%s\x00%s\x00%s", result[j].Component, result[j].Role, result[j].ContainerID)
		return first < last
	})
	return result
}

func fixtureDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
