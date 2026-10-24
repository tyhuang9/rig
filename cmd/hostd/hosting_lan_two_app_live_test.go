//go:build live_docker

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"sync"
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
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/hostnetwork"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
	generateddomaintest "github.com/hostd/hostd/internal/testsupport/generateddomain"
)

const (
	lanTwoAppClientNetworkPrefix = "rig-lan-two-app-test-client"
	lanTwoAppNodeImage           = "node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e"
	liveColdRuntimeManifestEnv   = "RIG_LIVE_COLD_RUNTIME_RESTART_MANIFEST"
	liveColdRuntimeLAN           = "lan"
	liveColdRuntimeMigration     = "migration"
)

// The opt-in job runs on a disposable Linux runner. Every administrative
// transition goes through the authenticated production controller handler.
func TestLiveControllerTwoAppLANJourney(t *testing.T) {
	if liveColdRuntimeRestartChildRequested(t, "RIG_RUN_LIVE_TWO_APP_LAN") {
		return
	}
	if os.Getenv("RIG_RUN_LIVE_TWO_APP_LAN") != "1" {
		t.Fatal("set RIG_RUN_LIVE_TWO_APP_LAN=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("two-app LAN gate requires the default local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	ctx, cancel := context.WithTimeout(context.Background(), 57*time.Minute)
	defer cancel()
	selected, ports := lanTwoAppHostPorts(t)
	lanTwoAppDockerPreflight(t, ctx, docker)
	root := newLiveColdRuntimeFixtureRoot(t)
	dataRoot := filepath.Join(root, "controller")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	appIDs := make([]string, 0, 2)
	upgradeID := uuid.NewString()
	lanTwoAppRegisterCleanup(t, docker, dataRoot, upgradeID, &appIDs)
	installed, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal(err)
	}
	transportServer := lanTwoAppTransportFixture(t, installed, "lan-transport-server.mjs")
	transportClient := lanTwoAppTransportFixture(t, installed, "lan-transport-client.cjs")
	tracked, err := controllerJourneyTrackedFixtureFiles(installed)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := controllerJourneyStageSource(installed, source, tracked); err != nil {
		t.Fatal(err)
	}
	// Keep the committed two-component build recipe but replace the API's
	// external PostgreSQL dependency with a small, explicit HTTP test fixture.
	// This staged source is the immutable archive served by the controlled
	// GitHub provider; no repository fixture or product code is changed.
	if err := os.WriteFile(filepath.Join(source, "api", "src", "server.js"), transportServer, 0o600); err != nil {
		t.Fatal(err)
	}
	provider := controllerJourneyNewGitHubProvider(t, source)
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal("prepare two-app controller directories:", err)
	}
	ownerLease, err := controllerowner.Acquire(ctx, directories.WorkingDirectory)
	if err != nil {
		t.Fatal("acquire two-app controller owner:", err)
	}
	ownerLeaseClosed := false
	defer func() {
		if !ownerLeaseClosed {
			_ = ownerLease.Close()
		}
	}()
	db, err := database.OpenForGeneratedDomain(ctx, dataRoot, ownerLease)
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
	user, session, err := authService.Bootstrap(bootstrap, "lan-journey-admin", "a sufficiently long disposable passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	appStore := apps.New(db)
	jobStore, bindingStore, err := newDeploymentJobService(db)
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
	sourceClock := time.Now().UTC()
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", func() time.Time { return sourceClock })
	snapshots, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentStore := deployments.New(db)
	effectsWorkingDirectory, scopedAdmission := liveDeploymentExecutionFixture(t, db, dataRoot)
	settings := config.Defaults()
	settings.DataRoot, settings.GeneratedRuntime = dataRoot, true
	dependencies := runtimeCompositionDependencies{
		db: db, applications: appStore, snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans, bindings: bindingStore,
		effectsWorkingDirectory: effectsWorkingDirectory,
	}
	runner := &runtimeprocess.ExecRunner{}
	options := runtimeCompositionOptions{
		dockerExecutable: docker,
		runner:           runner,
		routePauseLogger: newStructuredLogger(os.Stderr, "info"),
	}
	generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, ctx, settings, dependencies, options)
	if err != nil {
		t.Fatal("construct two-app generated domain:", err)
	}
	dependencies.generated = generated
	composition, err := prepareRuntimeComposition(ctx, settings, dependencies, options)
	if err != nil {
		t.Fatal("compose production runtime:", err)
	}
	access := appaccess.New(db)
	grantTrace := &lanTwoAppGrantTrace{Manager: composition.ingress}
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
	if connection.Status != "connected" {
		t.Fatal("controlled source connection did not become authorized")
	}
	githubSource := apicontract.GitHubSource{ConnectionID: connection.ID, InstallationID: 7, RepositoryID: 17, Branch: "main"}
	workerCtx, stopWorker := context.WithCancel(ctx)
	done, err := prepareRuntimeWorker(workerCtx, runtimeRecovery{
		recoverState: jobStore.RecoverRuntimeContext,
	}, composition.executor, func(workerContext context.Context, executor jobs.Executor) error {
		return jobStore.RunWorkerWithScopedAdmission(workerContext, executor, scopedAdmission)
	}, func(error) {})
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
			t.Error("runtime worker did not stop")
		}
	}()
	appA := lanTwoAppDeploy(t, &request, jobStore, deploymentStore, composition.state, githubSource, setup, "LAN A", "lan-a-v1", &appIDs)
	appB := lanTwoAppDeploy(t, &request, jobStore, deploymentStore, composition.state, githubSource, setup, "LAN B", "lan-b-v1", &appIDs)
	if appA.id == appB.id || appA.deploymentID == appB.deploymentID {
		t.Fatal("two distinct application deployments are required")
	}
	lanTwoAppAssertLoopback(t, ctx, appA.id, "lan-a-v1")
	lanTwoAppAssertLoopback(t, ctx, appB.id, "lan-b-v1")

	profileQuery := fmt.Sprintf("?interfaceId=%s&selectedIpv4=%s&portStart=%d&portEnd=%d",
		urlQuery(selected.InterfaceID), urlQuery(selected.IPv4), ports[0], ports[2])
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
	lanTwoAppProbe(t, ctx, selected.IPv4, ports[0], selected.IPv4, "/", http.StatusNotFound, "")
	lanTwoAppProbe(t, ctx, selected.IPv4, ports[1], selected.IPv4, "/", http.StatusNotFound, "")
	lanTwoAppProbe(t, ctx, selected.IPv4, ports[2], selected.IPv4, "/", http.StatusNotFound, "")

	portA := lanTwoAppGrant(t, &request, access, profile.Profile, appA.id, user.ID)
	portB := lanTwoAppGrant(t, &request, access, profile.Profile, appB.id, user.ID)
	if portA != ports[0] || portB != ports[1] || portA == portB {
		t.Fatalf("unexpected durable LAN assignments A=%d B=%d", portA, portB)
	}
	lanTwoAppProbeMatrix(t, ctx, selected.IPv4, portA, portB, ports[2], "lan-a-v1", "lan-b-v1")
	lanTwoAppWriteNote(t, ctx, selected.IPv4, portB, "b-persistent-note", http.StatusCreated)
	lanTwoAppProbe(t, ctx, selected.IPv4, portB, selected.IPv4, "/api/notes", http.StatusOK, "b-persistent-note")
	lanTwoAppProbe(t, ctx, selected.IPv4, portA, selected.IPv4, "/api/notes", http.StatusOK, `"notes":[]`)
	controllerAddress, err := controllerJourneyFixtureControllerListenAddress(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	controllerPort, err := controllerJourneyListenPort(controllerAddress)
	if err != nil {
		t.Fatal(err)
	}
	lanTwoAppNamespaceProbe(t, ctx, docker, selected.IPv4, portA, portB, ports[2], controllerPort,
		appA.id, appB.id, transportClient, "lan-a-v1", "lan-b-v1")

	beforeB := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-q", "--filter", "label=io.rig.application="+appB.id)
	if len(strings.Split(beforeB, ",")) != 2 || beforeB == "" {
		t.Fatal("B does not have exactly two running runtime containers before A redeploy")
	}
	var configA apicontract.ApplicationConfiguration
	request.do(http.MethodPut, "/api/v1/apps/"+appA.id+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: appA.config.RevisionNumber, PlanRevisionID: appA.plan.RevisionID,
		PlanRevisionNumber: appA.plan.RevisionNumber, PublicBuildDisclosureAcknowledged: true,
		Entries: lanTwoAppConfig("lan-a-v2"), Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &configA)
	if configA.RevisionID == appA.config.RevisionID || configA.RevisionNumber != appA.config.RevisionNumber+1 {
		t.Fatal("redeploy A did not save a distinct configuration revision")
	}
	lanTwoAppDeployJob(t, &request, jobStore, deploymentStore, composition.state, appA.id, appA.plan, configA)
	lanTwoAppProbeMatrix(t, ctx, selected.IPv4, portA, portB, ports[2], "lan-a-v2", "lan-b-v1")
	lanTwoAppNamespaceProbe(t, ctx, docker, selected.IPv4, portA, portB, ports[2], controllerPort,
		appA.id, appB.id, transportClient, "lan-a-v2", "lan-b-v1")
	lanTwoAppProbe(t, ctx, selected.IPv4, portB, selected.IPv4, "/api/notes", http.StatusOK, "b-persistent-note")
	if got := controllerJourneyDockerIDSet(t, ctx, docker, "ps", "-q", "--filter", "label=io.rig.application="+appB.id); got != beforeB {
		t.Fatal("redeploying A replaced B's runtime containers")
	}
	for _, app := range []struct {
		id   string
		port uint16
	}{{appA.id, portA}, {appB.id, portB}} {
		var read apicontract.LANAppAccessRead
		request.do(http.MethodGet, "/api/v1/apps/"+app.id+"/lan-access", nil, http.StatusOK, &read)
		if read.Availability != "verified" || read.Url != fmt.Sprintf("http://%s:%d/", selected.IPv4, app.port) || read.GrantClaim == nil || read.GrantClaim.State != "committed" {
			t.Fatalf("fresh LAN access observation for app %s is not bound to its durable claim", app.id)
		}
	}
	// A fresh process must rediscover the committed gateway. It cannot inherit
	// the domain owner or Manager that observed the grants in this process.
	if !stopInitialWorker() {
		t.Fatal("runtime worker did not stop before cold controller restart")
	}
	api.Close()
	if err := db.Close(); err != nil {
		t.Fatal("close two-app controller database before cold restart:", err)
	}
	dbClosed = true
	if err := ownerLease.Close(); err != nil {
		t.Fatal("release two-app controller owner before cold restart:", err)
	}
	ownerLeaseClosed = true
	cold := startLiveColdRuntimeRestartChild(t, ctx, "TestLiveControllerTwoAppLANJourney", liveColdRuntimeRestartManifest{
		Mode: liveColdRuntimeLAN, DataRoot: dataRoot, Source: source, Docker: docker,
	})
	freshRequest := request
	freshRequest.base = cold.url
	var freshUpgrade apicontract.LANGatewayUpgradeRead
	freshRequest.do(http.MethodGet, "/api/v1/system/lan-gateway-upgrade", nil, http.StatusOK, &freshUpgrade)
	if freshUpgrade.DesiredClaim == nil || freshUpgrade.DesiredClaim.OperationID != upgradeID ||
		freshUpgrade.Observed.OperationID != upgradeID || freshUpgrade.Observed.Availability != "serving" {
		t.Fatalf("fresh manager gateway observation=%#v, want exact serving operation %s", freshUpgrade, upgradeID)
	}
	for _, app := range []struct {
		id   string
		port uint16
	}{{appA.id, portA}, {appB.id, portB}} {
		var read apicontract.LANAppAccessRead
		freshRequest.do(http.MethodGet, "/api/v1/apps/"+app.id+"/lan-access", nil, http.StatusOK, &read)
		if read.Availability != "verified" || read.Url != fmt.Sprintf("http://%s:%d/", selected.IPv4, app.port) ||
			read.GrantClaim == nil || read.GrantClaim.State != "committed" {
			t.Fatalf("fresh manager did not reattest app %s LAN claim", app.id)
		}
	}
	cold.stop()
}

// Cold restart control files contain only fixture-owned paths and a closed
// scenario. Authentication, claims, receipts, authority proofs and secrets
// remain in the parent process or in the sealed database.
type liveColdRuntimeRestartManifest struct {
	Mode             string `json:"mode"`
	DataRoot         string `json:"dataRoot"`
	Source           string `json:"source"`
	SourceV2         string `json:"sourceV2"`
	SourceV3         string `json:"sourceV3"`
	Docker           string `json:"docker"`
	ControlDirectory string `json:"controlDirectory"`
}

type liveColdRuntimeRestartResponse struct {
	Sequence uint64
	PID      int
	URL      string
}

type liveColdRuntimeRestartProcess struct {
	t         *testing.T
	ctx       context.Context
	child     *controllerProcessKillProcess
	directory string
	url       string
}

func newLiveColdRuntimeFixtureRoot(t *testing.T) string {
	t.Helper()
	return generateddomaintest.NewRetainedRoot(t)
}

func liveColdRuntimeExactDirectory(path string, private bool) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() &&
		(!private || runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0)
}

func validLiveColdRuntimeRestartManifest(manifest liveColdRuntimeRestartManifest, manifestPath, expectedDocker string) bool {
	if manifest.Docker == "" || manifest.Docker != expectedDocker || !filepath.IsAbs(expectedDocker) ||
		filepath.Clean(expectedDocker) != expectedDocker || filepath.Base(manifest.DataRoot) != "controller" ||
		filepath.Base(manifestPath) != "manifest.json" || filepath.Dir(manifestPath) != manifest.ControlDirectory {
		return false
	}
	root := filepath.Dir(manifest.DataRoot)
	owner := filepath.Dir(root)
	if filepath.Base(root) == "." || !strings.HasPrefix(filepath.Base(root), "domain-") ||
		!strings.HasPrefix(filepath.Base(owner), "rig-generated-domain-test-roots-") ||
		filepath.Dir(manifest.ControlDirectory) != root ||
		!strings.HasPrefix(filepath.Base(manifest.ControlDirectory), "cold-restart-") ||
		!liveColdRuntimeExactDirectory(owner, true) || !liveColdRuntimeExactDirectory(root, true) ||
		!liveColdRuntimeExactDirectory(manifest.DataRoot, true) ||
		!liveColdRuntimeExactDirectory(manifest.ControlDirectory, true) {
		return false
	}
	marker, err := os.Lstat(filepath.Join(owner, ".owner"))
	if err != nil || marker.Mode()&os.ModeSymlink != 0 || !marker.Mode().IsRegular() ||
		(runtime.GOOS != "windows" && marker.Mode().Perm()&0o077 != 0) {
		return false
	}
	sources := []struct {
		path string
		base string
	}{{manifest.Source, "source"}}
	if manifest.Mode == liveColdRuntimeMigration {
		sources = []struct {
			path string
			base string
		}{{manifest.Source, "source-v1"}, {manifest.SourceV2, "source-v2"}, {manifest.SourceV3, "source-v3"}}
	} else if manifest.Mode != liveColdRuntimeLAN || manifest.SourceV2 != "" || manifest.SourceV3 != "" {
		return false
	}
	for _, source := range sources {
		if filepath.Dir(source.path) != root || filepath.Base(source.path) != source.base ||
			!liveColdRuntimeExactDirectory(source.path, false) {
			return false
		}
	}
	return true
}

func rejectLiveColdRuntimeDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("restart manifest is not an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		folded := strings.ToLower(key)
		if err != nil || !ok || folded == "" {
			return errors.New("restart manifest key is invalid")
		}
		if _, exists := seen[folded]; exists {
			return errors.New("restart manifest key is duplicated")
		}
		seen[folded] = struct{}{}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return errors.New("restart manifest value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("restart manifest object is incomplete")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("restart manifest has trailing input")
	}
	return nil
}

func readLiveColdRuntimeRestartManifest(path, expectedDocker string) (liveColdRuntimeRestartManifest, error) {
	var manifest liveColdRuntimeRestartManifest
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "manifest.json" {
		return manifest, errors.New("restart manifest path is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() ||
		(runtime.GOOS != "windows" && pathInfo.Mode().Perm()&0o077 != 0) {
		return manifest, errors.New("restart manifest file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, errors.New("restart manifest cannot be opened")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return manifest, errors.New("restart manifest identity changed")
	}
	body, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(body) == 0 || len(body) > 16<<10 || rejectLiveColdRuntimeDuplicateKeys(body) != nil {
		return manifest, errors.New("restart manifest JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validLiveColdRuntimeRestartManifest(manifest, path, expectedDocker) {
		return liveColdRuntimeRestartManifest{}, errors.New("restart manifest is invalid")
	}
	return manifest, nil
}

func TestLiveColdRuntimeRestartManifestRejectsCrossedOrSensitiveShape(t *testing.T) {
	newFixture := func(t *testing.T, mode string) (liveColdRuntimeRestartManifest, string) {
		t.Helper()
		root := newLiveColdRuntimeFixtureRoot(t)
		for _, name := range []string{"controller", "source", "source-v1", "source-v2", "source-v3"} {
			if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		docker := filepath.Join(root, "docker")
		if err := os.WriteFile(docker, []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
		control, err := os.MkdirTemp(root, "cold-restart-")
		if err != nil || os.Chmod(control, 0o700) != nil {
			t.Fatal("create private restart control directory")
		}
		manifest := liveColdRuntimeRestartManifest{
			Mode: mode, DataRoot: filepath.Join(root, "controller"), Docker: docker, ControlDirectory: control,
		}
		if mode == liveColdRuntimeMigration {
			manifest.Source = filepath.Join(root, "source-v1")
			manifest.SourceV2 = filepath.Join(root, "source-v2")
			manifest.SourceV3 = filepath.Join(root, "source-v3")
		} else {
			manifest.Source = filepath.Join(root, "source")
		}
		return manifest, filepath.Join(control, "manifest.json")
	}
	t.Run("exact LAN and migration manifests", func(t *testing.T) {
		for _, mode := range []string{liveColdRuntimeLAN, liveColdRuntimeMigration} {
			manifest, path := newFixture(t, mode)
			if !validLiveColdRuntimeRestartManifest(manifest, path, manifest.Docker) ||
				controllerJourneyWriteRestartFile(path, manifest) != nil {
				t.Fatalf("exact %s restart manifest was rejected", mode)
			}
			got, err := readLiveColdRuntimeRestartManifest(path, manifest.Docker)
			if err != nil || got != manifest {
				t.Fatalf("exact %s restart manifest did not round trip", mode)
			}
		}
	})
	t.Run("crossed paths and executable", func(t *testing.T) {
		base, path := newFixture(t, liveColdRuntimeLAN)
		mutations := []func(*liveColdRuntimeRestartManifest, *string, *string){
			func(value *liveColdRuntimeRestartManifest, _, _ *string) { value.Mode = "unknown" },
			func(value *liveColdRuntimeRestartManifest, _, _ *string) { value.DataRoot = "relative" },
			func(value *liveColdRuntimeRestartManifest, _, _ *string) {
				value.SourceV2 = filepath.Join(filepath.Dir(value.DataRoot), "source-v2")
			},
			func(_ *liveColdRuntimeRestartManifest, value, _ *string) {
				*value = filepath.Join(filepath.Dir(*value), "other.json")
			},
			func(_ *liveColdRuntimeRestartManifest, _, expectedDocker *string) { *expectedDocker += "-crossed" },
		}
		for index, mutate := range mutations {
			candidate, candidatePath, expectedDocker := base, path, base.Docker
			mutate(&candidate, &candidatePath, &expectedDocker)
			if validLiveColdRuntimeRestartManifest(candidate, candidatePath, expectedDocker) {
				t.Fatalf("invalid restart manifest mutation %d was accepted", index)
			}
		}
	})
	t.Run("unknown and duplicate fields", func(t *testing.T) {
		for name, mutate := range map[string]func([]byte) []byte{
			"unknown": func(body []byte) []byte { return bytes.Replace(body, []byte(`}`), []byte(`,"unknown":"value"}`), 1) },
			"duplicate": func(body []byte) []byte {
				return bytes.Replace(body, []byte(`"mode":"lan"`), []byte(`"mode":"lan","mode":"lan"`), 1)
			},
			"case folded duplicate": func(body []byte) []byte {
				return bytes.Replace(body, []byte(`"mode":"lan"`), []byte(`"mode":"lan","Mode":"lan"`), 1)
			},
		} {
			t.Run(name, func(t *testing.T) {
				manifest, path := newFixture(t, liveColdRuntimeLAN)
				body, err := json.Marshal(manifest)
				if err != nil || os.WriteFile(path, mutate(body), 0o600) != nil {
					t.Fatal("write invalid restart manifest")
				}
				if _, err := readLiveColdRuntimeRestartManifest(path, manifest.Docker); err == nil {
					t.Fatal("invalid restart manifest JSON was accepted")
				}
			})
		}
	})
	t.Run("symlink manifest and source", func(t *testing.T) {
		manifest, path := newFixture(t, liveColdRuntimeLAN)
		body, err := json.Marshal(manifest)
		target := filepath.Join(manifest.ControlDirectory, "payload.json")
		if err != nil || os.WriteFile(target, body, 0o600) != nil || os.Symlink(target, path) != nil {
			if runtime.GOOS == "windows" {
				t.Skip("file symlink unavailable")
			}
			t.Fatal("create manifest symlink")
		}
		if _, err := readLiveColdRuntimeRestartManifest(path, manifest.Docker); err == nil {
			t.Fatal("restart manifest symlink was accepted")
		}
		if err := os.Remove(path); err != nil || os.RemoveAll(manifest.Source) != nil ||
			os.Symlink(filepath.Dir(filepath.Dir(manifest.DataRoot)), manifest.Source) != nil {
			t.Fatal("create crossed source symlink")
		}
		if validLiveColdRuntimeRestartManifest(manifest, path, manifest.Docker) {
			t.Fatal("restart source symlink was accepted")
		}
	})
}

func startLiveColdRuntimeRestartChild(t *testing.T, ctx context.Context, selector string, manifest liveColdRuntimeRestartManifest) *liveColdRuntimeRestartProcess {
	t.Helper()
	switch selector {
	case "TestLiveControllerTwoAppLANJourney", "TestLiveControllerLANDatabaseJourney",
		"TestLiveGeneratedMigrationApprovalAndUncertaintyJourney":
	default:
		t.Fatal("cold restart child selector is invalid")
	}
	root := filepath.Dir(manifest.DataRoot)
	control, err := os.MkdirTemp(root, "cold-restart-")
	if err != nil || os.Chmod(control, 0o700) != nil {
		t.Fatal("create private cold restart control directory")
	}
	controlInfo, err := os.Lstat(control)
	if err != nil {
		t.Fatal("inspect private cold restart control directory")
	}
	t.Cleanup(func() {
		current, statErr := os.Lstat(control)
		if statErr != nil || !os.SameFile(controlInfo, current) || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() {
			t.Error("cold restart control directory identity changed; retaining")
			return
		}
		if removeErr := os.RemoveAll(control); removeErr != nil {
			t.Error("remove exact cold restart control directory")
		}
	})
	manifest.ControlDirectory = control
	manifestPath := filepath.Join(manifest.ControlDirectory, "manifest.json")
	if !validLiveColdRuntimeRestartManifest(manifest, manifestPath, manifest.Docker) ||
		controllerJourneyWriteRestartFile(manifestPath, manifest) != nil {
		t.Fatal("write private cold restart manifest")
	}
	written, err := readLiveColdRuntimeRestartManifest(manifestPath, manifest.Docker)
	if err != nil || written != manifest {
		t.Fatal("verify private cold restart manifest")
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(selector)+"$", "-test.timeout=26m")
	command.Env = liveColdRuntimeChildEnvironment(manifestPath)
	child := controllerProcessKillCapture(command)
	if err := command.Start(); err != nil {
		t.Fatal("start cold runtime process")
	}
	go func() {
		child.err = command.Wait()
		close(child.done)
	}()
	t.Cleanup(func() { controllerProcessKillCleanupChild(child) })
	process := &liveColdRuntimeRestartProcess{t: t, ctx: ctx, child: child, directory: manifest.ControlDirectory}
	ready := process.wait(0, 7*time.Minute)
	if ready.PID != child.command.Process.Pid || ready.PID == os.Getpid() {
		t.Fatal("cold restart readiness did not identify the fresh process")
	}
	address, err := controllerJourneyFixtureControllerListenAddress(ready.URL)
	if err != nil || !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatal("cold restarted controller did not bind loopback")
	}
	process.url = ready.URL
	return process
}

func liveColdRuntimeChildEnvironment(manifestPath string) []string {
	values := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, liveColdRuntimeManifestEnv+"=") {
			continue
		}
		values = append(values, value)
	}
	return append(values, liveColdRuntimeManifestEnv+"="+manifestPath)
}

func (child *liveColdRuntimeRestartProcess) wait(sequence uint64, maximum time.Duration) liveColdRuntimeRestartResponse {
	child.t.Helper()
	ctx, cancel := context.WithTimeout(child.ctx, maximum)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	path := filepath.Join(child.directory, fmt.Sprintf("%06d.response.json", sequence))
	for {
		var response liveColdRuntimeRestartResponse
		if err := controllerJourneyReadRestartFile(path, &response); err == nil {
			if response.Sequence != sequence || response.PID != child.child.command.Process.Pid {
				child.t.Fatal("cold restart response identity mismatch")
			}
			return response
		} else if !errors.Is(err, os.ErrNotExist) {
			child.t.Fatal("cold restart response is invalid")
		}
		select {
		case <-child.child.done:
			controllerProcessKillReportClosedDiagnostic(child.t, child.child)
			child.t.Fatal("cold restarted controller exited before response")
		case <-ctx.Done():
			child.t.Fatal("cold restart response exceeded bound")
		case <-tick.C:
		}
	}
}

func (child *liveColdRuntimeRestartProcess) stop() {
	child.t.Helper()
	path := filepath.Join(child.directory, "000001.request.json")
	if err := controllerJourneyWriteRestartFile(path, controllerJourneyRestartCommand{Sequence: 1, Action: "stop"}); err != nil {
		child.t.Fatal("write cold restart stop command")
	}
	child.wait(1, 3*time.Minute)
	select {
	case <-child.child.done:
		if child.child.err != nil {
			controllerProcessKillReportClosedDiagnostic(child.t, child.child)
			child.t.Fatal("cold restart child did not stop cleanly")
		}
	case <-time.After(10 * time.Second):
		child.t.Fatal("cold restart child did not exit after stopping")
	}
}

func liveColdRuntimeRestartChildRequested(t *testing.T, requiredOptIn string) bool {
	t.Helper()
	manifestPath := os.Getenv(liveColdRuntimeManifestEnv)
	if manifestPath == "" {
		return false
	}
	if requiredOptIn == "" || os.Getenv(requiredOptIn) != "1" {
		t.Fatal("cold runtime child requires the selected hosted test opt-in")
	}
	expectedDocker, err := exec.LookPath("docker")
	if err == nil {
		expectedDocker, err = filepath.Abs(expectedDocker)
	}
	manifest, manifestErr := readLiveColdRuntimeRestartManifest(manifestPath, expectedDocker)
	if err != nil || manifestErr != nil || runtime.GOOS != "linux" ||
		os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("cold runtime child requires an exact local Linux fixture manifest")
	}
	liveColdRuntimeRunRestartChild(t, manifest)
	return true
}

func liveColdRuntimeRunRestartChild(t *testing.T, manifest liveColdRuntimeRestartManifest) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	directories, err := runtimedocker.PrepareControllerDirectories(manifest.DataRoot)
	if err != nil {
		t.Fatal("prepare cold restarted controller directories")
	}
	ownerLease, err := controllerowner.Acquire(ctx, directories.WorkingDirectory)
	if err != nil {
		t.Fatal("acquire cold restarted controller owner")
	}
	defer func() { _ = ownerLease.Close() }()
	db, err := database.OpenForGeneratedDomain(ctx, manifest.DataRoot, ownerLease)
	if err != nil {
		t.Fatal("open sealed cold restarted controller database")
	}
	defer db.Close()
	provider := controllerJourneyNewGitHubProvider(t, manifest.Source)
	if manifest.Mode == liveColdRuntimeMigration {
		provider.AddRevision(t, migrationJourneyV2SHA, manifest.SourceV2)
		provider.AddRevision(t, migrationJourneyV3SHA, manifest.SourceV3)
	}
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider,
		sourceconnections.NewFileCredentialStore(manifest.DataRoot), "fixture-app", time.Now)
	snapshots, err := releasesnapshot.New(db, sources, manifest.DataRoot)
	if err != nil || snapshots.Recover() != nil {
		t.Fatal("recover cold restarted source materializer")
	}
	configuration, err := appconfig.New(db, manifest.DataRoot)
	if err != nil || configuration.Recover(ctx) != nil {
		t.Fatal("recover cold restarted scoped configuration")
	}
	plans, err := deploymentplans.New(db, manifest.DataRoot)
	if err != nil || plans.Recover(ctx) != nil {
		t.Fatal("recover cold restarted deployment plans")
	}
	jobStore, bindings, err := newDeploymentJobService(db)
	if err != nil {
		t.Fatal("open cold restarted deployment jobs")
	}
	effectsDirectory, admission := liveDeploymentExecutionFixture(t, db, manifest.DataRoot)
	settings := config.Defaults()
	settings.DataRoot, settings.GeneratedRuntime = manifest.DataRoot, true
	dependencies := runtimeCompositionDependencies{
		db: db, applications: apps.New(db), snapshots: snapshots, configuration: configuration,
		deployments: deployments.New(db), plans: plans, bindings: bindings, effectsWorkingDirectory: effectsDirectory,
	}
	var runner runtimeprocess.CommandRunner = &runtimeprocess.ExecRunner{}
	var recoveryMigration *migrationJourneyRunner
	options := runtimeCompositionOptions{dockerExecutable: manifest.Docker, runner: runner,
		routePauseLogger: newStructuredLogger(os.Stderr, "info")}
	if manifest.Mode == liveColdRuntimeMigration {
		runner = newMigrationAuthorityProcessObserver(db)
		recoveryMigration = &migrationJourneyRunner{}
		options.runner = runner
		options.migrationRunnerFactory = func(delegate generatedruntime.MigrationRunner) generatedruntime.MigrationRunner {
			recoveryMigration.SetDelegate(delegate)
			return recoveryMigration
		}
	}
	generated, err := prepareCompleteGeneratedRuntimeDomainForTest(t, ctx, settings, dependencies, options)
	if err != nil {
		t.Fatal("construct cold restarted generated domain:", err)
	}
	dependencies.generated = generated
	composition, err := prepareRuntimeComposition(ctx, settings, dependencies, options)
	if err != nil {
		t.Fatal("compose cold restarted generated runtime:", err)
	}
	workerContext, stopWorker := context.WithCancel(ctx)
	workerFailure := false
	done, err := prepareRuntimeWorker(workerContext, runtimeRecovery{recoverState: jobStore.RecoverRuntimeContext}, composition.executor,
		func(workerContext context.Context, executor jobs.Executor) error {
			return jobStore.RunWorkerWithScopedAdmission(workerContext, executor, admission)
		}, func(error) { workerFailure = true })
	if err != nil {
		t.Fatal("recover cold restarted durable worker")
	}
	workerJoined := false
	workerOutcomeChecked := false
	joinWorker := func() bool {
		if workerJoined {
			return true
		}
		stopWorker()
		select {
		case <-done:
			workerJoined = true
			return true
		case <-time.After(10 * time.Second):
			return false
		}
	}
	defer func() {
		if !joinWorker() {
			t.Error("cold restarted durable worker did not stop")
		} else if !workerOutcomeChecked && workerFailure {
			t.Error("cold restarted durable worker failed")
		}
	}()
	access := appaccess.New(db)
	server := controllerJourneyRestartControllerServer(t, (&controller.Server{
		Auth: auth.New(db), Apps: dependencies.applications, Jobs: jobStore, Machines: machines.New(db), Sources: sources,
		Configuration: configuration, Deployments: dependencies.deployments, DeploymentPlans: plans,
		GeneratedIngress: composition.ingress, GeneratedRuntimeState: composition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: manifest.DataRoot,
		GatewayProfiles: access, GatewayUpgrades: access, GatewayUpgradeRuntime: composition.ingress,
		AppAccess: access, AppGrants: access, LANGrantRuntime: composition.ingress,
		AppDisables: access, LANDisableRuntime: composition.ingress,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer server.Close()
	respond := func(sequence uint64) {
		path := filepath.Join(manifest.ControlDirectory, fmt.Sprintf("%06d.response.json", sequence))
		if err := controllerJourneyWriteRestartFile(path, liveColdRuntimeRestartResponse{
			Sequence: sequence, PID: os.Getpid(), URL: server.URL,
		}); err != nil {
			t.Fatal("publish cold restart response")
		}
	}
	respond(0)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("cold restart child exceeded bound")
		case <-done:
			workerJoined = true
			workerOutcomeChecked = true
			if workerFailure {
				t.Fatal("cold restarted durable worker failed")
			}
			t.Fatal("cold restarted durable worker stopped unexpectedly")
		case <-tick.C:
		}
		var command controllerJourneyRestartCommand
		path := filepath.Join(manifest.ControlDirectory, "000001.request.json")
		if err := controllerJourneyReadRestartFile(path, &command); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil || command.Sequence != 1 || command.Action != "stop" || command.Target != "" {
			t.Fatal("cold restart command is invalid")
		}
		if !joinWorker() {
			t.Fatal("cold restarted durable worker did not stop")
		}
		workerOutcomeChecked = true
		if workerFailure {
			t.Fatal("cold restarted durable worker failed")
		}
		if recoveryMigration != nil && recoveryMigration.Calls() != 0 {
			t.Fatal("cold migration recovery replayed the migration runner")
		}
		server.Close()
		respond(1)
		return
	}
}

func lanTwoAppTransportFixture(t *testing.T, installed, name string) []byte {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join(installed, "api", "fixtures", name))
	if err != nil || len(fixture) == 0 {
		t.Fatalf("read exact LAN transport fixture %s", name)
	}
	return fixture
}

type lanTwoAppAPI struct {
	t       *testing.T
	ctx     context.Context
	base    string
	session string
	csrf    string
	client  *http.Client
}

// The live gate prints only fixed operation names and closed outcomes after a
// failure. It never logs grant requests, Docker output, addresses, or secrets.
type lanTwoAppGrantTrace struct {
	*generatedingress.Manager
	mu     sync.Mutex
	events []string
}

type lanTwoAppGrantLeaseTrace struct {
	generatedingress.GatewayV2LANGrantAuthorizationLease
	trace *lanTwoAppGrantTrace
}

func (lease lanTwoAppGrantLeaseTrace) Revalidate(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest) error {
	err := lease.GatewayV2LANGrantAuthorizationLease.Revalidate(ctx, request)
	lease.trace.record("revalidate", err, "")
	return err
}

func (lease lanTwoAppGrantLeaseTrace) Activate(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest) error {
	err := lease.GatewayV2LANGrantAuthorizationLease.Activate(ctx, request)
	lease.trace.record("activate", err, "")
	return err
}

func (lease lanTwoAppGrantLeaseTrace) Release() error {
	err := lease.GatewayV2LANGrantAuthorizationLease.Release()
	lease.trace.record("release", err, "")
	return err
}

func (trace *lanTwoAppGrantTrace) record(stage string, err error, disposition generatedingress.GatewayV2LANGrantDisposition) {
	outcome := "ok"
	if err != nil {
		outcome = "other_error"
		var diagnostic *generatedingress.Error
		if errors.As(err, &diagnostic) {
			switch diagnostic.Code {
			case generatedingress.DiagnosticValidationFailed, generatedingress.DiagnosticIngressUnavailable,
				generatedingress.DiagnosticIngressDrift, generatedingress.DiagnosticRouteInvalid,
				generatedingress.DiagnosticRouteValidateFailed, generatedingress.DiagnosticRouteReloadFailed,
				generatedingress.DiagnosticRouteStateFailed, generatedingress.DiagnosticRouteUnresolved,
				generatedingress.DiagnosticGatewayReadinessFailed, generatedingress.DiagnosticCancelled:
				outcome = string(diagnostic.Code)
			}
		}
	} else {
		switch disposition {
		case generatedingress.GatewayV2LANGrantCommitted, generatedingress.GatewayV2LANGrantPendingPublished,
			generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation:
			outcome = string(disposition)
		}
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.events) < 64 {
		trace.events = append(trace.events, stage+":"+outcome)
	}
}

func (trace *lanTwoAppGrantTrace) recordDriver(stage string, ok bool) {
	outcome := "failed"
	if ok {
		outcome = "ok"
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.events) < 64 {
		trace.events = append(trace.events, stage+":"+outcome)
	}
}

func (trace *lanTwoAppGrantTrace) logFailure(t *testing.T) {
	if !t.Failed() {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	t.Logf("LAN grant closed trace: %s", strings.Join(trace.events, ","))
}

func (trace *lanTwoAppGrantTrace) GrantGatewayV2LAN(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	authorize generatedingress.GatewayV2LANGrantAuthorizer,
) (generatedingress.GatewayV2LANGrantResult, error) {
	tracedAuthorize := authorize
	if authorize != nil {
		tracedAuthorize = func(authorizeCtx context.Context, candidate generatedingress.GatewayV2LANGrantRequest) (generatedingress.GatewayV2LANGrantAuthorizationLease, error) {
			lease, err := authorize(authorizeCtx, candidate)
			trace.record("authorize", err, "")
			if err != nil || lease == nil {
				return lease, err
			}
			return lanTwoAppGrantLeaseTrace{GatewayV2LANGrantAuthorizationLease: lease, trace: trace}, nil
		}
	}
	result, err := trace.Manager.GrantGatewayV2LAN(ctx, request, tracedAuthorize)
	trace.record("grant", err, "")
	return result, err
}

func (trace *lanTwoAppGrantTrace) ObserveGatewayV2LAN(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest) (generatedingress.GatewayV2LANGrantObservation, error) {
	observation, err := trace.Manager.ObserveGatewayV2LAN(ctx, request)
	trace.record("observe", err, observation.Disposition)
	return observation, err
}

func (trace *lanTwoAppGrantTrace) WithGatewayV2LANCommitResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	resolve func(context.Context, generatedingress.GatewayV2LANGrantReceipt) error,
) error {
	err := trace.Manager.WithGatewayV2LANCommitResolution(ctx, request, func(callbackCtx context.Context, receipt generatedingress.GatewayV2LANGrantReceipt) error {
		callbackErr := resolve(callbackCtx, receipt)
		trace.record("commit_callback", callbackErr, "")
		return callbackErr
	})
	trace.record("commit_resolution", err, "")
	return err
}

func (trace *lanTwoAppGrantTrace) WithGatewayV2LANAbsenceResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	resolve func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	err := trace.Manager.WithGatewayV2LANAbsenceResolution(ctx, request, func(callbackCtx context.Context, observation generatedingress.GatewayV2LANGrantObservation) error {
		callbackErr := resolve(callbackCtx, observation)
		trace.record("absence_callback", callbackErr, observation.Disposition)
		return callbackErr
	})
	trace.record("absence_resolution", err, "")
	return err
}

func (trace *lanTwoAppGrantTrace) WithGatewayV2LANRollbackResolution(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	resolve func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	err := trace.Manager.WithGatewayV2LANRollbackResolution(ctx, request, func(callbackCtx context.Context, observation generatedingress.GatewayV2LANGrantObservation) error {
		callbackErr := resolve(callbackCtx, observation)
		trace.record("rollback_callback", callbackErr, observation.Disposition)
		return callbackErr
	})
	trace.record("rollback_resolution", err, "")
	return err
}

func (a *lanTwoAppAPI) do(method, path string, input any, want int, output any) []byte {
	a.t.Helper()
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			a.t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(a.ctx, method, a.base+path, body)
	if err != nil {
		a.t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: a.session})
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", a.csrf)
	}
	if method == http.MethodPost && strings.HasSuffix(path, "/deployments") {
		request.Header.Set("Idempotency-Key", uuid.NewString())
	}
	response, err := a.client.Do(request)
	if err != nil {
		a.t.Fatal("controller request failed:", err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		a.t.Fatal(err)
	}
	if response.StatusCode != want {
		a.t.Fatalf("controller %s %s: got %d want %d, problem=%s", method, path, response.StatusCode, want, controllerJourneyProblemCode(contents))
	}
	if output != nil && json.Unmarshal(contents, output) != nil {
		a.t.Fatalf("invalid controller response for %s %s", method, path)
	}
	return contents
}

type lanTwoAppDeployment struct {
	id           string
	deploymentID string
	plan         apicontract.DeploymentPlanRevision
	config       apicontract.ApplicationConfiguration
}

func lanTwoAppConfig(marker string) []apicontract.ScopedConfigurationValueInput {
	return []apicontract.ScopedConfigurationValueInput{
		{Phase: "runtime", TargetComponent: "api", Key: "API_RUNTIME_MARKER", Value: marker},
		{Phase: "build", TargetComponent: "frontend", Key: "VITE_BUILD_LABEL", Value: marker},
	}
}

func lanTwoAppDeploy(t *testing.T, api *lanTwoAppAPI, jobStore *jobs.Service, deploymentStore *deployments.Repository,
	state *generatedruntimestate.Repository, source apicontract.GitHubSource, setup apicontract.DeploymentSetupInput,
	name, marker string, appIDs *[]string,
) lanTwoAppDeployment {
	t.Helper()
	deployment := lanTwoAppCreateApplication(t, api, source, setup, name, appIDs)
	var saved apicontract.ApplicationConfiguration
	api.do(http.MethodPut, "/api/v1/apps/"+deployment.id+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
		ExpectedRevisionNumber: 0, PlanRevisionID: deployment.plan.RevisionID, PlanRevisionNumber: deployment.plan.RevisionNumber,
		PublicBuildDisclosureAcknowledged: true, Entries: lanTwoAppConfig(marker),
		Remove: []apicontract.ScopedConfigurationKey{},
	}, http.StatusOK, &saved)
	deployment.config = saved
	deployment.deploymentID = lanTwoAppDeployJob(t, api, jobStore, deploymentStore, state, deployment.id, deployment.plan, saved)
	return deployment
}

func lanTwoAppCreateApplication(t *testing.T, api *lanTwoAppAPI, source apicontract.GitHubSource,
	setup apicontract.DeploymentSetupInput, name string, appIDs *[]string,
) lanTwoAppDeployment {
	t.Helper()
	var inspection apicontract.InspectResponse
	api.do(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{GithubSource: source, Setup: &setup}, http.StatusOK, &inspection)
	if len(inspection.Analysis.Candidates) == 0 || inspection.Analysis.StructuralFingerprint == "" {
		t.Fatal("controlled source did not produce a generated plan candidate")
	}
	candidate := inspection.Analysis.Candidates[len(inspection.Analysis.Candidates)-1]
	var application apicontract.Application
	api.do(http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{
		Name: name, GithubSource: source, Setup: &setup,
	}, http.StatusCreated, &application)
	if application.ID == "" {
		t.Fatal("controller did not create application")
	}
	*appIDs = append(*appIDs, application.ID)
	var plan apicontract.DeploymentPlanRevision
	api.do(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		Setup: &setup, CandidateID: candidate.ID, ExpectedCandidateDigest: candidate.Digest,
		ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &plan)
	if plan.Strategy != string(deploymentplans.StrategyGeneratedNode) || len(plan.Components) != 2 {
		t.Fatal("reviewed fixture did not produce two generated components")
	}
	return lanTwoAppDeployment{id: application.ID, plan: plan}
}

func lanTwoAppDeployJob(t *testing.T, api *lanTwoAppAPI, jobStore *jobs.Service, deploymentStore *deployments.Repository,
	state *generatedruntimestate.Repository, appID string, plan apicontract.DeploymentPlanRevision,
	configuration apicontract.ApplicationConfiguration,
) string {
	t.Helper()
	path := "/api/v1/apps/" + appID + "/deployments"
	var mutation apicontract.JobMutationResponse
	// The ordinary controller request helper supplies session and CSRF. The
	// operation is unique because its exact plan/config pair changes on A.
	api.do(http.MethodPost, path, apicontract.DeployApplicationRequest{
		ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID:     configuration.RevisionID,
		ExpectedConfigurationRevisionNumber: configuration.RevisionNumber,
	}, http.StatusAccepted, &mutation)
	if mutation.Job.ID == "" || !mutation.Created {
		t.Fatal("controller did not enqueue a durable deployment")
	}
	for deadline := time.Now().Add(14 * time.Minute); ; time.Sleep(250 * time.Millisecond) {
		job, err := jobStore.Get(mutation.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == string(jobs.Succeeded) {
			break
		}
		if job.Status == string(jobs.Failed) || job.Status == string(jobs.NeedsAttention) ||
			job.Status == string(jobs.WaitingUser) || time.Now().After(deadline) || api.ctx.Err() != nil {
			lanTwoAppLogDeploymentFailure(t, api.ctx, appID, mutation.Job.ID, job, deploymentStore.List, state.Get)
			t.Fatal("generated deployment did not succeed; safe diagnostic logged")
		}
	}
	var history apicontract.DeploymentList
	api.do(http.MethodGet, path, nil, http.StatusOK, &history)
	if len(history.Items) == 0 || history.Items[0].JobID != mutation.Job.ID ||
		history.Items[0].ActualConfigurationRevisionID != configuration.RevisionID || history.Items[0].Status != "succeeded" {
		t.Fatal("durable deployment history does not match the completed job")
	}
	return history.Items[0].ID
}

func lanTwoAppGrant(t *testing.T, api *lanTwoAppAPI, access *appaccess.Repository,
	profile apicontract.LANGatewayProfileRevision, appID, actorID string,
) uint16 {
	t.Helper()
	path := "/api/v1/apps/" + appID + "/lan-access"
	var initial apicontract.LANAppAccessRead
	api.do(http.MethodGet, path, nil, http.StatusOK, &initial)
	if initial.ExpectedRevisionNumber != 0 || initial.Url != "" {
		t.Fatal("new app unexpectedly has a LAN claim")
	}
	var reserved apicontract.LANAppAccessReservationMutation
	api.do(http.MethodPost, path+"/reservations", apicontract.ReserveLANAppAccessRequest{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	}, http.StatusCreated, &reserved)
	if reserved.Allocation.AppID != appID || reserved.ApprovalDigest == "" {
		t.Fatal("reservation did not persist exact app and approval digest")
	}
	var approved apicontract.LANAppAccessApprovalMutation
	api.do(http.MethodPost, path+"/approval", apicontract.ApproveLANAppAccessRequest{
		OperationID: reserved.Allocation.OwnerOperationID, AllocationID: reserved.Allocation.ID,
		ExpectedRevisionNumber: 0, ApprovalDigest: reserved.ApprovalDigest,
	}, http.StatusCreated, &approved)
	if approved.Revision.ApprovedBy != actorID || approved.Revision.Allocation.ID != reserved.Allocation.ID {
		t.Fatal("access approval did not retain administrator and allocation identity")
	}
	var granted apicontract.LANAppGrantMutation
	api.do(http.MethodPost, path+"/grants", apicontract.GrantLANAppAccessRequest{
		AttemptID: uuid.NewString(), AccessRevisionID: approved.Revision.ID,
		AccessRevisionNumber: approved.Revision.RevisionNumber, ApprovalDigest: approved.Revision.SpecDigest,
	}, http.StatusCreated, &granted)
	if granted.Claim.State != "committed" || granted.Claim.AppID != appID || granted.Claim.AllocationID != reserved.Allocation.ID {
		t.Fatal("grant did not commit its exact app allocation")
	}
	current, err := access.CurrentAppAccess(api.ctx, appID)
	if err != nil || current.ID != approved.Revision.ID || current.Allocation.State != appaccess.AllocationActive ||
		current.Allocation.Port != uint16(reserved.Allocation.Port) {
		t.Fatal("SQLite does not retain the committed LAN allocation")
	}
	return current.Allocation.Port
}

func urlQuery(value string) string { return url.QueryEscape(value) }

func lanTwoAppHostPorts(t *testing.T) (hostnetwork.Candidate, [3]uint16) {
	t.Helper()
	candidates, err := hostnetwork.CurrentCandidates()
	if err != nil {
		t.Fatal("enumerate private host interfaces:", err)
	}
	for _, candidate := range candidates {
		if _, err := hostnetwork.Select(candidates, candidate.InterfaceID, candidate.IPv4); err != nil {
			continue
		}
		for first := appaccess.GatewayPortStart; first+2 <= appaccess.GatewayPortEnd; first++ {
			var ports [3]uint16
			available := true
			for offset := range ports {
				port := first + uint16(offset)
				for _, address := range []string{candidate.IPv4, "127.0.0.1"} {
					listener, err := net.Listen("tcp4", net.JoinHostPort(address, strconv.Itoa(int(port))))
					if err != nil {
						available = false
						break
					}
					if err := listener.Close(); err != nil {
						t.Fatal("release LAN port preflight:", err)
					}
				}
				ports[offset] = port
				if !available {
					break
				}
			}
			if available {
				return candidate, ports
			}
		}
	}
	t.Fatal("no unique private interface has three adjacent free LAN ports")
	return hostnetwork.Candidate{}, [3]uint16{}
}

func lanTwoAppDockerPreflight(t *testing.T, ctx context.Context, docker string) {
	t.Helper()
	contextName, err := controllerJourneyDocker(ctx, docker, nil, "context", "show")
	if err != nil || string(bytes.TrimSpace(contextName)) != "default" {
		t.Fatal("Docker must use the default context")
	}
	endpoint, err := controllerJourneyDocker(ctx, docker, nil, "context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil || !strings.HasPrefix(string(bytes.TrimSpace(endpoint)), "unix:///") {
		t.Fatal("Docker must use the local Unix endpoint")
	}
	if _, err := controllerJourneyDocker(ctx, docker, nil, "info"); err != nil {
		t.Fatal("local Docker daemon unavailable")
	}
	for _, resource := range []struct{ kind, name string }{
		{"container", "rig-generated-caddy-v1"}, {"volume", "rig-generated-caddy-config-v1"},
		{"network", "rig-generated-caddy-ingress-v1"},
		{"container", "rig-generated-caddy-v2"}, {"volume", "rig-generated-caddy-config-v2"},
		{"volume", "rig-generated-caddy-data-v2"}, {"network", "rig-generated-caddy-ingress-v2"},
	} {
		if controllerJourneyExists(t, ctx, docker, resource.kind, resource.name) {
			t.Fatalf("disposable daemon already has %s %s", resource.kind, resource.name)
		}
	}
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", "label=io.rig.managed"},
		{"network", "ls", "-q", "--filter", "label=io.rig.managed"},
		{"volume", "ls", "-q", "--filter", "label=io.rig.managed"},
		{"image", "ls", "-q", "--filter", "label=io.rig.managed=generated-image"},
		{"ps", "-aq", "--filter", "label=rig.controller=generated-builder"},
		{"network", "ls", "-q", "--filter", "label=rig.controller=generated-builder"},
	} {
		found, err := controllerJourneyDocker(ctx, docker, nil, args...)
		if err != nil || len(bytes.TrimSpace(found)) != 0 {
			t.Fatal("disposable daemon contains preexisting Rig-owned Docker resources")
		}
	}
}

func lanTwoAppProbe(t *testing.T, ctx context.Context, address string, port uint16, host, path string, want int, marker string) []byte {
	t.Helper()
	url := fmt.Sprintf("http://%s:%d%s", address, port, path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	client := &http.Client{
		Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("LAN route %s host=%s was unreachable: %v", url, host, err)
	}
	defer response.Body.Close()
	const maxProbeBody = 4 << 20
	content, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBody+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > maxProbeBody {
		t.Fatalf("LAN route %s host=%s exceeded the probe response limit", url, host)
	}
	if response.StatusCode != want || (marker != "" && !bytes.Contains(content, []byte(marker))) {
		t.Fatalf("LAN route %s host=%s: status=%d want=%d markerPresent=%t", url, host,
			response.StatusCode, want, bytes.Contains(content, []byte(marker)))
	}
	return content
}

func lanTwoAppAssertLoopback(t *testing.T, ctx context.Context, appID, marker string) {
	t.Helper()
	lanTwoAppProbe(t, ctx, "127.0.0.1", 8080, appID+".rig.localhost", "/api/version", http.StatusOK, marker)
}

func lanTwoAppProbeMatrix(t *testing.T, ctx context.Context, address string, portA, portB, unused uint16, markerA, markerB string) {
	t.Helper()
	for _, app := range []struct {
		port          uint16
		marker, other string
	}{{portA, markerA, markerB}, {portB, markerB, markerA}} {
		version := lanTwoAppProbe(t, ctx, address, app.port, address, "/api/version", http.StatusOK, app.marker)
		if bytes.Contains(version, []byte(app.other)) {
			t.Fatal("LAN route disclosed the other application's marker")
		}
		lanTwoAppProbe(t, ctx, address, app.port, net.JoinHostPort(address, strconv.Itoa(int(app.port))),
			"/api/version", http.StatusOK, app.marker)
		index := lanTwoAppProbe(t, ctx, address, app.port, address, "/", http.StatusOK, "<html")
		asset := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindSubmatch(index)
		if len(asset) != 2 {
			t.Fatal("LAN SPA has no built JavaScript asset")
		}
		bundle := lanTwoAppProbe(t, ctx, address, app.port, address, string(asset[1]), http.StatusOK, app.marker)
		if bytes.Contains(bundle, []byte(app.other)) {
			t.Fatal("LAN SPA bundle disclosed the other application's public marker")
		}
		lanTwoAppProbe(t, ctx, address, app.port, address, "/api/events", http.StatusOK, app.marker)
		lanTwoAppProbe(t, ctx, address, app.port, "wrong.invalid", "/api/version", http.StatusNotFound, "")
		lanTwoAppProbe(t, ctx, address, app.port, "127.0.0.1", "/api/version", http.StatusNotFound, "")
	}
	lanTwoAppProbe(t, ctx, address, portA, address, "/api/version", http.StatusOK, markerA)
	lanTwoAppProbe(t, ctx, address, portB, address, "/api/version", http.StatusOK, markerB)
	lanTwoAppProbe(t, ctx, address, unused, address, "/api/version", http.StatusNotFound, "")
	lanTwoAppProbe(t, ctx, address, unused, net.JoinHostPort(address, strconv.Itoa(int(unused))),
		"/", http.StatusNotFound, "")
	lanTwoAppProbe(t, ctx, address, unused, "wrong.invalid", "/", http.StatusNotFound, "")
}

func lanTwoAppAssertBindings(t *testing.T, ctx context.Context, docker, operationID, address string, ports [3]uint16) {
	t.Helper()
	var inspection struct {
		ID      string            `json:"id"`
		Labels  map[string]string `json:"labels"`
		Running bool              `json:"running"`
		PortMap map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"portMap"`
	}
	format := `{"id":{{json .ID}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"portMap":{{json .HostConfig.PortBindings}}}`
	body, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", format, "rig-generated-caddy-v2")
	if err != nil || json.Unmarshal(bytes.TrimSpace(body), &inspection) != nil || !inspection.Running ||
		inspection.Labels["io.rig.managed"] != "generated-ingress" ||
		inspection.Labels["io.rig.identity-version"] != "v2" ||
		inspection.Labels["io.rig.operation-id"] != operationID ||
		inspection.Labels["io.rig.gateway-role"] != "final" || len(inspection.PortMap) != 4 {
		t.Fatal("committed gateway has no exact owned Docker binding map")
	}
	for _, port := range ports {
		key := strconv.Itoa(int(port)) + "/tcp"
		bindings := inspection.PortMap[key]
		if len(bindings) != 1 || bindings[0].HostIP != address || bindings[0].HostPort != strconv.Itoa(int(port)) {
			t.Fatalf("LAN port %d is not bound only to the approved interface", port)
		}
	}
	local := inspection.PortMap["8080/tcp"]
	if len(local) != 1 || local[0].HostIP != "127.0.0.1" || local[0].HostPort != "8080" {
		t.Fatal("gateway controller-host route lost its loopback-only publication")
	}
}

func lanTwoAppWriteNote(t *testing.T, ctx context.Context, address string, port uint16, body string, want int) {
	t.Helper()
	endpoint := fmt.Sprintf("http://%s:%d/api/notes", address, port)
	encoded, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("LAN note write failed:", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("LAN note write status=%d want=%d", response.StatusCode, want)
	}
}

// This client has its own bridge and network namespace. It cannot inherit the
// controller process's host loopback or share the ingress container namespace.
func lanTwoAppNamespaceProbe(t *testing.T, ctx context.Context, docker, address string, portA, portB, unused uint16,
	controllerPort int, appAID, appBID string, clientSource []byte, markerA, markerB string,
) {
	t.Helper()
	clientOperationID := uuid.NewString()
	networkName := lanTwoAppClientNetworkPrefix + "-" + strings.ReplaceAll(clientOperationID, "-", "")[:12]
	created, err := controllerJourneyDocker(ctx, docker, nil, "network", "create", "--driver", "bridge",
		"--label", "io.rig.managed=lan-test-client", "--label", "io.rig.operation-id="+clientOperationID,
		networkName)
	if err != nil {
		t.Fatal("create separate Docker client network")
	}
	networkID := string(bytes.TrimSpace(created))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		containers, err := controllerJourneyDocker(cleanup, docker, nil, "ps", "-aq", "--filter", "label=io.rig.operation-id="+clientOperationID)
		if err != nil {
			t.Error("list exact client containers for cleanup")
		} else {
			for _, id := range strings.Fields(string(containers)) {
				labels, inspectErr := lanTwoAppDockerLabels(cleanup, docker, "container", id)
				if inspectErr != nil || labels["io.rig.managed"] != "lan-test-client" || labels["io.rig.operation-id"] != clientOperationID {
					t.Error("client container ownership uncertain; retaining")
					continue
				}
				if _, err := controllerJourneyDocker(cleanup, docker, nil, "container", "rm", "--force", id); err != nil {
					t.Error("remove exact client container")
				}
			}
		}
		if !controllerJourneyExists(t, cleanup, docker, "network", networkName) {
			return
		}
		var inspected struct {
			ID     string            `json:"Id"`
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		body, err := controllerJourneyDocker(cleanup, docker, nil, "network", "inspect", networkName)
		var rows []struct {
			ID     string            `json:"Id"`
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		if err != nil || json.Unmarshal(body, &rows) != nil || len(rows) != 1 {
			t.Error("client network ownership unavailable; retaining")
			return
		}
		inspected = rows[0]
		if inspected.ID != networkID || inspected.Name != networkName ||
			inspected.Labels["io.rig.managed"] != "lan-test-client" || inspected.Labels["io.rig.operation-id"] != clientOperationID {
			t.Error("client network identity changed; retaining")
			return
		}
		if _, err := controllerJourneyDocker(cleanup, docker, nil, "network", "rm", networkID); err != nil {
			t.Error("remove exact separate client network")
		}
	})
	args := []string{"run", "--rm", "--network", networkName,
		"--label", "io.rig.managed=lan-test-client", "--label", "io.rig.operation-id=" + clientOperationID,
		"--entrypoint", "node",
		lanTwoAppNodeImage, "-e", string(clientSource), "--", "--rig-lan-transport-client",
		address, strconv.Itoa(int(portA)), strconv.Itoa(int(portB)), strconv.Itoa(int(unused)), strconv.Itoa(controllerPort),
		appAID, appBID, markerA, markerB}
	if _, err := controllerJourneyDocker(ctx, docker, nil, args...); err != nil {
		t.Fatal("independent Docker client failed LAN/Host/controller denial probes")
	}
}

type lanTwoAppLegacyCleanupGuard func(context.Context, string, string, string, map[string]string) (string, bool)

func lanTwoAppRegisterCleanup(t *testing.T, docker, dataRoot, upgradeID string, appIDs *[]string) {
	lanTwoAppRegisterCleanupWithLegacyGuard(t, docker, dataRoot, upgradeID, appIDs, nil)
}

// lanTwoAppRegisterCleanupWithLegacyGuard keeps the original two-app cleanup
// behavior for existing callers. A fixture that has retained a v1 predecessor
// may supply an exact captured-identity guard before this callback removes the
// canonical v1 resources by name.
func lanTwoAppRegisterCleanupWithLegacyGuard(t *testing.T, docker, dataRoot, upgradeID string, appIDs *[]string,
	legacyGuard lanTwoAppLegacyCleanupGuard,
) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		// Stop only containers bound to the two IDs created by this test. The
		// global ingress is removed next so the app bridges can be detached.
		for _, appID := range *appIDs {
			ids, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-aq", "--filter", "label=io.rig.application="+appID)
			if err != nil {
				t.Error("list app-owned containers for cleanup")
				continue
			}
			for _, id := range strings.Fields(string(ids)) {
				if !lanTwoAppRemoveOwned(t, ctx, docker, "container", id, appID, "generated-runtime", "") {
					t.Error("retain uncertain app container")
				}
			}
		}
		for _, resource := range []struct{ kind, name, managed, version, role string }{
			{"container", "rig-generated-caddy-v2-stage-" + upgradeID, "generated-ingress", "v2", "stage"},
			{"container", "rig-generated-caddy-v2", "generated-ingress", "v2", "final"},
			{"container", "rig-generated-caddy-v1", "generated-ingress", "v1", ""},
			{"network", "rig-generated-caddy-ingress-v2", "generated-ingress-network", "v2", "ingress-network"},
			{"network", "rig-generated-caddy-ingress-v1", "generated-ingress-network", "v1", ""},
			{"volume", "rig-generated-caddy-config-v2", "generated-ingress", "v2", "config-volume"},
			{"volume", "rig-generated-caddy-data-v2", "generated-ingress", "v2", "data-volume"},
			{"volume", "rig-generated-caddy-config-v1", "generated-ingress", "v1", ""},
		} {
			if !controllerJourneyExists(t, ctx, docker, resource.kind, resource.name) {
				continue
			}
			labels, err := lanTwoAppDockerLabels(ctx, docker, resource.kind, resource.name)
			if err != nil || labels["io.rig.managed"] != resource.managed || labels["io.rig.identity-version"] != resource.version ||
				(resource.version == "v2" && (labels["io.rig.operation-id"] != upgradeID || labels["io.rig.gateway-role"] != resource.role)) {
				t.Errorf("retain uncertain %s %s", resource.kind, resource.name)
				continue
			}
			removalIdentity := resource.name
			if resource.version == "v1" && legacyGuard != nil {
				var verified bool
				removalIdentity, verified = legacyGuard(ctx, docker, resource.kind, resource.name, labels)
				if !verified {
					t.Errorf("retain uncertain %s %s", resource.kind, resource.name)
					continue
				}
			}
			args := []string{resource.kind, "rm", removalIdentity}
			if resource.kind == "container" {
				args = []string{"container", "rm", "--force", removalIdentity}
			}
			if _, err := controllerJourneyDocker(ctx, docker, nil, args...); err != nil {
				t.Errorf("remove exact %s %s", resource.kind, resource.name)
			}
		}
		controllerJourneyRemoveBuilder(t, ctx, docker, dataRoot)
		for _, appID := range *appIDs {
			network, err := generatedruntime.DescribeAppNetwork(appID)
			if err != nil {
				t.Error("describe app network for cleanup")
				continue
			}
			if controllerJourneyExists(t, ctx, docker, "network", network.Name) &&
				!lanTwoAppRemoveOwned(t, ctx, docker, "network", network.Name, appID, generatedruntime.NetworkOwnershipLabelValue, "") {
				t.Error("retain uncertain app network")
			}
			ids, err := controllerJourneyDocker(ctx, docker, nil, "image", "ls", "-q", "--filter", "label=io.rig.application="+appID)
			if err != nil {
				t.Error("list app-owned images for cleanup")
				continue
			}
			seen := make(map[string]bool)
			for _, id := range strings.Fields(string(ids)) {
				if !seen[id] {
					seen[id] = true
					if !lanTwoAppRemoveOwned(t, ctx, docker, "image", id, appID, "generated-image", "") {
						t.Error("retain uncertain app image")
					}
				}
			}
		}
		for _, args := range [][]string{
			{"ps", "-aq", "--filter", "label=io.rig.managed"},
			{"network", "ls", "-q", "--filter", "label=io.rig.managed"},
			{"volume", "ls", "-q", "--filter", "label=io.rig.managed"},
			{"image", "ls", "-q", "--filter", "label=io.rig.managed=generated-image"},
		} {
			remaining, err := controllerJourneyDocker(ctx, docker, nil, args...)
			if err != nil || len(bytes.TrimSpace(remaining)) != 0 {
				t.Error("Rig-owned Docker resources remain after exact cleanup")
			}
		}
	})
}

func lanTwoAppDockerLabels(ctx context.Context, docker, kind, name string) (map[string]string, error) {
	format := "{{json .Labels}}"
	if kind == "container" || kind == "image" {
		format = "{{json .Config.Labels}}"
	}
	result, err := controllerJourneyDocker(ctx, docker, nil, kind, "inspect", "--format", format, name)
	if err != nil {
		return nil, err
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(result), &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

func lanTwoAppRemoveOwned(t *testing.T, ctx context.Context, docker, kind, name, appID, managed, operationID string) bool {
	t.Helper()
	labels, err := lanTwoAppDockerLabels(ctx, docker, kind, name)
	if err != nil || labels["io.rig.managed"] != managed || labels["io.rig.application"] != appID ||
		(operationID != "" && labels["io.rig.operation-id"] != operationID) {
		return false
	}
	args := []string{kind, "rm", name}
	if kind == "container" || kind == "image" {
		args = []string{kind, "rm", "--force", name}
	}
	_, err = controllerJourneyDocker(ctx, docker, nil, args...)
	return err == nil
}
