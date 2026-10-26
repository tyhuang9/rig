package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// AttestPublished freshly proves that the exact receipt-backed active head is
// still the route served by the generated gateway. It is read-only and holds
// the Manager's gateway locks across physical observation and durable checks.
func (a *GeneratedRouteAuthority) AttestPublished(ctx context.Context, claim deploymentexecution.Claim, deploymentID string) error {
	if !a.configured() || ctx == nil || claim.ApplicationID() == "" || !validAppID(deploymentID) {
		return deploymentexecution.ErrInvalidConfiguration
	}
	return a.attestGeneratedRoute(ctx, claim, deploymentID, generatedruntimestate.PhaseDraining, nil,
		func(ctx context.Context, tx *sql.Tx) error {
			return a.stores.ExecutionGuard().CheckPublishedRouteAttestationTx(ctx, tx, claim, deploymentID)
		})
}

func (a *GeneratedRouteAuthority) readPublishedRouteAttestation(ctx context.Context, claim deploymentexecution.Claim, deploymentID string) (routeRecord, error) {
	return a.readGeneratedRouteAttestation(ctx, claim, deploymentID, generatedruntimestate.PhaseDraining, nil,
		func(ctx context.Context, tx *sql.Tx) error {
			return a.stores.ExecutionGuard().CheckPublishedRouteAttestationTx(ctx, tx, claim, deploymentID)
		})
}

type generatedRouteAttestationGuard func(context.Context, *sql.Tx) error

func (a *GeneratedRouteAuthority) attestRecoveredRouteCompletion(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	completion generatedruntimestate.RouteCompletionReceipt,
) error {
	if pinned == nil || activation.ReceiptID == "" || completion.ActivationReceiptID != activation.ReceiptID {
		return deploymentexecution.ErrInvalidConfiguration
	}
	return a.attestGeneratedRoute(ctx, claim, activation.SuccessorDeploymentID,
		generatedruntimestate.PhaseSucceeded, &activation, func(_ context.Context, tx *sql.Tx) error {
			return pinned.CheckGeneratedRouteCompletionReplayTx(tx, claim, activation, completion)
		})
}

func (a *GeneratedRouteAuthority) attestGeneratedRoute(ctx context.Context, claim deploymentexecution.Claim,
	deploymentID string, phase generatedruntimestate.Phase,
	activation *generatedruntimestate.RouteActivationReceipt, guard generatedRouteAttestationGuard,
) error {
	if guard == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	appID := claim.ApplicationID()
	return a.manager.WithObservation(ctx, appID, func(observationContext context.Context, observation Observation) error {
		expected, err := a.readGeneratedRouteAttestation(observationContext, claim, deploymentID,
			phase, activation, guard)
		if err != nil {
			return err
		}
		if !sameRoute(expected, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
			return errors.Join(deploymentexecution.ErrAuthorityStale, generatedruntimestate.ErrConflict)
		}
		return nil
	})
}

func (a *GeneratedRouteAuthority) readGeneratedRouteAttestation(ctx context.Context,
	claim deploymentexecution.Claim, deploymentID string, expectedPhase generatedruntimestate.Phase,
	expectedActivation *generatedruntimestate.RouteActivationReceipt, guard generatedRouteAttestationGuard,
) (routeRecord, error) {
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return routeRecord{}, err
	}
	defer tx.Rollback()
	if guard == nil {
		return routeRecord{}, deploymentexecution.ErrInvalidConfiguration
	}
	if err := guard(ctx, tx); err != nil {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	appID := claim.ApplicationID()
	head, err := readGeneratedRouteActivationHeadTx(ctx, tx, appID)
	if err != nil || head.DeploymentID != deploymentID || head.ActivationReceiptID == "" {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	receipt, err := readGeneratedRouteActivationReceiptTx(ctx, tx, head.ActivationReceiptID)
	if err != nil || receipt.AppID != appID || receipt.SuccessorDeploymentID != deploymentID ||
		receipt.SuccessorReleaseID != head.ReleaseID || receipt.SuccessorSlot != head.Slot ||
		receipt.SuccessorGeneration != head.Generation || receipt.CreatorJobID != claim.JobID() ||
		receipt.CreatorWorkerAttempt < 1 || receipt.CreatorWorkerAttempt > claim.Attempt() ||
		expectedActivation != nil && receipt != *expectedActivation {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	var releaseID, planID, slot, previousDeploymentID, previousSlot, deploymentJobID string
	var planNumber int64
	var phase generatedruntimestate.Phase
	err = tx.QueryRowContext(ctx, `SELECT runtime.release_id,runtime.deployment_plan_revision_id,
		runtime.deployment_plan_revision_number,runtime.candidate_slot,
		COALESCE(runtime.previous_active_deployment_id,''),COALESCE(runtime.previous_active_slot,''),
		runtime.phase,base.job_id
		FROM generated_runtime_deployments runtime
		JOIN deployments base ON base.id=runtime.deployment_id AND base.app_id=runtime.app_id
		WHERE runtime.app_id=? AND runtime.deployment_id=?`, appID, deploymentID).
		Scan(&releaseID, &planID, &planNumber, &slot, &previousDeploymentID, &previousSlot, &phase, &deploymentJobID)
	if err != nil || releaseID != receipt.SuccessorReleaseID || slot != receipt.SuccessorSlot ||
		previousDeploymentID != receipt.PredecessorDeploymentID || previousSlot != receipt.PredecessorSlot ||
		phase != expectedPhase || deploymentJobID != claim.JobID() {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	roster, err := readGeneratedRouteActivationRosterTx(ctx, tx, deploymentID, generatedruntimestate.ComponentActive)
	canonicalRoster, validRoster := canonicalGeneratedRouteActivationRoster(roster)
	if err != nil || !validRoster || len(canonicalRoster) == 0 ||
		generatedRouteActivationRosterDigest(canonicalRoster) != receipt.CandidateRosterDigest {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	if err := guard(ctx, tx); err != nil {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	if err := tx.Commit(); err != nil {
		return routeRecord{}, err
	}

	plan, err := a.stores.Plans().GetRevision(ctx, appID, planID, planNumber)
	if err != nil || plan.ID != planID || plan.RevisionNumber != planNumber ||
		plan.State != deploymentplans.RevisionAccepted && plan.State != deploymentplans.RevisionSuperseded ||
		plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode ||
		len(plan.Plan.Components) != len(canonicalRoster) {
		return routeRecord{}, publishedRouteAttestationError(err)
	}
	components := make(map[string]deploymentplans.Component, len(plan.Plan.Components))
	for _, component := range plan.Plan.Components {
		if _, exists := components[component.Name]; exists {
			return routeRecord{}, publishedRouteAttestationError(nil)
		}
		components[component.Name] = component
	}
	identitySlot := generatedidentity.Slot(receipt.SuccessorSlot)
	endpoints := make([]generatedroute.RouteEndpoint, 0, len(canonicalRoster))
	for _, member := range canonicalRoster {
		component, exists := components[member.ComponentName]
		if !exists || member.Slot != receipt.SuccessorSlot ||
			member.ContainerName != generatedidentity.ContainerName(appID, member.ComponentName, identitySlot) {
			return routeRecord{}, publishedRouteAttestationError(nil)
		}
		endpoints = append(endpoints, generatedroute.RouteEndpoint{
			Component: member.ComponentName, Role: component.Role, ContainerID: member.ContainerID,
			NetworkName:  generatedidentity.NetworkName(appID),
			NetworkAlias: generatedidentity.ContainerAlias(member.ComponentName, identitySlot),
			InternalPort: component.InternalPort,
		})
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Component < endpoints[j].Component })
	return routeRecord{Slot: generatedidentity.Slot(receipt.SuccessorSlot), Endpoints: endpoints}, nil
}

func publishedRouteAttestationError(err error) error {
	return errors.Join(deploymentexecution.ErrAuthorityStale, generatedruntimestate.ErrConflict, err)
}
