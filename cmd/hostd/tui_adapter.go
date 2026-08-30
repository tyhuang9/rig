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
	client     *controllerclient.Client
	store      tui.SessionStore
	origin     string
	mu         sync.Mutex
	session    controllerclient.Session
	loaded     bool
	generation uint64
}

type tuiSessionSnapshot struct {
	session    controllerclient.Session
	generation uint64
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
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return err
	}
	session := snapshot.session
	remoteErr := c.client.Logout(ctx, &session)
	if err := c.clearIfCurrent(ctx, snapshot.generation); err != nil {
		return err
	}
	return mapTUIErrorAt(remoteErr, snapshot.generation)
}
func (c *tuiControllerClient) ClearSession(ctx context.Context, generation uint64) error {
	return c.clearIfCurrent(ctx, generation)
}
func (c *tuiControllerClient) Me(ctx context.Context) (apicontract.MeResponse, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.MeResponse{}, err
	}
	value, err := c.client.Me(ctx, snapshot.session)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) Status(ctx context.Context) (apicontract.SystemStatus, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.SystemStatus{}, err
	}
	value, err := c.client.Status(ctx, snapshot.session)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) Doctor(ctx context.Context) (apicontract.DoctorResponse, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.DoctorResponse{}, err
	}
	value, err := c.client.Doctor(ctx, snapshot.session)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) Applications(ctx context.Context) (apicontract.ApplicationList, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.ApplicationList{}, err
	}
	value, err := c.client.Apps(ctx, snapshot.session)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) Application(ctx context.Context, id string) (apicontract.Application, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.Application{}, err
	}
	value, err := c.client.App(ctx, snapshot.session, id)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) Machines(ctx context.Context) (apicontract.MachineList, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.MachineList{}, err
	}
	value, err := c.client.Machines(ctx, snapshot.session)
	return value, mapTUIErrorAt(err, snapshot.generation)
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
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.JobList{}, err
	}
	value, err := c.client.Jobs(ctx, snapshot.session)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) Job(ctx context.Context, id string) (apicontract.Job, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.Job{}, err
	}
	value, err := c.client.Job(ctx, snapshot.session, id)
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) FollowJob(ctx context.Context, id string, after int64) (<-chan apicontract.JobEvent, <-chan error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		events := make(chan apicontract.JobEvent)
		errors := make(chan error, 1)
		errors <- err
		close(events)
		close(errors)
		return events, errors
	}
	stream := c.client.StreamJobEvents(ctx, snapshot.session, id, after)
	return mapTUIJobStream(ctx, stream.Events, stream.Errors, snapshot.generation)
}
func (c *tuiControllerClient) CancelJob(ctx context.Context, id, _ string) (apicontract.JobResponse, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.JobResponse{}, err
	}
	session := snapshot.session
	value, err := c.client.Cancel(ctx, &session, id)
	if err == nil {
		if saveErr := c.saveIfCurrent(ctx, snapshot.generation, session); saveErr != nil {
			return value, saveErr
		}
	}
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) ResumeJob(ctx context.Context, id, _ string) (apicontract.JobResponse, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.JobResponse{}, err
	}
	session := snapshot.session
	value, err := c.client.Resume(ctx, &session, id)
	if err == nil {
		if saveErr := c.saveIfCurrent(ctx, snapshot.generation, session); saveErr != nil {
			return value, saveErr
		}
	}
	return value, mapTUIErrorAt(err, snapshot.generation)
}
func (c *tuiControllerClient) mutation(ctx context.Context, run func(*controllerclient.Session) (apicontract.JobMutationResponse, error)) (apicontract.JobMutationResponse, error) {
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return apicontract.JobMutationResponse{}, err
	}
	session := snapshot.session
	value, err := run(&session)
	if err != nil {
		return value, mapTUIErrorAt(err, snapshot.generation)
	}
	if err := c.saveIfCurrent(ctx, snapshot.generation, session); err != nil {
		return value, err
	}
	return value, nil
}
func (c *tuiControllerClient) current(ctx context.Context) (controllerclient.Session, error) {
	snapshot, err := c.currentSnapshot(ctx)
	return snapshot.session, err
}
func (c *tuiControllerClient) currentSnapshot(ctx context.Context) (tuiSessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return tuiSessionSnapshot{session: c.session, generation: c.generation}, c.validateSessionOrigin(c.session)
	}
	value, err := c.store.Load(ctx)
	if err != nil {
		return tuiSessionSnapshot{}, fmt.Errorf("load controller session: %w", err)
	}
	if len(value) == 0 {
		c.loaded = true
		c.nextGenerationLocked()
		return tuiSessionSnapshot{session: c.session, generation: c.generation}, nil
	}
	var session controllerclient.Session
	if err := json.Unmarshal(value, &session); err != nil {
		return tuiSessionSnapshot{}, fmt.Errorf("decode controller session: %w", err)
	}
	if err := c.validateSessionOrigin(session); err != nil {
		return tuiSessionSnapshot{}, err
	}
	c.session = session
	c.loaded = true
	c.nextGenerationLocked()
	return tuiSessionSnapshot{session: c.session, generation: c.generation}, nil
}
func (c *tuiControllerClient) save(ctx context.Context, session controllerclient.Session) error {
	session.ControllerOrigin = c.origin
	value, err := json.Marshal(session)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.Save(ctx, value); err != nil {
		return fmt.Errorf("save controller session: %w", err)
	}
	c.session = session
	c.loaded = true
	c.nextGenerationLocked()
	return nil
}

func (c *tuiControllerClient) saveIfCurrent(ctx context.Context, generation uint64, session controllerclient.Session) error {
	session.ControllerOrigin = c.origin
	value, err := json.Marshal(session)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || generation == 0 || c.generation != generation {
		return nil
	}
	if err := c.store.Save(ctx, value); err != nil {
		return fmt.Errorf("save controller session: %w", err)
	}
	c.session = session
	c.nextGenerationLocked()
	return nil
}

func (c *tuiControllerClient) clearIfCurrent(ctx context.Context, generation uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || generation == 0 || c.generation != generation {
		return nil
	}
	if err := c.store.Clear(ctx); err != nil {
		return fmt.Errorf("clear controller session: %w", err)
	}
	c.session = controllerclient.Session{}
	c.loaded = true
	c.nextGenerationLocked()
	return nil
}

func (c *tuiControllerClient) nextGenerationLocked() {
	c.generation++
	if c.generation == 0 {
		c.generation++
	}
}

func (c *tuiControllerClient) validateSessionOrigin(session controllerclient.Session) error {
	if session.ControllerOrigin != "" && session.ControllerOrigin != c.origin {
		return errors.New("protected controller session belongs to a different endpoint")
	}
	return nil
}
func mapTUIError(err error) error {
	return mapTUIErrorAt(err, 0)
}
func mapTUIErrorAt(err error, generation uint64) error {
	if err == nil {
		return nil
	}
	var problem *controllerclient.ProblemError
	if errors.As(err, &problem) {
		return &tui.HTTPError{Status: problem.StatusCode, Code: problem.Problem.Code, Detail: problem.Problem.Detail, SessionGeneration: generation}
	}
	return err
}

func mapTUIJobStream(ctx context.Context, inputEvents <-chan apicontract.JobEvent, inputErrors <-chan error, generation uint64) (<-chan apicontract.JobEvent, <-chan error) {
	outputEvents := make(chan apicontract.JobEvent)
	outputErrors := make(chan error)
	go func() {
		defer close(outputErrors)
		defer close(outputEvents)
		for inputEvents != nil || inputErrors != nil {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-inputEvents:
				if !ok {
					inputEvents = nil
					continue
				}
				select {
				case outputEvents <- event:
				case <-ctx.Done():
					return
				}
			case err, ok := <-inputErrors:
				if !ok {
					inputErrors = nil
					continue
				}
				if err == nil {
					continue
				}
				select {
				case outputErrors <- mapTUIErrorAt(err, generation):
				case <-ctx.Done():
					return
				}
				// The unbuffered error handoff keeps the event channel open until the
				// model consumes the terminal error. Otherwise a select can observe a
				// closed event channel first and misclassify a 401 as normal EOF.
				return
			}
		}
	}()
	return outputEvents, outputErrors
}
