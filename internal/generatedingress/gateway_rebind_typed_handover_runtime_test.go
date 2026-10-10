package generatedingress

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindTypedHandoverRuntimeFixture struct {
	predecessor gatewayRebindPredecessorFixture
	attempt     gatewayRebindPreparedAttempt
	current     gatewayCurrentSelection
	records     []gatewayRebindProgressRecord
	stageDriver gatewayRebindTypedStageDriver
	runtime     gatewayRebindTypedHandoverRuntime
	runner      *gatewayRebindTypedHandoverRuntimeRunner
}

type gatewayRebindTypedHandoverRuntimeRunner struct {
	t                 *testing.T
	intent            gatewayRebindProtectedIntentV2
	effect            gatewayRebindTypedEffectProgress
	predecessorState  gatewayV2RouteState
	predecessor       gatewayV2DockerObservation
	stage             *gatewayRebindTypedStageRuntimeRunner
	stagePresent      bool
	networkPresent    bool
	configPresent     bool
	dataPresent       bool
	final             caddyInspection
	finalRuntime      gatewayContainerRuntime
	finalPresent      bool
	finalID           string
	lostCreateAck     bool
	failFinalCreate   bool
	createReplacement bool
	afterCreate       func()
	afterEffect       func([]string)
	refuseAbsence     *gatewayRebindTypedHandoverAbsenceRequest
	absenceRequests   []gatewayRebindTypedHandoverAbsenceRequest
	requests          [][]string
	effects           [][]string
}

type gatewayRebindTypedHandoverAbsenceRequest struct {
	Address string
	Port    uint16
}

func TestGatewayRebindTypedHandoverRuntimeReconcilesActualFinalCreate(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	guard := func(context.Context) error { return nil }
	effect := fixture.effectAt(12)
	preparation, err := fixture.runtime.prepareHandover(context.Background(), fixture.attempt,
		fixture.current, effect, guard)
	if err != nil {
		stage, stageErr := fixture.runtime.stage.read(context.Background(), fixture.attempt.Intent)
		topology := stageErr == nil && fixture.runtime.stage.networkTopologyMatches(context.Background(),
			fixture.attempt.Intent, stage)
		predecessor, predecessorErr := fixture.runtime.predecessor(context.Background(), fixture.attempt)
		resourceInventory := stageErr == nil && gatewayRebindTypedHandoverResourceInventoryMatches(
			fixture.attempt.Intent, effect, stage, true, false)
		configBinding := gatewayRebindStageConfigVolumeBinding(*effect.ConfigVolume)
		dataBinding := gatewayRebindStageConfigVolumeBinding{Name: effect.DataVolume.Name,
			Mountpoint: effect.DataVolume.Mountpoint, CreatedAt: effect.DataVolume.CreatedAt,
			OwnershipDigest: effect.DataVolume.OwnershipDigest}
		resourceParts := []bool{
			gatewayRebindTypedStageImageMatches(fixture.attempt.Intent, stage), stage.Image.ID == effect.ImageID,
			gatewayRebindTypedStageNetworkMatches(fixture.attempt.Intent, stage, effect.Network),
			stage.StageContainerFound, !stage.FinalContainerFound,
			gatewayRebindTypedStageVolumeMatches(fixture.attempt.Intent, stage.ConfigVolume,
				stage.ConfigVolumeIdentity, stage.ConfigVolumeFound, fixture.attempt.Intent.Identity.ConfigVolume,
				gatewayV2ConfigVolumeRole, &configBinding),
			gatewayRebindTypedStageVolumeMatches(fixture.attempt.Intent, stage.DataVolume,
				stage.DataVolumeIdentity, stage.DataVolumeFound, fixture.attempt.Intent.Identity.DataVolume,
				gatewayV2DataVolumeRole, &dataBinding),
			equalStrings(stage.OwnedContainers, []string{fixture.attempt.Intent.Identity.StageContainer}),
			equalStrings(stage.OwnedVolumes, []string{fixture.attempt.Intent.Identity.ConfigVolume,
				fixture.attempt.Intent.Identity.DataVolume}),
			equalStrings(stage.OwnedNetworks, []string{fixture.attempt.Intent.Identity.IngressNetwork}),
		}
		final, finalRuntime, finalFound, finalErr := fixture.predecessor.manager.inspectNamedGatewayContainer(
			context.Background(), fixture.attempt.Intent.Identity.FinalContainer)
		_, networks, endpointDigest, applicationErr := gatewayRebindTypedApplicationNetworks(context.Background(),
			fixture.predecessor.manager, fixture.attempt.Intent, effect, predecessor, final, finalRuntime, finalFound)
		physical := gatewayRebindTypedHandoverPhysical{Stage: stage, Final: final, FinalRuntime: finalRuntime,
			FinalFound: finalFound, Predecessor: predecessor, ApplicationNetworks: networks}
		ingress := gatewayRebindTypedIngressMembershipMatches(fixture.attempt.Intent, effect, physical)
		config := fixture.runtime.configInventory(context.Background(), fixture.attempt, effect,
			effect.StageContainer.ID, false)
		withdrawn := fixture.runtime.withdrawn(context.Background(), fixture.attempt, effect,
			predecessor, stage.StageContainer.Running, false)
		t.Fatalf("prepare actual handover: %v; stage=%v topology=%t resourceInventory=%t parts=%v predecessor=%v address=%s routes=%t finalErr=%v finalFound=%t applicationErr=%v networks=%d endpoint=%t ingress=%t config=%t withdrawn=%t",
			err, stageErr, topology, resourceInventory, resourceParts, predecessorErr, predecessor.Address,
			validSHA256(predecessor.Routes), finalErr, finalFound, applicationErr, len(networks),
			validSHA256(endpointDigest), ingress, config, withdrawn)
	}
	handover, err := gatewayRebindTypedHandoverIntentFor(fixture.attempt.Intent, fixture.records[11], effect,
		preparation.LocalHostPort, preparation.PredecessorObservationDigest,
		preparation.PredecessorInitiallyRunning, preparation.ApplicationNetworks)
	if err != nil {
		t.Fatalf("build actual handover intent: %v", err)
	}
	effect.HandoverIntent = &handover
	effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), preparation.ApplicationNetworks...)
	fixture.runner.effect = effect
	fixture.runner.stage.effect = effect
	fixture.runner.lostCreateAck = true

	binding, err := fixture.runtime.bindFinalContainer(context.Background(), fixture.attempt.Intent, effect, guard)
	if err != nil {
		t.Fatalf("lost create acknowledgement was not reconciled: %v", err)
	}
	if binding.ID != fixture.runner.finalID || !fixture.runner.finalPresent || fixture.runner.stagePresent ||
		fixture.runner.effectCount("container", "create") != 1 {
		t.Fatalf("final create binding=%#v stage=%t final=%t effects=%#v", binding,
			fixture.runner.stagePresent, fixture.runner.finalPresent, fixture.runner.effects)
	}
	if replay, replayErr := fixture.runtime.bindFinalContainer(context.Background(), fixture.attempt.Intent,
		effect, guard); replayErr != nil || replay != binding || fixture.runner.effectCount("container", "create") != 1 {
		t.Fatalf("idempotent final-create replay=%#v creates=%d error=%v", replay,
			fixture.runner.effectCount("container", "create"), replayErr)
	}
}

func TestGatewayRebindTypedHandoverRuntimeRejectsFinalCreateBoundaryDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindTypedHandoverRuntimeFixture, *bool)
	}{
		{name: "successful create replaced before readback", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, _ *bool) {
			f.runner.createReplacement = true
		}},
		{name: "authority changed after lost acknowledgement", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, stale *bool) {
			f.runner.lostCreateAck = true
			f.runner.afterCreate = func() { *stale = true }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
			effect := fixture.effectAt(12)
			preparation, err := fixture.runtime.prepareHandover(context.Background(), fixture.attempt,
				fixture.current, effect, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			handover, err := gatewayRebindTypedHandoverIntentFor(fixture.attempt.Intent, fixture.records[11], effect,
				preparation.LocalHostPort, preparation.PredecessorObservationDigest,
				preparation.PredecessorInitiallyRunning, preparation.ApplicationNetworks)
			if err != nil {
				t.Fatal(err)
			}
			effect.HandoverIntent = &handover
			effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), preparation.ApplicationNetworks...)
			fixture.runner.effect, fixture.runner.stage.effect = effect, effect
			stale := false
			test.mutate(&fixture, &stale)
			guard := func(context.Context) error {
				if stale {
					return errors.New("injected stale handover authority")
				}
				return nil
			}
			if binding, bindErr := fixture.runtime.bindFinalContainer(context.Background(), fixture.attempt.Intent,
				effect, guard); bindErr == nil || binding != (gatewayRebindFinalContainerBinding{}) {
				t.Fatalf("drifted create returned binding=%#v error=%v", binding, bindErr)
			}
			if fixture.runner.effectCount("container", "create") != 1 {
				t.Fatalf("drifted create count=%d effects=%#v",
					fixture.runner.effectCount("container", "create"), fixture.runner.effects)
			}
		})
	}
}

func TestGatewayRebindTypedHandoverRuntimeRejectsAmbiguousWithdrawalProbe(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	refused := gatewayRebindTypedHandoverAbsenceRequest{Address: fixture.attempt.Intent.SuccessorProfile.SelectedIPv4,
		Port: fixture.attempt.Intent.SuccessorProfile.PortStart}
	appendProgress := func(ctx context.Context, record gatewayRebindProgressRecord) error {
		if err := fixture.appendProgress(ctx, record); err != nil {
			return err
		}
		if record.Sequence == 14 {
			fixture.runner.absenceRequests = nil
			fixture.runner.refuseAbsence = &refused
		}
		return nil
	}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	if result, err := driver.reconcileSuccessorForwardLocked(context.Background(), request, appendProgress); err == nil ||
		!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) {
		t.Fatalf("ambiguous withdrawal probe returned result=%#v error=%v", result, err)
	}
	if !containsGatewayRebindTypedHandoverAbsenceRequest(fixture.runner.absenceRequests, refused) ||
		fixture.runner.predecessor.FinalContainer.Running == false || fixture.runner.final.Running ||
		fixture.runner.effectCount("container", "start") != 0 {
		t.Fatalf("ambiguous withdrawal calls=%#v predecessorRunning=%t finalRunning=%t effects=%#v",
			fixture.runner.absenceRequests, fixture.runner.predecessor.FinalContainer.Running,
			fixture.runner.final.Running, fixture.runner.effects)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 14 || len(history.TerminalsV2) != 0 {
		t.Fatalf("ambiguous probe advanced cutover history: progress=%d terminals=%d err=%v",
			len(history.Progress), len(history.TerminalsV2), err)
	}
}

func TestGatewayRebindTypedHandoverRuntimeCompletesActualForwardSequence(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	fixture.runner.lostCreateAck = true
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	result, err := driver.reconcileSuccessorForwardLocked(context.Background(), request, fixture.appendProgress)
	if err != nil {
		history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		snapshot, snapshotErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
		boundary, boundaryErr := fixture.predecessor.manager.readGatewayRebindTypedAttemptBoundaryLocked(
			context.Background(), request)
		resources, resourcesErr := fixture.runtime.readResources(context.Background(), fixture.attempt,
			fixture.runner.effect, false, true)
		stage, stageErr := fixture.runtime.stage.read(context.Background(), fixture.attempt.Intent)
		topologyOK := stageErr == nil && fixture.runtime.stage.networkTopologyMatches(context.Background(),
			fixture.attempt.Intent, stage)
		inventoryOK := stageErr == nil && gatewayRebindTypedHandoverResourceInventoryMatches(
			fixture.attempt.Intent, fixture.runner.effect, stage, false, true)
		final, finalRuntime, finalFound, finalErr := fixture.predecessor.manager.inspectNamedGatewayContainer(
			context.Background(), fixture.attempt.Intent.Identity.FinalContainer)
		finalOK := finalErr == nil && finalFound && gatewayRebindTypedFinalContainerMatches(fixture.attempt.Intent,
			fixture.runner.effect, final, finalRuntime, normalizeID(final.ID))
		predecessor, predecessorErr := fixture.runtime.predecessor(context.Background(), fixture.attempt)
		protectedHistory, protectedHistoryErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		predecessorSelection, predecessorSelectionErr := gatewayRebindTypedPredecessorSelection(
			fixture.attempt, protectedHistory)
		var predecessorInspect gatewayV2DockerObservation
		var predecessorInspectErr error
		predecessorValid, predecessorSame, predecessorDigestOK := false, false, false
		if predecessorSelectionErr == nil && predecessorSelection.Kind == gatewayCurrentSelectionUpgrade {
			predecessorInspect, predecessorInspectErr = fixture.predecessor.manager.inspectGatewayRebindDocker(
				context.Background(), *predecessorSelection.UpgradeSource, predecessorSelection.Upgrade.State,
				predecessorSelection.Upgrade.Journal)
			predecessorValid = predecessorInspectErr == nil && validGatewayRebindPredecessorDocker(
				*predecessorSelection.UpgradeSource, predecessorSelection.Upgrade.State,
				predecessorSelection.Upgrade.Journal, predecessorInspect)
			predecessorSame = predecessorValid && sameGatewayRebindPredecessorDockerObservation(
				predecessorInspect, predecessorInspect, predecessorSelection.Upgrade.State.Identity)
			predecessorDigest, digestErr := gatewayRebindPredecessorDockerDigest(predecessorInspect,
				predecessorSelection.Upgrade.State.Identity)
			predecessorDigestOK = digestErr == nil && validSHA256(predecessorDigest)
		}
		bindings, networks, endpointDigest, applicationErr := gatewayRebindTypedApplicationNetworks(
			context.Background(), fixture.predecessor.manager, fixture.attempt.Intent, fixture.runner.effect,
			predecessor, final, finalRuntime, finalFound)
		resourceBindings, resourceNetworks, resourceEndpointDigest, resourceApplicationErr :=
			gatewayRebindTypedApplicationNetworks(context.Background(), fixture.predecessor.manager,
				fixture.attempt.Intent, fixture.runner.effect, resources.Predecessor, final, finalRuntime, finalFound)
		ingressOK := false
		if stageErr == nil && finalErr == nil && predecessorErr == nil && applicationErr == nil {
			ingressOK = gatewayRebindTypedIngressMembershipMatches(fixture.attempt.Intent, fixture.runner.effect,
				gatewayRebindTypedHandoverPhysical{Stage: stage, Final: final, FinalRuntime: finalRuntime,
					FinalFound: finalFound, Predecessor: predecessor, ApplicationNetworks: networks})
		}
		configOK := resourcesErr == nil && fixture.runtime.configInventory(context.Background(), fixture.attempt,
			fixture.runner.effect, fixture.runner.effect.FinalContainer.ID, false)
		configRunningOK := resourcesErr == nil && fixture.runtime.configInventory(context.Background(), fixture.attempt,
			fixture.runner.effect, fixture.runner.effect.FinalContainer.ID, true)
		finalRoutesOK := resourcesErr == nil && fixture.runtime.finalRoutes(context.Background(), fixture.attempt,
			fixture.runner.effect, fixture.runner.effect.FinalContainer.ID)
		withdrawnOK := resourcesErr == nil && fixture.runtime.withdrawn(context.Background(), fixture.attempt,
			fixture.runner.effect, resources.Predecessor, false, finalFound && final.Running)
		diagnosticProof := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent,
			Final: gatewayRebindHandoverContainerRunning, FinalID: normalizeID(final.ID),
			PredecessorRunning: resources.Predecessor.Running, PredecessorAddress: resources.Predecessor.Address,
			PredecessorObservationDigest: resources.Predecessor.Observation,
			PredecessorStopDigest:        resources.Predecessor.Observation,
			ApplicationNetworks:          resourceBindings, ApplicationEndpointsDigest: resourceEndpointDigest,
			ConfigVolumePresent: resources.Stage.ConfigVolumeFound,
			DataVolumePresent:   resources.Stage.DataVolumeFound, IngressNetworkPresent: resources.Stage.NetworkFound,
			ConfigDigest: fixture.runner.effect.FinalConfigIntent.ContentDigest,
			RoutesDigest: fixture.runner.effect.FinalConfigIntent.RoutePlan.Digest}
		diagnosticProof.HostDigest, _ = canonicalDigest(struct {
			Network gatewayRebindSuccessorNetworkObservation `json:"network"`
			ID      string                                   `json:"id"`
			Present bool                                     `json:"present"`
			Address gatewayRebindPredecessorAddressState     `json:"predecessorAddress"`
		}{fixture.attempt.Intent.NetworkObservation, resources.Stage.NetworkID, resources.Stage.NetworkFound,
			resources.Predecessor.Address})
		stageInventory, finalInventory := resources.Stage, final
		stageInventory.StageContainer.Mounts, finalInventory.Mounts = nil, nil
		diagnosticProof.InventoryDigest, _ = canonicalDigest(struct {
			Stage        gatewayRebindTypedStageObservation `json:"stage"`
			Final        caddyInspection                    `json:"final"`
			FinalRuntime gatewayContainerRuntime            `json:"finalRuntime"`
			Predecessor  string                             `json:"predecessor"`
			Applications map[string]caddyNetworkInspection  `json:"applications"`
		}{stageInventory, finalInventory, finalRuntime, resources.Predecessor.Observation, resourceNetworks})
		diagnosticProof.Digest, _ = gatewayRebindFinalHandoverObservationDigest(diagnosticProof)
		diagnosticProofValid := validGatewayRebindFinalHandoverObservationValue(diagnosticProof)
		readProof, readPhysical, readErr := fixture.runtime.read(context.Background(), fixture.attempt,
			fixture.runner.effect)
		proof, physical, stableErr := fixture.runtime.stable(context.Background(), fixture.attempt,
			fixture.runner.effect, func(context.Context) error { return nil })
		selectionOK := stableErr == nil && gatewayRebindTypedSelectionAuthorizesAttempt(
			fixture.current, physical.Predecessor.Selection, fixture.attempt)
		ready := stableErr == nil && fixture.runner.effect.HandoverIntent != nil &&
			fixture.runner.effect.FinalContainer != nil && gatewayRebindFinalHandoverReadyForCutover(
			proof, fixture.runner.effect.HandoverIntent.Plan, *fixture.runner.effect.FinalContainer)
		phases := make([]string, 0, len(history.Progress))
		for _, record := range history.Progress {
			phases = append(phases, fmt.Sprintf("%d:%s", record.Record.Sequence, record.Record.Phase))
		}
		t.Fatalf("complete actual handover: %v; progress=%d phases=%v terminals=%d historyErr=%v snapshotErr=%v active=%t phase=%s rollback=%t boundaryErr=%v boundaryProgress=%d resourcesErr=%v stageErr=%v topology=%t inventory=%t finalErr=%v finalFound=%t final=%t predecessorErr=%v predecessor=%#v resourcePredecessorSame=%t protectedHistoryErr=%v predecessorSelectionErr=%v predecessorSelectionKind=%s predecessorInspectErr=%v predecessorValid=%t predecessorSame=%t predecessorDigest=%t predecessorInspect=%#v applicationErr=%v resourceApplicationErr=%v bindings=%#v resourceBindings=%#v networks=%d resourceNetworks=%d endpoint=%t resourceEndpoint=%t ingress=%t configStopped=%t configRunning=%t finalRoutes=%t withdrawn=%t diagnosticProofValid=%t diagnosticProof=%#v absenceRequests=%#v readErr=%v stableErr=%v readVsResources=%t selection=%t ready=%t observation=%#v readObservation=%#v effects=%#v",
			err, len(history.Progress), phases, len(history.TerminalsV2), historyErr, snapshotErr,
			snapshot.Active != nil, snapshot.Phase, snapshot.RollbackAllowed, boundaryErr,
			len(boundary.Progress), resourcesErr, stageErr, topologyOK, inventoryOK, finalErr, finalFound, finalOK,
			predecessorErr, predecessor, reflect.DeepEqual(predecessor, resources.Predecessor), protectedHistoryErr,
			predecessorSelectionErr, predecessorSelection.Kind,
			predecessorInspectErr, predecessorValid, predecessorSame, predecessorDigestOK, predecessorInspect,
			applicationErr, resourceApplicationErr, bindings, resourceBindings, len(networks), len(resourceNetworks),
			validSHA256(endpointDigest), validSHA256(resourceEndpointDigest),
			ingressOK, configOK, configRunningOK, finalRoutesOK, withdrawnOK, diagnosticProofValid, diagnosticProof,
			fixture.runner.absenceRequests, readErr, stableErr,
			reflect.DeepEqual(readPhysical, resources),
			selectionOK, ready, proof, readProof, fixture.runner.effects)
	}
	history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if result.Disposition != appaccess.GatewayRebindDispositionCommit || result.Last.Sequence != 17 ||
		result.Last.Phase != gatewayRebindProgressHandoverCommitted || historyErr != nil ||
		len(history.Progress) != 17 ||
		fixture.runner.effectCount("container", "create") != 1 ||
		fixture.runner.effectCount("container", "start") != 1 ||
		fixture.runner.finalPresent == false || !fixture.runner.final.Running ||
		fixture.runner.predecessor.FinalContainer.Running {
		t.Fatalf("actual handover result=%#v progress=%d historyErr=%v predecessorRunning=%t final=%t/%t effects=%#v", result,
			len(history.Progress), historyErr,
			fixture.runner.predecessor.FinalContainer.Running, fixture.runner.finalPresent,
			fixture.runner.final.Running, fixture.runner.effects)
	}
}

func TestGatewayRebindTypedHandoverRuntimeBindsAutosaveToDurablePhase(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("autosave test request is invalid")
	}
	if _, err := driver.reconcileSuccessorForwardLocked(context.Background(), request, fixture.appendProgress); err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	stageAutosave, err := gatewayRebindCanonicalAutosaveConfig(fixture.runner.stage.stageBody)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(stageAutosave)
	activeAutosave, err := gatewayRebindCanonicalAutosaveConfig(fixture.runner.stage.activeBody)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(activeAutosave)
	for _, test := range []struct {
		name       string
		sequence   uint64
		running    bool
		useActive  bool
		wantAccept bool
	}{
		{name: "seq13 stopped stage autosave", sequence: 13, wantAccept: true},
		{name: "seq13 stopped active autosave", sequence: 13, useActive: true},
		{name: "seq14 stopped stage autosave", sequence: 14, wantAccept: true},
		{name: "seq14 stopped active autosave", sequence: 14, useActive: true},
		{name: "seq15 stopped stage autosave", sequence: 15, wantAccept: true},
		{name: "seq15 stopped active autosave", sequence: 15, useActive: true, wantAccept: true},
		{name: "seq15 running stage autosave", sequence: 15, running: true},
		{name: "seq15 running active autosave", sequence: 15, running: true, useActive: true, wantAccept: true},
		{name: "seq16 stopped stage autosave", sequence: 16},
		{name: "seq16 stopped active autosave", sequence: 16, useActive: true, wantAccept: true},
		{name: "seq17 stopped stage autosave", sequence: 17},
		{name: "seq17 stopped active autosave", sequence: 17, useActive: true, wantAccept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			effect := gatewayRebindTypedHandoverEffectAt(t, history, test.sequence)
			fixture.runner.stage.autosave = append(fixture.runner.stage.autosave[:0], stageAutosave...)
			if test.useActive {
				fixture.runner.stage.autosave = append(fixture.runner.stage.autosave[:0], activeAutosave...)
			}
			containerID := effect.StageContainer.ID
			if effect.FinalContainer != nil {
				containerID = effect.FinalContainer.ID
			}
			accepted := fixture.runtime.configInventory(context.Background(), fixture.attempt, effect,
				containerID, test.running)
			if accepted != test.wantAccept {
				t.Fatalf("sequence=%d running=%t activeAutosave=%t accepted=%t want=%t",
					test.sequence, test.running, test.useActive, accepted, test.wantAccept)
			}
		})
	}
}

func TestGatewayRebindTypedHandoverRuntimeRollsBackStageRemovalBeforeFinalCreate(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	fixture.runner.failFinalCreate = true
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("stage-gap rollback request is invalid")
	}
	result, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
	if err != nil {
		history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		phases := make([]gatewayRebindProgressPhase, 0, len(history.Progress))
		for _, retained := range history.Progress {
			phases = append(phases, retained.Record.Phase)
		}
		predecessor, predecessorErr := fixture.runtime.predecessor(context.Background(), fixture.attempt)
		routesDigest, routesErr := gatewayRebindTypedCheckpointRoutesDigest(fixture.attempt.Checkpoint)
		lastPhase, rollbackRoutes := gatewayRebindProgressPhase(""), ""
		if len(history.Progress) != 0 {
			lastPhase = history.Progress[len(history.Progress)-1].Record.Phase
			last := history.Progress[len(history.Progress)-1].Record
			if last.TypedRollback != nil {
				rollbackRoutes = last.TypedRollback.PredecessorRoutesDigest
			}
		}
		stage, stageErr := fixture.runtime.stage.read(context.Background(), fixture.attempt.Intent)
		t.Fatalf("rollback stage gap: %v; historyErr=%v phases=%v last=%s predecessorErr=%v predecessorRunning=%t predecessorRoutesValid=%t checkpointRoutes=%s rollbackRoutes=%s routesErr=%v stageErr=%v topology=%t stageFound=%t finalFound=%t owned=%v/%v/%v stage=%t final=%t config=%t data=%t network=%t effects=%#v",
			err, historyErr, phases, lastPhase, predecessorErr, predecessor.Running,
			validSHA256(predecessor.Routes), routesDigest,
			rollbackRoutes, routesErr, stageErr,
			stageErr == nil && fixture.runtime.stage.networkTopologyMatches(context.Background(), fixture.attempt.Intent, stage),
			stage.StageContainerFound, stage.FinalContainerFound, stage.OwnedContainers, stage.OwnedVolumes,
			stage.OwnedNetworks, fixture.runner.stagePresent, fixture.runner.finalPresent,
			fixture.runner.configPresent, fixture.runner.dataPresent, fixture.runner.networkPresent, fixture.runner.effects)
	}
	history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if result.Disposition != appaccess.GatewayRebindDispositionAbort ||
		result.Last.Phase != gatewayRebindProgressHandoverRolledBack || historyErr != nil ||
		len(history.Progress) != 15 || fixture.runner.stagePresent || fixture.runner.finalPresent ||
		fixture.runner.configPresent || fixture.runner.dataPresent || fixture.runner.networkPresent ||
		!fixture.runner.predecessor.FinalContainer.Running ||
		fixture.runner.effectCount("container", "create") != 1 ||
		fixture.runner.effectCount("container", "start") != 0 ||
		fixture.runner.effectCount("volume", "rm") != 2 ||
		fixture.runner.effectCount("network", "rm") != 1 {
		t.Fatalf("rollback result=%#v progress=%d historyErr=%v stage=%t final=%t config=%t data=%t network=%t predecessorRunning=%t effects=%#v",
			result, len(history.Progress), historyErr, fixture.runner.stagePresent, fixture.runner.finalPresent,
			fixture.runner.configPresent, fixture.runner.dataPresent, fixture.runner.networkPresent,
			fixture.runner.predecessor.FinalContainer.Running, fixture.runner.effects)
	}
}

func TestGatewayRebindTypedHandoverRuntimeReplaysRollbackAfterFinalRemoval(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("rollback replay request is invalid")
	}
	refused := gatewayRebindTypedHandoverAbsenceRequest{
		Address: fixture.predecessor.state.Profile.SelectedIPv4,
		Port:    fixture.predecessor.state.Profile.PortStart,
	}
	fixture.runner.refuseAbsence = &refused
	ctx, cancel := context.WithCancel(context.Background())
	cancelAfterFinalRemoval := true
	fixture.runner.afterEffect = func(args []string) {
		if cancelAfterFinalRemoval && len(args) == 3 && args[0] == "container" && args[1] == "rm" &&
			normalizeID(args[2]) == fixture.runner.finalID {
			cancelAfterFinalRemoval = false
			cancel()
		}
	}
	if result, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress); err == nil ||
		!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) {
		t.Fatalf("cancelled rollback result=%#v error=%v", result, err)
	}
	if fixture.runner.finalPresent || fixture.runner.predecessor.FinalContainer.Running ||
		!fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
		t.Fatalf("lost rollback acknowledgement final=%t predecessorRunning=%t config=%t data=%t network=%t effects=%#v",
			fixture.runner.finalPresent, fixture.runner.predecessor.FinalContainer.Running,
			fixture.runner.configPresent, fixture.runner.dataPresent, fixture.runner.networkPresent,
			fixture.runner.effects)
	}
	fixture.runner.afterEffect = nil
	fixture.runner.refuseAbsence = nil
	request.Mode = gatewayRebindPhysicalReconcileUndecided
	request.RollbackAllowed = true
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("retained rollback replay request is invalid")
	}
	result, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
	if err != nil {
		t.Fatal(err)
	}
	history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	predecessorStarts, finalStarts := 0, 0
	for _, effect := range fixture.runner.effects {
		if len(effect) != 3 || effect[0] != "container" || effect[1] != "start" {
			continue
		}
		switch normalizeID(effect[2]) {
		case normalizeID(fixture.runner.predecessor.FinalContainer.ID):
			predecessorStarts++
		case fixture.runner.finalID:
			finalStarts++
		}
	}
	// The injected predecessor-address refusal occurs after the predecessor is
	// stopped and before the successor is started. The lost acknowledgement is
	// therefore for removal of the exact stopped final container; replay must
	// restart only the predecessor.
	if result.Disposition != appaccess.GatewayRebindDispositionAbort ||
		result.Last.Phase != gatewayRebindProgressHandoverRolledBack || historyErr != nil ||
		len(history.Progress) != 17 || fixture.runner.finalPresent || fixture.runner.stagePresent ||
		fixture.runner.configPresent || fixture.runner.dataPresent || fixture.runner.networkPresent ||
		!fixture.runner.predecessor.FinalContainer.Running ||
		fixture.runner.effectCount("container", "rm") != 2 ||
		fixture.runner.effectCount("container", "start") != 1 || predecessorStarts != 1 || finalStarts != 0 ||
		fixture.runner.effectCount("volume", "rm") != 2 ||
		fixture.runner.effectCount("network", "rm") != 1 {
		t.Fatalf("replayed rollback result=%#v progress=%d historyErr=%v stage=%t final=%t config=%t data=%t network=%t predecessorRunning=%t effects=%#v",
			result, len(history.Progress), historyErr, fixture.runner.stagePresent, fixture.runner.finalPresent,
			fixture.runner.configPresent, fixture.runner.dataPresent, fixture.runner.networkPresent,
			fixture.runner.predecessor.FinalContainer.Running, fixture.runner.effects)
	}
}

func newGatewayRebindTypedHandoverRuntimeFixture(t *testing.T,
	completeStage bool,
) gatewayRebindTypedHandoverRuntimeFixture {
	return newGatewayRebindTypedHandoverRuntimeFixtureWithApprovals(t, completeStage, nil)
}

func newGatewayRebindTypedHandoverRuntimeFixtureWithApprovals(t *testing.T, completeStage bool,
	prepare func(gatewayRebindPredecessorFixture, *gatewayRebindCommitInput),
) gatewayRebindTypedHandoverRuntimeFixture {
	t.Helper()
	// Match the complete SQL component census before immutable records exist.
	predecessor, input, template := newGatewayRebindCoordinatorFixtureWithPredecessor(t,
		newGatewayRebindPredecessorFixtureWithEndpointAndLANApprover(t, false, '3', uuid.NewString()))
	if prepare != nil {
		prepare(predecessor, &input)
	}
	predecessor.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := template.template.intent.NetworkObservation
		value.OperationID = claim.Spec.OperationID
		value.ClaimRequestDigest = claim.RequestDigest
		value.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
		value.Candidates = append(value.Candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: predecessor.state.Profile.InterfaceID,
			IPv4:        predecessor.state.Profile.SelectedIPv4,
			Prefix:      "192.168.96.0/24",
		})
		sort.Slice(value.Candidates, func(left, right int) bool {
			return gatewayRebindCandidateLess(value.Candidates[left], value.Candidates[right])
		})
		value.HostInterfaces = append(value.HostInterfaces, "192.168.96.0/24")
		sort.Strings(value.HostInterfaces)
		return value, nil
	}
	observation := gatewayRebindFixtureDockerObservation(t, predecessor)
	// The bounded stage fixture was built from an independent retained
	// generation. Rebind uses the same pinned image as the predecessor, so bind
	// the fake prefix to the image that the concrete command runner observes.
	template.template.receipt.Resources.ImageID = normalizeID(observation.Image.ID)
	stageDriver := &gatewayRebindTypedStageDriverFake{t: t, template: template.template}
	driver := managerGatewayRebindCrossStoreDriver{manager: predecessor.manager, stage: stageDriver}
	if _, err := predecessor.manager.commitGatewayRebindWithDriver(context.Background(), predecessor.repository,
		input, driver); err == nil {
		t.Fatal("typed prefix unexpectedly completed without handover driver")
	}
	history, err := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) != 12 {
		t.Fatalf("typed prefix history intents=%d progress=%d error=%v", len(history.IntentsV2), len(history.Progress), err)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(history.IntentsV2[0].Intent, history)
	if err != nil {
		t.Fatal(err)
	}
	current, err := gatewayRebindTypedPredecessorSelection(attempt, history)
	if err != nil {
		t.Fatal(err)
	}
	records := historyProgressRecords(history, attempt.Intent.Generation, attempt.Intent.OperationID)
	effect := *records[11].TypedEffect
	stageBody, err := gatewayRebindTypedStageConfigBytes(attempt.Intent)
	if err != nil {
		t.Fatal(err)
	}
	autosave, err := gatewayRebindCanonicalAutosaveConfig(stageBody)
	if err != nil {
		clear(stageBody)
		t.Fatal(err)
	}
	stage := newGatewayRebindTypedStageRuntimeRunner(t, attempt.Intent, effect, stageBody, autosave)
	active, err := gatewayRebindTypedFinalConfigBytes(attempt.Intent, attempt.Checkpoint,
		effect.FinalConfigIntent.RoutePlan)
	if err != nil {
		clear(stageBody)
		clear(autosave)
		t.Fatal(err)
	}
	stage.activeBody = append([]byte(nil), active...)
	clear(active)
	if completeStage {
		stage.start()
	}
	runner := &gatewayRebindTypedHandoverRuntimeRunner{t: t, intent: attempt.Intent, effect: effect,
		predecessorState: predecessor.state, predecessor: observation, stage: stage,
		stagePresent: true, networkPresent: true, configPresent: true, dataPresent: true,
		finalID: strings.Repeat("9", 64)}
	predecessor.manager.runner = runner
	reads := gatewayRebindTypedHandoverRuntimeReads(t, attempt.Intent, effect.Network.ID,
		func() bool { return runner.networkPresent })
	runtime := gatewayRebindTypedHandoverRuntime{manager: predecessor.manager,
		stage: gatewayRebindTypedStageRuntime{manager: predecessor.manager, reads: reads},
		hostProbe: func(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
			return gatewayRebindTypedHandoverHostProbe(ctx, predecessor.manager.options.HostPort,
				address, port, host, path)
		}, listenerAbsent: runner.listenerAbsent,
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
	t.Cleanup(func() {
		clear(stage.stageBody)
		clear(stage.activeBody)
		clear(stage.autosave)
		clearGatewayV2DockerObservation(&runner.predecessor)
	})
	return gatewayRebindTypedHandoverRuntimeFixture{predecessor: predecessor, attempt: attempt,
		current: current, records: records, stageDriver: stageDriver, runtime: runtime, runner: runner}
}

func containsGatewayRebindTypedHandoverAbsenceRequest(values []gatewayRebindTypedHandoverAbsenceRequest,
	want gatewayRebindTypedHandoverAbsenceRequest,
) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) listenerAbsent(ctx context.Context,
	address string, port uint16,
) bool {
	request := gatewayRebindTypedHandoverAbsenceRequest{Address: address, Port: port}
	r.absenceRequests = append(r.absenceRequests, request)
	if ctx == nil || ctx.Err() != nil || port == 0 ||
		(r.refuseAbsence != nil && *r.refuseAbsence == request) {
		return false
	}
	successor := r.intent.SuccessorProfile
	predecessor := r.predecessorState.Profile
	if address == successor.SelectedIPv4 && port >= successor.PortStart && port <= successor.PortEnd {
		return !(r.stagePresent && r.stage.container.Running) && !(r.finalPresent && r.final.Running)
	}
	if address == predecessor.SelectedIPv4 && port >= predecessor.PortStart && port <= predecessor.PortEnd {
		return !r.predecessor.FinalContainer.Running
	}
	if address != "127.0.0.1" {
		return false
	}
	localHostPort := uint16(0)
	if r.effect.HandoverIntent != nil {
		localHostPort = r.effect.HandoverIntent.Plan.LocalHostPort
	}
	if localHostPort != 0 && r.predecessor.FinalContainer.Running && port == localHostPort {
		return false
	}
	if r.finalPresent && r.final.Running && r.effect.HandoverIntent != nil &&
		port == r.effect.HandoverIntent.Plan.LocalHostPort {
		return false
	}
	return (port >= successor.PortStart && port <= successor.PortEnd) ||
		(port >= predecessor.PortStart && port <= predecessor.PortEnd) || port == localHostPort
}

func gatewayRebindTypedHandoverRuntimeReads(t *testing.T, intent gatewayRebindProtectedIntentV2,
	networkID string, networkPresent func() bool,
) gatewayRebindSuccessorPreflightReads {
	t.Helper()
	baseline := gatewayRebindTypedRetainedNetworkReads(t, intent)
	prefixes, err := baseline.network.docker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := baseline.dockerIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	basePrefixes, baseIDs := append([]netip.Prefix(nil), prefixes...), append([]string(nil), ids...)
	currentPrefixes := func() []netip.Prefix {
		values := append([]netip.Prefix(nil), basePrefixes...)
		if networkPresent != nil && networkPresent() {
			values = append(values, netip.MustParsePrefix(intent.Network.Subnet))
		}
		return values
	}
	currentIDs := func() []string {
		values := append([]string(nil), baseIDs...)
		if networkPresent != nil && networkPresent() {
			values = append(values, networkID)
		}
		sort.Strings(values)
		return values
	}
	return gatewayRebindSuccessorPreflightReads{network: gatewayV2NetworkPlanReads{
		candidates: baseline.network.candidates,
		host:       baseline.network.host,
		docker: func(context.Context) ([]netip.Prefix, error) {
			return currentPrefixes(), nil
		},
	}, dockerIDs: func(context.Context) ([]string, error) { return currentIDs(), nil }}
}

func (f gatewayRebindTypedHandoverRuntimeFixture) effectAt(sequence int) gatewayRebindTypedEffectProgress {
	if sequence <= 0 || sequence > len(f.records) || f.records[sequence-1].TypedEffect == nil {
		f.runner.t.Fatalf("typed effect sequence %d is unavailable", sequence)
	}
	return *f.records[sequence-1].TypedEffect
}

func (f *gatewayRebindTypedHandoverRuntimeFixture) appendProgress(ctx context.Context,
	record gatewayRebindProgressRecord,
) error {
	store, err := newGatewayRebindProgressStore(f.predecessor.manager.options.DataRoot,
		record.Generation, record.OperationID, record.Sequence)
	if err != nil {
		return err
	}
	if err = store.installExact(ctx, record); err != nil {
		return err
	}
	if record.TypedEffect != nil {
		f.runner.effect = *record.TypedEffect
		f.runner.stage.effect = *record.TypedEffect
	}
	return nil
}

func gatewayRebindTypedHandoverEffectAt(t *testing.T, history gatewayRebindProtectedIntentHistory,
	sequence uint64,
) gatewayRebindTypedEffectProgress {
	t.Helper()
	for _, retained := range history.Progress {
		if retained.Record.Sequence == sequence && retained.Record.TypedEffect != nil {
			return *retained.Record.TypedEffect
		}
	}
	t.Fatalf("typed effect sequence %d is unavailable", sequence)
	return gatewayRebindTypedEffectProgress{}
}

func gatewayRebindTypedHandoverHostProbe(_ context.Context, localHostPort uint16, address string, port uint16,
	_ string, path string,
) gatewayV2HostProbeResult {
	if address == "127.0.0.1" && port != localHostPort {
		return gatewayV2HostProbeResult{}
	}
	value := gatewayV2HostProbeResult{Connected: true, Responded: true, Status: 404}
	if strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
		value.Body = gatewayV2ChallengeBodyPrefix + strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
	}
	return value
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) effectCount(prefix ...string) int {
	count := 0
	for _, effect := range r.effects {
		if len(effect) >= len(prefix) && reflect.DeepEqual(effect[:len(prefix)], prefix) {
			count++
		}
	}
	return count
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.requests = append(r.requests, args)
	if len(args) < 2 {
		return runtimeprocess.CommandResult{}, fmt.Errorf("unexpected typed handover command: %v", args)
	}
	if args[1] == "ls" {
		return r.list(args)
	}
	if args[1] == "inspect" {
		return r.inspect(args)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] == "-" {
		return r.archive(args)
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "exec" {
		return r.exec(args)
	}
	if args[0] == "container" {
		switch args[1] {
		case "stop":
			r.effects = append(r.effects, args)
			id := args[len(args)-1]
			switch normalizeID(id) {
			case r.effect.StageContainer.ID:
				r.stopStage()
			case r.finalID:
				r.stopFinal()
			case normalizeID(r.predecessor.FinalContainer.ID):
				r.stopPredecessor()
			default:
				return runtimeprocess.CommandResult{}, errors.New("unexpected typed container stop")
			}
			r.afterPhysicalEffect(args)
			return runtimeprocess.CommandResult{}, nil
		case "rm":
			r.effects = append(r.effects, args)
			id := normalizeID(args[len(args)-1])
			if id == r.effect.StageContainer.ID {
				r.removeStage()
				r.afterPhysicalEffect(args)
				return runtimeprocess.CommandResult{}, nil
			}
			if id == r.finalID {
				r.removeFinal()
				r.afterPhysicalEffect(args)
				return runtimeprocess.CommandResult{}, nil
			}
		case "create":
			r.effects = append(r.effects, args)
			if r.failFinalCreate {
				r.failFinalCreate = false
				r.removeStage()
				r.afterPhysicalEffect(args)
				return runtimeprocess.CommandResult{}, errors.New("injected final create failure")
			}
			r.createFinal(args)
			if r.afterCreate != nil {
				r.afterCreate()
			}
			r.afterPhysicalEffect(args)
			stdout := []byte(r.finalID + "\n")
			if r.createReplacement {
				stdout = []byte(strings.Repeat("8", 64) + "\n")
			}
			if r.lostCreateAck {
				r.lostCreateAck = false
				return runtimeprocess.CommandResult{}, errors.New("lost final create acknowledgement")
			}
			return runtimeprocess.CommandResult{Stdout: stdout}, nil
		case "start":
			r.effects = append(r.effects, args)
			id := normalizeID(args[len(args)-1])
			if id == r.finalID {
				r.startFinal()
				r.afterPhysicalEffect(args)
				return runtimeprocess.CommandResult{}, nil
			}
			if id == normalizeID(r.predecessor.FinalContainer.ID) {
				r.startPredecessor()
				r.afterPhysicalEffect(args)
				return runtimeprocess.CommandResult{}, nil
			}
		}
	}
	if args[0] == "volume" && args[1] == "rm" {
		r.effects = append(r.effects, args)
		name := args[len(args)-1]
		switch name {
		case r.intent.Identity.ConfigVolume:
			r.configPresent = false
		case r.intent.Identity.DataVolume:
			r.dataPresent = false
		default:
			return runtimeprocess.CommandResult{}, errors.New("unexpected typed volume removal")
		}
		r.stage.ownedVolumes = removeGatewayRebindTypedHandoverName(r.stage.ownedVolumes, name)
		r.afterPhysicalEffect(args)
		return runtimeprocess.CommandResult{}, nil
	}
	if args[0] == "network" && args[1] == "rm" {
		r.effects = append(r.effects, args)
		id := normalizeID(args[len(args)-1])
		if id != normalizeID(r.stage.networkID) {
			return runtimeprocess.CommandResult{}, errors.New("unexpected typed network removal")
		}
		r.networkPresent = false
		r.stage.ownedNetworks = nil
		r.afterPhysicalEffect(args)
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, fmt.Errorf("unexpected typed handover command: %s", strings.Join(args, " "))
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) afterPhysicalEffect(args []string) {
	if r.afterEffect != nil {
		r.afterEffect(append([]string(nil), args...))
	}
}

func removeGatewayRebindTypedHandoverName(values []string, name string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != name {
			result = append(result, value)
		}
	}
	return result
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) inspect(args []string) (runtimeprocess.CommandResult, error) {
	name := args[len(args)-1]
	switch args[0] {
	case "image":
		return jsonResult(r.predecessor.Image), nil
	case "volume":
		switch name {
		case caddyVolumeName:
			return gatewayRebindTypedVolumeIdentityResult(r.predecessor.V1Volume, r.predecessor.V1VolumeIdentity)
		case r.predecessorState.Identity.ConfigVolume:
			return gatewayRebindTypedVolumeIdentityResult(r.predecessor.ConfigVolume, r.predecessor.ConfigVolumeIdentity)
		case r.predecessorState.Identity.DataVolume:
			return gatewayRebindTypedVolumeIdentityResult(r.predecessor.DataVolume, r.predecessor.DataVolumeIdentity)
		case r.intent.Identity.ConfigVolume:
			if !r.configPresent {
				return gatewayCurrentPhysicalNotFound("volume")
			}
			return jsonResult(r.stage.configVolume), nil
		case r.intent.Identity.DataVolume:
			if !r.dataPresent {
				return gatewayCurrentPhysicalNotFound("volume")
			}
			return jsonResult(r.stage.dataVolume), nil
		}
	case "network":
		switch name {
		case caddyNetworkName:
			return gatewayCurrentPhysicalNetworkResult(r.predecessor.V1NetworkID, r.predecessor.V1Network)
		case r.predecessorState.Identity.IngressNetwork:
			return gatewayCurrentPhysicalNetworkResult(r.predecessor.IngressNetworkID, r.predecessor.IngressNetwork)
		case r.intent.Identity.IngressNetwork:
			if !r.networkPresent {
				return gatewayCurrentPhysicalNotFound("network")
			}
			return gatewayCurrentPhysicalNetworkResult(r.stage.networkID, r.stage.network)
		default:
			if network, ok := r.applicationNetwork(name); ok {
				return gatewayCurrentPhysicalNetworkResult(r.applicationNetworkID(name), network)
			}
		}
	case "container":
		if name == caddyContainerName || normalizeID(name) == normalizeID(r.predecessor.V1Container.ID) {
			if !r.predecessor.V1ContainerFound {
				return gatewayCurrentPhysicalNotFound("container")
			}
			return jsonResult(gatewayContainerInspection{caddyInspection: r.predecessor.V1Container,
				gatewayContainerRuntime: r.predecessor.V1Runtime}), nil
		}
		if name == r.predecessorState.Identity.FinalContainer || normalizeID(name) == normalizeID(r.predecessor.FinalContainer.ID) {
			if !r.predecessor.FinalContainerFound {
				return gatewayCurrentPhysicalNotFound("container")
			}
			return jsonResult(gatewayContainerInspection{caddyInspection: r.predecessor.FinalContainer,
				gatewayContainerRuntime: r.predecessor.FinalRuntime}), nil
		}
		if name == r.intent.Identity.StageContainer || normalizeID(name) == r.effect.StageContainer.ID {
			if !r.stagePresent {
				return gatewayCurrentPhysicalNotFound("container")
			}
			return jsonResult(gatewayContainerInspection{caddyInspection: r.stage.container,
				gatewayContainerRuntime: r.stage.containerRuntime}), nil
		}
		if name == r.intent.Identity.FinalContainer || normalizeID(name) == r.finalID {
			if !r.finalPresent {
				return gatewayCurrentPhysicalNotFound("container")
			}
			return jsonResult(gatewayContainerInspection{caddyInspection: r.final,
				gatewayContainerRuntime: r.finalRuntime}), nil
		}
		if endpoint, ok := r.endpoint(name); ok {
			return jsonResult(endpoint), nil
		}
	}
	return gatewayCurrentPhysicalNotFound(args[0])
}

func gatewayRebindTypedVolumeIdentityResult(value volumeInspection,
	identity gatewayV1VolumeIdentity,
) (runtimeprocess.CommandResult, error) {
	return jsonResult(gatewayVolumeIdentityInspection{Name: value.Name, Driver: value.Driver, Scope: value.Scope,
		Options: value.Options, Labels: value.Labels, Mountpoint: identity.Mountpoint, CreatedAt: identity.CreatedAt}), nil
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) list(args []string) (runtimeprocess.CommandResult, error) {
	typed := false
	for _, arg := range args {
		if strings.Contains(arg, gatewayRebindIntentDigestLabelKey+"=") {
			typed = true
		}
	}
	var names []string
	if typed {
		switch args[0] {
		case "container":
			if r.stagePresent {
				names = append(names, r.intent.Identity.StageContainer)
			}
			if r.finalPresent {
				names = append(names, r.intent.Identity.FinalContainer)
			}
		case "volume":
			if r.configPresent {
				names = append(names, r.intent.Identity.ConfigVolume)
			}
			if r.dataPresent {
				names = append(names, r.intent.Identity.DataVolume)
			}
		case "network":
			if r.networkPresent {
				names = append(names, r.intent.Identity.IngressNetwork)
			}
		}
	} else {
		switch args[0] {
		case "container":
			names = append(names, r.predecessor.OwnedContainers...)
		case "volume":
			names = append(names, r.predecessor.OwnedVolumes...)
		case "network":
			names = append(names, r.predecessor.OwnedNetworks...)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(names, "\n") + "\n")}, nil
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) archive(args []string) (runtimeprocess.CommandResult, error) {
	id := normalizeID(strings.SplitN(args[2], ":/config/", 2)[0])
	if id == r.effect.StageContainer.ID || id == r.finalID {
		return runtimeprocess.CommandResult{Stdout: r.stage.configArchive()}, nil
	}
	if id == normalizeID(r.predecessor.V1Container.ID) {
		return runtimeprocess.CommandResult{Stdout: gatewayRebindSingleConfigArchive(r.t,
			gatewayV2ActiveConfigFile, r.predecessor.V1RestartConfig)}, nil
	}
	if id == normalizeID(r.predecessor.FinalContainer.ID) {
		return runtimeprocess.CommandResult{Stdout: gatewayRebindSingleConfigArchive(r.t,
			r.predecessorState.Identity.ActiveConfigFilename, r.predecessor.FinalRestartConfig)}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected typed handover archive")
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) exec(args []string) (runtimeprocess.CommandResult, error) {
	id := normalizeID(args[2])
	url := args[len(args)-1]
	if strings.Contains(url, "127.0.0.1:2019/config/") {
		switch id {
		case normalizeID(r.predecessor.V1Container.ID):
			return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.predecessor.V1Config...)}, nil
		case normalizeID(r.predecessor.FinalContainer.ID):
			return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.predecessor.FinalConfig...)}, nil
		case r.finalID:
			return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.stage.activeBody...)}, nil
		}
	}
	if strings.Contains(url, gatewayV2ChallengePathPrefix) {
		challenge := url[strings.Index(url, gatewayV2ChallengePathPrefix)+len(gatewayV2ChallengePathPrefix):]
		return runtimeprocess.CommandResult{Stdout: []byte(gatewayV2ChallengeBodyPrefix + challenge + "\n404")}, nil
	}
	return runtimeprocess.CommandResult{Stdout: []byte("200")}, nil
}

func gatewayRebindSingleConfigArchive(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	return gatewayRebindStageConfigCopyHeadersTar(t,
		[]tar.Header{{Name: name, Typeflag: tar.TypeReg, Size: int64(len(body))}}, body)
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) applicationNetwork(name string) (caddyNetworkInspection, bool) {
	value, ok := r.predecessor.ApplicationNetworks[name]
	if !ok {
		return caddyNetworkInspection{}, false
	}
	containers := make(map[string]caddyNetworkContainerInspection, len(value.Containers))
	for id, member := range value.Containers {
		containers[id] = member
	}
	value.Containers = containers
	predecessorID := normalizeID(r.predecessor.FinalContainer.ID)
	if !r.predecessor.FinalContainer.Running {
		delete(value.Containers, predecessorID)
		delete(value.Containers, r.predecessor.FinalContainer.ID)
	}
	if r.finalPresent && r.final.Running {
		attachment := r.final.Networks[name]
		if attachment == nil || attachment.IPAddress == "" {
			return caddyNetworkInspection{}, false
		}
		value.Containers[r.finalID] = caddyNetworkContainerInspection{Name: r.intent.Identity.FinalContainer,
			IPv4Address: attachment.IPAddress + "/24"}
	}
	for appID, route := range gatewayV2RouteRecords(r.predecessorState) {
		for _, endpoint := range route.Endpoints {
			if endpoint.NetworkName != name {
				continue
			}
			value.Containers[endpoint.ContainerID] = caddyNetworkContainerInspection{
				Name:        endpoint.Component,
				IPv4Address: r.endpointAddress(appID, endpoint.ContainerID, endpoint.NetworkName, endpoint.NetworkAlias) + "/24",
			}
		}
	}
	return value, true
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) applicationNetworkID(name string) string {
	if id := r.predecessor.ApplicationNetworkIDs[name]; id != "" {
		return normalizeID(id)
	}
	return strings.Repeat("7", 64)
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) endpoint(id string) (endpointInspection, bool) {
	routes := gatewayV2RouteRecords(r.predecessorState)
	for appID, route := range routes {
		for _, endpoint := range route.Endpoints {
			if normalizeID(endpoint.ContainerID) != normalizeID(id) {
				continue
			}
			return endpointInspection{ID: endpoint.ContainerID, Labels: map[string]string{
				"io.rig.managed": "generated-runtime", "io.rig.application": appID,
				"io.rig.component": endpoint.Component, "io.rig.slot": string(route.Slot), "io.rig.role": endpoint.Role,
			}, Running: true, Health: "healthy", Networks: map[string]*networkAttachment{
				endpoint.NetworkName: {
					Aliases:   []string{endpoint.NetworkAlias},
					IPAddress: r.endpointAddress(appID, endpoint.ContainerID, endpoint.NetworkName, endpoint.NetworkAlias),
				},
			}}, true
		}
	}
	return endpointInspection{}, false
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) endpointAddress(appID, containerID, networkName, networkAlias string) string {
	key := appID + "\x00" + containerID + "\x00" + networkName + "\x00" + networkAlias
	value := 10
	for _, character := range []byte(key) {
		value = 10 + (value*31+int(character))%200
	}
	return "172.30.0." + strconv.Itoa(value)
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) stopStage() {
	r.stage.container.Running = false
	r.stage.container.Networks = nil
	r.stage.containerRuntime.EffectivePortBindings = map[string][]map[string]string{}
	configured := r.stage.containerRuntime.ConfiguredNetworks[r.intent.Identity.IngressNetwork]
	configured.EndpointID, configured.IPAddress = "", ""
	r.stage.containerRuntime.ConfiguredNetworks[r.intent.Identity.IngressNetwork] = configured
	r.stage.network.Containers = map[string]caddyNetworkContainerInspection{}
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) removeStage() {
	r.stopStage()
	r.stagePresent = false
	r.stage.ownedContainers = nil
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) createFinal(_ []string) {
	r.removeStage()
	r.finalPresent = true
	r.final = r.stage.container
	r.final.ID = r.finalID
	r.final.Name = "/" + r.intent.Identity.FinalContainer
	r.final.Hostname = r.intent.Identity.FinalHostname
	r.final.Cmd = []string{"run", "--config", "/config/" + r.intent.Identity.ActiveConfigFilename}
	r.final.Restart = gatewayV2FinalRestartPolicy
	r.final.Running = false
	r.final.Networks = nil
	r.final.Labels = gatewayRebindTypedStageResourceLabels(r.intent,
		gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)
	bindings := cloneGatewayCurrentPhysicalTestPortBindings(r.stage.container.PortBindings)
	port := strconv.FormatUint(uint64(r.effect.HandoverIntent.Plan.LocalHostPort), 10)
	bindings[strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp"] = []map[string]string{{
		"HostIp": "127.0.0.1", "HostPort": port}}
	r.final.PortBindings = bindings
	r.finalRuntime = gatewayContainerRuntime{EffectivePortBindings: map[string][]map[string]string{},
		ConfiguredNetworks: map[string]gatewayV2ConfiguredNetwork{}}
	r.finalRuntime.ConfiguredNetworks[r.intent.Identity.IngressNetwork] = gatewayV2ConfiguredNetwork{
		NetworkID: r.stage.networkID, GwPriority: caddyGatewayPriority,
		IPAMConfig: &gatewayV2ConfiguredIPAM{IPv4Address: r.intent.Network.ContainerIPv4}}
	for _, network := range r.effect.HandoverIntent.Plan.ApplicationNetworks {
		r.finalRuntime.ConfiguredNetworks[network.Name] = gatewayV2ConfiguredNetwork{NetworkID: network.ID}
	}
	r.stage.ownedContainers = []string{r.intent.Identity.FinalContainer}
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) stopFinal() {
	r.final.Running = false
	r.final.Networks = nil
	r.finalRuntime.EffectivePortBindings = map[string][]map[string]string{}
	for name, configured := range r.finalRuntime.ConfiguredNetworks {
		configured.EndpointID, configured.IPAddress = "", ""
		r.finalRuntime.ConfiguredNetworks[name] = configured
	}
	r.stage.network.Containers = map[string]caddyNetworkContainerInspection{}
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) startFinal() {
	autosave, err := gatewayRebindCanonicalAutosaveConfig(r.stage.activeBody)
	if err != nil {
		r.t.Fatalf("build active final autosave: %v", err)
	}
	clear(r.stage.autosave)
	r.stage.autosave = autosave
	r.final.Running = true
	r.finalRuntime.EffectivePortBindings = cloneGatewayCurrentPhysicalTestPortBindings(r.final.PortBindings)
	r.final.Networks = make(map[string]*networkAttachment, len(r.finalRuntime.ConfiguredNetworks))
	for name, configured := range r.finalRuntime.ConfiguredNetworks {
		configured.EndpointID = strings.Repeat("6", 64)
		configured.IPAddress = "172.31.0.3"
		if name == r.intent.Identity.IngressNetwork {
			configured.IPAddress = r.intent.Network.ContainerIPv4
		}
		r.finalRuntime.ConfiguredNetworks[name] = configured
		r.final.Networks[name] = &networkAttachment{IPAddress: configured.IPAddress, GwPriority: configured.GwPriority}
	}
	prefix := netip.MustParsePrefix(r.intent.Network.Subnet)
	r.stage.network.Containers = map[string]caddyNetworkContainerInspection{r.finalID: {
		Name:        r.intent.Identity.FinalContainer,
		IPv4Address: r.intent.Network.ContainerIPv4 + "/" + strconv.Itoa(prefix.Bits())}}
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) removeFinal() {
	r.stopFinal()
	r.finalPresent = false
	r.final = caddyInspection{}
	r.finalRuntime = gatewayContainerRuntime{}
	r.stage.ownedContainers = nil
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) stopPredecessor() {
	r.predecessor.FinalContainer.Running = false
	r.predecessor.FinalContainer.Networks = nil
	r.predecessor.FinalRuntime.EffectivePortBindings = map[string][]map[string]string{}
	for name, configured := range r.predecessor.FinalRuntime.ConfiguredNetworks {
		configured.EndpointID, configured.IPAddress = "", ""
		r.predecessor.FinalRuntime.ConfiguredNetworks[name] = configured
	}
	r.predecessor.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	r.predecessor.FinalConfig = nil
}

func (r *gatewayRebindTypedHandoverRuntimeRunner) startPredecessor() {
	r.predecessor.FinalContainer.Running = true
	r.predecessor.FinalRuntime.EffectivePortBindings = cloneGatewayCurrentPhysicalTestPortBindings(r.predecessor.FinalContainer.PortBindings)
	r.predecessor.FinalContainer.Networks = make(map[string]*networkAttachment,
		len(r.predecessor.FinalRuntime.ConfiguredNetworks))
	for name, configured := range r.predecessor.FinalRuntime.ConfiguredNetworks {
		configured.EndpointID = strings.Repeat("5", 64)
		if name == r.predecessorState.Identity.IngressNetwork {
			configured.IPAddress = r.predecessorState.Network.ContainerIPv4
		} else {
			network, found := r.predecessor.ApplicationNetworks[name]
			if !found {
				r.t.Fatalf("restore predecessor application network %q", name)
			}
			configured.IPAddress = ""
			for id, member := range network.Containers {
				if normalizeID(id) != normalizeID(r.predecessor.FinalContainer.ID) {
					continue
				}
				prefix, err := netip.ParsePrefix(member.IPv4Address)
				if err != nil || !prefix.Addr().Is4() {
					r.t.Fatalf("restore predecessor application-network address %q: %v", member.IPv4Address, err)
				}
				configured.IPAddress = prefix.Addr().String()
				break
			}
			if configured.IPAddress == "" {
				r.t.Fatalf("restore predecessor application-network member %q", name)
			}
		}
		r.predecessor.FinalRuntime.ConfiguredNetworks[name] = configured
		r.predecessor.FinalContainer.Networks[name] = &networkAttachment{IPAddress: configured.IPAddress,
			GwPriority: configured.GwPriority}
	}
	prefix := netip.MustParsePrefix(r.predecessorState.Network.Subnet)
	r.predecessor.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{
		normalizeID(r.predecessor.FinalContainer.ID): {
			Name:        strings.TrimPrefix(r.predecessor.FinalContainer.Name, "/"),
			IPv4Address: r.predecessorState.Network.ContainerIPv4 + "/" + strconv.Itoa(prefix.Bits()),
		},
	}
	r.predecessor.FinalConfig = append([]byte(nil), r.predecessor.FinalRestartConfig...)
}

var _ runtimeprocess.CommandRunner = (*gatewayRebindTypedHandoverRuntimeRunner)(nil)
