package tui

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hostd/hostd/internal/apicontract"
)

var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#005FAF", Dark: "#7DD3FC"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#52606D", Dark: "#A0AEC0"}
	colorBad    = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#FDA4AF"}
	colorPanel  = lipgloss.AdaptiveColor{Light: "#94A3B8", Dark: "#475569"}
	colorSelect = lipgloss.AdaptiveColor{Light: "#DCEEFF", Dark: "#163A5F"}

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	mutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorBad)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPanel).Padding(0, 1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Background(colorSelect)
	buttonStyle   = lipgloss.NewStyle().Bold(true).Underline(true).Padding(0, 1)
	cancelStyle   = lipgloss.NewStyle().Underline(true).Padding(0, 1)
)

func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return "hostd operator console\n"
	}
	if m.width < 32 || m.height < 8 {
		return m.finishView(m.centered("hostd operator console\n\nTerminal too small\nResize to at least 32×8\nCtrl+C quits"))
	}
	if m.accessible {
		return m.finishView(m.accessibleView())
	}
	var view string
	switch m.screen {
	case screenLoading:
		view = m.centered("hostd operator console\n\nConnecting to " + endpointLabel(m.endpoint) + "…")
	case screenOffline:
		view = m.centered(titleStyle.Render("Controller unavailable") + "\n\nEndpoint: " + endpointLabel(m.endpoint) + "\n" + errorStyle.Render(sanitizeAPIText(m.err)) + "\n\nStart the controller with: hostd serve\nThen press Enter or r to retry. Ctrl+C quits.")
	case screenBootstrap:
		if m.bootstrapConfirm {
			view = m.bootstrapConfirmationView()
		} else {
			view = m.authView("Create the first administrator", "Enter the one-time bootstrap token and new administrator credentials.")
		}
	case screenLogin:
		view = m.authView("Sign in to hostd", "Credentials stay in memory only and are never written to command history.")
	case screenConsole:
		view = m.consoleView()
	}
	return m.finishView(view)
}

func (m *Model) centered(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

func (m *Model) authView(title, subtitle string) string {
	m.resizeAuthInputs()
	if m.compactAuthLayout() {
		return m.compactAuthView(title)
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render(subtitle))
	b.WriteString("\n\n")
	for i := range m.authInputs {
		b.WriteString(authFieldLabel(m.screen, i) + ":\n")
		b.WriteString(m.authInputs[i].View())
		b.WriteString("\n")
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render(sanitizeAPIText(m.err)))
	}
	if m.busy {
		b.WriteString("\nAuthenticating…")
	}
	b.WriteString("\n\nTab/Shift+Tab change field · Enter continues · Ctrl+C quits")
	boxWidth := min(max(1, m.width-4), 68)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panelStyle.Width(boxWidth).Render(b.String()))
}

// Compact auth keeps every field on screen at supported terminal sizes. It is
// intentionally linear rather than allowing a centered panel to crop fields
// below the bottom edge of a small terminal.
func (m *Model) compactAuthView(title string) string {
	lines := []string{titleStyle.Render(title)}
	for i := range m.authInputs {
		line := authFieldLabel(m.screen, i) + ": " + m.authInputs[i].View()
		lines = append(lines, cropWidth(line, m.width))
	}
	if m.err != "" {
		lines = append(lines, cropWidth(errorStyle.Render(sanitizeAPIText(m.err)), m.width))
	}
	if m.busy {
		lines = append(lines, "Authenticating…")
	}
	lines = append(lines, mutedStyle.Render("Tab fields · Enter continues · Ctrl+C quits"))
	return cropHeight(strings.Join(lines, "\n"), m.height)
}

func (m *Model) bootstrapConfirmationView() string {
	if m.compactAuthLayout() {
		lines := []string{
			titleStyle.Render("Confirm administrator creation"),
			cropWidth("Create administrator "+sanitizeAPIText(m.bootstrapUsername)+"?", m.width),
			buttonStyle.Render("Confirm [Enter]") + " " + cancelStyle.Render("Cancel [Esc]"),
			mutedStyle.Render("Enter confirms · Escape cancels"),
		}
		m.bootstrapConfirmRect = rect{0, 2, max(1, m.width/2), 1}
		m.bootstrapCancelRect = rect{max(1, m.width/2), 2, max(1, m.width-m.width/2), 1}
		return cropHeight(strings.Join(lines, "\n"), m.height)
	}
	content := strings.Join([]string{
		titleStyle.Render("Confirm administrator creation"),
		"",
		"Create administrator " + sanitizeAPIText(m.bootstrapUsername) + "?",
		"",
		buttonStyle.Render("Confirm [Enter]") + " " + cancelStyle.Render("Cancel [Esc]"),
		mutedStyle.Render("Enter confirms · Escape cancels"),
	}, "\n")
	boxWidth := min(max(1, m.width-4), 68)
	box := panelStyle.Width(boxWidth).Render(content)
	x := max(0, (m.width-lipgloss.Width(box))/2)
	y := max(0, (m.height-lipgloss.Height(box))/2)
	m.bootstrapConfirmRect = rect{x + 1, y + 5, 18, 1}
	m.bootstrapCancelRect = rect{x + 20, y + 5, 16, 1}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) consoleView() string {
	if m.layout.unsupported {
		return m.centered(titleStyle.Render("Terminal too small") + "\nResize to at least 32×8\nCtrl+C quits")
	}
	if m.layout.tiny {
		return m.tinyConsoleView()
	}
	header := m.renderHeader()
	overview := m.renderOverview()
	transcript := panelStyle.Width(max(1, m.layout.transcript.w-4)).Height(max(1, m.layout.transcript.h-2)).Render(m.viewport.View())
	body := overview + "\n" + transcript
	if m.layout.wide {
		body = lipgloss.JoinHorizontal(lipgloss.Top, overview, " ", transcript)
	}
	lower := m.renderLower()
	footer := mutedStyle.Render(m.renderFooter())
	return strings.Join([]string{header, body, lower, footer}, "\n")
}

func (m *Model) tinyConsoleView() string {
	selected := m.selectedAppName()
	header := titleStyle.Render("hostd") + " " + mutedStyle.Render(selected)
	if m.confirm != nil {
		prompt := cropWidth(errorStyle.Render("CONFIRM: "+m.confirm.Text), m.width)
		actions := cropWidth(buttonStyle.Render("Confirm [Enter]")+" "+cancelStyle.Render("Cancel [Esc]"), m.width)
		command := "Command: " + m.commandInput.View()
		footer := mutedStyle.Render("Enter confirms · Esc cancels · Ctrl+C quits")
		return cropHeight(strings.Join([]string{header, prompt, actions, command, footer}, "\n"), m.height)
	}
	transcript := cropHeight(m.viewport.View(), max(1, m.layout.transcript.h))
	command := "Command: " + m.commandInput.View()
	footer := mutedStyle.Render("Enter run · Esc stop follow · Ctrl+C quit")
	return cropHeight(strings.Join([]string{header, transcript, command, footer}, "\n"), m.height)
}

func (m *Model) renderHeader() string {
	connection := "[connected]"
	if m.busy {
		connection = "[working]"
	}
	left := titleStyle.Render("hostd operator console") + "  " + connection
	right := fmt.Sprintf("%s · %s", endpointLabel(m.endpoint), sanitizeAPIText(m.user.Username))
	line := left
	space := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if space > 1 {
		line += strings.Repeat(" ", space) + mutedStyle.Render(right)
	}
	selected := "application: " + m.selectedAppName()
	follow := ""
	if m.followJobID != "" {
		follow = "  following: " + sanitizeAPIText(m.followJobID)
	}
	return cropWidth(line, m.width) + "\n" + cropWidth(mutedStyle.Render(selected+follow), m.width) + "\n" + strings.Repeat("─", m.width)
}

func (m *Model) renderOverview() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Overview"))
	b.WriteString("\n")
	if !m.overviewLoaded {
		message := "Overview unavailable; run /status to retry."
		if m.overviewLoading {
			message = "Loading controller overview…"
		}
		b.WriteString(mutedStyle.Render(message))
		return panelStyle.Width(max(1, m.layout.overview.w-4)).Height(max(1, m.layout.overview.h-2)).Render(b.String())
	}
	daemon := sanitizeAPIText(m.status.Daemon)
	if daemon == "" {
		daemon = "loading"
	}
	fmt.Fprintf(&b, "daemon  %s\nengine  %s\n", daemon, boolWord(m.status.Diagnostics.EngineReady))
	b.WriteString("\n" + titleStyle.Render("Applications") + "\n")
	visible := min(len(m.apps), max(0, min(3, m.layout.overview.h-12)))
	for i := 0; i < visible; i++ {
		app := m.apps[i]
		line := fmt.Sprintf("%-14s %s", truncate(sanitizeAPIText(app.Slug), 14), truncate(sanitizeAPIText(app.Status), 9))
		if app.ID == m.selectedAppID {
			line = selectedStyle.Width(max(1, m.layout.overview.w-4)).Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(m.apps) == 0 {
		b.WriteString(mutedStyle.Render("No applications") + "\n")
	}
	b.WriteString("\n" + titleStyle.Render("Active / failures") + "\n")
	for _, job := range m.overviewJobRows {
		b.WriteString(fmt.Sprintf("%-10s %s\n", truncate(sanitizeAPIText(job.Status), 10), truncate(sanitizeAPIText(job.ID), max(8, m.layout.overview.w-15))))
	}
	if len(m.overviewJobRows) == 0 {
		b.WriteString(mutedStyle.Render("No active or failed jobs") + "\n")
	}
	return panelStyle.Width(max(1, m.layout.overview.w-4)).Height(max(1, m.layout.overview.h-2)).Render(strings.TrimSuffix(b.String(), "\n"))
}

func (m *Model) renderLower() string {
	if m.confirm != nil {
		prompt := cropWidth(errorStyle.Render("CONFIRMATION REQUIRED: "+m.confirm.Text), max(1, m.width-29))
		prompt += strings.Repeat(" ", max(1, m.width-29-lipgloss.Width(prompt)))
		return cropHeight(prompt+buttonStyle.Render("Run [Enter]")+" "+cancelStyle.Render("Cancel [Esc]"), max(1, m.layout.confirmation.h)) + "\n" + panelStyle.Width(max(1, m.layout.command.w-4)).Render("Command: "+m.commandInput.View())
	}
	var lines []string
	for i, suggestion := range m.suggestions {
		line := "  " + suggestion
		if i == m.suggestion {
			line = selectedStyle.Render("› " + suggestion)
		}
		lines = append(lines, cropWidth(line, m.width))
	}
	suggestions := cropHeight(strings.Join(lines, "\n"), m.layout.suggestions.h)
	command := panelStyle.Width(max(1, m.layout.command.w-4)).Render("Command: " + m.commandInput.View())
	if suggestions == "" {
		return command
	}
	return suggestions + "\n" + command
}

func (m *Model) renderFooter() string {
	state := "connected"
	if m.busy {
		state = "working"
	}
	return cropWidth(fmt.Sprintf("Endpoint: %s | App: %s | State: %s | Tab complete | ↑↓ history | PgUp/PgDn scroll | Esc cancel | Ctrl+C quit", endpointLabel(m.endpoint), m.selectedAppName(), state), m.width)
}

func (m *Model) rebuildHitTargets() {
	m.suggestionRects = m.suggestionRects[:0]
	for i := 0; i < min(len(m.suggestions), m.layout.suggestions.h); i++ {
		m.suggestionRects = append(m.suggestionRects, rect{0, m.layout.suggestions.y + i, m.width, 1})
	}
	m.appRects = m.appRects[:0]
	visible := min(len(m.apps), max(0, min(3, m.layout.overview.h-12)))
	for i := 0; i < visible; i++ {
		m.appRects = append(m.appRects, rect{m.layout.overview.x + 1, m.layout.overview.y + 6 + i, max(1, m.layout.overview.w-2), 1})
	}
	m.overviewJobRows = overviewJobs(m.jobs, max(0, m.layout.overview.h-10-visible))
	m.jobRects = m.jobRects[:0]
	jobStartY := m.layout.overview.y + 8 + visible
	for i := range m.overviewJobRows {
		m.jobRects = append(m.jobRects, rect{m.layout.overview.x + 1, jobStartY + i, max(1, m.layout.overview.w-2), 1})
	}
	if m.confirm != nil {
		if m.layout.tiny {
			y := m.layout.confirmation.y + 1
			m.confirmRect = rect{0, y, max(1, m.width/2), 1}
			m.cancelRect = rect{max(1, m.width/2), y, max(1, m.width-m.width/2), 1}
		} else {
			y := m.layout.confirmation.y
			m.confirmRect = rect{max(0, m.width-29), y, 14, 1}
			m.cancelRect = rect{max(0, m.width-14), y, 14, 1}
		}
	}
}

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.accessible || m.busy {
		return m, nil
	}
	event := tea.MouseEvent(msg)
	if event.Button == tea.MouseButtonWheelUp || event.Button == tea.MouseButtonWheelDown {
		if m.layout.transcript.contains(event.X, event.Y) {
			if event.Button == tea.MouseButtonWheelUp {
				m.viewport.ScrollUp(3)
			} else {
				m.viewport.ScrollDown(3)
			}
		}
		return m, nil
	}
	if event.Action != tea.MouseActionPress || event.Button != tea.MouseButtonLeft {
		return m, nil
	}
	if m.screen == screenBootstrap && m.bootstrapConfirm {
		if m.bootstrapConfirmRect.contains(event.X, event.Y) {
			return m.submitAuth()
		}
		if m.bootstrapCancelRect.contains(event.X, event.Y) {
			m.cancelBootstrapConfirmation()
		}
		return m, nil
	}
	if m.screen != screenConsole {
		return m, nil
	}
	if m.confirm != nil {
		if m.confirmRect.contains(event.X, event.Y) {
			cmd := m.confirm.Command
			m.confirm = nil
			return m, m.execute(cmd)
		}
		if m.cancelRect.contains(event.X, event.Y) {
			m.confirm = nil
			m.appendEntry(entrySystem, "cancelled", "No changes were made.")
			m.restoreCommandFocus()
		}
		return m, nil
	}
	for i, target := range m.suggestionRects {
		if target.contains(event.X, event.Y) && i < len(m.suggestions) {
			m.commandInput.SetValue(m.suggestions[i])
			m.commandInput.CursorEnd()
			m.refreshSuggestions()
			return m, nil
		}
	}
	for i, target := range m.appRects {
		if target.contains(event.X, event.Y) && i < len(m.apps) {
			m.selectedAppID = m.apps[i].ID
			return m, nil
		}
	}
	for i, target := range m.jobRects {
		if target.contains(event.X, event.Y) && i < len(m.overviewJobRows) {
			jobID := m.overviewJobRows[i].ID
			return m, m.execute(command{Name: "/job", Args: []string{jobID}, Raw: "/job " + jobID})
		}
	}
	return m, nil
}

func overviewJobs(jobs []apicontract.Job, limit int) []apicontract.Job {
	if limit <= 0 {
		return nil
	}
	rows := make([]apicontract.Job, 0, limit)
	for _, job := range jobs {
		status := strings.ToLower(job.Status)
		if status != "succeeded" && status != "failed" && status != "cancelled" && status != "interrupted" && status != "needs_attention" {
			rows = append(rows, job)
		}
		if len(rows) == limit {
			return rows
		}
	}
	for _, job := range jobs {
		status := strings.ToLower(job.Status)
		if status == "failed" || status == "interrupted" || status == "needs_attention" {
			rows = append(rows, job)
		}
		if len(rows) == limit {
			break
		}
	}
	return rows
}

func boolWord(value bool) string {
	if value {
		return "ready"
	}
	return "not ready"
}

func cropWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func cropHeight(value string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func truncate(value string, width int) string {
	return cropWidth(value, width)
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
		// The command prompt and current result are at the end of a console
		// frame, so retain the tail rather than obscuring the active control.
		lines = lines[len(lines)-m.height:]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) accessibleView() string {
	var b strings.Builder
	b.WriteString("hostd operator console\n")
	b.WriteString("Endpoint: " + endpointLabel(m.endpoint) + "\n")
	switch m.screen {
	case screenLoading:
		b.WriteString("Connection state: connecting\n")
	case screenOffline:
		b.WriteString("Connection state: unavailable\nError: " + sanitizeAPIText(m.err) + "\nRecovery: start hostd serve, then press Enter or r to retry.\n")
	case screenBootstrap:
		b.WriteString("Authentication: bootstrap required\n")
		if m.bootstrapConfirm {
			b.WriteString("Confirmation required: create administrator " + sanitizeAPIText(m.bootstrapUsername) + ". Press Enter to confirm or Escape to cancel.\n")
		} else {
			b.WriteString(m.linearAuthFields())
		}
	case screenLogin:
		b.WriteString("Authentication: sign in\n" + m.linearAuthFields())
	case screenConsole:
		state := "connected"
		if m.busy {
			state = "working"
		}
		b.WriteString("Connection state: " + state + "\nSelected application: " + m.selectedAppName() + "\n")
		if !m.overviewLoaded {
			b.WriteString("Overview: loading\n")
		}
		lines, page, pages := m.accessibleTranscriptPage()
		if pages > 0 {
			fmt.Fprintf(&b, "Transcript page %d of %d (PgUp older, PgDn newer)\n", page+1, pages)
			for _, line := range lines {
				b.WriteString(line + "\n")
			}
		}
		if m.confirm != nil {
			b.WriteString("Confirmation required: " + m.confirm.Text + ". Press Enter to run or Escape to cancel.\n")
		}
		b.WriteString("Command: " + m.commandInput.View() + "\nShortcuts: Enter run; Tab complete; Escape cancel; Ctrl+C quit.\n")
	}
	return b.String()
}

// accessibleTranscriptPage keeps ordinary keystrokes cheap: sanitization and
// line splitting happen only when transcript content or terminal width changes.
// The retained transcript itself can be 1 MiB, so the cache builds from a
// bounded tail before it starts splitting lines for the current terminal.
func (m *Model) accessibleTranscriptPage() ([]string, int, int) {
	m.refreshAccessibleTranscript()
	pageSize := m.accessiblePageSize()
	pages := max(1, (len(m.accessibleLines)+pageSize-1)/pageSize)
	if m.accessiblePage >= pages {
		m.accessiblePage = pages - 1
	}
	end := len(m.accessibleLines) - m.accessiblePage*pageSize
	if end < 0 {
		end = 0
	}
	start := max(0, end-pageSize)
	return m.accessibleLines[start:end], m.accessiblePage, pages
}

func (m *Model) accessiblePageSize() int {
	// Header, connection summary, paging label, command bar, and shortcuts use
	// at most eight lines. Keep at least one transcript line available.
	return max(1, m.height-8)
}

func (m *Model) accessiblePageUp() {
	m.refreshAccessibleTranscript()
	pages := max(1, (len(m.accessibleLines)+m.accessiblePageSize()-1)/m.accessiblePageSize())
	if m.accessiblePage < pages-1 {
		m.accessiblePage++
	}
}

func (m *Model) accessiblePageDown() {
	if m.accessiblePage > 0 {
		m.accessiblePage--
	}
}

func (m *Model) refreshAccessibleTranscript() {
	if m.accessibleGen == m.transcriptGen && m.accessibleWidth == m.width {
		return
	}
	budget := accessibleTranscriptBudget(m.width)
	start := len(m.entries)
	used := 0
	for start > 0 {
		size := transcriptEntryBytes(m.entries[start-1])
		if start < len(m.entries) && used+size > budget {
			break
		}
		start--
		used += size
		if used >= budget {
			break
		}
	}
	lines := make([]string, 0, min(len(m.entries)-start, max(1, m.height*4)))
	for _, entry := range m.entries[start:] {
		lines = append(lines, cropWidth(entryKindLabel(entry.Kind)+" "+entry.Title, m.width))
		// The last selected entry can still be close to the byte budget. Bound it
		// before Split so a single API message cannot make a redraw expensive.
		body := tailUTF8(entry.Body, budget)
		for _, line := range strings.Split(body, "\n") {
			lines = append(lines, cropWidth(line, m.width))
		}
	}
	m.accessibleLines = lines
	m.accessibleGen = m.transcriptGen
	m.accessibleWidth = m.width
	m.accessibleBuilds++
}

func accessibleTranscriptBudget(width int) int {
	// Height changes only alter the selected page. Width changes alter wrapping,
	// so it is the only terminal dimension that invalidates this cache.
	return min(128<<10, max(8<<10, max(1, width)*256))
}

func tailUTF8(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return "…" + value[start:]
}

func (m *Model) linearAuthFields() string {
	var b strings.Builder
	for i := range m.authInputs {
		b.WriteString(authFieldLabel(m.screen, i) + ": " + m.authInputs[i].View() + "\n")
	}
	if m.err != "" {
		b.WriteString("Error: " + sanitizeAPIText(m.err) + "\n")
	}
	return b.String()
}

func authFieldLabel(current screen, index int) string {
	if current == screenBootstrap {
		return []string{"Bootstrap token", "Administrator username", "Passphrase"}[index]
	}
	return []string{"Username", "Passphrase"}[index]
}

func entryKindLabel(kind entryKind) string {
	return []string{"SYSTEM", "COMMAND", "RESULT", "ERROR", "EVENT"}[kind]
}

func endpointLabel(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return sanitizeAPIText(parsed.Scheme + "://" + parsed.Host)
	}
	return sanitizeAPIText(endpoint)
}

func noColor() bool {
	_, set := os.LookupEnv("NO_COLOR")
	return set
}
