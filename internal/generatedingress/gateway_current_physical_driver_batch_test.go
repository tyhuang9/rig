package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type gatewayCurrentLANRecoveryRuntimeFake struct {
	*gatewayCurrentPhysicalRuntimeFake
	observeFn func(context.Context, gatewayCurrentLANRecoveryPhysicalAction,
		gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
	reconcileFn func(context.Context, gatewayCurrentLANRecoveryPhysicalAction,
		gatewayCurrentPhysicalTarget, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
	observes   int
	reconciles int
}

func (f *gatewayCurrentLANRecoveryRuntimeFake) observeLANRecovery(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	f.observes++
	if f.observeFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("batch observation unavailable")
	}
	return f.observeFn(ctx, action, target)
}

func (f *gatewayCurrentLANRecoveryRuntimeFake) reconcileLANRecovery(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
	guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	f.reconciles++
	if f.reconcileFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("batch reconciliation unavailable")
	}
	return f.reconcileFn(ctx, action, target, guard)
}

func TestManagedGatewayCurrentLANRecoveryDriverRefusesSelectionDriftBeforeEffects(t *testing.T) {
	fixture, _, action, target := managedGatewayCurrentLANRecoveryDriverFixture(t)
	drift := false
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		if drift {
			return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("current authority changed")
		}
		return snapshot, nil
	})
	runtime := &gatewayCurrentLANRecoveryRuntimeFake{gatewayCurrentPhysicalRuntimeFake: &gatewayCurrentPhysicalRuntimeFake{}}
	runtime.reconcileFn = func(ctx context.Context, actual gatewayCurrentLANRecoveryPhysicalAction,
		actualTarget gatewayCurrentPhysicalTarget, guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if !reflect.DeepEqual(actual, action) || !reflect.DeepEqual(actualTarget, target) {
			t.Fatal("managed driver changed the batch physical authority")
		}
		drift = true
		return gatewayCurrentPhysicalAttestation{}, guard(ctx)
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	if _, err := driver.withdrawGatewayCurrentLANRecoveryBatch(context.Background(), action); err == nil ||
		runtime.reconciles != 1 || runtime.observes != 0 {
		t.Fatalf("selection drift reached batch effects: reconciles=%d observes=%d error=%v",
			runtime.reconciles, runtime.observes, err)
	}
	retained, err := fixture.store.load()
	if err != nil || !reflect.DeepEqual(retained, action.Selected) {
		t.Fatalf("selection drift changed protected state: state=%#v error=%v", retained, err)
	}
	completeState := cloneGatewayCurrentRouteState(action.Selected)
	for _, item := range completeState.LANRecovery.Items {
		app := completeState.Apps[item.AppID]
		app.LAN = nil
		completeState.Apps[item.AppID] = app
	}
	completeState.LANRecovery.Head = len(completeState.LANRecovery.Items)
	completeState.Digest, _ = gatewayCurrentRouteStateDigest(completeState)
	completeSelection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
		Lineage: action.Lineage, Terminal: &action.Terminal, State: &completeState}
	complete, err := fixture.manager.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(completeSelection)
	if err != nil || !complete.Complete {
		t.Fatalf("complete batch action=%#v error=%v", complete, err)
	}
	if _, err := driver.withdrawGatewayCurrentLANRecoveryBatch(context.Background(), complete); err == nil ||
		runtime.reconciles != 1 {
		t.Fatalf("complete batch authorized withdrawal: reconciles=%d error=%v", runtime.reconciles, err)
	}

	drift = false
	observer := &gatewayCurrentLANRecoveryRuntimeFake{gatewayCurrentPhysicalRuntimeFake: &gatewayCurrentPhysicalRuntimeFake{}}
	observer.observeFn = func(_ context.Context, _ gatewayCurrentLANRecoveryPhysicalAction,
		actualTarget gatewayCurrentPhysicalTarget,
	) (gatewayCurrentPhysicalAttestation, error) {
		proof := gatewayCurrentPhysicalDriverTestAttestation(t, actualTarget, gatewayCurrentPhysicalRecoveryMixed)
		if observer.observes == 2 {
			drift = true
		}
		return proof, nil
	}
	attestDriver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             observer,
	}
	if _, err := attestDriver.attestGatewayCurrentLANRecoveryBatch(context.Background(), action); err == nil ||
		observer.observes != 2 {
		t.Fatalf("post-proof selection drift was accepted: observes=%d error=%v", observer.observes, err)
	}
}

func TestManagedGatewayCurrentLANRecoveryRuntimeClassifiesOnlyCanonicalPartialTopology(t *testing.T) {
	fixture, _, action, target := managedGatewayCurrentLANRecoveryDriverFixture(t)
	if len(action.Selected.LANRecovery.Items) < 2 {
		t.Fatal("batch fixture has fewer than two immutable items")
	}
	partial := cloneGatewayCurrentRouteState(action.Before)
	first := action.Selected.LANRecovery.Items[0]
	app := partial.Apps[first.AppID]
	app.LAN = nil
	partial.Apps[first.AppID] = app
	partial.Digest, _ = gatewayCurrentRouteStateDigest(partial)
	body := mustGatewayCurrentPhysicalConfig(t, partial)
	observed, err := gatewayCurrentLANRecoveryStateForConfig(action, body)
	if err != nil || !reflect.DeepEqual(observed, partial) {
		t.Fatalf("canonical partial topology=%#v error=%v", observed, err)
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Apps.HTTP.Servers[lanServerName(partial.Profile.PortStart)]
	server.Routes = append(server.Routes, notFoundRoute())
	config.Apps.HTTP.Servers[lanServerName(partial.Profile.PortStart)] = server
	tampered, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayCurrentLANRecoveryStateForConfig(action, tampered); err == nil {
		t.Fatal("noncanonical partial topology was accepted")
	}

	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, partial, partial, localPort)
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtime := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	result, err := driver.attestGatewayCurrentLANRecoveryBatch(context.Background(), action)
	if err != nil || result.BatchAbsent || result.Attestation.Outcome != gatewayCurrentPhysicalRecoveryMixed ||
		!reflect.DeepEqual(result.Attestation.State, partial) {
		t.Fatalf("actual partial batch topology=%#v error=%v", result, err)
	}
}

func TestManagedGatewayCurrentLANRecoveryRuntimeWithdrawsWholeBatchAfterLostReloadAcknowledgement(t *testing.T) {
	fixture, _, action, target := managedGatewayCurrentLANRecoveryDriverFixture(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, action.Before, action.Before, localPort)
	runner.lostReloadAck = true
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtime := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}

	before, err := driver.attestGatewayCurrentLANRecoveryBatch(context.Background(), action)
	if err != nil || before.BatchAbsent || !reflect.DeepEqual(before.Attestation.State, action.Before) {
		t.Fatalf("initial whole-batch attestation=%#v error=%v", before, err)
	}
	if withdrawn, err := driver.withdrawGatewayCurrentLANRecoveryBatch(context.Background(), action); err == nil ||
		!reflect.DeepEqual(withdrawn, gatewayCurrentLANRecoveryPhysicalResult{}) ||
		!sameCaddyConfig(runner.live, mustGatewayCurrentPhysicalConfig(t, action.Withdrawn)) ||
		!sameCaddyConfig(runner.files[target.Identity.Rebind.ActiveConfigFilename],
			mustGatewayCurrentPhysicalConfig(t, action.Before)) {
		t.Fatalf("live-only lost acknowledgement was resolved: result=%#v effects=%v error=%v",
			withdrawn, runner.effects, err)
	}
	retained, err := fixture.store.load()
	if err != nil || !reflect.DeepEqual(retained, action.Selected) {
		t.Fatalf("physical batch driver changed protected state after lost ack: state=%#v error=%v", retained, err)
	}
	withdrawn, err := driver.withdrawGatewayCurrentLANRecoveryBatch(context.Background(), action)
	if err != nil || !withdrawn.BatchAbsent ||
		!reflect.DeepEqual(withdrawn.Attestation.State, action.Withdrawn) ||
		!reflect.DeepEqual(withdrawn.Attestation.LANRecovery, action.Selected.LANRecovery) ||
		!sameCaddyConfig(runner.live, mustGatewayCurrentPhysicalConfig(t, action.Withdrawn)) ||
		!sameCaddyConfig(runner.files[target.Identity.Rebind.ActiveConfigFilename],
			mustGatewayCurrentPhysicalConfig(t, action.Withdrawn)) {
		t.Fatalf("retried whole-batch withdrawal=%#v effects=%v error=%v", withdrawn, runner.effects, err)
	}
	retained, err = fixture.store.load()
	if err != nil || !reflect.DeepEqual(retained, action.Selected) {
		t.Fatalf("physical batch driver changed protected state: state=%#v error=%v", retained, err)
	}
	for _, effect := range runner.effects {
		if len(effect) > 1 && effect[0] == "network" {
			t.Fatalf("LAN-only batch withdrawal changed application networks: %v", runner.effects)
		}
	}
}

func TestManagedGatewayCurrentLANRecoveryRuntimeAttestsStoppedWholeBatchWithoutServingDependencies(t *testing.T) {
	fixture, _, action, target := managedGatewayCurrentLANRecoveryDriverFixture(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, action.Before, action.Before, localPort)
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Before))
	runner.endpoints = nil
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtime := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe: func(context.Context, string, uint16, string, string) gatewayV2HostProbeResult {
			return gatewayV2HostProbeResult{}
		},
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return false }}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	before, err := driver.attestGatewayCurrentLANRecoveryBatch(context.Background(), action)
	if err == nil || !reflect.DeepEqual(before, gatewayCurrentLANRecoveryPhysicalResult{}) {
		t.Fatalf("stopped restart config with retained LAN was attested: proof=%#v error=%v", before, err)
	}
	result, err := driver.withdrawGatewayCurrentLANRecoveryBatch(context.Background(), action)
	if err != nil || !result.BatchAbsent || result.Attestation.Outcome != gatewayCurrentPhysicalRecoveryStopped ||
		!result.Attestation.Runtime.ListenerAbsent || !reflect.DeepEqual(result.Attestation.State, action.Withdrawn) ||
		runner.container.Running {
		t.Fatalf("stopped whole-batch proof=%#v error=%v", result, err)
	}
	for _, effect := range runner.effects {
		if len(effect) >= 2 && effect[0] == "container" && effect[1] == "start" {
			t.Fatalf("withdrawal-only stopped batch was published: effects=%v", runner.effects)
		}
	}
}

func managedGatewayCurrentLANRecoveryDriverFixture(t *testing.T) (gatewayCurrentStateFixture,
	gatewayCurrentSelection, gatewayCurrentLANRecoveryPhysicalAction, gatewayCurrentPhysicalTarget,
) {
	t.Helper()
	fixture, selection := gatewayCurrentLANRecoveryPhysicalSelectionFixture(t)
	selected := cloneGatewayCurrentRouteState(*selection.State)
	for appID, app := range selected.Apps {
		if app.LAN == nil || app.LAN.Transfer != nil {
			continue
		}
		for index := range app.Route.Endpoints {
			digest := sha256.Sum256([]byte(appID + "\x00batch\x00" + app.Route.Endpoints[index].Component))
			app.Route.Endpoints[index].ContainerID = hex.EncodeToString(digest[:])
			app.Route.Endpoints[index].NetworkName = "rig-current-batch-native"
			app.Route.Endpoints[index].NetworkAlias += "-batch"
		}
		selected.Apps[appID] = app
	}
	selected.Digest, _ = gatewayCurrentRouteStateDigest(selected)
	intermediate := cloneGatewayCurrentRouteState(selected)
	intermediate.Revision--
	intermediate.LANRecovery = nil
	intermediate.Digest, _ = gatewayCurrentRouteStateDigest(intermediate)
	if !validGatewayCurrentRouteState(intermediate) || fixture.store.installBaseline(fixture.baseline) != nil ||
		fixture.store.saveNext(fixture.baseline, intermediate) != nil ||
		fixture.store.saveNext(intermediate, selected) != nil {
		t.Fatal("install current LAN recovery state")
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		return snapshot, nil
	})
	controllerRoot := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(controllerRoot)
	if err != nil {
		t.Fatal(err)
	}
	workingIdentity, err := os.Lstat(directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.options.WorkingDirectory = directories.WorkingDirectory
	fixture.manager.workingDirectoryIdentity = workingIdentity
	selection.State = &selected
	action, err := fixture.manager.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	if err != nil {
		t.Fatal(err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, action.Before, nil, selected.LANRecovery)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, selection, action, target
}
