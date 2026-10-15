package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// gatewayStartupDiagnosticExpectedCheckpoints is intentionally independent of
// the production switch. It makes an added or removed startup checkpoint a
// reviewable contract change instead of silently broadening stderr parsing.
var gatewayStartupDiagnosticExpectedCheckpoints = []gatewayStartupDiagnosticCheckpoint{
	gatewayStartupDiagnosticStartupDependencies,
	gatewayStartupDiagnosticNormalRebindDecision,
	gatewayStartupDiagnosticPostPrepareFence,
	gatewayStartupDiagnosticNativeRebindPresenceRecheck,
	gatewayStartupDiagnosticPreparedRuntimePrecondition,
	gatewayStartupDiagnosticPreparedCurrentRecheck,
	gatewayStartupDiagnosticOrdinaryFence,
	gatewayStartupDiagnosticStartupSnapshot,
	gatewayStartupDiagnosticStartupSnapshotBegin,
	gatewayStartupDiagnosticStartupSnapshotUpgrade,
	gatewayStartupDiagnosticStartupSnapshotGrants,
	gatewayStartupDiagnosticStartupSnapshotDisables,
	gatewayStartupDiagnosticStartupSnapshotRebind,
	gatewayStartupDiagnosticStartupSnapshotRuntimeHeads,
	gatewayStartupDiagnosticStartupSnapshotRuntimeComponents,
	gatewayStartupDiagnosticStartupSnapshotApprovalAuthority,
	gatewayStartupDiagnosticStartupSnapshotCommit,
	gatewayStartupDiagnosticDisabledRuntimeHistory,
	gatewayStartupDiagnosticStartupManager,
	gatewayStartupDiagnosticUpgradeInspection,
	gatewayStartupDiagnosticCurrentAuthoritySnapshotRecheck,
	gatewayStartupDiagnosticV1WithLANHistory,
	gatewayStartupDiagnosticUpgradeRecoveryWithLANHistory,
	gatewayStartupDiagnosticStartupDisposition,
	gatewayStartupDiagnosticLANRecoveryBatchPresence,
	gatewayStartupDiagnosticHistoricalDisableSuccessor,
	gatewayStartupDiagnosticLANAccessInspection,
	gatewayStartupDiagnosticLANRecoverySelection,
	gatewayStartupDiagnosticUpgradeClaimsValidation,
	gatewayStartupDiagnosticUpgradeGatewayLock,
	gatewayStartupDiagnosticCurrentSelectionPrecondition,
	gatewayStartupDiagnosticCurrentSQLFirstRead,
	gatewayStartupDiagnosticCurrentProtectedSelection,
	gatewayStartupDiagnosticCurrentAbsenceProtectedPresence,
	gatewayStartupDiagnosticCurrentSQLStability,
	gatewayStartupDiagnosticCurrentProtectedStability,
	gatewayStartupDiagnosticUpgradeHistory,
	gatewayStartupDiagnosticUpgradeSourceState,
	gatewayStartupDiagnosticUpgradeCurrentBinding,
	gatewayStartupDiagnosticUpgradeClaimHistoryBinding,
	gatewayStartupDiagnosticUpgradePendingProof,
	gatewayStartupDiagnosticUpgradeLANBatchProof,
	gatewayStartupDiagnosticUpgradeCommittedTopology,
	gatewayStartupDiagnosticUpgradeCommittedHostRoutes,
	gatewayStartupDiagnosticUpgradeCommittedSelectedInterface,
	gatewayStartupDiagnosticUpgradeTerminalReceiptProof,
	gatewayStartupDiagnosticUpgradeNoArtifactProof,
	gatewayStartupDiagnosticUpgradeDisposition,
	gatewayStartupDiagnosticUpgradeSourceStability,
	gatewayStartupDiagnosticUpgradeHistoryStability,
	gatewayStartupDiagnosticUpgradeCurrentStability,
	gatewayStartupDiagnosticUpgradeGatewayUnlock,
	gatewayStartupDiagnosticLANClaimsValidation,
	gatewayStartupDiagnosticLANGatewayLock,
	gatewayStartupDiagnosticLANCurrentGatewaySelection,
	gatewayStartupDiagnosticLANCommittedState,
	gatewayStartupDiagnosticLANStartupAuthority,
	gatewayStartupDiagnosticLANProtectedClaimBinding,
	gatewayStartupDiagnosticLANPendingStateProof,
	gatewayStartupDiagnosticLANCommittedTopology,
	gatewayStartupDiagnosticLANCommittedPublications,
	gatewayStartupDiagnosticLANOrphanClaimRecovery,
	gatewayStartupDiagnosticLANDisableBinding,
	gatewayStartupDiagnosticLANDisableAbsenceProof,
	gatewayStartupDiagnosticLANSelectedInterface,
	gatewayStartupDiagnosticLANProtectedStateStability,
	gatewayStartupDiagnosticLANAuthorityStability,
	gatewayStartupDiagnosticLANRecoveryCandidateSelection,
	gatewayStartupDiagnosticLANGatewayUnlock,
}

var gatewayStartupDiagnosticExpectedOuterStages = []gatewayStartupDiagnosticCheckpoint{
	gatewayStartupDiagnosticStartupDependencies,
	gatewayStartupDiagnosticNormalRebindDecision,
	gatewayStartupDiagnosticPostPrepareFence,
	gatewayStartupDiagnosticNativeRebindPresenceRecheck,
	gatewayStartupDiagnosticPreparedRuntimePrecondition,
	gatewayStartupDiagnosticPreparedCurrentRecheck,
	gatewayStartupDiagnosticOrdinaryFence,
	gatewayStartupDiagnosticStartupSnapshot,
	gatewayStartupDiagnosticDisabledRuntimeHistory,
	gatewayStartupDiagnosticStartupManager,
	gatewayStartupDiagnosticUpgradeInspection,
	gatewayStartupDiagnosticCurrentAuthoritySnapshotRecheck,
	gatewayStartupDiagnosticV1WithLANHistory,
	gatewayStartupDiagnosticUpgradeRecoveryWithLANHistory,
	gatewayStartupDiagnosticStartupDisposition,
	gatewayStartupDiagnosticLANRecoveryBatchPresence,
	gatewayStartupDiagnosticHistoricalDisableSuccessor,
	gatewayStartupDiagnosticLANAccessInspection,
	gatewayStartupDiagnosticLANRecoverySelection,
}

var gatewayStartupDiagnosticExpectedSnapshotStages = []struct {
	stage      string
	checkpoint gatewayStartupDiagnosticCheckpoint
}{
	{stage: "begin", checkpoint: gatewayStartupDiagnosticStartupSnapshotBegin},
	{stage: "upgrade", checkpoint: gatewayStartupDiagnosticStartupSnapshotUpgrade},
	{stage: "grants", checkpoint: gatewayStartupDiagnosticStartupSnapshotGrants},
	{stage: "disables", checkpoint: gatewayStartupDiagnosticStartupSnapshotDisables},
	{stage: "rebind", checkpoint: gatewayStartupDiagnosticStartupSnapshotRebind},
	{stage: "runtime_heads", checkpoint: gatewayStartupDiagnosticStartupSnapshotRuntimeHeads},
	{stage: "runtime_components", checkpoint: gatewayStartupDiagnosticStartupSnapshotRuntimeComponents},
	{stage: "active_approval_authority", checkpoint: gatewayStartupDiagnosticStartupSnapshotApprovalAuthority},
	{stage: "commit", checkpoint: gatewayStartupDiagnosticStartupSnapshotCommit},
}

var gatewayStartupDiagnosticExpectedSnapshotCauses = []gatewayStartupDiagnosticCause{
	gatewayStartupDiagnosticCauseUnknown,
	gatewayStartupDiagnosticCauseInvalidStoredState,
	gatewayStartupDiagnosticCauseCanceled,
	gatewayStartupDiagnosticCauseDeadlineExceeded,
	gatewayStartupDiagnosticCauseSQLiteBusy,
	gatewayStartupDiagnosticCauseSQLiteLocked,
	gatewayStartupDiagnosticCauseSQLiteError,
	gatewayStartupDiagnosticCauseSQLiteCorrupt,
	gatewayStartupDiagnosticCauseSQLiteIO,
	gatewayStartupDiagnosticCauseSQLiteOther,
}

func TestGatewayStartupDiagnosticClosedCheckpointContract(t *testing.T) {
	seen := make(map[gatewayStartupDiagnosticCheckpoint]struct{}, len(gatewayStartupDiagnosticExpectedCheckpoints))
	for _, expected := range gatewayStartupDiagnosticExpectedCheckpoints {
		if _, duplicate := seen[expected]; duplicate {
			t.Fatalf("duplicate expected checkpoint %q", expected)
		}
		seen[expected] = struct{}{}
		got, ok := gatewayStartupDiagnosticCheckpointFromString(string(expected))
		if !ok || got != expected {
			t.Fatalf("checkpoint %q parsed as %q ok=%t", expected, got, ok)
		}
	}
	for _, forged := range []string{"", "unknown", "upgrade_gateway_lock_extra", "UPGRADE_GATEWAY_LOCK", "lan_gateway_unlock\nsecret"} {
		if got, ok := gatewayStartupDiagnosticCheckpointFromString(forged); ok || got != "" {
			t.Fatalf("forged checkpoint %q parsed as %q", forged, got)
		}
	}
}

func TestGatewayStartupDiagnosticSnapshotStageAndCauseContract(t *testing.T) {
	for _, expected := range gatewayStartupDiagnosticExpectedSnapshotStages {
		t.Run(expected.stage, func(t *testing.T) {
			got, ok := gatewayStartupDiagnosticSnapshotCheckpointForStage(expected.stage)
			if !ok || got != expected.checkpoint || !gatewayStartupDiagnosticSnapshotCheckpoint(got) {
				t.Fatalf("stage=%q checkpoint=%q ok=%t", expected.stage, got, ok)
			}
		})
	}
	if got, ok := gatewayStartupDiagnosticSnapshotCheckpointForStage("forged"); ok ||
		got != gatewayStartupDiagnosticStartupSnapshot || gatewayStartupDiagnosticSnapshotCheckpoint(got) {
		t.Fatalf("forged stage mapped to checkpoint=%q ok=%t", got, ok)
	}
	for _, expected := range gatewayStartupDiagnosticExpectedSnapshotCauses {
		got, ok := gatewayStartupDiagnosticCauseFromString(string(expected))
		if !ok || got != expected {
			t.Fatalf("cause=%q parsed as %q ok=%t", expected, got, ok)
		}
	}
	for _, forged := range []string{"", "sqlite_busy_extra", "SQLITE_BUSY", "sqlite_other\nmarker"} {
		if got, ok := gatewayStartupDiagnosticCauseFromString(forged); ok || got != gatewayStartupDiagnosticCauseUnknown {
			t.Fatalf("forged cause %q parsed as %q", forged, got)
		}
	}
}

func TestGatewayStartupDiagnosticOuterStageWrappingPreservesCause(t *testing.T) {
	for _, stage := range gatewayStartupDiagnosticExpectedOuterStages {
		t.Run(string(stage), func(t *testing.T) {
			cause := fmt.Errorf("private marker for %s", stage)
			wrapped := gatewayStartupDiagnosticWrap(stage, cause)
			if got := gatewayStartupDiagnosticCheckpointForError(wrapped); got != string(stage) {
				t.Fatalf("checkpoint=%q want %q", got, stage)
			}
			if !errors.Is(wrapped, cause) {
				t.Fatalf("wrapper no longer unwraps %v", cause)
			}
			var typed *gatewayStartupDiagnosticError
			if !errors.As(wrapped, &typed) || typed == nil {
				t.Fatalf("wrapper does not support errors.As: %v", wrapped)
			}
			if got := wrapped.Error(); got != "gateway startup diagnostic failed" || strings.Contains(got, "private marker") {
				t.Fatalf("wrapper exposed cause in Error: %q", got)
			}
		})
	}
}

func TestGatewayStartupDiagnosticRejectsForgedOrWrappedIngressMetadata(t *testing.T) {
	const marker = "private fixture marker"
	direct := &generatedingress.Error{Code: generatedingress.DiagnosticRouteUnresolved}
	wrappedIngress := fmt.Errorf("%s: %w", marker, direct)

	for _, test := range []struct {
		name       string
		checkpoint gatewayStartupDiagnosticCheckpoint
		cause      error
		ingressErr error
		want       string
	}{
		{
			name: "direct ingress without a trusted checkpoint keeps outer stage", checkpoint: gatewayStartupDiagnosticUpgradeInspection,
			cause: fmt.Errorf("inspect gateway: %w", direct), ingressErr: direct, want: "upgrade_inspection",
		},
		{
			name: "wrapped ingress error cannot manufacture a checkpoint", checkpoint: gatewayStartupDiagnosticLANAccessInspection,
			cause: fmt.Errorf("inspect LAN: %w", wrappedIngress), ingressErr: wrappedIngress, want: "lan_access_inspection",
		},
		{
			name: "custom error cannot manufacture a checkpoint", checkpoint: gatewayStartupDiagnosticUpgradeInspection,
			cause: errors.New(marker), ingressErr: errors.New(marker), want: "upgrade_inspection",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := gatewayStartupDiagnosticWrapIngress(test.checkpoint, test.cause, test.ingressErr)
			if code := gatewayStartupDiagnosticCheckpointForError(got); code != test.want {
				t.Fatalf("checkpoint=%q want %q", code, test.want)
			}
			if strings.Contains(got.Error(), marker) || !errors.Is(got, test.cause) {
				t.Fatalf("wrapper changed privacy or cause traversal: error=%q", got)
			}
		})
	}

	if got := gatewayStartupDiagnosticCheckpointForError(fmt.Errorf("outer: %w",
		gatewayStartupDiagnosticWrap(gatewayStartupDiagnosticOrdinaryFence, errors.New(marker)))); got != "unknown" {
		t.Fatalf("wrapped diagnostic was trusted through an error chain: %q", got)
	}
	if got := gatewayStartupDiagnosticCheckpointForError(
		gatewayStartupDiagnosticWrap(gatewayStartupDiagnosticCheckpoint("forged"), errors.New(marker))); got != "unknown" {
		t.Fatalf("forged private checkpoint was accepted: %q", got)
	}
}

func TestGatewayStartupDiagnosticWrapsRealStartupFailurePaths(t *testing.T) {
	assertFailure := func(t *testing.T, err, cause error, want string) {
		t.Helper()
		if got := gatewayStartupDiagnosticCheckpointForError(err); got != want {
			t.Fatalf("checkpoint=%q want %q; error=%v", got, want, err)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("wrapped error does not retain cause %v: %v", cause, err)
		}
		if got := err.Error(); got != "gateway startup diagnostic failed" {
			t.Fatalf("diagnostic Error=%q", got)
		}
	}

	t.Run("after preparation dependencies", func(t *testing.T) {
		_, err := inspectGatewayStartupAfterPreparationWithRunner(context.Background(), config.Config{}, nil, "",
			docker.ControllerDirectories{}, gatewayRebindStartupDecision{}, nil)
		if err == nil {
			t.Fatal("nil startup dependencies were accepted")
		}
		if got := gatewayStartupDiagnosticCheckpointForError(err); got != "startup_dependencies" {
			t.Fatalf("checkpoint=%q error=%v", got, err)
		}
	})

	t.Run("ordinary fence", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		denied := errors.New("fence denied")
		_, err := inspectGatewayStartupUsingIngressWithRunner(context.Background(), fixture.configuration, fixture.db,
			fixture.dockerExecutable, docker.ControllerDirectories{}, func(context.Context) error { return denied }, nil, nil)
		if err == nil {
			t.Fatal("denied fence was accepted")
		}
		assertFailure(t, err, denied, "ordinary_fence")
	})

	t.Run("startup snapshot", func(t *testing.T) {
		fixture := newRuntimeCompositionFixture(t)
		if err := fixture.db.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := inspectGatewayStartupUsingIngressWithRunner(context.Background(), fixture.configuration, fixture.db,
			fixture.dockerExecutable, docker.ControllerDirectories{}, func(context.Context) error { return nil }, nil, nil)
		if err == nil {
			t.Fatal("closed database snapshot was accepted")
		}
		if got := gatewayStartupDiagnosticCheckpointForError(err); got != "startup_snapshot_begin" {
			t.Fatalf("checkpoint=%q error=%v", got, err)
		}
	})
}

func TestGatewayStartupDiagnosticUsesDirectTaggedIngressResult(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	directories, err := docker.PrepareControllerDirectories(fixture.configuration.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := newGatewayStartupIngressWithRunner(fixture.configuration, fixture.dockerExecutable, directories,
		rebindFenceCheck(fixture.db), nil, runtimeprocess.ExecRunner{})
	if err != nil {
		t.Fatal(err)
	}
	_, ingressErr := ingress.InspectGatewayV2Startup(context.Background(), []generatedingress.GatewayV2StartupClaim{{}})
	if ingressErr == nil || generatedingress.GatewayStartupDiagnosticCheckpoint(ingressErr) != "upgrade_claims_validation" {
		t.Fatalf("invalid claim did not produce a direct tagged ingress error: %v", ingressErr)
	}
	contextual := fmt.Errorf("inspect gateway upgrade startup: %w", ingressErr)
	wrapped := gatewayStartupDiagnosticWrapIngress(gatewayStartupDiagnosticUpgradeInspection, contextual, ingressErr)
	if got := gatewayStartupDiagnosticCheckpointForError(wrapped); got != "upgrade_claims_validation" {
		t.Fatalf("checkpoint=%q error=%v", got, wrapped)
	}
	var ingressCause *generatedingress.Error
	if !errors.As(wrapped, &ingressCause) || ingressCause == nil || !errors.Is(wrapped, ingressErr) {
		t.Fatalf("direct ingress cause was not retained: %v", wrapped)
	}
}

func TestGatewayStartupDiagnosticUsesDirectSnapshotResult(t *testing.T) {
	fixture := newRuntimeCompositionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, snapshotErr := appaccess.New(fixture.db).HostingGatewayStartupSnapshot(ctx)
	stage, cause := appaccess.HostingGatewayStartupDiagnostic(snapshotErr)
	if snapshotErr == nil || stage != "begin" || cause != "canceled" || !errors.Is(snapshotErr, context.Canceled) {
		t.Fatalf("canceled snapshot stage=%q cause=%q error=%v", stage, cause, snapshotErr)
	}
	contextual := fmt.Errorf("read hosting gateway startup snapshot: %w", snapshotErr)
	wrapped := gatewayStartupDiagnosticWrapSnapshot(contextual, snapshotErr)
	if got := gatewayStartupDiagnosticCheckpointForError(wrapped); got != "startup_snapshot_begin" {
		t.Fatalf("checkpoint=%q error=%v", got, wrapped)
	}
	if got := gatewayStartupDiagnosticCauseForError(wrapped); got != "canceled" {
		t.Fatalf("cause=%q error=%v", got, wrapped)
	}
	if !errors.Is(wrapped, context.Canceled) || !errors.Is(wrapped, snapshotErr) ||
		wrapped.Error() != "gateway startup diagnostic failed" {
		t.Fatalf("snapshot wrapper did not retain the original cause: %v", wrapped)
	}
	if got := gatewayStartupDiagnosticCauseForError(gatewayStartupDiagnosticWrap(
		gatewayStartupDiagnosticOrdinaryFence, errors.New("ordinary failure"))); got != "unknown" {
		t.Fatalf("non-snapshot cause was emitted: %q", got)
	}
}
