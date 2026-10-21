package generatedingress

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestCandidateStoreCompositionPreservesSharedOwnerAndRejectsForeignStores(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	copyBefore := *fixture.repository.stores
	owner, err := NewGeneratedCandidateAuthority(fixture.repository.stores, fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := NewGeneratedCandidateAuthority(&copyBefore, fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil || alias != owner {
		t.Fatalf("candidate store alias forked owner: same=%t err=%v", alias == owner, err)
	}
	if result, err := NewGeneratedCandidateAuthority(&deployments.GeneratedOwnerStores{}, fixture.process, fixture.environment, fixture.budget, fixture.options); result != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("unconfigured store bundle accepted: result=%v error=%v", result != nil, err)
	}
	foreign := newCandidateFactoryFixture(t)
	foreignOwner, err := NewGeneratedCandidateAuthority(foreign.repository.stores, foreign.process, foreign.environment, foreign.budget, foreign.options)
	if err != nil {
		t.Fatal(err)
	}
	if foreignOwner == owner || foreignOwner.ledger == owner.ledger || foreignOwner.heads == owner.heads {
		t.Fatal("foreign store bundle aliased the existing candidate owner")
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
