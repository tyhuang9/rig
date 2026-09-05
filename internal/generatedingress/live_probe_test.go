package generatedingress

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	liveProbeTimeout       = 5 * time.Second
	liveProbeOutputLimit   = 64 << 10
	liveProbeMaximum       = 12
	liveProbeHealthCommand = `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`
)

type liveProbeGroup string

const (
	liveProbeInit       liveProbeGroup = "init"
	liveProbeFilesystem liveProbeGroup = "filesystem"
	liveProbePIDs       liveProbeGroup = "pids"
	liveProbeCompute    liveProbeGroup = "compute"
	liveProbeSecurity   liveProbeGroup = "security"
	liveProbeIdentity   liveProbeGroup = "identity"
	liveProbeNetwork    liveProbeGroup = "network"
	liveProbeOperations liveProbeGroup = "operations"
)

var liveProbeGroups = []liveProbeGroup{liveProbeInit, liveProbeFilesystem, liveProbePIDs, liveProbeCompute, liveProbeSecurity, liveProbeIdentity, liveProbeNetwork, liveProbeOperations}

const liveProbeLoggingMaximum = 7

// liveProbeLoggingProfile is closed so the hosted follow-up can only vary the
// fixed logging tuple already present in the synthetic candidate.
type liveProbeLoggingProfile string

const (
	liveProbeLocalMaxSize    liveProbeLoggingProfile = "local_max_size"
	liveProbeLocalMaxFile    liveProbeLoggingProfile = "local_max_file"
	liveProbeLocalOnly       liveProbeLoggingProfile = "local_only"
	liveProbeLocalBounded    liveProbeLoggingProfile = "local_bounded"
	liveProbeBoundedJSONFile liveProbeLoggingProfile = "bounded_json_file"
)

// All fields come from the exact synthetic candidate which failed StartCandidate.
// This remains test-only; it does not change runtime production behavior.
type liveProbeConfig struct {
	runner                                                                 runtimeprocess.CommandRunner
	executable, directory                                                  string
	env                                                                    []string
	image, command, network, alias, hostname, user, workdir                string
	internalPort, healthPath, tmpfs, memory, cpus, pids, logSize, logFiles string
}

func (c liveProbeConfig) valid() bool {
	return c.runner != nil && c.executable != "" && len(c.env) == 1 && c.image != "" && c.command != "" && c.network != "" && c.alias != "" && c.hostname != "" && c.user == "node" && c.workdir != "" && c.internalPort != "" && c.healthPath != "" && c.tmpfs != "" && c.memory != "" && c.cpus != "" && c.pids != "" && c.logSize != "" && c.logFiles != ""
}

type liveProbeAttempt struct{ name, status string }
type liveProbeOutcome struct {
	status, cleanup, diagnosis string
	attempts                   []liveProbeAttempt
}

func (o liveProbeOutcome) diagnostic() string {
	s, c := o.status, o.cleanup
	if s == "" {
		s = "not_run"
	}
	if c == "" {
		c = "none"
	}
	d := o.diagnosis
	if d == "" {
		d = "none"
	}
	v := make([]string, 0, len(o.attempts))
	for _, a := range o.attempts {
		v = append(v, a.name+":"+a.status)
	}
	if len(v) == 0 {
		v = []string{"none"}
	}
	return " live_probe_status=" + s + " live_probe_cleanup=" + c + " live_probe_diagnosis=" + d + " live_probe_groups=" + strings.Join(v, ",")
}

func liveRunStartBisection(ctx context.Context, c liveProbeConfig) liveProbeOutcome {
	if !c.valid() {
		return liveProbeOutcome{status: "fixture_invalid"}
	}
	name, nonce, ok := liveProbeName()
	if !ok {
		return liveProbeOutcome{status: "nonce_unavailable"}
	}
	defer clear(name)
	defer clear(nonce)
	f, ok := newLiveProbeFixture(string(name), string(nonce))
	if !ok {
		return liveProbeOutcome{status: "fixture_invalid"}
	}
	defer f.clear()
	o := liveProbeOutcome{cleanup: "none"}
	run := func(name string, omit []liveProbeGroup) (probeResult, bool) {
		r := liveProbeRun(ctx, c, f, name, omit)
		o.attempts = append(o.attempts, liveProbeAttempt{name, r.status})
		o.cleanup = r.cleanup
		if !r.safe {
			o.status, o.cleanup = "aborted", r.cleanup
			return r, false
		}
		return r, true
	}
	all, ok := run("all_options", nil)
	if !ok {
		return o
	}
	if all.status == "pass" {
		o.status, o.cleanup = "non_reproducible", all.cleanup
		return o
	}
	base, ok := run("baseline", liveProbeGroups)
	if !ok {
		return o
	}
	if base.status != "pass" {
		o.status, o.cleanup = "baseline_failed", base.cleanup
		return o
	}
	for _, g := range liveProbeGroups {
		if len(o.attempts) >= liveProbeMaximum {
			o.status = "budget_exhausted"
			return o
		}
		r, ok := run(string(g), []liveProbeGroup{g})
		if !ok {
			return o
		}
		if r.status != "pass" {
			continue
		}
		if len(o.attempts)+2 > liveProbeMaximum {
			o.status = "confirmed_budget_exhausted"
			return o
		}
		a, ok := run("confirm_all_options", nil)
		if !ok {
			return o
		}
		b, ok := run("confirm_"+string(g), []liveProbeGroup{g})
		if !ok {
			return o
		}
		if a.status == "fail" && b.status == "pass" {
			o.status = "confirmed_" + string(g)
		} else {
			o.status = "unconfirmed_" + string(g)
		}
		return o
	}
	for i, half := range [][]liveProbeGroup{liveProbeGroups[:4], liveProbeGroups[4:]} {
		if len(o.attempts) >= liveProbeMaximum {
			break
		}
		if _, ok := run([]string{"half_one", "half_two"}[i], half); !ok {
			return o
		}
	}
	o.status = "multi_group_or_invariant"
	return o
}

// liveRunLoggingTupleSplit is a fixed, hosted-only follow-up for the confirmed
// Docker logging tuple. It keeps every non-logging candidate argument exact
// and emits only fixed labels and statuses.
func liveRunLoggingTupleSplit(ctx context.Context, c liveProbeConfig) liveProbeOutcome {
	if !c.valid() {
		return liveProbeOutcome{status: "fixture_invalid"}
	}
	name, nonce, ok := liveProbeName()
	if !ok {
		return liveProbeOutcome{status: "nonce_unavailable"}
	}
	defer clear(name)
	defer clear(nonce)
	f, ok := newLiveProbeFixture(string(name), string(nonce))
	if !ok {
		return liveProbeOutcome{status: "fixture_invalid"}
	}
	defer f.clear()
	o := liveProbeOutcome{cleanup: "none"}
	run := func(name string, profile liveProbeLoggingProfile) (probeResult, bool) {
		if len(o.attempts) >= liveProbeLoggingMaximum {
			o.status = "budget_exhausted"
			return probeResult{}, false
		}
		r := liveProbeRunLoggingProfile(ctx, c, f, name, profile)
		o.attempts = append(o.attempts, liveProbeAttempt{name, r.status})
		o.cleanup = r.cleanup
		if !r.safe {
			o.status, o.cleanup = "aborted", r.cleanup
			return r, false
		}
		return r, true
	}
	confirm := func(name string, profile liveProbeLoggingProfile, expected, mismatch string) bool {
		r, ok := run(name, profile)
		if !ok {
			return false
		}
		if !liveProbeBinaryResult(r) {
			o.status = "non_binary_result"
			return false
		}
		if r.status != expected {
			o.status = mismatch
			return false
		}
		return true
	}

	maxSize, ok := run("local_max_size", liveProbeLocalMaxSize)
	if !ok {
		return o
	}
	if !liveProbeBinaryResult(maxSize) {
		o.status = "non_binary_result"
		return o
	}
	maxFile, ok := run("local_max_file", liveProbeLocalMaxFile)
	if !ok {
		return o
	}
	if !liveProbeBinaryResult(maxFile) {
		o.status = "non_binary_result"
		return o
	}

	switch {
	case maxSize.status == "pass" && maxFile.status == "pass":
		if !confirm("confirm_local_max_size", liveProbeLocalMaxSize, "pass", "unconfirmed_option_combination") ||
			!confirm("confirm_local_max_file", liveProbeLocalMaxFile, "pass", "unconfirmed_option_combination") {
			return o
		}
		localBounded, ok := run("local_bounded", liveProbeLocalBounded)
		if !ok {
			return o
		}
		if !liveProbeBinaryResult(localBounded) {
			o.status = "non_binary_result"
			return o
		}
		if localBounded.status != "fail" {
			o.status = "unconfirmed_option_combination"
			return o
		}
		o.diagnosis = "confirmed_option_combination"
	case maxSize.status == "fail" && maxFile.status == "pass":
		if !confirm("confirm_local_max_size", liveProbeLocalMaxSize, "fail", "unconfirmed_max_size") ||
			!confirm("confirm_local_max_file", liveProbeLocalMaxFile, "pass", "unconfirmed_max_size") {
			return o
		}
		o.diagnosis = "confirmed_max_size"
	case maxSize.status == "pass" && maxFile.status == "fail":
		if !confirm("confirm_local_max_size", liveProbeLocalMaxSize, "pass", "unconfirmed_max_file") ||
			!confirm("confirm_local_max_file", liveProbeLocalMaxFile, "fail", "unconfirmed_max_file") {
			return o
		}
		o.diagnosis = "confirmed_max_file"
	case maxSize.status == "fail" && maxFile.status == "fail":
		localOnly, ok := run("local_only", liveProbeLocalOnly)
		if !ok {
			return o
		}
		if !liveProbeBinaryResult(localOnly) {
			o.status = "non_binary_result"
			return o
		}
		if localOnly.status == "fail" {
			if !confirm("confirm_local_only", liveProbeLocalOnly, "fail", "unconfirmed_local_driver") {
				return o
			}
			o.diagnosis = "confirmed_local_driver"
		} else if !confirm("confirm_local_max_size", liveProbeLocalMaxSize, "fail", "unconfirmed_multiple_log_options") ||
			!confirm("confirm_local_max_file", liveProbeLocalMaxFile, "fail", "unconfirmed_multiple_log_options") {
			return o
		} else {
			o.diagnosis = "observed_multiple_log_options"
		}
	}

	boundedJSON, ok := run("bounded_json_file", liveProbeBoundedJSONFile)
	if !ok {
		return o
	}
	if !liveProbeBinaryResult(boundedJSON) {
		o.status = "non_binary_result"
		return o
	}
	confirmedJSON, ok := run("confirm_bounded_json_file", liveProbeBoundedJSONFile)
	if !ok {
		return o
	}
	if !liveProbeBinaryResult(confirmedJSON) {
		o.status = "non_binary_result"
		return o
	}
	switch {
	case boundedJSON.status == "pass" && confirmedJSON.status == "pass":
		o.status = "confirmed_bounded_json_file"
	case boundedJSON.status == "fail" && confirmedJSON.status == "fail":
		o.status = "confirmed_json_file_incompatible"
	default:
		o.status = "unconfirmed_bounded_json_file"
	}
	return o
}

func liveProbeBinaryResult(r probeResult) bool {
	return r.status == "pass" || r.status == "fail"
}

type liveProbeFixture struct{ name, nonce string }

func (f *liveProbeFixture) clear() { f.name = ""; f.nonce = "" }
func liveProbeName() ([]byte, []byte, bool) {
	raw := make([]byte, 12)
	if _, e := io.ReadFull(cryptorand.Reader, raw); e != nil {
		clear(raw)
		return nil, nil, false
	}
	nonce := make([]byte, hex.EncodedLen(len(raw)))
	hex.Encode(nonce, raw)
	clear(raw)
	name := make([]byte, len("rig-live-probe-")+len(nonce))
	copy(name, "rig-live-probe-")
	copy(name[len("rig-live-probe-"):], nonce)
	return name, nonce, true
}
func newLiveProbeFixture(name, nonce string) (liveProbeFixture, bool) {
	return liveProbeFixture{name, nonce}, name != "" && len(nonce) == 24 && lowerHex(nonce)
}
func lowerHex(s string) bool {
	for _, b := range []byte(s) {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	return true
}

type probeResult struct {
	status, cleanup string
	safe            bool
}

func liveProbeRun(ctx context.Context, c liveProbeConfig, f liveProbeFixture, attempt string, omit []liveProbeGroup) probeResult {
	var ok bool
	f, ok = f.forAttempt(attempt)
	if !ok {
		return probeResult{"fixture_invalid", "none", true}
	}
	args, ok := liveProbeCreateArgs(c, f, attempt, omit)
	defer clear(args)
	if !ok || !liveValidateProbeCreate(c, f, attempt, omit, args) {
		return probeResult{"fixture_invalid", "none", true}
	}
	return liveProbeRunCreateArgs(ctx, c, f, attempt, args)
}

func liveProbeRunLoggingProfile(ctx context.Context, c liveProbeConfig, f liveProbeFixture, attempt string, profile liveProbeLoggingProfile) probeResult {
	var ok bool
	f, ok = f.forAttempt(attempt)
	if !ok {
		return probeResult{"fixture_invalid", "none", true}
	}
	args, ok := liveProbeLoggingProfileCreateArgs(c, f, attempt, profile)
	defer clear(args)
	if !ok || !liveValidateLoggingProfileCreate(c, f, attempt, profile, args) {
		return probeResult{"fixture_invalid", "none", true}
	}
	return liveProbeRunCreateArgs(ctx, c, f, attempt, args)
}

func liveProbeRunCreateArgs(ctx context.Context, c liveProbeConfig, f liveProbeFixture, attempt string, args []string) probeResult {
	create, createErr := liveProbeCommand(ctx, c, args)
	createTruncated := create.StdoutTruncated || create.StderrTruncated
	id := copyProbeID(create.Stdout)
	clearLiveCommandResult(&create)
	if createErr != nil || createTruncated || !liveContainerID(id) {
		clear(id)
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveProbeTimeout)
		id = liveProbeRecoverID(recoveryCtx, c, f.name)
		if !liveContainerID(id) {
			cancel()
			clear(id)
			return probeResult{"create_failed", "not_attempted", false}
		}
		defer clear(id)
		if !liveProbeAttest(recoveryCtx, c, id, f, attempt) {
			cancel()
			return probeResult{"create_failed", "attestation_failed", false}
		}
		cancel()
		cleanup, safe := liveProbeCleanup(c, id, f, attempt)
		_ = safe
		return probeResult{"create_failed", cleanup, false}
	}
	defer clear(id)
	attestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveProbeTimeout)
	attested := liveProbeAttest(attestCtx, c, id, f, attempt)
	cancel()
	if !attested {
		return probeResult{"attestation_failed", "attestation_failed", false}
	}
	if ctx.Err() != nil {
		cleanup, _ := liveProbeCleanup(c, id, f, attempt)
		return probeResult{"inconclusive", cleanup, false}
	}
	startArgs := []string{"container", "start", string(id)}
	start, startErr := liveProbeCommand(ctx, c, startArgs)
	startTruncated := start.StdoutTruncated || start.StderrTruncated
	clear(startArgs)
	clearLiveCommandResult(&start)
	if startTruncated || ctx.Err() != nil || errors.Is(startErr, context.Canceled) || errors.Is(startErr, context.DeadlineExceeded) || errors.Is(startErr, runtimeprocess.ErrTerminationFailed) {
		cleanup, _ := liveProbeCleanup(c, id, f, attempt)
		return probeResult{"inconclusive", cleanup, false}
	}
	status := "pass"
	if startErr != nil {
		status = "fail"
	}
	cleanup, safe := liveProbeCleanup(c, id, f, attempt)
	return probeResult{status, cleanup, safe}
}

func (f liveProbeFixture) forAttempt(attempt string) (liveProbeFixture, bool) {
	if !validAttempt(attempt) {
		return liveProbeFixture{}, false
	}
	return liveProbeFixture{name: f.name + "-" + attempt, nonce: f.nonce}, true
}

// Cleanup deliberately owns a fresh bounded context: a cancelled start must not
// strand an attested probe. It re-attests, removes by exact ID, and verifies absence.
func liveProbeCleanup(c liveProbeConfig, id []byte, f liveProbeFixture, attempt string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), liveProbeTimeout)
	defer cancel()
	if !liveProbeAttest(ctx, c, id, f, attempt) {
		return "reattest_failed", false
	}
	args := []string{"container", "rm", "--force", string(id)}
	r, e := liveProbeCommand(ctx, c, args)
	clear(args)
	clearLiveCommandResult(&r)
	list := []string{"container", "ls", "--all", "--quiet", "--filter", "id=" + string(id)}
	a, ae := liveProbeCommand(ctx, c, list)
	clear(list)
	absent := ae == nil && !a.StdoutTruncated && !a.StderrTruncated && len(bytes.TrimSpace(a.Stdout)) == 0
	clearLiveCommandResult(&a)
	if !absent {
		return "absence_failed", false
	}
	if e != nil {
		return "removed_after_remove_error", true
	}
	return "removed", true
}

func liveProbeCreateArgs(c liveProbeConfig, f liveProbeFixture, attempt string, omit []liveProbeGroup) ([]string, bool) {
	if !c.valid() || f.name == "" || f.nonce == "" || !validAttempt(attempt) {
		return nil, false
	}
	m := map[liveProbeGroup]bool{}
	for _, g := range omit {
		m[g] = true
	}
	a := []string{"container", "create", "--name", f.name, "--restart", "no", "--env", "RIG_RUNTIME_INTERNAL_PORT=" + c.internalPort, "--env", "RIG_RUNTIME_HEALTH_PATH=" + c.healthPath, "--label", "io.rig.managed=generated-runtime-probe", "--label", "io.rig.probe.session=" + f.nonce, "--label", "io.rig.probe.group=" + attempt}
	if !m[liveProbeInit] {
		a = append(a, "--init")
	}
	if !m[liveProbeFilesystem] {
		a = append(a, "--read-only", "--tmpfs", c.tmpfs)
	}
	if !m[liveProbePIDs] {
		a = append(a, "--pids-limit", c.pids)
	}
	if !m[liveProbeCompute] {
		a = append(a, "--memory", c.memory, "--memory-swap", c.memory, "--cpus", c.cpus, "--ulimit", "nofile=1024:1024")
	}
	if !m[liveProbeSecurity] {
		a = append(a, "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true")
	}
	if !m[liveProbeIdentity] {
		a = append(a, "--user", c.user, "--workdir", c.workdir, "--hostname", c.hostname)
	}
	if m[liveProbeNetwork] {
		a = append(a, "--network", "none")
	} else {
		a = append(a, "--network", c.network, "--network-alias", c.alias)
	}
	if !m[liveProbeOperations] {
		a = append(a, "--log-driver", "local", "--log-opt", "max-size="+c.logSize, "--log-opt", "max-file="+c.logFiles, "--health-cmd", liveProbeHealthCommand, "--health-interval", "2s", "--health-timeout", "2s", "--health-start-period", "5s", "--health-retries", "3")
	}
	return append(a, c.image, "/bin/sh", "-lc", c.command), true
}

func liveProbeLoggingProfileCreateArgs(c liveProbeConfig, f liveProbeFixture, attempt string, profile liveProbeLoggingProfile) ([]string, bool) {
	args, ok := liveProbeCreateArgs(c, f, attempt, nil)
	if !ok {
		return nil, false
	}
	logging := liveProbeLoggingProfileArgs(c, profile)
	if logging == nil {
		clear(args)
		return nil, false
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--log-driver" && args[i+1] == "local" {
			end := i
			for end < len(args) && args[end] != "--health-cmd" {
				end++
			}
			if end == len(args) {
				clear(args)
				clear(logging)
				return nil, false
			}
			replaced := make([]string, 0, len(args)-end+i+len(logging))
			replaced = append(replaced, args[:i]...)
			replaced = append(replaced, logging...)
			replaced = append(replaced, args[end:]...)
			clear(args)
			clear(logging)
			return replaced, true
		}
	}
	clear(args)
	clear(logging)
	return nil, false
}

func liveProbeLoggingProfileArgs(c liveProbeConfig, profile liveProbeLoggingProfile) []string {
	switch profile {
	case liveProbeLocalMaxSize:
		return []string{"--log-driver", "local", "--log-opt", "max-size=" + c.logSize}
	case liveProbeLocalMaxFile:
		return []string{"--log-driver", "local", "--log-opt", "max-file=" + c.logFiles}
	case liveProbeLocalOnly:
		return []string{"--log-driver", "local"}
	case liveProbeLocalBounded:
		return []string{"--log-driver", "local", "--log-opt", "max-size=" + c.logSize, "--log-opt", "max-file=" + c.logFiles}
	case liveProbeBoundedJSONFile:
		return []string{"--log-driver", "json-file", "--log-opt", "max-size=" + c.logSize, "--log-opt", "max-file=" + c.logFiles}
	default:
		return nil
	}
}
func validAttempt(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, b := range []byte(s) {
		if !((b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_') {
			return false
		}
	}
	return true
}
func liveValidateProbeCreate(c liveProbeConfig, f liveProbeFixture, attempt string, omit []liveProbeGroup, args []string) bool {
	expected, ok := liveProbeCreateArgs(c, f, attempt, omit)
	defer clear(expected)
	return ok && sameArgs(args, expected)
}
func liveValidateLoggingProfileCreate(c liveProbeConfig, f liveProbeFixture, attempt string, profile liveProbeLoggingProfile, args []string) bool {
	expected, ok := liveProbeLoggingProfileCreateArgs(c, f, attempt, profile)
	defer clear(expected)
	return ok && sameArgs(args, expected)
}
func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func hasArgs(a []string, w ...string) bool {
	for i := 0; i+len(w) <= len(a); i++ {
		if sameArgs(a[i:i+len(w)], w) {
			return true
		}
	}
	return false
}
func liveProbeCommand(ctx context.Context, c liveProbeConfig, args []string) (runtimeprocess.CommandResult, error) {
	return c.runner.Run(ctx, runtimeprocess.CommandRequest{Executable: c.executable, Args: args, Directory: c.directory, Env: c.env, Timeout: liveProbeTimeout, OutputLimit: liveProbeOutputLimit})
}
func copyProbeID(o []byte) []byte {
	id := bytes.TrimSpace(o)
	if !liveContainerID(id) {
		return nil
	}
	return append([]byte(nil), id...)
}
func liveProbeRecoverID(ctx context.Context, c liveProbeConfig, name string) []byte {
	args := []string{"container", "inspect", "--format", "{{.ID}}", name}
	r, e := liveProbeCommand(ctx, c, args)
	clear(args)
	defer clearLiveCommandResult(&r)
	if e != nil || r.StdoutTruncated || r.StderrTruncated {
		return nil
	}
	return copyProbeID(r.Stdout)
}
func liveProbeAttest(ctx context.Context, c liveProbeConfig, id []byte, f liveProbeFixture, attempt string) bool {
	const format = "{{.ID}}\n{{.Name}}\n{{index .Config.Labels \"io.rig.managed\"}}\n{{index .Config.Labels \"io.rig.probe.session\"}}\n{{index .Config.Labels \"io.rig.probe.group\"}}"
	args := []string{"container", "inspect", "--format", format, string(id)}
	r, e := liveProbeCommand(ctx, c, args)
	clear(args)
	defer clearLiveCommandResult(&r)
	if e != nil || r.StdoutTruncated || r.StderrTruncated {
		return false
	}
	name := make([]byte, len(f.name)+1)
	name[0] = '/'
	copy(name[1:], f.name)
	defer clear(name)
	lines := bytes.Split(bytes.TrimSpace(r.Stdout), []byte("\n"))
	return len(lines) == 5 && bytes.Equal(lines[0], id) && bytes.Equal(lines[1], name) && bytes.Equal(lines[2], []byte("generated-runtime-probe")) && bytes.Equal(lines[3], []byte(f.nonce)) && bytes.Equal(lines[4], []byte(attempt))
}

var errLiveProbe = errors.New("live probe command failed")

func testProbeConfig(r runtimeprocess.CommandRunner) liveProbeConfig {
	return liveProbeConfig{runner: r, executable: "docker", directory: "test", env: []string{"DOCKER_CONFIG=test"}, image: "sha256:" + strings.Repeat("a", 64), command: "node server.mjs", network: "rig-private", alias: "runtime-blue", hostname: "runtime-blue-container", user: "node", workdir: "/workspace", internalPort: "8080", healthPath: "/healthz", tmpfs: "/tmp:rw,noexec,nosuid,nodev,size=16777216", memory: "134217728", cpus: "0.500", pids: "128", logSize: "1m", logFiles: "2"}
}
func testProbeFixture(t *testing.T) liveProbeFixture {
	t.Helper()
	f, ok := newLiveProbeFixture("rig-live-probe-test", "0123456789abcdef01234567")
	if !ok {
		t.Fatal("fixture")
	}
	t.Cleanup(f.clear)
	return f
}

func TestLiveProbeFixtureFidelityAndGroupOmission(t *testing.T) {
	c := testProbeConfig(&liveProbeRunner{})
	f := testProbeFixture(t)
	a, ok := liveProbeCreateArgs(c, f, "all_options", nil)
	if !ok || !liveValidateProbeCreate(c, f, "all_options", nil, a) {
		t.Fatal("valid fixture rejected")
	}
	for _, w := range []string{c.image, c.command, c.network, c.alias, c.hostname, "--cpus", "0.500", "--user", "node", liveProbeHealthCommand, "io.rig.managed=generated-runtime-probe"} {
		if !hasArgs(a, w) {
			t.Fatalf("missing exact field %q", w)
		}
	}
	clear(a)
	for _, g := range liveProbeGroups {
		a, ok = liveProbeCreateArgs(c, f, string(g), []liveProbeGroup{g})
		if !ok || !liveValidateProbeCreate(c, f, string(g), []liveProbeGroup{g}, a) {
			t.Fatalf("invalid group %s", g)
		}
		if g == liveProbeNetwork {
			if !hasArgs(a, "--network", "none") || hasArgs(a, "--network-alias", c.alias) {
				t.Fatal("network omission")
			}
		} else if !hasArgs(a, "--network", c.network, "--network-alias", c.alias) {
			t.Fatal("network drift")
		}
		clear(a)
	}
}

func TestLiveProbeRejectsRequestDrift(t *testing.T) {
	c := testProbeConfig(&liveProbeRunner{})
	f := testProbeFixture(t)
	a, _ := liveProbeCreateArgs(c, f, "all_options", nil)
	defer clear(a)
	for _, fn := range []func([]string){func(v []string) { v[6] = "--env-file" }, func(v []string) { v[len(v)-1] = "wrong" }, func(v []string) { v[15] = "io.rig.probe.group=wrong" }} {
		v := append([]string(nil), a...)
		fn(v)
		if liveValidateProbeCreate(c, f, "all_options", nil, v) {
			t.Fatal("drift accepted")
		}
		clear(v)
	}
	duplicate := append(append([]string(nil), a...), "--health-cmd", liveProbeHealthCommand)
	if liveValidateProbeCreate(c, f, "all_options", nil, duplicate) {
		t.Fatal("duplicate health flags accepted")
	}
	clear(duplicate)
}
func TestLiveProbeBisectionBounded(t *testing.T) {
	r := &liveProbeRunner{passWhenMissing: "--init"}
	o := liveRunStartBisection(context.Background(), testProbeConfig(r))
	if o.status != "confirmed_init" || len(o.attempts) != 5 || r.removes != 5 || r.concurrent {
		t.Fatalf("%+v removes=%d", o, r.removes)
	}
	if o.cleanup != "removed" {
		t.Fatalf("confirmed probe cleanup = %q, want removed", o.cleanup)
	}
	if len(r.names) != len(o.attempts) || r.names[0] == r.names[1] {
		t.Fatalf("attempt names were not unique: %#v", r.names)
	}
}

func TestLiveLoggingProfileArgumentFidelity(t *testing.T) {
	c, f := testProbeConfig(&liveProbeRunner{}), testProbeFixture(t)
	for _, test := range []struct {
		name    string
		profile liveProbeLoggingProfile
		want    []string
	}{
		{"local_max_size", liveProbeLocalMaxSize, []string{"--log-driver", "local", "--log-opt", "max-size=" + c.logSize}},
		{"local_max_file", liveProbeLocalMaxFile, []string{"--log-driver", "local", "--log-opt", "max-file=" + c.logFiles}},
		{"local_only", liveProbeLocalOnly, []string{"--log-driver", "local"}},
		{"local_bounded", liveProbeLocalBounded, []string{"--log-driver", "local", "--log-opt", "max-size=" + c.logSize, "--log-opt", "max-file=" + c.logFiles}},
		{"bounded_json_file", liveProbeBoundedJSONFile, []string{"--log-driver", "json-file", "--log-opt", "max-size=" + c.logSize, "--log-opt", "max-file=" + c.logFiles}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, ok := liveProbeCreateArgs(c, f, test.name, nil)
			if !ok {
				t.Fatal("candidate create arguments")
			}
			defer clear(candidate)
			args, ok := liveProbeLoggingProfileCreateArgs(c, f, test.name, test.profile)
			defer clear(args)
			if !ok || !liveValidateLoggingProfileCreate(c, f, test.name, test.profile, args) {
				t.Fatal("profile request was rejected")
			}
			if !sameArgs(liveProbeLoggingTuple(args), test.want) {
				t.Fatal("logging tuple mismatch")
			}
			if !sameArgs(liveProbeWithoutLogging(candidate), liveProbeWithoutLogging(args)) {
				t.Fatal("non-logging candidate arguments drifted")
			}
		})
	}
	if args, ok := liveProbeLoggingProfileCreateArgs(c, f, "invalid_profile", liveProbeLoggingProfile("invalid")); ok || args != nil {
		t.Fatal("unknown logging profile was accepted")
	}
}

func TestLiveLoggingProfileRejectsTupleDrift(t *testing.T) {
	c, f := testProbeConfig(&liveProbeRunner{}), testProbeFixture(t)
	for _, profile := range []liveProbeLoggingProfile{liveProbeLocalBounded, liveProbeBoundedJSONFile} {
		t.Run(string(profile), func(t *testing.T) {
			args, ok := liveProbeLoggingProfileCreateArgs(c, f, string(profile), profile)
			if !ok {
				t.Fatal("profile create arguments")
			}
			defer clear(args)
			otherDriver := "json-file"
			if profile == liveProbeBoundedJSONFile {
				otherDriver = "local"
			}
			for _, mutate := range []func([]string){
				func(v []string) { v[liveProbeLoggingStart(v)+1] = otherDriver },
				func(v []string) { v[liveProbeLoggingStart(v)+3] = "max-files=" + c.logFiles },
				func(v []string) { v[liveProbeLoggingStart(v)+3] = "max-size=wrong" },
				func(v []string) { i := liveProbeLoggingStart(v); v[i+3], v[i+5] = v[i+5], v[i+3] },
			} {
				v := append([]string(nil), args...)
				mutate(v)
				if liveValidateLoggingProfileCreate(c, f, string(profile), profile, v) {
					t.Fatal("logging tuple drift accepted")
				}
				clear(v)
			}
			extraAt := liveProbeLoggingEnd(args)
			extra := make([]string, 0, len(args)+2)
			extra = append(extra, args[:extraAt]...)
			extra = append(extra, "--log-opt", "max-file="+c.logFiles)
			extra = append(extra, args[extraAt:]...)
			defer clear(extra)
			if liveValidateLoggingProfileCreate(c, f, string(profile), profile, extra) {
				t.Fatal("extra logging option accepted")
			}
		})
	}
}

func liveProbeLoggingStart(args []string) int {
	for i := 0; i < len(args); i++ {
		if args[i] == "--log-driver" {
			return i
		}
	}
	return -1
}

func liveProbeLoggingEnd(args []string) int {
	for i := liveProbeLoggingStart(args); i >= 0 && i < len(args); i++ {
		if args[i] == "--health-cmd" {
			return i
		}
	}
	return -1
}

func liveProbeLoggingTuple(args []string) []string {
	start, end := liveProbeLoggingStart(args), liveProbeLoggingEnd(args)
	if start < 0 || end < start {
		return nil
	}
	return args[start:end]
}

func liveProbeWithoutLogging(args []string) []string {
	start, end := liveProbeLoggingStart(args), liveProbeLoggingEnd(args)
	if start < 0 || end < start {
		return nil
	}
	out := make([]string, 0, len(args)-end+start)
	out = append(out, args[:start]...)
	out = append(out, args[end:]...)
	return out
}

func TestLiveLoggingTupleSplitSequences(t *testing.T) {
	for _, test := range []struct {
		name, mode, status, diagnosis, cleanup, sequence string
	}{
		{"option_combination", "option_combination", "confirmed_bounded_json_file", "confirmed_option_combination", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,local_bounded,bounded_json_file,confirm_bounded_json_file"},
		{"max_size", "max_size", "confirmed_bounded_json_file", "confirmed_max_size", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,bounded_json_file,confirm_bounded_json_file"},
		{"max_file", "max_file", "confirmed_bounded_json_file", "confirmed_max_file", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,bounded_json_file,confirm_bounded_json_file"},
		{"local_driver", "local_driver", "confirmed_bounded_json_file", "confirmed_local_driver", "removed", "local_max_size,local_max_file,local_only,confirm_local_only,bounded_json_file,confirm_bounded_json_file"},
		{"multiple_log_options", "multiple_log_options", "confirmed_bounded_json_file", "observed_multiple_log_options", "removed", "local_max_size,local_max_file,local_only,confirm_local_max_size,confirm_local_max_file,bounded_json_file,confirm_bounded_json_file"},
		{"json_incompatible", "json_incompatible", "confirmed_json_file_incompatible", "confirmed_option_combination", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,local_bounded,bounded_json_file,confirm_bounded_json_file"},
		{"json_mismatch", "json_mismatch", "unconfirmed_bounded_json_file", "confirmed_option_combination", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,local_bounded,bounded_json_file,confirm_bounded_json_file"},
		{"local_bounded_pass_mismatch", "local_bounded_pass_mismatch", "unconfirmed_option_combination", "", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,local_bounded"},
		{"local_bounded_non_binary", "local_bounded_non_binary", "aborted", "", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,local_bounded"},
		{"local_bounded_unsafe", "local_bounded_unsafe", "aborted", "", "attestation_failed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file,local_bounded"},
		{"option_combination_first_confirmation_mismatch", "option_combination_first_confirmation_mismatch", "unconfirmed_option_combination", "", "removed", "local_max_size,local_max_file,confirm_local_max_size"},
		{"option_combination_second_confirmation_mismatch", "option_combination_second_confirmation_mismatch", "unconfirmed_option_combination", "", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file"},
		{"max_size_confirmation_mismatch", "max_size_confirmation_mismatch", "unconfirmed_max_size", "", "removed", "local_max_size,local_max_file,confirm_local_max_size"},
		{"max_file_confirmation_mismatch", "max_file_confirmation_mismatch", "unconfirmed_max_file", "", "removed", "local_max_size,local_max_file,confirm_local_max_size,confirm_local_max_file"},
		{"local_driver_confirmation_mismatch", "local_driver_confirmation_mismatch", "unconfirmed_local_driver", "", "removed", "local_max_size,local_max_file,local_only,confirm_local_only"},
		{"multiple_options_confirmation_mismatch", "multiple_options_confirmation_mismatch", "unconfirmed_multiple_log_options", "", "removed", "local_max_size,local_max_file,local_only,confirm_local_max_size"},
		{"non_binary", "non_binary", "aborted", "", "removed", "local_max_size"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &liveProbeRunner{loggingMode: test.mode}
			outcome := liveRunLoggingTupleSplit(context.Background(), testProbeConfig(runner))
			if outcome.status != test.status || outcome.diagnosis != test.diagnosis || len(outcome.attempts) > liveProbeLoggingMaximum || outcome.cleanup != test.cleanup || runner.concurrent || len(runner.names) != len(outcome.attempts) || !liveProbeUniqueNames(runner.names) {
				t.Fatalf("outcome=%+v", outcome)
			}
			if sequence := liveProbeAttemptNames(outcome.attempts); sequence != test.sequence {
				t.Fatalf("sequence=%q", sequence)
			}
		})
	}
}

func liveProbeAttemptNames(attempts []liveProbeAttempt) string {
	names := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		names = append(names, attempt.name)
	}
	return strings.Join(names, ",")
}

func liveProbeUniqueNames(names []string) bool {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			return false
		}
		seen[name] = struct{}{}
	}
	return true
}

func TestLiveLoggingTupleSplitStopsOnOwnershipAndCleanupUncertainty(t *testing.T) {
	for _, test := range []struct {
		name   string
		runner *liveProbeRunner
	}{
		{"early_attestation", &liveProbeRunner{loggingMode: "option_combination", badAttest: true}},
		{"late_reattest", &liveProbeRunner{loggingMode: "option_combination", drift: "name"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome := liveRunLoggingTupleSplit(context.Background(), testProbeConfig(test.runner))
			if outcome.status != "aborted" || len(outcome.attempts) != 1 || test.runner.removes != 0 {
				t.Fatalf("outcome=%+v removes=%d", outcome, test.runner.removes)
			}
		})
	}
}
func TestLiveProbeRecoveryAndCleanupSafety(t *testing.T) {
	f := testProbeFixture(t)
	for _, x := range []struct {
		name    string
		r       *liveProbeRunner
		cleanup string
		safe    bool
		removes int
	}{{"create_recovery", &liveProbeRunner{createErr: true}, "removed", false, 1}, {"create_truncated", &liveProbeRunner{createTruncated: true}, "removed", false, 1}, {"mismatch", &liveProbeRunner{badAttest: true}, "attestation_failed", false, 0}, {"truncated", &liveProbeRunner{truncated: true}, "attestation_failed", false, 0}, {"inspect_error", &liveProbeRunner{attestErr: true}, "attestation_failed", false, 0}, {"remove_error_absent", &liveProbeRunner{removeErr: true}, "removed_after_remove_error", true, 1}, {"absence", &liveProbeRunner{absence: true}, "absence_failed", false, 1}, {"cancelled", &liveProbeRunner{startErr: context.Canceled}, "removed", false, 1}, {"deadline", &liveProbeRunner{startErr: context.DeadlineExceeded}, "removed", false, 1}, {"start_truncated", &liveProbeRunner{startTruncated: true}, "removed", false, 1}} {
		t.Run(x.name, func(t *testing.T) {
			got := liveProbeRun(context.Background(), testProbeConfig(x.r), f, "all_options", nil)
			if got.cleanup != x.cleanup || got.safe != x.safe || x.r.removes != x.removes {
				t.Fatalf("got=%+v removes=%d", got, x.r.removes)
			}
		})
	}
}

func TestLiveProbeCancelledCreateRecoversWithDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &liveProbeRunner{createErr: true}
	got := liveProbeRun(ctx, testProbeConfig(r), testProbeFixture(t), "all_options", nil)
	if got.cleanup != "removed" || got.safe || r.removes != 1 {
		t.Fatalf("got=%+v removes=%d", got, r.removes)
	}
}

func TestLiveProbeStartControlAndTerminationAreInconclusive(t *testing.T) {
	for _, test := range []struct {
		name   string
		ctx    context.Context
		runner *liveProbeRunner
		starts int
	}{
		{"cancelled_before_start", cancelledProbeContext(), &liveProbeRunner{startErr: errLiveProbe}, 0},
		{"termination_failed", context.Background(), &liveProbeRunner{startErr: runtimeprocess.ErrTerminationFailed}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := liveProbeRun(test.ctx, testProbeConfig(test.runner), testProbeFixture(t), "all_options", nil)
			if got.status != "inconclusive" || got.cleanup != "removed" || got.safe || test.runner.removes != 1 || test.runner.starts != test.starts {
				t.Fatalf("got=%+v removes=%d starts=%d", got, test.runner.removes, test.runner.starts)
			}
		})
	}
}

func cancelledProbeContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestLiveProbeCleanupRejectsAttestationDrift(t *testing.T) {
	for _, drift := range []string{"name", "nonce", "group"} {
		t.Run(drift, func(t *testing.T) {
			runner := &liveProbeRunner{drift: drift}
			got := liveProbeRun(context.Background(), testProbeConfig(runner), testProbeFixture(t), "all_options", nil)
			if got.cleanup != "reattest_failed" || got.safe || runner.removes != 0 {
				t.Fatalf("got=%+v removes=%d", got, runner.removes)
			}
		})
	}
}
func TestLiveProbeDiagnosticRedacts(t *testing.T) {
	d := (liveProbeOutcome{status: "confirmed_bounded_json_file", cleanup: "removed", diagnosis: "observed_multiple_log_options", attempts: []liveProbeAttempt{{"local_max_size", "fail"}}}).diagnostic()
	for _, s := range []string{strings.Repeat("a", 64), "name", "/path", "command", "SECRET=value", "image", "label", "raw-error"} {
		if strings.Contains(d, s) {
			t.Fatal("canary leaked")
		}
	}
}

type liveProbeRunner struct {
	passWhenMissing, name, nonce, group, drift, loggingMode                                                          string
	names                                                                                                            []string
	active, hasPass, createErr, createTruncated, badAttest, truncated, attestErr, removeErr, absence, startTruncated bool
	startErr                                                                                                         error
	creates, removes, starts, attests                                                                                int
	running, concurrent                                                                                              bool
}

func (r *liveProbeRunner) Run(ctx context.Context, q runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if r.running {
		r.concurrent = true
	}
	r.running = true
	defer func() { r.running = false }()
	if q.Timeout > liveProbeTimeout || q.OutputLimit > liveProbeOutputLimit || len(q.Args) < 2 || q.Args[0] != "container" {
		return runtimeprocess.CommandResult{}, errLiveProbe
	}
	switch q.Args[1] {
	case "create":
		r.creates++
		r.name = q.Args[3]
		r.names = append(r.names, r.name)
		r.active = true
		r.hasPass = hasArgs(q.Args, r.passWhenMissing)
		for i := 0; i+1 < len(q.Args); i++ {
			if q.Args[i] == "--label" {
				if strings.HasPrefix(q.Args[i+1], "io.rig.probe.session=") {
					r.nonce = strings.TrimPrefix(q.Args[i+1], "io.rig.probe.session=")
				} else if strings.HasPrefix(q.Args[i+1], "io.rig.probe.group=") {
					r.group = strings.TrimPrefix(q.Args[i+1], "io.rig.probe.group=")
				}
			}
		}
		if r.createErr {
			return runtimeprocess.CommandResult{Stdout: []byte("bad")}, errLiveProbe
		}
		if r.createTruncated {
			return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("b", 64)), StdoutTruncated: true}, nil
		}
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("b", 64))}, nil
	case "start":
		r.starts++
		if r.startTruncated {
			return runtimeprocess.CommandResult{StdoutTruncated: true}, errLiveProbe
		}
		if r.startErr != nil {
			return runtimeprocess.CommandResult{}, r.startErr
		}
		if r.loggingInconclusive() {
			return runtimeprocess.CommandResult{}, context.Canceled
		}
		if r.loggingFailure() {
			return runtimeprocess.CommandResult{}, errLiveProbe
		}
		if r.hasPass {
			return runtimeprocess.CommandResult{}, errLiveProbe
		}
		return runtimeprocess.CommandResult{}, nil
	case "inspect":
		if ctx.Err() != nil {
			return runtimeprocess.CommandResult{}, ctx.Err()
		}
		if len(q.Args) > 3 && q.Args[3] == "{{.ID}}" {
			return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("b", 64))}, nil
		}
		r.attests++
		if r.attestErr {
			return runtimeprocess.CommandResult{}, errLiveProbe
		}
		if r.truncated {
			return runtimeprocess.CommandResult{StdoutTruncated: true}, nil
		}
		if r.badAttest || (r.loggingMode == "local_bounded_unsafe" && r.group == "local_bounded") {
			return runtimeprocess.CommandResult{Stdout: []byte("bad")}, nil
		}
		if r.attests > 1 {
			name, nonce, group := r.name, r.nonce, r.group
			switch r.drift {
			case "name":
				name = "wrong"
			case "nonce":
				nonce = "wrong"
			case "group":
				group = "wrong"
			}
			return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("b", 64) + "\n/" + name + "\ngenerated-runtime-probe\n" + nonce + "\n" + group + "\n")}, nil
		}
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("b", 64) + "\n/" + r.name + "\ngenerated-runtime-probe\n" + r.nonce + "\n" + r.group + "\n")}, nil
	case "rm":
		r.removes++
		r.active = false
		if r.removeErr {
			return runtimeprocess.CommandResult{}, errLiveProbe
		}
		return runtimeprocess.CommandResult{}, nil
	case "ls":
		if r.absence || r.active {
			return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("b", 64))}, nil
		}
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errLiveProbe
}

func (r *liveProbeRunner) loggingFailure() bool {
	switch r.loggingMode {
	case "option_combination":
		return r.group == "local_bounded"
	case "max_size":
		return r.group == "local_max_size" || r.group == "confirm_local_max_size"
	case "max_file":
		return r.group == "local_max_file" || r.group == "confirm_local_max_file"
	case "local_driver":
		return r.group == "local_max_size" || r.group == "local_max_file" || r.group == "local_only" || r.group == "confirm_local_only"
	case "multiple_log_options":
		return r.group == "local_max_size" || r.group == "local_max_file" || r.group == "confirm_local_max_size" || r.group == "confirm_local_max_file"
	case "json_incompatible":
		return r.group == "local_bounded" || r.group == "bounded_json_file" || r.group == "confirm_bounded_json_file"
	case "json_mismatch":
		return r.group == "local_bounded" || r.group == "confirm_bounded_json_file"
	case "option_combination_first_confirmation_mismatch":
		return r.group == "confirm_local_max_size"
	case "option_combination_second_confirmation_mismatch":
		return r.group == "confirm_local_max_file"
	case "max_size_confirmation_mismatch":
		return r.group == "local_max_size"
	case "max_file_confirmation_mismatch":
		return r.group == "local_max_file"
	case "local_driver_confirmation_mismatch":
		return r.group == "local_max_size" || r.group == "local_max_file" || r.group == "local_only"
	case "multiple_options_confirmation_mismatch":
		return r.group == "local_max_size" || r.group == "local_max_file"
	default:
		return false
	}
}

func (r *liveProbeRunner) loggingInconclusive() bool {
	return (r.loggingMode == "non_binary" && r.group == "local_max_size") ||
		(r.loggingMode == "local_bounded_non_binary" && r.group == "local_bounded")
}
