//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deploymentexecution"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const generatedDomainRegistryCaseEnvironment = "RIG_GENERATED_DOMAIN_REGISTRY_CASE"

// generatedDomainRegistryIsolated runs each irreversible-registry scenario in
// its own test process. It does not reset, replace or expose the production
// registry.
func generatedDomainRegistryIsolated(t *testing.T) bool {
	t.Helper()
	return generatedDomainRegistryIsolatedWithTimeouts(t, 90*time.Second, 2*time.Minute)
}

func generatedDomainRegistryIsolatedWithTimeouts(t *testing.T, childTimeout, outerTimeout time.Duration) bool {
	t.Helper()
	if childTimeout <= 0 || childTimeout%time.Second != 0 || outerTimeout <= childTimeout {
		t.Fatalf("invalid isolated registry timeouts: child=%s outer=%s", childTimeout, outerTimeout)
	}
	if os.Getenv(generatedDomainRegistryCaseEnvironment) == t.Name() {
		return true
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commandContext, cancel := context.WithTimeout(context.Background(), outerTimeout)
	defer cancel()
	command := exec.CommandContext(commandContext, executable,
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v",
		fmt.Sprintf("-test.timeout=%ds", childTimeout/time.Second))
	command.Env = append(os.Environ(), generatedDomainRegistryCaseEnvironment+"="+t.Name())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated registry scenario failed: commandContext=%v error=%v\n%s", commandContext.Err(), err, output)
	}
	t.Logf("isolated registry scenario output:\n%s", output)
	return false
}

// generatedDomainRegistryExpectedFailureIsolated verifies the cleanup path in
// a separate test process. The nested test intentionally fails after opening a
// pairing epoch; this outer process accepts only that known failure.
func generatedDomainRegistryExpectedFailureIsolated(t *testing.T, child string) bool {
	t.Helper()
	if os.Getenv(generatedDomainRegistryCaseEnvironment) == t.Name() {
		return true
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commandContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandContext, executable,
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v", "-test.timeout=90s")
	command.Env = append(os.Environ(), generatedDomainRegistryCaseEnvironment+"="+t.Name())
	output, err := command.CombinedOutput()
	text := string(output)
	childName := t.Name() + "/" + child
	if err == nil || commandContext.Err() != nil ||
		strings.Count(text, "=== RUN   "+t.Name()+"\n") != 1 ||
		strings.Count(text, "=== RUN   "+childName+"\n") != 1 ||
		strings.Count(text, "--- FAIL: "+childName) != 1 ||
		strings.Count(text, "--- FAIL: "+t.Name()+" ") != 1 ||
		strings.Count(text, "--- FAIL: ") != 2 ||
		!strings.Contains(text, "intentional registry fixture cleanup failure") ||
		strings.Count(text, "registry fixture cleanup control completed") != 1 ||
		strings.Contains(text, "panic: test timed out") ||
		strings.Contains(text, "cleanup did not finish") ||
		strings.Contains(text, "later reservation did not progress") {
		t.Fatalf("isolated registry cleanup control did not produce only its expected failure: commandContext=%v error=%v\n%s", commandContext.Err(), err, output)
	}
	t.Logf("isolated registry cleanup control output:\n%s", output)
	return false
}

type generatedDomainControlledRunner struct {
	mu sync.Mutex

	daemonID       string
	stdout         []byte
	stderr         []byte
	runErr         error
	truncated      bool
	waitForContext bool
	started        chan struct{}
	release        <-chan struct{}
	requests       []runtimeprocess.CommandRequest
}

func (r *generatedDomainControlledRunner) Run(
	ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	r.requests = append(r.requests, request)
	daemonID, stdout, stderr := r.daemonID, r.stdout, r.stderr
	runErr, truncated := r.runErr, r.truncated
	waitForContext, started, release := r.waitForContext, r.started, r.release
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-ctx.Done():
			return runtimeprocess.CommandResult{
				Stdout: stdout, Stderr: stderr,
				StdoutTruncated: truncated, StderrTruncated: truncated,
			}, ctx.Err()
		case <-release:
		}
	}
	if waitForContext {
		<-ctx.Done()
		return runtimeprocess.CommandResult{
			Stdout: stdout, Stderr: stderr,
			StdoutTruncated: truncated, StderrTruncated: truncated,
		}, ctx.Err()
	}
	if stdout == nil {
		stdout = []byte(fmt.Sprintf("%q", daemonID))
	}
	return runtimeprocess.CommandResult{
		Stdout: stdout, Stderr: stderr,
		StdoutTruncated: truncated, StderrTruncated: truncated,
	}, runErr
}

func (r *generatedDomainControlledRunner) configure(
	daemonID string,
	stdout, stderr []byte,
	runErr error,
	truncated, waitForContext bool,
	started chan struct{},
	release <-chan struct{},
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.daemonID = daemonID
	r.stdout = stdout
	r.stderr = stderr
	r.runErr = runErr
	r.truncated = truncated
	r.waitForContext = waitForContext
	r.started = started
	r.release = release
}

func (r *generatedDomainControlledRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func generatedDomainRegistryAssembleAdmittedManager(
	t *testing.T,
	ticket *generatedLegacyConstructionTicket,
	view *generatedManagerView,
	runner runtimeprocess.CommandRunner,
	options Options,
) *Manager {
	t.Helper()
	manager, err := newManager(runner, options)
	if err != nil {
		t.Fatal(err)
	}
	manager.domainView = view
	rebind, current := newGatewayRebindRuntimeDrivers(manager)
	manager.gatewayRebindCrossStoreDriver = rebind
	manager.gatewayCurrentPhysicalDriver = current
	if err := ticket.publish(manager); err != nil {
		t.Fatal(err)
	}
	return manager
}

func staleGeneratedDomainManager(t *testing.T, manager *Manager) {
	t.Helper()
	if manager == nil || manager.domainView == nil {
		t.Fatal("supported Manager has no shared generated-domain view")
	}
	manager.domainView.mu.Lock()
	manager.domainView.stale = true
	manager.domainView.generation++
	manager.domainView.mu.Unlock()
}

func TestGeneratedDomainRegistryLinearizesConstructionRosterAndFullPublication(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	domainFixture := newGeneratedDomainIdentityFixture(t)
	domainRunner := &generatedDomainRegistryRunner{daemonID: "registry-domain-daemon"}
	domainInput := generatedDomainRegistryInput(t, domainFixture, domainRunner, &candidateFactoryEnvironment{})

	legacyFixture := newGeneratedDomainIdentityFixture(t)
	legacyRunner := &generatedDomainRegistryRunner{daemonID: "registry-legacy-daemon"}
	legacyOptions := generatedDomainRegistryManagerOptions(legacyFixture)
	root, err := inspectGeneratedLegacyRootObservation(legacyRunner, legacyOptions)
	if err != nil {
		t.Fatal(err)
	}
	view := newGeneratedManagerView(root)
	ticket, err := admitGeneratedLegacyConstruction(view, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ticket.fail(context.Canceled) })
	if err := ticket.authorizeStateCreation(context.Background()); err != nil {
		t.Fatal(err)
	}

	type reservationResult struct {
		state *generatedDomainState
		err   error
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reservationDone := make(chan reservationResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		state, err := reserveGeneratedDomain(ctx, domainInput)
		reservationDone <- reservationResult{state: state, err: err}
	}()
	registerGeneratedDomainReservationCleanup(t, func() { ticket.fail(context.Canceled) }, cancel, finished, "linearized construction cleanup did not finish")
	waitGeneratedDomainPairingEpoch(t)

	if constructed, err := New(domainRunner, generatedDomainRegistryManagerOptions(domainFixture)); constructed != nil ||
		!IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("constructor admitted after roster closed: manager=%p error=%v", constructed, err)
	}
	if _, err := os.Lstat(filepath.Join(domainFixture.dataRoot, "runtime", "generated-ingress")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed-roster constructor created protected state: %v", err)
	}
	select {
	case result := <-reservationDone:
		t.Fatalf("reservation did not wait for admitted full construction: %+v", result)
	default:
	}

	manager := generatedDomainRegistryAssembleAdmittedManager(t, ticket, view, legacyRunner, legacyOptions)
	result := <-reservationDone
	if result.err != nil || result.state == nil || !result.state.fenceReady || result.state.failed {
		t.Fatalf("reservation after full publication: %+v", result)
	}
	view.mu.Lock()
	published, stale, pairing := view.manager, view.stale, view.pairing
	view.mu.Unlock()
	if published != manager || stale || pairing {
		t.Fatalf("noncolliding publication: manager=%p want=%p stale=%t pairing=%t", published, manager, stale, pairing)
	}
	if legacyRunner.count() != 1 {
		t.Fatalf("published legacy inspections=%d, want 1", legacyRunner.count())
	}
}

func TestGeneratedDomainLegacyConstructionTightensExistingIngressProtection(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	fixture := newGeneratedDomainIdentityFixture(t)
	ingressRoot := filepath.Join(fixture.dataRoot, "runtime", "generated-ingress")
	if err := os.MkdirAll(ingressRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ingressRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(ingressRoot)
	if err != nil {
		t.Fatal(err)
	}
	runner := &generatedDomainRegistryRunner{daemonID: "registry-existing-ingress"}
	manager, err := New(runner, generatedDomainRegistryManagerOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(ingressRoot)
	if err != nil || !os.SameFile(before, after) || manager.store == nil || manager.store.root != ingressRoot {
		t.Fatalf("existing ingress identity changed: same=%t store=%v error=%v",
			err == nil && os.SameFile(before, after), manager.store, err)
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o700 {
		t.Fatalf("existing ingress permissions=%#o, want 0700", after.Mode().Perm())
	}
	if runner.count() != 0 {
		t.Fatalf("ordinary construction daemon inspections=%d, want 0", runner.count())
	}
}

func TestGeneratedDomainRegistrySerializesInspectionDrainAndCoordinators(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	managerFixture := newGeneratedDomainIdentityFixture(t)
	managerRunner := &generatedDomainControlledRunner{daemonID: "registry-active-legacy"}
	manager, err := New(managerRunner, generatedDomainRegistryManagerOptions(managerFixture))
	if err != nil {
		t.Fatal(err)
	}

	unrelatedFixture := newGeneratedDomainIdentityFixture(t)
	unrelatedInput := generatedDomainRegistryInput(t, unrelatedFixture,
		&generatedDomainRegistryRunner{daemonID: "registry-unrelated-domain"}, &candidateFactoryEnvironment{})
	if state, err := reserveGeneratedDomain(context.Background(), unrelatedInput); err != nil || !state.fenceReady {
		t.Fatalf("unrelated reservation: state=%+v error=%v", state, err)
	}
	leave, err := manager.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	collidingInput := generatedDomainRegistryInput(t, managerFixture,
		&generatedDomainRegistryRunner{daemonID: "registry-colliding-domain"}, &candidateFactoryEnvironment{})
	secondFixture := newGeneratedDomainIdentityFixture(t)
	secondInput := generatedDomainRegistryInput(t, secondFixture,
		&generatedDomainRegistryRunner{daemonID: "registry-second-domain"}, &candidateFactoryEnvironment{})

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	managerRunner.configure("registry-active-legacy", nil, nil, nil, false, false, started, release)
	typedDone := make(chan error, 1)
	go func() {
		entered, err := manager.enterGeneratedDomainTypedMutation(context.Background())
		if entered != nil {
			entered()
		}
		typedDone <- err
	}()
	<-started

	type reservationResult struct {
		state *generatedDomainState
		err   error
	}
	firstDone := make(chan reservationResult, 1)
	go func() {
		state, err := reserveGeneratedDomain(context.Background(), collidingInput)
		firstDone <- reservationResult{state: state, err: err}
	}()
	<-started
	if entered, err := manager.enterGeneratedDomainTypedMutation(context.Background()); entered != nil ||
		!IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("late typed admission crossed active epoch: entered=%t error=%v", entered != nil, err)
	}

	closedRosterFixture := newGeneratedDomainIdentityFixture(t)
	closedRosterRunner := &generatedDomainRegistryRunner{daemonID: "registry-closed-roster"}
	if constructed, err := New(closedRosterRunner, generatedDomainRegistryManagerOptions(closedRosterFixture)); constructed != nil || !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("constructor crossed active epoch: manager=%p error=%v", constructed, err)
	}
	if _, err := os.Lstat(filepath.Join(closedRosterFixture.dataRoot, "runtime", "generated-ingress")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused constructor created protected state: %v", err)
	}
	if !manager.domainView.mu.TryLock() {
		t.Fatal("blocked identity observation retained the Manager view lock")
	}
	manager.domainView.mu.Unlock()
	if !manager.mu.TryLock() {
		t.Fatal("blocked identity observation retained the Manager gateway lock")
	}
	manager.mu.Unlock()

	secondDone := make(chan reservationResult, 1)
	go func() {
		state, err := reserveGeneratedDomain(context.Background(), secondInput)
		secondDone <- reservationResult{state: state, err: err}
	}()
	select {
	case result := <-secondDone:
		t.Fatalf("second coordinator crossed the active pairing epoch: %+v", result)
	default:
	}

	close(release)
	if err := <-typedDone; !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("inspection converted to typed activity after pairing began: %v", err)
	}
	select {
	case result := <-firstDone:
		t.Fatalf("pairing did not drain previously entered activity: %+v", result)
	default:
	}
	leave()
	leave()
	first := <-firstDone
	if first.err != nil || first.state == nil || !first.state.fenceReady {
		t.Fatalf("first coordinator result: %+v", first)
	}
	second := <-secondDone
	if second.err != nil || second.state == nil || !second.state.fenceReady {
		t.Fatalf("serialized second coordinator result: %+v", second)
	}
	manager.domainView.mu.Lock()
	typed, inspections, stale, pairing := manager.domainView.typedActive,
		manager.domainView.inspectionActive, manager.domainView.stale, manager.domainView.pairing
	manager.domainView.mu.Unlock()
	if typed != 0 || inspections != 0 || !stale || pairing {
		t.Fatalf("final activity view: typed=%d inspections=%d stale=%t pairing=%t", typed, inspections, stale, pairing)
	}
}

func TestGeneratedDomainRegistryConditionalObservationFailsClosedWithoutEffects(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	fixture := newGeneratedDomainIdentityFixture(t)
	runner := &generatedDomainControlledRunner{daemonID: "registry-observed-legacy"}
	manager, err := New(runner, generatedDomainRegistryManagerOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	unrelatedFixture := newGeneratedDomainIdentityFixture(t)
	unrelatedRunner := &generatedDomainRegistryRunner{daemonID: "registry-observed-domain"}
	unrelatedInput := generatedDomainRegistryInput(t, unrelatedFixture, unrelatedRunner, &candidateFactoryEnvironment{})
	if state, err := reserveGeneratedDomain(context.Background(), unrelatedInput); err != nil || !state.fenceReady {
		t.Fatalf("unrelated reservation: state=%+v error=%v", state, err)
	}
	routeBefore, routeBeforeErr := os.ReadFile(manager.store.path)
	if routeBeforeErr != nil && !errors.Is(routeBeforeErr, os.ErrNotExist) {
		t.Fatal(routeBeforeErr)
	}

	marker := "private-registry-runner-marker"
	tests := []struct {
		name           string
		stdout         []byte
		stderr         []byte
		runErr         error
		truncated      bool
		waitForContext bool
		context        func() (context.Context, context.CancelFunc)
	}{
		{name: "malformed ID", stdout: []byte(`" invalid "`)},
		{name: "truncated output", stdout: []byte(`"registry-observed-legacy"`), truncated: true},
		{name: "termination uncertainty", stdout: []byte(marker), stderr: []byte(marker),
			runErr: errors.Join(runtimeprocess.ErrTerminationFailed, errors.New(marker))},
		{name: "canceled", stdout: []byte(marker), stderr: []byte(marker), waitForContext: true,
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			}},
		{name: "deadline", stdout: []byte(marker), stderr: []byte(marker), waitForContext: true,
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout := append([]byte(nil), test.stdout...)
			stderr := append([]byte(nil), test.stderr...)
			runner.configure("registry-observed-legacy", stdout, stderr, test.runErr,
				test.truncated, test.waitForContext, nil, nil)
			beforeRuns := runner.count()
			ctx, cancel := context.Background(), func() {}
			if test.context != nil {
				ctx, cancel = test.context()
			}
			defer cancel()
			entered, err := manager.enterGeneratedDomainTypedMutation(ctx)
			if entered != nil || !IsCode(err, DiagnosticRouteUnresolved) || strings.Contains(err.Error(), marker) {
				t.Fatalf("conditional refusal: entered=%t error=%v", entered != nil, err)
			}
			afterRuns := runner.count()
			if afterRuns != beforeRuns {
				for _, output := range append(stdout, stderr...) {
					if output != 0 {
						t.Fatal("daemon command output handed to the runner was not cleared")
					}
				}
			} else if ctx.Err() == nil {
				t.Fatal("live observation refusal did not invoke the daemon runner")
			}
			manager.domainView.mu.Lock()
			typed, inspections, stale := manager.domainView.typedActive,
				manager.domainView.inspectionActive, manager.domainView.stale
			manager.domainView.mu.Unlock()
			if typed != 0 || inspections != 0 || stale {
				t.Fatalf("observation failure mutated view: typed=%d inspections=%d stale=%t", typed, inspections, stale)
			}
			routeAfter, routeAfterErr := os.ReadFile(manager.store.path)
			beforeMissing, afterMissing := errors.Is(routeBeforeErr, os.ErrNotExist), errors.Is(routeAfterErr, os.ErrNotExist)
			if beforeMissing != afterMissing || !beforeMissing && (routeBeforeErr != nil || routeAfterErr != nil) ||
				string(routeBefore) != string(routeAfter) {
				t.Fatalf("observation failure changed route state: before=%v after=%v", routeBeforeErr, routeAfterErr)
			}
		})
	}

	runner.configure(unrelatedInput.physical.manager.daemonID, nil, nil, nil, false, false, nil, nil)
	if entered, err := manager.enterGeneratedDomainTypedMutation(context.Background()); entered != nil ||
		!IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("daemon collision entered typed activity: entered=%t error=%v", entered != nil, err)
	}
	manager.domainView.mu.Lock()
	stale := manager.domainView.stale
	manager.domainView.mu.Unlock()
	if !stale {
		t.Fatal("proved daemon+namespace collision did not stale the shared Manager view")
	}
}

func TestGeneratedDomainRegistryRejectsChangedLegacyPathBeforeTypedEffect(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	fixture := newGeneratedDomainIdentityFixture(t)
	runner := &generatedDomainControlledRunner{daemonID: "registry-path-legacy"}
	manager, err := New(runner, generatedDomainRegistryManagerOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	unrelatedFixture := newGeneratedDomainIdentityFixture(t)
	unrelatedInput := generatedDomainRegistryInput(t, unrelatedFixture,
		&generatedDomainRegistryRunner{daemonID: "registry-path-domain"}, &candidateFactoryEnvironment{})
	if state, err := reserveGeneratedDomain(context.Background(), unrelatedInput); err != nil || !state.fenceReady {
		t.Fatalf("unrelated reservation: state=%+v error=%v", state, err)
	}
	ingressRoot := filepath.Join(fixture.dataRoot, "runtime", "generated-ingress")
	replaced := ingressRoot + "-replaced"
	if err := os.Rename(ingressRoot, replaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ingressRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	before := runner.count()
	if entered, err := manager.enterGeneratedDomainTypedMutation(context.Background()); entered != nil ||
		!IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("replaced path entered typed activity: entered=%t error=%v", entered != nil, err)
	}
	if runner.count() != before {
		t.Fatalf("path replacement reached daemon inspection: before=%d after=%d", before, runner.count())
	}
	manager.domainView.mu.Lock()
	typed, inspections, stale := manager.domainView.typedActive,
		manager.domainView.inspectionActive, manager.domainView.stale
	manager.domainView.mu.Unlock()
	if typed != 0 || inspections != 0 || stale {
		t.Fatalf("path refusal mutated view: typed=%d inspections=%d stale=%t", typed, inspections, stale)
	}
}

func TestGeneratedDomainReservationCanceledBeforeObservationIsSticky(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	fixture := newGeneratedDomainIdentityFixture(t)
	input := generatedDomainRegistryInput(t, fixture,
		&generatedDomainRegistryRunner{daemonID: "registry-pre-canceled"}, &candidateFactoryEnvironment{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := reserveGeneratedDomain(ctx, input)
	if state == nil || !state.failed || state.fenceReady || !errors.Is(err, context.Canceled) ||
		!errors.Is(state.err, context.Canceled) {
		t.Fatalf("pre-canceled reservation: state=%+v error=%v", state, err)
	}
	joined, err := reserveGeneratedDomain(context.Background(), input)
	if joined != state || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled retry minted new state: state=%p want=%p error=%v", joined, state, err)
	}
	changed := input
	changed.runner = &generatedDomainRegistryRunner{daemonID: "registry-pre-canceled"}
	if state, err := reserveGeneratedDomain(context.Background(), changed); state != nil ||
		!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("changed dependency joined failed reservation: state=%v error=%v", state, err)
	}
}

func TestGeneratedDomainRegistryRefusesRetargetedManagerPointerWithOldCopyActive(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	firstFixture := newGeneratedDomainIdentityFixture(t)
	firstRunner := &generatedDomainRegistryRunner{daemonID: "registry-retarget-first"}
	first, err := New(firstRunner, generatedDomainRegistryManagerOptions(firstFixture))
	if err != nil {
		t.Fatal(err)
	}
	oldView := first.domainView
	oldCopy := aliasGeneratedDomainManagerReferencesForTest(first)
	leave, err := oldCopy.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	secondFixture := newGeneratedDomainIdentityFixture(t)
	second, err := New(&generatedDomainRegistryRunner{daemonID: "registry-retarget-second"},
		generatedDomainRegistryManagerOptions(secondFixture))
	if err != nil {
		t.Fatal(err)
	}
	retargetGeneratedDomainManagerReferencesForTest(first, second)
	if first.domainView == oldView || oldCopy.domainView != oldView {
		t.Fatal("test did not preserve the old copied view while retargeting the retained Manager pointer")
	}

	input := generatedDomainRegistryInput(t, firstFixture,
		&generatedDomainRegistryRunner{daemonID: "registry-retarget-domain"}, &candidateFactoryEnvironment{})
	state, reserveErr := reserveGeneratedDomain(context.Background(), input)
	if state == nil || !state.failed || state.fenceReady || reserveErr == nil {
		t.Fatalf("retargeted record produced a ready fence: state=%+v error=%v", state, reserveErr)
	}
	oldView.mu.Lock()
	typed, stale, pairing := oldView.typedActive, oldView.stale, oldView.pairing
	oldView.mu.Unlock()
	if typed != 1 || stale || pairing {
		t.Fatalf("failed pairing misclassified old view: typed=%d stale=%t pairing=%t", typed, stale, pairing)
	}
	joined, retryErr := reserveGeneratedDomain(context.Background(), input)
	if joined != state || retryErr == nil {
		t.Fatalf("retargeted-record retry minted a new state: state=%p want=%p error=%v", joined, state, retryErr)
	}
	leave()
	oldView.mu.Lock()
	typed = oldView.typedActive
	oldView.mu.Unlock()
	if typed != 0 {
		t.Fatalf("old copied activity did not leave once: typed=%d", typed)
	}
}

func TestGeneratedDomainTypedOuterAPIFamiliesRefuseStaleViewBeforeEffects(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}

	t.Run("gateway v2 stage", func(t *testing.T) {
		manager, store, state, driver := gatewayV2StageTestFixture(t)
		_, before, err := store.loadBoundUpgrade(state.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		beforeEvents := append([]string(nil), driver.events...)
		beforeCreate, beforeStart, beforeRemove := driver.createCalls, driver.startCalls, driver.removeCalls
		staleGeneratedDomainManager(t, manager)
		if err := manager.StageGatewayV2(context.Background(), state.OperationID); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("stale stage error=%v", err)
		}
		_, after, err := store.loadBoundUpgrade(state.OperationID)
		if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.events, beforeEvents) ||
			driver.createCalls != beforeCreate || driver.startCalls != beforeStart || driver.removeCalls != beforeRemove {
			t.Fatalf("stale stage mutated state: journal=%#v events=%v calls=%d/%d/%d error=%v",
				after, driver.events, driver.createCalls, driver.startCalls, driver.removeCalls, err)
		}
	})

	t.Run("LAN grant", func(t *testing.T) {
		manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
		beforeEvents := append([]string(nil), driver.events...)
		authorizeCalls := 0
		staleGeneratedDomainManager(t, manager)
		result, err := manager.GrantGatewayV2LAN(context.Background(), request,
			func(context.Context, gatewayV2LANGrantRequest) (gatewayV2LANGrantAuthorizationLease, error) {
				authorizeCalls++
				return nil, errors.New("stale view reached authorization")
			})
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (gatewayV2LANGrantResult{}) || authorizeCalls != 0 {
			t.Fatalf("stale grant: result=%#v authorizations=%d error=%v", result, authorizeCalls, err)
		}
		after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) ||
			!reflect.DeepEqual(driver.events, beforeEvents) {
			t.Fatalf("stale grant mutated state: state=%#v live=%#v events=%v error=%v",
				after, driver.live, driver.events, loadErr)
		}
	})

	t.Run("LAN startup quarantine", func(t *testing.T) {
		manager, store, before, journal, request, driver := gatewayV2LANGrantFixture(t)
		beforeEvents := append([]string(nil), driver.events...)
		claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantDBActive, 3)
		staleGeneratedDomainManager(t, manager)
		if err := manager.QuarantineGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim}); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("stale startup quarantine error=%v", err)
		}
		after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if loadErr != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) ||
			!reflect.DeepEqual(driver.events, beforeEvents) {
			t.Fatalf("stale startup quarantine mutated state: state=%#v live=%#v events=%v error=%v",
				after, driver.live, driver.events, loadErr)
		}
	})

	t.Run("gateway rebind commit", func(t *testing.T) {
		fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
		before, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		sessionCalls := 0
		staleGeneratedDomainManager(t, fixture.manager)
		response, commitErr := fixture.manager.CommitGatewayRebind(context.Background(), fixture.repository,
			GatewayRebindCommitRequest{Version: gatewayRebindCommitReviewVersion,
				Proposal: GatewayRebindProposalInput{OperationID: input.Inspection.Spec.OperationID}},
			gatewayRebindTestAdministrator, func(context.Context) error { sessionCalls++; return nil })
		after, readErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if !IsCode(commitErr, DiagnosticRouteUnresolved) || response != (GatewayRebindCommitResponse{}) ||
			sessionCalls != 0 || readErr != nil || !reflect.DeepEqual(after, before) ||
			driver.commitCalls != 0 || driver.attestCalls != 0 || driver.abortCalls != 0 || driver.withdrawCalls != 0 {
			t.Fatalf("stale rebind: response=%#v sessions=%d driver=%#v changed=%t readError=%v error=%v",
				response, sessionCalls, driver, !reflect.DeepEqual(after, before), readErr, commitErr)
		}
	})
}
