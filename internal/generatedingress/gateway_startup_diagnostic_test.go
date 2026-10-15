package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type gatewayStartupDiagnosticForgedAsError struct {
	target *Error
}

func (e gatewayStartupDiagnosticForgedAsError) Error() string { return "forged startup diagnostic" }

func (e gatewayStartupDiagnosticForgedAsError) As(target any) bool {
	value, ok := target.(**Error)
	if !ok {
		return false
	}
	*value = e.target
	return true
}

func TestGatewayStartupDiagnosticCheckpointContract(t *testing.T) {
	want := []string{
		"upgrade_claims_validation",
		"upgrade_gateway_lock",
		"current_selection_precondition",
		"current_sql_first_read",
		"current_protected_selection",
		"current_absence_protected_presence",
		"current_sql_stability",
		"current_protected_stability",
		"upgrade_history",
		"upgrade_source_state",
		"upgrade_current_binding",
		"upgrade_claim_history_binding",
		"upgrade_pending_proof",
		"upgrade_lan_batch_proof",
		"upgrade_committed_topology",
		"upgrade_committed_host_routes",
		"upgrade_committed_selected_interface",
		"upgrade_terminal_receipt_proof",
		"upgrade_no_artifact_proof",
		"upgrade_disposition",
		"upgrade_source_stability",
		"upgrade_history_stability",
		"upgrade_current_stability",
		"upgrade_gateway_unlock",
		"lan_claims_validation",
		"lan_gateway_lock",
		"lan_current_gateway_selection",
		"lan_committed_state",
		"lan_startup_authority",
		"lan_protected_claim_binding",
		"lan_pending_state_proof",
		"lan_committed_topology",
		"lan_committed_publications",
		"lan_orphan_claim_recovery",
		"lan_disable_binding",
		"lan_disable_absence_proof",
		"lan_selected_interface",
		"lan_protected_state_stability",
		"lan_authority_stability",
		"lan_recovery_candidate_selection",
		"lan_gateway_unlock",
	}
	first := gatewayStartupDiagnosticCheckpointUpgradeClaimsValidation
	last := gatewayStartupDiagnosticCheckpointLANGatewayUnlock
	if int(last-first+1) != len(want) {
		t.Fatalf("checkpoint range=%d names=%d", last-first+1, len(want))
	}
	seen := make(map[string]struct{}, len(want))
	for offset, name := range want {
		checkpoint := first + gatewayStartupDiagnosticCheckpoint(offset)
		if !checkpoint.valid() || checkpoint.String() != name {
			t.Fatalf("checkpoint %d=%q, want %q", checkpoint, checkpoint.String(), name)
		}
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate checkpoint name %q", name)
		}
		seen[name] = struct{}{}
	}
	if gatewayStartupDiagnosticCheckpointUnknown.valid() ||
		gatewayStartupDiagnosticCheckpoint(255).valid() ||
		gatewayStartupDiagnosticCheckpointUnknown.String() != "unknown" {
		t.Fatal("unknown checkpoint became trusted")
	}

	for _, test := range []struct {
		name       string
		ctx        context.Context
		checkpoint gatewayStartupDiagnosticCheckpoint
		code       DiagnosticCode
	}{
		{
			name:       "known checkpoint",
			ctx:        context.Background(),
			checkpoint: gatewayStartupDiagnosticCheckpointUpgradeCommittedTopology,
			code:       DiagnosticRouteUnresolved,
		},
		{
			name: "cancelled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			checkpoint: gatewayStartupDiagnosticCheckpointLANAuthorityStability,
			code:       DiagnosticCancelled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := newGatewayStartupDiagnosticError(test.ctx, test.checkpoint)
			if err.Code != test.code || !IsCode(err, test.code) ||
				GatewayStartupDiagnosticCheckpoint(err) != test.checkpoint.String() ||
				err.Error() != "generated ingress: "+string(test.code) || err.CandidateMayBeLive() ||
				errors.Unwrap(err) != nil {
				t.Fatalf("diagnostic contract changed: code=%q checkpoint=%q may_live=%t unwrap=%v",
					err.Code, GatewayStartupDiagnosticCheckpoint(err), err.CandidateMayBeLive(), errors.Unwrap(err))
			}
			encoded, marshalErr := json.Marshal(err)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var object map[string]json.RawMessage
			if unmarshalErr := json.Unmarshal(encoded, &object); unmarshalErr != nil || len(object) != 1 || object["Code"] == nil ||
				strings.Contains(string(encoded), "checkpoint") || strings.Contains(string(encoded), test.checkpoint.String()) {
				t.Fatalf("diagnostic JSON exposed private checkpoint: json=%q err=%v", encoded, unmarshalErr)
			}
		})
	}
}

func TestGatewayStartupDiagnosticCheckpointRejectsUnknownWrappedAndForgedErrors(t *testing.T) {
	const marker = "untrusted-startup-cause"
	known := newGatewayStartupDiagnosticError(
		context.Background(), gatewayStartupDiagnosticCheckpointUpgradeCommittedHostRoutes)
	var typedNil *Error
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "nil", err: nil},
		{name: "typed nil", err: typedNil},
		{name: "ordinary", err: errors.New(marker)},
		{name: "wrapped known", err: fmt.Errorf("%s: %w", marker, known)},
		{name: "custom As", err: gatewayStartupDiagnosticForgedAsError{target: known}},
		{name: "forged text", err: errors.New(known.Error())},
		{name: "forged public Error", err: &Error{Code: DiagnosticRouteUnresolved}},
		{name: "invalid private value", err: &Error{Code: DiagnosticRouteUnresolved, startupCheckpoint: 255}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := GatewayStartupDiagnosticCheckpoint(test.err); got != "unknown" {
				t.Fatalf("untrusted diagnostic accepted: %q", got)
			}
		})
	}

	direct := &Error{
		Code:                         DiagnosticRouteReloadFailed,
		candidateMayBeLive:           true,
		currentAttestationCheckpoint: gatewayCurrentAttestationCheckpointDriverStability,
		startupCheckpoint:            gatewayStartupDiagnosticCheckpointLANCommittedPublications,
	}
	retained := gatewayStartupDiagnosticErrorFrom(
		context.Background(), direct, gatewayStartupDiagnosticCheckpointLANCommittedTopology)
	if retained == direct || retained.Code != direct.Code || !retained.CandidateMayBeLive() ||
		retained.currentAttestationCheckpoint != direct.currentAttestationCheckpoint ||
		GatewayStartupDiagnosticCheckpoint(retained) != "lan_committed_publications" || errors.Unwrap(retained) != nil {
		t.Fatalf("direct finite error state was not copied safely: %+v", retained)
	}

	for _, cause := range []error{errors.New(marker), fmt.Errorf("wrapper: %w", known)} {
		result := gatewayStartupDiagnosticErrorFrom(
			context.Background(), cause, gatewayStartupDiagnosticCheckpointLANCommittedTopology)
		encoded, err := json.Marshal(result)
		if err != nil || result.Code != DiagnosticRouteUnresolved || result.CandidateMayBeLive() ||
			GatewayStartupDiagnosticCheckpoint(result) != "lan_committed_topology" || errors.Unwrap(result) != nil ||
			errors.Is(result, cause) || strings.Contains(result.Error(), marker) || strings.Contains(string(encoded), marker) ||
			strings.Contains(string(encoded), "checkpoint") {
			t.Fatalf("untrusted cause was not sanitized: result=%v json=%q err=%v", result, encoded, err)
		}
	}

	invalid := gatewayStartupDiagnosticErrorFrom(context.Background(), errors.New(marker), 255)
	if GatewayStartupDiagnosticCheckpoint(invalid) != "unknown" {
		t.Fatalf("invalid fallback was not clamped: %q", GatewayStartupDiagnosticCheckpoint(invalid))
	}
	ordinary := errors.New(marker)
	if got := gatewayStartupDiagnosticErrorWithCheckpoint(
		ordinary, gatewayStartupDiagnosticCheckpointUpgradeGatewayLock); got != ordinary {
		t.Fatal("unknown direct dependency error was replaced")
	}
}
