package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// gatewayRebindLiveBoundary is the closed crash/restart contract for the
// supported hostd rebind journey. A boundary is admitted to the live matrix
// only when the child can prove the named durable write or completed effect;
// reaching a timer or beginning an operation is never sufficient.
type gatewayRebindLiveBoundary struct {
	ID string
	// ExpectedTerminalDisposition records the deterministic terminal result
	// required by this exact fixture. It is not a general recovery policy.
	ExpectedTerminalDisposition string
	Proof                       string
	SourceFile                  string
	Source                      string
}

// Keep this list independent from gatewayRebindLiveBoundaries. The control
// test below rejects a matrix that silently removes, duplicates, or renames a
// required boundary.
const gatewayRebindRequiredBoundaryIDs = `
a0-sql-claim
p0-predecessor-checkpoint
p1-protected-intent
p01-successor-intent
p02-image-observed
p03-network-bound
p04-config-volume-bound
p05-data-volume-bound
p06-stage-container-bound
p07-stage-config-intent
p08-stage-config-copied
p09-stage-start-intent
p10-stage-serving
p11-final-config-intent
p12-final-config-copied
p13-final-handover-intent
p14-final-container-bound
p15-cutover-intent
p16-successor-serving
p17-handover-completed
r01-rollback-intent
r02-rollback-completed
s01-protected-terminal-installed
s02-protected-terminal-readback
s03-successor-ready
s04-database-committed
s05-current-attestation
s06-terminal-sql
s07-stable-final-census
s08-fence-transition
s09-effects-lease-release
s10-gateway-lock-release
s11-response-loss
`

var gatewayRebindLiveBoundaries = []gatewayRebindLiveBoundary{
	{"a0-sql-claim", "commit", "prepared SQL claim exists; protected checkpoint, intent, progress and terminal are absent", "internal/generatedingress/gateway_rebind_cross_store_prepare.go", "gatewayRebindAfterClaim"},
	{"p0-predecessor-checkpoint", "commit", "exact predecessor checkpoint is readable; protected intent/progress are absent", "internal/generatedingress/gateway_rebind_cross_store_prepare.go", "gatewayRebindAfterPreparedCheckpoint"},
	{"p1-protected-intent", "commit", "exact protected intent is readable and digest-bound to A0 and P0", "internal/generatedingress/gateway_rebind_cross_store_prepare.go", "gatewayRebindAfterPreparedIntent"},
	{"p01-successor-intent", "commit", "protected progress sequence 1 is successor_intent", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressSuccessorIntent"},
	{"p02-image-observed", "commit", "protected progress sequence 2 binds the observed immutable image and stage plan", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "case 2:"},
	{"p03-network-bound", "commit", "protected progress sequence 3 binds the created successor network and ownership", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "case 3:"},
	{"p04-config-volume-bound", "commit", "protected progress sequence 4 binds the successor config volume", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "case 4:"},
	{"p05-data-volume-bound", "commit", "protected progress sequence 5 binds the successor data volume", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "case 5:"},
	{"p06-stage-container-bound", "commit", "protected progress sequence 6 binds the stopped stage container", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "case 6:"},
	{"p07-stage-config-intent", "commit", "protected progress sequence 7 records the stage config copy intent", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressStageConfigIntent"},
	{"p08-stage-config-copied", "commit", "protected progress sequence 8 attests the copied stage config", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressStageConfigCopied"},
	{"p09-stage-start-intent", "commit", "protected progress sequence 9 records the exact stage start intent", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressStageStartIntent"},
	{"p10-stage-serving", "commit", "protected progress sequence 10 binds the serving stage endpoint", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressStageServing"},
	{"p11-final-config-intent", "commit", "protected progress sequence 11 records the final config copy intent", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressFinalConfigIntent"},
	{"p12-final-config-copied", "commit", "protected progress sequence 12 attests the final config copy", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressFinalConfigCopied"},
	{"p13-final-handover-intent", "commit", "protected progress sequence 13 binds predecessor observation and application networks", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressFinalHandoverIntent"},
	{"p14-final-container-bound", "commit", "protected progress sequence 14 binds the final successor container", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressFinalContainerBound"},
	{"p15-cutover-intent", "commit", "protected progress sequence 15 binds the cutover observation", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressCutoverIntent"},
	{"p16-successor-serving", "commit", "protected progress sequence 16 proves successor serving after cutover", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressSuccessorServing"},
	{"p17-handover-completed", "commit", "protected progress sequence 17 proves final resources, predecessor withdrawal and handover", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressHandoverCommitted"},
	{"r01-rollback-intent", "abort", "protected rollback intent binds every exact-owned resource and predecessor route", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressRollbackIntent"},
	{"r02-rollback-completed", "abort", "protected rollback completion proves owned resources absent and predecessor serving", "internal/generatedingress/gateway_rebind_progress.go", "gatewayRebindProgressHandoverRolledBack"},
	{"s01-protected-terminal-installed", "commit", "exact protected terminal receipt is durably installed after the physical result and before decrypt/readback", "internal/generatedingress/gateway_rebind_durable_boundary.go", "GatewayRebindBoundaryProtectedTerminalInstalled"},
	{"s02-protected-terminal-readback", "commit", "the installed terminal receipt reads back with the exact operation, generation, sequence and digest", "internal/generatedingress/gateway_rebind_durable_boundary.go", "GatewayRebindBoundaryProtectedTerminalReadback"},
	{"s03-successor-ready", "commit", "immutable SQL successor_ready transition follows terminal publication and revalidated physical serving", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "appaccess.GatewayRebindSuccessorReady"},
	{"s04-database-committed", "commit", "one SQL transaction records database_committed, current profile and grant transfers", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "appaccess.GatewayRebindDatabaseCommitted"},
	{"s05-current-attestation", "commit", "selected-current physical attestation is bound to the exact terminal lineage before terminal SQL", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "GatewayRebindBoundaryCurrentAttestation"},
	{"s06-terminal-sql", "commit", "terminal SQL transition retains the terminal receipt, immutable database event and current attestation", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "appaccess.GatewayRebindCommitted"},
	{"s07-stable-final-census", "commit", "fresh SQL, protected history, selected current and runtime census remain stable", "internal/generatedingress/gateway_rebind_cross_store_recovery.go", "sameGatewayCurrentSelection"},
	{"s08-fence-transition", "commit", "active claim is absent and CheckGatewayRebindFence is clear", "internal/generatedingress/gateway_rebind_cross_store_recovery.go", "CheckGatewayRebindFence"},
	{"s09-effects-lease-release", "commit", "deployment-effects lease release completes successfully", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "releaseEffects()"},
	{"s10-gateway-lock-release", "commit", "gateway OS lock release completes after the effects lease", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "releaseGateway()"},
	{"s11-response-loss", "commit", "terminal response is intentionally dropped only after both releases", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "releaseGatewayRebindTerminalLocks"},
}

type gatewayRebindLiveScenario struct {
	ID         string
	Proof      string
	SourceFile string
	Source     string
}

const gatewayRebindRequiredScenarioIDs = `
lost-network-create-ack
lost-config-volume-create-ack
lost-data-volume-create-ack
lost-stage-container-create-ack
lost-stage-config-copy-ack
lost-stage-start-ack
lost-final-config-copy-ack
lost-final-container-create-ack
lost-cutover-ack
lost-successor-serving-ack
literal-unresolved-precommit
literal-unresolved-database-committed
terminal-replay
selected-current-restoration
pending-route-restart
pending-lan-restart
pending-lan-batch-restart
competing-controller
original-role-revocation
current-role-revocation
cancellation-before-write
cancellation-after-write
ownership-drift
configuration-drift
census-drift
effects-release-fault
gateway-release-fault
emergency-withdrawal-failure
recovery-worker-isolation
recovery-background-isolation
`

const gatewayRebindRequiredLostAcknowledgementScenarioIDs = `
lost-network-create-ack
lost-config-volume-create-ack
lost-data-volume-create-ack
lost-stage-container-create-ack
lost-stage-config-copy-ack
lost-stage-start-ack
lost-final-config-copy-ack
lost-final-container-create-ack
lost-cutover-ack
lost-successor-serving-ack
`

// gatewayRebindLiveLostAcknowledgementExpectedTerminalDispositions covers the
// exact fixture cases where a completed Docker effect loses only its
// coordinator acknowledgement and is adopted on restart. It has no fallback
// for other scenarios.
var gatewayRebindLiveLostAcknowledgementExpectedTerminalDispositions = map[string]string{
	"lost-network-create-ack":         "commit",
	"lost-config-volume-create-ack":   "commit",
	"lost-data-volume-create-ack":     "commit",
	"lost-stage-container-create-ack": "commit",
	"lost-stage-config-copy-ack":      "commit",
	"lost-stage-start-ack":            "commit",
	"lost-final-config-copy-ack":      "commit",
	"lost-final-container-create-ack": "commit",
	"lost-cutover-ack":                "commit",
	"lost-successor-serving-ack":      "commit",
}

var gatewayRebindLiveScenarios = []gatewayRebindLiveScenario{
	{"lost-network-create-ack", "network exists with exact ownership although its Docker success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "bindNetwork"},
	{"lost-config-volume-create-ack", "config volume exists with exact ownership although its Docker success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "bindConfigVolume"},
	{"lost-data-volume-create-ack", "data volume exists with exact ownership although its Docker success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "bindDataVolume"},
	{"lost-stage-container-create-ack", "stage container exists with exact binding although its Docker success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "bindStageContainer"},
	{"lost-stage-config-copy-ack", "stage config bytes match the intent although the copy success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "copyStageConfig"},
	{"lost-stage-start-ack", "stage container is serving although the start success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "serveStage"},
	{"lost-final-config-copy-ack", "final config bytes match the intent although the copy success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "copyFinalConfig"},
	{"lost-final-container-create-ack", "final container exists with exact binding although create/start success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "bindFinalContainer"},
	{"lost-cutover-ack", "successor route is active although cutover success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "prepareCutover"},
	{"lost-successor-serving-ack", "successor serves and predecessor is withdrawn although serving success was not returned", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "serveSuccessor"},
	{"literal-unresolved-precommit", "fixed precommit unresolved evidence permits only the validated rollback policy", "internal/generatedingress/gateway_rebind_cross_store_recovery.go", "appaccess.GatewayRebindUnresolved"},
	{"literal-unresolved-database-committed", "an immutable database commit forces forward completion from literal unresolved", "internal/generatedingress/gateway_rebind_cross_store_recovery.go", "snapshot.DatabaseCommitObserved"},
	{"terminal-replay", "fresh hostd returns retained terminal state without repeating any effect", "internal/generatedingress/gateway_rebind_commit_replay.go", "readGatewayRebindCommitReplay"},
	{"selected-current-restoration", "fresh hostd restores exact committed serving before normal admission", "internal/generatedingress/gateway_rebind_committed_serving_restore.go", "restoreGatewayRebindCommittedServingLocked"},
	{"pending-route-restart", "dedicated current route recovery retains precedence over historical rebind replay", "internal/generatedingress/gateway_rebind_cross_store_inspection.go", "GatewayCurrentRecoveryRoute"},
	{"pending-lan-restart", "dedicated current LAN recovery retains the exact operation pin", "internal/generatedingress/gateway_rebind_cross_store_inspection.go", "GatewayCurrentRecoveryLAN"},
	{"pending-lan-batch-restart", "migrated native LAN batch recovery retains the exact batch head", "internal/generatedingress/gateway_v2_lan_recovery_batch.go", "QuarantineGatewayV2LANAccessRecoveryBatch"},
	{"competing-controller", "a second hostd cannot cross owner/effects/gateway locks while the first owns recovery", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "runGatewayRebindCommitLocked"},
	{"original-role-revocation", "revoked original approver blocks a newly requested direction before effects", "internal/generatedingress/gateway_rebind_commit_facade.go", "liveSession"},
	{"current-role-revocation", "revoked current administrator blocks interactive reconcile before effects", "internal/controller/gateway_rebind_session.go", "bindLANGatewayRebindSessionAuthority"},
	{"cancellation-before-write", "cancellation before A0 leaves SQL and protected history unchanged", "internal/generatedingress/gateway_rebind_cross_store_prepare.go", "checkGatewayRebindAdmissionSession"},
	{"cancellation-after-write", "cancellation after durable admission preserves recoverable evidence and the fence", "internal/generatedingress/gateway_rebind_cross_store_prepare.go", "continueGatewayRebindStrictAdmissionLocked"},
	{"ownership-drift", "changed Docker ownership refuses execution and permits no guessed cleanup", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "readGatewayRebindTypedForwardBoundaryLocked"},
	{"configuration-drift", "changed protected or Docker config refuses execution and retains the fence", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "gatewayRebindEffectBoundaryError"},
	{"census-drift", "changed runtime heads, grants or hosting census refuses the guarded effect", "internal/generatedingress/gateway_rebind_committed_serving_restore.go", "gatewayRebindCommittedServingCensusMatchesLocked"},
	{"effects-release-fault", "failed effects lease release returns no success and latches the process", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "releaseGatewayRebindTerminalLocks"},
	{"gateway-release-fault", "failed gateway lock release returns no success and latches the process", "internal/generatedingress/gateway_rebind_cross_store_commit.go", "releaseGatewayRebindTerminalLocks"},
	{"emergency-withdrawal-failure", "failed exact-owned emergency withdrawal keeps ordinary authority closed", "internal/generatedingress/gateway_rebind_cross_store_driver.go", "withdrawForwardSuccessorLocked"},
	{"recovery-worker-isolation", "queued ordinary work remains untouched while recovery-only assembly omits worker, auto-deploy and relay graphs", "cmd/hostd/main.go", "runRecoveryOnlyController"},
	{"recovery-background-isolation", "enabled auto-deploy and relay startup graphs remain absent through recovery readiness, probes and reconcile, then start and recover durable sentinels in a fresh normal process", "cmd/hostd/gateway_rebind_background_isolation_test.go", "liveHostdRebindRunRecoveryBackgroundIsolation"},
}

func TestGatewayRebindLiveRequiredManifestIsClosed(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	validateGatewayRebindLiveManifest(t, root, "boundary", gatewayRebindRequiredBoundaryIDs,
		len(gatewayRebindLiveBoundaries), func(index int) (string, string, string, string) {
			entry := gatewayRebindLiveBoundaries[index]
			return entry.ID, entry.Proof, entry.SourceFile, entry.Source
		})
	validateGatewayRebindLiveManifest(t, root, "scenario", gatewayRebindRequiredScenarioIDs,
		len(gatewayRebindLiveScenarios), func(index int) (string, string, string, string) {
			entry := gatewayRebindLiveScenarios[index]
			return entry.ID, entry.Proof, entry.SourceFile, entry.Source
		})
	validateGatewayRebindLiveTerminalExpectations(t)
}

func validateGatewayRebindLiveTerminalExpectations(t *testing.T) {
	t.Helper()
	boundaryCounts := map[string]int{}
	for _, entry := range gatewayRebindLiveBoundaries {
		want := "commit"
		if entry.ID == "r01-rollback-intent" || entry.ID == "r02-rollback-completed" {
			want = "abort"
		}
		if entry.ExpectedTerminalDisposition != want {
			t.Errorf("boundary %q terminal disposition=%q want=%q", entry.ID,
				entry.ExpectedTerminalDisposition, want)
		}
		boundaryCounts[entry.ExpectedTerminalDisposition]++
	}
	if boundaryCounts["commit"] != 31 || boundaryCounts["abort"] != 2 ||
		len(boundaryCounts) != 2 {
		t.Errorf("boundary terminal disposition counts=%v want commit=31 abort=2", boundaryCounts)
	}

	scenarios := make(map[string]struct{}, len(gatewayRebindLiveScenarios))
	for _, entry := range gatewayRebindLiveScenarios {
		scenarios[entry.ID] = struct{}{}
	}
	wantLoss := map[string]string{
		"lost-network-create-ack":         "commit",
		"lost-config-volume-create-ack":   "commit",
		"lost-data-volume-create-ack":     "commit",
		"lost-stage-container-create-ack": "commit",
		"lost-stage-config-copy-ack":      "commit",
		"lost-stage-start-ack":            "commit",
		"lost-final-config-copy-ack":      "commit",
		"lost-final-container-create-ack": "commit",
		"lost-cutover-ack":                "commit",
		"lost-successor-serving-ack":      "commit",
	}
	lossIDs := strings.Fields(gatewayRebindRequiredLostAcknowledgementScenarioIDs)
	if len(lossIDs) != 10 || len(gatewayRebindLiveLostAcknowledgementExpectedTerminalDispositions) != len(lossIDs) {
		t.Errorf("lost acknowledgement terminal map size=%d ids=%d want=10",
			len(gatewayRebindLiveLostAcknowledgementExpectedTerminalDispositions), len(lossIDs))
	}
	lossSet := make(map[string]struct{}, len(lossIDs))
	for _, id := range lossIDs {
		if _, duplicate := lossSet[id]; duplicate {
			t.Errorf("lost acknowledgement scenario %q is duplicated", id)
		}
		lossSet[id] = struct{}{}
		if _, found := scenarios[id]; !found {
			t.Errorf("lost acknowledgement scenario %q is absent from the closed scenario manifest", id)
		}
		got, found := gatewayRebindLiveLostAcknowledgementExpectedTerminalDispositions[id]
		if !found || got != wantLoss[id] {
			t.Errorf("lost acknowledgement scenario %q terminal disposition=%q want=%q", id, got, wantLoss[id])
		}
	}
	for id, got := range gatewayRebindLiveLostAcknowledgementExpectedTerminalDispositions {
		if _, expected := lossSet[id]; !expected {
			t.Errorf("unexpected lost acknowledgement terminal disposition for %q=%q", id, got)
		}
		if got != "commit" && got != "abort" {
			t.Errorf("lost acknowledgement scenario %q has invalid terminal disposition %q", id, got)
		}
	}
}

func validateGatewayRebindLiveManifest(t *testing.T, root, kind, required string, count int,
	entry func(int) (id, proof, sourceFile, source string),
) {
	t.Helper()
	want := strings.Fields(required)
	wantSet := make(map[string]struct{}, len(want))
	for _, id := range want {
		if _, duplicate := wantSet[id]; duplicate {
			t.Fatalf("required %s id %q is duplicated", kind, id)
		}
		wantSet[id] = struct{}{}
	}
	gotSet := make(map[string]struct{}, count)
	for index := 0; index < count; index++ {
		id, proof, sourceFile, source := entry(index)
		if _, required := wantSet[id]; !required {
			t.Errorf("unknown %s id %q", kind, id)
		}
		if _, duplicate := gotSet[id]; duplicate {
			t.Errorf("implemented %s id %q is duplicated", kind, id)
		}
		gotSet[id] = struct{}{}
		if strings.TrimSpace(proof) == "" || strings.Contains(strings.ToLower(proof), "skip") {
			t.Errorf("%s %q has empty or skipped proof", kind, id)
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sourceFile)))
		if err != nil {
			t.Errorf("%s %q source %q: %v", kind, id, sourceFile, err)
			continue
		}
		if source == "" || !strings.Contains(string(body), source) {
			t.Errorf("%s %q source anchor %q is absent from %s", kind, id, source, sourceFile)
		}
	}
	var missing []string
	for id := range wantSet {
		if _, ok := gotSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 || len(gotSet) != len(wantSet) {
		t.Errorf("%s manifest mismatch: missing=%v got=%d want=%d", kind, missing, len(gotSet), len(wantSet))
	}
}

func gatewayRebindLiveCaseName(prefix, id string) string {
	return fmt.Sprintf("%s/%s", prefix, id)
}
