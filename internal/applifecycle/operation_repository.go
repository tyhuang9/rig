package applifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hostd/hostd/internal/jobs"
)

const operationSelectColumns = `job_id,app_id,desired_revision,expected_revision,action,actor_id,
 idempotency_key,pins_json,pins_digest,created_at`

// Accept is an internal storage admission helper. Its pins are trusted input
// from a caller already holding the required effects/gateway exclusion after
// physical validation. It neither acquires those locks nor grants durable
// permission for an effect. Immutable historical replay is resolved before
// any mutable job or current-pin work.
func (r *Repository) Accept(ctx context.Context, service *jobs.Service, request LifecycleRequest, pins GeneratedLifecyclePinsV1) (AcceptedOperation, error) {
	if service == nil || validateLifecycleRequest(request) != nil {
		return AcceptedOperation{}, ErrInvalidOperation
	}
	existing, err := r.LookupAccepted(ctx, request)
	if err == nil {
		return AcceptedOperation{Operation: existing}, nil
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return AcceptedOperation{}, err
	}

	var accepted Operation
	result, err := service.CreateWithInputTransaction(ctx, jobs.CreateRequest{
		Type: string(request.Action), ResourceType: "application", ResourceID: request.ApplicationID,
		IdempotencyKey: request.IdempotencyKey, RequestedBy: request.ActorID, Input: jobs.NoInput{},
	}, jobs.CreateTransactionHooks{
		BeforeCreate: func(ctx context.Context, tx *sql.Tx) (bool, error) {
			operation, lookupErr := lookupAcceptedQuery(ctx, tx, request)
			if lookupErr == nil {
				accepted = operation
				return true, nil
			}
			if !errors.Is(lookupErr, ErrOperationNotFound) {
				return false, lookupErr
			}
			exists, lookupErr := jobIdentityExists(ctx, tx, request)
			if lookupErr != nil {
				return false, lookupErr
			}
			if exists {
				return false, ErrMissingOperation
			}
			return false, nil
		},
		Existing: func(ctx context.Context, tx *sql.Tx, _ jobs.Job) (bool, error) {
			operation, lookupErr := lookupAcceptedQuery(ctx, tx, request)
			if lookupErr == nil {
				accepted = operation
				return true, nil
			}
			if errors.Is(lookupErr, ErrOperationNotFound) {
				return false, ErrMissingOperation
			}
			return false, lookupErr
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, job jobs.Job) error {
			operation, created, acceptErr := r.AcceptOperationTx(ctx, tx, job, request, pins)
			if acceptErr != nil {
				return acceptErr
			}
			if !created {
				return ErrOperationConflict
			}
			accepted = operation
			return nil
		},
	})
	if err != nil {
		return AcceptedOperation{}, err
	}
	if result.Handled {
		return AcceptedOperation{Operation: accepted}, nil
	}
	if !result.Created || accepted.JobID == "" {
		return AcceptedOperation{}, ErrMissingOperation
	}
	return AcceptedOperation{Operation: accepted, Created: true}, nil
}

func (r *Repository) LookupAccepted(ctx context.Context, request LifecycleRequest) (Operation, error) {
	if validateLifecycleRequest(request) != nil {
		return Operation{}, ErrInvalidOperation
	}
	return lookupAcceptedQuery(ctx, r.db, request)
}

func (r *Repository) Operation(ctx context.Context, jobID string) (Operation, error) {
	if jobID == "" {
		return Operation{}, ErrOperationNotFound
	}
	operation, err := scanOperation(r.db.QueryRowContext(ctx, `SELECT `+operationSelectColumns+`
		FROM application_lifecycle_operations WHERE job_id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	return operation, err
}

// AcceptOperationTx appends desired intent and immutable pins inside a
// caller-owned transaction. The enclosing savepoint prevents caught errors
// from leaving either part pending.
func (r *Repository) AcceptOperationTx(ctx context.Context, tx *sql.Tx, job jobs.Job, request LifecycleRequest, pins GeneratedLifecyclePinsV1) (Operation, bool, error) {
	if tx == nil || validateLifecycleRequest(request) != nil || job.ID == "" {
		return Operation{}, false, ErrInvalidOperation
	}
	existing, err := operationByJob(ctx, tx, job.ID)
	if err == nil {
		if operationMatchesRequest(existing, request) {
			return existing, false, nil
		}
		return Operation{}, false, ErrOperationConflict
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return Operation{}, false, err
	}
	if job.Type != string(request.Action) || job.ResourceType != "application" || job.ResourceID != request.ApplicationID ||
		job.RequestedBy != request.ActorID {
		return Operation{}, false, ErrInvalidOperation
	}
	encoded, digest, err := canonicalPins(pins)
	if err != nil {
		return Operation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT application_lifecycle_accept`); err != nil {
		return Operation{}, false, err
	}
	fail := func(cause error) (Operation, bool, error) {
		return Operation{}, false, failOperationSavepoint(tx, cause)
	}
	for index, name := range sortedComponentNames(pins) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_lifecycle_operation_components(job_id,ordinal,component_name) VALUES(?,?,?)`, job.ID, index, name); err != nil {
			return fail(fmt.Errorf("retain lifecycle component: %w", err))
		}
	}
	transition, created, err := r.AuthorTransition(ctx, tx, AuthorRequest{
		ApplicationID: request.ApplicationID, JobID: job.ID, Action: request.Action,
		ActorID: request.ActorID, ExpectedRevision: request.ExpectedDesiredRevision,
	})
	if err != nil {
		return fail(err)
	}
	if !created || transition.Revision != request.ExpectedDesiredRevision+1 {
		return fail(ErrOperationConflict)
	}
	createdAt := r.now().UTC().Format(time.RFC3339Nano)
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_lifecycle_operations(
		job_id,app_id,desired_revision,expected_revision,action,actor_id,idempotency_key,
		version,runtime_strategy,baseline_disposition,deployment_id,active_generation,release_id,slot,
		plan_revision_id,plan_revision_number,configuration_revision_id,configuration_revision_number,
		configuration_format,pins_json,pins_digest,created_at
	) VALUES(?,?,?,?,?,?,?,1,'generated_node',?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		job.ID, request.ApplicationID, transition.Revision, request.ExpectedDesiredRevision,
		string(request.Action), request.ActorID, request.IdempotencyKey, string(pins.BaselineDisposition),
		pins.DeploymentID, pins.ActiveGeneration, pins.ReleaseID, pins.Slot,
		pins.PlanRevision.ID, pins.PlanRevision.Number, pins.Configuration.RevisionID,
		pins.Configuration.RevisionNumber, pins.Configuration.FormatVersion, string(encoded), digest, createdAt)
	if err != nil {
		return fail(fmt.Errorf("retain application lifecycle operation: %w", err))
	}
	operation, err := operationByJob(ctx, tx, job.ID)
	if err != nil {
		return fail(err)
	}
	if err := releaseOperationSavepoint(tx); err != nil {
		return Operation{}, false, err
	}
	return operation, true, nil
}

func lookupAcceptedQuery(ctx context.Context, query queryRower, request LifecycleRequest) (Operation, error) {
	operation, err := scanOperation(query.QueryRowContext(ctx, `SELECT `+operationSelectColumns+`
		FROM application_lifecycle_operations WHERE app_id=? AND action=? AND idempotency_key=?`,
		request.ApplicationID, string(request.Action), request.IdempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	if !operationMatchesRequest(operation, request) {
		return Operation{}, ErrOperationConflict
	}
	return operation, nil
}

func operationByJob(ctx context.Context, query queryRower, jobID string) (Operation, error) {
	operation, err := scanOperation(query.QueryRowContext(ctx, `SELECT `+operationSelectColumns+`
		FROM application_lifecycle_operations WHERE job_id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	return operation, err
}

func scanOperation(row rowScanner) (Operation, error) {
	var operation Operation
	var action, pinsJSON, createdAt string
	if err := row.Scan(&operation.JobID, &operation.ApplicationID, &operation.DesiredRevision,
		&operation.ExpectedRevision, &action, &operation.ActorID, &operation.IdempotencyKey,
		&pinsJSON, &operation.PinsDigest, &createdAt); err != nil {
		return Operation{}, err
	}
	operation.Action = Action(action)
	if err := decodeCanonical([]byte(pinsJSON), &operation.Pins, func() ([]byte, string, error) {
		return canonicalPins(operation.Pins)
	}, operation.PinsDigest); err != nil {
		return Operation{}, wrapInvalid("decode lifecycle pins", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Operation{}, fmt.Errorf("parse lifecycle operation timestamp: %w", err)
	}
	operation.CreatedAt = parsed
	return operation, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func jobIdentityExists(ctx context.Context, query queryRower, request LifecycleRequest) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs
		WHERE type=? AND resource_type='application' AND resource_id=? AND idempotency_key=?)`,
		string(request.Action), request.ApplicationID, request.IdempotencyKey).Scan(&exists)
	return exists != 0, err
}

func failOperationSavepoint(tx *sql.Tx, cause error) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, rollbackErr := tx.ExecContext(cleanupContext, `ROLLBACK TO SAVEPOINT application_lifecycle_accept`)
	cancel()
	if rollbackErr != nil {
		return errors.Join(cause, invalidateAuthorTransaction(tx, rollbackErr))
	}
	cleanupContext, cancel = context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, releaseErr := tx.ExecContext(cleanupContext, `RELEASE SAVEPOINT application_lifecycle_accept`)
	cancel()
	if releaseErr != nil {
		return errors.Join(cause, invalidateAuthorTransaction(tx, releaseErr))
	}
	return cause
}

func releaseOperationSavepoint(tx *sql.Tx) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, err := tx.ExecContext(cleanupContext, `RELEASE SAVEPOINT application_lifecycle_accept`)
	cancel()
	if err != nil {
		return invalidateAuthorTransaction(tx, err)
	}
	return nil
}
