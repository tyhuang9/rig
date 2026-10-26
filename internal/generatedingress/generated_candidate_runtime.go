package generatedingress

import (
	"context"
	"errors"
	"sync"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GeneratedRetainedCapacityReservation is an opaque process-local charge in
// the domain candidate owner's single raw budget. The runtime can retain the
// handle but cannot inspect, copy, or release its underlying capacity leases.
type GeneratedRetainedCapacityReservation interface {
	generatedRetainedCapacityReservation()
}

type generatedRetainedCapacityReservation struct {
	owner     *GeneratedCandidateAuthority
	operation string
	leases    []*generatedcapacity.Lease

	mu       sync.Mutex
	released bool
}

func (*generatedRetainedCapacityReservation) generatedRetainedCapacityReservation() {}

// ValidateRuntimeOwner binds runtime process use to the exact dependencies
// and fixed resource policy retained by the domain candidate owner. It does
// not expose those dependencies or grant a separate construction path.
func (a *GeneratedCandidateAuthority) ValidateRuntimeOwner(process runtimeprocess.CommandRunner,
	environment GeneratedCandidateEnvironmentStager, options GeneratedCandidateProcessOptions,
) error {
	if !validGeneratedCandidateAuthority(a) ||
		!generatedCandidatePointerIdentity(process) || !generatedCandidatePointerIdentity(environment) ||
		!validGeneratedCandidateProcessOptions(options) ||
		!sameGeneratedPointer(a.process, process) || !sameGeneratedPointer(a.environment, environment) ||
		a.options != options {
		return deploymentexecution.ErrInvalidConfiguration
	}
	return nil
}

// ReserveRetainedCapacity atomically charges one fixed candidate-policy share
// for each definitely stopped retained component. Physical inspection and the
// attempted/unattempted release rules remain owned by generatedruntime.
func (a *GeneratedCandidateAuthority) ReserveRetainedCapacity(ctx context.Context, operationID string,
	stoppedCount int,
) (GeneratedRetainedCapacityReservation, error) {
	if ctx == nil || !validGeneratedCandidateAuthority(a) ||
		!candidatePolicyText(operationID, 256) || stoppedCount < 1 || stoppedCount > 2 {
		return nil, generatedcapacity.ErrInvalidCount
	}
	var borrow *generatedDomainCapacityBorrow
	if a.coordinator != nil {
		var borrowErr error
		borrow, borrowErr = a.coordinator.borrowAcquire()
		if borrowErr != nil {
			return nil, borrowErr
		}
		defer borrow.release()
	}
	leases, err := a.budget.Acquire(ctx, generatedcapacity.Limits{
		MemoryBytes: uint64(a.options.MemoryBytes),
		DiskBytes:   a.options.ReplacementDiskBytes,
	}, stoppedCount)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		for _, lease := range leases {
			lease.Release()
		}
		return nil, ctx.Err()
	}
	return &generatedRetainedCapacityReservation{
		owner: a, operation: operationID, leases: leases,
	}, nil
}

// ReleaseRetainedCapacity releases an exact opaque charge once. The caller
// must first prove its retained-runtime attempted/unattempted release rule.
func (a *GeneratedCandidateAuthority) ReleaseRetainedCapacity(operationID string,
	reservation GeneratedRetainedCapacityReservation,
) error {
	value, ok := reservation.(*generatedRetainedCapacityReservation)
	if !ok || value == nil || !validGeneratedCandidateAuthority(a) || value.owner != a || value.operation != operationID {
		return deploymentexecution.ErrInvalidConfiguration
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.released {
		return deploymentexecution.ErrInvalidConfiguration
	}
	value.released = true
	for _, lease := range value.leases {
		lease.Release()
	}
	value.leases = nil
	return nil
}

// PinGeneratedNetwork retains the sealed deployment repository behind the
// domain candidate owner while preserving its concrete execution guard.
func (a *GeneratedCandidateAuthority) PinGeneratedNetwork(ctx context.Context,
	request deployments.GeneratedNetworkRequest,
) (*deployments.GeneratedNetworkExecution, error) {
	if !validGeneratedCandidateAuthority(a) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return a.stores.Repository().PinGeneratedNetwork(ctx, request)
}

// CheckLegacyGeneratedCandidateRemoval keeps the legacy receipt exclusion on
// the same sealed repository family as candidate creation.
func (a *GeneratedCandidateAuthority) CheckLegacyGeneratedCandidateRemoval(ctx context.Context,
	appID, deploymentID, component, containerName, containerID string,
) error {
	if !validGeneratedCandidateAuthority(a) {
		return errors.New("generated candidate runtime owner is unavailable")
	}
	return a.stores.Repository().CheckLegacyGeneratedCandidateRemoval(ctx, appID, deploymentID,
		component, containerName, containerID)
}
