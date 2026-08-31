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

// RepositoryAuthorizationGate revalidates immutable deployment/plan identity
// and uses the deployment repository's policy transaction to move the main
// deployment to Applying immediately before the first Docker mutation.
type RepositoryAuthorizationGate struct {
	deployments authorizationStore
	plans       planReader
}

func NewAuthorizationGate(deploymentRepository authorizationStore, plans planReader) (*RepositoryAuthorizationGate, error) {
	if deploymentRepository == nil || plans == nil {
		return nil, errors.New("generated authorization dependencies are required")
	}
	return &RepositoryAuthorizationGate{deployments: deploymentRepository, plans: plans}, nil
}

func (g *RepositoryAuthorizationGate) Authorize(ctx context.Context, request AuthorizationRequest) error {
	if g == nil || ctx == nil || uuid.Validate(request.AppID) != nil || uuid.Validate(request.DeploymentID) != nil ||
		uuid.Validate(request.DeploymentPlanRevisionID) != nil || request.ReleaseID == "" || request.DeploymentPlanRevisionNumber < 1 ||
		(request.CandidateSlot != generatedruntime.SlotBlue && request.CandidateSlot != generatedruntime.SlotGreen) || request.ComponentCount < 1 || request.ComponentCount > 2 {
		return errors.New("generated authorization request is invalid")
	}
	deployment, err := g.deployments.Get(ctx, request.AppID, request.DeploymentID)
	if err != nil || !deployment.ProvenanceInitialized || deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode ||
		deployment.ReleaseID != request.ReleaseID || deployment.DeploymentPlanRevisionID != request.DeploymentPlanRevisionID ||
		deployment.DeploymentPlanRevisionNumber != request.DeploymentPlanRevisionNumber {
		return errors.New("generated deployment provenance is unavailable")
	}
	plan, err := g.plans.GetRevision(ctx, request.AppID, request.DeploymentPlanRevisionID, request.DeploymentPlanRevisionNumber)
	if err != nil || plan.ID != request.DeploymentPlanRevisionID || plan.AppID != request.AppID || plan.RevisionNumber != request.DeploymentPlanRevisionNumber ||
		plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode || len(plan.Plan.Components) != request.ComponentCount {
		return errors.New("generated deployment plan is unavailable")
	}
	if plan.Plan.Migration != nil && plan.Plan.Migration.Approval.Status != deploymentplans.MigrationApprovalApproved {
		return deployments.ErrApprovalRequired
	}
	switch deployment.Status {
	case deployments.Applying, deployments.WaitingHealth:
		return nil
	case deployments.Preparing, deployments.NeedsAttention:
		return g.deployments.Gate(ctx, request.AppID, request.DeploymentID, nil)
	default:
		return deployments.ErrInvalidTransition
	}
}
