package applifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

// DeploymentBindingValidator adapts the single canonical lifecycle codecs and
// finite graph validation to caller-owned deployment binding transactions.
type DeploymentBindingValidator struct {
	repository *Repository
}

func NewDeploymentBindingValidator(repository *Repository) (*DeploymentBindingValidator, error) {
	if repository == nil || repository.db == nil {
		return nil, deploymentbinding.ErrInvalidConfiguration
	}
	return &DeploymentBindingValidator{repository: repository}, nil
}

func (v *DeploymentBindingValidator) ConfiguredFor(db *sql.DB) bool {
	return v != nil && v.repository != nil && v.repository.db != nil && v.repository.db == db
}

func (v *DeploymentBindingValidator) HistoricalBindingTx(ctx context.Context, tx *sql.Tx, ref deploymentbinding.OperationRef) error {
	operation, err := v.validateOperationRefTx(ctx, tx, ref)
	if err != nil {
		return err
	}
	complete, err := completedOperationQuery(ctx, tx, operation)
	if err != nil {
		return lifecycleBindingValidationError(ctx, err)
	}
	if !complete {
		return deploymentbinding.ErrInvalidBinding
	}
	return nil
}

func (v *DeploymentBindingValidator) CurrentOperationTx(ctx context.Context, tx *sql.Tx, ref deploymentbinding.OperationRef) (deploymentbinding.Completion, error) {
	operation, err := v.validateOperationRefTx(ctx, tx, ref)
	if err != nil {
		return 0, err
	}
	complete, err := completedOperationQuery(ctx, tx, operation)
	if err != nil {
		return 0, lifecycleBindingValidationError(ctx, err)
	}
	if complete {
		return deploymentbinding.CompletionComplete, nil
	}
	return deploymentbinding.CompletionIncomplete, nil
}

func (v *DeploymentBindingValidator) UnresolvedHistoryTx(ctx context.Context, tx *sql.Tx, applicationID string) (bool, error) {
	if err := v.ready(ctx, tx); err != nil {
		return false, err
	}
	if applicationID == "" {
		return false, deploymentbinding.ErrInvalidBinding
	}
	if err := v.validateHistoryIdentitiesTx(ctx, tx, applicationID); err != nil {
		return false, err
	}
	unresolved, err := anyUnresolvedOperationQuery(ctx, tx, applicationID)
	if err != nil {
		return false, lifecycleBindingValidationError(ctx, err)
	}
	return unresolved, nil
}

func (v *DeploymentBindingValidator) validateHistoryIdentitiesTx(ctx context.Context, tx *sql.Tx, applicationID string) error {
	var missingOperation int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM application_desired_lifecycle_transitions t
		LEFT JOIN application_lifecycle_operations o
		  ON o.app_id=t.app_id AND o.desired_revision=t.revision_number AND o.job_id=t.job_id
		WHERE t.app_id=? AND o.job_id IS NULL
	)`, applicationID).Scan(&missingOperation); err != nil {
		return lifecycleBindingStorageError(ctx, err)
	}
	if missingOperation != 0 {
		return deploymentbinding.ErrInvalidBinding
	}
	var afterRevision int64
	for {
		rows, err := tx.QueryContext(ctx, `SELECT job_id,desired_revision
			FROM application_lifecycle_operations
			WHERE app_id=? AND desired_revision>?
			ORDER BY desired_revision LIMIT 32`, applicationID, afterRevision)
		if err != nil {
			return lifecycleBindingStorageError(ctx, err)
		}
		type operationIdentity struct {
			jobID    string
			revision int64
		}
		batch := make([]operationIdentity, 0, 32)
		for rows.Next() {
			var identity operationIdentity
			if err := rows.Scan(&identity.jobID, &identity.revision); err != nil {
				_ = rows.Close()
				return lifecycleBindingStorageError(ctx, err)
			}
			batch = append(batch, identity)
		}
		rowsErr := rows.Err()
		_ = rows.Close()
		if rowsErr != nil {
			return lifecycleBindingStorageError(ctx, rowsErr)
		}
		for _, identity := range batch {
			if _, err := v.validateOperationRefTx(ctx, tx, deploymentbinding.OperationRef{
				ApplicationID: applicationID, OperationID: identity.jobID, DesiredRevision: identity.revision,
			}); err != nil {
				return err
			}
		}
		if len(batch) < 32 {
			return nil
		}
		afterRevision = batch[len(batch)-1].revision
	}
}

func (v *DeploymentBindingValidator) validateOperationRefTx(ctx context.Context, tx *sql.Tx, ref deploymentbinding.OperationRef) (Operation, error) {
	if err := v.ready(ctx, tx); err != nil {
		return Operation{}, err
	}
	if ref.ApplicationID == "" || ref.OperationID == "" || ref.DesiredRevision < 1 {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}
	operation, err := operationByJob(ctx, tx, ref.OperationID)
	if err != nil {
		if canceled := lifecycleBindingContextError(ctx, err); canceled != nil {
			return Operation{}, canceled
		}
		var exists int
		if lookupErr := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM application_lifecycle_operations WHERE job_id=?)`, ref.OperationID).Scan(&exists); lookupErr != nil {
			return Operation{}, lifecycleBindingStorageError(ctx, lookupErr)
		}
		if exists != 0 || errors.Is(err, ErrOperationNotFound) {
			return Operation{}, deploymentbinding.ErrInvalidBinding
		}
		return Operation{}, lifecycleBindingValidationError(ctx, err)
	}
	if operation.JobID != ref.OperationID || operation.ApplicationID != ref.ApplicationID ||
		operation.DesiredRevision != ref.DesiredRevision || operation.ExpectedRevision+1 != operation.DesiredRevision {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}
	target, err := targetState(operation.Action)
	if err != nil {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}

	var transitionJobID, transitionAction, transitionActor, desiredState string
	var previousState sql.NullString
	var transitionExpected int64
	err = tx.QueryRowContext(ctx, `SELECT job_id,action,actor_id,expected_revision,previous_state,desired_state
		FROM application_desired_lifecycle_transitions
		WHERE app_id=? AND revision_number=?`, operation.ApplicationID, operation.DesiredRevision).Scan(
		&transitionJobID, &transitionAction, &transitionActor, &transitionExpected, &previousState, &desiredState)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}
	if err != nil {
		return Operation{}, lifecycleBindingStorageError(ctx, err)
	}
	if transitionJobID != operation.JobID || transitionAction != string(operation.Action) ||
		transitionActor != operation.ActorID || transitionExpected != operation.ExpectedRevision || desiredState != string(target) {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}
	if operation.ExpectedRevision == 0 {
		if previousState.Valid {
			return Operation{}, deploymentbinding.ErrInvalidBinding
		}
	} else {
		var priorDesired string
		err := tx.QueryRowContext(ctx, `SELECT desired_state
			FROM application_desired_lifecycle_transitions
			WHERE app_id=? AND revision_number=?`, operation.ApplicationID, operation.ExpectedRevision).Scan(&priorDesired)
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, deploymentbinding.ErrInvalidBinding
		}
		if err != nil {
			return Operation{}, lifecycleBindingStorageError(ctx, err)
		}
		if !previousState.Valid || previousState.String != priorDesired {
			return Operation{}, deploymentbinding.ErrInvalidBinding
		}
	}

	var jobType, resourceType, resourceID, actorID, idempotencyKey, inputJSON string
	err = tx.QueryRowContext(ctx, `SELECT type,resource_type,resource_id,
		COALESCE(requested_by,''),COALESCE(idempotency_key,''),input_json
		FROM jobs WHERE id=?`, operation.JobID).Scan(
		&jobType, &resourceType, &resourceID, &actorID, &idempotencyKey, &inputJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}
	if err != nil {
		return Operation{}, lifecycleBindingStorageError(ctx, err)
	}
	if jobType != string(operation.Action) || resourceType != "application" || resourceID != operation.ApplicationID ||
		actorID != operation.ActorID || idempotencyKey != operation.IdempotencyKey || inputJSON != "{}" {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}

	var version int
	var runtimeStrategy, baseline, deploymentID, releaseID, slot string
	var activeGeneration, planNumber, configurationNumber int64
	var planID, configurationID string
	var configurationFormat int
	err = tx.QueryRowContext(ctx, `SELECT version,runtime_strategy,baseline_disposition,
		deployment_id,active_generation,release_id,slot,
		plan_revision_id,plan_revision_number,configuration_revision_id,
		configuration_revision_number,configuration_format
		FROM application_lifecycle_operations WHERE job_id=?`, operation.JobID).Scan(
		&version, &runtimeStrategy, &baseline, &deploymentID, &activeGeneration, &releaseID, &slot,
		&planID, &planNumber, &configurationID, &configurationNumber, &configurationFormat)
	if err != nil {
		return Operation{}, lifecycleBindingStorageError(ctx, err)
	}
	pins := operation.Pins
	if version != 1 || runtimeStrategy != "generated_node" || baseline != string(pins.BaselineDisposition) ||
		deploymentID != pins.DeploymentID || activeGeneration != pins.ActiveGeneration || releaseID != pins.ReleaseID || slot != pins.Slot ||
		planID != pins.PlanRevision.ID || planNumber != pins.PlanRevision.Number ||
		configurationID != pins.Configuration.RevisionID || configurationNumber != pins.Configuration.RevisionNumber ||
		configurationFormat != pins.Configuration.FormatVersion {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}

	rows, err := tx.QueryContext(ctx, `SELECT ordinal,component_name
		FROM application_lifecycle_operation_components WHERE job_id=? ORDER BY ordinal`, operation.JobID)
	if err != nil {
		return Operation{}, lifecycleBindingStorageError(ctx, err)
	}
	componentNames := make([]string, 0, 2)
	for rows.Next() {
		var ordinal int
		var name string
		if err := rows.Scan(&ordinal, &name); err != nil {
			_ = rows.Close()
			return Operation{}, lifecycleBindingStorageError(ctx, err)
		}
		if ordinal != len(componentNames) {
			_ = rows.Close()
			return Operation{}, deploymentbinding.ErrInvalidBinding
		}
		componentNames = append(componentNames, name)
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return Operation{}, lifecycleBindingStorageError(ctx, rowsErr)
	}
	if len(componentNames) != len(pins.Components) {
		return Operation{}, deploymentbinding.ErrInvalidBinding
	}
	for index, name := range componentNames {
		if name != pins.Components[index].Name || name != pins.Configuration.RuntimeComponents[index] {
			return Operation{}, deploymentbinding.ErrInvalidBinding
		}
	}
	return operation, nil
}

func (v *DeploymentBindingValidator) ready(ctx context.Context, tx *sql.Tx) error {
	if v == nil || v.repository == nil || v.repository.db == nil || tx == nil {
		return deploymentbinding.ErrInvalidConfiguration
	}
	return lifecycleBindingContextError(ctx, nil)
}

func lifecycleBindingValidationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if canceled := lifecycleBindingContextError(ctx, err); canceled != nil {
		return canceled
	}
	for _, invalid := range []error{ErrInvalidOperation, ErrOperationNotFound, ErrMissingOperation, ErrInvalidPins, ErrInvalidEffect, ErrEffectConflict, ErrEffectNotFound} {
		if errors.Is(err, invalid) {
			return deploymentbinding.ErrInvalidBinding
		}
	}
	if errors.Is(err, sql.ErrTxDone) {
		return lifecycleBindingStorageError(ctx, err)
	}
	return lifecycleBindingStorageError(ctx, err)
}

func lifecycleBindingStorageError(ctx context.Context, err error) error {
	if canceled := lifecycleBindingContextError(ctx, err); canceled != nil {
		return canceled
	}
	return fmt.Errorf("%w: %v", deploymentbinding.ErrUnavailable, err)
}

func lifecycleBindingContextError(ctx context.Context, err error) error {
	if ctx == nil {
		return deploymentbinding.ErrInvalidConfiguration
	}
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("%w: %w", deploymentbinding.ErrCanceled, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", deploymentbinding.ErrCanceled, err)
	}
	return nil
}

var _ deploymentbinding.LifecycleTxValidator = (*DeploymentBindingValidator)(nil)
