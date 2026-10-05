package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func gatewayRebindProtectedIntentFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	appaccess.GatewayRebindStartupSnapshot, gatewayUpgradeGenerationSelection,
	gatewayRebindSuccessorPreflightObservation,
) {
	t.Helper()
	fixture := newGatewayRebindPredecessorFixture(t)
	snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claim := snapshot.Claims[0].Claim
	profile := GatewayRebindSuccessorProfile{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
		SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
	}
	address := netip.MustParseAddr(profile.SelectedIPv4)
	candidates := []hostNetworkCandidate{{
		InterfaceID: profile.InterfaceID, IPv4: profile.SelectedIPv4,
		Prefix: netip.PrefixFrom(address, 24).Masked(),
	}}
	host := gatewayV2HostNetworkSnapshot{Routes: []netip.Prefix{}, Interfaces: []netip.Prefix{}}
	docker := []netip.Prefix{netip.MustParsePrefix("172.28.0.0/16")}
	plan, err := selectGatewayV2NetworkPlan(context.Background(), gatewayProfileBinding{
		RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
		SpecDigest: profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4,
		InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
	}, gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return candidates, nil },
		host:       func() (gatewayV2HostNetworkSnapshot, error) { return host, nil },
		docker:     func(context.Context) ([]netip.Prefix, error) { return docker, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	preflight := GatewayRebindSuccessorPreflight{
		RebindOperationID: claim.Spec.OperationID, ClaimRequestDigest: claim.RequestDigest, Profile: profile,
		Network: GatewayRebindSuccessorNetworkPlan{
			Subnet: plan.Subnet, GatewayIPv4: plan.GatewayIPv4, ContainerIPv4: plan.ContainerIPv4,
		},
	}
	selection := gatewayUpgradeGenerationSelection{
		Store: fixture.store, Generation: fixture.store.generation, State: fixture.state,
		Journal: fixture.journal, Existing: true, operationID: fixture.journal.OperationID,
	}
	return fixture, snapshot, selection, gatewayRebindSuccessorPreflightObservation{
		database: snapshot, candidates: candidates, host: host,
		dockerIDs: []string{strings.Repeat("a", 64)}, docker: docker, result: preflight,
	}
}

func gatewayRebindProtectedIntentTwoAppFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	appaccess.GatewayRebindStartupSnapshot, gatewayUpgradeGenerationSelection,
	gatewayRebindSuccessorPreflightObservation,
) {
	t.Helper()
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	first := snapshot.Claims[0].Roster[0]
	second := first
	second.AppID = "55555555-5555-4555-8555-555555555555"
	second.AllocationID = "66666666-6666-4666-8666-666666666666"
	second.Port++
	second.AllocationOwnerOperationID = "77777777-7777-4777-8777-777777777777"
	second.AccessRevisionID = "88888888-8888-4888-8888-888888888888"
	second.GrantAttemptID = "99999999-9999-4999-8999-999999999999"
	var err error
	second.AccessSpecDigest, err = appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: second.AppID, AllocationID: second.AllocationID, Port: second.Port,
		GatewayProfileRevisionID:     predecessor.State.Profile.RevisionID,
		GatewayProfileRevisionNumber: predecessor.State.Profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Claims[0].Roster = append(snapshot.Claims[0].Roster, second)
	binding := snapshot.Claims[0].GrantBindings[0]
	binding.AppID, binding.AttemptID = second.AppID, second.GrantAttemptID
	snapshot.Claims[0].GrantBindings = append(snapshot.Claims[0].GrantBindings, binding)
	state := cloneGatewayV2RouteState(predecessor.State)
	route := cloneGatewayV2AppRoute(state.Apps[first.AppID])
	route.LAN.GrantAttemptID = second.GrantAttemptID
	route.LAN.OwnerOperationID = second.AllocationOwnerOperationID
	route.LAN.AccessRevisionID = second.AccessRevisionID
	route.LAN.AccessSpecDigest = second.AccessSpecDigest
	route.LAN.AllocationID = second.AllocationID
	route.LAN.Port = second.Port
	state.Apps[second.AppID] = route
	refreshGatewayRebindInitialIntentClaim(t, &snapshot, &observation.result)
	observation.database = snapshot
	predecessor.State = state
	fixture.state = state
	if err := predecessor.Store.writeExact(predecessor.Store.v2Path, predecessor.Store.v2Purpose,
		state, false, maxV2RouteStateBytes); err != nil {
		t.Fatal(err)
	}
	return fixture, snapshot, predecessor, observation
}

func TestGatewayRebindProtectedIntentInstallsWithExactReadbackAndReplay(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	selection, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(observation.result.RebindOperationID)
	if err != nil || selection.Existing || selection.Generation != predecessor.Generation+1 {
		t.Fatalf("reservation = %#v, err=%v", selection, err)
	}
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, selection.Generation)
	if err != nil || !validGatewayRebindProtectedIntent(value) || len(value.Intent.Roster) != 1 ||
		value.Intent.Roster[0].EntryDigest == "" ||
		value.Intent.Roster[0].EntryDigest != value.Intent.RosterEntryDigests[0] {
		t.Fatalf("protected intent = %#v, err=%v", value, err)
	}
	if err := selection.Store.installExact(value); err != nil {
		t.Fatal(err)
	}
	if err := selection.Store.installExact(value); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	loaded, err := selection.Store.load()
	if err != nil || !reflect.DeepEqual(loaded, value) {
		t.Fatalf("readback = %#v, err=%v", loaded, err)
	}
	if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
		t.Fatal("legacy history scanner accepted rebind namespace")
	}
	if _, err := fixture.manager.scanGatewayUpgradeHistoryLocked(); err == nil {
		t.Fatal("legacy ownership scan accepted rebind namespace")
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || !reflect.DeepEqual(history.Intents[0].Intent, value) {
		t.Fatalf("protected intent history = %#v, err=%v", history, err)
	}
	replay, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(value.OperationID)
	if err != nil || !replay.Existing || !reflect.DeepEqual(replay.Intent, value) {
		t.Fatalf("replay reservation = %#v, err=%v", replay, err)
	}
	if _, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(uuid.NewString()); err == nil {
		t.Fatal("unresolved occupied generation was reused")
	}
}

func TestGatewayRebindProtectedIntentTwoAppRosterInstallsLoadsAndScansHistory(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentTwoAppFixture(t)
	selection, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(observation.result.RebindOperationID)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, selection.Generation)
	if err != nil || len(value.Intent.Roster) != 2 || len(value.Intent.RosterEntryDigests) != 2 {
		t.Fatalf("two-app protected intent = %#v, err=%v", value, err)
	}
	for index, entry := range value.Intent.Roster {
		if entry.EntryDigest == "" || entry.EntryDigest != value.Intent.RosterEntryDigests[index] {
			t.Fatalf("roster entry %d lost digest: %#v", index, entry)
		}
	}
	if err := selection.Store.installExact(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := selection.Store.load()
	if err != nil || !reflect.DeepEqual(loaded, value) {
		t.Fatalf("two-app readback = %#v, err=%v", loaded, err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || !reflect.DeepEqual(history.Intents[0].Intent, value) {
		t.Fatalf("two-app history = %#v, err=%v", history, err)
	}
}

func TestGatewayRebindProtectedIntentRejectsWrongRosterEntryDigest(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	wrongInMemory := value
	wrongInMemory.Intent.Roster = append([]appaccess.GatewayRebindRosterEntry(nil), value.Intent.Roster...)
	wrongInMemory.Intent.Roster[0].EntryDigest = strings.Repeat("f", 64)
	if validGatewayRebindProtectedIntent(wrongInMemory) {
		t.Fatal("wrong in-memory roster entry digest was accepted")
	}

	wrongStored := value
	wrongStored.Intent.Roster = append([]appaccess.GatewayRebindRosterEntry(nil), value.Intent.Roster...)
	wrongStored.Intent.RosterEntryDigests = append([]string(nil), value.Intent.RosterEntryDigests...)
	wrongStored.Intent.RosterEntryDigests[0] = strings.Repeat("f", 64)
	wrongStored.Intent.Digest, err = gatewayRebindInitialSuccessorIntentDigest(wrongStored.Intent)
	if err != nil {
		t.Fatal(err)
	}
	wrongStored.Digest, err = gatewayRebindProtectedIntentDigest(wrongStored)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot,
		wrongStored.Generation, wrongStored.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, wrongStored, true, maxGatewayRebindProtectedIntentBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); err == nil {
		t.Fatal("persisted wrong roster entry digest was accepted")
	}
}

func TestGatewayRebindProtectedIntentHistoryRejectsSelfConsistentRosterForgedAgainstPredecessor(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
	if err != nil {
		t.Fatal(err)
	}

	forgedSnapshot := cloneGatewayRebindStartupSnapshot(snapshot)
	entry := &forgedSnapshot.Claims[0].Roster[0]
	entry.AllocationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	entry.AccessSpecDigest, err = appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: entry.AppID, AllocationID: entry.AllocationID, Port: entry.Port,
		GatewayProfileRevisionID:     predecessor.State.Profile.RevisionID,
		GatewayProfileRevisionNumber: predecessor.State.Profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	forgedObservation := observation
	refreshGatewayRebindInitialIntentClaim(t, &forgedSnapshot, &forgedObservation.result)
	claim := forgedSnapshot.Claims[0].Claim

	forged := value
	forged.Intent.Roster = append([]appaccess.GatewayRebindRosterEntry(nil), forgedSnapshot.Claims[0].Roster...)
	forged.Intent.RosterEntryDigests = []string{forged.Intent.Roster[0].EntryDigest}
	forged.Intent.Claim.RequestDigest = claim.RequestDigest
	forged.Intent.Claim.SpecDigest = claim.RebindApproval.SpecDigest
	forged.Intent.Claim.RebindApproval = claim.RebindApproval
	forged.Intent.Claim.RosterDigest = claim.Spec.RosterDigest
	forged.NetworkObservation.ClaimRequestDigest = claim.RequestDigest
	forged.DatabaseDigest, err = canonicalDigest(forgedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	forged.NetworkObservationDigest, err = canonicalDigest(forged.NetworkObservation)
	if err != nil {
		t.Fatal(err)
	}
	forged.Intent.Digest, err = gatewayRebindInitialSuccessorIntentDigest(forged.Intent)
	if err != nil {
		t.Fatal(err)
	}
	forged.Digest, err = gatewayRebindProtectedIntentDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if !validGatewayRebindProtectedIntent(forged) {
		t.Fatal("self-consistent forged roster did not pass stored artifact validation")
	}

	store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot,
		forged.Generation, forged.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, forged, true, maxGatewayRebindProtectedIntentBytes); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.load(); err != nil || !reflect.DeepEqual(loaded, forged) {
		t.Fatalf("self-consistent forged roster readback = %#v, err=%v", loaded, err)
	}
	if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("self-consistent roster forged against protected predecessor was accepted")
	}
}

func TestGatewayRebindProtectedIntentUsesSelectedGenerationAndPrivateObservation(t *testing.T) {
	_, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	generation := predecessor.Generation + 3
	intent, err := newGatewayRebindInitialSuccessorIntentAtGeneration(snapshot, predecessor, observation.result, generation)
	if err != nil || !validGatewayRebindInitialSuccessorIntentAtGeneration(snapshot, predecessor, observation.result, generation, intent) ||
		validGatewayRebindInitialSuccessorIntent(snapshot, predecessor, observation.result, intent) {
		t.Fatalf("selected generation pure intent = %#v, err=%v", intent, err)
	}
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, generation)
	if err != nil || value.Generation != generation || value.Intent.Identity.Generation != generation ||
		value.Intent.Digest != intent.Digest {
		t.Fatalf("selected generation intent = %#v, err=%v", value, err)
	}
	planOnly := observation
	planOnly.candidates = nil
	planOnly.host = gatewayV2HostNetworkSnapshot{}
	planOnly.dockerIDs = nil
	planOnly.docker = nil
	if _, err := newGatewayRebindProtectedIntent(snapshot, predecessor, planOnly, generation); err == nil {
		t.Fatal("public-plan-equivalent observation was accepted without provenance")
	}
	stale := cloneGatewayRebindStartupSnapshot(snapshot)
	stale.Claims[0].Claim.StateSequence++
	if _, err := newGatewayRebindProtectedIntent(stale, predecessor, observation, generation); err == nil {
		t.Fatal("stale SQLite observation was accepted")
	}
}

func TestGatewayRebindProtectedIntentRejectsForgedPurposeAndRecomputedDigests(t *testing.T) {
	_, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindProtectedIntent)
	}{
		{"purpose", func(v *gatewayRebindProtectedIntent) { v.Purpose = "hostd/generated-ingress/rebind/other" }},
		{"operation", func(v *gatewayRebindProtectedIntent) { v.OperationID = uuid.NewString() }},
		{"approval spec", func(v *gatewayRebindProtectedIntent) {
			v.Intent.Claim.RebindApproval.SpecDigest = strings.Repeat("b", 64)
		}},
		{"profile approver", func(v *gatewayRebindProtectedIntent) {
			v.Intent.SuccessorProfile.ApprovedBy = uuid.NewString()
		}},
		{"profile request", func(v *gatewayRebindProtectedIntent) {
			v.Intent.SuccessorProfile.RequestDigest = strings.Repeat("c", 64)
		}},
		{"network plan", func(v *gatewayRebindProtectedIntent) {
			v.Intent.Network.Subnet = "10.99.0.0/28"
			v.Intent.NetworkDigest, _ = canonicalDigest(struct {
				Version int                                 `json:"version"`
				Action  string                              `json:"action"`
				Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
			}{1, "rebind-successor-network", v.Intent.Network})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := value
			forged.Intent.Roster = append([]appaccess.GatewayRebindRosterEntry(nil), value.Intent.Roster...)
			forged.Intent.RosterEntryDigests = append([]string(nil), value.Intent.RosterEntryDigests...)
			test.mutate(&forged)
			forged.Intent.Digest, err = gatewayRebindInitialSuccessorIntentDigest(forged.Intent)
			if err != nil {
				t.Fatal(err)
			}
			forged.Digest, err = gatewayRebindProtectedIntentDigest(forged)
			if err != nil {
				t.Fatal(err)
			}
			if validGatewayRebindProtectedIntent(forged) {
				t.Fatal("recomputed inner and outer digests made forged intent valid")
			}
		})
	}
}

func TestGatewayRebindProtectedIntentCreateOnlyUncertainInstallNeedsFreshReplay(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, value.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	original := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if err := original(path, purpose, body); err != nil {
			return err
		}
		return errors.New("injected post-install uncertainty")
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = original })
	if err := store.installExact(value); err == nil {
		t.Fatal("uncertain first install was reported as success")
	}
	upgradeProtectedWriteNew = original
	if err := store.installExact(value); err != nil {
		t.Fatalf("fresh exact replay after uncertain install: %v", err)
	}
}

func TestGatewayRebindProtectedIntentHistoryRejectsUnknownGapCollisionAndSubstitution(t *testing.T) {
	t.Run("unknown namespace", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		if err := os.WriteFile(filepath.Join(fixture.manager.store.root, "gateway-rebind-unknown.bundle"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
			t.Fatal("legacy scanner accepted unknown rebind namespace")
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("rebind-aware scanner accepted unknown rebind namespace")
		}
	})

	t.Run("malformed intent filename", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		name := gatewayRebindProtectedIntentFilenamePrefix + "1." + uuid.NewString() + ".bundle"
		if err := os.WriteFile(filepath.Join(fixture.manager.store.root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
			t.Fatal("legacy scanner accepted malformed rebind intent filename")
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("rebind-aware scanner accepted malformed rebind intent filename")
		}
	})

	for _, name := range []string{
		"Gateway-Rebind-Successor-Intent.v1.g00000000000000000001." + uuid.NewString() + ".bundle",
		"Routes-V2.g00000000000000000001." + uuid.NewString() + ".bundle",
		"Gateway-V1-To-V2.g00000000000000000001." + uuid.NewString() + ".bundle",
	} {
		name := name
		t.Run("case folded "+name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			if err := os.WriteFile(filepath.Join(fixture.manager.store.root, name), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
				t.Fatal("legacy scanner ignored case-folded protected namespace")
			}
			if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatal("rebind-aware scanner ignored case-folded protected namespace")
			}
		})
	}

	t.Run("generation gap", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+2)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, value.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(value); err != nil {
			t.Fatalf("install gap fixture: %v", err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("generation gap was accepted")
		}
	})

	t.Run("same generation collision", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, value.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(value); err != nil {
			t.Fatalf("install collision fixture: %v", err)
		}
		body, err := os.ReadFile(store.path)
		if err != nil {
			t.Fatal(err)
		}
		other, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(other.path, body, 0o600); err != nil {
			t.Fatalf("write collision fixture: %v", err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("same-generation collision was accepted")
		}
	})

	t.Run("operation substitution", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, value.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(value); err != nil {
			t.Fatalf("install substitution fixture: %v", err)
		}
		other, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(store.path, other.path); err != nil {
			t.Fatalf("move substitution fixture: %v", err)
		}
		if _, err := other.load(); err == nil {
			t.Fatal("bundle sealed under the original protected purpose was accepted at the substituted path")
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("operation substitution was accepted")
		}
	})

	t.Run("orphan intent", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, value.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(value); err != nil {
			t.Fatalf("install orphan source fixture: %v", err)
		}
		body, err := os.ReadFile(store.path)
		if err != nil {
			t.Fatal(err)
		}
		orphan, _ := newManagerFixture(t, false)
		orphanStore, err := newGatewayRebindProtectedIntentStore(orphan.options.DataRoot, value.Generation, value.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(orphanStore.path, body, 0o600); err != nil {
			t.Fatalf("write orphan fixture: %v", err)
		}
		if _, err := orphan.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("intent without committed predecessor was accepted")
		}
	})
}

func TestGatewayRebindProtectedIntentHistoryRejectsUnsafeAndChangedArtifacts(t *testing.T) {
	t.Run("directory artifact", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		name, _ := gatewayRebindProtectedIntentName(1, uuid.NewString())
		if err := os.Mkdir(filepath.Join(fixture.manager.store.root, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("non-regular rebind artifact was accepted")
		}
	})

	t.Run("symlink artifact", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		name, _ := gatewayRebindProtectedIntentName(1, uuid.NewString())
		path := filepath.Join(fixture.manager.store.root, name)
		if err := os.Symlink(fixture.store.v2Path, path); err != nil {
			t.Skipf("symlink unavailable on this host: %v", err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("symlink rebind artifact was accepted")
		}
	})

	t.Run("changed during scan", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		value, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot, value.Generation, value.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(value); err != nil {
			t.Fatalf("install mutation fixture: %v", err)
		}
		checkpoint := func() {
			file, openErr := os.OpenFile(store.path, os.O_RDWR, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			first := []byte{0}
			if _, readErr := file.ReadAt(first, 0); readErr != nil {
				_ = file.Close()
				t.Fatal(readErr)
			}
			first[0] ^= 0xff
			if _, writeErr := file.WriteAt(first, 0); writeErr != nil {
				_ = file.Close()
				t.Fatal(writeErr)
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				t.Fatal(syncErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(checkpoint); err == nil {
			t.Fatal("changed protected artifact was accepted")
		}
	})
}

func TestGatewayHistoryRejectsCaseFoldedGenerationZeroNamespaces(t *testing.T) {
	for _, name := range []string{"Routes-V2.bundle", "Gateway-V1-To-V2.bundle"} {
		name := name
		t.Run(name, func(t *testing.T) {
			manager, _ := newManagerFixture(t, false)
			if err := os.WriteFile(filepath.Join(manager.store.root, name), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGatewayHistorySnapshot(manager.store); err == nil {
				t.Fatal("history scanner ignored case-folded generation-zero namespace")
			}
			if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
				t.Fatal("ownership scanner ignored case-folded generation-zero namespace")
			}
		})
	}
}
