package deployments

import (
	"context"
	"database/sql"
	"sync"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
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
