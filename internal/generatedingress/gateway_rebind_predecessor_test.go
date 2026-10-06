package generatedingress

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const gatewayRebindTestAdministrator = "91919191-9191-4191-8191-919191919191"

type gatewayRebindPredecessorFixture struct {
	manager    *Manager
	store      *gatewayUpgradeStateStore
	repository *appaccess.Repository
	db         *sql.DB
	runner     *ingressRunner
	state      gatewayV2RouteState
	journal    gatewayMigrationJournal
	proposal   appaccess.GatewayRebindPreclaimProposal
}

func TestInspectGatewayRebindPredecessorAcceptsExactReadOnlyPair(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixture(t)
	beforeDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(fixture.runner.commands)
	fixture.manager.options.RebindFenceCheck = func(context.Context) error {
		return appaccess.ErrGatewayRebindActive
	}

	if err := fixture.manager.InspectGatewayRebindPredecessor(context.Background(), fixture.repository); err != nil {
		t.Fatalf("inspection failed: %v", err)
	}
	afterDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	afterState, afterJournal, err := fixture.store.loadBoundUpgrade(fixture.journal.OperationID)
	if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) ||
		!reflect.DeepEqual(fixture.state, afterState) || !reflect.DeepEqual(fixture.journal, afterJournal) ||
		len(fixture.runner.commands) != beforeCommands {
		t.Fatalf("inspection mutated state: db=%t state=%t journal=%t commands=%d err=%v",
			reflect.DeepEqual(beforeDatabase, afterDatabase), reflect.DeepEqual(fixture.state, afterState),
			reflect.DeepEqual(fixture.journal, afterJournal), len(fixture.runner.commands)-beforeCommands, err)
	}
	afterArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
	if err != nil || !sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) {
		t.Fatalf("inspection changed protected artifacts: %v", err)
	}
}

func TestGatewayRebindPredecessorRejectsExactBindingMismatches(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixture(t)
	snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !gatewayRebindPredecessorMatches(snapshot, fixture.state, fixture.journal) {
		t.Fatalf("baseline mismatch: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*appaccess.GatewayRebindStartupSnapshot, *gatewayV2RouteState, *gatewayMigrationJournal)
	}{
		{"missing claim", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims = nil
		}},
		{"extra claim", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims = append(snapshot.Claims, snapshot.Claims[0])
		}},
		{"protected identity", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].Claim.Spec.PredecessorProtectedIdentityDigest = strings.Repeat("9", 64)
		}},
		{"profile identity", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].PredecessorProfile.ID = uuid.NewString()
		}},
		{"profile network", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].PredecessorProfile.Spec.InterfaceID = "replacement-interface"
		}},
		{"upgrade approver", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].PredecessorUpgrade.ApprovedBy = uuid.NewString()
		}},
		{"grant request projection", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].GrantBindings[0].RequestDigest = strings.Repeat("8", 64)
		}},
		{"grant operation projection", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].GrantBindings[0].GatewayOperationID = uuid.NewString()
		}},
		{"lan binding", func(_ *appaccess.GatewayRebindStartupSnapshot, state *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			app := state.Apps[fixture.stateAppID()]
			app.LAN.GrantRequestDigest = strings.Repeat("7", 64)
			state.Apps[fixture.stateAppID()] = app
		}},
		{"route slot", func(_ *appaccess.GatewayRebindStartupSnapshot, state *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			app := state.Apps[fixture.stateAppID()]
			app.Route.Slot = generatedruntime.SlotGreen
			state.Apps[fixture.stateAppID()] = app
		}},
		{"missing roster", func(snapshot *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			snapshot.Claims[0].Roster = nil
			snapshot.Claims[0].GrantBindings = nil
		}},
		{"extra protected binding", func(_ *appaccess.GatewayRebindStartupSnapshot, state *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			extra := cloneGatewayV2AppRoute(state.Apps[fixture.stateAppID()])
			extra.LAN.AllocationID = uuid.NewString()
			extra.LAN.GrantAttemptID = uuid.NewString()
			extra.LAN.AccessRevisionID = uuid.NewString()
			state.Apps[uuid.NewString()] = extra
		}},
		{"pending", func(_ *appaccess.GatewayRebindStartupSnapshot, state *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			state.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingRouteSwitch}
		}},
		{"lan recovery", func(_ *appaccess.GatewayRebindStartupSnapshot, state *gatewayV2RouteState, _ *gatewayMigrationJournal) {
			state.LANRecovery = &gatewayV2LANRecoveryBatch{}
		}},
		{"journal not committed", func(_ *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, journal *gatewayMigrationJournal) {
			journal.Phase = gatewayPhaseV2Serving
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateSnapshot := cloneGatewayRebindStartupSnapshot(snapshot)
			candidateState := cloneGatewayV2RouteState(fixture.state)
			candidateJournal := fixture.journal
			test.mutate(&candidateSnapshot, &candidateState, &candidateJournal)
			if gatewayRebindPredecessorMatches(candidateSnapshot, candidateState, candidateJournal) {
				t.Fatal("mismatch was accepted")
			}
		})
	}
}

func TestInspectGatewayRebindPredecessorRejectsCorruptReplacedAndDriftingInputs(t *testing.T) {
	t.Run("corrupt history", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		if err := os.WriteFile(fixture.store.v2Path, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.manager.InspectGatewayRebindPredecessor(context.Background(), fixture.repository); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("corrupt history error = %v", err)
		}
	})

	t.Run("replaced history fingerprint", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
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
		if err := fixture.manager.inspectGatewayRebindPredecessor(context.Background(), fixture.repository, checkpoint); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("replaced history error = %v", err)
		}
	})

	t.Run("sqlite drift", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		checkpoint := func() {
			if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, gatewayRebindTestAdministrator); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.manager.inspectGatewayRebindPredecessor(context.Background(), fixture.repository, checkpoint); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("SQLite drift error = %v", err)
		}
	})

	t.Run("cancellation after first read", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		checkpoint := func() {
			cancel()
		}
		if err := fixture.manager.inspectGatewayRebindPredecessor(ctx, fixture.repository, checkpoint); !IsCode(err, DiagnosticCancelled) {
			t.Fatalf("cancellation error = %v", err)
		}
	})
}

func TestInspectGatewayRebindPredecessorLocksBeforeReadAndFailsClosedOnRelease(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixture(t)
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
	if err := fixture.manager.InspectGatewayRebindPredecessor(context.Background(), fixture.repository); !IsCode(err, DiagnosticRouteUnresolved) || !acquired || !released {
		t.Fatalf("release failure error=%v acquired=%t released=%t", err, acquired, released)
	}
	if !fixture.manager.mu.TryLock() {
		t.Fatal("Manager lock remained held after release failure")
	}
	fixture.manager.mu.Unlock()
}

func (fixture gatewayRebindPredecessorFixture) stateAppID() string {
	for appID, app := range fixture.state.Apps {
		if app.LAN != nil {
			return appID
		}
	}
	return ""
}

func cloneGatewayRebindStartupSnapshot(value appaccess.GatewayRebindStartupSnapshot) appaccess.GatewayRebindStartupSnapshot {
	result := value
	if value.CurrentProfile != nil {
		profile := *value.CurrentProfile
		result.CurrentProfile = &profile
	}
	result.Claims = append([]appaccess.GatewayRebindStartupClaim(nil), value.Claims...)
	for index := range result.Claims {
		result.Claims[index].Roster = append([]appaccess.GatewayRebindRosterEntry(nil), value.Claims[index].Roster...)
		result.Claims[index].GrantBindings = append([]appaccess.GatewayRebindStartupGrantBinding(nil), value.Claims[index].GrantBindings...)
	}
	return result
}

func newGatewayRebindPredecessorFixture(t *testing.T) gatewayRebindPredecessorFixture {
	return newGatewayRebindPredecessorFixtureWithClaim(t, true)
}

func newGatewayRebindPredecessorFixtureWithClaim(t *testing.T, insertClaim bool) gatewayRebindPredecessorFixture {
	return newGatewayRebindPredecessorFixtureWithEndpoint(t, insertClaim, '4')
}

func newGatewayRebindPredecessorFixtureWithEndpoint(t *testing.T, insertClaim bool, endpointID rune) gatewayRebindPredecessorFixture {
	t.Helper()
	manager, runner := newManagerFixture(t, false)
	db, err := database.Open(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'rebind-admin','hash','administrator',datetime('now'),datetime('now'))`, gatewayRebindTestAdministrator); err != nil {
		t.Fatal(err)
	}
	repository := appaccess.New(db)
	ctx := context.Background()
	profileSpec := appaccess.GatewayProfileSpec{
		SelectedIPv4: "192.168.96.8", InterfaceID: "rebind-predecessor", PortStart: 8100, PortEnd: 8119,
	}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := repository.ConfigureGatewayProfile(ctx, appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), Spec: profileSpec,
		Approval: appaccess.Approval{
			Action: appaccess.ActionConfigureGateway, SpecDigest: profileDigest, ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	appID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, appID, "rebind-"+appID, "Rebind app"); err != nil {
		t.Fatal(err)
	}
	allocation, _, err := repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
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
	revision, _, err := repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{
		AppID: appID, OperationID: allocation.OwnerOperationID, AllocationID: allocation.ID,
		Approval: appaccess.Approval{
			Action: appaccess.ActionEnableAppAccess, SpecDigest: accessDigest, ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	upgradeOperationID := uuid.NewString()
	grant, _, err := repository.ClaimAppAccessGrant(ctx, appaccess.ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, profile),
		ActorID: gatewayRebindTestAdministrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantOwner := appaccess.AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, grantOwner,
		appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, grantOwner,
		appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	grant, _, err = repository.ResolveAppAccessGrantClaim(ctx, grantOwner,
		appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted, appaccess.AppAccessGrantProof{
			GatewayOperationID: upgradeOperationID, ProtectedStateDigest: strings.Repeat("a", 64),
		})
	if err != nil {
		t.Fatal(err)
	}
	active := seedGatewayRebindPredecessorRuntime(t, db, appID)

	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
	}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(ctx, appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: upgradeOperationID, Spec: upgradeSpec,
		Approval: appaccess.Approval{
			Action: appaccess.ActionUpgradeGateway, SpecDigest: upgradeDigest, ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	upgradeOwner := appaccess.GatewayProfileUpgradeClaimOwner{
		OperationID: upgrade.OperationID, ProfileRevisionID: upgrade.ProfileRevisionID,
		ProfileRevisionNumber: upgrade.ProfileRevisionNumber,
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, upgradeOwner,
		appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeServing); err != nil {
		t.Fatal(err)
	}
	upgrade, _, err = repository.AdvanceGatewayProfileUpgradeClaim(ctx, upgradeOwner,
		appaccess.GatewayProfileUpgradeServing, appaccess.GatewayProfileUpgradeCommitted)
	if err != nil {
		t.Fatal(err)
	}

	source := routeState{Version: stateVersion, Active: map[string]routeRecord{
		appID: {
			Slot: generatedruntime.Slot(active.Slot),
			Endpoints: []generatedruntime.RouteEndpoint{
				endpoint("api", "server", "rebind-app-network", "rebind-app-blue", 3000, endpointID),
			},
		},
	}}
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	profileBinding := gatewayProfileBinding{
		RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
		SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
		PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd,
	}
	sourceIdentityDigest, err := gatewayV1ObservedIdentityDigest(gatewayV2DockerObservation{
		Image:       gatewayV2IdentityTestImage(),
		V1Container: caddyInspection{ID: "sha256:" + strings.Repeat("b", 64)},
		V1NetworkID: "sha256:" + strings.Repeat("e", 64),
		V1VolumeIdentity: gatewayV1VolumeIdentity{
			Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data",
			CreatedAt:  "2026-09-29T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	preparation := gatewayUpgradePreparation{
		OperationID: upgrade.OperationID, Profile: profileBinding,
		Network:              gatewayV2NetworkPlan{Subnet: "10.241.0.0/28", GatewayIPv4: "10.241.0.1", ContainerIPv4: "10.241.0.2"},
		SourceIdentityDigest: sourceIdentityDigest, LocalHostPort: manager.options.HostPort,
		ApprovedBy: upgrade.ApprovedBy, ApprovedActionDigest: upgradeDigest,
	}
	state, journal, err := prepareGatewayV2State(source, preparation)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	phase := gatewayPhasePrepared
	for _, next := range []gatewayMigrationPhase{
		gatewayPhaseStageIntent, gatewayPhaseStaged, gatewayPhaseTransferIntent,
		gatewayPhaseV2Serving, gatewayPhaseCommitted,
	} {
		if next == gatewayPhaseStaged {
			bindUpgradeStageResources(t, store, upgrade.OperationID)
		}
		if next == gatewayPhaseV2Serving {
			bindUpgradeFinalResource(t, store, upgrade.OperationID)
		}
		journal, err = store.transitionMigrationJournal(upgrade.OperationID, phase, next)
		if err != nil {
			t.Fatal(err)
		}
		phase = next
	}
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
	previous := cloneGatewayV2AppRoute(state.Apps[appID])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = &binding
	pending := cloneGatewayV2RouteState(state)
	pending.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: appID, Previous: &previous, Proposed: proposed,
	}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}
	state = cloneGatewayV2RouteState(pending)
	state.Pending = nil
	state.Apps[appID] = proposed
	if err := store.saveCommittedV2State(state, journal); err != nil {
		t.Fatal(err)
	}

	rebindOperationID := uuid.NewString()
	entry := appaccess.GatewayRebindRosterEntry{
		OperationID: rebindOperationID, Ordinal: 1, AppID: appID,
		AllocationID: revision.Allocation.ID, Port: revision.Allocation.Port,
		AllocationOwnerOperationID: revision.OperationID, AllocationState: appaccess.AllocationActive,
		AccessRevisionID: revision.ID, AccessRevisionNumber: revision.RevisionNumber,
		AccessSpecDigest: revision.SpecDigest, GrantAttemptID: grant.AttemptID,
		GrantStateSequence: grant.StateSequence, GrantProtectedStateDigest: grant.Proof.ProtectedStateDigest,
		ServingDeploymentID: active.DeploymentID, ServingReleaseID: active.ReleaseID,
		ServingSlot: active.Slot, RouteGeneration: active.Generation,
	}
	entry.EntryDigest, err = appaccess.GatewayRebindRosterEntryDigest(entry)
	if err != nil {
		t.Fatal(err)
	}
	rosterDigest, err := appaccess.GatewayRebindRosterDigest([]appaccess.GatewayRebindRosterEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	successor := appaccess.GatewayProfileSpec{
		SelectedIPv4: "192.168.97.8", InterfaceID: "rebind-successor", PortStart: 8100, PortEnd: 8119,
	}
	rebindSpec := appaccess.GatewayRebindSpec{
		OperationID:                  rebindOperationID,
		PredecessorProfileRevisionID: profile.ID, PredecessorProfileRevisionNumber: profile.RevisionNumber,
		PredecessorProfileSpecDigest: profile.SpecDigest, PredecessorUpgradeOperationID: upgrade.OperationID,
		PredecessorProtectedIdentityDigest: state.Identity.Digest,
		SuccessorProfileRevisionID:         uuid.NewString(), SuccessorProfileRevisionNumber: profile.RevisionNumber + 1,
		SuccessorProfileOperationID: uuid.NewString(), SuccessorProfile: successor,
		RosterDigest: rosterDigest, RosterCount: 1,
	}
	successorDigest, err := appaccess.GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	configureApproval := appaccess.Approval{
		Action: appaccess.ActionConfigureGateway, SpecDigest: successorDigest, ActorID: gatewayRebindTestAdministrator,
	}
	rebindDigest, err := appaccess.GatewayRebindSpecDigest(rebindSpec)
	if err != nil {
		t.Fatal(err)
	}
	rebindApproval := appaccess.Approval{
		Action: appaccess.ActionRebindGateway, SpecDigest: rebindDigest, ActorID: gatewayRebindTestAdministrator,
	}
	claimTime := time.Now().UTC()
	claim := appaccess.GatewayRebindClaim{
		Spec: rebindSpec, RebindApproval: rebindApproval, RebindApprovedAt: claimTime,
		ConfigureApproval: configureApproval, ConfigureApprovedAt: claimTime,
		State: appaccess.GatewayRebindPrepared, StateSequence: 1, CreatedAt: claimTime, UpdatedAt: claimTime,
	}
	claim.SuccessorProfileRequestDigest = gatewayRebindTestDigest(t, struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{profile.RevisionNumber, successor, configureApproval})
	claim.RequestDigest = gatewayRebindTestDigest(t, struct {
		Spec      appaccess.GatewayRebindSpec `json:"spec"`
		Rebind    appaccess.Approval          `json:"rebindApproval"`
		Configure appaccess.Approval          `json:"configureApproval"`
	}{rebindSpec, rebindApproval, configureApproval})
	if insertClaim {
		insertGatewayRebindPredecessorClaim(t, db, claim)
		insertGatewayRebindPredecessorRoster(t, db, entry)
	}

	loadedState, loadedJournal, err := store.loadBoundUpgrade(upgrade.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayRebindPredecessorFixture{
		manager: manager, store: store, repository: repository, db: db, runner: runner,
		state: loadedState, journal: loadedJournal,
		proposal: appaccess.GatewayRebindPreclaimProposal{
			Spec: rebindSpec, RebindApproval: rebindApproval,
			ConfigureApproval: configureApproval, Roster: []appaccess.GatewayRebindRosterEntry{entry},
		},
	}
}

func gatewayRebindTestDigest(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func seedGatewayRebindPredecessorRuntime(t *testing.T, db *sql.DB, appID string) generatedruntimestate.ActiveHead {
	t.Helper()
	planID, releaseID, deploymentID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
		planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret",
		strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64),
		gatewayRebindTestAdministrator, stamp, gatewayRebindTestAdministrator, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, appID, stamp,
		strings.Repeat("d", 64), strings.Repeat("f", 64), planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,?,?)`, jobID, appID,
		gatewayRebindTestAdministrator, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, appID, releaseID, jobID, planID); err != nil {
		t.Fatal(err)
	}
	runtime := generatedruntimestate.New(db)
	value, _, err := runtime.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,
		created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, "api",
		strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64), stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetImageReady(context.Background(), appID, deploymentID, "api", artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerStarting(context.Background(), appID, deploymentID, "api", "rig-rebind-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerRunning(context.Background(), appID, deploymentID, "api", strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AdvanceComponent(context.Background(), appID, deploymentID, "api",
		generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(context.Background(), appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtime.SwitchActive(context.Background(), appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("switch runtime head=%#v changed=%t error=%v", head, changed, err)
	}
	return head
}

func insertGatewayRebindPredecessorClaim(t *testing.T, db *sql.DB, claim appaccess.GatewayRebindClaim) {
	t.Helper()
	stamp := func(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
	_, err := db.Exec(`INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,successor_profile_revision_id,
		successor_profile_revision_number,successor_profile_operation_id,
		successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		successor_port_start,successor_port_end,successor_profile_spec_digest,
		configure_approval_action,configure_approved_by,configure_approved_at,
		roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		claim.Spec.OperationID, claim.RequestDigest, claim.RebindApproval.Action,
		claim.RebindApproval.SpecDigest, claim.RebindApproval.ActorID, stamp(claim.RebindApprovedAt),
		claim.Spec.PredecessorProfileRevisionID, claim.Spec.PredecessorProfileRevisionNumber,
		claim.Spec.PredecessorProfileSpecDigest, claim.Spec.PredecessorUpgradeOperationID,
		claim.Spec.PredecessorProtectedIdentityDigest, claim.Spec.SuccessorProfileRevisionID,
		claim.Spec.SuccessorProfileRevisionNumber, claim.Spec.SuccessorProfileOperationID,
		claim.SuccessorProfileRequestDigest, claim.Spec.SuccessorProfile.SelectedIPv4,
		claim.Spec.SuccessorProfile.InterfaceID, claim.Spec.SuccessorProfile.PortStart,
		claim.Spec.SuccessorProfile.PortEnd, claim.ConfigureApproval.SpecDigest,
		claim.ConfigureApproval.Action, claim.ConfigureApproval.ActorID, stamp(claim.ConfigureApprovedAt),
		claim.Spec.RosterDigest, claim.Spec.RosterCount, claim.State, claim.StateSequence,
		stamp(claim.CreatedAt), stamp(claim.UpdatedAt))
	if err != nil {
		t.Fatal(err)
	}
}

func insertGatewayRebindPredecessorRoster(t *testing.T, db *sql.DB, entry appaccess.GatewayRebindRosterEntry) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO lan_gateway_rebind_roster_entries(
		operation_id,ordinal,app_id,allocation_id,allocated_port,allocation_owner_operation_id,
		allocation_state,access_revision_id,access_revision_number,access_spec_digest,
		grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		serving_deployment_id,serving_release_id,serving_slot,route_generation,entry_digest
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, entry.OperationID, entry.Ordinal, entry.AppID,
		entry.AllocationID, entry.Port, entry.AllocationOwnerOperationID, entry.AllocationState,
		entry.AccessRevisionID, entry.AccessRevisionNumber, entry.AccessSpecDigest,
		entry.GrantAttemptID, entry.GrantStateSequence, entry.GrantProtectedStateDigest,
		entry.ServingDeploymentID, entry.ServingReleaseID, entry.ServingSlot,
		entry.RouteGeneration, entry.EntryDigest)
	if err != nil {
		t.Fatal(err)
	}
}
