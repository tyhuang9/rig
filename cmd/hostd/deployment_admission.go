package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

// deploymentEffectsAdmission holds the cross-process lease before checking a
// fresh SQLite fence. A deployment worker takes it before job assignment and
// keeps it through executor cleanup and any resulting job-state persistence.
// Owner shutdown may leave a nonterminal job for startup recovery. A future
// rebind writer must take the same lease before the gateway lock and its
// SQLite write transaction, then inspect nonterminal jobs before proceeding.
func deploymentEffectsAdmission(db *sql.DB, workingDirectory string) (func(context.Context) (func() error, error), error) {
	if db == nil || workingDirectory == "" {
		return nil, errors.New("deployment effects admission dependencies are required")
	}
	checkFence := rebindFenceCheck(db)
	return func(ctx context.Context) (func() error, error) {
		release, err := deploymenteffects.Acquire(ctx, workingDirectory)
		if err != nil {
			return nil, err
		}
		if err := checkFence(ctx); err != nil {
			if releaseErr := release(); releaseErr != nil {
				return nil, errors.Join(err, fmt.Errorf("release deployment effects lease: %w", releaseErr))
			}
			return nil, err
		}
		return release, nil
	}, nil
}

// deploymentEffectsScopedAdmission binds the live OS-backed lease to the
// exact worker context. The legacy admission above remains the startup gate;
// only the worker path needs retained-effect scope authority.
func deploymentEffectsScopedAdmission(db *sql.DB, workingDirectory string) (jobs.ScopedAdmission, error) {
	if db == nil || workingDirectory == "" {
		return nil, errors.New("deployment effects admission dependencies are required")
	}
	checkFence := rebindFenceCheck(db)
	return func(ctx context.Context) (context.Context, func() error, error) {
		scope, release, err := deploymenteffects.AcquireScoped(ctx, workingDirectory)
		if err != nil {
			return nil, nil, err
		}
		if err := checkFence(ctx); err != nil {
			if releaseErr := release(); releaseErr != nil {
				return nil, nil, errors.Join(err, fmt.Errorf("release deployment effects lease: %w", releaseErr))
			}
			return nil, nil, err
		}
		if err := ctx.Err(); err != nil {
			if releaseErr := release(); releaseErr != nil {
				return nil, nil, errors.Join(err, fmt.Errorf("release deployment effects lease: %w", releaseErr))
			}
			return nil, nil, err
		}
		return deploymenteffects.WithScope(ctx, scope), release, nil
	}, nil
}
