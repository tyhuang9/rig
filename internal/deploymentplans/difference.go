package deploymentplans

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/projectanalysis"
)

// PlanDifference identifies one review-relevant field whose newly inferred
// value no longer agrees with an accepted generated plan. Values and commands
// are intentionally omitted so this result is safe to persist and observe.
type PlanDifference struct {
	Field string
}

// CompareAnalysis compares a fresh, non-executing repository analysis with an
// accepted generated plan. User-authored execution fields remain authoritative;
// inferred fields, component topology, detector semantics, and migration
// evidence must still agree. The returned field names are unique and sorted.
func CompareAnalysis(plan Plan, analysis projectanalysis.SourceAnalysis) ([]PlanDifference, error) {
	if plan.Strategy != StrategyGeneratedNode {
		return nil, errors.New("analysis comparison requires a generated plan")
	}
	if _, err := CanonicalDigest(plan); err != nil {
		return nil, err
	}

	differences := make(map[string]struct{})
	add := func(field string) { differences[field] = struct{}{} }
	if plan.Detector.Name != "projectanalysis" || plan.Detector.Version != analysis.SchemaVersion {
		add("detector")
	}

	candidates := matchingTopologyCandidates(plan, analysis.Candidates)
	if len(candidates) != 1 {
		add("components")
		return sortedDifferences(differences), nil
	}
	candidate := candidates[0]
	provenance := provenanceByField(plan.FieldProvenance)
	components := analysisComponentsByID(candidate.Components)
	recordMissingFields(differences, provenance, plan.Components, candidate.MissingFields)

	for _, accepted := range plan.Components {
		inferred := components[accepted.Name]
		prefix := "components." + accepted.Name + "."
		compareInferred(differences, provenance, prefix+"role", accepted.Role, inferred.Kind)
		compareInferred(differences, provenance, prefix+"rootDirectory", accepted.RootDirectory, normalizedRoot(inferred.RootDirectory))
		compareInferred(differences, provenance, prefix+"packageManager", accepted.PackageManager, candidate.PackageManager.Name)
		compareInferred(differences, provenance, prefix+"installBehavior", accepted.InstallBehavior, commandValue(candidate.Install))
		compareInferred(differences, provenance, prefix+"nodeVersion", accepted.NodeVersion, candidate.NodeVersion.Value)
		compareOptionalBuild(differences, provenance, prefix+"buildCommand", accepted.BuildCommand, commandValue(inferred.Build))
		compareInferred(differences, provenance, prefix+"runCommand", accepted.RunCommand, commandValue(inferred.Run))
		compareInferred(differences, provenance, prefix+"internalPort", strconv.Itoa(int(accepted.InternalPort)), inferredValue(inferred.InternalPort))
		compareInferred(differences, provenance, prefix+"healthProbe", accepted.HealthProbe, probeValue(inferred.HealthProbe))
	}

	compareMigrationEvidence(differences, plan, candidate)
	return sortedDifferences(differences), nil
}

func recordMissingFields(differences map[string]struct{}, provenance map[string]Provenance, components []Component, missing []string) {
	for _, field := range missing {
		switch field {
		case projectanalysis.FieldPackageManager:
			for _, component := range components {
				addMissingInferred(differences, provenance, "components."+component.Name+".packageManager")
			}
		case projectanalysis.FieldInstallBehavior:
			for _, component := range components {
				addMissingInferred(differences, provenance, "components."+component.Name+".installBehavior")
			}
		case "node_version":
			for _, component := range components {
				addMissingInferred(differences, provenance, "components."+component.Name+".nodeVersion")
			}
		default:
			canonical, overrideable := canonicalMissingField(field)
			if !overrideable {
				differences[field] = struct{}{}
				continue
			}
			addMissingInferred(differences, provenance, canonical)
		}
	}
}

func canonicalMissingField(field string) (string, bool) {
	for suffix, replacement := range map[string]string{
		".build": ".buildCommand", ".run": ".runCommand", ".internal_port": ".internalPort", ".health_probe": ".healthProbe",
	} {
		if strings.HasPrefix(field, "components.") && strings.HasSuffix(field, suffix) {
			return strings.TrimSuffix(field, suffix) + replacement, true
		}
	}
	return "", false
}

func addMissingInferred(differences map[string]struct{}, provenance map[string]Provenance, field string) {
	if provenance[field] != ProvenanceUser {
		differences[field] = struct{}{}
	}
}

func matchingTopologyCandidates(plan Plan, candidates []projectanalysis.DeploymentPlanCandidate) []projectanalysis.DeploymentPlanCandidate {
	accepted := make(map[string]Component, len(plan.Components))
	for _, component := range plan.Components {
		accepted[component.Name] = component
	}
	result := make([]projectanalysis.DeploymentPlanCandidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.Kind != projectanalysis.PlanKindJavaScript || candidate.Status == projectanalysis.StatusUnsupported || len(candidate.Components) != len(accepted) {
			continue
		}
		matched := true
		seen := make(map[string]struct{}, len(candidate.Components))
		for _, component := range candidate.Components {
			value, ok := accepted[component.ID]
			if !ok || value.Role != component.Kind || value.RootDirectory != normalizedRoot(component.RootDirectory) {
				matched = false
				break
			}
			if _, duplicate := seen[component.ID]; duplicate {
				matched = false
				break
			}
			seen[component.ID] = struct{}{}
		}
		if matched {
			result = append(result, candidate)
		}
	}
	return result
}

func provenanceByField(values []FieldProvenance) map[string]Provenance {
	result := make(map[string]Provenance, len(values))
	for _, value := range values {
		result[value.Field] = value.Origin
	}
	return result
}

func analysisComponentsByID(values []projectanalysis.Component) map[string]projectanalysis.Component {
	result := make(map[string]projectanalysis.Component, len(values))
	for _, value := range values {
		result[value.ID] = value
	}
	return result
}

func compareInferred(differences map[string]struct{}, provenance map[string]Provenance, field, accepted, inferred string) {
	if provenance[field] == ProvenanceUser {
		return
	}
	if accepted != inferred {
		differences[field] = struct{}{}
	}
}

func compareOptionalBuild(differences map[string]struct{}, provenance map[string]Provenance, field, accepted, inferred string) {
	if provenance[field] == ProvenanceUser {
		return
	}
	// An omitted build field represents an inferred no-build plan. A newly
	// discovered build command is review-relevant even though the old canonical
	// plan did not require provenance for the empty field.
	if accepted != inferred {
		differences[field] = struct{}{}
	}
}

func compareMigrationEvidence(differences map[string]struct{}, plan Plan, candidate projectanalysis.DeploymentPlanCandidate) {
	var inferredComponent, inferredDigest string
	count := 0
	for _, component := range candidate.Components {
		if component.Migration == nil {
			continue
		}
		count++
		inferredComponent, inferredDigest = component.ID, component.MigrationFingerprint
	}
	if plan.Migration == nil {
		if count != 0 {
			differences["migration.evidence"] = struct{}{}
		}
		return
	}
	if count != 1 || inferredComponent != plan.Migration.ComponentName || inferredDigest == "" || inferredDigest != plan.Migration.EvidenceDigest {
		differences["migration.evidence"] = struct{}{}
	}
}

func sortedDifferences(values map[string]struct{}) []PlanDifference {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	result := make([]PlanDifference, 0, len(fields))
	for _, field := range fields {
		result = append(result, PlanDifference{Field: field})
	}
	return result
}

func normalizedRoot(value string) string {
	if value == "" {
		return "."
	}
	return value
}

func commandValue(value *projectanalysis.Command) string {
	if value == nil {
		return ""
	}
	return value.Command
}

func inferredValue(value *projectanalysis.InferredValue) string {
	if value == nil {
		return ""
	}
	return value.Value
}

func probeValue(value *projectanalysis.HealthProbe) string {
	if value == nil {
		return ""
	}
	return value.Path
}
