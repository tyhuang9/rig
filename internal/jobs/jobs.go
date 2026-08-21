package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	WaitingUser    Status = "waiting_user"
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
	ErrEventBudget     = errors.New("job event budget exhausted")
	ErrJobNotPaused    = errors.New("job is not waiting for user action")
	ErrIdempotency     = errors.New("idempotency key conflicts with the original request")
)

// JobInput is deliberately sealed so new persisted input schemas are reviewed
// in this package before external callers can create them.
type JobInput interface{ jobInput() }

// NoInput preserves the legacy {} payload for resource actions without adding
// caller-controlled fields to the durable schema.
type NoInput struct{}

func (NoInput) jobInput() {}

type ConfigurationMode string

const (
	ConfigurationCurrent  ConfigurationMode = "current"
	ConfigurationOriginal ConfigurationMode = "original"
)

// DeploymentInput is the only durable input accepted for real deployment jobs.
// Runtime-derived paths, environment values, approvals, and diagnostics must
// never be accepted from an API caller or persisted in the job payload.
type DeploymentInput struct {
	ReleaseID         string            `json:"releaseId"`
	ConfigurationMode ConfigurationMode `json:"configurationMode"`
}

func (DeploymentInput) jobInput() {}

// CreateRequest is the controlled boundary for durable job creation.
type CreateRequest struct {
	Type           string
	ResourceType   string
	ResourceID     string
	IdempotencyKey string
	RequestedBy    string
	Input          JobInput
}

type Job struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	ResourceType     string          `json:"resourceType"`
	ResourceID       string          `json:"resourceId"`
	Status           string          `json:"status"`
	Phase            string          `json:"phase"`
	Checkpoint       string          `json:"checkpoint"`
	ErrorCode        string          `json:"errorCode,omitempty"`
	ErrorDetail      string          `json:"errorDetail,omitempty"`
	Progress         int             `json:"progress"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	RequestedBy      string          `json:"requestedBy,omitempty"`
	PauseDisposition string          `json:"pauseDisposition,omitempty"`
	Input            json.RawMessage `json:"-"`
	Attempt          int             `json:"-"`
}

type Event struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"jobId"`
	Sequence  int       `json:"sequence"`
	Attempt   int       `json:"attempt"`
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
	// Message is ignored. Persisted messages are centrally generated from Code.
	Message string
}

// ProgressReporter durably records executor progress. The runner owns terminal state.
type ProgressReporter interface{ Report(ProgressUpdate) error }

type ExecutionResult struct {
	// CompletionCode selects a centrally-owned completion message. Empty uses
	// the generic completion message.
	CompletionCode   string
	Disposition      ExecutionDisposition
	PauseDisposition string
}

type ExecutionDisposition string

const (
	ExecutionCompleted   ExecutionDisposition = ""
	ExecutionWaitingUser ExecutionDisposition = "waiting_user"
)

// ExecutionError identifies a known executor failure. Detail is retained for
// executor diagnostics only and is never persisted or returned to clients.
type ExecutionError struct {
	Code   string
	Detail string
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

type Executor interface {
	Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error)
}

type Service struct {
	db             *sql.DB
	now            func() time.Time
	wake           chan struct{}
	mu             sync.Mutex
	active         map[string]context.CancelFunc
	beforeClaim    func()
	beforeRegister func()
	beforeExecute  func()
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
	rows, err := tx.Query(`SELECT id FROM jobs WHERE status IN ('assigned','running','waiting_external') ORDER BY created_at`)
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
		if _, err := tx.Exec(`UPDATE jobs SET status='interrupted',phase='interrupted',pause_disposition=NULL,error_code='daemon_restarted',error_detail='Job interrupted because hostd restarted',updated_at=?,finished_at=? WHERE id=?`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id); err != nil {
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
	return s.CreateWithInput(CreateRequest{Type: kind, ResourceType: resourceType, ResourceID: resourceID, IdempotencyKey: idempotency, Input: NoInput{}})
}

func (s *Service) CreateWithInput(request CreateRequest) (Job, bool, error) {
	if request.Type == "" || request.ResourceType == "" || request.ResourceID == "" {
		return Job{}, false, fmt.Errorf("%w: type, resource type, and resource id are required", ErrInvalidInput)
	}
	input, err := marshalInput(request.Input)
	if err != nil {
		return Job{}, false, err
	}
	if request.IdempotencyKey != "" {
		existing, err := s.byIdempotency(request.Type, request.ResourceType, request.ResourceID, request.IdempotencyKey)
		if err == nil {
			if !sameCreateRequest(existing, request, input) {
				return Job{}, false, ErrIdempotency
			}
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
	job.RequestedBy = request.RequestedBy
	_, err = tx.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,idempotency_key,requested_by,input_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Type, job.ResourceType, job.ResourceID, job.Status, job.Phase, nullIfBlank(request.IdempotencyKey), nullIfBlank(request.RequestedBy), string(input), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if isConstraint(err) && request.IdempotencyKey != "" {
			_ = tx.Rollback()
			if existing, lookupErr := s.byIdempotency(request.Type, request.ResourceType, request.ResourceID, request.IdempotencyKey); lookupErr == nil {
				if !sameCreateRequest(existing, request, input) {
					return Job{}, false, ErrIdempotency
				}
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

func sameCreateRequest(existing Job, request CreateRequest, input json.RawMessage) bool {
	return existing.Type == request.Type &&
		existing.ResourceType == request.ResourceType &&
		existing.ResourceID == request.ResourceID &&
		existing.RequestedBy == request.RequestedBy &&
		bytes.Equal(existing.Input, input)
}

func marshalInput(input JobInput) (json.RawMessage, error) {
	if input == nil {
		return json.RawMessage(`{}`), nil
	}
	switch value := input.(type) {
	case NoInput:
		return json.RawMessage(`{}`), nil
	case DeploymentInput:
		if value.ReleaseID != "" {
			if _, err := uuid.Parse(value.ReleaseID); err != nil {
				return nil, fmt.Errorf("%w: release id must be a UUID", ErrInvalidInput)
			}
		}
		if value.ConfigurationMode != ConfigurationCurrent && value.ConfigurationMode != ConfigurationOriginal {
			return nil, fmt.Errorf("%w: configuration mode must be current or original", ErrInvalidInput)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrInvalidInput
		}
		return encoded, nil
	default:
		return nil, ErrInvalidInput
	}
}

func DeploymentInputFor(job Job) (DeploymentInput, error) {
	if job.Type != "deploy" {
		return DeploymentInput{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(job.Input))
	decoder.DisallowUnknownFields()
	var input DeploymentInput
	if err := decoder.Decode(&input); err != nil {
		return DeploymentInput{}, ErrInvalidInput
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return DeploymentInput{}, ErrInvalidInput
	}
	if _, err := marshalInput(input); err != nil {
		return DeploymentInput{}, err
	}
	return input, nil
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

const jobSelectColumns = `id,type,resource_type,resource_id,status,phase,progress_percent,checkpoint_json,COALESCE(error_code,''),COALESCE(error_detail,''),COALESCE(requested_by,''),COALESCE(pause_disposition,''),input_json,attempt,created_at,updated_at`

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
	if err := row.Scan(&j.ID, &j.Type, &j.ResourceType, &j.ResourceID, &j.Status, &j.Phase, &j.Progress, &j.Checkpoint, &j.ErrorCode, &j.ErrorDetail, &j.RequestedBy, &j.PauseDisposition, &input, &j.Attempt, &created, &updated); err != nil {
		return j, err
	}
	j.Input = json.RawMessage(input)
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return j, nil
}

func (s *Service) Append(jobID, level, phase, code, message string) (Event, error) {
	_ = message
	if level != "info" || code != "phase_started" || !knownPhase(phase) {
		return Event{}, ErrInvalidProgress
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	var status, statusPhase string
	var attempt, eventCount int
	if err := tx.QueryRow(`SELECT status,phase,attempt,(SELECT COUNT(*) FROM job_events WHERE job_id=jobs.id AND attempt=jobs.attempt) FROM jobs WHERE id=?`, jobID).Scan(&status, &statusPhase, &attempt, &eventCount); errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrJobNotFound
	} else if err != nil {
		return Event{}, err
	}
	if terminal(status) || (status == string(Waiting) && statusPhase == "cancelling") {
		return Event{}, ErrJobTerminal
	}
	if eventCount >= maxJobEventsPerAttempt-reservedTerminalEvents {
		return Event{}, ErrEventBudget
	}
	now := s.now().UTC()
	event, err := appendEvent(tx, now, jobID, "info", phase, "phase_started", "Job phase started")
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
	var attempt int
	if err := tx.QueryRow(`SELECT attempt FROM jobs WHERE id=?`, jobID).Scan(&attempt); err != nil {
		return Event{}, err
	}
	result, err := tx.Exec(`INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message) VALUES(?,?,?,?,?,?,?,?)`, jobID, sequence, attempt, now.Format(time.RFC3339Nano), level, phase, code, message)
	if err != nil {
		return Event{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	return Event{ID: id, JobID: jobID, Sequence: sequence, Attempt: attempt, Timestamp: now, Level: level, Phase: phase, Code: code, Message: message}, nil
}

func (s *Service) Events(jobID string, after int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id,job_id,sequence,attempt,timestamp,level,phase,code,message FROM job_events WHERE job_id=? AND id>? ORDER BY id`, jobID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at string
		if err = rows.Scan(&e.ID, &e.JobID, &e.Sequence, &e.Attempt, &at, &e.Level, &e.Phase, &e.Code, &e.Message); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) Update(id string, status Status, phase string, progress int, checkpoint string) error {
	_ = checkpoint
	return s.report(id, ProgressUpdate{Status: status, Phase: phase, Progress: progress, Code: "phase_started"})
}

func (s *Service) Cancel(id string) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var status, phase string
	if err := tx.QueryRow(`SELECT status,phase FROM jobs WHERE id=?`, id).Scan(&status, &phase); errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	} else if err != nil {
		return Job{}, err
	}
	if terminal(status) {
		return Job{}, ErrJobTerminal
	}
	if status == string(Waiting) && phase == "cancelling" {
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
	now := s.now().UTC()
	if status == string(Queued) || status == string(WaitingUser) {
		result, err := tx.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled',pause_disposition=NULL,updated_at=?,finished_at=? WHERE id=? AND status IN ('queued','waiting_user')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id)
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
	} else {
		result, err := tx.Exec(`UPDATE jobs SET status='waiting_external',phase='cancelling',updated_at=? WHERE id=? AND status IN ('assigned','running','waiting_external','waiting_user') AND NOT (status='waiting_external' AND phase='cancelling')`, now.Format(time.RFC3339Nano), id)
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
		if _, err := appendEvent(tx, now, id, "warn", "cancelling", "cancellation_requested", "Cancellation requested"); err != nil {
			return Job{}, err
		}
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

// Resume atomically requeues an intentional user pause. The next claim starts
// a new attempt; no executor can observe a partially resumed job.
func (s *Service) Resume(id string) (Job, error) {
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
	if status != string(WaitingUser) {
		return Job{}, ErrJobNotPaused
	}
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status='queued',phase='queued',progress_percent=0,checkpoint_json='{}',pause_disposition=NULL,error_code=NULL,error_detail=NULL,started_at=NULL,finished_at=NULL,updated_at=? WHERE id=? AND status='waiting_user'`, formattedNow, id)
	if err != nil {
		return Job{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if updated != 1 {
		return Job{}, ErrJobNotPaused
	}
	if _, err := appendEvent(tx, now, id, "info", "queued", "job_resumed", "Job resumed"); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
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

const (
	maxCheckpointBytes     = 4 << 10
	maxJobEventsPerAttempt = 32
	reservedTerminalEvents = 2
)

func (s *Service) report(id string, update ProgressUpdate) error {
	code, message, err := progressEvent(update)
	if err != nil {
		return err
	}
	checkpoint := checkpointForPhase(update.Phase)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, phase string
	var progress, eventCount int
	if err := tx.QueryRow(`SELECT status,phase,progress_percent,(SELECT COUNT(*) FROM job_events WHERE job_id=jobs.id AND attempt=jobs.attempt) FROM jobs WHERE id=?`, id).Scan(&status, &phase, &progress, &eventCount); errors.Is(err, sql.ErrNoRows) {
		return ErrJobNotFound
	} else if err != nil {
		return err
	}
	if (status != string(Assigned) && status != string(Running) && status != string(Waiting)) || (status == string(Waiting) && phase == "cancelling") {
		return ErrJobTerminal
	}
	if update.Progress < progress {
		return ErrInvalidProgress
	}
	if eventCount >= maxJobEventsPerAttempt-reservedTerminalEvents {
		return ErrEventBudget
	}
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status=?,phase=?,progress_percent=?,checkpoint_json=?,updated_at=?,started_at=COALESCE(started_at,?) WHERE id=? AND status IN ('assigned','running','waiting_external') AND NOT (status='waiting_external' AND phase='cancelling') AND progress_percent<=?`, update.Status, update.Phase, update.Progress, checkpoint, formattedNow, formattedNow, id, update.Progress)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrJobTerminal
	}
	if _, err := appendEvent(tx, now, id, "info", update.Phase, code, message); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

func progressEvent(update ProgressUpdate) (string, string, error) {
	if (update.Status != Running && update.Status != Waiting) || update.Progress < 0 || update.Progress > 100 || !knownPhase(update.Phase) || (update.Level != "" && update.Level != "info") {
		return "", "", ErrInvalidProgress
	}
	switch update.Code {
	case "fake_phase_started":
		if !fakePhase(update.Phase) {
			return "", "", ErrInvalidProgress
		}
		return "phase_started", "Fake runtime: " + update.Phase, nil
	case "phase_started":
		return "phase_started", "Job phase started", nil
	case "waiting_external":
		if update.Status != Waiting {
			return "", "", ErrInvalidProgress
		}
		return "waiting_external", "Job waiting for external work", nil
	default:
		return "", "", ErrInvalidProgress
	}
}

func fakePhase(phase string) bool {
	switch phase {
	case "validate", "prepare_workspace", "apply_fake_runtime", "wait_for_health", "finalize":
		return true
	default:
		return false
	}
}

func knownPhase(phase string) bool {
	switch phase {
	case "validate", "prepare_workspace", "materialize_release", "render_compose", "evaluate_policy", "apply_runtime", "apply_fake_runtime", "wait_for_health", "finalize", "running", "waiting_external":
		return true
	default:
		return false
	}
}

func checkpointForPhase(phase string) string { return `{"phase":"` + phase + `"}` }

func normalizeCheckpoint(checkpoint string) (string, error) {
	if len(checkpoint) > maxCheckpointBytes {
		return "", ErrInvalidProgress
	}
	normalized, err := normalizeInput(json.RawMessage(checkpoint))
	if err != nil {
		return "", ErrInvalidProgress
	}
	return string(normalized), nil
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
		if ctx.Err() != nil {
			return nil
		}
		if s.beforeClaim != nil {
			s.beforeClaim()
		}
		if err := s.runOne(ctx, executor); err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return nil
			}
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
	if s.beforeRegister != nil {
		s.beforeRegister()
	}
	s.mu.Lock()
	s.active[job.ID] = cancel
	assigned, stateErr := s.isAssigned(job.ID)
	s.mu.Unlock()
	if stateErr != nil {
		s.unregister(job.ID, cancel)
		return stateErr
	}
	if !assigned {
		cancelling, cancellationErr := s.cancellationRequested(job.ID)
		s.unregister(job.ID, cancel)
		if cancellationErr != nil {
			return cancellationErr
		}
		if cancelling {
			return s.finishCancellation(job.ID)
		}
		return nil
	}
	if s.beforeExecute != nil {
		s.beforeExecute()
	}
	if jobCtx.Err() != nil {
		s.unregister(job.ID, cancel)
		cancelling, cancellationErr := s.cancellationRequested(job.ID)
		if cancellationErr != nil {
			return cancellationErr
		}
		if cancelling {
			return s.finishCancellation(job.ID)
		}
		return nil
	}

	stopWatching := s.startCancellationWatcher(jobCtx, job.ID, cancel)
	result, executionErr := invokeExecutor(jobCtx, executor, job, reporter{service: s, jobID: job.ID})
	watcherErr := stopWatching()
	s.unregister(job.ID, cancel)
	validPause := executionErr == nil && result.Disposition == ExecutionWaitingUser && result.PauseDisposition == "approval_required" && result.CompletionCode == ""
	if validPause {
		pauseErr := s.pause(job.ID, result.PauseDisposition)
		if watcherErr != nil {
			return watcherErr
		}
		if errors.Is(pauseErr, ErrJobTerminal) || errors.Is(pauseErr, ErrJobNotFound) {
			return nil
		}
		return pauseErr
	}
	validSuccess := executionErr == nil && validateResult(result) == nil
	if validSuccess {
		successErr := s.succeed(job.ID, result.CompletionCode)
		if watcherErr != nil {
			return watcherErr
		}
		if errors.Is(successErr, ErrJobTerminal) || errors.Is(successErr, ErrJobNotFound) {
			return nil
		}
		return successErr
	}
	cancelling, err := s.cancellationRequested(job.ID)
	if err != nil {
		if watcherErr != nil {
			return watcherErr
		}
		return err
	}
	if cancelling {
		finishErr := s.finishCancellation(job.ID)
		if watcherErr != nil {
			return watcherErr
		}
		return finishErr
	}
	if watcherErr != nil {
		return watcherErr
	}
	if ctx.Err() != nil {
		return nil
	}
	if executionErr != nil {
		code, detail := safeExecutionFailure(executionErr)
		progress, checkpoint := s.failureState(job.ID)
		return s.resolveTerminalConflict(job.ID, s.fail(job.ID, progress, checkpoint, code, detail))
	}
	progress, checkpoint := s.failureState(job.ID)
	return s.resolveTerminalConflict(job.ID, s.fail(job.ID, progress, checkpoint, "executor_invalid_result", "Job execution failed"))
}

func (s *Service) pause(id, disposition string) error {
	if disposition != "approval_required" {
		return ErrInvalidProgress
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status='waiting_user',phase='approval_required',pause_disposition=?,updated_at=?,started_at=COALESCE(started_at,?) WHERE id=? AND status IN ('assigned','running','waiting_external') AND NOT (status='waiting_external' AND phase='cancelling')`, disposition, formattedNow, formattedNow, id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrJobTerminal
	}
	if _, err := appendEvent(tx, now, id, "warn", "approval_required", "approval_required", "Deployment requires approval"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

const cancellationWatchInterval = 25 * time.Millisecond

func (s *Service) startCancellationWatcher(jobCtx context.Context, id string, cancel context.CancelFunc) func() error {
	watchCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- s.watchCancellation(watchCtx, jobCtx, id, cancel)
	}()
	return func() error {
		stop()
		return <-done
	}
}

func (s *Service) watchCancellation(watchCtx, jobCtx context.Context, id string, cancel context.CancelFunc) error {
	ticker := time.NewTicker(cancellationWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-watchCtx.Done():
			return nil
		case <-jobCtx.Done():
			return nil
		case <-ticker.C:
			cancelling, err := s.cancellationRequestedContext(watchCtx, id)
			if err != nil {
				if watchCtx.Err() != nil {
					return nil
				}
				cancel()
				return err
			}
			if cancelling {
				cancel()
				return nil
			}
		}
	}
}

var errExecutorPanic = errors.New("executor panicked")

func invokeExecutor(ctx context.Context, executor Executor, job Job, reporter ProgressReporter) (result ExecutionResult, err error) {
	defer func() {
		if recover() != nil {
			result = ExecutionResult{}
			err = errExecutorPanic
		}
	}()
	return executor.Execute(ctx, job, reporter)
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

func (s *Service) failureState(id string) (int, string) {
	job, err := s.Get(id)
	if err != nil || job.Progress < 0 || job.Progress > 100 {
		return 0, `{"phase":"failed"}`
	}
	return job.Progress, checkpointOrDefault(job.Checkpoint, "failed")
}

func (s *Service) cancellationRequested(id string) (bool, error) {
	return s.cancellationRequestedContext(context.Background(), id)
}

func (s *Service) cancellationRequestedContext(ctx context.Context, id string) (bool, error) {
	var status, phase string
	if err := s.db.QueryRowContext(ctx, `SELECT status,phase FROM jobs WHERE id=?`, id).Scan(&status, &phase); err != nil {
		return false, err
	}
	return status == string(Waiting) && phase == "cancelling", nil
}

func (s *Service) resolveTerminalConflict(id string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrJobTerminal) && !errors.Is(err, ErrJobNotFound) {
		return err
	}
	cancelling, checkErr := s.cancellationRequested(id)
	if checkErr != nil {
		if errors.Is(checkErr, sql.ErrNoRows) {
			return nil
		}
		return checkErr
	}
	if cancelling {
		return s.finishCancellation(id)
	}
	return nil
}

func (s *Service) finishCancellation(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled',updated_at=?,finished_at=? WHERE id=? AND status='waiting_external' AND phase='cancelling'`, formattedNow, formattedNow, id)
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
	if _, err := appendEvent(tx, now, id, "warn", "cancelled", "job_cancelled", "Job cancelled"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *Service) fail(id string, progress int, checkpoint, code, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status='failed',phase='failed',progress_percent=?,checkpoint_json=?,error_code=?,error_detail=?,updated_at=?,started_at=COALESCE(started_at,?),finished_at=? WHERE id=? AND status IN ('assigned','running','waiting_external') AND NOT (status='waiting_external' AND phase='cancelling')`, progress, checkpoint, code, detail, formattedNow, formattedNow, formattedNow, id)
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

func (s *Service) succeed(id, completionCode string) error {
	message, err := completionMessage(completionCode)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',progress_percent=100,checkpoint_json='{"phase":"succeeded"}',error_code=NULL,error_detail=NULL,updated_at=?,started_at=COALESCE(started_at,?),finished_at=? WHERE id=? AND status IN ('assigned','running','waiting_external')`, formattedNow, formattedNow, formattedNow, id)
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
	if _, err := appendEvent(tx, now, id, "info", "succeeded", "job_succeeded", message); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

func completionMessage(code string) (string, error) {
	switch code {
	case "":
		return "Job completed", nil
	case "fake_deployment_completed":
		return "Fake deployment completed", nil
	default:
		return "", ErrInvalidProgress
	}
}

func safeExecutionFailure(err error) (string, string) {
	var executionError *ExecutionError
	if errors.As(err, &executionError) {
		switch executionError.Code {
		case "validation_failed":
			return "validation_failed", "Job validation failed"
		case "runtime_unavailable":
			return "runtime_unavailable", "Runtime unavailable"
		}
	}
	return "executor_failed", "Job execution failed"
}

func checkpointOrDefault(checkpoint, phase string) string {
	if normalized, err := normalizeCheckpoint(checkpoint); err == nil {
		return normalized
	}
	return `{"phase":"` + phase + `"}`
}

func validateResult(result ExecutionResult) error {
	if result.Disposition != ExecutionCompleted || result.PauseDisposition != "" {
		return ErrInvalidProgress
	}
	_, err := completionMessage(result.CompletionCode)
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
		if err := reporter.Report(ProgressUpdate{Status: Running, Phase: phase.name, Progress: phase.progress, Checkpoint: `{"phase":"` + phase.name + `"}`, Level: "info", Code: "fake_phase_started"}); err != nil {
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
	return ExecutionResult{CompletionCode: "fake_deployment_completed"}, nil
}

// RunFakeWorker is retained for existing callers.
func (s *Service) RunFakeWorker(ctx context.Context) { _ = s.RunWorker(ctx, NewFakeExecutor()) }
