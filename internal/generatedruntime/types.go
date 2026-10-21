// Package generatedruntime composes generated container execution. Candidate
// lifecycle commands delegate to the repository-owned authority; retained
// lifecycle and the legacy predecessor adapter keep their dedicated boundaries.
package generatedruntime

import (
	"context"
	"errors"
	"sync"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedroute"
)

type Slot = generatedidentity.Slot

const (
	SlotBlue  Slot = generatedidentity.SlotBlue
	SlotGreen Slot = generatedidentity.SlotGreen

	RoleServer = "server"
	RoleStatic = "static"

	// NetworkOwnershipLabelValue is retained as a source-compatible alias for
	// the shared identity contract.
	NetworkOwnershipLabelValue = generatedidentity.NetworkOwnershipLabelValue
)

// InactiveSlot deterministically selects the replacement slot. An empty active
// slot represents an application's first generated deployment.
func InactiveSlot(active Slot) (Slot, error) {
	return generatedidentity.InactiveSlot(active)
}

type DiagnosticCode string

const (
	DiagnosticValidationFailed             DiagnosticCode = "validation_failed"
	DiagnosticRuntimeUnavailable           DiagnosticCode = "runtime_unavailable"
	DiagnosticRuntimeTimeout               DiagnosticCode = "runtime_timeout"
	DiagnosticProcessTerminationFailed     DiagnosticCode = "process_termination_failed"
	DiagnosticRuntimeOutputTruncated       DiagnosticCode = "runtime_output_truncated"
	DiagnosticImageUnavailable             DiagnosticCode = "image_unavailable"
	DiagnosticImageDriftDetected           DiagnosticCode = "image_drift_detected"
	DiagnosticNetworkDriftDetected         DiagnosticCode = "network_drift_detected"
	DiagnosticNetworkProvisionFailed       DiagnosticCode = "network_provision_failed"
	DiagnosticCandidateSlotOccupied        DiagnosticCode = "candidate_slot_occupied"
	DiagnosticCandidateCreateFailed        DiagnosticCode = "candidate_create_failed"
	DiagnosticCandidateStartFailed         DiagnosticCode = "candidate_start_failed"
	DiagnosticCandidateHardeningFailed     DiagnosticCode = "candidate_hardening_failed"
	DiagnosticCandidateUnhealthy           DiagnosticCode = "candidate_unhealthy"
	DiagnosticCandidateExited              DiagnosticCode = "candidate_exited"
	DiagnosticCandidateCleanupFailed       DiagnosticCode = "candidate_cleanup_failed"
	DiagnosticInsufficientReplacementSpace DiagnosticCode = "insufficient_replacement_capacity"
	DiagnosticConfigurationUnavailable     DiagnosticCode = "configuration_unavailable"
	DiagnosticInternalError                DiagnosticCode = "internal_error"
	DiagnosticCancelled                    DiagnosticCode = "cancelled"
)

// Error intentionally carries only a stable, non-secret diagnostic code.
// Docker output and command arguments must never cross this boundary.
type Error struct{ Code DiagnosticCode }

func (e *Error) Error() string { return "generated runtime: " + string(e.Code) }

func IsCode(err error, code DiagnosticCode) bool {
	var target *Error
	if errors.As(err, &target) && target != nil && target.Code == code {
		return true
	}
	var candidate *generatedingress.GeneratedCandidateError
	if errors.As(err, &candidate) && candidate != nil && candidate.Code == string(code) {
		return true
	}
	var legacy *deployments.GeneratedCandidateError
	return errors.As(err, &legacy) && legacy != nil && legacy.Code == string(code)
}

type networkBoundaryError struct {
	diagnostic *Error
	cause      error
}

func (e *networkBoundaryError) Error() string { return e.diagnostic.Error() }

func (e *networkBoundaryError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.diagnostic}
	}
	return []error{e.diagnostic, e.cause}
}

func (e *networkBoundaryError) generatedNetworkBoundaryFailure() {}

func IsGeneratedNetworkBoundaryFailure(err error) bool {
	var failure interface{ generatedNetworkBoundaryFailure() }
	return errors.As(err, &failure)
}

type ContainerLimits struct {
	MemoryBytes int64
	MilliCPUs   int64
	PIDs        int64
	TmpfsBytes  int64
	LogSize     string
	LogFiles    int
}

type CandidateSpec struct {
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
	ActiveSlot                   Slot
	EnvironmentOperationID       string
	EnvironmentOperationAttempt  int
	// Environment describes fixture configuration. Production candidate
	// commands obtain configuration through the protected repository owner.
	Environment []byte
	// Reservation is sealed protected-roster admission. Descriptions carry no
	// authority to invoke or release a candidate command.
	Reservation ReplacementReservation
	// Network carries the original immutable deployment authority for the
	// hidden application-network boundary inside candidate creation.
	Network deployments.GeneratedNetworkRequest
}

type ImageSpec struct {
	AppID                        string
	ReleaseID                    string
	ArtifactID                   string
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	ComponentName                string
	Role                         string
	ImageContentID               string
	BuildDefinitionDigest        string
}

type Candidate struct {
	AppID                    string
	ReleaseID                string
	DeploymentID             string
	ArtifactID               string
	DeploymentPlanRevisionID string
	Component                string
	Role                     string
	Technology               string
	Slot                     Slot
	ContainerID              string
	ContainerName            string
	NetworkName              string
	NetworkAlias             string
	InternalPort             uint16
	ImageContentID           string
	WorkingDirectory         string
	RunCommandDigest         string
}

// ReplacementReservation is the repository's sealed protected-roster handle.
type ReplacementReservation = generatedingress.GeneratedReplacementReservation

// CandidateDescription exposes only deterministic, non-secret resource
// identity. Coordinators persist it before Docker mutation so recovery never
// has to infer intent from an existing container.
type CandidateDescription = generatedidentity.CandidateDescription

// AppNetworkDescription lets migration and ingress implementations join the
// exact app-private network without duplicating the naming algorithm.
type AppNetworkDescription = generatedidentity.AppNetworkDescription

type CapacitySnapshot = generatedcapacity.Snapshot

type CapacitySource = generatedcapacity.Source

type capacityLease struct {
	once    sync.Once
	release func()
}

func (l *capacityLease) Release() {
	if l != nil {
		l.once.Do(l.release)
	}
}

type EnvironmentLease interface {
	Path() string
	Cleanup() error
}

type EnvironmentStager interface {
	// Stage takes ownership of contents and must clear it before returning.
	Stage(operationID string, attempt int, contents []byte) (EnvironmentLease, error)
}

type RouteEndpoint = generatedroute.RouteEndpoint

type RouteSwitchRequest = generatedroute.RouteSwitchRequest

// RouteSwitchFailure describes whether a failed route switch may still be
// using the candidate. Callers must preserve the candidate when this is true
// because removing it could tear down live traffic.
type RouteSwitchFailure interface {
	error
	CandidateMayBeLive() bool
}

func RouteCandidateMayBeLive(err error) bool {
	var failure RouteSwitchFailure
	return errors.As(err, &failure) && failure.CandidateMayBeLive()
}

// RouteReadinessFailure reports that a candidate passed its container-local
// health check but was not reachable from the attested ingress boundary. The
// contract deliberately exposes no command output, endpoint, or configuration
// details to the deployment coordinator.
type RouteReadinessFailure interface {
	error
	GatewayReadinessFailed() bool
}

// RouteGatewayReadinessFailed lets the deployment coordinator persist the
// stable readiness outcome without depending on the ingress implementation.
func RouteGatewayReadinessFailed(err error) bool {
	var failure RouteReadinessFailure
	return errors.As(err, &failure) && failure.GatewayReadinessFailed()
}

// RouteSwitcher is implemented by the ingress milestone. The runtime engine
// stops at a healthy, isolated candidate and never edits Caddy itself.
type RouteSwitcher interface {
	Switch(context.Context, RouteSwitchRequest) error
}

// MigrationRunner accepts only a sealed deployment-owned invocation. It runs
// physical work without writing the durable migration outcome.
type MigrationRunner interface {
	Run(context.Context, deployments.GeneratedMigrationInvocation) error
}
