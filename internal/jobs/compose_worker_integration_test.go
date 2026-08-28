package jobs_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/composeruntime"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/sourceconnections"
)

type noReleaseResolver struct{}

func (noReleaseResolver) Materialize(context.Context, string, string) (releasesnapshot.Release, error) {
	return releasesnapshot.Release{}, errors.New("unexpected release materialization")
}

func (noReleaseResolver) MaterializeLocal(context.Context, string, string) (releasesnapshot.Release, error) {
	return releasesnapshot.Release{}, errors.New("unexpected local release materialization")
}

func (noReleaseResolver) ReadyRelease(context.Context, string, string) (releasesnapshot.Release, error) {
	return releasesnapshot.Release{}, errors.New("unexpected release lookup")
}

type unreachableSourceReader struct{}

func (unreachableSourceReader) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	return sourceconnections.SourceRepository{}, sourceconnections.Branch{}, errors.New("unexpected provider resolution")
}
func (unreachableSourceReader) ReadTree(context.Context, string, string, int64, string) (githubapp.Tree, error) {
	return githubapp.Tree{}, errors.New("unexpected provider tree")
}
func (unreachableSourceReader) DownloadArchive(context.Context, string, string, int64, string) (io.ReadCloser, error) {
	return nil, errors.New("unexpected provider archive")
}

type blockingComposeRunner struct {
	mu            sync.Mutex
	requests      []runtimeprocess.CommandRequest
	secretBuffers [][]byte
	upStarted     chan struct{}
	releaseUp     chan struct{}
	upStartedOnce sync.Once
	upResult      runtimeprocess.CommandResult
	upError       error
}

type ownerScopedGitHubSources struct {
	owner, connection    string
	installation, repo   int64
	branch, sha          string
	archive              []byte
	mu                   sync.Mutex
	resolveCalls         int
	materializationCalls int
}

func (source *ownerScopedGitHubSources) Resolve(_ context.Context, owner, connection string, installation, repository int64, branch string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	if owner != source.owner || connection != source.connection || installation != source.installation || repository != source.repo || branch != source.branch {
		return sourceconnections.SourceRepository{}, sourceconnections.Branch{}, errors.New("unexpected source scope")
	}
	source.mu.Lock()
	source.resolveCalls++
	source.mu.Unlock()
	return sourceconnections.SourceRepository{ID: source.repo, Owner: "octo", Name: "app"}, sourceconnections.Branch{Name: source.branch, SHA: source.sha}, nil
}

func (source *ownerScopedGitHubSources) ResolveHead(ctx context.Context, scope autodeploy.SourceScope) (string, error) {
	_, branch, err := source.Resolve(ctx, scope.OwnerUserID, scope.ConnectionID, scope.InstallationID, scope.RepositoryID, scope.Branch)
	if err != nil || scope.Ref != "refs/heads/"+scope.Branch {
		return "", errors.New("unexpected coordinator source scope")
	}
	return branch.SHA, nil
}

func (source *ownerScopedGitHubSources) ReadTree(_ context.Context, owner, connection string, repository int64, sha string) (githubapp.Tree, error) {
	if owner != source.owner || connection != source.connection || repository != source.repo || sha != source.sha {
		return githubapp.Tree{}, errors.New("unexpected tree scope")
	}
	return githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: source.sha}}}, nil
}

func (source *ownerScopedGitHubSources) DownloadArchive(_ context.Context, owner, connection string, repository int64, sha string) (io.ReadCloser, error) {
	if owner != source.owner || connection != source.connection || repository != source.repo || sha != source.sha {
		return nil, errors.New("unexpected archive scope")
	}
	source.mu.Lock()
	source.materializationCalls++
	source.mu.Unlock()
	return io.NopCloser(bytes.NewReader(source.archive)), nil
}

type successfulComposeRunner struct {
	mu       sync.Mutex
	requests []runtimeprocess.CommandRequest
}

func (runner *successfulComposeRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	runner.mu.Lock()
	runner.requests = append(runner.requests, request)
	runner.mu.Unlock()
	if hasArgument(request.Args, "config") {
		return runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{"image":"nginx"}}}`)}, nil
	}
	if hasArgument(request.Args, "up") {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected compose command")
}

func (source *ownerScopedGitHubSources) counts() (int, int) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.resolveCalls, source.materializationCalls
}

func TestCoordinatorJobRunsOwnerScopedGitHubComposeMaterialization(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	owner, machineID := uuid.NewString(), uuid.NewString()
	controllerID, bindingID := uuid.NewString(), uuid.NewString()
	connectionID := "0123456789abcdef0123456789abcdef"
	const installationID, repositoryID = int64(3), int64(7)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err = db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,'owner','hash','administrator',?,?)`, owner, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,'local','local','ready','test','test','test','test',?,?)`, machineID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES(?,?,'github','connected','1','owner',1,?,?,?,?,?)`, connectionID, owner, stamp, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	applications := apps.New(db)
	application, err := applications.CreateWithSource("GitHub Runtime App", "", machineID, apps.Source{Type: apps.SourceGitHub, ConnectionID: connectionID, InstallationID: installationID, RepositoryID: repositoryID, RepositoryOwner: "octo", RepositoryName: "app", TrackedBranch: "main", TrackedRef: "refs/heads/main", ComposePath: "compose.yaml", ResolvedSHA: sha})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, bindingID, owner, connectionID, controllerID, installationID, repositoryID, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configuration.Replace(context.Background(), application.ID, owner, appconfig.ReplaceInput{ExpectedRevisionNumber: 0}); err != nil {
		t.Fatal(err)
	}
	sources := &ownerScopedGitHubSources{owner: owner, connection: connectionID, installation: installationID, repo: repositoryID, branch: "main", sha: sha, archive: githubComposeArchive(t)}
	materializer, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := securetemp.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	runner := &successfulComposeRunner{}
	deploymentRepository := deployments.New(db)
	executor, err := composeruntime.NewExecutor(applications, materializer, configuration, deploymentRepository, temporary, runner, composeruntime.ExecutorOptions{DockerExecutable: "docker-test", ConfigTimeout: 5 * time.Second, ApplyTimeout: 30 * time.Second, WaitTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(db)
	autoRepository := autodeploy.NewRepository(db)
	if _, err = autoRepository.Configure(context.Background(), autodeploy.ConfigureRequest{ApplicationID: application.ID, ActorUserID: owner, Enabled: true}, now); err != nil {
		t.Fatal(err)
	}
	coordinatorConfig := autodeploy.DefaultCoordinatorConfig()
	coordinatorConfig.PollInterval = 10 * time.Millisecond
	coordinatorConfig.MinResolveInterval = time.Nanosecond
	coordinatorConfig.LeaseTTL = 5 * time.Second
	coordinator, err := autodeploy.NewCoordinator(autoRepository, sources, jobService, coordinatorConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	workerDone, coordinatorDone := make(chan error, 1), make(chan error, 1)
	go func() { workerDone <- jobService.RunWorker(ctx, executor) }()
	go func() { coordinatorDone <- coordinator.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		for name, done := range map[string]<-chan error{"worker": workerDone, "coordinator": coordinatorDone} {
			select {
			case runErr := <-done:
				if runErr != nil {
					t.Errorf("%s stopped: %v", name, runErr)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("%s did not stop", name)
			}
		}
	})

	var jobID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if queryErr := db.QueryRow(`SELECT id FROM jobs WHERE resource_id=?`, application.ID).Scan(&jobID); queryErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if jobID == "" {
		t.Fatal("coordinator did not create a job")
	}
	completed := waitForJob(t, jobService, jobID, jobs.Succeeded)
	if completed.RequestedBy != owner || string(completed.Input) != `{"releaseId":"","configurationMode":"current"}` {
		t.Fatalf("coordinator job actor/input=%q/%q", completed.RequestedBy, completed.Input)
	}
	resolveCalls, materializationCalls := sources.counts()
	if resolveCalls != 2 || materializationCalls != 1 {
		t.Fatalf("source calls resolve=%d materialize=%d", resolveCalls, materializationCalls)
	}
	var releaseSHA, provider string
	if err = db.QueryRow(`SELECT r.resolved_sha,r.source_provider FROM deployments d JOIN releases r ON r.id=d.release_id WHERE d.job_id=? AND d.status='succeeded'`, jobID).Scan(&releaseSHA, &provider); err != nil || releaseSHA != sha || provider != "github" {
		t.Fatalf("deployment provenance sha=%q provider=%q err=%v", releaseSHA, provider, err)
	}
	var persistedInput string
	if err = db.QueryRow(`SELECT input_json FROM jobs WHERE id=?`, jobID).Scan(&persistedInput); err != nil || strings.Contains(persistedInput, "token") || strings.Contains(persistedInput, "octo") || strings.Contains(persistedInput, sha) {
		t.Fatalf("job input contamination=%q err=%v", persistedInput, err)
	}
}

func githubComposeArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range []struct {
		name, body string
		typeFlag   byte
	}{{name: "repo/", typeFlag: tar.TypeDir}, {name: "repo/compose.yaml", body: "services:\n  web:\n    image: nginx\n", typeFlag: tar.TypeReg}} {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeFlag, Mode: 0o600, Size: int64(len(entry.body))}
		if entry.typeFlag == tar.TypeDir {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func (r *blockingComposeRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	if hasArgument(request.Args, "config") {
		stdout := []byte(`{"services":{"web":{"image":"nginx","privileged":true,"environment":{"TOKEN":"provider-secret"}}}}`)
		stderr := []byte("provider config diagnostics")
		r.mu.Lock()
		r.secretBuffers = append(r.secretBuffers, stdout, stderr)
		r.mu.Unlock()
		return runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr}, nil
	}
	if hasArgument(request.Args, "up") {
		r.upStartedOnce.Do(func() { close(r.upStarted) })
		if r.releaseUp != nil {
			select {
			case <-r.releaseUp:
				stdout := append([]byte(nil), r.upResult.Stdout...)
				stderr := append([]byte(nil), r.upResult.Stderr...)
				if len(stdout) == 0 {
					stdout = []byte("runtime output secret")
				}
				r.mu.Lock()
				r.secretBuffers = append(r.secretBuffers, stdout, stderr)
				r.mu.Unlock()
				return runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr}, r.upError
			case <-ctx.Done():
				return runtimeprocess.CommandResult{}, ctx.Err()
			}
		}
		<-ctx.Done()
		return runtimeprocess.CommandResult{}, ctx.Err()
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected Docker command")
}

func TestComposeWorkerApprovalResumeRevocationRaceAndSingleDeployment(t *testing.T) {
	fixture := newComposeWorkerFixture(t, make(chan struct{}))
	fixture.startWorker(t)
	job := fixture.createJob(t)

	paused := waitForJob(t, fixture.jobs, job.ID, jobs.WaitingUser)
	if paused.Attempt != 1 || paused.PauseDisposition != "approval_required" {
		t.Fatalf("paused job=%#v", paused)
	}
	history, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 || history[0].Status != deployments.NeedsAttention || len(history[0].Findings) == 0 {
		t.Fatalf("paused history=%#v err=%v", history, err)
	}
	deploymentID := history[0].ID
	fingerprint := approvalFingerprint(t, history[0].Findings)
	approval, created, err := fixture.deployments.Grant(context.Background(), fixture.app.ID, fixture.actorID, fingerprint)
	if err != nil || !created {
		t.Fatalf("grant=%#v created=%t err=%v", approval, created, err)
	}
	if _, err := fixture.jobs.Resume(job.ID); err != nil {
		t.Fatal(err)
	}

	select {
	case <-fixture.runner.upStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("up did not start")
	}
	if _, err := fixture.deployments.Revoke(context.Background(), fixture.app.ID, approval.ID, fixture.actorID); !errors.Is(err, deployments.ErrApprovalInUse) {
		t.Fatalf("active mutation revocation=%v", err)
	}
	close(fixture.runner.releaseUp)

	succeeded := waitForJob(t, fixture.jobs, job.ID, jobs.Succeeded)
	if succeeded.Attempt != 2 || succeeded.ErrorCode != "" {
		t.Fatalf("succeeded job=%#v", succeeded)
	}
	history, err = fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 || history[0].ID != deploymentID || history[0].Status != deployments.Succeeded {
		t.Fatalf("final history=%#v err=%v", history, err)
	}
	if _, err := fixture.deployments.Revoke(context.Background(), fixture.app.ID, approval.ID, fixture.actorID); err != nil {
		t.Fatalf("post-mutation revoke=%v", err)
	}
	assertNoRollbackAndSecretsCleared(t, fixture.runner)
	assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
	events, err := fixture.jobs.Events(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenAttempts := map[int]bool{}
	for _, event := range events {
		seenAttempts[event.Attempt] = true
	}
	if !seenAttempts[1] || !seenAttempts[2] {
		t.Fatalf("event attempts=%v", seenAttempts)
	}
}

func TestComposeWorkerCancellationPreservesPartialRuntimeAndCleansTemp(t *testing.T) {
	fixture := newComposeWorkerFixture(t, nil)
	fixture.startWorker(t)
	job := fixture.createJob(t)
	waitForJob(t, fixture.jobs, job.ID, jobs.WaitingUser)
	history, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("history=%#v err=%v", history, err)
	}
	fingerprint := approvalFingerprint(t, history[0].Findings)
	if _, _, err := fixture.deployments.Grant(context.Background(), fixture.app.ID, fixture.actorID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.jobs.Resume(job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.runner.upStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("up did not start")
	}
	if _, err := fixture.jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	waitForJob(t, fixture.jobs, job.ID, jobs.Cancelled)
	history, err = fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 || history[0].Status != deployments.Cancelled || history[0].DiagnosticCode != "cancelled" {
		t.Fatalf("cancelled history=%#v err=%v", history, err)
	}
	assertNoRollbackAndSecretsCleared(t, fixture.runner)
	assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestComposeWorkerApplyAndOuterHealthFailuresAreSanitizedWithoutRollback(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "nonzero up", err: errors.New("provider-secret exit status 1"), code: "compose_apply_failed"},
		{name: "outer timeout", err: context.DeadlineExceeded, code: "compose_apply_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			releaseUp := make(chan struct{})
			fixture := newComposeWorkerFixture(t, releaseUp)
			fixture.runner.upError = test.err
			fixture.runner.upResult = runtimeprocess.CommandResult{Stdout: []byte("runtime-secret stdout"), Stderr: []byte("provider-secret stderr")}
			fixture.startWorker(t)
			job := fixture.createJob(t)
			waitForJob(t, fixture.jobs, job.ID, jobs.WaitingUser)
			history, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
			if err != nil || len(history) != 1 {
				t.Fatalf("history=%#v err=%v", history, err)
			}
			fingerprint := approvalFingerprint(t, history[0].Findings)
			if _, _, err := fixture.deployments.Grant(context.Background(), fixture.app.ID, fixture.actorID, fingerprint); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.jobs.Resume(job.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-fixture.runner.upStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("up did not start")
			}
			close(releaseUp)
			failed := waitForJob(t, fixture.jobs, job.ID, jobs.Failed)
			if failed.ErrorCode != test.code || failed.ErrorDetail == "" || containsRawSecret(failed.ErrorDetail) {
				t.Fatalf("job failure=%#v", failed)
			}
			history, err = fixture.deployments.List(context.Background(), fixture.app.ID, 10)
			if err != nil || len(history) != 1 || history[0].Status != deployments.Failed || history[0].DiagnosticCode != test.code || containsRawSecret(history[0].FailureSummary) {
				t.Fatalf("deployment failure=%#v err=%v", history, err)
			}
			assertNoRollbackAndSecretsCleared(t, fixture.runner)
			assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestComposeRestartRecoveryCleansExactTempAndDoesNotDuplicateLinkage(t *testing.T) {
	fixture := newComposeWorkerFixture(t, nil)
	job := fixture.createJob(t)
	deployment, created, err := fixture.deployments.GetOrCreateByJob(context.Background(), fixture.app.ID, job.ID, "current")
	if err != nil || !created {
		t.Fatalf("deployment=%#v created=%t err=%v", deployment, created, err)
	}
	configuration, err := fixture.configuration.ExportCurrentForExecution(context.Background(), fixture.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = fixture.deployments.Initialize(context.Background(), fixture.app.ID, deployment.ID, "", configuration.RevisionID, configuration.RevisionNumber)
	configuration.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.deployments.Gate(context.Background(), fixture.app.ID, deployment.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='apply_runtime',attempt=1 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	files, err := fixture.temporary.Create(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteEnv([]byte("TOKEN='restart-secret'\n")); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCompose([]byte(`{"services":{"web":{}}}`)); err != nil {
		t.Fatal(err)
	}

	if err := fixture.temporary.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.deployments.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.jobs.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	persistedJob, err := fixture.jobs.Get(job.ID)
	if err != nil || persistedJob.Status != string(jobs.Interrupted) {
		t.Fatalf("job=%#v err=%v", persistedJob, err)
	}
	persistedDeployment, err := fixture.deployments.Get(context.Background(), fixture.app.ID, deployment.ID)
	if err != nil || persistedDeployment.Status != deployments.Failed || persistedDeployment.DiagnosticCode != "daemon_restarted" {
		t.Fatalf("deployment=%#v err=%v", persistedDeployment, err)
	}
	replayed, created, err := fixture.deployments.GetOrCreateByJob(context.Background(), fixture.app.ID, job.ID, "current")
	if err != nil || created || replayed.ID != deployment.ID {
		t.Fatalf("replay=%#v created=%t err=%v", replayed, created, err)
	}
	assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
}

type composeWorkerFixture struct {
	db            *sql.DB
	jobs          *jobs.Service
	deployments   *deployments.Repository
	configuration *appconfig.Store
	temporary     *securetemp.Manager
	runner        *blockingComposeRunner
	executor      *composeruntime.Executor
	app           apps.Application
	actorID       string
	dataRoot      string
	cancelWorker  context.CancelFunc
	workerDone    chan error
}

func newComposeWorkerFixture(t *testing.T, releaseUp chan struct{}) *composeWorkerFixture {
	t.Helper()
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	actorID := uuid.NewString()
	machineID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'owner','hash',?,?)`, actorID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,'local','local','ready','test','test','test','test',?,?)`, machineID, now, now); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applicationStore := apps.New(db)
	app, err := applicationStore.Create("Runtime App", "", workspace, machineID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configuration.Replace(context.Background(), app.ID, actorID, appconfig.ReplaceInput{ExpectedRevisionNumber: 0, Secrets: []appconfig.ValueInput{{Key: "TOKEN", Value: "runtime-secret"}}}); err != nil {
		t.Fatal(err)
	}
	temporary, err := securetemp.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(db)
	deploymentRepository := deployments.New(db)
	runner := &blockingComposeRunner{upStarted: make(chan struct{}), releaseUp: releaseUp}
	releaseMaterializer, err := releasesnapshot.New(db, unreachableSourceReader{}, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := composeruntime.NewExecutor(applicationStore, releaseMaterializer, configuration, deploymentRepository, temporary, runner, composeruntime.ExecutorOptions{
		DockerExecutable: "docker-test",
		ConfigTimeout:    5 * time.Second,
		ApplyTimeout:     30 * time.Second,
		WaitTimeout:      10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &composeWorkerFixture{db: db, jobs: jobService, deployments: deploymentRepository, configuration: configuration, temporary: temporary, runner: runner, executor: executor, app: app, actorID: actorID, dataRoot: dataRoot}
}

func (f *composeWorkerFixture) startWorker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancelWorker = cancel
	f.workerDone = make(chan error, 1)
	go func() { f.workerDone <- f.jobs.RunWorker(ctx, f.executor) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-f.workerDone:
			if err != nil {
				t.Errorf("worker stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
}

func (f *composeWorkerFixture) createJob(t *testing.T) jobs.Job {
	t.Helper()
	job, created, err := f.jobs.CreateWithInput(jobs.CreateRequest{
		Type:         "deploy",
		ResourceType: "application",
		ResourceID:   f.app.ID,
		RequestedBy:  f.actorID,
		Input: jobs.DeploymentInput{
			ConfigurationMode: jobs.ConfigurationCurrent,
		},
	})
	if err != nil || !created {
		t.Fatalf("job=%#v created=%t err=%v", job, created, err)
	}
	return job
}

func waitForJob(t *testing.T, service *jobs.Service, id string, status jobs.Status) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Get(id)
		if err == nil && job.Status == string(status) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := service.Get(id)
	t.Fatalf("job did not reach %s: %#v err=%v", status, job, err)
	return jobs.Job{}
}

func approvalFingerprint(t *testing.T, findings []deployments.Finding) string {
	t.Helper()
	for _, finding := range findings {
		if finding.Disposition == "approval_required" {
			return finding.Fingerprint
		}
	}
	t.Fatal("approval-required finding not found")
	return ""
}

func assertNoRollbackAndSecretsCleared(t *testing.T, runner *blockingComposeRunner) {
	t.Helper()
	runner.mu.Lock()
	defer runner.mu.Unlock()
	upCalls := 0
	for _, request := range runner.requests {
		if hasArgument(request.Args, "down") || hasArgument(request.Args, "rm") {
			t.Fatalf("rollback command executed: %v", request.Args)
		}
		if hasArgument(request.Args, "up") {
			upCalls++
		}
	}
	if upCalls != 1 {
		t.Fatalf("up calls=%d requests=%v", upCalls, runner.requests)
	}
	for _, buffer := range runner.secretBuffers {
		for _, character := range buffer {
			if character != 0 {
				t.Fatalf("secret-bearing runner output not cleared: %q", buffer)
			}
		}
	}
}

func assertComposeRuntimeTempEmpty(t *testing.T, dataRoot string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "runtime", "compose"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp entries=%v err=%v", entries, err)
	}
}

func hasArgument(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}

func containsRawSecret(value string) bool {
	return strings.Contains(value, "provider-secret") || strings.Contains(value, "runtime-secret")
}
