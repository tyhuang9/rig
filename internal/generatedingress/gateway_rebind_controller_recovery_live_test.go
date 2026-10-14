package generatedingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
)

const liveControllerRecoveryEnvironment = "RIG_RUN_LIVE_LAN_GATEWAY_REBIND_RECOVERY"
const liveControllerRecoveryManifestEnvironment = "RIG_TEST_LAN_GATEWAY_REBIND_RECOVERY_MANIFEST"
const liveControllerRecoveryChildTest = "TestLiveLANGatewayRebindRecoveryChild"
const liveControllerRecoveryOutcomePrefix = "LAN_GATEWAY_REBIND_RECOVERY_OUTCOME:"

// Keep this private manifest in sync with the controller child. It contains
// only this disposable fixture's session, never credentials from a running Rig.
type liveControllerRecoveryManifest struct {
	Version               int    `json:"version"`
	Purpose               string `json:"purpose"`
	DataRoot              string `json:"dataRoot"`
	WorkingDirectory      string `json:"workingDirectory"`
	DockerExecutable      string `json:"dockerExecutable"`
	DockerConfigDirectory string `json:"dockerConfigDirectory"`
	HostPort              uint16 `json:"hostPort"`
	OperationID           string `json:"operationId"`
	SessionToken          string `json:"sessionToken"`
	CSRFToken             string `json:"csrfToken"`
}

// This gate crosses actual HTTP, session, SQLite, protected-state and Docker
// boundaries. The parent owns the disposable fixture and its existing strict
// cleanup. A separate controller test binary permits private handler assembly
// without production test hooks or a generatedingress/controller import cycle.
// It does not establish full-hostd startup or process-crash acceptance.
func TestLiveGatewayRebindControllerRecoveryCommitAndReplay(t *testing.T) {
	if os.Getenv(liveControllerRecoveryEnvironment) != "1" {
		t.Skip("set the controller recovery and typed runtime live flags on a disposable Linux Docker host")
	}
	requireLiveGatewayRebindRuntime(t)
	// Fail missing compilation/discovery before creating a Docker resource.
	binary := buildLiveControllerRecoveryChild(t)
	f := newLiveGatewayRebindRuntimeFixture(t)
	ctx, repository, manager := f.fixture.ctx, f.repository, f.manager
	beforeSQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || beforeSQL.Active != nil || len(beforeSQL.History) != 0 || beforeSQL.CurrentSource == nil {
		t.Fatal("controller recovery fixture lacks exact initial SQL authority")
	}
	beforeHeads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || len(beforeHeads) != 2 {
		t.Fatal("controller recovery fixture lacks both runtime heads")
	}
	beforeAccess, err := repository.CurrentAppAccess(ctx, f.fixture.spec.appID)
	if err != nil {
		t.Fatal("read initial immutable LAN grant")
	}
	beforeLoopback, err := liveGatewayRebindRuntimeLoopbackSnapshot(ctx, repository, f.loopbackID)
	if err != nil {
		t.Fatal("read initial loopback-only access")
	}
	// Use the existing admission coordinator to stop at a real prepared prefix.
	// Recovery itself runs only in the child through the HTTP handlers.
	err = manager.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
		_, prepareErr := manager.prepareGatewayRebindLocked(ctx, repository, f.input)
		return false, prepareErr
	})
	if err != nil {
		failLiveIngress(t, "prepare controller recovery operation", err)
	}
	prepared, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || prepared.Active == nil || prepared.Active.Claim.V2 == nil ||
		prepared.Active.Claim.V2.Spec.OperationID != f.inspection.Spec.OperationID ||
		prepared.Phase != appaccess.GatewayRebindPrepared || prepared.DatabaseCommitObserved ||
		!prepared.RollbackAllowed || len(prepared.History) != 1 ||
		!reflect.DeepEqual(beforeSQL.CurrentSource, prepared.CurrentSource) ||
		!errors.Is(repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
		t.Fatal("controller recovery did not retain the exact prepared claim and fence")
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Checkpoints) != 1 ||
		len(history.Progress) != 1 || history.Progress[0].Record.Phase != gatewayRebindProgressSuccessorIntent ||
		len(history.TerminalsV2) != 0 {
		t.Fatal("controller recovery lacks the actual prepared protected prefix")
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal("read protected prefix before HTTP recovery")
	}

	authentication := auth.New(f.db)
	session, err := authentication.NewSession(gatewayRebindTestAdministrator)
	if err != nil {
		t.Fatal("create disposable controller recovery session")
	}
	t.Cleanup(func() {
		if err := authentication.Logout(session.Token); err != nil {
			t.Error("revoke disposable controller recovery session")
		}
	})
	options := manager.options
	manifest := liveControllerRecoveryManifest{
		Version: 1, Purpose: "hostd/controller/lan-gateway-rebind-recovery-live/v1",
		DataRoot: options.DataRoot, WorkingDirectory: options.WorkingDirectory,
		DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory,
		HostPort: options.HostPort, OperationID: f.inspection.Spec.OperationID,
		SessionToken: session.Token, CSRFToken: session.CSRF,
	}
	body, err := json.Marshal(manifest)
	if err != nil || len(body) > 16<<10 {
		t.Fatal("encode private controller recovery manifest")
	}
	manifestPath := filepath.Join(t.TempDir(), "controller-recovery.json")
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal("write private controller recovery manifest")
	}
	runLiveControllerRecoveryChild(t, ctx, binary, manifestPath, f)

	afterSQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || afterSQL.Active != nil || len(afterSQL.History) != 1 ||
		afterSQL.History[0].Claim.V2 == nil || afterSQL.History[0].Claim.V2.State != appaccess.GatewayRebindCommitted ||
		afterSQL.CurrentSource == nil || afterSQL.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
		afterSQL.CurrentSource.OperationID != f.inspection.Spec.OperationID ||
		afterSQL.CurrentSource.ProfileRevisionID != f.inspection.Spec.SuccessorProfileRevisionID ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("HTTP recovery did not commit exact successor authority and release its SQL fence")
	}
	afterHistory, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(afterHistory.Progress) != 17 || len(afterHistory.TerminalsV2) != 1 ||
		afterHistory.Progress[16].Record.Phase != gatewayRebindProgressHandoverCommitted ||
		afterHistory.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionCommit ||
		afterHistory.TerminalsV2[0].Receipt.Digest != afterSQL.CurrentSource.TerminalReceiptDigest {
		t.Fatal("HTTP recovery did not retain the exact protected terminal")
	}
	afterFiles, filesErr := readGatewayHistorySnapshotMode(manager.store, true)
	afterHeads, headsErr := repository.GatewayRebindRuntimeHeads(ctx)
	afterAccess, accessErr := repository.CurrentAppAccess(ctx, f.fixture.spec.appID)
	afterLoopback, loopErr := liveGatewayRebindRuntimeLoopbackSnapshot(ctx, repository, f.loopbackID)
	if filesErr != nil || !gatewayRebindAdmissionHistoryRetained(beforeFiles, afterFiles) ||
		headsErr != nil || !reflect.DeepEqual(beforeHeads, afterHeads) ||
		accessErr != nil || !reflect.DeepEqual(beforeAccess, afterAccess) ||
		loopErr != nil || !reflect.DeepEqual(beforeLoopback, afterLoopback) {
		t.Fatal("HTTP recovery changed retained history, raw application access or runtime heads")
	}
	assertLiveOneShot(t, ctx, options.HostPort, f.fixture.spec.appID, "/", f.fixture.spec.applicationReply,
		"HTTP recovery LAN application's loopback route")
	assertLiveOneShot(t, ctx, options.HostPort, f.loopbackID, "/", f.loopbackReply,
		"HTTP recovery loopback-only application route")
	wrongHost := probeGatewayV2HostStatus(ctx, f.successorIPv4, f.fixture.port, f.loopbackID+".rig.localhost", "/")
	if !liveGatewayRebindRuntimeLANBody(ctx, f.successorIPv4, f.fixture.port, f.fixture.spec.applicationReply) ||
		!wrongHost.Connected || !wrongHost.Responded || wrongHost.Status != 404 ||
		!probeGatewayRebindHandoverListenerAbsent(ctx, f.predecessorIPv4, f.fixture.port) {
		t.Fatal("HTTP recovery failed selected LAN routing, loopback isolation or predecessor withdrawal")
	}
	t.Log("verified actual HTTP recovery, rejected consent/replay, immutable history and both Docker application routes")
}

func buildLiveControllerRecoveryChild(t *testing.T) string {
	t.Helper()
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("controller recovery gate requires the repository Go toolchain")
	}
	binary := filepath.Join(t.TempDir(), "controller-recovery.test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, goExecutable, "test", "-c", "-o", binary, "../controller")
	command.Env = liveControllerRecoveryChildEnvironment("")
	command.Env = append(command.Env, "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly -p=1 -buildvcs=false")
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile exact controller recovery child: %v", err)
	}
	discovery := exec.CommandContext(ctx, binary, "-test.list=^"+liveControllerRecoveryChildTest+"$")
	discovery.Env = liveControllerRecoveryChildEnvironment("")
	output, err := discovery.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != liveControllerRecoveryChildTest {
		t.Fatal("controller recovery child was not discovered exactly once")
	}
	return binary
}

func runLiveControllerRecoveryChild(t *testing.T, ctx context.Context, binary, manifest string,
	fixture liveGatewayRebindRuntimeFixture,
) {
	t.Helper()
	before, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("read compiled controller recovery child")
	}
	command := exec.CommandContext(ctx, binary, "-test.run=^"+liveControllerRecoveryChildTest+"$",
		"-test.count=1", "-test.v", "-test.timeout=20m")
	command.Env = liveControllerRecoveryChildEnvironment(manifest)
	output, runErr := command.CombinedOutput()
	after, readErr := os.ReadFile(binary)
	if runErr != nil || readErr != nil || sha256.Sum256(before) != sha256.Sum256(after) ||
		bytes.Contains(output, []byte("--- SKIP:")) || bytes.Contains(output, []byte("--- FAIL:")) ||
		!bytes.Contains(output, []byte("--- PASS: "+liveControllerRecoveryChildTest+" ")) ||
		bytes.Count(output, []byte("LAN_GATEWAY_REBIND_RECOVERY_HTTP_OK")) != 1 {
		// This child handles session credentials. Never echo its raw output.
		state := fixture.failureStateDiagnostic(ctx, errors.New("controller recovery child failed"))
		t.Fatalf("controller recovery child did not complete the exact HTTP journey: error=%v last_attempt=%s approved_outcome=%s child_failure=%s sql_phase=%s terminal_receipt=%s current_artifact=%s authority=%s",
			runErr, liveControllerRecoveryLastAttempt(output), liveControllerRecoveryApprovedOutcome(output),
			liveControllerRecoveryChildFailure(output), state.SQLPhase, state.TerminalReceipt,
			state.CurrentArtifact, state.Authority)
	}
}

// Markers identify only the next request the child was about to attempt. They
// neither prove that request completed nor identify the cause of a failure.
func liveControllerRecoveryLastAttempt(output []byte) string {
	last := "unknown"
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		stage, found := strings.CutPrefix(strings.TrimSpace(string(line)),
			"LAN_GATEWAY_REBIND_RECOVERY_STAGE:")
		if !found {
			continue
		}
		switch stage {
		case "prepared-status", "prepared-review", "altered-consent", "post-refusal-review", "approved-reconcile",
			"terminal-status", "terminal-review", "stale-reconcile", "post-stale-status":
			last = stage
		}
	}
	return last
}

func liveControllerRecoveryApprovedOutcome(output []byte) string {
	result := "unknown"
	found := false
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		value, ok := strings.CutPrefix(strings.TrimSpace(string(line)), liveControllerRecoveryOutcomePrefix)
		if !ok {
			continue
		}
		switch value {
		case "success-commit", "success-abort", "success-other", "problem-unavailable", "problem-conflict",
			"problem-approval-mismatch", "problem-invalid-request", "problem-forbidden", "problem-not-found",
			"problem-other":
			if found {
				return "ambiguous"
			}
			result, found = value, true
		default:
			return "unknown"
		}
	}
	return result
}

func liveControllerRecoveryChildFailure(output []byte) string {
	for _, failure := range []struct {
		message string
		value   string
	}{
		{message: "execute controller recovery HTTP request", value: "request"},
		{message: "read bounded no-store controller recovery response", value: "response-boundary"},
		{message: "decode controller recovery response", value: "response-decode"},
		{message: "approved HTTP reconciliation did not return exact committed authority", value: "approved-authority"},
		{message: "approved reconciliation did not write exactly one purpose-bound audit", value: "approved-audit"},
	} {
		if bytes.Contains(output, []byte(failure.message)) {
			return failure.value
		}
	}
	return "unknown"
}

func TestLiveControllerRecoveryDiagnosticsAreFinite(t *testing.T) {
	for _, test := range []struct {
		name        string
		output      string
		wantOutcome string
		wantFailure string
	}{
		{name: "commit authority refusal",
			output: liveControllerRecoveryOutcomePrefix + "success-commit\n" +
				"approved HTTP reconciliation did not return exact committed authority private-value\n",
			wantOutcome: "success-commit", wantFailure: "approved-authority"},
		{name: "curated unavailable",
			output: liveControllerRecoveryOutcomePrefix + "problem-unavailable\n" +
				"decode controller recovery response private-value\n",
			wantOutcome: "problem-unavailable", wantFailure: "response-decode"},
		{name: "unknown values are clamped",
			output:      liveControllerRecoveryOutcomePrefix + "private-value\nprivate-value\n",
			wantOutcome: "unknown", wantFailure: "unknown"},
		{name: "duplicates are ambiguous",
			output: liveControllerRecoveryOutcomePrefix + "success-commit\n" +
				liveControllerRecoveryOutcomePrefix + "success-abort\n",
			wantOutcome: "ambiguous", wantFailure: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if outcome, failure := liveControllerRecoveryApprovedOutcome([]byte(test.output)),
				liveControllerRecoveryChildFailure([]byte(test.output)); outcome != test.wantOutcome || failure != test.wantFailure {
				t.Fatalf("outcome=%q failure=%q want_outcome=%q want_failure=%q",
					outcome, failure, test.wantOutcome, test.wantFailure)
			}
		})
	}
}

func liveControllerRecoveryChildEnvironment(manifest string) []string {
	result := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == liveControllerRecoveryManifestEnvironment || key == "GOTOOLCHAIN" || key == "GOFLAGS" {
			continue
		}
		result = append(result, entry)
	}
	if manifest != "" {
		result = append(result, liveControllerRecoveryManifestEnvironment+"="+manifest)
	}
	return result
}
