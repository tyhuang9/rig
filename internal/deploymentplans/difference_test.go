package deploymentplans

import (
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/projectanalysis"
)

func TestCompareAnalysisAcceptsUnchangedInferenceAndManualOverrides(t *testing.T) {
	plan, analysis := comparisonFixture()
	plan.Components[0].RunCommand = "node custom.js && echo ready"
	setOrigin(&plan, "components.web.runCommand", ProvenanceUser)
	analysis.Candidates[0].Components[0].Run.Command = "npm start"
	analysis.Candidates[0].MissingFields = []string{"components.web.run"}

	differences, err := CompareAnalysis(plan, analysis)
	if err != nil {
		t.Fatal(err)
	}
	if len(differences) != 0 {
		t.Fatalf("differences = %#v", differences)
	}
}

func TestCompareAnalysisAcceptsReviewedYarnVersionAmbiguity(t *testing.T) {
	plan, analysis := comparisonFixture()
	plan.Components[0].PackageManager = "yarn"
	plan.Components[0].InstallBehavior = "corepack prepare yarn@1.22.22 --activate && corepack yarn install --frozen-lockfile"
	setOrigin(&plan, "components.web.packageManager", ProvenanceUser)
	setOrigin(&plan, "components.web.installBehavior", ProvenanceUser)
	analysis.Candidates[0].PackageManager.Name = "yarn"
	analysis.Candidates[0].PackageManager.Version = ""
	analysis.Candidates[0].Install = nil
	analysis.Candidates[0].MissingFields = []string{"package_manager.version"}

	differences, err := CompareAnalysis(plan, analysis)
	if err != nil {
		t.Fatal(err)
	}
	if len(differences) != 0 {
		t.Fatalf("differences = %#v", differences)
	}
}

func TestCompareAnalysisFailsClosedForNewUnresolvedTopology(t *testing.T) {
	plan, analysis := comparisonFixture()
	analysis.Candidates[0].MissingFields = []string{"components.web.role", "topology.server_components"}

	differences, err := CompareAnalysis(plan, analysis)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlanDifference{{Field: "components.web.role"}, {Field: "topology.server_components"}}
	if !reflect.DeepEqual(differences, want) {
		t.Fatalf("differences = %#v, want %#v", differences, want)
	}
}

func TestCompareAnalysisReportsOnlyReviewRelevantFieldNames(t *testing.T) {
	plan, analysis := comparisonFixture()
	analysis.SchemaVersion = "future"
	analysis.Candidates[0].PackageManager.Name = "pnpm"
	analysis.Candidates[0].Components[0].Build = &projectanalysis.Command{Command: "npm run build"}
	analysis.Candidates[0].Components[0].Run.Command = "node changed.js"
	analysis.Candidates[0].Components[0].InternalPort.Value = "4000"
	analysis.Candidates[0].Components[0].HealthProbe.Path = "/ready"

	differences, err := CompareAnalysis(plan, analysis)
	if err != nil {
		t.Fatal(err)
	}
	want := []PlanDifference{
		{Field: "components.web.buildCommand"},
		{Field: "components.web.healthProbe"},
		{Field: "components.web.internalPort"},
		{Field: "components.web.packageManager"},
		{Field: "components.web.runCommand"},
		{Field: "detector"},
	}
	if !reflect.DeepEqual(differences, want) {
		t.Fatalf("differences = %#v, want %#v", differences, want)
	}
}

func TestCompareAnalysisFailsClosedForAmbiguousTopology(t *testing.T) {
	plan, analysis := comparisonFixture()
	analysis.Candidates = append(analysis.Candidates, analysis.Candidates[0])

	differences, err := CompareAnalysis(plan, analysis)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(differences, []PlanDifference{{Field: "components"}}) {
		t.Fatalf("differences = %#v", differences)
	}
}

func TestCompareAnalysisTracksMigrationEvidenceWithoutCommandContent(t *testing.T) {
	plan, analysis := comparisonFixture()
	plan.Migration = &Migration{
		ComponentName: "web", RootDirectory: ".", Command: "npm run migrate",
		EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: digestOf("b"),
		Approval: MigrationApproval{Status: MigrationApprovalPending},
	}
	analysis.Candidates[0].Components[0].Migration = &projectanalysis.Command{Command: "npm run migrate"}
	analysis.Candidates[0].Components[0].MigrationFingerprint = digestOf("b")

	differences, err := CompareAnalysis(plan, analysis)
	if err != nil || len(differences) != 0 {
		t.Fatalf("matching migration differences = %#v, err=%v", differences, err)
	}
	analysis.Candidates[0].Components[0].MigrationFingerprint = digestOf("c")
	differences, err = CompareAnalysis(plan, analysis)
	if err != nil || !reflect.DeepEqual(differences, []PlanDifference{{Field: "migration.evidence"}}) {
		t.Fatalf("changed migration differences = %#v, err=%v", differences, err)
	}
}

func TestCompareAnalysisIgnoresApprovedExternalMigrationLifecycle(t *testing.T) {
	plan, analysis := comparisonFixture()
	plan.Migration = &Migration{
		ComponentName: "web", RootDirectory: ".", Command: "npm run migrate",
		EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: digestOf("b"),
		Approval: MigrationApproval{Status: MigrationApprovalApproved, ActorID: "admin", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)},
	}
	analysis.Candidates[0].Components[0].Migration = &projectanalysis.Command{Command: "npm run migrate"}
	analysis.Candidates[0].Components[0].MigrationFingerprint = digestOf("b")

	if _, err := CanonicalDigest(plan); err == nil {
		t.Fatal("strict immutable plan canonicalization accepted an externally applied approval")
	}
	differences, err := CompareAnalysis(plan, analysis)
	if err != nil || len(differences) != 0 {
		t.Fatalf("approved lifecycle differences = %#v, err=%v", differences, err)
	}
}

func comparisonFixture() (Plan, projectanalysis.SourceAnalysis) {
	component := Component{
		Name: "web", Role: "server", RootDirectory: ".", PackageManager: "npm",
		InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "24", RunCommand: "npm start",
		InternalPort: 3000, HealthProbe: "/health",
	}
	fields := componentExecutionFields(component)
	provenance := make([]FieldProvenance, 0, len(fields))
	for _, field := range fields {
		provenance = append(provenance, FieldProvenance{Field: field, Origin: ProvenanceInferred, Confidence: 90, Evidence: []string{"package.json"}})
	}
	plan := Plan{
		Strategy:   StrategyGeneratedNode,
		Detector:   Detector{Name: "projectanalysis", Version: projectanalysis.SchemaVersion, SourceStructuralFingerprint: digestOf("a")},
		Source:     SourceIdentity{Provider: "github", RepositoryID: 7, ResolvedDigest: "0123456789abcdef0123456789abcdef01234567"},
		Components: []Component{component}, FieldProvenance: provenance,
	}
	analysis := projectanalysis.SourceAnalysis{
		SchemaVersion: projectanalysis.SchemaVersion, StructuralFingerprint: digestOf("a"),
		Candidates: []projectanalysis.DeploymentPlanCandidate{{
			ID: "candidate", Kind: projectanalysis.PlanKindJavaScript, Status: projectanalysis.StatusReady,
			PackageManager: projectanalysis.PackageManager{Name: "npm"},
			NodeVersion:    projectanalysis.InferredValue{Value: "24"},
			Install:        &projectanalysis.Command{Command: "npm ci"},
			Components: []projectanalysis.Component{{
				ID: "web", Kind: "server", RootDirectory: ".",
				Run:          &projectanalysis.Command{Command: "npm start"},
				InternalPort: &projectanalysis.InferredValue{Value: "3000"},
				HealthProbe:  &projectanalysis.HealthProbe{Path: "/health"},
			}},
		}},
	}
	return plan, analysis
}

func setOrigin(plan *Plan, field string, origin Provenance) {
	for index := range plan.FieldProvenance {
		if plan.FieldProvenance[index].Field == field {
			plan.FieldProvenance[index].Origin = origin
			plan.FieldProvenance[index].Confidence = 100
			plan.FieldProvenance[index].Evidence = []string{"user:override"}
			return
		}
	}
}

func digestOf(value string) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value[0]
	}
	return string(result)
}
