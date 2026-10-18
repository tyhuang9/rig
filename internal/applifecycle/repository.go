// Package applifecycle stores application desired lifecycle intent separately
// from observed application, route, deployment, and container state.
package applifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type State string

const (
	Running State = "running"
	Stopped State = "stopped"

	authorSavepointStatementTimeout = 5 * time.Second
)

type Action string

const (
	Start   Action = "start"
	Stop    Action = "stop"
	Restart Action = "restart"
)

var (
	ErrApplicationNotFound = errors.New("application desired lifecycle not found")
	ErrJobNotFound         = errors.New("lifecycle job not found")
	ErrJobMismatch         = errors.New("lifecycle job does not match transition")
	ErrInvalidAction       = errors.New("invalid lifecycle action")
	ErrInvalidRequest      = errors.New("invalid lifecycle transition request")
	ErrRevisionConflict    = errors.New("application desired lifecycle revision conflict")
	ErrOperationConflict   = errors.New("lifecycle operation conflicts with retained transition")
)

// Head is desired intent only. A nil DesiredState with revision zero is an
// explicitly uninitialized head and says nothing about observed runtime state.
type Head struct {
	ApplicationID string
	DesiredState  *State
	Revision      int64
	UpdatedAt     *time.Time
}

// Transition is an immutable historical desired-intent decision.
type Transition struct {
	ApplicationID    string
	Revision         int64
	ExpectedRevision int64
	PreviousState    *State
	DesiredState     State
	JobID            string
	Action           Action
	ActorID          string
	CreatedAt        time.Time
}

type AuthorRequest struct {
	ApplicationID    string
	JobID            string
	Action           Action
	ActorID          string
	ExpectedRevision int64
}

type Repository struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db, now: time.Now}
}

func (r *Repository) Head(ctx context.Context, applicationID string) (Head, error) {
	if applicationID == "" {
		return Head{}, ErrApplicationNotFound
	}
	return scanHead(r.db.QueryRowContext(ctx, `
		SELECT app_id, desired_state, revision_number, updated_at
		FROM application_desired_lifecycle_heads
		WHERE app_id = ?`, applicationID))
}

func (r *Repository) History(ctx context.Context, applicationID string) ([]Transition, error) {
	if _, err := r.Head(ctx, applicationID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT app_id, revision_number, expected_revision, previous_state,
		       desired_state, job_id, action, actor_id, created_at
		FROM application_desired_lifecycle_transitions
		WHERE app_id = ?
		ORDER BY revision_number`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Transition, 0)
	for rows.Next() {
		transition, err := scanTransition(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, transition)
	}
	return result, rows.Err()
}

// AuthorTransition appends desired intent inside a caller-owned transaction.
// It never commits or performs runtime effects. Ordinary failures roll back
// only this call through a local savepoint. If savepoint cleanup itself fails,
// the caller transaction is invalidated so uncertain intent cannot be committed.
// created is false only for an exact semantic replay.
func (r *Repository) AuthorTransition(ctx context.Context, tx *sql.Tx, request AuthorRequest) (transition Transition, created bool, err error) {
	if tx == nil || request.ApplicationID == "" || request.JobID == "" || request.ActorID == "" || request.ExpectedRevision < 0 {
		return Transition{}, false, ErrInvalidRequest
	}
	target, err := targetState(request.Action)
	if err != nil {
		return Transition{}, false, err
	}
	existing, err := transitionByOperation(ctx, tx, request.JobID, request.Action)
	switch {
	case err == nil:
		if transitionMatches(existing, request, target) {
			return existing, false, nil
		}
		return Transition{}, false, ErrOperationConflict
	case !errors.Is(err, sql.ErrNoRows):
		return Transition{}, false, err
	}
	if err := validateJob(ctx, tx, request); err != nil {
		return Transition{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT application_desired_lifecycle_author`); err != nil {
		return Transition{}, false, err
	}

	createdAt := r.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO application_desired_lifecycle_transitions(
			app_id, revision_number, expected_revision, previous_state,
			desired_state, job_id, action, actor_id, created_at
		)
		SELECT h.app_id, h.revision_number + 1, h.revision_number, h.desired_state,
		       ?, ?, ?, ?, ?
		FROM application_desired_lifecycle_heads h
		JOIN jobs j ON j.id = ?
		WHERE h.app_id = ?
		  AND h.revision_number = ?
		  AND j.type = ?
		  AND j.resource_type = 'application'
		  AND j.resource_id = h.app_id
		  AND j.requested_by = ?`,
		string(target), request.JobID, string(request.Action), request.ActorID, createdAt,
		request.JobID, request.ApplicationID, request.ExpectedRevision, string(request.Action), request.ActorID)
	if err != nil {
		return Transition{}, false, failAuthorSavepoint(tx, fmt.Errorf("append application desired lifecycle transition: %w", err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Transition{}, false, failAuthorSavepoint(tx, err)
	}
	if rows != 1 {
		if cleanupErr := rollbackAuthorSavepoint(tx); cleanupErr != nil {
			return Transition{}, false, cleanupErr
		}
		var revision int64
		err = tx.QueryRowContext(ctx, `SELECT revision_number FROM application_desired_lifecycle_heads WHERE app_id=?`, request.ApplicationID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return Transition{}, false, ErrApplicationNotFound
		}
		if err != nil {
			return Transition{}, false, err
		}
		return Transition{}, false, ErrRevisionConflict
	}
	transition, err = transitionByOperation(ctx, tx, request.JobID, request.Action)
	if err != nil {
		return Transition{}, false, failAuthorSavepoint(tx, err)
	}
	if err := releaseAuthorSavepoint(tx); err != nil {
		return Transition{}, false, err
	}
	return transition, true, nil
}

func failAuthorSavepoint(tx *sql.Tx, cause error) error {
	if cleanupErr := rollbackAuthorSavepoint(tx); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func rollbackAuthorSavepoint(tx *sql.Tx) error {
	rollbackContext, cancelRollback := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, err := tx.ExecContext(rollbackContext, `ROLLBACK TO SAVEPOINT application_desired_lifecycle_author`)
	cancelRollback()
	if err != nil {
		return invalidateAuthorTransaction(tx, fmt.Errorf("roll back application desired lifecycle savepoint: %w", err))
	}
	releaseContext, cancelRelease := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, err = tx.ExecContext(releaseContext, `RELEASE SAVEPOINT application_desired_lifecycle_author`)
	cancelRelease()
	if err != nil {
		return invalidateAuthorTransaction(tx, fmt.Errorf("release rolled-back application desired lifecycle savepoint: %w", err))
	}
	return nil
}

func releaseAuthorSavepoint(tx *sql.Tx) error {
	releaseContext, cancelRelease := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, err := tx.ExecContext(releaseContext, `RELEASE SAVEPOINT application_desired_lifecycle_author`)
	cancelRelease()
	if err != nil {
		return invalidateAuthorTransaction(tx, fmt.Errorf("release application desired lifecycle savepoint: %w", err))
	}
	return nil
}

func invalidateAuthorTransaction(tx *sql.Tx, cause error) error {
	// database/sql has no context-aware Tx.Rollback. If the driver wedges here,
	// this last-resort invalidation cannot promise a strict wall-clock bound. A
	// goroutine would only hide that limit while leaking work and would not make
	// the transaction safe for its caller to commit.
	rollbackErr := tx.Rollback()
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return errors.Join(cause, fmt.Errorf("invalidate caller transaction: %w", rollbackErr))
	}
	return fmt.Errorf("caller transaction invalidated after lifecycle savepoint cleanup failure: %w", cause)
}

func targetState(action Action) (State, error) {
	switch action {
	case Start, Restart:
		return Running, nil
	case Stop:
		return Stopped, nil
	default:
		return "", ErrInvalidAction
	}
}

func validateJob(ctx context.Context, tx *sql.Tx, request AuthorRequest) error {
	var action, resourceType, resourceID string
	var actor sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT type, resource_type, resource_id, requested_by
		FROM jobs WHERE id = ?`, request.JobID).Scan(&action, &resourceType, &resourceID, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrJobNotFound
	}
	if err != nil {
		return err
	}
	if action != string(request.Action) || resourceType != "application" || resourceID != request.ApplicationID || !actor.Valid || actor.String != request.ActorID {
		return ErrJobMismatch
	}
	return nil
}

func transitionMatches(transition Transition, request AuthorRequest, target State) bool {
	return transition.ApplicationID == request.ApplicationID &&
		transition.JobID == request.JobID && transition.Action == request.Action &&
		transition.ActorID == request.ActorID && transition.ExpectedRevision == request.ExpectedRevision &&
		transition.DesiredState == target
}

func transitionByOperation(ctx context.Context, tx *sql.Tx, jobID string, action Action) (Transition, error) {
	return scanTransition(tx.QueryRowContext(ctx, `
		SELECT app_id, revision_number, expected_revision, previous_state,
		       desired_state, job_id, action, actor_id, created_at
		FROM application_desired_lifecycle_transitions
		WHERE job_id = ? AND action = ?`, jobID, string(action)))
}

type rowScanner interface {
	Scan(...any) error
}

func scanHead(row rowScanner) (Head, error) {
	var result Head
	var desiredState, updatedAt sql.NullString
	if err := row.Scan(&result.ApplicationID, &desiredState, &result.Revision, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Head{}, ErrApplicationNotFound
		}
		return Head{}, err
	}
	if desiredState.Valid {
		state := State(desiredState.String)
		result.DesiredState = &state
	}
	if updatedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, updatedAt.String)
		if err != nil {
			return Head{}, fmt.Errorf("parse application desired lifecycle head timestamp: %w", err)
		}
		result.UpdatedAt = &parsed
	}
	return result, nil
}

func scanTransition(row rowScanner) (Transition, error) {
	var result Transition
	var previousState sql.NullString
	var desiredState, action, createdAt string
	if err := row.Scan(&result.ApplicationID, &result.Revision, &result.ExpectedRevision,
		&previousState, &desiredState, &result.JobID, &action, &result.ActorID, &createdAt); err != nil {
		return Transition{}, err
	}
	if previousState.Valid {
		state := State(previousState.String)
		result.PreviousState = &state
	}
	result.DesiredState = State(desiredState)
	result.Action = Action(action)
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Transition{}, fmt.Errorf("parse application desired lifecycle transition timestamp: %w", err)
	}
	result.CreatedAt = parsed
	return result, nil
}
