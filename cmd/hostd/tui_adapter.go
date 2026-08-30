package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/secretfile"
	"github.com/hostd/hostd/internal/tui"
)

// protectedSessionStore adapts controllerclient's authenticated session format
// to the opaque persistence interface consumed by the TUI.
type protectedSessionStore struct{ path string }

func newProtectedSessionStore(path string) tui.SessionStore {
	return &protectedSessionStore{path: path}
}
func (s *protectedSessionStore) Load(context.Context) ([]byte, error) {
	session, err := controllerclient.ReadSessionFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(session)
}
func (s *protectedSessionStore) Save(_ context.Context, value []byte) error {
	var session controllerclient.Session
	if err := json.Unmarshal(value, &session); err != nil {
		return fmt.Errorf("decode controller session: %w", err)
	}
	return controllerclient.WriteSessionFile(s.path, session)
}
func (s *protectedSessionStore) Clear(context.Context) error {
	return secretfile.Remove(s.path)
}

// tuiControllerClient owns the authenticated controller session and persists it
// through the supplied opaque store after login, bootstrap, and CSRF rotation.
type tuiControllerClient struct {
	client  *controllerclient.Client
	store   tui.SessionStore
	origin  string
	mu      sync.Mutex
	session controllerclient.Session
	loaded  bool
}

func newTUIControllerClient(endpoint string, store tui.SessionStore) (tui.Client, error) {
	if store == nil {
		return nil, errors.New("TUI session store is required")
	}
	origin, err := tuiControllerOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	client, err := controllerclient.New(controllerclient.Options{Endpoint: origin})
	if err != nil {
		return nil, err
	}
	return &tuiControllerClient{client: client, store: store, origin: origin}, nil
}

func tuiControllerOrigin(endpoint string) (string, error) {
	u, err := url.ParseRequestURI(endpoint)
	if err != nil || !u.IsAbs() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("TUI controller endpoint must be an absolute loopback HTTP(S) IP origin with an explicit port")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("TUI controller endpoint must use HTTP or HTTPS")
	}
	ip := net.ParseIP(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("TUI controller endpoint must be a loopback IP origin with an explicit port")
	}
	return u.Scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func (c *tuiControllerClient) BootstrapStatus(ctx context.Context) (apicontract.BootstrapStatus, error) {
	value, err := c.client.BootstrapStatus(ctx)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Bootstrap(ctx context.Context, request apicontract.BootstrapRequest) (apicontract.SessionResponse, error) {
	value, session, err := c.client.Bootstrap(ctx, request)
	if err != nil {
		return value, mapTUIError(err)
	}
	return value, c.save(ctx, session)
}
func (c *tuiControllerClient) Login(ctx context.Context, request apicontract.LoginRequest) (apicontract.SessionResponse, error) {
	value, session, err := c.client.Login(ctx, request)
	if err != nil {
		return value, mapTUIError(err)
	}
	return value, c.save(ctx, session)
}
func (c *tuiControllerClient) Logout(ctx context.Context) error {
	session, err := c.current(ctx)
	if err != nil {
		return err
	}
	remoteErr := c.client.Logout(ctx, &session)
	c.mu.Lock()
	c.session = controllerclient.Session{}
	c.loaded = true
	c.mu.Unlock()
	if err := c.store.Clear(ctx); err != nil {
		return fmt.Errorf("clear controller session: %w", err)
	}
	return mapTUIError(remoteErr)
}
func (c *tuiControllerClient) Me(ctx context.Context) (apicontract.MeResponse, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.MeResponse{}, err
	}
	value, err := c.client.Me(ctx, session)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Status(ctx context.Context) (apicontract.SystemStatus, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.SystemStatus{}, err
	}
	value, err := c.client.Status(ctx, session)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Doctor(ctx context.Context) (apicontract.DoctorResponse, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.DoctorResponse{}, err
	}
	value, err := c.client.Doctor(ctx, session)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Applications(ctx context.Context) (apicontract.ApplicationList, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.ApplicationList{}, err
	}
	value, err := c.client.Apps(ctx, session)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Application(ctx context.Context, id string) (apicontract.Application, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.Application{}, err
	}
	value, err := c.client.App(ctx, session, id)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Machines(ctx context.Context) (apicontract.MachineList, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.MachineList{}, err
	}
	value, err := c.client.Machines(ctx, session)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Deploy(ctx context.Context, id, key string) (apicontract.JobMutationResponse, error) {
	return c.mutation(ctx, func(s *controllerclient.Session) (apicontract.JobMutationResponse, error) {
		return c.client.Deploy(ctx, s, id, key)
	})
}
func (c *tuiControllerClient) Lifecycle(ctx context.Context, id, action, key string) (apicontract.JobMutationResponse, error) {
	return c.mutation(ctx, func(s *controllerclient.Session) (apicontract.JobMutationResponse, error) {
		switch action {
		case "start":
			return c.client.Start(ctx, s, id, key)
		case "stop":
			return c.client.Stop(ctx, s, id, key)
		case "restart":
			return c.client.Restart(ctx, s, id, key)
		default:
			return apicontract.JobMutationResponse{}, fmt.Errorf("unsupported lifecycle action %q", action)
		}
	})
}
func (c *tuiControllerClient) Jobs(ctx context.Context) (apicontract.JobList, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.JobList{}, err
	}
	value, err := c.client.Jobs(ctx, session)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) Job(ctx context.Context, id string) (apicontract.Job, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.Job{}, err
	}
	value, err := c.client.Job(ctx, session, id)
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) FollowJob(ctx context.Context, id string, after int64) (<-chan apicontract.JobEvent, <-chan error) {
	session, err := c.current(ctx)
	if err != nil {
		events := make(chan apicontract.JobEvent)
		errors := make(chan error, 1)
		errors <- err
		close(events)
		close(errors)
		return events, errors
	}
	stream := c.client.StreamJobEvents(ctx, session, id, after)
	return stream.Events, stream.Errors
}
func (c *tuiControllerClient) CancelJob(ctx context.Context, id, _ string) (apicontract.JobResponse, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.JobResponse{}, err
	}
	value, err := c.client.Cancel(ctx, &session, id)
	if err == nil {
		if saveErr := c.save(ctx, session); saveErr != nil {
			return value, saveErr
		}
	}
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) ResumeJob(ctx context.Context, id, _ string) (apicontract.JobResponse, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.JobResponse{}, err
	}
	value, err := c.client.Resume(ctx, &session, id)
	if err == nil {
		if saveErr := c.save(ctx, session); saveErr != nil {
			return value, saveErr
		}
	}
	return value, mapTUIError(err)
}
func (c *tuiControllerClient) mutation(ctx context.Context, run func(*controllerclient.Session) (apicontract.JobMutationResponse, error)) (apicontract.JobMutationResponse, error) {
	session, err := c.current(ctx)
	if err != nil {
		return apicontract.JobMutationResponse{}, err
	}
	value, err := run(&session)
	if err != nil {
		return value, mapTUIError(err)
	}
	if err := c.save(ctx, session); err != nil {
		return value, err
	}
	return value, nil
}
func (c *tuiControllerClient) current(ctx context.Context) (controllerclient.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.session, c.validateSessionOrigin(c.session)
	}
	value, err := c.store.Load(ctx)
	if err != nil {
		return c.session, fmt.Errorf("load controller session: %w", err)
	}
	if len(value) == 0 {
		c.loaded = true
		return c.session, nil
	}
	var session controllerclient.Session
	if err := json.Unmarshal(value, &session); err != nil {
		return c.session, fmt.Errorf("decode controller session: %w", err)
	}
	if err := c.validateSessionOrigin(session); err != nil {
		return c.session, err
	}
	c.session = session
	c.loaded = true
	return c.session, nil
}
func (c *tuiControllerClient) save(ctx context.Context, session controllerclient.Session) error {
	session.ControllerOrigin = c.origin
	value, err := json.Marshal(session)
	if err != nil {
		return err
	}
	if err := c.store.Save(ctx, value); err != nil {
		return fmt.Errorf("save controller session: %w", err)
	}
	c.mu.Lock()
	c.session = session
	c.loaded = true
	c.mu.Unlock()
	return nil
}

func (c *tuiControllerClient) validateSessionOrigin(session controllerclient.Session) error {
	if session.ControllerOrigin != "" && session.ControllerOrigin != c.origin {
		return errors.New("protected controller session belongs to a different endpoint")
	}
	return nil
}
func mapTUIError(err error) error {
	if err == nil {
		return nil
	}
	var problem *controllerclient.ProblemError
	if errors.As(err, &problem) {
		return &tui.HTTPError{Status: problem.StatusCode, Code: problem.Problem.Code, Detail: problem.Problem.Detail}
	}
	return err
}
