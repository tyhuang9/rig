//go:build live_docker

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	liveHostdRebindPendingRouteObservationCap = 10 * time.Minute
	liveHostdRebindPendingRouteStartCap       = 2 * time.Minute
	liveHostdRebindPendingRouteEventTail      = 8
)

type liveHostdRebindPendingRouteObservation struct {
	observed                 bool
	appID                    string
	jobID                    string
	jobAttempt               int
	jobInputSize             int
	jobInputDigest           [sha256.Size]byte
	jobStatus                string
	jobPhase                 string
	jobProgress              int
	jobErrorCode             string
	deploymentFound          bool
	deploymentID             string
	deploymentReleaseID      string
	deploymentPlanID         string
	deploymentPlanNumber     int64
	deploymentConfigID       string
	deploymentConfigNumber   int64
	deploymentStatus         deployments.Status
	deploymentDiagnosticCode string
	runtimeFound             bool
	runtimeDeploymentID      string
	runtimeReleaseID         string
	runtimePlanID            string
	runtimePlanNumber        int64
	runtimeCandidateSlot     string
	runtimePreviousID        string
	runtimePreviousSlot      string
	runtimePhase             generatedruntimestate.Phase
	runtimeDiagnosticCode    generatedruntimestate.DiagnosticCode
	componentIdentities      []liveHostdRebindPendingRouteComponentIdentity
	componentStates          []generatedruntimestate.ComponentState
	componentDiagnosticCodes []generatedruntimestate.DiagnosticCode
	activeHeadFound          bool
	activeHead               generatedruntimestate.ActiveHead
	events                   []jobs.Event
}

type liveHostdRebindPendingRouteComponentIdentity struct {
	name, slot, imageArtifactID, containerName, containerID string
}

type liveHostdRebindPendingRouteObserver struct {
	appID       string
	jobs        *jobs.Service
	deployments *deployments.Repository
	runtime     *generatedruntimestate.Repository
}

type liveHostdRebindPendingRouteSnapshot func(context.Context, string) (liveHostdRebindPendingRouteObservation, error)

func liveHostdRebindNewPendingRouteObserver(db *sql.DB, appID string) *liveHostdRebindPendingRouteObserver {
	return &liveHostdRebindPendingRouteObserver{
		appID: appID, jobs: jobs.New(db), deployments: deployments.New(db), runtime: generatedruntimestate.New(db),
	}
}

func (o *liveHostdRebindPendingRouteObserver) snapshot(ctx context.Context,
	jobID string,
) (liveHostdRebindPendingRouteObservation, error) {
	var empty liveHostdRebindPendingRouteObservation
	if o == nil || o.jobs == nil || o.deployments == nil || o.runtime == nil || ctx == nil || o.appID == "" || jobID == "" {
		return empty, errors.New("invalid pending route observer")
	}
	job, err := o.jobs.Get(jobID)
	if err != nil {
		return empty, errors.New("read pending route job snapshot failed")
	}
	events, err := o.jobs.Events(jobID, 0)
	if err != nil {
		return empty, errors.New("read pending route event snapshot failed")
	}
	values, err := o.deployments.List(ctx, o.appID, 200)
	if err != nil {
		return empty, errors.New("read pending route deployment snapshot failed")
	}
	observation, deploymentID, err := liveHostdRebindPendingRouteObservationFor(o.appID, jobID, job, events, values)
	if err != nil || deploymentID == "" {
		return observation, err
	}
	head, err := o.runtime.Active(ctx, o.appID)
	if err == nil {
		observation.activeHeadFound = true
		observation.activeHead = head
	} else if !errors.Is(err, generatedruntimestate.ErrNotFound) {
		return empty, errors.New("read pending route active head snapshot failed")
	}
	runtime, err := o.runtime.Get(ctx, o.appID, deploymentID)
	if errors.Is(err, generatedruntimestate.ErrNotFound) {
		return observation, nil
	}
	if err != nil {
		return empty, errors.New("read pending route runtime snapshot failed")
	}
	if runtime.AppID != o.appID || runtime.DeploymentID != deploymentID {
		return empty, errors.New("pending route runtime identity changed")
	}
	if !liveHostdRebindKnownRuntimePhase(runtime.Phase) {
		return empty, errors.New("pending route runtime phase is invalid")
	}
	observation.runtimeFound = true
	observation.runtimeDeploymentID = runtime.DeploymentID
	observation.runtimeReleaseID = runtime.ReleaseID
	observation.runtimePlanID = runtime.DeploymentPlanRevisionID
	observation.runtimePlanNumber = runtime.DeploymentPlanRevisionNumber
	observation.runtimeCandidateSlot = runtime.CandidateSlot
	observation.runtimePreviousID = runtime.PreviousActiveDeploymentID
	observation.runtimePreviousSlot = runtime.PreviousActiveSlot
	observation.runtimePhase = runtime.Phase
	observation.runtimeDiagnosticCode = runtime.DiagnosticCode
	for _, component := range runtime.Components {
		if !liveHostdRebindKnownComponentState(component.State) {
			return empty, errors.New("pending route component state is invalid")
		}
		observation.componentIdentities = append(observation.componentIdentities,
			liveHostdRebindPendingRouteComponentIdentity{
				name: component.Name, slot: component.Slot, imageArtifactID: component.ImageArtifactID,
				containerName: component.ContainerName, containerID: component.ContainerID,
			})
		observation.componentStates = append(observation.componentStates, component.State)
		observation.componentDiagnosticCodes = append(observation.componentDiagnosticCodes, component.DiagnosticCode)
	}
	indices := make([]int, len(observation.componentIdentities))
	for index := range indices {
		indices[index] = index
	}
	sort.Slice(indices, func(left, right int) bool {
		return liveHostdRebindPendingRouteComponentIdentityLess(
			observation.componentIdentities[indices[left]], observation.componentIdentities[indices[right]])
	})
	identities := make([]liveHostdRebindPendingRouteComponentIdentity, 0, len(indices))
	states := make([]generatedruntimestate.ComponentState, 0, len(indices))
	diagnostics := make([]generatedruntimestate.DiagnosticCode, 0, len(indices))
	for _, index := range indices {
		identities = append(identities, observation.componentIdentities[index])
		states = append(states, observation.componentStates[index])
		diagnostics = append(diagnostics, observation.componentDiagnosticCodes[index])
	}
	observation.componentIdentities = identities
	observation.componentStates = states
	observation.componentDiagnosticCodes = diagnostics
	return observation, nil
}

func liveHostdRebindPendingRouteComponentIdentityLess(left, right liveHostdRebindPendingRouteComponentIdentity) bool {
	leftValues := [...]string{left.name, left.slot, left.imageArtifactID, left.containerName, left.containerID}
	rightValues := [...]string{right.name, right.slot, right.imageArtifactID, right.containerName, right.containerID}
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return leftValues[index] < rightValues[index]
		}
	}
	return false
}

func liveHostdRebindPendingRouteObservationFor(appID, jobID string, job jobs.Job,
	events []jobs.Event, values []deployments.Deployment,
) (liveHostdRebindPendingRouteObservation, string, error) {
	var observation liveHostdRebindPendingRouteObservation
	if appID == "" || jobID == "" || job.ID != jobID || job.Type != "deploy" ||
		job.ResourceType != "application" || job.ResourceID != appID {
		return observation, "", errors.New("pending route job identity changed")
	}
	if !liveHostdRebindKnownJobStatus(job.Status) || job.Progress < 0 || job.Progress > 100 {
		return observation, "", errors.New("pending route job state is invalid")
	}
	observation = liveHostdRebindPendingRouteObservation{
		observed: true, appID: appID, jobID: job.ID, jobAttempt: job.Attempt, jobInputSize: len(job.Input),
		jobInputDigest: sha256.Sum256(job.Input), jobStatus: job.Status, jobPhase: job.Phase, jobProgress: job.Progress,
		jobErrorCode: job.ErrorCode,
	}
	for _, event := range events {
		if event.JobID != jobID {
			return liveHostdRebindPendingRouteObservation{}, "", errors.New("pending route event identity changed")
		}
	}
	if len(events) > liveHostdRebindPendingRouteEventTail {
		events = events[len(events)-liveHostdRebindPendingRouteEventTail:]
	}
	observation.events = append([]jobs.Event(nil), events...)
	deploymentID := ""
	for _, value := range values {
		if value.JobID != jobID {
			continue
		}
		if value.ID == "" || value.AppID != appID || deploymentID != "" {
			return liveHostdRebindPendingRouteObservation{}, "", errors.New("pending route deployment identity changed")
		}
		if !liveHostdRebindKnownDeploymentStatus(value.Status) {
			return liveHostdRebindPendingRouteObservation{}, "", errors.New("pending route deployment state is invalid")
		}
		deploymentID = value.ID
		observation.deploymentFound = true
		observation.deploymentID = value.ID
		observation.deploymentReleaseID = value.ReleaseID
		observation.deploymentPlanID = value.DeploymentPlanRevisionID
		observation.deploymentPlanNumber = value.DeploymentPlanRevisionNumber
		observation.deploymentConfigID = value.ActualConfigurationRevisionID
		observation.deploymentConfigNumber = value.ActualConfigurationRevisionNumber
		observation.deploymentStatus = value.Status
		observation.deploymentDiagnosticCode = value.DiagnosticCode
	}
	return observation, deploymentID, nil
}

func liveHostdRebindPendingRouteObservationBudget(now time.Time, parent context.Context,
	testDeadline time.Time, hasTestDeadline bool,
) (time.Duration, error) {
	if parent == nil {
		return 0, errors.New("invalid pending route observation context")
	}
	deadlines := make([]time.Time, 0, 2)
	if hasTestDeadline {
		deadlines = append(deadlines, testDeadline)
	}
	if deadline, ok := parent.Deadline(); ok {
		deadlines = append(deadlines, deadline)
	}
	return liveHostdRebindBoundedBudget(now, liveHostdRebindPendingRouteObservationCap, deadlines...)
}

type liveHostdRebindPendingRouteExpectation struct {
	jobID, planID, configurationID  string
	planNumber, configurationNumber int64
}

func liveHostdRebindPendingRouteBoundaryBaseline(observation liveHostdRebindPendingRouteObservation,
	expected liveHostdRebindPendingRouteExpectation,
) (liveHostdRebindPendingRouteObservation, error) {
	if expected.jobID == "" || expected.planID == "" || expected.planNumber < 1 ||
		expected.configurationID == "" || expected.configurationNumber < 1 {
		return liveHostdRebindPendingRouteObservation{}, errors.New("invalid pending route boundary expectation")
	}
	if !observation.observed || observation.appID == "" || observation.jobID != expected.jobID || observation.jobAttempt < 1 ||
		observation.jobInputSize == 0 {
		return liveHostdRebindPendingRouteObservation{}, errors.New("pending route boundary job identity is invalid")
	}
	if observation.jobStatus != string(jobs.Running) || observation.jobPhase != "wait_for_health" ||
		observation.jobProgress != 85 || observation.jobErrorCode != "" {
		return liveHostdRebindPendingRouteObservation{}, errors.New("pending route boundary job state is invalid")
	}
	if !observation.deploymentFound || observation.deploymentID == "" || observation.deploymentReleaseID == "" ||
		observation.deploymentPlanID != expected.planID || observation.deploymentPlanNumber != expected.planNumber ||
		observation.deploymentConfigID != expected.configurationID ||
		observation.deploymentConfigNumber != expected.configurationNumber ||
		observation.deploymentStatus != deployments.WaitingHealth || observation.deploymentDiagnosticCode != "" {
		return liveHostdRebindPendingRouteObservation{}, errors.New("pending route boundary deployment identity is invalid")
	}
	if !observation.runtimeFound || observation.runtimeDeploymentID != observation.deploymentID ||
		observation.runtimeReleaseID != observation.deploymentReleaseID ||
		observation.runtimePlanID != expected.planID || observation.runtimePlanNumber != expected.planNumber ||
		observation.runtimeCandidateSlot == "" || observation.runtimePreviousID == "" ||
		observation.runtimePreviousSlot == "" || observation.runtimeCandidateSlot == observation.runtimePreviousSlot ||
		observation.runtimePhase != generatedruntimestate.PhaseSwitchingRoute ||
		observation.runtimeDiagnosticCode != "" {
		return liveHostdRebindPendingRouteObservation{}, errors.New("pending route boundary runtime identity is invalid")
	}
	if !liveHostdRebindPendingRouteComponentsMatchState(observation,
		generatedruntimestate.ComponentHealthy) {
		return liveHostdRebindPendingRouteObservation{}, errors.New("pending route boundary candidate identity is invalid")
	}
	if !observation.activeHeadFound || observation.activeHead.AppID != observation.appID ||
		observation.activeHead.DeploymentID != observation.runtimePreviousID ||
		observation.activeHead.ReleaseID == "" || observation.activeHead.Slot != observation.runtimePreviousSlot ||
		observation.activeHead.Generation < 1 {
		return liveHostdRebindPendingRouteObservation{}, errors.New("pending route boundary current head is invalid")
	}
	return observation, nil
}

func liveHostdRebindPendingRouteComponentsMatchState(observation liveHostdRebindPendingRouteObservation,
	want generatedruntimestate.ComponentState,
) bool {
	if len(observation.componentIdentities) != 2 ||
		len(observation.componentStates) != len(observation.componentIdentities) ||
		len(observation.componentDiagnosticCodes) != len(observation.componentIdentities) {
		return false
	}
	if observation.componentIdentities[0].name != "api" || observation.componentIdentities[1].name != "frontend" {
		return false
	}
	for index, identity := range observation.componentIdentities {
		if identity.name == "" || identity.slot != observation.runtimeCandidateSlot || identity.imageArtifactID == "" ||
			identity.containerName == "" || identity.containerID == "" || observation.componentStates[index] != want ||
			observation.componentDiagnosticCodes[index] != "" ||
			(index > 0 && (observation.componentIdentities[index-1].name == identity.name ||
				!liveHostdRebindPendingRouteComponentIdentityLess(
					observation.componentIdentities[index-1], identity))) {
			return false
		}
	}
	return true
}

func liveHostdRebindPendingRouteConverged(baseline, observation liveHostdRebindPendingRouteObservation) error {
	if !baseline.observed || !observation.observed || observation.appID != baseline.appID ||
		observation.jobID != baseline.jobID ||
		observation.jobInputSize != baseline.jobInputSize || observation.jobInputDigest != baseline.jobInputDigest {
		return errors.New("pending route convergence job identity changed")
	}
	if observation.jobAttempt != baseline.jobAttempt+1 {
		return errors.New("pending route convergence attempt is invalid")
	}
	if observation.jobStatus != string(jobs.Succeeded) || observation.jobPhase != "succeeded" ||
		observation.jobProgress != 100 || observation.jobErrorCode != "" {
		return errors.New("pending route convergence job success is incomplete")
	}
	if !observation.deploymentFound || observation.deploymentID != baseline.deploymentID ||
		observation.deploymentReleaseID != baseline.deploymentReleaseID ||
		observation.deploymentPlanID != baseline.deploymentPlanID ||
		observation.deploymentPlanNumber != baseline.deploymentPlanNumber ||
		observation.deploymentConfigID != baseline.deploymentConfigID ||
		observation.deploymentConfigNumber != baseline.deploymentConfigNumber ||
		observation.deploymentStatus != deployments.Succeeded || observation.deploymentDiagnosticCode != "" {
		return errors.New("pending route convergence deployment identity changed")
	}
	if !observation.runtimeFound || observation.runtimeDeploymentID != baseline.runtimeDeploymentID ||
		observation.runtimeReleaseID != baseline.runtimeReleaseID ||
		observation.runtimePlanID != baseline.runtimePlanID ||
		observation.runtimePlanNumber != baseline.runtimePlanNumber ||
		observation.runtimeCandidateSlot != baseline.runtimeCandidateSlot ||
		observation.runtimePreviousID != baseline.runtimePreviousID ||
		observation.runtimePreviousSlot != baseline.runtimePreviousSlot ||
		observation.runtimePhase != generatedruntimestate.PhaseSucceeded || observation.runtimeDiagnosticCode != "" {
		return errors.New("pending route convergence runtime identity changed")
	}
	if !slices.Equal(observation.componentIdentities, baseline.componentIdentities) ||
		!liveHostdRebindPendingRouteComponentsMatchState(observation, generatedruntimestate.ComponentActive) {
		return errors.New("pending route convergence candidate identity changed")
	}
	if !observation.activeHeadFound || observation.activeHead.AppID != baseline.activeHead.AppID ||
		observation.activeHead.DeploymentID != baseline.deploymentID ||
		observation.activeHead.ReleaseID != baseline.deploymentReleaseID ||
		observation.activeHead.Slot != baseline.runtimeCandidateSlot ||
		observation.activeHead.Generation != baseline.activeHead.Generation+1 {
		return errors.New("pending route convergence current head is invalid")
	}
	return nil
}

func liveHostdRebindWaitPendingRouteConvergenceResult(parent context.Context, testDeadline time.Time,
	hasTestDeadline bool, child *liveHostdRebindChild, jobID string,
	baseline liveHostdRebindPendingRouteObservation, snapshot liveHostdRebindPendingRouteSnapshot,
) (liveHostdRebindPendingRouteObservation, error) {
	var last liveHostdRebindPendingRouteObservation
	if child == nil || child.command == nil || child.command.Process == nil || child.waitDone == nil ||
		jobID == "" || baseline.jobID != jobID || snapshot == nil {
		return last, errors.New("invalid pending route convergence observation")
	}
	budget, err := liveHostdRebindPendingRouteObservationBudget(time.Now(), parent, testDeadline, hasTestDeadline)
	if err != nil {
		return last, fmt.Errorf("reserve pending route convergence observation: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	child.startWait()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return last, fmt.Errorf("pending route convergence observation ended: %s snapshot=%s",
				liveHostdRebindFailureCategory(err), last)
		}
		select {
		case <-child.waitDone:
			return last, fmt.Errorf("pending route child exited before exact convergence: %s snapshot=%s",
				liveHostdRebindFailureCategory(child.waitErr), last)
		default:
		}
		last, err = snapshot(ctx, jobID)
		if err != nil {
			return last, errors.New("pending route convergence durable observation failed")
		}
		if err := ctx.Err(); err != nil {
			return last, fmt.Errorf("pending route convergence observation ended: %s snapshot=%s",
				liveHostdRebindFailureCategory(err), last)
		}
		select {
		case <-child.waitDone:
			return last, fmt.Errorf("pending route child exited before exact convergence: %s snapshot=%s",
				liveHostdRebindFailureCategory(child.waitErr), last)
		default:
		}
		if last.jobStatus == string(jobs.Succeeded) {
			if err := liveHostdRebindPendingRouteConverged(baseline, last); err != nil {
				return last, fmt.Errorf("%w snapshot=%s", err, last)
			}
			return last, nil
		}
		if liveHostdRebindPendingRouteTerminal(last.jobStatus) {
			return last, fmt.Errorf("pending route job reached %s before exact convergence snapshot=%s",
				liveHostdRebindSafeJobStatus(last.jobStatus), last)
		}
		select {
		case <-ctx.Done():
		case <-child.waitDone:
		case <-ticker.C:
		}
	}
}

func liveHostdRebindWaitPendingRouteBoundaryResult(parent context.Context, testDeadline time.Time,
	hasTestDeadline bool, child *liveHostdRebindChild, jobID string, snapshot liveHostdRebindPendingRouteSnapshot,
) (liveHostdRebindBoundaryMarker, liveHostdRebindPendingRouteObservation, error) {
	var marker liveHostdRebindBoundaryMarker
	var last liveHostdRebindPendingRouteObservation
	if child == nil || child.command == nil || child.command.Process == nil || child.waitDone == nil ||
		child.manifest.BoundaryMarker == "" || child.manifest.OperationID == "" || jobID == "" || snapshot == nil {
		return marker, last, errors.New("invalid pending route boundary observation")
	}
	budget, err := liveHostdRebindPendingRouteObservationBudget(time.Now(), parent, testDeadline, hasTestDeadline)
	if err != nil {
		return marker, last, fmt.Errorf("reserve pending route observation: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	child.startWait()
	startedDeadline := time.Now().Add(liveHostdRebindPendingRouteStartCap)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return marker, last, fmt.Errorf("pending route boundary observation ended: %s snapshot=%s",
				liveHostdRebindFailureCategory(err), last)
		}
		marker, found, err := liveHostdRebindReadPendingRouteBoundary(child)
		if err != nil {
			return marker, last, err
		}
		if found {
			return marker, last, nil
		}
		select {
		case <-child.waitDone:
			return marker, last, fmt.Errorf("pending route child exited before publication boundary: %s snapshot=%s",
				liveHostdRebindFailureCategory(child.waitErr), last)
		default:
		}
		last, err = snapshot(ctx, jobID)
		if err != nil {
			return marker, last, errors.New("pending route durable observation failed")
		}
		if err := ctx.Err(); err != nil {
			return marker, last, fmt.Errorf("pending route boundary observation ended: %s snapshot=%s",
				liveHostdRebindFailureCategory(err), last)
		}
		// The marker may have become durable while the SQLite snapshot was read.
		// Recheck it before interpreting a terminal snapshot so publication wins
		// the only race that matters to this crash-boundary contract.
		marker, found, err = liveHostdRebindReadPendingRouteBoundary(child)
		if err != nil {
			return marker, last, err
		}
		if found {
			return marker, last, nil
		}
		select {
		case <-child.waitDone:
			return marker, last, fmt.Errorf("pending route child exited before publication boundary: %s snapshot=%s",
				liveHostdRebindFailureCategory(child.waitErr), last)
		default:
		}
		if liveHostdRebindPendingRouteTerminal(last.jobStatus) {
			return marker, last, fmt.Errorf("pending route job reached %s before publication boundary snapshot=%s",
				liveHostdRebindSafeJobStatus(last.jobStatus), last)
		}
		if last.jobStatus == string(jobs.Queued) && time.Now().After(startedDeadline) {
			return marker, last, fmt.Errorf("pending route job did not start before publication boundary snapshot=%s", last)
		}
		select {
		case <-ctx.Done():
		case <-child.waitDone:
		case <-ticker.C:
		}
	}
}

func liveHostdRebindReadPendingRouteBoundary(child *liveHostdRebindChild) (liveHostdRebindBoundaryMarker, bool, error) {
	var marker liveHostdRebindBoundaryMarker
	body, err := os.ReadFile(child.manifest.BoundaryMarker)
	if errors.Is(err, os.ErrNotExist) {
		return marker, false, nil
	}
	if err != nil {
		return marker, false, errors.New("read pending route boundary marker failed")
	}
	if len(body) == 0 || len(body) > 8192 || liveHostdRebindDecodeBoundary(body, &marker) != nil ||
		marker.Boundary != "current_effect_ack_lost" || marker.OperationID != child.manifest.OperationID {
		return liveHostdRebindBoundaryMarker{}, false, errors.New("pending route boundary marker is invalid")
	}
	return marker, true, nil
}

func liveHostdRebindDecodeBoundary(body []byte, marker *liveHostdRebindBoundaryMarker) error {
	return json.Unmarshal(body, marker)
}

func (o liveHostdRebindPendingRouteObservation) String() string {
	if !o.observed {
		return "job=unobserved job_error=none deployment=unobserved deployment_diagnostic=none runtime=unobserved runtime_diagnostic=none components=[] component_diagnostics=[] events=[]"
	}
	deployment, runtime := "absent", "absent"
	if o.deploymentFound {
		deployment = liveHostdRebindSafeDeploymentStatus(o.deploymentStatus)
	}
	if o.runtimeFound {
		runtime = liveHostdRebindSafeRuntimePhase(o.runtimePhase)
	}
	components := make([]string, 0, len(o.componentStates))
	for _, state := range o.componentStates {
		components = append(components, liveHostdRebindSafeComponentState(state))
	}
	componentDiagnostics := make([]string, 0, len(o.componentDiagnosticCodes))
	for _, code := range o.componentDiagnosticCodes {
		componentDiagnostics = append(componentDiagnostics, liveHostdRebindSafeComponentDiagnosticCode(code))
	}
	sort.Strings(componentDiagnostics)
	events := make([]string, 0, len(o.events))
	for _, event := range o.events {
		events = append(events, liveHostdRebindSafeJobPhase(event.Phase)+"/"+liveHostdRebindSafeEventCode(event.Code))
	}
	return fmt.Sprintf("job=%s phase=%s progress=%d job_error=%s deployment=%s deployment_diagnostic=%s runtime=%s runtime_diagnostic=%s components=[%s] component_diagnostics=[%s] events=[%s]",
		liveHostdRebindSafeJobStatus(o.jobStatus), liveHostdRebindSafeJobPhase(o.jobPhase), o.jobProgress,
		liveHostdRebindSafeJobErrorCode(o.jobErrorCode), deployment,
		liveHostdRebindSafeDeploymentDiagnosticCode(o.deploymentDiagnosticCode), runtime,
		liveHostdRebindSafeRuntimeDiagnosticCode(o.runtimeDiagnosticCode), strings.Join(components, ","),
		strings.Join(componentDiagnostics, ","), strings.Join(events, ","))
}

func liveHostdRebindPendingRouteTerminal(status string) bool {
	switch jobs.Status(status) {
	case jobs.WaitingUser, jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return true
	default:
		return false
	}
}

func liveHostdRebindKnownJobStatus(value string) bool {
	switch jobs.Status(value) {
	case jobs.Queued, jobs.Assigned, jobs.Running, jobs.Waiting, jobs.WaitingUser, jobs.Succeeded, jobs.Failed,
		jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeJobStatus(value string) string {
	if liveHostdRebindKnownJobStatus(value) {
		return value
	}
	return "unknown"
}

// Keep this closed to jobs.safeExecutionFailure outputs and the worker's one
// direct invalid-result code. Job error detail remains deliberately omitted.
func liveHostdRebindSafeJobErrorCode(value string) string {
	if value == "" {
		return "none"
	}
	switch value {
	case "validation_failed", "runtime_unavailable", "process_termination_failed", "invalid_source",
		"source_unavailable", "source_access_lost", "provider_unavailable", "source_too_large",
		"source_storage_full", "configuration_unavailable", "configuration_review_required",
		"reviewed_revision_stale", "reviewed_revisions_required", "build_configuration_requires_build_command",
		"compose_invalid", "compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated",
		"policy_rejected", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
		"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "internal_error",
		"executor_failed", "executor_invalid_result":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindSafeJobPhase(value string) string {
	switch value {
	case "queued", "assigned", "validate", "prepare_workspace", "materialize_release", "render_compose",
		"evaluate_policy", "apply_runtime", "apply_fake_runtime", "wait_for_health", "finalize", "running",
		"waiting_external", "cancelling", "approval_required", "migration_approval_required",
		"insufficient_replacement_capacity", "route_reconciliation_required", "succeeded", "failed", "cancelled",
		"interrupted", "needs_attention", "waiting_user":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindSafeEventCode(value string) string {
	switch value {
	case "job_queued", "job_resumed", "job_assigned", "phase_started", "waiting_external",
		"cancellation_requested", "job_cancelled", "job_failed", "job_succeeded", "approval_required",
		"migration_approval_required", "insufficient_replacement_capacity", "route_reconciliation_required":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindKnownDeploymentStatus(value deployments.Status) bool {
	switch value {
	case deployments.Preparing, deployments.Applying, deployments.WaitingHealth, deployments.Succeeded,
		deployments.Failed, deployments.Cancelled, deployments.NeedsAttention:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeDeploymentStatus(value deployments.Status) string {
	if liveHostdRebindKnownDeploymentStatus(value) {
		return string(value)
	}
	return "unknown"
}

// Keep this closed to deployments.knownDiagnostic. Failure summary remains
// deliberately omitted.
func liveHostdRebindSafeDeploymentDiagnosticCode(value string) string {
	if value == "" {
		return "none"
	}
	switch value {
	case "daemon_restarted", "runtime_unavailable", "process_termination_failed", "compose_invalid",
		"compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated", "policy_rejected",
		"approval_required", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
		"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "cancelled", "internal_error",
		"invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full",
		"provider_unavailable", "configuration_unavailable", "configuration_review_required",
		"build_configuration_requires_build_command":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindKnownRuntimePhase(value generatedruntimestate.Phase) bool {
	switch value {
	case generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding,
		generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded,
		generatedruntimestate.PhaseFailed, generatedruntimestate.PhaseCancelled:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeRuntimePhase(value generatedruntimestate.Phase) string {
	if liveHostdRebindKnownRuntimePhase(value) {
		return string(value)
	}
	return "unknown"
}

// Keep this closed to generatedruntimestate.knownDiagnostic.
func liveHostdRebindSafeRuntimeDiagnosticCode(value generatedruntimestate.DiagnosticCode) string {
	if value == "" {
		return "none"
	}
	switch value {
	case generatedruntimestate.DiagnosticInsufficientReplacementCapacity,
		generatedruntimestate.DiagnosticPlanReviewRequired,
		generatedruntimestate.DiagnosticMigrationFailed,
		generatedruntimestate.DiagnosticStartFailed,
		generatedruntimestate.DiagnosticHealthFailed,
		generatedruntimestate.DiagnosticRouteSwitchFailed,
		generatedruntimestate.DiagnosticRuntimeUnavailable,
		generatedruntimestate.DiagnosticDaemonRestarted,
		generatedruntimestate.DiagnosticCancelled,
		generatedruntimestate.DiagnosticInternalError:
		return string(value)
	default:
		return "unknown"
	}
}

func liveHostdRebindKnownComponentState(value generatedruntimestate.ComponentState) bool {
	switch value {
	case generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady,
		generatedruntimestate.ComponentStarting, generatedruntimestate.ComponentRunning,
		generatedruntimestate.ComponentHealthy, generatedruntimestate.ComponentActive,
		generatedruntimestate.ComponentDraining, generatedruntimestate.ComponentStopped,
		generatedruntimestate.ComponentFailed:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeComponentState(value generatedruntimestate.ComponentState) string {
	if liveHostdRebindKnownComponentState(value) {
		return string(value)
	}
	return "unknown"
}

// Keep this closed to generatedruntimestate.componentDiagnostic.
func liveHostdRebindSafeComponentDiagnosticCode(value generatedruntimestate.DiagnosticCode) string {
	if value == "" {
		return "none"
	}
	switch value {
	case generatedruntimestate.DiagnosticStartFailed,
		generatedruntimestate.DiagnosticHealthFailed,
		generatedruntimestate.DiagnosticRuntimeUnavailable,
		generatedruntimestate.DiagnosticDaemonRestarted,
		generatedruntimestate.DiagnosticCancelled,
		generatedruntimestate.DiagnosticInternalError:
		return string(value)
	default:
		return "unknown"
	}
}

func liveHostdRebindPendingRouteObservationControls(t *testing.T) {
	t.Helper()
	const (
		operationID = "d5228ec6-7325-458f-851c-345277d57e1c"
		jobID       = "8d13dfa0-457c-4b8b-86f2-5f30dbefcbb1"
		appID       = "a358bfec-63bd-41c4-af16-af7f264f3f00"
		secret      = "private-pending-route-control-secret"
	)
	startChild := func(t *testing.T, mode string) *liveHostdRebindChild {
		t.Helper()
		directory := t.TempDir()
		child := liveHostdRebindStartStartupControlChild(t, directory, "", mode, secret)
		child.manifest.OperationID = operationID
		child.manifest.BoundaryMarker = filepath.Join(directory, "boundary.json")
		return child
	}
	nonterminal := func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
		return liveHostdRebindPendingRouteObservation{observed: true, jobStatus: string(jobs.Running), jobPhase: "apply_runtime", jobProgress: 70}, nil
	}
	writeMarker := func(t *testing.T, child *liveHostdRebindChild, marker liveHostdRebindBoundaryMarker) {
		t.Helper()
		if err := liveHostdRebindWritePrivateJSON(child.manifest.BoundaryMarker, marker); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(child *liveHostdRebindChild, deadline time.Time, snapshot liveHostdRebindPendingRouteSnapshot) (liveHostdRebindBoundaryMarker, liveHostdRebindPendingRouteObservation, error) {
		return liveHostdRebindWaitPendingRouteBoundaryResult(context.Background(), deadline, true, child, jobID, snapshot)
	}
	expectation := liveHostdRebindPendingRouteExpectation{
		jobID: jobID, planID: "8df741a9-58b9-48a3-8c83-54c06b29fc32", planNumber: 3,
		configurationID: "e7bd3565-1290-42f5-99d6-c66983f22a43", configurationNumber: 4,
	}
	boundaryObservation := func() liveHostdRebindPendingRouteObservation {
		input := []byte(`{"expectedConfigurationRevisionNumber":4}`)
		return liveHostdRebindPendingRouteObservation{
			observed: true, appID: appID, jobID: jobID, jobAttempt: 1, jobInputSize: len(input),
			jobInputDigest: sha256.Sum256(input), jobStatus: string(jobs.Running), jobPhase: "wait_for_health",
			jobProgress: 85, deploymentFound: true, deploymentID: "2ad6ab89-5ae6-4b98-a14b-a9e623110615",
			deploymentReleaseID: "release-next", deploymentPlanID: expectation.planID,
			deploymentPlanNumber: expectation.planNumber, deploymentConfigID: expectation.configurationID,
			deploymentConfigNumber: expectation.configurationNumber, deploymentStatus: deployments.WaitingHealth,
			runtimeFound: true, runtimeDeploymentID: "2ad6ab89-5ae6-4b98-a14b-a9e623110615",
			runtimeReleaseID: "release-next", runtimePlanID: expectation.planID,
			runtimePlanNumber: expectation.planNumber, runtimeCandidateSlot: "green",
			runtimePreviousID: "c4517430-68df-4e93-ab96-0ba4e958966f", runtimePreviousSlot: "blue",
			runtimePhase: generatedruntimestate.PhaseSwitchingRoute,
			componentIdentities: []liveHostdRebindPendingRouteComponentIdentity{
				{name: "api", slot: "green", imageArtifactID: "sha256:api", containerName: "api-green", containerID: "sha256:api-container"},
				{name: "frontend", slot: "green", imageArtifactID: "sha256:frontend", containerName: "frontend-green", containerID: "sha256:frontend-container"},
			},
			componentStates: []generatedruntimestate.ComponentState{
				generatedruntimestate.ComponentHealthy, generatedruntimestate.ComponentHealthy,
			},
			componentDiagnosticCodes: []generatedruntimestate.DiagnosticCode{"", ""},
			activeHeadFound:          true,
			activeHead: generatedruntimestate.ActiveHead{
				AppID: appID, DeploymentID: "c4517430-68df-4e93-ab96-0ba4e958966f", ReleaseID: "release-previous",
				Slot: "blue", Generation: 7,
			},
		}
	}
	convergedObservation := func(baseline liveHostdRebindPendingRouteObservation) liveHostdRebindPendingRouteObservation {
		result := baseline
		result.jobAttempt = baseline.jobAttempt + 1
		result.jobStatus, result.jobPhase, result.jobProgress = string(jobs.Succeeded), "succeeded", 100
		result.deploymentStatus = deployments.Succeeded
		result.runtimePhase = generatedruntimestate.PhaseSucceeded
		result.componentIdentities = append([]liveHostdRebindPendingRouteComponentIdentity(nil), baseline.componentIdentities...)
		result.componentStates = []generatedruntimestate.ComponentState{
			generatedruntimestate.ComponentActive, generatedruntimestate.ComponentActive,
		}
		result.componentDiagnosticCodes = []generatedruntimestate.DiagnosticCode{"", ""}
		result.activeHead = generatedruntimestate.ActiveHead{
			AppID: baseline.activeHead.AppID, DeploymentID: baseline.deploymentID,
			ReleaseID: baseline.deploymentReleaseID, Slot: baseline.runtimeCandidateSlot,
			Generation: baseline.activeHead.Generation + 1,
		}
		return result
	}
	waitConvergence := func(child *liveHostdRebindChild, deadline time.Time,
		baseline liveHostdRebindPendingRouteObservation, snapshot liveHostdRebindPendingRouteSnapshot,
	) (liveHostdRebindPendingRouteObservation, error) {
		return liveHostdRebindWaitPendingRouteConvergenceResult(context.Background(), deadline, true,
			child, jobID, baseline, snapshot)
	}

	t.Run("budget preserves both cleanup reserves and finite cap", func(t *testing.T) {
		now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
		for _, test := range []struct {
			name        string
			parent      context.Context
			deadline    time.Time
			hasDeadline bool
			want        time.Duration
			wantErr     bool
		}{
			{name: "finite cap", parent: context.Background(), want: liveHostdRebindPendingRouteObservationCap},
			{name: "short test deadline", parent: context.Background(), deadline: now.Add(liveHostdRebindStartupCleanupReserve + 90*time.Second), hasDeadline: true, want: 90 * time.Second},
			{name: "short fixture deadline", parent: liveHostdRebindControlDeadlineContext(t, now.Add(liveHostdRebindStartupCleanupReserve+30*time.Second)), want: 30 * time.Second},
			{name: "shortest known deadline", parent: liveHostdRebindControlDeadlineContext(t, now.Add(liveHostdRebindStartupCleanupReserve+30*time.Second)), deadline: now.Add(liveHostdRebindStartupCleanupReserve + time.Minute), hasDeadline: true, want: 30 * time.Second},
			{name: "reserve exhausted", parent: context.Background(), deadline: now.Add(liveHostdRebindStartupCleanupReserve), hasDeadline: true, wantErr: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, err := liveHostdRebindPendingRouteObservationBudget(now, test.parent, test.deadline, test.hasDeadline)
				if test.wantErr {
					if err == nil {
						t.Fatal("pending route budget accepted an exhausted cleanup reserve")
					}
					return
				}
				if err != nil || got != test.want {
					t.Fatalf("pending route budget=%s error=%v want=%s", got, err, test.want)
				}
			})
		}
	})

	t.Run("held marker baseline and exact resumed convergence", func(t *testing.T) {
		child := startChild(t, "stall")
		writeMarker(t, child, liveHostdRebindBoundaryMarker{Boundary: "current_effect_ack_lost", OperationID: operationID})
		reads := 0
		marker, first, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
			reads++
			return boundaryObservation(), nil
		})
		if err != nil || marker.Boundary != "current_effect_ack_lost" || reads != 0 || first.observed {
			t.Fatalf("preexisting marker marker=%+v reads=%d first=%s error=%v", marker, reads, first, err)
		}
		baseline, err := liveHostdRebindPendingRouteBoundaryBaseline(boundaryObservation(), expectation)
		if err != nil {
			t.Fatal(err)
		}
		pending := baseline
		pending.jobAttempt++
		final := convergedObservation(baseline)
		reads = 0
		got, err := waitConvergence(child, time.Now().Add(time.Hour), baseline,
			func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
				reads++
				if reads == 1 {
					return pending, nil
				}
				return final, nil
			})
		if err != nil || reads != 2 || got.jobStatus != string(jobs.Succeeded) ||
			got.activeHead.DeploymentID != baseline.deploymentID {
			t.Fatalf("exact convergence reads=%d observation=%s error=%v", reads, got, err)
		}
		liveHostdRebindKillAndReap(t, child)
	})

	t.Run("held boundary refuses incomplete or mismatched identity", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*liveHostdRebindPendingRouteObservation)
		}{
			{name: "job", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.jobID = secret }},
			{name: "configuration", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.deploymentConfigID = secret }},
			{name: "plan", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.deploymentPlanID = secret }},
			{name: "runtime phase", mutate: func(value *liveHostdRebindPendingRouteObservation) {
				value.runtimePhase = generatedruntimestate.PhaseWaitingHealth
			}},
			{name: "candidate state", mutate: func(value *liveHostdRebindPendingRouteObservation) {
				value.componentStates[0] = generatedruntimestate.ComponentRunning
			}},
			{name: "candidate identity", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.componentIdentities[0].containerID = "" }},
			{name: "prior current head", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.activeHead.DeploymentID = secret }},
		} {
			t.Run(test.name, func(t *testing.T) {
				value := boundaryObservation()
				value.componentIdentities = append([]liveHostdRebindPendingRouteComponentIdentity(nil), value.componentIdentities...)
				value.componentStates = append([]generatedruntimestate.ComponentState(nil), value.componentStates...)
				test.mutate(&value)
				_, err := liveHostdRebindPendingRouteBoundaryBaseline(value, expectation)
				if err == nil || strings.Contains(err.Error(), secret) {
					t.Fatalf("invalid held boundary accepted: %v", err)
				}
			})
		}
	})

	t.Run("resumed convergence refuses changed durable identity", func(t *testing.T) {
		baseline, err := liveHostdRebindPendingRouteBoundaryBaseline(boundaryObservation(), expectation)
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			name   string
			mutate func(*liveHostdRebindPendingRouteObservation)
		}{
			{name: "attempt", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.jobAttempt++ }},
			{name: "job input", mutate: func(value *liveHostdRebindPendingRouteObservation) {
				value.jobInputDigest = sha256.Sum256([]byte(secret))
			}},
			{name: "configuration pin", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.deploymentConfigID = secret }},
			{name: "plan pin", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.deploymentPlanNumber++ }},
			{name: "runtime deployment", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.runtimeDeploymentID = secret }},
			{name: "candidate identity", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.componentIdentities[0].containerID = secret }},
			{name: "component state", mutate: func(value *liveHostdRebindPendingRouteObservation) {
				value.componentStates[0] = generatedruntimestate.ComponentHealthy
			}},
			{name: "current deployment", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.activeHead.DeploymentID = secret }},
			{name: "current generation", mutate: func(value *liveHostdRebindPendingRouteObservation) { value.activeHead.Generation++ }},
		} {
			t.Run(test.name, func(t *testing.T) {
				value := convergedObservation(baseline)
				value.componentIdentities = append([]liveHostdRebindPendingRouteComponentIdentity(nil), value.componentIdentities...)
				value.componentStates = append([]generatedruntimestate.ComponentState(nil), value.componentStates...)
				test.mutate(&value)
				err := liveHostdRebindPendingRouteConverged(baseline, value)
				if err == nil || strings.Contains(err.Error(), secret) {
					t.Fatalf("changed durable identity accepted: %v", err)
				}
			})
		}
	})

	t.Run("resumed convergence refuses every terminal failure", func(t *testing.T) {
		baseline, err := liveHostdRebindPendingRouteBoundaryBaseline(boundaryObservation(), expectation)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range []jobs.Status{jobs.WaitingUser, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention} {
			t.Run(string(status), func(t *testing.T) {
				child := startChild(t, "stall")
				terminal := baseline
				terminal.jobAttempt++
				terminal.jobStatus, terminal.jobPhase = string(status), string(status)
				_, err := waitConvergence(child, time.Now().Add(time.Hour), baseline,
					func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) { return terminal, nil })
				if err == nil || !strings.Contains(err.Error(), "before exact convergence") || strings.Contains(err.Error(), secret) {
					t.Fatalf("terminal status=%s error=%v", status, err)
				}
				liveHostdRebindKillAndReap(t, child)
			})
		}
	})

	t.Run("resumed convergence child exit timeout and observer errors are closed", func(t *testing.T) {
		baseline, err := liveHostdRebindPendingRouteBoundaryBaseline(boundaryObservation(), expectation)
		if err != nil {
			t.Fatal(err)
		}
		pending := baseline
		pending.jobAttempt++
		t.Run("child exit", func(t *testing.T) {
			child := startChild(t, "exit")
			_, err := waitConvergence(child, time.Now().Add(time.Hour), baseline,
				func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) { return pending, nil })
			if err == nil || !strings.Contains(err.Error(), "child exited") || strings.Contains(err.Error(), secret) {
				t.Fatalf("child exit error=%v", err)
			}
			liveHostdRebindAssertChildReaped(t, child)
		})
		t.Run("timeout", func(t *testing.T) {
			child := startChild(t, "stall")
			_, err := waitConvergence(child, time.Now().Add(liveHostdRebindStartupCleanupReserve+100*time.Millisecond),
				baseline, func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) { return pending, nil })
			if err == nil || !strings.Contains(err.Error(), "deadline_exceeded") || strings.Contains(err.Error(), secret) {
				t.Fatalf("timeout error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
		t.Run("observer error", func(t *testing.T) {
			child := startChild(t, "stall")
			_, err := waitConvergence(child, time.Now().Add(time.Hour), baseline,
				func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
					return liveHostdRebindPendingRouteObservation{}, errors.New(secret)
				})
			if err == nil || !strings.Contains(err.Error(), "durable observation failed") || strings.Contains(err.Error(), secret) {
				t.Fatalf("observer error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
		t.Run("post snapshot cancellation", func(t *testing.T) {
			child := startChild(t, "stall")
			parent, cancel := context.WithCancel(context.Background())
			_, err := liveHostdRebindWaitPendingRouteConvergenceResult(parent, time.Now().Add(time.Hour), true,
				child, jobID, baseline, func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
					cancel()
					return convergedObservation(baseline), nil
				})
			if err == nil || !strings.Contains(err.Error(), "observation ended: canceled") ||
				!strings.Contains(err.Error(), "job=succeeded") || strings.Contains(err.Error(), secret) {
				t.Fatalf("post snapshot cancellation error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
		t.Run("post snapshot child exit", func(t *testing.T) {
			child := startChild(t, "stall")
			_, err := waitConvergence(child, time.Now().Add(time.Hour), baseline,
				func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
					if err := liveHostdRebindKillAndReapError(child); err != nil {
						t.Fatal(err)
					}
					return convergedObservation(baseline), nil
				})
			if err == nil || !strings.Contains(err.Error(), "child exited") ||
				!strings.Contains(err.Error(), "job=succeeded") || strings.Contains(err.Error(), secret) {
				t.Fatalf("post snapshot child exit error=%v", err)
			}
			liveHostdRebindAssertChildReaped(t, child)
		})
	})

	t.Run("durable marker wins an actual terminal ordering race", func(t *testing.T) {
		child := startChild(t, "stall")
		reads := 0
		marker, _, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
			reads++
			writeMarker(t, child, liveHostdRebindBoundaryMarker{Boundary: "current_effect_ack_lost", OperationID: operationID})
			return liveHostdRebindPendingRouteObservation{observed: true, jobStatus: string(jobs.Failed), jobPhase: "failed", jobProgress: 70}, nil
		})
		if err != nil || marker.Boundary != "current_effect_ack_lost" || reads != 1 {
			t.Fatalf("marker ordering marker=%+v error=%v reads=%d", marker, err, reads)
		}
		liveHostdRebindKillAndReap(t, child)
	})

	t.Run("terminal job before marker fails", func(t *testing.T) {
		for _, status := range []jobs.Status{jobs.WaitingUser, jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention} {
			t.Run(string(status), func(t *testing.T) {
				child := startChild(t, "stall")
				_, _, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
					return liveHostdRebindPendingRouteObservation{
						observed: true, jobStatus: string(status), jobPhase: string(status), jobProgress: 80,
						jobErrorCode: "apply_failed", deploymentFound: true, deploymentStatus: deployments.Failed,
						deploymentDiagnosticCode: "process_termination_failed", runtimeFound: true,
						runtimePhase:          generatedruntimestate.PhaseFailed,
						runtimeDiagnosticCode: generatedruntimestate.DiagnosticStartFailed,
						componentStates:       []generatedruntimestate.ComponentState{generatedruntimestate.ComponentFailed},
						componentDiagnosticCodes: []generatedruntimestate.DiagnosticCode{
							generatedruntimestate.DiagnosticInternalError,
						},
					}, nil
				})
				diagnostic := fmt.Sprint(err)
				if err == nil || !strings.Contains(diagnostic, "before publication boundary") ||
					!strings.Contains(diagnostic, "job_error=apply_failed") ||
					!strings.Contains(diagnostic, "deployment_diagnostic=process_termination_failed") ||
					!strings.Contains(diagnostic, "runtime_diagnostic=start_failed") ||
					!strings.Contains(diagnostic, "component_diagnostics=[internal_error]") ||
					strings.Contains(diagnostic, secret) {
					t.Fatalf("terminal status=%s error=%v", status, err)
				}
				liveHostdRebindKillAndReap(t, child)
			})
		}
	})

	t.Run("child exit is finite and does not expose stderr", func(t *testing.T) {
		child := startChild(t, "exit")
		_, _, err := wait(child, time.Now().Add(time.Hour), nonterminal)
		if err == nil || !strings.Contains(err.Error(), "child exited") || strings.Contains(err.Error(), secret) {
			t.Fatalf("child exit error=%v", err)
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	t.Run("timeout uses scenario cleanup to reap the exact child", func(t *testing.T) {
		child := startChild(t, "stall")
		_, observation, err := wait(child, time.Now().Add(liveHostdRebindStartupCleanupReserve+100*time.Millisecond), nonterminal)
		if err == nil || !strings.Contains(err.Error(), "deadline_exceeded") || observation.jobStatus != string(jobs.Running) {
			t.Fatalf("bounded timeout observation=%s error=%v", observation, err)
		}
		cancelled, joined := false, false
		cleanup := liveHostdRebindPendingCurrentCleanup{
			cancel: func() { cancelled = true },
			reap: func() error {
				if !cancelled {
					return errors.New("cleanup reaped before request cancellation")
				}
				return liveHostdRebindKillAndReapError(child)
			},
			join: func() error { joined = true; return nil },
		}
		if reapErr, joinErr := cleanup.run(); reapErr != nil || joinErr != nil || !joined {
			t.Fatalf("timeout cleanup reaping=%v joining=%v joined=%t", reapErr, joinErr, joined)
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	t.Run("invalid markers and observer failures are closed", func(t *testing.T) {
		t.Run("invalid marker", func(t *testing.T) {
			child := startChild(t, "stall")
			writeMarker(t, child, liveHostdRebindBoundaryMarker{Boundary: "wrong", OperationID: secret})
			_, _, err := wait(child, time.Now().Add(time.Hour), nonterminal)
			if err == nil || !strings.Contains(err.Error(), "marker is invalid") || strings.Contains(err.Error(), secret) {
				t.Fatalf("invalid marker error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
		t.Run("observer error", func(t *testing.T) {
			child := startChild(t, "stall")
			_, _, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
				return liveHostdRebindPendingRouteObservation{}, errors.New(secret)
			})
			if err == nil || !strings.Contains(err.Error(), "durable observation failed") || strings.Contains(err.Error(), secret) {
				t.Fatalf("observer error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
	})

	t.Run("exact job binding rejects foreign durable records", func(t *testing.T) {
		base := jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: appID,
			Status: string(jobs.Running), Phase: "apply_runtime", Progress: 70}
		for _, test := range []struct {
			name        string
			job         jobs.Job
			events      []jobs.Event
			deployments []deployments.Deployment
		}{
			{name: "foreign job", job: func() jobs.Job { value := base; value.ID = secret; return value }()},
			{name: "foreign event", job: base, events: []jobs.Event{{JobID: secret}}},
			{name: "foreign exact deployment", job: base, deployments: []deployments.Deployment{{ID: "deployment", AppID: secret, JobID: jobID, Status: deployments.Applying}}},
			{name: "duplicate exact deployment", job: base, deployments: []deployments.Deployment{{ID: "one", AppID: appID, JobID: jobID, Status: deployments.Applying}, {ID: "two", AppID: appID, JobID: jobID, Status: deployments.Applying}}},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, _, err := liveHostdRebindPendingRouteObservationFor(appID, jobID, test.job, test.events, test.deployments)
				if err == nil || strings.Contains(err.Error(), secret) {
					t.Fatalf("exact binding error=%v", err)
				}
			})
		}
	})

	t.Run("exact job binding selects only the requested records", func(t *testing.T) {
		job := jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: appID,
			Status: string(jobs.Running), Phase: "apply_runtime", Progress: 70, ErrorCode: "apply_failed"}
		events := make([]jobs.Event, 0, liveHostdRebindPendingRouteEventTail+2)
		for index := 0; index < liveHostdRebindPendingRouteEventTail+2; index++ {
			events = append(events, jobs.Event{JobID: jobID, Phase: "apply_runtime", Code: "phase_started"})
		}
		observation, deploymentID, err := liveHostdRebindPendingRouteObservationFor(appID, jobID, job, events,
			[]deployments.Deployment{
				{ID: "foreign", AppID: appID, JobID: "foreign-job", Status: deployments.Succeeded, DiagnosticCode: "runtime_unavailable"},
				{ID: "exact", AppID: appID, JobID: jobID, Status: deployments.Applying, DiagnosticCode: "apply_failed"},
			})
		if err != nil || deploymentID != "exact" || !observation.deploymentFound ||
			observation.jobErrorCode != "apply_failed" || observation.deploymentStatus != deployments.Applying ||
			observation.deploymentDiagnosticCode != "apply_failed" ||
			len(observation.events) != liveHostdRebindPendingRouteEventTail {
			t.Fatalf("exact binding deployment=%q observation=%s error=%v", deploymentID, observation, err)
		}
	})

	t.Run("real SQLite observer binds exact durable state and closes private fields", func(t *testing.T) {
		db, err := database.Open(filepath.Join(t.TempDir(), "observer"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error("close pending route SQLite observer control")
			}
		}()

		actorID, planID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		configurationID, foreignConfigurationID, foreignAppID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`,
			actorID, actorID, "hash", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`,
			appID, appID, "Pending route observer", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`,
			foreignAppID, foreignAppID, "Foreign pending route observer", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
			id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
			analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
			field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
		) VALUES(?,?,1,?,'generated_node','observer-control','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
			planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret", strings.Repeat("a", 64),
			strings.Repeat("b", 64), strings.Repeat("c", 64), actorID, now, actorID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO releases(
			id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
			workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`,
			releaseID, appID, now, strings.Repeat("d", 64), strings.Repeat("e", 64), planID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(
			id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
			bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,1,?,?,?,1,1,2,?,1),(?,?,2,?,?,?,1,1,2,?,1)`,
			configurationID, appID, "apps/"+appID+"/configuration/"+configurationID+".secret", actorID, now, planID,
			foreignConfigurationID, appID, "apps/"+appID+"/configuration/"+foreignConfigurationID+".secret", actorID, now, planID); err != nil {
			t.Fatal(err)
		}

		jobStore, deploymentStore, runtimeStore := jobs.New(db), deployments.New(db), generatedruntimestate.New(db)
		priorJob, created, err := jobStore.CreateWithInput(jobs.CreateRequest{
			Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID, Input: jobs.NoInput{},
		})
		if err != nil || !created {
			t.Fatalf("create prior active observer job: created=%t error=%v", created, err)
		}
		priorDeployment, created, err := deploymentStore.GetOrCreateByJob(context.Background(), appID, priorJob.ID,
			string(jobs.ConfigurationCurrent))
		if err != nil || !created {
			t.Fatalf("create prior active observer deployment: created=%t error=%v", created, err)
		}
		if _, err := deploymentStore.InitializeRuntime(context.Background(), appID, priorDeployment.ID, releaseID,
			configurationID, 1, deployments.RuntimeGeneratedNode, planID, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, priorDeployment.ID, deployments.Applying, ""); err != nil {
			t.Fatal(err)
		}
		if _, created, err := runtimeStore.Begin(context.Background(), generatedruntimestate.BeginInput{
			DeploymentID: priorDeployment.ID, AppID: appID, ReleaseID: releaseID,
			DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"},
		}); err != nil || !created {
			t.Fatalf("begin prior active observer runtime: created=%t error=%v", created, err)
		}
		artifactID := uuid.NewString()
		if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
			id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,
			build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
		) VALUES(?,?,?,?,?,'observer-control',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, "api",
			strings.Repeat("f", 64), "sha256:"+strings.Repeat("a", 64), now, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.SetImageReady(context.Background(), appID, priorDeployment.ID, "api", artifactID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.SetContainerStarting(context.Background(), appID, priorDeployment.ID, "api",
			"rig-observer-blue-api"); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.SetContainerRunning(context.Background(), appID, priorDeployment.ID, "api",
			strings.Repeat("b", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.AdvanceComponent(context.Background(), appID, priorDeployment.ID, "api",
			generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
			t.Fatal(err)
		}
		for _, transition := range []struct {
			from, to generatedruntimestate.Phase
		}{
			{generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding},
			{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate},
			{generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth},
			{generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute},
		} {
			if _, err := runtimeStore.Advance(context.Background(), appID, priorDeployment.ID,
				transition.from, transition.to, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, priorDeployment.ID, deployments.WaitingHealth, ""); err != nil {
			t.Fatal(err)
		}
		priorHead, switched, err := runtimeStore.SwitchActive(context.Background(), appID, priorDeployment.ID, 0)
		if err != nil || !switched || priorHead.DeploymentID != priorDeployment.ID || priorHead.Generation != 1 {
			t.Fatalf("establish prior active observer head: head=%#v switched=%t error=%v", priorHead, switched, err)
		}
		if _, err := runtimeStore.Advance(context.Background(), appID, priorDeployment.ID,
			generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.Advance(context.Background(), appID, priorDeployment.ID,
			generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, priorDeployment.ID, deployments.Succeeded, ""); err != nil {
			t.Fatal(err)
		}
		// This observer fixture represents an already completed prior deployment.
		// Retain its terminal job row before admitting another mutation, just as
		// the failed historical job below is materialized directly in SQLite.
		if _, err := db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',progress_percent=100,
			attempt=1,started_at=?,finished_at=?,updated_at=? WHERE id=?`, now, now, now, priorJob.ID); err != nil {
			t.Fatal(err)
		}

		foreignJob, created, err := jobStore.CreateWithInput(jobs.CreateRequest{
			Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID,
			Input: jobs.DeploymentInput{
				ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: planID, ExpectedPlanRevisionNumber: 1,
				ExpectedConfigurationRevisionID: foreignConfigurationID, ExpectedConfigurationRevisionNumber: 2,
			},
		})
		if err != nil || !created {
			t.Fatalf("create historical observer job: created=%t error=%v", created, err)
		}
		foreignDeployment, created, err := deploymentStore.GetOrCreateByJob(context.Background(), appID, foreignJob.ID,
			string(jobs.ConfigurationCurrent))
		if err != nil || !created {
			t.Fatalf("create historical observer deployment: created=%t error=%v", created, err)
		}
		if _, err := deploymentStore.InitializeRuntime(context.Background(), appID, foreignDeployment.ID, releaseID,
			foreignConfigurationID, 2, deployments.RuntimeGeneratedNode, planID, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE jobs SET status='failed',phase='failed',progress_percent=60,error_code='runtime_unavailable',error_detail=?,finished_at=?,updated_at=? WHERE id=?`,
			secret, now, now, foreignJob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployments SET status='failed',diagnostic_code='runtime_unavailable',failure_summary=? WHERE id=?`,
			secret, foreignDeployment.ID); err != nil {
			t.Fatal(err)
		}

		exactJob, created, err := jobStore.CreateWithInput(jobs.CreateRequest{
			Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID,
			Input: jobs.DeploymentInput{
				ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: planID, ExpectedPlanRevisionNumber: 1,
				ExpectedConfigurationRevisionID: configurationID, ExpectedConfigurationRevisionNumber: 1,
			},
		})
		if err != nil || !created {
			t.Fatalf("create exact observer job: created=%t error=%v", created, err)
		}
		exactDeployment, created, err := deploymentStore.GetOrCreateByJob(context.Background(), appID, exactJob.ID,
			string(jobs.ConfigurationCurrent))
		if err != nil || !created {
			t.Fatalf("create exact observer deployment: created=%t error=%v", created, err)
		}
		if _, err := deploymentStore.InitializeRuntime(context.Background(), appID, exactDeployment.ID, releaseID, configurationID, 1,
			deployments.RuntimeGeneratedNode, planID, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, exactDeployment.ID, deployments.Applying, ""); err != nil {
			t.Fatal(err)
		}
		if _, created, err := runtimeStore.Begin(context.Background(), generatedruntimestate.BeginInput{
			DeploymentID: exactDeployment.ID, AppID: appID, ReleaseID: releaseID,
			DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{secret},
		}); err != nil || !created {
			t.Fatalf("begin exact observer runtime: created=%t error=%v", created, err)
		}
		if _, err := db.Exec(`UPDATE jobs SET status='assigned',phase='assigned',attempt=1,updated_at=? WHERE id=?`,
			now, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		if err := jobStore.Update(exactJob.ID, jobs.Running, "apply_runtime", 70, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.FailComponent(context.Background(), appID, exactDeployment.ID, secret,
			generatedruntimestate.ComponentPending, generatedruntimestate.DiagnosticInternalError); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.Advance(context.Background(), appID, exactDeployment.ID,
			generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseFailed,
			generatedruntimestate.DiagnosticStartFailed); err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, exactDeployment.ID,
			deployments.Failed, "process_termination_failed"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE jobs SET status='failed',phase='failed',checkpoint_json=?,error_code='apply_failed',error_detail=?,finished_at=?,updated_at=? WHERE id=?`,
			`{"private":"`+secret+`"}`, secret, now, now, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployments SET failure_summary=? WHERE id=?`, secret, exactDeployment.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE job_events SET message=? WHERE job_id=? AND code='phase_started'`, secret, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		persisted, err := jobStore.Get(exactJob.ID)
		events, eventsErr := jobStore.Events(exactJob.ID, 0)
		persistedForeignDeployment, foreignDeploymentErr := deploymentStore.Get(context.Background(), appID, foreignDeployment.ID)
		foreignHead, foreignHeadErr := runtimeStore.Active(context.Background(), foreignAppID)
		privateEvent := len(events) == 2 && events[1].Message == secret
		if err != nil || eventsErr != nil || !strings.Contains(persisted.Checkpoint, secret) ||
			persisted.ErrorCode != "apply_failed" || persisted.ErrorDetail != secret || !privateEvent {
			t.Fatalf("private durable observer fixture was not established: job_error=%v events_error=%v private_event=%t",
				err, eventsErr, privateEvent)
		}
		if foreignDeploymentErr != nil || persistedForeignDeployment.ActualConfigurationRevisionID != foreignConfigurationID ||
			persistedForeignDeployment.ActualConfigurationRevisionNumber != 2 || foreignHeadErr != nil ||
			foreignHead.AppID != foreignAppID || foreignHead.DeploymentID != "" || foreignHead.ReleaseID != "" ||
			foreignHead.Slot != "" || foreignHead.Generation != 0 {
			t.Fatalf("foreign durable observer fixture was not established: deployment_error=%v head_error=%v",
				foreignDeploymentErr, foreignHeadErr)
		}

		observation, err := liveHostdRebindNewPendingRouteObserver(db, appID).snapshot(context.Background(), exactJob.ID)
		diagnostic := observation.String()
		privateDiagnostic := slices.ContainsFunc([]string{
			secret, appID, exactJob.ID, exactDeployment.ID, releaseID, planID, configurationID,
			priorDeployment.ID, foreignAppID, foreignJob.ID, foreignDeployment.ID, foreignConfigurationID,
		}, func(value string) bool { return strings.Contains(diagnostic, value) })
		if err != nil || observation.jobID != exactJob.ID || observation.jobAttempt != 1 || observation.jobInputSize == 0 ||
			observation.jobStatus != string(jobs.Failed) || observation.jobPhase != "failed" ||
			observation.jobProgress != 70 || observation.jobErrorCode != "apply_failed" ||
			!observation.deploymentFound || observation.deploymentID != exactDeployment.ID ||
			observation.deploymentReleaseID != releaseID || observation.deploymentPlanID != planID ||
			observation.deploymentPlanNumber != 1 || observation.deploymentConfigID != configurationID ||
			observation.deploymentConfigNumber != 1 || observation.deploymentStatus != deployments.Failed ||
			observation.deploymentDiagnosticCode != "process_termination_failed" || !observation.runtimeFound ||
			observation.runtimeDeploymentID != exactDeployment.ID || observation.runtimeReleaseID != releaseID ||
			observation.runtimePlanID != planID || observation.runtimePlanNumber != 1 ||
			observation.runtimeCandidateSlot == "" || observation.runtimeCandidateSlot == priorHead.Slot ||
			observation.runtimePreviousID != priorDeployment.ID || observation.runtimePreviousSlot != priorHead.Slot ||
			observation.runtimePhase != generatedruntimestate.PhaseFailed ||
			observation.runtimeDiagnosticCode != generatedruntimestate.DiagnosticStartFailed ||
			len(observation.componentIdentities) != 1 || observation.componentIdentities[0].name != secret ||
			len(observation.componentStates) != 1 || observation.componentStates[0] != generatedruntimestate.ComponentFailed ||
			len(observation.componentDiagnosticCodes) != 1 ||
			observation.componentDiagnosticCodes[0] != generatedruntimestate.DiagnosticInternalError ||
			!observation.activeHeadFound || observation.activeHead.AppID != appID ||
			observation.activeHead.DeploymentID != priorDeployment.ID || observation.activeHead.ReleaseID != releaseID ||
			observation.activeHead.Slot != priorHead.Slot || observation.activeHead.Generation != priorHead.Generation ||
			len(observation.events) != 2 || privateDiagnostic ||
			!strings.Contains(diagnostic, "job_error=apply_failed") ||
			!strings.Contains(diagnostic, "deployment=failed deployment_diagnostic=process_termination_failed") ||
			!strings.Contains(diagnostic, "runtime=failed runtime_diagnostic=start_failed") ||
			!strings.Contains(diagnostic, "components=[failed] component_diagnostics=[internal_error]") {
			t.Fatalf("real observer state invalid: error=%v historical_deployment_selected=%t private_diagnostic=%t",
				err, observation.deploymentStatus == foreignDeployment.Status, privateDiagnostic)
		}

		if _, err := db.Exec(`UPDATE jobs SET error_code=? WHERE id=?`, secret, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployments SET diagnostic_code=? WHERE id=?`, secret, exactDeployment.ID); err != nil {
			t.Fatal(err)
		}
		unknown, err := liveHostdRebindNewPendingRouteObserver(db, appID).snapshot(context.Background(), exactJob.ID)
		unknownDiagnostic := unknown.String()
		if err != nil || strings.Contains(unknownDiagnostic, secret) ||
			!strings.Contains(unknownDiagnostic, "job_error=unknown") ||
			!strings.Contains(unknownDiagnostic, "deployment_diagnostic=unknown") ||
			!strings.Contains(unknownDiagnostic, "runtime_diagnostic=start_failed") ||
			!strings.Contains(unknownDiagnostic, "component_diagnostics=[internal_error]") {
			t.Fatalf("real observer unknown-code closure failed: error=%v private=%t diagnostic=%s",
				err, strings.Contains(unknownDiagnostic, secret), unknownDiagnostic)
		}
	})

	t.Run("diagnostic allowlist excludes durable private fields", func(t *testing.T) {
		observation := liveHostdRebindPendingRouteObservation{
			observed: true, jobStatus: secret, jobPhase: secret, jobProgress: 42, jobErrorCode: secret,
			deploymentFound: true, deploymentStatus: deployments.Status(secret), deploymentDiagnosticCode: secret,
			runtimeFound: true, runtimePhase: generatedruntimestate.Phase(secret),
			runtimeDiagnosticCode: generatedruntimestate.DiagnosticCode(secret),
			componentStates:       []generatedruntimestate.ComponentState{generatedruntimestate.ComponentState(secret)},
			componentDiagnosticCodes: []generatedruntimestate.DiagnosticCode{
				generatedruntimestate.DiagnosticCode(secret),
			},
			events: []jobs.Event{{Phase: secret, Code: secret, Message: secret}},
		}
		diagnostic := observation.String()
		if privateDiagnostic, closedUnknown := strings.Contains(diagnostic, secret), strings.Contains(diagnostic, "unknown"); privateDiagnostic || !closedUnknown {
			t.Fatalf("diagnostic sanitization failed: private=%t closed_unknown=%t", privateDiagnostic, closedUnknown)
		}
	})

	t.Run("diagnostic code allowlists are exact and closed", func(t *testing.T) {
		domains := []struct {
			name   string
			values []string
			safe   func(string) string
		}{
			{
				name: "job error",
				values: []string{
					"validation_failed", "runtime_unavailable", "process_termination_failed", "invalid_source",
					"source_unavailable", "source_access_lost", "provider_unavailable", "source_too_large",
					"source_storage_full", "configuration_unavailable", "configuration_review_required",
					"reviewed_revision_stale", "reviewed_revisions_required", "build_configuration_requires_build_command",
					"compose_invalid", "compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated",
					"policy_rejected", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
					"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "internal_error",
					"executor_failed", "executor_invalid_result",
				},
				safe: liveHostdRebindSafeJobErrorCode,
			},
			{
				name: "deployment diagnostic",
				values: []string{
					"daemon_restarted", "runtime_unavailable", "process_termination_failed", "compose_invalid",
					"compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated", "policy_rejected",
					"approval_required", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
					"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "cancelled", "internal_error",
					"invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full",
					"provider_unavailable", "configuration_unavailable", "configuration_review_required",
					"build_configuration_requires_build_command",
				},
				safe: liveHostdRebindSafeDeploymentDiagnosticCode,
			},
			{
				name: "runtime diagnostic",
				values: []string{
					"insufficient_replacement_capacity", "deployment_plan_review_required", "migration_failed", "start_failed",
					"health_failed", "route_switch_failed", "runtime_unavailable", "daemon_restarted", "cancelled", "internal_error",
				},
				safe: func(value string) string {
					return liveHostdRebindSafeRuntimeDiagnosticCode(generatedruntimestate.DiagnosticCode(value))
				},
			},
			{
				name: "component diagnostic",
				values: []string{
					"start_failed", "health_failed", "runtime_unavailable", "daemon_restarted", "cancelled", "internal_error",
				},
				safe: func(value string) string {
					return liveHostdRebindSafeComponentDiagnosticCode(generatedruntimestate.DiagnosticCode(value))
				},
			},
		}
		for _, domain := range domains {
			t.Run(domain.name, func(t *testing.T) {
				if got := domain.safe(""); got != "none" {
					t.Fatalf("empty diagnostic=%q", got)
				}
				for _, value := range domain.values {
					if got := domain.safe(value); got != value {
						t.Fatalf("known diagnostic %q=%q", value, got)
					}
				}
				if got := domain.safe(secret); got != "unknown" || strings.Contains(got, secret) {
					t.Fatalf("unknown diagnostic=%q", got)
				}
			})
		}
	})
}
