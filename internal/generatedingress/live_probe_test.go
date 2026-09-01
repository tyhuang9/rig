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
	liveProbeLogging    liveProbeGroup = "logging"
	liveProbeHealth     liveProbeGroup = "health"
)

var liveProbeGroups = []liveProbeGroup{liveProbeInit, liveProbeFilesystem, liveProbePIDs, liveProbeCompute, liveProbeSecurity, liveProbeIdentity, liveProbeNetwork, liveProbeOperations}

const liveProbeOperationsMaximum = 7

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
	status, cleanup string
	attempts        []liveProbeAttempt
}

func (o liveProbeOutcome) diagnostic() string {
	s, c := o.status, o.cleanup
	if s == "" {
		s = "not_run"
	}
	if c == "" {
		c = "none"
	}
	v := make([]string, 0, len(o.attempts))
	for _, a := range o.attempts {
		v = append(v, a.name+":"+a.status)
	}
	if len(v) == 0 {
		v = []string{"none"}
	}
	return " live_probe_status=" + s + " live_probe_cleanup=" + c + " live_probe_groups=" + strings.Join(v, ",")
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

// liveRunOperationsSplit is a fixed, hosted-only follow-up after the broad
// candidate start failure. It narrows the combined operations group without
// changing any production runtime option and emits fixed labels only.
func liveRunOperationsSplit(ctx context.Context, c liveProbeConfig) liveProbeOutcome {
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
		if len(o.attempts) >= liveProbeOperationsMaximum {
			o.status = "budget"
			return probeResult{}, false
		}
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
		o.status = "non_reproducible"
		return o
	}
	operations, ok := run("without_operations", []liveProbeGroup{liveProbeOperations})
	if !ok {
		return o
	}
	if operations.status != "pass" {
		o.status = "operations_not_reproduced"
		return o
	}
	logging, ok := run("without_logging", []liveProbeGroup{liveProbeLogging})
	if !ok {
		return o
	}
	health, ok := run("without_health", []liveProbeGroup{liveProbeHealth})
	if !ok {
		return o
	}
	loggingPass, healthPass := logging.status == "pass", health.status == "pass"
	if !loggingPass && !healthPass {
		o.status = "neither_subgroup"
		return o
	}
	confirmAll, ok := run("confirm_all_options", nil)
	if !ok {
		return o
	}
	if confirmAll.status != "fail" {
		if loggingPass && healthPass {
			o.status = "unconfirmed_both"
		} else {
			o.status = "unconfirmed_" + firstLiveProbeSubgroup(loggingPass, healthPass)
		}
		return o
	}
	confirmedLogging, confirmedHealth := false, false
	if loggingPass {
		confirmation, ok := run("confirm_logging", []liveProbeGroup{liveProbeLogging})
		if !ok {
			return o
		}
		confirmedLogging = confirmation.status == "pass"
	}
	if healthPass {
		confirmation, ok := run("confirm_health", []liveProbeGroup{liveProbeHealth})
		if !ok {
			return o
		}
		confirmedHealth = confirmation.status == "pass"
	}
	switch {
	case confirmedLogging && confirmedHealth:
		o.status = "confirmed_both"
	case confirmedLogging:
		o.status = "confirmed_logging"
	case confirmedHealth:
		o.status = "confirmed_health"
	case loggingPass && healthPass:
		o.status = "unconfirmed_both"
	default:
		o.status = "unconfirmed_" + firstLiveProbeSubgroup(loggingPass, healthPass)
	}
	return o
}

func firstLiveProbeSubgroup(logging, health bool) string {
	if logging {
		return string(liveProbeLogging)
	}
	if health {
		return string(liveProbeHealth)
	}
	return "none"
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
	if !ok || !liveValidateProbeCreate(c, f, attempt, args) {
		return probeResult{"fixture_invalid", "none", true}
	}
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
	if !m[liveProbeOperations] && !m[liveProbeLogging] {
		a = append(a, "--log-driver", "local", "--log-opt", "max-size="+c.logSize, "--log-opt", "max-file="+c.logFiles)
	}
	if !m[liveProbeOperations] && !m[liveProbeHealth] {
		a = append(a, "--health-cmd", liveProbeHealthCommand, "--health-interval", "2s", "--health-timeout", "2s", "--health-start-period", "5s", "--health-retries", "3")
	}
	return append(a, c.image, "/bin/sh", "-lc", c.command), true
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
func liveValidateProbeCreate(c liveProbeConfig, f liveProbeFixture, attempt string, args []string) bool {
	expected, ok := liveProbeCreateArgs(c, f, attempt, omittedProbeGroups(args))
	defer clear(expected)
	return ok && sameArgs(args, expected)
}
func omittedProbeGroups(args []string) []liveProbeGroup {
	checks := []struct {
		g liveProbeGroup
		s string
	}{{liveProbeInit, "--init"}, {liveProbeFilesystem, "--read-only"}, {liveProbePIDs, "--pids-limit"}, {liveProbeCompute, "--memory"}, {liveProbeSecurity, "--cap-drop"}, {liveProbeIdentity, "--user"}}
	var out []liveProbeGroup
	for _, x := range checks {
		if !hasArgs(args, x.s) {
			out = append(out, x.g)
		}
	}
	if hasArgs(args, "--network", "none") {
		out = append(out, liveProbeNetwork)
	}
	hasLogging, hasHealth := hasArgs(args, "--log-driver"), hasArgs(args, "--health-cmd")
	switch {
	case !hasLogging && !hasHealth:
		out = append(out, liveProbeOperations)
	case !hasLogging:
		out = append(out, liveProbeLogging)
	case !hasHealth:
		out = append(out, liveProbeHealth)
	}
	return out
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
	return liveProbeConfig{runner: r, executable: "docker", directory: "test", env: []string{"DOCKER_CONFIG=test"}, image: "sha256:" + strings.Repeat("a", 64), command: "node server.mjs", network: "rig-private", alias: "runtime-blue", hostname: "runtime-blue-container", user: "node", workdir: "/workspace", internalPort: "8080", healthPath: "/healthz", tmpfs: "/tmp:rw,noexec,nosuid,nodev,size=16777216", memory: "134217728", cpus: "0.500", pids: "128", logSize: "1m", logFiles: "1"}
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
	if !ok || !liveValidateProbeCreate(c, f, "all_options", a) {
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
		if !ok || !liveValidateProbeCreate(c, f, string(g), a) {
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

func TestLiveProbeOperationsSubgroupsOmitExactBoundaries(t *testing.T) {
	c, f := testProbeConfig(&liveProbeRunner{}), testProbeFixture(t)
	for _, test := range []struct {
		name                    string
		omit                    liveProbeGroup
		wantLogging, wantHealth bool
	}{
		{"operations", liveProbeOperations, false, false},
		{"logging", liveProbeLogging, false, true},
		{"health", liveProbeHealth, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, ok := liveProbeCreateArgs(c, f, test.name, []liveProbeGroup{test.omit})
			defer clear(args)
			if !ok || !liveValidateProbeCreate(c, f, test.name, args) {
				t.Fatal("subgroup request was rejected")
			}
			if hasArgs(args, "--log-driver") != test.wantLogging || hasArgs(args, "--health-cmd") != test.wantHealth {
				t.Fatal("subgroup omitted an incorrect operation boundary")
			}
		})
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
		if liveValidateProbeCreate(c, f, "all_options", v) {
			t.Fatal("drift accepted")
		}
		clear(v)
	}
	duplicate := append(append([]string(nil), a...), "--health-cmd", liveProbeHealthCommand)
	if liveValidateProbeCreate(c, f, "all_options", duplicate) {
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

func TestLiveOperationsSplitSequences(t *testing.T) {
	for _, test := range []struct {
		name, mode, want, sequence string
	}{
		{"logging", "logging", "confirmed_logging", ""},
		{"health", "health", "confirmed_health", ""},
		{"both", "both", "confirmed_both", ""},
		{"both_logging", "both_logging", "confirmed_logging", "all_options,without_operations,without_logging,without_health,confirm_all_options,confirm_logging,confirm_health"},
		{"both_health", "both_health", "confirmed_health", "all_options,without_operations,without_logging,without_health,confirm_all_options,confirm_logging,confirm_health"},
		{"both_neither", "both_neither", "unconfirmed_both", "all_options,without_operations,without_logging,without_health,confirm_all_options,confirm_logging,confirm_health"},
		{"both_confirm_all_nonfail", "both_confirm_all_nonfail", "unconfirmed_both", "all_options,without_operations,without_logging,without_health,confirm_all_options"},
		{"neither", "neither", "neither_subgroup", ""},
		{"unconfirmed", "unconfirmed", "unconfirmed_logging", ""},
		{"not_reproduced", "not_reproduced", "operations_not_reproduced", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &liveProbeRunner{operationsMode: test.mode}
			outcome := liveRunOperationsSplit(context.Background(), testProbeConfig(runner))
			if outcome.status != test.want || len(outcome.attempts) > liveProbeOperationsMaximum || outcome.cleanup != "removed" || runner.concurrent {
				t.Fatalf("outcome=%+v", outcome)
			}
			if test.sequence != "" && liveProbeAttemptNames(outcome.attempts) != test.sequence {
				t.Fatalf("sequence=%q", liveProbeAttemptNames(outcome.attempts))
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

func TestLiveOperationsSplitStopsOnCleanupUncertainty(t *testing.T) {
	runner := &liveProbeRunner{operationsMode: "logging", drift: "name"}
	outcome := liveRunOperationsSplit(context.Background(), testProbeConfig(runner))
	if outcome.status != "aborted" || len(outcome.attempts) != 1 || runner.removes != 0 {
		t.Fatalf("outcome=%+v removes=%d", outcome, runner.removes)
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
	d := (liveProbeOutcome{status: "confirmed_init", cleanup: "removed", attempts: []liveProbeAttempt{{"init", "pass"}}}).diagnostic()
	for _, s := range []string{strings.Repeat("a", 64), "name", "/path", "command", "SECRET=value", "image", "label", "raw-error"} {
		if strings.Contains(d, s) {
			t.Fatal("canary leaked")
		}
	}
}

type liveProbeRunner struct {
	passWhenMissing, name, nonce, group, drift, operationsMode                                                                              string
	names                                                                                                                                   []string
	active, hasPass, hasLogging, hasHealth, createErr, createTruncated, badAttest, truncated, attestErr, removeErr, absence, startTruncated bool
	startErr                                                                                                                                error
	creates, removes, starts, attests                                                                                                       int
	running, concurrent                                                                                                                     bool
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
		r.hasLogging = hasArgs(q.Args, "--log-driver")
		r.hasHealth = hasArgs(q.Args, "--health-cmd")
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
		if r.operationsFailure() {
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
		if r.badAttest {
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

func (r *liveProbeRunner) operationsFailure() bool {
	switch r.operationsMode {
	case "logging":
		return r.hasLogging
	case "health":
		return r.hasHealth
	case "both":
		return r.hasLogging && r.hasHealth
	case "neither":
		return r.hasLogging || r.hasHealth
	case "unconfirmed":
		return r.hasLogging || r.group == "confirm_logging"
	case "not_reproduced":
		return true
	case "both_logging":
		return (r.hasLogging && r.hasHealth) || r.group == "confirm_health"
	case "both_health":
		return (r.hasLogging && r.hasHealth) || r.group == "confirm_logging"
	case "both_neither":
		return (r.hasLogging && r.hasHealth) || r.group == "confirm_logging" || r.group == "confirm_health"
	case "both_confirm_all_nonfail":
		return r.hasLogging && r.hasHealth && r.group != "confirm_all_options"
	default:
		return false
	}
}
