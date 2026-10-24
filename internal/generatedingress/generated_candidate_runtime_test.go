package generatedingress

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
)

func TestGeneratedCandidateRuntimeOwnerRequiresExactAuthorityAndComposition(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	authority := fixture.authority
	if err := authority.ValidateRuntimeOwner(fixture.runner, fixture.environment, fixture.options); err != nil {
		t.Fatalf("exact runtime owner validation: %v", err)
	}

	foreignProcess := &candidateOwnerRunner{}
	foreignEnvironment := &candidateOwnerEnvironment{}
	changedOptions := fixture.options
	changedOptions.MemoryBytes++
	for _, test := range []struct {
		name        string
		process     *candidateOwnerRunner
		environment *candidateOwnerEnvironment
		options     GeneratedCandidateProcessOptions
	}{
		{name: "foreign process", process: foreignProcess, environment: fixture.environment, options: fixture.options},
		{name: "foreign environment", process: fixture.runner, environment: foreignEnvironment, options: fixture.options},
		{name: "changed options", process: fixture.runner, environment: fixture.environment, options: changedOptions},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := authority.ValidateRuntimeOwner(test.process, test.environment, test.options); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
				t.Fatalf("runtime owner validation error=%v", err)
			}
		})
	}

	copy := *authority
	if err := copy.ValidateRuntimeOwner(fixture.runner, fixture.environment, fixture.options); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("shallow authority copy validated as the runtime owner: %v", err)
	}
	if reservation, err := copy.ReserveRetainedCapacity(context.Background(),
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1); err == nil || reservation != nil {
		if reservation != nil {
			_ = copy.ReleaseRetainedCapacity("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", reservation)
		}
		t.Fatalf("shallow authority copy reserved retained capacity: reservation=%T error=%v", reservation, err)
	}
	request := fixture.validation.request
	network := deployments.GeneratedNetworkRequest{
		Claim: request.Claim, DeploymentID: request.DeploymentID, ReleaseID: request.ReleaseID,
		ConfigurationRevisionID:      request.ConfigurationRevisionID,
		ConfigurationRevisionNumber:  request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID:     request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: request.DeploymentPlanRevisionNumber,
	}
	if execution, err := copy.PinGeneratedNetwork(context.Background(), network); execution != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("shallow authority copy pinned generated network: execution=%T error=%v", execution, err)
	}
}

func TestGeneratedCandidateRetainedCapacityEnforcesOwnerOperationAndOnceRelease(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	foreign := newCandidateOwnerFixture(t)
	const operation = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	reservation, err := fixture.authority.ReserveRetainedCapacity(context.Background(), operation, 1)
	if err != nil {
		t.Fatal(err)
	}
	alias := reservation
	copy := *fixture.authority
	if err := foreign.authority.ReleaseRetainedCapacity(operation, reservation); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("foreign owner release error=%v", err)
	}
	if err := copy.ReleaseRetainedCapacity(operation, reservation); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("shallow authority copy released original charge: %v", err)
	}
	if err := fixture.authority.ReleaseRetainedCapacity("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", reservation); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("crossed operation release error=%v", err)
	}
	if err := fixture.authority.ReleaseRetainedCapacity(operation, alias); err != nil {
		t.Fatalf("public interface alias release: %v", err)
	}
	if err := fixture.authority.ReleaseRetainedCapacity(operation, reservation); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("repeated release error=%v", err)
	}

	for _, test := range []struct {
		name      string
		ctx       context.Context
		operation string
		count     int
	}{
		{name: "nil context", operation: operation, count: 1},
		{name: "empty operation", ctx: context.Background(), count: 1},
		{name: "zero count", ctx: context.Background(), operation: operation},
		{name: "count above fixed roster maximum", ctx: context.Background(), operation: operation, count: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			reservation, err := fixture.authority.ReserveRetainedCapacity(test.ctx, test.operation, test.count)
			if reservation != nil || !errors.Is(err, generatedcapacity.ErrInvalidCount) {
				t.Fatalf("invalid reservation=%T error=%v", reservation, err)
			}
		})
	}
}

func TestGeneratedCandidateRetainedCapacityUsesFixedLimitsAndSharedReplacementBudget(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	const operation = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	memory := uint64(fixture.options.MemoryBytes)
	disk := fixture.options.ReplacementDiskBytes

	fixture.source.mu.Lock()
	fixture.source.value = generatedcapacity.Snapshot{MemoryAvailableBytes: memory - 1, DiskAvailableBytes: disk}
	fixture.source.mu.Unlock()
	if reservation, err := fixture.authority.ReserveRetainedCapacity(context.Background(), operation, 1); reservation != nil || !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("below-memory reservation=%T error=%v", reservation, err)
	}
	fixture.source.mu.Lock()
	fixture.source.value = generatedcapacity.Snapshot{MemoryAvailableBytes: memory, DiskAvailableBytes: disk - 1}
	fixture.source.mu.Unlock()
	if reservation, err := fixture.authority.ReserveRetainedCapacity(context.Background(), operation, 1); reservation != nil || !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("below-disk reservation=%T error=%v", reservation, err)
	}
	fixture.source.mu.Lock()
	fixture.source.value = generatedcapacity.Snapshot{MemoryAvailableBytes: memory, DiskAvailableBytes: disk}
	fixture.source.mu.Unlock()
	retained, err := fixture.authority.ReserveRetainedCapacity(context.Background(), operation, 1)
	if err != nil {
		t.Fatal(err)
	}

	replacement, err := fixture.authority.ReserveReplacement(fixture.validation.migration.guarded.context,
		GeneratedReplacementRequest(fixture.validation.request))
	if replacement != nil || !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("replacement did not contend with retained charge: reservation=%T error=%v", replacement, err)
	}
	if err := fixture.authority.ReleaseRetainedCapacity(operation, retained); err != nil {
		t.Fatal(err)
	}
	replacement, err = fixture.authority.ReserveReplacement(fixture.validation.migration.guarded.context,
		GeneratedReplacementRequest(fixture.validation.request))
	if err != nil || replacement == nil {
		t.Fatalf("released retained capacity was not reusable by replacement: reservation=%T error=%v", replacement, err)
	}
	replacement.Release()

	fixture.source.mu.Lock()
	fixture.source.value = generatedcapacity.Snapshot{MemoryAvailableBytes: 2 * memory, DiskAvailableBytes: 2 * disk}
	fixture.source.mu.Unlock()
	two, err := fixture.authority.ReserveRetainedCapacity(context.Background(), operation, 2)
	if err != nil {
		t.Fatalf("fixed two-share roster reservation: %v", err)
	}
	if extra, err := fixture.authority.ReserveRetainedCapacity(context.Background(),
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", 1); extra != nil || !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("two-share reservation did not charge both shares: reservation=%T error=%v", extra, err)
	}
	if err := fixture.authority.ReleaseRetainedCapacity(operation, two); err != nil {
		t.Fatal(err)
	}
}
