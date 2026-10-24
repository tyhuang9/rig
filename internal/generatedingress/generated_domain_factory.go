package generatedingress

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deploymentexecution"
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
	routes      *GeneratedRouteAuthority
	domain      *GeneratedDomain
	cold        *generatedDomainColdManifest
	manifest    *generatedDomainConstructionManifest
}

type generatedDomainConstructionManifest struct {
	startup  generatedDomainStartupObservation
	physical generatedDomainPhysicalIdentity
	proof    generatedDomainPathProof
	owners   generatedDomainOwnerManifest
	cold     *generatedDomainColdManifest
}

type generatedDomainOwnerManifest struct {
	capacityGroups int
	headGroups     int
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
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageStartupObservation,
			generatedDomainConstructionReasonInput, errGeneratedDomainStartupInvalid)
	}
	var valid bool
	options, valid = normalizeGeneratedDomainOptions(options)
	if !valid {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageStartupObservation,
			generatedDomainConstructionReasonInput, errGeneratedDomainStartupInvalid)
	}
	fixedStores := *stores
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, &fixedStores, runner, options)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageStartupObservation,
			generatedDomainConstructionReasonPhysicalIdentity, err)
	}
	input, err := newGeneratedDomainReservationInput(&fixedStores, runner, environment, options, physical)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageInitialClassification,
			generatedDomainConstructionReasonInput, err)
	}
	reservation, owner, joined, err := reserveGeneratedDomainConstruction(ctx, input)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageInitialClassification,
			generatedDomainConstructionReasonDisposition, err)
	}
	if !owner {
		joined, err = reattestGeneratedDomainConstructionJoin(ctx, reservation, input, joined)
		if err != nil {
			return nil, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageJoin, generatedDomainConstructionReasonManifest, err)
		}
		return joined, nil
	}
	completed := false
	defer func() {
		if !completed {
			resultErr = generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageManifest, generatedDomainConstructionReasonManifest, resultErr)
			failGeneratedDomainConstruction(reservation, resultErr)
			result = nil
		}
	}()

	// This complete classifier pass precedes every Manager allocation, path or
	// lock creation, Docker effect, and capacity object. Recovery and refusal
	// become sticky construction failures for every exact joiner.
	observation, err := inspectGeneratedDomainStartupObservation(ctx, input.stores, runner, options)
	if err != nil {
		return nil, err
	}
	switch {
	case observation.classification.disposition == generatedDomainStartupStable:
		result, err = constructGeneratedDomainStable(ctx, reservation, input)
		err = generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageStableConstruction,
			generatedDomainConstructionReasonRecovery, err)
	case observation.classification.disposition == generatedDomainStartupEmpty:
		result, err = constructGeneratedDomainEmpty(ctx, reservation, input)
		err = generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageEmptyConstruction,
			generatedDomainConstructionReasonRecovery, err)
	case observation.classification.disposition == generatedDomainStartupRecoveryRequired:
		err = generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageInitialClassification,
			generatedDomainConstructionReasonRecovery, errGeneratedDomainConstructionRecovery)
	case observation.classification.disposition == generatedDomainStartupRefused &&
		observation.classification.reason == generatedDomainStartupReasonColdAccounting &&
		len(observation.classification.recovery) == 0:
		result, err = constructGeneratedDomainCold(ctx, reservation, input, observation)
		err = generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonFinalValidation, err)
	default:
		reason := generatedDomainConstructionReasonDisposition
		if len(observation.classification.recovery) != 0 {
			reason = generatedDomainConstructionReasonRecovery
		}
		err = generatedDomainConstructionFinalDiagnostic(
			generatedDomainConstructionStageInitialClassification, reason, nil)
	}
	if err != nil {
		return nil, err
	}
	manifest, err := captureGeneratedDomainConstructionManifest(ctx, reservation, input, result)
	if err != nil {
		return nil, err
	}
	result.manifest = &manifest
	if err := completeGeneratedDomainConstruction(reservation, result); err != nil {
		return nil, err
	}
	completed = true
	return result, nil
}

func constructGeneratedDomainCold(
	ctx context.Context,
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
	initial generatedDomainStartupObservation,
) (_ *generatedDomainConstruction, resultErr error) {
	if initial.classification.disposition != generatedDomainStartupRefused ||
		initial.classification.reason != generatedDomainStartupReasonColdAccounting ||
		len(initial.classification.recovery) != 0 || input.proof.ingressRoot == nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonInput, errGeneratedDomainConstructionRefused)
	}
	releaseEffects, err := deploymenteffects.Acquire(ctx, input.options.WorkingDirectory)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonLock, err)
	}
	effectsHeld := true
	root, err := inspectGeneratedLegacyRootObservation(input.runner, generatedDomainManagerOptions(input))
	if err != nil || root.ingressRoot == nil ||
		!sameGeneratedDomainFileIdentity(root.dataRoot, input.proof.dataRoot) ||
		!sameGeneratedDomainFileIdentity(root.ingressRoot, input.proof.ingressRoot) {
		releaseErr := releaseEffects()
		effectsHeld = false
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonPathIdentity,
			errors.Join(errGeneratedDomainConstructionRefused, err, releaseErr))
	}
	view := newGeneratedManagerView(root)
	releaseActivity, err := beginGeneratedDomainColdConstructionActivity(reservation, view)
	if err != nil {
		releaseErr := releaseEffects()
		effectsHeld = false
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonLock,
			errors.Join(err, releaseErr))
	}
	activityHeld := true
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
		resultErr = generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonRelease, resultErr)
	}()

	store, present, _, err := inspectStateStoreReadOnly(input.options.DataRoot)
	if err != nil || !present || store == nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonStateStore,
			errors.Join(errGeneratedDomainConstructionRefused, err))
	}
	manager, err = assembleGeneratedDomainManager(input, store)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonAssembly, err)
	}
	if err := bindGeneratedDomainManagerViewInto(manager, input.physical.manager, view); err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonAssembly, err)
	}
	result, err := assembleGeneratedDomainOwners(reservation, input, manager)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonAssembly, err)
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonFence, err)
	}
	observed, err := inspectGeneratedDomainStartupObservation(ctx, input.stores, input.runner, input.options)
	if err != nil || !sameGeneratedDomainStartupObservation(initial, observed) {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonStability,
			errors.Join(errGeneratedDomainConstructionRefused, err))
	}
	cold, err := reconstructGeneratedDomainCold(ctx, result, observed)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdAccounting, err)
	}
	result.cold = &cold

	// Raw capacity acquisition above invokes Manager.Snapshot and therefore
	// owns the Manager/gateway lock itself. Acquire the construction-wide lock
	// only after every risky share is retained and seeded.
	releaseGateway, err = manager.lockGateway(ctx)
	if err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonLock, err)
	}
	if err := verifyGeneratedDomainColdConstruction(ctx, reservation, input, result, initial); err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonFinalValidation, err)
	}
	if err := releaseGateway(); err != nil {
		releaseGateway = nil
		manager.gatewayRebindFailStopLatch().Store(true)
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonRelease, err)
	}
	releaseGateway = nil
	if err := releaseActivity(); err != nil {
		activityHeld = false
		manager.gatewayRebindFailStopLatch().Store(true)
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonRelease, err)
	}
	activityHeld = false
	if err := releaseEffects(); err != nil {
		effectsHeld = false
		manager.gatewayRebindFailStopLatch().Store(true)
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, generatedDomainConstructionReasonRelease, err)
	}
	effectsHeld = false
	if err := verifyGeneratedDomainColdConstruction(ctx, reservation, input, result, initial); err != nil {
		return nil, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonFinalValidation, err)
	}
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
		return nil, generatedDomainConstructionFinalDiagnostic(
			generatedDomainConstructionStageStableFinal, generatedDomainConstructionReasonStateStore, err)
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
		generatedDomainConstructionStageStableFinal,
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
		return nil, generatedDomainConstructionFinalDiagnostic(
			generatedDomainConstructionStageEmptyReclassification,
			generatedDomainConstructionReasonClassification, err)
	}
	if err := bootstrapGeneratedDomainEmpty(ctx, manager, input, physical); err != nil {
		return nil, err
	}
	// Check the completed physical state while both exclusions are still held.
	if err := verifyGeneratedDomainConstructionFinal(ctx, manager, input, physical, transition.final,
		generatedDomainConstructionStageEmptyFinalLocked,
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
		generatedDomainConstructionStageEmptyFinalReleased,
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
	routes, err := newGeneratedRouteAuthority(manager, candidates, input.stores)
	if err != nil {
		return nil, err
	}
	result := &generatedDomainConstruction{reservation: reservation, manager: manager,
		budget: budget, candidates: candidates, routes: routes}
	result.domain = &GeneratedDomain{construction: result}
	return result, nil
}

func validGeneratedDomainConstructionResult(reservation *generatedDomainReservation,
	result *generatedDomainConstruction,
) bool {
	if reservation == nil || result == nil || result.reservation != reservation || result.manager == nil ||
		result.budget == nil || !result.budget.Configured() || result.candidates == nil ||
		result.routes == nil || !result.routes.configuredFor(result) || result.domain == nil ||
		result.domain.construction != result || result.manifest == nil ||
		!sameGeneratedDomainColdManifest(result.cold, result.manifest.cold) {
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

func captureGeneratedDomainConstructionManifest(
	ctx context.Context,
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
	result *generatedDomainConstruction,
) (generatedDomainConstructionManifest, error) {
	var empty generatedDomainConstructionManifest
	if ctx == nil || reservation == nil || result == nil || result.reservation != reservation ||
		result.manager == nil || result.candidates == nil || !generatedDomainConstructionInputMatches(reservation, input) {
		return empty, deploymentexecution.ErrInvalidConfiguration
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, result.manager); err != nil {
		return empty, err
	}
	startup, err := inspectGeneratedDomainStartupObservation(ctx, input.stores, input.runner, input.options)
	if err != nil || len(startup.classification.recovery) != 0 {
		reason := generatedDomainConstructionReasonFromError(err, generatedDomainConstructionReasonClassification)
		if err == nil && len(startup.classification.recovery) != 0 {
			reason = generatedDomainConstructionReasonRecovery
		}
		return empty, newGeneratedDomainConstructionDiagnostic(
			generatedDomainConstructionStageManifest, reason, errGeneratedDomainConstructionRefused, err)
	}
	var cold *generatedDomainColdManifest
	switch {
	case result.cold == nil && startup.classification.disposition == generatedDomainStartupStable:
	case result.cold != nil && startup.classification.disposition == generatedDomainStartupRefused &&
		startup.classification.reason == generatedDomainStartupReasonColdAccounting:
		if err := reattestGeneratedDomainCold(ctx, result.manager, result.candidates, startup, *result.cold); err != nil {
			return empty, err
		}
		cold = cloneGeneratedDomainColdManifest(*result.cold)
	default:
		return empty, generatedDomainConstructionFinalDiagnostic(
			generatedDomainConstructionStageManifest, generatedDomainConstructionReasonDisposition, nil)
	}
	physical, proof, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil {
		return empty, err
	}
	expectedPhysical, expectedProof, ok := generatedDomainConstructionExpectedIdentity(reservation)
	if !ok || !sameGeneratedDomainPhysicalIdentity(physical, expectedPhysical) ||
		!sameGeneratedDomainPathObservation(proof, expectedProof, false) {
		return empty, errors.New("generated domain construction identity changed")
	}
	owners, err := inspectGeneratedDomainOwnerManifest(result)
	expectedOwners := generatedDomainOwnerManifest{}
	if cold != nil {
		expectedOwners = generatedDomainColdOwnerManifest(*cold)
	}
	if err != nil || owners != expectedOwners {
		return empty, errors.Join(deploymentexecution.ErrInvalidConfiguration, err)
	}
	if gatewayRebindProcessFailStop.Load() || gatewayRebindProcessCommitBarrier.Load() {
		return empty, errors.New("generated domain process latch is closed")
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, result.manager); err != nil {
		return empty, err
	}
	return generatedDomainConstructionManifest{startup: startup, physical: physical, proof: proof,
		owners: owners, cold: cold}, nil
}

func reattestGeneratedDomainConstructionJoin(
	ctx context.Context,
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
	joined *generatedDomainConstruction,
) (*generatedDomainConstruction, error) {
	if !validGeneratedDomainConstructionResult(reservation, joined) ||
		!generatedDomainConstructionInputMatches(reservation, input) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	observed, err := captureGeneratedDomainConstructionManifest(ctx, reservation, input, joined)
	if err != nil {
		return nil, err
	}
	manifest := joined.manifest
	if manifest == nil || !sameGeneratedDomainStartupObservation(manifest.startup, observed.startup) ||
		!sameGeneratedDomainPhysicalIdentity(manifest.physical, observed.physical) ||
		!sameGeneratedDomainPathObservation(manifest.proof, observed.proof, false) ||
		manifest.owners != observed.owners || !sameGeneratedDomainColdManifest(manifest.cold, observed.cold) {
		return nil, generatedDomainConstructionFinalDiagnostic(
			generatedDomainConstructionStageJoin, generatedDomainConstructionReasonManifest, nil)
	}
	return joined, nil
}

func generatedDomainConstructionInputMatches(
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
) bool {
	if reservation == nil || reservation.state == nil || !sameGeneratedDomainReservationProvenance(reservation.input, input) {
		return false
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	if sameGeneratedDomainReservationInput(reservation.input, input) {
		return true
	}
	transition := reservation.state.transition
	return transition != nil && sameGeneratedDomainPhysicalIdentity(transition.physical, input.physical) &&
		sameGeneratedDomainPathObservation(transition.proof, input.proof, false)
}

func generatedDomainConstructionExpectedIdentity(
	reservation *generatedDomainReservation,
) (generatedDomainPhysicalIdentity, generatedDomainPathProof, bool) {
	if reservation == nil || reservation.state == nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, false
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	if transition := reservation.state.transition; transition != nil {
		return transition.physical, transition.proof, true
	}
	return reservation.input.physical, reservation.input.proof, true
}

func inspectGeneratedDomainOwnerManifest(result *generatedDomainConstruction) (generatedDomainOwnerManifest, error) {
	if result == nil || result.candidates == nil || result.candidates.ledger == nil || result.candidates.heads == nil {
		return generatedDomainOwnerManifest{}, deploymentexecution.ErrInvalidConfiguration
	}
	result.candidates.ledger.mu.Lock()
	capacityGroups := len(result.candidates.ledger.groups)
	result.candidates.ledger.mu.Unlock()
	result.candidates.heads.mu.Lock()
	headGroups := len(result.candidates.heads.groups)
	result.candidates.heads.mu.Unlock()
	return generatedDomainOwnerManifest{capacityGroups: capacityGroups, headGroups: headGroups}, nil
}

func verifyGeneratedDomainConstructionFinal(
	ctx context.Context,
	manager *Manager,
	input generatedDomainReservationInput,
	expected generatedDomainPhysicalIdentity,
	expectedProof generatedDomainPathProof,
	stage generatedDomainConstructionStage,
	disposition generatedDomainStartupDisposition,
) error {
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return newGeneratedDomainConstructionDiagnostic(stage,
			generatedDomainConstructionReasonFence, err)
	}
	classification, err := inspectGeneratedDomainStartup(ctx, input.stores, input.runner, input.options)
	if err != nil || classification.disposition != disposition || len(classification.recovery) != 0 {
		reason := generatedDomainConstructionReasonClassification
		if err == nil && len(classification.recovery) != 0 {
			reason = generatedDomainConstructionReasonRecovery
		} else if err == nil {
			reason = generatedDomainConstructionReasonDisposition
		}
		return generatedDomainConstructionFinalDiagnostic(stage, reason, err)
	}
	physical, proof, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil || !sameGeneratedDomainPhysicalIdentity(physical, expected) ||
		!sameGeneratedDomainPathObservation(proof, expectedProof, false) {
		return newGeneratedDomainConstructionDiagnostic(stage,
			generatedDomainConstructionReasonFromError(err, generatedDomainConstructionReasonPhysicalIdentity),
			errors.New("generated domain physical identity changed"))
	}
	if gatewayRebindProcessFailStop.Load() || gatewayRebindProcessCommitBarrier.Load() {
		return newGeneratedDomainConstructionDiagnostic(stage,
			generatedDomainConstructionReasonProcessLatch,
			errors.New("generated domain process latch is closed"))
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return newGeneratedDomainConstructionDiagnostic(stage,
			generatedDomainConstructionReasonFence, err)
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
