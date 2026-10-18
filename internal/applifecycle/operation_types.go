package applifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

const canonicalPayloadLimit = 64 << 10

type BaselineDisposition string

const (
	BaselineRunning BaselineDisposition = "running"
	BaselineStopped BaselineDisposition = "stopped"
)

type RevisionRef struct {
	ID     string `json:"id"`
	Number int64  `json:"number"`
}

type ConfigurationRef struct {
	RevisionID        string      `json:"revisionId"`
	RevisionNumber    int64       `json:"revisionNumber"`
	FormatVersion     int         `json:"formatVersion"`
	PlanRevision      RevisionRef `json:"planRevision"`
	RuntimeComponents []string    `json:"runtimeComponents"`
}

type RetainedLimits struct {
	MemoryBytes            int64  `json:"memoryBytes"`
	MemorySwapBytes        int64  `json:"memorySwapBytes"`
	CPUQuota               int64  `json:"cpuQuota"`
	CPUPeriod              int64  `json:"cpuPeriod"`
	PIDsLimit              int64  `json:"pidsLimit"`
	TmpfsBytes             int64  `json:"tmpfsBytes"`
	LogDriver              string `json:"logDriver"`
	LogMaxSize             string `json:"logMaxSize"`
	LogMaxFiles            int    `json:"logMaxFiles"`
	HardeningPolicyVersion string `json:"hardeningPolicyVersion"`
}

type RetainedComponent struct {
	Name                  string         `json:"name"`
	Role                  string         `json:"role"`
	Technology            string         `json:"technology"`
	ContainerID           string         `json:"containerId"`
	ContainerName         string         `json:"containerName"`
	ArtifactID            string         `json:"artifactId"`
	ImageContentID        string         `json:"imageContentId"`
	BuildDefinitionDigest string         `json:"buildDefinitionDigest"`
	NetworkName           string         `json:"networkName"`
	NetworkAlias          string         `json:"networkAlias"`
	InternalPort          uint16         `json:"internalPort"`
	WorkingDirectory      string         `json:"workingDirectory"`
	RunCommandDigest      string         `json:"runCommandDigest"`
	Limits                RetainedLimits `json:"limits"`
}

// AppRouteAnchorV1 is a dependency-free digest anchor. Gateway adapters own
// the target record and mutable before/effective route revisions.
type AppRouteAnchorV1 struct {
	SelectedLineageKind           string `json:"selectedLineageKind"`
	SelectedLineageGeneration     int64  `json:"selectedLineageGeneration"`
	SelectedLineageOperationID    string `json:"selectedLineageOperationId"`
	SelectedLineageTerminalDigest string `json:"selectedLineageTerminalDigest"`
	ProtectedFormatVersion        int    `json:"protectedFormatVersion"`
	TargetRecordDigest            string `json:"targetRecordDigest"`
}

type GeneratedLifecyclePinsV1 struct {
	Version             int                 `json:"version"`
	DeploymentID        string              `json:"deploymentId"`
	ReleaseID           string              `json:"releaseId"`
	Slot                string              `json:"slot"`
	ActiveGeneration    int64               `json:"activeGeneration"`
	PlanRevision        RevisionRef         `json:"planRevision"`
	Configuration       ConfigurationRef    `json:"configuration"`
	BaselineDisposition BaselineDisposition `json:"baselineDisposition"`
	Components          []RetainedComponent `json:"components"`
	Route               AppRouteAnchorV1    `json:"route"`
}

type LifecycleRequest struct {
	ApplicationID           string
	ActorID                 string
	IdempotencyKey          string
	Action                  Action
	ExpectedDesiredRevision int64
}

type Operation struct {
	JobID            string
	ApplicationID    string
	DesiredRevision  int64
	ExpectedRevision int64
	Action           Action
	ActorID          string
	IdempotencyKey   string
	Pins             GeneratedLifecyclePinsV1
	PinsDigest       string
	CreatedAt        time.Time
}

type AcceptedOperation struct {
	Operation Operation
	Created   bool
}

type Boundary string

const (
	BoundaryWithdraw Boundary = "withdraw"
	BoundaryStop     Boundary = "stop"
	BoundaryStart    Boundary = "start"
	BoundaryHealth   Boundary = "health"
	BoundaryPublish  Boundary = "publish"
	BoundaryComplete Boundary = "complete"
)

type EffectMode string

const (
	EffectCommand         EffectMode = "command"
	EffectObservationOnly EffectMode = "observation_only"
)

type Outcome string

const (
	OutcomeRoutesAbsent     Outcome = "routes_absent"
	OutcomeContainerStopped Outcome = "container_stopped"
	OutcomeContainerRunning Outcome = "container_running"
	OutcomeHealthy          Outcome = "healthy"
	OutcomeServing          Outcome = "serving"
	OutcomeCompleteStopped  Outcome = "complete_stopped"
	OutcomeCompleteRunning  Outcome = "complete_running"
)

type EffectKey struct {
	Boundary      Boundary
	ComponentName string
}

type EffectRequestV1 struct {
	Version         int        `json:"version"`
	OperationID     string     `json:"operationId"`
	ApplicationID   string     `json:"applicationId"`
	DesiredRevision int64      `json:"desiredRevision"`
	Boundary        Boundary   `json:"boundary"`
	ComponentName   string     `json:"componentName"`
	Mode            EffectMode `json:"mode"`
	PinsDigest      string     `json:"pinsDigest"`
}

type EffectProofV1 struct {
	Version         int      `json:"version"`
	OperationID     string   `json:"operationId"`
	ApplicationID   string   `json:"applicationId"`
	DesiredRevision int64    `json:"desiredRevision"`
	Boundary        Boundary `json:"boundary"`
	ComponentName   string   `json:"componentName"`
	Outcome         Outcome  `json:"outcome"`
	IntentDigest    string   `json:"intentDigest"`
	EvidenceDigest  string   `json:"evidenceDigest"`
	PinsDigest      string   `json:"pinsDigest"`
}

type Intent struct {
	JobID         string
	Key           EffectKey
	WriterAttempt int
	Request       EffectRequestV1
	RequestDigest string
	CreatedAt     time.Time
}

type Receipt struct {
	JobID         string
	Key           EffectKey
	WriterAttempt int
	Outcome       Outcome
	Proof         EffectProofV1
	ProofDigest   string
	ObservedAt    time.Time
}

type PendingAuthority struct {
	claim        WorkerClaim
	intentDigest string
}

func (a PendingAuthority) JobID() string        { return a.claim.jobID }
func (a PendingAuthority) Attempt() int         { return a.claim.attempt }
func (a PendingAuthority) IntentDigest() string { return a.intentDigest }

var (
	ErrInvalidOperation    = errors.New("invalid application lifecycle operation")
	ErrOperationNotFound   = errors.New("application lifecycle operation not found")
	ErrMissingOperation    = errors.New("lifecycle job exists without an immutable operation")
	ErrInvalidPins         = errors.New("invalid generated lifecycle pins")
	ErrInvalidEffect       = errors.New("invalid lifecycle effect record")
	ErrEffectConflict      = errors.New("lifecycle effect record conflicts with retained history")
	ErrEffectNotFound      = errors.New("lifecycle effect record not found")
	ErrEffectNotAuthorized = errors.New("lifecycle effect is not currently authorized")
)

func canonicalPins(value GeneratedLifecyclePinsV1) ([]byte, string, error) {
	if value.Version != 1 || !bounded(value.DeploymentID, 256) || !bounded(value.ReleaseID, 256) ||
		(value.Slot != "blue" && value.Slot != "green") || value.ActiveGeneration < 1 ||
		!validRevision(value.PlanRevision) || value.Configuration.RevisionNumber < 0 ||
		value.Configuration.FormatVersion < 0 || value.Configuration.FormatVersion > 2 ||
		!validRevision(value.Configuration.PlanRevision) || value.Configuration.PlanRevision != value.PlanRevision ||
		(value.Configuration.RevisionNumber == 0) != (value.Configuration.RevisionID == "") ||
		(value.Configuration.RevisionNumber == 0) != (value.Configuration.FormatVersion == 0) ||
		(value.Configuration.RevisionNumber > 0 && !bounded(value.Configuration.RevisionID, 256)) ||
		(value.BaselineDisposition != BaselineRunning && value.BaselineDisposition != BaselineStopped) ||
		len(value.Components) < 1 || len(value.Components) > 2 || len(value.Configuration.RuntimeComponents) != len(value.Components) {
		return nil, "", ErrInvalidPins
	}
	for index, component := range value.Components {
		if index > 0 && value.Components[index-1].Name >= component.Name {
			return nil, "", ErrInvalidPins
		}
		if value.Configuration.RuntimeComponents[index] != component.Name || !validComponent(component) {
			return nil, "", ErrInvalidPins
		}
	}
	if (value.Route.SelectedLineageKind != "gateway_upgrade" && value.Route.SelectedLineageKind != "gateway_rebind") ||
		value.Route.SelectedLineageGeneration < 1 || !bounded(value.Route.SelectedLineageOperationID, 256) ||
		!validDigest(value.Route.SelectedLineageTerminalDigest) || value.Route.ProtectedFormatVersion < 1 ||
		!validDigest(value.Route.TargetRecordDigest) {
		return nil, "", ErrInvalidPins
	}
	return canonicalJSON(value)
}

func validComponent(value RetainedComponent) bool {
	return bounded(value.Name, 256) && bounded(value.Role, 64) && bounded(value.Technology, 64) &&
		len(value.ContainerID) == 64 && validLowerHex(value.ContainerID) && bounded(value.ContainerName, 128) &&
		bounded(value.ArtifactID, 256) && len(value.ImageContentID) == 71 && bytes.HasPrefix([]byte(value.ImageContentID), []byte("sha256:")) &&
		validLowerHex(value.ImageContentID[7:]) && validDigest(value.BuildDefinitionDigest) &&
		bounded(value.NetworkName, 128) && bounded(value.NetworkAlias, 128) && value.InternalPort > 0 &&
		len(value.WorkingDirectory) <= 1024 && validDigest(value.RunCommandDigest) && validLimits(value.Limits)
}

func validLimits(value RetainedLimits) bool {
	return value.MemoryBytes > 0 && value.MemorySwapBytes >= value.MemoryBytes && value.CPUQuota > 0 &&
		value.CPUPeriod > 0 && value.PIDsLimit > 0 && value.TmpfsBytes >= 0 &&
		bounded(value.LogDriver, 64) && bounded(value.LogMaxSize, 64) && value.LogMaxFiles > 0 &&
		bounded(value.HardeningPolicyVersion, 128)
}

func canonicalEffectRequest(value EffectRequestV1) ([]byte, string, error) {
	if value.Version != 1 || !bounded(value.OperationID, 256) || !bounded(value.ApplicationID, 256) ||
		value.DesiredRevision < 1 || !validBoundary(value.Boundary) || len(value.ComponentName) > 256 ||
		(value.Mode != EffectCommand && value.Mode != EffectObservationOnly) || !validDigest(value.PinsDigest) {
		return nil, "", ErrInvalidEffect
	}
	return canonicalJSON(value)
}

func canonicalEffectProof(value EffectProofV1) ([]byte, string, error) {
	if value.Version != 1 || !bounded(value.OperationID, 256) || !bounded(value.ApplicationID, 256) ||
		value.DesiredRevision < 1 || !validBoundary(value.Boundary) || len(value.ComponentName) > 256 ||
		!validOutcome(value.Outcome) || !validDigest(value.IntentDigest) || !validDigest(value.EvidenceDigest) ||
		!validDigest(value.PinsDigest) {
		return nil, "", ErrInvalidEffect
	}
	return canonicalJSON(value)
}

func canonicalJSON(value any) ([]byte, string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > canonicalPayloadLimit {
		return nil, "", ErrInvalidEffect
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func decodeCanonical(raw []byte, destination any, validate func() ([]byte, string, error), digest string) error {
	if len(raw) == 0 || len(raw) > canonicalPayloadLimit || !validDigest(digest) {
		return ErrInvalidEffect
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalidEffect
	}
	canonical, computed, err := validate()
	if err != nil || !bytes.Equal(canonical, raw) || computed != digest {
		return ErrInvalidEffect
	}
	return nil
}

func validRevision(value RevisionRef) bool   { return bounded(value.ID, 256) && value.Number > 0 }
func bounded(value string, maximum int) bool { return len(value) > 0 && len(value) <= maximum }
func validDigest(value string) bool          { return len(value) == 64 && validLowerHex(value) }
func validLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
func validBoundary(value Boundary) bool {
	switch value {
	case BoundaryWithdraw, BoundaryStop, BoundaryStart, BoundaryHealth, BoundaryPublish, BoundaryComplete:
		return true
	}
	return false
}
func validOutcome(value Outcome) bool {
	switch value {
	case OutcomeRoutesAbsent, OutcomeContainerStopped, OutcomeContainerRunning, OutcomeHealthy, OutcomeServing, OutcomeCompleteStopped, OutcomeCompleteRunning:
		return true
	}
	return false
}

func validateLifecycleRequest(value LifecycleRequest) error {
	if !bounded(value.ApplicationID, 256) || !bounded(value.ActorID, 256) || !bounded(value.IdempotencyKey, 200) || value.ExpectedDesiredRevision < 0 {
		return ErrInvalidOperation
	}
	_, err := targetState(value.Action)
	return err
}

func sortedComponentNames(pins GeneratedLifecyclePinsV1) []string {
	names := make([]string, len(pins.Components))
	for index := range pins.Components {
		names[index] = pins.Components[index].Name
	}
	sort.Strings(names)
	return names
}

func operationMatchesRequest(operation Operation, request LifecycleRequest) bool {
	return operation.ApplicationID == request.ApplicationID && operation.Action == request.Action &&
		operation.ActorID == request.ActorID && operation.IdempotencyKey == request.IdempotencyKey &&
		operation.ExpectedRevision == request.ExpectedDesiredRevision
}

func wrapInvalid(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}
