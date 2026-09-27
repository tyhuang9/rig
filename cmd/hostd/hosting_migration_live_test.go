//go:build live_docker

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const (
	migrationJourneyProject = "rig-migration-journey-test"
	migrationJourneyNetwork = "rig-migration-journey-external"
)

// TestLiveGeneratedMigrationApprovalAndUncertaintyJourney proves the approved
// Knex path against a disposable, host-side PostgreSQL fixture. The test-only
// interruption occurs after the real short-lived migration container has
// committed its additive database changes and before state completion is
// recorded, so the durable Running state must refuse a replay after reopen.
func TestLiveGeneratedMigrationApprovalAndUncertaintyJourney(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_MIGRATION_JOURNEY") != "1" {
		t.Fatal("set RIG_RUN_LIVE_MIGRATION_JOURNEY=1 to run the hosted Docker gate")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("hosted migration gate requires the local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	sh := controllerJourneyExecutable(t, "sh")
	openssl := controllerJourneyExecutable(t, "openssl")
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	if selected, err := controllerJourneyDocker(ctx, docker, nil, "context", "show"); err != nil || string(bytes.TrimSpace(selected)) != "default" {
		t.Fatal("hosted migration gate requires Docker's default local context")
	}
	if endpoint, err := controllerJourneyDocker(ctx, docker, nil, "context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}"); err != nil || !strings.HasPrefix(string(bytes.TrimSpace(endpoint)), "unix:///") {
		t.Fatal("hosted migration gate requires a local Unix Docker endpoint")
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "info"); err != nil {
		t.Fatal("Docker daemon unavailable")
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "compose", "version"); err != nil {
		t.Fatal("Docker Compose unavailable")
	}
	for _, resource := range [][]string{
		{"network", migrationJourneyNetwork},
		{"volume", migrationJourneyProject + "_fixture-certs"},
		{"volume", migrationJourneyProject + "_fixture-postgres-data"},
	} {
		if controllerJourneyExists(t, ctx, docker, resource[0], resource[1]) {
			t.Fatalf("disposable daemon already has %s %s", resource[0], resource[1])
		}
	}
	if output, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-aq", "--filter", "label=com.docker.compose.project="+migrationJourneyProject); err != nil || len(bytes.TrimSpace(output)) != 0 {
		t.Fatal("disposable daemon already has the migration fixture project")
	}

	root := t.TempDir()
	dataRoot := filepath.Join(root, "controller")
	fixtureRoot := filepath.Join(root, "external")
	installed, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-migration"))
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := migrationJourneyTrackedFixtureFiles(installed)
	if err != nil {
		t.Fatal("verify committed migration fixture source:", err)
	}
	if err := os.Mkdir(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-compose.yml", "generate-test-ca.sh", "openssl-postgres.cnf", "openssl-https.cnf", "https-stub.mjs"} {
		contents, readErr := os.ReadFile(filepath.Join("..", "..", "examples", "hosting-notes", "harness", name))
		if readErr != nil || os.WriteFile(filepath.Join(fixtureRoot, name), contents, 0o600) != nil {
			t.Fatal("copy disposable external database fixture")
		}
	}
	// Freeze only the nonsecret CA into the reviewed source. The PostgreSQL leaf
	// is issued later for the app bridge's exact IP, from this same CA key that
	// remains only in the disposable external fixture.
	bootstrapCertificates := exec.CommandContext(ctx, sh, filepath.Join(fixtureRoot, "generate-test-ca.sh"))
	bootstrapCertificates.Dir = fixtureRoot
	bootstrapCertificates.Env = append(os.Environ(), "PATH="+filepath.Dir(openssl)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := bootstrapCertificates.Run(); err != nil {
		t.Fatal("generate disposable database root certificate")
	}
	source := filepath.Join(root, "source")
	if err := controllerJourneyStageSource(installed, source, tracked); err != nil {
		t.Fatal("stage immutable migration fixture source:", err)
	}
	ca, err := os.ReadFile(filepath.Join(fixtureRoot, "certs", "test-ca.crt"))
	if err != nil || os.WriteFile(filepath.Join(source, "test-ca.crt"), ca, 0o600) != nil {
		t.Fatal("stage nonsecret database root certificate with immutable source")
	}
	if bytes.Contains(ca, []byte("PRIVATE KEY")) {
		t.Fatal("migration source certificate unexpectedly contains private key material")
	}
	if _, err := os.Stat(filepath.Join(source, "test-ca.key")); !os.IsNotExist(err) {
		t.Fatal("migration source retained a disposable certificate private key")
	}

	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider := controllerJourneyNewGitHubProvider(t, source)
	for name, contents := range provider.files {
		if strings.HasSuffix(name, ".key") || bytes.Contains(contents, []byte("PRIVATE KEY")) {
			t.Fatal("controller archive unexpectedly contains disposable certificate private key material")
		}
	}
	sourceClock := time.Now().UTC()
	appStore := apps.New(db)
	jobStore := jobs.New(db)
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", func() time.Time { return sourceClock })
	snapshots, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentStore := deployments.New(db)
	migrationRunner := &migrationJourneyRunner{test: t}
	settings := config.Defaults()
	settings.DataRoot = dataRoot
	settings.GeneratedRuntime = true
	composition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: appStore, snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans,
	}, runtimeCompositionOptions{
		dockerExecutable: docker,
		migrationRunnerFactory: func(delegate generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
			migrationRunner.delegate = delegate
			return migrationRunner
		},
	})
	if err != nil {
		t.Fatal("compose production generated migration runtime:", err)
	}

	fixtureStarted := false
	var application apicontract.Application
	var appNetwork generatedruntime.AppNetworkDescription
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		if application.ID != "" {
			for _, kind := range []string{"container", "image"} {
				ids, listErr := controllerJourneyDocker(cleanup, docker, nil, map[string][]string{
					"container": {"ps", "-aq", "--filter", "label=io.rig.application=" + application.ID},
					"image":     {"image", "ls", "-q", "--filter", "label=io.rig.application=" + application.ID},
				}[kind]...)
				if listErr != nil {
					t.Errorf("list exact app-owned %s for cleanup", kind)
					continue
				}
				for _, id := range strings.Fields(string(ids)) {
					args := map[string][]string{"container": {"container", "rm", "--force", id}, "image": {"image", "rm", "--force", id}}[kind]
					if _, removeErr := controllerJourneyDocker(cleanup, docker, nil, args...); removeErr != nil {
						t.Errorf("remove exact app-owned %s", kind)
					}
				}
			}
		}
		migrationJourneyCleanupIngress(t, cleanup, docker)
		if fixtureStarted {
			if _, downErr := controllerJourneyDocker(cleanup, docker, nil, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", migrationJourneyProject, "down", "--volumes", "--remove-orphans"); downErr != nil {
				t.Error("remove exact external migration fixture project")
			}
		}
		controllerJourneyRemoveBuilder(t, cleanup, docker, dataRoot)
		if appNetwork.Name != "" && controllerJourneyExists(t, cleanup, docker, "network", appNetwork.Name) {
			labelsBody, inspectErr := controllerJourneyDocker(cleanup, docker, nil, "network", "inspect", "--format", "{{json .Labels}}", appNetwork.Name)
			var labels map[string]string
			if inspectErr != nil || json.Unmarshal(bytes.TrimSpace(labelsBody), &labels) != nil || labels["io.rig.managed"] != generatedruntime.NetworkOwnershipLabelValue || labels["io.rig.application"] != application.ID {
				t.Error("app network ownership uncertain; retaining")
			} else if _, removeErr := controllerJourneyDocker(cleanup, docker, nil, "network", "rm", appNetwork.Name); removeErr != nil {
				t.Error("remove exact app-private network")
			}
		}
		for _, resource := range [][]string{{"network", migrationJourneyNetwork}, {"volume", migrationJourneyProject + "_fixture-certs"}, {"volume", migrationJourneyProject + "_fixture-postgres-data"}} {
			if controllerJourneyExists(t, cleanup, docker, resource[0], resource[1]) {
				t.Errorf("external migration fixture %s remains", resource[0])
			}
		}
	})

	authService := auth.New(db)
	bootstrap, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := authService.Bootstrap(bootstrap, "migration-admin", "a sufficiently long disposable passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machines.New(db).EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer((&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobStore, Machines: machines.New(db), Sources: sources,
		Configuration: configuration, Deployments: deploymentStore, DeploymentPlans: plans,
		GeneratedIngress: composition.ingress, GeneratedRuntimeState: composition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer api.Close()
	request := migrationJourneyRequest(t, ctx, api.URL, session)

	var setup apicontract.DeploymentSetupInput
	setupBody, err := os.ReadFile(filepath.Join(source, "rig-setup.json"))
	if err != nil || json.Unmarshal(setupBody, &setup) != nil {
		t.Fatal("read reviewed migration setup")
	}
	var authorization apicontract.GitHubDeviceAuthorization
	request(http.MethodPost, "/api/v1/source-connections/github/device", nil, http.StatusCreated, &authorization)
	sourceClock = sourceClock.Add(2 * time.Second)
	var connection apicontract.SourceConnection
	request(http.MethodPost, "/api/v1/source-connections/"+authorization.ConnectionID+"/device/poll", nil, http.StatusOK, &connection)
	if connection.ID == "" || connection.Status != "connected" {
		t.Fatal("controlled GitHub connection did not become available")
	}
	githubSource := apicontract.GitHubSource{ConnectionID: connection.ID, InstallationID: 7, RepositoryID: 17, Branch: "main"}
	var inspection apicontract.InspectResponse
	request(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: githubSource, Setup: &setup}, http.StatusOK, &inspection)
	if len(inspection.Analysis.Candidates) == 0 || inspection.Analysis.StructuralFingerprint == "" {
		t.Fatal("migration source inspection returned no candidate")
	}
	candidate := inspection.Analysis.Candidates[len(inspection.Analysis.Candidates)-1]
	request(http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{Name: "Migration Journey", GithubSource: githubSource, Setup: &setup}, http.StatusCreated, &application)
	if application.ID == "" || application.Source.Type != "github" {
		t.Fatal("controller did not save the migration GitHub source")
	}
	var plan apicontract.DeploymentPlanRevision
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		Setup: &setup, CandidateID: candidate.ID, ExpectedCandidateDigest: candidate.Digest,
		ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &plan)
	if plan.RevisionID == "" || plan.RevisionNumber != 1 || !plan.Migration.Present || plan.Migration.ApprovalStatus != "pending" ||
		plan.Migration.Command != "npm exec -- knex migrate:latest" || !migrationJourneyExactKeys(plan.Migration.EnvironmentKeys, []string{"DATABASE_URL"}) {
		t.Fatal("accepted plan did not preserve the inferred Knex migration and its one scoped secret key")
	}

	appNetwork, err = generatedruntime.DescribeAppNetwork(application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "network", "create", "--driver", "bridge",
		"--label", "io.rig.managed="+generatedruntime.NetworkOwnershipLabelValue,
		"--label", "io.rig.application="+application.ID, appNetwork.Name); err != nil {
		t.Fatal("create exactly owned migration app bridge")
	}
	gatewaysBody, err := controllerJourneyDocker(ctx, docker, nil, "network", "inspect", "--format", "{{json .IPAM.Config}}", appNetwork.Name)
	var gateways []struct{ Gateway string }
	if err != nil || json.Unmarshal(bytes.TrimSpace(gatewaysBody), &gateways) != nil || len(gateways) != 1 || net.ParseIP(gateways[0].Gateway) == nil {
		t.Fatal("inspect migration app bridge gateway")
	}
	gateway := gateways[0].Gateway
	if err := migrationJourneyIssuePostgresLeaf(ctx, openssl, fixtureRoot, gateway); err != nil {
		t.Fatal("issue database certificate for app bridge gateway:", err)
	}
	if err := migrationJourneyVerifyPostgresLeaf(ctx, openssl, fixtureRoot, gateway); err != nil {
		t.Fatal("verify database certificate identity for app bridge gateway:", err)
	}
	dbPort := controllerJourneyPort(t, gateway)
	httpsPort := controllerJourneyPort(t, gateway)
	for httpsPort == dbPort {
		httpsPort = controllerJourneyPort(t, gateway)
	}
	password := uuid.NewString()
	fixtureEnv := []string{
		"FIXTURE_NETWORK_NAME=" + migrationJourneyNetwork,
		"FIXTURE_HOST_GATEWAY_IP=" + gateway,
		"FIXTURE_POSTGRES_BIND_ADDRESS=" + gateway,
		"FIXTURE_HTTPS_BIND_ADDRESS=" + gateway,
		"FIXTURE_POSTGRES_HOST_PORT=" + strconv.Itoa(dbPort),
		"FIXTURE_HTTPS_HOST_PORT=" + strconv.Itoa(httpsPort),
		"FIXTURE_POSTGRES_DB=fixture_migration",
		"FIXTURE_POSTGRES_USER=fixture_user",
		"FIXTURE_POSTGRES_PASSWORD=" + password,
		"FIXTURE_HTTPS_TOKEN=" + uuid.NewString(),
	}
	fixtureStarted = true
	if _, err := controllerJourneyDocker(ctx, docker, fixtureEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", migrationJourneyProject, "up", "-d"); err != nil {
		t.Fatal("start disposable external PostgreSQL fixture")
	}
	if err := migrationJourneyWaitForPostgres(ctx, docker, fixtureEnv, fixtureRoot); err != nil {
		t.Fatal("wait for disposable external PostgreSQL fixture:", err)
	}
	databaseURL := fmt.Sprintf("postgresql://fixture_user:%s@%s:%d/fixture_migration", password, gateway, dbPort)
	var saved apicontract.ApplicationConfiguration
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: 0, PlanRevisionID: plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true,
		Entries:                           []apicontract.ScopedConfigurationValueInput{{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_URL", Value: databaseURL, Sensitive: true}},
		Remove:                            []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &saved)
	if saved.RevisionID == "" || saved.RevisionNumber != 1 {
		t.Fatal("controller did not persist migration scoped configuration")
	}

	deployRequest := apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID: saved.RevisionID, ExpectedConfigurationRevisionNumber: saved.RevisionNumber,
	}
	var mutation apicontract.JobMutationResponse
	request(http.MethodPost, "/api/v1/apps/"+application.ID+"/deployments", deployRequest, http.StatusAccepted, &mutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !mutation.Created || mutation.Job.ID == "" || mutation.Job.RequestedBy != user.ID {
		t.Fatal("controller did not enqueue the exact migration deployment")
	}
	job, err := jobStore.Get(mutation.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	job.Attempt = 1
	result, err := composition.executor.Execute(ctx, job, migrationJourneyReporter{})
	if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseMigrationApprovalRequired || migrationRunner.calls != 0 {
		t.Fatalf("unapproved execution result=%#v err=%v migrationCalls=%d", result, err, migrationRunner.calls)
	}
	migrationJourneyAssertTableAbsent(t, ctx, docker, fixtureEnv, fixtureRoot, "rig_migration_ledger")
	migrationJourneyAssertTableAbsent(t, ctx, docker, fixtureEnv, fixtureRoot, "rig_migration_counter")

	var approved apicontract.DeploymentPlanRevision
	request(http.MethodPost, "/api/v1/apps/"+application.ID+"/deployment-plan/migration-approval", apicontract.ApproveDeploymentPlanMigrationRequest{
		RevisionID: plan.RevisionID, RevisionNumber: plan.RevisionNumber, ExpectedApprovalRevision: 0,
	}, http.StatusOK, &approved)
	if approved.RevisionID != plan.RevisionID || approved.RevisionNumber != plan.RevisionNumber || approved.Migration.ApprovalStatus != "approved" {
		t.Fatal("controller did not approve the exact migration plan revision")
	}
	migrationRunner.afterSuccess = func() {
		migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 1, 1)
	}
	migrationRunner.interruptAfterSuccess = true
	migrationJourneyExecuteAndRecover(t, ctx, composition.executor, job)
	if migrationRunner.calls != 1 {
		t.Fatalf("approved migration runner calls=%d, want 1", migrationRunner.calls)
	}
	running, err := composition.state.Get(ctx, application.ID, mutation.Job.ID)
	if err != nil || running.Phase != generatedruntimestate.PhaseMigrating || running.MigrationState != generatedruntimestate.MigrationRunning || running.MigrationStartedAt.IsZero() || !running.MigrationFinishedAt.IsZero() {
		t.Fatalf("interrupted migration state=%#v err=%v", running, err)
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 1, 1)
	var history apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &history)
	if len(history.Items) != 1 || history.Items[0].ReleaseID == "" || history.Items[0].DeploymentPlanRevisionID != plan.RevisionID || history.Items[0].ActualConfigurationRevisionID != saved.RevisionID {
		t.Fatal("interrupted deployment did not retain its immutable release, plan, and configuration pins")
	}
	if provider.archiveReads.Load() == 0 {
		t.Fatal("approved migration did not materialize an immutable GitHub archive")
	}

	api.Close()
	if err := db.Close(); err != nil {
		t.Fatal("close durable controller state before migration recovery:", err)
	}
	db, err = database.Open(dataRoot)
	if err != nil {
		t.Fatal("reopen durable controller state:", err)
	}
	defer db.Close()
	reopenedConfiguration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopenedPlans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", time.Now)
	reopenedSnapshots, err := releasesnapshot.New(db, reopenedSources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	recoveryRunner := &migrationJourneyRunner{test: t}
	reopened, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: apps.New(db), snapshots: reopenedSnapshots, configuration: reopenedConfiguration,
		deployments: deployments.New(db), plans: reopenedPlans,
	}, runtimeCompositionOptions{
		dockerExecutable: docker,
		migrationRunnerFactory: func(delegate generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
			recoveryRunner.delegate = delegate
			return recoveryRunner
		},
	})
	if err != nil {
		t.Fatal("reopen production generated migration runtime:", err)
	}
	reopenedJob, err := jobs.New(db).Get(mutation.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	reopenedJob.Attempt = 2
	if _, err := reopened.executor.Execute(ctx, reopenedJob, migrationJourneyReporter{}); err == nil {
		t.Fatal("durable interrupted migration unexpectedly completed on retry")
	}
	if recoveryRunner.calls != 0 {
		t.Fatalf("durable interrupted migration replayed the real migration runner %d times", recoveryRunner.calls)
	}
	recovered, err := reopened.state.Get(ctx, application.ID, mutation.Job.ID)
	if err != nil || recovered.MigrationState != generatedruntimestate.MigrationRunning || recovered.DiagnosticCode != generatedruntimestate.DiagnosticDaemonRestarted {
		t.Fatalf("reopened migration state=%#v err=%v", recovered, err)
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 1, 1)
}

type migrationJourneyReporter struct{}

func (migrationJourneyReporter) Report(jobs.ProgressUpdate) error { return nil }

type migrationJourneyInterrupted struct{}

type migrationJourneyRunner struct {
	test                  *testing.T
	delegate              generatedruntime.MigrationRunner
	calls                 int
	afterSuccess          func()
	interruptAfterSuccess bool
}

func (runner *migrationJourneyRunner) Run(ctx context.Context, request generatedruntime.MigrationRequest) error {
	runner.test.Helper()
	if runner.delegate == nil {
		runner.test.Fatal("migration test wrapper has no production delegate")
	}
	runner.calls++
	if err := runner.delegate.Run(ctx, request); err != nil {
		return err
	}
	if runner.afterSuccess != nil {
		runner.afterSuccess()
	}
	if runner.interruptAfterSuccess {
		panic(migrationJourneyInterrupted{})
	}
	return nil
}

func migrationJourneyExecuteAndRecover(t *testing.T, ctx context.Context, executor jobs.Executor, job jobs.Job) {
	t.Helper()
	interrupted := false
	defer func() {
		recovered := recover()
		if _, ok := recovered.(migrationJourneyInterrupted); !ok {
			t.Fatalf("migration interruption recovered=%T", recovered)
		}
		interrupted = true
		if !interrupted {
			t.Fatal("migration interruption was not observed")
		}
	}()
	if result, err := executor.Execute(ctx, job, migrationJourneyReporter{}); err != nil || result.CompletionCode != "" {
		t.Fatalf("migration execution returned before the test-only interruption: result=%#v err=%v", result, err)
	}
}

func migrationJourneyRequest(t *testing.T, ctx context.Context, baseURL string, session auth.Session) func(string, string, any, int, any, ...map[string]string) []byte {
	t.Helper()
	client := &http.Client{Timeout: 8 * time.Second}
	return func(method, path string, input any, want int, output any, headers ...map[string]string) []byte {
		t.Helper()
		var body io.Reader
		if input != nil {
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet {
			req.Header.Set("X-CSRF-Token", session.CSRF)
		}
		for _, values := range headers {
			for key, value := range values {
				req.Header.Set(key, value)
			}
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		contents, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("controller %s %s returned %d, want %d; body code=%s", method, path, response.StatusCode, want, controllerJourneyProblemCode(contents))
		}
		if output != nil && json.Unmarshal(contents, output) != nil {
			t.Fatalf("decode controller %s %s", method, path)
		}
		return contents
	}
}

func migrationJourneyTrackedFixtureFiles(installed string) (map[string]struct{}, error) {
	repositoryRoot := filepath.Dir(filepath.Dir(installed))
	relative, err := filepath.Rel(repositoryRoot, installed)
	if err != nil || filepath.ToSlash(relative) != "examples/hosting-migration" {
		return nil, fmt.Errorf("migration fixture source path is unexpected")
	}
	if err := exec.Command("git", "-C", repositoryRoot, "diff", "--quiet", "HEAD", "--", "examples/hosting-migration").Run(); err != nil {
		return nil, fmt.Errorf("migration fixture tracked files differ from HEAD")
	}
	listed, err := exec.Command("git", "-C", repositoryRoot, "ls-files", "-z", "--", "examples/hosting-migration").Output()
	if err != nil {
		return nil, fmt.Errorf("could not enumerate committed migration fixture files")
	}
	allowed := make(map[string]struct{})
	for _, entry := range bytes.Split(listed, []byte{0}) {
		name := strings.TrimPrefix(string(entry), "examples/hosting-migration/")
		if name != "" && name != string(entry) {
			allowed[name] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("committed migration fixture has no files")
	}
	return allowed, nil
}

func migrationJourneyIssuePostgresLeaf(ctx context.Context, openssl, fixtureRoot, gateway string) error {
	certificates := filepath.Join(fixtureRoot, "certs")
	config := filepath.Join(certificates, "migration-openssl-postgres.cnf")
	base, err := os.ReadFile(filepath.Join(fixtureRoot, "openssl-postgres.cnf"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(config, append(base, []byte("IP.1 = "+gateway+"\n")...), 0o600); err != nil {
		return err
	}
	defer os.Remove(config)
	key := filepath.Join(certificates, "postgres.fixture.test.key")
	csr := filepath.Join(certificates, "postgres.fixture.test.csr")
	certificate := filepath.Join(certificates, "postgres.fixture.test.crt")
	defer os.Remove(csr)
	if err := exec.CommandContext(ctx, openssl, "req", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", csr, "-config", config).Run(); err != nil {
		return err
	}
	return exec.CommandContext(ctx, openssl, "x509", "-req", "-days", "2", "-in", csr,
		"-CA", filepath.Join(certificates, "test-ca.crt"), "-CAkey", filepath.Join(certificates, "test-ca.key"), "-CAcreateserial",
		"-out", certificate, "-extfile", config, "-extensions", "v3_req").Run()
}

func migrationJourneyVerifyPostgresLeaf(ctx context.Context, openssl, fixtureRoot, gateway string) error {
	certificates := filepath.Join(fixtureRoot, "certs")
	output, err := exec.CommandContext(ctx, openssl, "verify", "-CAfile", filepath.Join(certificates, "test-ca.crt"),
		"-verify_ip", gateway, filepath.Join(certificates, "postgres.fixture.test.crt")).Output()
	if err != nil || !bytes.Contains(output, []byte("OK")) {
		return fmt.Errorf("verified PostgreSQL leaf did not match the configured database host")
	}
	return nil
}

func migrationJourneyWaitForPostgres(ctx context.Context, docker string, environment []string, fixtureRoot string) error {
	deadline := time.Now().Add(45 * time.Second)
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := controllerJourneyDocker(attempt, docker, environment, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", migrationJourneyProject, "exec", "-T", "postgres", "pg_isready", "-U", "fixture_user", "-d", "fixture_migration")
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("PostgreSQL fixture did not become ready")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func migrationJourneyAssertTableAbsent(t *testing.T, ctx context.Context, docker string, environment []string, fixtureRoot, table string) {
	t.Helper()
	value := migrationJourneySQL(t, ctx, docker, environment, fixtureRoot, "SELECT to_regclass('public."+table+"') IS NULL")
	if value != "t" {
		t.Fatalf("unapproved migration created database relation %q", table)
	}
}

func migrationJourneyAssertCounter(t *testing.T, ctx context.Context, docker string, environment []string, fixtureRoot string, rows, value int) {
	t.Helper()
	ledger := migrationJourneySQL(t, ctx, docker, environment, fixtureRoot, "SELECT count(*) || '|' || COALESCE(min(run_id),0) || '|' || COALESCE(max(run_id),0) FROM rig_migration_ledger")
	wantLedger := strconv.Itoa(rows) + "|1|" + strconv.Itoa(rows)
	if ledger != wantLedger {
		t.Fatalf("migration ledger=%q want=%q", ledger, wantLedger)
	}
	counter := migrationJourneySQL(t, ctx, docker, environment, fixtureRoot, "SELECT count(*) || '|' || COALESCE(max(value),0) FROM rig_migration_counter")
	wantCounter := "1|" + strconv.Itoa(value)
	if counter != wantCounter {
		t.Fatalf("migration counter=%q want=%q", counter, wantCounter)
	}
	history := migrationJourneySQL(t, ctx, docker, environment, fixtureRoot, "SELECT count(*) FROM rig_migration_history")
	if history != "1" {
		t.Fatalf("Knex migration history rows=%q want=1", history)
	}
}

func migrationJourneySQL(t *testing.T, ctx context.Context, docker string, environment []string, fixtureRoot, statement string) string {
	t.Helper()
	output, err := controllerJourneyDocker(ctx, docker, environment, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", migrationJourneyProject, "exec", "-T", "postgres", "psql", "-U", "fixture_user", "-d", "fixture_migration", "-At", "-c", statement)
	if err != nil {
		t.Fatal("query disposable migration database")
	}
	return strings.TrimSpace(string(output))
}

func migrationJourneyExactKeys(actual, expected []string) bool {
	return len(actual) == len(expected) && len(actual) == 1 && actual[0] == expected[0]
}

func migrationJourneyCleanupIngress(t *testing.T, ctx context.Context, docker string) {
	t.Helper()
	for _, resource := range [][]string{
		{"container", "rig-generated-caddy-v1", "generated-ingress"},
		{"volume", "rig-generated-caddy-config-v1", "generated-ingress"},
		{"network", "rig-generated-caddy-ingress-v1", "generated-ingress-network"},
	} {
		if !controllerJourneyExists(t, ctx, docker, resource[0], resource[1]) {
			continue
		}
		format := "{{json .Labels}}"
		if resource[0] == "container" {
			format = "{{json .Config.Labels}}"
		}
		body, err := controllerJourneyDocker(ctx, docker, nil, resource[0], "inspect", "--format", format, resource[1])
		var labels map[string]string
		if err != nil || json.Unmarshal(bytes.TrimSpace(body), &labels) != nil || labels["io.rig.managed"] != resource[2] || labels["io.rig.identity-version"] != "v1" {
			t.Errorf("global ingress %s ownership uncertain; retaining", resource[0])
			continue
		}
		removal := []string{resource[0], "rm", resource[1]}
		if resource[0] != "network" {
			removal = []string{resource[0], "rm", "--force", resource[1]}
		}
		if _, err := controllerJourneyDocker(ctx, docker, nil, removal...); err != nil {
			t.Errorf("remove exact ingress %s", resource[0])
		}
	}
}
