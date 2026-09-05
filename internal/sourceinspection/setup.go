package sourceinspection

import "github.com/hostd/hostd/internal/projectanalysis"

// WithSetup adds an explicitly configured candidate to the already inspected
// snapshot. Compose discovery is irrelevant to this requested strategy; source
// findings such as unsupported submodules remain blocking.
func WithSetup(result Result, setup projectanalysis.DeploymentSetup) (Result, error) {
	candidate, _, err := projectanalysis.PrepareSetup(result.Analysis, setup)
	if err != nil {
		return Result{}, err
	}
	for _, finding := range result.Findings {
		switch finding.Code {
		case "compose_not_found", "compose_selection_required", "malformed_compose", "missing_services", "malformed_service", "unsupported_compose_include", "unsupported_remote_resource", "workspace_escape":
			// The source's Compose document will neither build nor run this plan.
		default:
			return Result{}, &Error{Code: "invalid_source"}
		}
	}
	result.Analysis.Candidates = append(result.Analysis.Candidates, candidate)
	result.Source.ComposePath = ""
	result.Services, result.Findings = nil, nil
	return result, nil
}
