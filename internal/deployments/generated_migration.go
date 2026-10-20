package deployments

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedMigrationRequest binds the selected Ready image and immutable
// accepted-plan facts to the original deployment authority. Command, root,
// environment keys, network and process options deliberately are absent.
type GeneratedMigrationRequest struct {
	Claim                        deploymentexecution.Claim
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ComponentName                string
	CompilerVersion              string
	BuildDefinitionDigest        string
	PlanCanonicalDigest          string
	MigrationEvidenceDigest      string
	SourceProvider               string
	SourceRepositoryID           int64
	SourceResolvedDigest         string
	PlanSourceProvider           string
	PlanSourceRepositoryID       int64
	PlanSourceResolvedDigest     string
	ArchiveSHA256                string
	WorkspaceTreeSHA256          string
	ArtifactID                   string
	ArtifactAttemptNumber        int64
	ImageContentID               string
	Findings                     []Finding
}

type GeneratedMigrationExecution struct {
	state *generatedMigrationExecutionState
}

// GeneratedMigrationIdentity is an immutable copy of the authority and durable
// provenance pins needed by wrappers and diagnostics. It exposes no process
// context, deadline, command specification or result-proof setter.
type GeneratedMigrationIdentity struct {
	Claim                        deploymentexecution.Claim
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
}

type generatedMigrationExecutionState struct {
	mu         sync.Mutex
	changed    *sync.Cond
	repo       *Repository
	ctx        context.Context
	pinned     *deploymentexecution.Pinned
	request    GeneratedMigrationRequest
	components []string
	invocation *generatedMigrationInvocationState
	active     bool
	closing    bool
	left       bool
}

// GeneratedMigrationInvocation is an opaque, single-use permit minted only
// after the Pending-to-Running transaction commits. External packages cannot
// construct an implementation.
type GeneratedMigrationInvocation interface {
	Identity() GeneratedMigrationIdentity
	generatedMigrationInvocation() *generatedMigrationInvocationState
}

type generatedMigrationInvocationPermit struct {
	state *generatedMigrationInvocationState
}

func (p *generatedMigrationInvocationPermit) Identity() GeneratedMigrationIdentity {
	if p == nil || p.state == nil {
		return GeneratedMigrationIdentity{}
	}
	request := p.state.request
	return GeneratedMigrationIdentity{
		Claim: request.Claim, DeploymentID: request.DeploymentID, ReleaseID: request.ReleaseID,
		ConfigurationRevisionID: request.ConfigurationRevisionID, ConfigurationRevisionNumber: request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: request.DeploymentPlanRevisionNumber,
	}
}

func (p *generatedMigrationInvocationPermit) generatedMigrationInvocation() *generatedMigrationInvocationState {
	if p == nil {
		return nil
	}
	return p.state
}

type generatedMigrationInvocationState struct {
	mu               sync.Mutex
	repo             *Repository
	owner            *generatedMigrationExecutionState
	request          GeneratedMigrationRequest
	id               string
	consumed         bool
	commandLeft      bool
	exitObserved     bool
	exitCode         int
	absenceObserved  bool
	environmentClean bool
}

func (r *Repository) migrationConfigured() bool {
	return r != nil && r.ExecutionConfigured() && r.migrationPlans != nil && r.migrationPlans.ConfiguredFor(r.db) &&
		r.migrationConfiguration != nil && r.migrationConfiguration.ConfiguredFor(r.db) &&
		!nilInterface(r.migrationEnvironment) && !nilInterface(r.migrationProcess) && validGeneratedMigrationProcessOptions(r.migrationOptions)
}

func (r *Repository) PinGeneratedMigration(ctx context.Context, request GeneratedMigrationRequest) (*GeneratedMigrationExecution, error) {
	if !r.migrationConfigured() {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := validateGeneratedMigrationRequest(request); err != nil {
		return nil, err
	}
	pinned, err := r.executionGuard.PinScope(ctx)
	if err != nil {
		return nil, err
	}
	request.Findings = append([]Finding(nil), request.Findings...)
	state := &generatedMigrationExecutionState{repo: r, ctx: ctx, pinned: pinned, request: request}
	state.changed = sync.NewCond(&state.mu)
	revision, err := state.protectedPlanContext(ctx)
	if err != nil {
		pinned.Leave()
		return nil, err
	}
	state.components, err = generatedMigrationComponentNames(revision.Plan.Components)
	if err != nil {
		pinned.Leave()
		return nil, err
	}
	return &GeneratedMigrationExecution{state: state}, nil
}

func validateGeneratedMigrationRequest(request GeneratedMigrationRequest) error {
	if uuid.Validate(request.DeploymentID) != nil || request.Claim.JobID() == "" || uuid.Validate(request.Claim.ApplicationID()) != nil || request.Claim.Attempt() < 1 ||
		!validReleaseID(request.ReleaseID) || uuid.Validate(request.ConfigurationRevisionID) != nil || request.ConfigurationRevisionNumber < 1 ||
		uuid.Validate(request.DeploymentPlanRevisionID) != nil || request.DeploymentPlanRevisionNumber < 1 ||
		!validGeneratedImageText(request.ComponentName, 256) || !validGeneratedImageText(request.CompilerVersion, 128) ||
		!lowerHex(request.BuildDefinitionDigest, 64) || !lowerHex(request.PlanCanonicalDigest, 64) || !lowerHex(request.MigrationEvidenceDigest, 64) ||
		!validGeneratedImageText(request.SourceProvider, 32) || request.SourceRepositoryID < 0 || !validGeneratedImageText(request.SourceResolvedDigest, 128) ||
		!validGeneratedImageText(request.PlanSourceProvider, 32) || request.PlanSourceRepositoryID < 0 || !validGeneratedImageText(request.PlanSourceResolvedDigest, 128) ||
		(request.ArchiveSHA256 != "" && !lowerHex(request.ArchiveSHA256, 64)) || !lowerHex(request.WorkspaceTreeSHA256, 64) ||
		uuid.Validate(request.ArtifactID) != nil || request.ArtifactAttemptNumber < 1 || !validGeneratedMigrationImageID(request.ImageContentID) {
		return ErrInvalidDeployment
	}
	return validateFindings(request.Findings)
}

func (e *GeneratedMigrationExecution) ObserveSucceeded() (generatedruntimestate.Deployment, error) {
	state, done, err := e.begin()
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer done()
	if state.invocation != nil {
		return generatedruntimestate.Deployment{}, ErrInvalidTransition
	}
	if _, err := state.protectedPlanContext(state.ctx); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if err := state.readTx(generatedruntimestate.MigrationSucceeded); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	return generatedruntimestate.New(state.repo.db).Get(state.ctx, state.request.Claim.ApplicationID(), state.request.DeploymentID)
}

func (e *GeneratedMigrationExecution) Begin() (GeneratedMigrationInvocation, generatedruntimestate.Deployment, error) {
	state, done, err := e.begin()
	if err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	defer done()
	if state.invocation != nil {
		return nil, generatedruntimestate.Deployment{}, ErrInvalidTransition
	}
	if _, err := state.protectedPlanContext(state.ctx); err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	tx, err := state.writerTx()
	if err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	defer tx.Rollback()
	runtimeState := generatedruntimestate.New(state.repo.db)
	if err := runtimeState.BeginMigrationTx(state.ctx, tx, state.request.Claim.ApplicationID(), state.request.DeploymentID); err != nil {
		return nil, generatedruntimestate.Deployment{}, errors.Join(err, contextError(state.ctx))
	}
	if err := state.pinned.CheckGeneratedImageResultTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	if err := state.validateTx(state.ctx, tx, generatedruntimestate.MigrationRunning); err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	if err := contextError(state.ctx); err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, generatedruntimestate.Deployment{}, gateDatabaseError(state.ctx, err)
	}
	invocation := &generatedMigrationInvocationState{repo: state.repo, owner: state, request: state.request, id: uuid.NewString()}
	state.invocation = invocation
	if state.repo.afterGeneratedMigrationBeginCommit != nil {
		state.repo.afterGeneratedMigrationBeginCommit()
	}
	value, err := runtimeState.Get(state.ctx, state.request.Claim.ApplicationID(), state.request.DeploymentID)
	if err != nil {
		return nil, generatedruntimestate.Deployment{}, err
	}
	return &generatedMigrationInvocationPermit{state: invocation}, value, nil
}

func (e *GeneratedMigrationExecution) RecordSucceeded() (generatedruntimestate.Deployment, error) {
	return e.recordObserved(true)
}

func (e *GeneratedMigrationExecution) RecordObservedFailure() (generatedruntimestate.Deployment, error) {
	return e.recordObserved(false)
}

func (e *GeneratedMigrationExecution) recordObserved(success bool) (generatedruntimestate.Deployment, error) {
	state, done, err := e.begin()
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer done()
	invocation := state.invocation
	if invocation == nil {
		return generatedruntimestate.Deployment{}, ErrInvalidTransition
	}
	invocation.mu.Lock()
	valid := invocation.repo == state.repo && invocation.owner == state && invocation.commandLeft && invocation.exitObserved
	if success {
		valid = valid && invocation.exitCode == 0 && invocation.absenceObserved && invocation.environmentClean
	} else {
		valid = valid && invocation.exitCode != 0
	}
	invocation.mu.Unlock()
	if !valid {
		return generatedruntimestate.Deployment{}, ErrInvalidTransition
	}
	if _, err := state.protectedPlanContext(state.ctx); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	tx, err := state.writerTx()
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer tx.Rollback()
	if err := state.pinned.CheckGeneratedImageResultTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if err := state.validateTx(state.ctx, tx, generatedruntimestate.MigrationRunning); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	runtimeState := generatedruntimestate.New(state.repo.db)
	if success {
		err = runtimeState.RecordMigrationSucceededTx(state.ctx, tx, state.request.Claim.ApplicationID(), state.request.DeploymentID)
	} else {
		err = runtimeState.RecordMigrationFailedTx(state.ctx, tx, state.request.Claim.ApplicationID(), state.request.DeploymentID)
	}
	if err != nil {
		return generatedruntimestate.Deployment{}, errors.Join(err, contextError(state.ctx))
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.Deployment{}, gateDatabaseError(state.ctx, err)
	}
	return runtimeState.Get(state.ctx, state.request.Claim.ApplicationID(), state.request.DeploymentID)
}

func (e *GeneratedMigrationExecution) FinishPreparation() (generatedruntimestate.Deployment, error) {
	state, done, err := e.begin()
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer done()
	if _, err := state.protectedPlanContext(state.ctx); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	tx, err := state.writerTx()
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer tx.Rollback()
	if err := state.pinned.CheckGeneratedImageResultTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if err := state.validateTx(state.ctx, tx, generatedruntimestate.MigrationSucceeded); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	runtimeState := generatedruntimestate.New(state.repo.db)
	if _, err := runtimeState.FinishMigrationPreparationTx(state.ctx, tx, state.request.Claim.ApplicationID(), state.request.DeploymentID); err != nil {
		return generatedruntimestate.Deployment{}, errors.Join(err, contextError(state.ctx))
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.Deployment{}, gateDatabaseError(state.ctx, err)
	}
	return runtimeState.Get(state.ctx, state.request.Claim.ApplicationID(), state.request.DeploymentID)
}

func (e *GeneratedMigrationExecution) Leave() {
	if e == nil || e.state == nil {
		return
	}
	s := e.state
	s.mu.Lock()
	if s.closing {
		for !s.left {
			s.changed.Wait()
		}
		s.mu.Unlock()
		return
	}
	s.closing = true
	for s.active {
		s.changed.Wait()
	}
	pinned := s.pinned
	s.pinned = nil
	s.mu.Unlock()
	if pinned != nil {
		pinned.Leave()
	}
	s.mu.Lock()
	s.repo = nil
	s.ctx = nil
	s.left = true
	s.changed.Broadcast()
	s.mu.Unlock()
}

func (e *GeneratedMigrationExecution) begin() (*generatedMigrationExecutionState, func(), error) {
	if e == nil || e.state == nil {
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	s := e.state
	s.mu.Lock()
	if s.closing || s.left || s.active || s.repo == nil || s.ctx == nil || s.pinned == nil {
		s.mu.Unlock()
		return nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	s.active = true
	s.mu.Unlock()
	if err := contextError(s.ctx); err != nil {
		s.mu.Lock()
		s.active = false
		s.changed.Broadcast()
		s.mu.Unlock()
		return nil, nil, err
	}
	done := func() {
		s.mu.Lock()
		s.active = false
		s.changed.Broadcast()
		s.mu.Unlock()
	}
	return s, done, nil
}

func (s *generatedMigrationExecutionState) writerTx() (*sql.Tx, error) {
	tx, err := s.repo.db.BeginTx(s.ctx, nil)
	if err != nil {
		return nil, gateDatabaseError(s.ctx, err)
	}
	if _, err := tx.ExecContext(s.ctx, `UPDATE jobs SET id=id WHERE 0`); err != nil {
		_ = tx.Rollback()
		return nil, gateDatabaseError(s.ctx, err)
	}
	return tx, nil
}

func (s *generatedMigrationExecutionState) readTx(expected generatedruntimestate.MigrationState) error {
	tx, err := s.repo.db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	defer tx.Rollback()
	if err := s.pinned.CheckGeneratedImageResultTx(tx, s.request.Claim, s.request.DeploymentID); err != nil {
		return err
	}
	if err := s.validateTx(s.ctx, tx, expected); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *generatedMigrationExecutionState) validateTx(ctx context.Context, tx *sql.Tx, expected generatedruntimestate.MigrationState) error {
	network := generatedNetworkExecutionState{repo: s.repo, ctx: ctx, request: GeneratedNetworkRequest{
		Claim: s.request.Claim, DeploymentID: s.request.DeploymentID, ReleaseID: s.request.ReleaseID,
		ConfigurationRevisionID: s.request.ConfigurationRevisionID, ConfigurationRevisionNumber: s.request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: s.request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: s.request.DeploymentPlanRevisionNumber,
		Findings: s.request.Findings,
	}}
	if err := network.validateTx(tx); err != nil {
		return err
	}
	var phase generatedruntimestate.Phase
	var migration generatedruntimestate.MigrationState
	if err := tx.QueryRowContext(ctx, `SELECT phase,migration_state FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?`, s.request.DeploymentID, s.request.Claim.ApplicationID()).Scan(&phase, &migration); err != nil {
		return gateDatabaseError(ctx, err)
	}
	if phase != generatedruntimestate.PhaseMigrating || migration != expected {
		return deploymentexecution.ErrStageInvalid
	}
	var provider, resolved, archive, tree, planID, workspaceState string
	var repositoryID, planNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT source_provider,repository_id,resolved_sha,COALESCE(archive_sha256,''),COALESCE(workspace_tree_sha256,''),workspace_state,COALESCE(deployment_plan_revision_id,''),COALESCE(deployment_plan_revision_number,0) FROM releases WHERE id=? AND app_id=?`, s.request.ReleaseID, s.request.Claim.ApplicationID()).Scan(&provider, &repositoryID, &resolved, &archive, &tree, &workspaceState, &planID, &planNumber); err != nil {
		return gateDatabaseError(ctx, err)
	}
	if provider != s.request.SourceProvider || repositoryID != s.request.SourceRepositoryID || resolved != s.request.SourceResolvedDigest || archive != s.request.ArchiveSHA256 || tree != s.request.WorkspaceTreeSHA256 || workspaceState != "ready" || planID != s.request.DeploymentPlanRevisionID || planNumber != s.request.DeploymentPlanRevisionNumber {
		return ErrInvalidDeployment
	}
	var canonical, planProvider, planResolved, migrationDigest string
	var planRepositoryID, componentCount int64
	if err := tx.QueryRowContext(ctx, `SELECT canonical_digest,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,migration_evidence_digest,component_count FROM deployment_plan_revisions WHERE id=? AND app_id=? AND revision_number=?`, s.request.DeploymentPlanRevisionID, s.request.Claim.ApplicationID(), s.request.DeploymentPlanRevisionNumber).Scan(&canonical, &planProvider, &planRepositoryID, &planResolved, &migrationDigest, &componentCount); err != nil {
		return gateDatabaseError(ctx, err)
	}
	if canonical != s.request.PlanCanonicalDigest || planProvider != s.request.PlanSourceProvider || planRepositoryID != s.request.PlanSourceRepositoryID || planResolved != s.request.PlanSourceResolvedDigest || migrationDigest != s.request.MigrationEvidenceDigest || componentCount != int64(len(s.components)) {
		return ErrInvalidDeployment
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.component_name,c.state,COALESCE(c.container_name,''),COALESCE(c.container_id,''),COALESCE(c.image_artifact_id,''),COALESCE(a.state,''),COALESCE(a.release_id,''),COALESCE(a.deployment_plan_revision_id,''),COALESCE(a.deployment_plan_revision_number,0),COALESCE(a.component_id,'') FROM generated_runtime_components c LEFT JOIN generated_image_artifacts a ON a.id=c.image_artifact_id WHERE c.deployment_id=? ORDER BY c.component_name`, s.request.DeploymentID)
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var name, componentState, containerName, containerID, artifactID, artifactState, artifactRelease, artifactPlan, artifactComponent string
		var artifactPlanNumber int64
		if err := rows.Scan(&name, &componentState, &containerName, &containerID, &artifactID, &artifactState, &artifactRelease, &artifactPlan, &artifactPlanNumber, &artifactComponent); err != nil {
			return gateDatabaseError(ctx, err)
		}
		if index >= len(s.components) || name != s.components[index] || componentState != string(generatedruntimestate.ComponentImageReady) || containerName != "" || containerID != "" ||
			artifactID == "" || artifactState != string(generatedimagestate.ArtifactReady) || artifactRelease != s.request.ReleaseID || artifactPlan != s.request.DeploymentPlanRevisionID || artifactPlanNumber != s.request.DeploymentPlanRevisionNumber || artifactComponent != name {
			return ErrInvalidDeployment
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return gateDatabaseError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return gateDatabaseError(ctx, err)
	}
	if index != len(s.components) {
		return ErrInvalidDeployment
	}
	var componentState generatedruntimestate.ComponentState
	var artifactID string
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(image_artifact_id,'') FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, s.request.DeploymentID, s.request.ComponentName).Scan(&componentState, &artifactID); err != nil {
		return gateDatabaseError(ctx, err)
	}
	if componentState != generatedruntimestate.ComponentImageReady || artifactID != s.request.ArtifactID {
		return ErrInvalidDeployment
	}
	artifact, err := generatedimagestate.NewRepository(s.repo.db).GetTx(ctx, tx, artifactID)
	if err != nil {
		return err
	}
	if artifact.State != generatedimagestate.ArtifactReady || artifact.ReleaseID != s.request.ReleaseID || artifact.DeploymentPlanRevisionID != s.request.DeploymentPlanRevisionID || artifact.DeploymentPlanRevisionNumber != s.request.DeploymentPlanRevisionNumber || artifact.ComponentID != s.request.ComponentName || artifact.CompilerVersion != s.request.CompilerVersion || artifact.BuildDefinitionDigest != s.request.BuildDefinitionDigest || artifact.AttemptNumber != s.request.ArtifactAttemptNumber || artifact.ImageContentID != s.request.ImageContentID {
		return ErrInvalidDeployment
	}
	return contextError(ctx)
}

func (s *generatedMigrationExecutionState) protectedPlanContext(ctx context.Context) (deploymentplans.DeploymentPlanRevision, error) {
	if s == nil || s.repo == nil || s.repo.migrationPlans == nil {
		return deploymentplans.DeploymentPlanRevision{}, ErrInvalidDeployment
	}
	if err := contextError(ctx); err != nil {
		return deploymentplans.DeploymentPlanRevision{}, err
	}
	revision, err := s.repo.migrationPlans.GetRevision(ctx, s.request.Claim.ApplicationID(), s.request.DeploymentPlanRevisionID, s.request.DeploymentPlanRevisionNumber)
	if err != nil {
		return deploymentplans.DeploymentPlanRevision{}, err
	}
	if (revision.State != deploymentplans.RevisionAccepted && revision.State != deploymentplans.RevisionSuperseded) || revision.ID != s.request.DeploymentPlanRevisionID || revision.AppID != s.request.Claim.ApplicationID() || revision.RevisionNumber != s.request.DeploymentPlanRevisionNumber || revision.CanonicalDigest != s.request.PlanCanonicalDigest || revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode || revision.Plan.Source.Provider != s.request.PlanSourceProvider || revision.Plan.Source.RepositoryID != s.request.PlanSourceRepositoryID || revision.Plan.Source.ResolvedDigest != s.request.PlanSourceResolvedDigest || revision.Plan.Migration == nil || revision.Plan.Migration.ComponentName != s.request.ComponentName || revision.Plan.Migration.EvidenceDigest != s.request.MigrationEvidenceDigest || revision.Plan.Migration.Approval.Status != deploymentplans.MigrationApprovalApproved {
		return deploymentplans.DeploymentPlanRevision{}, ErrInvalidDeployment
	}
	return revision, nil
}

func generatedMigrationComponentNames(components []deploymentplans.Component) ([]string, error) {
	names := make([]string, len(components))
	for index, component := range components {
		if !validGeneratedImageText(component.Name, 256) {
			return nil, ErrInvalidDeployment
		}
		names[index] = component.Name
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, ErrInvalidDeployment
	}
	for index := 1; index < len(names); index++ {
		if names[index] == names[index-1] {
			return nil, ErrInvalidDeployment
		}
	}
	return names, nil
}
