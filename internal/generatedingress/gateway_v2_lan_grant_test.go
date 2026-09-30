package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2LANGrantCommitsExactApprovedBindingAndReplays(t *testing.T) {
	manager, store, state, journal, request, driver := gatewayV2LANGrantFixture(t)
	authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)

	result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || result.Receipt.Request != request || result.Receipt.GatewayOperationID != state.OperationID ||
		!validSHA256(result.Receipt.ProtectedStateDigest) {
		t.Fatalf("grant result=%#v err=%v events=%v", result, err, driver.events)
	}
	if _, exposesURL := reflect.TypeOf(result).FieldByName("URL"); exposesURL {
		t.Fatal("gateway grant result exposed a URL")
	}
	if len(*leases) != 1 || (*leases)[0].revalidateCalls != 2 || (*leases)[0].activateCalls != 1 ||
		(*leases)[0].releaseCalls != 1 || !(*leases)[0].active {
		t.Fatalf("grant lease=%#v", (*leases)[0])
	}
	installed, installedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || installed.Pending != nil || installed.Apps[request.AppID].LAN == nil ||
		installedJournal.Phase != gatewayPhaseCommitted {
		t.Fatalf("installed state=%#v journal=%#v err=%v", installed, installedJournal, err)
	}
	wantBinding, _ := gatewayV2LANBindingForRequest(request)
	if !reflect.DeepEqual(*installed.Apps[request.AppID].LAN, wantBinding) {
		t.Fatalf("installed binding=%#v want=%#v", installed.Apps[request.AppID].LAN, wantBinding)
	}
	installedDigest, digestErr := canonicalDigest(installed)
	if digestErr != nil || installedDigest != result.Receipt.ProtectedStateDigest {
		t.Fatalf("receipt digest=%q installed digest=%q err=%v", result.Receipt.ProtectedStateDigest, installedDigest, digestErr)
	}
	wantEvents := []string{
		"prove_committed", "selected_preflight", "selected_preflight", "preflight_candidate", "apply:lan-grant.json",
		"prove_granted", "selected_preflight",
	}
	if !reflect.DeepEqual(driver.live, installed) || !reflect.DeepEqual(driver.events, wantEvents) {
		t.Fatalf("driver live=%#v events=%v", driver.live, driver.events)
	}

	driver.events = nil
	driver.selectedPreflightCalls = 0
	replay, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || replay != result || len(*leases) != 2 || (*leases)[1].revalidateCalls != 1 ||
		(*leases)[1].activateCalls != 1 || (*leases)[1].releaseCalls != 1 ||
		!reflect.DeepEqual(driver.events, []string{"selected_preflight", "prove_granted", "selected_preflight"}) {
		t.Fatalf("replay=%#v err=%v leases=%#v events=%v", replay, err, *leases, driver.events)
	}
	unchanged, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(unchanged, installed) || reflect.DeepEqual(state, installed) {
		t.Fatalf("replay state=%#v err=%v", unchanged, err)
	}
}

func TestGatewayV2LANGrantReattestsCandidateImmediatelyBeforeReload(t *testing.T) {
	manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
	driver.failCandidatePreflight = true
	authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)

	result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
	if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || len(*leases) != 1 ||
		(*leases)[0].activateCalls != 0 || (*leases)[0].releaseCalls != 1 {
		t.Fatalf("result=%#v err=%v leases=%#v", result, err, *leases)
	}
	assertGatewayV2LANGrantUnchanged(t, store, journal, before, driver,
		[]string{"prove_committed", "selected_preflight", "selected_preflight", "preflight_candidate"})
}

func TestGatewayV2LANGrantAuthorizationLeaseFencesProtectedAndDockerMutation(t *testing.T) {
	t.Run("acquisition denial", func(t *testing.T) {
		manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
		calls := 0
		result, err := manager.grantGatewayV2LAN(context.Background(), request,
			func(context.Context, gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
				calls++
				return nil, errors.New("denied")
			})
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || calls != 1 {
			t.Fatalf("result=%#v err=%v calls=%d", result, err, calls)
		}
		assertGatewayV2LANGrantUnchanged(t, store, journal, before, driver,
			[]string{"prove_committed", "selected_preflight"})
	})

	for _, denyAt := range []int{1, 2} {
		t.Run("lease revalidation "+string(rune('0'+denyAt)), func(t *testing.T) {
			manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
			authorize, leases := allowGatewayV2LANGrant(t, manager, request, func(lease *fakeGatewayV2LANGrantLease) {
				lease.denyRevalidateAt = denyAt
			})
			result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
			if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || len(*leases) != 1 ||
				(*leases)[0].releaseCalls != 1 || (*leases)[0].activateCalls != 0 {
				t.Fatalf("result=%#v err=%v leases=%#v", result, err, *leases)
			}
			assertGatewayV2LANGrantUnchanged(t, store, journal, before, driver,
				[]string{"prove_committed", "selected_preflight"})
		})
	}
}

func TestGatewayV2LANGrantLeaseExcludesConcurrentDisableUntilActivation(t *testing.T) {
	manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
	var databaseFence sync.Mutex
	leaseAcquired := make(chan struct{})
	disableCommitted := make(chan struct{})
	authorize := func(_ context.Context, got gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
		if got != request {
			t.Fatalf("authorization request=%#v want=%#v", got, request)
		}
		databaseFence.Lock()
		lease := &fakeGatewayV2LANGrantLease{
			t: t, manager: manager, request: request, fence: &databaseFence, fenceHeld: true,
			activateHook: func() {
				select {
				case <-disableCommitted:
					t.Fatal("disable committed before exact allocation activation")
				default:
				}
			},
		}
		close(leaseAcquired)
		return lease, nil
	}
	go func() {
		<-leaseAcquired
		databaseFence.Lock()
		close(disableCommitted)
		databaseFence.Unlock()
	}()

	if _, err := manager.grantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	<-disableCommitted
}

func TestGatewayV2LANGrantRejectsSelectedInterfaceDriftAtEveryBoundary(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
			driver.failSelectedPreflightAt = failAt
			authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)
			result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
			if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if failAt == 1 && len(*leases) != 0 {
				t.Fatalf("authorization ran after initial interface drift: %#v", *leases)
			}
			if failAt > 1 && (len(*leases) != 1 || (*leases)[0].activateCalls != 0 || (*leases)[0].releaseCalls != 1) {
				t.Fatalf("lease=%#v", *leases)
			}
			after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) {
				t.Fatalf("state=%#v live=%#v loadErr=%v", after, driver.live, loadErr)
			}
			if failAt == 3 && (!containsString(driver.events, "apply:lan-grant-rollback.json") ||
				!containsString(driver.events, "prove_rolled_back")) {
				t.Fatalf("post-proof drift did not roll back and prove 404: %v", driver.events)
			}
		})
	}

	t.Run("replay", func(t *testing.T) {
		manager, _, _, _, request, driver := gatewayV2LANGrantFixture(t)
		authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
		if _, err := manager.grantGatewayV2LAN(context.Background(), request, authorize); err != nil {
			t.Fatal(err)
		}
		driver.events = nil
		driver.selectedPreflightCalls = 0
		driver.failSelectedPreflightAt = 1
		called := false
		result, err := manager.grantGatewayV2LAN(context.Background(), request,
			func(context.Context, gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
				called = true
				return nil, nil
			})
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || called ||
			!reflect.DeepEqual(driver.events, []string{"selected_preflight"}) {
			t.Fatalf("replay result=%#v err=%v called=%t events=%v", result, err, called, driver.events)
		}
	})
}

func TestGatewayV2LANGrantRollsBackAndProvesIsolationOnFaults(t *testing.T) {
	tests := []struct {
		name              string
		failApply         int
		mutateBeforeError bool
		failGrantProof    bool
		failRollbackProof bool
		wantPending       bool
	}{
		{name: "reload failed before mutation", failApply: 1},
		{name: "reload failed after mutation", failApply: 1, mutateBeforeError: true},
		{name: "grant proof failed", failGrantProof: true},
		{name: "rollback proof uncertain retains pending", failGrantProof: true, failRollbackProof: true, wantPending: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
			driver.failApply = test.failApply
			driver.mutateBeforeError = test.mutateBeforeError
			driver.failGrantProof = test.failGrantProof
			driver.failRollbackProof = test.failRollbackProof
			authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)
			result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
			if err == nil || result != (gatewayV2LANGrantResult{}) || len(*leases) != 1 ||
				(*leases)[0].activateCalls != 0 || (*leases)[0].releaseCalls != 1 {
				t.Fatalf("result=%#v err=%v leases=%#v", result, err, *leases)
			}
			after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if test.wantPending {
				if after.Pending == nil || after.Pending.Kind != gatewayV2PendingLANGrant {
					t.Fatalf("uncertain rollback did not retain pending grant: %#v", after.Pending)
				}
			} else if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) {
				t.Fatalf("rollback state=%#v live=%#v want=%#v", after, driver.live, before)
			}
			if !containsString(driver.events, "apply:lan-grant-rollback.json") ||
				!containsString(driver.events, "prove_rolled_back") {
				t.Fatalf("rollback did not apply and prove isolation: %v", driver.events)
			}
		})
	}
}

func TestGatewayV2LANGrantDurabilityOrReleaseUncertaintyReturnsNoReceipt(t *testing.T) {
	t.Run("final protected write", func(t *testing.T) {
		manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
		originalWrite := upgradeProtectedWrite
		writes := 0
		upgradeProtectedWrite = func(path, purpose string, body []byte) error {
			writes++
			if writes == 3 {
				return errors.New("injected final durability failure")
			}
			return originalWrite(path, purpose, body)
		}
		t.Cleanup(func() { upgradeProtectedWrite = originalWrite })
		authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)

		result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || len(*leases) != 1 ||
			!(*leases)[0].active || (*leases)[0].releaseCalls != 1 {
			t.Fatalf("result=%#v err=%v lease=%#v", result, err, *leases)
		}
		retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANGrant ||
			!retained.Pending.ActivationUncertain ||
			driver.live.Apps[request.AppID].LAN == nil {
			t.Fatalf("retained=%#v live=%#v loadErr=%v", retained, driver.live, loadErr)
		}
	})

	t.Run("activation committed then returned error", func(t *testing.T) {
		manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
		authorize, leases := allowGatewayV2LANGrant(t, manager, request, func(lease *fakeGatewayV2LANGrantLease) {
			lease.activateErr = errors.New("ambiguous activation result")
			lease.activateCommittedBeforeError = true
		})
		result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || len(*leases) != 1 ||
			!(*leases)[0].active || (*leases)[0].activateCalls != 1 || (*leases)[0].releaseCalls != 1 {
			t.Fatalf("result=%#v err=%v leases=%#v", result, err, *leases)
		}
		retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANGrant ||
			!retained.Pending.ActivationUncertain || !reflect.DeepEqual(driver.live, before) {
			t.Fatalf("retained=%#v live=%#v loadErr=%v", retained, driver.live, loadErr)
		}
		manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState,
			_ gatewayMigrationJournal,
		) gatewayObservedTopology {
			if candidate.Pending == nil && candidate.Apps[request.AppID].LAN == nil {
				return gatewayTopologyExactFinalV2
			}
			return gatewayTopologyUnknownOrDrift
		}
		if recoverErr := manager.Recover(context.Background()); !IsCode(recoverErr, DiagnosticRouteUnresolved) {
			t.Fatalf("generic recovery err=%v, want unresolved activation marker", recoverErr)
		}
		afterRecover, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || !reflect.DeepEqual(afterRecover, retained) {
			t.Fatalf("generic recovery cleared uncertain marker: state=%#v err=%v", afterRecover, loadErr)
		}
	})

	for _, test := range []struct {
		name        string
		lockRelease bool
	}{
		{name: "authorization lease release"},
		{name: "gateway lock release", lockRelease: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
			if test.lockRelease {
				originalAcquire := managerAcquireGatewayOSLock
				managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
					return func() error { return errors.New("injected unlock failure") }, nil
				}
				t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
			}
			authorize, _ := allowGatewayV2LANGrant(t, manager, request, func(lease *fakeGatewayV2LANGrantLease) {
				if !test.lockRelease {
					lease.releaseErr = errors.New("injected lease release failure")
				}
			})
			result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
			if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestGatewayV2LANGrantRejectsBindingAndProfileMismatchesBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*gatewayV2LANGrantRequest)
	}{
		{name: "app", edit: func(value *gatewayV2LANGrantRequest) { value.AppID = upgradeTestAppB }},
		{name: "allocation", edit: func(value *gatewayV2LANGrantRequest) {
			value.AllocationID = "99999999-9999-4999-8999-999999999999"
		}},
		{name: "profile", edit: func(value *gatewayV2LANGrantRequest) {
			value.GatewayProfileRevisionID = "99999999-9999-4999-8999-999999999999"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
			candidate := request
			test.edit(&candidate)
			called := false
			result, err := manager.grantGatewayV2LAN(context.Background(), candidate,
				func(context.Context, gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
					called = true
					return nil, nil
				})
			if err == nil || result != (gatewayV2LANGrantResult{}) || called {
				t.Fatalf("result=%#v err=%v authorization called=%t", result, err, called)
			}
			after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil || !reflect.DeepEqual(after, before) || len(driver.events) != 0 {
				t.Fatalf("mismatch changed state=%#v events=%v loadErr=%v", after, driver.events, loadErr)
			}
		})
	}
}

func TestGatewayV2LANGrantSerializesCompetingOwners(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := first
	second.AllocationID = "99999999-9999-4999-8999-999999999999"
	second.AccessRevisionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	second.AccessRevisionNumber++
	second.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, second)

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.grantGatewayV2LAN(context.Background(), first,
			func(_ context.Context, got gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
				close(entered)
				<-release
				return &fakeGatewayV2LANGrantLease{t: t, manager: manager, request: got}, nil
			})
		firstDone <- err
	}()
	<-entered

	var secondResult gatewayV2LANGrantResult
	var secondErr error
	secondAuthorized := false
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		secondResult, secondErr = manager.grantGatewayV2LAN(context.Background(), second,
			func(context.Context, gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
				secondAuthorized = true
				return nil, nil
			})
	}()
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	if secondErr == nil || secondResult != (gatewayV2LANGrantResult{}) || secondAuthorized {
		t.Fatalf("competing result=%#v err=%v authorized=%t", secondResult, secondErr, secondAuthorized)
	}
	installed, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || installed.Apps[first.AppID].LAN == nil ||
		installed.Apps[first.AppID].LAN.AllocationID != first.AllocationID || !reflect.DeepEqual(driver.live, installed) {
		t.Fatalf("installed state=%#v live=%#v err=%v", installed, driver.live, err)
	}
}

func TestGatewayV2PendingKindsKeepRouteSwitchAndLANGrantSeparate(t *testing.T) {
	_, _, state, _, request, _ := gatewayV2LANGrantFixture(t)
	previous := cloneGatewayV2AppRoute(state.Apps[request.AppID])
	binding, _ := gatewayV2LANBindingForRequest(request)
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = &binding
	grant := cloneGatewayV2RouteState(state)
	grant.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposed,
	}
	if !validGatewayV2RouteState(grant) {
		t.Fatal("exact nil-to-binding LAN grant pending state was rejected")
	}
	routeSwitch := cloneGatewayV2RouteState(grant)
	routeSwitch.Pending.Kind = gatewayV2PendingRouteSwitch
	if validGatewayV2RouteState(routeSwitch) {
		t.Fatal("route switch pending state granted LAN access")
	}
	changedRoute := cloneGatewayV2RouteState(grant)
	changedRoute.Pending.Proposed.Route.Slot = "green"
	if validGatewayV2RouteState(changedRoute) {
		t.Fatal("LAN grant pending state also changed the deployment route")
	}
	uncertainRouteSwitch := cloneGatewayV2RouteState(routeSwitch)
	uncertainRouteSwitch.Pending.ActivationUncertain = true
	if validGatewayV2RouteState(uncertainRouteSwitch) {
		t.Fatal("route switch accepted a LAN activation uncertainty marker")
	}
}

func TestGatewayV2LANGrantRestartRecoveryRollsBackProposedAndMixedTo404State(t *testing.T) {
	for _, topology := range []string{"proposed", "mixed"} {
		t.Run(topology, func(t *testing.T) {
			manager, store, state, journal, request, _ := gatewayV2LANGrantFixture(t)
			runner := &committedV2Runner{files: make(map[string][]byte)}
			manager.runner = runner
			binding, _ := gatewayV2LANBindingForRequest(request)
			previous := cloneGatewayV2AppRoute(state.Apps[request.AppID])
			proposed := cloneGatewayV2AppRoute(previous)
			proposed.LAN = &binding
			pending := cloneGatewayV2RouteState(state)
			pending.Pending = &gatewayV2PendingRoute{
				Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposed,
			}
			if err := store.saveCommittedV2State(pending, journal); err != nil {
				t.Fatal(err)
			}
			manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
				_, rollbackInstalled := runner.files[gatewayV2ContainerName+":"+"/config/rollback.json"]
				if rollbackInstalled && candidate.Apps[request.AppID].LAN == nil {
					return gatewayTopologyExactFinalV2
				}
				if topology == "proposed" && candidate.Apps[request.AppID].LAN != nil {
					return gatewayTopologyExactFinalV2
				}
				return gatewayTopologyUnknownOrDrift
			}
			manager.gatewayMixedRestartObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayV2RouteState, gatewayMigrationJournal) bool {
				return topology == "mixed"
			}
			if err := manager.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil || !reflect.DeepEqual(recovered, state) {
				t.Fatalf("recovered state=%#v err=%v", recovered, err)
			}
			if _, ok := runner.files[gatewayV2ContainerName+":"+"/config/rollback.json"]; !ok {
				t.Fatal("restart recovery did not reinstall and attest the committed 404 config")
			}
		})
	}
}

func TestGatewayV2StartupRoutesPendingLANGrantOnlyToSafeRecovery(t *testing.T) {
	tests := []struct {
		name      string
		live      string
		wantError bool
	}{
		{name: "committed 404 topology", live: "committed"},
		{name: "proposed grant topology", live: "proposed"},
		{name: "reload-only mixed topology", live: "mixed"},
		{name: "unknown topology", live: "unknown", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, store, upgradeRequest, _, _, _, _ := gatewayV2CoordinatorTestFixture(t)
			_, state, journal := installCommittedGeneration(t, manager, store, upgradeRequest.OperationID)
			request := gatewayV2LANGrantRequestForState(t, state)
			binding, _ := gatewayV2LANBindingForRequest(request)
			previous := cloneGatewayV2AppRoute(state.Apps[request.AppID])
			proposed := cloneGatewayV2AppRoute(previous)
			proposed.LAN = &binding
			pending := cloneGatewayV2RouteState(state)
			pending.Pending = &gatewayV2PendingRoute{
				Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposed,
			}
			if err := store.saveCommittedV2State(pending, journal); err != nil {
				t.Fatal(err)
			}
			manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
				hasBinding := candidate.Apps[request.AppID].LAN != nil
				if (test.live == "committed" && !hasBinding) || (test.live == "proposed" && hasBinding) {
					return gatewayTopologyExactFinalV2
				}
				return gatewayTopologyUnknownOrDrift
			}
			manager.gatewayMixedRestartObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayV2RouteState, gatewayMigrationJournal) bool {
				return test.live == "mixed"
			}
			inspection, err := manager.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{{
				Request: upgradeRequest, State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 2,
			}})
			if test.wantError {
				if !IsCode(err, DiagnosticRouteUnresolved) || inspection != (GatewayV2StartupInspection{}) {
					t.Fatalf("inspection=%#v err=%v", inspection, err)
				}
				return
			}
			want := GatewayV2StartupInspection{Disposition: GatewayV2StartupRecoveryOnly, OperationID: upgradeRequest.OperationID}
			if err != nil || inspection != want {
				t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
			}
		})
	}
}

func TestGatewayV2LANPublicationProofBindsRevisionAppHostPortAndRollback404(t *testing.T) {
	_, _, state, _, request, _ := gatewayV2LANGrantFixture(t)
	binding, _ := gatewayV2LANBindingForRequest(request)
	app := state.Apps[request.AppID]
	app.LAN = &binding
	state.Apps[request.AppID] = app
	base, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	assignment := caddyV2LANAssignment{
		AppID: request.AppID, AllocationID: request.AllocationID, AccessRevisionID: request.AccessRevisionID,
		AccessRevisionNumber: request.AccessRevisionNumber, AccessSpecDigest: request.AccessSpecDigest,
	}
	appChallenge := gatewayV2LANAppChallenge(base, request.Port, assignment)
	portChallenge := gatewayV2PortChallenge(base, request.Port)
	caddyID := strings.Repeat("e", 64)
	wrongHostIsolated := true
	probe := func(_ context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
		if address == "127.0.0.1" || address != state.Profile.SelectedIPv4 || port != request.Port {
			return gatewayV2HostProbeResult{}
		}
		if strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
			challenge := strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
			if host == state.Profile.SelectedIPv4 && (challenge == appChallenge || challenge == portChallenge) {
				return gatewayV2HostProbeResult{
					Status: 404, Body: gatewayV2ChallengeBodyPrefix + challenge, Connected: true, Responded: true,
				}
			}
		}
		if host == "wrong.invalid" {
			status := 404
			if !wrongHostIsolated {
				status = 200
			}
			return gatewayV2HostProbeResult{Status: status, Connected: true, Responded: true}
		}
		return gatewayV2HostProbeResult{Status: 204, Connected: true, Responded: true}
	}
	containerProbe := func(_ context.Context, gotID, address string, port uint16, host, challenge string) bool {
		return gotID == caddyID && address == state.Network.ContainerIPv4 && port == request.Port &&
			host == state.Profile.SelectedIPv4 && (challenge == appChallenge || challenge == portChallenge)
	}
	if !proveGatewayV2LANGrantPublication(context.Background(), state, request, caddyID, probe, containerProbe) {
		t.Fatal("exact assigned app publication was not proven")
	}
	staleRequest := request
	staleRequest.AccessRevisionID = "88888888-8888-4888-8888-888888888888"
	staleRequest.AccessRevisionNumber++
	staleBinding, _ := gatewayV2LANBindingForRequest(staleRequest)
	staleState := cloneGatewayV2RouteState(state)
	staleApp := staleState.Apps[request.AppID]
	staleApp.LAN = &staleBinding
	staleState.Apps[request.AppID] = staleApp
	if proveGatewayV2LANGrantPublication(context.Background(), staleState, staleRequest, caddyID, probe, containerProbe) {
		t.Fatal("a stale access revision challenge proved a later revision")
	}
	wrongHostIsolated = false
	if proveGatewayV2LANGrantPublication(context.Background(), state, request, caddyID, probe, containerProbe) {
		t.Fatal("wrong-Host exposure was accepted")
	}
	wrongHostIsolated = true
	rolledBack := cloneGatewayV2RouteState(state)
	app = rolledBack.Apps[request.AppID]
	app.LAN = nil
	rolledBack.Apps[request.AppID] = app
	rollbackProbe := func(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
		result := probe(ctx, address, port, host, path)
		if path == "/" && address != "127.0.0.1" {
			result.Status = 404
		}
		return result
	}
	if !proveGatewayV2LANRollbackPublication(context.Background(), rolledBack, request.Port, caddyID, rollbackProbe, containerProbe) {
		t.Fatal("rolled-back assigned port 404 was not proven")
	}
}

type fakeGatewayV2LANGrantDriver struct {
	t                       *testing.T
	manager                 *Manager
	live                    gatewayV2RouteState
	events                  []string
	applyCalls              int
	selectedPreflightCalls  int
	failApply               int
	failSelectedPreflightAt int
	failCandidatePreflight  bool
	mutateBeforeError       bool
	failGrantProof          bool
	failRollbackProof       bool
}

func (d *fakeGatewayV2LANGrantDriver) checkLocked() {
	d.t.Helper()
	if d.manager.mu.TryLock() {
		d.manager.mu.Unlock()
		d.t.Fatal("LAN grant driver ran without the Manager lock")
	}
}

func (d *fakeGatewayV2LANGrantDriver) proveCommitted(_ context.Context, state gatewayV2RouteState, _ gatewayMigrationJournal) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_committed")
	return reflect.DeepEqual(d.live, state)
}

func (d *fakeGatewayV2LANGrantDriver) selectedInterfacePreflight(gatewayProfileBinding) error {
	d.checkLocked()
	d.events = append(d.events, "selected_preflight")
	d.selectedPreflightCalls++
	if d.failSelectedPreflightAt == d.selectedPreflightCalls {
		return errors.New("injected selected interface drift")
	}
	return nil
}

func (d *fakeGatewayV2LANGrantDriver) preflightCandidate(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal, appID string, proposed gatewayV2AppRoute,
) error {
	d.checkLocked()
	d.events = append(d.events, "preflight_candidate")
	if d.failCandidatePreflight || !reflect.DeepEqual(d.live, state) || appID == "" ||
		!reflect.DeepEqual(proposed.Route, state.Apps[appID].Route) || proposed.LAN == nil {
		return errors.New("injected candidate endpoint or network drift")
	}
	return nil
}

func (d *fakeGatewayV2LANGrantDriver) apply(_ context.Context, state gatewayV2RouteState, filename string) error {
	d.checkLocked()
	d.events = append(d.events, "apply:"+filename)
	d.applyCalls++
	if d.failApply == d.applyCalls {
		if d.mutateBeforeError {
			d.live = cloneGatewayV2RouteState(state)
		}
		return errors.New("injected reload failure")
	}
	d.live = cloneGatewayV2RouteState(state)
	return nil
}

func (d *fakeGatewayV2LANGrantDriver) proveGranted(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_granted")
	return !d.failGrantProof && reflect.DeepEqual(d.live, state) && state.Apps[request.AppID].LAN != nil
}

func (d *fakeGatewayV2LANGrantDriver) proveRolledBack(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_rolled_back")
	return !d.failRollbackProof && reflect.DeepEqual(d.live, state) && state.Apps[request.AppID].LAN == nil
}

type fakeGatewayV2LANGrantLease struct {
	t                            *testing.T
	manager                      *Manager
	request                      gatewayV2LANGrantRequest
	revalidateCalls              int
	activateCalls                int
	releaseCalls                 int
	denyRevalidateAt             int
	activateErr                  error
	activateCommittedBeforeError bool
	releaseErr                   error
	active                       bool
	fence                        *sync.Mutex
	fenceHeld                    bool
	activateHook                 func()
}

func (l *fakeGatewayV2LANGrantLease) check(request gatewayV2LANGrantRequest) {
	l.t.Helper()
	if request != l.request {
		l.t.Fatalf("lease request=%#v want=%#v", request, l.request)
	}
	if l.manager.mu.TryLock() {
		l.manager.mu.Unlock()
		l.t.Fatal("authorization lease ran without the Manager lock")
	}
}

func (l *fakeGatewayV2LANGrantLease) Revalidate(_ context.Context, request gatewayV2LANGrantRequest) error {
	l.check(request)
	l.revalidateCalls++
	if l.denyRevalidateAt == l.revalidateCalls {
		return errors.New("injected authorization denial")
	}
	return nil
}

func (l *fakeGatewayV2LANGrantLease) Activate(_ context.Context, request gatewayV2LANGrantRequest) error {
	l.check(request)
	l.activateCalls++
	if l.activateHook != nil {
		l.activateHook()
	}
	if l.activateErr != nil {
		if l.activateCommittedBeforeError {
			l.active = true
		}
		return l.activateErr
	}
	l.active = true
	return nil
}

func (l *fakeGatewayV2LANGrantLease) Release() error {
	l.releaseCalls++
	if l.fenceHeld {
		l.fenceHeld = false
		l.fence.Unlock()
	}
	return l.releaseErr
}

func allowGatewayV2LANGrant(t *testing.T, manager *Manager, request gatewayV2LANGrantRequest,
	configure func(*fakeGatewayV2LANGrantLease),
) (gatewayV2LANGrantAuthorizer, *[]*fakeGatewayV2LANGrantLease) {
	t.Helper()
	leases := make([]*fakeGatewayV2LANGrantLease, 0, 1)
	authorize := func(_ context.Context, got gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
		if got != request {
			t.Fatalf("authorization request=%#v want=%#v", got, request)
		}
		if manager.mu.TryLock() {
			manager.mu.Unlock()
			t.Fatal("authorization callback ran without the Manager lock")
		}
		lease := &fakeGatewayV2LANGrantLease{t: t, manager: manager, request: request}
		if configure != nil {
			configure(lease)
		}
		leases = append(leases, lease)
		return lease, nil
	}
	return authorize, &leases
}

func assertGatewayV2LANGrantUnchanged(t *testing.T, store *gatewayUpgradeStateStore,
	journal gatewayMigrationJournal, before gatewayV2RouteState, driver *fakeGatewayV2LANGrantDriver, wantEvents []string,
) {
	t.Helper()
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) ||
		!reflect.DeepEqual(driver.events, wantEvents) {
		t.Fatalf("state=%#v live=%#v events=%v err=%v", after, driver.live, driver.events, err)
	}
}

func gatewayV2LANGrantFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore, gatewayV2RouteState,
	gatewayMigrationJournal, gatewayV2LANGrantRequest, *fakeGatewayV2LANGrantDriver,
) {
	t.Helper()
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayV2LANGrantRequestForState(t, state)
	driver := &fakeGatewayV2LANGrantDriver{t: t, manager: manager, live: cloneGatewayV2RouteState(state)}
	manager.gatewayV2LANGrantDriver = driver
	return manager, store, state, journal, request, driver
}

func gatewayV2LANGrantRequestForState(t *testing.T, state gatewayV2RouteState) gatewayV2LANGrantRequest {
	t.Helper()
	request := gatewayV2LANGrantRequest{
		AppID: upgradeTestAppA, AllocationID: "66666666-6666-4666-8666-666666666666", Port: 8100,
		AccessRevisionID: "77777777-7777-4777-8777-777777777777", AccessRevisionNumber: 3,
		GatewayProfileRevisionID: state.Profile.RevisionID, GatewayProfileRevisionNumber: state.Profile.RevisionNumber,
		GatewayProfileSpecDigest: state.Profile.SpecDigest,
	}
	request.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, request)
	return request
}

func mustGatewayV2LANAccessDigest(t *testing.T, request gatewayV2LANGrantRequest) string {
	t.Helper()
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: request.AppID, AllocationID: request.AllocationID, Port: request.Port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
