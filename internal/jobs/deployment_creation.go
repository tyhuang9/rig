package jobs

import (
	"context"
	"database/sql"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

// DeploymentCreateRequest is the only request shape accepted by the ordinary
// deployment admission boundary. The wrapper fixes the durable job and
// resource types so callers cannot create a binding for another job kind.
type DeploymentCreateRequest struct {
	ApplicationID  string
	IdempotencyKey string
	RequestedBy    string
	Input          DeploymentInput
}

// DeploymentCreateHooks keep new admission and historical replay separate.
// Every hook runs in the transaction that owns the job and immutable binding.
// Hooks must not retain tx or perform external I/O.
type DeploymentCreateHooks struct {
	ValidateNew    func(context.Context, *sql.Tx, Job) error
	FinalizeNew    func(context.Context, *sql.Tx, Job, deploymentbinding.Binding) error
	ValidateReplay func(context.Context, *sql.Tx, Job, deploymentbinding.Binding) error
}

type DeploymentCreateResult struct {
	Job     Job
	Binding deploymentbinding.Binding
	Created bool
}

// NewWithDeploymentBindings constructs a service that can admit real ordinary
// deployments. Generic services remain usable for generic job operations, but
// cannot create deployment lifecycle bindings.
func NewWithDeploymentBindings(db *sql.DB, bindings *deploymentbinding.Repository) (*Service, error) {
	if db == nil || bindings == nil || !bindings.UsesDatabase(db) {
		return nil, deploymentbinding.ErrInvalidConfiguration
	}
	service := New(db)
	service.deploymentBindings = bindings
	return service, nil
}

// CreateDeploymentWithInputTransaction atomically creates a deployment job,
// its queued event, immutable lifecycle binding, and caller-owned linkage.
// An exact historical replay validates the retained binding before comparing
// the original request, performs only the replay linkage check, and never
// wakes the worker or refreshes mutable job or binding state.
func (s *Service) CreateDeploymentWithInputTransaction(ctx context.Context, request DeploymentCreateRequest, hooks DeploymentCreateHooks) (DeploymentCreateResult, error) {
	if ctx == nil || s == nil || s.db == nil || s.deploymentBindings == nil || !s.deploymentBindings.UsesDatabase(s.db) {
		return DeploymentCreateResult{}, deploymentbinding.ErrInvalidConfiguration
	}
	create := CreateRequest{
		Type:           "deploy",
		ResourceType:   "application",
		ResourceID:     request.ApplicationID,
		IdempotencyKey: request.IdempotencyKey,
		RequestedBy:    request.RequestedBy,
		Input:          request.Input,
	}
	input, err := marshalInput(request.Input)
	if err != nil {
		return DeploymentCreateResult{}, err
	}
	var binding deploymentbinding.Binding
	result, err := s.createWithInputContext(ctx, create, CreateTransactionHooks{
		Existing: func(ctx context.Context, tx *sql.Tx, existing Job) (bool, error) {
			persisted, readErr := s.deploymentBindings.ReadTx(ctx, tx, existing.ID)
			if readErr != nil {
				return false, readErr
			}
			if !sameCreateRequest(existing, create, input) {
				return false, ErrIdempotency
			}
			if hooks.ValidateReplay != nil {
				if validateErr := hooks.ValidateReplay(ctx, tx, existing, persisted); validateErr != nil {
					return false, validateErr
				}
			}
			binding = persisted
			return true, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, job Job) error {
			if hooks.ValidateNew != nil {
				if validateErr := hooks.ValidateNew(ctx, tx, job); validateErr != nil {
					return validateErr
				}
			}
			persisted, insertErr := s.deploymentBindings.InsertNewTx(ctx, tx, job.ID, job.CreatedAt)
			if insertErr != nil {
				return insertErr
			}
			if hooks.FinalizeNew != nil {
				if finalizeErr := hooks.FinalizeNew(ctx, tx, job, persisted); finalizeErr != nil {
					return finalizeErr
				}
			}
			binding = persisted
			return nil
		},
	})
	if err != nil {
		return DeploymentCreateResult{}, err
	}
	return DeploymentCreateResult{Job: result.Job, Binding: binding, Created: result.Created}, nil
}
