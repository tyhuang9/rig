package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// Emergency withdrawal acquires its own effects lease. Release startup's
// admission first and retain any release error even if withdrawal succeeds.
func runGatewayStartupEmergencyStop(admission *deploymentEffectsStartupLease, stop func(context.Context) error) error {
	if stop == nil {
		return errors.New("gateway emergency stop is required")
	}
	releaseErr := admission.Release()
	if releaseErr != nil {
		releaseErr = fmt.Errorf("release startup admission before gateway emergency stop: %w", releaseErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(releaseErr, stop(ctx))
}

// No SQL is needed when database startup fails. Protected current ownership
// must be completely accounted for before considering native gateway history.
func stopOwnedGatewayOnStartupFailure(ctx context.Context, cfg config.Config, dockerExecutable string,
	directories docker.ControllerDirectories,
) error {
	options := generatedingress.Options{
		DockerExecutable: dockerExecutable, DockerEndpoint: cfg.DockerEndpoint,
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		DataRoot: cfg.DataRoot,
	}
	return stopOwnedGatewayWithStartupHistory(ctx,
		func(ctx context.Context) (generatedingress.GatewayCurrentStartupEmergencyStopResult, error) {
			return generatedingress.StopOwnedGatewayCurrentOnStartupFailure(ctx, runtimeprocess.ExecRunner{}, options)
		}, func(ctx context.Context) error {
			return generatedingress.StopOwnedGatewayV2OnStartupFailure(ctx, runtimeprocess.ExecRunner{}, options)
		})
}

func stopOwnedGatewayWithStartupHistory(ctx context.Context,
	current func(context.Context) (generatedingress.GatewayCurrentStartupEmergencyStopResult, error),
	native func(context.Context) error,
) error {
	if ctx == nil || current == nil || native == nil {
		return errors.New("gateway emergency ownership dependencies are required")
	}
	result, err := current(ctx)
	if err != nil {
		return fmt.Errorf("withdraw protected current gateway: %w", err)
	}
	if ctx.Err() != nil || result.Incomplete || result.OwnershipIndeterminate || result.UnresolvedProtectedRebindHistory ||
		result.VerifiedTargets < 0 || result.StoppedOrAbsentTargets < 0 ||
		result.VerifiedTargets != result.StoppedOrAbsentTargets {
		return errors.New("protected current gateway withdrawal is unresolved")
	}
	if result.RebindOwnershipPresent {
		if !result.ProtectedRebindHistoryPresent || result.VerifiedTargets == 0 {
			return errors.New("protected current gateway withdrawal lacks ownership evidence")
		}
		return nil
	}
	if result.VerifiedTargets != 0 {
		return errors.New("protected current gateway withdrawal has unassociated targets")
	}
	// This includes fully retained abort history with no possible successor
	// effects. The native path must independently prove its exact ownership.
	return native(ctx)
}
