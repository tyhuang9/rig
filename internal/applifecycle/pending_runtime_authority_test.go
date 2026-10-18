package applifecycle

import (
	"context"
	"errors"
	"testing"
)

func TestPendingRuntimeAuthorityLoadsCanonicalBoundaryAndCompleteStartRoster(t *testing.T) {
	fixture := newOperationFixture(t, 2)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Restart, BaselineRunning, "runtime-authority", 0), fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	prepareRuntimeStartBoundary(t, fixture, claim, accepted.Operation)
	for _, component := range accepted.Operation.Pins.Components {
		if _, created, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, component.Name, EffectCommand); err != nil || !created {
			t.Fatalf("begin start %s created=%t error=%v", component.Name, created, err)
		}
	}
	authorities, err := fixture.repo.PendingRuntimeStarts(context.Background(), claim)
	if err != nil || len(authorities) != 2 {
		t.Fatalf("pending starts = %#v, %v", authorities, err)
	}
	for _, authority := range authorities {
		if authority.Operation.JobID != accepted.Operation.JobID || authority.Intent.JobID != accepted.Operation.JobID ||
			authority.Intent.Key.Boundary != BoundaryStart || authority.Pending.JobID() != accepted.Operation.JobID ||
			authority.Pending.Attempt() != claim.Attempt() || authority.Pending.IntentDigest() != authority.Intent.RequestDigest {
			t.Fatalf("canonical runtime authority = %#v", authority)
		}
		loaded, err := fixture.repo.PendingRuntimeBoundary(context.Background(), claim, BoundaryStart, authority.Intent.Key.ComponentName)
		if err != nil || !SameRuntimeStartAuthority(authority, RuntimeStartAuthority{
			Operation: loaded.Operation, Intent: loaded.Intent, Pending: loaded.Pending,
		}) {
			t.Fatalf("reloaded authority = %#v, %v", loaded, err)
		}
	}
}

func TestPendingRuntimeStartsRefusesReceiptStaleClaimForeignAndMalformedRecords(t *testing.T) {
	t.Run("receipt is history", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Start, BaselineStopped, "receipted-start", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		prepareRuntimeStartBoundary(t, fixture, claim, accepted.Operation)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryStart, accepted.Operation.Pins.Components[0].Name, EffectCommand, OutcomeContainerRunning)
		roster, err := fixture.repo.PendingRuntimeStarts(context.Background(), claim)
		if err != nil || len(roster) != 1 || roster[0].Receipt == nil || roster[0].IsPending() {
			t.Fatalf("receipted start roster = %#v, %v", roster, err)
		}
	})

	t.Run("stale worker attempt", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Start, BaselineStopped, "stale-claim", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		prepareRuntimeStartBoundary(t, fixture, claim, accepted.Operation)
		component := accepted.Operation.Pins.Components[0].Name
		if _, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, component, EffectCommand); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, accepted.Operation.JobID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repo.PendingRuntimeStarts(context.Background(), claim); !errors.Is(err, ErrEffectNotAuthorized) {
			t.Fatalf("stale claim error = %v", err)
		}
	})

	t.Run("foreign pending start", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		first, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Start, BaselineStopped, "first", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		firstClaim := runningClaim(t, fixture, first.Operation.JobID)
		prepareRuntimeStartBoundary(t, fixture, firstClaim, first.Operation)
		component := first.Operation.Pins.Components[0].Name
		if _, _, err := beginEffect(t, fixture, firstClaim, first.Operation, BoundaryStart, component, EffectCommand); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE jobs SET status='failed',phase='failed' WHERE id=?`, first.Operation.JobID); err != nil {
			t.Fatal(err)
		}
		second, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Start, BaselineStopped, "second", 1), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		secondClaim := runningClaim(t, fixture, second.Operation.JobID)
		prepareRuntimeStartBoundary(t, fixture, secondClaim, second.Operation)
		if _, _, err := beginEffect(t, fixture, secondClaim, second.Operation, BoundaryStart, component, EffectCommand); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repo.PendingRuntimeStarts(context.Background(), secondClaim); !errors.Is(err, ErrEffectNotAuthorized) {
			t.Fatalf("foreign pending start error = %v", err)
		}
	})

	t.Run("malformed retained intent", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Start, BaselineStopped, "malformed", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		prepareRuntimeStartBoundary(t, fixture, claim, accepted.Operation)
		component := accepted.Operation.Pins.Components[0].Name
		if _, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, component, EffectCommand); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_intent_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE application_lifecycle_effect_intents SET request_json='{}' WHERE job_id=? AND boundary='start'`, accepted.Operation.JobID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repo.PendingRuntimeStarts(context.Background(), claim); err == nil {
			t.Fatal("malformed start intent was authorized")
		}
	})
}

func TestPendingRuntimeBoundaryRestrictsRetainedPhysicalBoundaries(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, fixture.request(Restart, BaselineRunning, "boundary", 0), fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectCommand, OutcomeRoutesAbsent)
	component := accepted.Operation.Pins.Components[0].Name
	if _, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStop, component, EffectCommand); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repo.PendingRuntimeBoundary(context.Background(), claim, BoundaryStop, component); err != nil {
		t.Fatal(err)
	}
	for _, key := range []EffectKey{{Boundary: BoundaryPublish}, {Boundary: BoundaryStop}, {Boundary: BoundaryHealth, ComponentName: component}} {
		if _, err := fixture.repo.PendingRuntimeBoundary(context.Background(), claim, key.Boundary, key.ComponentName); !errors.Is(err, ErrEffectNotAuthorized) {
			t.Fatalf("invalid retained boundary %#v error = %v", key, err)
		}
	}
}

func prepareRuntimeStartBoundary(t *testing.T, fixture *operationFixture, claim WorkerClaim, operation Operation) {
	t.Helper()
	writeEffect(t, fixture, claim, operation, BoundaryWithdraw, "", modeFor(operation, BoundaryWithdraw), OutcomeRoutesAbsent)
	if operation.Action == Restart && operation.Pins.BaselineDisposition == BaselineRunning {
		for _, component := range operation.Pins.Components {
			writeEffect(t, fixture, claim, operation, BoundaryStop, component.Name, EffectCommand, OutcomeContainerStopped)
		}
	}
}
