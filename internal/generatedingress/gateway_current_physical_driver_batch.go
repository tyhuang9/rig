package generatedingress

import (
	"context"
	"errors"
	"reflect"
)

type gatewayCurrentLANRecoveryRuntime interface {
	observeLANRecovery(context.Context, gatewayCurrentLANRecoveryPhysicalAction,
		gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
	reconcileLANRecovery(context.Context, gatewayCurrentLANRecoveryPhysicalAction,
		gatewayCurrentPhysicalTarget, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

func (d managedGatewayCurrentPhysicalDriver) attestGatewayCurrentLANRecoveryBatch(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	physical, ok := d.runtime.(gatewayCurrentLANRecoveryRuntime)
	if !ok || !validGatewayCurrentLANRecoveryPhysicalAction(action) {
		return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	selection, target, err := d.selectLANRecoveryActionExact(ctx, action)
	if err != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, err
	}
	first, err := physical.observeLANRecovery(ctx, action, target)
	if err != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, err
	}
	if _, _, err := d.selectLANRecoveryActionExact(ctx, action); err != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, err
	}
	second, err := physical.observeLANRecovery(ctx, action, target)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	confirmed, confirmedTarget, err := d.selectLANRecoveryActionExact(ctx, action)
	if err != nil || confirmed.Lineage != selection.Lineage || !reflect.DeepEqual(confirmedTarget, target) {
		return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return gatewayCurrentLANRecoveryPhysicalResultFor(action, second)
}

func (d managedGatewayCurrentPhysicalDriver) withdrawGatewayCurrentLANRecoveryBatch(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	physical, ok := d.runtime.(gatewayCurrentLANRecoveryRuntime)
	if !ok || !validGatewayCurrentLANRecoveryPhysicalAction(action) || action.Complete {
		return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	_, target, err := d.selectLANRecoveryActionExact(ctx, action)
	if err != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, err
	}
	guard := func(effectCtx context.Context) error {
		_, _, guardErr := d.selectLANRecoveryActionExact(effectCtx, action)
		return guardErr
	}
	proof, err := physical.reconcileLANRecovery(ctx, action, target, guard)
	if err != nil {
		proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager())
		defer cancel()
		if _, _, guardErr := d.selectLANRecoveryActionExact(proofCtx, action); guardErr != nil {
			return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		proof, err = physical.observeLANRecovery(proofCtx, action, target)
		if err != nil {
			return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	result, err := gatewayCurrentLANRecoveryPhysicalResultFor(action, proof)
	if err != nil || !result.BatchAbsent {
		return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager())
	defer cancel()
	if _, _, err := d.selectLANRecoveryActionExact(proofCtx, action); err != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return result, nil
}

func (d managedGatewayCurrentPhysicalDriver) selectLANRecoveryActionExact(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentSelection, gatewayCurrentPhysicalTarget, error) {
	if !validGatewayCurrentLANRecoveryPhysicalAction(action) {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, errors.New("invalid current LAN recovery action")
	}
	selection, err := d.selectExact(ctx, action.Selected)
	if err != nil {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, err
	}
	actual, err := d.manager().gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	if err != nil || !reflect.DeepEqual(actual, action) {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, action.Before, nil, action.Selected.LANRecovery)
	if err != nil {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return selection, target, nil
}

func gatewayCurrentLANRecoveryPhysicalResultFor(action gatewayCurrentLANRecoveryPhysicalAction,
	attestation gatewayCurrentPhysicalAttestation,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	absent, ok := gatewayCurrentLANRecoveryObservedProjection(action, attestation.State)
	if !ok {
		return gatewayCurrentLANRecoveryPhysicalResult{}, errors.New("invalid current LAN recovery physical observation")
	}
	result := gatewayCurrentLANRecoveryPhysicalResult{ActionDigest: action.Digest,
		BatchAbsent: absent, Attestation: attestation}
	var err error
	result.Digest, err = gatewayCurrentLANRecoveryPhysicalResultDigest(result)
	if err != nil || !validGatewayCurrentLANRecoveryPhysicalResult(action, result) {
		return gatewayCurrentLANRecoveryPhysicalResult{}, errors.New("invalid current LAN recovery physical result")
	}
	return result, nil
}

var _ gatewayCurrentLANRecoveryPhysicalDriver = managedGatewayCurrentPhysicalDriver{}
