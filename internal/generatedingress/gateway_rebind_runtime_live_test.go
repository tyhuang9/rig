package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveGatewayRebindRuntimeEnvironment = "RIG_RUN_LIVE_GATEWAY_REBIND_RUNTIME"

type liveGatewayRebindRuntimePreconditions struct {
	GatewayV2     string
	CrossStore    string
	Runtime       string
	GOOS          string
	DockerHost    string
	DockerContext string
}

// liveGatewayRebindRuntimePrecondition deliberately distinguishes a missing
// opt-in (skip) from an unsafe or remote host after the user has opted in.
func liveGatewayRebindRuntimePrecondition(value liveGatewayRebindRuntimePreconditions) (skip bool, err error) {
	if value.GatewayV2 != "1" || value.CrossStore != "1" || value.Runtime != "1" {
		return true, nil
	}
	if value.GOOS != "linux" || value.DockerHost != "" || value.DockerContext != "" {
		return false, errors.New("typed runtime rebind requires the default local Linux Docker host")
	}
	return false, nil
}

func requireLiveGatewayRebindRuntime(t *testing.T) {
	t.Helper()
	skip, err := liveGatewayRebindRuntimePrecondition(liveGatewayRebindRuntimePreconditions{
		GatewayV2: os.Getenv("RIG_RUN_LIVE_GATEWAY_V2"), CrossStore: os.Getenv(liveGatewayRebindCrossStoreEnvironment),
		Runtime: os.Getenv(liveGatewayRebindRuntimeEnvironment), GOOS: runtime.GOOS,
		DockerHost: os.Getenv("DOCKER_HOST"), DockerContext: os.Getenv("DOCKER_CONTEXT"),
	})
	if skip {
		t.Skip("set all three live gateway rebind flags on a disposable local Linux Docker host")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRebindRuntimeLivePreconditions(t *testing.T) {
	for _, test := range []struct {
		name  string
		value liveGatewayRebindRuntimePreconditions
		skip  bool
		want  bool
	}{
		{name: "missing runtime opt in skips", value: liveGatewayRebindRuntimePreconditions{GatewayV2: "1", CrossStore: "1", GOOS: "windows"}, skip: true},
		{name: "enabled windows fails", value: liveGatewayRebindRuntimePreconditions{GatewayV2: "1", CrossStore: "1", Runtime: "1", GOOS: "windows"}, want: true},
		{name: "enabled remote docker fails", value: liveGatewayRebindRuntimePreconditions{GatewayV2: "1", CrossStore: "1", Runtime: "1", GOOS: "linux", DockerHost: "tcp://remote"}, want: true},
		{name: "enabled remote docker context fails", value: liveGatewayRebindRuntimePreconditions{GatewayV2: "1", CrossStore: "1", Runtime: "1", GOOS: "linux", DockerContext: "remote"}, want: true},
		{name: "enabled local linux proceeds", value: liveGatewayRebindRuntimePreconditions{GatewayV2: "1", CrossStore: "1", Runtime: "1", GOOS: "linux"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			skip, err := liveGatewayRebindRuntimePrecondition(test.value)
			if skip != test.skip || (err != nil) != test.want {
				t.Fatalf("skip=%t err=%v", skip, err)
			}
		})
	}
}

type liveGatewayRebindRuntimeFixture struct {
	liveGatewayRebindSourceFixture
	manager          *Manager
	diagnosticRunner *liveGatewayRebindRuntimeDiagnosticRunner
	diagnosticDriver *liveGatewayRebindRuntimeRollbackDriver
	diagnosticStage  *liveGatewayRebindRuntimeTypedStageDiagnosticDriver
	inspection       GatewayRebindProposalInspection
	input            gatewayRebindCommitInput
}

func newLiveGatewayRebindRuntimeFixture(t *testing.T) liveGatewayRebindRuntimeFixture {
	t.Helper()
	requireLiveGatewayRebindRuntime(t)
	source := newLiveGatewayRebindSourceFixture(t, liveGatewayRebindRuntimeEnvironment)
	options := source.fixture.ingress.options
	options.RebindFenceCheck = source.repository.CheckGatewayRebindFence
	options.RebindCurrentStateRepository = source.repository
	diagnosticRunner := &liveGatewayRebindRuntimeDiagnosticRunner{runner: source.fixture.runner}
	manager, err := New(diagnosticRunner, options)
	if err != nil {
		t.Fatal("open typed runtime manager")
	}
	rebind, current := newGatewayRebindRuntimeDrivers(manager)
	diagnosticStage := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		gatewayRebindTypedStageDriver: rebind.stage,
		first:                         liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot{FirstMethod: liveGatewayRebindRuntimeTypedStageMethodUnknown},
	}
	rebind.stage = diagnosticStage
	diagnosticDriver := &liveGatewayRebindRuntimeRollbackDriver{managerGatewayRebindCrossStoreDriver: rebind}
	manager.gatewayRebindCrossStoreDriver = diagnosticDriver
	manager.gatewayCurrentPhysicalDriver = current
	source.fixture.ingress = manager
	inspection, err := manager.InspectGatewayRebindProposal(source.fixture.ctx, source.repository, GatewayRebindProposalInput{
		OperationID:                    source.proposal.Spec.OperationID,
		SuccessorProfileRevisionID:     source.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: source.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    source.proposal.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               source.proposal.Spec.SuccessorProfile,
	})
	if err != nil || len(inspection.Roster) != 1 {
		t.Fatal("inspect the exact typed rebind proposal")
	}
	value := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: source, manager: manager,
		diagnosticRunner: diagnosticRunner, diagnosticDriver: diagnosticDriver, diagnosticStage: diagnosticStage, inspection: inspection,
		input: gatewayRebindCommitInput{Inspection: inspection,
			RebindApproval:    appaccess.Approval{Action: appaccess.ActionRebindGateway, SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
			ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway, SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator}}}
	// Registered after the source fixture: it runs before the source cleanup and
	// only removes an exact committed successor terminal.
	t.Cleanup(func() { cleanupLiveGatewayRebindRuntime(t, value) })
	return value
}

type liveGatewayRebindRuntimeRollbackDriver struct {
	managerGatewayRebindCrossStoreDriver
	mu               sync.Mutex
	refuseSequence17 bool
	refused          bool
	lastSequence     uint64
	lastPhase        gatewayRebindProgressPhase
}

func (d *liveGatewayRebindRuntimeRollbackDriver) requireSequence17Refusal() {
	d.mu.Lock()
	d.refuseSequence17 = true
	d.mu.Unlock()
}

func (d *liveGatewayRebindRuntimeRollbackDriver) diagnosticSnapshot() (bool, uint64, gatewayRebindProgressPhase) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.refused, d.lastSequence, d.lastPhase
}

type liveGatewayRebindRuntimeDiagnosticRunner struct {
	mu               sync.Mutex
	runner           runtimeprocess.CommandRunner
	lastCommandClass string
	lastResult       liveGatewayRebindRuntimeCommandResult
}

type liveGatewayRebindRuntimeCommandResult struct {
	Outcome         string
	Context         string
	StdoutTruncated bool
	StderrTruncated bool
}

func (r *liveGatewayRebindRuntimeDiagnosticRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	class := liveGatewayRebindRuntimeCommandClass(request.Args)
	result, err := r.runner.Run(ctx, request)
	r.mu.Lock()
	r.lastCommandClass = class
	r.lastResult = liveGatewayRebindRuntimeCommandResult{
		Outcome: liveGatewayRebindRuntimeCommandOutcome(ctx, err), Context: liveGatewayRebindRuntimeCommandContext(ctx),
		StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated,
	}
	r.mu.Unlock()
	return result, err
}

func (r *liveGatewayRebindRuntimeDiagnosticRunner) diagnosticSnapshot() (string, liveGatewayRebindRuntimeCommandResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	class, result := r.lastCommandClass, r.lastResult
	if class == "" {
		class = "unknown"
	}
	if result.Outcome == "" {
		result.Outcome = "unknown"
	}
	if result.Context == "" {
		result.Context = "unknown"
	}
	return class, result
}

func liveGatewayRebindRuntimeCommandOutcome(ctx context.Context, err error) string {
	if err == nil {
		return "success"
	}
	switch liveGatewayRebindRuntimeCommandContext(ctx) {
	case "cancelled":
		return "cancelled"
	case "deadline":
		return "deadline"
	default:
		return "failure"
	}
}

func liveGatewayRebindRuntimeCommandContext(ctx context.Context) string {
	if ctx == nil {
		return "nil"
	}
	switch ctx.Err() {
	case context.Canceled:
		return "cancelled"
	case context.DeadlineExceeded:
		return "deadline"
	default:
		return "active"
	}
}

func liveGatewayRebindRuntimeCommandClass(args []string) string {
	if len(args) < 2 {
		return "unknown"
	}
	switch args[0] + "_" + args[1] {
	case "image_inspect", "image_ls", "network_inspect", "network_ls", "network_create", "network_rm", "network_connect", "network_disconnect",
		"volume_inspect", "volume_ls", "volume_create", "volume_rm", "container_inspect", "container_ls", "container_create", "container_start", "container_stop", "container_rm":
		return args[0] + "_" + args[1]
	case "container_cp":
		return "config_copy"
	case "container_exec":
		return "container_exec"
	default:
		return "unknown"
	}
}

type liveGatewayRebindRuntimeTypedStageMethod string

const (
	liveGatewayRebindRuntimeTypedStageMethodUnknown         liveGatewayRebindRuntimeTypedStageMethod = "unknown"
	liveGatewayRebindRuntimeTypedStageMethodServeStage      liveGatewayRebindRuntimeTypedStageMethod = "serve_stage"
	liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig liveGatewayRebindRuntimeTypedStageMethod = "copy_final_config"
)

type liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot struct {
	FirstMethod            liveGatewayRebindRuntimeTypedStageMethod
	Operation              string
	Checkpoint             string
	ServeStageSucceeded    bool
	CopyFinalConfigSuccess bool
	OuterRefusal           string
}

type liveGatewayRebindRuntimeTypedStageDiagnosticDriver struct {
	gatewayRebindTypedStageDriver
	mu    sync.Mutex
	first liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot
}

func (d *liveGatewayRebindRuntimeTypedStageDiagnosticDriver) serveStage(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (string, error) {
	endpoint, err := d.gatewayRebindTypedStageDriver.serveStage(ctx, intent, effect, guard)
	d.record(liveGatewayRebindRuntimeTypedStageMethodServeStage, err)
	return endpoint, err
}

func (d *liveGatewayRebindRuntimeTypedStageDiagnosticDriver) copyFinalConfig(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, checkpoint gatewayRebindPredecessorCheckpoint,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	err := d.gatewayRebindTypedStageDriver.copyFinalConfig(ctx, intent, checkpoint, effect, guard)
	d.record(liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig, err)
	return err
}

func (d *liveGatewayRebindRuntimeTypedStageDiagnosticDriver) record(
	method liveGatewayRebindRuntimeTypedStageMethod, err error,
) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		switch method {
		case liveGatewayRebindRuntimeTypedStageMethodServeStage:
			d.first.ServeStageSucceeded = true
		case liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig:
			d.first.CopyFinalConfigSuccess = true
		}
		return
	}
	if d.first.FirstMethod != "" && d.first.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown {
		return
	}
	d.first.FirstMethod = method
	d.first.Operation = gatewayRebindTypedStageDiagnosticOperationUnknown.String()
	d.first.Checkpoint = gatewayRebindTypedStageDiagnosticCheckpointUnknown.String()
	var diagnostic *gatewayRebindTypedStageDiagnosticError
	if errors.As(err, &diagnostic) && diagnostic != nil {
		d.first.Operation, d.first.Checkpoint = diagnostic.typedStageDiagnostic()
	}
}

func (d *liveGatewayRebindRuntimeTypedStageDiagnosticDriver) diagnosticSnapshot(
	outerErr error,
) liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	value := d.first
	if value.FirstMethod == "" {
		value.FirstMethod = liveGatewayRebindRuntimeTypedStageMethodUnknown
	}
	if value.Operation == "" {
		value.Operation = gatewayRebindTypedStageDiagnosticOperationUnknown.String()
	}
	if value.Checkpoint == "" {
		value.Checkpoint = gatewayRebindTypedStageDiagnosticCheckpointUnknown.String()
	}
	if value.OuterRefusal == "" {
		value.OuterRefusal = "none"
	}
	if outerErr != nil && value.FirstMethod == liveGatewayRebindRuntimeTypedStageMethodUnknown &&
		(value.ServeStageSucceeded || value.CopyFinalConfigSuccess) {
		value.OuterRefusal = "after_successful_stage_method"
	}
	return value
}

type liveGatewayRebindRuntimeFailureDiagnostic struct {
	LastSequence        uint64
	LastPhase           gatewayRebindProgressPhase
	FirstMethod         liveGatewayRebindRuntimeTypedStageMethod
	Operation           string
	Checkpoint          string
	ServeStageSucceeded bool
	CopyFinalConfigOK   bool
	OuterRefusal        string
	LatestCommand       string
	CommandOutcome      string
	CommandContext      string
	StdoutTruncated     bool
	StderrTruncated     bool
}

func (f liveGatewayRebindRuntimeFixture) failureDiagnostic(err error) liveGatewayRebindRuntimeFailureDiagnostic {
	value := liveGatewayRebindRuntimeFailureDiagnostic{
		FirstMethod:    liveGatewayRebindRuntimeTypedStageMethodUnknown,
		Operation:      gatewayRebindTypedStageDiagnosticOperationUnknown.String(),
		Checkpoint:     gatewayRebindTypedStageDiagnosticCheckpointUnknown.String(),
		OuterRefusal:   "none",
		LatestCommand:  "unknown",
		CommandOutcome: "unknown",
		CommandContext: "unknown",
	}
	if f.diagnosticDriver != nil {
		_, value.LastSequence, value.LastPhase = f.diagnosticDriver.diagnosticSnapshot()
	}
	if f.diagnosticStage != nil {
		stage := f.diagnosticStage.diagnosticSnapshot(err)
		value.FirstMethod, value.Operation, value.Checkpoint = stage.FirstMethod, stage.Operation, stage.Checkpoint
		value.ServeStageSucceeded, value.CopyFinalConfigOK, value.OuterRefusal = stage.ServeStageSucceeded,
			stage.CopyFinalConfigSuccess, stage.OuterRefusal
	}
	if f.diagnosticRunner != nil {
		class, result := f.diagnosticRunner.diagnosticSnapshot()
		value.LatestCommand = class
		value.CommandOutcome, value.CommandContext = result.Outcome, result.Context
		value.StdoutTruncated, value.StderrTruncated = result.StdoutTruncated, result.StderrTruncated
	}
	return value
}

func liveGatewayRebindRuntimeLogFailure(t *testing.T, fixture liveGatewayRebindRuntimeFixture, err error) {
	t.Helper()
	value := fixture.failureDiagnostic(err)
	t.Logf("typed runtime refusal: last_validated_sequence=%d last_validated_phase=%q first_stage_method=%q stage_operation=%q stage_checkpoint=%q serve_stage_succeeded=%t copy_final_config_succeeded=%t outer_refusal=%q latest_command=%q command_outcome=%q command_context=%q stdout_truncated=%t stderr_truncated=%t",
		value.LastSequence, value.LastPhase, value.FirstMethod, value.Operation, value.Checkpoint,
		value.ServeStageSucceeded, value.CopyFinalConfigOK, value.OuterRefusal, value.LatestCommand,
		value.CommandOutcome, value.CommandContext, value.StdoutTruncated, value.StderrTruncated)
}

type liveGatewayRebindRuntimeReplayRunner struct {
	runner  runtimeprocess.CommandRunner
	effects [][]string
}

func (r *liveGatewayRebindRuntimeReplayRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if !liveGatewayRebindRuntimeReplayRead(request.Args) {
		r.effects = append(r.effects, append([]string(nil), request.Args...))
	}
	return r.runner.Run(ctx, request)
}

func liveGatewayRebindRuntimeReplayRead(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "image", "network", "volume":
		return args[1] == "inspect" || args[1] == "ls"
	case "container":
		switch args[1] {
		case "inspect", "ls":
			return true
		case "cp":
			return len(args) == 4 && strings.Contains(args[2], ":/config/") && args[3] == "-"
		case "exec":
			return liveGatewayRebindRuntimeReplayReadExec(args)
		}
	}
	return false
}

func liveGatewayRebindRuntimeReplayReadExec(args []string) bool {
	if len(args) < 5 || args[0] != "container" || args[1] != "exec" || args[2] == "" || args[3] != "curl" {
		return false
	}
	config := []string{"container", "exec", args[2], "curl", "--disable", "--silent", "--show-error", "--fail",
		"--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/"}
	if reflect.DeepEqual(args, config) {
		return true
	}
	challengePrefix := []string{"container", "exec", args[2], "curl", "--disable", "--silent", "--show-error", "--output", "-",
		"--write-out", "\n%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1",
		"--max-time", "2", "--header"}
	if len(args) == len(challengePrefix)+2 && reflect.DeepEqual(args[:len(challengePrefix)], challengePrefix) &&
		liveGatewayRebindRuntimeReplayReadHeader(args[len(challengePrefix)]) && liveGatewayRebindRuntimeReplayReadURL(args[len(args)-1], true) {
		return true
	}
	statusPrefix := []string{"container", "exec", args[2], "curl", "--disable", "--silent", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1",
		"--max-time", "2"}
	if len(args) == len(statusPrefix)+3 && reflect.DeepEqual(args[:len(statusPrefix)], statusPrefix) && args[len(statusPrefix)] == "--header" &&
		liveGatewayRebindRuntimeReplayReadHeader(args[len(statusPrefix)+1]) && liveGatewayRebindRuntimeReplayReadURL(args[len(args)-1], false) {
		return true
	}
	headPrefix := []string{"container", "exec", args[2], "curl", "--disable", "--silent", "--head", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1",
		"--max-time", "2"}
	return len(args) == len(headPrefix)+1 && reflect.DeepEqual(args[:len(headPrefix)], headPrefix) &&
		liveGatewayRebindRuntimeReplayReadURL(args[len(args)-1], false)
}

func liveGatewayRebindRuntimeReplayReadHeader(value string) bool {
	return strings.HasPrefix(value, "Host: ") && len(value) > len("Host: ") && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func liveGatewayRebindRuntimeReplayReadURL(value string, challenge bool) bool {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.RawQuery != "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	if challenge {
		return validSHA256(strings.TrimPrefix(parsed.Path, gatewayV2ChallengePathPrefix))
	}
	return parsed.Path == "/"
}

func TestGatewayRebindRuntimeReplayCommandAudit(t *testing.T) {
	const container = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, test := range []struct {
		name string
		args []string
		read bool
	}{
		{name: "container inspect", args: []string{"container", "inspect", container}, read: true},
		{name: "network list", args: []string{"network", "ls", "--format", "{{.Name}}"}, read: true},
		{name: "volume inspect", args: []string{"volume", "inspect", "volume"}, read: true},
		{name: "config copy out", args: []string{"container", "cp", container + ":/config/active.json", "-"}, read: true},
		{name: "admin config get", args: []string{"container", "exec", container, "curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/"}, read: true},
		{name: "challenge get", args: []string{"container", "exec", container, "curl", "--disable", "--silent", "--show-error", "--output", "-", "--write-out", "\n%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2", "--header", "Host: 192.0.2.2", "http://172.18.0.2:8080" + gatewayV2ChallengePathPrefix + strings.Repeat("a", 64)}, read: true},
		{name: "status get", args: []string{"container", "exec", container, "curl", "--disable", "--silent", "--output", "/dev/null", "--write-out", "%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2", "--header", "Host: app.rig.localhost", "http://172.18.0.2:8080/"}, read: true},
		{name: "endpoint head", args: []string{"container", "exec", container, "curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2", "http://app.network:3000/"}, read: true},
		{name: "network connect", args: []string{"network", "connect", "network", container}},
		{name: "network disconnect", args: []string{"network", "disconnect", "network", container}},
		{name: "volume create", args: []string{"volume", "create", "volume"}},
		{name: "config copy upload", args: []string{"container", "cp", "-", container + ":/config/active.json"}},
		{name: "reload", args: []string{"container", "exec", container, "caddy", "reload", "--config", "/config/active.json"}},
		{name: "copy in container", args: []string{"container", "exec", "--user", "0:0", container, "cp", "/config/a", "/config/b"}},
		{name: "move in container", args: []string{"container", "exec", "--user", "0:0", container, "mv", "/config/a", "/config/b"}},
		{name: "unknown extra exec tokens", args: []string{"container", "exec", container, "curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/", "unexpected"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveGatewayRebindRuntimeReplayRead(test.args); got != test.read {
				t.Fatalf("read=%t args=%q", got, test.args)
			}
		})
	}
}

func (d *liveGatewayRebindRuntimeRollbackDriver) reconcileSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	return d.managerGatewayRebindCrossStoreDriver.reconcileSuccessorLocked(ctx, request,
		func(appendCtx context.Context, record gatewayRebindProgressRecord) error {
			d.mu.Lock()
			refuse := d.refuseSequence17 && record.Sequence == 17 && record.Phase == gatewayRebindProgressHandoverCommitted && !d.refused
			if refuse {
				d.refused = true
			}
			d.mu.Unlock()
			if refuse {
				return errors.New("test refusal before durable sequence seventeen")
			}
			return d.appendValidatedProgress(appendCtx, record, appendProgress)
		})
}

func (d *liveGatewayRebindRuntimeRollbackDriver) appendValidatedProgress(ctx context.Context,
	record gatewayRebindProgressRecord, appendProgress gatewayRebindTypedProgressAppender,
) error {
	if err := appendProgress(ctx, record); err != nil {
		return err
	}
	d.mu.Lock()
	d.lastSequence, d.lastPhase = record.Sequence, record.Phase
	d.mu.Unlock()
	return nil
}

func TestLiveGatewayRebindRuntimeCommitAndReplay(t *testing.T) {
	liveGatewayRebindRuntimeJourney(t, false)
}

func TestLiveGatewayRebindRuntimeRollbackAndReplay(t *testing.T) {
	liveGatewayRebindRuntimeJourney(t, true)
}

func liveGatewayRebindRuntimeJourney(t *testing.T, rollback bool) {
	t.Helper()
	f := newLiveGatewayRebindRuntimeFixture(t)
	ctx, repository, fixture := f.fixture.ctx, f.repository, f.fixture
	beforeSQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || beforeSQL.Active != nil || beforeSQL.CurrentSource == nil || len(beforeSQL.History) != 0 {
		t.Fatal("read source SQL authority")
	}
	beforeHeads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || len(beforeHeads) != 2 {
		t.Fatal("read source runtime heads")
	}
	beforeAccess, err := repository.CurrentAppAccess(ctx, fixture.spec.appID)
	if err != nil {
		t.Fatal("read source LAN access")
	}
	beforeLoopback, err := liveGatewayRebindRuntimeLoopbackSnapshot(ctx, repository, f.loopbackID)
	if err != nil {
		t.Fatal("read source loopback state")
	}
	beforeProfile, err := repository.CurrentGatewayProfile(ctx)
	if err != nil || beforeProfile != f.profile {
		t.Fatal("read source gateway profile")
	}
	roster := f.inspection.Roster[0]
	bindingRef := appaccess.GatewayBindingRef{AppID: roster.AppID, AllocationID: roster.AllocationID,
		AccessRevisionID: roster.AccessRevisionID, GrantAttemptID: roster.GrantAttemptID}
	beforeBinding, err := repository.ResolveGatewayBinding(ctx, bindingRef)
	if err != nil || beforeBinding.RawProfile != beforeProfile || len(beforeBinding.TransferChain) != 0 {
		t.Fatal("read exact immutable source grant binding")
	}
	beforeRoute, err := f.manager.store.load()
	if err != nil {
		t.Fatal("read protected source route")
	}
	beforeState, beforeJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)

	var result GatewayRebindCommitResult
	if rollback {
		driver := f.diagnosticDriver
		driver.requireSequence17Refusal()
		result, err = f.manager.commitGatewayRebind(ctx, repository, f.input)
		refused, _, _ := driver.diagnosticSnapshot()
		if err != nil || !refused {
			if err != nil {
				liveGatewayRebindRuntimeLogFailure(t, f, err)
			}
			t.Fatalf("force exact pre-sequence17 rollback: refused=%t err=%v", refused, err)
		}
	} else {
		result, err = f.manager.commitGatewayRebind(ctx, repository, f.input)
		if err != nil {
			liveGatewayRebindRuntimeLogFailure(t, f, err)
			failLiveIngress(t, "commit typed runtime rebind", err)
		}
	}
	wantDisposition, wantPhase := appaccess.GatewayRebindDispositionCommit, appaccess.GatewayRebindCommitted
	if rollback {
		wantDisposition, wantPhase = appaccess.GatewayRebindDispositionAbort, appaccess.GatewayRebindRolledBack
	}
	if result.OperationID != f.inspection.Spec.OperationID || result.FinalPhase != wantPhase || result.Disposition != wantDisposition ||
		result.TerminalReceiptDigest == "" || !result.FenceReleased {
		t.Fatalf("terminal coordinator result=%+v", result)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Checkpoints) != 1 || len(history.TerminalsV2) != 1 ||
		len(history.Progress) != map[bool]int{false: 17, true: 18}[rollback] {
		t.Fatal("typed runtime journey did not retain one complete immutable history")
	}
	last := history.Progress[len(history.Progress)-1].Record
	wantLast := gatewayRebindProgressHandoverCommitted
	if rollback {
		wantLast = gatewayRebindProgressHandoverRolledBack
	}
	if last.Phase != wantLast || history.TerminalsV2[0].Receipt.Disposition != wantDisposition ||
		history.TerminalsV2[0].Receipt.Digest != result.TerminalReceiptDigest {
		t.Fatal("typed terminal does not bind the durable physical outcome")
	}
	afterSQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || afterSQL.Active != nil || afterSQL.CurrentSource == nil ||
		(afterSQL.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayRebind && !rollback) ||
		(afterSQL.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade && rollback) ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("terminal SQL authority or fence is wrong")
	}
	afterAccess, accessErr := repository.CurrentAppAccess(ctx, fixture.spec.appID)
	afterLoopback, loopErr := liveGatewayRebindRuntimeLoopbackSnapshot(ctx, repository, f.loopbackID)
	afterProfile, profileErr := repository.CurrentGatewayProfile(ctx)
	afterHeads, headsErr := repository.GatewayRebindRuntimeHeads(ctx)
	afterBinding, bindingErr := repository.ResolveGatewayBinding(ctx, bindingRef)
	if accessErr != nil || loopErr != nil || profileErr != nil || headsErr != nil || bindingErr != nil || !reflect.DeepEqual(beforeAccess, afterAccess) ||
		!reflect.DeepEqual(beforeLoopback, afterLoopback) || !reflect.DeepEqual(beforeHeads, afterHeads) ||
		!reflect.DeepEqual(beforeBinding.RawAllocation, afterBinding.RawAllocation) ||
		!reflect.DeepEqual(beforeBinding.RawAccessRevision, afterBinding.RawAccessRevision) ||
		!reflect.DeepEqual(beforeBinding.RawGrant, afterBinding.RawGrant) || beforeBinding.RawProfile != afterBinding.RawProfile {
		t.Fatal("typed rebind changed immutable app access, profile, or runtime heads")
	}
	if rollback && (afterProfile != beforeProfile || afterBinding.EffectiveProfile != beforeProfile || len(afterBinding.TransferChain) != 0 ||
		afterBinding.TerminalReceiptDigest != beforeBinding.TerminalReceiptDigest) {
		t.Fatal("rollback did not restore the exact predecessor SQL authority")
	}
	if !rollback && (afterProfile.ID != f.inspection.Spec.SuccessorProfileRevisionID ||
		afterProfile.RevisionNumber != f.inspection.Spec.SuccessorProfileRevisionNumber ||
		afterProfile.SpecDigest != f.inspection.SuccessorProfileSpecDigest ||
		afterBinding.EffectiveProfile != afterProfile || len(afterBinding.TransferChain) != 1 ||
		afterBinding.TerminalReceiptDigest != result.TerminalReceiptDigest) {
		t.Fatal("commit did not install the one exact successor transfer")
	}
	afterRoute, routeErr := f.manager.store.load()
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if routeErr != nil || !reflect.DeepEqual(beforeRoute, afterRoute) || !reflect.DeepEqual(beforeState, afterState) || !reflect.DeepEqual(beforeJournal, afterJournal) {
		t.Fatal("typed rebind changed immutable predecessor protected state")
	}
	assertLiveOneShot(t, ctx, f.manager.options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply, "LAN app loopback route")
	assertLiveOneShot(t, ctx, f.manager.options.HostPort, f.loopbackID, "/", f.loopbackReply, "loopback-only app route")
	active, withdrawn := f.successorIPv4, f.predecessorIPv4
	if rollback {
		active, withdrawn = f.predecessorIPv4, f.successorIPv4
	}
	lanBody := liveGatewayRebindRuntimeLANBody(ctx, active, fixture.port, fixture.spec.applicationReply)
	wrongHost := probeGatewayV2HostStatus(ctx, active, fixture.port, f.loopbackID+".rig.localhost", "/")
	if !lanBody ||
		!wrongHost.Connected || !wrongHost.Responded || wrongHost.Status != 404 ||
		!probeGatewayRebindHandoverListenerAbsent(ctx, withdrawn, fixture.port) {
		t.Fatal("selected LAN authority or withdrawn listener is wrong")
	}

	options := f.manager.options
	replayRunner := &liveGatewayRebindRuntimeReplayRunner{runner: fixture.runner}
	fresh, err := New(replayRunner, options)
	if err != nil {
		t.Fatal("open fresh runtime manager")
	}
	rebind, current := newGatewayRebindRuntimeDrivers(fresh)
	fresh.gatewayRebindCrossStoreDriver, fresh.gatewayCurrentPhysicalDriver = rebind, current
	beforeReplayHistory, err := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil {
		t.Fatal("read complete protected history before fresh recovery")
	}
	beforeReplaySQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal("read SQL before fresh recovery")
	}
	recovery, err := fresh.RecoverGatewayRebindStartup(ctx, repository)
	if err != nil || recovery.Recovered || !recovery.FenceReleased || recovery.SelectedCurrentAuthority == nil ||
		*recovery.SelectedCurrentAuthority != *afterSQL.CurrentSource || (rollback && recovery.CurrentAttestationDigest != "") ||
		(!rollback && !validSHA256(recovery.CurrentAttestationDigest)) {
		t.Fatalf("fresh typed runtime recovery=%+v err=%v", recovery, err)
	}
	afterReplay, historyErr := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterReplaySQL, sqlErr := repository.GatewayRebindRecoverySnapshot(ctx)
	afterReplayHistory, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
	if historyErr != nil || sqlErr != nil || !reflect.DeepEqual(beforeReplaySQL, afterReplaySQL) ||
		filesErr != nil || !sameGatewayHistorySnapshot(beforeReplayHistory, afterReplayHistory) ||
		len(replayRunner.effects) != 0 || len(afterReplay.TerminalsV2) != 1 ||
		afterReplay.TerminalsV2[0].Receipt.Digest != result.TerminalReceiptDigest {
		t.Fatal("fresh recovery changed immutable typed history or SQL")
	}
	t.Logf("typed runtime %s completed through exact terminal and fresh-manager replay", map[bool]string{false: "commit", true: "rollback"}[rollback])
}

// liveGatewayRebindRuntimeLANBody checks the selected LAN listener's
// application response. probeGatewayV2HostStatus only reads challenge bodies.
func liveGatewayRebindRuntimeLANBody(parent context.Context, address string, port uint16, expected string) bool {
	parsed, err := netip.ParseAddr(address)
	if parent == nil || err != nil || !parsed.Is4() || parsed.String() != address || port == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 2 * time.Second,
		DialContext:           dialer.DialContext,
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+net.JoinHostPort(parsed.String(), strconv.FormatUint(uint64(port), 10))+"/", nil)
	if err != nil {
		return false
	}
	request.Host = address
	request.Header.Set("Connection", "close")
	response, err := (&http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		clear(body)
		return false
	}
	matches := response.StatusCode == http.StatusOK && string(body) == expected
	clear(body)
	return matches
}

func liveGatewayRebindRuntimeLoopbackSnapshot(ctx context.Context, repository *appaccess.Repository,
	appID string,
) (appaccess.AppAccessOperatorSnapshot, error) {
	snapshot, err := repository.ReadAppAccessOperatorSnapshot(ctx, appID)
	if err != nil {
		return appaccess.AppAccessOperatorSnapshot{}, err
	}
	if !reflect.DeepEqual(snapshot, appaccess.AppAccessOperatorSnapshot{}) {
		return appaccess.AppAccessOperatorSnapshot{}, errors.New("loopback-only application has LAN access state")
	}
	return snapshot, nil
}

func (f liveGatewayRebindRuntimeFixture) String() string {
	return fmt.Sprintf("runtime rebind %s", f.inspection.Spec.OperationID)
}
