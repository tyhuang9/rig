package generatedingress

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

// These startup tests use the bounded fake LAN observer. They establish the
// read-only classification contract and lock discipline; they do not stand in
// for the live Docker/Caddy topology evidence required by the hosted journey.
func TestGatewayV2StartupClassifiesProtectedLANRecoveryBatch(t *testing.T) {
	tests := []struct {
		name       string
		topology   gatewayV2LANRecoveryBatchTopology
		claimState appaccess.GatewayProfileUpgradeState
	}{
		{name: "before projection exact", topology: gatewayV2LANRecoveryBatchBeforeExact, claimState: appaccess.GatewayProfileUpgradeCommitted},
		{name: "effective projection exact", topology: gatewayV2LANRecoveryBatchEffectiveExact, claimState: appaccess.GatewayProfileUpgradeCommitted},
		{name: "reload-only mixed attested", topology: gatewayV2LANRecoveryBatchReloadMixed, claimState: appaccess.GatewayProfileUpgradeCommitted},
		{name: "stale serving claim remains proved recovery", topology: gatewayV2LANRecoveryBatchBeforeExact, claimState: appaccess.GatewayProfileUpgradeServing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, journal, batch, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
			claim.State = test.claimState
			effective, _, err := gatewayV2LANRecoveryEffectiveProjection(batch)
			if err != nil {
				t.Fatal(err)
			}
			switch test.topology {
			case gatewayV2LANRecoveryBatchBeforeExact:
			case gatewayV2LANRecoveryBatchEffectiveExact:
				driver.live = cloneGatewayV2RouteState(effective)
			case gatewayV2LANRecoveryBatchReloadMixed:
				driver.live = cloneGatewayV2RouteState(effective)
				driver.pendingTopology = gatewayV2PendingReloadOnlyMixed
			default:
				t.Fatalf("unhandled topology %q", test.topology)
			}
			liveBefore := cloneGatewayV2RouteState(driver.live)

			inspection, inspectErr := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{claim})
			want := GatewayV2StartupInspection{
				Disposition: GatewayV2StartupRecoveryOnly,
				OperationID: journal.OperationID,
			}
			retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if inspectErr != nil || inspection != want || loadErr != nil ||
				!reflect.DeepEqual(retained, batch) || !reflect.DeepEqual(retainedJournal, journal) ||
				!reflect.DeepEqual(driver.live, liveBefore) || driver.applyCalls != 0 || observer.observeCalls != 1 {
				t.Fatalf("inspection=%#v inspectErr=%v retained=%#v journal=%#v loadErr=%v live=%#v applyCalls=%d observeCalls=%d",
					inspection, inspectErr, retained, retainedJournal, loadErr, driver.live, driver.applyCalls, observer.observeCalls)
			}
		})
	}
}

func TestGatewayV2StartupRejectsUnsafeLANRecoveryBatchEvidence(t *testing.T) {
	t.Run("invalid protected batch", func(t *testing.T) {
		manager, store, journal, batch, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
		invalid := cloneGatewayV2RouteState(batch)
		invalid.LANRecovery.Items = append(invalid.LANRecovery.Items, invalid.LANRecovery.Items[0])
		gatewayV2LANRecoveryStartupOverwrite(t, store.v2Path, store.v2Purpose, invalid)

		assertGatewayV2LANRecoveryStartupRejected(t, manager, claim)
		if driver.applyCalls != 0 || observer.observeCalls != 0 {
			t.Fatalf("invalid batch reached observer: applyCalls=%d observeCalls=%d journal=%s",
				driver.applyCalls, observer.observeCalls, journal.OperationID)
		}
	})

	t.Run("wrong owned journal", func(t *testing.T) {
		manager, store, _, _, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
		wrong := observer.expectedJournal
		wrong.Resources.FinalContainerID = strings.Repeat("f", 64)
		gatewayV2LANRecoveryStartupOverwrite(t, store.journalPath, store.journalPurpose, wrong)

		assertGatewayV2LANRecoveryStartupRejected(t, manager, claim)
		if driver.applyCalls != 0 || observer.observeCalls != 1 {
			t.Fatalf("wrong journal applyCalls=%d observeCalls=%d", driver.applyCalls, observer.observeCalls)
		}
	})

	for _, topology := range []string{"unknown", "partial"} {
		t.Run(topology+" topology", func(t *testing.T) {
			manager, _, _, batch, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
			if topology == "unknown" {
				driver.live = gatewayV2RouteState{}
			} else {
				partial := cloneGatewayV2RouteState(driver.live)
				item := batch.LANRecovery.Items[0]
				app := partial.Apps[item.AppID]
				app.LAN = nil
				partial.Apps[item.AppID] = app
				driver.live = partial
			}

			assertGatewayV2LANRecoveryStartupRejected(t, manager, claim)
			if driver.applyCalls != 0 || observer.observeCalls != 1 {
				t.Fatalf("%s topology applyCalls=%d observeCalls=%d", topology, driver.applyCalls, observer.observeCalls)
			}
		})
	}

	t.Run("selected interface drift after observation", func(t *testing.T) {
		manager, _, _, _, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
		driver.failSelectedPreflightAt = 2

		assertGatewayV2LANRecoveryStartupRejected(t, manager, claim)
		if driver.applyCalls != 0 || observer.observeCalls != 1 || driver.selectedPreflightCalls != 2 {
			t.Fatalf("applyCalls=%d observeCalls=%d selectedPreflightCalls=%d",
				driver.applyCalls, observer.observeCalls, driver.selectedPreflightCalls)
		}
	})

	t.Run("protected state drift after observation", func(t *testing.T) {
		manager, store, _, batch, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
		drifted := cloneGatewayV2RouteState(batch)
		drifted.LANRecovery.Head = 1
		var overwriteErr error
		observer.afterObserve = func() {
			body, err := json.Marshal(drifted)
			if err == nil {
				err = upgradeProtectedWrite(store.v2Path, store.v2Purpose, body)
			}
			overwriteErr = err
		}

		assertGatewayV2LANRecoveryStartupRejected(t, manager, claim)
		if overwriteErr != nil || driver.applyCalls != 0 || observer.observeCalls != 1 {
			t.Fatalf("overwriteErr=%v applyCalls=%d observeCalls=%d", overwriteErr, driver.applyCalls, observer.observeCalls)
		}
	})
}

type gatewayV2LANRecoveryStartupObserver struct {
	*fakeGatewayV2LANGrantDriver
	expectedJournal gatewayMigrationJournal
	osLockHeld      *bool
	afterObserve    func()
	observeCalls    int
}

func (d *gatewayV2LANRecoveryStartupObserver) observeLANRecoveryBatch(ctx context.Context,
	state, effective gatewayV2RouteState, journal gatewayMigrationJournal,
) gatewayV2LANRecoveryBatchTopology {
	d.checkLocked()
	d.observeCalls++
	if d.osLockHeld == nil || !*d.osLockHeld || !reflect.DeepEqual(journal, d.expectedJournal) {
		return gatewayV2LANRecoveryBatchUnknown
	}
	topology := d.fakeGatewayV2LANGrantDriver.observeLANRecoveryBatch(ctx, state, effective, journal)
	if d.afterObserve != nil {
		d.afterObserve()
	}
	return topology
}

var _ gatewayV2LANRecoveryBatchDriver = (*gatewayV2LANRecoveryStartupObserver)(nil)

func gatewayV2LANRecoveryStartupFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore,
	gatewayMigrationJournal, gatewayV2RouteState, GatewayV2StartupClaim, *fakeGatewayV2LANGrantDriver,
	*gatewayV2LANRecoveryStartupObserver,
) {
	t.Helper()
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
	driver.events = nil
	driver.applyCalls = 0
	driver.selectedPreflightCalls = 0
	driver.pendingTopology = ""
	_, preparation := upgradeTestPreparation(t)
	if preparation.OperationID != journal.OperationID {
		t.Fatalf("preparation operation=%q journal operation=%q", preparation.OperationID, journal.OperationID)
	}
	claim := GatewayV2StartupClaim{
		Request: GatewayV2UpgradeRequest{
			OperationID: preparation.OperationID,
			Profile: GatewayV2ProfileBinding{
				RevisionID: preparation.Profile.RevisionID, RevisionNumber: preparation.Profile.RevisionNumber,
				SpecDigest: preparation.Profile.SpecDigest, SelectedIPv4: preparation.Profile.SelectedIPv4,
				InterfaceID: preparation.Profile.InterfaceID, PortStart: preparation.Profile.PortStart,
				PortEnd: preparation.Profile.PortEnd,
			},
			ApprovedBy: preparation.ApprovedBy, ApprovedActionDigest: preparation.ApprovedActionDigest,
		},
		State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2,
	}

	osLockHeld := false
	originalAcquire := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		osLockHeld = true
		return func() error { osLockHeld = false; return nil }, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
	observer := &gatewayV2LANRecoveryStartupObserver{
		fakeGatewayV2LANGrantDriver: driver,
		expectedJournal:             journal,
		osLockHeld:                  &osLockHeld,
	}
	manager.gatewayV2LANGrantDriver = observer
	return manager, store, journal, batch, claim, driver, observer
}

func gatewayV2LANRecoveryStartupOverwrite(t *testing.T, path, purpose string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeProtectedWrite(path, purpose, body); err != nil {
		t.Fatal(err)
	}
}

func assertGatewayV2LANRecoveryStartupRejected(t *testing.T, manager *Manager, claim GatewayV2StartupClaim) {
	t.Helper()
	inspection, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{claim})
	if !IsCode(err, DiagnosticRouteUnresolved) || inspection != (GatewayV2StartupInspection{}) {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
}
