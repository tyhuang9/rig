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
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const (
	controllerJourneyProject = "rig-controller-journey-test"
	controllerJourneyNetwork = "rig-controller-journey-external"
)

// This hosted gate exercises the real controller API, durable worker, compiler,
// runtime, and ingress. The source is a local immutable release snapshot; the
// controlled GitHub archive/connection journey remains a separate M2 gate.
func TestLiveControllerGeneratedDeploymentJourney(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_CONTROLLER_JOURNEY") != "1" {
		t.Fatal("set RIG_RUN_LIVE_CONTROLLER_JOURNEY=1 to run the hosted Docker gate")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("hosted controller gate requires the local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	node := controllerJourneyExecutable(t, "node")
	sh := controllerJourneyExecutable(t, "sh")
	openssl := controllerJourneyExecutable(t, "openssl")
	source, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(source, "api", "node_modules", "pg")); err != nil || !info.IsDir() {
		t.Fatal("install the hosting fixture with its frozen lockfile first")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "controller")
	fixtureRoot := filepath.Join(root, "external")
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Minute)
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
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), nil, sourceconnections.NewFileCredentialStore(dataRoot), "", time.Now)
	snapshots, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentStore := deployments.New(db)
	settings := config.Defaults()
	settings.DataRoot = dataRoot
	settings.GeneratedRuntime = true
	composition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: appStore, snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans,
	}, runtimeCompositionOptions{dockerExecutable: docker})
	if err != nil {
		t.Fatal("compose production generated runtime:", err)
	}
	handler := (&controller.Server{
		Auth: authService, Apps: appStore, Jobs: jobStore, Machines: machineStore, Sources: sources,
		Configuration: configuration, Deployments: deploymentStore, DeploymentPlans: plans,
		GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()
	api := httptest.NewServer(handler)
	defer api.Close()
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
	var inspection apicontract.InspectResponse
	request(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{SourcePath: source, Setup: &setup}, http.StatusOK, &inspection)
	if len(inspection.Analysis.Candidates) == 0 || inspection.Analysis.StructuralFingerprint == "" {
		t.Fatal("reviewed source inspection returned no candidate")
	}
	candidate := inspection.Analysis.Candidates[len(inspection.Analysis.Candidates)-1]
	var application apicontract.Application
	request(http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{Name: "Controller Journey", SourcePath: source, Setup: &setup}, http.StatusCreated, &application)
	if application.ID == "" || application.Source.Type != "local" {
		t.Fatal("controller did not save the selected local source")
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
	dbPort := controllerJourneyPort(t, gateway)
	httpsPort := controllerJourneyPort(t, gateway)
	for httpsPort == dbPort {
		httpsPort = controllerJourneyPort(t, gateway)
	}
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
	controllerJourneyPrepareSchema(t, ctx, node, source, dbURL, caBase64)
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
	idempotencyKey := uuid.NewString()
	request(http.MethodPost, deploymentPath, map[string]any{}, http.StatusAccepted, &mutation, map[string]string{"Idempotency-Key": idempotencyKey})
	if !mutation.Created || mutation.Job.ID == "" || mutation.Job.RequestedBy != user.ID {
		t.Fatal("controller did not enqueue an actor-bound durable job")
	}
	var replay apicontract.JobMutationResponse
	request(http.MethodPost, deploymentPath, map[string]any{}, http.StatusOK, &replay, map[string]string{"Idempotency-Key": idempotencyKey})
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
		releases.Items[0].SourceProvider != "local" || releases.Items[0].WorkspaceState != "ready" ||
		releases.Items[0].DeploymentPlanRevisionID != plan.RevisionID || releases.Items[0].DeploymentPlanRevisionNumber != plan.RevisionNumber ||
		releases.Items[0].ConfigurationRevisionID != saved.RevisionID || releases.Items[0].ConfigurationRevisionNumber != saved.RevisionNumber {
		t.Fatal("deployment did not pin one immutable release")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(releases.Items[0].ArchiveSha256) {
		t.Fatal("local release has no immutable source digest")
	}
	workspace, err := snapshots.ReadyWorkspace(ctx, application.ID, releases.Items[0].ID)
	if err != nil || workspace.WorkspaceTreeSHA256 != releases.Items[0].ArchiveSha256 || workspace.WorkspaceState != "ready" {
		t.Fatal("pinned local release workspace failed digest verification")
	}
	controllerJourneyAssertScopedContainers(t, ctx, docker, application.ID, dbURL, sentinel)
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/version", "", http.StatusOK, "controller-journey")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/test/dependency", "", http.StatusOK, "reachable")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodPost, "/api/notes", `{"body":"controller TLS note"}`, http.StatusCreated, "controller TLS note")
	controllerJourneyRoutedRequest(t, ctx, application.ID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
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
	t.Logf("M2 controller journey identities: app=%s plan=%s/%d config=%s/%d job=%s deployment=%s release=%s source=local-snapshot ingress=127.0.0.1:8080", application.ID, plan.RevisionID, plan.RevisionNumber, saved.RevisionID, saved.RevisionNumber, completed.ID, history.Items[0].ID, releases.Items[0].ID)
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

func controllerJourneyDocker(ctx context.Context, docker string, extraEnv []string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, docker, args...)
	command.Env = append(os.Environ(), extraEnv...)
	return command.CombinedOutput()
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

func controllerJourneyPort(t *testing.T, address string) int {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
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

func controllerJourneyProblemCode(body []byte) string {
	var problem struct{ Code string }
	if json.Unmarshal(body, &problem) != nil {
		return "unreadable"
	}
	return problem.Code
}

func controllerJourneyAssertScopedContainers(t *testing.T, ctx context.Context, docker, appID, dbURL, sentinel string) {
	t.Helper()
	ids, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-q", "--filter", "label=io.rig.application="+appID)
	if err != nil || len(strings.Fields(string(ids))) != 2 {
		t.Fatal("generated deployment did not start exactly two scoped components")
	}
	seen := map[string]bool{}
	for _, id := range strings.Fields(string(ids)) {
		body, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{json .Config}}", id)
		var configuration struct {
			Labels map[string]string
			Env    []string
		}
		if err != nil || json.Unmarshal(bytes.TrimSpace(body), &configuration) != nil {
			t.Fatal("inspect generated component configuration")
		}
		name := configuration.Labels["io.rig.component"]
		if configuration.Labels["io.rig.managed"] != "generated-runtime" || seen[name] || (name != "api" && name != "frontend") {
			t.Fatal("generated component ownership or identity is invalid")
		}
		seen[name] = true
		entries := strings.Join(configuration.Env, "\n")
		if name == "api" {
			if !strings.Contains(entries, "NOTES_FIXTURE_DB_URL="+dbURL) || !strings.Contains(entries, "TEST_SENTINEL_SECRET="+sentinel) {
				t.Fatal("API container did not receive its exact scoped server secrets")
			}
		} else if strings.Contains(entries, dbURL) || strings.Contains(entries, sentinel) || strings.Contains(entries, "NOTES_FIXTURE_DB_URL=") {
			t.Fatal("frontend container received a server runtime secret")
		}
	}
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
