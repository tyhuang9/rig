// Package generatedexecutor coordinates release-pinned generated runtimes.
// It owns orchestration only; compilation, Docker execution, ingress, and
// durable state remain behind narrow purpose-specific interfaces.
package generatedexecutor

import (
	"context"
	"errors"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/releasesnapshot"
)

var ErrInsufficientReplacementCapacity = errors.New("insufficient generated replacement capacity")

type applicationReader interface {
	Get(string) (apps.Application, error)
}

type releaseResolver interface {
	Materialize(context.Context, string, string) (releasesnapshot.Release, error)
	MaterializeLocal(context.Context, string, string) (releasesnapshot.Release, error)
	ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error)
}

type configurationExporter interface {
	ExportCurrentForExecution(context.Context, string) (appconfig.ExecutionConfiguration, error)
	ExportRevisionForExecution(context.Context, string, string, int64) (appconfig.ExecutionConfiguration, error)
}

type deploymentStore interface {
	GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error)
	Get(context.Context, string, string) (deployments.Deployment, error)
	InitializeRuntime(context.Context, string, string, string, string, int64, deployments.RuntimeStrategy, string, int64) (deployments.Deployment, error)
	Transition(context.Context, string, string, deployments.Status, string) (deployments.Deployment, error)
}

type planReader interface {
	GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error)
}

type imageCompiler interface {
	Compile(context.Context, string, string, string) (generatedimage.Artifact, error)
}

type artifactStore interface {
	Get(context.Context, string) (generatedimage.Artifact, error)
	MarkUnavailable(context.Context, string) (generatedimage.Artifact, error)
}

type runtimeState interface {
	Begin(context.Context, generatedruntimestate.BeginInput) (generatedruntimestate.Deployment, bool, error)
	Get(context.Context, string, string) (generatedruntimestate.Deployment, error)
	Active(context.Context, string) (generatedruntimestate.ActiveHead, error)
	Advance(context.Context, string, string, generatedruntimestate.Phase, generatedruntimestate.Phase, generatedruntimestate.DiagnosticCode) (generatedruntimestate.Deployment, error)
	BeginMigration(context.Context, string, string) (generatedruntimestate.Deployment, error)
	FinishMigration(context.Context, string, string, bool) (generatedruntimestate.Deployment, error)
	SetImageReady(context.Context, string, string, string, string) (generatedruntimestate.Component, error)
	SetContainerStarting(context.Context, string, string, string, string) (generatedruntimestate.Component, error)
	SetContainerRunning(context.Context, string, string, string, string) (generatedruntimestate.Component, error)
	AdvanceComponent(context.Context, string, string, string, generatedruntimestate.ComponentState, generatedruntimestate.ComponentState) (generatedruntimestate.Component, error)
	FailComponent(context.Context, string, string, string, generatedruntimestate.ComponentState, generatedruntimestate.DiagnosticCode) (generatedruntimestate.Component, error)
	SwitchActive(context.Context, string, string, int64) (generatedruntimestate.ActiveHead, bool, error)
}

type runtimeEngine interface {
	ReserveReplacement(context.Context, int) (generatedruntime.ReplacementReservation, error)
	ValidateImage(context.Context, generatedruntime.ImageSpec) error
	EnsureAppNetwork(context.Context, string) error
	CreateInactiveCandidate(context.Context, generatedruntime.CandidateSpec) (generatedruntime.Candidate, error)
	AdoptCandidate(generatedruntime.Candidate, generatedruntime.ReplacementReservation) (generatedruntime.Candidate, error)
	StartCandidate(context.Context, generatedruntime.Candidate) error
	WaitHealthy(context.Context, generatedruntime.Candidate) error
	StopAndRemove(context.Context, generatedruntime.Candidate, time.Duration) error
	ReleaseAdmission(generatedruntime.Candidate)
}

// AuthorizationRequest contains only immutable identities and aggregate
// capacity facts. Commands and configuration must never cross this boundary.
type AuthorizationRequest struct {
	AppID                        string
	DeploymentID                 string
	ReleaseID                    string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	CandidateSlot                generatedruntime.Slot
	ComponentCount               int
}

// AuthorizationGate is the final pre-mutation transaction. Implementations
// must be idempotent for an already authorized deployment, recheck approvals
// and capacity, and move the main deployment to Applying before returning.
type AuthorizationGate interface {
	Authorize(context.Context, AuthorizationRequest) (generatedruntime.ReplacementReservation, error)
}

type Options struct {
	DrainPeriod    time.Duration
	CleanupTimeout time.Duration
}
