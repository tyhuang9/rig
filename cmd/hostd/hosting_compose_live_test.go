//go:build live_docker

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const (
	composeLiveEnableEnv   = "RIG_RUN_LIVE_COMPOSE_APPLY"
	composeLiveRunTokenEnv = "RIG_LIVE_COMPOSE_APPLY_RUN_TOKEN"
	composeLiveEvidenceEnv = "RIG_LIVE_COMPOSE_APPLY_EVIDENCE_DIR"

	composeLiveOwnershipLabel = "io.rig.hostd-compose-live"
	composeLiveRunLabel       = "io.rig.hostd-compose-live-run"
	composeLiveCaseLabel      = "io.rig.hostd-compose-live-case"
	composeLiveAppLabel       = "io.rig.hostd-compose-live-app"

	composeLiveImage = "node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5"
)

type composeLiveFault uint8

const (
	composeLiveNoFault composeLiveFault = iota
	composeLiveAdvanceAttemptAfterUp
)

type composeLivePreflight struct {
	docker       string
	runToken     string
	evidenceRoot string
}

type composeLiveReceipt struct {
	Version                  int    `json:"version"`
	RunToken                 string `json:"runToken"`
	Case                     string `json:"case"`
	ConfigCalls              int    `json:"configCalls"`
	UpCalls                  int    `json:"upCalls"`
	ConfigModelSHA256        string `json:"configModelSha256,omitempty"`
	EffectiveSHA256          string `json:"effectiveSha256,omitempty"`
	UpExited                 bool   `json:"upExited"`
	UpExitCode               int    `json:"upExitCode,omitempty"`
	UpTimedOut               bool   `json:"upTimedOut"`
	UpOutputTruncated        bool   `json:"upOutputTruncated"`
	ExecutorCalled           bool   `json:"executorCalled"`
	CompletionCode           string `json:"completionCode"`
	CompletionDisposition    string `json:"completionDisposition"`
	PauseDisposition         string `json:"pauseDisposition"`
	JobStatus                string `json:"jobStatus"`
	JobAttempt               int    `json:"jobAttempt"`
	JobErrorCode             string `json:"jobErrorCode"`
	DeploymentStatus         string `json:"deploymentStatus"`
	DeploymentDiagnosticCode string `json:"deploymentDiagnosticCode"`
	RequestSecretSeen        bool   `json:"requestSecretSeen"`
	OutputSecretSeen         bool   `json:"outputSecretSeen"`
	ContainerObserved        bool   `json:"containerObserved"`
	NetworkObserved          bool   `json:"networkObserved"`
	CleanupSucceeded         bool   `json:"cleanupSucceeded"`
	AttemptAdvanceSeen       bool   `json:"attemptAdvanceSeen"`
}

type composeLiveContainer struct {
	ID       string            `json:"id"`
	Image    string            `json:"image"`
	Labels   map[string]string `json:"labels"`
	Running  bool              `json:"running"`
	Health   string            `json:"health"`
	Networks map[string]struct {
		NetworkID string `json:"NetworkID"`
		IPAddress string `json:"IPAddress"`
	} `json:"networks"`
}

type composeLiveNetwork struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels"`
	Containers map[string]struct {
		IPv4Address string `json:"IPv4Address"`
	} `json:"containers"`
}

type composeLiveRunnerSnapshot struct {
	configCalls               int
	upCalls                   int
	unknownCalls              int
	configSHA256              string
	effectiveSHA256           string
	configSource              string
	effectivePath             string
	protectedConfigSecretSeen bool
	requestSecretSeen         bool
	outputSecretSeen          bool
	upEvidence                composeLiveCommandEvidence
	attemptAdvanceSeen        bool
	injectionError            error
}

// composeLiveRunner observes the two fixed production Compose calls and then
// delegates the untouched request to the production ExecRunner. It retains no
// argv, environment, Compose output, or process output.
type composeLiveRunner struct {
	delegate runtimeprocess.CommandRunner
	db       *sql.DB
	fault    composeLiveFault
	jobID    string
	secret   []byte

	mu       sync.Mutex
	snapshot composeLiveRunnerSnapshot
}

func (r *composeLiveRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	kind := composeLiveClassifyCommand(request.Args)
	if kind == composeLiveCommandUp && len(request.Args) > 8 {
		body, err := os.ReadFile(request.Args[8])
		if err == nil {
			digest := sha256.Sum256(body)
			r.mu.Lock()
			r.snapshot.effectivePath = request.Args[8]
			r.snapshot.effectiveSHA256 = hex.EncodeToString(digest[:])
			r.mu.Unlock()
			clear(body)
		}
	}
	result, err := r.delegate.Run(ctx, request)
	evidence := composeLiveCommandEvidence{Kind: kind, TimedOut: errors.Is(err, context.DeadlineExceeded),
		OutputTruncated: result.StdoutTruncated || result.StderrTruncated}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		evidence.Exited, evidence.ExitCode = true, exitError.ExitCode()
	}
	secretSeen := len(r.secret) != 0 && (bytes.Contains(result.Stdout, r.secret) || bytes.Contains(result.Stderr, r.secret))
	requestSecretSeen := false
	if len(r.secret) != 0 {
		secret := string(r.secret)
		for _, values := range [][]string{request.Args, request.Env} {
			for _, value := range values {
				if strings.Contains(value, secret) {
					requestSecretSeen = true
					break
				}
			}
			if requestSecretSeen {
				break
			}
		}
	}
	if kind != composeLiveCommandUnknown {
		serialized, observedErr := composeLiveObservedEvidence(kind, request.Args, request.Env, result.Stdout, result.Stderr, evidence)
		clear(serialized)
		if observedErr != nil {
			r.mu.Lock()
			r.snapshot.injectionError = errors.New("ordinary Compose observer evidence conversion failed")
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	switch kind {
	case composeLiveCommandConfig:
		r.snapshot.configCalls++
		if len(request.Args) > 8 {
			r.snapshot.configSource = request.Args[8]
		}
		if len(result.Stdout) != 0 {
			digest := sha256.Sum256(result.Stdout)
			r.snapshot.configSHA256 = hex.EncodeToString(digest[:])
		}
	case composeLiveCommandUp:
		r.snapshot.upCalls++
		r.snapshot.upEvidence = evidence
	default:
		r.snapshot.unknownCalls++
	}
	if kind == composeLiveCommandConfig {
		r.snapshot.protectedConfigSecretSeen = r.snapshot.protectedConfigSecretSeen || secretSeen
	} else {
		r.snapshot.outputSecretSeen = r.snapshot.outputSecretSeen || secretSeen
	}
	r.snapshot.requestSecretSeen = r.snapshot.requestSecretSeen || requestSecretSeen
	r.mu.Unlock()

	if kind == composeLiveCommandUp && r.fault == composeLiveAdvanceAttemptAfterUp && err == nil && !evidence.OutputTruncated && ctx.Err() == nil {
		updated, updateErr := r.db.ExecContext(ctx, `UPDATE jobs SET attempt=attempt+1 WHERE id=? AND status='running' AND attempt=1`, r.jobID)
		if updateErr != nil {
			r.mu.Lock()
			r.snapshot.injectionError = updateErr
			r.mu.Unlock()
			return result, err
		}
		count, countErr := updated.RowsAffected()
		r.mu.Lock()
		if countErr != nil || count != 1 {
			r.snapshot.injectionError = errors.New("compose live post-up attempt injection did not affect one job")
		} else {
			r.snapshot.attemptAdvanceSeen = true
		}
		r.mu.Unlock()
	}
	return result, err
}

func (r *composeLiveRunner) Snapshot() composeLiveRunnerSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

type composeLiveExecutorCapture struct {
	delegate jobs.Executor
	mu       sync.Mutex
	result   jobs.ExecutionResult
	executed bool
	err      error
}

func (e *composeLiveExecutorCapture) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	result, err := e.delegate.Execute(ctx, job, reporter)
	e.mu.Lock()
	e.result = result
	e.executed = true
	e.err = err
	e.mu.Unlock()
	return result, err
}

func (e *composeLiveExecutorCapture) Error() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *composeLiveExecutorCapture) Result() (jobs.ExecutionResult, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result, e.executed
}

type composeLiveFixture struct {
	t             *testing.T
	preflight     composeLivePreflight
	caseName      string
	dataRoot      string
	docker        string
	db            *sql.DB
	applications  *apps.Store
	configuration *appconfig.Store
	plans         *deploymentplans.Store
	deployments   *deployments.Repository
	jobs          *jobs.Service
	bindings      *deploymentbinding.Repository
	admission     jobs.ScopedAdmission
	runner        *composeLiveRunner
	executor      *composeLiveExecutorCapture
	app           apps.Application
	actorID       string
	job           jobs.Job
	binding       deploymentbinding.Binding
	project       string
	networkName   string
	secret        []byte
	receipt       composeLiveReceipt

	workerCancel   context.CancelFunc
	workerDone     <-chan struct{}
	workerReleased chan struct{}
	releaseOnce    sync.Once
	cleanupOnce    sync.Once
}

func TestLiveHostdComposeApplyJourney(t *testing.T) {
	preflight := composeLiveRequirePreflight(t)
	cases := []struct {
		name      string
		unhealthy bool
		fault     composeLiveFault
		beforeUp  bool
	}{
		{name: "Success"},
		{name: "UnhealthyRetainsRuntime", unhealthy: true},
		{name: "AttemptLostBeforeUp", beforeUp: true},
		{name: "AttemptLostAfterUp", fault: composeLiveAdvanceAttemptAfterUp},
	}
	for _, current := range cases {
		current := current
		t.Run(current.name, func(t *testing.T) {
			fixture := newComposeLiveFixture(t, preflight, current.name, current.unhealthy, current.fault)
			if current.beforeUp {
				fixture.installAttemptAdvanceBeforeUp(t)
			}
			fixture.startWorker(t)
			job := fixture.waitTerminal(t)
			fixture.waitForAdmissionRelease(t)
			fixture.assertSafeRuntimeState(t)
			switch current.name {
			case "Success":
				fixture.assertSuccess(t, job)
			case "UnhealthyRetainsRuntime":
				fixture.assertUnhealthyRetainsRuntime(t, job)
			case "AttemptLostBeforeUp":
				fixture.assertAttemptLostBeforeUp(t, job)
			case "AttemptLostAfterUp":
				fixture.assertAttemptLostAfterUp(t, job)
			}
			fixture.cleanup(t)
		})
	}
}

func composeLiveRequirePreflight(t *testing.T) composeLivePreflight {
	t.Helper()
	if os.Getenv(composeLiveEnableEnv) != "1" {
		t.Fatal("RIG_RUN_LIVE_COMPOSE_APPLY=1 is required on a disposable hosted Linux Docker daemon")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" || os.Getenv("DOCKER_CONFIG") != "" {
		t.Fatal("ordinary Compose acceptance requires the default local Linux Docker daemon")
	}
	runToken := os.Getenv(composeLiveRunTokenEnv)
	if !composeLiveRunToken.MatchString(runToken) {
		t.Fatal("ordinary Compose acceptance requires a strict CI run token")
	}
	evidenceRoot := os.Getenv(composeLiveEvidenceEnv)
	if evidenceRoot == "" {
		t.Fatal("ordinary Compose acceptance requires an explicit evidence directory")
	}
	docker, err := exec.LookPath("docker")
	if err != nil || !filepath.IsAbs(docker) {
		t.Fatal("Docker CLI is unavailable")
	}
	// composeruntime intentionally resolves docker from PATH. Keep its scoped
	// command environment pointed at the exact preflighted CLI.
	t.Setenv("PATH", filepath.Dir(docker)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	contextName := composeLiveDocker(t, ctx, docker, "context", "show")
	if string(bytes.TrimSpace(contextName)) != "default" {
		clear(contextName)
		t.Fatal("Docker must use the default context")
	}
	clear(contextName)
	endpoint := composeLiveDocker(t, ctx, docker, "context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}")
	if !strings.HasPrefix(string(bytes.TrimSpace(endpoint)), "unix:///") {
		clear(endpoint)
		t.Fatal("Docker must use a local Unix endpoint")
	}
	clear(endpoint)
	_ = composeLiveDocker(t, ctx, docker, "info")
	_ = composeLiveDocker(t, ctx, docker, "compose", "version")
	image := composeLiveDocker(t, ctx, docker, "image", "inspect", composeLiveImage, "--format", "{{.Id}}")
	if len(bytes.TrimSpace(image)) == 0 {
		clear(image)
		t.Fatal("the reviewed immutable fixture image is unavailable")
	}
	clear(image)
	for _, args := range [][]string{
		{"ps", "-aq", "--no-trunc", "--filter", "label=" + composeLiveOwnershipLabel + "=1", "--filter", "label=" + composeLiveRunLabel + "=" + runToken},
		{"network", "ls", "-q", "--no-trunc", "--filter", "label=" + composeLiveOwnershipLabel + "=1", "--filter", "label=" + composeLiveRunLabel + "=" + runToken},
	} {
		found := composeLiveDocker(t, ctx, docker, args...)
		if len(bytes.TrimSpace(found)) != 0 {
			clear(found)
			t.Fatal("exact run identity already owns Docker resources")
		}
		clear(found)
	}
	return composeLivePreflight{docker: docker, runToken: runToken, evidenceRoot: evidenceRoot}
}

func newComposeLiveFixture(t *testing.T, preflight composeLivePreflight, caseName string, unhealthy bool, fault composeLiveFault) *composeLiveFixture {
	t.Helper()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "hostd")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal("create ordinary Compose fixture data directory")
	}
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal("open ordinary Compose fixture database")
	}
	fixture := &composeLiveFixture{t: t, preflight: preflight, caseName: caseName, dataRoot: dataRoot, docker: preflight.docker,
		db: db, workerReleased: make(chan struct{}), receipt: composeLiveReceipt{Version: 1, RunToken: preflight.runToken, Case: caseName}}
	t.Cleanup(func() { fixture.cleanup(t) })
	fixture.actorID = uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, fixture.actorID, "compose-live-owner-"+strings.ToLower(caseName), "fixture-hash", stamp, stamp); err != nil {
		t.Fatal("seed ordinary Compose fixture owner")
	}
	machine, err := machines.New(db).EnsureLocal()
	if err != nil {
		t.Fatal("ensure ordinary Compose fixture machine")
	}
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal("create ordinary Compose fixture source directory")
	}
	fixture.applications = apps.New(db)
	fixture.app, err = fixture.applications.Create("Compose Live "+caseName, "", source, machine.ID)
	if err != nil {
		t.Fatal("create ordinary Compose fixture application")
	}
	fixture.project = "rig-" + strings.ReplaceAll(fixture.app.ID, "-", "")
	fixture.networkName = fixture.project + "_default"
	if err := composeLiveWriteSource(source, fixture.app.ID, preflight.runToken, caseName, unhealthy); err != nil {
		t.Fatal("write ordinary Compose fixture source")
	}
	fixture.configuration, err = appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal("create ordinary Compose fixture configuration store")
	}
	fixture.secret = composeLiveRandomSecret(t)
	marker := "compose-live-" + strings.ToLower(caseName)
	if _, err := fixture.configuration.Replace(context.Background(), fixture.app.ID, fixture.actorID, appconfig.ReplaceInput{
		ExpectedRevisionNumber: 0,
		Variables:              []appconfig.ValueInput{{Key: "COMPOSE_LIVE_MARKER", Value: marker}},
		Secrets:                []appconfig.ValueInput{{Key: "COMPOSE_LIVE_SECRET", Value: string(fixture.secret)}},
	}); err != nil {
		t.Fatal("write ordinary Compose fixture configuration")
	}
	fixture.plans, err = deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal("create ordinary Compose fixture plan store")
	}
	var bindings *deploymentbinding.Repository
	fixture.jobs, bindings, err = newDeploymentJobService(db)
	if err != nil {
		t.Fatal("create ordinary Compose fixture deployment service")
	}
	fixture.bindings = bindings
	materializer, err := releasesnapshot.New(db, composeLiveUnreachableSource{}, dataRoot)
	if err != nil {
		t.Fatal("create ordinary Compose fixture release materializer")
	}
	fixture.deployments = deployments.New(db)
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal("prepare ordinary Compose fixture controller directories")
	}
	workingDirectory := directories.WorkingDirectory
	admission, err := deploymentEffectsScopedAdmission(db, workingDirectory)
	if err != nil {
		t.Fatal("prepare ordinary Compose fixture scoped admission")
	}
	fixture.admission = admission
	fixture.runner = &composeLiveRunner{delegate: runtimeprocess.ExecRunner{}, db: db, fault: fault, secret: fixture.secret}
	settings := config.Defaults()
	settings.DataRoot, settings.ComposeRuntime, settings.GeneratedRuntime, settings.FakeRuntime, settings.CaddyManagement = dataRoot, true, false, false, false
	settings.ComposeConfigTimeout, settings.ComposeWaitTimeout, settings.ComposeApplyTimeout = 30*time.Second, 30*time.Second, 90*time.Second
	composition, err := prepareRuntimeComposition(context.Background(), settings, runtimeCompositionDependencies{
		db: db, applications: fixture.applications, snapshots: materializer, configuration: fixture.configuration,
		deployments: fixture.deployments, plans: fixture.plans, bindings: bindings, effectsWorkingDirectory: workingDirectory,
	}, runtimeCompositionOptions{runner: fixture.runner})
	if err != nil {
		t.Fatal("prepare ordinary Compose runtime")
	}
	fixture.executor = &composeLiveExecutorCapture{delegate: composition.executor}
	if _, err := composeLiveWriteManifest(preflight.evidenceRoot, composeLiveManifest{
		Version: 1, RunToken: preflight.runToken, Case: caseName, AppID: fixture.app.ID,
		Project: fixture.project, Service: "app", NetworkName: fixture.networkName,
	}); err != nil {
		t.Fatal("write ordinary Compose ownership manifest")
	}
	created, err := fixture.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: fixture.app.ID, RequestedBy: fixture.actorID, IdempotencyKey: "compose-live-" + strings.ToLower(caseName),
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create ordinary Compose deployment created=%t", created.Created)
	}
	fixture.job = created.Job
	fixture.binding = created.Binding
	fixture.runner.jobID = fixture.job.ID
	return fixture
}

func (f *composeLiveFixture) installAttemptAdvanceBeforeUp(t *testing.T) {
	t.Helper()
	name := "compose_live_attempt_" + strings.ReplaceAll(f.job.ID, "-", "")
	statement := fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF status ON deployments
WHEN OLD.job_id='%s' AND OLD.status='preparing' AND NEW.status='applying'
BEGIN UPDATE jobs SET attempt=attempt+1 WHERE id='%s' AND status='running' AND attempt=1; END`, name, f.job.ID, f.job.ID)
	if _, err := f.db.Exec(statement); err != nil {
		t.Fatal("install fixture-only attempt drift trigger")
	}
}

func (f *composeLiveFixture) startWorker(t *testing.T) {
	t.Helper()
	workerContext, cancel := context.WithCancel(context.Background())
	f.workerCancel = cancel
	admission := f.admission
	wrappedAdmission := func(parent context.Context) (context.Context, func() error, error) {
		scope, release, err := admission(parent)
		if err != nil {
			return nil, nil, err
		}
		return scope, func() error {
			err := release()
			f.releaseOnce.Do(func() { close(f.workerReleased) })
			return err
		}, nil
	}
	done, err := prepareRuntimeWorker(workerContext, runtimeRecovery{recoverState: f.jobs.RecoverRuntimeContext}, f.executor,
		func(ctx context.Context, executor jobs.Executor) error {
			return f.jobs.RunWorkerWithScopedAdmission(ctx, executor, wrappedAdmission)
		},
		func(error) {})
	if err != nil {
		t.Fatal("start ordinary Compose fixture worker")
	}
	f.workerDone = done
}

func (f *composeLiveFixture) waitTerminal(t *testing.T) jobs.Job {
	t.Helper()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := f.jobs.Get(f.job.ID)
		if err == nil && (job.Status == string(jobs.Succeeded) || job.Status == string(jobs.Failed)) {
			return job
		}
		select {
		case <-deadline.C:
			t.Fatal("ordinary Compose worker did not reach a finite terminal state")
		case <-ticker.C:
		}
	}
}

func (f *composeLiveFixture) waitForAdmissionRelease(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-f.workerReleased:
	case <-timer.C:
		t.Fatal("ordinary Compose worker did not release its original scoped admission")
	}
}

func (f *composeLiveFixture) assertSafeRuntimeState(t *testing.T) {
	t.Helper()
	snapshot := f.runner.Snapshot()
	if snapshot.unknownCalls != 0 || !snapshot.protectedConfigSecretSeen || snapshot.requestSecretSeen || snapshot.outputSecretSeen || snapshot.injectionError != nil {
		t.Fatal("ordinary Compose observer recorded an unsafe or unrecognized command outcome")
	}
	entries, err := os.ReadDir(filepath.Join(f.dataRoot, "runtime", "compose"))
	if err != nil || len(entries) != 0 {
		t.Fatal("protected ordinary Compose files were not removed")
	}
	job, err := f.jobs.Get(f.job.ID)
	if err != nil || strings.Contains(job.ErrorDetail, string(f.secret)) {
		t.Fatal("durable ordinary Compose job diagnostics are unsafe")
	}
	events, err := f.jobs.Events(f.job.ID, 0)
	if err != nil {
		t.Fatal("read ordinary Compose fixture events")
	}
	for _, event := range events {
		if strings.Contains(event.Message, string(f.secret)) {
			t.Fatal("durable ordinary Compose event disclosed a secret")
		}
	}
}

func (f *composeLiveFixture) assertSuccess(t *testing.T, job jobs.Job) {
	t.Helper()
	snapshot := f.runner.Snapshot()
	result, executed := f.executor.Result()
	if snapshot.configCalls != 1 || snapshot.upCalls != 1 || !executed || f.executor.Error() != nil ||
		result.CompletionCode != "deployment_completed" || result.Disposition != jobs.ExecutionCompleted || result.PauseDisposition != "" ||
		job.Status != string(jobs.Succeeded) || job.Attempt != 1 || job.ErrorCode != "" || job.ErrorDetail != "" {
		t.Fatal("ordinary Compose success did not complete exactly one config/up deployment")
	}
	if snapshot.configSHA256 == "" || snapshot.configSHA256 != snapshot.effectiveSHA256 || snapshot.configSource == snapshot.effectivePath ||
		!strings.HasPrefix(filepath.Clean(snapshot.effectivePath), filepath.Join(f.dataRoot, "runtime", "compose")+string(filepath.Separator)) {
		t.Fatal("ordinary Compose up did not use the protected evaluated model")
	}
	deployment := f.oneDeployment(t)
	if deployment.Status != deployments.Succeeded || deployment.RuntimeStrategy != deployments.RuntimeCompose || !deployment.ProvenanceInitialized ||
		deployment.AppID != f.app.ID || deployment.JobID != f.job.ID || deployment.ConfigurationMode != string(jobs.ConfigurationCurrent) ||
		deployment.ReleaseID == "" || deployment.ActualConfigurationRevisionID == "" || deployment.ActualConfigurationRevisionNumber != 1 || deployment.FinishedAt.IsZero() ||
		(deployment.DeploymentPlanRevisionID == "") != (deployment.DeploymentPlanRevisionNumber == 0) || deployment.DiagnosticCode != "" || len(deployment.Findings) != 0 {
		t.Fatal("ordinary Compose success did not retain immutable completed provenance")
	}
	f.assertImmutableBindingAndCensus(t)
	f.assertEventCensus(t, true)
	f.assertOwnedRuntime(t, "healthy")
}

func (f *composeLiveFixture) assertUnhealthyRetainsRuntime(t *testing.T, job jobs.Job) {
	t.Helper()
	snapshot := f.runner.Snapshot()
	executionErr := f.executor.Error()
	var typedErr *jobs.ExecutionError
	if snapshot.configCalls != 1 || snapshot.upCalls != 1 || !snapshot.upEvidence.Exited || snapshot.upEvidence.ExitCode == 0 || snapshot.upEvidence.TimedOut || snapshot.upEvidence.OutputTruncated ||
		executionErr == nil || !errors.As(executionErr, &typedErr) || typedErr.Code != "compose_apply_failed" ||
		job.Status != string(jobs.Failed) || job.Attempt != 1 || job.ErrorCode != "compose_apply_failed" || job.ErrorDetail != "Container runtime failed to apply the deployment" {
		t.Fatal("unhealthy Compose service did not produce a trusted finite apply failure")
	}
	deployment := f.oneDeployment(t)
	if !f.isRetainedApplyingProvenance(deployment) {
		t.Fatal("unhealthy Compose failure did not retain the applying deployment")
	}
	f.assertImmutableBindingAndCensus(t)
	f.assertEventCensus(t, false)
	f.assertOwnedRuntime(t, "unhealthy")
	f.assertTerminalResumeRefused(t, job, deployment, 1)
}

func (f *composeLiveFixture) assertAttemptLostBeforeUp(t *testing.T, job jobs.Job) {
	t.Helper()
	snapshot := f.runner.Snapshot()
	if snapshot.configCalls != 1 || snapshot.upCalls != 0 || !errors.Is(f.executor.Error(), deploymentexecution.ErrIdentityLost) ||
		job.Status != string(jobs.Failed) || job.Attempt != 2 || job.ErrorCode != "runtime_unavailable" || job.ErrorDetail != "Runtime unavailable" {
		t.Fatal("original attempt was not refused at the real Compose up boundary")
	}
	deployment := f.oneDeployment(t)
	if !f.isRetainedApplyingProvenance(deployment) {
		t.Fatal("pre-up identity refusal did not retain applying provenance")
	}
	f.assertImmutableBindingAndCensus(t)
	f.assertEventCensus(t, false)
	f.assertNoOwnedRuntime(t)
	f.assertTerminalResumeRefused(t, job, deployment, 0)
}

func (f *composeLiveFixture) assertAttemptLostAfterUp(t *testing.T, job jobs.Job) {
	t.Helper()
	snapshot := f.runner.Snapshot()
	if snapshot.configCalls != 1 || snapshot.upCalls != 1 || !snapshot.attemptAdvanceSeen || !errors.Is(f.executor.Error(), deploymentexecution.ErrIdentityLost) ||
		job.Status != string(jobs.Failed) || job.Attempt != 2 || job.ErrorCode != "runtime_unavailable" || job.ErrorDetail != "Runtime unavailable" {
		t.Fatal("post-up original attempt authority loss was not retained")
	}
	deployment := f.oneDeployment(t)
	if !f.isRetainedApplyingProvenance(deployment) {
		t.Fatal("post-up identity refusal did not retain applying provenance")
	}
	f.assertImmutableBindingAndCensus(t)
	f.assertEventCensus(t, false)
	f.assertOwnedRuntime(t, "healthy")
	f.assertTerminalResumeRefused(t, job, deployment, 1)
}

func (f *composeLiveFixture) oneDeployment(t *testing.T) deployments.Deployment {
	t.Helper()
	values, err := f.deployments.List(context.Background(), f.app.ID, 2)
	if err != nil || len(values) != 1 || values[0].JobID != f.job.ID {
		t.Fatal("ordinary Compose fixture has an invalid deployment linkage census")
	}
	return values[0]
}

func (f *composeLiveFixture) isRetainedApplyingProvenance(value deployments.Deployment) bool {
	return value.Status == deployments.Applying && value.AppID == f.app.ID && value.JobID == f.job.ID &&
		value.ConfigurationMode == string(jobs.ConfigurationCurrent) && value.RuntimeStrategy == deployments.RuntimeCompose && value.ProvenanceInitialized &&
		value.ReleaseID != "" && value.ActualConfigurationRevisionID != "" && value.ActualConfigurationRevisionNumber == 1 &&
		(value.DeploymentPlanRevisionID == "") == (value.DeploymentPlanRevisionNumber == 0) && value.StartedAt.IsZero() == false &&
		value.FinishedAt.IsZero() && value.DiagnosticCode == "" && len(value.Findings) == 0
}

func (f *composeLiveFixture) assertImmutableBindingAndCensus(t *testing.T) {
	t.Helper()
	persisted, err := f.bindings.Read(context.Background(), f.job.ID)
	if err != nil || !persisted.Equal(f.binding) || persisted.JobID() != f.job.ID || persisted.ApplicationID() != f.app.ID {
		t.Fatal("ordinary Compose fixture immutable binding changed")
	}
	var jobCount, bindingCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE resource_id=?`, f.app.ID).Scan(&jobCount); err != nil || jobCount != 1 {
		t.Fatal("ordinary Compose fixture job census is invalid")
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings WHERE app_id=?`, f.app.ID).Scan(&bindingCount); err != nil || bindingCount != 1 {
		t.Fatal("ordinary Compose fixture binding census is invalid")
	}
}

func (f *composeLiveFixture) assertEventCensus(t *testing.T, succeeded bool) {
	t.Helper()
	events, err := f.jobs.Events(f.job.ID, 0)
	if err != nil {
		t.Fatal("read ordinary Compose fixture event census")
	}
	completed, failed := 0, 0
	for _, event := range events {
		switch event.Code {
		case "job_succeeded":
			completed++
		case "job_failed":
			failed++
		}
	}
	if (succeeded && (completed != 1 || failed != 0)) || (!succeeded && (completed != 0 || failed != 1)) {
		t.Fatal("ordinary Compose fixture terminal event census is invalid")
	}
}

func (f *composeLiveFixture) assertTerminalResumeRefused(t *testing.T, beforeJob jobs.Job, beforeDeployment deployments.Deployment, expectedUpCalls int) {
	t.Helper()
	if _, err := f.jobs.Resume(f.job.ID); !errors.Is(err, jobs.ErrJobNotPaused) {
		t.Fatal("terminal ordinary Compose job unexpectedly resumed")
	}
	afterJob, err := f.jobs.Get(f.job.ID)
	if err != nil || afterJob.Status != beforeJob.Status || afterJob.Attempt != beforeJob.Attempt || afterJob.ErrorCode != beforeJob.ErrorCode || afterJob.ErrorDetail != beforeJob.ErrorDetail {
		t.Fatal("terminal ordinary Compose resume changed the job")
	}
	afterDeployment := f.oneDeployment(t)
	if afterDeployment.ID != beforeDeployment.ID || afterDeployment.Status != beforeDeployment.Status || afterDeployment.ReleaseID != beforeDeployment.ReleaseID ||
		afterDeployment.ActualConfigurationRevisionID != beforeDeployment.ActualConfigurationRevisionID || afterDeployment.ActualConfigurationRevisionNumber != beforeDeployment.ActualConfigurationRevisionNumber ||
		afterDeployment.RuntimeStrategy != beforeDeployment.RuntimeStrategy || afterDeployment.DeploymentPlanRevisionID != beforeDeployment.DeploymentPlanRevisionID || afterDeployment.DeploymentPlanRevisionNumber != beforeDeployment.DeploymentPlanRevisionNumber {
		t.Fatal("terminal ordinary Compose resume changed the deployment")
	}
	if snapshot := f.runner.Snapshot(); snapshot.upCalls != expectedUpCalls {
		t.Fatal("terminal ordinary Compose resume changed the up command census")
	}
	f.assertImmutableBindingAndCensus(t)
}

func (f *composeLiveFixture) assertOwnedRuntime(t *testing.T, health string) {
	t.Helper()
	container := f.ownedContainer(t)
	if container.Image != composeLiveImage || !container.Running || container.Health != health ||
		container.Labels[composeLiveOwnershipLabel] != "1" || container.Labels[composeLiveRunLabel] != f.preflight.runToken ||
		container.Labels[composeLiveCaseLabel] != f.caseName || container.Labels[composeLiveAppLabel] != f.app.ID ||
		container.Labels["com.docker.compose.project"] != f.project || container.Labels["com.docker.compose.service"] != "app" {
		t.Fatal("ordinary Compose container ownership or state is invalid")
	}
	entry, found := container.Networks[f.networkName]
	if !found || entry.NetworkID == "" || net.ParseIP(entry.IPAddress) == nil {
		t.Fatal("ordinary Compose container is not attached to its exact owned network")
	}
	network := f.ownedNetwork(t)
	if network.Name != f.networkName || network.Labels[composeLiveOwnershipLabel] != "1" || network.Labels[composeLiveRunLabel] != f.preflight.runToken ||
		network.Labels[composeLiveCaseLabel] != f.caseName || network.Labels[composeLiveAppLabel] != f.app.ID || network.Labels["com.docker.compose.project"] != f.project || network.Labels["com.docker.compose.network"] != "default" {
		t.Fatal("ordinary Compose network ownership is invalid")
	}
	endpoint, found := network.Containers[container.ID]
	endpointAddress, _, endpointErr := net.ParseCIDR(endpoint.IPv4Address)
	if !found || entry.NetworkID != network.ID || endpointErr != nil || endpointAddress.String() != entry.IPAddress {
		t.Fatal("ordinary Compose container endpoint is not bound to its inspected owned network")
	}
	f.receipt.ContainerObserved, f.receipt.NetworkObserved = true, true
	if health == "healthy" {
		f.probeMarker(t, entry.IPAddress)
	}
}

func (f *composeLiveFixture) assertNoOwnedRuntime(t *testing.T) {
	t.Helper()
	if ids := f.listOwnedIDs(t, "ps", "-aq"); len(ids) != 0 {
		t.Fatal("pre-up identity refusal created a container")
	}
	if ids := f.listOwnedIDs(t, "network", "ls", "-q"); len(ids) != 0 {
		t.Fatal("pre-up identity refusal created a network")
	}
}

func (f *composeLiveFixture) ownedContainer(t *testing.T) composeLiveContainer {
	t.Helper()
	ids := f.listOwnedIDs(t, "ps", "-aq")
	if len(ids) != 1 {
		t.Fatal("ordinary Compose fixture did not retain exactly one owned container")
	}
	format := `{"id":{{json .Id}},"image":{{json .Config.Image}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"health":{{json .State.Health.Status}},"networks":{{json .NetworkSettings.Networks}}}`
	body := composeLiveDocker(t, context.Background(), f.docker, "container", "inspect", "--format", format, ids[0])
	defer clear(body)
	var value composeLiveContainer
	if json.Unmarshal(body, &value) != nil || value.ID != ids[0] {
		t.Fatal("ordinary Compose container inspection is invalid")
	}
	return value
}

func (f *composeLiveFixture) ownedNetwork(t *testing.T) composeLiveNetwork {
	t.Helper()
	ids := f.listOwnedIDs(t, "network", "ls", "-q")
	if len(ids) != 1 {
		t.Fatal("ordinary Compose fixture did not retain exactly one owned network")
	}
	format := `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Labels}},"containers":{{json .Containers}}}`
	body := composeLiveDocker(t, context.Background(), f.docker, "network", "inspect", "--format", format, ids[0])
	defer clear(body)
	var value composeLiveNetwork
	if json.Unmarshal(body, &value) != nil || value.ID != ids[0] {
		t.Fatal("ordinary Compose network inspection is invalid")
	}
	return value
}

func (f *composeLiveFixture) probeMarker(t *testing.T, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(address, "8080")+"/", nil)
	if err != nil {
		t.Fatal("create owned ordinary Compose probe request")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("owned ordinary Compose container is not reachable through its bridge address")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil || len(body) > 1024 || response.StatusCode != http.StatusOK || !bytes.Equal(body, []byte("compose-live-"+strings.ToLower(f.caseName))) {
		clear(body)
		t.Fatal("owned ordinary Compose container returned an unexpected public marker")
	}
	clear(body)
}

func (f *composeLiveFixture) listOwnedIDs(t *testing.T, command string, args ...string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := f.listOwnedIDsContext(ctx, command, args...)
	if err != nil {
		t.Fatal("ordinary Compose fixture resource census failed")
	}
	return ids
}

func (f *composeLiveFixture) listOwnedIDsContext(ctx context.Context, command string, args ...string) ([]string, error) {
	full := append([]string{command}, args...)
	if command == "ps" || command == "network" {
		full = append(full, "--no-trunc")
	}
	full = append(full, "--filter", "label="+composeLiveOwnershipLabel+"=1", "--filter", "label="+composeLiveRunLabel+"="+f.preflight.runToken, "--filter", "label="+composeLiveCaseLabel+"="+f.caseName)
	body, err := composeLiveDockerResult(ctx, f.docker, full...)
	if err != nil {
		clear(body)
		return nil, err
	}
	defer clear(body)
	return strings.Fields(string(body)), nil
}

func (f *composeLiveFixture) cleanup(t *testing.T) {
	t.Helper()
	f.cleanupOnce.Do(func() {
		if f.workerCancel != nil {
			f.workerCancel()
			f.workerCancel = nil
			if f.workerDone != nil {
				timer := time.NewTimer(30 * time.Second)
				select {
				case <-f.workerDone:
				case <-timer.C:
					t.Error("ordinary Compose worker did not join before fixture cleanup")
					f.receipt.CleanupSucceeded = false
					f.recordReceiptSnapshot()
					f.writeReceipt(t)
					timer.Stop()
					return
				}
				timer.Stop()
			}
		}
		if f.runner != nil {
			f.recordReceiptSnapshot()
			f.recordReceiptTerminalState()
			f.receipt.CleanupSucceeded = f.cleanupDocker(t)
			f.writeReceipt(t)
		}
		clear(f.secret)
		f.secret = nil
		if f.db != nil {
			if err := f.db.Close(); err != nil {
				t.Error("close ordinary Compose fixture database")
			}
			f.db = nil
		}
	})
}

func (f *composeLiveFixture) recordReceiptSnapshot() {
	if f.runner == nil {
		return
	}
	snapshot := f.runner.Snapshot()
	f.receipt.ConfigCalls = snapshot.configCalls
	f.receipt.UpCalls = snapshot.upCalls
	f.receipt.ConfigModelSHA256 = snapshot.configSHA256
	f.receipt.EffectiveSHA256 = snapshot.effectiveSHA256
	f.receipt.UpExited = snapshot.upEvidence.Exited
	f.receipt.UpExitCode = snapshot.upEvidence.ExitCode
	f.receipt.UpTimedOut = snapshot.upEvidence.TimedOut
	f.receipt.UpOutputTruncated = snapshot.upEvidence.OutputTruncated
	f.receipt.RequestSecretSeen = snapshot.requestSecretSeen
	f.receipt.OutputSecretSeen = snapshot.outputSecretSeen
	f.receipt.AttemptAdvanceSeen = snapshot.attemptAdvanceSeen
}

// recordReceiptTerminalState permits the hosted workflow to validate only
// finite, allowlisted outcome facts. It intentionally omits arbitrary errors,
// output, diagnostic details, and process arguments.
func (f *composeLiveFixture) recordReceiptTerminalState() {
	if f.executor != nil {
		result, executed := f.executor.Result()
		f.receipt.ExecutorCalled = executed
		f.receipt.CompletionCode = composeLiveReceiptCompletionCode(result.CompletionCode)
		f.receipt.CompletionDisposition = composeLiveReceiptDisposition(result.Disposition)
		f.receipt.PauseDisposition = composeLiveReceiptPauseDisposition(result.PauseDisposition)
	}
	if f.jobs != nil {
		if job, err := f.jobs.Get(f.job.ID); err == nil {
			f.receipt.JobStatus = composeLiveReceiptJobStatus(job.Status)
			f.receipt.JobAttempt = job.Attempt
			f.receipt.JobErrorCode = composeLiveReceiptJobErrorCode(job.ErrorCode)
		}
	}
	if f.deployments != nil {
		if values, err := f.deployments.List(context.Background(), f.app.ID, 2); err == nil && len(values) == 1 && values[0].JobID == f.job.ID {
			f.receipt.DeploymentStatus = composeLiveReceiptDeploymentStatus(values[0].Status)
			f.receipt.DeploymentDiagnosticCode = composeLiveReceiptDeploymentDiagnosticCode(values[0].DiagnosticCode)
		}
	}
}

func composeLiveReceiptCompletionCode(value string) string {
	switch value {
	case "":
		return "none"
	case "deployment_completed":
		return value
	default:
		return "unknown"
	}
}

func composeLiveReceiptDisposition(value jobs.ExecutionDisposition) string {
	switch value {
	case jobs.ExecutionCompleted:
		return "completed"
	case jobs.ExecutionWaitingUser:
		return "waiting_user"
	default:
		return "unknown"
	}
}

func composeLiveReceiptPauseDisposition(value string) string {
	switch value {
	case "":
		return "none"
	case jobs.PauseApprovalRequired, jobs.PauseMigrationApprovalRequired, jobs.PauseInsufficientReplacementCapacity, jobs.PauseRouteReconciliationRequired:
		return value
	default:
		return "unknown"
	}
}

func composeLiveReceiptJobStatus(value string) string {
	switch jobs.Status(value) {
	case jobs.Queued, jobs.Assigned, jobs.Running, jobs.Waiting, jobs.WaitingUser, jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return value
	default:
		return "unknown"
	}
}

func composeLiveReceiptJobErrorCode(value string) string {
	switch value {
	case "":
		return "none"
	case "runtime_unavailable", "compose_apply_failed":
		return value
	default:
		return "unknown"
	}
}

func composeLiveReceiptDeploymentStatus(value deployments.Status) string {
	switch value {
	case deployments.Preparing, deployments.Applying, deployments.WaitingHealth, deployments.Succeeded, deployments.Failed, deployments.Cancelled, deployments.NeedsAttention:
		return string(value)
	default:
		return "unknown"
	}
}

func composeLiveReceiptDeploymentDiagnosticCode(value string) string {
	if value == "" {
		return "none"
	}
	return "unknown"
}

func (f *composeLiveFixture) cleanupDocker(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	clean := true
	containerIDs, err := f.listOwnedIDsContext(ctx, "ps", "-aq")
	if err != nil {
		t.Error("ordinary Compose fixture container cleanup census failed")
		return false
	}
	for _, id := range containerIDs {
		container, inspected := f.inspectOwnedContainer(ctx, id)
		if !inspected || container.ID != id || container.Labels[composeLiveOwnershipLabel] != "1" || container.Labels[composeLiveRunLabel] != f.preflight.runToken ||
			container.Labels[composeLiveCaseLabel] != f.caseName || container.Labels[composeLiveAppLabel] != f.app.ID || container.Labels["com.docker.compose.project"] != f.project || container.Labels["com.docker.compose.service"] != "app" {
			t.Error("retain uncertain ordinary Compose container ownership")
			clean = false
			continue
		}
		body, err := composeLiveDockerResult(ctx, f.docker, "container", "rm", "--force", id)
		clear(body)
		if err != nil {
			t.Error("remove exact ordinary Compose container")
			clean = false
		}
	}
	networkIDs, err := f.listOwnedIDsContext(ctx, "network", "ls", "-q")
	if err != nil {
		t.Error("ordinary Compose fixture network cleanup census failed")
		return false
	}
	for _, id := range networkIDs {
		network, inspected := f.inspectOwnedNetwork(ctx, id)
		if !inspected || network.ID != id || network.Name != f.networkName || len(network.Containers) != 0 || network.Labels[composeLiveOwnershipLabel] != "1" ||
			network.Labels[composeLiveRunLabel] != f.preflight.runToken || network.Labels[composeLiveCaseLabel] != f.caseName || network.Labels[composeLiveAppLabel] != f.app.ID ||
			network.Labels["com.docker.compose.project"] != f.project || network.Labels["com.docker.compose.network"] != "default" {
			t.Error("retain uncertain or nonempty ordinary Compose network ownership")
			clean = false
			continue
		}
		body, err := composeLiveDockerResult(ctx, f.docker, "network", "rm", id)
		clear(body)
		if err != nil {
			t.Error("remove exact empty ordinary Compose network")
			clean = false
		}
	}
	if ids, err := f.listOwnedIDsContext(ctx, "ps", "-aq"); err != nil || len(ids) != 0 {
		t.Error("ordinary Compose fixture container cleanup is incomplete")
		clean = false
	}
	if ids, err := f.listOwnedIDsContext(ctx, "network", "ls", "-q"); err != nil || len(ids) != 0 {
		t.Error("ordinary Compose fixture network cleanup is incomplete")
		clean = false
	}
	return clean
}

func (f *composeLiveFixture) inspectOwnedContainer(ctx context.Context, id string) (composeLiveContainer, bool) {
	format := `{"id":{{json .Id}},"image":{{json .Config.Image}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"health":{{json .State.Health.Status}},"networks":{{json .NetworkSettings.Networks}}}`
	body, err := composeLiveDockerResult(ctx, f.docker, "container", "inspect", "--format", format, id)
	if err != nil {
		clear(body)
		return composeLiveContainer{}, false
	}
	defer clear(body)
	var value composeLiveContainer
	if err := json.Unmarshal(body, &value); err != nil {
		return composeLiveContainer{}, false
	}
	return value, true
}

func (f *composeLiveFixture) inspectOwnedNetwork(ctx context.Context, id string) (composeLiveNetwork, bool) {
	format := `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Labels}},"containers":{{json .Containers}}}`
	body, err := composeLiveDockerResult(ctx, f.docker, "network", "inspect", "--format", format, id)
	if err != nil {
		clear(body)
		return composeLiveNetwork{}, false
	}
	defer clear(body)
	var value composeLiveNetwork
	if err := json.Unmarshal(body, &value); err != nil {
		return composeLiveNetwork{}, false
	}
	return value, true
}

func (f *composeLiveFixture) writeReceipt(t *testing.T) {
	t.Helper()
	body, err := json.Marshal(f.receipt)
	if err != nil {
		t.Error("marshal ordinary Compose receipt")
		return
	}
	defer clear(body)
	path := filepath.Join(f.preflight.evidenceRoot, "receipt-"+strings.ToLower(f.caseName)+".json")
	if filepath.Dir(path) != f.preflight.evidenceRoot || os.WriteFile(path, append(body, '\n'), 0o600) != nil {
		t.Error("write ordinary Compose receipt")
	}
}

func composeLiveWriteSource(directory, appID, runToken, caseName string, unhealthy bool) error {
	health := `test: ["CMD", "node", "-e", "const http=require('http');if(!process.env.COMPOSE_LIVE_SECRET){process.exit(1)};const request=http.get('http://127.0.0.1:8080/',response=>{let body='';response.setEncoding('utf8');response.on('data',chunk=>body+=chunk);response.on('end',()=>process.exit(response.statusCode===200&&body===process.env.COMPOSE_LIVE_MARKER?0:1))});request.on('error',()=>process.exit(1));request.setTimeout(750,()=>{request.destroy();process.exit(1)})"]`
	if unhealthy {
		health = `test: ["CMD", "node", "-e", "process.exit(1)"]`
	}
	source := fmt.Sprintf(`services:
  app:
    image: %s
    command: ["node", "-e", "require('http').createServer((_,response)=>response.end(process.env.COMPOSE_LIVE_MARKER)).listen(8080,'0.0.0.0')"]
    environment:
      COMPOSE_LIVE_MARKER: ${COMPOSE_LIVE_MARKER}
      COMPOSE_LIVE_SECRET: ${COMPOSE_LIVE_SECRET}
    restart: "no"
    labels:
      %s: "1"
      %s: %q
      %s: %q
      %s: %q
    healthcheck:
      %s
      interval: 1s
      timeout: 1s
      retries: 2
networks:
  default:
    labels:
      %s: "1"
      %s: %q
      %s: %q
      %s: %q
`, composeLiveImage, composeLiveOwnershipLabel, composeLiveRunLabel, runToken, composeLiveCaseLabel, caseName, composeLiveAppLabel, appID,
		health, composeLiveOwnershipLabel, composeLiveRunLabel, runToken, composeLiveCaseLabel, caseName, composeLiveAppLabel, appID)
	return os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte(source), 0o600)
}

func composeLiveRandomSecret(t *testing.T) []byte {
	t.Helper()
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("generate ordinary Compose fixture secret")
	}
	encoded := make([]byte, hex.EncodedLen(len(value)))
	hex.Encode(encoded, value)
	clear(value)
	return encoded
}

func composeLiveDocker(t *testing.T, ctx context.Context, docker string, args ...string) []byte {
	t.Helper()
	body, err := composeLiveDockerResult(ctx, docker, args...)
	if err != nil {
		clear(body)
		t.Fatal("ordinary Compose Docker inspection command failed")
	}
	return body
}

func composeLiveDockerResult(ctx context.Context, docker string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, docker, args...)
	return command.CombinedOutput()
}

type composeLiveUnreachableSource struct{}

func (composeLiveUnreachableSource) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	return sourceconnections.SourceRepository{}, sourceconnections.Branch{}, errors.New("fixture source reader must not resolve a provider")
}

func (composeLiveUnreachableSource) ReadTree(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (githubapp.Tree, error) {
	return githubapp.Tree{}, errors.New("fixture source reader must not read a provider tree")
}

func (composeLiveUnreachableSource) DownloadArchive(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (io.ReadCloser, error) {
	return nil, errors.New("fixture source reader must not download a provider archive")
}
