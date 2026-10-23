package generatedruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

type RetainedState string

const (
	RetainedRunning RetainedState = "running"
	RetainedStopped RetainedState = "stopped"
)

// RetainedPolicy is the immutable resource and hardening policy retained by
// lifecycle operation pins. CPUQuota/CPUPeriod is compared as an exact ratio
// to Docker's NanoCPUs policy; Docker need not expose quota fields themselves.
type RetainedPolicy struct {
	MemoryBytes, MemorySwapBytes int64
	CPUQuota, CPUPeriod          int64
	PIDsLimit, TmpfsBytes        int64
	LogDriver, LogMaxSize        string
	LogMaxFiles                  int
	HardeningPolicyVersion       string
}

type RetainedTarget struct {
	AppID, ReleaseID, DeploymentID, ArtifactID, DeploymentPlanRevisionID string
	Component, Role, Technology, Slot                                    string
	ContainerID, ContainerName, NetworkName, NetworkAlias                string
	ImageContentID, BuildDefinitionDigest                                string
	WorkingDirectory, RunCommandDigest                                   string
	InternalPort                                                         uint16
	Policy                                                               RetainedPolicy
}

// RetainedObservation contains only stable, non-secret physical facts and a
// digest of the complete expected identity and policy.
type RetainedObservation struct {
	Component    string
	TargetDigest string
	State        RetainedState
	Healthy      bool
}

// RetainedGuard is invoked after exact physical inspection and immediately
// before a retained Docker mutation. It lets the composing authority layer
// revalidate durable permission without creating a runtime dependency cycle.
type RetainedGuard func(context.Context) error

// RetainedReservation is opaque and process-local. It cannot be constructed
// outside this package or serialized as durable authority.
type RetainedReservation interface{ retainedReservation() }

type retainedReservation struct {
	owner      *Engine
	operation  string
	accounting generatedingress.GeneratedRetainedCapacityReservation

	mu       sync.Mutex
	all      map[string]RetainedTarget
	reserved map[string]*retainedShare
	released bool
}

type retainedShare struct {
	attempted bool
}

func (*retainedReservation) retainedReservation() {}

// ObserveRetained validates a complete retained roster without reserving or
// mutating anything. It is the fresh physical census used before each
// component start so uncertainty in one component blocks effects on another.
func (e *Engine) ObserveRetained(ctx context.Context, targets []RetainedTarget) ([]RetainedObservation, error) {
	leave, err := e.enterRetained(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	if len(targets) < 1 || len(targets) > 2 {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	ordered := append([]RetainedTarget(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Component < ordered[j].Component })
	result := make([]RetainedObservation, 0, len(ordered))
	for index, target := range ordered {
		if index > 0 && ordered[index-1].Component >= target.Component || !e.validRetainedTarget(target) {
			return nil, &Error{Code: DiagnosticValidationFailed}
		}
		observation, inspectErr := e.inspectRetained(ctx, target)
		if inspectErr != nil {
			return nil, inspectErr
		}
		result = append(result, observation)
	}
	if ctx.Err() != nil {
		return nil, &Error{Code: DiagnosticCancelled}
	}
	return result, nil
}

// ObserveAndReserveRetained validates the complete retained roster under one
// live effects scope and atomically reserves capacity for every definitely
// stopped component. Unknown or drifted state never becomes free capacity.
func (e *Engine) ObserveAndReserveRetained(ctx context.Context, operationID string, targets []RetainedTarget) ([]RetainedObservation, RetainedReservation, error) {
	leave, err := e.enterRetained(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer leave()
	if !validText(operationID, 256) || len(targets) < 1 || len(targets) > 2 {
		return nil, nil, &Error{Code: DiagnosticValidationFailed}
	}
	ordered := append([]RetainedTarget(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Component < ordered[j].Component })
	all := make(map[string]RetainedTarget, len(ordered))
	reserved := make(map[string]*retainedShare, len(ordered))
	observations := make([]RetainedObservation, 0, len(ordered))
	for index, target := range ordered {
		if index > 0 && ordered[index-1].Component >= target.Component || !e.validRetainedTarget(target) {
			return nil, nil, &Error{Code: DiagnosticValidationFailed}
		}
		observation, inspectErr := e.inspectRetained(ctx, target)
		if inspectErr != nil {
			return nil, nil, inspectErr
		}
		digest := observation.TargetDigest
		all[digest] = target
		if observation.State == RetainedStopped {
			reserved[digest] = &retainedShare{}
		}
		observations = append(observations, observation)
	}
	if ctx.Err() != nil {
		return nil, nil, &Error{Code: DiagnosticCancelled}
	}
	var accounting generatedingress.GeneratedRetainedCapacityReservation
	if len(reserved) != 0 {
		accounting, err = e.candidates.ReserveRetainedCapacity(ctx, operationID, len(reserved))
		if err != nil {
			return nil, nil, capacityDiagnostic(ctx, err)
		}
		if ctx.Err() != nil {
			_ = e.candidates.ReleaseRetainedCapacity(operationID, accounting)
			return nil, nil, &Error{Code: DiagnosticCancelled}
		}
	}
	return observations, &retainedReservation{
		owner: e, operation: operationID, accounting: accounting, all: all, reserved: reserved,
	}, nil
}

// StopRetained stops but never removes an exact retained container. A stable
// already-stopped container is observation-only. Lost acknowledgements are
// reconciled only by a fresh exact stopped observation.
func (e *Engine) StopRetained(ctx context.Context, target RetainedTarget, grace time.Duration, guard RetainedGuard) (RetainedObservation, error) {
	leave, err := e.enterRetained(ctx)
	if err != nil {
		return RetainedObservation{}, err
	}
	defer leave()
	if !e.validRetainedTarget(target) || grace < 0 || grace > 30*time.Second || guard == nil {
		return RetainedObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	before, err := e.inspectRetained(ctx, target)
	if err != nil {
		return before, err
	}
	if ctx.Err() != nil {
		return RetainedObservation{}, &Error{Code: DiagnosticCancelled}
	}
	if before.State == RetainedStopped {
		return before, nil
	}
	if err := guard(ctx); err != nil || ctx.Err() != nil {
		if err != nil {
			return RetainedObservation{}, err
		}
		return RetainedObservation{}, &Error{Code: DiagnosticCancelled}
	}
	seconds := int64(grace / time.Second)
	if grace%time.Second != 0 {
		seconds++
	}
	result, runErr := e.run(ctx, []string{"container", "stop", "--time", strconv.FormatInt(seconds, 10), target.ContainerID}, grace+e.options.CommandTimeout)
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateCleanupFailed)
	clearResult(&result)
	after, inspectErr := e.inspectRetained(ctx, target)
	if inspectErr == nil && after.State == RetainedStopped && ctx.Err() == nil {
		return after, nil
	}
	if diagnostic != "" {
		return RetainedObservation{}, &Error{Code: diagnostic}
	}
	if inspectErr != nil {
		return RetainedObservation{}, inspectErr
	}
	return RetainedObservation{}, &Error{Code: DiagnosticCandidateExited}
}

// StartRetained starts one exact stopped target using its aggregate reserved
// share. A stable already-running target is reconciled without a command.
func (e *Engine) StartRetained(ctx context.Context, operationID string, target RetainedTarget, reservation RetainedReservation, guard RetainedGuard) (RetainedObservation, error) {
	leave, err := e.enterRetained(ctx)
	if err != nil {
		return RetainedObservation{}, err
	}
	defer leave()
	value, ok := reservation.(*retainedReservation)
	if !ok || !e.validRetainedTarget(target) || !validText(operationID, 256) || guard == nil {
		return RetainedObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	digest, err := retainedTargetDigest(target)
	if err != nil {
		return RetainedObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	expected, exists := value.all[digest]
	if value.released || value.owner != e || value.operation != operationID || !exists || expected != target {
		return RetainedObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	before, err := e.inspectRetained(ctx, target)
	if err != nil {
		return RetainedObservation{}, err
	}
	if ctx.Err() != nil {
		return RetainedObservation{}, &Error{Code: DiagnosticCancelled}
	}
	if before.State == RetainedRunning {
		return before, nil
	}
	if err := guard(ctx); err != nil || ctx.Err() != nil {
		if err != nil {
			return RetainedObservation{}, err
		}
		return RetainedObservation{}, &Error{Code: DiagnosticCancelled}
	}
	share, exists := value.reserved[digest]
	if !exists || share.attempted {
		return RetainedObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	share.attempted = true
	result, runErr := e.run(ctx, []string{"container", "start", target.ContainerID}, e.options.CommandTimeout)
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateStartFailed)
	clearResult(&result)
	after, inspectErr := e.inspectRetained(ctx, target)
	if inspectErr == nil && after.State == RetainedRunning && ctx.Err() == nil {
		return after, nil
	}
	if diagnostic != "" {
		return RetainedObservation{}, &Error{Code: diagnostic}
	}
	if inspectErr != nil {
		return RetainedObservation{}, inspectErr
	}
	return RetainedObservation{}, &Error{Code: DiagnosticCandidateExited}
}

// WaitRetainedHealthy observes the exact complete roster. It performs no
// cleanup on failure and keeps the live effects scope pinned for the bounded
// health wait.
func (e *Engine) WaitRetainedHealthy(ctx context.Context, targets []RetainedTarget) ([]RetainedObservation, error) {
	leave, err := e.enterRetained(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	if len(targets) < 1 || len(targets) > 2 {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	waitCtx, cancel := context.WithTimeout(ctx, e.options.HealthTimeout)
	defer cancel()
	for {
		observations := make([]RetainedObservation, 0, len(targets))
		allHealthy := true
		for _, target := range targets {
			if !e.validRetainedTarget(target) {
				return nil, &Error{Code: DiagnosticValidationFailed}
			}
			observation, inspectErr := e.inspectRetained(waitCtx, target)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if observation.State != RetainedRunning {
				return nil, &Error{Code: DiagnosticCandidateExited}
			}
			if !observation.Healthy {
				allHealthy = false
			}
			observations = append(observations, observation)
		}
		if allHealthy {
			if waitCtx.Err() != nil {
				return nil, retainedContextError(ctx)
			}
			return observations, nil
		}
		timer := time.NewTimer(e.options.HealthPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return nil, retainedContextError(ctx)
		case <-timer.C:
		}
	}
}

// ReleaseRetainedReservation releases accounting only after fresh exact
// observation proves every attempted target is running. An unattempted target
// may remain definitely stopped, proving no command consumed that share.
func (e *Engine) ReleaseRetainedReservation(ctx context.Context, reservation RetainedReservation) error {
	leave, err := e.enterRetained(ctx)
	if err != nil {
		return err
	}
	defer leave()
	value, ok := reservation.(*retainedReservation)
	if !ok || value.owner != e {
		return &Error{Code: DiagnosticValidationFailed}
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.released {
		return &Error{Code: DiagnosticValidationFailed}
	}
	for digest, target := range value.all {
		observation, inspectErr := e.inspectRetained(ctx, target)
		if inspectErr != nil {
			return inspectErr
		}
		if share := value.reserved[digest]; share != nil && share.attempted && observation.State != RetainedRunning {
			return &Error{Code: DiagnosticValidationFailed}
		}
	}
	if ctx.Err() != nil {
		return &Error{Code: DiagnosticCancelled}
	}
	if value.accounting != nil {
		if err := e.candidates.ReleaseRetainedCapacity(value.operation, value.accounting); err != nil {
			return &Error{Code: DiagnosticValidationFailed}
		}
	}
	value.released = true
	value.accounting = nil
	return nil
}

// ReleaseUnattemptedRetainedReservation releases a reservation that provably
// has never issued a start command. It performs no Docker read, so an
// authority or cancellation refusal after Prepare cannot hide a safe handle
// merely because Docker is unavailable. This accounting-only cleanup does
// not require a live effects scope: scope shutdown may race a final authority
// refusal, while the reservation mutex still proves that no command began.
func (e *Engine) ReleaseUnattemptedRetainedReservation(ctx context.Context, reservation RetainedReservation) error {
	if e == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	value, ok := reservation.(*retainedReservation)
	if !ok || value.owner != e {
		return &Error{Code: DiagnosticValidationFailed}
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.released {
		return &Error{Code: DiagnosticValidationFailed}
	}
	for _, share := range value.reserved {
		if share.attempted {
			return &Error{Code: DiagnosticValidationFailed}
		}
	}
	if value.accounting != nil {
		if err := e.candidates.ReleaseRetainedCapacity(value.operation, value.accounting); err != nil {
			return &Error{Code: DiagnosticValidationFailed}
		}
	}
	value.released = true
	value.accounting = nil
	return nil
}

func (e *Engine) enterRetained(ctx context.Context) (func(), error) {
	if e == nil || ctx == nil {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	if ctx.Err() != nil {
		return nil, &Error{Code: DiagnosticCancelled}
	}
	leave, err := deploymenteffects.EnterScope(ctx, e.options.WorkingDirectory)
	if err != nil {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	return leave, nil
}

func (e *Engine) inspectRetained(ctx context.Context, target RetainedTarget) (RetainedObservation, error) {
	container, found, err := e.inspectContainer(ctx, target.ContainerID)
	if err != nil {
		return RetainedObservation{}, err
	}
	if !found {
		return RetainedObservation{}, &Error{Code: DiagnosticCandidateHardeningFailed}
	}
	candidate := retainedCandidate(target)
	state := RetainedState("")
	switch {
	case container.Status == "running" && container.Running && !container.Paused && !container.Restarting && !container.Dead:
		if !matchesCandidateHardening(container, candidate, e.options.Limits) {
			return RetainedObservation{}, &Error{Code: DiagnosticCandidateHardeningFailed}
		}
		state = RetainedRunning
	case container.Status == "exited" && !container.Running && !container.Paused && !container.Restarting && !container.Dead:
		if !matchesCandidateConfiguredHardening(container, candidate, e.options.Limits) {
			return RetainedObservation{}, &Error{Code: DiagnosticCandidateHardeningFailed}
		}
		state = RetainedStopped
	default:
		return RetainedObservation{}, &Error{Code: DiagnosticCandidateExited}
	}
	network, found, err := e.inspectNetwork(ctx, target.NetworkName)
	if err != nil {
		return RetainedObservation{}, err
	}
	if !found || !matchesNetwork(network, target.AppID, target.NetworkName) {
		return RetainedObservation{}, &Error{Code: DiagnosticNetworkDriftDetected}
	}
	digest, err := retainedTargetDigest(target)
	if err != nil {
		return RetainedObservation{}, &Error{Code: DiagnosticValidationFailed}
	}
	return RetainedObservation{Component: target.Component, TargetDigest: digest, State: state, Healthy: state == RetainedRunning && container.Health == "healthy"}, nil
}

func (e *Engine) validRetainedTarget(target RetainedTarget) bool {
	candidate := retainedCandidate(target)
	if !validCandidate(candidate) || !lowerHex(target.BuildDefinitionDigest, 64) {
		return false
	}
	policy := target.Policy
	if policy.MemoryBytes != e.options.Limits.MemoryBytes || policy.MemorySwapBytes != policy.MemoryBytes ||
		policy.PIDsLimit != e.options.Limits.PIDs || policy.TmpfsBytes != e.options.Limits.TmpfsBytes ||
		policy.LogDriver != "local" || policy.LogMaxSize != e.options.Limits.LogSize ||
		policy.LogMaxFiles != e.options.Limits.LogFiles || policy.HardeningPolicyVersion != "v1" ||
		policy.CPUQuota <= 0 || policy.CPUPeriod <= 0 || policy.CPUQuota > math.MaxInt64/1000 {
		return false
	}
	value := policy.CPUQuota * 1000
	return value%policy.CPUPeriod == 0 && value/policy.CPUPeriod == e.options.Limits.MilliCPUs
}

func retainedCandidate(target RetainedTarget) Candidate {
	return Candidate{
		AppID: target.AppID, ReleaseID: target.ReleaseID, DeploymentID: target.DeploymentID,
		ArtifactID: target.ArtifactID, DeploymentPlanRevisionID: target.DeploymentPlanRevisionID,
		Component: target.Component, Role: target.Role, Technology: target.Technology, Slot: Slot(target.Slot),
		ContainerID: target.ContainerID, ContainerName: target.ContainerName, NetworkName: target.NetworkName,
		NetworkAlias: target.NetworkAlias, InternalPort: target.InternalPort, ImageContentID: target.ImageContentID,
		WorkingDirectory: target.WorkingDirectory, RunCommandDigest: target.RunCommandDigest,
	}
}

func retainedTargetDigest(target RetainedTarget) (string, error) {
	encoded, err := json.Marshal(target)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("hostd.generated-runtime.retained-target.v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func retainedContextError(parent context.Context) error {
	if errors.Is(parent.Err(), context.Canceled) {
		return &Error{Code: DiagnosticCancelled}
	}
	return &Error{Code: DiagnosticCandidateUnhealthy}
}
