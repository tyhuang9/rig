package deploymentbinding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type Repository struct {
	db        *sql.DB
	validator LifecycleTxValidator
}

func New(db *sql.DB, validator LifecycleTxValidator) (*Repository, error) {
	if db == nil || nilInterface(validator) || !validator.ConfiguredFor(db) {
		return nil, ErrInvalidConfiguration
	}
	return &Repository{db: db, validator: validator}, nil
}

func (r *Repository) UsesDatabase(db *sql.DB) bool {
	return r != nil && r.db != nil && r.db == db && !nilInterface(r.validator) && r.validator.ConfiguredFor(db)
}

func (r *Repository) Read(ctx context.Context, jobID string) (Binding, error) {
	if err := r.configured(); err != nil {
		return Binding{}, err
	}
	return r.readTransaction(ctx, func(tx *sql.Tx) (Binding, error) {
		return r.ReadTx(ctx, tx, jobID)
	})
}

// ReadTx returns historical immutable identity without requiring that it still
// match the mutable desired head. Running bindings always validate their full
// retained canonical authorizing graph first.
func (r *Repository) ReadTx(ctx context.Context, tx *sql.Tx, jobID string) (Binding, error) {
	if err := r.transactionReady(ctx, tx); err != nil {
		return Binding{}, err
	}
	if jobID == "" {
		return Binding{}, ErrBindingNotFound
	}
	var binding Binding
	var mode, authorizer, createdAt string
	var jobType, resourceType, resourceID string
	err := tx.QueryRowContext(ctx, `SELECT
		b.job_id,b.app_id,b.desired_revision,b.mode,
		COALESCE(b.authorizing_lifecycle_job_id,''),b.created_at,
		j.type,j.resource_type,j.resource_id
		FROM application_deployment_lifecycle_bindings b
		JOIN jobs j ON j.id=b.job_id
		WHERE b.job_id=?`, jobID).Scan(
		&binding.jobID, &binding.applicationID, &binding.desiredRevision, &mode,
		&authorizer, &createdAt, &jobType, &resourceType, &resourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, ErrBindingNotFound
	}
	if err != nil {
		return Binding{}, storageError(ctx, err)
	}
	binding.repository = r
	binding.mode = Mode(mode)
	binding.authorizingLifecycleJobID = authorizer
	binding.createdAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || jobType != "deploy" || resourceType != "application" || resourceID != binding.applicationID || !binding.validFor(r) {
		return Binding{}, ErrInvalidBinding
	}
	if binding.mode == ModeRunning {
		ref := OperationRef{ApplicationID: binding.applicationID, OperationID: binding.authorizingLifecycleJobID, DesiredRevision: binding.desiredRevision}
		if err := r.validator.HistoricalBindingTx(ctx, tx, ref); err != nil {
			return Binding{}, validatorError(ctx, err)
		}
	}
	return binding, nil
}

// InsertNewTx derives authority from the current head and inserts exactly one
// new binding inside the caller's deployment-creation transaction. There is no
// runtime adoption or read-time backfill path.
func (r *Repository) InsertNewTx(ctx context.Context, tx *sql.Tx, jobID string, now time.Time) (Binding, error) {
	if err := r.transactionReady(ctx, tx); err != nil {
		return Binding{}, err
	}
	if jobID == "" || now.IsZero() {
		return Binding{}, ErrInvalidBinding
	}
	var appID string
	err := tx.QueryRowContext(ctx, `SELECT resource_id FROM jobs
		WHERE id=? AND type='deploy' AND resource_type='application'
		  AND status='queued' AND phase='queued' AND attempt=0
		  AND started_at IS NULL AND finished_at IS NULL`, jobID).Scan(&appID)
	if errors.Is(err, sql.ErrNoRows) || appID == "" {
		return Binding{}, ErrInvalidBinding
	}
	if err != nil {
		return Binding{}, storageError(ctx, err)
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM application_deployment_lifecycle_bindings WHERE job_id=?)`, jobID).Scan(&existing); err != nil {
		return Binding{}, storageError(ctx, err)
	}
	if existing != 0 {
		return Binding{}, ErrInvalidBinding
	}

	head, found, err := currentHeadTx(ctx, tx, appID)
	if err != nil {
		return Binding{}, err
	}
	if !found || !head.valid() {
		return Binding{}, ErrInvalidBinding
	}
	binding := Binding{repository: r, jobID: jobID, applicationID: appID, desiredRevision: head.revision, createdAt: now.UTC()}
	switch {
	case head.revision == 0:
		invalidHistory, historyErr := legacyHistoryExistsTx(ctx, tx, appID)
		if historyErr != nil {
			return Binding{}, historyErr
		}
		if invalidHistory {
			return Binding{}, ErrInvalidBinding
		}
		binding.mode = ModeLegacyUninitialized
	case head.desiredState == "running":
		if head.operationID == "" {
			return Binding{}, ErrInvalidBinding
		}
		ref := OperationRef{ApplicationID: appID, OperationID: head.operationID, DesiredRevision: head.revision}
		completion, validationErr := r.validator.CurrentOperationTx(ctx, tx, ref)
		if validationErr != nil {
			return Binding{}, validatorError(ctx, validationErr)
		}
		if completion == CompletionIncomplete {
			return Binding{}, ErrLifecycleUnresolved
		}
		if completion != CompletionComplete {
			return Binding{}, ErrInvalidConfiguration
		}
		unresolved, validationErr := r.validator.UnresolvedHistoryTx(ctx, tx, appID)
		if validationErr != nil {
			return Binding{}, validatorError(ctx, validationErr)
		}
		if unresolved {
			return Binding{}, ErrLifecycleUnresolved
		}
		binding.mode = ModeRunning
		binding.authorizingLifecycleJobID = head.operationID
	default:
		return Binding{}, ErrInvalidBinding
	}
	if !binding.validFor(r) {
		return Binding{}, ErrInvalidBinding
	}
	createdAt := binding.createdAt.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_deployment_lifecycle_bindings(
		job_id,app_id,desired_revision,mode,authorizing_lifecycle_job_id,created_at
	) VALUES(?,?,?,?,NULLIF(?,''),?)`, binding.jobID, binding.applicationID, binding.desiredRevision,
		string(binding.mode), binding.authorizingLifecycleJobID, createdAt)
	if err != nil {
		if contextError(ctx, err) != nil {
			return Binding{}, contextError(ctx, err)
		}
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "constraint") || strings.Contains(message, "invalid application deployment lifecycle binding") {
			return Binding{}, fmt.Errorf("%w: insert rejected", ErrInvalidBinding)
		}
		return Binding{}, storageError(ctx, err)
	}
	return binding, nil
}

func (r *Repository) Check(ctx context.Context, binding Binding) (Classification, error) {
	if err := r.configured(); err != nil {
		return 0, err
	}
	var classification Classification
	_, err := r.readTransaction(ctx, func(tx *sql.Tx) (Binding, error) {
		value, checkErr := r.CheckTx(ctx, tx, binding)
		classification = value
		return Binding{}, checkErr
	})
	return classification, err
}

// CheckTx validates historical authority before inspecting mutable current
// state. A later legitimate operation, including an unfinished one, makes the
// old binding stale only after its canonical identity has been validated.
func (r *Repository) CheckTx(ctx context.Context, tx *sql.Tx, binding Binding) (Classification, error) {
	if err := r.transactionReady(ctx, tx); err != nil {
		return 0, err
	}
	if !binding.validFor(r) {
		return 0, ErrInvalidBinding
	}
	persisted, err := r.ReadTx(ctx, tx, binding.jobID)
	if err != nil {
		return 0, err
	}
	if !persisted.Equal(binding) {
		return 0, ErrInvalidBinding
	}
	head, found, err := currentHeadTx(ctx, tx, binding.applicationID)
	if err != nil {
		return 0, err
	}
	if !found {
		return ClassificationStale, nil
	}
	if !head.valid() {
		return 0, ErrInvalidBinding
	}
	if head.revision == 0 {
		invalidHistory, historyErr := legacyHistoryExistsTx(ctx, tx, binding.applicationID)
		if historyErr != nil {
			return 0, historyErr
		}
		if invalidHistory || binding.mode != ModeLegacyUninitialized {
			return 0, ErrInvalidBinding
		}
		return ClassificationCurrent, nil
	}
	if head.operationID == "" {
		return 0, ErrInvalidBinding
	}
	ref := OperationRef{ApplicationID: binding.applicationID, OperationID: head.operationID, DesiredRevision: head.revision}
	completion, validationErr := r.validator.CurrentOperationTx(ctx, tx, ref)
	if validationErr != nil {
		return 0, validatorError(ctx, validationErr)
	}
	if completion != CompletionComplete && completion != CompletionIncomplete {
		return 0, ErrInvalidConfiguration
	}
	if binding.mode == ModeLegacyUninitialized || head.revision > binding.desiredRevision {
		return ClassificationStale, nil
	}
	if head.revision < binding.desiredRevision || head.desiredState != "running" ||
		head.operationID != binding.authorizingLifecycleJobID {
		return 0, ErrInvalidBinding
	}
	if completion == CompletionIncomplete {
		return 0, ErrLifecycleUnresolved
	}
	unresolved, validationErr := r.validator.UnresolvedHistoryTx(ctx, tx, binding.applicationID)
	if validationErr != nil {
		return 0, validatorError(ctx, validationErr)
	}
	if unresolved {
		return 0, ErrLifecycleUnresolved
	}
	return ClassificationCurrent, nil
}

type currentHead struct {
	desiredState string
	revision     int64
	updatedAt    string
	operationID  string
	headPresent  bool
}

func (h currentHead) valid() bool {
	if !h.headPresent {
		return false
	}
	if h.revision == 0 {
		return h.desiredState == "" && h.updatedAt == "" && h.operationID == ""
	}
	if h.revision < 0 || (h.desiredState != "running" && h.desiredState != "stopped") || h.updatedAt == "" || h.operationID == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, h.updatedAt)
	return err == nil
}

func currentHeadTx(ctx context.Context, tx *sql.Tx, appID string) (currentHead, bool, error) {
	var head currentHead
	var headID, desiredState, updatedAt, operationID sql.NullString
	var revision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT h.app_id,h.desired_state,h.revision_number,h.updated_at,o.job_id
		FROM applications a
		LEFT JOIN application_desired_lifecycle_heads h ON h.app_id=a.id
		LEFT JOIN application_lifecycle_operations o
		  ON o.app_id=h.app_id AND o.desired_revision=h.revision_number
		WHERE a.id=?`, appID).Scan(&headID, &desiredState, &revision, &updatedAt, &operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return currentHead{}, false, nil
	}
	if err != nil {
		return currentHead{}, false, storageError(ctx, err)
	}
	head.headPresent = headID.Valid && revision.Valid
	head.desiredState = desiredState.String
	head.revision = revision.Int64
	head.updatedAt = updatedAt.String
	head.operationID = operationID.String
	return head, true, nil
}

func legacyHistoryExistsTx(ctx context.Context, tx *sql.Tx, appID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM application_desired_lifecycle_transitions WHERE app_id=?
		UNION ALL
		SELECT 1 FROM application_lifecycle_operations WHERE app_id=?
	)`, appID, appID).Scan(&exists)
	if err != nil {
		return false, storageError(ctx, err)
	}
	return exists != 0, nil
}

func (r *Repository) readTransaction(ctx context.Context, read func(*sql.Tx) (Binding, error)) (Binding, error) {
	if err := contextError(ctx, nil); err != nil {
		return Binding{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Binding{}, storageError(ctx, err)
	}
	value, err := read(tx)
	if err != nil {
		_ = tx.Rollback()
		return Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, storageError(ctx, err)
	}
	return value, nil
}

func (r *Repository) configured() error {
	if r == nil || r.db == nil || nilInterface(r.validator) || !r.validator.ConfiguredFor(r.db) {
		return ErrInvalidConfiguration
	}
	return nil
}

func (r *Repository) transactionReady(ctx context.Context, tx *sql.Tx) error {
	if err := r.configured(); err != nil {
		return err
	}
	if tx == nil {
		return ErrInvalidConfiguration
	}
	return contextError(ctx, nil)
}

func validatorError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if canceled := contextError(ctx, err); canceled != nil {
		return canceled
	}
	for _, known := range []error{ErrInvalidBinding, ErrLifecycleUnresolved, ErrUnavailable, ErrInvalidConfiguration, ErrBindingNotFound, ErrCanceled} {
		if errors.Is(err, known) {
			return err
		}
	}
	return fmt.Errorf("%w: validator returned an unclassified error", ErrInvalidConfiguration)
}

func storageError(ctx context.Context, err error) error {
	if canceled := contextError(ctx, err); canceled != nil {
		return canceled
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func contextError(ctx context.Context, err error) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrCanceled, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrCanceled, err)
	}
	return nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
