package deploymentplans

import "github.com/hostd/hostd/internal/projectanalysis"

// SetupFromPlan reconstructs only explicitly accepted configuration. Legacy
// plans remain in their original representation and retain their exact digest.
func SetupFromPlan(plan Plan) (projectanalysis.DeploymentSetup, bool) {
	if plan.SetupVersion != projectanalysis.SetupVersion {
		return projectanalysis.DeploymentSetup{}, false
	}
	setup := projectanalysis.DeploymentSetup{Components: make([]projectanalysis.SetupComponent, 0, len(plan.Components))}
	for _, c := range plan.Components {
		start := c.RunCommand
		if c.Technology == "static" {
			start = ""
		}
		setup.Components = append(setup.Components, projectanalysis.SetupComponent{
			ID: c.Name, Technology: c.Technology, RootDirectory: c.RootDirectory,
			PackageManager: c.PackageManager, NodeVersion: c.NodeVersion,
			InstallCommand: c.InstallBehavior, BuildCommand: c.BuildCommand, StartCommand: start,
			OutputDirectory: c.StaticOutputDirectory, InternalPort: int(c.InternalPort), HealthProbe: c.HealthProbe,
		})
	}
	if plan.Migration != nil {
		setup.MigrationCommand = plan.Migration.Command
	}
	return setup, true
}

func AcceptSetup(analysis projectanalysis.SourceAnalysis, input projectanalysis.DeploymentSetup, source SourceIdentity) (Plan, projectanalysis.DeploymentPlanCandidate, error) {
	candidate, setup, err := projectanalysis.PrepareSetup(analysis, input)
	if err != nil {
		return Plan{}, candidate, err
	}
	plan := Plan{SetupVersion: projectanalysis.SetupVersion, Strategy: StrategyGeneratedNode, Source: source,
		Detector: Detector{Name: "manual-setup", Version: "1", SourceStructuralFingerprint: analysis.StructuralFingerprint}}
	for _, c := range setup.Components {
		var analyzed projectanalysis.Component
		for _, item := range candidate.Components {
			if item.ID == c.ID {
				analyzed = item
				break
			}
		}
		component := Component{Name: c.ID, Role: analyzed.Kind, RootDirectory: c.RootDirectory,
			Technology: c.Technology, StaticOutputDirectory: c.OutputDirectory,
			PackageManager: c.PackageManager, InstallBehavior: c.InstallCommand, InstallDirectory: c.RootDirectory, NodeVersion: c.NodeVersion,
			BuildCommand: c.BuildCommand, RunCommand: analyzed.Run.Command, InternalPort: uint16(c.InternalPort), HealthProbe: c.HealthProbe}
		plan.Components = append(plan.Components, component)
		for _, field := range componentExecutionFields(component) {
			plan.FieldProvenance = append(plan.FieldProvenance, FieldProvenance{Field: field, Origin: ProvenanceUser, Confidence: 100, Evidence: []string{"user:deployment-setup"}})
		}
		if analyzed.Migration != nil {
			plan.Migration = &Migration{ComponentName: c.ID, RootDirectory: c.RootDirectory, Command: analyzed.Migration.Command,
				EnvironmentKeys: append([]string(nil), analyzed.Migration.EnvironmentKeys...), EvidenceDigest: analyzed.MigrationFingerprint,
				Approval: MigrationApproval{Status: MigrationApprovalPending}}
		}
	}
	canonical, err := canonicalPlan(plan)
	return canonical, candidate, err
}
