package generatedingress

import (
	"context"
	"strings"
	"testing"
)

func TestManagedGatewayCurrentPhysicalRuntimeAttestationRequiresExactIngressMembership(t *testing.T) {
	fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Effective, transition.Effective, localPort)
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}

	if _, err := runtimeDriver.observe(context.Background(), target); err != nil {
		t.Fatalf("exact ingress membership: %v", err)
	}
	runner.ingress.Containers[strings.Repeat("9", 64)] = caddyNetworkContainerInspection{
		Name: "foreign-ingress-peer", IPv4Address: "172.29.0.99/24",
	}
	if _, err := runtimeDriver.observe(context.Background(), target); err == nil || len(runner.effects) != 0 {
		t.Fatalf("foreign ingress peer admitted: effects=%v error=%v", runner.effects, err)
	}

	delete(runner.ingress.Containers, strings.Repeat("9", 64))
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, transition.Effective))
	if !runtimeDriver.validIngressMembership(target, runner.ingress, runner.container) {
		t.Fatal("stopped exact absence was not accepted")
	}
	runner.ingress.Containers[strings.Repeat("8", 64)] = caddyNetworkContainerInspection{
		Name: "foreign-stopped-peer", IPv4Address: "172.29.0.98/24",
	}
	if runtimeDriver.validIngressMembership(target, runner.ingress, runner.container) {
		t.Fatal("stopped ingress peer was accepted")
	}
}
