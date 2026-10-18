package applifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hostd/hostd/internal/jobs"
)

// WorkerClaim is created only from a running persisted worker snapshot. Every
// new journal write revalidates the exact job, attempt, status, operation and
// desired head in SQL.
type WorkerClaim struct {
	jobID   string
	attempt int
}

func WorkerClaimFor(job jobs.Job) (WorkerClaim, error) {
	if job.ID == "" || job.Attempt < 1 || job.Status != string(jobs.Running) {
		return WorkerClaim{}, ErrEffectNotAuthorized
	}
	return WorkerClaim{jobID: job.ID, attempt: job.Attempt}, nil
}

func (c WorkerClaim) JobID() string { return c.jobID }
func (c WorkerClaim) Attempt() int  { return c.attempt }

// BeginBoundaryTx retains a finite pending boundary. A Created=false replay is
// historical data and is never permission to issue the effect.
func (r *Repository) BeginBoundaryTx(ctx context.Context, tx *sql.Tx, claim WorkerClaim, operation Operation, request EffectRequestV1) (Intent, bool, error) {
	if tx == nil {
		return Intent{}, false, ErrInvalidEffect
	}
	encoded, digest, err := canonicalEffectRequest(request)
	if err != nil {
		return Intent{}, false, err
	}
	if !effectRequestMatchesOperation(request, operation) {
		return Intent{}, false, ErrInvalidEffect
	}
	existing, err := intentByKey(ctx, tx, operation.JobID, request.Boundary, request.ComponentName)
	if err == nil {
		if existing.RequestDigest == digest {
			return existing, false, nil
		}
		return Intent{}, false, ErrEffectConflict
	}
	if !errors.Is(err, ErrEffectNotFound) {
		return Intent{}, false, err
	}
	if claim.jobID != operation.JobID || claim.attempt < 1 {
		return Intent{}, false, ErrEffectNotAuthorized
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT application_lifecycle_journal`); err != nil {
		return Intent{}, false, err
	}
	createdAt := r.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_lifecycle_effect_intents(
		job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
	) VALUES(?,?,?,?,1,?,?,?)`, operation.JobID, string(request.Boundary), request.ComponentName,
		claim.attempt, string(encoded), digest, createdAt)
	if err != nil {
		return Intent{}, false, failJournalSavepoint(tx, fmt.Errorf("retain lifecycle effect intent: %w", err))
	}
	intent, err := intentByKey(ctx, tx, operation.JobID, request.Boundary, request.ComponentName)
	if err != nil {
		return Intent{}, false, failJournalSavepoint(tx, err)
	}
	if err := releaseJournalSavepoint(tx); err != nil {
		return Intent{}, false, err
	}
	return intent, true, nil
}

// ObserveBoundaryTx retains a verified observation. Identical historical proof
// replay precedes mutable worker/head checks and grants no effect permission.
func (r *Repository) ObserveBoundaryTx(ctx context.Context, tx *sql.Tx, claim WorkerClaim, intent Intent, proof EffectProofV1) (Receipt, bool, error) {
	if tx == nil {
		return Receipt{}, false, ErrInvalidEffect
	}
	encoded, digest, err := canonicalEffectProof(proof)
	if err != nil {
		return Receipt{}, false, err
	}
	if proof.OperationID != intent.JobID || proof.Boundary != intent.Key.Boundary ||
		proof.ComponentName != intent.Key.ComponentName || proof.IntentDigest != intent.RequestDigest {
		return Receipt{}, false, ErrInvalidEffect
	}
	existing, err := receiptByKey(ctx, tx, intent.JobID, intent.Key.Boundary, intent.Key.ComponentName)
	if err == nil {
		if existing.ProofDigest == digest {
			return existing, false, nil
		}
		return Receipt{}, false, ErrEffectConflict
	}
	if !errors.Is(err, ErrEffectNotFound) {
		return Receipt{}, false, err
	}
	if claim.jobID != intent.JobID || claim.attempt < 1 {
		return Receipt{}, false, ErrEffectNotAuthorized
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT application_lifecycle_journal`); err != nil {
		return Receipt{}, false, err
	}
	observedAt := r.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_lifecycle_effect_receipts(
		job_id,boundary,component_name,writer_attempt,outcome,proof_version,proof_json,proof_digest,observed_at
	) VALUES(?,?,?,?,?,1,?,?,?)`, intent.JobID, string(intent.Key.Boundary), intent.Key.ComponentName,
		claim.attempt, string(proof.Outcome), string(encoded), digest, observedAt)
	if err != nil {
		return Receipt{}, false, failJournalSavepoint(tx, fmt.Errorf("retain lifecycle effect receipt: %w", err))
	}
	receipt, err := receiptByKey(ctx, tx, intent.JobID, intent.Key.Boundary, intent.Key.ComponentName)
	if err != nil {
		return Receipt{}, false, failJournalSavepoint(tx, err)
	}
	if err := releaseJournalSavepoint(tx); err != nil {
		return Receipt{}, false, err
	}
	return receipt, true, nil
}

func (r *Repository) CompleteTx(ctx context.Context, tx *sql.Tx, claim WorkerClaim, intent Intent, proof EffectProofV1) (Receipt, bool, error) {
	if intent.Key.Boundary != BoundaryComplete || proof.Boundary != BoundaryComplete {
		return Receipt{}, false, ErrInvalidEffect
	}
	return r.ObserveBoundaryTx(ctx, tx, claim, intent, proof)
}

// AuthorizePending is a controller authority read. The caller must hold the
// shared effects exclusion and revalidate immediately before issuing the
// command; this disconnected read does not grant durable effect permission.
func (r *Repository) AuthorizePending(ctx context.Context, claim WorkerClaim, intent Intent) (PendingAuthority, error) {
	if claim.jobID == "" || claim.attempt < 1 || intent.JobID != claim.jobID {
		return PendingAuthority{}, ErrEffectNotAuthorized
	}
	var digest string
	err := r.db.QueryRowContext(ctx, `SELECT i.request_digest
		FROM application_lifecycle_effect_intents i
		JOIN application_lifecycle_operations o ON o.job_id=i.job_id
		JOIN jobs j ON j.id=o.job_id
		JOIN application_desired_lifecycle_heads h ON h.app_id=o.app_id AND h.revision_number=o.desired_revision
		WHERE i.job_id=? AND i.boundary=? AND i.component_name=? AND i.request_digest=?
		  AND j.status='running' AND j.attempt=? AND j.type=o.action
		  AND j.resource_type='application' AND j.resource_id=o.app_id AND j.requested_by=o.actor_id
		  AND NOT EXISTS (SELECT 1 FROM application_lifecycle_effect_receipts r
		                  WHERE r.job_id=i.job_id AND r.boundary=i.boundary AND r.component_name=i.component_name)`,
		intent.JobID, string(intent.Key.Boundary), intent.Key.ComponentName, intent.RequestDigest, claim.attempt).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingAuthority{}, ErrEffectNotAuthorized
	}
	if err != nil {
		return PendingAuthority{}, err
	}
	operation, err := r.Operation(ctx, intent.JobID)
	if err != nil {
		return PendingAuthority{}, err
	}
	persisted, err := intentByKey(ctx, r.db, intent.JobID, intent.Key.Boundary, intent.Key.ComponentName)
	if err != nil {
		return PendingAuthority{}, err
	}
	if persisted.RequestDigest != digest || !sameIntentIdentity(persisted, intent) {
		return PendingAuthority{}, ErrEffectNotAuthorized
	}
	spec, ok := effectSpecFor(operation, persisted.Key)
	if !ok || validateIntentForOperation(operation, persisted, spec) != nil {
		return PendingAuthority{}, ErrEffectNotAuthorized
	}
	return PendingAuthority{claim: claim, intentDigest: digest}, nil
}

// PendingStartExists is a controller admission fence read. Its bounded keyset
// scan uses multiple database reads, so callers must hold the shared effects
// exclusion when using the result to admit effects.
func (r *Repository) PendingStartExists(ctx context.Context) (bool, error) {
	return pendingStartExistsQuery(ctx, r.db)
}

func pendingStartExistsQuery(ctx context.Context, query lifecycleQuery) (bool, error) {
	found := false
	err := scanStartIntentsQuery(ctx, query, func(_ Operation, _ Intent, receipt *Receipt) (bool, error) {
		if receipt != nil {
			return false, nil
		}
		found = true
		return true, nil
	})
	if err != nil {
		return true, err
	}
	return found, nil
}

// scanStartIntents loads and validates every retained start intent and its
// optional receipt. The scan is not an atomic authorization by itself;
// callers hold effects exclusion and separately authorize exact pending
// intents before issuing an effect.
func (r *Repository) scanStartIntents(ctx context.Context, visit func(Operation, Intent, *Receipt) (bool, error)) error {
	return scanStartIntentsQuery(ctx, r.db, visit)
}

func scanStartIntentsQuery(ctx context.Context, query lifecycleQuery, visit func(Operation, Intent, *Receipt) (bool, error)) error {
	if query == nil {
		return ErrInvalidEffect
	}
	if visit == nil {
		return ErrInvalidEffect
	}
	var afterJob, afterComponent string
	for {
		rows, err := query.QueryContext(ctx, `SELECT job_id,component_name
			FROM application_lifecycle_effect_intents
			WHERE boundary='start' AND (job_id>? OR (job_id=? AND component_name>?))
			ORDER BY job_id,component_name LIMIT 32`, afterJob, afterJob, afterComponent)
		if err != nil {
			return err
		}
		type key struct{ jobID, component string }
		batch := make([]key, 0, 32)
		for rows.Next() {
			var value key
			if err := rows.Scan(&value.jobID, &value.component); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, value)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, value := range batch {
			operation, loadErr := operationByJob(ctx, query, value.jobID)
			if loadErr != nil {
				return loadErr
			}
			intent, loadErr := intentByKey(ctx, query, value.jobID, BoundaryStart, value.component)
			if loadErr != nil {
				return loadErr
			}
			spec, ok := effectSpecFor(operation, intent.Key)
			if !ok || spec.outcome != OutcomeContainerRunning || validateIntentForOperation(operation, intent, spec) != nil {
				return ErrInvalidEffect
			}
			receipt, loadErr := receiptByKey(ctx, query, value.jobID, BoundaryStart, value.component)
			if errors.Is(loadErr, ErrEffectNotFound) {
				stop, visitErr := visit(operation, intent, nil)
				if visitErr != nil {
					return visitErr
				}
				if stop {
					return nil
				}
				continue
			}
			if loadErr != nil {
				return loadErr
			}
			if err := validateReceiptForIntent(operation, intent, receipt, spec); err != nil {
				return err
			}
			stop, visitErr := visit(operation, intent, &receipt)
			if visitErr != nil {
				return visitErr
			}
			if stop {
				return nil
			}
		}
		if len(batch) < 32 {
			return nil
		}
		afterJob, afterComponent = batch[len(batch)-1].jobID, batch[len(batch)-1].component
	}
}

// ApplicationUnresolved is a controller fence read. Its bounded history scan
// is not a cross-process atomic snapshot and requires caller-held shared
// effects exclusion when the result controls an effect.
func (r *Repository) ApplicationUnresolved(ctx context.Context, applicationID string) (bool, error) {
	if applicationID == "" {
		return false, ErrInvalidOperation
	}
	var revision, currentExists int64
	err := r.db.QueryRowContext(ctx, `SELECT h.revision_number,EXISTS(
		SELECT 1 FROM application_lifecycle_operations o
		WHERE o.app_id=h.app_id AND o.desired_revision=h.revision_number)
		FROM application_desired_lifecycle_heads h WHERE h.app_id=?`, applicationID).Scan(&revision, &currentExists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrApplicationNotFound
	}
	if err != nil {
		return true, err
	}
	if revision == 0 {
		return false, nil
	}
	if currentExists == 0 {
		return true, nil
	}
	return r.anyUnresolvedOperation(ctx, applicationID)
}

// RebindExcluded is a controller fence read. Its bounded history scan is not a
// cross-process atomic snapshot and requires caller-held shared effects
// exclusion when the result controls a rebind.
func (r *Repository) RebindExcluded(ctx context.Context) (bool, error) {
	var missingCurrent int
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM application_desired_lifecycle_heads h
		LEFT JOIN application_lifecycle_operations current
		 ON current.app_id=h.app_id AND current.desired_revision=h.revision_number
		WHERE h.revision_number>0 AND current.job_id IS NULL)`).Scan(&missingCurrent)
	if err != nil {
		return true, err
	}
	if missingCurrent != 0 {
		return true, nil
	}
	return r.anyUnresolvedOperation(ctx, "")
}

type effectSpec struct {
	key     EffectKey
	mode    EffectMode
	outcome Outcome
}

func effectSpecs(operation Operation) ([]effectSpec, error) {
	components := operation.Pins.Components
	result := make([]effectSpec, 0, 2*len(components)+4)
	add := func(boundary Boundary, component string, mode EffectMode, outcome Outcome) {
		result = append(result, effectSpec{key: EffectKey{Boundary: boundary, ComponentName: component}, mode: mode, outcome: outcome})
	}
	if !(operation.Action == Start && operation.Pins.BaselineDisposition == BaselineRunning) {
		add(BoundaryWithdraw, "", modeForOperation(operation, BoundaryWithdraw), OutcomeRoutesAbsent)
	}
	if operation.Action == Stop || operation.Action == Restart && operation.Pins.BaselineDisposition == BaselineRunning {
		for _, component := range components {
			add(BoundaryStop, component.Name, modeForOperation(operation, BoundaryStop), OutcomeContainerStopped)
		}
	}
	if operation.Action == Restart || operation.Action == Start && operation.Pins.BaselineDisposition == BaselineStopped {
		for _, component := range components {
			add(BoundaryStart, component.Name, EffectCommand, OutcomeContainerRunning)
		}
	}
	if operation.Action == Start || operation.Action == Restart {
		add(BoundaryHealth, "", EffectObservationOnly, OutcomeHealthy)
		add(BoundaryPublish, "", EffectCommand, OutcomeServing)
	}
	switch operation.Action {
	case Stop:
		add(BoundaryComplete, "", EffectObservationOnly, OutcomeCompleteStopped)
	case Start, Restart:
		add(BoundaryComplete, "", EffectObservationOnly, OutcomeCompleteRunning)
	default:
		return nil, ErrInvalidOperation
	}
	return result, nil
}

func effectSpecFor(operation Operation, key EffectKey) (effectSpec, bool) {
	specs, err := effectSpecs(operation)
	if err != nil {
		return effectSpec{}, false
	}
	for _, spec := range specs {
		if spec.key == key {
			return spec, true
		}
	}
	return effectSpec{}, false
}

func modeForOperation(operation Operation, boundary Boundary) EffectMode {
	if boundary == BoundaryHealth || boundary == BoundaryComplete ||
		operation.Pins.BaselineDisposition == BaselineStopped && (boundary == BoundaryWithdraw || boundary == BoundaryStop) {
		return EffectObservationOnly
	}
	return EffectCommand
}

func sameIntentIdentity(left, right Intent) bool {
	if left.JobID != right.JobID || left.Key != right.Key || left.RequestDigest != right.RequestDigest {
		return false
	}
	_, digest, err := canonicalEffectRequest(right.Request)
	return err == nil && digest == right.RequestDigest
}

func validateIntentForOperation(operation Operation, intent Intent, spec effectSpec) error {
	if intent.JobID != operation.JobID || intent.Key != spec.key || intent.Request.Boundary != spec.key.Boundary ||
		intent.Request.ComponentName != spec.key.ComponentName || intent.Request.Mode != spec.mode ||
		!effectRequestMatchesOperation(intent.Request, operation) {
		return ErrInvalidEffect
	}
	return nil
}

func validateReceiptForIntent(operation Operation, intent Intent, receipt Receipt, spec effectSpec) error {
	proof := receipt.Proof
	if receipt.JobID != operation.JobID || receipt.Key != spec.key || receipt.Outcome != spec.outcome ||
		proof.OperationID != operation.JobID || proof.ApplicationID != operation.ApplicationID ||
		proof.DesiredRevision != operation.DesiredRevision || proof.Boundary != spec.key.Boundary ||
		proof.ComponentName != spec.key.ComponentName || proof.Outcome != spec.outcome ||
		proof.IntentDigest != intent.RequestDigest || proof.PinsDigest != operation.PinsDigest {
		return ErrInvalidEffect
	}
	return nil
}

func (r *Repository) completedOperation(ctx context.Context, operation Operation) (bool, error) {
	return completedOperationQuery(ctx, r.db, operation)
}

func completedOperationQuery(ctx context.Context, query lifecycleQuery, operation Operation) (bool, error) {
	specs, err := effectSpecs(operation)
	if err != nil {
		return false, err
	}
	type retainedEffect struct {
		hasIntent  bool
		hasReceipt bool
	}
	retained := make(map[EffectKey]retainedEffect, len(specs))
	rows, err := query.QueryContext(ctx, `SELECT boundary,component_name,'intent'
		FROM application_lifecycle_effect_intents WHERE job_id=?
		UNION ALL
		SELECT boundary,component_name,'receipt'
		FROM application_lifecycle_effect_receipts WHERE job_id=?`, operation.JobID, operation.JobID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var boundary, component, kind string
		if err := rows.Scan(&boundary, &component, &kind); err != nil {
			_ = rows.Close()
			return false, err
		}
		key := EffectKey{Boundary: Boundary(boundary), ComponentName: component}
		if _, expected := effectSpecFor(operation, key); !expected {
			_ = rows.Close()
			return false, ErrInvalidEffect
		}
		state := retained[key]
		switch kind {
		case "intent":
			if state.hasIntent {
				_ = rows.Close()
				return false, ErrInvalidEffect
			}
			state.hasIntent = true
		case "receipt":
			if state.hasReceipt {
				_ = rows.Close()
				return false, ErrInvalidEffect
			}
			state.hasReceipt = true
		default:
			_ = rows.Close()
			return false, ErrInvalidEffect
		}
		retained[key] = state
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return false, rowsErr
	}
	complete := true
	for _, spec := range specs {
		state := retained[spec.key]
		if state.hasReceipt && !state.hasIntent {
			return false, ErrInvalidEffect
		}
		if !state.hasIntent {
			complete = false
			continue
		}
		intent, loadErr := intentByKey(ctx, query, operation.JobID, spec.key.Boundary, spec.key.ComponentName)
		if loadErr != nil {
			return false, loadErr
		}
		if err := validateIntentForOperation(operation, intent, spec); err != nil {
			return false, err
		}
		if !state.hasReceipt {
			complete = false
			continue
		}
		receipt, loadErr := receiptByKey(ctx, query, operation.JobID, spec.key.Boundary, spec.key.ComponentName)
		if loadErr != nil {
			return false, loadErr
		}
		if err := validateReceiptForIntent(operation, intent, receipt, spec); err != nil {
			return false, err
		}
	}
	return complete, nil
}

func (r *Repository) anyUnresolvedOperation(ctx context.Context, applicationID string) (bool, error) {
	return anyUnresolvedOperationQuery(ctx, r.db, applicationID)
}

func anyUnresolvedOperationQuery(ctx context.Context, query lifecycleQuery, applicationID string) (bool, error) {
	var afterApp string
	var afterRevision int64
	for {
		statement := `SELECT app_id,desired_revision,job_id FROM application_lifecycle_operations
			WHERE (?='' OR app_id=?) AND (app_id>? OR (app_id=? AND desired_revision>?))
			ORDER BY app_id,desired_revision LIMIT 32`
		rows, err := query.QueryContext(ctx, statement, applicationID, applicationID, afterApp, afterApp, afterRevision)
		if err != nil {
			return true, err
		}
		type operationKey struct {
			appID    string
			revision int64
			jobID    string
		}
		batch := make([]operationKey, 0, 32)
		for rows.Next() {
			var value operationKey
			if err := rows.Scan(&value.appID, &value.revision, &value.jobID); err != nil {
				_ = rows.Close()
				return true, err
			}
			batch = append(batch, value)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return true, err
		}
		for _, value := range batch {
			operation, loadErr := operationByJob(ctx, query, value.jobID)
			if loadErr != nil {
				return true, loadErr
			}
			complete, loadErr := completedOperationQuery(ctx, query, operation)
			if loadErr != nil {
				return true, loadErr
			}
			if !complete {
				return true, nil
			}
		}
		if len(batch) < 32 {
			return false, nil
		}
		afterApp, afterRevision = batch[len(batch)-1].appID, batch[len(batch)-1].revision
	}
}

type lifecycleQuery interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func effectRequestMatchesOperation(request EffectRequestV1, operation Operation) bool {
	return request.OperationID == operation.JobID && request.ApplicationID == operation.ApplicationID &&
		request.DesiredRevision == operation.DesiredRevision && request.PinsDigest == operation.PinsDigest
}

func intentByKey(ctx context.Context, query queryRower, jobID string, boundary Boundary, component string) (Intent, error) {
	var intent Intent
	var boundaryValue, requestJSON, createdAt string
	err := query.QueryRowContext(ctx, `SELECT job_id,boundary,component_name,writer_attempt,request_json,request_digest,created_at
		FROM application_lifecycle_effect_intents WHERE job_id=? AND boundary=? AND component_name=?`,
		jobID, string(boundary), component).Scan(&intent.JobID, &boundaryValue, &intent.Key.ComponentName,
		&intent.WriterAttempt, &requestJSON, &intent.RequestDigest, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Intent{}, ErrEffectNotFound
	}
	if err != nil {
		return Intent{}, err
	}
	intent.Key.Boundary = Boundary(boundaryValue)
	if err := decodeCanonical([]byte(requestJSON), &intent.Request, func() ([]byte, string, error) {
		return canonicalEffectRequest(intent.Request)
	}, intent.RequestDigest); err != nil {
		return Intent{}, wrapInvalid("decode lifecycle intent", err)
	}
	intent.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Intent{}, fmt.Errorf("parse lifecycle intent timestamp: %v: %w", err, ErrInvalidEffect)
	}
	return intent, nil
}

func receiptByKey(ctx context.Context, query queryRower, jobID string, boundary Boundary, component string) (Receipt, error) {
	var receipt Receipt
	var boundaryValue, outcomeValue, proofJSON, observedAt string
	err := query.QueryRowContext(ctx, `SELECT job_id,boundary,component_name,writer_attempt,outcome,proof_json,proof_digest,observed_at
		FROM application_lifecycle_effect_receipts WHERE job_id=? AND boundary=? AND component_name=?`,
		jobID, string(boundary), component).Scan(&receipt.JobID, &boundaryValue, &receipt.Key.ComponentName,
		&receipt.WriterAttempt, &outcomeValue, &proofJSON, &receipt.ProofDigest, &observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrEffectNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	receipt.Key.Boundary, receipt.Outcome = Boundary(boundaryValue), Outcome(outcomeValue)
	if err := decodeCanonical([]byte(proofJSON), &receipt.Proof, func() ([]byte, string, error) {
		return canonicalEffectProof(receipt.Proof)
	}, receipt.ProofDigest); err != nil {
		return Receipt{}, wrapInvalid("decode lifecycle receipt", err)
	}
	receipt.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return Receipt{}, fmt.Errorf("parse lifecycle receipt timestamp: %v: %w", err, ErrInvalidEffect)
	}
	return receipt, nil
}

func failJournalSavepoint(tx *sql.Tx, cause error) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, rollbackErr := tx.ExecContext(cleanupContext, `ROLLBACK TO SAVEPOINT application_lifecycle_journal`)
	cancel()
	if rollbackErr != nil {
		return errors.Join(cause, invalidateAuthorTransaction(tx, rollbackErr))
	}
	cleanupContext, cancel = context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, releaseErr := tx.ExecContext(cleanupContext, `RELEASE SAVEPOINT application_lifecycle_journal`)
	cancel()
	if releaseErr != nil {
		return errors.Join(cause, invalidateAuthorTransaction(tx, releaseErr))
	}
	return cause
}

func releaseJournalSavepoint(tx *sql.Tx) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), authorSavepointStatementTimeout)
	_, err := tx.ExecContext(cleanupContext, `RELEASE SAVEPOINT application_lifecycle_journal`)
	cancel()
	if err != nil {
		return invalidateAuthorTransaction(tx, err)
	}
	return nil
}
