package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hostd/hostd/internal/apicontract"
)

type fakeClient struct {
	mu                                                              sync.Mutex
	bootstrapStatus                                                 apicontract.BootstrapStatus
	bootstrapStatusErr                                              error
	me                                                              apicontract.MeResponse
	meErr                                                           error
	status                                                          apicontract.SystemStatus
	apps                                                            apicontract.ApplicationList
	jobs                                                            apicontract.JobList
	overviewErr                                                     error
	job                                                             apicontract.Job
	jobErr                                                          error
	deployCalls, lifecycleCalls, cancelCalls, logoutCalls, jobCalls int
	lastApp, lastAction, lastKey, lastJob                           string
	followCtx                                                       context.Context
	followEvents                                                    chan apicontract.JobEvent
	followErrors                                                    chan error
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
	return apicontract.JobMutationResponse{Job: activeJob("deploy-job", app, "deploy")}, nil
}
func (f *fakeClient) Lifecycle(_ context.Context, app, action, key string) (apicontract.JobMutationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lifecycleCalls++
	f.lastApp, f.lastAction, f.lastKey = app, action, key
	return apicontract.JobMutationResponse{Job: activeJob(action+"-job", app, action)}, nil
}
func (f *fakeClient) Jobs(context.Context) (apicontract.JobList, error) { return f.jobs, f.overviewErr }
func (f *fakeClient) Job(context.Context, string) (apicontract.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobCalls++
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
	job := f.job
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
		{"compose running", apicontract.SystemStatus{Capabilities: apicontract.Capabilities{ComposeRuntime: true}}, "running", nil, true, false, false, false},
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
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("cancel command missing")
	}
	msg := cmd().(cancelMsg)
	m.Update(msg)
	if f.cancelCalls != 1 || f.lastJob != "job-1" {
		t.Fatalf("cancel=%d job=%s", f.cancelCalls, f.lastJob)
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
	if !strings.Contains(view, "> Alpha") || !strings.Contains(view, "Running") {
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
