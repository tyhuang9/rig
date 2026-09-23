package autodeploy

import "context"

const (
	PreflightHeadChanged = "head_changed"
	PreflightPlanReview  = "deployment_plan_review_required"
)

// DispatchPreflightRequest binds repository analysis and materialization to
// the exact durable dispatch candidate. It contains no command or secret data.
type DispatchPreflightRequest struct {
	ApplicationID string
	OwnerUserID   string
	Source        SourceScope
	ResolvedSHA   string
}

// DispatchPreflightResult is empty for Compose. Generated deployments return
// one immutable, revalidated release ID that is persisted in the job input.
type DispatchPreflightResult struct {
	ReleaseID string
}

// DispatchPreflight performs non-executing source analysis and immutable
// release preparation before any deployment job is created.
type DispatchPreflight interface {
	Prepare(context.Context, DispatchPreflightRequest) (DispatchPreflightResult, error)
}

// PreflightError exposes only a stable coordinator decision. Repository paths,
// inferred values, commands, and provider output must never be attached.
type PreflightError struct{ Code string }

func (err *PreflightError) Error() string {
	if err == nil {
		return ""
	}
	return err.Code
}
