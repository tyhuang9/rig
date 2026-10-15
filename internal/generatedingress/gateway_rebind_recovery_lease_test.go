package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindRecoveryLeaseRepository struct {
	*gatewayRebindReconcileTestRepository
	reads int
}

func (r *gatewayRebindRecoveryLeaseRepository) HostingGatewayStartupSnapshot(ctx context.Context) (
	appaccess.HostingGatewayStartupSnapshot, error,
) {
	r.reads++
	return r.gatewayRebindReconcileTestRepository.HostingGatewayStartupSnapshot(ctx)
}

func TestGatewayRebindRecoveryPreviewOwnsActualEffectsLease(t *testing.T) {
	fixture, _, _ := newGatewayRebindTypedForwardOnlyFixture(t)
	manager := fixture.predecessor.manager
	readCurrent := gatewayRebindReconcileControlledCurrentReader(t, fixture)
	gatewayRebindReconcileForwardReview(t, fixture, fixture.predecessor.repository, readCurrent)
	repository := &gatewayRebindRecoveryLeaseRepository{
		gatewayRebindReconcileTestRepository: &gatewayRebindReconcileTestRepository{
			Repository: fixture.predecessor.repository,
		},
	}
	before, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner.effects = nil
	realAcquire := gatewayRebindAcquireDeploymentEffects
	operationID := fixture.attempt.Claim.Spec.OperationID

	t.Run("waits before gateway lock or review reads", func(t *testing.T) {
		release, err := realAcquire(context.Background(), manager.options.WorkingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := release(); err != nil {
				t.Error(err)
			}
		}()
		attempted := make(chan struct{}, 1)
		realGateway := managerAcquireGatewayOSLock
		gatewayCalls, sessionCalls := 0, 0
		gatewayRebindAcquireDeploymentEffects = func(ctx context.Context, directory string) (func() error, error) {
			attempted <- struct{}{}
			return realAcquire(ctx, directory)
		}
		managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
			gatewayCalls++
			return realGateway(ctx, store)
		}
		t.Cleanup(func() {
			gatewayRebindAcquireDeploymentEffects = realAcquire
			managerAcquireGatewayOSLock = realGateway
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		type outcome struct {
			review GatewayRebindRecoveryReview
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			review, err := manager.InspectGatewayRebindRecoveryReview(ctx, repository, operationID,
				gatewayRebindTestAdministrator, func(context.Context) error {
					sessionCalls++
					return nil
				})
			done <- outcome{review: review, err: err}
		}()
		select {
		case <-attempted:
			cancel()
			result := <-done
			if result.err == nil || !reflect.DeepEqual(result.review, GatewayRebindRecoveryReview{}) ||
				gatewayCalls != 0 || sessionCalls != 0 || repository.reads != 0 {
				t.Fatalf("held effects lease was crossed: gateway=%d session=%d reads=%d error=%v",
					gatewayCalls, sessionCalls, repository.reads, result.err)
			}
		case result := <-done:
			t.Fatalf("preview did not attempt the effects lease: %v", result.err)
		}
	})

	t.Run("holds through session check and releases on refusal", func(t *testing.T) {
		stop := errors.New("stop after observing the held preview lease")
		sessionCalls := 0
		contended := false
		review, err := manager.InspectGatewayRebindRecoveryReview(context.Background(), repository, operationID,
			gatewayRebindTestAdministrator, func(context.Context) error {
				sessionCalls++
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				release, acquireErr := realAcquire(ctx, manager.options.WorkingDirectory)
				contended = release == nil && errors.Is(acquireErr, context.DeadlineExceeded)
				if release != nil {
					if releaseErr := release(); releaseErr != nil {
						t.Error(releaseErr)
					}
				}
				return stop
			})
		if err == nil || !reflect.DeepEqual(review, GatewayRebindRecoveryReview{}) ||
			!contended || sessionCalls != 1 || repository.reads != 0 {
			t.Fatalf("preview lease was not held: contended=%t sessions=%d reads=%d error=%v",
				contended, sessionCalls, repository.reads, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		release, err := realAcquire(ctx, manager.options.WorkingDirectory)
		if err != nil {
			t.Fatalf("preview did not release effects lease: %v", err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
	})

	after, snapshotErr := repository.GatewayRebindRecoverySnapshot(context.Background())
	afterFiles, filesErr := readGatewayHistorySnapshotMode(manager.store, true)
	if snapshotErr != nil || filesErr != nil || !reflect.DeepEqual(before, after) ||
		!sameGatewayHistorySnapshot(files, afterFiles) || repository.authorizeCall != 0 ||
		len(fixture.runner.effects) != 0 || manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("preview contention changed recovery state: audit=%d effects=%d snapshot_error=%v files_error=%v",
			repository.authorizeCall, len(fixture.runner.effects), snapshotErr, filesErr)
	}
}
