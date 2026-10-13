package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type liveGatewayRebindRuntimeCleanupAttestationDiagnostic struct {
	wrapped            bool
	managerRefused     bool
	driverEntries      int
	driverFailures     int
	retirementEntries  int
	retirementFailures int
	runtimeEntries     int
	runtimeFailures    int
}

func (d liveGatewayRebindRuntimeCleanupAttestationDiagnostic) refusal(stable, atTarget bool) error {
	return fmt.Errorf("attest committed serving target: wrapped=%t manager_refused=%t driver_entries=%d driver_failures=%d retirement_entries=%d retirement_failures=%d runtime_entries=%d runtime_failures=%d stable=%t at_target=%t",
		d.wrapped, d.managerRefused, d.driverEntries, d.driverFailures, d.retirementEntries,
		d.retirementFailures, d.runtimeEntries, d.runtimeFailures, stable, atTarget)
}

type liveGatewayRebindRuntimeCleanupDiagnosticDriver struct {
	managedGatewayCurrentPhysicalDriver
	diagnostic          *liveGatewayRebindRuntimeCleanupAttestationDiagnostic
	attestationDelegate func(context.Context, gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error)
	retirementDelegate  func(context.Context, gatewayCurrentPredecessorRetirementAction) error
}

func (d *liveGatewayRebindRuntimeCleanupDiagnosticDriver) attestGatewayCurrentPhysical(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	d.diagnostic.driverEntries++
	delegate := d.attestationDelegate
	if delegate == nil {
		delegate = d.managedGatewayCurrentPhysicalDriver.attestGatewayCurrentPhysical
	}
	proof, err := delegate(ctx, selection)
	if err != nil {
		d.diagnostic.driverFailures++
	}
	return proof, err
}

func (d *liveGatewayRebindRuntimeCleanupDiagnosticDriver) observeGatewayCurrentPredecessorsRetired(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction,
) error {
	d.diagnostic.retirementEntries++
	delegate := d.retirementDelegate
	if delegate == nil {
		delegate = d.managedGatewayCurrentPhysicalDriver.observeGatewayCurrentPredecessorsRetired
	}
	err := delegate(ctx, action)
	if err != nil {
		d.diagnostic.retirementFailures++
	}
	return err
}

type liveGatewayRebindRuntimeCleanupDiagnosticRuntime struct {
	managerGatewayCurrentPhysicalRuntime
	diagnostic      *liveGatewayRebindRuntimeCleanupAttestationDiagnostic
	observeDelegate func(context.Context, gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
}

func (d *liveGatewayRebindRuntimeCleanupDiagnosticRuntime) observe(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	d.diagnostic.runtimeEntries++
	delegate := d.observeDelegate
	if delegate == nil {
		delegate = d.managerGatewayCurrentPhysicalRuntime.observe
	}
	proof, err := delegate(ctx, target)
	if err != nil {
		d.diagnostic.runtimeFailures++
	}
	return proof, err
}

var _ gatewayCurrentPhysicalDriver = (*liveGatewayRebindRuntimeCleanupDiagnosticDriver)(nil)
var _ gatewayCurrentPredecessorRetirementDriver = (*liveGatewayRebindRuntimeCleanupDiagnosticDriver)(nil)
var _ gatewayCurrentPhysicalRuntime = (*liveGatewayRebindRuntimeCleanupDiagnosticRuntime)(nil)
var _ gatewayCurrentPredecessorRetirementRuntime = (*liveGatewayRebindRuntimeCleanupDiagnosticRuntime)(nil)

func liveGatewayRebindRuntimeCleanupAttestCurrent(ctx context.Context, manager *Manager,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, liveGatewayRebindRuntimeCleanupAttestationDiagnostic, error) {
	diagnostic := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}
	if manager == nil {
		diagnostic.managerRefused = true
		return gatewayCurrentPhysicalAttestation{}, diagnostic, &Error{Code: DiagnosticRouteUnresolved}
	}
	installed := manager.gatewayCurrentPhysicalDriver
	managed, driverOK := installed.(managedGatewayCurrentPhysicalDriver)
	runtime, runtimeOK := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	if !driverOK || !runtimeOK {
		proof, err := manager.attestGatewayCurrentPhysicalLocked(ctx, selection)
		diagnostic.managerRefused = err != nil
		return proof, diagnostic, err
	}

	diagnostic.wrapped = true
	observedRuntime := &liveGatewayRebindRuntimeCleanupDiagnosticRuntime{
		managerGatewayCurrentPhysicalRuntime: runtime,
		diagnostic:                           &diagnostic,
	}
	managed.runtime = observedRuntime
	observedDriver := &liveGatewayRebindRuntimeCleanupDiagnosticDriver{
		managedGatewayCurrentPhysicalDriver: managed,
		diagnostic:                          &diagnostic,
	}
	manager.gatewayCurrentPhysicalDriver = observedDriver
	defer func() { manager.gatewayCurrentPhysicalDriver = installed }()

	proof, err := manager.attestGatewayCurrentPhysicalLocked(ctx, selection)
	diagnostic.managerRefused = err != nil
	return proof, diagnostic, err
}

func TestLiveGatewayRebindRuntimeCleanupAttestationDiagnosticDelegatesWithoutMutation(t *testing.T) {
	proof := gatewayCurrentPhysicalAttestation{Outcome: gatewayCurrentPhysicalRecoveryMixed, Digest: "retained-proof"}
	failure := errors.New("private diagnostic delegate failure")

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "driver_pass"},
		{name: "driver_failure", err: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}, 0
			driver := &liveGatewayRebindRuntimeCleanupDiagnosticDriver{
				diagnostic: &diagnostic,
				attestationDelegate: func(context.Context, gatewayCurrentSelection) (gatewayCurrentPhysicalAttestation, error) {
					calls++
					return proof, test.err
				},
			}
			got, err := driver.attestGatewayCurrentPhysical(context.Background(), gatewayCurrentSelection{})
			if calls != 1 || !reflect.DeepEqual(got, proof) || err != test.err || diagnostic.driverEntries != 1 ||
				diagnostic.driverFailures != liveGatewayRebindRuntimeCleanupBoolInt(test.err != nil) {
				t.Fatalf("driver diagnostic changed delegation: calls=%d entries=%d failures=%d error_same=%t",
					calls, diagnostic.driverEntries, diagnostic.driverFailures, err == test.err)
			}
		})
	}

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "retirement_pass"},
		{name: "retirement_failure", err: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}, 0
			driver := &liveGatewayRebindRuntimeCleanupDiagnosticDriver{
				diagnostic: &diagnostic,
				retirementDelegate: func(context.Context, gatewayCurrentPredecessorRetirementAction) error {
					calls++
					return test.err
				},
			}
			err := driver.observeGatewayCurrentPredecessorsRetired(context.Background(), gatewayCurrentPredecessorRetirementAction{})
			if calls != 1 || err != test.err || diagnostic.retirementEntries != 1 ||
				diagnostic.retirementFailures != liveGatewayRebindRuntimeCleanupBoolInt(test.err != nil) {
				t.Fatalf("retirement diagnostic changed delegation: calls=%d entries=%d failures=%d error_same=%t",
					calls, diagnostic.retirementEntries, diagnostic.retirementFailures, err == test.err)
			}
		})
	}

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "runtime_pass"},
		{name: "runtime_failure", err: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}, 0
			runtime := &liveGatewayRebindRuntimeCleanupDiagnosticRuntime{
				diagnostic: &diagnostic,
				observeDelegate: func(context.Context, gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error) {
					calls++
					return proof, test.err
				},
			}
			got, err := runtime.observe(context.Background(), gatewayCurrentPhysicalTarget{})
			if calls != 1 || !reflect.DeepEqual(got, proof) || err != test.err || diagnostic.runtimeEntries != 1 ||
				diagnostic.runtimeFailures != liveGatewayRebindRuntimeCleanupBoolInt(test.err != nil) {
				t.Fatalf("runtime diagnostic changed delegation: calls=%d entries=%d failures=%d error_same=%t",
					calls, diagnostic.runtimeEntries, diagnostic.runtimeFailures, err == test.err)
			}
		})
	}

	t.Run("manager_refusal_restores_installed_driver", func(t *testing.T) {
		manager := &Manager{}
		managed := managedGatewayCurrentPhysicalDriver{
			managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: manager},
			runtime:                             managerGatewayCurrentPhysicalRuntime{manager: manager},
		}
		manager.gatewayCurrentPhysicalDriver = managed
		_, diagnostic, err := liveGatewayRebindRuntimeCleanupAttestCurrent(context.Background(), manager,
			gatewayCurrentSelection{})
		_, restored := manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
		if err == nil || !diagnostic.wrapped || !diagnostic.managerRefused || diagnostic.driverEntries != 0 || !restored {
			t.Fatalf("diagnostic installation was not restored: wrapped=%t refused=%t driver_entries=%d restored=%t",
				diagnostic.wrapped, diagnostic.managerRefused, diagnostic.driverEntries, restored)
		}
	})
}

func liveGatewayRebindRuntimeCleanupBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
