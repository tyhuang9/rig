// Package projectanalysis infers reviewable deployment plans from repository
// metadata. It never executes repository code or package scripts.
package projectanalysis

import (
	"context"
	"errors"
	"fmt"
)

const (
	// SchemaVersion is incremented when the serialized analysis contract changes.
	SchemaVersion = "2"

	// PinnedNodeLTS is the deterministic fallback used when a repository does not
	// declare a Node.js version. It must only change with a schema version change.
	PinnedNodeLTS = "24"

	// ManagedStaticServerCommand is supplied by Rig's runtime, not the repository.
	ManagedStaticServerPort    = "8080"
	ManagedStaticServerCommand = "rig-static --root dist --port " + ManagedStaticServerPort
)

const (
	StatusReady       = "ready"
	StatusNeedsInput  = "needs_input"
	StatusUnsupported = "unsupported"
)

const (
	PlanKindJavaScript = "javascript"
	PlanKindCompose    = "compose"
	PlanKindDockerfile = "dockerfile"
)

const (
	ComponentServer = "server"
	ComponentStatic = "static"
	ComponentWorker = "worker"
)

const (
	FrameworkNode    = "node"
	FrameworkNextJS  = "nextjs"
	FrameworkVite    = "vite"
	FrameworkExpress = "express"
	FrameworkFastify = "fastify"
	FrameworkNestJS  = "nestjs"
)

const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

const (
	ProvenanceManifestScript   = "manifest_script"
	ProvenancePackageManager   = "package_manager"
	ProvenanceLockfile         = "lockfile"
	ProvenanceRuntimeFile      = "runtime_file"
	ProvenanceEngineConstraint = "engine_constraint"
	ProvenanceRuntimeDefault   = "runtime_default"
	ProvenanceFrameworkDefault = "framework_default"
	ProvenanceManagedRuntime   = "managed_runtime"
	ProvenanceConfigFile       = "config_file"
)

const (
	OriginInferred = "inferred"
	OriginUser     = "user"
)

const (
	FieldPackageManager  = "package_manager"
	FieldInstallBehavior = "install_behavior"
)

// File describes an entry in a normalized repository snapshot. Paths must be
// slash-separated, relative, and unique under case-insensitive comparison.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// FileReader provides bounded access to files named in the supplied snapshot.
// Implementations must not return more than maxBytes.
type FileReader interface {
	ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error)
}

// SourceAnalysis is the stable, serializable result of repository inspection.
type SourceAnalysis struct {
	SchemaVersion         string                    `json:"schema_version"`
	StructuralFingerprint string                    `json:"structural_fingerprint"`
	Candidates            []DeploymentPlanCandidate `json:"candidates"`
	Findings              []Finding                 `json:"findings"`
}

// DeploymentPlanCandidate is a reviewable way Rig could deploy the source.
type DeploymentPlanCandidate struct {
	ID             string          `json:"id"`
	Origin         string          `json:"origin"`
	Status         string          `json:"status"`
	Kind           string          `json:"kind"`
	RootDirectory  string          `json:"root_directory"`
	ConfigPath     string          `json:"config_path,omitempty"`
	PackageManager PackageManager  `json:"package_manager,omitempty"`
	NodeVersion    InferredValue   `json:"node_version,omitempty"`
	Install        *Command        `json:"install,omitempty"`
	Components     []Component     `json:"components"`
	Evidence       []Evidence      `json:"evidence"`
	Findings       []Finding       `json:"findings"`
	MissingFields  []string        `json:"missing_fields"`
	AdvancedInputs []AdvancedInput `json:"advanced_inputs"`
	Digest         string          `json:"digest"`
}

type PackageManager struct {
	Origin     string     `json:"origin,omitempty"`
	Name       string     `json:"name,omitempty"`
	Version    string     `json:"version,omitempty"`
	Lockfile   string     `json:"lockfile,omitempty"`
	Provenance string     `json:"provenance,omitempty"`
	Confidence string     `json:"confidence,omitempty"`
	Evidence   []Evidence `json:"evidence"`
}

type InferredValue struct {
	Origin     string     `json:"origin,omitempty"`
	Value      string     `json:"value,omitempty"`
	Provenance string     `json:"provenance,omitempty"`
	Confidence string     `json:"confidence,omitempty"`
	Evidence   []Evidence `json:"evidence"`
}

type Component struct {
	ID                    string         `json:"id"`
	Origin                string         `json:"origin"`
	Name                  string         `json:"name,omitempty"`
	Kind                  string         `json:"kind"`
	Framework             string         `json:"framework"`
	RootDirectory         string         `json:"root_directory"`
	StaticOutputDirectory string         `json:"static_output_directory,omitempty"`
	InternalPort          *InferredValue `json:"internal_port,omitempty"`
	HealthProbe           *HealthProbe   `json:"health_probe,omitempty"`
	MigrationFingerprint  string         `json:"migration_fingerprint,omitempty"`
	Build                 *Command       `json:"build,omitempty"`
	Run                   *Command       `json:"run,omitempty"`
	Migration             *Command       `json:"migration,omitempty"`
	Evidence              []Evidence     `json:"evidence"`
	Findings              []Finding      `json:"findings"`
}

type Command struct {
	Origin           string     `json:"origin"`
	Phase            string     `json:"phase"`
	Command          string     `json:"command"`
	WorkingDirectory string     `json:"working_directory"`
	Provenance       string     `json:"provenance"`
	Confidence       string     `json:"confidence"`
	Evidence         []Evidence `json:"evidence"`
}

type HealthProbe struct {
	Origin     string     `json:"origin"`
	Path       string     `json:"path"`
	Method     string     `json:"method"`
	Provenance string     `json:"provenance"`
	Confidence string     `json:"confidence"`
	Evidence   []Evidence `json:"evidence"`
}

// AdvancedInput makes optional or framework-dependent deployment settings
// reviewable instead of silently guessing them.
type AdvancedInput struct {
	Field       string `json:"field"`
	ComponentID string `json:"component_id,omitempty"`
	Required    bool   `json:"required"`
	Reason      string `json:"reason"`
}

type Evidence struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Field    string `json:"field,omitempty"`
}

type ErrorCode string

const (
	CodeUnsafePath     ErrorCode = "unsafe_path"
	CodeDuplicatePath  ErrorCode = "duplicate_path"
	CodeSourceTooLarge ErrorCode = "source_too_large"
	CodeFileTooLarge   ErrorCode = "file_too_large"
	CodeSourceChanged  ErrorCode = "source_changed"
	CodeReadFailed     ErrorCode = "read_failed"
)

var ErrFileTooLarge = errors.New("file exceeds read limit")

type AnalysisError struct {
	Code ErrorCode
	Path string
	Err  error
}

func (e *AnalysisError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("project analysis %s for %q: %v", e.Code, e.Path, e.Err)
	}
	return fmt.Sprintf("project analysis %s: %v", e.Code, e.Err)
}

func (e *AnalysisError) Unwrap() error { return e.Err }

func IsErrorCode(err error, code ErrorCode) bool {
	var target *AnalysisError
	return errors.As(err, &target) && target.Code == code
}
