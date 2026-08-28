package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/secretfile"
)

type fakeClient struct {
	bootstrapStatus apicontract.BootstrapStatus
	bootstrapErr    error
	me              apicontract.MeResponse
	meErr           error
	lifecycleCalls  int
	bootstrapCalls  int
	logoutCalls     int
	followEvents    chan apicontract.JobEvent
	followErrors    chan error
	followCtx       context.Context
	lifecycleAppID  string
	applicationID   string
}

func (f *fakeClient) BootstrapStatus(context.Context) (apicontract.BootstrapStatus, error) {
	return f.bootstrapStatus, f.bootstrapErr
}
func (f *fakeClient) Bootstrap(_ context.Context, request apicontract.BootstrapRequest) (apicontract.SessionResponse, error) {
	f.bootstrapCalls++
	return apicontract.SessionResponse{User: apicontract.User{Username: request.Username, Role: "admin"}}, nil
}
func (f *fakeClient) Login(_ context.Context, request apicontract.LoginRequest) (apicontract.SessionResponse, error) {
	return apicontract.SessionResponse{User: apicontract.User{Username: request.Username, Role: "admin"}}, nil
}
func (f *fakeClient) Logout(context.Context) error                       { f.logoutCalls++; return nil }
func (f *fakeClient) Me(context.Context) (apicontract.MeResponse, error) { return f.me, f.meErr }
func (f *fakeClient) Status(context.Context) (apicontract.SystemStatus, error) {
	return apicontract.SystemStatus{Daemon: "running", Diagnostics: apicontract.Diagnostics{EngineReady: true}}, nil
}
func (f *fakeClient) Doctor(context.Context) (apicontract.DoctorResponse, error) {
	return apicontract.DoctorResponse{Checks: []apicontract.DoctorCheck{{Name: "engine", OK: true}}}, nil
}
func (f *fakeClient) Applications(context.Context) (apicontract.ApplicationList, error) {
	return apicontract.ApplicationList{Items: []apicontract.Application{{ID: "app-1", Slug: "one", Name: "One"}, {ID: "app-2", Slug: "two", Name: "Two"}}}, nil
}
func (f *fakeClient) Application(_ context.Context, id string) (apicontract.Application, error) {
	f.applicationID = id
	return apicontract.Application{ID: id, Slug: "one", Name: "One"}, nil
}
func (f *fakeClient) Machines(context.Context) (apicontract.MachineList, error) {
	return apicontract.MachineList{Items: []apicontract.Machine{{ID: "m1", Name: "local"}}}, nil
}
func (f *fakeClient) Deploy(context.Context, string, string) (apicontract.JobMutationResponse, error) {
	return apicontract.JobMutationResponse{Created: true, Job: apicontract.Job{ID: "job-deploy", Status: "queued"}}, nil
}
func (f *fakeClient) Lifecycle(_ context.Context, appID, _ string, _ string) (apicontract.JobMutationResponse, error) {
	f.lifecycleCalls++
	f.lifecycleAppID = appID
	return apicontract.JobMutationResponse{Created: true, Job: apicontract.Job{ID: "job-life", Status: "queued"}}, nil
}
func (f *fakeClient) Jobs(context.Context) (apicontract.JobList, error) {
	return apicontract.JobList{Items: []apicontract.Job{{ID: "job-1", Status: "running"}}}, nil
}
func (f *fakeClient) Job(_ context.Context, id string) (apicontract.Job, error) {
	return apicontract.Job{ID: id, Status: "running"}, nil
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
func (f *fakeClient) CancelJob(_ context.Context, id, _ string) (apicontract.JobResponse, error) {
	return apicontract.JobResponse{Job: apicontract.Job{ID: id, Status: "cancelling"}}, nil
}
func (f *fakeClient) ResumeJob(_ context.Context, id, _ string) (apicontract.JobResponse, error) {
	return apicontract.JobResponse{Job: apicontract.Job{ID: id, Status: "queued"}}, nil
}

func consoleModel(client *fakeClient) *Model {
	m := NewModel(context.Background(), client, &memoryHistoryStore{}, "http://controller")
	m.screen = screenConsole
	m.user = apicontract.User{Username: "operator"}
	m.apps = []apicontract.Application{{ID: "app-1", Slug: "one"}, {ID: "app-2", Slug: "two"}}
	m.selectedAppID = "app-1"
	m.width, m.height = 120, 30
	m.resize()
	return m
}

func TestParseAndCompleteCommands(t *testing.T) {
	for _, raw := range []string{"/help", "/clear", "/history clear", "/quit", "/logout", "/status", "/doctor", "/apps", "/app", "/use demo", "/machines", "/deploy", "/start", "/stop", "/restart", "/jobs", "/job j1", "/follow j1", "/cancel j1", "/resume j1"} {
		if _, err := parseCommand(raw); err != nil {
			t.Errorf("parseCommand(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"hello", "/bogus", "/use", "/job", "/apps extra", "/history"} {
		if _, err := parseCommand(raw); err == nil {
			t.Errorf("parseCommand(%q) unexpectedly succeeded", raw)
		}
	}
	suggestions := commandSuggestions("/st")
	if strings.Join(suggestions, ",") != "/start,/status,/stop" {
		t.Fatalf("suggestions = %v", suggestions)
	}
}

func TestProtectedHistoryLimitClearAndNoPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	store := NewProtectedHistoryStore(path)
	values := make([]string, 0, 105)
	for i := 0; i < 105; i++ {
		values = append(values, fmt.Sprintf("/job sensitive-marker-%03d", i))
	}
	values = append(values, "bad\ncommand", "")
	if err := store.Save(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != historyLimit || !strings.Contains(loaded[0], "005") || !strings.Contains(loaded[99], "104") {
		t.Fatalf("bounded history = %#v", loaded)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive-marker") {
		t.Fatal("history file contains plaintext command")
	}
	if err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(context.Background())
	if err != nil || len(loaded) != 0 {
		t.Fatalf("load after clear = %v, %v", loaded, err)
	}
}

func TestProtectedHistoryRejectsOversizedDecryptedPayloadAndEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	store := NewProtectedHistoryStore(path)
	if err := secretfile.Write(path, historyPurpose, []byte(`["`+strings.Repeat("x", maxHistoryPayloadBytes)+`"]`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized history error = %v", err)
	}
	if err := store.Save(context.Background(), []string{"/status", "/job " + strings.Repeat("x", maxHistoryEntryBytes+1)}); err != nil {
		t.Fatal(err)
	}
	values, err := store.Load(context.Background())
	if err != nil || len(values) != 1 || values[0] != "/status" {
		t.Fatalf("bounded history = %#v, %v", values, err)
	}
}

func TestAuthInputNeverEntersHistoryAndIsMasked(t *testing.T) {
	m := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "endpoint")
	m.showBootstrap()
	if m.authInputs[0].EchoMode != textinput.EchoPassword || m.authInputs[2].EchoMode != textinput.EchoPassword || m.authInputs[1].EchoMode == textinput.EchoPassword {
		t.Fatal("bootstrap masking configuration is incorrect")
	}
	m.authInputs[0].SetValue("bootstrap-secret")
	m.authInputs[1].SetValue("admin")
	m.authInputs[2].SetValue("passphrase-secret")
	m.authIndex = 2
	_, cmd := m.handleAuthKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !m.bootstrapConfirm || m.client.(*fakeClient).bootstrapCalls != 0 || len(m.historyValues) != 0 {
		t.Fatal("bootstrap did not wait for explicit confirmation")
	}
	m.width, m.height = 80, 20
	view := m.View()
	if !strings.Contains(view, "admin") || strings.Contains(view, "bootstrap-secret") || strings.Contains(view, "passphrase-secret") {
		t.Fatalf("confirmation rendered secrets or omitted username: %q", view)
	}
	_, cmd = m.handleAuthKey(tea.KeyMsg{Type: tea.KeyEnter})
	for _, input := range m.authInputs {
		if input.Value() != "" {
			t.Fatal("credential remained in input after submission")
		}
	}
	msg := cmd()
	m.Update(msg)
	if m.screen != screenConsole || len(m.historyValues) != 0 {
		t.Fatalf("screen/history after auth = %v/%v", m.screen, m.historyValues)
	}
}

func TestBootstrapConfirmationCancelsWithEscapeAndMouse(t *testing.T) {
	m := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "endpoint")
	m.showBootstrap()
	for i, value := range []string{"token", "operator", "passphrase"} {
		m.authInputs[i].SetValue(value)
	}
	m.authIndex = 2
	m.handleAuthKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleAuthKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.bootstrapConfirm || m.authInputs[0].Value() != "" || m.authInputs[2].Value() != "" {
		t.Fatal("Escape retained bootstrap credentials")
	}
	for i, value := range []string{"token", "operator", "passphrase"} {
		m.authInputs[i].SetValue(value)
	}
	m.authIndex = 2
	m.handleAuthKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.width, m.height = 80, 20
	m.View()
	m.handleMouse(tea.MouseMsg(tea.MouseEvent{X: m.bootstrapCancelRect.x, Y: m.bootstrapCancelRect.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
	if m.bootstrapConfirm || m.authInputs[0].Value() != "" || m.authInputs[2].Value() != "" {
		t.Fatal("mouse cancel retained bootstrap credentials")
	}
}

func TestSanitizeAPITextStripsTerminalControls(t *testing.T) {
	input := "safe\x1b[31mred\x1b[0m\x1b]0;owned\x07\r\nnext\u009b31mX\x00"
	got := sanitizeAPIText(input)
	if got != "safered\nnextX" {
		t.Fatalf("sanitize = %q", got)
	}
	if strings.ContainsAny(got, "\x1b\x00\r") {
		t.Fatalf("control survived: %q", got)
	}
}

func TestSanitizeAPITextAdvancesByRuneForUnicodeControls(t *testing.T) {
	got := sanitizeAPIText("before\u0085after")
	if got != "beforeafter" || !utf8.ValidString(got) {
		t.Fatalf("sanitize unicode control = %q", got)
	}
}

func TestLayoutResponsiveAndTiny(t *testing.T) {
	tiny := calculateLayout(40, 10, 4, false)
	if !tiny.tiny || tiny.unsupported || tiny.overview.h != 0 || tiny.transcript.h < 1 {
		t.Fatalf("tiny layout = %#v", tiny)
	}
	stacked := calculateLayout(80, 24, 3, false)
	if stacked.tiny || stacked.wide || stacked.overview.y >= stacked.transcript.y {
		t.Fatalf("stacked layout = %#v", stacked)
	}
	wide := calculateLayout(120, 30, 3, false)
	if !wide.wide || wide.overview.y != wide.transcript.y || wide.transcript.x <= wide.overview.x {
		t.Fatalf("wide layout = %#v", wide)
	}
	unsupported := calculateLayout(20, 5, 0, false)
	if !unsupported.unsupported {
		t.Fatal("extremely small layout must show resize guidance")
	}
}

func TestLifecycleAcceptsExplicitApplicationTarget(t *testing.T) {
	client := &fakeClient{}
	m := consoleModel(client)
	cmd, err := parseCommand("/restart two")
	if err != nil {
		t.Fatal(err)
	}
	msg := m.execute(cmd)()
	if result, ok := msg.(commandResultMsg); !ok || result.err != nil {
		t.Fatalf("result = %#v", msg)
	}
	if client.lifecycleAppID != "app-2" {
		t.Fatalf("explicit target = %q", client.lifecycleAppID)
	}
}

func TestAsyncCommandSnapshotsSelectedAndTargetState(t *testing.T) {
	client := &fakeClient{}
	m := consoleModel(client)
	run := m.execute(command{Name: "/stop", Args: []string{"one"}, Raw: "/stop one"})
	m.selectedAppID = "app-2"
	m.apps[0].ID = "changed"
	result := run().(commandResultMsg)
	if result.err != nil || client.lifecycleAppID != "app-1" {
		t.Fatalf("result=%#v lifecycle app=%q", result, client.lifecycleAppID)
	}
	run = m.execute(command{Name: "/app", Raw: "/app"})
	m.selectedAppID = "app-1"
	result = run().(commandResultMsg)
	if result.err != nil || client.applicationID != "app-2" {
		t.Fatalf("result=%#v application id=%q", result, client.applicationID)
	}
}

func TestLogoutSnapshotsHistoryBeforeAsyncWork(t *testing.T) {
	history := &memoryHistoryStore{}
	m := NewModel(context.Background(), &fakeClient{}, history, "endpoint")
	m.screen = screenConsole
	m.historyValues = []string{"/status"}
	run := m.execute(command{Name: "/logout"})
	m.historyValues = []string{"/stop"}
	_ = run()
	values, err := history.Load(context.Background())
	if err != nil || len(values) != 1 || values[0] != "/status" {
		t.Fatalf("saved history=%v err=%v", values, err)
	}
}

func TestRenderSanitizesStoredErrorAndUsesSameJobRowsAsHitTargets(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.screen = screenOffline
	m.err = "bad\x1b[31merror"
	if got := m.View(); strings.Contains(got, "\x1b[31m") {
		t.Fatalf("offline view rendered raw escape: %q", got)
	}
	m.screen = screenConsole
	m.jobs = []apicontract.Job{{ID: "active", Status: "running"}, {ID: "failed", Status: "failed"}}
	m.resize()
	rows := append([]apicontract.Job(nil), m.overviewJobRows...)
	_ = m.View()
	if !reflect.DeepEqual(rows, m.overviewJobRows) || len(m.jobRects) != len(rows) {
		t.Fatalf("rows=%v rendered=%v hit targets=%d", rows, m.overviewJobRows, len(m.jobRects))
	}
}

func TestMutationRequiresKeyboardConfirmationAndAutoFollows(t *testing.T) {
	client := &fakeClient{}
	m := consoleModel(client)
	m.newKey = func() string { return "fixed-key" }
	m.commandInput.SetValue("/stop")
	_, persist := m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if persist != nil {
		_ = persist()
	}
	if m.confirm == nil || client.lifecycleCalls != 0 {
		t.Fatalf("confirmation/calls = %#v/%d", m.confirm, client.lifecycleCalls)
	}
	_, run := m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	result := run()
	if client.lifecycleCalls != 1 {
		t.Fatalf("lifecycle calls = %d", client.lifecycleCalls)
	}
	_, follow := m.Update(result)
	if follow == nil {
		t.Fatal("lifecycle did not auto-follow")
	}
	m.Update(follow())
	if m.followJobID != "job-life" {
		t.Fatalf("following %q", m.followJobID)
	}
}

func TestTinyLayoutRendersConfirmationBeforeMutation(t *testing.T) {
	client := &fakeClient{}
	m := consoleModel(client)
	m.width, m.height = 40, 10
	m.resize()
	m.commandInput.SetValue("/stop")
	m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.confirm == nil || client.lifecycleCalls != 0 || !strings.Contains(m.View(), "CONFIRM") {
		t.Fatalf("tiny confirmation/calls/view = %#v/%d/%q", m.confirm, client.lifecycleCalls, m.View())
	}
	_, run := m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	_ = run()
	if client.lifecycleCalls != 1 {
		t.Fatal("tiny confirmation did not gate mutation")
	}
}

func TestMutationRefusesWhenSecureIdempotencyKeyFails(t *testing.T) {
	client := &fakeClient{}
	m := consoleModel(client)
	m.newKey = func() string { return "" }
	result := m.execute(command{Name: "/stop"})().(commandResultMsg)
	if result.err == nil || client.lifecycleCalls != 0 {
		t.Fatalf("result=%#v calls=%d", result, client.lifecycleCalls)
	}
}

func TestEscapeAndMouseCancelConfirmation(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.confirm = &confirmation{Command: command{Name: "/stop"}, Text: "stop?"}
	m.resize()
	m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.confirm != nil {
		t.Fatal("Escape did not cancel")
	}
	m.confirm = &confirmation{Command: command{Name: "/stop"}, Text: "stop?"}
	m.resize()
	e := tea.MouseMsg(tea.MouseEvent{X: m.cancelRect.x, Y: m.cancelRect.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.handleMouse(e)
	if m.confirm != nil {
		t.Fatal("mouse did not cancel")
	}
}

func TestMouseSuggestionApplicationAndWheel(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.commandInput.SetValue("/st")
	m.refreshSuggestions()
	target := m.suggestionRects[0]
	m.handleMouse(tea.MouseMsg(tea.MouseEvent{X: target.x, Y: target.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
	if m.commandInput.Value() != "/start" {
		t.Fatalf("clicked suggestion = %q", m.commandInput.Value())
	}
	appTarget := m.appRects[1]
	m.handleMouse(tea.MouseMsg(tea.MouseEvent{X: appTarget.x, Y: appTarget.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
	if m.selectedAppID != "app-2" {
		t.Fatalf("clicked app = %q", m.selectedAppID)
	}
	for i := 0; i < 80; i++ {
		m.appendEntry(entrySystem, "line", fmt.Sprintf("entry %d", i))
	}
	m.viewport.GotoTop()
	m.handleMouse(tea.MouseMsg(tea.MouseEvent{X: m.layout.transcript.x, Y: m.layout.transcript.y, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}))
	if m.viewport.YOffset == 0 {
		t.Fatal("wheel did not scroll viewport")
	}
}

func TestStartupOfflineBootstrapLoginAndExpiredTransitions(t *testing.T) {
	m := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "endpoint")
	m.Update(bootstrapStatusMsg{err: errors.New("dial refused")})
	if m.screen != screenOffline {
		t.Fatalf("offline screen = %v", m.screen)
	}
	m.Update(bootstrapStatusMsg{status: apicontract.BootstrapStatus{BootstrapRequired: true}})
	if m.screen != screenBootstrap {
		t.Fatalf("bootstrap screen = %v", m.screen)
	}
	m.Update(meMsg{err: &HTTPError{Status: 401, Detail: "expired"}})
	if m.screen != screenLogin {
		t.Fatalf("login screen = %v", m.screen)
	}
	m.screen = screenConsole
	m.Update(commandResultMsg{cmd: command{Name: "/status"}, err: &HTTPError{Status: 401, Detail: "expired"}})
	if m.screen != screenLogin || !strings.Contains(m.err, "expired") {
		t.Fatalf("expired transition = %v %q", m.screen, m.err)
	}
}

func TestFollowEscapeCancelsOnlyLocalStream(t *testing.T) {
	client := &fakeClient{}
	m := consoleModel(client)
	open := m.startFollowing("job-1", 7)
	m.Update(open())
	if m.followJobID != "job-1" {
		t.Fatal("follow was not opened")
	}
	m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.followJobID != "" || client.lifecycleCalls != 0 {
		t.Fatal("Escape mutated server state")
	}
	if client.followCtx == nil || client.followCtx.Err() == nil {
		t.Fatal("follow context was not cancelled")
	}
}

func TestStaleFollowMessagesCannotReplaceOrStopCurrentFollow(t *testing.T) {
	m := consoleModel(&fakeClient{})
	oldOpen := m.startFollowing("old-job", 0)().(followOpenedMsg)
	newOpen := m.startFollowing("new-job", 0)().(followOpenedMsg)
	m.Update(newOpen)
	m.Update(oldOpen)
	if m.followJobID != "new-job" {
		t.Fatalf("stale open replaced follow: %q", m.followJobID)
	}
	m.Update(followEventMsg{generation: oldOpen.generation, done: true})
	if m.followJobID != "new-job" {
		t.Fatal("stale done stopped current follow")
	}
	m.Update(followEventMsg{generation: oldOpen.generation, err: errors.New("stale")})
	if m.followJobID != "new-job" || strings.Contains(m.View(), "stale") {
		t.Fatal("stale error affected current follow")
	}
}

func TestOverviewJobsAndClickableJobRow(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.jobs = []apicontract.Job{{ID: "active-job", Status: "running"}, {ID: "old-ok", Status: "succeeded"}, {ID: "failed-job", Status: "failed"}}
	m.resize()
	view := m.View()
	if !strings.Contains(view, "active-job") || !strings.Contains(view, "failed-job") || strings.Contains(view, "old-ok") {
		t.Fatalf("overview jobs missing/filtering incorrectly:\n%s", view)
	}
	if len(m.jobRects) == 0 {
		t.Fatal("job hit targets missing")
	}
	target := m.jobRects[0]
	_, cmd := m.handleMouse(tea.MouseMsg(tea.MouseEvent{X: target.x, Y: target.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
	if cmd == nil {
		t.Fatal("job row click did not dispatch detail command")
	}
}

func TestRunUsesInjectedFactoriesAndRunner(t *testing.T) {
	run := false
	err := Run(context.Background(), Config{
		Client: &fakeClient{},
		HistoryStoreFactory: func() (HistoryStore, error) {
			return &memoryHistoryStore{}, nil
		},
		ProgramRunner: func(model tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			run = true
			if _, ok := model.(*Model); !ok {
				t.Fatalf("runner model = %T", model)
			}
			return model, nil
		},
	})
	if err != nil || !run {
		t.Fatalf("Run = %v, called=%t", err, run)
	}
}
