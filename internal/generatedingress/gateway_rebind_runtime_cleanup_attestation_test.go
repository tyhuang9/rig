package generatedingress

import (
	"context"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindRuntimeCleanupGenericAttestationAcceptsComposedCommittedServing(t *testing.T) {
	for _, test := range []struct {
		name                 string
		foreignImageMetadata bool
	}{
		{name: "baseline"},
		{name: "foreign image metadata", foreignImageMetadata: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, commitDriver := newGatewayRebindCurrentAttestationTwoAppCompositionFixture(t)
			metadataInjected := false
			if test.foreignImageMetadata {
				commitDriver.runner.afterCreate = func() {
					commitDriver.runner.final.Labels = cloneGatewayCurrentPhysicalTestContainer(
						commitDriver.runner.final).Labels
					commitDriver.runner.final.Labels["org.opencontainers.image.title"] = "Caddy"
					metadataInjected = true
				}
			}

			ctx := context.Background()
			result, err := fixture.manager.commitGatewayRebindWithDriver(ctx, fixture.repository, input, commitDriver)
			if err != nil || result.FinalPhase != appaccess.GatewayRebindCommitted || !result.FenceReleased ||
				!commitDriver.boundaryCalled || commitDriver.driverEntries != 1 || commitDriver.delegateRefused ||
				metadataInjected != test.foreignImageMetadata {
				t.Fatalf("composed commit prerequisite incomplete: phase=%q released=%t boundary=%t entries=%d refused=%t metadata=%t error=%v",
					result.FinalPhase, result.FenceReleased, commitDriver.boundaryCalled, commitDriver.driverEntries,
					commitDriver.delegateRefused, metadataInjected, err)
			}

			beforeSQL, err := fixture.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			selection, err := fixture.manager.selectGatewayCurrentLocked(ctx, beforeSQL)
			if err != nil || selection.State == nil || selection.State.Pending != nil || selection.State.LANRecovery != nil {
				t.Fatalf("composed commit did not select stable current: selection=%#v error=%v", selection, err)
			}
			target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			beforeHistory, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			beforeEffects := len(commitDriver.runner.effects)
			beforeBarrier := fixture.manager.gatewayRebindCommitBarrierLatch().Load()
			beforeFailStop := fixture.manager.gatewayRebindFailStopLatch().Load()

			installed, ok := fixture.manager.gatewayCurrentPhysicalDriver.(*gatewayRebindCurrentAttestationCompositionObserver)
			if !ok {
				t.Fatalf("composed commit did not retain concrete managed current driver: %T",
					fixture.manager.gatewayCurrentPhysicalDriver)
			}
			fixture.manager.gatewayCurrentPhysicalDriver = installed.managedGatewayCurrentPhysicalDriver

			proof, diagnostic, attestErr := liveGatewayRebindRuntimeCleanupAttestCurrent(ctx, fixture.manager, selection)
			restored, restoredOK := fixture.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
			restoredRuntime, runtimeRestored := restored.runtime.(managerGatewayCurrentPhysicalRuntime)
			afterSQL, afterSQLErr := fixture.repository.GatewayRebindRecoverySnapshot(ctx)
			afterHistory, afterHistoryErr := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			effectDelta := len(commitDriver.runner.effects) - beforeEffects
			if attestErr != nil || proof.Outcome != gatewayCurrentPhysicalStableServing ||
				!gatewayCurrentPhysicalAttestationAtTarget(proof, target) || !diagnostic.wrapped || diagnostic.managerRefused ||
				diagnostic.driverEntries != 1 || diagnostic.retirementEntries == 0 || diagnostic.runtimeEntries == 0 ||
				diagnostic.driverFailures != 0 || diagnostic.retirementFailures != 0 || diagnostic.runtimeFailures != 0 ||
				!restoredOK || restored.manager() != installed.manager() || !runtimeRestored || restoredRuntime.manager != fixture.manager {
				t.Fatalf("generic cleanup attestation refused composed current: outcome=%q at_target=%t diagnostic=%+v restored=%t effects=%d error=%v",
					proof.Outcome, gatewayCurrentPhysicalAttestationAtTarget(proof, target), diagnostic, restoredOK, effectDelta, attestErr)
			}
			if afterSQLErr != nil || afterHistoryErr != nil || !reflect.DeepEqual(beforeSQL, afterSQL) ||
				!sameGatewayHistorySnapshot(beforeHistory, afterHistory) || effectDelta != 0 ||
				fixture.manager.gatewayRebindCommitBarrierLatch().Load() != beforeBarrier ||
				fixture.manager.gatewayRebindFailStopLatch().Load() != beforeFailStop {
				t.Fatalf("generic cleanup attestation changed authority or runtime: sql=%v history=%v effects=%d barrier=%t fail_stop=%t",
					afterSQLErr, afterHistoryErr, effectDelta, fixture.manager.gatewayRebindCommitBarrierLatch().Load(),
					fixture.manager.gatewayRebindFailStopLatch().Load())
			}
			if test.foreignImageMetadata &&
				commitDriver.runner.final.Labels["org.opencontainers.image.title"] != "Caddy" {
				t.Fatal("generic cleanup attestation changed foreign image metadata")
			}
		})
	}
}
