package generatedingress

import (
	"context"
	"errors"
	"reflect"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GatewayCurrentStartupEmergencyStopResult reports withdrawal evidence only.
// ProtectedRebindHistoryPresent includes retained attempt records and scoped
// current-route bundles. RebindOwnershipPresent means at least one committed
// rebind terminal named an exact protected current-generation bundle.
// OwnershipIndeterminate means protected history could not be enumerated, so
// callers must not fall back to native-only ownership selection. A
// stopped-or-absent target has been freshly proved by exact container ID,
// metadata, ports, and listener state.
type GatewayCurrentStartupEmergencyStopResult struct {
	ProtectedRebindHistoryPresent    bool
	UnresolvedProtectedRebindHistory bool
	RebindOwnershipPresent           bool
	OwnershipIndeterminate           bool
	VerifiedTargets                  int
	StoppedOrAbsentTargets           int
	Incomplete                       bool
}

// StopOwnedGatewayCurrentOnStartupFailure is the no-SQL emergency withdrawal
// path for committed rebind generations. It constructs no serving authority,
// owns the deployment-effects lease and both gateway locks, and may cross an
// already-latched process fail-stop solely to withdraw exact protected-owned
// resources. The caller must not hold worker/startup effects admission.
func StopOwnedGatewayCurrentOnStartupFailure(ctx context.Context, runner runtimeprocess.CommandRunner,
	options Options,
) (GatewayCurrentStartupEmergencyStopResult, error) {
	manager, err := newManager(runner, options)
	if err != nil {
		return GatewayCurrentStartupEmergencyStopResult{}, err
	}
	return manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
}

func (m *Manager) stopOwnedGatewayCurrentOnStartupFailure(ctx context.Context) (
	result GatewayCurrentStartupEmergencyStopResult, resultErr error,
) {
	if m == nil || ctx == nil {
		return GatewayCurrentStartupEmergencyStopResult{}, &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return GatewayCurrentStartupEmergencyStopResult{}, gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRawForOwnedStop(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			m.gatewayRebindFailStopLatch().Store(true)
		}
		return GatewayCurrentStartupEmergencyStopResult{}, err
	}
	defer func() {
		resultErr = m.releaseGatewayCurrentEmergencyLocks(releaseEffects, releaseGateway, resultErr)
		if resultErr != nil && !result.Incomplete {
			result = GatewayCurrentStartupEmergencyStopResult{}
		}
	}()

	stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancelStop()
	targets, census, enumerateErr := m.gatewayCurrentOwnedStopTargetsProtectedPartialLocked()
	result.ProtectedRebindHistoryPresent = census.ProtectedRebindHistory
	result.UnresolvedProtectedRebindHistory = census.UnresolvedAttempt
	result.RebindOwnershipPresent = census.CommittedOwnership
	result.OwnershipIndeterminate = enumerateErr != nil
	result.VerifiedTargets = len(targets)
	if !census.ProtectedRebindHistory && enumerateErr == nil {
		if confirmErr := m.confirmGatewayCurrentEmergencyCensusLocked(targets, census); confirmErr != nil {
			m.gatewayRebindFailStopLatch().Store(true)
			result.Incomplete, result.OwnershipIndeterminate = true, true
			return result, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return result, nil
	}
	// Fully retained abort history names no current rebind ownership and no
	// possible successor effect. Report it explicitly without latching so the
	// caller may make its separate validated native-current withdrawal choice.
	if !census.UnresolvedAttempt && !census.CommittedOwnership && enumerateErr == nil {
		if confirmErr := m.confirmGatewayCurrentEmergencyCensusLocked(targets, census); confirmErr != nil {
			m.gatewayRebindFailStopLatch().Store(true)
			result.Incomplete, result.OwnershipIndeterminate = true, true
			return result, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
		}
		return result, nil
	}
	// Once committed rebind ownership or corrupt rebind history is observed,
	// ordinary admission in this process must never resume after emergency
	// withdrawal, even when every exact target was already absent.
	m.gatewayRebindFailStopLatch().Store(true)
	var stopErrors []error
	for _, target := range targets {
		if err := m.stopGatewayCurrentOwnedTargetLocked(stopCtx, target); err != nil {
			stopErrors = append(stopErrors, err)
			continue
		}
		result.StoppedOrAbsentTargets++
	}
	confirmErr := m.confirmGatewayCurrentEmergencyCensusLocked(targets, census)
	combined := errors.Join(append([]error{enumerateErr}, stopErrors...)...)
	if combined != nil || confirmErr != nil || census.UnresolvedAttempt || !census.CommittedOwnership || len(targets) == 0 ||
		result.StoppedOrAbsentTargets != len(targets) {
		result.OwnershipIndeterminate = result.OwnershipIndeterminate || confirmErr != nil
		result.Incomplete = true
		return result, &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	return result, nil
}

// A per-target stop guard proves only that target. Re-read the complete
// protected census before returning so a new active attempt or orphan current
// bundle cannot cross either the native-fallback absence decision or the
// all-owned-targets-stopped decision.
func (m *Manager) confirmGatewayCurrentEmergencyCensusLocked(targets []gatewayCurrentOwnedStopTarget,
	census gatewayCurrentOwnedStopHistoryCensus,
) error {
	confirmedTargets, confirmedCensus, err := m.gatewayCurrentOwnedStopTargetsProtectedPartialLocked()
	if err != nil || !reflect.DeepEqual(census, confirmedCensus) || !reflect.DeepEqual(targets, confirmedTargets) {
		return errors.New("current emergency protected ownership changed")
	}
	return nil
}

// Emergency withdrawal never owns the cross-store commit barrier. It latches
// every release failure but must not clear a barrier armed by another
// coordinator in the same process.
func (m *Manager) releaseGatewayCurrentEmergencyLocks(releaseEffects, releaseGateway func() error,
	prior error,
) error {
	failed := prior
	if releaseEffects != nil {
		if err := releaseEffects(); err != nil {
			failed = &Error{Code: DiagnosticRouteUnresolved}
			m.gatewayRebindFailStopLatch().Store(true)
		}
	}
	if releaseGateway != nil {
		if err := releaseGateway(); err != nil {
			failed = &Error{Code: DiagnosticRouteUnresolved}
			m.gatewayRebindFailStopLatch().Store(true)
		}
	}
	return failed
}
