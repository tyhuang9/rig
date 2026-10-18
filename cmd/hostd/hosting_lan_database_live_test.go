//go:build live_docker

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const (
	lanDatabaseFixtureProject      = "rig-lan-database-test"
	lanDatabaseFixtureNetwork      = "rig-lan-database-external"
	lanDatabaseFixtureCertVolume   = lanDatabaseFixtureProject + "_fixture-certs"
	lanDatabaseFixturePostgresData = lanDatabaseFixtureProject + "_fixture-postgres-data"
)

// TestLiveControllerLANDatabaseJourney supplements the two-app in-memory LAN
// transport journey with a single normal Notes API backed by the reviewed TLS
// PostgreSQL fixture. It requires a disposable Linux Docker daemon.
func TestLiveControllerLANDatabaseJourney(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_LAN_DATABASE") != "1" {
		t.Fatal("set RIG_RUN_LIVE_LAN_DATABASE=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("LAN database gate requires the default local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	node := controllerJourneyExecutable(t, "node")
	sh := controllerJourneyExecutable(t, "sh")
	openssl := controllerJourneyExecutable(t, "openssl")
	ctx, cancel := context.WithTimeout(context.Background(), 57*time.Minute)
	defer cancel()
	selected, ports := lanTwoAppHostPorts(t)
	lanTwoAppDockerPreflight(t, ctx, docker)
	lanDatabaseDockerPreflight(t, ctx, docker)

	root := t.TempDir()
	dataRoot := filepath.Join(root, "controller")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	appIDs := make([]string, 0, 1)
	upgradeID := uuid.NewString()
	lanTwoAppRegisterCleanup(t, docker, dataRoot, upgradeID, &appIDs)

	installedSource, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(installedSource, "api", "node_modules", "pg")); err != nil || !info.IsDir() {
		t.Fatal("install the hosting fixture with its frozen lockfile first")
	}
	tracked, err := controllerJourneyTrackedFixtureFiles(installedSource)
	if err != nil {
		t.Fatal("verify committed GitHub fixture source:", err)
	}
	source := filepath.Join(root, "source")
	if err := controllerJourneyStageSource(installedSource, source, tracked); err != nil {
		t.Fatal("stage immutable fixture source:", err)
	}

	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	dbClosed := false
	defer func() {
		if !dbClosed {
			_ = db.Close()
		}
	}()
	authService := auth.New(db)
	bootstrap, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := authService.Bootstrap(bootstrap, "lan-database-admin", "a sufficiently long disposable passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	appStore := apps.New(db)
	jobStore, _, err := newDeploymentJobService(db)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider := controllerJourneyNewGitHubProvider(t, source)
	sourceClock := time.Now().UTC()
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", func() time.Time { return sourceClock })
	snapshots, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentStore := deployments.New(db)
	settings := config.Defaults()
	settings.DataRoot, settings.GeneratedRuntime = dataRoot, true
	composition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: appStore, snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans,
	}, runtimeCompositionOptions{dockerExecutable: docker, runner: runtimeprocess.ExecRunner{}})
	if err != nil {
		t.Fatal("compose production runtime:", err)
	}
	access := appaccess.New(db)
	grantTrace := &lanTwoAppGrantTrace{Manager: composition.ingress}
	composition.ingress.TraceGatewayV2LANGrantForLiveDocker(grantTrace.recordDriver)
	t.Cleanup(func() { grantTrace.logFailure(t) })
	api := httptest.NewServer((&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobStore, Machines: machineStore, Sources: sources,
		Configuration: configuration, Deployments: deploymentStore, DeploymentPlans: plans,
		GeneratedIngress: composition.ingress, GeneratedRuntimeState: composition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot,
		GatewayProfiles: access, GatewayUpgrades: access, GatewayUpgradeRuntime: composition.ingress,
		AppAccess: access, AppGrants: access, LANGrantRuntime: grantTrace,
		AppDisables: access, LANDisableRuntime: composition.ingress,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer api.Close()
	request := lanTwoAppAPI{t: t, ctx: ctx, base: api.URL, session: session.Token, csrf: session.CSRF,
		client: &http.Client{Timeout: 5 * time.Minute}}

	var setup apicontract.DeploymentSetupInput
	setupBytes, err := os.ReadFile(filepath.Join(source, "rig-setup.json"))
	if err != nil || json.Unmarshal(setupBytes, &setup) != nil {
		t.Fatal("read reviewed generated build recipe")
	}
	var device apicontract.GitHubDeviceAuthorization
	request.do(http.MethodPost, "/api/v1/source-connections/github/device", nil, http.StatusCreated, &device)
	sourceClock = sourceClock.Add(2 * time.Second)
	var connection apicontract.SourceConnection
	request.do(http.MethodPost, "/api/v1/source-connections/"+device.ConnectionID+"/device/poll", nil, http.StatusOK, &connection)
	if connection.Status != "connected" || connection.ProviderUserID != "4242" {
		t.Fatal("controlled source connection did not become authorized")
	}
	githubSource := apicontract.GitHubSource{ConnectionID: connection.ID, InstallationID: 7, RepositoryID: 17, Branch: "main"}

	application := lanTwoAppCreateApplication(t, &request, githubSource, setup, "LAN Database", &appIDs)
	network, err := generatedruntime.DescribeAppNetwork(application.id)
	if err != nil {
		t.Fatal(err)
	}
	if controllerJourneyExists(t, ctx, docker, "network", network.Name) {
		t.Fatal("app-private network already exists")
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "network", "create", "--driver", "bridge",
		"--label", "io.rig.managed="+generatedruntime.NetworkOwnershipLabelValue,
		"--label", "io.rig.application="+application.id, network.Name); err != nil {
		t.Fatal("create exactly owned application bridge")
	}
	appNetworkID := lanDatabaseAppNetworkID(t, ctx, docker, network.Name, application.id)
	gatewayOutput, err := controllerJourneyDocker(ctx, docker, nil, "network", "inspect", "--format", "{{json .IPAM.Config}}", network.Name)
	var gateways []struct{ Gateway string }
	if err != nil || json.Unmarshal(bytes.TrimSpace(gatewayOutput), &gateways) != nil || len(gateways) != 1 || net.ParseIP(gateways[0].Gateway) == nil {
		t.Fatal("inspect app bridge gateway")
	}
	gateway := gateways[0].Gateway

	fixtureRoot := filepath.Join(root, "external")
	if err := os.Mkdir(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-compose.yml", "generate-test-ca.sh", "openssl-postgres.cnf", "openssl-https.cnf", "https-stub.mjs"} {
		contents, err := os.ReadFile(filepath.Join(source, "harness", name))
		if err != nil || os.WriteFile(filepath.Join(fixtureRoot, name), contents, 0o600) != nil {
			t.Fatal("copy reviewed external fixture")
		}
	}
	dbPort := controllerJourneyPort(t, gateway, int(ports[0]), int(ports[1]), int(ports[2]))
	httpsPort := controllerJourneyPort(t, gateway, dbPort, int(ports[0]), int(ports[1]), int(ports[2]))
	command := exec.CommandContext(ctx, sh, filepath.Join(fixtureRoot, "generate-test-ca.sh"))
	command.Dir = fixtureRoot
	command.Env = append(os.Environ(), "FIXTURE_HOST_GATEWAY_IP="+gateway, "PATH="+filepath.Dir(openssl)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := command.Run(); err != nil {
		t.Fatal("generate external fixture CA")
	}
	ca, err := os.ReadFile(filepath.Join(fixtureRoot, "certs", "test-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	password, httpsToken := uuid.NewString(), uuid.NewString()
	fixture := &lanDatabaseFixture{
		root: fixtureRoot,
		env: []string{
			"FIXTURE_NETWORK_NAME=" + lanDatabaseFixtureNetwork,
			"FIXTURE_HOST_GATEWAY_IP=" + gateway,
			"FIXTURE_POSTGRES_BIND_ADDRESS=" + gateway,
			"FIXTURE_HTTPS_BIND_ADDRESS=" + gateway,
			"FIXTURE_POSTGRES_HOST_PORT=" + strconv.Itoa(dbPort),
			"FIXTURE_HTTPS_HOST_PORT=" + strconv.Itoa(httpsPort),
			"FIXTURE_POSTGRES_DB=fixture_notes", "FIXTURE_POSTGRES_USER=fixture_user",
			"FIXTURE_POSTGRES_PASSWORD=" + password, "FIXTURE_HTTPS_TOKEN=" + httpsToken,
		},
	}
	lanDatabaseRegisterFixtureCleanup(t, docker, fixture)
	fixture.started = true
	if _, err := controllerJourneyDocker(ctx, docker, fixture.env, "compose", "-f", fixture.root+string(filepath.Separator)+"docker-compose.yml", "-p", lanDatabaseFixtureProject, "up", "-d"); err != nil {
		t.Fatal("start disposable external TLS services")
	}
	dbURL := fmt.Sprintf("postgresql://fixture_user:%s@%s:%d/fixture_notes?sslmode=verify-full", password, gateway, dbPort)
	caBase64 := base64.StdEncoding.EncodeToString(ca)
	controllerJourneyPrepareSchema(t, ctx, node, installedSource, dbURL, caBase64)
	fixtureIdentity := lanDatabaseReadFixtureIdentity(t, ctx, docker, fixture)

	sentinel := "rig-m3-lan-database-secret-" + uuid.NewString()
	entries := lanDatabaseConfiguration("lan-database-v1", dbURL, caBase64, sentinel)
	var saved apicontract.ApplicationConfiguration
	savedResponse := request.do(http.MethodPut, "/api/v1/apps/"+application.id+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: 0, PlanRevisionID: application.plan.RevisionID, PlanRevisionNumber: application.plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: entries, Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &saved)
	if saved.RevisionNumber != 1 || saved.RevisionID == "" {
		t.Fatal("controller did not persist the initial scoped configuration")
	}
	for _, secret := range []string{dbURL, password, caBase64, sentinel} {
		if bytes.Contains(savedResponse, []byte(secret)) {
			t.Fatal("controller echoed a scoped server secret")
		}
	}
	application.config = saved

	workerContext, stopWorker := context.WithCancel(ctx)
	done, err := prepareRuntimeWorker(workerContext, runtimeRecovery{
		recoverState: jobStore.RecoverRuntimeContext,
	}, composition.executor, jobStore.RunWorker, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	workerStopped := false
	stopInitialWorker := func() bool {
		if workerStopped {
			return true
		}
		stopWorker()
		select {
		case <-done:
			workerStopped = true
			return true
		case <-time.After(10 * time.Second):
			return false
		}
	}
	defer func() {
		if !stopInitialWorker() {
			t.Error("initial deployment worker did not stop")
		}
	}()
	application.deploymentID = lanTwoAppDeployJob(t, &request, jobStore, deploymentStore, composition.state, application.id, application.plan, saved)
	var initialHistory apicontract.DeploymentList
	request.do(http.MethodGet, "/api/v1/apps/"+application.id+"/deployments", nil, http.StatusOK, &initialHistory)
	if len(initialHistory.Items) != 1 || initialHistory.Items[0].ID != application.deploymentID || initialHistory.Items[0].ReleaseID == "" {
		t.Fatal("initial deployment did not retain an immutable release")
	}

	profileQuery := fmt.Sprintf("?interfaceId=%s&selectedIpv4=%s&portStart=%d&portEnd=%d",
		url.QueryEscape(selected.InterfaceID), url.QueryEscape(selected.IPv4), ports[0], ports[2])
	var profileRead apicontract.LANGatewayProfileRead
	request.do(http.MethodGet, "/api/v1/system/lan-gateway-profile"+profileQuery, nil, http.StatusOK, &profileRead)
	if profileRead.Proposal == nil || profileRead.Proposal.ApprovalDigest == "" || profileRead.ExpectedRevisionNumber != 0 {
		t.Fatal("controller returned no exact LAN profile proposal")
	}
	var profile apicontract.LANGatewayProfileMutation
	request.do(http.MethodPost, "/api/v1/system/lan-gateway-profile", apicontract.ConfigureLANGatewayProfileRequest{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, Spec: profileRead.Proposal.Spec,
		ApprovalDigest: profileRead.Proposal.ApprovalDigest,
	}, http.StatusCreated, &profile)
	var upgradeRead apicontract.LANGatewayUpgradeRead
	request.do(http.MethodGet, "/api/v1/system/lan-gateway-upgrade", nil, http.StatusOK, &upgradeRead)
	if upgradeRead.Proposal == nil || upgradeRead.Proposal.ProfileRevisionID != profile.Profile.ID {
		t.Fatal("controller returned no exact gateway upgrade proposal")
	}
	var upgraded apicontract.LANGatewayUpgradeMutation
	request.do(http.MethodPost, "/api/v1/system/lan-gateway-upgrade", apicontract.UpgradeLANGatewayRequest{
		OperationID: upgradeID, ProfileRevisionID: profile.Profile.ID,
		ProfileRevisionNumber: profile.Profile.RevisionNumber, ActionDigest: upgradeRead.Proposal.ActionDigest,
	}, http.StatusCreated, &upgraded)
	if upgraded.Claim.State != "committed" {
		t.Fatalf("gateway upgrade state=%s", upgraded.Claim.State)
	}
	request.do(http.MethodGet, "/api/v1/system/lan-gateway-upgrade", nil, http.StatusOK, &upgradeRead)
	if upgradeRead.Observed.Availability != "serving" {
		t.Fatalf("gateway upgrade observation=%s", upgradeRead.Observed.Availability)
	}
	lanTwoAppAssertBindings(t, ctx, docker, upgradeID, selected.IPv4, ports)
	port := lanTwoAppGrant(t, &request, access, profile.Profile, application.id, user.ID)
	if port != ports[0] {
		t.Fatalf("unexpected LAN assignment=%d want=%d", port, ports[0])
	}
	routeURL := lanDatabaseAttestedURL(t, &request, application.id, selected.IPv4, port)
	lanDatabaseAssertScopedContainers(t, ctx, docker, application.id, network.Name, gateway, dbURL, caBase64, sentinel)
	lanDatabaseAssertFrontendSecretsAbsent(t, ctx, routeURL, "lan-database-v1", dbURL, caBase64, sentinel)
	beforeContainers := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-q", "--no-trunc", "--filter", "label=io.rig.application="+application.id)
	lanDatabaseAssertContainerSet(t, beforeContainers)
	lanDatabaseAssertFixtureRetained(t, ctx, docker, fixture, fixtureIdentity)
	if lanDatabaseAppNetworkID(t, ctx, docker, network.Name, application.id) != appNetworkID {
		t.Fatal("application private network identity changed before redeploy")
	}
	note := lanDatabaseCreateNote(t, ctx, routeURL, "lan database note "+uuid.NewString())
	lanDatabaseAssertNote(t, ctx, routeURL, note)

	replacementEntries := lanDatabaseConfiguration("lan-database-v2", dbURL, caBase64, sentinel)
	var replacementConfiguration apicontract.ApplicationConfiguration
	replacementResponse := request.do(http.MethodPut, "/api/v1/apps/"+application.id+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: saved.RevisionNumber, PlanRevisionID: application.plan.RevisionID, PlanRevisionNumber: application.plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: replacementEntries, Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &replacementConfiguration)
	if replacementConfiguration.RevisionNumber != saved.RevisionNumber+1 || replacementConfiguration.RevisionID == saved.RevisionID {
		t.Fatal("redeploy did not persist a distinct configuration revision")
	}
	for _, secret := range []string{dbURL, password, caBase64, sentinel} {
		if bytes.Contains(replacementResponse, []byte(secret)) {
			t.Fatal("controller echoed a replacement scoped server secret")
		}
	}
	replacementDeploymentID := lanTwoAppDeployJob(t, &request, jobStore, deploymentStore, composition.state, application.id, application.plan, replacementConfiguration)
	if replacementDeploymentID == application.deploymentID {
		t.Fatal("same application redeploy did not create a distinct deployment")
	}
	var replacementHistory apicontract.DeploymentList
	request.do(http.MethodGet, "/api/v1/apps/"+application.id+"/deployments", nil, http.StatusOK, &replacementHistory)
	if len(replacementHistory.Items) != 2 || replacementHistory.Items[0].ID != replacementDeploymentID ||
		replacementHistory.Items[0].ReleaseID == "" || replacementHistory.Items[0].ReleaseID == initialHistory.Items[0].ReleaseID ||
		replacementHistory.Items[0].ActualConfigurationRevisionID != replacementConfiguration.RevisionID {
		t.Fatal("same application redeploy did not retain a distinct immutable release")
	}
	afterContainers := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-q", "--no-trunc", "--filter", "label=io.rig.application="+application.id)
	lanDatabaseAssertContainersReplaced(t, beforeContainers, afterContainers)
	lanDatabaseAssertFixtureRetained(t, ctx, docker, fixture, fixtureIdentity)
	if lanDatabaseAppNetworkID(t, ctx, docker, network.Name, application.id) != appNetworkID {
		t.Fatal("application private network identity changed during redeploy")
	}
	redeployedURL := lanDatabaseAttestedURL(t, &request, application.id, selected.IPv4, port)
	if redeployedURL != routeURL {
		t.Fatal("same application redeploy changed the committed LAN URL")
	}
	lanDatabaseAssertScopedContainers(t, ctx, docker, application.id, network.Name, gateway, dbURL, caBase64, sentinel)
	lanDatabaseAssertFrontendSecretsAbsent(t, ctx, redeployedURL, "lan-database-v2", dbURL, caBase64, sentinel)
	lanDatabaseAssertNote(t, ctx, redeployedURL, note)

	if !stopInitialWorker() {
		t.Fatal("initial deployment worker did not stop before controller reopen")
	}
	api.Close()
	if err := db.Close(); err != nil {
		t.Fatal("close controller database for reopen")
	}
	dbClosed = true

	reopenedDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal("reopen controller database:", err)
	}
	defer reopenedDB.Close()
	reopenedApps := apps.New(reopenedDB)
	reopenedJobs, _, err := newDeploymentJobService(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	reopenedConfiguration, err := appconfig.New(reopenedDB, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopenedPlans, err := deploymentplans.New(reopenedDB, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSources := sourceconnections.NewService(sourceconnections.NewRepository(reopenedDB), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", time.Now)
	reopenedSnapshots, err := releasesnapshot.New(reopenedDB, reopenedSources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopenedDeployments := deployments.New(reopenedDB)
	reopenedComposition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: reopenedDB, applications: reopenedApps, snapshots: reopenedSnapshots, configuration: reopenedConfiguration,
		deployments: reopenedDeployments, plans: reopenedPlans,
	}, runtimeCompositionOptions{dockerExecutable: docker, runner: runtimeprocess.ExecRunner{}})
	if err != nil {
		t.Fatal("reopen production runtime:", err)
	}
	reopenedAccess := appaccess.New(reopenedDB)
	reopenedGrantTrace := &lanTwoAppGrantTrace{Manager: reopenedComposition.ingress}
	reopenedComposition.ingress.TraceGatewayV2LANGrantForLiveDocker(reopenedGrantTrace.recordDriver)
	t.Cleanup(func() { reopenedGrantTrace.logFailure(t) })
	reopenedAPI := httptest.NewServer((&controller.Server{
		Auth: auth.New(reopenedDB), Apps: reopenedApps, Jobs: reopenedJobs, Machines: machines.New(reopenedDB), Sources: reopenedSources,
		Configuration: reopenedConfiguration, Deployments: reopenedDeployments, DeploymentPlans: reopenedPlans,
		GeneratedIngress: reopenedComposition.ingress, GeneratedRuntimeState: reopenedComposition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot,
		GatewayProfiles: reopenedAccess, GatewayUpgrades: reopenedAccess, GatewayUpgradeRuntime: reopenedComposition.ingress,
		AppAccess: reopenedAccess, AppGrants: reopenedAccess, LANGrantRuntime: reopenedGrantTrace,
		AppDisables: reopenedAccess, LANDisableRuntime: reopenedComposition.ingress,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer reopenedAPI.Close()
	reopenedWorkerContext, stopReopenedWorker := context.WithCancel(ctx)
	reopenedDone, err := prepareRuntimeWorker(reopenedWorkerContext, runtimeRecovery{
		recoverState: reopenedJobs.RecoverRuntimeContext,
	}, reopenedComposition.executor, reopenedJobs.RunWorker, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopReopenedWorker()
		select {
		case <-reopenedDone:
		case <-time.After(10 * time.Second):
			t.Error("reopened deployment worker did not stop")
		}
	}()
	request.base = reopenedAPI.URL
	reopenedURL := lanDatabaseAttestedURL(t, &request, application.id, selected.IPv4, port)
	if reopenedURL != routeURL {
		t.Fatal("fresh controller reopen did not recover the committed LAN URL")
	}
	lanDatabaseAssertFixtureRetained(t, ctx, docker, fixture, fixtureIdentity)
	if lanDatabaseAppNetworkID(t, ctx, docker, network.Name, application.id) != appNetworkID {
		t.Fatal("application private network identity changed after controller reopen")
	}
	lanDatabaseAssertScopedContainers(t, ctx, docker, application.id, network.Name, gateway, dbURL, caBase64, sentinel)
	lanDatabaseAssertFrontendSecretsAbsent(t, ctx, reopenedURL, "lan-database-v2", dbURL, caBase64, sentinel)
	lanDatabaseAssertNote(t, ctx, reopenedURL, note)
	t.Logf("M3 LAN database journey: app=%s initial-deployment=%s replacement-deployment=%s database-container=%s database-volume=%s LAN=%s note-id=%s", application.id, application.deploymentID, replacementDeploymentID, fixtureIdentity.postgresContainerID, fixtureIdentity.postgresVolumeName, reopenedURL, note.ID)
}

type lanDatabaseFixture struct {
	root    string
	env     []string
	started bool
}

type lanDatabaseFixtureIdentity struct {
	postgresContainerID string
	postgresVolumeName  string
}

func lanDatabaseDockerPreflight(t *testing.T, ctx context.Context, docker string) {
	t.Helper()
	for _, resource := range []struct{ kind, name string }{
		{"network", lanDatabaseFixtureNetwork},
		{"volume", lanDatabaseFixtureCertVolume},
		{"volume", lanDatabaseFixturePostgresData},
	} {
		if controllerJourneyExists(t, ctx, docker, resource.kind, resource.name) {
			t.Fatalf("disposable daemon already has %s %s", resource.kind, resource.name)
		}
	}
	containers, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-aq", "--filter", "label=com.docker.compose.project="+lanDatabaseFixtureProject)
	if err != nil || len(bytes.TrimSpace(containers)) != 0 {
		t.Fatal("disposable daemon already has the LAN database external fixture project")
	}
}

func lanDatabaseRegisterFixtureCleanup(t *testing.T, docker string, fixture *lanDatabaseFixture) {
	t.Helper()
	t.Cleanup(func() {
		if !fixture.started {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := controllerJourneyDocker(cleanup, docker, fixture.env, "compose", "-f", filepath.Join(fixture.root, "docker-compose.yml"), "-p", lanDatabaseFixtureProject, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error("remove exact LAN database external fixture project")
		}
		for _, resource := range []struct{ kind, name string }{
			{"network", lanDatabaseFixtureNetwork},
			{"volume", lanDatabaseFixtureCertVolume},
			{"volume", lanDatabaseFixturePostgresData},
		} {
			if controllerJourneyExists(t, cleanup, docker, resource.kind, resource.name) {
				t.Errorf("LAN database external fixture %s remains: %s", resource.kind, resource.name)
			}
		}
		containers, err := controllerJourneyDocker(cleanup, docker, nil, "ps", "-aq", "--filter", "label=com.docker.compose.project="+lanDatabaseFixtureProject)
		if err != nil || len(bytes.TrimSpace(containers)) != 0 {
			t.Error("LAN database external fixture containers remain")
		}
	})
}

func lanDatabaseReadFixtureIdentity(t *testing.T, ctx context.Context, docker string, fixture *lanDatabaseFixture) lanDatabaseFixtureIdentity {
	t.Helper()
	postgres, err := controllerJourneyDocker(ctx, docker, fixture.env, "compose", "-f", filepath.Join(fixture.root, "docker-compose.yml"), "-p", lanDatabaseFixtureProject, "ps", "-q", "postgres")
	ids := strings.Fields(string(postgres))
	if err != nil || len(ids) != 1 || !controllerJourneyCanonicalContainerID(ids[0]) {
		t.Fatal("external PostgreSQL fixture has no exact container identity")
	}
	var inspection struct {
		ID       string            `json:"id"`
		Labels   map[string]string `json:"labels"`
		Running  bool              `json:"running"`
		Networks map[string]struct {
			NetworkID string `json:"NetworkID"`
		} `json:"networks"`
		Mounts []struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Destination string `json:"Destination"`
		} `json:"mounts"`
	}
	format := `{"id":{{json .ID}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"networks":{{json .NetworkSettings.Networks}},"mounts":{{json .Mounts}}}`
	body, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", format, ids[0])
	decodeErr := json.Unmarshal(bytes.TrimSpace(body), &inspection)
	if err != nil || decodeErr != nil || inspection.ID != ids[0] || !inspection.Running ||
		inspection.Labels["com.docker.compose.project"] != lanDatabaseFixtureProject || inspection.Labels["com.docker.compose.service"] != "postgres" ||
		len(inspection.Networks) != 1 || inspection.Networks[lanDatabaseFixtureNetwork].NetworkID == "" {
		t.Fatal("external PostgreSQL fixture ownership or network identity is uncertain")
	}
	if !controllerJourneyExists(t, ctx, docker, "volume", lanDatabaseFixturePostgresData) {
		t.Fatal("external PostgreSQL fixture data volume is absent")
	}
	volumeLabels, err := lanTwoAppDockerLabels(ctx, docker, "volume", lanDatabaseFixturePostgresData)
	if err != nil || volumeLabels["com.docker.compose.project"] != lanDatabaseFixtureProject ||
		volumeLabels["com.docker.compose.volume"] != "fixture-postgres-data" {
		t.Fatal("external PostgreSQL data volume ownership is uncertain")
	}
	mounted := false
	for _, mount := range inspection.Mounts {
		if mount.Type == "volume" && mount.Name == lanDatabaseFixturePostgresData && mount.Destination == "/var/lib/postgresql/data" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatal("external PostgreSQL data volume is not mounted by its exact container")
	}
	return lanDatabaseFixtureIdentity{postgresContainerID: ids[0], postgresVolumeName: lanDatabaseFixturePostgresData}
}

func lanDatabaseAssertFixtureRetained(t *testing.T, ctx context.Context, docker string, fixture *lanDatabaseFixture, expected lanDatabaseFixtureIdentity) {
	t.Helper()
	if actual := lanDatabaseReadFixtureIdentity(t, ctx, docker, fixture); actual != expected {
		t.Fatal("application operation replaced the external PostgreSQL fixture identity")
	}
}

func lanDatabaseAppNetworkID(t *testing.T, ctx context.Context, docker, networkName, appID string) string {
	t.Helper()
	format := `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Labels}}}`
	body, err := controllerJourneyDocker(ctx, docker, nil, "network", "inspect", "--format", format, networkName)
	var inspection struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err != nil || json.Unmarshal(bytes.TrimSpace(body), &inspection) != nil || !controllerJourneyCanonicalContainerID(inspection.ID) ||
		inspection.Name != networkName || inspection.Labels["io.rig.managed"] != generatedruntime.NetworkOwnershipLabelValue || inspection.Labels["io.rig.application"] != appID {
		t.Fatal("application private network ownership or identity is uncertain")
	}
	if networkName == lanDatabaseFixtureNetwork {
		t.Fatal("application private network overlaps the external database fixture network")
	}
	return inspection.ID
}

func lanDatabaseConfiguration(marker, dbURL, caBase64, sentinel string) []apicontract.ScopedConfigurationValueInput {
	return []apicontract.ScopedConfigurationValueInput{
		{Phase: "runtime", TargetComponent: "api", Key: "API_RUNTIME_MARKER", Value: marker},
		{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_URL_ENV", Value: "NOTES_FIXTURE_DB_URL"},
		{Phase: "runtime", TargetComponent: "api", Key: "NOTES_FIXTURE_DB_URL", Value: dbURL, Sensitive: true},
		{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_TLS_CA_PEM_BASE64", Value: caBase64, Sensitive: true},
		{Phase: "runtime", TargetComponent: "api", Key: "FIXTURE_SCHEMA", Value: "rig_fixture_notes"},
		{Phase: "runtime", TargetComponent: "api", Key: "TEST_SENTINEL_SECRET", Value: sentinel, Sensitive: true},
		{Phase: "build", TargetComponent: "frontend", Key: "VITE_BUILD_LABEL", Value: marker},
	}
}

func lanDatabaseAttestedURL(t *testing.T, api *lanTwoAppAPI, appID, address string, port uint16) string {
	t.Helper()
	var access apicontract.LANAppAccessRead
	api.do(http.MethodGet, "/api/v1/apps/"+appID+"/lan-access", nil, http.StatusOK, &access)
	if access.Availability != "verified" || access.GrantClaim == nil || access.GrantClaim.State != "committed" {
		t.Fatal("controller did not return a verified committed LAN route")
	}
	parsed, err := url.Parse(access.Url)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host != net.JoinHostPort(address, strconv.Itoa(int(port))) ||
		parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatal("controller returned an invalid attested LAN URL")
	}
	return access.Url
}

type lanDatabaseNote struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

func lanDatabaseCreateNote(t *testing.T, ctx context.Context, routeURL, body string) lanDatabaseNote {
	t.Helper()
	response := lanDatabaseRoutedRequest(t, ctx, routeURL, http.MethodPost, "/api/notes", map[string]string{"body": body}, http.StatusCreated)
	var result struct {
		Note lanDatabaseNote `json:"note"`
	}
	if json.Unmarshal(response, &result) != nil || !lanDatabasePositiveDecimal(result.Note.ID) || result.Note.Body != body {
		t.Fatal("LAN Notes API did not return the uniquely identified created row")
	}
	return result.Note
}

func lanDatabaseAssertNote(t *testing.T, ctx context.Context, routeURL string, expected lanDatabaseNote) {
	t.Helper()
	response := lanDatabaseRoutedRequest(t, ctx, routeURL, http.MethodGet, "/api/notes", nil, http.StatusOK)
	var result struct {
		Notes []lanDatabaseNote `json:"notes"`
	}
	if json.Unmarshal(response, &result) != nil {
		t.Fatal("LAN Notes API returned invalid JSON")
	}
	matches := 0
	for _, note := range result.Notes {
		if note.ID == expected.ID && note.Body == expected.Body {
			matches++
		}
	}
	if matches != 1 {
		t.Fatal("LAN Notes API did not retain the exact database row")
	}
}

func lanDatabaseRoutedRequest(t *testing.T, ctx context.Context, routeURL, method, path string, input any, want int) []byte {
	t.Helper()
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(routeURL, "/")+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := lanDatabaseRouteHTTPClient()
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("attested LAN request failed: %v", err)
	}
	defer response.Body.Close()
	const responseLimit = 4 << 20
	contents, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) > responseLimit {
		t.Fatal("attested LAN response exceeded the probe response limit")
	}
	if response.StatusCode != want {
		t.Fatalf("attested LAN request %s %s: got %d want %d", method, path, response.StatusCode, want)
	}
	return contents
}

func lanDatabaseRouteHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func lanDatabasePositiveDecimal(value string) bool {
	if len(value) == 0 || value[0] == '0' {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func lanDatabaseAssertScopedContainers(t *testing.T, ctx context.Context, docker, appID, networkName, gateway, dbURL, caBase64, sentinel string) {
	t.Helper()
	sources := controllerJourneyAppProbeSources(t, ctx, docker, appID, networkName, gateway)
	for _, source := range sources {
		body, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{json .Config}}", source.ContainerID)
		var configuration struct {
			Labels map[string]string
			Env    []string
		}
		decodeErr := json.Unmarshal(bytes.TrimSpace(body), &configuration)
		clear(body)
		if err != nil || decodeErr != nil || configuration.Labels["io.rig.component"] != source.Component ||
			configuration.Labels["io.rig.managed"] != "generated-runtime" || configuration.Labels["io.rig.application"] != appID {
			t.Fatal("inspect generated component configuration")
		}
		if source.Component == "api" {
			if !lanDatabaseEnvContains(configuration.Env, "NOTES_FIXTURE_DB_URL="+dbURL) ||
				!lanDatabaseEnvContains(configuration.Env, "DATABASE_TLS_CA_PEM_BASE64="+caBase64) ||
				!lanDatabaseEnvContains(configuration.Env, "TEST_SENTINEL_SECRET="+sentinel) {
				t.Fatal("API container did not receive its exact scoped server secrets")
			}
		} else if lanDatabaseEnvironmentContains(configuration.Env, dbURL) || lanDatabaseEnvironmentContains(configuration.Env, caBase64) ||
			lanDatabaseEnvironmentContains(configuration.Env, sentinel) || lanDatabaseEnvironmentContains(configuration.Env, "NOTES_FIXTURE_DB_URL=") ||
			lanDatabaseEnvironmentContains(configuration.Env, "DATABASE_TLS_CA_PEM_BASE64=") {
			t.Fatal("frontend container received a database server secret or selector")
		}
	}
}

func lanDatabaseEnvContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func lanDatabaseEnvironmentContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func lanDatabaseAssertFrontendSecretsAbsent(t *testing.T, ctx context.Context, routeURL, marker, dbURL, caBase64, sentinel string) {
	t.Helper()
	index := lanDatabaseRoutedRequest(t, ctx, routeURL, http.MethodGet, "/", nil, http.StatusOK)
	asset := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindSubmatch(index)
	if len(asset) != 2 {
		t.Fatal("attested LAN frontend has no JavaScript asset")
	}
	script := lanDatabaseRoutedRequest(t, ctx, routeURL, http.MethodGet, string(asset[1]), nil, http.StatusOK)
	if !bytes.Contains(script, []byte(marker)) {
		t.Fatal("attested LAN frontend did not serve its public replacement marker")
	}
	for _, secret := range []string{dbURL, caBase64, sentinel, "NOTES_FIXTURE_DB_URL", "DATABASE_TLS_CA_PEM_BASE64"} {
		if bytes.Contains(index, []byte(secret)) || bytes.Contains(script, []byte(secret)) {
			t.Fatal("attested LAN frontend exposed a database server secret or selector")
		}
	}
}

func lanDatabaseAssertContainerSet(t *testing.T, value string) {
	t.Helper()
	ids := strings.Split(value, ",")
	if len(ids) != 2 || value == "" {
		t.Fatal("application does not have exactly two running runtime containers")
	}
	for _, id := range ids {
		if !controllerJourneyCanonicalContainerID(id) {
			t.Fatal("application container identity is not canonical")
		}
	}
}

func lanDatabaseAssertContainersReplaced(t *testing.T, before, after string) {
	t.Helper()
	lanDatabaseAssertContainerSet(t, before)
	lanDatabaseAssertContainerSet(t, after)
	previous := make(map[string]bool, 2)
	for _, id := range strings.Split(before, ",") {
		previous[id] = true
	}
	for _, id := range strings.Split(after, ",") {
		if previous[id] {
			t.Fatal("same application redeploy retained a runtime component container")
		}
	}
}

func TestLANDatabasePositiveDecimal(t *testing.T) {
	for _, value := range []struct {
		value string
		valid bool
	}{
		{value: "1", valid: true},
		{value: "9223372036854775808", valid: true},
		{value: "", valid: false},
		{value: "0", valid: false},
		{value: "01", valid: false},
		{value: "-1", valid: false},
		{value: "1.0", valid: false},
		{value: "1e3", valid: false},
	} {
		if lanDatabasePositiveDecimal(value.value) != value.valid {
			t.Fatalf("decimal validity for %q differs from expectation", value.value)
		}
	}
}

func TestLANDatabaseEnvContainsExactValue(t *testing.T) {
	values := []string{"NOTES_FIXTURE_DB_URL=expected", "PREFIX=NOTES_FIXTURE_DB_URL=expected", "NOTES_FIXTURE_DB_URL=expected-suffix"}
	if !lanDatabaseEnvContains(values, "NOTES_FIXTURE_DB_URL=expected") ||
		lanDatabaseEnvContains(values[1:], "NOTES_FIXTURE_DB_URL=expected") {
		t.Fatal("exact environment value matching accepted a partial value")
	}
}

func TestLANDatabaseConfigurationScopes(t *testing.T) {
	entries := lanDatabaseConfiguration("public-marker", "postgresql://fixture", "fixture-ca", "fixture-sentinel")
	if len(entries) != 7 {
		t.Fatal("LAN database configuration shape changed")
	}
	seen := make(map[string]apicontract.ScopedConfigurationValueInput, len(entries))
	for _, entry := range entries {
		seen[entry.TargetComponent+"/"+entry.Key] = entry
	}
	for _, expected := range []struct {
		key       string
		phase     string
		value     string
		sensitive bool
	}{
		{"api/NOTES_FIXTURE_DB_URL", "runtime", "postgresql://fixture", true},
		{"api/DATABASE_TLS_CA_PEM_BASE64", "runtime", "fixture-ca", true},
		{"api/TEST_SENTINEL_SECRET", "runtime", "fixture-sentinel", true},
		{"frontend/VITE_BUILD_LABEL", "build", "public-marker", false},
	} {
		entry, found := seen[expected.key]
		if !found || entry.Phase != expected.phase || entry.Value != expected.value || entry.Sensitive != expected.sensitive {
			t.Fatalf("configuration entry %s lost its required scope or sensitivity", expected.key)
		}
	}
}

func TestLANDatabaseRoutedRequestRejectsRedirect(t *testing.T) {
	targetReached := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetReached <- struct{}{}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	lanDatabaseRoutedRequest(t, context.Background(), redirector.URL+"/", http.MethodGet, "/", nil, http.StatusTemporaryRedirect)
	select {
	case <-targetReached:
		t.Fatal("attested LAN helper followed a redirect outside the returned route")
	default:
	}
}
