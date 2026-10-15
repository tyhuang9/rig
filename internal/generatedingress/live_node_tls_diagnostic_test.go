package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	liveNodeTLSDiagnosticFlag      = "rig-live-node-tls-diagnostic"
	liveNodeTLSDiagnosticProtocol  = "1"
	liveNodeTLSDiagnosticLimit     = 512
	liveNodeTLSDiagnosticMaxPhases = 7
	liveNodeTLSStartupAllowance    = 15 * time.Second
	liveNodeTLSCompletionAllowance = 5 * time.Second
	liveNodeTLSKillReapAllowance   = 2 * time.Second
	liveNodeTLSProbeHelperMode     = "RIG_TEST_LIVE_NODE_TLS_PROBE_HELPER_MODE"
)

type liveNodeTLSDiagnosticStderrBuffer struct {
	mu              sync.Mutex
	limit           int
	value           []byte
	truncated       bool
	startupObserved chan struct{}
	startupOnce     sync.Once
}

func newLiveNodeTLSDiagnosticStderrBuffer(limit int) *liveNodeTLSDiagnosticStderrBuffer {
	return &liveNodeTLSDiagnosticStderrBuffer{limit: limit, startupObserved: make(chan struct{})}
}

func (buffer *liveNodeTLSDiagnosticStderrBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(buffer.value) >= buffer.limit {
		if len(value) != 0 {
			buffer.truncated = true
		}
		return len(value), nil
	}
	remaining := buffer.limit - len(buffer.value)
	if len(value) > remaining {
		buffer.value = append(buffer.value, value[:remaining]...)
		buffer.truncated = true
		return len(value), nil
	}
	buffer.value = append(buffer.value, value...)
	buffer.observeStartupLocked()
	return len(value), nil
}

func (buffer *liveNodeTLSDiagnosticStderrBuffer) observeStartupLocked() {
	if buffer.truncated {
		return
	}
	newline := bytes.IndexByte(buffer.value, '\n')
	if newline < 0 {
		return
	}
	diagnostic := liveNodeTLSDiagnosticEvidence(buffer.value[:newline+1], false)
	if diagnostic.context == liveNodeTLSDiagnosticContextStartup {
		buffer.startupOnce.Do(func() { close(buffer.startupObserved) })
	}
}

func (buffer *liveNodeTLSDiagnosticStderrBuffer) StartupObserved() <-chan struct{} {
	return buffer.startupObserved
}

func (buffer *liveNodeTLSDiagnosticStderrBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.value...)
}

func (buffer *liveNodeTLSDiagnosticStderrBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}

type liveNodeTLSDiagnosticContextCategory string

const (
	liveNodeTLSDiagnosticContextUnknown           liveNodeTLSDiagnosticContextCategory = "unknown"
	liveNodeTLSDiagnosticContextNoObservedJSEntry liveNodeTLSDiagnosticContextCategory = "no-observed-js-entry"
	liveNodeTLSDiagnosticContextStartup           liveNodeTLSDiagnosticContextCategory = "startup"
	liveNodeTLSDiagnosticContextRequestCreated    liveNodeTLSDiagnosticContextCategory = "request-created"
	liveNodeTLSDiagnosticContextRequestSent       liveNodeTLSDiagnosticContextCategory = "request-sent"
	liveNodeTLSDiagnosticContextResponse          liveNodeTLSDiagnosticContextCategory = "response"
	liveNodeTLSDiagnosticContextResponseEnd       liveNodeTLSDiagnosticContextCategory = "response-end"
	liveNodeTLSDiagnosticContextRequestError      liveNodeTLSDiagnosticContextCategory = "request-error"
	liveNodeTLSDiagnosticContextTimer             liveNodeTLSDiagnosticContextCategory = "timer"
	liveNodeTLSDiagnosticContextStdoutCallback    liveNodeTLSDiagnosticContextCategory = "stdout-callback"
)

var liveNodeTLSDiagnosticPhaseSequences = [][]liveNodeTLSDiagnosticContextCategory{
	{
		liveNodeTLSDiagnosticContextStartup,
		liveNodeTLSDiagnosticContextRequestCreated,
		liveNodeTLSDiagnosticContextRequestSent,
		liveNodeTLSDiagnosticContextResponse,
		liveNodeTLSDiagnosticContextResponseEnd,
		liveNodeTLSDiagnosticContextStdoutCallback,
	},
	{
		liveNodeTLSDiagnosticContextStartup,
		liveNodeTLSDiagnosticContextRequestCreated,
		liveNodeTLSDiagnosticContextRequestSent,
		liveNodeTLSDiagnosticContextRequestError,
		liveNodeTLSDiagnosticContextStdoutCallback,
	},
	{
		liveNodeTLSDiagnosticContextStartup,
		liveNodeTLSDiagnosticContextRequestCreated,
		liveNodeTLSDiagnosticContextRequestSent,
		liveNodeTLSDiagnosticContextTimer,
		liveNodeTLSDiagnosticContextRequestError,
		liveNodeTLSDiagnosticContextStdoutCallback,
	},
	{
		liveNodeTLSDiagnosticContextStartup,
		liveNodeTLSDiagnosticContextRequestCreated,
		liveNodeTLSDiagnosticContextRequestSent,
		liveNodeTLSDiagnosticContextResponse,
		liveNodeTLSDiagnosticContextRequestError,
		liveNodeTLSDiagnosticContextStdoutCallback,
	},
	{
		liveNodeTLSDiagnosticContextStartup,
		liveNodeTLSDiagnosticContextRequestCreated,
		liveNodeTLSDiagnosticContextRequestSent,
		liveNodeTLSDiagnosticContextResponse,
		liveNodeTLSDiagnosticContextTimer,
		liveNodeTLSDiagnosticContextRequestError,
		liveNodeTLSDiagnosticContextStdoutCallback,
	},
}

type liveNodeTLSDiagnostic struct {
	context   liveNodeTLSDiagnosticContextCategory
	nodeMajor string
}

func liveNodeTLSDiagnosticEvidence(stderr []byte, truncated bool) liveNodeTLSDiagnostic {
	unknown := liveNodeTLSDiagnostic{context: liveNodeTLSDiagnosticContextUnknown, nodeMajor: "unknown"}
	if truncated || len(stderr) > liveNodeTLSDiagnosticLimit {
		return unknown
	}
	if len(stderr) == 0 {
		return liveNodeTLSDiagnostic{context: liveNodeTLSDiagnosticContextNoObservedJSEntry, nodeMajor: "unknown"}
	}
	if !bytes.Contains(stderr, []byte(liveNodeTLSDiagnosticFlag)) {
		return unknown
	}
	if stderr[len(stderr)-1] != '\n' {
		return unknown
	}
	lines := strings.Split(string(stderr[:len(stderr)-1]), "\n")
	if len(lines) == 0 || len(lines) > liveNodeTLSDiagnosticMaxPhases {
		return unknown
	}
	phases := make([]liveNodeTLSDiagnosticContextCategory, 0, len(lines))
	nodeMajor := "unknown"
	for index, line := range lines {
		fields := strings.Split(line, " ")
		if len(fields) < 3 || fields[0] != liveNodeTLSDiagnosticFlag || fields[1] != liveNodeTLSDiagnosticProtocol {
			return unknown
		}
		phase := liveNodeTLSDiagnosticContextCategory(fields[2])
		if index == 0 {
			if len(fields) != 4 || phase != liveNodeTLSDiagnosticContextStartup {
				return unknown
			}
			major, valid := liveNodeTLSDiagnosticNodeMajorVersion(fields[3])
			if !valid {
				return unknown
			}
			nodeMajor = major
		} else if len(fields) != 3 {
			return unknown
		}
		phases = append(phases, phase)
	}
	for _, sequence := range liveNodeTLSDiagnosticPhaseSequences {
		if len(phases) > len(sequence) {
			continue
		}
		matched := true
		for index := range phases {
			if phases[index] != sequence[index] {
				matched = false
				break
			}
		}
		if matched {
			return liveNodeTLSDiagnostic{context: phases[len(phases)-1], nodeMajor: nodeMajor}
		}
	}
	return unknown
}

func liveNodeTLSDiagnosticContext(stderr []byte, truncated bool) liveNodeTLSDiagnosticContextCategory {
	return liveNodeTLSDiagnosticEvidence(stderr, truncated).context
}

func liveNodeTLSDiagnosticNodeMajorVersion(value string) (string, bool) {
	if value == "" || len(value) > 5 || (len(value) > 1 && value[0] == '0') {
		return "", false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	version, err := strconv.ParseUint(value, 10, 16)
	return value, err == nil && version > 0
}

func liveNodeTLSDiagnosticProcessOutcome(err error) string {
	switch {
	case err == nil:
		return "completed"
	default:
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return "exit-nonzero"
		}
		return "start-or-io-failure"
	}
}

func liveNodeTLSDiagnosticParentContext(ctx context.Context) string {
	switch ctx.Err() {
	case nil:
		return "active"
	case context.DeadlineExceeded:
		return "parent-deadline"
	default:
		return "parent-canceled"
	}
}

func TestLiveNodeTLSDiagnosticContext(t *testing.T) {
	validResponse := []byte("rig-live-node-tls-diagnostic 1 startup 22\n" +
		"rig-live-node-tls-diagnostic 1 request-created\n" +
		"rig-live-node-tls-diagnostic 1 request-sent\n" +
		"rig-live-node-tls-diagnostic 1 response\n" +
		"rig-live-node-tls-diagnostic 1 response-end\n" +
		"rig-live-node-tls-diagnostic 1 stdout-callback\n")
	if got := liveNodeTLSDiagnosticEvidence(validResponse, false).nodeMajor; got != "22" {
		t.Fatalf("node major=%s want=22", got)
	}
	for _, test := range []struct {
		name      string
		stderr    []byte
		truncated bool
		want      liveNodeTLSDiagnosticContextCategory
	}{
		{name: "valid response", stderr: validResponse, want: liveNodeTLSDiagnosticContextStdoutCallback},
		{name: "valid request error", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 20\nrig-live-node-tls-diagnostic 1 request-created\nrig-live-node-tls-diagnostic 1 request-sent\nrig-live-node-tls-diagnostic 1 request-error\n"), want: liveNodeTLSDiagnosticContextRequestError},
		{name: "valid timer", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 20\nrig-live-node-tls-diagnostic 1 request-created\nrig-live-node-tls-diagnostic 1 request-sent\nrig-live-node-tls-diagnostic 1 timer\n"), want: liveNodeTLSDiagnosticContextTimer},
		{name: "response then timer", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 20\nrig-live-node-tls-diagnostic 1 request-created\nrig-live-node-tls-diagnostic 1 request-sent\nrig-live-node-tls-diagnostic 1 response\nrig-live-node-tls-diagnostic 1 timer\n"), want: liveNodeTLSDiagnosticContextTimer},
		{name: "response then request error", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 20\nrig-live-node-tls-diagnostic 1 request-created\nrig-live-node-tls-diagnostic 1 request-sent\nrig-live-node-tls-diagnostic 1 response\nrig-live-node-tls-diagnostic 1 request-error\n"), want: liveNodeTLSDiagnosticContextRequestError},
		{name: "empty capture is no observed entry", stderr: nil, want: liveNodeTLSDiagnosticContextNoObservedJSEntry},
		{name: "unmarked stderr is unknown", stderr: []byte("node warning\n"), want: liveNodeTLSDiagnosticContextUnknown},
		{name: "truncated", stderr: validResponse[:20], truncated: true, want: liveNodeTLSDiagnosticContextUnknown},
		{name: "malformed", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 22\nrig-live-node-tls-diagnostic 1 request-created extra\n"), want: liveNodeTLSDiagnosticContextUnknown},
		{name: "unknown phase", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 22\nrig-live-node-tls-diagnostic 1 request-created\nrig-live-node-tls-diagnostic 1 custom\n"), want: liveNodeTLSDiagnosticContextUnknown},
		{name: "out of order", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 22\nrig-live-node-tls-diagnostic 1 request-sent\n"), want: liveNodeTLSDiagnosticContextUnknown},
		{name: "wrong protocol", stderr: []byte("rig-live-node-tls-diagnostic 2 startup 22\n"), want: liveNodeTLSDiagnosticContextUnknown},
		{name: "non numeric node version", stderr: []byte("rig-live-node-tls-diagnostic 1 startup v22\n"), want: liveNodeTLSDiagnosticContextUnknown},
		{name: "partial record", stderr: []byte("rig-live-node-tls-diagnostic 1 startup 22"), want: liveNodeTLSDiagnosticContextUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveNodeTLSDiagnosticContext(test.stderr, test.truncated); got != test.want {
				t.Fatalf("context=%s want=%s", got, test.want)
			}
		})
	}
}

func TestLiveNodeTLSDiagnosticStderrBufferBoundsOutput(t *testing.T) {
	buffer := newLiveNodeTLSDiagnosticStderrBuffer(4)
	if count, err := buffer.Write([]byte("abcdef")); err != nil || count != 6 {
		t.Fatal("bounded diagnostic writer did not acknowledge stderr")
	}
	if got := string(buffer.Bytes()); got != "abcd" || !buffer.Truncated() {
		t.Fatal("bounded diagnostic writer retained unsafe output")
	}
}

func TestLiveNodeTLSDiagnosticStderrBufferSignalsCompleteStartup(t *testing.T) {
	buffer := newLiveNodeTLSDiagnosticStderrBuffer(liveNodeTLSDiagnosticLimit)
	partial := []byte("rig-live-node-tls-diagnostic 1 startup 22")
	if count, err := buffer.Write(partial); err != nil || count != len(partial) {
		t.Fatal("diagnostic writer did not retain the partial startup record")
	}
	select {
	case <-buffer.StartupObserved():
		t.Fatal("partial startup record released the startup allowance")
	default:
	}
	if count, err := buffer.Write([]byte("\n")); err != nil || count != 1 {
		t.Fatal("diagnostic writer did not retain the startup record terminator")
	}
	select {
	case <-buffer.StartupObserved():
	default:
		t.Fatal("complete validated startup record did not release the startup allowance")
	}
}

func TestRunLiveNodeTLSProbeBoundsStartupAndCompletion(t *testing.T) {
	for _, test := range []struct {
		name                string
		mode                string
		startupAllowance    time.Duration
		completionAllowance time.Duration
		wantParentContext   string
		wantNodeContext     liveNodeTLSDiagnosticContextCategory
	}{
		{name: "no startup", mode: "no-startup", startupAllowance: 100 * time.Millisecond,
			completionAllowance: 100 * time.Millisecond, wantParentContext: "node-startup-deadline",
			wantNodeContext: liveNodeTLSDiagnosticContextNoObservedJSEntry},
		{name: "stalled after startup", mode: "stalled-after-startup", startupAllowance: 5 * time.Second,
			completionAllowance: 100 * time.Millisecond, wantParentContext: "probe-completion-deadline",
			wantNodeContext: liveNodeTLSDiagnosticContextStartup},
	} {
		t.Run(test.name, func(t *testing.T) {
			stderr := newLiveNodeTLSDiagnosticStderrBuffer(liveNodeTLSDiagnosticLimit)
			command := exec.Command(os.Args[0], "-test.run=^TestLiveNodeTLSProbeProcessHelper$")
			command.Env = append(os.Environ(), liveNodeTLSProbeHelperMode+"="+test.mode)
			command.Stderr = stderr
			_, err, parentContext := runLiveNodeTLSProbeWithAllowances(command, stderr,
				test.startupAllowance, test.completionAllowance)
			diagnostic := liveNodeTLSDiagnosticEvidence(stderr.Bytes(), stderr.Truncated())
			if err == nil || parentContext != test.wantParentContext ||
				diagnostic.context != test.wantNodeContext || command.ProcessState == nil {
				t.Fatalf("bounded probe supervision: err=%v parent=%s node=%s reaped=%t",
					err, parentContext, diagnostic.context, command.ProcessState != nil)
			}
		})
	}
}

func TestLiveNodeTLSProbeProcessHelper(t *testing.T) {
	switch os.Getenv(liveNodeTLSProbeHelperMode) {
	case "":
		return
	case "no-startup":
		time.Sleep(time.Minute)
	case "stalled-after-startup":
		_, _ = os.Stderr.WriteString("rig-live-node-tls-diagnostic 1 startup 22\n")
		time.Sleep(time.Minute)
	default:
		t.Fatal("invalid live Node TLS probe helper mode")
	}
}

func TestLiveNodeTLSDiagnosticProcessOutcome(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	for _, test := range []struct {
		name          string
		ctx           context.Context
		err           error
		wantProcess   string
		wantParentCtx string
	}{
		{name: "completed", ctx: context.Background(), wantProcess: "completed", wantParentCtx: "active"},
		{name: "exit", ctx: context.Background(), err: &exec.ExitError{}, wantProcess: "exit-nonzero", wantParentCtx: "active"},
		{name: "start failure", ctx: context.Background(), err: errors.New("private failure"), wantProcess: "start-or-io-failure", wantParentCtx: "active"},
		{name: "canceled", ctx: canceled, err: context.Canceled, wantProcess: "start-or-io-failure", wantParentCtx: "parent-canceled"},
		{name: "deadline retains exit", ctx: expired, err: &exec.ExitError{}, wantProcess: "exit-nonzero", wantParentCtx: "parent-deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveNodeTLSDiagnosticProcessOutcome(test.err); got != test.wantProcess {
				t.Fatalf("process outcome=%s want=%s", got, test.wantProcess)
			}
			if got := liveNodeTLSDiagnosticParentContext(test.ctx); got != test.wantParentCtx {
				t.Fatalf("parent context=%s want=%s", got, test.wantParentCtx)
			}
		})
	}
}
