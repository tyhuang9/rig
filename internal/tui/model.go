package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hostd/hostd/internal/apicontract"
)

const transcriptLimit = 500

type screen uint8

const (
	screenLoading screen = iota
	screenOffline
	screenBootstrap
	screenLogin
	screenConsole
)

type entryKind uint8

const (
	entrySystem entryKind = iota
	entryCommand
	entrySuccess
	entryError
	entryEvent
)

type transcriptEntry struct {
	Kind  entryKind
	Title string
	Body  string
}

type confirmation struct {
	Command command
	Text    string
}

type dependencies struct {
	ctx      context.Context
	client   Client
	history  HistoryStore
	endpoint string
	newKey   func() string
}

// Model is exported so integrations can embed or test the console without
// starting a terminal program. NewModel returns it in its loading state.
type Model struct {
	ctx      context.Context
	client   Client
	history  HistoryStore
	endpoint string
	newKey   func() string

	screen screen
	width  int
	height int
	layout layout
	busy   bool
	err    string
	user   apicontract.User
	status apicontract.SystemStatus
	apps   []apicontract.Application
	jobs   []apicontract.Job

	selectedAppID string
	entries       []transcriptEntry
	viewport      viewport.Model
	commandInput  textinput.Model
	authInputs    []textinput.Model
	authIndex     int

	suggestions     []string
	suggestion      int
	historyValues   []string
	historyIndex    int
	draft           string
	confirm         *confirmation
	followJobID     string
	followCursor    int64
	followCancel    context.CancelFunc
	followContext   context.Context
	followEvents    <-chan apicontract.JobEvent
	followErrors    <-chan error
	suggestionRects []rect
	appRects        []rect
	jobRects        []rect
	overviewJobRows []apicontract.Job
	confirmRect     rect
	cancelRect      rect
}

func NewModel(ctx context.Context, client Client, history HistoryStore, endpoint string) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	if history == nil {
		history = &memoryHistoryStore{}
	}
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "/help"
	input.CharLimit = 512
	input.Focus()
	m := &Model{
		ctx:          ctx,
		client:       client,
		history:      history,
		endpoint:     endpoint,
		newKey:       randomIdempotencyKey,
		screen:       screenLoading,
		viewport:     viewport.New(1, 1),
		commandInput: input,
	}
	m.suggestions = commandSuggestions("")
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadHistory(), m.checkBootstrap())
}

type historyLoadedMsg struct {
	values []string
	err    error
}
type bootstrapStatusMsg struct {
	status apicontract.BootstrapStatus
	err    error
}
type meMsg struct {
	me  apicontract.MeResponse
	err error
}
type authMsg struct {
	session apicontract.SessionResponse
	err     error
}
type overviewMsg struct {
	status apicontract.SystemStatus
	apps   apicontract.ApplicationList
	jobs   apicontract.JobList
	err    error
}
type commandResultMsg struct {
	cmd      command
	body     string
	apps     []apicontract.Application
	jobs     []apicontract.Job
	job      *apicontract.Job
	followID string
	err      error
}
type followOpenedMsg struct {
	jobID  string
	events <-chan apicontract.JobEvent
	errors <-chan error
}
type followEventMsg struct {
	event apicontract.JobEvent
	err   error
	done  bool
}
type historySavedMsg struct{ err error }
type sessionClearedMsg struct{}

func (m *Model) loadHistory() tea.Cmd {
	return func() tea.Msg {
		values, err := m.history.Load(m.ctx)
		return historyLoadedMsg{values: values, err: err}
	}
}

func (m *Model) checkBootstrap() tea.Cmd {
	return func() tea.Msg {
		status, err := m.client.BootstrapStatus(m.ctx)
		return bootstrapStatusMsg{status: status, err: err}
	}
}

func (m *Model) checkMe() tea.Cmd {
	return func() tea.Msg {
		me, err := m.client.Me(m.ctx)
		return meMsg{me: me, err: err}
	}
}

func (m *Model) loadOverview() tea.Cmd {
	return func() tea.Msg {
		status, err := m.client.Status(m.ctx)
		if err != nil {
			return overviewMsg{err: err}
		}
		apps, err := m.client.Applications(m.ctx)
		if err != nil {
			return overviewMsg{err: err}
		}
		jobs, err := m.client.Jobs(m.ctx)
		return overviewMsg{status: status, apps: apps, jobs: jobs, err: err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case historyLoadedMsg:
		if msg.err == nil {
			m.historyValues = normalizeHistory(msg.values)
			m.historyIndex = len(m.historyValues)
		}
		return m, nil
	case bootstrapStatusMsg:
		if msg.err != nil {
			m.goOffline(msg.err)
			return m, nil
		}
		if msg.status.BootstrapRequired {
			m.showBootstrap()
			return m, nil
		}
		m.busy = true
		return m, m.checkMe()
	case meMsg:
		m.busy = false
		if msg.err != nil {
			if isUnauthenticated(msg.err) {
				m.showLogin("")
			} else {
				m.goOffline(msg.err)
			}
			return m, nil
		}
		m.enterConsole(msg.me.User)
		return m, m.loadOverview()
	case authMsg:
		m.busy = false
		if msg.err != nil {
			m.err = sanitizeAPIText(msg.err.Error())
			return m, nil
		}
		m.enterConsole(msg.session.User)
		return m, m.loadOverview()
	case overviewMsg:
		m.busy = false
		if msg.err != nil {
			return m, m.handleControllerError(msg.err)
		}
		m.status = msg.status
		m.apps = append([]apicontract.Application(nil), msg.apps.Items...)
		m.jobs = append([]apicontract.Job(nil), msg.jobs.Items...)
		if m.selectedAppID == "" && len(m.apps) > 0 {
			m.selectedAppID = m.apps[0].ID
		}
		m.rebuildHitTargets()
		return m, nil
	case commandResultMsg:
		m.busy = false
		if msg.err != nil {
			return m, m.handleControllerError(msg.err)
		}
		if msg.apps != nil {
			m.apps = append([]apicontract.Application(nil), msg.apps...)
			m.rebuildHitTargets()
		}
		if msg.jobs != nil {
			m.jobs = append([]apicontract.Job(nil), msg.jobs...)
			m.rebuildHitTargets()
		}
		m.appendEntry(entrySuccess, msg.cmd.Name, msg.body)
		if msg.cmd.Name == "/logout" {
			m.showLogin("Signed out.")
			return m, nil
		}
		if msg.followID != "" {
			return m, m.startFollowing(msg.followID, 0)
		}
		return m, nil
	case followOpenedMsg:
		m.followJobID = msg.jobID
		m.followEvents, m.followErrors = msg.events, msg.errors
		m.appendEntry(entrySystem, "follow", "Following job "+sanitizeAPIText(msg.jobID)+"; Escape stops local follow.")
		return m, m.waitForFollow()
	case followEventMsg:
		if msg.done {
			m.stopFollowing(false)
			return m, nil
		}
		if msg.err != nil {
			m.appendEntry(entryError, "follow", msg.err.Error())
			m.stopFollowing(false)
			return m, nil
		}
		if msg.event.ID > m.followCursor {
			m.followCursor = msg.event.ID
		}
		m.appendEntry(entryEvent, fmt.Sprintf("%s · %s · %d%%", msg.event.Phase, msg.event.Level, eventProgress(msg.event, m.jobs)), msg.event.Message)
		if jobEventTerminal(msg.event) {
			m.stopFollowing(false)
			return m, m.loadOverview()
		}
		return m, m.waitForFollow()
	case historySavedMsg:
		if msg.err != nil {
			m.appendEntry(entryError, "history", "Could not persist command history: "+msg.err.Error())
		}
		return m, nil
	case sessionClearedMsg:
		return m, nil
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.screen == screenConsole && m.confirm == nil {
		var cmd tea.Cmd
		m.commandInput, cmd = m.commandInput.Update(msg)
		m.refreshSuggestions()
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "ctrl+c" {
		m.stopFollowing(false)
		return m, tea.Quit
	}
	switch m.screen {
	case screenOffline:
		if key.String() == "enter" || strings.EqualFold(key.String(), "r") {
			m.screen, m.err, m.busy = screenLoading, "", true
			return m, m.checkBootstrap()
		}
		return m, nil
	case screenBootstrap, screenLogin:
		return m.handleAuthKey(key)
	case screenConsole:
		return m.handleConsoleKey(key)
	default:
		return m, nil
	}
}

func (m *Model) handleAuthKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	switch key.String() {
	case "tab", "down":
		m.focusAuth((m.authIndex + 1) % len(m.authInputs))
		return m, nil
	case "shift+tab", "up":
		m.focusAuth((m.authIndex - 1 + len(m.authInputs)) % len(m.authInputs))
		return m, nil
	case "enter":
		if m.authIndex < len(m.authInputs)-1 {
			m.focusAuth(m.authIndex + 1)
			return m, nil
		}
		return m.submitAuth()
	}
	var cmd tea.Cmd
	m.authInputs[m.authIndex], cmd = m.authInputs[m.authIndex].Update(key)
	return m, cmd
}

func (m *Model) handleConsoleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		switch key.String() {
		case "enter":
			cmd := m.confirm.Command
			m.confirm = nil
			return m, m.execute(cmd)
		case "esc":
			m.appendEntry(entrySystem, "cancelled", "No changes were made.")
			m.confirm = nil
		}
		return m, nil
	}
	if key.String() == "esc" {
		if m.followCancel != nil {
			m.stopFollowing(true)
			return m, nil
		}
		m.commandInput.SetValue("")
		m.refreshSuggestions()
		return m, nil
	}
	if m.busy {
		return m, nil
	}
	switch key.String() {
	case "enter":
		return m.submitCommand()
	case "tab":
		if len(m.suggestions) > 0 {
			m.commandInput.SetValue(m.suggestions[m.suggestion])
			m.commandInput.CursorEnd()
			m.refreshSuggestions()
		}
		return m, nil
	case "up":
		if len(m.suggestions) > 1 && strings.TrimSpace(m.commandInput.Value()) != "" {
			m.suggestion = (m.suggestion - 1 + len(m.suggestions)) % len(m.suggestions)
			return m, nil
		}
		m.previousHistory()
		return m, nil
	case "down":
		if len(m.suggestions) > 1 && strings.TrimSpace(m.commandInput.Value()) != "" {
			m.suggestion = (m.suggestion + 1) % len(m.suggestions)
			return m, nil
		}
		m.nextHistory()
		return m, nil
	case "pgup":
		m.viewport.PageUp()
		return m, nil
	case "pgdown":
		m.viewport.PageDown()
		return m, nil
	}
	var cmd tea.Cmd
	m.commandInput, cmd = m.commandInput.Update(key)
	m.refreshSuggestions()
	return m, cmd
}

func (m *Model) submitCommand() (tea.Model, tea.Cmd) {
	parsed, err := parseCommand(m.commandInput.Value())
	if err != nil {
		m.appendEntry(entryError, "command", err.Error())
		return m, nil
	}
	m.commandInput.SetValue("")
	m.refreshSuggestions()
	m.appendEntry(entryCommand, "you", parsed.Raw)
	var persist tea.Cmd
	if parsed.Name != "/history clear" {
		m.historyValues = normalizeHistory(append(m.historyValues, parsed.Raw))
		m.historyIndex = len(m.historyValues)
		values := append([]string(nil), m.historyValues...)
		persist = func() tea.Msg { return historySavedMsg{err: m.history.Save(m.ctx, values)} }
	}
	spec := commandSpecs[parsed.Name]
	if spec.Confirm {
		if err := m.validateCommand(parsed); err != nil {
			m.appendEntry(entryError, parsed.Name, err.Error())
			return m, persist
		}
		m.confirm = &confirmation{Command: parsed, Text: confirmationText(parsed, m.targetAppName(parsed))}
		m.resize()
		return m, persist
	}
	return m, tea.Batch(persist, m.execute(parsed))
}

func (m *Model) validateCommand(cmd command) error {
	switch cmd.Name {
	case "/deploy", "/start", "/stop", "/restart", "/app":
		if _, ok := m.targetApp(cmd); !ok {
			return errors.New("application not found; run /apps or /use <slug-or-id>")
		}
	}
	return nil
}

func (m *Model) execute(cmd command) tea.Cmd {
	if err := m.validateCommand(cmd); err != nil {
		return func() tea.Msg {
			return commandResultMsg{cmd: cmd, err: &HTTPError{Status: 400, Code: "invalid_command", Detail: err.Error()}}
		}
	}
	switch cmd.Name {
	case "/help":
		m.appendEntry(entrySystem, "commands", helpText())
		return nil
	case "/clear":
		m.entries = nil
		m.refreshTranscript()
		return nil
	case "/history clear":
		m.historyValues = nil
		m.historyIndex = 0
		return func() tea.Msg { return historySavedMsg{err: m.history.Clear(m.ctx)} }
	case "/quit":
		m.stopFollowing(false)
		return tea.Quit
	case "/logout":
		m.busy = true
		return func() tea.Msg {
			err := m.client.Logout(m.ctx)
			if err == nil {
				_ = m.history.Save(m.ctx, m.historyValues)
			}
			return commandResultMsg{cmd: cmd, body: "Session ended.", err: err}
		}
	case "/use":
		for _, app := range m.apps {
			if strings.EqualFold(app.ID, cmd.Args[0]) || strings.EqualFold(app.Slug, cmd.Args[0]) {
				m.selectedAppID = app.ID
				m.appendEntry(entrySuccess, "/use", fmt.Sprintf("Selected %s (%s).", sanitizeAPIText(app.Name), sanitizeAPIText(app.ID)))
				return nil
			}
		}
		m.appendEntry(entryError, "/use", "Application not found in the current list; run /apps to refresh.")
		return nil
	case "/follow":
		return m.startFollowing(cmd.Args[0], 0)
	}
	m.busy = true
	return func() tea.Msg { return m.runAPICommand(cmd) }
}

func (m *Model) runAPICommand(cmd command) commandResultMsg {
	result := commandResultMsg{cmd: cmd}
	switch cmd.Name {
	case "/status":
		v, err := m.client.Status(m.ctx)
		result.err = err
		result.body = formatStatus(v)
	case "/doctor":
		v, err := m.client.Doctor(m.ctx)
		result.err = err
		result.body = formatDoctor(v)
	case "/apps":
		v, err := m.client.Applications(m.ctx)
		result.err, result.apps = err, v.Items
		result.body = formatApps(v.Items)
	case "/app":
		v, err := m.client.Application(m.ctx, m.selectedAppID)
		result.err = err
		result.body = formatApp(v)
	case "/machines":
		v, err := m.client.Machines(m.ctx)
		result.err = err
		result.body = formatMachines(v.Items)
	case "/jobs":
		v, err := m.client.Jobs(m.ctx)
		result.err, result.jobs = err, v.Items
		result.body = formatJobs(v.Items)
	case "/job":
		v, err := m.client.Job(m.ctx, cmd.Args[0])
		result.err, result.job = err, &v
		result.body = formatJob(v)
	case "/deploy":
		target, _ := m.targetApp(cmd)
		v, err := m.client.Deploy(m.ctx, target.ID, m.newKey())
		result.err, result.job, result.followID = err, &v.Job, v.Job.ID
		result.body = mutationBody(v)
	case "/start", "/stop", "/restart":
		target, _ := m.targetApp(cmd)
		v, err := m.client.Lifecycle(m.ctx, target.ID, strings.TrimPrefix(cmd.Name, "/"), m.newKey())
		result.err, result.job, result.followID = err, &v.Job, v.Job.ID
		result.body = mutationBody(v)
	case "/cancel":
		v, err := m.client.CancelJob(m.ctx, cmd.Args[0], m.newKey())
		result.err, result.job = err, &v.Job
		result.body = formatJob(v.Job)
	case "/resume":
		v, err := m.client.ResumeJob(m.ctx, cmd.Args[0], m.newKey())
		result.err, result.job, result.followID = err, &v.Job, v.Job.ID
		result.body = formatJob(v.Job)
	}
	return result
}

func (m *Model) startFollowing(jobID string, after int64) tea.Cmd {
	m.stopFollowing(false)
	ctx, cancel := context.WithCancel(m.ctx)
	m.followCancel = cancel
	m.followContext = ctx
	m.followCursor = after
	return func() tea.Msg {
		events, errs := m.client.FollowJob(ctx, jobID, after)
		return followOpenedMsg{jobID: jobID, events: events, errors: errs}
	}
}

func (m *Model) waitForFollow() tea.Cmd {
	events, errs := m.followEvents, m.followErrors
	ctx := m.followContext
	if ctx == nil {
		ctx = m.ctx
	}
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return followEventMsg{done: true}
		case event, ok := <-events:
			if !ok {
				if errs == nil {
					return followEventMsg{done: true}
				}
				select {
				case err, ok := <-errs:
					if ok && err != nil {
						return followEventMsg{err: err}
					}
				default:
				}
				return followEventMsg{done: true}
			}
			return followEventMsg{event: event}
		case err, ok := <-errs:
			if ok && err != nil {
				return followEventMsg{err: err}
			}
			return followEventMsg{done: true}
		}
	}
}

func (m *Model) stopFollowing(notify bool) {
	if m.followCancel != nil {
		m.followCancel()
		if notify {
			m.appendEntry(entrySystem, "follow", "Stopped local follow; the server job is still running.")
		}
	}
	m.followCancel, m.followContext, m.followEvents, m.followErrors = nil, nil, nil, nil
	m.followJobID = ""
}

func (m *Model) submitAuth() (tea.Model, tea.Cmd) {
	for _, input := range m.authInputs {
		if strings.TrimSpace(input.Value()) == "" {
			m.err = "All fields are required."
			return m, nil
		}
	}
	m.busy, m.err = true, ""
	if m.screen == screenLogin {
		request := apicontract.LoginRequest{Username: m.authInputs[0].Value(), Passphrase: m.authInputs[1].Value()}
		m.clearAuthValues()
		return m, func() tea.Msg { v, err := m.client.Login(m.ctx, request); return authMsg{session: v, err: err} }
	}
	request := apicontract.BootstrapRequest{Token: m.authInputs[0].Value(), Username: m.authInputs[1].Value(), Passphrase: m.authInputs[2].Value()}
	m.clearAuthValues()
	return m, func() tea.Msg { v, err := m.client.Bootstrap(m.ctx, request); return authMsg{session: v, err: err} }
}

func (m *Model) showBootstrap() {
	m.screen, m.busy, m.err = screenBootstrap, false, ""
	m.authInputs = []textinput.Model{authInput("bootstrap token", true), authInput("admin username", false), authInput("passphrase", true)}
	m.focusAuth(0)
}

func (m *Model) showLogin(message string) {
	m.stopFollowing(false)
	m.screen, m.busy, m.err = screenLogin, false, sanitizeAPIText(message)
	m.authInputs = []textinput.Model{authInput("username", false), authInput("passphrase", true)}
	m.focusAuth(0)
}

func authInput(placeholder string, secret bool) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.CharLimit = 512
	if secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	return input
}

func (m *Model) focusAuth(index int) {
	m.authIndex = index
	for i := range m.authInputs {
		if i == index {
			m.authInputs[i].Focus()
		} else {
			m.authInputs[i].Blur()
		}
	}
}

func (m *Model) clearAuthValues() {
	for i := range m.authInputs {
		m.authInputs[i].SetValue("")
	}
}

func (m *Model) enterConsole(user apicontract.User) {
	m.screen, m.busy, m.err, m.user = screenConsole, false, "", user
	m.commandInput.Focus()
	m.appendEntry(entrySystem, "connected", "Authenticated as "+sanitizeAPIText(user.Username)+". Type /help for commands.")
}

func (m *Model) goOffline(err error) {
	m.stopFollowing(false)
	m.screen, m.busy, m.err = screenOffline, false, sanitizeAPIText(err.Error())
}

func (m *Model) handleControllerError(err error) tea.Cmd {
	if isUnauthenticated(err) {
		m.showLogin("Session expired. Sign in again.")
		return func() tea.Msg { _ = m.client.Logout(m.ctx); return sessionClearedMsg{} }
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		m.goOffline(err)
		return nil
	}
	m.appendEntry(entryError, "controller", err.Error())
	return nil
}

func (m *Model) appendEntry(kind entryKind, title, body string) {
	m.entries = append(m.entries, transcriptEntry{Kind: kind, Title: sanitizeAPIText(title), Body: sanitizeAPIText(body)})
	if len(m.entries) > transcriptLimit {
		m.entries = append([]transcriptEntry(nil), m.entries[len(m.entries)-transcriptLimit:]...)
	}
	m.refreshTranscript()
}

func (m *Model) refreshTranscript() {
	wasBottom := m.viewport.AtBottom()
	var b strings.Builder
	for _, entry := range m.entries {
		prefix := "·"
		switch entry.Kind {
		case entryCommand:
			prefix = ">"
		case entrySuccess:
			prefix = "✓"
		case entryError:
			prefix = "!"
		case entryEvent:
			prefix = "↳"
		}
		fmt.Fprintf(&b, "%s %s\n%s\n\n", prefix, entry.Title, entry.Body)
	}
	m.viewport.SetContent(strings.TrimSuffix(b.String(), "\n"))
	if wasBottom || len(m.entries) <= 1 {
		m.viewport.GotoBottom()
	}
}

func (m *Model) refreshSuggestions() {
	m.suggestions = commandSuggestions(m.commandInput.Value())
	if m.suggestion >= len(m.suggestions) {
		m.suggestion = 0
	}
	m.resize()
}

func (m *Model) previousHistory() {
	if len(m.historyValues) == 0 {
		return
	}
	if m.historyIndex == len(m.historyValues) {
		m.draft = m.commandInput.Value()
	}
	if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.commandInput.SetValue(m.historyValues[m.historyIndex])
	m.commandInput.CursorEnd()
	m.refreshSuggestions()
}

func (m *Model) nextHistory() {
	if m.historyIndex < len(m.historyValues)-1 {
		m.historyIndex++
		m.commandInput.SetValue(m.historyValues[m.historyIndex])
	} else {
		m.historyIndex = len(m.historyValues)
		m.commandInput.SetValue(m.draft)
	}
	m.commandInput.CursorEnd()
	m.refreshSuggestions()
}

func (m *Model) resize() {
	m.layout = calculateLayout(m.width, m.height, len(m.suggestions), m.confirm != nil)
	w := max(1, m.layout.transcript.w-2)
	h := max(1, m.layout.transcript.h-2)
	m.viewport.Width, m.viewport.Height = w, h
	m.commandInput.Width = max(1, m.layout.command.w-4)
	m.rebuildHitTargets()
	m.refreshTranscript()
}

func (m *Model) selectedAppName() string {
	for _, app := range m.apps {
		if app.ID == m.selectedAppID {
			if app.Slug != "" {
				return sanitizeAPIText(app.Slug)
			}
			return sanitizeAPIText(app.Name)
		}
	}
	return "selected application"
}

func (m *Model) targetApp(cmd command) (apicontract.Application, bool) {
	target := m.selectedAppID
	if (cmd.Name == "/deploy" || cmd.Name == "/start" || cmd.Name == "/stop" || cmd.Name == "/restart") && len(cmd.Args) == 1 {
		target = cmd.Args[0]
	}
	for _, app := range m.apps {
		if strings.EqualFold(app.ID, target) || strings.EqualFold(app.Slug, target) {
			return app, true
		}
	}
	return apicontract.Application{}, false
}

func (m *Model) targetAppName(cmd command) string {
	if app, ok := m.targetApp(cmd); ok {
		if app.Slug != "" {
			return sanitizeAPIText(app.Slug)
		}
		return sanitizeAPIText(app.Name)
	}
	return m.selectedAppName()
}

func randomIdempotencyKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "tui-operation"
	}
	return "tui-" + hex.EncodeToString(b)
}

func jobEventTerminal(event apicontract.JobEvent) bool {
	code := strings.ToLower(event.Code)
	phase := strings.ToLower(event.Phase)
	return code == "job_succeeded" || code == "job_failed" || code == "job_cancelled" || code == "daemon_restarted" || code == "needs_attention" || phase == "succeeded" || phase == "failed" || phase == "cancelled" || phase == "interrupted" || phase == "needs_attention"
}

func eventProgress(event apicontract.JobEvent, jobs []apicontract.Job) int {
	for _, job := range jobs {
		if job.ID == event.JobID {
			return job.Progress
		}
	}
	return 0
}

func formatStatus(v apicontract.SystemStatus) string {
	return fmt.Sprintf("daemon: %s\nengine ready: %t\ndocker: %s\ncompose: %s", sanitizeAPIText(v.Daemon), v.Diagnostics.EngineReady, sanitizeAPIText(v.Diagnostics.DockerVersion), sanitizeAPIText(v.Diagnostics.ComposeVersion))
}

func formatDoctor(v apicontract.DoctorResponse) string {
	lines := make([]string, 0, len(v.Checks)+1)
	for _, check := range v.Checks {
		mark := "FAIL"
		if check.OK {
			mark = "OK"
		}
		lines = append(lines, fmt.Sprintf("%-4s %-18s %s", mark, sanitizeAPIText(check.Name), sanitizeAPIText(check.Detail)))
	}
	if v.StartupLimitation != "" {
		lines = append(lines, "limitation: "+sanitizeAPIText(v.StartupLimitation))
	}
	return strings.Join(lines, "\n")
}

func formatApps(apps []apicontract.Application) string {
	if len(apps) == 0 {
		return "No applications."
	}
	items := append([]apicontract.Application(nil), apps...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Slug < items[j].Slug })
	lines := make([]string, 0, len(items))
	for _, app := range items {
		lines = append(lines, fmt.Sprintf("%-20s %-12s %s", sanitizeAPIText(app.Slug), sanitizeAPIText(app.Status), sanitizeAPIText(app.ID)))
	}
	return strings.Join(lines, "\n")
}

func formatApp(app apicontract.Application) string {
	return fmt.Sprintf("%s (%s)\nstatus: %s\nmachine: %s\nsource: %s\n%s", sanitizeAPIText(app.Name), sanitizeAPIText(app.Slug), sanitizeAPIText(app.Status), sanitizeAPIText(app.MachineName), sanitizeAPIText(app.Source.Type), sanitizeAPIText(app.Description))
}

func formatMachines(items []apicontract.Machine) string {
	if len(items) == 0 {
		return "No machines."
	}
	lines := make([]string, 0, len(items))
	for _, machine := range items {
		lines = append(lines, fmt.Sprintf("%-18s %-12s %s/%s", sanitizeAPIText(machine.Name), sanitizeAPIText(machine.Status), sanitizeAPIText(machine.OS), sanitizeAPIText(machine.Architecture)))
	}
	return strings.Join(lines, "\n")
}

func formatJobs(items []apicontract.Job) string {
	if len(items) == 0 {
		return "No jobs."
	}
	lines := make([]string, 0, len(items))
	for _, job := range items {
		lines = append(lines, fmt.Sprintf("%-24s %-10s %3d%% %s", sanitizeAPIText(job.ID), sanitizeAPIText(job.Status), job.Progress, sanitizeAPIText(job.Type)))
	}
	return strings.Join(lines, "\n")
}

func formatJob(job apicontract.Job) string {
	line := fmt.Sprintf("%s\nstatus: %s\nphase: %s\nprogress: %d%%\nresource: %s", sanitizeAPIText(job.ID), sanitizeAPIText(job.Status), sanitizeAPIText(job.Phase), job.Progress, sanitizeAPIText(job.ResourceID))
	if job.ErrorDetail != "" {
		line += "\nerror: " + sanitizeAPIText(job.ErrorDetail)
	}
	return line
}

func mutationBody(v apicontract.JobMutationResponse) string {
	verb := "Accepted"
	if !v.Created {
		verb = "Reused"
	}
	return fmt.Sprintf("%s job %s (%s).", verb, sanitizeAPIText(v.Job.ID), sanitizeAPIText(v.Job.Status))
}

var _ tea.Model = (*Model)(nil)
