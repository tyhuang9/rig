package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

// SQL is projected through the reader seam; protected files and locks are real.
// Docker observation is simulated by the bounded existing native driver.
func installNativeStartupSQL(t *testing.T, manager *Manager, operationID string) (
	appaccess.GatewayRebindRecoverySnapshot, GatewayV2LANStartupBindingProjection,
) {
	t.Helper()
	selection, err := manager.selectGatewayUpgradeGenerationLocked(operationID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := gatewayUpgradeEffectiveProof(selection)
	if err != nil {
		t.Fatal(err)
	}
	profile := gatewayCurrentFixtureProfile(proof.ProtectedLineage, selection.State.Profile)
	source := gatewayCurrentAuthority(proof.ProtectedLineage)
	snapshot := appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: &profile, CurrentSource: &source}
	manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		return snapshot, nil
	})
	return snapshot, GatewayV2LANStartupBindingProjection{EffectiveProfile: proof.EffectiveProfile, GatewaySource: source}
}

func TestGatewayV2LANStartupBindsNativeProjectionBeforeAndAfterPhysicalProof(t *testing.T) {
	for _, combined := range []bool{false, true} {
		name := "grant"
		if combined {
			name = "access"
		}
		t.Run(name, func(t *testing.T) {
			manager, store, journal, request, driver := grantedLANForDisable(t)
			driver.applyCalls = 0
			snapshot, projection := installNativeStartupSQL(t, manager, journal.OperationID)
			state, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			for _, failure := range []string{"", "missing projection", "wrong projected operation", "wrong projected interface", "missing provider", "SQL failure", "active rebind", "SQL drift after proof", "projection without SQL source"} {
				t.Run(failure, func(t *testing.T) {
					calls := 0
					driver.events = nil
					claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
					value := projection
					claim.CurrentBinding = &value
					switch failure {
					case "missing projection":
						claim.CurrentBinding = nil
					case "wrong projected operation":
						value.GatewaySource.OperationID = "41414141-4141-4141-8141-414141414141"
					case "wrong projected interface":
						value.EffectiveProfile.InterfaceID = "different-interface"
					}
					manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
						calls++
						if failure == "SQL failure" {
							return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("injected SQL failure")
						}
						if failure == "projection without SQL source" {
							return appaccess.GatewayRebindRecoverySnapshot{}, nil
						}
						result := snapshot
						if failure == "active rebind" {
							result.Active = &appaccess.GatewayRebindHistoryEntry{}
							result.Phase = appaccess.GatewayRebindPrepared
						}
						if failure == "SQL drift after proof" && calls >= 4 {
							source := *snapshot.CurrentSource
							source.OperationID = "41414141-4141-4141-8141-414141414141"
							result.CurrentSource = &source
						}
						return result, nil
					})
					if failure == "missing provider" {
						manager.options.RebindCurrentStateRepository = nil
					}
					var disposition GatewayV2LANStartupDisposition
					if combined {
						inspection, inspectErr := manager.InspectGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{claim}, nil)
						disposition, err = inspection.Disposition, inspectErr
					} else {
						inspection, inspectErr := manager.InspectGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim})
						disposition, err = inspection.Disposition, inspectErr
					}
					if failure == "" {
						if err != nil || disposition != GatewayV2LANStartupNormal || calls != 6 {
							t.Fatalf("exact authority refused: disposition=%s calls=%d err=%v", disposition, calls, err)
						}
					} else if err == nil || disposition != "" {
						t.Fatalf("unproved authority accepted: disposition=%s err=%v", disposition, err)
					}
					if failure != "" && failure != "SQL drift after proof" && len(driver.events) != 0 {
						t.Fatalf("invalid authority reached physical observer: %v", driver.events)
					}
					if failure == "SQL drift after proof" && !containsString(driver.events, "prove_committed") {
						t.Fatal("drift fixture did not reach physical proof")
					}
					retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
					if loadErr != nil || !reflect.DeepEqual(retained, state) || driver.applyCalls != 0 {
						t.Fatalf("inspection mutated state: loadErr=%v applyCalls=%d", loadErr, driver.applyCalls)
					}
				})
			}
		})
	}
}

func TestGatewayV2LANStartupRetainedNativeAuthorityIsHistoryOnly(t *testing.T) {
	manager, _, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
	disable := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, request),
		State: appaccess.AppAccessDisableCommitted, StateSequence: 3, ClearAcknowledged: true, RetainedBinding: &projection}
	grant := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
	grant.DisableIntentOperationID, grant.RetainedBinding = disable.Request.OperationID, &projection
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{disable})
	if err != nil || inspection.Disposition != GatewayV2LANStartupNormal || driver.applyCalls != 0 {
		t.Fatalf("exact retired history refused: inspection=%+v err=%v", inspection, err)
	}
	projection.GatewaySource.OperationID = "41414141-4141-4141-8141-414141414141"
	driver.events = nil
	inspection, err = manager.InspectGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{disable})
	if err == nil || inspection.Disposition != "" || len(driver.events) != 0 {
		t.Fatalf("unbound retired history accepted: inspection=%+v err=%v events=%v", inspection, err, driver.events)
	}
}

func TestGatewayV2LANStartupSelectedNativePendingRemainsRecoveryOnly(t *testing.T) {
	manager, _, _, _, journal, request, driver := pendingGatewayV2LANGrantFixture(t, true)
	_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
	driver.pendingTopology = gatewayV2PendingProposedExact
	claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantUncertain, 3)
	claim.CurrentBinding = &projection
	inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim})
	if err != nil || inspection.Disposition != GatewayV2LANStartupRecoveryOnly || inspection.AttemptID != request.AttemptID || driver.applyCalls != 0 {
		t.Fatalf("selected native pending inspection=%+v err=%v", inspection, err)
	}
}

func TestGatewayV2StartupSQLSelectedNativeLANRecoveryBatch(t *testing.T) {
	manager, store, journal, batch, claim, driver, observer := gatewayV2LANRecoveryStartupFixture(t)
	installNativeStartupSQL(t, manager, journal.OperationID)
	inspection, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{claim})
	retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || inspection.Disposition != GatewayV2StartupRecoveryOnly || inspection.OperationID != journal.OperationID ||
		loadErr != nil || !reflect.DeepEqual(retained, batch) || driver.applyCalls != 0 || observer.observeCalls != 1 {
		t.Fatalf("selected native batch inspection=%+v err=%v loadErr=%v applyCalls=%d observations=%d", inspection, err, loadErr, driver.applyCalls, observer.observeCalls)
	}
}

func TestGatewayV2StartupSQLSelectedNativePending(t *testing.T) {
	manager, store, request, _, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	_, state, journal := installCommittedGeneration(t, manager, store, request.OperationID)
	grant := gatewayV2LANGrantRequestForState(t, state)
	binding, err := gatewayV2LANBindingForRequest(grant)
	if err != nil {
		t.Fatal(err)
	}
	previous := cloneGatewayV2AppRoute(state.Apps[grant.AppID])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = &binding
	pending := cloneGatewayV2RouteState(state)
	pending.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingLANGrant, AppID: grant.AppID, Previous: &previous, Proposed: proposed}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}
	installNativeStartupSQL(t, manager, journal.OperationID)
	observations := 0
	manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
		observations++
		if candidate.Apps[grant.AppID].LAN == nil {
			return gatewayTopologyExactFinalV2
		}
		return gatewayTopologyUnknownOrDrift
	}
	inspection, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{{
		Request: request, State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2,
	}})
	retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || inspection.Disposition != GatewayV2StartupRecoveryOnly || inspection.OperationID != journal.OperationID ||
		loadErr != nil || !reflect.DeepEqual(retained, pending) || observations == 0 {
		t.Fatalf("selected native pending inspection=%+v err=%v loadErr=%v observations=%d", inspection, err, loadErr, observations)
	}
}

func TestGatewayV2LANStartupQuarantineRechecksSQLAtEffectBoundary(t *testing.T) {
	for _, kind := range []string{"grant", "disable"} {
		t.Run(kind, func(t *testing.T) {
			for _, fault := range []string{"", "wrong projection", "SQL drift before pending", "SQL drift after pending", "SQL drift after apply"} {
				t.Run(fault, func(t *testing.T) {
					manager, store, journal, request, driver := grantedLANForDisable(t)
					snapshot, projection := installNativeStartupSQL(t, manager, journal.OperationID)
					before, _, err := store.loadBoundUpgrade(journal.OperationID)
					if err != nil {
						t.Fatal(err)
					}
					grant := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantApplying, 2)
					grant.CurrentBinding = &projection
					disable := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, request),
						State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: &projection}
					if kind == "disable" {
						grant.State, grant.StateSequence = appaccess.AppAccessGrantCommitted, 4
						grant.DisableIntentOperationID = disable.Request.OperationID
					}
					if fault == "wrong projection" {
						projection.GatewaySource.OperationID = "41414141-4141-4141-8141-414141414141"
					}
					driver.events, driver.applyCalls = nil, 0
					calls := 0
					manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
						calls++
						state, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
						if loadErr != nil {
							return appaccess.GatewayRebindRecoverySnapshot{}, loadErr
						}
						result := snapshot
						if (fault == "SQL drift before pending" && calls >= 4) ||
							(fault == "SQL drift after pending" && state.Pending != nil) ||
							(fault == "SQL drift after apply" && driver.applyCalls > 0) {
							source := *snapshot.CurrentSource
							source.OperationID = "41414141-4141-4141-8141-414141414141"
							result.CurrentSource = &source
						}
						return result, nil
					})
					if kind == "grant" {
						err = manager.QuarantineGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{grant})
					} else {
						err = manager.QuarantineGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{disable})
					}
					if (fault == "") != (err == nil) {
						t.Fatalf("fault=%q unexpected result: %v", fault, err)
					}
					retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
					if loadErr != nil || !reflect.DeepEqual(retainedJournal, journal) || !reflect.DeepEqual(retained.Apps, before.Apps) {
						t.Fatalf("quarantine rewrote raw publication or journal: %v", loadErr)
					}
					beforeWrite := fault == "wrong projection" || fault == "SQL drift before pending"
					if beforeWrite {
						if !reflect.DeepEqual(retained, before) || driver.applyCalls != 0 {
							t.Fatal("unproved authority reached protected write or physical apply")
						}
					} else if retained.Pending == nil {
						t.Fatal("quarantine lost its retained pending recovery marker")
					}
					wantApplies := 0
					if fault == "" || fault == "SQL drift after apply" {
						wantApplies = 1
					}
					if driver.applyCalls != wantApplies {
						t.Fatalf("physical apply count=%d want=%d", driver.applyCalls, wantApplies)
					}
					if wantApplies == 1 && driver.live.Apps[request.AppID].LAN != nil {
						t.Fatal("quarantine re-published the binding")
					}
				})
			}
		})
	}
}

func TestGatewayV2LANStartupRetainedAuthorityCannotServeLiveGrant(t *testing.T) {
	manager, _, journal, request, driver := grantedLANForDisable(t)
	_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
	grant := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
	disable := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, request),
		State: appaccess.AppAccessDisableCommitted, StateSequence: 3, ClearAcknowledged: true, RetainedBinding: &projection}
	grant.DisableIntentOperationID, grant.RetainedBinding = disable.Request.OperationID, &projection
	driver.applyCalls = 0
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{disable})
	if err == nil || inspection.Disposition != "" || driver.applyCalls != 0 {
		t.Fatalf("retained authority permitted serving: inspection=%+v err=%v", inspection, err)
	}
}

func TestGatewayV2LANStartupQuarantineRejectsChangedMarkerAfterProof(t *testing.T) {
	for _, kind := range []string{"grant", "disable"} {
		t.Run(kind, func(t *testing.T) {
			manager, store, journal, request, driver := grantedLANForDisable(t)
			_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
			before, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			grant := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantApplying, 2)
			grant.CurrentBinding = &projection
			disable := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, request),
				State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: &projection}
			if kind == "disable" {
				grant.State, grant.StateSequence = appaccess.AppAccessGrantCommitted, 4
				grant.DisableIntentOperationID = disable.Request.OperationID
			}
			driver.events, driver.applyCalls = nil, 0
			originalRead := upgradeProtectedRead
			injected := false
			upgradeProtectedRead = func(path, purpose string) ([]byte, error) {
				body, readErr := originalRead(path, purpose)
				if readErr == nil && !injected && path == store.v2Path && driver.applyCalls == 1 && containsString(driver.events, "prove_rolled_back") {
					injected = true
					// Return the core's exact pending readback while an external
					// writer replaces it immediately afterward with valid old state.
					gatewayV2LANRecoveryStartupOverwrite(t, store.v2Path, store.v2Purpose, before)
				}
				return body, readErr
			}
			t.Cleanup(func() { upgradeProtectedRead = originalRead })
			if kind == "grant" {
				err = manager.QuarantineGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{grant})
			} else {
				err = manager.QuarantineGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{disable})
			}
			if !injected || driver.applyCalls != 1 || err == nil {
				t.Fatalf("changed post-proof marker accepted: injected=%v applyCalls=%d err=%v", injected, driver.applyCalls, err)
			}
		})
	}
}

func TestGatewayV2LANDisableStartupQuarantineRejectsLockReleaseFailure(t *testing.T) {
	manager, _, journal, request, driver := grantedLANForDisable(t)
	_, projection := installNativeStartupSQL(t, manager, journal.OperationID)
	grant := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
	grant.CurrentBinding = &projection
	disable := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, request),
		State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: &projection}
	grant.DisableIntentOperationID = disable.Request.OperationID
	driver.events, driver.applyCalls = nil, 0
	originalAcquire := managerAcquireGatewayOSLock
	releases := 0
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		release, err := originalAcquire(ctx, store)
		if err != nil {
			return nil, err
		}
		return func() error {
			err := release()
			releases++
			if err == nil && releases == 2 {
				return errors.New("injected mutation lock release failure")
			}
			return err
		}, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
	err := manager.QuarantineGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{disable})
	if releases != 2 || driver.applyCalls != 1 || !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("mutation release failure accepted: releases=%d applies=%d err=%v", releases, driver.applyCalls, err)
	}
}
