package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayV2LANGrantCommitsExactApprovedBindingAndReplays(t *testing.T) {
	manager, store, state, journal, request, driver := gatewayV2LANGrantFixture(t)
	authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)

	result, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || result.Receipt.Request != request || result.Receipt.GatewayOperationID != state.OperationID ||
		!validSHA256(result.Receipt.ProtectedStateDigest) || result.Receipt.ObservedAt.IsZero() {
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
		"prove_granted", "selected_preflight", "prove_granted", "selected_preflight",
	}
	if !reflect.DeepEqual(driver.live, installed) || !reflect.DeepEqual(driver.events, wantEvents) {
		t.Fatalf("driver live=%#v events=%v", driver.live, driver.events)
	}

	driver.events = nil
	driver.selectedPreflightCalls = 0
	replay, err := manager.grantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || replay.Receipt.Request != result.Receipt.Request ||
		replay.Receipt.GatewayOperationID != result.Receipt.GatewayOperationID ||
		replay.Receipt.ProtectedStateDigest != result.Receipt.ProtectedStateDigest || replay.Receipt.ObservedAt.IsZero() ||
		len(*leases) != 2 || (*leases)[1].revalidateCalls != 1 ||
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
	assertGatewayV2LANGrantPending(t, store, journal, before, request, driver,
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
			if denyAt == 1 {
				assertGatewayV2LANGrantUnchanged(t, store, journal, before, driver,
					[]string{"prove_committed", "selected_preflight"})
			} else {
				assertGatewayV2LANGrantPending(t, store, journal, before, request, driver,
					[]string{"prove_committed", "selected_preflight"})
			}
		})
	}
}

func TestGatewayV2LANGrantLeaseUsesShortTransactionsAcrossDocker(t *testing.T) {
	manager, _, _, _, request, driver := gatewayV2LANGrantFixture(t)
	var databaseWrite sync.Mutex
	dockerObservedUnlockedDB := false
	driver.applyHook = func() {
		if !databaseWrite.TryLock() {
			t.Fatal("grant retained a database write lock across Docker mutation")
		}
		dockerObservedUnlockedDB = true
		databaseWrite.Unlock()
	}
	authorize := func(_ context.Context, got gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
		if got != request {
			t.Fatalf("authorization request=%#v want=%#v", got, request)
		}
		databaseWrite.Lock()
		databaseWrite.Unlock()
		return &fakeGatewayV2LANGrantLease{
			t: t, manager: manager, request: request,
			activateHook: func() {
				databaseWrite.Lock()
				databaseWrite.Unlock()
			},
		}, nil
	}

	if _, err := manager.grantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	if !dockerObservedUnlockedDB {
		t.Fatal("Docker mutation did not test the released database lock")
	}
}

func TestGatewayV2LANGrantRejectsSelectedInterfaceDriftAtEveryBoundary(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4} {
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
			if failAt > 1 && failAt < 4 && (len(*leases) != 1 || (*leases)[0].activateCalls != 0 || (*leases)[0].releaseCalls != 1) {
				t.Fatalf("lease=%#v", *leases)
			}
			if failAt == 4 && (len(*leases) != 1 || (*leases)[0].activateCalls != 1 ||
				!(*leases)[0].active || (*leases)[0].releaseCalls != 1) {
				t.Fatalf("post-commit lease=%#v", *leases)
			}
			if failAt == 1 {
				assertGatewayV2LANGrantUnchanged(t, store, journal, before, driver, driver.events)
			} else if failAt < 4 {
				assertGatewayV2LANGrantPending(t, store, journal, before, request, driver, driver.events)
			} else {
				after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
				if loadErr != nil || after.Pending == nil || after.Pending.Kind != gatewayV2PendingLANWithdrawal ||
					driver.live.Apps[request.AppID].LAN != nil {
					t.Fatalf("post-commit state=%#v live=%#v err=%v", after, driver.live, loadErr)
				}
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

func TestGatewayV2LANGrantFinalProofFailureWithdrawsServingRoute(t *testing.T) {
	manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	driver.failGrantProofAt = 2
	authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)
	result, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize)
	if !IsCode(err, DiagnosticRouteUnresolved) || result != (GatewayV2LANGrantResult{}) ||
		len(*leases) != 1 || !(*leases)[0].active {
		t.Fatalf("result=%#v err=%v leases=%#v", result, err, *leases)
	}
	retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
		driver.live.Apps[request.AppID].LAN != nil || driver.gatewayStopped {
		t.Fatalf("retained=%#v live=%#v stopped=%t err=%v", retained, driver.live, driver.gatewayStopped, loadErr)
	}
}

func TestGatewayV2LANCommitResolutionFailedInitialProofWithdrawsOrStops(t *testing.T) {
	for _, stopFallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "withdraw", true: "stop fallback"}[stopFallback], func(t *testing.T) {
			manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
			authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
			if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
				t.Fatal(err)
			}
			driver.failGrantProof = true
			driver.failRollbackProof = stopFallback
			callback := false
			err := manager.WithGatewayV2LANCommitResolution(context.Background(), request,
				func(context.Context, GatewayV2LANGrantReceipt) error { callback = true; return nil })
			if !IsCode(err, DiagnosticRouteUnresolved) || callback {
				t.Fatalf("err=%v callback=%t", err, callback)
			}
			retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
				driver.live.Apps[request.AppID].LAN != nil || driver.gatewayStopped != stopFallback {
				t.Fatalf("retained=%#v live=%#v stopped=%t err=%v", retained, driver.live, driver.gatewayStopped, loadErr)
			}
		})
	}
}

func TestGatewayV2LANCommitResolutionStopsOwnedGatewayWhenProtectedRouteUnreadable(t *testing.T) {
	manager, store, _, _, request, driver := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.v2Path); err != nil {
		t.Fatal(err)
	}
	callback := false
	err := manager.WithGatewayV2LANCommitResolution(context.Background(), request,
		func(context.Context, GatewayV2LANGrantReceipt) error { callback = true; return nil })
	if !IsCode(err, DiagnosticRouteUnresolved) || callback || !driver.gatewayStopped {
		t.Fatalf("err=%v callback=%t stopped=%t", err, callback, driver.gatewayStopped)
	}
}

func TestGatewayV2LANGrantRollsBackAndProvesIsolationOnFaults(t *testing.T) {
	tests := []struct {
		name              string
		failApply         int
		mutateBeforeError bool
		failGrantProof    bool
		failRollbackProof bool
	}{
		{name: "reload failed before mutation", failApply: 1},
		{name: "reload failed after mutation", failApply: 1, mutateBeforeError: true},
		{name: "grant proof failed", failGrantProof: true},
		{name: "rollback proof uncertain retains pending", failGrantProof: true, failRollbackProof: true},
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
			assertGatewayV2LANGrantPending(t, store, journal, before, request, driver, driver.events)
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
		requestCtx, cancelRequest := context.WithCancel(context.Background())
		defer cancelRequest()
		authorize, leases := allowGatewayV2LANGrant(t, manager, request, func(lease *fakeGatewayV2LANGrantLease) {
			lease.activateHook = cancelRequest
		})

		result, err := manager.grantGatewayV2LAN(requestCtx, request, authorize)
		if !IsCode(err, DiagnosticCancelled) || result != (gatewayV2LANGrantResult{}) || len(*leases) != 1 ||
			!(*leases)[0].active || (*leases)[0].releaseCalls != 1 {
			t.Fatalf("result=%#v err=%v lease=%#v", result, err, *leases)
		}
		retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANGrant ||
			!retained.Pending.ActivationUncertain ||
			driver.live.Apps[request.AppID].LAN != nil || driver.gatewayStopped {
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
			runner := newCommittedV2Runner()
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
				_, rollbackInstalled := runner.files[journal.Resources.FinalContainerID+":"+"/config/rollback.json"]
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
			if _, ok := runner.files[journal.Resources.FinalContainerID+":"+"/config/rollback.json"]; !ok {
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

func TestGatewayV2LANCommittedPublicationProofCoversEveryApp(t *testing.T) {
	_, _, state, _, first, _ := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	firstBinding, _ := gatewayV2LANBindingForRequest(first)
	secondBinding, _ := gatewayV2LANBindingForRequest(second)
	firstApp := state.Apps[first.AppID]
	firstApp.LAN = &firstBinding
	state.Apps[first.AppID] = firstApp
	secondApp := state.Apps[second.AppID]
	secondApp.LAN = &secondBinding
	state.Apps[second.AppID] = secondApp
	base, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	requests := map[uint16]gatewayV2LANGrantRequest{first.Port: first, second.Port: second}
	challenges := make(map[uint16]string, len(requests))
	for port, request := range requests {
		challenges[port] = gatewayV2LANAppChallenge(base, port, caddyV2LANAssignment{
			AppID: request.AppID, AllocationID: request.AllocationID,
			AccessRevisionID: request.AccessRevisionID, AccessRevisionNumber: request.AccessRevisionNumber,
			AccessSpecDigest: request.AccessSpecDigest,
		})
	}
	wrongHostFailurePort := uint16(0)
	probe := func(_ context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
		if address == "127.0.0.1" || address != state.Profile.SelectedIPv4 {
			return gatewayV2HostProbeResult{}
		}
		if host == "wrong.invalid" {
			status := http.StatusNotFound
			if port == wrongHostFailurePort {
				status = http.StatusOK
			}
			return gatewayV2HostProbeResult{Status: status, Connected: true, Responded: true}
		}
		challenge := challenges[port]
		if host == state.Profile.SelectedIPv4 && path == gatewayV2ChallengePathPrefix+challenge {
			return gatewayV2HostProbeResult{
				Status: http.StatusNotFound, Body: gatewayV2ChallengeBodyPrefix + challenge,
				Connected: true, Responded: true,
			}
		}
		return gatewayV2HostProbeResult{Status: http.StatusNoContent, Connected: true, Responded: true}
	}
	caddyID := strings.Repeat("e", 64)
	containerProbe := func(_ context.Context, gotID, address string, port uint16, host, challenge string) bool {
		return gotID == caddyID && address == state.Network.ContainerIPv4 && host == state.Profile.SelectedIPv4 &&
			challenges[port] == challenge
	}
	if !proveGatewayV2LANCommittedPublications(context.Background(), state, caddyID, probe, containerProbe) {
		t.Fatal("exact publications for both apps were not proven")
	}
	wrongHostFailurePort = second.Port
	if proveGatewayV2LANCommittedPublications(context.Background(), state, caddyID, probe, containerProbe) {
		t.Fatal("first app proof masked second app wrong-Host exposure")
	}
}

func TestGatewayV2LANObservationAndResolutionHoldGatewayLock(t *testing.T) {
	manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
	grant, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		run  func(context.Context, GatewayV2LANGrantRequest, func(context.Context, GatewayV2LANGrantObservation) error) error
	}{
		{name: "read only", run: manager.WithGatewayV2LANObservation},
		{name: "terminal resolution", run: manager.WithGatewayV2LANResolution},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := test.run(context.Background(), request, func(_ context.Context, observation GatewayV2LANGrantObservation) error {
				called = true
				if manager.mu.TryLock() {
					manager.mu.Unlock()
					t.Fatal("observation callback ran without the gateway lock")
				}
				if observation.Request != request || observation.Disposition != GatewayV2LANGrantCommitted ||
					observation.GatewayOperationID != grant.Receipt.GatewayOperationID ||
					observation.ProtectedStateDigest != grant.Receipt.ProtectedStateDigest || observation.ObservedAt.IsZero() {
					t.Fatalf("observation=%#v grant=%#v", observation, grant)
				}
				if _, exposesURL := reflect.TypeOf(observation).FieldByName("URL"); exposesURL {
					t.Fatal("LAN observation exposed a URL")
				}
				return nil
			})
			if err != nil || !called {
				t.Fatalf("called=%t err=%v", called, err)
			}
		})
	}
}

func TestGatewayV2LANRecoveryWithdrawsUncertainGrantAndPreservesOtherApp(t *testing.T) {
	manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	other := request
	other.AttemptID = "15151515-1515-4515-8515-151515151515"
	other.ClaimRequestDigest = strings.Repeat("2", 64)
	other.AppID = upgradeTestAppB
	other.AllocationID = "16161616-1616-4616-8616-161616161616"
	other.OwnerOperationID = "17171717-1717-4717-8717-171717171717"
	other.AccessRevisionID = "18181818-1818-4818-8818-181818181818"
	other.AccessRevisionNumber = 4
	other.ApprovedBy = "19191919-1919-4919-8919-191919191919"
	other.Port = 8101
	other.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, other)
	authorizeOther, _ := allowGatewayV2LANGrant(t, manager, other, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), other, authorizeOther); err != nil {
		t.Fatal(err)
	}
	committed, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	previous := cloneGatewayV2AppRoute(committed.Apps[request.AppID])
	proposedApp := cloneGatewayV2AppRoute(previous)
	proposedApp.LAN = &binding
	pending := cloneGatewayV2RouteState(committed)
	pending.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposedApp,
	}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}
	uncertain := cloneGatewayV2RouteState(pending)
	uncertain.Pending.ActivationUncertain = true
	if err := store.saveCommittedV2State(uncertain, journal); err != nil {
		t.Fatal(err)
	}
	proposed := cloneGatewayV2RouteState(committed)
	proposed.Apps[request.AppID] = proposedApp
	driver.live = cloneGatewayV2RouteState(proposed)
	driver.events = nil

	observation, err := manager.ObserveGatewayV2LAN(context.Background(), request)
	if err != nil || observation.Disposition != GatewayV2LANGrantPendingPublished ||
		!observation.ActivationUncertain {
		t.Fatalf("pre-recovery observation=%#v err=%v", observation, err)
	}
	recovery, err := manager.RecoverGatewayV2LAN(context.Background(), request)
	if err != nil || recovery.Observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
		!recovery.Observation.ActivationUncertain || recovery.Observation.ObservedAt.IsZero() {
		t.Fatalf("recovery=%#v err=%v events=%v", recovery, err, driver.events)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, uncertain) || !reflect.DeepEqual(driver.live, committed) {
		t.Fatalf("retained=%#v live=%#v err=%v", retained, driver.live, err)
	}
	if retained.Apps[other.AppID].LAN == nil ||
		!reflect.DeepEqual(retained.Apps[other.AppID].LAN, committed.Apps[other.AppID].LAN) {
		t.Fatal("recovery disturbed the other app binding")
	}
	if !containsString(driver.events, "apply:lan-grant-recovery.json") ||
		!containsString(driver.events, "prove_rolled_back") {
		t.Fatalf("recovery did not apply and prove exact 404: %v", driver.events)
	}
	withdrawn, err := manager.ObserveGatewayV2LAN(context.Background(), request)
	if err != nil || withdrawn.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
		withdrawn.ProtectedStateDigest != recovery.Observation.ProtectedStateDigest {
		t.Fatalf("withdrawn observation=%#v err=%v", withdrawn, err)
	}
}

func TestGatewayV2LANRecoveryRejectsUnknownPendingTopology(t *testing.T) {
	manager, store, committed, journal, request, driver := gatewayV2LANGrantFixture(t)
	binding, _ := gatewayV2LANBindingForRequest(request)
	previous := cloneGatewayV2AppRoute(committed.Apps[request.AppID])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = &binding
	pending := cloneGatewayV2RouteState(committed)
	pending.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposed,
	}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}
	driver.live = gatewayV2RouteState{}
	driver.events = nil
	if recovery, err := manager.RecoverGatewayV2LAN(context.Background(), request); !IsCode(err, DiagnosticRouteUnresolved) ||
		!reflect.DeepEqual(recovery, GatewayV2LANGrantRecovery{}) {
		t.Fatalf("recovery=%#v err=%v", recovery, err)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, pending) || containsString(driver.events, "apply:lan-grant-recovery.json") {
		t.Fatalf("retained=%#v events=%v err=%v", retained, driver.events, err)
	}
}

func TestGatewayV2LANAbsenceResolutionProves404WithOrWithoutAppRoute(t *testing.T) {
	for _, appID := range []string{upgradeTestAppA, "23232323-2323-4323-8323-232323232323"} {
		t.Run(appID, func(t *testing.T) {
			manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
			request.AppID = appID
			request.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, request)
			called := false
			err := manager.WithGatewayV2LANAbsenceResolution(context.Background(), request,
				func(_ context.Context, observation GatewayV2LANGrantObservation) error {
					called = true
					if manager.mu.TryLock() {
						manager.mu.Unlock()
						t.Fatal("absence callback ran without gateway lock")
					}
					if observation.Request != request ||
						observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
						observation.GatewayOperationID == "" || !validSHA256(observation.ProtectedStateDigest) ||
						observation.ObservedAt.IsZero() {
						t.Fatalf("observation=%#v", observation)
					}
					if appID != upgradeTestAppA && (observation.Slot != "" || len(observation.Endpoints) != 0) {
						t.Fatalf("absent app synthesized route provenance: %#v", observation)
					}
					return nil
				})
			if err != nil || !called {
				t.Fatalf("called=%t err=%v", called, err)
			}
		})
	}
}

func TestGatewayV2LANAbsenceResolutionRejectsPortOwnedByAnotherApp(t *testing.T) {
	manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
	other := gatewayV2LANSecondAppRequest(t, request)
	other.Port = request.Port
	other.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, other)
	authorize, _ := allowGatewayV2LANGrant(t, manager, other, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), other, authorize); err != nil {
		t.Fatal(err)
	}
	called := false
	err := manager.WithGatewayV2LANAbsenceResolution(context.Background(), request,
		func(context.Context, GatewayV2LANGrantObservation) error {
			called = true
			return nil
		})
	if !IsCode(err, DiagnosticRouteUnresolved) || called {
		t.Fatalf("called=%t err=%v", called, err)
	}
}

func TestGatewayV2LANRollbackResolutionClearsOnlyAfterDBCallback(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		manager, store, committed, pending, journal, request, driver := pendingGatewayV2LANGrantFixture(t, true)
		called := false
		err := manager.WithGatewayV2LANRollbackResolution(context.Background(), request,
			func(_ context.Context, observation GatewayV2LANGrantObservation) error {
				called = true
				if observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
					!observation.ActivationUncertain || observation.ObservedAt.IsZero() {
					t.Fatalf("observation=%#v", observation)
				}
				installed, _, err := store.loadBoundUpgrade(journal.OperationID)
				if err != nil || !reflect.DeepEqual(installed, pending) {
					t.Fatalf("pending marker cleared before DB callback: state=%#v err=%v", installed, err)
				}
				return nil
			})
		if err != nil || !called {
			t.Fatalf("called=%t err=%v events=%v", called, err, driver.events)
		}
		cleared, _, err := store.loadBoundUpgrade(journal.OperationID)
		if err != nil || !reflect.DeepEqual(cleared, committed) || !reflect.DeepEqual(driver.live, committed) {
			t.Fatalf("cleared=%#v live=%#v err=%v", cleared, driver.live, err)
		}
	})

	t.Run("callback failure retains pending", func(t *testing.T) {
		manager, store, _, pending, journal, request, _ := pendingGatewayV2LANGrantFixture(t, false)
		callbackErr := errors.New("injected DB rollback failure")
		err := manager.WithGatewayV2LANRollbackResolution(context.Background(), request,
			func(context.Context, GatewayV2LANGrantObservation) error { return callbackErr })
		if !errors.Is(err, callbackErr) {
			t.Fatalf("err=%v", err)
		}
		retained, _, err := store.loadBoundUpgrade(journal.OperationID)
		if err != nil || !reflect.DeepEqual(retained, pending) {
			t.Fatalf("retained=%#v err=%v", retained, err)
		}
	})

	t.Run("ambiguous clear restores pending", func(t *testing.T) {
		manager, store, _, pending, journal, request, _ := pendingGatewayV2LANGrantFixture(t, true)
		originalWrite := upgradeProtectedWrite
		writes := 0
		upgradeProtectedWrite = func(path, purpose string, body []byte) error {
			writes++
			err := originalWrite(path, purpose, body)
			if writes == 1 && err == nil {
				return errors.New("injected post-install clear failure")
			}
			return err
		}
		t.Cleanup(func() { upgradeProtectedWrite = originalWrite })
		err := manager.WithGatewayV2LANRollbackResolution(context.Background(), request,
			func(context.Context, GatewayV2LANGrantObservation) error { return nil })
		if !IsCode(err, DiagnosticRouteUnresolved) || writes < 2 {
			t.Fatalf("err=%v writes=%d", err, writes)
		}
		retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || !reflect.DeepEqual(retained, pending) {
			t.Fatalf("retained=%#v err=%v", retained, loadErr)
		}
	})
}

func TestGatewayV2LANCommitResolutionQuarantinesFailedOrAmbiguousDBCommit(t *testing.T) {
	for _, test := range []struct {
		name               string
		committedBeforeErr bool
	}{
		{name: "callback failed before DB commit"},
		{name: "callback ambiguous after DB commit", committedBeforeErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
			authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
			if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
				t.Fatal(err)
			}
			published, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			driver.events = nil
			dbCommitted := false
			callbackErr := errors.New("injected terminal DB uncertainty")
			err = manager.WithGatewayV2LANCommitResolution(context.Background(), request,
				func(_ context.Context, receipt GatewayV2LANGrantReceipt) error {
					if receipt.Request != request || receipt.GatewayOperationID != published.OperationID ||
						!validSHA256(receipt.ProtectedStateDigest) || receipt.ObservedAt.IsZero() {
						t.Fatalf("receipt=%#v", receipt)
					}
					dbCommitted = test.committedBeforeErr
					return callbackErr
				})
			if !errors.Is(err, callbackErr) || dbCommitted != test.committedBeforeErr {
				t.Fatalf("err=%v dbCommitted=%t", err, dbCommitted)
			}
			retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil || retained.Pending == nil ||
				retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
				retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil ||
				retained.Pending.Previous.LAN.GrantAttemptID != request.AttemptID ||
				!retained.Pending.ActivationUncertain || driver.live.Apps[request.AppID].LAN != nil {
				t.Fatalf("retained=%#v live=%#v err=%v", retained, driver.live, loadErr)
			}
			if !containsString(driver.events, "apply:lan-grant-quarantine.json") ||
				!containsString(driver.events, "prove_rolled_back") {
				t.Fatalf("events=%v", driver.events)
			}
		})
	}
}

func TestGatewayV2LANCommitRecoveryRequiresDBProofBeforeRepublishing(t *testing.T) {
	manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("ambiguous terminal DB commit")
	if err := manager.WithGatewayV2LANCommitResolution(context.Background(), request,
		func(context.Context, GatewayV2LANGrantReceipt) error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatal(err)
	}
	driver.events = nil
	validationCalls := 0
	if err := manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
		func(_ context.Context, observation GatewayV2LANGrantObservation) error {
			validationCalls++
			if observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
				!observation.ActivationUncertain ||
				(validationCalls == 1 && driver.live.Apps[request.AppID].LAN != nil) {
				t.Fatalf("observation=%#v live=%#v", observation, driver.live)
			}
			return nil
		}); err != nil || validationCalls < 10 {
		t.Fatalf("recovery err=%v validationCalls=%d events=%v", err, validationCalls, driver.events)
	}
	installed, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || installed.Pending != nil || installed.Apps[request.AppID].LAN == nil ||
		driver.live.Apps[request.AppID].LAN == nil {
		t.Fatalf("installed=%#v live=%#v err=%v", installed, driver.live, err)
	}
}

type gatewayV2EmergencyStopRunner struct {
	inspection gatewayContainerInspection
	stopTarget string
}

func (r *gatewayV2EmergencyStopRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" &&
		args[4] == gatewayV2ContainerName {
		body, err := json.Marshal(r.inspection)
		return runtimeprocess.CommandResult{Stdout: body}, err
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "stop" && args[2] == "--time" && args[3] == "10" {
		if normalizeID(args[4]) != normalizeID(r.inspection.ID) {
			return runtimeprocess.CommandResult{}, errors.New("emergency stop targeted a replacement container")
		}
		r.stopTarget = args[4]
		r.inspection.Running = false
		r.inspection.Restarting = false
		r.inspection.EffectivePortBindings = map[string][]map[string]string{}
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected emergency stop Docker command")
}

func TestGatewayV2ProductionEmergencyStopUsesBoundIdentityWithForeignImageLabels(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gatewayContainerInspection)
		valid  bool
	}{
		{name: "foreign image metadata", valid: true},
		{name: "additional Rig label", mutate: func(value *gatewayContainerInspection) {
			value.Labels["io.rig.unexpected"] = "metadata"
		}},
		{name: "replacement container ID", mutate: func(value *gatewayContainerInspection) {
			value.ID = "sha256:" + strings.Repeat("9", 64)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, _, state, journal, _, _ := gatewayV2LANGrantFixture(t)
			labels := gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole, true)
			labels["org.opencontainers.image.title"] = "Caddy"
			runner := &gatewayV2EmergencyStopRunner{inspection: gatewayContainerInspection{
				caddyInspection: caddyInspection{
					ID: "sha256:" + journal.Resources.FinalContainerID, Name: "/" + gatewayV2ContainerName,
					Labels: labels, Running: true,
				},
				gatewayContainerRuntime: gatewayContainerRuntime{},
			}}
			if test.mutate != nil {
				test.mutate(&runner.inspection)
			}
			manager.runner = runner

			err := (managerGatewayV2LANGrantDriver{manager: manager}).stopOwnedGateway(context.Background(), journal)
			if test.valid {
				if err != nil || normalizeID(runner.stopTarget) != journal.Resources.FinalContainerID || runner.inspection.Running {
					t.Fatalf("bound emergency stop err=%v target=%q running=%t", err, runner.stopTarget, runner.inspection.Running)
				}
				return
			}
			if err == nil || runner.stopTarget != "" {
				t.Fatalf("identity drift emergency stop err=%v target=%q", err, runner.stopTarget)
			}
		})
	}
}

func TestGatewayV2EmergencyStopConstructionBypassesUnavailableDatabaseFenceOnlyForExactOwnedStop(t *testing.T) {
	priorFailStop := gatewayRebindProcessFailStop.Load()
	t.Cleanup(func() { gatewayRebindProcessFailStop.Store(priorFailStop) })
	manager, _, state, journal, _, _ := gatewayV2LANGrantFixture(t)
	labels := gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole, true)
	runner := &gatewayV2EmergencyStopRunner{inspection: gatewayContainerInspection{
		caddyInspection: caddyInspection{
			ID: "sha256:" + journal.Resources.FinalContainerID, Name: "/" + gatewayV2ContainerName,
			Labels: labels, Running: true,
		},
		gatewayContainerRuntime: gatewayContainerRuntime{},
	}}
	manager.options.RebindFenceCheck = func(context.Context) error {
		return errors.New("database unavailable during emergency stop")
	}
	if err := StopOwnedGatewayV2OnStartupFailure(context.Background(), runner, manager.options); err != nil ||
		normalizeID(runner.stopTarget) != journal.Resources.FinalContainerID || runner.inspection.Running {
		t.Fatalf("err=%v target=%q running=%t", err, runner.stopTarget, runner.inspection.Running)
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
	failGrantProofAt        int
	grantProofCalls         int
	allGrantProofCalls      int
	proveAllGrantedHook     func(int)
	failRollbackProof       bool
	pendingTopology         gatewayV2PendingLiveTopology
	applyHook               func()
	stopOwnedGatewayErr     error
	gatewayStopped          bool
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
	if d.applyHook != nil {
		d.applyHook()
	}
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

func (d *fakeGatewayV2LANGrantDriver) applyCommitRecovery(ctx context.Context, state gatewayV2RouteState,
	filename string, guard func(context.Context) error,
) error {
	d.checkLocked()
	d.events = append(d.events, "apply:"+filename)
	// Mirror the production copy, validate, reload, active-copy, rename, and
	// post-rename authority boundaries. Reload is the first serving effect.
	for step := 0; step < 6; step++ {
		if err := guard(ctx); err != nil {
			return err
		}
		if step == 2 {
			d.live = cloneGatewayV2RouteState(state)
		}
	}
	return nil
}

func (d *fakeGatewayV2LANGrantDriver) proveGranted(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_granted")
	d.grantProofCalls++
	return !d.failGrantProof && d.failGrantProofAt != d.grantProofCalls &&
		reflect.DeepEqual(d.live, state) && state.Apps[request.AppID].LAN != nil
}

func (d *fakeGatewayV2LANGrantDriver) proveAllGranted(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal,
) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_all_granted")
	d.allGrantProofCalls++
	if d.proveAllGrantedHook != nil {
		d.proveAllGrantedHook(d.allGrantProofCalls)
	}
	return !d.failGrantProof && reflect.DeepEqual(d.live, state)
}

func (d *fakeGatewayV2LANGrantDriver) proveRolledBack(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	d.checkLocked()
	d.events = append(d.events, "prove_rolled_back")
	return !d.failRollbackProof && reflect.DeepEqual(d.live, state) && state.Apps[request.AppID].LAN == nil
}

func (d *fakeGatewayV2LANGrantDriver) observePending(_ context.Context, committed, proposed gatewayV2RouteState,
	_ gatewayMigrationJournal,
) gatewayV2PendingLiveTopology {
	d.checkLocked()
	d.events = append(d.events, "observe_pending")
	if d.pendingTopology != "" {
		return d.pendingTopology
	}
	if reflect.DeepEqual(d.live, committed) {
		return gatewayV2PendingCommittedExact
	}
	if reflect.DeepEqual(d.live, proposed) {
		return gatewayV2PendingProposedExact
	}
	return gatewayV2PendingUnknown
}

func (d *fakeGatewayV2LANGrantDriver) stopOwnedGateway(_ context.Context, _ gatewayMigrationJournal) error {
	d.checkLocked()
	d.events = append(d.events, "stop_owned_gateway")
	if d.stopOwnedGatewayErr != nil {
		return d.stopOwnedGatewayErr
	}
	d.gatewayStopped = true
	return nil
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

func assertGatewayV2LANGrantPending(t *testing.T, store *gatewayUpgradeStateStore,
	journal gatewayMigrationJournal, before gatewayV2RouteState, request gatewayV2LANGrantRequest,
	driver *fakeGatewayV2LANGrantDriver, wantEvents []string,
) {
	t.Helper()
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || after.Pending == nil || after.Pending.Kind != gatewayV2PendingLANGrant ||
		after.Pending.AppID != request.AppID || after.Pending.Proposed.LAN == nil ||
		after.Pending.Proposed.LAN.GrantAttemptID != request.AttemptID ||
		!reflect.DeepEqual(driver.live, before) || !reflect.DeepEqual(driver.events, wantEvents) {
		t.Fatalf("state=%#v live=%#v events=%v err=%v", after, driver.live, driver.events, err)
	}
	committed := cloneGatewayV2RouteState(after)
	committed.Pending = nil
	if !reflect.DeepEqual(committed, before) {
		t.Fatalf("pending grant changed committed state: got=%#v want=%#v", committed, before)
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

func pendingGatewayV2LANGrantFixture(t *testing.T, uncertain bool) (*Manager, *gatewayUpgradeStateStore,
	gatewayV2RouteState, gatewayV2RouteState, gatewayMigrationJournal, gatewayV2LANGrantRequest,
	*fakeGatewayV2LANGrantDriver,
) {
	t.Helper()
	manager, store, committed, journal, request, driver := gatewayV2LANGrantFixture(t)
	binding, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	previous := cloneGatewayV2AppRoute(committed.Apps[request.AppID])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = &binding
	pending := cloneGatewayV2RouteState(committed)
	pending.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposed,
	}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}
	if uncertain {
		armed := cloneGatewayV2RouteState(pending)
		armed.Pending.ActivationUncertain = true
		if err := store.saveCommittedV2State(armed, journal); err != nil {
			t.Fatal(err)
		}
		pending = armed
	}
	driver.live = cloneGatewayV2RouteState(committed)
	driver.events = nil
	return manager, store, committed, pending, journal, request, driver
}

func gatewayV2LANGrantRequestForState(t *testing.T, state gatewayV2RouteState) gatewayV2LANGrantRequest {
	t.Helper()
	request := gatewayV2LANGrantRequest{
		AttemptID: "12121212-1212-4212-8212-121212121212", ClaimRequestDigest: strings.Repeat("1", 64),
		AppID: upgradeTestAppA, AllocationID: "66666666-6666-4666-8666-666666666666", Port: 8100,
		OwnerOperationID: "13131313-1313-4313-8313-131313131313",
		AccessRevisionID: "77777777-7777-4777-8777-777777777777", AccessRevisionNumber: 3,
		ApprovedBy:               "14141414-1414-4414-8414-141414141414",
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
