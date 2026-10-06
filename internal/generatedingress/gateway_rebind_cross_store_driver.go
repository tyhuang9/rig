package generatedingress

import (
	"context"
	"errors"

	"github.com/hostd/hostd/internal/appaccess"
)

// managerGatewayRebindCrossStoreDriver is the production physical adapter.
// The individual effect cores are filled by the normalized driver checkpoint;
// returning an error here keeps the private commit path fail closed until that
// adapter has proved the complete physical sequence.
type managerGatewayRebindCrossStoreDriver struct{ manager *Manager }

func (d managerGatewayRebindCrossStoreDriver) reconcileSuccessorLocked(context.Context,
	gatewayRebindPhysicalReconcileRequest, gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	return gatewayRebindTypedPhysicalResult{}, errors.New("generated ingress typed physical rebind is unavailable")
}

func (d managerGatewayRebindCrossStoreDriver) proveNoSuccessorEffectsLocked(context.Context,
	appaccess.GatewayRebindClaimV2, []appaccess.GatewayRebindRosterEntryV2,
	gatewayRebindPredecessorCheckpoint,
) (gatewayRebindNoEffectAbortProof, error) {
	return gatewayRebindNoEffectAbortProof{}, errors.New("generated ingress typed no-effect proof is unavailable")
}

func (d managerGatewayRebindCrossStoreDriver) attestCommittedCurrentLocked(context.Context,
	gatewayCurrentSelection,
) (string, error) {
	return "", errors.New("generated ingress typed current attestation is unavailable")
}
