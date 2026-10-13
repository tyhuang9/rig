package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type liveGatewayRebindRuntimeCleanupRetirementFailureStage uint8

const (
	liveGatewayRebindRuntimeCleanupRetirementFailureNone liveGatewayRebindRuntimeCleanupRetirementFailureStage = iota
	liveGatewayRebindRuntimeCleanupRetirementFailureInventory
	liveGatewayRebindRuntimeCleanupRetirementFailureProfileListener
	liveGatewayRebindRuntimeCleanupRetirementFailureLocalListener
)

func (s liveGatewayRebindRuntimeCleanupRetirementFailureStage) String() string {
	switch s {
	case liveGatewayRebindRuntimeCleanupRetirementFailureNone:
		return "none"
	case liveGatewayRebindRuntimeCleanupRetirementFailureInventory:
		return "inventory"
	case liveGatewayRebindRuntimeCleanupRetirementFailureProfileListener:
		return "profile_listener"
	case liveGatewayRebindRuntimeCleanupRetirementFailureLocalListener:
		return "local_listener"
	default:
		return "unknown"
	}
}

type liveGatewayRebindRuntimeCleanupAttestationDiagnostic struct {
	wrapped                            bool
	managerRefused                     bool
	driverEntries                      int
	driverFailures                     int
	retirementEntries                  int
	retirementFailures                 int
	runtimeEntries                     int
	runtimeFailures                    int
	predecessorInventoryEntries        int
	predecessorInventoryFailures       int
	predecessorInventoryFirstFailure   int
	retiredProfileListenerEntries      int
	retiredProfileListenerFailures     int
	retiredProfileListenerFirstFailure int
	retiredLocalListenerEntries        int
	retiredLocalListenerFailures       int
	retiredLocalListenerFirstFailure   int
	retirementFirstFailureStage        liveGatewayRebindRuntimeCleanupRetirementFailureStage
}

func (d liveGatewayRebindRuntimeCleanupAttestationDiagnostic) refusal(stable, atTarget bool) error {
	return fmt.Errorf("attest committed serving target: wrapped=%t manager_refused=%t driver_entries=%d driver_failures=%d retirement_entries=%d retirement_failures=%d runtime_entries=%d runtime_failures=%d predecessor_inventory_entries=%d predecessor_inventory_failures=%d predecessor_inventory_first_failure=%d retired_profile_listener_entries=%d retired_profile_listener_failures=%d retired_profile_listener_first_failure=%d retired_local_listener_entries=%d retired_local_listener_failures=%d retired_local_listener_first_failure=%d retirement_first_failure_stage=%s stable=%t at_target=%t",
		d.wrapped, d.managerRefused, d.driverEntries, d.driverFailures, d.retirementEntries,
		d.retirementFailures, d.runtimeEntries, d.runtimeFailures, d.predecessorInventoryEntries,
		d.predecessorInventoryFailures, d.predecessorInventoryFirstFailure, d.retiredProfileListenerEntries,
		d.retiredProfileListenerFailures, d.retiredProfileListenerFirstFailure, d.retiredLocalListenerEntries,
		d.retiredLocalListenerFailures, d.retiredLocalListenerFirstFailure,
		d.retirementFirstFailureStage.String(), stable, atTarget)
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
	diagnostic                            *liveGatewayRebindRuntimeCleanupAttestationDiagnostic
	observeDelegate                       func(context.Context, gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
	predecessorInventoriesStoppedDelegate func(context.Context, gatewayCurrentPredecessorRetirementAction) bool
	retiredProfileListenersSafeDelegate   func(context.Context, gatewayCurrentPredecessorRetirementAction, gatewayProfileBinding, func(context.Context) error) bool
	retiredLocalListenerSafeDelegate      func(context.Context, gatewayCurrentPredecessorRetirementAction, uint16, func(context.Context) error) bool
}

// exactCurrentServing has a concrete managerGatewayCurrentPhysicalRuntime receiver,
// so its nested observe calls bypass this wrapper. runtimeEntries counts only
// observe calls dispatched through liveGatewayRebindRuntimeCleanupDiagnosticRuntime.
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

func (d *liveGatewayRebindRuntimeCleanupDiagnosticRuntime) predecessorInventoriesStopped(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction,
) bool {
	d.diagnostic.predecessorInventoryEntries++
	delegate := d.predecessorInventoriesStoppedDelegate
	if delegate == nil {
		delegate = d.managerGatewayCurrentPhysicalRuntime.predecessorInventoriesStopped
	}
	stopped := delegate(ctx, action)
	if !stopped {
		d.diagnostic.predecessorInventoryFailures++
		if d.diagnostic.predecessorInventoryFirstFailure == 0 {
			d.diagnostic.predecessorInventoryFirstFailure = d.diagnostic.predecessorInventoryEntries
		}
		if d.diagnostic.retirementFirstFailureStage == liveGatewayRebindRuntimeCleanupRetirementFailureNone {
			d.diagnostic.retirementFirstFailureStage = liveGatewayRebindRuntimeCleanupRetirementFailureInventory
		}
	}
	return stopped
}

func (d *liveGatewayRebindRuntimeCleanupDiagnosticRuntime) retiredProfileListenersSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, profile gatewayProfileBinding, guard func(context.Context) error,
) bool {
	d.diagnostic.retiredProfileListenerEntries++
	delegate := d.retiredProfileListenersSafeDelegate
	if delegate == nil {
		delegate = d.managerGatewayCurrentPhysicalRuntime.retiredProfileListenersSafe
	}
	safe := delegate(ctx, action, profile, guard)
	if !safe {
		d.diagnostic.retiredProfileListenerFailures++
		if d.diagnostic.retiredProfileListenerFirstFailure == 0 {
			d.diagnostic.retiredProfileListenerFirstFailure = d.diagnostic.retiredProfileListenerEntries
		}
		if d.diagnostic.retirementFirstFailureStage == liveGatewayRebindRuntimeCleanupRetirementFailureNone {
			d.diagnostic.retirementFirstFailureStage = liveGatewayRebindRuntimeCleanupRetirementFailureProfileListener
		}
	}
	return safe
}

func (d *liveGatewayRebindRuntimeCleanupDiagnosticRuntime) retiredLocalListenerSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, port uint16, guard func(context.Context) error,
) bool {
	d.diagnostic.retiredLocalListenerEntries++
	delegate := d.retiredLocalListenerSafeDelegate
	if delegate == nil {
		delegate = d.managerGatewayCurrentPhysicalRuntime.retiredLocalListenerSafe
	}
	safe := delegate(ctx, action, port, guard)
	if !safe {
		d.diagnostic.retiredLocalListenerFailures++
		if d.diagnostic.retiredLocalListenerFirstFailure == 0 {
			d.diagnostic.retiredLocalListenerFirstFailure = d.diagnostic.retiredLocalListenerEntries
		}
		if d.diagnostic.retirementFirstFailureStage == liveGatewayRebindRuntimeCleanupRetirementFailureNone {
			d.diagnostic.retirementFirstFailureStage = liveGatewayRebindRuntimeCleanupRetirementFailureLocalListener
		}
	}
	return safe
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

	for _, test := range []struct {
		name    string
		stopped bool
	}{
		{name: "predecessor_inventory_pass", stopped: true},
		{name: "predecessor_inventory_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			action := gatewayCurrentPredecessorRetirementAction{CurrentFinalID: "retained-current"}
			argumentsSame := false
			runtime := &liveGatewayRebindRuntimeCleanupDiagnosticRuntime{
				diagnostic: &diagnostic,
				predecessorInventoriesStoppedDelegate: func(gotCtx context.Context,
					gotAction gatewayCurrentPredecessorRetirementAction,
				) bool {
					calls++
					argumentsSame = gotCtx == ctx && reflect.DeepEqual(gotAction, action)
					return test.stopped
				},
			}
			got := runtime.predecessorInventoriesStopped(ctx, action)
			wantFailure := liveGatewayRebindRuntimeCleanupBoolInt(!test.stopped)
			wantStage := liveGatewayRebindRuntimeCleanupRetirementFailureNone
			if !test.stopped {
				wantStage = liveGatewayRebindRuntimeCleanupRetirementFailureInventory
			}
			if calls != 1 || !argumentsSame || got != test.stopped || diagnostic.predecessorInventoryEntries != 1 ||
				diagnostic.predecessorInventoryFailures != wantFailure ||
				diagnostic.predecessorInventoryFirstFailure != wantFailure ||
				diagnostic.retirementFirstFailureStage != wantStage {
				t.Fatalf("predecessor inventory diagnostic changed delegation: calls=%d arguments_same=%t result=%t entries=%d failures=%d first_failure=%d stage=%s",
					calls, argumentsSame, got, diagnostic.predecessorInventoryEntries,
					diagnostic.predecessorInventoryFailures, diagnostic.predecessorInventoryFirstFailure,
					diagnostic.retirementFirstFailureStage)
			}
		})
	}

	for _, test := range []struct {
		name string
		safe bool
	}{
		{name: "retired_profile_listener_pass", safe: true},
		{name: "retired_profile_listener_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls, guardCalls := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}, 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			action := gatewayCurrentPredecessorRetirementAction{CurrentFinalID: "retained-current"}
			profile := gatewayProfileBinding{SelectedIPv4: "192.0.2.10", InterfaceID: "retained-interface", PortStart: 8080, PortEnd: 8080}
			guardResult := errors.New("retained guard result")
			guard := func(gotCtx context.Context) error {
				guardCalls++
				if gotCtx != ctx {
					return errors.New("changed guard context")
				}
				return guardResult
			}
			argumentsSame := false
			runtime := &liveGatewayRebindRuntimeCleanupDiagnosticRuntime{
				diagnostic: &diagnostic,
				retiredProfileListenersSafeDelegate: func(gotCtx context.Context,
					gotAction gatewayCurrentPredecessorRetirementAction, gotProfile gatewayProfileBinding,
					gotGuard func(context.Context) error,
				) bool {
					calls++
					argumentsSame = gotCtx == ctx && reflect.DeepEqual(gotAction, action) &&
						reflect.DeepEqual(gotProfile, profile) && gotGuard(gotCtx) == guardResult
					return test.safe
				},
			}
			got := runtime.retiredProfileListenersSafe(ctx, action, profile, guard)
			wantFailure := liveGatewayRebindRuntimeCleanupBoolInt(!test.safe)
			wantStage := liveGatewayRebindRuntimeCleanupRetirementFailureNone
			if !test.safe {
				wantStage = liveGatewayRebindRuntimeCleanupRetirementFailureProfileListener
			}
			if calls != 1 || guardCalls != 1 || !argumentsSame || got != test.safe ||
				diagnostic.retiredProfileListenerEntries != 1 ||
				diagnostic.retiredProfileListenerFailures != wantFailure ||
				diagnostic.retiredProfileListenerFirstFailure != wantFailure ||
				diagnostic.retirementFirstFailureStage != wantStage {
				t.Fatalf("retired profile listener diagnostic changed delegation: calls=%d guard_calls=%d arguments_same=%t result=%t entries=%d failures=%d first_failure=%d stage=%s",
					calls, guardCalls, argumentsSame, got, diagnostic.retiredProfileListenerEntries,
					diagnostic.retiredProfileListenerFailures, diagnostic.retiredProfileListenerFirstFailure,
					diagnostic.retirementFirstFailureStage)
			}
		})
	}

	for _, test := range []struct {
		name string
		safe bool
	}{
		{name: "retired_local_listener_pass", safe: true},
		{name: "retired_local_listener_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls, guardCalls := liveGatewayRebindRuntimeCleanupAttestationDiagnostic{}, 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			action := gatewayCurrentPredecessorRetirementAction{CurrentFinalID: "retained-current"}
			port := uint16(8080)
			guardResult := errors.New("retained guard result")
			guard := func(gotCtx context.Context) error {
				guardCalls++
				if gotCtx != ctx {
					return errors.New("changed guard context")
				}
				return guardResult
			}
			argumentsSame := false
			runtime := &liveGatewayRebindRuntimeCleanupDiagnosticRuntime{
				diagnostic: &diagnostic,
				retiredLocalListenerSafeDelegate: func(gotCtx context.Context,
					gotAction gatewayCurrentPredecessorRetirementAction, gotPort uint16,
					gotGuard func(context.Context) error,
				) bool {
					calls++
					argumentsSame = gotCtx == ctx && reflect.DeepEqual(gotAction, action) &&
						gotPort == port && gotGuard(gotCtx) == guardResult
					return test.safe
				},
			}
			got := runtime.retiredLocalListenerSafe(ctx, action, port, guard)
			wantFailure := liveGatewayRebindRuntimeCleanupBoolInt(!test.safe)
			wantStage := liveGatewayRebindRuntimeCleanupRetirementFailureNone
			if !test.safe {
				wantStage = liveGatewayRebindRuntimeCleanupRetirementFailureLocalListener
			}
			if calls != 1 || guardCalls != 1 || !argumentsSame || got != test.safe ||
				diagnostic.retiredLocalListenerEntries != 1 ||
				diagnostic.retiredLocalListenerFailures != wantFailure ||
				diagnostic.retiredLocalListenerFirstFailure != wantFailure ||
				diagnostic.retirementFirstFailureStage != wantStage {
				t.Fatalf("retired local listener diagnostic changed delegation: calls=%d guard_calls=%d arguments_same=%t result=%t entries=%d failures=%d first_failure=%d stage=%s",
					calls, guardCalls, argumentsSame, got, diagnostic.retiredLocalListenerEntries,
					diagnostic.retiredLocalListenerFailures, diagnostic.retiredLocalListenerFirstFailure,
					diagnostic.retirementFirstFailureStage)
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
