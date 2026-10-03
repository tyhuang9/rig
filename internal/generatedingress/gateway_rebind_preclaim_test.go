package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func TestInspectGatewayRebindPreclaimPredecessorAcceptsExactReadOnlyProposal(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	beforeDatabase, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
	if err != nil {
		t.Fatal(err)
	}
	beforeArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(fixture.runner.commands)
	if err := fixture.manager.InspectGatewayRebindPreclaimPredecessor(
		context.Background(), fixture.repository, fixture.proposal,
	); err != nil {
		t.Fatalf("preclaim inspection failed: %v", err)
	}
	afterDatabase, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
	if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) {
		t.Fatalf("preclaim database changed: %v", err)
	}
	afterArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
	if err != nil || !sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) ||
		len(fixture.runner.commands) != beforeCommands {
		t.Fatalf("preclaim mutated protected/Docker state: artifacts=%t commands=%d err=%v",
			sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts), len(fixture.runner.commands)-beforeCommands, err)
	}
	var claimCount int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims`).Scan(&claimCount); err != nil || claimCount != 0 {
		t.Fatalf("preclaim persisted claim: count=%d err=%v", claimCount, err)
	}
}

func TestInspectGatewayRebindPreclaimPredecessorRejectsClaimsAndProtectedMismatch(t *testing.T) {
	t.Run("claim already exists", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		if err := fixture.manager.InspectGatewayRebindPreclaimPredecessor(
			context.Background(), fixture.repository, fixture.proposal,
		); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("existing claim error = %v", err)
		}
	})
	t.Run("proposal identity disagrees with protected state", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		snapshot, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
		if err != nil || !gatewayRebindPreclaimMatches(snapshot, fixture.state, fixture.journal) {
			t.Fatalf("baseline mismatch: %v", err)
		}
		snapshot.ProposedClaim.Spec.PredecessorProtectedIdentityDigest = strings.Repeat("9", 64)
		if gatewayRebindPreclaimMatches(snapshot, fixture.state, fixture.journal) {
			t.Fatal("wrong protected identity accepted")
		}
	})
	t.Run("extra protected LAN binding", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		snapshot, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
		if err != nil {
			t.Fatal(err)
		}
		state := cloneGatewayV2RouteState(fixture.state)
		extra := cloneGatewayV2AppRoute(state.Apps[fixture.stateAppID()])
		extra.LAN.AllocationID = uuid.NewString()
		state.Apps[uuid.NewString()] = extra
		if gatewayRebindPreclaimMatches(snapshot, state, fixture.journal) {
			t.Fatal("extra protected LAN binding accepted")
		}
	})
	t.Run("corrupt protected history", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		if err := os.WriteFile(fixture.store.v2Path, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.manager.InspectGatewayRebindPreclaimPredecessor(
			context.Background(), fixture.repository, fixture.proposal,
		); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("corrupt history error = %v", err)
		}
	})
	t.Run("replaced protected history fingerprint", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		body, err := os.ReadFile(fixture.store.v2Path)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := func() {
			if err := os.Remove(fixture.store.v2Path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.store.v2Path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.manager.inspectGatewayRebindPreclaimPredecessor(
			context.Background(), fixture.repository, fixture.proposal, checkpoint,
		); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("replaced protected history error = %v", err)
		}
	})
}

func TestInspectGatewayRebindPreclaimPredecessorRejectsDriftCancellationAndLockRelease(t *testing.T) {
	t.Run("SQLite drift", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		checkpoint := func() {
			if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, gatewayRebindTestAdministrator); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.manager.inspectGatewayRebindPreclaimPredecessor(
			context.Background(), fixture.repository, fixture.proposal, checkpoint,
		); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("SQLite drift error = %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		ctx, cancel := context.WithCancel(context.Background())
		if err := fixture.manager.inspectGatewayRebindPreclaimPredecessor(
			ctx, fixture.repository, fixture.proposal, cancel,
		); !IsCode(err, DiagnosticCancelled) {
			t.Fatalf("cancellation error = %v", err)
		}
	})
	t.Run("lock release failure", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		originalAcquire := managerAcquireGatewayOSLock
		acquired := false
		released := false
		managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
			acquired = true
			if fixture.manager.mu.TryLock() {
				fixture.manager.mu.Unlock()
				t.Fatal("OS lock acquired before Manager lock")
			}
			return func() error {
				released = true
				return errors.New("injected release failure")
			}, nil
		}
		t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
		if err := fixture.manager.InspectGatewayRebindPreclaimPredecessor(
			context.Background(), fixture.repository, fixture.proposal,
		); !IsCode(err, DiagnosticRouteUnresolved) || !acquired || !released {
			t.Fatalf("release failure error=%v acquired=%t released=%t", err, acquired, released)
		}
	})
}

func TestInspectGatewayRebindPreclaimPredecessorRejectsNilInputs(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	if err := (*Manager)(nil).InspectGatewayRebindPreclaimPredecessor(
		context.Background(), fixture.repository, fixture.proposal,
	); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("nil manager error = %v", err)
	}
	if err := fixture.manager.InspectGatewayRebindPreclaimPredecessor(
		context.Background(), (*appaccess.Repository)(nil), fixture.proposal,
	); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("nil repository error = %v", err)
	}
}
