package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type candidateOwnerFixture struct {
	validation  *candidateValidationFixture
	authority   *GeneratedCandidateAuthority
	runner      *candidateOwnerRunner
	environment *candidateOwnerEnvironment
	source      *candidateOwnerCapacitySource
	budget      *generatedcapacity.Budget
	options     GeneratedCandidateProcessOptions
}

func newCandidateOwnerFixture(t *testing.T) *candidateOwnerFixture {
	t.Helper()
	return newCandidateOwnerFixtureWithValidation(t, newCandidateOwnerValidationFixture(t))
}

func newCandidateOwnerFixtureWithValidation(t *testing.T, validation *candidateValidationFixture) *candidateOwnerFixture {
	t.Helper()
	options := GeneratedCandidateProcessOptions{
		DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerConfigDirectory: t.TempDir(),
		WorkingDirectory: t.TempDir(), CommandTimeout: time.Minute, HealthTimeout: time.Minute,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: 16 << 10,
		MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 64, TmpfsBytes: 8 << 20,
		LogSize: "1m", LogFiles: 2, ReplacementDiskBytes: 128 << 20,
	}
	return newCandidateOwnerFixtureWithValidationAndOptions(t, validation, options)
}

func newCandidateOwnerFixtureWithValidationAndOptions(t *testing.T, validation *candidateValidationFixture,
	options GeneratedCandidateProcessOptions,
) *candidateOwnerFixture {
	t.Helper()
	if validation == nil || validation.migration == nil {
		t.Fatal("candidate owner fixture requires prepared validation authority")
	}
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: uint64(options.MemoryBytes), DiskAvailableBytes: options.ReplacementDiskBytes,
	}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	runner := &candidateOwnerRunner{
		containerID: strings.Repeat("7", 64), absenceErr: generatedMigrationCompletedExitOne(t),
		before:  make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results: make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)),
	}
	environment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := validation.migration.repository.GeneratedCandidateAuthority(runner, environment, budget, options)
	if err != nil {
		t.Fatal(err)
	}
	validation.authority = authority
	return &candidateOwnerFixture{validation: validation, authority: authority, runner: runner,
		environment: environment, source: source, budget: budget, options: options}
}

// The migration fixture has a public runtime value. Candidate owner tests add
// a secret to a new immutable revision, then select that exact revision before
// any candidate receipt is derived. This makes every create exercise staging,
// zeroing and cleanup through the real protected configuration store.
func newCandidateOwnerValidationFixture(t *testing.T) *candidateValidationFixture {
	t.Helper()
	return newCandidateOwnerValidationFixtureWithMigration(t, newGeneratedMigrationFixture(t))
}

func newCandidateOwnerValidationFixtureWithMigration(t *testing.T, migration *generatedMigrationFixture) *candidateValidationFixture {
	t.Helper()
	if migration == nil {
		t.Fatal("candidate owner validation requires a prepared migration fixture")
	}
	secret := "candidate-owner-runtime-secret"
	targets := make([]appconfig.ComponentTarget, len(migration.plan.Components))
	for index, component := range migration.plan.Components {
		targets[index] = appconfig.ComponentTarget{Name: component.Name, Role: component.Role}
	}
	configuration, err := migration.configuration.ReplaceScoped(context.Background(), migration.guarded.appID, migration.guarded.actorID, appconfig.ScopedReplaceInput{
		ExpectedRevisionNumber: migration.request.ConfigurationRevisionNumber,
		PlanRevisionID:         migration.request.DeploymentPlanRevisionID, PlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
		Components: targets,
		Entries: []appconfig.ScopedValueInput{{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: "api", Key: "OWNER_SECRET"},
			Sensitivity: appconfig.SensitivitySecret, Value: &secret,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	db := migration.repository.db
	if _, err := db.Exec(`DROP TRIGGER deployment_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deployments SET actual_configuration_revision_id=?,actual_configuration_revision_number=? WHERE id=?`,
		configuration.RevisionID, configuration.RevisionNumber, migration.request.DeploymentID); err != nil {
		t.Fatal(err)
	}
	migration.request.ConfigurationRevisionID = configuration.RevisionID
	migration.request.ConfigurationRevisionNumber = configuration.RevisionNumber

	state := generatedruntimestate.New(db)
	if _, err := state.BeginMigration(context.Background(), migration.claim.ApplicationID(), migration.request.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.FinishMigration(context.Background(), migration.claim.ApplicationID(), migration.request.DeploymentID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Advance(context.Background(), migration.claim.ApplicationID(), migration.request.DeploymentID,
		generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
		t.Fatal(err)
	}
	request := GeneratedCandidateRequest{
		Claim: migration.claim, DeploymentID: migration.request.DeploymentID, ReleaseID: migration.request.ReleaseID,
		ConfigurationRevisionID: configuration.RevisionID, ConfigurationRevisionNumber: configuration.RevisionNumber,
		DeploymentPlanRevisionID:     migration.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
		CandidateSlot:                "blue",
	}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	loader := &GeneratedCandidateAuthority{stores: migration.repository.stores}
	protected, err := loader.loadCandidateProtectedSnapshot(context.Background(), pins)
	if err != nil {
		t.Fatal(err)
	}
	return &candidateValidationFixture{migration: migration, authority: loader, state: state, request: request, pins: pins, protected: protected}
}

func (f *candidateOwnerFixture) reserve(t *testing.T) GeneratedReplacementReservation {
	t.Helper()
	reservation, err := f.authority.ReserveReplacement(f.validation.migration.guarded.context, GeneratedReplacementRequest(f.validation.request))
	if err != nil {
		t.Fatalf("reserve candidate replacement: %v", err)
	}
	return reservation
}

func (f *candidateOwnerFixture) pin(t *testing.T, reservation GeneratedReplacementReservation) *GeneratedCandidateExecution {
	t.Helper()
	execution, err := f.authority.PinCandidates(f.validation.migration.guarded.context, f.validation.request, reservation)
	if err != nil {
		t.Fatalf("pin candidate execution: %v", err)
	}
	return execution
}

func (f *candidateOwnerFixture) beginCreate(t *testing.T, execution *GeneratedCandidateExecution) GeneratedCandidateInvocation {
	t.Helper()
	invocation, err := execution.BeginCreate("api")
	if err != nil {
		t.Fatalf("begin candidate create: %v", err)
	}
	return invocation
}

func (f *candidateOwnerFixture) bindInvocation(t *testing.T, value GeneratedCandidateInvocation) *generatedCandidateInvocationPermit {
	t.Helper()
	permit, ok := value.(*generatedCandidateInvocationPermit)
	if !ok || permit == nil {
		t.Fatalf("invocation type = %T", value)
	}
	f.runner.bindPolicy(permit.policy)
	return permit
}

func (f *candidateOwnerFixture) moveToWaitingHealth(t *testing.T, execution *GeneratedCandidateExecution) {
	t.Helper()
	state, err := execution.FinishStarting()
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != generatedruntimestate.PhaseWaitingHealth {
		t.Fatalf("phase after FinishStarting = %q", state.Phase)
	}
}

type candidateOwnerCommandKind string

const (
	candidateOwnerImageInspect   candidateOwnerCommandKind = "image-inspect"
	candidateOwnerNetworkInspect candidateOwnerCommandKind = "network-inspect"
	candidateOwnerInspect        candidateOwnerCommandKind = "inspect"
	candidateOwnerCreate         candidateOwnerCommandKind = "create"
	candidateOwnerStart          candidateOwnerCommandKind = "start"
	candidateOwnerStop           candidateOwnerCommandKind = "stop"
	candidateOwnerRemove         candidateOwnerCommandKind = "remove"
)

type candidateOwnerPhysical struct {
	policy      candidatePolicy
	containerID string
	receiptID   string
	exists      bool
	running     bool
	startedAt   string
	health      string
}

type candidateOwnerRunner struct {
	mu          sync.Mutex
	requestsLog []runtimeprocess.CommandRequest
	containerID string
	absenceErr  error
	physical    candidateOwnerPhysical
	before      map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)
	results     map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)
}

func (r *candidateOwnerRunner) bindPolicy(policy candidatePolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.physical.policy = policy
}

func (r *candidateOwnerRunner) setBefore(kind candidateOwnerCommandKind, callback func(runtimeprocess.CommandRequest)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before[kind] = callback
}

func (r *candidateOwnerRunner) setResult(kind candidateOwnerCommandKind, result func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[kind] = result
}

func (r *candidateOwnerRunner) setPhysical(value candidateOwnerPhysical) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.physical = value
}

func (r *candidateOwnerRunner) setHealth(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.physical.health = value
}

func (r *candidateOwnerRunner) snapshot() candidateOwnerPhysical {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.physical
}

func (r *candidateOwnerRunner) requests() []runtimeprocess.CommandRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]runtimeprocess.CommandRequest, len(r.requestsLog))
	for index, request := range r.requestsLog {
		values[index] = copyCandidateOwnerRequest(request)
	}
	return values
}

func (r *candidateOwnerRunner) count(kind candidateOwnerCommandKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, request := range r.requestsLog {
		if candidateOwnerKind(request) == kind {
			count++
		}
	}
	return count
}

func (r *candidateOwnerRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	request = copyCandidateOwnerRequest(request)
	kind := candidateOwnerKind(request)
	r.mu.Lock()
	r.requestsLog = append(r.requestsLog, request)
	before, result := r.before[kind], r.results[kind]
	r.mu.Unlock()
	if before != nil {
		before(copyCandidateOwnerRequest(request))
	}
	if result != nil {
		value, err := result(copyCandidateOwnerRequest(request))
		return copyCandidateOwnerResult(value), err
	}
	return r.defaultResult(kind, request)
}

func (r *candidateOwnerRunner) defaultResult(kind candidateOwnerCommandKind, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case candidateOwnerImageInspect:
		policy := r.physical.policy
		body, err := json.Marshal(generatedCandidateImageInspection{
			ID: policy.identity.ImageContentID, Size: 1, User: generatedcontainer.User,
			WorkingDirectory: "/workspace", Entrypoint: []string{generatedcontainer.Entrypoint},
			Labels: map[string]string{
				"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID,
				"io.rig.release": policy.identity.ReleaseID, "io.rig.artifact": policy.identity.ArtifactID,
				"io.rig.plan": policy.identity.DeploymentPlanRevisionID, "io.rig.component": policy.identity.Component,
				"io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
			},
		})
		return runtimeprocess.CommandResult{Stdout: body}, err
	case candidateOwnerNetworkInspect:
		policy := r.physical.policy
		body, err := json.Marshal(generatedCandidateNetworkInspection{
			Name: policy.identity.NetworkName, Driver: "bridge", Scope: "local",
			Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
		})
		return runtimeprocess.CommandResult{Stdout: body}, err
	case candidateOwnerInspect:
		target := request.Args[len(request.Args)-1]
		if !r.physical.exists || (target != r.physical.containerID && target != r.physical.policy.identity.ContainerName) {
			return r.absentResultLocked(target)
		}
		body, err := json.Marshal(r.snapshotLocked())
		return runtimeprocess.CommandResult{Stdout: body}, err
	case candidateOwnerCreate:
		r.physical.containerID = r.containerID
		r.physical.exists = true
		r.physical.running = false
		r.physical.startedAt = ""
		r.physical.health = ""
		return runtimeprocess.CommandResult{Stdout: []byte(r.containerID + "\n")}, nil
	case candidateOwnerStart:
		r.physical.running = true
		r.physical.startedAt = "2026-10-20T12:00:00.123456789Z"
		if r.physical.health == "" {
			r.physical.health = "healthy"
		}
		return runtimeprocess.CommandResult{Stdout: []byte(r.physical.containerID + "\n")}, nil
	case candidateOwnerStop:
		r.physical.running = false
		r.physical.health = ""
		return runtimeprocess.CommandResult{Stdout: []byte(r.physical.containerID + "\n")}, nil
	case candidateOwnerRemove:
		r.physical.exists = false
		return runtimeprocess.CommandResult{Stdout: []byte(r.physical.containerID + "\n")}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected candidate owner command")
	}
}

func (r *candidateOwnerRunner) configuredSnapshot(policy candidatePolicy, containerID, receiptID string) generatedcontainer.Inspection {
	physical := candidateOwnerPhysical{policy: policy, containerID: containerID, receiptID: receiptID, exists: true}
	return candidateOwnerPhysicalInspection(physical)
}

func (r *candidateOwnerRunner) inspectResult(value generatedcontainer.Inspection) runtimeprocess.CommandResult {
	body, _ := json.Marshal(value)
	return runtimeprocess.CommandResult{Stdout: body}
}

func (r *candidateOwnerRunner) absentResult(target string) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.absentResultLocked(target)
}

func (r *candidateOwnerRunner) absentResultLocked(target string) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{Stderr: []byte("Error response from daemon: No such container: " + target)}, r.absenceErr
}

func (r *candidateOwnerRunner) snapshotLocked() generatedcontainer.Inspection {
	return candidateOwnerPhysicalInspection(r.physical)
}

func candidateOwnerPhysicalInspection(physical candidateOwnerPhysical) generatedcontainer.Inspection {
	policy := physical.policy
	receiptID := physical.receiptID
	if receiptID == "" {
		receiptID = policy.identity.ReceiptID
	}
	labels := map[string]string{
		"io.rig.managed": "generated-runtime", "io.rig.application": policy.identity.AppID,
		"io.rig.release": policy.identity.ReleaseID, "io.rig.deployment": policy.identity.DeploymentID,
		"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
		"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role,
		"io.rig.slot": policy.identity.Slot, generatedcontainer.ReceiptLabel: receiptID,
	}
	tmpfs := map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(policy.limits.TmpfsBytes, 10)}
	if policy.identity.Technology == "nextjs" {
		tmpfs[policy.identity.WorkingDirectory+"/.next/cache"] = "rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=" + strconv.FormatInt(generatedcontainer.NextCacheTmpfsBytes, 10)
	}
	startedAt := physical.startedAt
	status := "created"
	health := ""
	networks := map[string]generatedcontainer.NetworkAttachment{}
	if physical.running {
		status = "running"
		if startedAt == "" {
			startedAt = "2026-10-20T12:00:00.123456789Z"
		}
		health = physical.health
		if health == "" {
			health = "starting"
		}
		networks[policy.identity.NetworkName] = generatedcontainer.NetworkAttachment{Aliases: []string{policy.identity.NetworkAlias}}
	} else if startedAt != "" {
		status = "exited"
	}
	if startedAt == "" {
		startedAt = time.Time{}.Format(time.RFC3339Nano)
	}
	return generatedcontainer.Inspection{
		ID: physical.containerID, Name: "/" + policy.identity.ContainerName,
		Image: policy.identity.ImageContentID, ConfigImage: policy.identity.ImageContentID,
		Labels: labels, User: generatedcontainer.User, WorkingDirectory: policy.identity.WorkingDirectory,
		Entrypoint: []string{generatedcontainer.Entrypoint}, Command: []string{"/bin/sh", "-lc", policy.runCommand},
		Volumes: map[string]json.RawMessage{}, HealthTest: []string{"CMD-SHELL", generatedcontainer.HealthCommand},
		HealthInterval: int64(2 * time.Second), HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: policy.limits.MemoryBytes, MemorySwap: policy.limits.MemoryBytes, NanoCPUs: policy.limits.MilliCPUs * 1_000_000,
		PIDs: policy.limits.PIDs, Ulimits: []generatedcontainer.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}},
		Init: true, NetworkMode: policy.identity.NetworkName, ReadonlyRootfs: true,
		CapAdd: []string{}, CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges=true"}, Binds: []string{},
		HostMounts: []json.RawMessage{}, PortBindings: map[string]json.RawMessage{}, Tmpfs: tmpfs,
		LogType: "local", LogConfig: map[string]string{"max-size": policy.limits.LogSize, "max-file": strconv.Itoa(policy.limits.LogFiles)}, Restart: "no",
		Status: status, Running: physical.running, StartedAt: startedAt, Health: health,
		Networks: networks, Mounts: []generatedcontainer.Mount{},
	}
}

func candidateOwnerKind(request runtimeprocess.CommandRequest) candidateOwnerCommandKind {
	if len(request.Args) < 2 {
		return ""
	}
	switch request.Args[0] + " " + request.Args[1] {
	case "image inspect":
		return candidateOwnerImageInspect
	case "network inspect":
		return candidateOwnerNetworkInspect
	case "container inspect":
		return candidateOwnerInspect
	case "container create":
		return candidateOwnerCreate
	case "container start":
		return candidateOwnerStart
	case "container stop":
		return candidateOwnerStop
	case "container rm":
		return candidateOwnerRemove
	default:
		return ""
	}
}

func copyCandidateOwnerRequest(value runtimeprocess.CommandRequest) runtimeprocess.CommandRequest {
	value.Args = append([]string(nil), value.Args...)
	value.Env = append([]string(nil), value.Env...)
	return value
}

func copyCandidateOwnerResult(value runtimeprocess.CommandResult) runtimeprocess.CommandResult {
	value.Stdout = append([]byte(nil), value.Stdout...)
	value.Stderr = append([]byte(nil), value.Stderr...)
	return value
}

type candidateOwnerEnvironment struct {
	mu           sync.Mutex
	path         string
	stageErr     error
	cleanupErr   error
	afterStage   func([]byte)
	stageCalls   int
	cleanupCalls int
	operationID  string
	attempt      int
	captured     []byte
	raw          []byte
}

type candidateOwnerEnvironmentSnapshot struct {
	stageCalls, cleanupCalls int
	operationID              string
	attempt                  int
	captured                 []byte
	raw                      []byte
}

func (s *candidateOwnerEnvironment) setStageError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stageErr = err
}

func (s *candidateOwnerEnvironment) setCleanupError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupErr = err
}

func (s *candidateOwnerEnvironment) setAfterStage(callback func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterStage = callback
}

func (s *candidateOwnerEnvironment) snapshot() candidateOwnerEnvironmentSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return candidateOwnerEnvironmentSnapshot{
		stageCalls: s.stageCalls, cleanupCalls: s.cleanupCalls, operationID: s.operationID, attempt: s.attempt,
		captured: append([]byte(nil), s.captured...), raw: s.raw,
	}
}

func (s *candidateOwnerEnvironment) StageGeneratedCandidate(operationID string, attempt int, contents []byte) (GeneratedCandidateEnvironmentLease, error) {
	s.mu.Lock()
	s.stageCalls++
	s.operationID = operationID
	s.attempt = attempt
	s.captured = append([]byte(nil), contents...)
	s.raw = contents
	callback, stageErr := s.afterStage, s.stageErr
	lease := &candidateOwnerEnvironmentLease{environment: s, path: s.path}
	s.mu.Unlock()
	if callback != nil {
		callback(contents)
	}
	return lease, stageErr
}

type candidateOwnerEnvironmentLease struct {
	mu          sync.Mutex
	environment *candidateOwnerEnvironment
	path        string
	cleaned     bool
}

func (l *candidateOwnerEnvironmentLease) Path() string { return l.path }

func (l *candidateOwnerEnvironmentLease) Cleanup() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cleaned {
		return nil
	}
	l.environment.mu.Lock()
	l.environment.cleanupCalls++
	err := l.environment.cleanupErr
	l.environment.mu.Unlock()
	if err == nil {
		l.cleaned = true
	}
	return err
}

type candidateOwnerCapacitySource struct {
	mu    sync.Mutex
	value generatedcapacity.Snapshot
	err   error
	calls int
}

func (s *candidateOwnerCapacitySource) Snapshot(context.Context) (generatedcapacity.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.value, s.err
}

func (s *candidateOwnerCapacitySource) observed() (generatedcapacity.Snapshot, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.calls
}
