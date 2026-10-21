package generatedingress

import (
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
)

// GeneratedCandidateEnvironmentLease is the private staged environment owned
// by one generated candidate invocation.
type GeneratedCandidateEnvironmentLease interface {
	Path() string
	Cleanup() error
}

// GeneratedCandidateEnvironmentStager writes caller-owned protected bytes to
// a private file and must clear the supplied buffer before returning.
type GeneratedCandidateEnvironmentStager interface {
	StageGeneratedCandidate(
		operationID string,
		attempt int,
		contents []byte,
	) (GeneratedCandidateEnvironmentLease, error)
}

// GeneratedCandidateProcessOptions are fixed at repository composition. None
// of these values can be selected by an invocation holder.
type GeneratedCandidateProcessOptions struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	CommandTimeout        time.Duration
	HealthTimeout         time.Duration
	HealthPollInterval    time.Duration
	OutputLimit           int

	MemoryBytes          int64
	MilliCPUs            int64
	PIDs                 int64
	TmpfsBytes           int64
	LogSize              string
	LogFiles             int
	ReplacementDiskBytes uint64
}

type GeneratedReplacementRequest struct {
	Claim                        deploymentexecution.Claim
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	CandidateSlot                string
	Findings                     []Finding
}

type GeneratedCandidateRequest struct {
	Claim                        deploymentexecution.Claim
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	CandidateSlot                string
	Findings                     []Finding
}

// GeneratedCandidateInvocationIdentity is immutable descriptive identity. It
// does not grant command, cleanup, accounting or durable mutation authority.
type GeneratedCandidateInvocationIdentity struct {
	CurrentClaim         deploymentexecution.Claim
	DeploymentID         string
	ComponentName        string
	ReceiptID            string
	CreatorJobID         string
	CreatorWorkerAttempt int
}

// CandidateIdentity contains the non-secret candidate facts needed for route
// endpoint construction. It carries no private authority or release handle.
type CandidateIdentity struct {
	ReceiptID                string
	DeploymentID             string
	ReleaseID                string
	ArtifactID               string
	DeploymentPlanRevisionID string
	ComponentName            string
	Role                     string
	Technology               string
	Slot                     string
	ContainerID              string
	ContainerName            string
	NetworkName              string
	NetworkAlias             string
	InternalPort             uint16
}
