package generatedingress

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentStateFixture struct {
	manager     *Manager
	dataRoot    string
	history     gatewayRebindProtectedIntentHistory
	intent      gatewayRebindProtectedIntent
	receipt     gatewayRebindFinalHandoverTerminalReceipt
	predecessor gatewayV2RouteState
	transfers   []appaccess.GatewayRebindAllocationTransfer
	baseline    gatewayCurrentRouteState
	store       *gatewayCurrentRouteStateStore
}

func newGatewayCurrentStateFixture(t *testing.T) gatewayCurrentStateFixture {
	t.Helper()
	value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
	manager, dataRoot := handoverTestManager(t, value)
	for _, record := range handoverTestSuccessRecords(t, value, initial) {
		store, err := newGatewayRebindProgressStore(dataRoot, record.Generation, record.OperationID, record.Sequence)
		if err != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install terminal progress %d: %v", record.Sequence, err)
		}
	}
	pending, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := gatewayRebindFinalHandoverTerminalReceiptFor(pending, gatewayRebindProgressTimestamp(18))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := newGatewayRebindFinalHandoverTerminalStore(dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || terminal.installExact(context.Background(), receipt) != nil {
		t.Fatalf("install terminal receipt: %v", err)
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Terminals) != 1 {
		t.Fatalf("scan terminal history: intents=%d terminals=%d error=%v", len(history.Intents), len(history.Terminals), err)
	}
	intent := history.Intents[0].Intent
	transfers := gatewayCurrentFixtureTransfers(t, receipt, intent, value.Predecessor.State)
	baseline, err := newGatewayCurrentRouteBaselineFromV1Terminal(intent, receipt, value.Predecessor.State, transfers)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayCurrentRouteStateStore(dataRoot, baseline.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayCurrentStateFixture{manager: manager, dataRoot: dataRoot, history: history,
		intent: intent, receipt: receipt, predecessor: value.Predecessor.State,
		transfers: transfers, baseline: baseline, store: store}
}

func gatewayCurrentFixtureTransfers(t *testing.T, receipt gatewayRebindFinalHandoverTerminalReceipt,
	intent gatewayRebindProtectedIntent, predecessor gatewayV2RouteState,
) []appaccess.GatewayRebindAllocationTransfer {
	t.Helper()
	result := make([]appaccess.GatewayRebindAllocationTransfer, 0, len(intent.Intent.Roster))
	for index, entry := range intent.Intent.Roster {
		app, exists := predecessor.Apps[entry.AppID]
		if !exists || app.LAN == nil {
			t.Fatalf("missing raw predecessor binding for %s", entry.AppID)
		}
		raw := app.LAN
		transfer := appaccess.GatewayRebindAllocationTransfer{
			Version:     appaccess.GatewayRebindTransferVersionV1,
			OperationID: receipt.OperationID, Ordinal: int64(index + 1), AppID: entry.AppID,
			AllocationID: raw.AllocationID, GrantAttemptID: raw.GrantAttemptID,
			SourceBindingDigest: strings.Repeat("a", 64), RosterEntryDigest: receipt.RosterEntryDigests[index],
			SourceProfileRevisionID: raw.ProfileRevisionID, SourceProfileRevisionNumber: raw.ProfileRevisionNumber,
			SourceProfileSpecDigest:        raw.ProfileSpecDigest,
			SuccessorProfileRevisionID:     receipt.SuccessorProfile.RevisionID,
			SuccessorProfileRevisionNumber: receipt.SuccessorProfile.RevisionNumber,
			SuccessorProfileSpecDigest:     receipt.SuccessorProfile.SpecDigest,
			TerminalReceiptDigest:          receipt.Digest,
		}
		var err error
		transfer.TransferDigest, err = appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, transfer)
	}
	return result
}

func TestGatewayCurrentV1TerminalBuildsAdditiveScopedBaseline(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if fixture.baseline.Version != 1 || fixture.baseline.Revision != 1 ||
		fixture.baseline.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
		fixture.baseline.Lineage.TerminalReceiptDigest != fixture.receipt.Digest ||
		len(fixture.baseline.Apps) != len(fixture.predecessor.Apps) {
		t.Fatalf("unexpected baseline: %#v", fixture.baseline)
	}
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.store.load()
	if err != nil || !reflect.DeepEqual(loaded, fixture.baseline) {
		t.Fatalf("load baseline: %v", err)
	}
	wantName, _ := gatewayCurrentRouteStateName(fixture.receipt.Generation, fixture.receipt.OperationID)
	if filepathBase(fixture.store.path) != wantName {
		t.Fatalf("scoped path=%s want=%s", fixture.store.path, wantName)
	}
}

func TestGatewayCurrentPreservesTerminalV1CanonicalShapeAndDigest(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	body, err := json.Marshal(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"claimRequestDigest", "createdAt", "digest", "disposition", "finalBindingDigest",
		"finalConfigDigest", "generation", "operationId", "physicalProof", "planDigest", "predecessor",
		"preparedClaimStateSequence", "preparedDatabaseDigest", "protectedIntentDigest", "purpose", "resources",
		"rosterCount", "rosterDigest", "rosterEntryDigests", "routePlanDigest", "sequenceTwelveDigest",
		"successorIdentity", "successorProfile", "terminalProgressDigest", "terminalProgressPhase",
		"terminalProgressSequence", "version"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("terminal v1 protected JSON shape changed: got %v", keys)
	}
	if fixture.receipt.Version != 1 || fixture.receipt.Purpose != gatewayRebindFinalHandoverTerminalPurpose {
		t.Fatalf("terminal v1 version changed: %#v", fixture.receipt)
	}
	digest, err := gatewayRebindFinalHandoverTerminalDigest(fixture.receipt)
	if err != nil || digest != fixture.receipt.Digest {
		t.Fatalf("terminal v1 canonical digest changed: got=%s want=%s error=%v", digest, fixture.receipt.Digest, err)
	}
}

func TestGatewayCurrentUpdatePreservesOriginAndTerminalReceipt(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	terminalPath := fixture.history.Terminals[0].Store.path
	terminalBefore, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneGatewayCurrentRouteState(fixture.baseline)
	next.Revision++
	next.Digest, err = gatewayCurrentRouteStateDigest(next)
	if err != nil || fixture.store.saveNext(fixture.baseline, next) != nil {
		t.Fatalf("save current revision: %v", err)
	}
	terminalAfter, err := os.ReadFile(terminalPath)
	if err != nil || !reflect.DeepEqual(terminalAfter, terminalBefore) {
		t.Fatalf("terminal receipt changed after current write: %v", err)
	}
	if !sameGatewayCurrentRouteOrigin(fixture.baseline, next) || next.Lineage != fixture.baseline.Lineage {
		t.Fatal("normal current write changed immutable origin")
	}
}

func TestGatewayRebindCheckpointSurvivesLaterCurrentWrites(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	operationID := uuid.NewString()
	checkpoint, err := newGatewayRebindPredecessorCheckpoint(fixture.receipt.Generation+1,
		operationID, fixture.baseline.Lineage, nil, &fixture.baseline)
	if err != nil {
		t.Fatal(err)
	}
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install checkpoint: %v", err)
	}
	next := cloneGatewayCurrentRouteState(fixture.baseline)
	next.Revision++
	next.Digest, _ = gatewayCurrentRouteStateDigest(next)
	if err := fixture.store.saveNext(fixture.baseline, next); err != nil {
		t.Fatal(err)
	}
	retained, err := checkpointStore.load()
	if err != nil || !reflect.DeepEqual(retained, checkpoint) ||
		retained.SourceStateRevision != fixture.baseline.Revision || retained.SourceStateDigest != fixture.baseline.Digest {
		t.Fatalf("retained checkpoint changed after later current write: %v", err)
	}
}

func TestGatewayRebindCheckpointRetainsAbortedUpgradeHistoryAfterRouteDisable(t *testing.T) {
	fixture, fake := installGatewayRebindFinalHandoverFixture(t)
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := newGatewayRebindPredecessorCheckpoint(fixture.intent.Generation,
		fixture.intent.OperationID, lineage, &history.Predecessor.State, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(
		fixture.predecessor.manager.options.DataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install frozen upgrade checkpoint: %v", err)
	}

	fake.failAt = "create_final"
	if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(),
		fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(13), nil); err == nil {
		t.Fatal("required handover failure was not reached")
	}
	fake.manager = newGatewayRebindStageContainerManager(t, fixture)
	if err := fake.manager.rollbackGatewayRebindFinalHandoverWithDriver(context.Background(),
		fixture.predecessor.repository, fake, gatewayRebindProgressTimestamp(30), nil); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	aborted := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalAbort)

	current := aborted.Predecessor.State
	var request GatewayV2LANDisableRequest
	for appID, app := range current.Apps {
		if app.LAN != nil {
			request = disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(appID, *app.LAN))
			break
		}
	}
	if request.OperationID == "" {
		t.Fatal("upgrade source has no LAN binding to disable")
	}
	pending, withdrawn, err := gatewayV2LANDisablePendingState(current, request)
	if err != nil || fixture.predecessor.store.saveCommittedV2State(pending, aborted.Predecessor.Journal) != nil ||
		fixture.predecessor.store.saveCommittedV2State(withdrawn, aborted.Predecessor.Journal) != nil {
		t.Fatalf("ordinary route disable after abort: %v", err)
	}

	retained, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(retained.Terminals) != 1 ||
		retained.Terminals[0].Receipt.Disposition != gatewayRebindFinalHandoverTerminalAbort ||
		!reflect.DeepEqual(retained.Checkpoints[0].Checkpoint, checkpoint) {
		t.Fatalf("scan retained abort after route update: terminals=%d error=%v", len(retained.Terminals), err)
	}
	if reflect.DeepEqual(retained.Predecessor.State, *checkpoint.UpgradeState) {
		t.Fatal("test did not advance the live source route state")
	}
}

func TestGatewayCurrentSelectionDoesNotAdvanceOnReceiptAlone(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(fixture.history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	profile := gatewayCurrentFixtureProfile(lineage, fixture.predecessor.Profile)
	authority := gatewayCurrentAuthority(lineage)
	snapshot := appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: &profile, CurrentSource: &authority}
	selection, err := fixture.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil || selection.Kind != gatewayCurrentSelectionUpgrade || selection.Upgrade == nil || selection.State != nil {
		t.Fatalf("receipt without SQL commit selected successor: kind=%s error=%v", selection.Kind, err)
	}
}

func TestGatewayCurrentSelectionUsesCommittedHistoryWhileNextRebindIsPrepared(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	profile := gatewayCurrentFixtureProfile(fixture.baseline.Lineage, fixture.baseline.Profile)
	authority := gatewayCurrentAuthority(fixture.baseline.Lineage)
	committed := appaccess.GatewayRebindHistoryEntry{
		Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: 1, Legacy: &appaccess.GatewayRebindClaim{
			Spec: appaccess.GatewayRebindSpec{OperationID: fixture.receipt.OperationID},
		}},
		Events: []appaccess.GatewayRebindEvent{{OperationID: fixture.receipt.OperationID,
			Sequence: 4, State: appaccess.GatewayRebindDatabaseCommitted}},
		Transfers: append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers...),
	}
	activeOperationID := uuid.NewString()
	active := appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
		SpecVersion: appaccess.GatewayRebindSpecVersionV2,
		V2:          &appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{OperationID: activeOperationID}},
	}}
	snapshot := appaccess.GatewayRebindRecoverySnapshot{
		History: []appaccess.GatewayRebindHistoryEntry{committed}, Active: &active,
		CurrentProfile: &profile, CurrentSource: &authority,
		CurrentTransfers: append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers...),
		Phase:            appaccess.GatewayRebindPrepared, DatabaseCommitObserved: false,
	}
	selection, err := fixture.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil ||
		selection.Lineage.OperationID != fixture.receipt.OperationID {
		t.Fatalf("select committed A while B prepared: kind=%s error=%v", selection.Kind, err)
	}
}

func TestGatewayCurrentTransferHistorySurvivesDisableAndRejectsMissingManifest(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	disabled := cloneGatewayCurrentRouteState(fixture.baseline)
	removed := false
	for appID, app := range disabled.Apps {
		if app.LAN == nil {
			continue
		}
		app.LAN = nil
		disabled.Apps[appID] = app
		removed = true
		break
	}
	if !removed {
		t.Fatal("fixture has no transferred LAN binding")
	}
	disabled.Revision++
	disabled.Digest, _ = gatewayCurrentRouteStateDigest(disabled)
	if !validGatewayCurrentRouteState(disabled) || !gatewayCurrentTransfersMatchState(fixture.transfers, disabled) {
		t.Fatal("retained immutable transfer history was rejected after a valid disable")
	}
	missing := append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers[1:]...)
	missingDigest, err := appaccess.GatewayRebindTransferManifestDigest(missing)
	if err != nil {
		t.Fatal(err)
	}
	if missingDigest == disabled.TransferManifestDigest {
		t.Fatal("missing SQL transfer preserved complete baseline manifest digest")
	}
}

func TestGatewayV2LANEffectiveBindingProofMatchesSQLAuthorityAndChain(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	var binding *gatewayCurrentLANBinding
	for _, app := range fixture.baseline.Apps {
		if app.LAN != nil {
			binding = app.LAN
			break
		}
	}
	proof, err := gatewayCurrentEffectiveProof(fixture.baseline, binding)
	if err != nil || binding == nil || binding.Transfer == nil {
		t.Fatalf("build effective proof: %v", err)
	}
	profile := gatewayCurrentFixtureProfile(fixture.baseline.Lineage, fixture.baseline.Profile)
	resolution := appaccess.GatewayBindingResolution{
		EffectiveProfile: profile, CurrentGatewaySource: gatewayCurrentAuthority(fixture.baseline.Lineage),
		TransferChain:          []appaccess.GatewayRebindAllocationTransfer{*binding.Transfer},
		TransferChainTipDigest: binding.Transfer.TransferDigest,
		TerminalReceiptDigest:  fixture.receipt.Digest,
	}
	if !proof.MatchesResolution(resolution) {
		t.Fatal("exact effective SQL projection did not match protected proof")
	}
	stale := resolution
	stale.TransferChainTipDigest = strings.Repeat("f", 64)
	if proof.MatchesResolution(stale) {
		t.Fatal("stale transfer chain tip matched protected proof")
	}
	stale = resolution
	stale.CurrentGatewaySource.OperationID = uuid.NewString()
	if proof.MatchesResolution(stale) {
		t.Fatal("different SQL current operation matched protected proof")
	}
}

func TestGatewayV2LANEffectiveBindingProofMatchesNativeUpgradeAuthority(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	proof, err := gatewayUpgradeEffectiveProof(fixture.history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(fixture.history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	resolution := appaccess.GatewayBindingResolution{
		EffectiveProfile:     gatewayCurrentFixtureProfile(lineage, fixture.predecessor.Profile),
		CurrentGatewaySource: gatewayCurrentAuthority(lineage),
	}
	if !proof.MatchesResolution(resolution) {
		t.Fatal("native upgrade proof did not match its exact SQL authority projection")
	}
}

func TestGatewayV2LANEffectiveBindingProofIsPresentOnNativeObservation(t *testing.T) {
	manager, store, journal, request, _ := grantedLANForDisable(t)
	observation, err := manager.ObserveGatewayV2LAN(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	state, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retainedJournal, journal) {
		t.Fatalf("load committed native upgrade: %v", err)
	}
	selection := gatewayUpgradeGenerationSelection{Store: store, Generation: store.generation,
		State: state, Journal: journal, Existing: true, operationID: state.OperationID}
	want, err := gatewayUpgradeEffectiveProof(selection)
	if err != nil || !reflect.DeepEqual(observation.EffectiveBinding, want) {
		t.Fatalf("native observation effective proof mismatch: error=%v got=%#v want=%#v",
			err, observation.EffectiveBinding, want)
	}
}

func gatewayCurrentFixtureProfile(lineage appaccess.GatewayCurrentLineageRef,
	profile gatewayProfileBinding,
) appaccess.GatewayProfileRevision {
	return appaccess.GatewayProfileRevision{ID: lineage.ProfileRevisionID, RevisionNumber: lineage.ProfileRevisionNumber,
		Spec: appaccess.GatewayProfileSpec{SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID,
			PortStart: profile.PortStart, PortEnd: profile.PortEnd}, SpecDigest: lineage.ProfileSpecDigest}
}

func filepathBase(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' || path[index] == '\\' {
			return path[index+1:]
		}
	}
	return path
}
