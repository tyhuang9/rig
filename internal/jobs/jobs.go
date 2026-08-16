package jobs

import (
	"context"
	"database/sql"
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
	ErrJobNotFound = errors.New("job not found")
	ErrJobTerminal = errors.New("job is already terminal")
)

type Job struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Status       string    `json:"status"`
	Phase        string    `json:"phase"`
	Checkpoint   string    `json:"checkpoint"`
	ErrorCode    string    `json:"errorCode,omitempty"`
	ErrorDetail  string    `json:"errorDetail,omitempty"`
	Progress     int       `json:"progress"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
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
type Service struct {
	db   *sql.DB
	now  func() time.Time
	wake chan struct{}
	mu   sync.Mutex
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now, wake: make(chan struct{}, 1)} }
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
func (s *Service) Create(kind, resourceType, resourceID, idempotency string) (Job, bool, error) {
	if idempotency != "" {
		existing, err := s.byIdempotency(kind, resourceType, resourceID, idempotency)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Job{}, false, err
		}
	}
	now := s.now().UTC()
	job := Job{ID: uuid.NewString(), Type: kind, ResourceType: resourceType, ResourceID: resourceID, Status: string(Queued), Phase: "queued", CreatedAt: now, UpdatedAt: now}
	_, err := s.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, job.ID, job.Type, job.ResourceType, job.ResourceID, job.Status, job.Phase, nullIfBlank(idempotency), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if isConstraint(err) {
			return Job{}, false, fmt.Errorf("an application mutation is already active: %w", err)
		}
		return Job{}, false, err
	}
	if _, err = s.Append(job.ID, "info", "queued", "job_queued", "Job queued"); err != nil {
		return Job{}, false, err
	}
	s.signal()
	return job, true, nil
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
func (s *Service) byIdempotency(kind, rt, rid, key string) (Job, error) {
	return s.scan(s.db.QueryRow(`SELECT id,type,resource_type,resource_id,status,phase,progress_percent,checkpoint_json,COALESCE(error_code,''),COALESCE(error_detail,''),created_at,updated_at FROM jobs WHERE type=? AND resource_type=? AND resource_id=? AND idempotency_key=?`, kind, rt, rid, key))
}
func (s *Service) Get(id string) (Job, error) {
	return s.scan(s.db.QueryRow(`SELECT id,type,resource_type,resource_id,status,phase,progress_percent,checkpoint_json,COALESCE(error_code,''),COALESCE(error_detail,''),created_at,updated_at FROM jobs WHERE id=?`, id))
}

func (s *Service) List(limit int) ([]Job, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,type,resource_type,resource_id,status,phase,progress_percent,checkpoint_json,COALESCE(error_code,''),COALESCE(error_detail,''),created_at,updated_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Job, 0)
	for rows.Next() {
		var job Job
		var created, updated string
		if err := rows.Scan(&job.ID, &job.Type, &job.ResourceType, &job.ResourceID, &job.Status, &job.Phase, &job.Progress, &job.Checkpoint, &job.ErrorCode, &job.ErrorDetail, &created, &updated); err != nil {
			return nil, err
		}
		job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, job)
	}
	return result, rows.Err()
}
func (s *Service) scan(row *sql.Row) (Job, error) {
	var j Job
	var c, u string
	err := row.Scan(&j.ID, &j.Type, &j.ResourceType, &j.ResourceID, &j.Status, &j.Phase, &j.Progress, &j.Checkpoint, &j.ErrorCode, &j.ErrorDetail, &c, &u)
	if err != nil {
		return j, err
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
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
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		if _, err := s.Get(id); errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		}
		return ErrJobTerminal
	}
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

// RunFakeWorker is intentionally the only Phase A runtime executor. It persists every phase before progressing.
func (s *Service) RunFakeWorker(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.runOne(ctx)
		case <-ticker.C:
			s.runOne(ctx)
		}
	}
}
func (s *Service) runOne(ctx context.Context) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM jobs WHERE status='queued' ORDER BY created_at LIMIT 1`).Scan(&id)
	if err != nil {
		return
	}
	j, err := s.Get(id)
	if err != nil {
		return
	}
	phases := []struct {
		n string
		p int
	}{{"validate", 15}, {"prepare_workspace", 35}, {"apply_fake_runtime", 70}, {"wait_for_health", 90}, {"finalize", 100}}
	for _, phase := range phases {
		select {
		case <-ctx.Done():
			return
		default:
		}
		current, err := s.Get(j.ID)
		if err != nil || current.Status == string(Cancelled) {
			return
		}
		if err := s.Update(j.ID, Running, phase.n, phase.p, `{"phase":"`+phase.n+`"}`); err != nil {
			return
		}
		_, _ = s.Append(j.ID, "info", phase.n, "phase_started", "Fake runtime: "+phase.n)
		time.Sleep(120 * time.Millisecond)
	}
	if err := s.Update(j.ID, Succeeded, "succeeded", 100, `{"phase":"succeeded"}`); err != nil {
		return
	}
	_, _ = s.Append(j.ID, "info", "succeeded", "job_succeeded", "Fake deployment completed")
}
