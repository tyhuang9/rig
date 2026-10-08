package generatedingress

import (
	"context"
	"reflect"
)

type gatewayRebindCurrentPredecessorPhysical struct {
	Running bool
	Digest  string
	Serving *gatewayCurrentPhysicalAttestation
}

// A retained predecessor can be stopped while its successor owns shared host
// listeners. Prove the predecessor's exact inventory/config without claiming
// global listener absence. The handover proves contextual withdrawal; rollback
// separately requires full absence before authorizing a predecessor restart.
// Ordinary current observation keeps its stronger RecoveryStopped contract.
func (d gatewayRebindTypedHandoverRuntime) observeCurrentPredecessor(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayRebindCurrentPredecessorPhysical, error) {
	invalid := func() (gatewayRebindCurrentPredecessorPhysical, error) {
		return gatewayRebindCurrentPredecessorPhysical{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil || d.hostProbe == nil || d.containerProbe == nil ||
		!validGatewayCurrentPhysicalTarget(target) || target.Pending != nil || target.LANRecovery != nil {
		return invalid()
	}
	runtime := managerGatewayCurrentPhysicalRuntime{manager: d.manager, hostProbe: d.hostProbe, containerProbe: d.containerProbe}
	first, err := runtime.read(ctx, target)
	if err != nil {
		return invalid()
	}
	defer clearGatewayCurrentPhysicalInventory(&first)
	second, err := runtime.read(ctx, target)
	if err != nil {
		return invalid()
	}
	defer clearGatewayCurrentPhysicalInventory(&second)
	if !reflect.DeepEqual(first, second) || ctx.Err() != nil {
		return invalid()
	}
	if second.Final.Running {
		physical, err := runtime.attestation(ctx, target, second)
		if err != nil || physical.Outcome != gatewayCurrentPhysicalStableServing {
			return invalid()
		}
		return gatewayRebindCurrentPredecessorPhysical{Running: true, Digest: physical.Digest, Serving: &physical}, nil
	}
	expected, err := gatewayCurrentPhysicalConfigBytes(target.State)
	if err != nil {
		return invalid()
	}
	defer clear(expected)
	if second.Final.Restarting || gatewayV2HasEffectivePortBinding(second.FinalRuntime.EffectivePortBindings) ||
		!sameCaddyConfig(expected, second.RestartConfig) {
		return invalid()
	}
	digest, err := canonicalDigest(struct {
		Purpose   string
		Target    gatewayCurrentPhysicalTarget
		Inventory gatewayCurrentPhysicalInventory
	}{"hostd/generated-ingress/rebind/stopped-current-predecessor/v1", target, second})
	if err != nil || !validSHA256(digest) || ctx.Err() != nil {
		return invalid()
	}
	return gatewayRebindCurrentPredecessorPhysical{Digest: digest}, nil
}
