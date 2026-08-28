// Package controllerclient provides a bounded, context-aware client for the hostd controller API.
package controllerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/auth"
)

const maxResponseBytes int64 = 1 << 20

// Session holds the cookie and CSRF credentials for one controller session.
type Session struct {
	SessionToken     string `json:"sessionToken"`
	CSRFToken        string `json:"csrfToken"`
	ControllerOrigin string `json:"controllerOrigin,omitempty"`
}

// Options configures a Client. A nil HTTPClient gets a safe default timeout.
type Options struct {
	Endpoint   string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is safe for concurrent read requests. Concurrent mutations of the same Session should be serialized.
type Client struct {
	endpoint   *url.URL
	origin     string
	httpClient *http.Client
}

// ProblemError is returned for non-success controller responses.
type ProblemError struct {
	StatusCode int
	Status     string
	Problem    apicontract.Problem
}

func (e *ProblemError) Error() string {
	d := e.Problem.Detail
	if d == "" {
		d = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("hostd returned %s: %s", e.Status, d)
}

func New(options Options) (*Client, error) {
	if options.Endpoint == "" {
		options.Endpoint = "http://127.0.0.1:7345"
	}
	u, err := url.Parse(options.Endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid controller endpoint: %q", options.Endpoint)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Path = strings.TrimRight(u.Path, "/")
	c := options.HTTPClient
	if c == nil {
		timeout := options.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		c = &http.Client{Timeout: timeout}
	}
	return &Client{endpoint: u, origin: canonicalControllerOrigin(u), httpClient: c}, nil
}

func canonicalControllerOrigin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := u.Port()
	if port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		host = net.JoinHostPort(host, port)
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

func (c *Client) BootstrapStatus(ctx context.Context) (apicontract.BootstrapStatus, error) {
	var v apicontract.BootstrapStatus
	return v, c.doJSON(ctx, "bootstrapStatus", nil, nil, &v, false)
}
func (c *Client) Bootstrap(ctx context.Context, request apicontract.BootstrapRequest) (apicontract.SessionResponse, Session, error) {
	return c.login(ctx, "bootstrap", request)
}
func (c *Client) Login(ctx context.Context, request apicontract.LoginRequest) (apicontract.SessionResponse, Session, error) {
	return c.login(ctx, "login", request)
}
func (c *Client) login(ctx context.Context, operation string, payload any) (apicontract.SessionResponse, Session, error) {
	var response apicontract.SessionResponse
	var session Session
	httpResponse, err := c.request(ctx, operation, nil, payload, false, "", nil)
	if err != nil {
		return response, session, err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return response, session, decodeProblem(httpResponse)
	}
	if err := decodeJSON(httpResponse.Body, &response); err != nil {
		return response, session, err
	}
	session.CSRFToken = response.CSRFToken
	for _, cookie := range httpResponse.Cookies() {
		if cookie.Name == auth.SessionCookie {
			session.SessionToken = cookie.Value
		}
	}
	if session.SessionToken == "" || session.CSRFToken == "" {
		return response, session, errors.New("hostd login response omitted session credentials")
	}
	session.ControllerOrigin = c.origin
	return response, session, nil
}
func (c *Client) Logout(ctx context.Context, session *Session) error {
	return c.doJSON(ctx, "logout", session, nil, nil, true)
}
func (c *Client) Me(ctx context.Context, session Session) (apicontract.MeResponse, error) {
	var v apicontract.MeResponse
	return v, c.doJSON(ctx, "me", &session, nil, &v, false)
}
func (c *Client) CSRF(ctx context.Context, session Session) (apicontract.CSRFResponse, error) {
	var v apicontract.CSRFResponse
	return v, c.doJSON(ctx, "rotateCSRF", &session, nil, &v, false)
}
func (c *Client) Status(ctx context.Context, session Session) (apicontract.SystemStatus, error) {
	var v apicontract.SystemStatus
	return v, c.doJSON(ctx, "systemStatus", &session, nil, &v, false)
}
func (c *Client) Doctor(ctx context.Context, session Session) (apicontract.DoctorResponse, error) {
	var v apicontract.DoctorResponse
	return v, c.doJSON(ctx, "doctor", &session, nil, &v, false)
}
func (c *Client) Apps(ctx context.Context, session Session) (apicontract.ApplicationList, error) {
	var v apicontract.ApplicationList
	return v, c.doJSON(ctx, "listApplications", &session, nil, &v, false)
}
func (c *Client) App(ctx context.Context, session Session, appID string) (apicontract.Application, error) {
	var v apicontract.Application
	return v, c.doJSON(ctx, "getApplication", &session, nil, &v, false, appID)
}
func (c *Client) Machines(ctx context.Context, session Session) (apicontract.MachineList, error) {
	var v apicontract.MachineList
	return v, c.doJSON(ctx, "listMachines", &session, nil, &v, false)
}
func (c *Client) Jobs(ctx context.Context, session Session) (apicontract.JobList, error) {
	var v apicontract.JobList
	return v, c.doJSON(ctx, "listJobs", &session, nil, &v, false)
}
func (c *Client) Job(ctx context.Context, session Session, jobID string) (apicontract.Job, error) {
	var v apicontract.Job
	return v, c.doJSON(ctx, "getJob", &session, nil, &v, false, jobID)
}
func (c *Client) Events(ctx context.Context, session Session, jobID string, after int64) (apicontract.JobEventList, error) {
	var v apicontract.JobEventList
	return v, c.doJSONQuery(ctx, "listJobEvents", &session, nil, &v, false, url.Values{"after": []string{fmt.Sprint(after)}}, jobID)
}

func (c *Client) Deploy(ctx context.Context, session *Session, appID, idempotencyKey string) (apicontract.JobMutationResponse, error) {
	var v apicontract.JobMutationResponse
	return v, c.mutate(ctx, "deployApplication", session, nil, &v, idempotencyKey, appID)
}
func (c *Client) Start(ctx context.Context, session *Session, appID, idempotencyKey string) (apicontract.JobMutationResponse, error) {
	var v apicontract.JobMutationResponse
	return v, c.mutate(ctx, "startApplication", session, nil, &v, idempotencyKey, appID)
}
func (c *Client) Stop(ctx context.Context, session *Session, appID, idempotencyKey string) (apicontract.JobMutationResponse, error) {
	var v apicontract.JobMutationResponse
	return v, c.mutate(ctx, "stopApplication", session, nil, &v, idempotencyKey, appID)
}
func (c *Client) Restart(ctx context.Context, session *Session, appID, idempotencyKey string) (apicontract.JobMutationResponse, error) {
	var v apicontract.JobMutationResponse
	return v, c.mutate(ctx, "restartApplication", session, nil, &v, idempotencyKey, appID)
}
func (c *Client) Cancel(ctx context.Context, session *Session, jobID string) (apicontract.JobResponse, error) {
	var v apicontract.JobResponse
	return v, c.mutate(ctx, "cancelJob", session, nil, &v, "", jobID)
}
func (c *Client) Resume(ctx context.Context, session *Session, jobID string) (apicontract.JobResponse, error) {
	var v apicontract.JobResponse
	return v, c.mutate(ctx, "resumeJob", session, nil, &v, "", jobID)
}

func (c *Client) mutate(ctx context.Context, operation string, session *Session, payload, target any, idempotencyKey string, args ...string) error {
	if session == nil {
		return errors.New("controller session is required")
	}
	err := c.doJSONWithHeaders(ctx, operation, session, payload, target, true, idempotencyKey, nil, args...)
	var p *ProblemError
	if !errors.As(err, &p) || p.StatusCode != http.StatusForbidden || p.Problem.Code != "csrf_failed" {
		return err
	}
	csrf, refreshErr := c.CSRF(ctx, *session)
	if refreshErr != nil {
		return refreshErr
	}
	if csrf.CSRFToken == "" {
		return errors.New("controller returned empty CSRF token")
	}
	session.CSRFToken = csrf.CSRFToken
	return c.doJSONWithHeaders(ctx, operation, session, payload, target, true, idempotencyKey, nil, args...)
}
func (c *Client) doJSON(ctx context.Context, op string, s *Session, in, out any, mutation bool, args ...string) error {
	return c.doJSONWithHeaders(ctx, op, s, in, out, mutation, "", nil, args...)
}
func (c *Client) doJSONQuery(ctx context.Context, op string, s *Session, in, out any, mutation bool, q url.Values, args ...string) error {
	return c.doJSONWithHeaders(ctx, op, s, in, out, mutation, "", q, args...)
}
func (c *Client) doJSONWithHeaders(ctx context.Context, op string, s *Session, in, out any, mutation bool, idempotency string, q url.Values, args ...string) error {
	response, err := c.request(ctx, op, s, in, mutation, idempotency, q, args...)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeProblem(response)
	}
	if out == nil {
		return nil
	}
	return decodeJSON(response.Body, out)
}
func (c *Client) request(ctx context.Context, op string, s *Session, in any, mutation bool, idempotency string, q url.Values, args ...string) (*http.Response, error) {
	return c.requestWithClient(ctx, c.httpClient, op, s, in, mutation, idempotency, q, args...)
}

func (c *Client) requestWithClient(ctx context.Context, httpClient *http.Client, op string, s *Session, in any, mutation bool, idempotency string, q url.Values, args ...string) (*http.Response, error) {
	if s != nil && s.ControllerOrigin != "" && s.ControllerOrigin != c.origin {
		return nil, errors.New("controller session belongs to a different endpoint")
	}
	operation, ok := apicontract.Operations[op]
	if !ok {
		return nil, fmt.Errorf("unknown controller operation %q", op)
	}
	path := operation.Path
	for _, arg := range args {
		i := strings.Index(path, "{")
		if i < 0 {
			return nil, fmt.Errorf("too many path arguments for %s", op)
		}
		j := strings.Index(path[i:], "}")
		if j < 0 {
			return nil, fmt.Errorf("invalid contract path for %s", op)
		}
		path = path[:i] + url.PathEscape(arg) + path[i+j+1:]
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("missing path argument for %s", op)
	}
	u := *c.endpoint
	rawPath := strings.TrimRight(c.endpoint.EscapedPath(), "/") + path
	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, fmt.Errorf("decode controller path: %w", err)
	}
	u.Path = decodedPath
	u.RawPath = rawPath
	u.RawQuery = q.Encode()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, operation.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	if op == "streamJobEvents" && q.Get("after") != "" {
		req.Header.Set("Last-Event-ID", q.Get("after"))
	}
	if s != nil && s.SessionToken != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: s.SessionToken})
	}
	if mutation && s != nil && s.CSRFToken != "" {
		req.Header.Set("X-CSRF-Token", s.CSRFToken)
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hostd request failed: %w", err)
	}
	return response, nil
}
func decodeJSON(r io.Reader, target any) error {
	d := json.NewDecoder(io.LimitReader(r, maxResponseBytes+1))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("decode hostd response: %w", err)
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("hostd response must contain one JSON object")
	}
	return nil
}
func decodeProblem(r *http.Response) error {
	var p apicontract.Problem
	_ = decodeJSON(r.Body, &p)
	return &ProblemError{StatusCode: r.StatusCode, Status: r.Status, Problem: p}
}
