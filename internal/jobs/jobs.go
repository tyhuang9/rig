package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	Queued         Status = "queued"
	Assigned       Status = "assigned"
	Running        Status = "running"
	Waiting        Status = "waiting_external"
	Succeeded      Status = "succeeded"
	Failed         Status = "failed"
	Cancelled      Status = "cancelled"
	Interrupted    Status = "interrupted"
	NeedsAttention Status = "needs_attention"
)

var (
	ErrJobNotFound     = errors.New("job not found")
	ErrJobTerminal     = errors.New("job is already terminal")
	ErrInvalidInput    = errors.New("invalid job input")
	ErrInvalidProgress = errors.New("invalid job progress")
)

// CreateRequest is the controlled boundary for durable job creation.
type CreateRequest struct {
	Type           string
	ResourceType   string
	ResourceID     string
	IdempotencyKey string
	Input          json.RawMessage
}

type Job struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	ResourceType string          `json:"resourceType"`
	ResourceID   string          `json:"resourceId"`
	Status       string          `json:"status"`
	Phase        string          `json:"phase"`
	Checkpoint   string          `json:"checkpoint"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	ErrorDetail  string          `json:"errorDetail,omitempty"`
	Progress     int             `json:"progress"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Input        json.RawMessage `json:"-"`
	Attempt      int             `json:"-"`
}

type Event struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"jobId"`
	Sequence  int       `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Phase     string    `json:"phase"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
}

// ProgressUpdate is an executor-owned nonterminal state update.
type ProgressUpdate struct {
	Status     Status
	Phase      string
	Progress   int
	Checkpoint string
	Level      string
	Code       string
	Message    string
}

// ProgressReporter durably records executor progress. The runner owns terminal state.
type ProgressReporter interface{ Report(ProgressUpdate) error }

type ExecutionResult struct {
	Phase      string
	Progress   int
	Checkpoint string
	Message    string
}

// ExecutionError carries only values that are safe to persist and expose.
type ExecutionError struct {
	Code   string
	Detail string
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return e.Detail
	}
	return e.Code
}

type Executor interface {
	Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error)
}

type Service struct {
	db     *sql.DB
	now    func() time.Time
	wake   chan struct{}
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func New(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now, wake: make(chan struct{}, 1), active: make(map[string]context.CancelFunc)}
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) RecoverInterrupted() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM jobs WHERE status IN ('assigned','running','waiting_external','waiting_user') ORDER BY created_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now().UTC()
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE jobs SET status='interrupted',phase='interrupted',error_code='daemon_restarted',error_detail='hostd restarted while this job was active',updated_at=?,finished_at=? WHERE id=?`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		if _, err := appendEvent(tx, now, id, "error", "interrupted", "daemon_restarted", "Job interrupted because hostd restarted"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Create preserves the legacy no-input API.
func (s *Service) Create(kind, resourceType, resourceID, idempotency string) (Job, bool, error) {
	return s.CreateWithInput(CreateRequest{Type: kind, ResourceType: resourceType, ResourceID: resourceID, IdempotencyKey: idempotency, Input: json.RawMessage(`{}`)})
}

func (s *Service) CreateWithInput(request CreateRequest) (Job, bool, error) {
	input, err := normalizeInput(request.Input)
	if err != nil {
		return Job{}, false, err
	}
	if request.Type == "" || request.ResourceType == "" || request.ResourceID == "" {
		return Job{}, false, fmt.Errorf("%w: type, resource type, and resource id are required", ErrInvalidInput)
	}
	if request.IdempotencyKey != "" {
		existing, err := s.byIdempotency(request.Type, request.ResourceType, request.ResourceID, request.IdempotencyKey)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Job{}, false, err
		}
	}
	now := s.now().UTC()
	job := Job{ID: uuid.NewString(), Type: request.Type, ResourceType: request.ResourceType, ResourceID: request.ResourceID, Status: string(Queued), Phase: "queued", CreatedAt: now, UpdatedAt: now, Input: input}
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,idempotency_key,input_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Type, job.ResourceType, job.ResourceID, job.Status, job.Phase, nullIfBlank(request.IdempotencyKey), string(input), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if isConstraint(err) && request.IdempotencyKey != "" {
			_ = tx.Rollback()
			if existing, lookupErr := s.byIdempotency(request.Type, request.ResourceType, request.ResourceID, request.IdempotencyKey); lookupErr == nil {
				return existing, false, nil
			}
		}
		if isConstraint(err) {
			return Job{}, false, fmt.Errorf("an application mutation is already active: %w", err)
		}
		return Job{}, false, err
	}
	if _, err := appendEvent(tx, now, job.ID, "info", "queued", "job_queued", "Job queued"); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	s.signal()
	return job, true, nil
}

func normalizeInput(input json.RawMessage) (json.RawMessage, error) {
	if len(input) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(input) > 64<<10 || !json.Valid(input) {
		return nil, ErrInvalidInput
	}
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("%w: input must be a JSON object", ErrInvalidInput)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func nullIfBlank(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func isConstraint(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE") || contains(err.Error(), "constraint"))
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

const jobSelectColumns = `id,type,resource_type,resource_id,status,phase,progress_percent,checkpoint_json,COALESCE(error_code,''),COALESCE(error_detail,''),input_json,attempt,created_at,updated_at`

func (s *Service) byIdempotency(kind, rt, rid, key string) (Job, error) {
	return s.scan(s.db.QueryRow(`SELECT `+jobSelectColumns+` FROM jobs WHERE type=? AND resource_type=? AND resource_id=? AND idempotency_key=?`, kind, rt, rid, key))
}
func (s *Service) Get(id string) (Job, error) {
	return s.scan(s.db.QueryRow(`SELECT `+jobSelectColumns+` FROM jobs WHERE id=?`, id))
}

func (s *Service) List(limit int) ([]Job, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+jobSelectColumns+` FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func (s *Service) scan(row *sql.Row) (Job, error) { return scanJob(row) }
func scanJob(row rowScanner) (Job, error) {
	var j Job
	var input, created, updated string
	if err := row.Scan(&j.ID, &j.Type, &j.ResourceType, &j.ResourceID, &j.Status, &j.Phase, &j.Progress, &j.Checkpoint, &j.ErrorCode, &j.ErrorDetail, &input, &j.Attempt, &created, &updated); err != nil {
		return j, err
	}
	j.Input = json.RawMessage(input)
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return j, nil
}

func (s *Service) Append(jobID, level, phase, code, message string) (Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	event, err := appendEvent(tx, now, jobID, level, phase, code, message)
	if err != nil {
		return Event{}, err
	}
	if err = tx.Commit(); err != nil {
		return Event{}, err
	}
	s.signal()
	return event, nil
}

func appendEvent(tx *sql.Tx, now time.Time, jobID, level, phase, code, message string) (Event, error) {
	var sequence int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence),0)+1 FROM job_events WHERE job_id=?`, jobID).Scan(&sequence); err != nil {
		return Event{}, err
	}
	result, err := tx.Exec(`INSERT INTO job_events(job_id,sequence,timestamp,level,phase,code,message) VALUES(?,?,?,?,?,?,?)`, jobID, sequence, now.Format(time.RFC3339Nano), level, phase, code, message)
	if err != nil {
		return Event{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	return Event{ID: id, JobID: jobID, Sequence: sequence, Timestamp: now, Level: level, Phase: phase, Code: code, Message: message}, nil
}

func (s *Service) Events(jobID string, after int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id,job_id,sequence,timestamp,level,phase,code,message FROM job_events WHERE job_id=? AND id>? ORDER BY id`, jobID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at string
		if err = rows.Scan(&e.ID, &e.JobID, &e.Sequence, &at, &e.Level, &e.Phase, &e.Code, &e.Message); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) Update(id string, status Status, phase string, progress int, checkpoint string) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(`UPDATE jobs SET status=?,phase=?,progress_percent=?,checkpoint_json=?,updated_at=?,started_at=COALESCE(started_at,?),finished_at=CASE WHEN ? IN ('succeeded','failed','cancelled','interrupted','needs_attention') THEN ? ELSE NULL END WHERE id=? AND status IN ('queued','assigned','running','waiting_external','waiting_user')`, status, phase, progress, checkpoint, now, now, status, now, id)
	if err != nil {
		return err
	}
	return s.updated(result, id)
}

func (s *Service) updated(result sql.Result, id string) error {
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 1 {
		return nil
	}
	if _, err := s.Get(id); errors.Is(err, sql.ErrNoRows) {
		return ErrJobNotFound
	} else if err != nil {
		return err
	}
	return ErrJobTerminal
}

// transition remains for package-level compatibility tests. Executor progress
// uses report, which has a stricter state guard.
func (s *Service) transition(id string, status Status, phase string, progress int, checkpoint, level, code, message string) error {
	return s.transitionWithGuard(id, status, phase, progress, checkpoint, level, code, message, `('queued','assigned','running','waiting_external','waiting_user')`)
}

func (s *Service) transitionWithGuard(id string, status Status, phase string, progress int, checkpoint, level, code, message, allowed string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status=?,phase=?,progress_percent=?,checkpoint_json=?,updated_at=?,started_at=COALESCE(started_at,?),finished_at=CASE WHEN ? IN ('succeeded','failed','cancelled','interrupted','needs_attention') THEN ? ELSE NULL END WHERE id=? AND status IN `+allowed, status, phase, progress, checkpoint, formattedNow, formattedNow, status, formattedNow, id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM jobs WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		} else if err != nil {
			return err
		}
		return ErrJobTerminal
	}
	if _, err := appendEvent(tx, now, id, level, phase, code, message); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *Service) Cancel(id string) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM jobs WHERE id=?`, id).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	} else if err != nil {
		return Job{}, err
	}
	if terminal(status) {
		return Job{}, ErrJobTerminal
	}
	now := s.now().UTC()
	result, err := tx.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled',updated_at=?,finished_at=? WHERE id=? AND status IN ('queued','assigned','running','waiting_external','waiting_user')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
	if err != nil {
		return Job{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if updated != 1 {
		return Job{}, ErrJobTerminal
	}
	if _, err := appendEvent(tx, now, id, "warn", "cancelled", "job_cancelled", "Job cancelled"); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.signal()
	return s.Get(id)
}

func terminal(status string) bool {
	switch Status(status) {
	case Succeeded, Failed, Cancelled, Interrupted, NeedsAttention:
		return true
	default:
		return false
	}
}

// claimNext atomically reserves the oldest queued job and records its assignment.
func (s *Service) claimNext(ctx context.Context) (Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	row := tx.QueryRowContext(ctx, `WITH next AS (SELECT id FROM jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1)
		UPDATE jobs SET status='assigned',phase='assigned',attempt=attempt+1,updated_at=?
		WHERE id=(SELECT id FROM next) AND status='queued'
		RETURNING `+jobSelectColumns, now.Format(time.RFC3339Nano))
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	if _, err := appendEvent(tx, now, job.ID, "info", "assigned", "job_assigned", "Job assigned"); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	s.signal()
	return job, true, nil
}

type reporter struct {
	service *Service
	jobID   string
}

func (r reporter) Report(update ProgressUpdate) error { return r.service.report(r.jobID, update) }

func (s *Service) report(id string, update ProgressUpdate) error {
	if (update.Status != Running && update.Status != Waiting) || update.Phase == "" || update.Code == "" || update.Message == "" || update.Progress < 0 || update.Progress > 100 {
		return ErrInvalidProgress
	}
	if update.Level == "" {
		update.Level = "info"
	}
	checkpoint, err := normalizeInput(json.RawMessage(update.Checkpoint))
	if err != nil {
		return ErrInvalidProgress
	}
	return s.transitionWithGuard(id, update.Status, update.Phase, update.Progress, string(checkpoint), update.Level, update.Code, update.Message, `('assigned','running','waiting_external')`)
}

// RunWorker returns only infrastructure failures; executor failures are persisted.
func (s *Service) RunWorker(ctx context.Context, executor Executor) error {
	if executor == nil {
		return errors.New("job executor is required")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
		case <-ticker.C:
		}
		if err := s.runOne(ctx, executor); err != nil {
			return err
		}
	}
}

func (s *Service) runOne(ctx context.Context, executor Executor) error {
	job, claimed, err := s.claimNext(ctx)
	if err != nil || !claimed {
		return err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.active[job.ID] = cancel
	assigned, stateErr := s.isAssigned(job.ID)
	s.mu.Unlock()
	if stateErr != nil {
		s.unregister(job.ID, cancel)
		return stateErr
	}
	if !assigned || jobCtx.Err() != nil {
		s.unregister(job.ID, cancel)
		return nil
	}

	result, executionErr := executor.Execute(jobCtx, job, reporter{service: s, jobID: job.ID})
	s.unregister(job.ID, cancel)
	if executionErr != nil {
		code, detail := safeExecutionFailure(executionErr)
		err := s.fail(job.ID, result.Progress, checkpointOrDefault(result.Checkpoint, "failed"), code, detail)
		if errors.Is(err, ErrJobTerminal) || errors.Is(err, ErrJobNotFound) {
			return nil
		}
		return err
	}
	if err := validateResult(result); err != nil {
		err = s.fail(job.ID, 0, `{"phase":"failed"}`, "executor_invalid_result", "Job execution failed")
		if errors.Is(err, ErrJobTerminal) || errors.Is(err, ErrJobNotFound) {
			return nil
		}
		return err
	}
	err = s.transitionWithGuard(job.ID, Succeeded, result.Phase, result.Progress, result.Checkpoint, "info", "job_succeeded", result.Message, `('assigned','running','waiting_external')`)
	if errors.Is(err, ErrJobTerminal) || errors.Is(err, ErrJobNotFound) {
		return nil
	}
	return err
}

func (s *Service) isAssigned(id string) (bool, error) {
	var status string
	err := s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return status == string(Assigned), err
}

func (s *Service) unregister(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
	cancel()
}

func (s *Service) fail(id string, progress int, checkpoint, code, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status='failed',phase='failed',progress_percent=?,checkpoint_json=?,error_code=?,error_detail=?,updated_at=?,started_at=COALESCE(started_at,?),finished_at=? WHERE id=? AND status IN ('assigned','running','waiting_external')`, progress, checkpoint, code, detail, formattedNow, formattedNow, formattedNow, id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM jobs WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		} else if err != nil {
			return err
		}
		return ErrJobTerminal
	}
	if _, err := appendEvent(tx, now, id, "error", "failed", "job_failed", detail); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

func safeExecutionFailure(err error) (string, string) {
	var executionError *ExecutionError
	if errors.As(err, &executionError) && executionError.Code != "" && executionError.Detail != "" {
		return executionError.Code, executionError.Detail
	}
	return "executor_failed", "Job execution failed"
}

func checkpointOrDefault(checkpoint, phase string) string {
	if normalized, err := normalizeInput(json.RawMessage(checkpoint)); err == nil {
		return string(normalized)
	}
	return `{"phase":"` + phase + `"}`
}

func validateResult(result ExecutionResult) error {
	if result.Phase == "" || result.Message == "" || result.Progress < 0 || result.Progress > 100 {
		return ErrInvalidProgress
	}
	_, err := normalizeInput(json.RawMessage(result.Checkpoint))
	return err
}

// FakeExecutor preserves the existing Phase A runtime sequence.
type FakeExecutor struct{ phaseDelay time.Duration }

func NewFakeExecutor() *FakeExecutor { return &FakeExecutor{phaseDelay: 120 * time.Millisecond} }

func (e *FakeExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	phases := []struct {
		name     string
		progress int
	}{{"validate", 15}, {"prepare_workspace", 35}, {"apply_fake_runtime", 70}, {"wait_for_health", 90}, {"finalize", 100}}
	for _, phase := range phases {
		if err := reporter.Report(ProgressUpdate{Status: Running, Phase: phase.name, Progress: phase.progress, Checkpoint: `{"phase":"` + phase.name + `"}`, Level: "info", Code: "phase_started", Message: "Fake runtime: " + phase.name}); err != nil {
			return ExecutionResult{}, err
		}
		timer := time.NewTimer(e.phaseDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ExecutionResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return ExecutionResult{Phase: "succeeded", Progress: 100, Checkpoint: `{"phase":"succeeded"}`, Message: "Fake deployment completed"}, nil
}

// RunFakeWorker is retained for existing callers.
func (s *Service) RunFakeWorker(ctx context.Context) { _ = s.RunWorker(ctx, NewFakeExecutor()) }
