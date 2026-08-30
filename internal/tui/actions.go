package tui

import (
	"strings"

	"github.com/hostd/hostd/internal/apicontract"
)

type actionKind uint8

const (
	actionViewCurrent actionKind = iota
	actionViewLast
	actionDeploy
	actionStart
	actionStop
	actionRestart
	actionOpenDashboard
	actionBack
	actionCancelJob
	actionLogout
	actionQuit
)

type actionItem struct {
	Kind       actionKind
	Label      string
	Detail     string
	Enabled    bool
	DisabledBy string
}

type confirmation struct {
	Action       actionKind
	App          apicontract.Application
	Job          apicontract.Job
	ReturnScreen screen
}

type mutationRequest struct {
	Action         actionKind
	AppID          string
	JobID          string
	IdempotencyKey string
	Epoch          uint64
	RequestID      uint64
}

// actionsFor is capability-authoritative: deploy is supported by either
// configured runtime, while start/stop/restart are controller-supported only
// by the fake runtime today.
func actionsFor(app apicontract.Application, job *apicontract.Job, status apicontract.SystemStatus) []actionItem {
	items := make([]actionItem, 0, 9)
	if job != nil {
		kind, label := actionViewLast, "View last operation"
		if isActiveJob(*job) {
			cancelReason := ""
			if isCancellationPending(*job) {
				cancelReason = "cancellation already requested"
			}
			return []actionItem{
				{Kind: actionViewCurrent, Label: "View current operation", Detail: jobSummary(*job), Enabled: true},
				{Kind: actionCancelJob, Label: "Cancel current operation", Detail: "requires confirmation", Enabled: cancelReason == "", DisabledBy: cancelReason},
				{Kind: actionOpenDashboard, Label: "Open in web dashboard", Enabled: true},
				{Kind: actionBack, Label: "Back", Enabled: true},
			}
		}
		items = append(items, actionItem{Kind: kind, Label: label, Detail: jobSummary(*job), Enabled: true})
	}
	deployReady := status.Capabilities.FakeRuntime || status.Capabilities.ComposeRuntime
	lifecycleReady := status.Capabilities.FakeRuntime
	reason := ""
	if !deployReady {
		reason = "runtime not configured"
	}
	items = append(items, actionItem{Kind: actionDeploy, Label: "Deploy latest", Detail: deployDetail(app), Enabled: reason == "", DisabledBy: reason})

	state := strings.ToLower(strings.TrimSpace(app.Status))
	for _, candidate := range []struct {
		kind    actionKind
		label   string
		allowed bool
	}{
		{actionStart, "Start", state == "stopped"},
		{actionStop, "Stop", state == "running"},
		{actionRestart, "Restart", state == "running" || state == "failed"},
	} {
		reason = ""
		switch {
		case !lifecycleReady:
			reason = "requires fake runtime"
		case !candidate.allowed:
			reason = "unavailable while " + statusWord(app.Status)
		}
		items = append(items, actionItem{Kind: candidate.kind, Label: candidate.label, Enabled: reason == "", DisabledBy: reason})
	}
	return append(items,
		actionItem{Kind: actionOpenDashboard, Label: "Open in web dashboard", Enabled: true},
		actionItem{Kind: actionBack, Label: "Back", Enabled: true},
	)
}

func deployDetail(app apicontract.Application) string {
	if app.Source.TrackedBranch != "" && app.Source.ResolvedSha != "" {
		return sanitizeIdentity(app.Source.TrackedBranch, 256) + " @ " + shortRevision(app.Source.ResolvedSha)
	}
	return sanitizeIdentity(app.Source.TrackedBranch, 256)
}

func isMutationAction(action actionKind) bool {
	return action == actionDeploy || action == actionStart || action == actionStop || action == actionRestart || action == actionCancelJob
}

func actionVerb(action actionKind) string {
	switch action {
	case actionDeploy:
		return "Deploy"
	case actionStart:
		return "Start"
	case actionStop:
		return "Stop"
	case actionRestart:
		return "Restart"
	case actionCancelJob:
		return "Cancel job"
	case actionLogout:
		return "Logout"
	case actionQuit:
		return "Quit"
	default:
		return "Continue"
	}
}
