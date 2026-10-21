package deployments

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
)

type generatedOwnerDomainFixture struct {
	stores           *GeneratedOwnerStores
	dataRoot         string
	workingDirectory string
	base             repositoryFixture
}

func newGeneratedOwnerDomainFixture(t *testing.T) generatedOwnerDomainFixture {
	t.Helper()
	base := newRepositoryFixture(t)
	guarded := newGuardedRepositoryFixture(t, base)
	plans, err := deploymentplans.New(base.repository.db, base.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(base.repository.db, base.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewWithExecutionGuardAndGeneratedCandidates(
		base.repository.db,
		guarded.guard,
		plans,
		configuration,
	)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	return generatedOwnerDomainFixture{
		stores:           stores,
		dataRoot:         base.dataRoot,
		workingDirectory: guarded.workingDirectory,
		base:             base,
	}
}

func TestGeneratedOwnerStoresConfiguredForDomainRequiresExactSealedProvenance(t *testing.T) {
	original := newGeneratedOwnerDomainFixture(t)
	foreign := newGeneratedOwnerDomainFixture(t)

	if !original.stores.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
		t.Fatal("bundle rejected its exact domain configuration")
	}
	copyValue := *original.stores
	if !copyValue.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
		t.Fatal("unchanged bundle copy rejected its exact domain configuration")
	}

	rootAlias := generatedOwnerDomainPathAlias(t, original.dataRoot, "root-alias")
	workingAlias := generatedOwnerDomainPathAlias(t, original.workingDirectory, "working-alias")
	for name, request := range map[string]struct {
		root    string
		working string
	}{
		"wrong root":              {root: foreign.dataRoot, working: original.workingDirectory},
		"alias root":              {root: rootAlias, working: original.workingDirectory},
		"noncanonical root":       {root: original.dataRoot + string(filepath.Separator) + ".", working: original.workingDirectory},
		"wrong working directory": {root: original.dataRoot, working: foreign.workingDirectory},
		"alias working directory": {root: original.dataRoot, working: workingAlias},
		"noncanonical working directory": {
			root: original.dataRoot, working: original.workingDirectory + string(filepath.Separator) + ".",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if original.stores.ConfiguredForDomain(request.root, request.working) {
				t.Fatal("mismatched domain configuration accepted")
			}
		})
	}

	for name, mutate := range map[string]func(*GeneratedOwnerStores){
		"repository":    func(value *GeneratedOwnerStores) { value.repository = foreign.stores.repository },
		"database":      func(value *GeneratedOwnerStores) { value.database = foreign.stores.database },
		"guard":         func(value *GeneratedOwnerStores) { value.guard = foreign.stores.guard },
		"plans":         func(value *GeneratedOwnerStores) { value.plans = foreign.stores.plans },
		"configuration": func(value *GeneratedOwnerStores) { value.configuration = foreign.stores.configuration },
		"identity":      func(value *GeneratedOwnerStores) { value.identity = foreign.stores.identity },
	} {
		t.Run("changed sealed "+name, func(t *testing.T) {
			changed := *original.stores
			mutate(&changed)
			if changed.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
				t.Fatal("changed sealed dependency accepted")
			}
		})
	}

	wrongPlans, err := deploymentplans.New(original.stores.database, foreign.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	assertGeneratedOwnerDomainCompositionRejected(t, original, original.stores.guard, wrongPlans, original.stores.configuration, "plans root")
	wrongConfiguration, err := appconfig.New(original.stores.database, foreign.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	assertGeneratedOwnerDomainCompositionRejected(t, original, original.stores.guard, original.stores.plans, wrongConfiguration, "configuration root")
	wrongGuard := newGuardedRepositoryFixture(t, original.base).guard
	assertGeneratedOwnerDomainCompositionRejected(t, original, wrongGuard, original.stores.plans, original.stores.configuration, "guard working directory")

	privateSnapshot := *original.stores
	callerCopy := privateSnapshot
	callerCopy = *foreign.stores
	if callerCopy.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
		t.Fatal("overwritten caller copy retained the original domain provenance")
	}
	if !callerCopy.ConfiguredForDomain(foreign.dataRoot, foreign.workingDirectory) {
		t.Fatal("overwritten caller copy did not report its new exact foreign provenance")
	}
	if !privateSnapshot.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
		t.Fatal("caller overwrite retargeted an earlier private value snapshot")
	}
	overwrittenRepository := newGeneratedOwnerDomainFixture(t)
	*overwrittenRepository.stores.Repository() = *foreign.stores.Repository()
	if overwrittenRepository.stores.ConfiguredForDomain(overwrittenRepository.dataRoot, overwrittenRepository.workingDirectory) {
		t.Fatal("bundle accepted a public repository handle overwritten with foreign dependencies")
	}
	zeroRepository := newGeneratedOwnerDomainFixture(t)
	*zeroRepository.stores.Repository() = Repository{}
	if zeroRepository.stores.ConfiguredForDomain(zeroRepository.dataRoot, zeroRepository.workingDirectory) {
		t.Fatal("bundle accepted a zeroed public repository handle")
	}

	var zero GeneratedOwnerStores
	if zero.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
		t.Fatal("zero bundle reported domain configuration")
	}
	var missing *GeneratedOwnerStores
	if missing.ConfiguredForDomain(original.dataRoot, original.workingDirectory) {
		t.Fatal("nil bundle reported domain configuration")
	}
}

func assertGeneratedOwnerDomainCompositionRejected(
	t *testing.T,
	expected generatedOwnerDomainFixture,
	guard *deploymentexecution.Guard,
	plans *deploymentplans.Store,
	configuration *appconfig.Store,
	name string,
) {
	t.Helper()
	repository, err := NewWithExecutionGuardAndGeneratedCandidates(expected.stores.database, guard, plans, configuration)
	if err != nil {
		t.Fatalf("construct same-database bundle with mismatched %s: %v", name, err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatalf("issue same-database bundle with mismatched %s: %v", name, err)
	}
	if !stores.Configured() {
		t.Fatalf("same-database bundle with mismatched %s was not generally configured", name)
	}
	if stores.ConfiguredForDomain(expected.dataRoot, expected.workingDirectory) {
		t.Fatalf("same-database bundle with mismatched %s accepted domain configuration", name)
	}
}

func generatedOwnerDomainPathAlias(t *testing.T, target, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		alias := strings.ToUpper(target)
		if alias == target {
			alias = strings.ToLower(target)
		}
		if alias == target {
			t.Fatal("test path has no alternate Windows case spelling")
		}
		return alias
	}
	alias := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func TestGeneratedOwnerStoresAreSealedAndSharedAcrossRepositoryCopies(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	original := fixture.repository
	repository, err := NewWithExecutionGuardAndGeneratedCandidates(
		original.db,
		original.executionGuard,
		original.migrationPlans,
		original.migrationConfiguration,
	)
	if err != nil {
		t.Fatal(err)
	}
	if repository.migrationConfigured() {
		t.Fatal("candidate store composition enabled migration execution")
	}

	copyBefore := *repository
	stores, err := copyBefore.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	alias, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	if alias != stores || !stores.Configured() || stores.Repository() != &copyBefore ||
		stores.Database() != repository.db || stores.ExecutionGuard() != repository.executionGuard ||
		stores.Plans() != repository.migrationPlans || stores.Configuration() != repository.migrationConfiguration {
		t.Fatal("repository copies did not preserve the exact generated-owner store bundle")
	}

	var zero GeneratedOwnerStores
	if zero.Configured() || zero.Repository() != nil || zero.Database() != nil ||
		zero.ExecutionGuard() != nil || zero.Plans() != nil || zero.Configuration() != nil {
		t.Fatal("zero generated-owner store bundle was usable")
	}

	conflicting := copyBefore
	conflicting.executionGuard = nil
	if _, err := conflicting.GeneratedOwnerStores(); err == nil || err != deploymentexecution.ErrInvalidConfiguration {
		t.Fatalf("conflicting repository copy error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Repository)
	}{
		{"registry", func(value *Repository) { value.generatedOwnerStoresRegistry = nil }},
		{"plans", func(value *Repository) { value.migrationPlans = nil }},
		{"configuration", func(value *Repository) { value.migrationConfiguration = nil }},
	} {
		t.Run("missing-"+test.name, func(t *testing.T) {
			value := copyBefore
			test.mutate(&value)
			if result, err := value.GeneratedOwnerStores(); result != nil || err != deploymentexecution.ErrInvalidConfiguration {
				t.Fatalf("missing dependency accepted: result=%v error=%v", result != nil, err)
			}
		})
	}

	foreign := newGeneratedMigrationFixture(t).repository
	for _, test := range []struct {
		name string
		make func() (*Repository, error)
	}{
		{"guard", func() (*Repository, error) {
			return NewWithExecutionGuardAndGeneratedCandidates(original.db, foreign.executionGuard, original.migrationPlans, original.migrationConfiguration)
		}},
		{"plans", func() (*Repository, error) {
			return NewWithExecutionGuardAndGeneratedCandidates(original.db, original.executionGuard, foreign.migrationPlans, original.migrationConfiguration)
		}},
		{"configuration", func() (*Repository, error) {
			return NewWithExecutionGuardAndGeneratedCandidates(original.db, original.executionGuard, original.migrationPlans, foreign.migrationConfiguration)
		}},
	} {
		t.Run("foreign-"+test.name, func(t *testing.T) {
			if result, err := test.make(); result != nil || err != deploymentexecution.ErrInvalidConfiguration {
				t.Fatalf("foreign dependency accepted: result=%v error=%v", result != nil, err)
			}
		})
	}
}

func TestGeneratedOwnerStoresDoNotAliasReconstructedRepositoryIdentity(t *testing.T) {
	fixture := newGeneratedMigrationFixture(t)
	first, err := fixture.repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := NewWithExecutionGuardAndGeneratedCandidates(
		fixture.repository.db,
		fixture.repository.executionGuard,
		fixture.repository.migrationPlans,
		fixture.repository.migrationConfiguration,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reconstructed.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("reconstructed repository reused another repository identity")
	}
}
