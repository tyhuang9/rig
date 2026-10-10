package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const (
	lanTwoAppFailureHistoryLimit     = 10
	lanTwoAppFailureDiagnosticBudget = 2 * time.Second
)

type lanTwoAppDeploymentList func(context.Context, string, int) ([]deployments.Deployment, error)
type lanTwoAppRuntimeGet func(context.Context, string, string) (generatedruntimestate.Deployment, error)

type lanTwoAppComponentStateCounts struct {
	Pending, ImageReady, Starting, Running, Healthy, Active, Draining, Stopped, Failed, Unknown int
}

type lanTwoAppDeploymentFailureObservation struct {
	DeploymentRead, DeploymentStatus, RuntimeRead, RuntimePhase, MigrationState string
	Components                                                                  lanTwoAppComponentStateCounts
}

func lanTwoAppLogDeploymentFailure(t *testing.T, ctx context.Context, appID, jobID string,
	list lanTwoAppDeploymentList, get lanTwoAppRuntimeGet,
) {
	t.Helper()
	observation := lanTwoAppDeploymentFailureObservationFor(ctx, appID, jobID, list, get)
	t.Log(lanTwoAppDeploymentFailureObservationLine(observation))
}

func lanTwoAppFailureDiagnosticContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), lanTwoAppFailureDiagnosticBudget)
}

func lanTwoAppDeploymentFailureObservationLine(observation lanTwoAppDeploymentFailureObservation) string {
	return fmt.Sprintf("LAN deployment failure diagnostic: deployment_read=%s deployment_status=%s runtime_read=%s runtime_phase=%s migration_state=%s components=pending:%d,image_ready:%d,starting:%d,running:%d,healthy:%d,active:%d,draining:%d,stopped:%d,failed:%d,unknown:%d",
		observation.DeploymentRead, observation.DeploymentStatus, observation.RuntimeRead, observation.RuntimePhase, observation.MigrationState,
		observation.Components.Pending, observation.Components.ImageReady, observation.Components.Starting, observation.Components.Running,
		observation.Components.Healthy, observation.Components.Active, observation.Components.Draining, observation.Components.Stopped,
		observation.Components.Failed, observation.Components.Unknown)
}

func lanTwoAppDeploymentFailureObservationFor(parent context.Context, appID, jobID string,
	list lanTwoAppDeploymentList, get lanTwoAppRuntimeGet,
) lanTwoAppDeploymentFailureObservation {
	ctx, cancel := lanTwoAppFailureDiagnosticContext(parent)
	defer cancel()
	observation := lanTwoAppDeploymentFailureObservation{
		DeploymentRead: "unavailable", DeploymentStatus: "unavailable", RuntimeRead: "unavailable",
		RuntimePhase: "unavailable", MigrationState: "unavailable",
	}
	history, err := list(ctx, appID, lanTwoAppFailureHistoryLimit)
	if err != nil {
		observation.DeploymentRead = "failed"
		return observation
	}
	var matched *deployments.Deployment
	for index := range history {
		if history[index].JobID != jobID {
			continue
		}
		if matched != nil {
			observation.DeploymentRead = "ambiguous"
			return observation
		}
		matched = &history[index]
	}
	if matched == nil {
		observation.DeploymentRead = "missing"
		return observation
	}
	if matched.AppID != appID || matched.ID == "" {
		observation.DeploymentRead = "identity_mismatch"
		return observation
	}
	observation.DeploymentRead = "ok"
	observation.DeploymentStatus = lanTwoAppDeploymentStatusLabel(matched.Status)
	runtimeState, err := get(ctx, appID, matched.ID)
	if err != nil {
		observation.RuntimeRead = "failed"
		return observation
	}
	if runtimeState.AppID != appID || runtimeState.DeploymentID != matched.ID {
		observation.RuntimeRead = "identity_mismatch"
		return observation
	}
	observation.RuntimeRead = "ok"
	observation.RuntimePhase = lanTwoAppRuntimePhaseLabel(runtimeState.Phase)
	observation.MigrationState = lanTwoAppMigrationStateLabel(runtimeState.MigrationState)
	for _, component := range runtimeState.Components {
		switch component.State {
		case generatedruntimestate.ComponentPending:
			observation.Components.Pending++
		case generatedruntimestate.ComponentImageReady:
			observation.Components.ImageReady++
		case generatedruntimestate.ComponentStarting:
			observation.Components.Starting++
		case generatedruntimestate.ComponentRunning:
			observation.Components.Running++
		case generatedruntimestate.ComponentHealthy:
			observation.Components.Healthy++
		case generatedruntimestate.ComponentActive:
			observation.Components.Active++
		case generatedruntimestate.ComponentDraining:
			observation.Components.Draining++
		case generatedruntimestate.ComponentStopped:
			observation.Components.Stopped++
		case generatedruntimestate.ComponentFailed:
			observation.Components.Failed++
		default:
			observation.Components.Unknown++
		}
	}
	return observation
}

func lanTwoAppDeploymentStatusLabel(value deployments.Status) string {
	switch value {
	case deployments.Preparing, deployments.Applying, deployments.WaitingHealth, deployments.Succeeded, deployments.Failed, deployments.Cancelled, deployments.NeedsAttention:
		return string(value)
	default:
		return "unknown"
	}
}

func lanTwoAppRuntimePhaseLabel(value generatedruntimestate.Phase) string {
	switch value {
	case generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseMigrating,
		generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, generatedruntimestate.PhaseFailed, generatedruntimestate.PhaseCancelled:
		return string(value)
	default:
		return "unknown"
	}
}

func lanTwoAppMigrationStateLabel(value generatedruntimestate.MigrationState) string {
	switch value {
	case generatedruntimestate.MigrationNotRequired, generatedruntimestate.MigrationPending, generatedruntimestate.MigrationRunning,
		generatedruntimestate.MigrationSucceeded, generatedruntimestate.MigrationFailed:
		return string(value)
	default:
		return "unknown"
	}
}

func TestLanTwoAppDeploymentFailureObservationIsAllowlisted(t *testing.T) {
	const appID = "application-secret"
	const jobID = "job-secret"
	const deploymentID = "deployment-secret"
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	var listedApp string
	var listedLimit int
	var listContext, getContext context.Context
	var readApp, readDeployment string
	observation := lanTwoAppDeploymentFailureObservationFor(parent, appID, jobID,
		func(ctx context.Context, app string, limit int) ([]deployments.Deployment, error) {
			listContext, listedApp, listedLimit = ctx, app, limit
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []deployments.Deployment{
				{ID: "unrelated-deployment-secret", AppID: appID, JobID: "unrelated-job-secret", Status: deployments.Failed},
				{ID: deploymentID, AppID: appID, JobID: jobID, Status: deployments.Applying},
			}, nil
		},
		func(ctx context.Context, app, deployment string) (generatedruntimestate.Deployment, error) {
			getContext, readApp, readDeployment = ctx, app, deployment
			if err := ctx.Err(); err != nil {
				return generatedruntimestate.Deployment{}, err
			}
			return generatedruntimestate.Deployment{
				DeploymentID: deploymentID, AppID: appID, Phase: generatedruntimestate.PhaseWaitingHealth,
				MigrationState: generatedruntimestate.MigrationRunning,
				Components: []generatedruntimestate.Component{
					{Name: "component-name-secret", ImageArtifactID: "artifact-secret", ContainerName: "container-secret", ContainerID: "container-id-secret", State: generatedruntimestate.ComponentHealthy},
					{State: generatedruntimestate.ComponentState("component-state-secret")},
				},
			}, nil
		},
	)
	if listedApp != appID || listedLimit != lanTwoAppFailureHistoryLimit || readApp != appID || readDeployment != deploymentID || listContext != getContext {
		t.Fatalf("diagnostic read scope list=%q/%d get=%q/%q shared=%t", listedApp, listedLimit, readApp, readDeployment, listContext == getContext)
	}
	if observation.DeploymentRead != "ok" || observation.DeploymentStatus != "applying" || observation.RuntimeRead != "ok" || observation.RuntimePhase != "waiting_health" || observation.MigrationState != "running" || observation.Components.Healthy != 1 || observation.Components.Unknown != 1 {
		t.Fatalf("diagnostic observation=%#v", observation)
	}
	line := lanTwoAppDeploymentFailureObservationLine(observation)
	for _, forbidden := range []string{"secret", appID, jobID, deploymentID, "component-name", "artifact", "container"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("allowlisted diagnostic leaked %q: %s", forbidden, line)
		}
	}
}

func TestLanTwoAppFailureDiagnosticContextIgnoresParentCancellationAndDeadline(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent func() context.Context
	}{
		{
			name: "cancelled parent",
			parent: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "expired deadline",
			parent: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				return ctx
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := lanTwoAppFailureDiagnosticContext(test.parent())
			defer cancel()
			if err := ctx.Err(); err != nil {
				t.Fatalf("diagnostic context error=%v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > lanTwoAppFailureDiagnosticBudget {
				t.Fatalf("diagnostic deadline=%v present=%t", deadline, ok)
			}
		})
	}
}

func TestLanTwoAppDeploymentFailureObservationRejectsUnreadableOrAmbiguousState(t *testing.T) {
	for _, test := range []struct {
		name            string
		list            lanTwoAppDeploymentList
		get             lanTwoAppRuntimeGet
		wantDeployment  string
		wantRuntime     string
		wantGetCalls    int
		forbiddenInLine string
	}{
		{
			name: "list read failure",
			list: func(context.Context, string, int) ([]deployments.Deployment, error) {
				return nil, errors.New("list-read-secret")
			},
			wantDeployment: "failed", wantRuntime: "unavailable", forbiddenInLine: "list-read-secret",
		},
		{
			name: "missing job",
			list: func(context.Context, string, int) ([]deployments.Deployment, error) {
				return []deployments.Deployment{{ID: "other-deployment-secret", AppID: "application-secret", JobID: "other-job-secret"}}, nil
			},
			wantDeployment: "missing", wantRuntime: "unavailable", forbiddenInLine: "other-job-secret",
		},
		{
			name: "ambiguous job",
			list: func(context.Context, string, int) ([]deployments.Deployment, error) {
				return []deployments.Deployment{{ID: "first-secret", AppID: "application-secret", JobID: "job"}, {ID: "second-secret", AppID: "application-secret", JobID: "job"}}, nil
			},
			wantDeployment: "ambiguous", wantRuntime: "unavailable", forbiddenInLine: "first-secret",
		},
		{
			name: "crossed deployment identity",
			list: func(context.Context, string, int) ([]deployments.Deployment, error) {
				return []deployments.Deployment{{ID: "deployment-secret", AppID: "other-application-secret", JobID: "job"}}, nil
			},
			wantDeployment: "identity_mismatch", wantRuntime: "unavailable", forbiddenInLine: "other-application-secret",
		},
		{
			name: "runtime read failure",
			list: func(context.Context, string, int) ([]deployments.Deployment, error) {
				return []deployments.Deployment{{ID: "deployment-secret", AppID: "application-secret", JobID: "job", Status: deployments.Status("status-secret")}}, nil
			},
			get: func(context.Context, string, string) (generatedruntimestate.Deployment, error) {
				return generatedruntimestate.Deployment{}, errors.New("runtime-read-secret")
			},
			wantDeployment: "ok", wantRuntime: "failed", wantGetCalls: 1, forbiddenInLine: "runtime-read-secret",
		},
		{
			name: "crossed runtime identity",
			list: func(context.Context, string, int) ([]deployments.Deployment, error) {
				return []deployments.Deployment{{ID: "deployment-secret", AppID: "application-secret", JobID: "job"}}, nil
			},
			get: func(context.Context, string, string) (generatedruntimestate.Deployment, error) {
				return generatedruntimestate.Deployment{AppID: "application-secret", DeploymentID: "other-deployment-secret", Components: []generatedruntimestate.Component{{State: generatedruntimestate.ComponentHealthy}}}, nil
			},
			wantDeployment: "ok", wantRuntime: "identity_mismatch", wantGetCalls: 1, forbiddenInLine: "other-deployment-secret",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			getCalls := 0
			get := test.get
			if get == nil {
				get = func(context.Context, string, string) (generatedruntimestate.Deployment, error) {
					getCalls++
					return generatedruntimestate.Deployment{}, errors.New("unexpected-state-read-secret")
				}
			} else {
				delegate := get
				get = func(ctx context.Context, appID, deploymentID string) (generatedruntimestate.Deployment, error) {
					getCalls++
					return delegate(ctx, appID, deploymentID)
				}
			}
			observation := lanTwoAppDeploymentFailureObservationFor(context.Background(), "application-secret", "job", test.list, get)
			if observation.DeploymentRead != test.wantDeployment || observation.RuntimeRead != test.wantRuntime || getCalls != test.wantGetCalls {
				t.Fatalf("diagnostic observation=%#v getCalls=%d", observation, getCalls)
			}
			if observation.RuntimeRead == "identity_mismatch" && observation.Components != (lanTwoAppComponentStateCounts{}) {
				t.Fatalf("identity-mismatched state contributed components=%#v", observation.Components)
			}
			line := lanTwoAppDeploymentFailureObservationLine(observation)
			if strings.Contains(line, test.forbiddenInLine) || strings.Contains(line, "application-secret") || strings.Contains(line, "status-secret") {
				t.Fatalf("allowlisted diagnostic leaked input: %s", line)
			}
		})
	}
}
