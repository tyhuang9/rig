//go:build live_docker

package generatedimage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/projectanalysis"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const (
	hostingLiveProject        = "rig-hosting-notes-live-test"
	hostingLiveNetwork        = "rig-hosting-notes-live-test-external"
	hostingLiveSentinelPrefix = "rig-m1-runtime-secret-"
	// Ingress has a controller-wide identity, so this test requires an otherwise
	// empty disposable Docker daemon before it can take ownership of that name.
	hostingLiveCaddyContainer = "rig-generated-caddy-v1"
	hostingLiveCaddyVolume    = "rig-generated-caddy-config-v1"
	hostingLiveCaddyNetwork   = "rig-generated-caddy-ingress-v1"
)

// TestLiveHostingNotesDatabaseRoundtrip is a hosted-only acceptance gate. The
// F7 fixture owns PostgreSQL and HTTPS outside Rig's app network; Rig owns only
// its generated API container, private bridge, and ingress route.
func TestLiveHostingNotesDatabaseRoundtrip(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_HOSTING_NOTES") != "1" {
		t.Fatal("set RIG_RUN_LIVE_HOSTING_NOTES=1 to run the hosted Docker database gate")
	}
	if runtime.GOOS != "linux" || !localDockerEndpoint(os.Getenv("DOCKER_HOST")) {
		t.Fatal("hosted database gate requires a local Linux Docker daemon")
	}
	docker := hostingLiveExecutable(t, "docker")
	node := hostingLiveExecutable(t, "node")
	sh := hostingLiveExecutable(t, "sh")
	openssl := hostingLiveExecutable(t, "openssl")
	workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal("resolve hosting fixture")
	}
	if info, err := os.Stat(filepath.Join(workspace, "api", "node_modules", "pg")); err != nil || !info.IsDir() {
		t.Fatal("hosting fixture API dependencies are missing; run its frozen pnpm install before the live gate")
	}
	root := t.TempDir()
	for _, name := range []string{"runtime-docker", "build-docker", "working", "ingress-state"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal("create hosted test directory")
		}
	}
	runtimeDockerConfig := filepath.Join(root, "runtime-docker")
	buildDockerConfig := filepath.Join(root, "build-docker")
	if plugin := os.Getenv("RIG_LIVE_BUILDX_PLUGIN"); plugin != "" {
		if !filepath.IsAbs(plugin) {
			t.Fatal("task-local Buildx plugin path must be absolute")
		}
		pluginDirectory := filepath.Join(buildDockerConfig, "cli-plugins")
		if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
			t.Fatal("prepare isolated Buildx plugin directory")
		}
		if err := os.Symlink(plugin, filepath.Join(pluginDirectory, "docker-buildx")); err != nil {
			t.Fatal("install task-local Buildx plugin link")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	if !hostingLiveDockerOK(ctx, docker, runtimeDockerConfig, "info") {
		t.Fatal("Docker daemon is unavailable")
	}
	if !hostingLiveDockerOK(ctx, docker, runtimeDockerConfig, "compose", "version") {
		t.Fatal("Docker Compose plugin is unavailable")
	}
	for _, identity := range []struct{ kind, name string }{
		{"container", hostingLiveCaddyContainer}, {"volume", hostingLiveCaddyVolume},
		{"network", hostingLiveCaddyNetwork}, {"network", hostingLiveNetwork},
		{"volume", hostingLiveProject + "_fixture-certs"},
		{"volume", hostingLiveProject + "_fixture-postgres-data"},
	} {
		exists, err := hostingLiveResourceExists(ctx, docker, runtimeDockerConfig, identity.kind, identity.name)
		if err != nil {
			t.Fatal("cannot establish disposable Docker resource preflight")
		}
		if exists {
			t.Fatalf("disposable Docker daemon already contains %s %s", identity.kind, identity.name)
		}
	}
	projectContainers, err := hostingLiveDockerOutput(ctx, docker, runtimeDockerConfig, nil, "ps", "-aq", "--filter", "label=com.docker.compose.project="+hostingLiveProject)
	if err != nil || len(bytes.TrimSpace(projectContainers)) != 0 {
		t.Fatal("disposable Docker daemon already contains the fixture Compose project")
	}

	appID := uuid.NewString()
	dataRoot := filepath.Join(root, "data")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal("open isolated controller database")
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('hosting-live','hosting-live','test',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal("seed test owner")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,'hosting-live','Hosting Live','draft',datetime('now'),datetime('now'))`, appID); err != nil {
		t.Fatal("seed test application")
	}
	plan := hostingLivePlan(t, ctx, workspace, db, dataRoot, appID)
	config, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal("open scoped configuration store")
	}
	stagerRoot, err := securetemp.NewGeneratedRuntime(dataRoot)
	if err != nil {
		t.Fatal("prepare protected runtime environment storage")
	}
	stager, err := generatedruntime.NewSecureEnvironmentStager(stagerRoot)
	if err != nil {
		t.Fatal("prepare runtime environment stager")
	}
	engine, err := generatedruntime.NewEngine(runtimeprocess.ExecRunner{}, stager, hostingLiveCapacity{}, generatedruntime.EngineOptions{
		DockerExecutable: docker, DockerConfigDirectory: runtimeDockerConfig,
		WorkingDirectory: filepath.Join(root, "working"), CommandTimeout: 45 * time.Second,
		HealthTimeout: 35 * time.Second, HealthPollInterval: 250 * time.Millisecond,
		Limits:               generatedruntime.ContainerLimits{MemoryBytes: 256 << 20, MilliCPUs: 500, PIDs: 128, TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 2},
		ReplacementDiskBytes: 256 << 20,
	})
	if err != nil {
		t.Fatal("prepare generated runtime engine")
	}
	appNetwork, err := generatedruntime.DescribeAppNetwork(appID)
	if err != nil {
		t.Fatal("describe app-private network")
	}
	imageTag := "rig-hosting-notes-live:" + uuid.NewString()
	var blue, bad, green generatedruntime.Candidate
	fixtureStarted := false
	composeEnv := []string{}
	fixtureRoot := filepath.Join(root, "fixture")
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		for _, candidate := range []generatedruntime.Candidate{green, bad, blue} {
			if candidate.ContainerID != "" {
				if err := engine.StopAndRemove(cleanupCtx, candidate, 0); err != nil {
					t.Errorf("remove exact hosted candidate: %s", hostingLiveRuntimeCode(err))
				}
			}
		}
		hostingLiveRemoveOwnedIngress(t, cleanupCtx, docker, runtimeDockerConfig)
		if fixtureStarted {
			_, err := hostingLiveDockerOutput(cleanupCtx, docker, runtimeDockerConfig, composeEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", hostingLiveProject, "down", "--volumes", "--remove-orphans")
			if err != nil {
				t.Error("remove exact F7 Compose project")
			}
		}
		for _, removal := range [][]string{{"network", "rm", appNetwork.Name}, {"image", "rm", imageTag}} {
			_, _ = hostingLiveDockerOutput(cleanupCtx, docker, runtimeDockerConfig, nil, removal...)
		}
		for _, identity := range []struct{ kind, name string }{
			{"container", hostingLiveCaddyContainer}, {"volume", hostingLiveCaddyVolume},
			{"network", hostingLiveCaddyNetwork}, {"network", appNetwork.Name},
			{"network", hostingLiveNetwork}, {"image", imageTag},
			{"volume", hostingLiveProject + "_fixture-certs"},
			{"volume", hostingLiveProject + "_fixture-postgres-data"},
		} {
			exists, err := hostingLiveResourceExists(cleanupCtx, docker, runtimeDockerConfig, identity.kind, identity.name)
			if err != nil {
				t.Errorf("inspect exact hosted test %s after cleanup", identity.kind)
			} else if exists {
				t.Errorf("exact hosted test %s remains after cleanup", identity.kind)
			}
		}
		containers, err := hostingLiveDockerOutput(cleanupCtx, docker, runtimeDockerConfig, nil, "ps", "-aq", "--filter", "label=com.docker.compose.project="+hostingLiveProject)
		if err != nil || len(bytes.TrimSpace(containers)) != 0 {
			t.Error("F7 Compose containers remain after cleanup")
		}
	})
	if err := engine.EnsureAppNetwork(ctx, appID); err != nil {
		t.Fatalf("create app-private bridge: %s", hostingLiveRuntimeCode(err))
	}

	gateway := hostingLiveGateway(t, ctx, docker, runtimeDockerConfig, appNetwork.Name)
	postgresPort := hostingLiveFreePort(t, gateway)
	httpsPort := hostingLiveFreePort(t, gateway)
	for httpsPort == postgresPort {
		httpsPort = hostingLiveFreePort(t, gateway)
	}
	hostingLiveCopyHarness(t, workspace, fixtureRoot)
	hostingLiveGenerateCA(t, ctx, sh, openssl, fixtureRoot, gateway)
	ca, err := os.ReadFile(filepath.Join(fixtureRoot, "certs", "test-ca.crt"))
	if err != nil {
		t.Fatal("read fixture CA")
	}
	wrongCA := hostingLiveWrongCA(t, ctx, openssl, fixtureRoot)
	password, token, sentinel := uuid.NewString(), uuid.NewString(), hostingLiveSentinelPrefix+uuid.NewString()
	composeEnv = []string{
		"FIXTURE_NETWORK_NAME=" + hostingLiveNetwork,
		"FIXTURE_HOST_GATEWAY_IP=" + gateway,
		"FIXTURE_POSTGRES_BIND_ADDRESS=" + gateway,
		"FIXTURE_HTTPS_BIND_ADDRESS=" + gateway,
		"FIXTURE_POSTGRES_HOST_PORT=" + strconv.Itoa(postgresPort),
		"FIXTURE_HTTPS_HOST_PORT=" + strconv.Itoa(httpsPort),
		"FIXTURE_POSTGRES_DB=fixture_notes", "FIXTURE_POSTGRES_USER=fixture_user",
		"FIXTURE_POSTGRES_PASSWORD=" + password, "FIXTURE_HTTPS_TOKEN=" + token,
	}
	fixtureStarted = true // Also clean up a partially started Compose project.
	if _, err := hostingLiveDockerOutput(ctx, docker, runtimeDockerConfig, composeEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", hostingLiveProject, "up", "-d"); err != nil {
		t.Fatal("start disposable external TLS dependencies")
	}
	dbURL := fmt.Sprintf("postgresql://fixture_user:%s@%s:%d/fixture_notes?sslmode=verify-full", password, gateway, postgresPort)
	httpsURL := fmt.Sprintf("https://%s:%d/", gateway, httpsPort)
	hostingLivePrepareSchema(t, ctx, node, workspace, dbURL, base64.StdEncoding.EncodeToString(ca), docker, runtimeDockerConfig, fixtureRoot, composeEnv)
	settings := hostingLiveSettings{dbURL: dbURL, httpsURL: httpsURL, ca: base64.StdEncoding.EncodeToString(ca), token: token, sentinel: sentinel}
	revisionA := hostingLiveReplaceConfig(t, ctx, config, appID, plan, 0, settings, "runtime-A")
	releaseID, artifactID := uuid.NewString(), uuid.NewString()
	imageID, definitionDigest := hostingLiveBuildAPI(t, ctx, docker, buildDockerConfig, workspace, root, imageTag, plan, appID, releaseID, artifactID, dbURL, token, sentinel)
	hostingLiveImageHasNoSecrets(t, ctx, docker, runtimeDockerConfig, imageID, dbURL, token, sentinel)
	port := uint16(hostingLiveFreePort(t, "127.0.0.1"))
	ingress, err := generatedingress.New(runtimeprocess.ExecRunner{}, generatedingress.Options{
		DockerExecutable: docker, DockerConfigDirectory: runtimeDockerConfig,
		WorkingDirectory: filepath.Join(root, "working"), DataRoot: filepath.Join(root, "ingress-state"),
		HostPort: port, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal("prepare generated ingress")
	}
	blueSpec := hostingLiveCandidateSpec(appID, plan, imageID, definitionDigest, releaseID, artifactID)
	hostingLiveStart(t, ctx, docker, runtimeDockerConfig, engine, config, revisionA, settings, blueSpec, &blue)
	hostingLiveOnlyAppNetwork(t, ctx, docker, runtimeDockerConfig, blue.ContainerID, appNetwork.Name)
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{AppID: appID, ToSlot: blue.Slot, Endpoints: []generatedruntime.RouteEndpoint{hostingLiveEndpoint(blue)}}); err != nil {
		t.Fatal("route initial API candidate")
	}
	hostingLiveAwaitMarker(t, ctx, port, appID, "runtime-A")
	hostingLiveAssertAPI(t, ctx, port, appID, "runtime-A", true)
	hostingLiveAssertRequest(t, ctx, port, appID, http.MethodGet, "/api/test/dependency", "", http.StatusOK, "\"reachable\"")
	hostingLiveAssertRequest(t, ctx, port, appID, http.MethodPost, "/api/notes", `{"body":"TLS bridge roundtrip"}`, http.StatusCreated, "TLS bridge roundtrip")
	hostingLiveAssertRequest(t, ctx, port, appID, http.MethodGet, "/api/notes", "", http.StatusOK, "TLS bridge roundtrip")

	settings.ca = base64.StdEncoding.EncodeToString(wrongCA)
	badRevision := hostingLiveReplaceConfig(t, ctx, config, appID, plan, revisionA.RevisionNumber, settings, "bad-ca")
	badSpec := hostingLiveCandidateSpec(appID, plan, imageID, definitionDigest, releaseID, artifactID)
	badSpec.ActiveSlot = blue.Slot
	badExport := hostingLiveExport(t, ctx, config, appID, plan, badRevision)
	badSpec.Environment = append([]byte(nil), badExport.Environment...)
	badExport.Clear()
	badSpec.EnvironmentOperationID, badSpec.EnvironmentOperationAttempt = uuid.NewString(), 1
	bad, err = engine.CreateInactiveCandidate(ctx, badSpec)
	if err != nil {
		t.Fatalf("create bad-CA candidate: %s", hostingLiveRuntimeCode(err))
	}
	hostingLiveAssertSelectedDockerEnv(t, ctx, docker, runtimeDockerConfig, bad.ContainerID, settings)
	if err := engine.StartCandidate(ctx, bad); err != nil {
		t.Fatalf("start bad-CA candidate: %s", hostingLiveRuntimeCode(err))
	}
	if err := engine.WaitHealthy(ctx, bad); !generatedruntime.IsCode(err, generatedruntime.DiagnosticCandidateUnhealthy) {
		t.Fatalf("bad-CA candidate did not fail readiness: %s", hostingLiveRuntimeCode(err))
	}
	badPresent, inspectErr := hostingLiveResourceExists(ctx, docker, runtimeDockerConfig, "container", bad.ContainerName)
	if inspectErr != nil || badPresent {
		t.Fatal("unhealthy bad-CA candidate still occupies the inactive slot")
	}
	bad = generatedruntime.Candidate{}
	hostingLiveAssertAPI(t, ctx, port, appID, "runtime-A", false)
	settings.ca = base64.StdEncoding.EncodeToString(ca)
	revisionB := hostingLiveReplaceConfig(t, ctx, config, appID, plan, badRevision.RevisionNumber, settings, "runtime-B")
	greenSpec := hostingLiveCandidateSpec(appID, plan, imageID, definitionDigest, releaseID, artifactID)
	greenSpec.ActiveSlot = blue.Slot
	hostingLiveStart(t, ctx, docker, runtimeDockerConfig, engine, config, revisionB, settings, greenSpec, &green)
	hostingLiveAssertAPI(t, ctx, port, appID, "runtime-A", false)
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{AppID: appID, FromSlot: blue.Slot, ToSlot: green.Slot, Endpoints: []generatedruntime.RouteEndpoint{hostingLiveEndpoint(green)}}); err != nil {
		t.Fatal("switch to configuration-only replacement")
	}
	hostingLiveAwaitMarker(t, ctx, port, appID, "runtime-B")
	hostingLiveAssertAPI(t, ctx, port, appID, "runtime-B", false)
	if err := engine.StopAndRemove(ctx, blue, 0); err != nil {
		t.Fatalf("remove drained API candidate: %s", hostingLiveRuntimeCode(err))
	}
	blue = generatedruntime.Candidate{}
	hostingLiveAssertRequest(t, ctx, port, appID, http.MethodGet, "/api/notes", "", http.StatusOK, "TLS bridge roundtrip")
	t.Logf("M1 acceptance identities: app=%s strategy=generated plan=%s/%d config-initial=%s/%d config-final=%s/%d release=%s artifact=%s image=%s ingress-port=%d deployment=direct-runtime-gate", appID, plan.ID, plan.RevisionNumber, revisionA.RevisionID, revisionA.RevisionNumber, revisionB.RevisionID, revisionB.RevisionNumber, releaseID, artifactID, imageID, port)
	t.Log("M1 hosted Docker gate: app-private bridge, verified TLS PostgreSQL roundtrip, HTTPS probe, bad-CA rollback, same-image scoped revision replacement, and persistent note read passed")
}

type hostingLiveCapacity struct{}

func (hostingLiveCapacity) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 8 << 30}, nil
}

type hostingLiveSettings struct{ dbURL, httpsURL, ca, token, sentinel string }

func hostingLiveExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("hosted database gate requires %s", name)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve %s", name)
	}
	return path
}

func hostingLiveDockerOutput(ctx context.Context, docker, config string, extraEnv []string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, docker, args...)
	command.Env = append(append(os.Environ(), "DOCKER_CONFIG="+config), extraEnv...)
	return command.CombinedOutput()
}

func hostingLiveDockerOK(ctx context.Context, docker, config string, args ...string) bool {
	_, err := hostingLiveDockerOutput(ctx, docker, config, nil, args...)
	return err == nil
}

// A successful list is required to conclude that a fixed Docker identity is
// absent. Inspect failures alone cannot distinguish absence from daemon or
// permission failures, and this test must never adopt an existing resource.
func hostingLiveResourceExists(ctx context.Context, docker, config, kind, name string) (bool, error) {
	var args []string
	switch kind {
	case "container":
		args = []string{"container", "ls", "--all", "--format", "{{.Names}}"}
	case "volume":
		args = []string{"volume", "ls", "--format", "{{.Name}}"}
	case "network":
		args = []string{"network", "ls", "--format", "{{.Name}}"}
	case "image":
		args = []string{"image", "ls", "--format", "{{.Repository}}:{{.Tag}}"}
	default:
		return false, errors.New("unsupported Docker resource kind")
	}
	body, err := hostingLiveDockerOutput(ctx, docker, config, nil, args...)
	if err != nil {
		return false, err
	}
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		if string(bytes.TrimSpace(line)) == name {
			return true, nil
		}
	}
	return false, nil
}

func hostingLiveRemoveOwnedIngress(t *testing.T, ctx context.Context, docker, config string) {
	t.Helper()
	for _, resource := range []struct {
		kind, name, managed string
		remove              []string
	}{
		{"container", hostingLiveCaddyContainer, "generated-ingress", []string{"container", "rm", "--force", hostingLiveCaddyContainer}},
		{"volume", hostingLiveCaddyVolume, "generated-ingress", []string{"volume", "rm", "--force", hostingLiveCaddyVolume}},
		{"network", hostingLiveCaddyNetwork, "generated-ingress-network", []string{"network", "rm", hostingLiveCaddyNetwork}},
	} {
		exists, err := hostingLiveResourceExists(ctx, docker, config, resource.kind, resource.name)
		if err != nil {
			t.Errorf("cannot inspect generated ingress %s before cleanup", resource.kind)
			continue
		}
		if !exists {
			continue
		}
		labelsPath := "{{json .Labels}}"
		if resource.kind == "container" {
			labelsPath = "{{json .Config.Labels}}"
		}
		body, err := hostingLiveDockerOutput(ctx, docker, config, nil, resource.kind, "inspect", "--format", labelsPath, resource.name)
		var labels map[string]string
		if err != nil || json.Unmarshal(bytes.TrimSpace(body), &labels) != nil || labels["io.rig.managed"] != resource.managed || labels["io.rig.identity-version"] != "v1" {
			t.Errorf("generated ingress %s ownership is uncertain; preserving resource", resource.kind)
			continue
		}
		if _, err := hostingLiveDockerOutput(ctx, docker, config, nil, resource.remove...); err != nil {
			t.Errorf("remove exact generated ingress %s", resource.kind)
		}
	}
}

func hostingLiveGateway(t *testing.T, ctx context.Context, docker, config, network string) string {
	t.Helper()
	body, err := hostingLiveDockerOutput(ctx, docker, config, nil, "network", "inspect", "--format", "{{json .IPAM.Config}}", network)
	if err != nil {
		t.Fatal("inspect app-private bridge gateway")
	}
	var addresses []struct {
		Gateway string `json:"Gateway"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &addresses); err != nil || len(addresses) != 1 {
		t.Fatal("app-private bridge has no single gateway")
	}
	ip := net.ParseIP(addresses[0].Gateway)
	if ip == nil || ip.To4() == nil || ip.String() != addresses[0].Gateway {
		t.Fatal("app-private bridge gateway is not canonical IPv4")
	}
	return ip.String()
}

func hostingLiveFreePort(t *testing.T, address string) int {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatal("reserve hosted fixture port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal("release hosted fixture port")
	}
	return port
}

func hostingLiveCopyHarness(t *testing.T, workspace, destination string) {
	t.Helper()
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal("create isolated F7 harness")
	}
	for _, name := range []string{"docker-compose.yml", "generate-test-ca.sh", "openssl-postgres.cnf", "openssl-https.cnf", "https-stub.mjs"} {
		body, err := os.ReadFile(filepath.Join(workspace, "harness", name))
		if err != nil {
			t.Fatal("read reviewed F7 harness")
		}
		if err := os.WriteFile(filepath.Join(destination, name), body, 0o600); err != nil {
			t.Fatal("copy F7 harness into private test directory")
		}
	}
}

func hostingLiveGenerateCA(t *testing.T, ctx context.Context, sh, openssl, fixtureRoot, gateway string) {
	t.Helper()
	command := exec.CommandContext(ctx, sh, filepath.Join(fixtureRoot, "generate-test-ca.sh"))
	command.Dir = fixtureRoot
	command.Env = append(os.Environ(), "FIXTURE_HOST_GATEWAY_IP="+gateway, "PATH="+filepath.Dir(openssl)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := command.Run(); err != nil {
		t.Fatal("generate gateway-IP fixture certificates")
	}
}

func hostingLiveWrongCA(t *testing.T, ctx context.Context, openssl, fixtureRoot string) []byte {
	t.Helper()
	keyPath := filepath.Join(fixtureRoot, "wrong-ca.key")
	certPath := filepath.Join(fixtureRoot, "wrong-ca.crt")
	command := exec.CommandContext(ctx, openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-keyout", keyPath, "-out", certPath, "-subj", "/CN=Wrong disposable fixture CA")
	if err := command.Run(); err != nil {
		t.Fatal("generate wrong fixture CA")
	}
	body, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal("read wrong fixture CA")
	}
	return body
}

func hostingLivePrepareSchema(t *testing.T, ctx context.Context, node, workspace, dbURL, ca, docker, dockerConfig, fixtureRoot string, composeEnv []string) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	lastFailure := "unclassified"
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		command := exec.CommandContext(attemptCtx, node, "src/prepare-schema.js")
		command.Dir = filepath.Join(workspace, "api")
		command.Env = append(os.Environ(), "DATABASE_URL_ENV=NOTES_FIXTURE_DB_URL", "NOTES_FIXTURE_DB_URL="+dbURL, "DATABASE_TLS_CA_PEM_BASE64="+ca)
		output, err := command.CombinedOutput()
		cancel()
		if err == nil {
			return
		}
		lastFailure = hostingLiveSchemaFailureCode(output)
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("application-owned schema preparation failed with verified TLS: category=%s services=%s", lastFailure, hostingLiveComposeStatus(ctx, docker, dockerConfig, fixtureRoot, composeEnv))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func hostingLiveSchemaFailureCode(output []byte) string {
	for _, code := range []string{
		"ECONNREFUSED", "ETIMEDOUT", "ENETUNREACH", "EHOSTUNREACH", "ECONNRESET",
		"ERR_TLS_CERT_ALTNAME_INVALID", "UNABLE_TO_VERIFY_LEAF_SIGNATURE", "SELF_SIGNED_CERT_IN_CHAIN",
		"DEPTH_ZERO_SELF_SIGNED_CERT", "CERT_HAS_EXPIRED", "ERR_SOCKET_CONNECTION_TIMEOUT",
	} {
		if bytes.Contains(output, []byte(code)) {
			return code
		}
	}
	if bytes.Contains(output, []byte("password authentication failed")) {
		return "authentication-rejected"
	}
	return "unclassified"
}

func hostingLiveComposeStatus(ctx context.Context, docker, config, fixtureRoot string, composeEnv []string) string {
	output, err := hostingLiveDockerOutput(ctx, docker, config, composeEnv, "compose", "-f", filepath.Join(fixtureRoot, "docker-compose.yml"), "-p", hostingLiveProject, "ps", "--all", "--format", "json")
	if err != nil {
		return "unavailable"
	}
	var states []string
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
		var item struct {
			Service string `json:"Service"`
			State   string `json:"State"`
			Health  string `json:"Health"`
		}
		if json.Unmarshal(line, &item) != nil || item.Service == "" {
			return "unavailable"
		}
		states = append(states, item.Service+":"+item.State+":"+item.Health)
	}
	if len(states) == 0 {
		return "empty"
	}
	sort.Strings(states)
	return strings.Join(states, ",")
}

func hostingLivePlan(t *testing.T, ctx context.Context, workspace string, db *sql.DB, dataRoot, appID string) deploymentplans.DeploymentPlanRevision {
	t.Helper()
	inspection, err := sourceinspection.InspectLocal(workspace)
	if err != nil {
		t.Fatal("inspect actual hosting fixture source")
	}
	setupBody, err := os.ReadFile(filepath.Join(workspace, "rig-setup.json"))
	if err != nil {
		t.Fatal("read reviewed hosting setup")
	}
	var setup projectanalysis.DeploymentSetup
	if err := json.Unmarshal(setupBody, &setup); err != nil {
		t.Fatal("decode hosting setup")
	}
	plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal("accept reviewed hosting setup")
	}
	store, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal("open deployment plan store")
	}
	revision, err := store.Replace(ctx, appID, "hosting-live", deploymentplans.ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal("persist accepted hosting plan")
	}
	return revision
}

func hostingLiveBuildAPI(t *testing.T, ctx context.Context, docker, dockerConfig, workspace, root, tag string, plan deploymentplans.DeploymentPlanRevision, appID, releaseID, artifactID string, forbidden ...string) (string, string) {
	t.Helper()
	definition, digest, err := definitionForBuild(plan, "api", nil)
	if err != nil {
		t.Fatal("derive generated API recipe")
	}
	operationDirectory := filepath.Join(root, "image-context")
	if err := os.Mkdir(operationDirectory, 0o700); err != nil {
		t.Fatal("create generated API build directory")
	}
	layout, err := prepareBuildContext(ctx, workspace, operationDirectory, definition, contextLimits{})
	if err != nil {
		t.Fatal("stage reviewed API source")
	}
	if err := writeRecipe(layout, definition); err != nil {
		t.Fatal("write generated API recipe")
	}
	hostingLiveAssertNoSecretsInBuildContext(t, layout.contextDirectory, forbidden...)
	// The runtime engine accepts only images with the exact compiler ownership
	// labels. This build follows the production recipe and label contract.
	args := []string{"buildx", "build", "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain", "--tag", tag}
	for _, pair := range []string{
		"io.rig.managed=generated-image", "io.rig.application=" + appID,
		"io.rig.release=" + releaseID, "io.rig.artifact=" + artifactID,
		"io.rig.plan=" + plan.ID, "io.rig.component=api", "io.rig.role=server", "io.rig.definition=" + digest,
	} {
		args = append(args, "--label", pair)
	}
	args = append(args, "--secret", "id=rig-install-command,src="+layout.installCommand, layout.contextDirectory)
	if _, err := hostingLiveDockerOutput(ctx, docker, dockerConfig, nil, args...); err != nil {
		t.Fatal("build generated hosting API image")
	}
	body, err := os.ReadFile(layout.imageIDFile)
	if err != nil {
		t.Fatal("read generated hosting API image identity")
	}
	imageID := strings.TrimSpace(string(body))
	if len(imageID) != 71 || !strings.HasPrefix(imageID, "sha256:") {
		t.Fatal("generated hosting API image identity is invalid")
	}
	return imageID, digest
}

func hostingLiveAssertNoSecretsInBuildContext(t *testing.T, directory string, forbidden ...string) {
	t.Helper()
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		defer clear(body)
		for _, secret := range forbidden {
			if secret != "" && bytes.Contains(body, []byte(secret)) {
				return errors.New("runtime secret entered the generated build context")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect generated API build context without printing secrets: %v", err)
	}
}

func hostingLiveImageHasNoSecrets(t *testing.T, ctx context.Context, docker, config, imageID string, forbidden ...string) {
	t.Helper()
	for _, args := range [][]string{
		{"image", "inspect", "--format", "{{json .Config.Env}}", imageID},
		{"history", "--no-trunc", imageID},
	} {
		body, err := hostingLiveDockerOutput(ctx, docker, config, nil, args...)
		if err != nil {
			t.Fatal("inspect generated API image metadata")
		}
		for _, secret := range forbidden {
			if secret != "" && bytes.Contains(body, []byte(secret)) {
				t.Fatal("scoped runtime secret appeared in generated API image metadata")
			}
		}
	}
	archivePath := filepath.Join(t.TempDir(), "generated-api-image.tar")
	if _, err := hostingLiveDockerOutput(ctx, docker, config, nil, "image", "save", "--output", archivePath, imageID); err != nil {
		t.Fatal("save generated API image for layer inspection")
	}
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatal("open saved generated API image")
	}
	defer archiveFile.Close()
	layerPaths := hostingLiveImageLayerPaths(t, archiveFile)
	if _, err := archiveFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal("rewind saved generated API image")
	}
	layers := 0
	archive := tar.NewReader(archiveFile)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("read saved generated API image archive")
		}
		if _, selected := layerPaths[header.Name]; !selected {
			continue
		}
		layers++
		found, err := hostingLiveInspectLayer(archive, forbidden)
		if err != nil {
			t.Fatal("generated API image layer was not inspectable")
		}
		if found {
			t.Fatal("scoped runtime secret appeared in a generated API image layer")
		}
	}
	if layers != len(layerPaths) {
		t.Fatal("saved generated API image omitted a referenced layer")
	}
}

func hostingLiveImageLayerPaths(t *testing.T, image *os.File) map[string]struct{} {
	t.Helper()
	archive := tar.NewReader(image)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("read generated API image manifest archive")
		}
		if header.Name != "manifest.json" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(archive, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			t.Fatal("read bounded generated API image manifest")
		}
		var manifests []struct{ Layers []string }
		err = json.Unmarshal(body, &manifests)
		clear(body)
		if err != nil || len(manifests) == 0 {
			t.Fatal("decode generated API image manifest")
		}
		paths := make(map[string]struct{})
		for _, manifest := range manifests {
			for _, layer := range manifest.Layers {
				if layer == "" {
					t.Fatal("generated API image manifest has an empty layer path")
				}
				paths[layer] = struct{}{}
			}
		}
		if len(paths) == 0 {
			t.Fatal("generated API image manifest has no layers")
		}
		return paths
	}
	t.Fatal("saved generated API image has no Docker manifest")
	return nil
}

func hostingLiveInspectLayer(layer io.Reader, forbidden []string) (bool, error) {
	input := bufio.NewReader(layer)
	magic, err := input.Peek(2)
	if err != nil {
		return false, err
	}
	var payload io.Reader = input
	if bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		compressed, err := gzip.NewReader(input)
		if err != nil {
			return false, err
		}
		defer compressed.Close()
		payload = compressed
	}
	plain := bufio.NewReader(payload)
	header, err := plain.Peek(512)
	if err != nil || !bytes.Equal(header[257:262], []byte("ustar")) {
		return false, errors.New("image layer is not a tar archive")
	}
	return hostingLiveContainsSecret(plain, forbidden)
}

func hostingLiveContainsSecret(reader io.Reader, forbidden []string) (bool, error) {
	maximum := 0
	for _, secret := range forbidden {
		maximum = max(maximum, len(secret))
	}
	buffer := make([]byte, 64*1024+maximum)
	defer clear(buffer)
	carry := 0
	for {
		count, err := reader.Read(buffer[carry:])
		window := buffer[:carry+count]
		for _, secret := range forbidden {
			if secret != "" && bytes.Contains(window, []byte(secret)) {
				return true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			return false, errors.New("image layer reader made no progress")
		}
		carry = min(len(window), max(0, maximum-1))
		copy(buffer[:carry], window[len(window)-carry:])
	}
}

func TestHostingLiveContainsSecretAcrossReadBoundary(t *testing.T) {
	secret := "synthetic-secret"
	prefix := strings.Repeat("x", 64*1024+len(secret)-2)
	found, err := hostingLiveContainsSecret(strings.NewReader(prefix+secret+"tail"), []string{secret})
	if err != nil || !found {
		t.Fatal("layer scanner missed a secret across a read boundary")
	}
	found, err = hostingLiveContainsSecret(strings.NewReader(prefix+"safe-tail"), []string{secret})
	if err != nil || found {
		t.Fatal("layer scanner misclassified safe layer bytes")
	}
}

func TestHostingLiveInspectManifestReferencedLayer(t *testing.T) {
	secret := "synthetic-layer-secret"
	var layer bytes.Buffer
	inner := tar.NewWriter(&layer)
	if err := inner.WriteHeader(&tar.Header{Name: "fixture.txt", Mode: 0o600, Size: int64(len(secret))}); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.Write([]byte(secret)); err != nil {
		t.Fatal(err)
	}
	if err := inner.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	if _, err := zip.Write(layer.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{layer.Bytes(), compressed.Bytes()} {
		found, err := hostingLiveInspectLayer(bytes.NewReader(data), []string{secret})
		if err != nil || !found {
			t.Fatal("failed to inspect a manifest-referenced tar layer")
		}
	}
	image, err := os.Create(filepath.Join(t.TempDir(), "image.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	outer := tar.NewWriter(image)
	manifest := []byte(`[{"Layers":["blobs/sha256/synthetic"]}]`)
	if err := outer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := outer.WriteHeader(&tar.Header{Name: "blobs/sha256/synthetic", Mode: 0o600, Size: int64(layer.Len())}); err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Write(layer.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := image.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if paths := hostingLiveImageLayerPaths(t, image); len(paths) != 1 {
		t.Fatal("image manifest did not select the OCI-style blob path")
	}
}

func hostingLiveReplaceConfig(t *testing.T, ctx context.Context, store *appconfig.Store, appID string, plan deploymentplans.DeploymentPlanRevision, expected int64, settings hostingLiveSettings, marker string) appconfig.Configuration {
	t.Helper()
	components := make([]appconfig.ComponentTarget, 0, len(plan.Plan.Components))
	for _, component := range plan.Plan.Components {
		components = append(components, appconfig.ComponentTarget{Name: component.Name, Role: component.Role})
	}
	entry := func(phase appconfig.Phase, component, key string, sensitivity appconfig.Sensitivity, value string) appconfig.ScopedValueInput {
		return appconfig.ScopedValueInput{
			ScopedKey:   appconfig.ScopedKey{Phase: phase, Component: component, Key: key},
			Sensitivity: sensitivity, Value: &value,
		}
	}
	entries := []appconfig.ScopedValueInput{
		entry(appconfig.PhaseRuntime, "api", "API_RUNTIME_MARKER", appconfig.SensitivityPublic, marker),
		entry(appconfig.PhaseRuntime, "api", "DATABASE_TLS_CA_PEM_BASE64", appconfig.SensitivitySecret, settings.ca),
		entry(appconfig.PhaseRuntime, "api", "DATABASE_URL_ENV", appconfig.SensitivityPublic, "NOTES_FIXTURE_DB_URL"),
		entry(appconfig.PhaseRuntime, "api", "HTTPS_DEPENDENCY_URL_ENV", appconfig.SensitivityPublic, "NOTES_FIXTURE_HTTPS_URL"),
		entry(appconfig.PhaseRuntime, "api", "NOTES_FIXTURE_HTTPS_URL", appconfig.SensitivityPublic, settings.httpsURL),
		entry(appconfig.PhaseRuntime, "api", "HTTPS_DEPENDENCY_TOKEN_ENV", appconfig.SensitivityPublic, "NOTES_FIXTURE_HTTPS_TOKEN"),
		entry(appconfig.PhaseRuntime, "api", "TEST_FIXTURE_MODE", appconfig.SensitivityPublic, "1"),
		entry(appconfig.PhaseRuntime, "api", "FIXTURE_SCHEMA", appconfig.SensitivityPublic, "rig_fixture_notes"),
		entry(appconfig.PhaseBuild, "frontend", "VITE_BUILD_LABEL", appconfig.SensitivityPublic, "public-build-A"),
	}
	if expected == 0 {
		entries = append(entries,
			entry(appconfig.PhaseRuntime, "api", "NOTES_FIXTURE_DB_URL", appconfig.SensitivitySecret, settings.dbURL),
			entry(appconfig.PhaseRuntime, "api", "NOTES_FIXTURE_HTTPS_TOKEN", appconfig.SensitivitySecret, settings.token),
			entry(appconfig.PhaseRuntime, "api", "HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64", appconfig.SensitivitySecret, settings.ca),
			entry(appconfig.PhaseRuntime, "api", "TEST_SENTINEL_SECRET", appconfig.SensitivitySecret, settings.sentinel),
		)
	}
	revision, err := store.ReplaceScoped(ctx, appID, "hosting-live", appconfig.ScopedReplaceInput{
		ExpectedRevisionNumber: expected, PlanRevisionID: plan.ID, PlanRevisionNumber: plan.RevisionNumber,
		Components: components, Entries: entries,
	})
	if err != nil {
		t.Fatalf("persist immutable scoped configuration revision: %T", err)
	}
	if revision.FormatVersion != 2 || revision.RevisionNumber != expected+1 {
		t.Fatal("scoped revision was not incremented")
	}
	return revision
}

func hostingLiveExport(t *testing.T, ctx context.Context, store *appconfig.Store, appID string, plan deploymentplans.DeploymentPlanRevision, revision appconfig.Configuration) appconfig.ExecutionConfiguration {
	t.Helper()
	export, err := store.ExportComponentRuntimeForExecution(ctx, appID, revision.RevisionID, revision.RevisionNumber, plan.ID, plan.RevisionNumber, "api")
	if err != nil {
		t.Fatal("export exact API-scoped runtime configuration")
	}
	if export.RevisionID != revision.RevisionID || export.RevisionNumber != revision.RevisionNumber || bytes.Contains(export.Environment, []byte("VITE_BUILD_LABEL")) {
		export.Clear()
		t.Fatal("runtime export did not retain exact revision and component scope")
	}
	return export
}

func hostingLiveCandidateSpec(appID string, plan deploymentplans.DeploymentPlanRevision, imageID, definitionDigest, releaseID, artifactID string) generatedruntime.CandidateSpec {
	for _, component := range plan.Plan.Components {
		if component.Name == "api" {
			return generatedruntime.CandidateSpec{
				AppID: appID, ReleaseID: releaseID, DeploymentID: uuid.NewString(), ArtifactID: artifactID,
				DeploymentPlanRevisionID: plan.ID, ComponentName: component.Name, Role: component.Role,
				RootDirectory: component.RootDirectory, RunCommand: component.RunCommand,
				InternalPort: component.InternalPort, HealthProbe: component.HealthProbe,
				ImageContentID: imageID, BuildDefinitionDigest: definitionDigest,
			}
		}
	}
	return generatedruntime.CandidateSpec{}
}

func hostingLiveStart(t *testing.T, ctx context.Context, docker, dockerConfig string, engine *generatedruntime.Engine, store *appconfig.Store, revision appconfig.Configuration, settings hostingLiveSettings, spec generatedruntime.CandidateSpec, candidate *generatedruntime.Candidate) {
	t.Helper()
	// The candidate's plan identity is part of its image contract. Build the
	// export using the same accepted plan identity without a broad revision read.
	export, err := store.ExportComponentRuntimeForExecution(ctx, spec.AppID, revision.RevisionID, revision.RevisionNumber, spec.DeploymentPlanRevisionID, 1, spec.ComponentName)
	if err != nil {
		t.Fatal("export selected API runtime revision")
	}
	spec.Environment = append([]byte(nil), export.Environment...)
	export.Clear()
	spec.EnvironmentOperationID, spec.EnvironmentOperationAttempt = uuid.NewString(), 1
	created, err := engine.CreateInactiveCandidate(ctx, spec)
	if err != nil {
		t.Fatalf("create exact API candidate: %s", hostingLiveRuntimeCode(err))
	}
	*candidate = created
	hostingLiveAssertSelectedDockerEnv(t, ctx, docker, dockerConfig, created.ContainerID, settings)
	if err := engine.StartCandidate(ctx, created); err != nil {
		t.Fatalf("start exact API candidate: %s", hostingLiveRuntimeCode(err))
	}
	if err := engine.WaitHealthy(ctx, created); err != nil {
		t.Fatalf("API candidate did not pass database readiness: %s", hostingLiveRuntimeCode(err))
	}
}

func hostingLiveAssertSelectedDockerEnv(t *testing.T, ctx context.Context, docker, dockerConfig, containerID string, settings hostingLiveSettings) {
	t.Helper()
	body, err := hostingLiveDockerOutput(ctx, docker, dockerConfig, nil, "container", "inspect", "--format", "{{json .Config.Env}}", containerID)
	if err != nil {
		t.Fatal("inspect generated API environment transport")
	}
	var entries []string
	if err := json.Unmarshal(bytes.TrimSpace(body), &entries); err != nil {
		t.Fatal("decode generated API environment transport")
	}
	for _, expected := range []string{
		"DATABASE_URL_ENV=NOTES_FIXTURE_DB_URL", "FIXTURE_SCHEMA=rig_fixture_notes",
		"NOTES_FIXTURE_DB_URL=" + settings.dbURL,
		"NOTES_FIXTURE_HTTPS_TOKEN=" + settings.token,
		"TEST_SENTINEL_SECRET=" + settings.sentinel,
	} {
		if !slices.Contains(entries, expected) {
			t.Fatal("Docker did not receive an exact scoped runtime selector")
		}
	}
}

func hostingLiveRuntimeCode(err error) string {
	var diagnostic *generatedruntime.Error
	if errors.As(err, &diagnostic) && diagnostic != nil {
		return string(diagnostic.Code)
	}
	return "unclassified"
}

func hostingLiveEndpoint(candidate generatedruntime.Candidate) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{
		Component: candidate.Component, Role: candidate.Role, ContainerID: candidate.ContainerID,
		NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort,
	}
}

func hostingLiveOnlyAppNetwork(t *testing.T, ctx context.Context, docker, config, containerID, appNetwork string) {
	t.Helper()
	body, err := hostingLiveDockerOutput(ctx, docker, config, nil, "container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", containerID)
	if err != nil {
		t.Fatal("inspect generated API network attachments")
	}
	var networks map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(body), &networks); err != nil || len(networks) != 1 || networks[appNetwork] == nil {
		t.Fatal("generated API escaped its app-private bridge")
	}
}

func hostingLiveRequest(t *testing.T, ctx context.Context, port uint16, appID, method, path, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(body))
	if err != nil {
		t.Fatal("construct hosted API request")
	}
	request.Host = appID + ".rig.localhost"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("hosted API route is unavailable")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	if err != nil {
		t.Fatal("read hosted API response")
	}
	return response.StatusCode, responseBody
}

func hostingLiveAssertRequest(t *testing.T, ctx context.Context, port uint16, appID, method, path, body string, wantStatus int, wantFragment string) {
	t.Helper()
	status, response := hostingLiveRequest(t, ctx, port, appID, method, path, body)
	if status != wantStatus || !bytes.Contains(response, []byte(wantFragment)) {
		t.Fatalf("hosted API %s %s: status=%d, expected_status=%d, expected_fragment_present=%t", method, path, status, wantStatus, bytes.Contains(response, []byte(wantFragment)))
	}
}

func hostingLiveAssertAPI(t *testing.T, ctx context.Context, port uint16, appID, marker string, checkPresence bool) {
	t.Helper()
	status, body := hostingLiveRequest(t, ctx, port, appID, http.MethodGet, "/api/version", "")
	var version struct {
		RuntimeMarker string `json:"runtimeMarker"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &version) != nil || version.RuntimeMarker != marker {
		t.Fatal("routed API did not serve the selected configuration revision")
	}
	if !checkPresence {
		return
	}
	status, body = hostingLiveRequest(t, ctx, port, appID, http.MethodGet, "/api/test/runtime-presence", "")
	var presence struct {
		DatabaseURLConfigured   bool `json:"databaseUrlConfigured"`
		RuntimeMarkerConfigured bool `json:"runtimeMarkerConfigured"`
		SentinelConfigured      bool `json:"sentinelConfigured"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &presence) != nil || !presence.DatabaseURLConfigured || !presence.RuntimeMarkerConfigured || !presence.SentinelConfigured {
		t.Fatal("scoped server runtime values were unavailable to API")
	}
}

func hostingLiveAwaitMarker(t *testing.T, ctx context.Context, port uint16, appID, marker string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/version", port), nil)
		if err != nil {
			cancel()
			t.Fatal("construct hosted readiness request")
		}
		request.Host = appID + ".rig.localhost"
		response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
		matched := false
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<10))
			response.Body.Close()
			var version struct {
				RuntimeMarker string `json:"runtimeMarker"`
			}
			matched = readErr == nil && response.StatusCode == http.StatusOK && json.Unmarshal(body, &version) == nil && version.RuntimeMarker == marker
		}
		cancel()
		if matched {
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatal("generated ingress did not serve the selected API revision before deadline")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
