package tui

import (
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hostd/hostd/internal/apicontract"
)

var (
	colorAccent   = lipgloss.AdaptiveColor{Light: "#005FAF", Dark: "#7DD3FC"}
	colorMuted    = lipgloss.AdaptiveColor{Light: "#52606D", Dark: "#A0AEC0"}
	colorBad      = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#FDA4AF"}
	colorGood     = lipgloss.AdaptiveColor{Light: "#137333", Dark: "#86EFAC"}
	colorPanel    = lipgloss.AdaptiveColor{Light: "#94A3B8", Dark: "#475569"}
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	mutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorBad)
	goodStyle     = lipgloss.NewStyle().Foreground(colorGood)
	selectedStyle = lipgloss.NewStyle().Bold(true)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPanel).Padding(0, 1)
)

func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "Rig application switchboard\n"
	}
	if m.layout.unsupported {
		return m.finishView(m.centered("Rig\n\nTerminal too small\nResize to at least 32×8\n\nCtrl+C quits"))
	}
	var view string
	switch m.screen {
	case screenLoading:
		view = m.centered("Rig application switchboard\n\nConnecting to " + endpointLabel(m.endpoint) + "…")
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
	return m.centered(strings.Join([]string{titleStyle.Render("Controller unavailable"), "", "Rig could not reach " + endpointLabel(m.endpoint) + ".", errorStyle.Render(cropWidth(sanitizeAPIText(m.err), max(1, m.width-4))), "", "Start the controller with: hostd serve", "", "Enter Retry   q Quit   Ctrl+C Exit"}, "\n"))
}

func (m *Model) authView(title, subtitle string) string {
	m.resizeAuthInputs()
	if m.compactAuthLayout() {
		lines := []string{titleStyle.Render(title)}
		for i := range m.authInputs {
			lines = append(lines, cropWidth(authFieldLabel(m.screen, i)+": "+m.authInputs[i].View(), m.width))
		}
		if m.err != "" {
			lines = append(lines, errorStyle.Render(cropWidth(m.err, m.width)))
		}
		if m.mutationBusy {
			lines = append(lines, "Authenticating…")
		}
		lines = append(lines, mutedStyle.Render("Tab fields · Enter continues · Ctrl+C quits"))
		return strings.Join(lines, "\n")
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(title) + "\n" + mutedStyle.Render(subtitle) + "\n\n")
	for i := range m.authInputs {
		b.WriteString(authFieldLabel(m.screen, i) + ":\n" + m.authInputs[i].View() + "\n")
	}
	if m.err != "" {
		b.WriteString("\n" + errorStyle.Render(m.err))
	}
	if m.mutationBusy {
		b.WriteString("\nAuthenticating…")
	}
	b.WriteString("\n\nTab/Shift+Tab change field · Enter continues · Ctrl+C quits")
	return m.centered(panelStyle.Width(min(max(1, m.width-6), 68)).Render(b.String()))
}
func (m *Model) bootstrapConfirmationView() string {
	return m.centered(strings.Join([]string{titleStyle.Render("Confirm administrator creation"), "", "Create administrator " + sanitizeAPIText(m.bootstrapUsername) + "?", "", "Enter Create administrator   Esc Cancel"}, "\n"))
}

func (m *Model) header() string {
	runtime := "Not ready"
	if m.status.Capabilities.FakeRuntime || m.status.Capabilities.ComposeRuntime {
		runtime = "Ready"
	}
	refresh := ""
	if m.overviewLoading {
		refresh = " · Refreshing"
	}
	return cropWidth(titleStyle.Render("Rig")+"  "+goodStyle.Render("● Connected")+refresh+"  "+mutedStyle.Render("Runtime: "+runtime), m.width)
}

func (m *Model) switchboardView() string {
	if !m.overviewLoaded {
		return strings.Join([]string{m.header(), "", "Loading applications…", m.footer("r Refresh   ? Help   q Quit")}, "\n")
	}
	if len(m.apps) == 0 {
		lines := []string{m.header(), "", titleStyle.Render("No applications yet"), "", "Create or import an application in the Rig web dashboard,", "then return here and refresh.", "", "o Open Web   r Refresh   ? Help   l Logout   q Quit"}
		if m.err != "" {
			lines = append(lines, errorStyle.Render(m.err))
		}
		return strings.Join(lines, "\n")
	}
	if m.layout.wide {
		return m.wideSwitchboardView()
	}
	lines := []string{m.header()}
	if m.err != "" {
		lines = append(lines, errorStyle.Render(cropWidth(m.err, m.width)))
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
	lines = append(lines, m.footer(m.switchboardFooter()))
	return strings.Join(lines, "\n")
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
		lines = append(lines, errorStyle.Render(cropWidth(m.err, m.width)))
	} else {
		lines = append(lines, "")
	}
	lines = append(lines, body, m.footer(m.switchboardFooter()))
	return strings.Join(lines, "\n")
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
		lines = append(lines, "Branch     "+sanitizeAPIText(app.Source.TrackedBranch))
	}
	if app.Source.ResolvedSha != "" {
		lines = append(lines, "Revision   "+shortRevision(app.Source.ResolvedSha))
	}
	if app.MachineName != "" {
		lines = append(lines, "Machine    "+sanitizeAPIText(app.MachineName))
	}
	if job := relevantJob(app.ID, m.jobs); job != nil {
		lines = append(lines, "Operation  "+jobSummary(*job))
		if job.ErrorDetail != "" {
			lines = append(lines, "Error      "+cropWidth(sanitizeAPIText(job.ErrorDetail), max(8, m.width-11)))
		}
	}
	return lines
}
func (m *Model) switchboardFooter() string {
	if m.layout.compact {
		return "Enter Actions · r Refresh · ? Help · q Quit"
	}
	return "Enter Actions   r Refresh   o Open Web   ? Help   l Logout   q Quit"
}

func (m *Model) actionsView() string {
	app, ok := m.selectedApp()
	if !ok {
		return "No application selected\n\nEsc Back"
	}
	job := relevantJob(app.ID, m.jobs)
	items := actionsFor(app, job, m.status)
	lines := []string{titleStyle.Render("Actions — "+displayAppName(app)) + "  " + appStateLabel(app.Status, m.accessible), ""}
	for i, item := range items {
		marker := "  "
		if i == m.selectedAction {
			marker = "> "
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
	if m.err != "" {
		lines = append(lines, "", errorStyle.Render(cropWidth(m.err, m.width)))
	}
	lines = append(lines, "", "↑↓ Select   Enter Choose   Esc Back")
	return strings.Join(lines, "\n")
}

func (m *Model) confirmationView() string {
	if m.confirmation == nil {
		return "Confirmation unavailable\n\nEsc Back"
	}
	c := m.confirmation
	title := actionVerb(c.Action) + "?"
	if c.Action == actionQuit {
		title = "Quit Rig?"
	} else if c.Action == actionLogout {
		title = "Sign out of Rig?"
	} else if c.Action == actionCancelJob {
		title = "Cancel " + sanitizeAPIText(c.Job.Type) + " for " + displayAppName(c.App) + "?"
	} else {
		title = actionVerb(c.Action) + " " + displayAppName(c.App) + "?"
	}
	lines := []string{titleStyle.Render(title), ""}
	if c.App.ID != "" {
		lines = append(lines, "Application  "+displayAppName(c.App), "Current      "+statusWord(c.App.Status))
	}
	if c.Action == actionDeploy {
		if source := sourceSummary(c.App.Source); source != "" {
			lines = append(lines, "Source       "+source)
		}
		if c.App.Source.TrackedBranch != "" {
			lines = append(lines, "Branch       "+sanitizeAPIText(c.App.Source.TrackedBranch))
		}
		if c.App.Source.ResolvedSha != "" {
			lines = append(lines, "Revision     "+shortRevision(c.App.Source.ResolvedSha))
		}
	}
	if c.Action == actionCancelJob {
		lines = append(lines, "", "The running server job will be asked to stop.", "Returning with Escape does not cancel it.")
	}
	if m.err != "" {
		lines = append(lines, "", errorStyle.Render(cropWidth(m.err, m.width)))
	}
	label := actionVerb(c.Action)
	if m.mutationBusy {
		label = "Working…"
	}
	lines = append(lines, "", "Enter "+label+"   Esc Cancel")
	return m.centered(strings.Join(lines, "\n"))
}

func (m *Model) progressView() string {
	job := m.followedJob
	app := m.appByID(job.ResourceID)
	title := titleWord(job.Type) + "ing " + displayAppName(app)
	if strings.EqualFold(job.Type, "deploy") {
		title = "Deploying " + displayAppName(app)
	}
	lines := []string{titleStyle.Render(title) + "  " + percent(job.Progress), "", progressBar(job.Progress, max(10, min(50, m.width-2))), "", "Status: " + statusWord(job.Status) + " · " + strconv.Itoa(job.Progress) + " percent"}
	for _, phase := range m.phases {
		mark := "●"
		if phase.Completed {
			mark = "✓"
		}
		if m.accessible {
			if phase.Completed {
				mark = "completed"
			} else {
				mark = "current"
			}
		}
		lines = append(lines, mark+" "+sanitizeAPIText(phase.Name))
	}
	if len(m.phases) == 0 && job.Phase != "" {
		lines = append(lines, "Current phase: "+sanitizeAPIText(job.Phase))
	}
	if len(m.recentEvents) > 0 {
		event := m.recentEvents[len(m.recentEvents)-1]
		lines = append(lines, "", cropWidth(sanitizeAPIText(event.Message), m.width))
	}
	if m.err != "" {
		lines = append(lines, errorStyle.Render(cropWidth(m.err, m.width)))
	}
	lines = append(lines, "", m.footer("c Cancel job   Esc Back — operation continues   o Open Web"))
	return strings.Join(lines, "\n")
}
func (m *Model) resultView() string {
	if m.result == nil {
		return "Operation result unavailable\n\nEnter Return to Applications"
	}
	lines := []string{titleStyle.Render(m.result.Title), "", cropWidth(sanitizeAPIText(m.result.Detail), m.width), ""}
	if m.err != "" {
		lines = append(lines, errorStyle.Render(cropWidth(m.err, m.width)), "")
	}
	lines = append(lines, "Enter Return to Applications   o Open Web   q Quit")
	return m.centered(strings.Join(lines, "\n"))
}
func (m *Model) helpView() string {
	return strings.Join([]string{titleStyle.Render("Rig Switchboard Help"), "", "↑↓ / j k     Select an application or action", "PgUp/PgDn    Move one application page", "Home / End   First or last application", "Enter        Open, choose, or confirm", "Esc          Go back; never cancels a server job", "r            Refresh applications", "o            Open web dashboard", "c            Cancel job (progress screen, with confirmation)", "l            Logout", "q            Quit", "Ctrl+C       Exit locally", "", "Configuration, approvals, logs, releases, and relay management remain in the web dashboard.", "", "Esc Back"}, "\n")
}

func (m *Model) footer(value string) string { return cropWidth(value, m.width) }
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
		return sanitizeAPIText(app.Name)
	}
	if strings.TrimSpace(app.Slug) != "" {
		return sanitizeAPIText(app.Slug)
	}
	return sanitizeAPIText(app.ID)
}
func appStateLabel(raw string, accessible bool) string {
	word := statusWord(raw)
	if accessible {
		return word
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
	value := strings.ToLower(strings.TrimSpace(sanitizeAPIText(raw)))
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
	switch strings.ToLower(source.Type) {
	case "github":
		repo := strings.Trim(strings.TrimSpace(source.RepositoryOwner)+"/"+strings.TrimSpace(source.RepositoryName), "/")
		if repo == "" {
			repo = "GitHub"
		}
		return sanitizeAPIText(repo)
	case "local":
		if source.Path != "" {
			return sanitizeAPIText(source.Path)
		}
		return "Local source"
	default:
		return sanitizeAPIText(source.Type)
	}
}
func shortRevision(value string) string {
	value = sanitizeAPIText(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
func titleWord(value string) string {
	value = strings.TrimSpace(sanitizeAPIText(value))
	if value == "" {
		return "Operation"
	}
	return strings.ToUpper(value[:1]) + value[1:]
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
		return sanitizeAPIText(parsed.Scheme + "://" + parsed.Host)
	}
	return sanitizeAPIText(endpoint)
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
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = cropWidth(lines[i], m.width)
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}
