// Package deploymentplans owns immutable, purpose-bound accepted deployment
// plan revisions. SQLite contains audit-safe metadata only; commands and their
// supporting evidence remain exclusively in protected bundles.
package deploymentplans

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxBundleBytes  = 48 << 10
	maxCommandBytes = 8 << 10
)

type Strategy string

const (
	StrategyGeneratedNode Strategy = "generated_node"
	StrategyCompose       Strategy = "compose"
)

type Provenance string

const (
	ProvenanceInferred Provenance = "inferred"
	ProvenanceUser     Provenance = "user"
)

type RevisionState string

const (
	RevisionAccepted   RevisionState = "accepted"
	RevisionSuperseded RevisionState = "superseded"
)

type MigrationApprovalStatus string

const (
	MigrationApprovalPending  MigrationApprovalStatus = "pending"
	MigrationApprovalApproved MigrationApprovalStatus = "approved"
)

type Error struct {
	Code   string
	Fields map[string]string
}

func (e *Error) Error() string { return "deployment plan: " + e.Code }
func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

type Plan struct {
	Strategy        Strategy          `json:"strategy"`
	Detector        Detector          `json:"detector"`
	Components      []Component       `json:"components"`
	FieldProvenance []FieldProvenance `json:"fieldProvenance"`
	Migration       *Migration        `json:"migration,omitempty"`
}

type Detector struct {
	Name                        string `json:"name"`
	Version                     string `json:"version"`
	SourceStructuralFingerprint string `json:"sourceStructuralFingerprint"`
}

// Component has explicit execution-contract fields, preventing later layers
// from reinterpreting generic command positions.
type Component struct {
	Name            string `json:"name"`
	Role            string `json:"role"`
	RootDirectory   string `json:"rootDirectory"`
	PackageManager  string `json:"packageManager"`
	InstallBehavior string `json:"installBehavior"`
	NodeVersion     string `json:"nodeVersion"`
	BuildCommand    string `json:"buildCommand,omitempty"`
	RunCommand      string `json:"runCommand"`
	InternalPort    uint16 `json:"internalPort"`
	HealthProbe     string `json:"healthProbe"`
}

type FieldProvenance struct {
	Field      string     `json:"field"`
	Origin     Provenance `json:"origin"`
	Confidence int        `json:"confidence"`
	Evidence   []string   `json:"evidence"`
}

// Migration is protected plan content. Its approval is independent from plan
// acceptance; a later runtime must require Approved before running it.
type Migration struct {
	Command        string            `json:"command"`
	EvidenceDigest string            `json:"evidenceDigest"`
	Approval       MigrationApproval `json:"approval"`
}

type MigrationApproval struct {
	Status  MigrationApprovalStatus `json:"status"`
	ActorID string                  `json:"actorId,omitempty"`
	At      string                  `json:"at,omitempty"`
}

type DeploymentPlanRevision struct {
	ID              string
	AppID           string
	RevisionNumber  int64
	Plan            Plan
	CanonicalDigest string
	RevisedBy       string
	RevisedAt       string
	AcceptedBy      string
	AcceptedAt      string
	State           RevisionState
}

type ReplaceInput struct {
	ExpectedRevisionNumber int64
	Plan                   Plan
}

func ValidateCommand(command string) error {
	if !utf8.ValidString(command) || len(command) == 0 || len(command) > maxCommandBytes || strings.TrimSpace(command) == "" {
		return errors.New("command must be bounded valid UTF-8")
	}
	for _, value := range command {
		if value == '\n' || value == '\r' || value == 0 || unicode.IsControl(value) {
			return errors.New("command cannot contain line breaks or control characters")
		}
	}
	return nil
}

func CanonicalDigest(plan Plan) (string, error) {
	canonical, err := canonicalPlan(plan)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalPlan(plan Plan) (Plan, error) {
	if plan.Strategy != StrategyGeneratedNode && plan.Strategy != StrategyCompose {
		return Plan{}, invalid("strategy", "Must be generated_node or compose")
	}
	if validateText(plan.Detector.Name, 256) != nil || validateText(plan.Detector.Version, 256) != nil || !validDigest(plan.Detector.SourceStructuralFingerprint) {
		return Plan{}, invalid("detector", "Must include name, version, and lowercase structural fingerprint")
	}
	if len(plan.Components) > 64 || len(plan.FieldProvenance) > 256 {
		return Plan{}, invalid("plan", "Contains too many entries")
	}
	result := Plan{Strategy: plan.Strategy, Detector: plan.Detector, Migration: plan.Migration}
	result.Components = append([]Component(nil), plan.Components...)
	sort.Slice(result.Components, func(i, j int) bool { return result.Components[i].Name < result.Components[j].Name })
	seen := map[string]bool{}
	for _, component := range result.Components {
		if seen[component.Name] || validateText(component.Name, 256) != nil || validateText(component.Role, 256) != nil || validateText(component.RootDirectory, 1024) != nil || validateText(component.PackageManager, 256) != nil || validateText(component.InstallBehavior, 256) != nil || validateText(component.NodeVersion, 256) != nil || ValidateCommand(component.RunCommand) != nil || validateText(component.HealthProbe, 1024) != nil {
			return Plan{}, invalid("components", "Components must use complete bounded execution fields")
		}
		if component.BuildCommand != "" && ValidateCommand(component.BuildCommand) != nil {
			return Plan{}, invalid("components", "Build command is invalid")
		}
		seen[component.Name] = true
	}
	result.FieldProvenance = append([]FieldProvenance(nil), plan.FieldProvenance...)
	sort.Slice(result.FieldProvenance, func(i, j int) bool { return result.FieldProvenance[i].Field < result.FieldProvenance[j].Field })
	fields := map[string]bool{}
	for index := range result.FieldProvenance {
		field := &result.FieldProvenance[index]
		if fields[field.Field] || validateText(field.Field, 256) != nil || (field.Origin != ProvenanceInferred && field.Origin != ProvenanceUser) || field.Confidence < 0 || field.Confidence > 100 || canonicalEvidence(&field.Evidence) != nil {
			return Plan{}, invalid("fieldProvenance", "Fields require inferred or user provenance, confidence, and evidence")
		}
		fields[field.Field] = true
	}
	if result.Migration != nil {
		if ValidateCommand(result.Migration.Command) != nil || !validDigest(result.Migration.EvidenceDigest) {
			return Plan{}, invalid("migration", "Migration command and evidence digest are required")
		}
		approval := result.Migration.Approval
		if approval.Status == MigrationApprovalPending {
			if approval.ActorID != "" || approval.At != "" {
				return Plan{}, invalid("migration", "Pending approval cannot name an actor")
			}
		} else if approval.Status == MigrationApprovalApproved {
			if validateText(approval.ActorID, 256) != nil || validateText(approval.At, 128) != nil {
				return Plan{}, invalid("migration", "Approved migration requires actor and timestamp")
			}
		} else {
			return Plan{}, invalid("migration", "Approval must be pending or approved")
		}
	}
	return result, nil
}

func canonicalEvidence(values *[]string) error {
	if len(*values) == 0 || len(*values) > 32 {
		return errors.New("invalid evidence")
	}
	copied := append([]string(nil), (*values)...)
	sort.Strings(copied)
	for index, value := range copied {
		if validateText(value, 4096) != nil || (index > 0 && copied[index-1] == value) {
			return errors.New("invalid evidence")
		}
	}
	*values = copied
	return nil
}

func validateText(value string, maximum int) error {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > maximum || strings.TrimSpace(value) == "" {
		return errors.New("invalid text")
	}
	for _, value := range value {
		if value == 0 || unicode.IsControl(value) {
			return errors.New("invalid text")
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, value := range value {
		if !(value >= '0' && value <= '9') && !(value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}

func invalid(field, message string) error {
	return &Error{Code: "invalid_deployment_plan", Fields: map[string]string{field: message}}
}
