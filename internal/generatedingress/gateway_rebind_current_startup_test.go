package generatedingress

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type gatewayRebindStartupPhysical struct {
	gatewayCurrentPhysicalDriverFake
	before func()
	calls  int
}

func (f *gatewayRebindStartupPhysical) attestGatewayCurrentPhysical(context.Context, gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error) {
	f.calls++
	if f.before != nil {
		f.before()
	}
	return f.attest, nil
}

func gatewayRebindCurrentStartupFixture(t *testing.T) (gatewayCurrentStateFixture, appaccess.GatewayRebindRecoverySnapshot, []GatewayV2StartupClaim, *gatewayRebindStartupPhysical) {
	t.Helper()
	f := newGatewayCurrentStateFixture(t)
	if err := f.store.installBaseline(f.baseline); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(&gatewayCurrentNoCommandRunner{}, Options{DataRoot: f.dataRoot,
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, HostPort: f.history.Predecessor.Journal.Source.LocalHostPort,
		RebindFenceCheck: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	f.manager = manager
	profile := gatewayCurrentFixtureProfile(f.baseline.Lineage, f.baseline.Profile)
	source := gatewayCurrentAuthority(f.baseline.Lineage)
	event := appaccess.GatewayRebindEvent{OperationID: source.OperationID, Sequence: 3, State: appaccess.GatewayRebindDatabaseCommitted}
	snapshot := appaccess.GatewayRebindRecoverySnapshot{
		CurrentProfile: &profile, CurrentSource: &source, CurrentDatabaseCommittedEvent: &event, CurrentTransfers: f.transfers,
		History: []appaccess.GatewayRebindHistoryEntry{{Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: 1,
			Legacy: &appaccess.GatewayRebindClaim{Spec: appaccess.GatewayRebindSpec{OperationID: source.OperationID}, State: appaccess.GatewayRebindCommitted}},
			Events: []appaccess.GatewayRebindEvent{event}, Transfers: f.transfers}},
	}
	f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		return snapshot, nil
	})
	terminal, err := gatewayCurrentSelectionTerminalView(gatewayCurrentSelection{Receipt: &f.receipt})
	if err != nil {
		t.Fatal(err)
	}
	physical := &gatewayRebindStartupPhysical{gatewayCurrentPhysicalDriverFake: gatewayCurrentPhysicalDriverFake{
		attest: gatewayCurrentPhysicalAttestationFixture(t, f.baseline, terminal, gatewayCurrentPhysicalStableServing),
	}}
	f.manager.gatewayCurrentPhysicalDriver = physical
	claims := []GatewayV2StartupClaim{{Request: gatewayV2UpgradeRequestFromStateForTest(f.predecessor),
		State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2}}
	return f, snapshot, claims, physical
}

func TestGatewayRebindCurrentStartupBindsSQLAndPhysicalAuthority(t *testing.T) {
	f, snapshot, claims, physical := gatewayRebindCurrentStartupFixture(t)
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.manager.InspectGatewayV2Startup(context.Background(), claims)
	if err != nil || got.Disposition != GatewayV2StartupNormalV2 || got.OperationID != snapshot.CurrentSource.OperationID ||
		got.CurrentGatewaySource != *snapshot.CurrentSource || physical.calls != 1 {
		t.Fatalf("selected rebound startup=%+v calls=%d error=%v", got, physical.calls, err)
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	state, stateErr := f.store.load()
	if err != nil || stateErr != nil || !sameGatewayHistorySnapshot(before, after) || !reflect.DeepEqual(state, f.baseline) {
		t.Fatal("read-only startup changed protected history or current state")
	}

	for _, name := range []string{"missing historical claim", "forged historical approval", "SQL unavailable", "active rebind", "physical proof", "SQL drift after physical proof"} {
		t.Run(name, func(t *testing.T) {
			currentClaims := append([]GatewayV2StartupClaim(nil), claims...)
			currentSQL := snapshot
			physical.calls, physical.before = 0, nil
			physical.attest = gatewayCurrentPhysicalAttestationFixture(t, f.baseline, physical.attest.Terminal, gatewayCurrentPhysicalStableServing)
			var sqlErr error
			switch name {
			case "missing historical claim":
				currentClaims = nil
			case "forged historical approval":
				currentClaims[0].Request.ApprovedBy = "99999999-9999-4999-8999-999999999999"
			case "SQL unavailable":
				sqlErr = errors.New("unavailable")
			case "active rebind":
				currentSQL.Active = &appaccess.GatewayRebindHistoryEntry{}
			case "physical proof":
				physical.attest.Digest = "invalid"
			case "SQL drift after physical proof":
				physical.before = func() { currentSQL.RollbackAllowed = true }
			}
			f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				return currentSQL, sqlErr
			})
			got, err := f.manager.InspectGatewayV2Startup(context.Background(), currentClaims)
			if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
				t.Fatalf("unsafe startup accepted: %+v %v", got, err)
			}
			if physical.applyCalls != 0 || physical.restoreCalls != 0 || physical.stopCalls != 0 {
				t.Fatal("inspection performed a physical mutation")
			}
		})
	}
	t.Run("valid protected replacement after physical proof", func(t *testing.T) {
		f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
			return snapshot, nil
		})
		next := cloneGatewayCurrentRouteState(f.baseline)
		next.Revision++
		next.Digest, _ = gatewayCurrentRouteStateDigest(next)
		physical.attest = gatewayCurrentPhysicalAttestationFixture(t, f.baseline, physical.attest.Terminal, gatewayCurrentPhysicalStableServing)
		physical.before = func() {
			if err := f.store.saveNext(f.baseline, next); err != nil {
				t.Fatal(err)
			}
		}
		got, err := f.manager.InspectGatewayV2Startup(context.Background(), claims)
		if !IsCode(err, DiagnosticRouteUnresolved) || got != (GatewayV2StartupInspection{}) {
			t.Fatalf("changed protected authority accepted: %+v %v", got, err)
		}
		retained, err := f.store.load()
		if err != nil || !reflect.DeepEqual(retained, next) {
			t.Fatal("inspection rewrote changed protected state")
		}
	})
}

func TestGatewayRebindCurrentStartupDistinguishesRouteAndLANRecovery(t *testing.T) {
	for _, name := range []string{"route", "disable", "batch"} {
		t.Run(name, func(t *testing.T) {
			f, snapshot, claims, physical := gatewayRebindCurrentStartupFixture(t)
			state := cloneGatewayCurrentRouteState(f.baseline)
			var appID string
			var previous gatewayCurrentAppRoute
			for candidate, app := range state.Apps {
				if app.LAN != nil {
					appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
					break
				}
			}
			if appID == "" {
				t.Fatal("fixture has no transferred app")
			}
			proposed := cloneGatewayCurrentAppRoute(previous)
			kind := gatewayV2PendingRouteSwitch
			want := GatewayV2StartupNormalV2
			outcome := gatewayCurrentPhysicalRecoveryBefore
			if name == "route" {
				if proposed.Route.Slot == "blue" {
					proposed.Route.Slot = "green"
				} else {
					proposed.Route.Slot = "blue"
				}
			} else {
				proposed.LAN = nil
				kind, want = gatewayV2PendingLANDisable, GatewayV2StartupRecoveryOnly
			}
			state.Revision++
			state.Pending = &gatewayCurrentPendingRoute{Kind: kind, AppID: appID, Previous: &previous, Proposed: proposed}
			if name != "route" {
				disable := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(appID, previous.LAN.Raw))
				state.Pending.Disable, state.Pending.ActivationUncertain = &disable, true
				if name == "batch" {
					state.LANRecovery = &gatewayCurrentLANRecoveryBatch{Items: []gatewayCurrentLANRecoveryItem{{Kind: gatewayV2PendingLANDisable, AppID: appID, Disable: &disable}}, LegacyPending: state.Pending}
					state.Pending = nil
					outcome = gatewayCurrentPhysicalRecoveryMixed
				}
			}
			state.Digest, _ = gatewayCurrentRouteStateDigest(state)
			if err := f.store.saveNext(f.baseline, state); err != nil {
				t.Fatal(err)
			}
			repository := &gatewayRebindProposalRepositoryFake{snapshot: snapshot}
			current, err := f.manager.InspectGatewayRebindCurrent(context.Background(), repository)
			mode := GatewayCurrentRecoveryRoute
			if name == "disable" {
				mode = GatewayCurrentRecoveryLAN
			} else if name == "batch" {
				mode = GatewayCurrentRecoveryLANBatch
			}
			if err != nil || current.CurrentRecoveryMode != mode || current.CurrentStateDigest != state.Digest {
				t.Fatalf("advisory current recovery=%+v error=%v", current, err)
			}
			projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(state)
			if err != nil {
				t.Fatal(err)
			}
			physical.attest = gatewayCurrentPhysicalAttestationFixture(t, projection.Before, physical.attest.Terminal, outcome)
			physical.attest.Pending, physical.attest.LANRecovery = state.Pending, state.LANRecovery
			physical.attest.Digest, _ = gatewayCurrentPhysicalAttestationDigest(physical.attest)
			got, err := f.manager.InspectGatewayV2Startup(context.Background(), claims)
			if err != nil || got.Disposition != want || got.OperationID != state.Lineage.OperationID {
				t.Fatalf("recovery startup=%+v want=%s error=%v", got, want, err)
			}
			retained, err := f.store.load()
			if err != nil || !reflect.DeepEqual(retained, state) {
				t.Fatal("inspection cleared recovery marker")
			}
		})
	}
}

func TestGatewayRebindCurrentStartupRouteRecoveryBeforeAdmission(t *testing.T) {
	for _, outcome := range []gatewayCurrentPhysicalOutcome{gatewayCurrentPhysicalRecoveryBefore, gatewayCurrentPhysicalRecoveryEffective} {
		t.Run(string(outcome), func(t *testing.T) {
			f, snapshot, claims, physical := gatewayRebindCurrentStartupFixture(t)
			driver := &gatewayCurrentStateMachineDriver{t: t, terminal: physical.attest.Terminal, pendingAttestOutcome: outcome}
			f.manager.gatewayCurrentPhysicalDriver = driver
			appID, app := routeOperationTransferredApp(t, f.baseline)
			transition, err := gatewayCurrentSwitchTransition(f.baseline, routeOperationSwitchRequest(t, appID, app.Route))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.saveNext(f.baseline, transition.Pending); err != nil {
				t.Fatal(err)
			}
			before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := f.manager.InspectGatewayV2Startup(context.Background(), claims)
			if err != nil || inspection.Disposition != GatewayV2StartupNormalV2 || inspection.CurrentGatewaySource != *snapshot.CurrentSource {
				t.Fatalf("pending route did not select composition recovery: %+v %v", inspection, err)
			}
			if retained := routeOperationLoad(t, f.store); !reflect.DeepEqual(retained, transition.Pending) || driver.restoreCalls != 0 {
				t.Fatal("read-only startup inspection changed pending route")
			}
			// Composition calls this public method before starting deployment workers.
			if err := f.manager.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered := routeOperationLoad(t, f.store)
			if recovered.Pending != nil || recovered.LANRecovery != nil || recovered.Revision != transition.Pending.Revision+1 ||
				!reflect.DeepEqual(recovered.Apps, f.baseline.Apps) || recovered.Lineage != f.baseline.Lineage ||
				driver.restoreCalls != 1 || driver.applyCalls != 0 || driver.stopCalls != 0 {
				t.Fatalf("startup recovery did not restore committed routes and raw LAN bindings: %+v", recovered)
			}
			afterInspection, err := f.manager.InspectGatewayV2Startup(context.Background(), claims)
			if err != nil || afterInspection != inspection {
				t.Fatalf("stable startup changed selected authority: %+v %v", afterInspection, err)
			}
			after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil || !sameGatewayHistorySnapshot(before, after) {
				t.Fatal("operational recovery changed immutable gateway history")
			}
		})
	}
}
