package generatedingress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type gatewayCurrentPhysicalDriverFake struct {
	apply      gatewayCurrentPhysicalAttestation
	attest     gatewayCurrentPhysicalAttestation
	applyCalls int
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

	// A grant arms ActivationUncertain in a second protected pending revision
	// immediately before its SQL Activate effect. The physical reattestation
	// binds that exact retained marker and the next effective revision without
	// manufacturing a different original baseline.
	chained := transition
	chained.Pending = cloneGatewayCurrentRouteState(pending)
	chained.Pending.Revision++
	chained.Pending.Digest, err = gatewayCurrentRouteStateDigest(chained.Pending)
	if err != nil {
		t.Fatal(err)
	}
	chained.Effective = cloneGatewayCurrentRouteState(effective)
	chained.Effective.Revision = chained.Pending.Revision + 1
	chained.Effective.Digest, err = gatewayCurrentRouteStateDigest(chained.Effective)
	if err != nil || !validGatewayCurrentPhysicalTransition(chained) {
		t.Fatalf("chained pending transition rejected: %v", err)
	}
}
