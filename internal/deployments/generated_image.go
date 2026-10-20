package deployments

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedImageRequest binds one generated component's derived build
// definition to the original deployment authority and immutable source facts.
type GeneratedImageRequest struct {
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
	SourceProvider               string
	SourceRepositoryID           int64
	SourceResolvedDigest         string
	PlanSourceProvider           string
	PlanSourceRepositoryID       int64
	PlanSourceResolvedDigest     string
	ArchiveSHA256                string
	WorkspaceTreeSHA256          string
	Findings                     []Finding
}

type GeneratedImageExecution struct{ state *generatedImageExecutionState }

// GeneratedImageBuildSelection is an opaque proof that this invocation
// created the exact selected Building attempt. Only SelectArtifact can mint it.
type GeneratedImageBuildSelection struct {
	request         GeneratedImageRequest
	artifactID      string
	artifactAttempt int64
	createdBy       *generatedImageExecutionState
}

type generatedImageExecutionState struct {
	mu               sync.Mutex
	changed          *sync.Cond
	repo             *Repository
	ctx              context.Context
	pinned           *deploymentexecution.Pinned
	request          GeneratedImageRequest
	selected         generatedimagestate.Artifact
	selectedCreated  bool
	selectedAttached bool
	replacementUsed  bool
	active           bool
	closing          bool
	left             bool
}

func (r *Repository) PinGeneratedImage(ctx context.Context, request GeneratedImageRequest) (*GeneratedImageExecution, error) {
	if !r.ExecutionConfigured() || errGeneratedImageRequest(request) != nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	pinned, err := r.executionGuard.PinScope(ctx)
	if err != nil {
		return nil, err
	}
	request.Findings = append([]Finding(nil), request.Findings...)
	state := &generatedImageExecutionState{repo: r, ctx: ctx, pinned: pinned, request: request}
	state.changed = sync.NewCond(&state.mu)
	return &GeneratedImageExecution{state: state}, nil
}

func errGeneratedImageRequest(request GeneratedImageRequest) error {
	if uuid.Validate(request.DeploymentID) != nil || request.Claim.JobID() == "" || uuid.Validate(request.Claim.ApplicationID()) != nil || request.Claim.Attempt() < 1 ||
		!validReleaseID(request.ReleaseID) || uuid.Validate(request.ConfigurationRevisionID) != nil || request.ConfigurationRevisionNumber < 1 ||
		uuid.Validate(request.DeploymentPlanRevisionID) != nil || request.DeploymentPlanRevisionNumber < 1 ||
		!validGeneratedImageText(request.ComponentName, 256) || !validGeneratedImageText(request.CompilerVersion, 128) || !lowerHex(request.BuildDefinitionDigest, 64) ||
		!lowerHex(request.PlanCanonicalDigest, 64) || !validGeneratedImageText(request.SourceProvider, 32) || request.SourceRepositoryID < 0 ||
		!validGeneratedImageText(request.SourceResolvedDigest, 128) || !lowerHex(request.WorkspaceTreeSHA256, 64) ||
		!validGeneratedImageText(request.PlanSourceProvider, 32) || request.PlanSourceRepositoryID < 0 || !validGeneratedImageText(request.PlanSourceResolvedDigest, 128) ||
		(request.ArchiveSHA256 != "" && !lowerHex(request.ArchiveSHA256, 64)) {
		return ErrInvalidDeployment
	}
	return validateFindings(request.Findings)
}

func validGeneratedImageText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (e *GeneratedImageExecution) SelectArtifact() (generatedimagestate.Artifact, bool, error) {
	state, done, err := e.begin()
	if err != nil {
		return generatedimagestate.Artifact{}, false, err
	}
	defer done()
	if state.selected.ID != "" {
		return generatedimagestate.Artifact{}, false, ErrInvalidTransition
	}
	tx, err := state.writerTx()
	if err != nil {
		return generatedimagestate.Artifact{}, false, err
	}
	defer tx.Rollback()
	if err := state.pinned.CheckGeneratedImageSelectionTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return generatedimagestate.Artifact{}, false, err
	}
	if _, err := state.validateTx(tx, generatedruntimestate.ComponentPending, "", 0); err != nil {
		return generatedimagestate.Artifact{}, false, err
	}
	artifact, created, err := generatedimagestate.NewRepository(state.repo.db).BeginTx(state.ctx, tx, state.artifactInput())
	if err != nil {
		return generatedimagestate.Artifact{}, false, gateDatabaseError(state.ctx, err)
	}
	if err := contextError(state.ctx); err != nil {
		return generatedimagestate.Artifact{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return generatedimagestate.Artifact{}, false, gateDatabaseError(state.ctx, err)
	}
	state.selected, state.selectedCreated = artifact, created
	return artifact, created, nil
}

func (e *GeneratedImageExecution) ObservePersistedImage() (generatedimagestate.Artifact, error) {
	state, done, err := e.begin()
	if err != nil {
		return generatedimagestate.Artifact{}, err
	}
	defer done()
	if state.selected.ID != "" {
		return generatedimagestate.Artifact{}, ErrInvalidTransition
	}
	tx, err := state.repo.db.BeginTx(state.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(state.ctx, err)
	}
	defer tx.Rollback()
	if err := state.pinned.CheckGeneratedImageObservationTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	artifact, err := state.validateTx(tx, generatedruntimestate.ComponentImageReady, "", 0)
	if err != nil {
		return generatedimagestate.Artifact{}, err
	}
	if artifact.State != generatedimagestate.ArtifactReady {
		return generatedimagestate.Artifact{}, ErrInvalidDeployment
	}
	if err := tx.Commit(); err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(state.ctx, err)
	}
	state.selected = artifact
	state.selectedAttached = true
	return artifact, nil
}

// HasPersistedImage reports which of the two fixed image preparation states
// is currently durable. It does not authorize either follow-up operation;
// SelectArtifact and ObservePersistedImage each perform their own fresh check.
func (e *GeneratedImageExecution) HasPersistedImage() (bool, error) {
	state, done, err := e.begin()
	if err != nil {
		return false, err
	}
	defer done()
	if state.selected.ID != "" {
		return false, ErrInvalidTransition
	}
	tx, err := state.repo.db.BeginTx(state.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, gateDatabaseError(state.ctx, err)
	}
	defer tx.Rollback()
	if err := state.pinned.CheckGeneratedImageObservationTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return false, err
	}
	var componentState generatedruntimestate.ComponentState
	if err := tx.QueryRowContext(state.ctx, `SELECT state FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, state.request.DeploymentID, state.request.ComponentName).Scan(&componentState); err != nil {
		return false, gateDatabaseError(state.ctx, err)
	}
	switch componentState {
	case generatedruntimestate.ComponentPending:
		if _, err := state.validateTx(tx, generatedruntimestate.ComponentPending, "", 0); err != nil {
			return false, err
		}
	case generatedruntimestate.ComponentImageReady:
		artifact, err := state.validateTx(tx, generatedruntimestate.ComponentImageReady, "", 0)
		if err != nil {
			return false, err
		}
		if artifact.State != generatedimagestate.ArtifactReady {
			return false, imageSemanticError(state.ctx, ErrInvalidDeployment)
		}
	default:
		return false, imageSemanticError(state.ctx, deploymentexecution.ErrStageInvalid)
	}
	if err := contextError(state.ctx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, gateDatabaseError(state.ctx, err)
	}
	return componentState == generatedruntimestate.ComponentImageReady, nil
}

func (e *GeneratedImageExecution) RecordBuiltImageReady(imageContentID string) error {
	state, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	if !state.selectedCreated || state.selected.State != generatedimagestate.ArtifactBuilding {
		return ErrInvalidTransition
	}
	return state.resultTx(generatedruntimestate.ComponentPending, func(tx *sql.Tx) (generatedimagestate.Artifact, error) {
		artifact, err := generatedimagestate.NewRepository(state.repo.db).CompleteTx(state.ctx, tx, state.selected.ID, imageContentID)
		if err != nil {
			return generatedimagestate.Artifact{}, err
		}
		if artifact.AttemptNumber != state.selected.AttemptNumber {
			return generatedimagestate.Artifact{}, ErrInvalidDeployment
		}
		if err := generatedruntimestate.New(state.repo.db).RecordImageReadyTx(state.ctx, tx, state.request.Claim.ApplicationID(), state.request.DeploymentID, state.request.ComponentName, artifact.ID); err != nil {
			return generatedimagestate.Artifact{}, err
		}
		return artifact, nil
	})
}

func (e *GeneratedImageExecution) RecordReusedImageReady() error {
	state, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	if state.selectedCreated || state.selected.State != generatedimagestate.ArtifactReady {
		return ErrInvalidTransition
	}
	expected := generatedruntimestate.ComponentPending
	if state.selectedAttached {
		expected = generatedruntimestate.ComponentImageReady
	}
	return state.resultTx(expected, func(tx *sql.Tx) (generatedimagestate.Artifact, error) {
		if err := generatedruntimestate.New(state.repo.db).RecordImageReadyTx(state.ctx, tx, state.request.Claim.ApplicationID(), state.request.DeploymentID, state.request.ComponentName, state.selected.ID); err != nil {
			return generatedimagestate.Artifact{}, err
		}
		return state.selected, nil
	})
}

// ReplaceMissingReusableArtifact atomically retires the selected reusable
// artifact and selects the definition's one replacement attempt. Canonical
// physical evidence is required by the compiler before it calls this method.
func (e *GeneratedImageExecution) ReplaceMissingReusableArtifact() (generatedimagestate.Artifact, error) {
	state, done, err := e.begin()
	if err != nil {
		return generatedimagestate.Artifact{}, err
	}
	defer done()
	if state.selectedCreated || state.replacementUsed || state.selected.State != generatedimagestate.ArtifactReady {
		return generatedimagestate.Artifact{}, ErrInvalidTransition
	}
	tx, err := state.writerTx()
	if err != nil {
		return generatedimagestate.Artifact{}, err
	}
	defer tx.Rollback()
	if err := state.pinned.CheckGeneratedImageResultTx(tx, state.request.Claim, state.request.DeploymentID); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	if _, err := state.validateTx(tx, generatedruntimestate.ComponentPending, state.selected.ID, state.selected.AttemptNumber); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	artifacts := generatedimagestate.NewRepository(state.repo.db)
	if _, err := artifacts.MarkUnavailableTx(state.ctx, tx, state.selected.ID); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	replacement, created, err := artifacts.BeginTx(state.ctx, tx, state.artifactInput())
	if err != nil {
		return generatedimagestate.Artifact{}, err
	}
	if !created || replacement.State != generatedimagestate.ArtifactBuilding || replacement.AttemptNumber <= state.selected.AttemptNumber {
		return generatedimagestate.Artifact{}, ErrInvalidTransition
	}
	if err := contextError(state.ctx); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(state.ctx, err)
	}
	state.selected = replacement
	state.selectedCreated = true
	state.replacementUsed = true
	return replacement, nil
}

func (e *GeneratedImageExecution) RecordObservedBuildFailure(code generatedimagestate.DiagnosticCode) error {
	state, done, err := e.begin()
	if err != nil {
		return err
	}
	defer done()
	if !state.selectedCreated || state.selected.State != generatedimagestate.ArtifactBuilding {
		return ErrInvalidTransition
	}
	return state.resultTx(generatedruntimestate.ComponentPending, func(tx *sql.Tx) (generatedimagestate.Artifact, error) {
		artifact, err := generatedimagestate.NewRepository(state.repo.db).FailTx(state.ctx, tx, state.selected.ID, code)
		if err != nil {
			return generatedimagestate.Artifact{}, err
		}
		if artifact.AttemptNumber != state.selected.AttemptNumber {
			return generatedimagestate.Artifact{}, ErrInvalidDeployment
		}
		return artifact, nil
	})
}

func (e *GeneratedImageExecution) SelectedArtifact() (generatedimagestate.Artifact, bool) {
	if e == nil || e.state == nil {
		return generatedimagestate.Artifact{}, false
	}
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.left || s.active || s.selected.ID == "" {
		return generatedimagestate.Artifact{}, false
	}
	return s.selected, s.selectedCreated
}

func (e *GeneratedImageExecution) BuildSelection() (GeneratedImageBuildSelection, error) {
	if e == nil || e.state == nil {
		return GeneratedImageBuildSelection{}, deploymentexecution.ErrScopeUnavailable
	}
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.left {
		return GeneratedImageBuildSelection{}, deploymentexecution.ErrScopeUnavailable
	}
	if s.active || !s.selectedCreated || s.selected.State != generatedimagestate.ArtifactBuilding || s.selected.ID == "" || s.selected.AttemptNumber < 1 {
		return GeneratedImageBuildSelection{}, ErrInvalidTransition
	}
	return GeneratedImageBuildSelection{request: s.request, artifactID: s.selected.ID, artifactAttempt: s.selected.AttemptNumber, createdBy: s}, nil
}

func (e *GeneratedImageExecution) Leave() {
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
	p := s.pinned
	s.pinned = nil
	s.mu.Unlock()
	if p != nil {
		p.Leave()
	}
	s.mu.Lock()
	s.repo = nil
	s.ctx = nil
	s.left = true
	s.changed.Broadcast()
	s.mu.Unlock()
}

func (e *GeneratedImageExecution) begin() (*generatedImageExecutionState, func(), error) {
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
	done := func() { s.mu.Lock(); s.active = false; s.changed.Broadcast(); s.mu.Unlock() }
	return s, done, nil
}

func (s *generatedImageExecutionState) writerTx() (*sql.Tx, error) {
	tx, err := s.repo.db.BeginTx(s.ctx, nil)
	if err != nil {
		return nil, gateDatabaseError(s.ctx, err)
	}
	if _, err = tx.ExecContext(s.ctx, `UPDATE jobs SET id=id WHERE 0`); err != nil {
		_ = tx.Rollback()
		return nil, gateDatabaseError(s.ctx, err)
	}
	return tx, nil
}

func (s *generatedImageExecutionState) resultTx(componentState generatedruntimestate.ComponentState, apply func(*sql.Tx) (generatedimagestate.Artifact, error)) error {
	tx, err := s.writerTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.pinned.CheckGeneratedImageResultTx(tx, s.request.Claim, s.request.DeploymentID); err != nil {
		return err
	}
	if _, err := s.validateTx(tx, componentState, s.selected.ID, s.selected.AttemptNumber); err != nil {
		return err
	}
	selected, err := apply(tx)
	if err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	s.selected = selected
	return nil
}

func (s *generatedImageExecutionState) artifactInput() generatedimagestate.BeginArtifactInput {
	return generatedimagestate.BeginArtifactInput{ReleaseID: s.request.ReleaseID, DeploymentPlanRevisionID: s.request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: s.request.DeploymentPlanRevisionNumber, ComponentID: s.request.ComponentName, CompilerVersion: s.request.CompilerVersion, BuildDefinitionDigest: s.request.BuildDefinitionDigest}
}

func (s *generatedImageExecutionState) validateTx(tx *sql.Tx, componentState generatedruntimestate.ComponentState, selectedID string, selectedAttempt int64) (generatedimagestate.Artifact, error) {
	network := generatedNetworkExecutionState{repo: s.repo, ctx: s.ctx, pinned: s.pinned, request: GeneratedNetworkRequest{Claim: s.request.Claim, DeploymentID: s.request.DeploymentID, ReleaseID: s.request.ReleaseID, ConfigurationRevisionID: s.request.ConfigurationRevisionID, ConfigurationRevisionNumber: s.request.ConfigurationRevisionNumber, DeploymentPlanRevisionID: s.request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: s.request.DeploymentPlanRevisionNumber, Findings: s.request.Findings}}
	if err := network.validateTx(tx); err != nil {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, err)
	}
	if err := contextError(s.ctx); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	var phase generatedruntimestate.Phase
	if err := tx.QueryRowContext(s.ctx, `SELECT phase FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?`, s.request.DeploymentID, s.request.Claim.ApplicationID()).Scan(&phase); err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(s.ctx, err)
	}
	if phase != generatedruntimestate.PhaseBuilding {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, deploymentexecution.ErrStageInvalid)
	}
	var provider, resolved, archive, tree, configID, planID, state string
	var repositoryID, configNumber, planNumber int64
	err := tx.QueryRowContext(s.ctx, `SELECT source_provider,repository_id,resolved_sha,COALESCE(archive_sha256,''),COALESCE(workspace_tree_sha256,''),workspace_state,COALESCE(configuration_revision_id,''),configuration_revision_number,COALESCE(deployment_plan_revision_id,''),COALESCE(deployment_plan_revision_number,0) FROM releases WHERE id=? AND app_id=?`, s.request.ReleaseID, s.request.Claim.ApplicationID()).Scan(&provider, &repositoryID, &resolved, &archive, &tree, &state, &configID, &configNumber, &planID, &planNumber)
	if err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(s.ctx, err)
	}
	if s.repo.afterGeneratedImageIdentityRead != nil {
		s.repo.afterGeneratedImageIdentityRead()
	}
	if provider != s.request.SourceProvider || repositoryID != s.request.SourceRepositoryID || resolved != s.request.SourceResolvedDigest || archive != s.request.ArchiveSHA256 || tree != s.request.WorkspaceTreeSHA256 || state != "ready" || configID != s.request.ConfigurationRevisionID || configNumber != s.request.ConfigurationRevisionNumber || planID != s.request.DeploymentPlanRevisionID || planNumber != s.request.DeploymentPlanRevisionNumber {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	var canonical, planProvider, planResolved string
	var planRepositoryID int64
	if err := tx.QueryRowContext(s.ctx, `SELECT canonical_digest,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest FROM deployment_plan_revisions WHERE id=? AND app_id=? AND revision_number=?`, s.request.DeploymentPlanRevisionID, s.request.Claim.ApplicationID(), s.request.DeploymentPlanRevisionNumber).Scan(&canonical, &planProvider, &planRepositoryID, &planResolved); err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(s.ctx, err)
	}
	if canonical != s.request.PlanCanonicalDigest || planProvider != s.request.PlanSourceProvider || planRepositoryID != s.request.PlanSourceRepositoryID || planResolved != s.request.PlanSourceResolvedDigest {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	var candidateRisk int
	if err := tx.QueryRowContext(s.ctx, `SELECT EXISTS(SELECT 1 FROM generated_runtime_components
		WHERE deployment_id=? AND (state NOT IN ('pending','image_ready') OR container_name IS NOT NULL OR container_id IS NOT NULL))`, s.request.DeploymentID).Scan(&candidateRisk); err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(s.ctx, err)
	}
	if candidateRisk != 0 {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	var stateValue generatedruntimestate.ComponentState
	var artifactID, containerName, containerID string
	if err := tx.QueryRowContext(s.ctx, `SELECT state,COALESCE(image_artifact_id,''),COALESCE(container_name,''),COALESCE(container_id,'') FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, s.request.DeploymentID, s.request.ComponentName).Scan(&stateValue, &artifactID, &containerName, &containerID); err != nil {
		return generatedimagestate.Artifact{}, gateDatabaseError(s.ctx, err)
	}
	if stateValue != componentState || containerName != "" || containerID != "" {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	if componentState == generatedruntimestate.ComponentPending {
		if artifactID != "" {
			return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
		}
		if selectedID == "" {
			return generatedimagestate.Artifact{}, nil
		}
	}
	if selectedID == "" {
		selectedID = artifactID
	}
	if artifactID != "" && artifactID != selectedID {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	artifact, err := generatedimagestate.NewRepository(s.repo.db).GetTx(s.ctx, tx, selectedID)
	if err != nil {
		return generatedimagestate.Artifact{}, errors.Join(err, contextError(s.ctx))
	}
	if artifact.ReleaseID != s.request.ReleaseID || artifact.DeploymentPlanRevisionID != s.request.DeploymentPlanRevisionID || artifact.DeploymentPlanRevisionNumber != s.request.DeploymentPlanRevisionNumber || artifact.ComponentID != s.request.ComponentName || artifact.CompilerVersion != s.request.CompilerVersion || artifact.BuildDefinitionDigest != s.request.BuildDefinitionDigest || (selectedAttempt > 0 && artifact.AttemptNumber != selectedAttempt) {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	if s.selected.ID != "" && selectedID != "" && (artifact.State != s.selected.State || artifact.ImageContentID != s.selected.ImageContentID) {
		return generatedimagestate.Artifact{}, imageSemanticError(s.ctx, ErrInvalidDeployment)
	}
	if err := contextError(s.ctx); err != nil {
		return generatedimagestate.Artifact{}, err
	}
	return artifact, nil
}

func imageSemanticError(ctx context.Context, semantic error) error {
	if canceled := contextError(ctx); canceled != nil {
		return errors.Join(canceled, semantic)
	}
	return semantic
}

// GeneratedImageCommandExecution is a separately pinned, fixed-command view
// over an already selected newly-created Building artifact.
type GeneratedImageCommandExecution struct{ image *GeneratedImageExecution }

func (r *Repository) PinGeneratedImageCommand(selection GeneratedImageBuildSelection) (*GeneratedImageCommandExecution, error) {
	if selection.createdBy == nil || selection.artifactID == "" || selection.artifactAttempt < 1 {
		return nil, ErrInvalidTransition
	}
	selection.createdBy.mu.Lock()
	validOrigin := !selection.createdBy.closing && !selection.createdBy.left && !selection.createdBy.active && selection.createdBy.repo == r && selection.createdBy.ctx != nil &&
		selection.createdBy.selectedCreated &&
		selection.createdBy.selected.ID == selection.artifactID && selection.createdBy.selected.AttemptNumber == selection.artifactAttempt
	originalContext := selection.createdBy.ctx
	if !validOrigin {
		selection.createdBy.mu.Unlock()
		return nil, ErrInvalidTransition
	}
	if err := contextError(originalContext); err != nil {
		selection.createdBy.mu.Unlock()
		return nil, err
	}
	image, err := r.PinGeneratedImage(originalContext, selection.request)
	selection.createdBy.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s := image.state
	s.selected = generatedimagestate.Artifact{ID: selection.artifactID, AttemptNumber: selection.artifactAttempt, State: generatedimagestate.ArtifactBuilding}
	s.selectedCreated = true
	state, done, err := image.begin()
	if err != nil {
		image.Leave()
		return nil, err
	}
	tx, err := state.repo.db.BeginTx(state.ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		defer tx.Rollback()
		err = state.pinned.CheckGeneratedImageObservationTx(tx, state.request.Claim, state.request.DeploymentID)
		if err == nil {
			var a generatedimagestate.Artifact
			a, err = state.validateTx(tx, generatedruntimestate.ComponentPending, selection.artifactID, selection.artifactAttempt)
			if err == nil && (a.State != generatedimagestate.ArtifactBuilding) {
				err = ErrInvalidDeployment
			}
		}
	}
	done()
	if err != nil {
		image.Leave()
		return nil, err
	}
	return &GeneratedImageCommandExecution{image: image}, nil
}

func (e *GeneratedImageCommandExecution) Leave() {
	if e != nil && e.image != nil {
		e.image.Leave()
	}
}

type generatedImageCommandCheck func(*deploymentexecution.Pinned, *sql.Tx, deploymentexecution.Claim, string) error

func (e *GeneratedImageCommandExecution) check(check generatedImageCommandCheck) error {
	if e == nil || e.image == nil {
		return deploymentexecution.ErrScopeUnavailable
	}
	s, done, err := e.image.begin()
	if err != nil {
		return err
	}
	defer done()
	tx, err := s.repo.db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	defer tx.Rollback()
	if err := check(s.pinned, tx, s.request.Claim, s.request.DeploymentID); err != nil {
		return err
	}
	artifact, err := s.validateTx(tx, generatedruntimestate.ComponentPending, s.selected.ID, s.selected.AttemptNumber)
	if err != nil {
		return err
	}
	if artifact.State != generatedimagestate.ArtifactBuilding {
		return ErrInvalidDeployment
	}
	if err := contextError(s.ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	return nil
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuilderNetworkCreate() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuilderNetworkCreateTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuildkitRun() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuildkitRunTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuildkitStart() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuildkitStartTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuildkitRestart() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuildkitRestartTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuildkitUnpause() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuildkitUnpauseTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuildxCreate() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuildxCreateTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuilderBootstrap() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuilderBootstrapTx)
}
func (e *GeneratedImageCommandExecution) CheckBeforeBuildLoad() error {
	return e.check((*deploymentexecution.Pinned).CheckBeforeBuildLoadTx)
}
func (e *GeneratedImageCommandExecution) CheckObservation() error {
	return e.check((*deploymentexecution.Pinned).CheckGeneratedImageObservationTx)
}

func (r *Repository) FinishGeneratedImagePreparation(ctx context.Context, request GeneratedNetworkRequest) (generatedruntimestate.Phase, error) {
	// This writer uses the same immutable deployment request but validates all
	// component/artifact rows directly; component build metadata is not caller input.
	if !r.ExecutionConfigured() || ctx == nil || uuid.Validate(request.DeploymentID) != nil || request.Claim.JobID() == "" {
		return "", deploymentexecution.ErrInvalidConfiguration
	}
	pinned, err := r.executionGuard.PinScope(ctx)
	if err != nil {
		return "", err
	}
	defer pinned.Leave()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET id=id WHERE 0`); err != nil {
		return "", gateDatabaseError(ctx, err)
	}
	if err = pinned.CheckGeneratedImageResultTx(tx, request.Claim, request.DeploymentID); err != nil {
		return "", err
	}
	n := generatedNetworkExecutionState{repo: r, ctx: ctx, pinned: pinned, request: request}
	if err = n.validateTx(tx); err != nil {
		return "", err
	}
	var phase generatedruntimestate.Phase
	if err = tx.QueryRowContext(ctx, `SELECT phase FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?`, request.DeploymentID, request.Claim.ApplicationID()).Scan(&phase); err != nil || phase != generatedruntimestate.PhaseBuilding {
		if err != nil {
			return "", gateDatabaseError(ctx, err)
		}
		return "", imageSemanticError(ctx, deploymentexecution.ErrStageInvalid)
	}
	next, err := generatedruntimestate.New(r.db).FinishBuildingTx(ctx, tx, request.Claim.ApplicationID(), request.DeploymentID)
	if err != nil {
		return "", errors.Join(err, contextError(ctx))
	}
	if err = contextError(ctx); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", gateDatabaseError(ctx, err)
	}
	return next, nil
}
