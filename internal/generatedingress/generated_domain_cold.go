package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

type generatedDomainColdGroupKind uint8

const (
	generatedDomainColdNoEffect generatedDomainColdGroupKind = iota + 1
	generatedDomainColdRisky
	generatedDomainColdCurrent
	generatedDomainColdHistorical
)

type generatedDomainColdGroup struct {
	pins         candidateValidationPins
	protected    candidateProtectedSnapshot
	snapshot     candidateAdmissionSnapshot
	kind         generatedDomainColdGroupKind
	policies     map[string]candidatePolicy
	receipts     map[string]candidateColdReceiptSeed
	bindHead     bool
	selfHead     bool
	legacyHead   bool
	restoredDebt bool
}

type generatedDomainColdManifest struct {
	groups                        []generatedDomainColdManifestGroup
	retainedGateway               generatedDomainColdRetainedGatewayProof
	retainedV2                    generatedDomainColdRetainedV2Proof
	pendingActivation             *generatedRouteActivationGatewayTransition
	pendingActivationDeploymentID string
	pendingGatewayDisposition     generatedDomainColdPendingGatewayDisposition
}

type generatedDomainColdManifestGroup struct {
	appID, deploymentID string
	pins                candidateImmutablePins
	components          []generatedDomainColdManifestComponent
	head                *candidateHeadSnapshot
}

type generatedDomainColdManifestComponent struct {
	name, receiptID string
	receiptWord     uint64
	charged         bool
	retained        bool
}

type generatedDomainColdPendingActivationEvidence struct {
	transition          *generatedRouteActivationGatewayTransition
	successorDeployment string
	beforeActivation    bool
	protectedGateway    generatedDomainGatewayExpectation
	routeGateway        generatedDomainGatewayExpectation
	servingGateway      generatedDomainGatewayExpectation
	disposition         generatedDomainColdPendingGatewayDisposition
}

// generatedDomainColdPendingGatewayDisposition records the one physical
// projection that cold construction observed for a typed Pending activation.
// It is descriptive evidence only: construction does not clear Pending or
// publish either projection.
type generatedDomainColdPendingGatewayDisposition uint8

const (
	generatedDomainColdPendingGatewayUnknown generatedDomainColdPendingGatewayDisposition = iota
	generatedDomainColdPendingGatewayProposedExact
	generatedDomainColdPendingGatewayPreviousExact
)

func sameGeneratedDomainColdManifest(left, right *generatedDomainColdManifest) bool {
	return reflect.DeepEqual(left, right)
}

func cloneGeneratedDomainColdManifest(value generatedDomainColdManifest) *generatedDomainColdManifest {
	result := &generatedDomainColdManifest{groups: make([]generatedDomainColdManifestGroup, len(value.groups)),
		retainedGateway:               cloneGeneratedDomainColdRetainedGatewayProof(value.retainedGateway),
		retainedV2:                    cloneGeneratedDomainColdRetainedV2Proof(value.retainedV2),
		pendingActivation:             cloneGeneratedRouteActivationGatewayTransition(value.pendingActivation),
		pendingActivationDeploymentID: value.pendingActivationDeploymentID,
		pendingGatewayDisposition:     value.pendingGatewayDisposition}
	for index, group := range value.groups {
		result.groups[index] = group
		result.groups[index].components = append([]generatedDomainColdManifestComponent(nil), group.components...)
		if group.head != nil {
			head := *group.head
			result.groups[index].head = &head
		}
	}
	return result
}

func reconstructGeneratedDomainCold(ctx context.Context, result *generatedDomainConstruction,
	observed generatedDomainStartupObservation,
) (generatedDomainColdManifest, error) {
	if result == nil || result.candidates == nil || result.candidates.ledger == nil ||
		observed.classification.disposition != generatedDomainStartupRefused ||
		observed.classification.reason != generatedDomainStartupReasonColdAccounting ||
		len(observed.classification.recovery) != 0 {
		return generatedDomainColdManifest{}, errGeneratedDomainConstructionRefused
	}
	groups, err := readGeneratedDomainColdGroups(ctx, result.candidates, observed)
	if err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupReconstruction, err)
	}
	pendingActivation, pendingActivationFound, err := generatedDomainColdPendingActivation(
		observed.database, observed.protected, groups)
	if err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdOwnership, err)
	}
	var pendingEvidence *generatedDomainColdPendingActivationEvidence
	if pendingActivationFound {
		pendingEvidence = &pendingActivation
	}
	var retainedGateway generatedDomainColdRetainedGatewayProof
	var retainedV2 generatedDomainColdRetainedV2Proof
	if err := attestGeneratedDomainColdGroupsWithPendingProofs(ctx, result.manager, result.candidates, observed, groups,
		pendingEvidence, &retainedGateway, &retainedV2); err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupAttestation, err)
	}

	// Classify every completed Current and every exact interrupted Draining
	// handoff from the same typed startup snapshot, then install their configured
	// debt as one closed bootstrap batch. No Budget snapshot is available until
	// the whole batch and its ledger tokens exist.
	restoredDeployments := make(map[string]struct{})
	// FinishHealth marks a complete candidate roster handed to the route before
	// it durably enters SwitchingRoute. The retained pre-042 Pending proof is
	// the only cold shape that can reconstruct that in-memory marker: generic
	// healthy candidates remain merely Present. Validate the complete clone
	// before installing any bootstrap capacity so a partial or crossed Pending
	// cannot retain a share or manufacture route ownership.
	preActivationSeeds := map[string]map[string]candidateColdReceiptSeed{}
	if pendingActivationFound && pendingActivation.beforeActivation {
		matched := 0
		for index := range groups {
			group := groups[index]
			if group.pins.deploymentID != pendingActivation.successorDeployment {
				continue
			}
			matched++
			seeds, valid := generatedDomainColdPreActivationHandoffSeeds(group, pendingActivation)
			if !valid {
				return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdOwnership, errGeneratedDomainConstructionRefused)
			}
			preActivationSeeds[group.pins.deploymentID] = seeds
		}
		if matched != 1 || len(preActivationSeeds) != 1 {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdOwnership, errGeneratedDomainConstructionRefused)
		}
	}
	if pendingActivationFound && pendingActivation.beforeActivation {
		restoredDeployments[pendingActivation.successorDeployment] = struct{}{}
	}
	for index := range groups {
		switch groups[index].kind {
		case generatedDomainColdCurrent:
			restore, evidenceErr := generatedDomainColdCurrentDebtEvidence(observed.database, groups[index], groups)
			if evidenceErr != nil {
				return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdOwnership, evidenceErr)
			}
			if restore {
				restoredDeployments[groups[index].pins.deploymentID] = struct{}{}
			}
		case generatedDomainColdRisky:
			deployments, evidenceErr := generatedDomainColdDrainingDebtDeployments(
				observed.database, groups[index], groups)
			if evidenceErr != nil {
				return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdOwnership, evidenceErr)
			}
			for _, deploymentID := range deployments {
				restoredDeployments[deploymentID] = struct{}{}
			}
		}
	}
	var debtLimits []generatedcapacity.Limits
	for index := range groups {
		_, groups[index].restoredDebt = restoredDeployments[groups[index].pins.deploymentID]
		if groups[index].restoredDebt {
			limit := generatedDomainColdGroupSpec(result.candidates, groups[index]).limits
			for range groups[index].snapshot.components {
				debtLimits = append(debtLimits, limit)
			}
		}
	}
	var debtTokens []*generatedDomainCapacityDebtToken
	if len(debtLimits) != 0 {
		debtTokens, err = result.capacity.installBootstrapBatch(debtLimits)
		if err != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdOwnership, err)
		}
	}
	debtOffset := 0
	for index := range groups {
		group := &groups[index]
		spec := generatedDomainColdGroupSpec(result.candidates, *group)
		var seedErr error
		if group.restoredDebt {
			count := len(group.snapshot.components)
			if debtOffset+count > len(debtTokens) {
				seedErr = errGeneratedDomainCapacityDebtState
			} else {
				owner := candidateAccountingRouteActive
				seeds := group.receipts
				if pendingActivationFound && pendingActivation.beforeActivation &&
					group.pins.deploymentID == pendingActivation.successorDeployment {
					owner = candidateAccountingCandidate
					var found bool
					seeds, found = preActivationSeeds[group.pins.deploymentID]
					if !found {
						seedErr = errGeneratedDomainConstructionRefused
					}
				}
				if seedErr == nil {
					seedErr = result.candidates.ledger.seedColdOwnershipWithDebtFor(
						spec, seeds, debtTokens[debtOffset:debtOffset+count], owner)
				}
				debtOffset += count
			}
		} else if group.kind != generatedDomainColdRisky {
			seedErr = result.candidates.ledger.seedColdOwnership(spec, group.receipts)
		}
		if seedErr != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdOwnership, seedErr)
		}
	}
	if debtOffset != len(debtTokens) {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdOwnership, errGeneratedDomainCapacityDebtState)
	}
	if err := result.capacity.sealBootstrap(); err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdOwnership, err)
	}
	for index := range groups {
		group := &groups[index]
		if group.kind != generatedDomainColdRisky || group.restoredDebt {
			continue
		}
		spec := generatedDomainColdGroupSpec(result.candidates, *group)
		reservation, reserveErr := result.candidates.ledger.reserve(ctx, spec)
		if reserveErr != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdReservation, reserveErr)
		}
		// Reconstruction is conservative: once raw shares are acquired they
		// remain retained across every later refusal or uncertain observation.
		retainErr := reservation.retainAll()
		seedErr := seedCandidateRisk(reservation, group.snapshot)
		releaseErr := reservation.release()
		if retainErr != nil || seedErr != nil || releaseErr != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdReservation,
				errors.Join(retainErr, seedErr, releaseErr))
		}
		if err := result.candidates.ledger.seedColdOwnership(spec, group.receipts); err != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdOwnership, err)
		}
	}
	for index := range groups {
		group := &groups[index]
		if !group.bindHead {
			continue
		}
		var bindErr error
		if group.selfHead {
			_, bindErr = result.candidates.heads.bindColdSelf(group.snapshot.headPins,
				group.snapshot.observedHead, group.legacyHead)
		} else {
			_, bindErr = result.candidates.heads.bindPredecessor(group.snapshot.headPins,
				group.snapshot.observedHead)
		}
		if bindErr != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdHead, bindErr)
		}
	}
	manifest, err := snapshotGeneratedDomainColdManifest(result.candidates)
	if err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdManifest, err)
	}
	manifest.retainedGateway = cloneGeneratedDomainColdRetainedGatewayProof(retainedGateway)
	manifest.retainedV2 = cloneGeneratedDomainColdRetainedV2Proof(retainedV2)
	if pendingActivationFound {
		manifest.pendingActivation = cloneGeneratedRouteActivationGatewayTransition(pendingActivation.transition)
		manifest.pendingActivationDeploymentID = pendingActivation.successorDeployment
		manifest.pendingGatewayDisposition = pendingActivation.disposition
	}
	return manifest, nil
}

// generatedDomainColdPreActivationHandoffSeeds clones the one exact
// pre-activation successor roster whose Pending proof also authenticates a
// completed FinishHealth boundary. It returns no partial result: every
// healthy component must have the exact present receipt reconstructed from
// the same group before the clone is marked handed to the route.
func generatedDomainColdPreActivationHandoffSeeds(group generatedDomainColdGroup,
	pending generatedDomainColdPendingActivationEvidence,
) (map[string]candidateColdReceiptSeed, bool) {
	if !pending.beforeActivation || pending.successorDeployment == "" ||
		group.pins.deploymentID != pending.successorDeployment || group.kind != generatedDomainColdRisky ||
		!group.bindHead || group.selfHead || group.snapshot.runtime.phase != generatedruntimestate.PhaseSwitchingRoute ||
		group.snapshot.observedHead.deploymentID != group.snapshot.headPins.previousDeploymentID ||
		len(group.snapshot.components) == 0 || len(group.receipts) != len(group.snapshot.components) {
		return nil, false
	}
	result := make(map[string]candidateColdReceiptSeed, len(group.receipts))
	for _, component := range group.snapshot.components {
		name := component.plan.Name
		seed, found := group.receipts[name]
		if component.state != generatedruntimestate.ComponentHealthy || component.receipt == nil || name == "" ||
			!found || seed.id == "" || seed.id != component.receipt.id || seed.unknown ||
			seed.physical != candidatePhysicalPresent {
			return nil, false
		}
		if _, duplicate := result[name]; duplicate {
			return nil, false
		}
		seed.physical = candidatePhysicalHandedToRoute
		result[name] = seed
	}
	return result, len(result) == len(group.receipts)
}

func verifyGeneratedDomainColdConstruction(ctx context.Context, reservation *generatedDomainReservation,
	input generatedDomainReservationInput, result *generatedDomainConstruction,
	expected generatedDomainStartupObservation,
) error {
	if ctx == nil || reservation == nil || result == nil || result.cold == nil || result.manager == nil ||
		result.candidates == nil || !generatedDomainConstructionInputMatches(reservation, input) {
		return deploymentexecution.ErrInvalidConfiguration
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, result.manager); err != nil {
		return err
	}
	observed, err := inspectGeneratedDomainStartupObservation(ctx, input.stores, input.runner, input.options)
	if err != nil || observed.classification.disposition != generatedDomainStartupRefused ||
		observed.classification.reason != generatedDomainStartupReasonColdAccounting ||
		len(observed.classification.recovery) != 0 ||
		!sameGeneratedDomainStartupObservation(expected, observed) {
		return errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	physical, proof, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil {
		return err
	}
	expectedPhysical, expectedProof, ok := generatedDomainConstructionExpectedIdentity(reservation)
	if !ok || !sameGeneratedDomainPhysicalIdentity(physical, expectedPhysical) ||
		!sameGeneratedDomainPathObservation(proof, expectedProof, false) {
		return errors.New("generated domain cold identity changed")
	}
	if err := reattestGeneratedDomainCold(ctx, result.manager, result.candidates, observed, *result.cold); err != nil {
		return err
	}
	if gatewayRebindProcessFailStop.Load() || gatewayRebindProcessCommitBarrier.Load() {
		return errors.New("generated domain process latch is closed")
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, result.manager); err != nil {
		return err
	}
	return nil
}

func reattestGeneratedDomainCold(ctx context.Context, manager *Manager, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, expected generatedDomainColdManifest,
) error {
	groups, pendingEvidence, err := readGeneratedDomainColdGroupsForReattestation(
		ctx, authority, observed, &expected)
	if err != nil {
		return err
	}
	var retainedGateway generatedDomainColdRetainedGatewayProof
	var retainedV2 generatedDomainColdRetainedV2Proof
	if err := attestGeneratedDomainColdGroupsWithPendingProofs(ctx, manager, authority, observed, groups,
		pendingEvidence, &retainedGateway, &retainedV2); err != nil {
		return err
	}
	current, err := snapshotGeneratedDomainColdManifest(authority)
	current.retainedGateway = cloneGeneratedDomainColdRetainedGatewayProof(retainedGateway)
	current.retainedV2 = cloneGeneratedDomainColdRetainedV2Proof(retainedV2)
	if pendingEvidence != nil {
		current.pendingActivation = cloneGeneratedRouteActivationGatewayTransition(pendingEvidence.transition)
		current.pendingActivationDeploymentID = pendingEvidence.successorDeployment
		current.pendingGatewayDisposition = pendingEvidence.disposition
	} else if expected.pendingActivation != nil {
		if !generatedDomainColdFinalizedActivationMatches(observed, groups, expected) {
			return errGeneratedDomainConstructionRefused
		}
		current.pendingActivation = cloneGeneratedRouteActivationGatewayTransition(expected.pendingActivation)
		current.pendingActivationDeploymentID = expected.pendingActivationDeploymentID
		current.pendingGatewayDisposition = expected.pendingGatewayDisposition
	}
	if err != nil || !reflect.DeepEqual(expected, current) {
		return errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	return nil
}

func generatedDomainColdFinalizedActivationMatches(observed generatedDomainStartupObservation,
	groups []generatedDomainColdGroup, expected generatedDomainColdManifest,
) bool {
	transition := expected.pendingActivation
	if !validGeneratedRouteActivationGatewayTransition(transition) ||
		expected.pendingActivationDeploymentID == "" ||
		expected.pendingGatewayDisposition == generatedDomainColdPendingGatewayUnknown {
		return false
	}
	gateway, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
	if err != nil {
		return false
	}
	switch transition.kind {
	case generatedDomainGatewayNativeV1:
		if gateway.kind != generatedDomainGatewayNativeV1 || gateway.nativeV1 == nil ||
			!reflect.DeepEqual(*gateway.nativeV1, transition.legacy.effective) {
			return false
		}
	case generatedDomainGatewayNativeV2:
		if gateway.kind != generatedDomainGatewayNativeV2 || gateway.nativeV2 == nil || gateway.journal == nil ||
			!reflect.DeepEqual(*gateway.nativeV2, transition.nativeV2.effective) ||
			!reflect.DeepEqual(*gateway.journal, transition.nativeV2.journal) {
			return false
		}
	case generatedDomainGatewayCurrentV1:
		if gateway.kind != generatedDomainGatewayCurrentV1 || gateway.currentV1 == nil ||
			!reflect.DeepEqual(*gateway.currentV1, transition.currentV1.Effective) {
			return false
		}
	default:
		return false
	}
	group, found := generatedDomainColdGroupByDeployment(groups, expected.pendingActivationDeploymentID)
	if !found || !group.selfHead || group.snapshot.observedHead.activationReceiptID == "" {
		return false
	}
	return generatedDomainColdActivationMatches(group.pins, group.snapshot, groups,
		observed.database.Runtime.RouteActivations)
}

// generatedDomainColdClearedPreviousActivationEvidence is used only by
// reattestation of an already sealed PreviousExact manifest. A markerless new
// construction has no expected manifest and cannot enter this path.
func generatedDomainColdClearedPreviousActivationEvidence(observed generatedDomainStartupObservation,
	groups []generatedDomainColdGroup, expected generatedDomainColdManifest,
) (generatedDomainColdPendingActivationEvidence, bool) {
	transition := expected.pendingActivation
	if expected.pendingGatewayDisposition != generatedDomainColdPendingGatewayPreviousExact ||
		expected.pendingActivationDeploymentID == "" ||
		!validGeneratedRouteActivationGatewayTransition(transition) {
		return generatedDomainColdPendingActivationEvidence{}, false
	}
	gateway, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
	if err != nil {
		return generatedDomainColdPendingActivationEvidence{}, false
	}
	switch transition.kind {
	case generatedDomainGatewayNativeV1:
		if gateway.kind != generatedDomainGatewayNativeV1 || gateway.nativeV1 == nil ||
			!reflect.DeepEqual(*gateway.nativeV1, transition.legacy.before) {
			return generatedDomainColdPendingActivationEvidence{}, false
		}
	case generatedDomainGatewayNativeV2:
		if gateway.kind != generatedDomainGatewayNativeV2 || gateway.nativeV2 == nil ||
			gateway.journal == nil || gateway.upgrade == nil ||
			!reflect.DeepEqual(*gateway.nativeV2, transition.nativeV2.before) ||
			!reflect.DeepEqual(*gateway.journal, transition.nativeV2.journal) ||
			!reflect.DeepEqual(gateway.upgrade.State, transition.nativeV2.before) {
			return generatedDomainColdPendingActivationEvidence{}, false
		}
	case generatedDomainGatewayCurrentV1:
		cleared, clearErr := gatewayCurrentNextState(transition.currentV1.Pending,
			func(next *gatewayCurrentRouteState) { next.Pending = nil })
		if clearErr != nil || !reflect.DeepEqual(cleared.Apps, transition.currentV1.Before.Apps) ||
			!sameGatewayCurrentRouteOrigin(cleared, transition.currentV1.Before) ||
			gateway.kind != generatedDomainGatewayCurrentV1 || gateway.currentV1 == nil ||
			!reflect.DeepEqual(*gateway.currentV1, cleared) {
			return generatedDomainColdPendingActivationEvidence{}, false
		}
	default:
		return generatedDomainColdPendingActivationEvidence{}, false
	}
	before, hadBefore, proposed, valid := generatedRouteActivationGatewayRoutes(transition)
	if !valid || transition.request.AppID == "" || transition.request.FromSlot != before.Slot ||
		transition.request.ToSlot != proposed.Slot ||
		!sameRoute(proposed, routeRecord{Slot: transition.request.ToSlot,
			Endpoints: transition.request.Endpoints}) ||
		(!hadBefore && (!sameRoute(before, routeRecord{}) || transition.request.FromSlot != "")) {
		return generatedDomainColdPendingActivationEvidence{}, false
	}
	matched := 0
	for _, group := range groups {
		if group.pins.deploymentID != expected.pendingActivationDeploymentID {
			continue
		}
		matched++
		actual, actualValid := generatedDomainColdGroupRoute(group)
		if !actualValid || !sameRoute(actual, proposed) || group.pins.applicationID() != transition.request.AppID ||
			group.kind != generatedDomainColdRisky || !group.bindHead || group.selfHead ||
			group.snapshot.runtime.phase != generatedruntimestate.PhaseSwitchingRoute ||
			group.snapshot.observedHead.deploymentID != group.snapshot.headPins.previousDeploymentID ||
			group.snapshot.observedHead.releaseID != group.snapshot.headPins.previousReleaseID ||
			group.snapshot.observedHead.slot != group.snapshot.headPins.previousSlot ||
			group.snapshot.observedHead.slot != string(before.Slot) {
			return generatedDomainColdPendingActivationEvidence{}, false
		}
		if !hadBefore && (group.snapshot.observedHead.deploymentID != "" ||
			group.snapshot.observedHead.releaseID != "" || group.snapshot.observedHead.slot != "" ||
			group.snapshot.observedHead.generation != 0 ||
			group.snapshot.observedHead.activationReceiptID != "") {
			return generatedDomainColdPendingActivationEvidence{}, false
		}
	}
	if matched != 1 {
		return generatedDomainColdPendingActivationEvidence{}, false
	}
	for _, activation := range observed.database.Runtime.RouteActivations {
		if activation.AppID == transition.request.AppID &&
			activation.SuccessorDeploymentID == expected.pendingActivationDeploymentID {
			return generatedDomainColdPendingActivationEvidence{}, false
		}
	}
	for _, values := range [][]string{generatedDomainColdSuccessorIDs043(observed.database),
		generatedDomainColdSuccessorIDs044(observed.database), generatedDomainColdSuccessorIDs045(observed.database)} {
		for _, deploymentID := range values {
			if deploymentID == expected.pendingActivationDeploymentID {
				return generatedDomainColdPendingActivationEvidence{}, false
			}
		}
	}
	serving, err := generatedDomainColdPendingGatewayProjection(gateway, *transition, false)
	if err != nil {
		return generatedDomainColdPendingActivationEvidence{}, false
	}
	return generatedDomainColdPendingActivationEvidence{
		transition:          cloneGeneratedRouteActivationGatewayTransition(transition),
		successorDeployment: expected.pendingActivationDeploymentID,
		beforeActivation:    true,
		protectedGateway:    gateway,
		routeGateway:        gateway,
		servingGateway:      serving,
		disposition:         generatedDomainColdPendingGatewayPreviousExact,
	}, true
}

func generatedDomainColdOwnerManifest(value generatedDomainColdManifest) generatedDomainOwnerManifest {
	heads := 0
	for _, group := range value.groups {
		if group.head != nil {
			heads++
		}
	}
	return generatedDomainOwnerManifest{capacityGroups: len(value.groups), headGroups: heads}
}

func generatedDomainColdGroupSpec(authority *GeneratedCandidateAuthority,
	group generatedDomainColdGroup,
) candidateGroupSpec {
	components := make([]string, len(group.snapshot.components))
	for index := range group.snapshot.components {
		components[index] = group.snapshot.components[index].plan.Name
	}
	return candidateGroupSpec{appID: group.pins.applicationID(), deploymentID: group.pins.deploymentID,
		pins: group.snapshot.pins, components: components,
		limits: generatedcapacity.Limits{MemoryBytes: uint64(authority.options.MemoryBytes),
			DiskBytes: authority.options.ReplacementDiskBytes}}
}

func readGeneratedDomainColdGroupSet(ctx context.Context, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation,
) ([]generatedDomainColdGroup, error) {
	if authority == nil || authority.stores == nil || authority.stores.Database() == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	mainByID := make(map[string]deployments.GeneratedDomainStartupDeployment,
		len(observed.database.GeneratedDeployments))
	for _, value := range observed.database.GeneratedDeployments {
		if _, duplicate := mainByID[value.ID]; duplicate {
			return nil, errGeneratedDomainConstructionRefused
		}
		mainByID[value.ID] = value
	}
	runtimeByID := make(map[string]generatedruntimestate.GeneratedDomainStartupDeployment,
		len(observed.database.Runtime.Deployments))
	for _, value := range observed.database.Runtime.Deployments {
		if _, duplicate := runtimeByID[value.DeploymentID]; duplicate {
			return nil, errGeneratedDomainConstructionRefused
		}
		runtimeByID[value.DeploymentID] = value
	}
	if len(mainByID) == 0 || len(mainByID) != len(runtimeByID) {
		return nil, errGeneratedDomainConstructionRefused
	}
	ids := make([]string, 0, len(mainByID))
	for id := range mainByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	groups := make([]generatedDomainColdGroup, 0, len(ids))
	for _, id := range ids {
		main := mainByID[id]
		runtime, runtimePresent := runtimeByID[id]
		if !runtimePresent || main.AppID != runtime.AppID || main.ReleaseID != runtime.ReleaseID ||
			main.DeploymentPlanRevisionID != runtime.DeploymentPlanRevisionID ||
			main.DeploymentPlanRevisionNumber != runtime.DeploymentPlanRevisionNumber {
			return nil, errGeneratedDomainConstructionRefused
		}
		group, err := readGeneratedDomainColdGroup(ctx, authority, observed, main, runtime)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	for index := range groups {
		kind, bindHead, selfHead, legacyHead, err := classifyGeneratedDomainColdGroup(
			groups[index].pins, groups[index].snapshot, groups, observed.database)
		if err != nil {
			return nil, err
		}
		groups[index].kind, groups[index].bindHead = kind, bindHead
		groups[index].selfHead, groups[index].legacyHead = selfHead, legacyHead
	}
	return groups, nil
}

func readGeneratedDomainColdGroups(ctx context.Context, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation,
) ([]generatedDomainColdGroup, error) {
	groups, pending, err := readGeneratedDomainColdGroupsForReattestation(ctx, authority, observed, nil)
	_ = pending
	return groups, err
}

func readGeneratedDomainColdGroupsForReattestation(ctx context.Context,
	authority *GeneratedCandidateAuthority, observed generatedDomainStartupObservation,
	expected *generatedDomainColdManifest,
) ([]generatedDomainColdGroup, *generatedDomainColdPendingActivationEvidence, error) {
	groups, err := readGeneratedDomainColdGroupSet(ctx, authority, observed)
	if err != nil {
		return nil, nil, err
	}
	pendingActivation, pendingFound, err := generatedDomainColdPendingActivation(
		observed.database, observed.protected, groups)
	if err != nil {
		return nil, nil, err
	}
	var pending *generatedDomainColdPendingActivationEvidence
	if pendingFound {
		pending = &pendingActivation
	} else if expected != nil &&
		expected.pendingGatewayDisposition == generatedDomainColdPendingGatewayPreviousExact {
		cleared, found := generatedDomainColdClearedPreviousActivationEvidence(observed, groups, *expected)
		if found {
			pending = &cleared
		}
	}
	if err := validateGeneratedDomainColdLifecycleRetentionWithPending(observed.database, groups, pending); err != nil {
		return nil, nil, err
	}
	if err := validateGeneratedDomainColdGatewayRoutesWithPending(observed, groups, pending); err != nil {
		return nil, nil, err
	}
	return groups, pending, nil
}

// validateGeneratedDomainColdGatewayRoutes closes the durable head to
// protected route-state correlation. The SQL head selects the exact current
// deployment while the protected gateway state selects the route actually
// served for that application. Current V2 also retains the exact runtime head
// projection, so its deployment, release, slot and generation are checked as
// one record. Route and activation receipt digests remain authenticated by
// their create-only codecs and SQL rows; this check does not recreate either
// digest from an incomplete startup projection.
func validateGeneratedDomainColdGatewayRoutes(observed generatedDomainStartupObservation,
	groups []generatedDomainColdGroup,
) error {
	return validateGeneratedDomainColdGatewayRoutesWithPending(observed, groups, nil)
}

func validateGeneratedDomainColdGatewayRoutesWithPending(observed generatedDomainStartupObservation,
	groups []generatedDomainColdGroup, pending *generatedDomainColdPendingActivationEvidence,
) error {
	gateway, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
	if pending != nil {
		gateway, err = pending.routeGateway, nil
	} else {
		pending, found, pendingErr := generatedDomainColdPendingActivation(observed.database, observed.protected, groups)
		if pendingErr != nil {
			return pendingErr
		}
		if found {
			gateway, err = pending.routeGateway, nil
		}
	}
	if err != nil {
		return err
	}
	type expectedRoute struct {
		route   routeRecord
		runtime *gatewayCurrentRuntimePinV2
	}
	expected := make(map[string]expectedRoute)
	add := func(appID string, route routeRecord, runtime *gatewayCurrentRuntimePinV2) bool {
		if _, duplicate := expected[appID]; duplicate {
			return false
		}
		entry := expectedRoute{route: route}
		entry.route.Endpoints = append([]generatedroute.RouteEndpoint(nil), route.Endpoints...)
		if runtime != nil {
			copy := *runtime
			entry.runtime = &copy
		}
		expected[appID] = entry
		return true
	}
	switch gateway.kind {
	case generatedDomainGatewayNativeV1:
		if gateway.nativeV1 == nil {
			return errGeneratedDomainConstructionRefused
		}
		for appID, route := range gateway.nativeV1.Active {
			if !add(appID, route, nil) {
				return errGeneratedDomainConstructionRefused
			}
		}
	case generatedDomainGatewayNativeV2:
		if gateway.nativeV2 == nil {
			return errGeneratedDomainConstructionRefused
		}
		for appID, app := range gateway.nativeV2.Apps {
			if !add(appID, app.Route, nil) {
				return errGeneratedDomainConstructionRefused
			}
		}
	case generatedDomainGatewayCurrentV1:
		if gateway.currentV1 == nil {
			return errGeneratedDomainConstructionRefused
		}
		for appID, app := range gateway.currentV1.Apps {
			if !add(appID, app.Route, nil) {
				return errGeneratedDomainConstructionRefused
			}
		}
	case generatedDomainGatewayCurrentV2:
		if gateway.currentV2 == nil {
			return errGeneratedDomainConstructionRefused
		}
		for appID, app := range gateway.currentV2.Apps {
			runtime := app.Runtime
			if !add(appID, app.Route, &runtime) {
				return errGeneratedDomainConstructionRefused
			}
		}
	default:
		return errGeneratedDomainConstructionRefused
	}
	seen := make(map[string]struct{}, len(expected))
	for _, group := range groups {
		if !group.selfHead {
			continue
		}
		appID := group.pins.applicationID()
		want, ok := expected[appID]
		if !ok {
			return errGeneratedDomainConstructionRefused
		}
		if _, duplicate := seen[appID]; duplicate {
			return errGeneratedDomainConstructionRefused
		}
		actual, valid := generatedDomainColdGroupRoute(group)
		if !valid || !sameRoute(actual, want.route) {
			return errGeneratedDomainConstructionRefused
		}
		if want.runtime != nil && (want.runtime.DeploymentID != group.pins.deploymentID ||
			want.runtime.ReleaseID != group.pins.releaseID ||
			want.runtime.Slot != generatedidentity.Slot(group.snapshot.pins.candidateSlot) ||
			want.runtime.ActiveGeneration != group.snapshot.observedHead.generation) {
			return errGeneratedDomainConstructionRefused
		}
		seen[appID] = struct{}{}
	}
	if len(seen) != len(expected) {
		return errGeneratedDomainConstructionRefused
	}
	return nil
}

func generatedDomainColdGroupRoute(group generatedDomainColdGroup) (routeRecord, bool) {
	if len(group.snapshot.components) == 0 {
		return routeRecord{}, false
	}
	route := routeRecord{Slot: generatedidentity.Slot(group.snapshot.pins.candidateSlot),
		Endpoints: make([]generatedroute.RouteEndpoint, 0, len(group.snapshot.components))}
	for _, component := range group.snapshot.components {
		policy, ok := group.policies[component.plan.Name]
		if !ok {
			return routeRecord{}, false
		}
		route.Endpoints = append(route.Endpoints, generatedroute.RouteEndpoint{
			Component: component.plan.Name, Role: component.plan.Role,
			ContainerID: component.containerID, NetworkName: policy.identity.NetworkName,
			NetworkAlias: policy.identity.NetworkAlias,
			InternalPort: component.plan.InternalPort,
		})
	}
	sort.Slice(route.Endpoints, func(i, j int) bool {
		return route.Endpoints[i].Component < route.Endpoints[j].Component
	})
	return route, validateRoute(route) == nil
}

// resolveGeneratedDomainColdGatewayExpectation extends the descriptive stable
// resolver only for the original native V1 state whose application routes are
// explained by exact cold SQL heads. Every upgrade, current or rebind artifact
// excludes this fallback; those forms must continue through their canonical
// typed resolver.
func resolveGeneratedDomainColdGatewayExpectation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot,
) (generatedDomainGatewayExpectation, error) {
	value, err := resolveGeneratedDomainGatewayExpectation(database, protected)
	if err != nil || value.kind != generatedDomainGatewayNone {
		return value, err
	}
	if protected.nativeV1 == nil || !validRouteState(*protected.nativeV1) || protected.nativeV1.Pending != nil ||
		protected.upgrade.store != nil || protected.upgrade.committed || len(protected.upgrade.generations) != 0 ||
		!reflect.DeepEqual(protected.upgrade.state, gatewayV2RouteState{}) ||
		!reflect.DeepEqual(protected.upgrade.journal, gatewayMigrationJournal{}) ||
		len(protected.currentV1) != 0 || len(protected.currentV2.files) != 0 ||
		protected.hasRebind || len(protected.rebindArtifacts) != 0 || len(protected.terminals) != 0 ||
		!reflect.DeepEqual(protected.rebind, gatewayRebindProtectedIntentHistory{}) {
		return generatedDomainGatewayExpectation{}, nil
	}
	state := cloneRouteState(*protected.nativeV1)
	digest, err := canonicalDigest(state)
	if err != nil {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
	}
	value = generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
		stateDigest: digest, nativeV1: &state}
	value.names = generatedDomainGatewayExpectedNames(value)
	value.ids, err = generatedDomainGatewayExpectedIDs(value)
	if err != nil {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainStartupGatewayInvalid
	}
	return value, nil
}

func generatedDomainColdPendingActivation(database deployments.GeneratedDomainStartupSnapshot,
	protected generatedDomainProtectedStartupSnapshot, groups []generatedDomainColdGroup,
) (generatedDomainColdPendingActivationEvidence, bool, error) {
	var result generatedDomainColdPendingActivationEvidence
	var candidates []generatedDomainGatewayExpectation
	var allCandidates []generatedDomainGatewayExpectation
	selectedIndex := -1
	if protected.nativeV1 != nil && protected.nativeV1.Pending != nil {
		if !validRouteState(*protected.nativeV1) || protected.upgrade.store != nil ||
			len(protected.upgrade.generations) != 0 || len(protected.currentV1) != 0 ||
			len(protected.currentV2.files) != 0 || protected.hasRebind || len(protected.rebindArtifacts) != 0 {
			return result, false, errGeneratedDomainConstructionRefused
		}
		state := cloneRouteState(*protected.nativeV1)
		digest, err := canonicalDigest(state)
		if err != nil {
			return result, false, errGeneratedDomainConstructionRefused
		}
		candidates = append(candidates, generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
			stateDigest: digest, nativeV1: &state})
	} else {
		if candidate, ok := generatedDomainCommittedUpgradeRoutePendingGatewayCandidate(database, protected); ok {
			allCandidates = append(allCandidates, candidate)
		}
		allCandidates = append(allCandidates, generatedDomainCurrentGatewayCandidates(database, protected, false)...)
		for index, candidate := range allCandidates {
			pending := candidate.kind == generatedDomainGatewayNativeV2 && candidate.nativeV2 != nil &&
				candidate.nativeV2.Pending != nil && candidate.nativeV2.Pending.Kind == gatewayV2PendingRouteSwitch ||
				candidate.kind == generatedDomainGatewayCurrentV1 && candidate.currentV1 != nil &&
					candidate.currentV1.Pending != nil && candidate.currentV1.Pending.Kind == gatewayV2PendingRouteSwitch
			if pending {
				candidates = append(candidates, candidate)
				selectedIndex = index
			}
		}
	}
	if len(candidates) == 0 {
		return result, false, nil
	}
	if len(candidates) != 1 {
		return result, false, errGeneratedDomainConstructionRefused
	}
	protectedGateway := candidates[0]
	var transition generatedRouteActivationGatewayTransition
	switch protectedGateway.kind {
	case generatedDomainGatewayNativeV1:
		state := cloneRouteState(*protectedGateway.nativeV1)
		pending := state.Pending
		if pending == nil {
			return result, false, errGeneratedDomainConstructionRefused
		}
		before, effective := cloneRouteState(state), cloneRouteState(state)
		before.Pending, effective.Pending = nil, nil
		if pending.Previous == nil {
			delete(before.Active, pending.AppID)
		} else {
			before.Active[pending.AppID] = *pending.Previous
		}
		effective.Active[pending.AppID] = pending.Proposed
		transition = generatedRouteActivationGatewayTransition{kind: generatedDomainGatewayNativeV1,
			legacy: &generatedRouteActivationLegacyTransition{before: before, pending: state, effective: effective}}
		protectedGateway.names = generatedDomainGatewayExpectedNames(protectedGateway)
		ids, err := generatedDomainGatewayExpectedIDs(protectedGateway)
		if err != nil {
			return result, false, errGeneratedDomainConstructionRefused
		}
		protectedGateway.ids = ids
	case generatedDomainGatewayNativeV2:
		state := cloneGatewayV2RouteState(*protectedGateway.nativeV2)
		pending := state.Pending
		if pending == nil || protectedGateway.journal == nil {
			return result, false, errGeneratedDomainConstructionRefused
		}
		before, effective := cloneGatewayV2RouteState(state), cloneGatewayV2RouteState(state)
		before.Pending, effective.Pending = nil, nil
		if pending.Previous == nil {
			delete(before.Apps, pending.AppID)
		} else {
			before.Apps[pending.AppID] = cloneGatewayV2AppRoute(*pending.Previous)
		}
		effective.Apps[pending.AppID] = cloneGatewayV2AppRoute(pending.Proposed)
		transition = generatedRouteActivationGatewayTransition{kind: generatedDomainGatewayNativeV2,
			nativeV2: &generatedRouteActivationNativeV2Transition{before: before, pending: state,
				effective: effective, journal: *protectedGateway.journal, operationID: state.OperationID}}
	case generatedDomainGatewayCurrentV1:
		value, err := gatewayCurrentTransitionFromPending(*protectedGateway.currentV1)
		if err != nil || value.Pending.Pending == nil || value.Pending.Pending.Kind != gatewayV2PendingRouteSwitch {
			return result, false, errGeneratedDomainConstructionRefused
		}
		transition = generatedRouteActivationGatewayTransition{kind: generatedDomainGatewayCurrentV1,
			currentV1: &value}
	default:
		return result, false, errGeneratedDomainConstructionRefused
	}
	// A pending V1/upgrade record may coexist with only its one exact stable
	// Current V2 projection from immediately before the transition.  Prove the
	// projection against the reconstructed before-state; every other protected
	// candidate remains ambiguous and refuses construction.
	if selectedIndex >= 0 && len(allCandidates) > 1 {
		if len(allCandidates) != 2 {
			return result, false, errGeneratedDomainConstructionRefused
		}
		historical := candidates[0]
		switch transition.kind {
		case generatedDomainGatewayNativeV2:
			state := cloneGatewayV2RouteState(transition.nativeV2.before)
			selection := *historical.upgrade
			selection.State = state
			historical.nativeV2, historical.upgrade = &state, &selection
		case generatedDomainGatewayCurrentV1:
			state := cloneGatewayCurrentRouteState(transition.currentV1.Before)
			historical.currentV1 = &state
		default:
			return result, false, errGeneratedDomainConstructionRefused
		}
		otherIndex := 1 - selectedIndex
		if otherIndex < 0 || otherIndex >= len(allCandidates) ||
			allCandidates[otherIndex].kind != generatedDomainGatewayCurrentV2 {
			return result, false, errGeneratedDomainConstructionRefused
		}
		collapsed, ok := generatedDomainCollapseCurrentV2Sources(
			[]generatedDomainGatewayExpectation{historical, allCandidates[otherIndex]})
		if !ok || len(collapsed) != 1 || collapsed[0].kind != generatedDomainGatewayCurrentV2 {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	appID := ""
	requestBefore := routeRecord{}
	requestProposed := routeRecord{}
	switch transition.kind {
	case generatedDomainGatewayNativeV1:
		pending := transition.legacy.pending.Pending
		appID, requestProposed = pending.AppID, pending.Proposed
		if pending.Previous != nil {
			requestBefore = *pending.Previous
		}
	case generatedDomainGatewayNativeV2:
		pending := transition.nativeV2.pending.Pending
		appID, requestProposed = pending.AppID, pending.Proposed.Route
		if pending.Previous != nil {
			requestBefore = pending.Previous.Route
		}
	case generatedDomainGatewayCurrentV1:
		pending := transition.currentV1.Pending.Pending
		appID, requestProposed = pending.AppID, pending.Proposed.Route
		if pending.Previous != nil {
			requestBefore = pending.Previous.Route
		}
	}
	// Reconstruct the canonical request from the durable Pending record before
	// asking the gateway helper to validate the complete transition.  The
	// helper deliberately validates request as part of the transition, so using
	// it first would reject every restart reconstruction for a zero request.
	transition.request = generatedroute.RouteSwitchRequest{AppID: appID, FromSlot: requestBefore.Slot,
		ToSlot: requestProposed.Slot, Endpoints: append([]generatedroute.RouteEndpoint(nil), requestProposed.Endpoints...)}
	before, hadBefore, proposed, valid := generatedRouteActivationGatewayRoutes(&transition)
	if !valid || (!hadBefore && (transition.request.FromSlot != "" || !sameRoute(before, routeRecord{}))) {
		return result, false, errGeneratedDomainConstructionRefused
	}
	var successor *generatedDomainColdGroup
	beforeActivation := false
	for index := range groups {
		group := &groups[index]
		if group.pins.applicationID() != appID || group.kind != generatedDomainColdRisky || !group.bindHead {
			continue
		}
		for _, component := range group.snapshot.components {
			if component.state != generatedruntimestate.ComponentHealthy &&
				component.state != generatedruntimestate.ComponentActive {
				return result, false, errGeneratedDomainConstructionRefused
			}
		}
		actual, actualValid := generatedDomainColdGroupRoute(*group)
		if !actualValid {
			return result, false, errGeneratedDomainConstructionRefused
		}
		pre := group.snapshot.runtime.phase == generatedruntimestate.PhaseSwitchingRoute && !group.selfHead &&
			group.snapshot.observedHead.deploymentID == group.snapshot.headPins.previousDeploymentID
		post := group.snapshot.runtime.phase == generatedruntimestate.PhaseDraining && group.selfHead &&
			generatedDomainColdActivationMatches(group.pins, group.snapshot, groups,
				database.Runtime.RouteActivations)
		if sameRoute(actual, proposed) && (pre || post) {
			if successor != nil {
				return result, false, errGeneratedDomainConstructionRefused
			}
			successor = group
			beforeActivation = pre
		}
	}
	if successor == nil {
		return result, false, errGeneratedDomainConstructionRefused
	}
	if beforeActivation && !hadBefore && (successor.snapshot.observedHead.deploymentID != "" ||
		successor.snapshot.observedHead.releaseID != "" || successor.snapshot.observedHead.slot != "" ||
		successor.snapshot.observedHead.generation != 0 || successor.snapshot.observedHead.activationReceiptID != "") {
		return result, false, errGeneratedDomainConstructionRefused
	}
	if beforeActivation && successor.snapshot.observedHead.slot != string(before.Slot) {
		return result, false, errGeneratedDomainConstructionRefused
	}
	if !beforeActivation && successor.snapshot.observedHead.slot != string(proposed.Slot) {
		return result, false, errGeneratedDomainConstructionRefused
	}
	if beforeActivation {
		for _, value := range database.Runtime.RouteActivations {
			if value.AppID == appID && value.SuccessorDeploymentID == successor.pins.deploymentID {
				return result, false, errGeneratedDomainConstructionRefused
			}
		}
		for _, values := range [][]string{
			generatedDomainColdSuccessorIDs043(database), generatedDomainColdSuccessorIDs044(database),
			generatedDomainColdSuccessorIDs045(database),
		} {
			for _, deploymentID := range values {
				if deploymentID == successor.pins.deploymentID {
					return result, false, errGeneratedDomainConstructionRefused
				}
			}
		}
	} else {
		for _, deploymentID := range generatedDomainColdSuccessorIDs045(database) {
			if deploymentID == successor.pins.deploymentID {
				return result, false, errGeneratedDomainConstructionRefused
			}
		}
	}
	serving, err := generatedDomainColdPendingGatewayProjection(protectedGateway, transition, false)
	if err != nil {
		return result, false, errGeneratedDomainConstructionRefused
	}
	routeGateway := serving
	if beforeActivation {
		routeGateway, err = generatedDomainColdPendingGatewayProjection(protectedGateway, transition, true)
		if err != nil {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	result = generatedDomainColdPendingActivationEvidence{transition: &transition,
		successorDeployment: successor.pins.deploymentID, beforeActivation: beforeActivation,
		protectedGateway: protectedGateway, routeGateway: routeGateway, servingGateway: serving}
	return result, true, nil
}

// generatedDomainColdPendingGatewayProjection creates a canonical, standalone
// physical expectation for one protected Pending projection. The raw
// protectedGateway remains unmodified: it is durable marker evidence, not a
// serving-state expectation. Keeping all companion projections in step with
// the primary state prevents a native V2/current retained proof from observing
// the previous Pending-bearing state through a shallow pointer alias.
func generatedDomainColdPendingGatewayProjection(protected generatedDomainGatewayExpectation,
	transition generatedRouteActivationGatewayTransition, before bool,
) (generatedDomainGatewayExpectation, error) {
	if protected.kind != transition.kind {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
	}
	result := protected
	switch transition.kind {
	case generatedDomainGatewayNativeV1:
		if transition.legacy == nil || protected.nativeV1 == nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
		}
		state := cloneRouteState(transition.legacy.effective)
		if before {
			state = cloneRouteState(transition.legacy.before)
		}
		if !validRouteState(state) || state.Pending != nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
		}
		result.nativeV1, result.nativeV2, result.journal, result.upgrade = &state, nil, nil, nil
		result.currentV1, result.currentV2, result.terminal = nil, nil, nil
		result.retainedV1, result.retainedV2 = nil, nil
		digest, err := canonicalDigest(state)
		if err != nil {
			return generatedDomainGatewayExpectation{}, err
		}
		result.stateDigest = digest
	case generatedDomainGatewayNativeV2:
		if transition.nativeV2 == nil || protected.nativeV1 == nil || protected.nativeV2 == nil ||
			protected.journal == nil || protected.upgrade == nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
		}
		state := cloneGatewayV2RouteState(transition.nativeV2.effective)
		if before {
			state = cloneGatewayV2RouteState(transition.nativeV2.before)
		}
		if !validGatewayTopologyInputs(*protected.nativeV1, state, *protected.journal) || state.Pending != nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
		}
		upgrade := *protected.upgrade
		upgrade.State = cloneGatewayV2RouteState(state)
		result.nativeV2, result.upgrade = &state, &upgrade
		result.currentV1, result.currentV2, result.terminal = nil, nil, nil
		result.retainedV1, result.retainedV2 = nil, nil
		digest, err := canonicalDigest(state)
		if err != nil {
			return generatedDomainGatewayExpectation{}, err
		}
		result.stateDigest = digest
	case generatedDomainGatewayCurrentV1:
		if transition.currentV1 == nil || protected.currentV1 == nil || protected.terminal == nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
		}
		state := cloneGatewayCurrentRouteState(transition.currentV1.Effective)
		if before {
			state = cloneGatewayCurrentRouteState(transition.currentV1.Before)
		}
		if !validGatewayCurrentRouteState(state) || state.Pending != nil {
			return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
		}
		result.currentV1, result.currentV2 = &state, nil
		if protected.retainedV1 != nil {
			retained := cloneGeneratedDomainRetainedV1Projection(*protected.retainedV1)
			result.retainedV1 = &retained
		}
		if protected.retainedV2 != nil {
			retained := cloneGeneratedDomainRetainedV2Projection(*protected.retainedV2)
			retained.current = cloneGatewayCurrentRouteState(state)
			result.retainedV2 = &retained
		}
		result.stateDigest = state.Digest
	default:
		return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
	}
	if result.stateDigest == "" {
		return generatedDomainGatewayExpectation{}, errGeneratedDomainConstructionRefused
	}
	result.names = generatedDomainGatewayExpectedNames(result)
	ids, err := generatedDomainGatewayExpectedIDs(result)
	if err != nil {
		return generatedDomainGatewayExpectation{}, err
	}
	result.ids = ids
	return result, nil
}

// generatedDomainColdPendingGatewayIdentity identifies the one immutable
// gateway container and restart file that both canonical Pending projections
// must share before cold construction reads configuration bytes. Application
// networks deliberately are not compared here: the Before and Effective route
// states can legitimately have different app-network memberships.
func generatedDomainColdPendingGatewayIdentity(snapshot generatedDomainDockerStartupSnapshot,
	expected generatedDomainGatewayExpectation,
) (string, string, bool) {
	containers := expected.names["container"]
	if len(containers) != 1 {
		return "", "", false
	}
	containerName := ""
	for name := range containers {
		containerName = name
	}
	container, found := generatedDomainObjectByName(snapshot, "container", containerName)
	if !found || !generatedDomainCanonicalDockerID(container.id, false) || normalizeID(container.id) != container.id {
		return "", "", false
	}
	if ids := expected.ids["container"]; len(ids) != 0 {
		if len(ids) != 1 {
			return "", "", false
		}
		if _, matches := ids[container.id]; !matches {
			return "", "", false
		}
	}
	filename := ""
	switch expected.kind {
	case generatedDomainGatewayNativeV1:
		filename = gatewayV2ActiveConfigFile
	case generatedDomainGatewayNativeV2:
		if expected.nativeV2 == nil {
			return "", "", false
		}
		filename = expected.nativeV2.Identity.ActiveConfigFilename
	case generatedDomainGatewayCurrentV1:
		if expected.currentV1 == nil || expected.currentV1.Identity.Rebind == nil {
			return "", "", false
		}
		filename = expected.currentV1.Identity.Rebind.ActiveConfigFilename
	case generatedDomainGatewayCurrentV2:
		if expected.currentV2 == nil {
			return "", "", false
		}
		if expected.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade {
			if expected.nativeV2 == nil {
				return "", "", false
			}
			filename = expected.nativeV2.Identity.ActiveConfigFilename
		} else if expected.currentV2.Identity.Rebind != nil {
			filename = expected.currentV2.Identity.Rebind.ActiveConfigFilename
		}
	}
	if !validName(filename, 255) {
		return "", "", false
	}
	return container.id, filename, true
}

func sameGeneratedDomainColdPendingGatewayIdentity(left, right generatedDomainGatewayExpectation) bool {
	if left.kind != right.kind {
		return false
	}
	switch left.kind {
	case generatedDomainGatewayNativeV1:
		return left.nativeV1 != nil && right.nativeV1 != nil
	case generatedDomainGatewayNativeV2:
		return left.nativeV1 != nil && right.nativeV1 != nil && left.nativeV2 != nil && right.nativeV2 != nil &&
			left.journal != nil && right.journal != nil && left.upgrade != nil && right.upgrade != nil &&
			reflect.DeepEqual(left.nativeV1, right.nativeV1) &&
			reflect.DeepEqual(left.nativeV2.Identity, right.nativeV2.Identity) &&
			reflect.DeepEqual(left.journal.Resources, right.journal.Resources) &&
			left.upgrade.Generation == right.upgrade.Generation && left.upgrade.operationID == right.upgrade.operationID &&
			reflect.DeepEqual(left.upgrade.Journal, right.upgrade.Journal)
	case generatedDomainGatewayCurrentV1:
		return left.currentV1 != nil && right.currentV1 != nil && left.terminal != nil && right.terminal != nil &&
			reflect.DeepEqual(left.currentV1.Lineage, right.currentV1.Lineage) &&
			reflect.DeepEqual(left.currentV1.Identity, right.currentV1.Identity) &&
			reflect.DeepEqual(left.currentV1.Profile, right.currentV1.Profile) &&
			reflect.DeepEqual(left.currentV1.Network, right.currentV1.Network) &&
			reflect.DeepEqual(left.terminal.Resources, right.terminal.Resources)
	default:
		return false
	}
}

func generatedDomainColdPendingGatewayConfig(ctx context.Context, reader *generatedDomainStartupReader,
	deadline time.Time, snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
	containerID string,
) ([]byte, error) {
	if reader == nil || !generatedDomainCanonicalDockerID(containerID, false) {
		return nil, errGeneratedDomainConstructionRefused
	}
	switch expected.kind {
	case generatedDomainGatewayNativeV1:
		if expected.nativeV1 == nil {
			return nil, errGeneratedDomainConstructionRefused
		}
		containerObject, found := generatedDomainObjectByName(snapshot, "container", caddyContainerName)
		if !found || containerObject.id != containerID {
			return nil, errGeneratedDomainConstructionRefused
		}
		networkObject, found := generatedDomainObjectByName(snapshot, "network", caddyNetworkName)
		if !found {
			return nil, errGeneratedDomainConstructionRefused
		}
		container, _, err := reader.readDetailedContainer(ctx, deadline, containerID)
		if err != nil || container.ID != containerID {
			return nil, errors.Join(errGeneratedDomainConstructionRefused, err)
		}
		network, _, err := reader.readDetailedNetwork(ctx, deadline, networkObject.id)
		listenIP, valid := caddyIngressAddress(network, container.ID)
		if err != nil || !valid {
			return nil, errors.Join(errGeneratedDomainConstructionRefused, err)
		}
		config, err := buildCaddyConfig(expected.nativeV1.Active,
			net.JoinHostPort(listenIP, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)))
		if err != nil {
			return nil, errors.Join(errGeneratedDomainConstructionRefused, err)
		}
		return config, nil
	case generatedDomainGatewayNativeV2:
		if expected.nativeV2 == nil {
			return nil, errGeneratedDomainConstructionRefused
		}
		config, err := expectedGatewayV2FinalConfig(*expected.nativeV2)
		if err != nil {
			return nil, errors.Join(errGeneratedDomainConstructionRefused, err)
		}
		return config, nil
	case generatedDomainGatewayCurrentV1:
		if expected.currentV1 == nil {
			return nil, errGeneratedDomainConstructionRefused
		}
		config, err := gatewayCurrentPhysicalConfigBytes(*expected.currentV1)
		if err != nil {
			return nil, errors.Join(errGeneratedDomainConstructionRefused, err)
		}
		return config, nil
	default:
		return nil, errGeneratedDomainConstructionRefused
	}
}

func classifyGeneratedDomainColdPendingGateway(ctx context.Context, reader *generatedDomainStartupReader,
	deadline time.Time, snapshot generatedDomainDockerStartupSnapshot,
	pending *generatedDomainColdPendingActivationEvidence,
) (generatedDomainGatewayExpectation, *generatedDomainGatewayConfigObservation,
	generatedDomainColdPendingGatewayDisposition, error) {
	if pending == nil || pending.transition == nil || !pending.beforeActivation ||
		!sameGeneratedDomainColdPendingGatewayIdentity(pending.routeGateway, pending.servingGateway) {
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown,
			errGeneratedDomainConstructionRefused
	}
	containerID, filename, valid := generatedDomainColdPendingGatewayIdentity(snapshot, pending.routeGateway)
	if !valid {
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown,
			errGeneratedDomainConstructionRefused
	}
	otherID, otherFilename, valid := generatedDomainColdPendingGatewayIdentity(snapshot, pending.servingGateway)
	if !valid || otherID != containerID || otherFilename != filename {
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown,
			errGeneratedDomainConstructionRefused
	}
	observation, err := reader.readGatewayConfigObservation(ctx, deadline, containerID, filename)
	if err != nil {
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown, err
	}
	before, err := generatedDomainColdPendingGatewayConfig(ctx, reader, deadline, snapshot, pending.routeGateway, containerID)
	if err != nil {
		observation.clear()
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown, err
	}
	defer clear(before)
	serving, err := generatedDomainColdPendingGatewayConfig(ctx, reader, deadline, snapshot, pending.servingGateway, containerID)
	if err != nil {
		observation.clear()
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown, err
	}
	defer clear(serving)
	disposition, matches := classifyGeneratedDomainColdPendingGatewayConfigs(before, serving,
		observation.restart, observation.live)
	if !matches {
		observation.clear()
		return generatedDomainGatewayExpectation{}, nil, generatedDomainColdPendingGatewayUnknown,
			errGeneratedDomainConstructionRefused
	}
	if disposition == generatedDomainColdPendingGatewayPreviousExact {
		return pending.routeGateway, observation, generatedDomainColdPendingGatewayPreviousExact, nil
	}
	return pending.servingGateway, observation, generatedDomainColdPendingGatewayProposedExact, nil
}

// classifyGeneratedDomainColdPendingGatewayConfigs is deliberately pure so the
// physical pairing rule stays closed: both restart and live bytes must match
// exactly one canonical projection. A mixed, absent, or indistinguishable pair
// has no typed physical disposition.
func classifyGeneratedDomainColdPendingGatewayConfigs(before, serving, restart, live []byte) (
	generatedDomainColdPendingGatewayDisposition, bool,
) {
	beforeMatches := sameCaddyConfig(before, restart) && sameCaddyConfig(before, live)
	servingMatches := sameCaddyConfig(serving, restart) && sameCaddyConfig(serving, live)
	switch {
	case beforeMatches && !servingMatches:
		return generatedDomainColdPendingGatewayPreviousExact, true
	case servingMatches && !beforeMatches:
		return generatedDomainColdPendingGatewayProposedExact, true
	default:
		return generatedDomainColdPendingGatewayUnknown, false
	}
}

func generatedDomainColdSuccessorIDs043(database deployments.GeneratedDomainStartupSnapshot) []string {
	result := make([]string, 0, len(database.Runtime.PredecessorRetirementComponents)+len(database.Runtime.PredecessorRetirementReceipts))
	for _, value := range database.Runtime.PredecessorRetirementComponents {
		result = append(result, value.SuccessorDeploymentID)
	}
	for _, value := range database.Runtime.PredecessorRetirementReceipts {
		result = append(result, value.SuccessorDeploymentID)
	}
	return result
}
func generatedDomainColdSuccessorIDs044(database deployments.GeneratedDomainStartupSnapshot) []string {
	result := make([]string, 0, len(database.Runtime.AccountingHandoffAuthorizations)+len(database.Runtime.AccountingHandoffReceipts))
	for _, value := range database.Runtime.AccountingHandoffAuthorizations {
		result = append(result, value.SuccessorDeploymentID)
	}
	for _, value := range database.Runtime.AccountingHandoffReceipts {
		result = append(result, value.SuccessorDeploymentID)
	}
	return result
}
func generatedDomainColdSuccessorIDs045(database deployments.GeneratedDomainStartupSnapshot) []string {
	result := make([]string, 0, len(database.Runtime.RouteCompletionReceipts))
	for _, value := range database.Runtime.RouteCompletionReceipts {
		result = append(result, value.SuccessorDeploymentID)
	}
	return result
}

func readGeneratedDomainColdGroup(ctx context.Context, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, main deployments.GeneratedDomainStartupDeployment,
	runtime generatedruntimestate.GeneratedDomainStartupDeployment,
) (generatedDomainColdGroup, error) {
	var empty generatedDomainColdGroup
	var attempt int
	if err := authority.stores.Database().QueryRowContext(ctx, `SELECT attempt FROM jobs
		WHERE id=? AND resource_type='application' AND resource_id=?`, main.JobID, main.AppID).Scan(&attempt); err != nil {
		return empty, gateDatabaseError(ctx, err)
	}
	pins := candidateValidationPins{cold: true,
		identity:     candidateValidationIdentity{applicationID: main.AppID, jobID: main.JobID, attempt: attempt},
		deploymentID: main.ID, releaseID: main.ReleaseID,
		configurationRevisionID:      main.ActualConfigurationRevisionID,
		configurationRevisionNumber:  main.ActualConfigurationRevisionNumber,
		deploymentPlanRevisionID:     main.DeploymentPlanRevisionID,
		deploymentPlanRevisionNumber: main.DeploymentPlanRevisionNumber,
		candidateSlot:                runtime.CandidateSlot}
	if err := validateCandidateIdentityPins(pins); err != nil {
		return empty, err
	}
	protected, err := authority.loadCandidateProtectedSnapshotIdentity(ctx, pins)
	if err != nil {
		return empty, err
	}
	tx, err := authority.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	snapshot, err := readCandidateAdmissionSnapshotIdentityTx(ctx, tx, pins, protected)
	if err != nil {
		return empty, err
	}
	pins.findings = append([]Finding(nil), snapshot.persistedFindings...)
	if err := validateGeneratedDomainColdSnapshot(ctx, authority.stores, tx, pins, protected, snapshot,
		observed, main, runtime); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, gateDatabaseError(ctx, err)
	}
	group := generatedDomainColdGroup{pins: pins, protected: protected, snapshot: snapshot,
		policies: make(map[string]candidatePolicy), receipts: make(map[string]candidateColdReceiptSeed),
	}
	for _, component := range snapshot.components {
		if component.receipt != nil {
			physical := candidatePhysicalConfirmedAbsent
			unknown := false
			switch {
			case component.state == generatedruntimestate.ComponentStarting:
				physical, unknown = candidatePhysicalAttemptedUnknown, true
			case component.state == generatedruntimestate.ComponentRunning ||
				component.state == generatedruntimestate.ComponentHealthy:
				physical = candidatePhysicalPresent
			case component.state == generatedruntimestate.ComponentActive ||
				component.state == generatedruntimestate.ComponentDraining:
				physical = candidatePhysicalHandedToRoute
			}
			group.receipts[component.plan.Name] = candidateColdReceiptSeed{id: component.receipt.id,
				physical: physical, unknown: unknown}
		}
		if component.artifact != nil && component.receipt != nil {
			policy, policyErr := buildCandidatePolicy(pins, component.plan, *component.artifact,
				*component.receipt, authority.options)
			if policyErr != nil {
				return empty, policyErr
			}
			group.policies[component.plan.Name] = policy
		}
	}
	return group, nil
}

func validateGeneratedDomainColdSnapshot(ctx context.Context, stores *deployments.GeneratedOwnerStores,
	tx *sql.Tx, pins candidateValidationPins, protected candidateProtectedSnapshot,
	snapshot candidateAdmissionSnapshot, observed generatedDomainStartupObservation,
	main deployments.GeneratedDomainStartupDeployment,
	runtime generatedruntimestate.GeneratedDomainStartupDeployment,
) error {
	if err := validateProtectedCandidateSnapshot(pins, protected); err != nil ||
		snapshot.main.appID != main.AppID || snapshot.main.jobID != main.JobID ||
		snapshot.main.releaseID != main.ReleaseID || snapshot.main.status != main.Status ||
		snapshot.main.strategy != RuntimeGeneratedNode || snapshot.main.initialized != main.ProvenanceInitialized ||
		snapshot.main.configurationRevisionID != main.ActualConfigurationRevisionID ||
		snapshot.main.configurationRevisionNumber != main.ActualConfigurationRevisionNumber ||
		snapshot.main.planRevisionID != main.DeploymentPlanRevisionID ||
		snapshot.main.planRevisionNumber != main.DeploymentPlanRevisionNumber {
		return errGeneratedDomainConstructionRefused
	}
	if snapshot.runtime.appID != runtime.AppID || snapshot.runtime.releaseID != runtime.ReleaseID ||
		snapshot.runtime.planRevisionID != runtime.DeploymentPlanRevisionID ||
		snapshot.runtime.planRevisionNumber != runtime.DeploymentPlanRevisionNumber ||
		snapshot.runtime.candidateSlot != runtime.CandidateSlot ||
		snapshot.runtime.previousDeploymentID != runtime.PreviousActiveDeploymentID ||
		snapshot.runtime.previousSlot != runtime.PreviousActiveSlot || snapshot.runtime.phase != runtime.Phase ||
		snapshot.runtime.migrationState != runtime.MigrationState ||
		!validGeneratedSlotRelationship(snapshot.runtime.candidateSlot,
			snapshot.runtime.previousDeploymentID, snapshot.runtime.previousSlot) {
		return errGeneratedDomainConstructionRefused
	}
	if snapshot.runtime.previousDeploymentID != "" && snapshot.runtime.previousReleaseID == "" {
		return errGeneratedDomainConstructionRefused
	}
	if !snapshot.release.configurationValid ||
		snapshot.release.deploymentPlanRevisionID != pins.deploymentPlanRevisionID ||
		snapshot.release.deploymentPlanRevisionNumber != pins.deploymentPlanRevisionNumber ||
		snapshot.release.workspaceState != "ready" ||
		!validCandidateSource(snapshot.release.sourceProvider, snapshot.release.repositoryID, snapshot.release.resolvedSHA) ||
		(snapshot.release.archiveSHA256 != "" && !lowerHex(snapshot.release.archiveSHA256, 64)) ||
		!lowerHex(snapshot.release.workspaceTreeSHA256, 64) {
		return errGeneratedDomainConstructionRefused
	}
	revision := protected.revision
	if snapshot.plan.canonicalDigest != revision.CanonicalDigest ||
		snapshot.plan.sourceProvider != revision.Plan.Source.Provider ||
		snapshot.plan.sourceRepositoryID != revision.Plan.Source.RepositoryID ||
		snapshot.plan.sourceResolvedDigest != revision.Plan.Source.ResolvedDigest ||
		snapshot.plan.acceptanceStatus != string(revision.State) ||
		snapshot.plan.componentCount != int64(len(protected.components)) ||
		snapshot.configuration.formatVersion != 2 ||
		snapshot.configuration.deploymentPlanRevisionID != pins.deploymentPlanRevisionID ||
		snapshot.configuration.deploymentPlanRevisionNumber != pins.deploymentPlanRevisionNumber ||
		len(snapshot.components) != len(protected.components) {
		return errGeneratedDomainConstructionRefused
	}
	if !generatedDomainColdStagePair(snapshot.main.status, snapshot.runtime.phase,
		snapshot.runtime.migrationState, protected.revision.Plan.Migration != nil) {
		return deploymentexecution.ErrStageInvalid
	}
	startupComponents := generatedDomainColdStartupComponents(observed.database.Runtime.Components, pins.deploymentID)
	if len(startupComponents) != len(snapshot.components) {
		return errGeneratedDomainConstructionRefused
	}
	for index, component := range snapshot.components {
		if component.plan != protected.components[index] ||
			component.slot != pins.candidateSlot ||
			!sameGeneratedDomainColdStartupComponent(component, startupComponents[index]) ||
			validateGeneratedDomainColdComponent(pins, component) != nil {
			return errGeneratedDomainConstructionRefused
		}
	}
	if !generatedDomainColdReceiptsMatch(observed.database.Runtime.CandidateIntents, pins.deploymentID,
		snapshot.components) || !generatedDomainColdArtifactsMatch(observed.database.Artifacts,
		pins.deploymentID, snapshot.components) {
		return errGeneratedDomainConstructionRefused
	}
	if err := validateCandidatePolicyTx(ctx, stores, tx, pins, snapshot, true); err != nil {
		return err
	}
	return nil
}

type generatedDomainColdPredecessorRetirementPrefixEvidence struct {
	activation           generatedruntimestate.GeneratedDomainStartupRouteActivation
	predecessorGroup     generatedDomainColdGroup
	predecessorRoster    []generatedruntimestate.RouteActivationComponent
	componentRetirements map[string]generatedruntimestate.PredecessorRetirementObservation
}

// generatedDomainColdPredecessorRetirementPrefix recognizes only the exact
// post-activation prefix before the aggregate predecessor-retirement receipt.
// Component observations are correlated with the predecessor component state:
// a recorded member is Stopped and every unrecorded member remains Draining.
func generatedDomainColdPredecessorRetirementPrefix(database deployments.GeneratedDomainStartupSnapshot,
	group generatedDomainColdGroup, groups []generatedDomainColdGroup,
) (generatedDomainColdPredecessorRetirementPrefixEvidence, bool, error) {
	var result generatedDomainColdPredecessorRetirementPrefixEvidence
	if group.snapshot.runtime.phase != generatedruntimestate.PhaseDraining ||
		group.snapshot.observedHead.deploymentID != group.pins.deploymentID ||
		group.snapshot.observedHead.activationReceiptID == "" {
		return result, false, nil
	}
	for _, component := range group.snapshot.components {
		if component.state != generatedruntimestate.ComponentActive {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	receiptID := group.snapshot.observedHead.activationReceiptID
	activationCount, relevantActivations := 0, 0
	for _, value := range database.Runtime.RouteActivations {
		if value.ReceiptID == receiptID || value.SuccessorDeploymentID == group.pins.deploymentID {
			relevantActivations++
		}
		if value.ReceiptID == receiptID {
			result.activation, activationCount = value, activationCount+1
		}
	}
	if relevantActivations != 1 || activationCount != 1 ||
		!generatedDomainColdActivationMatches(group.pins, group.snapshot, groups,
			database.Runtime.RouteActivations) {
		return result, false, errGeneratedDomainConstructionRefused
	}
	if result.activation.PredecessorDeploymentID == "" {
		return result, false, nil
	}
	predecessor, found := generatedDomainColdGroupByDeployment(groups,
		result.activation.PredecessorDeploymentID)
	if !found || predecessor.snapshot.runtime.phase != generatedruntimestate.PhaseSucceeded ||
		predecessor.snapshot.main.status != Succeeded {
		return result, false, errGeneratedDomainConstructionRefused
	}
	result.predecessorGroup = predecessor
	result.predecessorRoster, found = generatedDomainColdActivationRoster(predecessor.snapshot)
	if !found || len(result.predecessorRoster) == 0 {
		return result, false, errGeneratedDomainConstructionRefused
	}
	expected := make(map[string]generatedruntimestate.RouteActivationComponent,
		len(result.predecessorRoster))
	states := make(map[string]generatedruntimestate.ComponentState, len(predecessor.snapshot.components))
	for _, component := range result.predecessorRoster {
		expected[component.ComponentName] = component
	}
	for _, component := range predecessor.snapshot.components {
		states[component.plan.Name] = component.state
	}
	result.componentRetirements = make(map[string]generatedruntimestate.PredecessorRetirementObservation,
		len(result.predecessorRoster))
	for _, value := range database.Runtime.PredecessorRetirementComponents {
		if value.ActivationReceiptID != receiptID &&
			value.SuccessorDeploymentID != result.activation.SuccessorDeploymentID {
			continue
		}
		want, ok := expected[value.ComponentName]
		if !ok || value.ActivationReceiptID != receiptID || value.AppID != result.activation.AppID ||
			value.SuccessorDeploymentID != result.activation.SuccessorDeploymentID ||
			value.PredecessorDeploymentID != result.activation.PredecessorDeploymentID ||
			value.CandidateReceiptID != want.CandidateReceiptID || value.ContainerID != want.ContainerID ||
			value.CandidatePolicyDigest != want.CandidatePolicyDigest || value.ObservedAbsentAt.IsZero() {
			return result, false, errGeneratedDomainConstructionRefused
		}
		if _, duplicate := result.componentRetirements[value.ComponentName]; duplicate {
			return result, false, errGeneratedDomainConstructionRefused
		}
		result.componentRetirements[value.ComponentName] = value
	}
	for name := range expected {
		_, recorded := result.componentRetirements[name]
		if recorded && states[name] != generatedruntimestate.ComponentStopped ||
			!recorded && states[name] != generatedruntimestate.ComponentDraining {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	for _, value := range database.Runtime.PredecessorRetirementReceipts {
		if value.ActivationReceiptID == receiptID ||
			value.SuccessorDeploymentID == result.activation.SuccessorDeploymentID {
			return result, false, nil
		}
	}
	for _, value := range database.Runtime.AccountingHandoffAuthorizations {
		if value.ActivationReceiptID == receiptID ||
			value.SuccessorDeploymentID == result.activation.SuccessorDeploymentID {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	for _, value := range database.Runtime.AccountingHandoffReceipts {
		if value.ActivationReceiptID == receiptID ||
			value.SuccessorDeploymentID == result.activation.SuccessorDeploymentID {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	for _, value := range database.Runtime.RouteCompletionReceipts {
		if value.ActivationReceiptID == receiptID ||
			value.SuccessorDeploymentID == result.activation.SuccessorDeploymentID {
			return result, false, errGeneratedDomainConstructionRefused
		}
	}
	return result, true, nil
}

func generatedDomainColdRunningBindingRetainsRetirementPrefix(
	database deployments.GeneratedDomainStartupSnapshot, groups []generatedDomainColdGroup,
	operation deployments.GeneratedDomainStartupLifecycleOperation, retained generatedDomainColdGroup,
) bool {
	if retained.kind != generatedDomainColdRisky || !retained.snapshot.lifecycleRetained {
		return false
	}
	matches := 0
	for _, successor := range groups {
		prefix, found, err := generatedDomainColdPredecessorRetirementPrefix(database, successor, groups)
		if err != nil || !found || prefix.activation.PredecessorDeploymentID != retained.pins.deploymentID ||
			successor.pins.applicationID() != operation.AppID {
			continue
		}
		headCount := 0
		for _, group := range groups {
			if group.pins.applicationID() == successor.pins.applicationID() &&
				group.snapshot.observedHead.deploymentID == group.pins.deploymentID {
				headCount++
			}
		}
		bindingCount := 0
		for _, binding := range database.LifecycleBindings {
			if binding.DeploymentJobID != successor.pins.jobID() {
				continue
			}
			bindingCount++
			if headCount != 1 || binding.AppID != operation.AppID ||
				binding.Mode != deploymentbinding.ModeRunning || binding.DesiredRevision != operation.DesiredRevision ||
				binding.AuthorizingOperationID != operation.JobID {
				return false
			}
		}
		if bindingCount == 1 {
			matches++
		}
	}
	return matches == 1
}

// validateGeneratedDomainColdLifecycleRetention is the cold-only exception to
// fresh candidate admission's blanket lifecycle-retention refusal. Retained
// operations remain descriptive history. Only the exact operation selected by
// a Current running binding can authorize configured-debt restoration, and its
// pinned deployment must be an authenticated historical ancestor.
func validateGeneratedDomainColdLifecycleRetention(database deployments.GeneratedDomainStartupSnapshot,
	groups []generatedDomainColdGroup,
) error {
	return validateGeneratedDomainColdLifecycleRetentionWithPending(database, groups, nil)
}

// validateGeneratedDomainColdLifecycleRetentionWithPending consumes, but never
// contributes to, the independently reconstructed pre-042 Pending evidence.
// The sole extra shape is the still-current predecessor selected by that
// evidence. It remains subject to all operation, binding, route and head
// correlation checks below.
func validateGeneratedDomainColdLifecycleRetentionWithPending(database deployments.GeneratedDomainStartupSnapshot,
	groups []generatedDomainColdGroup, pending *generatedDomainColdPendingActivationEvidence,
) error {
	type operationIdentity struct {
		appID, jobID string
		revision     int64
	}
	seenOperations := make(map[operationIdentity]struct{}, len(database.LifecycleOperations))
	retained := make(map[string]int, len(groups))
	for _, operation := range database.LifecycleOperations {
		identity := operationIdentity{appID: operation.AppID, jobID: operation.JobID,
			revision: operation.DesiredRevision}
		if _, duplicate := seenOperations[identity]; duplicate || !operation.Complete {
			return errGeneratedDomainConstructionRefused
		}
		seenOperations[identity] = struct{}{}
		group, ok := generatedDomainColdGroupByDeployment(groups, operation.DeploymentID)
		if !ok || group.pins.applicationID() != operation.AppID ||
			group.kind != generatedDomainColdHistorical &&
				!generatedDomainColdRunningBindingRetainsRetirementPrefix(database, groups, operation, group) &&
				!generatedDomainColdPendingActivationRetainsCurrent(database, groups, pending, operation, group) {
			return errGeneratedDomainConstructionRefused
		}
		retained[operation.DeploymentID]++
	}
	for _, group := range groups {
		if group.snapshot.lifecycleRetained != (retained[group.pins.deploymentID] != 0) {
			return errGeneratedDomainConstructionRefused
		}
	}

	for _, current := range groups {
		if current.kind != generatedDomainColdCurrent {
			continue
		}
		var binding deployments.GeneratedDomainStartupLifecycleBinding
		bindingCount := 0
		for _, value := range database.LifecycleBindings {
			if value.DeploymentJobID == current.pins.jobID() {
				binding, bindingCount = value, bindingCount+1
			}
		}
		if bindingCount != 1 || binding.AppID != current.pins.applicationID() {
			return errGeneratedDomainConstructionRefused
		}
		if binding.Mode == deploymentbinding.ModeLegacyUninitialized {
			if binding.DesiredRevision != 0 || binding.AuthorizingOperationID != "" {
				return errGeneratedDomainConstructionRefused
			}
			continue
		}
		if binding.Mode != deploymentbinding.ModeRunning || binding.DesiredRevision < 1 ||
			binding.AuthorizingOperationID == "" {
			return errGeneratedDomainConstructionRefused
		}
		var authorizer deployments.GeneratedDomainStartupLifecycleOperation
		authorizerCount := 0
		for _, operation := range database.LifecycleOperations {
			if operation.AppID == binding.AppID && operation.JobID == binding.AuthorizingOperationID &&
				operation.DesiredRevision == binding.DesiredRevision {
				authorizer, authorizerCount = operation, authorizerCount+1
			}
		}
		if authorizerCount != 1 || !authorizer.Complete {
			return errGeneratedDomainConstructionRefused
		}
		if authorizer.DeploymentID == current.pins.deploymentID ||
			!generatedDomainColdAuthorizerLineage(groups, current, authorizer.DeploymentID) {
			return errGeneratedDomainConstructionRefused
		}
	}
	return nil
}

// generatedDomainColdPendingActivationRetainsCurrent admits no general
// Current/self-authorized lifecycle shape. It recognizes only the exact
// predecessor that an independently reconstructed pre-042 Pending activation
// selected. The subsequent gateway correlation consumes the same Pending
// evidence before any reservation or effect is allowed.
func generatedDomainColdPendingActivationRetainsCurrent(database deployments.GeneratedDomainStartupSnapshot,
	groups []generatedDomainColdGroup,
	pending *generatedDomainColdPendingActivationEvidence,
	operation deployments.GeneratedDomainStartupLifecycleOperation, current generatedDomainColdGroup,
) bool {
	if pending == nil || !pending.beforeActivation || pending.transition == nil ||
		!validGeneratedRouteActivationGatewayTransition(pending.transition) ||
		current.kind != generatedDomainColdCurrent || !current.snapshot.lifecycleRetained ||
		operation.DeploymentID != current.pins.deploymentID || operation.AppID != current.pins.applicationID() ||
		current.snapshot.observedHead.deploymentID != current.pins.deploymentID ||
		current.snapshot.observedHead.releaseID != current.pins.releaseID ||
		current.snapshot.observedHead.slot != current.pins.candidateSlot {
		return false
	}
	before, hadBefore, proposed, valid := generatedRouteActivationGatewayRoutes(pending.transition)
	if !valid || !hadBefore || pending.transition.request.AppID != operation.AppID ||
		pending.transition.request.FromSlot != before.Slot || pending.transition.request.ToSlot != proposed.Slot ||
		!sameRoute(proposed, routeRecord{Slot: pending.transition.request.ToSlot,
			Endpoints: pending.transition.request.Endpoints}) {
		return false
	}
	currentRoute, currentRouteValid := generatedDomainColdGroupRoute(current)
	if !currentRouteValid || !sameRoute(currentRoute, before) {
		return false
	}
	successor, found := generatedDomainColdGroupByDeployment(groups, pending.successorDeployment)
	if !found || successor.kind != generatedDomainColdRisky || !successor.bindHead || successor.selfHead ||
		successor.pins.applicationID() != operation.AppID ||
		successor.snapshot.runtime.phase != generatedruntimestate.PhaseSwitchingRoute ||
		successor.snapshot.headPins.previousDeploymentID != current.pins.deploymentID ||
		successor.snapshot.headPins.previousReleaseID != current.pins.releaseID ||
		successor.snapshot.headPins.previousSlot != current.pins.candidateSlot ||
		successor.snapshot.observedHead.deploymentID != current.snapshot.observedHead.deploymentID ||
		successor.snapshot.observedHead.releaseID != current.snapshot.observedHead.releaseID ||
		successor.snapshot.observedHead.slot != current.snapshot.observedHead.slot ||
		successor.snapshot.observedHead.generation != current.snapshot.observedHead.generation ||
		successor.snapshot.observedHead.activationReceiptID != current.snapshot.observedHead.activationReceiptID {
		return false
	}
	successorRoute, successorRouteValid := generatedDomainColdGroupRoute(successor)
	if !successorRouteValid || !sameRoute(successorRoute, proposed) {
		return false
	}
	bindingCount := 0
	for _, binding := range database.LifecycleBindings {
		if binding.DeploymentJobID != successor.pins.jobID() {
			continue
		}
		bindingCount++
		if binding.AppID != operation.AppID || binding.Mode != deploymentbinding.ModeRunning ||
			binding.DesiredRevision != operation.DesiredRevision ||
			binding.AuthorizingOperationID != operation.JobID {
			return false
		}
	}
	return bindingCount == 1
}

func generatedDomainColdStagePair(status Status, phase generatedruntimestate.Phase,
	migration generatedruntimestate.MigrationState, migrationRequired bool,
) bool {
	switch phase {
	case generatedruntimestate.PhasePreflight:
		return status == Preparing && (migration == generatedruntimestate.MigrationNotRequired ||
			migrationRequired && migration == generatedruntimestate.MigrationPending)
	case generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseMigrating,
		generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth,
		generatedruntimestate.PhaseSwitchingRoute:
		return candidateStagePair(status, phase, migration, migrationRequired)
	case generatedruntimestate.PhaseDraining:
		return status == WaitingHealth && (!migrationRequired && migration == generatedruntimestate.MigrationNotRequired ||
			migrationRequired && migration == generatedruntimestate.MigrationSucceeded)
	case generatedruntimestate.PhaseSucceeded:
		return status == Succeeded && (!migrationRequired && migration == generatedruntimestate.MigrationNotRequired ||
			migrationRequired && migration == generatedruntimestate.MigrationSucceeded)
	case generatedruntimestate.PhaseFailed:
		return status == Failed
	case generatedruntimestate.PhaseCancelled:
		return status == Cancelled
	default:
		return false
	}
}

func validateGeneratedDomainColdComponent(pins candidateValidationPins,
	component candidateAdmissionComponent,
) error {
	switch component.state {
	case generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady,
		generatedruntimestate.ComponentStarting, generatedruntimestate.ComponentRunning,
		generatedruntimestate.ComponentHealthy, generatedruntimestate.ComponentActive:
		return validateCandidateComponentProvenance(pins, component,
			component.state != generatedruntimestate.ComponentPending)
	case generatedruntimestate.ComponentDraining:
		copy := component
		copy.state = generatedruntimestate.ComponentActive
		return validateCandidateComponentProvenance(pins, copy, true)
	case generatedruntimestate.ComponentStopped, generatedruntimestate.ComponentFailed:
		if component.selectedArtifactID == "" {
			if component.artifact != nil || component.receipt != nil || component.containerName != "" || component.containerID != "" {
				return ErrInvalidDeployment
			}
			return nil
		}
		copy := component
		if component.receipt == nil {
			copy.state = generatedruntimestate.ComponentImageReady
		} else if component.containerID == "" {
			copy.state = generatedruntimestate.ComponentStarting
		} else {
			copy.state = generatedruntimestate.ComponentActive
		}
		return validateCandidateComponentProvenance(pins, copy, component.receipt != nil)
	default:
		return deploymentexecution.ErrStageInvalid
	}
}

func generatedDomainColdStartupComponents(values []generatedruntimestate.GeneratedDomainStartupComponent,
	deploymentID string,
) []generatedruntimestate.GeneratedDomainStartupComponent {
	result := []generatedruntimestate.GeneratedDomainStartupComponent{}
	for _, value := range values {
		if value.DeploymentID == deploymentID {
			result = append(result, value)
		}
	}
	return result
}

func sameGeneratedDomainColdStartupComponent(left candidateAdmissionComponent,
	right generatedruntimestate.GeneratedDomainStartupComponent,
) bool {
	return left.plan.Name == right.ComponentName && left.slot == right.Slot &&
		left.selectedArtifactID == right.ImageArtifactID && left.containerName == right.ContainerName &&
		left.containerID == right.ContainerID && left.state == right.State
}

func generatedDomainColdReceiptsMatch(values []generatedruntimestate.GeneratedDomainStartupCandidateIntent,
	deploymentID string, components []candidateAdmissionComponent,
) bool {
	expected := make(map[string]generatedruntimestate.GeneratedDomainStartupCandidateIntent)
	for _, component := range components {
		if component.receipt == nil {
			continue
		}
		r := component.receipt
		expected[component.plan.Name] = generatedruntimestate.GeneratedDomainStartupCandidateIntent{
			ReceiptID: r.id, DeploymentID: deploymentID, ComponentName: component.plan.Name,
			CreatorJobID: r.creatorJobID, CreatorWorkerAttempt: r.creatorWorkerAttempt,
			ConfigurationRevisionID:     r.configurationRevisionID,
			ConfigurationRevisionNumber: r.configurationRevisionNumber,
			ImageArtifactID:             r.imageArtifactID, ImageArtifactAttempt: r.imageArtifactAttempt,
			EnvironmentOperationID:    r.environmentOperationID,
			EnvironmentStagingAttempt: r.environmentStagingAttempt,
			CandidatePolicyVersion:    r.policyVersion, CandidatePolicyDigest: r.policyDigest}
	}
	seen := 0
	for _, value := range values {
		if value.DeploymentID != deploymentID {
			continue
		}
		want, ok := expected[value.ComponentName]
		if !ok || !reflect.DeepEqual(value, want) {
			return false
		}
		seen++
	}
	return seen == len(expected)
}

func generatedDomainColdArtifactsMatch(values []deployments.GeneratedDomainStartupArtifact,
	deploymentID string, components []candidateAdmissionComponent,
) bool {
	_ = deploymentID
	for _, component := range components {
		if component.artifact == nil {
			continue
		}
		found := 0
		for _, value := range values {
			if value.ID == component.artifact.id {
				found++
				if value.ReleaseID != component.artifact.releaseID ||
					value.DeploymentPlanRevisionID != component.artifact.deploymentPlanRevisionID ||
					value.DeploymentPlanRevisionNumber != component.artifact.deploymentPlanRevisionNumber ||
					value.ComponentID != component.artifact.componentName ||
					value.AttemptNumber != component.artifact.attemptNumber || value.State != component.artifact.state {
					return false
				}
			}
		}
		if found != 1 {
			return false
		}
	}
	return true
}

func classifyGeneratedDomainColdGroup(pins candidateValidationPins, snapshot candidateAdmissionSnapshot,
	groups []generatedDomainColdGroup,
	database deployments.GeneratedDomainStartupSnapshot,
) (generatedDomainColdGroupKind, bool, bool, bool, error) {
	activations := database.Runtime.RouteActivations
	all := func(states ...generatedruntimestate.ComponentState) bool {
		allowed := make(map[generatedruntimestate.ComponentState]struct{}, len(states))
		for _, state := range states {
			allowed[state] = struct{}{}
		}
		for _, component := range snapshot.components {
			if _, ok := allowed[component.state]; !ok {
				return false
			}
		}
		return len(snapshot.components) != 0
	}
	self := snapshot.observedHead.deploymentID == snapshot.headPins.deploymentID
	predecessor := snapshot.observedHead.deploymentID == snapshot.headPins.previousDeploymentID &&
		snapshot.observedHead.releaseID == snapshot.headPins.previousReleaseID &&
		snapshot.observedHead.slot == snapshot.headPins.previousSlot
	if snapshot.headPins.previousDeploymentID == "" {
		predecessor = snapshot.observedHead.deploymentID == "" && snapshot.observedHead.generation == 0
	}
	switch snapshot.runtime.phase {
	case generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding,
		generatedruntimestate.PhaseMigrating:
		if !all(generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady) || !predecessor ||
			generatedDomainColdHasReceipt(snapshot.components) {
			return 0, false, false, false, errGeneratedDomainConstructionRefused
		}
		return generatedDomainColdNoEffect, true, false, false, nil
	case generatedruntimestate.PhaseStartingCandidate:
		if !all(generatedruntimestate.ComponentImageReady, generatedruntimestate.ComponentStarting,
			generatedruntimestate.ComponentRunning) || !predecessor {
			return 0, false, false, false, errGeneratedDomainConstructionRefused
		}
		if !generatedDomainColdHasReceipt(snapshot.components) {
			return generatedDomainColdNoEffect, true, false, false, nil
		}
		return generatedDomainColdRisky, true, false, false, nil
	case generatedruntimestate.PhaseWaitingHealth:
		if !all(generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy) || !predecessor {
			return 0, false, false, false, errGeneratedDomainConstructionRefused
		}
		return generatedDomainColdRisky, true, false, false, nil
	case generatedruntimestate.PhaseSwitchingRoute:
		if all(generatedruntimestate.ComponentHealthy) && predecessor {
			return generatedDomainColdRisky, true, false, false, nil
		}
		if all(generatedruntimestate.ComponentActive) && self &&
			generatedDomainColdActivationMatches(pins, snapshot, groups, activations) {
			return generatedDomainColdRisky, true, true, false, nil
		}
	case generatedruntimestate.PhaseDraining:
		if all(generatedruntimestate.ComponentActive) && self &&
			generatedDomainColdActivationMatches(pins, snapshot, groups, activations) {
			return generatedDomainColdRisky, true, true, false, nil
		}
	case generatedruntimestate.PhaseSucceeded:
		if all(generatedruntimestate.ComponentActive) && self {
			legacy := snapshot.observedHead.activationReceiptID == ""
			if !legacy && !generatedDomainColdActivationMatches(pins, snapshot, groups, activations) {
				break
			}
			return generatedDomainColdCurrent, true, true, legacy, nil
		}
		if all(generatedruntimestate.ComponentDraining) {
			return generatedDomainColdRisky, false, false, false, nil
		}
		mixedDrainingStopped := false
		if all(generatedruntimestate.ComponentDraining, generatedruntimestate.ComponentStopped) {
			seenDraining, seenStopped := false, false
			for _, component := range snapshot.components {
				seenDraining = seenDraining || component.state == generatedruntimestate.ComponentDraining
				seenStopped = seenStopped || component.state == generatedruntimestate.ComponentStopped
			}
			mixedDrainingStopped = seenDraining && seenStopped
		}
		if mixedDrainingStopped {
			matched := 0
			for _, successor := range groups {
				prefix, found, err := generatedDomainColdPredecessorRetirementPrefix(database, successor, groups)
				if err != nil {
					return 0, false, false, false, err
				}
				if found && prefix.predecessorGroup.pins.deploymentID == pins.deploymentID {
					matched++
				}
			}
			if matched != 1 {
				return 0, false, false, false, errGeneratedDomainConstructionRefused
			}
			return generatedDomainColdRisky, false, false, false, nil
		}
		if all(generatedruntimestate.ComponentStopped, generatedruntimestate.ComponentFailed) {
			return generatedDomainColdHistorical, false, false, false, nil
		}
	case generatedruntimestate.PhaseFailed, generatedruntimestate.PhaseCancelled:
		if all(generatedruntimestate.ComponentFailed, generatedruntimestate.ComponentStopped) {
			return generatedDomainColdHistorical, false, false, false, nil
		}
	}
	return 0, false, false, false, errGeneratedDomainConstructionRefused
}

func generatedDomainColdHasReceipt(components []candidateAdmissionComponent) bool {
	for _, component := range components {
		if component.receipt != nil || component.containerName != "" || component.containerID != "" {
			return true
		}
	}
	return false
}

func generatedDomainColdActivationMatches(pins candidateValidationPins, snapshot candidateAdmissionSnapshot,
	groups []generatedDomainColdGroup,
	values []generatedruntimestate.GeneratedDomainStartupRouteActivation,
) bool {
	id := snapshot.observedHead.activationReceiptID
	if id == "" {
		return false
	}
	found := 0
	for _, value := range values {
		if value.ReceiptID != id {
			continue
		}
		found++
		candidateRoster, ok := generatedDomainColdActivationRoster(snapshot)
		if !ok || value.AppID != snapshot.headPins.appID ||
			value.SuccessorDeploymentID != snapshot.headPins.deploymentID ||
			value.SuccessorReleaseID != snapshot.headPins.releaseID ||
			value.SuccessorSlot != snapshot.headPins.slot ||
			value.SuccessorGeneration != snapshot.observedHead.generation ||
			value.PredecessorDeploymentID != snapshot.headPins.previousDeploymentID ||
			value.PredecessorReleaseID != snapshot.headPins.previousReleaseID ||
			value.PredecessorSlot != snapshot.headPins.previousSlot ||
			value.CreatorJobID != snapshot.pins.jobID || value.CreatorWorkerAttempt < 1 ||
			value.WriterJobID != snapshot.pins.jobID ||
			value.CreatorWorkerAttempt > value.WriterWorkerAttempt ||
			value.WriterWorkerAttempt < 1 || value.WriterWorkerAttempt > pins.attempt() ||
			value.CandidateRosterDigest != generatedRouteActivationRosterDigest(candidateRoster) ||
			!lowerHex(value.RouteIntentDigest, 64) || !lowerHex(value.CommittedRouteProofDigest, 64) {
			return false
		}
		if value.PredecessorDeploymentID == "" {
			if value.PredecessorGeneration != 0 || value.PredecessorActivationReceiptID != "" ||
				value.PredecessorRosterDigest != generatedRouteActivationRosterDigest(nil) {
				return false
			}
		} else if value.PredecessorGeneration < 1 ||
			value.PredecessorGeneration+1 != value.SuccessorGeneration {
			return false
		} else {
			predecessor, ok := generatedDomainColdGroupByDeployment(groups, value.PredecessorDeploymentID)
			if !ok || predecessor.snapshot.pins.releaseID != value.PredecessorReleaseID ||
				predecessor.snapshot.pins.candidateSlot != value.PredecessorSlot {
				return false
			}
			predecessorRoster, ok := generatedDomainColdActivationRoster(predecessor.snapshot)
			if !ok || value.PredecessorRosterDigest != generatedRouteActivationRosterDigest(predecessorRoster) {
				return false
			}
			if value.PredecessorActivationReceiptID != "" &&
				!generatedDomainColdActivationReceiptLinksPredecessor(value, values) {
				return false
			}
		}
	}
	return found == 1
}

func generatedDomainColdGroupByDeployment(groups []generatedDomainColdGroup,
	deploymentID string,
) (generatedDomainColdGroup, bool) {
	var found generatedDomainColdGroup
	count := 0
	for _, group := range groups {
		if group.pins.deploymentID == deploymentID {
			found, count = group, count+1
		}
	}
	return found, count == 1
}

func generatedDomainColdActivationRoster(snapshot candidateAdmissionSnapshot) (
	[]generatedruntimestate.RouteActivationComponent, bool,
) {
	result := make([]generatedruntimestate.RouteActivationComponent, 0, len(snapshot.components))
	for _, component := range snapshot.components {
		if component.artifact == nil || component.receipt == nil {
			return nil, false
		}
		result = append(result, generatedruntimestate.RouteActivationComponent{
			DeploymentID: snapshot.headPins.deploymentID, ComponentName: component.plan.Name,
			Slot: component.slot, ImageArtifactID: component.artifact.id,
			ContainerName: component.containerName, ContainerID: component.containerID,
			CandidateReceiptID: component.receipt.id, CandidatePolicyDigest: component.receipt.policyDigest,
		})
	}
	canonical, ok := canonicalGeneratedRouteActivationRoster(result)
	return canonical, ok && len(canonical) != 0
}

func generatedDomainColdActivationReceiptLinksPredecessor(
	current generatedruntimestate.GeneratedDomainStartupRouteActivation,
	values []generatedruntimestate.GeneratedDomainStartupRouteActivation,
) bool {
	found := 0
	for _, value := range values {
		if value.ReceiptID != current.PredecessorActivationReceiptID {
			continue
		}
		found++
		if value.AppID != current.AppID ||
			value.SuccessorDeploymentID != current.PredecessorDeploymentID ||
			value.SuccessorReleaseID != current.PredecessorReleaseID ||
			value.SuccessorSlot != current.PredecessorSlot ||
			value.SuccessorGeneration != current.PredecessorGeneration {
			return false
		}
	}
	return found == 1
}

type generatedDomainColdCompletedRouteEvidence struct {
	activation    generatedruntimestate.GeneratedDomainStartupRouteActivation
	authorization generatedruntimestate.RouteAccountingHandoffAuthorization
	handoff       generatedruntimestate.RouteAccountingHandoffReceipt
	completion    generatedruntimestate.RouteCompletionReceipt
}

func generatedDomainColdCompletedRouteEvidenceForGroup(database deployments.GeneratedDomainStartupSnapshot,
	group generatedDomainColdGroup, groups []generatedDomainColdGroup,
) (generatedDomainColdCompletedRouteEvidence, bool) {
	var evidence generatedDomainColdCompletedRouteEvidence
	activationCount := 0
	for _, value := range database.Runtime.RouteActivations {
		if value.AppID == group.pins.applicationID() && value.SuccessorDeploymentID == group.pins.deploymentID {
			evidence.activation, activationCount = value, activationCount+1
		}
	}
	if activationCount != 1 {
		return generatedDomainColdCompletedRouteEvidence{}, false
	}
	activation := evidence.activation
	candidateRoster, ok := generatedDomainColdActivationRoster(group.snapshot)
	if !ok || activation.SuccessorReleaseID != group.pins.releaseID ||
		activation.SuccessorSlot != group.pins.candidateSlot ||
		activation.CreatorJobID != group.pins.jobID() || activation.CreatorWorkerAttempt < 1 ||
		activation.WriterJobID != group.pins.jobID() ||
		activation.CreatorWorkerAttempt > activation.WriterWorkerAttempt ||
		activation.WriterWorkerAttempt < 1 || activation.WriterWorkerAttempt > group.pins.attempt() ||
		activation.CandidateRosterDigest != generatedRouteActivationRosterDigest(candidateRoster) ||
		activation.PredecessorDeploymentID != group.snapshot.headPins.previousDeploymentID ||
		activation.PredecessorReleaseID != group.snapshot.headPins.previousReleaseID ||
		activation.PredecessorSlot != group.snapshot.headPins.previousSlot {
		return generatedDomainColdCompletedRouteEvidence{}, false
	}
	predecessorCount := 0
	if activation.PredecessorDeploymentID == "" {
		if activation.PredecessorGeneration != 0 || activation.PredecessorActivationReceiptID != "" ||
			activation.PredecessorRosterDigest != generatedRouteActivationRosterDigest(nil) {
			return generatedDomainColdCompletedRouteEvidence{}, false
		}
	} else {
		predecessor, found := generatedDomainColdGroupByDeployment(groups, activation.PredecessorDeploymentID)
		if !found || predecessor.pins.applicationID() != group.pins.applicationID() ||
			predecessor.pins.releaseID != activation.PredecessorReleaseID ||
			predecessor.pins.candidateSlot != activation.PredecessorSlot {
			return generatedDomainColdCompletedRouteEvidence{}, false
		}
		predecessorRoster, valid := generatedDomainColdActivationRoster(predecessor.snapshot)
		if !valid || activation.PredecessorRosterDigest != generatedRouteActivationRosterDigest(predecessorRoster) {
			return generatedDomainColdCompletedRouteEvidence{}, false
		}
		var predecessorActivation generatedruntimestate.GeneratedDomainStartupRouteActivation
		predecessorActivationCount := 0
		for _, value := range database.Runtime.RouteActivations {
			if value.AppID == activation.AppID && value.SuccessorDeploymentID == activation.PredecessorDeploymentID {
				predecessorActivation, predecessorActivationCount = value, predecessorActivationCount+1
			}
		}
		if predecessorActivationCount != 1 || activation.PredecessorActivationReceiptID != predecessorActivation.ReceiptID ||
			activation.PredecessorGeneration != predecessorActivation.SuccessorGeneration ||
			activation.SuccessorGeneration != predecessorActivation.SuccessorGeneration+1 {
			return generatedDomainColdCompletedRouteEvidence{}, false
		}
		predecessorCount = len(predecessor.snapshot.components)
	}

	authorizationCount, handoffCount, completionCount, present := 0, 0, 0, 0
	for _, value := range database.Runtime.AccountingHandoffAuthorizations {
		if value.AppID == activation.AppID && value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			present++
			if value.ActivationReceiptID == activation.ReceiptID {
				evidence.authorization, authorizationCount = value, authorizationCount+1
			}
		}
	}
	for _, value := range database.Runtime.AccountingHandoffReceipts {
		if value.AppID == activation.AppID && value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			present++
			if value.ActivationReceiptID == activation.ReceiptID {
				evidence.handoff, handoffCount = value, handoffCount+1
			}
		}
	}
	for _, value := range database.Runtime.RouteCompletionReceipts {
		if value.AppID == activation.AppID && value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			present++
			if value.ActivationReceiptID == activation.ReceiptID {
				evidence.completion, completionCount = value, completionCount+1
			}
		}
	}
	if present != 3 || authorizationCount != 1 || handoffCount != 1 || completionCount != 1 {
		return generatedDomainColdCompletedRouteEvidence{}, false
	}
	authorization, handoff, completion := evidence.authorization, evidence.handoff, evidence.completion
	if authorization.AppID != activation.AppID ||
		authorization.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
		authorization.PredecessorDeploymentID != activation.PredecessorDeploymentID ||
		authorization.CandidateRosterDigest != activation.CandidateRosterDigest ||
		authorization.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
		authorization.SuccessorComponentCount != len(group.snapshot.components) ||
		authorization.PredecessorComponentCount != predecessorCount || authorization.AuthorizedAt.IsZero() ||
		handoff.AppID != authorization.AppID ||
		handoff.SuccessorDeploymentID != authorization.SuccessorDeploymentID ||
		handoff.PredecessorDeploymentID != authorization.PredecessorDeploymentID ||
		handoff.CandidateRosterDigest != authorization.CandidateRosterDigest ||
		handoff.PredecessorRosterDigest != authorization.PredecessorRosterDigest ||
		handoff.SuccessorComponentCount != authorization.SuccessorComponentCount ||
		handoff.PredecessorComponentCount != authorization.PredecessorComponentCount || handoff.HandedOffAt.IsZero() ||
		authorization.AuthorizedAt.After(handoff.HandedOffAt) ||
		completion.AppID != activation.AppID || completion.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
		completion.CreatorJobID != activation.CreatorJobID ||
		completion.CreatorWorkerAttempt != activation.CreatorWorkerAttempt ||
		activation.WriterJobID != group.pins.jobID() ||
		activation.WriterWorkerAttempt < activation.CreatorWorkerAttempt ||
		activation.WriterWorkerAttempt > completion.CurrentWorkerAttempt ||
		completion.CurrentJobID != group.pins.jobID() || completion.CurrentWorkerAttempt != group.pins.attempt() ||
		completion.CompletionCode != "deployment_completed" || completion.CompletedAt.IsZero() ||
		handoff.HandedOffAt.After(completion.CompletedAt) {
		return generatedDomainColdCompletedRouteEvidence{}, false
	}
	var main deployments.GeneratedDomainStartupDeployment
	mainCount := 0
	for _, value := range database.GeneratedDeployments {
		if value.ID == group.pins.deploymentID {
			main, mainCount = value, mainCount+1
		}
	}
	if mainCount != 1 || main.AppID != group.pins.applicationID() || main.JobID != group.pins.jobID() ||
		main.Status != deployments.Succeeded || main.JobStatus != "succeeded" || main.JobPhase != "succeeded" ||
		main.JobAttempt != completion.CurrentWorkerAttempt || main.CanonicalSuccessEventCount != 1 ||
		main.SuccessEventCount != 1 || !main.CanonicalSuccessEventAt.Equal(completion.CompletedAt) {
		return generatedDomainColdCompletedRouteEvidence{}, false
	}
	return evidence, true
}

func generatedDomainColdAuthorizerAncestry(database deployments.GeneratedDomainStartupSnapshot,
	groups []generatedDomainColdGroup, current generatedDomainColdGroup, authorizerDeploymentID string,
) bool {
	if current.kind != generatedDomainColdCurrent || authorizerDeploymentID == "" ||
		authorizerDeploymentID == current.pins.deploymentID {
		return false
	}
	seen := make(map[string]struct{}, len(groups))
	cursor := current
	for len(seen) < len(groups) {
		if _, duplicate := seen[cursor.pins.deploymentID]; duplicate {
			return false
		}
		seen[cursor.pins.deploymentID] = struct{}{}
		if _, ok := generatedDomainColdCompletedRouteEvidenceForGroup(database, cursor, groups); !ok {
			return false
		}
		if cursor.pins.deploymentID == authorizerDeploymentID {
			return cursor.kind == generatedDomainColdHistorical && cursor.snapshot.lifecycleRetained
		}
		predecessorID := cursor.snapshot.headPins.previousDeploymentID
		if predecessorID == "" {
			return false
		}
		predecessor, ok := generatedDomainColdGroupByDeployment(groups, predecessorID)
		if !ok || predecessor.pins.applicationID() != current.pins.applicationID() ||
			predecessor.pins.releaseID != cursor.snapshot.headPins.previousReleaseID ||
			predecessor.pins.candidateSlot != cursor.snapshot.headPins.previousSlot {
			return false
		}
		cursor = predecessor
	}
	return false
}

func generatedDomainColdAuthorizerLineage(groups []generatedDomainColdGroup,
	current generatedDomainColdGroup, authorizerDeploymentID string,
) bool {
	if current.kind != generatedDomainColdCurrent || authorizerDeploymentID == "" ||
		authorizerDeploymentID == current.pins.deploymentID {
		return false
	}
	seen := make(map[string]struct{}, len(groups))
	cursor := current
	for len(seen) < len(groups) {
		if _, duplicate := seen[cursor.pins.deploymentID]; duplicate {
			return false
		}
		seen[cursor.pins.deploymentID] = struct{}{}
		if cursor.pins.deploymentID == authorizerDeploymentID {
			return cursor.kind == generatedDomainColdHistorical && cursor.snapshot.lifecycleRetained
		}
		predecessorID := cursor.snapshot.headPins.previousDeploymentID
		if predecessorID == "" {
			return false
		}
		predecessor, ok := generatedDomainColdGroupByDeployment(groups, predecessorID)
		if !ok || predecessor.pins.applicationID() != current.pins.applicationID() ||
			predecessor.pins.releaseID != cursor.snapshot.headPins.previousReleaseID ||
			predecessor.pins.candidateSlot != cursor.snapshot.headPins.previousSlot {
			return false
		}
		cursor = predecessor
	}
	return false
}

// generatedDomainColdDrainingDebtEvidence recognizes the closed durable prefix
// after predecessor retirement has completed and before route completion
// advances the deployment from Draining. The exact no-044 and authorization-
// only prefixes restore both successor and predecessor debt; a complete 044
// restores only successor debt because its predecessor release is durable.
// Earlier valid prefixes keep the existing Risky raw-reservation behavior.
// Once a relevant row is present, crossed or non-prefix evidence is refused
// rather than treated as absence.
func generatedDomainColdDrainingDebtEvidence(database deployments.GeneratedDomainStartupSnapshot,
	group generatedDomainColdGroup, groups []generatedDomainColdGroup,
) (bool, error) {
	if group.kind != generatedDomainColdRisky || group.snapshot.runtime.phase != generatedruntimestate.PhaseDraining ||
		group.snapshot.observedHead.deploymentID != group.pins.deploymentID ||
		group.snapshot.observedHead.activationReceiptID == "" {
		return false, nil
	}
	for _, component := range group.snapshot.components {
		if component.state != generatedruntimestate.ComponentActive {
			return false, errGeneratedDomainConstructionRefused
		}
	}
	receiptID := group.snapshot.observedHead.activationReceiptID
	var activation generatedruntimestate.GeneratedDomainStartupRouteActivation
	activationCount, relevantActivationCount := 0, 0
	for _, value := range database.Runtime.RouteActivations {
		if value.ReceiptID == receiptID || value.SuccessorDeploymentID == group.pins.deploymentID {
			relevantActivationCount++
		}
		if value.ReceiptID == receiptID {
			activation, activationCount = value, activationCount+1
		}
	}
	if relevantActivationCount != 1 || activationCount != 1 ||
		!generatedDomainColdActivationMatches(group.pins, group.snapshot, groups,
			database.Runtime.RouteActivations) {
		return false, errGeneratedDomainConstructionRefused
	}

	predecessorRoster := []generatedruntimestate.RouteActivationComponent{}
	if activation.PredecessorDeploymentID != "" {
		predecessor, ok := generatedDomainColdGroupByDeployment(groups, activation.PredecessorDeploymentID)
		if !ok {
			return false, errGeneratedDomainConstructionRefused
		}
		predecessorRoster, ok = generatedDomainColdActivationRoster(predecessor.snapshot)
		if !ok {
			return false, errGeneratedDomainConstructionRefused
		}
	}
	expectedPredecessor := make(map[string]generatedruntimestate.RouteActivationComponent, len(predecessorRoster))
	for _, component := range predecessorRoster {
		expectedPredecessor[component.ComponentName] = component
	}

	observations := make(map[string]generatedruntimestate.PredecessorRetirementObservation,
		len(predecessorRoster))
	claimed := 0
	for _, value := range database.Runtime.PredecessorRetirementComponents {
		if value.ActivationReceiptID != receiptID && value.SuccessorDeploymentID != activation.SuccessorDeploymentID {
			continue
		}
		claimed++
		expected, ok := expectedPredecessor[value.ComponentName]
		if !ok || value.ActivationReceiptID != receiptID || value.AppID != activation.AppID ||
			value.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
			value.PredecessorDeploymentID != activation.PredecessorDeploymentID ||
			value.CandidateReceiptID != expected.CandidateReceiptID ||
			value.ContainerID != expected.ContainerID ||
			value.CandidatePolicyDigest != expected.CandidatePolicyDigest || value.ObservedAbsentAt.IsZero() {
			return false, errGeneratedDomainConstructionRefused
		}
		if _, duplicate := observations[value.ComponentName]; duplicate {
			return false, errGeneratedDomainConstructionRefused
		}
		observations[value.ComponentName] = value
	}

	var retirement generatedruntimestate.PredecessorRetirementReceipt
	retirementCount := 0
	for _, value := range database.Runtime.PredecessorRetirementReceipts {
		if value.ActivationReceiptID != receiptID && value.SuccessorDeploymentID != activation.SuccessorDeploymentID {
			continue
		}
		claimed++
		if value.ActivationReceiptID != receiptID || value.AppID != activation.AppID ||
			value.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
			value.PredecessorDeploymentID != activation.PredecessorDeploymentID ||
			value.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
			value.ComponentCount != len(predecessorRoster) || value.RetiredAt.IsZero() {
			return false, errGeneratedDomainConstructionRefused
		}
		retirement, retirementCount = value, retirementCount+1
	}
	if retirementCount > 1 {
		return false, errGeneratedDomainConstructionRefused
	}

	var authorization generatedruntimestate.RouteAccountingHandoffAuthorization
	authorizationCount := 0
	for _, value := range database.Runtime.AccountingHandoffAuthorizations {
		if value.ActivationReceiptID != receiptID && value.SuccessorDeploymentID != activation.SuccessorDeploymentID {
			continue
		}
		claimed++
		if value.ActivationReceiptID != receiptID || value.AppID != activation.AppID ||
			value.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
			value.PredecessorDeploymentID != activation.PredecessorDeploymentID ||
			value.CandidateRosterDigest != activation.CandidateRosterDigest ||
			value.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
			value.SuccessorComponentCount != len(group.snapshot.components) ||
			value.PredecessorComponentCount != len(predecessorRoster) || value.AuthorizedAt.IsZero() {
			return false, errGeneratedDomainConstructionRefused
		}
		authorization, authorizationCount = value, authorizationCount+1
	}
	if authorizationCount > 1 {
		return false, errGeneratedDomainConstructionRefused
	}

	var handoff generatedruntimestate.RouteAccountingHandoffReceipt
	handoffCount := 0
	for _, value := range database.Runtime.AccountingHandoffReceipts {
		if value.ActivationReceiptID != receiptID && value.SuccessorDeploymentID != activation.SuccessorDeploymentID {
			continue
		}
		claimed++
		if value.ActivationReceiptID != receiptID || value.AppID != activation.AppID ||
			value.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
			value.PredecessorDeploymentID != activation.PredecessorDeploymentID ||
			value.CandidateRosterDigest != activation.CandidateRosterDigest ||
			value.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
			value.SuccessorComponentCount != len(group.snapshot.components) ||
			value.PredecessorComponentCount != len(predecessorRoster) || value.HandedOffAt.IsZero() {
			return false, errGeneratedDomainConstructionRefused
		}
		handoff, handoffCount = value, handoffCount+1
	}
	if handoffCount > 1 {
		return false, errGeneratedDomainConstructionRefused
	}

	for _, value := range database.Runtime.RouteCompletionReceipts {
		if value.ActivationReceiptID == receiptID || value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			return false, errGeneratedDomainConstructionRefused
		}
	}
	if claimed == 0 {
		return false, nil
	}
	if retirementCount == 0 {
		if authorizationCount != 0 || handoffCount != 0 {
			return false, errGeneratedDomainConstructionRefused
		}
		return false, nil
	}
	if len(observations) != len(predecessorRoster) {
		return false, errGeneratedDomainConstructionRefused
	}
	for _, observation := range observations {
		if observation.ObservedAbsentAt.After(retirement.RetiredAt) {
			return false, errGeneratedDomainConstructionRefused
		}
	}
	if authorizationCount == 0 {
		if handoffCount != 0 {
			return false, errGeneratedDomainConstructionRefused
		}
		return true, nil
	}
	if retirement.RetiredAt.After(authorization.AuthorizedAt) {
		return false, errGeneratedDomainConstructionRefused
	}
	if handoffCount == 0 {
		return true, nil
	}
	if authorization.AuthorizedAt.After(handoff.HandedOffAt) {
		return false, errGeneratedDomainConstructionRefused
	}
	return true, nil
}

func generatedDomainColdDrainingDebtDeployments(database deployments.GeneratedDomainStartupSnapshot,
	group generatedDomainColdGroup, groups []generatedDomainColdGroup,
) ([]string, error) {
	prefix, pendingRetirement, err := generatedDomainColdPredecessorRetirementPrefix(database, group, groups)
	if err != nil {
		return nil, err
	}
	if pendingRetirement {
		return []string{group.pins.deploymentID, prefix.activation.PredecessorDeploymentID}, nil
	}
	restored, err := generatedDomainColdDrainingDebtEvidence(database, group, groups)
	if err != nil || !restored {
		return nil, err
	}
	receiptID := group.snapshot.observedHead.activationReceiptID
	var activation generatedruntimestate.GeneratedDomainStartupRouteActivation
	activationCount := 0
	for _, value := range database.Runtime.RouteActivations {
		if value.ReceiptID == receiptID && value.AppID == group.pins.applicationID() &&
			value.SuccessorDeploymentID == group.pins.deploymentID {
			activation, activationCount = value, activationCount+1
		}
	}
	if activationCount != 1 {
		return nil, errGeneratedDomainConstructionRefused
	}
	handoffCount := 0
	for _, value := range database.Runtime.AccountingHandoffReceipts {
		if value.ActivationReceiptID == receiptID || value.SuccessorDeploymentID == group.pins.deploymentID {
			handoffCount++
		}
	}
	result := []string{group.pins.deploymentID}
	if handoffCount == 0 && activation.PredecessorDeploymentID != "" {
		if _, found := generatedDomainColdGroupByDeployment(groups, activation.PredecessorDeploymentID); !found {
			return nil, errGeneratedDomainConstructionRefused
		}
		result = append(result, activation.PredecessorDeploymentID)
	}
	return result, nil
}

// generatedDomainColdCurrentDebtEvidence classifies only the independently
// projected durable acknowledgement chain for one already-attested Current.
// The DTOs authenticate classification; configured debt is derived later from
// the validated component policies.
func generatedDomainColdCurrentDebtEvidence(database deployments.GeneratedDomainStartupSnapshot,
	group generatedDomainColdGroup, groups []generatedDomainColdGroup,
) (bool, error) {
	receiptID := group.snapshot.observedHead.activationReceiptID
	if receiptID == "" {
		if !group.legacyHead {
			return false, errGeneratedDomainConstructionRefused
		}
		for _, value := range database.Runtime.RouteActivations {
			if value.SuccessorDeploymentID == group.pins.deploymentID {
				return false, errGeneratedDomainConstructionRefused
			}
		}
		for _, value := range database.Runtime.AccountingHandoffAuthorizations {
			if value.SuccessorDeploymentID == group.pins.deploymentID {
				return false, errGeneratedDomainConstructionRefused
			}
		}
		for _, value := range database.Runtime.AccountingHandoffReceipts {
			if value.SuccessorDeploymentID == group.pins.deploymentID {
				return false, errGeneratedDomainConstructionRefused
			}
		}
		for _, value := range database.Runtime.RouteCompletionReceipts {
			if value.SuccessorDeploymentID == group.pins.deploymentID {
				return false, errGeneratedDomainConstructionRefused
			}
		}
		return false, nil
	}
	var activation generatedruntimestate.GeneratedDomainStartupRouteActivation
	activationCount, relevantActivationCount := 0, 0
	for _, value := range database.Runtime.RouteActivations {
		if value.ReceiptID == receiptID || value.SuccessorDeploymentID == group.pins.deploymentID {
			relevantActivationCount++
		}
		if value.ReceiptID == receiptID {
			activation, activationCount = value, activationCount+1
		}
	}
	if relevantActivationCount != 1 || activationCount != 1 || !generatedDomainColdActivationMatches(group.pins, group.snapshot, groups,
		database.Runtime.RouteActivations) {
		return false, errGeneratedDomainConstructionRefused
	}

	var authorization generatedruntimestate.RouteAccountingHandoffAuthorization
	var handoff generatedruntimestate.RouteAccountingHandoffReceipt
	var completion generatedruntimestate.RouteCompletionReceipt
	authorizationCount, handoffCount, completionCount := 0, 0, 0
	present := 0
	for _, value := range database.Runtime.AccountingHandoffAuthorizations {
		if value.ActivationReceiptID == receiptID || value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			present++
			if value.ActivationReceiptID == receiptID {
				authorization, authorizationCount = value, authorizationCount+1
			}
		}
	}
	for _, value := range database.Runtime.AccountingHandoffReceipts {
		if value.ActivationReceiptID == receiptID || value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			present++
			if value.ActivationReceiptID == receiptID {
				handoff, handoffCount = value, handoffCount+1
			}
		}
	}
	for _, value := range database.Runtime.RouteCompletionReceipts {
		if value.ActivationReceiptID == receiptID || value.SuccessorDeploymentID == activation.SuccessorDeploymentID {
			present++
			if value.ActivationReceiptID == receiptID {
				completion, completionCount = value, completionCount+1
			}
		}
	}
	if present == 0 {
		return false, nil
	}
	if present != 3 || authorizationCount != 1 || handoffCount != 1 || completionCount != 1 {
		return false, errGeneratedDomainConstructionRefused
	}

	predecessorCount := 0
	if activation.PredecessorDeploymentID != "" {
		predecessor, ok := generatedDomainColdGroupByDeployment(groups, activation.PredecessorDeploymentID)
		if !ok {
			return false, errGeneratedDomainConstructionRefused
		}
		predecessorCount = len(predecessor.snapshot.components)
	}
	successorCount := len(group.snapshot.components)
	if authorization.ActivationReceiptID != receiptID || authorization.AppID != activation.AppID ||
		authorization.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
		authorization.PredecessorDeploymentID != activation.PredecessorDeploymentID ||
		authorization.CandidateRosterDigest != activation.CandidateRosterDigest ||
		authorization.PredecessorRosterDigest != activation.PredecessorRosterDigest ||
		authorization.SuccessorComponentCount != successorCount ||
		authorization.PredecessorComponentCount != predecessorCount || authorization.AuthorizedAt.IsZero() ||
		handoff.ActivationReceiptID != authorization.ActivationReceiptID || handoff.AppID != authorization.AppID ||
		handoff.SuccessorDeploymentID != authorization.SuccessorDeploymentID ||
		handoff.PredecessorDeploymentID != authorization.PredecessorDeploymentID ||
		handoff.CandidateRosterDigest != authorization.CandidateRosterDigest ||
		handoff.PredecessorRosterDigest != authorization.PredecessorRosterDigest ||
		handoff.SuccessorComponentCount != authorization.SuccessorComponentCount ||
		handoff.PredecessorComponentCount != authorization.PredecessorComponentCount || handoff.HandedOffAt.IsZero() ||
		authorization.AuthorizedAt.After(handoff.HandedOffAt) {
		return false, errGeneratedDomainConstructionRefused
	}
	if completion.ActivationReceiptID != receiptID || completion.AppID != activation.AppID ||
		completion.SuccessorDeploymentID != activation.SuccessorDeploymentID ||
		completion.CreatorJobID != activation.CreatorJobID ||
		completion.CreatorWorkerAttempt != activation.CreatorWorkerAttempt ||
		activation.WriterJobID != group.pins.jobID() ||
		activation.WriterWorkerAttempt < activation.CreatorWorkerAttempt ||
		activation.WriterWorkerAttempt > completion.CurrentWorkerAttempt ||
		completion.CurrentJobID != group.pins.jobID() || completion.CurrentWorkerAttempt != group.pins.attempt() ||
		completion.CompletionCode != "deployment_completed" || completion.CompletedAt.IsZero() ||
		handoff.HandedOffAt.After(completion.CompletedAt) {
		return false, errGeneratedDomainConstructionRefused
	}

	var main deployments.GeneratedDomainStartupDeployment
	mainCount := 0
	for _, value := range database.GeneratedDeployments {
		if value.ID == group.pins.deploymentID {
			main, mainCount = value, mainCount+1
		}
	}
	if mainCount != 1 || main.AppID != group.pins.applicationID() || main.JobID != group.pins.jobID() ||
		main.Status != deployments.Succeeded || main.JobStatus != "succeeded" || main.JobPhase != "succeeded" ||
		main.JobAttempt != completion.CurrentWorkerAttempt || main.CanonicalSuccessEventCount != 1 ||
		main.SuccessEventCount != 1 || !main.CanonicalSuccessEventAt.Equal(completion.CompletedAt) {
		return false, errGeneratedDomainConstructionRefused
	}
	for _, appID := range database.UnresolvedLifecycleApps {
		if appID == group.pins.applicationID() {
			return false, errGeneratedDomainConstructionRefused
		}
	}
	var binding deployments.GeneratedDomainStartupLifecycleBinding
	bindingCount := 0
	for _, value := range database.LifecycleBindings {
		if value.DeploymentJobID == group.pins.jobID() {
			binding, bindingCount = value, bindingCount+1
		}
	}
	if bindingCount != 1 || binding.AppID != group.pins.applicationID() {
		return false, errGeneratedDomainConstructionRefused
	}
	if binding.Mode == deploymentbinding.ModeLegacyUninitialized {
		if binding.DesiredRevision != 0 || binding.AuthorizingOperationID != "" {
			return false, errGeneratedDomainConstructionRefused
		}
		return true, nil
	}
	if binding.Mode != deploymentbinding.ModeRunning || binding.DesiredRevision < 1 ||
		binding.AuthorizingOperationID == "" {
		return false, errGeneratedDomainConstructionRefused
	}
	var authorizer deployments.GeneratedDomainStartupLifecycleOperation
	operationCount := 0
	for _, operation := range database.LifecycleOperations {
		if operation.AppID == binding.AppID && operation.JobID == binding.AuthorizingOperationID &&
			operation.DesiredRevision == binding.DesiredRevision {
			authorizer, operationCount = operation, operationCount+1
			if !operation.Complete {
				return false, errGeneratedDomainConstructionRefused
			}
		}
	}
	if operationCount != 1 || authorizer.DeploymentID == activation.SuccessorDeploymentID ||
		!generatedDomainColdAuthorizerAncestry(database, groups, group, authorizer.DeploymentID) {
		return false, errGeneratedDomainConstructionRefused
	}
	return true, nil
}

func attestGeneratedDomainColdGroups(ctx context.Context, manager *Manager, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, groups []generatedDomainColdGroup,
) error {
	var retainedGateway generatedDomainColdRetainedGatewayProof
	return attestGeneratedDomainColdGroupsWithRetained(ctx, manager, authority, observed, groups, &retainedGateway)
}

func attestGeneratedDomainColdGroupsWithRetained(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, groups []generatedDomainColdGroup,
	retainedGateway *generatedDomainColdRetainedGatewayProof,
) error {
	var retainedV2 generatedDomainColdRetainedV2Proof
	return attestGeneratedDomainColdGroupsWithRetainedProofs(ctx, manager, authority, observed, groups,
		retainedGateway, &retainedV2)
}

func attestGeneratedDomainColdGroupsWithRetainedProofs(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, groups []generatedDomainColdGroup,
	retainedGateway *generatedDomainColdRetainedGatewayProof,
	retainedV2Proof *generatedDomainColdRetainedV2Proof,
) error {
	pending, found, err := generatedDomainColdPendingActivation(observed.database, observed.protected, groups)
	if err != nil {
		return err
	}
	var evidence *generatedDomainColdPendingActivationEvidence
	if found {
		evidence = &pending
	}
	return attestGeneratedDomainColdGroupsWithPendingProofs(ctx, manager, authority, observed, groups, evidence,
		retainedGateway, retainedV2Proof)
}

func attestGeneratedDomainColdGroupsWithPendingProofs(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, groups []generatedDomainColdGroup,
	pending *generatedDomainColdPendingActivationEvidence,
	retainedGateway *generatedDomainColdRetainedGatewayProof,
	retainedV2Proof *generatedDomainColdRetainedV2Proof,
) error {
	type retirementComponent struct {
		deploymentID, component string
	}
	type expectedObject struct {
		name, id string
		labels   map[string]string
	}
	containers := make(map[string]expectedObject)
	images := make(map[string]expectedObject)
	networks := make(map[string]expectedObject)
	recoverableRetirement := make(map[retirementComponent]struct{})
	for _, group := range groups {
		prefix, found, err := generatedDomainColdPredecessorRetirementPrefix(observed.database, group, groups)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		for _, component := range prefix.predecessorRoster {
			if _, recorded := prefix.componentRetirements[component.ComponentName]; !recorded {
				recoverableRetirement[retirementComponent{deploymentID: component.DeploymentID,
					component: component.ComponentName}] = struct{}{}
			}
		}
	}
	builder, err := attestGeneratedDomainColdBuilder(ctx, manager, authority, observed.docker)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, component := range group.snapshot.components {
			if component.artifact != nil && component.artifact.imageContentID != "" {
				artifact := *component.artifact
				policy := candidatePolicy{identity: generatedcontainer.Identity{
					AppID: group.pins.applicationID(), ReleaseID: group.pins.releaseID,
					DeploymentID: group.pins.deploymentID, ArtifactID: artifact.id,
					DeploymentPlanRevisionID: group.pins.deploymentPlanRevisionID,
					Component:                component.plan.Name, Role: component.plan.Role,
					ImageContentID: artifact.imageContentID}, buildDefinitionDigest: artifact.buildDefinitionDigest}
				if err := authority.attestCandidateImage(ctx, policy); err != nil {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupImageAttestation, err)
				}
				expected := expectedObject{id: artifact.imageContentID,
					labels: generatedDomainColdImageLabels(group.pins, component, artifact)}
				if previous, exists := images[artifact.imageContentID]; exists && !reflect.DeepEqual(previous, expected) {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupStructure,
						errGeneratedDomainConstructionRefused)
				}
				images[artifact.imageContentID] = expected
			}
			policy, hasPolicy := group.policies[component.plan.Name]
			if component.state == generatedruntimestate.ComponentStarting {
				// A name-only create acknowledgement cannot be upgraded to an
				// immutable full Docker identity by reconstruction.
				return generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupStructure,
					errGeneratedDomainConstructionRefused)
			}
			present := component.state == generatedruntimestate.ComponentRunning ||
				component.state == generatedruntimestate.ComponentHealthy ||
				component.state == generatedruntimestate.ComponentActive ||
				component.state == generatedruntimestate.ComponentDraining
			if present {
				if !hasPolicy || !lowerHex(component.containerID, 64) {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupStructure,
						errGeneratedDomainConstructionRefused)
				}
				actual, found, err := authority.inspectCandidate(ctx, component.containerID)
				expectedIdentity := policy.identity
				expectedIdentity.ContainerID = component.containerID
				_, recoveredRetirement := recoverableRetirement[retirementComponent{
					deploymentID: group.pins.deploymentID, component: component.plan.Name}]
				validPhysical := found && generatedcontainer.MatchesRunning(actual, expectedIdentity, policy.limits) &&
					generatedcontainer.CurrentlyRunning(actual)
				if recoveredRetirement {
					validPhysical = !found || generatedcontainer.MatchesConfigured(actual, expectedIdentity, policy.limits) &&
						(generatedcontainer.CurrentlyRunning(actual) || candidateRemovableState(actual))
				}
				if err != nil || !validPhysical ||
					(component.state == generatedruntimestate.ComponentHealthy && actual.Health != "healthy") {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupContainerAttestation,
						errors.Join(errGeneratedDomainConstructionRefused, err))
				}
				if found {
					container := expectedObject{name: policy.identity.ContainerName,
						id: component.containerID, labels: candidatePolicyContainerLabels(policy)}
					if previous, exists := containers[component.containerID]; exists && !reflect.DeepEqual(previous, container) {
						return generatedDomainConstructionBoundaryDiagnostic(
							generatedDomainConstructionStageColdConstruction,
							generatedDomainConstructionReasonColdGroupStructure,
							errGeneratedDomainConstructionRefused)
					}
					containers[component.containerID] = container
				}
				network := expectedObject{name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network",
						"io.rig.application": policy.identity.AppID}}
				if previous, exists := networks[policy.identity.NetworkName]; exists && !reflect.DeepEqual(previous, network) {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupStructure,
						errGeneratedDomainConstructionRefused)
				}
				networks[policy.identity.NetworkName] = network
				if err := authority.attestCandidateNetwork(ctx, policy); err != nil {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupNetworkAttestation, err)
				}
				continue
			}
			if component.containerID != "" || component.containerName != "" {
				identity := component.containerID
				if identity == "" {
					identity = component.containerName
				}
				if _, found, err := authority.inspectCandidate(ctx, identity); err != nil || found {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupComponentAbsence,
						errors.Join(errGeneratedDomainConstructionRefused, err))
				}
			}
		}
	}
	gateway, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
	var reader *generatedDomainStartupReader
	var configObservation *generatedDomainGatewayConfigObservation
	if pending != nil {
		gateway = pending.servingGateway
		if pending.beforeActivation {
			reader, err = newGeneratedDomainColdStartupReader(manager, authority, observed.docker, gateway)
			if err == nil {
				deadline, deadlineSet := ctx.Deadline()
				if !deadlineSet {
					deadline = time.Now().Add(generatedDomainStartupObservationTimeout)
				}
				gateway, configObservation, pending.disposition, err = classifyGeneratedDomainColdPendingGateway(
					ctx, reader, deadline, observed.docker, pending)
			}
			if configObservation != nil {
				defer configObservation.clear()
			}
		} else {
			pending.disposition = generatedDomainColdPendingGatewayProposedExact
		}
	}
	if err != nil {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupGatewaySelection, err)
	}
	retained, err := attestGeneratedDomainColdRetainedGateway(ctx, manager, authority, observed.docker, gateway)
	if err != nil {
		return errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	if retainedGateway == nil {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupStructure,
			errGeneratedDomainConstructionRefused)
	}
	*retainedGateway = cloneGeneratedDomainColdRetainedGatewayProof(retained)
	retainedV2, err := attestGeneratedDomainColdRetainedV2(ctx, manager, observed.docker, gateway)
	if err != nil {
		return errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	if retainedV2Proof == nil {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupStructure,
			errGeneratedDomainConstructionRefused)
	}
	*retainedV2Proof = cloneGeneratedDomainColdRetainedV2Proof(retainedV2)
	seenContainers := make(map[string]struct{}, len(containers))
	seenImages := make(map[string]struct{}, len(images))
	seenNetworks := make(map[string]struct{}, len(networks))
	for _, object := range observed.docker.objects {
		if builder.accounts(object) {
			continue
		}
		if retained.accounts(object) {
			continue
		}
		if retainedV2.accounts(object) {
			continue
		}
		name := object.name
		if names := gateway.names[object.kind]; names != nil {
			if _, ok := names[name]; ok {
				continue
			}
		}
		if ids := gateway.ids[object.kind]; ids != nil && object.id != "" {
			if _, ok := ids[normalizeID(object.id)]; ok {
				continue
			}
		}
		var expected expectedObject
		switch object.kind {
		case "container":
			var ok bool
			expected, ok = containers[object.id]
			if !ok || expected.name != name {
				return generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerCorrespondence,
					errGeneratedDomainConstructionRefused)
			}
			seenContainers[object.id] = struct{}{}
		case "network":
			var ok bool
			expected, ok = networks[name]
			if !ok {
				return generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupNetworkCorrespondence,
					errGeneratedDomainConstructionRefused)
			}
			if object.data.driver != "bridge" || object.data.scope != "local" {
				return generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupNetworkShape,
					errGeneratedDomainConstructionRefused)
			}
			if len(object.data.options) != 0 {
				return generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupNetworkOptions,
					errGeneratedDomainConstructionRefused)
			}
			seenNetworks[name] = struct{}{}
		case "image":
			var ok bool
			expected, ok = images[object.id]
			if !ok || object.data.contentID != object.id {
				return generatedDomainConstructionBoundaryDiagnostic(
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupImageCorrespondence,
					errGeneratedDomainConstructionRefused)
			}
			seenImages[object.id] = struct{}{}
		default:
			return generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdGroupOtherCorrespondence,
				errGeneratedDomainConstructionRefused)
		}
		if !generatedDomainExactRigLabels(object.labels, expected.labels) {
			return generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdGroupLabels,
				errGeneratedDomainConstructionRefused)
		}
	}
	if len(seenContainers) != len(containers) {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupMissingContainer,
			errGeneratedDomainConstructionRefused)
	}
	if len(seenImages) != len(images) {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupMissingImage,
			errGeneratedDomainConstructionRefused)
	}
	if len(seenNetworks) != len(networks) {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupMissingNetwork,
			errGeneratedDomainConstructionRefused)
	}
	if configObservation != nil {
		err = attestGeneratedDomainColdGatewayWithReader(ctx, reader, observed.docker, gateway, configObservation)
	} else {
		err = attestGeneratedDomainColdGateway(ctx, manager, authority, observed.docker, gateway)
	}
	if err != nil {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupGatewayAttestation, err)
	}
	if pending != nil && pending.disposition == generatedDomainColdPendingGatewayUnknown {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupGatewayAttestation, errGeneratedDomainConstructionRefused)
	}
	return nil
}

func attestGeneratedDomainColdGateway(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority, snapshot generatedDomainDockerStartupSnapshot,
	expected generatedDomainGatewayExpectation,
) error {
	if ctx == nil {
		return errGeneratedDomainConstructionRefused
	}
	reader, err := newGeneratedDomainColdStartupReader(manager, authority, snapshot, expected)
	if err != nil {
		return err
	}
	return attestGeneratedDomainColdGatewayWithReader(ctx, reader, snapshot, expected, nil)
}

func attestGeneratedDomainColdGatewayWithReader(ctx context.Context, reader *generatedDomainStartupReader,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
	observation *generatedDomainGatewayConfigObservation,
) error {
	if ctx == nil || reader == nil {
		return errGeneratedDomainConstructionRefused
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(generatedDomainStartupObservationTimeout)
	}
	var attestation generatedDomainGatewayAttestation
	var attestErr error
	switch expected.kind {
	case generatedDomainGatewayNativeV1:
		attestation, attestErr = reader.attestNativeV1Observed(ctx, deadline, snapshot, expected, true, observation)
	case generatedDomainGatewayNativeV2:
		attestation, attestErr = reader.attestNativeV2Observed(ctx, deadline, snapshot, expected, true, observation)
	case generatedDomainGatewayCurrentV1, generatedDomainGatewayCurrentV2:
		attestation, attestErr = reader.attestCurrentObserved(ctx, deadline, snapshot, expected, true, observation)
	default:
		return errGeneratedDomainConstructionRefused
	}
	if attestErr != nil || !attestation.exact || attestation.kind != expected.kind ||
		attestation.stateDigest != expected.stateDigest {
		return errors.Join(errGeneratedDomainConstructionRefused, attestErr)
	}
	return nil
}

func generatedDomainColdImageLabels(pins candidateValidationPins, component candidateAdmissionComponent,
	artifact candidateArtifactSnapshot,
) map[string]string {
	return map[string]string{
		"io.rig.managed": "generated-image", "io.rig.application": pins.applicationID(),
		"io.rig.release": pins.releaseID, "io.rig.artifact": artifact.id,
		"io.rig.plan": pins.deploymentPlanRevisionID, "io.rig.component": component.plan.Name,
		"io.rig.role": component.plan.Role, "io.rig.definition": artifact.buildDefinitionDigest,
	}
}

func snapshotGeneratedDomainColdManifest(authority *GeneratedCandidateAuthority) (generatedDomainColdManifest, error) {
	if authority == nil || authority.ledger == nil || authority.heads == nil {
		return generatedDomainColdManifest{}, deploymentexecution.ErrInvalidConfiguration
	}
	authority.ledger.mu.Lock()
	keys := make([]candidateCapacityGroupKey, 0, len(authority.ledger.groups))
	groups := make(map[candidateCapacityGroupKey]*candidateCapacityGroup, len(authority.ledger.groups))
	for key, group := range authority.ledger.groups {
		keys, groups[key] = append(keys, key), group
	}
	authority.ledger.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].appID != keys[j].appID {
			return keys[i].appID < keys[j].appID
		}
		return keys[i].deploymentID < keys[j].deploymentID
	})
	result := generatedDomainColdManifest{groups: make([]generatedDomainColdManifestGroup, 0, len(keys))}
	for _, key := range keys {
		group := groups[key]
		group.mu.Lock()
		if group.reserving {
			group.mu.Unlock()
			return generatedDomainColdManifest{}, errCandidateCapacityBusy
		}
		item := generatedDomainColdManifestGroup{appID: key.appID, deploymentID: key.deploymentID,
			pins: group.pins, components: make([]generatedDomainColdManifestComponent, 0, len(group.components))}
		for _, name := range group.components {
			component := group.byName[name]
			entry := generatedDomainColdManifestComponent{name: name}
			if component.receipt != nil {
				entry.receiptID, entry.receiptWord = component.receipt.id, component.receipt.state.Load()
			}
			if component.charge != nil {
				entry.charged = component.charge.state == candidateChargeActive && !component.charge.logicallyRetired
				entry.retained = component.charge.receiptRetained || component.charge.explicitRetained
			}
			item.components = append(item.components, entry)
		}
		group.mu.Unlock()
		authority.heads.mu.Lock()
		head := authority.heads.groups[key]
		if head != nil {
			if head.pins.appID != key.appID || head.pins.deploymentID != key.deploymentID {
				authority.heads.mu.Unlock()
				return generatedDomainColdManifest{}, deploymentexecution.ErrIdentityLost
			}
			if head.self != nil {
				copy := *head.self
				item.head = &copy
			} else if head.predecessor != nil {
				copy := *head.predecessor
				item.head = &copy
			}
		}
		authority.heads.mu.Unlock()
		result.groups = append(result.groups, item)
	}
	return result, nil
}
