package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// This is deliberately one contiguous journey: it proves that a transferred
// grant can be retired through the public current-state path, replaced through
// the same public path, and then transferred by a later typed rebind without
// reviving the retired authority.
func TestGatewayRebindConcreteCompositionTransferredLANLifecycle(t *testing.T) {
	ctx := context.Background()
	f, input, driver := newGatewayRebindMultiFixture(t)
	entry := input.Inspection.Roster[0]
	oldRef := appaccess.GatewayBindingRef{AppID: entry.AppID, AllocationID: entry.AllocationID,
		AccessRevisionID: entry.AccessRevisionID, GrantAttemptID: entry.GrantAttemptID}
	beforeRaw, err := f.repository.ResolveGatewayBinding(ctx, oldRef)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver)
	if err != nil || first.FinalPhase != appaccess.GatewayRebindCommitted || !first.FenceReleased {
		t.Fatalf("first typed commit=%#v error=%v", first, err)
	}
	prior := driver.backend.entries[0]
	priorID, priorConfig := prior.finalID, append([]byte(nil), prior.stage.activeBody...)
	defer clear(priorConfig)
	before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || len(before.History) != 1 || len(before.CurrentTransfers) != 1 {
		t.Fatalf("first current snapshot=%#v error=%v", before, err)
	}
	afterFirst, err := f.repository.ResolveGatewayBinding(ctx, oldRef)
	if err != nil || before.CurrentSource == nil || !reflect.DeepEqual(afterFirst.CurrentGatewaySource, *before.CurrentSource) {
		t.Fatalf("first rebind current source did not match durable authority: binding=%#v source=%#v error=%v",
			afterFirst.CurrentGatewaySource, before.CurrentSource, err)
	}
	selected, err := f.manager.selectGatewayCurrentLocked(ctx, before)
	if err != nil || selected.State == nil || selected.Store == nil || selected.State.Apps[entry.AppID].LAN == nil {
		t.Fatalf("select first transferred current=%#v error=%v", selected, err)
	}
	oldRequest := gatewayV2LANGrantRequestForBinding(entry.AppID, selected.State.Apps[entry.AppID].LAN.Raw)
	files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}

	disable, disableOwner := gatewayRebindTransferredDisableRequest(t, f.repository, oldRef, oldRequest)
	effectsBeforeDisable := len(driver.backend.effects)
	disableCalls := &gatewayRebindTransferredDisableLeaseCalls{}
	disabled, err := f.manager.DisableGatewayV2LAN(ctx, disable,
		gatewayRebindTransferredDisableAuthorizer(t, f.repository, disableOwner, disable, disableCalls))
	if err != nil {
		claim, claimErr := f.repository.AppAccessDisableClaim(ctx, disable.OperationID)
		snapshot, snapshotErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
		selected, selectedErr := f.manager.selectGatewayCurrentLocked(ctx, snapshot)
		var revision uint64
		pendingKind := gatewayV2PendingKind("")
		if selected.State != nil {
			revision = selected.State.Revision
			if selected.State.Pending != nil {
				pendingKind = selected.State.Pending.Kind
			}
		}
		t.Fatalf("public transferred disable error=%v callbacks authorize/revalidate/withdraw=%d/%d/%d claimState=%q selectedRevision=%d pendingKind=%q effects=%d/%d diagnostics claim/snapshot/selection=%v/%v/%v",
			err, disableCalls.authorize, disableCalls.revalidate, disableCalls.withdraw, claim.State, revision, pendingKind,
			len(driver.backend.effects), effectsBeforeDisable, claimErr, snapshotErr, selectedErr)
	}
	if !reflect.DeepEqual(disabled.Receipt.Request, disable) {
		t.Fatal("public transferred disable returned a mismatched receipt")
	}
	var clearAck appaccess.AppAccessDisableProtectedClearAck
	if err := f.manager.WithGatewayV2LANDisableFinalization(ctx, disable,
		func(callback context.Context, got GatewayV2LANDisableRequest) error {
			if !reflect.DeepEqual(got, disable) {
				return errors.New("disable resolution request drifted")
			}
			_, err := f.repository.AuthorizeAppAccessDisable(callback, appaccess.AppAccessDisableAuthorizationInput{
				Owner: disableOwner, PermittedStates: []appaccess.AppAccessDisableState{appaccess.AppAccessDisableWithdrawing},
			})
			return err
		}, func(callback context.Context, observed GatewayV2LANDisableObservation) error {
			if observed.Disposition != GatewayV2LANDisableWithdrawnPending || !reflect.DeepEqual(observed.Request, disable) {
				return errors.New("disable terminal observation drifted")
			}
			claim, _, err := f.repository.ResolveAppAccessDisableClaim(callback, disableOwner,
				appaccess.AppAccessDisableWithdrawing, appaccess.AppAccessDisableCommitted, appaccess.AppAccessDisableProof{
					GatewayOperationID: observed.GatewayOperationID, ProtectedStateDigest: observed.ProtectedStateDigest,
					ObservedAt: observed.ObservedAt})
			if err != nil {
				return err
			}
			confirmed, err := f.repository.AppAccessDisableClaim(callback, claim.OperationID)
			if err != nil || confirmed.State != appaccess.AppAccessDisableCommitted || confirmed.Proof == nil ||
				confirmed.Proof.GatewayOperationID != observed.GatewayOperationID ||
				confirmed.Proof.ProtectedStateDigest != observed.ProtectedStateDigest {
				return appaccess.ErrInvalidStoredState
			}
			return nil
		}, func(callback context.Context, observed GatewayV2LANDisableObservation) error {
			var acknowledged bool
			var err error
			clearAck, acknowledged, err = f.repository.AcknowledgeAppAccessDisableProtectedClear(callback,
				disable.OperationID, observed.GatewayOperationID, observed.ProtectedStateDigest, observed.ObservedAt)
			if err != nil || !acknowledged || clearAck.OperationID != disable.OperationID {
				return appaccess.ErrInvalidStoredState
			}
			return nil
		}); err != nil {
		t.Fatalf("public transferred disable finalization: %v", err)
	}
	afterDisable, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	disabledSelection, err := f.manager.selectGatewayCurrentLocked(ctx, afterDisable)
	if err != nil || disabledSelection.State == nil || disabledSelection.State.Apps[entry.AppID].LAN != nil ||
		!reflect.DeepEqual(before.History, afterDisable.History) || !reflect.DeepEqual(before.CurrentTransfers, afterDisable.CurrentTransfers) {
		t.Fatalf("disable changed immutable rebind authority: selection=%#v error=%v", disabledSelection, err)
	}
	withdrawn := driver.backend.hostProbe(ctx, disabledSelection.State.Profile.SelectedIPv4, oldRequest.Port,
		disabledSelection.State.Profile.SelectedIPv4, "/")
	if !withdrawn.Connected || !withdrawn.Responded || withdrawn.Status != 404 {
		t.Fatalf("disabled transferred binding still serves: %#v", withdrawn)
	}
	staleAuthorizerCalls := 0
	staleEffects := len(driver.backend.effects)
	staleSQLBefore, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	staleFilesBefore, filesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || filesErr != nil {
		t.Fatalf("stale request baseline snapshots: sql=%v files=%v", err, filesErr)
	}
	if _, err := f.manager.GrantGatewayV2LAN(ctx, oldRequest,
		func(context.Context, GatewayV2LANGrantRequest) (GatewayV2LANGrantAuthorizationLease, error) {
			staleAuthorizerCalls++
			return nil, errors.New("stale grant authorizer must not run")
		}); err == nil || staleAuthorizerCalls != 0 || len(driver.backend.effects) != staleEffects {
		t.Fatalf("retired or stale-profile grant reached effects/authorizer: error=%v authorizer=%d effects=%d/%d",
			err, staleAuthorizerCalls, len(driver.backend.effects), staleEffects)
	}
	staleSQLAfter, staleSQLErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
	staleFilesAfter, staleFilesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if staleSQLErr != nil || staleFilesErr != nil || !reflect.DeepEqual(staleSQLBefore, staleSQLAfter) ||
		!sameGatewayHistorySnapshot(staleFilesBefore, staleFilesAfter) {
		t.Fatalf("stale request changed SQL/protected history: sql=%v files=%v", staleSQLErr, staleFilesErr)
	}

	profile, err := f.repository.CurrentGatewayProfile(ctx)
	if err != nil || profile.ID != disabledSelection.State.Profile.RevisionID ||
		profile.RevisionNumber != disabledSelection.State.Profile.RevisionNumber || profile.SpecDigest != disabledSelection.State.Profile.SpecDigest {
		t.Fatalf("current successor profile=%#v state=%#v error=%v", profile, disabledSelection.State.Profile, err)
	}
	newRequest, newInput, newOwner := gatewayRebindTransferredNewGrant(t, f.repository, entry.AppID, profile,
		oldRequest.AccessRevisionNumber)
	if newRequest.AttemptID == oldRequest.AttemptID || newRequest.AllocationID == oldRequest.AllocationID ||
		newRequest.AccessRevisionID == oldRequest.AccessRevisionID || newRequest.OwnerOperationID == oldRequest.OwnerOperationID ||
		newRequest.AccessRevisionNumber != oldRequest.AccessRevisionNumber+1 {
		t.Fatal("regrant reused retired authority identity")
	}
	if _, _, err := f.repository.AdvanceAppAccessGrantClaim(ctx, newOwner,
		appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying); err != nil {
		t.Fatalf("advance new grant to applying: %v", err)
	}
	granted, err := f.manager.GrantGatewayV2LAN(ctx, newRequest,
		gatewayRebindTransferredGrantAuthorizer(t, f.repository, newInput, newOwner, newRequest))
	if err != nil || !reflect.DeepEqual(granted.Receipt.Request, newRequest) {
		t.Fatalf("public successor-profile grant=%#v error=%v", granted, err)
	}
	if err := f.manager.WithGatewayV2LANCommitResolution(ctx, newRequest,
		func(callback context.Context, receipt GatewayV2LANGrantReceipt) error {
			if !reflect.DeepEqual(receipt.Request, newRequest) {
				return errors.New("grant receipt request drifted")
			}
			claim, _, err := f.repository.ResolveAppAccessGrantClaim(callback, newOwner,
				appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted, appaccess.AppAccessGrantProof{
					GatewayOperationID: receipt.GatewayOperationID, ProtectedStateDigest: receipt.ProtectedStateDigest})
			if err != nil {
				return err
			}
			confirmed, err := f.repository.AppAccessGrantClaim(callback, claim.AttemptID)
			if err != nil || confirmed.State != appaccess.AppAccessGrantCommitted || confirmed.Proof == nil ||
				confirmed.Proof.GatewayOperationID != receipt.GatewayOperationID ||
				confirmed.Proof.ProtectedStateDigest != receipt.ProtectedStateDigest {
				return appaccess.ErrInvalidStoredState
			}
			return nil
		}); err != nil {
		t.Fatalf("public successor-profile grant finalization: %v", err)
	}

	afterGrant, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grantedSelection, err := f.manager.selectGatewayCurrentLocked(ctx, afterGrant)
	if err != nil || grantedSelection.State == nil || grantedSelection.State.Apps[entry.AppID].LAN == nil {
		t.Fatalf("select regranted current=%#v error=%v", grantedSelection, err)
	}
	newBinding := grantedSelection.State.Apps[entry.AppID].LAN
	if !reflect.DeepEqual(newBinding.Raw, mustGatewayV2LANBinding(t, newRequest)) || newBinding.Transfer != nil ||
		!reflect.DeepEqual(before.History, afterGrant.History) || !reflect.DeepEqual(before.CurrentTransfers, afterGrant.CurrentTransfers) {
		t.Fatalf("regrant changed retained authority or inherited a transfer: binding=%#v", newBinding)
	}
	newRef := appaccess.GatewayBindingRef{AppID: newRequest.AppID, AllocationID: newRequest.AllocationID,
		AccessRevisionID: newRequest.AccessRevisionID, GrantAttemptID: newRequest.AttemptID}
	newResolutionBeforeLater, err := f.repository.ResolveGatewayBinding(ctx, newRef)
	if err != nil || !reflect.DeepEqual(newResolutionBeforeLater.RawProfile, profile) ||
		newResolutionBeforeLater.RawGrant.AttemptID != newRequest.AttemptID ||
		newResolutionBeforeLater.RawGrant.RequestDigest != newRequest.ClaimRequestDigest ||
		newResolutionBeforeLater.RawAllocation.ID != newRequest.AllocationID ||
		newResolutionBeforeLater.RawAccessRevision.ID != newRequest.AccessRevisionID ||
		len(newResolutionBeforeLater.TransferChain) != 0 || newResolutionBeforeLater.TransferChainTipDigest != "" {
		t.Fatalf("new grant inherited a rebind chain before later rebind: resolution=%#v error=%v", newResolutionBeforeLater, err)
	}
	newGrant, err := f.repository.AppAccessGrantClaim(ctx, newRequest.AttemptID)
	oldGrant, oldGrantErr := f.repository.AppAccessGrantClaim(ctx, oldRequest.AttemptID)
	if err != nil || oldGrantErr != nil || newGrant.Proof == nil || newGrant.RetiredAt != nil || oldGrant.RetiredByDisableOperationID != disable.OperationID ||
		!reflect.DeepEqual(oldGrant.Spec, beforeRaw.RawGrant.Spec) || oldGrant.RequestDigest != beforeRaw.RawGrant.RequestDigest ||
		!reflect.DeepEqual(oldGrant.Proof, beforeRaw.RawGrant.Proof) || clearAck.OperationID != disable.OperationID {
		t.Fatalf("raw grant history changed across disable/regrant: old=%#v new=%#v errors=%v/%v", oldGrant, newGrant, oldGrantErr, err)
	}
	served := driver.backend.hostProbe(ctx, grantedSelection.State.Profile.SelectedIPv4, newRequest.Port,
		grantedSelection.State.Profile.SelectedIPv4, "/")
	if !served.Connected || !served.Responded || served.Status != 200 {
		t.Fatalf("regranted binding does not serve: %#v", served)
	}
	for _, effect := range driver.backend.effects[effectsBeforeDisable:] {
		if len(effect) < 2 {
			continue
		}
		if (effect[0] == "network" || effect[0] == "volume") && (effect[1] == "create" || effect[1] == "rm") ||
			effect[0] == "container" && (effect[1] == "create" || effect[1] == "rm" || effect[1] == "start" || effect[1] == "stop") {
			t.Fatalf("disable/regrant changed gateway resource lifecycle: %v", effect)
		}
	}
	if driver.backend.entries[0] != prior || prior.finalID != priorID || !prior.finalPresent || !prior.final.Running ||
		!reflect.DeepEqual(priorConfig, prior.stage.activeBody) || prior.predecessor.FinalContainer.Running {
		t.Fatal("disable/regrant replaced retained successor resources or restarted predecessor")
	}

	effectsBeforeReplay := len(driver.backend.effects)
	filesAfterGrant, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	observeNetwork := f.manager.gatewayRebindV2NetworkObserver
	fresh := freshGatewayRebindRecoveryManager(f.manager)
	// A fresh Manager retains the simulated host census while the shared backend
	// continues to model the same retained physical resources.
	fresh.gatewayRebindV2NetworkObserver = observeNetwork
	fresh.gatewayRebindFailStop, fresh.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
	replay := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: &gatewayRebindCompositionDriver{
		t: t, runner: driver.runner}, backend: driver.backend}
	replay.installMulti(fresh)
	recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
	if err != nil || recovered.Recovered || !recovered.FenceReleased || fresh.gatewayRebindAdmissionBlocked() ||
		len(driver.backend.effects) != effectsBeforeReplay {
		t.Fatalf("fresh regrant replay=%#v effects=%v error=%v", recovered, driver.backend.effects[effectsBeforeReplay:], err)
	}
	confirmed, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	confirmedFiles, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil || filesErr != nil || !reflect.DeepEqual(afterGrant, confirmed) || !sameGatewayHistorySnapshot(filesAfterGrant, confirmedFiles) {
		t.Fatalf("fresh regrant replay changed SQL/files: sql=%v files=%v", err, filesErr)
	}
	f.manager = fresh

	profileSpec := input.Inspection.Spec.SuccessorProfile
	profileSpec.SelectedIPv4, profileSpec.InterfaceID = "192.168.98.8", "rebind-next-successor"
	laterInput := gatewayRebindSequenceNextInput(t, f, input.Inspection.Spec.SuccessorProfileRevisionNumber+1, profileSpec)
	if laterInput.Inspection.Roster[0].PredecessorTransferDigest != nil {
		t.Fatal("later rebind inherited a retired grant transfer")
	}
	later := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: &gatewayRebindCompositionDriver{
		t: t, runner: driver.runner}, backend: driver.backend}
	later.installMulti(f.manager)
	laterResult, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, laterInput, later)
	if err != nil || laterResult.FinalPhase != appaccess.GatewayRebindCommitted || !laterResult.FenceReleased {
		t.Fatalf("later typed rebind=%#v error=%v", laterResult, err)
	}
	newResolution, err := f.repository.ResolveGatewayBinding(ctx, newRef)
	_, oldResolutionErr := f.repository.ResolveGatewayBinding(ctx, oldRef)
	if err != nil || !errors.Is(oldResolutionErr, appaccess.ErrInvalidStoredState) || len(newResolution.TransferChain) != 1 ||
		newResolution.TransferChain[0].PredecessorTransferDigest != nil {
		t.Fatalf("later transfer chain did not stay bound to the new active grant: new=%#v retired=%v error=%v",
			newResolution.TransferChain, oldResolutionErr, err)
	}

	laterSnapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || len(laterSnapshot.History) != 2 || !reflect.DeepEqual(laterSnapshot.History[0], before.History[0]) ||
		len(laterSnapshot.CurrentTransfers) != 1 || laterSnapshot.CurrentTransfers[0].AppID != newRequest.AppID ||
		laterSnapshot.CurrentTransfers[0].AllocationID != newRequest.AllocationID ||
		laterSnapshot.CurrentTransfers[0].GrantAttemptID != newRequest.AttemptID || len(laterSnapshot.History[1].RosterV2) != 1 ||
		laterSnapshot.History[1].RosterV2[0].GrantAttemptID != newRequest.AttemptID ||
		laterSnapshot.History[1].RosterV2[0].AllocationID != newRequest.AllocationID ||
		laterSnapshot.History[1].RosterV2[0].AccessRevisionID != newRequest.AccessRevisionID ||
		laterSnapshot.History[1].RosterV2[0].PredecessorTransferDigest != nil {
		t.Fatalf("later SQL roster/transfers did not identify only the new grant: snapshot=%#v error=%v", laterSnapshot, err)
	}
	startup, startupErr := f.repository.HostingGatewayStartupSnapshot(ctx)
	var oldHistory, newHistory *appaccess.AppAccessGrantStartupClaim
	if startupErr == nil {
		for index := range startup.Grants.Claims {
			claim := &startup.Grants.Claims[index]
			switch claim.Claim.AttemptID {
			case oldRequest.AttemptID:
				oldHistory = claim
			case newRequest.AttemptID:
				newHistory = claim
			}
		}
	}
	if startupErr != nil || oldHistory == nil || newHistory == nil || !reflect.DeepEqual(oldHistory.Profile, beforeRaw.RawProfile) ||
		!reflect.DeepEqual(oldHistory.RetainedTransferChain, before.CurrentTransfers) ||
		!reflect.DeepEqual(oldHistory.RetainedGatewaySource, afterFirst.CurrentGatewaySource) ||
		newHistory.Claim.AttemptID != newRequest.AttemptID || newHistory.Allocation.ID != newRequest.AllocationID ||
		newHistory.Revision.ID != newRequest.AccessRevisionID || len(newHistory.TransferChain) != 1 ||
		newHistory.TransferChain[0].GrantAttemptID != newRequest.AttemptID {
		t.Fatalf("startup retained old authority or selected new authority drifted: startup=%#v error=%v", startup, startupErr)
	}
	for _, test := range []struct {
		name   string
		mutate func(*appaccess.GatewayRebindRecoverySnapshot)
	}{
		{"missing proof", func(snapshot *appaccess.GatewayRebindRecoverySnapshot) { snapshot.CurrentSource = nil }},
		{"crossed proof", func(snapshot *appaccess.GatewayRebindRecoverySnapshot) {
			if len(snapshot.CurrentTransfers) != 0 {
				snapshot.CurrentTransfers = append([]appaccess.GatewayRebindAllocationTransfer(nil), snapshot.CurrentTransfers...)
				snapshot.CurrentTransfers[0].AppID = uuid.NewString()
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeSQL, sqlErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
			beforeFiles, filesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			if sqlErr != nil || filesErr != nil {
				t.Fatalf("snapshot before proof refusal: sql=%v files=%v", sqlErr, filesErr)
			}
			bad := laterSnapshot
			test.mutate(&bad)
			effects := len(driver.backend.effects)
			decorated := gatewayRebindTransferredInspectionRepository{base: f.repository, snapshot: bad}
			_, inspectErr := f.manager.InspectGatewayRebindCurrent(ctx, decorated)
			afterSQL, afterSQLErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
			afterFiles, afterFilesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			if inspectErr == nil || len(driver.backend.effects) != effects || afterSQLErr != nil || afterFilesErr != nil ||
				!reflect.DeepEqual(beforeSQL, afterSQL) || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
				t.Fatalf("read-only proof refusal error=%v effects=%d/%d sql=%v files=%v", inspectErr,
					len(driver.backend.effects), effects, afterSQLErr, afterFilesErr)
			}
		})
	}
	filesAfterLater, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	effectsBeforeFinalReplay := len(driver.backend.effects)
	finalFresh := freshGatewayRebindRecoveryManager(f.manager)
	finalFresh.gatewayRebindFailStop, finalFresh.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
	finalReplay := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: &gatewayRebindCompositionDriver{
		t: t, runner: later.runner}, backend: driver.backend}
	finalReplay.installMulti(finalFresh)
	finalRecovered, err := finalFresh.RecoverGatewayRebindStartup(ctx, f.repository)
	finalSnapshot, finalSQLErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
	finalFiles, finalFilesErr := readGatewayHistorySnapshotMode(finalFresh.store, true)
	if err != nil || finalSQLErr != nil || finalFilesErr != nil || finalRecovered.Recovered || !finalRecovered.FenceReleased ||
		finalFresh.gatewayRebindAdmissionBlocked() || f.repository.CheckGatewayRebindFence(ctx) != nil || len(driver.backend.effects) != effectsBeforeFinalReplay ||
		!reflect.DeepEqual(laterSnapshot, finalSnapshot) || !sameGatewayHistorySnapshot(filesAfterLater, finalFiles) ||
		finalRecovered.SelectedCurrentAuthority == nil || !reflect.DeepEqual(*finalRecovered.SelectedCurrentAuthority, *laterSnapshot.CurrentSource) {
		t.Fatalf("fresh later replay changed authority/effects: recovered=%#v errors=%v/%v/%v effects=%v", finalRecovered,
			err, finalSQLErr, finalFilesErr, driver.backend.effects[effectsBeforeFinalReplay:])
	}
	f.manager = finalFresh
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
}

type gatewayRebindTransferredDisableLease struct {
	repository *appaccess.Repository
	owner      appaccess.AppAccessDisableClaimOwner
	request    GatewayV2LANDisableRequest
	calls      *gatewayRebindTransferredDisableLeaseCalls
}

type gatewayRebindTransferredDisableLeaseCalls struct {
	authorize, revalidate, withdraw int
}

func (l gatewayRebindTransferredDisableLease) Revalidate(ctx context.Context, request GatewayV2LANDisableRequest) error {
	if !reflect.DeepEqual(request, l.request) {
		return appaccess.ErrConflict
	}
	if l.calls != nil {
		l.calls.revalidate++
	}
	claim, err := l.repository.AppAccessDisableClaim(ctx, l.owner.OperationID)
	if err != nil || claim.State != appaccess.AppAccessDisablePrepared && claim.State != appaccess.AppAccessDisableWithdrawing {
		return appaccess.ErrConflict
	}
	_, err = l.repository.AuthorizeAppAccessDisable(ctx, appaccess.AppAccessDisableAuthorizationInput{
		Owner: l.owner, PermittedStates: []appaccess.AppAccessDisableState{claim.State},
	})
	return err
}

func (l gatewayRebindTransferredDisableLease) Withdraw(ctx context.Context, request GatewayV2LANDisableRequest) error {
	if l.calls != nil {
		l.calls.withdraw++
	}
	if err := l.Revalidate(ctx, request); err != nil {
		return err
	}
	claim, err := l.repository.AppAccessDisableClaim(ctx, l.owner.OperationID)
	if err != nil {
		return err
	}
	if claim.State == appaccess.AppAccessDisableWithdrawing {
		return nil
	}
	if claim.State != appaccess.AppAccessDisablePrepared {
		return appaccess.ErrConflict
	}
	_, _, err = l.repository.AdvanceAppAccessDisableClaim(ctx, l.owner,
		appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing)
	return err
}

func (gatewayRebindTransferredDisableLease) Release() error { return nil }

func gatewayRebindTransferredDisableRequest(t *testing.T, repository *appaccess.Repository, ref appaccess.GatewayBindingRef,
	source GatewayV2LANGrantRequest,
) (GatewayV2LANDisableRequest, appaccess.AppAccessDisableClaimOwner) {
	t.Helper()
	resolution, err := repository.ResolveGatewayBinding(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	revision := resolution.RawAccessRevision
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(revision))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := repository.ClaimAppAccessDisable(context.Background(), appaccess.ApproveAppAccessDisableInput{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: revision.RevisionNumber,
		Owner: appaccess.AllocationOwner{AllocationID: revision.Allocation.ID, AppID: revision.AppID,
			OperationID: revision.OperationID, AccessRevisionID: revision.ID},
		Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess, SpecDigest: digest,
			ActorID: gatewayRebindTestAdministrator},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	authorized, err := repository.AuthorizeAppAccessDisable(context.Background(), appaccess.AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{appaccess.AppAccessDisablePrepared},
	})
	if err != nil || authorized.SourceGrant == nil || authorized.SourceGrant.AttemptID != source.AttemptID {
		t.Fatalf("authorize transferred disable=%#v error=%v", authorized, err)
	}
	return GatewayV2LANDisableRequest{OperationID: claim.OperationID, RequestDigest: claim.RequestDigest,
		SpecDigest: claim.SpecDigest, AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID, Port: claim.Spec.Port, AccessRevisionID: claim.Spec.AccessRevisionID,
		AccessRevisionNumber: claim.Spec.AccessRevisionNumber, AccessSpecDigest: authorized.Revision.SpecDigest,
		ApprovedBy: claim.ApprovedBy, GatewayProfileRevisionID: claim.Spec.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: claim.Spec.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     authorized.Profile.SpecDigest, SourceGrant: &source}, owner
}

func gatewayRebindTransferredDisableAuthorizer(t *testing.T, repository *appaccess.Repository,
	owner appaccess.AppAccessDisableClaimOwner, request GatewayV2LANDisableRequest, calls *gatewayRebindTransferredDisableLeaseCalls,
) GatewayV2LANDisableAuthorizer {
	t.Helper()
	return func(ctx context.Context, got GatewayV2LANDisableRequest) (GatewayV2LANDisableAuthorizationLease, error) {
		if !reflect.DeepEqual(got, request) {
			return nil, appaccess.ErrConflict
		}
		calls.authorize++
		claim, err := repository.AppAccessDisableClaim(ctx, owner.OperationID)
		if err != nil || claim.State != appaccess.AppAccessDisablePrepared && claim.State != appaccess.AppAccessDisableWithdrawing {
			return nil, appaccess.ErrConflict
		}
		if _, err := repository.AuthorizeAppAccessDisable(ctx, appaccess.AppAccessDisableAuthorizationInput{
			Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{claim.State},
		}); err != nil {
			return nil, err
		}
		return gatewayRebindTransferredDisableLease{repository: repository, owner: owner, request: request, calls: calls}, nil
	}
}

type gatewayRebindTransferredGrantLease struct {
	repository *appaccess.Repository
	input      appaccess.ClaimAppAccessGrantInput
	owner      appaccess.AppAccessGrantClaimOwner
	request    GatewayV2LANGrantRequest
}

func (l gatewayRebindTransferredGrantLease) Revalidate(ctx context.Context, request GatewayV2LANGrantRequest) error {
	if !reflect.DeepEqual(request, l.request) {
		return appaccess.ErrConflict
	}
	claim, _, err := l.repository.ClaimAppAccessGrant(ctx, l.input)
	if err != nil || claim.State != appaccess.AppAccessGrantApplying || claim.AttemptID != request.AttemptID ||
		claim.RequestDigest != request.ClaimRequestDigest {
		return appaccess.ErrConflict
	}
	_, err = l.repository.AuthorizeAppAccessGrant(ctx, appaccess.AppAccessGrantAuthorizationInput{
		Owner: l.owner, PermittedStates: []appaccess.AppAccessGrantState{appaccess.AppAccessGrantApplying},
	})
	return err
}

func (l gatewayRebindTransferredGrantLease) Activate(ctx context.Context, request GatewayV2LANGrantRequest) error {
	if err := l.Revalidate(ctx, request); err != nil {
		return err
	}
	_, _, err := l.repository.AdvanceAppAccessGrantClaim(ctx, l.owner,
		appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive)
	return err
}

func (gatewayRebindTransferredGrantLease) Release() error { return nil }

func gatewayRebindTransferredNewGrant(t *testing.T, repository *appaccess.Repository, appID string,
	profile appaccess.GatewayProfileRevision, expectedRevisionNumber int64,
) (GatewayV2LANGrantRequest, appaccess.ClaimAppAccessGrantInput, appaccess.AppAccessGrantClaimOwner) {
	t.Helper()
	ctx := context.Background()
	allocation, _, err := repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: expectedRevisionNumber, GatewayProfileRevisionID: profile.ID,
		GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{
		AppID: appID, OperationID: allocation.OwnerOperationID, AllocationID: allocation.ID,
		ExpectedRevisionNumber: expectedRevisionNumber,
		Approval: appaccess.Approval{Action: appaccess.ActionEnableAppAccess, SpecDigest: digest,
			ActorID: gatewayRebindTestAdministrator},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := appaccess.ClaimAppAccessGrantInput{AttemptID: uuid.NewString(),
		Spec: appaccess.AppAccessGrantSpecFor(revision, profile), ActorID: gatewayRebindTestAdministrator}
	claim, _, err := repository.ClaimAppAccessGrant(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	request := GatewayV2LANGrantRequest{AttemptID: claim.AttemptID, ClaimRequestDigest: claim.RequestDigest,
		AppID: revision.AppID, AllocationID: revision.Allocation.ID, OwnerOperationID: revision.OperationID,
		Port: revision.Allocation.Port, AccessRevisionID: revision.ID, AccessRevisionNumber: revision.RevisionNumber,
		AccessSpecDigest: revision.SpecDigest, ApprovedBy: claim.Spec.ApprovedBy,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
		GatewayProfileSpecDigest: profile.SpecDigest}
	return request, input, appaccess.AppAccessGrantClaimOwnerFor(claim)
}

func gatewayRebindTransferredGrantAuthorizer(t *testing.T, repository *appaccess.Repository,
	input appaccess.ClaimAppAccessGrantInput, owner appaccess.AppAccessGrantClaimOwner, request GatewayV2LANGrantRequest,
) GatewayV2LANGrantAuthorizer {
	t.Helper()
	return func(ctx context.Context, got GatewayV2LANGrantRequest) (GatewayV2LANGrantAuthorizationLease, error) {
		if !reflect.DeepEqual(got, request) {
			return nil, appaccess.ErrConflict
		}
		if _, err := repository.AuthorizeAppAccessGrant(ctx, appaccess.AppAccessGrantAuthorizationInput{
			Owner: owner, PermittedStates: []appaccess.AppAccessGrantState{appaccess.AppAccessGrantApplying},
		}); err != nil {
			return nil, err
		}
		return gatewayRebindTransferredGrantLease{repository: repository, input: input, owner: owner, request: request}, nil
	}
}

func mustGatewayV2LANBinding(t *testing.T, request GatewayV2LANGrantRequest) gatewayV2LANBinding {
	t.Helper()
	binding, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

type gatewayRebindTransferredInspectionRepository struct {
	base     gatewayRebindCurrentInspectionRepository
	snapshot appaccess.GatewayRebindRecoverySnapshot
}

func (r gatewayRebindTransferredInspectionRepository) GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
	return r.snapshot, nil
}

func (r gatewayRebindTransferredInspectionRepository) CheckGatewayRebindFence(ctx context.Context) error {
	return r.base.CheckGatewayRebindFence(ctx)
}

func TestGatewayRebindMultiRunnerCurrentCommandRefusal(t *testing.T) {
	f, input, driver := newGatewayRebindMultiFixture(t)
	prepared, err := withCrossStoreFixtureEffectLocks(t, f.manager, func() (gatewayRebindPreparedAttempt, error) {
		return f.manager.prepareGatewayRebindLocked(context.Background(), f.repository, input)
	})
	if err != nil {
		t.Fatal(err)
	}
	driver.initialize(prepared.Intent)
	driver.installMulti(f.manager)
	r := driver.runner
	r.finalPresent, r.final.Running = true, true
	ctx := context.Background()
	stageBody, err := gatewayRebindTypedStageConfigBytes(prepared.Intent)
	if err != nil {
		t.Fatal(err)
	}
	finalPlan, err := gatewayRebindTypedFinalConfigRoutePlanFor(prepared.Intent, prepared.Checkpoint, prepared.Intent.RuntimeHeads)
	if err != nil {
		clear(stageBody)
		t.Fatal(err)
	}
	activeBody, err := gatewayRebindTypedFinalConfigBytes(prepared.Intent, prepared.Checkpoint, finalPlan)
	if err != nil {
		clear(stageBody)
		t.Fatal(err)
	}
	r.stage.stageBody, r.stage.activeBody = stageBody, activeBody
	lan, candidate := gatewayRebindTransferredChangedLANRoute(t, activeBody, r.intent.Network.ContainerIPv4)
	endpoint := gatewayRebindTransferredCurrentEndpoint(t, r)
	r.final.ID, r.final.Running = r.finalID, true
	r.final.Networks = map[string]*networkAttachment{endpoint.value.NetworkName: {IPAddress: "172.31.0.3"}}
	r.finalRuntime.EffectivePortBindings = map[string][]map[string]string{
		strconv.FormatUint(uint64(lan.port), 10) + "/tcp": {{"HostIp": lan.host, "HostPort": strconv.FormatUint(uint64(lan.port), 10)}},
	}
	if initial := driver.backend.hostProbe(ctx, lan.host, lan.port, lan.host, "/"); !initial.Connected || !initial.Responded || initial.Status != 200 {
		t.Fatalf("initial aggregate LAN route=%#v", initial)
	}
	stageActive := append([]byte(nil), r.stage.activeBody...)
	defer clear(stageActive)
	initialRestart, err := f.manager.inspectStoppedCaddyRestartConfig(ctx, r.finalID, r.intent.Identity.ActiveConfigFilename)
	if err != nil || !bytes.Equal(initialRestart, activeBody) {
		clear(initialRestart)
		t.Fatalf("initial direct restart config=%d expected=%d error=%v", len(initialRestart), len(activeBody), err)
	}
	clear(initialRestart)
	local := t.TempDir() + "/candidate.json"
	if err := os.WriteFile(local, candidate, 0o644); err != nil {
		t.Fatal(err)
	}
	malformed := t.TempDir() + "/candidate-malformed.json"
	if err := os.WriteFile(malformed, []byte("["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
		"container", "cp", malformed, r.finalID + ":/config/" + gatewayCurrentPhysicalConfigFilename}}); err != nil {
		t.Fatal(err)
	}
	invalidBeforeValidate := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r)
	if !bytes.Equal(invalidBeforeValidate.candidate, []byte("[")) || len(invalidBeforeValidate.live) != 0 ||
		len(invalidBeforeValidate.next) != 0 || len(invalidBeforeValidate.restart) != 0 || len(invalidBeforeValidate.autosave) != 0 {
		t.Fatalf("malformed candidate upload changed live current state: %#v", invalidBeforeValidate)
	}
	if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
		"container", "exec", r.finalID, "caddy", "validate", "--config", "/config/" + gatewayCurrentPhysicalConfigFilename}}); err == nil {
		t.Fatal("accepted malformed current candidate validation")
	}
	if invalidAfterValidate := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r); !reflect.DeepEqual(invalidBeforeValidate, invalidAfterValidate) {
		t.Fatal("malformed candidate validation changed current retained state")
	}
	if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
		"container", "cp", local, r.finalID + ":/config/" + gatewayCurrentPhysicalConfigFilename}}); err != nil {
		t.Fatalf("canonical current candidate upload: %v", err)
	}
	if err := f.manager.probeGatewayEndpoint(ctx, r.finalID, endpoint.value); err != nil {
		t.Fatalf("canonical endpoint probe before reload: %v", err)
	}
	for _, args := range [][]string{
		{"container", "exec", r.finalID, "caddy", "validate", "--config", "/config/" + gatewayCurrentPhysicalConfigFilename},
		{"container", "exec", r.finalID, "caddy", "reload", "--config", "/config/" + gatewayCurrentPhysicalConfigFilename},
		{"container", "exec", "--user", "0:0", r.finalID, "cp", "/config/" + gatewayCurrentPhysicalConfigFilename, "/config/active.next.json"},
	} {
		if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: args}); err != nil {
			t.Fatalf("canonical current command %v: %v", args, err)
		}
	}
	admin, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{"container", "exec", r.finalID, "curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/"}})
	if err != nil || !bytes.Equal(admin.Stdout, candidate) || !bytes.Equal(r.currentLive, candidate) ||
		bytes.Equal(r.currentRestartBody(), candidate) || !bytes.Equal(r.currentNext, candidate) {
		t.Fatalf("canonical current reload did not leave restart state separate: live=%d restart=%d next=%d error=%v", len(r.currentLive), len(r.currentRestart), len(r.currentNext), err)
	}
	if err := f.manager.probeGatewayEndpoint(ctx, r.finalID, endpoint.value); err != nil {
		t.Fatalf("canonical endpoint probe after reload: %v", err)
	}
	if changed := driver.backend.hostProbe(ctx, lan.host, lan.port, lan.host, "/"); !changed.Connected || !changed.Responded || changed.Status != 404 {
		t.Fatalf("aggregate backend did not observe the reloaded LAN route: %#v", changed)
	}
	if !bytes.Equal(r.stage.activeBody, stageActive) {
		t.Fatal("current reload aliased or changed the immutable stage config")
	}
	challenge := gatewayRebindTransferredCurrentChallengeCommand(t, r)
	challengeResult, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: challenge.args})
	if err != nil || string(challengeResult.Stdout) != gatewayV2ChallengeBodyPrefix+challenge.token+"\n404" {
		t.Fatalf("canonical current challenge did not prove live config: output=%q error=%v", challengeResult.Stdout, err)
	}
	if !driver.backend.containerProbe(ctx, r.finalID, challenge.address, challenge.port, challenge.host, challenge.token) {
		t.Fatal("aggregate backend lost the canonical current challenge after LAN route reload")
	}
	archive, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{"container", "cp", r.finalID + ":/config/.", "-"}})
	if err != nil {
		t.Fatal(err)
	}
	entries := gatewayRebindTransferredArchiveEntries(t, archive.Stdout)
	for _, name := range []string{gatewayCurrentPhysicalConfigFilename, r.intent.Identity.ActiveConfigFilename, "active.next.json", "caddy/autosave.json"} {
		if _, found := entries[name]; !found {
			t.Fatalf("canonical archive omitted %s: %v", name, entries)
		}
	}
	if !bytes.Equal(gatewayRebindTransferredArchiveBody(t, archive.Stdout, "active.next.json"), candidate) {
		t.Fatalf("canonical archive did not retain the current next config: %#v", entries)
	}
	for _, want := range []struct {
		name     string
		mode     int64
		uid, gid int
	}{
		{".", 0o755, 0, 0},
		{"caddy/", 0o1777, 0, 0},
		{gatewayCurrentPhysicalConfigFilename, 0o644, 0, 0},
		{r.intent.Identity.StageConfigFilename, 0o644, 0, 0},
		{r.intent.Identity.ActiveConfigFilename, 0o644, 0, 0},
		{"active.next.json", 0o644, 0, 0},
		{"caddy/autosave.json", 0o600, 1000, 1000},
	} {
		entry, found := entries[want.name]
		if !found || entry.Mode != want.mode || entry.Uid != want.uid || entry.Gid != want.gid {
			t.Fatalf("canonical archive metadata %s=%#v found=%t, want mode=%#o uid/gid=%d/%d", want.name,
				entry, found, want.mode, want.uid, want.gid)
		}
	}
	if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
		"container", "exec", "--user", "0:0", r.finalID, "mv", "/config/active.next.json", "/config/" + r.intent.Identity.ActiveConfigFilename}}); err != nil {
		t.Fatal(err)
	}
	archive, err = driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{"container", "cp", r.finalID + ":/config/.", "-"}})
	if err != nil {
		t.Fatal(err)
	}
	entries = gatewayRebindTransferredArchiveEntries(t, archive.Stdout)
	if _, found := entries["active.next.json"]; found || !bytes.Equal(gatewayRebindTransferredArchiveBody(t, archive.Stdout, r.intent.Identity.ActiveConfigFilename), candidate) ||
		!bytes.Equal(r.currentRestart, candidate) || r.currentNext != nil {
		t.Fatal("canonical current move did not consume next into restart config")
	}
	if active := entries[r.intent.Identity.ActiveConfigFilename]; active.Mode != 0o644 || active.Uid != 0 || active.Gid != 0 {
		t.Fatalf("moved restart config metadata=%#v", active)
	}
	restart, err := f.manager.inspectStoppedCaddyRestartConfig(ctx, r.finalID, r.intent.Identity.ActiveConfigFilename)
	if err != nil || !bytes.Equal(restart, candidate) {
		clear(restart)
		t.Fatalf("current direct restart config=%d expected=%d error=%v", len(restart), len(candidate), err)
	}
	clear(restart)
	if runtime.GOOS != "windows" {
		wrongMode := t.TempDir() + "/candidate-0600.json"
		if err := os.WriteFile(wrongMode, candidate, 0o600); err != nil {
			t.Fatal(err)
		}
		beforeWrongMode := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r)
		if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
			"container", "cp", wrongMode, r.finalID + ":/config/" + gatewayCurrentPhysicalConfigFilename}}); err == nil {
			t.Fatal("accepted noncanonical current config source mode")
		}
		if afterWrongMode := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r); !reflect.DeepEqual(beforeWrongMode, afterWrongMode) {
			t.Fatal("noncanonical current config source changed retained command state")
		}
	}
	before := len(driver.backend.effects)
	refuse := func(label string, args []string) {
		t.Helper()
		state := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r)
		effects := len(driver.backend.effects)
		if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: args}); err == nil {
			t.Fatalf("accepted %s current endpoint command: %v", label, args)
		}
		if after := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r); !reflect.DeepEqual(state, after) {
			t.Fatalf("rejected %s current endpoint command changed retained state", label)
		}
		if len(driver.backend.effects) != effects {
			t.Fatalf("rejected %s current endpoint command recorded effects", label)
		}
	}
	endpointArgs := gatewayRebindTransferredCurrentEndpointArgs(r.finalID, endpoint.value)
	if result, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: endpointArgs}); err != nil || string(result.Stdout) != "200" {
		t.Fatalf("canonical current endpoint command output=%q error=%v", result.Stdout, err)
	}
	wrongEndpointID := append([]string(nil), endpointArgs...)
	wrongEndpointID[2] = r.stage.container.ID
	wrongEndpointAlias := append([]string(nil), endpointArgs...)
	wrongEndpointAlias[len(wrongEndpointAlias)-1] = "http://wrong." + endpoint.value.NetworkName + ":" + strconv.FormatUint(uint64(endpoint.value.InternalPort), 10) + "/"
	wrongEndpointNetwork := append([]string(nil), endpointArgs...)
	wrongEndpointNetwork[len(wrongEndpointNetwork)-1] = "http://" + endpoint.value.NetworkAlias + ".unowned-network:" + strconv.FormatUint(uint64(endpoint.value.InternalPort), 10) + "/"
	wrongEndpointPort := append([]string(nil), endpointArgs...)
	wrongEndpointPort[len(wrongEndpointPort)-1] = "http://" + endpoint.value.NetworkAlias + "." + endpoint.value.NetworkName + ":" + strconv.FormatUint(uint64(endpoint.value.InternalPort+1), 10) + "/"
	for label, args := range map[string][]string{
		"wrong identity": wrongEndpointID,
		"wrong alias":    wrongEndpointAlias,
		"wrong network":  wrongEndpointNetwork,
		"wrong port":     wrongEndpointPort,
		"extra argv":     append(append([]string(nil), endpointArgs...), "extra"),
	} {
		refuse(label, args)
	}
	attachment := r.final.Networks[endpoint.value.NetworkName]
	delete(r.final.Networks, endpoint.value.NetworkName)
	refuse("missing final attachment", endpointArgs)
	r.final.Networks[endpoint.value.NetworkName] = attachment
	originalLive := append([]byte(nil), r.currentLive...)
	r.currentLive = gatewayRebindTransferredWithoutEndpointUpstream(t, originalLive, endpoint.value)
	refuse("missing live upstream", endpointArgs)
	clear(r.currentLive)
	r.currentLive = originalLive
	route := r.predecessorState.Apps[endpoint.appID]
	originalEndpoints := append([]generatedruntime.RouteEndpoint(nil), route.Route.Endpoints...)
	duplicate := endpoint.value
	duplicate.ContainerID = strings.Repeat("e", 64)
	duplicate.InternalPort++
	route.Route.Endpoints = append(route.Route.Endpoints, duplicate)
	r.predecessorState.Apps[endpoint.appID] = route
	refuse("ambiguous retained alias", endpointArgs)
	route.Route.Endpoints = originalEndpoints
	r.predecessorState.Apps[endpoint.appID] = route
	route = r.predecessorState.Apps[endpoint.appID]
	route.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), originalEndpoints...)
	route.Route.Endpoints[endpoint.index].NetworkAlias = "missing-retained-alias"
	r.predecessorState.Apps[endpoint.appID] = route
	refuse("missing retained alias", endpointArgs)
	route.Route.Endpoints = originalEndpoints
	r.predecessorState.Apps[endpoint.appID] = route
	if err := f.manager.probeGatewayEndpoint(ctx, r.finalID, endpoint.value); err != nil {
		t.Fatalf("canonical endpoint probe after retained endpoint restoration: %v", err)
	}
	wrongHost := append([]string(nil), challenge.args...)
	wrongHost[len(wrongHost)-2] = "Host: 192.168.255.254"
	wrongAddress := append([]string(nil), challenge.args...)
	wrongAddress[len(wrongAddress)-1] = "http://192.168.99.254:" + strconv.FormatUint(uint64(challenge.port), 10) + challenge.path
	wrongPort := append([]string(nil), challenge.args...)
	wrongPortValue := challenge.port + 1
	if challenge.port == ^uint16(0) {
		wrongPortValue = challenge.port - 1
	}
	wrongPort[len(wrongPort)-1] = "http://" + net.JoinHostPort(challenge.address, strconv.FormatUint(uint64(wrongPortValue), 10)) + challenge.path
	for _, args := range [][]string{
		{"container", "exec", "--user", "0:1", r.finalID, "cp", "/config/" + gatewayCurrentPhysicalConfigFilename, "/config/active.next.json"},
		{"container", "exec", "--user", "0:0", r.stage.container.ID, "cp", "/config/" + gatewayCurrentPhysicalConfigFilename, "/config/active.next.json"},
		{"container", "exec", normalizeID(r.predecessor.FinalContainer.ID), "caddy", "reload", "--config", "/config/" + gatewayCurrentPhysicalConfigFilename},
		{"container", "exec", "--user", "0:0", r.finalID, "cp", "/config/" + gatewayCurrentPhysicalConfigFilename, "/config/active.next.json", "extra"},
		{"container", "exec", r.finalID, "curl", "--silent", "http://127.0.0.1:2019/config/"},
		wrongHost,
		wrongAddress,
		wrongPort,
		append(append([]string(nil), challenge.args...), "extra"),
	} {
		refuse("malformed or crossed", args)
	}
	r.final.Running = false
	stopped := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r)
	if _, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
		"container", "exec", r.finalID, "caddy", "validate", "--config", "/config/" + gatewayCurrentPhysicalConfigFilename}}); err == nil {
		t.Fatal("accepted current exec for stopped final container")
	}
	if after := gatewayRebindTransferredCurrentCommandState(t, ctx, driver, r); !reflect.DeepEqual(stopped, after) {
		t.Fatal("stopped current command changed retained state")
	}
	if len(driver.backend.effects) != before {
		t.Fatalf("rejected commands recorded effects: %v", driver.backend.effects[before:])
	}
}

type gatewayRebindTransferredCurrentCommandSnapshot struct {
	candidate, live, next, restart, autosave, archive []byte
	finalID                                           string
	finalPresent, finalRunning                        bool
}

type gatewayRebindTransferredLANRoute struct {
	host string
	port uint16
}

type gatewayRebindTransferredEndpoint struct {
	appID string
	index int
	value generatedruntime.RouteEndpoint
}

func gatewayRebindTransferredCurrentEndpoint(t *testing.T, runner *gatewayRebindCompositionRunner) gatewayRebindTransferredEndpoint {
	t.Helper()
	for appID, route := range gatewayV2RouteRecords(runner.predecessorState) {
		for index, endpoint := range route.Endpoints {
			if endpoint.NetworkName != "" && endpoint.NetworkAlias != "" && endpoint.InternalPort != 0 {
				return gatewayRebindTransferredEndpoint{appID: appID, index: index, value: endpoint}
			}
		}
	}
	t.Fatal("missing retained endpoint for current transport command")
	return gatewayRebindTransferredEndpoint{}
}

func gatewayRebindTransferredCurrentEndpointArgs(finalID string, endpoint generatedruntime.RouteEndpoint) []string {
	return []string{"container", "exec", finalID, "curl", "--disable", "--silent", "--head", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1",
		"--max-time", "2", "http://" + endpoint.NetworkAlias + "." + endpoint.NetworkName + ":" + strconv.FormatUint(uint64(endpoint.InternalPort), 10) + "/"}
}

func gatewayRebindTransferredWithoutEndpointUpstream(t *testing.T, body []byte, endpoint generatedruntime.RouteEndpoint) []byte {
	t.Helper()
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	dial := net.JoinHostPort(endpoint.NetworkAlias+"."+endpoint.NetworkName, strconv.FormatUint(uint64(endpoint.InternalPort), 10))
	removed := false
	for name, server := range config.Apps.HTTP.Servers {
		for routeIndex := range server.Routes {
			for handlerIndex := range server.Routes[routeIndex].Handle {
				handler := &server.Routes[routeIndex].Handle[handlerIndex]
				if handler.Handler != "reverse_proxy" {
					continue
				}
				upstreams := handler.Upstreams[:0]
				for _, upstream := range handler.Upstreams {
					if upstream.Dial == dial {
						removed = true
						continue
					}
					upstreams = append(upstreams, upstream)
				}
				handler.Upstreams = upstreams
			}
		}
		config.Apps.HTTP.Servers[name] = server
	}
	if !removed {
		t.Fatal("missing retained endpoint upstream")
	}
	result, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// gatewayRebindTransferredChangedLANRoute changes only the selected LAN root
// reverse-proxy route. The same server retains its challenge and unconditional
// fallback routes, so the command test can observe a real live-config change
// through the aggregate multi-generation probe without extending the Caddy
// interpreter.
func gatewayRebindTransferredChangedLANRoute(t *testing.T, body []byte, listener string) (gatewayRebindTransferredLANRoute, []byte) {
	t.Helper()
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	for name, server := range config.Apps.HTTP.Servers {
		if !strings.HasPrefix(name, "lan-") || len(server.Listen) != 1 || server.Listen[0] == "" {
			continue
		}
		address, portText, err := net.SplitHostPort(server.Listen[0])
		port, portErr := strconv.ParseUint(portText, 10, 16)
		if err != nil || portErr != nil || port == 0 || address != listener {
			continue
		}
		for index, route := range server.Routes {
			if len(route.Match) != 1 || len(route.Match[0].Host) != 1 || len(route.Match[0].Path) != 0 ||
				len(route.Handle) != 1 || route.Handle[0].Handler != "reverse_proxy" {
				continue
			}
			host, err := netip.ParseAddr(route.Match[0].Host[0])
			if err != nil || !host.Is4() || !host.IsPrivate() || host.String() != route.Match[0].Host[0] {
				continue
			}
			routes := append([]caddyRoute(nil), server.Routes[:index]...)
			routes = append(routes, server.Routes[index+1:]...)
			fallback := false
			for _, remaining := range routes {
				fallback = fallback || (len(remaining.Match) == 0 && len(remaining.Handle) == 1 &&
					remaining.Handle[0].Handler == "static_response" && remaining.Handle[0].StatusCode == 404)
			}
			if !fallback {
				continue
			}
			server.Routes = routes
			config.Apps.HTTP.Servers[name] = server
			candidate, err := json.Marshal(config)
			if err != nil || bytes.Equal(candidate, body) {
				t.Fatalf("changed LAN config bytes=%d error=%v", len(candidate), err)
			}
			return gatewayRebindTransferredLANRoute{host: host.String(), port: uint16(port)}, candidate
		}
	}
	t.Fatal("missing selected LAN root reverse-proxy route")
	return gatewayRebindTransferredLANRoute{}, nil
}

func gatewayRebindTransferredCurrentCommandState(t *testing.T, ctx context.Context,
	driver *gatewayRebindMultiDriver, runner *gatewayRebindCompositionRunner,
) gatewayRebindTransferredCurrentCommandSnapshot {
	t.Helper()
	archive, err := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{
		"container", "cp", runner.finalID + ":/config/.", "-"}})
	if err != nil {
		t.Fatal(err)
	}
	copyBytes := func(value []byte) []byte { return append([]byte(nil), value...) }
	return gatewayRebindTransferredCurrentCommandSnapshot{
		candidate:    copyBytes(runner.currentCandidate),
		live:         copyBytes(runner.currentLive),
		next:         copyBytes(runner.currentNext),
		restart:      copyBytes(runner.currentRestart),
		autosave:     copyBytes(runner.currentAutosave),
		archive:      copyBytes(archive.Stdout),
		finalID:      runner.finalID,
		finalPresent: runner.finalPresent,
		finalRunning: runner.final.Running,
	}
}

type gatewayRebindTransferredChallengeCommand struct {
	args                 []string
	token, host, address string
	port                 uint16
	path                 string
}

func gatewayRebindTransferredCurrentChallengeCommand(t *testing.T,
	runner *gatewayRebindCompositionRunner,
) gatewayRebindTransferredChallengeCommand {
	t.Helper()
	var config caddyConfig
	if err := json.Unmarshal(runner.currentLiveBody(), &config); err != nil {
		t.Fatal(err)
	}
	for _, server := range config.Apps.HTTP.Servers {
		if len(server.Listen) != 1 {
			continue
		}
		address, portText, err := net.SplitHostPort(server.Listen[0])
		parsedAddress, addressErr := netip.ParseAddr(address)
		port, portErr := strconv.ParseUint(portText, 10, 16)
		if err != nil || addressErr != nil || !parsedAddress.Is4() || portErr != nil || port == 0 {
			continue
		}
		for _, route := range server.Routes {
			for _, match := range route.Match {
				if len(match.Host) != 1 || len(match.Path) != 1 || !strings.HasPrefix(match.Path[0], gatewayV2ChallengePathPrefix) {
					continue
				}
				parsedHost, hostErr := netip.ParseAddr(match.Host[0])
				if hostErr != nil || !parsedHost.Is4() || !parsedHost.IsPrivate() || parsedHost.String() != match.Host[0] {
					continue
				}
				token := strings.TrimPrefix(match.Path[0], gatewayV2ChallengePathPrefix)
				if !validSHA256(token) {
					continue
				}
				proof := gatewayRebindCompositionConfigProbe(runner.currentLiveBody(), server.Listen[0], match.Host[0], match.Path[0])
				if !proof.Connected || !proof.Responded || proof.Status != 404 || proof.Body != gatewayV2ChallengeBodyPrefix+token {
					continue
				}
				return gatewayRebindTransferredChallengeCommand{
					args: []string{"container", "exec", runner.finalID, "curl", "--disable", "--silent", "--show-error", "--output", "-",
						"--write-out", "\n%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1",
						"--max-time", "2", "--header", "Host: " + match.Host[0],
						"http://" + server.Listen[0] + match.Path[0]},
					token: token, host: match.Host[0], address: parsedAddress.String(), port: uint16(port), path: match.Path[0],
				}
			}
		}
	}
	t.Fatal("current candidate did not contain a canonical challenge route")
	return gatewayRebindTransferredChallengeCommand{}
}

func gatewayRebindTransferredArchiveEntries(t *testing.T, value []byte) map[string]tar.Header {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(value))
	entries := make(map[string]tar.Header)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := io.ReadAll(reader); err != nil {
				t.Fatal(err)
			}
		}
		entries[header.Name] = *header
	}
}

func gatewayRebindTransferredArchiveBody(t *testing.T, value []byte, name string) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(value))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("archive omitted %s", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == name {
			return body
		}
	}
}
