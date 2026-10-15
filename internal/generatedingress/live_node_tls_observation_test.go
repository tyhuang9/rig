package generatedingress

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type liveNodeTLSDeadlineObservation struct {
	startupObserved bool
	waitObserved    bool
	kill            string
	reapMillis      int64
}

// These separate samples describe when the deadline branch ran, not the exact
// timer deadline. Neither consumes the sole Wait result or changes termination.
func observeLiveNodeTLSDeadline(startup, completed <-chan struct{}, kill func() error, waited <-chan error) (error, liveNodeTLSDeadlineObservation) {
	observation := liveNodeTLSDeadlineObservation{}
	select {
	case <-startup:
		observation.startupObserved = true
	default:
	}
	select {
	case <-completed:
		observation.waitObserved = true
	default:
	}
	start := time.Now()
	switch err := kill(); {
	case err == nil:
		observation.kill = "returned-nil"
	case errors.Is(err, os.ErrProcessDone):
		observation.kill = "already-finished"
	default:
		observation.kill = "other-error"
	}
	err := <-waited
	observation.reapMillis = time.Since(start).Milliseconds()
	return err, observation
}

func liveNodeTLSStdoutClass(output []byte) string {
	if len(output) == 0 {
		return "empty"
	}
	result, valid := decodeLiveApplicationProbeResult(output)
	if !valid {
		return "invalid"
	}
	if liveApplicationProbeClassification(result) == liveApplicationProbeReachable && result.Status == http.StatusNoContent {
		return "reachable-204"
	}
	return "other-valid"
}

const liveNodeTLSExecutableReadLimit = 128 << 20

type liveNodeTLSExecutableObservation struct {
	status string
	size   int64
	sha256 string
}

// Inspect only after the probe. This is bounded file provenance, not an assertion
// that these exact bytes executed, and it does not warm up the selected runtime.
func liveNodeTLSExecutableEvidence(path string) liveNodeTLSExecutableObservation {
	unavailable := liveNodeTLSExecutableObservation{status: "unavailable", sha256: "unknown"}
	info, err := os.Stat(path)
	if err != nil {
		return unavailable
	}
	if !info.Mode().IsRegular() {
		return liveNodeTLSExecutableObservation{status: "nonregular", sha256: "unknown"}
	}
	if info.Size() > liveNodeTLSExecutableReadLimit {
		return liveNodeTLSExecutableObservation{status: "oversized", size: info.Size(), sha256: "unknown"}
	}
	file, err := os.Open(path)
	if err != nil {
		return unavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != info.Size() {
		return unavailable
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, liveNodeTLSExecutableReadLimit+1))
	if err != nil || count != info.Size() {
		return unavailable
	}
	return liveNodeTLSExecutableObservation{status: "recorded", size: count, sha256: fmt.Sprintf("%x", hash.Sum(nil))}
}

func liveNodeTLSProbeObservationText(path string, output []byte, stderr *liveNodeTLSDiagnosticStderrBuffer, observation liveNodeTLSDeadlineObservation) string {
	executable := liveNodeTLSExecutableEvidence(path)
	_, options := os.LookupEnv("NODE_OPTIONS")
	_, extraCA := os.LookupEnv("NODE_EXTRA_CA_CERTS")
	_, nodePath := os.LookupEnv("NODE_PATH")
	_, openssl := os.LookupEnv("OPENSSL_CONF")
	return fmt.Sprintf("host Node observation: deadline_branch_startup_observed=%t,deadline_branch_wait_observed=%t,kill=%s,kill_through_wait_ms=%d,stdout_bytes=%d,stdout_class=%s,stderr_retained_bytes=%d,stderr_truncated=%t,postrun_executable=%s,postrun_executable_bytes=%d,postrun_executable_sha256=%s,node_options_present=%t,node_extra_ca_certs_present=%t,node_path_present=%t,openssl_conf_present=%t",
		observation.startupObserved, observation.waitObserved, observation.kill, observation.reapMillis,
		len(output), liveNodeTLSStdoutClass(output), len(stderr.Bytes()), stderr.Truncated(), executable.status, executable.size, executable.sha256,
		options, extraCA, nodePath, openssl)
}

func TestLiveNodeTLSDeadlineObservation(t *testing.T) {
	for _, test := range []struct {
		name, wantKill string
		ready          bool
		killError      error
	}{
		{name: "pending", wantKill: "returned-nil"},
		{name: "already observed", ready: true, killError: os.ErrProcessDone, wantKill: "already-finished"},
		{name: "kill failure", killError: errors.New("private kill failure"), wantKill: "other-error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			startup, completed := make(chan struct{}), make(chan struct{})
			if test.ready {
				close(startup)
				close(completed)
			}
			waited := make(chan error, 1)
			waitError := errors.New("private wait failure")
			calls := 0
			err, observation := observeLiveNodeTLSDeadline(startup, completed, func() error {
				calls++
				waited <- waitError
				return test.killError
			}, waited)
			if err != waitError || calls != 1 || len(waited) != 0 || observation.kill != test.wantKill ||
				observation.startupObserved != test.ready || observation.waitObserved != test.ready || observation.reapMillis < 0 {
				t.Fatal("deadline observation changed termination, readiness, or Wait ownership")
			}
		})
	}
}

func TestLiveNodeTLSStdoutObservation(t *testing.T) {
	for _, test := range []struct{ value, want string }{
		{"", "empty"}, {"private output", "invalid"},
		{`{"outcome":"response","status":204}`, "reachable-204"},
		{`{"outcome":"response","status":200}`, "other-valid"},
		{`{"outcome":"response","status":204} {}`, "invalid"},
		{`{"outcome":"response","status":204,"private":"secret"}`, "invalid"},
	} {
		if liveNodeTLSStdoutClass([]byte(test.value)) != test.want {
			t.Fatal("stdout observation bypassed the strict decoder")
		}
	}
}

func TestLiveNodeTLSExecutableObservation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private-executable")
	if got := liveNodeTLSExecutableEvidence(path); got.status != "unavailable" || got.sha256 != "unknown" {
		t.Fatal("missing file provenance was not unavailable")
	}
	if got := liveNodeTLSExecutableEvidence(dir); got.status != "nonregular" || got.sha256 != "unknown" {
		t.Fatal("nonregular file was read")
	}
	value := []byte("private executable bytes")
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal("create fixture")
	}
	if got := liveNodeTLSExecutableEvidence(path); got.status != "recorded" || got.size != int64(len(value)) || got.sha256 != fmt.Sprintf("%x", sha256.Sum256(value)) {
		t.Fatal("file provenance did not identify fixture bytes")
	}
	if err := os.Truncate(path, liveNodeTLSExecutableReadLimit+1); err != nil {
		t.Fatal("resize fixture")
	}
	if got := liveNodeTLSExecutableEvidence(path); got.status != "oversized" || got.sha256 != "unknown" {
		t.Fatal("oversized file was read")
	}
}

func TestLiveNodeTLSObservationRedactsValues(t *testing.T) {
	for _, name := range []string{"NODE_OPTIONS", "NODE_EXTRA_CA_CERTS", "NODE_PATH", "OPENSSL_CONF"} {
		t.Setenv(name, "private environment value")
	}
	// Explicitly empty still means present.
	t.Setenv("NODE_PATH", "")
	stderr := newLiveNodeTLSDiagnosticStderrBuffer(4)
	_, _ = stderr.Write([]byte("private stderr"))
	text := liveNodeTLSProbeObservationText(filepath.Join(t.TempDir(), "private-path"), []byte("private stdout"), stderr, liveNodeTLSDeadlineObservation{kill: "not-attempted"})
	if strings.Contains(text, "private") || !strings.Contains(text, "stdout_class=invalid") || !strings.Contains(text, "stderr_truncated=true") ||
		!strings.Contains(text, "node_options_present=true") || !strings.Contains(text, "node_extra_ca_certs_present=true") ||
		!strings.Contains(text, "node_path_present=true") || !strings.Contains(text, "openssl_conf_present=true") {
		t.Fatal("observation disclosed values or lost presence/bounds")
	}
}

func TestLiveNodeTLSProbeFailedStartObservation(t *testing.T) {
	stderr := newLiveNodeTLSDiagnosticStderrBuffer(liveNodeTLSDiagnosticLimit)
	command := exec.Command(filepath.Join(t.TempDir(), "missing-node"))
	_, err, parent, observation := runLiveNodeTLSProbe(command, stderr)
	if err == nil || parent != "active" || observation.kill != "not-attempted" || command.ProcessState != nil {
		t.Fatal("failed start was reclassified or termination attempted")
	}
}

func TestLiveNodeTLSProbeCompletionObservation(t *testing.T) {
	for _, mode := range []string{"completed-response", "response-before-stall"} {
		t.Run(mode, func(t *testing.T) {
			stderr := newLiveNodeTLSDiagnosticStderrBuffer(liveNodeTLSDiagnosticLimit)
			command := exec.Command(os.Args[0], "-test.run=^TestLiveNodeTLSProbeProcessHelper$")
			command.Env = append(os.Environ(), liveNodeTLSProbeHelperMode+"="+mode)
			command.Stderr = stderr
			completion := 5 * time.Second
			if mode == "response-before-stall" {
				completion = 100 * time.Millisecond
			}
			output, err, parent, observation := runLiveNodeTLSProbeWithAllowances(command, stderr, 5*time.Second, completion)
			if liveNodeTLSStdoutClass(output) != "reachable-204" || command.ProcessState == nil {
				t.Fatal("completed response was not retained after Wait")
			}
			if mode == "completed-response" {
				if err != nil || parent != "active" || observation.kill != "not-attempted" {
					t.Fatal("normal completion was changed")
				}
			} else if err == nil || parent != "probe-completion-deadline" || observation.kill != "returned-nil" || !observation.startupObserved {
				t.Fatal("valid stdout hid the completion deadline failure")
			}
		})
	}
}
