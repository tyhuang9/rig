// Package generatedruntime owns the Docker execution boundary for images
// produced by the generated-image compiler. It deliberately contains no job
// orchestration or durable deployment state.
package generatedruntime

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Slot string

const (
	SlotBlue  Slot = "blue"
	SlotGreen Slot = "green"
)

// InactiveSlot deterministically selects the replacement slot. An empty active
// slot represents an application's first generated deployment.
func InactiveSlot(active Slot) (Slot, error) {
	switch active {
	case "", SlotGreen:
		return SlotBlue, nil
	case SlotBlue:
		return SlotGreen, nil
	default:
		return "", errors.New("invalid generated runtime slot")
	}
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
	return errors.As(err, &target) && target.Code == code
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
	AppID                       string
	ReleaseID                   string
	DeploymentID                string
	ArtifactID                  string
	DeploymentPlanRevisionID    string
	ComponentName               string
	RootDirectory               string
	RunCommand                  string
	InternalPort                uint16
	HealthProbe                 string
	ImageContentID              string
	BuildDefinitionDigest       string
	ActiveSlot                  Slot
	EnvironmentOperationID      string
	EnvironmentOperationAttempt int
	// Environment is protected configuration exported for this exact release.
	// CreateInactiveCandidate takes ownership and clears it before returning.
	Environment []byte
}

type ImageSpec struct {
	AppID                    string
	ReleaseID                string
	ArtifactID               string
	DeploymentPlanRevisionID string
	ImageContentID           string
	BuildDefinitionDigest    string
}

type Candidate struct {
	AppID                    string
	ReleaseID                string
	DeploymentID             string
	ArtifactID               string
	DeploymentPlanRevisionID string
	Component                string
	Slot                     Slot
	ContainerID              string
	ContainerName            string
	NetworkName              string
	NetworkAlias             string
	InternalPort             uint16
	ImageContentID           string
	WorkingDirectory         string
	RunCommandDigest         string

	lease *capacityLease
}

// CandidateDescription exposes only deterministic, non-secret resource
// identity. Coordinators persist it before Docker mutation so recovery never
// has to infer intent from an existing container.
type CandidateDescription struct {
	Slot          Slot
	ContainerName string
	NetworkName   string
	NetworkAlias  string
}

// AppNetworkDescription lets migration and ingress implementations join the
// exact app-private network without duplicating the naming algorithm.
type AppNetworkDescription struct{ Name string }

type CapacitySnapshot struct {
	MemoryAvailableBytes uint64
	DiskAvailableBytes   uint64
}

type CapacitySource interface {
	Snapshot(context.Context) (CapacitySnapshot, error)
}

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

type RouteEndpoint struct {
	Component    string
	Role         string
	ContainerID  string
	NetworkName  string
	NetworkAlias string
	InternalPort uint16
}

type RouteSwitchRequest struct {
	AppID       string
	FromSlot    Slot
	ToSlot      Slot
	Endpoints   []RouteEndpoint
	DrainPeriod time.Duration
}

// RouteSwitcher is implemented by the ingress milestone. The runtime engine
// stops at a healthy, isolated candidate and never edits Caddy itself.
type RouteSwitcher interface {
	Switch(context.Context, RouteSwitchRequest) error
}

type MigrationRequest struct {
	AppID                       string
	ReleaseID                   string
	DeploymentID                string
	ArtifactID                  string
	DeploymentPlanRevisionID    string
	ComponentName               string
	RootDirectory               string
	ImageContentID              string
	Command                     string
	ConfigurationRevisionID     string
	ConfigurationRevisionNumber int64
	AllowedEnvironmentKeys      []string
}

// MigrationRunner is implemented separately because a migration has a
// different configuration and persistence boundary from an application slot.
type MigrationRunner interface {
	Run(context.Context, MigrationRequest) error
}
