//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainRegistryRunner struct {
	mu       sync.Mutex
	daemonID string
	requests []runtimeprocess.CommandRequest
}

func (r *generatedDomainRegistryRunner) Run(
	_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	r.requests = append(r.requests, request)
	return runtimeprocess.CommandResult{Stdout: []byte(fmt.Sprintf("%q", r.daemonID))}, nil
}

func (r *generatedDomainRegistryRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func generatedDomainRegistryManagerOptions(fixture generatedDomainIdentityFixture) Options {
	return Options{
		DockerExecutable: fixture.options.DockerExecutable, DockerEndpoint: fixture.options.DockerEndpoint,
		DockerConfigDirectory: fixture.options.DockerConfigDirectory,
		WorkingDirectory:      fixture.options.WorkingDirectory, DataRoot: fixture.options.DataRoot,
		HostPort: fixture.options.HostPort, CommandTimeout: fixture.options.CommandTimeout,
		PullTimeout: fixture.options.PullTimeout, OutputLimit: fixture.options.OutputLimit,
		RebindFenceCheck: func(context.Context) error { return nil },
	}
}

// aliasGeneratedDomainManagerReferencesForTest models the observable reference
// portion of a Manager value copy without copying its acquired contextMutex.
// The alias gets an independent lock while retaining every dependency, latch,
// driver, hook, and shared domain view that can affect Manager behavior.
func aliasGeneratedDomainManagerReferencesForTest(source *Manager) *Manager {
	alias := &Manager{mu: newContextMutex()}
	retargetGeneratedDomainManagerReferencesForTest(alias, source)
	return alias
}

// retargetGeneratedDomainManagerReferencesForTest models replacement by a
// separately constructed Manager while retaining the target's live lock. The
// registry test is about binding a retained pointer to its original domain
// view; copying a used synchronization primitive is outside that contract.
func retargetGeneratedDomainManagerReferencesForTest(target, source *Manager) {
	target.runner = source.runner
	target.store = source.store
	target.options = source.options
	target.dockerEnv = append(target.dockerEnv[:0], source.dockerEnv...)
	target.workingDirectoryIdentity = source.workingDirectoryIdentity
	target.domainView = source.domainView
	target.gatewayRebindFailStop = source.gatewayRebindFailStop
	target.gatewayRebindCommitBarrier = source.gatewayRebindCommitBarrier
	target.gatewayRebindV2NetworkObserver = source.gatewayRebindV2NetworkObserver
	target.gatewayRebindAfterClaim = source.gatewayRebindAfterClaim
	target.gatewayRebindAfterPreparedCheckpoint = source.gatewayRebindAfterPreparedCheckpoint
	target.gatewayRebindAfterPreparedIntent = source.gatewayRebindAfterPreparedIntent
	target.gatewayRebindClock = source.gatewayRebindClock
	target.gatewayTopologyObserver = source.gatewayTopologyObserver
	target.gatewayCandidateObserver = source.gatewayCandidateObserver
	target.gatewayMixedRestartObserver = source.gatewayMixedRestartObserver
	target.gatewayV2UpgradeDriver = source.gatewayV2UpgradeDriver
	target.gatewayV2TransferDriver = source.gatewayV2TransferDriver
	target.gatewayV2CoordinatorDriver = source.gatewayV2CoordinatorDriver
	target.gatewayV2LANGrantDriver = source.gatewayV2LANGrantDriver
	target.gatewayCurrentPhysicalDriver = source.gatewayCurrentPhysicalDriver
	target.gatewayRebindCrossStoreDriver = source.gatewayRebindCrossStoreDriver
	target.gatewayRebindTerminalReleaseEvent = source.gatewayRebindTerminalReleaseEvent
}

func generatedDomainRegistryInput(
	t *testing.T,
	fixture generatedDomainIdentityFixture,
	runner *generatedDomainRegistryRunner,
	environment *candidateFactoryEnvironment,
) generatedDomainReservationInput {
	t.Helper()
	identity, _, err := inspectGeneratedDomainPhysicalIdentity(
		context.Background(), fixture.stores, runner, fixture.options,
	)
	if err != nil {
		t.Fatal(err)
	}
	input, err := newGeneratedDomainReservationInput(fixture.stores, runner, environment, fixture.options, identity)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func waitGeneratedDomainPairingEpoch(t *testing.T) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		generatedDomains.mu.Lock()
		active := generatedDomains.pairingEpoch != 0
		generatedDomains.mu.Unlock()
		if active {
			return
		}
		select {
		case <-timeout.C:
			t.Fatal("generated-domain pairing epoch did not become active")
		case <-ticker.C:
		}
	}
}

func registerGeneratedDomainReservationCleanup(
	t *testing.T,
	release func(),
	cancel context.CancelFunc,
	finished <-chan struct{},
	failure string,
) {
	t.Helper()
	t.Cleanup(func() {
		release()
		cancel()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		select {
		case <-finished:
		case <-timeout.C:
			t.Error(failure)
		}
	})
}

func TestGeneratedDomainRegistryFixtureCleanupAfterFatalAllowsLaterReservation(t *testing.T) {
	const intentionalFailure = "intentional_fatal_after_pairing_epoch"
	if !generatedDomainRegistryExpectedFailureIsolated(t, intentionalFailure) {
		return
	}

	t.Run(intentionalFailure, func(t *testing.T) {
		fixture := newGeneratedDomainRetainedRegistryFixture(t)
		runner := &generatedDomainRegistryRunner{daemonID: "registry-cleanup-fatal-daemon"}
		root, err := inspectGeneratedLegacyRootObservation(runner, generatedDomainRegistryManagerOptions(fixture))
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := admitGeneratedLegacyConstruction(newGeneratedManagerView(root), root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ticket.fail(context.Canceled) })
		input := generatedDomainRegistryInput(t, fixture, runner, &candidateFactoryEnvironment{})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			_, _ = reserveGeneratedDomain(ctx, input)
		}()
		registerGeneratedDomainReservationCleanup(t, func() { ticket.fail(context.Canceled) }, cancel, finished, "cleanup did not finish")
		waitGeneratedDomainPairingEpoch(t)
		t.Fatal("intentional registry fixture cleanup failure")
	})

	laterFixture := newGeneratedDomainRetainedRegistryFixture(t)
	laterRunner := &generatedDomainRegistryRunner{daemonID: "registry-cleanup-later-daemon"}
	laterInput := generatedDomainRegistryInput(t, laterFixture, laterRunner, &candidateFactoryEnvironment{})
	state, err := reserveGeneratedDomain(context.Background(), laterInput)
	if err != nil || state == nil || !state.fenceReady || state.failed {
		t.Errorf("later reservation did not progress: state=%+v error=%v", state, err)
		return
	}
	t.Log("registry fixture cleanup control completed")
}

func TestGeneratedDomainRegistryFencesLegacyConstructionAndTypedActivity(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	managerFixture := newGeneratedDomainRetainedRegistryFixture(t)
	managerRunner := &generatedDomainRegistryRunner{daemonID: "registry-manager-daemon"}
	manager, err := New(managerRunner, generatedDomainRegistryManagerOptions(managerFixture))
	if err != nil {
		t.Fatal(err)
	}
	if managerRunner.count() != 0 {
		t.Fatalf("ordinary Manager construction daemon inspections = %d, want 0", managerRunner.count())
	}
	copyBeforeBinding := aliasGeneratedDomainManagerReferencesForTest(manager)
	leaveOriginal, err := manager.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	leaveCopy, err := copyBeforeBinding.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if managerRunner.count() != 0 {
		t.Fatalf("ordinary typed-entry daemon inspections = %d, want 0", managerRunner.count())
	}
	leaveCopy()
	leaveCopy()
	leaveOriginal()
	noncollidingFixture := newGeneratedDomainRetainedRegistryFixture(t)
	noncollidingRunner := &generatedDomainRegistryRunner{
		daemonID: "registry-noncolliding-daemon-" + filepath.Base(filepath.Dir(noncollidingFixture.dataRoot)),
	}

	t.Run("exact join and independent collision predicates", func(t *testing.T) {
		fixture := newGeneratedDomainRetainedRegistryFixture(t)
		runner := &generatedDomainRegistryRunner{daemonID: "registry-exact-daemon-" + filepath.Base(filepath.Dir(fixture.dataRoot))}
		environment := &candidateFactoryEnvironment{}
		input := generatedDomainRegistryInput(t, fixture, runner, environment)
		if _, err := newGeneratedDomainReservationInput(
			fixture.stores, runner, nil, fixture.options, input.physical,
		); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
			!errors.Is(err, generatedDomainReservationPostInspectionInput) ||
			strings.Contains(err.Error(), fixture.dataRoot) || strings.Contains(err.Error(), runner.daemonID) {
			t.Fatalf("post-inspection input attribution exposed mutable identity: %v", err)
		}
		state, err := reserveGeneratedDomain(context.Background(), input)
		if err != nil || state == nil || !state.fenceReady || state.failed {
			t.Fatalf("first reservation: state=%+v error=%v", state, err)
		}
		joined, err := reserveGeneratedDomain(context.Background(), input)
		if err != nil || joined != state {
			t.Fatalf("exact join: state=%p want=%p error=%v", joined, state, err)
		}

		changedRunner := input
		changedRunner.runner = &generatedDomainRegistryRunner{daemonID: runner.daemonID}
		if state, err := reserveGeneratedDomain(context.Background(), changedRunner); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
			t.Fatalf("changed runner: state=%v error=%v", state, err)
		}
		changedEnvironment := input
		changedEnvironment.environment = &candidateFactoryEnvironment{}
		if state, err := reserveGeneratedDomain(context.Background(), changedEnvironment); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
			t.Fatalf("changed environment: state=%v error=%v", state, err)
		}
		changedOptions := input
		changedOptions.options.HostPort++
		if state, err := reserveGeneratedDomain(context.Background(), changedOptions); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
			t.Fatalf("changed options: state=%v error=%v", state, err)
		}
		for name, change := range map[string]func(*generatedDomainReservationInput){
			"repository":    func(value *generatedDomainReservationInput) { value.repository = nil },
			"database":      func(value *generatedDomainReservationInput) { value.database = nil },
			"guard":         func(value *generatedDomainReservationInput) { value.guard = nil },
			"plans":         func(value *generatedDomainReservationInput) { value.plans = nil },
			"configuration": func(value *generatedDomainReservationInput) { value.configuration = nil },
		} {
			changed := input
			change(&changed)
			if state, err := reserveGeneratedDomain(context.Background(), changed); state != nil ||
				!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
				t.Fatalf("changed %s dependency: state=%v error=%v", name, state, err)
			}
		}
		zeroAliases := fixture.options
		zeroAliases.HostPort = 0
		zeroAliases.CommandTimeout = 0
		zeroAliases.PullTimeout = 0
		zeroAliases.OutputLimit = 0
		normalized, err := newGeneratedDomainReservationInput(fixture.stores, runner, environment, zeroAliases, input.physical)
		explicitAliases := fixture.options
		explicitAliases.HostPort = defaultHostPort
		explicitAliases.CommandTimeout = defaultTimeout
		explicitAliases.PullTimeout = defaultPullTimeout
		explicitAliases.OutputLimit = defaultOutputLimit
		explicit, explicitErr := newGeneratedDomainReservationInput(
			fixture.stores, runner, environment, explicitAliases, input.physical,
		)
		if err != nil || explicitErr != nil || !sameGeneratedDomainReservationInput(explicit, normalized) {
			t.Fatalf("zero aliases did not normalize to the existing explicit defaults: zero=%v explicit=%v", err, explicitErr)
		}

		separate := newGeneratedDomainRetainedRegistryFixture(t)
		separateRunner := &generatedDomainRegistryRunner{daemonID: input.physical.manager.daemonID}
		separateInput := generatedDomainRegistryInput(t, separate, separateRunner, &candidateFactoryEnvironment{})
		if generatedDomainRootOrDatabaseConflict(input.physical, separateInput.physical) ||
			!generatedDomainDaemonNamespaceConflict(input.physical, separateInput.physical) {
			t.Fatal("daemon+namespace collision was not independent of root/database identity")
		}
		rootOnly := separateInput
		rootOnly.physical.manager.dataRoot = input.physical.manager.dataRoot
		rootOnly.physical.manager.daemonID += "-other"
		if !generatedDomainRootOrDatabaseConflict(input.physical, rootOnly.physical) ||
			generatedDomainDaemonNamespaceConflict(input.physical, rootOnly.physical) {
			t.Fatal("root collision depended on the daemon+namespace predicate")
		}
		if state, err := reserveGeneratedDomain(context.Background(), rootOnly); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
			!errors.Is(err, generatedDomainReservationRetainedRootOrDatabase) {
			t.Fatalf("root collision attribution: state=%v error=%v", state, err)
		}
		databaseOnly := separateInput
		databaseOnly.physical.databaseFile = input.physical.databaseFile
		databaseOnly.physical.manager.daemonID += "-other"
		if !generatedDomainRootOrDatabaseConflict(input.physical, databaseOnly.physical) ||
			generatedDomainDaemonNamespaceConflict(input.physical, databaseOnly.physical) {
			t.Fatal("database collision depended on the daemon+namespace predicate")
		}
		if state, err := reserveGeneratedDomain(context.Background(), databaseOnly); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
			!errors.Is(err, generatedDomainReservationRetainedRootOrDatabase) {
			t.Fatalf("database collision attribution: state=%v error=%v", state, err)
		}
		if state, err := reserveGeneratedDomain(context.Background(), separateInput); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
			!errors.Is(err, generatedDomainReservationRetainedDaemonNamespace) {
			t.Fatalf("daemon+namespace-only collision: state=%v error=%v", state, err)
		}
		changedCurrent := separateInput
		changedCurrent.physical.manager.daemonID += "-current-check"
		changedCurrent.proof.dataRoot = input.proof.dataRoot
		if state, err := reserveGeneratedDomain(context.Background(), changedCurrent); state != nil ||
			!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
			!errors.Is(err, generatedDomainReservationInputNoLongerCurrent) {
			t.Fatalf("changed input attribution: state=%v error=%v", state, err)
		}
		otherNamespace := separateInput.physical
		otherNamespace.manager.namespace.network += "-other"
		if generatedDomainDaemonNamespaceConflict(input.physical, otherNamespace) {
			t.Fatal("different fixed namespace was treated as a daemon collision")
		}
	})

	t.Run("admission closes roster before protected state creation", func(t *testing.T) {
		fixture := newGeneratedDomainRetainedRegistryFixture(t)
		runner := &generatedDomainRegistryRunner{daemonID: "registry-admission-daemon-" + filepath.Base(filepath.Dir(fixture.dataRoot))}
		root, err := inspectGeneratedLegacyRootObservation(runner, generatedDomainRegistryManagerOptions(fixture))
		if err != nil {
			t.Fatal(err)
		}
		view := newGeneratedManagerView(root)
		ticket, err := admitGeneratedLegacyConstruction(view, root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ticket.fail(context.Canceled) })
		input := generatedDomainRegistryInput(t, fixture, runner, &candidateFactoryEnvironment{})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		type reservationResult struct {
			state *generatedDomainState
			err   error
		}
		result := make(chan reservationResult, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			state, err := reserveGeneratedDomain(ctx, input)
			result <- reservationResult{state: state, err: err}
		}()
		registerGeneratedDomainReservationCleanup(t, func() { ticket.fail(context.Canceled) }, cancel, finished, "reservation cleanup did not finish")
		waitGeneratedDomainPairingEpoch(t)
		if _, err := admitGeneratedLegacyConstruction(newGeneratedManagerView(root), root); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("constructor admitted after roster closed: %v", err)
		}
		select {
		case value := <-result:
			t.Fatalf("reservation did not wait for admitted constructor: %+v", value)
		default:
		}
		if _, err := os.Lstat(filepath.Join(fixture.dataRoot, "runtime", "generated-ingress")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected state was created before construction authorization: %v", err)
		}
		ticket.fail(errors.New("test construction stopped before publication"))
		value := <-result
		if value.err != nil || value.state == nil || !value.state.fenceReady {
			t.Fatalf("reservation after failed admitted constructor: %+v", value)
		}
	})

	t.Run("existing reservations conditionally inspect and track a noncolliding constructor", func(t *testing.T) {
		constructed, err := New(noncollidingRunner, generatedDomainRegistryManagerOptions(noncollidingFixture))
		if err != nil {
			t.Fatal(err)
		}
		if constructed.domainView == nil || constructed.domainView.manager != constructed || noncollidingRunner.count() != 1 {
			t.Fatalf("tracked constructor: view=%p manager=%p inspections=%d",
				constructed.domainView, constructed.domainView.manager, noncollidingRunner.count())
		}
		if _, err := os.Lstat(filepath.Join(noncollidingFixture.dataRoot, "runtime", "generated-ingress")); err != nil {
			t.Fatalf("tracked constructor did not create protected state: %v", err)
		}
	})

	t.Run("pairing drains entered activity and stales every Manager copy", func(t *testing.T) {
		input := generatedDomainRegistryInput(t, managerFixture, managerRunner, &candidateFactoryEnvironment{})
		leave, err := copyBeforeBinding.enterGeneratedDomainTypedMutation(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(leave)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		type reservationResult struct {
			state *generatedDomainState
			err   error
		}
		result := make(chan reservationResult, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			state, err := reserveGeneratedDomain(ctx, input)
			result <- reservationResult{state: state, err: err}
		}()
		registerGeneratedDomainReservationCleanup(t, leave, cancel, finished, "pairing cleanup did not finish")
		waitGeneratedDomainPairingEpoch(t)
		select {
		case value := <-result:
			t.Fatalf("pairing did not drain entered typed activity: %+v", value)
		default:
		}
		leave()
		leave()
		value := <-result
		if value.err != nil || value.state == nil || !value.state.fenceReady {
			t.Fatalf("pairing result: %+v", value)
		}
		if _, err := manager.enterGeneratedDomainTypedMutation(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("original Manager remained mutable after pairing: %v", err)
		}
		copyAfterBinding := aliasGeneratedDomainManagerReferencesForTest(manager)
		if _, err := copyAfterBinding.enterGeneratedDomainTypedMutation(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("copied Manager remained mutable after pairing: %v", err)
		}
	})

	t.Run("canceled pairing is sticky", func(t *testing.T) {
		fixture := newGeneratedDomainRetainedRegistryFixture(t)
		runner := &generatedDomainRegistryRunner{daemonID: "registry-cancel-daemon-" + filepath.Base(filepath.Dir(fixture.dataRoot))}
		root, err := inspectGeneratedLegacyRootObservation(runner, generatedDomainRegistryManagerOptions(fixture))
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := admitGeneratedLegacyConstruction(newGeneratedManagerView(root), root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ticket.fail(context.Canceled) })
		input := generatedDomainRegistryInput(t, fixture, runner, &candidateFactoryEnvironment{})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		result := make(chan *generatedDomainState, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			state, _ := reserveGeneratedDomain(ctx, input)
			result <- state
		}()
		registerGeneratedDomainReservationCleanup(t, func() { ticket.fail(context.Canceled) }, cancel, finished, "canceled pairing cleanup did not finish")
		waitGeneratedDomainPairingEpoch(t)
		cancel()
		state := <-result
		if state == nil || !state.failed || state.fenceReady || !errors.Is(state.err, context.Canceled) {
			t.Fatalf("canceled state = %+v", state)
		}
		joined, err := reserveGeneratedDomain(context.Background(), input)
		if joined != state || !errors.Is(err, context.Canceled) {
			t.Fatalf("sticky retry: state=%p want=%p error=%v", joined, state, err)
		}
		ticket.fail(context.Canceled)
	})

	t.Run("conditional observation refuses ambiguity before typed activity", func(t *testing.T) {
		fixture := newGeneratedDomainRetainedRegistryFixture(t)
		runner := &generatedDomainRegistryRunner{daemonID: "registry-observation-daemon-" + filepath.Base(filepath.Dir(fixture.dataRoot))}
		constructed, err := New(runner, generatedDomainRegistryManagerOptions(fixture))
		if err != nil {
			t.Fatal(err)
		}
		runner.mu.Lock()
		runner.daemonID = " ambiguous-daemon "
		runner.mu.Unlock()
		if leave, err := constructed.enterGeneratedDomainTypedMutation(context.Background()); leave != nil || !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("ambiguous conditional identity: leave=%v error=%v", leave != nil, err)
		}
		constructed.domainView.mu.Lock()
		active, inspections, stale := constructed.domainView.typedActive,
			constructed.domainView.inspectionActive, constructed.domainView.stale
		constructed.domainView.mu.Unlock()
		if active != 0 || inspections != 0 || stale {
			t.Fatalf("ambiguous observation mutated view: typed=%d inspections=%d stale=%t", active, inspections, stale)
		}
	})
}
