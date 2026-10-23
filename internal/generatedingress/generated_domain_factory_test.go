//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainConstructionFixture struct {
	identity    generatedDomainIdentityFixture
	runner      *generatedDomainConstructionRunner
	environment *candidateFactoryEnvironment
}

type generatedDomainConstructionRunner struct {
	mu       sync.Mutex
	delegate *generatedDomainRecoveryCensusRunner
	block    []string
	started  chan struct{}
	release  <-chan struct{}
	blocked  bool
	commands [][]string
}

func (r *generatedDomainConstructionRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.mu.Lock()
	r.commands = append(r.commands, args)
	shouldBlock := !r.blocked && reflect.DeepEqual(args, r.block)
	if shouldBlock {
		r.blocked = true
	}
	started, release := r.started, r.release
	r.mu.Unlock()
	if shouldBlock {
		if started != nil {
			close(started)
		}
		select {
		case <-ctx.Done():
			return runtimeprocess.CommandResult{}, ctx.Err()
		case <-release:
		}
	}
	return r.delegate.Run(ctx, request)
}

func (r *generatedDomainConstructionRunner) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([][]string, len(r.commands))
	for index := range r.commands {
		result[index] = append([]string(nil), r.commands[index]...)
	}
	return result
}

func newGeneratedDomainStableConstructionFixture(t *testing.T) generatedDomainConstructionFixture {
	t.Helper()
	identity := newGeneratedDomainIdentityFixture(t)
	state, journal, observation := generatedDomainNativeV1AttestationFixture(t)
	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	digest, err := canonicalDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
		stateDigest: digest, nativeV1: &state}
	expected.names = generatedDomainGatewayExpectedNames(expected)
	expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
	if err != nil {
		t.Fatal(err)
	}
	descriptors, _, ok := generatedDomainGatewayDockerDescriptors(expected)
	if !ok {
		t.Fatal("native-v1 Docker descriptors are unavailable")
	}
	objects := generatedDomainDockerObjectsForDescriptors(descriptors)
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = gateway.container.ID
		case caddyNetworkName:
			objects[index].id = gateway.network.ID
		}
	}
	identity.options.HostPort = journal.Source.LocalHostPort
	identity.options.CommandTimeout = time.Minute
	identity.options.OutputLimit = 1 << 20
	return generatedDomainConstructionFixture{
		identity: identity,
		runner: &generatedDomainConstructionRunner{delegate: &generatedDomainRecoveryCensusRunner{
			daemonID: "private-construction-daemon", objects: objects,
			imageID: gateway.image.ID, gateway: gateway,
		}},
		environment: &candidateFactoryEnvironment{path: t.TempDir()},
	}
}

func TestGeneratedDomainConstructionCanonicalInputsAndStickyClassification(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("canonical_inputs_and_captured_bundle", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainStableConstructionFixture(t)
			if _, err := newGeneratedDomainConstruction(context.Background(), nil, fixture.runner,
				fixture.environment, fixture.identity.options); err == nil {
				t.Fatal("nil sealed stores were accepted")
			}
			invalid := fixture.identity.options
			invalid.DataRoot = filepath.Join(t.TempDir(), "foreign")
			if _, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores, fixture.runner,
				fixture.environment, invalid); err == nil {
				t.Fatal("crossed data root was accepted")
			}
			result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores, fixture.runner,
				fixture.environment, fixture.identity.options)
			if err != nil || result == nil {
				t.Fatalf("canonical construction = %#v, %v", result, err)
			}
			copy := *fixture.identity.stores
			fixture.identity.stores = nil
			joined, err := newGeneratedDomainConstruction(context.Background(), &copy, fixture.runner,
				fixture.environment, fixture.identity.options)
			if err != nil || joined != result {
				t.Fatalf("captured sealed bundle join = %#v, %v; want %#v", joined, err, result)
			}
		})
	})

	t.Run("refusal_is_sticky_after_Docker_ambiguity", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainStableConstructionFixture(t)
			fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
				kind: "volume", name: "rig-unexpected-private-construction",
				labels: map[string]string{"io.rig.unexpected": "private-construction"},
				data: generatedDomainDockerObjectData{driver: "local", scope: "local",
					options: map[string]string{}, mountpoint: "/var/lib/docker/volumes/rig-unexpected/_data",
					createdAt: "2026-10-23T00:00:00Z"},
			})
			result, firstErr := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fixture.runner, fixture.environment, fixture.identity.options)
			if result != nil || firstErr == nil {
				t.Fatalf("ambiguous construction = %#v, %v", result, firstErr)
			}
			before := len(fixture.runner.snapshot())
			fixture.runner.delegate.objects = fixture.runner.delegate.objects[:len(fixture.runner.delegate.objects)-1]
			joined, joinErr := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fixture.runner, fixture.environment, fixture.identity.options)
			after := fixture.runner.snapshot()
			if joined != nil || !errors.Is(joinErr, firstErr) || !generatedDomainStickyJoinOnlyRecheckedDaemon(after, before) {
				t.Fatalf("sticky refusal join = %#v, %v; first=%v commands=%d/%d",
					joined, joinErr, firstErr, len(after), before)
			}
		})
	})

	t.Run("typed_recovery_is_sticky", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainPreparedRecoveryConstructionFixture(t)
			result, firstErr := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			if result != nil || !errors.Is(firstErr, errGeneratedDomainConstructionRecovery) {
				t.Fatalf("recovery construction = %#v, %v", result, firstErr)
			}
			before := len(runner.snapshot())
			joined, joinErr := newGeneratedDomainConstruction(context.Background(), identity.stores, runner,
				environment, identity.options)
			after := runner.snapshot()
			if joined != nil || !errors.Is(joinErr, firstErr) || !generatedDomainStickyJoinOnlyRecheckedDaemon(after, before) {
				t.Fatalf("sticky recovery join = %#v, %v; first=%v commands=%d/%d",
					joined, joinErr, firstErr, len(after), before)
			}
		})
	})

	t.Run("owner_cancellation_is_sticky", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainStableConstructionFixture(t)
			started := make(chan struct{})
			fixture.runner.block = []string{"container", "ls", "--all", "--no-trunc", "--format",
				generatedDomainContainerListFormat}
			fixture.runner.started = started
			fixture.runner.release = make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := newGeneratedDomainConstruction(ctx, fixture.identity.stores, fixture.runner,
					fixture.environment, fixture.identity.options)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("construction owner did not reach cancellation barrier")
			}
			cancel()
			firstErr := <-done
			if firstErr == nil || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("owner cancellation = %v; context=%v", firstErr, ctx.Err())
			}
			before := len(fixture.runner.snapshot())
			joined, joinErr := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fixture.runner, fixture.environment, fixture.identity.options)
			after := fixture.runner.snapshot()
			if joined != nil || !errors.Is(joinErr, firstErr) || !generatedDomainStickyJoinOnlyRecheckedDaemon(after, before) {
				t.Fatalf("sticky owner cancellation = %#v, %v commands=%d/%d",
					joined, joinErr, len(after), before)
			}
		})
	})
}

func generatedDomainStickyJoinOnlyRecheckedDaemon(commands [][]string, before int) bool {
	return len(commands) == before+1 && reflect.DeepEqual(commands[before],
		[]string{"info", "--format", "{{json .ID}}"})
}

func newGeneratedDomainPreparedRecoveryConstructionFixture(t *testing.T) (generatedDomainIdentityFixture,
	*generatedDomainConstructionRunner, *candidateFactoryEnvironment,
) {
	t.Helper()
	fixture := newGeneratedDomainGatewayOnlyUpgradeFixtureAtPhase(t, gatewayPhasePrepared)
	source, err := fixture.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	expected := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
	if expected.kind != generatedDomainRecoveryDockerUpgrade || len(expected.alternatives) != 1 {
		t.Fatalf("prepared construction recovery expectation = %#v", expected)
	}
	observation := gatewayV2IdentityTestObservation(t, source, fixture.state, fixture.journal,
		gatewayTopologyExactV1Only)
	observation.V1Container.ID = strings.Repeat("b", 64)
	observation.V1NetworkID = strings.Repeat("e", 64)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	objects := generatedDomainDockerObjectsForDescriptors(expected.alternatives[0])
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = observation.V1Container.ID
		case caddyNetworkName:
			objects[index].id = observation.V1NetworkID
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint,
				createdAt:  observation.V1VolumeIdentity.CreatedAt}
		}
	}
	fixture.identity.options.HostPort = fixture.manager.options.HostPort
	fixture.identity.options.CommandTimeout = time.Minute
	fixture.identity.options.OutputLimit = 1 << 20
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	runner := &generatedDomainConstructionRunner{delegate: &generatedDomainRecoveryCensusRunner{
		daemonID: "private-construction-recovery-daemon", objects: objects,
		imageID: observation.Image.ID, gateway: gateway,
	}}
	return fixture.identity, runner, &candidateFactoryEnvironment{path: t.TempDir()}
}

func TestGeneratedDomainConstructionJoinsOneCompleteOwner(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	fixture := newGeneratedDomainStableConstructionFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	fixture.runner.block = []string{"container", "ls", "--all", "--no-trunc", "--format", generatedDomainContainerListFormat}
	fixture.runner.started, fixture.runner.release = started, release
	type outcome struct {
		result *generatedDomainConstruction
		err    error
	}
	ownerDone := make(chan outcome, 1)
	go func() {
		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.runner, fixture.environment, fixture.identity.options)
		ownerDone <- outcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("construction owner did not reach classifier")
	}
	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan outcome, 1)
	go func() {
		result, err := newGeneratedDomainConstruction(waiterCtx, fixture.identity.stores,
			fixture.runner, fixture.environment, fixture.identity.options)
		waiterDone <- outcome{result: result, err: err}
	}()
	cancel()
	if waiter := <-waiterDone; waiter.result != nil || !errors.Is(waiter.err, context.Canceled) {
		t.Fatalf("canceled waiter = %#v, %v", waiter.result, waiter.err)
	}
	close(release)
	owner := <-ownerDone
	if owner.err != nil || owner.result == nil {
		t.Fatalf("construction owner = %#v, %v", owner.result, owner.err)
	}
	joined, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.runner, fixture.environment, fixture.identity.options)
	if err != nil || joined != owner.result || joined.manager != owner.result.manager ||
		joined.budget != owner.result.budget || joined.candidates != owner.result.candidates ||
		joined.candidates.ledger != owner.result.candidates.ledger ||
		joined.candidates.heads != owner.result.candidates.heads {
		t.Fatalf("completed join did not retain one owner family: %#v, %v", joined, err)
	}
	foreignEnvironment := &candidateFactoryEnvironment{path: fixture.environment.path}
	if collided, collisionErr := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.runner, foreignEnvironment, fixture.identity.options); collisionErr == nil || collided != nil {
		t.Fatalf("crossed environment joined completed owner: %#v, %v", collided, collisionErr)
	}
	retained, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.runner, fixture.environment, fixture.identity.options)
	if err != nil || retained != owner.result {
		t.Fatalf("crossed environment changed exact completed join: %#v, %v", retained, err)
	}
}

func TestGeneratedDomainConstructionAssemblesCompletePrivateManager(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	fixture := newGeneratedDomainStableConstructionFixture(t)
	statePath := filepath.Join(fixture.identity.dataRoot, "runtime", "generated-ingress", stateFilename)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores, fixture.runner,
		fixture.environment, fixture.identity.options)
	if err != nil || !validGeneratedDomainConstructionResult(result.reservation, result) {
		t.Fatalf("private Manager composition = %#v, %v", result, err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("stable construction changed protected state: %v", err)
	}
	leave, err := result.manager.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil || leave == nil {
		t.Fatalf("completed owned Manager typed admission = %v", err)
	}
	leave()
	generatedCandidateAuthorityRegistries.Lock()
	_, publiclyRegistered := generatedCandidateAuthorityRegistries.values[fixture.identity.stores.Repository()]
	generatedCandidateAuthorityRegistries.Unlock()
	if publiclyRegistered {
		t.Fatal("private candidate owner was published through the temporary public registry")
	}
	for _, command := range fixture.runner.snapshot() {
		effectCopy := len(command) >= 4 && command[0] == "container" && command[1] == "cp" &&
			command[len(command)-1] != "-"
		if len(command) >= 2 && (command[1] == "create" || command[1] == "start" ||
			command[1] == "stop" || command[1] == "rm" || command[1] == "pull" || effectCopy) {
			t.Fatalf("stable construction dispatched an effect: %#v", command)
		}
	}

	alias := aliasGeneratedDomainManagerReferencesForTest(result.manager)
	ownerLeave, err := result.manager.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil {
		t.Fatalf("owner typed activity: %v", err)
	}
	aliasLeave, err := alias.enterGeneratedDomainTypedMutation(context.Background())
	if err != nil {
		ownerLeave()
		t.Fatalf("exact alias typed activity: %v", err)
	}
	result.manager.domainView.mu.Lock()
	active := result.manager.domainView.typedActive
	result.manager.domainView.mu.Unlock()
	if active != 2 || alias.gatewayRebindFailStop != result.manager.gatewayRebindFailStop ||
		alias.gatewayRebindCommitBarrier != result.manager.gatewayRebindCommitBarrier {
		aliasLeave()
		ownerLeave()
		t.Fatalf("shared alias activity/latches = %d/%v/%v", active,
			alias.gatewayRebindFailStop == result.manager.gatewayRebindFailStop,
			alias.gatewayRebindCommitBarrier == result.manager.gatewayRebindCommitBarrier)
	}
	aliasLeave()
	ownerLeave()

	assertForgedRefused := func(name string, forged *Manager) {
		t.Helper()
		if leave, err := forged.enterGeneratedDomainTypedMutation(context.Background()); err == nil || leave != nil {
			if leave != nil {
				leave()
			}
			t.Fatalf("%s forged alias was admitted: %v", name, err)
		}
		leave, err := result.manager.enterGeneratedDomainTypedMutation(context.Background())
		if err != nil || leave == nil {
			t.Fatalf("%s forged alias poisoned owner: %v", name, err)
		}
		leave()
	}
	forgedDependency := aliasGeneratedDomainManagerReferencesForTest(result.manager)
	storeCopy := *forgedDependency.store
	forgedDependency.store = &storeCopy
	assertForgedRefused("dependency", forgedDependency)
	forgedLatch := aliasGeneratedDomainManagerReferencesForTest(result.manager)
	forgedLatch.gatewayRebindFailStop = &atomic.Bool{}
	assertForgedRefused("latch", forgedLatch)
	forgedDriver := aliasGeneratedDomainManagerReferencesForTest(result.manager)
	forgedDriver.gatewayCurrentPhysicalDriver = newManagedGatewayCurrentPhysicalDriver(forgedDriver)
	assertForgedRefused("driver", forgedDriver)
	forgedRunner := aliasGeneratedDomainManagerReferencesForTest(result.manager)
	forgedRunner.runner = &generatedDomainRegistryRunner{daemonID: "private-construction-daemon"}
	assertForgedRefused("runner", forgedRunner)
	forgedPhysical := aliasGeneratedDomainManagerReferencesForTest(result.manager)
	forgedPhysical.runner = &generatedDomainRegistryRunner{daemonID: "foreign-private-construction-daemon"}
	if leave, err := forgedPhysical.enterGeneratedDomainTypedMutation(context.Background()); err == nil || leave != nil {
		if leave != nil {
			leave()
		}
		t.Fatalf("physical-identity forged alias was admitted: %v", err)
	}
}

func TestGeneratedDomainConstructionOwnsExactPathTransition(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	runner := &generatedDomainRegistryRunner{daemonID: "path-transition-daemon"}
	physical, proof, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores,
		runner, fixture.options)
	if err != nil || physical.manager.ingressRoot != nil || proof.ingressRoot != nil {
		t.Fatalf("initial absent proof = %#v %#v, %v", physical, proof, err)
	}
	transition, err := proof.createIngressRoot()
	if err != nil || !transition.current() || transition.final.ingressRoot == nil {
		t.Fatalf("owned path transition = %#v, %v", transition, err)
	}
	if _, err := proof.createIngressRoot(); err == nil {
		t.Fatal("immutable absent proof adopted its previously created path")
	}

	foreign := newGeneratedDomainIdentityFixture(t)
	_, foreignProof, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), foreign.stores,
		&generatedDomainRegistryRunner{daemonID: "foreign-transition-daemon"}, foreign.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(foreignProof.ingressPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := foreignProof.createIngressRoot(); err == nil {
		t.Fatal("independently appearing ingress path was adopted")
	}
}

func TestGeneratedDomainInitialRouteStateCreateOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated-ingress")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &stateStore{root: root, path: filepath.Join(root, stateFilename), readOnly: true}
	if err := store.createInitialEmpty(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createInitialEmpty(); err == nil {
		t.Fatal("create-only initial state replaced an existing destination")
	}
	after, err := os.ReadFile(store.path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed create-only retry changed state: %v", err)
	}
	loaded, err := store.load()
	if err != nil || !validRouteState(loaded) || loaded.Pending != nil || len(loaded.Active) != 0 {
		t.Fatalf("initial state readback = %#v, %v", loaded, err)
	}
}
