package applifecycle

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hostd/hostd/internal/deploymentexecution"
)

// PositiveFenceValidator adapts the canonical lifecycle journal to a
// caller-owned ordinary-deployment authority transaction.
type PositiveFenceValidator struct {
	repository *Repository
}

func NewPositiveFenceValidator(repository *Repository) (*PositiveFenceValidator, error) {
	if repository == nil || repository.db == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return &PositiveFenceValidator{repository: repository}, nil
}

func (v *PositiveFenceValidator) ConfiguredFor(db *sql.DB) bool {
	return v != nil && v.repository != nil && v.repository.db != nil && v.repository.db == db
}

func (v *PositiveFenceValidator) PendingStartExistsTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	if !v.ConfiguredFor(v.database()) || tx == nil {
		return true, deploymentexecution.ErrInvalidConfiguration
	}
	if err := ctxError(ctx, nil); err != nil {
		return true, err
	}
	pending, err := pendingStartExistsQuery(ctx, tx)
	if err != nil {
		return true, positiveFenceError(ctx, err)
	}
	if err := ctxError(ctx, nil); err != nil {
		return true, err
	}
	return pending, nil
}

func (v *PositiveFenceValidator) database() *sql.DB {
	if v == nil || v.repository == nil {
		return nil
	}
	return v.repository.db
}

func positiveFenceError(ctx context.Context, err error) error {
	if canceled := ctxError(ctx, err); canceled != nil {
		return canceled
	}
	for _, invalid := range []error{ErrInvalidOperation, ErrOperationNotFound, ErrMissingOperation, ErrInvalidPins, ErrInvalidEffect, ErrEffectConflict, ErrEffectNotFound} {
		if errors.Is(err, invalid) {
			return errors.Join(deploymentexecution.ErrAuthorityInvalid, invalid)
		}
	}
	if errors.Is(err, sql.ErrTxDone) {
		return deploymentexecution.ErrUnavailable
	}
	return deploymentexecution.ErrUnavailable
}

func ctxError(ctx context.Context, err error) error {
	if ctx == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	if ctx.Err() != nil {
		return errors.Join(deploymentexecution.ErrCanceled, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(deploymentexecution.ErrCanceled, err)
	}
	return nil
}

var _ deploymentexecution.PositiveFenceTxValidator = (*PositiveFenceValidator)(nil)
