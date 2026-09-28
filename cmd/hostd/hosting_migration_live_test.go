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
	"sync"
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
	migrationJourneyV2SHA   = "dddddddddddddddddddddddddddddddddddddddd"
	migrationJourneyV3SHA   = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
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
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Minute)
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
		{"container", "rig-generated-caddy-v1"},
		{"volume", "rig-generated-caddy-config-v1"},
		{"network", "rig-generated-caddy-ingress-v1"},
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
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", "label=rig.controller=generated-builder"},
		{"network", "ls", "-q", "--filter", "label=rig.controller=generated-builder"},
	} {
		if output, err := controllerJourneyDocker(ctx, docker, nil, args...); err != nil || len(bytes.TrimSpace(output)) != 0 {
			t.Fatal("disposable daemon already has a generated builder resource")
		}
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
	sourceV1 := filepath.Join(root, "source-v1")
	sourceV2 := filepath.Join(root, "source-v2")
	sourceV3 := filepath.Join(root, "source-v3")
	if err := controllerJourneyStageSource(installed, sourceV1, tracked); err != nil {
		t.Fatal("stage immutable v1 fixture source:", err)
	}
	if err := controllerJourneyStageSource(installed, sourceV2, tracked); err != nil {
		t.Fatal("stage immutable v2 migration fixture source:", err)
	}
	ca, err := os.ReadFile(filepath.Join(fixtureRoot, "certs", "test-ca.crt"))
	if err != nil {
		t.Fatal("read nonsecret database root certificate:", err)
	}
	for _, source := range []string{sourceV1, sourceV2} {
		if os.WriteFile(filepath.Join(source, "test-ca.crt"), ca, 0o600) != nil {
			t.Fatal("stage nonsecret database root certificate with immutable source")
		}
	}
	if err := migrationJourneyMakeV1Source(sourceV1); err != nil {
		t.Fatal("prepare immutable v1 fixture source:", err)
	}
	if bytes.Contains(ca, []byte("PRIVATE KEY")) {
		t.Fatal("migration source certificate unexpectedly contains private key material")
	}
	for _, source := range []string{sourceV1, sourceV2} {
		if _, err := os.Stat(filepath.Join(source, "test-ca.key")); !os.IsNotExist(err) {
			t.Fatal("migration source retained a disposable certificate private key")
		}
	}
	if err := migrationJourneyCopySource(sourceV2, sourceV3); err != nil {
		t.Fatal("stage immutable v3 migration fixture source:", err)
	}
	if err := migrationJourneyAddUncertainMigration(sourceV3); err != nil {
		t.Fatal("prepare immutable v3 fixture source:", err)
	}

	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider := controllerJourneyNewGitHubProvider(t, sourceV1)
	provider.AddRevision(t, migrationJourneyV2SHA, sourceV2)
	provider.AddRevision(t, migrationJourneyV3SHA, sourceV3)
	for _, revision := range provider.revisions {
		for name, contents := range revision.files {
			if strings.HasSuffix(name, ".key") || bytes.Contains(contents, []byte("PRIVATE KEY")) {
				t.Fatal("controller archive unexpectedly contains disposable certificate private key material")
			}
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
	migrationRunner := &migrationJourneyRunner{}
	settings := config.Defaults()
	settings.DataRoot = dataRoot
	settings.GeneratedRuntime = true
	composition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: appStore, snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans,
	}, runtimeCompositionOptions{
		dockerExecutable: docker,
		migrationRunnerFactory: func(delegate generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
			migrationRunner.SetDelegate(delegate)
			return migrationRunner
		},
	})
	if err != nil {
		t.Fatal("compose production generated migration runtime:", err)
	}

	fixtureStarted := false
	var fixtureEnv []string
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
			if _, downErr := controllerJourneyDocker(cleanup, docker, fixtureEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", migrationJourneyProject, "down", "--volumes", "--remove-orphans"); downErr != nil {
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
	setupBody, err := os.ReadFile(filepath.Join(sourceV1, "rig-setup.json"))
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
	var v1Inspection apicontract.InspectResponse
	request(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: githubSource, Setup: &setup}, http.StatusOK, &v1Inspection)
	if len(v1Inspection.Analysis.Candidates) == 0 || v1Inspection.Analysis.StructuralFingerprint == "" {
		t.Fatal("v1 source inspection returned no candidate")
	}
	v1Candidate := v1Inspection.Analysis.Candidates[len(v1Inspection.Analysis.Candidates)-1]
	request(http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{Name: "Migration Journey", GithubSource: githubSource, Setup: &setup}, http.StatusCreated, &application)
	if application.ID == "" || application.Source.Type != "github" {
		t.Fatal("controller did not save the migration GitHub source")
	}
	var v1Plan apicontract.DeploymentPlanRevision
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		Setup: &setup, CandidateID: v1Candidate.ID, ExpectedCandidateDigest: v1Candidate.Digest,
		ExpectedSourceStructuralFingerprint: v1Inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &v1Plan)
	if v1Plan.RevisionID == "" || v1Plan.RevisionNumber != 1 || v1Plan.Migration.Present || v1Plan.Source.ResolvedDigest != controllerJourneyGitHubSHA {
		t.Fatal("accepted v1 plan did not retain its no-migration immutable source")
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
	fixtureEnv = []string{
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
	var v1Configuration apicontract.ApplicationConfiguration
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: 0, PlanRevisionID: v1Plan.RevisionID, PlanRevisionNumber: v1Plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true,
		Entries:                           []apicontract.ScopedConfigurationValueInput{{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_URL", Value: databaseURL, Sensitive: true}},
		Remove:                            []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &v1Configuration)
	if v1Configuration.RevisionID == "" || v1Configuration.RevisionNumber != 1 || v1Configuration.DeploymentPlanRevisionID != v1Plan.RevisionID ||
		len(v1Configuration.Entries) != 1 || v1Configuration.Entries[0].Key != "DATABASE_URL" || !v1Configuration.Entries[0].Sensitive {
		t.Fatal("controller did not persist v1 DATABASE_URL as one scoped runtime secret")
	}

	workerContext, stopWorkerContext := context.WithCancel(ctx)
	workerDone, err := prepareRuntimeWorker(workerContext, runtimeRecovery{
		deployments: deploymentStore.Recover, jobs: jobStore.RecoverInterrupted,
	}, composition.executor, jobStore.RunWorker, func(workerErr error) { t.Errorf("migration journey worker: %v", workerErr) })
	if err != nil {
		t.Fatal("start migration journey worker:", err)
	}
	workerStopped := false
	stopWorker := func() {
		if workerStopped {
			return
		}
		workerStopped = true
		stopWorkerContext()
		select {
		case <-workerDone:
		case <-time.After(10 * time.Second):
			t.Error("migration journey worker did not stop")
		}
	}
	defer stopWorker()

	deploymentPath := "/api/v1/apps/" + application.ID + "/deployments"
	var v1Mutation apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: v1Plan.RevisionID, ExpectedPlanRevisionNumber: v1Plan.RevisionNumber,
		ExpectedConfigurationRevisionID: v1Configuration.RevisionID, ExpectedConfigurationRevisionNumber: v1Configuration.RevisionNumber,
	}, http.StatusAccepted, &v1Mutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !v1Mutation.Created || v1Mutation.Job.ID == "" || v1Mutation.Job.RequestedBy != user.ID {
		t.Fatal("controller did not enqueue v1 deployment")
	}
	migrationJourneyWaitForJob(t, ctx, jobStore, v1Mutation.Job.ID, jobs.Succeeded)
	var v1History apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &v1History)
	v1Deployment := migrationJourneyDeploymentForJob(t, v1History, v1Mutation.Job.ID)
	if v1Deployment.Status != "succeeded" || v1Deployment.ReleaseID == "" || v1Deployment.DeploymentPlanRevisionID != v1Plan.RevisionID || v1Deployment.ActualConfigurationRevisionID != v1Configuration.RevisionID {
		t.Fatal("initial v1 deployment did not retain immutable provenance")
	}
	var v1Releases apicontract.ReleaseList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &v1Releases)
	v1Release := migrationJourneyReleaseForID(t, v1Releases, v1Deployment.ReleaseID)
	if v1Release.ResolvedSha != controllerJourneyGitHubSHA || v1Release.DeploymentPlanRevisionID != v1Plan.RevisionID || v1Release.ConfigurationRevisionID != v1Configuration.RevisionID {
		t.Fatal("initial v1 release did not retain its immutable source, plan, and configuration")
	}
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":0`)

	if err := provider.SelectRevision(migrationJourneyV2SHA); err != nil {
		t.Fatal("select immutable v2 source:", err)
	}
	var v2Inspection apicontract.InspectResponse
	request(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: githubSource, Setup: &setup}, http.StatusOK, &v2Inspection)
	if len(v2Inspection.Analysis.Candidates) == 0 || v2Inspection.Analysis.StructuralFingerprint == v1Inspection.Analysis.StructuralFingerprint {
		t.Fatal("v2 migration source was not detected as a distinct immutable revision")
	}
	v2Candidate := v2Inspection.Analysis.Candidates[len(v2Inspection.Analysis.Candidates)-1]
	var v2Plan apicontract.DeploymentPlanRevision
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		ExpectedRevisionNumber: v1Plan.RevisionNumber, Setup: &setup, CandidateID: v2Candidate.ID, ExpectedCandidateDigest: v2Candidate.Digest,
		ExpectedSourceStructuralFingerprint: v2Inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &v2Plan)
	if v2Plan.RevisionNumber != 2 || v2Plan.Source.ResolvedDigest != migrationJourneyV2SHA || !v2Plan.Migration.Present || v2Plan.Migration.ApprovalStatus != "pending" ||
		v2Plan.Migration.Command != "npm exec -- knex migrate:latest" || !migrationJourneyExactKeys(v2Plan.Migration.EnvironmentKeys, []string{"DATABASE_URL"}) {
		t.Fatal("accepted v2 plan did not preserve the inferred Knex migration and one scoped secret key")
	}
	var v2Configuration apicontract.ApplicationConfiguration
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: v1Configuration.RevisionNumber, PlanRevisionID: v2Plan.RevisionID, PlanRevisionNumber: v2Plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true,
		Entries:                           []apicontract.ScopedConfigurationValueInput{{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_URL", Value: databaseURL, Sensitive: true}},
		Remove:                            []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &v2Configuration)
	if v2Configuration.RevisionNumber != 2 || v2Configuration.DeploymentPlanRevisionID != v2Plan.RevisionID || len(v2Configuration.Entries) != 1 || v2Configuration.Entries[0].Key != "DATABASE_URL" || !v2Configuration.Entries[0].Sensitive {
		t.Fatal("controller did not persist v2 DATABASE_URL as one scoped runtime secret")
	}

	var v2Mutation apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: v2Plan.RevisionID, ExpectedPlanRevisionNumber: v2Plan.RevisionNumber,
		ExpectedConfigurationRevisionID: v2Configuration.RevisionID, ExpectedConfigurationRevisionNumber: v2Configuration.RevisionNumber,
	}, http.StatusAccepted, &v2Mutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !v2Mutation.Created || v2Mutation.Job.ID == "" {
		t.Fatal("controller did not enqueue v2 migration deployment")
	}
	v2Paused := migrationJourneyWaitForJob(t, ctx, jobStore, v2Mutation.Job.ID, jobs.WaitingUser)
	if v2Paused.PauseDisposition != jobs.PauseMigrationApprovalRequired || migrationRunner.Calls() != 0 {
		t.Fatalf("v2 pending approval status=%s pause=%s migrationCalls=%d", v2Paused.Status, v2Paused.PauseDisposition, migrationRunner.Calls())
	}
	migrationJourneyAssertTableAbsent(t, ctx, docker, fixtureEnv, fixtureRoot, "rig_migration_ledger")
	migrationJourneyAssertTableAbsent(t, ctx, docker, fixtureEnv, fixtureRoot, "rig_migration_counter")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":0`)

	var v2Approved apicontract.DeploymentPlanRevision
	request(http.MethodPost, "/api/v1/apps/"+application.ID+"/deployment-plan/migration-approval", apicontract.ApproveDeploymentPlanMigrationRequest{
		RevisionID: v2Plan.RevisionID, RevisionNumber: v2Plan.RevisionNumber, ExpectedApprovalRevision: 0,
	}, http.StatusOK, &v2Approved)
	if v2Approved.RevisionID != v2Plan.RevisionID || v2Approved.RevisionNumber != v2Plan.RevisionNumber || v2Approved.Migration.ApprovalStatus != "approved" {
		t.Fatal("controller did not approve exact v2 migration plan revision")
	}
	var v2Resumed apicontract.JobResponse
	request(http.MethodPost, "/api/v1/jobs/"+v2Mutation.Job.ID+"/resume", nil, http.StatusOK, &v2Resumed)
	migrationJourneyWaitForJob(t, ctx, jobStore, v2Mutation.Job.ID, jobs.Succeeded)
	if migrationRunner.Calls() != 1 {
		t.Fatalf("approved v2 migration runner calls=%d, want 1", migrationRunner.Calls())
	}
	var v2History apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &v2History)
	v2Deployment := migrationJourneyDeploymentForJob(t, v2History, v2Mutation.Job.ID)
	if v2Deployment.Status != "succeeded" || v2Deployment.ReleaseID == v1Deployment.ReleaseID || v2Deployment.DeploymentPlanRevisionID != v2Plan.RevisionID || v2Deployment.ActualConfigurationRevisionID != v2Configuration.RevisionID {
		t.Fatal("approved v2 deployment did not retain new immutable source, plan, and configuration")
	}
	var v2Releases apicontract.ReleaseList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &v2Releases)
	v2Release := migrationJourneyReleaseForID(t, v2Releases, v2Deployment.ReleaseID)
	if v2Release.ResolvedSha != migrationJourneyV2SHA || v2Release.DeploymentPlanRevisionID != v2Plan.RevisionID || v2Release.ConfigurationRevisionID != v2Configuration.RevisionID {
		t.Fatal("approved v2 release did not retain immutable migration provenance")
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 1, 1)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":1`)

	var rollbackMutation apicontract.JobMutationResponse
	request(http.MethodPost, "/api/v1/apps/"+application.ID+"/releases/"+v1Release.ID+"/deployments", apicontract.DeployReleaseRequest{ConfigurationMode: "original"}, http.StatusAccepted, &rollbackMutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !rollbackMutation.Created || rollbackMutation.Job.ID == "" {
		t.Fatal("controller did not enqueue historical v1 deployment")
	}
	migrationJourneyWaitForJob(t, ctx, jobStore, rollbackMutation.Job.ID, jobs.Succeeded)
	if migrationRunner.Calls() != 1 {
		t.Fatal("prior v1 release unexpectedly ran a migration")
	}
	var rollbackHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &rollbackHistory)
	rollbackDeployment := migrationJourneyDeploymentForJob(t, rollbackHistory, rollbackMutation.Job.ID)
	if rollbackDeployment.Status != "succeeded" || rollbackDeployment.ReleaseID != v1Release.ID || rollbackDeployment.DeploymentPlanRevisionID != v1Plan.RevisionID || rollbackDeployment.ActualConfigurationRevisionID != v1Configuration.RevisionID {
		t.Fatal("historical v1 deployment did not use its original immutable release and configuration")
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 1, 1)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":1`)

	if err := provider.SelectRevision(migrationJourneyV3SHA); err != nil {
		t.Fatal("select immutable v3 source:", err)
	}
	var v3Inspection apicontract.InspectResponse
	request(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: githubSource, Setup: &setup}, http.StatusOK, &v3Inspection)
	if len(v3Inspection.Analysis.Candidates) == 0 || v3Inspection.Analysis.StructuralFingerprint == v2Inspection.Analysis.StructuralFingerprint {
		t.Fatal("v3 uncertainty source was not detected as a distinct immutable revision")
	}
	v3Candidate := v3Inspection.Analysis.Candidates[len(v3Inspection.Analysis.Candidates)-1]
	var v3Plan apicontract.DeploymentPlanRevision
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		ExpectedRevisionNumber: v2Plan.RevisionNumber, Setup: &setup, CandidateID: v3Candidate.ID, ExpectedCandidateDigest: v3Candidate.Digest,
		ExpectedSourceStructuralFingerprint: v3Inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &v3Plan)
	if v3Plan.RevisionNumber != 3 || v3Plan.Source.ResolvedDigest != migrationJourneyV3SHA || !v3Plan.Migration.Present || v3Plan.Migration.ApprovalStatus != "pending" ||
		v3Plan.Migration.Command != v2Plan.Migration.Command || !migrationJourneyExactKeys(v3Plan.Migration.EnvironmentKeys, []string{"DATABASE_URL"}) {
		t.Fatal("accepted v3 plan did not preserve the changed migration evidence and scoped secret key")
	}
	var v3Configuration apicontract.ApplicationConfiguration
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: v2Configuration.RevisionNumber, PlanRevisionID: v3Plan.RevisionID, PlanRevisionNumber: v3Plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true,
		Entries:                           []apicontract.ScopedConfigurationValueInput{{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_URL", Value: databaseURL, Sensitive: true}},
		Remove:                            []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &v3Configuration)
	if v3Configuration.RevisionNumber != 3 || v3Configuration.DeploymentPlanRevisionID != v3Plan.RevisionID || len(v3Configuration.Entries) != 1 || v3Configuration.Entries[0].Key != "DATABASE_URL" || !v3Configuration.Entries[0].Sensitive {
		t.Fatal("controller did not rebind v3 DATABASE_URL as one scoped runtime secret")
	}

	var v3Mutation apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: v3Plan.RevisionID, ExpectedPlanRevisionNumber: v3Plan.RevisionNumber,
		ExpectedConfigurationRevisionID: v3Configuration.RevisionID, ExpectedConfigurationRevisionNumber: v3Configuration.RevisionNumber,
	}, http.StatusAccepted, &v3Mutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !v3Mutation.Created || v3Mutation.Job.ID == "" {
		t.Fatal("controller did not enqueue v3 uncertainty deployment")
	}
	v3Paused := migrationJourneyWaitForJob(t, ctx, jobStore, v3Mutation.Job.ID, jobs.WaitingUser)
	if v3Paused.PauseDisposition != jobs.PauseMigrationApprovalRequired || migrationRunner.Calls() != 1 {
		t.Fatalf("v3 pending approval status=%s pause=%s migrationCalls=%d", v3Paused.Status, v3Paused.PauseDisposition, migrationRunner.Calls())
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 1, 1)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":1`)

	var v3Approved apicontract.DeploymentPlanRevision
	request(http.MethodPost, "/api/v1/apps/"+application.ID+"/deployment-plan/migration-approval", apicontract.ApproveDeploymentPlanMigrationRequest{
		RevisionID: v3Plan.RevisionID, RevisionNumber: v3Plan.RevisionNumber, ExpectedApprovalRevision: 0,
	}, http.StatusOK, &v3Approved)
	if v3Approved.RevisionID != v3Plan.RevisionID || v3Approved.RevisionNumber != v3Plan.RevisionNumber || v3Approved.Migration.ApprovalStatus != "approved" {
		t.Fatal("controller did not approve exact v3 migration plan revision")
	}
	workerExited := make(chan struct{})
	migrationRunner.SetAfterSuccess(func() error { return migrationJourneyCounterError(ctx, docker, fixtureEnv, fixtureRoot, 2, 2) })
	migrationRunner.SetExitWorkerAfterSuccess(workerExited)
	var v3Resumed apicontract.JobResponse
	request(http.MethodPost, "/api/v1/jobs/"+v3Mutation.Job.ID+"/resume", nil, http.StatusOK, &v3Resumed)
	if v3Resumed.Job.ID != v3Mutation.Job.ID || v3Resumed.Job.Status != string(jobs.Queued) {
		t.Fatalf("v3 approval resume did not requeue the accepted job: %#v", v3Resumed)
	}
	select {
	case <-workerExited:
	case <-time.After(9 * time.Minute):
		t.Fatal("test-only worker interruption did not follow the real migration")
	}
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted production worker did not stop")
	}
	workerStopped = true
	stopWorkerContext()
	if migrationRunner.Calls() != 2 {
		t.Fatalf("approved v3 migration runner calls=%d, want 2", migrationRunner.Calls())
	}
	v3Running, err := jobStore.Get(v3Mutation.Job.ID)
	if err != nil || v3Running.Status != string(jobs.Running) || v3Running.Phase == "" || v3Running.Attempt != 2 {
		t.Fatalf("interrupted v3 job did not retain a claimed running attempt: %#v err=%v", v3Running, err)
	}
	var interruptedHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &interruptedHistory)
	interruptedDeployment := migrationJourneyDeploymentForJob(t, interruptedHistory, v3Mutation.Job.ID)
	if interruptedDeployment.Status != string(deployments.Applying) || interruptedDeployment.ReleaseID == "" || interruptedDeployment.DeploymentPlanRevisionID != v3Plan.RevisionID || interruptedDeployment.ActualConfigurationRevisionID != v3Configuration.RevisionID || interruptedDeployment.ReleaseID == rollbackDeployment.ReleaseID {
		t.Fatal("interrupted v3 deployment did not retain immutable release, plan, and configuration pins")
	}
	deploymentID := interruptedDeployment.ID
	running, err := composition.state.Get(ctx, application.ID, deploymentID)
	if err != nil || running.Phase != generatedruntimestate.PhaseMigrating || running.MigrationState != generatedruntimestate.MigrationRunning || running.MigrationStartedAt.IsZero() || !running.MigrationFinishedAt.IsZero() {
		t.Fatalf("interrupted v3 migration state=%#v err=%v", running, err)
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 2, 2)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":2`)
	for _, sha := range []string{controllerJourneyGitHubSHA, migrationJourneyV2SHA, migrationJourneyV3SHA} {
		if provider.ArchiveReadsFor(sha) == 0 {
			t.Fatalf("immutable source revision %s was not materialized through its GitHub archive", sha)
		}
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
	reopenedJobs := jobs.New(db)
	reopenedDeployments := deployments.New(db)
	recoveryRunner := &migrationJourneyRunner{}
	reopened, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: apps.New(db), snapshots: reopenedSnapshots, configuration: reopenedConfiguration,
		deployments: reopenedDeployments, plans: reopenedPlans,
	}, runtimeCompositionOptions{
		dockerExecutable: docker,
		migrationRunnerFactory: func(delegate generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
			recoveryRunner.SetDelegate(delegate)
			return recoveryRunner
		},
	})
	if err != nil {
		t.Fatal("reopen production generated migration runtime:", err)
	}
	recoveryWorkerContext, stopRecoveryWorkerContext := context.WithCancel(ctx)
	recoveryWorkerDone, err := prepareRuntimeWorker(recoveryWorkerContext, runtimeRecovery{
		deployments: reopenedDeployments.Recover, jobs: reopenedJobs.RecoverInterrupted,
	}, reopened.executor, reopenedJobs.RunWorker, func(workerErr error) { t.Errorf("migration journey recovery worker: %v", workerErr) })
	if err != nil {
		t.Fatal("start recovered migration journey worker:", err)
	}
	defer func() {
		stopRecoveryWorkerContext()
		if !waitForWorker(recoveryWorkerDone, 10*time.Second) {
			t.Error("recovered migration journey worker did not stop")
		}
	}()
	recoveredJob, err := reopenedJobs.Get(v3Mutation.Job.ID)
	if err != nil || recoveredJob.Status != string(jobs.Interrupted) || recoveredJob.Phase != string(jobs.Interrupted) || recoveredJob.ErrorCode != string(generatedruntimestate.DiagnosticDaemonRestarted) || recoveredJob.Attempt != 2 {
		t.Fatalf("recovered v3 job=%#v err=%v", recoveredJob, err)
	}
	if recoveryRunner.Calls() != 0 {
		t.Fatalf("durable interrupted migration replayed the real migration runner %d times", recoveryRunner.Calls())
	}
	recovered, err := reopened.state.Get(ctx, application.ID, deploymentID)
	if err != nil || recovered.Phase != generatedruntimestate.PhaseFailed || recovered.MigrationState != generatedruntimestate.MigrationFailed || recovered.DiagnosticCode != generatedruntimestate.DiagnosticDaemonRestarted {
		t.Fatalf("reopened migration state=%#v err=%v", recovered, err)
	}
	recoveredDeployment, err := reopenedDeployments.Get(ctx, application.ID, deploymentID)
	if err != nil || recoveredDeployment.Status != deployments.Failed || recoveredDeployment.DiagnosticCode != string(generatedruntimestate.DiagnosticDaemonRestarted) || recoveredDeployment.ReleaseID != interruptedDeployment.ReleaseID {
		t.Fatalf("recovered v3 deployment=%#v err=%v", recoveredDeployment, err)
	}
	migrationJourneyAssertCounter(t, ctx, docker, fixtureEnv, fixtureRoot, 2, 2)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":2`)
	t.Logf("M2 migration uncertainty identities: app=%s v1-release=%s v1-plan=%s/%d v2-release=%s v2-plan=%s/%d v3-release=%s v3-plan=%s/%d rollback-deployment=%s interrupted-deployment=%s source-shas=%s,%s,%s archive-reads=%d/%d/%d", application.ID, v1Release.ID, v1Plan.RevisionID, v1Plan.RevisionNumber, v2Release.ID, v2Plan.RevisionID, v2Plan.RevisionNumber, interruptedDeployment.ReleaseID, v3Plan.RevisionID, v3Plan.RevisionNumber, rollbackDeployment.ID, deploymentID, controllerJourneyGitHubSHA, migrationJourneyV2SHA, migrationJourneyV3SHA, provider.ArchiveReadsFor(controllerJourneyGitHubSHA), provider.ArchiveReadsFor(migrationJourneyV2SHA), provider.ArchiveReadsFor(migrationJourneyV3SHA))
}

const migrationJourneyV1Server = `const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const tls = require("node:tls");
const { Pool } = require("pg");

const connectionString = process.env.DATABASE_URL;
if (!connectionString) {
  throw new Error("DATABASE_URL is required");
}

const hostname = new URL(connectionString).hostname;
const pool = new Pool({
  connectionString,
  connectionTimeoutMillis: 2_000,
  query_timeout: 2_000,
  max: 2,
  ssl: {
    rejectUnauthorized: true,
    ca: fs.readFileSync(path.join(__dirname, "..", "test-ca.crt"), "utf8"),
    checkServerIdentity: (_reportedHost, certificate) => tls.checkServerIdentity(hostname, certificate)
  }
});

async function counter() {
  const table = await pool.query("SELECT to_regclass('public.rig_migration_counter') AS table_name");
  if (table.rows.length !== 1 || table.rows[0].table_name === null) {
    return 0;
  }
  const result = await pool.query("SELECT value FROM rig_migration_counter WHERE counter_key='approved_migration'");
  const value = Number(result.rows[0]?.value);
  if (result.rows.length !== 1 || !Number.isInteger(value) || value < 1) {
    throw new Error("approved migration counter is unavailable");
  }
  return value;
}

const server = http.createServer(async (request, response) => {
  if (request.url !== "/readyz" && request.url !== "/counter") {
    response.writeHead(404, { "content-type": "application/json" });
    response.end('{"error":"not_found"}');
    return;
  }
  try {
    const value = await counter();
    response.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
    response.end(JSON.stringify(request.url === "/readyz" ? { status: "ready" } : { value }));
  } catch {
    response.writeHead(503, { "content-type": "application/json" });
    response.end('{"status":"not_ready"}');
  }
});

server.listen(Number.parseInt(process.env.PORT || "3000", 10), "0.0.0.0");

async function close() {
  server.close(() => void pool.end());
}

process.once("SIGINT", close);
process.once("SIGTERM", close);
`

const migrationJourneyUncertainMigration = `exports.up = async function up(knex) {
  await knex.schema.createTable("rig_migration_uncertainty", (table) => {
    table.bigIncrements("run_id").primary();
    table.text("migration_key").notNullable();
  });
  await knex("rig_migration_uncertainty").insert({ migration_key: "202609270002_uncertainty" });
  await knex("rig_migration_ledger").insert({ migration_key: "202609270002_uncertainty" });
  await knex("rig_migration_counter").where({ counter_key: "approved_migration" }).increment("value", 1);
};

exports.down = async function down() {
  throw new Error("application rollback must not execute a down migration");
};
`

func migrationJourneyMakeV1Source(root string) error {
	if err := os.Remove(filepath.Join(root, "knexfile.js")); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(root, "migrations")); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "src", "server.js"), []byte(migrationJourneyV1Server), 0o600)
}

func migrationJourneyCopySource(from, to string) error {
	return filepath.WalkDir(from, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(from, current)
		if err != nil {
			return err
		}
		destination := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("immutable migration source has a nonregular file")
		}
		contents, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, contents, 0o600)
	})
}

func migrationJourneyAddUncertainMigration(root string) error {
	return os.WriteFile(filepath.Join(root, "migrations", "202609270002_uncertainty.cjs"), []byte(migrationJourneyUncertainMigration), 0o600)
}

func migrationJourneyWaitForJob(t *testing.T, ctx context.Context, store *jobs.Service, id string, want jobs.Status) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(9 * time.Minute)
	for {
		job, err := store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == string(want) {
			return job
		}
		if job.Status != string(jobs.Queued) && job.Status != string(jobs.Assigned) && job.Status != string(jobs.Running) {
			t.Fatalf("deployment job %s status=%s phase=%s code=%s pause=%s, want %s", id, job.Status, job.Phase, job.ErrorCode, job.PauseDisposition, want)
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			t.Fatalf("deployment job %s timed out status=%s phase=%s code=%s pause=%s, want %s", id, job.Status, job.Phase, job.ErrorCode, job.PauseDisposition, want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func migrationJourneyDeploymentForJob(t *testing.T, history apicontract.DeploymentList, jobID string) apicontract.Deployment {
	t.Helper()
	for _, deployment := range history.Items {
		if deployment.JobID == jobID {
			return deployment
		}
	}
	t.Fatalf("deployment history has no job %s", jobID)
	return apicontract.Deployment{}
}

func migrationJourneyReleaseForID(t *testing.T, releases apicontract.ReleaseList, id string) apicontract.Release {
	t.Helper()
	for _, release := range releases.Items {
		if release.ID == id {
			return release
		}
	}
	t.Fatalf("release history has no release %s", id)
	return apicontract.Release{}
}

type migrationJourneyRunner struct {
	mu                     sync.Mutex
	delegate               generatedruntime.MigrationRunner
	calls                  int
	afterSuccess           func() error
	exitWorkerAfterSuccess bool
	workerExited           chan<- struct{}
}

func (runner *migrationJourneyRunner) Run(ctx context.Context, request generatedruntime.MigrationRequest) error {
	runner.mu.Lock()
	delegate := runner.delegate
	afterSuccess := runner.afterSuccess
	exitWorkerAfterSuccess := runner.exitWorkerAfterSuccess
	workerExited := runner.workerExited
	runner.calls++
	runner.mu.Unlock()
	if delegate == nil {
		return fmt.Errorf("migration test wrapper has no production delegate")
	}
	if err := delegate.Run(ctx, request); err != nil {
		return err
	}
	if afterSuccess != nil {
		if err := afterSuccess(); err != nil {
			return err
		}
	}
	if exitWorkerAfterSuccess {
		if workerExited == nil {
			return fmt.Errorf("migration test wrapper has no worker interruption signal")
		}
		close(workerExited)
		runtime.Goexit()
	}
	return nil
}

func (runner *migrationJourneyRunner) SetDelegate(delegate generatedruntime.MigrationRunner) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.delegate = delegate
}

func (runner *migrationJourneyRunner) SetAfterSuccess(callback func() error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.afterSuccess = callback
}

// SetExitWorkerAfterSuccess makes the test-only runner end its worker
// goroutine after the real migration delegate has returned. It deliberately
// bypasses executor completion so recovery sees the durable running state.
func (runner *migrationJourneyRunner) SetExitWorkerAfterSuccess(exited chan<- struct{}) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.exitWorkerAfterSuccess = true
	runner.workerExited = exited
}

func (runner *migrationJourneyRunner) Calls() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.calls
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
	if err := migrationJourneyCounterError(ctx, docker, environment, fixtureRoot, rows, value); err != nil {
		t.Fatal(err)
	}
}

func migrationJourneyCounterError(ctx context.Context, docker string, environment []string, fixtureRoot string, rows, value int) error {
	ledger, err := migrationJourneySQLResult(ctx, docker, environment, fixtureRoot, "SELECT count(*) || '|' || COALESCE(min(run_id),0) || '|' || COALESCE(max(run_id),0) FROM rig_migration_ledger")
	if err != nil {
		return err
	}
	wantLedger := strconv.Itoa(rows) + "|1|" + strconv.Itoa(rows)
	if ledger != wantLedger {
		return fmt.Errorf("migration ledger=%q want=%q", ledger, wantLedger)
	}
	counter, err := migrationJourneySQLResult(ctx, docker, environment, fixtureRoot, "SELECT count(*) || '|' || COALESCE(max(value),0) FROM rig_migration_counter")
	if err != nil {
		return err
	}
	wantCounter := "1|" + strconv.Itoa(value)
	if counter != wantCounter {
		return fmt.Errorf("migration counter=%q want=%q", counter, wantCounter)
	}
	history, err := migrationJourneySQLResult(ctx, docker, environment, fixtureRoot, "SELECT count(*) FROM rig_migration_history")
	if err != nil {
		return err
	}
	if history != strconv.Itoa(rows) {
		return fmt.Errorf("Knex migration history rows=%q want=%d", history, rows)
	}
	if rows > 1 {
		uncertainty, err := migrationJourneySQLResult(ctx, docker, environment, fixtureRoot, "SELECT count(*) || '|' || COALESCE(max(migration_key),'') FROM rig_migration_uncertainty")
		if err != nil {
			return err
		}
		if uncertainty != "1|202609270002_uncertainty" {
			return fmt.Errorf("additive uncertainty schema=%q", uncertainty)
		}
	}
	return nil
}

func migrationJourneySQL(t *testing.T, ctx context.Context, docker string, environment []string, fixtureRoot, statement string) string {
	t.Helper()
	output, err := migrationJourneySQLResult(ctx, docker, environment, fixtureRoot, statement)
	if err != nil {
		t.Fatal("query disposable migration database")
	}
	return output
}

func migrationJourneySQLResult(ctx context.Context, docker string, environment []string, fixtureRoot, statement string) (string, error) {
	output, err := controllerJourneyDocker(ctx, docker, environment, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", migrationJourneyProject, "exec", "-T", "postgres", "psql", "-U", "fixture_user", "-d", "fixture_migration", "-At", "-c", statement)
	if err != nil {
		return "", fmt.Errorf("query disposable migration database")
	}
	return strings.TrimSpace(string(output)), nil
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
