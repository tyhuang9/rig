package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// candidateValidationPins is the caller identity accepted at the public
// boundary. Every command-bearing fact remains owner-derived.
type candidateValidationPins struct {
	claim deploymentexecution.Claim

	deploymentID                 string
	releaseID                    string
	configurationRevisionID      string
	configurationRevisionNumber  int64
	deploymentPlanRevisionID     string
	deploymentPlanRevisionNumber int64
	candidateSlot                string
	findings                     []Finding
}

type candidateProtectedSnapshot struct {
	revision   deploymentplans.DeploymentPlanRevision
	components []deploymentplans.Component
}

type candidateMainSnapshot struct {
	appID                       string
	jobID                       string
	releaseID                   string
	configurationRevisionID     string
	configurationRevisionNumber int64
	planRevisionID              string
	planRevisionNumber          int64
	status                      Status
	strategy                    RuntimeStrategy
	initialized                 bool
}

type candidateRuntimeSnapshot struct {
	appID                string
	releaseID            string
	planRevisionID       string
	planRevisionNumber   int64
	candidateSlot        string
	previousDeploymentID string
	previousReleaseID    string
	previousSlot         string
	phase                generatedruntimestate.Phase
	migrationState       generatedruntimestate.MigrationState
}

type candidateReleaseProvenance struct {
	sourceProvider               string
	repositoryID                 int64
	resolvedSHA                  string
	archiveSHA256                string
	workspaceTreeSHA256          string
	workspaceState               string
	configurationRevisionID      string
	configurationRevisionNumber  int64
	configurationValid           bool
	deploymentPlanRevisionID     string
	deploymentPlanRevisionNumber int64
}

type candidatePlanProvenance struct {
	canonicalDigest      string
	sourceProvider       string
	sourceRepositoryID   int64
	sourceResolvedDigest string
	acceptanceStatus     string
	componentCount       int64
}

type candidateConfigurationProvenance struct {
	formatVersion                int
	deploymentPlanRevisionID     string
	deploymentPlanRevisionNumber int64
}

type candidateArtifactSnapshot struct {
	id                           string
	releaseID                    string
	deploymentPlanRevisionID     string
	deploymentPlanRevisionNumber int64
	componentName                string
	compilerVersion              string
	buildDefinitionDigest        string
	attemptNumber                int64
	imageContentID               string
	state                        generatedimagestate.ArtifactState
}

type candidateIntentSnapshot struct {
	id                          string
	creatorJobID                string
	creatorWorkerAttempt        int
	configurationRevisionID     string
	configurationRevisionNumber int64
	imageArtifactID             string
	imageArtifactAttempt        int64
	environmentOperationID      string
	environmentStagingAttempt   int
	policyVersion               string
	policyDigest                string
}

type candidateAdmissionComponent struct {
	plan               deploymentplans.Component
	slot               string
	state              generatedruntimestate.ComponentState
	selectedArtifactID string
	containerName      string
	containerID        string
	artifact           *candidateArtifactSnapshot
	receipt            *candidateIntentSnapshot
	risky              bool
}

// candidateAdmissionSnapshot contains immutable database provenance and raw
// durable risk. It is descriptive only and grants no command authority.
type candidateAdmissionSnapshot struct {
	protected         candidateProtectedSnapshot
	pins              candidateImmutablePins
	headPins          candidateHeadPins
	observedHead      candidateHeadSnapshot
	main              candidateMainSnapshot
	runtime           candidateRuntimeSnapshot
	release           candidateReleaseProvenance
	plan              candidatePlanProvenance
	configuration     candidateConfigurationProvenance
	components        []candidateAdmissionComponent
	persistedFindings []Finding
	lifecycleRetained bool
	executionPinned   bool
}

func candidatePinsFromRequest(request GeneratedCandidateRequest) (candidateValidationPins, error) {
	pins := candidateValidationPins{
		claim:                        request.Claim,
		deploymentID:                 request.DeploymentID,
		releaseID:                    request.ReleaseID,
		configurationRevisionID:      request.ConfigurationRevisionID,
		configurationRevisionNumber:  request.ConfigurationRevisionNumber,
		deploymentPlanRevisionID:     request.DeploymentPlanRevisionID,
		deploymentPlanRevisionNumber: request.DeploymentPlanRevisionNumber,
		candidateSlot:                request.CandidateSlot,
		findings:                     append([]Finding(nil), request.Findings...),
	}
	if err := validateCandidateRequest(pins); err != nil {
		return candidateValidationPins{}, err
	}
	return pins, nil
}

func validateCandidateRequest(pins candidateValidationPins) error {
	if uuid.Validate(pins.deploymentID) != nil || pins.claim.JobID() == "" ||
		uuid.Validate(pins.claim.ApplicationID()) != nil || pins.claim.Attempt() < 1 ||
		!validReleaseID(pins.releaseID) || uuid.Validate(pins.configurationRevisionID) != nil ||
		pins.configurationRevisionNumber < 1 || uuid.Validate(pins.deploymentPlanRevisionID) != nil ||
		pins.deploymentPlanRevisionNumber < 1 || (pins.candidateSlot != "blue" && pins.candidateSlot != "green") {
		return ErrInvalidDeployment
	}
	return deployments.ValidateGeneratedOwnerFindings(pins.findings)
}

// loadCandidateProtectedSnapshot reads the command-bearing accepted plan
// outside SQL. Configuration export remains a separate owner operation.
func (a *GeneratedCandidateAuthority) loadCandidateProtectedSnapshot(ctx context.Context, pins candidateValidationPins) (candidateProtectedSnapshot, error) {
	if a == nil || a.stores == nil || a.stores.Plans() == nil ||
		!a.stores.Plans().ConfiguredFor(a.stores.Database()) {
		return candidateProtectedSnapshot{}, deploymentexecution.ErrInvalidConfiguration
	}
	if err := validateCandidateRequest(pins); err != nil {
		return candidateProtectedSnapshot{}, err
	}
	if err := contextError(ctx); err != nil {
		return candidateProtectedSnapshot{}, err
	}
	revision, err := a.stores.Plans().GetRevision(ctx, pins.claim.ApplicationID(), pins.deploymentPlanRevisionID, pins.deploymentPlanRevisionNumber)
	if err != nil {
		return candidateProtectedSnapshot{}, err
	}
	if (revision.State != deploymentplans.RevisionAccepted && revision.State != deploymentplans.RevisionSuperseded) ||
		revision.ID != pins.deploymentPlanRevisionID || revision.AppID != pins.claim.ApplicationID() ||
		revision.RevisionNumber != pins.deploymentPlanRevisionNumber ||
		revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode {
		return candidateProtectedSnapshot{}, ErrInvalidDeployment
	}
	components := append([]deploymentplans.Component(nil), revision.Plan.Components...)
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	if len(components) < 1 || len(components) > 2 {
		return candidateProtectedSnapshot{}, ErrInvalidDeployment
	}
	for index := range components {
		if !validGeneratedImageText(components[index].Name, 256) ||
			(index > 0 && components[index-1].Name == components[index].Name) {
			return candidateProtectedSnapshot{}, ErrInvalidDeployment
		}
	}
	revision.Plan.Components = append([]deploymentplans.Component(nil), components...)
	revision.Plan.FieldProvenance = append([]deploymentplans.FieldProvenance(nil), revision.Plan.FieldProvenance...)
	for index := range revision.Plan.FieldProvenance {
		revision.Plan.FieldProvenance[index].Evidence = append([]string(nil), revision.Plan.FieldProvenance[index].Evidence...)
	}
	if revision.Plan.Migration != nil {
		migration := *revision.Plan.Migration
		migration.EnvironmentKeys = append([]string(nil), migration.EnvironmentKeys...)
		revision.Plan.Migration = &migration
	}
	return candidateProtectedSnapshot{revision: revision, components: components}, nil
}

// readCandidateAdmissionSnapshotTx establishes only the exact protected
// deployment roster, then surfaces every durable receipt/name/ID/state before
// later policy, head, lifecycle, artifact, or receipt refusal.
func readCandidateAdmissionSnapshotTx(ctx context.Context, tx *sql.Tx, pins candidateValidationPins, protected candidateProtectedSnapshot) (candidateAdmissionSnapshot, error) {
	var snapshot candidateAdmissionSnapshot
	if tx == nil || ctx == nil {
		return snapshot, deploymentexecution.ErrInvalidConfiguration
	}
	if err := validateCandidateRequest(pins); err != nil {
		return snapshot, err
	}
	if err := validateProtectedCandidateSnapshot(pins, protected); err != nil {
		return snapshot, err
	}
	snapshot.protected = copyCandidateProtectedSnapshot(protected)

	err := tx.QueryRowContext(ctx, `SELECT app_id,COALESCE(job_id,''),COALESCE(release_id,''),
		COALESCE(actual_configuration_revision_id,''),actual_configuration_revision_number,
		COALESCE(deployment_plan_revision_id,''),COALESCE(deployment_plan_revision_number,0),
		status,runtime_strategy,provenance_initialized
		FROM deployments WHERE id=?`, pins.deploymentID).Scan(
		&snapshot.main.appID, &snapshot.main.jobID, &snapshot.main.releaseID,
		&snapshot.main.configurationRevisionID, &snapshot.main.configurationRevisionNumber,
		&snapshot.main.planRevisionID, &snapshot.main.planRevisionNumber,
		&snapshot.main.status, &snapshot.main.strategy, &snapshot.main.initialized,
	)
	if err != nil {
		return snapshot, candidateIdentityReadError(ctx, err)
	}
	if snapshot.main.appID != pins.claim.ApplicationID() || snapshot.main.jobID != pins.claim.JobID() {
		return candidateAdmissionSnapshot{}, deploymentexecution.ErrIdentityLost
	}

	err = tx.QueryRowContext(ctx, `SELECT app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,COALESCE(previous_active_deployment_id,''),COALESCE(previous_active_slot,''),phase,migration_state
		FROM generated_runtime_deployments WHERE deployment_id=?`, pins.deploymentID).Scan(
		&snapshot.runtime.appID, &snapshot.runtime.releaseID, &snapshot.runtime.planRevisionID,
		&snapshot.runtime.planRevisionNumber, &snapshot.runtime.candidateSlot,
		&snapshot.runtime.previousDeploymentID, &snapshot.runtime.previousSlot,
		&snapshot.runtime.phase, &snapshot.runtime.migrationState,
	)
	if err != nil {
		return snapshot, candidateIdentityReadError(ctx, err)
	}
	if snapshot.runtime.appID != pins.claim.ApplicationID() {
		return candidateAdmissionSnapshot{}, deploymentexecution.ErrIdentityLost
	}

	rows, err := tx.QueryContext(ctx, `SELECT c.component_name,c.slot,c.state,COALESCE(c.image_artifact_id,''),
		COALESCE(c.container_name,''),COALESCE(c.container_id,''),
		COALESCE(a.id,''),COALESCE(a.release_id,''),COALESCE(a.deployment_plan_revision_id,''),
		COALESCE(a.deployment_plan_revision_number,0),COALESCE(a.component_id,''),COALESCE(a.compiler_version,''),
		COALESCE(a.build_definition_digest,''),COALESCE(a.attempt_number,0),COALESCE(a.image_content_id,''),COALESCE(a.state,''),
		COALESCE(i.id,''),COALESCE(i.creator_job_id,''),COALESCE(i.creator_worker_attempt,0),
		COALESCE(i.configuration_revision_id,''),COALESCE(i.configuration_revision_number,0),
		COALESCE(i.image_artifact_id,''),COALESCE(i.image_artifact_attempt,0),
		COALESCE(i.environment_operation_id,''),COALESCE(i.environment_staging_attempt,0),
		COALESCE(i.candidate_policy_version,''),COALESCE(i.candidate_policy_digest,'')
		FROM generated_runtime_components c
		LEFT JOIN generated_image_artifacts a ON a.id=c.image_artifact_id
		LEFT JOIN generated_candidate_intents i ON i.deployment_id=c.deployment_id AND i.component_name=c.component_name
		WHERE c.deployment_id=? ORDER BY c.component_name`, pins.deploymentID)
	if err != nil {
		return snapshot, gateDatabaseError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var component candidateAdmissionComponent
		var artifact candidateArtifactSnapshot
		var receipt candidateIntentSnapshot
		var artifactState string
		if err := rows.Scan(
			&component.plan.Name, &component.slot, &component.state, &component.selectedArtifactID,
			&component.containerName, &component.containerID,
			&artifact.id, &artifact.releaseID, &artifact.deploymentPlanRevisionID,
			&artifact.deploymentPlanRevisionNumber, &artifact.componentName, &artifact.compilerVersion,
			&artifact.buildDefinitionDigest, &artifact.attemptNumber, &artifact.imageContentID, &artifactState,
			&receipt.id, &receipt.creatorJobID, &receipt.creatorWorkerAttempt,
			&receipt.configurationRevisionID, &receipt.configurationRevisionNumber,
			&receipt.imageArtifactID, &receipt.imageArtifactAttempt,
			&receipt.environmentOperationID, &receipt.environmentStagingAttempt,
			&receipt.policyVersion, &receipt.policyDigest,
		); err != nil {
			return snapshot, gateDatabaseError(ctx, err)
		}
		index := len(snapshot.components)
		if index >= len(protected.components) || component.plan.Name != protected.components[index].Name {
			return candidateAdmissionSnapshot{}, ErrInvalidDeployment
		}
		component.plan = protected.components[index]
		if artifact.id != "" {
			artifact.state = generatedimagestate.ArtifactState(artifactState)
			component.artifact = &artifact
		}
		if receipt.id != "" {
			component.receipt = &receipt
		}
		component.risky = component.receipt != nil || component.containerName != "" || component.containerID != "" || candidateRiskyState(component.state)
		snapshot.components = append(snapshot.components, component)
	}
	if err := rows.Err(); err != nil {
		return snapshot, gateDatabaseError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return snapshot, gateDatabaseError(ctx, err)
	}
	if len(snapshot.components) != len(protected.components) {
		return candidateAdmissionSnapshot{}, ErrInvalidDeployment
	}

	// Everything below occurs only after the complete protected roster and its
	// durable risk have been captured. Missing provenance remains zero-valued
	// for strict post-admission refusal instead of hiding discovered risk.
	if snapshot.runtime.previousDeploymentID != "" {
		err = tx.QueryRowContext(ctx, `SELECT release_id FROM generated_runtime_deployments
			WHERE deployment_id=? AND app_id=?`, snapshot.runtime.previousDeploymentID, pins.claim.ApplicationID()).
			Scan(&snapshot.runtime.previousReleaseID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return snapshot, gateDatabaseError(ctx, err)
		}
	}
	snapshot.pins = candidateImmutablePins{
		jobID: pins.claim.JobID(), releaseID: pins.releaseID,
		configurationRevisionID:      pins.configurationRevisionID,
		configurationRevisionNumber:  pins.configurationRevisionNumber,
		deploymentPlanRevisionID:     pins.deploymentPlanRevisionID,
		deploymentPlanRevisionNumber: pins.deploymentPlanRevisionNumber,
		candidateSlot:                pins.candidateSlot,
		previousDeploymentID:         snapshot.runtime.previousDeploymentID,
		previousSlot:                 snapshot.runtime.previousSlot,
	}
	snapshot.headPins = candidateHeadPins{
		appID: pins.claim.ApplicationID(), deploymentID: pins.deploymentID,
		releaseID: pins.releaseID, slot: pins.candidateSlot,
		previousDeploymentID: snapshot.runtime.previousDeploymentID,
		previousReleaseID:    snapshot.runtime.previousReleaseID,
		previousSlot:         snapshot.runtime.previousSlot,
	}

	err = tx.QueryRowContext(ctx, `SELECT r.source_provider,r.repository_id,r.resolved_sha,COALESCE(r.archive_sha256,''),
		COALESCE(r.workspace_tree_sha256,''),r.workspace_state,COALESCE(r.configuration_revision_id,''),r.configuration_revision_number,
		CASE WHEN (r.configuration_revision_number=0 AND r.configuration_revision_id IS NULL)
			OR (r.configuration_revision_number>0 AND EXISTS (
				SELECT 1 FROM application_configuration_revisions original
				WHERE original.id=r.configuration_revision_id AND original.app_id=r.app_id
					AND original.revision_number=r.configuration_revision_number
			)) THEN 1 ELSE 0 END,
		COALESCE(r.deployment_plan_revision_id,''),COALESCE(r.deployment_plan_revision_number,0)
		FROM releases r WHERE r.id=? AND r.app_id=?`, pins.releaseID, pins.claim.ApplicationID()).Scan(
		&snapshot.release.sourceProvider, &snapshot.release.repositoryID, &snapshot.release.resolvedSHA,
		&snapshot.release.archiveSHA256, &snapshot.release.workspaceTreeSHA256, &snapshot.release.workspaceState,
		&snapshot.release.configurationRevisionID, &snapshot.release.configurationRevisionNumber,
		&snapshot.release.configurationValid,
		&snapshot.release.deploymentPlanRevisionID, &snapshot.release.deploymentPlanRevisionNumber,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, gateDatabaseError(ctx, err)
	}
	err = tx.QueryRowContext(ctx, `SELECT canonical_digest,analyzed_source_provider,analyzed_repository_id,
		analyzed_resolved_digest,acceptance_status,component_count
		FROM deployment_plan_revisions WHERE id=? AND app_id=? AND revision_number=?`,
		pins.deploymentPlanRevisionID, pins.claim.ApplicationID(), pins.deploymentPlanRevisionNumber).Scan(
		&snapshot.plan.canonicalDigest, &snapshot.plan.sourceProvider, &snapshot.plan.sourceRepositoryID,
		&snapshot.plan.sourceResolvedDigest, &snapshot.plan.acceptanceStatus, &snapshot.plan.componentCount,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, gateDatabaseError(ctx, err)
	}
	err = tx.QueryRowContext(ctx, `SELECT bundle_version,COALESCE(deployment_plan_revision_id,''),
		COALESCE(deployment_plan_revision_number,0) FROM application_configuration_revisions
		WHERE id=? AND app_id=? AND revision_number=?`, pins.configurationRevisionID,
		pins.claim.ApplicationID(), pins.configurationRevisionNumber).Scan(
		&snapshot.configuration.formatVersion, &snapshot.configuration.deploymentPlanRevisionID,
		&snapshot.configuration.deploymentPlanRevisionNumber,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, gateDatabaseError(ctx, err)
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(deployment_id,''),COALESCE(release_id,''),
		COALESCE(slot,''),generation FROM generated_runtime_active_heads WHERE app_id=?`, pins.claim.ApplicationID()).Scan(
		&snapshot.observedHead.deploymentID, &snapshot.observedHead.releaseID,
		&snapshot.observedHead.slot, &snapshot.observedHead.generation,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, gateDatabaseError(ctx, err)
	}
	snapshot.observedHead.appID = pins.claim.ApplicationID()

	snapshot.persistedFindings, err = candidateFindingsRawTx(ctx, tx, pins.claim.ApplicationID(), pins.deploymentID)
	if err != nil {
		return snapshot, gateDatabaseError(ctx, err)
	}
	var retained int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_lifecycle_operations
		WHERE app_id=? AND deployment_id=?)`, pins.claim.ApplicationID(), pins.deploymentID).Scan(&retained); err != nil {
		return snapshot, gateDatabaseError(ctx, err)
	}
	snapshot.lifecycleRetained = retained != 0
	return snapshot, contextError(ctx)
}

func validateCandidateAdmissionSnapshotTx(ctx context.Context, stores *deployments.GeneratedOwnerStores, tx *sql.Tx, pins candidateValidationPins, protected candidateProtectedSnapshot, baseline candidateAdmissionSnapshot) (candidateAdmissionSnapshot, error) {
	current, err := readCandidateAdmissionSnapshotTx(ctx, tx, pins, protected)
	if err != nil {
		return current, err
	}
	if err := validateCandidateSnapshotCommon(pins, protected, baseline, current); err != nil {
		return current, err
	}
	if err := validateCandidatePolicyTx(ctx, stores, tx, pins, current, false); err != nil {
		return current, err
	}
	current.executionPinned = baseline.executionPinned
	return current, contextError(ctx)
}

func validateCandidateExecutionSnapshotTx(ctx context.Context, stores *deployments.GeneratedOwnerStores, tx *sql.Tx, pins candidateValidationPins, protected candidateProtectedSnapshot, baseline candidateAdmissionSnapshot) (candidateAdmissionSnapshot, error) {
	current, err := readCandidateAdmissionSnapshotTx(ctx, tx, pins, protected)
	if err != nil {
		return current, err
	}
	if err := validateCandidateSnapshotCommon(pins, protected, baseline, current); err != nil {
		return current, err
	}
	if err := validateCandidatePolicyTx(ctx, stores, tx, pins, current, true); err != nil {
		return current, err
	}
	if err := validateCandidateExecutionState(pins, current); err != nil {
		return current, err
	}
	current.executionPinned = true
	return current, contextError(ctx)
}

func validateCandidateSnapshotCommon(pins candidateValidationPins, protected candidateProtectedSnapshot, baseline, current candidateAdmissionSnapshot) error {
	if err := validateProtectedCandidateSnapshot(pins, protected); err != nil {
		return err
	}
	if baseline.pins != current.pins || baseline.pins.jobID != pins.claim.JobID() ||
		baseline.pins.releaseID != pins.releaseID || baseline.pins.configurationRevisionID != pins.configurationRevisionID ||
		baseline.pins.configurationRevisionNumber != pins.configurationRevisionNumber ||
		baseline.pins.deploymentPlanRevisionID != pins.deploymentPlanRevisionID ||
		baseline.pins.deploymentPlanRevisionNumber != pins.deploymentPlanRevisionNumber ||
		baseline.pins.candidateSlot != pins.candidateSlot || baseline.headPins != current.headPins ||
		!sameProtectedCandidateSnapshot(baseline.protected, protected) ||
		!sameProtectedCandidateSnapshot(current.protected, protected) {
		return deploymentexecution.ErrAuthorityStale
	}
	if current.main.appID != pins.claim.ApplicationID() || current.main.jobID != pins.claim.JobID() ||
		current.main.releaseID != pins.releaseID || current.main.configurationRevisionID != pins.configurationRevisionID ||
		current.main.configurationRevisionNumber != pins.configurationRevisionNumber ||
		current.main.planRevisionID != pins.deploymentPlanRevisionID ||
		current.main.planRevisionNumber != pins.deploymentPlanRevisionNumber ||
		current.main.strategy != RuntimeGeneratedNode || !current.main.initialized {
		return ErrInvalidDeployment
	}
	if !candidateMainProgression(baseline.main.status, current.main.status) ||
		!candidateAdmissionStatus(current.main.status) {
		return deploymentexecution.ErrStageInvalid
	}
	if current.runtime.appID != pins.claim.ApplicationID() || current.runtime.releaseID != pins.releaseID ||
		current.runtime.planRevisionID != pins.deploymentPlanRevisionID ||
		current.runtime.planRevisionNumber != pins.deploymentPlanRevisionNumber ||
		current.runtime.candidateSlot != pins.candidateSlot ||
		!validGeneratedSlotRelationship(current.runtime.candidateSlot, current.runtime.previousDeploymentID, current.runtime.previousSlot) ||
		(current.runtime.previousDeploymentID != "" && current.runtime.previousReleaseID == "") ||
		current.runtime.previousDeploymentID != baseline.runtime.previousDeploymentID ||
		current.runtime.previousReleaseID != baseline.runtime.previousReleaseID ||
		current.runtime.previousSlot != baseline.runtime.previousSlot {
		return ErrInvalidDeployment
	}
	if !candidateRuntimeProgression(baseline.runtime.phase, current.runtime.phase) ||
		!candidateMigrationProgression(baseline.runtime.migrationState, current.runtime.migrationState) ||
		!candidateAdmissionPhase(current.runtime.phase) {
		return deploymentexecution.ErrStageInvalid
	}
	if !candidateStagePair(current.main.status, current.runtime.phase, current.runtime.migrationState, protected.revision.Plan.Migration != nil) {
		return deploymentexecution.ErrStageInvalid
	}
	if !current.release.configurationValid ||
		current.release.deploymentPlanRevisionID != pins.deploymentPlanRevisionID ||
		current.release.deploymentPlanRevisionNumber != pins.deploymentPlanRevisionNumber ||
		current.release.workspaceState != "ready" ||
		!validCandidateSource(current.release.sourceProvider, current.release.repositoryID, current.release.resolvedSHA) ||
		(current.release.archiveSHA256 != "" && !lowerHex(current.release.archiveSHA256, 64)) ||
		!lowerHex(current.release.workspaceTreeSHA256, 64) || current.release != baseline.release {
		return ErrInvalidDeployment
	}
	revision := protected.revision
	if current.plan.canonicalDigest != revision.CanonicalDigest ||
		current.plan.sourceProvider != revision.Plan.Source.Provider ||
		current.plan.sourceRepositoryID != revision.Plan.Source.RepositoryID ||
		current.plan.sourceResolvedDigest != revision.Plan.Source.ResolvedDigest ||
		current.plan.acceptanceStatus != string(deploymentplans.RevisionAccepted) ||
		current.plan.componentCount != int64(len(protected.components)) || current.plan != baseline.plan {
		return ErrInvalidDeployment
	}
	if current.configuration.formatVersion != 2 ||
		current.configuration.deploymentPlanRevisionID != pins.deploymentPlanRevisionID ||
		current.configuration.deploymentPlanRevisionNumber != pins.deploymentPlanRevisionNumber ||
		current.configuration != baseline.configuration || current.lifecycleRetained {
		return ErrInvalidDeployment
	}
	if len(current.components) != len(protected.components) || len(baseline.components) != len(protected.components) {
		return ErrInvalidDeployment
	}
	for index := range current.components {
		before, component := baseline.components[index], current.components[index]
		if before.plan != protected.components[index] || component.plan != protected.components[index] ||
			component.slot != pins.candidateSlot || before.slot != component.slot ||
			!candidateComponentProgression(before.state, component.state) ||
			(before.selectedArtifactID != "" && before.selectedArtifactID != component.selectedArtifactID) ||
			(before.containerName != "" && before.containerName != component.containerName) ||
			(before.containerID != "" && before.containerID != component.containerID) ||
			(before.receipt != nil && (component.receipt == nil || *before.receipt != *component.receipt)) {
			return ErrInvalidDeployment
		}
		if baseline.executionPinned && (before.artifact == nil || component.artifact == nil || *before.artifact != *component.artifact) {
			return deploymentexecution.ErrAuthorityStale
		}
		if err := validateCandidateComponentProvenance(pins, component, false); err != nil {
			return err
		}
	}
	return nil
}

func validateCandidateExecutionState(pins candidateValidationPins, snapshot candidateAdmissionSnapshot) error {
	if snapshot.main.status != Applying && snapshot.main.status != WaitingHealth {
		return deploymentexecution.ErrStageInvalid
	}
	switch snapshot.runtime.phase {
	case generatedruntimestate.PhaseStartingCandidate:
		if snapshot.main.status != Applying {
			return deploymentexecution.ErrStageInvalid
		}
		for _, component := range snapshot.components {
			if component.state != generatedruntimestate.ComponentImageReady &&
				component.state != generatedruntimestate.ComponentStarting &&
				component.state != generatedruntimestate.ComponentRunning {
				return deploymentexecution.ErrStageInvalid
			}
		}
	case generatedruntimestate.PhaseWaitingHealth:
		if snapshot.main.status != WaitingHealth {
			return deploymentexecution.ErrStageInvalid
		}
		for _, component := range snapshot.components {
			if component.state != generatedruntimestate.ComponentRunning && component.state != generatedruntimestate.ComponentHealthy {
				return deploymentexecution.ErrStageInvalid
			}
		}
	case generatedruntimestate.PhaseSwitchingRoute:
		if snapshot.main.status != WaitingHealth {
			return deploymentexecution.ErrStageInvalid
		}
		allHealthy, allActive := true, true
		for _, component := range snapshot.components {
			allHealthy = allHealthy && component.state == generatedruntimestate.ComponentHealthy
			allActive = allActive && component.state == generatedruntimestate.ComponentActive
		}
		if !allHealthy && !allActive {
			return deploymentexecution.ErrStageInvalid
		}
	default:
		return deploymentexecution.ErrStageInvalid
	}
	for _, component := range snapshot.components {
		if err := validateCandidateComponentProvenance(pins, component, true); err != nil {
			return err
		}
	}
	return nil
}

func validateCandidateComponentProvenance(pins candidateValidationPins, component candidateAdmissionComponent, requireReady bool) error {
	switch component.state {
	case generatedruntimestate.ComponentPending:
		if component.selectedArtifactID != "" || component.containerName != "" || component.containerID != "" || component.receipt != nil {
			return ErrInvalidDeployment
		}
	case generatedruntimestate.ComponentImageReady:
		if component.selectedArtifactID == "" || component.containerName != "" || component.containerID != "" || component.receipt != nil {
			return ErrInvalidDeployment
		}
	case generatedruntimestate.ComponentStarting:
		if component.selectedArtifactID == "" || component.containerName == "" || component.containerID != "" || component.receipt == nil {
			return ErrInvalidDeployment
		}
	case generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy, generatedruntimestate.ComponentActive:
		if component.selectedArtifactID == "" || component.containerName == "" || component.containerID == "" || component.receipt == nil {
			return ErrInvalidDeployment
		}
	default:
		return deploymentexecution.ErrStageInvalid
	}
	if component.selectedArtifactID == "" {
		return nil
	}
	if component.artifact == nil {
		return ErrInvalidDeployment
	}
	artifact := *component.artifact
	if artifact.id != component.selectedArtifactID || artifact.releaseID != pins.releaseID ||
		artifact.deploymentPlanRevisionID != pins.deploymentPlanRevisionID ||
		artifact.deploymentPlanRevisionNumber != pins.deploymentPlanRevisionNumber ||
		artifact.componentName != component.plan.Name || artifact.attemptNumber < 1 ||
		!validGeneratedImageText(artifact.compilerVersion, 128) || !lowerHex(artifact.buildDefinitionDigest, 64) ||
		(artifact.imageContentID != "" && !validGeneratedMigrationImageID(artifact.imageContentID)) ||
		(requireReady && (artifact.state != generatedimagestate.ArtifactReady || !validGeneratedMigrationImageID(artifact.imageContentID))) {
		return ErrInvalidDeployment
	}
	if component.receipt == nil {
		return nil
	}
	receipt := *component.receipt
	if uuid.Validate(receipt.id) != nil || receipt.creatorJobID != pins.claim.JobID() ||
		receipt.creatorWorkerAttempt < 1 || (pins.claim.Attempt() > 0 && receipt.creatorWorkerAttempt > pins.claim.Attempt()) ||
		receipt.configurationRevisionID != pins.configurationRevisionID ||
		receipt.configurationRevisionNumber != pins.configurationRevisionNumber ||
		receipt.imageArtifactID != artifact.id || receipt.imageArtifactAttempt != artifact.attemptNumber ||
		receipt.environmentOperationID != artifact.id || receipt.environmentStagingAttempt != receipt.creatorWorkerAttempt ||
		!validGeneratedImageText(receipt.policyVersion, 128) || !lowerHex(receipt.policyDigest, 64) {
		return ErrInvalidDeployment
	}
	return nil
}

func validateCandidatePolicyTx(ctx context.Context, stores *deployments.GeneratedOwnerStores, tx *sql.Tx, pins candidateValidationPins, snapshot candidateAdmissionSnapshot, requirePersisted bool) error {
	if err := deployments.ValidateGeneratedOwnerFindings(snapshot.persistedFindings); err != nil {
		return err
	}
	if len(snapshot.persistedFindings) != 0 && !deployments.SameGeneratedOwnerFindingSet(snapshot.persistedFindings, pins.findings) {
		return ErrInvalidDeployment
	}
	if (requirePersisted || snapshot.main.status == Applying || snapshot.main.status == WaitingHealth) &&
		!deployments.SameGeneratedOwnerFindingSet(snapshot.persistedFindings, pins.findings) {
		return ErrInvalidDeployment
	}
	findings := snapshot.persistedFindings
	if len(findings) == 0 {
		findings = pins.findings
	}
	rejected, missingApproval, err := stores.EvaluateGeneratedPolicyTx(ctx, tx, pins.claim.ApplicationID(), findings)
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	if rejected {
		return ErrRejectedCapability
	}
	if missingApproval {
		return ErrApprovalRequired
	}
	return nil
}

func candidateFindingsRawTx(ctx context.Context, tx *sql.Tx, appID, deploymentID string) ([]Finding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT f.policy_version,f.capability,f.scope,f.fingerprint,f.disposition
		FROM deployment_policy_findings f JOIN deployments d ON d.id=f.deployment_id
		WHERE f.deployment_id=? AND d.app_id=? ORDER BY f.fingerprint`, deploymentID, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var findings []Finding
	for rows.Next() {
		var finding Finding
		if err := rows.Scan(&finding.PolicyVersion, &finding.Capability, &finding.Scope, &finding.Fingerprint, &finding.Disposition); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return findings, nil
}

func validateProtectedCandidateSnapshot(pins candidateValidationPins, protected candidateProtectedSnapshot) error {
	revision := protected.revision
	if (revision.State != deploymentplans.RevisionAccepted && revision.State != deploymentplans.RevisionSuperseded) ||
		revision.ID != pins.deploymentPlanRevisionID || revision.AppID != pins.claim.ApplicationID() ||
		revision.RevisionNumber != pins.deploymentPlanRevisionNumber || revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode ||
		len(protected.components) < 1 || len(protected.components) > 2 || len(revision.Plan.Components) != len(protected.components) {
		return ErrInvalidDeployment
	}
	for index := range protected.components {
		if revision.Plan.Components[index] != protected.components[index] || !validGeneratedImageText(protected.components[index].Name, 256) ||
			(index > 0 && protected.components[index-1].Name >= protected.components[index].Name) {
			return ErrInvalidDeployment
		}
	}
	return nil
}

func copyCandidateProtectedSnapshot(value candidateProtectedSnapshot) candidateProtectedSnapshot {
	result := value
	result.components = append([]deploymentplans.Component(nil), value.components...)
	result.revision.Plan.Components = append([]deploymentplans.Component(nil), value.revision.Plan.Components...)
	result.revision.Plan.FieldProvenance = append([]deploymentplans.FieldProvenance(nil), value.revision.Plan.FieldProvenance...)
	for index := range result.revision.Plan.FieldProvenance {
		result.revision.Plan.FieldProvenance[index].Evidence = append([]string(nil), value.revision.Plan.FieldProvenance[index].Evidence...)
	}
	if value.revision.Plan.Migration != nil {
		migration := *value.revision.Plan.Migration
		migration.EnvironmentKeys = append([]string(nil), value.revision.Plan.Migration.EnvironmentKeys...)
		result.revision.Plan.Migration = &migration
	}
	return result
}

func sameProtectedCandidateSnapshot(left, right candidateProtectedSnapshot) bool {
	if left.revision.ID != right.revision.ID || left.revision.AppID != right.revision.AppID ||
		left.revision.RevisionNumber != right.revision.RevisionNumber || left.revision.CanonicalDigest != right.revision.CanonicalDigest ||
		left.revision.State != right.revision.State || len(left.components) != len(right.components) {
		return false
	}
	for index := range left.components {
		if left.components[index] != right.components[index] {
			return false
		}
	}
	return true
}

func candidateRiskyState(state generatedruntimestate.ComponentState) bool {
	return state != generatedruntimestate.ComponentPending && state != generatedruntimestate.ComponentImageReady
}

func candidateAdmissionStatus(status Status) bool {
	return status == Preparing || status == NeedsAttention || status == Applying || status == WaitingHealth
}

func candidateAdmissionPhase(phase generatedruntimestate.Phase) bool {
	return phase == generatedruntimestate.PhaseBuilding || phase == generatedruntimestate.PhaseMigrating ||
		phase == generatedruntimestate.PhaseStartingCandidate || phase == generatedruntimestate.PhaseWaitingHealth ||
		phase == generatedruntimestate.PhaseSwitchingRoute
}

func candidateStagePair(status Status, phase generatedruntimestate.Phase, migration generatedruntimestate.MigrationState, migrationRequired bool) bool {
	expectedComplete := generatedruntimestate.MigrationNotRequired
	if migrationRequired {
		expectedComplete = generatedruntimestate.MigrationSucceeded
	}
	switch phase {
	case generatedruntimestate.PhaseBuilding:
		expected := generatedruntimestate.MigrationNotRequired
		if migrationRequired {
			expected = generatedruntimestate.MigrationPending
		}
		return (status == Preparing || status == NeedsAttention || status == Applying) && migration == expected
	case generatedruntimestate.PhaseMigrating:
		return migrationRequired && status == Applying && (migration == generatedruntimestate.MigrationPending ||
			migration == generatedruntimestate.MigrationRunning || migration == generatedruntimestate.MigrationSucceeded)
	case generatedruntimestate.PhaseStartingCandidate:
		return status == Applying && migration == expectedComplete
	case generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute:
		return status == WaitingHealth && migration == expectedComplete
	default:
		return false
	}
}

func candidateMainProgression(before, after Status) bool {
	switch before {
	case Preparing:
		return after == Preparing || after == NeedsAttention || after == Applying || after == WaitingHealth
	case NeedsAttention:
		return after == NeedsAttention || after == Applying || after == WaitingHealth
	case Applying:
		return after == Applying || after == WaitingHealth
	case WaitingHealth:
		return after == WaitingHealth
	default:
		return false
	}
}

func candidateRuntimeProgression(before, after generatedruntimestate.Phase) bool {
	if before == after {
		return candidateAdmissionPhase(after)
	}
	switch before {
	case generatedruntimestate.PhaseBuilding:
		return after == generatedruntimestate.PhaseMigrating || after == generatedruntimestate.PhaseStartingCandidate ||
			after == generatedruntimestate.PhaseWaitingHealth || after == generatedruntimestate.PhaseSwitchingRoute
	case generatedruntimestate.PhaseMigrating:
		return after == generatedruntimestate.PhaseStartingCandidate || after == generatedruntimestate.PhaseWaitingHealth ||
			after == generatedruntimestate.PhaseSwitchingRoute
	case generatedruntimestate.PhaseStartingCandidate:
		return after == generatedruntimestate.PhaseWaitingHealth || after == generatedruntimestate.PhaseSwitchingRoute
	case generatedruntimestate.PhaseWaitingHealth:
		return after == generatedruntimestate.PhaseSwitchingRoute
	default:
		return false
	}
}

func candidateMigrationProgression(before, after generatedruntimestate.MigrationState) bool {
	if before == after {
		return true
	}
	switch before {
	case generatedruntimestate.MigrationPending:
		return after == generatedruntimestate.MigrationRunning || after == generatedruntimestate.MigrationSucceeded
	case generatedruntimestate.MigrationRunning:
		return after == generatedruntimestate.MigrationSucceeded
	default:
		return false
	}
}

func candidateComponentProgression(before, after generatedruntimestate.ComponentState) bool {
	if before == after {
		return true
	}
	if after == generatedruntimestate.ComponentFailed {
		return before != generatedruntimestate.ComponentStopped && before != generatedruntimestate.ComponentFailed
	}
	switch before {
	case generatedruntimestate.ComponentPending:
		return after == generatedruntimestate.ComponentImageReady || after == generatedruntimestate.ComponentStarting ||
			after == generatedruntimestate.ComponentRunning || after == generatedruntimestate.ComponentHealthy || after == generatedruntimestate.ComponentActive
	case generatedruntimestate.ComponentImageReady:
		return after == generatedruntimestate.ComponentStarting || after == generatedruntimestate.ComponentRunning ||
			after == generatedruntimestate.ComponentHealthy || after == generatedruntimestate.ComponentActive
	case generatedruntimestate.ComponentStarting:
		return after == generatedruntimestate.ComponentRunning || after == generatedruntimestate.ComponentHealthy || after == generatedruntimestate.ComponentActive
	case generatedruntimestate.ComponentRunning:
		return after == generatedruntimestate.ComponentHealthy || after == generatedruntimestate.ComponentActive
	case generatedruntimestate.ComponentHealthy:
		return after == generatedruntimestate.ComponentActive
	case generatedruntimestate.ComponentActive:
		return after == generatedruntimestate.ComponentDraining || after == generatedruntimestate.ComponentStopped
	case generatedruntimestate.ComponentDraining:
		return after == generatedruntimestate.ComponentStopped
	default:
		return false
	}
}

func validCandidateSource(provider string, repositoryID int64, digest string) bool {
	switch provider {
	case "github":
		return repositoryID > 0 && (lowerHex(digest, 40) || lowerHex(digest, 64))
	case "local":
		return repositoryID == 0 && lowerHex(digest, 64)
	default:
		return false
	}
}

func candidateIdentityReadError(ctx context.Context, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	return gateDatabaseError(ctx, err)
}
