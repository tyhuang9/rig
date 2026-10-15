//go:build live_docker

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/hostnetwork"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const liveHostdGatewayRebindEnvironment = "RIG_RUN_LIVE_HOSTD_GATEWAY_REBIND"

type liveHostdGatewayRebindFixture struct {
	ctx         context.Context
	cancel      context.CancelFunc
	root        string
	dataRoot    string
	docker      string
	listen      string
	predecessor hostnetwork.Candidate
	successor   hostnetwork.Candidate
	ports       [3]uint16
	user        auth.User
	session     auth.Session
	app         lanTwoAppDeployment
	secondApp   lanTwoAppDeployment
	appIDs      []string
	upgradeID   string

	mu           sync.Mutex
	operationIDs map[string]struct{}
}

// newLiveHostdGatewayRebindFixture creates the predecessor through the same
// production stores, Docker runtime and authenticated controller handlers used
// by the existing two-app LAN gate. It stops the setup worker and private setup
// server before returning. Every rebind acceptance action is then made against
// a separately executed full hostd child.
func newLiveHostdGatewayRebindFixture(t *testing.T) *liveHostdGatewayRebindFixture {
	t.Helper()
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" ||
		os.Getenv("RIG_RUN_LIVE_GATEWAY_REBIND_CROSS_STORE") != "1" ||
		os.Getenv("RIG_RUN_LIVE_GATEWAY_REBIND_RUNTIME") != "1" ||
		os.Getenv(liveHostdGatewayRebindEnvironment) != "1" {
		t.Fatal("set all supported hostd rebind flags on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("supported hostd rebind requires the default local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Hour+40*time.Minute)
	t.Cleanup(cancel)
	lanTwoAppDockerPreflight(t, ctx, docker)
	predecessor, successor := liveHostdGatewayRebindAddresses(t)
	ports := liveHostdGatewayRebindPorts(t, predecessor.IPv4, successor.IPv4)
	root := t.TempDir()
	dataRoot := filepath.Join(root, "hostd-supported-rebind")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &liveHostdGatewayRebindFixture{
		ctx: ctx, cancel: cancel, root: root, dataRoot: dataRoot, docker: docker,
		predecessor: predecessor, successor: successor, ports: ports,
		upgradeID: uuid.NewString(), operationIDs: make(map[string]struct{}),
	}
	fixture.operationIDs[fixture.upgradeID] = struct{}{}
	lanTwoAppRegisterCleanup(t, docker, dataRoot, fixture.upgradeID, &fixture.appIDs)
	t.Cleanup(func() { fixture.cleanup(t) })

	installed, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := controllerJourneyTrackedFixtureFiles(installed)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := controllerJourneyStageSource(installed, source, tracked); err != nil {
		t.Fatal(err)
	}
	transportServer := lanTwoAppTransportFixture(t, installed, "lan-transport-server.mjs")
	if err := os.WriteFile(filepath.Join(source, "api", "src", "server.js"), transportServer, 0o600); err != nil {
		t.Fatal(err)
	}
	provider := controllerJourneyNewGitHubProvider(t, source)
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	setupClosed := false
	defer func() {
		if !setupClosed {
			_ = db.Close()
		}
	}()
	authentication := auth.New(db)
	bootstrap, err := authentication.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	fixture.user, fixture.session, err = authentication.Bootstrap(bootstrap,
		"supported-rebind-admin", "a sufficiently long disposable passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	applicationStore, jobStore := apps.New(db), jobs.New(db)
	configurationStore, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	planStore, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceClock := time.Now().UTC()
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider,
		sourceconnections.NewFileCredentialStore(dataRoot), "fixture-app", func() time.Time { return sourceClock })
	snapshots, err := releasesnapshot.New(db, sources, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentStore := deployments.New(db)
	settings := config.Defaults()
	settings.DataRoot, settings.GeneratedRuntime = dataRoot, true
	composition, err := prepareRuntimeComposition(ctx, settings, runtimeCompositionDependencies{
		db: db, applications: applicationStore, snapshots: snapshots, configuration: configurationStore,
		deployments: deploymentStore, plans: planStore,
	}, runtimeCompositionOptions{dockerExecutable: docker, runner: runtimeprocess.ExecRunner{}})
	if err != nil {
		t.Fatal("compose supported hostd rebind baseline:", err)
	}
	access := appaccess.New(db)
	setupServer := httptest.NewServer((&controller.Server{
		Auth: authentication, Apps: applicationStore, Jobs: jobStore, Machines: machineStore, Sources: sources,
		Configuration: configurationStore, Deployments: deploymentStore, DeploymentPlans: planStore,
		GeneratedIngress: composition.ingress, GeneratedRuntimeState: composition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: dataRoot,
		GatewayProfiles: access, GatewayUpgrades: access, GatewayUpgradeRuntime: composition.ingress,
		AppAccess: access, AppGrants: access, LANGrantRuntime: composition.ingress,
		AppDisables: access, LANDisableRuntime: composition.ingress,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	t.Cleanup(setupServer.Close)
	setupAPI := lanTwoAppAPI{t: t, ctx: ctx, base: setupServer.URL,
		session: fixture.session.Token, csrf: fixture.session.CSRF,
		client: &http.Client{Timeout: 5 * time.Minute}}

	var setup apicontract.DeploymentSetupInput
	setupBytes, err := os.ReadFile(filepath.Join(source, "rig-setup.json"))
	if err != nil || json.Unmarshal(setupBytes, &setup) != nil {
		t.Fatal("read reviewed generated build recipe")
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone, err := prepareRuntimeWorker(workerCtx, runtimeRecovery{
		deployments: deploymentStore.Recover, jobs: jobStore.RecoverInterrupted,
	}, composition.executor, jobStore.RunWorker, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(10 * time.Second):
			t.Error("baseline setup worker did not stop during cleanup")
		}
	})
	// The full hostd children intentionally run without a GitHub provider. Keep
	// the baseline applications local so a later public deployment request can
	// materialize its release through the same child rather than failing before
	// it reaches route publication.
	fixture.app = liveHostdRebindDeployLocal(t, &setupAPI, jobStore, deploymentStore, composition.state,
		source, setup, "Supported rebind A", "supported-rebind-a-v1", &fixture.appIDs)
	fixture.secondApp = liveHostdRebindDeployLocal(t, &setupAPI, jobStore, deploymentStore, composition.state,
		source, setup, "Supported rebind B", "supported-rebind-b-v1", &fixture.appIDs)

	predecessorQuery := fmt.Sprintf("?interfaceId=%s&selectedIpv4=%s&portStart=%d&portEnd=%d",
		urlQuery(predecessor.InterfaceID), urlQuery(predecessor.IPv4), ports[0], ports[2])
	var predecessorRead apicontract.LANGatewayProfileRead
	setupAPI.do(http.MethodGet, "/api/v1/system/lan-gateway-profile"+predecessorQuery, nil, http.StatusOK, &predecessorRead)
	if predecessorRead.Proposal == nil || predecessorRead.ExpectedRevisionNumber != 0 {
		t.Fatal("baseline predecessor profile proposal is unavailable")
	}
	var predecessorProfile apicontract.LANGatewayProfileMutation
	setupAPI.do(http.MethodPost, "/api/v1/system/lan-gateway-profile", apicontract.ConfigureLANGatewayProfileRequest{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, Spec: predecessorRead.Proposal.Spec,
		ApprovalDigest: predecessorRead.Proposal.ApprovalDigest,
	}, http.StatusCreated, &predecessorProfile)
	var upgradeRead apicontract.LANGatewayUpgradeRead
	setupAPI.do(http.MethodGet, "/api/v1/system/lan-gateway-upgrade", nil, http.StatusOK, &upgradeRead)
	if upgradeRead.Proposal == nil || upgradeRead.Proposal.ProfileRevisionID != predecessorProfile.Profile.ID {
		t.Fatal("baseline gateway upgrade proposal is unavailable")
	}
	var upgraded apicontract.LANGatewayUpgradeMutation
	setupAPI.do(http.MethodPost, "/api/v1/system/lan-gateway-upgrade", apicontract.UpgradeLANGatewayRequest{
		OperationID: fixture.upgradeID, ProfileRevisionID: predecessorProfile.Profile.ID,
		ProfileRevisionNumber: predecessorProfile.Profile.RevisionNumber,
		ActionDigest:          upgradeRead.Proposal.ActionDigest,
	}, http.StatusCreated, &upgraded)
	if upgraded.Claim.State != "committed" {
		t.Fatal("baseline gateway upgrade did not commit")
	}
	firstPort := lanTwoAppGrant(t, &setupAPI, access, predecessorProfile.Profile, fixture.app.id, fixture.user.ID)
	secondPort := lanTwoAppGrant(t, &setupAPI, access, predecessorProfile.Profile, fixture.secondApp.id, fixture.user.ID)
	if firstPort != ports[0] || secondPort != ports[1] {
		t.Fatalf("baseline LAN grant ports=(%d,%d) want=(%d,%d)", firstPort, secondPort, ports[0], ports[1])
	}
	lanTwoAppProbe(t, ctx, predecessor.IPv4, firstPort, predecessor.IPv4,
		"/api/version", http.StatusOK, "supported-rebind-a-v1")
	lanTwoAppProbe(t, ctx, predecessor.IPv4, secondPort, predecessor.IPv4,
		"/api/version", http.StatusOK, "supported-rebind-b-v1")

	stopWorker()
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("baseline setup worker did not stop")
	}
	setupServer.Close()
	if err := db.Close(); err != nil {
		t.Fatal("close baseline database before full hostd start:", err)
	}
	setupClosed = true
	fixture.listen = liveHostdGatewayRebindListenAddress(t)
	return fixture
}

func liveHostdRebindDeployLocal(t *testing.T, api *lanTwoAppAPI, jobStore *jobs.Service,
	deploymentStore *deployments.Repository, state *generatedruntimestate.Repository,
	source string, setup apicontract.DeploymentSetupInput, name, marker string, appIDs *[]string,
) lanTwoAppDeployment {
	t.Helper()
	var inspection apicontract.InspectResponse
	api.do(http.MethodPost, "/api/v1/apps/import/inspect", apicontract.InspectRequest{
		SourcePath: source, Setup: &setup,
	}, http.StatusOK, &inspection)
	if len(inspection.Analysis.Candidates) == 0 || inspection.Analysis.StructuralFingerprint == "" {
		t.Fatal("controlled local source did not produce a generated plan candidate")
	}
	candidate := inspection.Analysis.Candidates[len(inspection.Analysis.Candidates)-1]
	var application apicontract.Application
	api.do(http.MethodPost, "/api/v1/apps", apicontract.CreateApplicationRequest{
		Name: name, SourcePath: source, Setup: &setup,
	}, http.StatusCreated, &application)
	if application.ID == "" {
		t.Fatal("controller did not create local application")
	}
	*appIDs = append(*appIDs, application.ID)
	var plan apicontract.DeploymentPlanRevision
	api.do(http.MethodPut, "/api/v1/apps/"+application.ID+"/deployment-plan", apicontract.AcceptDeploymentPlanRequest{
		Setup: &setup, CandidateID: candidate.ID, ExpectedCandidateDigest: candidate.Digest,
		ExpectedSourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
	}, http.StatusOK, &plan)
	if plan.Strategy != string(deploymentplans.StrategyGeneratedNode) || len(plan.Components) != 2 {
		t.Fatal("reviewed local fixture did not produce two generated components")
	}
	var saved apicontract.ApplicationConfiguration
	api.do(http.MethodPut, "/api/v1/apps/"+application.ID+"/scoped-configuration",
		apicontract.ReplaceScopedApplicationConfigurationRequest{
			ExpectedRevisionNumber: 0, PlanRevisionID: plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
			PublicBuildDisclosureAcknowledged: true, Entries: lanTwoAppConfig(marker),
			Remove: []apicontract.ScopedConfigurationKey{},
		}, http.StatusOK, &saved)
	deploymentID := lanTwoAppDeployJob(t, api, jobStore, deploymentStore, state, application.ID, plan, saved)
	return lanTwoAppDeployment{id: application.ID, deploymentID: deploymentID, plan: plan, config: saved}
}

func (f *liveHostdGatewayRebindFixture) args() []string {
	return []string{"--data-root", f.dataRoot, "--listen", f.listen,
		"--generated-runtime", "--github-connections=false"}
}

func (f *liveHostdGatewayRebindFixture) registerOperation(operationID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.operationIDs[operationID] = struct{}{}
}

func (f *liveHostdGatewayRebindFixture) operationOwned(operationID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.operationIDs[operationID]
	return ok
}

// cleanup runs before the older two-app cleanup. The preflight established an
// empty daemon, so every accepted managed resource must bind either an exact
// operation or application created by this fixture. Anything else is retained
// and reported instead of being guessed away.
func (f *liveHostdGatewayRebindFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	appOwned := func(appID string) bool {
		for _, value := range f.appIDs {
			if value == appID {
				return true
			}
		}
		return false
	}
	for _, kind := range []string{"container", "network", "volume", "image"} {
		args := []string{kind, "ls", "-q", "--filter", "label=io.rig.managed"}
		if kind == "container" {
			args = []string{"ps", "-aq", "--filter", "label=io.rig.managed"}
		}
		ids, err := controllerJourneyDocker(ctx, f.docker, nil, args...)
		if err != nil {
			t.Errorf("list %s cleanup candidates", kind)
			continue
		}
		for _, id := range strings.Fields(string(ids)) {
			labels, labelErr := lanTwoAppDockerLabels(ctx, f.docker, kind, id)
			operationID, appID := labels["io.rig.operation-id"], labels["io.rig.application"]
			if labelErr != nil || (!f.operationOwned(operationID) && !appOwned(appID)) {
				t.Errorf("retain uncertain managed %s %s", kind, id)
				continue
			}
			remove := []string{kind, "rm", id}
			if kind == "container" || kind == "image" {
				remove = []string{kind, "rm", "--force", id}
			}
			if _, err := controllerJourneyDocker(ctx, f.docker, nil, remove...); err != nil {
				t.Errorf("remove exact managed %s %s", kind, id)
			}
		}
	}
	controllerJourneyRemoveBuilder(t, ctx, f.docker, f.dataRoot)
}

func liveHostdGatewayRebindListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve full hostd listener:", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal("release full hostd listener reservation:", err)
	}
	return address
}

func liveHostdGatewayRebindPorts(t *testing.T, predecessor, successor string) [3]uint16 {
	t.Helper()
	for first := appaccess.GatewayPortStart; first+2 <= appaccess.GatewayPortEnd; first++ {
		var result [3]uint16
		available := true
		for offset := range result {
			port := first + uint16(offset)
			for _, address := range []string{predecessor, successor, "127.0.0.1"} {
				listener, err := net.Listen("tcp4", net.JoinHostPort(address, strconv.Itoa(int(port))))
				if err != nil {
					available = false
					break
				}
				if err := listener.Close(); err != nil {
					t.Fatal("release rebind port preflight:", err)
				}
			}
			result[offset] = port
			if !available {
				break
			}
		}
		if available {
			return result
		}
	}
	t.Fatal("no three adjacent ports are free on both rebind addresses")
	return [3]uint16{}
}

// liveHostdGatewayRebindAddresses owns one disposable dummy adapter with two
// private addresses. This is a hosted-Docker fixture only; it is not the
// physical second-device qualification gate.
func liveHostdGatewayRebindAddresses(t *testing.T) (hostnetwork.Candidate, hostnetwork.Candidate) {
	t.Helper()
	snapshot, err := hostnetwork.CurrentIPv4NetworkSnapshot()
	if err != nil {
		t.Fatal("read host network snapshot")
	}
	var prefix netip.Prefix
	for _, candidate := range []string{"192.168.252.248/30", "172.28.252.248/30", "10.252.252.248/30"} {
		value := netip.MustParsePrefix(candidate)
		if hostnetwork.CheckDockerIngressSubnetNonoverlap(value, snapshot) == nil {
			prefix = value
			break
		}
	}
	if !prefix.IsValid() {
		t.Fatal("no nonoverlapping private prefix for the full hostd fixture")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal("iproute2 is required")
	}
	var nonce [5]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(nonce[:])
	name, alias := "righ"+suffix, "rig-supported-hostd-rebind:"+suffix
	command := func(ctx context.Context, args ...string) ([]byte, error) {
		if os.Geteuid() == 0 {
			return exec.CommandContext(ctx, ip, args...).Output()
		}
		sudo, lookupErr := exec.LookPath("sudo")
		if lookupErr != nil {
			return nil, lookupErr
		}
		return exec.CommandContext(ctx, sudo, append([]string{"-n", ip}, args...)...).Output()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := command(ctx, "link", "add", "name", name, "type", "dummy"); err != nil {
		t.Fatal("create test-owned adapter")
	}
	created, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal("read test-owned adapter")
	}
	first, second := prefix.Addr().Next(), prefix.Addr().Next().Next()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		current, currentErr := net.InterfaceByName(name)
		if currentErr != nil || current.Index != created.Index || current.Name != name {
			t.Error("test adapter identity changed; retaining it")
			return
		}
		body, inspectErr := command(cleanupCtx, "-json", "-details", "link", "show", "dev", name)
		var links []struct {
			Index int    `json:"ifindex"`
			Name  string `json:"ifname"`
			Alias string `json:"ifalias"`
			Info  struct {
				Kind string `json:"info_kind"`
			} `json:"linkinfo"`
		}
		if inspectErr != nil || json.Unmarshal(body, &links) != nil || len(links) != 1 ||
			links[0].Index != created.Index || links[0].Name != name || links[0].Alias != alias || links[0].Info.Kind != "dummy" {
			t.Error("test adapter ownership is uncertain; retaining it")
			return
		}
		addresses, addressErr := current.Addrs()
		if addressErr != nil {
			t.Error("cannot enumerate test adapter before cleanup")
			return
		}
		for _, address := range addresses {
			value, parseErr := netip.ParsePrefix(address.String())
			if parseErr != nil || (value.Addr().Is4() && (value.Bits() != 30 ||
				(value.Addr() != first && value.Addr() != second))) ||
				(value.Addr().Is6() && !value.Addr().IsLinkLocalUnicast()) {
				t.Error("test adapter gained an unowned address; retaining it")
				return
			}
		}
		if _, err := command(cleanupCtx, "link", "delete", "dev", name); err != nil {
			t.Error("remove exact test-owned adapter")
		}
	})
	if _, err := command(ctx, "link", "set", "dev", name, "alias", alias); err != nil {
		t.Fatal("label test-owned adapter")
	}
	for _, address := range []netip.Addr{first, second} {
		if _, err := command(ctx, "address", "add", address.String()+"/30", "dev", name); err != nil {
			t.Fatal("assign test-owned address")
		}
	}
	if _, err := command(ctx, "link", "set", "dev", name, "up"); err != nil {
		t.Fatal("activate test-owned adapter")
	}
	candidates, err := hostnetwork.CurrentCandidates()
	if err != nil {
		t.Fatal("enumerate test-owned host candidates")
	}
	identity := fmt.Sprintf("%d/%s", created.Index, name)
	predecessor, err := hostnetwork.Select(candidates, identity, first.String())
	if err != nil {
		t.Fatal("select predecessor address")
	}
	successor, err := hostnetwork.Select(candidates, identity, second.String())
	if err != nil || predecessor.IPv4 == successor.IPv4 {
		t.Fatal("select distinct successor address")
	}
	return predecessor, successor
}

func liveHostdGatewayRebindNoManagedResources(t *testing.T, fixture *liveHostdGatewayRebindFixture) {
	t.Helper()
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", "label=io.rig.managed"},
		{"network", "ls", "-q", "--filter", "label=io.rig.managed"},
		{"volume", "ls", "-q", "--filter", "label=io.rig.managed"},
		{"image", "ls", "-q", "--filter", "label=io.rig.managed=generated-image"},
	} {
		body, err := controllerJourneyDocker(fixture.ctx, fixture.docker, nil, args...)
		if err != nil || len(bytes.TrimSpace(body)) != 0 {
			t.Error("managed resources remain after full hostd fixture cleanup")
		}
	}
}
