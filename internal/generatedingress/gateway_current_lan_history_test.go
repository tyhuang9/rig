package generatedingress

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANStartupRequiresRetainedHistoricalProof(t *testing.T) {
	f, _, _, physical := gatewayRebindCurrentStartupFixture(t)
	grants := gatewayCurrentStartupGrants(t, f.baseline)
	cleared := cloneGatewayCurrentRouteState(f.baseline)
	disables := make([]GatewayV2LANDisableStartupClaim, len(grants))
	for index := range grants {
		grant := &grants[index]
		request := disableRequestForGrant(t, grant.Request)
		request.OperationID = fmt.Sprintf("62626262-6262-4262-8262-%012d", index+1)
		grant.DisableIntentOperationID = request.OperationID
		grant.RetainedBinding, grant.CurrentBinding = grant.CurrentBinding, nil
		disables[index] = GatewayV2LANDisableStartupClaim{Request: request,
			State: appaccess.AppAccessDisableCommitted, StateSequence: 3, ClearAcknowledged: true,
			RetainedBinding: grant.RetainedBinding}
		app := cleared.Apps[grant.Request.AppID]
		app.LAN = nil
		cleared.Apps[grant.Request.AppID] = app
	}
	cleared.Revision++
	cleared.Digest, _ = gatewayCurrentRouteStateDigest(cleared)
	if err := f.store.saveNext(f.baseline, cleared); err != nil {
		t.Fatal(err)
	}
	physical.attest = gatewayCurrentPhysicalAttestationFixture(t, cleared, physical.attest.Terminal, gatewayCurrentPhysicalStableServing)
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || got.Disposition != GatewayV2LANStartupNormal {
		t.Fatalf("exact cleared history refused: %+v %v", got, err)
	}
	for _, name := range []string{"native grant under original profile", "native grant under rebound profile"} {
		t.Run(name, func(t *testing.T) {
			profile := cleared.Profile
			lineage := cleared.Lineage
			if name == "native grant under original profile" {
				profile = f.predecessor.Profile
				lineage, err = gatewayUpgradeCurrentLineage(f.history.Predecessor)
				if err != nil {
					t.Fatal(err)
				}
			}
			requestState := cloneGatewayCurrentRouteState(cleared)
			requestState.Profile = profile
			request := routeOperationNativeGrantRequest(t, requestState, "64646464-6464-4464-8464-646464646464")
			disable := disableRequestForGrant(t, request)
			disable.OperationID = "65656565-6565-4565-8565-656565656565"
			projection := &GatewayV2LANStartupBindingProjection{EffectiveProfile: GatewayV2ProfileBinding(profile),
				GatewaySource: gatewayCurrentAuthority(lineage), TerminalReceiptDigest: lineage.TerminalReceiptDigest}
			withGrant := append(append([]GatewayV2LANStartupClaim(nil), grants...), GatewayV2LANStartupClaim{
				Request: request, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
				RetainedBinding: projection, DisableIntentOperationID: disable.OperationID})
			withDisable := append(append([]GatewayV2LANDisableStartupClaim(nil), disables...), GatewayV2LANDisableStartupClaim{
				Request: disable, State: appaccess.AppAccessDisableCommitted, StateSequence: 3,
				RetainedBinding: projection, ClearAcknowledged: true})
			got, err := f.manager.InspectGatewayV2LANAccessStartup(context.Background(), withGrant, withDisable)
			if err != nil || got.Disposition != GatewayV2LANStartupNormal {
				t.Fatalf("native historical grant with no transfer row refused: %+v %v", got, err)
			}
		})
	}
	for _, name := range []string{"rehashed false transfer", "rehashed false receipt", "unretained source", "omitted complete transfer chain", "missing retained authority"} {
		t.Run(name, func(t *testing.T) {
			changedGrants := append([]GatewayV2LANStartupClaim(nil), grants...)
			changedDisables := append([]GatewayV2LANDisableStartupClaim(nil), disables...)
			projection := *grants[0].RetainedBinding
			projection.TransferChain = append([]appaccess.GatewayRebindAllocationTransfer(nil), projection.TransferChain...)
			if len(projection.TransferChain) == 0 {
				t.Fatal("fixture requires a transferred historical grant")
			}
			last := &projection.TransferChain[len(projection.TransferChain)-1]
			switch name {
			case "rehashed false transfer":
				last.SourceBindingDigest = strings.Repeat("f", 64)
			case "rehashed false receipt":
				last.TerminalReceiptDigest = strings.Repeat("f", 64)
				projection.TerminalReceiptDigest = last.TerminalReceiptDigest
				projection.GatewaySource.TerminalReceiptDigest = last.TerminalReceiptDigest
			case "unretained source":
				last.OperationID = "63636363-6363-4363-8363-636363636363"
				projection.GatewaySource.OperationID = last.OperationID
			case "omitted complete transfer chain":
				lineage, err := gatewayUpgradeCurrentLineage(f.history.Predecessor)
				if err != nil {
					t.Fatal(err)
				}
				projection = GatewayV2LANStartupBindingProjection{
					EffectiveProfile: GatewayV2ProfileBinding(f.predecessor.Profile), GatewaySource: gatewayCurrentAuthority(lineage)}
			}
			if len(projection.TransferChain) != 0 {
				last.TransferDigest, err = appaccess.GatewayRebindAllocationTransferDigest(*last)
				if err != nil {
					t.Fatal(err)
				}
				projection.TransferChainTipDigest = last.TransferDigest
			}
			changedGrants[0].RetainedBinding, changedDisables[0].RetainedBinding = &projection, &projection
			if name == "missing retained authority" {
				changedGrants[0].RetainedBinding, changedDisables[0].RetainedBinding = nil, nil
			}
			if _, err := validateGatewayV2LANAccessStartupClaims(changedGrants, changedDisables); err != nil {
				t.Fatalf("negative case must remain structurally valid: %v", err)
			}
			calls := physical.calls
			got, err := f.manager.InspectGatewayV2LANAccessStartup(context.Background(), changedGrants, changedDisables)
			if err == nil || got.Disposition != "" || physical.calls != calls {
				t.Fatalf("unproved retained history reached physical inspection: %+v calls=%d error=%v", got, physical.calls-calls, err)
			}
		})
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	loaded, loadErr := f.store.load()
	if err != nil || loadErr != nil || !sameGatewayHistorySnapshot(before, after) || !reflect.DeepEqual(loaded, cleared) ||
		physical.applyCalls != 0 || physical.restoreCalls != 0 || physical.stopCalls != 0 {
		t.Fatal("historical proof inspection changed protected history or physical state")
	}
}

func TestGatewayCurrentLANRetainedHistoryUsesTypedTerminalAndRechecksOldOrigin(t *testing.T) {
	f, snapshot, _, physical := gatewayRebindCurrentStartupFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, f)
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, resources, proof := gatewayRebindTypedCompleteProgressFixture(t, f, checkpoint, intent, first)
	progress := installGatewayRebindTypedAttemptForTerminal(t, f, checkpoint, intent, resources, proof)
	receipt, err := newGatewayRebindCommitTerminalV2(intent, progress, resources, proof, time.Unix(21, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	terminalStore, err := newGatewayRebindTerminalStoreV2(f.dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || terminalStore.installExact(context.Background(), receipt) != nil {
		t.Fatalf("install typed terminal: %v", err)
	}
	transfers, err := newGatewayRebindTransfersV2(intent, receipt, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := newGatewayCurrentRouteBaselineFromV2Terminal(intent, receipt, checkpoint, transfers)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayCurrentRouteStateStore(f.dataRoot, baseline.Lineage)
	if err != nil || store.installBaseline(baseline) != nil {
		t.Fatalf("install typed current state: %v", err)
	}
	claim := intent.Claim
	claim.State, claim.StateSequence = appaccess.GatewayRebindCommitted, 4
	event := appaccess.GatewayRebindEvent{OperationID: receipt.OperationID, State: appaccess.GatewayRebindDatabaseCommitted, Sequence: 3}
	source := gatewayCurrentAuthority(baseline.Lineage)
	profile := gatewayCurrentFixtureProfile(baseline.Lineage, baseline.Profile)
	snapshot.History = append(snapshot.History, appaccess.GatewayRebindHistoryEntry{
		Claim:  appaccess.GatewayRebindClaimRecord{SpecVersion: appaccess.GatewayRebindSpecVersionV2, V2: &claim},
		Events: []appaccess.GatewayRebindEvent{event}, Transfers: transfers})
	snapshot.CurrentSource, snapshot.CurrentProfile = &source, &profile
	snapshot.CurrentDatabaseCommittedEvent, snapshot.CurrentTransfers = &event, transfers
	f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		return snapshot, nil
	})
	cleared := cloneGatewayCurrentRouteState(baseline)
	var grants []GatewayV2LANStartupClaim
	var disables []GatewayV2LANDisableStartupClaim
	for appID, app := range baseline.Apps {
		if app.LAN == nil {
			continue
		}
		var chain []appaccess.GatewayRebindAllocationTransfer
		for _, previous := range f.transfers {
			if previous.AppID == appID {
				chain = append(chain, previous)
			}
		}
		chain = append(chain, *app.LAN.Transfer)
		projection := gatewayCurrentBindingProjection(t, baseline, app.LAN, chain)
		request := gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw)
		disable := disableRequestForGrant(t, request)
		disable.OperationID = fmt.Sprintf("67676767-6767-4676-8676-%012d", len(grants)+1)
		grants = append(grants, GatewayV2LANStartupClaim{Request: request, State: appaccess.AppAccessGrantCommitted,
			StateSequence: 4, DisableIntentOperationID: disable.OperationID, RetainedBinding: &projection})
		disables = append(disables, GatewayV2LANDisableStartupClaim{Request: disable, State: appaccess.AppAccessDisableCommitted,
			StateSequence: 3, ClearAcknowledged: true, RetainedBinding: &projection})
		app.LAN = nil
		cleared.Apps[appID] = app
	}
	cleared.Revision++
	cleared.Digest, _ = gatewayCurrentRouteStateDigest(cleared)
	if err := store.saveNext(baseline, cleared); err != nil {
		t.Fatal(err)
	}
	terminal, err := newGatewayRebindAttemptTerminalViewV2(receipt)
	if err != nil {
		t.Fatal(err)
	}
	physical.attest = gatewayCurrentPhysicalAttestationFixture(t, cleared, terminal, gatewayCurrentPhysicalStableServing)
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || got.Disposition != GatewayV2LANStartupNormal {
		t.Fatalf("complete legacy-to-typed retained chain refused: %+v %v", got, err)
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		t.Fatal("typed historical inspection changed immutable history")
	}
	// Substitute an old generation's immutable manifest after the current
	// physical proof. Its current successor remains unchanged; only the final
	// historical recheck can reject this captured older origin.
	changed := cloneGatewayCurrentRouteState(f.baseline)
	changed.TransferManifestDigest = strings.Repeat("f", 64)
	changed.Digest, _ = gatewayCurrentRouteStateDigest(changed)
	if !validGatewayCurrentRouteState(changed) {
		t.Fatal("replacement must be structurally valid")
	}
	physical.before = func() {
		writer := gatewayUpgradeStateStore{directory: f.store.directory}
		if err := writer.writeExact(f.store.path, f.store.purpose, changed, false, maxGatewayCurrentRouteStateBytes); err != nil {
			t.Fatal(err)
		}
	}
	calls := physical.calls
	got, err = f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err == nil || got.Disposition != "" || physical.calls != calls+1 {
		t.Fatalf("changed historical origin survived final recheck: %+v calls=%d error=%v", got, physical.calls-calls, err)
	}
	old, oldErr := f.store.load()
	current, currentErr := store.load()
	if oldErr != nil || currentErr != nil || !reflect.DeepEqual(old, changed) || !reflect.DeepEqual(current, cleared) ||
		physical.applyCalls != 0 || physical.restoreCalls != 0 || physical.stopCalls != 0 {
		t.Fatal("refusal rewrote history, current state, or physical resources")
	}
}
