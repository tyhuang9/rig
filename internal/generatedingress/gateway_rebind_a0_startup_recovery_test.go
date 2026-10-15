package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func newGatewayRebindCompositionRawA0(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindCommitInput, *gatewayRebindCompositionRunner, appaccess.GatewayRebindRecoverySnapshot,
) {
	t.Helper()
	fixture, input, driver := newGatewayRebindCompositionFixture(t)
	runner := driver.runner
	fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
		return errors.New("injected interruption after raw A0 SQL claim")
	}
	result, commitErr := fixture.manager.commitGatewayRebindWithDriver(context.Background(),
		fixture.repository, input, driver)
	if commitErr == nil || result != (GatewayRebindCommitResult{}) {
		t.Fatalf("raw A0 interruption result=%#v error=%v", result, commitErr)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		snapshot.Phase != appaccess.GatewayRebindPrepared ||
		snapshot.Active.Claim.V2.State != appaccess.GatewayRebindPrepared ||
		snapshot.Active.Claim.V2.StateSequence != 1 || snapshot.DatabaseCommitObserved ||
		!snapshot.RollbackAllowed || fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
		t.Fatalf("raw A0 SQL boundary=%#v error=%v", snapshot, err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Checkpoints) != 0 || len(history.Intents) != 0 ||
		len(history.IntentsV2) != 0 || len(history.Progress) != 0 ||
		len(history.Terminals) != 0 || len(history.TerminalsV2) != 0 ||
		len(runner.effects) != 0 || runner.stagePresent || runner.finalPresent ||
		runner.networkPresent || runner.configPresent || runner.dataPresent {
		t.Fatalf("raw A0 protected/effect boundary: checkpoints=%d intents=%d/%d progress=%d terminals=%d/%d effects=%v error=%v",
			len(history.Checkpoints), len(history.Intents), len(history.IntentsV2), len(history.Progress),
			len(history.Terminals), len(history.TerminalsV2), runner.effects, err)
	}
	return fixture, input, runner, snapshot
}

func gatewayRebindCompositionA0ProofReader(t *testing.T, fixture gatewayRebindPredecessorFixture,
	runner *gatewayRebindCompositionRunner, snapshot appaccess.GatewayRebindRecoverySnapshot,
) gatewayRebindAdmissionProofReader {
	t.Helper()
	if snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		fixture.manager.gatewayRebindV2NetworkObserver == nil {
		t.Fatal("composition A0 proof input is unavailable")
	}
	ctx := context.Background()
	claim := *snapshot.Active.Claim.V2
	firstNetwork, firstErr := fixture.manager.gatewayRebindV2NetworkObserver(ctx, claim)
	secondNetwork, secondErr := fixture.manager.gatewayRebindV2NetworkObserver(ctx, claim)
	ids, prefixes := runner.networkInventory()
	prefixValues := make([]string, len(prefixes))
	for index := range prefixes {
		prefixValues[index] = prefixes[index].String()
	}
	if firstErr != nil || secondErr != nil || !reflect.DeepEqual(firstNetwork, secondNetwork) ||
		!reflect.DeepEqual(firstNetwork.DockerNetworkIDs, ids) ||
		!reflect.DeepEqual(firstNetwork.DockerPrefixes, prefixValues) {
		t.Fatalf("composition A0 network proof is not the stable backend census: first=%#v second=%#v ids=%v prefixes=%v errors=%v/%v",
			firstNetwork, secondNetwork, ids, prefixValues, firstErr, secondErr)
	}

	// Rehydrate independent read functions from the same canonical observation.
	// The proof core still performs its own two reads and validates the selected
	// plan; this check prevents the test adapter from changing that contract.
	reads := gatewayRebindTypedRetainedNetworkReads(t,
		gatewayRebindProtectedIntentV2{NetworkObservation: firstNetwork})
	firstRead, firstReadErr := readGatewayRebindV2SuccessorNetwork(ctx, claim, reads)
	secondRead, secondReadErr := readGatewayRebindV2SuccessorNetwork(ctx, claim, reads)
	firstCanonical, firstCanonicalErr := newGatewayRebindSuccessorNetworkObservation(firstRead)
	secondCanonical, secondCanonicalErr := newGatewayRebindSuccessorNetworkObservation(secondRead)
	if firstReadErr != nil || secondReadErr != nil || firstCanonicalErr != nil || secondCanonicalErr != nil ||
		!reflect.DeepEqual(firstRead, secondRead) ||
		!reflect.DeepEqual(firstCanonical, firstNetwork) ||
		!reflect.DeepEqual(secondCanonical, firstNetwork) {
		t.Fatalf("composition A0 proof reads changed the backend network contract: read=%v/%v canonical=%v/%v",
			firstReadErr, secondReadErr, firstCanonicalErr, secondCanonicalErr)
	}

	firstDocker := gatewayRebindFixtureDockerObservation(t, fixture)
	secondDocker := gatewayRebindFixtureDockerObservation(t, fixture)
	if !sameGatewayRebindPredecessorDockerObservation(firstDocker, secondDocker, fixture.state.Identity) ||
		!sameGatewayRebindPredecessorDockerObservation(firstDocker, runner.predecessor, fixture.state.Identity) {
		t.Fatal("composition A0 predecessor proof is not the stable backend observation")
	}
	inspectDocker := func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) (gatewayV2DockerObservation, error) {
		value := gatewayRebindFixtureDockerObservation(t, fixture)
		if !sameGatewayRebindPredecessorDockerObservation(value, runner.predecessor, fixture.state.Identity) {
			t.Fatal("composition A0 predecessor backend changed during proof")
		}
		return value, nil
	}
	return gatewayRebindAdmissionFixtureProofReader(t, fixture.manager, inspectDocker, reads)
}

// The public startup path deliberately reads the real host network for raw A0.
// This test-only reconstruction supplies the same stable admission proof seam
// used by the focused proof tests, installs exact P01, and leaves subsequent
// startup recovery on the public path.
func reconstructGatewayRebindCompositionA0(t *testing.T, fixture gatewayRebindPredecessorFixture,
	runner *gatewayRebindCompositionRunner, before appaccess.GatewayRebindRecoverySnapshot,
) gatewayRebindPreparedAttempt {
	t.Helper()
	prepared, err := withCrossStoreFixtureEffectLocks(t, fixture.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			proofReader := gatewayRebindCompositionA0ProofReader(t, fixture, runner, before)
			return fixture.manager.recoverGatewayRebindPreparedAdmissionWithProofLocked(
				context.Background(), fixture.repository, before, proofReader)
		})
	if err != nil {
		t.Fatalf("reconstruct raw A0 protected admission: %v", err)
	}
	afterSQL, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || historyErr != nil || !reflect.DeepEqual(before, afterSQL) ||
		fixture.repository.CheckGatewayRebindFence(context.Background()) == nil ||
		len(history.Checkpoints) != 1 || len(history.Intents) != 0 ||
		len(history.IntentsV2) != 1 || len(history.Progress) != 1 ||
		len(history.Terminals) != 0 || len(history.TerminalsV2) != 0 ||
		prepared.Progress.Sequence != 1 ||
		prepared.Progress.Phase != gatewayRebindProgressSuccessorIntent ||
		!reflect.DeepEqual(prepared.Claim, *before.Active.Claim.V2) ||
		!reflect.DeepEqual(history.Checkpoints[0].Checkpoint, prepared.Checkpoint) ||
		!reflect.DeepEqual(history.IntentsV2[0].Intent, prepared.Intent) ||
		!reflect.DeepEqual(history.Progress[0].Record, prepared.Progress) ||
		len(runner.effects) != 0 || runner.stagePresent || runner.finalPresent ||
		runner.networkPresent || runner.configPresent || runner.dataPresent {
		t.Fatalf("reconstructed A0/P01 boundary: prepared=%#v SQL=%v history=%v checkpoints=%d intents=%d/%d progress=%d terminals=%d/%d effects=%v",
			prepared, err, historyErr, len(history.Checkpoints), len(history.Intents), len(history.IntentsV2),
			len(history.Progress), len(history.Terminals), len(history.TerminalsV2), runner.effects)
	}
	return prepared
}

func TestGatewayRebindConcreteCompositionReconstructsFreshA0ThenRecoversForward(t *testing.T) {
	fixture, input, runner, before := newGatewayRebindCompositionRawA0(t)
	prepared := reconstructGatewayRebindCompositionA0(t, fixture, runner, before)
	retainedFiles, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fresh, _ := newGatewayRebindCompositionRecoveryProcessWithDriver(t, fixture.manager, runner)

	recovered, err := fresh.RecoverGatewayRebindStartup(ctx, fixture.repository)
	if err != nil || !recovered.Recovered ||
		recovered.InitialActivePhase != appaccess.GatewayRebindPrepared ||
		recovered.FinalActivePhase != appaccess.GatewayRebindCommitted ||
		recovered.Disposition != appaccess.GatewayRebindDispositionCommit ||
		!recovered.FenceReleased || !validSHA256(recovered.TerminalReceiptDigest) ||
		recovered.SelectedCurrentAuthority == nil || fresh.gatewayRebindAdmissionBlocked() {
		t.Fatalf("reconstructed A0 forward recovery=%#v error=%v", recovered, err)
	}
	after, err := fixture.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || after.Active != nil || len(after.History) != 1 ||
		after.History[0].Claim.V2 == nil ||
		after.History[0].Claim.V2.State != appaccess.GatewayRebindCommitted ||
		after.History[0].Claim.V2.StateSequence != 4 ||
		after.CurrentSource == nil ||
		after.CurrentSource.OperationID != input.Inspection.Spec.OperationID ||
		after.CurrentSource.TerminalReceiptDigest != recovered.TerminalReceiptDigest ||
		fixture.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatalf("reconstructed A0 committed SQL=%#v error=%v", after, err)
	}
	requireGatewayRebindCompositionSQLPrefix(t, before, after)

	history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Checkpoints) != 1 || len(history.IntentsV2) != 1 ||
		len(history.Progress) != 17 || len(history.TerminalsV2) != 1 ||
		!reflect.DeepEqual(history.Progress[0].Record, prepared.Progress) ||
		history.Progress[16].Record.Phase != gatewayRebindProgressHandoverCommitted ||
		history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionCommit ||
		history.TerminalsV2[0].Receipt.Digest != recovered.TerminalReceiptDigest {
		t.Fatalf("reconstructed A0 committed protected history: checkpoints=%d intents=%d progress=%d terminals=%d error=%v",
			len(history.Checkpoints), len(history.IntentsV2), len(history.Progress), len(history.TerminalsV2), err)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, fresh, retainedFiles)
	if !runner.finalPresent || !runner.final.Running || runner.stagePresent ||
		runner.predecessor.FinalContainer.Running || !runner.networkPresent ||
		!runner.configPresent || !runner.dataPresent {
		t.Fatalf("reconstructed A0 committed physical state: final=%t/%t stage=%t predecessor=%t resources=%t/%t/%t",
			runner.finalPresent, runner.final.Running, runner.stagePresent,
			runner.predecessor.FinalContainer.Running, runner.networkPresent,
			runner.configPresent, runner.dataPresent)
	}
	requireGatewayRebindCompositionReplay(t, fresh, fixture.repository, runner)
}

func TestGatewayRebindConcreteCompositionReconstructsFreshA0ThenPreservesFixedAbort(t *testing.T) {
	fixture, _, runner, before := newGatewayRebindCompositionRawA0(t)
	prepared := reconstructGatewayRebindCompositionA0(t, fixture, runner, before)
	retainedFiles, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fresh, recoveryDriver := newGatewayRebindCompositionRecoveryProcessWithDriver(t, fixture.manager, runner)
	failedForward := false
	recoveryDriver.beforeProgress = func(record gatewayRebindProgressRecord) error {
		if record.Phase == gatewayRebindProgressHandoverCommitted && !failedForward {
			failedForward = true
			return errors.New("injected forward completion failure")
		}
		return nil
	}
	rejectRollbackSQL := &gatewayRebindCompositionRejectTransition{
		Repository:    fixture.repository,
		stopAt:        appaccess.GatewayRebindRolledBack,
		beforeFailure: func() {},
	}
	if result, err := fresh.RecoverGatewayRebindStartup(ctx, rejectRollbackSQL); err == nil ||
		result != (GatewayRebindStartupRecoveryResult{}) || !failedForward ||
		rejectRollbackSQL.rejected != 1 {
		t.Fatalf("fixed-abort interruption result=%#v forwardFailure=%t SQLRefusals=%d error=%v",
			result, failedForward, rejectRollbackSQL.rejected, err)
	}
	retained, err := fixture.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, retained) ||
		fixture.repository.CheckGatewayRebindFence(ctx) == nil {
		t.Fatalf("fixed-abort interruption changed prepared SQL: snapshot=%#v error=%v", retained, err)
	}
	history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 18 || len(history.TerminalsV2) != 1 ||
		!reflect.DeepEqual(history.Progress[0].Record, prepared.Progress) ||
		history.Progress[16].Record.Phase != gatewayRebindProgressRollbackIntent ||
		history.Progress[17].Record.Phase != gatewayRebindProgressHandoverRolledBack ||
		history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionAbort ||
		!validSHA256(history.TerminalsV2[0].Receipt.Digest) {
		t.Fatalf("fixed-abort protected history: progress=%d terminals=%d error=%v",
			len(history.Progress), len(history.TerminalsV2), err)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, fresh, retainedFiles)
	receiptDigest := history.TerminalsV2[0].Receipt.Digest
	effects := len(runner.effects)

	restart, _ := newGatewayRebindCompositionRecoveryProcessWithDriver(t, fresh, runner)
	current, err := restart.InspectGatewayRebindCurrent(ctx, fixture.repository)
	if err != nil || current.ActiveOperationID != before.Active.Claim.V2.Spec.OperationID ||
		current.ActivePhase != appaccess.GatewayRebindPrepared ||
		current.ActiveRecoveryPolicy != appaccess.GatewayRebindRecoveryRollbackOnly ||
		current.FenceReleased {
		t.Fatalf("fixed-abort startup classification=%#v error=%v", current, err)
	}
	recovered, err := restart.RecoverGatewayRebindStartup(ctx, fixture.repository)
	if err != nil || !recovered.Recovered ||
		recovered.InitialActivePhase != appaccess.GatewayRebindPrepared ||
		recovered.FinalActivePhase != appaccess.GatewayRebindRolledBack ||
		recovered.Disposition != appaccess.GatewayRebindDispositionAbort ||
		recovered.TerminalReceiptDigest != receiptDigest || !recovered.FenceReleased ||
		restart.gatewayRebindAdmissionBlocked() || len(runner.effects) != effects {
		t.Fatalf("fixed-abort restart recovery=%#v effects=%v error=%v",
			recovered, runner.effects[effects:], err)
	}
	after, err := fixture.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || after.Active != nil || len(after.History) != 1 ||
		after.History[0].Claim.V2 == nil ||
		after.History[0].Claim.V2.State != appaccess.GatewayRebindRolledBack ||
		after.History[0].Claim.V2.StateSequence != 2 ||
		!reflect.DeepEqual(before.CurrentSource, after.CurrentSource) ||
		fixture.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatalf("fixed-abort final SQL=%#v error=%v", after, err)
	}
	requireGatewayRebindCompositionSQLPrefix(t, before, after)
	if runner.finalPresent || runner.stagePresent || runner.networkPresent ||
		runner.configPresent || runner.dataPresent ||
		!runner.predecessor.FinalContainer.Running {
		t.Fatalf("fixed-abort physical state: final=%t stage=%t predecessor=%t resources=%t/%t/%t",
			runner.finalPresent, runner.stagePresent, runner.predecessor.FinalContainer.Running,
			runner.networkPresent, runner.configPresent, runner.dataPresent)
	}
	requireGatewayRebindCompositionReplay(t, restart, fixture.repository, runner)
}

func gatewayRebindCompositionExpectedEarlyAdoption(t *testing.T, intent gatewayRebindProtectedIntentV2,
	last gatewayRebindProgressRecord, runner *gatewayRebindCompositionRunner, retainedSequence uint64,
) gatewayRebindTypedEffectProgress {
	t.Helper()
	if last.TypedEffect == nil {
		t.Fatal("early lost-ack prefix lacks typed effect")
	}
	effect := *last.TypedEffect
	switch retainedSequence {
	case 2:
		binding, err := gatewayRebindTypedStageNetworkBindingFor(intent, runner.stage.networkID)
		if err != nil {
			t.Fatal(err)
		}
		effect.Network = &binding
	case 3:
		binding, err := gatewayRebindTypedStageConfigVolumeBindingFor(intent,
			runner.stage.configVolume.Mountpoint, runner.stage.configVolume.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		effect.ConfigVolume = &binding
	case 4:
		binding, err := gatewayRebindTypedStageDataVolumeBindingFor(intent,
			runner.stage.dataVolume.Mountpoint, runner.stage.dataVolume.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		effect.DataVolume = &binding
	case 5:
		binding, err := gatewayRebindTypedStageContainerBindingFor(intent, effect,
			normalizeID(runner.stage.container.ID))
		if err != nil {
			t.Fatal(err)
		}
		effect.StageContainer = &binding
	default:
		t.Fatalf("unsupported early lost-ack sequence %d", retainedSequence)
	}
	effect.Version, effect.Purpose, effect.Digest =
		gatewayRebindTypedEffectVersion, gatewayRebindTypedEffectPurpose, ""
	var err error
	effect.Digest, err = gatewayRebindTypedEffectDigest(effect)
	if err != nil {
		t.Fatal(err)
	}
	return effect
}

func requireGatewayRebindCompositionUnboundCreateBoundary(t *testing.T, runner *gatewayRebindCompositionRunner,
	last gatewayRebindProgressRecord, retainedSequence uint64,
) {
	t.Helper()
	wantNetwork := true
	wantConfig := retainedSequence >= 3
	wantData := retainedSequence >= 4
	wantStage := retainedSequence >= 5
	if runner.networkPresent != wantNetwork || runner.configPresent != wantConfig ||
		runner.dataPresent != wantData || runner.stagePresent != wantStage ||
		runner.finalPresent || !runner.predecessor.FinalContainer.Running ||
		len(runner.effects) != int(retainedSequence-1) || last.TypedEffect == nil ||
		!reflect.DeepEqual(runner.effect, *last.TypedEffect) ||
		(last.TypedEffect.Network != nil) != (retainedSequence >= 3) ||
		(last.TypedEffect.ConfigVolume != nil) != (retainedSequence >= 4) ||
		(last.TypedEffect.DataVolume != nil) != (retainedSequence >= 5) ||
		last.TypedEffect.StageContainer != nil {
		t.Fatalf("unbound create boundary sequence=%d physical=%t/%t/%t/%t predecessor=%t effects=%v retained=%#v",
			retainedSequence, runner.networkPresent, runner.configPresent, runner.dataPresent,
			runner.stagePresent, runner.predecessor.FinalContainer.Running, runner.effects, last.TypedEffect)
	}
	for _, effect := range runner.effects {
		if len(effect) < 2 || effect[1] != "create" {
			t.Fatalf("unbound create boundary already compensated: %v", runner.effects)
		}
	}
}

// This digest deliberately excludes runner.effect and runner.stage.effect. A
// successful adoption appends the durable typed binding and only then updates
// that simulated bookkeeping. Physical identity, inventory, presence and
// command effects must remain unchanged while the existing resource is adopted.
func gatewayRebindCompositionEarlyPhysicalDigest(runner *gatewayRebindCompositionRunner) (string, error) {
	return canonicalDigest(struct {
		NetworkID       string
		Network         caddyNetworkInspection
		ConfigVolume    gatewayVolumeIdentityInspection
		DataVolume      gatewayVolumeIdentityInspection
		StageContainer  caddyInspection
		StageRuntime    gatewayContainerRuntime
		StageConfig     []byte
		StageActive     []byte
		StageAutosave   []byte
		OwnedContainers []string
		OwnedVolumes    []string
		OwnedNetworks   []string
		NetworkPresent  bool
		ConfigPresent   bool
		DataPresent     bool
		StagePresent    bool
		FinalContainer  caddyInspection
		FinalRuntime    gatewayContainerRuntime
		FinalID         string
		FinalPresent    bool
		Predecessor     gatewayV2DockerObservation
		Effects         [][]string
	}{
		NetworkID:       runner.stage.networkID,
		Network:         runner.stage.network,
		ConfigVolume:    runner.stage.configVolume,
		DataVolume:      runner.stage.dataVolume,
		StageContainer:  runner.stage.container,
		StageRuntime:    runner.stage.containerRuntime,
		StageConfig:     runner.stage.stageBody,
		StageActive:     runner.stage.activeBody,
		StageAutosave:   runner.stage.autosave,
		OwnedContainers: runner.stage.ownedContainers,
		OwnedVolumes:    runner.stage.ownedVolumes,
		OwnedNetworks:   runner.stage.ownedNetworks,
		NetworkPresent:  runner.networkPresent,
		ConfigPresent:   runner.configPresent,
		DataPresent:     runner.dataPresent,
		StagePresent:    runner.stagePresent,
		FinalContainer:  runner.final,
		FinalRuntime:    runner.finalRuntime,
		FinalID:         runner.finalID,
		FinalPresent:    runner.finalPresent,
		Predecessor:     runner.predecessor,
		Effects:         runner.effects,
	})
}

func gatewayRebindCompositionEffectOccurrences(effects [][]string, expected []string) int {
	result := 0
	for _, effect := range effects {
		if reflect.DeepEqual(effect, expected) {
			result++
		}
	}
	return result
}

func gatewayRebindCompositionRetainsEarlyAdoption(current, expected gatewayRebindTypedEffectProgress,
	retainedSequence uint64,
) bool {
	switch retainedSequence {
	case 2:
		return reflect.DeepEqual(current.Network, expected.Network)
	case 3:
		return reflect.DeepEqual(current.ConfigVolume, expected.ConfigVolume)
	case 4:
		return reflect.DeepEqual(current.DataVolume, expected.DataVolume)
	case 5:
		return reflect.DeepEqual(current.StageContainer, expected.StageContainer)
	default:
		return false
	}
}

func TestGatewayRebindConcreteCompositionRecoversUnboundCreateLostAcknowledgmentsForward(t *testing.T) {
	for _, tc := range []struct {
		name             string
		retainedSequence uint64
	}{
		{name: "network", retainedSequence: 2},
		{name: "config volume", retainedSequence: 3},
		{name: "data volume", retainedSequence: 4},
		{name: "stage container", retainedSequence: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, input, driver := newGatewayRebindCompositionFixture(t)
			runner := driver.runner
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			interrupted := false
			driver.beforeProgress = func(record gatewayRebindProgressRecord) error {
				if record.Sequence != tc.retainedSequence+1 || interrupted {
					return nil
				}
				interrupted = true
				cancel()
				return ctx.Err()
			}
			result, commitErr := fixture.manager.commitGatewayRebindWithDriver(ctx, fixture.repository, input, driver)
			if commitErr == nil || result != (GatewayRebindCommitResult{}) || !interrupted || ctx.Err() == nil {
				t.Fatalf("lost create acknowledgment result=%#v interrupted=%t context=%v error=%v",
					result, interrupted, ctx.Err(), commitErr)
			}

			before, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil || before.Active == nil || before.Active.Claim.V2 == nil ||
				before.Phase != appaccess.GatewayRebindPrepared ||
				before.Active.Claim.V2.StateSequence != 1 || before.DatabaseCommitObserved ||
				!before.RollbackAllowed || fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
				t.Fatalf("lost create SQL boundary=%#v error=%v", before, err)
			}
			retained, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(retained.Checkpoints) != 1 || len(retained.IntentsV2) != 1 ||
				len(retained.Progress) != int(tc.retainedSequence) || len(retained.TerminalsV2) != 0 {
				t.Fatalf("lost create protected prefix: checkpoints=%d intents=%d progress=%d terminals=%d error=%v",
					len(retained.Checkpoints), len(retained.IntentsV2), len(retained.Progress),
					len(retained.TerminalsV2), err)
			}
			last := retained.Progress[len(retained.Progress)-1].Record
			if last.Sequence != tc.retainedSequence || last.Phase != gatewayRebindProgressStageIntent ||
				last.TypedRollback != nil {
				t.Fatalf("lost create retained record=%#v", last)
			}
			requireGatewayRebindCompositionUnboundCreateBoundary(t, runner, last, tc.retainedSequence)
			expectedEffect := gatewayRebindCompositionExpectedEarlyAdoption(t,
				retained.IntentsV2[0].Intent, last, runner, tc.retainedSequence)
			beforePhysicalDigest, err := gatewayRebindCompositionEarlyPhysicalDigest(runner)
			if err != nil {
				t.Fatal(err)
			}
			lostCreate := append([]string(nil), runner.effects[len(runner.effects)-1]...)
			retainedFiles, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}

			fresh, recoveryDriver := newGatewayRebindCompositionRecoveryProcessWithDriver(t, fixture.manager, runner)
			effectStart := len(runner.effects)
			adoptionObserved := false
			adoptionDurableAndPhysicalUnchanged := false
			recoveryDriver.afterProgress = func(record gatewayRebindProgressRecord) error {
				if record.Sequence != tc.retainedSequence+1 {
					return nil
				}
				adoptionObserved = true
				history, scanErr := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				currentPhysicalDigest, digestErr := gatewayRebindCompositionEarlyPhysicalDigest(runner)
				adoptionDurableAndPhysicalUnchanged = scanErr == nil && digestErr == nil &&
					currentPhysicalDigest == beforePhysicalDigest &&
					len(history.Progress) == int(tc.retainedSequence+1) &&
					reflect.DeepEqual(history.Progress[tc.retainedSequence].Record, record) &&
					record.Phase == gatewayRebindProgressStageIntent &&
					record.TypedEffect != nil && record.TypedRollback == nil &&
					reflect.DeepEqual(*record.TypedEffect, expectedEffect) &&
					len(runner.effects) == effectStart
				return nil
			}
			recovered, err := fresh.RecoverGatewayRebindStartup(context.Background(), fixture.repository)
			if err != nil || !recovered.Recovered ||
				recovered.InitialActivePhase != appaccess.GatewayRebindPrepared ||
				recovered.FinalActivePhase != appaccess.GatewayRebindCommitted ||
				recovered.Disposition != appaccess.GatewayRebindDispositionCommit ||
				!recovered.FenceReleased || !validSHA256(recovered.TerminalReceiptDigest) ||
				fresh.gatewayRebindAdmissionBlocked() || !adoptionObserved ||
				!adoptionDurableAndPhysicalUnchanged ||
				gatewayRebindCompositionEffectOccurrences(runner.effects, lostCreate) != 1 {
				t.Fatalf("unbound create recovery=%#v adoption=%t/%t createOccurrences=%d effects=%v error=%v",
					recovered, adoptionObserved, adoptionDurableAndPhysicalUnchanged,
					gatewayRebindCompositionEffectOccurrences(runner.effects, lostCreate),
					runner.effects[effectStart:], err)
			}

			after, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil || after.Active != nil || len(after.History) != 1 ||
				after.History[0].Claim.V2 == nil ||
				after.History[0].Claim.V2.State != appaccess.GatewayRebindCommitted ||
				after.History[0].Claim.V2.StateSequence != 4 ||
				after.CurrentSource == nil ||
				after.CurrentSource.OperationID != input.Inspection.Spec.OperationID ||
				after.CurrentSource.TerminalReceiptDigest != recovered.TerminalReceiptDigest ||
				fixture.repository.CheckGatewayRebindFence(context.Background()) != nil {
				t.Fatalf("unbound create final SQL=%#v error=%v", after, err)
			}
			requireGatewayRebindCompositionSQLPrefix(t, before, after)
			history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.Progress) != 17 || len(history.TerminalsV2) != 1 {
				t.Fatalf("unbound create final protected history: progress=%d terminals=%d error=%v",
					len(history.Progress), len(history.TerminalsV2), err)
			}
			for index := range retained.Progress {
				if !reflect.DeepEqual(retained.Progress[index].Record, history.Progress[index].Record) {
					t.Fatalf("unbound create recovery rewrote retained progress sequence %d", index+1)
				}
			}
			adoption := history.Progress[tc.retainedSequence].Record
			complete := history.Progress[16].Record
			if adoption.Sequence != tc.retainedSequence+1 ||
				adoption.Phase != gatewayRebindProgressStageIntent ||
				adoption.TypedEffect == nil || adoption.TypedRollback != nil ||
				!reflect.DeepEqual(*adoption.TypedEffect, expectedEffect) ||
				complete.Sequence != 17 ||
				complete.Phase != gatewayRebindProgressHandoverCommitted ||
				complete.TypedEffect == nil ||
				!gatewayRebindCompositionRetainsEarlyAdoption(*complete.TypedEffect,
					expectedEffect, tc.retainedSequence) ||
				history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionCommit ||
				history.TerminalsV2[0].Receipt.Digest != recovered.TerminalReceiptDigest {
				t.Fatalf("unbound create adoption=%#v complete=%#v terminal=%#v",
					adoption, complete, history.TerminalsV2[0].Receipt)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, fresh, retainedFiles)
			if !runner.finalPresent || !runner.final.Running || runner.stagePresent ||
				runner.predecessor.FinalContainer.Running || !runner.networkPresent ||
				!runner.configPresent || !runner.dataPresent {
				t.Fatalf("unbound create committed physical state: final=%t/%t stage=%t predecessor=%t resources=%t/%t/%t",
					runner.finalPresent, runner.final.Running, runner.stagePresent,
					runner.predecessor.FinalContainer.Running, runner.networkPresent,
					runner.configPresent, runner.dataPresent)
			}
			requireGatewayRebindCompositionReplay(t, fresh, fixture.repository, runner)
		})
	}
}
