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
	manager    *Manager
	inspection GatewayRebindProposalInspection
	input      gatewayRebindCommitInput
}

func newLiveGatewayRebindRuntimeFixture(t *testing.T) liveGatewayRebindRuntimeFixture {
	t.Helper()
	requireLiveGatewayRebindRuntime(t)
	source := newLiveGatewayRebindSourceFixture(t, liveGatewayRebindRuntimeEnvironment)
	options := source.fixture.ingress.options
	options.RebindFenceCheck = source.repository.CheckGatewayRebindFence
	options.RebindCurrentStateRepository = source.repository
	manager, err := New(source.fixture.runner, options)
	if err != nil {
		t.Fatal("open typed runtime manager")
	}
	rebind, current := newGatewayRebindRuntimeDrivers(manager)
	manager.gatewayRebindCrossStoreDriver = rebind
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
	value := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: source, manager: manager, inspection: inspection,
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
	refused bool
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
			if record.Sequence == 17 && record.Phase == gatewayRebindProgressHandoverCommitted && !d.refused {
				d.refused = true
				return errors.New("test refusal before durable sequence seventeen")
			}
			return appendProgress(appendCtx, record)
		})
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
	beforeLoopback, err := repository.CurrentAppAccess(ctx, f.loopbackID)
	if err != nil {
		t.Fatal("read source loopback access")
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
		base := f.manager.gatewayRebindCrossStoreDriver.(managerGatewayRebindCrossStoreDriver)
		driver := &liveGatewayRebindRuntimeRollbackDriver{managerGatewayRebindCrossStoreDriver: base}
		f.manager.gatewayRebindCrossStoreDriver = driver
		result, err = f.manager.commitGatewayRebind(ctx, repository, f.input)
		if err != nil || !driver.refused {
			t.Fatalf("force exact pre-sequence17 rollback: refused=%t err=%v", driver.refused, err)
		}
	} else {
		result, err = f.manager.commitGatewayRebind(ctx, repository, f.input)
		if err != nil {
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
	afterLoopback, loopErr := repository.CurrentAppAccess(ctx, f.loopbackID)
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

func (f liveGatewayRebindRuntimeFixture) String() string {
	return fmt.Sprintf("runtime rebind %s", f.inspection.Spec.OperationID)
}
