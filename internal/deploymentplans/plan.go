// Package deploymentplans owns immutable, purpose-bound accepted deployment
// plan revisions. SQLite contains audit-safe metadata only; commands and their
// supporting evidence remain exclusively in protected bundles.
package deploymentplans

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
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
	Source          SourceIdentity    `json:"source"`
	Components      []Component       `json:"components"`
	FieldProvenance []FieldProvenance `json:"fieldProvenance"`
	Migration       *Migration        `json:"migration,omitempty"`
}

// SourceIdentity binds acceptance to the analyzed immutable snapshot. Later
// releases are compatible only after their own immutable snapshot is analyzed;
// they are not required to reuse this historical digest.
type SourceIdentity struct {
	Provider       string `json:"provider"`
	RepositoryID   int64  `json:"repositoryId"`
	ResolvedDigest string `json:"resolvedDigest"`
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
	ComponentName   string            `json:"componentName,omitempty"`
	RootDirectory   string            `json:"rootDirectory,omitempty"`
	Command         string            `json:"command"`
	EnvironmentKeys []string          `json:"environmentKeys,omitempty"`
	EvidenceDigest  string            `json:"evidenceDigest"`
	Approval        MigrationApproval `json:"approval"`
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
	return canonicalPlanWithLegacyMigration(plan, false)
}

func canonicalPlanWithLegacyMigration(plan Plan, allowLegacyMigration bool) (Plan, error) {
	if plan.Strategy != StrategyGeneratedNode && plan.Strategy != StrategyCompose {
		return Plan{}, invalid("strategy", "Must be generated_node or compose")
	}
	if validateText(plan.Detector.Name, 256) != nil || validateText(plan.Detector.Version, 256) != nil || !validDigest(plan.Detector.SourceStructuralFingerprint) {
		return Plan{}, invalid("detector", "Must include name, version, and lowercase structural fingerprint")
	}
	if !validSourceIdentity(plan.Source) {
		return Plan{}, invalid("source", "Must bind a GitHub repository and resolved digest")
	}
	if len(plan.Components) > 64 || len(plan.FieldProvenance) > 256 || (plan.Strategy == StrategyGeneratedNode && (len(plan.Components) < 1 || len(plan.Components) > 2)) {
		return Plan{}, invalid("plan", "Contains too many entries")
	}
	result := Plan{Strategy: plan.Strategy, Detector: plan.Detector, Source: plan.Source, Migration: plan.Migration}
	result.Components = append([]Component(nil), plan.Components...)
	sort.Slice(result.Components, func(i, j int) bool { return result.Components[i].Name < result.Components[j].Name })
	seen := map[string]bool{}
	for _, component := range result.Components {
		if seen[component.Name] || validateText(component.Name, 256) != nil || !supportedRole(component.Role) || !validRootDirectory(component.RootDirectory) || !supportedPackageManager(component.PackageManager) || ValidateCommand(component.InstallBehavior) != nil || validateText(component.NodeVersion, 256) != nil || ValidateCommand(component.RunCommand) != nil || component.InternalPort == 0 || !validHealthProbe(component.HealthProbe) {
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
	for _, component := range result.Components {
		for _, field := range componentExecutionFields(component) {
			if !fields[field] {
				return Plan{}, invalid("fieldProvenance", "Every execution field requires provenance")
			}
		}
	}
	if result.Migration != nil {
		if ValidateCommand(result.Migration.Command) != nil || !validDigest(result.Migration.EvidenceDigest) {
			return Plan{}, invalid("migration", "Migration command and evidence digest are required")
		}
		if result.Migration.ComponentName == "" || result.Migration.RootDirectory == "" {
			if !allowLegacyMigration || result.Migration.ComponentName != "" || result.Migration.RootDirectory != "" || len(result.Migration.EnvironmentKeys) != 0 {
				return Plan{}, invalid("migration", "Migration must be bound to one component and root directory")
			}
		} else {
			component, exists := componentByName(result.Components, result.Migration.ComponentName)
			if !exists || component.RootDirectory != result.Migration.RootDirectory {
				return Plan{}, invalid("migration", "Migration component and root directory must match the accepted component")
			}
			keys := append([]string(nil), result.Migration.EnvironmentKeys...)
			sort.Strings(keys)
			for index, key := range keys {
				if key != "DATABASE_URL" || (index > 0 && keys[index-1] == key) {
					return Plan{}, invalid("migration", "Migration environment keys must use the supported explicit allowlist")
				}
			}
			result.Migration.EnvironmentKeys = keys
		}
		if result.Migration.Approval.Status != MigrationApprovalPending || result.Migration.Approval.ActorID != "" || result.Migration.Approval.At != "" {
			return Plan{}, invalid("migration", "Migration approval is granted only through its explicit approval operation")
		}
	}
	return result, nil
}

func componentByName(components []Component, name string) (Component, bool) {
	for _, component := range components {
		if component.Name == name {
			return component, true
		}
	}
	return Component{}, false
}

func supportedRole(value string) bool { return value == "server" || value == "static" }
func supportedPackageManager(value string) bool {
	return value == "npm" || value == "pnpm" || value == "yarn"
}
func validRootDirectory(value string) bool {
	if value == "." {
		return true
	}
	if value == "" || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || strings.Contains(value, ":") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	return validateText(value, 1024) == nil
}
func validHealthProbe(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.Contains(value, "?") && !strings.Contains(value, "#") && path.Clean(value) == value && validateText(value, 1024) == nil
}
func componentExecutionFields(component Component) []string {
	prefix := "components." + component.Name + "."
	fields := []string{prefix + "role", prefix + "rootDirectory", prefix + "packageManager", prefix + "installBehavior", prefix + "nodeVersion", prefix + "runCommand", prefix + "internalPort", prefix + "healthProbe"}
	if component.BuildCommand != "" {
		fields = append(fields, prefix+"buildCommand")
	}
	return fields
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
func validResolvedDigest(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, value := range value {
		if !(value >= '0' && value <= '9') && !(value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}
func validSourceIdentity(source SourceIdentity) bool {
	switch source.Provider {
	case "github":
		return source.RepositoryID > 0 && validResolvedDigest(source.ResolvedDigest)
	case "local":
		return source.RepositoryID == 0 && validDigest(source.ResolvedDigest)
	default:
		return false
	}
}

func invalid(field, message string) error {
	return &Error{Code: "invalid_deployment_plan", Fields: map[string]string{field: message}}
}
