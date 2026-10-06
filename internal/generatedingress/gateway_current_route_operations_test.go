package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type routeOperationSnapshotRepository struct {
	snapshots []appaccess.GatewayRebindRecoverySnapshot
	calls     int
	errorAt   map[int]error
}

func (r *routeOperationSnapshotRepository) GatewayRebindRecoverySnapshot(context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	r.calls++
	if err := r.errorAt[r.calls]; err != nil {
		return appaccess.GatewayRebindRecoverySnapshot{}, err
	}
	if len(r.snapshots) == 0 {
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("missing test recovery snapshot")
	}
	index := r.calls - 1
	if index >= len(r.snapshots) {
		index = len(r.snapshots) - 1
	}
	return r.snapshots[index], nil
}

func TestGatewayCurrentRouteOperationsPreserveRawBindingsAndTransferManifest(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	repository := &routeOperationSnapshotRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot}}
	fixture.manager.options.RebindCurrentStateRepository = repository

	transferredAppID, transferred := routeOperationTransferredApp(t, fixture.baseline)
	retainedBinding := cloneGatewayCurrentOperationBinding(*transferred.LAN)
	switchRequest := routeOperationSwitchRequest(t, transferredAppID, transferred.Route)
	handled, err := fixture.manager.switchGatewayCurrentRouteLocked(context.Background(), switchRequest)
	if err != nil || !handled {
		t.Fatalf("switch selected current route: handled=%t error=%v", handled, err)
	}
	afterSwitch := routeOperationLoad(t, fixture.store)
	if afterSwitch.Revision != fixture.baseline.Revision+1 ||
		afterSwitch.Apps[transferredAppID].Route.Slot != switchRequest.ToSlot ||
		!reflect.DeepEqual(afterSwitch.Apps[transferredAppID].LAN, &retainedBinding) ||
		afterSwitch.TransferManifestDigest != fixture.baseline.TransferManifestDigest {
		t.Fatalf("route head advance changed raw/transfer authority: %#v", afterSwitch.Apps[transferredAppID])
	}

	nativeAppID := uuid.NewString()
	seed := afterSwitch.Apps[transferredAppID].Route
	create := generatedruntime.RouteSwitchRequest{
		AppID: nativeAppID, ToSlot: generatedruntime.SlotBlue,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), seed.Endpoints...),
	}
	handled, err = fixture.manager.switchGatewayCurrentRouteLocked(context.Background(), create)
	if err != nil || !handled {
		t.Fatalf("create native current route: handled=%t error=%v", handled, err)
	}
	afterCreate := routeOperationLoad(t, fixture.store)
	grant := routeOperationNativeGrantRequest(t, afterCreate, nativeAppID)
	handled, err = fixture.manager.grantGatewayCurrentLANLocked(context.Background(), grant)
	if err != nil || !handled {
		t.Fatalf("grant native current LAN: handled=%t error=%v", handled, err)
	}
	afterGrant := routeOperationLoad(t, fixture.store)
	native := afterGrant.Apps[nativeAppID]
	if native.LAN == nil || native.LAN.Transfer != nil ||
		native.LAN.Raw.ProfileRevisionID != afterGrant.Profile.RevisionID ||
		native.LAN.Raw.ProfileRevisionNumber != afterGrant.Profile.RevisionNumber ||
		native.LAN.Raw.ProfileSpecDigest != afterGrant.Profile.SpecDigest ||
		transferred.LAN.Raw.ProfileRevisionID == afterGrant.Profile.RevisionID {
		t.Fatalf("native and transferred raw profiles were not kept distinct: native=%#v transferred=%#v",
			native.LAN, transferred.LAN)
	}

	disable := disableRequestForGrant(t,
		gatewayV2LANGrantRequestForBinding(transferredAppID, retainedBinding.Raw))
	handled, err = fixture.manager.disableGatewayCurrentLANLocked(context.Background(), disable)
	if err != nil || !handled {
		t.Fatalf("disable transferred current LAN: handled=%t error=%v", handled, err)
	}
	afterTransferredDisable := routeOperationLoad(t, fixture.store)
	if afterTransferredDisable.Apps[transferredAppID].LAN != nil ||
		afterTransferredDisable.Apps[nativeAppID].LAN == nil ||
		afterTransferredDisable.TransferManifestDigest != fixture.baseline.TransferManifestDigest ||
		!gatewayCurrentTransfersMatchState(snapshot.CurrentTransfers, afterTransferredDisable) {
		t.Fatalf("transferred disable changed unrelated live or historical authority: %#v", afterTransferredDisable)
	}
	handled, err = fixture.manager.disableGatewayCurrentLANLocked(
		context.Background(), disableRequestForGrant(t, grant))
	if err != nil || !handled {
		t.Fatalf("disable native current LAN: handled=%t error=%v", handled, err)
	}
	final := routeOperationLoad(t, fixture.store)
	if final.Revision != fixture.baseline.Revision+5 || final.Apps[transferredAppID].LAN != nil ||
		final.Apps[nativeAppID].LAN != nil || final.TransferManifestDigest != fixture.baseline.TransferManifestDigest ||
		!gatewayCurrentTransfersMatchState(snapshot.CurrentTransfers, final) || repository.calls != 30 {
		t.Fatalf("ordinary mutations lost immutable transfer history: revision=%d calls=%d state=%#v",
			final.Revision, repository.calls, final)
	}
}

func TestGatewayCurrentRouteOperationsRejectSQLSelectionDrift(t *testing.T) {
	t.Run("before protected write", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		current := routeOperationCommittedSnapshot(fixture)
		changed := current
		changed.RollbackAllowed = !current.RollbackAllowed
		fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{current, changed},
		}
		appID, app := routeOperationTransferredApp(t, fixture.baseline)
		handled, err := fixture.manager.switchGatewayCurrentRouteLocked(
			context.Background(), routeOperationSwitchRequest(t, appID, app.Route))
		if handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("SQL drift before selection was not refused: handled=%t error=%v", handled, err)
		}
		if got := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(got, fixture.baseline) {
			t.Fatal("SQL drift advanced protected state")
		}
	})

	t.Run("immediately before protected write", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		current := routeOperationCommittedSnapshot(fixture)
		changed := current
		changed.RollbackAllowed = !current.RollbackAllowed
		repository := &routeOperationSnapshotRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{
			current, current, changed, changed,
		}}
		fixture.manager.options.RebindCurrentStateRepository = repository
		appID, app := routeOperationTransferredApp(t, fixture.baseline)
		handled, err := fixture.manager.switchGatewayCurrentRouteLocked(
			context.Background(), routeOperationSwitchRequest(t, appID, app.Route))
		if !handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("SQL drift before write was not refused: handled=%t error=%v", handled, err)
		}
		retained := routeOperationLoad(t, fixture.store)
		if !reflect.DeepEqual(retained, fixture.baseline) || repository.calls != 4 {
			t.Fatalf("pre-write drift advanced protected state: revision=%d calls=%d", retained.Revision, repository.calls)
		}
	})

	t.Run("after protected write", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		current := routeOperationCommittedSnapshot(fixture)
		changed := current
		changed.RollbackAllowed = !current.RollbackAllowed
		repository := &routeOperationSnapshotRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{
			current, current, current, current, changed, changed,
		}}
		fixture.manager.options.RebindCurrentStateRepository = repository
		appID, app := routeOperationTransferredApp(t, fixture.baseline)
		handled, err := fixture.manager.switchGatewayCurrentRouteLocked(
			context.Background(), routeOperationSwitchRequest(t, appID, app.Route))
		if !handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("SQL drift after write was not reported fail closed: handled=%t error=%v", handled, err)
		}
		advanced := routeOperationLoad(t, fixture.store)
		if advanced.Revision != fixture.baseline.Revision+1 || repository.calls != 6 {
			t.Fatalf("unexpected protected write/read sequence: revision=%d calls=%d", advanced.Revision, repository.calls)
		}
	})
}

func TestGatewayCurrentRouteOperationRejectsStaleProtectedStateAndCallbackAuthority(t *testing.T) {
	t.Run("stale selected state", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		snapshot := routeOperationCommittedSnapshot(fixture)
		fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
		}
		appID, app := routeOperationTransferredApp(t, fixture.baseline)
		handled, err := fixture.manager.mutateGatewayCurrentLocked(context.Background(),
			func(proposed gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
				competing := cloneGatewayCurrentOperationState(fixture.baseline)
				competingApp := competing.Apps[appID]
				competingApp.Route.Slot, _ = generatedruntime.InactiveSlot(competingApp.Route.Slot)
				competing.Apps[appID] = competingApp
				competing.Revision++
				competing.Digest, _ = gatewayCurrentRouteStateDigest(competing)
				if saveErr := fixture.store.saveNext(fixture.baseline, competing); saveErr != nil {
					t.Fatal(saveErr)
				}
				app.Route.Slot, _ = generatedruntime.InactiveSlot(app.Route.Slot)
				proposed.Apps[appID] = app
				return proposed, nil
			})
		if !handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("stale protected state was not refused: handled=%t error=%v", handled, err)
		}
		installed := routeOperationLoad(t, fixture.store)
		if installed.Revision != fixture.baseline.Revision+1 {
			t.Fatalf("stale writer replaced competing state: %#v", installed)
		}
	})

	t.Run("callback changes immutable origin", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		snapshot := routeOperationCommittedSnapshot(fixture)
		fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
		}
		handled, err := fixture.manager.mutateGatewayCurrentLocked(context.Background(),
			func(proposed gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
				proposed.TransferManifestDigest = strings.Repeat("f", 64)
				return proposed, nil
			})
		if !handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("callback authority change was not refused: handled=%t error=%v", handled, err)
		}
		if got := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(got, fixture.baseline) {
			t.Fatal("callback changed immutable origin")
		}
		handled, err = fixture.manager.mutateGatewayCurrentLocked(context.Background(),
			func(proposed gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
				proposed.Revision++
				return proposed, nil
			})
		if !handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("callback revision change was not refused: handled=%t error=%v", handled, err)
		}
		if got := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(got, fixture.baseline) {
			t.Fatal("callback chose its own protected revision")
		}
	})
}

func TestGatewayCurrentRouteOperationProviderAbsenceIsFailClosed(t *testing.T) {
	t.Run("scoped rebind current", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		appID, app := routeOperationTransferredApp(t, fixture.baseline)
		handled, err := fixture.manager.switchGatewayCurrentRouteLocked(
			context.Background(), routeOperationSwitchRequest(t, appID, app.Route))
		if handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("missing provider selected scoped current: handled=%t error=%v", handled, err)
		}
	})

	t.Run("validated native upgrade with provider", func(t *testing.T) {
		manager, _, _, _, _, _ := gatewayV2LANGrantFixture(t)
		history, historyErr := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if historyErr != nil {
			t.Fatal(historyErr)
		}
		lineage, lineageErr := gatewayUpgradeCurrentLineage(history.Predecessor)
		if lineageErr != nil {
			t.Fatal(lineageErr)
		}
		profile := gatewayCurrentFixtureProfile(lineage, history.Predecessor.State.Profile)
		authority := gatewayCurrentAuthority(lineage)
		manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{{
			CurrentProfile: &profile, CurrentSource: &authority,
		}}}
		called := false
		handled, err := manager.mutateGatewayCurrentLocked(context.Background(),
			func(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
				called = true
				return state, nil
			})
		if err != nil || handled || called {
			t.Fatalf("validated upgrade did not use legacy caller path: handled=%t called=%t error=%v",
				handled, called, err)
		}
	})

	t.Run("native upgrade without provider", func(t *testing.T) {
		manager, _, _, _, _, _ := gatewayV2LANGrantFixture(t)
		handled, err := manager.mutateGatewayCurrentLocked(context.Background(),
			func(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) { return state, nil })
		if handled || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("missing provider selected native upgrade: handled=%t error=%v", handled, err)
		}
	})
}

func routeOperationCommittedSnapshot(fixture gatewayCurrentStateFixture) appaccess.GatewayRebindRecoverySnapshot {
	profile := gatewayCurrentFixtureProfile(fixture.baseline.Lineage, fixture.baseline.Profile)
	authority := gatewayCurrentAuthority(fixture.baseline.Lineage)
	committed := appaccess.GatewayRebindHistoryEntry{
		Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: 1, Legacy: &appaccess.GatewayRebindClaim{
			Spec: appaccess.GatewayRebindSpec{OperationID: fixture.receipt.OperationID},
		}},
		Events: []appaccess.GatewayRebindEvent{{OperationID: fixture.receipt.OperationID,
			Sequence: 4, State: appaccess.GatewayRebindDatabaseCommitted}},
		Transfers: append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers...),
	}
	currentEvent := committed.Events[0]
	return appaccess.GatewayRebindRecoverySnapshot{
		History:                       []appaccess.GatewayRebindHistoryEntry{committed},
		CurrentProfile:                &profile,
		CurrentSource:                 &authority,
		CurrentDatabaseCommittedEvent: &currentEvent,
		CurrentTransfers:              append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers...),
	}
}

func routeOperationTransferredApp(t *testing.T, state gatewayCurrentRouteState) (string, gatewayCurrentAppRoute) {
	t.Helper()
	for appID, app := range state.Apps {
		if app.LAN != nil && app.LAN.Transfer != nil {
			return appID, cloneGatewayCurrentOperationApp(app)
		}
	}
	t.Fatal("fixture has no transferred current app")
	return "", gatewayCurrentAppRoute{}
}

func routeOperationSwitchRequest(t *testing.T, appID string, route routeRecord) generatedruntime.RouteSwitchRequest {
	t.Helper()
	next, err := generatedruntime.InactiveSlot(route.Slot)
	if err != nil {
		t.Fatal(err)
	}
	return generatedruntime.RouteSwitchRequest{AppID: appID, FromSlot: route.Slot, ToSlot: next,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), route.Endpoints...)}
}

func routeOperationNativeGrantRequest(t *testing.T, state gatewayCurrentRouteState,
	appID string,
) GatewayV2LANGrantRequest {
	t.Helper()
	used := make(map[uint16]struct{})
	for _, app := range state.Apps {
		if app.LAN != nil {
			used[app.LAN.Raw.Port] = struct{}{}
		}
	}
	portValue := uint32(state.Profile.PortStart)
	for ; portValue <= uint32(state.Profile.PortEnd); portValue++ {
		if _, exists := used[uint16(portValue)]; !exists {
			break
		}
	}
	if portValue > uint32(state.Profile.PortEnd) {
		t.Fatal("fixture current profile has no free LAN port")
	}
	port := uint16(portValue)
	request := GatewayV2LANGrantRequest{
		AttemptID: uuid.NewString(), ClaimRequestDigest: strings.Repeat("1", 64), AppID: appID,
		AllocationID: uuid.NewString(), OwnerOperationID: uuid.NewString(), Port: port,
		AccessRevisionID: uuid.NewString(), AccessRevisionNumber: 1, ApprovedBy: uuid.NewString(),
		GatewayProfileRevisionID:     state.Profile.RevisionID,
		GatewayProfileRevisionNumber: state.Profile.RevisionNumber,
		GatewayProfileSpecDigest:     state.Profile.SpecDigest,
	}
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: appID, AllocationID: request.AllocationID, Port: port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	request.AccessSpecDigest = digest
	return request
}

func routeOperationLoad(t *testing.T, store *gatewayCurrentRouteStateStore) gatewayCurrentRouteState {
	t.Helper()
	state, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	return state
}
