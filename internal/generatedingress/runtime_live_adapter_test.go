package generatedingress

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveRuntimeRoleServer = "server"

const (
	liveRuntimeDiagnosticValidationFailed             = "validation_failed"
	liveRuntimeDiagnosticRuntimeUnavailable           = "runtime_unavailable"
	liveRuntimeDiagnosticRuntimeTimeout               = "runtime_timeout"
	liveRuntimeDiagnosticProcessTerminationFailed     = "process_termination_failed"
	liveRuntimeDiagnosticRuntimeOutputTruncated       = "runtime_output_truncated"
	liveRuntimeDiagnosticImageUnavailable             = "image_unavailable"
	liveRuntimeDiagnosticImageDriftDetected           = "image_drift_detected"
	liveRuntimeDiagnosticNetworkDriftDetected         = "network_drift_detected"
	liveRuntimeDiagnosticNetworkProvisionFailed       = "network_provision_failed"
	liveRuntimeDiagnosticCandidateSlotOccupied        = "candidate_slot_occupied"
	liveRuntimeDiagnosticCandidateCreateFailed        = "candidate_create_failed"
	liveRuntimeDiagnosticCandidateStartFailed         = "candidate_start_failed"
	liveRuntimeDiagnosticCandidateHardeningFailed     = "candidate_hardening_failed"
	liveRuntimeDiagnosticCandidateUnhealthy           = "candidate_unhealthy"
	liveRuntimeDiagnosticCandidateExited              = "candidate_exited"
	liveRuntimeDiagnosticCandidateCleanupFailed       = "candidate_cleanup_failed"
	liveRuntimeDiagnosticInsufficientReplacementSpace = "insufficient_replacement_capacity"
	liveRuntimeDiagnosticConfigurationUnavailable     = "configuration_unavailable"
	liveRuntimeDiagnosticInternalError                = "internal_error"
	liveRuntimeDiagnosticCancelled                    = "cancelled"
)

type LiveRuntimeTestContainerLimits struct {
	MemoryBytes int64
	MilliCPUs   int64
	PIDs        int64
	TmpfsBytes  int64
	LogSize     string
	LogFiles    int
}

type LiveRuntimeTestEngineOptions struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	CommandTimeout        time.Duration
	HealthTimeout         time.Duration
	HealthPollInterval    time.Duration
	OutputLimit           int
	Limits                LiveRuntimeTestContainerLimits
	ReplacementDiskBytes  uint64
}

type LiveRuntimeTestCandidateSpec struct {
	AppID                        string
	ReleaseID                    string
	DeploymentID                 string
	ArtifactID                   string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	ComponentName                string
	Role                         string
	Technology                   string
	RootDirectory                string
	RunCommand                   string
	InternalPort                 uint16
	HealthProbe                  string
	ImageContentID               string
	BuildDefinitionDigest        string
	ActiveSlot                   generatedidentity.Slot
	EnvironmentOperationID       string
	EnvironmentOperationAttempt  int
	Environment                  []byte
	Reservation                  GeneratedReplacementReservation
	Network                      deployments.GeneratedNetworkRequest
}

type LiveRuntimeTestCandidate struct {
	AppID                    string
	ReleaseID                string
	DeploymentID             string
	ArtifactID               string
	DeploymentPlanRevisionID string
	Component                string
	Role                     string
	Technology               string
	Slot                     generatedidentity.Slot
	ContainerID              string
	ContainerName            string
	NetworkName              string
	NetworkAlias             string
	InternalPort             uint16
	ImageContentID           string
	WorkingDirectory         string
	RunCommandDigest         string
}

type LiveRuntimeTestEngine interface {
	StopAndRemove(context.Context, LiveRuntimeTestCandidate, time.Duration) error
	ReserveReplacement(context.Context, GeneratedReplacementRequest) (GeneratedReplacementReservation, error)
}

type LiveRuntimeTestAuthority interface {
	Repository() *deployments.Repository
	PrepareCandidate(*testing.T, context.Context, LiveRuntimeTestCandidateSpec) LiveRuntimeTestCandidateSpec
	BindNetwork(*testing.T, context.Context, LiveRuntimeTestCandidateSpec) LiveRuntimeTestCandidateSpec
	Begin(*testing.T, context.Context, LiveRuntimeTestEngine, LiveRuntimeTestCandidateSpec) LiveRuntimeTestSession
}

type LiveRuntimeTestSession interface {
	BoundSpec() LiveRuntimeTestCandidateSpec
	Start(*testing.T, LiveRuntimeTestCandidateSpec) (LiveRuntimeTestCandidate, error)
	WaitHealthyAndHandoff() error
	RejectObservedFailure() error
	RoutePublished(generatedidentity.Slot, []generatedroute.RouteEndpoint) error
	Close()
}

// LiveRuntimeTestAdapter is compiled only into the generatedingress test
// binary. The external test package supplies the real generatedruntime and
// liveauthority objects without creating a production dependency cycle.
type LiveRuntimeTestAdapter interface {
	NewAuthority(*testing.T, *sql.DB, string) LiveRuntimeTestAuthority
	NewEngine(runtimeprocess.CommandRunner, GeneratedCandidateEnvironmentStager, generatedcapacity.Source, *deployments.Repository, LiveRuntimeTestEngineOptions) (LiveRuntimeTestEngine, error)
	IsCode(error, string) bool
	ErrorCode(error) (string, bool)
	NewError(string) error
	RouteCandidateMayBeLive(error) bool
	RouteGatewayReadinessFailed(error) bool
}

var liveRuntimeTestAdapterRegistry struct {
	sync.Mutex
	value LiveRuntimeTestAdapter
}

// RegisterLiveRuntimeTestAdapter installs the one real runtime adapter used by
// the live ingress tests. Missing, nil and duplicate registration fail closed.
func RegisterLiveRuntimeTestAdapter(adapter LiveRuntimeTestAdapter) {
	if adapter == nil {
		panic("nil live runtime test adapter")
	}
	liveRuntimeTestAdapterRegistry.Lock()
	defer liveRuntimeTestAdapterRegistry.Unlock()
	if liveRuntimeTestAdapterRegistry.value != nil {
		panic("duplicate live runtime test adapter")
	}
	liveRuntimeTestAdapterRegistry.value = adapter
}

func registeredLiveRuntimeTestAdapter() LiveRuntimeTestAdapter {
	liveRuntimeTestAdapterRegistry.Lock()
	defer liveRuntimeTestAdapterRegistry.Unlock()
	if liveRuntimeTestAdapterRegistry.value == nil {
		panic("live runtime test adapter is not registered")
	}
	return liveRuntimeTestAdapterRegistry.value
}

type liveRuntimeCandidate = LiveRuntimeTestCandidate
type liveRuntimeCandidateSpec = LiveRuntimeTestCandidateSpec
type liveRuntimeContainerLimits = LiveRuntimeTestContainerLimits
type liveRuntimeEngineOptions = LiveRuntimeTestEngineOptions
type liveRuntimeEngine = LiveRuntimeTestEngine
type liveRuntimeSession = LiveRuntimeTestSession

func newLiveRuntimeEngine(runner runtimeprocess.CommandRunner, environment GeneratedCandidateEnvironmentStager, capacity generatedcapacity.Source, repository *deployments.Repository, options liveRuntimeEngineOptions) (liveRuntimeEngine, error) {
	return registeredLiveRuntimeTestAdapter().NewEngine(runner, environment, capacity, repository, options)
}

func newLiveRuntimeAuthority(t *testing.T, db *sql.DB, workingDirectory string) LiveRuntimeTestAuthority {
	t.Helper()
	return registeredLiveRuntimeTestAdapter().NewAuthority(t, db, workingDirectory)
}

func liveRuntimeIsCode(err error, code string) bool {
	return registeredLiveRuntimeTestAdapter().IsCode(err, code)
}

func liveRuntimeErrorCode(err error) (string, bool) {
	return registeredLiveRuntimeTestAdapter().ErrorCode(err)
}

func newLiveRuntimeError(code string) error {
	return registeredLiveRuntimeTestAdapter().NewError(code)
}
