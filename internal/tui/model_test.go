package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hostd/hostd/internal/apicontract"
)

type fakeClient struct {
	mu                                                                          sync.Mutex
	bootstrapStatus                                                             apicontract.BootstrapStatus
	bootstrapStatusErr                                                          error
	me                                                                          apicontract.MeResponse
	meErr                                                                       error
	status                                                                      apicontract.SystemStatus
	apps                                                                        apicontract.ApplicationList
	jobs                                                                        apicontract.JobList
	overviewErr                                                                 error
	job                                                                         apicontract.Job
	jobsByID                                                                    map[string]apicontract.Job
	jobErr                                                                      error
	mutationJob                                                                 apicontract.Job
	cancelJobResponse                                                           apicontract.Job
	deployCalls, lifecycleCalls, cancelCalls, logoutCalls, clearCalls, jobCalls int
	lastApp, lastAction, lastKey, lastJob                                       string
	lastClearGeneration                                                         uint64
	followCtx                                                                   context.Context
	followEvents                                                                chan apicontract.JobEvent
	followErrors                                                                chan error
}

func (f *fakeClient) BootstrapStatus(context.Context) (apicontract.BootstrapStatus, error) {
	return f.bootstrapStatus, f.bootstrapStatusErr
}
func (f *fakeClient) Bootstrap(_ context.Context, r apicontract.BootstrapRequest) (apicontract.SessionResponse, error) {
	return apicontract.SessionResponse{User: apicontract.User{Username: r.Username}}, nil
}
func (f *fakeClient) Login(_ context.Context, r apicontract.LoginRequest) (apicontract.SessionResponse, error) {
	return apicontract.SessionResponse{User: apicontract.User{Username: r.Username}}, nil
}
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalls++
	return nil
}
func (f *fakeClient) ClearSession(_ context.Context, generation uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalls++
	f.lastClearGeneration = generation
	return nil
}
func (f *fakeClient) Me(context.Context) (apicontract.MeResponse, error) { return f.me, f.meErr }
func (f *fakeClient) Status(context.Context) (apicontract.SystemStatus, error) {
	return f.status, f.overviewErr
}
func (f *fakeClient) Applications(context.Context) (apicontract.ApplicationList, error) {
	return f.apps, f.overviewErr
}
func (f *fakeClient) Deploy(_ context.Context, app, key string) (apicontract.JobMutationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployCalls++
	f.lastApp, f.lastKey = app, key
	job := f.mutationJob
	if job.ID == "" {
		job = activeJob("deploy-job", app, "deploy")
	}
	return apicontract.JobMutationResponse{Job: job}, nil
}
func (f *fakeClient) Lifecycle(_ context.Context, app, action, key string) (apicontract.JobMutationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lifecycleCalls++
	f.lastApp, f.lastAction, f.lastKey = app, action, key
	return apicontract.JobMutationResponse{Job: activeJob(action+"-job", app, action)}, nil
}
func (f *fakeClient) Jobs(context.Context) (apicontract.JobList, error) { return f.jobs, f.overviewErr }
func (f *fakeClient) Job(_ context.Context, id string) (apicontract.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobCalls++
	f.lastJob = id
	if job, ok := f.jobsByID[id]; ok {
		return job, f.jobErr
	}
	return f.job, f.jobErr
}
func (f *fakeClient) FollowJob(ctx context.Context, _ string, _ int64) (<-chan apicontract.JobEvent, <-chan error) {
	f.followCtx = ctx
	if f.followEvents == nil {
		f.followEvents = make(chan apicontract.JobEvent)
	}
	if f.followErrors == nil {
		f.followErrors = make(chan error)
	}
	return f.followEvents, f.followErrors
}
func (f *fakeClient) CancelJob(_ context.Context, id, key string) (apicontract.JobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.lastJob, f.lastKey = id, key
	job := f.cancelJobResponse
	if job.ID == "" {
		job = f.job
	}
	if job.ID == "" {
		job = activeJob(id, "app-a", "deploy")
	}
	return apicontract.JobResponse{Job: job}, nil
}
func (f *fakeClient) ResumeJob(context.Context, string, string) (apicontract.JobResponse, error) {
	return apicontract.JobResponse{}, errors.New("not exposed by switchboard")
}

func runningApp(id, name string) apicontract.Application {
	return apicontract.Application{ID: id, Name: name, Slug: strings.ToLower(name), Status: "running", MachineName: "local", Source: apicontract.SourceSummary{Type: "github", RepositoryOwner: "owner", RepositoryName: name, TrackedBranch: "main", ResolvedSha: "1234567890abcdef"}}
}
func activeJob(id, appID, kind string) apicontract.Job {
	return apicontract.Job{ID: id, ResourceType: "application", ResourceID: appID, Type: kind, Status: "running", Phase: "apply", Progress: 42, UpdatedAt: "2026-08-30T12:00:00Z"}
}
func readyStatus() apicontract.SystemStatus {
	return apicontract.SystemStatus{Daemon: "running", Capabilities: apicontract.Capabilities{FakeRuntime: true}, Diagnostics: apicontract.Diagnostics{EngineReady: true}}
}
func switchboardModel(client *fakeClient) *Model {
	m := NewModel(context.Background(), client, "http://127.0.0.1:7345")
	m.width, m.height = 80, 24
	m.layout = calculateLayout(80, 24)
	m.screen = screenSwitchboard
	m.status = readyStatus()
	m.overviewLoaded = true
	m.apps = []apicontract.Application{runningApp("app-a", "Alpha"), runningApp("app-b", "Beta")}
	m.selectedAppID = "app-a"
	m.newKey = func() string { return "test-key" }
	return m
}
func key(value string) tea.KeyMsg {
	keyType, ok := map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown}[value]
	if !ok {
		keyType = tea.KeyRunes
	}
	return tea.KeyMsg{Type: keyType, Runes: func() []rune {
		if len(value) == 1 {
			return []rune(value)
		}
		return nil
	}()}
}

func TestStartupOfflineBootstrapLoginAndOverview(t *testing.T) {
	f := &fakeClient{bootstrapStatus: apicontract.BootstrapStatus{BootstrapRequired: true}}
	m := NewModel(context.Background(), f, "http://127.0.0.1:7345")
	msg := m.Init()().(bootstrapStatusMsg)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(msg)
	if m.screen != screenBootstrap {
		t.Fatalf("screen=%v", m.screen)
	}
	m.Update(bootstrapStatusMsg{err: errors.New("down")})
	if m.screen != screenOffline || !strings.Contains(m.View(), "Controller unavailable") {
		t.Fatalf("offline view=%q", m.View())
	}
	f.bootstrapStatus = apicontract.BootstrapStatus{}
	f.me = apicontract.MeResponse{User: apicontract.User{Username: "admin"}}
	_, cmd := m.Update(key("enter"))
	msg2 := cmd().(bootstrapStatusMsg)
	_, cmd = m.Update(msg2)
	me := cmd().(meMsg)
	_, cmd = m.Update(me)
	if m.screen != screenSwitchboard || cmd == nil {
		t.Fatalf("screen=%v cmd=%v", m.screen, cmd)
	}
}

func TestOverviewSortSelectionGenerationAndAdvisoryFailure(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.selectedAppID = "app-b"
	m.overviewGen = 2
	m.overviewLoading = true
	m.Update(overviewMsg{generation: 1, apps: apicontract.ApplicationList{Items: []apicontract.Application{runningApp("x", "Stale")}}})
	if len(m.apps) != 2 {
		t.Fatal("stale overview applied")
	}
	m.Update(overviewMsg{generation: 2, status: readyStatus(), apps: apicontract.ApplicationList{Items: []apicontract.Application{runningApp("app-b", "zeta"), runningApp("app-a", "Alpha")}}})
	if m.apps[0].ID != "app-a" || m.selectedAppID != "app-b" {
		t.Fatalf("apps=%v selected=%s", m.apps, m.selectedAppID)
	}
	m.overviewGen = 3
	m.overviewLoading = true
	m.Update(overviewMsg{generation: 3, err: errors.New("temporary")})
	if len(m.apps) != 2 || !strings.Contains(m.err, "Refresh failed") {
		t.Fatalf("data erased or err missing: %q", m.err)
	}
}

func TestSelectionScrollingAndResize(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.apps = nil
	for i := 0; i < 20; i++ {
		m.apps = append(m.apps, runningApp(string(rune('a'+i)), "App "+string(rune('a'+i))))
	}
	m.selectedAppID = m.apps[0].ID
	m.layout = calculateLayout(50, 12)
	m.selectIndex(15)
	if m.listOffset == 0 || m.selectedIndex() != 15 {
		t.Fatalf("index=%d offset=%d", m.selectedIndex(), m.listOffset)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if m.selectedIndex() < m.listOffset || m.selectedIndex() >= m.listOffset+m.layout.listRows {
		t.Fatalf("selection not visible")
	}
	m.moveSelection(99)
	if m.selectedIndex() != len(m.apps)-1 {
		t.Fatal("selection escaped end")
	}
	m.moveSelection(-99)
	if m.selectedIndex() != 0 {
		t.Fatal("selection escaped start")
	}
}

func TestActionPolicyIsCapabilityAuthoritative(t *testing.T) {
	app := runningApp("a", "A")
	tests := []struct {
		name                         string
		status                       apicontract.SystemStatus
		appStatus                    string
		job                          *apicontract.Job
		deploy, start, stop, restart bool
	}{
		{"fake running", readyStatus(), "running", nil, true, false, true, true},
		{"fake stopped", readyStatus(), "stopped", nil, true, true, false, false},
		{"compose running", apicontract.SystemStatus{Capabilities: apicontract.Capabilities{ComposeRuntime: true}, Diagnostics: apicontract.Diagnostics{EngineReady: true, ComposeAvailable: true}}, "running", nil, true, false, false, false},
		{"none", apicontract.SystemStatus{}, "running", nil, false, false, false, false},
		{"active", readyStatus(), "running", func() *apicontract.Job { j := activeJob("j", "a", "deploy"); return &j }(), false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app.Status = tt.appStatus
			items := actionsFor(app, tt.job, tt.status)
			got := map[actionKind]bool{}
			for _, item := range items {
				got[item.Kind] = item.Enabled
				if !item.Enabled && item.DisabledBy == "" {
					t.Fatalf("disabled %s lacks reason", item.Label)
				}
			}
			if got[actionDeploy] != tt.deploy || got[actionStart] != tt.start || got[actionStop] != tt.stop || got[actionRestart] != tt.restart {
				t.Fatalf("policy=%v", got)
			}
			if tt.job != nil && (!got[actionViewCurrent] || !got[actionCancelJob]) {
				t.Fatalf("active operation actions=%v", got)
			}
		})
	}
}

func TestJobCorrelationBoundsAndTerminalClassification(t *testing.T) {
	old := apicontract.Job{ID: "old", ResourceType: "application", ResourceID: "a", Status: "failed", UpdatedAt: "2026-08-30T12:00:00Z"}
	active := activeJob("active", "a", "deploy")
	active.UpdatedAt = "bad"
	other := activeJob("other", "b", "deploy")
	if got := relevantJob("a", []apicontract.Job{old, other, active}); got == nil || got.ID != "active" {
		t.Fatalf("job=%v", got)
	}
	events := []apicontract.JobEvent{}
	for i := 0; i < 100; i++ {
		events = appendBoundedEvent(events, apicontract.JobEvent{ID: int64(i), Message: strings.Repeat("x", 4096), Phase: "phase"})
	}
	if len(events) > maxRecentEvents {
		t.Fatalf("events=%d", len(events))
	}
	phases := []phaseState{}
	for i := 0; i < 30; i++ {
		phases = updatePhases(phases, "phase-"+time.Unix(int64(i), 0).Format("05"))
	}
	if len(phases) > maxPhases {
		t.Fatalf("phases=%d", len(phases))
	}
	for _, status := range []string{"succeeded", "failed", "cancelled", "interrupted", "needs_attention"} {
		if !isTerminalJob(apicontract.Job{Status: status}) {
			t.Fatalf("%s not terminal", status)
		}
	}
	if !isFollowTerminal(apicontract.Job{Status: "waiting_user"}) {
		t.Fatal("waiting_user must hand off")
	}
}

func TestConfirmationSnapshotsTargetEscapesAndPreventsDuplicate(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0], ReturnScreen: screenActions}
	m.screen = screenConfirmation
	m.Update(key("esc"))
	if f.deployCalls != 0 || m.screen != screenActions {
		t.Fatalf("escape mutated or wrong screen")
	}
	m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0], ReturnScreen: screenActions}
	m.screen = screenConfirmation
	m.selectedAppID = "app-b"
	_, cmd := m.Update(key("enter"))
	if cmd == nil || !m.mutationBusy {
		t.Fatal("confirmation did not submit")
	}
	_, duplicate := m.Update(key("enter"))
	if duplicate != nil {
		t.Fatal("duplicate submit returned command")
	}
	msg := cmd().(mutationMsg)
	if msg.request.AppID != "app-a" {
		t.Fatalf("retargeted to %s", msg.request.AppID)
	}
	m.Update(msg)
	if f.deployCalls != 1 || f.lastApp != "app-a" || f.lastKey != "test-key" {
		t.Fatalf("calls=%d app=%s key=%s", f.deployCalls, f.lastApp, f.lastKey)
	}
}

func TestSecureKeyFailurePreventsMutation(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	m.newKey = func() string { return "" }
	m.confirmation = &confirmation{Action: actionStop, App: m.apps[0], ReturnScreen: screenActions}
	m.screen = screenConfirmation
	_, cmd := m.Update(key("enter"))
	if cmd != nil || f.lifecycleCalls != 0 || !strings.Contains(m.err, "secure idempotency") {
		t.Fatalf("cmd=%v calls=%d err=%q", cmd, f.lifecycleCalls, m.err)
	}
}

func TestLifecycleRoutesTypedActions(t *testing.T) {
	for _, action := range []actionKind{actionStart, actionStop, actionRestart} {
		t.Run(actionVerb(action), func(t *testing.T) {
			f := &fakeClient{}
			m := switchboardModel(f)
			m.confirmation = &confirmation{Action: action, App: m.apps[0]}
			m.screen = screenConfirmation
			_, cmd := m.Update(key("enter"))
			msg := cmd().(mutationMsg)
			if msg.err != nil || f.lastAction != strings.ToLower(actionVerb(action)) {
				t.Fatalf("action=%q err=%v", f.lastAction, msg.err)
			}
		})
	}
}

func TestFollowEscapeContinuesAndCancellationIsSeparate(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-1", "app-a", "deploy")
	f.job = job
	m.followedJob, m.followedJobID = job, job.ID
	m.screen = screenJobProgress
	open := m.startFollowing(job.ID, 0, true)().(followOpenedMsg)
	_, wait := m.Update(open)
	if wait == nil {
		t.Fatal("follow not waiting")
	}
	ctx := f.followCtx
	m.Update(key("esc"))
	select {
	case <-ctx.Done():
		t.Fatal("Escape stopped local follow")
	default:
	}
	if f.cancelCalls != 0 || m.screen != screenSwitchboard {
		t.Fatalf("cancel=%d screen=%v", f.cancelCalls, m.screen)
	}
	m.screen = screenJobProgress
	m.Update(key("c"))
	if m.screen != screenConfirmation {
		t.Fatal("c did not confirm")
	}
	m.Update(key("esc"))
	if f.cancelCalls != 0 {
		t.Fatal("escape cancelled job")
	}
	m.screen = screenJobProgress
	m.Update(key("c"))
	pending := job
	pending.Status, pending.Phase = "waiting_external", "cancelling"
	f.cancelJobResponse = pending
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("cancel command missing")
	}
	msg := cmd().(cancelMsg)
	m.Update(msg)
	if f.cancelCalls != 1 || f.lastJob != "job-1" {
		t.Fatalf("cancel=%d job=%s", f.cancelCalls, f.lastJob)
	}
	m.Update(key("c"))
	if f.cancelCalls != 1 || m.screen != screenJobProgress {
		t.Fatalf("pending cancellation repeated: cancel=%d screen=%v", f.cancelCalls, m.screen)
	}
}

func TestFollowFetchesExactJobAndBuildsTerminalResult(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-1", "app-a", "deploy")
	m.followedJob, m.followedJobID = job, job.ID
	m.followGeneration = 4
	event := apicontract.JobEvent{ID: 8, JobID: job.ID, Phase: "apply", Code: "phase_started", Message: "safe\x1b[31m event"}
	_, cmd := m.Update(followEventMsg{generation: 4, event: event})
	if cmd == nil {
		t.Fatal("event did not fetch exact job")
	}
	f.job = job
	msg := cmd().(jobSnapshotMsg)
	m.Update(msg)
	if f.jobCalls != 1 || m.followedJob.ID != job.ID {
		t.Fatalf("job calls=%d followed=%v", f.jobCalls, m.followedJob)
	}
	job.Status = "waiting_user"
	job.Phase = "approval_required"
	m.followGeneration = 5
	m.followedJobID = job.ID
	f.job = job
	msg = m.fetchFollowedJob(5, false)().(jobSnapshotMsg)
	m.Update(msg)
	if m.screen != screenResult || m.result == nil || !strings.Contains(strings.ToLower(m.result.Detail), "dashboard") {
		t.Fatalf("screen=%v result=%+v", m.screen, m.result)
	}
}

func TestStaleFollowAndSessionExpiryCannotMutateCurrentJob(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	m.followGeneration = 9
	m.followedJob = activeJob("new", "app-a", "deploy")
	m.Update(jobSnapshotMsg{generation: 8, job: apicontract.Job{ID: "stale", Status: "succeeded"}})
	if m.followedJob.ID != "new" || m.screen != screenSwitchboard {
		t.Fatal("stale follow changed model")
	}
	m.screen = screenJobProgress
	m.Update(jobSnapshotMsg{generation: 9, err: &HTTPError{Status: 401, Detail: "expired"}})
	if m.screen != screenLogin || !strings.Contains(m.err, "Session expired") {
		t.Fatalf("screen=%v err=%q", m.screen, m.err)
	}
}

func TestFollowUnauthorizedReturnsToLoginAndClearsExpiredSession(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-a", "app-a", "deploy")
	m.user = apicontract.User{Username: "admin"}
	m.followedJob, m.followedJobID = job, job.ID
	m.followGeneration = 9
	m.followCancel = func() {}
	_, cmd := m.Update(followEventMsg{generation: 9, err: &HTTPError{Status: 401, Detail: "expired", SessionGeneration: 27}})
	if cmd == nil || m.screen != screenLogin || m.followedJobID != "" || len(m.apps) != 0 || m.overviewLoaded || m.user.Username != "" {
		t.Fatalf("expiry state not cleared: screen=%v follow=%q apps=%d loaded=%v user=%q", m.screen, m.followedJobID, len(m.apps), m.overviewLoaded, m.user.Username)
	}
	msg := cmd().(sessionClearedMsg)
	m.Update(msg)
	if f.clearCalls != 1 || f.lastClearGeneration != 27 {
		t.Fatalf("local clear calls=%d generation=%d", f.clearCalls, f.lastClearGeneration)
	}
}

func TestEmptyRuntimeUnknownFailureAndHelpViews(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.apps = nil
	if view := m.View(); !strings.Contains(view, "No applications yet") || !strings.Contains(view, "Open Web") {
		t.Fatalf("empty=%q", view)
	}
	app := runningApp("app-a", "Alpha")
	app.Status = "odd\x1b[31m"
	m.apps = []apicontract.Application{app}
	m.selectedAppID = app.ID
	m.status = apicontract.SystemStatus{}
	m.accessible = true
	view := m.View()
	if !strings.Contains(view, "Unknown (odd)") || strings.Contains(view, "\x1b") {
		t.Fatalf("unknown=%q", view)
	}
	items := actionsFor(app, nil, m.status)
	for _, item := range items {
		if isMutationAction(item.Kind) && item.Enabled {
			t.Fatalf("runtime-unready mutation enabled: %+v", item)
		}
	}
	m.result = resultFor(apicontract.Job{ID: "j", Type: "deploy", Status: "failed", ErrorDetail: "bad\x1b[31m health"}, app)
	m.screen = screenResult
	if view = m.View(); strings.Contains(view, "\x1b") || !strings.Contains(view, "health") {
		t.Fatalf("failure=%q", view)
	}
	m.returnScreen, m.screen = screenSwitchboard, screenHelp
	if view = m.View(); !strings.Contains(view, "never cancels") || strings.Contains(view, "Resume") {
		t.Fatalf("help=%q", view)
	}
}

func TestLogoutAndQuitRequireConfirmation(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	m.Update(key("l"))
	if m.screen != screenConfirmation || m.confirmation.Action != actionLogout {
		t.Fatal("logout did not confirm")
	}
	m.Update(key("esc"))
	if f.logoutCalls != 0 {
		t.Fatal("escaped logout called API")
	}
	m.screen = screenSwitchboard
	m.Update(key("l"))
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if f.logoutCalls != 1 || m.screen != screenLogin {
		t.Fatalf("logout=%d screen=%v", f.logoutCalls, m.screen)
	}
	m.screen = screenSwitchboard
	m.Update(key("q"))
	if m.screen != screenConfirmation || m.confirmation.Action != actionQuit {
		t.Fatal("quit did not confirm")
	}
}

func TestDashboardOpenerSuccessFailureAndSanitization(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	calls := 0
	m.openURL = func(_ context.Context, target string) error {
		calls++
		if target != "http://127.0.0.1:7345" {
			t.Fatalf("target=%s", target)
		}
		return nil
	}
	cmd := m.openDashboard()
	m.Update(cmd())
	if calls != 1 || !strings.Contains(m.err, "opened") {
		t.Fatalf("calls=%d err=%q", calls, m.err)
	}
	now = now.Add(dashboardOpenCooldown)
	m.openURL = func(context.Context, string) error { return errors.New("failed\x1b]0;bad\a") }
	m.Update(m.openDashboard()())
	if strings.Contains(m.err, "\x1b") || !strings.Contains(m.err, "failed") {
		t.Fatalf("unsafe err=%q", m.err)
	}
	if got := sanitizeAPIText("ok\x1b[31m red\rline\b"); strings.Contains(got, "\x1b") || strings.Contains(got, "\b") {
		t.Fatalf("unsafe=%q", got)
	}
}

func TestViewsAtRequiredSizesAreSemanticAndBounded(t *testing.T) {
	sizes := [][2]int{{32, 8}, {40, 10}, {50, 12}, {60, 18}, {80, 24}, {80, 30}, {99, 30}, {100, 30}, {120, 40}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := switchboardModel(&fakeClient{})
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			view := m.View()
			if len(strings.Split(view, "\n")) > size[1] {
				t.Fatalf("height overflow\n%s", view)
			}
			for _, want := range []string{"Rig", "Alpha", "Running"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%dx%d missing %q\n%s", size[0], size[1], want, view)
				}
			}
			for _, old := range []string{"/help", "Tab complete", "Transcript", "Command:", "Resume"} {
				if strings.Contains(view, old) {
					t.Fatalf("old console residue %q", old)
				}
			}
		})
	}
	m := switchboardModel(&fakeClient{})
	m.Update(tea.WindowSizeMsg{Width: 31, Height: 8})
	if !strings.Contains(m.View(), "Terminal too small") {
		t.Fatal("minimum guidance missing")
	}
}

func TestAccessibleModeLabelsSelectionAndProgress(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.accessible = true
	view := m.View()
	if !strings.Contains(view, "Current: Alpha") || !strings.Contains(view, "Status: Running") {
		t.Fatalf("accessible view=%q", view)
	}
	m.followedJob = activeJob("j", "app-a", "deploy")
	m.screen = screenJobProgress
	view = m.View()
	if !strings.Contains(view, "42 percent") || strings.Contains(view, "\x1b") {
		t.Fatalf("progress=%q", view)
	}
}

func TestRunProgramOptionsNeverIncludeMouse(t *testing.T) {
	f := &fakeClient{}
	for _, accessible := range []bool{false, true} {
		count := -1
		err := Run(context.Background(), Config{Client: f, Accessible: accessible, ProgramRunner: func(_ tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
			count = len(options)
			return nil, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if accessible {
			want = 1
		}
		if count != want {
			t.Fatalf("accessible=%v options=%d want=%d", accessible, count, want)
		}
	}
}

func TestActionSelectionReconcilesWhenPolicyChanges(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.screen = screenActions
	initial := actionsFor(m.apps[0], nil, m.status)
	m.selectedAction = len(initial) - 1
	m.actionOffset = m.selectedAction
	m.overviewGen = 12
	m.overviewLoading = true
	job := activeJob("job-a", "app-a", "deploy")
	m.Update(overviewMsg{
		generation: 12,
		status:     m.status,
		apps:       apicontract.ApplicationList{Items: m.apps},
		jobs:       apicontract.JobList{Items: []apicontract.Job{job}},
	})
	_, current, ok := m.currentActions()
	if !ok || m.selectedAction < 0 || m.selectedAction >= len(current) {
		t.Fatalf("selection=%d actions=%d", m.selectedAction, len(current))
	}
	if view := m.View(); !strings.Contains(view, "Back") {
		t.Fatalf("reconciled action not rendered:\n%s", view)
	}
	if _, cmd := m.Update(key("enter")); cmd != nil || m.screen != screenSwitchboard {
		t.Fatalf("clamped Back action did not navigate: screen=%v cmd=%v", m.screen, cmd)
	}
}

func TestActionViewWindowsSelectionAndFooterAtCompactSizes(t *testing.T) {
	for _, size := range [][2]int{{32, 8}, {50, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := switchboardModel(&fakeClient{})
			m.accessible = true
			m.screen = screenActions
			m.err = "action unavailable"
			_, items, _ := m.currentActions()
			m.selectedAction = len(items) - 1
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			view := m.View()
			if len(strings.Split(view, "\n")) > size[1] {
				t.Fatalf("height overflow:\n%s", view)
			}
			if !strings.Contains(view, "Current: Back") || !strings.Contains(view, "Esc Back") || !strings.Contains(view, "action unavailable") {
				t.Fatalf("selection, error, or footer hidden:\n%s", view)
			}
		})
	}
}

func TestViewingTerminalJobDoesNotReplaceLiveFollow(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	live := activeJob("live-a", "app-a", "deploy")
	terminal := activeJob("done-b", "app-b", "deploy")
	terminal.Status = "succeeded"
	m.jobs = []apicontract.Job{live, terminal}
	m.selectedAppID = "app-b"
	m.followedJob, m.followedJobID = live, live.ID
	m.followGeneration = 7
	m.followCancel = func() {}
	_, cmd := m.chooseAction(actionItem{Kind: actionViewLast, Enabled: true}, m.apps[1])
	if cmd != nil || m.screen != screenResult || m.followedJobID != live.ID || m.followedJob.ID != live.ID || m.followGeneration != 7 {
		t.Fatalf("terminal view corrupted live follow: id=%q job=%q generation=%d", m.followedJobID, m.followedJob.ID, m.followGeneration)
	}
	f.jobsByID = map[string]apicontract.Job{live.ID: live}
	_, fetch := m.Update(followEventMsg{generation: 7, event: apicontract.JobEvent{ID: 2, JobID: live.ID, Message: "still live"}})
	if fetch == nil {
		t.Fatal("live follow event was no longer accepted")
	}
	_ = fetch().(jobSnapshotMsg)
	if f.lastJob != live.ID {
		t.Fatalf("fetched %q want %q", f.lastJob, live.ID)
	}
}

func TestCancellationPendingSuppressesRepeatedIntent(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-1", "app-a", "deploy")
	job.Status, job.Phase = "waiting_external", "cancelling"
	m.jobs = []apicontract.Job{job}
	m.followedJob, m.followedJobID = job, job.ID
	m.screen = screenJobProgress
	if _, cmd := m.Update(key("c")); cmd != nil || m.screen != screenJobProgress {
		t.Fatalf("pending cancellation accepted again: screen=%v cmd=%v", m.screen, cmd)
	}
	if view := m.View(); strings.Contains(view, "c Cancel job") || !strings.Contains(view, "Cancellation pending") {
		t.Fatalf("pending footer is misleading:\n%s", view)
	}
	items := actionsFor(m.apps[0], &job, m.status)
	for _, item := range items {
		if item.Kind == actionCancelJob && (item.Enabled || item.DisabledBy == "") {
			t.Fatalf("pending cancel action=%+v", item)
		}
	}
	if view := (&Model{width: 80, height: 24, layout: calculateLayout(80, 24), screen: screenConfirmation, confirmation: &confirmation{Action: actionCancelJob, App: m.apps[0], Job: job}}).View(); !strings.Contains(view, "Job ID") || !strings.Contains(view, job.ID) {
		t.Fatalf("cancel confirmation omits job identity:\n%s", view)
	}
}

func TestStaleAndMismatchedMutationResponsesAreIgnored(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0], ReturnScreen: screenActions}
	m.screen = screenConfirmation
	_, cmd := m.Update(key("enter"))
	msg := cmd().(mutationMsg)
	m.showLogin("Session expired. Sign in again.")
	m.Update(msg)
	if m.screen != screenLogin || m.followedJobID != "" || m.overviewLoaded || len(m.apps) != 0 {
		t.Fatalf("stale mutation changed cleared auth state: screen=%v follow=%q apps=%d", m.screen, m.followedJobID, len(m.apps))
	}

	f = &fakeClient{mutationJob: activeJob("wrong", "app-b", "deploy")}
	m = switchboardModel(f)
	m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0], ReturnScreen: screenActions}
	m.screen = screenConfirmation
	_, cmd = m.Update(key("enter"))
	m.Update(cmd())
	if m.screen != screenActions || m.followedJobID != "" || !strings.Contains(m.err, "unexpected application") {
		t.Fatalf("mismatched response accepted: screen=%v follow=%q err=%q", m.screen, m.followedJobID, m.err)
	}
}

func TestMutationResponseTypeMustMatchConfirmedAction(t *testing.T) {
	for _, test := range []struct {
		action actionKind
		got    string
	}{
		{actionDeploy, "restart"},
		{actionStart, "stop"},
		{actionStop, "deploy"},
		{actionRestart, "start"},
	} {
		t.Run(actionVerb(test.action), func(t *testing.T) {
			request := mutationRequest{Action: test.action, AppID: "app-a"}
			job := activeJob("job", "app-a", test.got)
			if err := validateMutationResponse(request, job); err == nil || !strings.Contains(err.Error(), "operation type") {
				t.Fatalf("mismatch %q accepted: %v", test.got, err)
			}
			expected, _ := expectedMutationJobType(test.action)
			job.Type = strings.ToUpper(expected)
			if err := validateMutationResponse(request, job); err != nil {
				t.Fatalf("normalized matching type rejected: %v", err)
			}
		})
	}
}

func TestMismatchedCancelResponseDoesNotRetargetFollow(t *testing.T) {
	job := activeJob("job-a", "app-a", "deploy")
	f := &fakeClient{cancelJobResponse: activeJob("job-b", "app-a", "deploy")}
	m := switchboardModel(f)
	m.followedJob, m.followedJobID = job, job.ID
	m.confirmation = &confirmation{Action: actionCancelJob, App: m.apps[0], Job: job, ReturnScreen: screenJobProgress}
	m.screen = screenConfirmation
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if m.screen != screenJobProgress || m.followedJobID != job.ID || !strings.Contains(m.err, "unexpected cancellation job") {
		t.Fatalf("mismatched cancellation accepted: screen=%v follow=%q err=%q", m.screen, m.followedJobID, m.err)
	}

	f = &fakeClient{}
	m = switchboardModel(f)
	m.followedJob, m.followedJobID = job, job.ID
	m.confirmation = &confirmation{Action: actionCancelJob, App: m.apps[0], Job: job, ReturnScreen: screenJobProgress}
	m.screen = screenConfirmation
	_, cmd = m.Update(key("enter"))
	msg := cmd().(cancelMsg)
	m.goOffline(errors.New("connection lost"))
	m.Update(msg)
	if m.screen != screenOffline || m.followedJobID != "" || len(m.apps) != 0 {
		t.Fatalf("stale cancellation changed offline state: screen=%v follow=%q apps=%d", m.screen, m.followedJobID, len(m.apps))
	}
}

func TestFollowStreamFailureStopsWithoutRetryLoop(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-a", "app-a", "deploy")
	m.followedJob, m.followedJobID = job, job.ID
	m.followGeneration = 4
	cancelled := 0
	m.followCancel = func() { cancelled++ }
	_, cmd := m.Update(followEventMsg{generation: 4, err: errors.New("transport failed")})
	if cmd != nil || m.followCancel != nil || cancelled != 1 || f.jobCalls != 0 || !strings.Contains(m.err, "Reopen") {
		t.Fatalf("stream failure retried: cmd=%v cancel=%d jobs=%d err=%q", cmd, cancelled, f.jobCalls, m.err)
	}
	_, cmd = m.Update(followEventMsg{generation: 4, err: errors.New("again")})
	if cmd != nil || f.jobCalls != 0 {
		t.Fatalf("stale failure produced work: cmd=%v jobs=%d", cmd, f.jobCalls)
	}
}

func TestStrictIdentitySanitization(t *testing.T) {
	raw := "Al\r\npha\t\u202e\u200b\ufe0fBeta" + strings.Repeat("界", 20)
	got := sanitizeIdentity(raw, 16)
	if !utf8.ValidString(got) || len(got) > 16 || strings.ContainsAny(got, "\r\n\t") || strings.ContainsRune(got, '\u202e') || strings.ContainsRune(got, '\u200b') || strings.ContainsRune(got, '\ufe0f') {
		t.Fatalf("unsafe identity %q (%d bytes)", got, len(got))
	}
	app := runningApp("app-a", raw)
	m := switchboardModel(&fakeClient{})
	m.accessible = true
	m.apps, m.selectedAppID = []apicontract.Application{app}, app.ID
	if view := m.View(); strings.ContainsAny(view, "\r\t") || strings.ContainsRune(view, '\u202e') || strings.ContainsRune(view, '\u200b') {
		t.Fatalf("unsafe identity rendered: %q", view)
	}
}

func TestDashboardOpenerBusyGuardStartsExactlyOnce(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	calls := 0
	m.openURL = func(context.Context, string) error { calls++; return nil }
	first := m.openDashboard()
	if first == nil || m.openDashboard() != nil {
		t.Fatal("busy opener did not suppress duplicate")
	}
	msg := first().(openURLMsg)
	if calls != 1 || m.openDashboard() != nil {
		t.Fatalf("opener calls=%d busy=%v", calls, m.openURLBusy)
	}
	m.Update(msg)
	if m.openDashboard() != nil || !strings.Contains(m.err, "already sent") {
		t.Fatalf("cooldown allowed rapid reopen: err=%q", m.err)
	}
	now = now.Add(dashboardOpenCooldown)
	second := m.openDashboard()
	if second == nil {
		t.Fatal("opener remained guarded after cooldown")
	}
	m.Update(second())
	if calls != 2 {
		t.Fatalf("opener calls=%d want=2", calls)
	}
}

func TestMajorScreensReserveEssentialControlsAtSupportedSizes(t *testing.T) {
	type screenCase struct {
		name  string
		setup func(*Model)
		want  []string
	}
	cases := []screenCase{
		{name: "loading", setup: func(m *Model) { m.screen = screenLoading }, want: []string{"Ctrl+C", "Quit"}},
		{name: "offline", setup: func(m *Model) { m.goOffline(errors.New("controller unavailable")) }, want: []string{"Retry", "q Quit"}},
		{name: "login", setup: func(m *Model) { m.showLogin("Session expired") }, want: []string{"Enter", "Ctrl+C Quit"}},
		{name: "bootstrap", setup: func(m *Model) { m.showBootstrap(); m.err = "Check token" }, want: []string{"Enter", "Ctrl+C Quit"}},
		{name: "bootstrap confirmation", setup: func(m *Model) { m.showBootstrap(); m.bootstrapConfirm = true; m.bootstrapUsername = "admin" }, want: []string{"Enter", "Esc Cancel"}},
		{name: "switchboard", setup: func(m *Model) { m.screen = screenSwitchboard }, want: []string{"Enter", "? Help", "q Quit"}},
		{name: "actions", setup: func(m *Model) { m.screen = screenActions }, want: []string{"Enter", "Esc Back", "q Quit"}},
		{name: "deploy confirmation", setup: func(m *Model) {
			m.screen = screenConfirmation
			m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0], ReturnScreen: screenActions}
			m.err = "Review target"
		}, want: []string{"Enter", "Esc Cancel"}},
		{name: "cancel confirmation", setup: func(m *Model) {
			job := activeJob("job-a", "app-a", "deploy")
			m.screen = screenConfirmation
			m.confirmation = &confirmation{Action: actionCancelJob, App: m.apps[0], Job: job, ReturnScreen: screenJobProgress}
			m.err = "Review cancellation"
		}, want: []string{"Enter", "Esc Cancel"}},
		{name: "progress", setup: func(m *Model) {
			job := activeJob("job-a", "app-a", "deploy")
			job.Checkpoint = "container-created"
			m.screen, m.followedJob, m.followedJobID = screenJobProgress, job, job.ID
			for i := 0; i < maxPhases; i++ {
				m.phases = append(m.phases, phaseState{Name: fmt.Sprintf("phase-%02d", i), Completed: i < maxPhases-1})
			}
			for i := 0; i < maxRecentEvents; i++ {
				m.recentEvents = append(m.recentEvents, apicontract.JobEvent{ID: int64(i + 1), Message: fmt.Sprintf("event-%02d", i)})
			}
			m.setBanner("Network is slow", bannerWarning)
		}, want: []string{"Esc Back", "c Cancel", "q Quit"}},
		{name: "result", setup: func(m *Model) {
			m.screen = screenResult
			m.result = &resultState{Title: "Deploy completed", Detail: "Alpha is running"}
		}, want: []string{"Enter Back", "q Quit", "r Refresh"}},
		{name: "help", setup: func(m *Model) { m.screen = screenHelp; m.returnScreen = screenSwitchboard }, want: []string{"Esc Back", "r Refresh", "q Quit"}},
	}
	for _, size := range [][2]int{{32, 8}, {40, 10}, {50, 12}} {
		for _, test := range cases {
			t.Run(fmt.Sprintf("%s/%dx%d", test.name, size[0], size[1]), func(t *testing.T) {
				m := switchboardModel(&fakeClient{})
				m.accessible = true
				test.setup(m)
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) != size[1] {
					t.Fatalf("line count=%d want=%d\n%s", len(lines), size[1], view)
				}
				footer := lines[len(lines)-1]
				for _, want := range test.want {
					if !strings.Contains(footer, want) {
						t.Fatalf("footer missing %q: %q\n%s", want, footer, view)
					}
				}
			})
		}
	}
}

func TestUnsupportedTerminalIgnoresOrdinaryKeysOnEveryInteractiveScreen(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Model)
		key   tea.KeyMsg
		check func(*testing.T, *Model, *fakeClient, tea.Cmd)
	}{
		{name: "auth", setup: func(m *Model) { m.showLogin("") }, key: key("r"), check: func(t *testing.T, m *Model, _ *fakeClient, cmd tea.Cmd) {
			if cmd != nil || m.screen != screenLogin || m.authInputs[0].Value() != "" || m.overviewLoading {
				t.Fatal("hidden auth accepted input")
			}
		}},
		{name: "actions", setup: func(m *Model) { m.screen = screenActions }, key: key("enter"), check: func(t *testing.T, m *Model, _ *fakeClient, cmd tea.Cmd) {
			if cmd != nil || m.screen != screenActions || m.confirmation != nil {
				t.Fatal("hidden action executed")
			}
		}},
		{name: "confirmation", setup: func(m *Model) {
			m.screen = screenConfirmation
			m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0]}
		}, key: key("enter"), check: func(t *testing.T, m *Model, f *fakeClient, cmd tea.Cmd) {
			if cmd != nil || m.screen != screenConfirmation || f.deployCalls != 0 || m.mutationBusy {
				t.Fatal("hidden confirmation executed")
			}
		}},
		{name: "progress", setup: func(m *Model) {
			job := activeJob("job-a", "app-a", "deploy")
			m.screen, m.followedJob, m.followedJobID = screenJobProgress, job, job.ID
		}, key: key("c"), check: func(t *testing.T, m *Model, f *fakeClient, cmd tea.Cmd) {
			if cmd != nil || m.screen != screenJobProgress || m.confirmation != nil || f.cancelCalls != 0 {
				t.Fatal("hidden progress action executed")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := &fakeClient{}
			m := switchboardModel(f)
			test.setup(m)
			m.Update(tea.WindowSizeMsg{Width: 31, Height: 8})
			_, cmd := m.Update(test.key)
			test.check(t, m, f, cmd)
		})
	}
}

func TestRuntimeReadinessIsCentralizedAcrossHeaderAndActions(t *testing.T) {
	tests := []struct {
		name              string
		status            apicontract.SystemStatus
		label             string
		deploy, lifecycle bool
	}{
		{name: "test runtime", status: readyStatus(), label: "Ready", deploy: true, lifecycle: true},
		{name: "compose ready", status: apicontract.SystemStatus{Capabilities: apicontract.Capabilities{ComposeRuntime: true}, Diagnostics: apicontract.Diagnostics{EngineReady: true, ComposeAvailable: true}}, label: "Ready", deploy: true},
		{name: "compose engine unavailable", status: apicontract.SystemStatus{Capabilities: apicontract.Capabilities{ComposeRuntime: true}, Diagnostics: apicontract.Diagnostics{ComposeAvailable: true}}, label: "Unavailable"},
		{name: "compose unavailable", status: apicontract.SystemStatus{Capabilities: apicontract.Capabilities{ComposeRuntime: true}, Diagnostics: apicontract.Diagnostics{EngineReady: true}}, label: "Unavailable"},
		{name: "not configured", status: apicontract.SystemStatus{}, label: "Not configured"},
	}
	app := runningApp("app-a", "Alpha")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := runtimeState(test.status)
			if state.Label != test.label || state.DeployReady != test.deploy || state.LifecycleReady != test.lifecycle {
				t.Fatalf("runtime state=%+v", state)
			}
			items := actionsFor(app, nil, test.status)
			enabled := map[actionKind]bool{}
			for _, item := range items {
				enabled[item.Kind] = item.Enabled
				if item.Kind == actionDeploy && item.Enabled != state.DeployReady {
					t.Fatalf("deploy policy=%+v runtime=%+v", item, state)
				}
			}
			if enabled[actionStop] != state.LifecycleReady || enabled[actionRestart] != state.LifecycleReady {
				t.Fatalf("lifecycle policy=%v runtime=%+v", enabled, state)
			}
			m := switchboardModel(&fakeClient{})
			m.status = test.status
			if header := ansi.Strip(m.header()); !strings.Contains(header, "Runtime: "+state.Label) {
				t.Fatalf("header=%q runtime=%+v", header, state)
			}
		})
	}
}

func TestAccessibleRenderingIsASCIIAndNamesCurrentControls(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.accessible = true
	m.showLogin("")
	login := m.View()
	if !strings.Contains(login, "> Username:") {
		t.Fatalf("auth focus missing:\n%s", login)
	}
	m = switchboardModel(&fakeClient{})
	m.accessible = true
	switchboard := m.View()
	for _, want := range []string{"Current: Alpha", "Status: Running"} {
		if !strings.Contains(switchboard, want) {
			t.Fatalf("switchboard missing %q:\n%s", want, switchboard)
		}
	}
	m.screen = screenActions
	actions := m.View()
	if !strings.Contains(actions, "Current: Deploy latest") {
		t.Fatalf("actions selection missing:\n%s", actions)
	}
	m = switchboardModel(&fakeClient{})
	m.accessible = true
	job := activeJob("job-a", "app-a", "deploy")
	m.followedJob, m.followedJobID, m.screen = job, job.ID, screenJobProgress
	m.phases = []phaseState{{Name: "prepare", Completed: true}, {Name: "apply"}}
	view := m.View()
	for _, unsafe := range []string{"●", "○", "×", "✓", "…", "—", "·", "█", "░", "╭", "│", "•"} {
		if strings.Contains(view, unsafe) {
			t.Fatalf("accessible view contains %q:\n%s", unsafe, view)
		}
	}
	for _, want := range []string{"[", "#", "done prepare", "current apply", "Status: Running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("accessible view missing %q:\n%s", want, view)
		}
	}
}

func TestRefreshIsGlobalOnlyOnSafeMainScreens(t *testing.T) {
	for _, current := range []screen{screenSwitchboard, screenActions, screenJobProgress, screenResult, screenHelp} {
		t.Run(fmt.Sprint(current), func(t *testing.T) {
			m := switchboardModel(&fakeClient{})
			m.screen = current
			if current == screenResult {
				m.result = &resultState{Title: "Done"}
			}
			if current == screenJobProgress {
				m.followedJob = activeJob("job", "app-a", "deploy")
			}
			_, cmd := m.Update(key("r"))
			if cmd == nil || !m.overviewLoading || m.screen != current {
				t.Fatalf("screen=%v cmd=%v loading=%v current=%v", current, cmd, m.overviewLoading, m.screen)
			}
		})
	}
	for _, current := range []screen{screenLogin, screenConfirmation} {
		m := switchboardModel(&fakeClient{})
		if current == screenLogin {
			m.showLogin("")
		} else {
			m.screen = current
			m.confirmation = &confirmation{Action: actionDeploy, App: m.apps[0]}
		}
		_, _ = m.Update(key("r"))
		if m.overviewLoading {
			t.Fatalf("unsafe screen %v refreshed", current)
		}
	}
}

func TestProgressShowsGerundRelativeUpdateAndCheckpoint(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	now := time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	job := activeJob("job-a", "app-a", "stop")
	job.UpdatedAt = now.Add(-90 * time.Minute).Format(time.RFC3339Nano)
	job.Checkpoint = "drained\ncontainers"
	m.followedJob, m.followedJobID, m.screen = job, job.ID, screenJobProgress
	view := m.View()
	for _, want := range []string{"Stopping Alpha", "updated 1h ago", "Checkpoint: drained containers"} {
		if !strings.Contains(view, want) {
			t.Fatalf("progress missing %q:\n%s", want, view)
		}
	}
	for raw, want := range map[string]string{"deploy": "Deploying", "start": "Starting", "stop": "Stopping", "restart": "Restarting"} {
		if got := operationGerund(raw); got != want {
			t.Fatalf("gerund %q=%q want=%q", raw, got, want)
		}
	}
}

func TestBannerIntentAndCtrlCRemainSemanticallyDistinct(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.setBanner("opened", bannerSuccess)
	if m.bannerTone != bannerSuccess {
		t.Fatal("success banner lost intent")
	}
	m.setBanner("continues", bannerInfo)
	if m.bannerTone != bannerInfo {
		t.Fatal("info banner lost intent")
	}
	m.setBanner("retry", bannerWarning)
	if m.bannerTone != bannerWarning {
		t.Fatal("warning banner lost intent")
	}
	f := &fakeClient{}
	m = switchboardModel(f)
	stopped := 0
	m.followCancel = func() { stopped++ }
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || stopped != 1 || f.cancelCalls != 0 {
		t.Fatalf("Ctrl+C cmd=%v local stops=%d server cancels=%d", cmd, stopped, f.cancelCalls)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C did not return Bubble Tea quit message")
	}
}

func TestCompactAuthFieldsPreserveFocusedLabelAndInputCell(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*Model)
		labels []string
	}{
		{name: "login", setup: func(m *Model) { m.showLogin("") }, labels: []string{"Username", "Passphrase"}},
		{name: "bootstrap", setup: func(m *Model) { m.showBootstrap() }, labels: []string{"Token", "Admin username", "Passphrase"}},
	}
	for _, size := range [][2]int{{32, 8}, {40, 10}, {50, 12}} {
		for _, test := range tests {
			for index, label := range test.labels {
				t.Run(fmt.Sprintf("%s/%s/%dx%d", test.name, label, size[0], size[1]), func(t *testing.T) {
					m := switchboardModel(&fakeClient{})
					m.accessible = true
					test.setup(m)
					m.focusAuth(index)
					m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					prefix := "> " + label + ": "
					var focused string
					for _, line := range strings.Split(m.View(), "\n") {
						if strings.HasPrefix(line, prefix) {
							focused = line
							break
						}
					}
					if focused == "" || ansi.StringWidth(focused) <= ansi.StringWidth(prefix) {
						t.Fatalf("focused label or input cell cropped; prefix=%q view:\n%s", prefix, m.View())
					}
				})
			}
		}
	}
}

func TestAccessibleFinalCropUsesASCIIEllipsis(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	m.accessible = true
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
	view := m.finishView(strings.Repeat("long ", 20) + "—…·●✓")
	if !strings.Contains(view, "...") {
		t.Fatalf("ASCII truncation marker missing: %q", view)
	}
	for _, unsafe := range []string{"…", "—", "·", "●", "✓"} {
		if strings.Contains(view, unsafe) {
			t.Fatalf("accessible crop contains %q: %q", unsafe, view)
		}
	}
	if ansi.StringWidth(view) > 32 {
		t.Fatalf("accessible crop width=%d: %q", ansi.StringWidth(view), view)
	}
}

func TestSwitchboardFooterDropsOptionalItemsByPriority(t *testing.T) {
	tests := []struct {
		width int
		want  string
	}{
		{32, "Enter Actions | ? Help | q Quit"},
		{50, "Enter Actions | r Refresh | ? Help | q Quit"},
		{60, "Enter Actions | r Refresh | o Open Web | ? Help | q Quit"},
		{80, "Enter Actions | r Refresh | o Open Web | ? Help | l Logout | q Quit"},
		{99, "Enter Actions | r Refresh | o Open Web | ? Help | l Logout | q Quit"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.width), func(t *testing.T) {
			m := switchboardModel(&fakeClient{})
			m.width = test.width
			if got := m.switchboardFooter(); got != test.want {
				t.Fatalf("footer=%q want=%q", got, test.want)
			}
			if ansi.StringWidth(m.switchboardFooter()) > test.width {
				t.Fatalf("footer exceeds width %d: %q", test.width, m.switchboardFooter())
			}
		})
	}
}

func TestSelectedApplicationJobSummaryShowsValidRecency(t *testing.T) {
	m := switchboardModel(&fakeClient{})
	now := time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	job := activeJob("job-a", "app-a", "deploy")
	job.UpdatedAt = now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
	m.jobs = []apicontract.Job{job}
	for name, value := range map[string]string{
		"compact": m.compactAppSummary(m.apps[0]),
		"details": strings.Join(m.appDetails(m.apps[0]), "\n"),
	} {
		if !strings.Contains(value, "updated 5m ago") {
			t.Fatalf("%s summary missing recency: %q", name, value)
		}
	}
}

func TestRelativeUpdatedAtOmitsFutureTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC)
	if got := relativeUpdatedAt(now.Add(time.Second).Format(time.RFC3339Nano), now); got != "" {
		t.Fatalf("future timestamp rendered as %q", got)
	}
}

func TestUnavailableRuntimeGuidanceIsCompactAndActionable(t *testing.T) {
	for _, size := range [][2]int{{32, 8}, {40, 10}, {50, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := switchboardModel(&fakeClient{})
			m.accessible = true
			m.status = apicontract.SystemStatus{Capabilities: apicontract.Capabilities{ComposeRuntime: true}}
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			view := m.View()
			for _, want := range []string{"Runtime unavailable.", "r Retry | o Web | hostctl doctor", "Enter Actions", "? Help", "q Quit"} {
				if !strings.Contains(view, want) {
					t.Fatalf("guidance missing %q:\n%s", want, view)
				}
			}
			if len(strings.Split(view, "\n")) != size[1] {
				t.Fatalf("height mismatch:\n%s", view)
			}
		})
	}
}
