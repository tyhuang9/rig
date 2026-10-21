package generatedingress_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntime/liveauthority"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type liveRuntimeAdapter struct{}

func init() {
	generatedingress.RegisterLiveRuntimeTestAdapter(liveRuntimeAdapter{})
}

type liveRuntimeEngine struct {
	value *generatedruntime.Engine
}

func (e *liveRuntimeEngine) StopAndRemove(ctx context.Context, candidate generatedingress.LiveRuntimeTestCandidate, grace time.Duration) error {
	if e == nil || e.value == nil {
		return errors.New("live runtime engine is unavailable")
	}
	return e.value.StopAndRemove(ctx, runtimeCandidate(candidate), grace)
}

func (e *liveRuntimeEngine) ReserveReplacement(ctx context.Context, request generatedingress.GeneratedReplacementRequest) (generatedingress.GeneratedReplacementReservation, error) {
	if e == nil || e.value == nil {
		return nil, errors.New("live runtime engine is unavailable")
	}
	return e.value.ReserveReplacement(ctx, request)
}

type liveRuntimeAuthority struct {
	value *liveauthority.Authority
}

func (a *liveRuntimeAuthority) Repository() *deployments.Repository {
	if a == nil || a.value == nil {
		return nil
	}
	return a.value.Repository()
}

func (a *liveRuntimeAuthority) PrepareCandidate(t *testing.T, ctx context.Context, spec generatedingress.LiveRuntimeTestCandidateSpec) generatedingress.LiveRuntimeTestCandidateSpec {
	return testCandidateSpec(a.value.PrepareCandidate(t, ctx, runtimeCandidateSpec(spec)))
}

func (a *liveRuntimeAuthority) BindNetwork(t *testing.T, ctx context.Context, spec generatedingress.LiveRuntimeTestCandidateSpec) generatedingress.LiveRuntimeTestCandidateSpec {
	return testCandidateSpec(a.value.BindNetwork(t, ctx, runtimeCandidateSpec(spec)))
}

func (a *liveRuntimeAuthority) CancelNetwork(t *testing.T, ctx context.Context, deploymentID string) {
	a.value.CancelNetwork(t, ctx, deploymentID)
}

func (a *liveRuntimeAuthority) Begin(t *testing.T, ctx context.Context, engine generatedingress.LiveRuntimeTestEngine, spec generatedingress.LiveRuntimeTestCandidateSpec) generatedingress.LiveRuntimeTestSession {
	wrapped, ok := engine.(*liveRuntimeEngine)
	if !ok || wrapped == nil || wrapped.value == nil {
		t.Fatal("live authority requires the registered real runtime engine")
	}
	return &liveRuntimeSession{value: a.value.Begin(t, ctx, wrapped.value, runtimeCandidateSpec(spec))}
}

type liveRuntimeSession struct {
	value *liveauthority.CandidateSession
}

func (s *liveRuntimeSession) BoundSpec() generatedingress.LiveRuntimeTestCandidateSpec {
	return testCandidateSpec(s.value.BoundSpec())
}

func (s *liveRuntimeSession) Candidate() generatedingress.LiveRuntimeTestCandidate {
	return testCandidate(s.value.Candidate())
}

func (s *liveRuntimeSession) Start(t *testing.T, spec generatedingress.LiveRuntimeTestCandidateSpec) (generatedingress.LiveRuntimeTestCandidate, error) {
	value, err := s.value.Start(t, runtimeCandidateSpec(spec))
	return testCandidate(value), err
}

func (s *liveRuntimeSession) WaitHealthyAndHandoff() error { return s.value.WaitHealthyAndHandoff() }
func (s *liveRuntimeSession) RejectObservedFailure() error { return s.value.RejectObservedFailure() }
func (s *liveRuntimeSession) RoutePublished(slot generatedruntime.Slot, endpoints []generatedroute.RouteEndpoint) error {
	return s.value.RoutePublished(slot, endpoints)
}
func (s *liveRuntimeSession) PublishedHistory(ctx context.Context, db *sql.DB) (generatedruntimestate.ActiveHead, error) {
	return s.value.PublishedHistory(ctx, db)
}
func (s *liveRuntimeSession) Close() { s.value.Close() }

type candidateEnvironmentStager struct {
	value generatedingress.GeneratedCandidateEnvironmentStager
}

func (s candidateEnvironmentStager) Stage(operationID string, attempt int, contents []byte) (generatedruntime.EnvironmentLease, error) {
	return s.value.StageGeneratedCandidate(operationID, attempt, contents)
}

func (s candidateEnvironmentStager) StageGeneratedCandidate(operationID string, attempt int, contents []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	return s.value.StageGeneratedCandidate(operationID, attempt, contents)
}

func (liveRuntimeAdapter) NewAuthority(t *testing.T, db *sql.DB, workingDirectory string) generatedingress.LiveRuntimeTestAuthority {
	return &liveRuntimeAuthority{value: liveauthority.New(t, db, workingDirectory)}
}

func (liveRuntimeAdapter) NewEngine(runner runtimeprocess.CommandRunner, environment generatedingress.GeneratedCandidateEnvironmentStager, capacity generatedcapacity.Source, repository *deployments.Repository, options generatedingress.LiveRuntimeTestEngineOptions) (generatedingress.LiveRuntimeTestEngine, error) {
	if environment == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	environmentValue := reflect.ValueOf(environment)
	if environmentValue.Kind() != reflect.Pointer || environmentValue.IsNil() {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	value, err := generatedruntime.NewEngine(runner, &candidateEnvironmentStager{value: environment}, capacity, repository, generatedruntime.EngineOptions{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint,
		DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
		CommandTimeout: options.CommandTimeout, HealthTimeout: options.HealthTimeout,
		HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		Limits: generatedruntime.ContainerLimits{
			MemoryBytes: options.Limits.MemoryBytes, MilliCPUs: options.Limits.MilliCPUs, PIDs: options.Limits.PIDs,
			TmpfsBytes: options.Limits.TmpfsBytes, LogSize: options.Limits.LogSize, LogFiles: options.Limits.LogFiles,
		},
		ReplacementDiskBytes: options.ReplacementDiskBytes,
	})
	if err != nil {
		return nil, err
	}
	return &liveRuntimeEngine{value: value}, nil
}

func (liveRuntimeAdapter) IsCode(err error, code string) bool {
	return generatedruntime.IsCode(err, generatedruntime.DiagnosticCode(code))
}

func (liveRuntimeAdapter) ErrorCode(err error) (string, bool) {
	var runtimeErr *generatedruntime.Error
	if !errors.As(err, &runtimeErr) || runtimeErr == nil {
		return "", false
	}
	return string(runtimeErr.Code), true
}

func (liveRuntimeAdapter) NewError(code string) error {
	return &generatedruntime.Error{Code: generatedruntime.DiagnosticCode(code)}
}

func (liveRuntimeAdapter) RouteCandidateMayBeLive(err error) bool {
	return generatedruntime.RouteCandidateMayBeLive(err)
}

func (liveRuntimeAdapter) RouteGatewayReadinessFailed(err error) bool {
	return generatedruntime.RouteGatewayReadinessFailed(err)
}

func runtimeCandidateSpec(value generatedingress.LiveRuntimeTestCandidateSpec) generatedruntime.CandidateSpec {
	return generatedruntime.CandidateSpec{
		AppID: value.AppID, ReleaseID: value.ReleaseID, DeploymentID: value.DeploymentID, ArtifactID: value.ArtifactID,
		DeploymentPlanRevisionID: value.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: value.DeploymentPlanRevisionNumber,
		ConfigurationRevisionID: value.ConfigurationRevisionID, ConfigurationRevisionNumber: value.ConfigurationRevisionNumber,
		ComponentName: value.ComponentName, Role: value.Role, Technology: value.Technology, RootDirectory: value.RootDirectory,
		RunCommand: value.RunCommand, InternalPort: value.InternalPort, HealthProbe: value.HealthProbe,
		ImageContentID: value.ImageContentID, BuildDefinitionDigest: value.BuildDefinitionDigest, ActiveSlot: value.ActiveSlot,
		EnvironmentOperationID: value.EnvironmentOperationID, EnvironmentOperationAttempt: value.EnvironmentOperationAttempt,
		Environment: value.Environment, Reservation: value.Reservation, Network: value.Network,
	}
}

func testCandidateSpec(value generatedruntime.CandidateSpec) generatedingress.LiveRuntimeTestCandidateSpec {
	return generatedingress.LiveRuntimeTestCandidateSpec{
		AppID: value.AppID, ReleaseID: value.ReleaseID, DeploymentID: value.DeploymentID, ArtifactID: value.ArtifactID,
		DeploymentPlanRevisionID: value.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: value.DeploymentPlanRevisionNumber,
		ConfigurationRevisionID: value.ConfigurationRevisionID, ConfigurationRevisionNumber: value.ConfigurationRevisionNumber,
		ComponentName: value.ComponentName, Role: value.Role, Technology: value.Technology, RootDirectory: value.RootDirectory,
		RunCommand: value.RunCommand, InternalPort: value.InternalPort, HealthProbe: value.HealthProbe,
		ImageContentID: value.ImageContentID, BuildDefinitionDigest: value.BuildDefinitionDigest, ActiveSlot: value.ActiveSlot,
		EnvironmentOperationID: value.EnvironmentOperationID, EnvironmentOperationAttempt: value.EnvironmentOperationAttempt,
		Environment: append([]byte(nil), value.Environment...), Reservation: value.Reservation, Network: value.Network,
	}
}

func runtimeCandidate(value generatedingress.LiveRuntimeTestCandidate) generatedruntime.Candidate {
	return generatedruntime.Candidate{
		AppID: value.AppID, ReleaseID: value.ReleaseID, DeploymentID: value.DeploymentID, ArtifactID: value.ArtifactID,
		DeploymentPlanRevisionID: value.DeploymentPlanRevisionID, Component: value.Component, Role: value.Role,
		Technology: value.Technology, Slot: value.Slot, ContainerID: value.ContainerID, ContainerName: value.ContainerName,
		NetworkName: value.NetworkName, NetworkAlias: value.NetworkAlias, InternalPort: value.InternalPort,
		ImageContentID: value.ImageContentID, WorkingDirectory: value.WorkingDirectory, RunCommandDigest: value.RunCommandDigest,
	}
}

func testCandidate(value generatedruntime.Candidate) generatedingress.LiveRuntimeTestCandidate {
	return generatedingress.LiveRuntimeTestCandidate{
		AppID: value.AppID, ReleaseID: value.ReleaseID, DeploymentID: value.DeploymentID, ArtifactID: value.ArtifactID,
		DeploymentPlanRevisionID: value.DeploymentPlanRevisionID, Component: value.Component, Role: value.Role,
		Technology: value.Technology, Slot: value.Slot, ContainerID: value.ContainerID, ContainerName: value.ContainerName,
		NetworkName: value.NetworkName, NetworkAlias: value.NetworkAlias, InternalPort: value.InternalPort,
		ImageContentID: value.ImageContentID, WorkingDirectory: value.WorkingDirectory, RunCommandDigest: value.RunCommandDigest,
	}
}
