//go:build live_docker

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	liveHostdRebindChildEnvironment    = "RIG_TEST_LIVE_HOSTD_REBIND_CHILD"
	liveHostdRebindManifestEnvironment = "RIG_TEST_LIVE_HOSTD_REBIND_MANIFEST"

	// These limits apply only to the closed test-harness classification below.
	// The complete stderr stream remains digest-only evidence; it is never
	// emitted by the parent test process.
	liveHostdRebindStartupDiagnosticMaxBytes   = 64 << 10
	liveHostdRebindStartupDiagnosticMaxRecords = 64
	liveHostdRebindRoutePauseMaxBytes          = 64 << 10
	liveHostdRebindRoutePauseMaxRecords        = 64

	// The fixture owns a four-minute cleanup, the registered two-app fixture
	// owns another four minutes, and test-owned network cleanup has one minute.
	// Keep a further twenty seconds for reaping the exact child, rounded up to
	// ten minutes so an enclosing Go test deadline cannot interrupt cleanup.
	liveHostdRebindStartupCleanupReserve = 10 * time.Minute
	// A full daemon normally starts far faster. This cap turns a child-internal
	// stall into a reportable test failure without extending any child deadline.
	liveHostdRebindStartupReadinessCap = 10 * time.Minute
	// Claim observation and startup refusal are deliberately shorter than
	// ordinary readiness so a stalled harness leaves its existing cleanup
	// reserve rather than reaching the enclosing package timeout.
	liveHostdRebindClaimObservationCap = 2 * time.Minute
	liveHostdRebindStartupRefusalCap   = 2 * time.Minute
	liveHostdRebindChildReapTimeout    = 20 * time.Second
)

type liveHostdRebindChildManifest struct {
	DataRoot         string `json:"dataRoot"`
	Listen           string `json:"listen"`
	OperationID      string `json:"operationId,omitempty"`
	BoundaryID       string `json:"boundaryId,omitempty"`
	ScenarioID       string `json:"scenarioId,omitempty"`
	ReadyMarker      string `json:"readyMarker"`
	BoundaryMarker   string `json:"boundaryMarker"`
	EffectLedger     string `json:"effectLedger"`
	RequestArm       string `json:"requestArm"`
	BackgroundLedger string `json:"backgroundLedger"`
	NetworkLedger    string `json:"networkLedger"`
	RelayLedger      string `json:"relayLedger"`
}

type liveHostdRebindReadyMarker struct {
	Address      string `json:"address"`
	RecoveryOnly bool   `json:"recoveryOnly"`
	RecoveryKind string `json:"recoveryKind,omitempty"`
	OperationID  string `json:"operationId,omitempty"`
}

type liveHostdRebindBoundaryMarker struct {
	Boundary            string `json:"boundary"`
	OperationID         string `json:"operationId"`
	SQLPhase            string `json:"sqlPhase,omitempty"`
	ProtectedPhase      string `json:"protectedPhase,omitempty"`
	ProtectedGeneration uint64 `json:"protectedGeneration,omitempty"`
	ProtectedSequence   uint64 `json:"protectedSequence,omitempty"`
	Digest              string `json:"digest,omitempty"`
	Disposition         string `json:"disposition,omitempty"`
}

// liveHostdRebindRunner delegates every command to the production ExecRunner.
// Its observer can only record a completed coordinator boundary and wait for
// the parent to kill the process. It receives no repository, Manager, lock or
// effect object and cannot manufacture a successful coordinator result.
type liveHostdRebindRunner struct {
	delegate runtimeprocess.ExecRunner
	manifest liveHostdRebindChildManifest

	lostAck                    atomic.Bool
	preclaimBlocked            atomic.Bool
	emergencyStartupFailure    atomic.Bool
	emergencyWithdrawalFailure atomic.Bool
	resourceMu                 sync.Mutex
	stageContainerID           string
	finalContainerID           string
	protectedPhase             string
	protectedSequence          uint64
}

func (r *liveHostdRebindRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if r.manifest.ScenarioID == "cancellation-before-write" &&
		liveHostdRebindDockerRead(request.Args) &&
		liveHostdRebindRegularFileExists(r.manifest.RequestArm) &&
		r.preclaimBlocked.CompareAndSwap(false, true) {
		_ = liveHostdRebindWriteExclusiveJSON(r.manifest.BoundaryMarker, liveHostdRebindBoundaryMarker{
			Boundary: "preclaim_read_blocked", OperationID: r.manifest.OperationID,
		})
		<-ctx.Done()
		return runtimeprocess.CommandResult{}, ctx.Err()
	}
	if r.manifest.ScenarioID == "emergency-withdrawal-failure" {
		if liveHostdRebindCommandHas(request.Args, "container", "inspect") &&
			r.emergencyStartupFailure.CompareAndSwap(false, true) {
			return runtimeprocess.CommandResult{}, errors.New("test-owned startup observation failure")
		}
		if r.emergencyStartupFailure.Load() && liveHostdRebindCommandHas(request.Args, "container", "stop") &&
			r.emergencyWithdrawalFailure.CompareAndSwap(false, true) {
			_ = liveHostdRebindWriteExclusiveJSON(r.manifest.BoundaryMarker, liveHostdRebindBoundaryMarker{
				Boundary: "emergency_withdrawal_failed", OperationID: r.manifest.OperationID,
			})
			return runtimeprocess.CommandResult{}, errors.New("test-owned emergency withdrawal failure")
		}
	}
	result, err := r.delegate.Run(ctx, request)
	if err == nil {
		if recordErr := r.recordReplayEffect(request.Args); recordErr != nil {
			return result, recordErr
		}
	}
	r.rememberCreatedContainer(request.Args, result, err)
	injectedRollback := ((r.manifest.ScenarioID == "" && (r.manifest.BoundaryID == "r01-rollback-intent" ||
		r.manifest.BoundaryID == "r02-rollback-completed")) ||
		r.manifest.ScenarioID == "literal-unresolved-precommit") &&
		r.stageStartCommand(request.Args)
	injectedLostAck := r.manifest.ScenarioID != "" &&
		r.lostAcknowledgementCommand(r.manifest.ScenarioID, request.Args)
	if err != nil || r.lostAck.Load() || (!injectedRollback && !injectedLostAck) {
		return result, err
	}
	if !r.lostAck.CompareAndSwap(false, true) {
		return result, err
	}
	if liveHostdRebindLostEffectScenario(r.manifest.ScenarioID) {
		_ = liveHostdRebindWriteExclusiveJSON(r.manifest.BoundaryMarker, liveHostdRebindBoundaryMarker{
			Boundary:    "completed_effect_ack_lost:" + r.manifest.ScenarioID,
			OperationID: r.manifest.OperationID,
		})
		// Do not return the completed command result to the coordinator. The
		// parent kills this exact process while the real effect is complete and
		// its durable protected acknowledgment is still absent.
		select {}
	}
	// The real command has completed. Clear captured output so an injected
	// acknowledgement loss cannot leak command results or be mistaken for an
	// observed object ID by the coordinator.
	for index := range result.Stdout {
		result.Stdout[index] = 0
	}
	for index := range result.Stderr {
		result.Stderr[index] = 0
	}
	result.Stdout, result.Stderr = nil, nil
	if strings.HasPrefix(r.manifest.ScenarioID, "pending-") {
		_ = liveHostdRebindWriteExclusiveJSON(r.manifest.BoundaryMarker, liveHostdRebindBoundaryMarker{
			Boundary: "current_effect_ack_lost", OperationID: r.manifest.OperationID,
		})
		select {}
	}
	return result, errors.New("test-owned completed effect acknowledgement lost")
}

func liveHostdRebindDockerRead(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] + " " + args[1] {
	case "container inspect", "image inspect", "network inspect", "volume inspect",
		"network ls", "volume ls":
		return true
	case "ps -aq", "ps --all", "ps -a":
		return true
	default:
		return false
	}
}

func liveHostdRebindRegularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func (r *liveHostdRebindRunner) recordReplayEffect(args []string) error {
	if r.manifest.ScenarioID != "terminal-replay" || !liveHostdRebindDockerMutation(args) {
		return nil
	}
	r.resourceMu.Lock()
	defer r.resourceMu.Unlock()
	file, err := os.OpenFile(r.manifest.EffectLedger, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(args[0] + " " + args[1] + "\n")
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}

func liveHostdRebindDockerMutation(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] + " " + args[1] {
	case "network create", "network rm", "network connect", "network disconnect",
		"volume create", "volume rm", "container create", "container start",
		"container stop", "container rm":
		return true
	case "container cp":
		return len(args) == 4 && filepath.IsAbs(args[2]) && args[3] != "-" && strings.Contains(args[3], ":")
	case "container exec":
		return liveHostdRebindCommandContains(args, "reload") ||
			liveHostdRebindCommandContains(args, "mv") || liveHostdRebindCommandContains(args, "cp")
	default:
		return false
	}
}

func TestLiveHostdRebindDockerMutationClassifiesCopyDirection(t *testing.T) {
	t.Parallel()
	hostSource := filepath.Join(os.TempDir(), "rebind-config.json")
	if !liveHostdRebindDockerMutation([]string{"container", "cp", hostSource, "container-id:/config/active.json"}) {
		t.Fatal("host-to-container configuration copy was not classified as an effect")
	}
	if liveHostdRebindDockerMutation([]string{"container", "cp", "container-id:/config/.", "-"}) {
		t.Fatal("container-to-stdout attestation copy was classified as an effect")
	}
}

func liveHostdRebindLostEffectScenario(id string) bool {
	switch id {
	case "lost-network-create-ack", "lost-config-volume-create-ack", "lost-data-volume-create-ack",
		"lost-stage-container-create-ack", "lost-stage-config-copy-ack", "lost-stage-start-ack",
		"lost-final-config-copy-ack", "lost-final-container-create-ack", "lost-cutover-ack",
		"lost-successor-serving-ack":
		return true
	default:
		return false
	}
}

func (r *liveHostdRebindRunner) rememberCreatedContainer(args []string,
	result runtimeprocess.CommandResult, err error,
) {
	if err != nil || !liveHostdRebindCommandHas(args, "container", "create") {
		return
	}
	id := strings.TrimSpace(string(result.Stdout))
	if id == "" {
		return
	}
	r.resourceMu.Lock()
	defer r.resourceMu.Unlock()
	switch {
	case liveHostdRebindCommandContains(args, "io.rig.gateway-role=stage"):
		r.stageContainerID = id
	case liveHostdRebindCommandContains(args, "io.rig.gateway-role=final"):
		r.finalContainerID = id
	}
}

func (r *liveHostdRebindRunner) lostAcknowledgementCommand(scenario string, args []string) bool {
	r.resourceMu.Lock()
	stageID, finalID := r.stageContainerID, r.finalContainerID
	r.resourceMu.Unlock()
	switch scenario {
	case "lost-network-create-ack":
		return liveHostdRebindCommandHas(args, "network", "create") &&
			liveHostdRebindCommandContains(args, "io.rig.operation-id="+r.manifest.OperationID)
	case "lost-config-volume-create-ack":
		return liveHostdRebindCommandHas(args, "volume", "create") &&
			liveHostdRebindCommandContains(args, "io.rig.gateway-role=config-volume")
	case "lost-data-volume-create-ack":
		return liveHostdRebindCommandHas(args, "volume", "create") &&
			liveHostdRebindCommandContains(args, "io.rig.gateway-role=data-volume")
	case "lost-stage-container-create-ack":
		return liveHostdRebindCommandHas(args, "container", "create") &&
			liveHostdRebindCommandContains(args, "io.rig.gateway-role=stage")
	case "lost-final-container-create-ack":
		return liveHostdRebindCommandHas(args, "container", "create") &&
			liveHostdRebindCommandContains(args, "io.rig.gateway-role=final")
	case "lost-stage-config-copy-ack":
		return r.writeCopyCommand(args, stageID, 7, "stage_config_intent", "stage.json")
	case "lost-final-config-copy-ack":
		// The final configuration is deliberately installed through the
		// still-running stage container before that container is retired and
		// the stopped final container is created from the same volumes.
		return r.writeCopyCommand(args, stageID, 11, "final_config_intent", "active.json")
	case "lost-stage-start-ack":
		return liveHostdRebindContainerCommand(args, "start", stageID)
	case "lost-cutover-ack":
		return len(args) == 5 && liveHostdRebindCommandHas(args, "container", "stop", "--time", "10") &&
			stageID != "" && finalID != "" &&
			strings.TrimPrefix(args[4], "sha256:") != strings.TrimPrefix(stageID, "sha256:") &&
			strings.TrimPrefix(args[4], "sha256:") != strings.TrimPrefix(finalID, "sha256:")
	case "lost-successor-serving-ack":
		return liveHostdRebindContainerCommand(args, "start", finalID)
	case "pending-route-restart", "pending-lan-restart", "pending-lan-batch-restart":
		return liveHostdRebindCurrentConfigPublishCommand(args)
	default:
		return false
	}
}

func (r *liveHostdRebindRunner) writeCopyCommand(args []string, containerID string,
	wantSequence uint64, wantPhase, wantFilename string,
) bool {
	r.resourceMu.Lock()
	sequence, phase := r.protectedSequence, r.protectedPhase
	r.resourceMu.Unlock()
	if sequence != wantSequence || phase != wantPhase || containerID == "" || len(args) != 4 ||
		!liveHostdRebindCommandHas(args, "container", "cp") || filepath.Clean(args[2]) != args[2] ||
		!filepath.IsAbs(args[2]) || args[3] != containerID+":/config/"+wantFilename {
		return false
	}
	info, err := os.Lstat(args[2])
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func (r *liveHostdRebindRunner) stageStartCommand(args []string) bool {
	r.resourceMu.Lock()
	stageID := r.stageContainerID
	r.resourceMu.Unlock()
	return liveHostdRebindContainerCommand(args, "start", stageID)
}

func (r *liveHostdRebindRunner) ObserveGatewayRebindDurableBoundary(ctx context.Context,
	event generatedingress.GatewayRebindDurableBoundaryEvent,
) {
	if event.OperationID == r.manifest.OperationID && string(event.Boundary) == "protected_progress_readback" {
		r.resourceMu.Lock()
		r.protectedPhase, r.protectedSequence = event.ProtectedPhase, event.ProtectedSequence
		r.resourceMu.Unlock()
	}
	if r.manifest.BoundaryID == "" || event.OperationID != r.manifest.OperationID ||
		!liveHostdRebindBoundaryMatches(r.manifest.BoundaryID, event) {
		return
	}
	marker := liveHostdRebindBoundaryMarker{
		Boundary: string(event.Boundary), OperationID: event.OperationID, SQLPhase: string(event.SQLPhase),
		ProtectedPhase: event.ProtectedPhase, ProtectedGeneration: event.ProtectedGeneration,
		ProtectedSequence: event.ProtectedSequence, Digest: event.Digest,
		Disposition: string(event.Disposition),
	}
	_ = liveHostdRebindWriteExclusiveJSON(r.manifest.BoundaryMarker, marker)
	if r.manifest.ScenarioID == "cancellation-after-write" {
		<-ctx.Done()
		return
	}
	select {}
}

func (r *liveHostdRebindRunner) GatewayRebindTerminalReleaseFault(
	kind generatedingress.GatewayRebindTerminalReleaseKind,
) error {
	want := generatedingress.GatewayRebindTerminalReleaseKind("")
	switch r.manifest.ScenarioID {
	case "effects-release-fault":
		want = generatedingress.GatewayRebindTerminalReleaseEffects
	case "gateway-release-fault":
		want = generatedingress.GatewayRebindTerminalReleaseGateway
	}
	if kind == want {
		return errors.New("test-owned post-release ambiguity")
	}
	return nil
}

// This child test calls the exact production daemon assembly. The only
// injected value is one CommandRunner which delegates all commands and owns
// the package-private observer described above.
func TestLiveControllerGatewayRebindCrashMatrixChild(t *testing.T) {
	if os.Getenv(liveHostdRebindChildEnvironment) != "1" {
		return
	}
	manifest, err := liveHostdRebindReadChildManifest(os.Getenv(liveHostdRebindManifestEnvironment))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "invalid private rebind child manifest")
		os.Exit(2)
	}
	runner := &liveHostdRebindRunner{manifest: manifest}
	arguments := []string{"--data-root", manifest.DataRoot, "--listen", manifest.Listen,
		"--generated-runtime", "--github-connections=false"}
	background, backgroundCleanup, backgroundErr := liveHostdRebindBackgroundChildControls(manifest, arguments)
	if backgroundErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "invalid rebind background child control")
		os.Exit(2)
	}
	defer backgroundCleanup()
	exitCode := runServerWithAssembly(background.args, hostdDaemonAssemblyOptions{
		runner:          runner,
		backgroundStart: background.observe,
		ready: func(value hostdDaemonReady) {
			if diagnostic := background.verify(); diagnostic != "" {
				panic("rebind background child observer failed: " + diagnostic)
			}
			_ = liveHostdRebindWriteExclusiveJSON(manifest.ReadyMarker, liveHostdRebindReadyMarker{
				Address: value.Address, RecoveryOnly: value.RecoveryOnly,
				RecoveryKind: value.RecoveryKind, OperationID: value.OperationID,
			})
		},
	})
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

type liveHostdRebindChild struct {
	command  *exec.Cmd
	manifest liveHostdRebindChildManifest
	stderr   bytes.Buffer

	waitOnce sync.Once
	waitDone chan struct{}
	waitErr  error
}

func liveHostdRebindStartChild(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID, boundaryID, scenarioID string,
) *liveHostdRebindChild {
	t.Helper()
	directory, err := os.MkdirTemp(fixture.root, "hostd-rebind-child-")
	if err != nil {
		t.Fatal(err)
	}
	manifest := liveHostdRebindChildManifest{
		DataRoot: fixture.dataRoot, Listen: fixture.listen, OperationID: operationID,
		BoundaryID: boundaryID, ScenarioID: scenarioID,
		ReadyMarker:      filepath.Join(directory, "ready.json"),
		BoundaryMarker:   filepath.Join(directory, "boundary.json"),
		EffectLedger:     filepath.Join(directory, "effects.log"),
		RequestArm:       filepath.Join(directory, "request-arm.json"),
		BackgroundLedger: filepath.Join(directory, "background-starts.log"),
		NetworkLedger:    filepath.Join(directory, "background-network.log"),
		RelayLedger:      filepath.Join(directory, "background-relay-accepts.log"),
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	if err := liveHostdRebindWritePrivateJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0],
		"-test.run=^TestLiveControllerGatewayRebindCrashMatrixChild$", "-test.count=1", "-test.timeout=40m")
	command.Env = liveHostdRebindChildEnvironmentValues(manifestPath)
	child := &liveHostdRebindChild{command: command, manifest: manifest, waitDone: make(chan struct{})}
	command.Stdout = io.Discard
	command.Stderr = &child.stderr
	if err := command.Start(); err != nil {
		t.Fatal("start full hostd child:", err)
	}
	t.Cleanup(func() { liveHostdRebindCleanupChild(t, child) })
	return child
}

func (c *liveHostdRebindChild) startWait() {
	c.waitOnce.Do(func() {
		go func() {
			c.waitErr = c.command.Wait()
			close(c.waitDone)
		}()
	})
}

func liveHostdRebindKillAndReapError(child *liveHostdRebindChild) error {
	if child == nil || child.command == nil || child.command.Process == nil || child.waitDone == nil {
		return errors.New("full hostd child identity is missing")
	}
	child.startWait()
	select {
	case <-child.waitDone:
		return nil
	default:
	}
	var killErr error
	if err := child.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		killErr = fmt.Errorf("SIGKILL full hostd child: %w", err)
	}
	select {
	case <-child.waitDone:
		// On Windows a process that exited between the first wait check and
		// TerminateProcess can report access denied. Its completed Wait still
		// proves this exact child was reaped, so there is nothing left to kill.
		return nil
	case <-time.After(liveHostdRebindChildReapTimeout):
		return errors.Join(killErr, errors.New("SIGKILLed full hostd child was not reaped"))
	}
}

func liveHostdRebindKillAndReap(t *testing.T, child *liveHostdRebindChild) {
	t.Helper()
	if err := liveHostdRebindKillAndReapError(child); err != nil {
		t.Fatal(err)
	}
}

func liveHostdRebindCleanupChild(t *testing.T, child *liveHostdRebindChild) {
	t.Helper()
	if child == nil || child.command == nil || child.command.Process == nil {
		return
	}
	if err := liveHostdRebindKillAndReapError(child); err != nil {
		t.Error(err)
	}
}

func liveHostdRebindWaitReady(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	child *liveHostdRebindChild,
) liveHostdRebindReadyMarker {
	t.Helper()
	deadline, hasDeadline := t.Deadline()
	marker, err := liveHostdRebindWaitReadyResult(fixture, child, deadline, hasDeadline)
	if err != nil {
		t.Fatal(err)
	}
	return marker
}

type liveHostdRebindReadyPhase string

const (
	liveHostdRebindReadyPhaseInitial       liveHostdRebindReadyPhase = "initial"
	liveHostdRebindReadyPhaseFirstRecovery liveHostdRebindReadyPhase = "first_recovery"
	liveHostdRebindReadyPhaseThirdNormal   liveHostdRebindReadyPhase = "third_normal"
)

func validLiveHostdRebindReadyPhase(phase liveHostdRebindReadyPhase) bool {
	switch phase {
	case liveHostdRebindReadyPhaseInitial,
		liveHostdRebindReadyPhaseFirstRecovery,
		liveHostdRebindReadyPhaseThirdNormal:
		return true
	default:
		return false
	}
}

func liveHostdRebindReadyPhaseError(phase liveHostdRebindReadyPhase, err error) error {
	if !validLiveHostdRebindReadyPhase(phase) || err == nil {
		return errors.New("full hostd child readiness phase is invalid")
	}
	return fmt.Errorf("full hostd child phase=%s readiness: %w", phase, err)
}

func liveHostdRebindWaitReadyPhase(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	child *liveHostdRebindChild, phase liveHostdRebindReadyPhase,
) liveHostdRebindReadyMarker {
	t.Helper()
	if !validLiveHostdRebindReadyPhase(phase) {
		t.Fatal("full hostd child readiness phase is invalid")
	}
	deadline, hasDeadline := t.Deadline()
	marker, err := liveHostdRebindWaitReadyResult(fixture, child, deadline, hasDeadline)
	if err != nil {
		t.Fatal(liveHostdRebindReadyPhaseError(phase, err))
	}
	return marker
}

func liveHostdRebindWaitReadyResult(fixture *liveHostdGatewayRebindFixture,
	child *liveHostdRebindChild, deadline time.Time, hasDeadline bool,
) (liveHostdRebindReadyMarker, error) {
	var marker liveHostdRebindReadyMarker
	budget, err := liveHostdRebindStartupReadinessBudget(time.Now(), deadline, hasDeadline)
	if err != nil {
		return marker, liveHostdRebindReadyFailure(child, "daemon assembly", err)
	}
	ctx, cancel := context.WithTimeout(fixture.ctx, budget)
	defer cancel()
	if err := liveHostdRebindWaitJSON(ctx, child, child.manifest.ReadyMarker, &marker, "daemon assembly"); err != nil {
		return marker, liveHostdRebindReadyFailure(child, "daemon assembly", err)
	}
	if marker.Address != fixture.listen {
		return marker, liveHostdRebindReadyFailure(child, "daemon assembly",
			fmt.Errorf("full hostd ready address changed: got=%q want=%q", marker.Address, fixture.listen))
	}
	// A callback proves assembly only. The authenticated request below proves
	// the actual handler is accepting requests before this child is used.
	want := http.StatusNotFound
	if marker.RecoveryOnly && marker.RecoveryKind == "gateway_rebind" {
		want = http.StatusConflict
	} else if marker.RecoveryOnly {
		want = http.StatusServiceUnavailable
	}
	probe := uuid.NewString()
	if err := ctx.Err(); err != nil {
		return marker, liveHostdRebindReadyFailure(child, "authenticated readiness", err)
	}
	status, _, err := liveHostdRebindHTTP(ctx, fixture, http.MethodGet,
		"/api/v1/system/lan-gateway-rebind/"+probe, nil)
	if contextErr := ctx.Err(); contextErr != nil {
		return marker, liveHostdRebindReadyFailure(child, "authenticated readiness", contextErr)
	}
	if err != nil || status != want {
		return marker, liveHostdRebindReadyFailure(child, "authenticated readiness",
			fmt.Errorf("status=%d want=%d error=%v", status, want, err))
	}
	if err := ctx.Err(); err != nil {
		return marker, liveHostdRebindReadyFailure(child, "authenticated readiness", err)
	}
	return marker, nil
}

func liveHostdRebindWaitBoundary(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	child *liveHostdRebindChild,
) liveHostdRebindBoundaryMarker {
	t.Helper()
	var marker liveHostdRebindBoundaryMarker
	if err := liveHostdRebindWaitJSON(fixture.ctx, child, child.manifest.BoundaryMarker, &marker, "durable boundary"); err != nil {
		if liveHostdRebindChildWaited(child) {
			t.Fatalf("%v stderr=%q", err, liveHostdRebindClosedDiagnostic(child.stderr.String()))
		}
		t.Fatal(err)
	}
	if marker.OperationID != child.manifest.OperationID || marker.Boundary == "" {
		t.Fatalf("boundary marker is not pinned to the requested operation")
	}
	return marker
}

func liveHostdRebindWaitJSON(ctx context.Context, child *liveHostdRebindChild,
	path string, output any, purpose string,
) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("timeout waiting for %s: %w", purpose, err)
		}
		body, err := os.ReadFile(path)
		if err == nil {
			if len(body) == 0 || len(body) > 8192 || json.Unmarshal(body, output) != nil {
				return fmt.Errorf("%s marker is invalid", purpose)
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("timeout waiting for %s: %w", purpose, err)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s marker: %w", purpose, err)
		}
		child.startWait()
		select {
		case <-child.waitDone:
			return fmt.Errorf("full hostd child exited before %s: %v", purpose, child.waitErr)
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s: %w", purpose, ctx.Err())
		}
	}
}

func liveHostdRebindStartupReadinessBudget(now, deadline time.Time, hasDeadline bool) (time.Duration, error) {
	if !hasDeadline {
		return liveHostdRebindBoundedBudget(now, liveHostdRebindStartupReadinessCap)
	}
	return liveHostdRebindBoundedBudget(now, liveHostdRebindStartupReadinessCap, deadline)
}

// liveHostdRebindBoundedBudget preserves the fixture cleanup reserve for each
// known enclosing deadline. Callers may pass both the Go test deadline and an
// inherited fixture context deadline; the shortest usable budget wins.
func liveHostdRebindBoundedBudget(now time.Time, cap time.Duration, deadlines ...time.Time) (time.Duration, error) {
	if cap <= 0 {
		return 0, errors.New("invalid bounded wait cap")
	}
	budget := cap
	for _, deadline := range deadlines {
		if deadline.IsZero() {
			continue
		}
		remaining := deadline.Sub(now) - liveHostdRebindStartupCleanupReserve
		if remaining <= 0 {
			return 0, fmt.Errorf("deadline leaves no bounded wait time after %s cleanup reserve",
				liveHostdRebindStartupCleanupReserve)
		}
		if remaining < budget {
			budget = remaining
		}
	}
	return budget, nil
}

func liveHostdRebindReadyFailure(child *liveHostdRebindChild, purpose string, cause error) error {
	if err := liveHostdRebindKillAndReapError(child); err != nil {
		return fmt.Errorf("full hostd child readiness failure before %s: %v; child cleanup failed: %w",
			purpose, cause, err)
	}
	// exec.Cmd.Wait has returned before the buffer is read, so the child stderr
	// writer cannot race this closed digest and allowlist classification.
	return fmt.Errorf("full hostd child readiness failure before %s: %v stderr=%q",
		purpose, cause, liveHostdRebindClosedDiagnostic(child.stderr.String()))
}

func liveHostdRebindChildWaited(child *liveHostdRebindChild) bool {
	if child == nil || child.waitDone == nil {
		return false
	}
	select {
	case <-child.waitDone:
		return true
	default:
		return false
	}
}

func liveHostdRebindReadChildManifest(path string) (liveHostdRebindChildManifest, error) {
	var manifest liveHostdRebindChildManifest
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Ext(path) != ".json" {
		return manifest, errors.New("invalid manifest path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return manifest, errors.New("unsafe manifest permissions")
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 8192 || json.Unmarshal(body, &manifest) != nil ||
		!liveHostdRebindSafeRoot(manifest.DataRoot) || !liveHostdRebindSafeAddress(manifest.Listen) ||
		!liveHostdRebindSafeMarker(manifest.ReadyMarker, filepath.Dir(path), "ready.json") ||
		!liveHostdRebindSafeMarker(manifest.BoundaryMarker, filepath.Dir(path), "boundary.json") ||
		!liveHostdRebindSafeMarker(manifest.EffectLedger, filepath.Dir(path), "effects.log") ||
		!liveHostdRebindSafeMarker(manifest.RequestArm, filepath.Dir(path), "request-arm.json") ||
		!liveHostdRebindSafeMarker(manifest.BackgroundLedger, filepath.Dir(path), "background-starts.log") ||
		!liveHostdRebindSafeMarker(manifest.NetworkLedger, filepath.Dir(path), "background-network.log") ||
		!liveHostdRebindSafeMarker(manifest.RelayLedger, filepath.Dir(path), "background-relay-accepts.log") ||
		(manifest.OperationID != "" && uuid.Validate(manifest.OperationID) != nil) ||
		(manifest.BoundaryID != "" && !liveHostdRebindKnownBoundary(manifest.BoundaryID)) ||
		(manifest.ScenarioID != "" && !liveHostdRebindKnownScenario(manifest.ScenarioID)) {
		return liveHostdRebindChildManifest{}, errors.New("invalid manifest")
	}
	return manifest, nil
}

func liveHostdRebindWritePrivateJSON(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > 8192 {
		return errors.New("invalid private JSON")
	}
	return os.WriteFile(path, body, 0o600)
}

func liveHostdRebindWriteExclusiveJSON(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > 8192 {
		return errors.New("invalid marker JSON")
	}
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	// A hard link publishes the already closed and synced inode atomically and
	// fails if another writer has published this one-shot marker.
	if err = os.Link(temporary, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func liveHostdRebindSafeRoot(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Base(path) == "hostd-supported-rebind"
}

func liveHostdRebindSafeAddress(value string) bool {
	if !strings.HasPrefix(value, "127.0.0.1:") {
		return false
	}
	port, err := strconv.Atoi(strings.TrimPrefix(value, "127.0.0.1:"))
	return err == nil && port > 0 && port <= 65535
}

func liveHostdRebindSafeMarker(path, parent, base string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Dir(path) == parent &&
		filepath.Base(path) == base
}

func liveHostdRebindChildEnvironmentValues(manifest string) []string {
	result := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, liveHostdRebindChildEnvironment+"=") ||
			strings.HasPrefix(value, liveHostdRebindManifestEnvironment+"=") {
			continue
		}
		result = append(result, value)
	}
	return append(result, liveHostdRebindChildEnvironment+"=1", liveHostdRebindManifestEnvironment+"="+manifest)
}

func liveHostdRebindKnownBoundary(id string) bool {
	for _, entry := range gatewayRebindLiveBoundaries {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func liveHostdRebindKnownScenario(id string) bool {
	for _, entry := range gatewayRebindLiveScenarios {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func liveHostdRebindBoundaryMatches(id string, event generatedingress.GatewayRebindDurableBoundaryEvent) bool {
	boundary := string(event.Boundary)
	phase := string(event.ProtectedPhase)
	switch id {
	case "a0-sql-claim":
		return boundary == "sql_claim_readback" && event.SQLPhase == "prepared"
	case "p0-predecessor-checkpoint":
		return boundary == "protected_checkpoint_readback"
	case "p1-protected-intent":
		return boundary == "protected_intent_readback"
	case "r01-rollback-intent":
		return boundary == "protected_progress_readback" && phase == "rollback_intent"
	case "r02-rollback-completed":
		return boundary == "protected_progress_readback" && phase == "handover_rolled_back"
	case "s01-protected-terminal-installed":
		return boundary == "protected_terminal_installed" && event.SQLPhase == ""
	case "s02-protected-terminal-readback":
		return boundary == "protected_terminal_readback"
	case "s03-successor-ready":
		return boundary == "sql_transition_readback" && event.SQLPhase == "successor_ready"
	case "s04-database-committed":
		return boundary == "sql_transition_readback" && event.SQLPhase == "database_committed"
	case "s05-current-attestation":
		return boundary == "current_attestation" && event.SQLPhase == "database_committed"
	case "s06-terminal-sql":
		return boundary == "sql_transition_readback" && event.SQLPhase == "committed"
	case "s07-stable-final-census":
		return boundary == "final_census_readback" && event.SQLPhase == "committed"
	case "s08-fence-transition":
		return boundary == "fence_clear_readback" && event.SQLPhase == "committed"
	case "s09-effects-lease-release":
		return boundary == "effects_release" && event.SQLPhase == "committed"
	case "s10-gateway-lock-release", "s11-response-loss":
		return boundary == "gateway_release" && event.SQLPhase == "committed"
	}
	if strings.HasPrefix(id, "p") && len(id) >= 3 {
		sequence, err := strconv.Atoi(id[1:3])
		return err == nil && boundary == "protected_progress_readback" &&
			event.ProtectedSequence == uint64(sequence) && phase == liveHostdRebindProgressPhase(uint64(sequence))
	}
	return false
}

func liveHostdRebindProgressPhase(sequence uint64) string {
	return map[uint64]string{
		1: "successor_intent", 2: "stage_intent", 3: "stage_intent", 4: "stage_intent",
		5: "stage_intent", 6: "stage_intent", 7: "stage_config_intent", 8: "stage_config_copied",
		9: "stage_start_intent", 10: "stage_serving", 11: "final_config_intent",
		12: "final_config_copied", 13: "final_handover_intent", 14: "final_container_bound",
		15: "cutover_intent", 16: "successor_serving", 17: "handover_committed",
	}[sequence]
}

func liveHostdRebindCommandHas(args []string, values ...string) bool {
	if len(args) < len(values) {
		return false
	}
	for index, value := range values {
		if args[index] != value {
			return false
		}
	}
	return true
}

func liveHostdRebindCommandContains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func liveHostdRebindContainerCommand(args []string, action, id string) bool {
	if id == "" || !liveHostdRebindCommandHas(args, "container", action) {
		return false
	}
	for _, arg := range args[2:] {
		if strings.TrimPrefix(arg, "sha256:") == strings.TrimPrefix(id, "sha256:") {
			return true
		}
	}
	return false
}

func liveHostdRebindCurrentConfigPublishCommand(args []string) bool {
	return liveHostdRebindCommandHas(args, "container", "exec") &&
		liveHostdRebindCommandContains(args, "mv") &&
		liveHostdRebindCommandContains(args, "/config/active.next.json")
}

func liveHostdRebindHTTP(ctx context.Context, fixture *liveHostdGatewayRebindFixture,
	method, path string, input any,
) (int, []byte, error) {
	if input == nil {
		return liveHostdRebindHTTPBody(ctx, fixture, method, path, nil, false)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	return liveHostdRebindHTTPBody(ctx, fixture, method, path, encoded, true)
}

// liveHostdRebindHTTPRaw sends the exact previously reviewed commit request.
// It exists only for restart replay; callers must not use it to synthesize a
// new approval-bearing request.
func liveHostdRebindHTTPRaw(ctx context.Context, fixture *liveHostdGatewayRebindFixture,
	method, path string, request []byte,
) (int, []byte, error) {
	return liveHostdRebindHTTPBody(ctx, fixture, method, path, request, true)
}

func liveHostdRebindHTTPBody(ctx context.Context, fixture *liveHostdGatewayRebindFixture,
	method, path string, requestBody []byte, hasBody bool,
) (int, []byte, error) {
	var body io.Reader
	if hasBody {
		body = bytes.NewReader(requestBody)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+fixture.listen+path, body)
	if err != nil {
		return 0, nil, err
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: fixture.session.Token})
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", fixture.session.CSRF)
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	return response.StatusCode, contents, err
}

func liveHostdRebindClosedDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "empty"
	}
	sum := sha256.Sum256([]byte(value))
	diagnostic := "sha256:" + hex.EncodeToString(sum[:])
	if code := liveHostdRebindStartupDiagnosticCode(value); code != "" {
		diagnostic += " startup=" + code
	}
	if code := liveHostdRebindRoutePauseDiagnosticCode(value); code != "" {
		diagnostic += " route_pause=" + code
	}
	return diagnostic
}

// liveHostdRebindRoutePauseDiagnosticCode accepts only the fixed executor
// message and a closed stage/cause pair. It examines a bounded stderr suffix
// because route reconciliation follows successful daemon startup, and it
// returns only the newest valid pair. Every other field is ignored.
func liveHostdRebindRoutePauseDiagnosticCode(stderr string) string {
	limited := []byte(stderr)
	if len(limited) > liveHostdRebindRoutePauseMaxBytes {
		limited = limited[len(limited)-liveHostdRebindRoutePauseMaxBytes:]
		newline := bytes.IndexByte(limited, '\n')
		if newline < 0 {
			return ""
		}
		limited = limited[newline+1:]
	}
	limited = bytes.TrimSpace(limited)
	for records := 0; len(limited) > 0 && records < liveHostdRebindRoutePauseMaxRecords; records++ {
		line := limited
		if newline := bytes.LastIndexByte(limited, '\n'); newline >= 0 {
			line, limited = limited[newline+1:], limited[:newline]
		} else {
			limited = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if stage, cause, ok := liveHostdRebindRoutePauseRecord(line); ok {
			return stage + "/" + cause
		}
	}
	return ""
}

func liveHostdRebindRoutePauseRecord(line []byte) (string, string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	opening, err := decoder.Token()
	openingDelimiter, isDelimiter := opening.(json.Delim)
	if err != nil || !isDelimiter || openingDelimiter != '{' {
		return "", "", false
	}
	var message, stage, cause string
	var seenMessage, seenStage, seenCause bool
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		key, isString := keyToken.(string)
		if keyErr != nil || !isString {
			return "", "", false
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return "", "", false
		}
		switch key {
		case "msg":
			if seenMessage {
				return "", "", false
			}
			seenMessage = true
			var valid bool
			message, valid = liveHostdRebindRoutePauseString(raw)
			if !valid {
				return "", "", false
			}
		case "route_pause_stage":
			if seenStage {
				return "", "", false
			}
			seenStage = true
			var valid bool
			stage, valid = liveHostdRebindRoutePauseString(raw)
			if !valid {
				return "", "", false
			}
		case "route_pause_cause":
			if seenCause {
				return "", "", false
			}
			seenCause = true
			var valid bool
			cause, valid = liveHostdRebindRoutePauseString(raw)
			if !valid {
				return "", "", false
			}
		default:
			if strings.EqualFold(key, "msg") || strings.EqualFold(key, "route_pause_stage") ||
				strings.EqualFold(key, "route_pause_cause") {
				return "", "", false
			}
		}
	}
	closing, closeErr := decoder.Token()
	closingDelimiter, isDelimiter := closing.(json.Delim)
	if closeErr != nil || !isDelimiter || closingDelimiter != '}' {
		return "", "", false
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF || !seenMessage || !seenStage || !seenCause ||
		message != "generated route reconciliation paused" || !liveHostdRebindKnownRoutePausePair(stage, cause) {
		return "", "", false
	}
	return stage, cause, true
}

func liveHostdRebindRoutePauseString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func liveHostdRebindKnownRoutePausePair(stage, cause string) bool {
	if cause == "unknown" {
		switch stage {
		case "runtime_begin", "runtime_success_head", "route_switch", "previous_retirement", "runtime_success_advance":
			return true
		default:
			return false
		}
	}
	stateCause := cause == "canceled" || cause == "deadline_exceeded" || cause == "state_not_found" ||
		cause == "invalid_state" || cause == "invalid_transition" || cause == "state_conflict"
	switch stage {
	case "runtime_begin":
		return cause == "deployment_in_progress"
	case "runtime_success_head":
		return cause == "active_head_mismatch" || stateCause
	case "route_switch":
		return cause == "gateway_readiness_failed" || stateCause
	case "previous_retirement":
		return cause == "retirement_unavailable" || cause == "candidate_cleanup_failed" || stateCause ||
			liveHostdRebindKnownPreviousRetirementIdentityCause(cause)
	case "runtime_success_advance":
		return stateCause
	default:
		return false
	}
}

func liveHostdRebindKnownPreviousRetirementIdentityCause(cause string) bool {
	switch cause {
	case "invalid_claim",
		"invalid_configuration",
		"scope_unavailable",
		"authority_stale",
		"authority_invalid",
		"authority_unresolved",
		"identity_lost",
		"stage_invalid",
		"positive_fence",
		"storage_unavailable",
		"route_unresolved",
		"candidate_observation_failed",
		"accounting_unresolved",
		"identity_ambiguous":
		return true
	default:
		return false
	}
}

// liveHostdRebindStartupDiagnosticCode accepts only the fixed slog message
// values emitted before the full daemon signals readiness. It examines the
// error value only for the exact generated-domain construction message and
// accepts that value only through the closed construction diagnostic grammar.
// Every other error field remains ignored. A later emergency-stop record
// therefore cannot replace the first primary startup failure.
func liveHostdRebindStartupDiagnosticCode(stderr string) string {
	if len(stderr) > liveHostdRebindStartupDiagnosticMaxBytes {
		stderr = stderr[:liveHostdRebindStartupDiagnosticMaxBytes]
	}
	limited := []byte(stderr)
	var secondary string
	for records := 0; len(limited) > 0 && records < liveHostdRebindStartupDiagnosticMaxRecords; records++ {
		line := limited
		if newline := bytes.IndexByte(limited, '\n'); newline >= 0 {
			line, limited = limited[:newline], limited[newline+1:]
		} else {
			limited = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if code, handled := liveHostdRebindConstructionDiagnosticCode(line); handled {
			if code != "" {
				return code
			}
			continue
		}
		var record struct {
			Message           string          `json:"msg"`
			StartupCheckpoint json.RawMessage `json:"startup_checkpoint"`
			StartupCause      json.RawMessage `json:"startup_cause"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		code, isPrimary, ok := liveHostdRebindKnownStartupMessage(record.Message)
		if ok && record.Message == "gateway startup inspection failed" {
			code = liveHostdRebindGatewayStartupDiagnosticCode(record.StartupCheckpoint)
			if cause := liveHostdRebindGatewayStartupSnapshotCause(code, record.StartupCause); cause != "" {
				code += " cause=" + cause
			}
		}
		if !ok {
			continue
		}
		if isPrimary {
			return code
		}
		if secondary == "" {
			secondary = code
		}
	}
	return secondary
}

// liveHostdRebindConstructionDiagnosticCode treats only the exact production
// construction-failure record as authoritative. Relevant duplicate or
// case-folded keys refuse the complete record rather than inheriting the JSON
// package's last-value-wins behavior. Other slog fields remain ignorable.
func liveHostdRebindConstructionDiagnosticCode(line []byte) (string, bool) {
	const message = "generated domain construction failed"
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	delimiter, ok := token.(json.Delim)
	if err != nil || !ok || delimiter != '{' {
		return "", false
	}
	var recordMessage, recordError string
	var seenMessage, seenError, constructionRecord, invalid bool
	for decoder.More() {
		token, err = decoder.Token()
		key, keyOK := token.(string)
		if err != nil || !keyOK {
			return "", constructionRecord
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return "", constructionRecord
		}
		switch {
		case strings.EqualFold(key, "msg"):
			if key != "msg" || seenMessage {
				invalid = true
			}
			seenMessage = true
			value, valid := controllerChildStartupDiagnosticString(raw)
			if !valid {
				invalid = true
				continue
			}
			recordMessage = value
			constructionRecord = constructionRecord || value == message
		case strings.EqualFold(key, "error"):
			if key != "error" || seenError {
				invalid = true
			}
			seenError = true
			value, valid := controllerChildStartupDiagnosticString(raw)
			if !valid {
				invalid = true
				continue
			}
			recordError = value
		}
	}
	closing, err := decoder.Token()
	delimiter, ok = closing.(json.Delim)
	if err != nil || !ok || delimiter != '}' {
		return "", constructionRecord
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF {
		return "", constructionRecord
	}
	if !constructionRecord {
		return "", false
	}
	if invalid || !seenMessage || !seenError || recordMessage != message {
		return "", true
	}
	stage, reason, valid := controllerChildConstructionDiagnosticText(recordError)
	if !valid {
		return "", true
	}
	return "construction=" + stage + "/" + reason, true
}

// liveHostdRebindGatewayStartupDiagnosticCode accepts the checkpoint only on
// the exact main-gate message and only through the production closed mapping.
// It deliberately does not examine the child error field or infer a value
// from checkpoint syntax.
func liveHostdRebindGatewayStartupDiagnosticCode(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "gateway_inspection"
	}
	checkpoint, ok := gatewayStartupDiagnosticCheckpointFromString(value)
	if !ok {
		return "gateway_inspection"
	}
	return string(checkpoint)
}

// liveHostdRebindGatewayStartupSnapshotCause accepts a cause only for the
// exact snapshot checkpoints. A malformed, unknown, or broad/non-snapshot
// pairing deliberately leaves the pre-existing stage diagnostic unchanged.
func liveHostdRebindGatewayStartupSnapshotCause(code string, raw json.RawMessage) string {
	checkpoint, ok := gatewayStartupDiagnosticCheckpointFromString(code)
	if !ok || !gatewayStartupDiagnosticSnapshotCheckpoint(checkpoint) {
		return ""
	}
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	cause, ok := gatewayStartupDiagnosticCauseFromString(value)
	if !ok || cause == gatewayStartupDiagnosticCauseUnknown {
		return ""
	}
	return string(cause)
}

// liveHostdRebindKnownStartupMessage is intentionally a closed mapping of
// exact message literals in cmd/hostd/main.go. Do not derive codes from text
// supplied by the child or add arbitrary structured-log attributes here.
func liveHostdRebindKnownStartupMessage(message string) (code string, primary bool, ok bool) {
	switch message {
	case "controller listener reservation failed":
		return "listener_reservation", true, true
	case "controller owner directory setup failed":
		return "owner_directory", true, true
	case "controller data root ownership failed":
		return "owner_lease", true, true
	case "Docker executable resolution failed":
		return "docker_resolution", true, true
	case "database startup failed":
		return "database", true, true
	case "gateway rebind startup preparation failed":
		return "rebind_preparation", true, true
	case "gateway rebind startup returned a refusing decision":
		return "rebind_refusal", true, true
	case "gateway rebind recovery startup decision failed validation":
		return "rebind_recovery_validation", true, true
	case "recovery controller authentication setup failed":
		return "recovery_authentication", true, true
	case "deployment effects admission setup failed":
		return "effects_admission_setup", true, true
	case "deployment effects startup admission failed":
		return "effects_admission", true, true
	case "deployment effects startup admission returned no release":
		return "effects_admission_release", true, true
	case "gateway startup inspection failed":
		return "gateway_inspection", true, true
	case "LAN access startup quarantine failed":
		return "lan_quarantine", true, true
	case "controller authentication setup failed":
		return "authentication", true, true
	case "LAN gateway rebind fence changed before controller startup":
		return "controller_fence", true, true
	case "local machine setup failed":
		return "local_machine", true, true
	case "local diagnostics persistence failed":
		return "diagnostics", true, true
	case "application configuration setup failed":
		return "configuration_setup", true, true
	case "application configuration recovery failed":
		return "configuration_recovery", true, true
	case "GitHub App configuration failed":
		return "github_configuration", true, true
	case "release snapshot configuration failed":
		return "snapshot_setup", true, true
	case "release snapshot recovery failed":
		return "snapshot_recovery", true, true
	case "deployment plan storage setup failed":
		return "plan_setup", true, true
	case "deployment plan recovery failed":
		return "plan_recovery", true, true
	case "runtime composition failed":
		return "runtime_composition", true, true
	case "hosting gateway startup claim snapshot changed":
		return "gateway_snapshot_changed", true, true
	case "gateway startup inspection changed after recovery":
		return "gateway_inspection_changed", true, true
	case "LAN access startup inspection changed after recovery":
		return "lan_inspection_changed", true, true
	case "LAN gateway rebind fence changed before worker start":
		return "worker_fence", true, true
	case "runtime recovery failed":
		return "runtime_recovery", true, true
	case "generated auto-deploy preflight setup failed":
		return "autodeploy_preflight", true, true
	case "deployment effects startup admission release failed":
		return "effects_admission_release_cleanup", false, true
	case "deployment effects startup admission release failed before recovery controller":
		return "effects_admission_release_before_recovery", true, true
	case "owned gateway emergency stop could not be verified":
		return "emergency_stop", false, true
	case "controller data root ownership release failed":
		return "owner_lease_release", false, true
	}
	return "", false, false
}

type liveHostdRebindReview struct {
	Version                 int             `json:"version"`
	ProposalDigest          string          `json:"proposalDigest"`
	ConfigureApprovalDigest string          `json:"configureApprovalDigest"`
	RebindApprovalDigest    string          `json:"rebindApprovalDigest"`
	Spec                    json.RawMessage `json:"spec"`
}

type liveHostdRebindStatus struct {
	Version         int                                           `json:"version"`
	OperationID     string                                        `json:"operationId"`
	Availability    string                                        `json:"availability"`
	Active          bool                                          `json:"active"`
	Historical      bool                                          `json:"historicalTerminal"`
	TerminalProof   string                                        `json:"terminalProof"`
	CurrentProof    string                                        `json:"currentProof"`
	OperationFence  string                                        `json:"operationFence"`
	RecoveryPolicy  string                                        `json:"recoveryPolicy,omitempty"`
	AllowedActions  []string                                      `json:"allowedActions"`
	TerminalDigest  string                                        `json:"terminalProofDigest,omitempty"`
	CurrentDigest   string                                        `json:"currentAttestationDigest,omitempty"`
	SelectedCurrent *apicontract.LANGatewayRebindCurrentAuthority `json:"selectedCurrentAuthority,omitempty"`
	Operation       *apicontract.LANGatewayRebindOperation        `json:"operation,omitempty"`
}

type liveHostdRebindRecoveryReview struct {
	OperationID            string                `json:"operationId"`
	Version                int                   `json:"version"`
	Status                 liveHostdRebindStatus `json:"status"`
	RecoveryDigest         string                `json:"recoveryDigest,omitempty"`
	RecoveryApprovalDigest string                `json:"recoveryApprovalDigest,omitempty"`
}

// liveHostdRebindSuccessorDraft is intentionally only the private request
// identity for a rebind successor. It is not a persisted or approved gateway
// profile revision: the rebind claim retains this identity together with the
// reviewed configure approval after it has checked the current profile under
// the writer reservation.
type liveHostdRebindSuccessorDraft struct {
	RevisionID     string
	RevisionNumber int64
	OperationID    string
	Spec           apicontract.LANGatewayProfileSpec
}

const liveHostdRebindMaxRevisionNumber = int64(1<<63 - 1)

// liveHostdRebindPlanSuccessor reads the current profile and prepares a
// synthetic successor identity for the existing rebind proposal/commit flow.
// It must not advance the ordinary profile head because active LAN bindings
// deliberately pin that head until the rebind is admitted.
func liveHostdRebindPlanSuccessor(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	target apicontract.LANGatewayProfileSpec,
) liveHostdRebindSuccessorDraft {
	t.Helper()
	draft, err := liveHostdRebindPlanSuccessorProfile(fixture, target)
	if err != nil {
		t.Fatalf("plan successor profile: %v", err)
	}
	return draft
}

func liveHostdRebindPlanSuccessorProfile(fixture *liveHostdGatewayRebindFixture,
	target apicontract.LANGatewayProfileSpec,
) (liveHostdRebindSuccessorDraft, error) {
	if fixture == nil || fixture.ctx == nil {
		return liveHostdRebindSuccessorDraft{}, errors.New("successor fixture is unavailable")
	}
	query := fmt.Sprintf("?interfaceId=%s&selectedIpv4=%s&portStart=%d&portEnd=%d",
		urlQuery(target.InterfaceID), urlQuery(target.SelectedIpv4), target.PortStart, target.PortEnd)
	status, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
		"/api/v1/system/lan-gateway-profile"+query, nil)
	if err != nil {
		return liveHostdRebindSuccessorDraft{}, fmt.Errorf("read successor profile: %w", err)
	}
	if status != http.StatusOK {
		return liveHostdRebindSuccessorDraft{}, fmt.Errorf("read successor profile: status=%d", status)
	}
	var read apicontract.LANGatewayProfileRead
	if json.Unmarshal(body, &read) != nil {
		return liveHostdRebindSuccessorDraft{}, errors.New("read successor profile: invalid response")
	}
	if read.DesiredProfile == nil || read.Proposal == nil || read.DesiredProfile.RevisionNumber <= 0 ||
		read.ExpectedRevisionNumber != read.DesiredProfile.RevisionNumber ||
		read.DesiredProfile.RevisionNumber == liveHostdRebindMaxRevisionNumber ||
		read.Proposal.Spec != target {
		return liveHostdRebindSuccessorDraft{}, errors.New("successor profile state is inconsistent")
	}
	return liveHostdRebindSuccessorDraft{
		RevisionID: uuid.NewString(), RevisionNumber: read.DesiredProfile.RevisionNumber + 1,
		OperationID: uuid.NewString(), Spec: target,
	}, nil
}

func liveHostdRebindPrepareRequest(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID string, successor liveHostdRebindSuccessorDraft,
) map[string]any {
	t.Helper()
	inspect := map[string]any{
		"operationId": operationID, "successorProfileRevisionId": successor.RevisionID,
		"successorProfileRevisionNumber": successor.RevisionNumber,
		"successorProfileOperationId":    successor.OperationID, "successorProfile": successor.Spec,
	}
	status, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind/proposal", inspect)
	var review liveHostdRebindReview
	if err != nil || status != http.StatusOK || json.Unmarshal(body, &review) != nil || review.Version != 1 ||
		review.ProposalDigest == "" || review.ConfigureApprovalDigest == "" || review.RebindApprovalDigest == "" {
		t.Fatalf("inspect rebind: status=%d error=%v", status, err)
	}
	inspect["version"] = review.Version
	inspect["proposalDigest"] = review.ProposalDigest
	inspect["configureApprovalDigest"] = review.ConfigureApprovalDigest
	inspect["rebindApprovalDigest"] = review.RebindApprovalDigest
	request, marshalErr := json.Marshal(inspect)
	if marshalErr != nil {
		t.Fatal("marshal reviewed rebind commit request")
	}
	if retainErr := fixture.retainCommitRequest(operationID, request); retainErr != nil {
		t.Fatalf("retain reviewed rebind commit request: %v", retainErr)
	}
	return inspect
}

// liveHostdRebindNormalReplay reads the diagnostic startup view and replays
// only the exact reviewed commit request retained before the child stopped.
// The diagnostic response remains unavailable by design; it becomes usable
// terminal evidence only after the commit facade independently re-proves it.
func liveHostdRebindNormalReplay(fixture *liveHostdGatewayRebindFixture,
	operationID string,
) (liveHostdRebindStatus, error) {
	diagnostic, replay, err := liveHostdRebindNormalReplayExchange(fixture, operationID)
	if err != nil {
		return liveHostdRebindStatus{}, err
	}
	if err := liveHostdRebindValidateNormalReplay(diagnostic, replay, operationID); err != nil {
		return liveHostdRebindStatus{}, err
	}
	return liveHostdRebindStatusFromReplay(diagnostic, replay), nil
}

func liveHostdRebindNormalReplayExchange(fixture *liveHostdGatewayRebindFixture,
	operationID string,
) (liveHostdRebindStatus, apicontract.LANGatewayRebindMutation, error) {
	if fixture == nil || fixture.ctx == nil || operationID == "" {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{},
			errors.New("normal rebind replay is unavailable")
	}
	path := "/api/v1/system/lan-gateway-rebind/" + operationID
	statusCode, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet, path, nil)
	if err != nil || statusCode != http.StatusOK {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{},
			errors.New("normal rebind diagnostic read failed")
	}
	var diagnostic liveHostdRebindStatus
	if json.Unmarshal(body, &diagnostic) != nil {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{},
			errors.New("normal rebind diagnostic response is invalid")
	}
	if err := liveHostdRebindValidateNormalReplayDiagnostic(diagnostic, operationID); err != nil {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{}, err
	}
	request, ok := fixture.retainedCommitRequest(operationID)
	if !ok {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{},
			errors.New("normal rebind replay request is unavailable")
	}
	statusCode, body, err = liveHostdRebindHTTPRaw(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind", request)
	if err != nil || statusCode != http.StatusOK {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{},
			liveHostdRebindNormalReplayFailure(statusCode, body, err)
	}
	var replay apicontract.LANGatewayRebindMutation
	if json.Unmarshal(body, &replay) != nil {
		return liveHostdRebindStatus{}, apicontract.LANGatewayRebindMutation{},
			errors.New("normal rebind replay response is invalid")
	}
	return diagnostic, replay, nil
}

func liveHostdRebindStatusFromReplay(diagnostic liveHostdRebindStatus,
	replay apicontract.LANGatewayRebindMutation,
) liveHostdRebindStatus {
	verified := diagnostic
	verified.TerminalProof = replay.TerminalProof
	verified.TerminalDigest = replay.TerminalProofDigest
	verified.CurrentProof = replay.CurrentProof
	verified.CurrentDigest = replay.CurrentAttestationDigest
	operation := replay.Operation
	verified.Operation = &operation
	authority := *replay.SelectedCurrentAuthority
	verified.SelectedCurrent = &authority
	return verified
}

// liveHostdRebindNormalReplayAfterCurrentRevision is the route-only seam for
// replay after an ordinary current-route recovery advanced the protected
// revision. It accepts only the production-defined superseded-terminal proof
// paired with a newly verified current attestation and an earlier fully
// verified committed baseline for the exact operation and authority.
func liveHostdRebindNormalReplayAfterCurrentRevision(fixture *liveHostdGatewayRebindFixture,
	operationID string, baseline liveHostdRebindStatus,
) (liveHostdRebindStatus, error) {
	if !liveHostdRebindValidNormalReplayBaseline(baseline, operationID) {
		return liveHostdRebindStatus{}, liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentBaseline)
	}
	diagnostic, replay, err := liveHostdRebindNormalReplayExchange(fixture, operationID)
	if err != nil {
		return liveHostdRebindStatus{}, err
	}
	if err := liveHostdRebindValidateLaterCurrentReplay(diagnostic, replay, operationID, baseline); err != nil {
		return liveHostdRebindStatus{}, err
	}
	return liveHostdRebindStatusFromReplay(diagnostic, replay), nil
}

type liveHostdRebindLaterCurrentCategory string

const (
	liveHostdRebindLaterCurrentBaseline      liveHostdRebindLaterCurrentCategory = "baseline"
	liveHostdRebindLaterCurrentFlags         liveHostdRebindLaterCurrentCategory = "flags"
	liveHostdRebindLaterCurrentTerminalProof liveHostdRebindLaterCurrentCategory = "terminal_proof"
	liveHostdRebindLaterCurrentCurrentProof  liveHostdRebindLaterCurrentCategory = "current_proof"
	liveHostdRebindLaterCurrentOperation     liveHostdRebindLaterCurrentCategory = "operation"
	liveHostdRebindLaterCurrentAuthority     liveHostdRebindLaterCurrentCategory = "authority"
	liveHostdRebindLaterCurrentIdentity      liveHostdRebindLaterCurrentCategory = "identity"
)

func liveHostdRebindValidNormalReplayBaseline(status liveHostdRebindStatus, operationID string) bool {
	return status.Version == generatedingress.GatewayRebindOperationInspectionVersion &&
		status.OperationID == operationID &&
		status.Availability == string(generatedingress.GatewayRebindOperationAvailable) &&
		!status.Active && status.Historical && status.TerminalProof == "verified" &&
		liveHostdRebindValidDigest(status.TerminalDigest) && status.CurrentProof == "verified" &&
		liveHostdRebindValidDigest(status.CurrentDigest) && status.OperationFence == "released" &&
		status.RecoveryPolicy == "" && len(status.AllowedActions) == 0 &&
		liveHostdRebindValidOperation(status.Operation, operationID) &&
		status.Operation.TerminalDisposition == "commit" &&
		liveHostdRebindValidAuthority(status.SelectedCurrent) &&
		status.SelectedCurrent.OperationID == operationID
}

func liveHostdRebindValidateLaterCurrentReplay(diagnostic liveHostdRebindStatus,
	replay apicontract.LANGatewayRebindMutation, operationID string, baseline liveHostdRebindStatus,
) error {
	if replay.Created || replay.RecoveryRequired || !replay.FenceReleased {
		return liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentFlags)
	}
	if replay.TerminalProof != "unavailable" || replay.TerminalProofDigest != "" {
		return liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentTerminalProof)
	}
	if replay.CurrentProof != "verified" || !liveHostdRebindValidDigest(replay.CurrentAttestationDigest) {
		return liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentCurrentProof)
	}
	if !liveHostdRebindValidOperation(&replay.Operation, operationID) {
		return liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentOperation)
	}
	if !liveHostdRebindValidAuthority(replay.SelectedCurrentAuthority) {
		return liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentAuthority)
	}
	if diagnostic.Operation == nil || diagnostic.SelectedCurrent == nil || baseline.Operation == nil ||
		baseline.SelectedCurrent == nil || *diagnostic.Operation != replay.Operation ||
		*baseline.Operation != replay.Operation ||
		*diagnostic.SelectedCurrent != *replay.SelectedCurrentAuthority ||
		*baseline.SelectedCurrent != *replay.SelectedCurrentAuthority {
		return liveHostdRebindLaterCurrentRefusal(liveHostdRebindLaterCurrentIdentity)
	}
	return nil
}

func liveHostdRebindLaterCurrentRefusal(category liveHostdRebindLaterCurrentCategory) error {
	return errors.New("normal rebind later-current refusal: " + string(category))
}

// The retained request and response may contain authority or private state.
// Classify only fixed public status/code pairs; never echo body or error text.
func liveHostdRebindNormalReplayFailure(statusCode int, body []byte, transportErr error) error {
	outcome := "problem_other"
	if transportErr != nil {
		switch {
		case errors.Is(transportErr, context.Canceled):
			outcome = "transport_canceled"
		case errors.Is(transportErr, context.DeadlineExceeded):
			outcome = "transport_deadline_exceeded"
		default:
			outcome = "transport_error"
		}
	} else {
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &problem) == nil {
			switch {
			case statusCode == http.StatusServiceUnavailable && problem.Code == "lan_gateway_unavailable":
				outcome = "gateway_unavailable"
			case statusCode == http.StatusServiceUnavailable && problem.Code == "capability_unavailable":
				outcome = "capability_unavailable"
			case statusCode == http.StatusConflict && problem.Code == "lan_gateway_conflict":
				outcome = "conflict"
			case statusCode == http.StatusForbidden && problem.Code == "lan_gateway_approval_mismatch":
				outcome = "approval_mismatch"
			case statusCode == http.StatusForbidden && problem.Code == "lan_gateway_forbidden":
				outcome = "forbidden"
			case statusCode == http.StatusBadRequest && problem.Code == "invalid_lan_gateway_request":
				outcome = "invalid_request"
			}
		}
	}
	return fmt.Errorf("normal rebind replay failed: status=%d outcome=%s", statusCode, outcome)
}

func liveHostdRebindValidateNormalReplayDiagnostic(status liveHostdRebindStatus, operationID string) error {
	if status.Version != generatedingress.GatewayRebindOperationInspectionVersion || status.OperationID != operationID ||
		status.Availability != string(generatedingress.GatewayRebindOperationAvailable) || status.Active || !status.Historical ||
		status.TerminalProof != "unavailable" || status.TerminalDigest != "" || status.CurrentProof != "unavailable" ||
		status.CurrentDigest != "" || status.OperationFence != "released" || status.RecoveryPolicy != "" ||
		len(status.AllowedActions) != 0 || !liveHostdRebindValidOperation(status.Operation, operationID) ||
		!liveHostdRebindValidAuthority(status.SelectedCurrent) {
		return errors.New("normal rebind diagnostic is not an unavailable terminal")
	}
	return nil
}

func liveHostdRebindValidateNormalReplay(diagnostic liveHostdRebindStatus,
	replay apicontract.LANGatewayRebindMutation, operationID string,
) error {
	if replay.Created || replay.RecoveryRequired || !replay.FenceReleased || replay.TerminalProof != "verified" ||
		replay.CurrentProof != "verified" || !liveHostdRebindValidDigest(replay.TerminalProofDigest) ||
		!liveHostdRebindValidDigest(replay.CurrentAttestationDigest) ||
		!liveHostdRebindValidOperation(&replay.Operation, operationID) ||
		!liveHostdRebindValidAuthority(replay.SelectedCurrentAuthority) {
		return errors.New("normal rebind replay proof is incomplete")
	}
	if diagnostic.Operation == nil || *diagnostic.Operation != replay.Operation || diagnostic.SelectedCurrent == nil ||
		*diagnostic.SelectedCurrent != *replay.SelectedCurrentAuthority {
		return errors.New("normal rebind replay identity changed")
	}
	return nil
}

func liveHostdRebindValidOperation(operation *apicontract.LANGatewayRebindOperation, operationID string) bool {
	if operation == nil || operation.OperationID != operationID || operation.StateSequence <= 0 ||
		!liveHostdRebindValidDigest(operation.TerminalReceiptDigest) {
		return false
	}
	switch operation.TerminalDisposition {
	case "commit":
		return operation.StoredState == string(appaccess.GatewayRebindCommitted) && operation.DatabaseCommitObserved
	case "abort":
		return operation.StoredState == string(appaccess.GatewayRebindRolledBack) && !operation.DatabaseCommitObserved
	default:
		return false
	}
}

func liveHostdRebindValidAuthority(authority *apicontract.LANGatewayRebindCurrentAuthority) bool {
	if authority == nil || authority.OperationID == "" || authority.ProfileRevisionID == "" ||
		authority.ProfileRevisionNumber <= 0 || !liveHostdRebindValidDigest(authority.ProfileSpecDigest) {
		return false
	}
	switch authority.Kind {
	case string(appaccess.GatewayRebindSourceGatewayUpgrade):
		return authority.TerminalReceiptDigest == ""
	case string(appaccess.GatewayRebindSourceGatewayRebind):
		return liveHostdRebindValidDigest(authority.TerminalReceiptDigest)
	default:
		return false
	}
}

func liveHostdRebindValidDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && (character < 'a' || character > 'f')) {
			return false
		}
	}
	return true
}

func liveHostdRebindRecover(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID string, ready liveHostdRebindReadyMarker,
) liveHostdRebindStatus {
	t.Helper()
	if !ready.RecoveryOnly {
		status, replayErr := liveHostdRebindNormalReplay(fixture, operationID)
		if replayErr != nil {
			t.Fatalf("normal retained rebind replay: %v", replayErr)
		}
		liveHostdRebindAssertDurableTerminal(t, fixture, status, operationID, false)
		return status
	}
	path := "/api/v1/system/lan-gateway-rebind/" + operationID
	statusCode, body, err := liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet, path, nil)
	var status liveHostdRebindStatus
	if err != nil || statusCode != http.StatusOK || json.Unmarshal(body, &status) != nil ||
		status.OperationID != operationID {
		t.Fatalf("read retained rebind: status=%d error=%v", statusCode, err)
	}
	if ready.RecoveryKind != "gateway_rebind" || ready.OperationID != operationID {
		t.Fatal("recovery-only full hostd did not retain the exact rebind pin")
	}
	// Cross-operation reads are rejected before runtime/repository/effect work.
	statusCode, _, err = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodGet,
		"/api/v1/system/lan-gateway-rebind/"+uuid.NewString(), nil)
	if err != nil || statusCode != http.StatusConflict {
		t.Fatalf("cross-operation recovery read: status=%d error=%v", statusCode, err)
	}
	statusCode, body, err = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		path+"/recovery-proposal", map[string]any{})
	var review liveHostdRebindRecoveryReview
	if err != nil || statusCode != http.StatusOK || json.Unmarshal(body, &review) != nil ||
		review.OperationID != operationID || review.RecoveryDigest == "" || review.RecoveryApprovalDigest == "" {
		t.Fatalf("inspect exact recovery: status=%d error=%v", statusCode, err)
	}
	statusCode, body, err = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost, path+"/reconcile", map[string]any{
		"version": 1, "action": "reconcile", "recoveryDigest": review.RecoveryDigest,
		"recoveryApprovalDigest": review.RecoveryApprovalDigest,
	})
	var response struct {
		Version int                   `json:"version"`
		Status  liveHostdRebindStatus `json:"status"`
	}
	if err != nil || statusCode != http.StatusOK || json.Unmarshal(body, &response) != nil ||
		response.Version != 1 || response.Status.OperationID != operationID {
		t.Fatalf("reconcile exact recovery: status=%d error=%v", statusCode, err)
	}
	// The same process remains recovery-only even after a terminal result.
	statusCode, _, err = liveHostdRebindHTTP(fixture.ctx, fixture, http.MethodPost,
		"/api/v1/system/lan-gateway-rebind/proposal", map[string]any{})
	if err != nil || statusCode != http.StatusServiceUnavailable {
		t.Fatalf("recovery process reopened normal admission: status=%d error=%v", statusCode, err)
	}
	liveHostdRebindAssertDurableTerminal(t, fixture, response.Status, operationID, true)
	return response.Status
}

func liveHostdRebindAssertDurableTerminal(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	status liveHostdRebindStatus, operationID string, recoveryAudited bool,
) {
	t.Helper()
	liveHostdRebindAssertTerminal(t, status, operationID)
	db, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal("open retained rebind database:", err)
	}
	repository := appaccess.New(db)
	snapshot, snapshotErr := repository.GatewayRebindRecoverySnapshot(context.Background())
	fenceErr := repository.CheckGatewayRebindFence(context.Background())
	var auditCount int
	auditErr := db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE resource_type='lan_gateway_rebind' AND action=? AND resource_id=?`,
		appaccess.GatewayRebindReconcilePurpose, operationID).Scan(&auditCount)
	closeErr := db.Close()
	if snapshotErr != nil || fenceErr != nil || auditErr != nil || closeErr != nil || snapshot.Active != nil {
		t.Fatalf("terminal SQL/fence evidence is incomplete: snapshot=%v fence=%v audit=%v close=%v active=%v",
			snapshotErr, fenceErr, auditErr, closeErr, snapshot.Active != nil)
	}
	if recoveryAudited && auditCount != 1 || !recoveryAudited && auditCount > 1 {
		t.Fatalf("exact reconcile audit count=%d recoveryAudited=%v", auditCount, recoveryAudited)
	}
	var history *appaccess.GatewayRebindHistoryEntry
	for index := range snapshot.History {
		candidate := &snapshot.History[index]
		if candidate.Claim.V2 != nil && candidate.Claim.V2.Spec.OperationID == operationID {
			if history != nil {
				t.Fatal("duplicate retained rebind history")
			}
			history = candidate
		}
	}
	if history == nil || history.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		history.Claim.V2 == nil || len(history.Events) < 2 || len(history.Commands) != len(history.Events)-1 ||
		len(history.RosterV2) != len(fixture.appIDs) || len(history.RuntimeHeads) != len(fixture.appIDs) {
		t.Fatalf("retained SQL history cardinality is incomplete: %#v", history)
	}
	claim := history.Claim.V2
	if history.Events[0].OperationID != operationID || history.Events[0].Sequence != 1 ||
		history.Events[0].State != appaccess.GatewayRebindPrepared || claim.State != history.Events[len(history.Events)-1].State {
		t.Fatal("retained SQL history does not begin at A0 or end at the claim state")
	}
	for index := 1; index < len(history.Events); index++ {
		previous, event, command := history.Events[index-1], history.Events[index], history.Commands[index-1]
		if event.OperationID != operationID || event.Sequence != int64(index+1) || event.CreatedAt.Before(previous.CreatedAt) ||
			command.OperationID != operationID || command.PreviousState != previous.State || command.NextState != event.State ||
			command.Sequence != event.Sequence || command.PreviousSequence != previous.Sequence || command.CommandDigest == "" ||
			command.ProtectedRecordDigest == "" {
			t.Fatalf("retained SQL event/command order failed at index %d", index)
		}
	}
	receipt := liveHostdRebindReadTerminal(t, fixture.dataRoot, operationID)
	terminalCommand := history.Commands[len(history.Commands)-1]
	if !liveHostdRebindTerminalReceiptBound(status, receipt, terminalCommand) ||
		status.Operation.OperationID != operationID || status.Operation.StoredState != string(claim.State) ||
		status.Operation.StateSequence != claim.StateSequence || receipt.ProtectedRecordDigest == "" ||
		receipt.ClaimRequestDigest != claim.RequestDigest ||
		receipt.ClaimSpecDigest != claim.RebindApproval.SpecDigest {
		t.Fatal("protected terminal does not bind the retained SQL claim")
	}
	switch status.Operation.TerminalDisposition {
	case "commit":
		if claim.State != appaccess.GatewayRebindCommitted || receipt.Disposition != appaccess.GatewayRebindDispositionCommit ||
			snapshot.CurrentSource == nil || snapshot.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
			snapshot.CurrentSource.OperationID != operationID || snapshot.CurrentDatabaseCommittedEvent == nil ||
			snapshot.CurrentDatabaseCommittedEvent.OperationID != operationID ||
			len(history.Transfers) != len(history.RosterV2) || len(snapshot.CurrentTransfers) != len(history.Transfers) {
			t.Fatal("committed SQL/current/grant-transfer evidence is incomplete")
		}
		seenSuccessor, seenDatabase := false, false
		for _, event := range history.Events {
			seenSuccessor = seenSuccessor || event.State == appaccess.GatewayRebindSuccessorReady
			seenDatabase = seenDatabase || event.State == appaccess.GatewayRebindDatabaseCommitted
		}
		if !seenSuccessor || !seenDatabase {
			t.Fatal("committed history omitted successor-ready or database-committed evidence")
		}
		liveHostdRebindAssertOwnedResourceCounts(t, fixture, operationID,
			map[string]int{"container": 1, "network": 1, "volume": 2})
	case "abort":
		if claim.State != appaccess.GatewayRebindRolledBack || receipt.Disposition != appaccess.GatewayRebindDispositionAbort ||
			len(history.Transfers) != 0 || snapshot.CurrentSource == nil || snapshot.CurrentSource.OperationID == operationID {
			t.Fatal("rolled-back SQL/current evidence is incomplete")
		}
		for _, event := range history.Events {
			if event.State == appaccess.GatewayRebindDatabaseCommitted {
				t.Fatal("rolled-back history contains immutable database commit evidence")
			}
		}
		liveHostdRebindAssertOwnedResourceCounts(t, fixture, operationID,
			map[string]int{"container": 0, "network": 0, "volume": 0})
	default:
		t.Fatalf("unknown terminal disposition %q", status.Operation.TerminalDisposition)
	}
}

// liveHostdRebindTerminalReceiptBound joins the separately generated terminal
// proof, protected receipt, and immutable final SQL command. The terminal
// proof digest is deliberately not a receipt digest and is validated elsewhere.
func liveHostdRebindTerminalReceiptBound(status liveHostdRebindStatus,
	receipt liveHostdRebindTerminalReceipt, command appaccess.GatewayRebindTransitionCommand,
) bool {
	return status.Operation != nil && receipt.OperationID == status.Operation.OperationID &&
		command.OperationID == status.Operation.OperationID && receipt.Digest == status.Operation.TerminalReceiptDigest &&
		command.TerminalReceiptDigest == receipt.Digest && string(receipt.Disposition) == status.Operation.TerminalDisposition &&
		string(command.TerminalDisposition) == status.Operation.TerminalDisposition &&
		string(command.NextState) == status.Operation.StoredState && command.Sequence == status.Operation.StateSequence
}

func liveHostdRebindAssertOwnedResourceCounts(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	operationID string, expected map[string]int,
) {
	t.Helper()
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls", "-q", "--filter", "label=io.rig.operation-id=" + operationID}
		if kind == "container" {
			args = []string{"ps", "-aq", "--no-trunc", "--filter", "label=io.rig.operation-id=" + operationID}
		}
		body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil, args...)
		ids := strings.Fields(string(body))
		if err != nil || len(ids) != expected[kind] {
			t.Fatalf("terminal exact-owned %s census=%d want=%d error=%v", kind, len(ids), expected[kind], err)
		}
		for _, id := range ids {
			labels, labelErr := lanTwoAppDockerLabels(fixture.ctx, fixture.docker, kind, id)
			if labelErr != nil || labels["io.rig.operation-id"] != operationID {
				t.Fatalf("terminal %s %q lost exact operation ownership: %v", kind, id, labelErr)
			}
		}
	}
}

func liveHostdRebindAssertTerminal(t *testing.T, status liveHostdRebindStatus, operationID string) {
	t.Helper()
	if status.OperationID != operationID || status.Active || !status.Historical || status.TerminalProof != "verified" ||
		status.OperationFence != "released" || !liveHostdRebindValidOperation(status.Operation, operationID) ||
		!liveHostdRebindValidDigest(status.TerminalDigest) || status.CurrentProof != "verified" ||
		!liveHostdRebindValidDigest(status.CurrentDigest) || !liveHostdRebindValidAuthority(status.SelectedCurrent) {
		t.Fatalf("rebind terminal proof is incomplete: operation=%q active=%v historical=%v terminal=%q fence=%q",
			status.OperationID, status.Active, status.Historical, status.TerminalProof, status.OperationFence)
	}
	switch status.Operation.TerminalDisposition {
	case "commit":
		if status.SelectedCurrent.OperationID != operationID || !status.Operation.DatabaseCommitObserved {
			t.Fatal("committed rebind lacks immutable database/current proof")
		}
	case "abort":
		if status.SelectedCurrent.OperationID == operationID || status.Operation.DatabaseCommitObserved {
			t.Fatal("rolled-back rebind lacks immutable current proof")
		}
	default:
		t.Fatal("rebind terminal disposition is invalid")
	}
}
