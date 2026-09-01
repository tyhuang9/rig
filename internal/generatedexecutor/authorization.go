package generatedexecutor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type authorizationStore interface {
	Get(context.Context, string, string) (deployments.Deployment, error)
	Gate(context.Context, string, string, []deployments.Finding) error
}

type replacementReserver interface {
	ReserveReplacement(context.Context, int) (generatedruntime.ReplacementReservation, error)
}

// RepositoryAuthorizationGate revalidates immutable deployment/plan identity
// and uses the deployment repository's policy transaction to move the main
// deployment to Applying immediately before the first Docker mutation.
type RepositoryAuthorizationGate struct {
	deployments authorizationStore
	plans       planReader
	capacity    replacementReserver
}

func NewAuthorizationGate(deploymentRepository authorizationStore, plans planReader, capacity replacementReserver) (*RepositoryAuthorizationGate, error) {
	if deploymentRepository == nil || plans == nil || capacity == nil {
		return nil, errors.New("generated authorization dependencies are required")
	}
	return &RepositoryAuthorizationGate{deployments: deploymentRepository, plans: plans, capacity: capacity}, nil
}

func (g *RepositoryAuthorizationGate) Authorize(ctx context.Context, request AuthorizationRequest) (generatedruntime.ReplacementReservation, error) {
	if g == nil || ctx == nil || uuid.Validate(request.AppID) != nil || uuid.Validate(request.DeploymentID) != nil ||
		uuid.Validate(request.DeploymentPlanRevisionID) != nil || request.ReleaseID == "" || request.DeploymentPlanRevisionNumber < 1 ||
		(request.CandidateSlot != generatedruntime.SlotBlue && request.CandidateSlot != generatedruntime.SlotGreen) || request.ComponentCount < 1 || request.ComponentCount > 2 {
		return nil, errors.New("generated authorization request is invalid")
	}
	deployment, err := g.deployments.Get(ctx, request.AppID, request.DeploymentID)
	if err != nil || !deployment.ProvenanceInitialized || deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode ||
		deployment.ReleaseID != request.ReleaseID || deployment.DeploymentPlanRevisionID != request.DeploymentPlanRevisionID ||
		deployment.DeploymentPlanRevisionNumber != request.DeploymentPlanRevisionNumber {
		return nil, errors.New("generated deployment provenance is unavailable")
	}
	plan, err := g.plans.GetRevision(ctx, request.AppID, request.DeploymentPlanRevisionID, request.DeploymentPlanRevisionNumber)
	if err != nil || plan.ID != request.DeploymentPlanRevisionID || plan.AppID != request.AppID || plan.RevisionNumber != request.DeploymentPlanRevisionNumber ||
		plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode || len(plan.Plan.Components) != request.ComponentCount {
		return nil, errors.New("generated deployment plan is unavailable")
	}
	if plan.Plan.Migration != nil && plan.Plan.Migration.Approval.Status != deploymentplans.MigrationApprovalApproved {
		return nil, deployments.ErrApprovalRequired
	}
	reservation, err := g.capacity.ReserveReplacement(ctx, request.ComponentCount)
	if generatedruntime.IsCode(err, generatedruntime.DiagnosticInsufficientReplacementSpace) {
		return nil, ErrInsufficientReplacementCapacity
	}
	if err != nil {
		return nil, err
	}
	if reservation == nil {
		return nil, errors.New("generated replacement reservation is unavailable")
	}
	releaseOnFailure := true
	defer func() {
		if releaseOnFailure {
			reservation.Release()
		}
	}()
	switch deployment.Status {
	case deployments.Applying, deployments.WaitingHealth:
		releaseOnFailure = false
		return reservation, nil
	case deployments.Preparing, deployments.NeedsAttention:
		if err = g.deployments.Gate(ctx, request.AppID, request.DeploymentID, nil); err != nil {
			return nil, err
		}
		releaseOnFailure = false
		return reservation, nil
	default:
		return nil, deployments.ErrInvalidTransition
	}
}
