package generatedingress

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// generatedDomainConstruction is private composition state, not a public
// capability. Publication through the irreversible process reservation occurs
// only after every lock held by empty bootstrap has been proved released. A
// stable construction holds no effects or gateway lock and records that
// distinct no-lock obligation separately in its tests.
type generatedDomainConstruction struct {
	reservation *generatedDomainReservation
	manager     *Manager
	budget      *generatedcapacity.Budget
	candidates  *GeneratedCandidateAuthority
}

var (
	errGeneratedDomainConstructionRefused  = errors.New("generated domain construction was refused")
	errGeneratedDomainConstructionRecovery = errors.New("generated domain construction requires recovery")
)

// newGeneratedDomainConstruction is the only package-private composition
// path. It accepts sealed stores and fixed concrete dependencies, joins one
// irreversible reservation, and publishes no result until all required
// observation and release proofs have completed.
func newGeneratedDomainConstruction(
	ctx context.Context,
	stores *deployments.GeneratedOwnerStores,
	runner runtimeprocess.CommandRunner,
	environment GeneratedCandidateEnvironmentStager,
	options generatedDomainOptions,
) (result *generatedDomainConstruction, resultErr error) {
	if ctx == nil || stores == nil || !generatedCandidatePointerIdentity(runner) ||
		!generatedCandidatePointerIdentity(environment) {
		return nil, errGeneratedDomainStartupInvalid
	}
	var valid bool
	options, valid = normalizeGeneratedDomainOptions(options)
	if !valid {
		return nil, errGeneratedDomainStartupInvalid
	}
	fixedStores := *stores
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, &fixedStores, runner, options)
	if err != nil {
		return nil, err
	}
	input, err := newGeneratedDomainReservationInput(&fixedStores, runner, environment, options, physical)
	if err != nil {
		return nil, err
	}
	reservation, owner, joined, err := reserveGeneratedDomainConstruction(ctx, input)
	if err != nil {
		return nil, err
	}
	if !owner {
		return joined, nil
	}
	completed := false
	defer func() {
		if !completed {
			failGeneratedDomainConstruction(reservation, resultErr)
			result = nil
		}
	}()

	// This complete classifier pass precedes every Manager allocation, path or
	// lock creation, Docker effect, and capacity object. Recovery and refusal
	// become sticky construction failures for every exact joiner.
	classification, err := inspectGeneratedDomainStartup(ctx, input.stores, runner, options)
	if err != nil {
		return nil, err
	}
	switch classification.disposition {
	case generatedDomainStartupStable:
		result, err = constructGeneratedDomainStable(ctx, reservation, input)
	case generatedDomainStartupEmpty:
		result, err = constructGeneratedDomainEmpty(ctx, reservation, input)
	case generatedDomainStartupRecoveryRequired:
		err = errGeneratedDomainConstructionRecovery
	default:
		err = errGeneratedDomainConstructionRefused
	}
	if err != nil {
		return nil, err
	}
	if err := completeGeneratedDomainConstruction(reservation, result); err != nil {
		return nil, err
	}
	completed = true
	return result, nil
}

// constructGeneratedDomainStable performs no persistent creation, chmod,
// effects lease, gateway lock, capacity snapshot, Docker effect, or route
// traffic. Its exclusion boundary is the irreversible in-process reservation,
// controller lease/sealed DB provenance, and repeated complete observations;
// it makes no cross-process atomicity claim.
func constructGeneratedDomainStable(
	ctx context.Context,
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
) (*generatedDomainConstruction, error) {
	store, present, _, err := inspectStateStoreReadOnly(input.options.DataRoot)
	if err != nil || !present || store == nil {
		return nil, errGeneratedDomainConstructionRefused
	}
	manager, err := assembleGeneratedDomainManager(input, store)
	if err != nil {
		return nil, err
	}
	if err := bindGeneratedDomainManagerView(manager, input.physical.manager); err != nil {
		return nil, err
	}
	result, err := assembleGeneratedDomainOwners(reservation, input, manager)
	if err != nil {
		return nil, err
	}
	if err := verifyGeneratedDomainConstructionFinal(ctx, manager, input, input.physical, input.proof,
		generatedDomainStartupStable); err != nil {
		return nil, err
	}
	return result, nil
}

func constructGeneratedDomainEmpty(
	ctx context.Context,
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
) (_ *generatedDomainConstruction, resultErr error) {
	// Empty construction alone creates the effects and gateway lock files. The
	// local Manager exclusion is acquired before the owned path transition and
	// is retained by the same Manager object through bootstrap.
	releaseEffects, err := deploymenteffects.Acquire(ctx, input.options.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	effectsHeld := true
	root, err := inspectGeneratedLegacyRootObservation(input.runner, generatedDomainManagerOptions(input))
	if err != nil || root.ingressRoot != nil ||
		!sameGeneratedDomainFileIdentity(root.dataRoot, input.proof.dataRoot) {
		releaseErr := releaseEffects()
		effectsHeld = false
		return nil, errors.Join(errGeneratedDomainConstructionRefused, err, releaseErr)
	}
	view := newGeneratedManagerView(root)
	releaseActivity, err := beginGeneratedDomainConstructionActivity(reservation, view)
	if err != nil {
		releaseErr := releaseEffects()
		effectsHeld = false
		return nil, errors.Join(err, releaseErr)
	}
	activityHeld := true
	prospective := &Manager{mu: newContextMutex()}
	if err := prospective.mu.LockContext(ctx); err != nil {
		activityErr := releaseActivity()
		activityHeld = false
		releaseErr := releaseEffects()
		effectsHeld = false
		return nil, errors.Join(err, activityErr, releaseErr)
	}
	managerHeld := true
	var releaseGateway func() error
	var manager *Manager
	defer func() {
		if releaseGateway != nil {
			if err := releaseGateway(); err != nil {
				resultErr = errors.Join(resultErr, err)
				if manager != nil {
					manager.gatewayRebindFailStopLatch().Store(true)
				}
			}
			releaseGateway = nil
		}
		if managerHeld {
			prospective.mu.Unlock()
			managerHeld = false
		}
		if activityHeld {
			if err := releaseActivity(); err != nil {
				resultErr = errors.Join(resultErr, err)
				if manager != nil {
					manager.gatewayRebindFailStopLatch().Store(true)
				}
			}
			activityHeld = false
		}
		if effectsHeld {
			if err := releaseEffects(); err != nil {
				resultErr = errors.Join(resultErr, err)
				if manager != nil {
					manager.gatewayRebindFailStopLatch().Store(true)
				}
			}
			effectsHeld = false
		}
	}()

	managerOptions := generatedDomainManagerOptions(input)
	if managerOptions.RebindFenceCheck == nil || managerOptions.generatedDomainRebindRepository == nil {
		return nil, errors.New("generated domain rebind fence is unavailable")
	}
	if err := managerOptions.generatedDomainRebindRepository.CheckGatewayRebindFence(ctx); err != nil {
		return nil, errors.New("generated domain rebind fence is closed")
	}
	transition, err := input.proof.createIngressRoot()
	if err != nil {
		return nil, err
	}
	store := &stateStore{root: transition.final.ingressPath,
		path: filepath.Join(transition.final.ingressPath, stateFilename), readOnly: true}
	if err := assembleManagerInto(prospective, input.runner, managerOptions, store); err != nil {
		return nil, err
	}
	manager = prospective
	physical, proof, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil || physical.manager.daemonID != input.physical.manager.daemonID ||
		!sameGeneratedDomainPathObservation(proof, transition.final, false) {
		return nil, errors.New("generated domain owned path transition changed")
	}
	if err := recordGeneratedDomainConstructionTransition(reservation, physical, proof, transition); err != nil {
		return nil, err
	}
	if err := bindGeneratedDomainManagerViewInto(manager, physical.manager, view); err != nil {
		return nil, err
	}
	releaseGateway, err = managerAcquireGatewayOSLock(ctx, store)
	if err != nil {
		return nil, err
	}
	classification, err := inspectGeneratedDomainStartup(ctx, input.stores, input.runner, input.options)
	if err != nil || classification.disposition != generatedDomainStartupEmpty {
		return nil, errGeneratedDomainConstructionRefused
	}
	if err := bootstrapGeneratedDomainEmpty(ctx, manager, input, physical); err != nil {
		return nil, err
	}
	// Check the completed physical state while both exclusions are still held.
	if err := verifyGeneratedDomainConstructionFinal(ctx, manager, input, physical, transition.final,
		generatedDomainStartupStable); err != nil {
		return nil, err
	}
	result, err := assembleGeneratedDomainOwners(reservation, input, manager)
	if err != nil {
		return nil, err
	}
	// Release in reverse acquisition order. A release error latches the Manager
	// and the deferred path converts it into sticky construction failure.
	if err := releaseGateway(); err != nil {
		releaseGateway = nil
		manager.gatewayRebindFailStopLatch().Store(true)
		return nil, err
	}
	releaseGateway = nil
	prospective.mu.Unlock()
	managerHeld = false
	if err := releaseActivity(); err != nil {
		activityHeld = false
		manager.gatewayRebindFailStopLatch().Store(true)
		return nil, err
	}
	activityHeld = false
	if err := releaseEffects(); err != nil {
		effectsHeld = false
		manager.gatewayRebindFailStopLatch().Store(true)
		return nil, err
	}
	effectsHeld = false
	// The last complete observation occurs only after every empty-path lock has
	// been proved released. The registry remains the in-process authority; this
	// final read does not claim cross-process atomicity.
	if err := verifyGeneratedDomainConstructionFinal(ctx, manager, input, physical, transition.final,
		generatedDomainStartupStable); err != nil {
		return nil, err
	}
	return result, nil
}

func generatedDomainManagerOptions(input generatedDomainReservationInput) Options {
	repository := appaccess.New(input.database)
	return Options{
		DockerExecutable: input.options.DockerExecutable, DockerEndpoint: input.options.DockerEndpoint,
		DockerConfigDirectory: input.options.DockerConfigDirectory, WorkingDirectory: input.options.WorkingDirectory,
		DataRoot: input.options.DataRoot, HostPort: input.options.HostPort,
		CommandTimeout: input.options.CommandTimeout, PullTimeout: input.options.PullTimeout,
		OutputLimit: input.options.OutputLimit, RebindFenceCheck: repository.CheckGatewayRebindFence,
		RebindCurrentStateRepository:    repository,
		generatedDomainRebindRepository: repository,
	}
}

func assembleGeneratedDomainManager(input generatedDomainReservationInput, store *stateStore) (*Manager, error) {
	return assembleManager(input.runner, generatedDomainManagerOptions(input), store)
}

func bindGeneratedDomainManagerView(manager *Manager, identity generatedManagerPhysicalIdentity) error {
	return bindGeneratedDomainManagerViewInto(manager, identity, nil)
}

func bindGeneratedDomainManagerViewInto(manager *Manager, identity generatedManagerPhysicalIdentity,
	view *generatedManagerView,
) error {
	if manager == nil {
		return errors.New("generated domain Manager is unavailable")
	}
	var root generatedLegacyRootObservation
	var err error
	if view == nil {
		root, err = inspectGeneratedLegacyRootObservation(manager.runner, manager.options)
		if err == nil {
			view = newGeneratedManagerView(root)
		}
	} else {
		view.mu.Lock()
		root = view.root
		view.mu.Unlock()
		root, err = root.recheck(true)
	}
	if err != nil || root.ingressRoot == nil || view == nil {
		return errors.New("generated domain Manager root is unavailable")
	}
	root.dockerEnv = append([]string(nil), manager.dockerEnv...)
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.manager != nil || view.stale || view.pairing {
		return errors.New("generated domain Manager view is unavailable")
	}
	view.root = root
	view.manager, view.identity, view.identityBound = manager, identity, true
	manager.domainView = view
	return nil
}

func assembleGeneratedDomainOwners(
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
	manager *Manager,
) (*generatedDomainConstruction, error) {
	budget, err := generatedcapacity.New(manager)
	if err != nil {
		return nil, err
	}
	candidates, err := newGeneratedCandidateAuthorityConcrete(input.stores, input.runner, input.environment,
		budget, generatedDomainCandidateOptions(input.options))
	if err != nil {
		return nil, err
	}
	return &generatedDomainConstruction{reservation: reservation, manager: manager,
		budget: budget, candidates: candidates}, nil
}

func validGeneratedDomainConstructionResult(reservation *generatedDomainReservation,
	result *generatedDomainConstruction,
) bool {
	if reservation == nil || result == nil || result.reservation != reservation || result.manager == nil ||
		result.budget == nil || !result.budget.Configured() || result.candidates == nil {
		return false
	}
	manager, candidates, input := result.manager, result.candidates, reservation.input
	if manager.domainView == nil || manager.domainView.manager != manager || manager.runner == nil || manager.store == nil ||
		!sameGeneratedPointer(manager.runner, input.runner) ||
		!sameGeneratedManagerIdentityOptions(manager.options, generatedDomainManagerOptions(input)) ||
		manager.options.generatedDomainRebindRepository == nil ||
		manager.options.RebindCurrentStateRepository != manager.options.generatedDomainRebindRepository ||
		manager.gatewayRebindFailStop != &gatewayRebindProcessFailStop ||
		manager.gatewayRebindCommitBarrier != &gatewayRebindProcessCommitBarrier ||
		manager.gatewayRebindCrossStoreDriver == nil || manager.gatewayCurrentPhysicalDriver == nil {
		return false
	}
	return candidates.stores == input.stores && sameGeneratedPointer(candidates.process, input.runner) &&
		sameGeneratedPointer(candidates.environment, input.environment) && candidates.budget == result.budget &&
		candidates.ledger != nil && candidates.heads != nil &&
		candidates.options == generatedDomainCandidateOptions(input.options)
}

func verifyGeneratedDomainConstructionFinal(
	ctx context.Context,
	manager *Manager,
	input generatedDomainReservationInput,
	expected generatedDomainPhysicalIdentity,
	expectedProof generatedDomainPathProof,
	disposition generatedDomainStartupDisposition,
) error {
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return err
	}
	classification, err := inspectGeneratedDomainStartup(ctx, input.stores, input.runner, input.options)
	if err != nil || classification.disposition != disposition || len(classification.recovery) != 0 {
		return errGeneratedDomainConstructionRefused
	}
	physical, proof, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil || !sameGeneratedDomainPhysicalIdentity(physical, expected) ||
		!sameGeneratedDomainPathObservation(proof, expectedProof, false) {
		return errors.New("generated domain physical identity changed")
	}
	if gatewayRebindProcessFailStop.Load() || gatewayRebindProcessCommitBarrier.Load() {
		return errors.New("generated domain process latch is closed")
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return err
	}
	return nil
}

func checkGeneratedDomainManagerRebindFence(ctx context.Context, manager *Manager) error {
	if ctx == nil || manager == nil || manager.options.RebindFenceCheck == nil ||
		manager.options.generatedDomainRebindRepository == nil ||
		manager.options.RebindCurrentStateRepository != manager.options.generatedDomainRebindRepository {
		return errors.New("generated domain rebind fence is unavailable")
	}
	if err := manager.options.generatedDomainRebindRepository.CheckGatewayRebindFence(ctx); err != nil {
		return errors.New("generated domain rebind fence is closed")
	}
	return nil
}
