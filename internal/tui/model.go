package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hostd/hostd/internal/apicontract"
)

type screen uint8

const (
	screenLoading screen = iota
	screenOffline
	screenBootstrap
	screenLogin
	screenSwitchboard
	screenActions
	screenConfirmation
	screenJobProgress
	screenResult
	screenHelp
)

type resultState struct{ AppID, JobID, Title, Detail string }

// Model is an explicit application-first state machine. Overview refresh,
// mutation submission, and the one active server-job follow have independent
// state so an advisory refresh cannot release a mutation guard.
type Model struct {
	ctx      context.Context
	client   Client
	endpoint string
	openURL  URLOpener
	newKey   func() string

	screen, returnScreen screen
	width, height        int
	layout               layout
	accessible           bool
	err                  string
	user                 apicontract.User

	status                          apicontract.SystemStatus
	apps                            []apicontract.Application
	jobs                            []apicontract.Job
	overviewLoaded, overviewLoading bool
	overviewGen                     uint64

	selectedAppID              string
	listOffset, selectedAction int
	confirmation               *confirmation
	result                     *resultState
	mutationBusy               bool

	followedJobID    string
	followedJob      apicontract.Job
	followCursor     int64
	followGeneration uint64
	followCancel     context.CancelFunc
	followContext    context.Context
	followEvents     <-chan apicontract.JobEvent
	followErrors     <-chan error
	recentEvents     []apicontract.JobEvent
	phases           []phaseState

	authInputs        []textinput.Model
	authIndex         int
	bootstrapConfirm  bool
	bootstrapUsername string
}

func NewModel(ctx context.Context, client Client, endpoint string) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Model{ctx: ctx, client: client, endpoint: endpoint, newKey: randomIdempotencyKey, screen: screenLoading, layout: calculateLayout(1, 1)}
}
func (m *Model) Init() tea.Cmd { return m.checkBootstrap() }

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
	generation uint64
	status     apicontract.SystemStatus
	apps       apicontract.ApplicationList
	jobs       apicontract.JobList
	err        error
}
type mutationMsg struct {
	request  mutationRequest
	response apicontract.JobMutationResponse
	err      error
}
type cancelMsg struct {
	request  mutationRequest
	response apicontract.JobResponse
	err      error
}
type logoutMsg struct{ err error }
type openURLMsg struct{ err error }
type followOpenedMsg struct {
	generation uint64
	jobID      string
	events     <-chan apicontract.JobEvent
	errors     <-chan error
}
type followEventMsg struct {
	generation uint64
	event      apicontract.JobEvent
	err        error
	done       bool
}
type jobSnapshotMsg struct {
	generation uint64
	job        apicontract.Job
	streamDone bool
	err        error
}

func (m *Model) checkBootstrap() tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg { value, err := client.BootstrapStatus(ctx); return bootstrapStatusMsg{value, err} }
}
func (m *Model) checkMe() tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg { value, err := client.Me(ctx); return meMsg{value, err} }
}
func (m *Model) startOverview() tea.Cmd {
	if m.overviewLoading {
		return nil
	}
	m.overviewGen++
	m.overviewLoading = true
	generation, client, ctx := m.overviewGen, m.client, m.ctx
	return func() tea.Msg {
		requestCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var status apicontract.SystemStatus
		var apps apicontract.ApplicationList
		var jobs apicontract.JobList
		var wg sync.WaitGroup
		errCh := make(chan error, 3)
		run := func(fn func(context.Context) error) {
			defer wg.Done()
			if err := fn(requestCtx); err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}
		wg.Add(3)
		go run(func(c context.Context) (err error) { status, err = client.Status(c); return err })
		go run(func(c context.Context) (err error) { apps, err = client.Applications(c); return err })
		go run(func(c context.Context) (err error) { jobs, err = client.Jobs(c); return err })
		wg.Wait()
		select {
		case err := <-errCh:
			return overviewMsg{generation: generation, err: err}
		default:
			return overviewMsg{generation: generation, status: status, apps: apps, jobs: jobs}
		}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout = calculateLayout(m.width, m.height)
		m.resizeAuthInputs()
		m.reconcileOffset()
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
		return m, m.checkMe()
	case meMsg:
		if msg.err != nil {
			if isUnauthenticated(msg.err) {
				m.showLogin("")
			} else {
				m.goOffline(msg.err)
			}
			return m, nil
		}
		m.enterSwitchboard(msg.me.User)
		return m, m.startOverview()
	case authMsg:
		m.mutationBusy = false
		if msg.err != nil {
			m.err = sanitizeAPIText(msg.err.Error())
			if m.screen == screenLogin {
				m.clearAuthValues()
			}
			if len(m.authInputs) > 0 {
				m.focusAuth(0)
			}
			return m, nil
		}
		m.enterSwitchboard(msg.session.User)
		return m, m.startOverview()
	case overviewMsg:
		if msg.generation != m.overviewGen {
			return m, nil
		}
		m.overviewLoading = false
		if msg.err != nil {
			if isUnauthenticated(msg.err) {
				m.showLogin("Session expired. Sign in again.")
				return m, m.clearRemoteSession()
			}
			if m.overviewLoaded {
				m.err = "Refresh failed: " + sanitizeAPIText(msg.err.Error())
				return m, nil
			}
			m.goOffline(msg.err)
			return m, nil
		}
		oldIndex := m.selectedIndex()
		m.status = msg.status
		m.apps = sortedApps(msg.apps.Items)
		m.jobs = append([]apicontract.Job(nil), msg.jobs.Items...)
		m.overviewLoaded = true
		m.err = ""
		m.reconcileSelection(oldIndex)
		m.syncFollowSnapshot()
		return m, nil
	case mutationMsg:
		m.mutationBusy = false
		if msg.err != nil {
			return m, m.operationError(msg.err, screenActions)
		}
		m.mergeJob(msg.response.Job)
		m.followedJob = msg.response.Job
		m.followedJobID = msg.response.Job.ID
		m.screen = screenJobProgress
		m.confirmation = nil
		m.err = ""
		return m, m.startFollowing(msg.response.Job.ID, 0, true)
	case cancelMsg:
		m.mutationBusy = false
		m.confirmation = nil
		if msg.err != nil {
			return m, m.operationError(msg.err, screenJobProgress)
		}
		m.mergeJob(msg.response.Job)
		m.followedJob = msg.response.Job
		m.followedJobID = msg.response.Job.ID
		m.screen = screenJobProgress
		m.err = "Cancellation requested; waiting for the controller job to finish."
		if m.followCancel == nil && !isFollowTerminal(msg.response.Job) {
			return m, m.startFollowing(msg.response.Job.ID, m.followCursor, false)
		}
		return m, nil
	case logoutMsg:
		m.mutationBusy = false
		m.confirmation = nil
		m.showLogin("Signed out.")
		if msg.err != nil {
			m.err = sanitizeAPIText(msg.err.Error())
		}
		return m, nil
	case openURLMsg:
		if msg.err != nil {
			m.err = "Could not open dashboard: " + sanitizeAPIText(msg.err.Error())
		} else {
			m.err = "Dashboard opened in your browser."
		}
		return m, nil
	case followOpenedMsg:
		if msg.generation != m.followGeneration {
			return m, nil
		}
		m.followedJobID, m.followEvents, m.followErrors = msg.jobID, msg.events, msg.errors
		return m, m.waitForFollow()
	case followEventMsg:
		if msg.generation != m.followGeneration {
			return m, nil
		}
		if msg.err != nil {
			m.err = "Live updates paused: " + sanitizeAPIText(msg.err.Error())
			return m, m.fetchFollowedJob(msg.generation, true)
		}
		if msg.done {
			return m, m.fetchFollowedJob(msg.generation, true)
		}
		if msg.event.ID > m.followCursor {
			m.followCursor = msg.event.ID
		}
		m.recentEvents = appendBoundedEvent(m.recentEvents, msg.event)
		m.phases = updatePhases(m.phases, msg.event.Phase)
		return m, m.fetchFollowedJob(msg.generation, false)
	case jobSnapshotMsg:
		if msg.generation != m.followGeneration {
			return m, nil
		}
		if msg.err != nil {
			if isUnauthenticated(msg.err) {
				m.showLogin("Session expired. Sign in again.")
				return m, m.clearRemoteSession()
			}
			m.err = "Could not refresh operation: " + sanitizeAPIText(msg.err.Error())
			if msg.streamDone {
				m.stopFollowing()
			}
			return m, nil
		}
		m.followedJob = msg.job
		m.mergeJob(msg.job)
		if isFollowTerminal(msg.job) {
			m.stopFollowing()
			m.result = resultFor(msg.job, m.appByID(msg.job.ResourceID))
			m.screen = screenResult
			// A terminal snapshot must supersede any advisory refresh already
			// in flight so its result cannot remain stale.
			m.overviewLoading = false
			return m, m.startOverview()
		}
		if msg.streamDone {
			return m, m.startFollowing(msg.job.ID, m.followCursor, false)
		}
		return m, m.waitForFollow()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	if (m.screen == screenBootstrap || m.screen == screenLogin) && !m.bootstrapConfirm && !m.mutationBusy && len(m.authInputs) > 0 {
		var cmd tea.Cmd
		m.authInputs[m.authIndex], cmd = m.authInputs[m.authIndex].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := key.String()
	if k == "ctrl+c" {
		m.stopFollowing()
		return m, tea.Quit
	}
	switch m.screen {
	case screenOffline:
		if k == "enter" || k == "r" {
			m.screen, m.err = screenLoading, ""
			return m, m.checkBootstrap()
		}
		if k == "q" {
			m.confirmation = &confirmation{Action: actionQuit, ReturnScreen: screenOffline}
			m.screen = screenConfirmation
		}
	case screenBootstrap, screenLogin:
		return m.handleAuthKey(key)
	case screenSwitchboard:
		return m.handleSwitchboardKey(k)
	case screenActions:
		return m.handleActionsKey(k)
	case screenConfirmation:
		return m.handleConfirmationKey(k)
	case screenJobProgress:
		return m.handleProgressKey(k)
	case screenResult:
		if k == "enter" || k == "esc" {
			m.screen, m.result, m.err = screenSwitchboard, nil, ""
			return m, nil
		}
		return m.handleCommonKey(k, screenResult)
	case screenHelp:
		if k == "esc" || k == "?" || k == "enter" {
			m.screen = m.returnScreen
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) handleAuthKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mutationBusy {
		return m, nil
	}
	k := key.String()
	if m.screen == screenBootstrap && m.bootstrapConfirm {
		if k == "enter" {
			return m.submitAuth()
		}
		if k == "esc" {
			m.cancelBootstrapConfirmation()
		}
		return m, nil
	}
	if len(m.authInputs) == 0 {
		return m, nil
	}
	switch k {
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
		if m.screen == screenBootstrap {
			m.beginBootstrapConfirmation()
			return m, nil
		}
		return m.submitAuth()
	}
	var cmd tea.Cmd
	m.authInputs[m.authIndex], cmd = m.authInputs[m.authIndex].Update(key)
	return m, cmd
}

func (m *Model) handleSwitchboardKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.moveSelection(-1)
	case "down", "j":
		m.moveSelection(1)
	case "pgup":
		m.moveSelection(-max(1, m.layout.listRows))
	case "pgdown":
		m.moveSelection(max(1, m.layout.listRows))
	case "home":
		m.selectIndex(0)
	case "end":
		m.selectIndex(len(m.apps) - 1)
	case "enter":
		if _, ok := m.selectedApp(); ok {
			m.selectedAction = 0
			m.screen = screenActions
		}
	case "r":
		return m, m.startOverview()
	default:
		return m.handleCommonKey(k, screenSwitchboard)
	}
	return m, nil
}

func (m *Model) handleActionsKey(k string) (tea.Model, tea.Cmd) {
	app, ok := m.selectedApp()
	if !ok {
		m.screen = screenSwitchboard
		return m, nil
	}
	actions := actionsFor(app, relevantJob(app.ID, m.jobs), m.status)
	switch k {
	case "esc":
		m.screen = screenSwitchboard
	case "up", "k":
		m.selectedAction = wrapIndex(m.selectedAction-1, len(actions))
	case "down", "j":
		m.selectedAction = wrapIndex(m.selectedAction+1, len(actions))
	case "home":
		m.selectedAction = 0
	case "end":
		m.selectedAction = max(0, len(actions)-1)
	case "enter":
		if len(actions) == 0 {
			return m, nil
		}
		item := actions[m.selectedAction]
		if !item.Enabled {
			m.err = item.DisabledBy
			return m, nil
		}
		return m.chooseAction(item, app)
	default:
		return m.handleCommonKey(k, screenActions)
	}
	return m, nil
}

func (m *Model) chooseAction(item actionItem, app apicontract.Application) (tea.Model, tea.Cmd) {
	switch item.Kind {
	case actionBack:
		m.screen = screenSwitchboard
	case actionOpenDashboard:
		return m, m.openDashboard()
	case actionViewCurrent, actionViewLast:
		job := relevantJob(app.ID, m.jobs)
		if job == nil {
			return m, nil
		}
		alreadyFollowing := m.followCancel != nil && m.followedJobID == job.ID
		m.followedJob, m.followedJobID = *job, job.ID
		if isFollowTerminal(*job) {
			m.result = resultFor(*job, app)
			m.screen = screenResult
			return m, nil
		}
		m.screen = screenJobProgress
		if !alreadyFollowing {
			return m, m.startFollowing(job.ID, 0, true)
		}
	default:
		confirmation := &confirmation{Action: item.Kind, App: app, ReturnScreen: screenActions}
		if item.Kind == actionCancelJob {
			if job := relevantJob(app.ID, m.jobs); job != nil {
				confirmation.Job = *job
			}
		}
		m.confirmation = confirmation
		m.screen = screenConfirmation
	}
	return m, nil
}

func (m *Model) handleConfirmationKey(k string) (tea.Model, tea.Cmd) {
	if m.confirmation == nil {
		m.screen = screenSwitchboard
		return m, nil
	}
	if m.mutationBusy {
		return m, nil
	}
	if k == "esc" {
		m.screen = m.confirmation.ReturnScreen
		m.confirmation = nil
		m.err = ""
		return m, nil
	}
	if k != "enter" {
		return m, nil
	}
	c := *m.confirmation
	if c.Action == actionQuit {
		m.stopFollowing()
		return m, tea.Quit
	}
	if c.Action == actionLogout {
		m.mutationBusy = true
		client, ctx := m.client, m.ctx
		return m, func() tea.Msg { return logoutMsg{err: client.Logout(ctx)} }
	}
	key, err := m.mutationKey()
	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	request := mutationRequest{Action: c.Action, AppID: c.App.ID, JobID: c.Job.ID, IdempotencyKey: key}
	m.mutationBusy = true
	if c.Action == actionCancelJob {
		return m, m.cancelJob(request)
	}
	return m, m.runMutation(request)
}

func (m *Model) handleProgressKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc":
		m.screen = screenSwitchboard
		m.err = "Operation continues; live updates remain active."
	case "c":
		if m.followedJobID != "" && isActiveJob(m.followedJob) {
			m.confirmation = &confirmation{Action: actionCancelJob, App: m.appByID(m.followedJob.ResourceID), Job: m.followedJob, ReturnScreen: screenJobProgress}
			m.screen = screenConfirmation
		}
	default:
		return m.handleCommonKey(k, screenJobProgress)
	}
	return m, nil
}
func (m *Model) handleCommonKey(k string, from screen) (tea.Model, tea.Cmd) {
	switch k {
	case "?":
		m.returnScreen, m.screen = from, screenHelp
	case "o":
		return m, m.openDashboard()
	case "l":
		m.confirmation = &confirmation{Action: actionLogout, ReturnScreen: from}
		m.screen = screenConfirmation
	case "q":
		m.confirmation = &confirmation{Action: actionQuit, ReturnScreen: from}
		m.screen = screenConfirmation
	}
	return m, nil
}

func (m *Model) runMutation(request mutationRequest) tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		var response apicontract.JobMutationResponse
		var err error
		if request.Action == actionDeploy {
			response, err = client.Deploy(ctx, request.AppID, request.IdempotencyKey)
		} else {
			response, err = client.Lifecycle(ctx, request.AppID, strings.ToLower(actionVerb(request.Action)), request.IdempotencyKey)
		}
		return mutationMsg{request: request, response: response, err: err}
	}
}
func (m *Model) cancelJob(request mutationRequest) tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		response, err := client.CancelJob(ctx, request.JobID, request.IdempotencyKey)
		return cancelMsg{request: request, response: response, err: err}
	}
}
func (m *Model) openDashboard() tea.Cmd {
	if m.openURL == nil {
		m.err = "Dashboard opener is unavailable."
		return nil
	}
	opener, ctx, endpoint := m.openURL, m.ctx, m.endpoint
	return func() tea.Msg { return openURLMsg{err: opener(ctx, endpoint)} }
}

func (m *Model) startFollowing(jobID string, after int64, reset bool) tea.Cmd {
	if m.followCancel != nil {
		m.followCancel()
	}
	m.followGeneration++
	generation := m.followGeneration
	ctx, cancel := context.WithCancel(m.ctx)
	m.followCancel, m.followContext = cancel, ctx
	m.followedJobID, m.followCursor = jobID, after
	if reset {
		m.recentEvents, m.phases = nil, nil
	}
	client := m.client
	return func() tea.Msg {
		events, errs := client.FollowJob(ctx, jobID, after)
		return followOpenedMsg{generation: generation, jobID: jobID, events: events, errors: errs}
	}
}
func (m *Model) waitForFollow() tea.Cmd {
	events, errs, ctx, generation := m.followEvents, m.followErrors, m.followContext, m.followGeneration
	if ctx == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return followEventMsg{generation: generation, done: true}
		case event, ok := <-events:
			if !ok {
				return followEventMsg{generation: generation, done: true}
			}
			return followEventMsg{generation: generation, event: event}
		case err, ok := <-errs:
			if ok && err != nil {
				return followEventMsg{generation: generation, err: err}
			}
			return followEventMsg{generation: generation, done: true}
		}
	}
}
func (m *Model) fetchFollowedJob(generation uint64, streamDone bool) tea.Cmd {
	client, ctx, jobID := m.client, m.ctx, m.followedJobID
	return func() tea.Msg {
		job, err := client.Job(ctx, jobID)
		return jobSnapshotMsg{generation: generation, job: job, streamDone: streamDone, err: err}
	}
}
func (m *Model) stopFollowing() {
	if m.followCancel != nil {
		m.followCancel()
	}
	m.followCancel, m.followContext, m.followEvents, m.followErrors = nil, nil, nil, nil
	m.followGeneration++
}

func (m *Model) submitAuth() (tea.Model, tea.Cmd) {
	if m.mutationBusy {
		return m, nil
	}
	for i, input := range m.authInputs {
		if strings.TrimSpace(input.Value()) == "" {
			m.err = authFieldLabel(m.screen, i) + " is required."
			m.focusAuth(i)
			return m, nil
		}
	}
	m.mutationBusy, m.err, m.bootstrapConfirm, m.bootstrapUsername = true, "", false, ""
	client, ctx := m.client, m.ctx
	if m.screen == screenLogin {
		request := apicontract.LoginRequest{Username: m.authInputs[0].Value(), Passphrase: m.authInputs[1].Value()}
		m.clearAuthValues()
		return m, func() tea.Msg { v, err := client.Login(ctx, request); return authMsg{v, err} }
	}
	request := apicontract.BootstrapRequest{Token: m.authInputs[0].Value(), Username: m.authInputs[1].Value(), Passphrase: m.authInputs[2].Value()}
	m.clearAuthValues()
	return m, func() tea.Msg { v, err := client.Bootstrap(ctx, request); return authMsg{v, err} }
}
func (m *Model) showBootstrap() {
	m.screen, m.mutationBusy, m.err, m.bootstrapConfirm = screenBootstrap, false, "", false
	m.authInputs = []textinput.Model{authInput("bootstrap token", true), authInput("admin username", false), authInput("passphrase", true)}
	m.resizeAuthInputs()
	m.focusAuth(0)
}
func (m *Model) beginBootstrapConfirmation() {
	for i, input := range m.authInputs {
		if strings.TrimSpace(input.Value()) == "" {
			m.err = authFieldLabel(m.screen, i) + " is required."
			m.focusAuth(i)
			return
		}
	}
	m.bootstrapConfirm, m.bootstrapUsername, m.err = true, sanitizeAPIText(m.authInputs[1].Value()), ""
	for i := range m.authInputs {
		m.authInputs[i].Blur()
	}
}
func (m *Model) cancelBootstrapConfirmation() {
	m.bootstrapConfirm, m.bootstrapUsername = false, ""
	m.clearAuthValues()
	m.focusAuth(0)
}
func (m *Model) showLogin(message string) {
	m.stopFollowing()
	m.screen, m.mutationBusy, m.err = screenLogin, false, sanitizeAPIText(message)
	m.authInputs = []textinput.Model{authInput("username", false), authInput("passphrase", true)}
	m.resizeAuthInputs()
	m.focusAuth(0)
}
func authInput(placeholder string, secret bool) textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 512
	if secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	return input
}
func (m *Model) compactAuthLayout() bool { return m.width < 60 || m.height < 18 }
func (m *Model) resizeAuthInputs() {
	if len(m.authInputs) == 0 {
		return
	}
	width := m.width
	if width <= 0 {
		width = 68
	}
	for i := range m.authInputs {
		if m.compactAuthLayout() {
			m.authInputs[i].Width = max(6, width-len(authFieldLabel(m.screen, i))-3)
		} else {
			m.authInputs[i].Width = max(12, min(width-8, 64))
		}
	}
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
func (m *Model) mutationKey() (string, error) {
	key := m.newKey()
	if key == "" {
		return "", errors.New("could not generate a secure idempotency key; no change was made")
	}
	return key, nil
}

func (m *Model) enterSwitchboard(user apicontract.User) {
	m.screen, m.user, m.err, m.mutationBusy, m.overviewLoaded, m.overviewLoading = screenSwitchboard, user, "", false, false, false
	m.confirmation, m.result = nil, nil
}
func (m *Model) goOffline(err error) {
	m.stopFollowing()
	m.screen, m.mutationBusy, m.err = screenOffline, false, sanitizeAPIText(err.Error())
}
func (m *Model) clearRemoteSession() tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg { _ = client.Logout(ctx); return nil }
}
func (m *Model) operationError(err error, fallback screen) tea.Cmd {
	if isUnauthenticated(err) {
		m.showLogin("Session expired. Sign in again.")
		return m.clearRemoteSession()
	}
	m.err = sanitizeAPIText(err.Error())
	m.screen, m.confirmation = fallback, nil
	return nil
}

func sortedApps(items []apicontract.Application) []apicontract.Application {
	out := append([]apicontract.Application(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		a, b = strings.ToLower(out[i].Slug), strings.ToLower(out[j].Slug)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func (m *Model) selectedIndex() int {
	for i := range m.apps {
		if m.apps[i].ID == m.selectedAppID {
			return i
		}
	}
	return -1
}
func (m *Model) reconcileSelection(fallback int) {
	if i := m.selectedIndex(); i >= 0 {
		m.reconcileOffset()
		return
	}
	if len(m.apps) == 0 {
		m.selectedAppID = ""
		m.listOffset = 0
		return
	}
	if fallback < 0 {
		fallback = 0
	}
	if fallback >= len(m.apps) {
		fallback = len(m.apps) - 1
	}
	m.selectedAppID = m.apps[fallback].ID
	m.reconcileOffset()
}
func (m *Model) reconcileOffset() {
	index := m.selectedIndex()
	if index < 0 {
		m.listOffset = 0
		return
	}
	capacity := max(1, m.layout.listRows)
	if index < m.listOffset {
		m.listOffset = index
	}
	if index >= m.listOffset+capacity {
		m.listOffset = index - capacity + 1
	}
	maxOffset := max(0, len(m.apps)-capacity)
	if m.listOffset > maxOffset {
		m.listOffset = maxOffset
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}
func (m *Model) moveSelection(delta int) {
	if len(m.apps) == 0 {
		return
	}
	index := m.selectedIndex()
	if index < 0 {
		index = 0
	}
	m.selectIndex(index + delta)
}
func (m *Model) selectIndex(index int) {
	if len(m.apps) == 0 {
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(m.apps) {
		index = len(m.apps) - 1
	}
	m.selectedAppID = m.apps[index].ID
	m.reconcileOffset()
}
func (m *Model) selectedApp() (apicontract.Application, bool) {
	for _, app := range m.apps {
		if app.ID == m.selectedAppID {
			return app, true
		}
	}
	return apicontract.Application{}, false
}
func (m *Model) appByID(id string) apicontract.Application {
	for _, app := range m.apps {
		if app.ID == id {
			return app
		}
	}
	return apicontract.Application{ID: id, Name: "Application"}
}
func (m *Model) mergeJob(job apicontract.Job) {
	for i := range m.jobs {
		if m.jobs[i].ID == job.ID {
			m.jobs[i] = job
			return
		}
	}
	m.jobs = append([]apicontract.Job{job}, m.jobs...)
}
func (m *Model) syncFollowSnapshot() {
	if m.followedJobID == "" {
		return
	}
	for _, job := range m.jobs {
		if job.ID == m.followedJobID {
			m.followedJob = job
			return
		}
	}
}
func wrapIndex(index, length int) int {
	if length <= 0 {
		return 0
	}
	for index < 0 {
		index += length
	}
	return index % length
}

func resultFor(job apicontract.Job, app apicontract.Application) *resultState {
	name := displayAppName(app)
	op := titleWord(job.Type)
	title := op + " completed"
	detail := name + " completed successfully."
	switch strings.ToLower(job.Status) {
	case "failed", "interrupted":
		title = op + " failed"
		detail = sanitizeAPIText(job.ErrorDetail)
		if detail == "" {
			detail = "The operation did not complete. Open the dashboard for full details."
		}
	case "cancelled":
		title = op + " cancelled"
		detail = "The controller job was cancelled."
	case "waiting_user":
		title = op + " needs approval"
		detail = "Approval review is available in the web dashboard."
	case "needs_attention":
		title = op + " needs attention"
		detail = sanitizeAPIText(job.ErrorDetail)
		if detail == "" {
			detail = "Continue in the web dashboard."
		}
	}
	return &resultState{AppID: app.ID, JobID: job.ID, Title: title, Detail: detail}
}
func randomIdempotencyKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "tui-" + hex.EncodeToString(b)
}
func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
