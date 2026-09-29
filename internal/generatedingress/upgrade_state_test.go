package generatedingress

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	upgradeTestAppA  = "11111111-1111-4111-8111-111111111111"
	upgradeTestAppB  = "22222222-2222-4222-8222-222222222222"
	upgradeTestActor = "33333333-3333-4333-8333-333333333333"
)

func TestPrepareGatewayV2StateBindsCanonicalPlanAndDeepCopiesV1(t *testing.T) {
	source, input := upgradeTestPreparation(t)
	original, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 2 || state.OperationID != input.OperationID || len(state.Apps) != 2 {
		t.Fatalf("state = %#v", state)
	}
	for appID, app := range state.Apps {
		if app.LAN != nil {
			t.Fatalf("initial app %s has LAN binding %#v", appID, app.LAN)
		}
	}
	if state.Identity.Version != gatewayV2IdentityVersion || state.Identity.CaddyImageDigest != gatewayV2CaddyImageDigest ||
		state.Identity.FinalContainer != gatewayV2ContainerName || state.Identity.StageContainer != gatewayV2StageContainerBase+input.OperationID ||
		state.Identity.ConfigVolume != gatewayV2ConfigVolumeName || state.Identity.DataVolume != gatewayV2DataVolumeName ||
		state.Identity.IngressNetwork != gatewayV2NetworkName || state.Identity.StageConfigFilename != "stage.json" || state.Identity.ActiveConfigFilename != "active.json" {
		t.Fatalf("identity = %#v", state.Identity)
	}
	if journal.Phase != gatewayPhasePrepared || journal.Source.Format != 1 || journal.Target.Format != 2 ||
		journal.Source.StateDigest != state.SourceV1StateDigest || journal.Target.IdentityDigest != state.Identity.Digest ||
		journal.UpgradeAction.ApprovedBy != upgradeTestActor {
		t.Fatalf("journal = %#v", journal)
	}
	stateDigest, _ := canonicalDigest(state)
	planDigest, _ := gatewayV2PlanDigest(state)
	if journal.Target.StateDigest != stateDigest || journal.Target.PlanDigest != planDigest {
		t.Fatalf("target digests state=%q plan=%q", journal.Target.StateDigest, journal.Target.PlanDigest)
	}

	secondState, secondJournal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(struct {
		State   gatewayV2RouteState     `json:"state"`
		Journal gatewayMigrationJournal `json:"journal"`
	}{state, journal})
	secondJSON, _ := json.Marshal(struct {
		State   gatewayV2RouteState     `json:"state"`
		Journal gatewayMigrationJournal `json:"journal"`
	}{secondState, secondJournal})
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("same v1 state and bindings produced different v2 artifacts")
	}

	delete(source.Active, upgradeTestAppB)
	changed := source.Active[upgradeTestAppA]
	changed.Endpoints[0].NetworkAlias = "mutated"
	source.Active[upgradeTestAppA] = changed
	if len(state.Apps) != 2 || state.Apps[upgradeTestAppA].Route.Endpoints[0].NetworkAlias == "mutated" {
		t.Fatal("prepared v2 state aliases mutable v1 input")
	}
	after, err := json.Marshal(struct {
		Version int                    `json:"version"`
		Active  map[string]routeRecord `json:"active"`
		Pending *pendingRoute          `json:"pending,omitempty"`
	}{Version: 1, Active: map[string]routeRecord{
		upgradeTestAppA: upgradeTestSourceState().Active[upgradeTestAppA],
		upgradeTestAppB: upgradeTestSourceState().Active[upgradeTestAppB],
	}})
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("fixture canonical v1 representation changed unexpectedly")
	}
}

func TestPrepareGatewayV2StateRejectsStaleBindingsAndPendingV1(t *testing.T) {
	source, input := upgradeTestPreparation(t)
	tests := []struct {
		name   string
		mutate func(*routeState, *gatewayUpgradePreparation)
	}{
		{name: "stale action digest", mutate: func(_ *routeState, input *gatewayUpgradePreparation) {
			input.ApprovedActionDigest = strings.Repeat("f", 64)
		}},
		{name: "stale profile digest", mutate: func(_ *routeState, input *gatewayUpgradePreparation) {
			input.Profile.SpecDigest = strings.Repeat("e", 64)
		}},
		{name: "missing approving actor", mutate: func(_ *routeState, input *gatewayUpgradePreparation) { input.ApprovedBy = "" }},
		{name: "wrong image-independent source identity", mutate: func(_ *routeState, input *gatewayUpgradePreparation) { input.SourceIdentityDigest = "not-a-digest" }},
		{name: "v2 subnet contains selected LAN address", mutate: func(_ *routeState, input *gatewayUpgradePreparation) {
			input.Network = gatewayV2NetworkPlan{Subnet: "192.168.50.0/24", GatewayIPv4: "192.168.50.1", ContainerIPv4: "192.168.50.2"}
		}},
		{name: "pending v1 route", mutate: func(source *routeState, _ *gatewayUpgradePreparation) {
			proposed := source.Active[upgradeTestAppA]
			proposed.Slot = generatedruntime.SlotGreen
			source.Pending = &pendingRoute{AppID: upgradeTestAppA, Previous: cloneRouteRecordPointer(source.Active[upgradeTestAppA]), Proposed: proposed}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateSource := cloneRouteStateForTest(source)
			candidateInput := input
			test.mutate(&candidateSource, &candidateInput)
			if _, _, err := prepareGatewayV2State(candidateSource, candidateInput); err == nil {
				t.Fatal("stale or unsafe preparation was accepted")
			}
		})
	}
}

func TestGatewayUpgradeStoreRetainsV1AndRejectsMissingOrTamperedState(t *testing.T) {
	t.Run("retains v1 and separates protected purposes", func(t *testing.T) {
		store, state, journal, source := persistedUpgradeFixture(t)
		loadedState, loadedJournal, err := store.loadBoundUpgrade(journal.OperationID)
		if err != nil || !reflect.DeepEqual(loadedState, state) || !reflect.DeepEqual(loadedJournal, journal) {
			t.Fatalf("bound load state=%#v journal=%#v err=%v", loadedState, loadedJournal, err)
		}
		retained, err := store.directory.load()
		if err != nil || !reflect.DeepEqual(retained, source) {
			t.Fatalf("retained v1 = %#v err=%v", retained, err)
		}
		if _, err := secretfile.Read(store.v2Path, gatewayMigrationPurpose); err == nil {
			t.Fatal("v2 state opened with migration purpose")
		}
		if _, err := secretfile.Read(store.journalPath, v2RouteStatePurpose); err == nil {
			t.Fatal("migration journal opened with v2 state purpose")
		}
		if v2RouteStatePurpose == statePurpose || gatewayMigrationPurpose == statePurpose || v2RouteStatePurpose == gatewayMigrationPurpose ||
			maxV2RouteStateBytes == maxStateBytes || maxGatewayMigrationBytes == maxStateBytes || maxV2RouteStateBytes == maxGatewayMigrationBytes {
			t.Fatal("upgrade artifacts do not have distinct protection boundaries")
		}
	})

	t.Run("missing target", func(t *testing.T) {
		source, input := upgradeTestPreparation(t)
		state, journal, err := prepareGatewayV2State(source, input)
		if err != nil {
			t.Fatal(err)
		}
		store := newUpgradeStoreWithV1(t, source)
		if _, err := store.loadV2State(); err == nil {
			t.Fatal("missing v2 state was accepted")
		}
		if err := store.createMigrationJournal(journal); err == nil {
			t.Fatal("journal was created without target state")
		}
		if err := store.createV2State(state); err != nil {
			t.Fatal(err)
		}
		if _, err := store.loadMigrationJournal(); err == nil {
			t.Fatal("missing journal was accepted")
		}
	})

	t.Run("unknown journal field", func(t *testing.T) {
		store, _, journal, _ := persistedUpgradeFixture(t)
		body, _ := json.Marshal(journal)
		tampered := append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"unknown":true}`)...)
		if err := secretfile.Write(store.journalPath, gatewayMigrationPurpose, tampered); err != nil {
			t.Fatal(err)
		}
		if _, err := store.loadMigrationJournal(); err == nil {
			t.Fatal("journal with unknown field was accepted")
		}
	})

	t.Run("target digest tamper", func(t *testing.T) {
		store, state, journal, _ := persistedUpgradeFixture(t)
		state.Network.ContainerIPv4 = "10.240.0.3"
		if !validGatewayV2RouteState(state) {
			t.Fatal("test state should remain structurally valid")
		}
		body, _ := json.Marshal(state)
		if err := secretfile.Write(store.v2Path, v2RouteStatePurpose, body); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.loadBoundUpgrade(journal.OperationID); err == nil {
			t.Fatal("pre-commit target digest tamper was accepted")
		}
	})
}

func TestGatewayUpgradeStoreCreateIsExactReplayOnly(t *testing.T) {
	source, input := upgradeTestPreparation(t)
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	store := newUpgradeStoreWithV1(t, source)
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatalf("exact orphan target replay failed: %v", err)
	}
	changedState := state
	changedState.Network.ContainerIPv4 = "10.240.0.3"
	if err := store.createV2State(changedState); err == nil {
		t.Fatal("changed target replay replaced orphan state")
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatalf("exact journal replay failed: %v", err)
	}
	changedJournal := journal
	changedJournal.Source.IdentityDigest = strings.Repeat("b", 64)
	if err := store.createMigrationJournal(changedJournal); err == nil || !strings.Contains(err.Error(), "idempotency mismatch") {
		t.Fatalf("journal payload mismatch error = %v", err)
	}
	stateWithLAN := state
	stateWithLAN.Apps = cloneGatewayV2RouteState(state).Apps
	bindLANForTest(t, &stateWithLAN, upgradeTestAppA, 8100)
	otherStore := newUpgradeStoreWithV1(t, source)
	if err := otherStore.createV2State(stateWithLAN); err == nil {
		t.Fatal("initial v2 state with LAN binding was accepted")
	}
}

func TestGatewayMigrationTransitionsReplayAndStopOnAmbiguousWrite(t *testing.T) {
	store, _, journal, _ := persistedUpgradeFixture(t)
	operationID := journal.OperationID
	if _, err := store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseStaged); err == nil {
		t.Fatal("skipped stage intent")
	}
	installed, err := store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseStageIntent)
	if err != nil || installed.Phase != gatewayPhaseStageIntent {
		t.Fatalf("stage intent = %#v err=%v", installed, err)
	}
	if replay, err := store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseStageIntent); err != nil || replay.Phase != gatewayPhaseStageIntent {
		t.Fatalf("stage intent replay = %#v err=%v", replay, err)
	}
	if _, err := store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseRollbackIntent); err == nil {
		t.Fatal("stale expected phase was accepted")
	}

	originalWrite := upgradeProtectedWrite
	upgradeProtectedWrite = func(path, purpose string, plaintext []byte) error {
		if err := originalWrite(path, purpose, plaintext); err != nil {
			return err
		}
		return errors.New("injected post-install write error")
	}
	t.Cleanup(func() { upgradeProtectedWrite = originalWrite })
	installed, err = store.transitionMigrationJournal(operationID, gatewayPhaseStageIntent, gatewayPhaseStaged)
	if err == nil || installed.Phase != "" {
		t.Fatalf("ambiguous installed write was treated as durable success: %#v err=%v", installed, err)
	}
	upgradeProtectedWrite = originalWrite
	observed, err := store.loadMigrationJournal()
	if err != nil || observed.Phase != gatewayPhaseStaged {
		t.Fatalf("protected journal after ambiguous write = %#v err=%v", observed, err)
	}

	for _, transition := range [][2]gatewayMigrationPhase{
		{gatewayPhaseStaged, gatewayPhaseTransferIntent},
		{gatewayPhaseTransferIntent, gatewayPhaseV2Serving},
		{gatewayPhaseV2Serving, gatewayPhaseCommitted},
	} {
		installed, err = store.transitionMigrationJournal(operationID, transition[0], transition[1])
		if err != nil || installed.Phase != transition[1] {
			t.Fatalf("transition %s -> %s = %#v err=%v", transition[0], transition[1], installed, err)
		}
	}
	if _, err := store.transitionMigrationJournal(operationID, gatewayPhaseCommitted, gatewayPhaseRollbackIntent); err == nil {
		t.Fatal("committed journal exited terminal phase")
	}

	rollbackStore, _, rollbackJournal, _ := persistedUpgradeFixture(t)
	rolled, err := rollbackStore.transitionMigrationJournal(rollbackJournal.OperationID, gatewayPhasePrepared, gatewayPhaseRollbackIntent)
	if err != nil || rolled.Phase != gatewayPhaseRollbackIntent {
		t.Fatal(err)
	}
	rolled, err = rollbackStore.transitionMigrationJournal(rollbackJournal.OperationID, gatewayPhaseRollbackIntent, gatewayPhaseRolledBack)
	if err != nil || rolled.Phase != gatewayPhaseRolledBack {
		t.Fatal(err)
	}

	uncertainStore, _, uncertainJournal, _ := persistedUpgradeFixture(t)
	uncertain, err := uncertainStore.transitionMigrationJournal(uncertainJournal.OperationID, gatewayPhasePrepared, gatewayPhaseUncertain)
	if err != nil || uncertain.Phase != gatewayPhaseUncertain {
		t.Fatal(err)
	}
	if _, err := uncertainStore.transitionMigrationJournal(uncertainJournal.OperationID, gatewayPhaseUncertain, gatewayPhaseRollbackIntent); err == nil {
		t.Fatal("uncertain journal exited terminal phase")
	}
}

func TestGatewayV2StateValidatesLANBindingDigestsAndUniquePorts(t *testing.T) {
	_, state, _, _ := persistedUpgradeFixture(t)
	bindLANForTest(t, &state, upgradeTestAppA, 8100)
	bindLANForTest(t, &state, upgradeTestAppB, 8101)
	if !validGatewayV2RouteState(state) {
		t.Fatal("valid LAN bindings were rejected")
	}

	tests := []struct {
		name   string
		mutate func(*gatewayV2RouteState)
	}{
		{name: "duplicate port", mutate: func(value *gatewayV2RouteState) { value.Apps[upgradeTestAppB].LAN.Port = 8100 }},
		{name: "duplicate allocation identity with valid digest", mutate: func(value *gatewayV2RouteState) {
			value.Apps[upgradeTestAppB].LAN.AllocationID = value.Apps[upgradeTestAppA].LAN.AllocationID
			binding := value.Apps[upgradeTestAppB].LAN
			digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
				AppID: upgradeTestAppB, AllocationID: binding.AllocationID, Port: binding.Port,
				GatewayProfileRevisionID: binding.ProfileRevisionID, GatewayProfileRevisionNumber: binding.ProfileRevisionNumber,
			})
			if err != nil {
				t.Fatal(err)
			}
			binding.AccessSpecDigest = digest
		}},
		{name: "duplicate access revision identity", mutate: func(value *gatewayV2RouteState) {
			value.Apps[upgradeTestAppB].LAN.AccessRevisionID = value.Apps[upgradeTestAppA].LAN.AccessRevisionID
		}},
		{name: "stale access digest", mutate: func(value *gatewayV2RouteState) {
			value.Apps[upgradeTestAppA].LAN.AccessSpecDigest = strings.Repeat("f", 64)
		}},
		{name: "stale profile ref", mutate: func(value *gatewayV2RouteState) { value.Apps[upgradeTestAppA].LAN.ProfileRevisionNumber++ }},
		{name: "route absent", mutate: func(value *gatewayV2RouteState) {
			app := value.Apps[upgradeTestAppA]
			app.Route.Endpoints = nil
			value.Apps[upgradeTestAppA] = app
		}},
		{name: "identity digest", mutate: func(value *gatewayV2RouteState) { value.Identity.Digest = strings.Repeat("e", 64) }},
		{name: "network broadcast", mutate: func(value *gatewayV2RouteState) { value.Network.ContainerIPv4 = "10.240.0.15" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneGatewayV2RouteState(state)
			test.mutate(&candidate)
			if validGatewayV2RouteState(candidate) {
				t.Fatal("invalid v2 route state was accepted")
			}
		})
	}
}

func TestCommittedMigrationKeepsInitialDigestAsHistoryAndAllowsValidRouteEvolution(t *testing.T) {
	store, state, journal, _ := persistedUpgradeFixture(t)
	for _, transition := range [][2]gatewayMigrationPhase{
		{gatewayPhasePrepared, gatewayPhaseStageIntent},
		{gatewayPhaseStageIntent, gatewayPhaseStaged},
		{gatewayPhaseStaged, gatewayPhaseTransferIntent},
		{gatewayPhaseTransferIntent, gatewayPhaseV2Serving},
		{gatewayPhaseV2Serving, gatewayPhaseCommitted},
	} {
		if _, err := store.transitionMigrationJournal(journal.OperationID, transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	initialDigest := journal.Target.StateDigest
	bindLANForTest(t, &state, upgradeTestAppA, 8100)
	body, _ := json.Marshal(state)
	if err := secretfile.Write(store.v2Path, v2RouteStatePurpose, body); err != nil {
		t.Fatal(err)
	}
	loaded, committed, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || loaded.Apps[upgradeTestAppA].LAN == nil || committed.Target.StateDigest != initialDigest {
		t.Fatalf("post-commit state=%#v journal=%#v err=%v", loaded, committed, err)
	}
	currentDigest, _ := canonicalDigest(loaded)
	if currentDigest == initialDigest {
		t.Fatal("historical initial target digest changed with current route state")
	}

	state.Network.ContainerIPv4 = "10.240.0.3"
	body, _ = json.Marshal(state)
	if err := secretfile.Write(store.v2Path, v2RouteStatePurpose, body); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.loadBoundUpgrade(journal.OperationID); err == nil {
		t.Fatal("post-commit plan drift was accepted")
	}
}

func TestDecideGatewayMigrationRecoveryNeverResumesMutation(t *testing.T) {
	tests := []struct {
		phase    gatewayMigrationPhase
		topology gatewayObservedTopology
		decision gatewayRecoveryDecision
	}{
		{gatewayPhasePrepared, gatewayTopologyExactV1Only, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhasePrepared, gatewayTopologyExactV1WithStage, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhaseStageIntent, gatewayTopologyExactV1Only, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhaseStageIntent, gatewayTopologyExactV1WithStage, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhaseStaged, gatewayTopologyExactV1WithStage, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhaseTransferIntent, gatewayTopologyExactV1WithStage, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhaseTransferIntent, gatewayTopologyExactFinalV2, gatewayRecoveryRecordV2Serving},
		{gatewayPhaseV2Serving, gatewayTopologyExactFinalV2, gatewayRecoveryCommit},
		{gatewayPhaseV2Serving, gatewayTopologyExactV1Only, gatewayRecoveryWriteRollbackIntent},
		{gatewayPhaseRollbackIntent, gatewayTopologyExactV1WithStage, gatewayRecoveryRollbackToV1},
		{gatewayPhaseRollbackIntent, gatewayTopologyExactFinalV2, gatewayRecoveryRollbackToV1},
		{gatewayPhaseRollbackIntent, gatewayTopologyExactV1Only, gatewayRecoveryRecordRolledBack},
		{gatewayPhaseCommitted, gatewayTopologyExactFinalV2, gatewayRecoveryComplete},
		{gatewayPhaseCommitted, gatewayTopologyExactV1Only, gatewayRecoveryTerminalDrift},
		{gatewayPhaseRolledBack, gatewayTopologyExactV1Only, gatewayRecoveryComplete},
		{gatewayPhaseRolledBack, gatewayTopologyExactFinalV2, gatewayRecoveryTerminalDrift},
		{gatewayPhaseUncertain, gatewayTopologyExactV1Only, gatewayRecoveryBlockedUncertain},
		{gatewayPhasePrepared, gatewayTopologyUnknownOrDrift, gatewayRecoveryMarkUncertain},
		{gatewayPhaseTransferIntent, gatewayTopologyUnknownOrDrift, gatewayRecoveryMarkUncertain},
	}
	for _, test := range tests {
		got, err := decideGatewayMigrationRecovery(test.phase, test.topology)
		if err != nil || got != test.decision {
			t.Fatalf("phase=%s topology=%s decision=%s want=%s err=%v", test.phase, test.topology, got, test.decision, err)
		}
	}
	if _, err := decideGatewayMigrationRecovery("future", gatewayTopologyExactV1Only); err == nil {
		t.Fatal("unknown phase was accepted")
	}
	if _, err := decideGatewayMigrationRecovery(gatewayPhasePrepared, "partial"); err == nil {
		t.Fatal("partial topology was accepted")
	}
}

func upgradeTestPreparation(t *testing.T) (routeState, gatewayUpgradePreparation) {
	t.Helper()
	profile := gatewayProfileBinding{
		RevisionID: "44444444-4444-4444-8444-444444444444", RevisionNumber: 7,
		SelectedIPv4: "192.168.50.20", InterfaceID: "windows-interface-guid", PortStart: 8100, PortEnd: 8119,
	}
	digest, err := appaccess.GatewayProfileSpecDigest(appaccess.GatewayProfileSpec{
		SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile.SpecDigest = digest
	input := gatewayUpgradePreparation{
		OperationID: "55555555-5555-4555-8555-555555555555", Profile: profile,
		Network:              gatewayV2NetworkPlan{Subnet: "10.240.0.0/28", GatewayIPv4: "10.240.0.1", ContainerIPv4: "10.240.0.2"},
		SourceIdentityDigest: strings.Repeat("a", 64), LocalHostPort: 8080, ApprovedBy: upgradeTestActor,
	}
	input.ApprovedActionDigest, err = gatewayUpgradeActionDigest(profile, gatewayV2IdentityVersion)
	if err != nil {
		t.Fatal(err)
	}
	return upgradeTestSourceState(), input
}

func upgradeTestSourceState() routeState {
	return routeState{Version: stateVersion, Active: map[string]routeRecord{
		upgradeTestAppB: {Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("web", "server", "net-b", "web-green", 3000, 'b'),
		}},
		upgradeTestAppA: {Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-blue", 4173, 'a'),
			endpoint("api", "server", "net-a", "api-blue", 3000, 'c'),
		}},
	}}
}

func newUpgradeStoreWithV1(t *testing.T, source routeState) *gatewayUpgradeStateStore {
	t.Helper()
	directory, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.save(source); err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayUpgradeStateStore(filepath.Dir(filepath.Dir(directory.root)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func persistedUpgradeFixture(t *testing.T) (*gatewayUpgradeStateStore, gatewayV2RouteState, gatewayMigrationJournal, routeState) {
	t.Helper()
	source, input := upgradeTestPreparation(t)
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	store := newUpgradeStoreWithV1(t, source)
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	return store, state, journal, source
}

func bindLANForTest(t *testing.T, state *gatewayV2RouteState, appID string, port uint16) {
	t.Helper()
	allocationID := uuid.NewString()
	accessRevisionID := uuid.NewString()
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: appID, AllocationID: allocationID, Port: port,
		GatewayProfileRevisionID: state.Profile.RevisionID, GatewayProfileRevisionNumber: state.Profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := state.Apps[appID]
	app.LAN = &gatewayV2LANBinding{
		AccessRevisionID: accessRevisionID, AccessRevisionNumber: 1, AccessSpecDigest: digest,
		AllocationID: allocationID, Port: port, ProfileRevisionID: state.Profile.RevisionID,
		ProfileRevisionNumber: state.Profile.RevisionNumber, ProfileSpecDigest: state.Profile.SpecDigest,
	}
	state.Apps[appID] = app
}

func cloneRouteStateForTest(state routeState) routeState {
	result := routeState{Version: state.Version, Active: cloneRoutes(state.Active)}
	if state.Pending != nil {
		pending := *state.Pending
		pending.Proposed.Endpoints = append([]generatedruntime.RouteEndpoint(nil), pending.Proposed.Endpoints...)
		if pending.Previous != nil {
			previous := *pending.Previous
			previous.Endpoints = append([]generatedruntime.RouteEndpoint(nil), previous.Endpoints...)
			pending.Previous = &previous
		}
		result.Pending = &pending
	}
	return result
}

func cloneRouteRecordPointer(route routeRecord) *routeRecord {
	result := route
	result.Endpoints = append([]generatedruntime.RouteEndpoint(nil), route.Endpoints...)
	return &result
}
