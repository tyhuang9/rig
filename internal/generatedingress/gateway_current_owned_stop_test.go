package generatedingress

import (
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
}
