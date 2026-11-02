//go:build linux || windows

package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedColdBoundaryExecutor func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error)

func (e generatedColdBoundaryExecutor) Execute(ctx context.Context, job jobs.Job, progress jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if err := progress.Report(jobs.ProgressUpdate{Status: jobs.Running, Phase: "validate", Progress: 1, Code: "phase_started"}); err != nil {
		return jobs.ExecutionResult{}, err
	}
	return e(ctx, job, progress)
}

// generatedColdCreatedCandidateStartRunner preserves one immutable process
// dependency across construction and recovery. Construction sees only the
// established read/census runner. A repaired cold manifest may later enable
// only explicitly frozen candidate argv for the revalidated continuation.
type generatedColdCreatedCandidateStartRunner struct {
	mu          sync.RWMutex
	delegate    runtimeprocess.CommandRunner
	candidate   runtimeprocess.CommandRunner
	containerID string
	allowed     map[string]struct{}
}

func (r *generatedColdCreatedCandidateStartRunner) enableStart() {
	r.allow([]string{"container", "start", r.containerID})
}

func (r *generatedColdCreatedCandidateStartRunner) allow(args []string) {
	r.mu.Lock()
	if r.allowed == nil {
		r.allowed = make(map[string]struct{})
	}
	r.allowed[strings.Join(args, "\x00")] = struct{}{}
	r.mu.Unlock()
}

func (r *generatedColdCreatedCandidateStartRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.RLock()
	_, allowed := r.allowed[strings.Join(request.Args, "\x00")]
	candidate, delegate := r.candidate, r.delegate
	r.mu.RUnlock()
	if allowed && candidate != nil {
		return candidate.Run(ctx, request)
	}
	return delegate.Run(ctx, request)
}

type generatedColdMigrationJourneyPlan struct {
	plan          deploymentplans.Plan
	revision      deploymentplans.DeploymentPlanRevision
	configuration appconfig.Configuration
	releaseID     string
	artifactID    string
	imageID       string
	buildDigest   string
}

type generatedColdMigrationJourneyHarness struct {
	identity      generatedDomainIdentityFixture
	repository    *candidateTestRepository
	bindings      *deploymentbinding.Repository
	jobs          *jobs.Service
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	guarded       *candidateTestGuarded
	state         *generatedruntimestate.Repository
}

type generatedColdMigrationJourneyRoute struct {
	migration   *generatedMigrationFixture
	identity    CandidateIdentity
	head        generatedruntimestate.ActiveHead
	retirement  *GeneratedPredecessorRetirement
	reservation GeneratedReplacementReservation
	execution   *GeneratedCandidateExecution
}

func newGeneratedColdMigrationJourneyHarness(t *testing.T) generatedColdMigrationJourneyHarness {
	t.Helper()
	identity := newGeneratedDomainIdentityFixture(t)
	database := identity.database
	lifecycle := applifecycle.New(database)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(database, validator)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewWithDeploymentBindings(database, bindings)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(database, bindings, positive, identity.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(database, identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(database, identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(database, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	identity.stores = stores
	now := time.Now().UTC().Format(time.RFC3339Nano)
	actorID, appID, machineID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := database.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at)
		VALUES(?,?,?,?,?)`, actorID, "migration-cold-"+actorID, "hash", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at)
		VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machineID, "local", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at)
		VALUES(?,?,?,?, 'draft',?,?)`, appID, appID, appID, machineID, now, now); err != nil {
		t.Fatal(err)
	}
	return generatedColdMigrationJourneyHarness{identity: identity,
		repository: &candidateTestRepository{Repository: repository, db: database, stores: stores},
		bindings:   bindings, jobs: jobService, plans: plans, configuration: configuration,
		guarded: &candidateTestGuarded{appID: appID, actorID: actorID}, state: generatedruntimestate.New(database)}
}

func (h generatedColdMigrationJourneyHarness) newPlan(t *testing.T, migration bool,
	resolvedDigest string,
) generatedColdMigrationJourneyPlan {
	t.Helper()
	if len(resolvedDigest) != 64 {
		t.Fatal("migration journey source digest is not canonical")
	}
	current, err := h.plans.Get(context.Background(), h.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	component := deploymentplans.Component{Name: "api", Role: "server", RootDirectory: "apps/api",
		PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: "apps/api",
		NodeVersion: "24", RunCommand: "npm start", InternalPort: 3000, HealthProbe: "/health"}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory",
		"nodeVersion", "runCommand", "internalPort", "healthProbe"}
	provenance := make([]deploymentplans.FieldProvenance, 0, len(fields))
	for _, field := range fields {
		provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components.api." + field,
			Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
	}
	plan := deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode,
		Detector: deploymentplans.Detector{Name: "package-json", Version: "1",
			SourceStructuralFingerprint: resolvedDigest},
		Source:     deploymentplans.SourceIdentity{Provider: "github", RepositoryID: 7, ResolvedDigest: resolvedDigest},
		Components: []deploymentplans.Component{component}, FieldProvenance: provenance}
	if migration {
		plan.Migration = &deploymentplans.Migration{ComponentName: "api", RootDirectory: "apps/api",
			Command: "npm exec -- knex migrate:latest", EnvironmentKeys: []string{"DATABASE_URL"},
			EvidenceDigest: resolvedDigest,
			Approval:       deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}}
	}
	revision, err := h.plans.Replace(context.Background(), h.guarded.appID, h.guarded.actorID,
		deploymentplans.ReplaceInput{ExpectedRevisionNumber: current.RevisionNumber, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	configurationHead, err := h.configuration.Get(context.Background(), h.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	secret := "migration-cold-secret-" + revision.ID
	configuration, err := h.configuration.ReplaceScoped(context.Background(), h.guarded.appID, h.guarded.actorID,
		appconfig.ScopedReplaceInput{ExpectedRevisionNumber: configurationHead.RevisionNumber,
			PlanRevisionID: revision.ID, PlanRevisionNumber: revision.RevisionNumber,
			Components: []appconfig.ComponentTarget{{Name: "api", Role: "server"}},
			Entries: []appconfig.ScopedValueInput{{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseRuntime,
				Component: "api", Key: "DATABASE_URL"}, Sensitivity: appconfig.SensitivitySecret, Value: &secret}}})
	if err != nil {
		t.Fatal(err)
	}
	releaseID, artifactID := uuid.NewString(), uuid.NewString()
	imageDigit := fmt.Sprintf("%x", revision.RevisionNumber+5)
	if len(imageDigit) != 1 {
		t.Fatal("migration journey image identity exceeded fixture range")
	}
	imageID, buildDigest := "sha256:"+strings.Repeat(imageDigit, 64), strings.Repeat(imageDigit, 64)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.identity.database.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,
		deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,'ready','{}',?,'github',7,?,'ready',?,?,?,?,?)`, releaseID, h.guarded.appID, now,
		resolvedDigest, strings.Repeat(imageDigit, 64), configuration.RevisionID,
		configuration.RevisionNumber, revision.ID, revision.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	if _, err := h.identity.database.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at)
		VALUES(?,?,?,?,?,'test-compiler',?,1,?,'ready',?,?,?)`, artifactID, releaseID, revision.ID,
		revision.RevisionNumber, "api", buildDigest, imageID, now, now, now); err != nil {
		t.Fatal(err)
	}
	return generatedColdMigrationJourneyPlan{plan: revision.Plan, revision: revision, configuration: configuration,
		releaseID: releaseID, artifactID: artifactID, imageID: imageID, buildDigest: buildDigest}
}

func (h generatedColdMigrationJourneyHarness) newDeployment(t *testing.T, plan generatedColdMigrationJourneyPlan,
	migration, finishMigration bool, configurationMode string,
) *generatedMigrationFixture {
	t.Helper()
	mode := jobs.ConfigurationCurrent
	input := jobs.DeploymentInput{ConfigurationMode: mode, ExpectedPlanRevisionID: plan.revision.ID,
		ExpectedPlanRevisionNumber:          plan.revision.RevisionNumber,
		ExpectedConfigurationRevisionID:     plan.configuration.RevisionID,
		ExpectedConfigurationRevisionNumber: plan.configuration.RevisionNumber}
	if configurationMode == "original" {
		mode = jobs.ConfigurationOriginal
		input = jobs.DeploymentInput{ReleaseID: plan.releaseID, ConfigurationMode: mode}
	}
	created, err := h.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: h.guarded.appID, IdempotencyKey: "migration-cold-" + uuid.NewString(),
		RequestedBy: h.guarded.actorID, Input: input}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create migration journey deployment: created=%t error=%v", created.Created, err)
	}
	deployment, _, err := h.repository.GetOrCreateByJob(context.Background(), h.guarded.appID,
		created.Job.ID, string(mode))
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = h.repository.InitializeRuntime(context.Background(), h.guarded.appID, deployment.ID,
		plan.releaseID, plan.configuration.RevisionID, plan.configuration.RevisionNumber,
		deployments.RuntimeGeneratedNode, plan.revision.ID, plan.revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	attempt := 1
	if migration {
		runGeneratedColdBoundaryWorker(t, h.jobs,
			generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
					PauseDisposition: jobs.PauseMigrationApprovalRequired}, nil
			}), created.Job.ID, jobs.WaitingUser, h.identity.options.WorkingDirectory, nil)
		if err := h.plans.ApproveMigration(context.Background(), h.guarded.appID, plan.revision.ID,
			plan.revision.RevisionNumber, 0, h.guarded.actorID); err != nil {
			t.Fatal(err)
		}
		resumed, err := h.jobs.Resume(created.Job.ID)
		if err != nil || resumed.Status != string(jobs.Queued) || resumed.Attempt != 1 {
			t.Fatalf("resume approved migration job=%#v error=%v", resumed, err)
		}
		attempt = 2
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := h.identity.database.Exec(`UPDATE jobs SET status='running',phase='running',attempt=?,
		started_at=?,updated_at=? WHERE id=? AND status='queued'`, attempt, now, now, created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("claim migration journey job rows=%d error=%v", changed, err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), h.identity.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	releaseEffects := func() error {
		if released {
			return nil
		}
		released = true
		return release()
	}
	t.Cleanup(func() {
		if err := releaseEffects(); err != nil {
			t.Errorf("release migration journey effects: %v", err)
		}
	})
	guardedContext := deploymenteffects.WithScope(context.Background(), scope)
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, h.guarded.appID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repository.Gate(guardedContext, deployments.GateRequest{Claim: claim,
		DeploymentID: deployment.ID}); err != nil {
		t.Fatal(err)
	}
	runtimeDeployment, wasCreated, err := h.state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: h.guarded.appID, ReleaseID: plan.releaseID,
		DeploymentPlanRevisionID: plan.revision.ID, DeploymentPlanRevisionNumber: plan.revision.RevisionNumber,
		ComponentNames: []string{"api"}})
	if err != nil || !wasCreated {
		t.Fatalf("begin migration journey runtime: created=%t error=%v", wasCreated, err)
	}
	runtimeDeployment, err = h.state.Advance(context.Background(), h.guarded.appID, deployment.ID,
		runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.state.SetImageReady(context.Background(), h.guarded.appID, deployment.ID, "api",
		plan.artifactID); err != nil {
		t.Fatal(err)
	}
	next := generatedruntimestate.PhaseStartingCandidate
	if migration {
		next = generatedruntimestate.PhaseMigrating
	}
	if _, err := h.state.Advance(context.Background(), h.guarded.appID, deployment.ID,
		runtimeDeployment.Phase, next, ""); err != nil {
		t.Fatal(err)
	}
	if migration {
		if _, err := h.state.BeginMigration(context.Background(), h.guarded.appID, deployment.ID); err != nil {
			t.Fatal(err)
		}
		if finishMigration {
			if _, err := h.state.FinishMigration(context.Background(), h.guarded.appID, deployment.ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err := h.state.Advance(context.Background(), h.guarded.appID, deployment.ID,
				generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &generatedMigrationFixture{repository: h.repository, bindings: h.bindings, jobService: h.jobs,
		plans: h.plans, configuration: h.configuration,
		guarded:        &candidateTestGuarded{context: guardedContext, appID: h.guarded.appID, actorID: h.guarded.actorID},
		releaseEffects: releaseEffects, plan: plan.plan, claim: claim, binding: created.Binding,
		request: candidateMigrationRequestFixture{DeploymentID: deployment.ID, ReleaseID: plan.releaseID,
			ConfigurationRevisionID:      plan.configuration.RevisionID,
			ConfigurationRevisionNumber:  plan.configuration.RevisionNumber,
			DeploymentPlanRevisionID:     plan.revision.ID,
			DeploymentPlanRevisionNumber: plan.revision.RevisionNumber,
			ArtifactID:                   plan.artifactID, ArtifactAttemptNumber: 1}}
}

func generatedColdMigrationJourneyValidation(t *testing.T,
	migration *generatedMigrationFixture,
) *candidateValidationFixture {
	t.Helper()
	if migration == nil {
		t.Fatal("migration journey candidate validation is incomplete")
	}
	state := generatedruntimestate.New(migration.repository.db)
	runtimeDeployment, err := state.Get(context.Background(), migration.guarded.appID,
		migration.request.DeploymentID)
	if err != nil || runtimeDeployment.Phase != generatedruntimestate.PhaseStartingCandidate {
		t.Fatalf("migration journey candidate phase=%q error=%v", runtimeDeployment.Phase, err)
	}
	request := GeneratedCandidateRequest{Claim: migration.claim,
		DeploymentID: migration.request.DeploymentID, ReleaseID: migration.request.ReleaseID,
		ConfigurationRevisionID:      migration.request.ConfigurationRevisionID,
		ConfigurationRevisionNumber:  migration.request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID:     migration.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
		CandidateSlot:                runtimeDeployment.CandidateSlot}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	loader := &GeneratedCandidateAuthority{stores: migration.repository.stores}
	protected, err := loader.loadCandidateProtectedSnapshot(context.Background(), pins)
	if err != nil {
		t.Fatal(err)
	}
	return &candidateValidationFixture{migration: migration, authority: loader, state: state,
		request: request, pins: pins, protected: protected}
}

func newGeneratedColdMigrationJourneyOwner(t *testing.T, identity generatedDomainIdentityFixture,
	migration *generatedMigrationFixture,
) (*candidateOwnerFixture, *generatedRouteCandidateRunner) {
	t.Helper()
	validation := generatedColdMigrationJourneyValidation(t, migration)
	options := generatedDomainCandidateOptions(identity.options)
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: 4 * uint64(options.MemoryBytes),
		DiskAvailableBytes:   4 * options.ReplacementDiskBytes}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	base := &candidateOwnerRunner{containerID: strings.Repeat("7", 64),
		absenceErr: generatedMigrationCompletedExitOne(t),
		before:     make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results:    make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
	runner := &generatedRouteCandidateRunner{delegate: base, physical: make(map[string]candidateOwnerPhysical)}
	environment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := migration.repository.GeneratedCandidateAuthority(runner, environment, budget, options)
	if err != nil {
		t.Fatal(err)
	}
	validation.authority = authority
	return &candidateOwnerFixture{validation: validation, authority: authority, runner: base,
		environment: environment, source: source, budget: budget, options: options}, runner
}

func completeGeneratedColdMigrationJourneyRoute(t *testing.T, owner *candidateOwnerFixture,
	candidates *generatedRouteCandidateRunner, routes *GeneratedRouteAuthority, manager *Manager,
	physicalRoutes *generatedRoutePredecessorRunner, migration *generatedMigrationFixture,
	containerID string,
) generatedColdMigrationJourneyRoute {
	t.Helper()
	if owner == nil || candidates == nil || routes == nil || manager == nil || physicalRoutes == nil ||
		migration == nil || !validContainerID(containerID) {
		t.Fatal("migration journey route dependencies are incomplete")
	}
	owner.validation = generatedColdMigrationJourneyValidation(t, migration)
	owner.runner.containerID = containerID
	reservation := owner.reserve(t)
	t.Cleanup(reservation.Release)
	execution := owner.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := owner.beginCreate(t, execution)
	owner.bindInvocation(t, invocation)
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	owner.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	request := generatedRouteRequest(migration.guarded.appID, identity)
	if observation, observeErr := manager.Observe(context.Background(), migration.guarded.appID); observeErr == nil {
		request.FromSlot = observation.Slot
	}
	endpoint := generatedRouteEndpoint(identity)
	physicalRoutes.endpoints[identity.ContainerID] = generatedRouteEndpointSnapshot{
		appID: migration.guarded.appID, endpoint: endpoint, slot: generatedidentity.Slot(identity.Slot)}
	physicalRoutes.network = identity.NetworkName
	physicalRoutes.endpoint = endpoint
	physicalRoutes.slot = generatedidentity.Slot(identity.Slot)
	sealed, err := routes.Pin(migration.guarded.context, execution, request)
	if err != nil {
		t.Fatal(err)
	}
	_, retirement, err := sealed.ActivateWithPredecessorRetirement(migration.guarded.context)
	if err != nil || retirement == nil {
		t.Fatalf("activate migration journey route retirement=%#v error=%v", retirement, err)
	}
	t.Cleanup(retirement.Leave)
	if err := retirement.Complete(migration.guarded.context); err != nil {
		t.Fatal(err)
	}
	head, err := generatedruntimestate.New(migration.repository.db).Active(context.Background(),
		migration.guarded.appID)
	if err != nil || head.DeploymentID != migration.request.DeploymentID ||
		head.ReleaseID != migration.request.ReleaseID || head.ActivationReceiptID == "" {
		t.Fatalf("migration journey active head=%#v error=%v", head, err)
	}
	return generatedColdMigrationJourneyRoute{migration: migration, identity: identity, head: head,
		retirement: retirement, reservation: reservation, execution: execution}
}

func (r *generatedColdMigrationJourneyRoute) release(t *testing.T) {
	t.Helper()
	if r == nil || r.migration == nil {
		t.Fatal("migration journey route release is incomplete")
	}
	if r.retirement != nil {
		r.retirement.Leave()
	}
	if r.execution != nil {
		r.execution.Leave()
	}
	if r.reservation != nil {
		r.reservation.Release()
	}
	if err := r.migration.releaseEffects(); err != nil {
		t.Fatal(err)
	}
}

func assertGeneratedColdMigrationJourneyRevisionPins(t *testing.T, database *sql.DB, store *appconfig.Store,
	planStore *deploymentplans.Store, plans []generatedColdMigrationJourneyPlan,
) {
	t.Helper()
	for _, plan := range plans {
		identity, err := store.ExactRevisionIdentity(context.Background(), plan.revision.AppID,
			plan.configuration.RevisionID, plan.configuration.RevisionNumber)
		if err != nil || identity.RevisionID != plan.configuration.RevisionID ||
			identity.RevisionNumber != plan.configuration.RevisionNumber || identity.FormatVersion != 2 ||
			identity.DeploymentPlanRevisionID != plan.revision.ID ||
			identity.DeploymentPlanRevisionNumber != plan.revision.RevisionNumber {
			t.Fatalf("migration journey configuration identity=%#v error=%v", identity, err)
		}
		var scopedCount int
		var phase, component, key, sensitivity string
		if err := database.QueryRow(`SELECT COUNT(*),COALESCE(MIN(phase),''),
			COALESCE(MIN(target_component),''),COALESCE(MIN(key),''),COALESCE(MIN(sensitivity),'')
			FROM application_configuration_scoped_entries WHERE revision_id=?`,
			plan.configuration.RevisionID).Scan(&scopedCount, &phase, &component, &key, &sensitivity); err != nil ||
			scopedCount != 1 || phase != string(appconfig.PhaseRuntime) || component != "api" ||
			key != "DATABASE_URL" || sensitivity != string(appconfig.SensitivitySecret) {
			t.Fatalf("migration journey secret-reference metadata unavailable for configuration revision %d",
				plan.configuration.RevisionNumber)
		}
		loaded, err := planStore.GetRevision(context.Background(), plan.revision.AppID,
			plan.revision.ID, plan.revision.RevisionNumber)
		migrationParity := (loaded.Plan.Migration == nil) == (plan.plan.Migration == nil)
		approved := loaded.Plan.Migration == nil ||
			loaded.Plan.Migration.Approval.Status == deploymentplans.MigrationApprovalApproved
		loadedPlan := loaded.Plan
		if loadedPlan.Migration != nil {
			migration := *loadedPlan.Migration
			migration.Approval = deploymentplans.MigrationApproval{}
			if plan.plan.Migration != nil {
				migration.Approval = plan.plan.Migration.Approval
			}
			loadedPlan.Migration = &migration
		}
		if err != nil || loaded.ID != plan.revision.ID || loaded.RevisionNumber != plan.revision.RevisionNumber ||
			loaded.CanonicalDigest != plan.revision.CanonicalDigest || !migrationParity || !approved ||
			!reflect.DeepEqual(loadedPlan, plan.plan) {
			t.Fatalf("migration journey durable approval unavailable for plan revision %d",
				plan.revision.RevisionNumber)
		}
		var releasePlanID, releaseConfigurationID, resolvedDigest string
		var releasePlanNumber, releaseConfigurationNumber int64
		if err := database.QueryRow(`SELECT deployment_plan_revision_id,
			deployment_plan_revision_number,configuration_revision_id,configuration_revision_number,resolved_sha
			FROM releases WHERE id=?`, plan.releaseID).Scan(&releasePlanID, &releasePlanNumber,
			&releaseConfigurationID, &releaseConfigurationNumber, &resolvedDigest); err != nil ||
			releasePlanID != plan.revision.ID || releasePlanNumber != plan.revision.RevisionNumber ||
			releaseConfigurationID != plan.configuration.RevisionID ||
			releaseConfigurationNumber != plan.configuration.RevisionNumber ||
			resolvedDigest != plan.plan.Source.ResolvedDigest {
			t.Fatalf("migration journey release revision pins disagree at plan revision %d",
				plan.revision.RevisionNumber)
		}
		var artifactReleaseID, artifactPlanID, artifactComponent, compilerVersion,
			definitionDigest, imageContentID, artifactState string
		var artifactPlanNumber, artifactAttempt int64
		if err := database.QueryRow(`SELECT release_id,deployment_plan_revision_id,
			deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,
			attempt_number,image_content_id,state FROM generated_image_artifacts WHERE id=?`,
			plan.artifactID).Scan(&artifactReleaseID, &artifactPlanID, &artifactPlanNumber,
			&artifactComponent, &compilerVersion, &definitionDigest, &artifactAttempt,
			&imageContentID, &artifactState); err != nil || artifactReleaseID != plan.releaseID ||
			artifactPlanID != plan.revision.ID || artifactPlanNumber != plan.revision.RevisionNumber ||
			artifactComponent != "api" || compilerVersion != "test-compiler" ||
			definitionDigest != plan.buildDigest || artifactAttempt != 1 || imageContentID != plan.imageID ||
			artifactState != "ready" {
			t.Fatalf("migration journey artifact pins disagree at plan revision %d",
				plan.revision.RevisionNumber)
		}
	}
}

func assertGeneratedColdMigrationJourneySnapshot(t *testing.T,
	snapshot deployments.GeneratedDomainStartupSnapshot, appID string,
	v1, v2, rollback, v3 *generatedMigrationFixture,
	v1Plan, v2Plan, v3Plan generatedColdMigrationJourneyPlan,
	v1Route, v2Route, rollbackRoute generatedColdMigrationJourneyRoute,
) {
	t.Helper()
	if len(snapshot.GeneratedDeployments) != 4 || len(snapshot.Runtime.Deployments) != 4 ||
		len(snapshot.Runtime.Components) != 4 || len(snapshot.Runtime.CandidateIntents) != 3 ||
		len(snapshot.Artifacts) != 3 ||
		len(snapshot.Runtime.RouteActivations) != 3 || len(snapshot.Runtime.ActiveHeads) != 1 ||
		snapshot.LifecycleOperationCount != 0 || len(snapshot.LifecycleOperations) != 0 ||
		len(snapshot.UnresolvedLifecycleApps) != 0 || len(snapshot.LifecycleBindings) != 4 {
		t.Fatalf("migration journey startup census main=%d runtime=%d components=%d intents=%d artifacts=%d activations=%d heads=%d lifecycle=%d/%d bindings=%d unresolved=%d",
			len(snapshot.GeneratedDeployments), len(snapshot.Runtime.Deployments), len(snapshot.Runtime.Components),
			len(snapshot.Runtime.CandidateIntents), len(snapshot.Artifacts), len(snapshot.Runtime.RouteActivations),
			len(snapshot.Runtime.ActiveHeads), snapshot.LifecycleOperationCount, len(snapshot.LifecycleOperations),
			len(snapshot.LifecycleBindings), len(snapshot.UnresolvedLifecycleApps))
	}
	expectedPlans := map[string]generatedColdMigrationJourneyPlan{
		v1.request.DeploymentID: v1Plan, v2.request.DeploymentID: v2Plan,
		rollback.request.DeploymentID: v1Plan, v3.request.DeploymentID: v3Plan}
	expectedAttempts := map[string]int{v1.request.DeploymentID: 1, v2.request.DeploymentID: 2,
		rollback.request.DeploymentID: 1, v3.request.DeploymentID: 2}
	for _, value := range snapshot.GeneratedDeployments {
		plan, ok := expectedPlans[value.ID]
		if !ok || value.AppID != appID || value.ReleaseID != plan.releaseID ||
			value.DeploymentPlanRevisionID != plan.revision.ID ||
			value.DeploymentPlanRevisionNumber != plan.revision.RevisionNumber ||
			value.ActualConfigurationRevisionID != plan.configuration.RevisionID ||
			value.ActualConfigurationRevisionNumber != plan.configuration.RevisionNumber ||
			value.JobAttempt != expectedAttempts[value.ID] || !value.ProvenanceInitialized {
			t.Fatalf("migration journey main deployment identity mismatch at revision %d", plan.revision.RevisionNumber)
		}
		if value.ID == v3.request.DeploymentID {
			if value.Status != deployments.Applying || value.JobStatus != string(jobs.Running) {
				t.Fatalf("interrupted migration main state status=%s job=%s", value.Status, value.JobStatus)
			}
		} else if value.Status != deployments.Succeeded || value.JobStatus != string(jobs.Succeeded) ||
			value.CanonicalSuccessEventCount != 1 || value.SuccessEventCount != 1 {
			t.Fatalf("completed migration journey main state status=%s job=%s canonical=%d success=%d",
				value.Status, value.JobStatus, value.CanonicalSuccessEventCount, value.SuccessEventCount)
		}
		delete(expectedPlans, value.ID)
	}
	if len(expectedPlans) != 0 {
		t.Fatal("migration journey omitted a pinned deployment")
	}
	runtimeByID := make(map[string]generatedruntimestate.GeneratedDomainStartupDeployment, 4)
	for _, value := range snapshot.Runtime.Deployments {
		runtimeByID[value.DeploymentID] = value
	}
	for _, value := range []struct {
		migration *generatedMigrationFixture
		plan      generatedColdMigrationJourneyPlan
		phase     generatedruntimestate.Phase
		state     generatedruntimestate.MigrationState
	}{
		{v1, v1Plan, generatedruntimestate.PhaseSucceeded, generatedruntimestate.MigrationNotRequired},
		{v2, v2Plan, generatedruntimestate.PhaseSucceeded, generatedruntimestate.MigrationSucceeded},
		{rollback, v1Plan, generatedruntimestate.PhaseSucceeded, generatedruntimestate.MigrationNotRequired},
		{v3, v3Plan, generatedruntimestate.PhaseMigrating, generatedruntimestate.MigrationRunning},
	} {
		runtime, ok := runtimeByID[value.migration.request.DeploymentID]
		if !ok || runtime.AppID != appID || runtime.ReleaseID != value.plan.releaseID ||
			runtime.DeploymentPlanRevisionID != value.plan.revision.ID ||
			runtime.DeploymentPlanRevisionNumber != value.plan.revision.RevisionNumber ||
			runtime.Phase != value.phase || runtime.MigrationState != value.state {
			t.Fatalf("migration journey runtime identity mismatch for plan revision %d", value.plan.revision.RevisionNumber)
		}
	}
	v3Runtime := runtimeByID[v3.request.DeploymentID]
	if v3Runtime.PreviousActiveDeploymentID != rollback.request.DeploymentID ||
		v3Runtime.PreviousActiveSlot != rollbackRoute.head.Slot {
		t.Fatalf("interrupted migration predecessor=%s/%s want=%s/%s",
			v3Runtime.PreviousActiveDeploymentID, v3Runtime.PreviousActiveSlot,
			rollback.request.DeploymentID, rollbackRoute.head.Slot)
	}
	componentByDeployment := make(map[string]generatedruntimestate.GeneratedDomainStartupComponent, 4)
	for _, component := range snapshot.Runtime.Components {
		componentByDeployment[component.DeploymentID] = component
	}
	if componentByDeployment[v1.request.DeploymentID].State != generatedruntimestate.ComponentStopped ||
		componentByDeployment[v2.request.DeploymentID].State != generatedruntimestate.ComponentStopped ||
		componentByDeployment[rollback.request.DeploymentID].State != generatedruntimestate.ComponentActive ||
		componentByDeployment[v3.request.DeploymentID].State != generatedruntimestate.ComponentImageReady ||
		componentByDeployment[v3.request.DeploymentID].ContainerID != "" ||
		componentByDeployment[v3.request.DeploymentID].ContainerName != "" {
		t.Fatal("migration journey component states do not preserve the historical/current/no-effect boundary")
	}
	intentCount := make(map[string]int)
	for _, intent := range snapshot.Runtime.CandidateIntents {
		intentCount[intent.DeploymentID]++
		plan, exists := map[string]generatedColdMigrationJourneyPlan{
			v1.request.DeploymentID: v1Plan, v2.request.DeploymentID: v2Plan,
			rollback.request.DeploymentID: v1Plan}[intent.DeploymentID]
		if !exists || intent.ComponentName != "api" ||
			intent.ConfigurationRevisionID != plan.configuration.RevisionID ||
			intent.ConfigurationRevisionNumber != plan.configuration.RevisionNumber ||
			intent.ImageArtifactID != plan.artifactID || intent.ImageArtifactAttempt != 1 ||
			intent.ReceiptID == "" {
			t.Fatal("migration journey candidate receipt lost exact configuration/artifact identity")
		}
	}
	if intentCount[v1.request.DeploymentID] != 1 || intentCount[v2.request.DeploymentID] != 1 ||
		intentCount[rollback.request.DeploymentID] != 1 || intentCount[v3.request.DeploymentID] != 0 {
		t.Fatalf("migration journey candidate intent counts=%#v", intentCount)
	}
	artifactIDs := make(map[string]bool, 3)
	for _, artifact := range snapshot.Artifacts {
		artifactIDs[artifact.ID] = true
	}
	if !artifactIDs[v1Plan.artifactID] || !artifactIDs[v2Plan.artifactID] || !artifactIDs[v3Plan.artifactID] {
		t.Fatal("migration journey artifact census omitted an exact release artifact")
	}
	activationByDeployment := make(map[string]generatedruntimestate.GeneratedDomainStartupRouteActivation, 3)
	for _, activation := range snapshot.Runtime.RouteActivations {
		activationByDeployment[activation.SuccessorDeploymentID] = activation
	}
	first, second, third := activationByDeployment[v1.request.DeploymentID],
		activationByDeployment[v2.request.DeploymentID], activationByDeployment[rollback.request.DeploymentID]
	if first.ReceiptID != v1Route.head.ActivationReceiptID || first.PredecessorDeploymentID != "" ||
		first.SuccessorGeneration != 1 ||
		second.ReceiptID != v2Route.head.ActivationReceiptID ||
		second.PredecessorDeploymentID != v1.request.DeploymentID ||
		second.PredecessorActivationReceiptID != v1Route.head.ActivationReceiptID ||
		second.PredecessorReleaseID != v1Plan.releaseID || second.PredecessorGeneration != 1 ||
		second.SuccessorGeneration != 2 ||
		third.ReceiptID != rollbackRoute.head.ActivationReceiptID ||
		third.PredecessorDeploymentID != v2.request.DeploymentID ||
		third.PredecessorActivationReceiptID != v2Route.head.ActivationReceiptID ||
		third.PredecessorReleaseID != v2Plan.releaseID || third.PredecessorGeneration != 2 ||
		third.SuccessorGeneration != 3 {
		t.Fatal("migration journey activation lineage is not the exact completed sequence")
	}
	head := snapshot.Runtime.ActiveHeads[0]
	if head.AppID != appID || head.DeploymentID != rollback.request.DeploymentID ||
		head.ReleaseID != v1Plan.releaseID || head.ActivationReceiptID != rollbackRoute.head.ActivationReceiptID ||
		head.Generation != rollbackRoute.head.Generation || head.Slot != rollbackRoute.head.Slot {
		t.Fatalf("migration journey active head=%#v", head)
	}
	if v1.request.DeploymentID == rollback.request.DeploymentID ||
		v1Route.identity.ContainerID == rollbackRoute.identity.ContainerID ||
		v1Route.identity.ReceiptID == rollbackRoute.identity.ReceiptID ||
		v1Route.identity.Slot != rollbackRoute.identity.Slot {
		t.Fatal("historical v1 redeploy did not retain immutable release with distinct runtime identity")
	}
	for _, activation := range snapshot.Runtime.RouteActivations {
		if activation.SuccessorDeploymentID == v3.request.DeploymentID {
			t.Fatal("interrupted migration unexpectedly created an activation receipt")
		}
	}
	if len(snapshot.Runtime.PredecessorRetirementComponents) != 2 ||
		len(snapshot.Runtime.PredecessorRetirementReceipts) != 3 ||
		len(snapshot.Runtime.AccountingHandoffAuthorizations) != 3 ||
		len(snapshot.Runtime.AccountingHandoffReceipts) != 3 ||
		len(snapshot.Runtime.RouteCompletionReceipts) != 3 {
		t.Fatalf("migration journey route history counts retire=%d/%d handoff=%d/%d complete=%d",
			len(snapshot.Runtime.PredecessorRetirementComponents),
			len(snapshot.Runtime.PredecessorRetirementReceipts),
			len(snapshot.Runtime.AccountingHandoffAuthorizations),
			len(snapshot.Runtime.AccountingHandoffReceipts), len(snapshot.Runtime.RouteCompletionReceipts))
	}
	boundJobs := make(map[string]bool, 4)
	for _, binding := range snapshot.LifecycleBindings {
		if binding.AppID != appID || binding.DesiredRevision != 0 ||
			binding.Mode != deploymentbinding.ModeLegacyUninitialized || binding.AuthorizingOperationID != "" {
			t.Fatalf("migration journey lifecycle binding=%#v", binding)
		}
		boundJobs[binding.DeploymentJobID] = true
	}
	for _, migration := range []*generatedMigrationFixture{v1, v2, rollback, v3} {
		if !boundJobs[migration.claim.JobID()] {
			t.Fatal("migration journey omitted an actual deployment lifecycle binding")
		}
	}
}

func assertGeneratedColdMigrationJourneyPhysical(t *testing.T, runner *generatedRouteCandidateRunner,
	v1, v2, rollback CandidateIdentity, interruptedDeploymentID string,
) {
	t.Helper()
	if runner == nil {
		t.Fatal("migration journey physical runner is unavailable")
	}
	if len(runner.physical) != 3 {
		t.Fatalf("migration journey physical roster size=%d", len(runner.physical))
	}
	for _, expected := range []struct {
		identity CandidateIdentity
		exists   bool
	}{{v1, false}, {v2, false}, {rollback, true}} {
		physical, ok := runner.physical[expected.identity.ContainerID]
		receiptID := physical.receiptID
		if receiptID == "" {
			receiptID = physical.policy.identity.ReceiptID
		}
		observed := candidateOwnerPhysicalInspection(physical)
		configured := physical.policy.identity
		configured.ContainerID = physical.containerID
		if !ok || physical.exists != expected.exists || physical.containerID != expected.identity.ContainerID ||
			receiptID != expected.identity.ReceiptID ||
			(expected.exists && (!physical.running || !generatedcontainer.MatchesConfigured(observed, configured,
				physical.policy.limits) || !generatedcontainer.CurrentlyRunning(observed))) {
			t.Fatalf("migration journey physical identity mismatch exists=%t", expected.exists)
		}
	}
	for _, physical := range runner.physical {
		if physical.policy.identity.DeploymentID == interruptedDeploymentID {
			t.Fatal("interrupted migration unexpectedly owns a candidate container")
		}
	}
}

func installGeneratedColdMigrationJourneyImages(t *testing.T, construction *generatedDomainConstructionRunner,
	appID, gatewayImageID, builderImageID string, plans []generatedColdMigrationJourneyPlan,
) map[string]generatedDomainDockerObject {
	t.Helper()
	if construction == nil || construction.delegate == nil || gatewayImageID == "" || builderImageID == "" {
		t.Fatal("migration journey cold census is unavailable")
	}
	census := construction.delegate
	present := make(map[string]generatedDomainDockerObject)
	for _, object := range census.objects {
		if object.kind == "image" {
			if _, duplicate := present[object.id]; duplicate {
				t.Fatal("migration journey cold census has duplicate image identities")
			}
			present[object.id] = object
		}
	}
	expected := make(map[string]generatedDomainDockerObject, len(plans))
	for _, plan := range plans {
		if plan.imageID == gatewayImageID || plan.imageID == builderImageID {
			t.Fatal("migration journey application image crossed a protected infrastructure image")
		}
		object := generatedDomainDockerObject{kind: "image", id: plan.imageID,
			labels: map[string]string{"io.rig.managed": "generated-image", "io.rig.application": appID,
				"io.rig.release": plan.releaseID, "io.rig.artifact": plan.artifactID,
				"io.rig.plan": plan.revision.ID, "io.rig.component": "api", "io.rig.role": "server",
				"io.rig.definition": plan.buildDigest},
			data: generatedDomainDockerObjectData{contentID: plan.imageID}}
		if _, duplicate := expected[plan.imageID]; duplicate {
			t.Fatal("migration journey application image identities are not unique")
		}
		expected[plan.imageID] = object
		if existing, exists := present[plan.imageID]; exists {
			if !reflect.DeepEqual(existing, object) {
				t.Fatal("migration journey application image crossed an existing census object")
			}
			continue
		}
		census.objects = append(census.objects, object)
		present[plan.imageID] = object
	}
	assertGeneratedColdMigrationJourneyImageObjects(t, census.objects, appID, expected)
	return expected
}

func assertGeneratedColdMigrationJourneyImageObjects(t *testing.T, objects []generatedDomainDockerObject,
	appID string, expected map[string]generatedDomainDockerObject,
) {
	t.Helper()
	if len(expected) != 3 {
		t.Fatal("migration journey expected image roster is incomplete")
	}
	seen := make(map[string]struct{}, len(expected))
	for _, object := range objects {
		if object.kind != "image" || object.labels["io.rig.application"] != appID {
			continue
		}
		want, found := expected[object.id]
		if !found || object.data.contentID != object.id || !reflect.DeepEqual(object, want) {
			t.Fatal("migration journey observed an unexpected application image")
		}
		if _, duplicate := seen[object.id]; duplicate {
			t.Fatal("migration journey observed a duplicate application image")
		}
		seen[object.id] = struct{}{}
	}
	if len(seen) != len(expected) {
		t.Fatalf("migration journey application image census=%d want=%d", len(seen), len(expected))
	}
}

// generatedColdMigrationJourneyImageRunner supplies only the detailed image
// observation for an authenticated image that has no candidate container. The
// startup census uses a different exact format and remains owned by delegate.
type generatedColdMigrationJourneyImageRunner struct {
	delegate runtimeprocess.CommandRunner
	images   map[string]candidatePolicy
}

func (r *generatedColdMigrationJourneyImageRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) == 5 && reflect.DeepEqual(args[:4],
		[]string{"image", "inspect", "--format", generatedCandidateImageInspectFormat}) {
		if policy, found := r.images[args[4]]; found {
			candidate := &candidateOwnerRunner{physical: candidateOwnerPhysical{policy: policy}}
			return candidate.defaultResult(candidateOwnerImageInspect, request)
		}
	}
	return r.delegate.Run(ctx, request)
}

func generatedColdMigrationJourneyImagePolicies(t *testing.T, appID string,
	plans []generatedColdMigrationJourneyPlan,
) map[string]candidatePolicy {
	t.Helper()
	result := make(map[string]candidatePolicy, len(plans))
	for _, plan := range plans {
		if len(plan.revision.Plan.Components) != 1 || plan.revision.Plan.Components[0].Name != "api" ||
			plan.imageID == "" || plan.releaseID == "" || plan.artifactID == "" || plan.buildDigest == "" {
			t.Fatal("migration journey image policy is incomplete")
		}
		component := plan.revision.Plan.Components[0]
		if _, duplicate := result[plan.imageID]; duplicate {
			t.Fatal("migration journey image policy is not unique")
		}
		result[plan.imageID] = candidatePolicy{identity: generatedcontainer.Identity{
			AppID: appID, ReleaseID: plan.releaseID, ArtifactID: plan.artifactID,
			DeploymentPlanRevisionID: plan.revision.ID, Component: component.Name,
			Role: component.Role, ImageContentID: plan.imageID,
		}, buildDefinitionDigest: plan.buildDigest}
	}
	if len(result) != 3 {
		t.Fatal("migration journey image policy roster is incomplete")
	}
	return result
}

func assertGeneratedColdMigrationJourneyGroups(t *testing.T, groups []generatedDomainColdGroup,
	v1, v2, rollback, v3 string,
) {
	t.Helper()
	if len(groups) != 4 {
		t.Fatalf("migration journey cold groups=%d", len(groups))
	}
	expected := map[string]generatedDomainColdGroupKind{v1: generatedDomainColdHistorical,
		v2: generatedDomainColdHistorical, rollback: generatedDomainColdCurrent,
		v3: generatedDomainColdNoEffect}
	for _, group := range groups {
		kind, ok := expected[group.pins.deploymentID]
		if !ok || group.kind != kind || len(group.snapshot.components) != 1 ||
			group.snapshot.components[0].plan.Name != "api" || group.snapshot.lifecycleRetained {
			t.Fatalf("migration journey cold group deployment=%s kind=%d", group.pins.deploymentID, group.kind)
		}
		if group.pins.deploymentID == v3 {
			component := group.snapshot.components[0]
			if !group.bindHead || group.selfHead || group.snapshot.runtime.phase != generatedruntimestate.PhaseMigrating ||
				group.snapshot.runtime.migrationState != generatedruntimestate.MigrationRunning ||
				component.state != generatedruntimestate.ComponentImageReady || component.receipt != nil ||
				component.containerID != "" || component.containerName != "" {
				t.Fatal("interrupted migration did not reconstruct as a no-effect successor")
			}
		}
		delete(expected, group.pins.deploymentID)
	}
	if len(expected) != 0 {
		t.Fatal("migration journey cold reconstruction omitted a deployment")
	}
}

func readGeneratedColdMigrationJourneyGroups(ctx context.Context, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, expectedRoles map[string]string,
) ([]generatedDomainColdGroup, string, string, error) {
	if authority == nil || len(expectedRoles) != 4 {
		return nil, "group_index", "census", deploymentexecution.ErrInvalidConfiguration
	}
	mainByID := make(map[string]deployments.GeneratedDomainStartupDeployment,
		len(observed.database.GeneratedDeployments))
	for _, value := range observed.database.GeneratedDeployments {
		if _, duplicate := mainByID[value.ID]; duplicate {
			return nil, "group_index", "census", errGeneratedDomainConstructionRefused
		}
		mainByID[value.ID] = value
	}
	runtimeByID := make(map[string]generatedruntimestate.GeneratedDomainStartupDeployment,
		len(observed.database.Runtime.Deployments))
	for _, value := range observed.database.Runtime.Deployments {
		if _, duplicate := runtimeByID[value.DeploymentID]; duplicate {
			return nil, "group_index", "census", errGeneratedDomainConstructionRefused
		}
		runtimeByID[value.DeploymentID] = value
	}
	if len(mainByID) != len(expectedRoles) || len(runtimeByID) != len(expectedRoles) {
		return nil, "group_index", "census", errGeneratedDomainConstructionRefused
	}
	ids := make([]string, 0, len(expectedRoles))
	for id := range expectedRoles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	groups := make([]generatedDomainColdGroup, 0, len(ids))
	for _, id := range ids {
		role := expectedRoles[id]
		main, mainPresent := mainByID[id]
		runtime, runtimePresent := runtimeByID[id]
		if role == "" || !mainPresent || !runtimePresent || main.AppID != runtime.AppID ||
			main.ReleaseID != runtime.ReleaseID ||
			main.DeploymentPlanRevisionID != runtime.DeploymentPlanRevisionID ||
			main.DeploymentPlanRevisionNumber != runtime.DeploymentPlanRevisionNumber {
			return nil, "group_read", role, errGeneratedDomainConstructionRefused
		}
		group, err := readGeneratedDomainColdGroup(ctx, authority, observed, main, runtime)
		if err != nil {
			return nil, "group_read", role, err
		}
		groups = append(groups, group)
	}
	for index := range groups {
		role := expectedRoles[groups[index].pins.deploymentID]
		kind, bindHead, selfHead, legacyHead, err := classifyGeneratedDomainColdGroup(
			groups[index].pins, groups[index].snapshot, groups, observed.database)
		if err != nil {
			return nil, "group_classify", role, err
		}
		groups[index].kind, groups[index].bindHead = kind, bindHead
		groups[index].selfHead, groups[index].legacyHead = selfHead, legacyHead
	}
	return groups, "group_classify", "complete", nil
}

func generatedColdMigrationJourneyErrorClass(err error) string {
	var diagnostic *generatedDomainConstructionDiagnostic
	if errors.As(err, &diagnostic) && diagnostic != nil &&
		validGeneratedDomainConstructionStage(diagnostic.stage) &&
		validGeneratedDomainConstructionReason(diagnostic.reason) {
		return string(diagnostic.stage) + "/" + string(diagnostic.reason)
	}
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, errGeneratedDomainConstructionRefused):
		return "construction_refused"
	case errors.Is(err, deploymentexecution.ErrIdentityLost):
		return "identity_lost"
	case errors.Is(err, deploymentexecution.ErrInvalidConfiguration):
		return "invalid_configuration"
	case errors.Is(err, deploymentexecution.ErrStageInvalid):
		return "stage_invalid"
	case errors.Is(err, ErrInvalidDeployment):
		return "invalid_deployment"
	case errors.Is(err, deployments.ErrApprovalRequired):
		return "approval_required"
	default:
		return "closed_other"
	}
}

type generatedColdMigrationJourneyMutationSnapshot struct {
	create, start, stop, remove int
	reloads, restartInstalls    int
	stopped                     bool
	slot                        generatedidentity.Slot
	endpoints                   map[string]generatedRouteEndpointSnapshot
	files                       map[string][]byte
	liveConfig                  []byte
	capacityProbes              int
}

func snapshotGeneratedColdMigrationJourneyMutations(owner *candidateOwnerRunner,
	routes *generatedRoutePredecessorRunner, construction *generatedDomainConstructionRunner,
) generatedColdMigrationJourneyMutationSnapshot {
	reloads := 0
	for _, command := range routes.commands {
		if len(command) >= 7 && command[0] == "container" && command[1] == "exec" &&
			command[3] == "caddy" && command[4] == "reload" {
			reloads++
		}
	}
	result := generatedColdMigrationJourneyMutationSnapshot{
		create: owner.count(candidateOwnerCreate), start: owner.count(candidateOwnerStart),
		stop: owner.count(candidateOwnerStop), remove: owner.count(candidateOwnerRemove),
		reloads: reloads, restartInstalls: routes.restartInstalls,
		stopped: routes.stopped, slot: routes.slot,
		endpoints:      make(map[string]generatedRouteEndpointSnapshot, len(routes.endpoints)),
		files:          make(map[string][]byte, len(routes.files)),
		liveConfig:     append([]byte(nil), routes.liveConfig...),
		capacityProbes: generatedRouteColdAccountingCapacityProbeCount(construction.snapshot()),
	}
	for id, endpoint := range routes.endpoints {
		result.endpoints[id] = endpoint
	}
	for name, body := range routes.files {
		result.files[name] = append([]byte(nil), body...)
	}
	return result
}

func assertGeneratedColdMigrationJourneyNoEffectManifest(t *testing.T, domain *GeneratedDomain,
	appID, interruptedDeploymentID, predecessorDeploymentID string,
) {
	t.Helper()
	if domain == nil || domain.construction == nil || domain.construction.cold == nil {
		t.Fatal("migration journey did not publish a cold manifest")
	}
	assert := func(manifest *generatedDomainColdManifest) {
		if manifest.pendingActivation != nil || manifest.pendingActivationDeploymentID != "" ||
			manifest.pendingGatewayDisposition != generatedDomainColdPendingGatewayUnknown {
			t.Fatal("migration journey cold manifest published pending route authority")
		}
		var matched *generatedDomainColdManifestGroup
		for index := range manifest.groups {
			group := &manifest.groups[index]
			if group.appID == appID && group.deploymentID == interruptedDeploymentID {
				matched = group
			}
		}
		if matched == nil || matched.head == nil || matched.head.deploymentID != predecessorDeploymentID ||
			len(matched.components) != 1 || matched.components[0].name != "api" ||
			matched.components[0].receiptID != "" || matched.components[0].receiptWord != 0 ||
			matched.components[0].charged || matched.components[0].retained {
			t.Fatal("interrupted migration cold manifest is not no-effect")
		}
	}
	assert(domain.construction.cold)
	ledger, err := snapshotGeneratedDomainColdManifest(domain.Candidates())
	if err != nil {
		t.Fatal("read migration journey post-construction accounting manifest")
	}
	assert(&ledger)
}

func TestGeneratedDomainColdMigrationUncertaintyHistoryReconstructsNoEffectSuccessor(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		harness := newGeneratedColdMigrationJourneyHarness(t)
		v1Plan := harness.newPlan(t, false, strings.Repeat("a", 64))
		v1 := harness.newDeployment(t, v1Plan, false, false, "current")

		owner, candidates := newGeneratedColdMigrationJourneyOwner(t, harness.identity, v1)
		store, err := newStateStore(harness.identity.dataRoot)
		if err != nil {
			t.Fatalf("create migration journey route store: %v", err)
		}
		if err := store.createInitialEmpty(); err != nil {
			t.Fatalf("create migration journey route state: %v", err)
		}
		_, _, ingressIP := ingressNetworkCandidate(0)
		ingress := &ingressRunner{appID: harness.guarded.appID, files: map[string][]byte{},
			caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
				GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
			ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
				Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}}
		physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
			endpoints: make(map[string]generatedRouteEndpointSnapshot)}
		routeReader := &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes, delegate: physicalRoutes}
		routeRunner := &generatedDomainTwoComponentRouteRunner{
			daemonID: "generated-domain-cold-migration-journey", delegate: routeReader}
		manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
			database: harness.identity.database, stores: harness.identity.stores,
			runner: routeRunner, options: harness.identity.options,
		}, store)
		if err != nil {
			t.Fatal("construct migration journey route manager")
		}
		physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), harness.identity.stores,
			routeRunner, harness.identity.options)
		if err != nil || bindGeneratedDomainManagerView(manager, physical.manager) != nil {
			t.Fatal("bind migration journey route manager")
		}
		routes, err := newGeneratedRouteAuthority(manager, owner.authority, harness.identity.stores)
		if err != nil {
			t.Fatal("construct migration journey route authority")
		}

		v1Route := completeGeneratedColdMigrationJourneyRoute(t, owner, candidates, routes, manager,
			physicalRoutes, v1, strings.Repeat("7", 64))
		v1Route.release(t)

		v2Plan := harness.newPlan(t, true, strings.Repeat("b", 64))
		v2 := harness.newDeployment(t, v2Plan, true, true, "current")
		v2Route := completeGeneratedColdMigrationJourneyRoute(t, owner, candidates, routes, manager,
			physicalRoutes, v2, strings.Repeat("8", 64))
		v2Route.release(t)

		rollback := harness.newDeployment(t, v1Plan, false, false, "original")
		rollbackRoute := completeGeneratedColdMigrationJourneyRoute(t, owner, candidates, routes, manager,
			physicalRoutes, rollback, strings.Repeat("9", 64))
		rollbackRoute.release(t)

		v3Plan := harness.newPlan(t, true, strings.Repeat("c", 64))
		v3 := harness.newDeployment(t, v3Plan, true, false, "current")
		if err := v3.releaseEffects(); err != nil {
			t.Fatal("release interrupted migration effects")
		}
		served, err := manager.Observe(context.Background(), harness.guarded.appID)
		if err != nil || served.Slot != generatedidentity.Slot(rollbackRoute.identity.Slot) ||
			len(served.Endpoints) != 1 || served.Endpoints[0] != generatedRouteEndpoint(rollbackRoute.identity) {
			t.Fatalf("migration journey served rollback route=%#v error=%v", served, err)
		}

		assertGeneratedColdMigrationJourneyRevisionPins(t, harness.identity.database, harness.configuration,
			harness.plans, []generatedColdMigrationJourneyPlan{v1Plan, v2Plan, v3Plan})
		before, err := harness.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal("read migration journey startup snapshot")
		}
		assertGeneratedColdMigrationJourneySnapshot(t, before, harness.guarded.appID,
			v1, v2, rollback, v3, v1Plan, v2Plan, v3Plan, v1Route, v2Route, rollbackRoute)
		assertGeneratedColdMigrationJourneyPhysical(t, candidates, v1Route.identity, v2Route.identity,
			rollbackRoute.identity, v3.request.DeploymentID)

		process, construction := generatedRouteColdAccountingPartialRestartProcess(t, harness.identity, store,
			physicalRoutes, candidates, []CandidateIdentity{rollbackRoute.identity})
		if construction.roster != candidates || process.predecessor != candidates ||
			len(process.predecessorPhysical) != 1 {
			t.Fatal("migration journey cold runner bindings are incomplete")
		}
		currentPhysical := process.predecessorPhysical[0]
		currentReceiptID := currentPhysical.receiptID
		if currentReceiptID == "" {
			currentReceiptID = currentPhysical.policy.identity.ReceiptID
		}
		if !currentPhysical.exists || currentPhysical.containerID != rollbackRoute.identity.ContainerID ||
			currentReceiptID != rollbackRoute.identity.ReceiptID ||
			currentPhysical.policy.identity.Component != "api" {
			t.Fatal("migration journey cold runner lost the exact current physical identity")
		}
		builder, ok := process.cold.(*generatedDomainTwoComponentRouteRunner)
		if !ok || builder == nil || builder.builder == nil {
			t.Fatal("migration journey cold builder identity is unavailable")
		}
		construction.roster = &generatedColdMigrationJourneyImageRunner{delegate: candidates,
			images: generatedColdMigrationJourneyImagePolicies(t, harness.guarded.appID,
				[]generatedColdMigrationJourneyPlan{v1Plan, v2Plan, v3Plan})}
		expectedImages := installGeneratedColdMigrationJourneyImages(t, construction, harness.guarded.appID,
			construction.delegate.imageID, builder.builder.image.ID,
			[]generatedColdMigrationJourneyPlan{v1Plan, v2Plan, v3Plan})
		mutationsBefore := snapshotGeneratedColdMigrationJourneyMutations(owner.runner, physicalRoutes, construction)
		observation, observationErr := inspectGeneratedDomainStartupObservation(context.Background(),
			harness.identity.stores, process, harness.identity.options)
		if observationErr == nil {
			assertGeneratedColdMigrationJourneyImageObjects(t, observation.docker.objects,
				harness.guarded.appID, expectedImages)
		}
		stage := "observation"
		role := "census"
		var groups []generatedDomainColdGroup
		var pending *generatedDomainColdPendingActivationEvidence
		if observationErr == nil {
			groups, stage, role, err = readGeneratedColdMigrationJourneyGroups(context.Background(),
				owner.authority, observation, map[string]string{
					v1.request.DeploymentID:       "v1_initial",
					v2.request.DeploymentID:       "v2_migrated",
					rollback.request.DeploymentID: "v1_rollback",
					v3.request.DeploymentID:       "v3_interrupted",
				})
		}
		if observationErr == nil && err == nil {
			var found bool
			var evidence generatedDomainColdPendingActivationEvidence
			evidence, found, err = generatedDomainColdPendingActivation(observation.database, observation.protected, groups)
			if found {
				pending = &evidence
			}
			stage, role = "pending_activation", "all"
		}
		if observationErr == nil && err == nil {
			err = validateGeneratedDomainColdLifecycleRetentionWithPending(observation.database, groups, pending)
			stage, role = "lifecycle", "all"
		}
		if observationErr == nil && err == nil {
			err = validateGeneratedDomainColdGatewayRoutesWithPending(observation, groups, pending)
			stage, role = "gateway", "all"
		}
		if observationErr == nil && err == nil {
			stage, role = "layer_complete", "all"
			assertGeneratedColdMigrationJourneyGroups(t, groups, v1.request.DeploymentID,
				v2.request.DeploymentID, rollback.request.DeploymentID, v3.request.DeploymentID)
			if pending != nil {
				t.Fatal("migration journey unexpectedly reconstructed a pending route activation")
			}
		}

		if observationErr == nil && err == nil {
			stage, role = "public_constructor", "all"
		}
		domain, constructErr := NewGeneratedDomain(context.Background(), harness.identity.stores, process,
			&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(harness.identity.options))
		mutationsAfter := snapshotGeneratedColdMigrationJourneyMutations(owner.runner, physicalRoutes, construction)
		after, snapshotErr := harness.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
		if snapshotErr != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("cold migration reconstruction changed durable history")
		}
		if observationErr != nil || err != nil || constructErr != nil || domain == nil {
			failure := observationErr
			if failure == nil {
				failure = err
			}
			if failure == nil {
				failure = constructErr
			}
			t.Fatalf("migration cold reconstruction stage=%s role=%s class=%s publicConstructed=%t",
				stage, role, generatedColdMigrationJourneyErrorClass(failure), domain != nil)
		}
		if !reflect.DeepEqual(mutationsBefore, mutationsAfter) {
			t.Fatal("migration cold reconstruction issued a candidate, route, or capacity effect")
		}
		assertGeneratedColdMigrationJourneyNoEffectManifest(t, domain, harness.guarded.appID,
			v3.request.DeploymentID, rollback.request.DeploymentID)
		assertGeneratedColdMigrationJourneyPhysical(t, candidates, v1Route.identity, v2Route.identity,
			rollbackRoute.identity, v3.request.DeploymentID)
	})
}

func TestGeneratedDomainColdRuntimeBoundary(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("no_effect_successor_resumes_with_one_capacity_charge", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			predecessor, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			candidate.moveToWaitingHealth(t, execution)
			if err := execution.WaitHealthyAndRecord(invocation); err != nil {
				t.Fatal(err)
			}
			if _, err := execution.FinishHealth(); err != nil {
				t.Fatal(err)
			}

			store, err := newStateStore(identity.dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.createInitialEmpty(); err != nil {
				t.Fatal(err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, _, ingressIP := ingressNetworkCandidate(0)
			ingress := &ingressRunner{
				appID: migration.guarded.appID, network: predecessor.NetworkName, endpoint: generatedRouteEndpoint(predecessor),
				caddyNetworks:     map[string]*networkAttachment{caddyNetworkName: {GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
				ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {Name: caddyContainerName, IPv4Address: ingressIP + "/28"}},
				files:             map[string][]byte{},
			}
			physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
				slot: generatedidentity.Slot(predecessor.Slot), endpoints: map[string]generatedRouteEndpointSnapshot{
					predecessor.ContainerID: {appID: migration.guarded.appID,
						endpoint: generatedRouteEndpoint(predecessor), slot: generatedidentity.Slot(predecessor.Slot)},
				}}
			routeSeed := &generatedDomainRouteSeedRunner{daemonID: "generated-cold-runtime-boundary", delegate: ingress}
			reader := &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes, delegate: routeSeed}
			manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database, stores: identity.stores, runner: reader, options: identity.options}, store)
			if err != nil {
				t.Fatal(err)
			}
			physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores, reader, identity.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
				t.Fatal(err)
			}
			routes, err := newGeneratedRouteAuthority(manager, candidate.authority, candidate.authority.stores)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := routes.Pin(migration.guarded.context, execution, generatedRouteRequest(migration.guarded.appID, predecessor))
			if err != nil {
				t.Fatal(err)
			}
			activated, err := sealed.Activate(migration.guarded.context)
			if err != nil || activated.Phase != generatedruntimestate.PhaseDraining {
				t.Fatalf("activate predecessor = %+v, %v", activated, err)
			}
			runtimeState := generatedruntimestate.New(identity.database)
			if _, err := runtimeState.Advance(context.Background(), migration.guarded.appID, migration.request.DeploymentID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := migration.repository.Transition(context.Background(), migration.guarded.appID, migration.request.DeploymentID, Succeeded, ""); err != nil {
				t.Fatal(err)
			}
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database, time.Now().UTC(), migration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover completed predecessor job: result=%+v err=%v", recovered, err)
			}
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return jobs.ExecutionResult{}, nil
				}), migration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory, nil)

			created, err := migration.jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
				ApplicationID: migration.guarded.appID, IdempotencyKey: "cold-runtime-successor", RequestedBy: migration.guarded.actorID,
				Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
			}, jobs.DeploymentCreateHooks{})
			if err != nil || !created.Created {
				t.Fatalf("create successor job: created=%t err=%v", created.Created, err)
			}
			var successor deployments.Deployment
			pauseStage := "not_entered"
			var pauseCause error
			pauseExecutor := generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				pauseStage = "get_or_create"
				var err error
				successor, _, err = migration.repository.GetOrCreateByJob(ctx, migration.guarded.appID, job.ID, "current")
				if err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "initialize_runtime"
				successor, err = migration.repository.InitializeRuntime(ctx, migration.guarded.appID, successor.ID, migration.request.ReleaseID,
					migration.request.ConfigurationRevisionID, migration.request.ConfigurationRevisionNumber, deployments.RuntimeGeneratedNode,
					migration.request.DeploymentPlanRevisionID, migration.request.DeploymentPlanRevisionNumber)
				if err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "claim"
				claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, migration.guarded.appID, job.Attempt)
				if err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "gate"
				if err := migration.repository.Gate(ctx, deployments.GateRequest{Claim: claim, DeploymentID: successor.ID}); err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "begin_runtime"
				begun, wasCreated, err := runtimeState.Begin(ctx, generatedruntimestate.BeginInput{DeploymentID: successor.ID, AppID: migration.guarded.appID,
					ReleaseID: migration.request.ReleaseID, DeploymentPlanRevisionID: migration.request.DeploymentPlanRevisionID,
					DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber, ComponentNames: []string{"api"}})
				if err != nil || !wasCreated {
					pauseCause = errors.Join(err, errors.New("successor runtime was not freshly created"))
					return jobs.ExecutionResult{}, pauseCause
				}
				pauseStage = "advance_building"
				if _, err := runtimeState.Advance(ctx, migration.guarded.appID, successor.ID, begun.Phase, generatedruntimestate.PhaseBuilding, ""); err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "paused"
				return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser, PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
			})
			runGeneratedColdBoundaryWorker(t, migration.jobService, pauseExecutor, created.Job.ID, jobs.WaitingUser, identity.options.WorkingDirectory,
				func() string { return "stage=" + pauseStage + " cause=" + boundedErrorText(pauseCause) })

			state, err = store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
			observation.V1Container.ID = strings.Repeat("d", 64)
			observation.V1NetworkID = strings.Repeat("e", 64)
			for id, member := range observation.V1Network.Containers {
				delete(observation.V1Network.Containers, id)
				observation.V1Network.Containers[observation.V1Container.ID] = member
			}
			gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
			objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
			for index := range objects {
				switch objects[index].name {
				case caddyContainerName:
					objects[index].id = observation.V1Container.ID
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver, scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver, scope: observation.V1Volume.Scope,
						options: cloneStringMap(observation.V1Volume.Options), mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := candidate.runner.snapshot().policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: predecessor.ContainerID, name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-boundary", objects: objects, imageID: observation.Image.ID, gateway: gateway,
			}}
			domain, err := NewGeneratedDomain(context.Background(), identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			if err != nil || domain == nil {
				diagnostic := generatedDomainDiagnoseOuterStable(t, identity, runner.delegate)
				stage := generatedColdBoundaryStageTrace(t, identity, runner, store)
				t.Fatalf("construct cold domain: domain=%v err=%v diagnostic=%s stage=%s snapshot=%s commandCount=%d", domain, err,
					diagnostic, stage, generatedColdBoundarySnapshotSummary(t, identity), len(runner.snapshot()))
			}
			if domain.construction.cold == nil || len(domain.construction.cold.groups) != 2 {
				t.Fatalf("cold groups=%#v", domain.construction.cold)
			}
			var successorGroup *generatedDomainColdManifestGroup
			for index := range domain.construction.cold.groups {
				group := &domain.construction.cold.groups[index]
				if group.deploymentID == successor.ID {
					successorGroup = group
				}
			}
			if successorGroup == nil || successorGroup.head == nil || successorGroup.head.deploymentID != migration.request.DeploymentID ||
				len(successorGroup.components) != 1 || successorGroup.components[0].charged || successorGroup.components[0].retained || successorGroup.components[0].receiptID != "" {
				t.Fatalf("no-effect successor group=%#v", successorGroup)
			}

			resumed, err := migration.jobService.Resume(created.Job.ID)
			if err != nil || resumed.Attempt != 1 || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume successor=%+v err=%v", resumed, err)
			}
			var reserveCalls int
			capacityProbesBefore := generatedColdBoundaryCapacityProbes(runner.snapshot())
			successorSlot := "blue"
			if predecessor.Slot == successorSlot {
				successorSlot = "green"
			}
			reserveExecutor := generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				reserveCalls++
				claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, migration.guarded.appID, job.Attempt)
				if err != nil {
					return jobs.ExecutionResult{}, err
				}
				reserved, err := domain.Candidates().ReserveReplacement(ctx, GeneratedReplacementRequest{
					Claim: claim, DeploymentID: successor.ID, ReleaseID: migration.request.ReleaseID,
					ConfigurationRevisionID: migration.request.ConfigurationRevisionID, ConfigurationRevisionNumber: migration.request.ConfigurationRevisionNumber,
					DeploymentPlanRevisionID: migration.request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
					CandidateSlot: successorSlot,
				})
				if err != nil {
					return jobs.ExecutionResult{}, err
				}
				reserved.Release()
				return jobs.ExecutionResult{}, nil
			})
			runGeneratedColdBoundaryWorker(t, migration.jobService, reserveExecutor, created.Job.ID, jobs.Succeeded, identity.options.WorkingDirectory, nil)
			if reserveCalls != 1 {
				t.Fatalf("resumed reserve calls=%d want 1", reserveCalls)
			}
			capacityProbesAfter := generatedColdBoundaryCapacityProbes(runner.snapshot())
			if capacityProbesAfter-capacityProbesBefore != 1 {
				t.Fatalf("resumed capacity probe delta=%d want 1 commands=%#v", capacityProbesAfter-capacityProbesBefore, runner.snapshot())
			}
		})
	})
	t.Run("retained_risk_resumes_without_a_second_capacity_charge", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			candidateIdentity, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			candidate.moveToWaitingHealth(t, execution)
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database, time.Now().UTC(), migration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover receipt-owned job: result=%+v err=%v", recovered, err)
			}
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser, PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
				}), migration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)

			store, err := newStateStore(identity.dataRoot)
			if err != nil || store.createInitialEmpty() != nil {
				t.Fatalf("install empty gateway state: %v", err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
			observation.V1Container.ID = strings.Repeat("d", 64)
			observation.V1NetworkID = strings.Repeat("e", 64)
			for id, member := range observation.V1Network.Containers {
				delete(observation.V1Network.Containers, id)
				observation.V1Network.Containers[observation.V1Container.ID] = member
			}
			gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
			objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
			for index := range objects {
				switch objects[index].name {
				case caddyContainerName:
					objects[index].id = observation.V1Container.ID
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver, scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver, scope: observation.V1Volume.Scope,
						options: cloneStringMap(observation.V1Volume.Options), mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := candidate.runner.snapshot().policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: candidateIdentity.ContainerID, name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-risk", objects: objects, imageID: observation.Image.ID, gateway: gateway,
			}}
			priorManager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
				database: identity.database, stores: identity.stores, runner: runner, options: identity.options,
			}, store)
			if err != nil {
				t.Fatal(err)
			}
			releasePriorLock, err := priorManager.lockGateway(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := releasePriorLock(); err != nil {
				t.Fatal(err)
			}
			domain, err := NewGeneratedDomain(context.Background(), identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			if err != nil || domain == nil || domain.construction.cold == nil || len(domain.construction.cold.groups) != 1 {
				diagnostic := generatedDomainDiagnoseOuterStable(t, identity, runner.delegate)
				t.Fatalf("construct retained risk: domain=%v err=%v diagnostic=%s snapshot=%s commandCount=%d", domain, err,
					diagnostic, generatedColdBoundarySnapshotSummary(t, identity), len(runner.snapshot()))
			}
			group := domain.construction.cold.groups[0]
			if len(group.components) != 1 || !group.components[0].charged || !group.components[0].retained || group.components[0].receiptID == "" {
				t.Fatalf("retained-risk group=%#v", group)
			}
			if probes := generatedColdBoundaryCapacityProbes(runner.snapshot()); probes != 1 {
				t.Fatalf("cold retained-risk probes=%d want 1 commands=%#v", probes, runner.snapshot())
			}
			resumed, err := migration.jobService.Resume(migration.claim.JobID())
			if err != nil || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume retained-risk job=%+v err=%v", resumed, err)
			}
			var reserveErr error
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					claim, err := deploymentexecution.NewClaim(migration.binding, job.ID, migration.guarded.appID, job.Attempt)
					if err != nil {
						return jobs.ExecutionResult{}, err
					}
					request := GeneratedReplacementRequest(validation.request)
					request.Claim = claim
					value, err := domain.Candidates().ReserveReplacement(ctx, request)
					reserveErr = err
					if value != nil {
						value.Release()
					}
					return jobs.ExecutionResult{}, err
				}), migration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory, nil)
			if reserveErr != nil {
				t.Fatalf("resumed retained-risk reserve=%v", reserveErr)
			}
			if probes := generatedColdBoundaryCapacityProbes(runner.snapshot()); probes != 1 {
				t.Fatalf("resumed retained-risk double charged: probes=%d commands=%#v", probes, runner.snapshot())
			}
		})
	})
	t.Run("initial_created_candidate_adopts_same_identity_and_starts_once_without_second_capacity_charge", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			candidateIdentity, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			created := validation.read(t)
			physicalCreated := candidate.runner.snapshot()
			expectedIdentity := physicalCreated.policy.identity
			expectedIdentity.ContainerID = candidateIdentity.ContainerID
			if created.runtime.phase != generatedruntimestate.PhaseStartingCandidate ||
				len(created.components) != 1 || created.components[0].state != generatedruntimestate.ComponentRunning ||
				created.components[0].receipt == nil || created.components[0].receipt.id != candidateIdentity.ReceiptID ||
				created.components[0].containerID != candidateIdentity.ContainerID ||
				created.components[0].containerName != candidateIdentity.ContainerName ||
				!physicalCreated.exists || physicalCreated.containerID != candidateIdentity.ContainerID ||
				!generatedcontainer.MatchesConfigured(candidateOwnerPhysicalInspection(physicalCreated), expectedIdentity, physicalCreated.policy.limits) ||
				!generatedcontainer.NeverStarted(candidateOwnerPhysicalInspection(physicalCreated)) ||
				candidate.runner.count(candidateOwnerCreate) != 1 || candidate.runner.count(candidateOwnerStart) != 0 ||
				candidate.runner.count(candidateOwnerStop) != 0 || candidate.runner.count(candidateOwnerRemove) != 0 {
				t.Fatalf("durable never-started candidate: admission=%#v identity=%#v physical=%#v create=%d start=%d stop=%d remove=%d",
					created, candidateIdentity, physicalCreated, candidate.runner.count(candidateOwnerCreate),
					candidate.runner.count(candidateOwnerStart), candidate.runner.count(candidateOwnerStop),
					candidate.runner.count(candidateOwnerRemove))
			}

			// The interrupted worker owns no scope when cold construction begins.
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database,
				time.Now().UTC(), migration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover created-candidate job: result=%+v err=%v", recovered, err)
			}
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
						PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
				}), migration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)

			store, err := newStateStore(identity.dataRoot)
			if err != nil || store.createInitialEmpty() != nil {
				t.Fatalf("install empty gateway state: %v", err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Version != stateVersion || len(state.Active) != 0 || state.Pending != nil {
				t.Fatalf("created-candidate gateway baseline=%#v", state)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
			observation.V1Container.ID = strings.Repeat("d", 64)
			observation.V1NetworkID = strings.Repeat("e", 64)
			for id, member := range observation.V1Network.Containers {
				delete(observation.V1Network.Containers, id)
				observation.V1Network.Containers[observation.V1Container.ID] = member
			}
			gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
			objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
			for index := range objects {
				switch objects[index].name {
				case caddyContainerName:
					objects[index].id = observation.V1Container.ID
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
						scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
						scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
						mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := physicalCreated.policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID,
					"io.rig.release": policy.identity.ReleaseID, "io.rig.artifact": policy.identity.ArtifactID,
					"io.rig.plan": policy.identity.DeploymentPlanRevisionID, "io.rig.component": policy.identity.Component,
					"io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: candidateIdentity.ContainerID,
					name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-initial-created-candidate", objects: objects,
				imageID: observation.Image.ID, gateway: gateway,
			}}
			process := &generatedColdCreatedCandidateStartRunner{delegate: runner, candidate: candidate.runner,
				containerID: candidateIdentity.ContainerID}
			priorManager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
				database: identity.database, stores: identity.stores, runner: process, options: identity.options,
			}, store)
			if err != nil {
				t.Fatal(err)
			}
			releasePriorLock, err := priorManager.lockGateway(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := releasePriorLock(); err != nil {
				t.Fatal(err)
			}
			startupBefore, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(startupBefore.Runtime.ActiveHeads) != 0 || len(startupBefore.Runtime.RouteActivations) != 0 {
				t.Fatalf("created-candidate baseline is not the canonical empty head: heads=%#v activations=%#v",
					startupBefore.Runtime.ActiveHeads, startupBefore.Runtime.RouteActivations)
			}
			admissionBefore := validation.read(t)
			gatewayBefore, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			physicalBefore := candidate.runner.snapshot()
			createBefore, startBefore := candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart)
			stopBefore, removeBefore := candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove)

			domain, constructErr := NewGeneratedDomain(context.Background(), identity.stores, process,
				&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			startupAfter, snapshotErr := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			gatewayAfter, stateErr := store.load()
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			admissionAfter := validation.read(t)
			physicalAfter := candidate.runner.snapshot()
			if !reflect.DeepEqual(startupBefore, startupAfter) || !reflect.DeepEqual(gatewayBefore, gatewayAfter) ||
				!reflect.DeepEqual(admissionBefore, admissionAfter) || !reflect.DeepEqual(physicalBefore, physicalAfter) ||
				candidate.runner.count(candidateOwnerCreate) != createBefore || candidate.runner.count(candidateOwnerStart) != startBefore ||
				candidate.runner.count(candidateOwnerStop) != stopBefore || candidate.runner.count(candidateOwnerRemove) != removeBefore {
				t.Fatalf("cold constructor changed created-candidate prefix: startup=%t gateway=%t admission=%t physical=%t commands=%d/%d/%d/%d want=%d/%d/%d/%d",
					reflect.DeepEqual(startupBefore, startupAfter), reflect.DeepEqual(gatewayBefore, gatewayAfter),
					reflect.DeepEqual(admissionBefore, admissionAfter), reflect.DeepEqual(physicalBefore, physicalAfter),
					candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart),
					candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove),
					createBefore, startBefore, stopBefore, removeBefore)
			}
			if constructErr != nil {
				message := "generated domain construction was refused: stage=cold_construction reason=cold_group_container_attestation"
				assertGeneratedDomainConstructionDiagnostic(t, constructErr,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerAttestation, message)
				if domain != nil || generatedColdBoundaryCapacityProbes(runner.snapshot()) != 0 {
					t.Fatalf("refused created-candidate construction published domain=%v or probed capacity: commands=%#v",
						domain, runner.snapshot())
				}
				t.Fatalf("created configured NeverStarted candidate must reconstruct and remain startable; current base refused at the exact cold container boundary: %v", constructErr)
			}
			if domain == nil || domain.construction == nil || domain.construction.cold == nil ||
				len(domain.construction.cold.groups) != 1 {
				t.Fatalf("created-candidate cold domain=%#v", domain)
			}
			group := domain.construction.cold.groups[0]
			if group.deploymentID != migration.request.DeploymentID || len(group.components) != 1 ||
				group.components[0].name != "api" || group.components[0].receiptID != candidateIdentity.ReceiptID ||
				!group.components[0].charged || !group.components[0].retained {
				t.Fatalf("created-candidate cold group=%#v", group)
			}
			if probes := generatedColdBoundaryCapacityProbes(runner.snapshot()); probes != 0 {
				t.Fatalf("created-candidate cold capacity probes=%d want 0 commands=%#v", probes, runner.snapshot())
			}

			ledger := domain.Candidates().ledger
			ledger.mu.Lock()
			capacityGroup := ledger.groups[candidateCapacityGroupKey{appID: migration.guarded.appID,
				deploymentID: migration.request.DeploymentID}]
			ledger.mu.Unlock()
			if capacityGroup == nil {
				t.Fatal("created-candidate cold construction omitted exact accounting group")
			}
			capacityGroup.mu.Lock()
			component := capacityGroup.byName["api"]
			var retainedCharge *candidateCapacityCharge
			var retainedDebt *generatedDomainCapacityDebtToken
			if component != nil && component.charge != nil {
				retainedDebt, _ = component.charge.token.(*generatedDomainCapacityDebtToken)
			}
			validAccounting := component != nil && component.receipt != nil && component.receipt.id == candidateIdentity.ReceiptID &&
				candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalPresent &&
				component.charge != nil && component.charge.state == candidateChargeActive &&
				component.charge.owner == candidateAccountingCandidate && component.charge.receiptRetained &&
				!component.charge.explicitRetained && !component.charge.logicallyRetired && component.charge.refs == 0 &&
				component.charge.lease == nil && retainedDebt != nil && retainedDebt.active &&
				retainedDebt.limits == capacityGroup.limits && !component.tokenHeld
			if component != nil {
				retainedCharge = component.charge
			}
			capacityGroup.mu.Unlock()
			if !validAccounting || !candidateCapacityChargeTokenPublished(retainedDebt) {
				t.Fatalf("created-candidate cold accounting component=%#v", component)
			}
			coordinator := domain.construction.capacity
			coordinator.mu.Lock()
			validCoordinator := coordinator.phase == generatedDomainCapacityPublished && coordinator.borrows == 0 &&
				coordinator.memory == capacityGroup.limits.MemoryBytes && coordinator.disk == capacityGroup.limits.DiskBytes &&
				len(coordinator.tokens) == 1 && retainedDebt != nil && retainedDebt.owner == coordinator
			_, debtRegistered := coordinator.tokens[retainedDebt]
			coordinatorPhase, coordinatorBorrows := coordinator.phase, coordinator.borrows
			coordinatorMemory, coordinatorDisk, coordinatorTokens := coordinator.memory, coordinator.disk, len(coordinator.tokens)
			coordinator.mu.Unlock()
			if !validCoordinator || !debtRegistered {
				t.Fatalf("created-candidate cold coordinator phase=%d borrows=%d memory=%d disk=%d tokens=%d",
					coordinatorPhase, coordinatorBorrows, coordinatorMemory, coordinatorDisk, coordinatorTokens)
			}
			process.enableStart()

			resumed, err := migration.jobService.Resume(migration.claim.JobID())
			if err != nil || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume created-candidate job=%+v err=%v", resumed, err)
			}
			var workerErr error
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					claim, err := deploymentexecution.NewClaim(migration.binding, job.ID, migration.guarded.appID, job.Attempt)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					request := GeneratedReplacementRequest(validation.request)
					request.Claim = claim
					freshReservation, err := domain.Candidates().ReserveReplacement(ctx, request)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshReservation.Release()
					freshExecution, err := domain.Candidates().PinCandidates(ctx, GeneratedCandidateRequest(request), freshReservation)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshExecution.Leave()
					adopted, err := freshExecution.AdoptCandidate("api")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					permit, ok := adopted.(*generatedCandidateInvocationPermit)
					if !ok || permit == nil || permit.containerID != candidateIdentity.ContainerID ||
						adopted.Identity().ReceiptID != candidateIdentity.ReceiptID {
						workerErr = fmt.Errorf("adopted created candidate = %T %#v", adopted, adopted.Identity())
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.StartCandidate(adopted); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					return jobs.ExecutionResult{}, nil
				}), migration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory,
				func() string { return boundedErrorText(workerErr) })
			if workerErr != nil {
				t.Fatalf("fresh created-candidate worker: %v", workerErr)
			}
			physicalRunning := candidate.runner.snapshot()
			if physicalRunning.containerID != candidateIdentity.ContainerID ||
				!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(physicalRunning), expectedIdentity, policy.limits) ||
				!generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(physicalRunning)) ||
				candidate.runner.count(candidateOwnerCreate) != 1 || candidate.runner.count(candidateOwnerStart) != 1 ||
				candidate.runner.count(candidateOwnerStop) != 0 || candidate.runner.count(candidateOwnerRemove) != 0 ||
				generatedColdBoundaryCapacityProbes(runner.snapshot()) != 0 {
				t.Fatalf("fresh worker did not start exact created candidate once: physical=%#v create=%d start=%d stop=%d remove=%d probes=%d",
					physicalRunning, candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart),
					candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove),
					generatedColdBoundaryCapacityProbes(runner.snapshot()))
			}
			capacityGroup.mu.Lock()
			component = capacityGroup.byName["api"]
			retainedAfterStart := component != nil && component.charge == retainedCharge && component.charge != nil &&
				component.charge.state == candidateChargeActive && component.charge.owner == candidateAccountingCandidate &&
				component.charge.receiptRetained && component.charge.refs == 0 && component.charge.lease == nil &&
				component.charge.token == retainedDebt && component.receipt != nil &&
				candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalPresent
			capacityGroup.mu.Unlock()
			if !retainedAfterStart || !candidateCapacityChargeTokenPublished(retainedDebt) {
				t.Fatalf("fresh worker replaced or released created-candidate accounting: %#v", component)
			}
		})
	})
	t.Run("replacement_sequential_created_prefix_recovers_without_new_capacity_or_forged_provenance", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			components := []string{"api", "frontend"}
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, components, components,
				generatedDomainTwoComponentPlan())
			identity.stores = migration.repository.stores
			validation := newGeneratedRouteSuccessorValidation(t, migration)
			owner, candidateMux := newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t, validation,
				generatedDomainCandidateOptions(identity.options), 4)
			predecessorReservation, predecessorExecution, predecessorIdentities :=
				prepareGeneratedRouteSequentialCandidates(t, owner, components,
					[]string{strings.Repeat("7", 64), strings.Repeat("8", 64)})
			predecessorReleased := false
			var predecessorRetirement *GeneratedPredecessorRetirement
			defer func() {
				if predecessorReleased {
					return
				}
				if predecessorRetirement != nil {
					predecessorRetirement.Leave()
				}
				predecessorExecution.Leave()
				predecessorReservation.Release()
				_ = migration.releaseEffects()
			}()
			predecessorByName := generatedRouteColdAccountingIdentityMap(t, components, predecessorIdentities)

			store, err := newStateStore(identity.dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			_, _, ingressIP := ingressNetworkCandidate(0)
			ingress := &ingressRunner{appID: migration.guarded.appID,
				network: predecessorByName["api"].NetworkName, endpoint: generatedRouteEndpoint(predecessorByName["api"]),
				caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
					GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
				ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
					Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
			physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
				slot:      generatedidentity.Slot(predecessorByName["api"].Slot),
				endpoints: make(map[string]generatedRouteEndpointSnapshot)}
			predecessorEndpoints := make([]generatedroute.RouteEndpoint, 0, len(components))
			for _, component := range components {
				value := predecessorByName[component]
				endpoint := generatedRouteEndpoint(value)
				predecessorEndpoints = append(predecessorEndpoints, endpoint)
				physicalRoutes.endpoints[value.ContainerID] = generatedRouteEndpointSnapshot{
					appID: migration.guarded.appID, endpoint: endpoint, slot: generatedidentity.Slot(value.Slot)}
			}
			routeRunner := &generatedDomainTwoComponentRouteRunner{
				daemonID: "generated-cold-runtime-created-prefix-seed", delegate: physicalRoutes}
			routeReader := &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes, delegate: routeRunner}
			manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
				database: identity.database, stores: identity.stores, runner: routeReader, options: identity.options,
			}, store)
			if err != nil {
				t.Fatal(err)
			}
			physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores,
				routeReader, identity.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
				t.Fatal(err)
			}
			routes, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
			if err != nil {
				t.Fatal(err)
			}
			predecessorSealed, err := routes.Pin(migration.guarded.context, predecessorExecution,
				generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
					ToSlot: generatedidentity.Slot(predecessorByName["api"].Slot), Endpoints: predecessorEndpoints})
			if err != nil {
				t.Fatal(err)
			}
			activated, predecessorRetirement, err := predecessorSealed.ActivateWithPredecessorRetirement(
				migration.guarded.context)
			if err != nil || predecessorRetirement == nil || activated.Phase != generatedruntimestate.PhaseDraining {
				t.Fatalf("activate served two-component predecessor state=%#v retirement=%#v error=%v",
					activated, predecessorRetirement, err)
			}
			if err := predecessorRetirement.Complete(context.Background()); err != nil {
				t.Fatalf("complete served two-component predecessor: %v", err)
			}
			predecessorHead, err := generatedruntimestate.New(identity.database).Active(context.Background(),
				migration.guarded.appID)
			if err != nil || predecessorHead.DeploymentID != migration.request.DeploymentID ||
				predecessorHead.ActivationReceiptID == "" {
				t.Fatalf("served two-component predecessor head=%#v error=%v", predecessorHead, err)
			}
			runtimeState := generatedruntimestate.New(identity.database)
			predecessorJob, err := migration.jobService.Get(migration.claim.JobID())
			if err != nil || predecessorJob.Status != string(jobs.Succeeded) {
				t.Fatalf("served predecessor completion job=%#v error=%v", predecessorJob, err)
			}
			predecessorRetirement.Leave()
			predecessorExecution.Leave()
			predecessorReservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			predecessorReleased = true

			successorScope, releaseSuccessor, err := deploymenteffects.AcquireScoped(context.Background(),
				identity.options.WorkingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			successorEffectsReleased := false
			defer func() {
				if !successorEffectsReleased {
					_ = releaseSuccessor()
				}
			}()
			t.Cleanup(func() {
				if err := releaseSuccessor(); err != nil {
					t.Errorf("release sequential-prefix successor effects: %v", err)
				}
			})
			restartedGuarded := *migration.guarded
			restartedGuarded.context = deploymenteffects.WithScope(context.Background(), successorScope)
			restartedMigration := *migration
			restartedMigration.guarded = &restartedGuarded
			restartedMigration.releaseEffects = releaseSuccessor
			successorMigration := newGeneratedRouteColdAccountingMultiSuccessorMigration(t,
				&restartedMigration, components)
			successorValidation := newGeneratedRouteSuccessorValidation(t, successorMigration)
			owner.validation = successorValidation
			successorReservation := owner.reserve(t)
			_, originalCapacityCalls := owner.source.observed()
			successorExecution := owner.pin(t, successorReservation)
			successorReleased := false
			defer func() {
				if !successorReleased {
					successorExecution.Leave()
					successorReservation.Release()
				}
			}()
			apiInvocation, err := successorExecution.BeginCreate("api")
			if err != nil {
				t.Fatal(err)
			}
			owner.runner.containerID = strings.Repeat("9", 64)
			owner.bindInvocation(t, apiInvocation)
			apiIdentity, err := successorExecution.CreateAndRecord(apiInvocation)
			if err != nil {
				t.Fatal(err)
			}
			if apiIdentity.ComponentName != "api" || apiIdentity.ContainerID != strings.Repeat("9", 64) ||
				apiIdentity.ReceiptID == "" {
				t.Fatalf("created api prefix identity=%#v", apiIdentity)
			}
			successorAdmission := successorValidation.read(t)
			if successorAdmission.runtime.phase != generatedruntimestate.PhaseStartingCandidate ||
				len(successorAdmission.components) != 2 ||
				successorAdmission.components[0].plan.Name != "api" ||
				successorAdmission.components[0].state != generatedruntimestate.ComponentRunning ||
				successorAdmission.components[0].receipt == nil ||
				successorAdmission.components[0].receipt.id != apiIdentity.ReceiptID ||
				successorAdmission.components[0].containerID != apiIdentity.ContainerID ||
				successorAdmission.components[1].plan.Name != "frontend" ||
				successorAdmission.components[1].state != generatedruntimestate.ComponentImageReady ||
				successorAdmission.components[1].receipt != nil ||
				successorAdmission.components[1].containerName != "" ||
				successorAdmission.components[1].containerID != "" {
				t.Fatalf("durable sequential created prefix=%#v", successorAdmission)
			}
			apiPhysical := candidateMux.physical[apiIdentity.ContainerID]
			apiExpected := apiPhysical.policy.identity
			apiExpected.ContainerID = apiIdentity.ContainerID
			if !apiPhysical.exists || apiPhysical.running ||
				!generatedcontainer.MatchesConfigured(candidateOwnerPhysicalInspection(apiPhysical),
					apiExpected, apiPhysical.policy.limits) ||
				!generatedcontainer.NeverStarted(candidateOwnerPhysicalInspection(apiPhysical)) {
				t.Fatalf("created api physical prefix=%#v", apiPhysical)
			}
			for _, physical := range candidateMux.physical {
				if physical.policy.identity.DeploymentID == successorMigration.request.DeploymentID &&
					physical.policy.identity.Component == "frontend" {
					t.Fatalf("frontend unexpectedly existed in created prefix: %#v", physical)
				}
			}
			var apiCreatorAttempt, successorIntentCount int
			if err := identity.database.QueryRow(`SELECT creator_worker_attempt FROM generated_candidate_intents WHERE id=?`,
				apiIdentity.ReceiptID).Scan(&apiCreatorAttempt); err != nil {
				t.Fatal(err)
			}
			if err := identity.database.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`,
				successorMigration.request.DeploymentID).Scan(&successorIntentCount); err != nil {
				t.Fatal(err)
			}
			if apiCreatorAttempt != successorMigration.claim.Attempt() || successorIntentCount != 1 {
				t.Fatalf("created prefix provenance attempt=%d want=%d intents=%d",
					apiCreatorAttempt, successorMigration.claim.Attempt(), successorIntentCount)
			}

			successorExecution.Leave()
			successorReservation.Release()
			successorReleased = true
			if err := releaseSuccessor(); err != nil {
				t.Fatal(err)
			}
			successorEffectsReleased = true
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database,
				time.Now().UTC(), successorMigration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover sequential created-prefix job: result=%+v err=%v", recovered, err)
			}
			var pausedAttempt int
			runGeneratedColdBoundaryWorker(t, successorMigration.jobService,
				generatedColdBoundaryExecutor(func(_ context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					pausedAttempt = job.Attempt
					return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
						PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
				}), successorMigration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)
			if pausedAttempt <= apiCreatorAttempt {
				t.Fatalf("recovered created-prefix attempt=%d original=%d", pausedAttempt, apiCreatorAttempt)
			}

			stateBefore, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			activeBefore, found := stateBefore.Active[migration.guarded.appID]
			predecessorRoute := routeRecord{Slot: generatedidentity.Slot(predecessorByName["api"].Slot),
				Endpoints: predecessorEndpoints}
			if stateBefore.Pending != nil || !found || !sameRoute(activeBefore, predecessorRoute) {
				t.Fatalf("served predecessor route before sequential-prefix recovery=%#v", stateBefore)
			}
			startupBefore, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(startupBefore.Runtime.ActiveHeads) != 1 || len(startupBefore.Runtime.RouteActivations) != 1 {
				t.Fatalf("sequential-prefix baseline head/history=%#v/%#v",
					startupBefore.Runtime.ActiveHeads, startupBefore.Runtime.RouteActivations)
			}
			for _, activation := range startupBefore.Runtime.RouteActivations {
				if activation.SuccessorDeploymentID == successorMigration.request.DeploymentID {
					t.Fatalf("sequential created prefix already had successor activation=%#v", activation)
				}
			}
			var successorLaterReceipts int
			if err := identity.database.QueryRow(`SELECT
				(SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts r JOIN generated_route_activations a ON a.id=r.activation_receipt_id WHERE a.successor_deployment_id=?) +
				(SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts h JOIN generated_route_activations a ON a.id=h.activation_receipt_id WHERE a.successor_deployment_id=?) +
				(SELECT COUNT(*) FROM generated_route_completion_receipts c JOIN generated_route_activations a ON a.id=c.activation_receipt_id WHERE a.successor_deployment_id=?)`,
				successorMigration.request.DeploymentID, successorMigration.request.DeploymentID,
				successorMigration.request.DeploymentID).Scan(&successorLaterReceipts); err != nil {
				t.Fatal(err)
			}
			if successorLaterReceipts != 0 {
				t.Fatalf("sequential created prefix had 043-045 rows=%d", successorLaterReceipts)
			}
			predecessorPhysical := generatedRouteColdAccountingPhysicalRoster(candidateMux, predecessorIdentities)
			processRunner, construction := generatedRouteColdAccountingPartialRestartProcess(t, identity, store,
				physicalRoutes, candidateMux, append(append([]CandidateIdentity(nil), predecessorIdentities...), apiIdentity))
			processRunner.routeWriteDirectory = identity.options.WorkingDirectory
			process := &generatedColdCreatedCandidateStartRunner{delegate: processRunner, candidate: candidateMux,
				containerID: apiIdentity.ContainerID}
			census := construction.delegate
			if census == nil {
				t.Fatalf("sequential created-prefix census=%T", construction.delegate)
			}
			var successorAPIContainers, successorFrontendContainers int
			for _, object := range census.objects {
				if object.kind != "container" || object.labels["io.rig.deployment"] != successorMigration.request.DeploymentID {
					continue
				}
				switch object.labels["io.rig.component"] {
				case "api":
					successorAPIContainers++
				case "frontend":
					successorFrontendContainers++
				}
			}
			if successorAPIContainers != 1 || successorFrontendContainers != 0 {
				t.Fatalf("sequential created-prefix census api=%d frontend=%d",
					successorAPIContainers, successorFrontendContainers)
			}
			admissionBefore := successorValidation.read(t)
			apiPhysicalBefore := candidateMux.physical[apiIdentity.ContainerID]
			liveBefore := append([]byte(nil), ingress.liveConfig...)
			restartBefore := append([]byte(nil), ingress.files[caddyContainerName+":/config/active.json"]...)
			if len(liveBefore) == 0 || !bytes.Equal(liveBefore, restartBefore) {
				t.Fatal("served two-component predecessor lacked exact live/restart route")
			}
			createBefore := owner.runner.count(candidateOwnerCreate)
			startBefore := owner.runner.count(candidateOwnerStart)
			stopBefore := owner.runner.count(candidateOwnerStop)
			removeBefore := owner.runner.count(candidateOwnerRemove)

			domain, constructErr := NewGeneratedDomain(context.Background(), identity.stores, process,
				&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			startupAfter, snapshotErr := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			stateAfter, stateErr := store.load()
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			admissionAfter := successorValidation.read(t)
			apiPhysicalAfter := candidateMux.physical[apiIdentity.ContainerID]
			activeHeadAfter, headErr := runtimeState.Active(context.Background(), migration.guarded.appID)
			if headErr != nil || !reflect.DeepEqual(activeHeadAfter, predecessorHead) ||
				!reflect.DeepEqual(startupBefore, startupAfter) || !reflect.DeepEqual(stateBefore, stateAfter) ||
				!reflect.DeepEqual(admissionBefore, admissionAfter) ||
				!reflect.DeepEqual(apiPhysicalBefore, apiPhysicalAfter) ||
				!reflect.DeepEqual(predecessorPhysical,
					generatedRouteColdAccountingPhysicalRoster(candidateMux, predecessorIdentities)) ||
				!bytes.Equal(liveBefore, ingress.liveConfig) ||
				!bytes.Equal(restartBefore, ingress.files[caddyContainerName+":/config/active.json"]) ||
				owner.runner.count(candidateOwnerCreate) != createBefore ||
				owner.runner.count(candidateOwnerStart) != startBefore ||
				owner.runner.count(candidateOwnerStop) != stopBefore ||
				owner.runner.count(candidateOwnerRemove) != removeBefore {
				t.Fatalf("cold construction changed sequential created prefix: head=%t startup=%t gateway=%t admission=%t api=%t predecessor=%t live=%t restart=%t commands=%d/%d/%d/%d want=%d/%d/%d/%d",
					reflect.DeepEqual(activeHeadAfter, predecessorHead), reflect.DeepEqual(startupBefore, startupAfter),
					reflect.DeepEqual(stateBefore, stateAfter), reflect.DeepEqual(admissionBefore, admissionAfter),
					reflect.DeepEqual(apiPhysicalBefore, apiPhysicalAfter),
					reflect.DeepEqual(predecessorPhysical,
						generatedRouteColdAccountingPhysicalRoster(candidateMux, predecessorIdentities)),
					bytes.Equal(liveBefore, ingress.liveConfig),
					bytes.Equal(restartBefore, ingress.files[caddyContainerName+":/config/active.json"]),
					owner.runner.count(candidateOwnerCreate), owner.runner.count(candidateOwnerStart),
					owner.runner.count(candidateOwnerStop), owner.runner.count(candidateOwnerRemove),
					createBefore, startBefore, stopBefore, removeBefore)
			}
			if constructErr != nil {
				message := "generated domain construction was refused: stage=cold_construction reason=cold_group_container_attestation"
				assertGeneratedDomainConstructionDiagnostic(t, constructErr,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerAttestation, message)
				if domain != nil || generatedColdBoundaryCapacityProbes(construction.snapshot()) != 0 {
					t.Fatalf("refused sequential created-prefix construction published domain=%v or probed capacity: commands=%#v",
						domain, construction.snapshot())
				}
				t.Fatalf("authenticated StartingCandidate prefix (api durable Running/configured NeverStarted, frontend ImageReady/absent) must reconstruct without new capacity; current base refused at the exact cold container boundary: %v",
					constructErr)
			}
			if domain == nil || domain.construction == nil || domain.construction.cold == nil ||
				len(domain.construction.cold.groups) != 2 {
				t.Fatalf("sequential created-prefix cold domain=%#v", domain)
			}
			if probes := generatedColdBoundaryCapacityProbes(construction.snapshot()); probes != 0 {
				t.Fatalf("sequential created-prefix capacity probes=%d want 0 commands=%#v",
					probes, construction.snapshot())
			}
			manifestByDeployment := make(map[string]generatedDomainColdManifestGroup, 2)
			for _, group := range domain.construction.cold.groups {
				manifestByDeployment[group.deploymentID] = group
			}
			predecessorManifest, predecessorFound := manifestByDeployment[migration.request.DeploymentID]
			successorManifest, successorFound := manifestByDeployment[successorMigration.request.DeploymentID]
			if !predecessorFound || !successorFound || len(predecessorManifest.components) != 2 ||
				len(successorManifest.components) != 2 ||
				predecessorManifest.components[0].name != "api" ||
				predecessorManifest.components[0].receiptID != predecessorByName["api"].ReceiptID ||
				predecessorManifest.components[1].name != "frontend" ||
				predecessorManifest.components[1].receiptID != predecessorByName["frontend"].ReceiptID ||
				successorManifest.components[0].name != "api" ||
				successorManifest.components[0].receiptID != apiIdentity.ReceiptID ||
				successorManifest.components[1].name != "frontend" || successorManifest.components[1].receiptID != "" {
				t.Fatalf("sequential created-prefix manifests predecessor=%#v successor=%#v",
					predecessorManifest, successorManifest)
			}
			for _, component := range predecessorManifest.components {
				if !component.charged || !component.retained {
					t.Fatalf("sequential created-prefix predecessor manifest component=%#v", component)
				}
			}
			if !successorManifest.components[0].charged || !successorManifest.components[0].retained ||
				!successorManifest.components[1].charged || !successorManifest.components[1].retained {
				t.Fatalf("sequential created-prefix successor manifest=%#v", successorManifest)
			}

			type expectedDebt struct {
				deploymentID   string
				component      string
				receiptID      string
				owner          candidateAccountingOwner
				physical       candidatePhysicalState
				receiptPresent bool
				explicit       bool
			}
			expectedDebts := []expectedDebt{
				{migration.request.DeploymentID, "api", predecessorByName["api"].ReceiptID,
					candidateAccountingRouteActive, candidatePhysicalHandedToRoute, true, false},
				{migration.request.DeploymentID, "frontend", predecessorByName["frontend"].ReceiptID,
					candidateAccountingRouteActive, candidatePhysicalHandedToRoute, true, false},
				{successorMigration.request.DeploymentID, "api", apiIdentity.ReceiptID,
					candidateAccountingCandidate, candidatePhysicalPresent, true, false},
				{successorMigration.request.DeploymentID, "frontend", "",
					candidateAccountingCandidate, candidatePhysicalUnused, false, true},
			}
			ledger := domain.Candidates().ledger
			var memory, disk uint64
			debts := make([]*generatedDomainCapacityDebtToken, 0, len(expectedDebts))
			charges := make([]*candidateCapacityCharge, 0, len(expectedDebts))
			for _, expected := range expectedDebts {
				ledger.mu.Lock()
				group := ledger.groups[candidateCapacityGroupKey{appID: migration.guarded.appID,
					deploymentID: expected.deploymentID}]
				ledger.mu.Unlock()
				if group == nil {
					t.Fatalf("sequential created-prefix omitted accounting group %s", expected.deploymentID)
				}
				group.mu.Lock()
				component := group.byName[expected.component]
				var receiptID string
				physical := candidatePhysicalUnused
				if component != nil && component.receipt != nil {
					receiptID = component.receipt.id
					physical = candidatePhysicalFromWord(component.receipt.state.Load())
				}
				var debt *generatedDomainCapacityDebtToken
				if component != nil && component.charge != nil {
					debt, _ = component.charge.token.(*generatedDomainCapacityDebtToken)
				}
				receiptValid := expected.receiptPresent && component != nil && component.receipt != nil &&
					receiptID == expected.receiptID && physical == expected.physical ||
					!expected.receiptPresent && component != nil && component.receipt == nil && receiptID == ""
				valid := component != nil && receiptValid &&
					component.charge != nil && component.charge.state == candidateChargeActive &&
					component.charge.owner == expected.owner && component.charge.lease == nil &&
					component.charge.receiptRetained == expected.receiptPresent &&
					component.charge.explicitRetained == expected.explicit &&
					!component.charge.logicallyRetired && component.charge.refs == 0 && !component.tokenHeld &&
					debt != nil && debt.active && debt.limits == group.limits && candidateCapacityChargeTokenPublished(debt)
				memory += group.limits.MemoryBytes
				disk += group.limits.DiskBytes
				group.mu.Unlock()
				if !valid {
					t.Fatalf("sequential created-prefix debt %s/%s=%#v", expected.deploymentID,
						expected.component, component)
				}
				debts = append(debts, debt)
				charges = append(charges, component.charge)
			}
			coordinator := domain.construction.capacity
			coordinator.mu.Lock()
			coordinatorValid := coordinator.phase == generatedDomainCapacityPublished && coordinator.borrows == 0 &&
				coordinator.memory == memory && coordinator.disk == disk && len(coordinator.tokens) == len(debts)
			for _, debt := range debts {
				_, registered := coordinator.tokens[debt]
				coordinatorValid = coordinatorValid && registered && debt.owner == coordinator
			}
			coordinator.mu.Unlock()
			if !coordinatorValid {
				t.Fatal("sequential created-prefix capacity coordinator did not retain exact four-component debt")
			}

			process.allow([]string{"container", "start", apiIdentity.ContainerID})
			resumed, err := successorMigration.jobService.Resume(successorMigration.claim.JobID())
			if err != nil || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume sequential created-prefix job=%+v err=%v", resumed, err)
			}
			frontendContainerID := strings.Repeat("a", 64)
			type creator041 struct {
				component string
				receipt   string
				job       string
				attempt   int
			}
			var freshAttempt int
			var frontendIdentity CandidateIdentity
			var successorActivationReceiptID string
			var mixed041Before []creator041
			var expectedPredecessorRosterDigest string
			var mixed041Proved bool
			activationStage := "continuation"
			var workerErr error
			runGeneratedColdBoundaryWorker(t, successorMigration.jobService,
				generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					freshAttempt = job.Attempt
					claim, err := deploymentexecution.NewClaim(successorMigration.binding, job.ID,
						successorMigration.guarded.appID, job.Attempt)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					request := GeneratedReplacementRequest(successorValidation.request)
					request.Claim = claim
					refusalReservation, err := domain.Candidates().ReserveReplacement(ctx, request)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					crossedRequest := GeneratedCandidateRequest(request)
					if crossedRequest.CandidateSlot == "blue" {
						crossedRequest.CandidateSlot = "green"
					} else {
						crossedRequest.CandidateSlot = "blue"
					}
					crossedExecution, crossedErr := domain.Candidates().PinCandidates(ctx,
						crossedRequest, refusalReservation)
					if crossedExecution != nil {
						crossedExecution.Leave()
					}
					refusalReservation.Release()
					if !errors.Is(crossedErr, deploymentexecution.ErrIdentityLost) {
						workerErr = fmt.Errorf("crossed helper pin error=%v", crossedErr)
						return jobs.ExecutionResult{}, workerErr
					}
					freshReservation, err := domain.Candidates().ReserveReplacement(ctx, request)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshReservation.Release()
					freshExecution, err := domain.Candidates().PinCandidates(ctx,
						GeneratedCandidateRequest(request), freshReservation)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshExecution.Leave()
					adopted, err := freshExecution.AdoptCandidate("api")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					adoptedPermit, ok := adopted.(*generatedCandidateInvocationPermit)
					if !ok || adoptedPermit == nil || adoptedPermit.containerID != apiIdentity.ContainerID ||
						adopted.Identity().ReceiptID != apiIdentity.ReceiptID {
						workerErr = fmt.Errorf("adopted sequential api=%T %#v", adopted, adopted.Identity())
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.StartCandidate(adopted); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}

					frontendInvocation, err := freshExecution.BeginCreate("frontend")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					frontendPermit, ok := frontendInvocation.(*generatedCandidateInvocationPermit)
					if !ok || frontendPermit == nil {
						workerErr = fmt.Errorf("frontend invocation=%T", frontendInvocation)
						return jobs.ExecutionResult{}, workerErr
					}
					owner.runner.mu.Lock()
					owner.runner.containerID = frontendContainerID
					owner.runner.physical = candidateOwnerPhysical{policy: frontendPermit.policy}
					owner.runner.mu.Unlock()
					createArgs, err := frontendPermit.policy.createArgs("")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					for _, args := range [][]string{
						{"image", "inspect", "--format", generatedCandidateImageInspectFormat,
							frontendPermit.policy.identity.ImageContentID},
						{"network", "inspect", "--format", generatedCandidateNetworkInspectFormat,
							frontendPermit.policy.identity.NetworkName},
						{"container", "inspect", "--format", generatedcontainer.InspectFormat,
							frontendPermit.policy.identity.ContainerName},
						createArgs,
						{"container", "inspect", "--format", generatedcontainer.InspectFormat, frontendContainerID},
						{"container", "start", frontendContainerID},
					} {
						process.allow(args)
					}
					frontendIdentity, err = freshExecution.CreateAndRecord(frontendInvocation)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if frontendIdentity.ContainerID != frontendContainerID ||
						frontendIdentity.ContainerID == apiIdentity.ContainerID || frontendIdentity.ReceiptID == "" {
						workerErr = fmt.Errorf("created sequential frontend=%#v", frontendIdentity)
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.StartCandidate(frontendInvocation); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					starting, err := freshExecution.FinishStarting()
					if err != nil || starting.Phase != generatedruntimestate.PhaseWaitingHealth {
						workerErr = errors.Join(err, fmt.Errorf("finish sequential starting phase=%s", starting.Phase))
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.WaitHealthyAndRecord(adopted); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if err := freshExecution.WaitHealthyAndRecord(frontendInvocation); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if _, err := freshExecution.FinishHealth(); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					rows, err := identity.database.QueryContext(ctx, `SELECT component_name,id,creator_job_id,
						creator_worker_attempt FROM generated_candidate_intents
						WHERE deployment_id=? ORDER BY component_name`, successorMigration.request.DeploymentID)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					creators := make([]creator041, 0, 2)
					for rows.Next() {
						var value creator041
						if err := rows.Scan(&value.component, &value.receipt, &value.job, &value.attempt); err != nil {
							_ = rows.Close()
							workerErr = err
							return jobs.ExecutionResult{}, err
						}
						creators = append(creators, value)
					}
					rowsErr := rows.Err()
					closeErr := rows.Close()
					if rowsErr != nil || closeErr != nil {
						workerErr = errors.Join(rowsErr, closeErr)
						return jobs.ExecutionResult{}, workerErr
					}
					mixed041Proved = len(creators) == 2 && creators[0] == (creator041{
						component: "api", receipt: apiIdentity.ReceiptID,
						job: job.ID, attempt: apiCreatorAttempt,
					}) && creators[1] == (creator041{
						component: "frontend", receipt: frontendIdentity.ReceiptID,
						job: job.ID, attempt: job.Attempt,
					}) && job.Attempt > apiCreatorAttempt
					if !mixed041Proved {
						workerErr = fmt.Errorf("pre-activation mixed 041 roster=%#v current=%s/%d original=%d",
							creators, job.ID, job.Attempt, apiCreatorAttempt)
						return jobs.ExecutionResult{}, workerErr
					}
					mixed041Before = append([]creator041(nil), creators...)
					activationStage = "observe"
					predecessor, err := domain.Gateway().Observe(ctx, migration.guarded.appID)
					if err != nil || predecessor.Slot != generatedidentity.Slot(predecessorByName["api"].Slot) {
						workerErr = errors.Join(err, fmt.Errorf("observe sequential predecessor slot=%s", predecessor.Slot))
						return jobs.ExecutionResult{}, workerErr
					}
					successorEndpoints := []generatedroute.RouteEndpoint{
						generatedRouteEndpoint(apiIdentity), generatedRouteEndpoint(frontendIdentity),
					}
					for _, value := range []CandidateIdentity{apiIdentity, frontendIdentity} {
						endpoint := generatedRouteEndpoint(value)
						physicalRoutes.endpoints[value.ContainerID] = generatedRouteEndpointSnapshot{
							appID: migration.guarded.appID, endpoint: endpoint,
							slot: generatedidentity.Slot(value.Slot),
						}
					}
					physicalRoutes.network = apiIdentity.NetworkName
					physicalRoutes.endpoint = successorEndpoints[0]
					physicalRoutes.slot = generatedidentity.Slot(apiIdentity.Slot)
					activationStage = "pin"
					sealed, err := domain.Routes().Pin(ctx, freshExecution, generatedroute.RouteSwitchRequest{
						AppID: migration.guarded.appID, FromSlot: predecessor.Slot,
						ToSlot: generatedidentity.Slot(apiIdentity.Slot), Endpoints: successorEndpoints,
					})
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if len(sealed.predecessorRoster) != 2 {
						workerErr = fmt.Errorf("sealed sequential predecessor roster=%#v", sealed.predecessorRoster)
						return jobs.ExecutionResult{}, workerErr
					}
					expectedPredecessorRosterDigest = generatedRouteActivationRosterDigest(sealed.predecessorRoster)
					activationStage = "activate"
					activated, retirement, err := sealed.ActivateWithPredecessorRetirement(ctx)
					if err != nil || retirement == nil || activated.Phase != generatedruntimestate.PhaseDraining {
						workerErr = fmt.Errorf("stage=%s mixed041=%t: %w", activationStage, mixed041Proved,
							errors.Join(err, fmt.Errorf("activate sequential successor phase=%s retirement=%v",
								activated.Phase, retirement != nil)))
						return jobs.ExecutionResult{}, workerErr
					}
					defer retirement.Leave()
					successorActivationReceiptID = retirement.state.receipt.ReceiptID
					if successorActivationReceiptID == "" {
						workerErr = errors.New("sequential successor activation omitted receipt")
						return jobs.ExecutionResult{}, workerErr
					}
					activationStage = "complete"
					if err := retirement.Complete(ctx); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					activationStage = "done"
					return jobs.ExecutionResult{}, nil
				}), successorMigration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory,
				func() string { return boundedErrorText(workerErr) })
			if workerErr != nil {
				t.Fatalf("fresh sequential created-prefix worker stage=%s mixed041=%t err=%s",
					activationStage, mixed041Proved, boundedErrorText(workerErr))
			}
			if freshAttempt <= apiCreatorAttempt || freshAttempt <= pausedAttempt {
				t.Fatalf("fresh sequential attempt=%d paused=%d original=%d",
					freshAttempt, pausedAttempt, apiCreatorAttempt)
			}
			var frontendCreatorAttempt int
			if err := identity.database.QueryRow(`SELECT creator_worker_attempt FROM generated_candidate_intents WHERE id=?`,
				frontendIdentity.ReceiptID).Scan(&frontendCreatorAttempt); err != nil {
				t.Fatal(err)
			}
			var apiCreatorAttemptAfter, intentCountAfter int
			if err := identity.database.QueryRow(`SELECT creator_worker_attempt FROM generated_candidate_intents WHERE id=?`,
				apiIdentity.ReceiptID).Scan(&apiCreatorAttemptAfter); err != nil {
				t.Fatal(err)
			}
			if err := identity.database.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`,
				successorMigration.request.DeploymentID).Scan(&intentCountAfter); err != nil {
				t.Fatal(err)
			}
			rowsAfter, err := identity.database.Query(`SELECT component_name,id,creator_job_id,
				creator_worker_attempt FROM generated_candidate_intents
				WHERE deployment_id=? ORDER BY component_name`, successorMigration.request.DeploymentID)
			if err != nil {
				t.Fatal(err)
			}
			mixed041After := make([]creator041, 0, 2)
			for rowsAfter.Next() {
				var value creator041
				if err := rowsAfter.Scan(&value.component, &value.receipt, &value.job, &value.attempt); err != nil {
					rowsAfter.Close()
					t.Fatal(err)
				}
				mixed041After = append(mixed041After, value)
			}
			rowsAfterErr := rowsAfter.Err()
			rowsAfterCloseErr := rowsAfter.Close()
			if rowsAfterErr != nil || rowsAfterCloseErr != nil {
				t.Fatal(errors.Join(rowsAfterErr, rowsAfterCloseErr))
			}
			_, capacityCallsAfter := owner.source.observed()
			apiRunning := candidateMux.physical[apiIdentity.ContainerID]
			frontendRunning := candidateMux.physical[frontendIdentity.ContainerID]
			apiRunningExpected := apiRunning.policy.identity
			apiRunningExpected.ContainerID = apiIdentity.ContainerID
			frontendRunningExpected := frontendRunning.policy.identity
			frontendRunningExpected.ContainerID = frontendIdentity.ContainerID
			finalState, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			finalRoute, finalRouteFound := finalState.Active[migration.guarded.appID]
			wantFinalRoute := routeRecord{Slot: generatedidentity.Slot(apiIdentity.Slot),
				Endpoints: []generatedroute.RouteEndpoint{
					generatedRouteEndpoint(apiIdentity), generatedRouteEndpoint(frontendIdentity),
				}}
			if apiCreatorAttemptAfter != apiCreatorAttempt || intentCountAfter != 2 ||
				frontendCreatorAttempt != freshAttempt || frontendCreatorAttempt == apiCreatorAttempt ||
				!reflect.DeepEqual(mixed041After, mixed041Before) ||
				!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(apiRunning),
					apiRunningExpected, apiRunning.policy.limits) ||
				!generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(apiRunning)) ||
				!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(frontendRunning),
					frontendRunningExpected, frontendRunning.policy.limits) ||
				!generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(frontendRunning)) ||
				owner.runner.count(candidateOwnerCreate) != createBefore+1 ||
				owner.runner.count(candidateOwnerStart) != startBefore+2 ||
				owner.runner.count(candidateOwnerStop) != stopBefore+len(predecessorIdentities) ||
				owner.runner.count(candidateOwnerRemove) != removeBefore+len(predecessorIdentities) ||
				capacityCallsAfter != originalCapacityCalls ||
				generatedColdBoundaryCapacityProbes(construction.snapshot()) != 0 ||
				successorActivationReceiptID == "" || finalState.Pending != nil || !finalRouteFound ||
				!sameRoute(finalRoute, wantFinalRoute) {
				t.Fatalf("fresh sequential continuation effects/provenance: attempts=%d/%d apiAfter=%d intents=%d running=%t/%t commands=%d/%d/%d/%d capacity=%d/%d probes=%d",
					apiCreatorAttempt, frontendCreatorAttempt, apiCreatorAttemptAfter, intentCountAfter,
					apiRunning.running, frontendRunning.running,
					owner.runner.count(candidateOwnerCreate), owner.runner.count(candidateOwnerStart),
					owner.runner.count(candidateOwnerStop), owner.runner.count(candidateOwnerRemove),
					originalCapacityCalls, capacityCallsAfter, generatedColdBoundaryCapacityProbes(construction.snapshot()))
			}

			finalSnapshot, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			candidateRoster, err := runtimeState.RouteActivationRoster(context.Background(),
				migration.guarded.appID, successorMigration.request.DeploymentID,
				generatedruntimestate.ComponentActive)
			if err != nil || len(candidateRoster) != 2 ||
				candidateRoster[0].ComponentName != "api" ||
				candidateRoster[0].CandidateReceiptID != apiIdentity.ReceiptID ||
				candidateRoster[1].ComponentName != "frontend" ||
				candidateRoster[1].CandidateReceiptID != frontendIdentity.ReceiptID {
				t.Fatalf("final sequential candidate roster=%#v error=%v", candidateRoster, err)
			}
			var activation generatedruntimestate.GeneratedDomainStartupRouteActivation
			activationCount := 0
			for _, value := range finalSnapshot.Runtime.RouteActivations {
				if value.SuccessorDeploymentID == successorMigration.request.DeploymentID {
					activation, activationCount = value, activationCount+1
				}
			}
			if activationCount != 1 || activation.ReceiptID != successorActivationReceiptID ||
				activation.CreatorJobID != successorMigration.claim.JobID() ||
				activation.CreatorWorkerAttempt != apiCreatorAttempt ||
				activation.WriterJobID != successorMigration.claim.JobID() ||
				activation.WriterWorkerAttempt != freshAttempt ||
				activation.CandidateRosterDigest != generatedRouteActivationRosterDigest(candidateRoster) ||
				activation.PredecessorRosterDigest != expectedPredecessorRosterDigest ||
				activation.PredecessorDeploymentID != predecessorHead.DeploymentID ||
				activation.PredecessorActivationReceiptID != predecessorHead.ActivationReceiptID {
				t.Fatalf("final sequential activation=%#v count=%d", activation, activationCount)
			}
			retiredComponents := make(map[string]generatedruntimestate.PredecessorRetirementObservation, 2)
			for _, value := range finalSnapshot.Runtime.PredecessorRetirementComponents {
				if value.ActivationReceiptID == successorActivationReceiptID {
					retiredComponents[value.ComponentName] = value
				}
			}
			for _, component := range components {
				observed, found := retiredComponents[component]
				want := predecessorByName[component]
				var policyDigest string
				if err := identity.database.QueryRow(`SELECT candidate_policy_digest
					FROM generated_candidate_intents WHERE id=?`, want.ReceiptID).Scan(&policyDigest); err != nil {
					t.Fatal(err)
				}
				if !found || observed.CandidateReceiptID != want.ReceiptID ||
					observed.ContainerID != want.ContainerID || observed.CandidatePolicyDigest != policyDigest {
					t.Fatalf("final sequential 043 component %s=%#v found=%t", component, observed, found)
				}
			}
			var retirementReceipt generatedruntimestate.PredecessorRetirementReceipt
			var authorization generatedruntimestate.RouteAccountingHandoffAuthorization
			var handoff generatedruntimestate.RouteAccountingHandoffReceipt
			var completion generatedruntimestate.RouteCompletionReceipt
			for _, value := range finalSnapshot.Runtime.PredecessorRetirementReceipts {
				if value.ActivationReceiptID == successorActivationReceiptID {
					retirementReceipt = value
				}
			}
			for _, value := range finalSnapshot.Runtime.AccountingHandoffAuthorizations {
				if value.ActivationReceiptID == successorActivationReceiptID {
					authorization = value
				}
			}
			for _, value := range finalSnapshot.Runtime.AccountingHandoffReceipts {
				if value.ActivationReceiptID == successorActivationReceiptID {
					handoff = value
				}
			}
			for _, value := range finalSnapshot.Runtime.RouteCompletionReceipts {
				if value.ActivationReceiptID == successorActivationReceiptID {
					completion = value
				}
			}
			if retirementReceipt.ActivationReceiptID == "" || retirementReceipt.ComponentCount != 2 ||
				retirementReceipt.AppID != migration.guarded.appID ||
				retirementReceipt.SuccessorDeploymentID != successorMigration.request.DeploymentID ||
				retirementReceipt.PredecessorDeploymentID != predecessorHead.DeploymentID ||
				retirementReceipt.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
				authorization.ActivationReceiptID == "" || authorization.SuccessorComponentCount != 2 ||
				authorization.PredecessorComponentCount != 2 ||
				authorization.AppID != migration.guarded.appID ||
				authorization.SuccessorDeploymentID != successorMigration.request.DeploymentID ||
				authorization.PredecessorDeploymentID != predecessorHead.DeploymentID ||
				authorization.CandidateRosterDigest != activation.CandidateRosterDigest ||
				authorization.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
				handoff.ActivationReceiptID == "" || handoff.AppID != migration.guarded.appID ||
				handoff.SuccessorDeploymentID != successorMigration.request.DeploymentID ||
				handoff.PredecessorDeploymentID != predecessorHead.DeploymentID ||
				handoff.SuccessorComponentCount != 2 || handoff.PredecessorComponentCount != 2 ||
				handoff.CandidateRosterDigest != activation.CandidateRosterDigest ||
				handoff.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
				completion.ActivationReceiptID == "" || completion.CreatorJobID != successorMigration.claim.JobID() ||
				completion.CreatorWorkerAttempt != apiCreatorAttempt ||
				completion.CurrentJobID != successorMigration.claim.JobID() ||
				completion.CurrentWorkerAttempt != freshAttempt || completion.AppID != migration.guarded.appID ||
				completion.SuccessorDeploymentID != successorMigration.request.DeploymentID ||
				completion.CompletionCode != "deployment_completed" {
				t.Fatalf("final sequential 043-045 retirement=%#v authorization=%#v handoff=%#v completion=%#v",
					retirementReceipt, authorization, handoff, completion)
			}
			finalHead, err := runtimeState.Active(context.Background(), migration.guarded.appID)
			if err != nil || finalHead.DeploymentID != successorMigration.request.DeploymentID ||
				finalHead.ActivationReceiptID != successorActivationReceiptID ||
				finalHead.Generation != predecessorHead.Generation+1 {
				t.Fatalf("final sequential active head=%#v error=%v", finalHead, err)
			}
			terminalAccounting := true
			for index, expected := range expectedDebts {
				ledger.mu.Lock()
				group := ledger.groups[candidateCapacityGroupKey{appID: migration.guarded.appID,
					deploymentID: expected.deploymentID}]
				ledger.mu.Unlock()
				if group == nil {
					terminalAccounting = false
					continue
				}
				group.mu.Lock()
				component := group.byName[expected.component]
				if index < 2 {
					terminalAccounting = terminalAccounting && component != nil && component.charge == charges[index] &&
						charges[index].state == candidateChargeReleased &&
						charges[index].owner == candidateAccountingPredecessorRetired &&
						charges[index].logicallyRetired
				} else {
					terminalAccounting = terminalAccounting && component != nil && component.charge == charges[index] &&
						charges[index].state == candidateChargeActive &&
						charges[index].owner == candidateAccountingRouteActive &&
						!charges[index].logicallyRetired
				}
				group.mu.Unlock()
			}
			coordinator.mu.Lock()
			terminalAccounting = terminalAccounting && coordinator.phase == generatedDomainCapacityPublished &&
				coordinator.borrows == 0 && len(coordinator.tokens) == 2
			for index, debt := range debts {
				_, registered := coordinator.tokens[debt]
				terminalAccounting = terminalAccounting && (index < 2 && !registered && !debt.active ||
					index >= 2 && registered && debt.active)
			}
			coordinator.mu.Unlock()
			if !terminalAccounting {
				t.Fatal("final sequential accounting did not release predecessor and retain exact successor debt")
			}

			// This is deliberately a lower-layer post-completion replay of the
			// production startup readers and cold attestations. A second public New
			// in this process would only join the singleton and would not prove a
			// fresh-process reconstruction.
			attemptBeforeReplay := freshAttempt
			createReplay, startReplay := owner.runner.count(candidateOwnerCreate), owner.runner.count(candidateOwnerStart)
			stopReplay, removeReplay := owner.runner.count(candidateOwnerStop), owner.runner.count(candidateOwnerRemove)
			presentContainers := make([]string, 0, 2)
			for id, physical := range candidateMux.physical {
				if physical.exists {
					presentContainers = append(presentContainers, id)
				}
			}
			sort.Strings(presentContainers)
			expectedPresent := []string{apiIdentity.ContainerID, frontendIdentity.ContainerID}
			sort.Strings(expectedPresent)
			predecessorAbsent := true
			for _, identity := range predecessorIdentities {
				physical, found := candidateMux.physical[identity.ContainerID]
				if !found || physical.exists {
					predecessorAbsent = false
				}
			}
			if !reflect.DeepEqual(presentContainers, expectedPresent) || !predecessorAbsent {
				t.Fatalf("lower-layer completed replay physical census=%v want=%v predecessorAbsent=%t",
					presentContainers, expectedPresent, predecessorAbsent)
			}
			successorIdentities := []CandidateIdentity{apiIdentity, frontendIdentity}
			successorPhysical := generatedRouteColdAccountingPhysicalRoster(candidateMux, successorIdentities)
			if len(successorPhysical) != 2 {
				t.Fatalf("lower-layer completed replay successor physical=%#v", successorPhysical)
			}
			for index, physical := range successorPhysical {
				want := successorIdentities[index]
				expected := physical.policy.identity
				expected.ContainerID = want.ContainerID
				if !physical.exists || !physical.running || physical.containerID != want.ContainerID ||
					physical.policy.identity.ReceiptID != want.ReceiptID || physical.policy.identity.Component != want.ComponentName ||
					!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(physical), expected,
						physical.policy.limits) || !generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(physical)) {
					t.Fatalf("lower-layer completed replay successor %s physical=%#v identity=%#v",
						want.ComponentName, physical, want)
				}
			}
			builderRunner, ok := processRunner.cold.(*generatedDomainTwoComponentRouteRunner)
			if !ok || builderRunner == nil || builderRunner.builder == nil || construction.delegate == nil {
				t.Fatalf("lower-layer completed replay immutable runner binding cold=%T builder=%v census=%v",
					processRunner.cold, builderRunner != nil && builderRunner.builder != nil,
					construction.delegate != nil)
			}
			replayObjects := make([]generatedDomainDockerObject, 0, len(construction.delegate.objects))
			for _, object := range construction.delegate.objects {
				if object.kind == "container" && object.labels["io.rig.application"] == migration.guarded.appID {
					continue
				}
				replayObjects = append(replayObjects, cloneGeneratedDomainColdRetainedGatewayObject(object))
			}
			for _, object := range generatedDomainA0ApplicationObjects(t, successorPhysical) {
				if object.kind == "container" {
					replayObjects = append(replayObjects, object)
					continue
				}
				matched := false
				for _, retained := range replayObjects {
					if retained.kind != object.kind || object.kind == "image" && retained.id != object.id ||
						object.kind == "network" && retained.name != object.name {
						continue
					}
					matched = true
					if !reflect.DeepEqual(retained, object) {
						t.Fatalf("lower-layer completed replay crossed retained %s object: retained=%#v current=%#v",
							object.kind, retained, object)
					}
					break
				}
				if !matched {
					replayObjects = append(replayObjects, object)
				}
			}
			replayCensus := &generatedDomainRecoveryCensusRunner{daemonID: construction.delegate.daemonID,
				objects: replayObjects, imageID: construction.delegate.imageID, gateway: construction.delegate.gateway}
			replayConstruction := &generatedDomainConstructionRunner{roster: candidateMux, delegate: replayCensus,
				capacity: append([]byte(nil), construction.capacity...)}
			replayBuilder := &generatedDomainTwoComponentRouteRunner{delegate: replayConstruction,
				builder: builderRunner.builder}
			replayProcess := &generatedRouteColdAccountingRunner{cold: replayBuilder, routes: physicalRoutes,
				routeWriteDirectory: identity.options.WorkingDirectory, predecessor: candidateMux,
				predecessorPhysical: successorPhysical}
			observedReplay, err := inspectGeneratedDomainStartupObservation(context.Background(), identity.stores,
				replayProcess, identity.options)
			if err != nil {
				t.Fatalf("lower-layer completed replay observation: %v", err)
			}
			groupsReplay, err := readGeneratedDomainColdGroups(context.Background(), domain.Candidates(), observedReplay)
			if err != nil {
				t.Fatalf("lower-layer completed replay groups: %v", err)
			}
			var retainedGateway generatedDomainColdRetainedGatewayProof
			var retainedV2 generatedDomainColdRetainedV2Proof
			if err := attestGeneratedDomainColdGroupsWithPendingProofs(context.Background(), domain.Gateway(),
				domain.Candidates(), observedReplay, groupsReplay, nil, &retainedGateway, &retainedV2); err != nil {
				t.Fatalf("lower-layer completed replay attestation: %v", err)
			}
			replayedSnapshot, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var attemptAfterReplay int
			if err := identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
				successorMigration.claim.JobID()).Scan(&attemptAfterReplay); err != nil {
				t.Fatal(err)
			}
			if attemptAfterReplay != attemptBeforeReplay || !reflect.DeepEqual(replayedSnapshot, finalSnapshot) ||
				owner.runner.count(candidateOwnerCreate) != createReplay ||
				owner.runner.count(candidateOwnerStart) != startReplay ||
				owner.runner.count(candidateOwnerStop) != stopReplay ||
				owner.runner.count(candidateOwnerRemove) != removeReplay {
				t.Fatalf("lower-layer completed replay mutated history/attempt/effects: attempt=%d/%d history=%t commands=%d/%d/%d/%d want=%d/%d/%d/%d",
					attemptBeforeReplay, attemptAfterReplay, reflect.DeepEqual(replayedSnapshot, finalSnapshot),
					owner.runner.count(candidateOwnerCreate), owner.runner.count(candidateOwnerStart),
					owner.runner.count(candidateOwnerStop), owner.runner.count(candidateOwnerRemove),
					createReplay, startReplay, stopReplay, removeReplay)
			}
		})
	})
	t.Run("stopped_physical_disagreement_refuses_without_traffic", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			candidateIdentity, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			candidate.moveToWaitingHealth(t, execution)
			if err := execution.WaitHealthyAndRecord(invocation); err != nil {
				t.Fatal(err)
			}
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			stopped := candidate.runner.snapshot()
			stopped.running = false
			candidate.runner.setPhysical(stopped)

			store, err := newStateStore(identity.dataRoot)
			if err != nil || store.createInitialEmpty() != nil {
				t.Fatalf("install empty gateway state: %v", err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
			observation.V1Container.ID = strings.Repeat("d", 64)
			observation.V1NetworkID = strings.Repeat("e", 64)
			for id, member := range observation.V1Network.Containers {
				delete(observation.V1Network.Containers, id)
				observation.V1Network.Containers[observation.V1Container.ID] = member
			}
			gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
			objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
			for index := range objects {
				switch objects[index].name {
				case caddyContainerName:
					objects[index].id = observation.V1Container.ID
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver, scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver, scope: observation.V1Volume.Scope,
						options: cloneStringMap(observation.V1Volume.Options), mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := candidate.runner.snapshot().policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: candidateIdentity.ContainerID, name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-stopped", objects: objects, imageID: observation.Image.ID, gateway: gateway,
			}}
			creates, starts := candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart)
			domain, err := NewGeneratedDomain(context.Background(), identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			if err == nil || domain != nil {
				t.Fatalf("stopped uncertain candidate published domain=%v err=%v", domain, err)
			}
			if candidate.runner.count(candidateOwnerCreate) != creates || candidate.runner.count(candidateOwnerStart) != starts ||
				candidate.runner.count(candidateOwnerStop) != 0 || candidate.runner.count(candidateOwnerRemove) != 0 {
				t.Fatalf("refusal emitted candidate effects: creates=%d/%d starts=%d/%d stops=%d removes=%d", candidate.runner.count(candidateOwnerCreate), creates,
					candidate.runner.count(candidateOwnerStart), starts, candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove))
			}
			for _, command := range runner.snapshot() {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) &&
					!(len(command) >= 6 && command[0] == "container" && command[1] == "exec" && command[len(command)-1] == capacityProbeCommand) {
					t.Fatalf("stopped refusal emitted Docker/app traffic: %#v", command)
				}
				if len(command) >= 2 && command[0] == "container" && command[1] == "exec" &&
					!(len(command) >= 6 && command[2] == caddyContainerName && command[len(command)-1] == capacityProbeCommand) &&
					!generatedDomainGatewayAdminCommand(command, observation.V1Container.ID) &&
					!generatedDomainGatewayChallengeCommand(command, observation.V1Container.ID) {
					t.Fatalf("stopped refusal emitted application or unowned-listener traffic: %#v", command)
				}
			}
		})
	})
	t.Run("activation_attempt_lineage_bounds", func(t *testing.T) {
		const (
			appID        = "11111111-1111-4111-8111-111111111111"
			deploymentID = "22222222-2222-4222-8222-222222222222"
			releaseID    = "33333333-3333-4333-8333-333333333333"
			jobID        = "44444444-4444-4444-8444-444444444444"
			receiptID    = "55555555-5555-4555-8555-555555555555"
			artifactID   = "66666666-6666-4666-8666-666666666666"
			intentID     = "77777777-7777-4777-8777-777777777777"
		)
		snapshot := candidateAdmissionSnapshot{
			pins: candidateImmutablePins{jobID: jobID, releaseID: releaseID, candidateSlot: "blue"},
			headPins: candidateHeadPins{appID: appID, deploymentID: deploymentID,
				releaseID: releaseID, slot: "blue"},
			observedHead: candidateHeadSnapshot{appID: appID, deploymentID: deploymentID,
				releaseID: releaseID, slot: "blue", activationReceiptID: receiptID, generation: 1},
			components: []candidateAdmissionComponent{{
				plan: deploymentplans.Component{Name: "api"}, slot: "blue",
				containerName: "rig-test-api-blue", containerID: strings.Repeat("8", 64),
				artifact: &candidateArtifactSnapshot{id: artifactID},
				receipt:  &candidateIntentSnapshot{id: intentID, policyDigest: strings.Repeat("9", 64)},
			}},
		}
		roster, ok := generatedDomainColdActivationRoster(snapshot)
		if !ok {
			t.Fatal("canonical activation roster was rejected")
		}
		activation := generatedruntimestate.GeneratedDomainStartupRouteActivation{
			ReceiptID: receiptID, AppID: appID, SuccessorDeploymentID: deploymentID,
			SuccessorReleaseID: releaseID, SuccessorSlot: "blue", SuccessorGeneration: 1,
			CreatorJobID: jobID, CreatorWorkerAttempt: 1, WriterJobID: jobID, WriterWorkerAttempt: 1,
			CandidateRosterDigest:   generatedRouteActivationRosterDigest(roster),
			PredecessorRosterDigest: generatedRouteActivationRosterDigest(nil),
			RouteIntentDigest:       strings.Repeat("a", 64), CommittedRouteProofDigest: strings.Repeat("b", 64),
		}
		pins := candidateValidationPins{identity: candidateValidationIdentity{
			applicationID: appID, jobID: jobID, attempt: 2,
		}}
		if !generatedDomainColdActivationMatches(pins, snapshot, nil,
			[]generatedruntimestate.GeneratedDomainStartupRouteActivation{activation}) {
			t.Fatal("immutable prior activation attempt did not survive a later same-job completion attempt")
		}
		for _, test := range []struct {
			name   string
			mutate func(*generatedruntimestate.GeneratedDomainStartupRouteActivation)
		}{
			{name: "zero creator attempt", mutate: func(value *generatedruntimestate.GeneratedDomainStartupRouteActivation) {
				value.CreatorWorkerAttempt = 0
			}},
			{name: "future creator attempt", mutate: func(value *generatedruntimestate.GeneratedDomainStartupRouteActivation) {
				value.CreatorWorkerAttempt = 3
			}},
			{name: "crossed creator job", mutate: func(value *generatedruntimestate.GeneratedDomainStartupRouteActivation) {
				value.CreatorJobID = "88888888-8888-4888-8888-888888888888"
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				crossed := activation
				test.mutate(&crossed)
				if generatedDomainColdActivationMatches(pins, snapshot, nil,
					[]generatedruntimestate.GeneratedDomainStartupRouteActivation{crossed}) {
					t.Fatal("crossed activation lineage was accepted")
				}
			})
		}
	})
}

type generatedDomainColdStartingCandidatePublicFixture struct {
	identity   generatedDomainIdentityFixture
	migration  *generatedMigrationFixture
	validation *candidateValidationFixture
	owner      *candidateOwnerFixture
	candidates *generatedRouteCandidateRunner
	identities map[string]CandidateIdentity
	ordered    []CandidateIdentity
	store      *stateStore
	runner     *generatedDomainConstructionRunner
	process    runtimeprocess.CommandRunner
}

// generatedDomainColdStartingCandidateCensusRunner supplies only the detailed
// immutable image observation for a not-yet-created suffix component. The
// startup census uses a different exact format and remains owned by delegate.
type generatedDomainColdStartingCandidateCensusRunner struct {
	delegate runtimeprocess.CommandRunner
	images   map[string]candidatePolicy
}

func (r *generatedDomainColdStartingCandidateCensusRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) == 5 && reflect.DeepEqual(args[:4],
		[]string{"image", "inspect", "--format", generatedCandidateImageInspectFormat}) {
		if policy, found := r.images[args[4]]; found {
			candidate := &candidateOwnerRunner{physical: candidateOwnerPhysical{policy: policy}}
			return candidate.defaultResult(candidateOwnerImageInspect, request)
		}
	}
	return r.delegate.Run(ctx, request)
}

func newGeneratedDomainColdStartingCandidatePublicFixture(t *testing.T, createOrder []string,
	start map[string]bool,
) generatedDomainColdStartingCandidatePublicFixture {
	t.Helper()
	components := []string{"api", "frontend"}
	identity := newGeneratedDomainIdentityFixture(t)
	migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, components, components,
		generatedDomainTwoComponentPlan())
	identity.stores = migration.repository.stores
	validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
	owner, candidates := newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t, validation,
		generatedDomainCandidateOptions(identity.options), 2)
	reservation := owner.reserve(t)
	execution := owner.pin(t, reservation)
	released := false
	defer func() {
		if released {
			return
		}
		execution.Leave()
		reservation.Release()
		_ = migration.releaseEffects()
	}()

	identities := make(map[string]CandidateIdentity, len(createOrder))
	ordered := make([]CandidateIdentity, 0, len(createOrder))
	containerIDs := map[string]string{"api": strings.Repeat("7", 64), "frontend": strings.Repeat("8", 64)}
	for _, component := range createOrder {
		containerID, known := containerIDs[component]
		if !known {
			t.Fatalf("unknown starting-candidate component %q", component)
		}
		invocation, err := execution.BeginCreate(component)
		if err != nil {
			t.Fatalf("begin %s candidate create: %v", component, err)
		}
		permit, ok := invocation.(*generatedCandidateInvocationPermit)
		if !ok || permit == nil {
			t.Fatalf("%s candidate invocation=%T", component, invocation)
		}
		owner.runner.mu.Lock()
		owner.runner.containerID = containerID
		owner.runner.physical = candidateOwnerPhysical{policy: permit.policy}
		owner.runner.mu.Unlock()
		created, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatalf("create %s candidate: %v", component, err)
		}
		if start[component] {
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatalf("start %s candidate: %v", component, err)
			}
		}
		identities[component] = created
		ordered = append(ordered, created)
	}
	admission := validation.read(t)
	if admission.runtime.phase != generatedruntimestate.PhaseStartingCandidate ||
		len(admission.components) != len(components) {
		t.Fatalf("public starting-candidate prefix=%#v", admission)
	}
	execution.Leave()
	reservation.Release()
	if err := migration.releaseEffects(); err != nil {
		t.Fatal(err)
	}
	released = true
	result, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database,
		time.Now().UTC(), migration.bindings)
	if err != nil || result.Jobs.RequeuedGenerated != 1 {
		t.Fatalf("recover public starting-candidate job: result=%+v err=%v", result, err)
	}
	runGeneratedColdBoundaryWorker(t, migration.jobService,
		generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
				PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
		}), migration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)

	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatalf("install public starting-candidate gateway state: %v", err)
	}
	if err := store.createInitialEmpty(); err != nil {
		t.Fatalf("install public starting-candidate gateway state: %v", err)
	}
	state, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	_, v2, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
	observation.V1Container.ID = strings.Repeat("d", 64)
	observation.V1NetworkID = strings.Repeat("e", 64)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = observation.V1Container.ID
		case caddyNetworkName:
			objects[index].id = observation.V1NetworkID
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
		}
	}
	physical := generatedRouteColdAccountingPhysicalRoster(candidates, ordered)
	objects = append(objects, generatedDomainA0ApplicationObjects(t, physical)...)
	physicalComponents := make(map[string]struct{}, len(physical))
	for _, value := range physical {
		physicalComponents[value.policy.identity.Component] = struct{}{}
	}
	absentImages := make(map[string]candidatePolicy)
	for _, component := range admission.components {
		if _, present := physicalComponents[component.plan.Name]; present || component.artifact == nil {
			continue
		}
		artifact := *component.artifact
		policy := candidatePolicy{identity: generatedcontainer.Identity{
			AppID: validation.pins.applicationID(), ReleaseID: validation.pins.releaseID,
			DeploymentID: validation.pins.deploymentID, ArtifactID: artifact.id,
			DeploymentPlanRevisionID: validation.pins.deploymentPlanRevisionID,
			Component:                component.plan.Name, Role: component.plan.Role,
			ImageContentID: artifact.imageContentID}, buildDefinitionDigest: artifact.buildDefinitionDigest}
		if previous, duplicate := absentImages[artifact.imageContentID]; duplicate && !reflect.DeepEqual(previous, policy) {
			t.Fatal("absent starting-candidate images cross immutable policy")
		}
		absentImages[artifact.imageContentID] = policy
		objects = append(objects, generatedDomainDockerObject{kind: "image", id: artifact.imageContentID,
			labels: generatedDomainColdImageLabels(validation.pins, component, artifact),
			data:   generatedDomainDockerObjectData{contentID: artifact.imageContentID}})
	}
	identity.options.HostPort = journal.Source.LocalHostPort
	runner := &generatedDomainConstructionRunner{
		delegate: &generatedDomainRecoveryCensusRunner{daemonID: "generated-cold-starting-candidate-public",
			objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	census := &generatedDomainColdStartingCandidateCensusRunner{delegate: runner, images: absentImages}
	process := &generatedRouteColdAccountingRunner{cold: census, predecessor: candidates,
		predecessorPhysical: physical}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
		database: identity.database, stores: identity.stores, runner: process, options: identity.options,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	return generatedDomainColdStartingCandidatePublicFixture{identity: identity, migration: migration,
		validation: validation, owner: owner, candidates: candidates, identities: identities, ordered: ordered,
		store: store, runner: runner, process: process}
}

func (f generatedDomainColdStartingCandidatePublicFixture) construct(t *testing.T) (*GeneratedDomain, error) {
	t.Helper()
	startupBefore, err := f.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := f.store.load()
	if err != nil {
		t.Fatal(err)
	}
	admissionBefore := f.validation.read(t)
	physicalBefore := generatedRouteColdAccountingPhysicalRoster(f.candidates, f.ordered)
	_, capacityCallsBefore := f.owner.source.observed()
	createBefore, startBefore := f.owner.runner.count(candidateOwnerCreate), f.owner.runner.count(candidateOwnerStart)
	stopBefore, removeBefore := f.owner.runner.count(candidateOwnerStop), f.owner.runner.count(candidateOwnerRemove)
	domain, constructErr := NewGeneratedDomain(context.Background(), f.identity.stores, f.process,
		&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(f.identity.options))
	startupAfter, err := f.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := f.store.load()
	if err != nil {
		t.Fatal(err)
	}
	admissionAfter := f.validation.read(t)
	physicalAfter := generatedRouteColdAccountingPhysicalRoster(f.candidates, f.ordered)
	_, capacityCallsAfter := f.owner.source.observed()
	if !reflect.DeepEqual(startupBefore, startupAfter) || !reflect.DeepEqual(stateBefore, stateAfter) ||
		!reflect.DeepEqual(admissionBefore, admissionAfter) || !reflect.DeepEqual(physicalBefore, physicalAfter) ||
		capacityCallsAfter != capacityCallsBefore || f.owner.runner.count(candidateOwnerCreate) != createBefore ||
		f.owner.runner.count(candidateOwnerStart) != startBefore ||
		f.owner.runner.count(candidateOwnerStop) != stopBefore ||
		f.owner.runner.count(candidateOwnerRemove) != removeBefore {
		t.Fatalf("cold starting-candidate construction mutated source state: startup=%t gateway=%t admission=%t physical=%t capacity=%d/%d commands=%d/%d/%d/%d want=%d/%d/%d/%d",
			reflect.DeepEqual(startupBefore, startupAfter), reflect.DeepEqual(stateBefore, stateAfter),
			reflect.DeepEqual(admissionBefore, admissionAfter), reflect.DeepEqual(physicalBefore, physicalAfter),
			capacityCallsBefore, capacityCallsAfter, f.owner.runner.count(candidateOwnerCreate),
			f.owner.runner.count(candidateOwnerStart), f.owner.runner.count(candidateOwnerStop),
			f.owner.runner.count(candidateOwnerRemove), createBefore, startBefore, stopBefore, removeBefore)
	}
	return domain, constructErr
}

func assertGeneratedDomainColdStartingCandidateDebt(t *testing.T,
	fixture generatedDomainColdStartingCandidatePublicFixture, domain *GeneratedDomain,
) {
	t.Helper()
	if domain == nil || domain.construction == nil || domain.construction.cold == nil ||
		len(domain.construction.cold.groups) != 1 || generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()) != 0 {
		t.Fatalf("all-running starting-candidate construction=%#v probes=%d",
			domain, generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()))
	}
	manifest := domain.construction.cold.groups[0]
	if manifest.deploymentID != fixture.migration.request.DeploymentID || len(manifest.components) != 2 {
		t.Fatalf("all-running starting-candidate manifest=%#v", manifest)
	}
	ledger := domain.Candidates().ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("all-running starting-candidate accounting group is absent")
	}
	group.mu.Lock()
	debts := make([]*generatedDomainCapacityDebtToken, 0, len(group.components))
	valid := reflect.DeepEqual(group.components, []string{"api", "frontend"})
	for _, name := range group.components {
		component := group.byName[name]
		identity, hasReceipt := fixture.identities[name]
		debt := (*generatedDomainCapacityDebtToken)(nil)
		if component != nil && component.charge != nil {
			debt, _ = component.charge.token.(*generatedDomainCapacityDebtToken)
		}
		validReceipt := hasReceipt && component != nil && component.receipt != nil &&
			component.receipt.id == identity.ReceiptID &&
			candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalPresent &&
			component.charge != nil && component.charge.receiptRetained && !component.charge.explicitRetained
		if !hasReceipt {
			validReceipt = component != nil && component.receipt == nil && component.charge != nil &&
				!component.charge.receiptRetained && component.charge.explicitRetained
		}
		valid = valid && validReceipt && component.charge.state == candidateChargeActive &&
			component.charge.owner == candidateAccountingCandidate && !component.charge.logicallyRetired &&
			component.charge.refs == 0 && component.charge.lease == nil && debt != nil && debt.active &&
			debt.limits == group.limits && !component.tokenHeld
		debts = append(debts, debt)
	}
	limits := group.limits
	group.mu.Unlock()
	coordinator := domain.construction.capacity
	coordinator.mu.Lock()
	valid = valid && coordinator.phase == generatedDomainCapacityPublished && coordinator.borrows == 0 &&
		coordinator.memory == uint64(len(debts))*limits.MemoryBytes &&
		coordinator.disk == uint64(len(debts))*limits.DiskBytes && len(coordinator.tokens) == len(debts)
	for _, debt := range debts {
		_, present := coordinator.tokens[debt]
		valid = valid && present && debt != nil && debt.owner == coordinator && debt.active
	}
	phase, borrows, memory, disk, tokens := coordinator.phase, coordinator.borrows,
		coordinator.memory, coordinator.disk, len(coordinator.tokens)
	coordinator.mu.Unlock()
	if !valid {
		t.Fatalf("all-running starting-candidate debt is not exact: group=%#v phase=%d borrows=%d memory=%d disk=%d tokens=%d",
			group, phase, borrows, memory, disk, tokens)
	}
}

func TestGeneratedDomainColdStartingCandidatePrefixPublicControls(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name        string
		createOrder []string
		start       map[string]bool
	}{
		{name: "running_prefix_with_absent_tail_restores_candidate_debt_without_capacity_probe",
			createOrder: []string{"api"}, start: map[string]bool{"api": true}},
		{name: "running_prefix_with_never_started_tail_restores_candidate_debt_without_capacity_probe",
			createOrder: []string{"api", "frontend"}, start: map[string]bool{"api": true}},
		{name: "all_running_restores_candidate_debt_without_capacity_probe",
			createOrder: []string{"api", "frontend"}, start: map[string]bool{"api": true, "frontend": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainColdStartingCandidatePublicFixture(t, test.createOrder, test.start)
				domain, err := fixture.construct(t)
				if err != nil {
					t.Fatalf("construct valid starting-candidate prefix: %v", err)
				}
				assertGeneratedDomainColdStartingCandidateDebt(t, fixture, domain)
			})
		})
	}
	t.Run("noncontiguous_durable_identity_refuses_before_capacity", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainColdStartingCandidatePublicFixture(t,
				[]string{"frontend"}, map[string]bool{"frontend": true})
			domain, err := fixture.construct(t)
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdGroupStructure,
				"generated domain construction was refused: stage=cold_construction reason=cold_group_structure")
			if domain != nil || generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()) != 0 {
				t.Fatalf("noncontiguous starting candidate published=%v probes=%d",
					domain, generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()))
			}
		})
	})
	for _, test := range []struct {
		name  string
		start map[string]bool
		stop  string
	}{
		{name: "earlier_never_started_refuses", start: map[string]bool{"frontend": true}},
		{name: "multiple_never_started_refuses", start: map[string]bool{}},
		{name: "final_stopped_refuses", start: map[string]bool{"api": true, "frontend": true}, stop: "frontend"},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainColdStartingCandidatePublicFixture(t,
					[]string{"api", "frontend"}, test.start)
				if test.stop != "" {
					identity := fixture.identities[test.stop]
					physical := fixture.candidates.physical[identity.ContainerID]
					physical.running = false
					fixture.candidates.physical[identity.ContainerID] = physical
				}
				domain, err := fixture.construct(t)
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerAttestation,
					"generated domain construction was refused: stage=cold_construction reason=cold_group_container_attestation")
				if domain != nil || generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()) != 0 {
					t.Fatalf("invalid starting-candidate physical state published=%v probes=%d",
						domain, generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()))
				}
			})
		})
	}
}

func TestGeneratedDomainColdStartingCandidatePrefixHelperRefusesNoncontiguousDurableIdentity(t *testing.T) {
	group := generatedDomainColdGroup{
		kind: generatedDomainColdRisky,
		snapshot: candidateAdmissionSnapshot{
			runtime: candidateRuntimeSnapshot{phase: generatedruntimestate.PhaseStartingCandidate},
			components: []candidateAdmissionComponent{
				{plan: deploymentplans.Component{Name: "api"}, state: generatedruntimestate.ComponentImageReady},
				{plan: deploymentplans.Component{Name: "frontend"}, state: generatedruntimestate.ComponentRunning,
					receipt: &candidateIntentSnapshot{id: "receipt-frontend"}, containerName: "frontend-container",
					containerID: strings.Repeat("a", 64)},
			},
		},
		policies: map[string]candidatePolicy{"frontend": {}},
		receipts: map[string]candidateColdReceiptSeed{"frontend": {
			id: "receipt-frontend", physical: candidatePhysicalPresent,
		}},
	}
	if !generatedDomainColdHasReceipt(group.snapshot.components) {
		t.Fatal("noncontiguous helper input did not retain durable identity evidence")
	}
	if prefix, ok := generatedDomainColdStartingCandidatePrefixLength(group); ok || prefix != 0 {
		t.Fatalf("noncontiguous starting-candidate prefix=(%d,%t)", prefix, ok)
	}
}

func TestGeneratedDomainColdPreActivationHandoffSeeds(t *testing.T) {
	const (
		successorID   = "22222222-2222-4222-8222-222222222222"
		predecessorID = "11111111-1111-4111-8111-111111111111"
		apiReceipt    = "33333333-3333-4333-8333-333333333333"
		webReceipt    = "44444444-4444-4444-8444-444444444444"
	)
	newGroup := func() generatedDomainColdGroup {
		return generatedDomainColdGroup{
			pins:     candidateValidationPins{deploymentID: successorID},
			kind:     generatedDomainColdRisky,
			bindHead: true,
			snapshot: candidateAdmissionSnapshot{
				headPins:     candidateHeadPins{previousDeploymentID: predecessorID},
				observedHead: candidateHeadSnapshot{deploymentID: predecessorID},
				runtime:      candidateRuntimeSnapshot{phase: generatedruntimestate.PhaseSwitchingRoute},
				components: []candidateAdmissionComponent{
					{plan: deploymentplans.Component{Name: "api"}, state: generatedruntimestate.ComponentHealthy,
						receipt: &candidateIntentSnapshot{id: apiReceipt}},
					{plan: deploymentplans.Component{Name: "web"}, state: generatedruntimestate.ComponentHealthy,
						receipt: &candidateIntentSnapshot{id: webReceipt}},
				},
			},
			receipts: map[string]candidateColdReceiptSeed{
				"api": {id: apiReceipt, physical: candidatePhysicalPresent},
				"web": {id: webReceipt, physical: candidatePhysicalPresent},
			},
		}
	}
	pending := generatedDomainColdPendingActivationEvidence{successorDeployment: successorID, beforeActivation: true}

	t.Run("exact pending promotes only the cloned complete roster", func(t *testing.T) {
		group := newGroup()
		seeds, ok := generatedDomainColdPreActivationHandoffSeeds(group, pending)
		if !ok || len(seeds) != 2 || seeds["api"].physical != candidatePhysicalHandedToRoute ||
			seeds["web"].physical != candidatePhysicalHandedToRoute ||
			group.receipts["api"].physical != candidatePhysicalPresent ||
			group.receipts["web"].physical != candidatePhysicalPresent {
			t.Fatalf("exact pending handoff seeds=%#v source=%#v ok=%t", seeds, group.receipts, ok)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*generatedDomainColdGroup, *generatedDomainColdPendingActivationEvidence)
	}{
		{name: "missing receipt refuses whole roster", mutate: func(group *generatedDomainColdGroup, _ *generatedDomainColdPendingActivationEvidence) {
			delete(group.receipts, "web")
		}},
		{name: "crossed pending successor refuses", mutate: func(_ *generatedDomainColdGroup, pending *generatedDomainColdPendingActivationEvidence) {
			pending.successorDeployment = predecessorID
		}},
		{name: "post activation pending refuses", mutate: func(_ *generatedDomainColdGroup, pending *generatedDomainColdPendingActivationEvidence) {
			pending.beforeActivation = false
		}},
		{name: "partial healthy roster refuses", mutate: func(group *generatedDomainColdGroup, _ *generatedDomainColdPendingActivationEvidence) {
			group.snapshot.components = group.snapshot.components[:1]
		}},
		{name: "non-present receipt refuses", mutate: func(group *generatedDomainColdGroup, _ *generatedDomainColdPendingActivationEvidence) {
			seed := group.receipts["api"]
			seed.physical = candidatePhysicalHandedToRoute
			group.receipts["api"] = seed
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			group, candidate := newGroup(), pending
			test.mutate(&group, &candidate)
			if seeds, ok := generatedDomainColdPreActivationHandoffSeeds(group, candidate); ok || seeds != nil {
				t.Fatalf("invalid pre-activation handoff seeds=%#v ok=%t", seeds, ok)
			}
		})
	}
}

type generatedDomainGatewayConfigObservationTestRunner struct {
	containerID string
	filename    string
	config      []byte
	failRestart bool
	failLive    bool
	requests    [][]string
}

func (r *generatedDomainGatewayConfigObservationTestRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, append([]string(nil), request.Args...))
	args := request.Args
	if reflect.DeepEqual(args, []string{"container", "cp", r.containerID + ":/config/" + r.filename, "-"}) {
		if r.failRestart {
			return runtimeprocess.CommandResult{}, errors.New("injected restart config read failure")
		}
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if err := writer.WriteHeader(&tar.Header{Name: r.filename, Mode: 0o600, Size: int64(len(r.config)), Typeflag: tar.TypeReg}); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		if _, err := writer.Write(r.config); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		if err := writer.Close(); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		return runtimeprocess.CommandResult{Stdout: archive.Bytes()}, nil
	}
	if reflect.DeepEqual(args, []string{"container", "exec", r.containerID,
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/"}) {
		if r.failLive {
			return runtimeprocess.CommandResult{}, errors.New("injected live config read failure")
		}
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.config...)}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected gateway config observation command")
}

func TestGeneratedDomainGatewayConfigObservationBindsExactReader(t *testing.T) {
	const filename = "active.json"
	containerID := strings.Repeat("a", 64)
	config := []byte(`{"apps":{}}`)
	runner := &generatedDomainGatewayConfigObservationTestRunner{containerID: containerID, filename: filename, config: config}
	reader := &generatedDomainStartupReader{options: generatedDomainOptions{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Second, OutputLimit: 1024,
	}, runner: runner}
	observation, err := reader.readGatewayConfigObservation(context.Background(), time.Now().Add(time.Second), containerID, filename)
	wantRequests := [][]string{
		{"container", "cp", containerID + ":/config/" + filename, "-"},
		{"container", "exec", containerID,
			"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
			"--max-time", "10", "http://127.0.0.1:2019/config/"},
	}
	if err != nil || observation == nil || !reflect.DeepEqual(runner.requests, wantRequests) {
		t.Fatalf("strict config observation=%#v error=%v requests=%#v", observation, err, runner.requests)
	}

	restart, err := reader.readGatewayRestartConfigObserved(context.Background(), time.Now().Add(time.Second),
		containerID, filename, observation)
	if err != nil || string(restart) != `{"apps":{}}` {
		t.Fatalf("borrow restart config=%q error=%v", restart, err)
	}
	restart[0] = 'x'
	clear(restart)
	if string(observation.restart) != `{"apps":{}}` {
		t.Fatal("borrowed restart config aliased the observation")
	}
	live, err := reader.readGatewayLiveConfigObserved(context.Background(), time.Now().Add(time.Second),
		containerID, filename, observation)
	if err != nil || string(live) != `{"apps":{}}` {
		t.Fatalf("borrow live config=%q error=%v", live, err)
	}
	live[0] = 'x'
	clear(live)
	if string(observation.live) != `{"apps":{}}` {
		t.Fatal("borrowed live config aliased the observation")
	}

	for _, test := range []struct {
		name     string
		reader   *generatedDomainStartupReader
		id, file string
	}{
		{name: "wrong reader", reader: &generatedDomainStartupReader{}, id: containerID, file: filename},
		{name: "wrong immutable id", reader: reader, id: strings.Repeat("b", 64), file: filename},
		{name: "wrong restart file", reader: reader, id: containerID, file: "other.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if value, err := test.reader.readGatewayRestartConfigObserved(context.Background(), time.Now().Add(time.Second),
				test.id, test.file, observation); err == nil || value != nil {
				clear(value)
				t.Fatalf("borrowed restart value=%q error=%v", value, err)
			}
			if value, err := test.reader.readGatewayLiveConfigObserved(context.Background(), time.Now().Add(time.Second),
				test.id, test.file, observation); err == nil || value != nil {
				clear(value)
				t.Fatalf("borrowed live value=%q error=%v", value, err)
			}
		})
	}

	failed := &generatedDomainGatewayConfigObservationTestRunner{containerID: containerID, filename: filename, config: config, failLive: true}
	reader.runner = failed
	if value, err := reader.readGatewayConfigObservation(context.Background(), time.Now().Add(time.Second),
		containerID, filename); err == nil || value != nil || !reflect.DeepEqual(failed.requests, wantRequests) {
		t.Fatalf("fresh staged observation=%#v error=%v requests=%#v", value, err, failed.requests)
	}
	restartFailed := &generatedDomainGatewayConfigObservationTestRunner{containerID: containerID, filename: filename, config: config, failRestart: true}
	reader.runner = restartFailed
	if value, err := reader.readGatewayConfigObservation(context.Background(), time.Now().Add(time.Second),
		containerID, filename); err == nil || value != nil || !reflect.DeepEqual(restartFailed.requests, wantRequests[:1]) {
		t.Fatalf("restart observation=%#v error=%v requests=%#v", value, err, restartFailed.requests)
	}
	observation.clear()
	if observation.reader != nil || observation.containerID != "" || observation.filename != "" ||
		observation.restart != nil || observation.live != nil {
		t.Fatalf("cleared observation=%#v", observation)
	}
}

func TestGeneratedDomainColdPendingGatewayConfigDisposition(t *testing.T) {
	before := []byte(`{"apps":{"http":{"servers":{"before":{}}}}}`)
	serving := []byte(`{"apps":{"http":{"servers":{"serving":{}}}}}`)
	for _, test := range []struct {
		name          string
		restart, live []byte
		want          generatedDomainColdPendingGatewayDisposition
		accepted      bool
	}{
		{name: "previous exact", restart: before, live: before, want: generatedDomainColdPendingGatewayPreviousExact, accepted: true},
		{name: "proposed exact", restart: serving, live: serving, want: generatedDomainColdPendingGatewayProposedExact, accepted: true},
		{name: "mixed projections refuse", restart: before, live: serving},
		{name: "neither projection refuses", restart: []byte(`{"apps":{}}`), live: []byte(`{"apps":{}}`)},
		{name: "both projections refuse", restart: before, live: before, want: generatedDomainColdPendingGatewayUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateBefore, candidateServing := before, serving
			if test.name == "both projections refuse" {
				candidateServing = before
			}
			got, accepted := classifyGeneratedDomainColdPendingGatewayConfigs(candidateBefore, candidateServing,
				test.restart, test.live)
			if accepted != test.accepted || got != test.want {
				t.Fatalf("disposition=%d accepted=%t want=%d/%t", got, accepted, test.want, test.accepted)
			}
		})
	}
}

// TestGeneratedDomainColdPreviousExactProtectedModeProjection is deliberately
// a protected-mode classifier boundary, not a second end-to-end cold process
// fixture. The authentic Legacy recovery parent exercises the common Pin path;
// these rows prove the NativeV2 and CurrentV1 switches reconstruct and clone
// their complete typed transitions without collapsing them into Legacy state.
func TestGeneratedDomainColdPreviousExactProtectedModeProjection(t *testing.T) {
	t.Run("committed_v2", func(t *testing.T) {
		fixture := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		fixture = generatedDomainColdCanonicalCommittedV2Pending(t, fixture)
		fixture.installProtected(t, fixture.pending)
		protected := generatedRoutePreviousCommittedV2ProtectedSnapshot(t, fixture)
		database, _ := generatedDomainNativeV2RecoveryFixture(t, fixture.pending, fixture.journal)
		originalSource := cloneRouteState(*protected.nativeV1)
		originalState := cloneGatewayV2RouteState(protected.upgrade.state)
		originalJournal := protected.upgrade.journal
		originalGenerations := make([]gatewayV2RouteState, len(protected.upgrade.generations))
		for index := range protected.upgrade.generations {
			originalGenerations[index] = cloneGatewayV2RouteState(protected.upgrade.generations[index].State)
		}
		if _, accepted := generatedDomainCommittedUpgradeGatewayCandidate(database, protected, false); accepted {
			t.Fatal("ordinary committed selector admitted a Pending route transition")
		}
		selected, accepted := generatedDomainCommittedUpgradeGatewaySelection(database, protected)
		candidate, routeAccepted := generatedDomainCommittedUpgradeRoutePendingGatewayCandidate(database, protected)
		digest, digestErr := canonicalDigest(fixture.pending)
		if !accepted || !routeAccepted || digestErr != nil || candidate.nativeV1 == nil || candidate.nativeV2 == nil ||
			candidate.journal == nil || candidate.upgrade == nil || candidate.kind != generatedDomainGatewayNativeV2 ||
			candidate.stateDigest != digest || !reflect.DeepEqual(*candidate.nativeV1, *protected.nativeV1) ||
			!reflect.DeepEqual(*candidate.nativeV2, protected.upgrade.state) ||
			!reflect.DeepEqual(*candidate.journal, protected.upgrade.journal) ||
			!reflect.DeepEqual(*candidate.upgrade, selected) ||
			!reflect.DeepEqual(candidate.upgrade.State, *candidate.nativeV2) ||
			!reflect.DeepEqual(candidate.upgrade.Journal, *candidate.journal) {
			t.Fatalf("committed V2 route-Pending candidate=%#v selected=%#v accepted=%t/%t digest=%v",
				candidate, selected, accepted, routeAccepted, digestErr)
		}

		lanRequest := gatewayV2LANGrantRequestForState(t, fixture.before)
		lanBinding, err := gatewayV2LANBindingForRequest(lanRequest)
		if err != nil {
			t.Fatal(err)
		}
		lanState := cloneGatewayV2RouteState(fixture.before)
		lanPrevious := cloneGatewayV2AppRoute(lanState.Apps[lanRequest.AppID])
		lanProposed := cloneGatewayV2AppRoute(lanPrevious)
		lanProposed.LAN = &lanBinding
		lanState.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingLANGrant,
			AppID: lanRequest.AppID, Previous: &lanPrevious, Proposed: lanProposed}
		if !validGatewayV2RouteState(lanState) {
			t.Fatal("committed V2 LAN refusal control is not a valid protected Pending")
		}
		lanProtected := generatedDomainColdCommittedV2ProtectedState(t, protected, lanState)
		generatedDomainColdAssertNoCommittedV2RoutePending(t, database, lanProtected, fixture.transition,
			"LAN Pending")

		invalidState := cloneGatewayV2RouteState(fixture.pending)
		invalidState.SourceV1StateDigest = strings.Repeat("f", 64)
		if invalidState.SourceV1StateDigest == fixture.pending.SourceV1StateDigest {
			invalidState.SourceV1StateDigest = strings.Repeat("e", 64)
		}
		invalidCleared := cloneGatewayV2RouteState(invalidState)
		invalidCleared.Pending = nil
		if !validGatewayV2RouteState(invalidState) ||
			validGatewayTopologyInputs(*protected.nativeV1, invalidCleared, fixture.journal) {
			t.Fatal("committed V2 cleared-topology refusal control is not isolated")
		}
		invalidProtected := generatedDomainColdCommittedV2ProtectedState(t, protected, invalidState)
		generatedDomainColdAssertNoCommittedV2RoutePending(t, database, invalidProtected, fixture.transition,
			"invalid Pending-cleared topology")

		recoveries, recoveriesValid := generatedDomainStartupRecoveries(database, protected,
			generatedDomainDockerStartupSnapshot{cold: true})
		classification, classifyErr := classifyGeneratedDomainStartup(database, protected,
			generatedDomainDockerStartupSnapshot{cold: true})
		if !recoveriesValid || len(recoveries) != 0 || classifyErr != nil ||
			classification.disposition != generatedDomainStartupRefused ||
			classification.reason != generatedDomainStartupReasonColdAccounting || len(classification.recovery) != 0 {
			t.Fatalf("committed V2 route-Pending cold classification=%#v recoveries=%#v valid=%t error=%v",
				classification, recoveries, recoveriesValid, classifyErr)
		}
		unresolvedLAN := database
		unresolvedLAN.Gateway.Grants = generatedDomainGrantStartupSnapshot([]GatewayV2LANStartupClaim{
			gatewayV2LANStartupClaim(lanRequest, appaccess.AppAccessGrantPrepared, 1),
		})
		classification, classifyErr = classifyGeneratedDomainStartup(unresolvedLAN, protected,
			generatedDomainDockerStartupSnapshot{cold: true})
		if classifyErr != nil || classification.disposition != generatedDomainStartupRefused ||
			classification.reason != generatedDomainStartupReasonAmbiguous || len(classification.recovery) != 0 {
			t.Fatalf("route-Pending with unresolved LAN claim classification=%#v error=%v",
				classification, classifyErr)
		}

		evidence := generatedDomainColdPreviousProtectedModeEvidence(t, database, protected, fixture.transition)
		if evidence.transition.kind != generatedDomainGatewayNativeV2 ||
			evidence.transition.nativeV2 == nil ||
			!reflect.DeepEqual(*evidence.transition.nativeV2, *fixture.transition.nativeV2) ||
			evidence.protectedGateway.kind != generatedDomainGatewayNativeV2 ||
			evidence.protectedGateway.nativeV1 == nil || evidence.protectedGateway.nativeV2 == nil ||
			evidence.protectedGateway.journal == nil || evidence.protectedGateway.upgrade == nil ||
			!reflect.DeepEqual(*evidence.protectedGateway.nativeV2, fixture.pending) ||
			!reflect.DeepEqual(*evidence.protectedGateway.journal, fixture.journal) {
			t.Fatalf("committed V2 Pending projection=%#v", evidence)
		}
		if !reflect.DeepEqual(*protected.nativeV1, originalSource) ||
			!reflect.DeepEqual(protected.upgrade.state, originalState) ||
			!reflect.DeepEqual(protected.upgrade.journal, originalJournal) ||
			len(protected.upgrade.generations) != len(originalGenerations) {
			t.Fatal("committed V2 route-Pending classification mutated its protected input")
		}
		for index := range originalGenerations {
			if !reflect.DeepEqual(protected.upgrade.generations[index].State, originalGenerations[index]) {
				t.Fatalf("committed V2 route-Pending classification mutated generation %d", index)
			}
		}
	})

	t.Run("gateway_current_with_retained_v2", func(t *testing.T) {
		fixture := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		fixture.installPending(t)
		snapshot := generatedRoutePreviousGatewayCurrentSnapshotFor(t, fixture)
		protected, currentV2Name := generatedDomainColdPreviousCurrentV2Companion(t,
			snapshot.protected, fixture.transition)
		var database deployments.GeneratedDomainStartupSnapshot
		database.Gateway.Rebind = routeOperationCommittedSnapshot(fixture.fixture)
		exactEntry, exactEntryFound := protected.currentV2.files[currentV2Name]
		if !exactEntryFound || !validGatewayCurrentRouteStateV2(exactEntry.State) ||
			!generatedDomainCurrentDatabaseMatches(database, exactEntry.State.Lineage,
				exactEntry.State.Profile, exactEntry.State.TransferManifestDigest) {
			t.Fatal("exact Current V2 companion is not structurally valid and database-selectable")
		}
		crossed := protected
		crossed.currentV2.files = make(map[string]gatewayCurrentV2CensusEntry, len(protected.currentV2.files))
		for name, entry := range protected.currentV2.files {
			entry.State = cloneGatewayCurrentRouteStateV2(entry.State)
			crossed.currentV2.files[name] = entry
		}
		crossedEntry := crossed.currentV2.files[currentV2Name]
		if crossedEntry.State.SourceV1 == nil {
			t.Fatal("Current V2 companion has no V1 origin")
		}
		origin := *crossedEntry.State.SourceV1
		origin.Digest = strings.Repeat("f", 64)
		crossedEntry.State.SourceV1 = &origin
		crossedEntry.State.Digest = ""
		var err error
		crossedEntry.State.Digest, err = gatewayCurrentRouteStateDigestV2(crossedEntry.State)
		if err != nil || !validGatewayCurrentRouteStateV2(crossedEntry.State) ||
			!generatedDomainCurrentDatabaseMatches(database, crossedEntry.State.Lineage,
				crossedEntry.State.Profile, crossedEntry.State.TransferManifestDigest) {
			t.Fatalf("crossed Current V2 companion is not a valid independent candidate: %v", err)
		}
		crossed.currentV2.files[currentV2Name] = crossedEntry
		if crossedEvidence, found, err := generatedDomainColdPendingActivation(database, crossed,
			[]generatedDomainColdGroup{generatedDomainColdPreviousProtectedModeGroup(t, fixture.sealed)}); found || !errors.Is(err, errGeneratedDomainConstructionRefused) || crossedEvidence.transition != nil {
			t.Fatalf("crossed Current V2 companion evidence=%#v found=%t error=%v",
				crossedEvidence, found, err)
		}

		evidence := generatedDomainColdPreviousProtectedModeEvidence(t, database, protected, fixture.sealed)
		if evidence.transition.kind != generatedDomainGatewayCurrentV1 ||
			evidence.transition.currentV1 == nil ||
			!reflect.DeepEqual(*evidence.transition.currentV1, fixture.transition) ||
			evidence.protectedGateway.kind != generatedDomainGatewayCurrentV1 ||
			evidence.protectedGateway.currentV1 == nil ||
			!reflect.DeepEqual(*evidence.protectedGateway.currentV1, fixture.transition.Pending) ||
			evidence.protectedGateway.retainedV2 == nil ||
			!generatedDomainRetainedV2ProjectionValid(evidence.protectedGateway.retainedV2) ||
			!reflect.DeepEqual(evidence.protectedGateway.retainedV2, snapshot.retained) {
			t.Fatalf("Current Pending retained-V2 projection=%#v retained=%#v",
				evidence.protectedGateway, snapshot.retained)
		}
		beforeRetained := cloneGeneratedDomainRetainedV2Projection(*snapshot.retained)
		beforeRetained.current = cloneGatewayCurrentRouteState(fixture.transition.Before)
		effectiveRetained := cloneGeneratedDomainRetainedV2Projection(*snapshot.retained)
		effectiveRetained.current = cloneGatewayCurrentRouteState(fixture.transition.Effective)
		if evidence.routeGateway.retainedV2 == nil || evidence.servingGateway.retainedV2 == nil ||
			!reflect.DeepEqual(*evidence.routeGateway.retainedV2, beforeRetained) ||
			!reflect.DeepEqual(*evidence.servingGateway.retainedV2, effectiveRetained) ||
			!generatedDomainRetainedV2ProjectionValid(evidence.routeGateway.retainedV2) ||
			!generatedDomainRetainedV2ProjectionValid(evidence.servingGateway.retainedV2) {
			t.Fatalf("Current projected retained-V2 before=%#v effective=%#v",
				evidence.routeGateway.retainedV2, evidence.servingGateway.retainedV2)
		}
	})
}

func generatedDomainColdCanonicalCommittedV2Pending(t *testing.T,
	fixture generatedRoutePreviousCommittedV2ClearFixture,
) generatedRoutePreviousCommittedV2ClearFixture {
	t.Helper()
	transition := cloneGeneratedRouteActivationGatewayTransition(fixture.transition)
	if transition == nil || transition.nativeV2 == nil || transition.nativeV2.pending.Pending == nil {
		t.Fatal("committed V2 fixture has no Pending transition")
	}
	request := canonicalGeneratedRouteRequest(transition.request)
	pending := cloneGatewayV2RouteState(transition.nativeV2.pending)
	pending.Pending.Proposed.Route.Endpoints = append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)
	effective := cloneGatewayV2RouteState(transition.nativeV2.effective)
	app := effective.Apps[request.AppID]
	app.Route.Endpoints = append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)
	effective.Apps[request.AppID] = app
	transition.request = request
	transition.nativeV2.pending = cloneGatewayV2RouteState(pending)
	transition.nativeV2.effective = cloneGatewayV2RouteState(effective)
	if !validGeneratedRouteActivationGatewayTransition(transition) {
		t.Fatal("canonical committed V2 fixture produced an invalid transition")
	}
	fixture.pending, fixture.effective, fixture.transition = pending, effective, transition
	return fixture
}

func generatedDomainColdCommittedV2ProtectedState(t *testing.T,
	protected generatedDomainProtectedStartupSnapshot, state gatewayV2RouteState,
) generatedDomainProtectedStartupSnapshot {
	t.Helper()
	result := protected
	result.upgrade.state = cloneGatewayV2RouteState(state)
	result.upgrade.generations = append([]gatewayUpgradeGenerationSelection(nil), protected.upgrade.generations...)
	matched := 0
	for index := range result.upgrade.generations {
		candidate := result.upgrade.generations[index]
		candidate.State = cloneGatewayV2RouteState(candidate.State)
		if reflect.DeepEqual(candidate.State, protected.upgrade.state) {
			candidate.State = cloneGatewayV2RouteState(state)
			matched++
		}
		result.upgrade.generations[index] = candidate
	}
	if matched != 1 {
		t.Fatalf("committed V2 protected control matched %d active generations", matched)
	}
	return result
}

func generatedDomainColdAssertNoCommittedV2RoutePending(t *testing.T,
	database deployments.GeneratedDomainStartupSnapshot, protected generatedDomainProtectedStartupSnapshot,
	transition *generatedRouteActivationGatewayTransition, name string,
) {
	t.Helper()
	if candidate, accepted := generatedDomainCommittedUpgradeRoutePendingGatewayCandidate(database, protected); accepted {
		t.Fatalf("%s admitted route-Pending candidate %#v", name, candidate)
	}
	evidence, found, err := generatedDomainColdPendingActivation(database, protected,
		[]generatedDomainColdGroup{generatedDomainColdPreviousProtectedModeGroup(t, transition)})
	if err != nil || found || evidence.transition != nil {
		t.Fatalf("%s cold evidence=%#v found=%t error=%v", name, evidence, found, err)
	}
}

func generatedDomainColdPreviousProtectedModeEvidence(t *testing.T,
	database deployments.GeneratedDomainStartupSnapshot, protected generatedDomainProtectedStartupSnapshot,
	expected *generatedRouteActivationGatewayTransition,
) generatedDomainColdPendingActivationEvidence {
	t.Helper()
	group := generatedDomainColdPreviousProtectedModeGroup(t, expected)
	evidence, found, err := generatedDomainColdPendingActivation(database, protected,
		[]generatedDomainColdGroup{group})
	if err != nil || !found || evidence.transition == nil || !evidence.beforeActivation ||
		evidence.successorDeployment != group.pins.deploymentID ||
		!reflect.DeepEqual(evidence.transition, expected) {
		t.Fatalf("protected Pending evidence=%#v found=%t error=%v", evidence, found, err)
	}
	wantBefore, wantHadBefore, wantProposed, wantValid := generatedRouteActivationGatewayRoutes(expected)
	gotBefore, gotHadBefore, gotProposed, gotValid := generatedRouteActivationGatewayRoutes(evidence.transition)
	if !wantValid || !gotValid || !wantHadBefore || !gotHadBefore ||
		!sameRoute(gotBefore, wantBefore) || !sameRoute(gotProposed, wantProposed) ||
		!sameGeneratedRouteActivationGatewayRequest(evidence.transition.request, expected.request) {
		t.Fatalf("protected Pending routes before=%#v/%#v had=%t/%t proposed=%#v/%#v valid=%t/%t",
			gotBefore, wantBefore, gotHadBefore, wantHadBefore, gotProposed, wantProposed, gotValid, wantValid)
	}
	wantRoute, err := generatedDomainColdPendingGatewayProjection(evidence.protectedGateway, *expected, true)
	if err != nil {
		t.Fatal(err)
	}
	wantServing, err := generatedDomainColdPendingGatewayProjection(evidence.protectedGateway, *expected, false)
	if err != nil || !reflect.DeepEqual(evidence.routeGateway, wantRoute) ||
		!reflect.DeepEqual(evidence.servingGateway, wantServing) {
		t.Fatalf("protected Pending projections route=%#v serving=%#v error=%v",
			evidence.routeGateway, evidence.servingGateway, err)
	}
	cloned := cloneGeneratedRouteActivationGatewayTransition(evidence.transition)
	if cloned == nil || !reflect.DeepEqual(cloned, evidence.transition) || len(cloned.request.Endpoints) == 0 {
		t.Fatal("protected Pending transition did not deep-clone")
	}
	cloned.request.Endpoints[0].NetworkAlias += "-clone-control"
	switch cloned.kind {
	case generatedDomainGatewayNativeV2:
		if cloned.nativeV2 == nil || cloned.nativeV2.pending.Pending == nil ||
			len(cloned.nativeV2.pending.Pending.Proposed.Route.Endpoints) == 0 {
			t.Fatal("committed V2 transition clone omitted nested Pending")
		}
		cloned.nativeV2.pending.Pending.Proposed.Route.Endpoints[0].NetworkAlias += "-clone-control"
	case generatedDomainGatewayCurrentV1:
		if cloned.currentV1 == nil || cloned.currentV1.Pending.Pending == nil ||
			len(cloned.currentV1.Pending.Pending.Proposed.Route.Endpoints) == 0 {
			t.Fatal("Current transition clone omitted nested Pending")
		}
		cloned.currentV1.Pending.Pending.Proposed.Route.Endpoints[0].NetworkAlias += "-clone-control"
	default:
		t.Fatalf("unexpected protected Pending transition kind %d", cloned.kind)
	}
	if reflect.DeepEqual(cloned, evidence.transition) || !reflect.DeepEqual(evidence.transition, expected) {
		t.Fatal("protected Pending transition clone aliases classifier evidence")
	}
	return evidence
}

func generatedDomainColdPreviousCurrentV2Companion(t *testing.T,
	protected generatedDomainProtectedStartupSnapshot, transition gatewayCurrentPhysicalTransition,
) (generatedDomainProtectedStartupSnapshot, string) {
	t.Helper()
	before := cloneGatewayCurrentRouteState(transition.Before)
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
		Lineage: before.Lineage, State: &before}
	authority := gatewayCurrentV2PromotionAuthorityForTest(t, selection)
	state, err := gatewayCurrentV2BaselineFor(selection, authority)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := gatewayCurrentRouteStateNameV2(state.Lineage.ProtectedGeneration, state.Lineage.OperationID)
	protected.currentV2 = gatewayCurrentV2Census{files: map[string]gatewayCurrentV2CensusEntry{name: {
		Generation: state.Lineage.ProtectedGeneration, OperationID: state.Lineage.OperationID, State: state,
	}}}
	return protected, name
}

func generatedDomainColdPreviousProtectedModeGroup(t *testing.T,
	transition *generatedRouteActivationGatewayTransition,
) generatedDomainColdGroup {
	t.Helper()
	before, hadBefore, proposed, valid := generatedRouteActivationGatewayRoutes(transition)
	if !valid || !hadBefore || len(proposed.Endpoints) == 0 ||
		transition.request.AppID == "" || transition.request.FromSlot != before.Slot ||
		transition.request.ToSlot != proposed.Slot {
		t.Fatalf("protected Pending transition is not a replacement: %#v", transition)
	}
	deploymentID, releaseID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	predecessorID, predecessorReleaseID := uuid.NewString(), uuid.NewString()
	components := make([]candidateAdmissionComponent, 0, len(proposed.Endpoints))
	policies := make(map[string]candidatePolicy, len(proposed.Endpoints))
	for _, endpoint := range proposed.Endpoints {
		components = append(components, candidateAdmissionComponent{
			plan: deploymentplans.Component{Name: endpoint.Component, Role: endpoint.Role,
				InternalPort: endpoint.InternalPort},
			state: generatedruntimestate.ComponentHealthy, containerID: endpoint.ContainerID,
		})
		policies[endpoint.Component] = candidatePolicy{identity: generatedcontainer.Identity{
			AppID: transition.request.AppID, DeploymentID: deploymentID,
			Component: endpoint.Component, Role: endpoint.Role,
			NetworkName: endpoint.NetworkName, NetworkAlias: endpoint.NetworkAlias,
		}}
	}
	group := generatedDomainColdGroup{pins: candidateValidationPins{
		identity: candidateValidationIdentity{applicationID: transition.request.AppID,
			jobID: jobID, attempt: 1},
		deploymentID: deploymentID, releaseID: releaseID, candidateSlot: string(proposed.Slot),
	}, snapshot: candidateAdmissionSnapshot{
		pins: candidateImmutablePins{jobID: jobID, releaseID: releaseID,
			candidateSlot: string(proposed.Slot), previousDeploymentID: predecessorID,
			previousSlot: string(before.Slot)},
		headPins: candidateHeadPins{appID: transition.request.AppID, deploymentID: deploymentID,
			releaseID: releaseID, slot: string(proposed.Slot), previousDeploymentID: predecessorID,
			previousReleaseID: predecessorReleaseID, previousSlot: string(before.Slot)},
		observedHead: candidateHeadSnapshot{appID: transition.request.AppID,
			deploymentID: predecessorID, releaseID: predecessorReleaseID,
			slot: string(before.Slot), generation: 1, activationReceiptID: uuid.NewString()},
		runtime:    candidateRuntimeSnapshot{phase: generatedruntimestate.PhaseSwitchingRoute},
		components: components,
	}, kind: generatedDomainColdRisky, policies: policies, bindHead: true}
	if route, ok := generatedDomainColdGroupRoute(group); !ok || !sameRoute(route, proposed) {
		t.Fatalf("protected Pending cloned group route=%#v valid=%t want=%#v", route, ok, proposed)
	}
	return group
}

func runGeneratedColdBoundaryWorker(t *testing.T, service *jobs.Service, executor jobs.Executor, id string, status jobs.Status, workingDirectory string, diagnostic func() string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("worker stopped: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("worker did not stop")
			}
		})
	}
	t.Cleanup(stop)
	admission := func(parent context.Context) (context.Context, func() error, error) {
		scope, release, err := deploymenteffects.AcquireScoped(parent, workingDirectory)
		if err != nil {
			return nil, nil, err
		}
		return deploymenteffects.WithScope(parent, scope), release, nil
	}
	go func() { done <- service.RunWorkerWithScopedAdmission(ctx, executor, admission) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		job, err := service.Get(id)
		if err == nil && job.Status == string(status) {
			stop()
			return
		}
		select {
		case <-deadline.C:
			detail := ""
			if diagnostic != nil {
				detail = diagnostic()
			}
			t.Fatalf("job %s did not reach %s: status=%s phase=%s code=%s attempt=%d getErr=%v diagnostic=%s", id, status,
				job.Status, job.Phase, job.ErrorCode, job.Attempt, err, detail)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func boundedErrorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	value := err.Error()
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func generatedColdBoundarySnapshotSummary(t *testing.T, identity generatedDomainIdentityFixture) string {
	t.Helper()
	snapshot, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		return "snapshot_error=" + boundedErrorText(err)
	}
	result := fmt.Sprintf("deployments=%d runtimes=%d components=%d intents=%d artifacts=%d heads=%d activations=%d",
		len(snapshot.GeneratedDeployments), len(snapshot.Runtime.Deployments), len(snapshot.Runtime.Components),
		len(snapshot.Runtime.CandidateIntents), len(snapshot.Artifacts), len(snapshot.Runtime.ActiveHeads), len(snapshot.Runtime.RouteActivations))
	for _, deployment := range snapshot.GeneratedDeployments {
		result += fmt.Sprintf(" main[%s]=%s/job:%s/release:%s/plan:%s:%d/config:%s:%d/init:%t", deployment.ID,
			deployment.Status, deployment.JobID, deployment.ReleaseID, deployment.DeploymentPlanRevisionID,
			deployment.DeploymentPlanRevisionNumber, deployment.ActualConfigurationRevisionID,
			deployment.ActualConfigurationRevisionNumber, deployment.ProvenanceInitialized)
	}
	for _, runtime := range snapshot.Runtime.Deployments {
		result += fmt.Sprintf(" runtime[%s]=%s/migration:%s/slot:%s/previous:%s:%s", runtime.DeploymentID,
			runtime.Phase, runtime.MigrationState, runtime.CandidateSlot, runtime.PreviousActiveDeploymentID, runtime.PreviousActiveSlot)
	}
	if len(result) > 2048 {
		return result[:2048]
	}
	return result
}

func generatedColdBoundaryStageTrace(t *testing.T, identity generatedDomainIdentityFixture, runner *generatedDomainConstructionRunner, store *stateStore) string {
	t.Helper()
	ctx := context.Background()
	observed, err := inspectGeneratedDomainStartupObservation(ctx, identity.stores, runner, identity.options)
	if err != nil {
		return "inspect=" + boundedErrorText(err)
	}
	prefix := fmt.Sprintf("startup=%d/reason:%d/recovery:%d", observed.classification.disposition, observed.classification.reason, len(observed.classification.recovery))
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database, stores: identity.stores, runner: runner, options: identity.options}, store)
	if err != nil {
		return prefix + "/manager=" + boundedErrorText(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, identity.stores, runner, identity.options)
	if err != nil {
		return prefix + "/physical=" + boundedErrorText(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		return prefix + "/view=" + boundedErrorText(err)
	}
	budget, err := generatedcapacity.New(manager)
	if err != nil {
		return prefix + "/budget=" + boundedErrorText(err)
	}
	authority, err := newGeneratedCandidateAuthorityConcrete(identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, budget, generatedDomainCandidateOptions(identity.options))
	if err != nil {
		return prefix + "/authority=" + boundedErrorText(err)
	}
	groups, err := readGeneratedDomainColdGroups(ctx, authority, observed)
	if err != nil {
		return prefix + "/groups=" + boundedErrorText(err)
	}
	groupSummary := ""
	for _, group := range groups {
		groupSummary += fmt.Sprintf("[%s kind=%d head=%t self=%t legacy=%t components=%d]", group.pins.deploymentID, group.kind, group.bindHead, group.selfHead, group.legacyHead, len(group.snapshot.components))
	}
	if err := attestGeneratedDomainColdGroups(ctx, manager, authority, observed, groups); err != nil {
		return prefix + "/groups=ok" + groupSummary + "/attest=" + boundedErrorText(err)
	}
	return prefix + "/groups=ok" + groupSummary + "/attest=ok"
}

func generatedColdBoundaryCapacityProbes(commands [][]string) int {
	result := 0
	for _, command := range commands {
		if len(command) >= 6 && command[0] == "container" && command[1] == "exec" && command[len(command)-1] == capacityProbeCommand {
			result++
		}
	}
	return result
}

func generatedColdBoundaryPublicOptions(options generatedDomainOptions) GeneratedDomainOptions {
	return GeneratedDomainOptions{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint, DockerConfigDirectory: options.DockerConfigDirectory,
		WorkingDirectory: options.WorkingDirectory, DataRoot: options.DataRoot, HostPort: options.HostPort, CommandTimeout: options.CommandTimeout,
		PullTimeout: options.PullTimeout, HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		MemoryBytes: options.MemoryBytes, MilliCPUs: options.MilliCPUs, PIDs: options.PIDs, TmpfsBytes: options.TmpfsBytes,
		LogSize: options.LogSize, LogFiles: options.LogFiles, ReplacementDiskBytes: options.ReplacementDiskBytes,
	}
}
