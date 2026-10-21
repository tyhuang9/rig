package generatedingress

import "github.com/hostd/hostd/internal/deployments"

// These aliases preserve the exact deployment-store values consumed by the
// relocated candidate owner. They add no writer or physical-effect authority.
type (
	Finding         = deployments.Finding
	Status          = deployments.Status
	RuntimeStrategy = deployments.RuntimeStrategy
)

const (
	Preparing            = deployments.Preparing
	Applying             = deployments.Applying
	WaitingHealth        = deployments.WaitingHealth
	Succeeded            = deployments.Succeeded
	Failed               = deployments.Failed
	Cancelled            = deployments.Cancelled
	NeedsAttention       = deployments.NeedsAttention
	RuntimeCompose       = deployments.RuntimeCompose
	RuntimeGeneratedNode = deployments.RuntimeGeneratedNode
)

var (
	ErrNotFound           = deployments.ErrNotFound
	ErrInvalidTransition  = deployments.ErrInvalidTransition
	ErrInvalidDeployment  = deployments.ErrInvalidDeployment
	ErrApprovalRequired   = deployments.ErrApprovalRequired
	ErrRejectedCapability = deployments.ErrRejectedCapability
)
