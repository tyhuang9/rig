package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"time"

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
	pins       candidateValidationPins
	protected  candidateProtectedSnapshot
	snapshot   candidateAdmissionSnapshot
	kind       generatedDomainColdGroupKind
	policies   map[string]candidatePolicy
	receipts   map[string]candidateColdReceiptSeed
	bindHead   bool
	selfHead   bool
	legacyHead bool
}

type generatedDomainColdManifest struct {
	groups []generatedDomainColdManifestGroup
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

func sameGeneratedDomainColdManifest(left, right *generatedDomainColdManifest) bool {
	return reflect.DeepEqual(left, right)
}

func cloneGeneratedDomainColdManifest(value generatedDomainColdManifest) *generatedDomainColdManifest {
	result := &generatedDomainColdManifest{groups: make([]generatedDomainColdManifestGroup, len(value.groups))}
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
	// Persistent builder identity and physical ownership are part of the host
	// census, not application capacity. Refuse incomplete or crossed builder
	// state before installing any cold application accounting tombstones.
	if _, err := attestGeneratedDomainColdBuilder(ctx, result.manager, result.candidates, observed.docker); err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdBuilderAttestation, err)
	}
	for index := range groups {
		group := &groups[index]
		spec := generatedDomainColdGroupSpec(result.candidates, *group)
		if group.kind == generatedDomainColdRisky {
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
		}
		if err := result.candidates.ledger.seedColdOwnership(spec, group.receipts); err != nil {
			return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdOwnership, err)
		}
		if group.bindHead {
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
	}
	if err := attestGeneratedDomainColdGroups(ctx, result.manager, result.candidates, observed, groups); err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupAttestation, err)
	}
	manifest, err := snapshotGeneratedDomainColdManifest(result.candidates)
	if err != nil {
		return generatedDomainColdManifest{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdManifest, err)
	}
	return manifest, nil
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
	groups, err := readGeneratedDomainColdGroups(ctx, authority, observed)
	if err != nil {
		return err
	}
	if err := attestGeneratedDomainColdGroups(ctx, manager, authority, observed, groups); err != nil {
		return err
	}
	current, err := snapshotGeneratedDomainColdManifest(authority)
	if err != nil || !reflect.DeepEqual(expected, current) {
		return errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	return nil
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

func readGeneratedDomainColdGroups(ctx context.Context, authority *GeneratedCandidateAuthority,
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
			groups[index].pins, groups[index].snapshot, groups, observed.database.Runtime.RouteActivations)
		if err != nil {
			return nil, err
		}
		groups[index].kind, groups[index].bindHead = kind, bindHead
		groups[index].selfHead, groups[index].legacyHead = selfHead, legacyHead
	}
	if err := validateGeneratedDomainColdGatewayRoutes(observed, groups); err != nil {
		return nil, err
	}
	return groups, nil
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
	gateway, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
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
		if len(group.snapshot.components) == 0 {
			return errGeneratedDomainConstructionRefused
		}
		actual := routeRecord{Slot: generatedidentity.Slot(group.snapshot.pins.candidateSlot),
			Endpoints: make([]generatedroute.RouteEndpoint, 0, len(group.snapshot.components))}
		for _, component := range group.snapshot.components {
			policy, ok := group.policies[component.plan.Name]
			if !ok {
				return errGeneratedDomainConstructionRefused
			}
			actual.Endpoints = append(actual.Endpoints, generatedroute.RouteEndpoint{
				Component: component.plan.Name, Role: component.plan.Role,
				ContainerID: component.containerID, NetworkName: policy.identity.NetworkName,
				NetworkAlias: policy.identity.NetworkAlias,
				InternalPort: component.plan.InternalPort,
			})
		}
		sort.Slice(actual.Endpoints, func(i, j int) bool {
			return actual.Endpoints[i].Component < actual.Endpoints[j].Component
		})
		if validateRoute(actual) != nil || !sameRoute(actual, want.route) {
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
		snapshot.lifecycleRetained || len(snapshot.components) != len(protected.components) {
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
	activations []generatedruntimestate.GeneratedDomainStartupRouteActivation,
) (generatedDomainColdGroupKind, bool, bool, bool, error) {
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
			value.CreatorWorkerAttempt > pins.attempt() ||
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

func attestGeneratedDomainColdGroups(ctx context.Context, manager *Manager, authority *GeneratedCandidateAuthority,
	observed generatedDomainStartupObservation, groups []generatedDomainColdGroup,
) error {
	type expectedObject struct {
		name, id string
		labels   map[string]string
	}
	containers := make(map[string]expectedObject)
	images := make(map[string]expectedObject)
	networks := make(map[string]expectedObject)
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
					return errGeneratedDomainConstructionRefused
				}
				images[artifact.imageContentID] = expected
			}
			policy, hasPolicy := group.policies[component.plan.Name]
			if component.state == generatedruntimestate.ComponentStarting {
				// A name-only create acknowledgement cannot be upgraded to an
				// immutable full Docker identity by reconstruction.
				return errGeneratedDomainConstructionRefused
			}
			present := component.state == generatedruntimestate.ComponentRunning ||
				component.state == generatedruntimestate.ComponentHealthy ||
				component.state == generatedruntimestate.ComponentActive ||
				component.state == generatedruntimestate.ComponentDraining
			if present {
				if !hasPolicy || !lowerHex(component.containerID, 64) {
					return errGeneratedDomainConstructionRefused
				}
				actual, found, err := authority.inspectCandidate(ctx, component.containerID)
				expectedIdentity := policy.identity
				expectedIdentity.ContainerID = component.containerID
				if err != nil || !found || !generatedcontainer.MatchesRunning(actual, expectedIdentity, policy.limits) ||
					!generatedcontainer.CurrentlyRunning(actual) ||
					(component.state == generatedruntimestate.ComponentHealthy && actual.Health != "healthy") {
					return generatedDomainConstructionBoundaryDiagnostic(
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupContainerAttestation,
						errors.Join(errGeneratedDomainConstructionRefused, err))
				}
				container := expectedObject{name: policy.identity.ContainerName,
					id: component.containerID, labels: generatedDomainColdContainerLabels(policy.identity)}
				if previous, exists := containers[component.containerID]; exists && !reflect.DeepEqual(previous, container) {
					return errGeneratedDomainConstructionRefused
				}
				containers[component.containerID] = container
				network := expectedObject{name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network",
						"io.rig.application": policy.identity.AppID}}
				if previous, exists := networks[policy.identity.NetworkName]; exists && !reflect.DeepEqual(previous, network) {
					return errGeneratedDomainConstructionRefused
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
					return errors.Join(errGeneratedDomainConstructionRefused, err)
				}
			}
		}
	}
	gateway, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
	if err != nil {
		return err
	}
	seenContainers := make(map[string]struct{}, len(containers))
	seenImages := make(map[string]struct{}, len(images))
	seenNetworks := make(map[string]struct{}, len(networks))
	for _, object := range observed.docker.objects {
		if builder.accounts(object) {
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
		var ok bool
		switch object.kind {
		case "container":
			expected, ok = containers[object.id]
			ok = ok && expected.name == name
			if ok {
				seenContainers[object.id] = struct{}{}
			}
		case "network":
			expected, ok = networks[name]
			ok = ok && object.data.driver == "bridge" && object.data.scope == "local" && len(object.data.options) == 0
			if ok {
				seenNetworks[name] = struct{}{}
			}
		case "image":
			expected, ok = images[object.id]
			ok = ok && object.data.contentID == object.id
			if ok {
				seenImages[object.id] = struct{}{}
			}
		default:
			ok = false
		}
		if !ok || !generatedDomainExactRigLabels(object.labels, expected.labels) {
			return generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdGroupCensus,
				errGeneratedDomainConstructionRefused)
		}
	}
	if len(seenContainers) != len(containers) || len(seenImages) != len(images) || len(seenNetworks) != len(networks) {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupCensus,
			errGeneratedDomainConstructionRefused)
	}
	if err := attestGeneratedDomainColdGateway(ctx, manager, authority, observed.docker, gateway); err != nil {
		return generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdGroupGatewayAttestation, err)
	}
	return nil
}

func attestGeneratedDomainColdGateway(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority, snapshot generatedDomainDockerStartupSnapshot,
	expected generatedDomainGatewayExpectation,
) error {
	if ctx == nil || manager == nil || authority == nil || manager.runner != authority.process ||
		expected.kind == generatedDomainGatewayNone || snapshot.daemonID == "" ||
		manager.options.DockerExecutable != authority.options.DockerExecutable ||
		manager.options.DockerEndpoint != authority.options.DockerEndpoint ||
		manager.options.DockerConfigDirectory != authority.options.DockerConfigDirectory ||
		manager.options.WorkingDirectory != authority.options.WorkingDirectory ||
		manager.options.CommandTimeout != authority.options.CommandTimeout ||
		manager.options.OutputLimit != authority.options.OutputLimit {
		return errGeneratedDomainConstructionRefused
	}
	reader := &generatedDomainStartupReader{runner: manager.runner,
		options: generatedDomainOptions{DockerExecutable: manager.options.DockerExecutable,
			DockerEndpoint:        manager.options.DockerEndpoint,
			DockerConfigDirectory: manager.options.DockerConfigDirectory,
			WorkingDirectory:      manager.options.WorkingDirectory,
			DataRoot:              manager.options.DataRoot, HostPort: manager.options.HostPort,
			CommandTimeout: manager.options.CommandTimeout, PullTimeout: manager.options.PullTimeout,
			OutputLimit: manager.options.OutputLimit},
		environment: append([]string(nil), manager.dockerEnv...), daemonID: snapshot.daemonID,
		expectation: expected}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(generatedDomainStartupObservationTimeout)
	}
	var attestation generatedDomainGatewayAttestation
	var err error
	switch expected.kind {
	case generatedDomainGatewayNativeV1:
		attestation, err = reader.attestNativeV1(ctx, deadline, snapshot, expected, true)
	case generatedDomainGatewayNativeV2:
		attestation, err = reader.attestNativeV2(ctx, deadline, snapshot, expected, true)
	case generatedDomainGatewayCurrentV1, generatedDomainGatewayCurrentV2:
		attestation, err = reader.attestCurrent(ctx, deadline, snapshot, expected, true)
	default:
		return errGeneratedDomainConstructionRefused
	}
	if err != nil || !attestation.exact || attestation.kind != expected.kind ||
		attestation.stateDigest != expected.stateDigest {
		return errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	return nil
}

func generatedDomainColdContainerLabels(identity generatedcontainer.Identity) map[string]string {
	return map[string]string{
		"io.rig.managed": "generated-runtime", "io.rig.application": identity.AppID,
		"io.rig.release": identity.ReleaseID, "io.rig.deployment": identity.DeploymentID,
		"io.rig.artifact": identity.ArtifactID, "io.rig.plan": identity.DeploymentPlanRevisionID,
		"io.rig.component": identity.Component, "io.rig.slot": identity.Slot,
		"io.rig.role": identity.Role, generatedcontainer.ReceiptLabel: identity.ReceiptID,
	}
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
