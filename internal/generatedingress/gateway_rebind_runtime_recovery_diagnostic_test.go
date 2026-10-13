package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// liveGatewayRebindRuntimeRecoveryDiagnostic contains only bounded stage
// evidence. It never retains an upstream error, command, resource, or
// authority value.
type liveGatewayRebindRuntimeRecoveryDiagnostic struct {
	Wrapped                    bool
	RecoveryEntries            int
	RetirementEntries          int
	RetirementSuccesses        int
	RetirementFailures         int
	AttestationEntries         int
	AttestationSuccesses       int
	AttestationFailures        int
	AttestationDigestValid     bool
	FirstAttestationCheckpoint gatewayCurrentAttestationCheckpoint
}

func (d liveGatewayRebindRuntimeRecoveryDiagnostic) String() string {
	checkpoint := d.FirstAttestationCheckpoint
	if !checkpoint.valid() {
		checkpoint = gatewayCurrentAttestationCheckpointUnknown
	}
	return fmt.Sprintf("wrapped=%t recovery_entries=%d retirement_entries=%d retirement_successes=%d retirement_failures=%d attestation_entries=%d attestation_successes=%d attestation_failures=%d attestation_digest_valid=%t first_attestation_checkpoint=%s",
		d.Wrapped, d.RecoveryEntries, d.RetirementEntries, d.RetirementSuccesses, d.RetirementFailures,
		d.AttestationEntries, d.AttestationSuccesses, d.AttestationFailures, d.AttestationDigestValid,
		checkpoint.String())
}

func (d liveGatewayRebindRuntimeRecoveryDiagnostic) committedCurrentSucceeded() bool {
	return d.Wrapped && d.RecoveryEntries == 1 &&
		d.RetirementEntries > 0 && d.RetirementSuccesses > 0 && d.RetirementFailures == 0 &&
		d.AttestationEntries > 0 && d.AttestationSuccesses > 0 && d.AttestationFailures == 0 &&
		d.AttestationDigestValid
}

type liveGatewayRebindRuntimeRecoveryCurrentDriver struct {
	managedGatewayCurrentPhysicalDriver
	diagnostic         *liveGatewayRebindRuntimeRecoveryDiagnostic
	retirementDelegate func(context.Context, gatewayCurrentPredecessorRetirementAction, func(context.Context) error) error
}

func (d *liveGatewayRebindRuntimeRecoveryCurrentDriver) retireGatewayCurrentPredecessors(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, guard func(context.Context) error,
) error {
	d.diagnostic.RetirementEntries++
	delegate := d.retirementDelegate
	if delegate == nil {
		delegate = d.managedGatewayCurrentPhysicalDriver.retireGatewayCurrentPredecessors
	}
	err := delegate(ctx, action, guard)
	if err != nil {
		d.diagnostic.RetirementFailures++
	} else {
		d.diagnostic.RetirementSuccesses++
	}
	return err
}

type liveGatewayRebindRuntimeRecoveryCrossStoreDriver struct {
	gatewayRebindCrossStoreDriver
	diagnostic          *liveGatewayRebindRuntimeRecoveryDiagnostic
	attestationDelegate func(context.Context, gatewayCurrentSelection) (string, error)
}

func (d *liveGatewayRebindRuntimeRecoveryCrossStoreDriver) attestCommittedCurrentLocked(ctx context.Context,
	selection gatewayCurrentSelection,
) (string, error) {
	d.diagnostic.AttestationEntries++
	delegate := d.attestationDelegate
	if delegate == nil {
		delegate = d.gatewayRebindCrossStoreDriver.attestCommittedCurrentLocked
	}
	digest, err := delegate(ctx, selection)
	if err != nil {
		d.diagnostic.AttestationFailures++
		checkpoint := gatewayCurrentAttestationCheckpointFromError(err)
		if !d.diagnostic.FirstAttestationCheckpoint.valid() && checkpoint.valid() {
			d.diagnostic.FirstAttestationCheckpoint = checkpoint
		}
	} else {
		d.diagnostic.AttestationSuccesses++
		d.diagnostic.AttestationDigestValid = validSHA256(digest)
	}
	return digest, err
}

var _ gatewayCurrentPhysicalDriver = (*liveGatewayRebindRuntimeRecoveryCurrentDriver)(nil)
var _ gatewayCurrentPredecessorRetirementDriver = (*liveGatewayRebindRuntimeRecoveryCurrentDriver)(nil)
var _ gatewayRebindCrossStoreDriver = (*liveGatewayRebindRuntimeRecoveryCrossStoreDriver)(nil)

type liveGatewayRebindRuntimeRecoveryCall func() (GatewayRebindStartupRecoveryResult, error)

func liveGatewayRebindRuntimeRecoverWithDiagnostic(ctx context.Context, manager *Manager,
	repository GatewayRebindStartupRecoveryRepository,
) (GatewayRebindStartupRecoveryResult, liveGatewayRebindRuntimeRecoveryDiagnostic, error) {
	return liveGatewayRebindRuntimeRecoverCallWithDiagnostic(manager, func() (GatewayRebindStartupRecoveryResult, error) {
		return manager.RecoverGatewayRebindStartup(ctx, repository)
	})
}

func liveGatewayRebindRuntimeRecoverCallWithDiagnostic(manager *Manager,
	call liveGatewayRebindRuntimeRecoveryCall,
) (GatewayRebindStartupRecoveryResult, liveGatewayRebindRuntimeRecoveryDiagnostic, error) {
	diagnostic := liveGatewayRebindRuntimeRecoveryDiagnostic{}
	if manager == nil || call == nil {
		return GatewayRebindStartupRecoveryResult{}, diagnostic, &Error{Code: DiagnosticValidationFailed}
	}
	installedRebind := manager.gatewayRebindCrossStoreDriver
	installedCurrent := manager.gatewayCurrentPhysicalDriver
	managed, currentOK := installedCurrent.(managedGatewayCurrentPhysicalDriver)
	if installedRebind == nil || !currentOK {
		diagnostic.RecoveryEntries++
		result, err := call()
		return result, diagnostic, err
	}

	diagnostic.Wrapped = true
	manager.gatewayRebindCrossStoreDriver = &liveGatewayRebindRuntimeRecoveryCrossStoreDriver{
		gatewayRebindCrossStoreDriver: installedRebind,
		diagnostic:                    &diagnostic,
	}
	manager.gatewayCurrentPhysicalDriver = &liveGatewayRebindRuntimeRecoveryCurrentDriver{
		managedGatewayCurrentPhysicalDriver: managed,
		diagnostic:                          &diagnostic,
	}
	defer func() {
		manager.gatewayRebindCrossStoreDriver = installedRebind
		manager.gatewayCurrentPhysicalDriver = installedCurrent
	}()

	diagnostic.RecoveryEntries++
	result, err := call()
	return result, diagnostic, err
}

func TestGatewayRebindRuntimeRecoveryDiagnosticDelegatesWithoutMutation(t *testing.T) {
	failure := errors.New("private diagnostic delegate failure")
	guard := func(context.Context) error { return nil }

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "retirement_pass"},
		{name: "retirement_failure", err: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls := liveGatewayRebindRuntimeRecoveryDiagnostic{}, 0
			driver := &liveGatewayRebindRuntimeRecoveryCurrentDriver{
				diagnostic: &diagnostic,
				retirementDelegate: func(context.Context, gatewayCurrentPredecessorRetirementAction,
					func(context.Context) error,
				) error {
					calls++
					return test.err
				},
			}
			err := driver.retireGatewayCurrentPredecessors(context.Background(),
				gatewayCurrentPredecessorRetirementAction{}, guard)
			if calls != 1 || err != test.err || diagnostic.RetirementEntries != 1 ||
				diagnostic.RetirementSuccesses != liveGatewayRebindRuntimeRecoveryBoolInt(test.err == nil) ||
				diagnostic.RetirementFailures != liveGatewayRebindRuntimeRecoveryBoolInt(test.err != nil) {
				t.Fatalf("retirement diagnostic changed delegation: calls=%d entries=%d successes=%d failures=%d error_same=%t",
					calls, diagnostic.RetirementEntries, diagnostic.RetirementSuccesses,
					diagnostic.RetirementFailures, err == test.err)
			}
		})
	}

	digest := fmt.Sprintf("%064x", 1)
	for _, test := range []struct {
		name       string
		err        error
		checkpoint gatewayCurrentAttestationCheckpoint
	}{
		{name: "attestation_pass"},
		{name: "attestation_failure", err: newGatewayCurrentAttestationError(context.Background(),
			gatewayCurrentAttestationCheckpointRuntimeServingProbes), checkpoint: gatewayCurrentAttestationCheckpointRuntimeServingProbes},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic, calls := liveGatewayRebindRuntimeRecoveryDiagnostic{}, 0
			driver := &liveGatewayRebindRuntimeRecoveryCrossStoreDriver{
				diagnostic: &diagnostic,
				attestationDelegate: func(context.Context, gatewayCurrentSelection) (string, error) {
					calls++
					return digest, test.err
				},
			}
			got, err := driver.attestCommittedCurrentLocked(context.Background(), gatewayCurrentSelection{})
			if calls != 1 || got != digest || err != test.err || diagnostic.AttestationEntries != 1 ||
				diagnostic.AttestationSuccesses != liveGatewayRebindRuntimeRecoveryBoolInt(test.err == nil) ||
				diagnostic.AttestationFailures != liveGatewayRebindRuntimeRecoveryBoolInt(test.err != nil) ||
				diagnostic.AttestationDigestValid != (test.err == nil) ||
				diagnostic.FirstAttestationCheckpoint != test.checkpoint {
				t.Fatalf("attestation diagnostic changed delegation: calls=%d entries=%d successes=%d failures=%d digest_valid=%t checkpoint=%s error_same=%t",
					calls, diagnostic.AttestationEntries, diagnostic.AttestationSuccesses,
					diagnostic.AttestationFailures, diagnostic.AttestationDigestValid,
					diagnostic.FirstAttestationCheckpoint.String(), err == test.err)
			}
		})
	}

	want := GatewayRebindStartupRecoveryResult{RebindHistoryPresent: true, CurrentStateVersion: 7,
		CurrentAttestationDigest: digest, FenceReleased: true}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "recovery_pass_restores_exact_drivers"},
		{name: "recovery_failure_restores_exact_drivers", err: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{}
			installedRebind := managerGatewayRebindCrossStoreDriver{manager: manager}
			installedCurrent := managedGatewayCurrentPhysicalDriver{
				managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: manager},
			}
			manager.gatewayRebindCrossStoreDriver = installedRebind
			manager.gatewayCurrentPhysicalDriver = installedCurrent
			calls, wrappedDuringCall := 0, false
			got, diagnostic, err := liveGatewayRebindRuntimeRecoverCallWithDiagnostic(manager,
				func() (GatewayRebindStartupRecoveryResult, error) {
					calls++
					_, rebindWrapped := manager.gatewayRebindCrossStoreDriver.(*liveGatewayRebindRuntimeRecoveryCrossStoreDriver)
					_, currentWrapped := manager.gatewayCurrentPhysicalDriver.(*liveGatewayRebindRuntimeRecoveryCurrentDriver)
					wrappedDuringCall = rebindWrapped && currentWrapped
					return want, test.err
				})
			if calls != 1 || !wrappedDuringCall || !reflect.DeepEqual(got, want) || err != test.err ||
				!diagnostic.Wrapped || diagnostic.RecoveryEntries != 1 ||
				!reflect.DeepEqual(manager.gatewayRebindCrossStoreDriver, installedRebind) ||
				!reflect.DeepEqual(manager.gatewayCurrentPhysicalDriver, installedCurrent) {
				t.Fatalf("recovery diagnostic changed delegation or restoration: calls=%d wrapped_during_call=%t diagnostic=%s result_same=%t error_same=%t rebind_restored=%t current_restored=%t",
					calls, wrappedDuringCall, diagnostic, reflect.DeepEqual(got, want), err == test.err,
					reflect.DeepEqual(manager.gatewayRebindCrossStoreDriver, installedRebind),
					reflect.DeepEqual(manager.gatewayCurrentPhysicalDriver, installedCurrent))
			}
		})
	}
}

func liveGatewayRebindRuntimeRecoveryBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
