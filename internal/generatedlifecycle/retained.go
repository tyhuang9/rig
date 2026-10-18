// Package generatedlifecycle composes durable application lifecycle authority
// with the generated runtime's retained-container primitives. It creates no
// jobs, intents, receipts, routes, or public action surface.
package generatedlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/generatedruntime"
)

var ErrRetainedAuthority = errors.New("generated lifecycle retained authority refused")

type Retained struct {
	repository *applifecycle.Repository
	runtime    *generatedruntime.Engine
}

// StartSet is an opaque, process-local aggregate reservation for the exact
// immutable operation roster. It is never durable effect authority.
type StartSet struct {
	owner        *Retained
	operation    applifecycle.Operation
	authorities  map[string]applifecycle.RuntimeStartAuthority
	targets      map[string]generatedruntime.RetainedTarget
	ordered      []string
	reservation  generatedruntime.RetainedReservation
	observations map[string]generatedruntime.RetainedObservation
}

func NewRetained(repository *applifecycle.Repository, runtime *generatedruntime.Engine) (*Retained, error) {
	if repository == nil || runtime == nil {
		return nil, ErrRetainedAuthority
	}
	return &Retained{repository: repository, runtime: runtime}, nil
}

// PrepareStarts loads the canonical complete pending-start roster, observes
// the complete retained physical roster, reserves stopped capacity together,
// then revalidates the same durable authority before returning.
func (r *Retained) PrepareStarts(ctx context.Context, claim applifecycle.WorkerClaim) (*StartSet, error) {
	if r == nil || ctx == nil {
		return nil, ErrRetainedAuthority
	}
	authorities, err := r.repository.PendingRuntimeStarts(ctx, claim)
	if err != nil || len(authorities) == 0 {
		return nil, retainedAuthorityError(err)
	}
	operation := authorities[0].Operation
	targets, ordered, err := targetsForOperation(operation)
	if err != nil || len(targets) != len(authorities) {
		return nil, ErrRetainedAuthority
	}
	authorityMap := make(map[string]applifecycle.RuntimeStartAuthority, len(authorities))
	pendingCount := 0
	for _, authority := range authorities {
		name := authority.Intent.Key.ComponentName
		if !sameOperation(operation, authority.Operation) || targets[name].Component == "" {
			return nil, ErrRetainedAuthority
		}
		authorityMap[name] = authority
		if authority.IsPending() {
			pendingCount++
		}
	}
	if pendingCount == 0 {
		return nil, ErrRetainedAuthority
	}
	orderedTargets := make([]generatedruntime.RetainedTarget, 0, len(ordered))
	for _, name := range ordered {
		orderedTargets = append(orderedTargets, targets[name])
	}
	observed, reservation, err := r.runtime.ObserveAndReserveRetained(ctx, operation.JobID, orderedTargets)
	if err != nil {
		return nil, err
	}
	releaseOnFailure := func(cause error) error {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		return errors.Join(cause, r.runtime.ReleaseUnattemptedRetainedReservation(cleanupContext, reservation))
	}
	refreshed, err := r.repository.PendingRuntimeStarts(ctx, claim)
	if err != nil || !sameStartAuthorities(authorities, refreshed) || ctx.Err() != nil {
		if err == nil {
			err = ctx.Err()
		}
		return nil, releaseOnFailure(retainedAuthorityError(err))
	}
	observationMap := make(map[string]generatedruntime.RetainedObservation, len(observed))
	for _, observation := range observed {
		observationMap[observation.Component] = observation
		if authority := authorityMap[observation.Component]; authority.Receipt != nil && observation.State != generatedruntime.RetainedRunning {
			return nil, releaseOnFailure(ErrRetainedAuthority)
		}
	}
	return &StartSet{
		owner: r, operation: operation, authorities: authorityMap, targets: targets,
		ordered: ordered, reservation: reservation, observations: observationMap,
	}, nil
}

// Start issues or reconciles one exact pending start. Durable authority is
// checked before inspection, after inspection immediately before a command,
// and again after the final observation before proof construction.
func (r *Retained) Start(ctx context.Context, claim applifecycle.WorkerClaim, set *StartSet, component string) (applifecycle.EffectProofV1, error) {
	if !validStartSet(r, set, component) || ctx == nil {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	expected := set.authorities[component]
	if !expected.IsPending() {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	if err := r.requireKnownStartRoster(ctx, set); err != nil {
		return applifecycle.EffectProofV1{}, err
	}
	if err := r.requireStartSet(ctx, claim, set); err != nil {
		return applifecycle.EffectProofV1{}, err
	}
	guard := func(guardContext context.Context) error {
		// The earlier census prevents obviously unsafe work. Repeat the full
		// physical roster census at the actual command boundary so another
		// retained member becoming unknown during target inspection blocks this
		// start as well.
		if err := r.requireKnownStartRoster(guardContext, set); err != nil {
			return err
		}
		if err := r.requireStartSet(guardContext, claim, set); err != nil {
			return err
		}
		return nil
	}
	observation, err := r.runtime.StartRetained(ctx, set.operation.JobID, set.targets[component], set.reservation, guard)
	if err != nil {
		return applifecycle.EffectProofV1{}, err
	}
	refreshed, err := r.repository.PendingRuntimeBoundary(ctx, claim, applifecycle.BoundaryStart, component)
	if err != nil || !applifecycle.SameRuntimeStartAuthority(expected, applifecycle.RuntimeStartAuthority{
		Operation: refreshed.Operation, Intent: refreshed.Intent, Pending: refreshed.Pending,
	}) || ctx.Err() != nil {
		return applifecycle.EffectProofV1{}, retainedAuthorityError(errors.Join(err, ctx.Err()))
	}
	set.observations[component] = observation
	return retainedProof(set.operation, expected.Intent, applifecycle.OutcomeContainerRunning, []generatedruntime.RetainedObservation{observation})
}

// ReleaseStarts drops aggregate accounting only after the runtime freshly
// proves every attempted target running and every unattempted target exact.
func (r *Retained) ReleaseStarts(ctx context.Context, set *StartSet) error {
	if r == nil || set == nil || set.owner != r || set.reservation == nil {
		return ErrRetainedAuthority
	}
	return r.runtime.ReleaseRetainedReservation(ctx, set.reservation)
}

// Stop issues or reconciles one exact retained stop. It never removes a
// container or changes durable journal state.
func (r *Retained) Stop(ctx context.Context, claim applifecycle.WorkerClaim, component string, grace time.Duration) (applifecycle.EffectProofV1, error) {
	if r == nil || ctx == nil {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	expected, err := r.repository.PendingRuntimeBoundary(ctx, claim, applifecycle.BoundaryStop, component)
	if err != nil {
		return applifecycle.EffectProofV1{}, retainedAuthorityError(err)
	}
	targets, _, err := targetsForOperation(expected.Operation)
	target, exists := targets[component]
	if err != nil || !exists {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	guard := func(guardContext context.Context) error {
		refreshed, guardErr := r.repository.PendingRuntimeBoundary(guardContext, claim, applifecycle.BoundaryStop, component)
		if guardErr != nil || !applifecycle.SameRuntimePendingAuthority(expected, refreshed) {
			return retainedAuthorityError(guardErr)
		}
		return nil
	}
	observation, err := r.runtime.StopRetained(ctx, target, grace, guard)
	if err != nil {
		return applifecycle.EffectProofV1{}, err
	}
	refreshed, err := r.repository.PendingRuntimeBoundary(ctx, claim, applifecycle.BoundaryStop, component)
	if err != nil || !applifecycle.SameRuntimePendingAuthority(expected, refreshed) || ctx.Err() != nil {
		return applifecycle.EffectProofV1{}, retainedAuthorityError(errors.Join(err, ctx.Err()))
	}
	return retainedProof(expected.Operation, expected.Intent, applifecycle.OutcomeContainerStopped, []generatedruntime.RetainedObservation{observation})
}

// Healthy observes the exact whole retained roster and revalidates its single
// observation-only health intent before returning proof.
func (r *Retained) Healthy(ctx context.Context, claim applifecycle.WorkerClaim) (applifecycle.EffectProofV1, error) {
	if r == nil || ctx == nil {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	expected, err := r.repository.PendingRuntimeBoundary(ctx, claim, applifecycle.BoundaryHealth, "")
	if err != nil {
		return applifecycle.EffectProofV1{}, retainedAuthorityError(err)
	}
	targets, ordered, err := targetsForOperation(expected.Operation)
	if err != nil {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	values := make([]generatedruntime.RetainedTarget, 0, len(ordered))
	for _, name := range ordered {
		values = append(values, targets[name])
	}
	observations, err := r.runtime.WaitRetainedHealthy(ctx, values)
	if err != nil {
		return applifecycle.EffectProofV1{}, err
	}
	refreshed, err := r.repository.PendingRuntimeBoundary(ctx, claim, applifecycle.BoundaryHealth, "")
	if err != nil || !applifecycle.SameRuntimePendingAuthority(expected, refreshed) || ctx.Err() != nil {
		return applifecycle.EffectProofV1{}, retainedAuthorityError(errors.Join(err, ctx.Err()))
	}
	return retainedProof(expected.Operation, expected.Intent, applifecycle.OutcomeHealthy, observations)
}

func (r *Retained) requireStartSet(ctx context.Context, claim applifecycle.WorkerClaim, set *StartSet) error {
	values, err := r.repository.PendingRuntimeStarts(ctx, claim)
	if err != nil || len(values) != len(set.ordered) {
		return retainedAuthorityError(err)
	}
	for _, value := range values {
		expected, exists := set.authorities[value.Intent.Key.ComponentName]
		if !exists || !applifecycle.SameRuntimeStartAuthority(expected, value) {
			return ErrRetainedAuthority
		}
	}
	if ctx.Err() != nil {
		return ErrRetainedAuthority
	}
	return nil
}

func (r *Retained) requireKnownStartRoster(ctx context.Context, set *StartSet) error {
	values := make([]generatedruntime.RetainedTarget, 0, len(set.ordered))
	for _, name := range set.ordered {
		values = append(values, set.targets[name])
	}
	observed, err := r.runtime.ObserveRetained(ctx, values)
	if err != nil {
		return err
	}
	for _, observation := range observed {
		authority, exists := set.authorities[observation.Component]
		if !exists || authority.Receipt != nil && observation.State != generatedruntime.RetainedRunning {
			return ErrRetainedAuthority
		}
	}
	return nil
}

func validStartSet(owner *Retained, set *StartSet, component string) bool {
	if owner == nil || set == nil || set.owner != owner || component == "" || set.reservation == nil {
		return false
	}
	_, hasAuthority := set.authorities[component]
	_, hasTarget := set.targets[component]
	return hasAuthority && hasTarget
}

func targetsForOperation(operation applifecycle.Operation) (map[string]generatedruntime.RetainedTarget, []string, error) {
	if operation.JobID == "" || operation.ApplicationID == "" || len(operation.Pins.Components) < 1 || len(operation.Pins.Components) > 2 {
		return nil, nil, ErrRetainedAuthority
	}
	targets := make(map[string]generatedruntime.RetainedTarget, len(operation.Pins.Components))
	ordered := make([]string, 0, len(operation.Pins.Components))
	for _, component := range operation.Pins.Components {
		if _, exists := targets[component.Name]; exists {
			return nil, nil, ErrRetainedAuthority
		}
		policy := component.Limits
		targets[component.Name] = generatedruntime.RetainedTarget{
			AppID: operation.ApplicationID, ReleaseID: operation.Pins.ReleaseID,
			DeploymentID: operation.Pins.DeploymentID, ArtifactID: component.ArtifactID,
			DeploymentPlanRevisionID: operation.Pins.PlanRevision.ID,
			Component:                component.Name, Role: component.Role, Technology: component.Technology,
			Slot: operation.Pins.Slot, ContainerID: component.ContainerID, ContainerName: component.ContainerName,
			NetworkName: component.NetworkName, NetworkAlias: component.NetworkAlias,
			ImageContentID: component.ImageContentID, BuildDefinitionDigest: component.BuildDefinitionDigest,
			WorkingDirectory: component.WorkingDirectory, RunCommandDigest: component.RunCommandDigest,
			InternalPort: component.InternalPort,
			Policy: generatedruntime.RetainedPolicy{
				MemoryBytes: policy.MemoryBytes, MemorySwapBytes: policy.MemorySwapBytes,
				CPUQuota: policy.CPUQuota, CPUPeriod: policy.CPUPeriod,
				PIDsLimit: policy.PIDsLimit, TmpfsBytes: policy.TmpfsBytes,
				LogDriver: policy.LogDriver, LogMaxSize: policy.LogMaxSize, LogMaxFiles: policy.LogMaxFiles,
				HardeningPolicyVersion: policy.HardeningPolicyVersion,
			},
		}
		ordered = append(ordered, component.Name)
	}
	sort.Strings(ordered)
	return targets, ordered, nil
}

func retainedProof(operation applifecycle.Operation, intent applifecycle.Intent, outcome applifecycle.Outcome, observations []generatedruntime.RetainedObservation) (applifecycle.EffectProofV1, error) {
	ordered := append([]generatedruntime.RetainedObservation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Component < ordered[j].Component })
	evidence := struct {
		Domain          string
		OperationID     string
		DesiredRevision int64
		PinsDigest      string
		IntentDigest    string
		Boundary        applifecycle.Boundary
		Component       string
		Outcome         applifecycle.Outcome
		Observations    []generatedruntime.RetainedObservation
	}{
		Domain: "hostd.generated-lifecycle.retained-proof.v1", OperationID: operation.JobID,
		DesiredRevision: operation.DesiredRevision, PinsDigest: operation.PinsDigest,
		IntentDigest: intent.RequestDigest, Boundary: intent.Key.Boundary,
		Component: intent.Key.ComponentName, Outcome: outcome, Observations: ordered,
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return applifecycle.EffectProofV1{}, ErrRetainedAuthority
	}
	digest := sha256.Sum256(encoded)
	return applifecycle.EffectProofV1{
		Version: 1, OperationID: operation.JobID, ApplicationID: operation.ApplicationID,
		DesiredRevision: operation.DesiredRevision, Boundary: intent.Key.Boundary,
		ComponentName: intent.Key.ComponentName, Outcome: outcome,
		IntentDigest: intent.RequestDigest, EvidenceDigest: hex.EncodeToString(digest[:]), PinsDigest: operation.PinsDigest,
	}, nil
}

func sameOperation(left, right applifecycle.Operation) bool {
	return left.JobID == right.JobID && left.ApplicationID == right.ApplicationID &&
		left.DesiredRevision == right.DesiredRevision && left.ExpectedRevision == right.ExpectedRevision &&
		left.Action == right.Action && left.ActorID == right.ActorID && left.IdempotencyKey == right.IdempotencyKey &&
		left.PinsDigest == right.PinsDigest
}

func sameStartAuthorities(left, right []applifecycle.RuntimeStartAuthority) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !applifecycle.SameRuntimeStartAuthority(left[index], right[index]) {
			return false
		}
	}
	return true
}

func retainedAuthorityError(err error) error {
	if err == nil {
		return ErrRetainedAuthority
	}
	return errors.Join(ErrRetainedAuthority, err)
}
