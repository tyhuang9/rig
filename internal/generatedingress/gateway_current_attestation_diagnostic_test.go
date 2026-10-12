package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentAttestationDiagnosticErrorContract(t *testing.T) {
	for _, test := range []struct {
		name       string
		ctx        context.Context
		checkpoint gatewayCurrentAttestationCheckpoint
		code       DiagnosticCode
	}{
		{
			name:       "known checkpoint",
			ctx:        context.Background(),
			checkpoint: gatewayCurrentAttestationCheckpointDriverStability,
			code:       DiagnosticRouteUnresolved,
		},
		{
			name: "cancelled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			checkpoint: gatewayCurrentAttestationCheckpointManagerDriver,
			code:       DiagnosticCancelled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := newGatewayCurrentAttestationError(test.ctx, test.checkpoint)
			if err.Code != test.code ||
				gatewayCurrentAttestationCheckpointFromError(err) != test.checkpoint ||
				err.Error() != "generated ingress: "+string(test.code) ||
				err.CandidateMayBeLive() ||
				errors.Unwrap(err) != nil {
				t.Fatalf("diagnostic contract changed: code=%q checkpoint=%q may_live=%t unwrap=%v",
					err.Code, gatewayCurrentAttestationCheckpointFromError(err).String(), err.CandidateMayBeLive(), errors.Unwrap(err))
			}
			var typed *Error
			if !errors.As(err, &typed) || typed != err || !errors.Is(err, err) {
				t.Fatal("diagnostic error no longer retains direct Error compatibility")
			}
			encoded, marshalErr := json.Marshal(err)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil || len(object) != 1 || object["Code"] == nil ||
				strings.Contains(string(encoded), "checkpoint") || strings.Contains(string(encoded), test.checkpoint.String()) {
				t.Fatalf("diagnostic JSON exposed private checkpoint: json=%q err=%v", encoded, err)
			}
		})
	}
}

func TestGatewayCurrentAttestationDiagnosticErrorSanitizesUnknownAndWrappedCauses(t *testing.T) {
	const marker = "untrusted-current-attestation-cause"
	known := newGatewayCurrentAttestationError(context.Background(), gatewayCurrentAttestationCheckpointDriverFirstObservation)
	for _, test := range []struct {
		name     string
		cause    error
		fallback gatewayCurrentAttestationCheckpoint
		want     gatewayCurrentAttestationCheckpoint
	}{
		{
			name:     "direct error retains finite checkpoint",
			cause:    known,
			fallback: gatewayCurrentAttestationCheckpointManagerDriver,
			want:     gatewayCurrentAttestationCheckpointDriverFirstObservation,
		},
		{
			name:     "wrapped error uses fallback",
			cause:    fmt.Errorf("wrapper: %w", known),
			fallback: gatewayCurrentAttestationCheckpointManagerDriver,
			want:     gatewayCurrentAttestationCheckpointManagerDriver,
		},
		{
			name:     "custom error uses fallback",
			cause:    errors.New(marker),
			fallback: gatewayCurrentAttestationCheckpointDriverStability,
			want:     gatewayCurrentAttestationCheckpointDriverStability,
		},
		{
			name:     "invalid fallback clamps unknown",
			cause:    errors.New(marker),
			fallback: gatewayCurrentAttestationCheckpoint(255),
			want:     gatewayCurrentAttestationCheckpointUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := gatewayCurrentAttestationErrorFrom(context.Background(), test.cause, test.fallback)
			if gatewayCurrentAttestationCheckpointFromError(result) != test.want ||
				result.Code != DiagnosticRouteUnresolved ||
				result.CandidateMayBeLive() ||
				errors.Unwrap(result) != nil ||
				errors.Is(result, test.cause) {
				t.Fatalf("diagnostic cause was not sanitized: checkpoint=%q code=%q may_live=%t unwrap=%v",
					gatewayCurrentAttestationCheckpointFromError(result).String(), result.Code, result.CandidateMayBeLive(),
					errors.Unwrap(result))
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(result.Error(), marker) || strings.Contains(string(encoded), marker) ||
				strings.Contains(string(encoded), "checkpoint") {
				t.Fatalf("diagnostic serialization leaked cause: json=%q err=%v", encoded, err)
			}
		})
	}
}

func TestGatewayCurrentAttestationDiagnosticLeavesCollapseWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name             string
		wantCheckpoint   gatewayCurrentAttestationCheckpoint
		wantObservations int
		wantRunning      bool
		configure        func(*gatewayRebindCompletedRestoreRepository, *gatewayRebindCurrentObservationRuntime, *gatewayCurrentPhysicalExecutor)
	}{
		{
			name:           "manager serving census guard",
			wantCheckpoint: gatewayCurrentAttestationCheckpointManagerServingCensus,
			wantRunning:    true,
			configure: func(repository *gatewayRebindCompletedRestoreRepository, _ *gatewayRebindCurrentObservationRuntime,
				_ *gatewayCurrentPhysicalExecutor,
			) {
				repository.alter = func(snapshot *appaccess.HostingGatewayStartupSnapshot) {
					snapshot.RuntimeComponents = nil
				}
			},
		},
		{
			name:             "driver first proof rejects stopped runtime",
			wantCheckpoint:   gatewayCurrentAttestationCheckpointDriverFirstProof,
			wantObservations: 1,
			wantRunning:      false,
			configure: func(_ *gatewayRebindCompletedRestoreRepository, _ *gatewayRebindCurrentObservationRuntime,
				runner *gatewayCurrentPhysicalExecutor,
			) {
				runner.stopAt(mustGatewayCurrentPhysicalConfig(t, runner.target.State))
			},
		},
		{
			name:             "driver stability",
			wantCheckpoint:   gatewayCurrentAttestationCheckpointDriverStability,
			wantObservations: 2,
			wantRunning:      true,
			configure: func(_ *gatewayRebindCompletedRestoreRepository, observations *gatewayRebindCurrentObservationRuntime,
				_ *gatewayCurrentPhysicalExecutor,
			) {
				observations.after = func(call int, proof *gatewayCurrentPhysicalAttestation) {
					if call != 2 {
						return
					}
					proof.Runtime.ListenerDigest = strings.Repeat("a", 64)
					proof.Runtime.Digest, _ = gatewayCurrentRuntimeProofDigest(proof.Runtime)
					proof.Digest, _ = gatewayCurrentPhysicalAttestationDigest(*proof)
					if !validGatewayCurrentPhysicalAttestation(*proof) {
						t.Fatal("invalid distinct second physical proof")
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, selection, _ := gatewayRebindCommittedServingFixture(t)
			ctx := context.Background()
			target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			beforeSQL, err := fixture.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeHistory, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			repository := &gatewayRebindCompletedRestoreRepository{Repository: fixture.repository}
			fixture.manager.options.RebindCurrentStateRepository = repository
			runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State,
				fixture.journal.Source.LocalHostPort)
			physical := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: fixture.manager},
				runner, fixture.journal.Source.LocalHostPort)
			observations := &gatewayRebindCurrentObservationRuntime{gatewayCurrentPhysicalRuntime: physical.runtime}
			physical.runtime = observations
			fixture.manager.gatewayCurrentPhysicalDriver = physical
			test.configure(repository, observations, runner)

			digest, err := fixture.manager.attestGatewayRebindCurrentLocked(ctx, selection)
			var diagnostic *Error
			if err == nil || digest != "" || !errors.As(err, &diagnostic) ||
				gatewayCurrentAttestationCheckpointFromError(diagnostic) != test.wantCheckpoint ||
				observations.calls != test.wantObservations || len(runner.effects) != 0 ||
				runner.container.Running != test.wantRunning ||
				fixture.manager.gatewayRebindFailStopLatch().Load() || fixture.manager.gatewayRebindCommitBarrierLatch().Load() {
				t.Fatalf("attestation leaf did not collapse safely: checkpoint=%q observations=%d effects=%d running=%t fail_stop=%t barrier=%t",
					gatewayCurrentAttestationCheckpointFromError(diagnostic).String(), observations.calls, len(runner.effects),
					runner.container.Running, fixture.manager.gatewayRebindFailStopLatch().Load(),
					fixture.manager.gatewayRebindCommitBarrierLatch().Load())
			}
			afterSQL, sqlErr := fixture.repository.HostingGatewayStartupSnapshot(ctx)
			afterHistory, historyErr := readGatewayHistorySnapshotMode(fixture.manager.store, true)
			if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(beforeSQL, afterSQL) ||
				!sameGatewayHistorySnapshot(beforeHistory, afterHistory) ||
				fixture.repository.CheckGatewayRebindFence(ctx) == nil {
				t.Fatalf("attestation failure mutated durable authority: sql=%v history=%v", sqlErr, historyErr)
			}
		})
	}
}
