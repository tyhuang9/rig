package generatedingress

import (
	"context"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindAdmissionCurrentPhysicalMatchesClosingInventory(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State, localPort)
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtime := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
	inventory, err := runtime.read(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer clearGatewayCurrentPhysicalInventory(&inventory)
	serving, err := runtime.attestation(context.Background(), target, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.ApplicationNetworks) == 0 || !validSHA256(inventory.EndpointDigest) {
		t.Fatal("running fixture lacks application network or endpoint evidence")
	}
	physical := gatewayRebindCurrentPredecessorPhysical{Running: true,
		Digest: serving.Digest, Serving: &serving}

	for _, test := range []struct {
		name   string
		accept bool
		rehash bool
		mutate func(*gatewayCurrentPhysicalAttestation, *gatewayCurrentPhysicalInventory)
	}{
		{name: "unchanged running inventory", accept: true},
		{name: "equivalent config formatting", accept: true,
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.LiveConfig = append([]byte("\n "), value.LiveConfig...)
				value.RestartConfig = append([]byte("\t "), value.RestartConfig...)
			}},
		{name: "changed live config",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.LiveConfig = []byte(`{"apps":{}}`)
			}},
		{name: "missing live config",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.LiveConfig = nil
			}},
		{name: "changed restart config",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.RestartConfig = []byte(`{"apps":{}}`)
			}},
		{name: "missing restart config",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.RestartConfig = nil
			}},
		{name: "changed endpoint proof",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.EndpointDigest = strings.Repeat("f", 64)
			}},
		{name: "missing endpoint proof",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.EndpointDigest = ""
			}},
		{name: "changed application network",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.ApplicationNetworks[0].ID = strings.Repeat("f", 64)
			}},
		{name: "missing application networks",
			mutate: func(_ *gatewayCurrentPhysicalAttestation, value *gatewayCurrentPhysicalInventory) {
				value.ApplicationNetworks = nil
			}},
		{name: "rehashed wrong active config proof", rehash: true,
			mutate: func(value *gatewayCurrentPhysicalAttestation, _ *gatewayCurrentPhysicalInventory) {
				value.Runtime.ActiveConfigDigest = strings.Repeat("f", 64)
			}},
		{name: "rehashed wrong route proof", rehash: true,
			mutate: func(value *gatewayCurrentPhysicalAttestation, _ *gatewayCurrentPhysicalInventory) {
				value.Runtime.RoutePlanDigest = strings.Repeat("f", 64)
			}},
		{name: "rehashed wrong listener proof", rehash: true,
			mutate: func(value *gatewayCurrentPhysicalAttestation, _ *gatewayCurrentPhysicalInventory) {
				value.Runtime.ListenerDigest = strings.Repeat("f", 64)
			}},
		{name: "rehashed wrong application networks proof", rehash: true,
			mutate: func(value *gatewayCurrentPhysicalAttestation, _ *gatewayCurrentPhysicalInventory) {
				value.Runtime.ApplicationNetworksDigest = strings.Repeat("f", 64)
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changedPhysical, changedInventory := physical, inventory
			changedServing := serving
			changedPhysical.Serving = &changedServing
			changedInventory.LiveConfig = append([]byte(nil), inventory.LiveConfig...)
			changedInventory.RestartConfig = append([]byte(nil), inventory.RestartConfig...)
			changedInventory.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), inventory.ApplicationNetworks...)
			defer clear(changedInventory.LiveConfig)
			defer clear(changedInventory.RestartConfig)
			if test.mutate != nil {
				test.mutate(&changedServing, &changedInventory)
			}
			if test.rehash {
				changedServing.Runtime.Digest, err = gatewayCurrentRuntimeProofDigest(changedServing.Runtime)
				if err != nil {
					t.Fatal(err)
				}
				changedServing.Digest, err = gatewayCurrentPhysicalAttestationDigest(changedServing)
				if err != nil {
					t.Fatal(err)
				}
				changedPhysical.Digest = changedServing.Digest
			}
			if !validGatewayCurrentPhysicalAttestation(changedServing) {
				t.Fatal("test must preserve an internally valid serving attestation")
			}
			if got := gatewayRebindAdmissionCurrentPhysicalMatches(target, changedPhysical, changedInventory); got != test.accept {
				t.Fatalf("closing inventory accepted=%t, want %t", got, test.accept)
			}
		})
	}
}

type gatewayRebindAdmissionClosingInventoryRunner struct {
	*gatewayCurrentPhysicalExecutor
	reads  int
	mutate func(*gatewayCurrentPhysicalExecutor)
}

func (r *gatewayRebindAdmissionClosingInventoryRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if len(request.Args) >= 2 && request.Args[0] == "image" && request.Args[1] == "inspect" {
		r.reads++
		// Two predecessor observations consume two inventories each. Drift only
		// after those proofs, at the final admission inventory read.
		if r.reads == 5 && r.mutate != nil {
			r.mutate(r.gatewayCurrentPhysicalExecutor)
		}
	}
	return r.gatewayCurrentPhysicalExecutor.Run(ctx, request)
}

func TestGatewayRebindAdmissionCurrentPredecessorRejectsClosingConfigDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		accept bool
		mutate func(*gatewayCurrentPhysicalExecutor)
	}{
		{name: "unchanged running inventory", accept: true},
		{name: "closing live config changed", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.live = []byte(`{"apps":{}}`)
		}},
		{name: "closing restart config changed", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.files[runner.target.Identity.Rebind.ActiveConfigFilename] = []byte(`{"apps":{}}`)
		}},
		{name: "both closing configs changed", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.live = []byte(`{"apps":{}}`)
			runner.files[runner.target.Identity.Rebind.ActiveConfigFilename] = []byte(`{"apps":{}}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayCurrentStateFixture(t)
			target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
			localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
			runner := &gatewayRebindAdmissionClosingInventoryRunner{
				gatewayCurrentPhysicalExecutor: newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State, localPort),
				mutate:                         test.mutate}
			fixture.manager.runner = runner
			fixture.manager.options.HostPort = localPort
			handover := gatewayRebindTypedHandoverRuntime{manager: fixture.manager,
				hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
				containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
			runtime := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
				hostProbe: handover.hostProbe, containerProbe: handover.containerProbe}
			selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: fixture.baseline.Lineage,
				Receipt: &fixture.receipt, State: &fixture.baseline}
			proof, err := fixture.manager.readGatewayRebindAdmissionPredecessorWithRuntimeLocked(context.Background(),
				selection, fixture.manager.inspectGatewayRebindDocker, handover, runtime)
			if proof.Current != nil {
				defer clearGatewayCurrentPhysicalInventory(&proof.Current.Inventory)
			}
			if runner.reads != 5 || len(runner.effects) != 0 {
				t.Fatalf("admission reads=%d effects=%d, want five reads and no effects", runner.reads, len(runner.effects))
			}
			if test.accept {
				if err != nil || proof.Current == nil || !proof.Current.Physical.Running || proof.Current.Physical.Serving == nil {
					t.Fatalf("unchanged running predecessor was refused: %v", err)
				}
			} else if !IsCode(err, DiagnosticRouteUnresolved) || proof.Current != nil || proof.Upgrade != nil {
				t.Fatalf("closing config drift returned a predecessor proof or unexpected error: %v", err)
			}
		})
	}
}
