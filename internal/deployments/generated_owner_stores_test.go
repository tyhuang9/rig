package deployments

import (
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
)

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
