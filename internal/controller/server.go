package controller

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/runtime/docker"
)

//go:embed all:ui
var web embed.FS

type Server struct {
	Auth           *auth.Service
	Apps           *apps.Store
	Jobs           *jobs.Service
	Machines       *machines.Store
	Caddy          bool
	FakeRuntime    bool
	DockerEndpoint string
	DataRoot       string
	Logger         *slog.Logger
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
		{http.MethodGet, "/api/v1/auth/bootstrap/status", "bootstrapStatus", s.bootstrapStatus},
		{http.MethodPost, "/api/v1/auth/bootstrap", "bootstrap", s.bootstrap},
		{http.MethodPost, "/api/v1/auth/sessions", "login", s.login},
		{http.MethodDelete, "/api/v1/auth/sessions/current", "logout", s.require(s.logout)},
		{http.MethodGet, "/api/v1/auth/me", "me", s.require(s.me)},
		{http.MethodGet, "/api/v1/auth/csrf", "rotateCSRF", s.require(s.rotateCSRF)},
		{http.MethodGet, "/api/v1/system/status", "systemStatus", s.require(s.status)},
		{http.MethodGet, "/api/v1/system/doctor", "doctor", s.require(s.doctor)},
		{http.MethodGet, "/api/v1/apps", "listApplications", s.require(s.listApps)},
		{http.MethodPost, "/api/v1/apps", "createApplication", s.require(s.createApp)},
		{http.MethodPost, "/api/v1/apps/import/inspect", "inspectImport", s.require(s.inspectApp)},
		{http.MethodGet, "/api/v1/apps/{appId}", "getApplication", s.require(s.getApp)},
		{http.MethodGet, "/api/v1/apps/{appId}/services", "listServices", s.require(s.services)},
		{http.MethodPost, "/api/v1/apps/{appId}/deployments", "deployApplication", s.require(s.action("deploy"))},
		{http.MethodPost, "/api/v1/apps/{appId}/start", "startApplication", s.require(s.action("start"))},
		{http.MethodPost, "/api/v1/apps/{appId}/stop", "stopApplication", s.require(s.action("stop"))},
		{http.MethodPost, "/api/v1/apps/{appId}/restart", "restartApplication", s.require(s.action("restart"))},
		{http.MethodGet, "/api/v1/apps/{appId}/logs/stream", "streamLogs", s.require(s.logsStream)},
		{http.MethodGet, "/api/v1/jobs", "listJobs", s.require(s.listJobs)},
		{http.MethodGet, "/api/v1/jobs/{jobId}", "getJob", s.require(s.getJob)},
		{http.MethodPost, "/api/v1/jobs/{jobId}/cancel", "cancelJob", s.require(s.cancelJob)},
		{http.MethodGet, "/api/v1/jobs/{jobId}/events", "listJobEvents", s.require(s.events)},
		{http.MethodGet, "/api/v1/jobs/{jobId}/events/stream", "streamJobEvents", s.require(s.eventsStream)},
		{http.MethodGet, "/api/v1/machines", "listMachines", s.require(s.listMachines)},
	}
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
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://hostd.local/problems/" + code, "title": http.StatusText(status), "status": status, "detail": detail, "code": code, "requestId": requestID(r), "errors": fields})
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
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.Auth.BootstrapStatus()
	if err != nil {
		problem(w, r, 500, "internal_error", "Could not inspect bootstrap state", nil)
		return
	}
	writeJSON(w, 200, map[string]any{"bootstrapRequired": needed})
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token      string `json:"token"`
		Username   string `json:"username"`
		Passphrase string `json:"passphrase"`
	}
	if err := readJSON(r, &b); err != nil {
		problem(w, r, 400, "invalid_request", "Invalid bootstrap request", nil)
		return
	}
	u, session, err := s.Auth.Bootstrap(b.Token, b.Username, b.Passphrase)
	if err != nil {
		problem(w, r, 400, "bootstrap_failed", err.Error(), nil)
		return
	}
	s.setSession(w, session)
	writeJSON(w, 201, map[string]any{"user": u, "csrfToken": session.CSRF})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Username   string `json:"username"`
		Passphrase string `json:"passphrase"`
	}
	if err := readJSON(r, &b); err != nil {
		problem(w, r, 400, "invalid_request", "Invalid session request", nil)
		return
	}
	u, session, err := s.Auth.Login(b.Username, b.Passphrase)
	if err != nil {
		problem(w, r, 401, "invalid_credentials", "Invalid username or passphrase", nil)
		return
	}
	s.setSession(w, session)
	writeJSON(w, 201, map[string]any{"user": u, "csrfToken": session.CSRF})
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
	writeJSON(w, 200, map[string]any{"user": p.user})
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
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": token})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	d := s.runDiagnostics(r.Context())
	writeJSON(w, 200, map[string]any{"daemon": "running", "diagnostics": d, "capabilities": map[string]bool{"fakeRuntime": s.FakeRuntime}})
}
func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	d := s.runDiagnostics(r.Context())
	checks := []map[string]any{{"name": "hostd", "ok": true, "detail": "daemon running"}, {"name": "docker client", "ok": d.ClientAvailable, "detail": d.DockerDetail}, {"name": "docker engine", "ok": d.EngineReady, "detail": d.DockerDetail}, {"name": "compose v2", "ok": d.ComposeAvailable, "detail": d.ComposeDetail}, {"name": "caddy management", "ok": d.CaddyManaged, "detail": "disabled in Phase A"}}
	writeJSON(w, 200, map[string]any{"checks": checks, "startupLimitation": d.StartupLimitation})
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
	writeJSON(w, 200, map[string]any{"items": v})
}
func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		SourcePath  string `json:"sourcePath"`
		MachineID   string `json:"machineId"`
	}
	if err := readJSON(r, &b); err != nil {
		problem(w, r, 400, "invalid_request", "Invalid application", nil)
		return
	}
	v, err := s.Apps.Create(b.Name, b.Description, b.SourcePath, b.MachineID)
	if err != nil {
		problem(w, r, 422, "validation_failed", err.Error(), map[string]string{"name": err.Error()})
		return
	}
	writeJSON(w, 201, v)
}
func (s *Server) inspectApp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		SourcePath string `json:"sourcePath"`
	}
	if err := readJSON(r, &b); err != nil {
		problem(w, r, 400, "invalid_request", "Invalid inspection request", nil)
		return
	}
	v, err := s.Apps.Inspect(b.SourcePath)
	if err != nil {
		problem(w, r, 422, "validation_failed", err.Error(), map[string]string{"sourcePath": err.Error()})
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.Apps.Get(r.PathValue("appId"))
	if err != nil {
		problem(w, r, 404, "app_not_found", "Application was not found", nil)
		return
	}
	writeJSON(w, 200, v)
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
	writeJSON(w, 200, map[string]any{"items": v})
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
		writeJSON(w, map[bool]int{true: 202, false: 200}[created], map[string]any{"job": job, "created": created})
	}
}
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	v, err := s.Jobs.List(100)
	if err != nil {
		problem(w, r, http.StatusInternalServerError, "internal_error", "Could not list jobs", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": v})
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	v, err := s.Jobs.Get(r.PathValue("jobId"))
	if err != nil {
		problem(w, r, 404, "job_not_found", "Job was not found", nil)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Jobs.Cancel(r.PathValue("jobId"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"job": job})
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
	writeJSON(w, 200, map[string]any{"items": v})
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
			b, _ := json.Marshal(e)
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
	writeJSON(w, 200, map[string]any{"items": v})
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
