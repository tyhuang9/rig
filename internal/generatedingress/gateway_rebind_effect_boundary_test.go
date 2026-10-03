package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

const gatewayRebindEffectBoundaryLeaseHelperEnvironment = "HOSTD_GATEWAY_REBIND_EFFECT_BOUNDARY_LEASE_HELPER"

type gatewayRebindEffectBoundaryFixture struct {
	predecessor gatewayRebindPredecessorFixture
	intent      gatewayRebindProtectedIntent
	reads       gatewayRebindSuccessorPreflightReads
	inspect     gatewayRebindDockerInspector
}

func newGatewayRebindEffectBoundaryFixture(t *testing.T) gatewayRebindEffectBoundaryFixture {
	t.Helper()
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	selection, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(observation.result.RebindOperationID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, selection.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.Store.installExact(intent); err != nil {
		t.Fatal(err)
	}
	reads := gatewayRebindEffectBoundaryReads(observation)
	dockerObservation := gatewayRebindFixtureDockerObservation(t, fixture)
	inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
		return dockerObservation, nil
	}
	return gatewayRebindEffectBoundaryFixture{
		predecessor: fixture, intent: intent, reads: reads, inspect: inspect,
	}
}

func gatewayRebindEffectBoundaryReads(observation gatewayRebindSuccessorPreflightObservation) gatewayRebindSuccessorPreflightReads {
	return gatewayRebindSuccessorPreflightReads{
		network: gatewayV2NetworkPlanReads{
			candidates: func() ([]hostNetworkCandidate, error) {
				return append([]hostNetworkCandidate(nil), observation.candidates...), nil
			},
			host: func() (gatewayV2HostNetworkSnapshot, error) {
				return gatewayV2HostNetworkSnapshot{
					Routes:     append([]netip.Prefix(nil), observation.host.Routes...),
					Interfaces: append([]netip.Prefix(nil), observation.host.Interfaces...),
				}, nil
			},
			docker: func(context.Context) ([]netip.Prefix, error) {
				return append([]netip.Prefix(nil), observation.docker...), nil
			},
		},
		dockerIDs: func(context.Context) ([]string, error) {
			return append([]string(nil), observation.dockerIDs...), nil
		},
	}
}

func installGatewayRebindEffectBoundaryProgress(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	count int,
) []gatewayRebindProgressRecord {
	t.Helper()
	if count < 0 || count > 2 {
		t.Fatalf("invalid progress count %d", count)
	}
	records := make([]gatewayRebindProgressRecord, 0, count)
	if count >= 1 {
		first, err := newGatewayRebindSuccessorIntentProgress(fixture.intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
			fixture.intent.Generation, fixture.intent.OperationID, first.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		records = append(records, first)
	}
	if count == 2 {
		second, err := newGatewayRebindStageIntentProgress(fixture.intent, records[0],
			gatewayRebindProgressStageObservation(fixture.intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
			fixture.intent.Generation, fixture.intent.OperationID, second.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		records = append(records, second)
	}
	return records
}

func TestGatewayRebindEffectBoundaryAttestsStablePreparedInitialRebind(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	beforeCommands := len(fixture.predecessor.runner.commands)
	dockerReads, networkIDReads := 0, 0
	originalInspect := fixture.inspect
	fixture.inspect = func(ctx context.Context, source routeState, state gatewayV2RouteState,
		journal gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
		dockerReads++
		if fixture.predecessor.manager.mu.TryLock() {
			fixture.predecessor.manager.mu.Unlock()
			t.Fatal("Docker observation ran without gateway lock")
		}
		return originalInspect(ctx, source, state, journal)
	}
	originalIDs := fixture.reads.dockerIDs
	fixture.reads.dockerIDs = func(ctx context.Context) ([]string, error) {
		networkIDReads++
		if fixture.predecessor.manager.mu.TryLock() {
			fixture.predecessor.manager.mu.Unlock()
			t.Fatal("successor network observation ran without gateway lock")
		}
		return originalIDs(ctx)
	}
	var fenceBefore string
	if err := fixture.predecessor.db.QueryRow(`SELECT sql FROM sqlite_master
		WHERE type='trigger' AND name='lan_gateway_rebind_fence_runtime_head_update'`).Scan(&fenceBefore); err != nil {
		t.Fatal(err)
	}
	evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, fixture.reads, fixture.inspect, nil)
	if err != nil || !validGatewayRebindEffectBoundaryEvidence(evidence) {
		t.Fatalf("evidence=%#v error=%v", evidence, err)
	}
	if evidence.Generation != fixture.intent.Generation || evidence.OperationID != fixture.intent.OperationID ||
		evidence.DatabaseDigest != fixture.intent.DatabaseDigest ||
		evidence.ProtectedIntentDigest != fixture.intent.Digest ||
		evidence.NetworkObservationDigest != fixture.intent.NetworkObservationDigest ||
		evidence.ProgressCount != 0 || !validSHA256(evidence.ProgressDigest) {
		t.Fatalf("evidence does not bind installed intent: %#v", evidence)
	}
	if len(fixture.predecessor.runner.commands) != beforeCommands {
		t.Fatalf("attestor executed routed-request Docker commands: %d", len(fixture.predecessor.runner.commands)-beforeCommands)
	}
	if dockerReads != 2 || networkIDReads != 4 {
		t.Fatalf("composite observation reads: Docker=%d network IDs=%d, want 2 and 4", dockerReads, networkIDReads)
	}
	var fenceAfter string
	if err := fixture.predecessor.db.QueryRow(`SELECT sql FROM sqlite_master
		WHERE type='trigger' AND name='lan_gateway_rebind_fence_runtime_head_update'`).Scan(&fenceAfter); err != nil || fenceAfter != fenceBefore {
		t.Fatalf("active migration-034 SQL fence changed: equal=%t error=%v", fenceAfter == fenceBefore, err)
	}
	if !fixture.predecessor.manager.mu.TryLock() {
		t.Fatal("gateway lock remained held")
	}
	fixture.predecessor.manager.mu.Unlock()
	release, err := deploymenteffects.Acquire(context.Background(), fixture.predecessor.manager.options.WorkingDirectory)
	if err != nil {
		t.Fatalf("deployment-effects lease remained held: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRebindEffectBoundaryBindsZeroOneAndTwoProgressRecords(t *testing.T) {
	digests := make(map[string]struct{}, 3)
	for count := 0; count <= 2; count++ {
		t.Run(fmt.Sprintf("count-%d", count), func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			records := installGatewayRebindEffectBoundaryProgress(t, fixture, count)
			beforeCommands := len(fixture.predecessor.runner.commands)
			inspect := func(context.Context, routeState, gatewayV2RouteState,
				gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				return gatewayRebindFixtureDockerObservation(t, fixture.predecessor), nil
			}
			first, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
				context.Background(), fixture.predecessor.repository, fixture.reads, inspect, nil)
			if err != nil || !validGatewayRebindEffectBoundaryEvidence(first) ||
				first.ProgressCount != uint64(count) || !validSHA256(first.ProgressDigest) {
				t.Fatalf("count=%d evidence=%#v error=%v", count, first, err)
			}
			for _, record := range records {
				store, storeErr := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
					record.Generation, record.OperationID, record.Sequence)
				if storeErr != nil {
					t.Fatal(storeErr)
				}
				if replayErr := store.installExact(context.Background(), record); replayErr != nil {
					t.Fatalf("exact progress replay: %v", replayErr)
				}
			}
			second, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
				context.Background(), fixture.predecessor.repository, fixture.reads, inspect, nil)
			if err != nil || second != first {
				t.Fatalf("exact replay changed evidence: first=%#v second=%#v error=%v", first, second, err)
			}
			if len(fixture.predecessor.runner.commands) != beforeCommands {
				t.Fatalf("progress-bound attestation executed Docker mutator commands: %d",
					len(fixture.predecessor.runner.commands)-beforeCommands)
			}
			if _, duplicate := digests[first.ProgressDigest]; duplicate {
				t.Fatalf("progress digest collided across record counts: %s", first.ProgressDigest)
			}
			digests[first.ProgressDigest] = struct{}{}
		})
	}
}

func TestGatewayRebindEffectBoundaryAcceptsStoppedOwnedPredecessor(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	inspect := func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
		observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
		makeGatewayRebindPreparedStoppedObservation(&observation)
		return observation, nil
	}
	evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, fixture.reads, inspect, nil)
	if err != nil || !validGatewayRebindEffectBoundaryEvidence(evidence) {
		t.Fatalf("stopped predecessor evidence=%#v error=%v", evidence, err)
	}
}

func TestGatewayRebindEffectBoundaryAttestsExactTwoAppRoster(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryTwoAppFixture(t)
	evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, fixture.reads, fixture.inspect, nil)
	if err != nil || !validGatewayRebindEffectBoundaryEvidence(evidence) || len(fixture.intent.Intent.Roster) != 2 {
		t.Fatalf("two-app evidence=%#v roster=%d error=%v", evidence, len(fixture.intent.Intent.Roster), err)
	}
}

func newGatewayRebindEffectBoundaryTwoAppFixture(t *testing.T) gatewayRebindEffectBoundaryFixture {
	t.Helper()
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	ctx := context.Background()
	preclaim, err := fixture.repository.GatewayRebindPreclaimSnapshot(ctx, fixture.proposal)
	if err != nil {
		t.Fatal(err)
	}
	profile := preclaim.CurrentProfile
	appID := uuid.NewString()
	if _, err := fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, appID, "rebind-"+appID, "Second rebind app"); err != nil {
		t.Fatal(err)
	}
	allocation, _, err := fixture.repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), GatewayProfileRevisionID: profile.ID,
		GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := fixture.repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{
		AppID: appID, OperationID: allocation.OwnerOperationID, AllocationID: allocation.ID,
		Approval: appaccess.Approval{
			Action: appaccess.ActionEnableAppAccess, SpecDigest: accessDigest, ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := fixture.repository.ClaimAppAccessGrant(ctx, appaccess.ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, profile),
		ActorID: gatewayRebindTestAdministrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := appaccess.AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := fixture.repository.AdvanceAppAccessGrantClaim(ctx, owner,
		appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.repository.AdvanceAppAccessGrantClaim(ctx, owner,
		appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	grant, _, err = fixture.repository.ResolveAppAccessGrantClaim(ctx, owner,
		appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted, appaccess.AppAccessGrantProof{
			GatewayOperationID: fixture.journal.OperationID, ProtectedStateDigest: strings.Repeat("b", 64),
		})
	if err != nil {
		t.Fatal(err)
	}
	active := seedGatewayRebindPredecessorRuntime(t, fixture.db, appID)
	binding, err := gatewayV2LANBindingForRequest(gatewayV2LANGrantRequest{
		AttemptID: grant.AttemptID, ClaimRequestDigest: grant.RequestDigest, AppID: appID,
		AllocationID: revision.Allocation.ID, OwnerOperationID: revision.OperationID, Port: revision.Allocation.Port,
		AccessRevisionID: revision.ID, AccessRevisionNumber: revision.RevisionNumber,
		AccessSpecDigest: revision.SpecDigest, ApprovedBy: grant.Spec.ApprovedBy,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
		GatewayProfileSpecDigest: profile.SpecDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := cloneGatewayV2RouteState(fixture.state)
	secondRoute := cloneGatewayV2AppRoute(state.Apps[fixture.proposal.Roster[0].AppID])
	secondRoute.Route.Endpoints = []generatedruntime.RouteEndpoint{
		endpoint("api", "server", "rebind-second-network", "rebind-second-blue", 3000, '4'),
	}
	secondRoute.LAN = &binding
	state.Apps[appID] = secondRoute
	if err := fixture.store.writeExact(fixture.store.v2Path, fixture.store.v2Purpose,
		state, false, maxV2RouteStateBytes); err != nil {
		t.Fatal(err)
	}
	fixture.state = state

	claim := appaccess.GatewayRebindClaim{
		Spec: fixture.proposal.Spec, RebindApproval: fixture.proposal.RebindApproval,
		ConfigureApproval: fixture.proposal.ConfigureApproval,
		State:             appaccess.GatewayRebindPrepared, StateSequence: 1,
	}
	secondEntry := appaccess.GatewayRebindRosterEntry{
		OperationID: claim.Spec.OperationID, Ordinal: 2, AppID: appID,
		AllocationID: revision.Allocation.ID, Port: revision.Allocation.Port,
		AllocationOwnerOperationID: revision.OperationID, AllocationState: appaccess.AllocationActive,
		AccessRevisionID: revision.ID, AccessRevisionNumber: revision.RevisionNumber,
		AccessSpecDigest: revision.SpecDigest, GrantAttemptID: grant.AttemptID,
		GrantStateSequence: grant.StateSequence, GrantProtectedStateDigest: grant.Proof.ProtectedStateDigest,
		ServingDeploymentID: active.DeploymentID, ServingReleaseID: active.ReleaseID,
		ServingSlot: active.Slot, RouteGeneration: active.Generation,
	}
	secondEntry.EntryDigest, err = appaccess.GatewayRebindRosterEntryDigest(secondEntry)
	if err != nil {
		t.Fatal(err)
	}
	roster := append([]appaccess.GatewayRebindRosterEntry(nil), fixture.proposal.Roster...)
	roster = append(roster, secondEntry)
	claim.Spec.RosterCount = int64(len(roster))
	claim.Spec.RosterDigest, err = appaccess.GatewayRebindRosterDigest(roster)
	if err != nil {
		t.Fatal(err)
	}
	claim.RebindApproval.SpecDigest, err = appaccess.GatewayRebindSpecDigest(claim.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim.SuccessorProfileRequestDigest = gatewayRebindTestDigest(t, struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{profile.RevisionNumber, claim.Spec.SuccessorProfile, claim.ConfigureApproval})
	claim.RequestDigest = gatewayRebindTestDigest(t, struct {
		Spec      appaccess.GatewayRebindSpec `json:"spec"`
		Rebind    appaccess.Approval          `json:"rebindApproval"`
		Configure appaccess.Approval          `json:"configureApproval"`
	}{claim.Spec, claim.RebindApproval, claim.ConfigureApproval})
	claim.RebindApprovedAt = time.Now().UTC()
	claim.ConfigureApprovedAt = claim.RebindApprovedAt
	claim.CreatedAt = claim.RebindApprovedAt
	claim.UpdatedAt = claim.RebindApprovedAt
	insertGatewayRebindPredecessorClaim(t, fixture.db, claim)
	for _, entry := range roster {
		insertGatewayRebindPredecessorRoster(t, fixture.db, entry)
	}

	snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(ctx)
	if err != nil || len(snapshot.Claims) != 1 || len(snapshot.Claims[0].Roster) != 2 {
		t.Fatalf("two-app startup snapshot claims=%d roster=%d error=%v", len(snapshot.Claims), len(snapshot.Claims[0].Roster), err)
	}
	reads, _ := gatewayRebindSuccessorTestReads(t, fixture)
	observation, err := readGatewayRebindSuccessorPreflightObservation(ctx, fixture.repository, reads)
	if err != nil {
		t.Fatal(err)
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: fixture.store, Generation: fixture.store.generation, State: fixture.state,
		Journal: fixture.journal, Existing: true, operationID: fixture.journal.OperationID,
	}
	selection, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(claim.Spec.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, selection.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.Store.installExact(intent); err != nil {
		t.Fatal(err)
	}
	dockerObservation := gatewayRebindFixtureDockerObservation(t, fixture)
	inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
		return dockerObservation, nil
	}
	return gatewayRebindEffectBoundaryFixture{
		predecessor: fixture, intent: intent, reads: reads, inspect: inspect,
	}
}

func TestGatewayRebindEffectBoundaryRejectsProtectedSQLiteAndExternalDrift(t *testing.T) {
	t.Run("forged installed database digest", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		forged := fixture.intent
		forged.DatabaseDigest = strings.Repeat("f", 64)
		var err error
		forged.Digest, err = gatewayRebindProtectedIntentDigest(forged)
		if err != nil || !validGatewayRebindProtectedIntent(forged) {
			t.Fatalf("forged intent invalid before attestation: %v", err)
		}
		store, err := newGatewayRebindProtectedIntentStore(fixture.predecessor.manager.options.DataRoot,
			forged.Generation, forged.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		state := gatewayUpgradeStateStore{directory: store.directory}
		if err := state.writeExact(store.path, store.purpose, forged, false, maxGatewayRebindProtectedIntentBytes); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})

	t.Run("stale SQLite claim", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})

	t.Run("stale SQLite event ledger", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_event_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claim_events
			SET created_at='2000-01-01T00:00:00Z'`); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})

	t.Run("stale committed grant", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		for _, trigger := range []string{
			"lan_gateway_rebind_fence_grant_update", "lan_app_access_grant_state_transition",
			"lan_app_access_grant_transition_event",
		} {
			if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_app_access_grant_claims
			SET protected_state_digest=? WHERE attempt_id=?`, strings.Repeat("9", 64),
			fixture.predecessor.proposal.Roster[0].GrantAttemptID); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})

	t.Run("stale second-app committed grant", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryTwoAppFixture(t)
		if len(fixture.intent.Intent.Roster) != 2 {
			t.Fatal("two-app fixture has no second grant")
		}
		for _, trigger := range []string{
			"lan_gateway_rebind_fence_grant_update", "lan_app_access_grant_state_transition",
			"lan_app_access_grant_transition_event",
		} {
			if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_app_access_grant_claims
			SET protected_state_digest=? WHERE attempt_id=?`, strings.Repeat("9", 64),
			fixture.intent.Intent.Roster[1].GrantAttemptID); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})

	t.Run("stale active runtime head", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		for _, trigger := range []string{
			"lan_gateway_rebind_fence_runtime_head_update", "generated_runtime_active_head_valid_update",
		} {
			if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE generated_runtime_active_heads
			SET generation=generation+1 WHERE app_id=?`, fixture.predecessor.proposal.Roster[0].AppID); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})

	for _, test := range []struct {
		name   string
		mutate func(*gatewayV2DockerObservation)
	}{
		{"wrong Docker ID", func(value *gatewayV2DockerObservation) {
			value.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
		}},
		{"wrong Docker labels", func(value *gatewayV2DockerObservation) {
			value.FinalContainer.Labels["io.rig.unexpected"] = "drift"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
				observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
				test.mutate(&observation)
				return observation, nil
			}
			assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, inspect, nil)
		})
	}

	t.Run("valid Docker state changes between observations", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		calls := 0
		checkpointReached := false
		inspect := func(_ context.Context, source routeState, state gatewayV2RouteState,
			journal gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			calls++
			observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
			if calls == 2 {
				makeGatewayRebindPreparedStoppedObservation(&observation)
			}
			if !validGatewayRebindPredecessorDocker(source, state, journal, observation) {
				t.Fatalf("Docker observation %d is not individually valid", calls)
			}
			return observation, nil
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, inspect,
			func() { checkpointReached = true })
		if !checkpointReached || calls != 2 {
			t.Fatalf("Docker drift was not observed across both reads: checkpoint=%t Docker reads=%d",
				checkpointReached, calls)
		}
	})

	t.Run("successor network changes between observations", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		reads := fixture.reads
		calls := 0
		checkpointReached := false
		originalHost := reads.network.host
		reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
			calls++
			host, err := originalHost()
			if err != nil {
				return gatewayV2HostNetworkSnapshot{}, err
			}
			if calls == 1 {
				return host, nil
			}
			host.Routes = append(host.Routes, netip.MustParsePrefix("10.99.0.0/16"))
			return host, nil
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, reads, fixture.inspect,
			func() { checkpointReached = true })
		if !checkpointReached || calls != 2 {
			t.Fatalf("network drift was not observed across both reads: checkpoint=%t host reads=%d",
				checkpointReached, calls)
		}
	})

	t.Run("protected history changes during second Docker observation", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		calls := 0
		inspect := func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			calls++
			observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
			if calls == 2 {
				state := gatewayUpgradeStateStore{directory: fixture.predecessor.manager.store}
				mutated := cloneGatewayV2RouteState(fixture.predecessor.state)
				mutated.Identity.Digest = strings.Repeat("9", 64)
				if err := state.writeExact(fixture.predecessor.store.v2Path, fixture.predecessor.store.v2Purpose,
					mutated, false, maxV2RouteStateBytes); err != nil {
					t.Fatal(err)
				}
			}
			return observation, nil
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, inspect, nil)
	})

	t.Run("progress is appended between complete observations", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		checkpointCalls := 0
		checkpoint := func() {
			checkpointCalls++
			installGatewayRebindEffectBoundaryProgress(t, fixture, 1)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, checkpoint)
		if checkpointCalls != 1 {
			t.Fatalf("checkpoint calls=%d, want 1", checkpointCalls)
		}
	})

	t.Run("valid same-count progress content changes between observations", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		original := installGatewayRebindEffectBoundaryProgress(t, fixture, 1)[0]
		replacement, err := newGatewayRebindSuccessorIntentProgress(
			fixture.intent, gatewayRebindProgressTimestamp(3))
		if err != nil || replacement.Digest == original.Digest {
			t.Fatalf("replacement progress is not distinct: error=%v", err)
		}
		store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
			fixture.intent.Generation, fixture.intent.OperationID, original.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		checkpointCalls := 0
		checkpoint := func() {
			checkpointCalls++
			state := gatewayUpgradeStateStore{directory: store.directory}
			if err := state.writeExact(store.path, store.purpose, replacement, false,
				maxGatewayRebindProgressBytes); err != nil {
				t.Fatal(err)
			}
			history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.Progress) != 1 || history.Progress[0].Record != replacement {
				t.Fatalf("replacement is not a valid one-record history: history=%#v error=%v", history.Progress, err)
			}
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, checkpoint)
		if checkpointCalls != 1 {
			t.Fatalf("checkpoint calls=%d, want 1", checkpointCalls)
		}
	})

	t.Run("progress is appended after second anchor before final anchor", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		calls := 0
		originalInspect := fixture.inspect
		inspect := func(ctx context.Context, source routeState, state gatewayV2RouteState,
			journal gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
			calls++
			if calls == 2 {
				installGatewayRebindEffectBoundaryProgress(t, fixture, 1)
			}
			return originalInspect(ctx, source, state, journal)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, inspect, nil)
		if calls != 2 {
			t.Fatalf("Docker observations=%d, want 2", calls)
		}
	})

	t.Run("corrupt progress is rejected by strict history scan", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(fixture.intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
			fixture.intent.Generation, fixture.intent.OperationID, first.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		first.Digest = strings.Repeat("f", 64)
		state := gatewayUpgradeStateStore{directory: store.directory}
		if err := state.writeExact(store.path, store.purpose, first, true, maxGatewayRebindProgressBytes); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
	})
}

func TestGatewayRebindEffectBoundaryCancellationAndLockFailuresEraseEvidence(t *testing.T) {
	t.Run("cancelled between observations", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
			ctx, fixture.predecessor.repository, fixture.reads, fixture.inspect, cancel)
		if !IsCode(err, DiagnosticCancelled) || evidence != (gatewayRebindEffectBoundaryEvidence{}) {
			t.Fatalf("evidence=%#v error=%v", evidence, err)
		}
	})

	t.Run("lease acquisition fails before gateway lock", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		originalLease, originalGateway := gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock
		gatewayCalls := 0
		gatewayRebindAcquireDeploymentEffects = func(context.Context, string) (func() error, error) {
			return nil, errors.New("injected lease acquisition failure")
		}
		managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
			gatewayCalls++
			return func() error { return nil }, nil
		}
		t.Cleanup(func() {
			gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock = originalLease, originalGateway
		})
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
		if gatewayCalls != 0 {
			t.Fatalf("gateway lock called after lease acquisition failure: %d", gatewayCalls)
		}
	})

	t.Run("gateway lock acquisition fails and releases lease", func(t *testing.T) {
		fixture := newGatewayRebindEffectBoundaryFixture(t)
		originalLease, originalGateway := gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock
		leaseReleases := 0
		gatewayRebindAcquireDeploymentEffects = func(context.Context, string) (func() error, error) {
			return func() error { leaseReleases++; return nil }, nil
		}
		managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
			return nil, errors.New("injected gateway acquisition failure")
		}
		t.Cleanup(func() {
			gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock = originalLease, originalGateway
		})
		assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
		if leaseReleases != 1 {
			t.Fatalf("effects lease releases=%d, want 1", leaseReleases)
		}
		if !fixture.predecessor.manager.mu.TryLock() {
			t.Fatal("Manager lock remained held after gateway acquisition failure")
		}
		fixture.predecessor.manager.mu.Unlock()
	})

	for _, test := range []struct {
		name           string
		gatewayRelease bool
		effectsRelease bool
	}{
		{name: "gateway release", gatewayRelease: true},
		{name: "effects release", effectsRelease: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindEffectBoundaryFixture(t)
			installGatewayRebindEffectBoundaryProgress(t, fixture, 2)
			originalLease, originalGateway := gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock
			gatewayRebindAcquireDeploymentEffects = func(context.Context, string) (func() error, error) {
				return func() error {
					if test.effectsRelease {
						return errors.New("injected effects release failure")
					}
					return nil
				}, nil
			}
			managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
				return func() error {
					if test.gatewayRelease {
						return errors.New("injected gateway release failure")
					}
					return nil
				}, nil
			}
			t.Cleanup(func() {
				gatewayRebindAcquireDeploymentEffects, managerAcquireGatewayOSLock = originalLease, originalGateway
			})
			assertGatewayRebindEffectBoundaryRejected(t, fixture, fixture.reads, fixture.inspect, nil)
		})
	}
}

func TestGatewayRebindEffectBoundaryHoldsDeploymentEffectsLeaseAgainstAnotherProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess coverage is disabled in short mode")
	}
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	checkpoint := func() {
		command := exec.Command(os.Args[0], "-test.run=^TestGatewayRebindEffectBoundaryLeaseContentionHelper$")
		command.Env = append(os.Environ(), gatewayRebindEffectBoundaryLeaseHelperEnvironment+"="+
			fixture.predecessor.manager.options.WorkingDirectory)
		output, err := command.CombinedOutput()
		marker := false
		for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
			if line == "contended" {
				marker = true
				break
			}
		}
		if err != nil || !marker {
			t.Fatalf("cross-process contender error=%v output=%q", err, output)
		}
	}
	evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, fixture.reads, fixture.inspect, checkpoint)
	if err != nil || !validGatewayRebindEffectBoundaryEvidence(evidence) {
		t.Fatalf("evidence=%#v error=%v", evidence, err)
	}
}

func TestGatewayRebindEffectBoundaryLeaseContentionHelper(t *testing.T) {
	directory := os.Getenv(gatewayRebindEffectBoundaryLeaseHelperEnvironment)
	if directory == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	release, err := deploymenteffects.Acquire(ctx, directory)
	if release != nil {
		_ = release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		_, _ = fmt.Fprintf(os.Stderr, "unexpected contention result: %v\n", err)
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(os.Stdout, "contended"); err != nil {
		t.Fatal(err)
	}
}

func assertGatewayRebindEffectBoundaryRejected(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	reads gatewayRebindSuccessorPreflightReads, inspect gatewayRebindDockerInspector, checkpoint func(),
) {
	t.Helper()
	beforeCommands := len(fixture.predecessor.runner.commands)
	evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, reads, inspect, checkpoint)
	if err == nil || evidence != (gatewayRebindEffectBoundaryEvidence{}) {
		t.Fatalf("evidence=%#v error=%v", evidence, err)
	}
	if len(fixture.predecessor.runner.commands) != beforeCommands {
		t.Fatalf("rejected attestation executed routed-request or Docker mutator commands: %d",
			len(fixture.predecessor.runner.commands)-beforeCommands)
	}
}

func TestGatewayRebindEffectBoundaryEvidenceDigestRejectsMutation(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	installGatewayRebindEffectBoundaryProgress(t, fixture, 1)
	evidence, err := fixture.predecessor.manager.attestGatewayRebindPreparedEffectBoundary(
		context.Background(), fixture.predecessor.repository, fixture.reads, fixture.inspect, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindEffectBoundaryEvidence)
	}{
		{"operation ID", func(value *gatewayRebindEffectBoundaryEvidence) {
			value.OperationID = "11111111-1111-4111-8111-111111111111"
		}},
		{"progress count", func(value *gatewayRebindEffectBoundaryEvidence) {
			value.ProgressCount = 2
		}},
		{"progress digest", func(value *gatewayRebindEffectBoundaryEvidence) {
			value.ProgressDigest = strings.Repeat("9", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := evidence
			test.mutate(&mutated)
			if reflect.DeepEqual(mutated, evidence) || validGatewayRebindEffectBoundaryEvidence(mutated) {
				t.Fatal("mutated effect-boundary evidence was accepted")
			}
		})
	}
}
