package generatedingress

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedimagestate"
)

type candidatePolicyFixture struct {
	network   *generatedMigrationFixture
	pins      candidateValidationPins
	component deploymentplans.Component
	artifact  candidateArtifactSnapshot
	receipt   candidateIntentSnapshot
	options   GeneratedCandidateProcessOptions
}

func newCandidatePolicyFixture(t *testing.T) candidatePolicyFixture {
	t.Helper()
	network := newGeneratedMigrationFixture(t)
	artifactID := uuid.NewString()
	pins := candidateValidationPins{
		claim: network.claim, identity: candidateValidationIdentity{applicationID: network.claim.ApplicationID(),
			jobID: network.claim.JobID(), attempt: network.claim.Attempt()}, deploymentID: network.request.DeploymentID,
		releaseID:                    network.request.ReleaseID,
		configurationRevisionID:      network.request.ConfigurationRevisionID,
		configurationRevisionNumber:  network.request.ConfigurationRevisionNumber,
		deploymentPlanRevisionID:     network.request.DeploymentPlanRevisionID,
		deploymentPlanRevisionNumber: network.request.DeploymentPlanRevisionNumber,
		candidateSlot:                "green",
	}
	component := deploymentplans.Component{
		Name: "api", Role: "server", RootDirectory: "apps/api",
		PackageManager: "npm", InstallBehavior: "npm ci",
		InstallDirectory: "apps/api", NodeVersion: "24",
		BuildCommand: "npm run build", RunCommand: "node server.js",
		InternalPort: 3000, HealthProbe: "/health", Technology: "node",
	}
	artifact := candidateArtifactSnapshot{
		id: artifactID, releaseID: pins.releaseID,
		deploymentPlanRevisionID:     pins.deploymentPlanRevisionID,
		deploymentPlanRevisionNumber: pins.deploymentPlanRevisionNumber,
		componentName:                component.Name, compilerVersion: "generated-node-v6",
		buildDefinitionDigest: strings.Repeat("b", 64), attemptNumber: 3,
		imageContentID: "sha256:" + strings.Repeat("c", 64),
		state:          generatedimagestate.ArtifactReady,
	}
	receipt := candidateIntentSnapshot{
		id: uuid.NewString(), creatorJobID: pins.claim.JobID(), creatorWorkerAttempt: 1,
		configurationRevisionID:     pins.configurationRevisionID,
		configurationRevisionNumber: pins.configurationRevisionNumber,
		imageArtifactID:             artifact.id, imageArtifactAttempt: artifact.attemptNumber,
		environmentOperationID: artifact.id, environmentStagingAttempt: 1,
	}
	return candidatePolicyFixture{
		network: network, pins: pins, component: component, artifact: artifact, receipt: receipt,
		options: GeneratedCandidateProcessOptions{
			DockerExecutable:      filepath.Join(t.TempDir(), "docker"),
			DockerConfigDirectory: t.TempDir(), WorkingDirectory: t.TempDir(),
			CommandTimeout: 30 * time.Second, HealthTimeout: 2 * time.Minute,
			HealthPollInterval: time.Second, OutputLimit: 64 << 10,
			MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256,
			TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3,
			ReplacementDiskBytes: 256 << 20,
		},
	}
}

func (f candidatePolicyFixture) build(t *testing.T) candidatePolicy {
	t.Helper()
	policy, err := buildCandidatePolicy(f.pins, f.component, f.artifact, f.receipt, f.options)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestCandidatePolicyUsesOriginalReceiptAttemptAcrossResume(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	original := fixture.build(t)
	resumedClaim, err := deploymentexecution.NewClaim(
		fixture.network.binding,
		fixture.pins.claim.JobID(),
		fixture.pins.claim.ApplicationID(),
		fixture.pins.claim.Attempt()+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	resumed := fixture
	resumed.pins.claim = resumedClaim
	resumedPolicy := resumed.build(t)
	if resumedPolicy.digest != original.digest {
		t.Fatalf("current claim attempt changed policy digest: %q != %q", resumedPolicy.digest, original.digest)
	}

	changedCreator := resumed
	changedCreator.receipt.creatorWorkerAttempt++
	changedCreator.receipt.environmentStagingAttempt++
	if changedCreator.build(t).digest == original.digest {
		t.Fatal("original receipt creator attempt was omitted from policy digest")
	}

	persisted := fixture
	persisted.receipt.policyVersion = original.version
	persisted.receipt.policyDigest = original.digest
	if replayed := persisted.build(t); replayed.digest != original.digest {
		t.Fatalf("exact persisted policy replay digest = %q", replayed.digest)
	}
	persisted.receipt.policyDigest = strings.Repeat("d", 64)
	if _, err := buildCandidatePolicy(persisted.pins, persisted.component, persisted.artifact, persisted.receipt, persisted.options); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("mismatched persisted policy error = %v", err)
	}

	if original.version != generatedCandidatePolicyVersion || !candidatePolicyLowerHex(original.digest, 64) ||
		original.identity.ReceiptID != fixture.receipt.id || original.identity.ContainerID != "" ||
		original.identity.WorkingDirectory != "/workspace/apps/api" ||
		original.identity.RunCommandDigest != candidatePolicySHA256(fixture.component.RunCommand) ||
		original.buildDefinitionDigest != fixture.artifact.buildDefinitionDigest {
		t.Fatalf("policy projection = %#v", original)
	}
}

func TestCandidatePolicyCreateArgumentsMatchHardenedRuntimeAndOptionalEnvironment(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	policy := fixture.build(t)
	withoutEnvironment, err := policy.createArgs("")
	if err != nil {
		t.Fatal(err)
	}
	if candidatePolicyArgumentValue(withoutEnvironment, "--env-file") != "" {
		t.Fatalf("empty configuration emitted environment file: %#v", withoutEnvironment)
	}
	environmentPath := filepath.Join(t.TempDir(), "candidate.env")
	withEnvironment, err := policy.createArgs(environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if candidatePolicyArgumentValue(withEnvironment, "--env-file") != environmentPath {
		t.Fatalf("environment path missing from %#v", withEnvironment)
	}
	if _, err := policy.createArgs(filepath.Join("relative", "candidate.env")); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("relative environment path error = %v", err)
	}

	wantWithoutEnvironment := candidatePolicyExpectedCreateArguments(policy)
	if !reflect.DeepEqual(withoutEnvironment, wantWithoutEnvironment) {
		t.Fatalf("create arguments mismatch\n got: %#v\nwant: %#v", withoutEnvironment, wantWithoutEnvironment)
	}
	labels := candidatePolicyLabels(withoutEnvironment)
	if labels[generatedcontainer.ReceiptLabel] != fixture.receipt.id ||
		labels["io.rig.definition"] != policy.buildDefinitionDigest || len(labels) != 11 {
		t.Fatalf("candidate labels = %#v", labels)
	}
	if candidatePolicyCountArgument(withoutEnvironment, "--label", generatedcontainer.ReceiptLabel+"="+fixture.receipt.id) != 1 {
		t.Fatalf("receipt label multiplicity in %#v", withoutEnvironment)
	}
	if candidatePolicyCountArgument(withoutEnvironment, "--label", "io.rig.definition="+policy.buildDefinitionDigest) != 1 {
		t.Fatal("definition label multiplicity")
	}

	first, _ := policy.createArgs("")
	second, _ := policy.createArgs("")
	first[0] = "mutated"
	if second[0] != "container" {
		t.Fatal("create argument results alias each other")
	}
}

func TestCandidatePolicyDigestBindsStableInputsAndExcludesProcessLocalPaths(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	baseline := fixture.build(t).digest

	local := fixture
	local.options.DockerExecutable = filepath.Join(t.TempDir(), "different-docker")
	local.options.DockerConfigDirectory = filepath.Join(t.TempDir(), "missing-config")
	local.options.WorkingDirectory = filepath.Join(t.TempDir(), "missing-working")
	local.options.DockerEndpoint = "npipe:////./pipe/docker_engine"
	if local.build(t).digest != baseline {
		t.Fatal("process-local path or endpoint changed policy digest")
	}

	mutations := []struct {
		name   string
		mutate func(*candidatePolicyFixture)
	}{
		{name: "configuration", mutate: func(value *candidatePolicyFixture) {
			value.pins.configurationRevisionNumber++
			value.receipt.configurationRevisionNumber++
		}},
		{name: "artifact attempt", mutate: func(value *candidatePolicyFixture) {
			value.artifact.attemptNumber++
			value.receipt.imageArtifactAttempt++
		}},
		{name: "compiler", mutate: func(value *candidatePolicyFixture) { value.artifact.compilerVersion = "generated-node-v7" }},
		{name: "build definition", mutate: func(value *candidatePolicyFixture) {
			value.artifact.buildDefinitionDigest = strings.Repeat("d", 64)
		}},
		{name: "component build command", mutate: func(value *candidatePolicyFixture) { value.component.BuildCommand = "npm run build:prod" }},
		{name: "run command", mutate: func(value *candidatePolicyFixture) { value.component.RunCommand = "node production.js" }},
		{name: "health", mutate: func(value *candidatePolicyFixture) { value.component.HealthProbe = "/ready" }},
		{name: "resource", mutate: func(value *candidatePolicyFixture) { value.options.MemoryBytes += 64 << 20 }},
		{name: "command timeout", mutate: func(value *candidatePolicyFixture) { value.options.CommandTimeout++ }},
		{name: "health polling", mutate: func(value *candidatePolicyFixture) { value.options.HealthPollInterval++ }},
		{name: "output limit", mutate: func(value *candidatePolicyFixture) { value.options.OutputLimit++ }},
		{name: "replacement disk", mutate: func(value *candidatePolicyFixture) { value.options.ReplacementDiskBytes++ }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := fixture
			test.mutate(&changed)
			if changed.build(t).digest == baseline {
				t.Fatal("stable policy input was omitted from digest")
			}
		})
	}
}

func TestCandidatePolicyRejectsBrokenReceiptAndArtifactLinks(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	tests := []struct {
		name   string
		mutate func(*candidatePolicyFixture)
	}{
		{name: "artifact not ready", mutate: func(value *candidatePolicyFixture) { value.artifact.state = generatedimagestate.ArtifactBuilding }},
		{name: "artifact release", mutate: func(value *candidatePolicyFixture) { value.artifact.releaseID = uuid.NewString() }},
		{name: "artifact plan", mutate: func(value *candidatePolicyFixture) { value.artifact.deploymentPlanRevisionID = uuid.NewString() }},
		{name: "artifact component", mutate: func(value *candidatePolicyFixture) { value.artifact.componentName = "worker" }},
		{name: "receipt creator job", mutate: func(value *candidatePolicyFixture) { value.receipt.creatorJobID = uuid.NewString() }},
		{name: "receipt configuration", mutate: func(value *candidatePolicyFixture) { value.receipt.configurationRevisionID = uuid.NewString() }},
		{name: "receipt artifact", mutate: func(value *candidatePolicyFixture) { value.receipt.imageArtifactID = uuid.NewString() }},
		{name: "receipt artifact attempt", mutate: func(value *candidatePolicyFixture) { value.receipt.imageArtifactAttempt++ }},
		{name: "environment operation", mutate: func(value *candidatePolicyFixture) { value.receipt.environmentOperationID = uuid.NewString() }},
		{name: "environment staging attempt", mutate: func(value *candidatePolicyFixture) { value.receipt.environmentStagingAttempt++ }},
		{name: "partial persisted policy", mutate: func(value *candidatePolicyFixture) { value.receipt.policyVersion = generatedCandidatePolicyVersion }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := fixture
			test.mutate(&changed)
			if _, err := buildCandidatePolicy(changed.pins, changed.component, changed.artifact, changed.receipt, changed.options); err == nil {
				t.Fatal("broken provenance was accepted")
			}
		})
	}
}

func TestCandidatePolicyRejectsInvalidRuntimeComponentBoundaries(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	tests := []struct {
		name   string
		mutate func(*deploymentplans.Component)
	}{
		{name: "root traversal", mutate: func(value *deploymentplans.Component) { value.RootDirectory = "../api" }},
		{name: "root backslash", mutate: func(value *deploymentplans.Component) { value.RootDirectory = `apps\api` }},
		{name: "run newline", mutate: func(value *deploymentplans.Component) { value.RunCommand = "node server.js\nrm" }},
		{name: "empty port", mutate: func(value *deploymentplans.Component) { value.InternalPort = 0 }},
		{name: "health query", mutate: func(value *deploymentplans.Component) { value.HealthProbe = "/health?secret=value" }},
		{name: "technology", mutate: func(value *deploymentplans.Component) { value.Technology = "unknown" }},
		{name: "role", mutate: func(value *deploymentplans.Component) { value.Role = "worker" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := fixture
			test.mutate(&changed.component)
			if _, err := buildCandidatePolicy(changed.pins, changed.component, changed.artifact, changed.receipt, changed.options); err == nil {
				t.Fatal("invalid runtime component was accepted")
			}
		})
	}
}

func TestCandidatePolicyNextJSAddsOnlyFixedCacheTmpfs(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	fixture.component.Technology = "nextjs"
	policy := fixture.build(t)
	args, err := policy.createArgs("")
	if err != nil {
		t.Fatal(err)
	}
	want := policy.identity.WorkingDirectory + "/.next/cache:rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=" + strconv.FormatInt(generatedcontainer.NextCacheTmpfsBytes, 10)
	if candidatePolicyCountArgument(args, "--tmpfs", want) != 1 || candidatePolicyArgumentCount(args, "--tmpfs") != 2 {
		t.Fatalf("nextjs tmpfs arguments = %#v", args)
	}
}

func TestCandidateCommandPreparationRechecksDirectoriesAndCopiesInputs(t *testing.T) {
	fixture := newCandidatePolicyFixture(t)
	process := &candidateFactoryProcess{}
	authority := &GeneratedCandidateAuthority{
		process: process, options: fixture.options,
		dockerEnv: []string{"DOCKER_CONFIG=" + fixture.options.DockerConfigDirectory, "PATH=fixed"},
	}
	args := []string{"container", "inspect", "candidate"}
	request, err := authority.prepareCandidateCommand(args)
	if err != nil {
		t.Fatal(err)
	}
	if request.Executable != fixture.options.DockerExecutable || request.Directory != fixture.options.WorkingDirectory ||
		request.Timeout != fixture.options.CommandTimeout || request.OutputLimit != fixture.options.OutputLimit ||
		process.runs != 0 {
		t.Fatalf("prepared request = %#v process runs=%d", request, process.runs)
	}
	args[0] = "mutated"
	authority.dockerEnv[0] = "mutated"
	if request.Args[0] != "container" || request.Env[0] != "DOCKER_CONFIG="+fixture.options.DockerConfigDirectory {
		t.Fatal("prepared command aliases authority or caller slices")
	}

	if err := os.Remove(fixture.options.DockerConfigDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.options.DockerConfigDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.prepareCandidateCommand([]string{"container", "inspect", "candidate"}); err == nil {
		t.Fatal("changed Docker configuration directory was accepted")
	}
}

func candidatePolicyExpectedCreateArguments(policy candidatePolicy) []string {
	return []string{
		"container", "create",
		"--name", policy.identity.ContainerName,
		"--hostname", policy.identity.ContainerName,
		"--network", policy.identity.NetworkName,
		"--network-alias", policy.identity.NetworkAlias,
		"--env", "RIG_RUNTIME_INTERNAL_PORT=3000",
		"--env", "RIG_RUNTIME_HEALTH_PATH=/health",
		"--user", generatedcontainer.User,
		"--workdir", "/workspace/apps/api",
		"--read-only", "--init",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=67108864",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--memory", "536870912", "--memory-swap", "536870912",
		"--cpus", "1.000", "--pids-limit", "256",
		"--ulimit", "nofile=1024:1024",
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--restart", "no",
		"--health-cmd", generatedcontainer.HealthCommand,
		"--health-interval", "2s", "--health-timeout", "2s",
		"--health-start-period", "5s", "--health-retries", "3",
		"--label", "io.rig.application=" + policy.identity.AppID,
		"--label", "io.rig.artifact=" + policy.identity.ArtifactID,
		"--label", generatedcontainer.ReceiptLabel + "=" + policy.identity.ReceiptID,
		"--label", "io.rig.component=" + policy.identity.Component,
		"--label", "io.rig.definition=" + policy.buildDefinitionDigest,
		"--label", "io.rig.deployment=" + policy.identity.DeploymentID,
		"--label", "io.rig.managed=generated-runtime",
		"--label", "io.rig.plan=" + policy.identity.DeploymentPlanRevisionID,
		"--label", "io.rig.release=" + policy.identity.ReleaseID,
		"--label", "io.rig.role=" + policy.identity.Role,
		"--label", "io.rig.slot=" + policy.identity.Slot,
		policy.identity.ImageContentID, "/bin/sh", "-lc", policy.runCommand,
	}
}

func candidatePolicyArgumentValue(args []string, key string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key {
			return args[index+1]
		}
	}
	return ""
}

func candidatePolicyArgumentCount(args []string, key string) int {
	count := 0
	for _, value := range args {
		if value == key {
			count++
		}
	}
	return count
}

func candidatePolicyCountArgument(args []string, key, value string) int {
	count := 0
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			count++
		}
	}
	return count
}

func candidatePolicyLabels(args []string) map[string]string {
	labels := make(map[string]string)
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--label" {
			continue
		}
		parts := strings.SplitN(args[index+1], "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return labels
}
