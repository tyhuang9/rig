package generatedingress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type gatewayCurrentPhysicalDriverFake struct {
	apply        gatewayCurrentPhysicalAttestation
	restore      gatewayCurrentPhysicalAttestation
	stop         gatewayCurrentPhysicalAttestation
	attest       gatewayCurrentPhysicalAttestation
	applyCalls   int
	restoreCalls int
	stopCalls    int
}

func (f *gatewayCurrentPhysicalDriverFake) stopGatewayCurrentPhysical(context.Context,
	gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	f.stopCalls++
	return f.stop, nil
}

func (f *gatewayCurrentPhysicalDriverFake) restoreGatewayCurrentPhysical(context.Context,
	gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	f.restoreCalls++
	return f.restore, nil
}

func (f *gatewayCurrentPhysicalDriverFake) applyGatewayCurrentPhysical(context.Context,
	gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	f.applyCalls++
	return f.apply, nil
}

func (f *gatewayCurrentPhysicalDriverFake) attestGatewayCurrentPhysical(context.Context,
	gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	return f.attest, nil
}

func (*gatewayCurrentPhysicalDriverFake) stopGatewayCurrentOwnedPredecessor(context.Context,
	gatewayRebindAttemptTerminalView,
) error {
	return errors.New("not used")
}

func gatewayCurrentPhysicalAttestationFixture(t *testing.T, state gatewayCurrentRouteState,
	terminal gatewayRebindAttemptTerminalView, outcome gatewayCurrentPhysicalOutcome,
) gatewayCurrentPhysicalAttestation {
	t.Helper()
	if terminal.Resources.FinalContainer == nil {
		t.Fatal("terminal has no final container")
	}
	runtime := gatewayCurrentRuntimeProof{
		Profile: GatewayV2ProfileBinding{RevisionID: state.Profile.RevisionID,
			RevisionNumber: state.Profile.RevisionNumber, SpecDigest: state.Profile.SpecDigest,
			SelectedIPv4: state.Profile.SelectedIPv4, InterfaceID: state.Profile.InterfaceID,
			PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd},
		ImageID: terminal.Resources.ImageID, ContainerID: terminal.Resources.FinalContainer.ID,
		ActiveConfigDigest: strings.Repeat("1", 64), RoutePlanDigest: strings.Repeat("2", 64),
		ListenerDigest: strings.Repeat("3", 64), ApplicationNetworksDigest: strings.Repeat("4", 64),
	}
	var err error
	runtime.Digest, err = gatewayCurrentRuntimeProofDigest(runtime)
	if err != nil {
		t.Fatal(err)
	}
	value := gatewayCurrentPhysicalAttestation{Outcome: outcome, State: cloneGatewayCurrentRouteState(state),
		Lineage: state.Lineage, Terminal: terminal, Identity: state.Identity,
		Resources: terminal.Resources, Runtime: runtime,
		Pending:     cloneGatewayCurrentPendingRoute(state.Pending),
		LANRecovery: cloneGatewayCurrentLANRecoveryBatch(state.LANRecovery)}
	value.Digest, err = gatewayCurrentPhysicalAttestationDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGatewayCurrentPhysicalTransitionValidatesDurablePendingAndExactAttestation(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneGatewayCurrentRouteState(fixture.baseline)
	appID := ""
	var previous gatewayCurrentAppRoute
	for candidate, app := range before.Apps {
		appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
		break
	}
	if appID == "" {
		t.Fatal("fixture has no app")
	}
	proposed := cloneGatewayCurrentAppRoute(previous)
	if proposed.Route.Slot == "blue" {
		proposed.Route.Slot = "green"
	} else {
		proposed.Route.Slot = "blue"
	}
	pending := cloneGatewayCurrentRouteState(before)
	pending.Revision++
	pending.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingRouteSwitch, AppID: appID,
		Previous: &previous, Proposed: proposed}
	pending.Digest, err = gatewayCurrentRouteStateDigest(pending)
	if err != nil || !validGatewayCurrentRouteState(pending) {
		t.Fatalf("valid pending state rejected: %v", err)
	}
	effective := cloneGatewayCurrentRouteState(before)
	effective.Revision = pending.Revision + 1
	effective.Apps[appID] = cloneGatewayCurrentAppRoute(proposed)
	effective.Digest, err = gatewayCurrentRouteStateDigest(effective)
	if err != nil || !validGatewayCurrentRouteState(effective) {
		t.Fatalf("valid effective state rejected: %v", err)
	}
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalRouteSwitch, AppID: appID,
		Before: before, Pending: pending, Effective: effective}
	physical := gatewayCurrentPhysicalAttestationFixture(t, effective, terminal, gatewayCurrentPhysicalRecoveryEffective)
	physical.Pending = cloneGatewayCurrentPendingRoute(pending.Pending)
	physical.Digest, err = gatewayCurrentPhysicalAttestationDigest(physical)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentPhysicalDriverFake{apply: physical}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	attestation, err := fixture.manager.applyGatewayCurrentPhysicalLocked(context.Background(), transition)
	if err != nil || driver.applyCalls != 1 || attestation.Outcome != gatewayCurrentPhysicalRecoveryEffective ||
		attestation.State.Digest != effective.Digest {
		t.Fatalf("current physical apply mismatch: outcome=%s calls=%d error=%v", attestation.Outcome, driver.applyCalls, err)
	}
	changed := transition
	changed.Effective.Lineage.OperationID = uuid.NewString()
	if _, err := fixture.manager.applyGatewayCurrentPhysicalLocked(context.Background(), changed); err == nil || driver.applyCalls != 1 {
		t.Fatalf("changed immutable lineage reached physical driver: calls=%d error=%v", driver.applyCalls, err)
	}

}

func TestGatewayCurrentPhysicalTransitionSupportsNewAppAndChainedGrant(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	before := cloneGatewayCurrentRouteState(fixture.baseline)
	var template gatewayCurrentAppRoute
	for _, app := range before.Apps {
		template = cloneGatewayCurrentAppRoute(app)
		break
	}
	template.LAN = nil
	newAppID := uuid.NewString()
	pendingNew := cloneGatewayCurrentRouteState(before)
	pendingNew.Revision++
	pendingNew.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingRouteSwitch, AppID: newAppID,
		Proposed: template}
	var err error
	pendingNew.Digest, err = gatewayCurrentRouteStateDigest(pendingNew)
	if err != nil || !validGatewayCurrentRouteState(pendingNew) {
		t.Fatalf("new-app pending state rejected: %v", err)
	}
	effectiveNew := cloneGatewayCurrentRouteState(before)
	effectiveNew.Revision = pendingNew.Revision + 1
	effectiveNew.Apps[newAppID] = cloneGatewayCurrentAppRoute(template)
	effectiveNew.Digest, err = gatewayCurrentRouteStateDigest(effectiveNew)
	newApp := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalRouteSwitch, AppID: newAppID,
		Before: before, Pending: pendingNew, Effective: effectiveNew}
	if err != nil || !validGatewayCurrentPhysicalTransition(newApp) {
		t.Fatalf("new-app Previous=nil transition rejected: %v", err)
	}

	grantBefore := cloneGatewayCurrentRouteState(before)
	grantAppID := ""
	var grantPrevious gatewayCurrentAppRoute
	for candidate, app := range grantBefore.Apps {
		grantAppID, grantPrevious = candidate, cloneGatewayCurrentAppRoute(app)
		break
	}
	grantPrevious.LAN = nil
	grantBefore.Apps[grantAppID] = cloneGatewayCurrentAppRoute(grantPrevious)
	grantBefore.Digest, err = gatewayCurrentRouteStateDigest(grantBefore)
	if err != nil || !validGatewayCurrentRouteState(grantBefore) {
		t.Fatalf("grant before state rejected: %v", err)
	}
	used := make(map[uint16]struct{})
	for _, app := range grantBefore.Apps {
		if app.LAN != nil {
			used[app.LAN.Raw.Port] = struct{}{}
		}
	}
	port := grantBefore.Profile.PortStart
	for ; port <= grantBefore.Profile.PortEnd; port++ {
		if _, exists := used[port]; !exists {
			break
		}
	}
	request := gatewayV2LANGrantRequest{
		AttemptID: uuid.NewString(), ClaimRequestDigest: strings.Repeat("9", 64), AppID: grantAppID,
		AllocationID: uuid.NewString(), OwnerOperationID: uuid.NewString(), Port: port,
		AccessRevisionID: uuid.NewString(), AccessRevisionNumber: 7, ApprovedBy: uuid.NewString(),
		GatewayProfileRevisionID:     grantBefore.Profile.RevisionID,
		GatewayProfileRevisionNumber: grantBefore.Profile.RevisionNumber,
		GatewayProfileSpecDigest:     grantBefore.Profile.SpecDigest,
	}
	request.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, request)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	grantProposed := cloneGatewayCurrentAppRoute(grantPrevious)
	grantProposed.LAN = &gatewayCurrentLANBinding{Raw: raw}
	grantPending := cloneGatewayCurrentRouteState(grantBefore)
	grantPending.Revision += 2
	grantPending.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANGrant, AppID: grantAppID,
		Previous: &grantPrevious, Proposed: grantProposed, ActivationUncertain: true}
	grantPending.Digest, err = gatewayCurrentRouteStateDigest(grantPending)
	if err != nil || !validGatewayCurrentRouteState(grantPending) {
		t.Fatalf("chained grant pending rejected: %v", err)
	}
	grantEffective := cloneGatewayCurrentRouteState(grantBefore)
	grantEffective.Revision = grantPending.Revision + 1
	grantEffective.Apps[grantAppID] = cloneGatewayCurrentAppRoute(grantProposed)
	grantEffective.Digest, err = gatewayCurrentRouteStateDigest(grantEffective)
	chained := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalLANGrant, AppID: grantAppID,
		Before: grantBefore, Pending: grantPending, Effective: grantEffective}
	if err != nil || !validGatewayCurrentPhysicalTransition(chained) {
		t.Fatalf("ActivationUncertain chained grant transition rejected: %v", err)
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	restored := gatewayCurrentPhysicalAttestationFixture(t, grantBefore, terminal,
		gatewayCurrentPhysicalRecoveryBefore)
	restored.Pending = cloneGatewayCurrentPendingRoute(grantPending.Pending)
	restored.Digest, err = gatewayCurrentPhysicalAttestationDigest(restored)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentPhysicalDriverFake{restore: restored}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	if _, err := fixture.manager.restoreGatewayCurrentPhysicalLocked(context.Background(), chained); err != nil ||
		driver.restoreCalls != 1 {
		t.Fatalf("pre-activation grant restore mismatch: calls=%d error=%v", driver.restoreCalls, err)
	}
}

func TestGatewayCurrentPhysicalWithdrawalQuarantinesAndRestoresExactGrant(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneGatewayCurrentRouteState(fixture.baseline)
	appID := ""
	var previous gatewayCurrentAppRoute
	for candidate, app := range before.Apps {
		if app.LAN != nil {
			appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
			break
		}
	}
	if appID == "" {
		t.Fatal("fixture has no active LAN grant")
	}
	proposed := cloneGatewayCurrentAppRoute(previous)
	proposed.LAN = nil
	pending := cloneGatewayCurrentRouteState(before)
	pending.Revision++
	pending.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANWithdrawal, AppID: appID,
		Previous: &previous, Proposed: proposed, ActivationUncertain: true}
	pending.Digest, err = gatewayCurrentRouteStateDigest(pending)
	if err != nil || !validGatewayCurrentRouteState(pending) {
		t.Fatalf("valid withdrawal marker rejected: %v", err)
	}
	effective := cloneGatewayCurrentRouteState(before)
	effective.Revision = pending.Revision + 1
	effective.Apps[appID] = cloneGatewayCurrentAppRoute(proposed)
	effective.Digest, err = gatewayCurrentRouteStateDigest(effective)
	if err != nil || !validGatewayCurrentRouteState(effective) {
		t.Fatalf("valid withdrawal effective state rejected: %v", err)
	}
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalLANWithdrawal, AppID: appID,
		Before: before, Pending: pending, Effective: effective}
	if !validGatewayCurrentPhysicalTransition(transition) {
		t.Fatal("exact request-bound withdrawal transition rejected")
	}
	quarantined := gatewayCurrentPhysicalAttestationFixture(t, effective, terminal,
		gatewayCurrentPhysicalRecoveryEffective)
	quarantined.Pending = cloneGatewayCurrentPendingRoute(pending.Pending)
	quarantined.Digest, err = gatewayCurrentPhysicalAttestationDigest(quarantined)
	if err != nil {
		t.Fatal(err)
	}
	restored := gatewayCurrentPhysicalAttestationFixture(t, before, terminal,
		gatewayCurrentPhysicalRecoveryBefore)
	restored.Pending = cloneGatewayCurrentPendingRoute(pending.Pending)
	restored.Digest, err = gatewayCurrentPhysicalAttestationDigest(restored)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentPhysicalDriverFake{apply: quarantined, restore: restored}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	if _, err := fixture.manager.applyGatewayCurrentPhysicalLocked(context.Background(), transition); err != nil ||
		driver.applyCalls != 1 {
		t.Fatalf("withdrawal quarantine mismatch: calls=%d error=%v", driver.applyCalls, err)
	}
	listenerAbsent := quarantined
	listenerAbsent.Runtime.ListenerAbsent = true
	listenerAbsent.Runtime.Digest, err = gatewayCurrentRuntimeProofDigest(listenerAbsent.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	listenerAbsent.Digest, err = gatewayCurrentPhysicalAttestationDigest(listenerAbsent)
	if err != nil {
		t.Fatal(err)
	}
	driver.apply = listenerAbsent
	if _, err := fixture.manager.applyGatewayCurrentPhysicalLocked(context.Background(), transition); err == nil ||
		driver.applyCalls != 2 {
		t.Fatalf("listener-absent effective result accepted: calls=%d error=%v", driver.applyCalls, err)
	}
	if _, err := fixture.manager.restoreGatewayCurrentPhysicalLocked(context.Background(), transition); err != nil ||
		driver.restoreCalls != 1 {
		t.Fatalf("withdrawal restore mismatch: calls=%d error=%v", driver.restoreCalls, err)
	}
	stopped := gatewayCurrentPhysicalAttestationFixture(t, effective, terminal,
		gatewayCurrentPhysicalRecoveryStopped)
	stopped.Pending = cloneGatewayCurrentPendingRoute(pending.Pending)
	stopped.Runtime.ListenerAbsent = true
	stopped.Runtime.Digest, err = gatewayCurrentRuntimeProofDigest(stopped.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	stopped.Digest, err = gatewayCurrentPhysicalAttestationDigest(stopped)
	if err != nil {
		t.Fatal(err)
	}
	driver.stop = stopped
	if _, err := fixture.manager.stopGatewayCurrentPhysicalLocked(context.Background(), transition); err != nil ||
		driver.stopCalls != 1 {
		t.Fatalf("exact selected-current stop mismatch: calls=%d error=%v", driver.stopCalls, err)
	}
	foreign := stopped
	foreign.Resources = stopped.Resources
	foreign.Resources.FinalContainer = &gatewayRebindFinalContainerBinding{
		ID: strings.Repeat("a", 64), OwnershipDigest: strings.Repeat("b", 64),
		ConfigurationDigest: strings.Repeat("c", 64),
	}
	foreign.Resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(foreign.Resources)
	if err != nil {
		t.Fatal(err)
	}
	foreign.Runtime.ContainerID = foreign.Resources.FinalContainer.ID
	foreign.Runtime.Digest, err = gatewayCurrentRuntimeProofDigest(foreign.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	foreign.Digest, err = gatewayCurrentPhysicalAttestationDigest(foreign)
	if err != nil {
		t.Fatal(err)
	}
	driver.stop = foreign
	if _, err := fixture.manager.stopGatewayCurrentPhysicalLocked(context.Background(), transition); err == nil ||
		driver.stopCalls != 2 {
		t.Fatalf("foreign stop resources accepted: calls=%d error=%v", driver.stopCalls, err)
	}

	changed := transition
	changed.Pending.Pending = cloneGatewayCurrentPendingRoute(transition.Pending.Pending)
	changed.Pending.Pending.Previous = nil
	if _, err := fixture.manager.restoreGatewayCurrentPhysicalLocked(context.Background(), changed); err == nil ||
		driver.restoreCalls != 1 {
		t.Fatalf("changed request-bound grant reached restore driver: calls=%d error=%v", driver.restoreCalls, err)
	}
	disable := transition
	disable.Kind = gatewayCurrentPhysicalLANDisable
	if _, err := fixture.manager.restoreGatewayCurrentPhysicalLocked(context.Background(), disable); err == nil ||
		driver.restoreCalls != 1 {
		t.Fatalf("synthetic disable reached restore driver: calls=%d error=%v", driver.restoreCalls, err)
	}
}

func TestGatewayCurrentPhysicalAttestationBindsPendingOutcomeState(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneGatewayCurrentRouteState(fixture.baseline)
	appID := ""
	var previous gatewayCurrentAppRoute
	for candidate, app := range before.Apps {
		appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
		break
	}
	proposed := cloneGatewayCurrentAppRoute(previous)
	if proposed.Route.Slot == "blue" {
		proposed.Route.Slot = "green"
	} else {
		proposed.Route.Slot = "blue"
	}
	pending := cloneGatewayCurrentRouteState(before)
	pending.Revision++
	pending.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingRouteSwitch, AppID: appID,
		Previous: &previous, Proposed: proposed}
	pending.Digest, err = gatewayCurrentRouteStateDigest(pending)
	if err != nil || !validGatewayCurrentRouteState(pending) {
		t.Fatalf("valid pending state rejected: %v", err)
	}
	effectiveProjection := cloneGatewayCurrentRouteState(pending)
	effectiveProjection.Pending = nil
	effectiveProjection.Apps[appID] = cloneGatewayCurrentAppRoute(proposed)
	effectiveProjection.Digest, err = gatewayCurrentRouteStateDigest(effectiveProjection)
	if err != nil || !validGatewayCurrentRouteState(effectiveProjection) {
		t.Fatalf("valid effective projection rejected: %v", err)
	}
	physical := gatewayCurrentPhysicalAttestationFixture(t, effectiveProjection, terminal,
		gatewayCurrentPhysicalRecoveryEffective)
	physical.Pending = cloneGatewayCurrentPendingRoute(pending.Pending)
	physical.Digest, err = gatewayCurrentPhysicalAttestationDigest(physical)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentPhysicalDriverFake{attest: physical}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: pending.Lineage,
		Receipt: &fixture.receipt, State: &pending}
	if _, err := fixture.manager.attestGatewayCurrentPhysicalLocked(context.Background(), selection); err != nil {
		t.Fatalf("exact effective projection rejected: %v", err)
	}

	unrelated := cloneGatewayCurrentRouteState(effectiveProjection)
	unrelatedApp := cloneGatewayCurrentAppRoute(unrelated.Apps[appID])
	if unrelatedApp.Route.Slot == "blue" {
		unrelatedApp.Route.Slot = "green"
	} else {
		unrelatedApp.Route.Slot = "blue"
	}
	unrelated.Apps[appID] = unrelatedApp
	unrelated.Digest, err = gatewayCurrentRouteStateDigest(unrelated)
	if err != nil || !validGatewayCurrentRouteState(unrelated) {
		t.Fatalf("unrelated route fixture invalid: %v", err)
	}
	driver.attest = gatewayCurrentPhysicalAttestationFixture(t, unrelated, terminal,
		gatewayCurrentPhysicalRecoveryEffective)
	driver.attest.Pending = cloneGatewayCurrentPendingRoute(pending.Pending)
	driver.attest.Digest, err = gatewayCurrentPhysicalAttestationDigest(driver.attest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.attestGatewayCurrentPhysicalLocked(context.Background(), selection); err == nil {
		t.Fatal("unrelated valid route state accepted for pending selection")
	}
}

func TestGatewayCurrentWithdrawalRecoveryBatchBindsExactRawGrant(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	appID := ""
	var previous gatewayCurrentAppRoute
	for candidate, app := range state.Apps {
		if app.LAN != nil {
			appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
			break
		}
	}
	if appID == "" {
		t.Fatal("fixture has no LAN app")
	}
	request := gatewayV2LANGrantRequest{
		AttemptID: "12121212-1212-4212-8212-121212121212", ClaimRequestDigest: strings.Repeat("9", 64),
		AppID: appID, AllocationID: "66666666-6666-4666-8666-666666666666", Port: previous.LAN.Raw.Port,
		OwnerOperationID: "13131313-1313-4313-8313-131313131313",
		AccessRevisionID: "77777777-7777-4777-8777-777777777777", AccessRevisionNumber: 3,
		ApprovedBy:                   "14141414-1414-4414-8414-141414141414",
		GatewayProfileRevisionID:     state.Profile.RevisionID,
		GatewayProfileRevisionNumber: state.Profile.RevisionNumber,
		GatewayProfileSpecDigest:     state.Profile.SpecDigest,
	}
	request.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, request)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	native := gatewayCurrentLANBinding{Raw: raw}
	previous.LAN = &native
	state.Apps[appID] = cloneGatewayCurrentAppRoute(previous)
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		t.Fatalf("native current binding invalid: %v", err)
	}
	proposed := cloneGatewayCurrentAppRoute(previous)
	proposed.LAN = nil
	legacy := gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANWithdrawal, AppID: appID,
		Previous: &previous, Proposed: proposed, ActivationUncertain: true}
	grant := cloneGatewayCurrentLANBinding(native)
	state.LANRecovery = &gatewayCurrentLANRecoveryBatch{Items: []gatewayCurrentLANRecoveryItem{{
		Kind: gatewayV2PendingLANGrant, AppID: appID, Grant: &grant,
	}}, LegacyPending: &legacy}
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		t.Fatalf("withdrawal recovery batch rejected: %v", err)
	}
	state.LANRecovery.Items[0].Grant.Raw.GrantRequestDigest = strings.Repeat("8", 64)
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	if validGatewayCurrentRouteState(state) {
		t.Fatal("recovery batch accepted a changed raw grant")
	}
}

func TestGatewayCurrentTransferredDisablePreservesRawProfileAndBindsEffectiveProfile(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	appID := ""
	var previous gatewayCurrentAppRoute
	for candidate, app := range state.Apps {
		if app.LAN != nil && app.LAN.Transfer != nil {
			appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
			break
		}
	}
	if appID == "" {
		t.Fatal("fixture has no transferred LAN app")
	}
	grant := gatewayV2LANGrantRequestForBinding(appID, previous.LAN.Raw)
	disable := disableRequestForGrant(t, grant)
	proposed := cloneGatewayCurrentAppRoute(previous)
	proposed.LAN = nil
	state.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANDisable, AppID: appID,
		Previous: &previous, Proposed: proposed, ActivationUncertain: true, Disable: &disable}
	state.Revision++
	var err error
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		t.Fatalf("transferred raw-profile disable rejected: %v", err)
	}
	state.Pending.Previous.LAN.Transfer.SuccessorProfileRevisionID = uuid.NewString()
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	if validGatewayCurrentRouteState(state) {
		t.Fatal("disable accepted a transfer for another effective profile")
	}
}
