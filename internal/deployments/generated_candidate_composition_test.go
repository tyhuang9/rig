package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestCandidateStoreCompositionPreservesSharedOwnerAndRejectsForeignStores(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	original := fixture.repository
	repository, err := NewWithExecutionGuardAndGeneratedCandidates(original.db, original.executionGuard,
		original.migrationPlans, original.migrationConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	copyBefore := *repository
	owner, err := repository.GeneratedCandidateAuthority(fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := copyBefore.GeneratedCandidateAuthority(fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil || alias != owner || repository.migrationConfigured() {
		t.Fatalf("candidate-only composition forked owner or enabled migration: same=%t err=%v", alias == owner, err)
	}
	foreign := newCandidateFactoryFixture(t).repository
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
		t.Run(test.name, func(t *testing.T) {
			if result, err := test.make(); result != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
				t.Fatalf("foreign dependency accepted: result=%v error=%v", result != nil, err)
			}
		})
	}
}

func TestCandidateIntentPreventsGenericTerminalWritesAfterCallerRead(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	ctx := context.Background()
	request := fixture.validation.request
	state := fixture.validation.state
	// Capture the receiptless state a generic failure caller may have read.
	before, err := state.Get(ctx, request.Claim.ApplicationID(), request.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	reservation := fixture.reserve(t)
	defer reservation.Release()
	execution := fixture.pin(t, reservation)
	defer execution.Leave()
	_ = fixture.beginCreate(t, execution)
	for _, status := range []Status{Failed, Cancelled} {
		if _, err := fixture.validation.migration.repository.Transition(ctx, before.AppID, before.DeploymentID, status, "internal_error"); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("generic main transition %s = %v", status, err)
		}
	}
	for _, target := range []struct {
		phase      generatedruntimestate.Phase
		diagnostic generatedruntimestate.DiagnosticCode
	}{
		{generatedruntimestate.PhaseFailed, generatedruntimestate.DiagnosticInternalError},
		{generatedruntimestate.PhaseCancelled, generatedruntimestate.DiagnosticCancelled},
	} {
		if _, err := state.Advance(ctx, before.AppID, before.DeploymentID, before.Phase, target.phase, target.diagnostic); err == nil {
			t.Fatalf("generic runtime transition %s succeeded", target.phase)
		}
	}
	if _, err := state.FailComponent(ctx, before.AppID, before.DeploymentID, "api", generatedruntimestate.ComponentStarting, generatedruntimestate.DiagnosticInternalError); !errors.Is(err, generatedruntimestate.ErrInvalidTransition) {
		t.Fatalf("generic component failure = %v", err)
	}
	after := fixture.validation.read(t)
	if after.main.status != Applying || after.runtime.phase != generatedruntimestate.PhaseStartingCandidate || after.components[0].state != generatedruntimestate.ComponentStarting {
		t.Fatal("generic failure caller rewrote receipt-owned state")
	}
	if fixture.runner.count(candidateOwnerCreate) != 0 {
		t.Fatal("terminal-write rejection issued Docker create")
	}
}
