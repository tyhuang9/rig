package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/sourceconnections"
)

type controllerProvider struct {
	deviceCalls       int
	pollCalls         int
	installationCalls int
	pollError         error
	repository        githubapp.Repository
	repositoryPage    githubapp.RepositoryPage
	branch            githubapp.Branch
	branchPage        githubapp.BranchPage
	tree              githubapp.Tree
	content           []byte
}

func (provider *controllerProvider) StartDevice(context.Context) (githubapp.DeviceAuthorization, error) {
	provider.deviceCalls++
	return githubapp.DeviceAuthorization{DeviceCode: "device-code-sentinel", UserCode: "USER-CODE", VerificationURI: githubapp.VerificationURI, ExpiresIn: 15 * time.Minute, Interval: 5 * time.Second}, nil
}
func (provider *controllerProvider) PollDevice(context.Context, string) (githubapp.TokenBundle, error) {
	provider.pollCalls++
	if provider.pollError != nil {
		return githubapp.TokenBundle{}, provider.pollError
	}
	return githubapp.TokenBundle{AccessToken: "ghu_api_sentinel", RefreshToken: "ghr_api_sentinel", AccessExpiresIn: time.Hour, RefreshExpiresIn: 24 * time.Hour}, nil
}
func (provider *controllerProvider) Refresh(context.Context, string) (githubapp.TokenBundle, error) {
	return githubapp.TokenBundle{AccessToken: "access-refreshed", RefreshToken: "refresh-refreshed", AccessExpiresIn: time.Hour, RefreshExpiresIn: 24 * time.Hour}, nil
}
func (provider *controllerProvider) CurrentUser(context.Context, string) (githubapp.User, error) {
	return githubapp.User{ID: "42", Login: "octo"}, nil
}
func (provider *controllerProvider) Installations(context.Context, string, int, int) (githubapp.InstallationPage, error) {
	provider.installationCalls++
	return githubapp.InstallationPage{}, nil
}
func (provider *controllerProvider) Repositories(context.Context, string, int64, int, int) (githubapp.RepositoryPage, error) {
	return provider.repositoryPage, nil
}
func (provider *controllerProvider) Repository(context.Context, string, int64, int64) (githubapp.Repository, error) {
	return provider.repository, nil
}
func (provider *controllerProvider) Branches(context.Context, string, int64, int, int) (githubapp.BranchPage, error) {
	return provider.branchPage, nil
}
func (provider *controllerProvider) Branch(context.Context, string, int64, string) (githubapp.Branch, error) {
	return provider.branch, nil
}
func (provider *controllerProvider) Tree(context.Context, string, int64, string) (githubapp.Tree, error) {
	return provider.tree, nil
}
func (provider *controllerProvider) Content(context.Context, string, int64, string, string) ([]byte, error) {
	return provider.content, nil
}

type controllerClock struct{ now time.Time }

func (clock *controllerClock) Time() time.Time { return clock.now }

type sourceHarness struct {
	handler      http.Handler
	session      auth.Session
	otherSession auth.Session
	service      *sourceconnections.Service
	repository   *sourceconnections.Repository
	credentials  *sourceconnections.FileCredentialStore
	provider     *controllerProvider
	clock        *controllerClock
	logs         *bytes.Buffer
}

func TestSourceConnectionAPIAuthenticationCSRFAndDisabledCleanup(t *testing.T) {
	harness := newSourceHarness(t, false)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/source-connections", nil)
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d", response.Code)
	}

	listed := sourceRequest(harness.handler, harness.session, http.MethodGet, "/api/v1/source-connections", "", true)
	if listed.Code != http.StatusOK {
		t.Fatalf("disabled list = %d %s", listed.Code, listed.Body.String())
	}
	start := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/github/device", "", true)
	if start.Code != http.StatusServiceUnavailable || !strings.Contains(start.Body.String(), "provider_unavailable") {
		t.Fatalf("disabled start = %d %s", start.Code, start.Body.String())
	}
	if harness.provider.deviceCalls != 0 {
		t.Fatalf("disabled start made %d provider calls", harness.provider.deviceCalls)
	}
	disabledStatus := sourceRequest(harness.handler, harness.session, http.MethodGet, "/api/v1/system/status", "", true)
	if !strings.Contains(disabledStatus.Body.String(), `"githubConnections":false`) {
		t.Fatalf("disabled capability = %s", disabledStatus.Body.String())
	}

	now := harness.clock.Time()
	connection, err := harness.repository.CreatePending(context.Background(), harness.sessionUserID(t, harness.session), now.Add(time.Minute), 5*time.Second, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.credentials.WriteDevice(connection.ID, "cleanup-device"); err != nil {
		t.Fatal(err)
	}
	deleted := sourceRequest(harness.handler, harness.session, http.MethodDelete, "/api/v1/source-connections/"+connection.ID, "", true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("disabled disconnect = %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := harness.credentials.ReadDevice(connection.ID); err == nil {
		t.Fatal("disabled disconnect left credential file")
	}

	for _, endpoint := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/source-connections/github/device"},
		{http.MethodPost, "/api/v1/source-connections/" + connection.ID + "/device/poll"},
		{http.MethodPost, "/api/v1/source-connections/" + connection.ID + "/refresh"},
		{http.MethodDelete, "/api/v1/source-connections/" + connection.ID},
	} {
		withoutCSRF := sourceRequest(harness.handler, harness.session, endpoint.method, endpoint.path, "", false)
		if withoutCSRF.Code != http.StatusForbidden || !strings.Contains(withoutCSRF.Body.String(), "csrf_failed") {
			t.Errorf("%s %s without CSRF = %d %s", endpoint.method, endpoint.path, withoutCSRF.Code, withoutCSRF.Body.String())
		}
	}
}

func TestGitHubRepositoryBranchInspectionAndApplicationCreationAPI(t *testing.T) {
	harness := newSourceHarness(t, true)
	owner := harness.sessionUserID(t, harness.session)
	started, err := harness.service.Start(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	harness.clock.now = harness.clock.now.Add(5 * time.Second)
	connection, err := harness.service.Poll(context.Background(), owner, started.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	harness.provider.repository = githubapp.Repository{ID: 77, Owner: "new-owner", Name: "renamed", DefaultBranch: "feature/slash", Private: true}
	harness.provider.repositoryPage = githubapp.RepositoryPage{TotalCount: 1, Repositories: []githubapp.Repository{harness.provider.repository}}
	harness.provider.branch = githubapp.Branch{Name: "feature/slash", SHA: sha, Protected: true}
	harness.provider.branchPage = githubapp.BranchPage{Branches: []githubapp.Branch{harness.provider.branch}}
	harness.provider.tree = githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "deploy/compose.yaml", Type: "blob", SHA: sha}}}
	harness.provider.content = []byte("services:\n  api:\n    image: example/api\n")
	base := "/api/v1/source-connections/" + connection.ID + "/github/installations/9/repositories"
	repositories := sourceRequest(harness.handler, harness.session, http.MethodGet, base+"?page=1&perPage=30", "", true)
	if repositories.Code != http.StatusOK || !strings.Contains(repositories.Body.String(), `"owner":"new-owner"`) {
		t.Fatalf("repositories=%d %s", repositories.Code, repositories.Body.String())
	}
	branches := sourceRequest(harness.handler, harness.session, http.MethodGet, base+"/77/branches", "", true)
	if branches.Code != http.StatusOK || !strings.Contains(branches.Body.String(), `"name":"feature/slash"`) {
		t.Fatalf("branches=%d %s", branches.Code, branches.Body.String())
	}
	githubJSON := `{"connectionId":"` + connection.ID + `","installationId":9,"repositoryId":77,"branch":"feature/slash","composePath":"deploy/compose.yaml"}`
	inspected := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/apps/import/inspect", `{"githubSource":`+githubJSON+`}`, true)
	if inspected.Code != http.StatusOK || !strings.Contains(inspected.Body.String(), `"resolvedSha":"`+sha+`"`) || !strings.Contains(inspected.Body.String(), `"name":"api"`) {
		t.Fatalf("inspect=%d %s", inspected.Code, inspected.Body.String())
	}
	created := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/apps", `{"name":"GitHub App","githubSource":`+githubJSON+`}`, true)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"repositoryName":"renamed"`) {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	assertAbsent(t, repositories.Body.String()+branches.Body.String()+inspected.Body.String()+created.Body.String()+harness.logs.String(), "ghu_api_sentinel", "ghr_api_sentinel", "device-code-sentinel")
}

func TestInspectRequiresExactlyOneSourceAndLocalInspectionRemainsUseful(t *testing.T) {
	harness := newSourceHarness(t, true)
	for _, body := range []string{`{}`, `{"sourcePath":"C:/local","githubSource":{"connectionId":"x","installationId":1,"repositoryId":1,"branch":"main"}}`} {
		response := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/apps/import/inspect", body, true)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_source") {
			t.Fatalf("exact-one=%d %s", response.Code, response.Body.String())
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  local:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/apps/import/inspect", `{"sourcePath":`+strconv.Quote(root)+`}`, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"local"`) {
		t.Fatalf("local=%d %s", response.Code, response.Body.String())
	}
}

func TestSourceConnectionAPIUsesNoStoreOwnerScopeAndFixedSafeProblems(t *testing.T) {
	harness := newSourceHarness(t, true)
	started := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/github/device", "", true)
	if started.Code != http.StatusCreated || started.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start = %d headers=%#v body=%s", started.Code, started.Header(), started.Body.String())
	}
	var body struct {
		ConnectionID string `json:"connectionId"`
		UserCode     string `json:"userCode"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.UserCode != "USER-CODE" || body.ConnectionID == "" {
		t.Fatalf("start body = %s", started.Body.String())
	}
	assertAbsent(t, started.Body.String()+harness.logs.String(), "device-code-sentinel", "ghu_api_sentinel", "ghr_api_sentinel", "raw provider description")
	listed := sourceRequest(harness.handler, harness.session, http.MethodGet, "/api/v1/source-connections", "", true)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"installUrl":"https://github.com/apps/hostd-app/installations/new"`) || !strings.Contains(listed.Body.String(), `"pendingExpiresAt"`) || !strings.Contains(listed.Body.String(), `"nextPollAt"`) {
		t.Fatalf("pending connection metadata = %d %s", listed.Code, listed.Body.String())
	}
	assertAbsent(t, listed.Body.String()+harness.logs.String(), "USER-CODE", "userCode", "verificationUri", "device-code-sentinel", "ghu_api_sentinel", "ghr_api_sentinel")
	enabledStatus := sourceRequest(harness.handler, harness.session, http.MethodGet, "/api/v1/system/status", "", true)
	if !strings.Contains(enabledStatus.Body.String(), `"githubConnections":true`) {
		t.Fatalf("enabled capability = %s", enabledStatus.Body.String())
	}

	otherList := sourceRequest(harness.handler, harness.otherSession, http.MethodGet, "/api/v1/source-connections", "", true)
	if strings.Contains(otherList.Body.String(), body.ConnectionID) {
		t.Fatalf("other owner listed connection: %s", otherList.Body.String())
	}
	otherDelete := sourceRequest(harness.handler, harness.otherSession, http.MethodDelete, "/api/v1/source-connections/"+body.ConnectionID, "", true)
	if otherDelete.Code != http.StatusNotFound {
		t.Fatalf("other owner delete = %d %s", otherDelete.Code, otherDelete.Body.String())
	}
	otherPoll := sourceRequest(harness.handler, harness.otherSession, http.MethodPost, "/api/v1/source-connections/"+body.ConnectionID+"/device/poll", "", true)
	if otherPoll.Code != http.StatusNotFound || harness.provider.pollCalls != 0 {
		t.Fatalf("other owner poll = %d calls=%d body=%s", otherPoll.Code, harness.provider.pollCalls, otherPoll.Body.String())
	}

	invalidPage := sourceRequest(harness.handler, harness.session, http.MethodGet, "/api/v1/source-connections/"+body.ConnectionID+"/github/installations?perPage=101", "", true)
	if invalidPage.Code != http.StatusBadRequest || harness.provider.installationCalls != 0 {
		t.Fatalf("invalid pagination = %d calls=%d", invalidPage.Code, harness.provider.installationCalls)
	}

	early := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/"+body.ConnectionID+"/device/poll", "", true)
	if early.Code != http.StatusTooManyRequests || early.Header().Get("Retry-After") == "" || harness.provider.pollCalls != 0 {
		t.Fatalf("early poll = %d retry=%q calls=%d body=%s", early.Code, early.Header().Get("Retry-After"), harness.provider.pollCalls, early.Body.String())
	}
	harness.clock.now = harness.clock.now.Add(5 * time.Second)
	harness.provider.pollError = &githubapp.Error{Code: "authorization_pending"}
	pending := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/"+body.ConnectionID+"/device/poll", "", true)
	if pending.Code != http.StatusAccepted || pending.Header().Get("Retry-After") == "" || !strings.Contains(pending.Body.String(), `"status":"pending"`) {
		t.Fatalf("pending poll = %d retry=%q body=%s", pending.Code, pending.Header().Get("Retry-After"), pending.Body.String())
	}
	if !strings.Contains(pending.Body.String(), `"installUrl":"https://github.com/apps/hostd-app/installations/new"`) {
		t.Fatalf("pending poll install URL = %s", pending.Body.String())
	}
	assertAbsent(t, pending.Body.String()+harness.logs.String(), "USER-CODE", "userCode", "verificationUri", "device-code-sentinel", "ghu_api_sentinel", "ghr_api_sentinel", "raw provider description")

	harness.clock.now = harness.clock.now.Add(5 * time.Second)
	harness.provider.pollError = errors.New("raw provider description")
	failed := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/"+body.ConnectionID+"/device/poll", "", true)
	if failed.Code != http.StatusServiceUnavailable || !strings.Contains(failed.Body.String(), "GitHub is temporarily unavailable") {
		t.Fatalf("provider failure = %d %s", failed.Code, failed.Body.String())
	}
	assertAbsent(t, failed.Body.String()+harness.logs.String(), "raw provider description")

	harness.clock.now = harness.clock.now.Add(5 * time.Second)
	harness.provider.pollError = nil
	connected := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/"+body.ConnectionID+"/device/poll", "", true)
	if connected.Code != http.StatusOK {
		t.Fatalf("poll = %d %s", connected.Code, connected.Body.String())
	}
	assertAbsent(t, connected.Body.String()+harness.logs.String(), "device-code-sentinel", "ghu_api_sentinel", "ghr_api_sentinel", "raw provider description")

	if _, err := harness.credentials.ReadBundle(body.ConnectionID); err != nil {
		t.Fatal(err)
	}
	// The connected poll is local and must not expose or log provider material.
	safePoll := sourceRequest(harness.handler, harness.session, http.MethodPost, "/api/v1/source-connections/"+body.ConnectionID+"/device/poll", "", true)
	if safePoll.Code != http.StatusOK {
		t.Fatalf("connected poll = %d %s", safePoll.Code, safePoll.Body.String())
	}
	assertAbsent(t, safePoll.Body.String()+harness.logs.String(), "device-code-sentinel", "ghu_api_sentinel", "ghr_api_sentinel", "raw provider description")
}

func newSourceHarness(t *testing.T, enabled bool) *sourceHarness {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authService := auth.New(db)
	token, err := authService.EnsureBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := authService.Bootstrap(token, "admin", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, username, passphrase_hash, role, created_at, updated_at) SELECT 'other-user', 'other', passphrase_hash, role, datetime('now'), datetime('now') FROM users WHERE username = 'admin'`); err != nil {
		t.Fatal(err)
	}
	_, otherSession, err := authService.Login("other", "a sufficiently long local passphrase")
	if err != nil {
		t.Fatal(err)
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		t.Fatal(err)
	}
	provider := &controllerProvider{}
	var configured sourceconnections.Provider
	appSlug := ""
	if enabled {
		configured, appSlug = provider, "hostd-app"
	}
	clock := &controllerClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	repository := sourceconnections.NewRepository(db)
	credentials := sourceconnections.NewFileCredentialStore(root)
	service := sourceconnections.NewService(repository, configured, credentials, appSlug, clock.Time)
	logs := &bytes.Buffer{}
	server := &controller.Server{Auth: authService, Apps: apps.New(db), Jobs: jobs.New(db), Machines: machineStore, Sources: service, Logger: slog.New(slog.NewJSONHandler(logs, nil))}
	return &sourceHarness{handler: server.Handler(), session: session, otherSession: otherSession, service: service, repository: repository, credentials: credentials, provider: provider, clock: clock, logs: logs}
}

func (harness *sourceHarness) sessionUserID(t *testing.T, session auth.Session) string {
	t.Helper()
	// The bootstrap user is the owner for the primary session; resolve it from
	// the authenticated API response without exposing auth internals.
	response := sourceRequest(harness.handler, session, http.MethodGet, "/api/v1/auth/me", "", true)
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.User.ID
}

func sourceRequest(handler http.Handler, session auth.Session, method, path, body string, csrf bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	request.Header.Set("Content-Type", "application/json")
	if csrf && method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", session.CSRF)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertAbsent(t *testing.T, value string, sentinels ...string) {
	t.Helper()
	for _, sentinel := range sentinels {
		if strings.Contains(value, sentinel) {
			t.Errorf("output contains sentinel %q: %s", sentinel, value)
		}
	}
}
