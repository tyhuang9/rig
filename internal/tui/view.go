package tui

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hostd/hostd/internal/apicontract"
)

var (
	colorAccent   = lipgloss.AdaptiveColor{Light: "#005FAF", Dark: "#7DD3FC"}
	colorMuted    = lipgloss.AdaptiveColor{Light: "#52606D", Dark: "#A0AEC0"}
	colorBad      = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#FDA4AF"}
	colorWarning  = lipgloss.AdaptiveColor{Light: "#8A4B00", Dark: "#FCD34D"}
	colorGood     = lipgloss.AdaptiveColor{Light: "#137333", Dark: "#86EFAC"}
	colorPanel    = lipgloss.AdaptiveColor{Light: "#94A3B8", Dark: "#475569"}
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	mutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorBad)
	warningStyle  = lipgloss.NewStyle().Foreground(colorWarning)
	goodStyle     = lipgloss.NewStyle().Foreground(colorGood)
	selectedStyle = lipgloss.NewStyle().Bold(true)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPanel).Padding(0, 1)
)

func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "Rig application switchboard\n"
	}
	if m.layout.unsupported {
		return m.finishView(m.centered("Rig\n\nTerminal too small\nResize to at least 32x8\n\nCtrl+C quits"))
	}
	var view string
	switch m.screen {
	case screenLoading:
		view = m.centeredScreen([]string{"Rig application switchboard", "Connecting to " + endpointLabel(m.endpoint) + "..."}, "Ctrl+C Quit")
	case screenOffline:
		view = m.offlineView()
	case screenBootstrap:
		if m.bootstrapConfirm {
			view = m.bootstrapConfirmationView()
		} else {
			view = m.authView("Create the first administrator", "Use the protected one-time bootstrap token.")
		}
	case screenLogin:
		view = m.authView("Sign in to Rig", "Credentials stay in memory only.")
	case screenSwitchboard:
		view = m.switchboardView()
	case screenActions:
		view = m.actionsView()
	case screenConfirmation:
		view = m.confirmationView()
	case screenJobProgress:
		view = m.progressView()
	case screenResult:
		view = m.resultView()
	case screenHelp:
		view = m.helpView()
	}
	return m.finishView(view)
}

func (m *Model) centered(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}
func (m *Model) offlineView() string {
	body := []string{titleStyle.Render("Controller unavailable"), "Rig could not reach " + endpointLabel(m.endpoint) + ".", m.bannerLine(max(1, m.width-4)), "Start the controller with: hostd serve"}
	return m.centeredScreen(nonEmptyLines(body), "Enter Retry | q Quit | Ctrl+C Exit")
}

func (m *Model) authView(title, subtitle string) string {
	m.resizeAuthInputs()
	lines := []string{titleStyle.Render(title)}
	if !m.layout.compact {
		lines = append(lines, mutedStyle.Render(subtitle))
	}
	for i := range m.authInputs {
		marker := "  "
		if i == m.authIndex {
			marker = "> "
			if m.accessible {
				marker = "Current field: "
			}
		}
		lines = append(lines, cropWidth(marker+authFieldLabel(m.screen, i)+": "+m.authInputs[i].View(), m.width))
	}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	}
	if m.mutationBusy {
		lines = append(lines, "Authenticating...")
	}
	return m.screenLines(lines, "Tab Fields | Enter | Ctrl+C Quit")
}
func (m *Model) bootstrapConfirmationView() string {
	return m.centeredScreen([]string{titleStyle.Render("Confirm administrator creation"), "Create administrator " + sanitizeIdentity(m.bootstrapUsername, 512) + "?"}, "Enter Confirm | Esc Cancel")
}

func (m *Model) header() string {
	runtime := runtimeState(m.status).Label
	refresh := ""
	if m.overviewLoading {
		refresh = " · Refreshing"
	}
	return cropWidth(titleStyle.Render("Rig")+"  "+goodStyle.Render("● Connected")+refresh+"  "+mutedStyle.Render("Runtime: "+runtime), m.width)
}

func (m *Model) switchboardView() string {
	if !m.overviewLoaded {
		return m.screenLines([]string{m.header(), "Loading applications..."}, "r Refresh | ? Help | q Quit")
	}
	if len(m.apps) == 0 {
		lines := []string{m.header(), titleStyle.Render("No applications yet"), "Create or import an application in the web dashboard,", "then return here and refresh."}
		if m.err != "" {
			lines = append(lines, m.bannerLine(m.width))
		}
		return m.screenLines(lines, m.switchboardFooter())
	}
	if m.layout.wide {
		return m.wideSwitchboardView()
	}
	lines := []string{m.header()}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	} else {
		lines = append(lines, "")
	}
	if !m.layout.compact {
		lines = append(lines, titleStyle.Render("Applications"))
	}
	lines = append(lines, m.applicationRows(m.width, m.layout.listRows)...)
	if app, ok := m.selectedApp(); ok {
		if m.layout.compact {
			lines = append(lines, m.compactAppSummary(app))
		} else {
			lines = append(lines, "")
			lines = append(lines, m.appDetails(app)...)
		}
	}
	return m.screenLines(lines, m.switchboardFooter())
}
func (m *Model) wideSwitchboardView() string {
	leftLines := append([]string{titleStyle.Render("Applications")}, m.applicationRows(max(1, m.layout.leftWidth-4), m.layout.listRows)...)
	rightLines := []string{"No application selected"}
	if app, ok := m.selectedApp(); ok {
		rightLines = append([]string{titleStyle.Render(displayAppName(app))}, m.appDetails(app)...)
	}
	panelHeight := max(3, m.height-5)
	left := panelStyle.Width(max(1, m.layout.leftWidth-4)).Height(panelHeight - 2).Render(strings.Join(leftLines, "\n"))
	right := panelStyle.Width(max(1, m.layout.rightWidth-4)).Height(panelHeight - 2).Render(strings.Join(rightLines, "\n"))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	lines := []string{m.header()}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	} else {
		lines = append(lines, "")
	}
	lines = append(lines, body)
	return m.screenLines(lines, m.switchboardFooter())
}
func (m *Model) applicationRows(width, capacity int) []string {
	end := min(len(m.apps), m.listOffset+max(1, capacity))
	rows := make([]string, 0, end-m.listOffset)
	for i := m.listOffset; i < end; i++ {
		app := m.apps[i]
		selected := app.ID == m.selectedAppID
		marker := "  "
		if selected {
			marker = "> "
			if m.accessible {
				marker = "Current: "
			}
		}
		state := appStateLabel(app.Status, m.accessible)
		job := relevantJob(app.ID, m.jobs)
		suffix := ""
		if job != nil {
			suffix = "  " + jobSummary(*job)
		}
		available := max(4, width-ansi.StringWidth(marker)-ansi.StringWidth(state)-ansi.StringWidth(suffix)-2)
		name := cropWidth(displayAppName(app), available)
		gap := max(1, width-ansi.StringWidth(marker+name+state+suffix))
		line := marker + name + strings.Repeat(" ", gap) + state + suffix
		if selected {
			line = selectedStyle.Render(line)
		}
		rows = append(rows, cropWidth(line, width))
	}
	return rows
}
func (m *Model) compactAppSummary(app apicontract.Application) string {
	source := sourceSummary(app.Source)
	summary := displayAppName(app)
	if source != "" {
		summary += " · " + source
	}
	if job := relevantJob(app.ID, m.jobs); job != nil {
		summary += " · " + jobSummary(*job)
	}
	return cropWidth(summary, m.width)
}
func (m *Model) appDetails(app apicontract.Application) []string {
	lines := []string{displayAppName(app) + " · " + appStateLabel(app.Status, m.accessible)}
	if source := sourceSummary(app.Source); source != "" {
		lines = append(lines, "Source     "+source)
	}
	if app.Source.TrackedBranch != "" {
		lines = append(lines, "Branch     "+sanitizeIdentity(app.Source.TrackedBranch, 256))
	}
	if app.Source.ResolvedSha != "" {
		lines = append(lines, "Revision   "+shortRevision(app.Source.ResolvedSha))
	}
	if app.MachineName != "" {
		lines = append(lines, "Machine    "+sanitizeIdentity(app.MachineName, 256))
	}
	if job := relevantJob(app.ID, m.jobs); job != nil {
		lines = append(lines, "Operation  "+jobSummary(*job))
		if job.ErrorDetail != "" {
			lines = append(lines, "Error      "+cropWidth(sanitizeIdentity(job.ErrorDetail, maxAPITextBytes), max(8, m.width-11)))
		}
	}
	return lines
}
func (m *Model) switchboardFooter() string {
	if m.layout.compact {
		return "Enter | r Refresh | q Quit"
	}
	return "Enter Actions | r Refresh | o Open Web | ? Help | l Logout | q Quit"
}

func (m *Model) actionsView() string {
	app, items, ok := m.currentActions()
	if !ok {
		return m.screenLines([]string{"No application selected"}, "Esc Back | q Quit")
	}
	m.reconcileActionSelection(len(items))
	lines := []string{cropWidth(titleStyle.Render("Actions — "+displayAppName(app))+"  "+appStateLabel(app.Status, m.accessible), m.width)}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	}
	end := min(len(items), m.actionOffset+m.actionCapacity())
	for i := m.actionOffset; i < end; i++ {
		item := items[i]
		marker := "  "
		if i == m.selectedAction {
			marker = "> "
			if m.accessible {
				marker = "Current: "
			}
		}
		detail := item.Detail
		if !item.Enabled {
			detail = item.DisabledBy
		}
		line := marker + item.Label
		if detail != "" {
			line += "  " + mutedStyle.Render(detail)
		}
		if i == m.selectedAction {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, cropWidth(line, m.width))
	}
	footer := "Up/Down Select | Enter | Esc Back | q Quit"
	if m.width < 45 {
		footer = "Enter | Esc Back | q Quit"
	}
	return m.screenLines(lines, footer)
}

func (m *Model) confirmationView() string {
	if m.confirmation == nil {
		return m.screenLines([]string{"Confirmation unavailable"}, "Esc Back | q Quit")
	}
	c := m.confirmation
	title := actionVerb(c.Action) + "?"
	if c.Action == actionQuit {
		title = "Quit Rig?"
	} else if c.Action == actionLogout {
		title = "Sign out of Rig?"
	} else if c.Action == actionCancelJob {
		title = "Cancel " + sanitizeIdentity(c.Job.Type, 128) + " for " + displayAppName(c.App) + "?"
	} else {
		title = actionVerb(c.Action) + " " + displayAppName(c.App) + "?"
	}
	lines := []string{titleStyle.Render(title)}
	if c.App.ID != "" {
		lines = append(lines, "Application  "+displayAppName(c.App), "Current      "+statusWord(c.App.Status))
	}
	if c.Action == actionDeploy {
		if source := sourceSummary(c.App.Source); source != "" {
			lines = append(lines, "Source       "+source)
		}
		if c.App.Source.TrackedBranch != "" {
			lines = append(lines, "Branch       "+sanitizeIdentity(c.App.Source.TrackedBranch, 256))
		}
		if c.App.Source.ResolvedSha != "" {
			lines = append(lines, "Revision     "+shortRevision(c.App.Source.ResolvedSha))
		}
	}
	if c.Action == actionCancelJob {
		lines = append(lines, "Job ID       "+sanitizeIdentity(c.Job.ID, 256), "Requests the server job to stop.", "Esc keeps the server job running.")
	}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	}
	label := actionVerb(c.Action)
	footer := "Enter " + label + " | Esc Cancel"
	if m.mutationBusy {
		footer = "Working... | Ctrl+C Exit"
	}
	return m.centeredScreen(lines, footer)
}

func (m *Model) progressView() string {
	job := m.followedJob
	app := m.appByID(job.ResourceID)
	title := operationGerund(job.Type) + " " + displayAppName(app)
	jobStatus := statusWord(job.Status)
	if isCancellationPending(job) {
		jobStatus = "Cancelling"
	}
	statusLine := "Status: " + jobStatus + " | " + strconv.Itoa(job.Progress) + " percent"
	if updated := relativeUpdatedAt(job.UpdatedAt, m.now()); updated != "" {
		statusLine += " | " + updated
	}
	lines := []string{titleStyle.Render(title) + "  " + percent(job.Progress), statusLine, progressBarForMode(job.Progress, max(10, min(50, m.width-2)), m.accessible)}
	if job.Checkpoint != "" {
		lines = append(lines, "Checkpoint: "+sanitizeIdentity(job.Checkpoint, 512))
	}
	reserve := 0
	if len(m.recentEvents) > 0 {
		reserve++
	}
	if m.err != "" {
		reserve++
	}
	phaseCapacity := max(0, (m.height-1)-len(lines)-reserve)
	phaseLines := m.progressPhaseLines(job)
	if len(phaseLines) > phaseCapacity {
		phaseLines = phaseLines[len(phaseLines)-phaseCapacity:]
	}
	lines = append(lines, phaseLines...)
	if len(m.recentEvents) > 0 {
		event := m.recentEvents[len(m.recentEvents)-1]
		lines = append(lines, "Latest: "+cropWidth(sanitizeIdentity(event.Message, maxAPITextBytes), max(1, m.width-8)))
	}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	}
	footer := "Esc Back | c Cancel | q Quit"
	if isCancellationPending(job) {
		footer = "Esc Back | q Quit | Cancelling"
	} else if !isActiveJob(job) {
		footer = "Esc Back | q Quit | r Refresh"
	}
	if m.width >= 50 {
		if isCancellationPending(job) {
			footer = "Esc Back | Cancellation pending | q Quit"
		}
		footer += " | o Web | r Refresh"
	}
	return m.screenLines(lines, footer)
}
func (m *Model) resultView() string {
	if m.result == nil {
		return m.centeredScreen([]string{"Operation result unavailable"}, "Enter Back | q Quit | r Refresh")
	}
	lines := []string{titleStyle.Render(sanitizeIdentity(m.result.Title, 512)), cropWidth(sanitizeIdentity(m.result.Detail, maxAPITextBytes), m.width)}
	if m.err != "" {
		lines = append(lines, m.bannerLine(m.width))
	}
	footer := "Enter Back | q Quit | r Refresh"
	if m.width >= 50 {
		footer += " | o Web"
	}
	return m.centeredScreen(lines, footer)
}
func (m *Model) helpView() string {
	lines := []string{titleStyle.Render("Rig Switchboard Help"), "Up/Down, j/k  Select", "PgUp/PgDn    Move one page", "Home/End     First or last", "Enter        Open or confirm", "Esc          Back; never cancels jobs", "r            Refresh", "o            Open web dashboard", "c            Confirm job cancellation", "l            Logout", "q            Quit", "Ctrl+C       Exit locally"}
	return m.screenLines(lines, "Esc Back | r Refresh | q Quit")
}

func (m *Model) footer(value string) string { return cropWidth(value, m.width) }

func (m *Model) screenLines(body []string, footer string) string {
	capacity := max(0, m.height-1)
	lines := make([]string, 0, capacity+1)
	for _, line := range body {
		lines = append(lines, strings.Split(line, "\n")...)
	}
	if len(lines) > capacity {
		lines = lines[:capacity]
	}
	for len(lines) < capacity {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, m.footer(footer)), "\n")
}

func (m *Model) centeredScreen(body []string, footer string) string {
	capacity := max(0, m.height-1)
	content := strings.Join(body, "\n")
	placed := lipgloss.Place(m.width, capacity, lipgloss.Center, lipgloss.Center, content)
	return placed + "\n" + m.footer(footer)
}

func nonEmptyLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (m *Model) bannerLine(width int) string {
	value := cropWidth(sanitizeIdentity(m.err, maxAPITextBytes), width)
	switch m.bannerTone {
	case bannerInfo:
		return mutedStyle.Render(value)
	case bannerWarning:
		return warningStyle.Render(value)
	case bannerSuccess:
		return goodStyle.Render(value)
	default:
		return errorStyle.Render(value)
	}
}

func (m *Model) progressPhaseLines(job apicontract.Job) []string {
	lines := make([]string, 0, len(m.phases)+1)
	for _, phase := range m.phases {
		mark := "*"
		if phase.Completed {
			mark = "done"
		} else if m.accessible {
			mark = "current"
		}
		lines = append(lines, mark+" "+sanitizeIdentity(phase.Name, 256))
	}
	if len(lines) == 0 && job.Phase != "" {
		lines = append(lines, "Current phase: "+sanitizeIdentity(job.Phase, 256))
	}
	return lines
}

func relativeUpdatedAt(value string, now time.Time) string {
	updated, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || now.IsZero() {
		return ""
	}
	age := now.Sub(updated)
	if age < time.Minute {
		return "updated just now"
	}
	if age < time.Hour {
		return "updated " + strconv.Itoa(int(age/time.Minute)) + "m ago"
	}
	if age < 24*time.Hour {
		return "updated " + strconv.Itoa(int(age/time.Hour)) + "h ago"
	}
	return "updated " + strconv.Itoa(int(age/(24*time.Hour))) + "d ago"
}

func operationGerund(value string) string {
	switch strings.ToLower(sanitizeIdentity(value, 128)) {
	case "deploy":
		return "Deploying"
	case "start":
		return "Starting"
	case "stop":
		return "Stopping"
	case "restart":
		return "Restarting"
	default:
		return "Working"
	}
}

func progressBarForMode(value, width int, accessible bool) string {
	if !accessible {
		return progressBar(value, width)
	}
	if width < 2 {
		return ""
	}
	filled := max(0, min(width-2, value*(width-2)/100))
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-2-filled) + "]"
}
func progressBar(value, width int) string {
	if width < 1 {
		return ""
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	filled := value * width / 100
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
func displayAppName(app apicontract.Application) string {
	if strings.TrimSpace(app.Name) != "" {
		return sanitizeIdentity(app.Name, 512)
	}
	if strings.TrimSpace(app.Slug) != "" {
		return sanitizeIdentity(app.Slug, 512)
	}
	return sanitizeIdentity(app.ID, 512)
}
func appStateLabel(raw string, accessible bool) string {
	word := statusWord(raw)
	if accessible {
		return "Status: " + word
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "running":
		return "● " + word
	case "stopped", "draft":
		return "○ " + word
	case "failed":
		return "× " + word
	default:
		return "? " + word
	}
}
func statusWord(raw string) string {
	value := strings.ToLower(sanitizeIdentity(raw, 128))
	switch value {
	case "running":
		return "Running"
	case "stopped":
		return "Stopped"
	case "draft":
		return "Draft"
	case "failed":
		return "Failed"
	case "queued":
		return "Queued"
	case "assigned":
		return "Assigned"
	case "waiting_external":
		return "Waiting"
	case "waiting_user":
		return "Needs approval"
	case "succeeded":
		return "Succeeded"
	case "cancelled":
		return "Cancelled"
	case "cancelling":
		return "Cancelling"
	case "interrupted":
		return "Interrupted"
	case "needs_attention":
		return "Needs attention"
	case "":
		return "Unknown"
	default:
		return "Unknown (" + value + ")"
	}
}
func sourceSummary(source apicontract.SourceSummary) string {
	switch strings.ToLower(sanitizeIdentity(source.Type, 128)) {
	case "github":
		owner := sanitizeIdentity(source.RepositoryOwner, 256)
		name := sanitizeIdentity(source.RepositoryName, 256)
		repo := strings.Trim(owner+"/"+name, "/")
		if repo == "" {
			repo = "GitHub"
		}
		return sanitizeIdentity(repo, 512)
	case "local":
		if source.Path != "" {
			return sanitizeIdentity(source.Path, 512)
		}
		return "Local source"
	default:
		return sanitizeIdentity(source.Type, 128)
	}
}
func shortRevision(value string) string {
	value = sanitizeIdentity(value, 256)
	if len(value) > 12 {
		return truncateUTF8Bytes(value, 12)
	}
	return value
}
func titleWord(value string) string {
	value = sanitizeIdentity(value, 128)
	if value == "" {
		return "Operation"
	}
	_, size := utf8.DecodeRuneInString(value)
	return strings.ToUpper(value[:size]) + value[size:]
}
func authFieldLabel(current screen, index int) string {
	if current == screenBootstrap {
		return []string{"Bootstrap token", "Administrator username", "Passphrase"}[index]
	}
	return []string{"Username", "Passphrase"}[index]
}
func endpointLabel(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return sanitizeIdentity(parsed.Scheme+"://"+parsed.Host, 512)
	}
	return sanitizeIdentity(endpoint, 512)
}
func noColor() bool { _, set := os.LookupEnv("NO_COLOR"); return set }
func cropWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}
func (m *Model) finishView(value string) string {
	if m.accessible || noColor() {
		value = ansi.Strip(value)
	}
	if m.accessible {
		value = asciiSafe(value)
	}
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = cropWidth(lines[i], m.width)
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func asciiSafe(value string) string {
	replacer := strings.NewReplacer(
		"…", "...", "—", "-", "–", "-", "·", " | ",
		"●", "*", "○", "o", "×", "x", "✓", "done", "◐", "~",
		"█", "#", "░", "-", "↑", "Up", "↓", "Down", "•", "*",
		"╭", "+", "╮", "+", "╰", "+", "╯", "+", "─", "-", "│", "|",
	)
	return replacer.Replace(value)
}
