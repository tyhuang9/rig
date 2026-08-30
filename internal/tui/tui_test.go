package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/secretfile"
)

type fakeClient struct {
	bootstrapStatus apicontract.BootstrapStatus
	bootstrapErr    error
	loginErr        error
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
	overviewStarted chan<- string
	overviewRelease <-chan struct{}
}

type delayedHistoryCall struct {
	kind   historyOperationKind
	values []string
}

// delayedHistoryStore makes the ordering of persistence completions explicit
// in tests. Each storage call waits for the test to release it.
type delayedHistoryStore struct {
	mu      sync.Mutex
	values  []string
	started chan delayedHistoryCall
	release chan error
}

func newDelayedHistoryStore(values []string) *delayedHistoryStore {
	return &delayedHistoryStore{
		values:  append([]string(nil), values...),
		started: make(chan delayedHistoryCall),
		release: make(chan error),
	}
}

func (s *delayedHistoryStore) Load(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.values...), nil
}

func (s *delayedHistoryStore) Save(ctx context.Context, values []string) error {
	call := delayedHistoryCall{kind: historySaveOperation, values: append([]string(nil), values...)}
	select {
	case s.started <- call:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-s.release:
		if err == nil {
			s.mu.Lock()
			s.values = append([]string(nil), call.values...)
			s.mu.Unlock()
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *delayedHistoryStore) Clear(ctx context.Context) error {
	select {
	case s.started <- delayedHistoryCall{kind: historyClearOperation}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-s.release:
		if err == nil {
			s.mu.Lock()
			s.values = nil
			s.mu.Unlock()
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *delayedHistoryStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.values...)
}

func startHistoryCommand(t *testing.T, cmd tea.Cmd) <-chan tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected history command")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	return result
}

func waitHistoryCall(t *testing.T, store *delayedHistoryStore) delayedHistoryCall {
	t.Helper()
	select {
	case call := <-store.started:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for history storage call")
		return delayedHistoryCall{}
	}
}

func finishHistoryCommand(t *testing.T, m *Model, store *delayedHistoryStore, result <-chan tea.Msg, err error) tea.Cmd {
	t.Helper()
	store.release <- err
	select {
	case msg := <-result:
		_, next := m.Update(msg)
		return next
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for history storage result")
		return nil
	}
}

func (f *fakeClient) waitOverview(ctx context.Context, operation string) error {
	if f.overviewStarted != nil {
		select {
		case f.overviewStarted <- operation:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.overviewRelease != nil {
		select {
		case <-f.overviewRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakeClient) BootstrapStatus(context.Context) (apicontract.BootstrapStatus, error) {
	return f.bootstrapStatus, f.bootstrapErr
}
func (f *fakeClient) Bootstrap(_ context.Context, request apicontract.BootstrapRequest) (apicontract.SessionResponse, error) {
	f.bootstrapCalls++
	return apicontract.SessionResponse{User: apicontract.User{Username: request.Username, Role: "admin"}}, nil
}
func (f *fakeClient) Login(_ context.Context, request apicontract.LoginRequest) (apicontract.SessionResponse, error) {
	return apicontract.SessionResponse{User: apicontract.User{Username: request.Username, Role: "admin"}}, f.loginErr
}
func (f *fakeClient) Logout(context.Context) error                       { f.logoutCalls++; return nil }
func (f *fakeClient) Me(context.Context) (apicontract.MeResponse, error) { return f.me, f.meErr }
func (f *fakeClient) Status(ctx context.Context) (apicontract.SystemStatus, error) {
	if err := f.waitOverview(ctx, "status"); err != nil {
		return apicontract.SystemStatus{}, err
	}
	return apicontract.SystemStatus{Daemon: "running", Diagnostics: apicontract.Diagnostics{EngineReady: true}}, nil
}
func (f *fakeClient) Doctor(context.Context) (apicontract.DoctorResponse, error) {
	return apicontract.DoctorResponse{Checks: []apicontract.DoctorCheck{{Name: "engine", OK: true}}}, nil
}
func (f *fakeClient) Applications(ctx context.Context) (apicontract.ApplicationList, error) {
	if err := f.waitOverview(ctx, "apps"); err != nil {
		return apicontract.ApplicationList{}, err
	}
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
func (f *fakeClient) Jobs(ctx context.Context) (apicontract.JobList, error) {
	if err := f.waitOverview(ctx, "jobs"); err != nil {
		return apicontract.JobList{}, err
	}
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
	m.overviewLoaded = true
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

func TestOverviewRequestsRunConcurrently(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	client := &fakeClient{overviewStarted: started, overviewRelease: release}
	m := NewModel(context.Background(), client, &memoryHistoryStore{}, "http://controller")
	result := make(chan tea.Msg, 1)
	go func() { result <- m.loadOverview()() }()
	seen := map[string]bool{}
	for len(seen) < 3 {
		select {
		case operation := <-started:
			seen[operation] = true
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("overview requests did not overlap; started=%v", seen)
		}
	}
	close(release)
	select {
	case raw := <-result:
		msg, ok := raw.(overviewMsg)
		if !ok || msg.err != nil || len(msg.apps.Items) != 2 || len(msg.jobs.Items) != 1 {
			t.Fatalf("overview result=%#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent overview did not finish")
	}
}

func TestOrdinaryInputDoesNotRebuildTranscript(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.appendEntry(entrySuccess, "/status", "daemon: running")
	m.transcriptBuilds = 0
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m.refreshSuggestions()
	m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	if m.transcriptBuilds != 0 {
		t.Fatalf("ordinary input rebuilt transcript %d times", m.transcriptBuilds)
	}
	m.Update(tea.WindowSizeMsg{Width: m.width - 10, Height: m.height})
	if m.transcriptBuilds != 1 {
		t.Fatalf("width change rebuilt transcript %d times", m.transcriptBuilds)
	}
}

func TestOverviewCompletionCannotReleaseCommandBusyState(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	client := &fakeClient{overviewStarted: started, overviewRelease: release}
	m := consoleModel(client)
	m.overviewLoaded = false
	overview := m.startOverview()
	result := make(chan tea.Msg, 1)
	go func() { result <- overview() }()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("overview did not begin all controller requests")
		}
	}
	mutation := m.execute(command{Name: "/start", Raw: "/start"})
	if mutation == nil || !m.busy {
		t.Fatal("mutation did not acquire the command busy guard")
	}
	close(release)
	select {
	case raw := <-result:
		m.Update(raw)
	case <-time.After(time.Second):
		t.Fatal("slow overview did not finish")
	}
	if !m.busy {
		t.Fatal("overview completion released an in-flight mutation busy guard")
	}
	m.commandInput.SetValue("/start")
	_, second := m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if second != nil || client.lifecycleCalls != 0 {
		t.Fatalf("busy state accepted a second mutation: calls=%d cmd=%v", client.lifecycleCalls, second)
	}

	oldGeneration := m.overviewGen
	m.overviewGen++
	m.overviewLoading = true
	m.status.Daemon = "fresh"
	m.Update(overviewMsg{generation: oldGeneration, status: apicontract.SystemStatus{Daemon: "stale"}})
	if m.status.Daemon != "fresh" || !m.overviewLoading || !m.busy {
		t.Fatalf("stale overview changed active state: daemon=%q loading=%t busy=%t", m.status.Daemon, m.overviewLoading, m.busy)
	}
	m.overviewGen++
	m.overviewLoading = true
	m.Update(overviewMsg{generation: m.overviewGen, err: errors.New("temporary overview failure")})
	if !m.busy || m.screen != screenConsole {
		t.Fatalf("overview failure interrupted the active command: busy=%t screen=%d", m.busy, m.screen)
	}
}

func TestOrdinaryInputAndViewReuseTranscriptCaches(t *testing.T) {
	for _, accessible := range []bool{false, true} {
		t.Run(fmt.Sprintf("accessible=%t", accessible), func(t *testing.T) {
			m := consoleModel(&fakeClient{})
			m.accessible = accessible
			for i := 0; i < transcriptLimit; i++ {
				m.appendEntry(entryEvent, "event", strings.Repeat("detail ", 200))
			}
			_ = m.View() // Populate the mode-specific cache before measuring input.
			m.transcriptBuilds = 0
			m.accessibleBuilds = 0
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			_ = m.View()
			if m.transcriptBuilds != 0 || m.accessibleBuilds != 0 {
				t.Fatalf("ordinary input rebuilt transcript: normal=%d accessible=%d", m.transcriptBuilds, m.accessibleBuilds)
			}
		})
	}
}

func TestTranscriptHasUnicodeSafeByteBudget(t *testing.T) {
	m := consoleModel(&fakeClient{})
	body := strings.Repeat("界", maxAPITextBytes)
	for i := 0; i < 100; i++ {
		entry := boundTranscriptEntry(transcriptEntry{Kind: entryEvent, Title: fmt.Sprintf("event-%d", i), Body: sanitizeAPIText(body)}, transcriptByteLimit)
		m.entries = append(m.entries, entry)
		m.transcriptBytes += transcriptEntryBytes(entry)
	}
	m.appendEntry(entryEvent, "latest", body)
	if m.transcriptBytes > transcriptByteLimit {
		t.Fatalf("transcript retained %d bytes, limit %d", m.transcriptBytes, transcriptByteLimit)
	}
	measured := 0
	for _, entry := range m.entries {
		measured += transcriptEntryBytes(entry)
		if !utf8.ValidString(entry.Title) || !utf8.ValidString(entry.Body) {
			t.Fatal("transcript trimming split a UTF-8 rune")
		}
	}
	if measured != m.transcriptBytes {
		t.Fatalf("tracked transcript bytes=%d measured=%d", m.transcriptBytes, measured)
	}
	m.execute(command{Name: "/clear"})
	if m.transcriptBytes != 0 || len(m.entries) != 0 {
		t.Fatalf("clear retained %d bytes across %d entries", m.transcriptBytes, len(m.entries))
	}
}

func BenchmarkOrdinaryInputWithLargeTranscript(b *testing.B) {
	m := consoleModel(&fakeClient{})
	for i := 0; i < transcriptLimit; i++ {
		m.appendEntry(entryEvent, "event", strings.Repeat("detail ", 200))
	}
	m.transcriptBuilds = 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	}
	b.StopTimer()
	if m.transcriptBuilds != 0 {
		b.Fatalf("ordinary input rebuilt transcript %d times", m.transcriptBuilds)
	}
}

func BenchmarkOrdinaryInputAndViewWithLargeTranscript(b *testing.B) {
	for _, accessible := range []bool{false, true} {
		b.Run(fmt.Sprintf("accessible=%t", accessible), func(b *testing.B) {
			m := consoleModel(&fakeClient{})
			m.accessible = accessible
			for i := 0; i < transcriptLimit; i++ {
				m.appendEntry(entryEvent, "event", strings.Repeat("detail ", 200))
			}
			_ = m.View()
			m.transcriptBuilds, m.accessibleBuilds = 0, 0
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
				_ = m.View()
			}
			b.StopTimer()
			if m.transcriptBuilds != 0 || m.accessibleBuilds != 0 {
				b.Fatalf("ordinary input rebuilt transcript: normal=%d accessible=%d", m.transcriptBuilds, m.accessibleBuilds)
			}
		})
	}
}

func TestProtectedHistoryLimitClearAndNoPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	store := NewProtectedHistoryStore(path)
	inputCount := historyLimit + 5
	values := make([]string, 0, inputCount)
	for i := 0; i < inputCount; i++ {
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
	firstRetained := inputCount - historyLimit
	lastRetained := inputCount - 1
	if len(loaded) != historyLimit || !strings.Contains(loaded[0], fmt.Sprintf("%03d", firstRetained)) || !strings.Contains(loaded[historyLimit-1], fmt.Sprintf("%03d", lastRetained)) {
		t.Fatalf("bounded history = %#v", loaded)
	}
	if runtime.GOOS == "windows" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "sensitive-marker") {
			t.Fatal("history file contains plaintext command")
		}
	} else {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("history file mode = %v, want a regular file", info.Mode())
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("history file permissions = %04o, want 0600", info.Mode().Perm())
		}
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

func TestAuthInputsResizeAndCompactLayoutsKeepAllFieldsVisible(t *testing.T) {
	types := []struct {
		name            string
		setup           func(*Model)
		labels          []string
		wideVisibleText string
	}{
		{
			name: "login",
			setup: func(m *Model) {
				m.showLogin("")
				m.authInputs[0].SetValue("operator-name")
				m.authInputs[1].SetValue("sensitive-passphrase")
			},
			labels:          []string{"Username:", "Passphrase:"},
			wideVisibleText: "operator",
		},
		{
			name: "bootstrap",
			setup: func(m *Model) {
				m.showBootstrap()
				m.authInputs[0].SetValue("bootstrap-token")
				m.authInputs[1].SetValue("administrator-name")
				m.authInputs[2].SetValue("sensitive-passphrase")
			},
			labels:          []string{"Bootstrap token:", "Administrator username:", "Passphrase:"},
			wideVisibleText: "administrator",
		},
	}
	for _, auth := range types {
		for _, size := range [][2]int{{80, 24}, {32, 8}, {40, 10}, {50, 12}} {
			t.Run(fmt.Sprintf("%s-%dx%d", auth.name, size[0], size[1]), func(t *testing.T) {
				m := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "http://controller")
				auth.setup(m)
				m.width, m.height = size[0], size[1]
				m.resize()
				view := ansi.Strip(m.View())
				for _, label := range auth.labels {
					if !strings.Contains(view, label) {
						t.Fatalf("auth field %q was cropped from %dx%d:\n%s", label, size[0], size[1], view)
					}
				}
				for i, input := range m.authInputs {
					if input.Width < 6 || ansi.StringWidth(ansi.Strip(input.View())) < 2 {
						t.Fatalf("auth input %d has unusable width=%d view=%q", i, input.Width, input.View())
					}
				}
				if size == [2]int{80, 24} && !strings.Contains(view, auth.wideVisibleText) {
					t.Fatalf("wide auth value was not visibly multi-character:\n%s", view)
				}
			})
		}
	}
}

func TestFailedLoginClearsCredentialsAndRefocusesUsername(t *testing.T) {
	client := &fakeClient{loginErr: errors.New("invalid credentials")}
	m := NewModel(context.Background(), client, &memoryHistoryStore{}, "http://controller")
	m.showLogin("")
	m.authInputs[0].SetValue("operator")
	m.authInputs[1].SetValue("bad-passphrase")
	_, cmd := m.submitAuth()
	if cmd == nil {
		t.Fatal("login did not start")
	}
	m.Update(cmd())
	if m.authIndex != 0 || !m.authInputs[0].Focused() || m.authInputs[0].Value() != "" || m.authInputs[1].Value() != "" {
		t.Fatalf("failed login did not clear and refocus username: index=%d username=%q passphrase=%q", m.authIndex, m.authInputs[0].Value(), m.authInputs[1].Value())
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

func TestHistorySaveSnapshotsBeforeAsyncWork(t *testing.T) {
	history := &memoryHistoryStore{}
	m := NewModel(context.Background(), &fakeClient{}, history, "endpoint")
	m.historyValues = []string{"/status"}
	run := m.enqueueHistorySave(m.historyValues)
	m.historyValues = []string{"/stop"}
	_ = run()
	values, err := history.Load(context.Background())
	if err != nil || len(values) != 1 || values[0] != "/status" {
		t.Fatalf("saved history=%v err=%v", values, err)
	}
}

func TestHistoryPersistenceSerializesOlderSnapshotsBeforeNewerRequests(t *testing.T) {
	store := newDelayedHistoryStore(nil)
	m := NewModel(context.Background(), &fakeClient{}, store, "endpoint")
	first := startHistoryCommand(t, m.enqueueHistorySave([]string{"/status"}))
	if call := waitHistoryCall(t, store); call.kind != historySaveOperation || !reflect.DeepEqual(call.values, []string{"/status"}) {
		t.Fatalf("first call = %#v", call)
	}
	if cmd := m.enqueueHistorySave([]string{"/jobs"}); cmd != nil {
		t.Fatal("newer history request started before older save completed")
	}
	if cmd := m.enqueueHistorySave([]string{"/apps"}); cmd != nil {
		t.Fatal("latest history request started before older save completed")
	}
	next := finishHistoryCommand(t, m, store, first, nil)
	second := startHistoryCommand(t, next)
	if call := waitHistoryCall(t, store); call.kind != historySaveOperation || !reflect.DeepEqual(call.values, []string{"/apps"}) {
		t.Fatalf("second call = %#v", call)
	}
	if next := finishHistoryCommand(t, m, store, second, nil); next != nil {
		t.Fatal("unexpected extra history operation")
	}
	if got := store.snapshot(); !reflect.DeepEqual(got, []string{"/apps"}) {
		t.Fatalf("persisted history = %v", got)
	}
}

func TestHistoryClearWaitsForPendingSaveAndCannotBeResurrected(t *testing.T) {
	store := newDelayedHistoryStore([]string{"/old"})
	m := NewModel(context.Background(), &fakeClient{}, store, "endpoint")
	first := startHistoryCommand(t, m.enqueueHistorySave([]string{"/status"}))
	_ = waitHistoryCall(t, store)
	if cmd := m.enqueueHistoryClear(); cmd != nil {
		t.Fatal("clear started before pending save completed")
	}
	next := finishHistoryCommand(t, m, store, first, nil)
	clear := startHistoryCommand(t, next)
	if call := waitHistoryCall(t, store); call.kind != historyClearOperation {
		t.Fatalf("clear call = %#v", call)
	}
	if next := finishHistoryCommand(t, m, store, clear, nil); next != nil {
		t.Fatal("unexpected history operation after clear")
	}
	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("clear was resurrected by stale save: %v", got)
	}
}

func TestHistoryClearThenLaterCommandPersistsOnlyPostClearHistory(t *testing.T) {
	store := newDelayedHistoryStore([]string{"/old"})
	m := NewModel(context.Background(), &fakeClient{}, store, "endpoint")
	clear := startHistoryCommand(t, m.enqueueHistoryClear())
	if call := waitHistoryCall(t, store); call.kind != historyClearOperation {
		t.Fatalf("clear call = %#v", call)
	}
	if cmd := m.enqueueHistorySave([]string{"/status"}); cmd != nil {
		t.Fatal("post-clear save started before clear completed")
	}
	next := finishHistoryCommand(t, m, store, clear, nil)
	save := startHistoryCommand(t, next)
	if call := waitHistoryCall(t, store); call.kind != historySaveOperation || !reflect.DeepEqual(call.values, []string{"/status"}) {
		t.Fatalf("post-clear call = %#v", call)
	}
	if next := finishHistoryCommand(t, m, store, save, nil); next != nil {
		t.Fatal("unexpected extra history operation")
	}
	if got := store.snapshot(); !reflect.DeepEqual(got, []string{"/status"}) {
		t.Fatalf("persisted post-clear history = %v", got)
	}
}

func TestLateHistoryLoadCannotReplaceCommandsAcceptedAfterStartup(t *testing.T) {
	m := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "endpoint")
	m.historyValues = []string{"/status"}
	_ = m.enqueueHistorySave(m.historyValues)
	_, bootstrap := m.Update(historyLoadedMsg{values: []string{"/old", "/jobs"}})
	if !reflect.DeepEqual(m.historyValues, []string{"/status"}) {
		t.Fatalf("late startup load overwrote accepted history: %v", m.historyValues)
	}
	if bootstrap == nil {
		t.Fatal("history load did not continue startup")
	}
}

func TestHistoryPersistenceReportsErrorsAndContinuesQueue(t *testing.T) {
	store := newDelayedHistoryStore(nil)
	m := consoleModel(&fakeClient{})
	m.history = store
	first := startHistoryCommand(t, m.enqueueHistorySave([]string{"/status"}))
	_ = waitHistoryCall(t, store)
	if cmd := m.enqueueHistorySave([]string{"/jobs"}); cmd != nil {
		t.Fatal("second save started before first completed")
	}
	next := finishHistoryCommand(t, m, store, first, errors.New("disk unavailable"))
	if len(m.entries) == 0 || m.entries[len(m.entries)-1].Title != "history" {
		t.Fatalf("save error was not visible: %#v", m.entries)
	}
	second := startHistoryCommand(t, next)
	_ = waitHistoryCall(t, store)
	if next := finishHistoryCommand(t, m, store, second, nil); next != nil {
		t.Fatal("unexpected extra history operation")
	}
	if got := store.snapshot(); !reflect.DeepEqual(got, []string{"/jobs"}) {
		t.Fatalf("queue did not continue after error: %v", got)
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

func TestAccessibleRunUsesPrimaryBufferOnly(t *testing.T) {
	for _, accessible := range []bool{false, true} {
		t.Run(fmt.Sprintf("accessible=%t", accessible), func(t *testing.T) {
			var optionCount int
			err := Run(context.Background(), Config{
				Client:              &fakeClient{},
				Accessible:          accessible,
				HistoryStoreFactory: func() (HistoryStore, error) { return &memoryHistoryStore{}, nil },
				ProgramRunner: func(_ tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
					optionCount = len(options)
					return nil, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if accessible {
				want = 1
			}
			if optionCount != want {
				t.Fatalf("program options = %d, want %d", optionCount, want)
			}
		})
	}
}

func TestRenderedFramesFitTerminalAndKeepCommandVisible(t *testing.T) {
	for _, size := range [][2]int{{32, 8}, {40, 10}, {50, 12}, {80, 24}, {80, 30}, {99, 30}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := consoleModel(&fakeClient{})
			m.width, m.height = size[0], size[1]
			m.commandInput.SetValue("/status")
			m.resize()
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) > size[1] {
				t.Fatalf("height=%d exceeds %d:\n%s", len(lines), size[1], view)
			}
			for _, line := range lines {
				if width := ansi.StringWidth(line); width > size[0] {
					t.Fatalf("line width=%d exceeds %d: %q", width, size[0], line)
				}
			}
			if !strings.Contains(ansi.Strip(view), "/status") {
				t.Fatalf("command prompt was clipped:\n%s", view)
			}
		})
	}
}

func TestAccessibleViewIsLinearMonochromeAndChronological(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.accessible = true
	m.width, m.height = 120, 30
	m.entries = nil
	m.appendEntry(entryCommand, "you", "/status")
	m.appendEntry(entrySuccess, "/status", "daemon: running")
	m.appendEntry(entryEvent, "running", "halfway")
	view := m.View()
	for _, text := range []string{"Endpoint:", "Connection state: connected", "COMMAND you", "RESULT /status", "EVENT running", "Command:"} {
		if !strings.Contains(view, text) {
			t.Fatalf("accessible view missing %q:\n%s", text, view)
		}
	}
	if strings.Contains(view, "\x1b[") || strings.Contains(view, "●") || strings.Contains(view, "✓") {
		t.Fatalf("accessible view contains presentation-only output: %q", view)
	}
}

func TestAccessibleTranscriptPagingAndAuthMinimumSize(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.accessible = true
	m.width, m.height = 80, 12
	m.entries = nil
	for i := 0; i < 4; i++ {
		m.appendEntry(entryEvent, fmt.Sprintf("event-%d", i), fmt.Sprintf("body-%d", i))
	}
	latest := m.View()
	if !strings.Contains(latest, "event-3") || strings.Contains(latest, "event-0") {
		t.Fatalf("latest accessible page was not bounded to newest entries:\n%s", latest)
	}
	m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	older := m.View()
	if !strings.Contains(older, "event-0") || !strings.Contains(older, "Transcript page 2 of") {
		t.Fatalf("PgUp did not expose an older accessible page:\n%s", older)
	}
	m.handleConsoleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if current := m.View(); !strings.Contains(current, "event-3") {
		t.Fatalf("PgDn did not return to the newest accessible page:\n%s", current)
	}

	login := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "http://controller")
	login.showLogin("")
	login.width, login.height = 31, 7
	login.resize()
	if view := ansi.Strip(login.View()); !strings.Contains(view, "Terminal too small") {
		t.Fatalf("auth view bypassed minimum-size guard: %q", view)
	}
}

func TestNoColorAndANSISafeTruncation(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := consoleModel(&fakeClient{})
	if view := m.View(); strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR view contains ANSI styling: %q", view)
	}
	styled := "\x1b[31m界界界\x1b[0m"
	got := cropWidth(styled, 5)
	if ansi.StringWidth(got) > 5 || ansi.Strip(got) != "界界…" {
		t.Fatalf("ANSI-aware truncation = %q (%q, width=%d)", got, ansi.Strip(got), ansi.StringWidth(got))
	}
}

func TestAuthLabelsFocusFirstInvalidAndFocusRestores(t *testing.T) {
	m := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "http://controller")
	m.showLogin("")
	m.width, m.height = 80, 24
	m.resize()
	if view := m.View(); !strings.Contains(view, "Username:") || !strings.Contains(view, "Passphrase:") {
		t.Fatalf("auth labels missing: %q", view)
	}
	m.authInputs[1].SetValue("present")
	_, cmd := m.submitAuth()
	if cmd != nil || m.authIndex != 0 || !m.authInputs[0].Focused() || m.err != "Username is required." {
		t.Fatalf("invalid auth did not focus username: index=%d focused=%t error=%q", m.authIndex, m.authInputs[0].Focused(), m.err)
	}

	console := consoleModel(&fakeClient{})
	console.commandInput.SetValue("/stop")
	console.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if console.commandInput.Focused() {
		t.Fatal("command input remained focused behind confirmation")
	}
	console.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus after cancel")
	}
	console.commandInput.Blur()
	console.busy = true
	console.Update(commandResultMsg{cmd: command{Name: "/status"}, body: "daemon: running"})
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus after result")
	}

	console = consoleModel(&fakeClient{})
	console.commandInput.SetValue("/stop")
	console.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEnter})
	console.resize()
	console.handleMouse(tea.MouseMsg(tea.MouseEvent{X: console.cancelRect.x, Y: console.cancelRect.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus after mouse cancellation")
	}

	console.commandInput.Blur()
	console.busy = true
	_, follow := console.Update(commandResultMsg{cmd: command{Name: "/start"}, followID: "job-1"})
	if follow == nil || !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus when lifecycle follow began")
	}
	console.commandInput.Blur()
	console.stopFollowing(false)
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus when follow ended")
	}
	console.startFollowing("job-2", 0)
	console.commandInput.Blur()
	console.handleConsoleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus when Escape stopped follow")
	}
	console.startFollowing("job-3", 0)
	console.commandInput.Blur()
	console.Update(followEventMsg{generation: console.followGeneration, event: apicontract.JobEvent{Code: "job_succeeded"}})
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus when terminal follow event arrived")
	}
	console.startFollowing("job-4", 0)
	console.commandInput.Blur()
	console.Update(followEventMsg{generation: console.followGeneration, err: errors.New("stream disconnected")})
	if !console.commandInput.Focused() {
		t.Fatal("command input did not regain focus when follow failed")
	}

	bootstrap := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "http://controller")
	bootstrap.showBootstrap()
	bootstrap.focusAuth(2)
	bootstrap.Update(authMsg{err: errors.New("bootstrap rejected")})
	if bootstrap.authIndex != 0 || !bootstrap.authInputs[0].Focused() {
		t.Fatal("failed bootstrap did not refocus the bootstrap token field")
	}
}

func TestOfflineLoadingMergeSelectionAndFooterStates(t *testing.T) {
	m := consoleModel(&fakeClient{})
	m.overviewLoaded = false
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Overview") || strings.Contains(view, "not ready") {
		t.Fatalf("initial overview implied failure: %q", view)
	}
	m.goOffline(errors.New("dial refused"))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Endpoint: http://controller") || !strings.Contains(view, "hostd serve") {
		t.Fatalf("offline recovery text missing: %q", view)
	}
	m = consoleModel(&fakeClient{})
	m.Update(commandResultMsg{cmd: command{Name: "/stop"}, job: &apicontract.Job{ID: "new", Progress: 42, Status: "running"}})
	if eventProgress(apicontract.JobEvent{JobID: "new"}, m.jobs) != 42 {
		t.Fatalf("mutation job was not merged: %#v", m.jobs)
	}
	m.selectedAppID = "gone"
	m.Update(overviewMsg{apps: apicontract.ApplicationList{Items: []apicontract.Application{{ID: "remaining", Slug: "kept"}}}})
	if m.selectedAppID != "remaining" || m.selectedAppName() != "kept" {
		t.Fatalf("selected app was not reconciled: %q / %q", m.selectedAppID, m.selectedAppName())
	}
	if footer := ansi.Strip(m.renderFooter()); !strings.Contains(footer, "Endpoint:") || !strings.Contains(footer, "App:") || !strings.Contains(footer, "State:") {
		t.Fatalf("footer summary incomplete: %q", footer)
	}
}

func TestRunCancelsModelWorkOnProgramExitAndRejectsBusyInput(t *testing.T) {
	var model *Model
	err := Run(context.Background(), Config{
		Client:              &fakeClient{},
		HistoryStoreFactory: func() (HistoryStore, error) { return &memoryHistoryStore{}, nil },
		ProgramRunner: func(value tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			model = value.(*Model)
			model.startFollowing("job", 0)
			return value, nil
		},
	})
	if err != nil || model == nil || model.followContext == nil || model.followContext.Err() == nil {
		t.Fatalf("Run did not cancel model follow context: err=%v model=%#v", err, model)
	}

	console := consoleModel(&fakeClient{})
	console.busy = true
	console.confirm = &confirmation{Command: command{Name: "/stop"}, Text: "stop"}
	console.resize()
	console.handleMouse(tea.MouseMsg(tea.MouseEvent{X: console.confirmRect.x, Y: console.confirmRect.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
	if console.confirm == nil {
		t.Fatal("busy mouse interaction accepted a confirmation")
	}
	login := NewModel(context.Background(), &fakeClient{}, &memoryHistoryStore{}, "endpoint")
	login.showLogin("")
	login.busy = true
	if _, cmd := login.submitAuth(); cmd != nil {
		t.Fatal("busy authentication submission started a second request")
	}
}
