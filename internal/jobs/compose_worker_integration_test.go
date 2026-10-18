package jobs_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/composeruntime"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/runtimeexecutor"
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

type providerCallCounter struct {
	mu    sync.Mutex
	calls int
}

func (counter *providerCallCounter) record() {
	if counter == nil {
		return
	}
	counter.mu.Lock()
	counter.calls++
	counter.mu.Unlock()
}

func (counter *providerCallCounter) count() int {
	if counter == nil {
		return 0
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.calls
}

type unreachableSourceReader struct{ calls *providerCallCounter }

func (reader unreachableSourceReader) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	reader.calls.record()
	return sourceconnections.SourceRepository{}, sourceconnections.Branch{}, errors.New("unexpected provider resolution")
}
func (reader unreachableSourceReader) ReadTree(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (githubapp.Tree, error) {
	reader.calls.record()
	return githubapp.Tree{}, errors.New("unexpected provider tree")
}
func (reader unreachableSourceReader) DownloadArchive(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (io.ReadCloser, error) {
	reader.calls.record()
	return nil, errors.New("unexpected provider archive")
}

type blockingComposeRunner struct {
	mu            sync.Mutex
	requests      []runtimeprocess.CommandRequest
	secretBuffers [][]byte
	configJSON    []byte
	upStarted     chan struct{}
	releaseUp     chan struct{}
	upStartedOnce sync.Once
	upResult      runtimeprocess.CommandResult
	upError       error
	afterUp       func() error
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

func (source *ownerScopedGitHubSources) ReadTree(_ context.Context, owner, connection string, installation int64, repository sourceconnections.SourceRepository, sha string) (githubapp.Tree, error) {
	if owner != source.owner || connection != source.connection || installation != source.installation || repository.ID != source.repo || repository.Owner != "octo" || repository.Name != "app" || sha != source.sha {
		return githubapp.Tree{}, errors.New("unexpected tree scope")
	}
	return githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: source.sha}}}, nil
}

func (source *ownerScopedGitHubSources) DownloadArchive(_ context.Context, owner, connection string, installation int64, repository sourceconnections.SourceRepository, sha string) (io.ReadCloser, error) {
	if owner != source.owner || connection != source.connection || installation != source.installation || repository.ID != source.repo || repository.Owner != "octo" || repository.Name != "app" || sha != source.sha {
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
	jobService, bindings := newComposeDeploymentJobService(t, db)
	executor, _, admission := newBoundComposeWorkerExecutor(t, db, dataRoot, bindings, applications, materializer, configuration, temporary, runner)
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
	go func() { workerDone <- jobService.RunWorkerWithScopedAdmission(ctx, executor, admission) }()
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
		r.mu.Lock()
		stdout := append([]byte(nil), r.configJSON...)
		r.mu.Unlock()
		if len(stdout) == 0 {
			stdout = []byte(`{"services":{"web":{"image":"nginx","privileged":true,"environment":{"TOKEN":"provider-secret"}}}}`)
		}
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
				r.mu.Lock()
				afterUp := r.afterUp
				r.mu.Unlock()
				if afterUp != nil {
					if err := afterUp(); err != nil {
						return runtimeprocess.CommandResult{}, err
					}
				}
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

func (r *blockingComposeRunner) setAfterUp(callback func() error) {
	r.mu.Lock()
	r.afterUp = callback
	r.mu.Unlock()
}

func (r *blockingComposeRunner) setConfigJSON(model []byte) {
	r.mu.Lock()
	r.configJSON = append(r.configJSON[:0], model...)
	r.mu.Unlock()
}

func (r *blockingComposeRunner) upCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, request := range r.requests {
		if hasArgument(request.Args, "up") {
			count++
		}
	}
	return count
}

type blockingComposeRunnerFailureDiagnostic struct {
	Total, Config, Up, Unknown int
	Last                       string
}

func (r *blockingComposeRunner) failureDiagnosticSnapshot() blockingComposeRunnerFailureDiagnostic {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := blockingComposeRunnerFailureDiagnostic{Last: "none"}
	for _, request := range r.requests {
		value.Total++
		switch blockingComposeRunnerCommandClass(request.Args) {
		case "config":
			value.Config++
			value.Last = "config"
		case "up":
			value.Up++
			value.Last = "up"
		default:
			value.Unknown++
			value.Last = "unknown"
		}
	}
	return value
}

func blockingComposeRunnerCommandClass(arguments []string) string {
	switch {
	case blockingComposeConfigCommand(arguments):
		return "config"
	case blockingComposeUpCommand(arguments):
		return "up"
	default:
		return "unknown"
	}
}

func blockingComposeConfigCommand(arguments []string) bool {
	return len(arguments) == 13 &&
		arguments[0] == "compose" &&
		arguments[1] == "--project-name" &&
		arguments[3] == "--project-directory" &&
		arguments[5] == "--env-file" &&
		arguments[7] == "-f" &&
		arguments[9] == "config" &&
		arguments[10] == "--format" &&
		arguments[11] == "json" &&
		arguments[12] == "--no-env-resolution"
}

func blockingComposeUpCommand(arguments []string) bool {
	return len(arguments) == 14 &&
		arguments[0] == "compose" &&
		arguments[1] == "--project-name" &&
		arguments[3] == "--project-directory" &&
		arguments[5] == "--env-file" &&
		arguments[7] == "-f" &&
		arguments[9] == "up" &&
		arguments[10] == "-d" &&
		arguments[11] == "--wait" &&
		arguments[12] == "--wait-timeout" &&
		blockingComposeWaitTimeout(arguments[13])
}

func blockingComposeWaitTimeout(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func blockingComposeJobStatusLabel(value string) string {
	switch jobs.Status(value) {
	case jobs.Queued, jobs.Assigned, jobs.Running, jobs.Waiting, jobs.WaitingUser, jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return value
	default:
		return "unknown"
	}
}

func blockingComposeJobPhaseLabel(value string) string {
	switch value {
	case "queued", "assigned", "validate", "prepare_workspace", "materialize_release", "render_compose", "evaluate_policy", "apply_runtime", "wait_for_health", "finalize", "running", "waiting_external", "waiting_user", "cancelling", "succeeded", "failed", "cancelled", "interrupted", "needs_attention", "approval_required", "migration_approval_required", "insufficient_replacement_capacity", "route_reconciliation_required":
		return value
	default:
		return "unknown"
	}
}

func TestBlockingComposeRunnerFailureDiagnosticSnapshotIsAllowlisted(t *testing.T) {
	runner := &blockingComposeRunner{
		requests: []runtimeprocess.CommandRequest{
			{Args: []string{"compose", "--project-name", "config-secret", "--project-directory", "C:/private/project", "--env-file", "C:/private/config-secret.env", "-f", "C:/private/config-secret.yaml", "config", "--format", "json", "--no-env-resolution"}, Env: []string{"TOKEN=config-secret"}},
			{Args: []string{"compose", "--project-name", "config", "--project-directory", "C:/private/project", "--env-file", "C:/private/runtime-secret.env", "-f", "C:/private/compose.yaml", "up", "-d", "--wait", "--wait-timeout", "30"}, Env: []string{"TOKEN=runtime-secret"}},
			{Args: []string{"compose", "mystery", "argument-secret"}, Env: []string{"TOKEN=unknown-secret"}},
		},
		upResult: runtimeprocess.CommandResult{Stdout: []byte("runtime-output-secret"), Stderr: []byte("runtime-error-secret")},
		upError:  errors.New("runner-error-secret"),
	}
	diagnostic := runner.failureDiagnosticSnapshot()
	if diagnostic.Total != 3 || diagnostic.Config != 1 || diagnostic.Up != 1 || diagnostic.Unknown != 1 || diagnostic.Last != "unknown" {
		t.Fatalf("diagnostic=%#v", diagnostic)
	}
	body, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "private", "mystery", "argument"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, body)
		}
	}
}

func TestBlockingComposeRunnerCommandClassRequiresExactComposeShape(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "exact config",
			args: []string{"compose", "--project-name", "project", "--project-directory", "workspace", "--env-file", "environment", "-f", "compose.yaml", "config", "--format", "json", "--no-env-resolution"},
			want: "config",
		},
		{
			name: "exact up with config project value",
			args: []string{"compose", "--project-name", "config", "--project-directory", "workspace", "--env-file", "environment", "-f", "compose.yaml", "up", "-d", "--wait", "--wait-timeout", "30"},
			want: "up",
		},
		{
			name: "config value in unrelated position",
			args: []string{"compose", "--project-name", "project", "--project-directory", "config", "--env-file", "environment", "-f", "compose.yaml", "up", "-d", "--wait", "--wait-timeout", "30"},
			want: "up",
		},
		{
			name: "config with extra argument",
			args: []string{"compose", "--project-name", "project", "--project-directory", "workspace", "--env-file", "environment", "-f", "compose.yaml", "config", "--format", "json", "--no-env-resolution", "extra"},
			want: "unknown",
		},
		{
			name: "up with malformed timeout",
			args: []string{"compose", "--project-name", "project", "--project-directory", "workspace", "--env-file", "environment", "-f", "compose.yaml", "up", "-d", "--wait", "--wait-timeout", "thirty"},
			want: "unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := blockingComposeRunnerCommandClass(test.args); got != test.want {
				t.Fatalf("command class=%q want=%q", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: string(jobs.Running), want: string(jobs.Running)},
		{value: "status-secret", want: "unknown"},
	} {
		if got := blockingComposeJobStatusLabel(test.value); got != test.want {
			t.Fatalf("status label=%q want=%q", got, test.want)
		}
	}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "render_compose", want: "render_compose"},
		{value: "phase-secret", want: "unknown"},
	} {
		if got := blockingComposeJobPhaseLabel(test.value); got != test.want {
			t.Fatalf("phase label=%q want=%q", got, test.want)
		}
	}
}

func (r *blockingComposeRunner) requestsSnapshot() []runtimeprocess.CommandRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	requests := make([]runtimeprocess.CommandRequest, len(r.requests))
	for index, request := range r.requests {
		requests[index] = request
		requests[index].Args = append([]string(nil), request.Args...)
		requests[index].Env = append([]string(nil), request.Env...)
	}
	return requests
}

func customVolumeModel(project, volumeName string) []byte {
	model, err := json.Marshal(map[string]any{
		"services": map[string]any{"web": map[string]any{
			"image":    "nginx",
			"volumes":  []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}},
			"networks": map[string]any{"private": nil},
		}},
		"volumes":  map[string]any{"data": map[string]any{"name": volumeName}},
		"networks": map[string]any{"private": map[string]any{"name": project + "_private"}},
	})
	if err != nil {
		panic(err)
	}
	return model
}

func TestLegacyLocalSourceDraftMigratesAndCompletesManagedComposeDeployment(t *testing.T) {
	sourceRoot := t.TempDir()
	composeBody := []byte("services:\n  web:\n    image: nginx\n")
	markerBody := []byte("legacy-source-must-not-change\n")
	if err := os.WriteFile(filepath.Join(sourceRoot, "compose.yaml"), composeBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "source-marker.txt"), markerBody, 0o600); err != nil {
		t.Fatal(err)
	}

	dataRoot := t.TempDir()
	db, actorID, appID := openLegacyFoundationDatabase(t, dataRoot, sourceRoot)
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}

	applicationStore := apps.New(db)
	application, err := applicationStore.Get(appID)
	if err != nil {
		t.Fatal(err)
	}
	if application.Status != "draft" || application.Source.Type != apps.SourceLocal || application.Source.Path != sourceRoot {
		t.Fatalf("migrated legacy application=%#v", application)
	}
	if application.Source.ConnectionID != "" || application.Source.InstallationID != 0 || application.Source.RepositoryID != 0 || application.Source.RepositoryOwner != "" || application.Source.RepositoryName != "" || application.Source.TrackedBranch != "" || application.Source.TrackedRef != "" || application.Source.ComposePath != "" || application.Source.ResolvedSHA != "" {
		t.Fatalf("local source retained provider metadata: %#v", application.Source)
	}
	var sourceRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_sources WHERE application_id=? AND source_type='local'`, appID).Scan(&sourceRows); err != nil || sourceRows != 1 {
		t.Fatalf("backfilled local source rows=%d err=%v", sourceRows, err)
	}

	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := configuration.Replace(context.Background(), appID, actorID, appconfig.ReplaceInput{
		ExpectedRevisionNumber: 0,
		Secrets:                []appconfig.ValueInput{{Key: "TOKEN", Value: "runtime-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := securetemp.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := &providerCallCounter{}
	materializer, err := releasesnapshot.New(db, unreachableSourceReader{calls: providerCalls}, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	jobService, bindings := newComposeDeploymentJobService(t, db)
	releaseUp := make(chan struct{})
	var releaseUpOnce sync.Once
	release := func() { releaseUpOnce.Do(func() { close(releaseUp) }) }
	runner := &blockingComposeRunner{upStarted: make(chan struct{}), releaseUp: releaseUp}
	runner.setConfigJSON([]byte(`{"services":{"web":{"image":"nginx","environment":{"TOKEN":"runtime-secret"}}}}`))
	executor, deploymentRepository, admission := newBoundComposeWorkerExecutor(t, db, dataRoot, bindings, applicationStore, materializer, configuration, temporary, runner)
	fixture := &composeWorkerFixture{
		db:            db,
		jobs:          jobService,
		deployments:   deploymentRepository,
		configuration: configuration,
		temporary:     temporary,
		runner:        runner,
		executor:      executor,
		admission:     admission,
		bindings:      bindings,
		app:           application,
		actorID:       actorID,
		dataRoot:      dataRoot,
	}
	fixture.startWorker(t)
	t.Cleanup(release)
	job := fixture.createJob(t)
	select {
	case <-runner.upStarted:
	case <-time.After(20 * time.Second):
		diagnostic := runner.failureDiagnosticSnapshot()
		durable, getErr := jobService.Get(job.ID)
		if getErr != nil {
			t.Fatalf("legacy local deployment did not reach compose up: durable_read=failed runner_total=%d runner_config=%d runner_up=%d runner_unknown=%d runner_last=%s", diagnostic.Total, diagnostic.Config, diagnostic.Up, diagnostic.Unknown, diagnostic.Last)
		}
		t.Fatalf("legacy local deployment did not reach compose up: durable_read=ok job_status=%s job_phase=%s runner_total=%d runner_config=%d runner_up=%d runner_unknown=%d runner_last=%s", blockingComposeJobStatusLabel(durable.Status), blockingComposeJobPhaseLabel(durable.Phase), diagnostic.Total, diagnostic.Config, diagnostic.Up, diagnostic.Unknown, diagnostic.Last)
	}
	release()
	completed := waitForJob(t, jobService, job.ID, jobs.Succeeded)
	if completed.ErrorCode != "" {
		t.Fatalf("completed legacy job=%#v", completed)
	}
	if providerCalls.count() != 0 {
		t.Fatalf("local deployment made %d provider calls", providerCalls.count())
	}

	application, err = applicationStore.Get(appID)
	if err != nil || application.Status != "draft" || application.Source.Type != apps.SourceLocal || application.Source.Path != sourceRoot {
		t.Fatalf("legacy draft changed after deployment: %#v err=%v", application, err)
	}
	history, err := deploymentRepository.List(context.Background(), appID, 10)
	if err != nil || len(history) != 1 || history[0].Status != deployments.Succeeded || history[0].ReleaseID == "" || history[0].ActualConfigurationRevisionID != revision.RevisionID || history[0].ActualConfigurationRevisionNumber != revision.RevisionNumber {
		t.Fatalf("legacy deployment history=%#v err=%v", history, err)
	}

	var provider, resolvedSHA, archiveSHA, composePath, workspaceRef, workspaceState, releaseStatus, configurationID string
	var workspaceSize, configurationNumber int64
	if err := db.QueryRow(`SELECT source_provider,resolved_sha,archive_sha256,compose_path,workspace_path,workspace_state,status,workspace_size_bytes,configuration_revision_id,configuration_revision_number FROM releases WHERE id=?`, history[0].ReleaseID).Scan(
		&provider, &resolvedSHA, &archiveSHA, &composePath, &workspaceRef, &workspaceState, &releaseStatus, &workspaceSize, &configurationID, &configurationNumber,
	); err != nil {
		t.Fatal(err)
	}
	if provider != apps.SourceLocal || len(resolvedSHA) != 64 || archiveSHA != resolvedSHA || composePath != "compose.yaml" || filepath.IsAbs(workspaceRef) || workspaceState != releasesnapshot.WorkspaceStateReady || releaseStatus != "ready" || workspaceSize <= 0 || configurationID != revision.RevisionID || configurationNumber != revision.RevisionNumber {
		t.Fatalf("legacy release provenance provider=%q resolved=%q archive=%q compose=%q workspace=%q state=%q status=%q size=%d configuration=%q/%d", provider, resolvedSHA, archiveSHA, composePath, workspaceRef, workspaceState, releaseStatus, workspaceSize, configurationID, configurationNumber)
	}
	managedWorkspace := filepath.Clean(filepath.Join(dataRoot, filepath.FromSlash(workspaceRef)))
	expectedWorkspace := filepath.Clean(filepath.Join(dataRoot, "apps", appID, "releases", history[0].ReleaseID, "workspace"))
	if managedWorkspace != expectedWorkspace {
		t.Fatalf("managed workspace=%q want exact release namespace=%q", managedWorkspace, expectedWorkspace)
	}
	if !withinTestRoot(dataRoot, managedWorkspace) || withinTestRoot(sourceRoot, managedWorkspace) || managedWorkspace == sourceRoot {
		t.Fatalf("managed workspace=%q dataRoot=%q sourceRoot=%q", managedWorkspace, dataRoot, sourceRoot)
	}
	if got, err := os.ReadFile(filepath.Join(managedWorkspace, "compose.yaml")); err != nil || !bytes.Equal(got, composeBody) {
		t.Fatalf("managed compose=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(managedWorkspace, "source-marker.txt")); err != nil || !bytes.Equal(got, markerBody) {
		t.Fatalf("managed marker=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(sourceRoot, "compose.yaml")); err != nil || !bytes.Equal(got, composeBody) {
		t.Fatalf("original compose changed=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(sourceRoot, "source-marker.txt")); err != nil || !bytes.Equal(got, markerBody) {
		t.Fatalf("original marker changed=%q err=%v", got, err)
	}

	requests := runner.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("compose requests=%#v", requests)
	}
	for _, request := range requests {
		if request.Directory != managedWorkspace || argumentAfter(request.Args, "--project-directory") != managedWorkspace || withinTestRoot(sourceRoot, request.Directory) {
			t.Fatalf("compose request did not use managed workspace: %#v", request)
		}
		if strings.Contains(strings.Join(request.Args, "\x00"), sourceRoot) {
			t.Fatalf("compose request referenced mutable source: %#v", request.Args)
		}
	}
	if got := argumentAfter(requests[0].Args, "-f"); got != filepath.Join(managedWorkspace, "compose.yaml") || !hasArgument(requests[0].Args, "config") {
		t.Fatalf("compose config source=%q args=%v", got, requests[0].Args)
	}
	if got := argumentAfter(requests[1].Args, "-f"); !hasArgument(requests[1].Args, "up") || !withinTestRoot(filepath.Join(dataRoot, "runtime", "compose"), got) {
		t.Fatalf("compose up source=%q args=%v", got, requests[1].Args)
	}
	assertNoRollbackAndSecretsCleared(t, runner)
	assertComposeRuntimeTempEmpty(t, dataRoot)
}

func openLegacyFoundationDatabase(t *testing.T, dataRoot, sourceRoot string) (*sql.DB, string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataRoot, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	foundation, err := os.ReadFile(filepath.Join("..", "database", "migrations", "001_foundation.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(foundation)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES('001_foundation.sql',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	actorID, machineID, appID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'legacy-owner','hash',?,?)`, actorID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,'legacy-local','local','ready','test','test','test','test',?,?)`, machineID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,description,source_path,active_machine_id,status,created_at,updated_at) VALUES(?,'legacy-local-draft','Legacy Local Draft','created before typed sources',?,?,'draft',?,?)`, appID, sourceRoot, machineID, now, now); err != nil {
		t.Fatal(err)
	}
	return db, actorID, appID
}

func withinTestRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func argumentAfter(arguments []string, name string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1]
		}
	}
	return ""
}

func TestComposeWorkerCustomResourceNameRequiresExactApprovalBeforeMutation(t *testing.T) {
	releaseUp := make(chan struct{})
	fixture := newComposeWorkerFixture(t, releaseUp)
	project := "rig-" + strings.ReplaceAll(fixture.app.ID, "-", "")
	fixture.runner.setConfigJSON(customVolumeModel(project, "shared-data"))
	fixture.startWorker(t)

	job := fixture.createJob(t)
	paused := waitForJob(t, fixture.jobs, job.ID, jobs.WaitingUser)
	if paused.PauseDisposition != "approval_required" || fixture.runner.upCallCount() != 0 {
		t.Fatalf("custom name mutated before approval: job=%#v upCalls=%d", paused, fixture.runner.upCallCount())
	}
	history, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 || history[0].Status != deployments.NeedsAttention {
		t.Fatalf("paused history=%#v err=%v", history, err)
	}
	finding := deploymentFindingByCapability(t, history[0].Findings, "custom_volume_name")
	wantScope := `{"applicationId":"` + fixture.app.ID + `","name":"shared-data","resource":"data"}`
	if finding.Disposition != composeruntime.DispositionApprovalRequired || finding.Scope != wantScope || strings.Contains(finding.Scope, "runtime-secret") {
		t.Fatalf("custom finding=%#v wantScope=%s", finding, wantScope)
	}
	if _, created, grantErr := fixture.deployments.Grant(context.Background(), fixture.app.ID, fixture.actorID, finding.Fingerprint); grantErr != nil || !created {
		t.Fatalf("grant created=%t err=%v", created, grantErr)
	}
	if _, err := fixture.jobs.Resume(job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.runner.upStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("approved custom name did not start up")
	}
	close(releaseUp)
	waitForJob(t, fixture.jobs, job.ID, jobs.Succeeded)
	if fixture.runner.upCallCount() != 1 {
		t.Fatalf("approved up calls=%d", fixture.runner.upCallCount())
	}

	fixture.runner.setConfigJSON(customVolumeModel(project, "shared-data-v2"))
	changedJob := fixture.createJob(t)
	waitForJob(t, fixture.jobs, changedJob.ID, jobs.WaitingUser)
	changedHistory, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(changedHistory) != 2 {
		t.Fatalf("changed history=%#v err=%v", changedHistory, err)
	}
	changed := deploymentFindingByCapability(t, changedHistory[0].Findings, "custom_volume_name")
	if changed.Fingerprint == finding.Fingerprint || changed.Scope == finding.Scope || fixture.runner.upCallCount() != 1 {
		t.Fatalf("changed name reused approval or mutated: original=%#v changed=%#v upCalls=%d", finding, changed, fixture.runner.upCallCount())
	}
	if _, err := fixture.jobs.Cancel(changedJob.ID); err != nil {
		t.Fatal(err)
	}
	waitForJob(t, fixture.jobs, changedJob.ID, jobs.Cancelled)
	assertNoRollbackAndSecretsCleared(t, fixture.runner)
	assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
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
	if err != nil || len(history) != 1 || history[0].Status != deployments.Applying || history[0].DiagnosticCode != "" {
		t.Fatalf("cancelled history=%#v err=%v", history, err)
	}
	if _, err := fixture.jobs.Resume(job.ID); err == nil {
		t.Fatal("cancelled job unexpectedly resumed")
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
			if err != nil || len(history) != 1 || history[0].Status != deployments.Applying || history[0].DiagnosticCode != "" || history[0].FailureSummary != "" {
				t.Fatalf("deployment failure=%#v err=%v", history, err)
			}
			if _, err := fixture.jobs.Resume(job.ID); err == nil {
				t.Fatal("failed job unexpectedly resumed")
			}
			assertNoRollbackAndSecretsCleared(t, fixture.runner)
			assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestComposeWorkerAcknowledgedUpFinalizeFailureRecoversWithoutDuplicateMutation(t *testing.T) {
	releaseUp := make(chan struct{})
	fixture := newComposeWorkerFixture(t, releaseUp)
	fixture.startWorker(t)
	job := fixture.createJob(t)
	waitForJob(t, fixture.jobs, job.ID, jobs.WaitingUser)
	history, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("paused history=%#v err=%v", history, err)
	}
	deploymentID := history[0].ID
	fingerprint := approvalFingerprint(t, history[0].Findings)
	if _, _, err := fixture.deployments.Grant(context.Background(), fixture.app.ID, fixture.actorID, fingerprint); err != nil {
		t.Fatal(err)
	}
	fixture.runner.setAfterUp(func() error {
		_, err := fixture.db.Exec(`CREATE TRIGGER compose_finalize_fault BEFORE UPDATE OF phase ON jobs WHEN NEW.phase='finalize' BEGIN SELECT RAISE(ABORT,'fixture finalize failure'); END`)
		return err
	})
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
	if failed.ErrorCode != "internal_error" {
		t.Fatalf("finalize failure job=%#v", failed)
	}
	history, err = fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(history) != 1 || history[0].ID != deploymentID || history[0].JobID != job.ID || history[0].Status != deployments.WaitingHealth {
		t.Fatalf("acknowledged deployment history=%#v err=%v", history, err)
	}
	if fixture.runner.upCallCount() != 1 {
		t.Fatalf("up calls=%d", fixture.runner.upCallCount())
	}
	replayed, created, err := fixture.deployments.GetOrCreateByJob(context.Background(), fixture.app.ID, job.ID, "current")
	if err != nil || created || replayed.ID != deploymentID {
		t.Fatalf("pre-restart replay=%#v created=%t err=%v", replayed, created, err)
	}
	fixture.stopWorker(t)
	if err := fixture.temporary.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.jobs.RecoverRuntimeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	recoveredJob, err := fixture.jobs.Get(job.ID)
	if err != nil || recoveredJob.Status != string(jobs.Failed) || recoveredJob.Attempt != failed.Attempt {
		t.Fatalf("recovered job=%#v err=%v", recoveredJob, err)
	}
	recovered, err := fixture.deployments.Get(context.Background(), fixture.app.ID, deploymentID)
	if err != nil || recovered.Status != deployments.Failed || recovered.DiagnosticCode != "daemon_restarted" || recovered.JobID != job.ID {
		t.Fatalf("recovered deployment=%#v err=%v", recovered, err)
	}
	if _, err := fixture.jobs.Resume(job.ID); err == nil {
		t.Fatal("failed finalized job unexpectedly resumed")
	}
	decoy := fixture.createJob(t)
	iterationDone := make(chan struct{})
	var iterationOnce sync.Once
	admission := func(parent context.Context) (context.Context, func() error, error) {
		admitted, release, err := fixture.admission(parent)
		if err != nil {
			return nil, nil, err
		}
		if _, err := fixture.jobs.Cancel(decoy.ID); err != nil {
			_ = release()
			return nil, nil, err
		}
		return admitted, func() error {
			err := release()
			iterationOnce.Do(func() { close(iterationDone) })
			return err
		}, nil
	}
	workerContext, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- fixture.jobs.RunWorkerWithScopedAdmission(workerContext, fixture.executor, admission)
	}()
	select {
	case <-iterationDone:
	case <-time.After(5 * time.Second):
		cancelWorker()
		t.Fatal("restarted worker did not complete the empty claim iteration")
	}
	cancelWorker()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("restarted worker: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restarted worker did not stop")
	}
	afterWorker, err := fixture.jobs.Get(job.ID)
	if err != nil || afterWorker.Status != string(jobs.Failed) || afterWorker.Attempt != failed.Attempt {
		t.Fatalf("post-worker job=%#v err=%v", afterWorker, err)
	}
	replayed, created, err = fixture.deployments.GetOrCreateByJob(context.Background(), fixture.app.ID, job.ID, "current")
	if err != nil || created || replayed.ID != deploymentID || fixture.runner.upCallCount() != 1 {
		t.Fatalf("post-restart replay=%#v created=%t upCalls=%d err=%v", replayed, created, fixture.runner.upCallCount(), err)
	}
	assertNoRollbackAndSecretsCleared(t, fixture.runner)
	assertComposeRuntimeTempEmpty(t, fixture.dataRoot)
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
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='apply_runtime',attempt=1 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	binding, err := fixture.bindings.Read(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, job.ID, fixture.app.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateCtx, releaseGate, err := fixture.admission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.deployments.Gate(gateCtx, deployments.GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		_ = releaseGate()
		t.Fatal(err)
	}
	if err := releaseGate(); err != nil {
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
	if err := fixture.jobs.RecoverRuntimeContext(context.Background()); err != nil {
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
	if _, err := fixture.jobs.Resume(job.ID); err == nil {
		t.Fatal("interrupted job unexpectedly resumed")
	}
	if len(fixture.runner.requests) != 0 {
		t.Fatalf("restart reran Compose: %#v", fixture.runner.requests)
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
	executor      jobs.Executor
	admission     jobs.ScopedAdmission
	bindings      *deploymentbinding.Repository
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
	jobService, bindings := newComposeDeploymentJobService(t, db)
	runner := &blockingComposeRunner{upStarted: make(chan struct{}), releaseUp: releaseUp}
	releaseMaterializer, err := releasesnapshot.New(db, unreachableSourceReader{}, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	executor, deploymentRepository, admission := newBoundComposeWorkerExecutor(t, db, dataRoot, bindings, applicationStore, releaseMaterializer, configuration, temporary, runner)
	return &composeWorkerFixture{db: db, jobs: jobService, deployments: deploymentRepository, configuration: configuration, temporary: temporary, runner: runner, executor: executor, admission: admission, bindings: bindings, app: app, actorID: actorID, dataRoot: dataRoot}
}

func (f *composeWorkerFixture) startWorker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancelWorker = cancel
	done := make(chan error, 1)
	f.workerDone = done
	go func() { done <- f.jobs.RunWorkerWithScopedAdmission(ctx, f.executor, f.admission) }()
	t.Cleanup(func() { f.stopWorker(t) })
}

func (f *composeWorkerFixture) stopWorker(t *testing.T) {
	t.Helper()
	if f.cancelWorker == nil {
		return
	}
	f.cancelWorker()
	done := f.workerDone
	f.cancelWorker = nil
	f.workerDone = nil
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("worker stopped: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("worker did not stop")
	}
}

func (f *composeWorkerFixture) createJob(t *testing.T) jobs.Job {
	t.Helper()
	result, err := f.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: f.app.ID,
		RequestedBy:   f.actorID,
		Input: jobs.DeploymentInput{
			ConfigurationMode: jobs.ConfigurationCurrent,
		},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !result.Created {
		t.Fatalf("job=%#v created=%t err=%v", result.Job, result.Created, err)
	}
	return result.Job
}

func newComposeDeploymentJobService(t *testing.T, db *sql.DB) (*jobs.Service, *deploymentbinding.Repository) {
	t.Helper()
	validator, err := applifecycle.NewDeploymentBindingValidator(applifecycle.New(db))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return service, bindings
}

func newBoundComposeWorkerExecutor(
	t *testing.T,
	db *sql.DB,
	dataRoot string,
	bindings *deploymentbinding.Repository,
	applications *apps.Store,
	releases *releasesnapshot.Materializer,
	configuration *appconfig.Store,
	temporary *securetemp.Manager,
	runner runtimeprocess.CommandRunner,
) (jobs.Executor, *deployments.Repository, jobs.ScopedAdmission) {
	t.Helper()
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(applifecycle.New(db))
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	deploymentRepository, err := deployments.NewWithExecutionGuard(db, guard)
	if err != nil {
		t.Fatal(err)
	}
	boundExecutor, err := composeruntime.NewExecutor(applications, releases, configuration, deploymentRepository, temporary, runner, composeruntime.ExecutorOptions{
		DockerExecutable: "docker-test",
		ConfigTimeout:    5 * time.Second,
		ApplyTimeout:     30 * time.Second,
		WaitTimeout:      10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	router, err := runtimeexecutor.New(deploymentRepository, plans, releases, boundExecutor, nil, bindings, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	admission := func(parent context.Context) (context.Context, func() error, error) {
		scope, release, err := deploymenteffects.AcquireScoped(parent, directories.WorkingDirectory)
		if err != nil {
			return nil, nil, err
		}
		return deploymenteffects.WithScope(parent, scope), release, nil
	}
	return router, deploymentRepository, admission
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

func deploymentFindingByCapability(t *testing.T, findings []deployments.Finding, capability string) deployments.Finding {
	t.Helper()
	for _, finding := range findings {
		if finding.Capability == capability {
			return finding
		}
	}
	t.Fatalf("%s finding not found in %#v", capability, findings)
	return deployments.Finding{}
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
