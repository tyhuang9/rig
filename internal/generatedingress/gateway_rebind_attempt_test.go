package generatedingress

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindAttemptViewPreservesLegacySourceWithoutChangingFormat(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := fixture.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	view, err := newGatewayRebindAttemptViewLegacy(intent, source, predecessor)
	if err != nil {
		t.Fatal(err)
	}
	if view.Format != gatewayRebindAttemptLegacyV1 || view.LegacyIntent == nil || view.TypedIntent != nil ||
		view.LegacyIntent.Digest != intent.Digest || view.Source.Upgrade == nil || view.Source.Rebind != nil ||
		view.Source.FrozenRef != nil || view.Source.CheckpointDigest != "" ||
		view.Source.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		view.Source.Lineage.ProtectedJournalDigest != intent.Intent.Predecessor.JournalDigest ||
		view.Source.StateVersion != 2 || view.Source.StateRevision != 0 ||
		view.Source.StateDigest != intent.Intent.Predecessor.StateDigest || len(view.Roster) != len(intent.Intent.Roster) {
		t.Fatalf("legacy attempt projection mismatch: %#v", view)
	}
	for _, entry := range view.Roster {
		if entry.SourceProfileRevisionID != predecessor.State.Profile.RevisionID ||
			entry.SourceProfileRevisionNumber != predecessor.State.Profile.RevisionNumber ||
			entry.SourceProfileSpecDigest != predecessor.State.Profile.SpecDigest || entry.PredecessorTransferDigest != nil {
			t.Fatalf("legacy roster source projection mismatch: %#v", entry)
		}
	}
	if !reflect.DeepEqual(*view.LegacyIntent, intent) {
		t.Fatal("legacy adapter changed the immutable protected intent")
	}

	changed := source
	changed.Version++
	if _, err := newGatewayRebindAttemptViewLegacy(intent, changed, predecessor); err == nil {
		t.Fatal("legacy adapter accepted a source that disagrees with the protected upgrade")
	}
}

func TestGatewayRebindAttemptViewUsesTypedCheckpointAndActualPriorReceipt(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	receipt := fixture.receipt
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(receipt)
	if err != nil {
		t.Fatal(err)
	}
	selection := gatewayCurrentSelection{
		Kind: gatewayCurrentSelectionRebind, Lineage: fixture.baseline.Lineage,
		State: &state, Receipt: &receipt, Terminal: &terminal, Store: fixture.store,
	}
	view, err := newGatewayRebindAttemptViewV2(intent, checkpoint, selection)
	if err != nil {
		t.Fatal(err)
	}
	if view.Format != gatewayRebindAttemptTypedV2 || view.LegacyIntent != nil || view.TypedIntent == nil ||
		view.Source.FrozenRef == nil || *view.Source.FrozenRef != checkpoint.sourceRef() ||
		view.Source.Upgrade != nil || view.Source.Rebind == nil ||
		view.Source.Rebind.Terminal.Format != gatewayRebindAttemptTerminalLegacyV1 ||
		view.Source.Rebind.Terminal.LegacyReceipt == nil ||
		view.Source.Rebind.Terminal.LegacyReceipt.Digest != fixture.receipt.Digest ||
		view.Source.Rebind.Terminal.Digest != fixture.baseline.Lineage.TerminalReceiptDigest ||
		view.Source.Lineage.ProtectedJournalDigest != "" ||
		view.Source.Lineage.ProtectedIntentDigest != fixture.receipt.ProtectedIntentDigest {
		t.Fatalf("typed attempt source projection mismatch: %#v", view.Source)
	}
	forgedLineage := fixture.baseline.Lineage
	forgedLineage.OperationID = uuid.NewString()
	if gatewayRebindAttemptTerminalMatchesLineage(view.Source.Rebind.Terminal, forgedLineage) {
		t.Fatal("validated terminal projection accepted a different self-consistent lineage identity")
	}
	for index, entry := range view.Roster {
		want := intent.Roster[index]
		if entry.SourceProfileRevisionID != want.SourceProfileRevisionID ||
			entry.SourceProfileRevisionNumber != want.SourceProfileRevisionNumber ||
			entry.SourceProfileSpecDigest != want.SourceProfileSpecDigest ||
			(entry.PredecessorTransferDigest == nil) != (want.PredecessorTransferDigest == nil) ||
			entry.PredecessorTransferDigest != nil && *entry.PredecessorTransferDigest != *want.PredecessorTransferDigest {
			t.Fatalf("typed roster projection mismatch: got=%#v want=%#v", entry, want)
		}
	}

	drifted := selection
	driftedState := cloneGatewayCurrentRouteState(state)
	driftedState.Revision++
	drifted.State = &driftedState
	if _, err := newGatewayRebindAttemptViewV2(intent, checkpoint, drifted); err == nil {
		t.Fatal("typed adapter accepted current state that drifted from the frozen checkpoint")
	}
	wrongReceipt := selection
	changedReceipt := receipt
	changedReceipt.Digest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	changedTerminal := terminal
	changedTerminal.LegacyReceipt = &changedReceipt
	wrongReceipt.Receipt = &changedReceipt
	wrongReceipt.Terminal = &changedTerminal
	if _, err := newGatewayRebindAttemptViewV2(intent, checkpoint, wrongReceipt); err == nil {
		t.Fatal("typed adapter accepted a substituted prior terminal receipt")
	}
	missingTerminal := selection
	missingTerminal.Terminal = nil
	if _, err := newGatewayRebindAttemptViewV2(intent, checkpoint, missingTerminal); err == nil {
		t.Fatal("typed adapter accepted a legacy receipt without its canonical terminal selection")
	}
}

func TestGatewayRebindAttemptViewAcceptsGrantedUpgradeCheckpointWithoutInitialStateRewrite(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	source, err := fixture.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(predecessor)
	if err != nil {
		t.Fatal(err)
	}
	operationID := snapshot.Claims[0].Claim.Spec.OperationID
	checkpoint, err := newGatewayRebindPredecessorCheckpoint(predecessor.Generation+1, operationID,
		lineage, &predecessor.State, nil)
	if err != nil {
		t.Fatal(err)
	}
	roster := make([]appaccess.GatewayRebindRosterEntryV2, len(snapshot.Claims[0].Roster))
	for index, legacy := range snapshot.Claims[0].Roster {
		entry := appaccess.GatewayRebindRosterEntryV2{
			Version: appaccess.GatewayRebindRosterVersionV2, OperationID: operationID,
			Ordinal: legacy.Ordinal, AppID: legacy.AppID, AllocationID: legacy.AllocationID, Port: legacy.Port,
			AllocationOwnerOperationID: legacy.AllocationOwnerOperationID, AllocationState: legacy.AllocationState,
			AccessRevisionID: legacy.AccessRevisionID, AccessRevisionNumber: legacy.AccessRevisionNumber,
			AccessSpecDigest: legacy.AccessSpecDigest, GrantAttemptID: legacy.GrantAttemptID,
			GrantStateSequence:          legacy.GrantStateSequence,
			GrantProtectedStateDigest:   legacy.GrantProtectedStateDigest,
			SourceProfileRevisionID:     predecessor.State.Profile.RevisionID,
			SourceProfileRevisionNumber: predecessor.State.Profile.RevisionNumber,
			SourceProfileSpecDigest:     predecessor.State.Profile.SpecDigest,
			ServingDeploymentID:         legacy.ServingDeploymentID, ServingReleaseID: legacy.ServingReleaseID,
			ServingSlot: legacy.ServingSlot,
			// Runtime heads are SQL evidence. A later redeploy can advance this
			// generation without rewriting the protected route.
			RouteGeneration: legacy.RouteGeneration + 1,
		}
		entry.EntryDigest, err = appaccess.GatewayRebindRosterEntryV2Digest(entry)
		if err != nil {
			t.Fatal(err)
		}
		roster[index] = entry
	}
	legacyIntent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, checkpoint.Generation)
	if err != nil {
		t.Fatal(err)
	}
	intent := gatewayRebindAttemptTypedIntentForCheckpoint(t, checkpoint, roster,
		snapshot.Claims[0].Claim.Spec.SuccessorProfileRevisionID,
		snapshot.Claims[0].Claim.Spec.SuccessorProfileRevisionNumber,
		snapshot.Claims[0].Claim.Spec.SuccessorProfileOperationID,
		snapshot.Claims[0].Claim.Spec.SuccessorProfile, legacyIntent.NetworkObservation)
	state := predecessor
	routes := cloneRouteState(source)
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: lineage,
		Upgrade: &state, UpgradeSource: &routes}
	view, err := newGatewayRebindAttemptViewV2(intent, checkpoint, selection)
	if err != nil {
		t.Fatal(err)
	}
	if view.Source.Upgrade == nil || view.Source.Rebind != nil || len(view.Roster) != len(roster) ||
		view.Roster[0].RouteGeneration != snapshot.Claims[0].Roster[0].RouteGeneration+1 {
		t.Fatalf("typed upgrade attempt lost granted/runtime-head evidence: %#v", view)
	}
	if gatewayV2InitialStateMatchesSource(predecessor.State, source) {
		t.Fatal("fixture unexpectedly remained an initial no-LAN upgrade state")
	}
}

func gatewayRebindAttemptTypedFixture(t *testing.T,
	fixture gatewayCurrentStateFixture,
) (gatewayRebindPredecessorCheckpoint, gatewayRebindProtectedIntentV2) {
	t.Helper()
	operationID := uuid.NewString()
	checkpoint, err := newGatewayRebindPredecessorCheckpoint(fixture.baseline.Lineage.ProtectedGeneration+1,
		operationID, fixture.baseline.Lineage, nil, &fixture.baseline)
	if err != nil {
		t.Fatal(err)
	}
	roster := make([]appaccess.GatewayRebindRosterEntryV2, len(fixture.intent.Intent.Roster))
	for index, legacy := range fixture.intent.Intent.Roster {
		current := fixture.baseline.Apps[legacy.AppID]
		if current.LAN == nil || current.LAN.Transfer == nil {
			t.Fatalf("fixture app %s has no retained transfer", legacy.AppID)
		}
		predecessorTransfer := current.LAN.Transfer.TransferDigest
		entry := appaccess.GatewayRebindRosterEntryV2{
			Version: appaccess.GatewayRebindRosterVersionV2, OperationID: operationID,
			Ordinal: legacy.Ordinal, AppID: legacy.AppID, AllocationID: legacy.AllocationID, Port: legacy.Port,
			AllocationOwnerOperationID: legacy.AllocationOwnerOperationID, AllocationState: legacy.AllocationState,
			AccessRevisionID: legacy.AccessRevisionID, AccessRevisionNumber: legacy.AccessRevisionNumber,
			AccessSpecDigest: legacy.AccessSpecDigest, GrantAttemptID: legacy.GrantAttemptID,
			GrantStateSequence:          legacy.GrantStateSequence,
			GrantProtectedStateDigest:   legacy.GrantProtectedStateDigest,
			SourceProfileRevisionID:     current.LAN.Raw.ProfileRevisionID,
			SourceProfileRevisionNumber: current.LAN.Raw.ProfileRevisionNumber,
			SourceProfileSpecDigest:     current.LAN.Raw.ProfileSpecDigest,
			PredecessorTransferDigest:   &predecessorTransfer,
			ServingDeploymentID:         legacy.ServingDeploymentID, ServingReleaseID: legacy.ServingReleaseID,
			ServingSlot: legacy.ServingSlot, RouteGeneration: legacy.RouteGeneration,
		}
		entry.EntryDigest, err = appaccess.GatewayRebindRosterEntryV2Digest(entry)
		if err != nil {
			t.Fatal(err)
		}
		roster[index] = entry
	}
	profileSpec := appaccess.GatewayProfileSpec{
		SelectedIPv4: fixture.baseline.Profile.SelectedIPv4, InterfaceID: fixture.baseline.Profile.InterfaceID,
		PortStart: fixture.baseline.Profile.PortStart, PortEnd: fixture.baseline.Profile.PortEnd,
	}
	intent := gatewayRebindAttemptTypedIntentForCheckpoint(t, checkpoint, roster, uuid.NewString(),
		fixture.baseline.Profile.RevisionNumber+1, uuid.NewString(), profileSpec, fixture.intent.NetworkObservation)
	return checkpoint, intent
}

func gatewayRebindAttemptTypedIntentForCheckpoint(t *testing.T, checkpoint gatewayRebindPredecessorCheckpoint,
	roster []appaccess.GatewayRebindRosterEntryV2, successorRevisionID string, successorRevisionNumber int64,
	successorOperationID string, profileSpec appaccess.GatewayProfileSpec,
	network gatewayRebindSuccessorNetworkObservation,
) gatewayRebindProtectedIntentV2 {
	t.Helper()
	runtimeHeads := gatewayRebindAttemptRuntimeHeads(t, checkpoint, roster)
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(roster)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHeadsDigest, err := appaccess.GatewayRebindRuntimeHeadsV2Digest(checkpoint.OperationID, runtimeHeads)
	if err != nil {
		t.Fatal(err)
	}
	claim := appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{
		Version: appaccess.GatewayRebindSpecVersionV2, OperationID: checkpoint.OperationID,
		Predecessor: checkpoint.sourceRef(), SuccessorProtectedGeneration: checkpoint.Generation,
		SuccessorProfileRevisionID:     successorRevisionID,
		SuccessorProfileRevisionNumber: successorRevisionNumber,
		SuccessorProfileOperationID:    successorOperationID, SuccessorProfile: profileSpec,
		RosterVersion: appaccess.GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: int64(len(roster)),
		RuntimeHeadsVersion: appaccess.GatewayRebindRuntimeHeadsVersionV1,
		RuntimeHeadsDigest:  runtimeHeadsDigest, RuntimeHeadsCount: int64(len(runtimeHeads)),
	}, State: appaccess.GatewayRebindPrepared, StateSequence: 1}
	claim.RebindApproval = appaccess.Approval{Action: appaccess.ActionRebindGateway, ActorID: uuid.NewString()}
	claim.RebindApproval.SpecDigest, err = appaccess.GatewayRebindSpecV2Digest(claim.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim.ConfigureApproval = appaccess.Approval{Action: appaccess.ActionConfigureGateway, ActorID: uuid.NewString()}
	claim.ConfigureApproval.SpecDigest, err = appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	claim.RequestDigest, err = canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpecV2 `json:"spec"`
		Rebind    appaccess.Approval            `json:"rebindApproval"`
		Configure appaccess.Approval            `json:"configureApproval"`
	}{claim.Spec, claim.RebindApproval, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
	claim.SuccessorProfileRequestDigest, err = canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{claim.Spec.Predecessor.Lineage.ProfileRevisionNumber, claim.Spec.SuccessorProfile, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
	claim.RebindApprovedAt = time.Unix(1, 0).UTC()
	claim.ConfigureApprovedAt = time.Unix(2, 0).UTC()
	claim.CreatedAt, claim.UpdatedAt = time.Unix(3, 0).UTC(), time.Unix(3, 0).UTC()
	network.OperationID = checkpoint.OperationID
	network.ClaimRequestDigest = claim.RequestDigest
	network.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
	intent, err := newGatewayRebindProtectedIntentV2(claim, roster, runtimeHeads, checkpoint, network)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func gatewayRebindAttemptRuntimeHeads(t *testing.T, checkpoint gatewayRebindPredecessorCheckpoint,
	roster []appaccess.GatewayRebindRosterEntryV2,
) []appaccess.GatewayRebindRuntimeHead {
	t.Helper()
	apps, _, ok := gatewayRebindTypedCheckpointApps(checkpoint)
	if !ok {
		t.Fatal("typed fixture checkpoint has no apps")
	}
	entries := make(map[string]appaccess.GatewayRebindRosterEntryV2, len(roster))
	for _, entry := range roster {
		entries[entry.AppID] = entry
	}
	appIDs := make([]string, 0, len(apps))
	for appID := range apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	heads := make([]appaccess.GatewayRebindRuntimeHead, 0, len(appIDs))
	for index, appID := range appIDs {
		app := apps[appID]
		head := appaccess.GatewayRebindRuntimeHead{AppID: appID,
			DeploymentID: uuid.NewSHA1(uuid.Nil, []byte("typed-head-deployment-"+appID)).String(),
			ReleaseID:    uuid.NewSHA1(uuid.Nil, []byte("typed-head-release-"+appID)).String(),
			Slot:         string(app.Route.Slot), Generation: int64(index + 1), UpdatedAt: time.Unix(int64(index+1), 0).UTC()}
		if entry, exists := entries[appID]; exists {
			head.DeploymentID, head.ReleaseID, head.Slot, head.Generation = entry.ServingDeploymentID,
				entry.ServingReleaseID, entry.ServingSlot, entry.RouteGeneration
		}
		heads = append(heads, head)
	}
	return heads
}
