package deployments

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedOwnerStores is the sealed set of concrete protected stores used by
// the generated-runtime owners. Its zero value is invalid and callers cannot
// replace any dependency after the repository issues it.
//
// The accessors intentionally expose the original concrete objects rather than
// accepting validator or writer callbacks. Generated owners still perform each
// operation-specific authority check at its existing transaction boundary.
type GeneratedOwnerStores struct {
	repository    *Repository
	database      *sql.DB
	guard         *deploymentexecution.Guard
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	identity      *generatedOwnerStoresRegistry
}

// generatedOwnerStoresRegistry is allocated with the repository and shared by
// every value copy. A copy made before the first bundle request therefore
// cannot mint a second store identity.
type generatedOwnerStoresRegistry struct {
	mu     sync.Mutex
	stores *GeneratedOwnerStores
}

// GeneratedDomainStartupSnapshot is the committed, sanitized database-side
// description used by later startup classification. It grants no effect or
// readiness authority.
type GeneratedDomainStartupSnapshot struct {
	Gateway                 appaccess.HostingGatewayStartupSnapshot
	Runtime                 generatedruntimestate.GeneratedDomainStartupSnapshot
	GeneratedDeployments    []GeneratedDomainStartupDeployment
	Artifacts               []GeneratedDomainStartupArtifact
	LifecycleOperationCount int64
	LifecycleOperations     []GeneratedDomainStartupLifecycleOperation
	LifecycleBindings       []GeneratedDomainStartupLifecycleBinding
	UnresolvedLifecycleApps []string
}

type GeneratedDomainStartupDeployment struct {
	ID                                string
	AppID                             string
	JobID                             string
	ReleaseID                         string
	Status                            Status
	ProvenanceInitialized             bool
	DeploymentPlanRevisionID          string
	DeploymentPlanRevisionNumber      int64
	ActualConfigurationRevisionID     string
	ActualConfigurationRevisionNumber int64
	JobStatus                         string
	JobPhase                          string
	JobAttempt                        int
	CanonicalSuccessEventCount        int
	SuccessEventCount                 int
	CanonicalSuccessEventAt           time.Time
}

type GeneratedDomainStartupLifecycleOperation struct {
	AppID           string
	JobID           string
	DeploymentID    string
	DesiredRevision int64
	Complete        bool
}

type GeneratedDomainStartupLifecycleBinding struct {
	DeploymentJobID        string
	AppID                  string
	DesiredRevision        int64
	Mode                   deploymentbinding.Mode
	AuthorizingOperationID string
}

type GeneratedDomainStartupArtifact struct {
	ID                           string
	ReleaseID                    string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ComponentID                  string
	AttemptNumber                int64
	State                        generatedimagestate.ArtifactState
}

// ValidateGeneratedOwnerFindings is the pure finding validator shared with
// the relocated candidate owner. It performs no store access or mutation.
func ValidateGeneratedOwnerFindings(findings []Finding) error {
	return validateFindings(findings)
}

// SameGeneratedOwnerFindingSet is the canonical ordering-insensitive finding
// comparison used at generated-owner validation boundaries.
func SameGeneratedOwnerFindingSet(left, right []Finding) bool {
	return sameFindingSet(left, right)
}

// GeneratedOwnerStores returns the repository's immutable protected-store
// bundle. Repeated calls through repository value copies return the same bundle
// pointer. Reconstructed repositories have a distinct identity even when they
// address the same database; generated-domain collision checks handle that
// physical conflict before publishing ownership.
func (r *Repository) GeneratedOwnerStores() (*GeneratedOwnerStores, error) {
	if !generatedOwnerRepositoryConfigured(r) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	registry := r.generatedOwnerStoresRegistry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.stores != nil {
		if !registry.stores.matches(r) {
			return nil, deploymentexecution.ErrInvalidConfiguration
		}
		return registry.stores, nil
	}
	stores := &GeneratedOwnerStores{
		repository: r, database: r.db, guard: r.executionGuard,
		plans: r.migrationPlans, configuration: r.migrationConfiguration,
		identity: registry,
	}
	registry.stores = stores
	return stores, nil
}

func generatedOwnerRepositoryConfigured(r *Repository) bool {
	return r != nil && r.generatedOwnerStoresRegistry != nil && r.ExecutionConfigured() &&
		r.migrationPlans != nil && r.migrationPlans.ConfiguredFor(r.db) &&
		r.migrationConfiguration != nil && r.migrationConfiguration.ConfiguredFor(r.db)
}

func (s *GeneratedOwnerStores) matches(r *Repository) bool {
	return s != nil && r != nil && s.identity != nil && s.identity == r.generatedOwnerStoresRegistry &&
		s.repository != nil && s.database == r.db && s.guard == r.executionGuard &&
		s.plans == r.migrationPlans && s.configuration == r.migrationConfiguration &&
		s.Configured()
}

// Configured revalidates the concrete same-database pairing. It does not
// confer operation authority and does not replace per-effect guard checks.
func (s *GeneratedOwnerStores) Configured() bool {
	return s != nil && s.identity != nil && s.repository != nil && s.database != nil &&
		s.guard != nil && s.guard.UsesDatabase(s.database) &&
		s.plans != nil && s.plans.ConfiguredFor(s.database) &&
		s.configuration != nil && s.configuration.ConfiguredFor(s.database)
}

// ConfiguredForDomain reports whether this bundle value still names the exact
// sealed concrete dependencies and configured paths expected by a generated
// domain constructor. An unchanged value copy remains valid. The result is
// configuration provenance only: it neither proves physical path identity nor
// grants effect authority.
func (s *GeneratedOwnerStores) ConfiguredForDomain(dataRoot, workingDirectory string) bool {
	if s == nil || s.identity == nil {
		return false
	}
	registry := s.identity
	registry.mu.Lock()
	canonical := registry.stores
	matchesCanonical := canonical != nil && canonical.identity == registry &&
		s.repository == canonical.repository && s.database == canonical.database &&
		s.guard == canonical.guard && s.plans == canonical.plans &&
		s.configuration == canonical.configuration
	registry.mu.Unlock()
	return matchesCanonical && s.matches(s.repository) &&
		s.guard.ConfiguredForDomain(s.database, workingDirectory) &&
		s.plans.ConfiguredForDomain(s.database, dataRoot) &&
		s.configuration.ConfiguredForDomain(s.database, dataRoot)
}

// Repository returns the exact generic deployment repository captured by the
// bundle. Candidate-specific authority is not constructed by this repository.
func (s *GeneratedOwnerStores) Repository() *Repository {
	if !s.Configured() {
		return nil
	}
	return s.repository
}

// Database returns the exact database used by every protected store.
func (s *GeneratedOwnerStores) Database() *sql.DB {
	if !s.Configured() {
		return nil
	}
	return s.database
}

// ExecutionGuard returns the exact effects guard captured by the bundle.
func (s *GeneratedOwnerStores) ExecutionGuard() *deploymentexecution.Guard {
	if !s.Configured() {
		return nil
	}
	return s.guard
}

// Plans returns the exact protected deployment-plan store.
func (s *GeneratedOwnerStores) Plans() *deploymentplans.Store {
	if !s.Configured() {
		return nil
	}
	return s.plans
}

// Configuration returns the exact protected application-configuration store.
func (s *GeneratedOwnerStores) Configuration() *appconfig.Store {
	if !s.Configured() {
		return nil
	}
	return s.configuration
}

// EvaluateGeneratedPolicyTx evaluates the generated deployment's persisted
// approval policy through the concrete read-only transaction already owned by
// the caller. It exposes no query callback or result writer.
func (s *GeneratedOwnerStores) EvaluateGeneratedPolicyTx(ctx context.Context, tx *sql.Tx, appID string, findings []Finding) (bool, bool, error) {
	if !s.Configured() || ctx == nil || tx == nil || uuid.Validate(appID) != nil {
		return false, false, deploymentexecution.ErrInvalidConfiguration
	}
	return evaluatePolicyTx(ctx, tx, appID, findings)
}

// GeneratedDomainStartupSnapshot owns one read-only transaction on the exact
// canonical database retained by this sealed store bundle. It returns only a
// committed descriptive value and returns the zero value on every failure.
func (s *GeneratedOwnerStores) GeneratedDomainStartupSnapshot(ctx context.Context) (GeneratedDomainStartupSnapshot, error) {
	if s == nil || ctx == nil {
		return GeneratedDomainStartupSnapshot{}, deploymentexecution.ErrInvalidConfiguration
	}
	value := *s
	canonical, err := generatedDomainStartupCanonicalBundle(value, nil)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	tx, err := canonical.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := canonical.generatedDomainStartupSnapshotTx(ctx, tx)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	if _, err := generatedDomainStartupCanonicalBundle(value, canonical); err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	if _, err := generatedDomainStartupCanonicalBundle(value, canonical); err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	return snapshot, nil
}

func generatedDomainStartupCanonicalBundle(value GeneratedOwnerStores, expected *GeneratedOwnerStores) (*GeneratedOwnerStores, error) {
	if value.repository == nil || value.database == nil || value.guard == nil || value.plans == nil ||
		value.configuration == nil || value.identity == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	canonical, err := value.repository.GeneratedOwnerStores()
	if err != nil || canonical == nil || (expected != nil && canonical != expected) ||
		canonical.identity != value.identity || canonical.repository != value.repository ||
		canonical.database != value.database || canonical.guard != value.guard || canonical.plans != value.plans ||
		canonical.configuration != value.configuration || !canonical.matches(value.repository) || !value.matches(value.repository) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return canonical, nil
}

func (s *GeneratedOwnerStores) generatedDomainStartupSnapshotTx(ctx context.Context, tx *sql.Tx) (GeneratedDomainStartupSnapshot, error) {
	if s == nil || !s.Configured() || ctx == nil || tx == nil {
		return GeneratedDomainStartupSnapshot{}, deploymentexecution.ErrInvalidConfiguration
	}
	gateway, err := appaccess.New(s.database).HostingGatewayStartupSnapshotTx(ctx, tx)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	runtimeSnapshot, err := generatedruntimestate.New(s.database).GeneratedDomainStartupSnapshotTx(ctx, tx)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	generatedDeployments, err := readGeneratedDomainStartupDeployments(ctx, tx)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	artifacts, err := readGeneratedDomainStartupArtifacts(ctx, tx)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	operations, bindings, unresolvedApps, err := readGeneratedDomainStartupLifecycle(ctx, tx, s.database,
		generatedDeployments)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	return GeneratedDomainStartupSnapshot{
		Gateway: gateway, Runtime: runtimeSnapshot, GeneratedDeployments: generatedDeployments, Artifacts: artifacts,
		LifecycleOperationCount: int64(len(operations)), LifecycleOperations: operations,
		LifecycleBindings:       bindings,
		UnresolvedLifecycleApps: unresolvedApps,
	}, nil
}

func readGeneratedDomainStartupDeployments(ctx context.Context, tx *sql.Tx) ([]GeneratedDomainStartupDeployment, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.app_id,d.job_id,d.release_id,d.status,d.provenance_initialized,
		d.deployment_plan_revision_id,d.deployment_plan_revision_number,
		COALESCE(d.actual_configuration_revision_id,''),d.actual_configuration_revision_number,
		COALESCE(a.id,''),COALESCE(j.id,''),COALESCE(j.resource_type,''),COALESCE(j.resource_id,''),
		COALESCE(r.id,''),COALESCE(r.app_id,''),COALESCE(r.deployment_plan_revision_id,''),
		COALESCE(r.deployment_plan_revision_number,0),COALESCE(p.id,''),COALESCE(p.app_id,''),
		COALESCE(p.revision_number,0),COALESCE(p.strategy,''),COALESCE(c.id,''),COALESCE(c.app_id,''),
		COALESCE(c.revision_number,0),COALESCE(j.status,''),COALESCE(j.phase,''),COALESCE(j.attempt,0),
		(SELECT COUNT(*) FROM job_events e WHERE e.job_id=j.id AND e.attempt=j.attempt
		 AND e.level='info' AND e.phase='succeeded' AND e.code='job_succeeded'
		 AND e.message='Deployment completed'),
		(SELECT COUNT(*) FROM job_events e WHERE e.job_id=j.id AND e.attempt=j.attempt
		 AND e.code='job_succeeded'),
		COALESCE((SELECT e.timestamp FROM job_events e WHERE e.job_id=j.id AND e.attempt=j.attempt
		 AND e.level='info' AND e.phase='succeeded' AND e.code='job_succeeded'
		 AND e.message='Deployment completed' ORDER BY e.sequence LIMIT 1),'')
		FROM deployments d
		LEFT JOIN applications a ON a.id=d.app_id
		LEFT JOIN jobs j ON j.id=d.job_id
		LEFT JOIN releases r ON r.id=d.release_id
		LEFT JOIN deployment_plan_revisions p ON p.id=d.deployment_plan_revision_id
		LEFT JOIN application_configuration_revisions c ON c.id=d.actual_configuration_revision_id
		WHERE d.runtime_strategy='generated_node'
		ORDER BY d.app_id,d.id`)
	if err != nil {
		return nil, err
	}
	result := []GeneratedDomainStartupDeployment{}
	for rows.Next() {
		var value GeneratedDomainStartupDeployment
		var initialized int
		var applicationID, jobID, jobResourceType, jobResourceID string
		var releaseID, releaseApp, releasePlan string
		var releasePlanNumber int64
		var planID, planApp, planStrategy string
		var planNumber int64
		var configurationID, configurationApp string
		var configurationNumber int64
		var canonicalEventAt string
		if err := rows.Scan(&value.ID, &value.AppID, &value.JobID, &value.ReleaseID, &value.Status, &initialized,
			&value.DeploymentPlanRevisionID, &value.DeploymentPlanRevisionNumber,
			&value.ActualConfigurationRevisionID, &value.ActualConfigurationRevisionNumber,
			&applicationID, &jobID, &jobResourceType, &jobResourceID, &releaseID, &releaseApp,
			&releasePlan, &releasePlanNumber, &planID, &planApp, &planNumber, &planStrategy,
			&configurationID, &configurationApp, &configurationNumber, &value.JobStatus, &value.JobPhase,
			&value.JobAttempt, &value.CanonicalSuccessEventCount, &value.SuccessEventCount,
			&canonicalEventAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		value.ProvenanceInitialized = initialized == 1
		if canonicalEventAt != "" {
			value.CanonicalSuccessEventAt, err = time.Parse(time.RFC3339Nano, canonicalEventAt)
			if err != nil {
				_ = rows.Close()
				return nil, ErrInvalidDeployment
			}
		}
		configurationValid := value.ActualConfigurationRevisionID == "" && value.ActualConfigurationRevisionNumber == 0
		if value.ActualConfigurationRevisionID != "" {
			configurationValid = canonicalGeneratedDomainUUID(value.ActualConfigurationRevisionID) &&
				value.ActualConfigurationRevisionNumber > 0 && configurationID == value.ActualConfigurationRevisionID &&
				configurationApp == value.AppID && configurationNumber == value.ActualConfigurationRevisionNumber
		}
		if !canonicalGeneratedDomainUUID(value.ID) || !canonicalGeneratedDomainUUID(value.AppID) ||
			!canonicalGeneratedDomainUUID(value.JobID) || !validReleaseID(value.ReleaseID) ||
			!knownStatus(value.Status) || !value.ProvenanceInitialized ||
			!canonicalGeneratedDomainUUID(value.DeploymentPlanRevisionID) || value.DeploymentPlanRevisionNumber < 1 ||
			applicationID != value.AppID || jobID != value.JobID || jobResourceType != "application" ||
			jobResourceID != value.AppID || releaseID != value.ReleaseID || releaseApp != value.AppID ||
			releasePlan != value.DeploymentPlanRevisionID || releasePlanNumber != value.DeploymentPlanRevisionNumber ||
			planID != value.DeploymentPlanRevisionID || planApp != value.AppID ||
			planNumber != value.DeploymentPlanRevisionNumber || planStrategy != "generated_node" || !configurationValid {
			_ = rows.Close()
			return nil, ErrInvalidDeployment
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func readGeneratedDomainStartupArtifacts(ctx context.Context, tx *sql.Tx) ([]GeneratedDomainStartupArtifact, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.release_id,a.deployment_plan_revision_id,
		a.deployment_plan_revision_number,a.component_id,a.attempt_number,a.state,a.compiler_version,
		a.build_definition_digest,COALESCE(a.image_content_id,''),COALESCE(a.diagnostic_code,''),
		a.created_at,a.updated_at,COALESCE(a.finished_at,''),COALESCE(r.id,''),COALESCE(r.app_id,''),
		COALESCE(r.deployment_plan_revision_id,''),COALESCE(r.deployment_plan_revision_number,0),
		COALESCE(p.id,''),COALESCE(p.app_id,''),COALESCE(p.revision_number,0),COALESCE(p.strategy,'')
		FROM generated_image_artifacts a
		LEFT JOIN releases r ON r.id=a.release_id
		LEFT JOIN deployment_plan_revisions p
		 ON p.id=a.deployment_plan_revision_id AND p.revision_number=a.deployment_plan_revision_number
		ORDER BY a.release_id,a.component_id,a.attempt_number,a.id`)
	if err != nil {
		return nil, err
	}
	result := []GeneratedDomainStartupArtifact{}
	for rows.Next() {
		var value GeneratedDomainStartupArtifact
		var compilerVersion, definitionDigest, imageContentID, diagnostic, createdAt, updatedAt, finishedAt string
		var releaseID, releaseApp, releasePlan string
		var releasePlanNumber int64
		var planID, planApp, planStrategy string
		var planNumber int64
		if err := rows.Scan(&value.ID, &value.ReleaseID, &value.DeploymentPlanRevisionID,
			&value.DeploymentPlanRevisionNumber, &value.ComponentID, &value.AttemptNumber, &value.State,
			&compilerVersion, &definitionDigest, &imageContentID, &diagnostic, &createdAt, &updatedAt, &finishedAt,
			&releaseID, &releaseApp, &releasePlan, &releasePlanNumber, &planID, &planApp, &planNumber, &planStrategy); err != nil {
			_ = rows.Close()
			return nil, err
		}
		created, createdErr := time.Parse(time.RFC3339Nano, createdAt)
		updated, updatedErr := time.Parse(time.RFC3339Nano, updatedAt)
		var finished time.Time
		var finishedErr error
		if finishedAt != "" {
			finished, finishedErr = time.Parse(time.RFC3339Nano, finishedAt)
		}
		if !canonicalGeneratedDomainUUID(value.ID) || !validReleaseID(value.ReleaseID) ||
			!canonicalGeneratedDomainUUID(value.DeploymentPlanRevisionID) || value.DeploymentPlanRevisionNumber < 1 ||
			!validGeneratedImageText(value.ComponentID, 256) || value.AttemptNumber < 1 ||
			!validGeneratedImageText(compilerVersion, 128) || !lowerHex(definitionDigest, 64) ||
			createdErr != nil || updatedErr != nil || finishedErr != nil || updated.Before(created) ||
			(!finished.IsZero() && finished.Before(created)) ||
			!validGeneratedDomainStartupArtifactState(value.State, imageContentID, diagnostic, updated, finished) ||
			releaseID != value.ReleaseID || releaseApp == "" || releasePlan != value.DeploymentPlanRevisionID ||
			releasePlanNumber != value.DeploymentPlanRevisionNumber || planID != value.DeploymentPlanRevisionID ||
			planApp != releaseApp || planNumber != value.DeploymentPlanRevisionNumber || planStrategy != "generated_node" {
			_ = rows.Close()
			return nil, ErrInvalidDeployment
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func validGeneratedDomainStartupArtifactState(state generatedimagestate.ArtifactState, imageContentID, diagnostic string, updated, finished time.Time) bool {
	validImage := len(imageContentID) == 71 && imageContentID[:7] == "sha256:" && lowerHex(imageContentID[7:], 64)
	switch state {
	case generatedimagestate.ArtifactBuilding:
		return imageContentID == "" && diagnostic == "" && finished.IsZero()
	case generatedimagestate.ArtifactReady:
		return validImage && diagnostic == "" && !finished.IsZero() && updated.Equal(finished)
	case generatedimagestate.ArtifactFailed:
		return imageContentID == "" && generatedDomainArtifactFailureDiagnostic(diagnostic) &&
			!finished.IsZero() && updated.Equal(finished)
	case generatedimagestate.ArtifactCancelled:
		return imageContentID == "" && diagnostic == string(generatedimagestate.DiagnosticBuildCancelled) &&
			!finished.IsZero() && updated.Equal(finished)
	case generatedimagestate.ArtifactUnavailable:
		return validImage && diagnostic == string(generatedimagestate.DiagnosticImageUnavailable) && !finished.IsZero()
	default:
		return false
	}
}

func generatedDomainArtifactFailureDiagnostic(value string) bool {
	switch generatedimagestate.DiagnosticCode(value) {
	case generatedimagestate.DiagnosticBuildFailed, generatedimagestate.DiagnosticBuildTimeout,
		generatedimagestate.DiagnosticBuildContextInvalid, generatedimagestate.DiagnosticBuildContextTooLarge,
		generatedimagestate.DiagnosticBuildOutputTruncated, generatedimagestate.DiagnosticBuildDiskExhausted,
		generatedimagestate.DiagnosticBuildCapacityExceeded, generatedimagestate.DiagnosticRuntimeUnavailable,
		generatedimagestate.DiagnosticSourceUnavailable, generatedimagestate.DiagnosticSourceIntegrityFailed,
		generatedimagestate.DiagnosticProcessTerminationFailed, generatedimagestate.DiagnosticDaemonRestarted,
		generatedimagestate.DiagnosticInternalError:
		return true
	default:
		return false
	}
}

func readGeneratedDomainStartupLifecycle(ctx context.Context, tx *sql.Tx, db *sql.DB,
	deployments []GeneratedDomainStartupDeployment,
) (
	[]GeneratedDomainStartupLifecycleOperation, []GeneratedDomainStartupLifecycleBinding, []string, error,
) {
	rows, err := tx.QueryContext(ctx, `SELECT app_id,job_id,deployment_id,desired_revision
		FROM application_lifecycle_operations ORDER BY app_id,desired_revision,job_id`)
	if err != nil {
		return nil, nil, nil, err
	}
	operations := []GeneratedDomainStartupLifecycleOperation{}
	apps := map[string]struct{}{}
	for rows.Next() {
		var operation GeneratedDomainStartupLifecycleOperation
		if err := rows.Scan(&operation.AppID, &operation.JobID, &operation.DeploymentID,
			&operation.DesiredRevision); err != nil {
			_ = rows.Close()
			return nil, nil, nil, err
		}
		if !canonicalGeneratedDomainUUID(operation.AppID) || !canonicalGeneratedDomainUUID(operation.JobID) ||
			!canonicalGeneratedDomainUUID(operation.DeploymentID) || operation.DesiredRevision < 1 {
			_ = rows.Close()
			return nil, nil, nil, deploymentbinding.ErrInvalidBinding
		}
		operations = append(operations, operation)
		apps[operation.AppID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}

	missingCurrent := map[string]struct{}{}
	headRows, err := tx.QueryContext(ctx, `SELECT h.app_id,h.revision_number,COALESCE(current.job_id,'')
		FROM application_desired_lifecycle_heads h
		LEFT JOIN application_lifecycle_operations current
		 ON current.app_id=h.app_id AND current.desired_revision=h.revision_number
		WHERE h.revision_number>0 ORDER BY h.app_id`)
	if err != nil {
		return nil, nil, nil, err
	}
	for headRows.Next() {
		var appID, currentJobID string
		var revision int64
		if err := headRows.Scan(&appID, &revision, &currentJobID); err != nil {
			_ = headRows.Close()
			return nil, nil, nil, err
		}
		if !canonicalGeneratedDomainUUID(appID) || revision < 1 {
			_ = headRows.Close()
			return nil, nil, nil, deploymentbinding.ErrInvalidBinding
		}
		if currentJobID == "" {
			missingCurrent[appID] = struct{}{}
		} else if !canonicalGeneratedDomainUUID(currentJobID) {
			_ = headRows.Close()
			return nil, nil, nil, deploymentbinding.ErrInvalidBinding
		}
	}
	if err := headRows.Err(); err != nil {
		_ = headRows.Close()
		return nil, nil, nil, err
	}
	if err := headRows.Close(); err != nil {
		return nil, nil, nil, err
	}

	repository := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(repository)
	if err != nil || !validator.ConfiguredFor(db) {
		return nil, nil, nil, deploymentbinding.ErrInvalidConfiguration
	}
	bindingRepository, err := deploymentbinding.New(db, validator)
	if err != nil {
		return nil, nil, nil, err
	}
	bindings := make([]GeneratedDomainStartupLifecycleBinding, 0, len(deployments))
	for _, deployment := range deployments {
		binding, err := bindingRepository.ReadTx(ctx, tx, deployment.JobID)
		if err != nil {
			return nil, nil, nil, err
		}
		if binding.JobID() != deployment.JobID || binding.ApplicationID() != deployment.AppID {
			return nil, nil, nil, deploymentbinding.ErrInvalidBinding
		}
		bindings = append(bindings, GeneratedDomainStartupLifecycleBinding{
			DeploymentJobID: binding.JobID(), AppID: binding.ApplicationID(),
			DesiredRevision: binding.DesiredRevision(), Mode: binding.Mode(),
			AuthorizingOperationID: binding.AuthorizingLifecycleJobID(),
		})
	}
	appIDs := make([]string, 0, len(apps))
	for appID := range apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	unresolved := missingCurrent
	for _, appID := range appIDs {
		value, err := validator.UnresolvedHistoryTx(ctx, tx, appID)
		if err != nil {
			return nil, nil, nil, err
		}
		if value {
			unresolved[appID] = struct{}{}
		}
	}
	for _, operation := range operations {
		completion, err := validator.CurrentOperationTx(ctx, tx, deploymentbinding.OperationRef{
			ApplicationID: operation.AppID, OperationID: operation.JobID, DesiredRevision: operation.DesiredRevision,
		})
		if err != nil {
			return nil, nil, nil, err
		}
		switch completion {
		case deploymentbinding.CompletionComplete:
			operation.Complete = true
			for index := range operations {
				if operations[index].AppID == operation.AppID && operations[index].JobID == operation.JobID &&
					operations[index].DeploymentID == operation.DeploymentID &&
					operations[index].DesiredRevision == operation.DesiredRevision {
					operations[index].Complete = true
					break
				}
			}
		case deploymentbinding.CompletionIncomplete:
			unresolved[operation.AppID] = struct{}{}
		default:
			return nil, nil, nil, deploymentbinding.ErrInvalidBinding
		}
	}
	result := make([]string, 0, len(unresolved))
	for appID := range unresolved {
		result = append(result, appID)
	}
	sort.Strings(result)
	return operations, bindings, result, nil
}

func canonicalGeneratedDomainUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
