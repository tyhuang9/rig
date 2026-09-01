package controller

import (
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func contractSourceAnalysis(inspection sourceinspection.Result) apicontract.SourceAnalysis {
	value := inspection.Analysis
	resolvedDigest := inspection.ResolvedSHA
	if resolvedDigest == "" {
		resolvedDigest = value.StructuralFingerprint
	}
	result := apicontract.SourceAnalysis{
		Source: contractSourceSummary(inspection.Source, resolvedDigest), ResolvedDigest: resolvedDigest,
		SchemaVersion: value.SchemaVersion, StructuralFingerprint: value.StructuralFingerprint,
		Candidates: make([]apicontract.DeploymentPlanCandidate, 0, len(value.Candidates)),
		Findings:   contractAnalysisFindings(value.Findings),
	}
	for _, candidate := range value.Candidates {
		contract := apicontract.DeploymentPlanCandidate{
			ID: candidate.ID, Origin: candidate.Origin, Status: candidate.Status, Kind: candidate.Kind,
			RootDirectory: candidate.RootDirectory, ConfigPath: candidate.ConfigPath, Digest: candidate.Digest,
			PackageManager: apicontract.AnalysisPackageManager{
				Present: candidate.PackageManager.Name != "",
				Origin:  candidate.PackageManager.Origin, Name: candidate.PackageManager.Name, Version: candidate.PackageManager.Version,
				Lockfile: candidate.PackageManager.Lockfile, Provenance: candidate.PackageManager.Provenance,
				Confidence: candidate.PackageManager.Confidence, Evidence: contractAnalysisEvidence(candidate.PackageManager.Evidence),
			},
			NodeVersion:    contractAnalysisValue(candidate.NodeVersion),
			Install:        contractAnalysisCommand(candidate.Install),
			Components:     make([]apicontract.AnalysisComponent, 0, len(candidate.Components)),
			Evidence:       contractAnalysisEvidence(candidate.Evidence),
			Findings:       contractAnalysisFindings(candidate.Findings),
			MissingFields:  append([]string(nil), candidate.MissingFields...),
			AdvancedInputs: make([]apicontract.AnalysisAdvancedInput, 0, len(candidate.AdvancedInputs)),
		}
		for _, advanced := range candidate.AdvancedInputs {
			contract.AdvancedInputs = append(contract.AdvancedInputs, apicontract.AnalysisAdvancedInput{Field: advanced.Field, ComponentID: advanced.ComponentID, Required: advanced.Required, Reason: advanced.Reason})
		}
		for _, component := range candidate.Components {
			item := apicontract.AnalysisComponent{
				ID: component.ID, Origin: component.Origin, Name: component.Name, Kind: component.Kind, Framework: component.Framework,
				RootDirectory: component.RootDirectory, StaticOutputDirectory: component.StaticOutputDirectory,
				MigrationFingerprint: component.MigrationFingerprint, Build: contractAnalysisCommand(component.Build), Run: contractAnalysisCommand(component.Run),
				Migration: contractAnalysisCommand(component.Migration), Evidence: contractAnalysisEvidence(component.Evidence), Findings: contractAnalysisFindings(component.Findings),
			}
			if component.InternalPort != nil {
				item.InternalPort = contractAnalysisValue(*component.InternalPort)
			}
			if component.HealthProbe != nil {
				item.HealthProbe = apicontract.AnalysisHealthProbe{Present: true, Origin: component.HealthProbe.Origin, Path: component.HealthProbe.Path, Method: component.HealthProbe.Method, Provenance: component.HealthProbe.Provenance, Confidence: component.HealthProbe.Confidence, Evidence: contractAnalysisEvidence(component.HealthProbe.Evidence)}
			}
			contract.Components = append(contract.Components, item)
		}
		result.Candidates = append(result.Candidates, contract)
	}
	return result
}

func contractAnalysisCommand(value *projectanalysis.Command) apicontract.AnalysisCommand {
	if value == nil {
		return apicontract.AnalysisCommand{Present: false, Evidence: []apicontract.AnalysisEvidence{}}
	}
	return apicontract.AnalysisCommand{Present: true, Origin: value.Origin, Phase: value.Phase, Command: value.Command, WorkingDirectory: value.WorkingDirectory, EnvironmentKeys: append([]string(nil), value.EnvironmentKeys...), Provenance: value.Provenance, Confidence: value.Confidence, Evidence: contractAnalysisEvidence(value.Evidence)}
}

func contractSourceSummary(value sourceinspection.SourceMetadata, resolvedDigest string) apicontract.SourceSummary {
	return apicontract.SourceSummary{
		Type: value.Type, Path: value.Path, ConnectionID: value.ConnectionID, InstallationID: value.InstallationID,
		RepositoryID: value.RepositoryID, RepositoryOwner: value.RepositoryOwner, RepositoryName: value.RepositoryName,
		TrackedBranch: value.TrackedBranch, TrackedRef: value.TrackedRef, ComposePath: value.ComposePath, ResolvedSha: resolvedDigest,
	}
}

func contractAnalysisValue(value projectanalysis.InferredValue) apicontract.AnalysisValue {
	return apicontract.AnalysisValue{Present: value.Value != "", Origin: value.Origin, Value: value.Value, Provenance: value.Provenance, Confidence: value.Confidence, Evidence: contractAnalysisEvidence(value.Evidence)}
}

func contractAnalysisEvidence(values []projectanalysis.Evidence) []apicontract.AnalysisEvidence {
	result := make([]apicontract.AnalysisEvidence, 0, len(values))
	for _, value := range values {
		result = append(result, apicontract.AnalysisEvidence{Code: value.Code, Path: value.Path, Field: value.Field, Detail: value.Detail})
	}
	return result
}

func contractAnalysisFindings(values []projectanalysis.Finding) []apicontract.AnalysisFinding {
	result := make([]apicontract.AnalysisFinding, 0, len(values))
	for _, value := range values {
		result = append(result, apicontract.AnalysisFinding{Code: value.Code, Severity: value.Severity, Message: value.Message, Path: value.Path, Field: value.Field})
	}
	return result
}
