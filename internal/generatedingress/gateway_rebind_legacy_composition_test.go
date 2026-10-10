package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// Seed the historical legacy lifecycle with its existing protected fixtures
// and guarded SQL transitions. This does not claim concrete Docker execution
// for the historical lifecycle; the mixed-format journey starts after this seed.
func gatewayRebindLegacyCommittedSeed(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayCurrentStateFixture, gatewayCurrentSelection,
) {
	t.Helper()
	legacy := newGatewayCurrentStateFixture(t)
	db, err := database.Open(legacy.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := appaccess.New(db)
	directories, err := docker.PrepareControllerDirectories(legacy.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(&gatewayCurrentNoCommandRunner{}, Options{DataRoot: legacy.dataRoot,
		DockerExecutable:      filepath.Join(legacy.dataRoot, "docker-test"),
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		HostPort:         legacy.history.Predecessor.Journal.Source.LocalHostPort,
		RebindFenceCheck: repository.CheckGatewayRebindFence, RebindCurrentStateRepository: repository})
	if err != nil {
		t.Fatal(err)
	}
	f := gatewayRebindPredecessorFixture{manager: manager, repository: repository, db: db,
		state: legacy.predecessor, journal: legacy.history.Predecessor.Journal, store: legacy.history.Predecessor.Store}
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	// Complete the historical fixture's already-switched deployment so the next
	// claim must satisfy the normal quiescent admission census.
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET status='succeeded',phase='completed',updated_at=?,finished_at=? WHERE status='running'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deployments SET status='succeeded',finished_at=? WHERE status='preparing'`, stamp); err != nil {
		t.Fatal(err)
	}
	if err := legacy.store.installBaseline(legacy.baseline); err != nil {
		t.Fatal(err)
	}
	receipt := legacy.receipt
	_, err = withCrossStoreFixtureEffectLocks(t, manager, func() (gatewayRebindPreparedAttempt, error) {
		for _, next := range []appaccess.GatewayRebindState{appaccess.GatewayRebindSuccessorReady,
			appaccess.GatewayRebindDatabaseCommitted, appaccess.GatewayRebindCommitted} {
			snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || snapshot.Active == nil || snapshot.Active.Claim.Legacy == nil || snapshot.CurrentSource == nil {
				t.Fatalf("legacy active SQL seed: %v", err)
			}
			claim := snapshot.Active.Claim.Legacy
			proof := appaccess.GatewayRebindTransitionProof{
				Version: appaccess.GatewayRebindTransitionVersionV1, Purpose: appaccess.GatewayRebindTransitionPurpose,
				OperationID: receipt.OperationID, ClaimRequestDigest: claim.RequestDigest, ClaimSpecDigest: claim.RebindApproval.SpecDigest,
				ExpectedState: snapshot.Phase, ExpectedSequence: claim.StateSequence, NextState: next,
				ExpectedHeadRevisionID: snapshot.CurrentSource.ProfileRevisionID, ExpectedHeadRevisionNumber: snapshot.CurrentSource.ProfileRevisionNumber,
				ExpectedHeadSpecDigest: snapshot.CurrentSource.ProfileSpecDigest, ProtectedGeneration: receipt.Generation,
				ProtectedPhase: string(receipt.TerminalProgressPhase), ProtectedRecordSequence: receipt.TerminalProgressSequence,
				ProtectedRecordDigest: receipt.TerminalProgressDigest, TerminalReceiptDigest: receipt.Digest,
				TerminalDisposition: appaccess.GatewayRebindDispositionCommit,
				// Legacy has no typed checkpoint. Its retained journal/source anchor
				// is the compatibility projection; no V2 artifact is invented.
				PredecessorCheckpointDigest: receipt.Predecessor.JournalDigest, SourceStateVersion: 2,
				SourceStateDigest: receipt.Predecessor.StateDigest,
			}
			if next == appaccess.GatewayRebindDatabaseCommitted {
				proof.SuccessorOperationalStateVersion, proof.SuccessorOperationalStateRevision = legacy.baseline.Version, legacy.baseline.Revision
				proof.SuccessorOperationalStateDigest, proof.TransferManifestDigest = legacy.baseline.Digest, legacy.baseline.TransferManifestDigest
				proof.Transfers = legacy.transfers
			}
			if next == appaccess.GatewayRebindCommitted {
				proof.LocalAttestationDigest = receipt.PhysicalProof.Digest
			}
			if _, err := repository.ApplyGatewayRebindTransition(ctx, proof); err != nil {
				t.Fatalf("legacy SQL %s: %v", next, err)
			}
		}
		return gatewayRebindPreparedAttempt{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active != nil || len(snapshot.History) != 1 || snapshot.History[0].Claim.SpecVersion != 1 {
		t.Fatalf("legacy committed seed: %v", err)
	}
	selected, err := manager.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil || selected.Terminal == nil || selected.Terminal.LegacyReceipt == nil || selected.Terminal.TypedReceipt != nil {
		t.Fatalf("canonical legacy selection: %v", err)
	}
	files, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	delete(files.files, filepath.Base(selected.Store.path))
	entry := snapshot.History[0].RosterV1[0]
	// The historical fixture's endpoint differs from its old SQL component.
	// An ordinary redeploy advances mutable current state through real SQL and
	// protected transitions using the existing bounded physical seed driver.
	selected = gatewayCurrentServingRestoreRedeployFixture(t, f, selected, appaccess.GatewayRebindRosterEntryV2{
		AppID: entry.AppID, ServingDeploymentID: entry.ServingDeploymentID, RouteGeneration: entry.RouteGeneration})
	after, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot.History, after.History) || !reflect.DeepEqual(snapshot.CurrentTransfers, after.CurrentTransfers) ||
		selected.State.Revision <= legacy.baseline.Revision || selected.Terminal.Digest != receipt.Digest {
		t.Fatalf("historical redeploy changed immutable legacy authority: %v", err)
	}
	// Current route bundles are intentionally mutable; freeze/check the original
	// legacy intent, progress, and terminal files independently of that bundle.
	gatewayRebindSequenceRequireRetainedFiles(t, manager, files)
	return f, legacy, selected
}

func TestGatewayRebindLegacyCompositionSeedRetainsCanonicalAuthority(t *testing.T) {
	f, legacy, selected := gatewayRebindLegacyCommittedSeed(t)
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || !gatewayCurrentServingRuntimeCensusMatches(*selected.State, snapshot) {
		t.Fatalf("legacy current/runtime head census: %v", err)
	}
	if selected.Terminal.Format != gatewayRebindAttemptTerminalLegacyV1 ||
		!reflect.DeepEqual(*selected.Terminal.LegacyReceipt, legacy.receipt) {
		t.Fatal("seed converted legacy terminal format")
	}
}

func newGatewayRebindLegacyCompositionFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayCurrentSelection, gatewayRebindCommitInput, *gatewayRebindMultiDriver,
) {
	t.Helper()
	f, _, selected := gatewayRebindLegacyCommittedSeed(t)
	target, err := gatewayCurrentPhysicalTargetFor(selected, *selected.State, nil, nil)
	if err != nil || !validGatewayCurrentPhysicalTarget(target) {
		t.Fatalf("legacy physical target: %v", err)
	}
	legacy := newGatewayCurrentPhysicalExecutor(t, target, *selected.State, *selected.State, f.manager.options.HostPort)
	for _, network := range target.Resources.ApplicationNetworks {
		legacy.networkIDs[network.Name] = network.ID
	}
	legacy.syncFinalNetworks(*selected.State)
	native := &gatewayRebindTypedHandoverRuntimeRunner{t: t, predecessorState: f.state,
		predecessor: gatewayRebindFixtureDockerObservation(t, f)}
	native.stopPredecessor()
	runner := &gatewayRebindCompositionRunner{gatewayRebindTypedHandoverRuntimeRunner: native, before: native}
	backend := &gatewayRebindMultiRunner{entries: []*gatewayRebindCompositionRunner{runner}, legacy: legacy}
	core := &gatewayRebindCompositionDriver{managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{manager: f.manager},
		t: t, runner: runner}
	driver := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: core, backend: backend}
	installCrossStoreFixtureNetworkObserver(f.manager, f.state.Network.Subnet)
	observe := f.manager.gatewayRebindV2NetworkObserver
	f.manager.gatewayRebindV2NetworkObserver = func(ctx context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		if ctx == nil {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid legacy retained host census")
		}
		if err := ctx.Err(); err != nil {
			return gatewayRebindSuccessorNetworkObservation{}, err
		}
		// Retirement has no new claim. Preserve only the current address that
		// this fixture explicitly arranges, rather than deriving host presence
		// from retained Docker or protected artifacts.
		if reflect.DeepEqual(claim, appaccess.GatewayRebindClaimV2{}) {
			profile := selected.State.Profile
			prefix := netip.PrefixFrom(netip.MustParseAddr(profile.SelectedIPv4), 24).Masked().String()
			return gatewayRebindSuccessorNetworkObservation{
				Candidates: []gatewayRebindSuccessorNetworkCandidate{{
					InterfaceID: profile.InterfaceID, IPv4: profile.SelectedIPv4, Prefix: prefix}},
				HostInterfaces: []string{prefix},
			}, nil
		}
		value, err := observe(ctx, claim)
		if err != nil {
			return value, err
		}
		// The retained current address remains present during forward handover
		// and rollback. The outer helper canonicalizes this fixed host census.
		profile := selected.State.Profile
		prefix := netip.PrefixFrom(netip.MustParseAddr(profile.SelectedIPv4), 24).Masked().String()
		value.Candidates = append(value.Candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: profile.InterfaceID, IPv4: profile.SelectedIPv4, Prefix: prefix})
		value.HostInterfaces = append(value.HostInterfaces, prefix)
		return value, nil
	}
	installGatewayRebindMultiNetworkObserver(t, f.manager, backend)
	f.manager.runner, f.manager.gatewayRebindCrossStoreDriver = backend, driver
	// Before there is a typed intent, current attestation already uses the real
	// current driver against the retained legacy executor and global probes.
	_, current := newGatewayRebindRuntimeDrivers(f.manager)
	managed := current.(managedGatewayCurrentPhysicalDriver)
	physical := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	physical.hostProbe, physical.containerProbe = backend.hostProbe, backend.containerProbe
	managed.runtime = physical
	f.manager.gatewayCurrentPhysicalDriver = managed
	profile, err := f.repository.CurrentGatewayProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	profile.Spec.SelectedIPv4, profile.Spec.InterfaceID = "192.168.98.8", "rebind-next-successor"
	input := gatewayRebindSequenceNextInput(t, f, profile.RevisionNumber+1, profile.Spec)
	t.Cleanup(func() {
		clear(legacy.live)
		for _, body := range legacy.files {
			clear(body)
		}
		for _, entry := range backend.entries {
			if entry.stage != nil {
				clear(entry.stage.stageBody)
				clear(entry.stage.activeBody)
				clear(entry.stage.autosave)
			}
		}
		clearGatewayV2DockerObservation(&native.predecessor)
	})
	return f, selected, input, driver
}

func TestGatewayRebindLegacyCompositionCurrentCensus(t *testing.T) {
	f, selected, _, driver := newGatewayRebindLegacyCompositionFixture(t)
	ctx := context.Background()
	proof, err := f.manager.gatewayCurrentPhysicalDriver.attestGatewayCurrentPhysical(ctx, selected)
	if err != nil || !gatewayCurrentPhysicalAttestationAtTarget(proof, driver.backend.legacy.target) || len(driver.backend.effects) != 0 {
		managed := f.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
		_, selectionErr := managed.selectExact(context.Background(), *selected.State)
		physical := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
		inventory, inventoryErr := physical.immutableInventoryValue(context.Background(), driver.backend.legacy.target)
		defer clearGatewayCurrentPhysicalInventory(&inventory)
		t.Fatalf("legacy serving attestation: effects=%v error=%v selection=%v immutable=%v requests=%v", driver.backend.effects, err, selectionErr, inventoryErr, driver.backend.legacy.requests)
	}
	for _, id := range []string{strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("f", 64)} {
		if _, err := driver.backend.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"container", "inspect", id}}); err == nil {
			t.Fatalf("accepted stale or unknown container %s", id)
		}
	}
	for _, command := range [][]string{
		{"image", "inspect", "caddy:latest"},
		{"container", "start", normalizeID(driver.runner.predecessor.FinalContainer.ID)},
	} {
		if _, err := driver.backend.Run(context.Background(), runtimeprocess.CommandRequest{Args: command}); err == nil {
			t.Fatalf("accepted unowned image or retained native mutation: %v", command)
		}
	}
	if len(driver.backend.effects) != 0 {
		t.Fatal("read-only census changed physical state")
	}
}

func TestGatewayRebindLegacyCompositionRetirementCensus(t *testing.T) {
	f, selected, _, driver := newGatewayRebindLegacyCompositionFixture(t)
	ctx := context.Background()
	nativeStage := driver.runner.predecessorState.Identity.StageContainer
	stageResult, stageErr := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{"container", "inspect", nativeStage}})
	if stageErr == nil || string(stageResult.Stderr) != "no such container" {
		t.Fatalf("known absent native stage was not an exact Docker not-found: result=%#v error=%v", stageResult, stageErr)
	}
	unknownStage := nativeStage + "-foreign"
	unknownResult, unknownErr := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{"container", "inspect", unknownStage}})
	if unknownErr == nil || len(unknownResult.Stdout) != 0 || len(unknownResult.Stderr) != 0 || !strings.Contains(unknownErr.Error(), "unowned multi-generation command") {
		t.Fatalf("unknown native stage was accepted as known absence: result=%#v error=%v", unknownResult, unknownErr)
	}
	managed := f.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	if _, err := managed.selectExact(ctx, *selected.State); err != nil {
		t.Fatalf("baseline current selection: %v", err)
	}
	stageFound := driver.runner.predecessor.StageContainerFound
	driver.runner.predecessor.StageContainerFound = true
	presentStage, presentErr := driver.backend.Run(ctx, runtimeprocess.CommandRequest{Args: []string{"container", "inspect", nativeStage}})
	if presentErr != nil || len(presentStage.Stdout) == 0 || len(presentStage.Stderr) != 0 {
		t.Fatalf("known present native stage was not read from the retained observation: result=%#v error=%v", presentStage, presentErr)
	}
	if _, err := managed.selectExact(ctx, *selected.State); err == nil {
		t.Fatal("reappeared native stage was accepted by current selection")
	}
	driver.runner.predecessor.StageContainerFound = stageFound
	empty, err := f.manager.gatewayRebindV2NetworkObserver(ctx, appaccess.GatewayRebindClaimV2{})
	profile := selected.State.Profile
	prefix := netip.PrefixFrom(netip.MustParseAddr(profile.SelectedIPv4), 24).Masked().String()
	wantCandidates := []gatewayRebindSuccessorNetworkCandidate{
		{InterfaceID: profile.InterfaceID, IPv4: profile.SelectedIPv4, Prefix: prefix},
		{InterfaceID: "rebind-next-successor", IPv4: "192.168.98.8", Prefix: "192.168.98.0/24"},
	}
	sort.Slice(wantCandidates, func(i, j int) bool { return gatewayRebindCandidateLess(wantCandidates[i], wantCandidates[j]) })
	if err != nil || !reflect.DeepEqual(empty.Candidates, wantCandidates) || empty.OperationID != "" || empty.ClaimRequestDigest != "" || empty.ProfileSpecDigest != "" {
		t.Fatalf("retirement candidate census=%#v error=%v", empty, err)
	}
	malformed := appaccess.GatewayRebindClaimV2{State: appaccess.GatewayRebindPrepared, StateSequence: 1}
	if _, err := f.manager.gatewayRebindV2NetworkObserver(ctx, malformed); err == nil {
		t.Fatal("malformed nonempty claim bypassed the claim-bound network planner")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.manager.gatewayRebindV2NetworkObserver(canceled, appaccess.GatewayRebindClaimV2{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retirement census error=%v", err)
	}
	if len(driver.backend.effects) != 0 {
		t.Fatal("retirement census controls changed physical state")
	}
}

func TestGatewayRebindConcreteCompositionLegacySource(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "commit"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			f, selected, input, driver := newGatewayRebindLegacyCompositionFixture(t)
			ctx := context.Background()
			legacy := driver.backend.legacy
			retainedID := legacy.target.Resources.FinalContainer.ID
			retainedTargetDigest, err := canonicalDigest(legacy.target)
			if err != nil {
				t.Fatal(err)
			}
			retainedConfig := append([]byte(nil), legacy.files[legacy.target.Identity.Rebind.ActiveConfigFilename]...)
			defer clear(retainedConfig)
			before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || len(before.History) != 1 || len(before.CurrentTransfers) != 1 {
				t.Fatalf("legacy seed history: %v", err)
			}
			entry := input.Inspection.Roster[0]
			ref := appaccess.GatewayBindingRef{AppID: entry.AppID, AllocationID: entry.AllocationID,
				AccessRevisionID: entry.AccessRevisionID, GrantAttemptID: entry.GrantAttemptID}
			original, err := f.repository.ResolveGatewayBinding(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if input.Inspection.Spec.Predecessor.Lineage != selected.State.Lineage ||
				input.Inspection.Spec.Predecessor.SourceStateDigest != selected.State.Digest ||
				entry.PredecessorTransferDigest == nil || *entry.PredecessorTransferDigest != before.CurrentTransfers[0].TransferDigest {
				t.Fatal("typed proposal did not bind the retained legacy current and exact prior transfer")
			}
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			refused := 0
			driver.beforeProgress = func(record gatewayRebindProgressRecord) error {
				if rollback && record.Phase == gatewayRebindProgressHandoverCommitted {
					refused++
					return errors.New("injected completion write failure after legacy handover")
				}
				return nil
			}
			result, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver)
			phase, disposition := appaccess.GatewayRebindCommitted, appaccess.GatewayRebindDispositionCommit
			if rollback {
				phase, disposition = appaccess.GatewayRebindRolledBack, appaccess.GatewayRebindDispositionAbort
			}
			if err != nil || result.FinalPhase != phase || result.Disposition != disposition || !result.FenceReleased || (rollback && refused != 1) {
				history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				t.Fatalf("legacy continuation result=%#v err=%v progress=%d history=%v effects=%v", result, err, len(history.Progress), historyErr, driver.backend.effects)
			}
			afterTargetDigest, targetErr := canonicalDigest(legacy.target)
			if targetErr != nil || driver.backend.legacy != legacy || afterTargetDigest != retainedTargetDigest || !legacy.containerPresent ||
				normalizeID(legacy.container.ID) != normalizeID(retainedID) || legacy.container.Running != rollback ||
				!reflect.DeepEqual(retainedConfig, legacy.files[legacy.target.Identity.Rebind.ActiveConfigFilename]) ||
				len(driver.backend.entries) != 1 || driver.runner.predecessor.FinalContainer.Running ||
				driver.runner.stagePresent || driver.runner.finalPresent == rollback || driver.runner.final.Running == rollback ||
				driver.runner.configPresent == rollback || driver.runner.dataPresent == rollback || driver.runner.networkPresent == rollback {
				t.Fatal("typed handover replaced retained legacy resources or left the wrong gateway serving")
			}
			starts, stops := 0, 0
			for _, effect := range driver.backend.effects {
				name := effect[len(effect)-1]
				if effect[0] == "container" && (effect[1] == "start" || effect[1] == "stop") {
					if normalizeID(name) == normalizeID(driver.runner.predecessor.FinalContainer.ID) {
						t.Fatal("changed retained native serving state")
					}
					if normalizeID(name) == normalizeID(retainedID) {
						if effect[1] == "start" {
							starts++
						} else {
							stops++
						}
					}
				}
				if effect[1] == "rm" && (normalizeID(name) == normalizeID(retainedID) ||
					normalizeID(name) == normalizeID(legacy.target.Resources.IngressNetwork.ID) ||
					name == legacy.target.Identity.Rebind.ConfigVolume || name == legacy.target.Identity.Rebind.DataVolume) {
					t.Fatal("removed retained legacy resource")
				}
			}
			wantStarts := 0
			if rollback {
				wantStarts = 1
			}
			if stops != 1 || starts != wantStarts {
				t.Fatalf("legacy effects: starts=%d stops=%d", starts, stops)
			}
			if len(legacy.effects) != 1+wantStarts {
				t.Fatalf("unexpected retained legacy effects: %v", legacy.effects)
			}
			for index, effect := range legacy.effects {
				verb := "stop"
				if index == 1 {
					verb = "start"
				}
				if len(effect) < 3 || effect[0] != "container" || effect[1] != verb || normalizeID(effect[len(effect)-1]) != normalizeID(retainedID) {
					t.Fatalf("unexpected retained legacy effect: %v", effect)
				}
			}
			after, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || len(after.History) != 2 || after.Active != nil || !reflect.DeepEqual(before.History[0], after.History[0]) {
				t.Fatalf("typed operation changed legacy SQL history: %v", err)
			}
			resolved, err := f.repository.ResolveGatewayBinding(ctx, ref)
			wantTransfers := 2
			if rollback {
				wantTransfers = 1
			}
			if err != nil || len(resolved.TransferChain) != wantTransfers ||
				!reflect.DeepEqual(resolved.TransferChain[0], before.CurrentTransfers[0]) ||
				!reflect.DeepEqual(resolved.RawAllocation, original.RawAllocation) ||
				!reflect.DeepEqual(resolved.RawAccessRevision, original.RawAccessRevision) ||
				!reflect.DeepEqual(resolved.RawGrant, original.RawGrant) || !reflect.DeepEqual(resolved.RawProfile, original.RawProfile) {
				t.Fatalf("typed operation changed raw authority or transfer chain: %v", err)
			}
			if !rollback && (resolved.TransferChain[1].PredecessorTransferDigest == nil ||
				*resolved.TransferChain[1].PredecessorTransferDigest != before.CurrentTransfers[0].TransferDigest ||
				resolved.CurrentGatewaySource != result.SelectedCurrentAuthority) {
				t.Fatal("typed commit did not extend the exact legacy transfer chain")
			}
			if rollback && !reflect.DeepEqual(before.CurrentSource, after.CurrentSource) {
				t.Fatal("rollback replaced legacy current authority")
			}
			retained, err := selected.Store.load()
			if err != nil || !reflect.DeepEqual(retained, *selected.State) {
				t.Fatalf("legacy protected bundle changed: %v", err)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
			files, err = readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			effects := len(driver.backend.effects)
			fresh := freshGatewayRebindRecoveryManager(f.manager)
			fresh.gatewayRebindFailStop, fresh.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
			// Recovery managers deliberately omit fixture-only observers. Preserve
			// this retained host census before installing the shared multi backend.
			fresh.gatewayRebindV2NetworkObserver = f.manager.gatewayRebindV2NetworkObserver
			replay := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: &gatewayRebindCompositionDriver{
				t: t, runner: driver.runner}, backend: driver.backend}
			replay.installMulti(fresh)
			recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
			if err != nil || recovered.Recovered || !recovered.FenceReleased || !validSHA256(recovered.CurrentAttestationDigest) ||
				fresh.gatewayRebindAdmissionBlocked() || len(driver.backend.effects) != effects {
				t.Fatalf("legacy-source replay=%#v effects=%v err=%v", recovered, driver.backend.effects[effects:], err)
			}
			confirmed, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			confirmedFiles, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
			if err != nil || filesErr != nil || !reflect.DeepEqual(after, confirmed) || !sameGatewayHistorySnapshot(files, confirmedFiles) {
				t.Fatalf("fresh replay changed SQL/files: SQL=%v files=%v", err, filesErr)
			}
			if recovered.SelectedCurrentAuthority == nil || !reflect.DeepEqual(recovered.SelectedCurrentAuthority, confirmed.CurrentSource) {
				t.Fatal("fresh replay selected a different SQL authority")
			}
			current, err := fresh.selectGatewayCurrentLocked(ctx, confirmed)
			if err != nil || current.Terminal == nil {
				t.Fatalf("fresh canonical selection: %v", err)
			}
			if rollback {
				if current.Terminal.Format != gatewayRebindAttemptTerminalLegacyV1 || current.Terminal.TypedReceipt != nil ||
					!reflect.DeepEqual(current.Terminal.LegacyReceipt, selected.Terminal.LegacyReceipt) {
					t.Fatal("rollback replay converted or replaced the retained legacy receipt")
				}
			} else if current.Terminal.Format != gatewayRebindAttemptTerminalTypedV2 || current.Terminal.LegacyReceipt != nil ||
				current.Terminal.TypedReceipt == nil || current.Terminal.OperationID != input.Inspection.Spec.OperationID ||
				current.Terminal.Digest != result.TerminalReceiptDigest || current.Terminal.TypedReceipt.Digest != result.TerminalReceiptDigest {
				t.Fatal("commit replay did not select the exact typed terminal receipt")
			}
		})
	}
}
