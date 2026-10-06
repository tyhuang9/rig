package generatedingress

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindTypedImageRunner struct{ image imageInspection }

func gatewayRebindTypedRetainedNetworkReads(t *testing.T,
	intent gatewayRebindProtectedIntentV2,
) gatewayRebindSuccessorPreflightReads {
	t.Helper()
	candidates := make([]hostNetworkCandidate, len(intent.NetworkObservation.Candidates))
	for index, value := range intent.NetworkObservation.Candidates {
		prefix, err := netip.ParsePrefix(value.Prefix)
		if err != nil {
			t.Fatal(err)
		}
		candidates[index] = hostNetworkCandidate{InterfaceID: value.InterfaceID, IPv4: value.IPv4, Prefix: prefix}
	}
	parse := func(values []string) []netip.Prefix {
		result := make([]netip.Prefix, len(values))
		for index, value := range values {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				t.Fatal(err)
			}
			result[index] = prefix
		}
		return result
	}
	routes := parse(intent.NetworkObservation.HostRoutes)
	interfaces := parse(intent.NetworkObservation.HostInterfaces)
	docker := parse(intent.NetworkObservation.DockerPrefixes)
	ids := append([]string(nil), intent.NetworkObservation.DockerNetworkIDs...)
	return gatewayRebindSuccessorPreflightReads{network: gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return append([]hostNetworkCandidate(nil), candidates...), nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{Routes: append([]netip.Prefix(nil), routes...),
				Interfaces: append([]netip.Prefix(nil), interfaces...)}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			return append([]netip.Prefix(nil), docker...), nil
		},
	}, dockerIDs: func(context.Context) ([]string, error) { return append([]string(nil), ids...), nil }}
}

func (r gatewayRebindTypedImageRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
		body, err := json.Marshal(r.image)
		return runtimeprocess.CommandResult{Stdout: body}, err
	}
	if len(args) >= 2 && args[1] == "inspect" {
		return runtimeprocess.CommandResult{Stderr: []byte("Error: No such " + args[0])}, errors.New("not found")
	}
	if len(args) >= 2 && args[1] == "ls" {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage Docker command: " + strings.Join(args, " "))
}

type gatewayRebindTypedStageDriverFake struct {
	t        *testing.T
	template gatewayCurrentStateFixture
	guards   int
	steps    []string
}

func (f *gatewayRebindTypedStageDriverFake) guard(ctx context.Context, value gatewayRebindTypedEffectGuard) {
	f.t.Helper()
	if value == nil || value(ctx) != nil {
		f.t.Fatal("typed stage fake received stale effect authority")
	}
	f.guards++
}

func (f *gatewayRebindTypedStageDriverFake) observeImage(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	guard gatewayRebindTypedEffectGuard,
) (string, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "image")
	return f.template.receipt.Resources.ImageID, nil
}

func (f *gatewayRebindTypedStageDriverFake) bindNetwork(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageNetworkBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "network")
	return gatewayRebindTypedStageNetworkBindingFor(intent, f.template.receipt.Resources.IngressNetwork.ID)
}

func (f *gatewayRebindTypedStageDriverFake) bindConfigVolume(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageConfigVolumeBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "config-volume")
	value := f.template.receipt.Resources.ConfigVolume
	return gatewayRebindTypedStageConfigVolumeBindingFor(intent, value.Mountpoint, value.CreatedAt)
}

func (f *gatewayRebindTypedStageDriverFake) bindDataVolume(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageDataVolumeBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "data-volume")
	value := f.template.receipt.Resources.DataVolume
	return gatewayRebindTypedStageDataVolumeBindingFor(intent, value.Mountpoint, value.CreatedAt)
}

func (f *gatewayRebindTypedStageDriverFake) bindStageContainer(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageContainerBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "stage-container")
	return gatewayRebindTypedStageContainerBindingFor(intent, effect,
		f.template.receipt.Resources.StageContainer.ID)
}

func (f *gatewayRebindTypedStageDriverFake) copyStageConfig(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "stage-config")
	return nil
}

func (f *gatewayRebindTypedStageDriverFake) serveStage(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (string, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "stage-serving")
	return f.template.receipt.Resources.StageContainer.ID, nil
}

func (f *gatewayRebindTypedStageDriverFake) copyFinalConfig(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	_ gatewayRebindPredecessorCheckpoint, _ gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) error {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "final-config")
	return nil
}

func (f *gatewayRebindTypedStageDriverFake) prepareHandover(ctx context.Context,
	_ gatewayRebindPreparedAttempt, _ gatewayCurrentSelection, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedHandoverPreparation, error) {
	f.guard(ctx, guard)
	if effect.FinalConfigIntent == nil {
		return gatewayRebindTypedHandoverPreparation{}, errors.New("missing typed final route plan")
	}
	return gatewayRebindTypedHandoverPreparation{
		LocalHostPort:                f.template.history.Progress[12].Record.Handover.Plan.LocalHostPort,
		PredecessorObservationDigest: f.template.receipt.PhysicalProof.Observation.PredecessorObservationDigest,
		PredecessorInitiallyRunning:  true,
		ApplicationNetworks: gatewayRebindTypedFixtureApplicationNetworks(f.t,
			effect.FinalConfigIntent.RoutePlan, f.template.receipt.Resources.ApplicationNetworks),
	}, nil
}

func (f *gatewayRebindTypedStageDriverFake) bindFinalContainer(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalContainerBinding, error) {
	f.guard(ctx, guard)
	return gatewayRebindTypedFinalContainerBindingFor(intent, effect, effect.HandoverIntent.Plan,
		f.template.receipt.Resources.FinalContainer.ID)
}

func (f *gatewayRebindTypedStageDriverFake) prepareCutover(ctx context.Context,
	_ gatewayRebindPreparedAttempt, _ gatewayCurrentSelection, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverObservation, error) {
	f.guard(ctx, guard)
	value := f.template.receipt.PhysicalProof.Observation
	value.Stage, value.Final = gatewayRebindHandoverContainerAbsent, gatewayRebindHandoverContainerStopped
	value.FinalID = effect.FinalContainer.ID
	value.PredecessorRunning = effect.HandoverIntent.Plan.PredecessorInitiallyRunning
	value.PredecessorObservationDigest = effect.HandoverIntent.Plan.PredecessorObservationDigest
	value.PredecessorStopDigest, value.PredecessorRoutesDigest, value.RoutesDigest = "", "", ""
	value.ConfigDigest = effect.HandoverIntent.Plan.FinalConfigDigest
	value.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), effect.ApplicationNetworks...)
	value.Digest = ""
	var err error
	value.Digest, err = gatewayRebindFinalHandoverObservationDigest(value)
	return value, err
}

func (f *gatewayRebindTypedStageDriverFake) serveSuccessor(ctx context.Context,
	_ gatewayRebindPreparedAttempt, _ gatewayCurrentSelection, effect gatewayRebindTypedEffectProgress,
	mode gatewayRebindPhysicalReconcileMode, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverObservation, error) {
	f.guard(ctx, guard)
	if mode == gatewayRebindPhysicalReconcileRollbackOnly {
		return gatewayRebindFinalHandoverObservation{}, errors.New("rollback authority cannot publish successor")
	}
	value := f.template.receipt.PhysicalProof.Observation
	value.Stage, value.Final = gatewayRebindHandoverContainerAbsent, gatewayRebindHandoverContainerRunning
	value.FinalID, value.PredecessorRunning = effect.FinalContainer.ID, false
	value.PredecessorObservationDigest = effect.HandoverIntent.Plan.PredecessorObservationDigest
	value.PredecessorStopDigest = value.PredecessorObservationDigest
	value.ConfigDigest, value.RoutesDigest = effect.HandoverIntent.Plan.FinalConfigDigest,
		effect.HandoverIntent.Plan.RoutePlanDigest
	value.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), effect.ApplicationNetworks...)
	value.Digest = ""
	var err error
	value.Digest, err = gatewayRebindFinalHandoverObservationDigest(value)
	return value, err
}

func (f *gatewayRebindTypedStageDriverFake) finalizeSuccessor(ctx context.Context,
	_ gatewayRebindPreparedAttempt, _ gatewayCurrentSelection, previous gatewayRebindProgressRecord,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverResourceBindings, gatewayRebindFinalHandoverTerminalProof, error) {
	f.guard(ctx, guard)
	resources := gatewayRebindFinalHandoverResourceBindings{ImageID: effect.ImageID,
		IngressNetwork: *effect.Network, ConfigVolume: *effect.ConfigVolume, DataVolume: *effect.DataVolume,
		StageContainer: *effect.StageContainer, FinalContainer: effect.FinalContainer,
		ApplicationNetworks: append([]gatewayRebindHandoverApplicationNetwork(nil), effect.ApplicationNetworks...)}
	var err error
	resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
	if err != nil {
		return gatewayRebindFinalHandoverResourceBindings{}, gatewayRebindFinalHandoverTerminalProof{}, err
	}
	proof := gatewayRebindFinalHandoverTerminalProof{Version: gatewayRebindFinalHandoverProgressVersion,
		Context: gatewayRebindFinalHandoverOutcomeContext, Kind: gatewayRebindFinalHandoverOutcomeCommit,
		PriorProgressDigest: previous.Digest, Observation: effect.SuccessorServing.Observation}
	proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	return resources, proof, err
}

func TestGatewayRebindTypedStageDriverPersistsCompleteStagePrefix(t *testing.T) {
	fixture, input, bounded := newGatewayRebindCoordinatorFixture(t)
	fake := &gatewayRebindTypedStageDriverFake{t: t, template: bounded.template}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager, stage: fake}

	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("incomplete typed final handover unexpectedly committed")
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) != 12 ||
		history.Progress[11].Record.Sequence != 12 ||
		history.Progress[11].Record.Phase != gatewayRebindProgressFinalConfigCopied ||
		history.Progress[11].Record.TypedEffect == nil ||
		history.Progress[11].Record.TypedEffect.FinalConfigCopy == nil ||
		len(history.TerminalsV2) != 0 || fake.guards < len(fake.steps) || len(fake.steps) != 8 {
		t.Fatalf("typed stage prefix progress=%d terminals=%d steps=%v guards=%d error=%v",
			len(history.Progress), len(history.TerminalsV2), fake.steps, fake.guards, err)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != "prepared" || !snapshot.RollbackAllowed {
		t.Fatalf("typed stage fence snapshot=%#v error=%v", snapshot, err)
	}
	intent := history.IntentsV2[0].Intent
	effect := history.Progress[4].Record.TypedEffect
	if effect == nil || effect.ConfigVolume == nil {
		t.Fatal("typed config-volume binding is unavailable")
	}
	observed := volumeInspection{Name: intent.Identity.ConfigVolume, Driver: "local", Scope: "local",
		Options: map[string]string{}, Labels: gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)}
	identity := gatewayV1VolumeIdentity{Mountpoint: effect.ConfigVolume.Mountpoint,
		CreatedAt: effect.ConfigVolume.CreatedAt}
	if !gatewayRebindTypedStageVolumeMatches(intent, observed, identity, true, intent.Identity.ConfigVolume,
		gatewayV2ConfigVolumeRole, effect.ConfigVolume) {
		t.Fatal("exact retained config-volume identity was refused")
	}
	replaced := identity
	replaced.Mountpoint += "-replacement"
	if reflect.DeepEqual(replaced, identity) || gatewayRebindTypedStageVolumeMatches(intent, observed, replaced, true,
		intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole, effect.ConfigVolume) {
		t.Fatal("same-name replacement config volume was accepted")
	}
	actualManager := &Manager{runner: gatewayRebindTypedImageRunner{image: imageInspection{
		ID: "sha256:" + bounded.template.receipt.Resources.ImageID, OS: "linux",
		RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest},
	}}, options: Options{DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Second}}
	reads := gatewayRebindTypedRetainedNetworkReads(t, intent)
	runtime := gatewayRebindTypedStageRuntime{manager: actualManager, reads: reads}
	imageID, imageErr := runtime.observeImage(
		context.Background(), intent, func(context.Context) error { return nil })
	if imageErr != nil || imageID != bounded.template.receipt.Resources.ImageID {
		t.Fatalf("normalized typed image id=%q error=%v", imageID, imageErr)
	}
	drifted := reads
	drifted.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
		value, err := reads.network.host()
		value.Routes = append(value.Routes, netip.MustParsePrefix("198.18.0.0/15"))
		return value, err
	}
	if _, err := (gatewayRebindTypedStageRuntime{manager: actualManager, reads: drifted}).observeImage(
		context.Background(), intent, func(context.Context) error { return nil }); err == nil {
		t.Fatal("typed stage accepted a changed host route census")
	}
	started := history.Progress[8].Record.TypedEffect
	if started == nil || started.StageStartIntent == nil || started.StageContainer == nil {
		t.Fatal("typed durable stage-start prefix is unavailable")
	}
	stageBody, err := gatewayRebindTypedStageConfigBytes(intent)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(stageBody)
	autosave, err := gatewayRebindCanonicalAutosaveConfig(stageBody)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(autosave)
	archive := stageAutosaveTestArchive(t, stageBody, autosave)
	archiveRunner := &gatewayRebindStageConfigIntentRunner{result: runtimeprocess.CommandResult{Stdout: archive}}
	fixture.manager.runner = archiveRunner
	actualStage := gatewayRebindTypedStageRuntime{manager: fixture.manager}
	if got, err := actualStage.stageConfigInventory(context.Background(), *started, stageBody, true); err != nil ||
		got != gatewayRebindStageConfigInventoryExact || len(archiveRunner.requests) != 1 ||
		archiveRunner.requests[0].OutputLimit != gatewayRebindStartedStageConfigArchiveLimit {
		t.Fatalf("typed post-start autosave inventory=%d requests=%#v error=%v", got, archiveRunner.requests, err)
	}
	if _, err := actualStage.stageConfigInventory(context.Background(), *started, stageBody, false); err == nil {
		t.Fatal("typed pre-start inventory admitted an autosave")
	}
	archiveRunner.result.Stdout = stageAutosaveTestArchive(t, stageBody, []byte(`{"tampered":true}`))
	if _, err := actualStage.stageConfigInventory(context.Background(), *started, stageBody, true); err == nil {
		t.Fatal("typed post-start inventory admitted a changed autosave")
	}
	headers := []tar.Header{{Name: ".", Typeflag: tar.TypeDir},
		gatewayRebindPinnedImageConfigDirectoryTestHeader(),
		{Name: intent.Identity.StageConfigFilename, Typeflag: tar.TypeReg, Size: int64(len(stageBody))},
		{Name: intent.Identity.ActiveConfigFilename, Typeflag: tar.TypeReg, Size: 2}}
	archiveRunner.result.Stdout = gatewayRebindStageConfigCopyHeadersTar(t, headers,
		append(append([]byte(nil), stageBody...), []byte("{}")...))
	if _, err := actualStage.stageConfigInventory(context.Background(), *started, stageBody, true); err == nil {
		t.Fatal("typed post-start inventory admitted an active config")
	}
}

func TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists(t *testing.T) {
	fixture, input, _ := newGatewayRebindCoordinatorFixture(t)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("partial typed physical adapter became production reachable")
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 1 || history.Progress[0].Record.Sequence != 1 {
		t.Fatalf("closed adapter changed physical prefix: progress=%d error=%v", len(history.Progress), err)
	}
}

func TestGatewayRebindTypedDriverPersistsCompletePhysicalPrefixWithInjectedAdapter(t *testing.T) {
	fixture, input, bounded := newGatewayRebindCoordinatorFixture(t)
	fake := &gatewayRebindTypedStageDriverFake{t: t, template: bounded.template}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager, stage: fake, handover: fake}
	result, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository, input, driver)
	if err == nil || result != (GatewayRebindCommitResult{}) {
		history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		lastSequence, lastPhase := uint64(0), gatewayRebindProgressPhase("")
		if len(history.Progress) != 0 {
			lastSequence, lastPhase = history.Progress[len(history.Progress)-1].Record.Sequence,
				history.Progress[len(history.Progress)-1].Record.Phase
		}
		t.Fatalf("typed physical boundary result=%#v error=%v progress=%d last=%d/%s historyErr=%v",
			result, err, len(history.Progress), lastSequence, lastPhase, historyErr)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 17 || len(history.TerminalsV2) != 1 ||
		history.Progress[16].Record.Phase != gatewayRebindProgressHandoverCommitted {
		t.Fatalf("typed commit history progress=%d terminals=%d error=%v", len(history.Progress), len(history.TerminalsV2), err)
	}
}
