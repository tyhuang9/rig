package deployments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedimagestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const generatedCandidatePolicyVersion = "generated-candidate-v1"

type candidatePolicy struct {
	identity              generatedcontainer.Identity
	limits                generatedcontainer.Limits
	version               string
	digest                string
	runCommand            string
	healthProbe           string
	internalPort          uint16
	buildDefinitionDigest string
}

type candidatePolicyDigestV1 struct {
	Version string `json:"version"`

	ApplicationID                string `json:"applicationId"`
	DeploymentID                 string `json:"deploymentId"`
	ReleaseID                    string `json:"releaseId"`
	ConfigurationRevisionID      string `json:"configurationRevisionId"`
	ConfigurationRevisionNumber  int64  `json:"configurationRevisionNumber"`
	DeploymentPlanRevisionID     string `json:"deploymentPlanRevisionId"`
	DeploymentPlanRevisionNumber int64  `json:"deploymentPlanRevisionNumber"`
	CandidateSlot                string `json:"candidateSlot"`

	ReceiptID                 string `json:"receiptId"`
	CreatorJobID              string `json:"creatorJobId"`
	CreatorWorkerAttempt      int    `json:"creatorWorkerAttempt"`
	ImageArtifactID           string `json:"imageArtifactId"`
	ImageArtifactAttempt      int64  `json:"imageArtifactAttempt"`
	EnvironmentOperationID    string `json:"environmentOperationId"`
	EnvironmentStagingAttempt int    `json:"environmentStagingAttempt"`

	ArtifactCompilerVersion       string `json:"artifactCompilerVersion"`
	ArtifactBuildDefinitionDigest string `json:"artifactBuildDefinitionDigest"`
	ArtifactImageContentID        string `json:"artifactImageContentId"`
	ArtifactState                 string `json:"artifactState"`

	ComponentName                  string `json:"componentName"`
	ComponentRole                  string `json:"componentRole"`
	ComponentRootDirectory         string `json:"componentRootDirectory"`
	ComponentPackageManager        string `json:"componentPackageManager"`
	ComponentInstallBehavior       string `json:"componentInstallBehavior"`
	ComponentInstallDirectory      string `json:"componentInstallDirectory"`
	ComponentNodeVersion           string `json:"componentNodeVersion"`
	ComponentBuildCommand          string `json:"componentBuildCommand"`
	ComponentRunCommand            string `json:"componentRunCommand"`
	ComponentInternalPort          uint16 `json:"componentInternalPort"`
	ComponentHealthProbe           string `json:"componentHealthProbe"`
	ComponentTechnology            string `json:"componentTechnology"`
	ComponentStaticOutputDirectory string `json:"componentStaticOutputDirectory"`

	ContainerName         string `json:"containerName"`
	NetworkName           string `json:"networkName"`
	NetworkAlias          string `json:"networkAlias"`
	WorkingDirectory      string `json:"workingDirectory"`
	RunCommandDigest      string `json:"runCommandDigest"`
	ImageUser             string `json:"imageUser"`
	ImageEntrypoint       string `json:"imageEntrypoint"`
	ImageWorkingDirectory string `json:"imageWorkingDirectory"`

	MemoryBytes          int64  `json:"memoryBytes"`
	MilliCPUs            int64  `json:"milliCpus"`
	PIDs                 int64  `json:"pids"`
	TmpfsBytes           int64  `json:"tmpfsBytes"`
	LogSize              string `json:"logSize"`
	LogFiles             int    `json:"logFiles"`
	ReplacementDiskBytes uint64 `json:"replacementDiskBytes"`

	CommandTimeoutNanos     int64 `json:"commandTimeoutNanos"`
	HealthTimeoutNanos      int64 `json:"healthTimeoutNanos"`
	HealthPollIntervalNanos int64 `json:"healthPollIntervalNanos"`
	OutputLimit             int   `json:"outputLimit"`

	EnvironmentFilePolicy string   `json:"environmentFilePolicy"`
	CreateArguments       []string `json:"createArguments"`
}

func buildCandidatePolicy(
	pins candidateValidationPins,
	component deploymentplans.Component,
	artifact candidateArtifactSnapshot,
	receipt candidateIntentSnapshot,
	options GeneratedCandidateProcessOptions,
) (candidatePolicy, error) {
	if err := validateCandidateRequest(pins); err != nil ||
		!validGeneratedCandidateFixedOptions(options) ||
		!validCandidatePolicyComponent(component) ||
		!validCandidatePolicyArtifact(pins, component, artifact) ||
		!validCandidatePolicyReceipt(pins, artifact, receipt) {
		return candidatePolicy{}, ErrInvalidDeployment
	}

	slot := generatedidentity.Slot(pins.candidateSlot)
	workingDirectory := "/workspace"
	if component.RootDirectory != "." {
		workingDirectory += "/" + component.RootDirectory
	}
	runDigest := candidatePolicySHA256(component.RunCommand)
	policy := candidatePolicy{
		identity: generatedcontainer.Identity{
			AppID: pins.claim.ApplicationID(), ReleaseID: pins.releaseID,
			DeploymentID: pins.deploymentID, ArtifactID: artifact.id,
			DeploymentPlanRevisionID: pins.deploymentPlanRevisionID,
			Component:                component.Name, Role: component.Role,
			Technology: component.Technology, Slot: pins.candidateSlot,
			ReceiptID:        receipt.id,
			ContainerName:    generatedidentity.ContainerName(pins.claim.ApplicationID(), component.Name, slot),
			NetworkName:      generatedidentity.NetworkName(pins.claim.ApplicationID()),
			NetworkAlias:     generatedidentity.ContainerAlias(component.Name, slot),
			ImageContentID:   artifact.imageContentID,
			WorkingDirectory: workingDirectory, RunCommandDigest: runDigest,
		},
		limits: generatedcontainer.Limits{
			MemoryBytes: options.MemoryBytes, MilliCPUs: options.MilliCPUs,
			PIDs: options.PIDs, TmpfsBytes: options.TmpfsBytes,
			LogSize: options.LogSize, LogFiles: options.LogFiles,
		},
		version: generatedCandidatePolicyVersion, runCommand: component.RunCommand,
		healthProbe: component.HealthProbe, internalPort: component.InternalPort,
		buildDefinitionDigest: artifact.buildDefinitionDigest,
	}
	createArguments, err := candidatePolicyCreateArgs(policy, "")
	if err != nil {
		return candidatePolicy{}, ErrInvalidDeployment
	}
	material := candidatePolicyDigestV1{
		Version:       generatedCandidatePolicyVersion,
		ApplicationID: pins.claim.ApplicationID(), DeploymentID: pins.deploymentID,
		ReleaseID:                    pins.releaseID,
		ConfigurationRevisionID:      pins.configurationRevisionID,
		ConfigurationRevisionNumber:  pins.configurationRevisionNumber,
		DeploymentPlanRevisionID:     pins.deploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: pins.deploymentPlanRevisionNumber,
		CandidateSlot:                pins.candidateSlot,
		ReceiptID:                    receipt.id, CreatorJobID: receipt.creatorJobID,
		CreatorWorkerAttempt:          receipt.creatorWorkerAttempt,
		ImageArtifactID:               receipt.imageArtifactID,
		ImageArtifactAttempt:          receipt.imageArtifactAttempt,
		EnvironmentOperationID:        receipt.environmentOperationID,
		EnvironmentStagingAttempt:     receipt.environmentStagingAttempt,
		ArtifactCompilerVersion:       artifact.compilerVersion,
		ArtifactBuildDefinitionDigest: artifact.buildDefinitionDigest,
		ArtifactImageContentID:        artifact.imageContentID,
		ArtifactState:                 string(artifact.state),
		ComponentName:                 component.Name, ComponentRole: component.Role,
		ComponentRootDirectory:         component.RootDirectory,
		ComponentPackageManager:        component.PackageManager,
		ComponentInstallBehavior:       component.InstallBehavior,
		ComponentInstallDirectory:      component.InstallDirectory,
		ComponentNodeVersion:           component.NodeVersion,
		ComponentBuildCommand:          component.BuildCommand,
		ComponentRunCommand:            component.RunCommand,
		ComponentInternalPort:          component.InternalPort,
		ComponentHealthProbe:           component.HealthProbe,
		ComponentTechnology:            component.Technology,
		ComponentStaticOutputDirectory: component.StaticOutputDirectory,
		ContainerName:                  policy.identity.ContainerName,
		NetworkName:                    policy.identity.NetworkName,
		NetworkAlias:                   policy.identity.NetworkAlias,
		WorkingDirectory:               policy.identity.WorkingDirectory,
		RunCommandDigest:               policy.identity.RunCommandDigest,
		ImageUser:                      generatedcontainer.User,
		ImageEntrypoint:                generatedcontainer.Entrypoint,
		ImageWorkingDirectory:          "/workspace",
		MemoryBytes:                    options.MemoryBytes, MilliCPUs: options.MilliCPUs,
		PIDs: options.PIDs, TmpfsBytes: options.TmpfsBytes,
		LogSize: options.LogSize, LogFiles: options.LogFiles,
		ReplacementDiskBytes:    options.ReplacementDiskBytes,
		CommandTimeoutNanos:     int64(options.CommandTimeout),
		HealthTimeoutNanos:      int64(options.HealthTimeout),
		HealthPollIntervalNanos: int64(options.HealthPollInterval),
		OutputLimit:             options.OutputLimit,
		EnvironmentFilePolicy:   "omit-if-empty;clean-absolute-if-present",
		CreateArguments:         append([]string(nil), createArguments...),
	}
	body, err := json.Marshal(material)
	if err != nil {
		return candidatePolicy{}, ErrInvalidDeployment
	}
	policy.digest = candidatePolicySHA256Bytes(body)
	if receipt.policyVersion != "" || receipt.policyDigest != "" {
		if receipt.policyVersion != policy.version || receipt.policyDigest != policy.digest {
			return candidatePolicy{}, deploymentexecution.ErrIdentityLost
		}
	}
	return policy, nil
}

func (p candidatePolicy) createArgs(environmentPath string) ([]string, error) {
	if p.version != generatedCandidatePolicyVersion || !candidatePolicyLowerHex(p.digest, 64) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return candidatePolicyCreateArgs(p, environmentPath)
}

func candidatePolicyCreateArgs(p candidatePolicy, environmentPath string) ([]string, error) {
	if !validCandidatePolicyProjection(p) ||
		(environmentPath != "" && !generatedCandidateCleanAbsolutePath(environmentPath)) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	args := []string{
		"container", "create",
		"--name", p.identity.ContainerName,
		"--hostname", p.identity.ContainerName,
		"--network", p.identity.NetworkName,
		"--network-alias", p.identity.NetworkAlias,
	}
	if environmentPath != "" {
		args = append(args, "--env-file", environmentPath)
	}
	args = append(args,
		"--env", "RIG_RUNTIME_INTERNAL_PORT="+strconv.FormatUint(uint64(p.internalPort), 10),
		"--env", "RIG_RUNTIME_HEALTH_PATH="+p.healthProbe,
		"--user", generatedcontainer.User,
		"--workdir", p.identity.WorkingDirectory,
		"--read-only",
		"--init",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size="+strconv.FormatInt(p.limits.TmpfsBytes, 10),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--memory", strconv.FormatInt(p.limits.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(p.limits.MemoryBytes, 10),
		"--cpus", strconv.FormatFloat(float64(p.limits.MilliCPUs)/1000, 'f', 3, 64),
		"--pids-limit", strconv.FormatInt(p.limits.PIDs, 10),
		"--ulimit", "nofile=1024:1024",
		"--log-driver", "local",
		"--log-opt", "max-size="+p.limits.LogSize,
		"--log-opt", "max-file="+strconv.Itoa(p.limits.LogFiles),
		"--restart", "no",
		"--health-cmd", generatedcontainer.HealthCommand,
		"--health-interval", "2s",
		"--health-timeout", "2s",
		"--health-start-period", "5s",
		"--health-retries", "3",
	)
	if p.identity.Technology == "nextjs" {
		args = append(args, "--tmpfs", p.identity.WorkingDirectory+"/.next/cache:rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size="+strconv.FormatInt(generatedcontainer.NextCacheTmpfsBytes, 10))
	}
	labels := map[string]string{
		"io.rig.managed":                "generated-runtime",
		"io.rig.application":            p.identity.AppID,
		"io.rig.release":                p.identity.ReleaseID,
		"io.rig.deployment":             p.identity.DeploymentID,
		"io.rig.artifact":               p.identity.ArtifactID,
		"io.rig.plan":                   p.identity.DeploymentPlanRevisionID,
		"io.rig.component":              p.identity.Component,
		"io.rig.role":                   p.identity.Role,
		"io.rig.slot":                   p.identity.Slot,
		generatedcontainer.ReceiptLabel: p.identity.ReceiptID,
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, p.identity.ImageContentID, "/bin/sh", "-lc", p.runCommand)
	return args, nil
}

func (a *GeneratedCandidateAuthority) prepareCandidateCommand(args []string) (runtimeprocess.CommandRequest, error) {
	if a == nil || len(args) == 0 || !validGeneratedCandidateFixedOptions(a.options) ||
		a.process == nil || len(a.dockerEnv) == 0 {
		return runtimeprocess.CommandRequest{}, deploymentexecution.ErrInvalidConfiguration
	}
	if err := generatedCandidateValidateCommandDirectories(a.options); err != nil {
		return runtimeprocess.CommandRequest{}, err
	}
	return runtimeprocess.CommandRequest{
		Executable:  a.options.DockerExecutable,
		Args:        append([]string(nil), args...),
		Directory:   a.options.WorkingDirectory,
		Env:         append([]string(nil), a.dockerEnv...),
		Timeout:     a.options.CommandTimeout,
		OutputLimit: a.options.OutputLimit,
	}, nil
}

func validCandidatePolicyComponent(component deploymentplans.Component) bool {
	if !candidatePolicyText(component.Name, 256) ||
		!candidatePolicyRoot(component.RootDirectory) ||
		deploymentplans.ValidateCommand(component.RunCommand) != nil ||
		component.InternalPort == 0 || !candidatePolicyHealthProbe(component.HealthProbe) {
		return false
	}
	return candidatePolicyTechnologyRole(component.Technology, component.Role)
}

func validCandidatePolicyArtifact(pins candidateValidationPins, component deploymentplans.Component, artifact candidateArtifactSnapshot) bool {
	return candidatePolicyUUID(artifact.id) && artifact.releaseID == pins.releaseID &&
		artifact.deploymentPlanRevisionID == pins.deploymentPlanRevisionID &&
		artifact.deploymentPlanRevisionNumber == pins.deploymentPlanRevisionNumber &&
		artifact.componentName == component.Name && candidatePolicyText(artifact.compilerVersion, 128) &&
		candidatePolicyLowerHex(artifact.buildDefinitionDigest, 64) && artifact.attemptNumber > 0 &&
		strings.HasPrefix(artifact.imageContentID, "sha256:") &&
		candidatePolicyLowerHex(strings.TrimPrefix(artifact.imageContentID, "sha256:"), 64) &&
		artifact.state == generatedimagestate.ArtifactReady
}

func validCandidatePolicyReceipt(pins candidateValidationPins, artifact candidateArtifactSnapshot, receipt candidateIntentSnapshot) bool {
	return candidatePolicyUUID(receipt.id) && receipt.creatorJobID == pins.claim.JobID() &&
		receipt.creatorWorkerAttempt > 0 && receipt.configurationRevisionID == pins.configurationRevisionID &&
		receipt.configurationRevisionNumber == pins.configurationRevisionNumber &&
		receipt.imageArtifactID == artifact.id && receipt.imageArtifactAttempt == artifact.attemptNumber &&
		receipt.environmentOperationID == artifact.id &&
		receipt.environmentStagingAttempt == receipt.creatorWorkerAttempt &&
		((receipt.policyVersion == "" && receipt.policyDigest == "") ||
			(receipt.policyVersion != "" && candidatePolicyLowerHex(receipt.policyDigest, 64)))
}

func validCandidatePolicyProjection(p candidatePolicy) bool {
	return candidatePolicyUUID(p.identity.AppID) && validReleaseID(p.identity.ReleaseID) &&
		candidatePolicyUUID(p.identity.DeploymentID) && candidatePolicyUUID(p.identity.ArtifactID) &&
		candidatePolicyUUID(p.identity.DeploymentPlanRevisionID) && candidatePolicyText(p.identity.Component, 256) &&
		candidatePolicyUUID(p.identity.ReceiptID) && p.identity.ContainerID == "" &&
		candidatePolicyTechnologyRole(p.identity.Technology, p.identity.Role) &&
		(p.identity.Slot == string(generatedidentity.SlotBlue) || p.identity.Slot == string(generatedidentity.SlotGreen)) &&
		p.identity.ContainerName == generatedidentity.ContainerName(p.identity.AppID, p.identity.Component, generatedidentity.Slot(p.identity.Slot)) &&
		p.identity.NetworkName == generatedidentity.NetworkName(p.identity.AppID) &&
		p.identity.NetworkAlias == generatedidentity.ContainerAlias(p.identity.Component, generatedidentity.Slot(p.identity.Slot)) &&
		strings.HasPrefix(p.identity.ImageContentID, "sha256:") &&
		candidatePolicyLowerHex(strings.TrimPrefix(p.identity.ImageContentID, "sha256:"), 64) &&
		candidatePolicyLowerHex(p.identity.RunCommandDigest, 64) &&
		p.identity.RunCommandDigest == candidatePolicySHA256(p.runCommand) &&
		candidatePolicyWorkingDirectory(p.identity.WorkingDirectory) && p.internalPort != 0 &&
		candidatePolicyHealthProbe(p.healthProbe) && deploymentplans.ValidateCommand(p.runCommand) == nil &&
		candidatePolicyLowerHex(p.buildDefinitionDigest, 64) &&
		p.limits.MemoryBytes > 0 && p.limits.MilliCPUs > 0 && p.limits.PIDs > 0 &&
		p.limits.TmpfsBytes > 0 && p.limits.LogSize != "" && p.limits.LogFiles > 0
}

func candidatePolicyTechnologyRole(technology, role string) bool {
	switch technology {
	case "":
		return role == "server" || role == "static"
	case "node", "nextjs":
		return role == "server"
	case "static":
		return role == "static"
	default:
		return false
	}
}

func candidatePolicyWorkingDirectory(value string) bool {
	if value == "/workspace" {
		return true
	}
	return strings.HasPrefix(value, "/workspace/") &&
		candidatePolicyRoot(strings.TrimPrefix(value, "/workspace/"))
}

// validGeneratedCandidateFixedOptions validates immutable scalar and lexical
// process policy without reading the filesystem. Command preparation performs
// the separate last-moment directory validation.
func validGeneratedCandidateFixedOptions(options GeneratedCandidateProcessOptions) bool {
	if !generatedCandidateCleanAbsolutePath(options.DockerExecutable) ||
		!generatedCandidateCleanAbsolutePath(options.DockerConfigDirectory) ||
		!generatedCandidateCleanAbsolutePath(options.WorkingDirectory) ||
		!generatedMigrationLocalEndpoint(options.DockerEndpoint) ||
		options.CommandTimeout < time.Second || options.CommandTimeout > 10*time.Minute ||
		options.HealthTimeout < time.Second || options.HealthTimeout > 30*time.Minute ||
		options.HealthPollInterval < 100*time.Millisecond || options.HealthPollInterval > 30*time.Second ||
		options.OutputLimit < 1024 || options.OutputLimit > runtimeprocess.DefaultOutputLimit ||
		options.MemoryBytes < 64<<20 || options.MemoryBytes > 64<<30 ||
		options.MilliCPUs < 100 || options.MilliCPUs > 16000 ||
		options.PIDs < 32 || options.PIDs > 4096 ||
		options.TmpfsBytes < 1<<20 || options.TmpfsBytes > 1<<30 ||
		options.LogFiles < 2 || options.LogFiles > 10 {
		return false
	}
	logMegabytes := uint64(0)
	switch options.LogSize {
	case "1m":
		logMegabytes = 1
	case "5m":
		logMegabytes = 5
	case "10m":
		logMegabytes = 10
	case "20m":
		logMegabytes = 20
	case "50m":
		logMegabytes = 50
	default:
		return false
	}
	minimumDisk := logMegabytes*uint64(options.LogFiles)*(1<<20) + 64<<20
	return options.ReplacementDiskBytes >= minimumDisk && options.ReplacementDiskBytes <= 64<<30
}

func candidatePolicyRoot(value string) bool {
	if value == "." {
		return true
	}
	return candidatePolicyText(value, 1024) && !strings.Contains(value, "\\") &&
		!strings.Contains(value, ":") && !path.IsAbs(value) && path.Clean(value) == value &&
		value != ".." && !strings.HasPrefix(value, "../")
}

func candidatePolicyHealthProbe(value string) bool {
	return candidatePolicyText(value, 1024) && strings.HasPrefix(value, "/") &&
		!strings.Contains(value, "?") && !strings.Contains(value, "#") &&
		path.Clean(value) == value
}

func candidatePolicyText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func candidatePolicyUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func candidatePolicyLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func candidatePolicySHA256(value string) string {
	return candidatePolicySHA256Bytes([]byte(value))
}

func candidatePolicySHA256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
