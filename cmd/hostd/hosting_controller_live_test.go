//go:build live_docker

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
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
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const (
	controllerJourneyProject = "rig-controller-journey-test"
	controllerJourneyNetwork = "rig-controller-journey-external"
)

// This hosted gate exercises the real controller API, durable worker, compiler,
// runtime, and ingress. The source comes through a controlled GitHub connection
// and HTTP archive fixture into an immutable release snapshot.
func TestLiveControllerGeneratedDeploymentJourney(t *testing.T) {
	controllerJourneyRun(t, false)
}

func controllerJourneyRun(t *testing.T, processKill bool) {
	requiredGate := "RIG_RUN_LIVE_CONTROLLER_JOURNEY"
	if processKill {
		requiredGate = "RIG_RUN_LIVE_PROCESS_KILL"
	}
	if os.Getenv(requiredGate) != "1" {
		t.Fatalf("set %s=1 to run the hosted Docker gate", requiredGate)
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("hosted controller gate requires the local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	node := controllerJourneyExecutable(t, "node")
	sh := controllerJourneyExecutable(t, "sh")
	openssl := controllerJourneyExecutable(t, "openssl")
	installedSource, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(installedSource, "api", "node_modules", "pg")); err != nil || !info.IsDir() {
		t.Fatal("install the hosting fixture with its frozen lockfile first")
	}
	root := t.TempDir()
	// pnpm installs package links under node_modules. A local release must
	// reject links, so present the controller with a clean source tree while
	// the installed checkout remains available for schema preparation.
	source := filepath.Join(root, "source")
	tracked, err := controllerJourneyTrackedFixtureFiles(installedSource)
	if err != nil {
		t.Fatal("verify committed GitHub fixture source:", err)
	}
	if err := controllerJourneyStageSource(installedSource, source, tracked); err != nil {
		t.Fatal("stage immutable fixture source:", err)
	}
	dataRoot := filepath.Join(root, "controller")
	fixtureRoot := filepath.Join(root, "external")
	journeyTimeout := 24 * time.Minute
	if processKill {
		journeyTimeout = 47 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), journeyTimeout)
	defer cancel()
	selectedContext, err := controllerJourneyDocker(ctx, docker, nil, "context", "show")
	if err != nil || string(bytes.TrimSpace(selectedContext)) != "default" {
		t.Fatal("hosted controller gate requires Docker's default local context")
	}
	endpoint, err := controllerJourneyDocker(ctx, docker, nil, "context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil || !strings.HasPrefix(string(bytes.TrimSpace(endpoint)), "unix:///") {
		t.Fatal("hosted controller gate requires a local Unix Docker endpoint")
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "info"); err != nil {
		t.Fatal("Docker daemon unavailable")
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "compose", "version"); err != nil {
		t.Fatal("Docker Compose unavailable")
	}
	for _, resource := range [][]string{
		{"container", "rig-generated-caddy-v1"}, {"volume", "rig-generated-caddy-config-v1"},
		{"network", "rig-generated-caddy-ingress-v1"}, {"network", controllerJourneyNetwork},
		{"volume", controllerJourneyProject + "_fixture-certs"},
		{"volume", controllerJourneyProject + "_fixture-postgres-data"},
	} {
		if controllerJourneyExists(t, ctx, docker, resource[0], resource[1]) {
			t.Fatalf("disposable daemon already has %s %s", resource[0], resource[1])
		}
	}
	if output, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-aq", "--filter", "label=com.docker.compose.project="+controllerJourneyProject); err != nil || len(bytes.TrimSpace(output)) != 0 {
		t.Fatal("disposable daemon already has the external fixture project")
	}
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", "label=rig.controller=generated-builder"},
		{"network", "ls", "-q", "--filter", "label=rig.controller=generated-builder"},
	} {
		if output, err := controllerJourneyDocker(ctx, docker, nil, args...); err != nil || len(bytes.TrimSpace(output)) != 0 {
			t.Fatal("disposable daemon already has a generated builder resource")
		}
	}
	// Composition can create ingress before an application exists. Register its
	// ownership-checked cleanup before the first operation that can create it.
	cleanupIngress := func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		for _, resource := range [][]string{
			{"container", "rig-generated-caddy-v1", "generated-ingress"},
			{"volume", "rig-generated-caddy-config-v1", "generated-ingress"},
			{"network", "rig-generated-caddy-ingress-v1", "generated-ingress-network"},
		} {
			if !controllerJourneyExists(t, cleanup, docker, resource[0], resource[1]) {
				continue
			}
			format := "{{json .Labels}}"
			if resource[0] == "container" {
				format = "{{json .Config.Labels}}"
			}
			body, err := controllerJourneyDocker(cleanup, docker, nil, resource[0], "inspect", "--format", format, resource[1])
			var labels map[string]string
			if err != nil || json.Unmarshal(bytes.TrimSpace(body), &labels) != nil || labels["io.rig.managed"] != resource[2] || labels["io.rig.identity-version"] != "v1" {
				t.Errorf("global ingress %s ownership uncertain; retaining", resource[0])
				continue
			}
			removal := []string{resource[0], "rm", resource[1]}
			if resource[0] != "network" {
				removal = []string{resource[0], "rm", "--force", resource[1]}
			}
			if _, err := controllerJourneyDocker(cleanup, docker, nil, removal...); err != nil {
				t.Errorf("remove exact ingress %s", resource[0])
			}
		}
		for _, resource := range [][]string{
			{"container", "rig-generated-caddy-v1"},
			{"volume", "rig-generated-caddy-config-v1"},
			{"network", "rig-generated-caddy-ingress-v1"},
		} {
			if controllerJourneyExists(t, cleanup, docker, resource[0], resource[1]) {
				t.Errorf("generated ingress %s remains after cleanup", resource[0])
			}
		}
	}
	t.Cleanup(cleanupIngress)

	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.New(db)
	bootstrap, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := authService.Bootstrap(bootstrap, "journey-admin", "a sufficiently long disposable passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
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
	provider := controllerJourneyNewGitHubProvider(t, source)
	sourceClock := time.Now().UTC()
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", func() time.Time { return sourceClock })
	snapshots, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentStore := deployments.New(db)
	settings := config.Defaults()
	settings.DataRoot = dataRoot
	settings.GeneratedRuntime = true
	healthObserver := &controllerJourneyHealthObserver{
		delegate: runtimeprocess.ExecRunner{}, test: t, last: make(map[string]string),
	}
	composition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: appStore, snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans,
	}, runtimeCompositionOptions{dockerExecutable: docker, runner: healthObserver})
	if err != nil {
		t.Fatal("compose production generated runtime:", err)
	}
	handler := (&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobStore, Machines: machineStore, Sources: sources,
		Configuration: configuration, Deployments: deploymentStore, DeploymentPlans: plans,
		GeneratedIngress: composition.ingress, GeneratedRuntimeState: composition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()
	api := httptest.NewServer(handler)
	defer api.Close()
	apiListenAddress, err := controllerJourneyFixtureControllerListenAddress(api.URL)
	if err != nil {
		t.Fatal("validate first fixture controller listener")
	}
	apiPort, err := controllerJourneyListenPort(apiListenAddress)
	if err != nil {
		t.Fatal("read first fixture controller listener port")
	}
	client := &http.Client{Timeout: 8 * time.Second}
	request := func(method, path string, input any, want int, output any, headers ...map[string]string) []byte {
		t.Helper()
		var body io.Reader
		if input != nil {
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, api.URL+path, body)
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
	var setup apicontract.DeploymentSetupInput
	setupBytes, err := os.ReadFile(filepath.Join(source, "rig-setup.json"))
	if err != nil || json.Unmarshal(setupBytes, &setup) != nil {
		t.Fatal("read reviewed hosting setup")
	}
	var authorization apicontract.GitHubDeviceAuthorization
	request(http.MethodPost, "/api/v1/source-connections/github/device", nil, http.StatusCreated, &authorization)
	if authorization.ConnectionID == "" || authorization.PollIntervalSeconds != 1 {
		t.Fatal("controlled GitHub connection did not start")
	}
	sourceClock = sourceClock.Add(2 * time.Second)
	var connection apicontract.SourceConnection
	request(http.MethodPost, "/api/v1/source-connections/"+authorization.ConnectionID+"/device/poll", nil, http.StatusOK, &connection)
	if connection.Status != "connected" || connection.ProviderUserID != "4242" {
		t.Fatal("controlled GitHub connection did not persist an authorized identity")
	}
	var repositories apicontract.GitHubRepositoryPage
	request(http.MethodGet, "/api/v1/source-connections/"+connection.ID+"/github/installations/7/repositories", nil, http.StatusOK, &repositories)
	if len(repositories.Items) != 1 || repositories.Items[0].ID != 17 {
		t.Fatal("controlled GitHub repository selection failed")
	}
	var branches apicontract.GitHubBranchPage
	request(http.MethodGet, "/api/v1/source-connections/"+connection.ID+"/github/installations/7/repositories/17/branches", nil, http.StatusOK, &branches)
	if len(branches.Items) != 1 || branches.Items[0].Sha != controllerJourneyGitHubSHA {
		t.Fatal("controlled GitHub branch selection failed")
	}
	githubSource := apicontract.GitHubSource{ConnectionID: connection.ID, InstallationID: 7, RepositoryID: 17, Branch: "main"}
	var inspection apicontract.InspectResponse
	request(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: githubSource, Setup: &setup}, http.StatusOK, &inspection)
	if len(inspection.Analysis.Candidates) == 0 || inspection.Analysis.StructuralFingerprint == "" {
		t.Fatal("reviewed source inspection returned no candidate")
	}
	candidate := inspection.Analysis.Candidates[len(inspection.Analysis.Candidates)-1]
	var application apicontract.Application
	request(http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{Name: "Controller Journey", GithubSource: githubSource, Setup: &setup}, http.StatusCreated, &application)
	if application.ID == "" || application.Source.Type != "github" || application.Source.RepositoryID != 17 {
		t.Fatal("controller did not save the selected GitHub source")
	}
	network, err := generatedruntime.DescribeAppNetwork(application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if controllerJourneyExists(t, ctx, docker, "network", network.Name) {
		t.Fatal("app-private network already exists")
	}
	fixtureStarted := false
	fixtureEnv := []string{}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Minute)
		defer stop()
		// This gate starts on an otherwise empty disposable daemon. Remove only
		// resources carrying this application's exact ownership label.
		for _, kind := range []string{"container", "image"} {
			ids, err := controllerJourneyDocker(cleanup, docker, nil, map[string][]string{
				"container": {"ps", "-aq", "--filter", "label=io.rig.application=" + application.ID},
				"image":     {"image", "ls", "-q", "--filter", "label=io.rig.application=" + application.ID},
			}[kind]...)
			if err != nil {
				t.Errorf("list exact app-owned %s for cleanup", kind)
				continue
			}
			for _, id := range strings.Fields(string(ids)) {
				command := map[string][]string{"container": {"container", "rm", "--force", id}, "image": {"image", "rm", "--force", id}}[kind]
				if _, err := controllerJourneyDocker(cleanup, docker, nil, command...); err != nil {
					t.Errorf("remove exact app-owned %s", kind)
				}
			}
		}
		// Caddy may still be attached to the app bridge. Remove its exact
		// owned resources before checking that bridge is empty.
		cleanupIngress()
		if fixtureStarted {
			if _, err := controllerJourneyDocker(cleanup, docker, fixtureEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", controllerJourneyProject, "down", "--volumes", "--remove-orphans"); err != nil {
				t.Error("remove exact external fixture project")
			}
		}
		controllerJourneyRemoveBuilder(t, cleanup, docker, dataRoot)
		if controllerJourneyExists(t, cleanup, docker, "network", network.Name) {
			labels, err := controllerJourneyDocker(cleanup, docker, nil, "network", "inspect", "--format", "{{json .Labels}}", network.Name)
			var identity map[string]string
			if err != nil || json.Unmarshal(bytes.TrimSpace(labels), &identity) != nil || identity["io.rig.managed"] != generatedruntime.NetworkOwnershipLabelValue || identity["io.rig.application"] != application.ID {
				t.Error("app network ownership uncertain; retaining")
			} else if _, err := controllerJourneyDocker(cleanup, docker, nil, "network", "rm", network.Name); err != nil {
				t.Error("remove exact app-private network")
			}
		}
		if controllerJourneyExists(t, cleanup, docker, "network", controllerJourneyNetwork) {
			t.Error("external fixture network remains")
		}
		if controllerJourneyExists(t, cleanup, docker, "network", network.Name) {
			t.Error("app-private network remains")
		}
		for _, args := range [][]string{
			{"ps", "-aq", "--filter", "label=rig.controller=generated-builder"},
			{"network", "ls", "-q", "--filter", "label=rig.controller=generated-builder"},
		} {
			if output, err := controllerJourneyDocker(cleanup, docker, nil, args...); err != nil || len(bytes.TrimSpace(output)) != 0 {
				t.Error("generated builder resource remains")
			}
		}
	})
	var plan apicontract.DeploymentPlanRevision
	request(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		Setup: &setup, CandidateID: candidate.ID, ExpectedCandidateDigest: candidate.Digest,
		ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &plan)
	if plan.RevisionNumber != 1 || plan.RevisionID == "" || plan.Strategy != string(deploymentplans.StrategyGeneratedNode) {
		t.Fatal("controller did not persist the generated plan")
	}
	frontendPlan := false
	for _, component := range plan.Components {
		if component.Name == "frontend" {
			frontendPlan = component.Role == "static" && component.RootDirectory == "frontend" &&
				component.RunCommand == "rig-static --root 'dist' --port 8080" &&
				component.InternalPort == 8080 && component.HealthProbe == "/"
		}
	}
	if !frontendPlan {
		t.Fatal("accepted frontend plan differs from the reviewed static recipe")
	}
	// Reserve the application bridge first so the external fixture can bind to
	// its gateway. The production engine validates the same ownership labels.
	if _, err := controllerJourneyDocker(ctx, docker, nil, "network", "create", "--driver", "bridge",
		"--label", "io.rig.managed="+generatedruntime.NetworkOwnershipLabelValue,
		"--label", "io.rig.application="+application.ID, network.Name); err != nil {
		t.Fatal("create exactly owned application bridge")
	}
	gatewayOutput, err := controllerJourneyDocker(ctx, docker, nil, "network", "inspect", "--format", "{{json .IPAM.Config}}", network.Name)
	var gateways []struct{ Gateway string }
	if err != nil || json.Unmarshal(bytes.TrimSpace(gatewayOutput), &gateways) != nil || len(gateways) != 1 || net.ParseIP(gateways[0].Gateway) == nil {
		t.Fatal("inspect app bridge gateway")
	}
	gateway := gateways[0].Gateway
	dbPort := controllerJourneyPort(t, gateway, apiPort)
	httpsPort := controllerJourneyPort(t, gateway, apiPort, dbPort)
	if err := os.Mkdir(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-compose.yml", "generate-test-ca.sh", "openssl-postgres.cnf", "openssl-https.cnf", "https-stub.mjs"} {
		contents, err := os.ReadFile(filepath.Join(source, "harness", name))
		if err != nil || os.WriteFile(filepath.Join(fixtureRoot, name), contents, 0o600) != nil {
			t.Fatal("copy reviewed external fixture")
		}
	}
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
	password, dependencyToken, sentinel := uuid.NewString(), uuid.NewString(), "rig-m2-runtime-secret-"+uuid.NewString()
	fixtureEnv = []string{
		"FIXTURE_NETWORK_NAME=" + controllerJourneyNetwork,
		"FIXTURE_HOST_GATEWAY_IP=" + gateway,
		"FIXTURE_POSTGRES_BIND_ADDRESS=" + gateway,
		"FIXTURE_HTTPS_BIND_ADDRESS=" + gateway,
		"FIXTURE_POSTGRES_HOST_PORT=" + strconv.Itoa(dbPort),
		"FIXTURE_HTTPS_HOST_PORT=" + strconv.Itoa(httpsPort),
		"FIXTURE_POSTGRES_DB=fixture_notes", "FIXTURE_POSTGRES_USER=fixture_user",
		"FIXTURE_POSTGRES_PASSWORD=" + password, "FIXTURE_HTTPS_TOKEN=" + dependencyToken,
	}
	fixtureStarted = true
	if _, err := controllerJourneyDocker(ctx, docker, fixtureEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", controllerJourneyProject, "up", "-d"); err != nil {
		t.Fatal("start disposable external TLS services")
	}
	dbURL := fmt.Sprintf("postgresql://fixture_user:%s@%s:%d/fixture_notes?sslmode=verify-full", password, gateway, dbPort)
	caBase64 := base64.StdEncoding.EncodeToString(ca)
	controllerJourneyPrepareSchema(t, ctx, node, installedSource, dbURL, caBase64)
	entries := []apicontract.ScopedConfigurationValueInput{}
	add := func(component, key, value string, sensitive bool) {
		entries = append(entries, apicontract.ScopedConfigurationValueInput{Phase: "runtime", TargetComponent: component, Key: key, Value: value, Sensitive: sensitive})
	}
	add("api", "API_RUNTIME_MARKER", "controller-journey", false)
	add("api", "DATABASE_URL_ENV", "NOTES_FIXTURE_DB_URL", false)
	add("api", "NOTES_FIXTURE_DB_URL", dbURL, true)
	add("api", "DATABASE_TLS_CA_PEM_BASE64", caBase64, true)
	add("api", "FIXTURE_SCHEMA", "rig_fixture_notes", false)
	add("api", "HTTPS_DEPENDENCY_URL_ENV", "NOTES_FIXTURE_HTTPS_URL", false)
	add("api", "NOTES_FIXTURE_HTTPS_URL", fmt.Sprintf("https://%s:%d/", gateway, httpsPort), false)
	add("api", "HTTPS_DEPENDENCY_TOKEN_ENV", "NOTES_FIXTURE_HTTPS_TOKEN", false)
	add("api", "NOTES_FIXTURE_HTTPS_TOKEN", dependencyToken, true)
	add("api", "HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64", caBase64, true)
	add("api", "TEST_FIXTURE_MODE", "1", false)
	add("api", "TEST_SENTINEL_SECRET", sentinel, true)
	entries = append(entries, apicontract.ScopedConfigurationValueInput{Phase: "build", TargetComponent: "frontend", Key: "VITE_BUILD_LABEL", Value: "controller-journey-public", Sensitive: false})
	var saved apicontract.ApplicationConfiguration
	savedResponse := request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: 0, PlanRevisionID: plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: entries, Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &saved)
	if saved.RevisionNumber != 1 || saved.RevisionID == "" {
		t.Fatal("controller did not persist scoped configuration")
	}
	for _, secret := range []string{dbURL, password, dependencyToken, sentinel, caBase64} {
		if bytes.Contains(savedResponse, []byte(secret)) {
			t.Fatal("controller echoed a scoped server secret")
		}
	}
	workerContext, stopWorker := context.WithCancel(ctx)
	done, err := prepareRuntimeWorker(workerContext, runtimeRecovery{
		deployments: deploymentStore.Recover, jobs: jobStore.RecoverInterrupted,
	}, composition.executor, jobStore.RunWorker, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopWorker()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("deployment worker did not stop")
		}
	}()
	var mutation apicontract.JobMutationResponse
	deploymentPath := "/api/v1/apps/" + application.ID + "/deployments"
	assertAttestedRoute := func(expected apicontract.Deployment) string {
		t.Helper()
		var route struct {
			Status                      string `json:"status"`
			Scope                       string `json:"scope"`
			URL                         string `json:"url"`
			DeploymentID                string `json:"deploymentId"`
			ReleaseID                   string `json:"releaseId"`
			ConfigurationRevisionID     string `json:"configurationRevisionId"`
			ConfigurationRevisionNumber int64  `json:"configurationRevisionNumber"`
			PlanRevisionID              string `json:"planRevisionId"`
			PlanRevisionNumber          int64  `json:"planRevisionNumber"`
			ObservedAt                  string `json:"observedAt"`
		}
		body := request(http.MethodGet, "/api/v1/apps/"+application.ID+"/local-route", nil, http.StatusOK, &route)
		if route.Status != "verified" || route.Scope != "controller_loopback" ||
			route.URL != "http://"+application.ID+".rig.localhost:8080" ||
			route.DeploymentID != expected.ID || route.ReleaseID != expected.ReleaseID ||
			route.ConfigurationRevisionID != expected.ActualConfigurationRevisionID ||
			route.ConfigurationRevisionNumber != expected.ActualConfigurationRevisionNumber ||
			route.PlanRevisionID != expected.DeploymentPlanRevisionID ||
			route.PlanRevisionNumber != expected.DeploymentPlanRevisionNumber {
			t.Fatal("attested local route does not match the active immutable generated deployment")
		}
		if _, err := time.Parse(time.RFC3339Nano, route.ObservedAt); err != nil {
			t.Fatal("attested local route has no observation time")
		}
		if bytes.Contains(body, []byte(dbURL)) || bytes.Contains(body, []byte(sentinel)) {
			t.Fatal("attested local route disclosed a scoped runtime secret")
		}
		return route.URL
	}
	assertControllerAppIsolation := func(probeSources []controllerJourneyAppProbeSource) {
		t.Helper()
		target, err := controllerJourneyControllerProbeTarget(api.URL, gateway, application.ID)
		if err != nil {
			t.Fatal("derive the attested controller isolation probe target")
		}
		var deployments apicontract.DeploymentList
		request(http.MethodGet, target.Path, nil, http.StatusOK, &deployments)
		if len(deployments.Items) == 0 {
			t.Fatal("authenticated controller read-only positive control returned no deployments")
		}
		controllerJourneyAssertAppControllerDenied(t, ctx, docker, dataRoot, probeSources, target)
	}
	var absentRoute struct {
		Status       string `json:"status"`
		URL          string `json:"url"`
		DeploymentID string `json:"deploymentId"`
	}
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/local-route", nil, http.StatusOK, &absentRoute)
	if absentRoute.Status == "verified" || absentRoute.URL != "" || absentRoute.DeploymentID != "" {
		t.Fatal("controller exposed a generated route before deployment")
	}
	deployRequest := apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID: saved.RevisionID, ExpectedConfigurationRevisionNumber: saved.RevisionNumber,
	}
	idempotencyKey := uuid.NewString()
	request(http.MethodPost, deploymentPath, deployRequest, http.StatusAccepted, &mutation, map[string]string{"Idempotency-Key": idempotencyKey})
	if !mutation.Created || mutation.Job.ID == "" || mutation.Job.RequestedBy != user.ID {
		t.Fatal("controller did not enqueue an actor-bound durable job")
	}
	var replay apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, deployRequest, http.StatusOK, &replay, map[string]string{"Idempotency-Key": idempotencyKey})
	if replay.Created || replay.Job.ID != mutation.Job.ID {
		t.Fatal("controller did not replay the exact durable deployment job")
	}
	deadline := time.Now().Add(16 * time.Minute)
	var completed jobs.Job
	for {
		completed, err = jobStore.Get(mutation.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status == string(jobs.Succeeded) {
			break
		}
		if completed.Status == string(jobs.Failed) || completed.Status == string(jobs.WaitingUser) || completed.Status == string(jobs.NeedsAttention) || time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("generated deployment did not succeed: status=%s phase=%s code=%s", completed.Status, completed.Phase, completed.ErrorCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	var history apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &history)
	if len(history.Items) != 1 || history.Items[0].JobID != completed.ID || history.Items[0].RuntimeStrategy != string(deploymentplans.StrategyGeneratedNode) || history.Items[0].Status != "succeeded" ||
		history.Items[0].DeploymentPlanRevisionID != plan.RevisionID || history.Items[0].DeploymentPlanRevisionNumber != plan.RevisionNumber ||
		history.Items[0].ActualConfigurationRevisionID != saved.RevisionID || history.Items[0].ActualConfigurationRevisionNumber != saved.RevisionNumber {
		t.Fatal("successful durable deployment has no exact generated history")
	}
	var releases apicontract.ReleaseList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &releases)
	if len(releases.Items) != 1 || releases.Items[0].ID != history.Items[0].ReleaseID ||
		releases.Items[0].SourceProvider != "github" || releases.Items[0].RepositoryID != 17 || releases.Items[0].ResolvedSha != controllerJourneyGitHubSHA || releases.Items[0].WorkspaceState != "ready" ||
		releases.Items[0].DeploymentPlanRevisionID != plan.RevisionID || releases.Items[0].DeploymentPlanRevisionNumber != plan.RevisionNumber ||
		releases.Items[0].ConfigurationRevisionID != saved.RevisionID || releases.Items[0].ConfigurationRevisionNumber != saved.RevisionNumber {
		t.Fatal("deployment did not pin one immutable release")
	}
	archiveDigest := sha256.Sum256(provider.archive)
	if releases.Items[0].ArchiveSha256 != fmt.Sprintf("%x", archiveDigest) || provider.archiveReads.Load() == 0 {
		t.Fatal("GitHub release has no downloaded immutable archive digest")
	}
	workspace, err := snapshots.ReadyWorkspace(ctx, application.ID, releases.Items[0].ID)
	if err != nil || workspace.WorkspaceTreeSHA256 == "" || workspace.WorkspaceState != "ready" {
		t.Fatal("pinned GitHub release workspace failed digest verification")
	}
	initialRouteURL := assertAttestedRoute(history.Items[0])
	initialProbeSources := controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, network.Name, gateway, dbURL, sentinel)
	assertControllerAppIsolation(initialProbeSources)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/version", "", http.StatusOK, "controller-journey")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/test/dependency", "", http.StatusOK, "reachable")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodPost, "/api/notes", `{"body":"controller TLS note"}`, http.StatusCreated, "controller TLS note")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	if !t.Run("ExternalDependencyOutage", func(t *testing.T) {
		compose := []string{"compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", controllerJourneyProject}
		postgresID, err := controllerJourneyDocker(ctx, docker, fixtureEnv, append(append([]string{}, compose...), "ps", "-a", "-q", "postgres")...)
		if err != nil || len(strings.Fields(string(postgresID))) != 1 {
			t.Fatal("identify exactly one application-owned PostgreSQL fixture container")
		}
		postgresID = bytes.TrimSpace(postgresID)
		postgresLabels, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{json .Config.Labels}}", string(postgresID))
		var fixtureLabels map[string]string
		if err != nil || json.Unmarshal(bytes.TrimSpace(postgresLabels), &fixtureLabels) != nil ||
			fixtureLabels["com.docker.compose.project"] != controllerJourneyProject || fixtureLabels["com.docker.compose.service"] != "postgres" {
			t.Fatal("PostgreSQL fixture ownership is uncertain")
		}
		beforeState, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{.State.Running}}", string(postgresID))
		if err != nil || string(bytes.TrimSpace(beforeState)) != "true" {
			t.Fatal("PostgreSQL fixture is not running before outage")
		}
		archiveReadsBefore := provider.archiveReads.Load()
		containersBefore := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=io.rig.application="+application.ID)
		apiContainer := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-q", "--filter", "label=io.rig.application="+application.ID, "--filter", "label=io.rig.component=api")
		frontendContainer := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-q", "--filter", "label=io.rig.application="+application.ID, "--filter", "label=io.rig.component=frontend")
		if apiContainer == "" || strings.Contains(apiContainer, ",") || frontendContainer == "" || strings.Contains(frontendContainer, ",") {
			t.Fatal("outage preflight requires exactly one API and one frontend container")
		}
		stopped := true
		defer func() {
			if !stopped {
				return
			}
			recovery, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := controllerJourneyDocker(recovery, docker, nil, "container", "start", string(postgresID)); err != nil {
				t.Error("restore application-owned PostgreSQL fixture after failed outage assertion")
			}
		}()
		if _, err := controllerJourneyDocker(ctx, docker, nil, "container", "stop", "--time", "10", string(postgresID)); err != nil {
			t.Fatal("stop only the exact external PostgreSQL container")
		}
		state, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{.State.Running}}", string(postgresID))
		if err != nil || string(bytes.TrimSpace(state)) != "false" {
			t.Fatal("external PostgreSQL fixture did not stop")
		}
		controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusServiceUnavailable, `"database_unavailable"`)
		unhealthy := false
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			if controllerJourneyContainerHealth(ctx, docker, apiContainer) == "unhealthy" {
				unhealthy = true
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !unhealthy || controllerJourneyContainerHealth(ctx, docker, frontendContainer) != "healthy" {
			t.Fatal("Docker did not observe API unready and frontend healthy during database outage")
		}
		controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/test/dependency", "", http.StatusOK, "reachable")
		controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/", "", http.StatusOK, "<html")
		var duringConnection apicontract.SourceConnectionList
		request(http.MethodGet, "/api/v1/source-connections", nil, http.StatusOK, &duringConnection)
		if len(duringConnection.Items) != 1 || duringConnection.Items[0].ID != connection.ID ||
			duringConnection.Items[0].Status != "connected" || duringConnection.Items[0].CredentialGeneration != connection.CredentialGeneration {
			t.Fatal("workload dependency outage changed GitHub source authorization")
		}
		var duringHistory apicontract.DeploymentList
		request(http.MethodGet, deploymentPath, nil, http.StatusOK, &duringHistory)
		if len(duringHistory.Items) != 1 || !reflect.DeepEqual(duringHistory.Items[0], history.Items[0]) {
			t.Fatal("workload dependency outage rewrote immutable deployment history")
		}
		var duringReleases apicontract.ReleaseList
		request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &duringReleases)
		if !reflect.DeepEqual(duringReleases.Items, releases.Items) {
			t.Fatal("workload dependency outage rewrote immutable release history")
		}
		if assertAttestedRoute(history.Items[0]) != initialRouteURL {
			t.Fatal("external database outage changed the attested ingress route")
		}
		if got := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=io.rig.application="+application.ID); got != containersBefore {
			t.Fatal("external database outage replaced an application container")
		}
		if _, err := controllerJourneyDocker(ctx, docker, nil, "container", "start", string(postgresID)); err != nil {
			t.Fatal("restore application-owned PostgreSQL fixture")
		}
		stopped = false
		recovered := false
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
			if controllerJourneyContainerHealth(ctx, docker, apiContainer) == "healthy" &&
				controllerJourneyRoutedStatus(ctx, application.ID, "/api/notes") == http.StatusOK {
				recovered = true
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !recovered {
			t.Fatal("application did not reconnect to external PostgreSQL within 60 seconds")
		}
		controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
		postgresAfter, err := controllerJourneyDocker(ctx, docker, fixtureEnv, append(append([]string{}, compose...), "ps", "-a", "-q", "postgres")...)
		if err != nil || !bytes.Equal(bytes.TrimSpace(postgresAfter), postgresID) {
			t.Fatal("external fixture recovery replaced its PostgreSQL container")
		}
		if got := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=io.rig.application="+application.ID); got != containersBefore {
			t.Fatal("external dependency recovery replaced an application container")
		}
		var afterHistory apicontract.DeploymentList
		request(http.MethodGet, deploymentPath, nil, http.StatusOK, &afterHistory)
		if len(afterHistory.Items) != 1 || !reflect.DeepEqual(afterHistory.Items[0], history.Items[0]) || provider.archiveReads.Load() != archiveReadsBefore {
			t.Fatal("external dependency recovery created a deployment or changed a release")
		}
		var afterReleases apicontract.ReleaseList
		request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &afterReleases)
		if !reflect.DeepEqual(afterReleases.Items, releases.Items) {
			t.Fatal("external dependency recovery rewrote immutable release history")
		}
		var afterConnection apicontract.SourceConnectionList
		request(http.MethodGet, "/api/v1/source-connections", nil, http.StatusOK, &afterConnection)
		if len(afterConnection.Items) != 1 || afterConnection.Items[0].ID != connection.ID ||
			afterConnection.Items[0].Status != "connected" || afterConnection.Items[0].CredentialGeneration != connection.CredentialGeneration {
			t.Fatal("external dependency recovery changed GitHub source authorization")
		}
		t.Logf("RUN-11 external PostgreSQL outage: app=%s deployment=%s release=%s source=%s status=connected route=%s api-health=unhealthy/recovered frontend-health=healthy notes=503/recovered postgres-container-preserved=true app-containers-preserved=true", application.ID, history.Items[0].ID, releases.Items[0].ID, connection.ID, initialRouteURL)
	}) {
		t.Fatal("RUN-11 external dependency outage failed")
	}
	index := controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/", "", http.StatusOK, "<html")
	asset := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindSubmatch(index)
	if len(asset) != 2 {
		t.Fatal("deployed frontend has no JavaScript asset")
	}
	script := controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, string(asset[1]), "", http.StatusOK, "controller-journey-public")
	if bytes.Contains(index, []byte("rig-m2-runtime-secret-")) || bytes.Contains(script, []byte("rig-m2-runtime-secret-")) ||
		bytes.Contains(index, []byte("NOTES_FIXTURE_DB_URL")) || bytes.Contains(script, []byte("NOTES_FIXTURE_DB_URL")) {
		t.Fatal("deployed frontend exposed a scoped server secret or selector")
	}
	browserNote := "browser TLS note " + uuid.NewString()
	controllerJourneyBrowser(t, ctx, node, application.ID, initialRouteURL, "create", browserNote)
	if processKill {
		stopWorker()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("initial deployment worker did not stop before process-kill matrix")
		}
		controllerProcessKillMatrix(t, ctx, docker, source, dataRoot, application.ID, user.ID, plan, saved, entries, &api, handler, composition.ingress, jobStore, db, request)
		return
	}
	stopWorker()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deployment worker did not stop for controller restart")
	}
	api.Close()
	if err := db.Close(); err != nil {
		t.Fatal("close controller database for restart")
	}
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	capacityPressure := &controllerJourneyCapacitySource{}
	restartedObserver := &controllerJourneyHealthObserver{
		delegate: runtimeprocess.ExecRunner{}, test: t, last: make(map[string]string),
	}
	type restartedController struct {
		db            *sql.DB
		apps          *apps.Store
		jobs          *jobs.Service
		deployments   *deployments.Repository
		configuration *appconfig.Store
		plans         *deploymentplans.Store
		sources       *sourceconnections.Service
		composition   runtimeComposition
	}
	openRestartedController := func(controllerDB *sql.DB) (restartedController, error) {
		configuration, err := appconfig.New(controllerDB, dataRoot)
		if err != nil {
			return restartedController{}, err
		}
		plans, err := deploymentplans.New(controllerDB, dataRoot)
		if err != nil {
			return restartedController{}, err
		}
		sources := sourceconnections.NewService(sourceconnections.NewRepository(controllerDB), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", time.Now)
		snapshots, err := releasesnapshot.New(controllerDB, sources, dataRoot)
		if err != nil {
			return restartedController{}, err
		}
		value := restartedController{
			db: controllerDB, apps: apps.New(controllerDB), jobs: jobs.New(controllerDB), deployments: deployments.New(controllerDB),
			configuration: configuration, plans: plans, sources: sources,
		}
		value.composition, err = prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
			db: controllerDB, applications: value.apps, snapshots: snapshots, configuration: value.configuration,
			deployments: value.deployments, plans: value.plans,
		}, runtimeCompositionOptions{
			dockerExecutable: docker,
			runner:           restartedObserver,
			capacitySourceFactory: func(source generatedruntime.CapacitySource) generatedruntime.CapacitySource {
				capacityPressure.SetDelegate(source)
				return capacityPressure
			},
		})
		return value, err
	}
	startRestartedController := func(value restartedController) (*httptest.Server, context.CancelFunc, <-chan struct{}, error) {
		workerContext, stop := context.WithCancel(ctx)
		done, err := prepareRuntimeWorker(workerContext, runtimeRecovery{
			deployments: value.deployments.Recover, jobs: value.jobs.RecoverInterrupted,
		}, value.composition.executor, value.jobs.RunWorker, func(error) {})
		if err != nil {
			stop()
			return nil, nil, nil, err
		}
		server := controllerJourneyRestartControllerServer(t, (&controller.Server{
			Auth: auth.New(value.db), Apps: value.apps, Jobs: value.jobs, Machines: machines.New(value.db), Sources: value.sources,
			Configuration: value.configuration, Deployments: value.deployments, DeploymentPlans: value.plans,
			GeneratedIngress: value.composition.ingress, GeneratedRuntimeState: value.composition.state,
			GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}).Handler(), dbPort, httpsPort)
		return server, stop, done, nil
	}
	reopenedDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal("reopen durable controller database:", err)
	}
	defer reopenedDB.Close()
	restarted, err := openRestartedController(reopenedDB)
	if err != nil {
		t.Fatal("recover generated runtime after controller restart:", err)
	}
	var stopRestartedWorker context.CancelFunc
	var restartedDone <-chan struct{}
	api, stopRestartedWorker, restartedDone, err = startRestartedController(restarted)
	if err != nil {
		t.Fatal("restart durable deployment worker:", err)
	}
	defer func() {
		stopRestartedWorker()
		select {
		case <-restartedDone:
		case <-time.After(10 * time.Second):
			t.Error("restarted deployment worker did not stop")
		}
	}()
	defer api.Close()
	var retainedJob jobs.Job
	request(http.MethodGet, "/api/v1/jobs/"+completed.ID, nil, http.StatusOK, &retainedJob)
	if retainedJob.ID != completed.ID || retainedJob.Status != string(jobs.Succeeded) {
		t.Fatal("controller restart lost durable deployment job")
	}
	var retainedHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &retainedHistory)
	if len(retainedHistory.Items) != 1 || retainedHistory.Items[0].ID != history.Items[0].ID || retainedHistory.Items[0].ReleaseID != releases.Items[0].ID {
		t.Fatal("controller restart lost active deployment identity")
	}
	retainedRouteURL := assertAttestedRoute(retainedHistory.Items[0])
	restartedProbeSources := controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, network.Name, gateway, dbURL, sentinel)
	assertControllerAppIsolation(restartedProbeSources)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	controllerJourneyBrowser(t, ctx, node, application.ID, retainedRouteURL, "read", browserNote)
	activeBeforeCapacity, err := restarted.composition.state.Active(ctx, application.ID)
	if err != nil || activeBeforeCapacity.DeploymentID != retainedHistory.Items[0].ID || activeBeforeCapacity.ReleaseID != retainedHistory.Items[0].ReleaseID {
		t.Fatal("restarted controller has no active immutable serving head")
	}
	servingContainerIDs := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=io.rig.application="+application.ID)
	builderContainerIDs := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=rig.controller=generated-builder")
	builderNetworkIDs := controllerJourneyDockerIDSet(t, ctx, docker, "network", "ls", "-q", "--filter", "label=rig.controller=generated-builder")
	buildCallsBeforeCapacity := restartedObserver.BuildCalls()
	archiveReadsBeforeCapacity := provider.archiveReads.Load()
	publicEntries := make([]apicontract.ScopedConfigurationValueInput, 0, len(entries))
	for _, entry := range entries {
		if entry.Sensitive {
			continue // The scoped store preserves secrets in the same plan and scope.
		}
		if entry.Key == "API_RUNTIME_MARKER" {
			entry.Value = "controller-journey-v2"
		}
		publicEntries = append(publicEntries, entry)
	}
	var replacementConfiguration apicontract.ApplicationConfiguration
	replacementResponse := request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: saved.RevisionNumber, PlanRevisionID: plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: publicEntries, Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &replacementConfiguration)
	if replacementConfiguration.RevisionNumber != saved.RevisionNumber+1 || replacementConfiguration.RevisionID == saved.RevisionID ||
		bytes.Contains(replacementResponse, []byte(dbURL)) || bytes.Contains(replacementResponse, []byte(sentinel)) {
		t.Fatal("configuration-only replacement did not create a protected revision")
	}
	capacityPressure.SetArmed(true)
	var replacementMutation apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID: replacementConfiguration.RevisionID, ExpectedConfigurationRevisionNumber: replacementConfiguration.RevisionNumber,
	}, http.StatusAccepted, &replacementMutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !replacementMutation.Created || replacementMutation.Job.ID == completed.ID {
		t.Fatal("controller did not enqueue a distinct replacement job")
	}
	pauseDeadline := time.Now().Add(4 * time.Minute)
	var pausedReplacement jobs.Job
	for {
		pausedReplacement, err = restarted.jobs.Get(replacementMutation.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if pausedReplacement.Status == string(jobs.WaitingUser) && pausedReplacement.PauseDisposition == jobs.PauseInsufficientReplacementCapacity {
			break
		}
		if pausedReplacement.Status == string(jobs.Failed) || pausedReplacement.Status == string(jobs.Succeeded) || pausedReplacement.Status == string(jobs.NeedsAttention) || time.Now().After(pauseDeadline) || ctx.Err() != nil {
			t.Fatalf("replacement did not pause for capacity: status=%s phase=%s code=%s disposition=%s", pausedReplacement.Status, pausedReplacement.Phase, pausedReplacement.ErrorCode, pausedReplacement.PauseDisposition)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if pausedReplacement.ID != replacementMutation.Job.ID || pausedReplacement.Attempt != 1 || pausedReplacement.Phase != jobs.PauseInsufficientReplacementCapacity || pausedReplacement.ErrorCode != "" {
		t.Fatal("capacity pause did not preserve the original durable job")
	}
	var pausedHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &pausedHistory)
	if len(pausedHistory.Items) != 2 {
		t.Fatal("capacity pause did not retain one accepted replacement deployment")
	}
	var pausedDeployment apicontract.Deployment
	for _, item := range pausedHistory.Items {
		if item.JobID == pausedReplacement.ID {
			pausedDeployment = item
		}
	}
	if pausedDeployment.ID == "" || pausedDeployment.Status != "preparing" || pausedDeployment.ReleaseID == "" ||
		pausedDeployment.RuntimeStrategy != string(deploymentplans.StrategyGeneratedNode) ||
		pausedDeployment.DeploymentPlanRevisionID != plan.RevisionID || pausedDeployment.DeploymentPlanRevisionNumber != plan.RevisionNumber ||
		pausedDeployment.ActualConfigurationRevisionID != replacementConfiguration.RevisionID || pausedDeployment.ActualConfigurationRevisionNumber != replacementConfiguration.RevisionNumber {
		t.Fatal("capacity pause did not retain accepted immutable deployment pins")
	}
	if provider.archiveReads.Load() != archiveReadsBeforeCapacity+1 {
		t.Fatal("capacity pause did not materialize exactly one accepted replacement release")
	}
	pausedRuntime, err := restarted.composition.state.Get(ctx, application.ID, pausedDeployment.ID)
	if err != nil || pausedRuntime.Phase != generatedruntimestate.PhaseBuilding || pausedRuntime.ReleaseID != pausedDeployment.ReleaseID ||
		pausedRuntime.DeploymentPlanRevisionID != pausedDeployment.DeploymentPlanRevisionID || pausedRuntime.DeploymentPlanRevisionNumber != pausedDeployment.DeploymentPlanRevisionNumber {
		t.Fatal("capacity pause durable runtime state is not pinned before candidate work")
	}
	for _, component := range pausedRuntime.Components {
		if component.State != generatedruntimestate.ComponentPending || component.ContainerID != "" {
			t.Fatal("capacity pause created a candidate component")
		}
	}
	if restartedObserver.BuildCalls() != buildCallsBeforeCapacity ||
		controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=io.rig.application="+application.ID) != servingContainerIDs ||
		controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=rig.controller=generated-builder") != builderContainerIDs ||
		controllerJourneyDockerIDSet(t, ctx, docker, "network", "ls", "-q", "--filter", "label=rig.controller=generated-builder") != builderNetworkIDs {
		t.Fatal("capacity pause performed a build or changed Docker resource identities")
	}
	activeAfterCapacity, err := restarted.composition.state.Active(ctx, application.ID)
	if err != nil || activeAfterCapacity.DeploymentID != activeBeforeCapacity.DeploymentID || activeAfterCapacity.ReleaseID != activeBeforeCapacity.ReleaseID ||
		activeAfterCapacity.Slot != activeBeforeCapacity.Slot || activeAfterCapacity.Generation != activeBeforeCapacity.Generation {
		t.Fatal("capacity pause changed the active serving head")
	}
	pausedRouteURL := assertAttestedRoute(retainedHistory.Items[0])
	if pausedRouteURL != retainedRouteURL {
		t.Fatal("capacity pause changed the attested serving route")
	}
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	controllerJourneyBrowser(t, ctx, node, application.ID, retainedRouteURL, "read", browserNote)
	pausedInput := append([]byte(nil), pausedReplacement.Input...)
	stopRestartedWorker()
	select {
	case <-restartedDone:
	case <-time.After(10 * time.Second):
		t.Fatal("capacity-paused deployment worker did not stop for controller restart")
	}
	api.Close()
	if err := reopenedDB.Close(); err != nil {
		t.Fatal("close capacity-paused controller database for restart")
	}
	capacityRestartDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal("reopen capacity-paused controller database:", err)
	}
	defer capacityRestartDB.Close()
	capacityRestart, err := openRestartedController(capacityRestartDB)
	if err != nil {
		t.Fatal("recover capacity-paused generated runtime after controller restart:", err)
	}
	api, stopCapacityRestartWorker, capacityRestartDone, err := startRestartedController(capacityRestart)
	if err != nil {
		t.Fatal("restart capacity-paused durable deployment worker:", err)
	}
	defer func() {
		stopCapacityRestartWorker()
		select {
		case <-capacityRestartDone:
		case <-time.After(10 * time.Second):
			t.Error("capacity-restarted deployment worker did not stop")
		}
	}()
	defer api.Close()
	recoveredPaused, err := capacityRestart.jobs.Get(pausedReplacement.ID)
	if err != nil || recoveredPaused.Status != string(jobs.WaitingUser) || recoveredPaused.Phase != jobs.PauseInsufficientReplacementCapacity ||
		recoveredPaused.PauseDisposition != jobs.PauseInsufficientReplacementCapacity || recoveredPaused.ErrorCode != "" ||
		recoveredPaused.Attempt != pausedReplacement.Attempt || !bytes.Equal(recoveredPaused.Input, pausedInput) {
		t.Fatal("controller restart changed the intentional capacity pause")
	}
	var capacityRestartHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &capacityRestartHistory)
	if len(capacityRestartHistory.Items) != 2 {
		t.Fatal("controller restart changed capacity-paused deployment history")
	}
	var capacityRestartDeployment apicontract.Deployment
	for _, item := range capacityRestartHistory.Items {
		if item.JobID == pausedReplacement.ID {
			capacityRestartDeployment = item
		}
	}
	if capacityRestartDeployment.ID != pausedDeployment.ID || capacityRestartDeployment.ReleaseID != pausedDeployment.ReleaseID ||
		capacityRestartDeployment.Status != pausedDeployment.Status || capacityRestartDeployment.RuntimeStrategy != pausedDeployment.RuntimeStrategy ||
		capacityRestartDeployment.DeploymentPlanRevisionID != pausedDeployment.DeploymentPlanRevisionID || capacityRestartDeployment.DeploymentPlanRevisionNumber != pausedDeployment.DeploymentPlanRevisionNumber ||
		capacityRestartDeployment.ActualConfigurationRevisionID != pausedDeployment.ActualConfigurationRevisionID || capacityRestartDeployment.ActualConfigurationRevisionNumber != pausedDeployment.ActualConfigurationRevisionNumber {
		t.Fatal("controller restart changed capacity-paused immutable deployment pins")
	}
	var capacityRestartReleases apicontract.ReleaseList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &capacityRestartReleases)
	if len(capacityRestartReleases.Items) != 2 {
		t.Fatal("controller restart changed capacity-paused release history")
	}
	var capacityRestartRelease apicontract.Release
	for _, item := range capacityRestartReleases.Items {
		if item.ID == pausedDeployment.ReleaseID {
			capacityRestartRelease = item
		}
	}
	if capacityRestartRelease.ID != pausedDeployment.ReleaseID || capacityRestartRelease.ConfigurationRevisionID != replacementConfiguration.RevisionID ||
		capacityRestartRelease.ConfigurationRevisionNumber != replacementConfiguration.RevisionNumber || capacityRestartRelease.DeploymentPlanRevisionID != plan.RevisionID ||
		capacityRestartRelease.DeploymentPlanRevisionNumber != plan.RevisionNumber {
		t.Fatal("controller restart changed capacity-paused release pins")
	}
	capacityRestartRuntime, err := capacityRestart.composition.state.Get(ctx, application.ID, pausedDeployment.ID)
	if err != nil || capacityRestartRuntime.Phase != pausedRuntime.Phase || capacityRestartRuntime.MigrationState != pausedRuntime.MigrationState ||
		capacityRestartRuntime.ReleaseID != pausedRuntime.ReleaseID || capacityRestartRuntime.DeploymentPlanRevisionID != pausedRuntime.DeploymentPlanRevisionID ||
		capacityRestartRuntime.DeploymentPlanRevisionNumber != pausedRuntime.DeploymentPlanRevisionNumber {
		t.Fatal("controller restart changed capacity-paused runtime state")
	}
	if len(capacityRestartRuntime.Components) != len(pausedRuntime.Components) {
		t.Fatal("controller restart changed capacity-paused runtime components")
	}
	for index, component := range capacityRestartRuntime.Components {
		before := pausedRuntime.Components[index]
		if component.Name != before.Name || component.Slot != before.Slot || component.ImageArtifactID != before.ImageArtifactID ||
			component.ContainerName != before.ContainerName || component.ContainerID != before.ContainerID || component.State != before.State {
			t.Fatal("controller restart changed capacity-paused runtime component identity")
		}
	}
	capacityRestartHead, err := capacityRestart.composition.state.Active(ctx, application.ID)
	if err != nil || capacityRestartHead.DeploymentID != activeBeforeCapacity.DeploymentID || capacityRestartHead.ReleaseID != activeBeforeCapacity.ReleaseID ||
		capacityRestartHead.Slot != activeBeforeCapacity.Slot || capacityRestartHead.Generation != activeBeforeCapacity.Generation {
		t.Fatal("controller restart changed the active serving head during capacity pause")
	}
	if restartedObserver.BuildCalls() != buildCallsBeforeCapacity || provider.archiveReads.Load() != archiveReadsBeforeCapacity+1 ||
		controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=io.rig.application="+application.ID) != servingContainerIDs ||
		controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-aq", "--filter", "label=rig.controller=generated-builder") != builderContainerIDs ||
		controllerJourneyDockerIDSet(t, ctx, docker, "network", "ls", "-q", "--filter", "label=rig.controller=generated-builder") != builderNetworkIDs {
		t.Fatal("controller restart changed capacity-paused runtime resources")
	}
	capacityRestartRouteURL := assertAttestedRoute(retainedHistory.Items[0])
	if capacityRestartRouteURL != retainedRouteURL {
		t.Fatal("controller restart changed the attested route during capacity pause")
	}
	capacityRestartProbeSources := controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, network.Name, gateway, dbURL, sentinel)
	assertControllerAppIsolation(capacityRestartProbeSources)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	controllerJourneyBrowser(t, ctx, node, application.ID, retainedRouteURL, "read", browserNote)
	capacityPressure.SetArmed(false)
	var resumedResponse apicontract.JobResponse
	request(http.MethodPost, "/api/v1/jobs/"+pausedReplacement.ID+"/resume", nil, http.StatusOK, &resumedResponse)
	resumedInProgress := resumedResponse.Job.Status == string(jobs.Queued) || resumedResponse.Job.Status == string(jobs.Assigned) ||
		resumedResponse.Job.Status == string(jobs.Running) || resumedResponse.Job.Status == string(jobs.Waiting) || resumedResponse.Job.Status == string(jobs.Succeeded)
	if resumedResponse.Job.ID != pausedReplacement.ID || !resumedInProgress || resumedResponse.Job.PauseDisposition != "" || resumedResponse.Job.ErrorCode != "" ||
		(resumedResponse.Job.Attempt != pausedReplacement.Attempt && resumedResponse.Job.Attempt != pausedReplacement.Attempt+1) {
		t.Fatal("authenticated capacity resume did not preserve the original job")
	}
	replacementDeadline := time.Now().Add(8 * time.Minute)
	var replacementJob jobs.Job
	for {
		replacementJob, err = capacityRestart.jobs.Get(replacementMutation.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if replacementJob.Status == string(jobs.Succeeded) {
			break
		}
		if replacementJob.Status == string(jobs.Failed) || replacementJob.Status == string(jobs.WaitingUser) || replacementJob.Status == string(jobs.NeedsAttention) || time.Now().After(replacementDeadline) || ctx.Err() != nil {
			t.Fatalf("healthy replacement did not succeed: status=%s phase=%s code=%s", replacementJob.Status, replacementJob.Phase, replacementJob.ErrorCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if replacementJob.ID != pausedReplacement.ID || replacementJob.Attempt != pausedReplacement.Attempt+1 {
		t.Fatal("capacity resume did not complete the original job on its next attempt")
	}
	if restartedObserver.BuildCalls() != buildCallsBeforeCapacity+len(plan.Components) || provider.archiveReads.Load() != archiveReadsBeforeCapacity+1 {
		t.Fatal("capacity resume duplicated accepted replacement build or source materialization")
	}
	var replacementHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &replacementHistory)
	if len(replacementHistory.Items) != 2 {
		t.Fatal("replacement history did not retain both deployments")
	}
	var replacementDeployment apicontract.Deployment
	for _, item := range replacementHistory.Items {
		if item.JobID == replacementJob.ID {
			replacementDeployment = item
		}
	}
	if replacementDeployment.ID != pausedDeployment.ID || replacementDeployment.ID == "" || replacementDeployment.Status != "succeeded" || replacementDeployment.ReleaseID == history.Items[0].ReleaseID ||
		replacementDeployment.ReleaseID != pausedDeployment.ReleaseID ||
		replacementDeployment.ActualConfigurationRevisionID != replacementConfiguration.RevisionID || replacementDeployment.ActualConfigurationRevisionNumber != replacementConfiguration.RevisionNumber {
		t.Fatal("replacement deployment lacks the new configuration and source pins")
	}
	var replacementReleases apicontract.ReleaseList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &replacementReleases)
	if len(replacementReleases.Items) != 2 {
		t.Fatal("capacity resume did not retain exactly one replacement release")
	}
	var replacementRelease apicontract.Release
	for _, item := range replacementReleases.Items {
		if item.ID == replacementDeployment.ReleaseID {
			replacementRelease = item
		}
	}
	if replacementRelease.ID == "" || replacementRelease.ArchiveSha256 != releases.Items[0].ArchiveSha256 ||
		replacementRelease.ConfigurationRevisionID != replacementConfiguration.RevisionID || replacementRelease.ConfigurationRevisionNumber != replacementConfiguration.RevisionNumber {
		t.Fatal("same-source replacement did not pin the new configuration")
	}
	replacementRouteURL := assertAttestedRoute(replacementDeployment)
	controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, network.Name, gateway, dbURL, sentinel)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/version", "", http.StatusOK, "controller-journey-v2")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	controllerJourneyBrowser(t, ctx, node, application.ID, replacementRouteURL, "read", browserNote)
	wrongCA := controllerJourneyWrongCA(t, ctx, openssl, fixtureRoot)
	wrongCABase64 := base64.StdEncoding.EncodeToString(wrongCA)
	badEntries := append(append([]apicontract.ScopedConfigurationValueInput(nil), publicEntries...), apicontract.ScopedConfigurationValueInput{
		Phase: "runtime", TargetComponent: "api", Key: "DATABASE_TLS_CA_PEM_BASE64", Value: wrongCABase64, Sensitive: true,
	})
	var badConfiguration apicontract.ApplicationConfiguration
	badResponse := request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: replacementConfiguration.RevisionNumber, PlanRevisionID: plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: badEntries, Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &badConfiguration)
	if badConfiguration.RevisionNumber != replacementConfiguration.RevisionNumber+1 || badConfiguration.RevisionID == replacementConfiguration.RevisionID ||
		bytes.Contains(badResponse, []byte(wrongCABase64)) || bytes.Contains(badResponse, []byte(dbURL)) {
		t.Fatal("bad-CA revision was not saved without exposing secrets")
	}
	var failedMutation apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID: badConfiguration.RevisionID, ExpectedConfigurationRevisionNumber: badConfiguration.RevisionNumber,
	}, http.StatusAccepted, &failedMutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !failedMutation.Created || failedMutation.Job.ID == replacementJob.ID {
		t.Fatal("controller did not enqueue a distinct unhealthy replacement")
	}
	failedDeadline := time.Now().Add(8 * time.Minute)
	var failedJob jobs.Job
	for {
		failedJob, err = capacityRestart.jobs.Get(failedMutation.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if failedJob.Status == string(jobs.Failed) {
			break
		}
		if failedJob.Status == string(jobs.Succeeded) || failedJob.Status == string(jobs.WaitingUser) || failedJob.Status == string(jobs.NeedsAttention) || time.Now().After(failedDeadline) || ctx.Err() != nil {
			t.Fatalf("bad-CA replacement had unexpected result: status=%s phase=%s code=%s", failedJob.Status, failedJob.Phase, failedJob.ErrorCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if failedJob.ErrorCode != "health_failed" {
		t.Fatalf("bad-CA candidate failed outside readiness: code=%s", failedJob.ErrorCode)
	}
	var failedHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &failedHistory)
	if len(failedHistory.Items) != 3 {
		t.Fatal("failed replacement did not retain immutable deployment history")
	}
	var failedDeployment apicontract.Deployment
	for _, item := range failedHistory.Items {
		if item.JobID == failedJob.ID {
			failedDeployment = item
		}
	}
	if failedDeployment.ID == "" || failedDeployment.Status != "failed" || failedDeployment.DiagnosticCode != "health_failed" ||
		failedDeployment.ActualConfigurationRevisionID != badConfiguration.RevisionID {
		t.Fatal("failed replacement did not retain its bad-CA configuration pin")
	}
	retainedReplacementRouteURL := assertAttestedRoute(replacementDeployment)
	controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, network.Name, gateway, dbURL, sentinel)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/version", "", http.StatusOK, "controller-journey-v2")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	controllerJourneyBrowser(t, ctx, node, application.ID, retainedReplacementRouteURL, "read", browserNote)
	t.Logf("M2 controller journey identities: app=%s connection=%s plan=%s/%d config=%s/%d job=%s deployment=%s release=%s replacement-config=%s/%d capacity-pause-job=%s capacity-pause-disposition=%s capacity-pause-deployment=%s capacity-pause-release=%s capacity-pause-no-build=%t capacity-pause-serving-unchanged=%t capacity-resume-attempt=%d replacement-job=%s replacement-deployment=%s replacement-release=%s bad-config=%s/%d failed-job=%s failed-deployment=%s source=github-fixture sha=%s archive-reads=%d ingress=127.0.0.1:8080", application.ID, connection.ID, plan.RevisionID, plan.RevisionNumber, saved.RevisionID, saved.RevisionNumber, completed.ID, history.Items[0].ID, releases.Items[0].ID, replacementConfiguration.RevisionID, replacementConfiguration.RevisionNumber, pausedReplacement.ID, pausedReplacement.PauseDisposition, pausedDeployment.ID, pausedDeployment.ReleaseID, true, true, replacementJob.Attempt, replacementJob.ID, replacementDeployment.ID, replacementRelease.ID, badConfiguration.RevisionID, badConfiguration.RevisionNumber, failedJob.ID, failedDeployment.ID, controllerJourneyGitHubSHA, provider.archiveReads.Load())

	gatewayEntries := append([]apicontract.ScopedConfigurationValueInput(nil), publicEntries...)
	for index := range gatewayEntries {
		if gatewayEntries[index].Key == "API_RUNTIME_MARKER" {
			gatewayEntries[index].Value = "controller-journey-gateway-loopback"
		}
	}
	gatewayEntries = append(gatewayEntries,
		apicontract.ScopedConfigurationValueInput{Phase: "runtime", TargetComponent: "api", Key: "DATABASE_TLS_CA_PEM_BASE64", Value: caBase64, Sensitive: true},
		apicontract.ScopedConfigurationValueInput{Phase: "runtime", TargetComponent: "api", Key: "API_BIND_ADDRESS", Value: "127.0.0.1", Sensitive: false},
	)
	var gatewayConfiguration apicontract.ApplicationConfiguration
	gatewayResponse := request(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: badConfiguration.RevisionNumber, PlanRevisionID: plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: gatewayEntries, Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &gatewayConfiguration)
	if gatewayConfiguration.RevisionNumber != badConfiguration.RevisionNumber+1 || gatewayConfiguration.RevisionID == badConfiguration.RevisionID ||
		bytes.Contains(gatewayResponse, []byte(caBase64)) || bytes.Contains(gatewayResponse, []byte(dbURL)) || bytes.Contains(gatewayResponse, []byte(sentinel)) {
		t.Fatal("gateway loopback revision was not saved with restored protected configuration")
	}

	gatewayProbeTarget, err := controllerJourneyGatewayProbeTarget(application.ID)
	if err != nil {
		t.Fatal("derive expected gateway readiness target")
	}
	restartedObserver.beginGatewayReadinessCapture(gatewayProbeTarget)
	var gatewayMutation apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID: gatewayConfiguration.RevisionID, ExpectedConfigurationRevisionNumber: gatewayConfiguration.RevisionNumber,
	}, http.StatusAccepted, &gatewayMutation, map[string]string{"Idempotency-Key": uuid.NewString()})
	if !gatewayMutation.Created || gatewayMutation.Job.ID == failedJob.ID {
		t.Fatal("controller did not enqueue a distinct gateway readiness failure")
	}
	gatewayDeadline := time.Now().Add(8 * time.Minute)
	var gatewayJob jobs.Job
	for {
		gatewayJob, err = restarted.jobs.Get(gatewayMutation.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if gatewayJob.Status == string(jobs.Failed) {
			break
		}
		if gatewayJob.Status == string(jobs.Succeeded) || gatewayJob.Status == string(jobs.WaitingUser) || gatewayJob.Status == string(jobs.NeedsAttention) || time.Now().After(gatewayDeadline) || ctx.Err() != nil {
			t.Fatalf("gateway loopback replacement had unexpected result: status=%s phase=%s code=%s", gatewayJob.Status, gatewayJob.Phase, gatewayJob.ErrorCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
	apiCandidate, gatewayProbeFailed := restartedObserver.gatewayReadinessCapture()
	var gatewayHistory apicontract.DeploymentList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/deployments", nil, http.StatusOK, &gatewayHistory)
	if len(gatewayHistory.Items) != 4 {
		t.Fatal("gateway readiness failure did not retain immutable deployment history")
	}
	var gatewayDeployment apicontract.Deployment
	for _, item := range gatewayHistory.Items {
		if item.JobID == gatewayJob.ID {
			gatewayDeployment = item
		}
	}
	if gatewayDeployment.ID == "" || gatewayDeployment.Status != "failed" || gatewayDeployment.DiagnosticCode != "gateway_readiness_failed" ||
		gatewayDeployment.ActualConfigurationRevisionID != gatewayConfiguration.RevisionID || gatewayDeployment.ActualConfigurationRevisionNumber != gatewayConfiguration.RevisionNumber ||
		gatewayDeployment.DeploymentPlanRevisionID != plan.RevisionID || gatewayDeployment.DeploymentPlanRevisionNumber != plan.RevisionNumber || gatewayDeployment.ReleaseID == "" {
		t.Fatal("gateway readiness deployment did not retain its immutable pins")
	}
	if gatewayJob.ErrorCode != "gateway_readiness_failed" || apiCandidate.ID == "" ||
		apiCandidate.AppID != application.ID || apiCandidate.DeploymentID != gatewayDeployment.ID || apiCandidate.ReleaseID != gatewayDeployment.ReleaseID || !gatewayProbeFailed {
		t.Fatal("gateway loopback candidate did not fail after Docker health and the fixed Caddy probe")
	}
	var gatewayReleases apicontract.ReleaseList
	request(http.MethodGet, "/api/v1/apps/"+application.ID+"/releases", nil, http.StatusOK, &gatewayReleases)
	var gatewayRelease apicontract.Release
	for _, item := range gatewayReleases.Items {
		if item.ID == gatewayDeployment.ReleaseID {
			gatewayRelease = item
		}
	}
	if gatewayRelease.ID == "" || gatewayRelease.ArchiveSha256 != replacementRelease.ArchiveSha256 ||
		gatewayRelease.ConfigurationRevisionID != gatewayConfiguration.RevisionID || gatewayRelease.ConfigurationRevisionNumber != gatewayConfiguration.RevisionNumber ||
		gatewayRelease.DeploymentPlanRevisionID != plan.RevisionID || gatewayRelease.DeploymentPlanRevisionNumber != plan.RevisionNumber {
		t.Fatal("gateway readiness release did not retain same-source immutable pins")
	}
	retainedGatewayRouteURL := assertAttestedRoute(replacementDeployment)
	controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, network.Name, gateway, dbURL, sentinel)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/version", "", http.StatusOK, "controller-journey-v2")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
	controllerJourneyBrowser(t, ctx, node, application.ID, retainedGatewayRouteURL, "read", browserNote)
	t.Logf("M2 gateway readiness identities: gateway-config=%s/%d gateway-job=%s gateway-deployment=%s gateway-release=%s serving-deployment=%s", gatewayConfiguration.RevisionID, gatewayConfiguration.RevisionNumber, gatewayJob.ID, gatewayDeployment.ID, gatewayRelease.ID, replacementDeployment.ID)
}

func controllerJourneyExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("required hosted executable %s is unavailable", name)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func controllerJourneyTrackedFixtureFiles(installed string) (map[string]struct{}, error) {
	repositoryRoot := filepath.Dir(filepath.Dir(installed))
	relative, err := filepath.Rel(repositoryRoot, installed)
	if err != nil || filepath.ToSlash(relative) != "examples/hosting-notes" {
		return nil, fmt.Errorf("fixture source path is unexpected")
	}
	if err := exec.Command("git", "-C", repositoryRoot, "diff", "--quiet", "HEAD", "--", "examples/hosting-notes").Run(); err != nil {
		return nil, fmt.Errorf("fixture tracked files differ from HEAD")
	}
	listed, err := exec.Command("git", "-C", repositoryRoot, "ls-files", "-z", "--", "examples/hosting-notes").Output()
	if err != nil {
		return nil, fmt.Errorf("could not enumerate committed fixture files")
	}
	allowed := make(map[string]struct{})
	for _, entry := range bytes.Split(listed, []byte{0}) {
		name := strings.TrimPrefix(string(entry), "examples/hosting-notes/")
		if name != "" && name != string(entry) {
			allowed[name] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("committed fixture has no files")
	}
	return allowed, nil
}

func controllerJourneyStageSource(installed, destination string, allowed map[string]struct{}) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	seen := 0
	err := filepath.WalkDir(installed, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(installed, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		canonical := filepath.ToSlash(relative)
		if entry.IsDir() && (entry.Name() == "node_modules" || canonical == "frontend/dist" || canonical == "harness/certs") {
			return filepath.SkipDir
		}
		if entry.IsDir() && allowed != nil {
			containsTrackedFile := false
			for name := range allowed {
				if strings.HasPrefix(name, canonical+"/") {
					containsTrackedFile = true
					break
				}
			}
			if !containsTrackedFile {
				return filepath.SkipDir
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe fixture entry %s", canonical)
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if allowed != nil {
			if _, ok := allowed[canonical]; !ok {
				return nil
			}
			seen++
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutputErr != nil {
			return closeOutputErr
		}
		return closeInputErr
	})
	if err != nil {
		return err
	}
	if allowed != nil && seen != len(allowed) {
		return fmt.Errorf("committed fixture files are missing")
	}
	return nil
}

func TestControllerJourneyStageSource(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "installed")
	for _, directory := range []string{"api/src", "api/node_modules", "frontend/dist", "harness/certs"} {
		if err := os.MkdirAll(filepath.Join(installed, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"api/src/server.js": "export default true", "api/node_modules/package.js": "installed",
		"frontend/dist/index.html": "generated", "harness/certs/ca.pem": "private",
	} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	staged := filepath.Join(t.TempDir(), "source")
	if err := controllerJourneyStageSource(installed, staged, nil); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(staged, "api/src/server.js")); err != nil || string(body) != "export default true" {
		t.Fatal("source file was not staged")
	}
	for _, excluded := range []string{"api/node_modules", "frontend/dist", "harness/certs"} {
		if _, err := os.Lstat(filepath.Join(staged, excluded)); !os.IsNotExist(err) {
			t.Fatalf("generated fixture directory %s was staged", excluded)
		}
	}
	if err := os.WriteFile(filepath.Join(installed, "api", ".env"), []byte("untracked secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	trackedStage := filepath.Join(t.TempDir(), "tracked-source")
	if err := controllerJourneyStageSource(installed, trackedStage, map[string]struct{}{"api/src/server.js": {}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(trackedStage, "api", ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("untracked fixture secret was staged")
	}
	if err := os.Symlink(filepath.Join(installed, "api/src/server.js"), filepath.Join(installed, "unexpected-link")); err == nil {
		if err := controllerJourneyStageSource(installed, filepath.Join(t.TempDir(), "rejected"), nil); err == nil {
			t.Fatal("unexpected source link was accepted")
		}
	}
}

// Record only Docker's component health transitions. Runtime command output
// and healthcheck logs may contain application secrets and are never logged.
type controllerJourneyHealthObserver struct {
	delegate                runtimeprocess.CommandRunner
	test                    *testing.T
	mu                      sync.Mutex
	last                    map[string]string
	builds                  int
	captureGatewayReadiness bool
	expectedGatewayTarget   string
	apiCandidate            controllerJourneyCandidate
	gatewayProbeFailed      bool
}

type controllerJourneyCandidate struct {
	ID           string
	AppID        string
	DeploymentID string
	ReleaseID    string
}

func (observer *controllerJourneyHealthObserver) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] == "build" {
		observer.mu.Lock()
		observer.builds++
		observer.mu.Unlock()
	}
	result, err := observer.delegate.Run(ctx, request)
	observer.mu.Lock()
	if observer.captureGatewayReadiness && controllerJourneyGatewayProbe(request, observer.expectedGatewayTarget) {
		if err != nil || result.StdoutTruncated || result.StderrTruncated {
			observer.gatewayProbeFailed = true
		}
	}
	observer.mu.Unlock()
	if err != nil || len(request.Args) < 4 || request.Args[0] != "container" || request.Args[1] != "inspect" {
		return result, err
	}
	var state struct {
		ID       string            `json:"id"`
		Name     string            `json:"name"`
		Labels   map[string]string `json:"labels"`
		Running  bool              `json:"running"`
		ExitCode int               `json:"exitCode"`
		Health   string            `json:"health"`
	}
	if json.Unmarshal(result.Stdout, &state) != nil || state.Labels["io.rig.managed"] != "generated-runtime" {
		return result, err
	}
	status := fmt.Sprintf("running=%t health=%s exit=%d", state.Running, state.Health, state.ExitCode)
	observer.mu.Lock()
	previous := observer.last[state.Name]
	observer.last[state.Name] = status
	if observer.captureGatewayReadiness && state.Labels["io.rig.component"] == "api" && state.Running && state.Health == "healthy" && state.ID != "" {
		observer.apiCandidate = controllerJourneyCandidate{
			ID: state.ID, AppID: state.Labels["io.rig.application"],
			DeploymentID: state.Labels["io.rig.deployment"], ReleaseID: state.Labels["io.rig.release"],
		}
	}
	observer.mu.Unlock()
	if previous != status {
		observer.test.Logf("Docker candidate %s %s", state.Labels["io.rig.component"], status)
		if state.Labels["io.rig.component"] == "frontend" && !state.Running && state.ExitCode != 0 {
			logs, logErr := observer.delegate.Run(ctx, runtimeprocess.CommandRequest{
				Executable: request.Executable, Args: []string{"container", "logs", request.Args[len(request.Args)-1]},
				Directory: request.Directory, Env: request.Env, Timeout: 3 * time.Second, OutputLimit: 8 << 10,
			})
			if logErr == nil {
				// Only fixed error categories enter the public CI log. The raw
				// output belongs to the application and may contain secrets.
				combined := append(logs.Stdout, logs.Stderr...)
				for _, marker := range []string{"ERR_MODULE_NOT_FOUND", "MODULE_NOT_FOUND", "ENOENT", "EACCES", "EPERM", "SyntaxError", "ReferenceError", "TypeError", "Permission denied", "not found"} {
					if bytes.Contains(combined, []byte(marker)) {
						observer.test.Logf("static frontend startup category: %s", marker)
					}
				}
				for _, code := range regexp.MustCompile(`\bERR_[A-Z_]{3,64}\b`).FindAll(combined, 3) {
					observer.test.Logf("static frontend Node error code: %s", code)
				}
				for _, match := range regexp.MustCompile(`Cannot find module ['"]([^'"\r\n]{1,256})['"]`).FindAllSubmatch(combined, 2) {
					missing := string(match[1])
					if strings.HasPrefix(missing, "/workspace/") || strings.HasPrefix(missing, "/usr/local/lib/rig/") {
						observer.test.Logf("static frontend missing module basename: %s", filepath.Base(missing))
						if strings.HasPrefix(missing, "/usr/local/lib/rig/") {
							observer.test.Log("static frontend missing module location: Rig runtime library")
							copyResult, copyErr := observer.delegate.Run(ctx, runtimeprocess.CommandRequest{
								Executable: request.Executable,
								Args:       []string{"container", "cp", request.Args[len(request.Args)-1] + ":/usr/local/lib/rig", "-"},
								Directory:  request.Directory, Env: request.Env, Timeout: 3 * time.Second, OutputLimit: 32 << 10,
							})
							if copyErr == nil && !copyResult.StdoutTruncated {
								contents := tar.NewReader(bytes.NewReader(copyResult.Stdout))
								for {
									entry, err := contents.Next()
									if err != nil {
										break
									}
									basename := filepath.Base(strings.TrimSuffix(entry.Name, "/"))
									if basename == "rig" || basename == "static.mjs" {
										observer.test.Logf("static frontend runtime library %s mode=%#o uid=%d type=%d", basename, entry.Mode, entry.Uid, entry.Typeflag)
									}
								}
							}
							clear(copyResult.Stdout)
							clear(copyResult.Stderr)
						} else {
							observer.test.Log("static frontend missing module location: application workspace")
						}
					}
				}
				clear(combined)
				clear(logs.Stdout)
				clear(logs.Stderr)
			}
		}
	}
	return result, err
}

func (observer *controllerJourneyHealthObserver) BuildCalls() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.builds
}

type controllerJourneyCapacitySource struct {
	mu       sync.RWMutex
	delegate generatedruntime.CapacitySource
	armed    bool
}

func (source *controllerJourneyCapacitySource) SetDelegate(delegate generatedruntime.CapacitySource) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.delegate = delegate
}

func (source *controllerJourneyCapacitySource) SetArmed(armed bool) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.armed = armed
}

func (source *controllerJourneyCapacitySource) Snapshot(ctx context.Context) (generatedruntime.CapacitySnapshot, error) {
	source.mu.RLock()
	armed, delegate := source.armed, source.delegate
	source.mu.RUnlock()
	if armed {
		return generatedruntime.CapacitySnapshot{}, nil
	}
	if delegate == nil {
		return generatedruntime.CapacitySnapshot{}, errors.New("capacity source delegate is required")
	}
	return delegate.Snapshot(ctx)
}

func (observer *controllerJourneyHealthObserver) beginGatewayReadinessCapture(expectedTarget string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.captureGatewayReadiness = true
	observer.expectedGatewayTarget = expectedTarget
	observer.apiCandidate = controllerJourneyCandidate{}
	observer.gatewayProbeFailed = false
}

func (observer *controllerJourneyHealthObserver) gatewayReadinessCapture() (apiCandidate controllerJourneyCandidate, gatewayProbeFailed bool) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.captureGatewayReadiness = false
	observer.expectedGatewayTarget = ""
	return observer.apiCandidate, observer.gatewayProbeFailed
}

// controllerJourneyGatewayProbe recognizes the fixed, body-discarding Caddy
// readiness command against a controller-derived target. It retains neither
// command output nor application data.
func controllerJourneyGatewayProbe(request runtimeprocess.CommandRequest, expectedTarget string) bool {
	if len(request.Args) != 21 || request.Args[0] != "container" || request.Args[1] != "exec" || request.Args[3] != "curl" {
		return false
	}
	fixed := []string{
		"curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
	}
	for index, expected := range fixed {
		if request.Args[index+3] != expected {
			return false
		}
	}
	return expectedTarget != "" && request.Args[len(request.Args)-1] == expectedTarget
}

func controllerJourneyGatewayProbeTarget(appID string) (string, error) {
	network, err := generatedruntime.DescribeAppNetwork(appID)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("api"))
	alias := "rig-c-" + fmt.Sprintf("%x", digest)[:12] + "-blue"
	return "http://" + alias + "." + network.Name + ":3000/", nil
}

func controllerJourneyDocker(ctx context.Context, docker string, extraEnv []string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, docker, args...)
	command.Env = append(os.Environ(), extraEnv...)
	return command.CombinedOutput()
}

func controllerJourneyDockerIDSet(t *testing.T, ctx context.Context, docker string, args ...string) string {
	t.Helper()
	output, err := controllerJourneyDocker(ctx, docker, nil, args...)
	if err != nil {
		t.Fatal("list owned Docker resource identities")
	}
	identities := strings.Fields(string(output))
	sort.Strings(identities)
	return strings.Join(identities, ",")
}

func controllerJourneyExists(t *testing.T, ctx context.Context, docker, kind, name string) bool {
	t.Helper()
	commands := map[string][]string{
		"container": {"container", "ls", "--all", "--format", "{{.Names}}"},
		"volume":    {"volume", "ls", "--format", "{{.Name}}"},
		"network":   {"network", "ls", "--format", "{{.Name}}"},
	}
	args, ok := commands[kind]
	if !ok {
		t.Fatalf("unsupported resource kind %s", kind)
	}
	output, err := controllerJourneyDocker(ctx, docker, nil, args...)
	if err != nil {
		t.Fatalf("list Docker %s", kind)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
		if string(bytes.TrimSpace(line)) == name {
			return true
		}
	}
	return false
}

func controllerJourneyFixtureControllerListenAddress(apiURL string) (string, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid fixture controller URL")
	}
	configured, err := config.FromFlags([]string{"--listen", parsed.Host})
	if err != nil || configured.ListenAddress != parsed.Host {
		return "", errors.New("fixture controller listener is not an explicit loopback address")
	}
	host, _, err := net.SplitHostPort(configured.ListenAddress)
	if err != nil {
		return "", errors.New("invalid fixture controller listener")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("fixture controller listener is not loopback")
	}
	return configured.ListenAddress, nil
}

func controllerJourneyListenPort(address string) (int, error) {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0, errors.New("invalid controller listener")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid controller listener port")
	}
	return port, nil
}

func controllerJourneyPort(t *testing.T, address string, excludedPorts ...int) int {
	t.Helper()
	excluded := make(map[int]struct{}, len(excludedPorts))
	for _, port := range excludedPorts {
		excluded[port] = struct{}{}
	}
	for attempts := 0; attempts < 64; attempts++ {
		listener, err := net.Listen("tcp4", net.JoinHostPort(address, "0"))
		if err != nil {
			t.Fatal("reserve fixture listener port")
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal("release fixture listener port")
		}
		if _, found := excluded[port]; !found {
			return port
		}
	}
	t.Fatal("could not reserve a fixture port disjoint from the controller listener")
	return 0
}

func controllerJourneyRestartControllerServer(t *testing.T, handler http.Handler, excludedPorts ...int) *httptest.Server {
	t.Helper()
	excluded := make(map[int]struct{}, len(excludedPorts))
	for _, port := range excludedPorts {
		excluded[port] = struct{}{}
	}
	for attempts := 0; attempts < 16; attempts++ {
		server := httptest.NewServer(handler)
		address, err := controllerJourneyFixtureControllerListenAddress(server.URL)
		if err != nil {
			server.Close()
			t.Fatal("validate restarted fixture controller listener")
		}
		port, err := controllerJourneyListenPort(address)
		if err != nil {
			server.Close()
			t.Fatal("read restarted fixture controller listener port")
		}
		if _, found := excluded[port]; !found {
			return server
		}
		server.Close()
	}
	t.Fatal("could not start a controller listener disjoint from external fixture services")
	return nil
}

func controllerJourneyBrowser(t *testing.T, ctx context.Context, node, appID, routeURL, mode, note string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "web", "scripts", "verify-hosted-notes.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, node, script, appID, "8080", mode, note, routeURL)
	command.Dir = filepath.Dir(script)
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("hosted Chromium "+mode+" passed")) {
		t.Fatalf("deployed Chromium %s journey failed: %v", mode, err)
	}
	t.Logf("deployed Chromium %s journey passed", mode)
}

func controllerJourneyWrongCA(t *testing.T, ctx context.Context, openssl, fixtureRoot string) []byte {
	t.Helper()
	key := filepath.Join(fixtureRoot, "wrong-ca.key")
	certificate := filepath.Join(fixtureRoot, "wrong-ca.crt")
	command := exec.CommandContext(ctx, openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
		"-keyout", key, "-out", certificate, "-subj", "/CN=Wrong disposable fixture CA")
	if err := command.Run(); err != nil {
		t.Fatal("generate unrelated disposable TLS CA")
	}
	body, err := os.ReadFile(certificate)
	if err != nil {
		t.Fatal("read unrelated disposable TLS CA")
	}
	return body
}

func controllerJourneyPrepareSchema(t *testing.T, ctx context.Context, node, source, dbURL, ca string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
		command := exec.CommandContext(attempt, node, "src/prepare-schema.js")
		command.Dir = filepath.Join(source, "api")
		command.Env = append(os.Environ(), "DATABASE_URL_ENV=NOTES_FIXTURE_DB_URL", "NOTES_FIXTURE_DB_URL="+dbURL, "DATABASE_TLS_CA_PEM_BASE64="+ca)
		err := command.Run()
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatal("application-owned schema preparation did not connect to the verified TLS fixture")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func controllerJourneyRoutedRequest(t *testing.T, ctx context.Context, appID, method, path, body string, want int, fragment string) []byte {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = appID + ".rig.localhost"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("generated Caddy route unavailable")
	}
	defer response.Body.Close()
	limit := int64(8 << 10)
	if strings.HasPrefix(path, "/assets/") {
		limit = 4 << 20
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(content)) > limit || response.StatusCode != want || !bytes.Contains(content, []byte(fragment)) {
		t.Fatalf("routed %s %s: status=%d want=%d fragment-present=%t", method, path, response.StatusCode, want, bytes.Contains(content, []byte(fragment)))
	}
	return content
}

func controllerJourneyRoutedStatus(ctx context.Context, appID, path string) int {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080"+path, nil)
	if err != nil {
		return 0
	}
	request.Host = appID + ".rig.localhost"
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	return response.StatusCode
}

func controllerJourneyContainerHealth(ctx context.Context, docker, containerID string) string {
	output, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{end}}", containerID)
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(output))
}

func controllerJourneyProblemCode(body []byte) string {
	var problem struct{ Code string }
	if json.Unmarshal(body, &problem) != nil {
		return "unreadable"
	}
	return problem.Code
}

type controllerJourneyHTTPProbeTarget struct {
	Gateway string
	Port    int
	Path    string
}

func controllerJourneyControllerProbeTarget(apiURL, gateway, appID string) (controllerJourneyHTTPProbeTarget, error) {
	listenAddress, err := controllerJourneyFixtureControllerListenAddress(apiURL)
	if err != nil {
		return controllerJourneyHTTPProbeTarget{}, err
	}
	port, err := controllerJourneyListenPort(listenAddress)
	if err != nil {
		return controllerJourneyHTTPProbeTarget{}, err
	}
	gatewayIP := net.ParseIP(gateway)
	if gatewayIP == nil || gatewayIP.To4() == nil || gatewayIP.IsLoopback() || gatewayIP.IsUnspecified() {
		return controllerJourneyHTTPProbeTarget{}, errors.New("invalid application bridge gateway")
	}
	parsedAppID, err := uuid.Parse(appID)
	if err != nil || parsedAppID.String() != appID {
		return controllerJourneyHTTPProbeTarget{}, errors.New("invalid application identity")
	}
	return controllerJourneyHTTPProbeTarget{
		Gateway: gatewayIP.To4().String(),
		Port:    port,
		Path:    "/api/v1/apps/" + appID + "/deployments",
	}, nil
}

type controllerJourneyAppProbeSource struct {
	ContainerID string
	Component   string
}

type controllerJourneyAppProbeInspection struct {
	ID               string                                        `json:"id"`
	Labels           map[string]string                             `json:"labels"`
	Running          bool                                          `json:"running"`
	NetworkMode      string                                        `json:"networkMode"`
	PIDMode          string                                        `json:"pidMode"`
	IPCMode          string                                        `json:"ipcMode"`
	Binds            []string                                      `json:"binds"`
	Mounts           []controllerJourneyMountInspection            `json:"mounts"`
	ConfiguredMounts []controllerJourneyMountInspection            `json:"configuredMounts"`
	Networks         map[string]controllerJourneyNetworkAttachment `json:"networks"`
}

type controllerJourneyMountInspection struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Target      string `json:"Target"`
}

type controllerJourneyNetworkAttachment struct {
	NetworkID string `json:"NetworkID"`
	IPAddress string `json:"IPAddress"`
	Gateway   string `json:"Gateway"`
}

const controllerJourneyAppProbeInspectFormat = `{"id":{{json .ID}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"networkMode":{{json .HostConfig.NetworkMode}},"pidMode":{{json .HostConfig.PidMode}},"ipcMode":{{json .HostConfig.IpcMode}},"binds":{{json .HostConfig.Binds}},"mounts":{{json .Mounts}},"configuredMounts":{{json .HostConfig.Mounts}},"networks":{{if .NetworkSettings}}{{json .NetworkSettings.Networks}}{{else}}null{{end}}}`

const controllerJourneyAppNetworkInspectFormat = `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Labels}}}`

func controllerJourneyAppProbeSources(t *testing.T, ctx context.Context, docker, appID, networkName, gateway string) []controllerJourneyAppProbeSource {
	t.Helper()
	networkBody, err := controllerJourneyDocker(ctx, docker, nil, "network", "inspect", "--format", controllerJourneyAppNetworkInspectFormat, networkName)
	var network struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	networkDecodeErr := json.Unmarshal(bytes.TrimSpace(networkBody), &network)
	clear(networkBody)
	if err != nil || networkDecodeErr != nil || network.ID == "" || network.Name != networkName ||
		network.Labels["io.rig.managed"] != generatedruntime.NetworkOwnershipLabelValue || network.Labels["io.rig.application"] != appID {
		t.Fatal("app-private network identity is not attested")
	}
	ids, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-q", "--no-trunc", "--filter", "label=io.rig.application="+appID)
	if err != nil || len(strings.Fields(string(ids))) != 2 {
		clear(ids)
		t.Fatal("generated deployment did not start exactly two scoped components")
	}
	containerIDs := strings.Fields(string(ids))
	clear(ids)
	seen := make(map[string]bool, len(containerIDs))
	sources := make([]controllerJourneyAppProbeSource, 0, len(containerIDs))
	for _, id := range containerIDs {
		body, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", controllerJourneyAppProbeInspectFormat, id)
		var inspection controllerJourneyAppProbeInspection
		decodeErr := json.Unmarshal(bytes.TrimSpace(body), &inspection)
		clear(body)
		component := inspection.Labels["io.rig.component"]
		attachment, attached := inspection.Networks[networkName]
		address := net.ParseIP(attachment.IPAddress)
		if err != nil || decodeErr != nil || !controllerJourneyCanonicalContainerID(id) || inspection.ID != id || !inspection.Running || inspection.NetworkMode != networkName ||
			!controllerJourneyPrivateNamespaceMode(inspection.PIDMode) || !controllerJourneyPrivateNamespaceMode(inspection.IPCMode) || len(inspection.Binds) != 0 ||
			!controllerJourneyMountsArePrivate(inspection.Mounts) || !controllerJourneyMountsArePrivate(inspection.ConfiguredMounts) ||
			len(inspection.Networks) != 1 || !attached || attachment.NetworkID != network.ID || attachment.Gateway != gateway || address == nil || address.IsLoopback() ||
			inspection.Labels["io.rig.managed"] != "generated-runtime" || inspection.Labels["io.rig.application"] != appID || seen[component] ||
			(component != "api" && component != "frontend") {
			t.Fatal("generated app probe source is not an owned isolated running component")
		}
		seen[component] = true
		sources = append(sources, controllerJourneyAppProbeSource{ContainerID: id, Component: component})
	}
	if !seen["api"] || !seen["frontend"] {
		t.Fatal("generated app probe sources do not cover both components")
	}
	sort.Slice(sources, func(left, right int) bool { return sources[left].Component < sources[right].Component })
	return sources
}

func controllerJourneyMountsArePrivate(mounts []controllerJourneyMountInspection) bool {
	for _, mount := range mounts {
		if !strings.EqualFold(mount.Type, "tmpfs") || strings.TrimSpace(mount.Source) != "" || controllerJourneyDockerSocketMount(mount.Source) ||
			controllerJourneyDockerSocketMount(mount.Destination) || controllerJourneyDockerSocketMount(mount.Target) {
			return false
		}
	}
	return true
}

func controllerJourneyPrivateNamespaceMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "private":
		return true
	default:
		return false
	}
}

func controllerJourneyCanonicalContainerID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func controllerJourneyDockerSocketMount(value string) bool {
	value = strings.TrimSuffix(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	return value == "/var/run/docker.sock" || value == "//./pipe/docker_engine"
}

const controllerJourneyAppControllerProbeScript = `const http=require("node:http");const host=process.argv[1],port=Number(process.argv[2]),path=process.argv[3];let reported=false;function report(value){if(reported)return;reported=true;process.stdout.write(JSON.stringify(value));}const request=http.request({host,port,path,method:"GET",headers:{connection:"close"},agent:false,timeout:1500},response=>{report({outcome:"response",status:response.statusCode});response.resume();});request.once("timeout",()=>request.destroy(Object.assign(new Error("timeout"),{code:"ETIMEDOUT"})));request.once("error",error=>report({outcome:"denied",code:typeof error.code==="string"?error.code:""}));request.end();`

type controllerJourneyAppProbeResult struct {
	Outcome string `json:"outcome"`
	Status  int    `json:"status"`
	Code    string `json:"code"`
}

func controllerJourneyClassifyAppControllerProbe(output []byte) (bool, error) {
	if len(output) == 0 || len(output) > 512 {
		return false, errors.New("invalid controller probe output")
	}
	var result controllerJourneyAppProbeResult
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false, errors.New("invalid controller probe output")
	}
	if result.Outcome == "denied" && result.Status == 0 && controllerJourneyTransportDenied(result.Code) {
		return true, nil
	}
	if result.Outcome == "response" && result.Status >= 100 && result.Status <= 599 && result.Code == "" {
		return false, errors.New("controller probe received an HTTP response")
	}
	return false, errors.New("unknown controller probe result")
}

func controllerJourneyTransportDenied(code string) bool {
	switch code {
	case "ECONNREFUSED", "ECONNRESET", "EHOSTUNREACH", "ENETUNREACH", "ETIMEDOUT":
		return true
	default:
		return false
	}
}

func TestControllerJourneyControllerProbeTargetUsesLoopbackControllerPort(t *testing.T) {
	const appID = "11111111-1111-4111-8111-111111111111"
	target, err := controllerJourneyControllerProbeTarget("http://127.0.0.1:7345", "172.27.0.1", appID)
	if err != nil || target.Gateway != "172.27.0.1" || target.Port != 7345 || target.Path != "/api/v1/apps/"+appID+"/deployments" {
		t.Fatal("controller probe target did not preserve the validated controller port and read-only path")
	}
	for _, value := range []struct {
		apiURL  string
		gateway string
		appID   string
	}{
		{apiURL: "http://localhost:7345", gateway: "172.27.0.1", appID: appID},
		{apiURL: "http://0.0.0.0:7345", gateway: "172.27.0.1", appID: appID},
		{apiURL: "http://127.0.0.1:7345", gateway: "controller.local", appID: appID},
		{apiURL: "http://127.0.0.1:7345", gateway: "127.0.0.1", appID: appID},
		{apiURL: "http://127.0.0.1:7345", gateway: "172.27.0.1", appID: "invalid"},
	} {
		if _, err := controllerJourneyControllerProbeTarget(value.apiURL, value.gateway, value.appID); err == nil {
			t.Fatal("controller probe target accepted an untrusted endpoint input")
		}
	}
}

func TestControllerJourneyAppControllerProbeClassifierFailsClosed(t *testing.T) {
	for _, value := range []struct {
		result  string
		blocked bool
		valid   bool
	}{
		{result: `{"outcome":"denied","code":"ECONNREFUSED"}`, blocked: true, valid: true},
		{result: `{"outcome":"response","status":200}`, valid: false},
		{result: `{"outcome":"response","status":401}`, valid: false},
		{result: `{"outcome":"denied","code":"EUNKNOWN"}`, valid: false},
		{result: `{"outcome":"denied","code":"ECONNREFUSED","extra":true}`, valid: false},
		{result: `not-json`, valid: false},
	} {
		blocked, err := controllerJourneyClassifyAppControllerProbe([]byte(value.result))
		if (err == nil) != value.valid || blocked != value.blocked {
			t.Fatal("controller probe classifier accepted an invalid result or misclassified reachability")
		}
	}
}

func TestControllerJourneyAppProbeAttestationBoundaries(t *testing.T) {
	if !controllerJourneyCanonicalContainerID(strings.Repeat("a", 64)) || controllerJourneyCanonicalContainerID(strings.Repeat("a", 12)) ||
		controllerJourneyCanonicalContainerID(strings.Repeat("A", 64)) {
		t.Fatal("app probe source ID validation accepted an ambiguous container identity")
	}
	for _, value := range []struct {
		mode string
		ok   bool
	}{
		{mode: "", ok: true}, {mode: "private", ok: true}, {mode: "host"}, {mode: "container:abc"}, {mode: "shareable"},
	} {
		if controllerJourneyPrivateNamespaceMode(value.mode) != value.ok {
			t.Fatal("app probe namespace mode validation accepted a shared namespace")
		}
	}
	for _, value := range []struct {
		mounts []controllerJourneyMountInspection
		ok     bool
	}{
		{ok: true},
		{mounts: []controllerJourneyMountInspection{{Type: "tmpfs", Destination: "/tmp"}}, ok: true},
		{mounts: []controllerJourneyMountInspection{{Type: "bind", Source: "/host", Destination: "/tmp"}}},
		{mounts: []controllerJourneyMountInspection{{Type: "volume", Source: "runtime-state", Destination: "/tmp"}}},
		{mounts: []controllerJourneyMountInspection{{Type: "tmpfs", Source: "/host", Destination: "/tmp"}}},
		{mounts: []controllerJourneyMountInspection{{Type: "tmpfs", Destination: "/var/run/docker.sock"}}},
	} {
		if controllerJourneyMountsArePrivate(value.mounts) != value.ok {
			t.Fatal("app probe mount validation accepted a non-private mount")
		}
	}
}

func controllerJourneyAssertAppControllerDenied(t *testing.T, ctx context.Context, docker, directory string, sources []controllerJourneyAppProbeSource, target controllerJourneyHTTPProbeTarget) {
	t.Helper()
	if len(sources) != 2 || target.Gateway == "" || target.Port < 1 || target.Port > 65535 || target.Path == "" {
		t.Fatal("controller isolation probe has no attested inputs")
	}
	for _, source := range sources {
		result, err := (runtimeprocess.ExecRunner{}).Run(ctx, runtimeprocess.CommandRequest{
			Executable: docker,
			Args: []string{
				"container", "exec", "--env", "HTTP_PROXY=", "--env", "HTTPS_PROXY=", "--env", "ALL_PROXY=", "--env", "NO_PROXY=*",
				source.ContainerID, "node", "--no-warnings", "-e", controllerJourneyAppControllerProbeScript,
				target.Gateway, strconv.Itoa(target.Port), target.Path,
			},
			Directory: directory, Env: os.Environ(), Timeout: 4 * time.Second, OutputLimit: 512,
		})
		if ctx.Err() != nil || err != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
			clear(result.Stdout)
			clear(result.Stderr)
			t.Fatalf("app-origin controller probe did not complete for %s", source.Component)
		}
		blocked, classifyErr := controllerJourneyClassifyAppControllerProbe(result.Stdout)
		clear(result.Stdout)
		clear(result.Stderr)
		if classifyErr != nil || !blocked {
			t.Fatalf("controller API was reachable or probe output was invalid from %s", source.Component)
		}
	}
}

func controllerJourneyAssertScopedContainers(t *testing.T, ctx context.Context, docker, appID, networkName, gateway, dbURL, sentinel string) []controllerJourneyAppProbeSource {
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
		entries := strings.Join(configuration.Env, "\n")
		if source.Component == "api" {
			if !strings.Contains(entries, "NOTES_FIXTURE_DB_URL="+dbURL) || !strings.Contains(entries, "TEST_SENTINEL_SECRET="+sentinel) {
				t.Fatal("API container did not receive its exact scoped server secrets")
			}
		} else if strings.Contains(entries, dbURL) || strings.Contains(entries, sentinel) || strings.Contains(entries, "NOTES_FIXTURE_DB_URL=") {
			t.Fatal("frontend container received a server runtime secret")
		}
	}
	return sources
}

func controllerJourneyRemoveBuilder(t *testing.T, ctx context.Context, docker, dataRoot string) {
	t.Helper()
	root := filepath.Join(dataRoot, "runtime", "generated-builder")
	body, err := os.ReadFile(filepath.Join(root, "builder-identity-v2.json"))
	if os.IsNotExist(err) {
		return
	}
	var identity struct {
		BuilderName string `json:"builderName"`
		NodeName    string `json:"nodeName"`
		NetworkName string `json:"networkName"`
	}
	suffix := strings.TrimPrefix(identity.BuilderName, "rig-buildkit-")
	if err == nil {
		err = json.Unmarshal(body, &identity)
		suffix = strings.TrimPrefix(identity.BuilderName, "rig-buildkit-")
	}
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(suffix) ||
		identity.NodeName != "rig-node-"+suffix || identity.NetworkName != "rig-buildnet-"+suffix {
		t.Error("generated builder identity is uncertain; preserving resources")
		return
	}
	containerName := "rig-buildkitd-" + strings.TrimPrefix(identity.NodeName, "rig-node-")
	builderEnv := []string{
		"DOCKER_CONFIG=" + filepath.Join(root, "docker-config"),
		"BUILDX_CONFIG=" + filepath.Join(root, "buildx-config"),
	}
	if _, err := controllerJourneyDocker(ctx, docker, builderEnv, "buildx", "rm", "--force", identity.BuilderName); err != nil {
		t.Error("remove exact generated Buildx record")
	}
	if _, err := controllerJourneyDocker(ctx, docker, builderEnv, "buildx", "inspect", identity.BuilderName); err == nil {
		t.Error("generated Buildx record remains after cleanup")
	}
	for _, resource := range [][]string{{"container", containerName}, {"network", identity.NetworkName}} {
		if !controllerJourneyExists(t, ctx, docker, resource[0], resource[1]) {
			continue
		}
		format := "{{json .Labels}}"
		if resource[0] == "container" {
			format = "{{json .Config.Labels}}"
		}
		labelsBody, err := controllerJourneyDocker(ctx, docker, nil, resource[0], "inspect", "--format", format, resource[1])
		var labels map[string]string
		if err != nil || json.Unmarshal(bytes.TrimSpace(labelsBody), &labels) != nil ||
			labels["rig.controller"] != "generated-builder" ||
			labels["rig.builder"] != identity.BuilderName ||
			labels["rig.network"] != identity.NetworkName {
			t.Errorf("generated builder %s ownership uncertain; preserving", resource[0])
			continue
		}
		removal := []string{resource[0], "rm", resource[1]}
		if resource[0] == "container" {
			removal = []string{resource[0], "rm", "--force", resource[1]}
		}
		if _, err := controllerJourneyDocker(ctx, docker, nil, removal...); err != nil {
			t.Errorf("remove exact generated builder %s", resource[0])
		}
	}
}
