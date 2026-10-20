package generatedimage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedImageTestAuthority struct {
	db               *sql.DB
	repository       *deployments.Repository
	bindings         *deploymentbinding.Repository
	context          context.Context
	scope            deploymenteffects.Scope
	releaseScope     func() error
	claim            deploymentexecution.Claim
	request          deployments.GeneratedImageRequest
	release          releasesnapshot.Release
	revision         deploymentplans.DeploymentPlanRevision
	workingDirectory string
}

func newGeneratedImageTestAuthority(t *testing.T, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision, component string) *generatedImageTestAuthority {
	return newGeneratedImageTestAuthorityWithConfiguration(t, release, revision, component, "", 0)
}

func newGeneratedImageTestAuthorityWithConfiguration(t *testing.T, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision, component, configurationID string, configurationNumber int64) *generatedImageTestAuthority {
	return newGeneratedImageTestAuthorityWithDependencies(t, nil, nil, nil, "", release, revision, component, configurationID, configurationNumber)
}

func newGeneratedImageTestAuthorityWithDependencies(t *testing.T, db *sql.DB, repository *deployments.Repository, bindings *deploymentbinding.Repository, workingDirectory string, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision, component, configurationID string, configurationNumber int64) *generatedImageTestAuthority {
	t.Helper()
	if release.AppID == "" {
		release.AppID = uuid.NewString()
	}
	if release.ID == "" {
		release.ID = uuid.NewString()
	}
	if release.ConfigurationRevisionID == "" {
		release.ConfigurationRevisionID = uuid.NewString()
		release.ConfigurationRevisionNumber = 1
	}
	if configurationID == "" {
		configurationID = release.ConfigurationRevisionID
		configurationNumber = release.ConfigurationRevisionNumber
	}
	if release.DeploymentPlanRevisionID == "" {
		release.DeploymentPlanRevisionID = revision.ID
	}
	if release.DeploymentPlanRevisionNumber == 0 {
		release.DeploymentPlanRevisionNumber = revision.RevisionNumber
	}
	if release.SourceProvider == "" {
		release.SourceProvider = "local"
	}
	if release.ResolvedSHA == "" {
		release.ResolvedSHA = digestForTest("release")
	}
	if release.WorkspaceTreeSHA256 == "" {
		release.WorkspaceTreeSHA256 = digestForTest("tree")
	}
	if release.WorkspaceState == "" {
		release.WorkspaceState = releasesnapshot.WorkspaceStateReady
	}
	if revision.ID == "" {
		revision.ID = release.DeploymentPlanRevisionID
	}
	if revision.AppID == "" {
		revision.AppID = release.AppID
	}
	if revision.RevisionNumber == 0 {
		revision.RevisionNumber = release.DeploymentPlanRevisionNumber
	}
	if revision.CanonicalDigest == "" {
		revision.CanonicalDigest = digestForTest("plan")
	}
	if revision.Plan.Source.Provider == "" {
		revision.Plan.Source.Provider = release.SourceProvider
		revision.Plan.Source.RepositoryID = release.RepositoryID
		revision.Plan.Source.ResolvedDigest = release.ResolvedSHA
	}

	var err error
	if db == nil {
		db, err = database.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
	}
	actorID, machineID := uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`, actorID, "image-owner-"+release.AppID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machineID, "local-"+release.AppID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?, 'draft',?,?)`, release.AppID, release.AppID, "Image Test", machineID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	lifecycle := applifecycle.New(db)
	if bindings == nil {
		validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
		if err != nil {
			t.Fatal(err)
		}
		bindings, err = deploymentbinding.New(db, validator)
		if err != nil {
			t.Fatal(err)
		}
	}
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: release.AppID, IdempotencyKey: "generated-image-" + uuid.NewString(), RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create image authority job: created=%t err=%v", created.Created, err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,0)`, release.ConfigurationRevisionID, release.AppID, release.ConfigurationRevisionNumber, "image-test:"+release.ConfigurationRevisionID, actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if configurationID != release.ConfigurationRevisionID || configurationNumber != release.ConfigurationRevisionNumber {
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,0)`, configurationID, release.AppID, configurationNumber, "image-test:"+configurationID, actorID, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,'',?,?,'accepted',?,?)`,
		revision.ID, release.AppID, revision.RevisionNumber, "image-test:"+revision.ID, revision.Plan.Strategy, revision.Plan.Detector.Name, revision.Plan.Detector.Version, revision.Plan.Detector.SourceStructuralFingerprint, revision.Plan.Source.Provider, revision.Plan.Source.RepositoryID, revision.Plan.Source.ResolvedDigest, revision.CanonicalDigest, len(revision.Plan.Components), actorID, stamp, actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,archive_sha256,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,?,?,?,?,'ready',?,?,?,?,?)`,
		release.ID, release.AppID, stamp, release.SourceProvider, release.RepositoryID, release.ResolvedSHA, nullableTestText(release.ArchiveSHA256), release.WorkspaceTreeSHA256, release.ConfigurationRevisionID, release.ConfigurationRevisionNumber, revision.ID, revision.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	if repository == nil {
		positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
		if err != nil {
			t.Fatal(err)
		}
		directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workingDirectory = directories.WorkingDirectory
		guard, err := deploymentexecution.NewGuard(db, bindings, positive, workingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		repository, err = deployments.NewWithExecutionGuard(db, guard)
		if err != nil {
			t.Fatal(err)
		}
	} else if workingDirectory == "" {
		t.Fatal("shared generated image authority requires its repository working directory")
	}
	scope, releaseEffectScope, err := deploymenteffects.AcquireScoped(context.Background(), workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	var releaseErr error
	releaseScope := func() error {
		releaseOnce.Do(func() { releaseErr = releaseEffectScope() })
		return releaseErr
	}
	t.Cleanup(func() {
		if err := releaseScope(); err != nil {
			t.Errorf("release generated image test scope: %v", err)
		}
	})
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	deployment, _, err := repository.GetOrCreateByJob(context.Background(), release.AppID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = repository.InitializeRuntime(context.Background(), release.AppID, deployment.ID, release.ID, configurationID, configurationNumber, deployments.RuntimeGeneratedNode, revision.ID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, release.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Gate(ctx, deployments.GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatal(err)
	}
	components := make([]string, 0, len(revision.Plan.Components))
	for _, value := range revision.Plan.Components {
		components = append(components, value.Name)
	}
	state := generatedruntimestate.New(db)
	runtimeDeployment, wasCreated, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: release.AppID, ReleaseID: release.ID, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber, ComponentNames: components})
	if err != nil || !wasCreated {
		t.Fatalf("begin image runtime: created=%t err=%v", wasCreated, err)
	}
	if _, err := state.Advance(context.Background(), release.AppID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, ""); err != nil {
		t.Fatal(err)
	}
	definition, definitionDigest, err := definitionForBuild(revision, component, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = definition
	request := deployments.GeneratedImageRequest{Claim: claim, DeploymentID: deployment.ID, ReleaseID: release.ID,
		ConfigurationRevisionID: configurationID, ConfigurationRevisionNumber: configurationNumber,
		DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber,
		ComponentName: component, CompilerVersion: CompilerVersion, BuildDefinitionDigest: definitionDigest, PlanCanonicalDigest: revision.CanonicalDigest,
		SourceProvider: release.SourceProvider, SourceRepositoryID: release.RepositoryID, SourceResolvedDigest: release.ResolvedSHA,
		PlanSourceProvider: revision.Plan.Source.Provider, PlanSourceRepositoryID: revision.Plan.Source.RepositoryID, PlanSourceResolvedDigest: revision.Plan.Source.ResolvedDigest,
		ArchiveSHA256: release.ArchiveSHA256, WorkspaceTreeSHA256: release.WorkspaceTreeSHA256}
	return &generatedImageTestAuthority{db: db, repository: repository, bindings: bindings, context: ctx, scope: scope, releaseScope: releaseScope, claim: claim, request: request, release: release, revision: revision, workingDirectory: workingDirectory}
}

func (f *generatedImageTestAuthority) selectBuild(t *testing.T) (*deployments.GeneratedImageExecution, deployments.GeneratedImageBuildSelection) {
	t.Helper()
	execution, err := f.repository.PinGeneratedImage(f.context, f.request)
	if err != nil {
		t.Fatal(err)
	}
	artifact, created, err := execution.SelectArtifact()
	if err != nil || !created {
		execution.Leave()
		t.Fatalf("select image build: artifact=%#v created=%t err=%v", artifact, created, err)
	}
	selection, err := execution.BuildSelection()
	if err != nil {
		execution.Leave()
		t.Fatal(err)
	}
	return execution, selection
}

func defaultGeneratedImageTestSource() (releasesnapshot.Release, deploymentplans.DeploymentPlanRevision) {
	appID, releaseID, configID, planID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	release := releasesnapshot.Release{ID: releaseID, AppID: appID, SourceProvider: "local", ResolvedSHA: digestForTest("source"), WorkspaceState: releasesnapshot.WorkspaceStateReady, WorkspaceTreeSHA256: digestForTest("tree"), ConfigurationRevisionID: configID, ConfigurationRevisionNumber: 1, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1}
	revision := deploymentplans.DeploymentPlanRevision{ID: planID, AppID: appID, RevisionNumber: 1, CanonicalDigest: digestForTest("plan"), Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Detector: deploymentplans.Detector{Name: "test", Version: "1", SourceStructuralFingerprint: digestForTest("structure")}, Source: deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: release.ResolvedSHA}, Components: []deploymentplans.Component{{Name: "app", Role: "server", RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "24", BuildCommand: "npm run build", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}}}}
	return release, revision
}

func digestForTest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func nullableTestText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func invalidateGeneratedImageJobAttempt(t *testing.T, authority *generatedImageTestAuthority) {
	t.Helper()
	result, err := authority.db.Exec(`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), authority.claim.JobID())
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("invalidate generated image job attempt: rows=%d err=%v", rows, err)
	}
}

type boundBuilderManager struct {
	*BuilderManager
	execution *deployments.GeneratedImageExecution
	selection deployments.GeneratedImageBuildSelection
	authority *generatedImageTestAuthority
}

func (m *boundBuilderManager) Prepare(ctx context.Context) (BuilderSession, error) {
	return m.prepare(ctx, m.selection)
}

func (m *boundBuilderManager) Close() {
	if m != nil && m.execution != nil {
		m.execution.Leave()
	}
}

func newBoundBuilderManager(t *testing.T, runner runtimeprocess.CommandRunner, options BuilderManagerOptions) *boundBuilderManager {
	t.Helper()
	release, revision := defaultGeneratedImageTestSource()
	authority := newGeneratedImageTestAuthority(t, release, revision, "app")
	manager, err := NewBuilderManager(authority.repository, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	execution, selection := authority.selectBuild(t)
	bound := &boundBuilderManager{BuilderManager: manager, execution: execution, selection: selection, authority: authority}
	t.Cleanup(bound.Close)
	return bound
}
