package generatedingress

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestGatewayCurrentOwnedStopTargetBindsExactProtectedOwnership(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	target, err := fixture.manager.gatewayCurrentOwnedStopTargetForStateLocked(fixture.baseline)
	if err != nil || !validGatewayCurrentOwnedStopTarget(target) || target.FinalContainer.ID != fixture.receipt.Resources.FinalContainer.ID {
		t.Fatalf("owned-stop target=%#v error=%v", target, err)
	}
	fresh, err := fixture.manager.revalidateGatewayCurrentOwnedStopTargetLocked(target)
	if err != nil || !reflect.DeepEqual(fresh, target) {
		t.Fatalf("owned-stop revalidation=%#v error=%v", fresh, err)
	}
	target.FinalContainer.ID = strings.Repeat("a", 64)
	target.Digest, _ = gatewayCurrentOwnedStopTargetDigest(target)
	if validGatewayCurrentOwnedStopTarget(target) {
		t.Fatal("owned-stop target accepted a rehashed container outside its terminal")
	}

	target, err = fixture.manager.gatewayCurrentOwnedStopTargetForStateLocked(fixture.baseline)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneGatewayCurrentRouteState(fixture.baseline)
	next.Revision++
	next.Digest, _ = gatewayCurrentRouteStateDigest(next)
	if err := fixture.store.saveNext(fixture.baseline, next); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.revalidateGatewayCurrentOwnedStopTargetLocked(target); err == nil {
		t.Fatal("owned-stop target survived an unpermitted protected revision")
	}
}

func TestGatewayCurrentOwnedStopTransitionRejectsUnrelatedPermittedEvidence(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	before := cloneGatewayCurrentRouteState(fixture.baseline)
	var appID string
	var previous gatewayCurrentAppRoute
	for candidate, app := range before.Apps {
		appID, previous = candidate, cloneGatewayCurrentAppRoute(app)
		break
	}
	if appID == "" {
		t.Fatal("fixture has no route")
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
	pending.Digest, _ = gatewayCurrentRouteStateDigest(pending)
	effective := cloneGatewayCurrentRouteState(before)
	effective.Revision = pending.Revision + 1
	effective.Apps[appID] = proposed
	effective.Digest, _ = gatewayCurrentRouteStateDigest(effective)
	transition := gatewayCurrentPhysicalTransition{Kind: gatewayCurrentPhysicalRouteSwitch,
		AppID: appID, Before: before, Pending: pending, Effective: effective}
	target, err := fixture.manager.gatewayCurrentOwnedStopTargetForTransitionLocked(transition)
	if err != nil || !validGatewayCurrentOwnedStopTarget(target) {
		t.Fatalf("transition target=%#v error=%v", target, err)
	}
	changed := target
	changed.PermittedStateDigests = append([]string(nil), target.PermittedStateDigests...)
	changed.PermittedStateDigests[0] = strings.Repeat("f", 64)
	changed.Digest, _ = gatewayCurrentOwnedStopTargetDigest(changed)
	if validGatewayCurrentOwnedStopTarget(changed) {
		t.Fatal("transition target accepted an unrelated permitted state")
	}
	changed = target
	changed.Pending = cloneGatewayCurrentPendingRoute(target.Pending)
	changed.Pending.AppID = "unrelated"
	changed.Digest, _ = gatewayCurrentOwnedStopTargetDigest(changed)
	if validGatewayCurrentOwnedStopTarget(changed) {
		t.Fatal("transition target accepted an unrelated pending marker")
	}
}

func TestGatewayCurrentOwnedStopTargetsEnumerateWithdrawalOnlyBundles(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	targets, err := fixture.manager.gatewayCurrentOwnedStopTargetsProtectedLocked()
	if err != nil || len(targets) != 1 || targets[0].Lineage != fixture.baseline.Lineage ||
		targets[0].FinalContainer.ID != fixture.receipt.Resources.FinalContainer.ID {
		t.Fatalf("protected owned-stop targets=%#v error=%v", targets, err)
	}
	if err := os.Remove(fixture.store.path); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.gatewayCurrentOwnedStopTargetsProtectedLocked(); err == nil {
		t.Fatal("protected owned-stop enumeration ignored a missing current route bundle")
	}
}
