package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type fakeGatewayV2LANSuccessorDriver struct {
	*fakeGatewayV2LANGrantDriver
	failPortAbsence   bool
	failPortAbsenceAt int
	portProofs        int
}

func (d *fakeGatewayV2LANSuccessorDriver) provePortAbsent(_ context.Context, state gatewayV2RouteState,
	_ gatewayMigrationJournal, port uint16,
) bool {
	d.checkLocked()
	d.portProofs++
	if d.failPortAbsence || d.failPortAbsenceAt == d.portProofs || !reflect.DeepEqual(d.live, state) {
		return false
	}
	for _, app := range state.Apps {
		if app.LAN != nil && app.LAN.Port == port {
			return false
		}
	}
	return true
}

func successorRequestForOld(t *testing.T, old GatewayV2LANGrantRequest, newPort bool,
	sameAppSamePort ...bool,
) GatewayV2LANGrantRequest {
	t.Helper()
	next := old
	next.AttemptID = "20202020-2020-4020-8020-202020202020"
	next.ClaimRequestDigest = strings.Repeat("a", 64)
	next.AllocationID = "21212121-2121-4121-8121-212121212121"
	next.OwnerOperationID = "24242424-2424-4424-8424-242424242424"
	next.AccessRevisionID = "22222222-2222-4222-8222-222222222222"
	next.AccessRevisionNumber++
	if newPort {
		next.Port++
	} else if len(sameAppSamePort) == 0 || !sameAppSamePort[0] {
		next.AppID = upgradeTestAppB
	}
	next.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, next)
	return next
}

func successorAttestationFixture(t *testing.T, newPort bool, sameAppSamePort ...bool) (*Manager, *gatewayUpgradeStateStore,
	gatewayMigrationJournal, GatewayV2LANDisableRequest, GatewayV2LANGrantRequest,
	[]GatewayV2LANStartupClaim, []GatewayV2LANDisableStartupClaim, *fakeGatewayV2LANSuccessorDriver,
) {
	t.Helper()
	manager, store, journal, original, driver := grantedLANForDisable(t)
	disable := disableRequestForGrant(t, original)
	authorize, _ := allowGatewayV2LANDisable(t, manager, disable)
	if _, err := manager.DisableGatewayV2LAN(context.Background(), disable, authorize); err != nil {
		t.Fatal(err)
	}
	if err := manager.WithGatewayV2LANDisableResolution(context.Background(), disable,
		permitGatewayV2LANDisableResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	successor := successorRequestForOld(t, original, newPort, sameAppSamePort...)
	authorizeGrant, _ := allowGatewayV2LANGrant(t, manager, successor, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), successor, authorizeGrant); err != nil {
		t.Fatal(err)
	}
	wrapped := &fakeGatewayV2LANSuccessorDriver{fakeGatewayV2LANGrantDriver: driver}
	manager.gatewayV2LANGrantDriver = wrapped
	grants := []GatewayV2LANStartupClaim{
		{
			Request: original, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
			DisableIntentOperationID: disable.OperationID,
		},
		{Request: successor, State: appaccess.AppAccessGrantCommitted, StateSequence: 4},
	}
	disables := []GatewayV2LANDisableStartupClaim{{
		Request: disable, State: appaccess.AppAccessDisableCommitted, StateSequence: 3,
	}}
	return manager, store, journal, disable, successor, grants, disables, wrapped
}

func TestGatewayV2LANDisableSuccessorAttestsLegitimateShapes(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		newPort         bool
		sameAppSamePort bool
		relation        string
	}{
		{"same app new port", true, false, GatewayV2LANSuccessorSameAppNewPort404},
		{"same app same port", false, true, GatewayV2LANSuccessorSamePort},
		{"different app same port", false, false, GatewayV2LANSuccessorSamePort},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager, store, journal, old, next, grants, disables, driver := successorAttestationFixture(t, scenario.newPort, scenario.sameAppSamePort)
			before, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			err = manager.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next, scenario.relation,
				grants, disables, func(_ context.Context, observed GatewayV2LANDisableSuccessorObservation) error {
					calls++
					if manager.mu.TryLock() {
						manager.mu.Unlock()
						t.Fatal("successor acknowledgment lacked gateway lock")
					}
					if observed.Request != old || observed.Successor != next ||
						observed.Relation != scenario.relation || observed.GatewayOperationID != journal.OperationID ||
						!validSHA256(observed.ProtectedStateDigest) || observed.ObservedAt.IsZero() {
						t.Fatal("successor acknowledgment lacked exact locked observation")
					}
					return nil
				})
			if err != nil || calls != 1 || driver.portProofs != map[bool]int{true: 2, false: 0}[scenario.newPort] {
				t.Fatalf("attest err=%v calls=%d port proofs=%d", err, calls, driver.portProofs)
			}
			after, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(driver.live, after) {
				t.Fatalf("attestation mutated route state: err=%v", err)
			}
		})
	}
}

func TestGatewayV2LANDisableSuccessorRejectsUnprovedOrStaleClaims(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(*testing.T, *Manager, *gatewayUpgradeStateStore, gatewayMigrationJournal,
			*GatewayV2LANDisableRequest, *GatewayV2LANGrantRequest, *[]GatewayV2LANStartupClaim,
			*[]GatewayV2LANDisableStartupClaim, *fakeGatewayV2LANSuccessorDriver)
	}{
		{"stale successor census", func(_ *testing.T, _ *Manager, _ *gatewayUpgradeStateStore, _ gatewayMigrationJournal,
			_ *GatewayV2LANDisableRequest, _ *GatewayV2LANGrantRequest, grants *[]GatewayV2LANStartupClaim,
			_ *[]GatewayV2LANDisableStartupClaim, _ *fakeGatewayV2LANSuccessorDriver) {
			(*grants)[1].Request.ClaimRequestDigest = strings.Repeat("b", 64)
		}},
		{"retired successor", func(_ *testing.T, _ *Manager, _ *gatewayUpgradeStateStore, _ gatewayMigrationJournal,
			_ *GatewayV2LANDisableRequest, _ *GatewayV2LANGrantRequest, grants *[]GatewayV2LANStartupClaim,
			_ *[]GatewayV2LANDisableStartupClaim, _ *fakeGatewayV2LANSuccessorDriver) {
			(*grants)[1].DisableIntentOperationID = "23232323-2323-4323-8323-232323232323"
		}},
		{"old source still live", func(t *testing.T, _ *Manager, _ *gatewayUpgradeStateStore, _ gatewayMigrationJournal,
			old *GatewayV2LANDisableRequest, _ *GatewayV2LANGrantRequest, _ *[]GatewayV2LANStartupClaim,
			_ *[]GatewayV2LANDisableStartupClaim, driver *fakeGatewayV2LANSuccessorDriver) {
			binding, err := gatewayV2LANBindingForRequest(*old.SourceGrant)
			if err != nil {
				t.Fatal(err)
			}
			live := cloneGatewayV2RouteState(driver.live)
			app := live.Apps[old.AppID]
			app.LAN = &binding
			live.Apps[old.AppID] = app
			driver.live = live
		}},
		{"pending protected state", func(t *testing.T, _ *Manager, store *gatewayUpgradeStateStore, journal gatewayMigrationJournal,
			_ *GatewayV2LANDisableRequest, next *GatewayV2LANGrantRequest, _ *[]GatewayV2LANStartupClaim,
			_ *[]GatewayV2LANDisableStartupClaim, driver *fakeGatewayV2LANSuccessorDriver) {
			state, _, err := store.loadBoundUpgrade(journal.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			previous := cloneGatewayV2AppRoute(state.Apps[next.AppID])
			proposed := cloneGatewayV2AppRoute(previous)
			proposed.LAN = nil
			state.Pending = &gatewayV2PendingRoute{
				Kind: gatewayV2PendingLANWithdrawal, AppID: next.AppID,
				Previous: &previous, Proposed: proposed, ActivationUncertain: true,
			}
			if err := store.saveCommittedV2State(state, journal); err != nil {
				t.Fatal(err)
			}
			driver.live = cloneGatewayV2RouteState(state)
		}},
		{"failed old port 404", func(_ *testing.T, _ *Manager, _ *gatewayUpgradeStateStore, _ gatewayMigrationJournal,
			_ *GatewayV2LANDisableRequest, _ *GatewayV2LANGrantRequest, _ *[]GatewayV2LANStartupClaim,
			_ *[]GatewayV2LANDisableStartupClaim, driver *fakeGatewayV2LANSuccessorDriver) {
			driver.failPortAbsence = true
		}},
		{"failed successor challenge", func(_ *testing.T, _ *Manager, _ *gatewayUpgradeStateStore, _ gatewayMigrationJournal,
			_ *GatewayV2LANDisableRequest, _ *GatewayV2LANGrantRequest, _ *[]GatewayV2LANStartupClaim,
			_ *[]GatewayV2LANDisableStartupClaim, driver *fakeGatewayV2LANSuccessorDriver) {
			driver.failGrantProof = true
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager, store, journal, old, next, grants, disables, driver := successorAttestationFixture(t, true)
			scenario.mutate(t, manager, store, journal, &old, &next, &grants, &disables, driver)
			called := false
			err := manager.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next,
				GatewayV2LANSuccessorSameAppNewPort404, grants, disables,
				func(context.Context, GatewayV2LANDisableSuccessorObservation) error { called = true; return nil })
			if err == nil || called || driver.gatewayStopped {
				t.Fatalf("unsafe successor accepted: err=%v callback=%t stopped=%t", err, called, driver.gatewayStopped)
			}
		})
	}
}

func TestGatewayV2LANDisableSuccessorCallbackFailureLeavesProtectedStateUntouched(t *testing.T) {
	manager, store, journal, old, next, grants, disables, driver := successorAttestationFixture(t, true)
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("injected acknowledgment failure")
	err = manager.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next,
		GatewayV2LANSuccessorSameAppNewPort404, grants, disables,
		func(context.Context, GatewayV2LANDisableSuccessorObservation) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("acknowledgment failure=%v", err)
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(before, after) || driver.gatewayStopped {
		t.Fatalf("callback failure mutated route: err=%v stopped=%t", err, driver.gatewayStopped)
	}
}

func TestGatewayV2LANDisableSuccessorFinalProofFailureDoesNotAcknowledge(t *testing.T) {
	for _, scenario := range []struct {
		name string
		fail func(*fakeGatewayV2LANSuccessorDriver)
	}{
		{"successor challenge", func(driver *fakeGatewayV2LANSuccessorDriver) {
			driver.failGrantProofAt = driver.grantProofCalls + 2
		}},
		{"old port 404", func(driver *fakeGatewayV2LANSuccessorDriver) {
			driver.failPortAbsenceAt = 2
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager, _, _, old, next, grants, disables, driver := successorAttestationFixture(t, true)
			scenario.fail(driver)
			acknowledged := false
			err := manager.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next,
				GatewayV2LANSuccessorSameAppNewPort404, grants, disables,
				func(context.Context, GatewayV2LANDisableSuccessorObservation) error {
					acknowledged = true
					return nil
				})
			if err == nil || acknowledged || driver.gatewayStopped {
				t.Fatalf("final proof failure: err=%v acknowledged=%t stopped=%t", err, acknowledged, driver.gatewayStopped)
			}
		})
	}
}

func TestGatewayV2LANDisableSuccessorCensusRejectsOldProtectedBinding(t *testing.T) {
	_, store, journal, old, next, grants, disables, _ := successorAttestationFixture(t, true)
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	claimSet, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil || !gatewayV2LANSuccessorCensusMatches(state, claimSet, old, next) {
		t.Fatalf("baseline census err=%v", err)
	}
	binding, err := gatewayV2LANBindingForRequest(*old.SourceGrant)
	if err != nil {
		t.Fatal(err)
	}
	stale := cloneGatewayV2RouteState(state)
	app := stale.Apps[old.AppID]
	app.LAN = &binding
	stale.Apps[old.AppID] = app
	if gatewayV2LANSuccessorCensusMatches(stale, claimSet, old, next) {
		t.Fatal("old source binding accepted alongside successor")
	}
	// Migration can preserve a disable intent without an original grant.
	// The old allocation must still be absent from every protected binding.
	noSource := old
	noSource.SourceGrant = nil
	noSourceClaims := claimSet
	noSourceClaims.disables = map[string]GatewayV2LANDisableStartupClaim{
		old.OperationID: {Request: noSource, State: appaccess.AppAccessDisableCommitted, StateSequence: 3},
	}
	if gatewayV2LANSuccessorCensusMatches(stale, noSourceClaims, noSource, next) {
		t.Fatal("old allocation accepted with nil source grant")
	}
}

func TestGatewayV2LANDisableSuccessorAcceptsLegacyNilSourceGrant(t *testing.T) {
	manager, _, _, _, original, driver := gatewayV2LANGrantFixture(t)
	old := disableRequestForGrant(t, original)
	old.SourceGrant = nil
	next := successorRequestForOld(t, original, false)
	authorize, _ := allowGatewayV2LANGrant(t, manager, next, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), next, authorize); err != nil {
		t.Fatal(err)
	}
	manager.gatewayV2LANGrantDriver = &fakeGatewayV2LANSuccessorDriver{fakeGatewayV2LANGrantDriver: driver}
	grants := []GatewayV2LANStartupClaim{{Request: next, State: appaccess.AppAccessGrantCommitted, StateSequence: 4}}
	disables := []GatewayV2LANDisableStartupClaim{{
		Request: old, State: appaccess.AppAccessDisableCommitted, StateSequence: 3,
	}}
	called := false
	if err := manager.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next,
		GatewayV2LANSuccessorSamePort, grants, disables,
		func(context.Context, GatewayV2LANDisableSuccessorObservation) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("nil-source attestation err=%v called=%t", err, called)
	}
}

func TestGatewayV2LANDisableSuccessorSerializesOtherManagers(t *testing.T) {
	manager, _, _, old, next, grants, disables, driver := successorAttestationFixture(t, true)
	other, err := New(manager.runner, manager.options)
	if err != nil {
		t.Fatal(err)
	}
	otherDriver := &fakeGatewayV2LANSuccessorDriver{fakeGatewayV2LANGrantDriver: &fakeGatewayV2LANGrantDriver{
		t: t, manager: other, live: cloneGatewayV2RouteState(driver.live),
	}}
	other.gatewayV2LANGrantDriver = otherDriver
	originalAcquire := managerAcquireGatewayOSLock
	attempted := make(chan struct{})
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		if store == other.store {
			close(attempted)
		}
		return originalAcquire(ctx, store)
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
	entered := make(chan struct{})
	release := make(chan struct{})
	firstResult := make(chan error, 1)
	secondEntered := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		firstResult <- manager.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next,
			GatewayV2LANSuccessorSameAppNewPort404, grants, disables,
			func(context.Context, GatewayV2LANDisableSuccessorObservation) error {
				close(entered)
				<-release
				return nil
			})
	}()
	<-entered
	go func() {
		secondResult <- other.AttestGatewayV2LANDisableSuccessor(context.Background(), old, next,
			GatewayV2LANSuccessorSameAppNewPort404, grants, disables,
			func(context.Context, GatewayV2LANDisableSuccessorObservation) error {
				close(secondEntered)
				return nil
			})
	}()
	<-attempted
	select {
	case <-secondEntered:
		t.Fatal("contending Manager entered acknowledgment under held gateway lock")
	default:
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	default:
		t.Fatal("contending Manager never entered callback")
	}
}
