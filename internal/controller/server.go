package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/runtime/docker"
	"github.com/hostd/hostd/internal/sourceconnections"
)

//go:embed all:ui
var web embed.FS

type Server struct {
	Auth               authenticationService
	Apps               *apps.Store
	Jobs               *jobs.Service
	Machines           *machines.Store
	Caddy              bool
	FakeRuntime        bool
	DockerEndpoint     string
	DataRoot           string
	Logger             *slog.Logger
	BootstrapCompleted func()
	Sources            *sourceconnections.Service
	authenticationWork *authenticationWorkGate
}

type authenticationService interface {
	BootstrapStatus() (bool, error)
	Bootstrap(string, string, string) (auth.User, auth.Session, error)
	Login(string, string) (auth.User, auth.Session, error)
	Authenticate(string) (auth.User, string, error)
	CheckCSRF(string, string) bool
	RotateCSRF(string) (string, error)
	Logout(string) error
}
type principalKey struct{}
type principal struct {
	user     auth.User
	csrfHash string
}

func (s *Server) Handler() http.Handler { return s.requestID(s.logRequests(s.routes())) }

type apiRoute struct {
	method      string
	path        string
	operationID string
	handler     http.HandlerFunc
}

func (s *Server) apiRoutes() []apiRoute {
	return []apiRoute{
		contractRoute("bootstrapStatus", s.bootstrapStatus),
		contractRoute("bootstrap", s.bootstrap),
		contractRoute("login", s.login),
		contractRoute("logout", s.require(s.logout)),
		contractRoute("me", s.require(s.me)),
		contractRoute("rotateCSRF", s.require(s.rotateCSRF)),
		contractRoute("systemStatus", s.require(s.status)),
		contractRoute("doctor", s.require(s.doctor)),
		contractRoute("listApplications", s.require(s.listApps)),
		contractRoute("createApplication", s.require(s.createApp)),
		contractRoute("inspectImport", s.require(s.inspectApp)),
		contractRoute("getApplication", s.require(s.getApp)),
		contractRoute("listServices", s.require(s.services)),
		contractRoute("deployApplication", s.require(s.action("deploy"))),
		contractRoute("startApplication", s.require(s.action("start"))),
		contractRoute("stopApplication", s.require(s.action("stop"))),
		contractRoute("restartApplication", s.require(s.action("restart"))),
		contractRoute("streamLogs", s.require(s.logsStream)),
		contractRoute("listJobs", s.require(s.listJobs)),
		contractRoute("getJob", s.require(s.getJob)),
		contractRoute("cancelJob", s.require(s.cancelJob)),
		contractRoute("listJobEvents", s.require(s.events)),
		contractRoute("streamJobEvents", s.require(s.eventsStream)),
		contractRoute("listMachines", s.require(s.listMachines)),
		contractRoute("listSourceConnections", s.require(s.listSourceConnections)),
		contractRoute("startGitHubDeviceConnection", s.require(s.startGitHubDeviceConnection)),
		contractRoute("pollGitHubDeviceConnection", s.require(s.pollGitHubDeviceConnection)),
		contractRoute("refreshSourceConnection", s.require(s.refreshSourceConnection)),
		contractRoute("disconnectSourceConnection", s.require(s.disconnectSourceConnection)),
		contractRoute("listGitHubInstallations", s.require(s.listGitHubInstallations)),
	}
}

func contractRoute(operationID string, handler http.HandlerFunc) apiRoute {
	operation, ok := apicontract.Operations[operationID]
	if !ok {
		panic("OpenAPI operation artifact missing " + operationID)
	}
	return apiRoute{method: operation.Method, path: operation.Path, operationID: operationID, handler: handler}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	for _, route := range s.apiRoutes() {
		mux.HandleFunc(route.method+" "+route.path, route.handler)
	}
	mux.HandleFunc("/", s.spa)
	return mux
}
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type requestIDKey struct{}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Logger.Info("request", "request_id", r.Context().Value(requestIDKey{}), "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
func (s *Server) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(auth.SessionCookie)
		if err != nil {
			problem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", nil)
			return
		}
		u, csrf, err := s.Auth.Authenticate(c.Value)
		if err != nil {
			problem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", nil)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !s.Auth.CheckCSRF(csrf, r.Header.Get("X-CSRF-Token")) {
			problem(w, r, http.StatusForbidden, "csrf_failed", "CSRF validation failed", nil)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{u, csrf})))
	}
}
func requestID(r *http.Request) string { v, _ := r.Context().Value(requestIDKey{}).(string); return v }
func problem(w http.ResponseWriter, r *http.Request, status int, code, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apicontract.Problem{Type: "https://hostd.local/problems/" + code, Title: http.StatusText(status), Status: status, Detail: detail, Code: code, RequestID: requestID(r), Errors: fields})
}
func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain a single JSON object")
	}
	return nil
}

const (
	maxAuthRequestBodyBytes = 4 << 10
	maxAuthTokenBytes       = 256
	maxAuthUsernameBytes    = 128
	maxAuthPassphraseBytes  = 1024
	authWorkCapacity        = 2
	authAttemptsPerWindow   = 8
	authAttemptWindow       = time.Minute
	maxAuthRateClients      = 1024
)

var (
	errAuthContentType  = errors.New("authentication requests require application/json")
	errAuthBodyTooLarge = errors.New("authentication request body is too large")
)

var defaultAuthenticationWorkGate = newAuthenticationWorkGate(
	time.Now,
	authWorkCapacity,
	authAttemptsPerWindow,
	authAttemptWindow,
	maxAuthRateClients,
)

type authenticationWorkGate struct {
	workers chan struct{}
	rates   *authRateLimiter
}

type authRateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	limit   int
	window  time.Duration
	maxKeys int
	entries map[string]authRateEntry
}

type authRateEntry struct {
	started  time.Time
	seen     time.Time
	attempts int
}

func newAuthenticationWorkGate(now func() time.Time, capacity, limit int, window time.Duration, maxKeys int) *authenticationWorkGate {
	return &authenticationWorkGate{
		workers: make(chan struct{}, capacity),
		rates: &authRateLimiter{
			now: now, limit: limit, window: window, maxKeys: maxKeys, entries: make(map[string]authRateEntry),
		},
	}
}

func (g *authenticationWorkGate) admit(remoteAddr string) (bool, int) {
	if allowed, retryAfter := g.rates.allow(authClientKey(remoteAddr)); !allowed {
		return false, retryAfter
	}
	select {
	case g.workers <- struct{}{}:
		return true, 0
	default:
		return false, 1
	}
}

func (g *authenticationWorkGate) release() { <-g.workers }

func (l *authRateLimiter) allow(client string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for key, entry := range l.entries {
		if !now.Before(entry.seen.Add(l.window)) {
			delete(l.entries, key)
		}
	}

	entry, ok := l.entries[client]
	if !ok && l.maxKeys > 0 && len(l.entries) >= l.maxKeys {
		var oldestKey string
		var oldest time.Time
		for key, value := range l.entries {
			if oldestKey == "" || value.seen.Before(oldest) {
				oldestKey, oldest = key, value.seen
			}
		}
		delete(l.entries, oldestKey)
	}
	if !ok || !now.Before(entry.started.Add(l.window)) {
		entry = authRateEntry{started: now}
	}
	if entry.attempts >= l.limit {
		entry.seen = now
		l.entries[client] = entry
		return false, retryAfterSeconds(entry.started.Add(l.window).Sub(now))
	}
	entry.attempts++
	entry.seen = now
	l.entries[client] = entry
	return true, 0
}

func authClientKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	if host == "" {
		return "unknown"
	}
	return host
}

func retryAfterSeconds(wait time.Duration) int {
	if wait <= 0 {
		return 1
	}
	seconds := int(wait / time.Second)
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func (s *Server) authenticationWorkGate() *authenticationWorkGate {
	if s.authenticationWork != nil {
		return s.authenticationWork
	}
	return defaultAuthenticationWorkGate
}

func (s *Server) admitAuthenticationWork(w http.ResponseWriter, r *http.Request) bool {
	allowed, retryAfter := s.authenticationWorkGate().admit(r.RemoteAddr)
	if allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	problem(w, r, http.StatusTooManyRequests, "auth_rate_limited", "Too many authentication attempts. Try again later.", nil)
	return false
}

func readAuthJSON(r *http.Request, dst any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errAuthContentType
	}
	defer r.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxAuthRequestBodyBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxAuthRequestBodyBytes {
		return errAuthBodyTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain a single JSON object")
	}
	return nil
}

func authRequestProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAuthContentType):
		problem(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
	case errors.Is(err, errAuthBodyTooLarge):
		problem(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "Authentication request is too large", nil)
	default:
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid authentication request", nil)
	}
}

func validBootstrapRequest(value apicontract.BootstrapRequest) bool {
	return len(value.Token) > 0 && len(value.Token) <= maxAuthTokenBytes &&
		len(value.Username) > 0 && len(value.Username) <= maxAuthUsernameBytes &&
		len(value.Passphrase) >= 12 && len(value.Passphrase) <= maxAuthPassphraseBytes
}

func validLoginRequest(value apicontract.LoginRequest) bool {
	return len(value.Username) > 0 && len(value.Username) <= maxAuthUsernameBytes &&
		len(value.Passphrase) > 0 && len(value.Passphrase) <= maxAuthPassphraseBytes
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func contractUser(value auth.User) apicontract.User {
	return apicontract.User{ID: value.ID, Username: value.Username, Role: value.Role}
}

func contractApplication(value apps.Application) apicontract.Application {
	return apicontract.Application{ID: value.ID, Slug: value.Slug, Name: value.Name, Description: value.Description, Status: value.Status, MachineName: value.MachineName, CreatedAt: value.CreatedAt.Format(time.RFC3339Nano)}
}

func contractApplications(values []apps.Application) []apicontract.Application {
	result := make([]apicontract.Application, 0, len(values))
	for _, value := range values {
		result = append(result, contractApplication(value))
	}
	return result
}

func contractServices(values []apps.Service) []apicontract.Service {
	result := make([]apicontract.Service, 0, len(values))
	for _, value := range values {
		service := apicontract.Service{ID: value.ID, Name: value.Name, Kind: value.Kind, Status: value.Status}
		if value.Port != nil {
			service.Port = *value.Port
		}
		result = append(result, service)
	}
	return result
}

func contractJob(value jobs.Job) apicontract.Job {
	return apicontract.Job{ID: value.ID, Type: value.Type, ResourceType: value.ResourceType, ResourceID: value.ResourceID, Status: value.Status, Phase: value.Phase, Checkpoint: value.Checkpoint, ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail, Progress: value.Progress, CreatedAt: value.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.Format(time.RFC3339Nano)}
}

func contractJobs(values []jobs.Job) []apicontract.Job {
	result := make([]apicontract.Job, 0, len(values))
	for _, value := range values {
		result = append(result, contractJob(value))
	}
	return result
}

func contractJobEvent(value jobs.Event) apicontract.JobEvent {
	return apicontract.JobEvent{ID: value.ID, JobID: value.JobID, Sequence: value.Sequence, Timestamp: value.Timestamp.Format(time.RFC3339Nano), Level: value.Level, Phase: value.Phase, Code: value.Code, Message: value.Message}
}

func contractJobEvents(values []jobs.Event) []apicontract.JobEvent {
	result := make([]apicontract.JobEvent, 0, len(values))
	for _, value := range values {
		result = append(result, contractJobEvent(value))
	}
	return result
}

func contractMachine(value machines.Machine) apicontract.Machine {
	return apicontract.Machine{ID: value.ID, Name: value.Name, Status: value.Status, OS: value.OS, Architecture: value.Architecture, Hostname: value.Hostname, DockerVersion: value.DockerVersion, ComposeVersion: value.ComposeVersion, Resources: value.Resources}
}

func contractMachines(values []machines.Machine) []apicontract.Machine {
	result := make([]apicontract.Machine, 0, len(values))
	for _, value := range values {
		result = append(result, contractMachine(value))
	}
	return result
}

func contractDiagnostics(value docker.Diagnostics) apicontract.Diagnostics {
	return apicontract.Diagnostics{
		DaemonRunning: value.DaemonRunning, ClientAvailable: value.ClientAvailable, EngineReady: value.EngineReady,
		ComposeAvailable: value.ComposeAvailable, CaddyManaged: value.CaddyManaged, OS: value.OS,
		Architecture: value.Architecture, DockerVersion: value.DockerVersion, ComposeVersion: value.ComposeVersion,
		DockerDetail: value.DockerDetail, ComposeDetail: value.ComposeDetail, StartupLimitation: value.StartupLimitation,
		Resources: apicontract.HostResources{
			MemoryTotalBytes: int64(value.Resources.MemoryTotalBytes), MemoryAvailableBytes: int64(value.Resources.MemoryAvailableBytes),
			DiskTotalBytes: int64(value.Resources.DiskTotalBytes), DiskAvailableBytes: int64(value.Resources.DiskAvailableBytes),
		},
	}
}

func (s *Server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.Auth.BootstrapStatus()
	if err != nil {
		problem(w, r, 500, "internal_error", "Could not inspect bootstrap state", nil)
		return
	}
	writeJSON(w, 200, apicontract.BootstrapStatus{BootstrapRequired: needed})
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	var b apicontract.BootstrapRequest
	if err := readAuthJSON(r, &b); err != nil {
		authRequestProblem(w, r, err)
		return
	}
	if !validBootstrapRequest(b) {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid authentication request", nil)
		return
	}
	if !s.admitAuthenticationWork(w, r) {
		return
	}
	defer s.authenticationWorkGate().release()
	u, session, err := s.Auth.Bootstrap(b.Token, b.Username, b.Passphrase)
	if err != nil {
		problem(w, r, 400, "bootstrap_failed", err.Error(), nil)
		return
	}
	if s.BootstrapCompleted != nil {
		s.BootstrapCompleted()
	}
	s.setSession(w, session)
	writeJSON(w, 201, apicontract.SessionResponse{User: contractUser(u), CSRFToken: session.CSRF})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b apicontract.LoginRequest
	if err := readAuthJSON(r, &b); err != nil {
		authRequestProblem(w, r, err)
		return
	}
	if !validLoginRequest(b) {
		problem(w, r, http.StatusBadRequest, "invalid_request", "Invalid authentication request", nil)
		return
	}
	if !s.admitAuthenticationWork(w, r) {
		return
	}
	defer s.authenticationWorkGate().release()
	u, session, err := s.Auth.Login(b.Username, b.Passphrase)
	if err != nil {
		problem(w, r, 401, "invalid_credentials", "Invalid username or passphrase", nil)
		return
	}
	s.setSession(w, session)
	writeJSON(w, 201, apicontract.SessionResponse{User: contractUser(u), CSRFToken: session.CSRF})
}
func (s *Server) setSession(w http.ResponseWriter, v auth.Session) {
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: v.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: v.ExpiresAt, Secure: false})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie(auth.SessionCookie)
	_ = s.Auth.Logout(c.Value)
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	w.WriteHeader(204)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(principalKey{}).(principal)
	writeJSON(w, 200, apicontract.MeResponse{User: contractUser(p.user)})
}
func (s *Server) rotateCSRF(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		problem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", nil)
		return
	}
	token, err := s.Auth.RotateCSRF(cookie.Value)
	if err != nil {
		problem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, apicontract.CSRFResponse{CSRFToken: token})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	d := s.runDiagnostics(r.Context())
	writeJSON(w, 200, apicontract.SystemStatus{Daemon: "running", Diagnostics: contractDiagnostics(d), Capabilities: apicontract.Capabilities{FakeRuntime: s.FakeRuntime, GithubConnections: s.Sources != nil && s.Sources.ProviderEnabled()}})
}
func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	d := s.runDiagnostics(r.Context())
	checks := []apicontract.DoctorCheck{{Name: "hostd", OK: true, Detail: "daemon running"}, {Name: "docker client", OK: d.ClientAvailable, Detail: d.DockerDetail}, {Name: "docker engine", OK: d.EngineReady, Detail: d.DockerDetail}, {Name: "compose v2", OK: d.ComposeAvailable, Detail: d.ComposeDetail}, {Name: "caddy management", OK: d.CaddyManaged, Detail: "disabled in Phase A"}}
	writeJSON(w, 200, apicontract.DoctorResponse{Checks: checks, StartupLimitation: d.StartupLimitation})
}
func (s *Server) runDiagnostics(ctx context.Context) docker.Diagnostics {
	diagnostic := docker.Check(ctx, s.Caddy, s.DockerEndpoint, s.DataRoot)
	_ = s.Machines.UpdateLocalDiagnostics(diagnostic.DockerVersion, diagnostic.ComposeVersion, diagnostic.Resources)
	return diagnostic
}
func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	v, err := s.Apps.List()
	if err != nil {
		problem(w, r, 500, "internal_error", "Could not list applications", nil)
		return
	}
	writeJSON(w, 200, apicontract.ApplicationList{Items: contractApplications(v)})
}
func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var b apicontract.CreateApplicationRequest
	if err := readJSON(r, &b); err != nil {
		problem(w, r, 400, "invalid_request", "Invalid application", nil)
		return
	}
	v, err := s.Apps.Create(b.Name, b.Description, b.SourcePath, b.MachineID)
	if err != nil {
		problem(w, r, 422, "validation_failed", err.Error(), map[string]string{"name": err.Error()})
		return
	}
	writeJSON(w, 201, contractApplication(v))
}
func (s *Server) inspectApp(w http.ResponseWriter, r *http.Request) {
	var b apicontract.InspectRequest
	if err := readJSON(r, &b); err != nil {
		problem(w, r, 400, "invalid_request", "Invalid inspection request", nil)
		return
	}
	v, err := s.Apps.Inspect(b.SourcePath)
	if err != nil {
		problem(w, r, 422, "validation_failed", err.Error(), map[string]string{"sourcePath": err.Error()})
		return
	}
	writeJSON(w, 200, apicontract.InspectResponse{Source: fmt.Sprint(v["source"]), Inspection: fmt.Sprint(v["inspection"]), Message: fmt.Sprint(v["message"])})
}
func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.Apps.Get(r.PathValue("appId"))
	if err != nil {
		problem(w, r, 404, "app_not_found", "Application was not found", nil)
		return
	}
	writeJSON(w, 200, contractApplication(v))
}
func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Apps.Get(r.PathValue("appId")); err != nil {
		problem(w, r, http.StatusNotFound, "app_not_found", "Application was not found", nil)
		return
	}
	v, err := s.Apps.Services(r.PathValue("appId"))
	if err != nil {
		problem(w, r, 404, "app_not_found", "Application was not found", nil)
		return
	}
	writeJSON(w, 200, apicontract.ServiceList{Items: contractServices(v)})
}
func (s *Server) action(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.FakeRuntime {
			problem(w, r, http.StatusConflict, "capability_unavailable", "Runtime actions are unavailable in this build configuration", nil)
			return
		}
		id := r.PathValue("appId")
		if _, err := s.Apps.Get(id); err != nil {
			problem(w, r, 404, "app_not_found", "Application was not found", nil)
			return
		}
		job, created, err := s.Jobs.Create(kind, "application", id, r.Header.Get("Idempotency-Key"))
		if err != nil {
			problem(w, r, 409, "application_busy", err.Error(), nil)
			return
		}
		writeJSON(w, map[bool]int{true: 202, false: 200}[created], apicontract.JobMutationResponse{Job: contractJob(job), Created: created})
	}
}
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	v, err := s.Jobs.List(100)
	if err != nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not list jobs", nil)
		return
	}
	writeJSON(w, http.StatusOK, apicontract.JobList{Items: contractJobs(v)})
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	v, err := s.Jobs.Get(r.PathValue("jobId"))
	if err != nil {
		problem(w, r, 404, "job_not_found", "Job was not found", nil)
		return
	}
	writeJSON(w, 200, contractJob(v))
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Jobs.Cancel(r.PathValue("jobId"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, apicontract.JobResponse{Job: contractJob(job)})
	case errors.Is(err, jobs.ErrJobNotFound):
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
	case errors.Is(err, jobs.ErrJobTerminal):
		problem(w, r, http.StatusConflict, "job_terminal", "Job is already terminal and cannot be cancelled", nil)
	default:
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not cancel job", nil)
	}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Jobs.Get(r.PathValue("jobId")); err != nil {
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	v, err := s.Jobs.Events(r.PathValue("jobId"), after)
	if err != nil {
		problem(w, r, 500, "internal_error", "Could not list events", nil)
		return
	}
	writeJSON(w, 200, apicontract.JobEventList{Items: contractJobEvents(v)})
}
func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Jobs.Get(r.PathValue("jobId")); err != nil {
		problem(w, r, http.StatusNotFound, "job_not_found", "Job was not found", nil)
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if after == 0 {
		after, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	f, ok := w.(http.Flusher)
	if !ok {
		problem(w, r, 500, "streaming_unavailable", "Streaming is unavailable", nil)
		return
	}
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for {
		events, err := s.Jobs.Events(r.PathValue("jobId"), after)
		if err != nil {
			return
		}
		for _, e := range events {
			b, _ := json.Marshal(contractJobEvent(e))
			_, _ = fmt.Fprintf(w, "id: %d\nevent: job-event\ndata: %s\n\n", e.ID, b)
			after = e.ID
		}
		f.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (s *Server) logsStream(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Apps.Get(r.PathValue("appId")); err != nil {
		problem(w, r, http.StatusNotFound, "app_not_found", "Application was not found", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(w, "event: log\ndata: {\"message\":\"Logs require a real runtime in Milestone 2\"}\n\n")
}
func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	v, err := s.Machines.List()
	if err != nil {
		problem(w, r, 500, "internal_error", "Could not list machines", nil)
		return
	}
	writeJSON(w, 200, apicontract.MachineList{Items: contractMachines(v)})
}
func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		problem(w, r, 404, "not_found", "No such endpoint", nil)
		return
	}
	name := "ui/index.html"
	if r.URL.Path != "/" {
		candidate := "ui" + r.URL.Path
		if b, err := web.ReadFile(candidate); err == nil {
			contentType := "text/plain"
			if strings.HasSuffix(candidate, ".js") {
				contentType = "application/javascript"
			}
			if strings.HasSuffix(candidate, ".css") {
				contentType = "text/css"
			}
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(b)
			return
		}
	}
	b, err := web.ReadFile(name)
	if err != nil {
		problem(w, r, 500, "ui_unavailable", "Embedded dashboard is unavailable", nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
