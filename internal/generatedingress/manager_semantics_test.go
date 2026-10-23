package generatedingress

import (
	"context"

	"github.com/hostd/hostd/internal/generatedroute"
)

// exerciseManagerSwitch preserves unit coverage of the private physical route
// state machine after ordinary Manager.Switch was closed. Sealed domain route
// execution remains the separately tested production caller.
func exerciseManagerSwitch(manager *Manager, ctx context.Context, request generatedroute.RouteSwitchRequest) error {
	return manager.switchGeneratedDomainRoute(ctx, request)
}

// exerciseGatewayCurrentRecoveryCore unit-tests the canonical current-state
// recovery core under the gateway lock it requires. It deliberately has no
// fallback to another format: startup owns disposition selection and ordinary
// Manager recovery is closed.
func exerciseGatewayCurrentRecoveryCore(manager *Manager, ctx context.Context) (resultErr error) {
	if manager == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := manager.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	handled, err := manager.recoverGatewayCurrentStateMachineLocked(ctx)
	if err != nil {
		return err
	}
	if !handled {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}

// exerciseCommittedV2RecoveryCore unit-tests only the canonical committed-v2
// recovery core selected by its exact protected state. It does not recreate
// the removed generic Recover/Provision dispatcher.
func exerciseCommittedV2RecoveryCore(manager *Manager, ctx context.Context) (resultErr error) {
	if manager == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := manager.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	store, state, journal, committed, err := manager.committedV2Locked(ctx)
	if err != nil {
		return err
	}
	if !committed {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return manager.recoverCommittedV2Locked(ctx, store, state, journal)
}

// exerciseLegacyV1FenceCore unit-tests the exact legacy-format fence used by
// the private route primitive. Provisioning effects are exercised through the
// production primitives that own them rather than a copied startup wrapper.
func exerciseLegacyV1FenceCore(manager *Manager, ctx context.Context) (resultErr error) {
	if manager == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := manager.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	return manager.fenceLegacyV1Locked()
}

// exerciseLegacyV1EnsureGatewayCore unit-tests ensureCaddy with the exact
// committed legacy route set under its production lock and format fence. The
// helper intentionally excludes the removed Provision dispatcher and its
// duplicated post-ensure mutations.
func exerciseLegacyV1EnsureGatewayCore(manager *Manager, ctx context.Context) (resultErr error) {
	if manager == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	release, err := manager.lockGateway(ctx)
	if err != nil {
		return err
	}
	defer releaseGatewayLock(release, &resultErr)
	if err := manager.fenceLegacyV1Locked(); err != nil {
		return err
	}
	state, err := manager.store.load()
	if err != nil {
		return &Error{Code: DiagnosticRouteStateFailed}
	}
	return manager.ensureCaddy(ctx, state.Active)
}
