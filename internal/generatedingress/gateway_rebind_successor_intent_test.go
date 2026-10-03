package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func gatewayRebindInitialIntentFixture(t *testing.T) (appaccess.GatewayRebindStartupSnapshot,
	gatewayV2RouteState, gatewayMigrationJournal, GatewayRebindSuccessorPreflight,
	gatewayUpgradeGenerationSelection,
) {
	t.Helper()
	fixture := newGatewayRebindPredecessorFixture(t)
	snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claim := snapshot.Claims[0].Claim
	profile := GatewayRebindSuccessorProfile{
		RevisionID:     claim.Spec.SuccessorProfileRevisionID,
		RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		OperationID:    claim.Spec.SuccessorProfileOperationID,
		RequestDigest:  claim.SuccessorProfileRequestDigest,
		SpecDigest:     claim.ConfigureApproval.SpecDigest,
		SelectedIPv4:   claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID:    claim.Spec.SuccessorProfile.InterfaceID,
		PortStart:      claim.Spec.SuccessorProfile.PortStart,
		PortEnd:        claim.Spec.SuccessorProfile.PortEnd,
		ApprovedBy:     claim.ConfigureApproval.ActorID,
	}
	preflight := GatewayRebindSuccessorPreflight{
		RebindOperationID:  claim.Spec.OperationID,
		ClaimRequestDigest: claim.RequestDigest,
		Profile:            profile,
		Network: GatewayRebindSuccessorNetworkPlan{
			Subnet: "10.241.0.16/28", GatewayIPv4: "10.241.0.17", ContainerIPv4: "10.241.0.18",
		},
	}
	return snapshot, fixture.state, fixture.journal, preflight, gatewayUpgradeGenerationSelection{
		Store: fixture.store, Generation: fixture.store.generation,
		State: fixture.state, Journal: fixture.journal, Existing: true,
		operationID: fixture.journal.OperationID,
	}
}

func gatewayRebindIntentSelection(selection gatewayUpgradeGenerationSelection,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) gatewayUpgradeGenerationSelection {
	selection.State, selection.Journal = state, journal
	return selection
}

func refreshGatewayRebindInitialIntentClaim(t *testing.T, snapshot *appaccess.GatewayRebindStartupSnapshot,
	preflight *GatewayRebindSuccessorPreflight,
) {
	t.Helper()
	entry := &snapshot.Claims[0]
	claim := &entry.Claim
	claim.Spec.RosterCount = int64(len(entry.Roster))
	var err error
	for index := range entry.Roster {
		entry.Roster[index].Ordinal = int64(index + 1)
		entry.Roster[index].EntryDigest, err = appaccess.GatewayRebindRosterEntryDigest(entry.Roster[index])
		if err != nil {
			t.Fatal(err)
		}
	}
	claim.Spec.RosterDigest, err = appaccess.GatewayRebindRosterDigest(entry.Roster)
	if err != nil {
		t.Fatal(err)
	}
	claim.RebindApproval.SpecDigest, err = appaccess.GatewayRebindSpecDigest(claim.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim.RequestDigest, err = canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpec `json:"spec"`
		Rebind    appaccess.Approval          `json:"rebindApproval"`
		Configure appaccess.Approval          `json:"configureApproval"`
	}{claim.Spec, claim.RebindApproval, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
	preflight.ClaimRequestDigest = claim.RequestDigest
}

func TestGatewayRebindInitialSuccessorIntentBindsPreparedClaimAndResources(t *testing.T) {
	snapshot, state, journal, preflight, selection := gatewayRebindInitialIntentFixture(t)
	intent, err := newGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight)
	if err != nil {
		t.Fatal(err)
	}
	if !validGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight, intent) ||
		intent.Predecessor.Kind != gatewayRebindInitialSourceKind || intent.Predecessor.Generation != 0 ||
		intent.Predecessor.Resources != journal.Resources || intent.Identity.Version != gatewayRebindSuccessorIdentityVersion ||
		intent.Identity.Generation != 1 || intent.Claim.RosterCount != 1 ||
		len(intent.Roster) != 1 || len(intent.RosterEntryDigests) != 1 ||
		intent.RosterEntryDigests[0] != snapshot.Claims[0].Roster[0].EntryDigest ||
		intent.NetworkDigest != "d09be41f92a27d04dda633b3a9d7361db98a936ac071caaffd75884e6ce46451" {
		t.Fatalf("intent lost exact binding: %+v", intent)
	}
	second, err := newGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight)
	if err != nil || !reflect.DeepEqual(second, intent) {
		t.Fatalf("intent was not deterministic: %v", err)
	}
}

func TestGatewayRebindInitialSuccessorIntentUsesAttestedHistoryGeneration(t *testing.T) {
	snapshot, state, journal, preflight, selection := gatewayRebindInitialIntentFixture(t)
	// A committed migration-026 upgrade can be the latest generation after
	// earlier retired upgrade attempts. Its fixed v2 resource names do not
	// imply generation zero.
	store := *selection.Store
	store.generation = 2
	store.operationID = journal.OperationID
	selection.Store = &store
	selection.Generation = 2
	selection = gatewayRebindIntentSelection(selection, state, journal)
	intent, err := newGatewayRebindInitialSuccessorIntent(snapshot, selection, preflight)
	if err != nil || intent.Predecessor.Generation != 2 || intent.Identity.Generation != 3 ||
		!validGatewayRebindInitialSuccessorIntent(snapshot, selection, preflight, intent) {
		t.Fatalf("nonzero predecessor generation rejected: intent=%+v err=%v", intent, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayUpgradeGenerationSelection)
	}{
		{"store generation mismatch", func(v *gatewayUpgradeGenerationSelection) {
			copy := *v.Store
			copy.generation = 1
			v.Store = &copy
		}},
		{"store operation mismatch", func(v *gatewayUpgradeGenerationSelection) {
			copy := *v.Store
			copy.operationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			v.Store = &copy
		}},
		{"selection operation mismatch", func(v *gatewayUpgradeGenerationSelection) {
			v.operationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		}},
		{"not existing", func(v *gatewayUpgradeGenerationSelection) { v.Existing = false }},
		{"partial state", func(v *gatewayUpgradeGenerationSelection) { v.PartialState = true }},
		{"retired", func(v *gatewayUpgradeGenerationSelection) { v.Retired = true }},
		{"aborted", func(v *gatewayUpgradeGenerationSelection) { v.Aborted = true }},
		{"overflow", func(v *gatewayUpgradeGenerationSelection) {
			copy := *v.Store
			copy.generation = math.MaxUint64
			v.Store = &copy
			v.Generation = math.MaxUint64
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := selection
			test.mutate(&changed)
			if _, err := newGatewayRebindInitialSuccessorIntent(snapshot, changed, preflight); err == nil {
				t.Fatal("invalid history selection was accepted")
			}
		})
	}
}

func TestGatewayRebindInitialSuccessorIntentEmptyAndOrderedRoster(t *testing.T) {
	snapshot, state, journal, preflight, selection := gatewayRebindInitialIntentFixture(t)
	for appID, route := range state.Apps {
		route.LAN = nil
		state.Apps[appID] = route
	}
	snapshot.Claims[0].Roster = nil
	snapshot.Claims[0].GrantBindings = nil
	refreshGatewayRebindInitialIntentClaim(t, &snapshot, &preflight)
	empty, err := newGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight)
	if err != nil || empty.Claim.RosterCount != 0 || len(empty.Roster) != 0 || len(empty.RosterEntryDigests) != 0 {
		t.Fatalf("empty roster rejected: %+v %v", empty, err)
	}
	// The canonical roster digest sorts ordinals, so this intent independently
	// checks the caller's slice order before copying it.
	snapshot, state, journal, preflight, selection = gatewayRebindInitialIntentFixture(t)
	first := snapshot.Claims[0].Roster[0]
	second := first
	second.AppID = "55555555-5555-4555-8555-555555555555"
	second.AllocationID = "66666666-6666-4666-8666-666666666666"
	second.Port++
	second.AllocationOwnerOperationID = "77777777-7777-4777-8777-777777777777"
	second.AccessRevisionID = "88888888-8888-4888-8888-888888888888"
	second.GrantAttemptID = "99999999-9999-4999-8999-999999999999"
	second.AccessSpecDigest, err = appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: second.AppID, AllocationID: second.AllocationID, Port: second.Port,
		GatewayProfileRevisionID:     state.Profile.RevisionID,
		GatewayProfileRevisionNumber: state.Profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Claims[0].Roster = append(snapshot.Claims[0].Roster, second)
	binding := snapshot.Claims[0].GrantBindings[0]
	binding.AppID, binding.AttemptID = second.AppID, second.GrantAttemptID
	snapshot.Claims[0].GrantBindings = append(snapshot.Claims[0].GrantBindings, binding)
	route := cloneGatewayV2AppRoute(state.Apps[first.AppID])
	route.LAN.GrantAttemptID = second.GrantAttemptID
	route.LAN.OwnerOperationID = second.AllocationOwnerOperationID
	route.LAN.AccessRevisionID = second.AccessRevisionID
	route.LAN.AccessSpecDigest = second.AccessSpecDigest
	route.LAN.AllocationID = second.AllocationID
	route.LAN.Port = second.Port
	state.Apps[second.AppID] = route
	refreshGatewayRebindInitialIntentClaim(t, &snapshot, &preflight)
	ordered, err := newGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight)
	if err != nil || len(ordered.Roster) != 2 || ordered.Roster[0].Ordinal != 1 || ordered.Roster[1].Ordinal != 2 {
		t.Fatalf("ordered roster rejected: %+v %v", ordered, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*appaccess.GatewayRebindRosterEntry)
	}{
		{"duplicate app", func(v *appaccess.GatewayRebindRosterEntry) { v.AppID = first.AppID }},
		{"duplicate allocation", func(v *appaccess.GatewayRebindRosterEntry) { v.AllocationID = first.AllocationID }},
		{"duplicate port", func(v *appaccess.GatewayRebindRosterEntry) { v.Port = first.Port }},
		{"skipped ordinal", func(v *appaccess.GatewayRebindRosterEntry) { v.Ordinal = 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			altered := append([]appaccess.GatewayRebindRosterEntry(nil), snapshot.Claims[0].Roster...)
			test.mutate(&altered[1])
			if _, err := appaccess.GatewayRebindRosterDigest(altered); err == nil {
				t.Fatal("noncanonical roster was digestible")
			}
		})
	}
	missingBinding := snapshot
	missingBinding.Claims = append([]appaccess.GatewayRebindStartupClaim(nil), snapshot.Claims...)
	missingBinding.Claims[0].GrantBindings = missingBinding.Claims[0].GrantBindings[:1]
	if _, err := newGatewayRebindInitialSuccessorIntent(missingBinding, gatewayRebindIntentSelection(selection, state, journal), preflight); err == nil {
		t.Fatal("missing grant binding was accepted")
	}
	mismatchedBinding := snapshot
	mismatchedBinding.Claims = append([]appaccess.GatewayRebindStartupClaim(nil), snapshot.Claims...)
	mismatchedBinding.Claims[0].GrantBindings = append([]appaccess.GatewayRebindStartupGrantBinding(nil), snapshot.Claims[0].GrantBindings...)
	mismatchedBinding.Claims[0].GrantBindings[1].AttemptID = first.GrantAttemptID
	if _, err := newGatewayRebindInitialSuccessorIntent(mismatchedBinding, gatewayRebindIntentSelection(selection, state, journal), preflight); err == nil {
		t.Fatal("mismatched grant binding was accepted")
	}
	snapshot.Claims[0].Roster[0], snapshot.Claims[0].Roster[1] = snapshot.Claims[0].Roster[1], snapshot.Claims[0].Roster[0]
	if _, err := newGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight); err == nil {
		t.Fatal("reordered roster was accepted")
	}
	snapshot.Claims[0].Roster[0], snapshot.Claims[0].Roster[1] = snapshot.Claims[0].Roster[1], snapshot.Claims[0].Roster[0]
	stateDigest := ordered.Predecessor.StateDigest
	snapshot.Claims[0].Roster[0].Port++
	preflight.Network.Subnet = "10.241.0.32/28"
	journal.Resources.FinalContainerID = strings.Repeat("a", 64)
	state.Apps[first.AppID] = gatewayV2AppRoute{}
	if ordered.Roster[0].Port != first.Port || ordered.Network.Subnet != "10.241.0.16/28" ||
		ordered.Predecessor.Resources.FinalContainerID == journal.Resources.FinalContainerID ||
		ordered.Predecessor.StateDigest != stateDigest {
		t.Fatal("intent retained caller-owned mutable input")
	}
}

func TestGatewayRebindInitialSuccessorIntentRejectsMutatedInputs(t *testing.T) {
	snapshot, state, journal, preflight, selection := gatewayRebindInitialIntentFixture(t)
	tests := []struct {
		name   string
		mutate func(*appaccess.GatewayRebindStartupSnapshot, *gatewayV2RouteState, *gatewayMigrationJournal, *GatewayRebindSuccessorPreflight)
	}{
		{"missing claim", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims = nil
		}},
		{"multiple claims", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims = append(s.Claims, s.Claims[0])
		}},
		{"wrong phase", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.State = appaccess.GatewayRebindState("committed")
		}},
		{"wrong sequence", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.StateSequence++
		}},
		{"claim digest", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.RequestDigest = strings.Repeat("a", 64)
		}},
		{"spec digest", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.RebindApproval.SpecDigest = strings.Repeat("a", 64)
		}},
		{"rebind action", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.RebindApproval.Action = appaccess.ActionConfigureGateway
		}},
		{"configure action", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.ConfigureApproval.Action = appaccess.ActionRebindGateway
		}},
		{"configure actor", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.ConfigureApproval.ActorID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		}},
		{"successor request", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Claim.SuccessorProfileRequestDigest = strings.Repeat("a", 64)
		}},
		{"predecessor state", func(_ *appaccess.GatewayRebindStartupSnapshot, s *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Network.Subnet = "10.240.0.16/28"
		}},
		{"predecessor journal", func(_ *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, j *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			j.Resources.FinalContainerID = ""
		}},
		{"predecessor identity", func(_ *appaccess.GatewayRebindStartupSnapshot, s *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Identity.Digest = strings.Repeat("a", 64)
		}},
		{"predecessor profile", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.CurrentProfile.Spec.InterfaceID = "other"
		}},
		{"preflight request", func(_ *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, p *GatewayRebindSuccessorPreflight) {
			p.ClaimRequestDigest = strings.Repeat("a", 64)
		}},
		{"preflight profile", func(_ *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, p *GatewayRebindSuccessorPreflight) {
			p.Profile.SelectedIPv4 = "192.168.97.9"
		}},
		{"preflight network", func(_ *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, p *GatewayRebindSuccessorPreflight) {
			p.Network.ContainerIPv4 = "10.241.0.19"
		}},
		{"roster digest", func(s *appaccess.GatewayRebindStartupSnapshot, _ *gatewayV2RouteState, _ *gatewayMigrationJournal, _ *GatewayRebindSuccessorPreflight) {
			s.Claims[0].Roster[0].EntryDigest = strings.Repeat("a", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot
			currentProfile := *snapshot.CurrentProfile
			s.CurrentProfile = &currentProfile
			s.Claims = append([]appaccess.GatewayRebindStartupClaim(nil), snapshot.Claims...)
			s.Claims[0].Roster = append([]appaccess.GatewayRebindRosterEntry(nil), snapshot.Claims[0].Roster...)
			st := cloneGatewayV2RouteState(state)
			j, p := journal, preflight
			test.mutate(&s, &st, &j, &p)
			if _, err := newGatewayRebindInitialSuccessorIntent(s,
				gatewayRebindIntentSelection(selection, st, j), p); err == nil {
				t.Fatal("mutated input was accepted")
			}
		})
	}
}

func TestGatewayRebindInitialSuccessorIntentRejectsSubstitutionWithForgedDigest(t *testing.T) {
	snapshot, state, journal, preflight, selection := gatewayRebindInitialIntentFixture(t)
	original, err := newGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindSuccessorIntent)
	}{
		{"source kind", func(v *gatewayRebindSuccessorIntent) { v.Predecessor.Kind = "prior-rebind" }},
		{"source generation", func(v *gatewayRebindSuccessorIntent) { v.Predecessor.Generation = 1 }},
		{"state digest", func(v *gatewayRebindSuccessorIntent) { v.Predecessor.StateDigest = strings.Repeat("a", 64) }},
		{"journal digest", func(v *gatewayRebindSuccessorIntent) { v.Predecessor.JournalDigest = strings.Repeat("a", 64) }},
		{"resource", func(v *gatewayRebindSuccessorIntent) {
			v.Predecessor.Resources.FinalContainerID = strings.Repeat("a", 64)
		}},
		{"network", func(v *gatewayRebindSuccessorIntent) { v.Network.Subnet = "10.241.0.32/28" }},
		{"roster entry", func(v *gatewayRebindSuccessorIntent) { v.Roster[0].Port++ }},
		{"roster entry digest", func(v *gatewayRebindSuccessorIntent) { v.RosterEntryDigests[0] = strings.Repeat("a", 64) }},
		{"legacy v2 identity", func(v *gatewayRebindSuccessorIntent) { v.Identity.Version = gatewayV2IdentityVersion }},
		{"identity name", func(v *gatewayRebindSuccessorIntent) { v.Identity.FinalContainer = gatewayV2ContainerName }},
		{"claim actor", func(v *gatewayRebindSuccessorIntent) {
			v.Claim.RebindApproval.ActorID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			changed.Roster = append([]appaccess.GatewayRebindRosterEntry(nil), original.Roster...)
			changed.RosterEntryDigests = append([]string(nil), original.RosterEntryDigests...)
			test.mutate(&changed)
			changed.Digest, err = gatewayRebindInitialSuccessorIntentDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if validGatewayRebindInitialSuccessorIntent(snapshot, gatewayRebindIntentSelection(selection, state, journal), preflight, changed) {
				t.Fatal("forged candidate was accepted")
			}
		})
	}
}

func TestGatewayRebindInitialSuccessorIntentGoldenEncoding(t *testing.T) {
	vector := gatewayRebindSuccessorIntent{
		Version: 1,
		Claim: gatewayRebindSuccessorClaimBinding{
			OperationID: "rebind", RequestDigest: "request", SpecDigest: "spec",
			RebindApproval:                appaccess.Approval{Action: appaccess.ActionRebindGateway, SpecDigest: "rebind-spec", ActorID: "admin-a"},
			ConfigureApproval:             appaccess.Approval{Action: appaccess.ActionConfigureGateway, SpecDigest: "profile-spec", ActorID: "admin-b"},
			SuccessorProfileRequestDigest: "profile-request", RosterDigest: "roster", RosterCount: 0,
		},
		Predecessor: gatewayRebindSuccessorSourceBinding{
			Kind: gatewayRebindInitialSourceKind, Generation: 0, OperationID: "upgrade",
			Profile: gatewayProfileBinding{
				RevisionID: "old-profile", RevisionNumber: 1, SpecDigest: "old-spec",
				SelectedIPv4: "192.168.1.2", InterfaceID: "old-nic", PortStart: 8100, PortEnd: 8110,
			},
			StateDigest: "state", JournalDigest: "journal", IdentityDigest: "old-identity",
			Resources: gatewayV2ResourceBindings{ImageID: "image", IngressNetworkID: "old-network", FinalContainerID: "old-container"},
		},
		SuccessorProfile: gatewayRebindSuccessorIntentProfile{
			RevisionID: "new-profile", RevisionNumber: 2, OperationID: "configure",
			RequestDigest: "profile-request", SpecDigest: "profile-spec", SelectedIPv4: "192.168.1.3",
			InterfaceID: "new-nic", PortStart: 8100, PortEnd: 8110, ApprovedBy: "admin-b",
		},
		Roster: []appaccess.GatewayRebindRosterEntry{}, RosterEntryDigests: []string{},
		Network: gatewayRebindSuccessorIntentNetwork{
			Subnet: "10.241.0.16/28", GatewayIPv4: "10.241.0.17", ContainerIPv4: "10.241.0.18",
		},
		NetworkDigest: "plan",
		Identity: gatewayRebindSuccessorIdentity{
			Version: gatewayRebindSuccessorIdentityVersion, Digest: "identity", Generation: 1,
			OperationID: "rebind", ProfileRevisionID: "new-profile", ProfileRevisionNumber: 2,
			ProfileSpecDigest: "profile-spec", CaddyImageDigest: "image-digest",
			FinalContainer: "final", StageContainer: "stage", ConfigVolume: "config",
			DataVolume: "data", IngressNetwork: "ingress", FinalHostname: "final-host",
			StageHostname: "stage-host", StageConfigFilename: "stage.json", ActiveConfigFilename: "active.json",
		},
	}
	body, err := json.Marshal(vector)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := gatewayRebindInitialSuccessorIntentDigest(vector)
	if err != nil {
		t.Fatal(err)
	}
	// This fixed vector pins the complete protected JSON field order and empty
	// roster arrays. The expected digest was computed independently from the
	// canonical JSON, not by deriving the expectation from the constructor.
	const want = "f689ee4ccfa498d8ea245dd91e7c5febe801713e1183e474acb7d59723c8ff6f"
	independent := sha256.Sum256(body)
	if digest != want || hex.EncodeToString(independent[:]) != want ||
		!strings.Contains(string(body), `"roster":[],"rosterEntryDigests":[]`) ||
		!strings.Contains(string(body), `"successorProfile":{"revisionId"`) ||
		!strings.Contains(string(body), `"network":{"subnet"`) {
		t.Fatalf("intent canonical encoding drifted: digest=%s body=%s", digest, body)
	}
}
